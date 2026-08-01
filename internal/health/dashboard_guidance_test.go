package health

import "testing"

func TestResolveDashboardTodayGuidance_Precedence(t *testing.T) {
	tests := []struct {
		name       string
		energy     *EnergyBank
		readiness  *ReadinessServingState
		illness    *IllnessSuspicion
		sleep      *SleepQualityBreakdown
		wantAction string
		wantConf   string
	}{
		{
			name:       "fresh evidence preserves push hard",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank and HRV support a hard day."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			sleep:      finalSleepQuality(88),
			wantAction: "push_hard",
			wantConf:   ReadinessConfidenceFinal,
		},
		{
			name:       "provisional readiness caps push hard",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingDataAccruing, ReadinessConfidenceProvisional),
			sleep:      finalSleepQuality(88),
			wantAction: "moderate",
			wantConf:   ReadinessConfidenceProvisional,
		},
		{
			name:       "partial sleep caps push hard",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			sleep:      &SleepQualityBreakdown{DurationPct: 88, Confidence: SleepQualityConfidencePartial},
			wantAction: "moderate",
			wantConf:   ReadinessConfidenceProvisional,
		},
		{
			name:       "moderate illness wins",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			illness:    &IllnessSuspicion{Confidence: IllnessConfidenceModerate},
			sleep:      finalSleepQuality(88),
			wantAction: "active_recovery",
			wantConf:   ReadinessConfidenceProvisional,
		},
		{
			name:       "high illness forces rest",
			energy:     &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "Bank is charged."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			illness:    &IllnessSuspicion{Confidence: IllnessConfidenceHigh},
			sleep:      finalSleepQuality(88),
			wantAction: "rest",
			wantConf:   ReadinessConfidenceLow,
		},
		{
			name:       "existing rest is never promoted",
			energy:     &EnergyBank{ActionVerdict: "rest", VerdictReason: "Bank is depleted."},
			readiness:  servingState(ReadinessServingFresh, ReadinessConfidenceFinal),
			sleep:      finalSleepQuality(88),
			wantAction: "rest",
			wantConf:   ReadinessConfidenceFinal,
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

func servingState(status, confidence string) *ReadinessServingState {
	return &ReadinessServingState{Status: status, Confidence: confidence}
}

func finalSleepQuality(score int) *SleepQualityBreakdown {
	return &SleepQualityBreakdown{ScorePct: &score, Confidence: SleepQualityConfidenceFinal}
}
