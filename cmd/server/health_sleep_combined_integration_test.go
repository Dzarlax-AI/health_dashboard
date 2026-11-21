package main

import (
	"context"
	"encoding/json"
	"math"
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

	"health-receiver/internal/applehealth"
	"health-receiver/internal/handler"
	"health-receiver/internal/health"
	"health-receiver/internal/sleepwire"
	"health-receiver/internal/storage"
)

func TestHealthSleepSwiftAndArchiveIntegration(t *testing.T) {
	dir := os.Getenv("HEALTH_SLEEP_COMBINED_FIXTURES")
	if dir == "" {
		t.Skip("set HEALTH_SLEEP_COMBINED_FIXTURES to the Swift fixture directory")
	}
	readPacket := func(name string) []sleepwire.Candidate {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var frame struct {
			Data struct {
				Candidates struct {
					Items []struct {
						SourceID  string `json:"source_id"`
						Candidate sleepwire.Candidate
					} `json:"candidates"`
				} `json:"sleep_candidates_v1"`
			}
		}
		if err := json.Unmarshal(body, &frame); err != nil {
			t.Fatal("invalid Swift fixture shape")
		}
		entries := frame.Data.Candidates.Items
		candidates := make([]sleepwire.Candidate, 0, len(entries))
		for _, entry := range entries {
			if entry.Candidate.Coverage == nil || entry.Candidate.Coverage.Revision != entry.Candidate.Revision ||
				entry.Candidate.Coverage.SourceID != entry.SourceID {
				t.Fatal("Swift fixture did not bind coverage to identity and revision")
			}
			candidates = append(candidates, entry.Candidate)
		}
		return candidates
	}
	positives := readPacket("initial")
	tombstones := readPacket("withdrawal")
	if len(tombstones) != 1 || tombstones[0].Withdrawal == nil || len(tombstones[0].Stages) != 0 {
		t.Fatal("Swift did not emit one interval-free withdrawal")
	}
	const date = "2026-08-30"
	day, _ := time.Parse(time.DateOnly, date)
	clock := func() time.Time { return day.Add(22 * time.Hour) }
	var liveStage sleepwire.Stage
	var watchSource, ringSource string
	for _, c := range positives {
		for _, s := range c.Stages {
			if !s.Start.Equal(day) || s.Kind != "core" {
				continue
			}
			switch s.End.Sub(s.Start) {
			case 332 * time.Minute:
				liveStage, ringSource = s, c.Coverage.SourceID
			case 120 * time.Minute:
				watchSource = c.Coverage.SourceID
			}
		}
	}
	if liveStage.Start.IsZero() || watchSource == "" || watchSource == ringSource {
		t.Fatal("common episode and competing device must have distinct sources")
	}
	priority, err := json.Marshal([]string{watchSource, ringSource})
	if err != nil {
		t.Fatal(err)
	}

	mgr, root := newSleepRolloutFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	mux := http.NewServeMux()
	ingest := handler.New(mgr, nil, health.HRZones{})
	workerCtx, cancelWorker := context.WithCancel(ctx)
	cancelWorker()
	registerHealthIngest(workerCtx, mux, mgr, ingest, true)
	defer ingest.Shutdown(context.Background())
	server := httptest.NewServer(mux)
	defer server.Close()
	const fullArchive = `<HealthData><ExportDate value="2026-08-30 18:00:00 +0000"/>
	<Record type="HKCategoryTypeIdentifierSleepAnalysis" sourceName="Fixture Ring" value="HKCategoryValueSleepAnalysisAsleepCore" startDate="2026-08-30 02:00:00 +0200" endDate="2026-08-30 07:32:00 +0200"/>
</HealthData>`
	const partialArchive = `<HealthData><ExportDate value="2026-08-31 18:00:00 +0000"/>
	<Record type="HKQuantityTypeIdentifierStepCount" sourceName="Fixture Ring" unit="count" value="20" startDate="2026-08-30 02:00:00 +0200" endDate="2026-08-30 02:03:00 +0200"/>
</HealthData>`
	for _, fixture := range []struct {
		name, schema, key string
		archiveFirst      bool
	}{
		{"archive before Swift", "rollout_a", "rollout_a-key", true},
		{"Swift before archive", "rollout_b", "rollout_b-key", false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			db, _, _, ok := mgr.DBForAPIKey(ctx, fixture.key)
			if !ok {
				t.Fatal("synthetic tenant unavailable")
			}
			if err := db.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "prepare", "timezone": "UTC",
				"sleep_rollout_source_priority": string(priority)}); err != nil {
				t.Fatal(err)
			}
			execSQL := func(sql string, args ...any) {
				t.Helper()
				if _, err := root.Exec(ctx, strings.ReplaceAll(sql, "fixture_schema", fixture.schema), args...); err != nil {
					t.Fatal(err)
				}
			}
			execSQL(`INSERT INTO fixture_schema.daily_scores (date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories)
			 SELECT ($1::text::date+i)::text,7,1,1,0,500 FROM generate_series(-35,0) i`, date)
			stamp := day.Add(8 * time.Hour)
			if err := db.UpsertEnergySnapshotAt(ctx, "UTC", stamp, storage.BankResult{Bank: 51, FormulaVersion: 3}); err != nil {
				t.Fatal(err)
			}
			observation := func() string {
				t.Helper()
				var out string
				if err := root.QueryRow(ctx, `SELECT to_jsonb(s)::text||':'||xmin FROM `+fixture.schema+`.energy_snapshots s WHERE ts_bucket=$1`, stamp).Scan(&out); err != nil {
					t.Fatal(err)
				}
				return out
			}
			before := observation()
			send := func(name string, status int) {
				t.Helper()
				command := exec.CommandContext(ctx, filepath.Join(dir, "emit"), "--send",
					filepath.Join(dir, name+".json"), server.URL, strconv.Itoa(status))
				command.Env = append(os.Environ(), "FIXTURE_AUTH="+fixture.key)
				if err := command.Run(); err != nil {
					t.Fatalf("Swift transport did not verify the outcome for %s (expected HTTP %d): %v", name, status, err)
				}
			}
			drain := func(want int) {
				t.Helper()
				if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
					t.Fatalf("ordinary work = %v, %v", ok, err)
				}
				n := 0
				for ; n <= want; n++ {
					ok, err := db.RunSleepRolloutNext(ctx, clock)
					if err != nil {
						t.Fatal(err)
					}
					if !ok {
						break
					}
				}
				if n != want {
					t.Fatalf("applied revisions = %d, want %d", n, want)
				}
			}
			importArchive := func(xml string, full bool) {
				t.Helper()
				var records []applehealth.ArchiveRecord
				var points []storage.MetricPoint
				err := applehealth.ParseXMLWithOptions(strings.NewReader(xml), applehealth.EmitOptions{
					Records: func(r applehealth.ArchiveRecord) { records = append(records, r) },
					Points:  func(ps []storage.MetricPoint) { points = append(points, ps...) },
				})
				if err != nil || len(records) != 1 {
					t.Fatalf("archive observations = %d, parse error=%v", len(records), err != nil)
				}
				if full {
					record := records[0]
					if !record.Accepted || record.Identifier != "HKCategoryTypeIdentifierSleepAnalysis" || record.SourceName != "Fixture Ring" ||
						!record.Start.Equal(liveStage.Start) || !record.End.Equal(liveStage.End) || record.SnapshotAt.IsZero() {
						t.Fatal("XML projection failed to preserve the shared interval and original provenance")
					}
					_, offset := record.Start.Zone()
					if offset != 7200 {
						t.Fatal("XML interval offset was lost")
					}
				}
				session, err := db.BeginAppleHealthXMLImport(storage.ImportOptions{SnapshotAt: records[0].SnapshotAt, UnverifiedCoverage: true, CatalogNormalized: true})
				if err != nil {
					t.Fatal(err)
				}
				if err := session.AddPoints(points); err != nil {
					session.Abort(err)
					t.Fatal(err)
				}
				counters, err := session.Commit()
				if err != nil || counters.DeletedPoints != 0 {
					t.Fatalf("archive commit error=%v and deletes=%d", err != nil, counters.DeletedPoints)
				}
			}
			projection := func(state string, minutes, nap int) {
				t.Helper()
				var got string
				var sleep, additional int
				err := root.QueryRow(ctx, `SELECT episode_selection->>'state',COALESCE(sleep_seconds/60,-1)::int,
				COALESCE((episode_selection->>'additional_seconds')::double precision/60,-1)::int
				FROM `+fixture.schema+`.sleep_rollout_daily WHERE wake_date=$1::text::date`, date).Scan(&got, &sleep, &additional)
				if err != nil || got != state || sleep != minutes || additional != nap {
					t.Fatalf("prepared selection %q %d %d; error=%v", got, sleep, additional, err != nil)
				}
			}
			if fixture.archiveFirst {
				importArchive(fullArchive, true)
				var rows int
				if err := root.QueryRow(ctx, `SELECT count(*) FROM `+fixture.schema+`.sleep_rollout_packets`).Scan(&rows); err != nil || rows != 0 {
					t.Fatal("archive must not create device proof")
				}
				send("withdrawal", http.StatusConflict)
			}
			send("initial", http.StatusOK)
			drain(len(positives))
			projection("measured", 332, 136)
			if !fixture.archiveFirst {
				importArchive(fullArchive, true)
			}
			importArchive(partialArchive, false)
			importArchive(strings.Replace(partialArchive, `<ExportDate value="2026-08-31 18:00:00 +0000"/>`, "", 1), false)
			projection("measured", 332, 136)
			send("withdrawal", http.StatusOK)
			drain(1)
			projection("unknown", -1, -1)
			send("initial", http.StatusOK)
			send("withdrawal", http.StatusOK)
			importArchive(fullArchive, true)
			projection("unknown", -1, -1)
			var bank storage.BankResult
			var readiness storage.LocalShadowReadiness
			if err := root.QueryRow(ctx, `SELECT energy,readiness FROM `+fixture.schema+`.sleep_rollout_results WHERE day=$1::text::date`, date).Scan(&bank, &readiness); err != nil {
				t.Fatal(err)
			}
			if readiness.Sleep == nil || readiness.Sleep.Total != nil || !slices.Contains(bank.Flags, "imputed_sleep") || observation() != before {
				t.Fatal("withdrawal did not repair dependents while preserving the observed snapshot")
			}
			legacy, err := db.GetSleepSummary(date, date)
			if err != nil || len(legacy) != 1 || math.Abs(legacy[0].Total*60-332) > 0.01 {
				t.Fatalf("expected the Go reader to expose unreconciled archive data; rows=%d error=%v", len(legacy), err != nil)
			}
			// Raw deletion alone is insufficient. Both the inline and
			// historical paths deliberately retain a prior complete block.
			execSQL(`DELETE FROM fixture_schema.metric_points WHERE metric_name LIKE 'sleep%' AND substring(date,1,10)=$1`, date)
			if err := db.RebuildHistoricalCacheDate(ctx, date); err != nil {
				t.Fatal(err)
			}
			legacyBank, err := db.ComputeBankForDate(ctx, "UTC", date)
			if err != nil || slices.Contains(legacyBank.Flags, "imputed_sleep") {
				t.Fatal("fixture should demonstrate a stale ordinary EnergyBank input after raw deletion alone")
			}
			// Explicitly reconcile the derived block in the disposable
			// profile. This is a fixture operation, not a deployable rollback.
			execSQL(`UPDATE fixture_schema.daily_scores SET sleep_total=NULL, sleep_deep=NULL, sleep_rem=NULL,
				sleep_core=NULL, sleep_awake=NULL, sleep_unspecified=NULL WHERE date=$1`, date)
			if err := db.RecomputeReadinessSince(date); err != nil {
				t.Fatal(err)
			}
			legacy, err = db.GetSleepSummary(date, date)
			if err != nil || len(legacy) != 0 || observation() != before {
				t.Fatal("Go did not converge after ordinary fixture inputs were reconciled")
			}
			ordinaryBank, err := db.ComputeBankForDate(ctx, "UTC", date)
			if err != nil || !slices.Contains(ordinaryBank.Flags, "imputed_sleep") {
				t.Fatal("ordinary EnergyBank did not restore missing-sleep policy after reconciliation")
			}
			var cachedSleep *float64
			if err := root.QueryRow(ctx, `SELECT sleep_total FROM `+fixture.schema+`.daily_scores WHERE date=$1`, date).Scan(&cachedSleep); err != nil || cachedSleep != nil {
				t.Fatal("ordinary readiness input still contains sleep after reconciliation")
			}
		})
	}
}
