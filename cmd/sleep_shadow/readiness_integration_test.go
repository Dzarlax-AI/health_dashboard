package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/health"
	"health-receiver/internal/sleepshadow"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func newLocalShadowTestDB(t *testing.T) (*storage.DB, *pgxpool.Pool, *pgxpool.Pool, string) {
	t.Helper()
	dsn := testdb.DSN(t)
	ctx := context.Background()
	bootstrap, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bootstrap.Close)
	schema := testdb.SchemaName("local_shadow_readiness_test")
	if err := testdb.CreateSchema(ctx, bootstrap, schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testdb.DropSchema(ctx, bootstrap, schema); err != nil {
			t.Error(err)
		}
	})
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	db := storage.NewFromPool(pool)
	t.Cleanup(db.Close)
	if err := db.EnsureAllTables(); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateSchemaContract(); err != nil {
		t.Fatal(err)
	}
	// As in the command, the tenant reader and shadow writer use separate pools.
	shadowPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shadowPool.Close)
	return db, pool, shadowPool, schema
}

func TestLocalReadinessUsesPublishedSleep(t *testing.T) {
	db, pool, shadowPool, _ := newLocalShadowTestDB(t)
	ctx := context.Background()
	shadow, err := sleepshadow.New(ctx, shadowPool)
	if err != nil {
		t.Fatal(err)
	}

	full, empty := uuid.NewString(), uuid.NewString()
	fullHTTP := withLocalReadiness(shadow, db, full)
	emptyHTTP := withLocalReadiness(shadow, db, empty)
	const oldDay, newDay = "2026-01-26", "2026-01-27"
	const a = `{"id":"A","revision":1,"priority":0,"wake_zone":"Europe/Belgrade","zero_stages":["awake"],
		"stages":[
		{"id":"A1","kind":"core","start_at":"2026-01-26T16:30:00Z","end_at":"2026-01-26T19:30:00Z"},
		{"id":"A2","kind":"deep","start_at":"2026-01-26T19:30:00Z","end_at":"2026-01-26T21:00:00Z"},
		{"id":"A3","kind":"rem","start_at":"2026-01-26T21:00:00Z","end_at":"2026-01-26T22:30:00Z"}]}`
	const b = `{"id":"B","revision":1,"priority":1,"wake_zone":"Europe/Belgrade",
		"stages":[{"id":"B1","kind":"asleep_unspecified","start_at":"2026-01-25T23:00:00Z","end_at":"2026-01-26T06:00:00Z"}]}`
	const c = `{"id":"C","revision":1,"priority":2,"wake_zone":"Europe/Belgrade","zero_stages":["awake"],
		"stages":[
		{"id":"C1","kind":"core","start_at":"2026-01-26T22:00:00Z","end_at":"2026-01-27T03:00:00Z"},
		{"id":"C2","kind":"deep","start_at":"2026-01-27T03:00:00Z","end_at":"2026-01-27T04:30:00Z"},
		{"id":"C3","kind":"rem","start_at":"2026-01-27T04:30:00Z","end_at":"2026-01-27T06:00:00Z"}]}`
	request := func(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	post := func(handler http.Handler, profile, packet, status string) {
		t.Helper()
		result := request(handler, http.MethodPost, "/profiles/"+profile+"/v2/sleep", packet)
		if result.Code != http.StatusOK || result.Body.String() != `{"status":"`+status+`"}`+"\n" {
			t.Fatalf("sync: %d %s", result.Code, result.Body.String())
		}
	}
	read := func(handler http.Handler, profile, date string) storage.LocalShadowReadiness {
		t.Helper()
		result := request(handler, http.MethodGet, "/profiles/"+profile+"/readiness?date="+date, "")
		var r storage.LocalShadowReadiness
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &r) != nil || r.Serving == nil {
			t.Fatalf("readiness: %d %s", result.Code, result.Body.String())
		}
		return r
	}
	for _, p := range []struct {
		handler http.Handler
		profile string
	}{{fullHTTP, full}, {emptyHTTP, empty}} {
		post(p.handler, p.profile, a, "applied")
		post(p.handler, p.profile, c, "applied")
	}
	post(fullHTTP, full, b, "applied")

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO daily_scores(date, hrv_avg, rhr_avg, sleep_total, steps)
		SELECT (date '2025-12-26'+i)::text,43+(i*7)%9,57+(i*3)%6,6.5+((i*5)%9)*0.25,4000
		FROM generate_series(0,30) s(i)`)
	exec(`INSERT INTO daily_scores(date,hrv_avg,rhr_avg,sleep_total,sleep_core,sleep_deep,sleep_rem,steps)
		VALUES ('2026-01-26',55,53,6,3,1.5,1.5,4000),('2026-01-27',54,54,8,5,1.5,1.5,4000)`)
	var recordID int64
	if err := pool.QueryRow(ctx, "INSERT INTO health_records DEFAULT VALUES RETURNING id").Scan(&recordID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO metric_points(health_record_id,metric_name,date,qty,units,source,quality)
		SELECT $1,'heart_rate_variability',day||' 08:0'||n||':00 +0000',hrv,'ms','Apple Watch','ok'
		FROM (VALUES ('2026-01-26',55),('2026-01-27',54)) d(day,hrv)
		CROSS JOIN generate_series(1,4) s(n)`, recordID)
	exec(`INSERT INTO metric_points(health_record_id,metric_name,date,qty,units,source,quality)
		SELECT $1,'resting_heart_rate',day||' 08:10:00 +0000',rhr,'bpm','Apple Watch','ok'
		FROM (VALUES ('2026-01-26',53),('2026-01-27',54)) d(day,rhr)`, recordID)
	exec(`INSERT INTO metric_points(health_record_id,metric_name,date,qty,units,source,quality)
		SELECT $1,metric,date||' 08:30:00 +0000',qty,'hr','Apple Watch','ok'
		FROM daily_scores CROSS JOIN LATERAL (VALUES ('sleep_total',sleep_total),
			('sleep_core',sleep_core),('sleep_deep',sleep_deep),('sleep_rem',sleep_rem)) x(metric,qty)
		WHERE date IN ('2026-01-26','2026-01-27') AND qty IS NOT NULL`, recordID)

	legacy := func(day string) *health.BriefingResponse {
		t.Helper()
		exec("TRUNCATE hourly_metrics")
		exec(`INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,sample_count,min_val,max_val)
			VALUES('step_count',$1,'synthetic',10,1,10,10)`, day+" 08")
		r, err := db.GetHealthBriefing("en")
		if err != nil || r == nil || r.Date != day || r.ReadinessServing == nil {
			t.Fatalf("legacy readiness for %s: %#v, %v", day, r, err)
		}
		return r
	}
	for _, day := range []string{oldDay, newDay} {
		before, current := legacy(day), read(fullHTTP, full, day)
		if current.Score != before.ReadinessDisplayScore || current.RawScore != before.ReadinessRawScore ||
			current.Band != before.ReadinessBand || current.Serving.Status != before.ReadinessServing.Status {
			t.Fatalf("same sleep inputs diverged at %s: %+v", day, current)
		}
	}
	moved := strings.Replace(strings.Replace(a, `"revision":1`, `"revision":2`, 1), "Europe/Belgrade", "Asia/Singapore", 1)
	post(fullHTTP, full, moved, "applied")
	post(emptyHTTP, empty, moved, "applied")
	post(fullHTTP, full, moved, "repeat")
	previous, current := read(fullHTTP, full, oldDay), read(fullHTTP, full, newDay)
	if s := previous.Sleep; previous.RawScore != 88 || previous.Score != 88 || previous.Band != "optimal" || previous.Serving.Status != "fresh" || s == nil ||
		s.Candidate == nil || *s.Candidate != "B" || s.Total == nil || *s.Total != 7 ||
		s.Unspecified == nil || *s.Unspecified != 7 || s.Deep != nil || s.REM != nil || s.Core != nil || s.Awake != nil {
		t.Fatalf("old date did not use the entire coarse B block: %+v, sleep=%+v", previous, s)
	}
	if s := current.Sleep; current.RawScore != 81 || current.Score != 65 || current.Band != "fair" || current.Serving.Reason != "sleep_debt_headline" ||
		s == nil || s.Candidate == nil || *s.Candidate != "A" || s.Revision == nil || *s.Revision != 2 ||
		s.Total == nil || *s.Total != 6 || s.Awake == nil || *s.Awake != 0 {
		t.Fatalf("new date did not use A revision 2 with measured zero: %+v, sleep=%+v", current, s)
	}
	if before := legacy(newDay); before.ReadinessDisplayScore != 91 {
		t.Fatalf("expected legacy path to still read the previous sleep points: %+v", before.ReadinessServing)
	}
	legacy(oldDay)
	if latest := read(fullHTTP, full, ""); latest.Date != newDay || latest.Score != current.Score {
		t.Fatalf("unexpected latest reading: %+v", latest)
	}
	absent := read(emptyHTTP, empty, oldDay)
	if absent.Sleep == nil || absent.Sleep.Candidate != nil || absent.Sleep.Total != nil || absent.Sleep.Awake != nil ||
		absent.Serving.Status != health.ReadinessServingMissing || absent.Serving.Reason != "missing_same_day_evidence" || absent.Score != 65 {
		t.Fatalf("cleared date fell back to old points: %+v", absent)
	}

	for _, check := range []struct {
		result storage.LocalShadowReadiness
		want   string
	}{{previous, health.ReadinessFreshnessOK}, {current, health.ReadinessFreshnessOK}, {absent, health.ReadinessFreshnessMissing}} {
		found := false
		for _, component := range check.result.Serving.Components {
			if component.Metric == "sleep_total" {
				found = component.Freshness == check.want
			}
		}
		if !found {
			t.Fatalf("expected sleep freshness %s, got %+v", check.want, check.result.Serving)
		}
	}

	unknownAwake := strings.Replace(strings.Replace(moved, `"revision":2`, `"revision":3`, 1), `"zero_stages":["awake"],`, "", 1)
	post(fullHTTP, full, unknownAwake, "applied")
	unknown := read(fullHTTP, full, newDay)
	if unknown.Sleep == nil || unknown.Sleep.Awake != nil || unknown.SleepQuality.Confidence != health.SleepQualityConfidencePartial || unknown.Score != current.Score {
		t.Fatalf("unknown awake inferred as zero: %+v", unknown)
	}
	zeroDeep := strings.Replace(strings.Replace(strings.Replace(moved, `"revision":2`, `"revision":4`, 1),
		`"zero_stages":["awake"]`, `"zero_stages":["awake","deep"]`, 1), `"kind":"deep"`, `"kind":"core"`, 1)
	post(fullHTTP, full, zeroDeep, "applied")
	zeros := read(fullHTTP, full, newDay)
	if zeros.Sleep == nil || zeros.Sleep.Deep == nil || *zeros.Sleep.Deep != 0 ||
		zeros.Sleep.Awake == nil || *zeros.Sleep.Awake != 0 || zeros.SleepQuality.Confidence != health.SleepQualityConfidenceFinal {
		t.Fatalf("confirmed zero phase was lost: %+v", zeros)
	}
	if other := request(fullHTTP, http.MethodGet, "/profiles/"+empty+"/readiness", ""); other.Code != http.StatusNotFound {
		t.Fatalf("route returned a profile outside its local binding: %d", other.Code)
	}
}
