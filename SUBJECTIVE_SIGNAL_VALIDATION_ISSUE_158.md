# Issue 158 Subjective Signal Validation

Analysis date: 2026-06-26

## Scope

Read-only validation for GitHub issue #158. The analysis checks whether readiness, EnergyBank, and stress-derived daily signals align with subjective morning check-in answers.

No scoring, threshold, schema, UI, or notification changes are recommended from this sample alone.

## Cohort

Primary inclusion:

- `subjective_checkins.answer IS NOT NULL`
- `subjective_checkins.status <> 'late_answered'`
- joined by local `date` to `daily_scores`

Tenant eligibility:

| Profile | Eligible answered rows | Included |
|---|---:|---|
| Profile A | 34 | yes |
| Profile B | 4 | no |

Profile B remains below the issue precondition of 30 answered days, so it is excluded from all signal conclusions.

Profile A answer distribution:

| Answer | Rows | First date | Last date |
|---|---:|---|---|
| great | 7 | 2026-05-20 | 2026-06-23 |
| ok | 10 | 2026-05-25 | 2026-06-26 |
| meh | 7 | 2026-05-21 | 2026-06-16 |
| sick | 10 | 2026-06-02 | 2026-06-13 |

One `sick` row on 2026-06-05 was excluded because it was `late_answered`.

All 34 included rows had `readiness`, `energy_eod_current`, `energy_verdict`, and `stress_flags` coverage.

## Per-Answer Summary

Subjective ordinal mapping for correlations: `sick=1`, `meh=2`, `ok=3`, `great=4`.

| Answer | n | Readiness avg | Readiness median | Readiness min-max | Energy avg | Energy median | Energy min-max | Stress load avg | Stress load median | Sleep avg | HRV avg | RHR avg |
|---|---:|---:|---:|---|---:|---:|---|---:|---:|---:|---:|---:|
| great | 7 | 66.1 | 64 | 59-80 | 54.7 | 52 | 45-69 | 1.884 | 1.517 | 7.43 | 41.0 | 65.4 |
| ok | 10 | 67.0 | 66 | 58-80 | 44.2 | 48 | 25-57 | 3.340 | 1.834 | 7.04 | 39.4 | 64.8 |
| meh | 7 | 65.9 | 65 | 61-74 | 48.3 | 53 | 23-62 | 1.255 | 1.318 | 6.92 | 38.2 | 62.9 |
| sick | 10 | 66.6 | 66 | 61-71 | 54.6 | 54.5 | 41-65 | 1.507 | 0.428 | 7.22 | 41.7 | 65.5 |

The readiness buckets overlap almost completely. `sick` days are not lower-readiness days in this sample.

## Correlations

| Metric | Pearson vs subjective ordinal | Spearman vs subjective ordinal |
|---|---:|---:|
| readiness | -0.003 | -0.112 |
| energy_eod_current | -0.098 | -0.096 |
| energy_capacity | -0.194 | -0.097 |
| energy_drain | -0.009 | 0.010 |
| sustained_hr_load | 0.156 | 0.227 |
| hrv_avg | -0.043 | -0.068 |
| rhr_avg | 0.019 | 0.161 |
| sleep_total | 0.058 | 0.089 |

No tested metric shows a strong monotonic or linear relationship to the subjective answer ordinal.

## Energy Verdict Distribution

| Answer | Verdict | Rows |
|---|---|---:|
| great | active_recovery | 4 |
| great | moderate | 1 |
| great | push_hard | 1 |
| great | rest | 1 |
| ok | active_recovery | 2 |
| ok | moderate | 2 |
| ok | push_hard | 1 |
| ok | rest | 5 |
| meh | active_recovery | 3 |
| meh | push_hard | 1 |
| meh | rest | 3 |
| sick | active_recovery | 7 |
| sick | moderate | 1 |
| sick | rest | 2 |

`rest` and `active_recovery` are common on worse subjective days, but also common on `great` and `ok` days. This makes them high-recall but low-specificity signals for `meh`/`sick`.

## Stress Flags

| Answer | Flag | Rows |
|---|---|---:|
| great | none | 7 |
| ok | none | 9 |
| ok | acute_stress | 1 |
| ok | sustained_load | 1 |
| meh | none | 7 |
| sick | none | 8 |
| sick | sustained_load | 2 |

Stress flags are sparse. They fire on 2 of 17 `meh`/`sick` days and 1 of 17 `great`/`ok` days.

## Bad-Day Detection Checks

Bad day definition for this exploratory table: `answer IN ('meh', 'sick')`.

| Predicate | TP | FP | FN | TN | Precision for bad day | Recall for bad day |
|---|---:|---:|---:|---:|---:|---:|
| any_stress_flag | 2 | 1 | 15 | 16 | 0.67 | 0.12 |
| energy_rest_only | 5 | 6 | 12 | 11 | 0.45 | 0.29 |
| energy_rest_or_active_recovery | 15 | 12 | 2 | 5 | 0.56 | 0.88 |
| readiness_lt_60 | 0 | 3 | 17 | 14 | 0.00 | 0.00 |
| readiness_lt_65 | 6 | 8 | 11 | 9 | 0.43 | 0.35 |
| stress_load_gt_2 | 4 | 7 | 13 | 10 | 0.36 | 0.24 |

## Interpretation

The current 34-row Profile A sample is enough to unblock the issue's precondition, but it is not enough to justify threshold changes.

Main findings:

- Readiness is essentially uncorrelated with the subjective ordinal in this sample.
- EnergyBank current value and capacity are also weakly and negatively correlated with the subjective ordinal.
- Stress flags are more specific than broad EnergyBank recovery verdicts, but recall is too low to be useful as a standalone subjective-state detector.
- `energy_verdict IN ('rest', 'active_recovery')` catches most `meh`/`sick` days, but also catches many `great`/`ok` days.
- `readiness < 60` misses every `meh`/`sick` day in the sample.

## Recommendation

Do not change readiness, EnergyBank, or stress thresholds from issue #158.

Open separate follow-up work only if we want to improve the product behavior around subjective check-ins, for example:

- Add a recurring validation report that reruns when the cohort reaches 60 or 90 answered rows.
- Investigate why `sick` days can retain normal readiness, likely because the subjective label captures symptoms/context that current overnight physiology does not.
- Treat subjective check-ins as a separate contextual input in explanations, not as automatic calibration truth for readiness or EnergyBank.

## Limitations

- Only one tenant met the 30-row precondition.
- N=34 is still small, and date clusters may reflect one illness period rather than independent samples.
- The report uses same-day `daily_scores`; it does not test lagged relationships such as previous-day load vs next-day subjective answer.
- Exact tenant names and row-level details are omitted from the public summary.
