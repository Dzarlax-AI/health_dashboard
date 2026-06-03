package ui

// translationsRu holds the Russian UI strings. Missing keys fall back
// to English via T().
var translationsRu = map[string]string{
	"app_title":             "Здоровье",
	"explore":               "Поиск",
	"loading":               "Загрузка данных",
	"readiness":             "Готовность",
	"recovery":              "Восстановление",
	"readiness_today_label": "Сегодня",

	"section_cardio_title":      "Сердце и лёгкие",
	"section_cardio_subtitle":   "Пульс покоя · HRV · VO2 · дыхание",
	"section_activity_title":    "Активность",
	"section_activity_subtitle": "Шаги · калории · упражнения · расстояние",
	"section_recovery_title":    "Восстановление",
	"section_recovery_subtitle": "Сон · CV HRV · температура запястья",

	"readiness_trend_label":      "Тренд 7д",
	"back":                       "Назад",
	"compare":                    "Сравнить",
	"all_metrics":                "Метрики",
	"nav_settings":               "Настройки",
	"nav_logout":                 "Выйти",
	"your_trends":                "Ваши тренды",
	"search_placeholder":         "Поиск метрик...",
	"esc_hint":                   "ESC — закрыть",
	"no_metrics_found":           "Метрики не найдены",
	"no_data":                    "Нет данных",
	"no_data_range":              "Нет данных за этот период",
	"no_sleep_data":              "Нет данных о сне за этот период",
	"start_syncing":              "Начните синхронизацию данных о здоровье.",
	"data_from":                  "Данные от ",
	"days_ago":                   "д. назад",
	"this_week":                  "Эта неделя",
	"activity_vs_recovery":       "Активность и восстановление",
	"activity_recovery_subtitle": "Как нагрузка влияет на ВСР",
	"activity_load":              "Нагрузка",
	"sleep_section":              "Сон",
	"sleep_subtitle":             "Среднее за 7 ночей",
	"deep_sleep":                 "Глубокий сон",
	"rem_sleep":                  "REM сон",
	"awake_time":                 "Бодрствование",
	"efficiency":                 "Эффективность",
	"bucket":                     "Период",
	"agg":                        "Агр.",
	"auto":                       "Авто",
	"minute":                     "Минута",
	"hour":                       "Час",
	"day":                        "День",
	"preset_all":                 "Всё",
	"avg":                        "Ср.",
	"sum":                        "Сумма",
	"max":                        "Макс",
	"min":                        "Мин",
	"previous_period":            "Прошлый период",
	"vs_yesterday":               "к вчера",
	"stable":                     "Стабильно",
	"load_pct":                   "Нагрузка %",
	"hrv_ms":                     "ВСР мс",
	"nights":                     "Ночей",
	"avg_total":                  "Ср. всего",
	"avg_deep":                   "Ср. глубокий",
	"avg_rem":                    "Ср. REM",
	"points":                     "Точки",
	"stale_prefix":               "Данные от ",
	"stale_suffix":               "д. назад",
	"status_good":                "Хорошо",
	"status_fair":                "Требует внимания",
	"status_low":                 "Берегите себя",
	"cat_heart":                  "Сердце и показатели",
	"cat_activity":               "Активность",
	"cat_fitness":                "Фитнес",
	"cat_sleep":                  "Сон",
	"cat_body":                   "Тело",
	"cat_env":                    "Окружающая среда",
	"cat_nutrition":              "Питание",
	"cat_other":                  "Прочее",
	"phase_deep":                 "Глубокий",
	"phase_rem":                  "REM",
	"phase_core":                 "Основной",
	"phase_awake":                "Бодрствование",
	"trend_steps":                "Шаги",
	"trend_heart_rate":           "ЧСС",
	"trend_sleep":                "Сон",
	"trend_hrv":                  "ВСР",
	"trend_readiness":            "Готовность",
	"ai_insight_title":           "Развёрнутый AI-отчёт",

	"illness_suspicion_title":                  "Возможные признаки болезни",
	"illness_suspicion_desc":                   "Данные носимого устройства показывают сходящиеся признаки респираторной или автономной нагрузки. Это не диагноз.",
	"illness_suspicion_moderate":               "Умеренно",
	"illness_suspicion_high":                   "Высоко",
	"illness_suspicion_signals":                "Сигналы",
	"illness_signal_respiratory_rate":          "Частота дыхания",
	"illness_signal_blood_oxygen_saturation":   "Кислород крови",
	"illness_signal_resting_heart_rate":        "Пульс покоя",
	"illness_signal_heart_rate_variability":    "ВСР",
	"illness_signal_wrist_temperature":         "Температура запястья",
	"illness_signal_sleep_total":               "Нарушение сна",
	"illness_signal_sleep_disruption":          "Нарушение сна",
	"illness_signal_sustained_hr_load":         "Длительная ЧСС-нагрузка",
	"illness_signal_objective_illness_pattern": "Паттерн последних дней",
	"illness_signal_stress_flags":              "Стресс-флаги",
	"illness_signal_subjective_checkin":        "Самочувствие",

	"teps": "Шаги",
	"leep": "Сон",
	"ate":  "ЧДД",

	// Metric labels.
	"metric_sleep_total":                       "Общий сон",
	"metric_night_sleep_total":                 "Ночной сон",
	"metric_nap_total":                         "Дневной сон",
	"metric_sleep_deep":                        "Глубокий сон",
	"metric_sleep_rem":                         "REM сон",
	"metric_sleep_core":                        "Основной сон",
	"metric_sleep_unspecified":                 "Сон (без стадий)",
	"metric_sleep_awake":                       "Бодрствование",
	"chart_sleep_unspecified_hint":             "источник не предоставил разбивку на фазы",
	"metric_heart_rate":                        "ЧСС",
	"metric_resting_heart_rate":                "Пульс покоя",
	"metric_walking_heart_rate_average":        "Пульс при ходьбе",
	"metric_heart_rate_variability":            "ВСР",
	"metric_blood_oxygen_saturation":           "Кислород крови",
	"metric_respiratory_rate":                  "ЧДД",
	"metric_step_count":                        "Шаги",
	"metric_walking_running_distance":          "Дистанция",
	"metric_active_energy":                     "Акт. калории",
	"metric_basal_energy_burned":               "Калории покоя",
	"metric_apple_exercise_time":               "Упражнения",
	"metric_apple_stand_time":                  "Время стоя",
	"metric_apple_stand_hour":                  "Часы стоя",
	"metric_physical_effort":                   "Физ. нагрузка",
	"metric_flights_climbed":                   "Пролёты лестниц",
	"metric_stair_speed_up":                    "Скорость по лестнице",
	"metric_walking_speed":                     "Скорость ходьбы",
	"metric_walking_step_length":               "Длина шага",
	"metric_walking_double_support_percentage": "Двойная опора",
	"metric_walking_asymmetry_percentage":      "Асимметрия ходьбы",
	"metric_apple_sleeping_wrist_temperature":  "Темп. запястья",
	"metric_breathing_disturbances":            "Нарушения дыхания",
	"metric_environmental_audio_exposure":      "Шумовая нагрузка",
	"metric_headphone_audio_exposure":          "Громкость наушников",
	"metric_time_in_daylight":                  "Дневной свет",
	"metric_vo2_max":                           "МПК (VO2 Max)",
	"metric_six_minute_walking_test_distance":  "6-мин ходьба",
	"metric_readiness":                         "Готовность",
	"metric_oxygen_saturation":                 "Кислород крови",
	"metric_heart_rate_variability_sdnn":       "ВСР",
	"metric_environmental_audio":               "Шум окружения",
	"metric_headphone_audio":                   "Громкость наушников",
	"metric_walking_double_support":            "Двойная опора",
	"metric_walking_asymmetry":                 "Асимметрия ходьбы",
	"metric_environmental_sound_reduction":     "Шумоподавление",
	"metric_stair_ascent_speed":                "Скорость подъёма",
	"metric_stair_descent_speed":               "Скорость спуска",
	"metric_wrist_temperature":                 "Темп. запястья",
	"metric_walking_steadiness":                "Устойчивость ходьбы",
	"metric_body_mass":                         "Масса тела",
	"metric_body_mass_index":                   "ИМТ",
	"metric_body_fat_percentage":               "Жировая масса",
	"metric_lean_body_mass":                    "Сухая масса",
	"metric_height":                            "Рост",
	"metric_blood_pressure_systolic":           "АД систолическое",
	"metric_blood_pressure_diastolic":          "АД диастолическое",
	"metric_heart_rate_recovery":               "Восст. ЧСС",
	"metric_distance_cycling":                  "Велодистанция",
	"metric_distance_swimming":                 "Дистанция плавания",
	"metric_swimming_stroke_count":             "Гребки",
	"metric_mindful_minutes":                   "Медитация",
	"metric_alcoholic_beverages":               "Алкоголь",
	"metric_six_min_walk_distance":             "6-мин ходьба",
	"metric_dietary_energy":                    "Калории (питание)",
	"metric_dietary_protein":                   "Белки",
	"metric_dietary_carbs":                     "Углеводы",
	"metric_dietary_fat":                       "Жиры",
	"metric_dietary_fat_saturated":             "Насыщ. жиры",
	"metric_dietary_fat_monounsaturated":       "Мононенасыщ. жиры",
	"metric_dietary_fat_polyunsaturated":       "Полиненасыщ. жиры",
	"metric_dietary_water":                     "Вода",
	"metric_dietary_sodium":                    "Натрий",
	"metric_dietary_sugar":                     "Сахар",
	"metric_dietary_fiber":                     "Клетчатка",
	"metric_dietary_caffeine":                  "Кофеин",
	"metric_dietary_calcium":                   "Кальций",
	"metric_dietary_iron":                      "Железо",
	"metric_dietary_cholesterol":               "Холестерин",
	"metric_dietary_potassium":                 "Калий",
	"metric_dietary_magnesium":                 "Магний",
	"metric_dietary_phosphorus":                "Фосфор",
	"metric_dietary_zinc":                      "Цинк",
	"metric_dietary_copper":                    "Медь",
	"metric_dietary_manganese":                 "Марганец",
	"metric_dietary_selenium":                  "Селен",
	"metric_dietary_iodine":                    "Йод",
	"metric_dietary_molybdenum":                "Молибден",
	"metric_dietary_folate":                    "Фолат",
	"metric_dietary_biotin":                    "Биотин (B7)",
	"metric_dietary_vitamin_a":                 "Витамин A",
	"metric_dietary_vitamin_c":                 "Витамин C",
	"metric_dietary_vitamin_d":                 "Витамин D",
	"metric_dietary_vitamin_e":                 "Витамин E",
	"metric_dietary_vitamin_k":                 "Витамин K",
	"metric_dietary_vitamin_b6":                "Витамин B6",
	"metric_dietary_vitamin_b12":               "Витамин B12",
	"metric_dietary_niacin":                    "Ниацин (B3)",
	"metric_dietary_riboflavin":                "Рибофлавин (B2)",
	"metric_dietary_thiamin":                   "Тиамин (B1)",
	"metric_dietary_pantothenic_acid":          "Пантотен. к-та (B5)",
	"by_source":                                "Источники",
	"source_comparison":                        "Сравнение источников",
	"lbl_total":                                "Итого",
	"lbl_deep":                                 "Глубокий",
	"lbl_core":                                 "Основной",
	"how_it_works":                             "Как это работает",
	"health_sections":                          "Обзор здоровья",
	"at_a_glance":                              "Сегодня",

	"trend_vs_7d":  "к 7д",
	"trend_vs_30d": "к 30д",

	"energy_label":                      "Энергетический банк",
	"energy_capacity_label":             "Капасити на сегодня",
	"energy_current_label":              "Сейчас доступно",
	"energy_drain_label":                "Потрачено",
	"energy_verdict_push_hard":          "Можно жёстко",
	"energy_verdict_moderate":           "Умеренный день",
	"energy_verdict_active_recovery":    "Только активное восстановление",
	"energy_verdict_rest":               "День отдыха",
	"energy_component_morning_capacity": "Утренняя капасити",
	"energy_component_activity_load":    "Нагрузка (сегодня vs 28-дн норма)",
	"energy_component_autonomic_stress": "Автономный стресс (RHR / HRV)",
	"energy_state_good_title":           "Заряжен",
	"energy_state_good_desc":            "Запас полный — сегодня можно бросить себе вызов.",
	"energy_state_medium_title":         "В балансе",
	"energy_state_medium_desc":          "Резерв приличный — тренируйся, но следи за объёмом.",
	"energy_state_low_title":            "Резерв на исходе",
	"energy_state_low_desc":             "Запас тонкий — держи лёгкий темп и фокус на восстановление.",
	"energy_state_critical_title":       "Истощён",
	"energy_state_critical_desc":        "Бак почти пуст — отдых принесёт больше всего пользы.",
	"details":                           "Детали",

	// Subjective morning check-in confirmation on the dashboard hero.
	"checkin_today_label":  "Ваш утренний ответ:",
	"checkin_answer_great": "Отлично",
	"checkin_answer_ok":    "Нормально",
	"checkin_answer_meh":   "Не очень",
	"checkin_answer_sick":  "Болен(а)",

	// Methodology status badges (see i18n_en.go for the full mapping).
	"methodology_status_heuristic_personalized":           "Эвристика",
	"methodology_status_heuristic_personalized_desc":      "Экспертная формула на персональных baseline. Не валидированная предсказательная модель.",
	"methodology_status_heuristic_prescriptive":           "Эвристика",
	"methodology_status_heuristic_prescriptive_desc":      "Предписания по правилам поверх эвристики capacity/drain. Пока не валидировано против субъективного состояния.",
	"methodology_status_experimental_formula":             "Экспериментально",
	"methodology_status_experimental_formula_desc":        "Формула на проверке. Пока не production-уровень принятия решений.",
	"methodology_status_validated_floor_candidate":        "Проверенная база",
	"methodology_status_validated_floor_candidate_desc":   "Целевой показатель без утечек данных, с проверенной базовой линией (EWMA45). Production-инфраструктура; обученной модели поверх ещё нет.",
	"methodology_status_labeling_framework_ready":         "Готовые метки",
	"methodology_status_labeling_framework_ready_desc":    "Размеченные события без утечек данных, готовые для будущих моделей. Сама метка и есть результат.",
	"methodology_status_experimental_not_production":      "Экспериментально",
	"methodology_status_experimental_not_production_desc": "На проверке. Не воспринимай этот score как валидированный.",

	"admin_title":               "Админка",
	"admin_cache_status":        "Состояние кэша",
	"admin_refresh":             "Обновить",
	"admin_actions":             "Действия",
	"admin_raw":                 "Сырые данные",
	"admin_minute":              "Минутный кэш",
	"admin_hourly":              "Часовой кэш",
	"admin_daily":               "Дневные оценки",
	"admin_metrics":             "метрик",
	"admin_empty":               "пусто",
	"admin_score_version":       "Версия скоринга",
	"admin_last_sync":           "Последний синх",
	"admin_incremental_title":   "Обновить кэш",
	"admin_incremental_desc":    "Дозаполнить пропущенные записи. Быстро, безопасно.",
	"admin_force_title":         "Перестроить всё",
	"admin_force_desc":          "Очистить и пересчитать все кэши. Используйте после изменения формулы.",
	"admin_run":                 "Запустить",
	"admin_force_run":           "Перестроить",
	"admin_target_user":         "Целевой пользователь",
	"admin_target_user_current": "— текущий пользователь —",

	"admin_notify_title":            "Telegram-отчёты",
	"admin_notify_morning_title":    "Утренний отчёт",
	"admin_notify_morning_desc":     "Отправить тестовую сводку по сну сейчас.",
	"admin_notify_evening_title":    "Вечерний отчёт",
	"admin_notify_evening_desc":     "Отправить тестовую сводку за день сейчас.",
	"admin_notify_token":            "Токен бота",
	"admin_notify_chat_id":          "Chat ID",
	"admin_notify_webhook_label":    "Webhook",
	"admin_notify_webhook_retry":    "Повторить",
	"webhook_badge_ok":              "✓ зарегистрирован",
	"webhook_badge_pending":         "⏳ регистрируется",
	"webhook_badge_failed":          "✗ ошибка",
	"webhook_badge_deleted":         "— удалён",
	"webhook_badge_unknown":         "— неизвестно",
	"admin_notify_lang":             "Язык",
	"admin_notify_timezone":         "Часовой пояс",
	"admin_notify_timezone_hint":    "Нужен для ежедневных отчётов и расчёта исторического EnergyBank. Формат IANA (например, Europe/Belgrade, America/New_York).",
	"admin_notify_schedule_morning": "Час утреннего отчёта",
	"admin_notify_schedule_evening": "Час вечернего отчёта",
	"admin_notify_weekday":          "Будни",
	"admin_notify_weekend":          "Выходные",
	"admin_notify_save":             "Сохранить",
	"admin_notify_saved":            "Настройки сохранены",
	"admin_notify_send":             "Отправить тест",
	"admin_notify_test_morning":     "Тест утро",
	"admin_notify_test_evening":     "Тест вечер",

	"admin_energy_backfill_title":      "Исторический EnergyBank",
	"admin_energy_backfill_desc":       "Пересчитать снимки EnergyBank по импортированной истории Apple Health. Без этого персональная калибровка вердиктов работает на дефолтных порогах, а не на твоей реальной шкале.",
	"admin_energy_backfill_loading":    "Загрузка…",
	"admin_energy_backfill_load_error": "Не удалось загрузить статус backfill.",
	"admin_energy_backfill_summary":    "{complete} полных дней истории · {backfilled} снимков EnergyBank уже backfilled · диапазон: {earliest} → {to}",
	"admin_energy_backfill_run":        "Посчитать исторический EnergyBank",
	"admin_energy_backfill_running":    "Backfill уже выполняется.",
	"admin_energy_backfill_need_tz":    "Сначала выставь часовой пояс выше.",
	"admin_energy_backfill_no_data":    "Полных дней нет — импортируй данные Apple Health или дождись накопления через live ingest.",
	"admin_energy_backfill_confirm":    "Это пересчитает EnergyBank за каждый исторический день. Продолжить?",
	"admin_energy_backfill_starting":   "Запуск…",
	"admin_energy_backfill_progress":   "День {done} из {total} · ok={ok}",
	"admin_energy_backfill_done":       "Готово: {ok} записано · {skipped} пропущено (нет данных для lookback) · {errors} ошибок",

	"admin_gaps_section_title":        "Целостность данных",
	"admin_gaps_check":                "Проверить пробелы",
	"admin_quality_title":             "Проверка качества данных",
	"admin_quality_run":               "Запустить аудит",
	"admin_quality_fix":               "Пометить подозрительные + почистить мусор",
	"admin_quality_digest":            "Отправить дайджест сейчас",
	"admin_quality_clean":             "Всё чисто — аномалий не найдено.",
	"admin_quality_total":             "Всего строк",
	"admin_quality_bad":               "Вне нормы",
	"admin_quality_range":             "Диапазон",
	"admin_quality_metric":            "Метрика",
	"admin_quality_sample":            "Пример",
	"admin_quality_week":              "За 7 дней",
	"admin_quality_impossible":        "Невозможные",
	"admin_quality_suspect":           "Подозрительные",
	"admin_quality_missed":            "Пропущенные ночи",
	"admin_quality_fixed":             "Помечено: невозможных %d, подозрительных %d.",
	"admin_checkin_coverage_title":    "Покрытие утренних check-in",
	"admin_checkin_coverage_desc":     "Только чтение: последние утренние Telegram-вопросы и задержка ответа.",
	"admin_checkin_total":             "Дней",
	"admin_checkin_prompted_coverage": "Промпт был",
	"admin_checkin_answered_coverage": "Ответ был",
	"admin_checkin_avg_response":      "Средний ответ",
	"admin_checkin_no_response":       "Нет ответов",
	"admin_checkin_answers":           "Ответы:",
	"admin_checkin_date":              "Дата",
	"admin_checkin_status":            "Статус",
	"admin_checkin_answer":            "Ответ",
	"admin_checkin_latency":           "Задержка",
	"admin_checkin_status_prompted":   "Ожидает",
	"admin_checkin_status_answered":   "Ответ",
	"admin_checkin_status_late":       "Поздно",
	"admin_checkin_status_expired":    "Истёк",
	"admin_checkin_status_missing":    "Нет строки",

	"admin_ai_title":      "AI-брифинг утром",
	"admin_ai_key":        "Gemini API ключ",
	"admin_ai_model":      "Модель",
	"admin_ai_max_tokens": "Макс. токенов",
	"admin_ai_save":       "Сохранить",
	"admin_ai_saved":      "Настройки сохранены",

	"admin_energy_title":                        "EnergyBank v2.2 — стресс-дрейн",
	"admin_energy_warning":                      "Non-production значения до тех пор, пока §4.5 валидация не вернёт 'validated'. Effective β = 0, пока stress drain выключен — независимо от значения β ниже.",
	"admin_energy_stress_enabled":               "Stress drain включён",
	"admin_energy_beta":                         "β (коэф. стресс-дрейна)",
	"admin_energy_z_threshold":                  "z-score порог",
	"admin_energy_effective_beta":               "Effective β (live)",
	"admin_energy_save":                         "Сохранить",
	"admin_energy_saved":                        "Настройки сохранены",
	"admin_stress_observability_title":          "Наблюдаемость stress-нагрузки",
	"admin_stress_observability_desc":           "Read-only операционный срез наблюдаемой sustained stress load. Это не включает beta и не меняет EnergyBank drain.",
	"admin_stress_observability_loading":        "Загружаю наблюдаемость stress-нагрузки...",
	"admin_stress_observability_load_error":     "Не удалось загрузить наблюдаемость stress-нагрузки.",
	"admin_stress_observability_observed_only":  "Только наблюдение: stress_drain_enabled=false, поэтому effective beta равен 0 и stress load не применяется к EnergyBank drain.",
	"admin_stress_observability_applied":        "Применяется: effective beta ненулевой, sustained stress load влияет на EnergyBank drain.",
	"admin_stress_observability_window":         "Окно",
	"admin_stress_observability_load_stats":     "Статистика load",
	"admin_stress_observability_flag_counts":    "Счётчики флагов",
	"admin_stress_observability_validation":     "Валидация",
	"admin_stress_observability_effective_beta": "Effective beta",
	"admin_stress_observability_no_flags":       "Флагов нет",
	"admin_help_summary":                        "Что это и когда менять?",
	"admin_energy_help_html": `<h4 style="margin:8px 0 4px">β — коэффициент stress-drain</h4>
<p>Множитель, переводящий <code>sustained_hr_load_z</code> в очки drain. Формула: <code>drain = α·active_kcal + <strong>β</strong>·load_z</code>.</p>
<p><strong>Default 0.8</strong> (placeholder из §6 Q3). Когда validation rubric ниже вернёт <code>validated</code> — подкручивать в диапазоне 0.4–1.2, в зависимости от того насколько резво хочется чтобы бар реагировал на длительную автономную нагрузку.</p>
<h4 style="margin:12px 0 4px">z-threshold — порог по часу</h4>
<p>В load integral идут только часы, где HR выше дневного baseline более чем на это число SD.</p>
<p><strong>Default 0.5.</strong> Поднять до 0.7–0.8 если сидячая работа и HR сидит на z≈0.5 систематически без реального стресса (mental activation ≠ автономная нагрузка → false positives).</p>
<h4 style="margin:12px 0 4px">Stress drain enabled — мастер-переключатель</h4>
<p>Когда ВЫКЛ (default), <code>sustained_hr_load_z</code> всё равно считается и пишется в <code>components</code> JSONB для audit, но в bank не идёт — β_effective = 0.</p>
<p><strong>Включать ТОЛЬКО когда validation rubric ниже вернула <code>validated</code>.</strong> Включишь раньше — drain будет реагировать на шум.</p>
<h4 style="margin:12px 0 4px">Workflow</h4>
<ol style="margin:4px 0 0 18px;padding:0">
  <li>Прогнать validation rubric (кнопка ниже).</li>
  <li>Verdict ≠ validated → β=0, ничего не трогать. Monthly Telegram nudge придёт сам когда verdict перейдёт.</li>
  <li>Verdict = validated → включить Stress drain enabled, β=0.8.</li>
  <li>Через ~2 недели — если drain слишком агрессивен, уменьшить β до 0.4–0.5. Перепрогонять rubric раз в месяц.</li>
</ol>`,
	"admin_stress_validation_help_html": `<h4 style="margin:8px 0 4px">Что считает rubric</h4>
<p>Pearson r на скользящем 30-дневном окне между <code>sustained_hr_load[d]</code> и тремя независимыми next-morning сигналами восстановления. Логика: если формула реально ловит стресс — высокий load сегодня предсказывает деградацию autonomic-маркеров утром.</p>
<h4 style="margin:12px 0 4px">Каналы</h4>
<dl style="margin:4px 0">
  <dt><strong>Channel 1 — утренний HRV (primary, анкер)</strong></dt>
  <dd>Корреляция load[d] с overnight HRV[d+1]. Ожидаемый знак: <strong>отрицательный</strong> (high load → low HRV).</dd>
  <dt style="margin-top:6px"><strong>Channel 2 — сдвиг утреннего RHR (secondary)</strong></dt>
  <dd>load[d] vs (overnight RHR[d+1] − 30d baseline). Ожидаемый знак: <strong>положительный</strong>. Cross-check.</dd>
  <dt style="margin-top:6px"><strong>Channel 3 — архитектура сна (tertiary)</strong></dt>
  <dd>Голосование 3 sub-correlations: sleep_awake (+), onset latency (+), deep% (−). Sub-signal голосует если |r|≥0.10 в ожидаемом направлении; channel "agrees" при ≥2 голосах.</dd>
</dl>
<h4 style="margin:12px 0 4px">Вердикты</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li><strong style="color:var(--good)">validated</strong> — r ≤ −0.30 на channel 1 И хотя бы один cross-channel согласен. Можно включать <code>Stress drain enabled</code>.</li>
  <li><strong style="color:var(--fair)">weak</strong> — −0.30 &lt; r &lt; −0.10. β=0. Recheck через месяц.</li>
  <li><strong style="color:var(--low)">inconclusive</strong> — |r| &lt; 0.10 на channel 1, или меньше 15 HRV образцов (sparsity fallback). Нужно больше ночного HRV (Breathe sessions на Apple Watch помогают).</li>
  <li><strong style="color:var(--low)">wrong_direction</strong> — r &gt; 0. Формула не ловит физиологию этого пользователя — manual review.</li>
</ul>
<h4 style="margin:12px 0 4px">Когда запускать</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li>После major data ingest (импорт Apple Health, восстановление sleep).</li>
  <li>Раз в месяц как routine. Monthly Telegram nudge придёт сам когда verdict перейдёт в <strong>validated</strong>.</li>
  <li>Перед включением <code>Stress drain enabled</code> выше.</li>
</ul>`,
	"admin_stress_validation_title":       "Валидация stress-формулы (§4.5)",
	"admin_stress_validation_desc":        "Pearson r на скользящем 30-дневном окне: утренний HRV (основной), сдвиг утреннего RHR, архитектура сна. Только чтение — НЕ переключает stress_drain_enabled автоматически. Решение оператора после ознакомления с вердиктом.",
	"admin_stress_validation_run":         "Запустить",
	"admin_stress_validation_loading":     "Считаю rubric на 30-дневном окне…",
	"admin_stress_validation_sparse":      "мало данных",
	"admin_stress_validation_no_data":     "нет данных",
	"admin_stress_validation_flags_label": "флаги:",
	"admin_stress_validation_ch1":         "Канал 1 (утренний HRV)",
	"admin_stress_validation_ch2":         "Канал 2 (сдвиг утреннего RHR)",
	"admin_stress_validation_ch3":         "Канал 3 (архитектура сна)",
	"admin_stress_validation_votes":       "голоса",
	"admin_stress_validation_window_fmt":  "окно {window} дней, {days} дней с данными",

	"admin_import_title":     "Импорт Apple Health",
	"admin_import_desc":      "Загрузите export.zip для импорта исторических данных. Дубликаты пропускаются.",
	"admin_import_choose":    "Выбрать файл…",
	"admin_import_batch":     "записей/батч",
	"admin_import_pause":     "мс пауза",
	"admin_import_start":     "Начать импорт",
	"admin_import_uploading": "Загрузка…",
	"admin_import_running":   "Импорт выполняется…",

	"stress_flags_aria":              "Сигналы стресса",
	"stress_detail_what":             "Что это",
	"stress_detail_cause":            "Что вызвало",
	"stress_detail_risk":             "Чем важно",
	"stress_detail_action":           "Что делать",
	"stress_flag_acute_stress_label": "Скачок пульса",
	"stress_flag_acute_stress_detail_html": `<h5>Что это</h5>
<p>Один час за день пульс резко подскочил выше вашей нормы.</p>
<h5>Что вызвало</h5>
<p>Кофе, неожиданный звонок, нервы перед встречей, испуг, кратковременный конфликт — любая короткая активация симпатической нервной системы.</p>
<h5>Чем важно</h5>
<p>Само по себе — ничем. Изолированный пик за день не нагружает систему, это нормальная реакция организма.</p>
<h5>Что делать</h5>
<p>Ничего. Флаг показан для диагностики — чтобы было видно, что это разовое событие, а не хронический стресс.</p>`,
	"stress_flag_sustained_load_label": "Долгий рост пульса",
	"stress_flag_sustained_load_detail_html": `<h5>Что это</h5>
<p>Пульс держался выше вашей нормы как минимум 4 часа подряд сегодня.</p>
<h5>Что вызвало</h5>
<p>Длинный стрессовый день (deadline, переговоры), тяжёлая тренировка с долгим восстановлением, начало болезни, обезвоживание, недосып.</p>
<h5>Чем важно</h5>
<p>Реальная нагрузка на автономную нервную систему. Несколько таких дней подряд — расход энергии накапливается, восстановление замедляется. EnergyBank уже учёл это: бар сегодня сел больше обычного.</p>
<h5>Что делать</h5>
<p>Вечером — ранний отбой, без алкоголя и кофеина после обеда. Завтра — лёгкая активность, не повторять нагрузку.</p>`,
	"stress_flag_illness_signature_label": "Похоже на болезнь",
	"stress_flag_illness_signature_detail_html": `<h5>Что это</h5>
<p>Температура тела, частота дыхания и HRV — все три отклонились от вашей нормы в "болезненную" сторону одновременно.</p>
<h5>Что вызвало</h5>
<p>Чаще всего — вирусная инфекция в начальной стадии. Реже — серьёзная перетренированность с подсаженным иммунитетом.</p>
<h5>Чем важно</h5>
<p>Жёсткая тренировка на этом фоне обычно затягивает простуду на лишние 3-5 дней. Иммунка работает на отбой инфекции — занимать её спортом сверху значит давать организму двойную нагрузку.</p>
<h5>Что делать</h5>
<p>Сегодня — спать. Тренировку отменить или сделать максимально лёгкой (медленная прогулка). Вода, тёплая еда. Завтра проверить состояние: если флаг ушёл, можно постепенно возвращаться к плану.</p>`,
	"stress_flag_recovery_debt_label": "Не восстановились",
	"stress_flag_recovery_debt_detail_html": `<h5>Что это</h5>
<p>Ночью HRV упал ниже вашей нормы, а пульс покоя — поднялся выше. Сон не сбросил вчерашнюю нагрузку.</p>
<h5>Что вызвало</h5>
<p>Вчерашняя интенсивная тренировка или рабочий день, поздний приём пищи, алкоголь, поздний отбой, эмоциональный стресс.</p>
<h5>Чем важно</h5>
<p>Сегодня вы уже не в оптимальной форме. Если игнорировать и пойти на жёсткую тренировку — растёт риск перетренироваться, и шансы получить мелкую травму выше обычного.</p>
<h5>Что делать</h5>
<p>Лёгкий день: прогулка, мобилка, йога. Хорошо поесть, рано лечь. Завтра флаг должен уйти — если нет, два лёгких дня подряд лучше, чем заработать настоящий перетрен.</p>`,
	"stress_flag_parasympathetic_rebound_label": "Восстановление",
	"stress_flag_parasympathetic_rebound_detail_html": `<h5>Что это</h5>
<p>Пульс был выше нормы, но HRV тоже был выше нормы. Парасимпатическая нервная система активна — это режим восстановления, не стресс.</p>
<h5>Что вызвало</h5>
<p>Обычно — после тяжёлой тренировки или интенсивного дня. Тело потратило ресурс и теперь активно его восстанавливает.</p>
<h5>Чем важно</h5>
<p>Ничем плохим — это здоровая реакция. <strong>Не путать с острым стрессом</strong>: внешне пульс высокий, но физиология совсем другая.</p>
<h5>Что делать</h5>
<p>Дать организму время: лёгкий день, нормальный сон. Можно тренироваться, но не на пределе.</p>`,
	"stress_flag_stale_stress_label": "Пробел в данных",
	"stress_flag_stale_stress_detail_html": `<h5>Что это</h5>
<p>День закончился, а в бодрствующем окне собралось меньше 8 часов записей пульса. Поэтому накопленная нагрузка сердца на этот день не считается.</p>
<h5>Что произошло</h5>
<p>Скорее всего часы лежали без руки заметную часть дня или была пауза в синхронизации с iPhone.</p>
<h5>Что делать</h5>
<p>Стабильнее носить часы и проверять синхронизацию. Сегодняшние цифры пересчитываться не будут — прошлые дни мы не перерисовываем.</p>`,
	"stress_flag_data_accruing_label": "Накапливаем данные",
	"stress_flag_data_accruing_detail_html": `<h5>Что это</h5>
<p>День ещё не закончился. Для оценки накопленной нагрузки сердца нужно минимум 8 часов записей пульса в дневное окно, а пока столько не набралось.</p>
<h5>Почему так</h5>
<p>Score отвечает на вопрос «правда ли пульс был повышен ощутимый кусок дня?». Этот ответ имеет смысл только когда сам день уже прошёл ощутимый кусок. Показывать число с утра было бы вводящим в заблуждение.</p>
<h5>Что делать</h5>
<p>Ничего — просто проносить часы свой обычный день. Флаг уйдёт автоматически, как только наберётся достаточно часов.</p>`,
	"stress_flag_calibration_warmup_label": "Учим норму",
	"stress_flag_calibration_warmup_detail_html": `<h5>Что это</h5>
<p>Личная норма (HRV, пульс, дыхание) ещё учится — нужно около недели непрерывных данных. Пороги стресс-флагов пока консервативные.</p>
<h5>Что делать</h5>
<p>Просто носить часы. Через несколько дней флаг уйдёт автоматически.</p>`,

	// ─── Admin: Readiness redesign — operational contract preview ───
	"admin_contract_title": "Предпросмотр контракта Readiness",
	"admin_contract_desc":  "Таблица по дням: что покажет каждая плашка readiness. Бинарные плашки (Acute, Chronic) берут порог из <code>chip_calibrations</code> — обновить можно кнопкой «Пересчитать калибровки плашек» в группе Operations ниже. Наведи на ячейку, чтобы увидеть значение, порог, baseline, цель и эпоху.",

	// ─── Admin: Operations group ────────────────────────────────────
	"admin_ops_group":            "Операции",
	"admin_ops_group_desc":       "Действия из этой группы выполняются только над активной вкладкой профиля.",
	"admin_ops_redesign_title":   "Readiness redesign",
	"admin_ops_redesign_desc":    "Пороги плашек для каждого тенанта считаются по последним 180 дням. Нажми <strong>Пересчитать</strong> после смены конфигурации или свежего backfill. Результат отразится в предпросмотре контракта выше.",
	"admin_chip_recompute_title": "Пересчитать калибровки плашек",
	"admin_chip_recompute_desc":  "Берёт последние 180 подходящих дней, считает порог и страховку по base rate для каждой бинарной плашки, пишет в <code>chip_calibrations</code>. Применяется только к активной вкладке профиля.",
	"admin_chip_recompute_btn":   "Пересчитать",

	"admin_quality_maintenance_title": "Чистка качества данных",
	"admin_quality_maintenance_desc":  "Эти кнопки меняют флаги в <code>metric_points.quality</code> или отправляют дайджест в Telegram. Read-only аудит лежит в <strong>Status &amp; diagnostics</strong> выше — открой сначала там, чтобы увидеть, что поменяется.",
	"admin_quality_fix_desc":          "Помечает значения вне допустимого диапазона как <code>impossible</code>, а z-score-выбросы — как <code>suspect</code>, чтобы они не попадали в скоринг.",
	"admin_quality_digest_desc":       "Отправить недельный quality-дайджест в Telegram прямо сейчас (обычно уходит в настроенный день недели).",

	// ─── Admin: Onboarding wizard — page shell ──────────────────────
	"admin_wizard_title":       "Readiness redesign — мастер онбординга тенанта",
	"admin_wizard_desc":        "7 шагов для активной вкладки профиля. Каждый шаг каждый раз заново читает состояние из базы — можно закрыть страницу и вернуться, ничего не теряется (нет ни сессий, ни кук). На шагах с записью <code>schema=all</code> запрещён. Чтобы перерисовать любой шаг, нажми его <em>Обновить</em>.",
	"admin_wizard_load_all":    "Загрузить мастер для активного профиля",
	"admin_wizard_pick_tenant": "Открой вкладку профиля и нажми <em>Загрузить</em>.",
	"admin_wizard_refresh":     "Обновить",
	"admin_wizard_show_plan":   "Показать план",
	"admin_wizard_recompute":   "Пересчитать",
	"admin_wizard_step1_title": "Проверка тенанта",
	"admin_wizard_step2_title": "Конфиг chronic_load",
	"admin_wizard_step3_title": "Покрытие и base rate",
	"admin_wizard_step4_title": "Phase 0 backfill",
	"admin_wizard_step5_title": "Верификация",
	"admin_wizard_step6_title": "Пересчёт калибровок плашек",
	"admin_wizard_step7_title": "Итоговый предпросмотр (7 дней)",
	"admin_wizard_step6_intro": "После того как пройдёт Шаг 4 и Шаг 5 покажется зелёным — нажми <em>Пересчитать</em>, чтобы получить пороги плашек.",

	// ─── Wizard step 1 (tenant check) ───────────────────────────────
	"wiz_schema":             "Схема",
	"wiz_active_epoch":       "Активная эпоха",
	"wiz_active_epoch_from":  "с",
	"wiz_schema_health":      "Здоровье схемы",
	"wiz_schema_ok":          "✓ ok",
	"wiz_unknown_epoch_warn": "строк помечены sentinel-эпохой <code>unknown</code>. Перед калибровкой нужно разобраться — это значит, что writer'ы свалились на sentinel, что-то не так в таблице <code>source_epochs</code>.",
	"wiz_col_sub_score":      "sub_score",
	"wiz_col_targets":        "targets",
	"wiz_col_eligible":       "eligible",
	"wiz_col_baselines":      "baselines",
	"wiz_col_w_value":        "со значением",
	"wiz_col_features":       "features",
	"wiz_col_latest":         "последняя",
	"wiz_col_target_kind":    "target_kind",
	"wiz_step1_have_rows":    "Строки Phase 0 есть — иди на <strong>Шаг 2</strong>. Если цифры выглядят устаревшими, Шаг 4 (backfill) обновит их.",
	"wiz_step1_no_rows":      "Phase 0 строк у этого тенанта пока нет. Следующее действие — <strong>Шаг 4 backfill</strong>; Шаги 2–3 будут показывать дефолты / пустое покрытие пока не пройдёт Шаг 4.",

	// ─── Wizard step 2 (chronic config) ─────────────────────────────
	"wiz_row_effective":        "effective",
	"wiz_row_defaults":         "defaults",
	"wiz_step2_using_defaults": "Используются дефолты, откалиброванные на тенанте <code>health</code> (PR #97). Если Acute OR base rate из Шага 3 не попадёт в полосу 15–30%, перенастрой <code>min_acute_density</code> через <code>/api/admin/readiness-redesign/config</code> до Шага 4. Иначе дефолты безопасны.",
	"wiz_step2_custom":         "Применены per-tenant override'ы. Перед backfill убедись, что они всё ещё имеют смысл с учётом base rate из Шага 3.",
	"wiz_step2_clamped":        "⚠ В <code>settings</code> попалась строка с непозитивным значением — её зажали к дефолту. Перед продолжением посмотри строки <code>settings</code>.",

	// ─── Wizard step 3 (coverage) ───────────────────────────────────
	"wiz_step3_acute_eligible":   "Eligible-строк Acute (текущая эпоха)",
	"wiz_step3_acute_baserate":   "Acute OR base rate",
	"wiz_step3_chronic_eligible": "Eligible-строк Chronic",
	"wiz_step3_no_rows":          "Eligible Acute OR строк в текущей эпохе пока нет. Шаг 4 backfill их создаст; вернись сюда после него, чтобы прочитать rate.",
	"wiz_step3_in_band":          "Acute OR base rate в полосе 15–30% — дефолты из Шага 2 подходят, перенастраивать не нужно.",
	"wiz_step3_out_band":         "⚠ Acute OR base rate <strong>вне полосы 15–30%</strong>. Стоит перенастроить <code>min_acute_density</code> через <code>POST /api/admin/readiness-redesign/config</code> до Шага 4 — иначе метки <code>chronic_acute_density</code> могут оказаться слишком редкими или слишком частыми и не нести смысла.",

	// ─── Wizard step 4 (plan + run + result) ────────────────────────
	"wiz_step4_tenant":          "Тенант",
	"wiz_step4_from":            "С",
	"wiz_step4_to":              "По",
	"wiz_step4_to_local":        "локальное тенанта",
	"wiz_step4_days":            "Дней",
	"wiz_step4_force":           "Force",
	"wiz_step4_subscores":       "Sub-scores",
	"wiz_step4_subscores_order": "Запускается в порядке зависимостей Recovery → Passive → Acute → Chronic. Идемпотентно по ключам строк — повторный запуск безопасен.",
	"wiz_step4_run_btn":         "Запустить backfill",
	"wiz_step4_run_hint":        "Синхронно — держи вкладку открытой пока не появится таблица результатов. Пока backfill в полёте, кнопка остаётся отключённой.",
	"wiz_step4_progress":        "Бежит Phase 0 backfill — синхронно, обычно 1–2 мин на тенанта. Не закрывай вкладку.",
	"wiz_step4_range":           "Диапазон",
	"wiz_step4_col_written":     "записано",
	"wiz_step4_col_error":       "ошибка",
	"wiz_step4_done_hint":       "Шаг 5 (верификация) и Шаг 7 (предпросмотр) обновились автоматически. Следующее действие — Шаг 6 (пересчёт калибровок плашек).",

	// ─── Wizard step 5 (verify + threshold echo) ────────────────────
	"wiz_step5_unknown_epoch_warn": "строк до сих пор помечены эпохой <code>unknown</code>. Разберись с <code>source_epochs</code>, прежде чем доверять ячейкам плашек в Шаге 7.",
	"wiz_step5_threshold_title":    "Эхо порогов chronic",
	"wiz_step5_threshold_desc":     "Сравнивает пороги, которые chronic-writer проштамповал в <code>data_coverage</code> сэмпл-строки, с эффективной конфигурацией из Шага 2. Расхождение значит, что writer отработал на устаревших настройках — перезапусти Шаг 4.",
	"wiz_step5_threshold_load_err": "Не удалось прочитать эхо:",
	"wiz_step5_threshold_no_rows":  "Eligible chronic-строк пока нет — сначала пройди Шаг 4, потом вернись сюда.",
	"wiz_step5_field":              "поле",
	"wiz_step5_sampled_from":       "сэмпл от",
	"wiz_step5_effective_config":   "effective config",
	"wiz_step5_match":              "✓",
	"wiz_step5_mismatch":           "✗ расхождение",
	"wiz_step5_writer_drift":       "⚠ Writer записал пороги, отличные от текущей конфигурации. Перезапусти Шаг 4, чтобы выровнять chronic-строки, потом вернись сюда.",

	// ─── Wizard step 6 (recompute result) ───────────────────────────
	"wiz_step6_progress":   "Пересчёт калибровок плашек — обычно меньше секунды на тенанта.",
	"wiz_step6_col_status": "статус",
	"wiz_step6_col_cutoff": "порог",
	"wiz_step6_col_p80":    "p80",
	"wiz_step6_col_base":   "base rate",
	"wiz_step6_col_neli":   "n_eligible",
	"wiz_step6_col_npos":   "n_positive",
	"wiz_step6_done_hint":  "Шаг 7 (итоговый предпросмотр) обновился автоматически. Наведи на любую плашку — увидишь её порог и цепочку причин.",

	// ─── Admin: registered users (multi-tenant) ─────────────────────
	"admin_users_title":        "Зарегистрированные пользователи",
	"admin_users_empty":        "Пользователей пока нет.",
	"admin_users_col_username": "Логин",
	"admin_users_col_email":    "Email",
	"admin_users_col_api_key":  "API-ключ",
	"admin_users_col_role":     "Роль",
	"admin_users_role_admin":   "админ",
	"admin_users_role_user":    "пользователь",
	"admin_users_add_title":    "Добавить пользователя",
	"admin_users_username":     "Логин",
	"admin_users_email":        "Email",
	"admin_users_email_hint":   "(необязательно, нужен для SSO)",
	"admin_users_password":     "Пароль",
	"admin_users_add_btn":      "Добавить",
	"admin_users_reveal_key":   "Показать",

	// ─── Admin: top-level group headers ─────────────────────────────
	"admin_group_status":          "Статус и диагностика",
	"admin_group_configuration":   "Конфигурация",
	"admin_group_users":           "Пользователи",
	"admin_tab_admin":             "Админка",
	"admin_tab_general":           "Общие настройки",
	"admin_tab_current_user":      "Текущий пользователь",
	"admin_scope_global":          "Глобально",
	"admin_scope_profiles":        "Профили",
	"admin_profile_diagnostics":   "Диагностика",
	"admin_profile_readiness":     "Readiness",
	"admin_profile_energy":        "EnergyBank",
	"admin_overview_cache_desc":   "Синхронизация и свежесть кэша",
	"admin_overview_gaps_desc":    "Проверить пропуски в данных",
	"admin_overview_quality_desc": "Проверить impossible/suspect точки",
	"admin_overview_checkin_desc": "Покрытие утренних Telegram check-in",
	"admin_user_scope_label":      "Область пользователя",
	"admin_user_scope_desc":       "Действия в этой вкладке затрагивают только tenant-схему этого пользователя.",
	"admin_general_scope_desc":    "Эти настройки влияют на всю установку Health Dashboard.",
	"admin_admin_scope_desc":      "Управление пользователями для всей установки Health Dashboard.",
	"admin_open_and_refresh":      "Открой секцию и нажми «Обновить», чтобы загрузить таблицу.",

	// ─── Admin: read-only quality audit blurb ───────────────────────
	"admin_quality_audit_desc": "Только чтение: показывает точки, которые писатели пометили бы как impossible / suspect. Действия по чистке (Fix / Digest) — в группе <strong>Operations</strong> ниже, чтобы они не были «в одном клике» от read-only кнопки обновления.",

	// ─── Admin: operational-contract preview fragment ───────────────
	"admin_contract_window_label":  "Окно",
	"admin_contract_window_suffix": "локальная TZ тенанта",
	"admin_contract_col_tenant":    "тенант",
	"admin_contract_col_date":      "дата",
	"admin_contract_col_recovery":  "recovery",
	"admin_contract_col_passive":   "passive",
	"admin_contract_col_chronic":   "chronic",
	"admin_contract_col_acute":     "acute",
	"admin_contract_empty":         "пока ничего нет — сначала запусти readiness redesign backfill",

	// ─── Admin: readiness naive-layer monitoring fragment ───────────
	"admin_monitoring_title":                 "Мониторинг naive-layer readiness",
	"admin_monitoring_desc":                  "Read-only проверки §6.4: покрытие target rows, drift classifier-меток, source epochs и доля unknown chip-состояний.",
	"admin_monitoring_empty":                 "пока нет строк мониторинга",
	"admin_monitoring_as_of":                 "на дату",
	"admin_monitoring_col_signal":            "сигнал",
	"admin_monitoring_col_target":            "target",
	"admin_monitoring_col_status":            "статус",
	"admin_monitoring_col_value":             "значение",
	"admin_monitoring_col_reference":         "сравнение",
	"admin_monitoring_signal_coverage":       "coverage",
	"admin_monitoring_signal_drift":          "positive-rate drift",
	"admin_monitoring_signal_unknown":        "unknown rate",
	"admin_monitoring_floor":                 "floor",
	"admin_monitoring_window":                "окно",
	"admin_monitoring_inputs_stable_through": "inputs стабильны до %s",
	"admin_monitoring_inputs_stale_reason":   "%s: %s",
	"admin_monitoring_inputs_stale_by":       "%s: inputs устарели на %d дн., последние стабильные %s",

	// ─── Admin: user-management JS feedback messages ────────────────
	"admin_users_msg_required":      "Логин и пароль обязательны.",
	"admin_users_msg_created":       "Пользователь {username} создан. API-ключ: {apiKey}",
	"admin_users_msg_created_warn":  "Пользователь {username} создан. API-ключ: {apiKey} (предупреждение: {warning})",
	"admin_users_msg_error_generic": "Не удалось создать пользователя.",
}
