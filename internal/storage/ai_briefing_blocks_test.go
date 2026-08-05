package storage

import "testing"

func TestCanonicalAIBlockTextsPrefersSynthesisWithoutDeletingLegacyFallback(t *testing.T) {
	full := map[string]*AIBlock{
		"SLEEP":          {Block: "SLEEP", Text: "legacy sleep"},
		"YESTERDAY":      {Block: "YESTERDAY", Text: "legacy yesterday"},
		"RECOVERY":       {Block: "RECOVERY", Text: "legacy recovery"},
		"RECOMMENDATION": {Block: "RECOMMENDATION", Text: "legacy recommendation"},
		"SYNTHESIS":      {Block: "SYNTHESIS", Text: "one aligned explanation"},
	}
	got := canonicalAIBlockTexts(full)
	if len(got) != 1 || got["SYNTHESIS"] != "one aligned explanation" {
		t.Fatalf("canonical blocks = %#v", got)
	}
	if len(full) != 5 || full["SLEEP"].Text != "legacy sleep" {
		t.Fatalf("legacy fallback was mutated: %#v", full)
	}
}

func TestCanonicalAIBlockTextsFallsBackToLegacyBeforeSynthesisExists(t *testing.T) {
	full := map[string]*AIBlock{
		"SLEEP":          {Block: "SLEEP", Text: "legacy sleep"},
		"RECOMMENDATION": {Block: "RECOMMENDATION", Text: "legacy recommendation"},
	}
	got := canonicalAIBlockTexts(full)
	if got["SLEEP"] != "legacy sleep" || got["RECOMMENDATION"] != "legacy recommendation" {
		t.Fatalf("legacy fallback = %#v", got)
	}
}
