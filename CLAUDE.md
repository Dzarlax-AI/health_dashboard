# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make dev              # run server locally (data stored in ./data/health.db)
make build            # CGO_ENABLED=1 binary → bin/server
make migrate          # re-parse raw payloads from health_records → metric_points
make dedup            # rebuild metric_points with UNIQUE constraint, remove duplicates
make backfill         # rebuild pre-aggregated caches incrementally (missing rows only)
make backfill-force   # wipe and fully rebuild all caches from metric_points
make docker-up        # docker compose up -d --build
make docker-down      # docker compose down
make test             # send a test POST to localhost:8080/health
```

Build requires CGO (for go-sqlite3). Always use `CGO_ENABLED=1`.

## Architecture

Single binary HTTP server (`cmd/server/main.go`) that wires together three packages:

- **`internal/handler`** — receives health data from the Health Auto Export iOS app via `POST /health`. Parses JSON payload into `[]storage.MetricPoint` and writes to DB. Auth via `X-API-Key` header (env `API_KEY`). After a successful insert, calls `onNewData()` to trigger cache invalidation + debounced backfill.

- **`internal/ui`** — web dashboard SPA at `/`. Password-protected via cookie (env `UI_PASSWORD`). Login page at `/login`. API endpoints: `/api/dashboard`, `/api/metrics`, `/api/metrics/data`, `/api/health-briefing`, `/api/readiness-history`, `/api/admin/status`, `/api/admin/backfill`. The entire frontend is embedded Go strings in `internal/ui/` (template, scripts, styles). Uses Chart.js 4 from CDN.

- **`internal/mcpserver`** — MCP Streamable HTTP server at `/mcp` (mark3labs/mcp-go v0.44.1). Auth via `Authorization: Bearer <key>` or `X-API-Key` header (same `API_KEY` env). Tools: `get_health_briefing`, `get_readiness_history`, `list_metrics`, `get_dashboard`, `get_metric_data`, `summarize_metric`, `compare_periods`, `get_sleep_summary`, `find_anomalies`, `get_weekly_summary`, `get_personal_records`, `sql_query`.

- **`internal/storage`** — SQLite via `go-sqlite3` (CGO). WAL mode. Four tables: `health_records`, `metric_points`, and three pre-aggregated cache tables (`minute_metrics`, `hourly_metrics`, `daily_scores`).

- **`cmd/backfill`** — standalone CLI to rebuild caches. Flags: `--force` / `-f`.

## Data Flow

```
POST /health → health_records + metric_points → [debounced] backfill scheduler
                                                      ↓
                         metric_points → minute_metrics → hourly_metrics → daily_scores
```

Reads are cache-first: `daily_scores` → `hourly_metrics` → `minute_metrics` → `metric_points` (fallback).

Payload structure from Health Auto Export:
```json
{"data": {"metrics": [{"name": "...", "units": "...", "data": [...]}]}}
```

Special metric handling in `internal/handler/health.go::extractPoints`:
- `heart_rate` → reads `Avg` field (not `qty`)
- `sleep_analysis` → expands to 5 metrics: `sleep_deep`, `sleep_rem`, `sleep_core`, `sleep_awake`, `sleep_total`
- All others → read `qty` field

## Database Schema

```
health_records     — raw JSON payloads, never modified
metric_points      — parsed time series, append-only, UNIQUE(metric_name, date, source)
minute_metrics     — Level 1 cache: metric_points → per-minute per-source aggregates
hourly_metrics     — Level 2 cache: minute_metrics → per-hour per-source aggregates
daily_scores       — Level 3 cache: hourly_metrics → per-day rollups (hrv_avg, rhr_avg,
                     sleep_*, steps, calories, exercise_min, spo2_avg, vo2_avg, resp_avg)
                     + Level 4: readiness score (0–100) with score_version
```

`daily_scores` is the primary read target for briefing and dashboard queries — one row replaces 14+ metric_points queries.

## Aggregation Rules

Defined in `internal/storage/aggregates.go::SumMetrics` (exported):
- **SUM metrics**: step_count, active_energy, basal_energy_burned, apple_exercise_time, apple_stand_time, flights_climbed, walking_running_distance, time_in_daylight, apple_stand_hour, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake
- **Sleep dedup**: sleep metrics use `MAX(per-source sum)` across sources to avoid double-counting two wearables (Apple Watch + RingConn)
- **All others**: AVG

## Cache Invalidation & Backfill

- **On startup**: `RunIncrementalBackfill()` fires after 10 s (fills missing rows only)
- **After `POST /health`**: `InvalidateRecentAggregates(6h)` + `InvalidateRecentScores(3d)` then debounced backfill (2 min window collapses multiple syncs)
- **ScoreVersion** constant in `scores.go`: bump to invalidate all cached readiness scores on next run
- **Force rebuild**: wipes cache tables, recomputes everything from `metric_points`

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
| `TELEGRAM_TOKEN` | — | Telegram bot token; if set with `TELEGRAM_CHAT_ID` — enables daily reports |
| `TELEGRAM_CHAT_ID` | — | Recipient chat/user ID |
| `REPORT_LANG` | `en` | Report language: en/ru/sr |
| `REPORT_MORNING_WEEKDAY` | `8` | Morning report hour on weekdays |
| `REPORT_MORNING_WEEKEND` | `9` | Morning report hour on weekends |
| `REPORT_EVENING_WEEKDAY` | `20` | Evening report hour on weekdays |
| `REPORT_EVENING_WEEKEND` | `21` | Evening report hour on weekends |

## One-time DB Migrations

If migrating from an old schema without `UNIQUE` constraint on `metric_points`:
```bash
make dedup    # run once — rebuilds table and removes duplicates
make migrate  # re-parse existing health_records payloads (e.g. after adding new metric types)
```

After schema changes to `daily_scores`, run `make backfill-force` to rebuild the cache.
