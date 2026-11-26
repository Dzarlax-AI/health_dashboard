package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	SettingAPNSPushEnabled = "apns_push_enabled"

	PushEnvProduction = "production"
	PushEnvSandbox    = "sandbox"

	PushDeliveryStatePending   = "pending"
	PushDeliveryStateSending   = "sending"
	PushDeliveryStateRetryWait = "retry_wait"
	PushDeliveryStateAccepted  = "accepted"
	PushDeliveryStateRejected  = "rejected"
	PushDeliveryStateExpired   = "expired"
	PushDeliveryStateAmbiguous = "ambiguous"

	PushAttemptStatusSending         = "sending"
	PushAttemptStatusAccepted        = "accepted"
	PushAttemptStatusRetryableFailed = "retryable_failed"
	PushAttemptStatusRejected        = "rejected"
	PushAttemptStatusAmbiguous       = "ambiguous"

	PushDeviceStaleTTL = 180 * 24 * time.Hour
	PushLeaseDuration  = 45 * time.Second
	PushMaxAttempts    = 5

	morningReportPushTTL = 12 * time.Hour
)

// ErrPushLeaseLost indicates that a delivery lease expired or was reclaimed
// before an attempt could start or complete.
var ErrPushLeaseLost = errors.New("push delivery lease is no longer active")

// ErrPushDeliveryIneligible means the device or the event changed after claim.
var ErrPushDeliveryIneligible = errors.New("push delivery is no longer eligible")

const pushTablesDDL = `
CREATE TABLE IF NOT EXISTS push_devices (
	device_id            UUID PRIMARY KEY,
	binding_id           UUID NOT NULL UNIQUE,
	apns_token           TEXT NOT NULL,
	token_hash           BYTEA NOT NULL,
	apns_env             TEXT NOT NULL CHECK (apns_env IN ('production','sandbox')),
	bundle_id            TEXT NOT NULL,
	locale               TEXT NOT NULL,
	registration_version BIGINT NOT NULL DEFAULT 1,
	enabled              BOOLEAN NOT NULL DEFAULT TRUE,
	invalidated_at       TIMESTAMPTZ,
	invalid_reason       TEXT NOT NULL DEFAULT '',
	created_at           TIMESTAMPTZ NOT NULL,
	updated_at           TIMESTAMPTZ NOT NULL,
	last_seen_at         TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_push_devices_active_token
	ON push_devices (token_hash, apns_env)
	WHERE enabled AND invalidated_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_push_devices_active
	ON push_devices (device_id)
	WHERE enabled AND invalidated_at IS NULL;

CREATE TABLE IF NOT EXISTS push_deliveries (
	id                   UUID PRIMARY KEY,
	delivery_key         TEXT NOT NULL UNIQUE,
	inbox_item_id        UUID NOT NULL REFERENCES inbox_items(id),
	kind                 TEXT NOT NULL CHECK (kind IN ('morning_report','prompt_checkin')),
	device_id            UUID NOT NULL REFERENCES push_devices(device_id),
	binding_id           UUID NOT NULL,
	registration_version BIGINT NOT NULL,
	token_hash           BYTEA NOT NULL,
	apns_env             TEXT NOT NULL CHECK (apns_env IN ('production','sandbox')),
	locale               TEXT NOT NULL,
	apns_id              UUID NOT NULL,
	collapse_id          TEXT NOT NULL,
	state                TEXT NOT NULL CHECK (state IN ('pending','sending','retry_wait','accepted','rejected','expired','ambiguous')),
	lease_token          UUID,
	lease_until          TIMESTAMPTZ,
	attempts             INTEGER NOT NULL DEFAULT 0,
	next_attempt_at      TIMESTAMPTZ NOT NULL,
	expires_at           TIMESTAMPTZ NOT NULL,
	status_code          INTEGER,
	error_reason         TEXT NOT NULL DEFAULT '',
	created_at           TIMESTAMPTZ NOT NULL,
	updated_at           TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_push_deliveries_due
	ON push_deliveries (next_attempt_at, id)
	WHERE state IN ('pending','retry_wait');
CREATE INDEX IF NOT EXISTS idx_push_deliveries_sending
	ON push_deliveries (lease_until)
	WHERE state = 'sending';

CREATE TABLE IF NOT EXISTS push_delivery_attempts (
	id           UUID PRIMARY KEY,
	delivery_id  UUID NOT NULL REFERENCES push_deliveries(id),
	lease_token  UUID NOT NULL,
	status       TEXT NOT NULL CHECK (status IN ('sending','accepted','retryable_failed','rejected','ambiguous')),
	status_code  INTEGER,
	apns_reason  TEXT NOT NULL DEFAULT '',
	attempted_at TIMESTAMPTZ NOT NULL,
	completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_push_attempts_delivery
	ON push_delivery_attempts (delivery_id, attempted_at, id);
`

func (s *DB) EnsurePushTablesContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, pushTablesDDL)
	return err
}

// IsAPNSPushEnabled reports whether Direct APNs push enqueueing and delivery
// are enabled for this tenant. Defaults to false for phased per-profile rollout.
func IsAPNSPushEnabled(s *DB) bool {
	ctx, cancel := queryCtx()
	defer cancel()
	return apnsPushEnabled(ctx, s)
}

func apnsPushEnabled(ctx context.Context, s *DB) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("APNS_PUSH_ENABLED")))
	defaultEnabled := raw == "true" || raw == "1" || raw == "on" || raw == "yes"
	if s == nil {
		return defaultEnabled
	}

	var setting string
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, SettingAPNSPushEnabled).Scan(&setting)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultEnabled
	}
	if err != nil {
		return false
	}
	enabled, err := strconv.ParseBool(setting)
	return err == nil && enabled
}

func ValidAPNsToken(token string) bool {
	if len(token) < 64 || len(token) > 1024 || len(token)%2 != 0 {
		return false
	}
	if strings.ToLower(token) != token {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func ValidAPNsEnv(env string) bool {
	return env == PushEnvProduction || env == PushEnvSandbox
}

func ValidPushLocale(locale string) bool {
	return locale == "en" || locale == "ru" || locale == "sr"
}

func PushTokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

type UpsertPushDeviceInput struct {
	DeviceID  uuid.UUID
	APNsToken string
	APNsEnv   string
	BundleID  string
	Locale    string
}

type PushDevice struct {
	DeviceID            uuid.UUID
	BindingID           uuid.UUID
	APNsToken           string
	TokenHash           []byte
	APNsEnv             string
	BundleID            string
	Locale              string
	RegistrationVersion int64
	Enabled             bool
	InvalidatedAt       *time.Time
	InvalidReason       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	LastSeenAt          time.Time
}

// UpsertPushDevice registers or updates an iOS push installation inside the
// current tenant schema. Active token uniqueness within the tenant is enforced
// atomically by deactivating any other device holding the same (token_hash, apns_env)
// before upserting the target device_id. Re-enabling a previously disabled or
// invalidated device issues a fresh binding_id and bumps registration_version.
func (s *DB) UpsertPushDevice(ctx context.Context, in UpsertPushDeviceInput) (PushDevice, error) {
	if in.DeviceID == uuid.Nil {
		return PushDevice{}, errors.New("invalid device_id")
	}
	if !ValidAPNsToken(in.APNsToken) {
		return PushDevice{}, errors.New("invalid apns_token")
	}
	if !ValidAPNsEnv(in.APNsEnv) {
		return PushDevice{}, errors.New("invalid apns_env")
	}
	if strings.TrimSpace(in.BundleID) == "" {
		return PushDevice{}, errors.New("invalid bundle_id")
	}
	if !ValidPushLocale(in.Locale) {
		return PushDevice{}, errors.New("invalid locale")
	}
	tokenHash := PushTokenHash(in.APNsToken)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PushDevice{}, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('push_devices:' || current_schema()))`); err != nil {
		return PushDevice{}, err
	}

	// Atomically deactivate any other device_id holding this active token in this tenant.
	if _, err = tx.Exec(ctx, `
		UPDATE push_devices
		   SET enabled = FALSE,
		       updated_at = clock_timestamp()
		 WHERE device_id <> $1
		   AND token_hash = $2
		   AND apns_env = $3
		   AND enabled
		   AND invalidated_at IS NULL`,
		in.DeviceID, tokenHash, in.APNsEnv,
	); err != nil {
		return PushDevice{}, err
	}

	var existingBinding uuid.UUID
	var existingHash []byte
	var existingEnv string
	var existingVersion int64
	var existingEnabled bool
	var existingInvalidated *time.Time
	err = tx.QueryRow(ctx, `
		SELECT binding_id, token_hash, apns_env, registration_version, enabled, invalidated_at
		  FROM push_devices
		 WHERE device_id = $1
		   FOR UPDATE`, in.DeviceID,
	).Scan(&existingBinding, &existingHash, &existingEnv, &existingVersion, &existingEnabled, &existingInvalidated)

	var out PushDevice
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		bindingID := uuid.New()
		err = tx.QueryRow(ctx, `
			INSERT INTO push_devices (
				device_id, binding_id, apns_token, token_hash, apns_env, bundle_id, locale,
				registration_version, enabled, invalidated_at, invalid_reason,
				created_at, updated_at, last_seen_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				1, TRUE, NULL, '',
				clock_timestamp(), clock_timestamp(), clock_timestamp()
			)
			RETURNING device_id, binding_id, apns_token, token_hash, apns_env, bundle_id, locale,
			          registration_version, enabled, invalidated_at, invalid_reason,
			          created_at, updated_at, last_seen_at`,
			in.DeviceID, bindingID, in.APNsToken, tokenHash, in.APNsEnv, in.BundleID, in.Locale,
		).Scan(
			&out.DeviceID, &out.BindingID, &out.APNsToken, &out.TokenHash, &out.APNsEnv, &out.BundleID, &out.Locale,
			&out.RegistrationVersion, &out.Enabled, &out.InvalidatedAt, &out.InvalidReason,
			&out.CreatedAt, &out.UpdatedAt, &out.LastSeenAt,
		)
	case err == nil:
		bindingID := existingBinding
		version := existingVersion
		tokenChanged := !bytesEqual(existingHash, tokenHash) || existingEnv != in.APNsEnv
		if !existingEnabled || existingInvalidated != nil {
			bindingID = uuid.New()
			version++
		} else if tokenChanged {
			version++
		}
		err = tx.QueryRow(ctx, `
			UPDATE push_devices
			   SET binding_id = $2,
			       apns_token = $3,
			       token_hash = $4,
			       apns_env = $5,
			       bundle_id = $6,
			       locale = $7,
			       registration_version = $8,
			       enabled = TRUE,
			       invalidated_at = NULL,
			       invalid_reason = '',
			       updated_at = clock_timestamp(),
			       last_seen_at = clock_timestamp()
			 WHERE device_id = $1
			 RETURNING device_id, binding_id, apns_token, token_hash, apns_env, bundle_id, locale,
			           registration_version, enabled, invalidated_at, invalid_reason,
			           created_at, updated_at, last_seen_at`,
			in.DeviceID, bindingID, in.APNsToken, tokenHash, in.APNsEnv, in.BundleID, in.Locale, version,
		).Scan(
			&out.DeviceID, &out.BindingID, &out.APNsToken, &out.TokenHash, &out.APNsEnv, &out.BundleID, &out.Locale,
			&out.RegistrationVersion, &out.Enabled, &out.InvalidatedAt, &out.InvalidReason,
			&out.CreatedAt, &out.UpdatedAt, &out.LastSeenAt,
		)
	default:
		return PushDevice{}, err
	}
	if err != nil {
		return PushDevice{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PushDevice{}, err
	}
	return out, nil
}

// DisablePushDeviceBinding disables only the exact (device_id, binding_id)
// registration. A delayed offline DELETE for an earlier binding_id cannot
// disable a newer registration created after returning to the profile.
func (s *DB) DisablePushDeviceBinding(ctx context.Context, deviceID, bindingID uuid.UUID) (bool, error) {
	if deviceID == uuid.Nil || bindingID == uuid.Nil {
		return false, errors.New("device_id and binding_id are required")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE push_devices
		   SET enabled = FALSE,
		       updated_at = clock_timestamp()
		 WHERE device_id = $1
		   AND binding_id = $2
		   AND enabled`,
		deviceID, bindingID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func pushCollapseID(kind string, inboxItemID uuid.UUID) string {
	prefix := "mr-"
	if kind == InboxKindPromptCheckin {
		prefix = "ci-"
	}
	return prefix + inboxItemID.String()
}

func pushDeliveryKey(kind string, inboxItemID, deviceID uuid.UUID) string {
	return fmt.Sprintf("push:%s:%s:%s", kind, inboxItemID.String(), deviceID.String())
}

// enqueuePushDeliveriesTx atomically inserts pending push_deliveries rows for
// all currently active, non-stale devices of this tenant inside the card
// creation transaction. Backfilling on later ticks is intentionally forbidden.
func enqueuePushDeliveriesTx(ctx context.Context, tx pgx.Tx, inboxItemID uuid.UUID, kind string, expiresAt time.Time) error {
	if inboxItemID == uuid.Nil || (kind != InboxKindMorningReport && kind != InboxKindPromptCheckin) {
		return errors.New("invalid push delivery event")
	}
	rows, err := tx.Query(ctx, `
		SELECT device_id, binding_id, registration_version, token_hash, apns_env, locale
		  FROM push_devices
		 WHERE enabled
		   AND invalidated_at IS NULL
		   AND last_seen_at > clock_timestamp() - INTERVAL '180 days'
		 ORDER BY device_id`)
	if err != nil {
		return err
	}
	type target struct {
		deviceID            uuid.UUID
		bindingID           uuid.UUID
		registrationVersion int64
		tokenHash           []byte
		apnsEnv             string
		locale              string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.deviceID, &t.bindingID, &t.registrationVersion, &t.tokenHash, &t.apnsEnv, &t.locale); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	collapse := pushCollapseID(kind, inboxItemID)
	for _, t := range targets {
		deliveryID := uuid.New()
		apnsID := uuid.New()
		key := pushDeliveryKey(kind, inboxItemID, t.deviceID)
		if _, err := tx.Exec(ctx, `
			INSERT INTO push_deliveries (
				id, delivery_key, inbox_item_id, kind, device_id, binding_id,
				registration_version, token_hash, apns_env, locale, apns_id, collapse_id,
				state, lease_token, lease_until, attempts, next_attempt_at, expires_at,
				status_code, error_reason, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10, $11, $12,
				'pending', NULL, NULL, 0, clock_timestamp(), $13,
				NULL, '', clock_timestamp(), clock_timestamp()
			)
			ON CONFLICT (delivery_key) DO NOTHING`,
			deliveryID, key, inboxItemID, kind, t.deviceID, t.bindingID,
			t.registrationVersion, t.tokenHash, t.apnsEnv, t.locale, apnsID, collapse,
			expiresAt.UTC(),
		); err != nil {
			return err
		}
	}
	return nil
}

// HousekeepPushDeliveries disables devices past the 180-day TTL, expires due
// deliveries past expires_at, and resolves expired sending leases (retrying
// zero-attempt crashes, marking already-attempted crashes ambiguous).
func (s *DB) HousekeepPushDeliveries(ctx context.Context, limit int) error {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `
		UPDATE push_devices
		   SET enabled = FALSE,
		       invalid_reason = CASE WHEN invalid_reason = '' THEN 'stale_ttl' ELSE invalid_reason END,
		       updated_at = clock_timestamp()
		 WHERE enabled
		   AND invalidated_at IS NULL
		   AND last_seen_at <= clock_timestamp() - INTERVAL '180 days'`); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `
		WITH due AS (
			SELECT id
			  FROM push_deliveries
			 WHERE state IN ('pending','retry_wait')
			   AND expires_at <= clock_timestamp()
			 ORDER BY expires_at, id
			 LIMIT $1
			   FOR UPDATE SKIP LOCKED
		)
		UPDATE push_deliveries d
		   SET state = 'expired',
		       lease_token = NULL,
		       lease_until = NULL,
		       updated_at = clock_timestamp()
		  FROM due
		 WHERE d.id = due.id`, limit); err != nil {
		return err
	}

	// Expired sending leases WITH a recorded transport attempt become ambiguous.
	if _, err = tx.Exec(ctx, `
		WITH stale AS (
			SELECT d.id, d.lease_token
			  FROM push_deliveries d
			 WHERE d.state = 'sending'
			   AND d.lease_until <= clock_timestamp()
			   AND EXISTS (
			       SELECT 1
			         FROM push_delivery_attempts a
			        WHERE a.delivery_id = d.id
			          AND a.lease_token = d.lease_token
			   )
			 ORDER BY d.lease_until, d.id
			 LIMIT $1
			   FOR UPDATE OF d SKIP LOCKED
		),
		upd_attempts AS (
			UPDATE push_delivery_attempts a
			   SET status = 'ambiguous',
			       apns_reason = CASE WHEN a.apns_reason = '' THEN 'lease_expired_after_attempt' ELSE a.apns_reason END,
			       completed_at = clock_timestamp()
			  FROM stale s
			 WHERE a.delivery_id = s.id
			   AND a.lease_token = s.lease_token
			   AND a.status = 'sending'
		)
		UPDATE push_deliveries d
		   SET state = 'ambiguous',
		       lease_token = NULL,
		       lease_until = NULL,
		       error_reason = 'lease_expired_after_attempt',
		       updated_at = clock_timestamp()
		  FROM stale s
		 WHERE d.id = s.id`, limit); err != nil {
		return err
	}

	// Expired sending leases with ZERO recorded transport attempts can safely
	// return to retry_wait (or expire/reject if past TTL or max attempts).
	if _, err = tx.Exec(ctx, `
		WITH stale AS (
			SELECT d.id
			  FROM push_deliveries d
			 WHERE d.state = 'sending'
			   AND d.lease_until <= clock_timestamp()
			   AND NOT EXISTS (
			       SELECT 1
			         FROM push_delivery_attempts a
			        WHERE a.delivery_id = d.id
			          AND a.lease_token = d.lease_token
			   )
			 ORDER BY d.lease_until, d.id
			 LIMIT $1
			   FOR UPDATE OF d SKIP LOCKED
		)
		UPDATE push_deliveries d
		   SET state = CASE
		         WHEN d.expires_at <= clock_timestamp() THEN 'expired'
		         WHEN d.attempts >= $2 THEN 'rejected'
		         ELSE 'retry_wait'
		       END,
		       next_attempt_at = clock_timestamp(),
		       lease_token = NULL,
		       lease_until = NULL,
		       updated_at = clock_timestamp()
		  FROM stale s
		 WHERE d.id = s.id`, limit, PushMaxAttempts); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

type ClaimedPushDelivery struct {
	ID                  uuid.UUID
	DeliveryKey         string
	InboxItemID         uuid.UUID
	Kind                string
	LocalDate           string
	DeviceID            uuid.UUID
	BindingID           uuid.UUID
	RegistrationVersion int64
	TokenHash           []byte
	APNsEnv             string
	Locale              string
	APNsID              uuid.UUID
	CollapseID          string
	LeaseToken          uuid.UUID
	LeaseUntil          time.Time
	Attempts            int
	ExpiresAt           time.Time

	// Current device state loaded alongside claim for freshness validation.
	DeviceAPNsToken           string
	DeviceEnabled             bool
	DeviceInvalidatedAt       *time.Time
	DeviceBindingID           uuid.UUID
	DeviceRegistrationVersion int64
	DeviceTokenHash           []byte

	// Canonical check-in state (populated for prompt_checkin).
	CheckinStatus    string
	CheckinExpiresAt *time.Time
}

// ClaimNextPushDelivery claims one due push_deliveries row with a 45s lease.
// Returns (nil, nil) when no deliveries are due.
func (s *DB) ClaimNextPushDelivery(ctx context.Context) (*ClaimedPushDelivery, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	lease := uuid.New()
	var d ClaimedPushDelivery
	err = tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id
			  FROM push_deliveries
			 WHERE state IN ('pending','retry_wait')
			   AND attempts < $1
			   AND next_attempt_at <= clock_timestamp()
			   AND expires_at > clock_timestamp()
			 ORDER BY next_attempt_at, id
			 LIMIT 1
			   FOR UPDATE SKIP LOCKED
		)
		UPDATE push_deliveries p
		   SET state = 'sending',
		       lease_token = $2,
		       lease_until = clock_timestamp() + INTERVAL '45 seconds',
		       attempts = p.attempts + 1,
		       updated_at = clock_timestamp()
		  FROM candidate c
		 WHERE p.id = c.id
		 RETURNING p.id, p.delivery_key, p.inbox_item_id, p.kind, p.device_id, p.binding_id,
		           p.registration_version, p.token_hash, p.apns_env, p.locale, p.apns_id,
		           p.collapse_id, p.lease_token, p.lease_until, p.attempts, p.expires_at`,
		PushMaxAttempts, lease,
	).Scan(
		&d.ID, &d.DeliveryKey, &d.InboxItemID, &d.Kind, &d.DeviceID, &d.BindingID,
		&d.RegistrationVersion, &d.TokenHash, &d.APNsEnv, &d.Locale, &d.APNsID,
		&d.CollapseID, &d.LeaseToken, &d.LeaseUntil, &d.Attempts, &d.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err = tx.QueryRow(ctx, `
		SELECT i.local_date,
		       dev.apns_token, dev.enabled, dev.invalidated_at, dev.binding_id,
		       dev.registration_version, dev.token_hash,
		       COALESCE(sc.status, ''), sc.expires_at
		  FROM inbox_items i
		  JOIN push_devices dev ON dev.device_id = $2
		  LEFT JOIN subjective_checkins sc
		         ON i.kind = 'prompt_checkin'
		        AND sc.date = i.checkin_date
		        AND sc.source = i.checkin_source
		 WHERE i.id = $1`,
		d.InboxItemID, d.DeviceID,
	).Scan(
		&d.LocalDate,
		&d.DeviceAPNsToken, &d.DeviceEnabled, &d.DeviceInvalidatedAt, &d.DeviceBindingID,
		&d.DeviceRegistrationVersion, &d.DeviceTokenHash,
		&d.CheckinStatus, &d.CheckinExpiresAt,
	); err != nil {
		return nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &d, nil
}

// StartPushDeliveryAttempt verifies that the delivery row is still in 'sending'
// under leaseToken with an unexpired lease_until > clock_timestamp() and
// durably inserts a push_delivery_attempts row before any APNs HTTP call.
func (s *DB) StartPushDeliveryAttempt(ctx context.Context, deliveryID, leaseToken uuid.UUID) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	var eligible bool
	err = tx.QueryRow(ctx, `
		UPDATE push_deliveries p
		   SET lease_until = clock_timestamp() + INTERVAL '45 seconds'
		 WHERE p.id = $1
		   AND p.state = 'sending'
		   AND p.lease_token = $2
		   AND p.lease_until > clock_timestamp()
		 RETURNING p.expires_at > clock_timestamp()
		   AND EXISTS (
		         SELECT 1 FROM push_devices d
		          WHERE d.device_id = p.device_id
		            AND d.enabled AND d.invalidated_at IS NULL
		            AND d.binding_id = p.binding_id
		            AND d.registration_version = p.registration_version
		            AND d.token_hash = p.token_hash
		            AND d.apns_env = p.apns_env
		       )
		   AND (p.kind <> 'prompt_checkin' OR EXISTS (
		         SELECT 1 FROM inbox_items i
		         JOIN subjective_checkins c ON c.date = i.checkin_date AND c.source = i.checkin_source
		          WHERE i.id = p.inbox_item_id AND c.status = 'prompted'
		            AND c.expires_at > clock_timestamp()
		       ))`,
		deliveryID, leaseToken,
	).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrPushLeaseLost
	}
	if err != nil {
		return uuid.Nil, err
	}
	if !eligible {
		return uuid.Nil, ErrPushDeliveryIneligible
	}

	attemptID := uuid.New()
	if _, err = tx.Exec(ctx, `
		INSERT INTO push_delivery_attempts (
			id, delivery_id, lease_token, status, status_code, apns_reason, attempted_at, completed_at
		) VALUES (
			$1, $2, $3, 'sending', NULL, '', clock_timestamp(), NULL
		)`, attemptID, deliveryID, leaseToken); err != nil {
		return uuid.Nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return attemptID, nil
}

type CompletePushDeliveryInput struct {
	DeliveryID          uuid.UUID
	LeaseToken          uuid.UUID
	AttemptID           uuid.UUID
	DeliveryState       string
	AttemptStatus       string
	StatusCode          *int
	Reason              string
	RetryAfter          time.Duration
	InvalidateDevice    bool
	DeviceID            uuid.UUID
	RegistrationVersion int64
	TokenHash           []byte
	AppleInvalidatedAt  *time.Time
}

// CompletePushDelivery atomically finalizes a sending delivery and its attempt
// strictly under (id, state='sending', lease_token, lease_until > clock_timestamp()).
// Returns applied=false when the lease already expired before finalization.
func (s *DB) CompletePushDelivery(ctx context.Context, in CompletePushDeliveryInput) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	retrySeconds := in.RetryAfter.Seconds()
	if retrySeconds < 0 {
		retrySeconds = 0
	}
	tag, err := tx.Exec(ctx, `
		UPDATE push_deliveries
		   SET state = $3,
		       status_code = COALESCE($4, status_code),
		       error_reason = $5,
		       next_attempt_at = CASE
		         WHEN $3 = 'retry_wait' THEN clock_timestamp() + ($6 * INTERVAL '1 second')
		         ELSE next_attempt_at
		       END,
		       lease_token = NULL,
		       lease_until = NULL,
		       updated_at = clock_timestamp()
		 WHERE id = $1
		   AND state = 'sending'
		   AND lease_token = $2
		   AND lease_until > clock_timestamp()`,
		in.DeliveryID, in.LeaseToken, in.DeliveryState, in.StatusCode, in.Reason, retrySeconds,
	)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}

	if in.AttemptID != uuid.Nil && in.AttemptStatus != "" {
		if _, err = tx.Exec(ctx, `
			UPDATE push_delivery_attempts
			   SET status = $3,
			       status_code = $4,
			       apns_reason = $5,
			       completed_at = clock_timestamp()
			 WHERE id = $1
			   AND lease_token = $2
			   AND status = 'sending'`,
			in.AttemptID, in.LeaseToken, in.AttemptStatus, in.StatusCode, in.Reason,
		); err != nil {
			return false, err
		}
	}

	if in.InvalidateDevice && in.DeviceID != uuid.Nil {
		if in.AppleInvalidatedAt != nil {
			if _, err = tx.Exec(ctx, `
				UPDATE push_devices
				   SET enabled = FALSE,
				       invalidated_at = clock_timestamp(),
				       invalid_reason = $4,
				       updated_at = clock_timestamp()
				 WHERE device_id = $1
				   AND registration_version = $2
				   AND token_hash = $3
				   AND updated_at <= $5`,
				in.DeviceID, in.RegistrationVersion, in.TokenHash, in.Reason, in.AppleInvalidatedAt.UTC(),
			); err != nil {
				return false, err
			}
		} else {
			if _, err = tx.Exec(ctx, `
				UPDATE push_devices
				   SET enabled = FALSE,
				       invalidated_at = clock_timestamp(),
				       invalid_reason = $4,
				       updated_at = clock_timestamp()
				 WHERE device_id = $1
				   AND registration_version = $2
				   AND token_hash = $3`,
				in.DeviceID, in.RegistrationVersion, in.TokenHash, in.Reason,
			); err != nil {
				return false, err
			}
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// ListPushDevices returns devices registered in this tenant schema (for tests/admin).
func (s *DB) ListPushDevices(ctx context.Context) ([]PushDevice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT device_id, binding_id, apns_token, token_hash, apns_env, bundle_id, locale,
		       registration_version, enabled, invalidated_at, invalid_reason,
		       created_at, updated_at, last_seen_at
		  FROM push_devices
		 ORDER BY created_at, device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushDevice
	for rows.Next() {
		var d PushDevice
		if err := rows.Scan(
			&d.DeviceID, &d.BindingID, &d.APNsToken, &d.TokenHash, &d.APNsEnv, &d.BundleID, &d.Locale,
			&d.RegistrationVersion, &d.Enabled, &d.InvalidatedAt, &d.InvalidReason,
			&d.CreatedAt, &d.UpdatedAt, &d.LastSeenAt,
		); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
