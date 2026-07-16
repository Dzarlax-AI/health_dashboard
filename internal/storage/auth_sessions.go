package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const authSessionTokenBytes = 32

func (s *DB) EnsureAuthSessionsTable() error {
	ctx, cancel := queryCtx()
	defer cancel()
	return s.EnsureAuthSessionsTableContext(ctx)
}

func (s *DB) EnsureAuthSessionsTableContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS auth_sessions (
			id_hash      TEXT PRIMARY KEY,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at   TIMESTAMPTZ NOT NULL,
			last_seen_at TIMESTAMPTZ
		)
	`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions (expires_at)`)
	return err
}

func (s *DB) CreateAuthSession(ttl time.Duration) (string, error) {
	token, err := generateAuthSessionToken()
	if err != nil {
		return "", err
	}
	ctx, cancel := queryCtx()
	defer cancel()
	_, _ = s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE expires_at <= NOW()`)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO auth_sessions (id_hash, expires_at)
		VALUES ($1, $2)
	`, authSessionDigest(token), time.Now().Add(ttl))
	if err != nil {
		return "", fmt.Errorf("create auth session: %w", err)
	}
	return token, nil
}

func (s *DB) AuthSessionValid(token string) bool {
	ctx, cancel := queryCtx()
	defer cancel()
	var ok bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM auth_sessions
			WHERE id_hash = $1 AND expires_at > NOW()
		)
	`, authSessionDigest(token)).Scan(&ok); err != nil || !ok {
		return false
	}
	_, _ = s.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = NOW() WHERE id_hash = $1`, authSessionDigest(token))
	return true
}

func (s *DB) DeleteAuthSession(token string) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE id_hash = $1`, authSessionDigest(token))
	return err
}

func generateAuthSessionToken() (string, error) {
	b := make([]byte, authSessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func authSessionDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
