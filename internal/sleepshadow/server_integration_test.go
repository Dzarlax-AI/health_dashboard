package sleepshadow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestShadowTransferAndLegacyReplay(t *testing.T) {
	dsn := os.Getenv("SLEEP_SHADOW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SLEEP_SHADOW_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !filepath.IsAbs(config.ConnConfig.Host) || len(config.ConnConfig.Fallbacks) != 0 {
		t.Fatal("test requires a single local PostgreSQL Unix socket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	handler, err := New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	profile := uuid.NewString()
	path := "/profiles/" + profile
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx))
		return response
	}
	encode := func(value any) []byte {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	checkStatus := func(response *httptest.ResponseRecorder, code int, expected string) {
		t.Helper()
		var result struct{ Status string }
		if response.Code != code || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Status != expected {
			t.Fatalf("want %d %s, got %d %s", code, expected, response.Code, response.Body.String())
		}
	}
	post := func(input candidate, status string) {
		t.Helper()
		checkStatus(request(http.MethodPost, path+"/v2/sleep", encode(input)), http.StatusOK, status)
	}
	makeCandidate := func(id string, rank int, end string, widths ...time.Duration) candidate {
		last, err := time.Parse(time.RFC3339, end)
		if err != nil {
			t.Fatal(err)
		}
		output := candidate{ID: id, Revision: 1, Priority: rank, Zone: "Europe/Belgrade"}
		for index, width := range widths {
			first := last.Add(-width)
			output.Stages = append(output.Stages, stage{ID: fmt.Sprintf("%s%d", id, index+1), Kind: "core", Start: first, End: last})
			last = first
		}
		return output
	}
	alias := legacyAlias{"health-sync-ios-v1", "Apple Watch", "healthkit-sleep-v1:synthetic-original"}
	a := makeCandidate("A", 0, "2026-01-26T22:30:00Z", 2*time.Hour, 2*time.Hour, 2*time.Hour)
	a.LegacyAliases = []legacyAlias{alias}
	a.Stages[1].Kind = "deep"
	a.Stages[2].Kind = "rem"
	a.ZeroStages = []string{"awake"}
	b := makeCandidate("B", 1, "2026-01-26T06:00:00Z", 3*time.Hour, 4*time.Hour)
	c := makeCandidate("C", 2, "2026-01-27T06:00:00Z", 2*time.Hour, 2*time.Hour, 2*time.Hour, 2*time.Hour)
	for i := range b.Stages {
		b.Stages[i].Kind = "asleep_unspecified"
	}
	corrected := a
	corrected.Revision = 2
	corrected.Zone = "Asia/Singapore"
	post(b, "applied")
	post(c, "applied")
	post(a, "applied")

	type dailyRow struct {
		Date        string   `json:"wake_date"`
		Candidate   string   `json:"candidate"`
		Revision    int      `json:"revision"`
		Seconds     float64  `json:"sleep_seconds"`
		Stages      []stage  `json:"stages"`
		Deep        *float64 `json:"deep_seconds"`
		REM         *float64 `json:"rem_seconds"`
		Core        *float64 `json:"core_seconds"`
		Unspecified *float64 `json:"unspecified_seconds"`
		Awake       *float64 `json:"awake_seconds"`
		Conflict    *bool    `json:"stage_conflict"`
	}
	read := func(expected ...dailyRow) {
		t.Helper()
		response := request(http.MethodGet, path+"/daily", nil)
		var rows []dailyRow
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &rows) != nil || !reflect.DeepEqual(rows, expected) {
			t.Fatalf("daily projection = %s, want %+v", response.Body.String(), expected)
		}
	}
	noConflict := false
	duration := func(v float64) *float64 { return &v }
	a26 := dailyRow{Date: "2026-01-26", Candidate: "A", Revision: 1, Seconds: 21600, Stages: a.Stages,
		Deep: duration(7200), REM: duration(7200), Core: duration(7200), Awake: duration(0), Conflict: &noConflict}
	b26 := dailyRow{Date: "2026-01-26", Candidate: "B", Revision: 1, Seconds: 25200, Stages: b.Stages,
		Unspecified: duration(25200), Conflict: &noConflict}
	c27 := dailyRow{Date: "2026-01-27", Candidate: "C", Revision: 1, Seconds: 28800, Stages: c.Stages,
		Core: duration(28800), Conflict: &noConflict}
	a27 := a26
	a27.Date, a27.Revision = "2026-01-27", 2
	read(a26, c27)

	// Emulate a previously persisted shadow row with no stage projection.
	exec(`UPDATE sleep_shadow.daily SET deep_seconds=NULL, rem_seconds=NULL, core_seconds=NULL,
		awake_seconds=NULL, stage_conflict=NULL WHERE profile=$1 AND candidate='A'`, profile)
	handler, err = New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	read(a26, c27)

	trigger := pgx.Identifier{"shadow_" + uuid.NewString()}.Sanitize()
	// Inject a failure on the second date, after the first date has been rebuilt.
	exec(fmt.Sprintf(`CREATE FUNCTION sleep_shadow.%s() RETURNS trigger LANGUAGE plpgsql AS $test$
		BEGIN
			IF NEW.profile = '%s' AND NEW.wake_date = DATE '2026-01-27' THEN
				RAISE EXCEPTION 'synthetic second-date failure';
			END IF;
			RETURN NEW;
		END; $test$;
		CREATE TRIGGER %s BEFORE INSERT ON sleep_shadow.daily
		FOR EACH ROW EXECUTE FUNCTION sleep_shadow.%s()`, trigger, profile, trigger, trigger))
	dropTrigger := func() {
		exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON sleep_shadow.daily; DROP FUNCTION IF EXISTS sleep_shadow.%s()", trigger, trigger))
	}
	defer dropTrigger()
	if got := request(http.MethodPost, path+"/v2/sleep", encode(corrected)); got.Code != http.StatusInternalServerError {
		t.Fatalf("expected failed transfer, got %d %s", got.Code, got.Body.String())
	}
	read(a26, c27)
	var pending int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sleep_shadow.pending WHERE profile=$1", profile).
		Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending correction after rollback: count=%d, err=%v", pending, err)
	}
	dropTrigger()
	restartedPool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer restartedPool.Close()
	handler, err = New(ctx, restartedPool)
	if err != nil {
		t.Fatal(err)
	}
	read(b26, a27)
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sleep_shadow.pending WHERE profile=$1", profile).
		Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending correction after recovery: count=%d, err=%v", pending, err)
	}

	legacy := func(date, generation string, includeCoverage bool) []byte {
		midnight, err := time.Parse("2006-01-02 15:04:05 -0700", date)
		if err != nil {
			t.Fatal(err)
		}
		var oldStages []map[string]any
		for _, point := range a.Stages {
			oldStages = append(oldStages, map[string]any{
				"date":   point.Start.In(midnight.Location()).Format("2006-01-02 15:04:05 -0700"),
				"source": alias.Source, "qty": point.End.Sub(point.Start).Hours(),
			})
		}
		data := map[string]any{"metrics": []map[string]any{
			{"name": "night_sleep_total", "units": "hr",
				"data": []map[string]any{{"date": date, "source": alias.Source, "qty": 6}}},
			{"name": "sleep_core", "units": "hr", "data": oldStages},
		}}
		if includeCoverage {
			noon := midnight.Add(12 * time.Hour)
			data["night_sleep_coverage"] = []map[string]any{{
				"wake_date": date[:10], "metric_date": date, "source": alias.Source,
				"source_epoch": alias.SourceEpoch, "sync_generation": generation,
				"capture_completeness":   "complete",
				"covered_interval_start": noon.AddDate(0, 0, -1).Format(time.RFC3339),
				"covered_interval_end":   noon.Format(time.RFC3339),
			}}
		}
		return encode(map[string]any{"data": data})
	}
	old := legacy("2026-01-26 00:00:00 +0100", alias.Generation, true)
	travel := legacy("2026-01-27 00:00:00 +0800", alias.Generation, true)
	checkStatus(request(http.MethodPost, path+"/v1/health", old), 200, "matched_legacy_repeat")
	checkStatus(request(http.MethodPost, path+"/v1/health", travel), 200, "matched_legacy_repeat")
	checkStatus(request(http.MethodPost, path+"/v1/health", legacy("2026-01-26 00:00:00 +0100", "unknown", true)), 200, "unresolved_legacy")
	checkStatus(request(http.MethodPost, path+"/v1/health", legacy("2026-01-26 00:00:00 +0100", "", false)), 200, "unresolved_legacy")
	exec("INSERT INTO sleep_shadow.legacy_alias VALUES ($1,'C',$2,$3,$4)", profile, alias.SourceEpoch, alias.Source, alias.Generation)
	checkStatus(request(http.MethodPost, path+"/v1/health", old), 200, "unresolved_legacy")
	post(a, "stale")
	post(corrected, "repeat")
	read(b26, a27)

	var candidates, stages, matched, unresolved, oldGeneration, newGeneration int
	err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM sleep_shadow.candidate WHERE profile=$1),
		(SELECT sum(jsonb_array_length(snapshot->'stages')) FROM sleep_shadow.candidate WHERE profile=$1),
		(SELECT count(*) FROM sleep_shadow.legacy_receipt WHERE profile=$1 AND classification='matched_legacy_repeat'),
		(SELECT count(*) FROM sleep_shadow.legacy_receipt WHERE profile=$1 AND classification='unresolved_legacy'),
		(SELECT generation FROM sleep_shadow.recalculation WHERE profile=$1 AND wake_date='2026-01-26'),
		(SELECT generation FROM sleep_shadow.recalculation WHERE profile=$1 AND wake_date='2026-01-27')`,
		profile).Scan(&candidates, &stages, &matched, &unresolved, &oldGeneration, &newGeneration)
	if err != nil || candidates != 3 || stages != 9 || matched != 2 || unresolved != 3 || oldGeneration != 3 || newGeneration != 2 {
		t.Fatalf("candidates=%d, stages=%d, legacy=%d/%d, generations=%d/%d: %v",
			candidates, stages, matched, unresolved, oldGeneration, newGeneration, err)
	}

	path = "/profiles/" + uuid.NewString()
	post(b, "applied")
	post(c, "applied")
	post(corrected, "applied")
	read(b26, a27)
	post(a, "stale")
	checkStatus(request(http.MethodPost, path+"/v1/health", old), 200, "matched_legacy_repeat")
	read(b26, a27)

	path = "/profiles/" + uuid.NewString()
	post(a, "applied")
	post(corrected, "applied")
	read(a27)
}
