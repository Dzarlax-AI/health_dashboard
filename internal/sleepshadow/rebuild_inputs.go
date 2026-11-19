package sleepshadow

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Keep this list aligned with the local Readiness and Energy readers.
// Rebuild outputs and unused settings are excluded so writes by the rebuild
// itself do not invalidate the checkpoint.
var rebuildInputQueries = []struct {
	table    string
	query    string
	optional bool
}{
	{"daily_scores", `SELECT (to_jsonb(d) - ARRAY['readiness','score_version','computed_at',
		'energy_capacity','energy_eod_current','energy_drain','energy_verdict'])::text
		FROM daily_scores d ORDER BY date COLLATE "C"`, false},
	{"metric_points", `SELECT to_jsonb(p)::text FROM metric_points p ORDER BY id`, false},
	{"hourly_metrics", `SELECT to_jsonb(h)::text FROM hourly_metrics h
		ORDER BY metric_name COLLATE "C",hour COLLATE "C",source COLLATE "C"`, true},
	{"subjective_checkins", `SELECT jsonb_build_array(date,source,status,answer)::text
		FROM subjective_checkins ORDER BY date COLLATE "C",source COLLATE "C"`, true},
	{"settings", `SELECT jsonb_build_array(key,value)::text FROM settings
		WHERE key='timezone' OR key LIKE 'energy.%' ORDER BY key COLLATE "C"`, false},
}

//go:embed rebuild_input_tracking.sql
var rebuildInputTrackingSQL string

func installRebuildInputTracking(ctx context.Context, tx pgx.Tx, profiles []string, bindings map[string]string) error {
	if _, err := tx.Exec(ctx, rebuildInputTrackingSQL); err != nil {
		return err
	}
	for _, profile := range profiles {
		if _, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.rebuild_input_revision (tenant_schema, revision)
			VALUES ($1,0) ON CONFLICT DO NOTHING`, bindings[profile]); err != nil {
			return err
		}
		for _, input := range rebuildInputQueries {
			table := pgx.Identifier{bindings[profile], input.table}.Sanitize()
			var exists, tracked, tracksTruncate bool
			if err := tx.QueryRow(ctx, `SELECT relation IS NOT NULL,
				EXISTS(SELECT 1 FROM pg_catalog.pg_trigger WHERE tgrelid=relation AND tgname='sleep_shadow_rebuild_input'),
				EXISTS(SELECT 1 FROM pg_catalog.pg_trigger WHERE tgrelid=relation AND tgname='sleep_shadow_rebuild_truncate')
				FROM to_regclass($1) AS relation`, table).Scan(&exists, &tracked, &tracksTruncate); err != nil {
				return err
			}
			if !exists && input.optional {
				continue
			}
			if !tracked {
				if _, err := tx.Exec(ctx, `CREATE TRIGGER sleep_shadow_rebuild_input
					AFTER INSERT OR UPDATE OR DELETE ON `+table+`
					FOR EACH ROW EXECUTE FUNCTION sleep_shadow.bump_rebuild_input_revision()`); err != nil {
					return fmt.Errorf("track rebuild input %s: %w", table, err)
				}
			}
			if !tracksTruncate {
				if _, err := tx.Exec(ctx, `CREATE TRIGGER sleep_shadow_rebuild_truncate
					AFTER TRUNCATE ON `+table+`
					FOR EACH STATEMENT EXECUTE FUNCTION sleep_shadow.bump_rebuild_input_revision()`); err != nil {
					return fmt.Errorf("track rebuild input truncate %s: %w", table, err)
				}
			}
		}
	}
	return nil
}

// Fingerprint before and after a profile run. A single read-only snapshot
// covers both the change revision and the streamed contents of the inputs.
func rebuildInputsFingerprint(ctx context.Context, pool *pgxpool.Pool, schema string) (string, error) {
	var fingerprint string
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		revision, err := rebuildInputRevision(ctx, tx, schema)
		if err == nil {
			fingerprint, err = fingerprintRebuildInputs(ctx, tx, schema, revision)
		}
		return err
	})
	return fingerprint, err
}

const rebuildInputRevisionSQL = `SELECT revision FROM sleep_shadow.rebuild_input_revision WHERE tenant_schema=$1`

func rebuildInputRevision(ctx context.Context, tx pgx.Tx, schema string) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, rebuildInputRevisionSQL, schema).Scan(&revision)
	return revision, err
}

func fingerprintRebuildInputs(ctx context.Context, tx pgx.Tx, schema string, revision int64) (string, error) {
	if _, err := tx.Exec(ctx, "SELECT set_config('search_path',$1,true),set_config('TimeZone','UTC',true)",
		pgx.Identifier{schema}.Sanitize()); err != nil {
		return "", err
	}
	hash := sha256.New()
	// Revisit older checkpoints to repair EOD dates after a time zone change.
	hash.Write([]byte("rebuild-inputs-v3"))
	hash.Write([]byte{0})
	hash.Write([]byte(os.Getenv("REPORT_TZ")))
	hash.Write([]byte{0})
	hash.Write(strconv.AppendInt(nil, revision, 10))
	for _, input := range rebuildInputQueries {
		if input.optional {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL",
				pgx.Identifier{schema, input.table}.Sanitize()).Scan(&exists); err != nil {
				return "", err
			}
			if !exists {
				continue
			}
		}
		hash.Write([]byte{0})
		hash.Write([]byte(input.table))
		rows, err := tx.Query(ctx, input.query)
		if err != nil {
			return "", fmt.Errorf("fingerprint %s: %w", input.table, err)
		}
		for rows.Next() {
			hash.Write([]byte{0})
			hash.Write(rows.RawValues()[0])
		}
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("fingerprint %s: %w", input.table, err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
