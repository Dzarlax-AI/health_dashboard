package storage

import (
	"encoding/json"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

// MorningReportAIMetadata describes only admitted text, without credentials or
// raw records. The same evidence policy determines provider selections.
func MorningReportAIMetadata(cfg AIConfig, lang, date string, briefing *health.BriefingResponse, sleep health.MorningReportSleep, blocks, hashes map[string]string) json.RawMessage {
	provenance := map[string]string{}
	for _, key := range ai.GeneratedBlockOrder {
		provenance[key] = "deterministic"
		if blocks[key] != "" {
			provenance[key] = "model"
		}
	}
	provenance[ai.BlockSynthesis], provenance[ai.BlockRecommendation] = "deterministic", "deterministic"
	if len(blocks) > 0 {
		evidence := health.BuildMorningInsightEvidenceWithOptions(briefing, nil, health.MorningInsightOptions{ExcludeSections: map[string]bool{"sleep": true, "activity": true, "cardio": true}})
		evidence.Date, evidence.NightSleep = date, &sleep
		facts := health.BuildInsightFactContext(health.BuildDailyInsightSnapshot(briefingForInsightSleep(briefing, sleep), lang))
		evidence.FactContext = &facts
		health.NormalizeMorningInsightEvidence(&evidence, lang)
		if health.PreliminaryMorningSleepExplanationEligible(evidence.NightSleep, date) {
			if !health.MorningSleepAnalysisEligible(&sleep, date) {
				provenance[ai.BlockSleep] = "server_selected"
			}
		}
		if options := evidence.PreliminaryOptions; options != nil {
			if len(options.Activity) > 0 {
				provenance[ai.BlockYesterday] = "server_selected"
			}
			if len(options.Recovery) > 0 {
				provenance[ai.BlockRecovery] = "server_selected"
			}
		}
	}
	var fingerprint ai.GenerationFingerprint
	if len(blocks) > 0 {
		_, _, fingerprint, _ = morningGenerationConfig(cfg)
	}
	metadata, _ := json.Marshal(map[string]any{"format_revision": "morning-domains-v2", "accepted_blocks": blocks, "matching_cache_hashes": hashes, "provenance": provenance, "generation": fingerprint})
	return metadata
}
