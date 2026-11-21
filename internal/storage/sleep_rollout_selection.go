package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/internal/sleepstudy"
	"health-receiver/internal/sleepwire"
)

const sleepSelectionPolicy = "source-covered-personal-window-v1"

// Candidates carry up to 72 elapsed hours, and a neighboring packet can
// move a borderline fragment across a local date or an offset change.
const rolloutAccountingNeighborDays = 4
const rolloutFragmentNeighborDays = 2

type rolloutEpisode struct {
	studyCandidate
	packetID string
}

type rolloutEpisodeDay struct {
	zone        string
	mixedZone   bool
	withdrawals []sleepwire.Coverage
	episodes    []rolloutEpisode
}

type sleepSelection struct {
	Policy            string   `json:"policy"`
	State             string   `json:"state"`
	Reason            string   `json:"reason"`
	AdditionalSeconds *float64 `json:"additional_seconds"`
	AccountedSeconds  *float64 `json:"accounted_seconds"`
}

// Source identities are stored alongside selection evidence for the local
// reader. The public audit decodes sleepSelection and omits these identities.
type storedSleepSelection struct {
	sleepSelection
	CompleteSources   []string `json:"complete_sources,omitempty"`
	ObservedSources   []string `json:"observed_sources,omitempty"`
	GoProjectionOwned bool     `json:"go_projection_owned,omitempty"`
}

func addAffectedEpisodeDates(dates map[string]bool, snapshot sleepwire.Candidate) error {
	if snapshot.Coverage == nil {
		return nil
	}
	if snapshot.Withdrawal != nil {
		dates[snapshot.Withdrawal.WakeDate] = true
		return nil
	}
	location, err := time.LoadLocation(snapshot.Zone)
	if err != nil {
		return err
	}
	for _, episode := range groupRolloutSnapshot("", snapshot.Coverage.SourceID, 0, snapshot, location) {
		spans := episode.episode.Asleep
		dates[spans[len(spans)-1].End.In(location).Format(time.DateOnly)] = true
	}
	return nil
}

func (s *DB) refreshSleepEpisodeSelection(ctx context.Context, tx pgx.Tx, profile uuid.UUID, changed []string, strictChanged map[string]bool, priorities []string, generation int64, today string) ([]string, error) {
	var refreshed []string
	for _, date := range changed {
		if strictChanged[date] {
			continue
		}
		var strict bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sleep_rollout_daily WHERE wake_date=$1::text::date AND episode_selection IS NOT NULL)`, date).Scan(&strict); err != nil {
			return nil, err
		}
		if strict {
			continue
		}
		if _, err := tx.Exec(ctx, rolloutDailySQL, profile, date, generation); err != nil {
			return nil, fmt.Errorf("rollout daily: %w", err)
		}
		refreshed = append(refreshed, date)
	}
	var dirty []string
	for date := range strictChanged {
		dirty = append(dirty, date)
	}
	slices.Sort(dirty)
	// A stable candidate identity may move between dates. Process disjoint
	// causal timespans independently and bound the scan to the model history.
	for i := 0; i < len(dirty); {
		j := i + 1
		for j < len(dirty) && dirty[j] <= subtractDays(dirty[j-1], -sleepstudy.HistoryDays-2*rolloutAccountingNeighborDays-2*rolloutFragmentNeighborDays) {
			j++
		}
		from, through := subtractDays(dirty[i], rolloutAccountingNeighborDays+rolloutFragmentNeighborDays), subtractDays(dirty[j-1], -sleepstudy.HistoryDays-rolloutAccountingNeighborDays-rolloutFragmentNeighborDays)
		part, err := refreshSleepEpisodeRange(ctx, tx, profile, strictChanged, priorities, generation, from, min(through, today), today)
		if err != nil {
			return nil, err
		}
		refreshed = append(refreshed, part...)
		i = j
	}
	return refreshed, nil
}

func refreshSleepEpisodeRange(ctx context.Context, tx pgx.Tx, profile uuid.UUID, strictChanged map[string]bool, priorities []string, generation int64, from, through, today string) ([]string, error) {
	days, err := loadRolloutEpisodes(ctx, tx, profile, subtractDays(from, sleepstudy.HistoryDays+2*rolloutAccountingNeighborDays+rolloutFragmentNeighborDays), subtractDays(through, -2*rolloutAccountingNeighborDays-rolloutFragmentNeighborDays), priorities)
	if err != nil {
		return nil, err
	}
	projection, err := loadSleepGoProjection(ctx, tx)
	if err != nil {
		return nil, err
	}
	decisions := make(map[string]sleepstudy.Decision)
	selected := make(map[string]rolloutEpisode)
	for date := subtractDays(from, rolloutAccountingNeighborDays); date <= min(today, subtractDays(through, -rolloutAccountingNeighborDays)); date = subtractDays(date, -1) {
		if day := days[date]; day != nil {
			decision, main := selectRolloutDay(days, date)
			decisions[date], selected[date] = decision, main
		}
	}
	var dirty []string
	for date := from; date <= through; date = subtractDays(date, -1) {
		day := days[date]
		if day == nil && !strictChanged[date] {
			continue
		}
		main := selected[date]
		meta := storedSleepSelection{sleepSelection: sleepSelection{Policy: sleepSelectionPolicy, State: "unknown", Reason: "revision_moved"}}
		if day != nil {
			meta = summarizeRolloutDay(days, decisions, selected, date)
		}
		meta.GoProjectionOwned = projection.admits(date, meta)
		raw, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		var block LocalShadowSleep
		var candidate *string
		var revision *int64
		if main.packetID != "" {
			block = *studySleepBlock(main.studyCandidate)
			candidate, revision = &main.packetID, &main.candidate.Revision
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sleep_rollout_daily
			(wake_date,generation,candidate,revision,sleep_seconds,deep_seconds,rem_seconds,core_seconds,unspecified_seconds,awake_seconds,stage_conflict,episode_selection)
			VALUES ($1::text::date,$2,$3,$4,$5*3600::double precision,$6*3600::double precision,$7*3600::double precision,
				$8*3600::double precision,$9*3600::double precision,$10*3600::double precision,$11,$12)
			ON CONFLICT(wake_date) DO UPDATE SET generation=excluded.generation,candidate=excluded.candidate,revision=excluded.revision,
				sleep_seconds=excluded.sleep_seconds,deep_seconds=excluded.deep_seconds,rem_seconds=excluded.rem_seconds,
				core_seconds=excluded.core_seconds,unspecified_seconds=excluded.unspecified_seconds,awake_seconds=excluded.awake_seconds,
				stage_conflict=excluded.stage_conflict,
				episode_selection=CASE WHEN sleep_rollout_daily.episode_selection->>'go_projection_owned'='true'
					THEN jsonb_set(excluded.episode_selection,'{go_projection_owned}','true'::jsonb)
					ELSE excluded.episode_selection END`,
			date, generation, candidate, revision, block.Total, block.Deep, block.REM, block.Core, block.Unspecified, block.Awake, block.Conflict, json.RawMessage(raw)); err != nil {
			return nil, err
		}
		dirty = append(dirty, date)
	}
	return dirty, nil
}

func loadRolloutEpisodes(ctx context.Context, tx pgx.Tx, profile uuid.UUID, from, through string, priorities []string) (map[string]*rolloutEpisodeDay, error) {
	rows, err := tx.Query(ctx, `SELECT id,id::jsonb->>0,snapshot
		FROM sleep_rollout_candidates WHERE profile=$1 AND wake_date BETWEEN $2::text::date AND $3::text::date
		AND snapshot ? 'coverage' ORDER BY id`, profile, from, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := make(map[string]*rolloutEpisodeDay)
	dayFor := func(date, zone string) *rolloutEpisodeDay {
		if days[date] == nil {
			days[date] = &rolloutEpisodeDay{zone: zone}
		}
		day := days[date]
		day.mixedZone = day.mixedZone || day.zone != zone
		return day
	}
	for rows.Next() {
		var id, source string
		var raw []byte
		if err := rows.Scan(&id, &source, &raw); err != nil {
			return nil, err
		}
		var snapshot sleepwire.Candidate
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return nil, err
		}
		wake, err := snapshot.ValidateSource(source)
		if err != nil {
			return nil, err
		}
		if snapshot.Withdrawal != nil {
			day := dayFor(wake, snapshot.Zone)
			day.withdrawals = append(day.withdrawals, *snapshot.Coverage)
			continue
		}
		loc, err := time.LoadLocation(snapshot.Zone)
		if err != nil {
			return nil, err
		}
		priority := slices.Index(priorities, source)
		if priority < 0 {
			priority = len(priorities)
		}
		for _, episode := range groupRolloutSnapshot(id, source, priority, snapshot, loc) {
			spans := episode.episode.Asleep
			date := spans[len(spans)-1].End.In(loc).Format(time.DateOnly)
			day := dayFor(date, snapshot.Zone)
			day.episodes = append(day.episodes, episode)
		}
	}
	disqualifySplitSourcePackets(days)
	return days, rows.Err()
}

// Independent packet revisions cannot certify a contiguous same-device
// episode. Reject near-adjacent or partially overlapping fragments across
// wake dates until one authoritative snapshot carries the full set.
func disqualifySplitSourcePackets(days map[string]*rolloutEpisodeDay) {
	sources := make(map[string][]*rolloutEpisode)
	for _, day := range days {
		for i := range day.episodes {
			episode := &day.episodes[i]
			sources[episode.episode.Source] = append(sources[episode.episode.Source], episode)
		}
	}
	for _, episodes := range sources {
		slices.SortFunc(episodes, func(a, b *rolloutEpisode) int {
			return a.episode.Asleep[0].Start.Compare(b.episode.Asleep[0].Start)
		})
		for i, first := range episodes {
			a := first.episode.Asleep
			for _, second := range episodes[i+1:] {
				b := second.episode.Asleep
				gap := b[0].Start.Sub(a[len(a)-1].End)
				if gap > studyBridgeLimit {
					break
				}
				if first.packetID == second.packetID {
					continue
				}
				if gap < 0 && (sleepstudy.Overlap(a, sleepstudy.Span{Start: b[0].Start, End: b[len(b)-1].End}) == sleepstudy.Duration(a) ||
					sleepstudy.Overlap(b, sleepstudy.Span{Start: a[0].Start, End: a[len(a)-1].End}) == sleepstudy.Duration(b)) {
					// A contained observation introduces no additional edge.
					continue
				}
				first.episode.Covered = false
				second.episode.Covered = false
			}
		}
	}
}

// Group sleep from absolute asleep bounds. Awake may corroborate a longer
// break, while in-bed never connects two asleep intervals.
func groupRolloutSnapshot(id, source string, priority int, snapshot sleepwire.Candidate, location *time.Location) []rolloutEpisode {
	var detail, coarse, awake []sleepstudy.Span
	for _, stage := range snapshot.Stages {
		span := sleepstudy.Span{Start: stage.Start, End: stage.End}
		switch stage.Kind {
		case "core", "deep", "rem":
			detail = append(detail, span)
		case "asleep_unspecified":
			coarse = append(coarse, span)
		case "awake":
			awake = append(awake, span)
		}
	}
	basis := sleepstudy.Union(coarse)
	if len(detail) != 0 {
		basis = sleepstudy.Union(detail)
		// Standalone coarse observations can be separate episodes. Coarse
		// observations overlapping detail cannot act as a gap bridge.
		for _, span := range coarse {
			if !studyIntersects(detail, []sleepstudy.Span{span}) {
				basis = append(basis, span)
			}
		}
	}
	groups := sleepstudy.Group(basis, studyBridgeLimit)
	var merged [][]sleepstudy.Span
	for _, group := range groups {
		if len(merged) != 0 {
			last := merged[len(merged)-1]
			combined := append(slices.Clone(last), group...)
			if studyHasRecordedWakeGaps(combined, awake, sleepstudy.Span{Start: snapshot.Coverage.Start, End: snapshot.Coverage.End}) {
				merged[len(merged)-1] = combined
				continue
			}
		}
		merged = append(merged, group)
	}
	out := make([]rolloutEpisode, 0, len(merged))
	for i, spans := range merged {
		start, end := spans[0].Start, spans[len(spans)-1].End
		part := snapshot
		part.Stages = nil
		if len(merged) > 1 {
			part.ZeroStages = nil
		}
		var attached []sleepstudy.Stage
		for _, stage := range snapshot.Stages {
			sourceSpan := sleepstudy.Span{Start: stage.Start, End: stage.End}
			switch stage.Kind {
			case "core", "deep", "rem", "asleep_unspecified":
				if sleepstudy.Overlap(spans, sourceSpan) > 0 {
					attached = append(attached, sleepstudy.Stage{Kind: stage.Kind, Span: sourceSpan})
				}
			}
			stage.Start, stage.End = maxTime(stage.Start, start), minTime(stage.End, end)
			if !stage.Start.Before(stage.End) {
				continue
			}
			if stage.Kind != "asleep_unspecified" {
				part.Stages = append(part.Stages, stage)
				continue
			}
			// A coarse interval may corroborate stages, but it cannot add
			// asleep minutes between detailed blocks or extend their edges.
			for _, asleep := range spans {
				piece := stage
				piece.Start, piece.End = maxTime(piece.Start, asleep.Start), minTime(piece.End, asleep.End)
				if piece.Start.Before(piece.End) {
					part.Stages = append(part.Stages, piece)
				}
			}
		}
		_, coarseEdge := sleepstudy.StagedCoverage(attached, studyBridgeLimit)
		wakeDate := end.In(location).Format(time.DateOnly)
		bounds := rolloutAccountingWindow(wakeDate, location)
		covered := !coarseEdge && snapshot.Coverage.Contains(bounds.Start, bounds.End)
		e := sleepstudy.Episode{ID: fmt.Sprintf("%s/%d", id, i), Source: source, Priority: priority, Asleep: spans, Covered: covered}
		item := studyCandidate{candidate: part, episode: e, profileCovered: covered, sleepBridge: len(sleepstudy.Group(spans, studyBridgeLimit)) > 1, coarseEdge: coarseEdge}
		block := studySleepBlock(item)
		item.episode.Covered = item.episode.Covered && block.Conflict != nil && !*block.Conflict
		out = append(out, rolloutEpisode{studyCandidate: item, packetID: id})
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func minTime(a, b time.Time) time.Time {
	if a.After(b) {
		return b
	}
	return a
}

func rolloutSleepWindow(days map[string]*rolloutEpisodeDay, date, zone string) *sleepstudy.Window {
	var history []sleepstudy.PriorDay
	for i := sleepstudy.HistoryDays; i > 0; i-- {
		priorDate := subtractDays(date, i)
		if prior := days[priorDate]; prior != nil && !prior.mixedZone {
			row := sleepstudy.PriorDay{Date: priorDate, Zone: prior.zone}
			for _, candidate := range prior.episodes {
				episode := candidate.episode
				// A long gap must be confirmed again against today's
				// personal window. It cannot seed the historical anchor.
				episode.Covered = episode.Covered && !candidate.sleepBridge
				row.Episodes = append(row.Episodes, episode)
			}
			history = append(history, row)
		}
	}
	return sleepstudy.HistoricalWindow(date, zone, nil, history)
}

func selectRolloutDay(days map[string]*rolloutEpisodeDay, date string) (sleepstudy.Decision, rolloutEpisode) {
	day := days[date]
	if day.mixedZone {
		return sleepstudy.Decision{Reason: "mixed_zones"}, rolloutEpisode{}
	}
	window := rolloutSleepWindow(days, date, day.zone)
	var episodes []sleepstudy.Episode
	for _, candidate := range day.episodes {
		e := candidate.episode
		if candidate.sleepBridge {
			var awake []sleepstudy.Span
			for _, stage := range candidate.candidate.Stages {
				if stage.Kind == "awake" {
					awake = append(awake, sleepstudy.Span{Start: stage.Start, End: stage.End})
				}
			}
			e.Covered = e.Covered && window != nil && studyHasRecordedWakeGaps(e.Asleep, awake, sleepstudy.Span{Start: window.Start, End: window.End})
		}
		episodes = append(episodes, e)
	}
	decision := sleepstudy.Select(episodes, window, "")
	decision.Additional, decision.Uncertain = resolveAdditionalEpisodes(episodes, decision)
	for _, candidate := range day.episodes {
		if candidate.episode.ID == decision.Main {
			return decision, candidate
		}
	}
	return decision, rolloutEpisode{}
}

// Overlapping additional candidates can describe one event. Equivalent
// observations contribute their common span once. Competing edges or a
// partially overlapping observation stay uncertain until adjudicated.
func resolveAdditionalEpisodes(episodes []sleepstudy.Episode, decision sleepstudy.Decision) ([]string, []string) {
	var additional []string
	uncertain := slices.Clone(decision.Uncertain)
	for _, e := range episodes {
		if !e.Covered {
			uncertain = append(uncertain, e.ID)
			continue
		}
		if !slices.Contains(decision.Additional, e.ID) {
			continue
		}
		conflict := false
		for _, other := range episodes {
			if e.ID == other.ID || !slices.Contains(decision.Additional, other.ID) ||
				!studyIntersects(e.Asleep, other.Asleep) {
				continue
			}
			if !slices.EqualFunc(e.Asleep, other.Asleep, func(a, b sleepstudy.Span) bool {
				return a.Start.Equal(b.Start) && a.End.Equal(b.End)
			}) {
				conflict = true
				break
			}
		}
		if conflict {
			uncertain = append(uncertain, e.ID)
		} else {
			additional = append(additional, e.ID)
		}
	}
	return additional, uncertain
}

func rolloutAccountingWindow(date string, location *time.Location) sleepstudy.Span {
	calendar, _ := time.Parse(time.DateOnly, date) // Dates are selected from verified inputs.
	end := time.Date(calendar.Year(), calendar.Month(), calendar.Day(), 12, 0, 0, 0, location)
	return sleepstudy.Span{Start: end.AddDate(0, 0, -1), End: end}
}

func summarizeRolloutDay(days map[string]*rolloutEpisodeDay, decisions map[string]sleepstudy.Decision, selected map[string]rolloutEpisode, date string) storedSleepSelection {
	day, decision, main := days[date], decisions[date], selected[date]
	result := storedSleepSelection{sleepSelection: sleepSelection{Policy: sleepSelectionPolicy, State: "unknown", Reason: decision.Reason}}
	if day.mixedZone {
		return result
	}
	location, err := time.LoadLocation(day.zone)
	if err != nil {
		return result
	}
	bounds := rolloutAccountingWindow(date, location)
	var complete, observed []string
	for _, item := range day.episodes {
		proof := item.candidate.Coverage
		if proof != nil {
			observed = append(observed, proof.SourceID)
			if proof.Contains(bounds.Start, bounds.End) {
				complete = append(complete, proof.SourceID)
			}
		}
	}
	for _, proof := range day.withdrawals {
		observed = append(observed, proof.SourceID)
		if proof.Contains(bounds.Start, bounds.End) {
			complete = append(complete, proof.SourceID)
		}
	}
	slices.Sort(complete)
	slices.Sort(observed)
	result.CompleteSources = slices.Compact(complete)
	result.ObservedSources = slices.Compact(observed)
	if main.packetID == "" {
		if len(day.episodes) == 0 {
			for _, proof := range day.withdrawals {
				if proof.Contains(bounds.Start, bounds.End) {
					result.State, result.Reason = "confirmed_source_absence", "withdrawal"
					break
				}
			}
		}
		return result
	}
	result.State = "measured"
	var additional []sleepstudy.Span
	for _, candidate := range day.episodes {
		if slices.Contains(decision.Additional, candidate.episode.ID) {
			additional = append(additional, candidate.episode.Asleep...)
		}
	}
	if len(additional) > 0 {
		value := sleepstudy.Duration(additional).Seconds()
		result.AdditionalSeconds = &value
	}
	if !main.candidate.Coverage.Contains(bounds.Start, bounds.End) {
		return result
	}
	var accounted, unresolved [][]sleepstudy.Span
	for delta := -rolloutAccountingNeighborDays; delta <= rolloutAccountingNeighborDays; delta++ {
		dayKey := subtractDays(date, delta)
		adjacent := days[dayKey]
		if adjacent == nil {
			continue
		}
		selection := decisions[dayKey]
		for _, candidate := range adjacent.episodes {
			if sleepstudy.Overlap(candidate.episode.Asleep, bounds) == 0 {
				continue
			}
			if adjacent.mixedZone || adjacent.zone != day.zone {
				return result
			}
			if selection.Main == candidate.episode.ID || slices.Contains(selection.Additional, candidate.episode.ID) {
				accounted = append(accounted, candidate.episode.Asleep)
			} else if selection.Main == "" || slices.Contains(selection.Uncertain, candidate.episode.ID) {
				unresolved = append(unresolved, candidate.episode.Asleep)
			}
		}
	}
	confirmed := sleepstudy.AccountedDuration(accounted, bounds)
	if sleepstudy.AccountedDuration(append(accounted, unresolved...), bounds) != confirmed {
		return result
	}
	value := confirmed.Seconds()
	result.AccountedSeconds = &value
	return result
}
