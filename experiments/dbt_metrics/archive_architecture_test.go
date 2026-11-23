package dbtmetrics_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func markedArchivePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbName := os.Getenv("ARCHITECTURE_ARCHIVE_DB")
	if dbName == "" {
		t.Skip("requires an isolated architecture archive")
	}
	if len(dbName) != len("retained_0") || !strings.HasPrefix(dbName, "retained_") ||
		dbName[len(dbName)-1] < '0' || dbName[len(dbName)-1] > '4' {
		t.Fatal("invalid archive name")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://postgres@127.0.0.1:5432/"+dbName+"?sslmode=disable&search_path=source_data")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var guarded bool
	if err := pool.QueryRow(ctx, "select exists(select from public.architecture_fixture where token=$1)", os.Getenv("ARCHIVE_FIXTURE_TOKEN")).Scan(&guarded); err != nil || !guarded {
		t.Fatal("unverified fixture")
	}
	return pool
}

// Prepare only local, explicitly marked disposable archive inputs and oracle rows.
func TestPrepareArchiveArchitecture(t *testing.T) {
	ctx := t.Context()
	pool := markedArchivePool(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`create schema replay;
		create table replay.architecture_captures(profile text,capture text,from_day date,to_day date,preferred_source text);
		create table replay.architecture_segments(profile text,capture text,day text,ordinal bigint,metric text,
			source text,start_ns bigint,end_ns bigint,hours float8);
		create table replay.expected_days(day text primary key,value jsonb);
		create table replay.expected_windows(anchor text primary key,value jsonb)`)
	const stamp = "2006-01-02 15:04:05 -0700"
	rows, err := pool.Query(ctx, `select date,metric_name,source,qty from metric_points
		where metric_name in ('sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
		and qty>0 and quality='ok' and substring(date,12,8)!='00:00:00'
		order by date,metric_name collate "C",qty`)
	if err != nil {
		t.Fatal(err)
	}
	var segments [][]any
	origins := map[string]time.Time{}
	for rows.Next() {
		var date, metric, source string
		var hours float64
		if err := rows.Scan(&date, &metric, &source, &hours); err != nil {
			t.Fatal(err)
		}
		start, err := time.Parse(stamp, date)
		if err != nil || source == "" {
			continue
		}
		end := start.Add(time.Duration(hours * float64(time.Hour)))
		day := end.Format(time.DateOnly)
		origin, ok := origins[day]
		if !ok {
			origin = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, end.Location())
			origins[day] = origin
		}
		segments = append(segments, []any{"archive", "as_of", day, len(segments) + 1, metric, source,
			int64(start.Sub(origin)), int64(end.Sub(origin)), end.Sub(start).Hours()})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(segments) == 0 {
		return
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"replay", "architecture_segments"},
		strings.Fields("profile capture day ordinal metric source start_ns end_ns hours"), pgx.CopyFromRows(segments)); err != nil {
		t.Fatal(err)
	}
	exec(`insert into replay.architecture_captures select 'archive','as_of',min(day)::date,max(day)::date,
		coalesce((select value from settings where key='preferred_sleep_source'),'') from replay.architecture_segments`)
	var first, last time.Time
	if err := pool.QueryRow(ctx, "select from_day,to_day from replay.architecture_captures").Scan(&first, &last); err != nil {
		t.Fatal(err)
	}
	db := storage.NewFromPool(pool)
	write := func(table, key string, data [][]any) {
		t.Helper()
		if _, err := pool.CopyFrom(ctx, pgx.Identifier{"replay", table}, []string{key, "value"}, pgx.CopyFromRows(data)); err != nil {
			t.Fatal(err)
		}
	}
	for from := first; !from.After(last); from = from.AddDate(0, 0, 90) {
		to := from.AddDate(0, 0, 89)
		if to.After(last) {
			to = last
		}
		start, end := from.Format(time.DateOnly), to.Format(time.DateOnly)
		days, err := db.LoadSleepArchitectureDays(start, end)
		if err != nil {
			t.Fatal(err)
		}
		var values [][]any
		for day, row := range days {
			v, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			values = append(values, []any{day, json.RawMessage(v)})
		}
		write("expected_days", "day", values)
		windows, err := db.LoadSleepArchitectureFeatures(start, end)
		if err != nil {
			t.Fatal(err)
		}
		values = nil
		for day, row := range windows {
			v, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			values = append(values, []any{day, json.RawMessage(v)})
		}
		write("expected_windows", "anchor", values)
	}
}

// Prepare the same 14-day query window used by the live Go briefing.
func TestPrepareArchiveSRI(t *testing.T) {
	ctx := t.Context()
	pool := markedArchivePool(t)
	_, err := pool.Exec(ctx, `create table replay.sri_captures as select 'archive'::text as profile,'now'::text as capture,
          current_date as anchor,14 as window_days,coalesce((select value from settings where key='preferred_sleep_source'),'') as preferred_source;
          create table replay.sri_segments(profile text,capture text,observed_at timestamp,source text,metric_name text,qty float8,quality text);
          create table replay.expected_sri(nights int,sri float8)`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `select date,source,metric_name,qty from metric_points
        where metric_name in ('sleep_total','sleep_deep','sleep_core','sleep_unspecified','sleep_rem') and quality='ok'
        and qty>0 and source is not null and substring(date,12,8)<>'00:00:00'
        and substring(date,1,10)>=to_char(now()-interval '15 days','YYYY-MM-DD')`)
	if err != nil {
		t.Fatal(err)
	}
	var segments [][]any
	for rows.Next() {
		var stamp, source, metric string
		var qty float64
		if err := rows.Scan(&stamp, &source, &metric, &qty); err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse("2006-01-02 15:04:05 -0700", stamp)
		if err != nil {
			continue
		}
		// The source adapter applies the midnight gate before rounding to local minute.
		wall := time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), at.Second(), at.Nanosecond(), time.UTC)
		segments = append(segments, []any{"archive", "now", wall, source, metric, qty, "ok"})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"replay", "sri_segments"}, strings.Fields("profile capture observed_at source metric_name qty quality"), pgx.CopyFromRows(segments)); err != nil {
		t.Fatal(err)
	}
	score, nights, ok := storage.NewFromPool(pool).ComputeSRI(14)
	var value *float64
	if ok {
		value = &score
	}
	if _, err := pool.Exec(ctx, "insert into replay.expected_sri values($1,$2)", nights, value); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyArchiveNightQuality(t *testing.T) {
	pool := markedArchivePool(t)
	rows, err := pool.Query(t.Context(), `select total_hours,deep_hours,rem_hours,awake_hours,
        evidence_present,evidence_freshness,evidence_confidence,quality from archive_formula.semantic_sleep_quality_v1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var days, scored, diff int
	for rows.Next() {
		var total, deep, rem, awake *float64
		var evidence health.ReadinessComponentEvidence
		var got health.SleepQualityBreakdown
		if err := rows.Scan(&total, &deep, &rem, &awake, &evidence.Present, &evidence.Freshness, &evidence.Confidence, &got); err != nil {
			t.Fatal(err)
		}
		want := health.ComputeSleepQualityBreakdown(total, deep, rem, awake, evidence)
		days++
		if want.ScorePct != nil {
			scored++
		}
		if !reflect.DeepEqual(got, *want) {
			diff++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("days=%d scored=%d differences=%d", days, scored, diff)
	if diff > 0 {
		t.Fail()
	}
}
