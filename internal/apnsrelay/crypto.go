package apnsrelay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

// TokenCipher encrypts APNs device tokens at rest using AES-256-GCM with
// versioned key IDs so encryption keys can be rotated without downtime.
// Format: "v1:<key_id>:<base64url(nonce || ciphertext)>".
type TokenCipher struct {
	activeID string
	keys     map[string]cipher.AEAD
}

// ParseTokenCipherKeys parses comma-separated "<key_id>:<64-hex-chars>" entries.
// The first entry is the active encryption key; all entries can decrypt.
func ParseTokenCipherKeys(spec string) (*TokenCipher, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("RELAY_TOKEN_ENCRYPTION_KEYS is required")
	}
	parts := strings.Split(spec, ",")
	tc := &TokenCipher{keys: make(map[string]cipher.AEAD, len(parts))}
	for _, raw := range parts {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		kid, hexKey, ok := strings.Cut(entry, ":")
		kid = strings.TrimSpace(kid)
		hexKey = strings.TrimSpace(hexKey)
		if !ok || kid == "" || strings.ContainsAny(kid, ": \t\r\n") {
			return nil, errors.New("invalid encryption key entry; expected key ID and 32-byte hex key")
		}
		if _, dup := tc.keys[kid]; dup {
			return nil, errors.New("duplicate encryption key ID")
		}
		keyBytes, err := hex.DecodeString(hexKey)
		if err != nil || len(keyBytes) != 32 {
			return nil, errors.New("encryption key must be 32-byte hex (64 chars)")
		}
		block, err := aes.NewCipher(keyBytes)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		if tc.activeID == "" {
			tc.activeID = kid
		}
		tc.keys[kid] = aead
	}
	if tc.activeID == "" {
		return nil, errors.New("RELAY_TOKEN_ENCRYPTION_KEYS contains no keys")
	}
	return tc, nil
}

func (c *TokenCipher) Encrypt(plaintext string, aad []byte) ([]byte, error) {
	if c == nil || c.activeID == "" {
		return nil, errors.New("token cipher not configured")
	}
	aead := c.keys[c.activeID]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), aad)
	encoded := "v1:" + c.activeID + ":" + base64.RawURLEncoding.EncodeToString(sealed)
	return []byte(encoded), nil
}

func (c *TokenCipher) UsesActiveKey(ciphertext []byte) bool {
	return c != nil && strings.HasPrefix(string(ciphertext), "v1:"+c.activeID+":")
}

func (c *TokenCipher) Decrypt(ciphertext, aad []byte) (string, error) {
	if c == nil {
		return "", errors.New("token cipher not configured")
	}
	parts := strings.SplitN(string(ciphertext), ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return "", errors.New("invalid encrypted token envelope")
	}
	aead, ok := c.keys[parts[1]]
	if !ok {
		return "", errors.New("unknown encryption key ID")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("invalid encrypted token encoding")
	}
	ns := aead.NonceSize()
	if len(raw) <= ns {
		return "", errors.New("truncated encrypted token")
	}
	plain, err := aead.Open(nil, raw[:ns], raw[ns:], aad)
	if err != nil {
		return "", errors.New("failed to decrypt token")
	}
	return string(plain), nil
}
