package registry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNeedsManualSetup is returned when the database user lacks privileges to
// create the health_registry schema. The caller should log SQL and continue
// in legacy single-user mode.
type ErrNeedsManualSetup struct {
	SQL string
}

func (e *ErrNeedsManualSetup) Error() string {
	return fmt.Sprintf("insufficient privileges to create health_registry schema — run as PostgreSQL superuser:\n  %s\nThen restart the server.", e.SQL)
}

// User represents a registered health dashboard user.
type User struct {
	Username     string
	SchemaName   string
	APIKey       string
	PasswordHash string // hex(sha256(password))
	Email        string // optional; used for Authentik X-authentik-email matching
	IsAdmin      bool
	CreatedAt    time.Time
}

// Registry manages user accounts stored in the health_registry schema.
// All queries use fully-qualified table names so search_path doesn't matter.
type Registry struct {
	pool *pgxpool.Pool
}

// New opens a registry connection. The pool uses no fixed search_path so it
// works regardless of the role-level default.
func New(ctx context.Context, connStr string) (*Registry, error) {
	config, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	config.MaxConns = 5
	config.MinConns = 1
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Registry{pool: pool}, nil
}

// EnsureSchema creates the health_registry schema and users table if they do
// not exist. Returns *ErrNeedsManualSetup if the DB user lacks CREATE privilege.
func (r *Registry) EnsureSchema(ctx context.Context) error {
	// Check if schema already exists.
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.schemata
			WHERE schema_name = 'health_registry'
		)
	`).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check schema: %w", err)
	}

	if !exists {
		_, err = r.pool.Exec(ctx, `CREATE SCHEMA health_registry`)
		if err != nil {
			if isPermissionDenied(err) {
				return &ErrNeedsManualSetup{
					SQL: "CREATE SCHEMA health_registry AUTHORIZATION " + r.currentUser(ctx) + ";",
				}
			}
			return fmt.Errorf("create schema: %w", err)
		}
	}

	_, err = r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS health_registry.users (
			username      TEXT PRIMARY KEY,
			schema_name   TEXT UNIQUE NOT NULL,
			api_key       TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			email         TEXT UNIQUE,
			is_admin      BOOLEAN NOT NULL DEFAULT false,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create users table: %w", err)
	}
	// Add email column to existing installations that predate this field.
	_, _ = r.pool.Exec(ctx, `ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS email TEXT UNIQUE`)
	return nil
}

// IsEmpty reports whether the users table has no rows.
func (r *Registry) IsEmpty(ctx context.Context) bool {
	var exists bool
	r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_registry.users LIMIT 1)`).Scan(&exists)
	return !exists
}

// DetectLegacyInstall returns true when a health schema with metric_points
// exists but no users are registered — i.e., an upgrade from single-user mode.
func (r *Registry) DetectLegacyInstall(ctx context.Context) bool {
	var exists bool
	r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'health' AND table_name = 'metric_points'
		)
	`).Scan(&exists)
	return exists && r.IsEmpty(ctx)
}

// GetByAPIKey looks up a user by their API key.
func (r *Registry) GetByAPIKey(ctx context.Context, key string) (*User, error) {
	return r.getUser(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at
		FROM health_registry.users WHERE api_key = $1
	`, key)
}

// GetByUsername looks up a user by username.
func (r *Registry) GetByUsername(ctx context.Context, username string) (*User, error) {
	return r.getUser(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at
		FROM health_registry.users WHERE username = $1
	`, username)
}

// GetByEmail looks up a user by email address.
func (r *Registry) GetByEmail(ctx context.Context, email string) (*User, error) {
	return r.getUser(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at
		FROM health_registry.users WHERE email = $1
	`, email)
}

func (r *Registry) getUser(ctx context.Context, query string, arg string) (*User, error) {
	var u User
	var email *string
	err := r.pool.QueryRow(ctx, query, arg).Scan(
		&u.Username, &u.SchemaName, &u.APIKey, &u.PasswordHash, &email, &u.IsAdmin, &u.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, err
	}
	if email != nil {
		u.Email = *email
	}
	return &u, nil
}

// ListUsers returns all registered users.
func (r *Registry) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at
		FROM health_registry.users ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var email *string
		if err := rows.Scan(&u.Username, &u.SchemaName, &u.APIKey, &u.PasswordHash, &email, &u.IsAdmin, &u.CreatedAt); err != nil {
			return nil, err
		}
		if email != nil {
			u.Email = *email
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CreateUserReq holds parameters for creating a new user.
type CreateUserReq struct {
	Username   string
	SchemaName string // derived from username if empty
	Password   string
	Email      string // optional
	IsAdmin    bool
}

// CreateUser inserts a new user. Generates an API key automatically.
// Returns the created user (with APIKey populated).
func (r *Registry) CreateUser(ctx context.Context, req CreateUserReq) (*User, error) {
	if req.SchemaName == "" {
		req.SchemaName = "health_" + strings.ToLower(req.Username)
	}
	apiKey, err := generateAPIKey()
	if err != nil {
		return nil, fmt.Errorf("generate api key: %w", err)
	}
	hash := hashPassword(req.Password)

	var emailPtr *string
	if req.Email != "" {
		emailPtr = &req.Email
	}

	u := User{
		Username:     req.Username,
		SchemaName:   req.SchemaName,
		APIKey:       apiKey,
		PasswordHash: hash,
		Email:        req.Email,
		IsAdmin:      req.IsAdmin,
		CreatedAt:    time.Now(),
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO health_registry.users (username, schema_name, api_key, password_hash, email, is_admin)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, u.Username, u.SchemaName, u.APIKey, u.PasswordHash, emailPtr, u.IsAdmin)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return &u, nil
}

// MigrateFromEnv creates the first admin user from env-var credentials.
// Used when upgrading from single-user mode or seeding from docker-compose env.
func (r *Registry) MigrateFromEnv(ctx context.Context, apiKey, passwordHash, schemaName, email string) error {
	if apiKey == "" {
		var err error
		apiKey, err = generateAPIKey()
		if err != nil {
			return err
		}
		fmt.Printf("[health] NOTICE: no API_KEY set — generated: %s\n", apiKey)
	}
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO health_registry.users (username, schema_name, api_key, password_hash, email, is_admin)
		VALUES ('admin', $1, $2, $3, $4, true)
		ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email WHERE health_registry.users.email IS NULL
	`, schemaName, apiKey, passwordHash, emailPtr)
	return err
}

// DeleteUser removes a user by username. Does not drop their schema.
func (r *Registry) DeleteUser(ctx context.Context, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM health_registry.users WHERE username = $1`, username)
	return err
}

// Close releases the connection pool.
func (r *Registry) Close() {
	r.pool.Close()
}

// HashPassword returns hex(sha256(password)), matching the cookie auth format.
func HashPassword(password string) string {
	return hashPassword(password)
}

func hashPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (r *Registry) currentUser(ctx context.Context) string {
	var u string
	if err := r.pool.QueryRow(ctx, `SELECT current_user`).Scan(&u); err != nil {
		return "current_user"
	}
	return u
}

func isPermissionDenied(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42501" // insufficient_privilege
	}
	return false
}
