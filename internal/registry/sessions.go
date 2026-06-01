package registry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const sessionTokenBytes = 32

var ErrSessionNotFound = errors.New("session not found")

func (r *Registry) CreateSession(ctx context.Context, username string, ttl time.Duration) (string, error) {
	token, err := generateSessionToken()
	if err != nil {
		return "", err
	}
	_, _ = r.pool.Exec(ctx, `DELETE FROM health_registry.sessions WHERE expires_at <= NOW()`)
	_, err = r.pool.Exec(ctx, `
		INSERT INTO health_registry.sessions (id_hash, username, expires_at)
		VALUES ($1, $2, $3)
	`, sessionDigest(token), username, time.Now().Add(ttl))
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

func (r *Registry) GetSessionUser(ctx context.Context, token string) (*User, error) {
	var u User
	var email *string
	err := r.pool.QueryRow(ctx, `
		SELECT u.username, u.schema_name, u.api_key, u.password_hash, u.email, u.is_admin, u.created_at
		FROM health_registry.sessions s
		JOIN health_registry.users u ON u.username = s.username
		WHERE s.id_hash = $1 AND s.expires_at > NOW()
	`, sessionDigest(token)).Scan(
		&u.Username, &u.SchemaName, &u.APIKey, &u.PasswordHash, &email, &u.IsAdmin, &u.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	if email != nil {
		u.Email = *email
	}
	_, _ = r.pool.Exec(ctx, `UPDATE health_registry.sessions SET last_seen_at = NOW() WHERE id_hash = $1`, sessionDigest(token))
	return &u, nil
}

func (r *Registry) DeleteSession(ctx context.Context, token string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM health_registry.sessions WHERE id_hash = $1`, sessionDigest(token))
	return err
}

func generateSessionToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func sessionDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
