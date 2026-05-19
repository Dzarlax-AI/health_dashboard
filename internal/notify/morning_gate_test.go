package notify

import (
	"testing"
	"time"

	"health-receiver/internal/storage"
)

// TestEffectiveMorningCap pins the Codex P2 fix: when a prompted row
// already exists with a stored ExpiresAt, the gate must use THAT
// deadline (the contract with the user when the prompt was sent)
// instead of the freshly-recomputed MorningCapTime — otherwise an
// ingest-saved prompt at 07:30 (expires_at=08:30) would have its
// deadline silently floored forward to now+60min on every later
// scheduler tick, indefinitely deferring the morning report.
func TestEffectiveMorningCap(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	computed := time.Date(2026, 5, 19, 11, 0, 0, 0, loc)
	rowExpires := time.Date(2026, 5, 19, 8, 30, 0, 0, loc)

	t.Run("nil row → computed cap unchanged", func(t *testing.T) {
		got := EffectiveMorningCap(computed, nil)
		if !got.Equal(computed) {
			t.Fatalf("got=%s want=%s", got.Format("15:04"), computed.Format("15:04"))
		}
	})

	t.Run("prompted row with ExpiresAt → row.ExpiresAt wins", func(t *testing.T) {
		row := &storage.CheckinRow{
			Status:    storage.CheckinStatusPrompted,
			ExpiresAt: rowExpires,
		}
		got := EffectiveMorningCap(computed, row)
		if !got.Equal(rowExpires) {
			t.Fatalf("got=%s want=%s (row deadline must override freshly-floored cap)",
				got.Format("15:04"), rowExpires.Format("15:04"))
		}
	})

	t.Run("answered row → computed cap unchanged", func(t *testing.T) {
		// Once answered, the row's ExpiresAt is historical metadata,
		// not an active deadline — fall through to the computed cap so
		// the scheduler's normal force-send logic still bounds the loop.
		row := &storage.CheckinRow{
			Status:    storage.CheckinStatusAnswered,
			ExpiresAt: rowExpires,
		}
		got := EffectiveMorningCap(computed, row)
		if !got.Equal(computed) {
			t.Fatalf("got=%s want=%s (non-prompted row must not override)",
				got.Format("15:04"), computed.Format("15:04"))
		}
	})

	t.Run("expired row → computed cap unchanged", func(t *testing.T) {
		row := &storage.CheckinRow{
			Status:    storage.CheckinStatusExpired,
			ExpiresAt: rowExpires,
		}
		got := EffectiveMorningCap(computed, row)
		if !got.Equal(computed) {
			t.Fatalf("got=%s want=%s", got.Format("15:04"), computed.Format("15:04"))
		}
	})

	t.Run("prompted row with zero ExpiresAt → computed cap unchanged", func(t *testing.T) {
		// Defensive: a row with prompted status but no ExpiresAt is
		// malformed (SaveCheckinPrompted never produces this), but if
		// it ever appears we must not propagate the zero time into the
		// gate — that would be interpreted as "epoch", always-past.
		row := &storage.CheckinRow{
			Status: storage.CheckinStatusPrompted,
		}
		got := EffectiveMorningCap(computed, row)
		if !got.Equal(computed) {
			t.Fatalf("got=%s want=%s (zero ExpiresAt must fall through)",
				got.Format("15:04"), computed.Format("15:04"))
		}
	})
}

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
