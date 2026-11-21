package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// This opt-in supplies the same sleep inputs to ordinary Go readers during a
// local prepare pilot. It does not create or change a serve publication.
const SleepGoProjectionSetting = "sleep_go_projection"

type sleepGoProjection struct {
	From    string   `json:"from"`
	Through string   `json:"through"`
	Sources []string `json:"sources"`
}

func (s *DB) sleepGoProjectionEnabled() bool {
	return s.sleepRolloutTenant && sleepGoProjectionProcessEnabled()
}

func sleepGoProjectionProcessEnabled() bool {
	return os.Getenv("HEALTH_SLEEP_V2_ENABLED") == "1" && os.Getenv("HEALTH_SLEEP_GO_PROJECTION_ENABLED") == "1"
}

func loadSleepGoProjection(ctx context.Context, tx pgx.Tx) (*sleepGoProjection, error) {
	publication, err := readSleepRolloutPublication(ctx, tx)
	if err != nil {
		return nil, err
	}
	if publication != nil && publication.Projection != nil {
		if err := verifySleepRolloutTenant(ctx, tx, publication.Tenant); err != nil {
			return nil, err
		}
		return publication.Projection, nil
	}
	if !sleepGoProjectionProcessEnabled() {
		return nil, nil
	}
	return loadPreparedSleepGoProjection(ctx, tx)
}

func loadPreparedSleepGoProjection(ctx context.Context, tx pgx.Tx) (*sleepGoProjection, error) {
	var raw string
	err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1
		AND EXISTS(SELECT 1 FROM settings WHERE key=$2 AND value='prepare')`, SleepGoProjectionSetting, SleepRolloutModeSetting).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var projection sleepGoProjection
	if err := json.Unmarshal([]byte(raw), &projection); err != nil {
		return nil, errors.New("invalid Go sleep projection settings")
	}
	first, firstErr := time.Parse(time.DateOnly, projection.From)
	last, lastErr := time.Parse(time.DateOnly, projection.Through)
	if firstErr != nil || lastErr != nil || last.Before(first) || last.Sub(first) > 13*24*time.Hour ||
		len(projection.Sources) == 0 || len(projection.Sources) > 16 {
		return nil, errors.New("invalid Go sleep projection window or source set")
	}
	for _, source := range projection.Sources {
		if strings.TrimSpace(source) == "" || len(source) > 256 {
			return nil, errors.New("invalid Go sleep projection source")
		}
	}
	slices.Sort(projection.Sources)
	projection.Sources = slices.Compact(projection.Sources)
	marker, err := ReadTenantIdentityMarker(ctx, tx, "")
	if err != nil {
		return nil, err
	}
	if marker.TenantID == uuid.Nil || marker.SchemaContractVersion != SchemaContractVersion ||
		marker.SchemaContractChecksum != SchemaContractChecksum() {
		return nil, errors.New("Go sleep projection tenant contract is unverified")
	}
	return &projection, nil
}

func (p *sleepGoProjection) admits(date string, selection storedSleepSelection) bool {
	return p != nil && date >= p.From && date <= p.Through &&
		slices.Equal(selection.CompleteSources, p.Sources) &&
		slices.Equal(selection.ObservedSources, p.Sources) &&
		(selection.State == "confirmed_source_absence" ||
			(selection.State == "measured" && selection.AccountedSeconds != nil))
}

const ownedSleepGoProjectionSQL = `d.episode_selection->>'go_projection_owned' = 'true'`

// The result and daily row were prepared by the same atomic candidate worker.
// Missing or ambiguous coverage leaves the ordinary date on its current lane.
const readySleepGoProjectionSQL = `
	d.episode_selection->>'policy'='source-covered-personal-window-v1'
	AND d.episode_selection->'complete_sources' @> to_jsonb($3::text[])
	AND d.episode_selection->'observed_sources' <@ to_jsonb($3::text[])
	AND (d.episode_selection->>'state'='confirmed_source_absence'
		OR (d.episode_selection->>'state'='measured' AND d.episode_selection->>'accounted_seconds' IS NOT NULL))
	AND EXISTS(SELECT 1 FROM sleep_rollout_results r WHERE r.day=d.wake_date AND r.generation>=d.generation)`

func (s *DB) loadReaderSleepWindow(ctx context.Context, date string, publication SleepRolloutPublication) (map[string]*LocalShadowSleep, []float64, error) {
	projection := publication.goProjection
	if projection == nil {
		return s.loadRolloutSleepWindow(ctx, date, publication.window())
	}
	rows, err := s.pool.Query(ctx, `SELECT day::text,d.generation,NULL::text,
		CASE WHEN proof.ready THEN d.revision END,CASE WHEN proof.ready THEN d.sleep_seconds/3600 END,
		CASE WHEN proof.ready THEN d.deep_seconds/3600 END,CASE WHEN proof.ready THEN d.rem_seconds/3600 END,
		CASE WHEN proof.ready THEN d.core_seconds/3600 END,CASE WHEN proof.ready THEN d.unspecified_seconds/3600 END,
		CASE WHEN proof.ready THEN d.awake_seconds/3600 END,CASE WHEN proof.ready THEN d.stage_conflict END,h.sleep_total
		FROM (SELECT $1::text::date-i AS day FROM generate_series(0,$2::integer) AS days(i)) days
		LEFT JOIN sleep_rollout_daily d ON d.wake_date=day AND day BETWEEN $4::text::date AND $5::text::date AND `+ownedSleepGoProjectionSQL+`
		LEFT JOIN LATERAL (SELECT `+readySleepGoProjectionSQL+` AS ready) proof ON true
		LEFT JOIN daily_scores h ON h.date=day::text ORDER BY day DESC`,
		date, rolloutReadinessBaselineDays, projection.Sources, projection.From, projection.Through)
	return readSleepBlocks(rows, err, date)
}

func hasProjectedEnergyInput(date string, blocks map[string]*LocalShadowSleep) bool {
	from := subtractDays(date, energyWindowDays-1)
	for day := range blocks {
		if day >= from && day <= date {
			return true
		}
	}
	return false
}
