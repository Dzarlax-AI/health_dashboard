package notify

import (
	"strings"
	"testing"

	"health-receiver/internal/storage"
)

func TestParseContextPromptCallback(t *testing.T) {
	promptID, category, ok := parseContextPromptCallback("ctx:cp_abcdef:travel")
	if !ok {
		t.Fatal("callback should parse")
	}
	if promptID != "cp_abcdef" || category != storage.ContextPromptCategoryTravel {
		t.Fatalf("got prompt=%q category=%q", promptID, category)
	}
	for _, payload := range []string{
		"",
		"checkin:great:2026-06-20",
		"ctx::travel",
		"ctx:cp_abcdef:illness",
		"ctx:cp_abcdef:travel:2026-06-20",
	} {
		if _, _, ok := parseContextPromptCallback(payload); ok {
			t.Fatalf("payload %q should be rejected", payload)
		}
	}
}

func TestBuildContextPromptButtons_UsesOpaqueCallbackData(t *testing.T) {
	prompt := storage.ContextPromptInteraction{
		PromptID: "cp_abcdef",
		AllowedCategories: []string{
			storage.ContextPromptCategoryPoorSleep,
			storage.ContextPromptCategoryStress,
			storage.ContextPromptCategoryTravel,
			storage.ContextPromptCategoryUnknown,
			storage.ContextPromptCategorySkip,
		},
	}
	rows, text := buildContextPromptButtons("en", prompt)
	if text == "" {
		t.Fatal("prompt text should be localized")
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		for _, btn := range row {
			if len(btn.CallbackData) > 64 {
				t.Fatalf("callback too long: %q (%d)", btn.CallbackData, len(btn.CallbackData))
			}
			if !strings.HasPrefix(btn.CallbackData, "ctx:cp_abcdef:") {
				t.Fatalf("callback should be opaque prompt id + choice code, got %q", btn.CallbackData)
			}
			if strings.Contains(btn.CallbackData, "2026") || strings.Contains(btn.CallbackData, "low_sleep") || strings.Contains(btn.CallbackData, "context") {
				t.Fatalf("callback leaks semantic context: %q", btn.CallbackData)
			}
		}
	}
}
