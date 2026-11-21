package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const sleepRolloutPublicationSetting = "sleep_rollout_publication"

var ErrSleepRolloutEvidenceChanged = errors.New("sleep rollout inputs changed after audit")

type SleepRolloutPublication struct {
	Active       bool               `json:"active"`
	Tenant       uuid.UUID          `json:"tenant"`
	From         string             `json:"from"`
	Through      string             `json:"through"`
	Packet       int64              `json:"packet_boundary"`
	Evidence     string             `json:"evidence"`
	Projection   *sleepGoProjection `json:"projection,omitempty"`
	goProjection *sleepGoProjection
}

func (p SleepRolloutPublication) readerPublication() SleepRolloutPublication {
	p.goProjection = p.Projection
	return p
}

func (p SleepRolloutPublication) window() sleepRolloutWindow {
	return sleepRolloutWindow{From: p.From, Through: p.Through}
}

// The same table set is fingerprinted by the read-only audit and fenced by
// the switching transaction. Tuple versions cover inserts, updates and deletes.
var sleepRolloutInputTables = []string{
	"settings", "health_records", "metric_points", "hourly_metrics", "daily_scores",
	"sleep_rollout_batches", "sleep_rollout_packets", "sleep_rollout_candidates",
	"sleep_rollout_daily", "sleep_rollout_results", "subjective_checkins",
	"context_prompt_interactions", "completed_night_sleep", "night_sleep_coverage_commitments",
	"sleep_period_coverage", "completed_sleep_episode", "energy_snapshots",
}

func verifySleepRolloutTenant(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	identity, err := ReadTenantIdentityMarker(ctx, tx, "")
	if err != nil {
		return err
	}
	if identity.TenantID != tenant || identity.SchemaContractVersion != SchemaContractVersion ||
		identity.SchemaContractChecksum != SchemaContractChecksum() {
		return errors.New("sleep rollout database does not match the expected tenant contract")
	}
	return nil
}

func sleepRolloutEvidence(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, from, through string) (string, error) {
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\n%s\n%s\n%s\n", tenant, from, through, SchemaContractChecksum())
	for _, table := range sleepRolloutInputTables {
		fmt.Fprintln(digest, table)
		rows, err := tx.Query(ctx, `SELECT ctid::text,xmin::text FROM `+pgx.Identifier{table}.Sanitize()+` ORDER BY ctid`)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var row, version string
			if err := rows.Scan(&row, &version); err != nil {
				rows.Close()
				return "", err
			}
			fmt.Fprintf(digest, "%s %s\n", row, version)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func readSleepRolloutPublication(ctx context.Context, query ContractCatalog) (*SleepRolloutPublication, error) {
	var value, mode string
	err := query.QueryRow(ctx, `SELECT value,
		COALESCE((SELECT value FROM settings WHERE key=$2),'off') FROM settings WHERE key=$1`,
		sleepRolloutPublicationSetting, SleepRolloutModeSetting).Scan(&value, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var publication SleepRolloutPublication
	if err := json.Unmarshal([]byte(value), &publication); err != nil {
		return nil, err
	}
	if publication.Active != (mode == "serve") {
		return nil, errors.New("sleep publication and tenant mode disagree")
	}
	return &publication, nil
}

func (s *DB) sleepRolloutServeEnabled() bool {
	return s.sleepRolloutTenant && os.Getenv("HEALTH_SLEEP_V2_ENABLED") == "1" && os.Getenv("HEALTH_SLEEP_SERVE_ENABLED") == "1"
}

func (s *DB) withActiveSleepRollout(ctx context.Context, read func(*DB, SleepRolloutPublication) error) (bool, error) {
	return s.readSleepRolloutSnapshot(ctx, true, read)
}

func (s *DB) withSleepRolloutPublication(ctx context.Context, read func(*DB, SleepRolloutPublication) error) (bool, error) {
	return s.readSleepRolloutSnapshot(ctx, false, read)
}

func (s *DB) readSleepRolloutSnapshot(ctx context.Context, activeOnly bool, read func(*DB, SleepRolloutPublication) error) (bool, error) {
	enabled := s.sleepRolloutServeEnabled()
	projectionEnabled := s.sleepGoProjectionEnabled()
	if !s.sleepRolloutTenant {
		return false, nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	publication, err := readSleepRolloutPublication(ctx, tx)
	if err != nil {
		return false, err
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	if publication != nil && publication.Projection != nil {
		if err := verifySleepRolloutTenant(ctx, tx, publication.Tenant); err != nil {
			return false, err
		}
		resolved := publication.readerPublication()
		resolved.Active = resolved.Active && enabled
		publication = &resolved
	} else if (publication == nil || !publication.Active || !enabled) && projectionEnabled {
		projection, err := loadSleepGoProjection(ctx, tx)
		if err != nil {
			return false, err
		}
		if projection != nil {
			publication = &SleepRolloutPublication{From: projection.From, Through: projection.Through, goProjection: projection}
		}
	}
	if publication == nil || (activeOnly && publication.goProjection == nil && (!enabled || !publication.Active)) {
		return false, nil
	}
	if publication.goProjection == nil {
		if err := verifySleepRolloutTenant(ctx, tx, publication.Tenant); err != nil {
			return false, err
		}
		// Serve can be disabled while observations from the old pilot persist.
		publication.Active = publication.Active && enabled
	}
	if err := read(unit, *publication); err != nil {
		return true, err
	}
	return true, tx.Commit(ctx)
}

func pendingSleepRolloutLegacyDates(ctx context.Context, tx pgx.Tx) (int, error) {
	var raw string
	err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, cacheMaintenanceSettingKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	state, err := decodeCacheMaintenanceState(raw)
	return len(state.Dirty), err
}

// SwitchSleepRollout changes the active reader and candidate mode together.
// The local operator supplies the exact report reviewed before the switch.
func (s *DB) SwitchSleepRollout(ctx context.Context, tenant uuid.UUID, from, through, evidence string, active bool) (SleepRolloutPublication, error) {
	var publication SleepRolloutPublication
	if !s.sleepRolloutTenant || tenant == uuid.Nil || evidence == "" {
		return publication, errors.New("sleep rollout transition requires a restricted tenant and reviewed audit")
	}
	if active && !s.sleepRolloutServeEnabled() {
		return publication, errors.New("enable the server serve switch before publication")
	}
	// Validate the revision-bound audit outside the writer fence.
	var reviewed *sleepGoProjection
	if s.sleepGoProjectionEnabled() || !active {
		report, err := s.AuditSleepRolloutPreparation(ctx, tenant, from, through)
		if err != nil {
			return publication, err
		}
		reviewed = report.projection
		if reviewed != nil {
			if report.Evidence != evidence {
				return publication, ErrSleepRolloutEvidenceChanged
			}
			if active && (!report.ProjectionReady || report.VerifiedProjectionDates == 0) {
				return publication, errors.New("sleep projection has no verified dates or has an incomplete result")
			}
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return publication, err
	}
	defer tx.Rollback(context.Background())
	if err := verifySleepRolloutTenant(ctx, tx, tenant); err != nil {
		return publication, err
	}
	if err := lockSleepRollout(ctx, tx); err != nil {
		return publication, err
	}
	if reviewed != nil {
		return switchRevisionBoundSleepRollout(ctx, tx, tenant, from, through, evidence, active)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('health/sleep-rollout-legacy/' || current_schema(),0))`); err != nil {
		return publication, err
	}
	tables := make([]string, len(sleepRolloutInputTables))
	for i, table := range sleepRolloutInputTables {
		tables[i] = pgx.Identifier{table}.Sanitize()
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE `+strings.Join(tables, ",")+` IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return publication, err
	}
	actual, err := sleepRolloutEvidence(ctx, tx, tenant, from, through)
	if err != nil {
		return publication, err
	}
	if actual != evidence {
		return publication, ErrSleepRolloutEvidenceChanged
	}

	current, err := readSleepRolloutPublication(ctx, tx)
	if err != nil {
		return publication, err
	}
	// Legacy remains writable throughout the pilot. Both transitions wait
	// for already accepted ordinary uploads at the fenced boundary.
	var pending int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM health_records WHERE processing_status='pending'`).Scan(&pending); err != nil {
		return publication, err
	}
	if pending != 0 {
		return publication, errors.New("legacy ingest has not caught up to the transition boundary")
	}
	dirty, err := pendingSleepRolloutLegacyDates(ctx, tx)
	if err != nil {
		return publication, err
	}
	if dirty != 0 {
		return publication, errors.New("legacy calculations have not caught up to the transition boundary")
	}
	mode := "off"
	if active {
		if current != nil && current.Active {
			return publication, errors.New("sleep rollout is already published")
		}
		if _, err := sleepRolloutProfile(ctx, tx); err != nil {
			return publication, err
		}
		unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
		if through != time.Now().In(unit.reportTZLocation()).Format(time.DateOnly) {
			return publication, errors.New("sleep rollout audit must include the current local date")
		}
		report, err := auditSleepRolloutInTransaction(ctx, tx, tenant, from, through)
		if err != nil {
			return publication, err
		}
		if report.ExpectedDates == 0 || report.MatchedDates != report.ExpectedDates || report.UnownedDates != 0 ||
			report.PendingMixedBatches != 0 || report.FailedMixedBatches != 0 || report.PendingCandidates != 0 {
			return publication, errors.New("sleep rollout preparation is incomplete")
		}
		var revisionBound bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sleep_rollout_daily WHERE episode_selection IS NOT NULL)`).Scan(&revisionBound); err != nil {
			return publication, err
		}
		if revisionBound {
			return publication, errors.New("revision-bound sleep selection is pending independent acceptance")
		}
		mode = "serve"
	} else if current == nil || !current.Active || current.Projection != nil || current.Tenant != tenant || current.From != from || current.Through != through {
		return publication, errors.New("rollback must target the current published range")
	}
	// Every accepted candidate during serve must have an ordinary replayable
	// payload. The experimental v2 endpoint cannot add work in serve.
	publication = SleepRolloutPublication{Active: active, Tenant: tenant, From: from, Through: through, Evidence: actual}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM sleep_rollout_packets`).Scan(&publication.Packet); err != nil {
		return publication, err
	}
	return saveSleepRolloutPublication(ctx, tx, mode, publication)
}

func saveSleepRolloutPublication(ctx context.Context, tx pgx.Tx, mode string, publication SleepRolloutPublication) (SleepRolloutPublication, error) {
	raw, err := json.Marshal(publication)
	if err != nil {
		return publication, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO settings(key,value) VALUES ($1,$2),($3,$4)
		ON CONFLICT (key) DO UPDATE SET value=excluded.value,updated_at=NOW()::text`,
		SleepRolloutModeSetting, mode, sleepRolloutPublicationSetting, string(raw)); err != nil {
		return publication, err
	}
	return publication, tx.Commit(ctx)
}
