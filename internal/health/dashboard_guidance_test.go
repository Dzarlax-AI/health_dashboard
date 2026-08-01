package health

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestResolveDashboardTodayGuidance_Precedence(t *testing.T) {
	ls := GetStrings("en")
	tests := []struct {
		name       string
		energy     *EnergyBank
		readiness  *ReadinessServingState
		illness    *IllnessSuspicion
		sleep      *SleepQualityBreakdown
		wantAction string
		wantConf   string
		wantReason string
	}{
		{
			name:       "fresh evidence preserves push hard",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank and HRV support a hard day."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			sleep:      finalSleepQuality(88),
			wantAction: "push_hard",
			wantConf:   ReadinessConfidenceFinal,
			wantReason: "Bank and HRV support a hard day.",
		},
		{
			name:       "provisional readiness caps push hard",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingDataAccruing, ReadinessConfidenceProvisional),
			sleep:      finalSleepQuality(88),
			wantAction: "moderate",
			wantConf:   ReadinessConfidenceProvisional,
			wantReason: ls["dashboard_guidance_reason_readiness_pending"],
		},
		{
			name:       "partial sleep caps push hard",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			sleep:      &SleepQualityBreakdown{DurationPct: 88, Confidence: SleepQualityConfidencePartial},
			wantAction: "moderate",
			wantConf:   ReadinessConfidenceProvisional,
			wantReason: ls["dashboard_guidance_reason_sleep_partial"],
		},
		{
			name:       "missing sleep uses the low-confidence reason",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			sleep:      &SleepQualityBreakdown{Confidence: SleepQualityConfidenceMissing},
			wantAction: "moderate",
			wantConf:   ReadinessConfidenceLow,
			wantReason: ls["dashboard_guidance_reason_sleep_low"],
		},
		{
			name:       "moderate illness wins",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			illness:    &IllnessSuspicion{Confidence: IllnessConfidenceModerate},
			sleep:      finalSleepQuality(88),
			wantAction: "active_recovery",
			wantConf:   ReadinessConfidenceProvisional,
			wantReason: "Bank is charged.",
		},
		{
			name:       "high illness forces rest",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			illness:    &IllnessSuspicion{Confidence: IllnessConfidenceHigh},
			sleep:      finalSleepQuality(88),
			wantAction: "rest",
			wantConf:   ReadinessConfidenceLow,
			wantReason: "Bank is charged.",
		},
		{
			name:       "existing rest is never promoted",
			energy:     &EnergyBank{ActionVerdict: "rest", VerdictReason: "Bank is depleted."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			sleep:      finalSleepQuality(88),
			wantAction: "rest",
			wantConf:   ReadinessConfidenceFinal,
			wantReason: "Bank is depleted.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDashboardTodayGuidance(tt.energy, tt.readiness, tt.illness, tt.sleep, GetStrings("en"))
			if got == nil {
				t.Fatal("guidance = nil")
			}
			if got.Action != tt.wantAction {
				t.Fatalf("action = %q, want %q", got.Action, tt.wantAction)
			}
			if got.Confidence != tt.wantConf {
				t.Fatalf("confidence = %q, want %q", got.Confidence, tt.wantConf)
			}
			if got.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Label == "" || got.Summary == "" || got.Reason == "" {
				t.Fatalf("localized guidance incomplete: %+v", got)
			}
		})
	}
}

func TestResolveDashboardTodayGuidance_WithoutEnergyIsUnavailable(t *testing.T) {
	if got := ResolveDashboardTodayGuidance(nil, servingState(ReadinessServingFresh, ReadinessConfidenceFinal), nil, finalSleepQuality(90), GetStrings("en")); got != nil {
		t.Fatalf("guidance = %+v, want nil without EnergyBank", got)
	}
}

func TestResolveDashboardTodayGuidance_IllnessReasonHasPriority(t *testing.T) {
	const safetyReason = "Illness-like signals triggered a conservative safety cap."
	got := ResolveDashboardTodayGuidance(
		&EnergyBank{ActionVerdict: "rest", VerdictReason: safetyReason},
		servingState(ReadinessServingMissing, ReadinessConfidenceLow),
		&IllnessSuspicion{Confidence: IllnessConfidenceHigh},
		&SleepQualityBreakdown{Confidence: SleepQualityConfidenceMissing},
		GetStrings("en"),
	)
	if got == nil {
		t.Fatal("guidance = nil")
	}
	if got.Reason != safetyReason {
		t.Fatalf("reason = %q, want illness safety reason %q", got.Reason, safetyReason)
	}
}

func TestDashboardTodayGuidanceUpdatedAtJSONPresence(t *testing.T) {
	withoutTimestamp, err := json.Marshal(DashboardTodayGuidance{Action: "moderate"})
	if err != nil {
		t.Fatalf("marshal guidance without timestamp: %v", err)
	}
	if strings.Contains(string(withoutTimestamp), "updated_at") {
		t.Fatalf("missing timestamp was serialized: %s", withoutTimestamp)
	}

	zero := time.Time{}
	withExplicitZero, err := json.Marshal(DashboardTodayGuidance{Action: "moderate", UpdatedAt: &zero})
	if err != nil {
		t.Fatalf("marshal guidance with explicit zero timestamp: %v", err)
	}
	if !strings.Contains(string(withExplicitZero), `"updated_at":"0001-01-01T00:00:00Z"`) {
		t.Fatalf("explicit zero timestamp was not preserved: %s", withExplicitZero)
	}
}

func servingState(status, confidence string) *ReadinessServingState {
	return &ReadinessServingState{Status: status, Confidence: confidence}
}

func finalSleepQuality(score int) *SleepQualityBreakdown {
	return &SleepQualityBreakdown{ScorePct: &score, Confidence: SleepQualityConfidenceFinal}
}
