package notify

import (
	"time"

	"health-receiver/internal/storage"
)

// EffectiveMorningCap honours an outstanding prompt's stored ExpiresAt
// over a freshly-computed MorningCapTime. Without this preference an
// ingest-driven prompt sent before morning_hour saves expires_at at
// the original (unfloored) cap; a later scheduler tick recomputes
// cap, MorningCapTime's floor pushes it to now+MinPromptWindow, and
// the gate sees the prompted row as "before cap" instead of expiring
// it on schedule. Callers pass the row that GetTodayCheckin returned
// — nil or non-prompted rows fall through to the freshly-computed
// cap unchanged. Pinned by TestEffectiveMorningCap.
func EffectiveMorningCap(computed time.Time, row *storage.CheckinRow) time.Time {
	if row == nil || row.Status != storage.CheckinStatusPrompted {
		return computed
	}
	if row.ExpiresAt.IsZero() {
		return computed
	}
	return row.ExpiresAt
}

// MorningAction enumerates the scheduler decisions for one tick of
// runMorningSmartRetry.
type MorningAction string

const (
	MorningActionNoop           MorningAction = "noop"             // report already sent today
	MorningActionWait           MorningAction = "wait"             // try again next tick
	MorningActionPrompt         MorningAction = "prompt"           // send check-in prompt (first time)
	MorningActionSendReport     MorningAction = "send_report"      // user answered, send report now
	MorningActionExpireAndForce MorningAction = "expire_and_force" // cap reached, mark expired, force-send
	MorningActionForce          MorningAction = "force"            // cap reached, no check-in row, force-send
)

// MorningGateInputs carries the per-tick state DecideMorningAction
// needs. Kept as a struct so adding a future signal doesn't churn
// every call site.
type MorningGateInputs struct {
	Now               time.Time
	Cap               time.Time
	SleepSettled      bool
	HasCheckin        bool
	CheckinStatus     string
	ReportAlreadySent bool

	// CheckinEnabled is the feature-flag input. False = the webhook is
	// not registered (no TELEGRAM_WEBHOOK_SECRET in env), so the user
	// cannot answer a prompt. In that case the gate must bypass the
	// prompt+wait path entirely and behave exactly like the pre-PR
	// scheduler: try SendMorningSmart, force-send at cap. Otherwise we
	// would prompt every morning and never get an answer, blocking the
	// report until cap on every day.
	CheckinEnabled bool
}

// DecideMorningAction is the pure decision table. Lives separately
// from the scheduler loop so the policy stays auditable and tests
// don't need a fake scheduler.
//
// Policy:
//   - report already sent → noop
//   - past cap:
//       - prompt is still in `prompted` state → expire + force
//       - prompt was answered (even late) → send report normally
//       - no prompt row → force without expire
//   - before cap:
//       - sleep not settled → wait (don't prompt yet)
//       - no checkin yet → prompt
//       - checkin answered or late_answered → send report
//       - checkin still in `prompted` → wait
func DecideMorningAction(in MorningGateInputs) MorningAction {
	if in.ReportAlreadySent {
		return MorningActionNoop
	}
	past := !in.Now.Before(in.Cap)

	// Feature-flag bypass: when check-in is disabled we mirror the
	// pre-PR scheduler exactly — past cap → force-send, before cap →
	// SendReport (which itself defers when sleep isn't settled).
	if !in.CheckinEnabled {
		if past {
			return MorningActionForce
		}
		return MorningActionSendReport
	}

	if past {
		// At/after cap. The check-in answer state determines whether
		// we send normally or force, and whether expire fires first.
		if in.HasCheckin {
			switch in.CheckinStatus {
			case storage.CheckinStatusAnswered, storage.CheckinStatusLateAnswered:
				return MorningActionSendReport
			case storage.CheckinStatusPrompted:
				return MorningActionExpireAndForce
			}
		}
		return MorningActionForce
	}

	// Before cap.
	if !in.SleepSettled {
		return MorningActionWait
	}
	if !in.HasCheckin {
		return MorningActionPrompt
	}
	if in.CheckinStatus == storage.CheckinStatusAnswered ||
		in.CheckinStatus == storage.CheckinStatusLateAnswered {
		return MorningActionSendReport
	}
	// prompted, before cap → keep waiting.
	return MorningActionWait
}
