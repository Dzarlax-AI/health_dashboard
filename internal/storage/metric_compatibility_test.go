package storage

import (
	"math"
	"slices"
	"testing"
	"time"
)

func TestNormalizeStoredMetricPoint(t *testing.T) {
	for _, tc := range []struct {
		name, unit         string
		qty                float64
		wantName, wantUnit string
		wantQty            float64
	}{
		{"walking_asymmetry", "%", 0, "walking_asymmetry_percentage", "%", 0},
		{"environmental_audio", "dBASPL", 42, "environmental_audio_exposure", "dBASPL", 42},
		{"height", "cm", 180, "height", "m", 1.8},
		{"walking_step_length", "cm", 75, "walking_step_length", "m", .75},
		{"walking_speed", "km/hr", 7.2, "walking_speed", "m/s", 2},
		{"walking_speed", "m/s", 2, "walking_speed", "m/s", 2},
		{"walking_speed", "unknown", 7, "walking_speed", "unknown", 7},
		{"active_energy", "Cal", 10, "active_energy", "kcal", 10},
		{"heart_rate", "count/min", 66, "heart_rate", "bpm", 66},
		{"vo2_max", "mL/min·kg", 41, "vo2_max", "mL/kg/min", 41},
		{"cycling_functional_threshold_power", "W", 253, "cycling_ftp", "W", 253},
		{"u_v_exposure", "count", 3, "uv_exposure", "count", 3},
		{"dietary_energy", "Cal", 250, "dietary_energy", "kcal", 250},
		{"distance_swimming", "m", 1250, "distance_swimming", "km", 1.25},
		{"stair_descent_speed", "ft/s", 2, "stair_descent_speed", "m/s", .6096},
		{"unrelated", "cm", 5, "unrelated", "cm", 5},
	} {
		point := MetricPoint{MetricName: tc.name, Units: tc.unit, Qty: tc.qty, Source: "fixture"}
		want := MetricPoint{MetricName: tc.wantName, Units: tc.wantUnit, Qty: tc.wantQty, Source: "fixture"}
		got := normalizeStoredMetricPoint(point)
		if got != want || normalizeStoredMetricPoint(got) != want {
			t.Errorf("%s/%s: %+v, want %+v", tc.name, tc.unit, got, want)
		}
	}
}

func TestArchiveAndLiveShareMetricCompatibility(t *testing.T) {
	db, clean := testDB(t)
	defer clean()
	const old = "walking_asymmetry"
	const canonical = "walking_asymmetry_percentage"
	const source = "fixture-watch"
	ctx := t.Context()
	fresh := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	oldExact := "2026-07-02 10:00:00 +0000"
	oldStale := "2026-07-02 11:00:00 +0000"
	newerAlias := "2026-07-02 12:00:00 +0000"
	outside := "2026-07-01 09:00:00 +0000"
	fixtureID := insertTestRawRecord(t, db, "pre-compatibility-live")
	seed := func(metric, date, unit string, qty float64, snapshot time.Time) {
		t.Helper()
		_, err := db.pool.Exec(ctx, `INSERT INTO metric_points(metric_name,date,units,source,qty,source_snapshot_at,health_record_id)
		 VALUES($1,$2,$3,$4,$5,$6,$7)`, metric, date, unit, source, qty, snapshot, fixtureID)
		if err != nil {
			t.Fatal(err)
		}
	}
	seed(old, oldExact, "%", 5, fresh.Add(-time.Hour))
	seed(old, oldStale, "%", 3, fresh.Add(-time.Hour))
	seed(old, newerAlias, "%", 9, fresh.Add(time.Hour))
	seed(old, outside, "%", 4, fresh.Add(-time.Hour))
	seed("height", outside, "cm", 180, fresh.Add(-time.Hour))
	seed("distance_swimming", outside, "m", 1250, fresh.Add(-time.Hour))
	seed("stair_ascent_speed", outside, "ft/s", 2, fresh.Add(-time.Hour))
	for i := 0; i < 2; i++ {
		run, err := db.BeginAppleHealthXMLImport(ImportOptions{SourceName: "fixture", SnapshotAt: fresh})
		if err != nil {
			t.Fatal(err)
		}
		if err = run.AddPoints([]MetricPoint{
			{MetricName: old, Date: oldExact, Units: "%", Source: source, Qty: 7},
			{MetricName: canonical, Date: newerAlias, Units: "%", Source: source, Qty: 6},
		}); err != nil {
			t.Fatal(err)
		}
		result, err := run.Commit()
		if err != nil {
			t.Fatal(err)
		}
		if result.SkippedPoints != 1 {
			t.Errorf("run %d protected count = %d", i, result.SkippedPoints)
		}
	}
	assertPointState(t, db, canonical, oldExact, source, 7, appleHealthXMLOrigin)
	assertPointState(t, db, old, newerAlias, source, 9, "live")
	var n int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM metric_points WHERE metric_name=ANY($1) AND date=ANY($2)`, []string{old, canonical}, []string{oldExact, oldStale, newerAlias}).Scan(&n); err != nil || n != 2 {
		t.Fatalf("same-day winning rows = %d, err=%v", n, err)
	}
	// A later live point submitted under an old API name shares the canonical
	// import key. The legacy rows on other dates remain visible via either name.
	record := insertTestRawRecord(t, db, "compatibility-live")
	if err := db.InsertPoints(record, []MetricPoint{{MetricName: old, Date: oldExact, Units: "%", Source: source, Qty: 11}}); err != nil {
		t.Fatal(err)
	}
	assertPointState(t, db, canonical, oldExact, source, 11, "live")
	// A database upgraded in place can still contain both names for a key.
	// The older alias must neither change the value nor add a second point.
	seed(old, oldExact, "%", 99, fresh.Add(-time.Hour))
	for _, name := range []string{old, canonical} {
		from, to := "2026-07-01", "2026-07-02 23:59:59"
		points, err := db.GetMetricData(name, from, to, "day", "AVG")
		if err != nil || len(points) != 2 || points[0].Qty != 4 || points[1].Qty != 10 {
			t.Fatalf("%s day points=%+v err=%v", name, points, err)
		}
		grouped, err := db.GetMetricDataBySource(name, from, to, "minute", "AVG")
		if err != nil || len(grouped) != 1 || len(grouped[0].Points) != 3 {
			t.Fatalf("%s sources=%+v err=%v", name, grouped, err)
		}
		min, max, err := db.GetMetricDateRange(name)
		if err != nil || min != "2026-07-01" || max != "2026-07-02" {
			t.Fatalf("%s range %s..%s err=%v", name, min, max, err)
		}
	}
	height, err := db.GetMetricData("height", "2026-07-01", "2026-07-01 23:59:59", "day", "AVG")
	if err != nil || len(height) != 1 || height[0].Qty != 1.8 {
		t.Fatalf("height=%+v err=%v", height, err)
	}
	for metric, expected := range map[string]float64{"distance_swimming": 1.25, "stair_ascent_speed": .6096} {
		points, err := db.GetMetricData(metric, "2026-07-01", "2026-07-01 23:59:59", "day", "AVG")
		if err != nil || len(points) != 1 || points[0].Qty != expected {
			t.Fatalf("%s=%+v err=%v", metric, points, err)
		}
	}
	summaries, err := db.ListMetrics()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(summaries))
	for _, s := range summaries {
		names = append(names, s.Name)
	}
	if !slices.Contains(names, canonical) || slices.Contains(names, old) {
		t.Fatalf("catalog names=%v", names)
	}
	latest, err := db.GetLatestMetricValues()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(latest, func(x LatestValue) bool { return x.Metric == canonical && x.Value == 10 }) {
		t.Fatalf("latest=%+v", latest)
	}
}

func TestHealthKitCompatibilityHistoricalReadAndOrdering(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	const day = "2026-07-01"
	const name = "height"
	old := insertTestRawRecord(t, db, "older receipt")
	newer := insertTestRawRecord(t, db, "newer receipt")
	snapshot := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	for id, received := range map[int64]time.Time{old: snapshot.Add(-time.Minute), newer: snapshot.Add(time.Minute)} {
		if _, err := db.pool.Exec(ctx, "UPDATE health_records SET received_at=$2 WHERE id=$1", id, received); err != nil {
			t.Fatal(err)
		}
	}
	p := MetricPoint{MetricName: name, Date: day + " 08:00:00 +0000", Units: "cm", Source: "Watch", Qty: 180}
	session, err := db.BeginAppleHealthXMLImport(ImportOptions{SnapshotAt: snapshot, UnverifiedCoverage: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.AddPoints([]MetricPoint{p}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Commit(); err != nil {
		t.Fatal(err)
	}
	p.Qty = 190
	if err := db.InsertPoints(old, []MetricPoint{p}); err != nil {
		t.Fatal(err)
	}
	assertPointState(t, db, name, p.Date, p.Source, 1.8, appleHealthXMLOrigin)
	p.Qty = 200
	if err := db.InsertPoints(newer, []MetricPoint{p}); err != nil {
		t.Fatal(err)
	}
	p.Qty = 210
	if err := db.InsertPoints(old, []MetricPoint{p}); err != nil {
		t.Fatal(err)
	}
	assertPointState(t, db, name, p.Date, p.Source, 2, "live")

	const alias = "six_min_walk_distance"
	const canonical = "six_minute_walking_test_distance"
	for _, point := range []MetricPoint{
		{MetricName: alias, Date: day + " 08:00:00 +0000", Units: "m", Source: "Watch", Qty: 510},
		{MetricName: alias, Date: day + " 08:01:00 +0000", Units: "m", Source: "Watch", Qty: 510},
		{MetricName: alias, Date: day + " 09:00:00 +0000", Units: "m", Source: "Watch", Qty: 540},
		{MetricName: name, Date: day + " 10:00:00 +0000", Units: "in", Source: "Watch", Qty: 72},
		{MetricName: "blood_alcohol_content", Date: day + " 11:00:00 +0000", Units: "%", Source: "Watch", Qty: 0.5},
	} {
		_, err := db.pool.Exec(ctx, `INSERT INTO metric_points(metric_name,date,units,source,qty,health_record_id)
			VALUES($1,$2,$3,$4,$5,$6)`, point.MetricName, point.Date, point.Units, point.Source, point.Qty, old)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, legacy := range []bool{true, false} {
		catalog, err := db.ListMetricsForContract(legacy)
		if err != nil || !slices.ContainsFunc(catalog, func(m MetricSummary) bool { return m.Name == canonical }) ||
			slices.ContainsFunc(catalog, func(m MetricSummary) bool { return m.Name == alias }) != legacy {
			t.Fatalf("legacy=%t catalog=%+v err=%v", legacy, catalog, err)
		}
		latest, err := db.LatestMetricsForContract(legacy)
		if err != nil || slices.ContainsFunc(latest, func(m LatestValue) bool { return m.Metric == alias }) != legacy {
			t.Fatalf("legacy=%t latest=%+v err=%v", legacy, latest, err)
		}
		points, err := db.MetricDataForContract(alias, day, day+" 23:59:59", "day", "AVG", legacy)
		want := 525.0
		if legacy {
			want = 520
		}
		if err != nil || len(points) != 1 || points[0].Qty != want {
			t.Fatalf("legacy=%t hourly weighting points=%+v want=%v err=%v", legacy, points, want, err)
		}
	}
	stats, err := db.SummarizeMetric(name, 1)
	if err != nil || stats.Avg != 2 || stats.Units != "m" {
		t.Fatalf("summary=%+v err=%v", stats, err)
	}
	if stats, _ := db.SummarizeMetric("blood_alcohol_content", 1); stats != nil {
		t.Fatalf("ambiguous percentage leaked into canonical summary: %+v", stats)
	}
	const wire = `{"data":{"metrics":[{"name":"blood_alcohol_content","units":"%","data":[{"date":"2026-07-01 12:00:00 +0000","source":"Phone","qty":0.04}]}]}}`
	provenLive, err := db.InsertRaw(Record{ContentType: "application/json", Payload: wire})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertPoints(provenLive, []MetricPoint{{MetricName: "blood_alcohol_content",
		Date: day + " 12:00:00 +0000", Units: "%", Source: "Phone", Qty: .04}}); err != nil {
		t.Fatal(err)
	}
	stats, err = db.SummarizeMetric("blood_alcohol_content", 1)
	if err != nil || stats.Avg < .039 || stats.Avg > .041 || stats.Units != "%" {
		t.Fatalf("legacy app percent=%+v err=%v", stats, err)
	}
}

func TestHealthKitArchiveCoverageAndGrain(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const day = "2026-07-01"
	const metric = "distance_swimming"
	const watch = "Watch"
	const ring = "Ring"
	live, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	point := func(metric, clock, source string, qty float64) MetricPoint {
		return MetricPoint{MetricName: metric, Date: day + " " + clock + ":00 +0000", Units: "km", Source: source, Qty: qty}
	}
	livePoint := point(metric, "08:00", watch, .9)
	if err := db.InsertPoints(live, []MetricPoint{livePoint}); err != nil {
		t.Fatal(err)
	}
	rows := []MetricPoint{point(metric, "08:00", watch, .2), point(metric, "08:15", watch, .3), point(metric, "08:25", ring, .4)}
	for _, opts := range []ImportOptions{
		{UnverifiedCoverage: true, CatalogNormalized: true},
		{SnapshotAt: time.Now().Add(time.Minute), CatalogNormalized: true},
		{SnapshotAt: time.Now().Add(time.Minute), UnverifiedCoverage: true, CatalogNormalized: true},
	} {
		run, err := db.BeginAppleHealthXMLImport(opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := run.AddPoints(rows); err != nil {
			t.Fatal(err)
		}
		result, err := run.Commit()
		if err != nil || result.DeletedPoints != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	assertPointState(t, db, metric, livePoint.Date, watch, .9, "live")
	bySource, err := db.GetMetricDataBySource(metric, day, day+" 23:59:59", "hour", "SUM")
	if err != nil || len(bySource) != 2 ||
		!slices.ContainsFunc(bySource, func(s SourceDataPoints) bool {
			return s.Source == watch && len(s.Points) == 1 && math.Abs(s.Points[0].Qty-.9) < 1e-6
		}) ||
		!slices.ContainsFunc(bySource, func(s SourceDataPoints) bool {
			return s.Source == ring && len(s.Points) == 1 && math.Abs(s.Points[0].Qty-.4) < 1e-6
		}) {
		t.Fatalf("hours by source=%+v err=%v", bySource, err)
	}
	points, err := db.GetMetricData(metric, day, day+" 23:59:59", "minute", "AVG")
	if err != nil || len(points) != 3 || points[1].Date != day+" 08:15" {
		t.Fatalf("archive minutes=%+v err=%v", points, err)
	}

	const effort = "physical_effort"
	first := MetricPoint{MetricName: effort, Date: day + " 07:00:00 +0000", Units: "kcal/hr·kg", Qty: 3, Source: watch}
	rejected := MetricPoint{MetricName: effort, Date: day + " 07:05:00 +0000", Units: "unknown", Qty: 7, Source: watch}
	if err := db.InsertPoints(live, []MetricPoint{first, rejected}); err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []time.Time{time.Time{}, time.Now().Add(time.Minute)} {
		run, err := db.BeginAppleHealthXMLImport(ImportOptions{SnapshotAt: stamp, UnverifiedCoverage: true})
		if err != nil {
			t.Fatal(err)
		}
		first.Qty = 5
		if err := run.AddPoints([]MetricPoint{first}); err != nil {
			t.Fatal(err)
		}
		if result, err := run.Commit(); err != nil || result.DeletedPoints != 0 {
			t.Fatalf("partial import=%+v err=%v", result, err)
		}
		assertPointState(t, db, effort, rejected.Date, watch, 7, "live")
		wantQty, origin := 3.0, "live"
		if !stamp.IsZero() {
			wantQty, origin = 5, appleHealthXMLOrigin
		}
		assertPointState(t, db, effort, first.Date, watch, wantQty, origin)
	}
}

func TestHealthKitCoreHourlyCollisionKeepsArchiveSample(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const day = "2026-07-01"
	const metric = "step_count"
	snapshot := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	id, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.pool.Exec(t.Context(), "UPDATE health_records SET received_at=$2 WHERE id=$1", id, snapshot.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		hour         string
		archiveFirst bool
	}{{"07", true}, {"08", false}} {
		point := MetricPoint{MetricName: metric, Date: day + " " + tc.hour + ":00:00 +0000", Units: "count", Source: "Watch", Qty: 150}
		live := func() {
			t.Helper()
			if err := db.InsertPoints(id, []MetricPoint{point}); err != nil {
				t.Fatal(err)
			}
		}
		archive := func() {
			t.Helper()
			run, err := db.BeginAppleHealthXMLImport(ImportOptions{SnapshotAt: snapshot, UnverifiedCoverage: true, CatalogNormalized: true})
			if err != nil {
				t.Fatal(err)
			}
			sample := point
			sample.Qty = 12
			otherSource := sample
			otherSource.Source = "Phone"
			otherSource.Qty = 10
			if err := run.AddPoints([]MetricPoint{sample, otherSource}); err != nil {
				t.Fatal(err)
			}
			if _, err := run.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		if tc.archiveFirst {
			archive()
			live()
		} else {
			live()
			archive()
		}
		assertPointState(t, db, metric, point.Date, point.Source, 150, "live")
		assertPointState(t, db, archiveSamplePrefix+metric, point.Date, point.Source, 12, appleHealthXMLOrigin)
	}
	hae := insertTestRawRecord(t, db, "legacy HAE rows")
	if err := db.InsertPoints(hae, []MetricPoint{
		{MetricName: metric, Date: day + " 09:15:00 +0000", Units: "count", Source: "Watch", Qty: 20},
		{MetricName: metric, Date: day + " 09:45:00 +0000", Units: "count", Source: "Watch", Qty: 40},
	}); err != nil {
		t.Fatal(err)
	}
	hourly, err := db.MetricDataForContract(metric, day, day+" 23:59:59", "hour", "SUM", false)
	if err != nil || len(hourly) != 3 || hourly[0].Qty != 150 || hourly[1].Qty != 150 || hourly[2].Qty != 60 {
		t.Fatalf("canonical hourly totals=%+v err=%v", hourly, err)
	}
	minutes, err := db.MetricDataForContract(metric, day, day+" 23:59:59", "minute", "SUM", false)
	if err != nil || len(minutes) != 4 || minutes[0].Qty != 12 || minutes[1].Qty != 12 {
		t.Fatalf("canonical archive minute samples=%+v err=%v", minutes, err)
	}
	bySource, err := db.MetricDataBySourceForContract(metric, day, day+" 23:59:59", "minute", "SUM", false)
	if err != nil || len(bySource) != 2 ||
		!slices.ContainsFunc(bySource, func(s SourceDataPoints) bool {
			return s.Source == "Phone" && len(s.Points) == 2 && s.Points[0].Qty == 10 && s.Points[1].Qty == 10
		}) {
		t.Fatalf("canonical minute samples by source=%+v err=%v", bySource, err)
	}
	latest, err := db.LatestMetricsForContract(false)
	if err != nil || !slices.ContainsFunc(latest, func(p LatestValue) bool { return p.Metric == metric && p.Unit == "count" && p.Value == 360 }) {
		t.Fatalf("canonical latest=%+v err=%v", latest, err)
	}
}

func TestHealthKitArchiveFreshnessAndZeroLiveTotal(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const day = "2026-07-01"
	const metric = "step_count"
	const date = day + " 08:00:00 +0000"
	const source = "Watch"
	newClock := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	point := MetricPoint{MetricName: metric, Units: "count", Date: date, Source: source, Qty: 20}
	initial, err := db.BeginAppleHealthXMLImport(ImportOptions{SnapshotAt: newClock})
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.AddPoints([]MetricPoint{point}); err != nil {
		t.Fatal(err)
	}
	if _, err := initial.Commit(); err != nil {
		t.Fatal(err)
	}
	// Restore the pre-catalog storage shape, with the archive at the main key only.
	if _, err := db.pool.Exec(t.Context(), "DELETE FROM metric_points WHERE metric_name=$1", archiveSamplePrefix+metric); err != nil {
		t.Fatal(err)
	}
	run, err := db.BeginAppleHealthXMLImport(ImportOptions{SnapshotAt: newClock.Add(-time.Hour), UnverifiedCoverage: true, CatalogNormalized: true})
	if err != nil {
		t.Fatal(err)
	}
	point.Qty = 10
	if err := run.AddPoints([]MetricPoint{point}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Commit(); err != nil {
		t.Fatal(err)
	}
	assertPointState(t, db, metric, date, source, 20, appleHealthXMLOrigin)
	assertPointState(t, db, archiveSamplePrefix+metric, date, source, 10, appleHealthXMLOrigin)

	live, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	for _, qty := range []float64{150, 0} {
		point.Qty = qty
		if err := db.InsertPoints(live, []MetricPoint{point}); err != nil {
			t.Fatal(err)
		}
		hours, err := db.MetricDataForContract(metric, day, day+" 23:59:59", "hour", "SUM", false)
		if err != nil || len(hours) != 1 || hours[0].Qty != qty {
			t.Fatalf("hourly total=%+v want %v err=%v", hours, qty, err)
		}
		assertPointState(t, db, archiveSamplePrefix+metric, date, source, 20, appleHealthXMLOrigin)
	}
}

func TestHealthKitReadVersionsKeepExactMatchesUnitsAndDayBounds(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const day = "2026-07-01"
	const end = day + " 23:59:59"
	const nextDay = "2026-07-02"
	const latestDay = "2026-07-03"
	const source = "Watch|A"
	const cycling = "distance_cycling"
	receipt := insertTestRawRecord(t, db, "synthetic legacy observations")
	run, err := db.BeginAppleHealthXMLImport(ImportOptions{
		SnapshotAt: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC), UnverifiedCoverage: true, CatalogNormalized: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sample := MetricPoint{MetricName: cycling, Units: "km", Date: day + " 08:00:00 +0000", Source: source, Qty: 1}
	if err := run.AddPoints([]MetricPoint{sample}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), "UPDATE health_records SET received_at='2026-07-01 11:00:00+00' WHERE id=$1", receipt); err != nil {
		t.Fatal(err)
	}
	sample.Qty = 3
	if err := db.InsertPoints(receipt, []MetricPoint{sample}); err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []string{"minute", "hour", "day"} {
		points, err := db.MetricDataForContract(cycling, day, end, bucket, "AVG", true)
		if err != nil || len(points) != 1 || points[0].Qty != 3 {
			t.Fatalf("legacy %s=%+v err=%v", bucket, points, err)
		}
	}
	values, err := db.LatestMetricsForContract(true)
	if err != nil || !slices.ContainsFunc(values, func(p LatestValue) bool { return p.Metric == cycling && p.Value == 3 }) {
		t.Fatalf("legacy latest=%+v err=%v", values, err)
	}

	if _, err := db.pool.Exec(t.Context(), `
		INSERT INTO metric_points(health_record_id,metric_name,units,date,source,qty,origin,quality,source_snapshot_at)
		VALUES
			($1,'six_min_walk_distance','m','2026-07-01 09:00:00 +0000','Watch|A',500,'live','ok','2026-07-01 12:00:00+00'),
			($1,'six_minute_walking_test_distance','km','2026-07-01 09:00:00 +0000','Watch|A',0.5,'live','ok','2026-07-01 13:00:00+00'),
			($1,'height','m','2026-07-01 23:59:59 +0200','Watch|A',1.8,'live','ok',NULL),
			($1,'height','m','2026-07-01 23:59:59 +0200','Watch|B',2,'live','ok',NULL),
			($1,'height','m','2026-07-02 00:00:00 +0200','Watch|A',1.7,'live','ok',NULL),
			($1,'height','m','2026-07-03 10:00:00 +0200','Watch|A',1.6,'live','suspect',NULL)`, receipt); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{true, false} {
		points, err := db.MetricDataForContract("six_min_walk_distance", day, end, "hour", "AVG", legacy)
		if err != nil || len(points) != 1 || points[0].Qty != 500 {
			t.Fatalf("legacy=%t compatible unit=%+v err=%v", legacy, points, err)
		}
		points, err = db.MetricDataForContract("height", day, end, "hour", "AVG", legacy)
		if err != nil || len(points) != 1 || points[0].Date != day+" 23:00" || math.Abs(points[0].Qty-1.9) > 1e-6 {
			t.Fatalf("legacy=%t day boundary=%+v err=%v", legacy, points, err)
		}
		min, max, err := db.MetricDateRangeForContract("height", legacy)
		wantMax := nextDay
		if legacy {
			wantMax = latestDay
		}
		if err != nil || min != day || max != wantMax {
			t.Fatalf("legacy=%t range=%s to %s want %s to %s err=%v", legacy, min, max, day, wantMax, err)
		}
	}
	points, err := db.MetricDataBySourceForContract("height", day, end, "minute", "AVG", true)
	if err != nil || len(points) != 1 || points[0].Source != "Watch" || len(points[0].Points) != 1 ||
		math.Abs(points[0].Points[0].Qty-1.9) > 1e-6 {
		t.Fatalf("legacy minute device groups=%+v err=%v", points, err)
	}
}
