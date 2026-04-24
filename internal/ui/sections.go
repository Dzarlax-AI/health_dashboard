package ui

import (
	"html/template"

	"health-receiver/internal/storage"
)

// SectionChart describes a chart to render on a section page.
type SectionChart struct {
	ID      string
	Metric  string
	Agg     string
	Label   string
	Unit    string
	Color   string
	Type    string // "line" or "bar"
	Stacked bool
	Virtual bool
}

// SectionExplain is a "How it works" card.
type SectionExplain struct {
	Title string
	Body  string
}

// SectionDetail is a single metric detail row.
type SectionDetail struct {
	Label string
	Value string
	Trend string // "positive", "negative", "stable"
	Note  string
}

// SleepStat is a sleep stats grid item.
type SleepStat struct {
	Label  string
	Value  string
	Accent bool
}

// SectionPageData is the template data for section pages.
type SectionPageData struct {
	BasePage
	SectionKey   string
	SectionTitle string
	IconColor    string
	IconSVG      template.HTML
	Summary      string
	Details      []SectionDetail
	SleepStats   []SleepStat
	Charts       []SectionChart
	Explains     []SectionExplain
}

// sectionMeta defines the charts and explainer keys for each section.
var sectionMeta = map[string]struct {
	Charts      []SectionChart
	ExplainKeys []string
}{
	"recovery": {
		Charts: []SectionChart{
			{ID: "sc-hrv", Metric: "heart_rate_variability", Agg: "AVG", Label: "HRV", Unit: "ms", Color: "#e11d48", Type: "line"},
			{ID: "sc-rhr", Metric: "resting_heart_rate", Agg: "AVG", Label: "Resting HR", Unit: "bpm", Color: "#f97316", Type: "line"},
			{ID: "sc-ready", Virtual: true, Label: "Readiness", Unit: "%", Color: "#0ea5e9", Type: "line"},
		},
		ExplainKeys: []string{"explain_hrv", "explain_rhr", "explain_readiness_score"},
	},
	"sleep": {
		Charts: []SectionChart{
			{ID: "sc-sleep", Stacked: true, Label: "Sleep Phases", Unit: "h"},
		},
		ExplainKeys: []string{"explain_sleep_deep", "explain_sleep_rem", "explain_sleep_reg"},
	},
	"activity": {
		Charts: []SectionChart{
			{ID: "sc-steps", Metric: "step_count", Agg: "SUM", Label: "Steps", Unit: "", Color: "#059669", Type: "bar"},
			{ID: "sc-cal", Metric: "active_energy", Agg: "SUM", Label: "Active Energy", Unit: "kcal", Color: "#d97706", Type: "bar"},
			{ID: "sc-ex", Metric: "apple_exercise_time", Agg: "SUM", Label: "Exercise", Unit: "min", Color: "#2563eb", Type: "bar"},
		},
		ExplainKeys: []string{"explain_steps", "explain_exercise"},
	},
	"cardio": {
		Charts: []SectionChart{
			{ID: "sc-spo2", Metric: "blood_oxygen_saturation", Agg: "AVG", Label: "SpO₂", Unit: "%", Color: "#06b6d4", Type: "line"},
			{ID: "sc-vo2", Metric: "vo2_max", Agg: "AVG", Label: "VO₂ Max", Unit: "ml/kg/min", Color: "#8b5cf6", Type: "line"},
			{ID: "sc-resp", Metric: "respiratory_rate", Agg: "AVG", Label: "Respiratory Rate", Unit: "br/min", Color: "#0ea5e9", Type: "line"},
		},
		ExplainKeys: []string{"explain_spo2", "explain_vo2", "explain_resp"},
	},
}

// sectionIcons maps section keys to SVG icons and colors.
var sectionIcons = map[string]struct {
	Color string
	SVG   template.HTML
}{
	"recovery": {Color: "var(--heart)", SVG: `<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="1" y="6" width="18" height="12" rx="2"/><line x1="23" y1="13" x2="23" y2="11"/></svg>`},
	"sleep":    {Color: "var(--sleep)", SVG: `<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>`},
	"activity": {Color: "var(--activity)", SVG: `<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>`},
	"cardio":   {Color: "var(--heart)", SVG: `<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"/></svg>`},
}

// sectionExplains holds all "How it works" texts keyed by language and explain key.
var sectionExplains = map[string]map[string]SectionExplain{
	"en": {
		"explain_hrv":             {Title: "Heart Rate Variability (HRV)", Body: "RMSSD — the variation between heartbeats — is the primary recovery biomarker. Higher HRV signals a well-recovered nervous system. Your score uses a dynamic ±1 SD threshold relative to your personal 30-day baseline."},
		"explain_rhr":             {Title: "Resting Heart Rate", Body: "RHR reflects cardiovascular efficiency. A lower-than-usual RHR suggests solid recovery; an elevated RHR often signals stress, illness, or incomplete recovery. Five-day trends are more meaningful than a single reading."},
		"explain_readiness_score": {Title: "Readiness Score", Body: "Computed as HRV × 40% + Resting HR × 30% + Sleep × 30%. Each component compares your 5-day recent average to your personal 30-day baseline. Score 70 = a normal day. Above 80 is genuinely good."},
		"explain_sleep_deep":      {Title: "Deep (Slow-Wave) Sleep", Body: "The most physically restorative phase — growth hormone is released, muscles repair, and immune function strengthens. Aim for ≥15% of total sleep. Deep sleep occurs mainly in the first half of the night."},
		"explain_sleep_rem":       {Title: "REM Sleep", Body: "REM supports memory consolidation and emotional regulation. Healthy adults spend ~20–25% of sleep in REM. Alcohol and sleep deprivation suppress REM disproportionately."},
		"explain_sleep_reg":       {Title: "Sleep Regularity", Body: "Consistent sleep and wake times predict health outcomes independently of duration. The ±Xh value shows your standard deviation in nightly sleep length."},
		"explain_steps":           {Title: "Steps Goal", Body: "Your step target is your personal 30-day average — not a fixed 10,000. Staying within 10% of your baseline indicates consistent activity."},
		"explain_exercise":        {Title: "Exercise Time", Body: "WHO recommends 150–300 min/week of moderate aerobic activity (~30 min/day). Apple Exercise ring counts active minutes above brisk-walk intensity."},
		"explain_spo2":            {Title: "Blood Oxygen (SpO₂)", Body: "Normal resting SpO₂ is 95–100%. Below 92% indicates reduced respiratory reserve. Consumer wearables may overestimate by 2–3%."},
		"explain_vo2":             {Title: "VO₂ Max", Body: "The gold standard for cardiorespiratory fitness. Each 1-MET improvement reduces all-cause mortality risk by ~13%. Track your personal trend rather than the absolute number."},
		"explain_resp":            {Title: "Respiratory Rate", Body: "Normal adult resting rate is 12–20 br/min. Persistent elevation (>20) may signal respiratory illness, autonomic stress, or overtraining."},
	},
	"ru": {
		"explain_hrv":             {Title: "Вариабельность сердечного ритма (ВСР)", Body: "RMSSD — вариация интервалов между ударами сердца — основной биомаркер восстановления. Высокая ВСР сигнализирует о хорошо восстановленной нервной системе. Оценка использует динамический порог ±1 СО относительно вашего 30-дневного базового уровня."},
		"explain_rhr":             {Title: "Пульс в покое", Body: "ЧСС покоя отражает эффективность сердечно-сосудистой системы. Пульс ниже обычного указывает на хорошее восстановление; повышенный пульс часто сигнализирует о стрессе или болезни."},
		"explain_readiness_score": {Title: "Оценка готовности", Body: "Рассчитывается как ВСР × 40% + ЧСС покоя × 30% + Сон × 30%. Каждый компонент сравнивает среднее за последние 5 дней с вашим 30-дневным базовым уровнем. Оценка 70 = обычный день. Выше 80 — действительно хорошо."},
		"explain_sleep_deep":      {Title: "Глубокий сон", Body: "Самая восстановительная фаза — выделяется гормон роста, мышцы восстанавливаются, укрепляется иммунитет. Цель: ≥15% общего сна. Глубокий сон преобладает в первой половине ночи."},
		"explain_sleep_rem":       {Title: "REM-сон", Body: "REM поддерживает консолидацию памяти и эмоциональную регуляцию. Здоровые взрослые проводят ~20–25% сна в фазе REM. Алкоголь и недосып подавляют REM непропорционально."},
		"explain_sleep_reg":       {Title: "Регулярность сна", Body: "Постоянное время сна и пробуждения предсказывает здоровье независимо от продолжительности. Значение ±Xч показывает стандартное отклонение продолжительности сна."},
		"explain_steps":           {Title: "Цель по шагам", Body: "Ваша цель по шагам — ваше личное среднее за 30 дней, а не фиксированные 10 000. Отклонение в пределах 10% от базы означает стабильную активность."},
		"explain_exercise":        {Title: "Время упражнений", Body: "ВОЗ рекомендует 150–300 мин/неделю умеренной аэробной активности (~30 мин/день). Кольцо упражнений Apple считает активные минуты выше интенсивности быстрой ходьбы."},
		"explain_spo2":            {Title: "Кислород крови (SpO₂)", Body: "Нормальный SpO₂ в покое — 95–100%. Ниже 92% указывает на сниженный респираторный резерв. Носимые устройства могут завышать на 2–3%."},
		"explain_vo2":             {Title: "VO₂ Max", Body: "Золотой стандарт кардиореспираторной подготовки. Каждое улучшение на 1 МЕТ снижает риск общей смертности на ~13%. Отслеживайте личный тренд, а не абсолютное число."},
		"explain_resp":            {Title: "Частота дыхания", Body: "Нормальная частота дыхания в покое — 12–20 вд/мин. Постоянное повышение (>20) может сигнализировать о респираторном заболевании или перетренированности."},
	},
	"sr": {
		"explain_hrv":             {Title: "Varijabilnost srčanog ritma (HRV)", Body: "RMSSD — varijacija intervala između otkucaja srca — osnovni biomarker oporavka. Viši HRV signalizira dobro oporavljeni nervni sistem."},
		"explain_rhr":             {Title: "Puls u miru", Body: "Puls u miru odražava efikasnost kardiovaskularnog sistema. Niži puls ukazuje na dobar oporavak; povišeni puls često signalizira stres ili bolest."},
		"explain_readiness_score": {Title: "Ocena spremnosti", Body: "Računa se kao HRV × 40% + puls u miru × 30% + san × 30%. Svaka komponenta poredi prosek za poslednjih 5 dana sa vašim 30-dnevnim baznim nivoom."},
		"explain_sleep_deep":      {Title: "Duboki san", Body: "Najrestorativnija faza — oslobađa se hormon rasta, mišići se oporavljaju. Cilj: ≥15% ukupnog sna."},
		"explain_sleep_rem":       {Title: "REM san", Body: "REM podržava konsolidaciju memorije i emocionalnu regulaciju. Zdravi odrasli provode ~20–25% sna u REM fazi."},
		"explain_sleep_reg":       {Title: "Regularnost sna", Body: "Konzistentno vreme spavanja i buđenja predviđa zdravstvene ishode nezavisno od trajanja."},
		"explain_steps":           {Title: "Cilj koraka", Body: "Vaš cilj koraka je vaš lični prosek za 30 dana, a ne fiksiranih 10.000."},
		"explain_exercise":        {Title: "Vreme vežbanja", Body: "WHO preporučuje 150–300 min/nedelje umerene aerobne aktivnosti (~30 min/dan)."},
		"explain_spo2":            {Title: "Kiseonik u krvi (SpO₂)", Body: "Normalan SpO₂ u miru je 95–100%. Ispod 92% ukazuje na smanjenu respiratornu rezervu."},
		"explain_vo2":             {Title: "VO₂ Maks", Body: "Zlatni standard kardiorespiratornog fitnesa. Svako poboljšanje od 1 MET smanjuje rizik od sveukupne smrtnosti za ~13%."},
		"explain_resp":            {Title: "Respiratorni ritam", Body: "Normalna frekvencija disanja u miru je 12–20 ud/min. Trajno povišenje (>20) može signalizirati respiratornu bolest."},
	},
}

// buildSectionPage creates a SectionPageData for the given section key.
func (h *Handler) buildSectionPage(key, lang string, db *storage.DB) SectionPageData {
	meta := sectionMeta[key]
	icon := sectionIcons[key]

	// i18n chart labels
	charts := make([]SectionChart, len(meta.Charts))
	copy(charts, meta.Charts)
	for i := range charts {
		if lk := "metric_" + charts[i].Metric; charts[i].Metric != "" {
			if v := T(lang, lk); v != lk {
				charts[i].Label = v
			}
		}
		if charts[i].Virtual {
			charts[i].Label = T(lang, "trend_readiness")
		}
		if charts[i].Stacked {
			charts[i].Label = T(lang, "sleep_section")
		}
	}

	// i18n section title
	titleKeys := map[string]string{
		"recovery": "recovery",
		"sleep":    "sleep_section",
		"activity": "cat_activity",
		"cardio":   "cat_heart",
	}
	title := T(lang, titleKeys[key])

	// Explainer cards
	var explains []SectionExplain
	langExplains := sectionExplains[lang]
	if langExplains == nil {
		langExplains = sectionExplains["en"]
	}
	for _, ek := range meta.ExplainKeys {
		if ex, ok := langExplains[ek]; ok {
			explains = append(explains, ex)
		}
	}

	// Get section details from briefing
	var summary string
	var details []SectionDetail
	if br, err := db.GetHealthBriefing(lang); err == nil && br != nil {
		for _, s := range br.Sections {
			if s.Key == key {
				summary = s.Summary
				for _, d := range s.Details {
					details = append(details, SectionDetail{
						Label: d.Label,
						Value: d.Value,
						Trend: d.Trend,
						Note:  d.Note,
					})
				}
				break
			}
		}
	}

	return SectionPageData{
		BasePage:     BasePage{Lang: lang, Title: title, ActiveNav: ""},
		SectionKey:   key,
		SectionTitle: title,
		IconColor:    icon.Color,
		IconSVG:      icon.SVG,
		Summary:      summary,
		Details:      details,
		Charts:       charts,
		Explains:     explains,
	}
}
