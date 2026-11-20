package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/handler"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"health-receiver/internal/ui"
)

func TestSleepRolloutServeAndRollback(t *testing.T) {
	t.Run("persisted rollback with serving enabled", func(t *testing.T) {
		testSleepRolloutServeAndRollback(t, false)
	})
	t.Run("rollback with serving disabled", func(t *testing.T) {
		testSleepRolloutServeAndRollback(t, true)
	})
}

func testSleepRolloutServeAndRollback(t *testing.T, disableServeBeforeRollback bool) {
	t.Helper()
	t.Setenv("HEALTH_SLEEP_V2_ENABLED", "1")
	t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "1")
	t.Setenv("REPORT_TZ", "UTC")
	mgr, root := newSleepRolloutFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	db, _, _, ok := mgr.DBForAPIKey(ctx, "rollout_a-key")
	if !ok {
		t.Fatal("isolated profile did not resolve")
	}
	read := func(query string, args ...any) string {
		t.Helper()
		var value string
		if err := root.QueryRow(ctx, query, args...).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := root.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	tenant := uuid.MustParse(read(`SELECT tenant_id::text FROM rollout_a.__tenant_identity`))
	if err := db.SaveSettings(map[string]string{"timezone": "UTC", storage.SleepRolloutModeSetting: "prepare",
		"sleep_rollout_source_priority": `["ring","watch"]`}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	first, old := today.AddDate(0, 0, -2), today.AddDate(0, 0, -1)
	firstDate, oldDate, todayDate := first.Format(time.DateOnly), old.Format(time.DateOnly), today.Format(time.DateOnly)
	exec(`INSERT INTO rollout_a.daily_scores
		(date,sleep_total,sleep_core,sleep_deep,sleep_rem,sleep_awake,hrv_avg,rhr_avg,calories)
		SELECT ($1::text::date+i)::text,8,5.8,1,1.2,0.3,55,57,550 FROM generate_series(-65,0) i`, todayDate)
	observedAt := now.Truncate(5 * time.Minute)
	historicalAt := old.Add(23*time.Hour + 55*time.Minute)
	for _, slot := range []time.Time{observedAt, historicalAt} {
		if err := db.UpsertEnergySnapshotAt(ctx, "UTC", slot, storage.BankResult{Bank: 73, FormulaVersion: 3}); err != nil {
			t.Fatal(err)
		}
	}
	observed := func() string {
		return read(`SELECT string_agg(to_jsonb(s)::text || ':' || xmin::text, '|' ORDER BY ts_bucket)
			FROM rollout_a.energy_snapshots s WHERE ts_bucket=$1 OR ts_bucket=$2`, observedAt, historicalAt)
	}
	initialObservations := observed()

	mux := http.NewServeMux()
	ingest := handler.New(mgr, nil, health.HRZones{})
	defer ingest.Shutdown(ctx)
	// The real router and handlers are used. The test advances the two
	// workers explicitly so their commit boundary is observable.
	paused, stop := context.WithCancel(ctx)
	stop()
	registerHealthIngest(paused, mux, mgr, ingest, true)
	ui.New(mgr, nil, false).Register(mux)
	request := func(method, path, key, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-API-Key", key)
		mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	sendBatch := func(body, expectedAck string) int64 {
		t.Helper()
		w := request("POST", "/health", "rollout_a-key", body, 200)
		if ack := w.Header().Get("X-Health-Sleep-Candidates-Ack"); ack != expectedAck {
			t.Fatalf("candidate ack %q, want %q", ack, expectedAck)
		}
		var reply struct{ ID int64 }
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.ID == 0 {
			t.Fatalf("bad receipt %s: %v", w.Body.String(), err)
		}
		return reply.ID
	}
	batch := func(day time.Time, source, candidate, zone string, revision int, hours float64) (string, string) {
		t.Helper()
		id := uuid.NewString()
		start := day.Add(16 * time.Hour)
		body, err := json.Marshal(map[string]any{"data": map[string]any{
			"metrics": []any{map[string]any{"name": "sleep_analysis", "units": "hr", "data": []any{map[string]any{
				"date": day.Format(time.DateOnly) + " 00:00:00 +0000", "source": "Legacy Watch",
				"totalSleep": 8, "core": 5.8, "deep": 1, "rem": 1.2, "awake": 0.3,
			}}}},
			"sleep_candidates_v1": map[string]any{"batch_id": id, "candidates": []any{map[string]any{
				"source_id": source, "candidate": map[string]any{"id": candidate, "revision": revision, "wake_zone": zone,
					"stages": []any{
						map[string]any{"id": "core", "kind": "core", "start_at": start, "end_at": start.Add(time.Duration(hours-2) * time.Hour)},
						map[string]any{"id": "deep", "kind": "deep", "start_at": start.Add(time.Duration(hours-2) * time.Hour), "end_at": start.Add(time.Duration(hours-1) * time.Hour)},
						map[string]any{"id": "rem", "kind": "rem", "start_at": start.Add(time.Duration(hours-1) * time.Hour), "end_at": start.Add(time.Duration(hours) * time.Hour)},
					}, "zero_stages": []string{"awake"}},
			}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return string(body), id
	}
	// With the derived worker paused, advance its durable completion
	// acknowledgement explicitly after the ordinary calculations commit.
	completeLegacy := func() {
		t.Helper()
		captured, err := db.CaptureCacheDirty(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CompleteCacheDirty(ctx, captured); err != nil {
			t.Fatal(err)
		}
	}
	apply := func() {
		t.Helper()
		if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
			t.Fatalf("ordinary part: %v, %v", ok, err)
		}
		if ok, err := db.RunSleepRolloutNext(ctx, func() time.Time { return now }); err != nil || !ok {
			t.Fatalf("candidate part: %v, %v", ok, err)
		}
	}
	briefing := func() health.BriefingResponse {
		t.Helper()
		var result health.BriefingResponse
		w := request("GET", "/api/health-briefing?lang=en", "rollout_a-key", "", 200)
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	sleepAndBank := func(wantSleep float64, wantBank int) health.BriefingResponse {
		t.Helper()
		result := briefing()
		if result.Sleep == nil || result.Sleep.LatestTotal == nil || *result.Sleep.LatestTotal != wantSleep {
			t.Fatalf("latest sleep %+v, want %g", result.Sleep, wantSleep)
		}
		shown := result.EnergyBankCurrent
		if shown == nil && result.EnergyBank != nil {
			shown = &result.EnergyBank.Current
		}
		if shown == nil || *shown != wantBank {
			t.Fatalf("current EnergyBank %v (%s), want %d", shown, result.EnergyBankStatus, wantBank)
		}
		if result.ReadinessServing == nil || !slices.ContainsFunc(result.ReadinessServing.Components,
			func(component health.ReadinessComponentSummary) bool {
				return component.Metric == "sleep_total" && component.Value != nil && *component.Value == wantSleep
			}) {
			t.Fatalf("readiness did not use the same sleep as the briefing: %+v", result.ReadinessServing)
		}
		return result
	}
	audit := func() storage.SleepRolloutAudit {
		t.Helper()
		result, err := db.AuditSleepRolloutPreparation(ctx, tenant, firstDate, todayDate)
		if err != nil || result.Evidence == "" {
			t.Fatalf("could not capture evidence: %+v, %v", result, err)
		}
		return result
	}
	prehistoryBody, prehistoryAck := batch(first.AddDate(0, 0, -1), "watch", "prehistory", "UTC", 1, 4)
	firstBody, firstAck := batch(first, "watch", "first", "UTC", 1, 7)
	oldBody, oldAck := batch(old, "ring", "moves", "UTC", 1, 6)
	todayBody, todayAck := batch(today, "watch", "other", "UTC", 1, 7)
	for _, packet := range [][2]string{{prehistoryBody, prehistoryAck}, {firstBody, firstAck}, {oldBody, oldAck}, {todayBody, todayAck}} {
		sendBatch(packet[0], packet[1])
		apply()
	}
	completeLegacy()
	sleepAndBank(8, 73)
	if before := audit(); before.MismatchedResults == 0 {
		t.Fatal("audit approved unbounded inputs before rebuilding the publication window")
	}
	exec(`INSERT INTO rollout_a.metric_points(health_record_id,metric_name,date,qty,source)
		SELECT MIN(id),'night_sleep_total',$1 || ' 00:00:00 +0000',9,'Legacy Watch' FROM rollout_a.health_records`, todayDate)
	// Rebuild can repair an interrupted preparation one date at a time.
	exec(`DELETE FROM rollout_a.sleep_rollout_results WHERE day=$1::text::date`, firstDate)
	if repaired, err := db.RebuildSleepRolloutPreparation(ctx, tenant, firstDate, todayDate); err != nil || repaired != 3 {
		t.Fatalf("repair prepared dates: %d, %v", repaired, err)
	}
	if repaired, err := db.RebuildSleepRolloutPreparation(ctx, tenant, firstDate, todayDate); err != nil || repaired != 0 {
		t.Fatalf("repair repeat: %d, %v", repaired, err)
	}
	report := audit()
	if report.MatchedDates != 3 || report.UnownedDates != 0 {
		t.Fatalf("unexpected preparation: %+v", report)
	}
	if err := db.SaveSettings(map[string]string{"sleep_rollout_switch_test": "changed after audit"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SwitchSleepRollout(ctx, tenant, firstDate, todayDate, report.Evidence, true); !errors.Is(err, storage.ErrSleepRolloutEvidenceChanged) {
		t.Fatalf("publication accepted obsolete evidence: %v", err)
	}
	publication, err := db.SwitchSleepRollout(ctx, tenant, firstDate, todayDate, audit().Evidence, true)
	if err != nil || !publication.Active || publication.Packet != 4 {
		t.Fatalf("serve: %+v, %v", publication, err)
	}
	preparedResult := func(date string) storage.BankResult {
		t.Helper()
		var result storage.BankResult
		if err := json.Unmarshal([]byte(read(`SELECT energy::text FROM rollout_a.sleep_rollout_results WHERE day=$1::text::date`, date)), &result); err != nil {
			t.Fatal(err)
		}
		if result.State == "stale" {
			t.Fatal("expected a numeric prepared bank")
		}
		return result
	}
	preparedBank := func(date string) int { return preparedResult(date).Display }
	// The current bank comes from the same served revision as readiness.
	original := sleepAndBank(7, preparedBank(todayDate))
	if !slices.ContainsFunc(original.MetricCards, func(card health.MetricCard) bool {
		return card.Metric == "night_sleep_total" && card.Value == "7.0"
	}) {
		t.Fatalf("sleep card did not replace legacy night duration: %+v", original.MetricCards)
	}
	t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "")
	legacyWhileDisabled, err := db.ComputeBankForDate(ctx, "UTC", todayDate)
	if err != nil || legacyWhileDisabled.State == "stale" {
		t.Fatalf("legacy bank with serve flag disabled: %+v, %v", legacyWhileDisabled, err)
	}
	sleepAndBank(8, legacyWhileDisabled.Display)
	t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "1")
	// A legacy-only date has no published row. It keeps the legacy sleep,
	// while an explicit cleared row still shadows it after a move.
	unpublishedDate := today.AddDate(0, 0, 1).Format(time.DateOnly)
	exec(`INSERT INTO rollout_a.metric_points(health_record_id,metric_name,date,qty,source)
		SELECT MIN(id),'sleep_total',$1 || ' 00:00:00 +0000',5,'Legacy Watch' FROM rollout_a.health_records`, unpublishedDate)
	legacyOnly, err := db.GetSleepSummary(unpublishedDate, unpublishedDate)
	if err != nil || len(legacyOnly) != 1 || legacyOnly[0].Total != 5 {
		t.Fatalf("legacy-only date after activation: %+v, %v", legacyOnly, err)
	}
	invalid := strings.Replace(todayBody, "00:00:00 +0000", "00:75:00 +0000", 1)
	invalid = strings.Replace(invalid, todayAck, uuid.NewString(), 1)
	request("POST", "/health", "rollout_a-key", invalid, 400)
	isolated := request("GET", "/api/health-briefing?lang=en", "rollout_b-key", "", 200)
	var peer health.BriefingResponse
	if err := json.Unmarshal(isolated.Body.Bytes(), &peer); err != nil || peer.Sleep != nil {
		t.Fatalf("unpublished profile saw another profile's sleep: %+v, %v", peer.Sleep, err)
	}

	moveBody, moveAck := batch(old, "ring", "moves", "Asia/Singapore", 2, 6)
	moveID := sendBatch(moveBody, moveAck)
	if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
		t.Fatalf("ordinary correction: %v, %v", ok, err)
	}
	// Fail the free-slot write, after both corrected dates have been updated
	// in the candidate transaction. Retrying then publishes all three writes.
	freeSlot := today.Add(12 * time.Hour)
	if freeSlot.Equal(observedAt) {
		freeSlot = freeSlot.Add(5 * time.Minute)
	}
	bothDates := func() string {
		return read(`SELECT string_agg(to_jsonb(d)::text||':'||d.xmin::text||'/'||to_jsonb(r)::text||':'||r.xmin::text,'|' ORDER BY d.wake_date)
			FROM rollout_a.sleep_rollout_daily d JOIN rollout_a.sleep_rollout_results r ON r.day=d.wake_date
			WHERE d.wake_date IN ($1::text::date,$2::text::date)`, oldDate, todayDate)
	}
	beforeMove := bothDates()
	exec(`CREATE FUNCTION rollout_a.reject_test_sleep_snapshot() RETURNS trigger LANGUAGE plpgsql
		AS $$ BEGIN RAISE EXCEPTION 'synthetic snapshot failure'; END $$`)
	exec(`CREATE TRIGGER reject_test_sleep_snapshot BEFORE INSERT ON rollout_a.energy_snapshots
		FOR EACH ROW EXECUTE FUNCTION rollout_a.reject_test_sleep_snapshot()`)
	if _, err := db.RunSleepRolloutNext(ctx, func() time.Time { return freeSlot }); err == nil || !strings.Contains(err.Error(), "synthetic snapshot failure") {
		t.Fatalf("expected injected snapshot failure: %v", err)
	}
	if bothDates() != beforeMove ||
		read(`SELECT COUNT(*)::text FROM rollout_a.sleep_rollout_packets WHERE health_record_id=$1 AND applied_at IS NULL`, moveID) != "1" ||
		read(`SELECT COUNT(*)::text FROM rollout_a.energy_snapshots WHERE ts_bucket=$1`, freeSlot) != "0" {
		t.Fatal("failure changed the old or new date, receipt or snapshot")
	}
	sleepAndBank(7, preparedBank(todayDate))
	if wake := read(`SELECT wake_date::text FROM rollout_a.sleep_rollout_candidates WHERE revision=1 AND id LIKE '%moves%'`); wake != oldDate {
		t.Fatalf("candidate moved despite snapshot failure: %s", wake)
	}
	exec(`DROP TRIGGER reject_test_sleep_snapshot ON rollout_a.energy_snapshots`)
	exec(`DROP FUNCTION rollout_a.reject_test_sleep_snapshot()`)
	if ok, err := db.RunSleepRolloutNext(ctx, func() time.Time { return freeSlot }); err != nil || !ok {
		t.Fatalf("retry candidate and snapshot: %v, %v", ok, err)
	}
	served := sleepAndBank(6, preparedBank(todayDate))
	newObservation := read(`SELECT to_jsonb(s)::text || ':' || xmin::text FROM rollout_a.energy_snapshots s WHERE ts_bucket=$1`, freeSlot)
	if got := read(`SELECT bank::text FROM rollout_a.energy_snapshots WHERE ts_bucket=$1`, freeSlot); got != strconv.Itoa(preparedResult(todayDate).Bank) {
		t.Fatalf("free slot contains the wrong bank: %s", newObservation)
	}
	nights, err := db.GetSleepSummary(oldDate, todayDate)
	if err != nil || len(nights) != 1 || nights[0].Date != todayDate || nights[0].Total != 6 {
		t.Fatalf("served sleep on both dates: %+v, %v", nights, err)
	}
	history, err := db.GetReadinessHistory(2)
	if err != nil || len(history) != 2 || history[0].Date != oldDate ||
		history[1].Date != todayDate || history[1].Score != served.ReadinessScore {
		t.Fatalf("served readiness history: %+v, %v", history, err)
	}
	// Even an unexpected missing row inside the audited range must not
	// resurrect the legacy night that the move cleared.
	exec(`DELETE FROM rollout_a.sleep_rollout_daily WHERE wake_date=$1::text::date`, oldDate)
	if nights, err := db.GetSleepSummary(oldDate, oldDate); err != nil || len(nights) != 0 {
		t.Fatalf("missing in-range night used legacy data: %+v, %v", nights, err)
	}
	if got := sendBatch(moveBody, moveAck); got != moveID {
		t.Fatalf("serve replay changed receipt from %d to %d", moveID, got)
	}
	if _, err := db.SwitchSleepRollout(ctx, tenant, firstDate, todayDate, report.Evidence, false); !errors.Is(err, storage.ErrSleepRolloutEvidenceChanged) {
		t.Fatalf("rollback accepted obsolete evidence: %v", err)
	}
	energy := storage.NewEnergyV2Orchestrator()
	if !energy.Refresh(ctx, db, "rollout_a", "UTC") || observed() != initialObservations {
		t.Fatal("serve rewrote historical observations")
	}
	hourly, err := db.GetEnergyHistoryV2(ctx, "UTC", 72)
	freeHistoricalSlot := first.Add(23*time.Hour + 55*time.Minute)
	if err != nil || !slices.ContainsFunc(hourly, func(point storage.EnergySnapshotPoint) bool {
		return point.TS.Equal(historicalAt) && point.Bank == 73 && !slices.Contains(point.Flags, "backfilled")
	}) || !slices.ContainsFunc(hourly, func(point storage.EnergySnapshotPoint) bool {
		return point.TS.Equal(freeHistoricalSlot) && slices.Contains(point.Flags, "backfilled")
	}) {
		t.Fatalf("historical observations or synthetic free slot missing: %+v, %v", hourly, err)
	}
	rendered, err := db.GetEnergyHistoryV2(ctx, "America/Los_Angeles", 72)
	if err != nil || !slices.ContainsFunc(rendered, func(point storage.EnergySnapshotPoint) bool {
		return point.TS.Equal(freeHistoricalSlot) && slices.Contains(point.Flags, "backfilled")
	}) {
		t.Fatalf("rendering timezone changed the synthetic observation instant: %+v, %v", rendered, err)
	}
	pendingBody, pendingAck := batch(today, "watch", "other", "UTC", 1, 7)
	pendingID := sendBatch(pendingBody, pendingAck)
	if _, err := db.SwitchSleepRollout(ctx, tenant, firstDate, todayDate, audit().Evidence, false); err == nil {
		t.Fatal("rollback escaped the still-pending legacy receipt")
	}
	if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
		t.Fatalf("catch up legacy receipt: %v, %v", ok, err)
	}
	if status := read(`SELECT processing_status FROM rollout_a.health_records WHERE id=$1`, pendingID); status != "complete" {
		t.Fatalf("legacy receipt status %s", status)
	}
	if report := audit(); report.PendingLegacyDates == 0 {
		t.Fatal("ordinary input should remain in the derived-state journal")
	} else if _, err := db.SwitchSleepRollout(ctx, tenant, firstDate, todayDate, report.Evidence, false); err == nil {
		t.Fatal("rollback escaped the durable legacy calculation backlog")
	}
	completeLegacy()
	if disableServeBeforeRollback {
		t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "")
	}
	publication, err = db.SwitchSleepRollout(ctx, tenant, firstDate, todayDate, audit().Evidence, false)
	if err != nil || publication.Active || publication.Packet != 5 {
		t.Fatalf("rollback: %+v, %v", publication, err)
	}
	legacyBank, err := db.ComputeBankForDate(ctx, "UTC", todayDate)
	if err != nil || legacyBank.State == "stale" {
		t.Fatalf("legacy bank after rollback: %+v, %v", legacyBank, err)
	}
	sleepAndBank(8, legacyBank.Display)
	if err := db.RefreshLegacyEnergyBankSnapshot("en"); err != nil {
		t.Fatal(err)
	}
	if got := read(`SELECT energy_eod_current::text FROM rollout_a.daily_scores WHERE date=$1`, todayDate); got != strconv.Itoa(legacyBank.Display) {
		t.Fatalf("legacy EOD differs from the rolled-back briefing: %s vs %d", got, legacyBank.Display)
	}
	nights, err = db.GetSleepSummary(oldDate, todayDate)
	if err != nil || len(nights) != 2 || nights[0].Total != 8 || nights[1].Total != 8 {
		t.Fatalf("rolled-back sleep on both dates: %+v, %v", nights, err)
	}
	if err := ingest.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	restarted := handler.New(mgr, nil, health.HRZones{})
	defer restarted.Shutdown(ctx)
	mux = http.NewServeMux()
	registerHealthIngest(paused, mux, mgr, restarted, true)
	ui.New(mgr, nil, false).Register(mux)
	if got := sendBatch(moveBody, moveAck); got != moveID || observed() != initialObservations ||
		read(`SELECT to_jsonb(s)::text || ':' || xmin::text FROM rollout_a.energy_snapshots s WHERE ts_bucket=$1`, freeSlot) != newObservation {
		t.Fatal("rollback or restart lost a packet or rewrote observations")
	}
	if got := sendBatch(pendingBody, pendingAck); got != pendingID {
		t.Fatal("rollback lost the receipt accepted during serve")
	}
	var graph struct {
		Points []storage.EnergySnapshotPoint `json:"points"`
	}
	response := request("GET", "/api/energy-history?granularity=hour&hours=72&timezone="+url.QueryEscape("UTC"), "rollout_a-key", "", 200)
	if err := json.Unmarshal(response.Body.Bytes(), &graph); err != nil || !slices.ContainsFunc(graph.Points,
		func(point storage.EnergySnapshotPoint) bool { return point.TS.Equal(historicalAt) && point.Bank == 73 }) {
		t.Fatalf("rollback energy history lost the observation: %+v, %v", graph.Points, err)
	}
	t.Logf("served both wake dates, restored legacy readers after restart and retained mixed receipt %d and observations", moveID)
}
