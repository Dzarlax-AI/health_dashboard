package notify

import (
	"time"

	"health-receiver/internal/storage"
)

// CheckinPromptExpiry is the primary answer-window boundary for every new
// prompt. The report scheduler has its own deadline and cannot change this.
func CheckinPromptExpiry(now time.Time) time.Time { return now.Add(2 * time.Hour) }

// CheckinPending reports whether an unanswered prompt is still inside its
// original two-hour window. Expired legacy rows suppress ancillary prompts
// only until this window ends.
func CheckinPending(row *storage.CheckinRow, now time.Time) bool {
	if row == nil || row.PromptedAt.IsZero() || now.Before(row.PromptedAt) || !now.Before(CheckinPromptExpiry(row.PromptedAt)) {
		return false
	}
	if !row.AnsweredAt.IsZero() || row.Answer != "" {
		return false
	}
	return row.Status == storage.CheckinStatusPrompted || row.Status == storage.CheckinStatusExpired
}

// CheckinReminderDue selects the one reminder window: at least 60 minutes,
// no more than four hours after the original prompt, on that same local day,
// and before 20:00. Expired rows remain eligible when they are unanswered.
func CheckinReminderDue(row *storage.CheckinRow, now time.Time, loc *time.Location) bool {
	if row == nil || row.PromptedAt.IsZero() || loc == nil || row.Date == "" ||
		!row.AnsweredAt.IsZero() || row.Answer != "" ||
		(row.Status != storage.CheckinStatusPrompted && row.Status != storage.CheckinStatusExpired) {
		return false
	}
	localNow := now.In(loc)
	if row.Date != localNow.Format("2006-01-02") || row.PromptedAt.In(loc).Format("2006-01-02") != row.Date || localNow.Hour() >= 20 {
		return false
	}
	elapsed := now.Sub(row.PromptedAt)
	return elapsed >= time.Hour && elapsed <= 4*time.Hour
}

// MorningAction enumerates the report scheduler decisions for one tick.
type MorningAction string

const (
	MorningActionNoop       MorningAction = "noop"        // report already sent today
	MorningActionWait       MorningAction = "wait"        // try again next tick
	MorningActionSendReport MorningAction = "send_report" // wake data ready, send report
	MorningActionForce      MorningAction = "force"       // cap reached, force-send report
)

// MorningGateInputs carries the per-tick state DecideMorningAction
// needs. Kept as a struct so adding a future signal doesn't churn
// every call site.
type MorningGateInputs struct {
	Now               time.Time
	Cap               time.Time
	WakeReady         bool
	HasCheckin        bool   // deprecated, ignored; retained for call-site compatibility
	CheckinStatus     string // deprecated, ignored; retained for call-site compatibility
	ReportAlreadySent bool

	CheckinEnabled bool // deprecated, ignored; retained for call-site compatibility
}

// DecideMorningAction keeps report delivery independent from check-in.
// The check-in-related fields remain in MorningGateInputs for source
// compatibility with scheduler callers during integration, but do not
// affect this decision.
func DecideMorningAction(in MorningGateInputs) MorningAction {
	if in.ReportAlreadySent {
		return MorningActionNoop
	}
	if !in.Cap.IsZero() && !in.Now.Before(in.Cap) {
		return MorningActionForce
	}
	if !in.WakeReady {
		return MorningActionWait
	}
	return MorningActionSendReport
}
