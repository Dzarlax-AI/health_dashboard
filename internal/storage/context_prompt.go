package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

const (
	SettingProactiveContextPrompts    = "proactive_context_prompts"
	SettingContextCaveatsEnabled      = "context_caveats_enabled"
	SettingContextPromptRetentionDays = "context_prompt_retention_days"

	ContextPromptDetectedReasonLowSleep    = "low_sleep"
	ContextPromptDetectorVersionLowSleepV2 = "low_sleep_v2"

	ContextPromptStatusReserved   = "reserved"
	ContextPromptStatusPrompted   = "prompted"
	ContextPromptStatusAnswered   = "answered"
	ContextPromptStatusSkipped    = "skipped"
	ContextPromptStatusExpired    = "expired"
	ContextPromptStatusSendFailed = "send_failed"

	ContextPromptCategoryPoorSleep = "poor_sleep_context"
	ContextPromptCategoryStress    = "stress_context"
	ContextPromptCategoryTravel    = "travel_context"
	ContextPromptCategoryUnknown   = "unknown_context"
	ContextPromptCategorySkip      = "skip_context"

	ContextPromptSourceTelegram = "telegram"
)

var DefaultContextPromptCategories = []string{
	ContextPromptCategoryPoorSleep,
	ContextPromptCategoryStress,
	ContextPromptCategoryTravel,
	ContextPromptCategoryUnknown,
	ContextPromptCategorySkip,
}

type ContextPromptInteraction struct {
	PromptID          string
	SignalDate        string
	PromptLocalDate   string
	DetectedReason    string
	DetectorVersion   string
	Status            string
	Category          string
	Source            string
	PromptMessageID   int64
	PromptedAt        time.Time
	ExpiresAt         time.Time
	AnsweredAt        time.Time
	AllowedCategories []string
	Metadata          map[string]any
}

type LowSleepContextDetection struct {
	SignalDate     string
	SleepHours     float64
	BaselineAvg    float64
	BaselineStdDev float64
	BaselineDays   int
	ZScore         float64
	Eligible       bool
	Reason         string
}

type LowSleepPromptGate struct {
	Eligible                bool
	Reason                  string
	SleepStructureDisrupted bool
	TimingDeviation         bool
	RepeatedShortNights     int
	RecentEquivalentPrompt  bool
	ExistingCheckinAnswer   string
	IllnessConfidence       string
}

type lowSleepPromptGateInput struct {
	Candidate              LowSleepContextDetection
	SleepAwakeHours        float64
	TimingDeviation        bool
	RepeatedShortNights    int
	RecentEquivalentPrompt bool
	ExistingCheckinAnswer  string
	IllnessConfidence      string
}

type contextSleepDay struct {
	Date           string
	SleepHours     float64
	SourceCount    int
	MaxSourceSleep float64
}

type ContextAnnotation struct {
	Date           string  `json:"date"`
	DetectedReason string  `json:"detected_reason"`
	Category       string  `json:"category"`
	SleepHours     float64 `json:"sleep_hours,omitempty"`
	BaselineAvg    float64 `json:"baseline_avg,omitempty"`
	ZScore         float64 `json:"z_score,omitempty"`
}

func contextPromptInteractionsTableDDL() string {
	return `CREATE TABLE IF NOT EXISTS context_prompt_interactions (
		prompt_id          TEXT PRIMARY KEY,
		signal_date        DATE NOT NULL,
		prompt_local_date  DATE NOT NULL,
		detected_reason    TEXT NOT NULL,
		detector_version   TEXT NOT NULL,
		status             TEXT NOT NULL,
		category           TEXT,
		source             TEXT,
		prompt_message_id  BIGINT,
		prompted_at        TIMESTAMPTZ,
		expires_at         TIMESTAMPTZ NOT NULL,
		answered_at        TIMESTAMPTZ,
		allowed_categories JSONB NOT NULL DEFAULT '[]'::jsonb,
		metadata           JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE (signal_date, detected_reason)
	)`
}

func contextPromptDailyDedupeIndexDDL() string {
	return `CREATE UNIQUE INDEX IF NOT EXISTS idx_context_prompt_one_sent_per_day
			ON context_prompt_interactions (prompt_local_date)
			WHERE status IN ('reserved','prompted','answered','skipped','expired','send_failed')`
}

func (s *DB) EnsureContextPromptInteractionsTable() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stmts := []string{
		contextPromptInteractionsTableDDL(),
		contextPromptDailyDedupeIndexDDL(),
		`CREATE INDEX IF NOT EXISTS idx_context_prompt_status_expires
			ON context_prompt_interactions (status, expires_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			log.Printf("EnsureContextPromptInteractionsTable: %v", err)
		}
	}
}

func ValidateContextPromptCategory(category string) error {
	switch category {
	case ContextPromptCategoryPoorSleep,
		ContextPromptCategoryStress,
		ContextPromptCategoryTravel,
		ContextPromptCategoryUnknown,
		ContextPromptCategorySkip:
		return nil
	}
	return fmt.Errorf("invalid context prompt category %q", category)
}

func IsContextPromptsEnabled(s *DB) bool {
	return getSettingBool(s, SettingProactiveContextPrompts, false)
}

func IsContextCaveatsEnabled(s *DB) bool {
	return getSettingBool(s, SettingContextCaveatsEnabled, false)
}

func (s *DB) ContextPromptRetentionDays() int {
	days := getSettingInt(s, SettingContextPromptRetentionDays, 180)
	if days < 30 {
		return 30
	}
	if days > 730 {
		return 730
	}
	return days
}

func (s *DB) PruneContextPromptInteractions(now time.Time) (int64, error) {
	cutoff := now.AddDate(0, 0, -s.ContextPromptRetentionDays())
	ctx, cancel := queryCtx()
	defer cancel()
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM context_prompt_interactions
		 WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *DB) DetectLowSleepContextPrompt(signalDate string) (LowSleepContextDetection, error) {
	out := LowSleepContextDetection{SignalDate: signalDate}
	if _, err := time.Parse("2006-01-02", signalDate); err != nil {
		out.Reason = "bad_signal_date"
		return out, fmt.Errorf("signalDate must be YYYY-MM-DD: %w", err)
	}
	ctx, cancel := queryCtx()
	defer cancel()
	var sleep *float64
	if err := s.pool.QueryRow(ctx, `
		SELECT sleep_total
		  FROM daily_scores
		 WHERE date = $1`, signalDate).Scan(&sleep); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			out.Reason = "missing_signal_day"
			return out, nil
		}
		return out, err
	}
	if sleep == nil || *sleep <= 0 {
		out.Reason = "missing_sleep_total"
		return out, nil
	}
	targetStats, err := s.contextSleepSourceStats(ctx, signalDate)
	if err != nil {
		return out, err
	}
	target := contextSleepDay{
		Date:           signalDate,
		SleepHours:     *sleep,
		SourceCount:    targetStats.SourceCount,
		MaxSourceSleep: targetStats.MaxSourceSleep,
	}

	var baseline []contextSleepDay
	rows, err := s.pool.Query(ctx, `
		SELECT date, sleep_total
		  FROM daily_scores
		 WHERE date >= $1
		   AND date < $2
		   AND sleep_total IS NOT NULL
		   AND sleep_total > 0
		 ORDER BY date DESC`,
		subtractDays(signalDate, 30), signalDate)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var date string
		var v float64
		if err := rows.Scan(&date, &v); err != nil {
			return out, err
		}
		stats, err := s.contextSleepSourceStats(ctx, date)
		if err != nil {
			return out, err
		}
		baseline = append(baseline, contextSleepDay{
			Date:           date,
			SleepHours:     v,
			SourceCount:    stats.SourceCount,
			MaxSourceSleep: stats.MaxSourceSleep,
		})
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return evaluateLowSleepContext(target, baseline), nil
}

type contextSleepSourceStats struct {
	SourceCount    int
	MaxSourceSleep float64
}

func (s *DB) contextSleepSourceStats(ctx context.Context, date string) (contextSleepSourceStats, error) {
	var stats contextSleepSourceStats
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int, COALESCE(MAX(source_total), 0)::double precision
		  FROM (
			SELECT source, SUM(avg_val)::double precision AS source_total
			  FROM hourly_metrics
			 WHERE metric_name = 'sleep_total'
			   AND SUBSTRING(hour, 1, 10) = $1
			 GROUP BY source
		  ) src`, date).Scan(&stats.SourceCount, &stats.MaxSourceSleep)
	return stats, err
}

func evaluateLowSleepContext(target contextSleepDay, baseline []contextSleepDay) LowSleepContextDetection {
	out := LowSleepContextDetection{SignalDate: target.Date, SleepHours: target.SleepHours}
	prelim := make([]float64, 0, len(baseline))
	for _, day := range baseline {
		if day.SleepHours >= 3.0 {
			prelim = append(prelim, day.SleepHours)
		}
	}
	baselineRef := 6.0
	if len(prelim) > 0 {
		baselineRef, _ = avgStdDev(prelim)
	}

	validBaseline := make([]float64, 0, len(baseline))
	for _, day := range baseline {
		if reason := contextSleepQualityRejectReason(day, baselineRef); reason != "" {
			continue
		}
		validBaseline = append(validBaseline, day.SleepHours)
	}
	out.BaselineDays = len(validBaseline)
	if len(validBaseline) < 14 {
		out.Reason = "baseline_warmup"
		return out
	}
	avg, sd := avgStdDev(validBaseline)
	out.BaselineAvg = avg
	out.BaselineStdDev = sd

	if reason := contextSleepQualityRejectReason(target, avg); reason != "" {
		out.Reason = reason
		return out
	}
	if sd <= 0.15 {
		out.Reason = "baseline_flat"
		return out
	}
	out.ZScore = (out.SleepHours - avg) / sd
	if out.SleepHours <= 6.0 && out.ZScore <= -1.5 {
		out.Eligible = true
		out.Reason = "eligible"
		return out
	}
	out.Reason = "within_expected_range"
	return out
}

func contextSleepQualityRejectReason(day contextSleepDay, baselineAvg float64) string {
	if day.SleepHours < 3.0 {
		return "capture_gap"
	}
	if day.SourceCount > 1 && day.MaxSourceSleep-day.SleepHours >= 2.0 {
		conflictFloor := math.Max(6.0, baselineAvg-1.0)
		if day.MaxSourceSleep >= conflictFloor {
			return "source_conflict"
		}
	}
	return ""
}

func evaluateLowSleepPromptGate(input lowSleepPromptGateInput) LowSleepPromptGate {
	out := LowSleepPromptGate{
		SleepStructureDisrupted: input.SleepAwakeHours >= 1.0 ||
			(input.Candidate.SleepHours > 0 && input.SleepAwakeHours/input.Candidate.SleepHours >= 0.12),
		TimingDeviation:        input.TimingDeviation,
		RepeatedShortNights:    input.RepeatedShortNights,
		RecentEquivalentPrompt: input.RecentEquivalentPrompt,
		ExistingCheckinAnswer:  input.ExistingCheckinAnswer,
		IllnessConfidence:      input.IllnessConfidence,
	}
	if !input.Candidate.Eligible {
		out.Reason = "candidate_" + input.Candidate.Reason
		return out
	}
	if input.RecentEquivalentPrompt {
		out.Reason = "recent_equivalent_prompt"
		return out
	}
	if input.ExistingCheckinAnswer == CheckinAnswerSick {
		out.Reason = "existing_sick_checkin"
		return out
	}
	if input.IllnessConfidence == "moderate" || input.IllnessConfidence == "high" {
		out.Reason = "active_illness_flow"
		return out
	}
	strongAnomaly := input.Candidate.SleepHours <= 4.5 || input.Candidate.ZScore <= -2.5
	if strongAnomaly || out.SleepStructureDisrupted || out.TimingDeviation || out.RepeatedShortNights >= 2 {
		out.Eligible = true
		out.Reason = "eligible"
		return out
	}
	out.Reason = "low_usefulness"
	return out
}

func avgStdDev(vals []float64) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	avg := sum / float64(len(vals))
	var ss float64
	for _, v := range vals {
		d := v - avg
		ss += d * d
	}
	return avg, math.Sqrt(ss / float64(len(vals)))
}

func (s *DB) ReserveLowSleepContextPrompt(signalDate, promptLocalDate string, now, expiresAt time.Time) (*ContextPromptInteraction, bool, error) {
	detection, err := s.DetectLowSleepContextPrompt(signalDate)
	if err != nil || !detection.Eligible {
		return nil, false, err
	}
	gate, err := s.EvaluateLowSleepPromptUsefulness(signalDate, detection)
	if err != nil || !gate.Eligible {
		return nil, false, err
	}
	metadata := map[string]any{
		"sleep_hours":               round1(detection.SleepHours),
		"baseline_avg":              round1(detection.BaselineAvg),
		"z_score":                   round2(detection.ZScore),
		"baseline_days":             detection.BaselineDays,
		"prompt_gate_reason":        gate.Reason,
		"sleep_structure_disrupted": gate.SleepStructureDisrupted,
		"timing_deviation":          gate.TimingDeviation,
		"repeated_short_nights":     gate.RepeatedShortNights,
	}
	return s.reserveContextPrompt(ContextPromptDetectedReasonLowSleep, ContextPromptDetectorVersionLowSleepV2,
		signalDate, promptLocalDate, DefaultContextPromptCategories, metadata, now, expiresAt)
}

func (s *DB) EvaluateLowSleepPromptUsefulness(signalDate string, candidate LowSleepContextDetection) (LowSleepPromptGate, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	awake, err := s.contextSleepAwakeHours(ctx, signalDate)
	if err != nil {
		return LowSleepPromptGate{}, err
	}
	repeated, err := s.contextRepeatedShortNights(ctx, signalDate, candidate.BaselineAvg)
	if err != nil {
		return LowSleepPromptGate{}, err
	}
	timing, err := s.contextSleepTimingDeviation(ctx, signalDate)
	if err != nil {
		return LowSleepPromptGate{}, err
	}
	recent, err := s.contextRecentEquivalentPrompt(ctx, signalDate)
	if err != nil {
		return LowSleepPromptGate{}, err
	}
	checkinAnswer := ""
	var subjective *health.SubjectiveCheckinSummary
	// daily_scores.sleep_total is keyed by the local wake/sleep-summary
	// date. The subjective morning check-in for that same local date is
	// the user-provided context for the analyzed sleep; do not look at
	// promptLocalDate here.
	if row, err := s.GetTodayCheckin(signalDate, CheckinSourceTelegram); err == nil && row != nil {
		checkinAnswer = row.Answer
		subjective = &health.SubjectiveCheckinSummary{Status: row.Status, Answer: row.Answer}
	} else if err != nil {
		return LowSleepPromptGate{}, err
	}
	illness := health.ComputeIllnessSuspicion(s.BuildIllnessEvidenceInput(signalDate, subjective))
	return evaluateLowSleepPromptGate(lowSleepPromptGateInput{
		Candidate:              candidate,
		SleepAwakeHours:        awake,
		TimingDeviation:        timing,
		RepeatedShortNights:    repeated,
		RecentEquivalentPrompt: recent,
		ExistingCheckinAnswer:  checkinAnswer,
		IllnessConfidence:      illness.Confidence,
	}), nil
}

func (s *DB) contextSleepAwakeHours(ctx context.Context, date string) (float64, error) {
	var awake *float64
	err := s.pool.QueryRow(ctx, `
		SELECT sleep_awake
		  FROM daily_scores
		 WHERE date = $1`, date).Scan(&awake)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	if awake == nil {
		return 0, nil
	}
	return *awake, nil
}

func (s *DB) contextRepeatedShortNights(ctx context.Context, signalDate string, baselineAvg float64) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date, sleep_total
		  FROM daily_scores
		 WHERE date >= $1
		   AND date <= $2
		   AND sleep_total IS NOT NULL
		   AND sleep_total > 0`,
		subtractDays(signalDate, 2), signalDate)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var date string
		var sleep float64
		if err := rows.Scan(&date, &sleep); err != nil {
			return 0, err
		}
		stats, err := s.contextSleepSourceStats(ctx, date)
		if err != nil {
			return 0, err
		}
		day := contextSleepDay{Date: date, SleepHours: sleep, SourceCount: stats.SourceCount, MaxSourceSleep: stats.MaxSourceSleep}
		if contextSleepQualityRejectReason(day, baselineAvg) == "" && sleep <= 6.0 {
			n++
		}
	}
	return n, rows.Err()
}

func (s *DB) contextRecentEquivalentPrompt(ctx context.Context, signalDate string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			  FROM context_prompt_interactions
			 WHERE detected_reason = $1
			   AND signal_date >= $2::date - INTERVAL '14 days'
			   AND signal_date < $2::date
			   AND status IN ('reserved','prompted','answered','skipped','expired','send_failed')
		)`,
		ContextPromptDetectedReasonLowSleep, signalDate).Scan(&exists)
	return exists, err
}

func (s *DB) contextSleepTimingDeviation(ctx context.Context, signalDate string) (bool, error) {
	// Disabled for MVP send decisions. A raw metric_points timing read can
	// mix sources after the candidate detector already filtered source
	// conflicts. Re-enable only with a source-qualified timing extractor
	// that uses the same normalized sleep day/window as low_sleep_v2.
	return false, nil
}

func (s *DB) contextSleepTimingForDate(ctx context.Context, date string) (int, int, bool, error) {
	var minTime, maxTime *string
	err := s.pool.QueryRow(ctx, `
		SELECT MIN(SUBSTRING(date, 12, 5)), MAX(SUBSTRING(date, 12, 5))
		  FROM metric_points
		 WHERE metric_name IN ('sleep_deep','sleep_rem','sleep_core','sleep_unspecified')
		   AND SUBSTRING(date, 1, 10) = $1
		   AND SUBSTRING(date, 12, 8) <> '00:00:00'`, date).Scan(&minTime, &maxTime)
	if err != nil {
		return 0, 0, false, err
	}
	if minTime == nil || maxTime == nil {
		return 0, 0, false, nil
	}
	start, ok := hhmmToMinute(*minTime)
	if !ok {
		return 0, 0, false, nil
	}
	end, ok := hhmmToMinute(*maxTime)
	if !ok {
		return 0, 0, false, nil
	}
	return start, end, true, nil
}

func hhmmToMinute(v string) (int, bool) {
	if len(v) != 5 || v[2] != ':' {
		return 0, false
	}
	for _, idx := range []int{0, 1, 3, 4} {
		if v[idx] < '0' || v[idx] > '9' {
			return 0, false
		}
	}
	h := int(v[0]-'0')*10 + int(v[1]-'0')
	m := int(v[3]-'0')*10 + int(v[4]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func (s *DB) reserveContextPrompt(reason, detectorVersion, signalDate, promptLocalDate string, allowed []string, metadata map[string]any, now, expiresAt time.Time) (*ContextPromptInteraction, bool, error) {
	for _, category := range allowed {
		if err := ValidateContextPromptCategory(category); err != nil {
			return nil, false, err
		}
	}
	promptID, err := newContextPromptID()
	if err != nil {
		return nil, false, err
	}
	allowedJSON, err := json.Marshal(allowed)
	if err != nil {
		return nil, false, err
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := queryCtx()
	defer cancel()
	row := &ContextPromptInteraction{}
	var allowedRaw []byte
	var metadataRaw []byte
	err = s.pool.QueryRow(ctx, `
		INSERT INTO context_prompt_interactions
			(prompt_id, signal_date, prompt_local_date, detected_reason, detector_version, status, expires_at, allowed_categories, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10, $10)
		ON CONFLICT DO NOTHING
		RETURNING prompt_id, signal_date::text, prompt_local_date::text, detected_reason, detector_version, status,
		          COALESCE(category, ''), COALESCE(source, ''), COALESCE(prompt_message_id, 0),
		          COALESCE(prompted_at, '0001-01-01'::timestamptz), expires_at,
		          COALESCE(answered_at, '0001-01-01'::timestamptz), allowed_categories, metadata`,
		promptID, signalDate, promptLocalDate, reason, detectorVersion, ContextPromptStatusReserved, expiresAt, allowedJSON, metadataJSON, now).
		Scan(&row.PromptID, &row.SignalDate, &row.PromptLocalDate, &row.DetectedReason, &row.DetectorVersion, &row.Status,
			&row.Category, &row.Source, &row.PromptMessageID, &row.PromptedAt, &row.ExpiresAt, &row.AnsweredAt, &allowedRaw, &metadataRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := json.Unmarshal(allowedRaw, &row.AllowedCategories); err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(metadataRaw, &row.Metadata); err != nil {
		return nil, false, err
	}
	return row, true, nil
}

func (s *DB) MarkContextPromptSent(promptID string, msgID int64, promptedAt time.Time) error {
	ctx, cancel := queryCtx()
	defer cancel()
	tag, err := s.pool.Exec(ctx, `
		UPDATE context_prompt_interactions
		   SET status = $2, source = $3, prompt_message_id = $4, prompted_at = $5, updated_at = $5
		 WHERE prompt_id = $1
		   AND status = $6`,
		promptID, ContextPromptStatusPrompted, ContextPromptSourceTelegram, msgID, promptedAt, ContextPromptStatusReserved)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("context prompt %s was not reserved", promptID)
	}
	return nil
}

func (s *DB) MarkContextPromptSendFailed(promptID string, failedAt time.Time) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		UPDATE context_prompt_interactions
		   SET status = $2, updated_at = $3
		 WHERE prompt_id = $1
		   AND status = $4`,
		promptID, ContextPromptStatusSendFailed, failedAt, ContextPromptStatusReserved)
	return err
}

func (s *DB) SaveContextPromptAnswer(promptID, category, source string, answeredAt time.Time) (string, error) {
	if err := ValidateContextPromptCategory(category); err != nil {
		return "", err
	}
	ctx, cancel := queryCtx()
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var status string
	var expiresAt time.Time
	var allowedRaw []byte
	if err := tx.QueryRow(ctx, `
		SELECT status, expires_at, allowed_categories
		  FROM context_prompt_interactions
		 WHERE prompt_id = $1
		 FOR UPDATE`, promptID).Scan(&status, &expiresAt, &allowedRaw); err != nil {
		return "", err
	}
	if status == ContextPromptStatusAnswered || status == ContextPromptStatusSkipped || status == ContextPromptStatusExpired {
		return status, tx.Commit(ctx)
	}
	if !contextPromptCanAcceptAnswer(status) {
		return status, tx.Commit(ctx)
	}
	if !answeredAt.Before(expiresAt) {
		if _, err := tx.Exec(ctx, `
			UPDATE context_prompt_interactions
			   SET status = $2, updated_at = $3
			 WHERE prompt_id = $1`,
			promptID, ContextPromptStatusExpired, answeredAt); err != nil {
			return "", err
		}
		return ContextPromptStatusExpired, tx.Commit(ctx)
	}
	var allowed []string
	if err := json.Unmarshal(allowedRaw, &allowed); err != nil {
		return "", err
	}
	if !stringInSlice(category, allowed) {
		return "", fmt.Errorf("category %q not allowed for prompt %s", category, promptID)
	}
	nextStatus := ContextPromptStatusAnswered
	if category == ContextPromptCategoryUnknown || category == ContextPromptCategorySkip {
		nextStatus = ContextPromptStatusSkipped
	}
	if _, err := tx.Exec(ctx, `
		UPDATE context_prompt_interactions
		   SET status = $2, category = $3, source = $4, answered_at = $5, updated_at = $5
		 WHERE prompt_id = $1`,
		promptID, nextStatus, category, source, answeredAt); err != nil {
		return "", err
	}
	return nextStatus, tx.Commit(ctx)
}

func contextPromptCanAcceptAnswer(status string) bool {
	return status == ContextPromptStatusPrompted || status == ContextPromptStatusReserved
}

func (s *DB) GetContextAnnotationsForDate(signalDate string) ([]ContextAnnotation, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT signal_date::text, detected_reason, category, metadata
		  FROM context_prompt_interactions
		 WHERE signal_date = $1
		   AND status = $2
		   AND category IS NOT NULL
		 ORDER BY answered_at DESC`, signalDate, ContextPromptStatusAnswered)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContextAnnotation
	for rows.Next() {
		var a ContextAnnotation
		var metadataRaw []byte
		if err := rows.Scan(&a.Date, &a.DetectedReason, &a.Category, &metadataRaw); err != nil {
			return nil, err
		}
		var metadata map[string]any
		_ = json.Unmarshal(metadataRaw, &metadata)
		a.SleepHours = floatFromMetadata(metadata, "sleep_hours")
		a.BaselineAvg = floatFromMetadata(metadata, "baseline_avg")
		a.ZScore = floatFromMetadata(metadata, "z_score")
		out = append(out, a)
	}
	return out, rows.Err()
}

func newContextPromptID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "cp_" + hex.EncodeToString(b[:]), nil
}

func stringInSlice(v string, vals []string) bool {
	for _, x := range vals {
		if v == x {
			return true
		}
	}
	return false
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func floatFromMetadata(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}
