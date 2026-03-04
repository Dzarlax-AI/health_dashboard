# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make dev          # run server locally (data stored in ./data/health.db)
make build        # CGO_ENABLED=1 binary → bin/server
make migrate      # re-parse raw payloads from health_records → metric_points
make dedup        # rebuild metric_points with UNIQUE constraint, remove duplicates
make docker-up    # docker compose up -d --build
make docker-down  # docker compose down
make test         # send a test POST to localhost:8080/health
```

Build requires CGO (for go-sqlite3). Always use `CGO_ENABLED=1`.

## Architecture

Single binary HTTP server (`cmd/server/main.go`) that wires together three packages:

- **`internal/handler`** — receives health data from the Health Auto Export iOS app via `POST /health`. Parses JSON payload into `[]storage.MetricPoint` and writes to DB. Auth via `X-API-Key` header (env `API_KEY`).

- **`internal/ui`** — web dashboard SPA at `/`. Password-protected via cookie (env `UI_PASSWORD`). Login page at `/login`. API endpoints: `/api/dashboard`, `/api/metrics`, `/api/metrics/data`. The entire frontend is a single embedded Go string (`template.go`), using Chart.js 4 from CDN.

- **`internal/mcpserver`** — MCP Streamable HTTP server at `/mcp` (mark3labs/mcp-go v0.44.1). Auth via `Authorization: Bearer <key>` or `X-API-Key` header (same `API_KEY` env). Tools: `list_metrics`, `get_dashboard`, `get_metric_data`, `summarize_metric`, `sql_query`.

- **`internal/storage`** — SQLite via `go-sqlite3` (CGO). Two tables: `health_records` (raw payloads) and `metric_points` (parsed time series). WAL mode, `UNIQUE(metric_name, date, source)`.

## Data Flow

Health Auto Export → `POST /health` → raw JSON saved to `health_records` + parsed into `metric_points`.

Payload structure from Health Auto Export:
```json
{"data": {"metrics": [{"name": "...", "units": "...", "data": [...]}]}}
```

Special metric handling in `internal/handler/health.go::extractPoints`:
- `heart_rate` → reads `Avg` field (not `qty`)
- `sleep_analysis` → expands to 5 metrics: `sleep_deep`, `sleep_rem`, `sleep_core`, `sleep_awake`, `sleep_total`
- All others → read `qty` field

## SQLite Date Format

Dates arrive with timezone offset: `"2026-03-04 09:02:00 +0100"`. SQLite's `strftime` cannot parse this format — always use `substr(date, 1, N)` for date truncation (not `strftime`).

## Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `DB_PATH` | `/app/data/health.db` | SQLite file path |
| `ADDR` | `:8080` | Listen address |
| `API_KEY` | — | Auth for `/health` and `/mcp` |
| `UI_PASSWORD` | — | Auth for web UI |
| `BASE_URL` | `http://localhost:8080` | Used for MCP server URL in logs |

## One-time DB Migrations

If migrating from an old schema without `UNIQUE` constraint on `metric_points`:
```bash
make dedup    # run once — rebuilds table and removes duplicates
make migrate  # re-parse existing health_records payloads (e.g. after adding new metric types)
```
