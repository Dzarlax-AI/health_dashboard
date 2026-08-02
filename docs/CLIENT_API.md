# Health Dashboard client API

The client API is the tenant-aware JSON surface shared by the browser UI,
the `health-sync` iOS application, and trusted machine clients. The canonical
machine-readable contract is [`contracts/openapi.json`](../contracts/openapi.json).

## Compatibility policy

Existing `/api/*` paths remain canonical. A parallel `/api/v1/*` tree is not
introduced until there is a real breaking migration to perform.

Response changes are additive by default:

- existing required fields remain required;
- existing field types and nullability remain accepted;
- documented enum values are never removed;
- new optional fields and new open-enum values may be added;
- legacy fields stay until the supported clients no longer depend on them.

`contracts/openapi.compat.json` is the protected compatibility baseline.
`make contract-check` rejects a current contract that removes a baseline
schema, property, required guarantee, accepted type/null value, or enum value.
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
| `GET /api/dashboard` | Lean current-day metric cards | Cards may be empty for a fresh tenant. |
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

## Dates, timestamps, and units

- Calendar dates use `YYYY-MM-DD` in the tenant report timezone.
- Timestamps are JSON strings; EnergyBank v2 `ts` values are RFC 3339
  date-times rendered with the tenant timezone offset.
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
