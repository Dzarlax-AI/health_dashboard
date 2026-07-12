package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildServerDoesNotExposeArbitrarySQL(t *testing.T) {
	s := buildServer(nil)

	response := s.HandleMessage(context.Background(), []byte(`{
		"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}
	}`))
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal tools/list response: %v", err)
	}
	listed := string(payload)

	if strings.Contains(listed, `"name":"sql_query"`) {
		t.Fatalf("tools/list exposes removed arbitrary SQL tool: %s", listed)
	}
	for _, name := range []string{"get_health_briefing", "get_metric_data", "get_workout"} {
		if !strings.Contains(listed, `"name":"`+name+`"`) {
			t.Errorf("tools/list no longer exposes typed tool %q: %s", name, listed)
		}
	}

	response = s.HandleMessage(context.Background(), []byte(`{
		"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{"name":"sql_query","arguments":{"query":"SELECT 1"}}
	}`))
	payload, err = json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal removed tool response: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(payload)), "tool not found") {
		t.Fatalf("sql_query remains callable or returned an unexpected response: %s", payload)
	}

	// Exercise a typed handler that validates its request before accessing storage.
	// This proves legitimate tools are not merely listed; their handlers remain callable.
	response = s.HandleMessage(context.Background(), []byte(`{
		"jsonrpc":"2.0","id":3,"method":"tools/call",
		"params":{"name":"get_workout","arguments":{}}
	}`))
	payload, err = json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal typed tool response: %v", err)
	}
	if !strings.Contains(string(payload), "external_id is required") {
		t.Fatalf("typed tool handler was not usable: %s", payload)
	}
}
