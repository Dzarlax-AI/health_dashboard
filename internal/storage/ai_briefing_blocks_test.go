package storage

import (
	"testing"

	"health-receiver/internal/ai"
)

func TestCanonicalAIBlockTextsPrefersSynthesisWithoutDeletingLegacyFallback(t *testing.T) {
	full := map[string]*AIBlock{
		"SLEEP":          {Block: "SLEEP", Text: "legacy sleep"},
		"YESTERDAY":      {Block: "YESTERDAY", Text: "legacy yesterday"},
		"RECOVERY":       {Block: "RECOVERY", Text: "legacy recovery"},
		"RECOMMENDATION": {Block: "RECOMMENDATION", Text: "legacy recommendation"},
		"SYNTHESIS":      {Block: "SYNTHESIS", Text: "one aligned explanation", InputsHash: "new"},
	}
	got := canonicalAIBlockTexts(full)
	if len(got) != 1 || got["SYNTHESIS"] != "one aligned explanation" {
		t.Fatalf("canonical blocks = %#v", got)
	}
	if len(full) != 5 || full["SLEEP"].Text != "legacy sleep" {
		t.Fatalf("legacy fallback was mutated: %#v", full)
	}
}

func TestCanonicalAIBlockTextsReturnsAlignedStructuredBundle(t *testing.T) {
	full := map[string]*AIBlock{
		"SYNTHESIS":      {Block: "SYNTHESIS", Text: "overview", InputsHash: "bundle"},
		"SLEEP":          {Block: "SLEEP", Text: "sleep", InputsHash: "bundle"},
		"YESTERDAY":      {Block: "YESTERDAY", Text: "activity", InputsHash: "bundle"},
		"RECOVERY":       {Block: "RECOVERY", Text: "recovery", InputsHash: "bundle"},
		"RECOMMENDATION": {Block: "RECOMMENDATION", Text: "recommendation", InputsHash: "bundle"},
	}
	got := canonicalAIBlockTexts(full)
	if len(got) != 5 || got["SLEEP"] != "sleep" || got["RECOMMENDATION"] != "recommendation" {
		t.Fatalf("canonical structured bundle = %#v", got)
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

func TestAIBundleCacheCompleteRequiresEveryAlignedBlock(t *testing.T) {
	full := make(map[string]*AIBlock)
	for _, block := range ai.GeneratedBlockOrder {
		full[block] = &AIBlock{Block: block, Text: "text", InputsHash: "bundle"}
	}
	if !aiBundleCacheComplete(full, "bundle") {
		t.Fatal("complete aligned bundle was treated as stale")
	}
	full[ai.BlockSleep].InputsHash = "old"
	if aiBundleCacheComplete(full, "bundle") {
		t.Fatal("mixed-hash bundle was treated as complete")
	}
}
