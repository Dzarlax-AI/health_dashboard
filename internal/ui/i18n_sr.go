package ui

// translationsSr holds the Serbian UI strings. Keys not present here
// fall back to English via T(). The admin onboarding wizard keys
// added in PR #117 intentionally fall back to English until a
// reviewer with Serbian fluency lands translations.
var translationsSr = map[string]string{
	"app_title":             "Zdravlje",
	"explore":               "Pretraži",
	"loading":               "Učitavanje podataka",
	"readiness":             "Spremnost",
	"recovery":              "Oporavak",
	"readiness_today_label": "Danas",

	"section_cardio_title":      "Srce i pluća",
	"section_cardio_subtitle":   "Puls u miru · HRV · VO2 · disanje",
	"section_activity_title":    "Aktivnost",
	"section_activity_subtitle": "Koraci · kalorije · vežbanje · razdaljina",
	"section_recovery_title":    "Oporavak",
	"section_recovery_subtitle": "San · HRV CV · temperatura zgloba",

	"readiness_trend_label":      "7-dnevni trend",
	"back":                       "Nazad",
	"compare":                    "Uporedi",
	"all_metrics":                "Metrike",
	"nav_settings":               "Podešavanja",
	"your_trends":                "Vaši trendovi",
	"search_placeholder":         "Pretraži metrike...",
	"esc_hint":                   "ESC — zatvori",
	"no_metrics_found":           "Nema metrika",
	"no_data":                    "Nema podataka",
	"no_data_range":              "Nema podataka za ovaj period",
	"no_sleep_data":              "Nema podataka o snu za ovaj period",
	"start_syncing":              "Počnite sinhronizaciju podataka o zdravlju.",
	"data_from":                  "Podaci od ",
	"days_ago":                   "d ranije",
	"this_week":                  "Ova nedelja",
	"activity_vs_recovery":       "Aktivnost i oporavak",
	"activity_recovery_subtitle": "Kako fizičko opterećenje utiče na HRV",
	"activity_load":              "Opterećenje",
	"sleep_section":              "San",
	"sleep_subtitle":             "Prosek za 7 noći",
	"deep_sleep":                 "Duboki san",
	"rem_sleep":                  "REM san",
	"awake_time":                 "Vreme budnosti",
	"efficiency":                 "Efikasnost",
	"bucket":                     "Period",
	"agg":                        "Agr.",
	"auto":                       "Auto",
	"minute":                     "Minut",
	"hour":                       "Sat",
	"day":                        "Dan",
	"preset_all":                 "Sve",
	"avg":                        "Pros.",
	"sum":                        "Zbir",
	"max":                        "Maks",
	"min":                        "Min",
	"previous_period":            "Prethodni period",
	"vs_yesterday":               "vs juče",
	"stable":                     "Stabilno",
	"load_pct":                   "Opterećenje %",
	"hrv_ms":                     "HRV ms",
	"nights":                     "Noći",
	"avg_total":                  "Pros. ukupno",
	"avg_deep":                   "Pros. duboki",
	"avg_rem":                    "Pros. REM",
	"points":                     "Tačke",
	"stale_prefix":               "Podaci od ",
	"stale_suffix":               "d ranije",
	"status_good":                "Odlično",
	"status_fair":                "Treba pažnje",
	"status_low":                 "Čuvajte se",
	"cat_heart":                  "Srce i vitalni znaci",
	"cat_activity":               "Aktivnost",
	"cat_fitness":                "Fitnes",
	"cat_sleep":                  "San",
	"cat_body":                   "Telo",
	"cat_env":                    "Okruženje",
	"cat_nutrition":              "Ishrana",
	"cat_other":                  "Ostalo",
	"phase_deep":                 "Duboki",
	"phase_rem":                  "REM",
	"phase_core":                 "Osnovni",
	"phase_awake":                "Budan",
	"trend_steps":                "Koraci",
	"trend_heart_rate":           "Puls",
	"trend_sleep":                "San",
	"trend_hrv":                  "HRV",
	"trend_readiness":            "Spremnost",
	"ai_insight_title":           "Detaljan AI izveštaj",
	"teps":                       "Koraci",
	"leep":                       "San",
	"ate":                        "Respiratorni ritam",

	// Metric labels.
	"metric_sleep_total":                       "Ukupan san",
	"metric_night_sleep_total":                 "Noćni san",
	"metric_nap_total":                         "Dremke",
	"metric_sleep_deep":                        "Duboki san",
	"metric_sleep_rem":                         "REM san",
	"metric_sleep_core":                        "Osnovni san",
	"metric_sleep_unspecified":                 "San (bez faza)",
	"metric_sleep_awake":                       "Vreme budnosti",
	"chart_sleep_unspecified_hint":             "izvor nije pružio podelu na faze",
	"metric_heart_rate":                        "Puls",
	"metric_resting_heart_rate":                "Puls u miru",
	"metric_walking_heart_rate_average":        "Puls pri hodu",
	"metric_heart_rate_variability":            "HRV",
	"metric_blood_oxygen_saturation":           "Kiseonik u krvi",
	"metric_respiratory_rate":                  "Respiratorni ritam",
	"metric_step_count":                        "Koraci",
	"metric_walking_running_distance":          "Distanca",
	"metric_active_energy":                     "Akt. kalorije",
	"metric_basal_energy_burned":               "Kalorije u miru",
	"metric_apple_exercise_time":               "Vežbanje",
	"metric_apple_stand_time":                  "Vreme stajanja",
	"metric_apple_stand_hour":                  "Sati stajanja",
	"metric_physical_effort":                   "Fizički napor",
	"metric_flights_climbed":                   "Penjanje uz stepenice",
	"metric_stair_speed_up":                    "Brzina na stepenicama",
	"metric_walking_speed":                     "Brzina hoda",
	"metric_walking_step_length":               "Dužina koraka",
	"metric_walking_double_support_percentage": "Dvostrana podrška",
	"metric_walking_asymmetry_percentage":      "Asimetrija hoda",
	"metric_apple_sleeping_wrist_temperature":  "Temp. zgloba",
	"metric_breathing_disturbances":            "Poremećaji disanja",
	"metric_environmental_audio_exposure":      "Izloženost buci",
	"metric_headphone_audio_exposure":          "Glasnoća slušalica",
	"metric_time_in_daylight":                  "Dnevna svetlost",
	"metric_vo2_max":                           "VO2 Maks",
	"metric_six_minute_walking_test_distance":  "6-min hod",
	"metric_readiness":                         "Spremnost",
	"metric_oxygen_saturation":                 "Kiseonik u krvi",
	"metric_heart_rate_variability_sdnn":       "HRV",
	"metric_environmental_audio":               "Buka okoline",
	"metric_headphone_audio":                   "Glasnoća slušalica",
	"metric_walking_double_support":            "Dvostrana podrška",
	"metric_walking_asymmetry":                 "Asimetrija hoda",
	"metric_environmental_sound_reduction":     "Redukcija buke",
	"metric_stair_ascent_speed":                "Brzina penjanja",
	"metric_stair_descent_speed":               "Brzina silaska",
	"metric_wrist_temperature":                 "Temp. zgloba",
	"metric_walking_steadiness":                "Stabilnost hoda",
	"metric_body_mass":                         "Telesna masa",
	"metric_body_mass_index":                   "BMI",
	"metric_body_fat_percentage":               "Procenat masti",
	"metric_lean_body_mass":                    "Mišićna masa",
	"metric_height":                            "Visina",
	"metric_blood_pressure_systolic":           "Sistolni pritisak",
	"metric_blood_pressure_diastolic":          "Dijastolni pritisak",
	"metric_heart_rate_recovery":               "Oporavak pulsa",
	"metric_distance_cycling":                  "Distanca kolesarenja",
	"metric_distance_swimming":                 "Distanca plivanja",
	"metric_swimming_stroke_count":             "Zaveslaji",
	"metric_mindful_minutes":                   "Meditacija",
	"metric_alcoholic_beverages":               "Alkohol",
	"metric_six_min_walk_distance":             "6-min hod",
	"metric_dietary_energy":                    "Kalorije (ishrana)",
	"metric_dietary_protein":                   "Proteini",
	"metric_dietary_carbs":                     "Ugljeni hidrati",
	"metric_dietary_fat":                       "Ukupne masti",
	"metric_dietary_fat_saturated":             "Zasićene masti",
	"metric_dietary_fat_monounsaturated":       "Mononezasićene masti",
	"metric_dietary_fat_polyunsaturated":       "Polinezasićene masti",
	"metric_dietary_water":                     "Voda",
	"metric_dietary_sodium":                    "Natrijum",
	"metric_dietary_sugar":                     "Šećer",
	"metric_dietary_fiber":                     "Vlakna",
	"metric_dietary_caffeine":                  "Kofein",
	"metric_dietary_calcium":                   "Kalcijum",
	"metric_dietary_iron":                      "Gvožđe",
	"metric_dietary_cholesterol":               "Holesterol",
	"metric_dietary_potassium":                 "Kalijum",
	"metric_dietary_magnesium":                 "Magnezijum",
	"metric_dietary_phosphorus":                "Fosfor",
	"metric_dietary_zinc":                      "Cink",
	"metric_dietary_copper":                    "Bakar",
	"metric_dietary_manganese":                 "Mangan",
	"metric_dietary_selenium":                  "Selen",
	"metric_dietary_iodine":                    "Jod",
	"metric_dietary_molybdenum":                "Molibden",
	"metric_dietary_folate":                    "Folat",
	"metric_dietary_biotin":                    "Biotin (B7)",
	"metric_dietary_vitamin_a":                 "Vitamin A",
	"metric_dietary_vitamin_c":                 "Vitamin C",
	"metric_dietary_vitamin_d":                 "Vitamin D",
	"metric_dietary_vitamin_e":                 "Vitamin E",
	"metric_dietary_vitamin_k":                 "Vitamin K",
	"metric_dietary_vitamin_b6":                "Vitamin B6",
	"metric_dietary_vitamin_b12":               "Vitamin B12",
	"metric_dietary_niacin":                    "Niacin (B3)",
	"metric_dietary_riboflavin":                "Riboflavin (B2)",
	"metric_dietary_thiamin":                   "Tijamin (B1)",
	"metric_dietary_pantothenic_acid":          "Pantotenska kis. (B5)",
	"by_source":                                "Izvori",
	"source_comparison":                        "Poređenje izvora",
	"lbl_total":                                "Ukupno",
	"lbl_deep":                                 "Duboki",
	"lbl_core":                                 "Osnovni",
	"how_it_works":                             "Kako to radi",
	"health_sections":                          "Pregled zdravlja",
	"at_a_glance":                              "Danas",

	"trend_vs_7d":  "vs 7d",
	"trend_vs_30d": "vs 30d",

	"energy_label":                      "Energetski bank",
	"energy_capacity_label":             "Današnji kapacitet",
	"energy_current_label":              "Dostupno sada",
	"energy_drain_label":                "Potrošeno",
	"energy_verdict_push_hard":          "Slobodno tvrdo",
	"energy_verdict_moderate":           "Umeren dan",
	"energy_verdict_active_recovery":    "Samo aktivni oporavak",
	"energy_verdict_rest":               "Dan odmora",
	"energy_component_morning_capacity": "Jutarnji kapacitet",
	"energy_component_activity_load":    "Opterećenje (danas vs 28d hronični)",
	"energy_component_autonomic_stress": "Autonomni stres (RHR / HRV)",
	"energy_state_good_title":           "Napunjen",
	"energy_state_good_desc":            "Rezerve su pune — danas slobodno guraj jače.",
	"energy_state_medium_title":         "U balansu",
	"energy_state_medium_desc":          "Pristojne rezerve — treniraj sa namerom, ali pazi na obim.",
	"energy_state_low_title":            "Rezerve niske",
	"energy_state_low_desc":             "Rezerva je tanka — lakši napor i fokus na oporavak.",
	"energy_state_critical_title":       "Ispražnjen",
	"energy_state_critical_desc":        "Rezervoar gotovo prazan — odmor je danas najbolji izbor.",
	"details":                           "Detalji",

	// Subjective morning check-in confirmation on the dashboard hero.
	"checkin_today_label":  "Vaš jutarnji odgovor:",
	"checkin_answer_great": "Odlično",
	"checkin_answer_ok":    "Normalno",
	"checkin_answer_meh":   "Onako",
	"checkin_answer_sick":  "Bolestan(a)",

	// Methodology status badges (see i18n_en.go for the full mapping).
	"methodology_status_heuristic_personalized":           "Heuristika",
	"methodology_status_heuristic_personalized_desc":      "Ekspertska formula na ličnim baseline-ima. Nije validiran prediktivni model.",
	"methodology_status_heuristic_prescriptive":           "Heuristika",
	"methodology_status_heuristic_prescriptive_desc":      "Pravila preko heurističkog capacity/drain. Još nije validirano protiv subjektivnog stanja.",
	"methodology_status_experimental_formula":             "Eksperimentalno",
	"methodology_status_experimental_formula_desc":        "Formula u proveri. Još nije production sloj odlučivanja.",
	"methodology_status_validated_floor_candidate":        "Proverena osnova",
	"methodology_status_validated_floor_candidate_desc":   "Ciljni pokazatelj bez curenja podataka, sa proverenom osnovnom linijom (EWMA45). Production infrastruktura; nema naučenog modela iznad.",
	"methodology_status_labeling_framework_ready":         "Spremne oznake",
	"methodology_status_labeling_framework_ready_desc":    "Označeni događaji bez curenja podataka, spremni za buduće modele. Sama oznaka je rezultat.",
	"methodology_status_experimental_not_production":      "Eksperimentalno",
	"methodology_status_experimental_not_production_desc": "U proveri. Ne tretiraj kao validiran score.",

	"admin_notify_title":               "Telegram izveštaji",
	"admin_notify_morning_title":       "Jutarnji izveštaj",
	"admin_notify_morning_desc":        "Pošalji test san sada.",
	"admin_notify_evening_title":       "Večernji izveštaj",
	"admin_notify_evening_desc":        "Pošalji test pregled dana sada.",
	"admin_notify_token":               "Token bota",
	"admin_notify_chat_id":             "Chat ID",
	"admin_notify_webhook_label":       "Webhook",
	"admin_notify_webhook_retry":       "Pokušaj ponovo",
	"webhook_badge_ok":                 "✓ registrovan",
	"webhook_badge_pending":            "⏳ registruje se",
	"webhook_badge_failed":             "✗ greška",
	"webhook_badge_deleted":            "— obrisan",
	"webhook_badge_unknown":            "— nepoznato",
	"admin_notify_lang":                "Jezik",
	"admin_notify_timezone":            "Vremenska zona",
	"admin_notify_timezone_hint":       "Potrebna za dnevne izveštaje i izračunavanje istorijskog EnergyBank-a. IANA format (npr. Europe/Belgrade).",
	"admin_energy_backfill_title":      "Istorijski EnergyBank",
	"admin_energy_backfill_desc":       "Izračunaj retrospektivne EnergyBank snapshot-ove iz uvezene Apple Health istorije. Bez ovoga personalna kalibracija radi na default pragovima umesto na tvojoj realnoj distribuciji.",
	"admin_energy_backfill_loading":    "Učitavanje…",
	"admin_energy_backfill_load_error": "Greška pri učitavanju statusa.",
	"admin_energy_backfill_summary":    "{complete} kompletnih dana · {backfilled} snapshot-ova već backfilled · opseg: {earliest} → {to}",
	"admin_energy_backfill_run":        "Izračunaj istorijski EnergyBank",
	"admin_energy_backfill_running":    "Backfill je već u toku.",
	"admin_energy_backfill_need_tz":    "Prvo podesi vremensku zonu iznad.",
	"admin_energy_backfill_no_data":    "Nema kompletnih dana — uvezi Apple Health ili sačekaj live ingest.",
	"admin_energy_backfill_confirm":    "Ovo će preračunati EnergyBank za svaki istorijski dan. Nastaviti?",
	"admin_energy_backfill_starting":   "Pokretanje…",
	"admin_energy_backfill_progress":   "Dan {done}/{total} · ok={ok}",
	"admin_energy_backfill_done":       "Gotovo: {ok} upisano · {skipped} preskočeno · {errors} grešaka",
	"admin_notify_schedule_morning":    "Sat jutarnjeg izveštaja",
	"admin_notify_schedule_evening":    "Sat večernjeg izveštaja",
	"admin_notify_weekday":             "Radni dani",
	"admin_notify_weekend":             "Vikend",
	"admin_notify_save":                "Sačuvaj",
	"admin_notify_saved":               "Postavke sačuvane",
	"admin_notify_send":                "Pošalji test",
	"admin_notify_test_morning":        "Test jutro",
	"admin_notify_test_evening":        "Test veče",
	"admin_target_user":                "Ciljni korisnik",
	"admin_target_user_current":        "— trenutni korisnik —",
	"admin_gaps_section_title":         "Integritet podataka",
	"admin_gaps_check":                 "Proveri praznine",
	"admin_quality_title":              "Provera kvaliteta podataka",
	"admin_quality_run":                "Pokreni audit",
	"admin_quality_fix":                "Označi sumnjive + očisti nemoguće",
	"admin_quality_digest":             "Pošalji digest sada",
	"admin_quality_clean":              "Sve čisto — nema anomalija.",
	"admin_quality_total":              "Ukupno redova",
	"admin_quality_bad":                "Van opsega",
	"admin_quality_range":              "Opseg",
	"admin_quality_metric":             "Metrika",
	"admin_quality_sample":             "Primer",
	"admin_quality_week":               "Za 7 dana",
	"admin_quality_impossible":         "Nemoguće",
	"admin_quality_suspect":            "Sumnjivo",
	"admin_quality_missed":             "Propuštene noći",
	"admin_quality_fixed":              "Označeno: nemoguće %d, sumnjive %d.",
	"admin_checkin_coverage_title":     "Pokrivenost jutarnog check-ina",
	"admin_checkin_coverage_desc":      "Samo za čitanje: poslednja jutarnja Telegram pitanja i latencija odgovora.",
	"admin_checkin_total":              "Dana",
	"admin_checkin_prompted_coverage":  "Prompt poslat",
	"admin_checkin_answered_coverage":  "Odgovoreno",
	"admin_checkin_avg_response":       "Prosečan odgovor",
	"admin_checkin_no_response":        "Nema odgovora",
	"admin_checkin_answers":            "Odgovori:",
	"admin_checkin_date":               "Datum",
	"admin_checkin_status":             "Status",
	"admin_checkin_answer":             "Odgovor",
	"admin_checkin_latency":            "Latencija",
	"admin_checkin_status_prompted":    "Čeka",
	"admin_checkin_status_answered":    "Odgovoreno",
	"admin_checkin_status_late":        "Kasno",
	"admin_checkin_status_expired":     "Isteklo",
	"admin_checkin_status_missing":     "Nema reda",
	"admin_ai_title":                   "AI jutarnji izveštaj",
	"admin_ai_key":                     "Gemini API ključ",
	"admin_ai_model":                   "Model",
	"admin_ai_max_tokens":              "Maks. tokena",
	"admin_ai_save":                    "Sačuvaj",
	"admin_ai_saved":                   "Postavke sačuvane",
	"admin_energy_title":               "EnergyBank v2.2 — stres drain",
	"admin_energy_warning":             "Non-production placeholder vrednosti dok §4.5 validacija ne vrati 'validated'. Effective β je 0 dok je stres drain isključen, bez obzira na β ispod.",
	"admin_energy_stress_enabled":      "Stres drain uključen",
	"admin_energy_beta":                "β (koef. stres draina)",
	"admin_energy_z_threshold":         "z-score prag",
	"admin_energy_effective_beta":      "Effective β (uživo)",
	"admin_energy_save":                "Sačuvaj",
	"admin_energy_saved":               "Postavke sačuvane",
	"admin_help_summary":               "Šta je ovo i kada menjati?",
	"admin_energy_help_html": `<h4 style="margin:8px 0 4px">β — koeficijent stres-drain</h4>
<p>Množilac koji pretvara <code>sustained_hr_load_z</code> u drain poene. Formula: <code>drain = α·active_kcal + <strong>β</strong>·load_z</code>.</p>
<p><strong>Default 0.8</strong> (placeholder iz §6 Q3). Kada validation rubric ispod vrati <code>validated</code> — podesi u opsegu 0.4–1.2, u zavisnosti od toga koliko agresivno želiš da bar reaguje na trajno autonomno opterećenje.</p>
<h4 style="margin:12px 0 4px">z-threshold — prag po satu</h4>
<p>U load integral idu samo sati gde je HR viši od dnevnog baseline-a za više od ovog broja SD.</p>
<p><strong>Default 0.5.</strong> Podigni na 0.7–0.8 ako imaš sedentarni stil i HR sistematski stoji na z≈0.5 bez stvarnog stresa (mental activation ≠ autonomno opterećenje → false positives).</p>
<h4 style="margin:12px 0 4px">Stress drain enabled — master prekidač</h4>
<p>Kada je ISKLJUČEN (default), <code>sustained_hr_load_z</code> i dalje računa i piše se u <code>components</code> JSONB za audit, ali ne ulazi u bank — β_effective = 0.</p>
<p><strong>Uključi SAMO kada validation rubric ispod vrati <code>validated</code>.</strong> Uključiš ranije — drain će reagovati na šum.</p>
<h4 style="margin:12px 0 4px">Workflow</h4>
<ol style="margin:4px 0 0 18px;padding:0">
  <li>Pokreni validation rubric (dugme ispod).</li>
  <li>Verdict ≠ validated → β=0, ne diraj ništa. Monthly Telegram nudge će stići sam kada verdict pređe.</li>
  <li>Verdict = validated → uključi Stress drain enabled, β=0.8.</li>
  <li>Nakon ~2 nedelje — ako je drain previše agresivan, smanji β na 0.4–0.5. Pokreni rubric mesečno.</li>
</ol>`,
	"admin_stress_validation_help_html": `<h4 style="margin:8px 0 4px">Šta meri rubric</h4>
<p>Pearson r na rotirajućem 30-dnevnom prozoru između <code>sustained_hr_load[d]</code> i tri nezavisna next-morning signala oporavka. Logika: ako formula stvarno hvata stres — visok load danas predviđa degradaciju autonomnih markera ujutru.</p>
<h4 style="margin:12px 0 4px">Kanali</h4>
<dl style="margin:4px 0">
  <dt><strong>Kanal 1 — jutarnji HRV (primary, anchor)</strong></dt>
  <dd>Korelacija load[d] sa overnight HRV[d+1]. Očekivani znak: <strong>negativan</strong> (high load → low HRV).</dd>
  <dt style="margin-top:6px"><strong>Kanal 2 — pomeraj jutarnjeg RHR (secondary)</strong></dt>
  <dd>load[d] vs (overnight RHR[d+1] − 30d baseline). Očekivani znak: <strong>pozitivan</strong>. Cross-check.</dd>
  <dt style="margin-top:6px"><strong>Kanal 3 — arhitektura sna (tertiary)</strong></dt>
  <dd>Glasanje 3 sub-korelacije: sleep_awake (+), onset latency (+), deep% (−). Sub-signal glasa ako |r|≥0.10 u očekivanom smeru; kanal "se slaže" sa ≥2 glasa.</dd>
</dl>
<h4 style="margin:12px 0 4px">Verdikti</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li><strong style="color:var(--good)">validated</strong> — r ≤ −0.30 na kanalu 1 I bar jedan cross-channel se slaže. Možeš uključiti <code>Stress drain enabled</code>.</li>
  <li><strong style="color:var(--fair)">weak</strong> — −0.30 &lt; r &lt; −0.10. β=0. Recheck za mesec.</li>
  <li><strong style="color:var(--low)">inconclusive</strong> — |r| &lt; 0.10 na kanalu 1, ili manje od 15 HRV uzoraka (sparsity fallback). Treba više noćnog HRV-a (Breathe sesije na Apple Watch-u pomažu).</li>
  <li><strong style="color:var(--low)">wrong_direction</strong> — r &gt; 0. Formula ne hvata fiziologiju ovog korisnika — manual review.</li>
</ul>
<h4 style="margin:12px 0 4px">Kada pokrenuti</h4>
<ul style="margin:4px 0 0 18px;padding:0">
  <li>Posle major data ingest-a (Apple Health uvoz, obnova sna).</li>
  <li>Jednom mesečno kao routine. Monthly Telegram nudge dolazi sam kada verdict pređe u <strong>validated</strong>.</li>
  <li>Pre nego što uključiš <code>Stress drain enabled</code> iznad.</li>
</ul>`,
	"admin_stress_validation_title":       "Validacija stres-formule (§4.5)",
	"admin_stress_validation_desc":        "Pearson r na rotirajućem 30-dnevnom prozoru: jutarnji HRV (osnovni), pomeraj jutarnjeg RHR, arhitektura sna. Samo čitanje — NE prebacuje stress_drain_enabled automatski. Operator odlučuje nakon pregleda verdikta.",
	"admin_stress_validation_run":         "Pokreni",
	"admin_stress_validation_loading":     "Računam rubric na 30-dnevnom prozoru…",
	"admin_stress_validation_sparse":      "malo podataka",
	"admin_stress_validation_no_data":     "nema podataka",
	"admin_stress_validation_flags_label": "oznake:",
	"admin_stress_validation_ch1":         "Kanal 1 (jutarnji HRV)",
	"admin_stress_validation_ch2":         "Kanal 2 (pomeraj jutarnjeg RHR)",
	"admin_stress_validation_ch3":         "Kanal 3 (arhitektura sna)",
	"admin_stress_validation_votes":       "glasovi",
	"admin_stress_validation_window_fmt":  "prozor {window} dana, {days} dana sa podacima",

	"admin_import_title":     "Uvoz Apple Health",
	"admin_import_desc":      "Otpremite export.zip za uvoz istorijskih podataka. Duplikati se automatski preskaču.",
	"admin_import_choose":    "Izaberi fajl…",
	"admin_import_batch":     "zapisa/batch",
	"admin_import_pause":     "ms pauza",
	"admin_import_start":     "Pokreni uvoz",
	"admin_import_uploading": "Otpremanje…",
	"admin_import_running":   "Uvoz u toku…",

	"admin_tab_general":        "Opšta podešavanja",
	"admin_tab_admin":          "Admin",
	"admin_tab_current_user":   "Trenutni korisnik",
	"admin_user_scope_label":   "Opseg korisnika",
	"admin_user_scope_desc":    "Radnje u ovoj kartici utiču samo na tenant šemu ovog korisnika.",
	"admin_general_scope_desc": "Ova podešavanja utiču na celu Health Dashboard instalaciju.",
	"admin_admin_scope_desc":   "Upravljanje korisnicima za celu Health Dashboard instalaciju.",
	"admin_open_and_refresh":   "Otvori ovu sekciju i klikni Osveži da učitaš tabelu.",
	"admin_users_reveal_key":   "Prikaži",

	"admin_scope_global":          "Globalno",
	"admin_scope_profiles":        "Profili",
	"admin_profile_diagnostics":   "Dijagnostika",
	"admin_profile_readiness":     "Readiness",
	"admin_profile_energy":        "EnergyBank",
	"admin_overview_cache_desc":   "Sinhronizacija i svežina keša",
	"admin_overview_gaps_desc":    "Proveri dane koji nedostaju u ingestu",
	"admin_overview_quality_desc": "Pregled impossible/suspect tačaka",
	"admin_overview_checkin_desc": "Pokrivenost jutarnjih Telegram check-in pitanja",

	"admin_contract_window_label":  "Prozor",
	"admin_contract_window_suffix": "lokalna TZ tenanta",
	"admin_contract_col_tenant":    "tenant",
	"admin_contract_col_date":      "datum",
	"admin_contract_col_recovery":  "recovery",
	"admin_contract_col_passive":   "passive",
	"admin_contract_col_chronic":   "chronic",
	"admin_contract_col_acute":     "acute",
	"admin_contract_empty":         "još nema redova — prvo pokrenite readiness redesign backfill",

	"admin_monitoring_title":                 "Monitoring readiness naive sloja",
	"admin_monitoring_desc":                  "Read-only §6.4 provere: pokrivenost target redova, drift classifier oznaka, source epochs i udeo unknown chip stanja.",
	"admin_monitoring_empty":                 "još nema redova monitoringa",
	"admin_monitoring_as_of":                 "na datum",
	"admin_monitoring_col_signal":            "signal",
	"admin_monitoring_col_target":            "target",
	"admin_monitoring_col_status":            "status",
	"admin_monitoring_col_value":             "vrednost",
	"admin_monitoring_col_reference":         "poređenje",
	"admin_monitoring_signal_coverage":       "coverage",
	"admin_monitoring_signal_drift":          "positive-rate drift",
	"admin_monitoring_signal_unknown":        "unknown rate",
	"admin_monitoring_floor":                 "floor",
	"admin_monitoring_window":                "prozor",
	"admin_monitoring_inputs_stable_through": "inputi stabilni do %s",
	"admin_monitoring_inputs_stale_reason":   "%s: %s",
	"admin_monitoring_inputs_stale_by":       "%s: inputi kasne %d dana, poslednji stabilan datum %s",

	"stress_flags_aria":              "Signali stresa",
	"stress_detail_what":             "Šta je",
	"stress_detail_cause":            "Šta je izazvalo",
	"stress_detail_risk":             "Zašto je važno",
	"stress_detail_action":           "Šta raditi",
	"stress_flag_acute_stress_label": "Skok pulsa",
	"stress_flag_acute_stress_detail_html": `<h5>Šta je</h5>
<p>Vaš puls je naglo skočio iznad vaše norme jedan sat danas.</p>
<h5>Šta je izazvalo</h5>
<p>Kafa, neočekivan poziv, nervi pred sastanak, iznenađenje, kratki konflikt — bilo koja kratka aktivacija simpatičkog nervnog sistema.</p>
<h5>Zašto je važno</h5>
<p>Samo po sebi — ničim. Izolovani skok ne opterećuje sistem, to je normalna reakcija organizma.</p>
<h5>Šta raditi</h5>
<p>Ništa. Oznaka služi za dijagnostiku — da se vidi da je bio jednokratan događaj, ne hronični stres.</p>`,
	"stress_flag_sustained_load_label": "Dug rast pulsa",
	"stress_flag_sustained_load_detail_html": `<h5>Šta je</h5>
<p>Vaš puls je stajao iznad vaše norme bar 4 sata zaredom danas.</p>
<h5>Šta je izazvalo</h5>
<p>Dug stresan dan (deadline, pregovori), težak trening sa sporim oporavkom, početak bolesti, dehidracija, nedovoljno sna.</p>
<h5>Zašto je važno</h5>
<p>Stvarno opterećenje za autonomni nervni sistem. Više takvih dana zaredom — dug energije se gomila, oporavak usporava. EnergyBank je već uračunao ovo: bar se danas više potrošio nego obično.</p>
<h5>Šta raditi</h5>
<p>Veče: rano u krevet, bez alkohola i kofeina posle ručka. Sutra: laka aktivnost, ne ponavljati opterećenje.</p>`,
	"stress_flag_illness_signature_label": "Možda bolest",
	"stress_flag_illness_signature_detail_html": `<h5>Šta je</h5>
<p>Telesna temperatura, frekvencija disanja i HRV — sve tri su se pomerile od vaše norme u "bolesnom" smeru istovremeno.</p>
<h5>Šta je izazvalo</h5>
<p>Najčešće virusna infekcija u ranoj fazi. Ređe — ozbiljan pretrening sa već oslabljenim imunitetom.</p>
<h5>Zašto je važno</h5>
<p>Težak trening na ovoj pozadini obično odugovlači prehladu za dodatnih 3-5 dana. Imunitet radi na odbijanju infekcije; opterećivati ga sportom = dvostruko opterećenje za organizam.</p>
<h5>Šta raditi</h5>
<p>Danas: spavanje. Trening otkazati ili maksimalno olakšati (spora šetnja). Voda, topla hrana. Sutra proveriti stanje: ako je oznaka nestala, postepeno se vraćati u plan.</p>`,
	"stress_flag_recovery_debt_label": "Niste se oporavili",
	"stress_flag_recovery_debt_detail_html": `<h5>Šta je</h5>
<p>Tokom noći HRV je pao ispod vaše norme, a puls u mirovanju je porastao. San nije skinuo jučerašnje opterećenje.</p>
<h5>Šta je izazvalo</h5>
<p>Jučerašnji intenzivan trening ili radni dan, kasan obrok, alkohol, kasno spavanje, emocionalni stres.</p>
<h5>Zašto je važno</h5>
<p>Danas niste u optimalnoj formi. Ako ignorišete i ipak idete na težak trening — raste rizik od pretreniranosti i šanse za sitnu povredu.</p>
<h5>Šta raditi</h5>
<p>Lak dan: šetnja, mobilnost, joga. Dobro pojesti, rano u krevet. Sutra oznaka treba da nestane — ako ne, dva laka dana zaredom su bolja od ozbiljnog pretreninga.</p>`,
	"stress_flag_parasympathetic_rebound_label": "Oporavak",
	"stress_flag_parasympathetic_rebound_detail_html": `<h5>Šta je</h5>
<p>Puls je bio povišen, ali je HRV takođe bio iznad norme. Parasimpatički nervni sistem je aktivan — to je režim oporavka, ne stres.</p>
<h5>Šta je izazvalo</h5>
<p>Obično posle teškog treninga ili intenzivnog dana. Telo je potrošilo resurs i sad ga aktivno obnavlja.</p>
<h5>Zašto je važno</h5>
<p>Ničim lošim — to je zdrava reakcija. <strong>Ne brkati sa akutnim stresom</strong>: spolja puls izgleda visok, ali je fiziologija sasvim drugačija.</p>
<h5>Šta raditi</h5>
<p>Dati telu vreme: lak dan, normalan san. Trening je u redu, samo ne do krajnjih granica.</p>`,
	"stress_flag_stale_stress_label": "Praznina u podacima",
	"stress_flag_stale_stress_detail_html": `<h5>Šta je</h5>
<p>Dan je završen, a u budnom prozoru je prikupljeno manje od 8 sati podataka o pulsu. Procena trajnog opterećenja srca se za ovaj dan ne računa.</p>
<h5>Šta se dogodilo</h5>
<p>Najverovatnije je sat dugo stajao van ruke ili je bila pauza u sinhronizaciji sa iPhone-om.</p>
<h5>Šta raditi</h5>
<p>Stabilnije nositi sat i proveriti sinhronizaciju. Današnji brojevi se neće preračunati — prošle dane ne prepisujemo.</p>`,
	"stress_flag_data_accruing_label": "Skupljamo podatke",
	"stress_flag_data_accruing_detail_html": `<h5>Šta je</h5>
<p>Dan je još u toku. Za procenu trajnog opterećenja srca potrebno je najmanje 8 sati podataka o pulsu u budnom prozoru, a još nije skupljeno dovoljno sati.</p>
<h5>Zašto je tako</h5>
<p>Score odgovara na pitanje "da li je puls bio povišen tokom realnog dela dana?". Taj odgovor ima smisla tek kada je sam dan već prošao realan deo. Prikazivati broj ujutru bilo bi obmanjujuće.</p>
<h5>Šta raditi</h5>
<p>Ništa — samo nositi sat tokom uobičajenog dana. Oznaka nestaje automatski čim se skupi dovoljno sati.</p>`,
	"stress_flag_calibration_warmup_label": "Kalibracija",
	"stress_flag_calibration_warmup_detail_html": `<h5>Šta je</h5>
<p>Lična norma (HRV, puls, disanje) se još uči — treba oko nedelju dana neprekidnih podataka. Pragovi oznaka stresa za sada ostaju konzervativni.</p>
<h5>Šta raditi</h5>
<p>Samo nosite sat. Nakon nekoliko dana oznaka nestaje sama.</p>`,
}
