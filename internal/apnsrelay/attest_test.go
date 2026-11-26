package apnsrelay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"strings"
	"testing"
	"time"
)

type testAttestCA struct {
	rootCert  *x509.Certificate
	rootKey   *ecdsa.PrivateKey
	interCert *x509.Certificate
	interKey  *ecdsa.PrivateKey
	pool      *x509.CertPool
}

func newTestAttestCA(t *testing.T, now time.Time) *testAttestCA {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("gen root key: %v", err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Apple App Attestation Root CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create root cert: %v", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatalf("parse root cert: %v", err)
	}

	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen inter key: %v", err)
	}
	interTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Test Apple App Attestation Intermediate CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, rootCert, &interKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create inter cert: %v", err)
	}
	interCert, err := x509.ParseCertificate(interDER)
	if err != nil {
		t.Fatalf("parse inter cert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(rootCert)
	return &testAttestCA{
		rootCert:  rootCert,
		rootKey:   rootKey,
		interCert: interCert,
		interKey:  interKey,
		pool:      pool,
	}
}

type testDeviceKey struct {
	priv      *ecdsa.PrivateKey
	keyID     string
	keyIDRaw  []byte
	signCount uint32
}

func newTestDeviceKey(t *testing.T) *testDeviceKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen device key: %v", err)
	}
	uncompressed, err := marshalUncompressedP256(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	sum := sha256.Sum256(uncompressed)
	return &testDeviceKey{
		priv:     priv,
		keyID:    base64.StdEncoding.EncodeToString(sum[:]),
		keyIDRaw: sum[:],
	}
}

func (ca *testAttestCA) buildAttestationObject(t *testing.T, dev *testDeviceKey, appleAppID string, clientDataHash []byte, aaguid []byte, initSignCount uint32, now time.Time) []byte {
	t.Helper()
	rpHash := sha256.Sum256([]byte(appleAppID))
	authData := make([]byte, 0, 87)
	authData = append(authData, rpHash[:]...)
	authData = append(authData, 0x41) // flags
	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], initSignCount)
	authData = append(authData, sc[:]...)
	authData = append(authData, aaguid...)
	var cl [2]byte
	binary.BigEndian.PutUint16(cl[:], uint16(len(dev.keyIDRaw)))
	authData = append(authData, cl[:]...)
	authData = append(authData, dev.keyIDRaw...)

	h := sha256.New()
	h.Write(authData)
	h.Write(clientDataHash)
	nonce := h.Sum(nil)

	octetDER, err := asn1.Marshal(nonce)
	if err != nil {
		t.Fatalf("marshal nonce octet: %v", err)
	}
	taggedDER, err := asn1.Marshal(asn1.RawValue{
		Class:      asn1.ClassContextSpecific,
		Tag:        1,
		IsCompound: true,
		Bytes:      octetDER,
	})
	if err != nil {
		t.Fatalf("marshal tagged nonce: %v", err)
	}
	extDER, err := asn1.Marshal(asn1.RawValue{
		Class:      asn1.ClassUniversal,
		Tag:        asn1.TagSequence,
		IsCompound: true,
		Bytes:      taggedDER,
	})
	if err != nil {
		t.Fatalf("marshal seq nonce: %v", err)
	}

	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "Test Attested Leaf"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtraExtensions: []pkix.Extension{
			{Id: oidAppleAppAttestNonce, Value: extDER},
		},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca.interCert, &dev.priv.PublicKey, ca.interKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	obj, err := encodeCBOR(map[string]any{
		"fmt":      "apple-appattest",
		"authData": authData,
		"attStmt": map[string]any{
			"x5c":     [][]byte{leafDER, ca.interCert.Raw},
			"receipt": []byte("test-receipt-bytes"),
		},
	})
	if err != nil {
		t.Fatalf("encode attestation CBOR: %v", err)
	}
	return obj
}

func (dev *testDeviceKey) buildAssertion(t *testing.T, appleAppID string, clientDataHash []byte, signCount uint32) []byte {
	t.Helper()
	rpHash := sha256.Sum256([]byte(appleAppID))
	authData := make([]byte, 0, 37)
	authData = append(authData, rpHash[:]...)
	authData = append(authData, 0x01)
	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], signCount)
	authData = append(authData, sc[:]...)

	h := sha256.New()
	h.Write(authData)
	h.Write(clientDataHash)
	nonce := h.Sum(nil)

	sig, err := ecdsa.SignASN1(rand.Reader, dev.priv, nonce)
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}
	obj, err := encodeCBOR(map[string]any{
		"authenticatorData": authData,
		"signature":         sig,
	})
	if err != nil {
		t.Fatalf("encode assertion CBOR: %v", err)
	}
	return obj
}

func TestAppAttestVerifierEmbeddedRootLoads(t *testing.T) {
	v, err := NewAppAttestVerifier()
	if err != nil || v.RootPool == nil {
		t.Fatalf("expected embedded Apple Root CA to load, err=%v", err)
	}
}

func TestAppAttestAttestationAndAssertionValidation(t *testing.T) {
	now := time.Date(2026, 11, 26, 19, 0, 0, 0, time.UTC)
	ca := newTestAttestCA(t, now)
	otherCA := newTestAttestCA(t, now)
	dev := newTestDeviceKey(t)
	verifier := &AppAttestVerifier{RootPool: ca.pool, Now: func() time.Time { return now }}

	canonical := CanonicalRegisterPayload(AppIDHealthSync, dev.keyID, "ch-1", "nonce-1")
	clientHash := sha256.Sum256(canonical)

	// 1. Valid production attestation succeeds.
	attProd := ca.buildAttestationObject(t, dev, DefaultHealthAppleAppID, clientHash[:], aaguidProduction, 0, now)
	verified, err := verifier.VerifyAttestation(DefaultHealthAppleAppID, dev.keyID, clientHash[:], attProd, false)
	if err != nil {
		t.Fatalf("VerifyAttestation valid prod failed: %v", err)
	}
	if verified.AttestEnv != AttestEnvProduction {
		t.Fatalf("attest_env=%q, want production", verified.AttestEnv)
	}

	// 2. Development AAGUID rejected when allowSandbox=false, accepted when true.
	attDev := ca.buildAttestationObject(t, dev, DefaultHealthAppleAppID, clientHash[:], aaguidDevelopment, 0, now)
	if _, err := verifier.VerifyAttestation(DefaultHealthAppleAppID, dev.keyID, clientHash[:], attDev, false); err == nil {
		t.Fatal("expected dev aaguid to fail when allowSandbox=false")
	}
	verifiedDev, err := verifier.VerifyAttestation(DefaultHealthAppleAppID, dev.keyID, clientHash[:], attDev, true)
	if err != nil || verifiedDev.AttestEnv != AttestEnvDevelopment {
		t.Fatalf("expected dev aaguid to pass when allowSandbox=true, got %+v err=%v", verifiedDev, err)
	}

	// 3. Untrusted root CA rejected.
	attUntrusted := otherCA.buildAttestationObject(t, dev, DefaultHealthAppleAppID, clientHash[:], aaguidProduction, 0, now)
	if _, err := verifier.VerifyAttestation(DefaultHealthAppleAppID, dev.keyID, clientHash[:], attUntrusted, false); err == nil {
		t.Fatal("expected untrusted root CA to fail")
	}

	// 4. Wrong rpIdHash rejected.
	attWrongRP := ca.buildAttestationObject(t, dev, "TEAM999999.com.other.app", clientHash[:], aaguidProduction, 0, now)
	if _, err := verifier.VerifyAttestation(DefaultHealthAppleAppID, dev.keyID, clientHash[:], attWrongRP, false); err == nil {
		t.Fatal("expected wrong rpIdHash to fail")
	}

	// 5. Non-zero initial signCount rejected.
	attNonZeroSC := ca.buildAttestationObject(t, dev, DefaultHealthAppleAppID, clientHash[:], aaguidProduction, 1, now)
	if _, err := verifier.VerifyAttestation(DefaultHealthAppleAppID, dev.keyID, clientHash[:], attNonZeroSC, false); err == nil {
		t.Fatal("expected non-zero initial signCount to fail")
	}

	// 6. Assertion verification: monotonic signCount and exact clientDataHash.
	bodyHash := sha256.Sum256([]byte(`{"op":"grant.create"}`))
	as1 := dev.buildAssertion(t, DefaultHealthAppleAppID, bodyHash[:], 1)
	newSC, err := verifier.VerifyAssertion(DefaultHealthAppleAppID, verified.PublicKeySPKI, 0, bodyHash[:], as1)
	if err != nil || newSC != 1 {
		t.Fatalf("VerifyAssertion valid failed: sc=%d err=%v", newSC, err)
	}
	// Replay with same signCount must fail.
	if _, err := verifier.VerifyAssertion(DefaultHealthAppleAppID, verified.PublicKeySPKI, 1, bodyHash[:], as1); err == nil {
		t.Fatal("expected non-increasing signCount to fail")
	}
	// Tampered body hash must fail.
	otherBodyHash := sha256.Sum256([]byte(`{"op":"grant.revoke"}`))
	if _, err := verifier.VerifyAssertion(DefaultHealthAppleAppID, verified.PublicKeySPKI, 0, otherBodyHash[:], as1); err == nil {
		t.Fatal("expected tampered clientDataHash to fail")
	}
}

func TestTokenCipherKeyRotation(t *testing.T) {
	k1 := strings.Repeat("11", 32)
	k2 := strings.Repeat("22", 32)

	c1, err := ParseTokenCipherKeys("v1:" + k1)
	if err != nil {
		t.Fatalf("ParseTokenCipherKeys v1: %v", err)
	}
	aad := []byte("health_sync\x00key\x00grant")
	ct1, err := c1.Encrypt("deadbeef", aad)
	if err != nil {
		t.Fatalf("Encrypt v1: %v", err)
	}

	// Rotate to v2 active while retaining v1 for decryption.
	c2, err := ParseTokenCipherKeys("v2:" + k2 + ",v1:" + k1)
	if err != nil {
		t.Fatalf("ParseTokenCipherKeys v2,v1: %v", err)
	}
	plain1, err := c2.Decrypt(ct1, aad)
	if err != nil || plain1 != "deadbeef" {
		t.Fatalf("Decrypt v1 with rotated cipher failed: plain=%q err=%v", plain1, err)
	}
	ct2, err := c2.Encrypt("cafebabe", aad)
	if err != nil {
		t.Fatalf("Encrypt v2: %v", err)
	}
	if !strings.HasPrefix(string(ct2), "v1:v2:") {
		t.Fatalf("expected active key id v2 in envelope, got %q", string(ct2))
	}
	plain2, err := c2.Decrypt(ct2, aad)
	if err != nil || plain2 != "cafebabe" {
		t.Fatalf("Decrypt v2 failed: plain=%q err=%v", plain2, err)
	}
	// Wrong AAD must fail decryption.
	if _, err := c2.Decrypt(ct2, []byte("wrong-aad")); err == nil {
		t.Fatal("expected wrong AAD to fail decryption")
	}
}
