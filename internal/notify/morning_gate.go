package notify

import (
	"time"

	"health-receiver/internal/storage"
)

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
