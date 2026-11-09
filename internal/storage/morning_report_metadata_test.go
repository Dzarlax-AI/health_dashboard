package storage

import (
	"encoding/json"
	"strings"
	"testing"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

func TestMorningReportAIMetadataPreservesExplanationOrigins(t *testing.T) {
	hours := 7.0
	b := &health.BriefingResponse{Date: "2026-11-09"}
	sleep := health.MorningReportSleep{ReportDate: b.Date, Date: b.Date, Hours: &hours, Capture: health.NightCapturePartial, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional}
	blocks := map[string]string{ai.BlockSynthesis: "Server guidance", ai.BlockSleep: "Selected statement", ai.BlockRecovery: "Selected recovery", ai.BlockEnergy: "Model energy", ai.BlockYesterday: "Selected activity"}
	cfg := AIConfig{Provider: ai.ProviderOpenAI, Providers: map[string]AIProviderSettings{ai.ProviderOpenAI: {APIKey: "synthetic-secret-never-archive"}}}
	metadata := MorningReportAIMetadata(cfg, "en", b.Date, b, sleep, blocks, map[string]string{ai.BlockSleep: "matching-material"})
	var got struct {
		Origins map[string]string `json:"provenance"`
		Hashes  map[string]string `json:"matching_cache_hashes"`
	}
	if err := json.Unmarshal(metadata, &got); err != nil {
		t.Fatal(err)
	}
	if got.Origins[ai.BlockSynthesis] != "deterministic" || got.Origins[ai.BlockSleep] != "server_selected" || got.Origins[ai.BlockRecovery] != "server_selected" || got.Origins[ai.BlockEnergy] != "model" || got.Hashes[ai.BlockSleep] != "matching-material" {
		t.Fatalf("unexpected metadata: %s", metadata)
	}
	if strings.Contains(string(metadata), "synthetic-secret-never-archive") {
		t.Fatal("AI credentials leaked into report history")
	}
	sleep.Capture, sleep.Finalization = health.NightCaptureComplete, health.NightFinalFinal
	metadata = MorningReportAIMetadata(cfg, "en", b.Date, b, sleep, blocks, nil)
	if err := json.Unmarshal(metadata, &got); err != nil || got.Origins[ai.BlockSleep] != "model" {
		t.Fatalf("final-night explanation lost model origin: %s", metadata)
	}
}
