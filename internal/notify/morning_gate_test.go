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
		want              MorningAction
	}{
		{"sleep not settled before cap", t0, false, false, "", false, MorningActionWait},
		{"settled, no prompt yet", t0, true, false, "", false, MorningActionPrompt},
		{"prompt sent, answered → send report", t0, true, true, storage.CheckinStatusAnswered, false, MorningActionSendReport},
		{"prompt sent, late_answered → send report", t0, true, true, storage.CheckinStatusLateAnswered, false, MorningActionSendReport},
		{"prompt sent, not answered, before cap → wait", t0, true, true, storage.CheckinStatusPrompted, false, MorningActionWait},
		{"past cap, prompt unanswered → expire+force", t0.Add(3 * time.Hour), true, true, storage.CheckinStatusPrompted, false, MorningActionExpireAndForce},
		{"past cap, no prompt ever → force without expire", t0.Add(3 * time.Hour), true, false, "", false, MorningActionForce},
		{"past cap, sleep never settled, no prompt → force", t0.Add(3 * time.Hour), false, false, "", false, MorningActionForce},
		{"past cap, already answered → send report", t0.Add(3 * time.Hour), true, true, storage.CheckinStatusAnswered, false, MorningActionSendReport},
		{"already sent → noop", t0, true, true, storage.CheckinStatusAnswered, true, MorningActionNoop},
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
			})
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
