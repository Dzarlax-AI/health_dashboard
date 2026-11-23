package storage

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPersonalBaselineDBTShadow(t *testing.T) {
	port := os.Getenv("PERSONAL_BASELINE_FIXTURE_PORT")
	if port == "" {
		t.Skip("requires synthetic personal baseline runner")
	}
	ctx := t.Context()
	for _, profile := range []string{"fixture_one", "fixture_two"} {
		t.Run(profile, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig("postgres://postgres:synthetic-only@127.0.0.1:" + port + "/personal_baseline_fixture?sslmode=disable")
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.RuntimeParams["search_path"] = profile
			cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			db := &DB{pool: pool}
			rows, err := pool.Query(ctx, `select channel,anchor::text,zone,window_days,state,sample_count,
				age_seconds::bigint,eligible,baseline_median,mad_sd
				from derived.personal_baseline_v1 where profile=$1 order by capture`, profile)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var count int
			states := map[CalibrationState]int{}
			for rows.Next() {
				var channel BaselineChannel
				var day, zone string
				var window, n int
				var age int64
				var eligible bool
				var state CalibrationState
				var median, sd *float64
				if err := rows.Scan(&channel, &day, &zone, &window, &state, &n, &age, &eligible, &median, &sd); err != nil {
					t.Fatal(err)
				}
				loc, err := time.LoadLocation(zone)
				if err != nil {
					t.Fatal(err)
				}
				goResult, ok, err := db.PersonalBaselineContext(ctx, day, channel, window, loc)
				if err != nil {
					t.Fatal(err)
				}
				// Keep the pre-migration Go calculation as an independent oracle.
				anchor, err := time.ParseInLocation(time.DateOnly, day, loc)
				if err != nil {
					t.Fatal(err)
				}
				span := window
				if span <= 0 {
					span = 30
				}
				samples, newest, err := db.fetchBaselineSamplesContext(ctx, channel, anchor.AddDate(0, 0, -span), anchor, loc)
				if err != nil {
					t.Fatal(err)
				}
				wantState := classifyState(len(samples), newest, anchor)
				if channel == ChannelTemp && len(samples) < MinTempSamples {
					wantState = CalibrationCold
				}
				wantMedian, wantSD := computeMedianMADSD(samples)
				if goResult.State != wantState || goResult.SampleCount != len(samples) ||
					goResult.NewestAge != ageOf(newest, anchor) || (ok && (math.Abs(goResult.Median-wantMedian) > 1e-9 ||
					math.Abs(goResult.MADSD-math.Max(wantSD, channelSDFloor(channel))) > 1e-9)) {
					t.Fatalf("reader differs from Go oracle: %s %s window %d", channel, day, window)
				}
				if ok != eligible || goResult.State != state || goResult.SampleCount != n || goResult.NewestAge != time.Duration(age)*time.Second {
					t.Fatalf("%s %s window %d: Go %+v eligible=%t; SQL %s n=%d age=%ds eligible=%t", channel, day, window, goResult, ok, state, n, age, eligible)
				}
				if (median != nil) != ok || (sd != nil) != ok || (ok && (math.Abs(goResult.Median-*median) > 1e-9 || math.Abs(goResult.MADSD-*sd) > 1e-9)) {
					t.Fatalf("%s %s window %d median/MAD disagreement", channel, day, window)
				}
				count++
				states[state]++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if count != 130 || len(states) != 3 {
				t.Fatalf("unexpected count=%d states=%v", count, states)
			}
			t.Logf("%d baseline comparisons matched, states %v", count, states)
		})
	}

	t.Run("legacy_clock", func(t *testing.T) {
		cfg, err := pgxpool.ParseConfig("postgres://postgres:synthetic-only@127.0.0.1:" + port + "/personal_baseline_fixture?sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = "fixture_clock"
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		db := &DB{pool: pool}
		result, ok, err := db.PersonalBaselineContext(ctx, "2001-03-16", ChannelResp, 30, time.UTC)
		if err != nil || !ok || result.SampleCount != 7 || result.Median != 42 || result.MADSD != 0.5 ||
			result.NewestAge != 14*24*time.Hour+time.Nanosecond || result.State != CalibrationWarmup {
			t.Fatalf("nanosecond staleness boundary: result=%+v ok=%t err=%v", result, ok, err)
		}
		if _, _, err := db.PersonalBaselineContext(ctx, "2001-03-18", ChannelResp, 30, time.UTC); err == nil {
			t.Fatal("null source quantity must preserve the adapter read error")
		}
	})
}
