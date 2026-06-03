package health

const (
	IllnessConfidenceNone     = "none"
	IllnessConfidenceLow      = "low"
	IllnessConfidenceModerate = "moderate"
	IllnessConfidenceHigh     = "high"

	IllnessPatternRespiratory       = "respiratory"
	IllnessPatternOxygen            = "oxygen"
	IllnessPatternAutonomicProdrome = "autonomic_prodrome"
	IllnessPatternMixed             = "mixed"
)

type illnessSignalEval struct {
	signal            IllnessEvidenceSignal
	contributes       bool
	objective         bool
	primary           bool
	autoOrTemp        bool
	strong            bool
	illnessSignature  bool
	strongConfounder  bool
	autonomicProdrome bool
}

// ComputeIllnessSuspicion converts a date-aligned evidence input into an
// experimental, non-diagnostic illness/respiratory-stress evidence object.
// It deliberately does not read RawMetrics slices and does not affect
// EnergyBank verdicts or stress flags.
func ComputeIllnessSuspicion(in IllnessEvidenceInput) *IllnessSuspicion {
	var evals []illnessSignalEval

	evals = append(evals, evalMetric(in.RespiratoryRate, metricEvalSpec{
		metric: "respiratory_rate", role: "primary", category: "respiratory",
		direction: "high", mildZ: 1.5, strongZ: 2.0, highIsBad: true,
		mildText:   "Respiratory rate is elevated versus personal baseline.",
		strongText: "Respiratory rate is strongly elevated versus personal baseline.",
	})...)
	evals = append(evals, evalSpO2Average(in.SpO2Average)...)
	evals = append(evals, evalSpO2Cluster(in.SpO2LowCluster)...)
	evals = append(evals, evalMetric(in.RHR, metricEvalSpec{
		metric: "resting_heart_rate", role: "support", category: "autonomic",
		direction: "high", mildZ: 1.0, strongZ: 1.5, highIsBad: true,
		mildText:   "Resting heart rate is above personal baseline.",
		strongText: "Resting heart rate is strongly above personal baseline.",
	})...)
	evals = append(evals, evalMetric(in.HRV, metricEvalSpec{
		metric: "heart_rate_variability", role: "support", category: "autonomic",
		direction: "low", mildZ: -1.0, strongZ: -1.5, highIsBad: false,
		mildText:   "HRV is below personal baseline.",
		strongText: "HRV is strongly below personal baseline.",
	})...)
	evals = append(evals, evalMetric(in.WristTempDeviation, metricEvalSpec{
		metric: "wrist_temperature", role: "support", category: "temperature",
		direction: "high", mildZ: 1.0, strongZ: 1.5, highIsBad: true,
		mildText:   "Wrist temperature is above personal baseline.",
		strongText: "Wrist temperature is strongly above personal baseline.",
	})...)
	evals = append(evals, evalMetric(in.SleepDisruption, metricEvalSpec{
		metric: "sleep_disruption", role: "support", category: "sleep",
		direction: "low", mildZ: -1.0, strongZ: -1.5, highIsBad: false,
		mildText:   "Sleep was disrupted versus personal baseline.",
		strongText: "Sleep was strongly disrupted versus personal baseline.",
	})...)
	evals = append(evals, evalSustainedHRLoad(in.SustainedHRLoad)...)
	evals = append(evals, evalAutonomicProdrome(in)...)
	evals = append(evals, evalObjectivePersistence(in.ObjectivePatternDays)...)

	for _, flag := range in.StressFlags {
		if flag == "" {
			continue
		}
		sig := IllnessEvidenceSignal{
			Metric:    "stress_flags",
			Kind:      "stress_flag",
			Role:      "context",
			Category:  "stress_flag",
			Direction: "present",
			Strength:  "mild",
			Status:    "ok",
			Method:    "stress_flags",
			Evidence:  "Existing stress flag present: " + flag + ".",
		}
		isIllnessSignature := flag == "illness_signature"
		evals = append(evals, illnessSignalEval{
			signal:           sig,
			contributes:      isIllnessSignature,
			objective:        true,
			autoOrTemp:       isIllnessSignature,
			strong:           isIllnessSignature,
			illnessSignature: isIllnessSignature,
		})
	}
	if in.SubjectiveCheckin != nil && in.SubjectiveCheckin.Answer != "" {
		strength := "mild"
		if in.SubjectiveCheckin.Answer == "sick" {
			strength = "strong"
		}
		evals = append(evals, illnessSignalEval{signal: IllnessEvidenceSignal{
			Metric:    "subjective_checkin",
			Kind:      "subjective",
			Role:      "support",
			Category:  "subjective",
			Direction: "present",
			Strength:  strength,
			Status:    "ok",
			Method:    "subjective_checkin",
			Evidence:  "Subjective check-in is present.",
		}, contributes: in.SubjectiveCheckin.Answer == "sick", strong: in.SubjectiveCheckin.Answer == "sick"})
	}

	signals := make([]IllnessEvidenceSignal, 0, len(evals))
	objectiveCategories := map[string]struct{}{}
	strongObjective := false
	strongPrimary := false
	primaryCategories := map[string]struct{}{}
	autoOrTempCategories := map[string]struct{}{}
	hasRespiratoryPrimary := false
	hasStrongRespiratoryPrimary := false
	hasOxygenObjective := false
	hasObjectivePersistence := false
	subjectiveSick := false
	strongConfounder := false
	hasIllnessSignature := false
	hasAutonomicProdrome := false
	for _, e := range evals {
		sig := e.signal
		sig.Contributes = e.contributes
		signals = append(signals, sig)
		if e.illnessSignature {
			hasIllnessSignature = true
		}
		if e.signal.Kind == "objective_persistence" && e.contributes {
			hasObjectivePersistence = true
		}
		if e.signal.Kind == "subjective" && e.signal.Strength == "strong" {
			subjectiveSick = true
		}
		if e.strongConfounder {
			strongConfounder = true
		}
		if e.autonomicProdrome && e.contributes {
			hasAutonomicProdrome = true
		}
		if !e.contributes {
			continue
		}
		if e.objective {
			objectiveCategories[e.signal.Category] = struct{}{}
			if e.signal.Category == "oxygen" {
				hasOxygenObjective = true
			}
			if e.strong {
				strongObjective = true
			}
			if e.primary {
				primaryCategories[e.signal.Category] = struct{}{}
				if e.signal.Category == "respiratory" {
					hasRespiratoryPrimary = true
					if e.strong {
						hasStrongRespiratoryPrimary = true
					}
				}
				if e.strong {
					strongPrimary = true
				}
			}
			if e.autoOrTemp {
				autoOrTempCategories[e.signal.Category] = struct{}{}
			}
		}
	}

	confidence := IllnessConfidenceNone
	switch {
	case hasIllnessSignature:
		confidence = IllnessConfidenceHigh
	case len(objectiveCategories) >= 3 && hasStrongRespiratoryPrimary && len(autoOrTempCategories) > 0 && !strongConfounder:
		confidence = IllnessConfidenceHigh
	case hasRespiratoryPrimary && (hasOxygenObjective || len(autoOrTempCategories) > 0):
		confidence = IllnessConfidenceModerate
	case hasObjectivePersistence && len(objectiveCategories) > 0:
		confidence = IllnessConfidenceModerate
	case hasAutonomicProdrome:
		confidence = IllnessConfidenceModerate
	case (strongPrimary || strongObjective) && subjectiveSick:
		confidence = IllnessConfidenceModerate
	case len(objectiveCategories) > 0:
		confidence = IllnessConfidenceLow
	}

	pattern := illnessPattern(confidence, hasRespiratoryPrimary, hasOxygenObjective, hasAutonomicProdrome)
	return &IllnessSuspicion{
		Date:         in.Date,
		Confidence:   confidence,
		Pattern:      pattern,
		Reason:       illnessReason(confidence, pattern),
		Experimental: true,
		Signals:      signals,
		StressFlags:  append([]string(nil), in.StressFlags...),
	}
}

// ApplyIllnessSafetyCap leaves the numeric readiness/bank values intact but
// caps the prescriptive EnergyBank action when illness evidence says a hard
// recommendation would be unsafe.
func ApplyIllnessSafetyCap(resp *BriefingResponse, ls LangStrings) {
	if resp == nil || resp.EnergyBank == nil || resp.IllnessSuspicion == nil {
		return
	}
	switch resp.IllnessSuspicion.Confidence {
	case IllnessConfidenceHigh:
		if verdictRank(resp.EnergyBank.ActionVerdict) > verdictRank("rest") {
			resp.EnergyBank.ActionVerdict = "rest"
			resp.EnergyBank.VerdictReason = ls["energy_reason_illness_suspicion_high"]
		}
	case IllnessConfidenceModerate:
		if verdictRank(resp.EnergyBank.ActionVerdict) > verdictRank("active_recovery") {
			resp.EnergyBank.ActionVerdict = "active_recovery"
			if resp.IllnessSuspicion.Pattern == IllnessPatternAutonomicProdrome {
				resp.EnergyBank.VerdictReason = ls["energy_reason_autonomic_prodrome_moderate"]
			} else {
				resp.EnergyBank.VerdictReason = ls["energy_reason_illness_suspicion_moderate"]
			}
		}
	}
}

func verdictRank(v string) int {
	switch v {
	case "rest":
		return 0
	case "active_recovery":
		return 1
	case "moderate":
		return 2
	case "push_hard":
		return 3
	default:
		return 2
	}
}

type metricEvalSpec struct {
	metric     string
	role       string
	category   string
	direction  string
	mildZ      float64
	strongZ    float64
	highIsBad  bool
	mildText   string
	strongText string
}

func evalMetric(in *MetricEvidenceInput, spec metricEvalSpec) []illnessSignalEval {
	if in == nil {
		return nil
	}
	if in.Status == "" {
		in.Status = "ok"
	}
	if in.Status != "ok" {
		return []illnessSignalEval{missingSignal(in, spec)}
	}
	strength := ""
	strong := false
	if spec.highIsBad {
		switch {
		case in.ZScore >= spec.strongZ:
			strength, strong = "strong", true
		case in.ZScore >= spec.mildZ:
			strength = "mild"
		}
	} else {
		switch {
		case in.ZScore <= spec.strongZ:
			strength, strong = "strong", true
		case in.ZScore <= spec.mildZ:
			strength = "mild"
		}
	}
	if strength == "" {
		return nil
	}
	text := spec.mildText
	if strong {
		text = spec.strongText
	}
	sig := metricSignal(in, spec.metric, "metric", spec.role, spec.category, spec.direction, strength, text)
	return []illnessSignalEval{{
		signal:      sig,
		contributes: true,
		objective:   true,
		primary:     spec.role == "primary",
		autoOrTemp:  spec.category == "autonomic" || spec.category == "temperature",
		strong:      strong,
	}}
}

func evalSpO2Average(in *MetricEvidenceInput) []illnessSignalEval {
	if in == nil {
		return nil
	}
	if in.Status == "" {
		in.Status = "ok"
	}
	spec := metricEvalSpec{metric: "blood_oxygen_saturation", role: "primary", category: "oxygen", direction: "low"}
	if in.Status != "ok" {
		return []illnessSignalEval{missingSignal(in, spec)}
	}
	drop := in.Baseline - in.Value
	strength := ""
	strong := false
	switch {
	case in.ZScore <= -2.0 || drop >= 1.0:
		strength, strong = "strong", true
	case in.ZScore <= -1.5 || drop >= 0.7:
		strength = "mild"
	}
	if strength == "" {
		return nil
	}
	sig := metricSignal(in, "blood_oxygen_saturation", "metric", "primary", "oxygen", "low", strength, "SpO2 average is below personal baseline.")
	return []illnessSignalEval{{signal: sig, contributes: true, objective: true, primary: true, strong: strong}}
}

func evalSpO2Cluster(in *SpO2ClusterEvidence) []illnessSignalEval {
	if in == nil {
		return nil
	}
	if in.Status == "" {
		in.Status = "ok"
	}
	if in.Status != "ok" {
		return []illnessSignalEval{{
			signal: IllnessEvidenceSignal{
				Metric:   "blood_oxygen_saturation",
				Kind:     "spo2_cluster",
				Role:     "primary",
				Category: "oxygen",
				Strength: in.Status,
				Status:   in.Status,
				Method:   "same_day_low_count",
				Evidence: "SpO2 low-cluster evidence is " + in.Status + ".",
			},
		}}
	}
	if in.Below94Count == 0 && in.Below92Count == 0 {
		return nil
	}
	strength := "weak"
	contributes := false
	strong := false
	switch {
	case in.Below92Count >= 3:
		strength, contributes, strong = "strong", true, true
	case in.Below94Count >= 2:
		strength, contributes = "mild", true
	}
	v := in.Min
	sig := IllnessEvidenceSignal{
		Metric:    "blood_oxygen_saturation",
		Kind:      "spo2_cluster",
		Role:      "primary",
		Category:  "oxygen",
		Direction: "low",
		Strength:  strength,
		Status:    "ok",
		Value:     &v,
		Unit:      "%",
		Method:    "same_day_low_count",
		Evidence:  "SpO2 has low readings on the evaluated date.",
		Sources:   append([]SpO2SourceEvidence(nil), in.Sources...),
	}
	return []illnessSignalEval{{signal: sig, contributes: contributes, objective: contributes, primary: contributes, strong: strong}}
}

func evalSustainedHRLoad(in *MetricEvidenceInput) []illnessSignalEval {
	if in == nil {
		return nil
	}
	if in.Status == "" {
		in.Status = "ok"
	}
	spec := metricEvalSpec{metric: "sustained_hr_load", role: "support", category: "autonomic", direction: "high"}
	if in.Status != "ok" {
		return []illnessSignalEval{missingSignal(in, spec)}
	}
	strength := ""
	strong := false
	switch {
	case in.ZScore >= 2.0:
		strength, strong = "strong", true
	case in.ZScore >= 1.5:
		strength = "mild"
	}
	if strength == "" {
		return nil
	}
	status := "ok"
	strongConfounder := false
	if in.ActivityContext == "high" {
		status = "confounded"
		strongConfounder = true
	}
	sig := metricSignal(in, "sustained_hr_load", "metric", "support", "autonomic", "high", strength, "Daytime HR load is elevated; activity context: "+activityContext(in.ActivityContext)+".")
	sig.Status = status
	return []illnessSignalEval{{signal: sig, contributes: false, strong: strong, strongConfounder: strongConfounder}}
}

func evalAutonomicProdrome(in IllnessEvidenceInput) []illnessSignalEval {
	// Thresholds come from the local Profile A/B validation documented in
	// docs/ai-plans/2026-06-03-illness-autonomic-prodrome.html: require a
	// strong current HR-load day, known-normal activity context, and RHR
	// corroboration before surfacing a moderate autonomic-only warning.
	load := in.SustainedHRLoad
	rhr := in.RHR
	if load == nil || rhr == nil || load.Status != "ok" || rhr.Status != "ok" {
		return nil
	}
	if load.ZScore < 2.0 || load.ActivityContext != "normal" {
		return nil
	}
	rhrZ := rhr.ZScore
	days := in.AutonomicPatternDays
	// Two load days need a clearer RHR rise; three or more load days can
	// accept a borderline RHR rise to catch early prodrome without letting
	// HRV/SpO2/stress flags independently escalate this path.
	contributes := (days >= 2 && rhrZ >= 1.0) || (days >= 3 && rhrZ >= 0.8)
	if !contributes {
		return nil
	}
	v := float64(days)
	z := rhrZ
	strength := "mild"
	// A longer load run or stronger RHR rise changes signal strength only;
	// confidence stays capped at moderate for autonomic-only evidence.
	if days >= 3 || rhrZ >= 1.5 {
		strength = "strong"
	}
	sig := IllnessEvidenceSignal{
		Metric:          "autonomic_prodrome",
		Kind:            "autonomic_pattern",
		Role:            "context",
		Category:        "autonomic",
		Direction:       "present",
		Strength:        strength,
		Status:          "ok",
		Contributes:     true,
		Value:           &v,
		ZScore:          &z,
		Unit:            "days",
		Method:          "rolling_4_day_sustained_hr_load_rhr",
		ActivityContext: activityContext(load.ActivityContext),
		Evidence:        "Repeated normal-activity HR load with elevated resting heart rate suggests autonomic strain or recovery load.",
	}
	return []illnessSignalEval{{
		signal:            sig,
		contributes:       true,
		objective:         true,
		autoOrTemp:        true,
		strong:            strength == "strong",
		autonomicProdrome: true,
	}}
}

func evalObjectivePersistence(days int) []illnessSignalEval {
	if days < 2 {
		return nil
	}
	strength := "mild"
	if days >= 3 {
		strength = "strong"
	}
	v := float64(days)
	sig := IllnessEvidenceSignal{
		Metric:    "objective_illness_pattern",
		Kind:      "objective_persistence",
		Role:      "context",
		Category:  "persistence",
		Direction: "present",
		Strength:  strength,
		Status:    "ok",
		Value:     &v,
		Unit:      "days",
		Method:    "rolling_3_day_objective_pattern",
		Evidence:  "Objective respiratory/oxygen evidence with autonomic support persists across recent days.",
	}
	return []illnessSignalEval{{signal: sig, contributes: true, strong: days >= 3}}
}

func missingSignal(in *MetricEvidenceInput, spec metricEvalSpec) illnessSignalEval {
	status := in.Status
	if status == "" {
		status = "missing"
	}
	return illnessSignalEval{signal: IllnessEvidenceSignal{
		Metric:    fallbackMetric(in.Metric, spec.metric),
		Kind:      "metric",
		Role:      spec.role,
		Category:  spec.category,
		Direction: spec.direction,
		Strength:  status,
		Status:    status,
		Method:    in.Method,
		Evidence:  fallbackMetric(in.Metric, spec.metric) + " evidence is " + status + ".",
	}}
}

func metricSignal(in *MetricEvidenceInput, metric, kind, role, category, direction, strength, evidence string) IllnessEvidenceSignal {
	v, b, z := in.Value, in.Baseline, in.ZScore
	d := in.Value - in.Baseline
	return IllnessEvidenceSignal{
		Metric:          fallbackMetric(in.Metric, metric),
		Kind:            kind,
		Role:            role,
		Category:        category,
		Direction:       direction,
		Strength:        strength,
		Status:          "ok",
		Value:           &v,
		Baseline:        &b,
		DeltaAbs:        &d,
		ZScore:          &z,
		Unit:            in.Unit,
		Method:          in.Method,
		ActivityContext: in.ActivityContext,
		Evidence:        evidence,
	}
}

func illnessPattern(confidence string, hasRespiratory, hasOxygen, hasAutonomicProdrome bool) string {
	if confidence == IllnessConfidenceNone {
		return ""
	}
	switch {
	case hasAutonomicProdrome && !hasRespiratory && !hasOxygen:
		return IllnessPatternAutonomicProdrome
	case hasRespiratory && (hasOxygen || hasAutonomicProdrome):
		return IllnessPatternMixed
	case hasRespiratory:
		return IllnessPatternRespiratory
	case hasOxygen:
		return IllnessPatternOxygen
	default:
		return ""
	}
}

func illnessReason(confidence, pattern string) string {
	if pattern == IllnessPatternAutonomicProdrome {
		switch confidence {
		case IllnessConfidenceModerate:
			return "Repeated autonomic strain is present during normal activity. This can happen before illness or during recovery, but it is not a diagnosis."
		case IllnessConfidenceLow:
			return "One autonomic strain signal is outside its usual range. This is not a diagnosis."
		}
	}
	switch confidence {
	case IllnessConfidenceHigh:
		return "Multiple wearable signals are consistent with respiratory stress or illness. This is not a diagnosis."
	case IllnessConfidenceModerate:
		return "Some wearable signals are consistent with respiratory stress or illness. This is not a diagnosis."
	case IllnessConfidenceLow:
		return "One mild wearable signal is outside its usual range. This is not a diagnosis."
	default:
		return "No converging wearable evidence of acute respiratory stress or illness."
	}
}

func fallbackMetric(got, fallback string) string {
	if got != "" {
		return got
	}
	return fallback
}

func activityContext(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
