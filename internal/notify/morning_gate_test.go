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
		{"wake not ready before cap", t0, false, false, "", false, true, MorningActionWait},
		{"wake ready, no check-in", t0, true, false, "", false, true, MorningActionSendReport},
		{"wake ready, unanswered check-in", t0, true, true, storage.CheckinStatusPrompted, false, true, MorningActionSendReport},
		{"wake ready, answered check-in", t0, true, true, storage.CheckinStatusAnswered, false, true, MorningActionSendReport},
		{"past cap, wake not ready", t0.Add(3 * time.Hour), false, true, storage.CheckinStatusPrompted, false, true, MorningActionForce},
		{"past cap, answered check-in", t0.Add(3 * time.Hour), true, true, storage.CheckinStatusAnswered, false, true, MorningActionForce},
		{"already sent", t0, true, true, storage.CheckinStatusAnswered, true, true, MorningActionNoop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideMorningAction(MorningGateInputs{
				Now:               tc.now,
				Cap:               cap,
				WakeReady:         tc.sleepSettled,
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

func TestCheckinWindows(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	prompted := time.Date(2026, 10, 1, 14, 0, 0, 0, loc)
	row := &storage.CheckinRow{Date: "2026-10-01", Status: storage.CheckinStatusPrompted, PromptedAt: prompted, ExpiresAt: prompted.Add(9 * time.Minute)}
	if got := CheckinPromptExpiry(prompted); !got.Equal(prompted.Add(2 * time.Hour)) {
		t.Fatalf("expiry=%s", got)
	}
	if !CheckinPending(row, prompted.Add(90*time.Minute)) || CheckinPending(row, prompted.Add(2*time.Hour)) {
		t.Fatal("pending should follow the original two-hour window")
	}
	row.Status = storage.CheckinStatusExpired
	if !CheckinPending(row, prompted.Add(time.Hour)) {
		t.Fatal("expired unanswered row should still suppress ancillary prompts")
	}
	row.Status = storage.CheckinStatusPrompted
	row.PromptedAt = time.Date(2026, 9, 30, 19, 30, 0, 0, loc)
	if CheckinReminderDue(row, time.Date(2026, 10, 1, 1, 0, 0, 0, loc), loc) {
		t.Fatal("prompt from a previous local day must not qualify")
	}
	row.PromptedAt = prompted
	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before hour", prompted.Add(time.Hour - time.Second), false},
		{"at hour", prompted.Add(time.Hour), true},
		{"four hours", prompted.Add(4 * time.Hour), true},
		{"after four hours", prompted.Add(4*time.Hour + time.Second), false},
		{"after 20", time.Date(2026, 10, 1, 20, 0, 0, 0, loc), false},
		{"next day", time.Date(2026, 10, 2, 8, 0, 0, 0, loc), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckinReminderDue(row, tc.at, loc); got != tc.want {
				t.Fatalf("due=%v, want %v", got, tc.want)
			}
		})
	}
	row.AnsweredAt = prompted.Add(30 * time.Minute)
	if CheckinReminderDue(row, prompted.Add(90*time.Minute), loc) || CheckinPending(row, prompted.Add(90*time.Minute)) {
		t.Fatal("answered row must stop reminders and suppression")
	}
}
