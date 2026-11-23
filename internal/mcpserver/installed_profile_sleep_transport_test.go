package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"health-receiver/internal/api"
	"health-receiver/internal/storage"
)

func TestInstalledProfileSleepTransport(t *testing.T) {
	if os.Getenv("PROFILE_GRAPH_FIXTURE_TOKEN") == "" {
		t.Skip("run the installed profile graph probe")
	}
	ctx := t.Context()
	dsn, source := os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA")
	db, err := storage.NewLocalProfileGraphReader(ctx, dsn, source, os.Getenv("PROFILE_GRAPH_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resolve := func(_ context.Context, key string) (*storage.DB, string, bool, bool) {
		return db, source, false, key == "fixture-key"
	}
	handler := withAuth(newStatelessTransport(buildServer(resolve)), resolve, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_sleep_duration_comparison","arguments":{"date":"1981-03-15"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-API-Key", "fixture-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	payload := w.Body.Bytes()
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(string(payload), "\n") {
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				payload = []byte(strings.TrimSpace(data))
				break
			}
		}
	}
	var response struct {
		Result struct {
			Structured api.SleepDurationComparisonResponse `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || w.Code != http.StatusOK {
		t.Fatalf("MCP returned HTTP %d: %s", w.Code, payload)
	}
	device := "Graph Watch"
	if source == "peer_source" {
		device = "Peer Graph Watch"
	}
	f := response.Result.Structured.Comparison
	if f == nil || f.DeltaMinutes == nil || *f.DeltaMinutes != -42 ||
		f.PreferredSleepSource != device || f.Source != device || !reflect.DeepEqual(f, db.GetSleepDurationComparison(ctx, "1981-03-15")) {
		t.Fatalf("MCP comparison: %+v", f)
	}
}
