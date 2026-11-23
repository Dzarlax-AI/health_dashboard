package ui

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/storage"
)

func TestChipDBTDisplay(t *testing.T) {
	if os.Getenv("READINESS_CHIP_SHADOW_COMPARE") != "1" {
		t.Skip("requires synthetic dbt chip comparison")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("READINESS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	pred, cutoff, revised := 0.375, 0.375, 0.5
	reason, active, waiting := "source_epoch_change", "active", "insufficient_positives"
	for _, tc := range []struct {
		id, mode string
		row      storage.OperationalContractRow
	}{
		{"pending", "snapshot", storage.OperationalContractRow{SubScore: "passive_efficiency"}},
		{"unknown", "snapshot", storage.OperationalContractRow{SubScore: "acute_risk", BaselineReason: &reason}},
		{"changed", "snapshot", storage.OperationalContractRow{SubScore: "chronic_load", SourceEpochChanged: true, PredictedValue: &pred}},
		{"continuous", "snapshot", storage.OperationalContractRow{SubScore: "recovery_stability", PredictedValue: &pred}},
		{"uncalibrated", "snapshot", storage.OperationalContractRow{SubScore: "acute_risk", PredictedValue: &pred}},
		{"inactive", "snapshot", storage.OperationalContractRow{SubScore: "chronic_load", PredictedValue: &pred, Cutoff: &cutoff, CalibrationStatus: &waiting}},
		{"at_cutoff", "snapshot", storage.OperationalContractRow{SubScore: "acute_risk", PredictedValue: &pred, Cutoff: &cutoff, CalibrationStatus: &active}},
		{"revised_cutoff", "history_replay", storage.OperationalContractRow{SubScore: "acute_risk", PredictedValue: &pred, Cutoff: &revised, CalibrationStatus: &active}},
		{"lost_capture", "unrecoverable", storage.OperationalContractRow{SubScore: "acute_risk", PredictedValue: &pred, Cutoff: &revised}},
	} {
		row := tc.row
		_, err := conn.Exec(ctx, "INSERT INTO source_data.chip_display_input VALUES($1,$2,$3,$4,$5,$6,$7,$8)", tc.id, tc.mode, row.SubScore, row.PredictedValue, row.Cutoff, row.CalibrationStatus, row.BaselineReason, row.SourceEpochChanged)
		if err != nil {
			t.Fatal(err)
		}
		var state *string
		var v *float64
		err = conn.QueryRow(ctx, "SELECT chip_state,numeric_value FROM dbt_output.chip_display_v1 WHERE case_id=$1", tc.id).Scan(&state, &v)
		if err != nil {
			t.Fatal(err)
		}
		if tc.mode == "unrecoverable" {
			if state != nil || v != nil {
				t.Fatal("uncaptured morning returned values")
			}
			continue
		}
		if state == nil {
			t.Fatal(tc.id)
		}
		text := *state
		if text == "value" {
			if v == nil {
				t.Fatal(tc.id)
			}
			text = fmt.Sprintf("%.3f", *v)
		}
		if want := buildChipCell(row).Text; text != want {
			t.Fatalf("%s: got %s want %s", tc.id, text, want)
		}
	}
}
