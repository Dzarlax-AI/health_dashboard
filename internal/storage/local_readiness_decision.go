package storage

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

type localReadinessCaptureKey struct{}

type LocalReadinessDecision struct {
	CapturedAt     time.Time                  `json:"captured_at"`
	Database       string                     `json:"database"`
	Role           string                     `json:"role"`
	Source         string                     `json:"source_schema"`
	FormulaVersion int                        `json:"formula_version"`
	Input          readinessScoreInput        `json:"input"`
	Candidates     json.RawMessage            `json:"hrv_candidates"`
	Reader         string                     `json:"reader"`
	Path           string                     `json:"reader_path"`
	Result         health.ReadinessProjection `json:"result"`
}

func (s *DB) CaptureLocalReadinessDecision(ctx context.Context, date, reader string) (LocalReadinessDecision, error) {
	packet := LocalReadinessDecision{FormulaVersion: ScoreVersion, Reader: reader}
	if s.profileGraphSchema == "" {
		return packet, errors.New("Readiness capture requires an isolated local profile graph")
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return packet, err
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}, profileGraphSchema: s.profileGraphSchema}
		if err := tx.QueryRow(ctx, "select transaction_timestamp(),current_database(),current_user,current_schema()").Scan(&packet.CapturedAt, &packet.Database, &packet.Role, &packet.Source); err != nil {
			return err
		}
		ctx = context.WithValue(ctx, localReadinessCaptureKey{}, &packet)
		switch reader {
		case "history":
			packet.Path = "history"
			if _, err := unit.computeReadinessHistoryAt(ctx, 1, date); err != nil {
				return err
			}
		case "morning":
			packet.Path = "morning_warm"
			data := unit.rawMetricsFromDailyScoresAt(date, true)
			if data == nil {
				packet.Path = "morning_cold"
				data = unit.rawMetricsFromPoints(date)
			}
			if data == nil {
				return errors.New("no morning Readiness input")
			}
			if _, err := unit.morningReadinessProjection(ctx, data); err != nil {
				return err
			}
		default:
			return errors.New("unsupported Readiness reader")
		}
		if packet.Input.Date != date {
			return errors.New("no Readiness decision on the requested date")
		}
		// All input queries and the original product score share this snapshot.
		return tx.QueryRow(ctx, `select jsonb_agg(to_jsonb(c) order by observed_date desc) from (
            select days.d as observed_date,raw.ms as raw_ms,cache.hrv_avg::text::float8 as cached_ms,
                repair.key is not null as repair_pending, cache.hrv_avg is not null or cache.sleep_total is not null or cache.steps is not null as cache_row_eligible
            from (select ($1::date-n)::text as d from generate_series(0,30)n) days
            left join daily_scores cache on cache.date=days.d
            left join settings repair on repair.key=$2||days.d
            left join lateral(select avg(qty) as ms from metric_points where metric_name='heart_rate_variability'
                and substring(date,1,10)=days.d and quality='ok' and qty>0) raw on true) c`, date, hrvRepairKeyPrefix).Scan(&packet.Candidates)
	})
	return packet, err
}

// VerifyLocalReadinessDecision runs the installed HRV selection and complete
// score models from the same dated measurements seen by the product reader.
func (s *DB) VerifyLocalReadinessDecision(ctx context.Context, packet LocalReadinessDecision) error {
	if s.profileGraphSchema == "" || packet.FormulaVersion != ScoreVersion {
		return errors.New("unsupported local Readiness capture")
	}
	if packet.Reader != "history" && packet.Reader != "morning" {
		return errors.New("invalid captured reader")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var matches bool
	if err = tx.QueryRow(ctx, "select current_database()=$1 and current_user=$2 and current_schema()=$3", packet.Database, packet.Role, packet.Source).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return errors.New("Readiness capture belongs to another profile")
	}
	id := rand.Text()
	_, err = tx.Exec(ctx, `insert into readiness_hrv_snapshot_inputs(snapshot_id,profile,target_date,reader,reader_path,snapshot_state,
             observed_date,raw_ms,cached_ms,repair_pending,cache_row_eligible,evidence_present)
        select $1,current_user,$2,$3,$4,'captured',observed_date,raw_ms,cached_ms,repair_pending,cache_row_eligible,$5
        from jsonb_to_recordset($6::jsonb)as c(observed_date text,raw_ms float8,cached_ms float8,repair_pending bool,cache_row_eligible bool)`,
		id, packet.Input.Date, packet.Reader, packet.Path, packet.Input.HRVPresent, packet.Candidates)
	if err != nil {
		return err
	}
	i := packet.Input
	_, err = tx.Exec(ctx, `insert into readiness_score_inputs(case_id,profile,target_date,reader,hrv_snapshot_id,rhr,sleep,
          rhr_present,sleep_present,low_sleep_quality,hrv_provisional,hrv_samples,headline,illness)
          values ($1,current_user,$2::date,$11,$1,$3,$4,$5,$6,$7,$8,$9,'',$10)`,
		id, i.Date, i.RHR, i.Sleep, i.RHRPresent, i.SleepPresent, i.LowQuality, i.Provisional, i.Samples, i.Illness, packet.Reader)
	if err != nil {
		return err
	}
	var selected []float64
	if err := tx.QueryRow(ctx, "select input_values from "+s.profileGraphRelation("semantic_readiness_hrv_component_v1")+" where profile=current_user and snapshot_id=$1", id).Scan(&selected); err != nil {
		return err
	}
	if !slices.Equal(selected, packet.Input.HRV) {
		return errors.New("dbt selected different dated HRV values than the product reader")
	}
	var got health.ReadinessProjection
	var band string
	err = tx.QueryRow(ctx, "select raw_score,display_score,coalesce(confidence,''),coalesce(cap_reason,''),band from "+s.profileGraphRelation("semantic_readiness_score_v1")+" where profile=current_user and case_id=$1", id).Scan(&got.RawScore, &got.DisplayScore, &got.Confidence, &got.CapReason, &band)
	if err != nil {
		return err
	}
	if got != packet.Result || (packet.Reader == "history" && band != health.ReadinessBand(packet.Result.RawScore)) {
		return errors.New("replayed Readiness differs from captured result")
	}
	return tx.Rollback(ctx)
}
