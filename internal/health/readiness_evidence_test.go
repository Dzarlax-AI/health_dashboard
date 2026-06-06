package health

import "testing"

func TestReadinessEvidence_MissingRHRCapsConfidentOptimal(t *testing.T) {
	d := RawMetrics{
		HRV:   append([]float64{90}, repeatFloat(40, 20)...),
		RHR:   append([]float64{55}, repeatFloat(55, 20)...),
		Sleep: repeatFloat(7.5, 21),
		ReadinessEvidence: &ReadinessEvidenceInput{
			Date:          "2026-06-04",
			HRV:           presentReadinessComponent("heart_rate_variability", 90, 4),
			RHR:           missingReadinessComponent("resting_heart_rate"),
			SleepDuration: presentReadinessComponent("sleep_total", 7.5, 0),
		},
	}

	got := computeReadinessWithEvidence(d)
	if got.RawScore <= readinessFairCap {
		t.Fatalf("test setup raw score = %d, want above cap", got.RawScore)
	}
	if got.DisplayScore > readinessFairCap {
		t.Fatalf("display score = %d, want capped <= %d", got.DisplayScore, readinessFairCap)
	}
	if got.Confidence != ReadinessConfidenceProvisional {
		t.Fatalf("confidence = %q, want provisional", got.Confidence)
	}
	if got.CapReason != "missing_same_day_evidence" {
		t.Fatalf("cap reason = %q", got.CapReason)
	}
}

func TestReadinessEvidence_SparseHRVCapsConfidence(t *testing.T) {
	d := RawMetrics{
		HRV:   append([]float64{90}, repeatFloat(40, 20)...),
		RHR:   repeatFloat(55, 21),
		Sleep: repeatFloat(7.5, 21),
		ReadinessEvidence: &ReadinessEvidenceInput{
			Date:          "2026-06-04",
			HRV:           presentReadinessComponent("heart_rate_variability", 90, 2),
			RHR:           presentReadinessComponent("resting_heart_rate", 55, 1),
			SleepDuration: presentReadinessComponent("sleep_total", 7.5, 0),
		},
	}
	d.ReadinessEvidence.HRV.Confidence = ReadinessConfidenceProvisional

	got := computeReadinessWithEvidence(d)
	if got.DisplayScore > readinessFairCap {
		t.Fatalf("display score = %d, want capped <= %d", got.DisplayScore, readinessFairCap)
	}
	if got.CapReason != "hrv_provisional" {
		t.Fatalf("cap reason = %q", got.CapReason)
	}
	if got.Serving == nil || got.Serving.Status != ReadinessServingDataAccruing {
		t.Fatalf("serving = %+v, want data_accruing", got.Serving)
	}
}

func TestReadinessEvidence_IllnessCapsDisplayNotRaw(t *testing.T) {
	d := RawMetrics{
		HRV:   append([]float64{90}, repeatFloat(40, 20)...),
		RHR:   repeatFloat(55, 21),
		Sleep: repeatFloat(7.5, 21),
		ReadinessEvidence: &ReadinessEvidenceInput{
			Date:              "2026-06-04",
			HRV:               presentReadinessComponent("heart_rate_variability", 90, 4),
			RHR:               presentReadinessComponent("resting_heart_rate", 55, 1),
			SleepDuration:     presentReadinessComponent("sleep_total", 7.5, 0),
			IllnessConfidence: IllnessConfidenceHigh,
		},
	}

	got := computeReadinessWithEvidence(d)
	if got.RawScore <= readinessLowCap {
		t.Fatalf("test setup raw score = %d, want above low cap", got.RawScore)
	}
	if got.DisplayScore > readinessLowCap {
		t.Fatalf("display score = %d, want capped <= %d", got.DisplayScore, readinessLowCap)
	}
	if got.Confidence != ReadinessConfidenceLow {
		t.Fatalf("confidence = %q, want low", got.Confidence)
	}
	if got.CapReason != "illness_suspicion_high" {
		t.Fatalf("cap reason = %q", got.CapReason)
	}
	if got.Serving == nil || got.Serving.Status != ReadinessServingCapped {
		t.Fatalf("serving = %+v, want capped", got.Serving)
	}
}

func TestApplyIllnessSafetyCap_UpdatesMetadataAtBoundary(t *testing.T) {
	resp := &BriefingResponse{
		ReadinessScore:        readinessFairCap,
		ReadinessDisplayScore: readinessFairCap,
		ReadinessRawScore:     readinessFairCap,
		ReadinessConfidence:   ReadinessConfidenceFinal,
		ReadinessBand:         ReadinessBand(readinessFairCap),
		RecoveryPct:           readinessFairCap,
		IllnessSuspicion:      &IllnessSuspicion{Confidence: IllnessConfidenceModerate},
	}

	ApplyIllnessSafetyCap(resp, GetStrings("en"))

	if resp.ReadinessDisplayScore != readinessFairCap {
		t.Fatalf("display score = %d, want %d", resp.ReadinessDisplayScore, readinessFairCap)
	}
	if resp.ReadinessConfidence != ReadinessConfidenceProvisional {
		t.Fatalf("confidence = %q", resp.ReadinessConfidence)
	}
	if resp.ReadinessCapReason != "illness_suspicion_moderate" {
		t.Fatalf("cap reason = %q", resp.ReadinessCapReason)
	}
	if resp.ReadinessServing == nil || resp.ReadinessServing.Status != ReadinessServingCapped {
		t.Fatalf("serving = %+v, want capped", resp.ReadinessServing)
	}
}

func TestApplyIllnessSafetyCap_HighIllnessReasonWinsOverSleepQuality(t *testing.T) {
	resp := &BriefingResponse{
		ReadinessScore:        readinessLowCap,
		ReadinessDisplayScore: readinessLowCap,
		ReadinessRawScore:     70,
		ReadinessConfidence:   ReadinessConfidenceLow,
		ReadinessCapReason:    "sleep_quality_low",
		ReadinessBand:         ReadinessBand(readinessLowCap),
		RecoveryPct:           readinessLowCap,
		IllnessSuspicion:      &IllnessSuspicion{Confidence: IllnessConfidenceHigh},
	}

	ApplyIllnessSafetyCap(resp, GetStrings("en"))

	if resp.ReadinessDisplayScore != readinessLowCap {
		t.Fatalf("display score = %d, want %d", resp.ReadinessDisplayScore, readinessLowCap)
	}
	if resp.ReadinessCapReason != "illness_suspicion_high" {
		t.Fatalf("cap reason = %q", resp.ReadinessCapReason)
	}
}

func TestReadinessEvidence_LowSleepQualityPenalizesAndCaps(t *testing.T) {
	d := RawMetrics{
		HRV:   append([]float64{70}, repeatFloat(40, 20)...),
		RHR:   repeatFloat(55, 21),
		Sleep: repeatFloat(7.5, 21),
		ReadinessEvidence: &ReadinessEvidenceInput{
			Date:          "2026-06-04",
			HRV:           presentReadinessComponent("heart_rate_variability", 70, 4),
			RHR:           presentReadinessComponent("resting_heart_rate", 55, 1),
			SleepDuration: presentReadinessComponent("sleep_total", 7.5, 0),
			SleepQuality:  presentReadinessComponent("sleep_quality", 5, 0),
		},
	}
	d.ReadinessEvidence.SleepQuality.Confidence = ReadinessConfidenceLow

	got := computeReadinessWithEvidence(d)
	if got.DisplayScore > readinessFairCap {
		t.Fatalf("display score = %d, want capped <= %d", got.DisplayScore, readinessFairCap)
	}
	if got.CapReason != "sleep_quality_low" {
		t.Fatalf("cap reason = %q", got.CapReason)
	}
	if got.Serving == nil || got.Serving.Status != ReadinessServingLowCoverage {
		t.Fatalf("serving = %+v, want low_coverage", got.Serving)
	}
}

func TestReadinessServingState_MissingCoreComponentWins(t *testing.T) {
	state := readinessServingState(ReadinessConfidenceProvisional, "missing_same_day_evidence", []ReadinessComponentSummary{
		{Metric: "heart_rate_variability", Present: true, Freshness: ReadinessFreshnessOK, Confidence: ReadinessConfidenceFinal},
		{Metric: "resting_heart_rate", Present: false, Freshness: ReadinessFreshnessMissing, Confidence: ReadinessConfidenceFinal, MissingReason: "missing_same_day_value"},
		{Metric: "sleep_total", Present: true, Freshness: ReadinessFreshnessOK, Confidence: ReadinessConfidenceFinal},
	})
	if state.Status != ReadinessServingMissing {
		t.Fatalf("status = %q, want missing", state.Status)
	}
	if state.Confidence != ReadinessConfidenceProvisional || state.Reason != "missing_same_day_evidence" {
		t.Fatalf("state = %+v, want provisional missing_same_day_evidence", state)
	}
}

func TestReadinessServingState_EvidenceFreeInputIsMissing(t *testing.T) {
	state := readinessServingState(ReadinessConfidenceFinal, "", nil)
	if state.Status != ReadinessServingMissing {
		t.Fatalf("status = %q, want missing", state.Status)
	}
	if state.Confidence != ReadinessConfidenceFinal {
		t.Fatalf("confidence = %q, want final", state.Confidence)
	}
}

func TestComputeBriefing_ExposesReadinessServingContract(t *testing.T) {
	d := RawMetrics{
		LastDate: "2026-06-04",
		HRV:      append([]float64{90}, repeatFloat(40, 20)...),
		RHR:      repeatFloat(55, 21),
		Sleep:    repeatFloat(7.5, 21),
		ReadinessEvidence: &ReadinessEvidenceInput{
			Date:          "2026-06-04",
			HRV:           presentReadinessComponent("heart_rate_variability", 90, 2),
			RHR:           presentReadinessComponent("resting_heart_rate", 55, 1),
			SleepDuration: presentReadinessComponent("sleep_total", 7.5, 0),
		},
	}
	d.ReadinessEvidence.HRV.Confidence = ReadinessConfidenceProvisional

	resp := ComputeBriefing(d, "en")
	if resp.ReadinessServing == nil {
		t.Fatal("readiness serving contract missing")
	}
	if resp.ReadinessServing.Status != ReadinessServingDataAccruing {
		t.Fatalf("serving status = %q, want data_accruing", resp.ReadinessServing.Status)
	}
	if len(resp.ReadinessServing.Components) == 0 {
		t.Fatal("serving components missing")
	}
}

func presentReadinessComponent(metric string, value float64, samples int) ReadinessComponentEvidence {
	return ReadinessComponentEvidence{
		Metric:        metric,
		Value:         &value,
		Present:       true,
		EvaluatedDate: "2026-06-04",
		SourceDate:    "2026-06-04",
		Freshness:     ReadinessFreshnessOK,
		SampleCount:   samples,
		Confidence:    ReadinessConfidenceFinal,
	}
}

func missingReadinessComponent(metric string) ReadinessComponentEvidence {
	return ReadinessComponentEvidence{
		Metric:        metric,
		EvaluatedDate: "2026-06-04",
		Freshness:     ReadinessFreshnessMissing,
		Confidence:    ReadinessConfidenceLow,
		MissingReason: "missing_same_day_value",
	}
}
