# EnergyBank v2 — Design

Status: design, not implemented. v1 (current `internal/health/energy.go`) is a stateless daily snapshot whose strain scale saturates on typical days; v2 replaces it with a Bevel-style continuous battery integrating drain and restore over time, with cross-day carryover.

## Model

```text
bank[t] = bank[t−1] + restore(t−1 → t) − drain(t−1 → t)
```

State machine. Each event recomputes the bank for the affected interval and writes a snapshot. No daily reset — yesterday's residual carries through sleep restoration into today's morning capacity.

- **Live current** (dashboard hero, Telegram report): computed on read = latest stored snapshot + delta from `metric_points` since that snapshot. Always reflects "now", independent of write cadence.
- **History** (sparkline, hourly graph in `<details>`): reads `energy_snapshots` directly.

## Storage

```sql
CREATE TABLE energy_snapshots (
  ts_bucket       TIMESTAMPTZ NOT NULL,   -- floor(now, 5min); single source of truth for time
  date            TEXT NOT NULL,           -- computed at write time in tenant's TZ (NOT generated)
  bank            INTEGER NOT NULL,        -- signed, may go negative
  drain_delta     INTEGER NOT NULL,        -- drain accrued in this bucket
  restore_delta   INTEGER NOT NULL,        -- restore accrued in this bucket
  formula_version INTEGER NOT NULL,        -- snapshot of the formula used; never overwritten across versions
  components      JSONB,                   -- INPUT audit trail: {hr_avg, rhr, kcal, sleep_stage, ...}
  flags           TEXT[] NOT NULL DEFAULT '{}',  -- STATE markers (separate from components, see below)
  computed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (ts_bucket)
);
CREATE INDEX idx_energy_snapshots_date ON energy_snapshots (date DESC);
CREATE INDEX idx_energy_snapshots_ts ON energy_snapshots (ts_bucket DESC);
CREATE INDEX idx_energy_snapshots_flags ON energy_snapshots USING GIN (flags);
```

**`date` is computed in Go at write time, NOT a Postgres `GENERATED` column.** Reason: a generated column expression must be `IMMUTABLE`, which forces the timezone to be hardcoded in DDL — wrong for a per-tenant multi-tenant system. The tenant's TZ comes from `REPORT_TZ` (env var per tenant) or `settings.report_tz` (DB override, future v3.0). On write:

```go
loc, err := time.LoadLocation(tenantTZ)   // 'Europe/Belgrade', 'America/New_York', etc.
if err != nil { loc = time.UTC }           // safe fallback
dateStr := tsBucket.In(loc).Format("2006-01-02")
// INSERT INTO energy_snapshots (ts_bucket, date, ...) VALUES ($1, $2, ...)
```

This naturally handles the travel case: snapshots written today in Belgrade carry `date = '2026-05-08'` even after the user moves to NYC; future snapshots will use the new TZ once `settings.report_tz` is updated. Past data stays as it was — which is correct, because *it was* that day in the user's then-current TZ. `ts_bucket` (TIMESTAMPTZ, always UTC internally) remains the canonical absolute time for cross-TZ comparison.

Project convention elsewhere is `date TEXT`; v2 keeps the column for query ergonomics but makes `ts_bucket` the canonical key.

**`components` vs `flags` — separate semantic layers, deliberately.** `components` is an audit trail of the formula's *inputs* (HR averages, RHR baseline, kcal, sleep stages used) — read-mostly after write, for "what did the formula see when it computed this snapshot". `flags TEXT[]` is *state metadata* about how to interpret the row — imputation status, trust level, calibration markers. Mixing them risks accidental mutation of the audit trail when adjusting flag rendering, and conflates two query patterns: "show me what the formula consumed" vs "show me imputed days in last 30". GIN index on `flags` makes membership queries (`WHERE 'imputed_sleep' = ANY(flags)`) O(log n) without the expression-index acrobatics JSONB would need. Bitmask was rejected for being self-undocumenting in psql output (`flags=5` is opaque) and brittle when adding new flag bits.

Initial flag set for v2.0:

```text
imputed_sleep      — SQ came from 7d trailing average (sensor data missing)
imputed_activity   — drain came from 7d trailing average
recovering         — within 3 days of stale-state recovery, computed via re-bootstrap
bootstrap_tail     — within first 3 days after formula_version bump (transient state)
```

v2.5+ extends with `calibration_alpha_tuned`, `calibration_alert`, `data_quality_warn` — no schema change required, just new string constants in Go.

`daily_scores.energy_*` columns become a roll-up (last bucket of the day) — no longer the source of truth.

## Cadence — event-driven, not timer

Hooked into the existing ingest path (`onNewData()` in `cmd/server/main.go`). No fixed cron tick.

- Each ingest → recompute & upsert snapshots in the **last 24h window**, bucketed at 5-minute granularity.
- Buckets older than 24h are frozen (HAE retroactive sync window comfortably covered).
- Multiple ingests inside the same 5-min bucket → upsert, last-write-wins.
- Quiet periods (phone in airplane mode overnight) → no rows written, graph naturally shows the gap; UI renders dotted line for gaps > 30min.

## Settled decisions

| Fork | Decision | Reason |
|---|---|---|
| Bank min | Signed in DB, clamped 0..100 in API/UI | AI needs the "in the hole" signal; user shouldn't see scary minus numbers |
| Restore source | Sleep only in v2; daytime parasympathetic restore deferred to v3 | Validates the integral cleanly before adding HR-baseline-per-hour complexity |
| Retroactive recompute | Last 24h on every ingest | HAE late-syncs sleep ~1-2h after wake; older than 24h converges to <1% delta |
| Backfill | "From day X forward" at launch + manual `/admin` button | Don't lock historical numbers into a still-tuning formula |
| Formula versioning | `formula_version INT` on every snapshot row; bumped manually when constants change; backfill never overwrites rows of a different version | Avoids "rerunning backfill in v2.2 makes last week's numbers different" — week-over-week comparisons stay valid within a version, and UI surfaces calibration-change boundaries explicitly |

## Drain & restore — formulas

### Drain — additive (kcal + autonomic load term)

**Paradigm shift from v1.** `internal/health/energy.go` (v1, legacy)
treats stress as a *multiplier* on physical strain:
`drain = strain · (1 + 0.5 · stress/100)`. v2 abandons this:
autonomic load is its **own additive expense** alongside calories,
not a tax on movement. The consequence — a stressful sedentary day
with `active_kcal ≈ 0` produces **non-zero drain in v2** but
near-zero drain in v1, because v1 multiplies a small number by a
factor and stays small. This is the central methodological reason
v2 exists; reviewers migrating logic from v1 should expect different
distributions per category of day, not just "the same numbers
shifted".



```text
drain(Δt) = α · active_energy_kcal[Δt]
          + β · sustained_hr_load[Δt]      # v2.2, off by default
```

In v2.0 launch ship with `α ≈ 0.08, β = 0` — calories alone are sufficient for non-athletic profiles, and validated by simulation against 31 days of historical data (`/tmp/calibrate_v2.py`, see Validation section). The `β`-term schema stays in `components JSONB` from day one because it covers the *autonomic load* signal kcal misses: elevated daytime HR with normal calories (sustained stress, illness onset, acute anxiety) should drain.

Activated in v2.2. The exact shape of `sustained_hr_load` — hourly z-shift integration against a personal MAD-based awake baseline, with a coverage gate — is **defined in [STRESS_MEASUREMENT.md](STRESS_MEASUREMENT.md) §4.4**. This file does not duplicate that definition; the components JSONB carries both the canonical `sustained_hr_load_z` value used for drain and a parallel `hr_overshoot_bpm_hours` for human-readable audit ("HR ran ~8 bpm above your normal for 4 hours"). Raw `HR − RHR` is **not** the canonical signal — see the superseded `ENERGY_BANK_V2_2_DRAFT.md` for the historical proposal and why it was replaced.

### Restore — asymptotic (Garmin Body Battery style)

**Not** an additive `capacity = bank_yesterday + restore_units`. Sleep refills the bank toward 100 as an asymptote — the closer to full, the smaller the dose:

```text
sleep_quality ∈ [0, 1] = duration_factor · efficiency_factor · structure_factor
   duration_factor   = clamp01(sleep_total_h / 8)
   efficiency_factor = sleep_total / (sleep_total + sleep_awake)
   structure_factor  = max(0.5, 1.0 − max(0, 0.15 − deep_pct)
                                    − max(0, 0.20 − rem_pct))

capacity_today = bank_yesterday + (100 − bank_yesterday) · sleep_quality
```

Three properties this gives us that the additive form did not:

1. **Multi-night sleep deficit accumulates.** A 3-night stretch of poor sleep drags the bank down progressively because each morning starts from a lower asymptotic anchor. Validated: a single bad night in the dataset (5.4h, Apr 9) keeps the bank capped at 73/100 the next morning even after a normal 7.1h sleep.
2. **Capacity never pins at the ceiling.** Across 31 simulated days, capacity hit 100 zero times (the additive form pinned 28/31). Bank distribution gains a real lower tail — p10 = 17 vs 35 in additive — making the chart actually informative.
3. **`structure_factor` only penalizes, never rewards above 1.0.** The asymptote already provides the upper bound; bonus weights would just chase a ceiling that already exists. Range stays cleanly in [0.5, 1.0].

### Bootstrap — seed-erasing forward iteration

The asymptotic restore is a contraction mapping: any seed gets exponentially forgotten through the `(100 − bank) × sleep_quality` factor. Empirically, on real data, three seeds {10, 50, 90} converge to within **0.005 by day 7** and become bit-identical by day 9.

This means **no seed state needs to be persisted**. On every recompute, walk forward through the last 14 days of history starting from a hard-coded `bank = 50`:

```text
function compute_today_bank():
    bank = 50.0                              # convergence-erased seed
    for day in metric_points.last_14_days:
        sq = sleep_quality(day.sleep_metrics)
        bank = (bank + (100 - bank) * sq) - drain(day.activity_metrics)
    return bank
```

By the time iteration reaches "today", the seed is mathematically forgotten — 14 days is double the convergence window. This single mechanism cleanly handles every edge case:

| Scenario | Behavior |
|---|---|
| New user post-warmup | Forward-iterate from seed=50, the 14-day-ago seed dies before today renders |
| Existing user at v2.0 launch | Same — no special "migrate from v1" path needed |
| `formula_version` bump | Non-event — re-iterate 14 days under new formula, dashboard transitions smoothly within a week |
| Data gap < 14 days (no watch) | Self-heals: missing days contribute `sq=0, drain=0`, bank decays toward 0; first watch night refills asymptotically |
| Data gap > 14 days | Triggers staleness re-entry to `warmup` (RHR baseline state machine), no bank rendered until fresh |

### Missing-data handling — trailing-average imputation

A 90-day cross-user prototype run (two tenants: `health` and `health_mariia`) revealed a critical formula behavior at data gaps. Tenant 2 had sleep data missing on 21/90 days (~23%, watch on charger / sync gaps); the original formula applied `sq=0` (no restore) but kept drain active, integrating into a runaway negative signed bank (internal value reached -54). All 5 of that tenant's "worst days" were data gaps, not real depletion.

**Fix: trailing-average imputation when input is missing.**

```text
if sleep_metrics missing for day d:
   sq[d] = avg(sleep_quality over last 7 days with valid, NON-IMPUTED data)
   snapshot.flags |= 'imputed_sleep'

if activity_metrics missing for day d:
   drain[d] = avg(drain over last 7 days with valid, NON-IMPUTED data)
   snapshot.flags |= 'imputed_activity'
```

**Computed in Go during the 14-day forward iteration, not as cached columns or SQL window functions.** The `compute_today_bank()` function already pulls 14 days for iteration; extending the read to 21 days (14 iteration + 7 imputation lookback) is negligible. Imputed values **must be excluded** from subsequent days' trailing windows to prevent feedback drift — track an `imputed: bool` flag on each day in the iteration state. If fewer than 3 valid (non-imputed) days exist in the lookback, fall back to the user's all-time median; this fallback path correlates with the `stale` state below and triggers re-bootstrap.

Behaviorally, during a data gap the bank drifts toward the user's personal equilibrium rather than collapsing to zero from a half-applied formula. This is more honest than "you didn't recover" and more accurate than "nothing happened". UI surfaces imputed buckets via dotted-line rendering with tooltip "imputed from your 7-day average — sensor data missing".

**Graduated trust — three states.** Imputation isn't all-or-nothing. The signal degrades smoothly with gap size, and so should the user's trust in it:

| State | Trigger | Render | AI usage |
|---|---|---|---|
| `fresh` | ≤1 imputed day in last 7 | Bank rendered normally, no flags | Used in RECOMMENDATION |
| `estimated` | 2–4 consecutive imputed days **OR** 2–4 imputed in 14-day window | Bank rendered with dotted-line graph + "estimated · sensor data missing" badge | Used with prompt hedge: "based on 7d trailing pattern, not direct measurement" |
| `stale` | ≥5 consecutive imputed days **OR** >7 imputed days in 14-day window | Bank NOT rendered; placeholder "No recent data · check watch sync" | Not used; AI pivots from activity advice to "sync your watch to get personalized advice" |

Two parallel triggers (`consecutive` and `proportion`) catch two different failure modes: a clean stretch of bad sync (consecutive) vs scattered gaps that accumulate (proportion). On the cross-user prototype, tenant 2 had 23% scattered gaps (21/90) — a `consecutive` rule alone wouldn't have caught it.

**Recovery from `stale` ≠ instant `fresh`.** When fresh data resumes after a stale period, the bank enters a 3-day `recovering` state where it's computed by fresh forward-iteration from `seed=50` over the last 14 days (existing bootstrap mechanism), ignoring any frozen pre-stale snapshots. After 3 days of continuous fresh data, transitions to `fresh`. Logic: 5+ days without data could mean illness, travel, burnout, or sensor failure — applying model state from before the gap is risky. Re-bootstrap is safer than blind continuation.

Note: `stale` here (5-day threshold) is intentionally tighter than RHR baseline staleness (14-day threshold). Bank is a "right now" signal that decays fast under uncertainty; RHR baseline is a slow-moving population statistic that tolerates longer gaps.

**Signed-bank floor.** In DB the bank is signed (preserves "in the hole" semantics for AI), but the floor is `-50`. Below that, the signal saturates and further integration is noise. The original design clamped only above (at 100); the missing-data finding made the symmetric floor necessary.

### Data-quality preflight in personalization (v2.5)

The same cross-user run also exposed a gap in the v2.5 calibration rubric. On tenant 2, HRV correlation was r=+0.017 across all alpha values (flat) — rubric correctly returned "do not auto-tune", but the *cause* was data gaps, not a calibration mismatch. These are different diagnoses requiring different actions.

```text
preflight inside the v2.5 weekly calibrator:
  if missing_sleep_days_in_window / 30 > 0.1
     OR missing_activity_days_in_window / 30 > 0.1:
       verdict = 'data_quality_issue'
       skip calibration run; surface alert "check watch sync"
       do NOT modify alpha_factor

only when preflight passes, run the HRV-correlation rubric described earlier.
```

This separates "formula doesn't fit this user's physiology" (alert: review model) from "we lack the data to know" (alert: check sensors). Both are useful signals, but conflating them is wrong.

### Validation (Apr 8 – May 8, sedentary profile)

Distribution of `bank_eod` under asymptotic restore: min=3, p10=17, p25=34, median=45, p75=50, p90=53, max=59. Three clearly-bad days flagged at eod≤20 (Apr 9 / Apr 17 / May 1) — each corresponds to short sleep + above-typical kcal. p90 lower than the athlete-targeted ~75 because the user's sedentary pattern offers no "rest day" extremes; this is correct behavior, not under-calibration. Constants stay hard-coded for v2.0; tune via `cmd/calibrate` after v2.0 ships and writes real snapshots.

## Phased delivery

**v2.0 — skeleton.** Schema + event-driven recompute + sleep-only restore + UI hourly chart in hero `<details>` + live read with compute-on-read. Constants set to plausible starting values, calibration deferred. Old `EnergyBank` formula and verdict thresholds stay live until v2.0 is validated; AI orchestrator continues reading the daily snapshot field.

**v2.1 — calibration.** Run for 7+ days; tune α and `quality_weight` against the personal bank_eod histogram. Verdict thresholds (currently 25/45/60) re-derived from new distribution percentiles. β stays at 0 (the autonomic-load term is a v2.2 piece, not v2.1) — tuning it here against pre-rubric data would fit it to whatever the bank already does, defeating the validation in STRESS_MEASUREMENT.md §4.5.

**v2.2 — autonomic load drain term.** Adds the `β · sustained_hr_load` term to the drain formula — captures "stressful sedentary days" that `α · kcal` alone misses. Methodology, formula, and validation plan are owned by **[STRESS_MEASUREMENT.md](STRESS_MEASUREMENT.md)** (canonical); this file carries only the storage/integration shape. Ships behind feature flag `energy.stress_drain_enabled`, default off. Illness, recovery-debt, and acute-stress flags computed in the same pass but route to the **verdict layer**, not drain (no double-counting). Earlier v2.2 proposal — ACWR (acute 7d / chronic 28d) multiplier and HRV z-score amplifier — is deferred to v2.5+; multiplicative modulators on top of an uncalibrated additive base are premature.

**v2.3 — daytime restore + day-tagging.** Low-HR / parasympathetic-dominant intervals restore bank in waking hours. Adds `/admin` day-tagging UI (`alcohol`, `caffeine_high`, `illness_confirmed`, `travel`, `menstrual`) — tags do **not** modify drain (30d personal baseline already absorbs habitual intake; one-off events correctly read as deviation) but reshape verdict narrative ("elevated HR likely from alcohol metabolism, not sustained stress").

**v2.4 — actionable hooks.** Once verdict shape is trustworthy, gate real behaviors (workout-type suggestion in morning report, calendar slot blocking, etc.).

**v2.5 — per-user personalization (one knob, HRV-residual-based).** Static `α` for all users is the right base instrument but the wrong long-term answer — an athlete (low RHR, high active kcal, efficient metabolism) and a sedentary user genuinely have different energy economies. Personalization is added in v2.5, NOT earlier, because **adjusting the formula based on its own predictions is overfitting**. We need an external signal the formula doesn't see — *next-morning HRV*.

Original v2.5 design also planned to use next-day RHR as a second signal, but a 90-day prototype run on real data showed RHR sign-flips physiologically (vagal rebound after high load → next-morning RHR drops, not rises) and `daily_scores.rhr_avg` is an Apple Watch retrospective aggregate with timing lag. **Drop RHR from the personalization signal**; HRV alone is the cleaner residual. RHR may be reintroduced in v3.0 if a true *morning-only* RHR (first 2 hours after wake, from `metric_points`) is wired in as a separate metric.

```text
for each user, on a 30-day rolling window:
   pairs = { (bank_eod[d], hrv[d+1]) for d in window }   # lag=+1, validated empirically as the signal peak
   r = pearson(pairs)
   
   if r < 0.2:
      formula does not explain this user's autonomic recovery — alert, do NOT auto-tune
   elif r < 0.3:
      narrow tune: 1D grid search over alpha_factor ∈ [0.8, 1.2]
   elif r < 0.5:
      full grid search: alpha_factor ∈ [0.5, 1.5]
   else:
      strong signal: alpha_factor ∈ [0.4, 1.7] (rare)
   
   alpha_personal = base_alpha * alpha_factor
   stored as user-level setting, refreshed weekly
```

**Storage shape — leaves slot for manual override without shipping it.** Two `settings` keys, not one:

```text
energy.alpha_factor           NUMERIC  -- always read; default 1.0
energy.alpha_factor_source    TEXT     -- 'default' | 'auto' | 'manual'
```

In v2.5 the weekly calibration writes `'auto'` when the rubric permits a tune, otherwise leaves `'default'`. **No manual knob ships in v2.5** — the architectural slot is reserved for v3.0, when profile auto-detection (athlete/sedentary/recovery-impaired) lands and a "switch detected profile" override becomes informed rather than preference-driven. Authority model: `'manual' > 'auto' > 'default'`; auto-calibrator skips its run when `source='manual'`. Single source of truth preserved, no competing-writer footguns.

**Transparency over override in v2.5.** Surface "calibrated automatically · `α_factor=0.85` · last update Mon 5 May" in `/settings` and as a one-line subtext in the dashboard `<details>`. Add a "reset to default" button in `/admin` for debugging (so the user can see what bank looks like without personalization). Surfacing how / when the formula adapts removes most of the user motivation for a manual knob — a measurement instrument should not, by default, expose a "make my number look better" control.

The **narrow tune** band is critical: on a 90-day prototype run, the unconstrained optimum landed at the search boundary (factor=0.5) for a marginal r-improvement — a textbook edge-solution sign of misspecification, not personalization. Clamping to [0.8, 1.2] in the marginal-r zone prevents a small statistical signal from causing a large formula shift. Only when r ≥ 0.3 (statistically significant on n=30, physiologically plausible per HRV-load literature) does the full search range open up.

**Sign requirement is mandatory.** If `r < 0` (low bank correlates with high HRV — opposite physiology), the formula is measuring the wrong thing for this user; auto-tune is suppressed and the case escalates to a calibration review rather than chasing the wrong direction.

With one parameter and 30 observations against an external physiological signal, overfitting is mathematically impossible. If r is weak, the formula isn't capturing what matters for that user — surface as a calibration alert rather than chasing noise.

**v3.0 — profile presets.** Detect user profile from gross stats over 60 days (avg active kcal, RHR baseline, HRV baseline). Three presets: `athlete` / `sedentary` / `recovery-impaired`. Each preset adjusts both `α` and `structure_factor` weights — a qualitative difference that one-knob personalization cannot capture (e.g., apnea sufferers need deep_pct discounted, not amplified).

## Out of scope for v2.0

- **Per-user calibration tuning** at launch (everyone gets same starting α/β/quality_weight/z_threshold) — but constants live in `settings` table from day one, not as Go consts, so v2.1 can tune per-user without schema migration. β reserves the setting slot but stays at 0 effective until v2.2 ships and the §4.5 validation rubric passes; same for `z_threshold` and `stress_drain_enabled`. "Athlete profile" preset becomes a v2.1+ feature.
- Workout-type detection (drain is HR/kcal driven, not exercise-class aware)
- Real-time push notifications when bank drops below threshold
- Migration of `daily_scores.energy_*` rows — left in place as-is; v2 starts writing alongside; `daily_scores` becomes a derived view of v2 snapshots in v2.1

## Open implementation questions

These don't block v2.0 design but need resolution during build:

1. **`RHR_baseline` — derived view, three calibration states.** Baseline is **never persisted** — it's recomputed from `metric_points` overnight RHR samples on every recompute pass. State is determined by **how many overnight samples are available in the last 30 days**, NOT by "days since v2 launch". This means an existing user with months of historical RHR jumps straight to `steady` on the first recompute after v2 deploys; no warmup period needed for accounts that already have history.

    | State | Trigger | Baseline source | UI |
    |---|---|---|---|
    | `cold` | `n_nights < 3` overnight RHR samples in last 30d | None — `computeEnergyBank` returns `nil` | "Collecting baseline · 3 nights minimum" placeholder, no bank rendered |
    | `warmup` | `3 ≤ n_nights < 7` OR newest sample > 14 days old | Simple mean of available samples (EMA α not calibrated for <7 points) | Bank rendered with `calibration_state: "warmup"` flag → frontend badges "calibrating · need N more nights" |
    | `steady` | `n_nights ≥ 7` AND newest sample ≤ 14 days old | EMA, α ≈ 0.25 (~7-day effective window) | Bank rendered normally |

    **EMA initialization for existing users.** When transitioning into `steady` (either at v2 launch with historical data, or after warmup completes), seed EMA by walking forward through the historical samples in chronological order — start from the oldest of the 7 nights, apply α at each step up to the newest. This produces the same value EMA would have held if it had been running in real time, so backfill is correct by construction. No averaging shortcut.

    **No population-constant fallback** (e.g. 65 bpm). For an athlete (RHR ~48) a constant baseline produces artificially high `(HR_avg − RHR_baseline)` deltas → runaway drain on day 1. Silence (gate) is a better first impression than a wrong number.

    The 14-day staleness rule re-enters `warmup` automatically when the user returns from a stretch without the watch — protects against the "EMA looks full but baseline is 14 days stale" trap.
2. **Sleep stage data on multi-source nights.** `sleep_*` stages are picked from one source per night (PR #26). Restoration formula must read the picked source's stages, not a sum. Hook into `sleepCrossValidationPickSourceExpr`.
3. **Event-driven recompute concurrency: per-tenant worker + dirty flag (request collapsing).** Not the `golang.org/x/sync/singleflight` package — that one collapses identical concurrent reads and exposes only `Do`/`DoChan`. We need a different shape: at most one recompute worker per tenant, with a "rerun needed" flag for events that arrive while it's busy. Modeled on `aiRegenInFlight sync.Map` in `ai_orchestrator.go`. Canonical pattern:

    ```go
    type tenantRecompute struct {
        mu    sync.Mutex
        dirty atomic.Bool   // atomic: read by worker, written by triggers concurrently
    }

    func (t *tenantRecompute) trigger(work func()) {
        if !t.mu.TryLock() {
            t.dirty.Store(true)   // worker busy → mark "do another pass"
            return
        }
        go func() {
            defer t.mu.Unlock()
            for {
                t.dirty.Store(false)
                work()
                if !t.dirty.Load() { return }
                time.Sleep(2 * time.Second)   // debounce window: coalesce burst ingests
            }
        }()
    }
    ```

    `dirty` MUST be `atomic.Bool` (race detector will catch a plain `bool`). The 2s sleep is between passes, not before the first pass — first recompute is immediate, only repeats are debounced. Recompute is idempotent over source data (`onNewData()` fires after `InsertPoints` commit, so a parallel event sees the same or fresher `metric_points`); the dirty flag closes the microsecond race where an ingest commits after the current pass started reading. **Not a job queue** — queues add state, failure modes, and backpressure risk we don't need.

    **HAE fragmented-burst caveat.** Health Auto Export occasionally syncs backlogs as 5–15 small POSTs spaced 3–8 seconds apart (typical pattern: phone reconnects to Wi-Fi after a few hours offline, HAE replays the buffered chunks one metric type at a time). With a 2s debounce, the worker can finish pass N, then 4s later pass N+1 fires on a partial-backlog state, then pass N+2 fires again. This is *correct* (each pass sees fresher data and the last one is authoritative) but wasteful — N extra recomputes per burst. Mitigation: bump debounce to **5s** if production telemetry shows >3 recomputes/burst on a typical HAE backlog sync, OR add an "ingest-burst-active" hint from `InsertPoints` that extends the sleep to 10s while the burst is in flight. Not pre-optimising — measure first; the worst case under 2s is still bounded (one recompute per actual data delta) and the bank value converges either way.
