# Readiness Redesign — Phase 0 Results

Snapshot of the empirical state after Phase 0 storage + four sub-score
writers landed and were exercised against the full historical record.
Read alongside `READINESS_REDESIGN_PLAN.md` (architecture/decisions)
and `READINESS_REDESIGN_BACKFILL_RUNBOOK.md` (ops procedure).

Companion to plan §4–§6: this doc captures **what we learned by
running**, separate from what we decided in advance.

## 1. Operational status

Production endpoint `POST /api/admin/readiness-redesign/backfill` has
been exercised end-to-end on `health.dzarlax.dev`.

| Item | State |
|---|---|
| Schema health (`/api/admin/status → redesign_storage`) | `healthy: true` across all runs |
| Backfill range covered | `2021-01-01 → 2026-05-15` (1961 days) |
| Sub-scores backfilled | Recovery Stability, Passive Efficiency, Acute Risk, Chronic Load |
| Target snapshot rows | **15 688** = 1961 days × 4 sub-scores × 2 target_kinds (Acute+Chronic carry 2 each; Recovery+Passive carry 2 each) |
| Feature snapshot rows | **7 844** = 1961 × 4 sub-scores |
| Naive baseline rows | per (sub_score × target_kind × baseline_kind × date); written only for eligible labels |
| Idempotency | full re-runs of all four writers preserve row counts; only `computed_at` advances |
| `source_epoch=unknown` rows | **0** across all 15 688 target rows |

Total runtime for a single full-history pass of all four writers
(two chunks, 2021-2023 + 2024-2026): ~3.5 minutes including network
hops, exercised via the production endpoint.

## 2. Row counts by eligibility (full history)

### Recovery Stability

| target_kind | eligible | reason | n |
|---|---|---|---|
| daily_point | ✓ | `ok` | 827 |
| daily_point | ✓ | `ok_awake_structural_zero` | 63 |
| daily_point | ✗ | `sleep_data_missing` | 536 |
| daily_point | ✗ | `sleep_total_out_of_range` | 213 |
| daily_point | ✗ | `coarse_only_source` | 322 |
| rolling_3d | ✓ | `ok` | 650 |
| rolling_3d | ✗ | `sleep_data_missing` | 896 |
| rolling_3d | ✗ | `sleep_total_out_of_range` | 298 |
| rolling_3d | ✗ | `coarse_only_source` | 117 |

Value distribution (eligible only): efficiency mean 0.94, SD 0.05.

### Passive Efficiency

| target_kind | eligible | reason | n |
|---|---|---|---|
| daily_point | ✓ | `ok` | 1235 |
| daily_point | ✗ | `no_walking_hr` | 726 |
| rolling_3d | ✓ | `ok` | 891 |
| rolling_3d | ✗ | `no_walking_hr` | 1070 |

Value distribution (eligible only): walking_hr mean 101.7 bpm, SD 7.89.
Distribution matches the original audit exactly (audit mean 101.7 ± 7.9).

### Acute Risk

| target_kind | eligible | reason | n | positive |
|---|---|---|---|---|
| event_t1_t3 | ✓ | `ok` | 1482 | 407 (27.5%) |
| event_t1_t3 | ✗ | `baseline_warmup` | 337 | 0 |
| event_t1_t3 | ✗ | `event_window_data_missing` | 142 | 0 |
| event_strict_t1_t3 | ✓ | `ok` | 1482 | 34 (2.3%) |
| event_strict_t1_t3 | ✗ | `baseline_warmup` | 337 | 0 |
| event_strict_t1_t3 | ✗ | `event_window_data_missing` | 142 | 0 |

Strict is 12× sparser than OR — confirms the post-review fix to compute
strict base rate from strict labels independently (PR #93).

### Chronic Load

| target_kind | eligible | reason | n | positive |
|---|---|---|---|---|
| chronic_label | ✓ | `ok` | 469 | 82 (17.5%) |
| chronic_label | ✗ | `baseline_warmup` | 1191 | 0 |
| chronic_label | ✗ | `event_window_data_missing` | 301 | 0 |
| chronic_acute_density | ✓ | `ok` | 386 | 295 (76.4%) |
| chronic_acute_density | ✗ | `baseline_warmup` | 1191 | 0 |
| chronic_acute_density | ✗ | `event_window_data_missing` | 384 | 0 |

`baseline_warmup` dominates because Chronic Load needs ≥ 30 eligible
Recovery `rolling_3d` rows in the current source_epoch. The 2024 gap
year wipes out most of 2024 and bleeds into Q1-2025; pre-2025 epochs
fill in gradually as Recovery history accumulates.

## 3. Source epoch catalogue (finalized in this phase)

Three epochs live in `source_epochs`:

| epoch_id | range | description |
|---|---|---|
| `initial` | 2014-01-01 → 2023-12-31 | Pre-2024 ingest method. HRV + walking_heart_rate_average present from Apple Watch via HAE. |
| `source_2024_gap` | 2024-01-01 → 2024-12-31 | walking_heart_rate_average AND HRV absent across the entire year. RHR partially present. Source/ingest method anomaly, likely HAE config change. |
| `source_2025_current` | 2025-01-01 → NULL | Post-gap epoch. walking_heart_rate_average and HRV resumed. 180-day rolling baselines for Acute Risk and Passive Efficiency reset at this boundary. |

All 15 688 target rows resolve to one of these three; the
`SentinelSourceEpoch = "unknown"` fallback never triggered.

## 4. Discovered data issues (empirical, surfaced by Phase 0)

These are issues we did not know about before running the writers
end-to-end on production data.

### 4.1 2024 HRV gap (new finding)

`daily_scores.hrv_avg` is NULL for **all 366 days of 2024**, even
though `daily_scores.rhr_avg` is partially present (275 days). Same
root cause as the previously-known 2024 walking_heart_rate_average
gap — likely a HAE config change. Now captured as
`source_2024_gap` epoch so neither Acute Risk nor Passive Efficiency
treats the gap as physiology.

### 4.2 2022-09 and 2023-12 mini-gaps in walking_hr

18 days each. Not the same scale as 2024 but worth flagging in
follow-up source-epoch curation. Recovery and Acute Risk are
unaffected; only Passive Efficiency reports `no_walking_hr` for those
weeks.

### 4.3 2021 sleep was coarse-only

322 daily Recovery rows and 117 rolling_3d Recovery rows are
`coarse_only_source` ineligible, concentrated in 2021 and early 2022.
This is the period when sleep data came from RingConn / iPhone Sleep
Schedule without stage tracking, before the Apple Watch became the
primary sleep source. The writer correctly distinguishes these from
real out-of-range nights.

### 4.4 `sleep_total_out_of_range` was overloaded (fixed)

The first version of Recovery Stability reported `sleep_total_out_of_range`
for two distinct cases: physiologically-short nights (nap, all-nighter)
AND fully-missing source rows (Total IS NULL). PR #92 split the
nil-Total case into a new `sleep_data_missing` reason. Net effect:

- daily_point `sleep_total_out_of_range` went from 749 to 213 (real out-of-range only)
- 536 daily rows reclassified to the new `sleep_data_missing` bucket
- rolling_3d analogously: 1194 → 298 + 896 reclassified
- eligibility outcome unchanged; only reason text differs. `formula_version` bumped 1 → 2.

## 5. Calibration warnings for Phase 1

Empirically observed thresholds that look mis-tuned on the actual
distribution. None are bugs — Phase 0 just emits honest labels — but
each is a Phase 1 retune candidate.

| Signal | Observed | Concern |
|---|---|---|
| Acute Risk OR base rate | 27.5% across full history | Stable across years (except 2024 gap) at ~27–30%. Reasonable for "daily HRV/RHR fluctuation" but probably too noisy for a "did something acute happen?" alert. |
| Acute Risk strict base rate | 2.3% | Sparse enough to be operationally informative. Calibration for precision@fixed-recall is hard at this rate — requires stratified bootstrapping when Phase 1 evaluates models. |
| Chronic density positive rate | **76.4% of eligible days** | Threshold ≥3 acute OR-events in a 14-day window is too low given the 27.5% OR base rate. Expected events per window = 14 × 0.275 = 3.85, above threshold by construction. Phase 1 should retune to ≥5–6 events. |
| Chronic label positive rate | 17.5% | Plausible for a sustained-deterioration signal. Hold for Phase 1 floors before retuning. |
| 2022 strict event rate | 5.7% vs. 1–2% in other years | Possibly real physiological pattern (illness/stress period) worth investigating. Not a Phase 0 fix — flag for Phase 1 narrative review. |

## 6. Open questions to revisit in Phase 1

1. **Baseline floors before models.** Compare persistence / 7d-mean /
   30d-mean / EWMA45 baselines on a time-based hold-out for each
   continuous target (Recovery + Passive 3d-roll). Calibrate
   event_base_rate and recency-decayed baselines for Acute + Chronic
   classifiers. Only after these floors are characterised should we
   decide whether any model is worth training.

2. **Chronic density threshold retune.** With observed 76% positive
   rate at threshold ≥3, retune to a value that keeps the label
   informative (probably ≥5 or ≥6 within 14 days). Bump
   `chronicLoadFormulaVersion` when applied; re-backfill via the
   admin endpoint.

3. **Source-epoch catalogue refinement.** The 2022-09 and 2023-12
   walking_hr mini-gaps and the 2021 coarse-only sleep period are
   candidate epoch boundaries. They are not currently codified;
   downstream models will see those as physiological drift unless we
   add them.

4. **2022 strict spike investigation.** 5.7% strict-event rate that
   year vs. 1–2% elsewhere. This could reflect an illness cluster,
   lifestyle change, or sensor artifact. Worth a manual narrative
   review before feeding into trained models.

5. **`event_window_data_missing` at bleeding edges.** Every daily
   backfill leaves the last 3-14 dates ineligible because future
   windows are not yet observable. This is by design (no false-
   negatives), but the live pipeline should re-emit those dates as
   they become observable. Likely a small follow-up where ingest
   triggers an Acute/Chronic re-pass over the trailing N days.

6. **Athletic Readiness still dormant.** No structured workouts beyond
   the 102 Walking entries from the HAE iOS app; Apple Health XML
   import does not parse `<Workout>` elements yet. Plan §4.4 tracks
   this as a parallel-track unblock. Empirically not blocking Phase 1
   for the other four sub-scores.

## 7. Method-of-record summary (what works, verified)

The following invariants were end-to-end verified against production
data — not just unit tests:

- **Per-candidate baselines exclude the candidate itself** (Acute Risk
  + Chronic Load both use `windowStatsBefore`, exclusion property
  exercised by integration tests + label distributions match
  expectation).
- **Source-epoch clipping resets baselines at boundaries** (Acute Risk
  paired-warmup count starts from zero at 2025-01-01 and takes ~75
  days to release — exactly the expected behaviour for the 30-paired
  warmup gate within the new epoch).
- **Window observability gates protect against false-negatives**
  (Acute Risk + Chronic Load both refuse to emit `eligible=0` labels
  when forward windows are incomplete; bleeding edge of every backfill
  lands as `event_window_data_missing` rather than fabricated 0).
- **Distinct target_kind rows for the two label variants on each
  classifier** (Acute Risk OR vs. strict; Chronic Load primary vs.
  acute-density). Strict ≠ OR base rate (12× difference), primary ≠
  density base rate (4× difference) — confirmed independent
  computation, no cross-contamination.
- **Idempotent backfill** across all four sub-scores. Re-runs preserve
  row counts; only `computed_at` advances.
- **Schema health stays `healthy: true`** through the full backfill
  cycle including formula_version bumps and epoch catalogue updates.

## 8. Next action

Phase 1 starts with **baseline floors**, not models. See §6 item 1.
Documented separately when Phase 1 begins.
