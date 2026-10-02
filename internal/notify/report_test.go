package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/ai"
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

func TestDeliverEveningReportDefersWhenDashboardSnapshotUnavailable(t *testing.T) {
	store := &recordingDeliveryStore{}
	sendCalls := 0

	sent, err := deliverEveningReport(
		reportDeliveryDurable,
		store,
		&storage.DashboardResponse{CacheState: storage.DashboardCacheStateUnavailable},
		"report:evening:2026-09-10",
		func() error {
			sendCalls++
			return nil
		},
	)
	if sent {
		t.Fatal("unavailable dashboard must not be marked as sent")
	}
	if !errors.Is(err, ErrDashboardSnapshotUnavailable) {
		t.Fatalf("error = %v, want ErrDashboardSnapshotUnavailable", err)
	}
	if sendCalls != 0 || store.reserveCtx != nil || store.completeCtx != nil {
		t.Fatalf("unavailable dashboard sent or reserved a durable delivery: calls=%d reserve=%v complete=%v", sendCalls, store.reserveCtx != nil, store.completeCtx != nil)
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

// Morning report timing is independent from the check-in response window.
func TestMorningCapTimeKeepsDeadlineIndependentFromCheckin(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	enter := time.Date(2026, 5, 19, 10, 0, 0, 0, loc)

	t.Run("adaptive cap already past remains unchanged", func(t *testing.T) {
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
			TypicalWakeHour:    7,
			TypicalWakeMinute:  47,
			TypicalWakeOK:      true,
		}
		cap := cfg.MorningCapTime(enter)
		want := time.Date(2026, 5, 19, 8, 47, 0, 0, loc)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (computed deadline)", cap.Format("15:04"), want.Format("15:04"))
		}
	})

	t.Run("adaptive cap in the future → untouched", func(t *testing.T) {
		// Late riser: wakes at 11:00, cap = 12:00. Scheduler enters at
		// 10:00 → cap is in the future and remains fixed.
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
		// morning_hour+4 = 14:00.
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

	t.Run("explicit MorningCapHour past at entry remains unchanged", func(t *testing.T) {
		cfg := Config{
			Timezone:           "Europe/Belgrade",
			MorningWeekdayHour: 10,
			MorningCapHour:     9,
		}
		cap := cfg.MorningCapTime(enter)
		want := time.Date(2026, 5, 19, 9, 0, 0, 0, loc)
		if !cap.Equal(want) {
			t.Fatalf("cap=%s want=%s (explicit cap)", cap.Format("15:04"), want.Format("15:04"))
		}
	})
}

// CheckinExpired remains in the call signature for compatibility; report copy
// does not depend on whether a check-in was answered.
func TestFormatMorningIgnoresCheckinExpiry(t *testing.T) {
	briefing := &health.BriefingResponse{Date: "2026-05-18"}
	loc, _ := time.LoadLocation("UTC")

	for _, expired := range []bool{false, true} {
		out := formatMorning(briefing, nil, "ru", loc, freshness{}, expired)
		if strings.Contains(out, "Хотите") || strings.Contains(out, "answer tomorrow") || strings.Contains(out, "ответьте") {
			t.Errorf("report must not mention an unanswered check-in:\n%s", out)
		}
	}
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
		"<h2>🌅 Sunday, June 14</h2>",
		"<h3>😴 Sleep</h3>",
		"No sleep data for last night yet.",
		"<aside><strong>Moderate</strong>",
		"<p><strong>At a glance</strong>",
		"⚡ <strong>64/100</strong> · Energy",
		"◉ <strong>70/100</strong> · Readiness",
		"<hr/>",
		"<details><summary>✦ Insights</summary>",
		"<em>AI explains &lt;moderate &amp; controlled&gt;</em>",
		"<strong>🎯 Plan for today</strong>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rich morning missing %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{
		"<table", "<blockquote>", "average of up to 7 nights", "legacy sleep essay must stay hidden",
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

func TestFormatMorning_UsesLatestNightAndSuppressesDuplicateSynthesis(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	latest := 7.93
	briefing.Sleep.LatestTotal = &latest
	briefing.Sleep.LatestDate = briefing.Date
	briefing.TodayGuidance = &health.DashboardTodayGuidance{
		Action:  "moderate",
		Label:   "Moderate",
		Summary: "Keep the day controlled.",
		Reason:  "Sleep data is still settling.",
	}

	out := formatMorning(briefing, map[string]string{
		"SYNTHESIS": "Sleep data is still settling.",
	}, "en", loc, freshness{}, false)

	if !strings.Contains(out, "7 h 56 min") || !strings.Contains(out, "2026-06-14") {
		t.Fatalf("morning report should show the latest night, got:\n%s", out)
	}
	if strings.Contains(out, "7 h 18 min") {
		t.Fatalf("morning report leaked the rolling average as last night's sleep:\n%s", out)
	}
	if strings.Contains(out, "🤖") {
		t.Fatalf("morning report repeated the rule-based reason as AI synthesis:\n%s", out)
	}
}

func TestMorningSleepContextIsCurrentDatedAndVisibleInPlainAndRich(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	hours, baseline := 7.4, 7.1
	briefing.Date = "2026-06-15"
	sleep := health.MorningReportSleep{
		ReportDate: "2026-06-15", Date: "2026-06-15", Hours: &hours,
		BaselineHours: &baseline, BaselineNights: 8, Capture: health.NightCaptureComplete,
		Assessment: health.NightDurationPlausible, Finalization: health.NightFinalFinal,
	}
	blocks := map[string]string{ai.BlockSleep: "The duration is close to your usual range."}
	plain := formatMorning(briefing, blocks, "en", loc, freshness{}, false, sleep)
	rich := formatMorningRich(briefing, blocks, "en", loc, freshness{}, false, "", sleep)
	for _, want := range []string{sleep.ReportDate, "7 h 24 min", "Usual level: 7 h 6 min across 8 nights", "The duration is close to your usual range."} {
		if !strings.Contains(plain, want) || !strings.Contains(rich, want) {
			t.Fatalf("plain/rich should both include %q\nplain:\n%s\nrich:\n%s", want, plain, rich)
		}
	}
	if strings.Contains(plain, "preliminary") || strings.Contains(rich, "preliminary") {
		t.Fatalf("complete final sleep should not mark estimates preliminary\nplain:\n%s\nrich:\n%s", plain, rich)
	}
	if !strings.Contains(plain, "Morning report — 2026-06-15") || !strings.Contains(rich, "Monday, June 15") {
		t.Fatalf("explicit report date should own both headers\nplain:\n%s\nrich:\n%s", plain, rich)
	}
	if strings.Index(plain, "😴 <b>Sleep") > strings.Index(plain, "⚡ <b>Fair") || strings.Index(rich, "<h3>😴 Sleep") > strings.Index(rich, "<aside>") {
		t.Fatalf("sleep must precede readiness/verdict\nplain:\n%s\nrich:\n%s", plain, rich)
	}
}

func TestMorningSleepIncompleteStatesHaveOneCaveatAndNoComparisonOrAIClaim(t *testing.T) {
	loc := time.UTC
	briefing := sampleBriefing()
	hours := 6.5
	cases := []struct {
		name   string
		sleep  health.MorningReportSleep
		caveat string
	}{
		{
			name:   "partial",
			sleep:  health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, BaselineHours: &hours, BaselineNights: 10, Capture: health.NightCapturePartial, Assessment: health.NightDurationUnknown},
			caveat: "Sleep data are incomplete; duration may change.",
		},
		{
			name:   "unknown capture",
			sleep:  health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, BaselineHours: &hours, BaselineNights: 10, Capture: health.NightCaptureUnknown, Assessment: health.NightDurationUnknown},
			caveat: "Sleep capture status is unknown.",
		},
		{
			name:   "stale",
			sleep:  health.MorningReportSleep{ReportDate: briefing.Date, Date: "2026-06-13", Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalFinal},
			caveat: "Energy and readiness estimates are preliminary",
		},
		{
			name:   "missing",
			sleep:  health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Capture: health.NightCaptureUnknown, Assessment: health.NightDurationUnknown},
			caveat: "Sleep capture status is unknown.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plain := formatMorning(briefing, map[string]string{ai.BlockSleep: "Unsupported AI sleep claim."}, "en", loc, freshness{}, true, tc.sleep)
			rich := formatMorningRich(briefing, map[string]string{ai.BlockSleep: "Unsupported AI sleep claim."}, "en", loc, freshness{}, true, "technical banner", tc.sleep)
			for _, output := range []string{plain, rich} {
				if !strings.Contains(output, tc.caveat) {
					t.Fatalf("%s output missing caveat %q:\n%s", tc.name, tc.caveat, output)
				}
				if strings.Contains(output, "Usual level") || strings.Contains(output, "Unsupported AI sleep claim") || strings.Contains(output, "technical banner") || strings.Contains(output, "deadline") {
					t.Fatalf("%s output includes comparison, sleep AI, or technical banner:\n%s", tc.name, output)
				}
			}
			if strings.Count(plain, tc.caveat) != 1 || strings.Count(rich, tc.caveat) != 1 {
				t.Fatalf("%s caveat should appear once\nplain:\n%s\nrich:\n%s", tc.name, plain, rich)
			}
		})
	}
}

func TestMorningWhyOmitsAlertsWithoutPerFactorDateProvenance(t *testing.T) {
	briefing := sampleBriefing()
	briefing.Alerts = []health.Alert{{Metric: "wrist_temperature", Severity: "warning", Text: "Wrist temperature is above its rolling average."}}
	sleep := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Capture: health.NightCaptureUnknown}
	for _, output := range []string{
		formatMorning(briefing, nil, "en", time.UTC, freshness{}, false, sleep),
		formatMorningRich(briefing, nil, "en", time.UTC, freshness{}, false, "", sleep),
	} {
		if strings.Contains(output, "Wrist temperature") || strings.Contains(output, "Why") {
			t.Fatalf("rolling-average alert without factor date/provenance must be omitted:\n%s", output)
		}
	}
}

func TestMorningDateMismatchDatesTheReadinessAndEnergyValues(t *testing.T) {
	briefing := sampleBriefing()
	currentReportDate := "2026-06-15"
	sleep := health.MorningReportSleep{ReportDate: currentReportDate, Date: currentReportDate, Capture: health.NightCaptureUnknown}
	plain := formatMorning(briefing, nil, "en", time.UTC, freshness{}, false, sleep)
	rich := formatMorningRich(briefing, nil, "en", time.UTC, freshness{}, false, "", sleep)
	for _, output := range []string{plain, rich} {
		if !strings.Contains(output, "As of 2026-06-14") || !strings.Contains(output, "data from 2026-06-14") {
			t.Fatalf("older readiness/energy values need their source date:\n%s", output)
		}
	}
	if strings.Contains(plain, "Today: Moderate") {
		t.Fatalf("plain report labeled older metrics as today:\n%s", plain)
	}
}

func TestSleepDurationFormattingUsesLocalizedHoursAndMinutes(t *testing.T) {
	for _, tc := range []struct{ lang, want string }{
		{"en", "7 h 30 min"}, {"ru", "7 ч 30 мин"}, {"sr", "7 h 30 min"},
	} {
		if got := formatSleepDuration(7.5, tc.lang); got != tc.want {
			t.Errorf("%s duration = %q, want %q", tc.lang, got, tc.want)
		}
	}
}

func TestCheckinAckSavedCopyIsLocalized(t *testing.T) {
	for _, tc := range []struct{ lang, want string }{
		{"en", "Thanks, your answer was saved."},
		{"ru", "Спасибо, ваш ответ сохранён."},
		{"sr", "Hvala, vaš odgovor je sačuvan."},
	} {
		if got := tr(tc.lang, "checkin_ack_saved"); got != tc.want {
			t.Errorf("%s saved acknowledgement = %q, want %q", tc.lang, got, tc.want)
		}
	}
}

func TestMorningFormattersLocalizedSleepScenarios(t *testing.T) {
	type locale struct {
		code, current, partial, unknown, duration, baseline, verdict, reason, action, aiSleep string
	}
	locales := []locale{
		{"en", "Sleep for the night", "Sleep data are incomplete", "No sleep data for last night yet.", "7 h 30 min", "Usual level: 7 h across 8 nights", "Moderate", "Useful capacity, not a peak day.", "Keep the day controlled.", "Sleep duration is one useful data point."},
		{"ru", "Сон за ночь", "Данные о сне неполные", "Данных о прошедшей ночи пока нет.", "7 ч 30 мин", "Обычный уровень: 7 ч за 8 ночей", "Умеренный день", "Ресурса достаточно, но это не день для максимальной нагрузки.", "Сохраняйте умеренный темп.", "Длительность сна — лишь один показатель."},
		{"sr", "San za noć", "Podaci o snu su nepotpuni", "Podaci o protekloj noći još nisu dostupni.", "7 h 30 min", "Uobičajen nivo: 7 h tokom 8 noći", "Umeren dan", "Ima dovoljno kapaciteta, ali nije dan za maksimum.", "Držite umeren tempo.", "Trajanje sna je jedan koristan podatak."},
	}
	for _, loc := range locales {
		for _, state := range []string{"complete", "partial", "stale", "missing"} {
			t.Run(loc.code+"/"+state, func(t *testing.T) {
				briefing := sampleBriefing()
				briefing.EnergyBank.VerdictLabel = loc.verdict
				briefing.EnergyBank.VerdictReason = loc.reason
				briefing.ReadinessTip = loc.action
				hours := 7.5
				baseline := 7.0
				sleep := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, BaselineHours: &baseline, BaselineNights: 8, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalFinal}
				want := loc.current
				switch state {
				case "partial":
					sleep.Capture = health.NightCapturePartial
					sleep.Finalization = health.NightFinalProvisional
					want = loc.partial
				case "stale":
					sleep.ReportDate = "2026-06-15"
					sleep.Date = "2026-06-13"
					want = loc.unknown
				case "missing":
					sleep.Capture = health.NightCaptureUnknown
					sleep.Hours = nil
					want = loc.unknown
				}
				plain := formatMorning(briefing, map[string]string{ai.BlockSleep: loc.aiSleep}, loc.code, time.UTC, freshness{}, true, sleep)
				rich := formatMorningRich(briefing, map[string]string{ai.BlockSleep: loc.aiSleep}, loc.code, time.UTC, freshness{}, true, "deadline", sleep)
				for _, output := range []string{plain, rich} {
					if !strings.Contains(output, want) {
						t.Fatalf("%s %s output missing %q:\n%s", loc.code, state, want, output)
					}
					for _, localized := range []string{loc.verdict, loc.reason, loc.action} {
						if !strings.Contains(output, localized) {
							t.Fatalf("%s %s output missing localized fixture %q:\n%s", loc.code, state, localized, output)
						}
					}
					if state != "complete" && state != "partial" && strings.Contains(output, loc.aiSleep) {
						t.Fatalf("%s %s output leaked AI sleep text:\n%s", loc.code, state, output)
					}
					if state == "partial" && !strings.Contains(output, loc.aiSleep) {
						t.Fatalf("%s partial output should include bounded AI sleep explanation:\n%s", loc.code, output)
					}
					if state != "complete" && strings.Contains(output, loc.baseline) {
						t.Fatalf("%s %s output leaked baseline comparison:\n%s", loc.code, state, output)
					}
					if strings.Contains(output, "deadline") || strings.Contains(output, "deadline reached") {
						t.Fatalf("%s %s output leaked technical banner:\n%s", loc.code, state, output)
					}
				}
				if state == "complete" {
					if !strings.Contains(plain, loc.duration) || !strings.Contains(rich, loc.duration) || !strings.Contains(plain, loc.baseline) || !strings.Contains(rich, loc.baseline) || !strings.Contains(plain, loc.aiSleep) || !strings.Contains(rich, loc.aiSleep) {
						t.Fatalf("complete final sleep should use h/min and may use cached AI\nplain:\n%s\nrich:\n%s", plain, rich)
					}
				}
			})
		}
	}
}

func TestMorningSleepContextHidesStaleAIAndLabelsOlderObservation(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	hours := 6.8
	sleep := health.MorningReportSleep{ReportDate: briefing.Date, Date: "2026-06-13", Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalFinal}
	blocks := map[string]string{ai.BlockSleep: "stale AI sleep text"}
	plain := formatMorning(briefing, blocks, "en", loc, freshness{}, false, sleep)
	rich := formatMorningRich(briefing, blocks, "en", loc, freshness{}, false, "", sleep)
	for _, out := range []string{plain, rich} {
		if !strings.Contains(out, "No sleep data for last night yet.") || !strings.Contains(out, "2026-06-13") || !strings.Contains(out, "6 h 48 min") {
			t.Fatalf("missing sleep should have an explicitly dated older observation: %s", out)
		}
		if strings.Contains(out, "stale AI sleep text") {
			t.Fatalf("stale AI sleep text leaked: %s", out)
		}
	}
}

func TestMorningSleepPartialAndProvisionalAreMarkedAndEscaped(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	hours := 7.2
	sleep := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, Capture: health.NightCapturePartial, Assessment: health.NightDurationUnknown}
	plain := formatMorning(briefing, map[string]string{ai.BlockSleep: "UNSAFE AI SLEEP CLAIM"}, "en", loc, freshness{}, false, sleep)
	rich := formatMorningRich(briefing, map[string]string{ai.BlockSleep: "UNSAFE AI SLEEP CLAIM"}, "en", loc, freshness{}, false, "", sleep)
	for _, out := range []string{plain, rich} {
		if !strings.Contains(out, "incomplete") || !strings.Contains(out, "Recorded sleep for 2026-06-14") || strings.Contains(out, "Sleep for the night") || strings.Contains(out, "UNSAFE AI SLEEP CLAIM") || strings.Contains(out, "Limited <coverage") {
			t.Fatalf("partial capture should use deterministic caveat and assessment: %s", out)
		}
	}
	if strings.Contains(rich, "&lt;coverage &amp; confidence&gt;") {
		t.Fatalf("assessment enum must not render as free text: %s", rich)
	}

	sleep.Capture = "complete"
	sleep.Finalization = "provisional"
	sleep.Assessment = health.NightDurationPlausible
	plain = formatMorning(briefing, map[string]string{ai.BlockSleep: "provisional AI"}, "ru", loc, freshness{}, false, sleep)
	if !strings.Contains(plain, "предварительные") || !strings.Contains(plain, "provisional AI") {
		t.Fatalf("provisional current sleep should be marked and may render AI: %s", plain)
	}
}

func TestMorningSleepExplicitMissingAndOutlierContextNeverFallsBackToBriefingOrAI(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	legacyHours := 7.6
	briefing.Sleep.LatestDate = briefing.Date
	briefing.Sleep.LatestTotal = &legacyHours
	missing := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Capture: health.NightCaptureUnknown, Assessment: health.NightDurationUnknown}
	blocks := map[string]string{ai.BlockSleep: "must not show"}
	plain := formatMorning(briefing, blocks, "en", loc, freshness{}, false, missing)
	if !strings.Contains(plain, "No sleep data for last night yet.") || strings.Contains(plain, "7.6 h") || strings.Contains(plain, "must not show") {
		t.Fatalf("explicit missing context must not fall through to legacy sleep or AI: %s", plain)
	}
	unknownHours := 7.6
	unknown := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &unknownHours, Capture: health.NightCaptureUnknown, Assessment: health.NightDurationUnknown}
	plain = formatMorning(briefing, nil, "en", loc, freshness{}, false, unknown)
	if !strings.Contains(plain, "Recorded sleep for 2026-06-14") || strings.Contains(plain, "Sleep for the night") {
		t.Fatalf("unknown capture must not claim a full night: %s", plain)
	}

	hours := 10.5
	outlier := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationOutlier, Finalization: health.NightFinalFinal}
	plain = formatMorning(briefing, blocks, "sr", loc, freshness{}, false, outlier)
	if strings.Contains(plain, "neuobičajeno") || strings.Contains(plain, "must not show") {
		t.Fatalf("outlier enum should not become a user-facing certainty or AI claim: %s", plain)
	}
}

func TestMorningSleepCaveatsRemainVisibleForOlderAndMissingDurations(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	olderHours := 5.5
	cases := []struct {
		name    string
		context health.MorningReportSleep
		want    string
	}{
		{
			name: "older partial outlier",
			context: health.MorningReportSleep{
				ReportDate: briefing.Date, Date: "2026-06-13", Hours: &olderHours,
				Capture: health.NightCapturePartial, Assessment: health.NightDurationOutlier,
				Finalization: health.NightFinalProvisional,
			},
			want: "incomplete",
		},
		{
			name: "current partial without duration",
			context: health.MorningReportSleep{
				ReportDate: briefing.Date, Date: briefing.Date,
				Capture: health.NightCapturePartial, Assessment: health.NightDurationUnknown,
			},
			want: "incomplete",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plain := formatMorning(briefing, nil, "en", loc, freshness{}, false, tc.context)
			rich := formatMorningRich(briefing, nil, "en", loc, freshness{}, false, "", tc.context)
			for _, out := range []string{plain, rich} {
				if !strings.Contains(out, tc.want) || !strings.Contains(out, "incomplete") {
					t.Fatalf("capture caveat missing from selected night: %s", out)
				}
				if tc.context.Finalization == health.NightFinalProvisional && tc.context.Capture == health.NightCaptureComplete && !strings.Contains(out, "preliminary") {
					t.Fatalf("provisional caveat missing from selected night: %s", out)
				}
			}
		})
	}
}

func TestCanonicalSleepSectionSuppressesConflictingBriefingSleepReason(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	briefing.Sections[0].Summary = "Conflicting sleep duration: 3.1h from another night"
	hours := 7.4
	sleep := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalFinal}
	plain := formatMorning(briefing, nil, "en", loc, freshness{}, false, sleep)
	rich := formatMorningRich(briefing, nil, "en", loc, freshness{}, false, "", sleep)
	for _, out := range []string{plain, rich} {
		if !strings.Contains(out, "7 h 24 min") || strings.Contains(out, "Conflicting sleep duration") || strings.Contains(out, "3.1h") {
			t.Fatalf("generic conflicting sleep reason leaked below canonical sleep section: %s", out)
		}
	}
}

func TestDurationOnlyFallbackIsLocalizedAndFollowsDatedFact(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	hours := 7.4
	cases := []struct {
		lang string
		want string
	}{
		{"en", "Duration alone cannot show sleep quality."},
		{"ru", "По одной длительности нельзя оценить качество сна."},
		{"sr", "Samo trajanje sna ne pokazuje njegov kvalitet."},
	}
	for _, tc := range cases {
		sleep := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalFinal}
		plain := formatMorning(briefing, nil, tc.lang, loc, freshness{}, false, sleep)
		factAt, explanationAt := strings.Index(plain, "Sleep for the night"), strings.Index(plain, tc.want)
		if tc.lang != "en" {
			factAt = strings.Index(plain, "2026-06-14")
		}
		if !strings.Contains(plain, tc.want) || factAt < 0 || explanationAt <= factAt {
			t.Fatalf("%s copy should put the dated fact before the duration limit:\n%s", tc.lang, plain)
		}
		if strings.Contains(plain, "plausible range") || strings.Contains(plain, "правдоподобном диапазоне") || strings.Contains(plain, "verovatnom rasponu") {
			t.Fatalf("%s copy exposed an internal classification:\n%s", tc.lang, plain)
		}
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

	for _, want := range []string{"Updated: Watch 1h · Sleep 2 days"} {
		if !strings.Contains(out, want) {
			t.Fatalf("morning report missing %q:\n%s", want, out)
		}
	}
	for _, hidden := range []string{"Adequate sleep", "Mixed markers", "Normal load", "<b>Why</b>"} {
		if strings.Contains(out, hidden) {
			t.Fatalf("undated or generic factor %q leaked into Why:\n%s", hidden, out)
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

	for _, staleValue := range []string{"7 h 18 min", "68%"} {
		if strings.Contains(out, staleValue) {
			t.Fatalf("rich summary should suppress stale value %q:\n%s", staleValue, out)
		}
	}
	for _, want := range []string{"No sleep data for last night yet.", "Apple Watch off"} {
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

func TestCanonicalSleepSectionSuppressesSleepDerivedHeadlines(t *testing.T) {
	for _, headline := range []health.HeadlineSignal{
		{Key: "sleep_debt", Severity: "warning", Detail: "Conflicting headline: 3.1h"},
		{Key: "stress", Severity: "warning", Detail: "Conflicting headline: 3.1h", Metrics: []health.HeadlineMetricDelta{{Metric: "sleep_awake"}, {Metric: "resting_heart_rate"}}},
	} {
		briefing := sampleBriefing()
		briefing.Headline = &headline
		hours := 7.4
		sleep := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalFinal}
		for _, output := range []string{formatMorning(briefing, nil, "en", time.UTC, freshness{}, false, sleep), formatMorningRich(briefing, nil, "en", time.UTC, freshness{}, false, "", sleep)} {
			if !strings.Contains(output, "7 h 24 min") || strings.Contains(output, "Conflicting headline") || strings.Contains(output, "3.1h") {
				t.Fatalf("sleep-derived headline leaked: %s", output)
			}
		}
	}
}

func TestMorningPartialSleepKeepsOtherValidatedAIExplanations(t *testing.T) {
	b := sampleBriefing()
	hours := 6.0
	sleep := health.MorningReportSleep{ReportDate: b.Date, Date: b.Date, Hours: &hours, Capture: health.NightCapturePartial, Assessment: health.NightDurationUnknown, Finalization: health.NightFinalProvisional}
	blocks := map[string]string{ai.BlockSleep: "unsupported sleep claim", ai.BlockRecovery: "grounded recovery explanation", ai.BlockYesterday: "grounded yesterday explanation"}
	for _, output := range []string{formatMorning(b, blocks, "ru", time.UTC, freshness{}, false, sleep), formatMorningRich(b, blocks, "ru", time.UTC, freshness{}, false, "", sleep)} {
		if strings.Contains(output, blocks[ai.BlockSleep]) {
			t.Fatal("partial sleep leaked AI sleep claim")
		}
		for _, key := range []string{ai.BlockRecovery, ai.BlockYesterday} {
			if !strings.Contains(output, blocks[key]) {
				t.Fatalf("missing validated %s", key)
			}
		}
	}
}

func TestMorningPreliminarySleepExplanationIsLocalizedAndGated(t *testing.T) {
	briefing := sampleBriefing()
	hours := 7.5
	base := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, Capture: health.NightCapturePartial, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional}
	cases := []struct {
		name  string
		sleep health.MorningReportSleep
		want  bool
	}{
		{"partial", base, true},
		{"complete provisional", func() health.MorningReportSleep { v := base; v.Capture = health.NightCaptureComplete; return v }(), true},
		{"unknown", func() health.MorningReportSleep { v := base; v.Assessment = health.NightDurationUnknown; return v }(), false},
		{"outlier", func() health.MorningReportSleep { v := base; v.Assessment = health.NightDurationOutlier; return v }(), false},
		{"stale", func() health.MorningReportSleep { v := base; v.Date = "2026-06-13"; return v }(), false},
	}
	for _, tc := range cases {
		for _, lang := range []string{"en", "ru", "sr"} {
			t.Run(tc.name+"/"+lang, func(t *testing.T) {
				choice := health.MorningSleepChoiceAwaitingCompletion
				caveatKey := "partial"
				if tc.name == "complete provisional" {
					choice, caveatKey = health.MorningSleepChoiceAwaitingFinalization, "provisional"
				}
				localized, _ := health.MorningSleepExplanation(choice, lang)
				caveat := morningSleepCopy(lang, caveatKey)
				blocks := map[string]string{ai.BlockSleep: localized}
				plain := formatMorning(briefing, blocks, lang, time.UTC, freshness{}, false, tc.sleep)
				rich := formatMorningRich(briefing, blocks, lang, time.UTC, freshness{}, false, "", tc.sleep)
				for _, output := range []string{plain, rich} {
					if strings.Contains(output, "Usual level") || strings.Contains(output, "Обычный уровень") || strings.Contains(output, "Uobičajen nivo") {
						t.Fatalf("preliminary output included baseline:\n%s", output)
					}
					if strings.Contains(output, localized) != tc.want {
						t.Fatalf("preliminary AI visibility = %v, want %v\n%s", strings.Contains(output, localized), tc.want, output)
					}
					if tc.want && (strings.Count(output, caveat) != 1 || strings.Count(output, localized) != 1) {
						t.Fatalf("AI explanation and server caveat should appear once:\n%s", output)
					}
				}
			})
		}
	}
}

func TestMorningOutlierHasDeterministicLimitationInBothFormats(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	briefing := sampleBriefing()
	hours, baseline := 10.5, 7.5
	sleep := health.MorningReportSleep{ReportDate: briefing.Date, Date: briefing.Date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationOutlier, Finalization: health.NightFinalFinal, BaselineHours: &baseline, BaselineNights: 14}
	for _, lang := range []string{"en", "ru", "sr"} {
		t.Run(lang, func(t *testing.T) {
			blocks := map[string]string{ai.BlockSleep: "UNSAFE_SLEEP_PROSE"}
			plain := formatMorning(briefing, blocks, lang, loc, freshness{}, false, sleep)
			rich := formatMorningRich(briefing, blocks, lang, loc, freshness{}, false, "", sleep)
			for name, text := range map[string]string{"plain": plain, "rich": rich} {
				if !strings.Contains(text, morningSleepCopy(lang, "assessment_limited")) {
					t.Fatalf("%s missing deterministic limitation: %s", name, text)
				}
				if strings.Contains(text, "UNSAFE_SLEEP_PROSE") || strings.Contains(text, morningSleepCopy(lang, "partial")) || strings.Contains(text, fmt.Sprintf(morningSleepCopy(lang, "baseline"), formatSleepDuration(baseline, lang), 14)) {
					t.Fatalf("%s contains unsupported sleep claim: %s", name, text)
				}
			}
		})
	}
}
