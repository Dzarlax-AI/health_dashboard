package notify

import (
	"context"
	"fmt"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestDaytimeWindowAndDefaultOff(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		utc  string
		want bool
	}{{"2026-03-29T06:59:00Z", false}, {"2026-03-29T07:00:00Z", true}, {"2026-03-29T19:00:00Z", false}, {"2026-10-25T08:00:00Z", true}, {"2026-10-25T20:00:00Z", false}} {
		at, e := time.Parse(time.RFC3339, tt.utc)
		if e != nil {
			t.Fatal(e)
		}
		if got := daytimeWindow(at, loc, 9, 21); got != tt.want {
			t.Errorf("%s got %v", tt.utc, got)
		}
	}
	for _, tt := range []struct {
		hour int
		want bool
	}{{21, false}, {22, true}, {0, true}, {6, true}, {7, false}} {
		if got := daytimeWindow(time.Date(2026, 11, 9, tt.hour, 0, 0, 0, time.UTC), time.UTC, 22, 7); got != tt.want {
			t.Errorf("overnight hour %d got %v", tt.hour, got)
		}
	}
	// A disabled tenant must return before touching a DB, bot or provider.
	MaybeFireDaytime(context.Background(), nil, nil, Config{}, "", time.Now())
}

func TestDaytimeMessageUsesFinalizationTime(t *testing.T) {
	finalized := time.Date(2026, 11, 9, 10, 15, 0, 0, time.UTC)
	c := &daytimeCandidate{event: storage.ProactiveEventCandidate{PreviousConclusion: "moderate", CurrentConclusion: "push_hard"}, decision: &health.DailyDecision{Label: "Push hard"}, sleep: health.CompletedNightSleep{FinalizedAt: &finalized}}
	msg := formatDaytimeGuidance(c, Config{Timezone: "UTC", Lang: "en"}, "", finalized.Add(2*time.Hour))
	if !strings.Contains(msg, "2026-11-09 10:15") || strings.Contains(msg, "12:15") {
		t.Fatalf("wrong factual time: %q", msg)
	}
}

func TestDaytimeGuidanceRequiresSleepCause(t *testing.T) {
	archived := archivedGuidanceContext{Decision: &health.DailyDecision{Mode: "moderate"}, Basis: map[string]string{"rule_version": "dashboard-guidance-caps-v1", "energy_action": "push_hard", "readiness_status": health.ReadinessServingFresh, "readiness_confidence": health.ReadinessConfidenceFinal, "sleep_confidence": health.SleepQualityConfidenceLow}}
	current := &health.BriefingResponse{DailyDecision: &health.DailyDecision{Mode: "push_hard"}, SleepQuality: &health.SleepQualityBreakdown{Confidence: health.SleepQualityConfidenceFinal}}
	if !sleepChangedGuidance(archived, current, "en") {
		t.Fatal("final sleep should release the archived cap")
	}
	current.DailyDecision.Mode = "active_recovery"
	if sleepChangedGuidance(archived, current, "en") {
		t.Fatal("energy-only movement must not trigger")
	}
	current.DailyDecision.Mode = "push_hard"
	archived.Basis["energy_action"] = "moderate"
	if sleepChangedGuidance(archived, current, "en") {
		t.Fatal("missing causal counterfactual must not trigger")
	}
	archived.Basis["rule_version"] = "other"
	if sleepChangedGuidance(archived, current, "en") {
		t.Fatal("unknown rule version must not trigger")
	}
}

func TestDaytimeEvaluatorSendsFreshFinalSleepOnce(t *testing.T) {
	dsn := testdb.DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := testdb.SchemaName("daytime_eval")
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	if err = testdb.CreateSchema(ctx, bootstrap, schema); err != nil {
		t.Fatal(err)
	}
	defer testdb.DropSchema(context.Background(), bootstrap, schema)
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	db := storage.NewFromPool(pool)
	defer db.Close()
	if err = db.EnsureAllTables(); err != nil {
		t.Fatal(err)
	}
	if err = db.EnsureReportHistoryTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.EnsureProactiveEventsTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.EnsureCompletedNightSleepTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	date := now.Format("2006-01-02")
	key := "report:morning:" + date
	contextJSON := []byte(`{"decision":{"id":"old","mode":"moderate"},"sleep":{"input_hash":"old-night"},"guidance_basis":{"rule_version":"dashboard-guidance-caps-v1","energy_action":"push_hard","readiness_status":"fresh","readiness_confidence":"final","sleep_confidence":"low"}}`)
	morning := &storage.ReportHistory{DeliveryKey: &key, Kind: "morning", LocalDate: date, Timezone: "UTC", Language: "en", PlainBody: "morning", DecisionID: "old", Context: contextJSON}
	token, ok, err := db.ReserveArchivedReport(ctx, morning)
	if err != nil || !ok {
		t.Fatalf("morning: %v %v", ok, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE report_history SET prepared_at=$2 WHERE id=$1`, morning.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	aid, err := db.ReserveReportAttempt(ctx, morning.ID, "html", "morning", token)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteReportAttempt(ctx, aid, "sent", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteNotificationDelivery(ctx, key, token, "sent", ""); err != nil {
		t.Fatal(err)
	}
	finalized := now.Add(-30 * time.Minute)
	night := health.CompletedNightSleep{WakeDate: date, DurationHours: 7.5, Source: "synthetic", SourceEpoch: "synthetic", InputHash: "new-night", AlgorithmVersion: "v1", CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: finalized, FinalizedAt: &finalized}
	if err = db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":21}}`)
	}))
	defer srv.Close()
	oldBase := telegramAPIBase
	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = oldBase }()
	cfg := Config{Token: "fixture", ChatID: "fixture", Timezone: "UTC", Lang: "en", DaytimeEnabled: true, DaytimeStartHour: 9, DaytimeEndHour: 21}
	bot := NewBot("fixture", "fixture")
	briefing := &health.BriefingResponse{Date: date, DailyDecision: &health.DailyDecision{ID: "new", Mode: "push_hard", Label: "Push hard"}, SleepQuality: &health.SleepQualityBreakdown{Confidence: health.SleepQualityConfidenceFinal}}
	load := func() (*health.BriefingResponse, error) { return briefing, nil }
	maybeFireDaytime(ctx, bot, db, cfg, "", now, load)
	maybeFireDaytime(ctx, bot, db, cfg, "", now.Add(time.Minute), load)
	maybeFireDaytime(ctx, bot, db, cfg, "", now.Add(24*time.Hour), load)
	if calls != 1 {
		t.Fatalf("calls=%d, expected one", calls)
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM notification_deliveries WHERE delivery_key LIKE 'proactive:daytime:%'`).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}
