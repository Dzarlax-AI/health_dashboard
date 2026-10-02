package storage

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"health-receiver/internal/ai"
)

type stalledMorningTestProvider struct {
	id      string
	entered chan struct{}
	stopped chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (p *stalledMorningTestProvider) Descriptor() ai.ProviderDescriptor {
	return ai.ProviderDescriptor{ID: p.id, DefaultModel: "synthetic"}
}
func (p *stalledMorningTestProvider) ListModels(context.Context, string) ([]ai.Model, error) {
	return nil, nil
}
func (p *stalledMorningTestProvider) Generate(ctx context.Context, _ ai.ProviderConfig, _ ai.GenerationRequest) (ai.GenerationResult, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.entered) })
	<-ctx.Done()
	close(p.stopped)
	return ai.GenerationResult{}, ctx.Err()
}

var stalledMorningProviderSequence atomic.Int32

// Exercise the real async orchestration and DB writes while a provider is
// stalled. The transport is entirely synthetic and cancellation is explicit.
func TestMorningAsyncProviderStallKeepsCheckinPersistenceAvailable(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	stalledMorningProvider := &stalledMorningTestProvider{id: fmt.Sprintf("morning-stall-integration-test-%d", stalledMorningProviderSequence.Add(1)), entered: make(chan struct{}), stopped: make(chan struct{})}
	ai.RegisterProvider(stalledMorningProvider)
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(context.Background(), `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,sample_count,min_val,max_val) VALUES('step_count',$1,'synthetic',10,1,10,10)`, today+" 08"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(context.Background(), `INSERT INTO daily_scores(date,sleep_total,hrv_avg,steps) VALUES($1,7.5,55,10)`, today); err != nil {
		t.Fatal(err)
	}
	cfg := AIConfig{Provider: stalledMorningProvider.Descriptor().ID, Providers: map[string]AIProviderSettings{stalledMorningProvider.Descriptor().ID: {APIKey: "synthetic-only"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !db.EnsureTodayAIInsightAsyncContext(ctx, cfg, "en") {
		t.Fatal("generation did not start")
	}
	select {
	case <-stalledMorningProvider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider was not reached")
	}
	if !db.AIRegenInFlight("en") {
		t.Fatal("stall lost single-flight reservation")
	}
	if db.EnsureTodayAIInsightAsyncContext(ctx, cfg, "en") {
		t.Fatal("second generation started during stall")
	}
	done := make(chan error, 1)
	go func() {
		if err := db.SaveCheckinPrompted(today, CheckinSourceTelegram, 123, now, now.Add(2*time.Hour)); err != nil {
			done <- err
			return
		}
		_, err := db.SaveCheckinAnswer(today, CheckinSourceTelegram, "ok", now.Add(time.Minute))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled provider blocked check-in persistence")
	}
	row, err := db.GetTodayCheckin(today, CheckinSourceTelegram)
	if err != nil || row == nil || row.Status != CheckinStatusAnswered {
		t.Fatalf("check-in did not persist: %#v %v", row, err)
	}
	cancel()
	select {
	case <-stalledMorningProvider.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("provider ignored cancellation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for db.AIRegenInFlight("en") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if db.AIRegenInFlight("en") {
		t.Fatal("canceled generation did not release single-flight")
	}
	if stalledMorningProvider.calls.Load() != 1 {
		t.Fatal("single-flight called provider more than once")
	}
	if len(db.GetAIBlocksFull(today, "en")) != 0 {
		t.Fatal("failed generation persisted a bundle")
	}
}
