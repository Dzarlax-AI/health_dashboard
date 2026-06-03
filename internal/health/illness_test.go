package health

import (
	"math"
	"strings"
	"testing"
)

func TestComputeIllnessSuspicion_SingleMildRespiratoryCapsAtLow(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-06-02",
		RespiratoryRate: &MetricEvidenceInput{
			Metric: "respiratory_rate", Value: 18, Baseline: 16.8, ZScore: 1.6, Unit: "br/min", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceLow {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceLow, got)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_SingleLowSpO2MinimumNeverHigh(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-06-02",
		SpO2LowCluster: &SpO2ClusterEvidence{
			Status: "ok", ValidReadings: 8, Below94Count: 1, Below92Count: 1, Min: 91, Avg: 96.1,
		},
	})
	if got.Confidence != IllnessConfidenceNone {
		t.Fatalf("confidence = %q, want %q for isolated low SpO2; %+v", got.Confidence, IllnessConfidenceNone, got)
	}
	if len(got.Signals) != 1 || got.Signals[0].Strength != "weak" {
		t.Fatalf("signals = %+v, want one weak SpO2 context signal", got.Signals)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_RepeatedSpO2LowsPlusRespiratoryStrongIsModerate(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-06-02",
		RespiratoryRate: &MetricEvidenceInput{
			Metric: "respiratory_rate", Value: 18.2, Baseline: 16.8, ZScore: 2.2, Unit: "br/min", Method: "personal_baseline_mad", Status: "ok",
		},
		SpO2LowCluster: &SpO2ClusterEvidence{
			Status: "ok", ValidReadings: 10, Below94Count: 3, Below92Count: 0, Min: 93, Avg: 95.5,
		},
	})
	if got.Confidence != IllnessConfidenceModerate {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceModerate, got)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_RespiratoryPlusSleepOnlyStaysLow(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-04-11",
		RespiratoryRate: &MetricEvidenceInput{
			Metric: "respiratory_rate", Value: 18.1, Baseline: 16.8, ZScore: 1.6, Unit: "br/min", Method: "personal_baseline_mad", Status: "ok",
		},
		SleepDisruption: &MetricEvidenceInput{
			Metric: "sleep_total", Value: 5.5, Baseline: 7.2, ZScore: -1.2, Unit: "hr", Method: "daily_scores_mean_std", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceLow {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceLow, got)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_RespiratoryPlusSustainedHRLoadOnlyStaysLow(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-06-03",
		RespiratoryRate: &MetricEvidenceInput{
			Metric: "respiratory_rate", Value: 18.4, Baseline: 16.9, ZScore: 2.2, Unit: "br/min", Method: "daily_scores_mean_std", Status: "ok",
		},
		SustainedHRLoad: &MetricEvidenceInput{
			Metric: "sustained_hr_load", Value: 2.0, Baseline: 0, ZScore: 2.0, Method: "sustained_hr_load_z", Status: "ok", ActivityContext: "normal",
		},
	})
	if got.Confidence != IllnessConfidenceLow {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceLow, got)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_RepeatedAutonomicLoadWithRHRIsModerateProdrome(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:                 "2026-02-06",
		AutonomicPatternDays: 4,
		SustainedHRLoad: &MetricEvidenceInput{
			Metric: "sustained_hr_load", Value: 3.4, Baseline: 0, ZScore: 3.4, Method: "sustained_hr_load_z", Status: "ok", ActivityContext: "normal",
		},
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 70, Baseline: 60, ZScore: 1.2, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceModerate {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceModerate, got)
	}
	if got.Pattern != IllnessPatternAutonomicProdrome {
		t.Fatalf("pattern = %q, want %q; %+v", got.Pattern, IllnessPatternAutonomicProdrome, got)
	}
	if !hasSignal(got, "autonomic_prodrome", "autonomic_pattern") {
		t.Fatalf("signals = %+v, want autonomic prodrome pattern signal", got.Signals)
	}
	if strings.Contains(strings.ToLower(got.Reason), "respiratory") {
		t.Fatalf("autonomic prodrome reason should not claim respiratory evidence: %q", got.Reason)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_RepeatedAutonomicLoadNeedsRHRGate(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:                 "2026-02-05",
		AutonomicPatternDays: 3,
		SustainedHRLoad: &MetricEvidenceInput{
			Metric: "sustained_hr_load", Value: 7.7, Baseline: 0, ZScore: 7.7, Method: "sustained_hr_load_z", Status: "ok", ActivityContext: "normal",
		},
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 68, Baseline: 60, ZScore: 0.7, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceNone {
		t.Fatalf("confidence = %q, want none without RHR gate; %+v", got.Confidence, got)
	}
}

func TestComputeIllnessSuspicion_AutonomicProdromeRequiresKnownNormalActivity(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:                 "2026-02-06",
		AutonomicPatternDays: 4,
		SustainedHRLoad: &MetricEvidenceInput{
			Metric: "sustained_hr_load", Value: 3.4, Baseline: 0, ZScore: 3.4, Method: "sustained_hr_load_z", Status: "ok", ActivityContext: "unknown",
		},
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 70, Baseline: 60, ZScore: 1.2, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceLow {
		t.Fatalf("confidence = %q, want low from RHR only when activity context is unknown; %+v", got.Confidence, got)
	}
	if got.Pattern == IllnessPatternAutonomicProdrome {
		t.Fatalf("pattern = %q, want no autonomic prodrome without known-normal activity", got.Pattern)
	}
}

func TestComputeIllnessSuspicion_ThreeDayAutonomicLoadAllowsBorderlineRHR(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:                 "2026-02-05",
		AutonomicPatternDays: 3,
		SustainedHRLoad: &MetricEvidenceInput{
			Metric: "sustained_hr_load", Value: 7.7, Baseline: 0, ZScore: 7.7, Method: "sustained_hr_load_z", Status: "ok", ActivityContext: "normal",
		},
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 68, Baseline: 60, ZScore: 0.85, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceModerate {
		t.Fatalf("confidence = %q, want moderate for repeated load with borderline RHR; %+v", got.Confidence, got)
	}
	if got.Pattern != IllnessPatternAutonomicProdrome {
		t.Fatalf("pattern = %q, want autonomic prodrome", got.Pattern)
	}
}

func TestComputeIllnessSuspicion_AutonomicProdromeNeverHigh(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:                 "2026-02-08",
		AutonomicPatternDays: 4,
		SustainedHRLoad: &MetricEvidenceInput{
			Metric: "sustained_hr_load", Value: 8, Baseline: 0, ZScore: 8, Method: "sustained_hr_load_z", Status: "ok", ActivityContext: "normal",
		},
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 75, Baseline: 60, ZScore: 3, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence == IllnessConfidenceHigh {
		t.Fatalf("autonomic-only confidence = high, want capped below high; %+v", got)
	}
}

func TestComputeIllnessSuspicion_OxygenPlusAutonomicWithoutRespiratoryOrSickStaysLow(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-06-02",
		SpO2Average: &MetricEvidenceInput{
			Metric: "blood_oxygen_saturation", Value: 95.2, Baseline: 96.3, ZScore: -2.1, Unit: "%", Method: "daily_scores_mean_std", Status: "ok",
		},
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 59, Baseline: 53, ZScore: 1.8, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceLow {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceLow, got)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_ObjectivePersistenceIsModerateWithoutSubjective(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:                 "2026-06-03",
		ObjectivePatternDays: 2,
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 62, Baseline: 53, ZScore: 3, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceModerate {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceModerate, got)
	}
	if !hasSignal(got, "objective_illness_pattern", "objective_persistence") {
		t.Fatalf("signals = %+v, want objective persistence signal", got.Signals)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_ObjectivePersistenceAloneStaysNone(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:                 "2026-06-03",
		ObjectivePatternDays: 2,
	})
	if got.Confidence != IllnessConfidenceNone {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceNone, got)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_PersistenceDoesNotCreateHighWithoutAdditionalPrimary(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:                 "2026-06-03",
		ObjectivePatternDays: 3,
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 62, Baseline: 53, ZScore: 3, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceModerate {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceModerate, got)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_MildRespiratoryDoesNotCreateHigh(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-05-23",
		RespiratoryRate: &MetricEvidenceInput{
			Metric: "respiratory_rate", Value: 18.0, Baseline: 16.9, ZScore: 1.6, Unit: "br/min", Method: "daily_scores_mean_std", Status: "ok",
		},
		SpO2Average: &MetricEvidenceInput{
			Metric: "blood_oxygen_saturation", Value: 95.1, Baseline: 96.3, ZScore: -2.2, Unit: "%", Method: "daily_scores_mean_std", Status: "ok",
		},
		RHR: &MetricEvidenceInput{
			Metric: "resting_heart_rate", Value: 58, Baseline: 53, ZScore: 1.1, Unit: "bpm", Method: "personal_baseline_mad", Status: "ok",
		},
		WristTempDeviation: &MetricEvidenceInput{
			Metric: "wrist_temperature", Value: 35.5, Baseline: 35.1, ZScore: 1.1, Unit: "degC", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	if got.Confidence != IllnessConfidenceModerate {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceModerate, got)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_IllnessSignatureIsHigh(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date:        "2026-06-02",
		StressFlags: []string{"illness_signature"},
	})
	if got.Confidence != IllnessConfidenceHigh {
		t.Fatalf("confidence = %q, want %q; %+v", got.Confidence, IllnessConfidenceHigh, got)
	}
	if !hasSignal(got, "stress_flags", "stress_flag") {
		t.Fatalf("signals = %+v, want illness_signature stress flag signal", got.Signals)
	}
	assertNonDiagnosticWording(t, got)
}

func TestApplyIllnessSafetyCap_CapsPushHardForModerateIllness(t *testing.T) {
	resp := &BriefingResponse{
		EnergyBank:       &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "green"},
		IllnessSuspicion: &IllnessSuspicion{Confidence: IllnessConfidenceModerate},
	}
	ApplyIllnessSafetyCap(resp, GetStrings("en"))
	if resp.EnergyBank.ActionVerdict != "active_recovery" {
		t.Fatalf("verdict = %q, want active_recovery", resp.EnergyBank.ActionVerdict)
	}
	if !strings.Contains(resp.EnergyBank.VerdictReason, "Illness-like") {
		t.Fatalf("reason = %q, want illness cap reason", resp.EnergyBank.VerdictReason)
	}
}

func TestApplyIllnessSafetyCap_AutonomicProdromeUsesAutonomicReason(t *testing.T) {
	resp := &BriefingResponse{
		EnergyBank: &EnergyBank{ActionVerdict: "push_hard", VerdictReason: "green"},
		IllnessSuspicion: &IllnessSuspicion{
			Confidence: IllnessConfidenceModerate,
			Pattern:    IllnessPatternAutonomicProdrome,
		},
	}
	ApplyIllnessSafetyCap(resp, GetStrings("en"))
	if resp.EnergyBank.ActionVerdict != "active_recovery" {
		t.Fatalf("verdict = %q, want active_recovery", resp.EnergyBank.ActionVerdict)
	}
	if !strings.Contains(resp.EnergyBank.VerdictReason, "autonomic strain") {
		t.Fatalf("reason = %q, want autonomic prodrome reason", resp.EnergyBank.VerdictReason)
	}
}

func TestComputeIllnessSuspicion_HighActivityHRLoadDoesNotFalsePositive(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-06-02",
		SustainedHRLoad: &MetricEvidenceInput{
			Metric: "sustained_hr_load", Value: 2.1, Baseline: 0, ZScore: 2.1, Method: "sustained_hr_load_z", Status: "ok", ActivityContext: "high",
		},
	})
	if got.Confidence != IllnessConfidenceNone {
		t.Fatalf("confidence = %q, want %q for high-activity HR load alone; %+v", got.Confidence, IllnessConfidenceNone, got)
	}
	if len(got.Signals) != 1 || got.Signals[0].Status != "confounded" {
		t.Fatalf("signals = %+v, want confounded HR-load signal", got.Signals)
	}
	assertNonDiagnosticWording(t, got)
}

func TestComputeIllnessSuspicion_MissingAndWarmupAreExplicit(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-06-02",
		SpO2Average: &MetricEvidenceInput{
			Metric: "blood_oxygen_saturation", Status: "missing", Method: "personal_baseline_mad",
		},
		HRV: &MetricEvidenceInput{
			Metric: "heart_rate_variability", Status: "warmup", Method: "personal_baseline_mad",
		},
	})
	if got.Confidence != IllnessConfidenceNone {
		t.Fatalf("confidence = %q, want none for missing/warmup only; %+v", got.Confidence, got)
	}
	if !hasSignalStatus(got, "blood_oxygen_saturation", "missing") || !hasSignalStatus(got, "heart_rate_variability", "warmup") {
		t.Fatalf("signals = %+v, want explicit missing and warmup signals", got.Signals)
	}
}

func TestComputeIllnessSuspicion_NoNaNInfOutput(t *testing.T) {
	got := ComputeIllnessSuspicion(IllnessEvidenceInput{
		Date: "2026-06-02",
		RespiratoryRate: &MetricEvidenceInput{
			Metric: "respiratory_rate", Value: 18, Baseline: 16.8, ZScore: 2.2, Unit: "br/min", Method: "personal_baseline_mad", Status: "ok",
		},
	})
	for _, sig := range got.Signals {
		for name, ptr := range map[string]*float64{
			"value": sig.Value, "baseline": sig.Baseline, "delta_abs": sig.DeltaAbs, "z_score": sig.ZScore,
		} {
			if ptr != nil && (math.IsNaN(*ptr) || math.IsInf(*ptr, 0)) {
				t.Fatalf("%s emitted non-finite %s: %+v", sig.Metric, name, sig)
			}
		}
	}
}

func assertNonDiagnosticWording(t *testing.T, got *IllnessSuspicion) {
	t.Helper()
	joined := strings.ToLower(got.Reason)
	for _, sig := range got.Signals {
		joined += " " + strings.ToLower(sig.Evidence)
	}
	for _, banned := range []string{"you are sick", "likely sick", "you have low oxygen", "hypoxia"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("output contains banned wording %q: %+v", banned, got)
		}
	}
}

func hasSignal(got *IllnessSuspicion, metric, kind string) bool {
	for _, sig := range got.Signals {
		if sig.Metric == metric && sig.Kind == kind {
			return true
		}
	}
	return false
}

func hasSignalStatus(got *IllnessSuspicion, metric, status string) bool {
	for _, sig := range got.Signals {
		if sig.Metric == metric && sig.Status == status {
			return true
		}
	}
	return false
}
