# Architecture

Health Dashboard is a single Go service backed by PostgreSQL. It ingests Apple Health / HealthKit data, stores raw payloads, derives rebuildable cache layers, and serves a private dashboard, Telegram reports, and MCP tools.

## System Boundary

```text
iOS health client
  -> HTTP ingest endpoints
  -> raw records and metric points
  -> cache/backfill layers
  -> scoring and derived signals
  -> dashboard, Telegram, MCP, admin APIs
```

The server is self-hosted. Raw health data remains in the operator's PostgreSQL database unless the operator explicitly connects external services such as Gemini for optional briefing text.

## Major Components

| Component | Location | Responsibility |
| --- | --- | --- |
| HTTP server | `cmd/server` | Process startup, tenant wiring, schedulers, routes |
| Ingest handlers | `internal/handler` | Accept Health Auto Export / health-sync payloads and persist raw JSON |
| Storage | `internal/storage` | PostgreSQL access, cache tables, admin queries, derived snapshots |
| Health logic | `internal/health` | Pure scoring, readiness, EnergyBank, alerts, and report inputs |
| Dashboard UI | `internal/ui` | Private browser dashboard and admin pages |
| Notifications | `internal/notify` | Telegram morning/evening reports and proactive nudges |
| AI briefing | `internal/ai` | Optional Gemini-backed briefing generation |
| MCP server | `internal/mcpserver` | Authenticated assistant-facing tools |
| Apple Health XML import | `internal/applehealth` | Streaming parser for exported Apple Health XML |

## Data Layers

Health Dashboard separates source data from derived data:

- `health_records`: raw JSON payloads, preserved as source-of-truth ingest evidence.
- `metric_points`: parsed time series rows.
- `hourly_metrics`: pre-aggregated cache for common time-series reads.
- `daily_scores`: daily rollups, readiness score, freshness-related fields, and stress/load fields.
- `energy_snapshots`: EnergyBank v2 state-machine snapshots.
- `target_snapshots`, `feature_snapshots`, `naive_baselines`: readiness redesign experiment and monitoring tables.
- `ai_briefing_blocks`: cached AI text blocks keyed by date, language, block, and input hash.

Derived tables should be rebuildable from lower layers. Methodology changes should make cache invalidation or backfill requirements explicit.

## Ingest Flow

```text
POST /health or related ingest endpoint
  -> persist raw payload in health_records
  -> parse metric points
  -> insert metric_points in batches
  -> refresh recent hourly/daily caches
  -> trigger report/backfill callbacks when applicable
```

The ingest path is designed to accept first and process asynchronously where possible. The raw payload is kept even when downstream parsing or cache refresh needs to be revisited.

## Read Flow

Most read paths are cache-first:

```text
daily_scores
  -> hourly_metrics
  -> metric_points fallback
```

Admin and diagnostic endpoints may query deeper layers directly when they need auditability, coverage checks, or source-level details.

## Scoring And Methodology

Scoring is implemented in pure Go where possible and documented separately:

- Readiness and general scoring: `SCORING.md`
- EnergyBank: `ENERGY_BANK.md`
- Stress measurement and validation: `STRESS_MEASUREMENT.md`
- Readiness redesign: `READINESS_REDESIGN_PLAN.md`

Methodology status matters. A score or feature may be:

- `heuristic`: rule-based and useful, but not clinically validated.
- `experimental`: under evaluation and should not drive confident user guidance yet.
- `personalized`: calibrated against the operator's own history.
- `observed_only`: measured and displayed without affecting the score.

Do not treat missing data as evidence of normal physiology. Surface stale, imputed, low-coverage, and warmup states explicitly.

## Multi-Tenant Model

The service can run in multi-user mode. A registry schema maps users to tenant schemas. Tenant-local tables hold health data and derived outputs. Tenant-specific schedulers and callbacks should use the tenant's configured timezone and schema.

Public issues and PRs should avoid real tenant names. Use anonymized labels such as Profile A and Profile B.

## Privacy Model

Health Dashboard is designed for private operation:

- Raw health history stays in the operator's database.
- Public fixtures must be synthetic or anonymized.
- Public issues should not include real health values, schema names, tenant names, API keys, or identifying screenshots.
- Optional AI briefing generation should be treated as an operator choice, not a requirement for core analytics.

## Operational Notes

- The Docker image embeds timezone data through Go's `time/tzdata`; timezone-aware reporting depends on it.
- Startup and ingest-triggered cache refreshes are expected.
- Production deployments should verify container revision, startup logs, dashboard/API smoke, and relevant diagnostic endpoints.
- Scoring changes should include a rollback or disable path when they affect user-facing decisions.

## Contributor Map

- Start with `README.md` for setup and product context.
- Use `CONTRIBUTING.md` for development and privacy expectations.
- Use GitHub Issues for scoped work and Todoist or other private tools only as personal reminders.
- For non-trivial behavior changes, write or update an implementation plan before production code changes.
