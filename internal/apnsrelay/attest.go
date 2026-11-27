package apnsrelay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

//go:embed apple_app_attestation_root_ca.pem
var appleAppAttestRootCAPEM []byte

var (
	oidAppleAppAttestNonce = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 8, 2}

	aaguidProduction  = []byte{'a', 'p', 'p', 'a', 't', 't', 'e', 's', 't', 0, 0, 0, 0, 0, 0, 0}
	aaguidDevelopment = []byte("appattestdevelop")
	aaguidSandbox     = []byte("appattestsandbox")

	ErrAttestationFailed = errors.New("app attest verification failed")
	ErrAssertionFailed   = errors.New("app attest assertion failed")
)

const (
	AttestEnvProduction  = "production"
	AttestEnvDevelopment = "development"

	OpAttestRegister         = "attest.register"
	OpGrantCreate            = "grant.create"
	OpGrantRefresh           = "grant.refresh"
	OpGrantRevoke            = "grant.revoke"
	OpGrantCancelByClientKey = "grant.cancel_by_client_key"
	OpDeviceUpdateToken      = "device.update_token"
)

func ValidAttestOp(op string) bool {
	switch op {
	case OpAttestRegister, OpGrantCreate, OpGrantRefresh, OpGrantRevoke, OpGrantCancelByClientKey, OpDeviceUpdateToken:
		return true
	default:
		return false
	}
}

// DefaultAppleRootCAPool returns the embedded Apple App Attestation Root CA pool.
func DefaultAppleRootCAPool() (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(appleAppAttestRootCAPEM) {
		return nil, errors.New("failed to parse embedded Apple App Attestation Root CA")
	}
	return pool, nil
}

// CanonicalRegisterPayload constructs the deterministic JSON bytes signed via
// SHA-256 into clientDataHash during key attestation.
func CanonicalRegisterPayload(appID, keyID, challengeID, nonceB64 string) []byte {
	type payload struct {
		AppID       string `json:"app_id"`
		ChallengeID string `json:"challenge_id"`
		Nonce       string `json:"nonce"`
		Op          string `json:"op"`
		ResourceID  string `json:"resource_id"`
	}
	raw, _ := json.Marshal(payload{
		AppID:       appID,
		Op:          OpAttestRegister,
		ResourceID:  keyID,
		ChallengeID: challengeID,
		Nonce:       nonceB64,
	})
	return raw
}

// VerifiedAttestation holds the validated App Attest public key metadata.
type VerifiedAttestation struct {
	KeyID         string
	PublicKeySPKI []byte
	AttestEnv     string
	Receipt       []byte
}

// AppAttestVerifier verifies Apple App Attest attestations and assertions.
type AppAttestVerifier struct {
	RootPool *x509.CertPool
	Now      func() time.Time
}

func NewAppAttestVerifier() (*AppAttestVerifier, error) {
	pool, err := DefaultAppleRootCAPool()
	if err != nil {
		return nil, err
	}
	return &AppAttestVerifier{RootPool: pool, Now: time.Now}, nil
}

func (v *AppAttestVerifier) now() time.Time {
	if v != nil && v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// VerifyAttestation validates an Apple App Attest attestationObject per
// Apple's "Validating Apps That Connect to Your Server" specification.
func (v *AppAttestVerifier) VerifyAttestation(appleAppID, keyID string, clientDataHash, attestationObj []byte, allowSandbox bool) (*VerifiedAttestation, error) {
	if v == nil || v.RootPool == nil || appleAppID == "" || keyID == "" || len(clientDataHash) != sha256.Size {
		return nil, ErrAttestationFailed
	}
	keyIDBytes, err := decodeAppAttestKeyID(keyID)
	if err != nil || len(keyIDBytes) != sha256.Size {
		return nil, fmt.Errorf("%w: invalid key_id", ErrAttestationFailed)
	}

	decoded, err := decodeCBOR(attestationObj)
	if err != nil {
		return nil, fmt.Errorf("%w: cbor: %v", ErrAttestationFailed, err)
	}
	top, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: root not map", ErrAttestationFailed)
	}
	if fmtStr, _ := top["fmt"].(string); fmtStr != "apple-appattest" {
		return nil, fmt.Errorf("%w: unsupported fmt", ErrAttestationFailed)
	}
	authData, ok := top["authData"].([]byte)
	if !ok {
		return nil, fmt.Errorf("%w: missing authData", ErrAttestationFailed)
	}
	attStmt, ok := top["attStmt"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: missing attStmt", ErrAttestationFailed)
	}
	receipt, ok := attStmt["receipt"].([]byte)
	if !ok || len(receipt) == 0 {
		return nil, fmt.Errorf("%w: missing receipt", ErrAttestationFailed)
	}
	x5cAny, ok := attStmt["x5c"].([]any)
	if !ok || len(x5cAny) < 2 {
		return nil, fmt.Errorf("%w: missing x5c chain", ErrAttestationFailed)
	}

	certs := make([]*x509.Certificate, 0, len(x5cAny))
	for _, rawCertAny := range x5cAny {
		rawCert, ok := rawCertAny.([]byte)
		if !ok || len(rawCert) == 0 {
			return nil, fmt.Errorf("%w: invalid x5c entry", ErrAttestationFailed)
		}
		cert, err := x509.ParseCertificate(rawCert)
		if err != nil {
			return nil, fmt.Errorf("%w: parse cert: %v", ErrAttestationFailed, err)
		}
		certs = append(certs, cert)
	}
	leaf := certs[0]
	intermediates := x509.NewCertPool()
	for _, ic := range certs[1:] {
		intermediates.AddCert(ic)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         v.RootPool,
		Intermediates: intermediates,
		CurrentTime:   v.now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, fmt.Errorf("%w: x5c chain: %v", ErrAttestationFailed, err)
	}

	// Verify nonce extension (OID 1.2.840.113635.100.8.2) == SHA256(authData || clientDataHash).
	certNonce, err := extractAppAttestNonce(leaf)
	if err != nil {
		return nil, fmt.Errorf("%w: nonce ext: %v", ErrAttestationFailed, err)
	}
	h := sha256.New()
	h.Write(authData)
	h.Write(clientDataHash)
	expectedNonce := h.Sum(nil)
	if !bytes.Equal(certNonce, expectedNonce) {
		return nil, fmt.Errorf("%w: nonce mismatch", ErrAttestationFailed)
	}

	// Verify public key is P-256 and SHA256(uncompressed point) == keyID.
	pubKey, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pubKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%w: leaf public key is not P-256", ErrAttestationFailed)
	}
	uncompressed, err := marshalUncompressedP256(pubKey)
	if err != nil {
		return nil, fmt.Errorf("%w: pubkey encode: %v", ErrAttestationFailed, err)
	}
	pubHash := sha256.Sum256(uncompressed)
	if !bytes.Equal(pubHash[:], keyIDBytes) {
		return nil, fmt.Errorf("%w: key_id does not match leaf public key", ErrAttestationFailed)
	}
	spki, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal spki: %v", ErrAttestationFailed, err)
	}

	// Parse authenticatorData:
	// [0..32)  rpIdHash
	// [32]     flags
	// [33..37) signCount (must be 0 on attestation)
	// [37..53) aaguid (16 bytes)
	// [53..55) credentialIdLength (uint16 big-endian, must be 32)
	// [55..87) credentialId (must equal keyIDBytes)
	if len(authData) < 87 {
		return nil, fmt.Errorf("%w: authData too short", ErrAttestationFailed)
	}
	expectedRPIDHash := sha256.Sum256([]byte(appleAppID))
	if !bytes.Equal(authData[0:32], expectedRPIDHash[:]) {
		return nil, fmt.Errorf("%w: rpIdHash mismatch", ErrAttestationFailed)
	}
	signCount := binary.BigEndian.Uint32(authData[33:37])
	if signCount != 0 {
		return nil, fmt.Errorf("%w: initial signCount must be 0", ErrAttestationFailed)
	}
	aaguid := authData[37:53]
	var attestEnv string
	switch {
	case bytes.Equal(aaguid, aaguidProduction):
		attestEnv = AttestEnvProduction
	case bytes.Equal(aaguid, aaguidDevelopment), bytes.Equal(aaguid, aaguidSandbox):
		if !allowSandbox {
			return nil, fmt.Errorf("%w: development aaguid disabled for app", ErrAttestationFailed)
		}
		attestEnv = AttestEnvDevelopment
	default:
		return nil, fmt.Errorf("%w: unrecognized aaguid", ErrAttestationFailed)
	}
	credLen := int(binary.BigEndian.Uint16(authData[53:55]))
	if credLen != sha256.Size || len(authData) < 55+credLen {
		return nil, fmt.Errorf("%w: invalid credentialId length", ErrAttestationFailed)
	}
	credID := authData[55 : 55+credLen]
	if !bytes.Equal(credID, keyIDBytes) {
		return nil, fmt.Errorf("%w: credentialId mismatch", ErrAttestationFailed)
	}

	return &VerifiedAttestation{
		KeyID:         keyID,
		PublicKeySPKI: spki,
		AttestEnv:     attestEnv,
		Receipt:       receipt,
	}, nil
}

// VerifyAssertion verifies an Apple App Attest assertionObject against a stored
// SPKI P-256 public key and strictly increasing signCount.
func (v *AppAttestVerifier) VerifyAssertion(appleAppID string, pubKeySPKI []byte, storedSignCount int64, clientDataHash, assertionObj []byte) (int64, error) {
	if appleAppID == "" || len(pubKeySPKI) == 0 || len(clientDataHash) != sha256.Size {
		return 0, ErrAssertionFailed
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubKeySPKI)
	if err != nil {
		return 0, fmt.Errorf("%w: parse spki: %v", ErrAssertionFailed, err)
	}
	pubKey, ok := pubAny.(*ecdsa.PublicKey)
	if !ok || pubKey.Curve != elliptic.P256() {
		return 0, fmt.Errorf("%w: stored key is not P-256", ErrAssertionFailed)
	}

	decoded, err := decodeCBOR(assertionObj)
	if err != nil {
		return 0, fmt.Errorf("%w: cbor: %v", ErrAssertionFailed, err)
	}
	top, ok := decoded.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("%w: root not map", ErrAssertionFailed)
	}
	authData, ok := top["authenticatorData"].([]byte)
	if !ok || len(authData) < 37 {
		return 0, fmt.Errorf("%w: invalid authenticatorData", ErrAssertionFailed)
	}
	sig, ok := top["signature"].([]byte)
	if !ok || len(sig) == 0 {
		return 0, fmt.Errorf("%w: missing signature", ErrAssertionFailed)
	}

	expectedRPIDHash := sha256.Sum256([]byte(appleAppID))
	if !bytes.Equal(authData[0:32], expectedRPIDHash[:]) {
		return 0, fmt.Errorf("%w: rpIdHash mismatch", ErrAssertionFailed)
	}
	newSignCount := int64(binary.BigEndian.Uint32(authData[33:37]))
	if newSignCount <= storedSignCount {
		return 0, fmt.Errorf("%w: signCount did not increase", ErrAssertionFailed)
	}

	// Verify ECDSA-P256 signature over SHA256(authenticatorData || clientDataHash).
	h := sha256.New()
	h.Write(authData)
	h.Write(clientDataHash)
	nonce := h.Sum(nil)
	if !ecdsa.VerifyASN1(pubKey, nonce, sig) {
		return 0, fmt.Errorf("%w: invalid signature", ErrAssertionFailed)
	}
	return newSignCount, nil
}

func decodeAppAttestKeyID(keyID string) ([]byte, error) {
	if raw, err := base64.StdEncoding.DecodeString(keyID); err == nil && len(raw) == sha256.Size {
		return raw, nil
	}
	if raw, err := base64.RawURLEncoding.DecodeString(keyID); err == nil && len(raw) == sha256.Size {
		return raw, nil
	}
	return nil, errors.New("invalid key_id encoding")
}

func marshalUncompressedP256(pub *ecdsa.PublicKey) ([]byte, error) {
	ecdhKey, err := pub.ECDH()
	if err != nil {
		return nil, err
	}
	raw := ecdhKey.Bytes()
	if len(raw) != 65 || raw[0] != 0x04 {
		return nil, errors.New("unexpected P-256 public key encoding")
	}
	return raw, nil
}

// extractAppAttestNonce parses Apple's OID 1.2.840.113635.100.8.2 extension:
// SEQUENCE { [1] EXPLICIT OCTET STRING }
func extractAppAttestNonce(cert *x509.Certificate) ([]byte, error) {
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(oidAppleAppAttestNonce) {
			continue
		}
		var seq asn1.RawValue
		rest, err := asn1.Unmarshal(ext.Value, &seq)
		if err != nil || len(rest) != 0 || seq.Class != asn1.ClassUniversal || seq.Tag != asn1.TagSequence || !seq.IsCompound {
			return nil, errors.New("malformed nonce sequence")
		}
		var tagged asn1.RawValue
		rest, err = asn1.Unmarshal(seq.Bytes, &tagged)
		if err != nil || len(rest) != 0 || tagged.Class != asn1.ClassContextSpecific || tagged.Tag != 1 || !tagged.IsCompound {
			return nil, errors.New("malformed tagged nonce")
		}
		var octet []byte
		rest, err = asn1.Unmarshal(tagged.Bytes, &octet)
		if err != nil || len(rest) != 0 || len(octet) != sha256.Size {
			return nil, errors.New("malformed nonce octet string")
		}
		return octet, nil
	}
	return nil, errors.New("nonce extension missing")
}
