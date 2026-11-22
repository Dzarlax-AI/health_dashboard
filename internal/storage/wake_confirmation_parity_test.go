package storage

import (
	"os"
	"testing"
	"time"
)

// TestWakeCandidateDBTParity drives the storage reader through actual typed
// metric_points including its quality/date filter and detailed/summary priority.
func TestWakeCandidateDBTParity(t *testing.T) {
	if os.Getenv("PREFERRED_SLEEP_FIXTURE_TOKEN") == "" {
		t.Skip("synthetic sleep Docker fixture required")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	var token string
	if err := db.pool.QueryRow(ctx, "select token from public.sleep_fixture_identity").Scan(&token); err != nil || token != os.Getenv("PREFERRED_SLEEP_FIXTURE_TOKEN") {
		t.Fatal("synthetic fixture identity mismatch")
	}
	record := insertTestRawRecord(t, db, "wake-parity")
	rows, err := db.pool.Query(ctx, `select c.profile,c.capture,c.target_day::text,c.zone,r.state,r.source,r.signal,r.candidate_wake,r.latest_ingest,c.preferred_source,r.source_selection_reason
		from source_data.wake_segment_case c left join dbt_output.wake_candidate_v1 r using(profile,capture)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var checked int
	for rows.Next() {
		var profile, capture, day, zone, state string
		var source, signal, preferred, selectionReason *string
		var wake, received *time.Time
		if err := rows.Scan(&profile, &capture, &day, &zone, &state, &source, &signal, &wake, &received, &preferred, &selectionReason); err != nil {
			t.Fatal(err)
		}
		t.Run(profile+"_"+capture, func(t *testing.T) {
			tx, err := db.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, "select set_config('timezone',$1,true)", zone); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, `insert into metric_points(health_record_id,metric_name,units,date,qty,source,quality,received_at)
			  select $1,metric,'hr',to_char(start_at,'YYYY-MM-DD HH24:MI:SS TZHTZM'),hours,source,quality,received_at
			  from source_data.wake_segment where profile=$2 and capture=$3`, record, profile, capture)
			if err != nil {
				t.Fatal(err)
			}
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
			if preferred != nil {
				if _, err := tx.Exec(ctx, "insert into settings(key,value) values($1,$2) on conflict(key) do update set value=excluded.value", PreferredSleepSourceSetting, *preferred); err != nil {
					t.Fatal(err)
				}
			}
			goValue, ok, err := unit.loadWakeCandidateContext(ctx, day, loc)
			if err != nil {
				t.Fatal(err)
			}
			if capture == "tie" {
				expectedWake := time.Date(2026, 3, 30, 6, 45, 0, 0, loc)
				if !ok || goValue.Source != "Z Apple Watch" || !goValue.Wake.Equal(expectedWake) {
					t.Fatalf("equal-source wake winner: got %q at %v", goValue.Source, goValue.Wake)
				}
			}
			switch capture {
			case "invalid_watch", "absent_watch":
				want := time.Date(2026, 3, 30, 7, 0, 0, 0, loc)
				if !ok || goValue.Source != "RingConn" || !goValue.Wake.Equal(want) || goValue.SourceSelectionReason != "preferred_unavailable" {
					t.Fatalf("expected valid midnight fallback from RingConn at %v, got %+v (ok=%t)", want, goValue, ok)
				}
			case "automatic_invalid_watch":
				if ok {
					t.Fatal("automatic mode must preserve the existing no-data result")
				}
			}
			expected := map[string][2]string{
				"prefer_ring": {"RingConn", "preferred"}, "thin": {"RingConn", "preferred_unavailable"},
				"missing_pref": {"Z Apple Watch", "preferred_unavailable"}, "quality_first": {"Apple Watch", "preferred_unavailable"},
				"prefer_midnight": {"RingConn", "preferred"}, "invalid_midnight": {"Apple Watch", "preferred_unavailable"},
			}
			if v, asserted := expected[capture]; asserted && (!ok || goValue.Source != v[0] || goValue.SourceSelectionReason != v[1]) {
				t.Fatalf("source/reason got %q/%q want %v", goValue.Source, goValue.SourceSelectionReason, v)
			}
			if !ok {
				if state != "no_data" || wake != nil || source != nil || signal != nil || received != nil || selectionReason != nil {
					t.Fatal("empty candidate mismatch")
				}
			} else if state != "available" || selectionReason == nil || *selectionReason != goValue.SourceSelectionReason || wake == nil || received == nil || source == nil || signal == nil || !wake.Equal(goValue.Wake) || !received.Equal(goValue.LatestIngest) || *source != goValue.Source || *signal != goValue.Signal {
				t.Fatalf("candidate mismatch, Go wake %s source %s signal %s; SQL %s", goValue.Wake, goValue.Source, goValue.Signal, state)
			}
			if capture == "prefer_ring" || capture == "thin" {
				now := goValue.Wake.Add(3 * time.Hour)
				if _, err := unit.ComputeMorningWakeStatus(day, loc, now); err != nil {
					t.Fatal(err)
				}
				var persistedSource, persistedPreference, persistedReason string
				err := tx.QueryRow(ctx, `select metadata->>'input_source',metadata->>'preferred_source',metadata->>'source_selection_reason'
					from derived_metrics where metric_name=$1 and metric_date=$2`, DerivedMetricWakeTime, day).Scan(&persistedSource, &persistedPreference, &persistedReason)
				if err != nil || persistedSource != *source || persistedPreference != *preferred || persistedReason != *selectionReason {
					t.Fatalf("saved selection: %q %q %q (%v)", persistedSource, persistedPreference, persistedReason, err)
				}
			}
			checked++
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 19 {
		t.Fatalf("expected 19 candidate cases, got %d", checked)
	}
	t.Logf("candidate matches=%d, including an equal-source tie", checked)
}
