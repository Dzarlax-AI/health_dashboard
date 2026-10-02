package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"health-receiver/internal/notify"
	"health-receiver/internal/storage"
)

func TestMorningLifecycleRecoversDueReportAndKeepsCheckinAlive(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	cfg := notify.Config{Timezone: "Europe/Belgrade", MorningWeekdayHour: 10, MorningWeekendHour: 12}
	for _, tc := range []struct {
		name                string
		day, hour, minute   int
		sent, checkin, want bool
	}{
		{"before schedule", 1, 9, 59, false, false, false},
		{"scheduled no ingestion", 1, 10, 0, false, false, true},
		{"restart after schedule", 1, 11, 45, false, false, true},
		{"report done reminder still active", 1, 11, 0, true, true, true},
		{"early checkin remains active", 1, 9, 0, false, true, true},
		{"weekend before noon", 3, 11, 59, false, false, false},
		{"weekend due", 3, 12, 0, false, false, true},
		{"late missed report", 1, 21, 0, false, false, true},
		{"quiet evening", 1, 20, 0, true, true, false},
		{"quiet early morning", 1, 4, 59, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, tc.day, tc.hour, tc.minute, 0, 0, loc)
			if got := morningLifecycleDue(cfg, now, tc.sent, tc.checkin); got != tc.want {
				t.Fatalf("due=%v want %v", got, tc.want)
			}
		})
	}
}

func TestMorningLifecycleUsesTenantDateAcrossUTCAndDST(t *testing.T) {
	cfg := notify.Config{Timezone: "Europe/Belgrade", MorningWeekdayHour: 10, MorningWeekendHour: 10}
	for _, timestamp := range []string{"2026-10-01T08:00:00Z", "2026-10-25T09:00:00Z"} {
		now, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			t.Fatal(err)
		}
		if !morningLifecycleDue(cfg, now, false, false) {
			t.Fatalf("tenant-local10:00 not due: %s", timestamp)
		}
		if morningLifecycleDue(cfg, now.Add(-time.Second), false, false) {
			t.Fatalf("due before tenant schedule: %s", timestamp)
		}
	}
}

func TestMorningLifecycleLateConfiguredSchedule(t *testing.T) {
	cfg := notify.Config{Timezone: "Europe/Belgrade", MorningWeekdayHour: 21, MorningWeekendHour: 21}
	loc, _ := time.LoadLocation("Europe/Belgrade")
	now := time.Date(2026, 10, 1, 21, 0, 0, 0, loc)
	if !morningLifecycleDue(cfg, now, false, false) {
		t.Fatal("configured late report must remain eligible")
	}
	if morningLifecycleDue(cfg, now.Add(-time.Minute), false, false) {
		t.Fatal("report is not due yet")
	}
}

func TestMorningReportRejectedAttemptCanRetry(t *testing.T) {
	var mu sync.Mutex
	marked, calls := false, 0
	send := func() (bool, string, error) {
		calls++
		if calls == 1 {
			return true, "ready", errors.New("definitive rejection")
		}
		return true, "ready", nil
	}
	mark := func() error { marked = true; return nil }
	runMorningReportAttempt(&mu, func() bool { return marked }, send, mark)
	if marked {
		t.Fatal("rejected send must not mark report sent")
	}
	runMorningReportAttempt(&mu, func() bool { return marked }, send, mark)
	runMorningReportAttempt(&mu, func() bool { return marked }, send, mark)
	if !marked || calls != 2 {
		t.Fatalf("marked=%v calls=%d", marked, calls)
	}
}

func TestMorningReportReservationSuppressesAmbiguousRetry(t *testing.T) {
	var mu sync.Mutex
	attempts, deliveries, marks := 0, 0, 0
	reserved := false
	send := func() (bool, string, error) {
		attempts++
		if reserved {
			return false, "reserved", nil
		}
		reserved = true
		deliveries++
		return true, "ready", errors.New("ambiguous transport")
	}
	for range 2 {
		runMorningReportAttempt(&mu, func() bool { return false }, send, func() error { marks++; return nil })
	}
	if attempts != 2 || deliveries != 1 || marks != 0 {
		t.Fatalf("attempts=%d deliveries=%d marks=%d", attempts, deliveries, marks)
	}
}

func TestMorningReportConcurrentAttemptsSendOnce(t *testing.T) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	marked, deliveries := false, 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runMorningReportAttempt(&mu, func() bool { return marked }, func() (bool, string, error) { deliveries++; return true, "ready", nil }, func() error { marked = true; return nil })
		}()
	}
	wg.Wait()
	if deliveries != 1 || !marked {
		t.Fatalf("deliveries=%d marked=%v", deliveries, marked)
	}
}

func TestMorningAIWaitRequiresActualReadyCache(t *testing.T) {
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	deadline := now.Add(morningAIWaitBudget)
	for _, tc := range []struct {
		name                   string
		enabled, ready, failed bool
		at                     time.Time
		want                   bool
	}{
		{"cold cache waits", true, false, false, now, false},
		{"ongoing generation waits", true, false, false, now.Add(time.Minute), false},
		{"matching cache sends", true, true, false, now.Add(time.Minute), true},
		{"confirmed failure falls back", true, false, true, now, true},
		{"disabled AI sends", false, false, false, now, true},
		{"bounded timeout falls back", true, false, false, deadline, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := morningAIAllowsReport(tc.enabled, tc.ready, tc.failed, tc.at, deadline); got != tc.want {
				t.Fatalf("allows=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestMorningAIWaitDeadlineDoesNotSlide(t *testing.T) {
	var db storage.DB
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	first := morningAIReportDeadline(&db, "ru", "2026-10-01", now)
	later := morningAIReportDeadline(&db, "ru", "2026-10-01", now.Add(time.Minute))
	if !first.Equal(later) {
		t.Fatal("polling extended AI wait")
	}
	next := morningAIReportDeadline(&db, "ru", "2026-10-02", now.Add(24*time.Hour))
	if !next.Equal(now.Add(24*time.Hour + morningAIWaitBudget)) {
		t.Fatal("new day inherited old deadline")
	}
}

func TestMorningAIPrewarmStartsBeforeScheduledReport(t *testing.T) {
	cfg := notify.Config{Timezone: "UTC", MorningWeekdayHour: 10, MorningWeekendHour: 10}
	now := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	if !morningAIShouldPrewarm(cfg, now, false) || morningAIShouldPrewarm(cfg, now.Add(-time.Second), false) {
		t.Fatal("prewarm window must start 30 minutes before report")
	}
	if !morningAIShouldPrewarm(cfg, now.Add(-time.Hour), true) {
		t.Fatal("early ready sleep must start generation")
	}
}

func TestMorningAIReportAttemptWaitsThenSendsExactlyOnce(t *testing.T) {
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	deadline := now.Add(morningAIWaitBudget)
	ready, marked := false, false
	sends := 0
	attempt := func(at time.Time) {
		runMorningReportAttempt(nil, func() bool { return marked }, func() (bool, string, error) {
			if !morningAIAllowsReport(true, ready, false, at, deadline) {
				return false, "ai_pending", nil
			}
			sends++
			return true, "ai_ready", nil
		}, func() error { marked = true; return nil })
	}
	attempt(now)
	attempt(now.Add(time.Minute))
	if sends != 0 || marked {
		t.Fatal("cold cache report was sent before AI completed")
	}
	ready = true
	attempt(now.Add(2 * time.Minute))
	attempt(now.Add(3 * time.Minute))
	if sends != 1 || !marked {
		t.Fatalf("sends=%d marked=%v", sends, marked)
	}
}

func TestMorningLifecycleAndPrewarmHonorEarlierAdaptiveCap(t *testing.T) {
	cfg := notify.Config{Timezone: "Europe/Belgrade", MorningWeekdayHour: 10, MorningWeekendHour: 10, TypicalWakeOK: true, TypicalWakeHour: 7, TypicalWakeMinute: 47}
	loc, _ := time.LoadLocation("Europe/Belgrade")
	cap := time.Date(2026, 10, 1, 8, 47, 0, 0, loc)
	if !morningLifecycleDue(cfg, cap, false, false) || morningLifecycleDue(cfg, cap.Add(-time.Second), false, false) {
		t.Fatal("lifecycle must become due at adaptive cap before schedule")
	}
	if !morningAIShouldPrewarm(cfg, cap.Add(-30*time.Minute), false) || morningAIShouldPrewarm(cfg, cap.Add(-30*time.Minute-time.Second), false) {
		t.Fatal("prewarm must precede the earlier adaptive cap")
	}
}

type overdueCheckinRecorder struct{ times []time.Time }

func (s *overdueCheckinRecorder) ExpireOverdueTelegramCheckins(now time.Time) error {
	s.times = append(s.times, now)
	return nil
}
func TestMorningExpiryRunsWithoutSendingThroughQuietHours(t *testing.T) {
	store := &overdueCheckinRecorder{}
	var mu sync.Mutex
	for _, hour := range []int{20, 21, 23, 3} {
		now := time.Date(2026, 10, 1, hour, 0, 0, 0, time.UTC)
		if err := expireOverdueMorningCheckins(store, &mu, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.times) != 4 {
		t.Fatal("quiet-hour persistence cleanup was suppressed")
	}
}
