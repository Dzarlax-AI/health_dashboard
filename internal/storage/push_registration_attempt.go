package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	pushRegistrationAttemptWindow = 7 * 24 * time.Hour
	pushRegistrationClockSkew     = 5 * time.Minute
)

var (
	ErrInvalidPushRegistrationAttempt  = errors.New("invalid registration_attempt_id")
	ErrPushRegistrationAttemptConflict = errors.New("registration attempt is cancelled, expired, superseded, or belongs to another request")
)

func lockPushRegistrations(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('push_devices:' || current_schema()))`)
	return err
}

// A time-bearing UUID lets the server reject a replay even after its receipt
// or cancellation marker has been pruned.
func pushRegistrationAttemptTime(id uuid.UUID, now time.Time) (time.Time, error) {
	if id.Version() != 7 || id.Variant() != uuid.RFC4122 {
		return time.Time{}, ErrInvalidPushRegistrationAttempt
	}
	ms := binary.BigEndian.Uint64(id[:8]) >> 16
	issued := time.UnixMilli(int64(ms))
	if issued.After(now.Add(pushRegistrationClockSkew)) {
		return time.Time{}, ErrInvalidPushRegistrationAttempt
	}
	return issued, nil
}

func pushRegistrationRequestHash(in UpsertPushDeviceInput) [sha256.Size]byte {
	contractVer := in.InboxContractVersion
	if contractVer < InboxContractV1 {
		contractVer = InboxContractV1
	}
	if contractVer == InboxContractV1 {
		// Keep the exact v22 4-field hash for contract v1 so TestFlight 33
		// retries of POSTs accepted before a v22 -> v23 migration replay cleanly
		// instead of returning 409 Conflict.
		return sha256.Sum256([]byte(strings.Join([]string{in.APNsToken, in.APNsEnv, in.BundleID, in.Locale}, "\x00")))
	}
	return sha256.Sum256([]byte(strings.Join([]string{in.APNsToken, in.APNsEnv, in.BundleID, in.Locale, strconv.Itoa(contractVer)}, "\x00")))
}

func replayPushRegistration(ctx context.Context, tx pgx.Tx, in UpsertPushDeviceInput, requestHash []byte) (PushDevice, bool, error) {
	now := time.Now()
	issued, err := pushRegistrationAttemptTime(in.RegistrationAttemptID, now)
	if err != nil {
		return PushDevice{}, false, err
	}
	if !issued.Add(pushRegistrationAttemptWindow).After(now) {
		return PushDevice{}, false, ErrPushRegistrationAttemptConflict
	}

	var result PushDevice
	var savedHash []byte
	var binding *uuid.UUID
	var version *int64
	var env *string
	var cancelled *time.Time
	// The receipt remains authoritative for duplicate requests. Also verify that
	// the binding still belongs to that version before reporting it as active.
	err = tx.QueryRow(ctx, `
		SELECT a.device_id, a.request_hash, a.binding_id, a.registration_version, a.apns_env, a.cancelled_at,
		       COALESCE(d.enabled AND d.invalidated_at IS NULL, FALSE),
		       COALESCE(d.inbox_contract_version, 1)
		  FROM push_registration_attempts a
		  LEFT JOIN push_devices d ON d.device_id = a.device_id AND d.binding_id = a.binding_id
		                           AND d.registration_version = a.registration_version
		 WHERE a.id = $1`, in.RegistrationAttemptID).
		Scan(&result.DeviceID, &savedHash, &binding, &version, &env, &cancelled, &result.Enabled, &result.InboxContractVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return PushDevice{}, false, nil
	}
	if err != nil {
		return PushDevice{}, false, err
	}
	if result.DeviceID != in.DeviceID || cancelled != nil || !result.Enabled || !bytes.Equal(savedHash, requestHash) ||
		binding == nil || version == nil || env == nil {
		return PushDevice{}, false, ErrPushRegistrationAttemptConflict
	}
	result.BindingID = *binding
	result.RegistrationVersion = *version
	result.APNsEnv = *env
	return result, true, nil
}

// CancelPushRegistrationAttempt serializes with registration. Cancel-before-
// POST leaves a marker and cancel-after-POST can only disable the saved version.
func (s *DB) CancelPushRegistrationAttempt(ctx context.Context, deviceID, attemptID uuid.UUID) error {
	if deviceID == uuid.Nil {
		return ErrInvalidPushRegistrationAttempt
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err = lockPushRegistrations(ctx, tx); err != nil {
		return err
	}
	now := time.Now()
	issued, err := pushRegistrationAttemptTime(attemptID, now)
	if err != nil {
		return err
	}
	var owner uuid.UUID
	var binding *uuid.UUID
	var version *int64
	err = tx.QueryRow(ctx, `
		SELECT device_id, binding_id, registration_version
		  FROM push_registration_attempts WHERE id = $1 FOR UPDATE`, attemptID).Scan(&owner, &binding, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		// An expired POST cannot make a binding, so an absent receipt
		// no longer needs a cancellation marker.
		if !issued.Add(pushRegistrationAttemptWindow).After(now) {
			return nil
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO push_registration_attempts (id, device_id, cancelled_at)
			VALUES ($1, $2, clock_timestamp())`, attemptID, deviceID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if owner != deviceID {
		return ErrPushRegistrationAttemptConflict
	}

	if binding != nil && version != nil {
		if _, err = tx.Exec(ctx, `
			UPDATE push_devices
			   SET enabled = FALSE, updated_at = clock_timestamp()
			 WHERE device_id = $1 AND binding_id = $2 AND registration_version = $3 AND enabled`,
			deviceID, *binding, *version); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `
		UPDATE push_registration_attempts SET cancelled_at = clock_timestamp()
		 WHERE id = $1 AND cancelled_at IS NULL`, attemptID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func prunePushRegistrationAttempts(ctx context.Context, tx pgx.Tx, limit int) error {
	// Retain receipts beyond the device lifetime, and retain any receipt which
	// can still disable a current binding (even after a legacy refresh).
	_, err := tx.Exec(ctx, `
		WITH expired AS (
			SELECT a.id
			  FROM push_registration_attempts a
			 WHERE a.created_at < clock_timestamp() - INTERVAL '190 days'
			   AND NOT EXISTS (
			       SELECT 1 FROM push_devices d
			        WHERE d.device_id = a.device_id AND d.binding_id = a.binding_id
			          AND d.registration_version = a.registration_version AND d.enabled
			   )
			 ORDER BY a.created_at, a.id LIMIT $1 FOR UPDATE OF a SKIP LOCKED
		)
		DELETE FROM push_registration_attempts a USING expired e WHERE a.id = e.id`, limit)
	return err
}
