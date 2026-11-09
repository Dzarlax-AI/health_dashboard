# Insights and report delivery

The final `TodayGuidance`/`DailyDecision` remains authoritative. Recovery describes
its own indicators; Energy describes reserve and respects final safety caps.
Both AI pipelines use a bounded derived fact context. Raw records, device
identities and credentials never enter that context or the report archive.

## AI lifecycle

Independent Today slots refresh after derived-state work, at startup, on a
one-minute lifecycle, and on a page request. Existing tenant preview/enabled
gates, material debounce, leases, compare-and-swap and provider backoff remain
in force. Only the configured tenant language is prepared automatically.
Sleep, Recovery and Energy settle before Overall; intentional null is terminal
for the exact material. A changed provider/input/prompt fingerprint invalidates
current text without relabelling retained text as current.

`ai_generation_status` observes the existing workers. It does not authorize or
reserve generations. Safe reasons and attempt/success timestamps accompany
Today slot metadata; `/api/ai-briefing` can return morning bundle diagnostics.
Historical reads never start generation.

The morning bundle adds `ENERGY` to its single structured call. The server owns
`SYNTHESIS` and `RECOMMENDATION`; four domain explanations share one evidence
identity. Incomplete capture can use a server-selected limitation; complete recorded sleep receives an actual analysis before calendar finalization. The archive
distinguishes model prose, server-selected text and deterministic output.

## Morning sleep and timing

`MorningSleepReadiness` is separate from canonical complete/final sleep. A
stable observed episode requires matching wake evidence, source consistency,
continuous intervals and the existing input-change quiet window. It can be
described in the morning without treating the future noon boundary as a gap.
This scheduler state does not grant definitive completed-night claims or change physiology scoring. Analysis of current observations is independent of calendar finalization.
A usable same-day complete/final night takes precedence over observed sleep.
Wake confirmation accepts the detector's existing high-confidence path and its
medium-confidence passive timeout paths, with their original minimum ages. It
still requires exact source/hash/wake agreement and the content quiet window;
receipt-only replays do not reset that window. A stable episode has no blanket
warning about a future calendar boundary.

Today narrative claims use canonical sleep finality, even when the legacy sleep
score is final. A complete plausible recorded episode can display a fresh
duration while retaining provisional confidence. Its evidence includes source-aligned current-night architecture, recorded wake periods and an explicitly labelled recent-record mean. Finalized-night reference claims remain separate. Actual partial capture keeps its specific limit.

The detailed message contains Sleep, Recovery, Energy and dated yesterday
activity. Historical averages are labelled with their period. Missing evidence
gets a domain-specific caveat. Existing schedule, wake gates, cap time and fixed
three-minute AI wait budget remain; the check-in does not block the report.

Receipt time and opaque iOS generation IDs cannot order two different delayed
snapshots. Identical replays are idempotent, but distinguishing an old replay
from a genuine correction needs a trusted source clock in a separate client
contract change. The broader wake hash restarts the quiet window once on upgrade.

## Immutable delivery history

`report_history` stores each prepared morning/evening/proactive body, language,
time zone, template revision, derived context and admitted AI metadata. Previews
have their own IDs and never take a scheduled delivery key.
`report_delivery_attempts` records the exact selected body and Telegram receipt
for each rich/plain attempt. Archive and delivery reservation commit before
transport. A confirmed rich rejection may fall back to plain; an unreadable,
incomplete or lost acknowledgment is ambiguous and cannot trigger another send.
Only a fenced reservation with proof of zero transport attempts can recover
automatically after a preparation crash. Unknown legacy reservations stay closed.

Authenticated read-only routes:

- `GET /api/reports/history?from=&to=&kind=&limit=&cursor=`: defaults to the last
  30 local days, 20 rows, at most 100. Cursor order is prepared time and UUID.
- `GET /api/reports/history/{id}`: saved variants, evidence and transport attempts.
  Another tenant's UUID returns 404. HTML is a data string, never executable UI.

Old delivery rows expose metadata with `content_available=false`. Their original
text cannot be reconstructed from today's cache. No automatic history deletion
is introduced. Content stays in tenant storage and its ordinary backups.

## Daytime messages

`report_daytime_enabled` defaults to **false**, independently of environment
defaults. The existing settings screen/API controls it and the local hour window
(`report_daytime_start_hour=9`, `report_daytime_end_hour=21`). Hours are 0–23;
equal bounds are invalid and overnight windows are supported.

An independent minute loop reads current admitted facts. It never calls AI or waits on ingestion. A material recorded-sleep change can send an updated analysis even while provisional and with unchanged daily guidance. Its identity uses five-minute observations and stage/continuity availability, excluding raw sync hashes, technical finality and model phrasing. The update links the previous report, explains the changed observation, and preserves both bodies. A current matching Sleep AI opinion may be used; otherwise the factual comparison remains available. The separately supported final-sleep guidance correction holds Energy and other original caps constant. A delivered analysis that already explains the same current decision suppresses a second guidance message. Ordinary Energy drain and AI rephrasing cannot trigger an analysis update.

Multisignal health alerts stay disabled without a validated existing rule.
Source-gap alerts stay disabled without an authoritative configured-source
contract. Historical presence of a device is not treated as configuration.

`proactive_events` persists episode identity across syncs, restarts and midnight.
Reservation serializes event identity, the archive and delivery ledger under a
tenant lock. All proactive messages, including existing digest/nudge rules,
share a maximum of two reservations per local day and a 60-minute cooldown.
Reserved/sent/ambiguous deliveries consume budget. Morning/evening reports and
interactive check-ins do not. Legacy cadence and migration keys are preserved;
legacy rules never run from the new minute loop. The configurable daytime window
applies to new daytime events; legacy nudges retain their own timing. Quiet hours do not accumulate
a queue; the current situation is evaluated again when the window opens.

## Schema and verification

The new status/archive/event tables are part of tenant schema contract v12.
They are created only by the complete offline migration/provisioning path,
not ordinary runtime housekeeping. Use the existing
[tenant isolation deployment gate](TENANT_ISOLATION_RUNBOOK.md); older binaries
do not satisfy the new identity marker, so rollback uses the same fleet gate.
No destructive data migration or automatic activation is included.

`make test-db-insights-notifications` explicitly exercises the new DB-backed
scenarios in CI. It supplements unit, contract, web and tenant-isolation checks.
Local fake-provider/Telegram tests verify mechanics; they do not establish prose
quality on real health data. The approved frozen-corpus review and real report
acceptance remain release gates before production activation.

## Recorded-night analysis correction

The user rejected the server-authored duration disclaimer and explicitly chose
current analysis followed by a linked update when late data changes it. Complete
plausible capture is now analyzed before calendar finalization. Current-night
architecture is reconstructed from the canonical source's intervals; midnight
summary rows follow the existing architecture exclusion. Source/date/duration-
aligned hourly stage totals remain available if interval reconstruction is
unusable. Missing fields remain unknown, and overlapping stage labels do not
become invented stage totals. The recent-record mean is explicitly distinguished
from an older personal baseline and includes the current record.

Morning v10 sends reader-relevant observations instead of internal finalization,
capture and diagnostic fields. Cache identity covers the exact provider payload
plus the canonical input identity. The independent Sleep pipeline receives the
same architecture/comparison facts under input v9 / prompt v8. No scoring formula
or canonical definitive-claim finalization gate changes. The diagnostic CLI embeds
tzdata, matching the server's timezone contract.

Morning history stores a sleep-analysis identity and exact text. The new
sleep_analysis_update rule uses the existing archive, at-most-once ledger,
episode CAS, time window and shared proactive budget. It remains default-off;
no production settings or sends have been enabled. A DB integration test covers
an update before finality with an unchanged decision, immutable prior text,
revision links, identical replays/finality-only suppression, and an ambiguous
transport result without automatic retry.

Release verification includes unit, contract and targeted DB suites. A real
Russian spot check exercised the observation-only packet; this does not establish
full multi-locale prose acceptance. The storage suite has two independently
reproduced baseline failures, which must remain explicit when reporting skipped
checks. Private health payloads and report previews are not release artifacts.
