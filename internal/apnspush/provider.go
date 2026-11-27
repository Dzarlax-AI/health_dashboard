package apnspush

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"health-receiver/internal/storage"
)

const DefaultTopic = "com.dzarlax.health-sync"

var ErrCredential = errors.New("invalid APNs credential")
var ErrPreSend = errors.New("APNs request not dispatched")

type Credential struct {
	KeyID      string `json:"key_id"`
	TeamID     string `json:"team_id"`
	PrivateKey string `json:"private_key"`
}

// ParseCredential validates an APNs JSON credential bundle with strict Apple ID
// and PKCS#8 P-256 private key checks.
func ParseCredential(raw []byte) (Credential, *ecdsa.PrivateKey, error) {
	var c Credential
	if len(raw) == 0 || len(raw) > 8192 {
		return c, nil, ErrCredential
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil || hasDuplicateJSONKey(raw) || len(object) != 3 {
		return c, nil, ErrCredential
	}
	for _, key := range []string{"key_id", "team_id", "private_key"} {
		v, ok := object[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return c, nil, ErrCredential
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return Credential{}, nil, ErrCredential
	}
	k, err := ParseCredentialFields(c)
	if err != nil {
		return Credential{}, nil, err
	}
	return c, k, nil
}

// ParseCredentialFields validates KeyID, TeamID, and PEM-encoded P-256 private key.
func ParseCredentialFields(c Credential) (*ecdsa.PrivateKey, error) {
	if !validAppleID(c.KeyID) || !validAppleID(c.TeamID) {
		return nil, ErrCredential
	}
	pemText := strings.ReplaceAll(strings.TrimSpace(c.PrivateKey), `\n`, "\n")
	b, rest := pem.Decode([]byte(pemText))
	if b == nil || b.Type != "PRIVATE KEY" || len(b.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrCredential
	}
	k, err := x509ParsePKCS8(b.Bytes)
	if err != nil || k.Curve != elliptic.P256() || k.D == nil || k.D.Sign() <= 0 || k.D.Cmp(k.Params().N) >= 0 {
		return nil, ErrCredential
	}
	return k, nil
}

// LoadSenderFromEnv loads the optional process-level APNs sender from environment
// configuration. Returns (nil, nil) when APNs is not configured.
func LoadSenderFromEnv() (*Sender, error) {
	topic := strings.TrimSpace(os.Getenv("APNS_TOPIC"))
	if topic == "" {
		topic = DefaultTopic
	}

	credJSON := strings.TrimSpace(os.Getenv("APNS_CREDENTIALS_JSON"))
	credFile := strings.TrimSpace(os.Getenv("APNS_CREDENTIALS_FILE"))
	keyID := strings.TrimSpace(os.Getenv("APNS_KEY_ID"))
	teamID := strings.TrimSpace(os.Getenv("APNS_TEAM_ID"))
	privKey := strings.TrimSpace(os.Getenv("APNS_PRIVATE_KEY"))
	privKeyFile := strings.TrimSpace(os.Getenv("APNS_PRIVATE_KEY_FILE"))

	if credJSON == "" && credFile == "" && keyID == "" && teamID == "" && privKey == "" && privKeyFile == "" {
		return nil, nil
	}

	var cred Credential
	var key *ecdsa.PrivateKey
	var err error
	switch {
	case credJSON != "":
		cred, key, err = ParseCredential([]byte(credJSON))
	case credFile != "":
		raw, readErr := os.ReadFile(credFile)
		if readErr != nil {
			return nil, fmt.Errorf("read APNS_CREDENTIALS_FILE: %w", readErr)
		}
		cred, key, err = ParseCredential(raw)
	default:
		if privKey == "" && privKeyFile != "" {
			raw, readErr := os.ReadFile(privKeyFile)
			if readErr != nil {
				return nil, fmt.Errorf("read APNS_PRIVATE_KEY_FILE: %w", readErr)
			}
			privKey = string(raw)
		}
		cred = Credential{KeyID: keyID, TeamID: teamID, PrivateKey: privKey}
		key, err = ParseCredentialFields(cred)
	}
	if err != nil {
		return nil, err
	}
	return &Sender{
		Topic:      topic,
		Credential: cred,
		Key:        key,
	}, nil
}

// Sign creates a short-lived APNs ES256 provider JWT using raw R||S signature encoding.
func Sign(c Credential, k *ecdsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.KeyID})
	h := enc.EncodeToString(header)
	p, _ := json.Marshal(map[string]any{"iss": c.TeamID, "iat": now.Unix()})
	input := h + "." + enc.EncodeToString(p)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
	if err != nil {
		return "", err
	}
	const wide = 32
	sig := make([]byte, wide*2)
	r.FillBytes(sig[:wide])
	s.FillBytes(sig[wide:])
	return input + "." + enc.EncodeToString(sig), nil
}

func validAppleID(v string) bool {
	if len(v) != 10 {
		return false
	}
	for _, c := range v {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

type Sender struct {
	mu                sync.Mutex
	Topic             string
	ProductionBaseURL string
	SandboxBaseURL    string
	Client            *http.Client
	Credential        Credential
	Key               *ecdsa.PrivateKey
	Now               func() time.Time
	jwt               string
	jwtAt             time.Time
}

func (s *Sender) EffectiveTopic() string {
	if s == nil || strings.TrimSpace(s.Topic) == "" {
		return DefaultTopic
	}
	return strings.TrimSpace(s.Topic)
}

func (s *Sender) CloseIdleConnections() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Client != nil {
		s.Client.CloseIdleConnections()
	}
}

type Result struct {
	Status     int
	APNsID     string
	RetryAfter time.Duration
	Reason     string
	Timestamp  *time.Time
}

func (s *Sender) Send(ctx context.Context, environment, token, requestID, pushType, collapse string, priority int, expiration time.Time, payload []byte) (Result, error) {
	var out Result
	if s == nil || s.Key == nil || !storage.ValidAPNsEnv(environment) || !storage.ValidAPNsToken(token) {
		return out, ErrCredential
	}
	base := s.ProductionBaseURL
	if base == "" {
		base = "https://api.push.apple.com"
	}
	if environment == storage.PushEnvSandbox {
		base = s.SandboxBaseURL
		if base == "" {
			base = "https://api.sandbox.push.apple.com"
		}
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	client, jwt, err := s.transportAndJWT(now)
	if err != nil {
		return out, fmt.Errorf("%w: %w", ErrPreSend, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/3/device/"+token, bytes.NewReader(payload))
	if err != nil {
		return out, ErrPreSend
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", s.EffectiveTopic())
	req.Header.Set("apns-push-type", pushType)
	req.Header.Set("apns-priority", strconv.Itoa(priority))
	if requestID != "" {
		req.Header.Set("apns-id", requestID)
	}
	if collapse != "" {
		req.Header.Set("apns-collapse-id", collapse)
	}
	if !expiration.IsZero() {
		req.Header.Set("apns-expiration", strconv.FormatInt(expiration.Unix(), 10))
	}
	req.Header.Set("content-type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return out, errors.New("APNs transport outcome unknown")
	}
	defer resp.Body.Close()

	out.Status = resp.StatusCode
	out.APNsID = resp.Header.Get("apns-id")
	if v := resp.Header.Get("retry-after"); v != "" {
		if sec, e := strconv.Atoi(v); e == nil && sec >= 0 && sec <= 3600 {
			out.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var parsed struct {
		Reason    string `json:"reason"`
		Timestamp int64  `json:"timestamp"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		out.Reason = parsed.Reason
		if parsed.Timestamp > 0 {
			ts := time.UnixMilli(parsed.Timestamp).UTC()
			out.Timestamp = &ts
		}
	}
	return out, nil
}

func (s *Sender) transportAndJWT(now time.Time) (*http.Client, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Client == nil {
		s.Client = &http.Client{
			Transport: &http2.Transport{IdleConnTimeout: 90 * time.Second},
			Timeout:   15 * time.Second,
		}
	}
	if s.jwt == "" || now.Sub(s.jwtAt) >= 40*time.Minute || now.Before(s.jwtAt) {
		token, err := Sign(s.Credential, s.Key, now)
		if err != nil {
			return nil, "", err
		}
		s.jwt = token
		s.jwtAt = now
	}
	return s.Client, s.jwt, nil
}

func x509ParsePKCS8(der []byte) (*ecdsa.PrivateKey, error) {
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	e, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not EC")
	}
	return e, nil
}

func hasDuplicateJSONKey(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() bool
	walk = func() bool {
		t, e := d.Token()
		if e != nil {
			return false
		}
		del, ok := t.(json.Delim)
		if !ok {
			return false
		}
		switch del {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return true
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return true
				}
				seen[key] = true
				if walk() {
					return true
				}
			}
			_, e = d.Token()
			return e != nil
		case '[':
			for d.More() {
				if walk() {
					return true
				}
			}
			_, e = d.Token()
			return e != nil
		}
		return false
	}
	return walk()
}
