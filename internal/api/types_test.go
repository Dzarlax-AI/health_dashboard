package api

import (
	"encoding/json"
	"testing"
)

func TestAIBriefingResponseKeepsReleasedIOSCompatibilityFields(t *testing.T) {
	response := NewAIBriefingResponse(
		"2026-08-02",
		"en",
		"combined",
		[]AIBriefingSection{},
		map[string]string{
			"SLEEP":          "sleep body",
			"YESTERDAY":      "yesterday body",
			"RECOVERY":       "recovery body",
			"RECOMMENDATION": "recommendation body",
		},
		false,
		false,
	)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"date",
		"lang",
		"insight",
		"sections",
		"blocks",
		"sleep",
		"yesterday",
		"recovery",
		"recommendation",
		"generating",
		"disabled",
	} {
		if _, ok := payload[key]; !ok {
			t.Errorf("AI briefing compatibility field %q is missing", key)
		}
	}
}
