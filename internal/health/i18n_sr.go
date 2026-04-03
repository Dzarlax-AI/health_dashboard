package health

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
	"lbl_vs_avg":     "vs avg",
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
}
