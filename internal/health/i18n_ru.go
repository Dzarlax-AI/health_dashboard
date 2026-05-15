package health

var ru = LangStrings{
	"readiness_optimal": "Оптимально",
	"readiness_fair":    "Умеренно",
	"readiness_low":     "Низкая",
	"tip_optimal":       "Отличный день для тренировки или важных задач.",
	"tip_fair":          "Небольшое отклонение от нормы. Умеренная активность — хороший выбор.",
	"tip_low":           "Сосредоточьтесь на восстановлении: пейте воду, отдыхайте, избегайте интенсивных нагрузок.",

	// Per-section status labels (BriefingSection.Status) — surfaced via
	// EnrichLabels so iOS / web consumers don't maintain a parallel i18n
	// table for the good/fair/low enum.
	"section_status_good": "Хорошо",
	"section_status_fair": "Средне",
	"section_status_low":  "Низко",

	"sec_recovery": "Восстановление",
	"sec_sleep":    "Сон",
	"sec_activity": "Активность",
	"sec_cardio":   "Сердце и лёгкие",

	"lbl_hrv":        "ВСР",
	"lbl_vs_avg":     "отн. сред.",
	"lbl_resting_hr": "Пульс покоя",
	"lbl_duration":   "Продолжительность",
	"lbl_deep_sleep": "Глубокий сон",
	"lbl_rem":        "REM",
	"lbl_nap_badge":  "+%d мин днём",
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

	"rec_summary_good":        "Вы хорошо восстановились. Тело готово к активности.",
	"rec_summary_fair":        "Восстановление умеренное. Прислушивайтесь к своему телу.",
	"rec_summary_low":         "Телу нужно больше отдыха. Не перегружайтесь.",
	"rec_summary_fair_stress": "HRV/RHR в норме, но другие маркеры указывают на накопленный стресс — см. заголовок выше.",

	"sleep_dur_stable": "соответствует вашему паттерну",
	"sleep_dur_more":   "больше обычного — отлично",
	"sleep_dur_less":   "меньше обычного",

	"sleep_deep_good": "хорошее соотношение для восстановительного сна",
	"sleep_deep_low":  "ниже идеального 15%+ — качество может пострадать",

	"sleep_rem_good": "здоровый диапазон для памяти и обучения",
	"sleep_rem_low":  "немного мало — REM помогает консолидации памяти",

	// Sleep regularity detail
	"lbl_sleep_regularity": "Регулярность",
	"sleep_reg_regular":    "очень стабильный режим — сильный сигнал долголетия",
	"sleep_reg_moderate":   "небольшая вариативность — придерживайтесь фиксированного времени сна",
	"sleep_reg_irregular":  "высокая вариативность — нерегулярный сон повышает риски для здоровья",

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

	"unit_steps_day": "%s/день",
	"unit_min_day":   "%s мин/день",
	"unit_hrs_night": "%.1f ч/ночь",
	"unit_pct_total": "%.0f%% от общего",

	"insight_steps_good":    "Вы достигли среднего количества шагов в %d из 7 дней. Отличная стабильность!",
	"insight_steps_low":     "Только %d из 7 дней выше среднего. Старайтесь двигаться равномернее.",
	"insight_hrv_drop":      "ВСР имеет тенденцию снижаться после дней высокой активности. Не забывайте о восстановлении.",
	"insight_hrv_resilient": "Ваш ВСР остаётся стабильным после активных дней — восстановление хорошее.",
	"insight_sleep_active":  "Вы спите %.1f ч в активные дни и %.1f ч в дни отдыха — активность помогает сну.",
	"insight_sleep_rest":    "Вы лучше спите в дни отдыха (%.1f ч против %.1f ч). Вечерняя активность может влиять на сон.",
	"insight_overtrain":     "Высокая активность при признаках истощения. Риск перетренированности повышен.",

	// Alerts
	"alert_rr_anomaly":         "Частота дыхания значительно отклоняется от вашей нормы. Это может быть ранним признаком болезни или стресса.",
	"alert_wrist_temp_anomaly": "Температура запястья значительно отклоняется от вашей нормы. Возможны лихорадка, воспаление или гормональные изменения.",
	"alert_hrv_cv_high":        "Вариабельность HRV за 7 дней повышена (CV %.0f%%), что указывает на нестабильное восстановление. Проверьте качество сна и уровень стресса.",

	// Headline (cross-metric signal of the day)
	"headline_stress_title":         "Накопленный стресс восстановления",
	"headline_stress_detail":        "Несколько маркеров одновременно указывают на нагрузку: %s. Рекомендуется снизить интенсивность сегодня.",
	"headline_part_rhr":             "RHR %.0f bpm (+%.0f от вашей нормы)",
	"headline_part_hrv":             "HRV %.0f ms (z=%.1f)",
	"headline_part_sleep":           "сон %.1fч",
	"headline_part_awake":           "пробуждения %.1fч",
	"headline_sleep_debt_title":     "Дефицит сна",
	"headline_sleep_debt_detail":    "Вчерашние %.1fч ниже целевых 7ч. Одна короткая ночь — норма; следите, чтобы не превратилось в паттерн.",
	"headline_stable_title":         "Сегодня всё в норме",
	"headline_stable_detail":        "Все ключевые показатели близко к вашему персональному baseline.",
	"headline_dev_heart_rate_variability_above_baseline": "HRV выше обычного",
	"headline_dev_heart_rate_variability_below_baseline": "HRV ниже обычного",
	"headline_dev_resting_heart_rate_above_baseline":     "Пульс покоя повышен",
	"headline_dev_resting_heart_rate_below_baseline":     "Пульс покоя ниже обычного",
	"headline_dev_sleep_total_above_baseline":            "Сегодня спали больше обычного",
	"headline_dev_sleep_total_below_baseline":            "Сегодня спали меньше обычного",
	"headline_dev_generic":                               "Заметное отклонение от baseline",
	"headline_dev_detail":                                "%.1f%s — %+.1f%% от вашего среднего %.1f%s.",

	// Energy Bank (action prescription)
	"energy_label":          "Энергетический банк",
	"energy_hourly_label":   "Последние 72 часа",
	"energy_capacity_label": "Капасити на сегодня",
	"energy_current_label":  "Сейчас доступно",
	"energy_drain_label":    "Потрачено",
	"trend_vs_7d":           "к 7д",
	"trend_vs_30d":          "к 30д",

	"energy_verdict_push_hard":       "Можно жёстко",
	"energy_verdict_moderate":        "Умеренный день",
	"energy_verdict_active_recovery": "Только активное восстановление",
	"energy_verdict_rest":            "День отдыха",

	"energy_reason_full_capacity": "Резерв полный, HRV выше или на уровне нормы — зелёный свет для жёсткой тренировки.",
	"energy_reason_optimal":       "Резерв приличный, маркеры стресса чистые — нормальный тренировочный день ок.",
	"energy_reason_low_capacity":  "Резерв низкий после сегодняшней нагрузки — держите интенсивность лёгкой.",
	"energy_reason_high_stress":   "HRV %.1f SD от baseline, индекс стресса %d — автономная нагрузка повышена.",
	"energy_reason_acwr_spike":    "Сегодняшняя нагрузка уже %.0f%% от 28-дневной нормы — жёсткая сессия сильно увеличит spike.",

	// v2.2 stress-flag verdict overrides — STRESS_MEASUREMENT.md §4.3.
	"energy_reason_illness_signature": "Температура, частота дыхания и HRV — все три указывают, что тело борется с инфекцией. Сегодня покой лучше тренировки.",
	"energy_reason_recovery_debt":     "Вчерашняя нагрузка догнала ночью (HRV ↓, RHR ↑) — держите день лёгким, чтобы вернуть долг.",
	"energy_reason_rebound_addon":     "Примечание: пульс был повышен, но HRV выше нормы — это паттерн фазы восстановления, не острый стресс.",

	// v2.2 hero-row stress-flag chips.
	"stress_flags_aria":                       "Флаги стресс-сигналов",
	"stress_flag_illness_signature_label":     "Признаки болезни",
	"stress_flag_illness_signature_desc":      "Температура, ЧД и HRV — все три в illness-направлении. Покой соответствует физиологии.",
	"stress_flag_recovery_debt_label":         "Долг восстановления",
	"stress_flag_recovery_debt_desc":          "Ночью HRV ↓, RHR ↑ — вчерашняя нагрузка догнала. Сегодня держите легко.",
	"stress_flag_parasympathetic_rebound_label": "Вагус-rebound",
	"stress_flag_parasympathetic_rebound_desc":  "Пульс повышен, но HRV выше нормы — паттерн восстановления, не острый стресс.",
	"stress_flag_acute_stress_label":          "Острый всплеск",
	"stress_flag_acute_stress_desc":           "Один час с ЧСС > 2 SD выше дневной нормы. Транзиторно, действия не требуется.",
	"stress_flag_sustained_load_label":        "Длительная нагрузка",
	"stress_flag_sustained_load_desc":         "4+ часа подряд с ЧСС > 1 SD выше дневной нормы. Реальная автономная нагрузка.",
	"stress_flag_stale_stress_label":          "Стресс-данные неполные",
	"stress_flag_stale_stress_desc":           "Менее 8ч HR-данных в бодрствующем окне — sustained-load drain отключён на этот день.",
	"stress_flag_calibration_warmup_label":    "Калибровка",
	"stress_flag_calibration_warmup_desc":     "Персональный baseline ещё в warmup (3-6 образцов). Пороги флагов могут быть консервативными.",

	"energy_note_capacity":              "утренняя капасити = сон %.1fч + маркеры восстановления",
	"energy_component_morning_capacity": "Утренняя капасити",
	"energy_component_activity_load":    "Нагрузка (сегодня vs 28-дн норма)",
	"energy_component_autonomic_stress": "Автономный стресс (RHR / HRV)",

	"details": "Детали",

	// Telegram report sections
	"tg_morning_header":      "Утренний отчёт",
	"tg_evening_header":      "Что накопилось за день",
	"tg_readiness":           "Готовность",
	"tg_readiness_today":     "сегодня",
	"tg_readiness_trend":     "тренд 7 дней",
	"tg_today":               "Сегодня",
	"tg_yesterday":           "Вчера",
	"tg_recommendation":      "План на сегодня",
	"tg_alerts":              "Аномалии",
	"tg_insights":            "Инсайты",
	"tg_sources":             "Источники",
	"tg_energy":              "Энергия",
	"tg_no_data":             "Свежих данных пока нет.",
	"tg_warn_stale":          "<i>Данные устарели на %d дн. — возможно, синхронизация ещё не прошла.</i>",
	"tg_warn_no_sleep":       "<i>Данных о сне прошлой ночи пока нет — возможно, телефон ещё не синхронизировался.</i>",
	"tg_warn_no_activity":    "<i>Данных об активности за сегодня пока нет.</i>",
	"tg_vs_yesterday_up":     "+%.0f%% ко вчера",
	"tg_vs_yesterday_down":   "%.0f%% ко вчера",

	// Smart-retry stale-data banners
	"tg_stale_no_data":        "⏰ <i>Дедлайн наступил, но данных о сне с часов так и не пришло — открой Health или проверь синхронизацию Apple Watch.</i>",
	"tg_stale_recent_segment": "⏰ <i>Дедлайн наступил, но часы всё ещё пишут сон — числа ниже могут быть неполными.</i>",
	"tg_stale_still_writing":  "⏰ <i>Дедлайн наступил, но фрагменты сна продолжают приходить — числа ниже могут быть неполными.</i>",

	// Per-metric "device off" banners
	"tg_watch_off":     "🔕 <b>Apple Watch не на руке</b> — последние HRV/RHR были %s назад. Восстановление не оценить.",
	"tg_phone_off":     "📵 <b>Телефон не синкает</b> — данных о шагах нет уже %s. Активность пропущена.",
	"tg_sleep_silence": "😴 <b>Сон не записан</b> — последняя ночь с данными была %s назад.",
	"tg_dur_hours":     "%dч",
	"tg_dur_days":      "%d дн.",

	// Weekly data-quality digest
	"tg_digest_header":      "🔬 Качество данных за неделю",
	"tg_digest_clean":       "Всё чисто — за последние %d дней аномалий нет.",
	"tg_digest_impossible":  "🚫 <b>Невозможные значения</b> (ошибки сенсора)",
	"tg_digest_suspect":     "⚠️ <b>Подозрительные значения</b> (>3σ от твоей нормы)",
	"tg_digest_missed":      "😴 <b>Ночи без данных о сне</b>",
	"tg_digest_watch_off":   "🔕 <b>Часы сняты:</b> около %dч за период",
	"tg_digest_more_in_ui":  "<i>Подробности по строкам — в админке.</i>",

	// Onboarding nudge
	"tg_energy_backfill_nudge_header": "📊 Исторический EnergyBank ждёт",
	"tg_energy_backfill_nudge_body":   "У тебя {complete} полных дней истории, но всего {backfilled} backfilled-снимков EnergyBank. Пока не запустишь backfill, пороги вердикта (rest / moderate / push hard) работают на дефолтных значениях, а не на твоей личной шкале — рекомендации смещены, отчёты в Telegram недокалиброваны под твою реальную физиологию.",
	"tg_energy_backfill_nudge_cta":    "Открыть Настройки → Исторический EnergyBank",

	// Monthly stress-validation nudge — STRESS_MEASUREMENT.md §4.5.
	"tg_stress_validation_header": "🎯 Stress-формула валидирована",
	"tg_stress_validation_body":   "§4.5 4-канальная rubric вернула <b>{verdict}</b> для тебя. {reason} Можно включать <code>settings.energy.stress_drain_enabled</code>, если хочешь чтобы EnergyBank drain реагировал на длительную автономную нагрузку.",
	"tg_stress_validation_cta":    "Открыть /admin → Валидация stress-формулы",
}
