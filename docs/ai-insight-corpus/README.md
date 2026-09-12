# B1 frozen narrative corpus

This directory holds no production health data and no provider outputs. A B1
release starts by placing a reviewed, anonymized JSON corpus outside the
repository and freezing its SHA-256 checksum in the review record.

The corpus JSON has this shape:

```json
{
  "version": "daily-insight-narrative-corpus-v1",
  "cases": [
    {
      "id": "synthetic-001",
      "locale": "ru",
      "origin": "synthetic_controlled",
      "tags": ["synthetic_controlled", "mixed_sleep_baseline", "complete_sleep"],
      "scenario": {
        "checkin": "absent"
      },
      "snapshot": { "...": "a sanitized DailyInsightSnapshot" },
      "narrative_subjects": {
        "energy": "active_recovery"
      }
    }
  ]
}
```

Use 20–30 cases. The validator requires coverage for: mixed sleep baseline,
complete/incomplete sleep, limited history, no check-in, recovery/energy
conflict, late source update, and no data. Required tags are not decorative:
the validator checks their matching snapshot state. In particular, complete
sleep must be fresh/final, incomplete sleep must be unavailable or partial,
limited history must be a non-claim provisional sleep context, no data must be
fallback-only, and a late update needs a timestamp plus
`update_kind: "late_source_update"`. At least one narrative-eligible case is
required for each of EN, RU and SR, so all three localizations receive actual
model candidates rather than only deterministic fallback cases.

Every case has an explicit `origin`. `observed_aggregate` means the packet was
reconstructed from retained aggregate and derived state; it is never a raw
record export. `synthetic_controlled` is allowed only for a narrowly scoped
edge state absent from retained history. Such a case must carry both the
`synthetic_controlled` tag and at least one required product-state tag; the
validator caps the corpus at four synthetic cases. Synthetic cases are safety
fixtures, not claims about the user, and remain visible as a separate group in
the product review. The current scaffold has 17 observed packets and three
controlled fixtures (`limited_history`, `energy_recovery_conflict`, and
`late_source_update`).

`scenario` is review-only provenance, never provider input. Use
`checkin: "absent"` for the no-check-in case. For an
`energy_recovery_conflict` case, include the exact server evidence IDs for both
domains, for example:

```json
"scenario": {
  "conflict_evidence_ids": {
    "recovery": "recovery-evidence-id",
    "energy": "energy-evidence-id"
  }
}
```

Replace dates, IDs, free text and any identifying fields before the corpus
leaves the trusted review boundary. The printed SHA-256 is calculated from the
canonical JSON representation, so formatting-only changes do not alter the
review identity; any snapshot, tag or scenario change does.

`narrative_subjects` is optional and exists only for a closed server-only
variant that is deliberately omitted from the live client snapshot. Today that
is the energy verdict behind `energy_current_verdict_context`; it may be only
`rest`, `active_recovery`, or `push_hard`. It is not free text, never carries a
number, and must be included when an eligible energy claim is frozen. The
evaluator restores this enum before deriving the provider claim packet, so the
review sees the same permitted proposition as production rather than a
defaulted variant.

Freeze and inspect a corpus before supplying a provider key. This performs no
network request and prints the SHA-256 that must be bound to the later review:

```bash
go run ./cmd/daily_insight_eval \
  --corpus /safe/path/frozen-corpus.json \
  --validate
```

## Building candidate packets

`daily_insight_candidates` is the preceding read-only, non-serving step. It
reconstructs candidate snapshots from retained aggregate and derived state for
an explicit tenant schema, then removes original dates, evidence IDs, source
IDs, values, units, display copy and actions. It neither reads
`health_records` payloads nor creates bundles, calls a provider, or writes a
tenant setting. Its output is deliberately **not** a valid corpus: a reviewer
must choose 20–30 cases, attach the required tags/scenario provenance, and
freeze that reviewed JSON outside the repository. Each candidate may carry
`review_hints`, which are only structural selection aids derived from its
sanitized snapshot (for example, fresh final sleep or a narrative-eligible
claim). They are deliberately not corpus tags: `no_checkin`,
`late_source_update`, and `energy_recovery_conflict` require separately
verified provenance and are never inferred by the export.

For the optional `no_checkin` scenario only, the exporter may attach
`scenario.checkin` as `answered` or `absent`. It reads just the row status for
the configured Telegram source: it never reads or exports the answer, message
identifier, or timestamps. Pending, expired, late, and missing rows are all
`absent` because they are not timely labels. If that optional lookup is
unavailable, the scenario remains unannotated rather than guessing. It is
disabled by default and requires an explicit
`--include-checkin-presence` invocation, so the basic aggregate/derived export
does not read the separate check-in table.

`--max` caps the emitted review artifact, not the source scan. The exporter
reads the full explicit date interval, retains one candidate for each closed
locale-aware structural signature, and then fills the remaining limit from
evenly distributed scan positions. This avoids silently reviewing only the
most recent homogeneous days; it does not claim that every required corpus
tag is present.

After a reviewer has inspected the candidate selection, create the local-only
draft. It requires the optional status-only check-in marker only when the
real `no_checkin` scenario is desired; the draft never calls a provider or
writes to a database.

```bash
go run ./cmd/daily_insight_corpus_scaffold \
  --candidates /safe/path/daily-insight-candidates.json \
  --out /safe/path/daily-insight-narrative-corpus-draft.json
go run ./cmd/daily_insight_eval \
  --corpus /safe/path/daily-insight-narrative-corpus-draft.json \
  --validate
```

The scaffold creates a **draft**, not a release approval. Inspect its origin
mix, required tags and deterministic fallback references; freeze the resulting
checksum in the review record before any provider evaluation. The references
are derived from the same closed server claim packet, not copied display text:
the corpus intentionally does not retain personal copy or measurements.

Create an offline review packet before selecting a provider. It contains the
locale, origin, tags, permitted claim propositions and the claim-derived
fallback reference for every case, but makes no network request:

```bash
go run ./cmd/daily_insight_eval \
  --corpus /safe/path/daily-insight-narrative-corpus-draft.json \
  --out /safe/path/daily-insight-narrative-review-packet.json \
  --prepare-review
```

```bash
DATABASE_URL=postgres://... go run ./cmd/daily_insight_candidates \
  --schema health \
  --from 2026-06-01 --to 2026-09-12 \
  --locales en,ru,sr --max 90 \
  --out /safe/path/daily-insight-candidates.json
```

The utility also accepts the standard `PGHOST`/`PGPORT`/`PGDATABASE`/`PGUSER`
environment variables instead of `DATABASE_URL`. It must run with a
read-only role that has access only to the intended tenant schema. An
unavailable candidate becomes an opaque category such as
`no_retained_metrics`, `sleep_claim_unavailable`, or `storage_sqlstate_…` —
the original date, query, and database error text are intentionally not
emitted into the artifact.

Run exactly three independent generations per eligible case, with an explicit
provider/model/reasoning selection:

```bash
go run ./cmd/daily_insight_eval \
  --corpus /safe/path/frozen-corpus.json \
  --out /safe/path/b1-review.json \
  --provider openai --model gpt-5.6-luna --reasoning none \
  --api-key-env OPENAI_API_KEY
```

The evaluator sends only the closed claim packet, never snapshot display copy,
actions, raw health records or credentials. It writes the closed
claim-derived fallback reference next to every candidate narrative for product
review. Mark a case
useful only when the narrative adds meaning without repeating a card,
strengthening a claim, inventing a cause, giving advice, or using medical
language. Provider errors, null domains and fallback-only cases are not
improvements. B1 remains disabled unless all factual/safety checks pass and at
least 70% of the full frozen denominator is judged more useful than fallback.

For every successful generation, fill its `review` object in the output JSON:

```json
"review": {
  "safety": "safe",
  "usefulness": "better_than_fallback",
  "notes": "Adds a calm interpretation without restating the card."
}
```

The only other values are `safety: "violation"` and
`usefulness: "not_better"`. Do not remove cases or unsuccessful runs: the
denominator is the full frozen corpus. Then validate the immutable corpus hash,
all three runs and the 70% rule:

```bash
go run ./cmd/daily_insight_eval \
  --corpus /safe/path/frozen-corpus.json \
  --check /safe/path/b1-review.json
```

The command exits non-zero for a missing review, an invalid or altered
narrative, a reviewer-marked safety violation, fewer than 70% improved cases,
or a review that does not match the frozen corpus or the exact B1 prompt,
response schema and claim-packet contract. A green result is necessary for B1
enablement, but does not authorize a tenant flag or a production rollout.
To make the passing gate durable for one tenant, an authenticated admin submits
the frozen corpus and reviewed evaluation to
`POST /api/admin/today-insights/b1-quality-gate`. The server recalculates the
canonical checksum and gate itself, then stores only checksum/model/reasoning
plus prompt/claim-contract approval metadata. Until that record exists, a
request to enable B1 returns `409`. Changing the active provider, model,
reasoning, B1 prompt, response schema or claim-packet contract invalidates the
effective approval until that exact configuration is reviewed again.
