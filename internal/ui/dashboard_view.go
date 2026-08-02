package ui

import (
	"encoding/json"
	"fmt"
	"html/template"

	"health-receiver/internal/health"
)

type scoreGaugeView struct {
	ID         string
	Label      string
	Value      int
	HasValue   bool
	Status     string
	Subline    string
	Tone       string
	AriaLabel  string
	EmptyLabel string
}

type dashboardSleepView struct {
	Nights   int
	AvgTotal string
	AvgDeep  string
	AvgREM   string
}

type dashboardPageData struct {
	BasePage
	HasHealthData       bool
	DataDate            string
	UpdatedLabel        string
	Guidance            *health.DashboardTodayGuidance
	ReadinessGauge      scoreGaugeView
	EnergyGauge         scoreGaugeView
	SleepGauge          scoreGaugeView
	ReadinessScore      int
	ReadinessBand       string
	ReadinessLabel      string
	ReadinessTip        string
	ReadinessConfidence string
	ReadinessCapReason  string
	ReadinessRawScore   int
	ReadinessServing    readinessServingView
	RecoveryPct         int
	RecoverySource      string
	Headline            *health.HeadlineSignal
	EnergyBank          *health.EnergyBank
	IllnessSuspicion    *health.IllnessSuspicion
	SubjectiveCheckin   *health.SubjectiveCheckinSummary
	Cards               []health.MetricCard
	Alerts              []health.Alert
	Sections            []health.BriefingSection
	Sleep               *dashboardSleepView
	Insights            []health.Insight
	Correlation         []health.CorrelationPoint
	CorrelationJSON     template.JS
	AIInsight           string
}

func buildDashboardPageData(base BasePage, br *health.BriefingResponse, aiInsight string) dashboardPageData {
	data := dashboardPageData{
		BasePage:        base,
		CorrelationJSON: "null",
		AIInsight:       aiInsight,
	}
	if br == nil {
		return data
	}

	data.HasHealthData = br.Date != ""
	data.DataDate = br.Date
	data.Guidance = br.TodayGuidance
	data.ReadinessScore = br.ReadinessToday
	data.ReadinessBand = br.ReadinessTodayBand
	data.ReadinessLabel = br.ReadinessTodayLabel
	data.ReadinessTip = br.ReadinessTip
	data.ReadinessConfidence = br.ReadinessConfidence
	data.ReadinessCapReason = br.ReadinessCapReason
	data.ReadinessRawScore = br.ReadinessRawScore
	data.ReadinessServing = buildReadinessServingView(base.Lang, br.ReadinessServing)
	data.RecoveryPct = br.RecoveryPct
	data.RecoverySource = br.RecoverySource
	data.Headline = br.Headline
	data.EnergyBank = br.EnergyBank
	data.IllnessSuspicion = br.IllnessSuspicion
	data.SubjectiveCheckin = br.SubjectiveCheckin
	data.Cards = br.MetricCards
	data.Alerts = br.Alerts
	data.Sections = br.Sections
	data.Insights = br.Insights
	data.Correlation = br.Correlation

	if br.TodayGuidance != nil && br.TodayGuidance.UpdatedAt != nil {
		data.UpdatedLabel = fmt.Sprintf(T(base.Lang, "today_updated_at"), br.TodayGuidance.UpdatedAt.Format("15:04"))
	}

	data.ReadinessGauge = scoreGaugeView{
		ID:         "readiness",
		Label:      T(base.Lang, "readiness"),
		Value:      clampGaugeValue(br.ReadinessToday),
		HasValue:   data.HasHealthData,
		Status:     br.ReadinessTodayLabel,
		Subline:    data.ReadinessServing.Note,
		Tone:       "readiness",
		EmptyLabel: T(base.Lang, "score_waiting"),
	}
	data.ReadinessGauge.AriaLabel = gaugeAriaLabel(base.Lang, data.ReadinessGauge)

	if br.EnergyBank != nil {
		data.EnergyGauge = scoreGaugeView{
			ID:         "energy",
			Label:      T(base.Lang, "energy_label"),
			Value:      clampGaugeValue(br.EnergyBank.Current),
			HasValue:   true,
			Status:     T(base.Lang, "energy_state_"+br.EnergyBank.Level()+"_title"),
			Subline:    fmt.Sprintf(T(base.Lang, "energy_capacity_short"), br.EnergyBank.Capacity),
			Tone:       "energy",
			EmptyLabel: T(base.Lang, "score_waiting"),
		}
	} else {
		data.EnergyGauge = scoreGaugeView{ID: "energy", Label: T(base.Lang, "energy_label"), Tone: "energy", EmptyLabel: T(base.Lang, "score_waiting")}
	}
	data.EnergyGauge.AriaLabel = gaugeAriaLabel(base.Lang, data.EnergyGauge)

	data.SleepGauge = buildSleepGauge(base.Lang, br.SleepQuality)

	if br.Sleep != nil {
		data.Sleep = &dashboardSleepView{
			Nights:   br.Sleep.Nights,
			AvgTotal: fmtMinutes(br.Sleep.TotalAvg * 60),
			AvgDeep:  fmtMinutes(br.Sleep.DeepAvg * 60),
			AvgREM:   fmtMinutes(br.Sleep.REMAvg * 60),
		}
	}
	if len(br.Correlation) > 0 {
		if encoded, err := json.Marshal(br.Correlation); err == nil {
			data.CorrelationJSON = template.JS(encoded)
		}
	}

	return data
}

func clampGaugeValue(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func buildSleepGauge(lang string, sleep *health.SleepQualityBreakdown) scoreGaugeView {
	gauge := scoreGaugeView{
		ID:         "sleep",
		Label:      T(lang, "sleep_quality"),
		Tone:       "sleep",
		EmptyLabel: T(lang, "sleep_quality_missing"),
	}
	if sleep == nil {
		gauge.AriaLabel = gaugeAriaLabel(lang, gauge)
		return gauge
	}

	switch sleep.Confidence {
	case health.SleepQualityConfidenceFinal, health.SleepQualityConfidenceLow:
		if sleep.ScorePct != nil {
			gauge.Value = clampGaugeValue(*sleep.ScorePct)
			gauge.HasValue = true
			gauge.Status = sleepQualityBandLabel(lang, *sleep.ScorePct)
		}
		if sleep.Confidence == health.SleepQualityConfidenceLow {
			gauge.Subline = T(lang, "sleep_quality_low_confidence")
		}
	case health.SleepQualityConfidencePartial:
		gauge.EmptyLabel = T(lang, "sleep_quality_partial")
		if sleep.DurationPct > 0 {
			gauge.Subline = fmt.Sprintf(T(lang, "sleep_duration_target"), sleep.DurationPct)
		}
	}
	gauge.AriaLabel = gaugeAriaLabel(lang, gauge)
	return gauge
}

func sleepQualityBandLabel(lang string, score int) string {
	key := "sleep_quality_poor"
	switch {
	case score >= 80:
		key = "sleep_quality_restorative"
	case score >= 60:
		key = "sleep_quality_good"
	case score >= 40:
		key = "sleep_quality_mixed"
	}
	return T(lang, key)
}

func gaugeAriaLabel(lang string, gauge scoreGaugeView) string {
	value := gauge.EmptyLabel
	if gauge.HasValue {
		value = fmt.Sprintf("%d%%", gauge.Value)
	}
	return fmt.Sprintf(T(lang, "score_gauge_aria"), gauge.Label, value)
}
