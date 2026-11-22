# Workout-to-sleep coverage preflight

This is a read-only, aggregate-only feasibility check, not an association study. Obtain authorization for each tenant separately. Connect as that tenant's schema-owning role with its schema first in `search_path`; begin a `REPEATABLE READ READ ONLY` transaction. The guard checks these session properties, not whether the operator is authorized. Supply actual inclusive local **wake dates** and the tenant's actual IANA timezone; no dates or timezone are assumed. For example, replace all placeholders before running:

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
-- SET LOCAL search_path TO the authorized tenant schema, pg_catalog;
\set start_date 'YYYY-MM-DD'
\set end_date 'YYYY-MM-DD'
\set tz 'Europe/Belgrade'
\i experiments/dbt_metrics/semantic/workout_sleep_coverage.sql
ROLLBACK;
```

The denominator is every calendar wake date in the requested interval, including dates without records. The output gives nested qualifying counts and mutually exclusive exclusion counts; epoch rows count only fully eligible nights. Epoch numbers are anonymous within one result and must not be compared between runs. Interval statistics are withheld for epochs with fewer than five eligible nights. Hours are elapsed time between instants, so DST transitions are handled by PostgreSQL; mean, range and sample standard deviation describe variation, not statistical evidence.

A supported onset proxy is accepted only for a complete, plausible, finalized and eligible main-night record with exactly one episode on that wake date; episode and night must agree on source and epoch, all three coverage generations must agree, episode must be complete and plausible, contained in complete period coverage, end on the local wake date, and its elapsed duration must agree with the night duration within one second. The schema has no explicit binding between the main-night record and an episode; matching stored fields and duration supports a proxy, not proof of authoritative identity. The single-episode condition is deliberately restrictive: no episode is labeled main when naps or multiple candidates exist. Coverage start is never substituted for sleep start. Missing or ambiguous evidence is unknown. The semantic pilot also evaluates stage-based intervals, which this preflight does not include: low proxy coverage does not establish absence of all possible onset evidence. This conservative match can undercount valid nights and does not prove event-level provenance beyond the stored fields.

The latest valid workout that starts before sleep and ends within the preceding 24 hours or later is selected; it must end at or before sleep start. An overlapping latest workout disqualifies the night rather than falling back to an earlier workout. Equality at the boundary is accepted. The window is a feasibility convention, not a physiological threshold; absent or malformed workouts and gaps in workout ingestion cannot be distinguished. The check-in must have status `answered` or `late_answered`, have a non-null answer, be recorded after or at episode end and on the same tenant-local wake date. Multiple sources count once. The overall answered count includes late answers. The separate on-time count requires status `answered` and an answer strictly before `expires_at`; late-only counts answered nights without an on-time answer. These refer to the prompt response window, not a verified morning clock: even on-time answers can occur later in the day, and no assumption is made that an answer was available to the morning report.

Current snapshots may reflect later corrections: this query does not reconstruct what was known at the time, establish correction/deletion completeness, identify exact devices independently of source epochs, measure nonresponse bias, or demonstrate causation. Source epochs and source labels may be imperfect device proxies. Small cell counts and precise aggregate values can still reveal sensitive information; share only with the authorized recipient. No raw records are output.

## Selected stage snapshots

The second preflight uses the daily selection's candidate ID and exact revision. A selected snapshot can contain several sessions. The row model groups contiguous stages with gaps of at most 30 minutes, compares the longest session against the next longest and checks its asleep duration against both the selected and canonical durations. A one-minute tolerance admits recording precision. The summary reports missing stages, inconsistent labels and ambiguous sessions. A candidate without a matching coverage commitment remains outside the stricter subset. The start is the first recorded asleep stage. A selected snapshot with a matching day and duration is a candidate proxy. The stricter sensitivity check also requires the legacy source, epoch and sync generation to match and the coverage interval from that revision to contain the first and last asleep stages. Daily phase totals have no recorded onset and are excluded from this approach.

The workout must finish in the preceding 24 hours. A subsequent check-in must be answered within its prompt window and between 05:00 inclusive and noon exclusive on the wake date in the profile's timezone. These bounds define the research cohort. Results distinguish “great” or “ok”, “meh”, and “sick”. The separate duration comparison distinguishes check-ins after the end of a matched candidate from dates paired by the morning calendar window only. When any candidate end is present, an earlier answer is excluded conservatively. The stricter subset still uses a recorded proxy for the end of sleep. Duration means in a response group mix dates and can reflect device periods or changing response habits.

The two SQL models implement the experiment. The private per-night intermediate is ephemeral. Only the one-row aggregate is materialized by dbt in the isolated fixture. The fixture supplies exactly one `research_parameters(start_date, end_date, tz)` row and production-shaped tables on its search path. The opt-in dbt check builds the summary and compares it to direct SQL on the same inputs, including checking that dbt created no relation for private rows. Production preflight uses a single aggregate SELECT in the verified tenant's read-only transaction.

Run `STAGE_ONSET_CHECK_DBT=1 bash experiments/dbt_metrics/semantic/test_stage_onset_coverage.sh` from the repository root to exercise the synthetic fixture and dbt parity check. For the authorized read-only path, `scripts/render_stage_onset_summary.py` renders the same aggregate for psql variables `start_date`, `end_date` and `tz`. Use it inside the existing tenant-verified transaction. It emits no connection commands or credentials.

## Annual check using the selected receipt

The raw-packet method follows the ordinary ingest receipt that owns the selected night's total. It reads standalone, same-source stage measurements from that receipt inside PostgreSQL. Sharing a receipt, a source and a plausible duration supports an onset estimate, but samples lack a direct link to the main episode. Summary buckets and measurements with uncertain grain limit interpretation. This study describes the receipts linked to the current corrected history. For newer nights the ingest journal identifies the active receipt. For older nights the study reports a separate sensitivity check on the current owner of the total. That check requires the raw coverage content to reproduce the legacy commitment fingerprint, the total to match, and the selected night to reproduce the canonical fingerprint. A later receipt can repeat the same total and coverage while carrying different stage samples. This second check therefore supports content equivalence only. Neither tier can establish which standalone samples belonged to the main episode.

Both the selected-snapshot method and the packet method use the same workout and morning-answer SQL. The comparison counts the two sets of qualifying wake dates, their intersection and their union. A date can contribute at most once. When both methods produce an onset, starts and ends must agree within one minute. Different workout or answer assignments, including qualifying in only one method when both have an onset, exclude that date from the usable combined cohort. The packet interval is used when both accepted methods agree. Within each source and device period, group comparisons require at least five responses in each compared group. This reporting threshold does not establish statistical significance.

The packet comparison is opt-in with dbt variable `packet_onset_research`. Only its aggregate summary is materialized. `render_stage_onset_summary.py --packet` emits the equivalent single-tenant read-only psql query; the default still selects the earlier snapshot-only study.

## Local report and prospective check

The device period used for grouping is the one assigned to the recorded main night. A stage-based estimate accepted on duration and timing alone can come from a different source or period. The pooled comparison therefore retains some uncertainty about which device supplied the stages.

Method `packet-onset-v1.1` reports the positive-minus-meh difference in median workout-to-onset intervals within each source and epoch. Sick mornings are counted separately. Both groups need five observations. The sensitivity check removes each meh morning once and applies the same minimum to the remaining groups. Counts describe accepted wake dates, including sick mornings. Coverage before pairing is the union of both onset methods and counts each date once.

Run the guarded packet summary for the authorized tenant and period and capture its single JSON result (psql unaligned, tuples only). The report consumes that aggregate locally, including method version, timezone and period. For a private output outside the checkout:

```sh
(umask 077; python3 experiments/dbt_metrics/scripts/render_workout_sleep_report.py < "$summary_json" > "$report_html")
```

The initial study ends on 2026-11-21. The prospective period runs from 2026-11-23 through 2027-11-22 inclusive, Europe/Belgrade. November 22, 2026 is reserved for protocol definition. After the period closes, use the same definition and compare the direction and size of the median difference within comparable device periods. Show insufficient coverage when a group falls below the reporting threshold. Compute each period separately. When both periods are available, match the actual source and epoch inside the authorized tenant query before anonymizing. Epoch numbers are local to each report. Report unmatched periods separately.

Frozen SQL sources for this prospective comparison (SHA-256 of each file):

- `macros/pair_research_onset.sql` — `9d5680b9046915221a994cddf2f0348fc5cf6884abd5100f62dc98b4d428acb5`
- `models/research_stage_onset_rows_v1.sql` — `7a10bc1bd40a18a515ec1b23314e25a8b6aae4ca07973cab3edb288faf1827a9`
- `models/research_packet_onset_base_v1.sql` — `cdce51dd32f2f27d63a3e6646bfff0ab941741aa5b18fc81921c535334b33b4c`
- `models/research_packet_onset_rows_v1.sql` — `34144c33b482b60900794df59e3a02e93a0ae81dffd86337a5402938f0d6a1a1`
- `models/research_packet_onset_summary_v1.sql` — `bb620cbace7228406c56556c3f2611c727def133d176f0b1a78338072ae75aad`
