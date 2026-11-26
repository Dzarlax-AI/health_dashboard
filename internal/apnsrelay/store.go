package apnsrelay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ChallengeTTL            = 3 * time.Minute
	GrantTTL                = 90 * 24 * time.Hour
	MaxActiveGrantsPerKey   = 10
	MaxGrantsCreatedPerDay  = 20
	DefaultDailyGrantLimit  = 30
	GrantCleanupRetention   = 30 * 24 * time.Hour
)

var (
	ErrChallengeInvalid     = errors.New("challenge invalid or expired")
	ErrKeyAlreadyRegistered = errors.New("attest key already registered")
	ErrKeyNotFound          = errors.New("attest key not found or revoked")
	ErrGrantConflict        = errors.New("grant conflict or cancelled")
	ErrGrantNotFound        = errors.New("grant not found")
	ErrGrantLimitExceeded   = errors.New("grant limit exceeded")
	ErrEnvNotAllowed        = errors.New("apns environment not allowed for attested key")
	ErrDailyQuotaExceeded   = errors.New("daily delivery quota exceeded")
)

const relaySchemaDDL = `
CREATE TABLE IF NOT EXISTS attest_challenges (
	id         UUID PRIMARY KEY,
	app_id     TEXT NOT NULL,
	key_id     TEXT NOT NULL DEFAULT '',
	op         TEXT NOT NULL CHECK (op IN ('attest.register','grant.create','grant.refresh','grant.revoke','grant.cancel_by_client_key','device.update_token')),
	nonce      BYTEA NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_attest_challenges_expires
	ON attest_challenges (expires_at);

CREATE TABLE IF NOT EXISTS attest_keys (
	key_id          TEXT PRIMARY KEY,
	app_id          TEXT NOT NULL,
	public_key_spki BYTEA NOT NULL,
	attest_env      TEXT NOT NULL CHECK (attest_env IN ('development','production')),
	sign_count      BIGINT NOT NULL DEFAULT 0,
	receipt         BYTEA NOT NULL,
	created_at      TIMESTAMPTZ NOT NULL,
	last_used_at    TIMESTAMPTZ NOT NULL,
	revoked_at      TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS relay_grants (
	grant_id               UUID PRIMARY KEY,
	app_id                 TEXT NOT NULL,
	key_id                 TEXT NOT NULL REFERENCES attest_keys(key_id) ON DELETE CASCADE,
	client_grant_key       UUID NOT NULL,
	secret_hash            BYTEA NOT NULL UNIQUE,
	reg_id                 UUID NOT NULL,
	inbox_contract_version SMALLINT NOT NULL CHECK (inbox_contract_version >= 1),
	apns_token_enc         BYTEA NOT NULL,
	token_hash             BYTEA NOT NULL,
	token_version          BIGINT NOT NULL DEFAULT 1,
	token_updated_at       TIMESTAMPTZ NOT NULL,
	apns_env               TEXT NOT NULL CHECK (apns_env IN ('production','sandbox')),
	expires_at             TIMESTAMPTZ NOT NULL,
	revoked_at             TIMESTAMPTZ,
	invalidated_at         TIMESTAMPTZ,
	invalid_reason         TEXT NOT NULL DEFAULT '',
	created_at             TIMESTAMPTZ NOT NULL,
	updated_at             TIMESTAMPTZ NOT NULL,
	last_used_at           TIMESTAMPTZ,
	UNIQUE (key_id, client_grant_key)
);
CREATE INDEX IF NOT EXISTS idx_relay_grants_key_active
	ON relay_grants (key_id, apns_env)
	WHERE revoked_at IS NULL AND invalidated_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_relay_grants_cleanup
	ON relay_grants (expires_at, revoked_at, invalidated_at);

CREATE TABLE IF NOT EXISTS cancelled_client_grant_keys (
	key_id           TEXT NOT NULL REFERENCES attest_keys(key_id) ON DELETE CASCADE,
	client_grant_key UUID NOT NULL,
	cancelled_at     TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (key_id, client_grant_key)
);

CREATE TABLE IF NOT EXISTS grant_daily_usage (
	grant_id   UUID NOT NULL REFERENCES relay_grants(grant_id) ON DELETE CASCADE,
	utc_day    DATE NOT NULL,
	sent_count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (grant_id, utc_day)
);
`

type Store struct {
	pool     *pgxpool.Pool
	cipher   *TokenCipher
	verifier *AppAttestVerifier
	now      func() time.Time
}

func NewStore(pool *pgxpool.Pool, cipher *TokenCipher, verifier *AppAttestVerifier) *Store {
	return &Store{
		pool:     pool,
		cipher:   cipher,
		verifier: verifier,
		now:      time.Now,
	}
}

func (s *Store) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, relaySchemaDDL)
	return err
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

type Challenge struct {
	ID        uuid.UUID
	NonceB64  string
	ExpiresAt time.Time
}

func (s *Store) IssueChallenge(ctx context.Context, appID, keyID, op string) (Challenge, error) {
	if !ValidAttestOp(op) {
		return Challenge{}, ErrChallengeInvalid
	}
	if op == OpAttestRegister {
		keyID = ""
	} else if keyID == "" {
		return Challenge{}, ErrChallengeInvalid
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Challenge{}, err
	}
	now := s.currentTime()
	exp := now.Add(ChallengeTTL)
	id := uuid.New()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO attest_challenges (id, app_id, key_id, op, nonce, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, appID, keyID, op, nonce, exp, now,
	); err != nil {
		return Challenge{}, err
	}
	return Challenge{
		ID:        id,
		NonceB64:  base64.RawURLEncoding.EncodeToString(nonce),
		ExpiresAt: exp,
	}, nil
}

func consumeChallengeTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, appID, keyID, op string, now time.Time) ([]byte, error) {
	var nonce []byte
	err := tx.QueryRow(ctx, `
		DELETE FROM attest_challenges
		 WHERE id = $1
		   AND app_id = $2
		   AND key_id = $3
		   AND op = $4
		   AND expires_at > $5
		 RETURNING nonce`,
		id, appID, keyID, op, now,
	).Scan(&nonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrChallengeInvalid
	}
	return nonce, err
}

// RegisterAttestedKey atomically consumes the registration challenge, validates
// the Apple App Attest attestationObject, and stores the verified key.
func (s *Store) RegisterAttestedKey(ctx context.Context, app *AppConfig, keyID string, challengeID uuid.UUID, attestationObj []byte) (*VerifiedAttestation, error) {
	now := s.currentTime()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	nonce, err := consumeChallengeTx(ctx, tx, challengeID, app.AppID, "", OpAttestRegister, now)
	if err != nil {
		return nil, err
	}
	nonceB64 := base64.RawURLEncoding.EncodeToString(nonce)
	canonical := CanonicalRegisterPayload(app.AppID, keyID, challengeID.String(), nonceB64)
	clientDataHash := sha256.Sum256(canonical)

	verified, err := s.verifier.VerifyAttestation(app.AppleAppID, keyID, clientDataHash[:], attestationObj, app.AllowSandbox)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO attest_keys (key_id, app_id, public_key_spki, attest_env, sign_count, receipt, created_at, last_used_at, revoked_at)
		VALUES ($1, $2, $3, $4, 0, $5, $6, $6, NULL)
		ON CONFLICT (key_id) DO NOTHING`,
		verified.KeyID, app.AppID, verified.PublicKeySPKI, verified.AttestEnv, verified.Receipt, now,
	)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrKeyAlreadyRegistered
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return verified, nil
}

type attestedKeyRow struct {
	KeyID         string
	AppID         string
	PublicKeySPKI []byte
	AttestEnv     string
	SignCount     int64
}

type AssertionContext struct {
	App         *AppConfig
	KeyID       string
	Op          string
	ChallengeID uuid.UUID
	NonceB64    string
	RawBody     []byte
	Assertion   []byte
}

func (s *Store) withVerifiedAssertion(ctx context.Context, ac AssertionContext, fn func(tx pgx.Tx, key attestedKeyRow, now time.Time) error) error {
	now := s.currentTime()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var key attestedKeyRow
	var revokedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT key_id, app_id, public_key_spki, attest_env, sign_count, revoked_at
		  FROM attest_keys
		 WHERE key_id = $1
		   AND app_id = $2
		   FOR UPDATE`,
		ac.KeyID, ac.App.AppID,
	).Scan(&key.KeyID, &key.AppID, &key.PublicKeySPKI, &key.AttestEnv, &key.SignCount, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) || revokedAt != nil {
		return ErrKeyNotFound
	}
	if err != nil {
		return err
	}

	nonce, err := consumeChallengeTx(ctx, tx, ac.ChallengeID, ac.App.AppID, ac.KeyID, ac.Op, now)
	if err != nil {
		return err
	}
	expectedNonceB64 := base64.RawURLEncoding.EncodeToString(nonce)
	if ac.NonceB64 != expectedNonceB64 {
		return ErrChallengeInvalid
	}

	clientDataHash := sha256.Sum256(ac.RawBody)
	newSignCount, err := s.verifier.VerifyAssertion(ac.App.AppleAppID, key.PublicKeySPKI, key.SignCount, clientDataHash[:], ac.Assertion)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE attest_keys
		   SET sign_count = $2,
		       last_used_at = $3
		 WHERE key_id = $1`,
		key.KeyID, newSignCount, now,
	); err != nil {
		return err
	}

	if err := fn(tx, key, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type CreateGrantInput struct {
	ClientGrantKey       uuid.UUID
	SecretHash           []byte
	RegID                uuid.UUID
	InboxContractVersion int
	APNsToken            string
	APNsEnv              string
}

type GrantSummary struct {
	GrantID   uuid.UUID
	ExpiresAt time.Time
	Replayed  bool
}

func tokenAAD(appID, keyID string, grantID uuid.UUID) []byte {
	return []byte(appID + "\x00" + keyID + "\x00" + grantID.String())
}

// CreateGrant creates a new delivery grant or idempotently replays an existing
// active grant matching (key_id, client_grant_key) without rotating secret_hash.
func (s *Store) CreateGrant(ctx context.Context, ac AssertionContext, in CreateGrantInput) (GrantSummary, error) {
	var out GrantSummary
	err := s.withVerifiedAssertion(ctx, ac, func(tx pgx.Tx, key attestedKeyRow, now time.Time) error {
		if !ac.App.AllowsEnvForAttest(key.AttestEnv, in.APNsEnv) {
			return ErrEnvNotAllowed
		}
		var cancelled bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM cancelled_client_grant_keys
				 WHERE key_id = $1 AND client_grant_key = $2
			)`, key.KeyID, in.ClientGrantKey).Scan(&cancelled); err != nil {
			return err
		}
		if cancelled {
			return ErrGrantConflict
		}

		// Check for idempotent replay by (key_id, client_grant_key).
		var existingID uuid.UUID
		var existingSecretHash []byte
		var existingRegID uuid.UUID
		var existingContract int
		var existingEnv string
		var existingExp time.Time
		var existingRevoked, existingInvalidated *time.Time
		err := tx.QueryRow(ctx, `
			SELECT grant_id, secret_hash, reg_id, inbox_contract_version, apns_env,
			       expires_at, revoked_at, invalidated_at
			  FROM relay_grants
			 WHERE key_id = $1
			   AND client_grant_key = $2
			   FOR UPDATE`,
			key.KeyID, in.ClientGrantKey,
		).Scan(
			&existingID, &existingSecretHash, &existingRegID, &existingContract, &existingEnv,
			&existingExp, &existingRevoked, &existingInvalidated,
		)
		if err == nil {
			if existingRevoked != nil || existingInvalidated != nil || !existingExp.After(now) {
				return ErrGrantConflict
			}
			if !bytes.Equal(existingSecretHash, in.SecretHash) ||
				existingRegID != in.RegID ||
				existingContract != in.InboxContractVersion ||
				existingEnv != in.APNsEnv {
				return ErrGrantConflict
			}
			out = GrantSummary{GrantID: existingID, ExpiresAt: existingExp, Replayed: true}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// Enforce active grant cap and daily creation cap per attested key.
		var activeCount, createdToday int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FILTER (WHERE revoked_at IS NULL AND invalidated_at IS NULL AND expires_at > $2),
			       COUNT(*) FILTER (WHERE created_at >= $2 - INTERVAL '24 hours')
			  FROM relay_grants
			 WHERE key_id = $1`,
			key.KeyID, now,
		).Scan(&activeCount, &createdToday); err != nil {
			return err
		}
		if activeCount >= MaxActiveGrantsPerKey || createdToday >= MaxGrantsCreatedPerDay {
			return ErrGrantLimitExceeded
		}

		// Ensure secret_hash is not already taken by another grant.
		var secretExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM relay_grants WHERE secret_hash = $1)`, in.SecretHash).Scan(&secretExists); err != nil {
			return err
		}
		if secretExists {
			return ErrGrantConflict
		}

		grantID := uuid.New()
		encToken, err := s.cipher.Encrypt(in.APNsToken, tokenAAD(ac.App.AppID, key.KeyID, grantID))
		if err != nil {
			return err
		}
		tokHash := sha256.Sum256([]byte(in.APNsToken))
		exp := now.Add(GrantTTL)
		if _, err := tx.Exec(ctx, `
			INSERT INTO relay_grants (
				grant_id, app_id, key_id, client_grant_key, secret_hash, reg_id,
				inbox_contract_version, apns_token_enc, token_hash, token_version, token_updated_at,
				apns_env, expires_at, revoked_at, invalidated_at, invalid_reason,
				created_at, updated_at, last_used_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, 1, $10,
				$11, $12, NULL, NULL, '',
				$10, $10, NULL
			)`,
			grantID, ac.App.AppID, key.KeyID, in.ClientGrantKey, in.SecretHash, in.RegID,
			in.InboxContractVersion, encToken, tokHash[:], now,
			in.APNsEnv, exp,
		); err != nil {
			return err
		}
		out = GrantSummary{GrantID: grantID, ExpiresAt: exp, Replayed: false}
		return nil
	})
	return out, err
}

type RefreshGrantInput struct {
	GrantID              uuid.UUID
	APNsToken            string
	InboxContractVersion *int
}

func (s *Store) RefreshGrant(ctx context.Context, ac AssertionContext, in RefreshGrantInput) (GrantSummary, error) {
	var out GrantSummary
	err := s.withVerifiedAssertion(ctx, ac, func(tx pgx.Tx, key attestedKeyRow, now time.Time) error {
		var existingTokenHash []byte
		var existingTokenVersion int64
		var existingTokenUpdatedAt time.Time
		var existingTokenEnc []byte
		var existingContract int
		var revokedAt, invalidatedAt *time.Time
		var expiresAt time.Time
		err := tx.QueryRow(ctx, `
			SELECT token_hash, token_version, token_updated_at, apns_token_enc,
			       inbox_contract_version, revoked_at, invalidated_at, expires_at
			  FROM relay_grants
			 WHERE grant_id = $1
			   AND key_id = $2
			   AND app_id = $3
			   FOR UPDATE`,
			in.GrantID, key.KeyID, ac.App.AppID,
		).Scan(&existingTokenHash, &existingTokenVersion, &existingTokenUpdatedAt, &existingTokenEnc,
			&existingContract, &revokedAt, &invalidatedAt, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGrantNotFound
		}
		if err != nil {
			return err
		}
		if revokedAt != nil || invalidatedAt != nil || !expiresAt.After(now) {
			return ErrGrantConflict
		}

		newEnc := existingTokenEnc
		newHash := existingTokenHash
		newVer := existingTokenVersion
		newTokUpdatedAt := existingTokenUpdatedAt
		if in.APNsToken != "" {
			h := sha256.Sum256([]byte(in.APNsToken))
			if !bytes.Equal(h[:], existingTokenHash) {
				enc, encErr := s.cipher.Encrypt(in.APNsToken, tokenAAD(ac.App.AppID, key.KeyID, in.GrantID))
				if encErr != nil {
					return encErr
				}
				newEnc = enc
				newHash = h[:]
				newVer++
				newTokUpdatedAt = now
			}
		}
		newContract := existingContract
		if in.InboxContractVersion != nil {
			newContract = *in.InboxContractVersion
		}
		newExp := now.Add(GrantTTL)
		if _, err := tx.Exec(ctx, `
			UPDATE relay_grants
			   SET apns_token_enc = $2,
			       token_hash = $3,
			       token_version = $4,
			       token_updated_at = $5,
			       inbox_contract_version = $6,
			       expires_at = $7,
			       updated_at = $8
			 WHERE grant_id = $1`,
			in.GrantID, newEnc, newHash, newVer, newTokUpdatedAt, newContract, newExp, now,
		); err != nil {
			return err
		}
		out = GrantSummary{GrantID: in.GrantID, ExpiresAt: newExp}
		return nil
	})
	return out, err
}

// UpdateDeviceToken rotates the APNs device token across all active grants for
// (key_id, app_id, apns_env), bumping token_version and token_updated_at.
func (s *Store) UpdateDeviceToken(ctx context.Context, ac AssertionContext, apnsToken, apnsEnv string) (int, error) {
	var updated int
	err := s.withVerifiedAssertion(ctx, ac, func(tx pgx.Tx, key attestedKeyRow, now time.Time) error {
		if !ac.App.AllowsEnvForAttest(key.AttestEnv, apnsEnv) {
			return ErrEnvNotAllowed
		}
		rows, err := tx.Query(ctx, `
			SELECT grant_id, token_hash
			  FROM relay_grants
			 WHERE key_id = $1
			   AND app_id = $2
			   AND apns_env = $3
			   AND revoked_at IS NULL
			   AND invalidated_at IS NULL
			   AND expires_at > $4
			   FOR UPDATE`,
			key.KeyID, ac.App.AppID, apnsEnv, now,
		)
		if err != nil {
			return err
		}
		type target struct {
			grantID   uuid.UUID
			tokenHash []byte
		}
		var targets []target
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.grantID, &t.tokenHash); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		newHash := sha256.Sum256([]byte(apnsToken))
		for _, t := range targets {
			if bytes.Equal(t.tokenHash, newHash[:]) {
				continue
			}
			enc, err := s.cipher.Encrypt(apnsToken, tokenAAD(ac.App.AppID, key.KeyID, t.grantID))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE relay_grants
				   SET apns_token_enc = $2,
				       token_hash = $3,
				       token_version = token_version + 1,
				       token_updated_at = $4,
				       updated_at = $4
				 WHERE grant_id = $1`,
				t.grantID, enc, newHash[:], now,
			); err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	return updated, err
}

func (s *Store) RevokeGrant(ctx context.Context, ac AssertionContext, grantID uuid.UUID) (bool, error) {
	var revoked bool
	err := s.withVerifiedAssertion(ctx, ac, func(tx pgx.Tx, key attestedKeyRow, now time.Time) error {
		tag, err := tx.Exec(ctx, `
			UPDATE relay_grants
			   SET revoked_at = COALESCE(revoked_at, $4),
			       updated_at = $4
			 WHERE grant_id = $1
			   AND key_id = $2
			   AND app_id = $3`,
			grantID, key.KeyID, ac.App.AppID, now,
		)
		if err != nil {
			return err
		}
		revoked = tag.RowsAffected() > 0
		return nil
	})
	return revoked, err
}

// CancelGrantByClientKey records a permanent (for the lifetime of attest_keys)
// tombstone in cancelled_client_grant_keys and revokes any grant created with
// the same (key_id, client_grant_key).
func (s *Store) CancelGrantByClientKey(ctx context.Context, ac AssertionContext, clientGrantKey uuid.UUID) error {
	return s.withVerifiedAssertion(ctx, ac, func(tx pgx.Tx, key attestedKeyRow, now time.Time) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO cancelled_client_grant_keys (key_id, client_grant_key, cancelled_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (key_id, client_grant_key) DO NOTHING`,
			key.KeyID, clientGrantKey, now,
		); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE relay_grants
			   SET revoked_at = COALESCE(revoked_at, $4),
			       updated_at = $4
			 WHERE key_id = $1
			   AND app_id = $2
			   AND client_grant_key = $3`,
			key.KeyID, ac.App.AppID, clientGrantKey, now,
		)
		return err
	})
}

type GrantLookupStatus string

const (
	GrantStatusActive      GrantLookupStatus = "active"
	GrantStatusNotFound    GrantLookupStatus = "not_found"
	GrantStatusRevoked     GrantLookupStatus = "revoked"
	GrantStatusExpired     GrantLookupStatus = "expired"
	GrantStatusInvalidated GrantLookupStatus = "invalidated"
)

type GrantSnapshot struct {
	GrantID              uuid.UUID
	AppID                string
	KeyID                string
	RegID                uuid.UUID
	InboxContractVersion int
	APNsToken            string
	TokenHash            []byte
	TokenVersion         int64
	TokenUpdatedAt       time.Time
	APNsEnv              string
	ExpiresAt            time.Time
	InvalidReason        string
	Status               GrantLookupStatus
}

func (s *Store) LookupGrantBySecret(ctx context.Context, deliverySecret string) (GrantSnapshot, error) {
	sum := sha256.Sum256([]byte(deliverySecret))
	var g GrantSnapshot
	var encToken []byte
	var revokedAt, invalidatedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT grant_id, app_id, key_id, reg_id, inbox_contract_version,
		       apns_token_enc, token_hash, token_version, token_updated_at,
		       apns_env, expires_at, revoked_at, invalidated_at, invalid_reason
		  FROM relay_grants
		 WHERE secret_hash = $1`,
		sum[:],
	).Scan(
		&g.GrantID, &g.AppID, &g.KeyID, &g.RegID, &g.InboxContractVersion,
		&encToken, &g.TokenHash, &g.TokenVersion, &g.TokenUpdatedAt,
		&g.APNsEnv, &g.ExpiresAt, &revokedAt, &invalidatedAt, &g.InvalidReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GrantSnapshot{Status: GrantStatusNotFound}, nil
	}
	if err != nil {
		return GrantSnapshot{}, err
	}
	now := s.currentTime()
	switch {
	case revokedAt != nil:
		g.Status = GrantStatusRevoked
		return g, nil
	case invalidatedAt != nil:
		g.Status = GrantStatusInvalidated
		return g, nil
	case !g.ExpiresAt.After(now):
		g.Status = GrantStatusExpired
		return g, nil
	}
	plain, err := s.cipher.Decrypt(encToken, tokenAAD(g.AppID, g.KeyID, g.GrantID))
	if err != nil {
		return GrantSnapshot{}, fmt.Errorf("decrypt token: %w", err)
	}
	g.APNsToken = plain
	g.Status = GrantStatusActive
	return g, nil
}

// ReserveDailyDeliveryQuota atomically checks and increments the per-grant daily
// delivery counter and updates last_used_at.
func (s *Store) ReserveDailyDeliveryQuota(ctx context.Context, grantID uuid.UUID, dailyLimit int) error {
	if dailyLimit <= 0 {
		dailyLimit = DefaultDailyGrantLimit
	}
	now := s.currentTime()
	utcDay := now.Format("2006-01-02")
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var count int
	err = tx.QueryRow(ctx, `
		INSERT INTO grant_daily_usage (grant_id, utc_day, sent_count)
		VALUES ($1, $2::date, 1)
		ON CONFLICT (grant_id, utc_day) DO UPDATE
		   SET sent_count = grant_daily_usage.sent_count + 1
		 WHERE grant_daily_usage.sent_count < $3
		 RETURNING sent_count`,
		grantID, utcDay, dailyLimit,
	).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDailyQuotaExceeded
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE relay_grants SET last_used_at = $2 WHERE grant_id = $1`, grantID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type InvalidationResult string

const (
	InvalidationResultInvalidated    InvalidationResult = "invalidated"
	InvalidationResultTokenRotated   InvalidationResult = "token_rotated"
	InvalidationResultAlreadyInvalid InvalidationResult = "already_invalid"
	InvalidationResultStaleTimestamp InvalidationResult = "stale_timestamp"
)

// ConditionallyInvalidateGrant invalidates a grant after a terminal APNs token
// rejection ONLY when token_version and token_hash match the sent snapshot, and
// (for 410 responses with a timestamp) appleTimestamp >= token_updated_at.
// For 400 responses, appleTimestamp must be nil.
func (s *Store) ConditionallyInvalidateGrant(
	ctx context.Context,
	grantID uuid.UUID,
	sentTokenVersion int64,
	sentTokenHash []byte,
	reason string,
	appleTimestamp *time.Time,
) (InvalidationResult, error) {
	now := s.currentTime()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE relay_grants
		   SET invalidated_at = $6,
		       invalid_reason = $4,
		       updated_at = $6
		 WHERE grant_id = $1
		   AND token_version = $2
		   AND token_hash = $3
		   AND invalidated_at IS NULL
		   AND revoked_at IS NULL
		   AND ($5::timestamptz IS NULL OR $5::timestamptz >= token_updated_at)`,
		grantID, sentTokenVersion, sentTokenHash, reason, appleTimestamp, now,
	)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return InvalidationResultInvalidated, nil
	}

	// Re-read grant to distinguish token rotation from already-invalid or stale 410 timestamp.
	var curVersion int64
	var curHash []byte
	var revokedAt, invalidatedAt *time.Time
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT token_version, token_hash, revoked_at, invalidated_at, expires_at
		  FROM relay_grants
		 WHERE grant_id = $1`,
		grantID,
	).Scan(&curVersion, &curHash, &revokedAt, &invalidatedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvalidationResultAlreadyInvalid, tx.Commit(ctx)
	}
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if revokedAt != nil || invalidatedAt != nil || !expiresAt.After(now) {
		return InvalidationResultAlreadyInvalid, nil
	}
	if curVersion > sentTokenVersion || !bytes.Equal(curHash, sentTokenHash) {
		return InvalidationResultTokenRotated, nil
	}
	return InvalidationResultStaleTimestamp, nil
}

// Cleanup removes expired challenges and long-expired/revoked/invalidated grants.
// Intentionally NEVER deletes rows from cancelled_client_grant_keys: tombstones
// live for the entire lifetime of their parent attest_keys row (ON DELETE CASCADE).
func (s *Store) Cleanup(ctx context.Context) error {
	now := s.currentTime()
	cutoff := now.Add(-GrantCleanupRetention)
	if _, err := s.pool.Exec(ctx, `DELETE FROM attest_challenges WHERE expires_at <= $1`, now); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM relay_grants
		 WHERE (expires_at <= $1)
		    OR (revoked_at IS NOT NULL AND revoked_at <= $1)
		    OR (invalidated_at IS NOT NULL AND invalidated_at <= $1)`,
		cutoff,
	); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM grant_daily_usage WHERE utc_day < $1::date`, cutoff.Format("2006-01-02")); err != nil {
		return err
	}
	return nil
}

func (s *Store) CountActiveGrantsByApp(ctx context.Context) (map[string]int, error) {
	now := s.currentTime()
	rows, err := s.pool.Query(ctx, `
		SELECT app_id, COUNT(*)
		  FROM relay_grants
		 WHERE revoked_at IS NULL
		   AND invalidated_at IS NULL
		   AND expires_at > $1
		 GROUP BY app_id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var appID string
		var cnt int
		if err := rows.Scan(&appID, &cnt); err != nil {
			return nil, err
		}
		out[appID] = cnt
	}
	return out, rows.Err()
}
