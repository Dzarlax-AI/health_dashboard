# EnergyBank v2.2 — HR-overshoot drain term (SUPERSEDED)

> **Status: SUPERSEDED by [STRESS_MEASUREMENT.md](STRESS_MEASUREMENT.md)
> as of 2026-05-12.** Kept in repo as historical context only.
>
> What changed and why:
>
> - This draft proposed `β · ∫ max(0, HR(t) − RHR) dt` (bpm·hours,
>   raw RHR subtraction). Multi-model review (2026-05-12) flagged
>   three problems with that shape: (a) it leans on
>   `daily_scores.rhr_avg`, which is a per-day *average* not a true
>   resting baseline — known to be wrong (see STRESS_MEASUREMENT
>   §4.1); (b) it mixes physical, postural, and stress components
>   into a single raw bpm delta; (c) at daily-average granularity
>   it loses the temporal structure of the day (a 4h sustained
>   shift vs a 1h spike look identical).
> - The canonical v2.2 formula is now **hourly sustained
>   z-load over a personal awake baseline**, with an MAD floor
>   and a coverage gate. See STRESS_MEASUREMENT.md §4.4.
> - Calibration target was given here as β ≈ 0.12 (against
>   bpm·hours) and in STRESS_MEASUREMENT as β ≈ 0.075 (against
>   z-shift × awake_hours). Neither survives — the canonical
>   formula uses different units. Treat both numbers as historical;
>   the new placeholder is set in STRESS_MEASUREMENT.md §6.
> - Sick-day attribution moves out of drain entirely. Illness
>   flag now lives in the verdict layer (suppresses
>   `push_hard`) rather than inflating drain — avoids
>   double-counting the HR rise the flag already caught.
>
> Everything below this banner is the original draft, preserved
> verbatim. Do not edit. Do not implement from this file.

---

Status: design draft. Not for review yet.

## Problem v2.0 doesn't solve

v2.0 drain is `α · active_kcal`. This captures physical exertion but misses **autonomic load without movement**:

| Scenario | v2.0 drain | Reality |
|---|---|---|
| Stressful WFH day, RHR 75 (baseline 60) | ≈ 0 | Significant tax |
| Mild illness (cold/flu), bedrest | ≈ 0 | High immune cost |
| Caffeine + emotional event, elevated HR all day | ≈ 0 | Real autonomic drawdown |
| Public speaking / interview, sympathetic spike | ≈ 0 | Recovery debt accrues |

The bank doesn't move on these days, so a user can wake up with a "fresh" 70 after spending the previous day quietly in the red autonomic-wise. The HRV-gate in `chooseVerdict` partially compensates (it'll downgrade `push_hard` → `active_recovery`), but the **number itself** lies — and the AI / Telegram prompts read the number.

## v2.2 formula

```
drain_total = α · active_kcal + β · ∫ max(0, HR(t) − RHR) dt
```

Reserved in code comment ([energy_v2.go:57-61](internal/health/energy_v2.go)) but not implemented:

> v2.0 ships with the calorie-only term active (alpha · active_kcal); the β · max(0, HR − RHR) · duration term is reserved for v2.2 once HR-per-hour reads are wired in.

Integration step (hourly):

```
hr_overshoot_hours = Σ_h max(0, HR_avg[h] − RHR_baseline) / 60   # bpm·hours
drain_hr           = β · hr_overshoot_hours
```

Concrete picks (placeholders, calibrate empirically):
- RHR baseline: 14-day median (already computed elsewhere — reuse `internal/health/baseline.go` if there is one)
- β: target ~10-15 drain points on a "stressful sedentary day" (8h × 10bpm overshoot = 80 bpm·h × β = 10 → β ≈ 0.12)
- Cap per day: clamp `drain_hr ≤ 30` to prevent a noisy sensor day from blowing the bank

## Data pipeline

Inputs needed beyond what v2.0 reads:

| Input | Source | Status |
|---|---|---|
| HR per hour | `hourly_metrics` where `metric_name='heart_rate'` | **Available** (cache already builds it on ingest) |
| RHR baseline | `daily_scores.rhr_avg` over 14d | **Available** |

No new ingest paths, no schema changes. The HR-overshoot integral is a SQL query against `hourly_metrics` for the target date.

New storage method (proposed):

```go
// HourlyHROvershoot returns Σ max(0, HR_avg - rhr) for the given date,
// in units of bpm·hours. Returns 0 when no HR samples exist for the day
// (early-morning before any sync, or sleep-only days with no daytime
// readings) — caller treats that as "no HR signal, drain falls back
// to v2.0 (kcal only)".
func (s *DB) HourlyHROvershoot(ctx context.Context, date string, rhr float64) (float64, error)
```

Update `DrainV2` signature:

```go
func DrainV2(activeKcal, hrOvershootHours, alpha, beta float64) float64 {
    return alpha*max0(activeKcal) + beta*max0(hrOvershootHours)
}
```

`EnergyConfig` grows `BetaHR float64` (default 0.12, settings-overridable like alpha).

## Calibration plan

Same approach as v2.0:

1. Implement formula + tests (kernel-only, no orchestrator)
2. Backfill against historical `hourly_metrics` over 6-12 months — this needs a **new** backfill cmd (`cmd/energy_backfill_v22`) because `cmd/energy_backfill` is a forward iteration that doesn't replay hourly aggregates. OR: extend `cmd/energy_backfill` to accept a `--formula-version` flag and have it pull hourly HR when v2.2.
3. Compare bank distributions v2.0 vs v2.2 on the same dataset
4. Tune β so the spread is meaningful but not chaotic (target: change median by ≤ 5 points, but introduce 3-5 net-new "actually-drained-not-from-movement" days per month)
5. Ship behind a feature flag (`energy_formula_version` in settings, default 2 = v2.0, opt-in 3 = v2.2)

## Open questions

1. **RHR per day or rolling baseline?** `daily_scores.rhr_avg` is per-day average, not the resting-true value. For sleep-only nights it might overshoot. Possibly use 30-day percentile-5 of HR instead (true resting baseline). Trade-off: more storage / aggregation vs. simpler reuse.
2. **Sleep window exclusion?** Should we integrate HR overshoot 24h or daytime-only? Sleeping HR drops below RHR baseline naturally — the `max(0, ...)` floor handles this mathematically but a daytime-only window is more interpretable.
3. **Sensor coverage gaps.** Apple Watch off → no HR samples → hourly_metrics has gaps → integral underestimates. Need an imputation policy. Probably: if fewer than N awake hours have HR samples, fall back to v2.0 for that day (drain_hr = 0). Document as a known limitation.
4. **Sick-day attribution.** Fever raises HR. v2.2 will drain hard on sick days — that's correct (the body IS spending energy), but the AI recommendation "rest" might double-down on an already-resting user. Possibly cross-reference wrist_temperature anomaly to soften the verdict on those days. Out of scope for v2.2; flag for v2.3.

## Migration / cutover

v2.0 → v2.2 is opt-in, gated on `energy_formula_version` setting. Default stays v2.0 for existing installs. Admin UI toggle. Cutover plan inherits from v2.1 (the verdict-thresholds cutover) once that lands.

Backfilled rows from `cmd/energy_backfill` (current) are v2.0 — they should NOT be overwritten when v2.2 ships, just supplemented. New rows from v2.2 carry `formula_version=3` and the `chart` / sparkline reads the latest version per date.

## Out of scope

- HRV-derived drain (multi-day chronic stress signal). Tempting but methodologically harder — HRV is itself the recovery signal we use in verdict gating, and double-counting it on the drain side would distort.
- Cold-exposure / heat-exposure drain (sauna, ice bath). Real signal, no clean Apple HK metric. Defer.
- Sleep efficiency vs sleep stages as restore input. v2.0 uses both already.

## Todoist tracking

Existing: `6gc2R6692674v3Gf` — final v1→v2 cutover (verdict thresholds). Add separate task for v2.2 when work starts. Do not block v2.1 cutover on v2.2.
