package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/handler"
	"health-receiver/internal/health"
	"health-receiver/internal/sleepwire"
	"health-receiver/internal/storage"
)

func TestHealthSleepRevisionBoundWithdrawal(t *testing.T) {
	t.Setenv("HEALTH_SLEEP_V2_ENABLED", "1")
	t.Setenv("HEALTH_SLEEP_GO_PROJECTION_ENABLED", "1")
	t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "1")
	manager, root := newSleepRolloutFixture(t)
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	db, _, _, ok := manager.DBForAPIKey(ctx, "rollout_a-key")
	if !ok {
		t.Fatal("synthetic profile could not be resolved")
	}
	day := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	date := day.Format(time.DateOnly)
	projection := fmt.Sprintf(`{"from":%q,"through":%q,"sources":["fixture-device"]}`, date, date)
	if err := db.SaveSettings(map[string]string{storage.SleepRolloutModeSetting: "prepare", "timezone": "UTC",
		storage.SleepGoProjectionSetting: projection}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	ingest := handler.New(manager, nil, health.HRZones{})
	workerCtx, cancelWorker := context.WithCancel(ctx)
	cancelWorker()
	registerHealthIngest(workerCtx, mux, manager, ingest, true)
	defer ingest.Shutdown(context.Background())

	var tenant uuid.UUID
	if err := root.QueryRow(ctx, `SELECT tenant_id FROM rollout_a.__tenant_identity`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := root.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO rollout_a.daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories)
		SELECT ($1::text::date+i)::text,7,1,1,0,500 FROM generate_series(-35,0) i`, date)
	exec(`INSERT INTO rollout_a.energy_snapshots(ts_bucket,date,bank,drain_delta,restore_delta,formula_version,components)
		VALUES ($1,$2,51,1,2,2,'{}')`, day.Add(8*time.Hour), date)
	audit := func() storage.SleepRolloutAudit {
		t.Helper()
		report, err := db.AuditSleepRolloutPreparation(ctx, tenant, date, date)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	readUnknown := func() {
		t.Helper()
		nights, err := db.GetSleepSummary(date, date)
		if err != nil || len(nights) != 0 {
			t.Fatalf("withdrawn night visible or failed to read: count=%d error=%v", len(nights), err)
		}
		bank, err := db.ComputeBankForDate(ctx, "UTC", date)
		_, observedSleep := bank.Components["sleep_total_h"]
		if err != nil || observedSleep || !slices.Contains(bank.Flags, "imputed_sleep") {
			t.Fatalf("withdrawal did not reach EnergyBank: observed=%v error=%v", observedSleep, err)
		}
		briefing, err := db.GetHealthBriefing("en")
		if err != nil || (briefing.Sleep != nil && briefing.Sleep.LatestTotal != nil) {
			t.Fatalf("briefing shows the withdrawn night or failed to read: %v", err)
		}
		history, err := db.GetReadinessHistoryContext(ctx, 1)
		if err != nil || len(history) != 1 || history[0].Date != date {
			t.Fatalf("readiness did not recalculate for projected date: count=%d error=%v", len(history), err)
		}
		var readiness storage.LocalShadowReadiness
		if err := root.QueryRow(ctx, `SELECT readiness FROM rollout_a.sleep_rollout_results WHERE day=$1::text::date`, date).Scan(&readiness); err != nil ||
			readiness.Sleep == nil || readiness.Sleep.Total != nil || history[0].Score != readiness.Score {
			t.Fatalf("readiness did not preserve unknown sleep: %v", err)
		}
		var saved float64
		if err := root.QueryRow(ctx, `SELECT bank FROM rollout_a.energy_snapshots WHERE ts_bucket=$1`, day.Add(8*time.Hour)).Scan(&saved); err != nil || saved != 51 {
			t.Fatalf("observed EnergyBank snapshot changed: error=%v", err)
		}
	}
	wire := func(candidate sleepwire.Candidate) (string, string) {
		t.Helper()
		id := uuid.NewString()
		entry := map[string]any{"source_id": "fixture-device", "candidate": candidate}
		payload, err := json.Marshal(map[string]any{"data": map[string]any{
			"metrics": []any{}, "sleep_candidates_v1": map[string]any{"batch_id": id, "candidates": []any{entry}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return id, string(payload)
	}
	send := func(body string, expected int, ack string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/health", strings.NewReader(body))
		request.Header.Set("X-API-Key", "rollout_a-key")
		result := httptest.NewRecorder()
		mux.ServeHTTP(result, request)
		if result.Code != expected || result.Header().Get("X-Health-Sleep-Candidates-Ack") != ack {
			t.Fatalf("HTTP=%d ack present=%v; want HTTP=%d ack present=%v", result.Code, result.Header().Get("X-Health-Sleep-Candidates-Ack") != "", expected, ack != "")
		}
	}
	apply := func() {
		t.Helper()
		if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
			t.Fatalf("ordinary stage = %v, %v", ok, err)
		}
		if ok, err := db.RunSleepRolloutNext(ctx, func() time.Time { return day.Add(20 * time.Hour) }); err != nil || !ok {
			t.Fatalf("revision stage = %v, %v", ok, err)
		}
	}
	assert := func(revision int, status string) {
		t.Helper()
		var got int
		var state string
		if err := root.QueryRow(ctx, `SELECT c.revision,d.episode_selection->>'state'
			FROM rollout_a.sleep_rollout_candidates c JOIN rollout_a.sleep_rollout_daily d USING(wake_date)
			WHERE c.wake_date=$1::text::date AND d.sleep_seconds IS NULL`, day.Format(time.DateOnly)).Scan(&got, &state); err != nil || got != revision || state != status {
			t.Fatalf("projection got revision=%d state=%s err=%v; want revision=%d state=%s", got, state, err, revision, status)
		}
	}
	proof := sleepwire.Coverage{SourceID: "unrelated-device", Revision: 1, Start: day.Add(-12 * time.Hour), End: day.Add(12 * time.Hour), ObservedAt: day.Add(13 * time.Hour)}
	first := sleepwire.Candidate{ID: "fixture-candidate", Zone: "UTC", Revision: 1, Coverage: &proof,
		Stages: []sleepwire.Stage{{ID: "fixture-stage", Kind: "core", Start: day, End: day.Add(7 * time.Hour)}}}
	_, badBody := wire(first)
	send(badBody, http.StatusBadRequest, "")
	proof.SourceID = "fixture-device"
	firstID, firstBody := wire(first)
	send(firstBody, http.StatusOK, firstID)
	apply()
	assert(1, "unknown") // Full coverage of one observed night cannot bootstrap a schedule.
	staleAudit := audit()
	secondProof := proof
	secondProof.Revision = 2
	secondProof.ObservedAt = proof.ObservedAt.Add(time.Hour)
	withdrawal := sleepwire.Candidate{ID: first.ID, Zone: "UTC", Revision: 2, Coverage: &secondProof,
		Withdrawal: &sleepwire.Withdrawal{WakeDate: day.Format(time.DateOnly), PreviousRevision: 1}}
	secondID, secondBody := wire(withdrawal)
	send(secondBody, http.StatusOK, secondID)
	if _, err := db.SwitchSleepRollout(ctx, tenant, date, date, staleAudit.Evidence, true); !errors.Is(err, storage.ErrSleepRolloutEvidenceChanged) {
		t.Fatalf("activation missed newly queued withdrawal: %v", err)
	}
	apply()
	assert(2, "confirmed_source_absence")
	send(firstBody, http.StatusOK, firstID)
	send(secondBody, http.StatusOK, secondID)
	assert(2, "confirmed_source_absence")
	if _, err := db.SwitchSleepRollout(ctx, tenant, date, date, staleAudit.Evidence, true); !errors.Is(err, storage.ErrSleepRolloutEvidenceChanged) {
		t.Fatalf("activation accepted stale evidence: %v", err)
	}
	reviewed := audit()
	if !reviewed.ProjectionReady || reviewed.VerifiedProjectionDates != 1 {
		t.Fatal("covered generation was not ready")
	}
	exec(`UPDATE rollout_a.daily_scores SET calories=700 WHERE date=($1::text::date-31)::text`, date)
	unrelatedDay := day.AddDate(0, 0, 35)
	unrelated := first
	unrelated.ID = "fixture-unrelated"
	unrelatedCoverage := proof
	unrelatedCoverage.Start = unrelatedDay.Add(-12 * time.Hour)
	unrelatedCoverage.End = unrelatedDay.Add(12 * time.Hour)
	unrelatedCoverage.ObservedAt = unrelatedDay.Add(13 * time.Hour)
	unrelated.Coverage = &unrelatedCoverage
	unrelated.Stages = []sleepwire.Stage{{ID: unrelated.ID, Kind: "core", Start: unrelatedDay, End: unrelatedDay.Add(7 * time.Hour)}}
	unrelatedID, unrelatedBody := wire(unrelated)
	send(unrelatedBody, http.StatusOK, unrelatedID)
	publication, err := db.SwitchSleepRollout(ctx, tenant, date, date, reviewed.Evidence, true)
	if err != nil || publication.Packet != reviewed.Dates[0].Generation {
		t.Fatalf("publication boundary did not match the reviewed generation: %v", err)
	}
	if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
		t.Fatalf("unrelated ordinary stage: %v, %v", ok, err)
	}
	readUnknown()

	for offset := 7; offset > 0; offset-- {
		prior := day.AddDate(0, 0, -offset)
		history := first
		history.ID = fmt.Sprintf("fixture-history-%d", offset)
		coverage := proof
		coverage.Start = prior.Add(-12 * time.Hour)
		coverage.End = prior.Add(12 * time.Hour)
		coverage.ObservedAt = prior.Add(13 * time.Hour)
		history.Coverage = &coverage
		history.Stages = []sleepwire.Stage{{ID: history.ID, Kind: "core", Start: prior, End: prior.Add(7 * time.Hour)}}
		historyID, historyBody := wire(history)
		send(historyBody, http.StatusOK, historyID)
		apply()
	}
	thirdProof := secondProof
	thirdProof.Revision = 3
	thirdProof.ObservedAt = secondProof.ObservedAt.Add(time.Hour)
	replacement := first
	replacement.Revision = 3
	replacement.Coverage = &thirdProof
	thirdID, thirdBody := wire(replacement)
	send(thirdBody, http.StatusOK, thirdID)
	apply()
	nights, err := db.GetSleepSummary(date, date)
	if err != nil || len(nights) != 1 || nights[0].Total != 7 {
		t.Fatalf("serve did not show the new covered night: count=%d error=%v", len(nights), err)
	}
	fourthProof := thirdProof
	fourthProof.Revision = 4
	fourthProof.ObservedAt = thirdProof.ObservedAt.Add(time.Hour)
	withdrawal.Revision = 4
	withdrawal.Coverage = &fourthProof
	withdrawal.Withdrawal.PreviousRevision = 3
	fourthID, fourthBody := wire(withdrawal)
	send(fourthBody, http.StatusOK, fourthID)
	apply()
	assert(4, "confirmed_source_absence")
	send(secondBody, http.StatusOK, secondID)
	oldReceipt, oldRepeat := wire(first)
	send(oldRepeat, http.StatusOK, oldReceipt)
	if ok, err := ingest.ProcessSleepCandidateMetrics(ctx, db); err != nil || !ok {
		t.Fatalf("stale repeat ordinary stage: %v, %v", ok, err)
	}
	if ok, err := db.RunSleepRolloutNext(ctx, func() time.Time { return day.Add(20 * time.Hour) }); err != nil || ok {
		t.Fatalf("stale repeat changed current revision: %v, %v", ok, err)
	}
	assert(4, "confirmed_source_absence")
	readUnknown()
	t.Setenv("HEALTH_SLEEP_SERVE_ENABLED", "")
	t.Setenv("HEALTH_SLEEP_GO_PROJECTION_ENABLED", "")
	t.Setenv("HEALTH_SLEEP_V2_ENABLED", "")
	readUnknown()
	if _, err := db.SwitchSleepRollout(ctx, tenant, date, date, audit().Evidence, false); err != nil {
		t.Fatal(err)
	}
	readUnknown()
	// The ordinary configuration may change after rollback. The stored
	// publication retains its original source set and keeps receiving.
	if err := db.SaveSettings(map[string]string{storage.SleepGoProjectionSetting: "{}"}); err != nil {
		t.Fatal(err)
	}
	postRollback := replacement
	fifthProof := fourthProof
	fifthProof.Revision = 5
	fifthProof.ObservedAt = fourthProof.ObservedAt.Add(time.Hour)
	postRollback.Coverage = &fifthProof
	postRollback.Revision = 5
	postRollback.Stages = []sleepwire.Stage{{ID: "fixture-new-stage", Kind: "core", Start: day, End: day.Add(6 * time.Hour)}}
	postID, postBody := wire(postRollback)
	t.Setenv("HEALTH_SLEEP_V2_ENABLED", "1")
	send(postBody, http.StatusOK, postID)
	apply()
	nights, err = db.GetSleepSummary(date, date)
	if err != nil || len(nights) != 1 || nights[0].Total != 6 {
		t.Fatalf("rollback did not process a new revision using the pinned source set: count=%d error=%v", len(nights), err)
	}
	beforeMultiDay := audit()
	multiDay := first
	multiDay.ID = "fixture-multi-day"
	multiCoverage := proof
	multiCoverage.End = day.Add(36 * time.Hour)
	multiCoverage.ObservedAt = day.Add(37 * time.Hour)
	multiDay.Coverage = &multiCoverage
	multiDay.Stages = append(slices.Clone(first.Stages),
		sleepwire.Stage{ID: "fixture-next-day", Kind: "core", Start: day.Add(29 * time.Hour), End: day.Add(31 * time.Hour)})
	multiID, multiBody := wire(multiDay)
	send(multiBody, http.StatusOK, multiID)
	afterMultiDay := audit()
	if afterMultiDay.PendingCandidates != beforeMultiDay.PendingCandidates+1 || afterMultiDay.Evidence == beforeMultiDay.Evidence {
		t.Fatal("audit missed a pending multi-date snapshot touching the reviewed window")
	}

	// A date outside the episode selection lookback can still invalidate a
	// dependent readiness result for the reviewed date.
	prior := day.AddDate(0, 0, -45)
	dependent := first
	dependent.ID = "fixture-readiness-dependency"
	dependentProof := proof
	dependentProof.Start = prior.Add(-12 * time.Hour)
	dependentProof.End = prior.Add(12 * time.Hour)
	dependentProof.ObservedAt = prior.Add(13 * time.Hour)
	dependent.Coverage = &dependentProof
	dependent.Stages = []sleepwire.Stage{{ID: dependent.ID, Kind: "core", Start: prior, End: prior.Add(7 * time.Hour)}}
	dependentID, dependentBody := wire(dependent)
	send(dependentBody, http.StatusOK, dependentID)
	if report := audit(); report.PendingCandidates != afterMultiDay.PendingCandidates+1 || report.Evidence == afterMultiDay.Evidence {
		t.Fatal("audit missed a pending change to a dependent readiness calculation")
	}

}
