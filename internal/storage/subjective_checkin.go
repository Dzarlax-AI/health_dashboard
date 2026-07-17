package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// Status enum for subjective_checkins.status. Stored as plain TEXT in
// Postgres; values validated at the Go layer. Adding a new state is a
// Go-only change — no migration needed.
const (
	CheckinStatusPrompted     = "prompted"
	CheckinStatusAnswered     = "answered"
	CheckinStatusExpired      = "expired"
	CheckinStatusLateAnswered = "late_answered"
)

// Answer enum for subjective_checkins.answer. NULL in DB until the
// status transitions out of `prompted`.
const (
	CheckinAnswerGreat = "great"
	CheckinAnswerOK    = "ok"
	CheckinAnswerMeh   = "meh"
	CheckinAnswerSick  = "sick"
)

// Source enum for subjective_checkins.source. MVP only emits telegram;
// future dashboard answers will use a separate source so coverage
// analytics can tell them apart per entry point.
const CheckinSourceTelegram = "telegram"

const SettingSubjectiveCheckinEnabledSince = "subjective_checkin_enabled_since"

// subjectiveCheckinTableDDL returns the CREATE TABLE statement for
// subjective_checkins. Exposed package-internal so the structural test
// can assert column shape without a live DB.
//
// Schema rationale:
//   - `date TEXT` matches every other dated table in this schema
//     (daily_scores, energy_snapshots, target_snapshots). The writer
//     formats it in tenant REPORT_TZ before INSERT.
//   - PK (date, source) so multiple entry points (Telegram now,
//     dashboard later) can co-exist without overwriting each other's
//     coverage signal.
//   - status: prompted | answered | expired | late_answered. Closed
//     enum validated at the Go layer.
//   - answer: NULL until status moves out of `prompted`. One of
//     {great, ok, meh, sick} when set.
//   - metadata JSONB: open-ended box for callback nonces, retry
//     counts, future analytics tags. Starts empty.
func subjectiveCheckinTableDDL() string {
	return `CREATE TABLE IF NOT EXISTS subjective_checkins (
		date              TEXT NOT NULL,
		source            TEXT NOT NULL,
		status            TEXT NOT NULL,
		answer            TEXT,
		prompt_message_id BIGINT,
		prompted_at       TIMESTAMPTZ NOT NULL,
		answered_at       TIMESTAMPTZ,
		expires_at        TIMESTAMPTZ NOT NULL,
		metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
		PRIMARY KEY (date, source)
	)`
}

// EnsureSubjectiveCheckinsTable creates the subjective_checkins table.
// Idempotent — safe to call on every startup.
func (s *DB) EnsureSubjectiveCheckinsTable() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.EnsureSubjectiveCheckinsTableContext(ctx); err != nil {
		log.Printf("EnsureSubjectiveCheckinsTable: %v", err)
	}
}

func (s *DB) EnsureSubjectiveCheckinsTableContext(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, subjectiveCheckinTableDDL())
	return err
}

// ValidateCheckinAnswer reports whether ans is one of the four allowed
// strings. Error carries the rejected value so callers can surface it
// in callback acks / logs without re-formatting.
func ValidateCheckinAnswer(ans string) error {
	switch ans {
	case CheckinAnswerGreat, CheckinAnswerOK, CheckinAnswerMeh, CheckinAnswerSick:
		return nil
	}
	return fmt.Errorf("invalid checkin answer %q", ans)
}

// nextCheckinStatus computes the target status for a row given its
// current state, the action applied, the row's expires_at, and the
// current wall clock. Pure function — exercised by tests without a DB
// so the policy stays explicit and trivially auditable.
//
// Actions:
//   - "tap"    — user pressed an inline-keyboard button. Before
//     expires_at → answered (primary validation pool).
//     At/after expires_at → late_answered (analytics-only).
//     Already answered → idempotent re-tap, stay answered.
//   - "expire" — scheduler decided the cap has passed. Only valid
//     from prompted, and only when expires_at is reached.
//
// Returns ("", err) when the transition is not valid (e.g. expire
// called before cap).
func nextCheckinStatus(current, action string, expiresAt, now time.Time) (string, error) {
	switch action {
	case "tap":
		if current == CheckinStatusAnswered {
			return CheckinStatusAnswered, nil
		}
		if now.Before(expiresAt) && current == CheckinStatusPrompted {
			return CheckinStatusAnswered, nil
		}
		// Anything else (expired, prompted-past-cap, late_answered) → late_answered.
		return CheckinStatusLateAnswered, nil
	case "expire":
		if current != CheckinStatusPrompted {
			return "", errors.New("can only expire a prompted row")
		}
		if now.Before(expiresAt) {
			return "", errors.New("expires_at not reached yet")
		}
		return CheckinStatusExpired, nil
	}
	return "", fmt.Errorf("unknown action %q", action)
}

// CheckinRow is the read-shape of one subjective_checkins row.
type CheckinRow struct {
	Date            string
	Source          string
	Status          string
	Answer          string // "" when NULL
	PromptMessageID int64  // 0 when NULL
	PromptedAt      time.Time
	AnsweredAt      time.Time // zero when NULL
	ExpiresAt       time.Time
}

// CheckinCoverage is the admin read model for the morning check-in
// coverage table. Rows/Summary keep the latest actual subjective_checkins
// history. SLARows/SLASummary synthesize calendar-day rows only after an
// explicit enabled-since date is configured, avoiding false pre-rollout
// missing days.
type CheckinCoverage struct {
	From         string                 `json:"from"`
	To           string                 `json:"to"`
	Days         int                    `json:"days"`
	Source       string                 `json:"source"`
	EnabledSince string                 `json:"enabled_since,omitempty"`
	SLAActive    bool                   `json:"sla_active"`
	SLASummary   CheckinCoverageSummary `json:"sla_summary"`
	SLARows      []CheckinCoverageRow   `json:"sla_rows"`
	Summary      CheckinCoverageSummary `json:"summary"`
	Rows         []CheckinCoverageRow   `json:"rows"`
}

type CheckinCoverageSummary struct {
	TotalDays               int            `json:"total_days"`
	Prompted                int            `json:"prompted"`
	Answered                int            `json:"answered"`
	LateAnswered            int            `json:"late_answered"`
	Expired                 int            `json:"expired"`
	Missing                 int            `json:"missing"`
	Pending                 int            `json:"pending"`
	AnswerCounts            map[string]int `json:"answer_counts"`
	AverageResponseSeconds  *int64         `json:"average_response_seconds,omitempty"`
	AnsweredCoveragePercent int            `json:"answered_coverage_percent"`
	PromptedCoveragePercent int            `json:"prompted_coverage_percent"`
}

type CheckinCoverageRow struct {
	Date                   string     `json:"date"`
	Source                 string     `json:"source"`
	Status                 string     `json:"status"`
	Answer                 string     `json:"answer,omitempty"`
	PromptedAt             *time.Time `json:"prompted_at,omitempty"`
	AnsweredAt             *time.Time `json:"answered_at,omitempty"`
	ExpiresAt              *time.Time `json:"expires_at,omitempty"`
	ResponseLatencySeconds *int64     `json:"response_latency_seconds,omitempty"`
}

const CheckinStatusMissing = "missing"
const CheckinStatusPending = "pending"

func ValidateCheckinEnabledSince(date string) error {
	if date == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("enabled_since must be YYYY-MM-DD: %w", err)
	}
	return nil
}

func (s *DB) GetCheckinEnabledSince() string {
	return s.GetSetting(SettingSubjectiveCheckinEnabledSince, "")
}

func (s *DB) SaveCheckinEnabledSince(date string) error {
	if err := ValidateCheckinEnabledSince(date); err != nil {
		return err
	}
	return s.SaveSettings(map[string]string{SettingSubjectiveCheckinEnabledSince: date})
}

// SaveCheckinPrompted upserts a `prompted` row for (date, source).
// Idempotent on retry: a second prompt for the same date overwrites
// prompt_message_id / prompted_at / expires_at without erasing an
// existing answer (the WHERE-on-status guards that).
func (s *DB) SaveCheckinPrompted(date, source string, msgID int64, promptedAt, expiresAt time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO subjective_checkins
			(date, source, status, prompt_message_id, prompted_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (date, source) DO UPDATE SET
			prompt_message_id = EXCLUDED.prompt_message_id,
			prompted_at       = EXCLUDED.prompted_at,
			expires_at        = EXCLUDED.expires_at
		WHERE subjective_checkins.status = $3`,
		date, source, CheckinStatusPrompted, msgID, promptedAt, expiresAt)
	return err
}

// SaveCheckinAnswer transitions a row based on the pure helper's
// decision. Reads current status + expires_at inside a transaction so
// a racing scheduler-expire cannot clobber a real answer. Returns the
// resulting status (answered | late_answered).
func (s *DB) SaveCheckinAnswer(date, source, answer string, answeredAt time.Time) (string, error) {
	if err := ValidateCheckinAnswer(answer); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var status string
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT status, expires_at FROM subjective_checkins
		WHERE date = $1 AND source = $2 FOR UPDATE`, date, source).Scan(&status, &expiresAt); err != nil {
		return "", err
	}
	nextStatus, err := nextCheckinStatus(status, "tap", expiresAt, answeredAt)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE subjective_checkins
		   SET status = $3, answer = $4, answered_at = $5
		 WHERE date = $1 AND source = $2`,
		date, source, nextStatus, answer, answeredAt); err != nil {
		return "", err
	}
	return nextStatus, tx.Commit(ctx)
}

// ExpireCheckin transitions a `prompted` row to `expired` when cap has
// passed. No-op (returns "", nil) if the row is already answered /
// late_answered / expired — caller can ignore the return.
//
// Implementation: a single conditional UPDATE with the policy encoded
// in WHERE. Postgres takes a row-level lock for the duration of the
// statement, so a racing SaveCheckinAnswer (which itself runs FOR
// UPDATE inside a tx) cannot interleave: whichever statement acquires
// the lock first commits its transition, the other sees the new
// status and its WHERE clause filters the row out.
//
// Encoded policy (mirrors nextCheckinStatus's "expire" branch):
//   - status must still be `prompted`
//   - expires_at must have been reached
//
// nextCheckinStatus stays as the canonical Go-side policy doc and is
// exercised by TestNextCheckinStatus; ExpireCheckin re-encodes the
// same rules in SQL for atomicity. Both must change together.
func (s *DB) ExpireCheckin(date, source string, now time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tag, err := s.pool.Exec(ctx, `
		UPDATE subjective_checkins
		   SET status = $3
		 WHERE date = $1 AND source = $2
		   AND status = $4
		   AND expires_at <= $5`,
		date, source, CheckinStatusExpired, CheckinStatusPrompted, now)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", nil
	}
	return CheckinStatusExpired, nil
}

// GetTodayCheckin returns the row for (date, source). Returns
// (nil, nil) when no row exists — `pgx.ErrNoRows` is treated as a
// non-error so callers can render the "no checkin yet" path uniformly.
func (s *DB) GetTodayCheckin(date, source string) (*CheckinRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row := &CheckinRow{Date: date, Source: source}
	var answer *string
	var msgID *int64
	var answeredAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT status, answer, prompt_message_id, prompted_at, answered_at, expires_at
		  FROM subjective_checkins
		 WHERE date = $1 AND source = $2`, date, source).
		Scan(&row.Status, &answer, &msgID, &row.PromptedAt, &answeredAt, &row.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if answer != nil {
		row.Answer = *answer
	}
	if msgID != nil {
		row.PromptMessageID = *msgID
	}
	if answeredAt != nil {
		row.AnsweredAt = *answeredAt
	}
	return row, nil
}

// GetCheckinCoverage returns the latest N check-in rows up to today.
// `today` must be YYYY-MM-DD in the tenant's REPORT_TZ; callers own
// timezone resolution so this storage method stays pure SQL + date
// arithmetic.
func (s *DB) GetCheckinCoverage(today, source string, days int) (*CheckinCoverage, error) {
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	if _, err := time.Parse("2006-01-02", today); err != nil {
		return nil, fmt.Errorf("today must be YYYY-MM-DD: %w", err)
	}
	enabledSince := s.GetCheckinEnabledSince()
	if err := ValidateCheckinEnabledSince(enabledSince); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, source, status, answer, prompt_message_id, prompted_at, answered_at, expires_at
		  FROM subjective_checkins
		 WHERE source = $1
		   AND date <= $2
		 ORDER BY date DESC
		 LIMIT $3`, source, today, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries, err := scanCheckinRows(rows)
	if err != nil {
		return nil, err
	}
	coverage, err := buildCheckinCoverage(today, source, days, entries)
	if err != nil {
		return nil, err
	}
	coverage.EnabledSince = enabledSince
	if enabledSince == "" {
		return coverage, nil
	}

	from := calendarCoverageStart(today, enabledSince, days)
	calendarEntries, err := s.getCheckinRowsBetween(ctx, source, from, today)
	if err != nil {
		return nil, err
	}
	return buildCheckinCoverageWithSLA(coverage, today, from, source, enabledSince, calendarEntries)
}

func (s *DB) getCheckinRowsBetween(ctx context.Context, source, from, to string) ([]CheckinRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date, source, status, answer, prompt_message_id, prompted_at, answered_at, expires_at
		  FROM subjective_checkins
		 WHERE source = $1
		   AND date >= $2
		   AND date <= $3
		 ORDER BY date DESC`, source, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCheckinRows(rows)
}

func scanCheckinRows(rows pgx.Rows) ([]CheckinRow, error) {
	var entries []CheckinRow
	for rows.Next() {
		row := CheckinRow{}
		var answer *string
		var msgID *int64
		var answeredAt *time.Time
		if err := rows.Scan(&row.Date, &row.Source, &row.Status, &answer, &msgID, &row.PromptedAt, &answeredAt, &row.ExpiresAt); err != nil {
			return nil, err
		}
		if answer != nil {
			row.Answer = *answer
		}
		if msgID != nil {
			row.PromptMessageID = *msgID
		}
		if answeredAt != nil {
			row.AnsweredAt = *answeredAt
		}
		entries = append(entries, row)
	}
	return entries, rows.Err()
}

func buildCheckinCoverage(today, source string, days int, entries []CheckinRow) (*CheckinCoverage, error) {
	if days < 1 {
		days = 1
	}
	if _, err := time.Parse("2006-01-02", today); err != nil {
		return nil, fmt.Errorf("today must be YYYY-MM-DD: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Date > entries[j].Date })
	if len(entries) > days {
		entries = entries[:days]
	}

	out := &CheckinCoverage{
		From:   "",
		To:     today,
		Days:   days,
		Source: source,
		Summary: CheckinCoverageSummary{
			TotalDays:    len(entries),
			AnswerCounts: map[string]int{},
		},
		Rows: make([]CheckinCoverageRow, 0, len(entries)),
	}
	if len(entries) > 0 {
		out.From = entries[len(entries)-1].Date
		out.To = entries[0].Date
	}

	var latencySum int64
	var latencyN int64
	promptedDays := 0
	for _, entry := range entries {
		row := checkinCoverageRow(entry)
		if row.ResponseLatencySeconds != nil {
			latencySum += *row.ResponseLatencySeconds
			latencyN++
		}
		switch entry.Status {
		case CheckinStatusPrompted:
			out.Summary.Prompted++
		case CheckinStatusAnswered:
			out.Summary.Answered++
		case CheckinStatusLateAnswered:
			out.Summary.LateAnswered++
		case CheckinStatusExpired:
			out.Summary.Expired++
		default:
			// Preserve unknown statuses in rows without folding them into a
			// known bucket. Prompted coverage below still counts the row.
		}
		promptedDays++
		if entry.Answer != "" {
			out.Summary.AnswerCounts[entry.Answer]++
		}
		out.Rows = append(out.Rows, row)
	}
	if latencyN > 0 {
		avg := latencySum / latencyN
		out.Summary.AverageResponseSeconds = &avg
	}
	out.Summary.AnsweredCoveragePercent = percent(out.Summary.Answered, len(entries))
	out.Summary.PromptedCoveragePercent = percent(promptedDays, len(entries))
	return out, nil
}

func calendarCoverageStart(today, enabledSince string, days int) string {
	t, err := time.Parse("2006-01-02", today)
	if err != nil {
		return enabledSince
	}
	windowStart := t.AddDate(0, 0, -days+1).Format("2006-01-02")
	if enabledSince > windowStart {
		return enabledSince
	}
	return windowStart
}

func buildCheckinCoverageWithSLA(base *CheckinCoverage, today, from, source, enabledSince string, entries []CheckinRow) (*CheckinCoverage, error) {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, fmt.Errorf("from must be YYYY-MM-DD: %w", err)
	}
	end, err := time.Parse("2006-01-02", today)
	if err != nil {
		return nil, fmt.Errorf("today must be YYYY-MM-DD: %w", err)
	}
	if end.Before(start) {
		base.SLAActive = true
		base.EnabledSince = enabledSince
		base.SLASummary = CheckinCoverageSummary{AnswerCounts: map[string]int{}}
		base.SLARows = []CheckinCoverageRow{}
		return base, nil
	}

	byDate := map[string]CheckinRow{}
	for _, entry := range entries {
		byDate[entry.Date] = entry
	}
	rows := make([]CheckinCoverageRow, 0, int(end.Sub(start).Hours()/24)+1)
	summary := CheckinCoverageSummary{AnswerCounts: map[string]int{}}
	var latencySum int64
	var latencyN int64
	promptedDays := 0
	for d := end; !d.Before(start); d = d.AddDate(0, 0, -1) {
		date := d.Format("2006-01-02")
		entry, ok := byDate[date]
		if !ok {
			status := CheckinStatusMissing
			if date == today {
				status = CheckinStatusPending
				summary.Pending++
			} else {
				summary.Missing++
			}
			rows = append(rows, CheckinCoverageRow{Date: date, Source: source, Status: status})
			continue
		}
		row := checkinCoverageRow(entry)
		rows = append(rows, row)
		promptedDays++
		switch entry.Status {
		case CheckinStatusPrompted:
			summary.Prompted++
		case CheckinStatusAnswered:
			summary.Answered++
		case CheckinStatusLateAnswered:
			summary.LateAnswered++
		case CheckinStatusExpired:
			summary.Expired++
		}
		if entry.Answer != "" {
			summary.AnswerCounts[entry.Answer]++
		}
		if row.ResponseLatencySeconds != nil {
			latencySum += *row.ResponseLatencySeconds
			latencyN++
		}
	}
	summary.TotalDays = len(rows)
	if latencyN > 0 {
		avg := latencySum / latencyN
		summary.AverageResponseSeconds = &avg
	}
	completedDays := summary.TotalDays - summary.Pending
	summary.AnsweredCoveragePercent = percent(summary.Answered, completedDays)
	summary.PromptedCoveragePercent = percent(promptedDays, completedDays)

	base.SLAActive = true
	base.EnabledSince = enabledSince
	base.SLASummary = summary
	base.SLARows = rows
	return base, nil
}

func checkinCoverageRow(entry CheckinRow) CheckinCoverageRow {
	promptedAt := entry.PromptedAt
	expiresAt := entry.ExpiresAt
	row := CheckinCoverageRow{
		Date:       entry.Date,
		Source:     entry.Source,
		Status:     entry.Status,
		Answer:     entry.Answer,
		PromptedAt: &promptedAt,
		ExpiresAt:  &expiresAt,
	}
	if !entry.AnsweredAt.IsZero() {
		answeredAt := entry.AnsweredAt
		row.AnsweredAt = &answeredAt
		latency := int64(answeredAt.Sub(promptedAt).Seconds())
		if latency < 0 {
			latency = 0
		}
		row.ResponseLatencySeconds = &latency
	}
	return row
}

func percent(n, total int) int {
	if total <= 0 {
		return 0
	}
	return int(math.Round(float64(n) * 100 / float64(total)))
}
