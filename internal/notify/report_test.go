package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

type recordingDeliveryStore struct {
	reserveCtx  context.Context
	completeCtx context.Context
	token       uuid.UUID
	status      string
}

func (s *recordingDeliveryStore) ReserveNotificationDelivery(ctx context.Context, _ string) (uuid.UUID, bool, error) {
	if s.status == "sent" || s.status == "ambiguous" {
		return uuid.Nil, false, nil
	}
	s.reserveCtx = ctx
	s.token = uuid.New()
	return s.token, true, nil
}

func (s *recordingDeliveryStore) CompleteNotificationDelivery(ctx context.Context, _ string, token uuid.UUID, status, _ string) error {
	if token != s.token {
		return errors.New("unexpected delivery token")
	}
	s.completeCtx = ctx
	s.status = status
	return ctx.Err()
}

func TestSendDurableReportUsesFreshCompletionContext(t *testing.T) {
	store := &recordingDeliveryStore{}
	sent, err := sendDurableReport(store, "report:test", func() error {
		select {
		case <-store.reserveCtx.Done():
			return nil
		default:
			return errors.New("reservation context remained live during external send")
		}
	})
	if err != nil || !sent {
		t.Fatalf("send = %v, %v", sent, err)
	}
	if store.completeCtx == nil || store.completeCtx == store.reserveCtx {
		t.Fatal("completion reused the reservation context")
	}
	if _, ok := store.completeCtx.Deadline(); !ok {
		t.Fatal("completion context must be bounded")
	}
	if store.status != "sent" {
		t.Fatalf("completion status = %q, want sent", store.status)
	}
}

func TestDeliverReportPreviewBypassesDurableReservation(t *testing.T) {
	store := &recordingDeliveryStore{status: "sent"}
	sendCalls := 0

	for range 2 {
		sent, err := deliverReport(reportDeliveryPreview, store, "report:morning:2026-08-05", func() error {
			sendCalls++
			return nil
		})
		if err != nil || !sent {
			t.Fatalf("preview delivery = %v, %v", sent, err)
		}
	}

	if sendCalls != 2 {
		t.Fatalf("preview send calls = %d, want 2", sendCalls)
	}
	if store.reserveCtx != nil || store.completeCtx != nil {
		t.Fatal("preview delivery touched the durable reservation store")
	}

	wantErr := errors.New("telegram rejected preview")
	_, err := deliverReport(reportDeliveryPreview, store, "report:morning:2026-08-05", func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("preview delivery error = %v, want %v", err, wantErr)
	}
}

func TestDeliverReportDurableKeepsAtMostOnceGate(t *testing.T) {
	store := &recordingDeliveryStore{}
	sendCalls := 0

	for range 2 {
		_, err := deliverReport(reportDeliveryDurable, store, "report:morning:2026-08-05", func() error {
			sendCalls++
			return nil
		})
		if err != nil {
			t.Fatalf("durable delivery: %v", err)
		}
	}

	if sendCalls != 1 {
		t.Fatalf("durable send calls = %d, want 1", sendCalls)
	}
}

func TestResolveMorningWakeStatusAllowsForcedSendAfterDetectorError(t *testing.T) {
	wakeErr := errors.New("wake detector unavailable")
	status, err := resolveMorningWakeStatus(storage.MorningWakeStatus{}, wakeErr, true)
	if err != nil || status.Reason != "query_error" {
		t.Fatalf("forced status=%+v err=%v", status, err)
	}
	if _, err := resolveMorningWakeStatus(storage.MorningWakeStatus{Reason: "steps_query_error"}, wakeErr, false); !errors.Is(err, wakeErr) {
		t.Fatalf("non-forced error=%v, want detector error", err)
	}
}

// TestMorningCapTime_FloorsPastCapsToPromptWindow pins the floor that
// keeps the check-in prompt window alive for users whose adaptive cap
// (typical_wake + 60min) lands earlier than the configured morning
// hour. Without it the smart-retry loop enters past cap on first tick
// and skips MorningActionPrompt entirely — silently disabling
// subjective check-in. See morning_gate.go.
func TestMorningCapTime_FloorsPastCapsToPromptWindow(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	enter := time.Date(2026, 5, 19, 10, 0, 0, 0, loc)

	t.Run("adaptive cap already past at scheduler entry → floored to now+MinPromptWindow", func(t *testing.T) {
		// User wakes at 07:47, adaptive cap = 08:47. Scheduler enters at
		// morning_weekday=10 → cap is 73 min in the past. Floor must
		// push it to 11:00 so the gate has a prompt window.
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
			TypicalWakeHour:    7,
			TypicalWakeMinute:  47,
			TypicalWakeOK:      true,
		}
		cap := cfg.MorningCapTime(enter)
		want := enter.Add(MinPromptWindow)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (floor to now+MinPromptWindow)", cap.Format("15:04"), want.Format("15:04"))
		}
	})

	t.Run("adaptive cap in the future → untouched", func(t *testing.T) {
		// Late riser: wakes at 11:00, cap = 12:00. Scheduler enters at
		// 10:00 → cap is in the future, floor must NOT fire.
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
			TypicalWakeHour:    11,
			TypicalWakeMinute:  0,
			TypicalWakeOK:      true,
		}
		cap := cfg.MorningCapTime(enter)
		want := time.Date(2026, 5, 19, 12, 0, 0, 0, loc)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (adaptive cap, untouched)", cap.Format("15:04"), want.Format("15:04"))
		}
	})

	t.Run("static fallback cap in the future → untouched", func(t *testing.T) {
		// No typical-wake data, no MorningCapHour override → default
		// morning_hour+4 = 14:00. Floor must NOT fire.
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
		}
		cap := cfg.MorningCapTime(enter)
		want := time.Date(2026, 5, 19, 14, 0, 0, 0, loc)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (static fallback)", cap.Format("15:04"), want.Format("15:04"))
		}
	})

	t.Run("explicit MorningCapHour past at entry → floored", func(t *testing.T) {
		// Operator set report_morning_cap=9 with morning_hour=10. The
		// configured cap fires before the scheduler even wakes — same
		// failure mode as the adaptive path, same floor applies.
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
			MorningCapHour:     9,
		}
		cap := cfg.MorningCapTime(enter)
		want := enter.Add(MinPromptWindow)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (explicit cap floored)", cap.Format("15:04"), want.Format("15:04"))
		}
	})
}

// formatMorning renders a soft expired-checkin footer only when the
// cap-path explicitly passes checkinExpired=true. Pins the contract
// promised by the PR description (and the existing checkin_expired_note
// i18n string) so future refactors don't accidentally drop the note.
func TestFormatMorning_AppendsExpiredNote(t *testing.T) {
	briefing := &health.BriefingResponse{Date: "2026-05-18"}
	loc, _ := time.LoadLocation("UTC")

	t.Run("checkinExpired=true → note appears", func(t *testing.T) {
		out := formatMorning(briefing, nil, "ru", loc, freshness{}, true)
		// The RU note text starts with "Хотите". Any future copy change
		// keeps the same key, so look for the i18n marker by checking
		// the key resolves and its prefix lands in the output.
		want := "Хотите"
		if !strings.Contains(out, want) {
			t.Errorf("expired note missing from output:\n%s\n\nwanted substring %q", out, want)
		}
	})

	t.Run("checkinExpired=false → no note", func(t *testing.T) {
		out := formatMorning(briefing, nil, "ru", loc, freshness{}, false)
		if strings.Contains(out, "Хотите") {
			t.Errorf("expired note rendered when checkinExpired=false:\n%s", out)
		}
	})

	t.Run("english locale also renders", func(t *testing.T) {
		out := formatMorning(briefing, nil, "en", loc, freshness{}, true)
		want := "Want the report"
		if !strings.Contains(out, want) {
			t.Errorf("EN expired note missing:\n%s\n\nwanted substring %q", out, want)
		}
	})
}

type fakeHTMLReportSender struct {
	richErr      error
	sendCalls    int
	richCalls    int
	lastSendText string
	lastRichHTML string
}

func (f *fakeHTMLReportSender) Send(text string) error {
	f.sendCalls++
	f.lastSendText = text
	return nil
}

func (f *fakeHTMLReportSender) SendRichHTML(html string) error {
	f.richCalls++
	f.lastRichHTML = html
	return f.richErr
}

func TestSendReportHTML_FallbackBehavior(t *testing.T) {
	t.Run("rich disabled sends fallback only", func(t *testing.T) {
		bot := &fakeHTMLReportSender{}
		if err := sendReportHTML(bot, Config{}, "morning", "<h2>rich</h2>", "<b>fallback</b>"); err != nil {
			t.Fatalf("send report: %v", err)
		}
		if bot.richCalls != 0 || bot.sendCalls != 1 || bot.lastSendText != "<b>fallback</b>" {
			t.Fatalf("unexpected calls: rich=%d send=%d text=%q", bot.richCalls, bot.sendCalls, bot.lastSendText)
		}
	})

	t.Run("rich success does not also send fallback", func(t *testing.T) {
		bot := &fakeHTMLReportSender{}
		if err := sendReportHTML(bot, Config{TelegramRichMessages: true}, "morning", "<h2>rich</h2>", "<b>fallback</b>"); err != nil {
			t.Fatalf("send report: %v", err)
		}
		if bot.richCalls != 1 || bot.sendCalls != 0 || bot.lastRichHTML != "<h2>rich</h2>" {
			t.Fatalf("unexpected calls: rich=%d send=%d richHTML=%q", bot.richCalls, bot.sendCalls, bot.lastRichHTML)
		}
	})

	t.Run("rich error falls back once", func(t *testing.T) {
		bot := &fakeHTMLReportSender{richErr: errors.New("telegram rejected rich")}
		if err := sendReportHTML(bot, Config{TelegramRichMessages: true}, "evening", "<h2>rich</h2>", "<b>fallback</b>"); err != nil {
			t.Fatalf("send report: %v", err)
		}
		if bot.richCalls != 1 || bot.sendCalls != 1 || bot.lastSendText != "<b>fallback</b>" {
			t.Fatalf("unexpected calls: rich=%d send=%d text=%q", bot.richCalls, bot.sendCalls, bot.lastSendText)
		}
	})

	t.Run("ambiguous rich transport does not risk a duplicate fallback", func(t *testing.T) {
		bot := &fakeHTMLReportSender{richErr: telegramTransportError{cause: errors.New("timeout after write")}}
		if err := sendReportHTML(bot, Config{TelegramRichMessages: true}, "morning", "<h2>rich</h2>", "<b>fallback</b>"); err == nil {
			t.Fatal("expected ambiguous transport error")
		}
		if bot.richCalls != 1 || bot.sendCalls != 0 {
			t.Fatalf("unexpected calls: rich=%d send=%d", bot.richCalls, bot.sendCalls)
		}
	})
}

func TestFormatMorningRich_StructureAndEscaping(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	out := formatMorningRich(briefing, map[string]string{
		"SLEEP":     "legacy sleep essay must stay hidden",
		"SYNTHESIS": "AI explains <moderate & controlled>",
	}, "en", loc, freshness{}, false, "")

	for _, want := range []string{
		"<h2>🌅 Morning report — 2026-06-14</h2>",
		"<h3>⚡ Today: Moderate</h3>",
		"<h3>Key metrics</h3>",
		"<h3>Why</h3>",
		"🤖 <em>AI explains &lt;moderate &amp; controlled&gt;</em>",
		"<h3>🎯 Plan for today</h3>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rich morning missing %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{
		"<table>", "<blockquote>", "<details>", "legacy sleep essay must stay hidden",
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("rich morning contains obsolete %q:\n%s", forbidden, out)
		}
	}
}

func TestFormatMorningLegacy_UsesSingleEscapedSynthesis(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	briefing.TodayGuidance = &health.DashboardTodayGuidance{
		Action:  "moderate",
		Label:   "Moderate <day>",
		Summary: "Keep effort <7 & controlled.",
		Reason:  "Fresh HRV & adequate energy.",
	}
	out := formatMorning(briefing, map[string]string{
		"SLEEP":     "legacy essay",
		"SYNTHESIS": "One <safe & aligned> explanation.",
	}, "en", loc, freshness{}, false)

	for _, want := range []string{
		"Moderate &lt;day&gt;",
		"Fresh HRV &amp; adequate energy.",
		"One &lt;safe &amp; aligned&gt; explanation.",
		"Keep effort &lt;7 &amp; controlled.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("legacy morning missing escaped %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "legacy essay") {
		t.Fatalf("legacy leaf leaked into v2 morning:\n%s", out)
	}
}

func TestFormatMorningLegacy_FiltersStaleReasonsBeforeCapAndShowsFreshness(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	out := formatMorning(briefing, nil, "en", loc, freshness{
		sleep:      48 * time.Hour,
		watch:      time.Hour,
		sleepKnown: true,
		watchKnown: true,
	}, false)

	if strings.Contains(out, "Adequate sleep") {
		t.Fatalf("stale sleep reason leaked into report:\n%s", out)
	}
	for _, want := range []string{
		"Mixed markers",
		"Normal load",
		"Updated: Watch 1h · Sleep 2 days",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("morning report missing %q:\n%s", want, out)
		}
	}
}

func TestFormatMorningRich_SuppressesStaleSummaryMetrics(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	out := formatMorningRich(briefing, nil, "en", loc, freshness{
		sleep:      48 * time.Hour,
		watch:      48 * time.Hour,
		sleepKnown: true,
		watchKnown: true,
	}, false, "")

	for _, staleValue := range []string{"7.3h", "68%"} {
		if strings.Contains(out, staleValue) {
			t.Fatalf("rich summary should suppress stale value %q:\n%s", staleValue, out)
		}
	}
	for _, want := range []string{"No sleep recorded", "Apple Watch off"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rich summary should include stale banner %q:\n%s", want, out)
		}
	}
}

func TestFormatEveningRich_TodayTable(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	now := time.Now().In(loc).Format("2006-01-02")
	briefing := sampleBriefing()
	briefing.Date = now
	dash := &storage.DashboardResponse{
		Date: now,
		Cards: []storage.CardData{
			{Metric: "step_count", Value: 8400, Prev: 7500, Unit: "steps"},
			{Metric: "active_energy", Value: 540, Prev: 560, Unit: "kcal"},
			{Metric: "apple_exercise_time", Value: 35, Prev: 25, Unit: "min"},
		},
	}
	out := formatEveningRich(briefing, dash, "en", loc, freshness{})

	for _, want := range []string{
		"<h2>🌆 Day so far — " + now + "</h2>",
		"<th>Vs yesterday</th>",
		"👟 Steps",
		"🏃 Exercise",
		"<h3>💡 Insights</h3>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rich evening missing %q:\n%s", want, out)
		}
	}
}

func sampleBriefing() *health.BriefingResponse {
	return &health.BriefingResponse{
		Date:                "2026-06-14",
		ReadinessScore:      72,
		ReadinessLabel:      "Fair",
		ReadinessToday:      70,
		ReadinessTodayLabel: "Fair",
		ReadinessTip:        "Keep the day controlled.",
		RecoveryPct:         68,
		Headline: &health.HeadlineSignal{
			Severity: "info",
			Title:    "Stable recovery",
			Detail:   "No major warning signs.",
		},
		EnergyBank: &health.EnergyBank{
			Capacity:      100,
			Current:       64,
			ActionVerdict: "moderate",
			VerdictLabel:  "Moderate",
			VerdictReason: "Useful capacity, not a peak day.",
		},
		Sleep: &health.SleepAnalysis{
			TotalAvg: 7.3,
			Sources: []health.SleepSourceSummary{
				{Source: "Apple Watch", Total: 7.3},
				{Source: "RingConn", Total: 7.0},
			},
		},
		Sections: []health.BriefingSection{
			{
				Key:     "sleep",
				Title:   "Sleep",
				Status:  "fair",
				Summary: "Adequate sleep",
				Details: []health.BriefingDetail{
					{Label: "Total", Value: "7.3h"},
				},
			},
			{
				Key:     "activity",
				Title:   "Activity",
				Status:  "good",
				Summary: "Normal load",
				Details: []health.BriefingDetail{
					{Label: "Steps", Value: "8400"},
				},
			},
			{
				Key:     "recovery",
				Title:   "Recovery",
				Status:  "fair",
				Summary: "Mixed markers",
				Details: []health.BriefingDetail{
					{Label: "HRV", Value: "42 ms"},
				},
			},
		},
		Insights: []health.Insight{{Text: "Keep tonight consistent.", Type: "positive"}},
	}
}
