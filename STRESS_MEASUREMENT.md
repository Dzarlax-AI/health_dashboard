# Stress Measurement — methodology

Status: methodology design draft. **Canonical source** for the v2.2
autonomic-load drain term and stratified stress flags. Companion to
`ENERGY_BANK.md` § Drain — that file defers formula details here and
owns only storage/integration shape. Supersedes
`ENERGY_BANK_V2_2_DRAFT.md` (kept in repo as historical context).

This document is about *what stress means and how we measure it on the
data we have*, separate from how those measurements feed into the
EnergyBank formula.

## 0. Pre-v2.2 blockers

These must land before β can be turned on; tracked here so the
methodology read isn't blocked on chasing them from other files:

1. **`daily_scores.rhr_avg` is not RHR.** It's a per-day average of
   heart_rate, not a resting baseline. v2.2 must NOT consume this
   column as `RHR_baseline`. Either (a) fix the column to compute
   from overnight low-HR windows, or (b) introduce a separate
   `baseline_hr_overnight` (median HR over the last 3 hours of the
   main sleep segment, with `03:00–06:00` fallback only when sleep
   data is imputed/stale; see §4.1) and route v2.2 to it. Pick one
   path, do not run both in parallel.
2. **Awake window definition.** Default `07:00–22:00` is a stopgap.
   Real awake window must derive from `sleep_analysis` **for the
   specific date d**. Algorithm — NOT "first asleep segment after
   noon" (breaks on siestas, night-shift, jet-lag):
   1. Pick the **longest continuous asleep segment** whose midpoint
      falls within ±6 hours of local midnight for date d. This is
      "the main sleep for night-ending-on-d".
   2. `wake_time[d]` = end of that segment.
   3. `sleep_onset[d]` = start of the next continuous asleep
      segment whose midpoint falls within ±6 hours of local
      midnight for date d+1.
   4. If either is missing (no main sleep found in the window —
      pulled all-nighter, watch off, etc.), fall back to default
      `07:00–22:00` AND emit `imputed_awake_window` flag.

   This handles: late nights (asleep 02:00→10:00 → wake 10:00), shift
   work (asleep 06:00→14:00 → wake 14:00, day awake 14:00→22:00),
   biphasic sleep (siesta 14:00–16:00 doesn't pollute window —
   shorter than the main sleep), trans-meridian travel (algorithm
   anchors to the date d's local midnight, so the window shifts with
   the user). Edge case: equally-long sleeps split around midnight
   → pick the one whose end is later (treats it as wake-up for day d).

   **Implementation note**: `internal/storage/typical_wake.go::GetTypicalWakeTime(days)`
   returns the *typical* (averaged) wake time across N days — wrong
   shape for this use case. Need a new helper `WakeTimeForDate(date)`
   that returns (wakeHour, sleepOnsetHour, ok) implementing the
   algorithm above. `GetTypicalWakeTime` stays for `MorningCapTime`
   usage (which legitimately wants the average); v2.2 must not
   reuse it.
3. **Coverage gate.** A day with <8 hours of HR-covered awake time
   (watch off charger, sync gap) must NOT compute `sustained_hr_load`
   — emit `stale_stress` flag and fall back to v2.0 (kcal-only)
   drain for that day. Without this, days where the watch was off
   between 10:00 and 16:00 systematically under-drain.

## 1. What "stress" means physiologically

"Stress" is not one state — it's a spectrum with distinct biological
signatures, timescales, and measurable proxies. Conflating them
produces what Marco Altini (HRV4Training, PhD physiology) called the
Garmin Body Battery problem: ["made up scores"][altini-2025] with no
peer-review validation.

| Type | Marker pattern | Timescale | In our data? |
|---|---|---|---|
| **Acute sympathetic spike** (startle, caffeine hit, pre-deploy nerves) | HR↑, HRV↓, EDA↑ | seconds–minutes | HR yes; HRV/EDA no/partial |
| **Sustained autonomic load** (worried about the dog all day) | sustained HR shift +5-10 bpm, depressed HRV, faster resp rate | hours | **v2.2 target** |
| **Allostatic load / chronic stress** (burnout, sustained sleep debt) | baseline RHR drift up, HRV drift down, over weeks | weeks-months | Yes via 30-90d trends |
| **Illness response** (flu, infection, COVID prodrome) | RHR↑5-10 bpm, wrist temp↑0.3°C, HRV↓↓ | days | Partial (wrist_temperature only since 2025-10) |
| **Recovery debt** (yesterday's hard session) | overnight HRV↓, slight RHR↑ | following morning | Yes |

Two implications for design:
1. **Multiple signals, multiple flags** — not a single "stress score". A
   composite obscures which physiological process is loaded. The
   wearable-evaluation literature [[degruyter-2025]] is unanimous on
   this:
   > Raw HRV and RHR показали significant associations с validated
   > stress measures. **Proprietary composite algorithms lack
   > transparency and independent validation**. Composite scores add
   > questionable value over raw signals.
2. **Personal baseline is mandatory.** All consumer wearables (Whoop,
   Oura, even Garmin) train a personal 14-30 day baseline before
   producing scores. Population-constant thresholds are noise. This is
   already the convention in `ENERGY_BANK.md` (RHR baseline state
   machine `cold → warmup → steady`).

## 2. Available signals in the project's database

Sample counts as of 2026-05-12:

```text
heart_rate              1 916 248 samples   2015-01-11 → present   high-density continuous (every few min)
respiratory_rate           35 954 samples   2021-09-24 → present   ~daily aggregates
blood_oxygen_saturation    32 830 samples   2020-12-31 → present
heart_rate_variability     13 373 samples   2018-04-29 → present   sparse (~5/day, event-triggered)
wrist_temperature             191 samples   2025-10-08 → present   nightly only, requires Apple Watch S8+
```

**Critical gaps:**
- **EDA / GSR** — Apple Watch doesn't report it. Fitbit Sense and Whoop 4.0+ do.
- **Cortisol** — no non-invasive wearable path exists in 2026.
- **Continuous HRV** — Apple measures HRV only during specific events
  (Breathe app sessions, after exercise, occasionally nocturnal).
  Whoop and Oura compute RMSSD continuously during sleep; Apple
  doesn't expose that aggregate even when the sensor has the data.
- **Subjective check-ins** — Apple "State of Mind" exists since iOS 17
  but isn't in the HAE export schema. Could be added as a manual
  entry path later.

**Implication:** Heart rate is our only dense continuous signal. Stress
methodology must lean on HR primarily, with HRV / resp / temp as
**secondary confirmatory channels** rather than primary inputs.

## 3. State of the art (2025 literature)

### HRV-based stress

[Mdpi sensors 2025][mdpi-rmssd-2025]: Daily RMSSD reliably tracks
perceived stress, fatigue, and sleep quality in general adults. Methods
require **standardised morning protocol**: same posture, same time,
controlled for last meal / caffeine. Without standardisation, the
signal-to-noise ratio is too poor for daily decisions. **Apple Watch
HRV is not standardised** — events fire at varying times of day,
varying postures, varying contexts — so single readings are noisy.

[MDPI sensors 2023 review][mdpi-hrv-wearable-2023]: time-domain RMSSD
is the preferred metric for consumer wearables (vs frequency-domain
LF/HF) because it's robust to short recordings (5 min sufficient) and
doesn't need careful breathing control.

### Nocturnal HRV vs daytime HRV

[PMC 2025 validation study][pmc-nocturnal-2025]: nocturnal HRV
(measured during deep sleep) is the cleanest baseline. Whoop uses last
slow-wave sleep cycle exclusively; Oura averages 5-min segments across
the night. Eliminates daytime confounders: caffeine, posture,
sympathetic activation from notifications/screens.

For our pipeline this means: **if we want HRV as a stress signal,
restrict to overnight aggregates** computed from `metric_points` where
the sample falls inside the sleep window (cross-reference with
`sleep_analysis` segments).

### Daytime stress detection — Oura's approach

Oura's 2024-2025 daytime stress feature polls 15-min biometric windows
(HR + HRV + temperature) and flags windows where the deltas vs
personal baseline exceed thresholds. **It is not a single score** —
it's annotated time windows with deltas attached. This is methodologically
sound (matches the "raw signals, no composite" principle from
literature review) and produces actionable UX (see what stressed you,
not just "your stress was 67 today").

We could replicate this directly on our `hourly_metrics` table — see
§ 5 below.

### Body Battery / readiness scores

[The5krunner 2025][altini-2025]: Altini's critique of Garmin Body
Battery is that the algorithm is closed, the validation is not
peer-reviewed, and outputs drift significantly between physiologically
similar days. Whoop's Strain/Recovery scoring is more transparent (a
4-week personal baseline plus published formula sketches) but still
proprietary. Apple doesn't ship a recovery composite — they expose raw
HRV/RHR and let the user judge.

**Lesson for v2:** if EnergyBank is to claim methodological rigor, it
must (a) cite the inputs explicitly in `components JSONB` (already
does), (b) avoid a single opaque score (it kind of has one — the
bank — but `verdict_reason` provides component context), (c) document
the baselines and validate them.

## 4. Methodology for our system

### 4.1. Per-user baselines (rolling 30d)

For each user, compute daily:

```text
baseline_hr_awake[d]     = median of hourly_metrics.heart_rate.avg_val
                           for hour-of-day ∈ awake_window[d]
                           over days [d−30, d−1]
                           (use median not mean — robust to outlier days)
sd_hr_awake[d]           = max(MAD-based SD of the above, SD_FLOOR_HR)

# SD_FLOOR_HR = 3.0 bpm.
# Rationale: users with very stable daytime HR can have MAD-derived SD
# as low as 1.5–2 bpm. Without a floor, a routinely-busier-than-usual
# day (+5 bpm) reads as z ≈ 2.5 — false "anomaly". The 3 bpm floor
# was chosen empirically on a single-user 90-day prototype
# (see §4.5 step 4); N=1 is a known limitation. Sedentary users with
# very narrow HR distributions may need 2.0; athletes with wider
# day-to-day variance may tolerate 4.0. **Revisit in v2.5 once the
# cohort study (≥3 users, see §6 Q6) returns empirical SD
# distributions per profile preset.** Same pattern applies to other
# channels: each has its own MIN sd guard documented inline.

baseline_hr_overnight[d] = median HR over the LAST 3 HOURS of the
                           longest continuous sleep segment for the
                           night ending on day d (derived from
                           sleep_analysis, same source as
                           WakeTimeForDate). NOT a fixed wall-clock
                           window — a fixed 03:00–06:00 breaks on
                           jet-lag, night shift, and "asleep at 02:00"
                           late nights, systematically biasing RHR
                           upward on those nights. Fallback to fixed
                           03:00–06:00 only when sleep_analysis is
                           imputed_sleep / stale for that night.
                           This is what daily_scores.rhr_avg should be
                           but currently isn't (§0 blocker 1).

baseline_hrv[d]          = median heart_rate_variability over [d−30, d−1]
                           filtered to overnight samples only.
                           Caveat: with ~5 events/day, ~5 nocturnal
                           samples per 30 days, baseline is thin.

baseline_resp[d]         = median respiratory_rate over [d−30, d−1]

baseline_temp[d]         = median wrist_temperature over [d−30, d−1]
                           gated: require ≥14 samples or skip the
                           temperature channel for that day.
```

All baselines are **per-user, computed from his own history**. No
population norms — they are noise (different RHR ranges for athletes
vs sedentary, different HRV ranges by age/sex/genetics).

**Storage**: baselines are NOT persisted, recomputed on demand from
`metric_points` and `hourly_metrics`. Same convention as
`ENERGY_BANK.md` § 1 ("Baseline is never persisted — recomputed from
metric_points overnight RHR samples on every recompute pass").

**Calibration states** (per `ENERGY_BANK.md` spec, lifted):

| State | Condition | Output |
|---|---|---|
| `cold` | < 3 valid samples in last 30d | nil — no stress signal computed |
| `warmup` | 3-7 samples | computed but flagged `calibration:warmup` |
| `steady` | ≥ 7 samples, newest ≤14d old | normal |

### 4.2. Daily deviations (per-day z-scores)

```text
hr_shift[d]       = (today_awake_hr_avg − baseline_hr_awake[d]) / sd_hr_awake[d]
rhr_shift[d]      = (today_overnight_rhr − baseline_hr_overnight[d]) / sd_hr_overnight[d]
                    // overnight RHR per §4.1 (last 3h of main sleep
                    // segment; fallback 03:00–06:00 only when sleep
                    // data is imputed/stale for that night),
                    // floored sd same as hr_awake (SD_FLOOR_HR = 3.0 bpm)
hrv_drop[d]       = (baseline_hrv[d] − today_overnight_hrv) / sd_hrv[d]   // higher = worse
resp_shift[d]     = (today_resp_avg − baseline_resp[d]) / sd_resp[d]
temp_shift[d]     = (today_temp − baseline_temp[d]) / sd_temp[d]
```

Z-scores, not absolute. Interpretation per literature [[mdpi-rmssd-2025]]:
- |z| ≤ 1 — within normal personal range
- 1 < |z| ≤ 2 — meaningful deviation, worth flagging
- |z| > 2 — anomaly, attention-worthy

This matches the existing convention in `internal/health/quality.go`
suspect-flagging (z > 3) and `internal/health/alerts.go` HRV-CV
thresholds.

### 4.3. Stratified flags (not a composite score)

Flag predicates operate on the **hourly z-series** `hr_z_hour[h]`
defined in §4.4, not on a daily-average `hr_shift`. The daily
`hr_shift[d]` in §4.2 is kept only as a single summary number for
UI / Telegram one-liners; flag logic must read the hourly array to
distinguish "one 30-min spike" from "sustained 4h elevation".

```text
acute_stress[d]       = exists window <2h where hr_z_hour[h] > +2
sustained_load[d]     = hr_z_hour[h] > +1 sustained ≥4h consecutive
illness_signature[d]  = (temp_shift > +1) AND (resp_shift > +1)
                        AND (hrv_drop > +1)   // all three together
recovery_debt[d]      = overnight (hrv_drop > +1) AND (rhr_shift > +0.5)
                        // next-morning signal from prior day's load
parasympathetic_rebound[d]
                      = (hr_shift > +1) AND (hrv_drop < −1)
                        // elevated HR but HRV ABOVE normal — vagal
                        // rebound after sustained stress / heavy
                        // session; physiologically real, not noise
```

Each flag drives **distinct downstream behaviour**:
- `acute_stress` → no action (transient, no point telling user)
- `sustained_load` → factors into EnergyBank drain (v2.2)
- `illness_signature` → does NOT factor into drain (the HR rise the
  flag detected is already in `sustained_hr_load` — see §4.4). Routes
  to verdict layer: overrides `verdict_reason` text ("body fighting
  infection — rest aligns with this") and suppresses AI `push_hard`
  recommendation.
- `recovery_debt` → factors into next-day readiness baseline
- `parasympathetic_rebound` → **interpretation flag, not a
  correction term.** Does NOT subtract from `sustained_hr_load` and
  does NOT amplify it; only enriches `verdict_reason` text
  ("autonomic state mixed — HR high, HRV high, likely recovery
  phase, not acute stress"). The HR rise was real autonomic
  expenditure and stays in drain; the flag adds context for the
  user-facing narrative. Without this flag the day would otherwise
  classify as `sustained_load` only, missing the recovery-phase
  nuance.

### 4.4. Integration with EnergyBank (the v2.2 piece)

Drain formula becomes:

```text
drain_v22[d] = α · active_energy_kcal[d]
             + β · sustained_hr_load[d]

sustained_hr_load[d] = Σ_h max(0, hr_z_hour[h] − Z_THRESHOLD)
                       for h ∈ awake_window[d]
                       only if hr_coverage_hours ≥ MIN_COVERAGE

hr_z_hour[h] = (hour_hr[h] − baseline_hr_awake[d])
             / sd_hr_awake[d]    # sd already floored, see §4.1
```

Where:
- `hour_hr[h]` = **median of 5-min minima** in hour h (not mean —
  postural noise averages out, per §4.6 risk row).
- `Z_THRESHOLD = 0.5` (default; **read from `settings.energy.z_threshold`,
  not hardcoded** — same pattern as α and β). Only hours noticeably
  above the user's personal awake norm contribute; below this, treat
  as routine variation. Starting value chosen so a typical workday
  with one 2h busy stretch (z≈1) yields ~3 load units and a sustained
  5-hour anxiety day (z≈2) yields ~7.5. **Risk noted in
  multi-model review**: for cognitively-demanding desk work, HR can
  sit at z=0.5–0.8 systematically without being "stress" in the
  autonomic sense — just sustained mental activation. v2.1
  calibration may need to push the threshold up to 0.7–0.8 for
  desk-heavy lifestyle profiles. Making it a setting from day one
  avoids a schema/code change at that point — flip the value in
  `/admin`, recompute via `cmd/energy_backfill`.
- `MIN_COVERAGE = 8 hours` of HR samples inside the awake window;
  otherwise emit `stale_stress` flag and set `sustained_hr_load = 0`
  (drain falls back to v2.0 kcal-only for the day). Coverage gate is
  a §0 blocker — must land before β goes non-zero.

**Why hourly sustained, not daily average.** A day-average `hr_shift
× awake_hours` would assign the same drain to (a) 4 hours of
sustained +10 bpm vs (b) 1 short spike + 14 normal hours. The hourly
integral preserves temporal structure: only the hours that actually
deviated count. This is the same shape Oura uses for daytime stress
windows (§3) and avoids the "spike-masking-by-averaging" failure
mode the multi-model review flagged for the daily-avg variant.

**Two values in `components` JSONB for transparency.** Drain
consumes only `sustained_hr_load_z`, but the audit trail also stores
`hr_overshoot_bpm_hours = Σ max(0, hour_hr[h] − baseline_hr_awake)`
in raw units — that's the number that can be quoted to the user
("HR ran ~8 bpm above your normal for ~4 hours"). The two are
correlated but not redundant; both stay until cohort validation
shows one suffices.

The other flags (`illness_signature`, `recovery_debt`,
`parasympathetic_rebound`) influence the **verdict layer**, not the
drain layer. Drain is the "physiological cost of the day"; verdict
is "given the cost and the trend, what should the user do tomorrow".
Mixing them inflates drain artificially when the user is sick (the
HR rise the flag detected is already in `sustained_hr_load` — adding
an illness multiplier would double-count).

### 4.5. Validation strategy

**No self-report.** Subjective daily stress logs (1–5 scale) are the
intuitive choice but methodologically poisoned: people forget, default
to "3", and most importantly rationalise post-hoc ("there was a
deadline today → must have been stressed → log 4"). This is textbook
confirmation bias and it inflates Pearson r in the direction the
formula already points. The validation harness uses **only objective
signals already in the health database** — no user input path, no
external integrations (no calendar — Apple Calendar isn't in the HAE
export, and we won't add a calendar dependency for a health-data
tool).

Four channels, ranked by signal quality:

1. **Next-morning HRV residual (primary).** Pearson(`sustained_hr_load[d]`,
   `overnight_HRV[d+1]`) over a 30-day rolling window. This is the
   same external residual ENERGY_BANK.md §v2.5 uses for α-personalization
   — reusing it here costs nothing and keeps a single source of truth
   for "is the autonomic signal real for this user". HRV the next
   morning is not *directly* controllable and is far less prone to
   retrospective reporting bias than a self-report log (it can still
   be influenced by behaviour — alcohol, late training, breathwork —
   but the user isn't writing the number down, so post-hoc
   rationalisation doesn't bend it). Expected: |r| ≥ 0.3 with
   negative sign (high stress load → depressed next-morning HRV) for
   the formula to be considered validated for this user.

2. **Next-morning RHR shift (secondary).** Pearson(`sustained_hr_load[d]`,
   `overnight_RHR[d+1] − baseline_hr_overnight`). Cross-check for
   channel 1. Caveat per ENERGY_BANK.md §v2.5: RHR can sign-flip
   physiologically (vagal rebound after heavy load → next-morning RHR
   *drops*). Hence secondary — used as agreement check, not standalone
   pass criterion.

3. **Sleep architecture degradation (tertiary).** High daytime
   autonomic load → longer sleep_onset_latency, more sleep_awake,
   reduced deep_pct in the first third of the night. All fields
   present in `sleep_analysis`. Compute three sub-correlations:
   `sustained_hr_load[d]` vs `sleep_onset_latency[d]`,
   `sustained_hr_load[d]` vs `sleep_awake[d]`,
   `sustained_hr_load[d]` vs `deep_pct_first_third[d]`. Aggregate sign
   agreement, not raw r — each is noisy individually.

   **Apple-Watch-only caveat.** Apple Watch sleep staging is known
   to over-detect deep sleep (it conflates immobility with
   slow-wave sleep, where polysomnography would call it light
   sleep / quiet wake — multiple validation studies have flagged
   this). For nights where the picked sleep source is Apple Watch
   (per ENERGY_BANK.md sleep-cross-validation rules),
   `deep_pct_first_third` is downweighted: only `sleep_onset_latency`
   and `sleep_awake` count toward the channel 3 vote. Both signals
   are robust on Apple data (latency from timestamps, awake fragments
   are explicit segments). For nights where Oura or a research-grade
   device is the picked source, all three sub-signals vote. This
   prevents the disagreement-override below from rejecting valid
   calibration just because Apple miscalled deep-sleep architecture.

4. **Test-retest stability (sanity).** Run the same date through the
   formula on day d+1 vs d+7 (when daily_scores fully settles).
   `sustained_hr_load` should differ by ≤ 1 z-load unit; bank should
   differ by ≤ 2 points. Drift larger than that means inputs are
   non-settled or the formula is overfitting recent context. **Pinned
   in Go test `TestBankConvergence`**: fixed seed metric_points
   fixture, three bootstrap seeds {10, 50, 90}, assert convergence
   within 2 points by day 9 of forward iteration. Without this CI
   guard, silent drift on α/β tuning regresses the bootstrap
   convergence claim in ENERGY_BANK.md without surfacing.

**Decision rubric** (run by `/api/admin/stress-validation`, no CSV
upload — pure internal automaton over the user's own history):

| Channel 1 (HRV) | Channels 2+3 agreement | Action |
|---|---|---|
| `r ≤ −0.3` | At least one of {RHR, sleep} agrees in sign | **Validated** — β may be tuned per §6 Q3 calibration |
| `−0.3 < r < −0.1` | At least two channels agree in sign | **Weak signal** — β stays at placeholder, surface `calibration_weak` flag, recheck weekly |
| `\|r\| < 0.1` on channel 1 | — | **Inconclusive** — likely data sparsity (HRV samples < 15 in window) or low-variance lifestyle; β stays at 0, `data_quality_warn` flag |
| `r > 0` on channel 1 | — | **Wrong-direction** — formula isn't capturing this user's physiology; β suppressed, log diagnostic, escalate to manual review. Matches the `r < 0` (sign-flip) suppression in ENERGY_BANK.md §v2.5. |

**HRV sparsity preflight.** Channel 1 requires a minimum sample
density to be computable at all. Apple Watch HRV is event-triggered
(Breathe sessions, post-exercise, occasional nocturnal) — a user
without a daily breathing habit may produce 0–2 overnight HRV
samples per week. **If `count(overnight_HRV samples)` in the 30-day
window < 15**, channel 1 is marked `cold` (per §4.1 calibration
states) and the rubric falls back to channels 2+3+4 only with a
2-of-3 agreement requirement (instead of channel 1 anchoring the
verdict). If channels 2 and 3 are also `cold` (no overnight RHR /
no scored sleep) → verdict is `inconclusive`, β stays at 0,
`data_quality_warn` flag. Without this guard, computing Pearson r
on 3–4 points produces statistical noise that the rubric would
mis-interpret as signal.

**Disagreement override.** Channel 1 alone is not sufficient — if
channel 1 says `r ≤ −0.3` but **both** channels 2 (RHR) and 3 (sleep
architecture) disagree in sign (i.e. both point to "no stress" or
opposite-direction signal), the verdict is **downgraded to `weak`
regardless of r magnitude**. Rationale: a single-channel signal with
two contradicting cross-checks is more likely overfitting or
artefact (sample sparsity in HRV, sleep tracker noise) than real
physiology. This closes the asymmetry the multi-model review
flagged: previously `r=−0.35` validated even when both cross-checks
contradicted.

The point of this table: the system **refuses to calibrate silently**
when the signal is weak or self-contradictory, instead of fitting β
to noise. Same philosophy as v2.5 personalization rubric.

5. **Comparison vs naïve baseline.** Does v2.2 beat v2.0 at predicting
   next-morning HRV / readiness? If the only added complexity gives
   <5% improvement on RMSE, it's not worth the moving parts — drop β
   back to 0, keep the components JSONB audit trail for future
   re-evaluation when wrist_temperature has 6+ months of data.

### 4.6. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Posture confounding (standing HR > lying HR by ~10 bpm) | Use **median of 5-min minima per hour**, not means — postural noise averages out |
| Caffeine, alcohol confounding | Personal baseline (rolling 30d) absorbs habitual intake; non-habitual events correctly show as z-spikes (the organism IS spending autonomic resource). Day-tagging UI (`alcohol`, `caffeine_high`, `travel`) deferred to v2.3 — tags do **not** modify drain, only reshape verdict narrative ("elevated HR likely from alcohol metabolism, not sustained stress"). Alcohol's overnight HR tail is the strongest case for tagging — without a tag it currently reads as recovery debt, which is technically true but mis-attributed. |
| Sparse HRV (5 samples/day) | HRV is **secondary** signal; use only when ≥3 nightly samples; otherwise skip the channel that day |
| Sensor classification gaps (Apple sometimes registers HR+steps but zero active_energy) | v2.2's HR-shift term catches these — that's the user's observed Sunday 21:00 case |
| Circular reasoning (formula affects user behaviour, behaviour affects inputs) | Validation channel 1 (next-morning HRV residual, §4.5) is the external ground truth — runs automatically on every recompute, no user input needed. HRV the next morning is not consciously controllable, breaking the feedback loop. |
| Day-of-week effects | Weekday-of-week stratification in baseline if needed — but only if 30d median shows pattern; otherwise YAGNI |
| Cyclical patterns (menstrual cycle) | Future scope, gated on user setting in admin UI |

## 5. Concrete next implementation steps

Decoupled from ENERGY_BANK cutover work — can ship independently.

1. **Add storage method `HourlyHRSeriesForAwakeWindow(date)`** —
   returns per-hour series for the date's awake window (resolved via
   `WakeTimeForDate(date)`, see §0 blocker 2). Each row:
   `{hour, median_5min_min_hr, sample_count, coverage_ok}` where
   `median_5min_min_hr` is the §4.4 canonical input (median of per-
   5-minute minima within the hour — postural noise averages out)
   and `coverage_ok` is true when sample_count meets the per-hour
   density threshold. Caller aggregates `coverage_ok=true` rows to
   compute `hr_coverage_hours` for the §4.4 MIN_COVERAGE gate. A
   secondary helper `HourlyHRStats(date)` that returns mean/min/max
   stays useful for UI display but is **not** the input to drain.

2. **Add storage method `PersonalBaseline(date, channel, window)`** —
   returns (median, mad_sd, sample_count, calibration_state) for a
   given biometric channel. Pure read from `hourly_metrics` /
   `metric_points`, no persistence.

3. **Add `internal/health/stress.go`** — pure-math kernel:
   - `func HourZShift(hourHR, baseline, sd float64) float64` — per-hour z
   - `func SustainedHRLoad(hourZ []float64, zThreshold float64) float64` — §4.4 integral
   - `func IllnessSignature(tempZ, respZ, hrvZ float64) bool`
   - `func RecoveryDebt(hrvZ, rhrZ float64) bool`
   - `func ParasympatheticRebound(dayHRShift, hrvDrop float64) bool`
   - Tests covering each branch + edge cases (NaN, empty, < calibration).

4. **Wire into `internal/storage/briefing.go`** — populate stress
   flags on the briefing response; render in hero or below it in
   `internal/ui/sections.go`.

5. **Wire into EnergyBank drain** — extend `DrainV2` to accept
   `sustainedHRLoad float64`. Effective β reads from
   `settings.energy.beta` AND gated by
   `settings.energy.stress_drain_enabled` (default `false`); when
   the flag is off, `β_effective = 0` regardless of stored β —
   `sustained_hr_load_z` still computes into `components` JSONB
   for audit, but never multiplies into bank. Flag flips to `true`
   only after §4.5 validation rubric returns `validated` for this
   user.

6. **Calibration cmd** — `cmd/stress_backfill` or extend
   `cmd/energy_backfill` with a `--include-stress` flag. Runs the
   stratified flag detection over historical days, prints distribution
   stats. Same pattern as the existing backfill: write to a flags
   column, idempotent, dry-run option.

7. **Validation harness** — `/api/admin/stress-validation` runs the
   §4.5 four-channel automaton over the user's own history: pulls
   `sustained_hr_load[d]` series, joins against `overnight_HRV[d+1]`,
   `overnight_RHR[d+1]`, and sleep-architecture fields, returns per-
   channel Pearson r, sign-agreement matrix, and the decision-rubric
   verdict (`validated` / `weak` / `inconclusive` / `wrong_direction`).
   **No request body, no CSV upload, no user input** — pure read over
   existing tables. Idempotent, safe to call on every admin page
   load. Result cached for 1h in `settings` (`stress_validation_last`).

## 6. Open methodological questions

1. ~~**What's the "awake window"?**~~ **Resolved → see §0 blocker 2.**
   Awake window derives from `sleep_analysis` segments for the
   specific date d using the §0 blocker 2 rule: pick the **longest
   continuous asleep segment** whose midpoint falls within ±6 hours
   of local midnight for date d, set `wake_time[d]` to its end;
   `sleep_onset[d]` is the start of the next qualifying segment
   (midpoint within ±6h of midnight for d+1). Fallback to
   `07:00–22:00` only when sleep data is `imputed_sleep` or stale,
   AND emit `imputed_awake_window` flag. Existing
   `internal/storage/typical_wake.go::GetTypicalWakeTime(days)` returns
   an *average* across N days — wrong shape; v2.2 needs a new
   per-date helper `WakeTimeForDate(date)`. See §0 blocker 2 for
   why the "last asleep before noon" simplification breaks on
   siestas, night-shift, and jet-lag.

2. **HRV during deep sleep specifically vs whole-night?** Apple sample
   timestamps don't tell us sleep stage. Cross-reference with
   sleep_analysis segments where available; fall back to whole-night
   median where not. Validate that the difference is < 1 SD on
   well-sampled nights.

3. **β calibration target.** Single source of truth lives here; the
   superseded `ENERGY_BANK_V2_2_DRAFT.md` quoted β ≈ 0.12 against
   raw bpm·hours, which is no longer the canonical input unit (see
   §4.4 — drain consumes `sustained_hr_load_z`, not bpm·hours).

   **β = 0.8 is a non-production starting placeholder**, not a tuned
   constant. Chosen as a unit-scaling anchor so the Sunday-with-
   anxious-dog example (z-load ≈ 7.5 per §4.4) maps to ~6 drain
   points, matching the qualitative "tired by evening, not
   exhausted" interpretation. The anchor is a *human-readable
   sanity scale*, not a validation requirement — §4.5 makes self-
   report explicitly out of scope.

   **Production β_effective remains 0** until the §4.5 four-channel
   validation rubric returns `validated` for this user. At that
   point β is re-derived objectively: grid-search over a candidate
   range, pick the value that maximises `comparison vs naïve
   baseline` (§4.5 step 5 — RMSE improvement on next-morning HRV
   prediction) subject to the disagreement-override constraint.
   No self-report, no CSV upload. Until then, ship with
   `settings.energy.stress_drain_enabled = false`: compute
   `sustained_hr_load_z` into `components` for audit, but
   `β_effective = 0` and the bank does not move on the new term.

4. **What if HRV signal is too sparse for individual users?** Some
   users have only Breathe-session HRV (one per day or fewer). The
   methodology degrades gracefully: HRV channel skipped, HR-only flags
   still fire. Document this as a known limitation.

5. **How to communicate "stratified flags" in UI without overwhelming?**
   Possible: show a single coloured indicator in hero (illness/heavy
   stress/normal/recovery), expand to component breakdown on tap.
   Avoid surfacing all flags simultaneously.

6. **Multi-tenant cohort study.** If we get >10 open-source users, run
   the same methodology on their data, publish distributions of
   `hr_shift`, β-empirical values. Cohort variance tells us whether
   methodology is portable or needs per-user tuning beyond baselines.

## 7. Out of scope

- **Cortisol biomarker integration** — no consumer device path exists.
- **Continuous EDA** — not available on Apple Watch; revisit if Whoop
  / Fitbit integration is added.
- **Mental-state inference from movement patterns** (sitting too long,
  pacing, fidgeting) — interesting research direction (Apple's
  `step_count` granularity might allow it) but separate workstream.
- **HRV frequency-domain analysis** (LF/HF ratio) — requires
  beat-to-beat RR intervals which Apple doesn't expose; only
  time-domain RMSSD is feasible on consumer wearable data.
- **Acute stress alerting in real-time** — possible technically but
  UX is questionable (push notification "you are stressed RIGHT NOW"
  is meta-stress-inducing). Defer.

---

[altini-2025]: https://the5krunner.com/2025/08/19/garmin-body-battery-slammed-indirectly-by-altini-made-up-scores/
[degruyter-2025]: https://www.degruyterbrill.com/document/doi/10.1515/teb-2025-0001/html?lang=en
[mdpi-rmssd-2025]: https://www.mdpi.com/1424-8220/25/14/4415
[mdpi-hrv-wearable-2023]: https://www.mdpi.com/1660-4601/20/24/7146
[pmc-nocturnal-2025]: https://pmc.ncbi.nlm.nih.gov/articles/PMC12367097/
