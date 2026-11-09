package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestDaytimeSleepAnalysisUpdateBeforeFinalityKeepsBothVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := testdb.NewPool(ctx, testdb.DSN(t), "")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := testdb.SchemaName("sleep_analysis_revision")
	if err = testdb.CreateSchema(ctx, admin, schema); err != nil {
		t.Fatal(err)
	}
	defer testdb.DropSchema(context.Background(), admin, schema)
	pool, err := testdb.NewPool(ctx, testdb.DSN(t), schema)
	if err != nil {
		t.Fatal(err)
	}
	db := storage.NewFromPool(pool)
	defer db.Close()
	for _, ensure := range []func() error{db.EnsureAllTables, func() error { return db.EnsureReportHistoryTablesContext(ctx) }, func() error { return db.EnsureProactiveEventsTableContext(ctx) }, func() error { return db.EnsureCompletedNightSleepTableContext(ctx) }} {
		if err = ensure(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE metric_points ADD COLUMN IF NOT EXISTS quality TEXT NOT NULL DEFAULT 'ok', ADD COLUMN IF NOT EXISTS received_at TIMESTAMPTZ NOT NULL DEFAULT now()`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	date := now.Format("2006-01-02")
	hours := 6.0
	oldSleep := health.MorningReportSleep{ReportDate: date, Date: date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional}
	initial := newSleepAnalysisSnapshot(oldSleep, "Initial analysis of six hours.")
	data, _ := json.Marshal(map[string]any{"sleep": oldSleep, "sleep_analysis": initial, "decision": &health.DailyDecision{ID: "same", Mode: "moderate"}})
	key := "report:morning:" + date
	morning := &storage.ReportHistory{DeliveryKey: &key, Kind: "morning", LocalDate: date, Timezone: "UTC", Language: "en", PlainBody: initial.Text, Context: data, DecisionID: "same"}
	token, reserved, err := db.ReserveArchivedReport(ctx, morning)
	if err != nil || !reserved {
		t.Fatalf("morning reservation: %v %v", reserved, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE report_history SET prepared_at=$2 WHERE id=$1`, morning.ID, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	attempt, err := db.ReserveReportAttempt(ctx, morning.ID, "html", morning.PlainBody, token)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteReportAttempt(ctx, attempt, "sent", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteNotificationDelivery(ctx, key, token, "sent", ""); err != nil {
		t.Fatal(err)
	}
	var calls int
	ambiguous := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if ambiguous {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
	}))
	defer server.Close()
	oldBase := telegramAPIBase
	telegramAPIBase = server.URL
	defer func() { telegramAPIBase = oldBase }()
	cfg := Config{Token: "fixture", ChatID: "fixture", Lang: "en", Timezone: "UTC", DaytimeEnabled: true, DaytimeStartHour: 9, DaytimeEndHour: 21}
	b := &health.BriefingResponse{Date: date, DailyDecision: &health.DailyDecision{ID: "same", Mode: "moderate"}, Sleep: &health.SleepAnalysis{LatestDate: date, Nights: 7, TotalAvg: 7}}
	setNight := func(h float64, hash string, final bool) {
		t.Helper()
		b.Sleep.LatestTotal = &h
		b.Sleep.Sources = []health.SleepSourceSummary{{Source: "fixture", Total: h, Core: h - 2, Deep: 1, REM: 1, Awake: 0.1}}
		n := health.CompletedNightSleep{WakeDate: date, DurationHours: h, Source: "fixture", SourceEpoch: "fixture", InputHash: hash, AlgorithmVersion: "v1", CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalProvisional, ClaimEligibility: health.NightClaimIneligible, ObservedAt: now}
		if final {
			n.FinalizationState = health.NightFinalFinal
			n.ClaimEligibility = health.NightClaimEligible
			n.FinalizedAt = &now
		}
		if e := db.SaveCompletedNightSleep(ctx, n, nil); e != nil {
			t.Fatal(e)
		}
	}
	load := func() (*health.BriefingResponse, error) { return b, nil }
	bot := NewBot("fixture", "fixture")
	setNight(7.5, "late-data", false)
	aligned := b.Sleep.LatestTotal
	stale := 6.25
	b.Sleep.LatestTotal = &stale
	maybeFireDaytime(ctx, bot, db, cfg, "", now, load)
	if calls != 0 {
		t.Fatal("an analysis update escaped while the aggregate cache lagged the canonical night")
	}
	b.Sleep.LatestTotal = aligned
	if candidate, e := evaluateSleepAnalysisUpdate(ctx, db, cfg, now, load); e != nil || candidate == nil {
		t.Fatalf("candidate missing: %v %+v", e, candidate)
	}
	maybeFireDaytime(ctx, bot, db, cfg, "", now, load)
	maybeFireDaytime(ctx, bot, db, cfg, "", now.Add(time.Minute), load)
	if calls != 1 {
		t.Fatalf("provisional analysis with unchanged day decision should send once: %d", calls)
	}
	delivered, err := db.LastDeliveredProactiveAnalysis(ctx, date, daytimeSleepAnalysisRule)
	if err != nil || delivered == nil {
		t.Fatalf("new analysis not archived: %v", err)
	}
	detail, err := db.GetReportHistory(ctx, delivered.ReportID)
	if err != nil || !strings.Contains(detail.PlainBody, "6 h → 7 h 30 min") {
		t.Fatalf("change not explained: %+v %v", detail, err)
	}
	original, err := db.GetReportHistory(ctx, morning.ID)
	if err != nil || original.PlainBody != initial.Text {
		t.Fatal("original analysis overwritten")
	}
	var saved map[string]any
	if json.Unmarshal(delivered.Context, &saved) != nil || saved["supersedes_report_id"] != morning.ID.String() {
		t.Fatalf("revision does not link to original: %s", delivered.Context)
	}
	setNight(7.5, "identical-new-sync", true)
	maybeFireDaytime(ctx, bot, db, cfg, "", now.Add(61*time.Minute), load)
	if calls != 1 {
		t.Fatal("finality or sync hash alone sent a correction")
	}
	setNight(8, "another-real-change", false)
	ambiguous = true
	maybeFireDaytime(ctx, bot, db, cfg, "", now.Add(62*time.Minute), load)
	maybeFireDaytime(ctx, bot, db, cfg, "", now.Add(63*time.Minute), load)
	if calls != 2 {
		t.Fatalf("ambiguous correction repeated or disappeared: %d", calls)
	}
}
