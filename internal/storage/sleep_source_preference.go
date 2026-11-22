package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const PreferredSleepSourceSetting = "preferred_sleep_source"

var ErrUnknownSleepSource = errors.New("unknown sleep source")

// Legacy samples identify a source by its exact label. A packet identifies it
// by a publisher-supplied ID. Do not infer identity from the device family.
const pointSleepSources = `SELECT source FROM metric_points
	WHERE metric_name IN ('night_sleep_total','sleep_total','sleep_core','sleep_deep','sleep_rem','sleep_unspecified')
	AND quality='ok' AND qty>0 AND source<>''`

// Older installations can have point data without the optional packet tables.
func sleepSourceOptions(ctx context.Context, q ContractCatalog) (string, error) {
	var points, packets bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_attribute
		WHERE attrelid=to_regclass(format('%I.metric_points',current_schema())) AND attname='quality' AND NOT attisdropped),
		to_regclass(format('%I.sleep_rollout_candidates',current_schema())) IS NOT NULL`).Scan(&points, &packets); err != nil {
		return "", err
	}
	var parts []string
	if points {
		parts = append(parts, pointSleepSources)
	}
	if packets {
		parts = append(parts, `SELECT id::jsonb->>0 FROM sleep_rollout_candidates
		WHERE snapshot->'withdrawal' IS NULL AND id::jsonb->>0 <> ''`)
	}
	if len(parts) == 0 {
		return `SELECT NULL::text AS source WHERE false`, nil
	}
	return strings.Join(parts, " UNION ALL "), nil
}

func (s *DB) SleepSourcePreference(ctx context.Context) (preferred string, sources []string, err error) {
	opts, err := sleepSourceOptions(ctx, s.pool)
	if err != nil {
		return "", nil, err
	}
	err = s.pool.QueryRow(ctx, `SELECT coalesce((SELECT value FROM settings WHERE key=$1),''),
		ARRAY(SELECT DISTINCT source COLLATE "C" FROM (`+opts+`) AS known(source) ORDER BY 1)`, PreferredSleepSourceSetting).Scan(&preferred, &sources)
	return
}

// SaveUserSettings validates the source and commits all fields of a settings
// request together. An empty preference restores automatic selection.
func (s *DB) SaveUserSettings(ctx context.Context, settings map[string]string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if value, ok := settings[PreferredSleepSourceSetting]; ok && value != "" {
		opts, err := sleepSourceOptions(ctx, tx)
		if err != nil {
			return err
		}
		var known bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM (`+opts+`) choices(source) WHERE source=$1)
			OR EXISTS(SELECT 1 FROM settings WHERE key=$2 AND value=$1)`, value, PreferredSleepSourceSetting).Scan(&known); err != nil {
			return err
		}
		if !known {
			return ErrUnknownSleepSource
		}
	}
	if err = writeSettings(ctx, tx, settings); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func readPreferredSleepSource(ctx context.Context, q ContractCatalog) (string, error) {
	var source string
	err := q.QueryRow(ctx, `SELECT coalesce((SELECT value FROM settings WHERE key=$1),'')`, PreferredSleepSourceSetting).Scan(&source)
	if err != nil {
		return "", fmt.Errorf("read preferred sleep source: %w", err)
	}
	return source, nil
}

func writeSettings(ctx context.Context, tx pgx.Tx, settings map[string]string) error {
	for key, value := range settings {
		if _, err := tx.Exec(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()::text)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value); err != nil {
			return err
		}
	}
	return nil
}
