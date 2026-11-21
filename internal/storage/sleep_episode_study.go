package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
	"health-receiver/internal/sleepstudy"
	"health-receiver/internal/sleepwire"
)

const studyPolicy = "personal-window-v1.1"
const studyBridgeLimit = 30 * time.Minute

type studyCandidate struct {
	candidate      sleepwire.Candidate
	episode        sleepstudy.Episode
	coarseEdge     bool
	sleepBridge    bool
	profileCovered bool
}

type studyDay struct {
	zone       string
	mixedZone  bool
	candidates []studyCandidate
}

type studySummary struct {
	Policy       string               `json:"policy"`
	From         string               `json:"from"`
	Through      string               `json:"through"`
	Counters     map[string]int       `json:"counters"`
	Reasons      map[string]int       `json:"decision_reasons"`
	Rejections   map[string]int       `json:"rejection_details"`
	Changes      map[string]float64   `json:"maximum_absolute_change"`
	Cases        map[string]studyCase `json:"pilot_cases,omitempty"`
	RecordedWake *studyMetricEffects  `json:"recorded_wake_same_day_effects,omitempty"`
}

type studyMetricEffects struct {
	Counters map[string]int     `json:"counters"`
	Changes  map[string]float64 `json:"maximum_absolute_change"`
}

type studyCase struct {
	Reason            string `json:"reason"`
	RejectionDetail   string `json:"rejection_detail,omitempty"`
	MainMinutes       *int   `json:"main_minutes"`
	AccountedMinutes  *int   `json:"confirmed_minutes_in_accounting_window"`
	AdditionalMinutes *int   `json:"additional_minutes"`
	Candidates        int    `json:"candidates"`
	CoarseExtension   bool   `json:"coarse_extension"`
	SleepBridge       bool   `json:"sleep_bridge"`
}

func (s *studySummary) tally(name string, matched bool) {
	s.Counters[name] += 0
	if matched {
		s.Counters[name]++
	}
}

func (s *studySummary) delta(name string, value float64) {
	s.Changes[name] = max(s.Changes[name], math.Abs(value))
}

// SleepEpisodeStudy runs only the proof-of-concept. It neither prepares nor
// publishes a projection. Selected tenant and physiological inputs stay local
// to the transaction. The returned result contains aggregate counts only.
func SleepEpisodeStudy(ctx context.Context, conn *pgx.Conn, from, through string, reportDates []string) (out studySummary, err error) {
	step := "date validation"
	defer func() {
		if err != nil {
			var pgError interface{ SQLState() string }
			if errors.As(err, &pgError) {
				err = fmt.Errorf("sleep episode study: %s (%s)", step, pgError.SQLState())
			} else {
				err = fmt.Errorf("sleep episode study: %s", step)
			}
		}
	}()
	first, e1 := time.Parse(time.DateOnly, from)
	last, e2 := time.Parse(time.DateOnly, through)
	if e1 != nil || e2 != nil || last.Before(first) || last.Sub(first) > 180*24*time.Hour {
		return out, errors.New("expected at most 181 calendar days")
	}
	step = "open snapshot"
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='1s'; SET LOCAL idle_in_transaction_session_timeout='15s'"); err != nil {
		return out, err
	}
	step = "verify tenant and read-only snapshot"
	var count int
	var schema, role string
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(min(schema_name),''),coalesce(min(db_role),'')
 FROM health_registry.users WHERE provisioning_state='active' AND is_admin AND db_isolation_ready`).Scan(&count, &schema, &role); err != nil {
		return out, err
	}
	if count != 1 || schema == "" || role == "" {
		return out, errors.New("primary scope not unique")
	}
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('search_path',$1,true)", pgx.Identifier{schema}.Sanitize()); err != nil {
		return out, err
	}
	var isolated bool
	if err = tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only')='on' AND current_setting('transaction_isolation')='repeatable read'
 AND current_user=$1 AND current_schema()=$2 AND schema_contract_version >= 15 FROM __tenant_identity`, role, schema).Scan(&isolated); err != nil {
		return out, err
	}
	if !isolated {
		return out, errors.New("snapshot guard failed")
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	out = studySummary{Policy: studyPolicy, From: from, Through: through, Counters: map[string]int{}, Reasons: map[string]int{}, Rejections: map[string]int{}, Changes: map[string]float64{}, Cases: map[string]studyCase{}}
	step = "read current candidate revisions"
	days, err := loadStudyCandidates(ctx, tx, subtractDays(from, rolloutReadinessBaselineDays+sleepstudy.HistoryDays), through)
	if err != nil {
		return out, err
	}
	step = "project episode roles"
	var history []sleepstudy.PriorDay
	wakeEffects := studySummary{Counters: map[string]int{}, Changes: map[string]float64{}}
	blocks := map[string]*LocalShadowSleep{}
	subsets := map[string]studyCase{}
	selected := map[string]string{}
	wakeScenario := map[string]string{}
	confirmed := map[string][][]sleepstudy.Span{}
	for date := subtractDays(from, rolloutReadinessBaselineDays+sleepstudy.HistoryDays); date <= through; date = subtractDays(date, -1) {
		day := days[date]
		if day == nil {
			continue
		}
		episodes := make([]sleepstudy.Episode, 0, len(day.candidates))
		for _, c := range day.candidates {
			episodes = append(episodes, c.episode)
		}
		window := sleepstudy.HistoricalWindow(date, day.zone, nil, history)
		if day.mixedZone {
			window = nil
		}
		decision := sleepstudy.Select(episodes, window, "")
		wakeScenario[date] = studyRecordedWakeOnly(day, window).Main
		if day.mixedZone {
			decision.Reason = "mixed_zone"
		}
		history = append(history, sleepstudy.PriorDay{Date: date, Zone: day.zone, Episodes: episodes})
		if day.mixedZone {
			history[len(history)-1].Zone = ""
		}
		result := studyCase{Reason: decision.Reason, Candidates: len(episodes)}
		if decision.Main == "" {
			if decision.Reason == "blind_mode" {
				result.RejectionDetail = studyHistoryRejection(date, day.zone, history)
			} else if decision.Reason == "below_threshold" {
				result.RejectionDetail = studyRejection(day, window)
			}
		}
		block := &LocalShadowSleep{}
		var additions [][]sleepstudy.Span
		for _, c := range day.candidates {
			result.CoarseExtension = result.CoarseExtension || c.coarseEdge
			result.SleepBridge = result.SleepBridge || c.sleepBridge
			if c.episode.ID == decision.Main {
				block = studySleepBlock(c)
				selected[date] = c.episode.ID
				confirmed[date] = append(confirmed[date], c.episode.Asleep)
				minutes := int(math.Round(sleepstudy.Duration(c.episode.Asleep).Minutes()))
				result.MainMinutes = &minutes
			}
			if slices.Contains(decision.Additional, c.episode.ID) {
				additions = append(additions, c.episode.Asleep)
				confirmed[date] = append(confirmed[date], c.episode.Asleep)
			}
		}
		if len(additions) > 0 {
			// Distinct observations are unioned before summing this wake-day tally.
			var spans []sleepstudy.Span
			for _, intervals := range additions {
				spans = append(spans, intervals...)
			}
			minutes := int(math.Round(sleepstudy.Duration(spans).Minutes()))
			result.AdditionalMinutes = &minutes
		}
		if decision.Main == "" && result.CoarseExtension && decision.Reason == "below_threshold" {
			result.Reason = "ambiguous_coarse_edge"
		}
		blocks[date] = block
		subsets[date] = result
	}
	step = "read ordinary evaluation labels after role projection"
	references, err := studyOrdinaryReferences(ctx, tx, from, through)
	if err != nil {
		return out, err
	}
	step = "load current projection inputs"
	cfg := unit.GetEnergyConfig()
	window, err := unit.sleepRolloutPreparationWindow(ctx)
	if err != nil {
		return out, err
	}
	for date := from; date <= through; date = subtractDays(date, -1) {
		c, ok := subsets[date]
		if !ok {
			c.Reason = "no_candidate"
		}
		if c.MainMinutes != nil {
			c.AccountedMinutes = studyAccountedMinutes(date, days, confirmed)
		}
		out.Reasons[c.Reason]++
		if c.RejectionDetail != "" {
			out.Rejections[c.RejectionDetail]++
		}
		reference := references[date]
		out.evaluateOrdinary(reference, blocks[date])
		alternative := wakeScenario[date]
		var scenarioBlock *LocalShadowSleep
		if alternative != selected[date] {
			out.tally("recorded_wake_scenario_recovered", alternative != "" && selected[date] == "")
			out.tally("recorded_wake_scenario_lost_previous_selection", alternative == "" && selected[date] != "")
			out.tally("recorded_wake_scenario_switched_selection", alternative != "" && selected[date] != "")
			if alternative == "" {
				scenarioBlock = &LocalShadowSleep{}
			} else {
				for _, candidate := range days[date].candidates {
					if candidate.episode.ID == alternative {
						scenarioBlock = studySleepBlock(candidate)
						if reference.hours > 0 {
							out.tally("recorded_wake_scenario_new_selection_duration_agrees", math.Abs(*scenarioBlock.Total-reference.hours) <= time.Second.Hours())
						}
						break
					}
				}
			}
		}
		if c.RejectionDetail != "" && reference.hours > 0 {
			out.tally("ordinary_rejected_"+c.RejectionDetail+"_has_duration_reference", true)
			available := false
			for _, candidate := range days[date].candidates {
				if math.Abs(sleepstudy.Duration(candidate.episode.Asleep).Hours()-reference.hours) <= time.Second.Hours() {
					available = true
				}
			}
			out.tally("ordinary_rejected_"+c.RejectionDetail+"_has_matching_candidate_duration", available)
		}
		out.tally("days_with_candidates", ok)
		out.tally("days_with_main", c.MainMinutes != nil)
		if id := selected[date]; id != "" {
			step = "verify selected sleep against SQL"
			same, err := studySQLParity(ctx, tx, id, blocks[date])
			if err != nil {
				return out, err
			}
			out.tally("same_candidate_go_sql_stages_equal", same)
		}
		step = "compare dependent Go metrics"
		out.tally("days_with_additional", c.AdditionalMinutes != nil)
		out.tally("days_with_attributed_accounting_minutes", c.AccountedMinutes != nil)
		out.tally("days_ambiguous_coarse_edge", c.CoarseExtension)
		out.tally("days_with_asleep_gap_in_capture", c.SleepBridge)
		// This retrospective store has no archived morning computation checkpoint
		// with the complete immutable input versions. It supports latest revision
		// comparison; none can be certified as an as-of reconstruction here.
		out.Counters["historical_morning_checkpoint_unverified"]++
		out.tally("days_using_legacy_label", false)
		if slices.Contains(reportDates, date) {
			out.Cases[date] = c
		}
		if !ok && window.From == "" {
			continue
		}
		old, oldBaseline, err := unit.loadRolloutSleepWindow(ctx, date, window)
		if err != nil {
			return out, err
		}
		newBlocks := make(map[string]*LocalShadowSleep, len(old))
		for d, b := range old {
			newBlocks[d] = b
			if next := blocks[d]; next != nil {
				newBlocks[d] = next
			}
		}
		if newBlocks[date] == nil {
			newBlocks[date] = &LocalShadowSleep{}
		}
		baseline, err := unit.studySleepBaseline(ctx, date, newBlocks)
		if err != nil {
			return out, err
		}
		oldR := unit.readinessWithSleep(ctx, date, old, oldBaseline)
		newR := unit.readinessWithSleep(ctx, date, newBlocks, baseline)
		out.tally("readiness_raw_equal", oldR.RawScore == newR.RawScore)
		out.tally("readiness_display_equal", oldR.Score == newR.Score)
		out.tally("readiness_components_equal", reflect.DeepEqual(oldR.Serving, newR.Serving))
		out.delta("readiness_display", float64(newR.Score-oldR.Score))
		if err = unit.compareStudyEnergy(ctx, date, old, newBlocks, cfg, &out); err != nil {
			return out, err
		}
		if scenarioBlock != nil {
			step = "compare recorded wake scenario on fixed history"
			alternativeBlocks := maps.Clone(newBlocks)
			alternativeBlocks[date] = scenarioBlock
			alternativeR := unit.readinessWithSleep(ctx, date, alternativeBlocks, baseline)
			wakeEffects.tally("compared_dates", true)
			wakeEffects.tally("readiness_raw_equal", newR.RawScore == alternativeR.RawScore)
			wakeEffects.tally("readiness_display_equal", newR.Score == alternativeR.Score)
			wakeEffects.tally("readiness_components_equal", reflect.DeepEqual(newR.Serving, alternativeR.Serving))
			wakeEffects.delta("readiness_display", float64(alternativeR.Score-newR.Score))
			if alternative != "" {
				same, err := studySQLParity(ctx, tx, alternative, scenarioBlock)
				if err != nil {
					return out, err
				}
				wakeEffects.tally("go_sql_stage_parity", same)
			}
			if err = unit.compareStudyEnergy(ctx, date, newBlocks, alternativeBlocks, cfg, &wakeEffects); err != nil {
				return out, err
			}
		}
	}
	out.RecordedWake = &studyMetricEffects{Counters: wakeEffects.Counters, Changes: wakeEffects.Changes}
	step = "close snapshot"
	if err = tx.Rollback(ctx); err != nil {
		return out, err
	}
	return out, nil
}

func studyAccountedMinutes(date string, days map[string]*studyDay, confirmed map[string][][]sleepstudy.Span) *int {
	zone := days[date].zone
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil
	}
	day, err := time.ParseInLocation(time.DateOnly, date, loc)
	if err != nil {
		return nil
	}
	end := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)
	bound := sleepstudy.Span{Start: end.AddDate(0, 0, -1), End: end}
	var episodes [][]sleepstudy.Span
	for i := -1; i <= 1; i++ {
		key := subtractDays(date, i)
		if profileDay := days[key]; profileDay != nil && (profileDay.mixedZone || profileDay.zone != zone) {
			return nil
		}
		episodes = append(episodes, confirmed[key]...)
	}
	minutes := int(math.Round(sleepstudy.AccountedDuration(episodes, bound).Minutes()))
	return &minutes
}

func loadStudyCandidates(ctx context.Context, tx pgx.Tx, from, through string) (map[string]*studyDay, error) {
	rows, err := tx.Query(ctx, `SELECT c.wake_date::text,c.id,c.id::jsonb->>0,c.priority,c.snapshot,
 COALESCE((SELECT range_agg(tstzrange(covered_interval_start,covered_interval_end,'[)'))
   FROM sleep_period_coverage p WHERE capture_completeness='complete'
   AND p.wake_date BETWEEN (c.wake_date-2)::text AND (c.wake_date+2)::text),
   '{}'::tstzmultirange) @> (
   SELECT tstzrange(min(start_at),max(end_at),'[)')
   FROM jsonb_to_recordset(c.snapshot->'stages') AS stage(kind text,start_at timestamptz,end_at timestamptz)
   WHERE kind IN ('deep','rem','core','asleep_unspecified'))
 FROM sleep_rollout_candidates c
 WHERE c.profile=(SELECT tenant_id FROM __tenant_identity)
 AND NOT c.snapshot ? 'withdrawal' AND NOT c.snapshot ? 'coverage'
 AND c.wake_date BETWEEN $1::text::date AND $2::text::date ORDER BY c.wake_date,c.id`, from, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := map[string]*studyDay{}
	for rows.Next() {
		var date, id, source string
		var priority int
		var raw []byte
		var covered bool
		if err = rows.Scan(&date, &id, &source, &priority, &raw, &covered); err != nil {
			return nil, err
		}
		var candidate sleepwire.Candidate
		if err = json.Unmarshal(raw, &candidate); err != nil {
			return nil, err
		}
		wake, err := candidate.WakeDate()
		if err != nil || wake != date {
			return nil, errors.New("invalid stored candidate")
		}
		day := days[date]
		if day == nil {
			day = &studyDay{zone: candidate.Zone}
			days[date] = day
		}
		day.mixedZone = day.mixedZone || day.zone != candidate.Zone
		var stages []sleepstudy.Stage
		for _, s := range candidate.Stages {
			stages = append(stages, sleepstudy.Stage{Kind: s.Kind, Span: sleepstudy.Span{Start: s.Start, End: s.End}})
		}
		asleep, coarse := sleepstudy.StagedCoverage(stages, studyBridgeLimit)
		bridge := len(sleepstudy.Group(asleep, studyBridgeLimit)) > 1
		// Accepted coverage confirms that the profile queried the interval.
		// V1 does not bind this proof to the candidate device or revision.
		episode := sleepstudy.Episode{ID: id, Source: source, Priority: priority, Asleep: asleep, Covered: covered && !coarse && !bridge}
		day.candidates = append(day.candidates, studyCandidate{candidate: candidate, episode: episode, coarseEdge: coarse, sleepBridge: bridge, profileCovered: covered})
	}
	return days, rows.Err()
}

var studySQL = `SELECT sleep_seconds/3600,deep_seconds/3600,rem_seconds/3600,
	core_seconds/3600,unspecified_seconds/3600,awake_seconds/3600,stage_conflict FROM (` +
	dbtmetrics.SleepDailySQL("sleep_rollout_candidates", "profile=(SELECT tenant_id FROM __tenant_identity) AND id=$1") + `) model`

func studySQLParity(ctx context.Context, tx pgx.Tx, id string, block *LocalShadowSleep) (bool, error) {
	var observed [6]*float64
	var conflict bool
	err := tx.QueryRow(ctx, studySQL, id).Scan(&observed[0], &observed[1], &observed[2], &observed[3], &observed[4], &observed[5], &conflict)
	if err != nil {
		return false, err
	}
	values := []*float64{block.Total, block.Deep, block.REM, block.Core, block.Unspecified, block.Awake}
	for i, value := range values {
		other := observed[i]
		if (value == nil) != (other == nil) || (value != nil && math.Abs(*value-*other) > 1e-8) {
			return false, nil
		}
	}
	return block.Conflict != nil && *block.Conflict == conflict, nil
}

func studySleepBlock(c studyCandidate) *LocalShadowSleep {
	block := &LocalShadowSleep{}
	block.Total = studyHours(sleepstudy.Duration(c.episode.Asleep))
	kindSpans := map[string][]sleepstudy.Span{}
	for _, s := range c.candidate.Stages {
		kindSpans[s.Kind] = append(kindSpans[s.Kind], sleepstudy.Span{Start: s.Start, End: s.End})
	}
	conflict := false
	for a, k := range []string{"deep", "rem", "core"} {
		for _, other := range []string{"deep", "rem", "core"}[a+1:] {
			conflict = conflict || studyIntersects(kindSpans[k], kindSpans[other])
		}
	}
	conflict = conflict || studyIntersects(c.episode.Asleep, kindSpans["awake"])
	block.Conflict = &conflict
	if conflict {
		return block
	}
	field := func(kind string) *float64 {
		if len(kindSpans[kind]) == 0 && !slices.Contains(c.candidate.ZeroStages, kind) {
			return nil
		}
		return studyHours(sleepstudy.Duration(kindSpans[kind]))
	}
	block.Deep, block.REM, block.Core, block.Awake = field("deep"), field("rem"), field("core"), field("awake")
	if len(kindSpans["asleep_unspecified"]) != 0 || slices.Contains(c.candidate.ZeroStages, "asleep_unspecified") {
		detailed := append(append(kindSpans["deep"][:len(kindSpans["deep"]):len(kindSpans["deep"])], kindSpans["rem"]...), kindSpans["core"]...)
		block.Unspecified = studyHours(sleepstudy.Duration(c.episode.Asleep) - sleepstudy.Duration(detailed))
	}
	return block
}

func studyIntersects(a, b []sleepstudy.Span) bool {
	for _, s := range sleepstudy.Union(b) {
		if sleepstudy.Overlap(a, s) > 0 {
			return true
		}
	}
	return false
}

func studyHours(d time.Duration) *float64 { h := d.Hours(); return &h }

func (s *DB) studySleepBaseline(ctx context.Context, date string, blocks map[string]*LocalShadowSleep) ([]float64, error) {
	rows, err := s.pool.Query(ctx, `SELECT date,sleep_total FROM daily_scores WHERE date BETWEEN $1 AND $2`, subtractDays(date, 30), subtractDays(date, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	legacy := map[string]*float64{}
	for rows.Next() {
		var d string
		var value *float64
		if err = rows.Scan(&d, &value); err != nil {
			return nil, err
		}
		legacy[d] = value
	}
	var result []float64
	for i := 1; i <= 30; i++ {
		day := subtractDays(date, i)
		value := legacy[day]
		if b := blocks[day]; b != nil {
			value = b.Total
		}
		if value != nil && *value > 0 && isFiniteFloat(*value) {
			result = append(result, *value)
		}
	}
	return result, rows.Err()
}

func (s *DB) compareStudyEnergy(ctx context.Context, date string, old, next map[string]*LocalShadowSleep, cfg EnergyConfig, out *studySummary) error {
	from := subtractDays(date, energyWindowDays-1)
	base, err := s.loadDailyEnergyInputs(ctx, from, date)
	if err != nil {
		return err
	}
	project := func(blocks map[string]*LocalShadowSleep) []dailyInputs {
		days := make([]dailyInputs, energyWindowDays)
		for i := range days {
			d := subtractDays(date, energyWindowDays-1-i)
			input := base[d]
			if block := blocks[d]; block != nil {
				input.SleepTotal, input.SleepDeep, input.SleepRem, input.SleepAwake = block.Total, block.Deep, block.REM, block.Awake
			}
			days[i] = input
		}
		return days
	}
	oldInputs, nextInputs := project(old), project(next)
	a := computeBankFromDays(oldInputs, cfg)
	b := computeBankFromDays(nextInputs, cfg)
	oldTrace, nextTrace := studyEnergyTrace(oldInputs, cfg), studyEnergyTrace(nextInputs, cfg)
	out.tally("energy_state_equal", a.State == b.State)
	out.tally("energy_imputed_sleep_equal", slices.Contains(a.Flags, "imputed_sleep") == slices.Contains(b.Flags, "imputed_sleep"))
	comparable := a.State != "stale" && b.State != "stale"
	out.tally("energy_comparable", comparable)
	if !comparable {
		return nil
	}
	if !oldTrace.agrees(a) || !nextTrace.agrees(b) {
		return errors.New("energy trace differs from normal projection")
	}
	out.tally("energy_signed_equal", a.Bank == b.Bank)
	out.tally("energy_display_equal", a.Display == b.Display)
	out.tally("energy_restore_equal", a.TodayRestore == b.TodayRestore)
	out.tally("energy_clamped_today", oldTrace.beforeClamp != oldTrace.bank || nextTrace.beforeClamp != nextTrace.bank)
	out.tally("energy_rounding_hides_bank_change", a.Bank == b.Bank && !studyNear(oldTrace.bank, nextTrace.bank))
	out.tally("energy_rounding_hides_restore_change", a.TodayRestore == b.TodayRestore && !studyNear(oldTrace.restore, nextTrace.restore))
	out.delta("energy_sleep_quality", nextTrace.quality-oldTrace.quality)
	out.delta("energy_incoming", nextTrace.incoming-oldTrace.incoming)
	out.delta("energy_before_limit", nextTrace.beforeClamp-oldTrace.beforeClamp)
	out.delta("energy_bank_unrounded", nextTrace.bank-oldTrace.bank)
	out.delta("energy_restore_unrounded", nextTrace.restore-oldTrace.restore)
	todayOnly := slices.Clone(oldInputs)
	todayOnly[len(todayOnly)-1] = nextInputs[len(nextInputs)-1]
	out.delta("energy_only_current_night_restore", studyEnergyTrace(todayOnly, cfg).restore-oldTrace.restore)
	return nil
}

func studyNear(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }

type energyTrace struct{ quality, incoming, restore, beforeClamp, bank float64 }

func (t energyTrace) agrees(bank BankResult) bool {
	return int(math.Round(t.bank)) == bank.Bank && int(math.Round(t.restore)) == bank.TodayRestore && studyNear(t.quality, bank.Components["sleep_quality"])
}

func studyEnergyTrace(days []dailyInputs, cfg EnergyConfig) energyTrace {
	var quality, drain [energyWindowDays]float64
	var missingSleep, missingActivity [energyWindowDays]bool
	for i, d := range days {
		missingSleep[i] = d.SleepTotal == nil || *d.SleepTotal <= 0 || d.SleepDeep == nil || d.SleepRem == nil || d.SleepAwake == nil
		if !missingSleep[i] {
			quality[i] = health.SleepQuality(*d.SleepTotal, *d.SleepDeep, *d.SleepRem, *d.SleepAwake)
		}
		missingActivity[i] = d.ActiveKcal == nil || *d.ActiveKcal < 0
		if !missingActivity[i] {
			drain[i] = health.DrainV2(*d.ActiveKcal, zeroIfNil(d.SustainedHRLoad), cfg.EffectiveAlpha(), cfg.EffectiveBeta())
		}
	}
	t := energyTrace{bank: energySeed}
	for i := energyIterStart; i < energyWindowDays; i++ {
		if missingSleep[i] {
			quality[i] = trailingAvg(quality[:], missingSleep[:], i, energyMinValidLookback, prefixMedianFloat(quality[:], missingSleep[:], i))
		}
		if missingActivity[i] {
			drain[i] = trailingAvg(drain[:], missingActivity[:], i, energyMinValidLookback, prefixMedianFloat(drain[:], missingActivity[:], i))
		}
		t.quality, t.incoming = quality[i], t.bank
		capacity := health.AsymptoticCapacity(t.incoming, t.quality)
		t.restore, t.beforeClamp = capacity-t.incoming, capacity-drain[i]
		t.bank = health.ClampSignedBank(t.beforeClamp)
	}
	return t
}
