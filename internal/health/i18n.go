package health

// LangStrings maps translation keys to localised text for one language.
type LangStrings map[string]string

// langs holds all supported translations. "en" is also the fallback.
var langs = map[string]LangStrings{
	"en": en,
	"ru": ru,
	"sr": sr,
}

// GetStrings returns localised strings for the given lang code (falls back to "en").
func GetStrings(lang string) LangStrings {
	if ls, ok := langs[lang]; ok {
		return ls
	}
	return en
}

// ── English ────────────────────────────────────────────────────────────────────
var en = LangStrings{
	// Readiness
	"readiness_optimal": "Optimal",
	"readiness_fair":    "Fair",
	"readiness_low":     "Low",
	"tip_optimal":       "Great day for a challenging workout or important tasks.",
	"tip_fair":          "Some deviation from your norm. Moderate activity is a good choice.",
	"tip_low":           "Focus on recovery: hydrate, rest, and avoid intense exercise.",

	// Section titles
	"sec_recovery": "Recovery",
	"sec_sleep":    "Sleep",
	"sec_activity": "Activity",
	"sec_cardio":   "Heart & Lungs",

	// Detail labels
	"lbl_hrv":         "HRV",
	"lbl_resting_hr":  "Resting HR",
	"lbl_duration":    "Duration",
	"lbl_deep_sleep":  "Deep sleep",
	"lbl_rem":         "REM",
	"lbl_steps":       "Steps",
	"lbl_active_cal":  "Active calories",
	"lbl_exercise":    "Exercise",
	"lbl_blood_o2":    "Blood oxygen",
	"lbl_vo2":         "VO2 Max",
	"lbl_resp":        "Respiratory rate",

	// HRV detail notes
	"hrv_note_stable": "stable compared to your baseline",
	"hrv_note_good":   "above your usual range — good sign",
	"hrv_note_low":    "below your baseline — could indicate fatigue",

	// RHR detail notes
	"rhr_note_normal":  "within your normal range",
	"rhr_note_low":     "lower than usual — well rested",
	"rhr_note_high":    "elevated — may indicate stress or poor recovery",

	// Recovery section summaries
	"rec_summary_good": "You're well recovered. Your body's ready for activity.",
	"rec_summary_fair": "Recovery is moderate. Listen to your body today.",
	"rec_summary_low":  "Your body needs more rest. Take it easy if you can.",

	// Sleep duration detail notes
	"sleep_dur_stable": "consistent with your pattern",
	"sleep_dur_more":   "more than usual — nice",
	"sleep_dur_less":   "less than you usually get",

	// Sleep deep detail notes
	"sleep_deep_good": "good ratio for restorative sleep",
	"sleep_deep_low":  "below the ideal 15%+ — quality may suffer",

	// Sleep REM detail notes
	"sleep_rem_good": "healthy range for memory & learning",
	"sleep_rem_low":  "a bit low — REM helps with memory consolidation",

	// Sleep section summaries (use fmt.Sprintf with one float64)
	"sleep_summary_good": "Averaging %.1f hours — you're sleeping well.",
	"sleep_summary_fair": "Averaging %.1f hours — decent, but there's room to improve.",
	"sleep_summary_low":  "Only %.1f hours on average. Try to get to bed earlier.",

	// Activity steps detail notes
	"steps_note_normal": "on par with your usual activity",
	"steps_note_good":   "more active than usual — keep it up",
	"steps_note_low":    "noticeably below your baseline",

	// Activity calories detail notes
	"cal_note_high":   "burning more than usual",
	"cal_note_low":    "lower burn than your baseline",
	"cal_note_normal": "consistent with your routine",

	// Activity exercise detail notes
	"ex_note_good": "meeting the daily guideline",
	"ex_note_low":  "aim for 30+ min of activity",

	// Activity section summaries (use fmt.Sprintf with one string)
	"act_summary_good": "Averaging %s steps — you're staying active.",
	"act_summary_fair": "Around %s steps — a bit below your usual pace.",
	"act_summary_low":  "Only %s steps recently. Try to move more today.",

	// Cardio SpO2 detail notes
	"spo2_note_good": "healthy range",
	"spo2_note_low":  "slightly low — worth monitoring",

	// Cardio VO2 detail notes
	"vo2_note_stable":  "stable cardio fitness",
	"vo2_note_good":    "improving — your fitness is trending up",
	"vo2_note_decline": "slight decline — stay consistent with cardio",

	// Cardio resp detail notes
	"resp_note_normal":  "normal range (12-20)",
	"resp_note_outside": "outside normal range — keep an eye on it",

	// Cardio section summaries
	"cardio_summary_good": "Cardiovascular indicators look healthy.",
	"cardio_summary_fair": "Some markers are slightly off — keep monitoring.",
	"cardio_summary_low":  "A few indicators need attention. Consider checking with a doctor.",

	// Metric value suffixes
	"unit_steps_day":  "%s/day",
	"unit_min_day":    "%s min/day",
	"unit_hrs_night":  "%.1f hrs/night",
	"unit_pct_total":  "%.0f%% of total",

	// Insights (use fmt.Sprintf with numeric args where noted)
	"insight_steps_good":      "You hit your average step count on %d of the last 7 days. Nice consistency!",
	"insight_steps_low":       "Only %d of 7 days above your average steps. Try to move more consistently.",
	"insight_hrv_drop":        "Your HRV tends to drop after high-activity days. Make sure to schedule recovery.",
	"insight_hrv_resilient":   "Your HRV stays resilient after active days — your recovery is solid.",
	"insight_sleep_active":    "You sleep %.1f hrs on active days vs %.1f hrs on rest days — activity helps your sleep.",
	"insight_sleep_rest":      "You sleep better on rest days (%.1f hrs vs %.1f hrs). Evening activity might be affecting sleep.",
	"insight_overtrain":       "Your activity is high despite signs of exhaustion. Risk of overtraining is elevated.",

	// Metric card names (matched by backend key, displayed on frontend via cardName())
	"card_Steps":            "Steps",
	"card_Sleep":            "Sleep",
	"card_HRV":              "HRV",
	"card_Resting_HR":       "Resting HR",
	"card_Respiratory_Rate": "Respiratory Rate",
}

// ── Russian ────────────────────────────────────────────────────────────────────
var ru = LangStrings{
	"readiness_optimal": "Оптимально",
	"readiness_fair":    "Умеренно",
	"readiness_low":     "Низкая",
	"tip_optimal":       "Отличный день для тренировки или важных задач.",
	"tip_fair":          "Небольшое отклонение от нормы. Умеренная активность — хороший выбор.",
	"tip_low":           "Сосредоточьтесь на восстановлении: пейте воду, отдыхайте, избегайте интенсивных нагрузок.",

	"sec_recovery": "Восстановление",
	"sec_sleep":    "Сон",
	"sec_activity": "Активность",
	"sec_cardio":   "Сердце и лёгкие",

	"lbl_hrv":        "ВСР",
	"lbl_resting_hr": "Пульс покоя",
	"lbl_duration":   "Продолжительность",
	"lbl_deep_sleep": "Глубокий сон",
	"lbl_rem":        "REM",
	"lbl_steps":      "Шаги",
	"lbl_active_cal": "Акт. калории",
	"lbl_exercise":   "Упражнения",
	"lbl_blood_o2":   "Кислород крови",
	"lbl_vo2":        "VO2 Max",
	"lbl_resp":       "ЧДД",

	"hrv_note_stable": "стабильно относительно базового уровня",
	"hrv_note_good":   "выше обычного — хороший знак",
	"hrv_note_low":    "ниже базового уровня — возможна усталость",

	"rhr_note_normal": "в пределах нормы",
	"rhr_note_low":    "ниже обычного — хорошо отдохнули",
	"rhr_note_high":   "повышен — возможен стресс или плохое восстановление",

	"rec_summary_good": "Вы хорошо восстановились. Тело готово к активности.",
	"rec_summary_fair": "Восстановление умеренное. Прислушивайтесь к своему телу.",
	"rec_summary_low":  "Телу нужно больше отдыха. Не перегружайтесь.",

	"sleep_dur_stable": "соответствует вашему паттерну",
	"sleep_dur_more":   "больше обычного — отлично",
	"sleep_dur_less":   "меньше обычного",

	"sleep_deep_good": "хорошее соотношение для восстановительного сна",
	"sleep_deep_low":  "ниже идеального 15%+ — качество может пострадать",

	"sleep_rem_good": "здоровый диапазон для памяти и обучения",
	"sleep_rem_low":  "немного мало — REM помогает консолидации памяти",

	"sleep_summary_good": "В среднем %.1f часа — вы хорошо спите.",
	"sleep_summary_fair": "В среднем %.1f часа — неплохо, но есть куда расти.",
	"sleep_summary_low":  "Всего %.1f часа в среднем. Постарайтесь ложиться раньше.",

	"steps_note_normal": "в рамках обычной активности",
	"steps_note_good":   "активнее обычного — так держать",
	"steps_note_low":    "заметно ниже базового уровня",

	"cal_note_high":   "сжигаете больше обычного",
	"cal_note_low":    "ниже обычного сжигания",
	"cal_note_normal": "соответствует вашему распорядку",

	"ex_note_good": "выполняете дневную норму",
	"ex_note_low":  "стремитесь к 30+ минутам активности",

	"act_summary_good": "В среднем %s шагов — вы остаётесь активными.",
	"act_summary_fair": "Около %s шагов — немного ниже обычного.",
	"act_summary_low":  "Всего %s шагов. Постарайтесь больше двигаться сегодня.",

	"spo2_note_good": "в норме",
	"spo2_note_low":  "немного низко — стоит следить",

	"vo2_note_stable":  "стабильная кардиофитнес",
	"vo2_note_good":    "улучшается — ваша форма растёт",
	"vo2_note_decline": "небольшое снижение — продолжайте кардио",

	"resp_note_normal":  "норма (12–20)",
	"resp_note_outside": "вне нормального диапазона — следите за этим",

	"cardio_summary_good": "Кардиоваскулярные показатели в норме.",
	"cardio_summary_fair": "Некоторые показатели немного отклонены — продолжайте следить.",
	"cardio_summary_low":  "Несколько показателей требуют внимания. Рассмотрите консультацию врача.",

	"unit_steps_day":  "%s/день",
	"unit_min_day":    "%s мин/день",
	"unit_hrs_night":  "%.1f ч/ночь",
	"unit_pct_total":  "%.0f%% от общего",

	"insight_steps_good":    "Вы достигли среднего количества шагов в %d из 7 дней. Отличная стабильность!",
	"insight_steps_low":     "Только %d из 7 дней выше среднего. Старайтесь двигаться равномернее.",
	"insight_hrv_drop":      "ВСР имеет тенденцию снижаться после дней высокой активности. Не забывайте о восстановлении.",
	"insight_hrv_resilient": "Ваш ВСР остаётся стабильным после активных дней — восстановление хорошее.",
	"insight_sleep_active":  "Вы спите %.1f ч в активные дни и %.1f ч в дни отдыха — активность помогает сну.",
	"insight_sleep_rest":    "Вы лучше спите в дни отдыха (%.1f ч против %.1f ч). Вечерняя активность может влиять на сон.",
	"insight_overtrain":     "Высокая активность при признаках истощения. Риск перетренированности повышен.",
}

// ── Serbian (Latin) ────────────────────────────────────────────────────────────
var sr = LangStrings{
	"readiness_optimal": "Optimalno",
	"readiness_fair":    "Umjereno",
	"readiness_low":     "Niska",
	"tip_optimal":       "Odličan dan za naporan trening ili važne zadatke.",
	"tip_fair":          "Malo odstupanje od vaše norme. Umjerena aktivnost je dobar izbor.",
	"tip_low":           "Fokusirajte se na oporavak: hidratacija, odmor i izbjegavanje intenzivnog vježbanja.",

	"sec_recovery": "Oporavak",
	"sec_sleep":    "San",
	"sec_activity": "Aktivnost",
	"sec_cardio":   "Srce i pluća",

	"lbl_hrv":        "HRV",
	"lbl_resting_hr": "Puls u miru",
	"lbl_duration":   "Trajanje",
	"lbl_deep_sleep": "Duboki san",
	"lbl_rem":        "REM",
	"lbl_steps":      "Koraci",
	"lbl_active_cal": "Akt. kalorije",
	"lbl_exercise":   "Vježbanje",
	"lbl_blood_o2":   "Kiseonik u krvi",
	"lbl_vo2":        "VO2 Maks",
	"lbl_resp":       "Respiratorni ritam",

	"hrv_note_stable": "stabilno u odnosu na vaš baseline",
	"hrv_note_good":   "iznad uobičajenog — dobar znak",
	"hrv_note_low":    "ispod bazeline — moguć umor",

	"rhr_note_normal": "u normalnom opsegu",
	"rhr_note_low":    "niže nego obično — dobro ste se odmorili",
	"rhr_note_high":   "povišen — moguć stres ili loš oporavak",

	"rec_summary_good": "Dobro ste se oporavili. Telo je spremno za aktivnost.",
	"rec_summary_fair": "Oporavak je umjeren. Slušajte svoje telo danas.",
	"rec_summary_low":  "Telu je potrebno više odmora. Ne preopterećujte se.",

	"sleep_dur_stable": "u skladu s vašim obrascem",
	"sleep_dur_more":   "više nego obično — odlično",
	"sleep_dur_less":   "manje nego obično",

	"sleep_deep_good": "dobar omjer za restorativni san",
	"sleep_deep_low":  "ispod idealnih 15%+ — kvalitet može patiti",

	"sleep_rem_good": "zdrav opseg za pamćenje i učenje",
	"sleep_rem_low":  "malo nisko — REM pomaže konsolidaciji pamćenja",

	"sleep_summary_good": "Prosječno %.1f sati — spavate dobro.",
	"sleep_summary_fair": "Prosječno %.1f sati — pristojno, ali ima mjesta za napredak.",
	"sleep_summary_low":  "Samo %.1f sati prosječno. Pokušajte ići ranije na spavanje.",

	"steps_note_normal": "u skladu s uobičajenom aktivnošću",
	"steps_note_good":   "aktivniji nego obično — nastavite",
	"steps_note_low":    "primjetno ispod vašeg prosjeka",

	"cal_note_high":   "sagorevate više nego obično",
	"cal_note_low":    "niže sagorevanje od vašeg prosjeka",
	"cal_note_normal": "u skladu s vašom rutinom",

	"ex_note_good": "ispunjavate dnevnu preporuku",
	"ex_note_low":  "ciljajte na 30+ minuta aktivnosti",

	"act_summary_good": "Prosječno %s koraka — ostajete aktivni.",
	"act_summary_fair": "Oko %s koraka — malo ispod uobičajenog tempa.",
	"act_summary_low":  "Samo %s koraka. Pokušajte se više kretati danas.",

	"spo2_note_good": "zdrav opseg",
	"spo2_note_low":  "malo nisko — vrijedi pratiti",

	"vo2_note_stable":  "stabilna kardio kondicija",
	"vo2_note_good":    "poboljšava se — vaša kondicija raste",
	"vo2_note_decline": "blagi pad — nastavite s kardio treningom",

	"resp_note_normal":  "normalan opseg (12–20)",
	"resp_note_outside": "van normalnog opsega — pratite to",

	"cardio_summary_good": "Kardiovaskularni pokazatelji izgledaju zdravo.",
	"cardio_summary_fair": "Neki pokazatelji su malo van normale — nastavite pratiti.",
	"cardio_summary_low":  "Nekoliko pokazatelja zahtijeva pažnju. Razmislite o pregledu kod ljekara.",

	"unit_steps_day":  "%s/dan",
	"unit_min_day":    "%s min/dan",
	"unit_hrs_night":  "%.1f h/noć",
	"unit_pct_total":  "%.0f%% od ukupnog",

	"insight_steps_good":    "Dosegli ste prosječan broj koraka u %d od poslednjih 7 dana. Odlična konzistentnost!",
	"insight_steps_low":     "Samo %d od 7 dana iznad prosječnih koraka. Pokušajte se kretati konzistentnije.",
	"insight_hrv_drop":      "Vaš HRV ima tendenciju pada nakon dana visoke aktivnosti. Obavezno planirajte oporavak.",
	"insight_hrv_resilient": "Vaš HRV ostaje otporan nakon aktivnih dana — vaš oporavak je solidan.",
	"insight_sleep_active":  "Spavate %.1f sati na aktivne dane vs %.1f sati na dane odmora — aktivnost pomaže vašem snu.",
	"insight_sleep_rest":    "Bolje spavate na dane odmora (%.1f h vs %.1f h). Večerna aktivnost možda utiče na san.",
	"insight_overtrain":     "Vaša aktivnost je visoka unatoč znakovima iscrpljenosti. Rizik od pretreniranosti je povišen.",
}
