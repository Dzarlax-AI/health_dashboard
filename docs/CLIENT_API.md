# Health Dashboard client API

The client API is the tenant-aware JSON surface shared by the browser UI,
the `health-sync` iOS application, and trusted machine clients. The canonical
machine-readable contract is [`contracts/openapi.json`](../contracts/openapi.json).

## Compatibility policy

Existing `/api/*` paths remain canonical. A parallel `/api/v1/*` tree is not
introduced until there is a real breaking migration to perform.

Contract changes are additive by default:

- existing paths, operations, parameters, and success response schemas remain;
- existing required fields remain required;
- existing field types and nullability remain accepted;
- existing query-parameter values remain accepted;
- documented enum values are never removed;
- new optional fields and new open-enum values may be added;
- legacy fields stay until the supported clients no longer depend on them.

`contracts/openapi.compat.json` is the protected compatibility baseline.
`make contract-check` rejects a current contract that removes a baseline path,
operation, parameter, success response schema, response schema/property,
required guarantee, accepted type/null value, or enum value.
Updating that baseline is a breaking-change review action, not a routine
generation step.

## Authentication and tenant resolution

The same endpoints support two client authentication modes:

| Client | Credential | Tenant resolution |
|---|---|---|
| iOS and trusted machine clients | `X-API-Key` header | Backend resolves the matching tenant from the registry. |
| Browser | Opaque `auth` cookie | Backend resolves the session user and tenant from the registry. |
| Browser behind Authentik | Trusted ForwardAuth headers establish a local session | Headers are accepted only from configured trusted proxy networks. |

Public client endpoints do not accept `tenant`, `tenant_id`, `schema`, or
`schema_name` selectors. Query parameters and arbitrary headers cannot replace
the authenticated tenant context. Admin-only cross-tenant operations are a
separate explicit surface and are not part of this client contract.

Clients must not send `X-authentik-*` headers themselves.

## Locale behavior

Server-localized content accepts `?lang=en|ru|sr`. Any other value falls back
to English. The iOS app intentionally has two localization layers:

- native chrome follows the iOS app locale;
- health content follows the server `report_lang` returned by `/api/settings`
  and is forwarded as the `lang` query parameter.

## Phase-one contract coverage

The first TypeScript dashboard depends on these machine-described operations:

| Endpoint | Purpose | Important partial states |
|---|---|---|
| `GET /api/health-briefing` | Rich current-day health state | Optional headline, EnergyBank, sleep quality, readiness serving state, illness/context/check-in signals. `sleep` is required but may be `null`. |
| `GET /api/ai-briefing` | Non-blocking AI narrative | `generating=true` means poll; `disabled=true` means hide AI; empty content is valid. |
| `GET /api/today-insights` | Server-owned Today hero and Sleep/Recovery/Energy explanations | Today-only. Every primary/domain insight has a non-empty `title`, `observation`, and `meaning`; `answer_kind` distinguishes confirmed personal evidence, a provisional pattern, factual context, and data guidance. |
| `GET /api/dashboard` | Lean cache-backed metric cards | Returns the latest complete hourly-cache day and may be empty for a fresh tenant; it never synchronously scans raw metric points. |
| `GET /api/readiness-history` | Readiness trend | Empty `points` is valid; `days` is clamped to the documented range. |
| `GET /api/energy-history` | Day or intraday EnergyBank trend | The `granularity` discriminator selects day or hour point shape; empty `points` is valid. |

The OpenAPI response schemas are generated from the Go values encoded by the
handlers. Named transport envelopes live in `internal/api`; health calculation
and persistence types remain in `internal/health` and `internal/storage`.

## Existing iOS call inventory

The companion application is currently located at:

`../health-sync/health-sync/ServerClient.swift`

Its read-only dashboard calls are:

| Endpoint | iOS model / use | Contract status |
|---|---|---|
| `/api/health-briefing` | `BriefingResponse`, Today | Phase-one OpenAPI |
| `/api/ai-briefing` | `AIBriefingResponse`, Today polling | Phase-one OpenAPI |
| `/api/dashboard` | `DashboardResponse`, lean cards | Phase-one OpenAPI |
| `/api/readiness-history` | `[ReadinessPoint]`, Today/Trends | Phase-one OpenAPI |
| `/api/settings` | `UserSettings`, server content locale and account display | Existing endpoint; full settings contract belongs to #219 |
| `/api/metrics` | `[MetricSummary]`, localized metric catalogue | Existing endpoint; read-only expansion belongs to #218 |
| `/api/metrics/latest` | `[LatestValue]` | Existing endpoint; read-only expansion belongs to #218 |
| `/api/metrics/data` | `MetricDataResponse` | Existing endpoint; read-only expansion belongs to #218 |
| `/api/metrics/range` | `MetricDateRange` | Existing endpoint; read-only expansion belongs to #218 |
| `/api/section/{key}` | `SectionResponse` | Existing endpoint; read-only expansion belongs to #218 |
| `/api/sections` | `SectionsCatalogueResponse` | Existing endpoint; read-only expansion belongs to #218 |

The iOS request client sends `Accept: application/json`, refuses to follow an
authentication redirect silently, and decodes server errors separately from
JSON responses.

### AI briefing compatibility

`sections[]` is the canonical ordered and extensible AI shape. Released iOS
models also understand the combined `insight`, `blocks`, and named top-level
fields (`sleep`, `yesterday`, `recovery`, `recommendation`). The server emits
all of these compatibility representations from the same cached blocks.

New clients should render `sections[]`, then fall back to named blocks or
`insight` only when talking to an older server.

### Today Insights safety boundary

`/api/today-insights` is a personal-observation surface, not a medical
monitor. The server owns facts, data state, evidence and any action; a client
must not infer a diagnosis, prognosis or treatment from `answer_kind`.

- `confirmed_personal` means a closed server claim passed its documented
  evidence gate.
- `provisional_pattern` describes current, not-yet-final data and never carries
  a health action.
- `factual_context` explains an available fact without claiming an individual
  physiological conclusion.
- `data_guidance` identifies a material data limitation. `gap_reason` is
  machine-readable and `remediation_id`, when present, is an optional data
  action rather than health advice.

The optional `claim_id` is server-owned. The current B0 sleep claim,
`recent_sleep_below_reference`, is disabled by default and can use only the
canonical `completed_night_sleep` records. It never falls back to a dashboard
sleep card or `daily_scores.sleep_total`. Its optional `next_step` with ID
`wind_down` belongs only to the Sleep domain; it does not replace the main
daily decision or authorize an AI provider to create another action.

AI framing for Today Insights is separately opt-in (`today_insights_b1_enabled`)
and disabled by default. It additionally requires a tenant-scoped record of a
passing frozen-corpus quality gate; a bare boolean cannot activate a provider.
A provider outage, invalid response or disabled flag never removes the
deterministic server explanation.

When B1 is enabled and a domain-specific provider response passes validation,
the corresponding `domains[].insight` may additionally contain:

```json
"narrative": {
  "text": "A short, evidence-grounded explanation.",
  "claim_ids": ["server_claim_id"],
  "evidence_ids": ["server_evidence_id"]
}
```

This overlay is optional and applies only to Sleep, Recovery, or Energy. It
does not replace the server-owned `observation`, `meaning`, `next_step`,
evidence, or data state. Clients may prefer non-empty `narrative.text` for
presentation, but must fall back to `observation` and `meaning` when it is
absent. Clients must not interpret its prose as a new claim or construct an
action from it.

Admins inspect or change these tenant-scoped rollout gates through
`GET`/`POST /api/admin/today-insights/config`; the POST body may contain only
the boolean keys `today_insights_b0_enabled` and
`today_insights_b1_enabled`. Enabling a gate is an operator decision after
coverage or output review, never an automatic side effect of deployment. B1
returns `409 Conflict` until an admin submits a reviewed corpus/evaluation
artifact to `POST /api/admin/today-insights/b1-quality-gate`; the server
independently revalidates its canonical checksum, three runs and quality rule,
then persists only the approval metadata. `GET /api/admin/today-insights/config`
also exposes `b1_quality_gate_approved`.
It also exposes `b1_quality_gate_matches_active_ai`: changing the active
provider, model, reasoning, B1 prompt, response schema, or claim-packet
contract makes B1 ineffective until that exact configuration has a newly
recorded quality gate.

## Dates, timestamps, and units

- Calendar dates use `YYYY-MM-DD` in the tenant report timezone.
- Timestamps are JSON strings; EnergyBank v2 `ts` values are RFC 3339
  date-times rendered with the tenant timezone offset.
- `DashboardResponse.last_updated` remains the latest ingest receipt timestamp
  (`health_records.received_at`); it is not a cache bucket label.
- `DashboardResponse.cache_state` is `complete`, `updating`, or `unavailable`.
  A response in `updating` continues to serve the prior complete snapshot;
  `cache_completed_at` is the RFC 3339 timestamp of that snapshot and is
  absent only when no complete snapshot exists.
- Older metric endpoints may expose database timestamp strings. Consumers
  must not reinterpret them as UTC without an explicit offset.
- Units are server-provided strings. Clients format them but do not recompute
  health values or aggregation semantics.

## Errors

Current guarded client endpoints preserve existing behavior:

- `200 application/json` for successful responses;
- `302` with `Location` for missing interactive authentication or initial
  setup;
- `400 text/plain` where an endpoint rejects an invalid parameter;
- `500 text/plain` when the tenant read or JSON encoding fails.

The API does not yet impose a new JSON error envelope because changing all
released client error handling is outside issue #212. A future additive error
contract should be introduced deliberately and continue serving legacy
clients during its compatibility window.

## Generation and verification

```bash
make contract-generate  # refresh contracts/openapi.json
make contract-check     # validate generation drift and compatibility
```

Do not regenerate `contracts/openapi.compat.json` during normal development.
It changes only after an explicitly reviewed breaking-contract decision.
The generator refuses that file as an output path, and CODEOWNERS requires an
explicit owner review when the protected baseline changes.
