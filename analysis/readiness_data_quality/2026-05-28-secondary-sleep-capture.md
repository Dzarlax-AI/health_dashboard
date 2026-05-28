# Secondary-Tenant Sleep Capture Evidence Pass - 2026-05-28

Read-only investigation against a secondary tenant using direct Postgres
access. No production code or database rows were changed.

This artifact intentionally keeps tenant identity and date-level health rows
out of the repository. It records only the aggregate evidence needed to justify
or reject a production code change.

## Question

Before continuing Readiness redesign §6.5 sleep-architecture feature work,
check whether sparse Recovery labels are caused by physiology or by sleep data
capture / aggregation quality.

## Current-Window Data

Recent daily sleep totals showed mixed capture quality:

| Bucket | Days | Average total |
|---|---:|---:|
| normal_6_10h | 15 | 7.35h |
| missing | 2 | n/a |
| short_4_6h | 5 | 5.54h |
| short_lt_4h | 5 | 2.72h |
| long_gt_10h | 1 | 10.92h |

Recovery target coverage for the same mature scoring window:

| Target | Rows | Eligible | Ineligible | Has partial short | Has missing capture | Avg target |
|---|---:|---:|---:|---:|---:|---:|
| `rolling_3d` | 27 | 11 | 16 | 12 | 6 | 0.9397 |
| `rolling_3d_candidate_2of3` | 27 | 22 | 5 | 12 | 6 | 0.9420 |

The candidate target rescues coverage, but it does not currently prove a better
prediction layer. Against existing `ewma_45d` rows:

- strict `rolling_3d`: MAE `0.0291` over 11 eligible rows
- persisted `rolling_3d_candidate_2of3`: MAE `0.0296` over 22 eligible rows
- candidate delta versus strict: `-1.7%`

## Sleep Architecture Probe

Existing read-only probe:

```text
go run ./cmd/readiness_sleep_architecture_probe --schema primary,secondary --min-samples 20
```

Result:

| Tenant class | Target | Samples | Floor | Architecture model | Delta |
|---|---|---:|---:|---:|---:|
| primary | `recovery_stability/rolling_3d` | 90 | 0.0205 | 0.0233 | -13.8% |
| secondary | `recovery_stability/rolling_3d` | 35 | 0.0314 | 0.0503 | -60.0% |
| secondary | `chronic_load/chronic_label` | 6 | n/a | n/a | inconclusive |

Interpretation: do not promote sleep architecture features now. The model loses
to the floor and likely encodes capture noise.

## Raw Segment Check

The date-level check found both true sparse capture and a narrow boundary case
near the hard 4h cutoff. To avoid committing private health rows, the exact
dates and raw segment values are omitted.

Aggregate findings across the inspected window:

- 2 days have missing `sleep_total`
- 5 days have `sleep_total < 4h`
- 1 day has `sleep_total < 4h` but staged sum >= 4h
- 1 day has staged sum more than 1h greater than `sleep_total`
- exactly one source-rounded borderline night sits in `[3.95h, 4.0h)`

This suggests two separate failure modes:

1. Actual partial/missing capture.
2. Possible aggregation/source mismatch where staged rows contain more usable
   evidence than `sleep_total`.

## SQL-Only Alternative Target Simulation

This simulation did not change code or rows. It reused existing `ewma_45d`
baselines, so results are directional only; a real implementation would need
versioned baselines.

| Scenario | Rows | Eligible | MAE |
|---|---:|---:|---:|
| strict_3of3_base | 27 | 11 | 0.0291 |
| strict_3of3_staged_sub | 27 | 11 | 0.0291 |
| strict_3of3_tolerance_3_95h | 27 | 14 | 0.0269 |
| candidate_2of3_base | 27 | 22 | 0.0296 |
| candidate_2of3_staged_sub | 27 | 25 | 0.0289 |
| candidate_2of3_tolerance_3_95h | 27 | 22 | 0.0286 |

Interpretation:

- `2-of-3` is primarily a coverage rescue, not a proven predictive improvement.
- Sleep architecture is a no-go on current data.
- A tiny tolerance around the 4h cutoff is more promising than architecture, but
  still only a small boundary fix.
- Staged-sum fallback is exploratory and should not be promoted in this PR.

## Final Check: What Exactly Does 3.95h Tolerance Rescue?

The tolerance rule rescues exactly three strict `rolling_3d` rows in the
current-window simulation. All three are caused by the same source-rounded
borderline night just below 4h.

Rescued strict rows, anonymized:

| Rescued row | Base eligible nights | Tolerance eligible nights | EWMA45 | Tolerance target | Tolerance error | Candidate target | Candidate error | Verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| A | 2 | 3 | 0.9520 | 0.9792 | 0.0271 | 0.9820 | 0.0300 | tolerance better |
| B | 2 | 3 | 0.9538 | 0.9590 | 0.0052 | 0.9518 | 0.0020 | candidate better |
| C | 2 | 3 | 0.9546 | 0.9297 | 0.0249 | 0.9077 | 0.0469 | tolerance better |

Interpretation:

- This is not a broad new rule justified by many examples.
- It is a narrow boundary effect around the hard 4h cutoff.
- A tolerance like `3.95h` would have changed 3 strict rows in this window and
  improved 2/3 of those rows versus the existing candidate fallback.
- The case for a code change is plausible but small; if implemented, it should
  be a narrowly scoped threshold-tolerance change with tests, not a model or
  architecture-feature change.

## 90-Day Check

The current-window check was not enough because the secondary tenant already
has roughly 90 days of usable history. Re-running the same simulation over the
latest 90-day window gives:

| Scenario | Rows | Eligible | MAE |
|---|---:|---:|---:|
| strict_3of3_base | 87 | 35 | 0.0255 |
| strict_3of3_tolerance_3_95h | 87 | 38 | 0.0250 |
| strict_3of3_staged_sub | 87 | 35 | 0.0255 |
| candidate_2of3_base | 87 | 77 | 0.0276 |
| candidate_2of3_tolerance_3_95h | 87 | 77 | 0.0273 |
| candidate_2of3_staged_sub | 87 | 80 | 0.0275 |

The 90-day rescued rows are the same three strict rows from the current-window
check, all caused by the single source-rounded borderline night.

Decision update:

- Waiting for more data is not necessary; the 90-day history already answers
  the question.
- `2-of-3` remains evidence-only: much better coverage, worse MAE.
- Sleep architecture remains no-go.
- A small `3.95h` tolerance PR is justified as numerical stability around a
  hard threshold, provided tests prove clearly short nights remain ineligible.

## Primary-Tenant Cross-Check

The same 90-day simulation was run against the primary tenant.

| Scenario | Rows | Eligible | MAE |
|---|---:|---:|---:|
| strict_3of3_base | 87 | 87 | 0.0245 |
| strict_3of3_tolerance_3_95h | 87 | 87 | 0.0245 |
| strict_3of3_staged_sub | 87 | 87 | 0.0245 |
| candidate_2of3_base | 87 | 87 | 0.0245 |
| candidate_2of3_tolerance_3_95h | 87 | 87 | 0.0245 |
| candidate_2of3_staged_sub | 87 | 87 | 0.0245 |

Additional checks:

- `sleep_total < 4h`: 0 days
- `sleep_total in [3.95h,4.0h)`: 0 days
- missing `sleep_total`: 0 days
- staged sum more than 1h above `sleep_total`: 0 days
- rescued strict rows from tolerance: 0

Interpretation: the tolerance is a no-op for the primary tenant over the latest
90 days. It fixes the secondary tenant's borderline source-rounded case without
changing the primary tenant's Recovery target history in this window.
