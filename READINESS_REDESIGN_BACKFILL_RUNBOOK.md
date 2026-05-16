# Readiness Redesign — Phase 0 Backfill Runbook

End-to-end procedure for the first production sanity run of the
Recovery Stability and Passive Efficiency writers. Used the first time
to validate output distributions, eligibility reasons, and source-epoch
clipping on real data; reusable afterward for routine backfills.

Writes only into the four redesign tables (`target_snapshots`,
`feature_snapshots`, `naive_baselines`, `source_epochs`) via idempotent
upserts. `daily_scores`, `metric_points`, `health_records`,
`energy_snapshots` are not touched. Safe to retry.

## 0. Pre-flight

```bash
# Confirm admin credentials.
echo "$API_KEY" | head -c 4 && echo "…"   # sanity check, no full key in shell history

# Confirm base URL of the production server.
BASE_URL="https://health.example/"        # set yours

# Confirm DB access for inspection.
source ~/.health-db && psql -c "\dt" | head

# Confirm the branch is clean of the short-lived CLI runner that was
# created and removed during Phase 0 development. Re-introducing it
# would split the operational entry point into two — the runbook
# assumes the HTTP endpoint is the only path. Expected output: empty.
ls cmd/readiness_redesign_backfill 2>/dev/null || true
```

Deploy the new binary to production via the usual path
(`make docker-up` on the host, or whichever CI/CD route the repo uses).
The route `POST /api/admin/readiness-redesign/backfill` did not exist
before this change; calls against an unpatched binary will 404.

## 1. Schema health probe (before any backfill)

`Ensure*` runs at startup; this confirms it landed cleanly.

```bash
curl -fsS -H "X-API-Key: $API_KEY" "$BASE_URL/api/admin/status" \
  | jq '.redesign_storage'
```

Expected:
```json
{"healthy": true}
```

If `healthy: false`, stop and investigate — `error` field surfaces the
specific missing table or epoch row. Likely cause is a partial
migration; re-deploy or restart the server (Ensure is idempotent and
runs again on every boot).

## 2. 30-day sanity backfill

```bash
curl -fsS -X POST -H "X-API-Key: $API_KEY" \
  "$BASE_URL/api/admin/readiness-redesign/backfill?from=2026-04-01&to=2026-04-30" \
  | jq '.'
```

Expected response shape:
```json
{
  "schema": "health",
  "from": "2026-04-01",
  "to": "2026-04-30",
  "days": 30,
  "force": false,
  "sub_scores": {
    "recovery_stability": {"written": 30, "error": ""},
    "passive_efficiency": {"written": 30, "error": ""}
  },
  "schema_health": {"healthy": true}
}
```

Stop and report if `written < 30` for either writer, or any non-empty
`error` field, or `schema_health.healthy=false`.

## 3. psql inspection block — 30-day window

Source the DB env once:
```bash
source ~/.health-db
```

### 3.1 Counts by (sub_score, target_kind, eligible, eligibility_reason)

```sql
psql -c "
SELECT sub_score, target_kind, eligible, eligibility_reason, COUNT(*)
  FROM target_snapshots
 WHERE date BETWEEN '2026-04-01' AND '2026-04-30'
 GROUP BY 1, 2, 3, 4
 ORDER BY 1, 2, 3 DESC, 5 DESC;
"
```

What to look for:
- Every (sub_score, target_kind) pair should sum to 30 rows.
- Recovery Stability `daily_point` and `rolling_3d` eligible-row count
  should be close — large mismatch suggests rolling target is being
  blocked too often, which would mean one ineligible night per
  3-night window is more common than expected.
- Passive Efficiency `daily_point` rows ineligible with reason
  `no_walking_hr` are normal (sedentary days, device off, sync gap).
  Mass `walking_hr_out_of_range` would mean either real artifacts in
  source data or a bug in our band.
- For Recovery: a tall pile of `missing_awake_unknown` is the canary
  for the structural-zero heuristic mis-firing — check 3.4 below.

### 3.2 Target value distribution for eligible rows

```sql
psql -c "
SELECT sub_score, target_kind,
       COUNT(*) AS n,
       ROUND(MIN(target_value)::numeric, 4)  AS min,
       ROUND(MAX(target_value)::numeric, 4)  AS max,
       ROUND(AVG(target_value)::numeric, 4)  AS avg,
       ROUND(STDDEV(target_value)::numeric, 4) AS sd
  FROM target_snapshots
 WHERE date BETWEEN '2026-04-01' AND '2026-04-30'
   AND eligible = TRUE
 GROUP BY 1, 2
 ORDER BY 1, 2;
"
```

What to look for:
- Recovery efficiency typically 0.80–1.00. min < 0.50 is suspicious
  (very fragmented night or imputation artefact). max > 1.00 is a
  bug.
- Passive walking HR typically 90–115 bpm for a healthy adult.
  Anything outside [50, 180] should never appear here (eligibility
  filter); if it does, the writer has a bug.

### 3.3 source_epoch distribution

```sql
psql -c "
SELECT sub_score, source_epoch, COUNT(*)
  FROM target_snapshots
 WHERE date BETWEEN '2026-04-01' AND '2026-04-30'
 GROUP BY 1, 2
 ORDER BY 1, 3 DESC;
"
```

For the 30-day window today, all rows should resolve to `initial`
(the only confirmed epoch). Any `unknown` rows here mean
ResolveSourceEpoch fell through to the sentinel — which should not
happen given the bootstrap row covers 2014-01-01..NULL. If it does,
report it.

### 3.4 Sample coverage JSON for ineligible rows

Read why each writer rejected the cases it rejected. Take a few
representative ineligible rows per reason:

```sql
psql -c "
SELECT date, sub_score, target_kind, eligibility_reason,
       data_coverage::text
  FROM target_snapshots
 WHERE date BETWEEN '2026-04-01' AND '2026-04-30'
   AND eligible = FALSE
 ORDER BY sub_score, eligibility_reason, date
 LIMIT 20;
"
```

What to look for:
- For Recovery `missing_awake_unknown`: the coverage JSON should
  reveal the night had staging data but the staged-vs-total gap
  exceeded the 2% structural-zero tolerance. If the gap looks tiny
  (e.g. 2.1%), we may want to widen the tolerance later. If the gap
  is huge (e.g. 30%), the source genuinely doesn't add up and the
  rejection is correct.
- For Recovery `coarse_only_source`: typical for nights logged by
  RingConn / iPhone Sleep Schedule without staging. Expected count
  depends on how often you use a non-Apple-Watch sleep source.
- For Passive `no_walking_hr`: travel days, device-off days, or days
  with too few walking minutes for Apple to compute the daily
  aggregate.
- For Passive `walking_hr_out_of_range`: should be very rare. If any
  exist, look at the `observed_bpm` field to decide whether the band
  needs widening or the source row is a sensor artifact.

### 3.5 Feature snapshot health

```sql
psql -c "
SELECT sub_score,
       COUNT(*) AS n,
       COUNT(*) FILTER (WHERE features::jsonb ? 'prev_efficiency'
                          OR features::jsonb ? 'prev_walking_hr') AS has_prev,
       COUNT(*) FILTER (WHERE (features->>'eligible_count_7d')::int >= 5) AS warm_7d,
       COUNT(*) FILTER (WHERE (features->>'eligible_count_45d')::int >= 22) AS warm_45d
  FROM feature_snapshots
 WHERE date BETWEEN '2026-04-01' AND '2026-04-30'
 GROUP BY 1
 ORDER BY 1;
"
```

What to look for:
- `n = 30` per sub_score.
- `has_prev` close to `n` for sub_scores with steady data coverage.
  Gaps indicate days where the previous-day observation was
  ineligible.
- `warm_7d` and `warm_45d` close to `n` — by April 2026 all baselines
  should be fully warmed up. Cold baselines this far into the history
  would mean the epoch_start lookup is mis-clipping (a real bug).

### 3.6 Baseline coverage

```sql
psql -c "
SELECT sub_score, target_kind, baseline_kind,
       COUNT(*) AS n,
       COUNT(*) FILTER (WHERE predicted_value IS NOT NULL) AS with_value
  FROM naive_baselines
 WHERE date BETWEEN '2026-04-01' AND '2026-04-30'
 GROUP BY 1, 2, 3
 ORDER BY 1, 2, 3;
"
```

Expected: 30 rows per (sub_score × target_kind × baseline_kind) combo,
which means 30 × 2 × 4 = 240 rows per sub_score. `with_value` close to
30 for each — gaps acceptable on the bleeding edge (persistence for
day t has nothing to predict on if t itself is ineligible).

## 4. Decision: extend or stop

After 3.1–3.6, decide based on what we saw:

| Signal | Next step |
|---|---|
| Counts clean, distributions plausible, no missing epoch resolution | Run 4.1 (extend to 2025-01..today) |
| Mass `missing_awake_unknown` or wrong-signed values | Stop; debug pure functions before extending |
| `schema_health.healthy=false` post-run | Stop; investigate why migrations regressed |
| Sparse Passive coverage in 2024 | Expected (the §1.1 anomaly). Still extend, but tag the 2024 window in source_epochs first |

## 4.1 Extended backfill (only after 30-day clean)

```bash
curl -fsS -X POST -H "X-API-Key: $API_KEY" \
  "$BASE_URL/api/admin/readiness-redesign/backfill?from=2025-01-01&to=2026-05-15&force=1" \
  | jq '.'
```

Re-run the 3.1–3.6 queries with the date range widened to
`'2025-01-01' AND '2026-05-15'`. Compare distributions to the 30-day
window; large drift is the signal that an epoch boundary or data
anomaly is being silently absorbed.

## 4.2 Full history (only after 1.5-year clean)

```bash
curl -fsS -X POST -H "X-API-Key: $API_KEY" \
  "$BASE_URL/api/admin/readiness-redesign/backfill?from=2021-01-01&to=2026-05-15&force=1" \
  | jq '.'
```

Note: the 2024 anomaly (per plan §1.1) means Passive Efficiency will
see zero coverage for that entire year. Expected; the writer marks
those dates `no_walking_hr`. Recovery Stability is unaffected because
sleep data is intact in 2024 — but `sleep_efficiency` mean in 2024
was 0.97 vs 0.90 prior years, which is what the source_epoch catalogue
should eventually capture as a separate epoch. Until that's recorded,
the 2024 rows look like a benign distribution shift in the data.

## 5. Cleanup

None required. All writes are upsert into Phase 0 tables; no
production state was modified. If a backfill needs to be discarded
entirely:

```sql
-- Remove all redesign writes for a date range.
-- Touches Phase 0 OUTPUT tables only. Source-of-truth health data
-- (daily_scores, metric_points, hourly_metrics, health_records,
-- workouts, energy_snapshots, ai_briefing_blocks, ai_briefings,
-- settings) is NOT affected by these statements — they target only
-- the four new redesign tables.
psql -c "
DELETE FROM target_snapshots   WHERE date BETWEEN '2026-04-01' AND '2026-04-30';
DELETE FROM feature_snapshots  WHERE date BETWEEN '2026-04-01' AND '2026-04-30';
DELETE FROM naive_baselines    WHERE date BETWEEN '2026-04-01' AND '2026-04-30';
"
```

Safe — these tables exist only for the redesign and have no downstream
consumers yet. `source_epochs` is left intact; the bootstrap row
should always remain.

## 6. What to report back

Paste back, in order:

1. The full JSON response from the 30-day curl.
2. The output of queries 3.1 through 3.6.
3. A short note on anything that surprised you (mass ineligible
   reason, distribution skew, missing rows).

That gives enough to decide whether to extend, tune the eligibility
thresholds, or proceed to the Acute Risk writer.
