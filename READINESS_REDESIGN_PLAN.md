# Readiness Redesign — Plan

Status: planning. No code changes yet. This document is the agreed scope before Phase 0 begins.

## 0. Why this document exists

The current `readiness` score (`SCORING.md` → `readiness = HRV×0.40 + RHR×0.30 + Sleep×0.30`)
was empirically tested against future physiology on the full historical dataset.
Result: **today's readiness has essentially zero correlation with tomorrow's HRV, RHR or sleep**.
Same-day internal consistency is fine; forward predictive power is not.

This is not a bug fix. The score is methodologically misnamed: it works as a
*current-state descriptor*, not as a *forecast of readiness for load*. The redesign
addresses that gap, not the weights.

A parallel finding: `EnergyBank` (formula v1, n=387 paired days) is **wrong-signed**
against next-day HRV (r=−0.149) and RHR (r=+0.212). The concept is not broken, but
its target is mis-specified — it likely captures *productive strain*, not
*recovery deficit*.

## 1. Empirical facts that constrain the design

From the feasibility audit (2021-01-01 → 2026-05-15):

| Target | Eligible days | AR(1) daily | AR(1) 3-day rolling | Status |
|---|---|---|---|---|
| sleep_efficiency | 1463 | 0.233 | **0.584** | feasible_now |
| walking_hr_apple (Apple `walking_heart_rate_average`) | 1236 | 0.282 | **0.503** | feasible_now |
| acute_deviation_event (HRV OR RHR > 1.5σ) | 1261 | event rate 12.3% (155 events) | — | feasible_now (classification) |
| acute_deviation_strict (HRV AND RHR same day) | 1261 | 15 events | — | collect_more / silent_only |
| chronic_load (sustained_hr_load, ACWR-style) | 3257 | — | — | feasible_now as features |
| workout_hr_residual | **11** in DB | — | — | **importer gap, not data absence** — see §1.1 |
| hrv_daily as target | 1435 | 0.07–0.13 | ~0.4 | descriptive_only on daily |
| rhr_daily as target | 1435 | 0.16 | — | descriptive_only on daily; use as feature |

Intersection of HRV + sleep + walking_hr: **889 days (45% of calendar)** — the core training/test pool.

Anomalies to handle explicitly:
- **2024 ingest gap**: `walking_heart_rate_average` missing entirely; `sleep_efficiency` mean 0.97 vs 0.90 prior. Source/method change. Tagged as its own `source_epoch`; exclusion from training is downstream choice (see §3.4).
- **Baseline drift across years**: HRV 37–43 ms range, RHR 62–68 bpm range. Fixed baselines smear physiology.
- **`minute_metrics` does not store `walking_speed`** — minute-level walking segment path is closed. Use the Apple-computed daily `walking_heart_rate_average` instead.

### 1.1 Apple Health XML import does not parse `<Workout>` elements

The "only 11 usable workouts in DB" finding is **not** a data absence — it is an
importer gap. The audit was misread initially; this subsection corrects it.

`internal/applehealth/parse.go` is a streaming XML decoder that handles only
`<Record>` elements (HK quantity/category types via `hkTypeMap`). The
top-level `<Workout>` tag — and its children `<WorkoutStatistics>`,
`<WorkoutEvent>`, `<WorkoutRoute>`, `<MetadataEntry>` — is **silently
ignored**. As a result, `make import FILE=export.zip` drops every structured
workout in the user's Apple Health archive.

The 102 Walking workouts currently in the `workouts` table all came in via
the separate `/health/workouts` endpoint (`internal/handler/workouts.go`),
which receives JSON from the Health Auto Export iOS app. That endpoint
started populating data only from 2026-02-13 onward.

Inspection of a representative `export.zip` (149 MB compressed, 2.4 GB
`export.xml`, 2026-05-16) yielded:

| Workout type | Count |
|---|---|
| HKWorkoutActivityTypeWalking | **757** |
| HKWorkoutActivityTypeCycling | 15 |
| HKWorkoutActivityTypeSwimming | 7 |
| HKWorkoutActivityTypeRunning | **6** |
| HKWorkoutActivityTypeHiking | 4 |
| HKWorkoutActivityTypeFunctionalStrengthTraining | 4 |
| HKWorkoutActivityTypeDownhillSkiing | 4 |
| Underwater Diving / Equestrian / Cross Training | 1 each |
| **Total** | **800** |

Distribution by year: 2019:8 / 2020:4 / 2021:30 / 2022:52 / 2023:68 /
**2024:275 / 2025:234 / 2026:129**. 185 entries carry GPX route files
(outdoor with GPS track).

Per-workout payload is rich: `duration`, `<WorkoutStatistics>` for
`DistanceWalkingRunning` (sum), `ActiveEnergyBurned`, `BasalEnergyBurned`;
`<MetadataEntry>` for `HKAverageMETs`, `HKWeatherTemperature`,
`HKWeatherHumidity`, `HKIndoorWorkout`, `HKElevationAscended`, `HKTimeZone`;
`<WorkoutEvent>` segments for pause/resume markers; `<WorkoutRoute>`
reference for GPX file.

**Heart-rate coverage**:
- 170 entries have inline `<WorkoutStatistics type="HKQuantityTypeIdentifierHeartRate" average=… minimum=… maximum=…>` (modern iOS 15+ format).
- The remaining ~630 entries do not carry inline HR statistics, but the underlying `heart_rate` records *are* already in `metric_points`. HR for those workouts is reconstructable as a windowed aggregate over the workout's `startDate`..`endDate` interval — exactly what the HAE handler already does via `heartRateData` for live ingest.

**Practical implications**:

1. **Walking is the only type with sufficient volume for residual modeling.** Cycling 15, Running 6 — far below any sane n for per-type residual regression. Athletic Readiness on these data is **Walking residual**, not multi-modal.
2. **Walking residual partly overlaps with Passive Efficiency.** Passive Efficiency uses Apple's daily `walking_heart_rate_average` aggregate. Walking residual would be per-event with external load features (distance, duration, pace, elevation, weather, indoor). Sharper resolution, same physiological channel.
3. **Possible cross-finding for the 2024 anomaly**: 2024 has 275 walking workouts in the export but zero `walking_heart_rate_average` daily aggregates in the DB. Re-importing XML may or may not recover the daily aggregate (Apple may have stopped emitting it that year). Worth checking after XML workout import is wired (see §9.6).

## 2. Architectural decision: from readiness-first to recovery-first

The audit invalidated the original framing. On this dataset the project cannot
be a *readiness* system — the structured-workout ground truth that "readiness"
needs is empirically absent. What the data **does** support, with measurable
signal, is a **daily recovery + passive efficiency** system. Athletic readiness
remains a valid concept but becomes a future module, not the centerpiece.

Three layers, ordered by operational importance on the available data:

```
Core daily layer (flagship — what the user sees first):
  1. Recovery Stability      → 3d-rolling sleep_efficiency / WASO / fragmentation
  2. Passive Efficiency      → 3d-rolling walking_heart_rate_average

Risk layer (parallel signals, different time horizons):
  3. Acute Risk              → HRV/RHR/sleep tail events, t+1..t+3, silent until calibrated
  4. Chronic Load            → sustained_hr_load + ACWR-style continuous features

Optional athletic layer (dormant until data exists):
  5. Performance Readiness   → workout HR residual, activates when structured workouts accumulate
```

| # | Sub-score | Primary target | Phase 0 status |
|---|---|---|---|
| 1 | Recovery Stability | 3d-rolling `sleep_efficiency`; secondary: WASO, fragmentation | active writer |
| 2 | Passive Efficiency | 3d-rolling `walking_heart_rate_average` | active writer |
| 3 | Acute Risk | composite tail event in `t+1..t+3` (HRV < base − 1.5σ OR RHR > base + 1.5σ); strict variant tracked separately | active writer, silent mode |
| 4 | Chronic Load | sustained deterioration in Recovery Stability (≥1σ drop for ≥5 days in `t+1..t+14`); see §4.2 | features-only writer in Phase 0; target labels written for backfill |
| 5 | Performance Readiness | walking HR residual per event (after XML import gap is fixed) | writer present, currently emits `unknown:importer_gap`; activates after §4.4 milestone |

**Why no Athletic Readiness in the flagship.** Two reasons:
1. The structured workout history exists in Apple Health (800 entries
   across 2019–2026 per §1.1) but `applehealth/parse.go` does not parse the
   `<Workout>` element, so it never reaches the DB. Until that gap is fixed
   the sub-score has nothing to compute.
2. Even after the import gap is closed, **Walking is the only type with
   enough volume** (757 of 800). Running has 6 entries, Cycling 15. So
   Athletic Readiness on this dataset is effectively Walking residual, which
   shares its physiological channel with Passive Efficiency
   (`walking_heart_rate_average`). The differentiation is resolution:
   Passive Efficiency = one daily aggregate; Walking residual = per-event
   with external load context (distance, pace, elevation, weather, indoor).

The sub-score is allocated, its writer is present and emits
`eligible=false, reason='importer_gap'` until §4.4 closes the gap. Whether it
then becomes a distinct sub-score or a secondary target_kind under Passive
Efficiency is an open call recorded in §9.5.

**Why daily *point* targets are not the default.** Daily AR(1) on these
metrics is 0.07–0.28 — most of the daily signal is measurement noise. The 3-day
rolling window lifts AR to 0.50–0.58, which is the actual signal floor models
have to beat. Targets are therefore **3-day rolling values or event-window
probabilities**, not single-day points. This is not cosmetic smoothing; it is
where predictability begins.

**Why no aggregate "readiness" number.** Each sub-score answers a different
question (does the next night recover well? was today's cardiac cost typical?
is this week trending toward overload? is something acute happening now?).
Collapsing them into a weighted average re-introduces the exact failure mode the
redesign is built to escape: a single score with no defined target. The UI
layer presents dimensions and a rule-based decision layer; it does not present
a sum.

## 3. Methodological rules (binding)

These apply to every sub-score, no exceptions.

### 3.1 Eligibility gate per target

Every target write must carry:
- `target_value` (NULL if ineligible)
- `eligible` (bool)
- `eligibility_reason` (enum: `ok`, `insufficient_sleep_coverage`, `no_walking_segments`, `hrv_sparse`, `device_off`, `baseline_warmup`, `data_anomaly_2024`, …)
- `data_coverage` (per-source coverage metrics that drove the decision)

`unknown` is a legitimate output. Missing-data days are **not** imputed in
Phase 0–1. Imputation systematically biases evaluation precisely on the days
that matter (travel, illness, device off).

### 3.2 Time windows: no leakage

A single anchor is fixed for every sub-score to remove ambiguity about what
"day `t`" means in the presence of nights crossing midnight and same-day
target windows.

**Feature anchor** — features for row dated `t` are computed from data
available up to the **end of local day `t`** (operationally: a write that
fires once per day, after the day rolls over in the tenant's `REPORT_TZ`).
No data points from `t+1` or later are read by the feature writer for the
row dated `t`. Sleep that begins on the night of `t` and ends on the
morning of `t+1` is **assigned to night `t`** for feature purposes (it
belongs to the day that just ended), but **excluded** from any target
window that opens at `t+1`.

| Layer | Window | Anchor |
|---|---|---|
| Features for row dated `t` | data ≤ end of local day `t` | end of `t` |
| Recovery Stability target (3d rolling) | nights `t+1 → t+2`, `t+2 → t+3`, `t+3 → t+4` (mean efficiency over the three nights) | strictly after `t` |
| Recovery Stability target (next-night) | night `t+1 → t+2` | strictly after `t` |
| Passive Efficiency target (3d rolling) | days `t+1..t+3` walking_hr mean | strictly after `t` |
| Acute event target | window `t+1..t+3` | strictly after `t` |
| Chronic load target | composite event in `t+1..t+14` (TBD, see §9.2) | strictly after `t` |
| Walking workout residual | per-event with `event.start_time > t` | strictly after `t` |

No same-window features. No retrospective features. No shuffled CV.
Time-based hold-out only. Any writer that would need to read data after the
end of day `t` to compute features for row `t` is a bug — features lag
targets by at least one day boundary.

### 3.3 Baselines must be adaptive

Fixed-window personal baselines (30-day or longer) smear multi-month drift.
Phase 0 captures features against an **EWMA baseline** plus a separate
**slow** baseline for chronic drift detection. The acute vs slow gap is
itself a feature.

**Decision (Phase 0 defaults, fixed):**

- Adaptive EWMA baseline: **45 days** effective window, applied uniformly to
  all metrics that need an acute personal baseline.
- Slow baseline: **180 days** effective window, applied uniformly to all
  metrics that need a chronic personal baseline.

These are starting defaults, not claims of optimality. Phase 1 may retune
them only through a versioned change — a metric-specific override requires
an explicit `feature_version` bump and is documented at the writer site.
This forecloses the ability to silently re-fit history by tweaking window
sizes; any tune is a logged migration.

### 3.4 Baselines must be source-epoch aware

A baseline that adapts to *anything that changes* will silently absorb
ingest artifacts as if they were physiology. The 2024 anomaly — `walking_heart_rate_average`
missing entirely, `sleep_efficiency` mean jumping from 0.90 to 0.97 — is the
canonical example: source/method change, not body change.

Every observation is tagged with a `source_epoch` identifier. Baselines
(EWMA and slow) are computed **within an epoch** and reset at epoch
boundaries. Cross-epoch comparisons are flagged, not silently averaged.

**Phase 0 minimum** for what `source_epoch` carries:
1. **Manually curated date ranges** in the `source_epochs` table for known
   transitions (initial seed: at least 2024-01-01 and 2025-01-01 boundaries
   suggested by the audit; more get added as they are recognised).
2. **Auto-detected distribution shifts** on the eligible series per
   sub-score (running-mean shift; KS test on a sliding window). Detections
   land as unconfirmed rows in `source_epochs` and surface for manual
   confirmation before being honoured by baselines.
3. **Importer version tag** (`xml_v1`, `xml_v2_with_workouts`, `hae_v23`,
   etc.) attached to each ingested row at write time, so a re-import or
   importer change creates a recognisable epoch even when nothing else
   changed.

**Deferred** until evidence shows they are extractable and useful:
device firmware versions, Apple Health source-set fingerprints, sensor
identity. The XML records do carry `device="…hardware:Watch6,2,software:7.3…"`
strings, so these are recoverable, but parsing and curating them is not on
the Phase 0 critical path.

The same concept applies to user-side regime changes (long illness,
significant fitness phase, pregnancy) — those are separate `physiology_epoch`
markers maintained by the user, not auto-detected. Epochs are explicit,
auditable, and reversible.

### 3.5 Cold-start for rare-event models (Acute Risk)

Acute risk lands as a **silent classifier** for the first 6–12 months of
production: writes predictions to DB, never surfaces to UI. Calibration metric:
**precision at recall = 0.5**, with stratified bootstrap CI over the small
event set. Goes live only when out-of-time precision is stable across two
consecutive quarters.

### 3.6 No aggregate UI score

The dashboard presents per-dimension chips:

```
Passive efficiency:   normal | strained | strong | unknown
Recovery stability:   stable | at risk  | strong | unknown
Chronic load:         low    | balanced | elevated | unknown
Acute risk:           clear  | alert    | unknown
(Athletic readiness: deferred)
```

A rule-based decision layer sits on top and outputs operational verdicts
(e.g. *avoid hard workout*, *easy movement only*, *rest focus*). The rules
are explicit and inspectable; they are **not** weighted averages.

### 3.7 ACWR is a feature, not a threshold

Acute:chronic workload ratio is included as a continuous feature for Chronic
Load. No `ACWR > 1.5 → injury risk` claim. The literature around that
threshold has known statistical artifacts (mathematical coupling).

## 4. Phase 0 — Collect-the-targets

Goal: stop being unable to validate. Every day, write the targets we **could**
predict, the eligibility verdict, the feature snapshot, and naive baselines.
**No models are trained.** This is infrastructure for honest evaluation later.

### 4.1 Deliverables

1. **`target_snapshots` table**, one row per (date × sub-score × target_kind):
   ```
   date            DATE
   sub_score       TEXT  -- 'recovery_stability' | 'passive_efficiency' | 'chronic_load' | 'acute_risk' | 'athletic_readiness'
   target_kind     TEXT  -- 'rolling_3d' (primary) | 'daily_point' (secondary) | 'event_t1_t3' (acute) | 'wo_residual' (athletic)
   target_value    REAL  -- nullable; for event targets stored as 0/1
   eligible        BOOL
   eligibility_reason TEXT
   data_coverage   JSONB
   source_epoch    TEXT  -- ingest/device epoch tag (see §3.4)
   computed_at     TIMESTAMPTZ
   formula_version INT
   ```

2. **`feature_snapshots` table**, one row per (date × sub-score):
   ```
   date            DATE
   sub_score       TEXT
   features        JSONB  -- timestamped strictly before target window opens
   source_epoch    TEXT
   feature_version INT
   computed_at     TIMESTAMPTZ
   ```

3. **`naive_baselines` table**, one row per (date × sub-score × target_kind × baseline_kind):
   ```
   date            DATE
   sub_score       TEXT
   target_kind     TEXT
   baseline_kind   TEXT  -- 'persistence_yesterday' | 'rolling_7d_mean' | 'rolling_30d_mean' | 'ewma_30d' | 'event_base_rate'
   predicted_value REAL
   source_epoch    TEXT
   ```

4. **`source_epochs` table** (small, hand-curated + auto-detected):
   ```
   epoch_id        TEXT PRIMARY KEY
   start_date      DATE
   end_date        DATE  -- nullable for current
   kind            TEXT  -- 'source_epoch' | 'physiology_epoch'
   description     TEXT
   detected_by     TEXT  -- 'manual' | 'distribution_shift'
   confirmed       BOOL
   ```

5. **Backfill job**: compute the above for the full eligible history
   (2021-01-01 → today). 2024 is included but tagged with its own
   `source_epoch`; it is not silently dropped — its exclusion from training
   sets is a downstream choice, not a write-time filter.

6. **Daily writer**: extend the existing daily aggregation pipeline to emit
   these four tables every day going forward.

### 4.2 Sub-score writers in Phase 0

Primary target is **3-day rolling** for the two flagship sub-scores. Daily
point is written as a secondary target_kind so baseline floors can be compared
across horizons, but it is not the optimization target.

| Sub-score | Primary target | Secondary target(s) | Eligibility threshold | Baselines emitted |
|---|---|---|---|---|
| Recovery Stability | 3d-rolling `sleep_efficiency` over nights `t+1..t+3` (per §3.2 anchor) | next-night efficiency; WASO; fragmentation | see §4.2.1 (refined sleep eligibility) | persistence, 7d mean, 30d EWMA (per source_epoch) |
| Passive Efficiency | 3d-rolling `walking_heart_rate_average` over days `t+1..t+3` (per §3.2 anchor) | daily value at `t+1` | metric present AND value ∈ [50, 180] bpm in each of the three target days | persistence, 7d mean, 30d EWMA (per source_epoch) |
| Chronic Load | **sustained deterioration in Recovery Stability**: 3d-rolling `sleep_efficiency` falls below its source-epoch 45d EWMA baseline by >1σ for ≥5 days within window `t+1..t+14` (binary label) | secondary: `acute_events_14d ≥ 3` count-based label (analysis feature, not primary) | both Recovery Stability eligibility AND ≥30 eligible Recovery rows of warmup in current source_epoch | base rate; recency-decayed base rate; persistence of prior Chronic Load label |
| Acute Risk | composite event `(HRV < base − 1.5σ) OR (RHR > base + 1.5σ)` in window `t+1 .. t+3` | strict event `(HRV drop AND RHR spike same day)` in same window | HRV and RHR baselines warmed up (≥30d history within source_epoch) | event base rate; recency-decayed base rate |
| Athletic Readiness | dormant writer — emits `eligible=false, eligibility_reason='importer_gap'` until §4.4 lands, then `'walking_only'` for non-Walking dates | — | activates per-event after Walking residual feasibility lands (see §4.4 + §9.5) | — |

#### 4.2.1 Sleep eligibility — `missing_awake_unknown` vs `awake_structural_zero`

A naive eligibility rule like `sleep_awake IS NOT NULL` silently drops a
known-good class of Apple Watch nights. The upstream hourly cache
(`buildHourlyMetric` in `internal/storage/aggregates.go`) filters
`qty > 0`, so a night with `sleep_awake = 0` arrives at the daily layer
with the awake row **absent**, not with `awake = 0`. This is documented in
CLAUDE.md as the rationale for the 4-of-5-stages exception in the sleep
source-validation gate.

Recovery Stability eligibility must distinguish these two cases:

| Pattern | Interpretation | eligibility_reason | Target |
|---|---|---|---|
| `sleep_total ∈ [4,14]` AND `sleep_asleep > 0` AND `sleep_awake > 0` | normal night | `ok` | `asleep / (asleep + awake)` |
| `sleep_total ∈ [4,14]` AND `sleep_asleep > 0` AND `sleep_awake` row **absent** AND source is `Apple Watch` (or HAE Apple Watch ingest fingerprint) AND `asleep ≈ sleep_total` within tolerance | structural zero — no waking detected by Watch | `ok_awake_structural_zero` | `1.0` (efficiency = 1) |
| `sleep_total ∈ [4,14]` AND `sleep_asleep > 0` AND `sleep_awake` row absent AND source is **not** Apple Watch, OR `asleep` and `sleep_total` disagree | genuinely unknown awake | `missing_awake_unknown` | NULL (ineligible) |
| `sleep_total ∉ [4,14]` | physiologically implausible or missing | `sleep_total_out_of_range` | NULL (ineligible) |
| `sleep_asleep = 0` AND `sleep_total > 0` | coarse-only source (no staging) | `coarse_only_source` | NULL (ineligible for primary efficiency; secondary targets may still apply) |

The `awake_structural_zero` branch can only collapse to `eligible=ok` when
we are confident the missing awake row is a filter artifact and not a
silent gap. The two signals supporting that confidence are: (a) the source
matches a known Apple Watch fingerprint where `qty > 0` strips genuine
zeros, and (b) `sleep_asleep` (sum of `deep + rem + core` and/or
`unspecified` per the CLAUDE.md gate) is within ~2% of `sleep_total` for
the night. Both conditions must hold; otherwise default to
`missing_awake_unknown`.

Secondary targets follow the same eligibility rules with their own reasons
(`waso_requires_segments`, `fragmentation_requires_segments`) — they may
land as ineligible even when primary efficiency is OK.

### 4.3 Out of scope for Phase 0

- Any predictive model.
- Any change to the existing `daily_scores.readiness` value or `energy_*` columns. The new system runs alongside.
- Any UI change. The chips/decision layer are Phase 2.
- `EnergyBank` redesign. Treated separately (see §7).

### 4.4 Parallel track — Apple Health XML import gap closure

Independent of the four sub-score writers above, the Apple Health XML import
path needs a small, self-contained patch to start ingesting the structured
workouts already present in users' Apple Health archives (see §1.1 for the
audit details).

Scope:

1. Extend `internal/applehealth/parse.go` to recognise the `<Workout>` token
   alongside the existing `<Record>` handling. Mirror the JSON shape that
   `internal/handler/workouts.go::haeWorkout` already accepts so the
   downstream `storage.Workout` writer is unchanged.
2. Inside each `<Workout>` block, collect: top-level attributes
   (`workoutActivityType`, `duration`, `durationUnit`, `startDate`,
   `endDate`, `sourceName`, `device`); child `<WorkoutStatistics>` rows for
   `DistanceWalkingRunning` (sum), `ActiveEnergyBurned`, `BasalEnergyBurned`,
   and inline `HeartRate` (average / minimum / maximum, when present);
   child `<MetadataEntry>` rows for `HKAverageMETs`,
   `HKWeatherTemperature`, `HKWeatherHumidity`, `HKIndoorWorkout`,
   `HKElevationAscended`, `HKTimeZone`.
3. For workouts without inline HR statistics (~630 of ~800 entries in the
   reference export), reconstruct `avg_hr_bpm` / `min_hr_bpm` / `max_hr_bpm`
   from `heart_rate` records overlapping the `start_time`..`end_time`
   window. The handler-side flow already does this for HAE payloads via
   `heartRateData`; the XML path can use a post-insert SQL pass per workout
   over `metric_points`, or in-memory aggregation during the same XML
   stream by buffering relevant `<Record type="HKQuantityTypeIdentifierHeartRate">`.
4. **Dry-run / shadow-schema pass before production.** A 2.4 GB XML through
   a brand-new parser branch is not run against production on first try.
   The sequence is:
   - **(a)** Add an `--dry-run` flag to `cmd/import` (or use a `health_test`
     schema) that exercises the full parse + workout-build path but writes
     to a shadow location or to stdout summary only.
   - **(b)** Run dry-run against the reference `export.zip` and verify:
     total `<Workout>` count seen by parser; counts per `workoutActivityType`
     match the §1.1 inventory; `external_id` collision count (expected: 0
     within the export, some overlap with existing HAE-ingested rows on
     2026 dates); HR reconstruction success rate (how many of the ~630
     without inline HR statistics get a non-NULL `avg_hr_bpm` via the
     `metric_points` overlap query); count of workouts that would be
     skipped and why.
   - **(c)** Investigate any divergence from §1.1 numbers before promoting.
   - **(d)** Run for real against production with dedup guarded by the
     existing `external_id` UNIQUE constraint on `workouts`.
   Expected outcome on the reference export: workouts table grows from 102
   rows to ~800, predominantly Walking, spanning 2019–2026.

Not in scope for this track:
- Any change to the live HAE ingest path (`/health/workouts`).
- Backfilling GPX route data — paths exist in the XML as `<FileReference>`
  but the route bodies live in separate files inside the zip. Routes give
  pace/grade granularity per second but are not required for residual
  modeling; deferred.
- Deduplication strategy between HAE ingest (~Feb 2026 onward) and XML
  import (2019–2026). Existing `external_id` UNIQUE constraint on
  `workouts` likely handles this; verify empirically once XML parsing is in
  place.

Success criterion: the Athletic Readiness writer in §4.2 flips from
`eligible=false, reason='importer_gap'` to `eligible=true` on all Walking
events with reconstructable HR and `duration ≥ 10 min`; emits
`reason='walking_only'` on non-Walking dates pending a future multi-modal
extension.

## 5. Phase 1 — Baselines and feasibility check

Begins when ≥ 60 days of Phase 0 data are in place, OR immediately on backfilled history if backfill is implemented cleanly.

### 5.1 What we compute

For each active sub-score, on a strict time hold-out (train ≤ 2024-12-31 with 2024 anomaly window excluded; test ≥ 2025-01-01):

1. **Persistence floor**: MAE/RMSE of "predicted = previous day target".
2. **Rolling mean floor**: MAE/RMSE of "predicted = trailing 7/30d mean".
3. **EWMA floor**: same with EWMA.

### 5.2 Decision rule for Phase 2

For each sub-score: a model is worth building only if a candidate feature set
plausibly beats the strongest naive baseline on hold-out by a margin larger
than bootstrap CI. If not, the sub-score remains **descriptive_only** (state index,
no forecast) and we ship the chip with `descriptive` semantics.

### 5.3 Three-tier success criteria (carried from prior discussion)

Any predictive model proposed in Phase 2 must clear all three:

1. **Regression uplift**: beats best naive baseline on MAE/RMSE on time hold-out.
2. **Rank discrimination**: bottom predicted quartile differs materially from top quartile on observed target (effect size, not just p-value).
3. **Decision usefulness**: when the model says *bad day*, does it catch the bad outcomes better than a single-feature flag (e.g. raw HRV drop)? If not — the model is not adding signal beyond naive heuristics.

R² alone is not a success criterion.

## 6. Phase 2 — Models, calibration, UI

Out of scope for this plan in detail. The shape:

- One model per active sub-score (Recovery Stability, Passive Efficiency, Acute Risk first; Chronic Load may stay rule-based).
- `predictions` table mirroring `target_snapshots` for forward calibration.
- Daily calibration job comparing predictions vs observed; per-model residual dashboards.
- UI: chips + decision layer. No composite number.

## 7. EnergyBank — open question

The wrong-signed correlation against next-day HRV/RHR (formula v1, n=387) is
not addressed by this plan. Two options exist; both are deferred until after
Phase 1 baselines land.

- **Option A**: re-target. EnergyBank stops predicting recovery and is honestly
  re-labeled as a *load ledger / energy accounting* descriptor. No predictive
  claim, no validation needed beyond internal consistency.
- **Option B**: re-parameterize. Find a target it *can* predict — e.g. workout
  performance once that data exists, or subjective fatigue once it is tracked.

Decision is parked until we see whether Recovery Stability + Chronic Load
already cover the operational use cases EnergyBank was reaching for.

## 8. Explicit non-goals

- Renaming `readiness` in the existing pipeline to escape the validation
  problem ("rename and ship") was rejected as the wrong path.
- Adding a fifth/sixth target to chase universal coverage was rejected. The
  family has a defined surface; sedentary users with no walking and no sleep
  data are honestly told `unknown`, not given a best-guess composite.
- Imputing missing physiology in Phase 0 was rejected. Missingness is a state,
  not a gap.

## 8.5. Multi-tenant calibration status

Discovered after Phase 1 step 1 (PR #97), surfaced explicitly when the
operator asked "we've been calibrating against me — what about other
tenants?". The honest answer:

**The system is multi-tenant capable but not multi-tenant calibrated.**

### What is tenant-universal (architecture)

These mechanics are correct for any tenant without further work:

- The four redesign tables and their PKs.
- All four writers and their eligibility decision trees.
- The admin endpoint and its tenant-resolution path.
- All sigma-based thresholds — Acute Risk HRV/RHR z-score cutoffs,
  Chronic Load deterioration z-score cutoff. Sigma is computed from
  the *personal* baseline (`windowStatsBefore` reads only that
  tenant's rolling_3d history), so the threshold automatically scales
  to the tenant's distribution.
- EWMA windows (45d adaptive, 180d slow). Window size is universal;
  the baseline it produces is per-tenant.
- Source-epoch catalogue — `source_epochs` is per-schema. Each tenant
  has their own `initial` row from `EnsureReadinessRedesignTables`;
  additional epochs are added manually as data anomalies are
  discovered.

### What was calibrated against a single tenant

Three constants in code:

| constant | value | calibration source | risk for other tenants |
|---|---|---|---|
| `ChronicLoadMinAcuteDensity` | 7 events / 14 days | empirical distribution on `health` schema (PR #97) | other tenants with different Acute OR base rate will get a misaligned positive rate; label may not discriminate |
| `ChronicLoadMinBreachDays` | 5 days / 14 days | plan-level guess, never empirically calibrated | same class of risk — depends on tenant's Recovery rolling_3d stability |
| Bootstrap source epoch (`initial`) covering 2014-01-01..NULL | hard-coded into the migration | universal — every tenant gets the same bootstrap row | safe; the start date is far enough back that no real tenant has older data |

The third row is safe. The first two are count-based thresholds that
do NOT scale with tenant-specific data distributions the way
sigma-based thresholds do.

### Operational impact

`health_mariia` (the second tenant) has empty `target_snapshots` for
the redesign sub-scores as of the date this section was written. No
labels yet to be miscalibrated. The risk materialises when their data
is backfilled — chronic_load labels will be written using the `health`-
tenant-calibrated thresholds, and the resulting positive rate may
sit outside the operationally-useful 15–30% band.

### Backlog item — tenant-configurable thresholds via `settings`

The chosen path (operator decision, option B in the calibration
discussion):

- Move `ChronicLoadMinAcuteDensity` and `ChronicLoadMinBreachDays`
  into the existing per-schema `settings` table:
  - `chronic_load.min_acute_density` (default `7`)
  - `chronic_load.min_breach_days` (default `5`)
- The writer reads thresholds at write time via the existing config
  path used by Telegram/Gemini settings, falling back to the code
  defaults when the keys are absent.
- Each Chronic Load `data_coverage` JSON records the threshold values
  actually used for that row, so labels remain audit-able and a future
  recalibration does not silently change historical labels' meaning.
- `analysis/phase1_floors/floors.py` should print a recommended
  threshold per tenant (derived from that tenant's Acute OR event
  count distribution) but must NOT change anything automatically —
  the operator approves the change explicitly.
- Defaults documented in code as "calibrated against the `health`
  schema on 2026-05-16; other tenants should run floors and retune
  before interpreting Chronic Load labels."

Why not adaptive (percentile-derived) thresholds: that becomes an
automatic calibration system, and Phase 1 hasn't yet shown which
labels actually carry signal worth automating. Premature.

Why not "single-tenant calibration as design": the system would work
technically for the second tenant but produce silently-broken Chronic
Load labels with no operator signal. A configurable default with a
floors-driven recommendation surfaces the problem instead of hiding
it.

### Until the backlog item lands

- Passive Efficiency, Recovery Stability, Acute Risk are
  personal-baseline-based and unaffected. Feasibility work on those
  proceeds without blocker.
- Chronic Load labels on tenants other than `health` are flagged
  "default calibrated, not tenant-retuned" in any downstream
  interpretation. Documented; not silently treated as authoritative.

## 9. Open questions to revisit

Decisions previously parked here and now closed (kept in source for
traceability):

- **Chronic Load target definition** — decided in §4.2: sustained
  deterioration in Recovery Stability (≥1σ drop for ≥5 days in
  `t+1..t+14`). Acute Risk event density retained as a secondary
  analysis-only label, not the primary Chronic Load target.
- **EWMA window values** — decided in §3.3: 45 days adaptive, 180 days
  slow, fixed across metrics in Phase 0. Changes only through versioned
  bumps.

Still open:

1. **Initial source_epoch catalogue**: the 2024 anomaly suggests at least one
   epoch boundary at 2024-01-01 and one at 2025-01-01 (walking_hr returns).
   Before backfill, run distribution-shift detection across all years and
   land a starting catalogue in `source_epochs`. This replaces the previous
   "exclude 2024" decision — epochs are first-class, not filters.
2. **Sleep fragmentation as separate target vs feature**: efficiency is
   primary, but fragmentation is independently informative. Decide after
   baselines.
3. **WASO measurement**: do we have enough segment-level data to compute true
   WASO (wake bouts mid-sleep) or only end-of-night awake? Audit the segment
   structure of `sleep_awake` records.
4. **Walking segments from raw HR**: minute-level `walking_speed` is empty,
   but raw `heart_rate` has 1.9M points. Worth a follow-up audit on whether
   walking segments can be reconstructed from `heart_rate + step_count`
   co-occurrence instead of relying solely on Apple's daily aggregate. Would
   raise Passive Efficiency resolution from one number/day to multiple
   segments/day.
5. **Athletic Readiness: separate sub-score or secondary target_kind under
   Passive Efficiency?** Once §4.4 lands and Walking workouts are in the DB,
   the per-event Walking residual measures the same physiological channel as
   `walking_heart_rate_average` — just at sharper resolution with external
   load features. Two equally honest framings:

   - **(a) Separate sub-score** — Athletic Readiness stays distinct,
     emits per-event verdicts. Pro: cleanest mapping to "workout HR
     residual" methodology; explicit eligibility per event. Con: name
     overstates what it measures (no Running/Cycling at scale); risks
     duplicate signals confusing the decision layer.
   - **(b) Secondary target_kind under Passive Efficiency** —
     `target_kind = 'wo_residual'` rows alongside the daily aggregate, same
     sub-score, same writer. Pro: honest single-channel framing
     ("how expensive is walking, at whatever resolution we can measure
     today"); no UI proliferation. Con: collapses the natural per-event
     vs daily-aggregate distinction; harder to surface "this particular
     long uphill walk was unusually costly" as its own signal.

   Decision belongs after §4.4 has yielded data — pick whichever the
   downstream model and UI prefer empirically. The `target_snapshots`
   schema supports both shapes (`target_kind = 'rolling_3d'`,
   `'daily_point'`, `'wo_residual'` …), but **no writer emits
   `wo_residual` rows until §4.4 lands** — the dormant Athletic Readiness
   writer continues to write `eligible=false, reason='importer_gap'`. No
   methodological commitment is taken in Phase 0 by the schema's
   expressivity alone.
6. **2024 anomaly cross-check after §4.4**: 2024 has 275 walking workouts in
   the export but zero `walking_heart_rate_average` daily aggregates in the
   DB. Re-importing XML may or may not recover the daily aggregate (Apple
   may have stopped emitting it that year). Confirm whether the gap was a
   missing record stream in the export, an importer filter, or an
   upstream Apple change. This decides whether 2024 is a recoverable
   `source_epoch` or a permanently degraded one.

## 10. Phase 1 status per target

Active record of each sub_score / target_kind's Phase 1 state. Updated
as feasibility experiments complete. "Closed" verdicts are not
permanent — each row carries a revisit trigger that, if observed,
re-opens the target.

All five active targets are now `closed` — the two continuous targets
(Passive, Recovery) onto `ewma_45d`, and the three classifier targets
(Chronic acute_density, Chronic label, Acute Risk event_t1_t3) onto
`event_base_rate`. The pattern is consistent across both metric
families: trained linear models either collapse onto the calibrated
naive baseline or do not clear the success CI by the predeclared
criterion. This is a methodological finding, not failure — it sets
the bar that any non-linear escalation (e.g. GBM) must clear before
being deployable.

| target | status | floor | best linear MAE | verdict | revisit trigger |
|---|---|---|---|---|---|
| `passive_efficiency / rolling_3d` | **closed: EWMA45 production layer** | `ewma_45d` MAE 3.0978, lower CI 2.9263 (split-local) | Ridge α=100 MAE 3.0783 | linear ≈ EWMA45; CI overlap; PR #100 | new signals — real walking segments, weather, route/grade, additional tenant distributions that change the autocorrelation profile |
| `recovery_stability / rolling_3d` | **closed: EWMA45 production layer** | `ewma_45d` MAE 0.0255, lower CI 0.0231 (split-local) | Ridge α=100 MAE 0.0246 | linear ≈ EWMA45; CI overlap with floor; PR after #100 | sleep-stage architecture features (WASO, fragmentation), cross-sub_score features once another classifier shows lift |
| `acute_risk / event_t1_t3` | **closed: event_base_rate production layer** | `event_base_rate` precision@R=0.5 = 0.273 (split-local), CI [0.193, 0.394] | L2 logistic α=0.1 precision@R=0.5 = 0.229, CI [0.200, 0.750], AUC 0.657 vs floor AUC 0.612 | Floor wins on point estimate at predeclared R=0.5; CIs overlap. AUC advantage is small (+0.045) compared to chronic_label (+0.14) — modest ranking signal at best. Walk-forward mean model 0.515 vs floor 0.420, model wins on average but not significantly. | re-evaluate at a different operating point or accumulate more positives in the test tail |
| `acute_risk / event_strict_t1_t3` | **silent diagnostic only** | n/a | — | 9 positives in test slice; CIs uselessly wide | 30+ positives accumulate, OR strict criterion relaxed to ±1.0σ |
| `chronic_load / chronic_label` | **closed: event_base_rate production layer** | `event_base_rate` precision@R=0.5 = 0.850 (split-local), CI [0.682, 1.000] | L2 logistic α=0.01 precision@R=0.5 = 0.750, CI [0.593, 1.000], AUC 0.936 vs floor AUC 0.794 | AUC shows the model has global ranking signal, but on the **predeclared precision@R=0.5 operating point** the calibrated `event_base_rate` floor is already at least as good. Model lower CI below floor upper CI; CIs overlap. (Walk-forward comparison now reports model and floor precision side-by-side per month — see report.) | re-evaluate at a different operating point (lower target recall, or top-k precision) where the AUC advantage may translate into a precision lift over the floor |
| `chronic_load / chronic_acute_density` | **closed: event_base_rate production layer** | `event_base_rate` precision@R=0.5 = 0.042 (split-local), CI [0.037, 0.049] | L2 logistic α=0.01 precision@R=0.5 = 0.020, CI [0.020, 0.031] | model significantly below floor with non-overlapping CIs (only 3 positives in 104-row test tail; walk-forward more favourable but primary split governs) | additional positive events accumulate (≥30 in test slice), cross-sub_score acute lag features |
| `athletic_readiness` | **deferred** | n/a | — | XML import gap; Walking-only volume even after fix | structured Run / Cycle workouts logged at scale (≥ ~200 per type) |

Notes that apply to both closed continuous targets:
- The "floor" column shows the split-local EWMA45 MAE (re-computed on
  the same 70/30 chronological hold-out used for the model
  evaluation), not the full-test-slice floor from
  `READINESS_REDESIGN_PHASE1_FLOORS.md`. The success criterion uses
  the full-slice lower CI bound from the floors report (Passive
  2.9263, Recovery 0.0231), because that's the broader statistical
  claim a model would have to clear to be a candidate.
- Heavy regularisation was required just to recover
  EWMA45-equivalent performance. OLS overfits on the small feature
  set; walk-forward early folds confirm.
- Cross-sub_score features were NOT tried per Phase 1 sequencing rule
  ("no feature fishing without an explicit physiological hypothesis").
  This option stays in reserve only if (a) a classifier target shows
  meaningful linear lift on its own features, AND (b) we have a
  reason to believe cross-pollination from this continuous target
  would help that classifier.
- GBM not attempted. The rule remains: only as a signal-probe after
  at least one target shows meaningful linear / cross-feature
  evidence.
