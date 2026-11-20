// Package sleepshadow provides an isolated, explicitly started sleep replay server.
package sleepshadow

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/experiments/dbt_metrics"
)

//go:embed schema.sql
var schema string

const dailyColumns = `profile, wake_date, candidate, revision, sleep_seconds, stages,
	deep_seconds, rem_seconds, core_seconds, unspecified_seconds, awake_seconds, stage_conflict`

var recalculate = "INSERT INTO sleep_shadow.daily (" + dailyColumns + ") " +
	dbtmetrics.ShadowSleepDailySQL("profile = $1 AND wake_date = $2::text::date")

func initialize(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, schema); err != nil {
			return err
		}
		// Rows written before stage aggregates were added have a null conflict marker.
		legacyRows := `(profile, wake_date) IN (
			SELECT profile, wake_date FROM sleep_shadow.daily WHERE stage_conflict IS NULL)`
		_, err := tx.Exec(ctx, `WITH rebuilt AS (`+dbtmetrics.ShadowSleepDailySQL(legacyRows)+`)
			UPDATE sleep_shadow.daily d SET deep_seconds=r.deep_seconds, rem_seconds=r.rem_seconds,
			core_seconds=r.core_seconds, unspecified_seconds=r.unspecified_seconds,
			awake_seconds=r.awake_seconds, stage_conflict=r.stage_conflict
			FROM rebuilt r WHERE (d.profile,d.wake_date,d.candidate,d.revision) =
				(r.profile,r.wake_date,r.candidate,r.revision) AND d.stage_conflict IS NULL`)
		return err
	})
}

type store struct {
	pool    *pgxpool.Pool
	refresh *DailyRefresh
}

var errRefreshUnavailable = errors.New("pending sync requires its daily refresh")

func (s store) refreshSchema(profile string) string {
	if s.refresh != nil && s.refresh.Profile == profile {
		return s.refresh.Schema
	}
	return ""
}

// Check under the profile lock so a newer sync cannot bypass a pending
// correction when its writer is unavailable. Unmarked packets must be
// explicitly bound before any writer can recover them.
func requirePendingRefresh(ctx context.Context, tx pgx.Tx, profile, schema string) error {
	var blocked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM sleep_shadow.pending WHERE profile=$1
		AND (refresh_schema IS NULL OR (refresh_schema<>'' AND refresh_schema<>$2))
	)`, profile, schema).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return errRefreshUnavailable
	}
	return nil
}

// ConfirmLegacyPending binds only unmarked packets for the selected profile.
// Publication is left to startup with the confirmed writer.
func ConfirmLegacyPending(ctx context.Context, pool *pgxpool.Pool, profile, schema string) (int64, error) {
	if profile == "" || schema == "" {
		return 0, errors.New("legacy pending confirmation requires a profile and schema")
	}
	if err := initialize(ctx, pool); err != nil {
		return 0, err
	}
	var confirmed int64
	err := (store{pool: pool}).transaction(ctx, profile, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE sleep_shadow.pending SET refresh_schema=$2
			WHERE profile=$1 AND refresh_schema IS NULL`, profile, schema)
		if err != nil {
			return err
		}
		if err := requirePendingRefresh(ctx, tx, profile, schema); err != nil {
			return err
		}
		confirmed = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return confirmed, nil
}

func (s store) transaction(ctx context.Context, profile string, work func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO sleep_shadow.profile (id) VALUES ($1) ON CONFLICT DO NOTHING", profile); err != nil {
			return err
		}
		// Both wire formats use the same lock before reading candidate identity.
		if _, err := tx.Exec(ctx, "SELECT id FROM sleep_shadow.profile WHERE id=$1 FOR UPDATE", profile); err != nil {
			return err
		}
		return work(tx)
	})
}

func (s store) sync(ctx context.Context, profile string, input candidate, date string) (string, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	var matches bool
	schema := s.refreshSchema(profile)
	err = s.transaction(ctx, profile, func(tx pgx.Tx) error {
		if err := requirePendingRefresh(ctx, tx, profile, schema); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.pending (profile,candidate,revision,wake_date,snapshot,refresh_schema)
			VALUES ($1,$2,$3,$4::text::date,$5,$6) ON CONFLICT DO NOTHING`, profile, input.ID, input.Revision, date, body, schema); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT snapshot=$4::jsonb FROM sleep_shadow.pending
			WHERE profile=$1 AND candidate=$2 AND revision=$3`, profile, input.ID, input.Revision, body).Scan(&matches)
	})
	if err != nil {
		return "", err
	}
	if !matches {
		return "revision_conflict", nil
	}
	return s.apply(ctx, profile, input, date)
}

func (s store) recoverPending(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT profile, wake_date::text, snapshot FROM sleep_shadow.pending
		ORDER BY profile, candidate, revision`)
	if err != nil {
		return err
	}
	type packet struct {
		profile, date string
		snapshot      json.RawMessage
	}
	pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (packet, error) {
		var p packet
		err := row.Scan(&p.profile, &p.date, &p.snapshot)
		return p, err
	})
	if err != nil {
		return err
	}
	for _, p := range pending {
		var input candidate
		if err := json.Unmarshal(p.snapshot, &input); err != nil {
			return err
		}
		if _, err := s.apply(ctx, p.profile, input, p.date); err != nil && !errors.Is(err, errRefreshUnavailable) {
			return err
		}
	}
	return nil
}

func (s store) apply(ctx context.Context, profile string, input candidate, date string) (string, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	var result string
	schema := s.refreshSchema(profile)
	err = s.transaction(ctx, profile, func(tx pgx.Tx) error {
		if err := requirePendingRefresh(ctx, tx, profile, schema); err != nil {
			return err
		}
		// A failed recalculation rolls back this deletion together with the projection.
		if _, err := tx.Exec(ctx, `DELETE FROM sleep_shadow.pending
			WHERE profile=$1 AND candidate=$2 AND revision=$3 AND snapshot=$4::jsonb`,
			profile, input.ID, input.Revision, body); err != nil {
			return err
		}
		var revision int64
		var previousDate string
		var identical bool
		err := tx.QueryRow(ctx, `SELECT revision, wake_date::text, snapshot=$3::jsonb
			FROM sleep_shadow.candidate WHERE profile=$1 AND id=$2`, profile, input.ID, body).
			Scan(&revision, &previousDate, &identical)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case input.Revision < revision:
			result = "stale"
			return nil
		case input.Revision == revision:
			result = "revision_conflict"
			if identical {
				result = "repeat"
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.candidate VALUES ($1,$2,$3,$4::text::date,$5,$6)
			ON CONFLICT (profile,id) DO UPDATE SET revision=excluded.revision,
			wake_date=excluded.wake_date, priority=excluded.priority, snapshot=excluded.snapshot`,
			profile, input.ID, input.Revision, date, input.Priority, body); err != nil {
			return err
		}
		for _, alias := range input.LegacyAliases {
			if _, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.legacy_alias VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT DO NOTHING`, profile, input.ID, alias.SourceEpoch, alias.Source, alias.Generation); err != nil {
				return err
			}
		}

		dates := []string{date}
		if previousDate != "" && previousDate != date {
			dates = append(dates, previousDate)
		}
		slices.Sort(dates)
		// Clear both placements before reinserting so the candidate remains unique across dates.
		if _, err := tx.Exec(ctx, `DELETE FROM sleep_shadow.daily WHERE profile=$1
			AND wake_date::text = ANY($2::text[])`, profile, dates); err != nil {
			return err
		}
		for _, day := range dates {
			if _, err := tx.Exec(ctx, recalculate, profile, day); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.recalculation VALUES ($1,$2::text::date,1)
				ON CONFLICT (profile,wake_date) DO UPDATE SET generation=sleep_shadow.recalculation.generation+1`,
				profile, day); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sleep_shadow.profile
			SET source_revision=source_revision+1 WHERE id=$1`, profile); err != nil {
			return err
		}
		if schema != "" {
			if err := s.refresh.Apply(ctx, tx, dates); err != nil {
				return err
			}
		}
		result = "applied"
		return nil
	})
	return result, err
}

func (s store) legacy(ctx context.Context, profile string, body json.RawMessage, packet legacyPacket) (string, error) {
	result := "unresolved_legacy"
	err := s.transaction(ctx, profile, func(tx pgx.Tx) error {
		bindings := packet.boundAliases()
		if len(bindings) > 0 && len(bindings) == len(packet.Data.Coverage) {
			result = "matched_legacy_repeat"
			for _, alias := range bindings {
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM sleep_shadow.legacy_alias
					WHERE profile=$1 AND source_epoch=$2 AND source=$3 AND generation=$4`,
					profile, alias.SourceEpoch, alias.Source, alias.Generation).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					result = "unresolved_legacy"
					break
				}
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.legacy_receipt(profile,classification,payload)
			VALUES ($1,$2,$3)`, profile, result, body)
		return err
	})
	return result, err
}

func (s store) daily(ctx context.Context, profile string) (json.RawMessage, error) {
	var data json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(d) - 'profile' ORDER BY d.wake_date),'[]')
		FROM sleep_shadow.daily d WHERE profile=$1`, profile).Scan(&data)
	return data, err
}
