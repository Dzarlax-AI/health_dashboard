package storage

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"health-receiver/internal/health"
)

// The graph driver owns the database and its random marker. The application
// reader stays read-only; a separate fixture handle simulates ingestion and archival.
func TestInstalledProfileSleepReader(t *testing.T) {
	ctx := t.Context()
	token := os.Getenv("PROFILE_GRAPH_FIXTURE_TOKEN")
	if token == "" {
		t.Skip("run the installed profile graph probe")
	}
	dsn, source, graph := os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA")
	if !(source == "profile_source" && graph == "profile_graph" || source == "peer_source" && graph == "peer_graph") {
		t.Fatal("unexpected synthetic profile schemas")
	}
	writer, err := NewWithSchema(ctx, dsn, source)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var verified bool
	err = writer.pool.QueryRow(ctx, `select current_database()='health_profile_graph_fixture'
		and current_schema()=$2 and token=$1 from profile_graph_fixture_marker`, token, source).Scan(&verified)
	if err != nil || !verified {
		t.Fatalf("invalid disposable database marker: %v", err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := writer.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`create table if not exists settings(key text primary key, value text not null)`)
	var hasReference bool
	if err := writer.pool.QueryRow(ctx, `select to_regclass('sleep_duration_reference_v1') is not null`).Scan(&hasReference); err != nil {
		t.Fatal(err)
	}
	if !hasReference {
		if err := writer.ensureSleepDurationReference(ctx); err != nil {
			t.Fatal(err)
		}
	}
	db, err := NewLocalProfileGraphReader(ctx, dsn, source, graph)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const day = "1981-03-15"
	device := "Graph Watch"
	if source == "peer_source" {
		device = "Peer Graph Watch"
	}
	exec(`insert into settings(key,value) values($1,'false') on conflict(key) do update set value=excluded.value`, sleepComparisonSetting)
	if f := db.GetSleepDurationComparison(ctx, day); f != nil {
		t.Fatalf("disabled feature returned %+v", f)
	}
	exec(`update settings set value='true' where key=$1`, sleepComparisonSetting)
	if f := db.GetSleepDurationComparison(ctx, day); f == nil || f.Status != health.SleepComparisonNoNight {
		t.Fatalf("expected absent night, got %+v", f)
	}
	exec(`insert into completed_night_sleep(wake_date,duration_hours,source,source_epoch,algorithm_version,
		input_hash,capture_completeness,duration_assessment,finalization_state,claim_eligibility,observed_at,diagnostics)
		select ($1::date-i)::text,case when i=0 then 6.8 else 7.5 end,$2::text,'graph','v1',
		i::text,'complete','plausible','final','eligible',($1::date-i)::timestamp at time zone 'UTC',
		jsonb_build_object('preferred_sleep_source',$2::text,'source_selection_reason','preferred_source')
		from generate_series(0,6) i`, day, device)
	compare := func() *health.SleepDurationComparison {
		t.Helper()
		runtime, candidate := writer.GetSleepDurationComparison(ctx, day), db.GetSleepDurationComparison(ctx, day)
		if runtime == nil || candidate == nil || !reflect.DeepEqual(runtime, candidate) {
			t.Fatalf("legacy and graph results differ: %+v / %+v", runtime, candidate)
		}
		return candidate
	}
	if f := compare(); f.Status != health.SleepComparisonInsufficient || f.EligibleNights == nil || *f.EligibleNights != 6 {
		t.Fatalf("six-night boundary: %+v", f)
	}
	exec(`insert into completed_night_sleep(wake_date,duration_hours,source,source_epoch,algorithm_version,
		input_hash,capture_completeness,duration_assessment,finalization_state,claim_eligibility,observed_at)
		values(($1::date-7)::text,8.5,$2,'graph','v1','late','complete','plausible','final','eligible',
		($1::date-7)::timestamp at time zone 'UTC')`, day, device)
	first := compare()
	if first.DeltaMinutes == nil || *first.DeltaMinutes != -51 || first.PreferredSleepSource != device || first.Source != device ||
		first.SourceSelectionReason != "preferred_source" || first.FactVersion == "" {
		t.Fatalf("incorrect source explanation or comparison: %+v", first)
	}
	if err := writer.EnsureEnergySnapshotsTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := writer.EnsureReportHistoryTablesContext(ctx); err != nil {
		t.Fatal(err)
	}
	exec(notificationDeliveriesTableDDL)
	_, body := health.SleepComparisonText(*first, "en")
	evidence, err := json.Marshal(health.MorningReportSleep{ReportDate: day, Comparison: first})
	if err != nil {
		t.Fatal(err)
	}
	key := "report:morning:" + day
	report := &ReportHistory{DeliveryKey: &key, Kind: "morning", LocalDate: day, Timezone: "UTC",
		Language: "en", PlainBody: body, RichBody: body, Context: evidence}
	lease, reserved, err := writer.ReserveArchivedReport(ctx, report)
	if err != nil || !reserved {
		t.Fatalf("reserve archived fixture: %t, %v", reserved, err)
	}
	attempt, err := writer.ReserveReportAttempt(ctx, report.ID, "rich", body, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.CompleteReportAttempt(ctx, attempt, "sent", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.CompleteNotificationDelivery(ctx, key, lease, "sent", ""); err != nil {
		t.Fatal(err)
	}
	exec(`update completed_night_sleep set duration_hours=7.5 where wake_date=($1::date-7)::text`, day)
	current := compare()
	if current.DeltaMinutes == nil || *current.DeltaMinutes != -42 || current.FactVersion == first.FactVersion {
		t.Fatalf("late sync did not revise the live explanation: %+v", current)
	}
	archived, err := db.GetReportHistory(ctx, report.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored health.MorningReportSleep
	if err := json.Unmarshal(archived.Context, &stored); err != nil || !reflect.DeepEqual(stored.Comparison, first) ||
		archived.PlainBody != body || !strings.Contains(body, "-51 min") || len(archived.Attempts) != 1 ||
		archived.Attempts[0].Status != "sent" || archived.Attempts[0].Body != body {
		t.Fatalf("archive changed after late sync: %v", err)
	}
	// The HTTP Today probe also exercises a dated observation through the normal briefing route.
	exec(`insert into completed_night_sleep(wake_date,duration_hours,source,source_epoch,algorithm_version,
		input_hash,capture_completeness,duration_assessment,finalization_state,claim_eligibility,observed_at)
		values($1,6.8,$2,'graph','v1','today','complete','plausible','final','eligible',now())
		on conflict(wake_date) do update set observed_at=coalesce(completed_night_sleep.observed_at, excluded.observed_at)`, db.Today(), device)
	// Make the graph relation unavailable to prove both which query runs and
	// that switching back to the native handle restores the complete result.
	exec("alter view " + db.profileGraphRelation("semantic_sleep_duration_comparison_v1") + " rename to sleep_probe_unavailable")
	defer exec("alter view " + db.profileGraphRelation("sleep_probe_unavailable") + " rename to semantic_sleep_duration_comparison_v1")
	if f := db.GetSleepDurationComparison(ctx, day); f == nil || f.Status != health.SleepComparisonUnavailable || f.DeltaMinutes != nil {
		t.Fatalf("missing graph view returned %+v", f)
	}
	if f := writer.GetSleepDurationComparison(ctx, day); !reflect.DeepEqual(f, current) {
		t.Fatalf("rollback lost live comparison: %+v", f)
	}
}
