package storage

import (
	"math"
	"os"
	"reflect"
	"testing"

	"health-receiver/internal/health"
)

func TestBriefingProjectionShadow(t *testing.T) {
	if os.Getenv("BRIEFING_STORAGE_PILOT") != "1" {
		t.Skip("requires the synthetic briefing fixture")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	rows, err := db.pool.Query(ctx, `select inputs->'raw', inputs->'decision' from briefing_pilot.captured_inputs order by profile,capture`)
	if err != nil {
		t.Fatal(err)
	}
	type capture struct {
		raw      health.RawMetrics
		decision health.BriefingResponse
	}
	var cases []capture
	for rows.Next() {
		var c capture
		if err := rows.Scan(&c.raw, &c.decision); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		cases = append(cases, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("missing briefing fixtures")
	}
	for i, c := range cases {
		decision, err := db.dailyDecision(ctx, &c.decision)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decision, health.BuildDailyDecision(&c.decision)) {
			t.Fatalf("decision differs at capture %d", i)
		}
		for _, lang := range []string{"en", "ru", "sr"} {
			response, err := db.computeBriefingSQL(ctx, &c.raw, lang, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := health.ComputeBriefing(c.raw, lang)
			// PostgreSQL and the legacy Go energy accumulator can differ in the final bit of an HRV z-score.
			if response.EnergyBank != nil && want.EnergyBank != nil && math.Abs(response.EnergyBank.HRVZRaw-want.EnergyBank.HRVZRaw) <= 1e-9 {
				response.EnergyBank.HRVZRaw = want.EnergyBank.HRVZRaw
			}
			if !reflect.DeepEqual(response, want) {
				actual, expected := reflect.ValueOf(response).Elem(), reflect.ValueOf(want).Elem()
				for n := 0; n < actual.NumField(); n++ {
					if !reflect.DeepEqual(actual.Field(n).Interface(), expected.Field(n).Interface()) {
						t.Errorf("briefing differs at capture %d, language %s, field %s", i, lang, actual.Type().Field(n).Name)
					}
				}
				t.FailNow()
			}
		}
	}
}
