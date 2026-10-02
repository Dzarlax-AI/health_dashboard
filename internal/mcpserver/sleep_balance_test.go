package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/health"
)

type fakeSleepBalanceReader struct {
	today      string
	snapshot   *health.SleepDurationBalance
	readDate   string
	reads      int
	todayReads int
}

func (r *fakeSleepBalanceReader) Today() string {
	r.todayReads++
	return r.today
}

func (r *fakeSleepBalanceReader) GetSleepDurationBalance(_ context.Context, date string) (*health.SleepDurationBalance, error) {
	r.reads++
	r.readDate = date
	return r.snapshot, nil
}

func TestValidateSleepBalanceDateStrict(t *testing.T) {
	for _, date := range []string{"2026-10-03", "2000-02-29"} {
		if err := validateSleepBalanceDate(date); err != nil {
			t.Errorf("validateSleepBalanceDate(%q): %v", date, err)
		}
	}
	for _, date := range []string{"", "2026-2-03", "2026-02-30", "2026/10/03", " 2026-10-03"} {
		if err := validateSleepBalanceDate(date); err == nil {
			t.Errorf("validateSleepBalanceDate(%q) succeeded; want strict YYYY-MM-DD rejection", date)
		}
	}
}

func TestSleepBalanceToolIsReadOnlyAndRejectsBadDateBeforeStorage(t *testing.T) {
	s := server.NewMCPServer("test", "1", server.WithToolCapabilities(true))
	registerSleepBalanceTool(s)

	listed := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	payload, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("unmarshal tools/list: %v\n%s", err, payload)
	}
	var found *mcp.Tool
	for i := range response.Result.Tools {
		if response.Result.Tools[i].Name == "get_sleep_balance" {
			found = &response.Result.Tools[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("get_sleep_balance not listed: %s", payload)
	}
	if found.Annotations.ReadOnlyHint == nil || !*found.Annotations.ReadOnlyHint {
		t.Fatal("get_sleep_balance must advertise the read-only hint")
	}
	if found.Annotations.DestructiveHint == nil || *found.Annotations.DestructiveHint {
		t.Fatal("get_sleep_balance must advertise non-destructive behavior")
	}
	if found.OutputSchema.Type != "object" {
		t.Fatalf("get_sleep_balance output schema type = %q, want object", found.OutputSchema.Type)
	}
	if found.Annotations.IdempotentHint == nil || !*found.Annotations.IdempotentHint || found.Annotations.OpenWorldHint == nil || *found.Annotations.OpenWorldHint {
		t.Fatal("get_sleep_balance must advertise idempotent, closed-world behavior")
	}

	reader := &fakeSleepBalanceReader{today: "2026-10-03"}
	for _, arguments := range []string{
		`{"date":"2026-02-30"}`,
		`{"date":20261003}`,
		`{"date":"2026-10-03","schema":"other"}`,
		`{"date":"2026-10-03","username":"other"}`,
		`{"date":"2026-10-03","tenant":"other"}`,
	} {
		called := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_sleep_balance","arguments":`+arguments+`}}`))
		encoded, err := json.Marshal(called)
		if err != nil {
			t.Fatal(err)
		}
		var outcome struct {
			Result *struct {
				IsError bool `json:"isError"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(encoded, &outcome); err != nil {
			t.Fatalf("decode tool-call outcome: %v\n%s", err, encoded)
		}
		if outcome.Result == nil || !outcome.Result.IsError {
			t.Errorf("arguments %s did not return an error: %s", arguments, encoded)
		}
	}
	if reader.reads != 0 {
		t.Fatalf("invalid or unsupported arguments reached storage %d times", reader.reads)
	}
	badShape := sleepBalanceRequest(nil)
	badShape.Params.Arguments = []any{"2026-10-03"}
	badResult, err := handleSleepBalance(context.Background(), badShape, reader)
	if err != nil {
		t.Fatal(err)
	}
	if !badResult.IsError || reader.reads != 0 {
		t.Fatalf("non-object arguments were not rejected before storage: result=%#v reads=%d", badResult, reader.reads)
	}
}

func TestSleepBalanceHandlerReadsExactDateAndReturnsCompleteSnapshot(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	date, _ := time.ParseInLocation("2006-01-02", "2026-10-25", loc)
	windowStart := date.AddDate(0, 0, -(health.SleepDurationBalanceWindowDays - 1))
	periods := make([]health.SleepDurationBalancePeriod, 0, health.SleepDurationBalanceWindowDays)
	for offset := 0; offset < health.SleepDurationBalanceWindowDays; offset++ {
		wakeDate := windowStart.AddDate(0, 0, offset)
		previousDate := wakeDate.AddDate(0, 0, -1)
		start := time.Date(previousDate.Year(), previousDate.Month(), previousDate.Day(), 12, 0, 0, 0, loc)
		end := time.Date(wakeDate.Year(), wakeDate.Month(), wakeDate.Day(), 12, 0, 0, 0, loc)
		goalHours := 7.5
		if wakeDate.Format("2006-01-02") >= "2026-10-20" {
			goalHours = 8
		}
		periods = append(periods, health.SleepDurationBalancePeriod{
			WakeDate: wakeDate.Format("2006-01-02"), Start: start, End: end,
			GoalHours: goalHours, TotalSleepHours: 8, DeltaHours: 8 - goalHours,
			CaptureState: health.SleepBalanceCoverageComplete,
		})
	}
	balance := health.SleepDurationBalance{
		WakeDate: date.Format("2006-01-02"), WindowStartDate: windowStart.Format("2006-01-02"),
		CalculatedThrough: time.Date(date.Year(), date.Month(), date.Day(), 12, 0, 0, 0, loc),
		State:             health.SleepBalanceStateComplete, Confidence: health.SleepBalanceConfidenceNormal,
		BalanceHours: floatPointer(0.5), LastCompleteDate: date.Format("2006-01-02"), Periods: periods,
	}
	reader := &fakeSleepBalanceReader{today: "2026-10-25", snapshot: &balance}
	req := sleepBalanceRequest(map[string]any{"date": "2026-10-25"})
	result, err := handleSleepBalance(context.Background(), req, reader)
	if err != nil {
		t.Fatal(err)
	}
	if reader.reads != 1 || reader.readDate != "2026-10-25" || reader.todayReads != 0 {
		t.Fatalf("reader calls = reads:%d date:%q today:%d", reader.reads, reader.readDate, reader.todayReads)
	}
	output := decodeSleepBalanceResult(t, result)
	if output.WakeDate != date.Format("2006-01-02") || output.Balance == nil || output.Balance.State != health.SleepBalanceStateComplete {
		t.Fatalf("output = %#v", output)
	}
	if output.Balance.BalanceHours == nil || *output.Balance.BalanceHours != 0.5 {
		t.Fatalf("balance_hours = %#v", output.Balance.BalanceHours)
	}
	if len(output.Balance.Periods) != 14 {
		t.Fatalf("periods = %d, want 14", len(output.Balance.Periods))
	}
	if !output.Balance.CalculatedThrough.Equal(balance.CalculatedThrough) {
		t.Fatalf("calculated_through = %s, want %s", output.Balance.CalculatedThrough, balance.CalculatedThrough)
	}
	if output.Balance.Periods[7].GoalHours != 7.5 || output.Balance.Periods[8].GoalHours != 8 {
		t.Fatalf("effective goals did not survive conversion: %#v %#v", output.Balance.Periods[7], output.Balance.Periods[8])
	}
	if got := output.Balance.Periods[13].End.Sub(output.Balance.Periods[13].Start); got != 25*time.Hour {
		t.Fatalf("DST-crossing period duration = %s, want 25h", got)
	}
	if output.Balance.Periods[13].CaptureState != health.SleepBalanceCoverageComplete {
		t.Fatalf("capture state = %q, want complete", output.Balance.Periods[13].CaptureState)
	}
	if !output.Balance.Periods[len(output.Balance.Periods)-1].End.Equal(time.Date(2026, 10, 25, 12, 0, 0, 0, loc)) {
		t.Fatalf("last noon boundary = %s", output.Balance.Periods[len(output.Balance.Periods)-1].End)
	}
}

func TestSleepBalanceHandlerKeepsPartialNullAndMissingSnapshotExplicit(t *testing.T) {
	partial := &health.SleepDurationBalance{
		WakeDate: "2026-10-03", WindowStartDate: "2026-09-20",
		CalculatedThrough: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC),
		State:             health.SleepBalanceStateIncomplete, Confidence: health.SleepBalanceConfidenceLow,
		IncompleteReason: "goal_missing", Periods: []health.SleepDurationBalancePeriod{},
	}
	reader := &fakeSleepBalanceReader{today: "2026-10-03", snapshot: partial}
	result, err := handleSleepBalance(context.Background(), sleepBalanceRequest(nil), reader)
	if err != nil {
		t.Fatal(err)
	}
	partialOutput := decodeSleepBalanceResult(t, result)
	if reader.todayReads != 1 || reader.reads != 1 || reader.readDate != "2026-10-03" {
		t.Fatalf("default-date reader calls = %#v", reader)
	}
	if partialOutput.Balance == nil || partialOutput.Balance.State != health.SleepBalanceStateIncomplete || partialOutput.Balance.BalanceHours != nil {
		t.Fatalf("partial snapshot = %#v, want incomplete/null balance", partialOutput)
	}

	reader.snapshot = nil
	result, err = handleSleepBalance(context.Background(), sleepBalanceRequest(map[string]any{"date": "2026-10-03"}), reader)
	if err != nil {
		t.Fatal(err)
	}
	missingOutput := decodeSleepBalanceResult(t, result)
	if missingOutput.WakeDate != "2026-10-03" || missingOutput.Balance != nil {
		t.Fatalf("missing snapshot output = %#v, want explicit null balance", missingOutput)
	}
}

func sleepBalanceRequest(arguments map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: arguments}}
}

func decodeSleepBalanceResult(t *testing.T, result *mcp.CallToolResult) sleepBalanceToolOutput {
	t.Helper()
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("result content type = %T, want text", result.Content[0])
	}
	var output sleepBalanceToolOutput
	if err := json.Unmarshal([]byte(content.Text), &output); err != nil {
		t.Fatalf("decode result: %v\n%s", err, content.Text)
	}
	return output
}

func floatPointer(value float64) *float64 { return &value }
