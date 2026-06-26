# Issue 155 Calibration Warmup Gate Investigation

Analysis date: 2026-06-26

## Scope

Read-only investigation for GitHub issue #155. The goal was to check whether the absence of recent `calibration_warmup` stress flags is valid data-quality silence or evidence that the warmup gate is broken.

No source code, schema, scoring, or threshold changes were made.

## Code Path Checked

`calibration_warmup` is emitted by `ComputeSustainedHRLoadForDate` only after the HR coverage gate passes and `PersonalBaseline(date, ChannelHRAwake, 30, loc)` returns `CalibrationWarmup`.

The relevant state machine is:

- `cold`: fewer than 3 baseline samples
- `warmup`: 3-6 baseline samples, or 7+ samples whose newest sample is older than 14 days relative to the scored date
- `steady`: 7+ baseline samples and newest sample age <= 14 days

This means a date with poor HR coverage can correctly receive `stale_stress` instead of `calibration_warmup`, even if some baseline data exists.

## Persisted Flag Evidence

| Profile | Rows with stress flag column | `calibration_warmup` rows | First warmup | Last warmup | `stale_stress` rows |
|---|---:|---:|---|---|---:|
| Profile A | 3298 | 1 | 2017-07-12 | 2017-07-12 | 1075 |
| Profile B | 3928 | 0 | - | - | 2290 |

The single Profile A warmup row is historical:

| Profile | Date | Flags | Baseline HR overnight | HRV avg | RHR avg | Sustained HR load |
|---|---|---|---:|---:|---:|---:|
| Profile A | 2017-07-12 | `{calibration_warmup}` | null | null | null | 0 |

Stress flag distribution:

| Profile | Flag | Rows |
|---|---|---:|
| Profile A | `stale_stress` | 1075 |
| Profile A | `acute_stress` | 279 |
| Profile A | `sustained_load` | 151 |
| Profile A | `recovery_debt` | 15 |
| Profile A | `parasympathetic_rebound` | 2 |
| Profile A | `calibration_warmup` | 1 |
| Profile B | `stale_stress` | 2290 |
| Profile B | `acute_stress` | 387 |
| Profile B | `sustained_load` | 103 |
| Profile B | `recovery_debt` | 21 |
| Profile B | `parasympathetic_rebound` | 7 |

## Recent Opportunity Audit

The last 121 daily-score dates, 2026-02-26 through 2026-06-26, had populated `stress_flags` for both profiles.

| Profile | Recent days | Null stress flags | Non-null stress flags |
|---|---:|---:|---:|
| Profile A | 121 | 0 | 121 |
| Profile B | 121 | 0 | 121 |

Using the same rolling 30-day HR-awake sample source shape as `PersonalBaseline(ChannelHRAwake)`, every recent date in both profiles was safely in `steady` state.

| Profile | Proxy state | Days | First date | Last date | Persisted warmup | Persisted stale stress |
|---|---|---:|---|---|---:|---:|
| Profile A | `steady` | 121 | 2026-02-26 | 2026-06-26 | 0 | 0 |
| Profile B | `steady` | 121 | 2026-02-26 | 2026-06-26 | 0 | 4 |

Recent rolling baseline sample counts were far above the warmup threshold:

| Profile | Min samples | Median samples | Max samples | Newest sample age min | Newest sample age max |
|---|---:|---:|---:|---:|---:|
| Profile A | 1128 | 1377 | 1807 | 1 day | 1 day |
| Profile B | 526 | 584 | 645 | 1 day | 1 day |

The 4 recent Profile B `stale_stress` rows also had steady baseline evidence, so they are consistent with HR coverage gaps rather than baseline warmup:

| Date | Flags | Baseline samples | Newest sample day | Newest age |
|---|---|---:|---|---:|
| 2026-05-20 | `{stale_stress}` | 628 | 2026-05-19 | 1 day |
| 2026-03-29 | `{stale_stress}` | 583 | 2026-03-28 | 1 day |
| 2026-03-06 | `{stale_stress}` | 553 | 2026-03-05 | 1 day |
| 2026-02-27 | `{stale_stress}` | 536 | 2026-02-26 | 1 day |

## Conclusion

The absence of recent `calibration_warmup` flags appears valid. The recent production window had no low-sample HR-awake baseline opportunities: both profiles were consistently in steady baseline state, with fresh samples and counts far above the warmup threshold.

This does not look like a broken gate. No follow-up code issue is needed from the current evidence.

## Limitations

- The opportunity audit intentionally used a bounded 121-day recent window because a full rolling historical join across all dates was too expensive for interactive investigation.
- The SQL proxy matches the rolling HR-awake baseline sample source shape, but it does not reconstruct the exact Go awake-window coverage gate. Persisted `stress_flags` remain the source of truth for actual emitted flags.
- Exact schema names are omitted from the report.
