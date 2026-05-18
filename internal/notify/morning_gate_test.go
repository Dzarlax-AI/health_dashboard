package notify

import (
	"testing"
	"time"

	"health-receiver/internal/storage"
)

func TestMorningGate(t *testing.T) {
	t0 := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	cap := time.Date(2026, 5, 18, 11, 0, 0, 0, time.UTC)
	cases := []struct {
		name              string
		now               time.Time
		sleepSettled      bool
		hasCheckin        bool
		checkinStatus     string
		reportAlreadySent bool
		checkinEnabled    bool
		want              MorningAction
	}{
		// Check-in enabled — full gate.
		{"sleep not settled before cap", t0, false, false, "", false, true, MorningActionWait},
		{"settled, no prompt yet", t0, true, false, "", false, true, MorningActionPrompt},
		{"prompt sent, answered → send report", t0, true, true, storage.CheckinStatusAnswered, false, true, MorningActionSendReport},
		{"prompt sent, late_answered → send report", t0, true, true, storage.CheckinStatusLateAnswered, false, true, MorningActionSendReport},
		{"prompt sent, not answered, before cap → wait", t0, true, true, storage.CheckinStatusPrompted, false, true, MorningActionWait},
		{"past cap, prompt unanswered → expire+force", t0.Add(3 * time.Hour), true, true, storage.CheckinStatusPrompted, false, true, MorningActionExpireAndForce},
		{"past cap, no prompt ever → force without expire", t0.Add(3 * time.Hour), true, false, "", false, true, MorningActionForce},
		{"past cap, sleep never settled, no prompt → force", t0.Add(3 * time.Hour), false, false, "", false, true, MorningActionForce},
		{"past cap, already answered → send report", t0.Add(3 * time.Hour), true, true, storage.CheckinStatusAnswered, false, true, MorningActionSendReport},
		{"already sent → noop", t0, true, true, storage.CheckinStatusAnswered, true, true, MorningActionNoop},

		// Check-in DISABLED (TELEGRAM_WEBHOOK_SECRET unset) — gate must
		// bypass the prompt path entirely so the report still goes out.
		// Behaviour mirrors the pre-PR scheduler: past cap → force,
		// otherwise SendReport (which defers internally if not settled).
		{"disabled: before cap, settled → SendReport", t0, true, false, "", false, false, MorningActionSendReport},
		{"disabled: before cap, NOT settled → SendReport (defers inside)", t0, false, false, "", false, false, MorningActionSendReport},
		{"disabled: past cap → Force", t0.Add(3 * time.Hour), true, false, "", false, false, MorningActionForce},
		{"disabled: already sent → noop", t0, true, false, "", true, false, MorningActionNoop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideMorningAction(MorningGateInputs{
				Now:               tc.now,
				Cap:               cap,
				SleepSettled:      tc.sleepSettled,
				HasCheckin:        tc.hasCheckin,
				CheckinStatus:     tc.checkinStatus,
				ReportAlreadySent: tc.reportAlreadySent,
				CheckinEnabled:    tc.checkinEnabled,
			})
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
