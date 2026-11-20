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
var rebuildInputTables = []struct {
	table    string
	optional bool
}{
	{"daily_scores", false},
	{"metric_points", false},
	{"hourly_metrics", true},
	{"subjective_checkins", true},
	{"settings", false},
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
		reinstalledTracking := false
		for _, input := range rebuildInputTables {
			table := pgx.Identifier{bindings[profile], input.table}.Sanitize()
			var exists bool
			var tracked, tracksTruncate *bool
			if err := tx.QueryRow(ctx, `SELECT relation IS NOT NULL,
				(SELECT tgenabled IN ('O','A') AND tgfoid='sleep_shadow.bump_rebuild_input_revision()'::regprocedure
				 FROM pg_catalog.pg_trigger WHERE tgrelid=relation AND tgname='sleep_shadow_rebuild_input'),
				(SELECT tgenabled IN ('O','A') AND tgfoid='sleep_shadow.bump_rebuild_input_revision()'::regprocedure
				 FROM pg_catalog.pg_trigger WHERE tgrelid=relation AND tgname='sleep_shadow_rebuild_truncate')
				FROM to_regclass($1) AS relation`, table).Scan(&exists, &tracked, &tracksTruncate); err != nil {
				return err
			}
			if !exists && input.optional {
				continue
			}
			if (tracked != nil && !*tracked) || (tracksTruncate != nil && !*tracksTruncate) {
				return fmt.Errorf("rebuild input tracking is disabled or unexpected on %s", table)
			}
			if tracked == nil {
				reinstalledTracking = true
				if _, err := tx.Exec(ctx, `CREATE TRIGGER sleep_shadow_rebuild_input
					AFTER INSERT OR UPDATE OR DELETE ON `+table+`
					FOR EACH ROW EXECUTE FUNCTION sleep_shadow.bump_rebuild_input_revision()`); err != nil {
					return fmt.Errorf("track rebuild input %s: %w", table, err)
				}
			}
			if tracksTruncate == nil {
				reinstalledTracking = true
				if _, err := tx.Exec(ctx, `CREATE TRIGGER sleep_shadow_rebuild_truncate
					AFTER TRUNCATE ON `+table+`
					FOR EACH STATEMENT EXECUTE FUNCTION sleep_shadow.bump_rebuild_input_revision()`); err != nil {
					return fmt.Errorf("track rebuild input truncate %s: %w", table, err)
				}
			}
		}
		if reinstalledTracking {
			if _, err := tx.Exec(ctx, `UPDATE sleep_shadow.rebuild_input_revision SET revision=revision+1
				WHERE tenant_schema=$1`, bindings[profile]); err != nil {
				return err
			}
		}
	}
	return nil
}

// Fingerprint the durable, trigger-maintained revision and configuration. Tracking
// is installed before this is read; writes update the revision transactionally.
func rebuildInputsFingerprint(ctx context.Context, pool *pgxpool.Pool, schema string) (string, error) {
	var fingerprint string
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		revision, err := rebuildInputRevision(ctx, tx, schema)
		if err == nil {
			fingerprint = fingerprintRebuildInputs(revision)
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

func fingerprintRebuildInputs(revision int64) string {
	hash := sha256.New()
	// Revisit older checkpoints to repair EOD dates after a time zone change.
	hash.Write([]byte("rebuild-inputs-v4-tracked-revision"))
	hash.Write([]byte{0})
	hash.Write([]byte(os.Getenv("REPORT_TZ")))
	hash.Write([]byte{0})
	hash.Write(strconv.AppendInt(nil, revision, 10))

	return hex.EncodeToString(hash.Sum(nil))
}
