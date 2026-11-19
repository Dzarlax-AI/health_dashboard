package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/handler"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

func TestHealthSleepCandidatesMixedIngest(t *testing.T) {
	mgr, root := newSleepRolloutFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	a, _, _, ok := mgr.DBForAPIKey(ctx, "rollout_a-key")
	if !ok {
		t.Fatal("isolated profile did not resolve")
	}
	setMode := func(mode string) {
		t.Helper()
		if err := a.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: mode, "timezone": "UTC"}); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := root.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	read := func(query string, args ...any) string {
		t.Helper()
		var value string
		if err := root.QueryRow(ctx, query, args...).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	expect := func(want, query string, args ...any) {
		t.Helper()
		if got := read(query, args...); got != want {
			t.Fatalf("got %q, want %q for %s", got, want, query)
		}
	}
	wait := func(want, query string, args ...any) {
		t.Helper()
		until := time.Now().Add(7 * time.Second)
		for time.Now().Before(until) {
			if read(query, args...) == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		expect(want, query, args...)
	}
	open := func(enabled, runWorker bool) (*http.ServeMux, *handler.Handler, func()) {
		t.Helper()
		runCtx, stop := context.WithCancel(ctx)
		if !runWorker {
			stop()
		}
		ingest := handler.New(mgr, nil, health.HRZones{})
		mux := http.NewServeMux()
		registerHealthIngest(runCtx, mux, mgr, ingest, enabled)
		return mux, ingest, func() {
			t.Helper()
			stop()
			if err := ingest.Shutdown(ctx); err != nil {
				t.Error(err)
			}
		}
	}
	mux, ingest, stop := open(true, false)
	defer stop()
	type response struct {
		status int
		ack    string
		data   string
	}
	send := func(router *http.ServeMux, path, key, body string) response {
		out := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("X-API-Key", key)
		router.ServeHTTP(out, req)
		return response{out.Code, out.Header().Get("X-Health-Sleep-Candidates-Ack"), strings.TrimSpace(out.Body.String())}
	}
	post := func(router *http.ServeMux, key, body string) response {
		return send(router, "/health", key, body)
	}
	check := func(got response, status int, ack string) int64 {
		t.Helper()
		if got.status != status || got.ack != ack {
			t.Fatalf("response %+v, want status %d and ack %q", got, status, ack)
		}
		if status != http.StatusOK {
			return 0
		}
		var body struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(got.data), &body); err != nil || body.ID <= 0 || body.Status != "ok" {
			t.Fatalf("unexpected response body %q: %v", got.data, err)
		}
		return body.ID
	}

	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3)
	nextDay := day.AddDate(0, 0, 1)
	metricDate := day.Format(time.DateOnly) + " 00:00:00 +0000"
	legacy := fmt.Sprintf(`{"data":{"metrics":[{"name":"sleep_total","units":"hr","data":[{"date":%q,"qty":7,"source":"Legacy RingConn"}]}]}}`, metricDate)
	makeCandidate := func(id, source, zone string, revision int) map[string]any {
		start := day.Add(16 * time.Hour)
		return map[string]any{"source_id": source, "candidate": map[string]any{
			"id": id, "revision": revision, "wake_zone": zone,
			"stages": []map[string]any{{"id": "interval", "kind": "asleep_unspecified", "start_at": start, "end_at": start.Add(6 * time.Hour)}},
		}}
	}
	mixed := func(batch uuid.UUID, candidates ...map[string]any) string {
		t.Helper()
		var object map[string]map[string]any
		if err := json.Unmarshal([]byte(legacy), &object); err != nil {
			t.Fatal(err)
		}
		object["data"]["sleep_candidates_v1"] = map[string]any{"batch_id": batch.String(), "candidates": candidates}
		out, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	original := makeCandidate("night", "ring", "UTC", 1)
	check(post(mux, "rollout_a-key", legacy), 200, "")
	offBody := mixed(uuid.New(), original)
	check(post(mux, "rollout_a-key", offBody), 200, "")
	check(post(mux, "rollout_b-key", offBody), 200, "")
	check(post(mux, "wrong-key", offBody), 401, "")
	wait("2", `SELECT count(*)::text FROM rollout_a.health_records WHERE processing_status='complete'`)
	wait("1", `SELECT count(*)::text FROM rollout_b.health_records WHERE processing_status='complete'`)
	expect("0", `SELECT count(*)::text FROM rollout_a.sleep_rollout_batches`)
	expect("0", `SELECT count(*)::text FROM rollout_b.sleep_rollout_batches`)

	setMode("prepare")
	defaultMux, _, stopDefault := open(false, false)
	check(post(defaultMux, "rollout_a-key", offBody), 200, "")
	stopDefault()
	expect("0", `SELECT count(*)::text FROM rollout_a.sleep_rollout_batches`)
	check(post(mux, "rollout_a-key", mixed(uuid.New(), original, original)), 400, "")
	// A failed mode read must not silently fall back to legacy acceptance.
	modeProbe := mixed(uuid.New(), original)
	exec(`ALTER TABLE rollout_a.settings ALTER COLUMN value DROP NOT NULL;
		UPDATE rollout_a.settings SET value=NULL WHERE key='sleep_rollout_mode'`)
	check(post(mux, "rollout_a-key", modeProbe), 503, "")
	expect("0", `SELECT count(*)::text FROM rollout_a.health_records WHERE payload=$1`, modeProbe)
	setMode("prepare")
	exec(`ALTER TABLE rollout_a.settings ALTER COLUMN value SET NOT NULL`)

	brokenBatch := uuid.New()
	brokenBody := strings.Replace(mixed(brokenBatch, original), metricDate, "2026-02-30 00:00:00 +0000", 1)
	check(post(mux, "rollout_a-key", brokenBody), 400, "")
	expect("0", `SELECT count(*)::text FROM rollout_a.health_records WHERE payload=$1`, brokenBody)
	badClock := strings.Replace(mixed(uuid.New(), original), metricDate, day.Format(time.DateOnly)+" 12:75:00 +0000", 1)
	check(post(mux, "rollout_a-key", badClock), 400, "")
	expect("0", `SELECT count(*)::text FROM rollout_a.health_records WHERE payload=$1`, badClock)
	// Model a receipt and its candidate stored before validation existed.
	brokenDigest := sha256.Sum256([]byte(brokenBody))
	exec(`WITH raw AS (
		INSERT INTO rollout_a.health_records(payload,processing_status,processing_kind)
		VALUES ($1,'pending','sleep_candidates') RETURNING id
	)
	INSERT INTO rollout_a.sleep_rollout_batches(batch_id,payload_hash,health_record_id)
	SELECT $2,$3,id FROM raw`, brokenBody, brokenBatch, brokenDigest[:])
	candidateJSON, err := json.Marshal(original["candidate"])
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO rollout_a.sleep_rollout_packets(
		source_id,candidate_id,revision,wake_date,raw_payload,candidate,legacy_payload,health_record_id)
		SELECT 'ring','night',1,$1::text::date,convert_to(h.payload,'UTF8'),
		       $2::jsonb || '{"priority":0,"legacy_aliases":null}'::jsonb,'null'::jsonb,h.id
		FROM rollout_a.health_records h WHERE h.payload=$3`, day.Format(time.DateOnly), json.RawMessage(candidateJSON), brokenBody)
	if processed, err := ingest.ProcessSleepCandidateMetrics(ctx, a); err != nil || !processed {
		t.Fatalf("existing invalid mixed receipt did not leave the pending queue: %v, %v", processed, err)
	}
	expect("failed", `SELECT processing_status FROM rollout_a.health_records WHERE payload=$1`, brokenBody)
	brokenID := read(`SELECT id::text FROM rollout_a.health_records WHERE payload=$1`, brokenBody)
	setMode("off")
	replayID := check(post(mux, "rollout_a-key", brokenBody), 200, brokenBatch.String())
	if fmt.Sprint(replayID) != brokenID {
		t.Fatalf("old exact receipt returned %d; original %s", replayID, brokenID)
	}
	expect("1", `SELECT count(*)::text FROM rollout_a.health_records WHERE payload=$1`, brokenBody)
	setMode("prepare")
	// The quarantined receipt remains durable without counting as a valid upload.
	firstBatch := uuid.New()
	firstBody := mixed(firstBatch, original)
	var repeats [2]response
	var sends sync.WaitGroup
	for i := range repeats {
		sends.Go(func() { repeats[i] = post(mux, "rollout_a-key", firstBody) })
	}
	sends.Wait()
	id := check(repeats[0], 200, firstBatch.String())
	if again := check(repeats[1], 200, firstBatch.String()); again != id {
		t.Fatalf("concurrent repeat returned %d instead of %d", again, id)
	}
	expect(firstBody, `SELECT payload FROM rollout_a.health_records WHERE id=$1`, id)
	expect("1", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets p
		JOIN rollout_a.health_records h ON p.health_record_id=h.id
		WHERE p.recovery_health_record_id=$1 AND h.payload=$2 AND h.processing_status='failed'`, id, brokenBody)
	wait("Legacy RingConn", `SELECT COALESCE(MAX(source),'') FROM rollout_a.metric_points WHERE metric_name='sleep_total'`)
	wait("7", `SELECT COALESCE(MAX(sleep_total)::text,'') FROM rollout_a.daily_scores`)
	observedAt := day.Add(15 * time.Hour)
	if err := a.UpsertEnergySnapshotAt(ctx, "UTC", observedAt, storage.BankResult{Bank: 63, FormulaVersion: 3}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		return read(`SELECT to_jsonb(s)::text || ':' || xmin::text FROM rollout_a.energy_snapshots s WHERE ts_bucket=$1`, observedAt)
	}
	observed := snapshot()
	check(post(mux, "rollout_a-key", mixed(firstBatch, makeCandidate("night", "ring", "Asia/Singapore", 1))), 409, "")
	check(post(mux, "rollout_a-key", mixed(uuid.New(), makeCandidate("night", "ring", "Asia/Singapore", 1))), 409, "")
	expect("1", `SELECT count(*)::text FROM rollout_a.sleep_rollout_batches WHERE batch_id<>$1`, brokenBatch)

	// The experimental route may only acknowledge the legacy body it stored
	// itself. A mixed receipt links to an entire /health packet instead.
	experimental := func(item map[string]any, legacyJSON string) string {
		t.Helper()
		out, err := json.Marshal(map[string]any{"source_id": item["source_id"], "candidate": item["candidate"],
			"legacy_payload": json.RawMessage(legacyJSON)})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	check(send(mux, "/health/sleep/v2", "rollout_a-key", experimental(original, legacy)), 409, "")
	check(send(mux, "/health/sleep/v2", "rollout_a-key", experimental(original,
		strings.Replace(legacy, `"qty":7`, `"qty":6`, 1))), 409, "")
	workerNow := time.Now()
	if applied, err := a.RunSleepRolloutNext(ctx, func() time.Time { return workerNow }); err != nil || applied {
		t.Fatalf("candidate ran before its ordinary metrics: %v, %v", applied, err)
	}
	if processed, err := ingest.ProcessSleepCandidateMetrics(ctx, a); err != nil || !processed {
		t.Fatalf("process first mixed metrics: %v, %v", processed, err)
	}
	if applied, err := a.RunSleepRolloutNext(ctx, func() time.Time { return workerNow }); err != nil || !applied {
		t.Fatalf("apply first candidate: %v, %v", applied, err)
	}
	expect("21600", `SELECT sleep_seconds::text FROM rollout_a.sleep_rollout_daily WHERE wake_date=$1::text::date`, day.Format(time.DateOnly))
	auditTenant := uuid.MustParse(read(`SELECT tenant_id::text FROM rollout_a.__tenant_identity`))
	if report, err := a.AuditSleepRolloutPreparation(ctx, auditTenant, day.Format(time.DateOnly), day.Format(time.DateOnly)); err != nil || report.FailedMixedBatches != 0 {
		t.Fatalf("completed repair still reported as failed: %+v, %v", report, err)
	}
	correctionBatch := uuid.New()
	corrected := mixed(correctionBatch, makeCandidate("night", "ring", "Asia/Singapore", 2),
		makeCandidate("second-night", "watch", "Asia/Singapore", 1))
	// This activity point is present only in the mixed upload. The candidate
	// worker must see its daily aggregate before publishing prepared results.
	activityDate := nextDay.Format(time.DateOnly) + " 12:00:00 +0000"
	overlapDate := day.Format(time.DateOnly) + " 12:00:00 +0000"
	corrected = strings.Replace(corrected, `"metrics":[`, fmt.Sprintf(`"metrics":[{"name":"active_energy","units":"kcal","data":[{"date":%q,"qty":420,"source":"Mixed Only"},{"date":%q,"qty":420,"source":"Overlap"}]},`, activityDate, overlapDate), 1)
	exec(`CREATE FUNCTION rollout_a.fail_candidate_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.source_id='watch' THEN RAISE EXCEPTION 'synthetic acceptance failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER fail_candidate_insert BEFORE INSERT ON rollout_a.sleep_rollout_packets
		FOR EACH ROW EXECUTE FUNCTION rollout_a.fail_candidate_insert()`)
	check(post(mux, "rollout_a-key", corrected), 503, "")
	expect("0", `SELECT count(*)::text FROM rollout_a.health_records WHERE payload=$1`, corrected)
	expect("1", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets`)
	expect("1", `SELECT count(*)::text FROM rollout_a.sleep_rollout_batches WHERE batch_id<>$1`, brokenBatch)
	exec(`DROP TRIGGER fail_candidate_insert ON rollout_a.sleep_rollout_packets`)
	exec(`CREATE FUNCTION rollout_a.fail_legacy_processing() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'synthetic legacy processing failure'; END $$;
		CREATE TRIGGER fail_legacy_processing BEFORE INSERT ON rollout_a.metric_points
		FOR EACH ROW EXECUTE FUNCTION rollout_a.fail_legacy_processing()`)
	correctionID := check(post(mux, "rollout_a-key", corrected), 200, correctionBatch.String())
	expect("2", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE health_record_id=$1 AND applied_at IS NULL`, correctionID)
	if processed, err := ingest.ProcessSleepCandidateMetrics(ctx, a); err == nil || processed {
		t.Fatalf("expected transient point write failure: %v, %v", processed, err)
	}
	expect("pending", `SELECT processing_status FROM rollout_a.health_records WHERE id=$1`, correctionID)
	if applied, err := a.RunSleepRolloutNext(ctx, func() time.Time { return workerNow }); err != nil || applied {
		t.Fatalf("candidate ran after failed point write: %v, %v", applied, err)
	}
	exec(`DROP TRIGGER fail_legacy_processing ON rollout_a.metric_points`)
	exec(`CREATE FUNCTION rollout_a.fail_mixed_cache() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'synthetic cache failure'; END $$;
		CREATE TRIGGER fail_mixed_cache BEFORE INSERT OR UPDATE ON rollout_a.daily_scores
		FOR EACH ROW EXECUTE FUNCTION rollout_a.fail_mixed_cache()`)
	if processed, err := ingest.ProcessSleepCandidateMetrics(ctx, a); err == nil || processed {
		t.Fatalf("expected transient cache write failure: %v, %v", processed, err)
	}
	expect("pending", `SELECT processing_status FROM rollout_a.health_records WHERE id=$1`, correctionID)
	expect("0", `SELECT count(*)::text FROM rollout_a.metric_points WHERE source='Mixed Only'`)
	if applied, err := a.RunSleepRolloutNext(ctx, func() time.Time { return workerNow }); err != nil || applied {
		t.Fatalf("candidate ran after failed cache write: %v, %v", applied, err)
	}
	expect("2", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE applied_at IS NULL`)
	setMode("off")
	if got := check(post(mux, "rollout_a-key", corrected), 200, correctionBatch.String()); got != correctionID {
		t.Fatalf("replay during off returned %d instead of %d", got, correctionID)
	}
	check(post(mux, "rollout_a-key", strings.Replace(corrected, `"qty":420`, `"qty":421`, 1)), 409, "")
	expect("1", `SELECT count(*)::text FROM rollout_a.health_records WHERE payload=$1`, corrected)
	exec(`DROP TRIGGER fail_mixed_cache ON rollout_a.daily_scores`)
	newer := fmt.Sprintf(`{"data":{"metrics":[{"name":"active_energy","units":"kcal","data":[{"date":%q,"qty":500,"source":"Overlap"}]}]}}`, overlapDate)
	newerID := check(post(mux, "rollout_a-key", newer), 200, "")
	wait("500", `SELECT COALESCE(MAX(calories)::text,'') FROM rollout_a.daily_scores WHERE date=$1`, day.Format(time.DateOnly))
	newerPoint := read(`SELECT to_jsonb(p)::text || ':' || xmin::text FROM rollout_a.metric_points p WHERE health_record_id=$1`, newerID)
	stop()
	// Mixed recovery must not depend on the one-page legacy startup scan.
	exec(`INSERT INTO rollout_a.health_records(id,payload,processing_status)
		SELECT i,'{"data":{"metrics":[]}}','pending' FROM generate_series(-1001,-1) i`)
	restarted, _, closeRestarted := open(false, true)
	wait("complete", `SELECT processing_status FROM rollout_a.health_records WHERE id=$1`, correctionID)
	wait("1", `SELECT count(*)::text FROM rollout_a.health_records WHERE id<0 AND processing_status='pending'`)
	expect("420", `SELECT calories::text FROM rollout_a.daily_scores WHERE date=$1`, nextDay.Format(time.DateOnly))
	expect("420", `SELECT qty::text FROM rollout_a.metric_points WHERE source='Mixed Only' AND health_record_id=$1`, correctionID)
	expect("500", `SELECT calories::text FROM rollout_a.daily_scores WHERE date=$1`, day.Format(time.DateOnly))
	expect(newerPoint, `SELECT to_jsonb(p)::text || ':' || xmin::text FROM rollout_a.metric_points p WHERE health_record_id=$1`, newerID)
	if got := check(post(restarted, "rollout_a-key", corrected), 200, correctionBatch.String()); got != correctionID {
		t.Fatalf("replay with the global flag off returned %d instead of %d", got, correctionID)
	}
	check(post(restarted, "rollout_a-key", mixed(uuid.New(), original)), 200, "")
	if applied, err := a.RunSleepRolloutNext(ctx, func() time.Time { return workerNow }); err != nil || applied {
		t.Fatalf("off should hold queue, got %v, %v", applied, err)
	}
	expect("2", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE applied_at IS NULL`)
	expect(corrected, `SELECT payload FROM rollout_a.health_records WHERE id=$1`, correctionID)
	closeRestarted()
	exec(`CREATE FUNCTION rollout_a.expect_mixed_metric() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF (SELECT calories FROM rollout_a.daily_scores WHERE date=NEW.day::text)
			IS DISTINCT FROM 420::real AND NEW.day::text=TG_ARGV[0] THEN
			  RAISE EXCEPTION 'candidate projection did not see committed mixed metric';
		  END IF;
		  RETURN NEW;
		END $$;
		CREATE TRIGGER expect_mixed_metric BEFORE INSERT OR UPDATE ON rollout_a.sleep_rollout_results
		FOR EACH ROW EXECUTE FUNCTION rollout_a.expect_mixed_metric('` + nextDay.Format(time.DateOnly) + `')`)
	restarted, _, closePrepared := open(true, true)
	defer closePrepared()
	setMode("prepare")
	wait("0", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets WHERE applied_at IS NULL`)
	if got := check(post(restarted, "rollout_a-key", corrected), 200, correctionBatch.String()); got != correctionID {
		t.Fatalf("replay after restart returned %d instead of %d", got, correctionID)
	}
	if got := check(post(restarted, "rollout_a-key", firstBody), 200, firstBatch.String()); got != id {
		t.Fatalf("old repeat returned %d instead of %d", got, id)
	}
	tenant, _, _, ok := mgr.DBForAPIKey(ctx, "rollout_a-key")
	if !ok || tenant != a {
		t.Fatal("audit pool changed tenant")
	}
	identity := uuid.MustParse(read(`SELECT tenant_id::text FROM rollout_a.__tenant_identity`))
	auditFrom := day.AddDate(0, 0, -1).Format(time.DateOnly)
	auditTo := nextDay.Format(time.DateOnly)
	report, err := a.AuditSleepRolloutPreparation(ctx, identity, auditFrom, auditTo)
	if err != nil || report.ExpectedDates != 3 || report.MatchedDates != 2 ||
		report.MissingResults != 1 || report.UnownedDates != 1 || report.PendingCandidates != 0 ||
		report.Dates[1].Sleep != "confirmed_absence" || report.Dates[2].Sleep != "candidate" {
		t.Fatalf("unexpected server preparation audit: %+v, %v", report, err)
	}
	if _, err := a.AuditSleepRolloutPreparation(ctx, uuid.New(), auditFrom, auditTo); err == nil {
		t.Fatal("audit accepted another tenant's identity")
	}
	exec(`UPDATE rollout_a.sleep_rollout_results SET energy=energy||'{"Bank":12345}'::jsonb WHERE day=$1::text::date`, auditTo)
	beforeAudit := read(`SELECT (to_jsonb(r)::text || ':' || xmin::text) FROM rollout_a.sleep_rollout_results r WHERE day=$1::text::date`, auditTo)
	report, err = a.AuditSleepRolloutPreparation(ctx, identity, auditFrom, auditTo)
	if err != nil || report.MismatchedResults != 1 || report.Dates[2].EnergyBankOK || !report.Dates[2].ReadinessOK {
		t.Fatalf("audit missed changed prepared bank: %+v, %v", report, err)
	}
	expect(beforeAudit, `SELECT (to_jsonb(r)::text || ':' || xmin::text) FROM rollout_a.sleep_rollout_results r WHERE day=$1::text::date`, auditTo)

	staleBatch := uuid.New()
	check(post(restarted, "rollout_a-key", mixed(staleBatch, original)), 200, staleBatch.String())
	expect("3", `SELECT count(*)::text FROM rollout_a.sleep_rollout_packets`)
	expect("empty", `SELECT COALESCE(sleep_seconds::text, 'empty') FROM rollout_a.sleep_rollout_daily WHERE wake_date=$1::text::date`, day.Format(time.DateOnly))
	expect("21600", `SELECT sleep_seconds::text FROM rollout_a.sleep_rollout_daily WHERE wake_date=$1::text::date`, nextDay.Format(time.DateOnly))
	expect(nextDay.Format(time.DateOnly)+"/2", `SELECT wake_date::text||'/'||revision::text FROM rollout_a.sleep_rollout_candidates WHERE id::jsonb->>0='ring'`)
	expect("0", `SELECT count(*)::text FROM rollout_b.sleep_rollout_packets`)
	if snapshot() != observed {
		t.Fatal("preparation or rollback rewrote the observed EnergyBank snapshot")
	}
	// An older conventional receipt can also retry after the mixed worker has
	// already committed a newer value. Normal restart replay must respect it.
	exec(`DROP TRIGGER expect_mixed_metric ON rollout_a.sleep_rollout_results`)
	const newerMixedCalories = "550"
	oldOrdinary := fmt.Sprintf(`{"data":{"metrics":[{"name":"active_energy","units":"kcal","data":[{"date":%q,"qty":510,"source":"Mixed Only"}]}]}}`, activityDate)
	oldOrdinaryID, err := a.InsertRaw(storage.Record{Payload: oldOrdinary, PendingProcessing: true})
	if err != nil {
		t.Fatal(err)
	}
	newMixedID := uuid.New()
	newMixed := mixed(newMixedID, original)
	newMixed = strings.Replace(newMixed, `"metrics":[`, fmt.Sprintf(`"metrics":[{"name":"active_energy","units":"kcal","data":[{"date":%q,"qty":550,"source":"Mixed Only"}]},`, activityDate), 1)
	newMixedRecord := check(post(restarted, "rollout_a-key", newMixed), 200, newMixedID.String())
	wait("complete", `SELECT processing_status FROM rollout_a.health_records WHERE id=$1`, newMixedRecord)
	unknownZone := strings.Replace(mixed(uuid.New(), original), `"wake_zone":"UTC"`, `"wake_zone":"Unmapped/Region"`, 1)
	unknownZone = strings.ReplaceAll(unknownZone, "Legacy RingConn", "Unknown zone legacy")
	unknownZoneID := check(post(restarted, "rollout_a-key", unknownZone), 200, "")
	wait("complete", `SELECT processing_status FROM rollout_a.health_records WHERE id=$1`, unknownZoneID)
	expect("1", `SELECT count(*)::text FROM rollout_a.metric_points WHERE health_record_id=$1 AND source='Unknown zone legacy'`, unknownZoneID)
	expect("0", `SELECT count(*)::text FROM rollout_a.sleep_rollout_batches WHERE health_record_id=$1`, unknownZoneID)
	closePrepared()
	_, _, closeOldRecovery := open(true, false)
	defer closeOldRecovery()
	wait("complete", `SELECT processing_status FROM rollout_a.health_records WHERE id=$1`, oldOrdinaryID)
	expect(newerMixedCalories, `SELECT qty::text FROM rollout_a.metric_points WHERE source='Mixed Only' AND health_record_id=$1`, newMixedRecord)

	t.Log("legacy and mixed /health, concurrent repeat, atomic acceptance, prepared move, restart and rollback verified")
}

func TestHealthSleepCandidatesMixedSessionAggregatesOnce(t *testing.T) {
	mgr, root := newSleepRolloutFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	db, _, _, ok := mgr.DBForAPIKey(ctx, "rollout_a-key")
	if !ok {
		t.Fatal("isolated profile did not resolve")
	}
	if err := db.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "prepare", "timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := root.QueryRow(ctx, `SELECT quote_ident(pg_get_userbyid(nspowner)) FROM pg_namespace WHERE nspname='rollout_a'`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, `CREATE TABLE rollout_a.aggregate_calls(day text);
		GRANT INSERT ON rollout_a.aggregate_calls TO `+role+`;
		CREATE FUNCTION rollout_a.record_aggregate_call() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF current_query() LIKE '%sleep_picked_complete AS%' THEN
		    INSERT INTO rollout_a.aggregate_calls VALUES (NEW.date);
		  END IF;
		  RETURN NEW;
		END $$;
		CREATE TRIGGER record_aggregate_call AFTER INSERT OR UPDATE ON rollout_a.daily_scores
		FOR EACH ROW EXECUTE FUNCTION rollout_a.record_aggregate_call()`); err != nil {
		t.Fatal(err)
	}

	type notification struct {
		dates []string
		ready bool
	}
	calls := make(chan notification, 4)
	derived := storage.NewTodayDerivedStateCoordinator(storage.NewEnergyV2Orchestrator())
	refreshDerived := makeTodayDerivedStateRefresh(ctx, db, "rollout_a", storage.NotifyConfig{}, derived, func() storage.AIConfig {
		return storage.AIConfig{}
	})
	ingest := handler.New(mgr, func(_ *storage.DB, dates []string, ready bool) {
		refreshDerived(dates, ready)
		calls <- notification{dates: slices.Clone(dates), ready: ready}
	}, health.HRZones{})
	defer func() {
		if err := ingest.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	// Advance mixed work explicitly to observe each incomplete session state.
	workerCtx, stopWorker := context.WithCancel(ctx)
	stopWorker()
	mux := http.NewServeMux()
	registerHealthIngest(workerCtx, mux, mgr, ingest, true)

	session := uuid.NewString()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	firstDay, secondDay := today.AddDate(0, 0, -2), today.AddDate(0, 0, -1)
	post := func(date time.Time, metric, source string, withCandidate bool) int64 {
		t.Helper()
		qty := 100
		if !withCandidate && metric == "resting_heart_rate" {
			qty = 77
		}
		sample := map[string]any{"qty": qty, "source": source, "date": date.Format(time.DateOnly) + " 10:00:00 +0000"}
		data := map[string]any{"metrics": []any{map[string]any{"name": metric, "data": []any{sample}}}}
		if withCandidate {
			data["sleep_candidates_v1"] = map[string]any{"batch_id": uuid.NewString(), "candidates": []any{map[string]any{
				"source_id": source, "candidate": map[string]any{
					"id": "night", "revision": 1, "wake_zone": "UTC",
					"stages": []any{map[string]any{"id": "sleep", "kind": "asleep_unspecified", "start_at": date, "end_at": date.Add(6 * time.Hour)}},
				},
			}}}
		}
		body, err := json.Marshal(map[string]any{"data": data})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(string(body)))
		req.Header.Set("X-API-Key", "rollout_a-key")
		req.Header.Set("X-Sync-Session", session)
		req.Header.Set("X-Sync-Session-Total", "3")
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		var response struct{ ID int64 }
		if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil || out.Code != 200 || response.ID == 0 {
			t.Fatalf("sync failed: %d %s", out.Code, out.Body.String())
		}
		return response.ID
	}
	process := func() {
		t.Helper()
		if processed, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !processed {
			t.Fatalf("process mixed session chunk: %v, %v", processed, err)
		}
	}
	assertWaiting := func() {
		t.Helper()
		select {
		case got := <-calls:
			t.Fatalf("incomplete session notified readers: %+v", got)
		default:
		}
	}

	post(firstDay, "resting_heart_rate", "Mixed A", true)
	process()
	assertWaiting()
	post(firstDay, "resting_heart_rate", "Mixed A", false)
	// A subsequent empty job completes only after the FIFO worker joins
	// the ordinary chunk to its session. It has no notification or cache work.
	barrier := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(`{"data":{"metrics":[]}}`))
	barrier.Header.Set("X-API-Key", "rollout_a-key")
	barrierReply := httptest.NewRecorder()
	mux.ServeHTTP(barrierReply, barrier)
	var ordinaryBarrier struct{ ID int64 }
	if err := json.Unmarshal(barrierReply.Body.Bytes(), &ordinaryBarrier); err != nil || barrierReply.Code != 200 || ordinaryBarrier.ID == 0 {
		t.Fatalf("ordinary queue barrier: %d %s %v", barrierReply.Code, barrierReply.Body, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		if err := root.QueryRow(ctx, `SELECT processing_status FROM rollout_a.health_records WHERE id=$1`, ordinaryBarrier.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "complete" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ordinary sync did not reach the session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertWaiting()
	day := firstDay.Format(time.DateOnly)
	// Model an unrelated refresh that writes the current aggregate but
	// leaves the earlier mixed chunk's readiness result in place.
	if err := db.UpsertRecentCache([]string{day}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, `UPDATE rollout_a.daily_scores SET readiness=123 WHERE date=$1`, day); err != nil {
		t.Fatal(err)
	}
	post(secondDay, "active_energy", "Mixed B", true)
	process()
	select {
	case got := <-calls:
		slices.Sort(got.dates)
		if !got.ready || !slices.Equal(got.dates, []string{firstDay.Format(time.DateOnly), secondDay.Format(time.DateOnly)}) {
			t.Fatalf("wrong session notification: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("completed mixed session did not notify readers")
	}
	var scoreRefreshed bool
	if err := root.QueryRow(ctx, `SELECT COALESCE(readiness<>123 AND rhr_avg=77,false)
		FROM rollout_a.daily_scores WHERE date=$1`, day).Scan(&scoreRefreshed); err != nil || !scoreRefreshed {
		t.Fatalf("session certified an earlier readiness score: %v, %v", scoreRefreshed, err)
	}
	if err := ingest.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := derived.WaitForIdle(ctx, db); err != nil {
		t.Fatal(err)
	}
	assertWaiting()
	var aggregateCalls string
	if err := root.QueryRow(ctx, `SELECT string_agg(day||':'||n::text,',' ORDER BY day)
		FROM (SELECT day,count(*) n FROM rollout_a.aggregate_calls GROUP BY day) calls`).Scan(&aggregateCalls); err != nil {
		t.Fatal(err)
	}
	expected := day + ":2," + secondDay.Format(time.DateOnly) + ":1"
	if aggregateCalls != expected {
		t.Fatalf("daily aggregates executed %s, want %s", aggregateCalls, expected)
	}
	t.Logf("one session notification, current readiness, and no redundant aggregate: %s", aggregateCalls)
}

func TestHealthSleepPeriodReceiptOrder(t *testing.T) {
	mgr, root := newSleepRolloutFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, _, _, ok := mgr.DBForAPIKey(ctx, "rollout_a-key")
	if !ok {
		t.Fatal("isolated profile did not resolve")
	}
	if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	accept := func() int64 {
		t.Helper()
		id, err := db.InsertRaw(storage.Record{Payload: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	older, newer := accept(), accept()
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2)
	date := day.Format(time.DateOnly)
	now := time.Now().UTC()
	coverage := storage.SleepPeriodCoverageCommitment{
		WakeDate: date, SourceEpoch: "watch", CaptureCompleteness: health.SleepBalanceCoverageComplete,
		CoverageGeneration: "newer", CoveredIntervalStart: day.Add(-12 * time.Hour),
		CoveredIntervalEnd: day.Add(12 * time.Hour), ObservedAt: now, InputHash: "newer-input",
	}
	episodes := []storage.CompletedSleepEpisodeCommitment{{
		EpisodeID: "newer-episode", WakeDate: date, Start: day.Add(-2 * time.Hour), End: day.Add(6 * time.Hour),
		Source: "Watch", SourceEpoch: "watch", InputHash: "newer-episode-input", CoverageGeneration: "newer",
		CaptureState: health.SleepBalanceCoverageComplete, DurationAssessment: "plausible", ObservedAt: now,
	}}
	if changed, err := db.ApplyIngestSleepPeriodSnapshot(ctx, newer, coverage, episodes); err != nil || !changed {
		t.Fatalf("apply newer sleep period: %v, %v", changed, err)
	}
	latestRepeat := accept()
	if changed, err := db.ApplyIngestSleepPeriodSnapshot(ctx, latestRepeat, coverage, episodes); err != nil || changed {
		t.Fatalf("exact content replay must only advance receipt precedence: %v, %v", changed, err)
	}
	coverage.CoverageGeneration, coverage.InputHash = "older", "older-empty-input"
	coverage.ObservedAt = now.Add(time.Minute) // The old receipt is processed later.
	if changed, err := db.ApplyIngestSleepPeriodSnapshot(ctx, older, coverage, nil); err != nil || changed {
		t.Fatalf("delayed empty period erased the newer episode: %v, %v", changed, err)
	}
	var episodeID string
	var recordID int64
	if err := root.QueryRow(ctx, `SELECT e.episode_id,p.ingest_record_id FROM rollout_a.completed_sleep_episode e
		JOIN rollout_a.sleep_period_coverage p USING(wake_date) WHERE e.wake_date=$1`, date).
		Scan(&episodeID, &recordID); err != nil || episodeID != "newer-episode" || recordID != latestRepeat {
		t.Fatalf("unexpected retained episode %s from record %d: %v", episodeID, recordID, err)
	}
}
