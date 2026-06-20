package storage

import (
	"strings"
	"testing"
)

func TestContextPromptInteractionsDDL_HasPrivacyAndLifecycleColumns(t *testing.T) {
	ddl := contextPromptInteractionsTableDDL()
	for _, col := range []string{
		"prompt_id          TEXT PRIMARY KEY",
		"signal_date        DATE NOT NULL",
		"prompt_local_date  DATE NOT NULL",
		"detected_reason    TEXT NOT NULL",
		"detector_version   TEXT NOT NULL",
		"status             TEXT NOT NULL",
		"category           TEXT",
		"source             TEXT",
		"prompt_message_id  BIGINT",
		"prompted_at        TIMESTAMPTZ",
		"expires_at         TIMESTAMPTZ NOT NULL",
		"answered_at        TIMESTAMPTZ",
		"allowed_categories JSONB NOT NULL DEFAULT '[]'::jsonb",
		"metadata           JSONB NOT NULL DEFAULT '{}'::jsonb",
		"UNIQUE (signal_date, detected_reason)",
	} {
		if !strings.Contains(ddl, col) {
			t.Errorf("DDL missing %q\n\nfull DDL:\n%s", col, ddl)
		}
	}
	if strings.Contains(strings.ToLower(ddl), "free_text") || strings.Contains(strings.ToLower(ddl), "note") {
		t.Fatalf("context prompt DDL must not add free-text note fields:\n%s", ddl)
	}
}

func TestContextPromptDailyDedupeIncludesSendFailed(t *testing.T) {
	indexDDL := contextPromptDailyDedupeIndexDDL()
	if !strings.Contains(indexDDL, "'send_failed'") {
		t.Fatal("send_failed must occupy the daily prompt slot to preserve at-most-once semantics")
	}
}

func TestValidateContextPromptCategory(t *testing.T) {
	for _, category := range DefaultContextPromptCategories {
		if err := ValidateContextPromptCategory(category); err != nil {
			t.Fatalf("category %q should be valid: %v", category, err)
		}
	}
	for _, category := range []string{"", "illness_context", "procedure", "custom_text"} {
		if err := ValidateContextPromptCategory(category); err == nil {
			t.Fatalf("category %q should be rejected", category)
		}
	}
}

func TestAvgStdDev(t *testing.T) {
	avg, sd := avgStdDev([]float64{7, 7, 8, 6})
	if avg != 7 {
		t.Fatalf("avg = %v, want 7", avg)
	}
	if sd < 0.70 || sd > 0.71 {
		t.Fatalf("sd = %v, want about 0.707", sd)
	}
}

func TestContextPromptRetentionSettingName(t *testing.T) {
	if SettingContextPromptRetentionDays != "context_prompt_retention_days" {
		t.Fatalf("retention setting name changed: %q", SettingContextPromptRetentionDays)
	}
}

func TestContextPromptCanAcceptAnswer_RecoversReservedPrompt(t *testing.T) {
	for _, status := range []string{ContextPromptStatusPrompted, ContextPromptStatusReserved} {
		if !contextPromptCanAcceptAnswer(status) {
			t.Fatalf("status %q should accept answers", status)
		}
	}
	for _, status := range []string{ContextPromptStatusAnswered, ContextPromptStatusSkipped, ContextPromptStatusExpired, ContextPromptStatusSendFailed} {
		if contextPromptCanAcceptAnswer(status) {
			t.Fatalf("status %q should not accept answers", status)
		}
	}
}

func TestEvaluateLowSleepContext_RejectsCaptureGap(t *testing.T) {
	got := evaluateLowSleepContext(contextSleepDay{
		Date:           "2026-06-20",
		SleepHours:     2.99,
		SourceCount:    1,
		MaxSourceSleep: 2.99,
	}, baselineDays(7.2))
	if got.Eligible {
		t.Fatalf("capture gap must not be eligible: %+v", got)
	}
	if got.Reason != "capture_gap" {
		t.Fatalf("reason = %q, want capture_gap", got.Reason)
	}
}

func TestEvaluateLowSleepContext_RejectsMaterialSourceConflict(t *testing.T) {
	got := evaluateLowSleepContext(contextSleepDay{
		Date:           "2026-01-03",
		SleepHours:     4.92,
		SourceCount:    2,
		MaxSourceSleep: 8.01,
	}, baselineDays(7.2))
	if got.Eligible {
		t.Fatalf("source conflict must not be eligible: %+v", got)
	}
	if got.Reason != "source_conflict" {
		t.Fatalf("reason = %q, want source_conflict", got.Reason)
	}
}

func TestEvaluateLowSleepContext_AllSourcesComparableLowPrompts(t *testing.T) {
	got := evaluateLowSleepContext(contextSleepDay{
		Date:           "2026-06-20",
		SleepHours:     4.9,
		SourceCount:    2,
		MaxSourceSleep: 5.4,
	}, baselineDays(7.2))
	if !got.Eligible {
		t.Fatalf("comparable low sleep should be eligible: %+v", got)
	}
	if got.Reason != "eligible" {
		t.Fatalf("reason = %q, want eligible", got.Reason)
	}
}

func TestEvaluateLowSleepContext_FiltersInvalidBaselineBeforeZScore(t *testing.T) {
	baseline := baselineDays(7.2)
	baseline = append(baseline,
		contextSleepDay{Date: "gap-1", SleepHours: 0.8, SourceCount: 1, MaxSourceSleep: 0.8},
		contextSleepDay{Date: "conflict-1", SleepHours: 4.9, SourceCount: 2, MaxSourceSleep: 8.1},
	)
	got := evaluateLowSleepContext(contextSleepDay{
		Date:           "2026-06-20",
		SleepHours:     5.4,
		SourceCount:    1,
		MaxSourceSleep: 5.4,
	}, baseline)
	if !got.Eligible {
		t.Fatalf("valid baseline after filtering should keep target eligible: %+v", got)
	}
	if got.BaselineDays != 14 {
		t.Fatalf("baseline days = %d, want 14 valid days after filtering", got.BaselineDays)
	}
	if got.BaselineAvg < 7.1 || got.BaselineAvg > 7.3 {
		t.Fatalf("baseline avg = %.3f, want invalid low days excluded", got.BaselineAvg)
	}
}

func TestEvaluateLowSleepPromptGate_VetoesExistingSickCheckin(t *testing.T) {
	got := evaluateLowSleepPromptGate(lowSleepPromptGateInput{
		Candidate:             eligibleLowSleepCandidate(),
		ExistingCheckinAnswer: CheckinAnswerSick,
		RepeatedShortNights:   2,
	})
	if got.Eligible {
		t.Fatalf("sick check-in should veto context prompt: %+v", got)
	}
	if got.Reason != "existing_sick_checkin" {
		t.Fatalf("reason = %q, want existing_sick_checkin", got.Reason)
	}
}

func TestEvaluateLowSleepPromptGate_VetoesActiveIllnessFlow(t *testing.T) {
	got := evaluateLowSleepPromptGate(lowSleepPromptGateInput{
		Candidate:           eligibleLowSleepCandidate(),
		IllnessConfidence:   "moderate",
		RepeatedShortNights: 2,
	})
	if got.Eligible {
		t.Fatalf("illness flow should veto context prompt: %+v", got)
	}
	if got.Reason != "active_illness_flow" {
		t.Fatalf("reason = %q, want active_illness_flow", got.Reason)
	}
}

func TestEvaluateLowSleepPromptGate_VetoesRecentEquivalentPrompt(t *testing.T) {
	got := evaluateLowSleepPromptGate(lowSleepPromptGateInput{
		Candidate:              eligibleLowSleepCandidate(),
		RecentEquivalentPrompt: true,
		RepeatedShortNights:    2,
	})
	if got.Eligible {
		t.Fatalf("recent equivalent prompt should veto: %+v", got)
	}
	if got.Reason != "recent_equivalent_prompt" {
		t.Fatalf("reason = %q, want recent_equivalent_prompt", got.Reason)
	}
}

func TestEvaluateLowSleepPromptGate_AllowsUsefulSignals(t *testing.T) {
	cases := []struct {
		name  string
		input lowSleepPromptGateInput
	}{
		{"sleep structure", lowSleepPromptGateInput{Candidate: eligibleLowSleepCandidate(), SleepAwakeHours: 1.1}},
		{"timing", lowSleepPromptGateInput{Candidate: eligibleLowSleepCandidate(), TimingDeviation: true}},
		{"trend", lowSleepPromptGateInput{Candidate: eligibleLowSleepCandidate(), RepeatedShortNights: 2}},
		{"strong anomaly", lowSleepPromptGateInput{Candidate: strongLowSleepCandidate()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateLowSleepPromptGate(tc.input)
			if !got.Eligible {
				t.Fatalf("expected useful prompt: %+v", got)
			}
		})
	}
}

func TestEvaluateLowSleepPromptGate_RejectsLowUsefulnessCandidate(t *testing.T) {
	candidate := eligibleLowSleepCandidate()
	candidate.SleepHours = 5.8
	candidate.ZScore = -1.6
	got := evaluateLowSleepPromptGate(lowSleepPromptGateInput{Candidate: candidate})
	if got.Eligible {
		t.Fatalf("weak isolated candidate should be low usefulness: %+v", got)
	}
	if got.Reason != "low_usefulness" {
		t.Fatalf("reason = %q, want low_usefulness", got.Reason)
	}
}

func TestEvaluateLowSleepPromptGate_CandidateQualityCannotBeBypassed(t *testing.T) {
	candidate := eligibleLowSleepCandidate()
	candidate.Eligible = false
	candidate.Reason = "source_conflict"
	got := evaluateLowSleepPromptGate(lowSleepPromptGateInput{
		Candidate:           candidate,
		SleepAwakeHours:     1.4,
		TimingDeviation:     true,
		RepeatedShortNights: 3,
	})
	if got.Eligible {
		t.Fatalf("quality-rejected candidate must not be rescued by usefulness evidence: %+v", got)
	}
	if got.Reason != "candidate_source_conflict" {
		t.Fatalf("reason = %q, want candidate_source_conflict", got.Reason)
	}
}

func eligibleLowSleepCandidate() LowSleepContextDetection {
	return LowSleepContextDetection{
		SignalDate:     "2026-06-20",
		SleepHours:     5.4,
		BaselineAvg:    7.2,
		BaselineStdDev: 0.8,
		BaselineDays:   14,
		ZScore:         -2.25,
		Eligible:       true,
		Reason:         "eligible",
	}
}

func strongLowSleepCandidate() LowSleepContextDetection {
	c := eligibleLowSleepCandidate()
	c.SleepHours = 4.4
	c.ZScore = -2.7
	return c
}

func baselineDays(center float64) []contextSleepDay {
	pattern := []float64{-0.4, -0.3, -0.2, -0.1, 0, 0.1, 0.2, 0.3, 0.4, -0.25, 0.25, -0.15, 0.15, 0.05}
	out := make([]contextSleepDay, 0, len(pattern))
	for _, delta := range pattern {
		sleep := center + delta
		out = append(out, contextSleepDay{
			Date:           "baseline",
			SleepHours:     sleep,
			SourceCount:    1,
			MaxSourceSleep: sleep,
		})
	}
	return out
}
