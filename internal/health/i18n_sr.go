package health

var sr = LangStrings{
	"readiness_optimal": "Optimalno",
	"readiness_fair":    "Umjereno",
	"readiness_low":     "Niska",
	"tip_optimal":       "Odličan dan za naporan trening ili važne zadatke.",
	"tip_fair":          "Malo odstupanje od vaše norme. Umjerena aktivnost je dobar izbor.",
	"tip_low":           "Fokusirajte se na oporavak: hidratacija, odmor i izbjegavanje intenzivnog vježbanja.",

	// Per-section status labels (BriefingSection.Status) — surfaced via
	// EnrichLabels so iOS / web consumers don't maintain a parallel i18n
	// table for the good/fair/low enum.
	"section_status_good": "Dobro",
	"section_status_fair": "Srednje",
	"section_status_low":  "Nisko",

	"sec_recovery": "Oporavak",
	"sec_sleep":    "San",
	"sec_activity": "Aktivnost",
	"sec_cardio":   "Srce i pluća",

	// AI block headers — see i18n_en.go for the rationale on keeping
	// these independent of the sec_* section names.
	"ai_block_sleep_header":          "San",
	"ai_block_yesterday_header":      "Juče",
	"ai_block_recovery_header":       "Oporavak",
	"ai_block_recommendation_header": "Plan za danas",

	"lbl_hrv":        "HRV",
	"lbl_vs_avg":     "vs avg",
	"lbl_resting_hr": "Puls u miru",
	"lbl_duration":   "Trajanje",
	"lbl_deep_sleep": "Duboki san",
	"lbl_rem":        "REM",
	"lbl_nap_badge":  "+%dm dremke",
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

	"rec_summary_good":        "Dobro ste se oporavili. Telo je spremno za aktivnost.",
	"rec_summary_fair":        "Oporavak je umjeren. Slušajte svoje telo danas.",
	"rec_summary_low":         "Telu je potrebno više odmora. Ne preopterećujte se.",
	"rec_summary_fair_stress": "HRV/RHR pojedinačno izgledaju u redu, ali drugi markeri ukazuju na nakupljeni stres — vidi naslov iznad.",

	"sleep_dur_stable": "u skladu s vašim obrascem",
	"sleep_dur_more":   "više nego obično — odlično",
	"sleep_dur_less":   "manje nego obično",

	"sleep_deep_good": "dobar omjer za restorativni san",
	"sleep_deep_low":  "ispod idealnih 15%+ — kvalitet može patiti",

	"sleep_rem_good": "zdrav opseg za pamćenje i učenje",
	"sleep_rem_low":  "malo nisko — REM pomaže konsolidaciji pamćenja",

	// Sleep regularity detail
	"lbl_sleep_regularity": "Konzistentnost",
	"sleep_reg_regular":    "veoma dosljedan raspored — jak signal dugovječnosti",
	"sleep_reg_moderate":   "malo varijabilnosti — pokušajte zadržati fiksno vrijeme spavanja",
	"sleep_reg_irregular":  "visoka varijabilnost — neredovit san povećava zdravstvene rizike",

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

	"unit_steps_day": "%s/dan",
	"unit_min_day":   "%s min/dan",
	"unit_hrs_night": "%.1f h/noć",
	"unit_pct_total": "%.0f%% od ukupnog",

	"insight_steps_good":    "Dosegli ste prosječan broj koraka u %d od poslednjih 7 dana. Odlična konzistentnost!",
	"insight_steps_low":     "Samo %d od 7 dana iznad prosječnih koraka. Pokušajte se kretati konzistentnije.",
	"insight_hrv_drop":      "Vaš HRV ima tendenciju pada nakon dana visoke aktivnosti. Obavezno planirajte oporavak.",
	"insight_hrv_resilient": "Vaš HRV ostaje otporan nakon aktivnih dana — vaš oporavak je solidan.",
	"insight_sleep_active":  "Spavate %.1f sati na aktivne dane vs %.1f sati na dane odmora — aktivnost pomaže vašem snu.",
	"insight_sleep_rest":    "Bolje spavate na dane odmora (%.1f h vs %.1f h). Večerna aktivnost možda utiče na san.",
	"insight_overtrain":     "Vaša aktivnost je visoka unatoč znakovima iscrpljenosti. Rizik od pretreniranosti je povišen.",

	// Alerts
	"alert_rr_anomaly":         "Respiratorni ritam značajno odstupa od vaše norme. To može biti rani znak bolesti ili stresa.",
	"alert_wrist_temp_anomaly": "Temperatura zgloba značajno odstupa od vaše norme. Mogući uzroci: groznica, upala ili hormonske promjene.",
	"alert_hrv_cv_high":        "Varijabilnost HRV-a za 7 dana je povišena (CV %.0f%%), što ukazuje na nekonzistentan oporavak. Provjerite kvalitetu sna i nivo stresa.",

	// Headline (cross-metric signal of the day)
	"headline_stress_title":         "Nakupljeni stres oporavka",
	"headline_stress_detail":        "Više markera istovremeno ukazuje na opterećenje: %s. Preporučuje se smanjenje intenziteta.",
	"headline_part_rhr":             "RHR %.0f bpm (+%.0f od norme)",
	"headline_part_hrv":             "HRV %.0f ms (z=%.1f)",
	"headline_part_sleep":           "san %.1fh",
	"headline_part_awake":           "buđenja %.1fh",
	"headline_sleep_debt_title":     "Manjak sna",
	"headline_sleep_debt_detail":    "%.1fh je ispod ciljne 7h. Jedna kratka noć je ok; pratite da ne postane obrazac.",
	"headline_stable_title":         "Sve je u normi",
	"headline_stable_detail":        "Svi ključni pokazatelji blizu vaše lične baseline.",
	"headline_dev_heart_rate_variability_above_baseline": "HRV iznad norme",
	"headline_dev_heart_rate_variability_below_baseline": "HRV ispod norme",
	"headline_dev_resting_heart_rate_above_baseline":     "Puls u mirovanju povišen",
	"headline_dev_resting_heart_rate_below_baseline":     "Puls u mirovanju ispod norme",
	"headline_dev_sleep_total_above_baseline":            "Spavali više nego obično",
	"headline_dev_sleep_total_below_baseline":            "Spavali manje nego obično",
	"headline_dev_generic":                               "Značajno odstupanje od baseline",
	"headline_dev_detail":                                "%.1f%s — %+.1f%% od vašeg proseka %.1f%s.",

	// Energy Bank (action prescription)
	"energy_label":          "Energetski bank",
	"energy_hourly_label":   "Poslednja 72 sata",
	"energy_capacity_label": "Današnji kapacitet",
	"energy_current_label":  "Dostupno sada",
	"energy_drain_label":    "Potrošeno",
	"trend_vs_7d":           "vs 7d",
	"trend_vs_30d":          "vs 30d",

	"energy_verdict_push_hard":       "Slobodno tvrdo",
	"energy_verdict_moderate":        "Umeren dan",
	"energy_verdict_active_recovery": "Samo aktivni oporavak",
	"energy_verdict_rest":            "Dan odmora",

	"energy_reason_full_capacity": "Rezerva puna, HRV iznad ili na normi — zeleno svetlo za tvrdu sesiju.",
	"energy_reason_optimal":       "Rezerva solidna, markeri stresa su čisti — normalan trening dan je ok.",
	"energy_reason_low_capacity":  "Rezerva niska nakon današnjeg opterećenja — držite intenzitet lakim.",
	"energy_reason_high_stress":   "HRV %.1f SD od baseline, stres indeks %d — autonomna opterećenja su povišena.",
	"energy_reason_acwr_spike":    "Današnje opterećenje je već %.0f%% od 28-dnevne norme — tvrda sesija bi gurnula spike dalje.",

	// v2.2 stress-flag verdict overrides — STRESS_MEASUREMENT.md §4.3.
	"energy_reason_illness_signature": "Temperatura, frekvencija disanja i HRV — sve tri govore da telo bori infekciju. Danas je odmor pravi izbor.",
	"energy_reason_recovery_debt":     "Jučerašnje opterećenje stiglo je noću (HRV ↓, RHR ↑) — držite danas lakim da se vrati dug pre nego što opet pritisnete.",
	"energy_reason_rebound_addon":     "Napomena: HR je bio povišen, ali je HRV iznad norme — to je obrazac faze oporavka, ne akutni stres.",

	// v2.2 hero-row stress-flag chips.
	"stress_flags_aria":                       "Oznake stres signala",
	"stress_flag_illness_signature_label":     "Znaci bolesti",
	"stress_flag_illness_signature_desc":      "Temperatura, frekvencija disanja i HRV — sve tri u smeru bolesti. Odmor je u skladu sa fiziologijom.",
	"stress_flag_recovery_debt_label":         "Dug oporavka",
	"stress_flag_recovery_debt_desc":          "Noću HRV ↓, RHR ↑ — jučerašnje opterećenje stiglo. Danas držite lakim.",
	"stress_flag_parasympathetic_rebound_label": "Vagalni rebound",
	"stress_flag_parasympathetic_rebound_desc":  "HR povišen ali HRV iznad norme — obrazac oporavka, ne akutni stres.",
	"stress_flag_acute_stress_label":          "Akutni skok",
	"stress_flag_acute_stress_desc":           "Jedan sat sa HR > 2 SD iznad dnevne norme. Tranzitorno, akcija nije potrebna.",
	"stress_flag_sustained_load_label":        "Trajno opterećenje",
	"stress_flag_sustained_load_desc":         "4+ uzastopnih sati sa HR > 1 SD iznad dnevne norme. Stvarno autonomno opterećenje.",
	"stress_flag_stale_stress_label":          "Podaci stresa nepotpuni",
	"stress_flag_stale_stress_desc":           "Dan je završen, u budnom prozoru manje od 8h HR podataka (sat skinut / sync gap) — sustained-load drain isključen za ovaj dan.",
	"stress_flag_data_accruing_label":         "Skupljamo podatke",
	"stress_flag_data_accruing_desc":          "Dan je u toku: potrebno je ≥8h HR podataka u budnom prozoru za procenu trajnog opterećenja srca. Metrika će se pojaviti kad se nakupi dovoljno sati.",
	"stress_flag_calibration_warmup_label":    "Kalibracija",
	"stress_flag_calibration_warmup_desc":     "Lični baseline još u warmup-u (3-6 uzoraka). Pragovi oznaka mogu biti konzervativni.",

	// Subjective morning check-in (Telegram inline keyboard).
	"checkin_prompt_text":  "Kako se osećate jutros?",
	"checkin_btn_great":    "Odlično",
	"checkin_btn_ok":       "Normalno",
	"checkin_btn_meh":      "Onako",
	"checkin_btn_sick":     "Bolestan(a)",
	"checkin_ack_great":    "Zabeleženo: Odlično. Lep dan.",
	"checkin_ack_ok":       "Zabeleženo: Normalno.",
	"checkin_ack_meh":      "Zabeleženo: Onako. Štedite se danas.",
	"checkin_ack_sick":     "Zabeleženo: Bolestan. Odmarajte.",
	"checkin_ack_late":     "Zabeleženo posle izveštaja — ide u analitiku.",
	"checkin_expired_note": "<i>Želite da izveštaj bolje odražava vaše stanje? Odgovorite jednim dodirom sutra.</i>",

	"energy_note_capacity":              "jutarnji kapacitet iz sna %.1fh i markera oporavka",
	"energy_component_morning_capacity": "Jutarnji kapacitet",
	"energy_component_activity_load":    "Opterećenje (danas vs 28d hronični)",
	"energy_component_autonomic_stress": "Autonomni stres (RHR / HRV)",

	"details": "Detalji",

	// Telegram report sections
	"tg_morning_header":      "Jutarnji izveštaj",
	"tg_evening_header":      "Dan dosad",
	"tg_readiness":           "Spremnost",
	"tg_readiness_today":     "danas",
	"tg_readiness_trend":     "trend 7 dana",
	"tg_today":               "Danas dosad",
	"tg_yesterday":           "Juče",
	"tg_recommendation":      "Plan za danas",
	"tg_alerts":              "Anomalije",
	"tg_insights":            "Uvidi",
	"tg_sources":             "Izvori",
	"tg_energy":              "Energija",
	"tg_no_data":             "Još nema svežih podataka.",
	"tg_warn_stale":          "<i>Podaci su stari %d dan(a) — sinhronizacija možda još nije završena.</i>",
	"tg_warn_no_sleep":       "<i>Nema podataka o snu za prošlu noć — telefon možda još nije sinhronizovan.</i>",
	"tg_warn_no_activity":    "<i>Nema podataka o aktivnosti za danas.</i>",
	"tg_vs_yesterday_up":     "+%.0f%% u odnosu na juče",
	"tg_vs_yesterday_down":   "%.0f%% u odnosu na juče",

	// Smart-retry stale-data banners
	"tg_stale_no_data":        "⏰ <i>Rok je prošao, ali podaci o snu sa sata nisu stigli — otvori Health ili proveri sinhronizaciju Apple Watch-a.</i>",
	"tg_stale_recent_segment": "⏰ <i>Rok je prošao, ali sat još uvek beleži san — vrednosti ispod mogu biti nepotpune.</i>",
	"tg_stale_still_writing":  "⏰ <i>Rok je prošao, ali fragmenti sna i dalje pristižu — vrednosti ispod mogu biti nepotpune.</i>",

	// Per-metric "device off" banners
	"tg_watch_off":     "🔕 <b>Apple Watch nije na ruci</b> — poslednji HRV/RHR pre %s. Oporavak preskočen.",
	"tg_phone_off":     "📵 <b>Telefon ne sinhronizuje</b> — nema podataka o koracima %s. Aktivnost preskočena.",
	"tg_sleep_silence": "😴 <b>San nije zabeležen</b> — poslednja noć sa podacima bila je pre %s.",
	"tg_dur_hours":     "%dh",
	"tg_dur_days":      "%d dana",

	// Weekly data-quality digest
	"tg_digest_header":      "🔬 Kvalitet podataka za nedelju",
	"tg_digest_clean":       "Sve čisto — u poslednjih %d dana nema anomalija.",
	"tg_digest_impossible":  "🚫 <b>Nemoguće vrednosti</b> (greške senzora)",
	"tg_digest_suspect":     "⚠️ <b>Sumnjive vrednosti</b> (>3σ od tvoje norme)",
	"tg_digest_missed":      "😴 <b>Noći bez podataka o snu</b>",
	"tg_digest_watch_off":   "🔕 <b>Sat skinut:</b> oko %dh u ovom periodu",
	"tg_digest_more_in_ui":  "<i>Detalji po redovima — u admin stranici.</i>",

	// Onboarding nudge
	"tg_energy_backfill_nudge_header": "📊 Istorijski EnergyBank te čeka",
	"tg_energy_backfill_nudge_body":   "Imaš {complete} kompletnih dana istorije, ali samo {backfilled} backfilled snapshot-ova EnergyBank-a. Dok ne pokreneš backfill, pragovi verdikta (rest / moderate / push hard) koriste default vrednosti umesto tvoje lične distribucije — preporuke su pristrasne, Telegram izveštaji nedokalibrisani.",
	"tg_energy_backfill_nudge_cta":    "Otvori Podešavanja → Istorijski EnergyBank",

	// Monthly stress-validation nudge — STRESS_MEASUREMENT.md §4.5.
	"tg_stress_validation_header": "🎯 Stres-formula validirana",
	"tg_stress_validation_body":   "§4.5 4-kanalna rubric je vratila <b>{verdict}</b> za tvoju instancu. {reason} Možeš uključiti <code>settings.energy.stress_drain_enabled</code> ako želiš da EnergyBank drain reaguje na trajno autonomno opterećenje.",
	"tg_stress_validation_cta":    "Otvori /admin → Validacija stres-formule",
}
