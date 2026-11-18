package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

type controlledMorningProvider struct {
	id               string
	entered, release chan struct{}
	calls            atomic.Int32
}

func (p *controlledMorningProvider) Descriptor() ai.ProviderDescriptor {
	return ai.ProviderDescriptor{ID: p.id, DefaultModel: "synthetic"}
}
func (p *controlledMorningProvider) ListModels(context.Context, string) ([]ai.Model, error) {
	return nil, nil
}
func (p *controlledMorningProvider) Generate(ctx context.Context, _ ai.ProviderConfig, req ai.GenerationRequest) (ai.GenerationResult, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ai.GenerationResult{}, ctx.Err()
		}
	}
	fields := map[string]string{}
	props := req.ResponseSchema.Schema["properties"].(map[string]any)
	for _, name := range []string{"overview", "sleep", "activity", "recovery", "energy", "recommendation"} {
		fields[name] = "Synthetic observed context."
		if choices, ok := props[name].(map[string]any)["enum"].([]string); ok && len(choices) > 0 {
			fields[name] = choices[0]
		}
	}
	body, err := json.Marshal(fields)
	return ai.GenerationResult{Text: string(body)}, err
}

var morningDeliveryTestSequence atomic.Int32

func TestMorningDeliveryWaitsForCurrentAIAndDoesNotWaitForCheckin(t *testing.T) {
	dsn := testdb.DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := testdb.SchemaName("notify_morning_delivery")
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	if err := testdb.CreateSchema(ctx, bootstrap, schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := testdb.DropSchema(context.Background(), bootstrap, schema); err != nil {
			t.Error(err)
		}
	}()
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	db := storage.NewFromPool(pool)
	defer db.Close()
	if err := db.EnsureAllTables(); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateSchemaContract(); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(map[string]string{"timezone": "UTC", "sleep_duration_comparison_enabled": "true"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	if _, err := pool.Exec(ctx, `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,sample_count,min_val,max_val) VALUES('step_count',$1,'synthetic',10,1,10,10)`, today+" 08"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total,hrv_avg,steps) VALUES($1,6.8,55,10)`, today); err != nil {
		t.Fatal(err)
	}
	night := health.CompletedNightSleep{WakeDate: today, DurationHours: 6.8, Source: "synthetic", SourceEpoch: "synthetic", InputHash: "night-1", AlgorithmVersion: "v1", CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalProvisional, ClaimEligibility: health.NightClaimIneligible, ObservedAt: now}
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	for offset := 1; offset <= 7; offset++ {
		prior := night
		prior.WakeDate = now.AddDate(0, 0, -offset).Format(time.DateOnly)
		prior.ObservedAt = now.AddDate(0, 0, -offset)
		prior.DurationHours, prior.InputHash = 7.5, prior.WakeDate
		prior.FinalizationState, prior.ClaimEligibility = health.NightFinalFinal, health.NightClaimEligible
		if err := db.SaveCompletedNightSleep(ctx, prior, nil); err != nil {
			t.Fatal(err)
		}
	}
	briefing, err := db.GetHealthBriefing("en")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	fallback, blocks, err := db.MorningReportEvidence(ctx, storage.AIConfig{}, "en", today, briefing)
	t.Logf("morning evidence with comparison and AI disabled: %s", time.Since(started))
	if err != nil || len(blocks) != 0 || fallback.Comparison == nil || *fallback.Comparison.DeltaMinutes != -42 {
		t.Fatalf("fallback comparison: %+v, blocks=%d, err=%v", fallback.Comparison, len(blocks), err)
	}
	plain := formatMorning(briefing, nil, "en", time.UTC, freshness{}, false, fallback)
	rich := formatMorningRich(briefing, nil, "en", time.UTC, freshness{}, false, "", fallback)
	for _, body := range []string{plain, rich} {
		if !strings.Contains(body, "-42 min") || !strings.Contains(body, "Preliminary record") {
			t.Fatal("preparing without AI did not preserve the comparison")
		}
	}
	provider := &controlledMorningProvider{id: fmt.Sprintf("notify-morning-test-%d", morningDeliveryTestSequence.Add(1)), entered: make(chan struct{}), release: make(chan struct{})}
	ai.RegisterProvider(provider)
	aiCfg := storage.AIConfig{Provider: provider.id, Providers: map[string]storage.AIProviderSettings{provider.id: {APIKey: "synthetic-only"}}}
	cfg := Config{Timezone: "UTC", Lang: "en", AIConfig: aiCfg, TelegramRichMessages: true}
	var mu sync.Mutex
	var messages []string
	fakeTelegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		messages = append(messages, body.Text)
		id := len(messages)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, id)
	}))
	defer fakeTelegram.Close()
	oldBase := telegramAPIBase
	telegramAPIBase = fakeTelegram.URL
	defer func() { telegramAPIBase = oldBase }()
	bot := NewBot("synthetic-token", "synthetic-chat")
	assertPending := func() {
		t.Helper()
		attempted, reason, err := SendMorningSmartOpts(bot, db, cfg, MorningSendOpts{Force: true, RequireAI: true})
		if err != nil || attempted || reason != "ai_pending" {
			t.Fatalf("cold/stale send: attempted=%v reason=%s err=%v", attempted, reason, err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM notification_deliveries WHERE delivery_key=$1`, "report:morning:"+today).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("pending AI reserved report delivery")
		}
	}
	assertPending()
	db.EnsureTodayAIInsightAsyncContext(ctx, aiCfg, "en")
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("provider did not enter")
	}
	if err := SendCheckinPrompt(bot, db, "en", today, now, CheckinPromptExpiry(now)); err != nil {
		t.Fatal(err)
	}
	assertPending()
	priorDate := now.AddDate(0, 0, -7).Format(time.DateOnly)
	if _, err := pool.Exec(ctx, "UPDATE completed_night_sleep SET duration_hours=8.5 WHERE wake_date=$1", priorDate); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	waitDone := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for db.AIRegenInFlight("en") && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if db.AIRegenInFlight("en") {
			t.Fatal("generation did not finish")
		}
	}
	waitDone()
	assertPending() // The completed bundle described the previous input.
	db.EnsureTodayAIInsightContext(ctx, aiCfg, "en")
	attempted, _, err := SendMorningSmartOpts(bot, db, cfg, MorningSendOpts{Force: true, RequireAI: true})
	if err != nil || !attempted {
		t.Fatalf("current AI send: attempted=%v err=%v", attempted, err)
	}
	attempted, reason, err := SendMorningSmartOpts(bot, db, cfg, MorningSendOpts{Force: true, RequireAI: true})
	if err != nil || attempted || reason != "delivery_already_reserved" {
		t.Fatalf("duplicate send: attempted=%v reason=%s err=%v", attempted, reason, err)
	}
	items, err := db.ListReportHistory(ctx, storage.ReportHistoryQuery{Kind: "morning", From: today, To: today, Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("morning archive count=%d error=%v", len(items), err)
	}
	detail, err := db.GetReportHistory(ctx, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var archived struct {
		Sleep health.MorningReportSleep `json:"sleep"`
	}
	if err := json.Unmarshal(detail.Context, &archived); err != nil {
		t.Fatal(err)
	}
	fact := archived.Sleep.Comparison
	current := db.GetSleepDurationComparison(ctx, today)
	if fact == nil || current == nil || fact.FactVersion != current.FactVersion || fact.DeltaMinutes == nil || *fact.DeltaMinutes != -51 ||
		*fact.EligibleNights != 7 || fact.Finalization != health.NightFinalProvisional ||
		!strings.Contains(detail.PlainBody, "-51 min") || !strings.Contains(detail.RichBody, "-51 min") {
		t.Fatalf("prepared variants disagreed with shared comparison: %+v", fact)
	}
	if _, err := pool.Exec(ctx, "UPDATE completed_night_sleep SET duration_hours=7.5 WHERE wake_date=$1", priorDate); err != nil {
		t.Fatal(err)
	}
	current = db.GetSleepDurationComparison(ctx, today)
	if current.FactVersion == fact.FactVersion || *current.DeltaMinutes != -42 {
		t.Fatalf("correction did not create a new comparison: %+v", current)
	}
	unchanged, err := db.GetReportHistory(ctx, items[0].ID)
	if err != nil || string(unchanged.Context) != string(detail.Context) {
		t.Fatalf("late correction rewrote prior archive: %v", err)
	}
	revision, err := evaluateSleepAnalysisUpdate(ctx, db, cfg, now, func() (*health.BriefingResponse, error) {
		return db.GetHealthBriefing("en")
	})
	if err != nil || revision != nil {
		t.Fatalf("historical correction triggered analysis revision: %v %v", revision, err)
	}

	row, err := db.GetTodayCheckin(today, storage.CheckinSourceTelegram)
	if err != nil || row == nil || row.Status != storage.CheckinStatusPrompted {
		t.Fatalf("report depended on answering check-in: %#v %v", row, err)
	}
	mu.Lock()
	count := len(messages)
	mu.Unlock()
	if count != 2 || provider.calls.Load() != 2 {
		t.Fatalf("messages=%d provider_calls=%d; want one prompt, one report, two input generations", count, provider.calls.Load())
	}
}
