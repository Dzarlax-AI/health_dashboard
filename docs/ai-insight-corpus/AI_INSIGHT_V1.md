# Independent AI Insight evaluation (local-only)

This is a new quality gate. The old B1 narrative corpus and its v42 results do
not validate the independent `Server Insight` / `AI Insight` contract.

The input is a manually reviewed `ai-insight-corpus-v1` JSON file with 20–30
cases. Each case has `id`, `locale` (`en`, `ru`, `sr`), `origin`
(`observed_aggregate` or `synthetic_controlled`), `tags`, `primary`, `domains`,
and `facts`. The latter three are B0 display insights and derived facts only;
never include raw HealthKit samples, free medical history, dates, device/source
IDs, tenant identifiers, credentials, old model output, or hidden reasoning.
No more than five cases may be controlled synthetic edge fixtures. The cases
must cover agreement, justified and unjustified disagreement, conflicting
actions, partial/stale data, and no-data fallback. A human must verify every
case before transfer; structural validation cannot certify anonymity.

Run the no-provider validation first:

```bash
go run ./cmd/ai_insight_eval --corpus /private/tmp/ai-insight-corpus-v1.json --validate
```

Before a provider call, verify the active Admin configuration from within the
already authorized isolated backend container. This reads the installation-wide
registry only and prints no key:

```bash
ai_insight_eval --check-config --database-config
```

For a small sentinel run, validate the entire reviewed corpus first and then
select exact case IDs. The result retains the hash of the full corpus and
records the explicit selection; it is not a substitute for the later full run:

```bash
ai_insight_eval --database-config \
  --corpus /tmp/ai-insight-corpus-v1.json \
  --case-ids=case-id-1,case-id-2 \
  --out /tmp/ai-insight-sentinel-v1.json \
  --confirmed-anonymized --runs=3
```

After recording the checksum and reviewing all packets, run exactly three
independent generations per case. In that backend container, use the active
Admin configuration without exporting or copying its key:

```bash
ai_insight_eval --database-config \
  --corpus /tmp/ai-insight-corpus-v1.json \
  --out /tmp/ai-insight-evaluation-v1.json \
  --confirmed-anonymized --runs=3
```

For a separate, explicitly configured evaluation environment, the evaluator
also accepts an API-key environment variable:

```bash
OPENAI_API_KEY=... go run ./cmd/ai_insight_eval \
  --corpus /private/tmp/ai-insight-corpus-v1.json \
  --out /private/tmp/ai-insight-evaluation-v1.json \
  --confirmed-anonymized --runs=3
```

The provider run always requires `openai / gpt-6-luna / medium`. In
`--database-config` mode it fails closed if the active Admin selection differs.
It reads only provider settings from the registry, never tenant data or raw
records. Both modes write a new mode-0600 result file under the system temp
directory (`/tmp` in the Linux container, `/private/tmp` on macOS) and
never change serving flags. It generates Sleep, Recovery, and Energy first;
overall then sees the accepted sibling texts or explicit fallback states. A
rejected or failed candidate has no retained model text in the result. The
result records exact corpus hash, input hashes, prompt/reviewer identity,
per-slot outcome and token metadata. Failed slots also carry a bounded
`failure_kind` such as `candidate_rejected`, `review_rejected`,
`review_provider`, `timeout`, or `provider_error`; the result never stores the
failed candidate text or provider error body. Progress logs contain only the
case count. Do not score `valid` as product success by itself.

For comparisons after a packet or prompt revision, rerun the exact same
reviewed corpus and keep its hash unchanged. A separate, newly reviewed
corpus can broaden observed coverage, but must receive its own hash and result
file; never pool the two denominators. In the current AI packet, partial Sleep
does not contribute completed-night duration or a four-day trend containing
that partial day. The distinct recent-seven-night, older headline-baseline,
and four-day windows are described explicitly; Energy drain, strain, and
stress are not treated as one numeric scale. Historical Recovery candidates
must reconstruct same-day HRV sample counts from retained points, otherwise
cache-only snapshots falsely mark them all as `data_accruing`.

In input v5, a Recovery domain packet also receives the bounded
`energy_authoritative_state` fact only when Energy is fresh/final, plus the
Energy domain state even when its reading is partial. Recovery must still cite
at least one Recovery fact. It may additionally contain HRV and resting-HR
aggregates only when their ReadinessEvidence proves same-day freshness, final
confidence, and coverage. A personal comparison is included only when a fresh,
matching headline carries that baseline; otherwise the fact states only the
confirmed value and sample count. This makes a contradictory day-level action
visible without turning either server verdict into an answer key. The v5 author
task asks for what matters, why, and whether it changes an ordinary choice;
the reviewer allows candid caveats and rejects only concrete grounding or
safety failures. The reader-copy guard rejects obvious editing notes, invisible
formatting, gender-alternative/formal-address patterns, and clear gender-marked
Serbian AI first-person forms before review or cache write. It is not a grammar
checker or a replacement for human language review.

Manual review is required for each eligible slot and all three runs:
fidelity to supplied facts, safety, usefulness, naturalness, screen
duplication, clarity of an alternative action, and coherence between overall
and domain opinions. Keep the review artifact local. B1 remains disabled
until a separate acceptance and rollout decision.
