package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

const cacheMaintenanceSettingKey = "_cache_maintenance_v1"

// AuxiliaryCacheVersion covers historical cache calculations not included in
// AggregateContractChecksum or ScoreVersion. Bump it when their semantics
// change.
const AuxiliaryCacheVersion = 1

const cacheMaintenanceJournalVersion = 1

const (
	CacheMaintenancePhaseAggregates = "aggregates"
	CacheMaintenancePhaseBaseline   = "baseline"
	CacheMaintenancePhaseSustained  = "sustained"
	CacheMaintenancePhaseRecovery   = "recovery"
	CacheMaintenancePhasePassive    = "passive"
	CacheMaintenancePhaseAcute      = "acute"
	CacheMaintenancePhaseChronic    = "chronic"
	CacheMaintenancePhaseDerived    = "derived"
	CacheMaintenancePhaseComplete   = "complete"
)

// CacheMaintenanceState is the durable, tenant-local checkpoint for bounded
// cache recovery. Dirty maps store the generation captured by each date;
// ForegroundDates prevents a later legacy background stage from overwriting a
// live foreground result during the current recovery generation.
type CacheMaintenanceState struct {
	Version           int               `json:"version"`
	CompletedIdentity string            `json:"completed_identity"`
	TargetIdentity    string            `json:"target_identity"`
	Phase             string            `json:"phase"`
	FromDate          string            `json:"from_date"`
	NextDate          string            `json:"next_date"`
	EndDate           string            `json:"end_date"`
	Generation        uint64            `json:"generation"`
	Dirty             map[string]uint64 `json:"dirty"`
	ForegroundDates   map[string]bool   `json:"foreground_dates"`
}

// CacheMaintenanceIdentity identifies the code contracts that determine
// historical aggregate and score cache contents.
func CacheMaintenanceIdentity() string {
	parts := fmt.Sprintf("aggregates=%s\nscore=%d\nauxiliary=%d\nenergy=%d\nrecovery_stability=%d:%d\npassive_efficiency=%d:%d\nacute_risk=%d:%d\nchronic_load=%d:%d",
		AggregateContractChecksum(), ScoreVersion, AuxiliaryCacheVersion, DefaultEnergyConfig().FormulaVersion,
		recoveryStabilityFormulaVersion, recoveryStabilityFeatureVersion,
		passiveEfficiencyFormulaVersion, passiveEfficiencyFeatureVersion,
		acuteRiskFormulaVersion, acuteRiskFeatureVersion,
		chronicLoadFormulaVersion, chronicLoadFeatureVersion)
	sum := sha256.Sum256([]byte(parts))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func emptyCacheMaintenanceState() CacheMaintenanceState {
	return CacheMaintenanceState{
		Version:         cacheMaintenanceJournalVersion,
		Dirty:           make(map[string]uint64),
		ForegroundDates: make(map[string]bool),
	}
}

func cloneCacheMaintenanceState(state CacheMaintenanceState) CacheMaintenanceState {
	out := state
	out.Dirty = make(map[string]uint64, len(state.Dirty))
	for date, generation := range state.Dirty {
		out.Dirty[date] = generation
	}
	out.ForegroundDates = make(map[string]bool, len(state.ForegroundDates))
	for date, foreground := range state.ForegroundDates {
		out.ForegroundDates[date] = foreground
	}
	return out
}

func validateCacheDate(date string) error {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Format("2006-01-02") != date {
		return fmt.Errorf("invalid cache maintenance date %q", date)
	}
	return nil
}

func normalizeCacheMaintenanceState(state *CacheMaintenanceState) error {
	if state.Version != cacheMaintenanceJournalVersion {
		return fmt.Errorf("unsupported cache maintenance journal version %d", state.Version)
	}
	if state.Dirty == nil {
		state.Dirty = make(map[string]uint64)
	}
	if state.ForegroundDates == nil {
		state.ForegroundDates = make(map[string]bool)
	}
	if state.TargetIdentity == "" {
		if state.Phase != "" || state.FromDate != "" || state.NextDate != "" || state.EndDate != "" {
			return errors.New("cache maintenance cursor exists without a target identity")
		}
	} else {
		switch state.Phase {
		case CacheMaintenancePhaseAggregates, CacheMaintenancePhaseBaseline, CacheMaintenancePhaseSustained, CacheMaintenancePhaseRecovery, CacheMaintenancePhasePassive, CacheMaintenancePhaseAcute, CacheMaintenancePhaseChronic, CacheMaintenancePhaseDerived, CacheMaintenancePhaseComplete:
		default:
			return fmt.Errorf("invalid cache maintenance phase %q", state.Phase)
		}
		if err := validateCacheDate(state.FromDate); err != nil {
			return fmt.Errorf("invalid cache maintenance from date: %w", err)
		}
		if err := validateCacheDate(state.EndDate); err != nil {
			return fmt.Errorf("invalid cache maintenance end date: %w", err)
		}
		if state.FromDate > state.EndDate {
			return errors.New("cache maintenance from date is after end date")
		}
		if state.Phase == CacheMaintenancePhaseComplete {
			if state.NextDate != "" {
				return errors.New("complete cache maintenance phase has a cursor")
			}
		} else if state.NextDate != "" {
			if err := validateCacheDate(state.NextDate); err != nil {
				return fmt.Errorf("invalid cache maintenance next date: %w", err)
			}
			if state.NextDate > state.EndDate {
				return errors.New("cache maintenance next date is after end date")
			}
		}
	}
	for date, generation := range state.Dirty {
		if err := validateCacheDate(date); err != nil {
			return err
		}
		if generation == 0 || generation > state.Generation {
			return fmt.Errorf("invalid dirty generation %d for %s", generation, date)
		}
	}
	for date, foreground := range state.ForegroundDates {
		if err := validateCacheDate(date); err != nil {
			return err
		}
		if !foreground {
			return fmt.Errorf("false foreground marker for %s", date)
		}
	}
	return nil
}

func decodeCacheMaintenanceState(raw string) (CacheMaintenanceState, error) {
	var state CacheMaintenanceState
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return CacheMaintenanceState{}, fmt.Errorf("decode cache maintenance journal: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return CacheMaintenanceState{}, errors.New("cache maintenance journal contains multiple JSON values")
		}
		return CacheMaintenanceState{}, fmt.Errorf("decode cache maintenance journal trailing data: %w", err)
	}
	if err := normalizeCacheMaintenanceState(&state); err != nil {
		return CacheMaintenanceState{}, fmt.Errorf("invalid cache maintenance journal: %w", err)
	}
	return state, nil
}

func encodeCacheMaintenanceState(state CacheMaintenanceState) (string, error) {
	if err := normalizeCacheMaintenanceState(&state); err != nil {
		return "", err
	}
	b, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encode cache maintenance journal: %w", err)
	}
	return string(b), nil
}

// lockCacheMaintenanceRow inserts the empty journal if needed, then locks and
// strictly decodes it in the caller's transaction. All journal mutations use
// this row lock so concurrent ingestion and recovery updates cannot lose a
// generation.
func lockCacheMaintenanceRow(ctx context.Context, tx pgx.Tx) (CacheMaintenanceState, error) {
	initial, err := json.Marshal(emptyCacheMaintenanceState())
	if err != nil {
		return CacheMaintenanceState{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO settings (key, value, updated_at)
		VALUES ($1, $2, NOW()::TEXT)
		ON CONFLICT (key) DO NOTHING`, cacheMaintenanceSettingKey, string(initial)); err != nil {
		return CacheMaintenanceState{}, fmt.Errorf("initialize cache maintenance journal: %w", err)
	}
	var raw string
	if err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1 FOR UPDATE`, cacheMaintenanceSettingKey).Scan(&raw); err != nil {
		return CacheMaintenanceState{}, fmt.Errorf("lock cache maintenance journal: %w", err)
	}
	return decodeCacheMaintenanceState(raw)
}

func saveCacheMaintenanceRow(ctx context.Context, tx pgx.Tx, state CacheMaintenanceState) error {
	raw, err := encodeCacheMaintenanceState(state)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE settings SET value = $2, updated_at = NOW()::TEXT WHERE key = $1`, cacheMaintenanceSettingKey, raw); err != nil {
		return fmt.Errorf("save cache maintenance journal: %w", err)
	}
	return nil
}

func (s *DB) withCacheMaintenanceTx(ctx context.Context, update func(pgx.Tx, *CacheMaintenanceState) error) (CacheMaintenanceState, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CacheMaintenanceState{}, fmt.Errorf("begin cache maintenance transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := lockCacheMaintenanceRow(ctx, tx)
	if err != nil {
		return CacheMaintenanceState{}, err
	}
	if update != nil {
		if err := update(tx, &state); err != nil {
			return CacheMaintenanceState{}, err
		}
		if err := saveCacheMaintenanceRow(ctx, tx, state); err != nil {
			return CacheMaintenanceState{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return CacheMaintenanceState{}, fmt.Errorf("commit cache maintenance transaction: %w", err)
	}
	return cloneCacheMaintenanceState(state), nil
}

// LoadCacheMaintenance returns a normalized state and initializes the journal
// row on first use. Corrupt or incompatible stored JSON is an error.
func (s *DB) LoadCacheMaintenance(ctx context.Context) (CacheMaintenanceState, error) {
	return s.withCacheMaintenanceTx(ctx, nil)
}

// BeginCacheMaintenance resumes a same-identity cursor, avoids work when that
// identity is already complete, and starts a full inclusive range otherwise.
// Existing dirty-only work does not trigger a historical range.
func (s *DB) BeginCacheMaintenance(ctx context.Context, fromDate, toDate string) (CacheMaintenanceState, error) {
	if err := validateCacheDate(fromDate); err != nil {
		return CacheMaintenanceState{}, err
	}
	if err := validateCacheDate(toDate); err != nil {
		return CacheMaintenanceState{}, err
	}
	if fromDate > toDate {
		return CacheMaintenanceState{}, errors.New("cache maintenance range starts after it ends")
	}
	identity := CacheMaintenanceIdentity()
	return s.withCacheMaintenanceTx(ctx, func(_ pgx.Tx, state *CacheMaintenanceState) error {
		return beginCacheMaintenanceState(state, identity, fromDate, toDate)
	})
}

func beginCacheMaintenanceState(state *CacheMaintenanceState, identity, fromDate, toDate string) error {
	if state.TargetIdentity == identity || (state.TargetIdentity == "" && state.CompletedIdentity == identity) {
		return nil
	}
	state.TargetIdentity = identity
	state.Phase = CacheMaintenancePhaseAggregates
	state.FromDate = fromDate
	state.NextDate = fromDate
	state.EndDate = toDate
	state.ForegroundDates = make(map[string]bool, len(state.Dirty))
	for date := range state.Dirty {
		state.ForegroundDates[date] = true
	}
	return nil
}

func normalizeDirtyDates(dates []string) ([]string, error) {
	uniq := make(map[string]struct{}, len(dates))
	for _, date := range dates {
		if err := validateCacheDate(date); err != nil {
			return nil, err
		}
		uniq[date] = struct{}{}
	}
	out := make([]string, 0, len(uniq))
	for date := range uniq {
		out = append(out, date)
	}
	sort.Strings(out)
	return out, nil
}

func markCacheDirtyState(state *CacheMaintenanceState, dates []string) error {
	if len(dates) == 0 {
		return nil
	}
	if state.Generation == ^uint64(0) {
		return errors.New("cache maintenance generation overflow")
	}
	state.Generation++
	for _, date := range dates {
		state.Dirty[date] = state.Generation
		if state.TargetIdentity != "" {
			state.ForegroundDates[date] = true
		}
	}
	return nil
}

// MarkCacheDirty records foreground dates before their cache writes begin.
func (s *DB) MarkCacheDirty(ctx context.Context, dates []string) error {
	dates, err := normalizeDirtyDates(dates)
	if err != nil {
		return err
	}
	_, err = s.withCacheMaintenanceTx(ctx, func(_ pgx.Tx, state *CacheMaintenanceState) error {
		return markCacheDirtyState(state, dates)
	})
	return err
}

// MarkCacheDirtyTx records dates in a caller-owned transaction, allowing an
// import's source mutation and its recovery marker to commit atomically.
func (s *DB) MarkCacheDirtyTx(ctx context.Context, tx pgx.Tx, dates []string) error {
	dates, err := normalizeDirtyDates(dates)
	if err != nil {
		return err
	}
	state, err := lockCacheMaintenanceRow(ctx, tx)
	if err != nil {
		return err
	}
	if err := markCacheDirtyState(&state, dates); err != nil {
		return err
	}
	return saveCacheMaintenanceRow(ctx, tx, state)
}

// CaptureCacheDirty snapshots generations for selected dates. An empty date
// slice captures every currently dirty date.
func (s *DB) CaptureCacheDirty(ctx context.Context, dates []string) (map[string]uint64, error) {
	selected, err := normalizeDirtyDates(dates)
	if err != nil {
		return nil, err
	}
	state, err := s.LoadCacheMaintenance(ctx)
	if err != nil {
		return nil, err
	}
	captured := make(map[string]uint64)
	if len(selected) == 0 {
		for date, generation := range state.Dirty {
			captured[date] = generation
		}
		return captured, nil
	}
	for _, date := range selected {
		if generation, ok := state.Dirty[date]; ok {
			captured[date] = generation
		}
	}
	return captured, nil
}

// CompleteCacheDirty clears only captured generations that have not been
// superseded by a newer foreground mutation.
func (s *DB) CompleteCacheDirty(ctx context.Context, captured map[string]uint64) error {
	for date, generation := range captured {
		if err := validateCacheDate(date); err != nil {
			return err
		}
		if generation == 0 {
			return fmt.Errorf("invalid captured generation for %s", date)
		}
	}
	_, err := s.withCacheMaintenanceTx(ctx, func(_ pgx.Tx, state *CacheMaintenanceState) error {
		for date, generation := range captured {
			if state.Dirty[date] == generation {
				delete(state.Dirty, date)
			}
		}
		return nil
	})
	return err
}

// AdvanceCacheMaintenance advances the inclusive range cursor. An empty next
// date or a date after EndDate moves the journal to the next durable phase.
func (s *DB) AdvanceCacheMaintenance(ctx context.Context, nextDate string) (CacheMaintenanceState, error) {
	if nextDate != "" {
		if err := validateCacheDate(nextDate); err != nil {
			return CacheMaintenanceState{}, err
		}
	}
	return s.withCacheMaintenanceTx(ctx, func(_ pgx.Tx, state *CacheMaintenanceState) error {
		return advanceCacheMaintenanceState(state, nextDate)
	})
}

func advanceCacheMaintenanceState(state *CacheMaintenanceState, nextDate string) error {
	if state.TargetIdentity == "" {
		return errors.New("cache maintenance has no active target")
	}
	if state.Phase == CacheMaintenancePhaseComplete {
		if nextDate == "" {
			return nil
		}
		return errors.New("cache maintenance phases are already complete")
	}
	if state.NextDate == "" {
		return errors.New("active cache maintenance phase has no cursor")
	}
	if nextDate != "" && nextDate <= state.NextDate {
		return errors.New("cache maintenance cursor must advance")
	}
	if nextDate == "" || nextDate > state.EndDate {
		switch state.Phase {
		case CacheMaintenancePhaseAggregates:
			state.Phase = CacheMaintenancePhaseBaseline
			state.NextDate = state.FromDate
		case CacheMaintenancePhaseBaseline:
			state.Phase = CacheMaintenancePhaseSustained
			state.NextDate = state.FromDate
		case CacheMaintenancePhaseSustained:
			state.Phase = CacheMaintenancePhaseRecovery
			state.NextDate = state.FromDate
		case CacheMaintenancePhaseRecovery:
			state.Phase = CacheMaintenancePhasePassive
			state.NextDate = state.FromDate
		case CacheMaintenancePhasePassive:
			state.Phase = CacheMaintenancePhaseAcute
			state.NextDate = state.FromDate
		case CacheMaintenancePhaseAcute:
			state.Phase = CacheMaintenancePhaseChronic
			state.NextDate = state.FromDate
		case CacheMaintenancePhaseChronic:
			state.Phase = CacheMaintenancePhaseDerived
			state.NextDate = state.FromDate
		case CacheMaintenancePhaseDerived:
			state.Phase = CacheMaintenancePhaseComplete
			state.NextDate = ""
		default:
			return fmt.Errorf("invalid cache maintenance phase %q", state.Phase)
		}
	} else {
		state.NextDate = nextDate
	}
	return nil
}

// FinishCacheMaintenance publishes the target identity only after the range
// and every dirty date have completed. Foreground protection remains durable
// until this point.
func (s *DB) FinishCacheMaintenance(ctx context.Context) (CacheMaintenanceState, error) {
	return s.withCacheMaintenanceTx(ctx, func(_ pgx.Tx, state *CacheMaintenanceState) error {
		return finishCacheMaintenanceState(state)
	})
}

func finishCacheMaintenanceState(state *CacheMaintenanceState) error {
	if state.TargetIdentity == "" {
		return errors.New("cache maintenance has no active target")
	}
	if state.NextDate != "" {
		return errors.New("cache maintenance phases are incomplete")
	}
	if state.Phase != CacheMaintenancePhaseComplete {
		return errors.New("cache maintenance dependency phases are incomplete")
	}
	if len(state.Dirty) != 0 {
		return fmt.Errorf("cache maintenance has %d dirty dates", len(state.Dirty))
	}
	state.CompletedIdentity = state.TargetIdentity
	state.TargetIdentity = ""
	state.Phase = ""
	state.FromDate = ""
	state.NextDate = ""
	state.EndDate = ""
	state.ForegroundDates = make(map[string]bool)
	return nil
}
