package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
)

// State enum for the Telegram webhook registration lifecycle of one
// tenant. Stored as a JSON-serialised field of WebhookStatus inside
// health_registry.global_settings. Values are validated at the Go
// layer; adding a new state never requires a migration.
const (
	StateUnknown  = "unknown"
	StatePending  = "pending"
	StateOK       = "ok"
	StateFailed   = "failed"
	StateDeleted  = "deleted"
)

// ReasonRestartInterrupted is written by ResetPendingOnStartup to any
// row still in StatePending at process start — the previous run got
// killed (kill -9 / OOM / crash) before the registrar could write a
// final state. Operator sees this with a clear cause + Retry button.
const ReasonRestartInterrupted = "restart_interrupted"

// WebhookStatus is the per-tenant state of the Telegram webhook
// registration. Persisted as JSON in health_registry.global_settings
// under key "webhook_status_<schema>".
//
// updated_at is the wall-clock of the last state transition. Critical
// for UI ("3m ago" vs "2h ago") so the operator can tell "still
// running" from "stuck for hours".
type WebhookStatus struct {
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// validStates is the closed set parseWebhookStatus accepts. Anything
// else falls back to StateUnknown.
var validStates = map[string]bool{
	StateUnknown: true,
	StatePending: true,
	StateOK:      true,
	StateFailed:  true,
	StateDeleted: true,
}

// parseWebhookStatus is the load-bearing safe parser. It's the only
// entry point that touches the raw TEXT from global_settings.value.
//
// Contract: on ANY malformed/unexpected input, returns
// (WebhookStatus{State: StateUnknown}, non-nil error). Callers that
// don't care about the error (GetWebhookStatus) can discard it and
// use the unknown status. Callers that DO care (ResetPendingOnStartup)
// use the error to count `skipped` rows.
//
// This is an inversion of the default "errors must bubble up" — here
// recovery + read paths must be more robust than the producer, because
// a crash on one corrupt row would mean infinite startup crashloop
// with no way for the operator to fix it.
func parseWebhookStatus(raw string) (WebhookStatus, error) {
	out := WebhookStatus{State: StateUnknown}
	if raw == "" {
		return out, errors.New("empty value")
	}
	var s WebhookStatus
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return out, fmt.Errorf("unmarshal: %w", err)
	}
	if !validStates[s.State] {
		return out, fmt.Errorf("unknown state %q", s.State)
	}
	return s, nil
}

// serialiseWebhookStatus is the only path that writes a status row.
// Callers never assemble JSON by hand.
func serialiseWebhookStatus(s WebhookStatus) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// webhookStatusKey is the global_settings key for the given tenant
// schema. Empty schema yields empty key — caller decides to error or
// skip silently (we don't want webhook_status_ as a real key).
func webhookStatusKey(schema string) string {
	if schema == "" {
		return ""
	}
	return "webhook_status_" + schema
}

// webhookStatusRow is the (key, value) shape that classifyPendingRows
// works on. Internal type so the pure decision can be tested without
// a live pool.
type webhookStatusRow struct {
	Key   string
	Value string
}

// classifyPendingRows is the pure decision used by
// ResetPendingOnStartup. Walks every row, parses each via the safe
// parser, returns the keys that are in StatePending. Malformed rows
// contribute to `skipped` and are left alone (we don't overwrite what
// we can't read).
//
// Load-bearing: one malformed value must not poison the entire pass.
// See TestClassifyPendingRows_SurvivesMalformedRows for the contract.
func classifyPendingRows(rows []webhookStatusRow) (toReset []string, skipped int) {
	for _, r := range rows {
		s, err := parseWebhookStatus(r.Value)
		if err != nil {
			skipped++
			continue
		}
		if s.State == StatePending {
			toReset = append(toReset, r.Key)
		}
	}
	return toReset, skipped
}

// GetWebhookStatus returns the persisted status for `schema`. Discards
// any parse error from the safe parser — callers get StateUnknown on
// missing/malformed rows. Use this from request paths where you want
// "current best-effort state".
func (r *Registry) GetWebhookStatus(ctx context.Context, schema string) WebhookStatus {
	key := webhookStatusKey(schema)
	if key == "" {
		return WebhookStatus{State: StateUnknown}
	}
	raw := r.GetGlobalSetting(ctx, key)
	if raw == "" {
		return WebhookStatus{State: StateUnknown}
	}
	s, _ := parseWebhookStatus(raw)
	return s
}

// SetWebhookStatus writes a fresh status row for `schema` with the
// current wall clock. Always serialised via json.Marshal — caller
// never assembles a raw string. Use from registrar success/failure
// callbacks and from service-layer "started pending" transitions.
func (r *Registry) SetWebhookStatus(ctx context.Context, schema, state, reason string) error {
	key := webhookStatusKey(schema)
	if key == "" {
		return errors.New("empty schema")
	}
	if !validStates[state] {
		return fmt.Errorf("invalid state %q", state)
	}
	raw, err := serialiseWebhookStatus(WebhookStatus{
		State:     state,
		Reason:    reason,
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return r.SaveGlobalSettings(ctx, map[string]string{key: raw})
}

// ResetPendingOnStartup is the recovery path. Called once at process
// start before the HTTP listener, before any new registrar goroutines
// could spawn. Any row still in StatePending must be a leftover from
// the previous run that died before writing a final state — we
// transition those to StateFailed with reason=restart_interrupted so
// the operator sees a clear UI signal (badge + Retry button).
//
// Returns (reset, skipped, err):
//   - reset:   how many pending rows we transitioned to failed
//   - skipped: how many rows had unparseable values (left untouched)
//   - err:     hard DB error (rare; logged at call site)
//
// Idempotent: a second call with no pending rows returns (0, 0, nil).
// Survives malformed rows: TestClassifyPendingRows_SurvivesMalformedRows
// pins the contract.
func (r *Registry) ResetPendingOnStartup(ctx context.Context) (reset, skipped int, err error) {
	// Escape underscores in the LIKE pattern: '_' is a single-char
	// wildcard in SQL LIKE, so 'webhook_status_%' would also match
	// 'webhookXstatusX...' for any single chars X. Use ESCAPE to make
	// the underscores literal — without this we could accidentally
	// reset unrelated global_settings rows if any future key shares
	// the prefix shape by coincidence.
	rows, err := r.pool.Query(ctx, `
		SELECT key, value FROM health_registry.global_settings
		 WHERE key LIKE 'webhook\_status\_%' ESCAPE '\'`)
	if err != nil {
		return 0, 0, fmt.Errorf("query webhook_status keys: %w", err)
	}
	var batch []webhookStatusRow
	for rows.Next() {
		var k, v string
		if scanErr := rows.Scan(&k, &v); scanErr != nil {
			// Should never happen with TEXT/TEXT columns; treat as
			// skip rather than abort the whole pass.
			log.Printf("ResetPendingOnStartup: scan: %v", scanErr)
			continue
		}
		batch = append(batch, webhookStatusRow{Key: k, Value: v})
	}
	rows.Close()
	if rows.Err() != nil {
		return 0, 0, fmt.Errorf("iterate webhook_status keys: %w", rows.Err())
	}

	toReset, skipped := classifyPendingRows(batch)
	if skipped > 0 {
		log.Printf("ResetPendingOnStartup: %d malformed webhook_status rows skipped (left untouched)", skipped)
	}
	if len(toReset) == 0 {
		return 0, skipped, nil
	}

	newRaw, _ := serialiseWebhookStatus(WebhookStatus{
		State:     StateFailed,
		Reason:    ReasonRestartInterrupted,
		UpdatedAt: time.Now().UTC(),
	})
	for _, key := range toReset {
		if _, uerr := r.pool.Exec(ctx, `
			UPDATE health_registry.global_settings
			   SET value = $1, updated_at = NOW()
			 WHERE key = $2`, newRaw, key); uerr != nil {
			log.Printf("ResetPendingOnStartup: update %s: %v", key, uerr)
			continue
		}
		reset++
	}
	return reset, skipped, nil
}
