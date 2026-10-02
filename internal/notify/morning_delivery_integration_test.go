package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	for _, name := range []string{"overview", "sleep", "activity", "recovery", "recommendation"} {
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
	if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	if _, err := pool.Exec(ctx, `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,sample_count,min_val,max_val) VALUES('step_count',$1,'synthetic',10,1,10,10)`, today+" 08"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total,hrv_avg,steps) VALUES($1,7.5,55,10)`, today); err != nil {
		t.Fatal(err)
	}
	night := health.CompletedNightSleep{WakeDate: today, DurationHours: 7.5, Source: "synthetic", SourceEpoch: "synthetic", InputHash: "night-1", AlgorithmVersion: "v1", CaptureCompleteness: health.NightCapturePartial, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalProvisional, ClaimEligibility: health.NightClaimIneligible, ObservedAt: now}
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	provider := &controlledMorningProvider{id: fmt.Sprintf("notify-morning-test-%d", morningDeliveryTestSequence.Add(1)), entered: make(chan struct{}), release: make(chan struct{})}
	ai.RegisterProvider(provider)
	aiCfg := storage.AIConfig{Provider: provider.id, Providers: map[string]storage.AIProviderSettings{provider.id: {APIKey: "synthetic-only"}}}
	cfg := Config{Timezone: "UTC", Lang: "en", AIConfig: aiCfg}
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
	night.InputHash = "night-2"
	night.DurationHours = 7.75
	if err := db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
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
