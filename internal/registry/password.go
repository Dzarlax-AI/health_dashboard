package registry

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// HashPassword returns a bcrypt password hash for new credentials.
func HashPassword(password string) (string, error) {
	return hashPassword(password)
}

func HashPasswordForStorage(password string) (string, error) {
	return hashPassword(password)
}

func IsPasswordTooLong(err error) bool {
	return errors.Is(err, bcrypt.ErrPasswordTooLong)
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword accepts current bcrypt hashes and legacy unsalted SHA-256
// hashes. The second return value is true when a successful legacy match should
// be rehashed to bcrypt.
func VerifyPassword(storedHash, password string) (ok bool, needsRehash bool) {
	if IsBcryptHash(storedHash) {
		return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)) == nil, false
	}
	if IsLegacySHA256Hash(storedHash) {
		legacy := LegacySHA256Hash(password)
		if subtle.ConstantTimeCompare([]byte(legacy), []byte(storedHash)) == 1 {
			return true, true
		}
	}
	return false, false
}

func IsBcryptHash(hash string) bool {
	return strings.HasPrefix(hash, "$2a$") ||
		strings.HasPrefix(hash, "$2b$") ||
		strings.HasPrefix(hash, "$2y$")
}

func IsLegacySHA256Hash(hash string) bool {
	if len(hash) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func LegacySHA256Hash(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}
