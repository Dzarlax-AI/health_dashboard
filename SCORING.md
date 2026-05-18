# Health Dashboard — Scoring & Calculation Logic

All calculations run server-side in `internal/health/scoring.go → ComputeBriefing()`.
Data is fetched by `internal/storage/briefing.go → GetHealthBriefing()`.

> **Why open-source the scoring?** A 2025 review of consumer wearable readiness scores found that proprietary algorithms (Oura, Whoop, Garmin) routinely differ by 20+ points on the same night's data — readiness/recovery is computed in opaque black boxes that lack independent validation [[29]](#ref-29). Every threshold here is sourced from published research and documented below; users (and Claude) can inspect, dispute, and improve it.

---

## Methodology Status

Not every score in this system carries the same evidential weight. Some are
expert-tuned formulae on personal baselines; some are leakage-aware floors
that survived feasibility analysis; some are still under evaluation. Each
score on the dashboard is tagged with one of the statuses below so the
reader can calibrate their trust accordingly. The UI surfaces a short badge
on the hero card; the table here is the authoritative mapping.

| Score / module | Status | What that means |
|---|---|---|
| Readiness v1 | `heuristic_personalized` | Expert-tuned ratio formula on personal baselines (HRV 40% / RHR 30% / Sleep 30%). Not a validated forecasting model — weights are literature-anchored, not learned from outcome data. |
| Energy Bank v1 (live) | `heuristic_prescriptive` | Rule-based daily verdict (rest / active recovery / moderate / push hard) layered on top of Readiness + activity load + stress amplifiers. Not yet validated against subjective state. |
| Energy Bank v2 (in progress) | `experimental_formula` | Separated sleep-quality restore, drain, signed bank, calibrated verdict bands, multi-channel stress overrides. Kernel under evaluation; not yet the production decision layer. See `ENERGY_BANK.md`. |
| Recovery Stability sub-score | `validated_floor_candidate` | Leakage-aware forward target with eligibility tree, source-epoch clipping and EWMA45 baseline floor. Phase 1 feasibility (PR #101) confirmed Ridge ≈ EWMA45 — production layer is the baseline floor; no learned model on top yet. See `READINESS_REDESIGN_PHASE1_RECOVERY_FEASIBILITY.md`. |
| Passive Efficiency sub-score | `validated_floor_candidate` | Same shape as Recovery; Phase 1 feasibility (PR #100) closed — EWMA45 wins. |
| Acute Risk labels | `labeling_framework_ready` | Leakage-free `t+1..t+3` event labels with per-candidate baselines (strict + OR variants). The labels are the deliverable; the downstream classifier is a separate, queued layer. |
| Chronic Load labels | `experimental_not_production` | Forward 14-day deterioration label. Useful as a research target; the count-based thresholds (`MinAcuteDensity`, `MinBreachDays`) are still hard-coded in Go and need to move to per-schema `settings` before other tenants' chronic labels can be trusted. |
| Stress flags (illness / recovery debt / rebound) | `heuristic_prescriptive` | Multi-channel z-shift detectors on 30-day personal baselines. Physiologically plausible warning signals; precision/recall against external labels not yet measured. |
| Headline / Coherence pass | `heuristic_prescriptive` | UX-safety layer that prevents contradictory cards (e.g. "stress" + "optimal recovery") rather than a score in its own right. |

**How statuses move**:

- `heuristic_personalized` → `validated_floor_candidate` requires a leakage-aware target, feasibility report that beats a baseline floor's lower CI bound, and subjective-label validation.
- `validated_floor_candidate` → production model requires a learned model that beats the floor's lower CI bound on a held-out window with pre-registered decision metric.
- `experimental_not_production` → anything else requires both an honest baseline and the multi-tenant calibration story closed (constants live in `settings`, not in Go).

Adapted from the methodology review by Manus AI (2026-05-17).

---

## Data window

Every metric is fetched for the **last 30 days** relative to the most recent date that has any data in the DB (not today's date — data can be stale).

Two different "recent" and "baseline" definitions are used depending on context:

**Readiness score & Recovery section** (require ≥ 9 days):
- **"Recent"** = average of the last **7 days** (index 0–6, most recent first)
- **"Baseline"** = average of days **8–30** (index 7 onward) — recent days are **excluded** from the baseline to prevent dilution

**Sleep / Activity / Cardio sections & Metric cards** (require ≥ 3 days):
- **"Recent"** = average of the last **3 days** (index 0–2)
- **"Baseline"** = average of **all 30 days** (including recent) — simpler because these sections use point-based or absolute-value scoring, not ratio-based

**% change** = `(recent − baseline) / baseline × 100`

> **Why 7 for "recent"?** The 7-day rolling average is the consensus recommendation for HRV-guided training decisions [[18]](#ref-18) [[19]](#ref-19). It aligns with the ACWR acute window [[2]](#ref-2) and fully covers the 72-hour post-exercise HRV suppression window [[1]](#ref-1) while reducing single-day noise. The coefficient of variation (CV) of the 7-day window is also a strong indicator of overreaching [[18]](#ref-18).

> **Why 9 total for Readiness?** With 7 recent days and only 1 baseline day, the baseline is a single data point. Requiring at least 2 baseline days (9 total) ensures a meaningful comparison. Section cards use simpler absolute or point-based logic so 3 days is sufficient.

---

## Readiness Score (0–100)

Shown as the big number in the hero card.

```
readiness = HRV_score × 0.40 + RHR_score × 0.30 + Sleep_score × 0.30
```

All three sub-scores use the same **ratio model**: `score = clamp(70 + (ratio − 1) × 150, 0, 100)`

This means:
- A perfectly average day (ratio = 1.0) → **70**
- ~10% above baseline (ratio = 1.10) → **85**
- ~20% above baseline (ratio = 1.20) → **100**
- ~15% below baseline (ratio = 0.85) → **47**
- ~33% below baseline (ratio = 0.67) → **~5**

A score of 100 is genuinely exceptional, not the default.

> **Why HRV 40% / RHR 30% / Sleep 30%?** WHOOP's published methodology states HRV should be the single biggest recovery factor [[3]](#ref-3). Marco Altini's HRV4Training model uses HRV at roughly double the weight of RHR [[4]](#ref-4), implying a ~2:1 HRV:RHR ratio — matching this codebase's 40:30 ratio. Oura analysis suggests RHR explains ~29% of readiness variance [[5]](#ref-5), consistent with the 30% weight.

### HRV sub-score (0–100)

`ratio = recent_HRV / baseline_HRV`

Applied directly to the ratio model (higher HRV = better).

> Using personal baseline rather than population norms is the recommended approach for HRV interpretation: individual longitudinal changes are more meaningful than cross-sectional comparisons [[6]](#ref-6) [[7]](#ref-7).

### Resting HR sub-score (0–100)

`ratio = baseline_RHR / recent_RHR`  ← inverted because lower RHR is better

Applied to the ratio model.

### Sleep sub-score (0–100)

Blends two components equally (50/50):

**Absolute component** — based on recent average sleep duration [[8]](#ref-8) [[9]](#ref-9), with oversleep penalty [[20]](#ref-20):

| Recent avg sleep | Score |
|---|---|
| ≥ 10.0 h | 40 (oversleep penalty) |
| ≥ 9.5 h | 60 (oversleep penalty) |
| ≥ 9.0 h | 80 (oversleep penalty) |
| ≥ 8.0 h | 95 |
| ≥ 7.5 h | 85 |
| ≥ 7.0 h | 75 |
| ≥ 6.5 h | 60 |
| ≥ 6.0 h | 45 |
| ≥ 5.5 h | 30 |
| < 5.5 h | 15 |

> **Why penalize oversleep?** A 2025 meta-analysis of 79 cohort studies (Li et al.) found that sleep ≥ 9 hours is associated with **34% higher all-cause mortality** (HR 1.34, 95% CI 1.26–1.42) — a stronger effect than short sleep (14% higher, HR 1.14). The U-shaped mortality curve means both extremes carry risk. The penalty is applied as a cap on the absolute score using `math.Min` so it only reduces, never raises [[20]](#ref-20).

**Relative component** — `ratio = recent_sleep / baseline_sleep`, applied to the ratio model.

`sleep_score = abs_score × 0.5 + rel_score × 0.5`

### Readiness labels

| Score | Label | Tip |
|---|---|---|
| ≥ 80 | Optimal | Great day for a challenging workout |
| ≥ 50 | Fair | Moderate activity is fine; listen to your body |
| < 50 | Low | Focus on recovery, avoid intense exercise |

### Recovery % bar
`recovery_pct = readiness_score` (same value, displayed as a progress bar)

---

## Readiness History

Available at `GET /api/readiness-history?days=N` (default 30, max 365).

For each output day D, the score is computed using a **sliding 30-day window**: HRV, RHR, and sleep_total values for D and the 29 days before D, sorted most-recent-first (D is index 0). The same `computeReadiness` formula applies.

**Important**: the `valsBefore` helper in `briefing.go` sorts days by **date** descending, so index 0 is always the most recent calendar day — not the highest-value day. An earlier bug sorted by value, which inflated "recent" averages.

---

## Section cards (Recovery / Sleep / Activity / Heart & Lungs)

Each section has its own point-based score that maps to **good / fair / low**.

**Important**: Section cards use different window parameters than Readiness:
- **Recovery**: 5-day recent, baseline = days 6+ (same as Readiness) — requires ≥ 7 days
- **Sleep / Activity / Cardio**: 3-day recent, baseline = all 30 days — requires ≥ 3 days

### Recovery section

Inputs: HRV and Resting HR (≥ 7 days required per metric).

#### HRV threshold — dynamic ±1 SD

Instead of a fixed ±5% threshold, the HRV threshold is computed from the **standard deviation of the baseline window** (days 6+):

```
thresholdPct = stddev(HRV_baseline) / mean(HRV_baseline) × 100
```

Clamped to [3%, 15%] to handle sparse or very stable data.

| Condition | Points |
|---|---|
| HRV recent > baseline + thresholdPct | +2 |
| HRV recent within ±thresholdPct of baseline | +1 |
| HRV recent < baseline − thresholdPct | +0 |
| RHR recent < baseline − 2% | +2 |
| RHR recent within −2% to +3% of baseline | +1 |
| RHR recent > baseline + 3% | +0 |

Max possible: 4 points (if both metrics available).

| Ratio (score / max) | Status |
|---|---|
| ≥ 0.75 | good |
| ≥ 0.40 | fair |
| < 0.40 | low |

> **Why dynamic SD threshold?** A fixed 5% threshold ignores individual HRV variability. Someone with a coefficient of variation (CV) of 2% would trigger "below baseline" on normal fluctuation noise; someone with CV 20% would rarely trigger it even when meaningfully depleted. A ±1 SD threshold (as a % of baseline) adapts to each person's natural day-to-day variability, giving fewer false positives for high-CV individuals and fewer missed detections for low-CV individuals [[4]](#ref-4) [[6]](#ref-6).

**Trend arrows on detail rows:**
- HRV: up if > threshold, down if < −threshold, otherwise stable
- RHR: inverted — up (good) if < −3%, down (bad) if > +3%

---

### Sleep section

Inputs: sleep_total, sleep_deep, sleep_rem, sleep_awake (≥ 3 days required).
Baseline here is `avg(all available days)` — the sleep section uses a simple
duration-based point scale, not the ratio model.

**Duration score** [[8]](#ref-8):

| Recent avg sleep | Points |
|---|---|
| ≥ 7 h | +3 |
| ≥ 6 h | +2 |
| ≥ 5 h | +1 |
| < 5 h | +0 |

**Deep sleep score** (% of total sleep):

| Deep % | Points |
|---|---|
| ≥ 15% | +2 |
| ≥ 10% | +1 |
| < 10% | +0 |

**Awake time bonus:**

| Recent avg awake | Points |
|---|---|
| < 0.5 h | +1 |

**Sleep regularity bonus** (≥ 7 days required) [[10]](#ref-10) [[33]](#ref-33) [[34]](#ref-34):

Two paths, in priority order:

1. **Sleep Regularity Index (SRI)** — primary, used when ≥ 7 calendar days of per-segment minute-level sleep data are available (iOS health-sync per-segment pushes; HAE midnight-summary nights alone cannot drive this).

   Formula (Phillips & Czeisler 2017): `SRI = 200 × p − 100`, where `p` is the fraction of (minute, day) pairs whose sleep/wake state matches the same minute on the previous day, computed over a 14-day rolling window.

   | SRI | Points | Tier (UK Biobank 2025 [[33]](#ref-33)) |
   |---|---|---|
   | ≥ 75 | +1 | protective (HR 0.90 mortality) |
   | 50–75 | +0 | neutral |
   | < 50 | +0 | clinically irregular (HR 1.53 mortality) |

2. **stddev fallback** — when per-segment data is sparse:

   | stddev(sleep_duration over window) | Points |
   |---|---|
   | ≤ 0.5 h | +1 |
   | > 0.5 h | +0 |

Max possible: 7 points.

| Total points | Status |
|---|---|
| ≥ 5 | good |
| ≥ 3 | fair |
| < 3 | low |

> **Why SRI over stddev?** stddev of nightly duration is a coarse proxy — it ignores *when* the user slept, only *how long*. Two people both averaging 7±0.5h can have wildly different circadian regularity (e.g. 23:00→06:30 vs nightly 02:00→09:30 vs erratic 21:00→04:00 / 02:00→09:00). SRI directly measures pairwise consistency at the minute level. UK Biobank 2025 (n ≈ 88 000) gives the concrete percentile thresholds used above. RIRI 2026 [[34]](#ref-34) shows alternative SRI formulations diverge enough at the tails to flip clinical conclusions, so we pin the original Phillips & Czeisler 2017 algorithm rather than re-deriving.

**REM display** (not scored, shown for info):
- Ideal: ≥ 20% of total sleep
- Formula: `recent_rem / recent_total × 100`

**Consistency display:**
- When SRI is available: `SRI N` (or `SRI N (n=k)` if fewer than 14 days contributed). Trend: up if ≥ 75, down if < 50, stable otherwise.
- Otherwise: `±Xh` legacy stddev. Trend: up if ≤ 0.5h SD, down if > 1.0h SD, stable otherwise.

---

### Activity section

Inputs: step_count (SUM), active_energy (SUM), apple_exercise_time (SUM).

**Steps score:**

| Steps recent vs baseline | Points |
|---|---|
| Within −10% or better | +2 |
| Within −30% | +1 |
| Below −30% | +0 |

**Active calories score:** same thresholds as steps.

**Exercise time score:**

| Recent avg exercise | Points |
|---|---|
| ≥ 30 min/day | +2 |
| ≥ 15 min/day | +1 |
| < 15 min/day | +0 |

Max possible: 6 points (if all three metrics available).

| Ratio | Status |
|---|---|
| ≥ 0.70 | good |
| ≥ 0.40 | fair |
| < 0.40 | low |

---

### Heart & Lungs section

Inputs: blood_oxygen_saturation (AVG), vo2_max (AVG), respiratory_rate (AVG).

**SpO₂ score** [[11]](#ref-11) [[12]](#ref-12):

| Recent avg SpO₂ | Points |
|---|---|
| ≥ 95% | +2 |
| ≥ 92% | +1 |
| < 92% | +0 |

> The 95% threshold matches the WHO action threshold [[12]](#ref-12). The 92–95% partial-credit band reflects the BTS guideline floor: below 92% is hypoxaemia for most adults, while 92–95% indicates reduced respiratory reserve [[11]](#ref-11).

**VO₂ Max score** [[13]](#ref-13) [[14]](#ref-14):

| VO₂ Max % change vs baseline | Points |
|---|---|
| > −3% | +2 |
| > −8% | +1 |
| ≤ −8% | +0 |

> VO₂ max is recognised by the AHA as a clinical vital sign [[14]](#ref-14). A 122 000-patient study found each 1-MET improvement in CRF reduces all-cause mortality risk by ~13% [[13]](#ref-13). Consumer wearable VO₂ estimates have ±10–15% error vs. lab measurement — trend tracking is more reliable than absolute values.

**Respiratory rate score** [[15]](#ref-15):

| Recent avg RR | Points |
|---|---|
| 12–20 br/min (normal) | +2 |
| 10–24 br/min (borderline) | +1 |
| Outside 10–24 | +0 |

> 12–20 br/min is the universally agreed adult resting normal (StatPearls, ALA, Cleveland Clinic) [[15]](#ref-15).

Max possible: 6 points.

| Ratio | Status |
|---|---|
| ≥ 0.70 | good |
| ≥ 0.40 | fair |
| < 0.40 | low |

---

## Headline signal (cross-metric)

The briefing surfaces **one** prominent signal at the top of the response in
`response.headline`. Single-metric "all good" verdicts in the presence of
multiple stress signals are a known clinical failure mode (Meeusen 2013
[[16]](#ref-16), Plews 2014 [[18]](#ref-18)) — the headline forces the
multi-signal convergence to be visible.

Computed in `internal/health/headline.go::computeHeadline`. Priority order:

### Priority 1 — Stress (multi-signal converging evidence)

Fires when **≥ 2** of the following stress markers are simultaneously true:

| Signal | Threshold | Evidence |
|---|---|---|
| RHR elevation | today − baseline ≥ **+5 bpm** | Wearable HF decompensation 2025 [[30]](#ref-30): nocturnal RHR rise > 5 bpm doubled CV hospitalisation/mortality risk. Below this is dominated by device noise (Dial 2025 [[31]](#ref-31)). |
| Sleep debt | today < **6.5 h** | Functional debt zone — below AASM ≥ 7 h [[8]](#ref-8) but above U-curve worst-case (< 5 h) [[20]](#ref-20). |
| HRV depression | z-score ≤ **−1.0** vs personal baseline | 1 SD below baseline; matches the dynamic-threshold approach already used in `scoreRecovery` (Beattie 2024 [[6]](#ref-6), Plews 2014 [[18]](#ref-18)). |
| Sleep fragmentation | awake_today > **0.5 h** | Same threshold used by `scoreSleep` for the consolidated-sleep bonus. |

> **Why ≥ 2 signals?** Single-marker triggers have too many benign explanations
> (alcohol, late workout, stressful day at work). Two or more *simultaneous*
> markers is the recognised early-overreaching / early-illness pattern
> (Mishra 2024 [[21]](#ref-21), persistent COVID changes 2025 [[32]](#ref-32)).

When a stress headline fires, the **coherence pass** (see below) downgrades a
"good" Recovery section to "fair" with an explanatory summary, and caps
Readiness label at "Fair" (max 65) regardless of the numeric score from the
weighted formula.

### Priority 2 — Sleep debt (single signal)

Fires when sleep < 6.5 h but no other stress markers — purely informational
("watch that this doesn't become a pattern"). Coherence pass caps Readiness
at 65: per Walker 2017 and Watson 2015 [[8]](#ref-8) sleep debt cannot be
"erased" by one good HRV/RHR night, so an Optimal verdict is unwarranted.

### Priority 3 — Largest single |z-score| deviation

When nothing rises to "stress" or "sleep_debt", the headline shows the metric
that deviates most from personal baseline. Mapping:

| |z-score| range | Severity |
|---|---|
| ≥ 1.5 | warning |
| 0.5 – 1.5 | info |
| < 0.5 | "stable" headline (positive framing) |

Direction sets `severity = positive` for HRV↑ / RHR↓ / sleep↑.

### Headline payload

Every headline carries `metrics: HeadlineMetricDelta[]` with concrete numbers
(today's value, baseline, absolute Δ, percent Δ, z-score, unit) so the UI and
AI briefing can render specific phrases like *"RHR 68 bpm, +6 vs your norm 62"*
instead of *"in normal range"* (Altini 2021 [[4]](#ref-4): personal-baseline
deltas are the recommended display).

---

## Coherence pass

After all sections + readiness are computed, `applyCoherencePass` reconciles
the headline with section verdicts so the briefing is **internally consistent**:

| Headline | Action |
|---|---|
| `stress` | Recovery `good` → `fair` with `rec_summary_fair_stress`. Readiness label capped at "Fair" (score ≤ 65). |
| `sleep_debt` | Readiness label capped at "Fair" (score ≤ 65). |
| `single_deviation` / `stable` | No changes. |

> **Why cap, not zero out?** A capped score retains useful gradation between
> "borderline" and "very bad" while preventing the dashboard from declaring
> Optimal during converging stress. This is the explicit recommendation of
> Plews 2014 [[18]](#ref-18) for HRV-guided training decisions: when multi-marker
> signals diverge, defer to the conservative interpretation.

---

## Energy Bank v1 (shipping — to be replaced by v2)

> **Status:** v1 is what currently runs in `internal/health/energy.go`. Cross-user empirical validation revealed structural problems (saturation on typical days, no multi-day carryover, formula squashing); a redesign — Energy Bank v2 — is documented in `ENERGY_BANK.md` and summarised in the next section. v1 stays here as the operational reference until v2 ships.

Computed in `internal/health/energy.go::computeEnergyBank`, returned in
`response.energy_bank`. Where the Headline answers *"what's notable today?"*,
the Energy Bank answers *"what should you do?"* — a prescriptive verdict that
rolls capacity, observed activity load, and autonomic stress into a single
plain-language action ("push_hard", "moderate", "active_recovery", "rest").

### Pipeline

1. **Capacity** (0–100, fixed at briefing time) = readiness score, which
   already blends sleep + HRV + RHR with the U-shaped duration penalty
   (Watson 2015 [[8]](#ref-8), Plews 2014 [[18]](#ref-18), Walker 2017).
   We re-use the readiness number rather than deriving an analogous one — at
   morning they should be the same, and any drift between them would be more
   confusing than helpful.

2. **Strain** (0–100) — ACWR-flavoured load (Gabbett 2016 [[36]](#ref-36)):
   ```text
   strain = clamp01( 0.5 * (steps_today / steps_chronic_28d)
                   + 0.5 * (active_energy_today / kcal_chronic_28d) ) * 100
   ```
   Today's partial sums come from `hourly_metrics` using the same
   preferred-source pick (Apple Watch > iPhone > best other) as
   `buildDailyMetricCol`, so the intraday numerator stays consistent with the
   chronic denominator computed over `daily_scores`. Chronic 28d excludes
   today (so an active morning doesn't bias its own baseline). Ratio 0.8–1.3
   is the classic "sweet spot" Gabbett ties to lower injury risk; >1.5 is the
   spike zone. The codebase deliberately stops short of an injury-prediction
   claim — ACWR is a *vocabulary* for verdict gating, not a prediction model
   (Impellizzeri 2023 critique).

3. **Stress** (0–100) — one-sided z-scores of RHR (positive only) and HRV
   (negative only) against personal baseline:
   ```text
   rhr_z   = max(0,  zScore(rhr_today, baseline, sd))
   hrv_z   = max(0, -zScore(hrv_today, baseline, sd))
   stress  = clamp01( (rhr_z + hrv_z) / 2 ) * 100
   ```
   Only the *bad* direction of each axis matters — a low RHR or high HRV is a
   recovery signal, not stress. Z-score normalisation against personal
   baseline is the consensus approach for individualised wearable analytics
   (Beattie 2024 [[6]](#ref-6), Dial 2025 [[31]](#ref-31)).

4. **Drain** with allostatic-load multiplier:
   ```text
   drain   = strain * (1 + 0.5 * stress/100)        # up to 1.5x at stress=100
   current = max(0, capacity - drain)
   ```
   The same active day costs more when autonomics are already taxed — this
   is the McEwen 1998 / 2024 PNAS allostatic-load shape [[38]](#ref-38),
   conservatively parameterised. Without intraday HR we cannot model
   parasympathetic *refill*; the system therefore monotonically drains
   through the day. Documented as `// TODO(v2)` in `energy.go`.

### Verdict thresholds (Plews / Vesterinen smallest-worthwhile-change band)

The verdict gates primarily on the *raw* HRV z-score; the bank balance acts as
a secondary clamp.

| Condition | Verdict |
|---|---|
| `hrv_z ≤ -1.0` OR `current ≤ 25` | `rest` |
| `hrv_z ≤ -0.5` OR `current ≤ 45` | `active_recovery` |
| `hrv_z ≥ +0.5` AND `current ≥ 60` | `push_hard` |
| otherwise | `moderate` |

> **Why ±0.5 SD?** Plews 2014 [[18]](#ref-18), Vesterinen 2016 [[37]](#ref-37)
> and Düking 2023 [[39]](#ref-39) all use a ±0.5 SD "smallest worthwhile
> change" band as the decision boundary for HRV-guided training prescription.
> Within ±0.5 SD a wearable HRV reading isn't reliably distinguishable from
> normal day-to-day noise; outside it, the deviation is large enough to
> warrant adjusting load. The −1.0 SD rest threshold matches
> Vesterinen's overreaching-prevention rule.

### Coherence with the Headline

When a stress headline fires (≥ 2 converging stress markers, see "Headline
signal" above), `push_hard` is forced down to `active_recovery` regardless of
the numeric verdict. This mirrors the headline → recovery-section coherence
pattern: a multi-signal converging-evidence verdict (Meeusen 2013 [[16]](#ref-16))
is stronger than any single-marker green light.

### Edge cases

- **< 9 days of HRV/sleep** → returns `nil`, the briefing simply omits the
  field. UI hides the widget via `omitempty`. Same gate as readiness.
- **No partial-day activity yet** (early morning before any wearable sync)
  → `strain = 0`; verdict is then driven by capacity and stress alone.
- **Single huge workout** that pushes ratio > 3 → `strain` clamps to 100;
  verdict ends up at `rest` via the `current ≤ 25` clamp. Correct outcome.
- **Stale data** (`d.Sleep[0] <= 0` despite ≥ 9 days history) → returns
  `nil`. Better to show nothing than a verdict from yesterday's data.

---

## Energy Bank v2 (design — pending implementation)

> Full specification: [`ENERGY_BANK.md`](./ENERGY_BANK.md). This section captures
> the methodology and the empirically-derived design principles; the spec doc
> carries the implementation detail (schema, concurrency, rollout phases).

Three structural problems with v1 were discovered through an empirical
prototype run on 90 days of real data, on **two distinct users** (one
sedentary with clean data, one mixed-source with frequent sync gaps):

| v1 problem | Symptom on real data | Root cause |
|---|---|---|
| **Saturation / squashing** | `capacity` pinned at 100 on 28/31 typical days; `bank_eod` never below 18 | `clamp01(today_load / 28d_chronic) × 100` treats a typical day as maximum strain. Ratio = 1.0 → strain = 100. |
| **No multi-day carryover** | A 5-night sleep deficit gets fully erased by one good night because v1 has no state across days | Capacity is set fresh from readiness each morning — yesterday's residual is discarded |
| **Linear restore overshoots** | Even after `bank_eod = 3` (real bad day), one normal night returns user to ~100 the next morning | `restore = sleep_total × efficiency × quality_weight` is additive in absolute units; saturates at the ceiling |

### Core model — asymptotic restore (Garmin Body Battery family)

Replaces the additive `capacity = bank_yesterday + restore` with a
contraction-mapping form:

```
sleep_quality ∈ [0, 1] = duration_factor · efficiency_factor · structure_factor
   duration_factor   = clamp01(sleep_total_h / 8)
   efficiency_factor = sleep_total / (sleep_total + sleep_awake)
   structure_factor  = max(0.5, 1.0 − max(0, 0.15 − deep_pct)
                                    − max(0, 0.20 − rem_pct))   # ≤ 1.0

capacity_today = bank_yesterday + (100 − bank_yesterday) · sleep_quality
bank_eod       = clamp(capacity_today − drain_today, −50, 100)
```

Three properties this gives that the additive form did not:

1. **Multi-night sleep deficit accumulates.** A 3-night stretch of poor sleep
   drags the bank down progressively because each morning starts from a lower
   asymptotic anchor. Validated empirically: a single bad night (5.4 h, drain
   62) keeps the bank capped at 73/100 the next morning even after a normal
   7.1 h sleep.

2. **Capacity never pins at the ceiling.** Across 90 simulated days, capacity
   hit 100 zero times (v1 pinned 28/31). The bank distribution gains a real
   lower tail — p10 = 17 vs 35 in additive — making the chart actually
   informative.

3. **`structure_factor` only penalises, never rewards above 1.0.** The
   asymptote already provides the upper bound; bonus weights would just chase
   a ceiling that already exists. The `[0.5, 1.0]` range is intentional, not
   a clamp artefact.

### Drain — additive, calorie-only at v2.0

```
drain = α · active_energy_kcal     # α ≈ 0.08 at v2.0 launch
      + β · max(0, HR_avg − RHR_baseline) · duration_min   # β = 0 in v2.0
```

The `β`-term schema is reserved from day one (lives in `components` JSONB)
because it covers the *illness/stress* signal kcal misses: high RHR with
normal calories (fever, acute stress) should drain. Activated in v2.2 when
HR-per-hour reads land. v2.0 ships with calories alone — sufficient for
non-athletic profiles per the prototype.

### Signed bank with floor at −50

The bank in DB is stored signed (so AI receives "you're 15 in the hole"
distinguishable from "you're at 0") but rendered clamped to `[0, 100]` in
the API. The signed floor is `−50`: beyond that the integration is noise,
not signal. The cross-user prototype confirmed this — one tenant's bank
ran to −54 during a missing-data cascade, validating the need for a
symmetric clamp.

### Bootstrap — seed-erasing forward iteration

Asymptotic restore is a contraction mapping; any seed gets exponentially
forgotten through the `(100 − bank) · sleep_quality` factor. Empirically,
seeds {10, 50, 90} converge to within 0.005 by day 7 and bit-identical by
day 9. This means **no seed state is persisted**: on every recompute,
walk forward through the last 14 days of history starting from
`bank = 50.0`. By the time iteration reaches "today", the seed is
mathematically forgotten.

This single mechanism handles every edge case cleanly: new user
post-warmup, existing user at v2.0 launch, formula-version bump,
short/medium data gaps, and recovery from `stale` state — no
special-case branches in code.

### Missing-data handling — trailing-average imputation

Cross-user prototype revealed that `sq=0` on missing-sleep days causes
catastrophic bank collapse (drain still applies, restore is suppressed —
intermediate days integrate to deep negative territory). Fix:

```
if sleep_metrics missing for day d:
   sq[d] = avg(sleep_quality over last 7 days with valid, NON-IMPUTED data)
   flags |= 'imputed_sleep'
```

The **non-imputed-only** lookback is critical — without it, after a few
imputed days the trailing average is computed mostly from previously
imputed values, creating a feedback loop that drifts the bank to a
constant. Imputation tracks the validity flag through the iteration.

### Three-state trust model

Imputation isn't all-or-nothing. The signal degrades smoothly with gap
size, and so should user trust:

| State | Trigger | Render | AI usage |
|---|---|---|---|
| `fresh` | ≤ 1 imputed day in last 7 | normal, no flags | Used in RECOMMENDATION |
| `estimated` | 2–4 consecutive imputed days **or** 2–4 in 14d window | dotted-line graph + "estimated" badge | Used with prompt hedge "based on 7d trailing pattern" |
| `stale` | ≥ 5 consecutive imputed days **or** > 7 in 14d window | bank not rendered; "No recent data" placeholder | Not used; AI pivots to "sync your watch" advice |

Two parallel triggers (`consecutive` and `proportion`) catch two
different failure modes: a clean stretch of bad sync vs scattered gaps
that accumulate (one tenant in the prototype had 23 % scattered gaps —
a single-threshold rule wouldn't have caught it).

After exiting `stale`, the bank enters a 3-day `recovering` state computed
via fresh forward-iteration with `seed = 50`, ignoring frozen pre-stale
snapshots. 5+ days without data could mean illness, travel, burnout or
sensor failure — re-bootstrap is safer than blind continuation.

### Personalisation methodology (v2.5)

v2.5 adds per-user `α_factor` calibration. The mechanism is principled
rather than gradient-based: any tuning of the formula based on its own
output is overfitting. We need an **external signal the formula doesn't
see** — *next-morning HRV at lag = +1*. If the formula is calibrated
for a user, `bank_eod[d]` should correlate positively with `hrv[d+1]`
(low bank → poor recovery → tomorrow's autonomic markers reflect it).

```
on a 30-day rolling window:
   r = pearson(bank_eod[d], hrv[d+1])
   
   data-quality preflight:
     if missing_data_pct > 10%: skip calibration, alert "check sensor sync"
   
   sign requirement:
     if r < 0: alert "formula doesn't fit this user", do NOT auto-tune
   
   magnitude rubric:
     r < 0.2  → wait, do not auto-tune
     0.2..0.3 → narrow tune: α_factor ∈ [0.8, 1.2]   ← prevents edge-solution
     0.3..0.5 → full grid:   α_factor ∈ [0.5, 1.5]
     ≥ 0.5    → strong signal: α_factor ∈ [0.4, 1.7]
```

**Three guardrails** caught silently in the prototype:

1. **RHR sign-flip on daily aggregates.** The original v2.5 plan included
   RHR as a second signal; on real data the sign was inverted (low bank
   correlated with *low* next-day RHR — vagal rebound after exertion +
   Apple Watch's retrospective RHR aggregation timing). Dropped from
   personalisation; reintroduction requires morning-only RHR from
   `metric_points`, not the daily aggregate.

2. **Edge-solution defence.** Unconstrained grid search on this user's
   data preferred α = 0.04 (half of base), gaining Δr = 0.019. Edge
   solutions in optimisation almost always indicate misspecification, not
   personalisation. The narrow-tune band `[0.8, 1.2]` clamps marginal-r
   tunes so a tiny statistical signal can't trigger a large formula
   shift. Only when r ≥ 0.3 does the full search range open.

3. **Data-quality preflight separates two failure modes.** "Formula
   doesn't fit this user" (model issue, alert and review) and "We lack
   the data to know" (sensor issue, alert user) both produce weak
   correlations but require different actions. Conflating them is the
   default rubric mistake.

### Validation methodology — empirical, cross-user, pre-implementation

The full v2 design was validated against real data **before any code was
committed**. The validation rule is: every architectural decision must
be defensible against at least two distinct user profiles. Six bugs
were caught and design-changed pre-commit:

| Bug | Caught by |
|---|---|
| Squashing (v1 ceiling pin) | 31-day Lyosha simulation |
| Missing multi-day carryover | 31-day simulation showed only 2-day declines; 90-day produced 15 cascades up to 5 days deep |
| RHR sign-flip in personalisation | Lyosha proto-personalisation with both signals |
| Unconstrained grid search edge solution | Lyosha grid showing optimum at α = 0.04 boundary |
| Missing-data formula collapse | Mariia 90-day simulation (21/90 missing-sleep days produced 19 false "battery=0" verdicts) |
| Single-threshold staleness misses scattered gaps | Mariia gaps scattered, not consecutive — a `consecutive`-only rule wouldn't have caught 23 % missing-data rate |

The cross-user pattern is the load-bearing methodology choice. A formula
that works on one well-behaved dataset always works "on paper". Only a
second user with different physiology and different data quality
exposes the assumptions the first user happened to satisfy. This
methodology is to be repeated for every future Energy Bank revision.

### References (v2-specific)

- **Garmin Body Battery** (2018+) — popularised the asymptotic
  state-machine design where rest charges by `(100 − current) · rate`.
  No formal whitepaper, but the mechanism is reverse-engineerable from
  Garmin's documentation and is the canonical implementation of the
  pattern adopted here.
- **Banach fixed-point theorem** — formal basis for the
  contraction-mapping property used in seed-erasing bootstrap. The
  empirical convergence test (3 seeds, exponential gap decay) is the
  applied validation of this theorem on real data.
- **HRV residual analysis as personalisation signal** — Plews 2014
  [[18]](#ref-18) and Vesterinen 2016 [[37]](#ref-37) underpin the
  use of HRV deviation against personal baseline as the gold-standard
  external-signal residual for adaptive training prescription.

---

## Overall status

Aggregates the statuses of all available sections.

| Condition | Overall |
|---|---|
| 2 or more sections are "low" | low |
| fair + low sections > good sections | fair |
| otherwise | good |

---

## Metric cards (top row)

5 cards: Steps, Sleep, HRV, Resting HR, Respiratory Rate.

Each card shows:
- **Value**: **today's** value (`vals[0]`, most recent day)
- **Trend %**: `(today − baseline) / baseline × 100`, rounded to 1 decimal
  where baseline = `avg(all 30 days)`
- **Trend label**: positive if > +3%, negative if < −3%, neutral otherwise

---

## Activity vs Recovery chart (Correlation)

Shows the last **7 days** where both step count and HRV data exist on the same calendar day.

- **Load axis (left, %)**: daily step count normalized to the week's max steps
  Formula: `steps_day / max_steps_in_week × 100`
- **HRV axis (right, ms)**: daily average HRV in milliseconds

Only days where **both** metrics have data are plotted. If fewer than 2 matching days exist, the chart is hidden.

> ⚠️ Note: this chart shows correlation visually but does not compute a statistical correlation coefficient.

---

## Weekly Insights (up to 3)

### Insight 1 — Step consistency
Counts how many of the last 7 days had steps ≥ the 30-day average.

- ≥ 5 days → positive: "You hit your average step count on N of the last 7 days"
- < 5 days → warning: "Only N of 7 days above your average steps"

Requires: ≥ 7 days of step data.

### Insight 2 — HRV resilience after hard days
For each day in the overlapping steps/HRV window:
- A day is "high activity" if steps > 1.2 × 30-day average
- The **following day's** HRV is checked against the 30-day HRV average
- If following-day HRV < 95% of average → counted as "HRV dropped"

Array indexing: both slices are most-recent-first. Index `i` = today, `i+1` = yesterday.
Code checks `steps[i+1]` (yesterday was high-activity) → checks `hrv[i]` (today's HRV dropped).

Requires: ≥ 2 high-activity days in the window.

- If > half of high-activity days caused an HRV drop → warning
- Otherwise → positive

> Post-exercise HRV suppression typically lasts 48–72 hours and is a documented marker of autonomic stress [[1]](#ref-1). Chronic suppression (multiple high-activity days with persistent HRV drop) is associated with overreaching syndrome [[16]](#ref-16).

### Insight 3 — Sleep on active vs rest days
Splits the last 30 days into "active" (steps > average) and "rest" days, compares average sleep duration.

- Active sleep avg > rest sleep avg + 0.5 h → positive
- Rest sleep avg > active sleep avg + 0.5 h → warning
- Within 0.5 h → no insight generated

Requires: ≥ 7 days of both steps and sleep data, and at least 1 day in each category.

### Insight 4 — Overtraining warning
**High priority** — inserted at the front of the insight list so it always survives the 3-insight cap.

- Condition: `Activity status == "good"` AND `Readiness score < 50`
- Message: "Your activity is high despite signs of exhaustion. Risk of overtraining is elevated." (type: warning)

---

## Sleep Analysis panel

Shows averages over the last **3 nights**:

| Field | Calculation |
|---|---|
| Deep sleep | avg(sleep_deep last 3 days) — in hours |
| REM sleep | avg(sleep_rem last 3 days) — in hours |
| Awake time | avg(sleep_awake last 3 days) — in hours |
| Efficiency | `(total_sleep − awake) / total_sleep × 100` — green if ≥ 85% |

---

## Multi-device deduplication (all SUM metrics)

When two wearables (e.g. Apple Watch + RingConn) both record the same day, raw `SUM` would double-count. **All SUM metrics** (steps, calories, sleep, exercise time, distance, etc.) use a **MAX-per-source** pattern:

```sql
SELECT MAX(source_sum)
FROM (
    SELECT date, source, SUM(qty) AS source_sum
    FROM metric_points WHERE metric_name = ? ...
    GROUP BY date, source
)
GROUP BY date
```

This picks the device with the highest daily total for each day, avoiding double-counting. Applied consistently in: `GetHealthBriefing`, `GetMetricData`, `GetDashboard`, `GetSleepSummary`, `GetReadinessHistory`, `GetLatestMetricValues`, `BuildDailyMetrics`.

SUM metrics are defined in `internal/storage/aggregates.go::SumMetrics`: step_count, active_energy, basal_energy_burned, apple_exercise_time, apple_stand_time, flights_climbed, walking_running_distance, time_in_daylight, apple_stand_hour, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake.

---

## Health Alerts (anomaly detection)

Alerts are **not score components** — they flag potential health issues based on statistical deviations from personal baselines. They appear in the `alerts` field of the briefing response.

### Respiratory rate anomaly [[21]](#ref-21) [[22]](#ref-22)

Triggers when the 7-day average RR deviates **> 2 SD** from the baseline (days 8+). Requires ≥ 9 days of RR data.

RR elevation often appears **1–2 days before** other markers during infection. Mishra et al. (2024) achieved 95% specificity for respiratory infection detection using a multi-signal algorithm (HR, RR, HRV). Used as an alert rather than a score component because false positives can be triggered by exercise, poor sleep, stress, and alcohol.

### Wrist temperature anomaly [[23]](#ref-23) [[24]](#ref-24)

Triggers when the 7-day average wrist temperature deviates **> 2 SD** from the baseline. Requires ≥ 9 days of data.

Temperature anomalies as small as 0.4°C over 2–3 weeks can be detected with < 10% error rate (Fuller et al. 2024, n = 63,153). Useful for early fever/inflammation detection. Not all users have Apple Watch wrist temperature data — the alert gracefully degrades if data is unavailable.

### HRV coefficient of variation (CV) [[18]](#ref-18) [[19]](#ref-19)

Triggers when the 7-day HRV CV exceeds **15%**, suggesting autonomic instability or overreaching.

The CV of the 7-day rolling average is a strong indicator of maladaptation to training load (Plews et al. 2012, 2014). High CV indicates inconsistent recovery patterns even when the average HRV looks normal. Used as a supplement to the HRV sub-score, which only evaluates the mean.

---

## Known limitations

1. **Readiness requires 9 days minimum** — with fewer days, baseline is too thin to be meaningful. Score defaults to 70 (neutral) if a component has insufficient data.

2. **Section windows differ by design** — Recovery uses 7-day recent / days 8+ baseline (matches Readiness); Sleep / Activity / Cardio use 3-day recent / all-30-day baseline. The 3-day window makes these sections more responsive to acute changes, while the full-window baseline is acceptable because they use point-based or absolute-value scoring rather than ratio-based.

3. **Recovery % = Readiness score** — they are literally the same number, displayed twice (score and progress bar).

4. **Correlation chart is visual only** — no statistical significance is computed.

5. **No absolute step threshold** — the step goal is the personal 30-day average, not a fixed 10,000. Intentional.

6. **Exercise time** — `apple_exercise_time` counts minutes in the Apple Watch Exercise ring, not total movement time.

7. **Readiness History `valsBefore` sorts by date** — an earlier version sorted by metric value, which put the highest-HRV days first and inflated "recent" averages. Fixed: now sorts by calendar date descending.

8. **Consumer SpO₂ accuracy** — consumer pulse oximeters overestimate SpO₂ by 2–3% in people with darker skin pigmentation [[17]](#ref-17). The 92% threshold may represent true saturation of ~89–90% for affected users.

9. **Consumer VO₂ max accuracy** — wearable VO₂ max estimates have ±10–15% error vs. laboratory testing. Trend direction is more reliable than absolute values.

10. **Sleep regularity** — requires ≥ 7 days of sleep data. With fewer days, no regularity bonus or display is shown.

11. **Missing days are excluded, not zero-filled** — if a metric has no data for a given day, that day is skipped entirely in the averaging arrays. This prevents NULL days from artificially depressing scores. Both the `daily_scores` cache path and the `metric_points` fallback path use this approach consistently.

12. **Respiratory rate and wrist temperature not in Readiness** — these are strong illness markers but are only used in the Cardio section card, not in the main Readiness formula. Adding them could improve early illness detection but would also add noise for healthy users.

13. **Timezone changes (travel)** — day boundaries are determined by `substr(date, 1, 10)` which uses the device's local time at recording. When travelling across timezones, Apple Watch/iPhone automatically update the offset (e.g. `+0100` → `+0800`). This means travel days may appear "compressed" (fewer hours when flying east) or "stretched" (more hours when flying west), causing artificially low step counts, calories, and sleep duration for those days. Readiness scores may dip temporarily due to the distorted data. The system self-corrects once 1–2 full days pass in the new timezone. A future improvement could detect mixed timezone offsets within a single day and flag it as a travel day.

---

## References

<a id="ref-1"></a>[1] Zulfiqar U et al. (2010). Relation of high heart rate variability to healthy longevity. *American Journal of Cardiology*, 105(8), 1181–1185. — Post-exercise HRV suppression, 72h recovery window. [PubMed](https://pubmed.ncbi.nlm.nih.gov/20381670/)

<a id="ref-2"></a>[2] Gabbett TJ (2016). The training—injury prevention paradox: should athletes be training smarter and harder? *British Journal of Sports Medicine*, 50(5), 273–280. — Acute:Chronic Workload Ratio, 7-day acute window. [PMC7047972](https://pmc.ncbi.nlm.nih.gov/articles/PMC7047972/)

<a id="ref-3"></a>[3] WHOOP Inc. HRV & Recovery Score Methodology. — "HRV will always be the single biggest factor in your recovery score." [whoop.com](https://www.whoop.com/us/en/thelocker/episode364-hrv-cv-explained/)

<a id="ref-4"></a>[4] Altini M (2021). On Heart Rate Variability (HRV) and readiness. *Medium / HRV4Training*. — Personal baseline approach; HRV weighted ~2× RHR. [hrv4training.com](https://www.hrv4training.com/blog2/daily-score-baseline-and-normal-range-an-overview)

<a id="ref-5"></a>[5] Oura Ring (2023). Readiness Score documentation. — RHR as a major readiness contributor. [ouraring.com](https://ouraring.com/blog/readiness-score/)

<a id="ref-6"></a>[6] Beattie K et al. (2024). Heart Rate Variability Applications in Strength and Conditioning: A Narrative Review. *PMC*. — Individual baseline comparisons preferred over population norms. [PMC11204851](https://pmc.ncbi.nlm.nih.gov/articles/PMC11204851/)

<a id="ref-7"></a>[7] Gisselman AS et al. (2026). Monitoring Training Adaptation and Recovery Using HRV via Mobile Devices. *MDPI Sensors*, 26(1). — RMSSD as primary autonomic recovery signal. [MDPI](https://www.mdpi.com/1424-8220/26/1/3)

<a id="ref-8"></a>[8] Watson NF et al. (2015). Recommended Amount of Sleep for a Healthy Adult. *Sleep*, 38(6), 843–844. — AASM/SRS joint consensus: ≥7 hours for adults. [PMC4513271](https://pmc.ncbi.nlm.nih.gov/articles/PMC4513271/)

<a id="ref-9"></a>[9] Van Dongen HPA et al. (2003). The Cumulative Cost of Additional Wakefulness. *Sleep*, 26(2), 117–126. — Cognitive deficits at 6h vs 8h sleep over 14 days. [PubMed](https://pubmed.ncbi.nlm.nih.gov/12603781/)

<a id="ref-10"></a>[10] Huang T et al. (2024). Sleep regularity and major health outcomes: a systematic review and meta-analysis. *Sleep Health*, 10(1), 7–14. — Sleep regularity index predicts all-cause & CV mortality more strongly than mean duration (HR 0.97 vs 0.99). [PMC10782501](https://pmc.ncbi.nlm.nih.gov/articles/PMC10782501/)

<a id="ref-11"></a>[11] British Thoracic Society (2017). BTS Guideline for oxygen use in adults in healthcare and emergency settings. — SpO₂ <92% is clinically significant; 94–98% target range for most adults. [BTS](https://www.brit-thoracic.org.uk/quality-improvement/guidelines/emergency-oxygen/)

<a id="ref-12"></a>[12] WHO (2011). Pulse Oximetry Training Manual. — Action threshold SpO₂ < 95%. [WHO PDF](https://cdn.who.int/media/docs/default-source/patient-safety/pulse-oximetry/who-ps-pulse-oxymetry-training-manual-en.pdf)

<a id="ref-13"></a>[13] Kokkinos P et al. (2018). Exercise Capacity and Mortality in Older Men. *JACC*, 71(9), 964–975. — 122 000-patient study: each MET improvement ↓ all-cause mortality ~13%. [JACC](https://www.jacc.org/doi/10.1016/j.jacc.2018.06.045)

<a id="ref-14"></a>[14] Ross R et al. (2016). Importance of Assessing Cardiorespiratory Fitness in Clinical Practice. *Circulation*, 134(24), e653–e699. — AHA recommendation to treat VO₂ max as clinical vital sign. [AHA](https://www.ahajournals.org/doi/10.1161/CIR.0000000000000461)

<a id="ref-15"></a>[15] Physiology, Respiratory Rate. *StatPearls*, NCBI Bookshelf. — Normal adult RR = 12–20 br/min. [NBK537306](https://www.ncbi.nlm.nih.gov/books/NBK537306/)

<a id="ref-16"></a>[16] Meeusen R et al. (2013). Prevention, Diagnosis, and Treatment of the Overtraining Syndrome. *Medicine & Science in Sports & Exercise*, 45(1), 186–205. — HRV as overtraining/overreaching biomarker; parasympathetic withdrawal pattern. [PubMed](https://pubmed.ncbi.nlm.nih.gov/23247672/)

<a id="ref-17"></a>[17] Sjoding MW et al. (2020). Racial Bias in Pulse Oximetry Measurement. *NEJM*, 383(25), 2477–2478. — Consumer oximeters overestimate SpO₂ by 2–3% in darker skin tones. [NEJM](https://www.nejm.org/doi/10.1056/NEJMc2029240)

<a id="ref-18"></a>[18] Plews DJ et al. (2014). Monitoring Training with Heart Rate Variability: How Much Compliance Is Needed for Valid Assessment? *Int J Sports Physiol Perform*, 9(5). — 7-day rolling average is the consensus window for HRV-guided training; CV of 7-day Ln(RMSSD) as overreaching indicator. [PubMed](https://pubmed.ncbi.nlm.nih.gov/24334285/)

<a id="ref-19"></a>[19] Plews DJ et al. (2012). Heart Rate Variability in Elite Triathletes. *Eur J Appl Physiol*. — Validated 7-day rolling mean and CV as fatigue/adaptation markers in competitive athletes. [PubMed](https://pubmed.ncbi.nlm.nih.gov/22367011/)

<a id="ref-20"></a>[20] Li M et al. (2025). Imbalanced Sleep Increases Mortality Risk by 14–34%: A Meta-Analysis. *GeroScience*. — 79 cohort studies: short sleep (<7h) → HR 1.14; long sleep (≥9h) → HR 1.34 (U-shaped curve). [DOI](https://doi.org/10.1007/s11357-025-01592-y)

<a id="ref-21"></a>[21] Mishra T et al. (2024). Detection of Common Respiratory Infections Using Consumer Wearable Devices in Health Care Workers. *JMIR Form Res*, 8:e53716. — Multi-signal algorithm (HR, RR, HRV): 43% sensitivity, 95% specificity for respiratory infections within 7 days of symptom onset. [PMC11292157](https://pmc.ncbi.nlm.nih.gov/articles/PMC11292157/)

<a id="ref-22"></a>[22] Chung YS et al. (2024). Smartwatch-Based Algorithm for Early Detection of Pulmonary Infection. *BMC Med Inform Decis Mak*. — RR as "the most important predictor of patients' prognosis" in clinical deterioration. [PMC11512465](https://pmc.ncbi.nlm.nih.gov/articles/PMC11512465/)

<a id="ref-23"></a>[23] Fuller D et al. (2024). Utilizing Wearable Device Data for Syndromic Surveillance: A Fever Detection Approach. *Sensors*, 24(6):1818. — n = 63,153 participants; wearable temperature detects fever onset in acute infectious disease. [DOI](https://doi.org/10.3390/s24061818)

<a id="ref-24"></a>[24] Grant CC et al. (2020). Monitoring Skin Temperature at the Wrist in Hospitalised Patients May Assist in the Detection of Infection. *Intern Med J*, 50(7). — Temperature anomalies as small as 0.4°C detectable. [PubMed](https://pubmed.ncbi.nlm.nih.gov/31908128/)

<a id="ref-25"></a>[25] Pereira LA et al. (2026). Monitoring Training Adaptation and Recovery Status in Athletes Using Heart Rate Variability via Mobile Devices: A Narrative Review. *Sensors*, 26(1), 3. — RMSSD remains the recommended metric; strong agreement between ECG and consumer wearables; 7-day rolling average recommended. [DOI](https://doi.org/10.3390/s26010003)

<a id="ref-26"></a>[26] Scully D et al. (2025). Investigating the Accuracy of Apple Watch VO₂ Max Measurements: A Validation Study. *PLOS ONE*, 20(5):e0323741. — Apple Watch underestimates VO₂ max by 6.07 mL/kg/min (MAPE 13.31%). [DOI](https://doi.org/10.1371/journal.pone.0323741)

<a id="ref-27"></a>[27] Choe S et al. (2025). Apple Watch Accuracy in Monitoring Health Metrics: A Systematic Review and Meta-Analysis. *Physiol Meas*, 46(4). — Comprehensive accuracy assessment across metrics. [PubMed](https://pubmed.ncbi.nlm.nih.gov/40199339/)

<a id="ref-28"></a>[28] Zhang D et al. (2017). Resting Heart Rate and All-Cause, Cardiovascular, and Cancer Mortality: A Systematic Review and Dose–Response Meta-Analysis. *Nutr Metab Cardiovasc Dis*, 27(6):504–517. — 87 studies: each 10 bpm RHR increase → 17% higher all-cause mortality. [PubMed](https://pubmed.ncbi.nlm.nih.gov/28552551/)

<a id="ref-29"></a>[29] *Apple Watch vs WHOOP vs Oura vs Garmin: What the Science Actually Says* (2025 review of consumer wearable readiness scores). — Proprietary readiness/recovery algorithms differ by 20+ points on identical input data; lack independent validation across devices. Underpins this codebase's choice to keep every threshold open and research-cited. [Medium](https://medium.com/@CuriousCatalyst/apple-watch-vs-whoop-vs-oura-vs-garmin-what-the-science-actually-says-76055e9de930)

<a id="ref-30"></a>[30] *Non-Invasive Wearable Technology to Predict Heart Failure Decompensation* (2025), MDPI *J Clin Med*, 14(20):7423. — ZOLL LifeVest cohort: nocturnal RHR rise > 5 bpm more than doubled CV hospitalisation and mortality risk. Source for the +5 bpm absolute threshold in the cross-metric stress flag. [DOI](https://doi.org/10.3390/jcm14207423)

<a id="ref-31"></a>[31] Dial MB et al. (2025). Validation of nocturnal resting heart rate and heart rate variability in consumer wearables. *Physiological Reports*, e70527. — ECG-validation across Garmin Fenix 6, Oura Gen 3 / 4, Polar Grit X Pro, Whoop 4.0. Oura highest accuracy for both RHR and HRV; Whoop moderate; Polar/Garmin poor. Establishes the device noise floor that motivates absolute (not just %) RHR thresholds. [PMC12367097](https://pmc.ncbi.nlm.nih.gov/articles/PMC12367097/)

<a id="ref-32"></a>[32] *Automatic detection of persistent physiological changes after COVID infection via wearable devices* (2025). *Scientific Reports* (Nature). — Multi-signal pattern (elevated nightly HR + reduced HRV) detects long-COVID at the population level; reinforces the converging-evidence rule used in the headline. [DOI](https://doi.org/10.1038/s41598-025-15208-0)

<a id="ref-33"></a>[33] *Sleep Regularity and Mortality: A Prospective Analysis in the UK Biobank* (eLife 2025). — n ≈ 88 000 adults; SRI=41 (5th percentile) → HR 1.53 vs median; SRI=75 (95th percentile) → HR 0.90. Provides the percentile-based tier thresholds for the planned SRI implementation (Этап 2). [eLife 88359](https://elifesciences.org/articles/88359)

<a id="ref-34"></a>[34] *Comparison of Sleep Regularity Index scores calculated by open-source packages and implications for outcomes research: rationale and design of the RIRI statement* (Sleep journal 2026, zsaf299). — Two widely used SRI implementations diverge enough on the same data to flip clinical conclusions; RIRI is a 14-item reporting standard. Implementing SRI here will pin to the Phillips & Czeisler 2017 original formula and document accordingly. [Sleep journal](https://academic.oup.com/sleep/article-abstract/49/4/zsaf299/8265987)

<a id="ref-35"></a>[35] Banister EW (1991). *Modeling elite athletic performance.* In Physiological Testing of the High-Performance Athlete (Human Kinetics). — Original TRIMP (Training Impulse) framework; load = duration × HR-reserve weight. Foundation of all modern training-load metrics. [Reference](https://us.humankinetics.com/products/physiological-testing-of-the-high-performance-athlete-2nd-edition)

<a id="ref-36"></a>[36] Gabbett TJ (2016). *The training–injury prevention paradox: should athletes be training smarter and harder?* *British Journal of Sports Medicine*, 50(5), 273–280. — Acute:Chronic Workload Ratio: today's load (acute) ÷ rolling 4-week chronic average. Sweet spot 0.8–1.3, danger > 1.5. Source for the 28-day chronic denominator in Energy Bank's strain calculation. [PMC7047972](https://pmc.ncbi.nlm.nih.gov/articles/PMC7047972/)

<a id="ref-37"></a>[37] Vesterinen V et al. (2016). *Individual endurance training prescription with heart rate variability.* *Med Sci Sports Exerc*, 48(7), 1347–1354. — RCT: HRV-guided prescription (drop intensity when HRV is below personal baseline) outperforms predefined-block training. Foundation for the verdict thresholds gated on HRV z-score. [PubMed](https://pubmed.ncbi.nlm.nih.gov/26909527/)

<a id="ref-38"></a>[38] McEwen BS (1998). *Stress, adaptation, and disease: Allostasis and allostatic load.* *Annals NYAS*, 840(1), 33–44. Updated as McEwen et al. (2024) in *PNAS*, *Allostatic load: a 30-year review* — same physiological response costs more when the system is already taxed. Source for the stress-amplifies-strain multiplier in Energy Bank drain. [DOI](https://doi.org/10.1111/j.1749-6632.1998.tb09546.x)

<a id="ref-39"></a>[39] Düking P et al. (2023). *Heart Rate Variability-Guided Endurance Training: A Systematic Review and Meta-Analysis.* *Sports Med — Open*, 9, 67. — Updated meta-analysis confirms moderate effect for HRV-guided training vs predefined; ±0.5 SD smallest-worthwhile-change band remains the recommended threshold. [DOI](https://doi.org/10.1186/s40798-023-00614-3)
