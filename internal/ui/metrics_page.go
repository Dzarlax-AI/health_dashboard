package ui

import (
	"fmt"
	"strings"
)

// MetricsPageData is the template data for the metrics list page.
type MetricsPageData struct {
	BasePage
	Query      string
	Categories []MetricCategory
}

// MetricCategory groups metrics for display.
type MetricCategory struct {
	Label   string
	Color   string
	Metrics []MetricListItem
}

// MetricListItem is a single metric in the list.
type MetricListItem struct {
	Name        string
	DisplayName string
	Value       string
	Unit        string
}

// categories defines metric groupings matching the SPA's CATEGORIES array.
var categories = []struct {
	LabelKey string
	Color    string
	Cat      string
	Metrics  []string
}{
	{"cat_heart", "var(--heart)", "heart", []string{"heart_rate", "resting_heart_rate", "walking_heart_rate_average", "heart_rate_variability", "blood_oxygen_saturation", "respiratory_rate", "blood_pressure_systolic", "blood_pressure_diastolic", "heart_rate_recovery", "wrist_temperature", "breathing_disturbances"}},
	{"cat_activity", "var(--activity)", "activity", []string{"step_count", "walking_running_distance", "active_energy", "basal_energy_burned", "apple_exercise_time", "apple_stand_time", "apple_stand_hour", "physical_effort", "flights_climbed", "stair_ascent_speed", "stair_descent_speed", "distance_cycling", "distance_swimming", "swimming_stroke_count", "mindful_minutes"}},
	{"cat_fitness", "#f59e0b", "mobility", []string{"vo2_max", "six_min_walk_distance", "walking_speed", "walking_step_length", "walking_double_support", "walking_asymmetry", "walking_steadiness"}},
	{"cat_sleep", "var(--sleep)", "sleep", []string{"sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake"}},
	{"cat_body", "#10b981", "body", []string{"body_mass", "body_mass_index", "body_fat_percentage", "lean_body_mass", "height"}},
	{"cat_env", "#06b6d4", "env", []string{"environmental_audio", "headphone_audio", "time_in_daylight", "environmental_sound_reduction"}},
	{"cat_nutrition", "#f97316", "nutrition", []string{"dietary_energy", "dietary_protein", "dietary_carbs", "dietary_fat", "dietary_fat_saturated", "dietary_fat_monounsaturated", "dietary_fat_polyunsaturated", "dietary_water", "dietary_fiber", "dietary_sugar", "dietary_sodium", "dietary_caffeine", "dietary_calcium", "dietary_iron", "dietary_cholesterol", "dietary_potassium", "dietary_magnesium", "dietary_phosphorus", "dietary_zinc", "dietary_copper", "dietary_manganese", "dietary_selenium", "dietary_iodine", "dietary_molybdenum", "dietary_folate", "dietary_biotin", "dietary_vitamin_a", "dietary_vitamin_c", "dietary_vitamin_d", "dietary_vitamin_e", "dietary_vitamin_k", "dietary_vitamin_b6", "dietary_vitamin_b12", "dietary_niacin", "dietary_riboflavin", "dietary_thiamin", "dietary_pantothenic_acid", "alcoholic_beverages"}},
}

func (h *Handler) buildMetricsPageData(lang, query string) MetricsPageData {
	query = strings.ToLower(strings.TrimSpace(query))

	// Fetch latest values to show on the list
	latestMap := map[string]struct {
		Value float64
		Unit  string
	}{}
	if vals, err := h.db.GetLatestMetricValues(); err == nil {
		for _, v := range vals {
			latestMap[v.Metric] = struct {
				Value float64
				Unit  string
			}{v.Value, v.Unit}
		}
	}

	// Also fetch the list of available metrics to only show ones with data
	availableMetrics := map[string]bool{}
	if metrics, err := h.db.ListMetrics(); err == nil {
		for _, m := range metrics {
			availableMetrics[m.Name] = true
		}
	}

	var cats []MetricCategory
	for _, c := range categories {
		cat := MetricCategory{
			Label: T(lang, c.LabelKey),
			Color: c.Color,
		}
		for _, m := range c.Metrics {
			if !availableMetrics[m] {
				continue
			}
			displayName := MetricName(lang, m)
			if query != "" && !strings.Contains(strings.ToLower(displayName), query) && !strings.Contains(m, query) {
				continue
			}
			item := MetricListItem{
				Name:        m,
				DisplayName: displayName,
			}
			if lv, ok := latestMap[m]; ok {
				item.Value = fmtValue(lv.Value, lv.Unit)
				item.Unit = lv.Unit
			}
			cat.Metrics = append(cat.Metrics, item)
		}
		if len(cat.Metrics) > 0 {
			cats = append(cats, cat)
		}
	}

	return MetricsPageData{
		BasePage:   BasePage{Lang: lang, Title: T(lang, "all_metrics"), ActiveNav: "metrics"},
		Query:      query,
		Categories: cats,
	}
}

func fmtValue(v float64, unit string) string {
	if v >= 10000 {
		return fmt.Sprintf("%.1fk", v/1000)
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f", v)
	}
	if v < 10 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", v)
}
