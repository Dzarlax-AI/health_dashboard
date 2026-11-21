package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/handler"
	"health-receiver/internal/health"
	"health-receiver/internal/sleepwire"
	"health-receiver/internal/storage"
)

func TestHealthSleepEarlyOrdinarySync(t *testing.T) {
	manager, root := newSleepRolloutFixture(t)
	db, _, _, ok := manager.DBForAPIKey(t.Context(), "rollout_a-key")
	if !ok {
		t.Fatal("synthetic profile unavailable")
	}
	if err := db.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "prepare", "timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 2)
	ingest := handler.New(manager, func(_ *storage.DB, _ []string, cacheReady bool) {
		ready <- cacheReady
	}, health.HRZones{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ingest.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	mux := http.NewServeMux()
	registerHealthIngest(t.Context(), mux, manager, ingest, true)

	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, time.UTC)
	date := day.Format(time.DateOnly)
	send := func(napMinutes int) {
		t.Helper()
		napHours := float64(napMinutes) / 60
		sample := func(at time.Time, value float64) map[string]any {
			return map[string]any{"date": at.Format("2006-01-02 15:04:05 -0700"), "qty": value, "source": "fixture-device"}
		}
		metric := func(name string, data ...map[string]any) map[string]any {
			return map[string]any{"name": name, "units": "hr", "data": data}
		}
		core := []map[string]any{sample(day.Add(time.Hour), 4)}
		if napMinutes > 0 {
			core = append(core, sample(day.Add(14*time.Hour), napHours))
		}
		summary := sample(day, 0)
		summary["totalSleep"], summary["core"], summary["deep"], summary["rem"] = 7+napHours, 4+napHours, 1.5, 1.5
		metrics := []map[string]any{
			metric("sleep_analysis", summary),
			metric("night_sleep_total", sample(day, 7)),
			metric("sleep_core", core...),
			metric("sleep_deep", sample(day.Add(5*time.Hour), 1.5)),
			metric("sleep_rem", sample(day.Add(6*time.Hour+30*time.Minute), 1.5)),
		}
		if napMinutes > 0 {
			metrics = append(metrics, metric("nap_total", sample(day, napHours)))
		}
		body, err := json.Marshal(map[string]any{"data": map[string]any{"metrics": metrics}})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(string(body)))
		request.Header.Set("X-API-Key", "rollout_a-key")
		result := httptest.NewRecorder()
		mux.ServeHTTP(result, request)
		if result.Code != http.StatusOK {
			t.Fatalf("ordinary sync returned HTTP %d", result.Code)
		}
		select {
		case cacheReady := <-ready:
			if !cacheReady {
				t.Fatal("ordinary sync did not refresh the cache")
			}
		case <-time.After(7 * time.Second):
			t.Fatal("ordinary sync did not notify the derived-state callback")
		}
	}
	briefing := func() *health.BriefingResponse {
		t.Helper()
		result, err := db.GetHealthBriefing("en")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	send(0)
	morning := briefing()
	if morning.Sleep == nil || morning.Sleep.LatestDate != date || morning.Sleep.LatestTotal == nil || *morning.Sleep.LatestTotal <= 0 {
		t.Fatal("ordinary morning sync did not supply sleep analysis")
	}
	send(40)
	afternoon := briefing()
	if !slices.ContainsFunc(afternoon.MetricCards, func(card health.MetricCard) bool {
		return card.Metric == "night_sleep_total" && card.Badge == "+40m nap"
	}) {
		t.Fatal("ordinary afternoon sync did not surface the nap badge")
	}
	var packets int
	if err := root.QueryRow(t.Context(), `SELECT COUNT(*) FROM rollout_a.sleep_rollout_packets`).Scan(&packets); err != nil || packets != 0 {
		t.Fatalf("ordinary analysis required a revision-bound packet: %v", err)
	}
}

// Test the current strict wire using variants of a packet produced by the
// Swift journal. The variants deliberately exercise unsupported transitions.
func TestHealthSleepEarlyWireCompatibility(t *testing.T) {
	fixtures := os.Getenv("HEALTH_SLEEP_COMBINED_FIXTURES")
	if fixtures == "" {
		t.Skip("set HEALTH_SLEEP_COMBINED_FIXTURES to the Swift fixture directory")
	}
	variants := writeEarlySleepWireVariants(t, fixtures)
	manager, root := newSleepRolloutFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ingest := handler.New(manager, nil, health.HRZones{})
	mux := http.NewServeMux()
	stopped, stop := context.WithCancel(ctx)
	stop()
	registerHealthIngest(stopped, mux, manager, ingest, true)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer ingest.Shutdown(context.Background())

	type scenario struct {
		name, schema string
		fullFirst    bool
	}
	for _, tc := range []scenario{{"early first", "rollout_a", false}, {"full first", "rollout_b", true}} {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.schema + "-key"
			db, _, _, ok := manager.DBForAPIKey(ctx, key)
			if !ok {
				t.Fatal("synthetic profile unavailable")
			}
			if err := db.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "prepare", "timezone": "UTC"}); err != nil {
				t.Fatal(err)
			}
			send := func(name string, status int) {
				t.Helper()
				command := exec.CommandContext(ctx, filepath.Join(fixtures, "emit"), "--send",
					filepath.Join(variants, name+".json"), server.URL, strconv.Itoa(status))
				command.Env = append(os.Environ(), "FIXTURE_AUTH="+key)
				if err := command.Run(); err != nil {
					t.Fatalf("%s did not receive HTTP %d: %v", name, status, err)
				}
			}
			work := func(want int) {
				t.Helper()
				if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
					t.Fatalf("ordinary stage: %v, %v", ok, err)
				}
				for i := 0; i <= want; i++ {
					applied, err := db.RunSleepRolloutNext(ctx, func() time.Time {
						return time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
					})
					if err != nil || applied != (i < want) {
						t.Fatalf("candidate stage at iteration %d: %v, %v", i, applied, err)
					}
				}
			}
			state := func(wantRevision int, wantWithdrawal bool) {
				t.Helper()
				var revision int
				var withdrawal bool
				if err := root.QueryRow(ctx, "SELECT revision,snapshot ? 'withdrawal' FROM "+tc.schema+".sleep_rollout_candidates").Scan(&revision, &withdrawal); err != nil ||
					revision != wantRevision || withdrawal != wantWithdrawal {
					t.Fatalf("unexpected current revision or withdrawal state: %v", err)
				}
			}

			send("observation", http.StatusBadRequest)
			if tc.fullFirst {
				send("full_next_revision", http.StatusOK)
				work(1)
				send("legacy_early", http.StatusOK)
				work(0)
			} else {
				send("legacy_early", http.StatusOK)
				work(1)
				state(1, false)
				var ordinary, covered bool
				if err := root.QueryRow(ctx, "SELECT candidate IS NOT NULL,episode_selection IS NOT NULL FROM "+tc.schema+".sleep_rollout_daily").Scan(&ordinary, &covered); err != nil || !ordinary || covered {
					t.Fatalf("coverage-free input did not use the legacy classifier: %v", err)
				}
				send("legacy_early", http.StatusOK)
				if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || ok {
					t.Fatalf("retry scheduled duplicated work: %v, %v", ok, err)
				}
				send("full_same_revision", http.StatusConflict)
				send("full_next_revision", http.StatusOK)
				work(1)
			}
			state(2, false)
			send("legacy_newer", http.StatusConflict)
			send("withdrawal_after_full", http.StatusOK)
			work(1)
			state(3, true)
			send("legacy_early", http.StatusOK)
			send("full_next_revision", http.StatusOK)
			if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || ok {
				t.Fatalf("replaying acknowledged batches scheduled work: %v, %v", ok, err)
			}
			state(3, true)
		})
	}
}

func writeEarlySleepWireVariants(t *testing.T, fixtures string) string {
	t.Helper()
	type candidateEntry struct {
		SourceID  string              `json:"source_id"`
		Candidate sleepwire.Candidate `json:"candidate"`
	}
	var source struct {
		Data struct {
			Batch struct {
				Items []candidateEntry `json:"candidates"`
			} `json:"sleep_candidates_v1"`
		} `json:"data"`
	}
	filename := filepath.Join(fixtures, "late.json")
	body, err := os.ReadFile(filename)
	stageID := "fixture-afternoon"
	if os.IsNotExist(err) {
		body, err = os.ReadFile(filepath.Join(fixtures, "initial.json"))
		stageID = "fixture-nap"
	}
	if err != nil || json.Unmarshal(body, &source) != nil {
		t.Fatal("could not read the Swift fixture")
	}
	index := slices.IndexFunc(source.Data.Batch.Items, func(entry candidateEntry) bool {
		return slices.ContainsFunc(entry.Candidate.Stages, func(stage sleepwire.Stage) bool { return stage.ID == stageID })
	})
	if index < 0 {
		t.Fatal("Swift fixture is missing the additional sleep episode")
	}
	item := source.Data.Batch.Items[index]
	full := item.Candidate
	if full.Coverage == nil || full.Revision != 1 {
		t.Fatal("the Swift fixture must have initial full coverage")
	}
	dir := t.TempDir()
	write := func(name string, candidate any) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"data": map[string]any{
			"metrics": []any{}, "sleep_candidates_v1": map[string]any{"batch_id": uuid.NewString(),
				"candidates": []any{map[string]any{"source_id": item.SourceID, "candidate": candidate}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	early := full
	early.Coverage = nil
	type observation struct {
		ObservedAt time.Time `json:"observed_at"`
		Start      time.Time `json:"start_at"`
		End        time.Time `json:"end_at"`
	}
	stageIndex := slices.IndexFunc(early.Stages, func(stage sleepwire.Stage) bool { return stage.ID == stageID })
	stage := early.Stages[stageIndex]
	readAt := stage.End.Add(20 * time.Minute)
	write("observation", struct {
		sleepwire.Candidate
		Observation observation `json:"observation"`
	}{Candidate: early, Observation: observation{ObservedAt: readAt, Start: stage.Start.Add(-time.Hour), End: readAt}})
	write("legacy_early", early)
	early.Revision = 3
	write("legacy_newer", early)
	write("full_same_revision", full)
	proof := *full.Coverage
	proof.Revision = 2
	full.Revision, full.Coverage = 2, &proof
	write("full_next_revision", full)
	date, err := full.WakeDate()
	if err != nil {
		t.Fatal(err)
	}
	proof.Revision = 3
	proof.ObservedAt = proof.ObservedAt.Add(time.Hour)
	full.Revision, full.Coverage, full.Stages = 3, &proof, nil
	full.Withdrawal = &sleepwire.Withdrawal{WakeDate: date, PreviousRevision: 2}
	write("withdrawal_after_full", full)
	return dir
}

func TestHealthSleepEarlyShadowDelivery(t *testing.T) {
	fixtures := os.Getenv("HEALTH_SLEEP_EARLY_WIRE_FIXTURES")
	if fixtures == "" {
		t.Skip("set HEALTH_SLEEP_EARLY_WIRE_FIXTURES to the early Swift fixture directory")
	}
	t.Setenv("HEALTH_SLEEP_V2_ENABLED", "1")
	t.Setenv("HEALTH_SLEEP_GO_PROJECTION_ENABLED", "1")
	t.Setenv("HEALTH_SLEEP_EARLY_OBSERVATIONS_ENABLED", "1")

	packet, err := os.ReadFile(filepath.Join(fixtures, "early-observation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data struct {
			Batch struct {
				Items []struct {
					Source    string              `json:"source_id"`
					Candidate sleepwire.Candidate `json:"candidate"`
				} `json:"candidates"`
			} `json:"sleep_candidates_v1"`
		} `json:"data"`
	}
	if err := json.Unmarshal(packet, &envelope); err != nil || len(envelope.Data.Batch.Items) != 1 {
		t.Fatal("expected one early episode from Swift")
	}
	item := envelope.Data.Batch.Items[0]
	if item.Candidate.Observation == nil {
		t.Fatal("missing early evidence")
	}
	day := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	wakeDate := day.Format(time.DateOnly)
	nextDate := day.AddDate(0, 0, 1).Format(time.DateOnly)
	for _, scenario := range []string{"early withdrawal", "early then confirmed", "confirmed then early", "late early receipt", "transition with pending observation", "disabled stores canonical selection only"} {
		t.Run(scenario, func(t *testing.T) {
			manager, root := newSleepRolloutFixture(t)
			ctx := t.Context()
			ingest := handler.New(manager, nil, health.HRZones{})
			mux := http.NewServeMux()
			stopped, stop := context.WithCancel(ctx)
			stop()
			registerHealthIngest(stopped, mux, manager, ingest, true)
			server := httptest.NewServer(mux)
			defer server.Close()
			defer ingest.Shutdown(context.Background())

			const schema = "rollout_a"
			const key = "rollout_a-key"
			db, _, _, ok := manager.DBForAPIKey(ctx, key)
			if !ok {
				t.Fatal("synthetic profile unavailable")
			}
			config, err := json.Marshal(map[string]any{"from": wakeDate, "through": nextDate, "sources": []string{item.Source}})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "prepare", "timezone": "UTC",
				storage.SleepGoProjectionSetting: string(config)}); err != nil {
				t.Fatal(err)
			}
			var tenant uuid.UUID
			if err := root.QueryRow(ctx, "SELECT tenant_id FROM "+schema+".__tenant_identity").Scan(&tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := root.Exec(ctx, "INSERT INTO "+schema+".daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories) "+
				"SELECT ($1::text::date+i)::text,7,1,1,0,500 FROM generate_series(-35,1)i", wakeDate); err != nil {
				t.Fatal(err)
			}
			send := func(name string, status int) {
				t.Helper()
				command := exec.CommandContext(ctx, filepath.Join(fixtures, "emit"), "--send",
					filepath.Join(fixtures, name+".json"), server.URL, strconv.Itoa(status))
				command.Env = append(os.Environ(), "FIXTURE_AUTH="+key)
				if err := command.Run(); err != nil {
					t.Fatalf("%s did not receive HTTP %d: %v", name, status, err)
				}
			}
			apply := func(at time.Time) {
				t.Helper()
				if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
					t.Fatalf("ordinary stage: %v %v", ok, err)
				}
				for i := 0; i < 12; i++ {
					ok, err := db.RunSleepRolloutNext(ctx, func() time.Time { return at })
					if err != nil {
						t.Fatal(err)
					}
					if !ok {
						return
					}
				}
				t.Fatal("synthetic inbox did not drain")
			}
			stampApplied := func(at time.Time) {
				t.Helper()
				// Preserve the real receive/apply path and put its wall clock on
				// the historical fixture timeline for as-of comparisons.
				if _, err := root.Exec(ctx, "UPDATE "+schema+".sleep_rollout_packets SET applied_at=$1 WHERE applied_at>$2",
					at, day.AddDate(0, 0, 2)); err != nil {
					t.Fatal(err)
				}
			}
			report := func(at time.Time) storage.EnergyNapShadowResult {
				t.Helper()
				out, err := db.CompareEnergyNapShadow(ctx, tenant, at)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			morning := day.Add(12*time.Hour + time.Minute)
			syncTime := day.Add(15 * time.Hour)
			if scenario == "disabled stores canonical selection only" {
				t.Setenv("HEALTH_SLEEP_EARLY_OBSERVATIONS_ENABLED", "")
			}
			send("early-baseline", http.StatusOK)
			apply(morning)
			stampApplied(morning)
			if scenario == "disabled stores canonical selection only" {
				var state string
				var experimentalFields bool
				if err := root.QueryRow(ctx, `SELECT episode_selection->>'state',
					episode_selection ?| ARRAY['energy_naps','energy_main_end','energy_main_zone','energy_main_min_seconds']
					FROM rollout_a.sleep_rollout_daily WHERE wake_date=$1::text::date`, wakeDate).
					Scan(&state, &experimentalFields); err != nil || state != "measured" || experimentalFields {
					t.Fatalf("expected measured selection without experimental fields: %s %v %v", state, experimentalFields, err)
				}
				send("early-observation", http.StatusBadRequest)
				return
			}
			if scenario == "transition with pending observation" {
				t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "1")
				reviewed, err := db.AuditSleepRolloutPreparation(ctx, tenant, wakeDate, nextDate)
				if err != nil || !reviewed.ProjectionReady || reviewed.VerifiedProjectionDates == 0 {
					t.Fatalf("canonical review not ready: %+v %v", reviewed, err)
				}
				send("early-observation", http.StatusOK)
				for _, active := range []bool{true, false} {
					publication, err := db.SwitchSleepRollout(ctx, tenant, wakeDate, nextDate, reviewed.Evidence, active)
					if err != nil || publication.Active != active {
						t.Fatalf("pending observation changed the canonical transition: active=%v err=%v", active, err)
					}
				}
				return
			}
			if scenario == "late early receipt" {
				next := day.Add(33 * time.Hour)
				send("early-observation", http.StatusOK)
				apply(next)
				stampApplied(next)
				if report(next).Episodes != 0 || report(syncTime).Episodes != 0 {
					t.Fatal("a delayed receipt credited the next day or rewrote the earlier comparison")
				}
				var count int
				if err := root.QueryRow(ctx, "SELECT count(*) FROM rollout_a.sleep_rollout_packets WHERE candidate ? 'observation' AND applied_at IS NOT NULL").Scan(&count); err != nil || count != 1 {
					t.Fatalf("late evidence was not kept: %v", err)
				}
				return
			}
			if scenario == "confirmed then early" {
				next := day.Add(36*time.Hour + time.Minute)
				send("early-confirmed", http.StatusOK)
				apply(next)
				stampApplied(next)
				send("early-observation", http.StatusOK)
				apply(next)
				var count int
				if err := root.QueryRow(ctx, "SELECT count(*) FROM rollout_a.sleep_rollout_packets WHERE candidate ? 'observation'").Scan(&count); err != nil || count != 0 {
					t.Fatalf("older early observation displaced covered revision: %v", err)
				}
				return
			}
			send("early-observation", http.StatusOK)
			apply(syncTime)
			stampApplied(syncTime)
			if before := report(syncTime.Add(-time.Second)); before.Episodes != 0 {
				t.Fatal("early observation anticipated its sync")
			}
			now := report(syncTime)
			if now.Status != "compared" || now.Episodes != 1 || now.Immediate == nil || now.Immediate.PointsBeforeLimit <= 0 {
				t.Fatalf("expected one early contribution, got %+v", now)
			}
			send("early-observation", http.StatusOK)
			if retry := report(syncTime); retry.Episodes != 1 || *retry.Immediate != *now.Immediate {
				t.Fatal("old receipt duplicated credit")
			}
			if scenario == "early withdrawal" {
				deletedAt := syncTime.Add(2 * time.Minute)
				send("early-delete", http.StatusOK)
				apply(deletedAt)
				stampApplied(deletedAt)
				send("early-observation", http.StatusOK)
				if got := report(deletedAt); got.Status != "compared" || got.Episodes != 0 || got.Immediate.SignedDelta != 0 {
					t.Fatal("early deletion failed to clear the credit")
				}
			} else {
				confirmedAt := day.Add(36*time.Hour + time.Minute)
				send("early-confirmed", http.StatusOK)
				apply(confirmedAt)
				stampApplied(confirmedAt)
				send("early-observation", http.StatusOK)
				if got := report(confirmedAt); got.Episodes != 0 {
					t.Fatal("next-day confirmation started new credit")
				}
				var confirmed int
				if err := root.QueryRow(ctx, "SELECT count(*) FROM "+schema+".sleep_rollout_candidates WHERE id::jsonb->>1=$1 AND revision=2", item.Candidate.ID).Scan(&confirmed); err != nil || confirmed != 1 {
					t.Fatalf("full coverage did not promote the same identity: %v", err)
				}
			}
		})
	}
}
