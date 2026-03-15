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
make import FILE=export.zip  # import Apple Health export (ZIP or XML)
```

Build requires CGO (for go-sqlite3). Always use `CGO_ENABLED=1`.

## Architecture

Single binary HTTP server (`cmd/server/main.go`) that wires together several packages:

- **`internal/handler`** — receives health data from the Health Auto Export iOS app via `POST /health`. Parses JSON payload into `[]storage.MetricPoint` and writes to DB. Auth via `X-API-Key` header (env `API_KEY`). After a successful insert, calls `onNewData()` to trigger cache invalidation + debounced backfill.

- **`internal/ui`** — web dashboard SPA at `/`. Password-protected via cookie (env `UI_PASSWORD`). Login page at `/login`. API endpoints: `/api/dashboard`, `/api/metrics`, `/api/metrics/data`, `/api/metrics/latest`, `/api/metrics/range`, `/api/health-briefing`, `/api/readiness-history`, `/api/admin/status`, `/api/admin/backfill`, `/api/admin/settings`, `/api/admin/test-notify`, `/api/admin/gaps`, `/api/admin/import/upload`, `/api/admin/import/status`. The entire frontend is embedded Go strings in `internal/ui/` (template, scripts, styles). Uses Chart.js 4 from CDN.

- **`internal/mcpserver`** — MCP Streamable HTTP server at `/mcp` (mark3labs/mcp-go v0.44.1). Auth via `Authorization: Bearer <key>` or `X-API-Key` header (same `API_KEY` env). Tools: `get_health_briefing`, `get_readiness_history`, `list_metrics`, `get_dashboard`, `get_metric_data`, `summarize_metric`, `compare_periods`, `get_sleep_summary`, `find_anomalies`, `get_weekly_summary`, `get_personal_records`, `sql_query`.

- **`internal/health`** — pure business logic for health analysis (no I/O). Readiness scoring (`scoring.go`, `readiness.go`), health anomaly alerts (`alerts.go`), cardio analysis (`cardio.go`), sleep breakdowns (`sleep.go`), activity analysis (`activity.go`), insights generation (`insights.go`), and i18n (`i18n_en.go`, `i18n_ru.go`, `i18n_sr.go`). Core types in `types.go`.

- **`internal/storage`** — SQLite via `go-sqlite3` (CGO). WAL mode. Tables: `health_records`, `metric_points`, three pre-aggregated cache tables (`minute_metrics`, `hourly_metrics`, `daily_scores`), and `settings` (key-value store for Telegram config). Also includes `admin.go` (data gap detection) and `settings.go` (notification config persistence).

- **`internal/notify`** — Telegram notification subsystem. Bot client (`telegram.go`) and report scheduler (`report.go`) with timezone-aware morning/evening scheduling. Config loaded from env vars with DB overrides.

- **`internal/applehealth`** — streaming XML parser for Apple Health export files (`export.xml` or `.zip`). Memory-efficient, maps 100+ HK metric types to internal metric names. Normalizes fraction-based percentage metrics (SpO₂, body fat, etc.) to 0–100 scale during import.

- **`cmd/backfill`** — standalone CLI to rebuild caches. Flags: `--force` / `-f`.

- **`cmd/import`** — standalone CLI to import Apple Health export files. Flags: `--db`, `--file`, `--batch`, `--pause`, `--dry-run`. Streams XML to avoid memory overload.

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
- **Multi-device dedup**: all SUM metrics use `MAX(per-source sum)` across sources to avoid double-counting overlapping devices (Apple Watch + iPhone + RingConn)
- **All others**: AVG

## Cache Invalidation & Backfill

- **On startup**: `RunIncrementalBackfill()` fires after 10 s (fills missing rows only)
- **After `POST /health`**: inline `UpsertRecentCache()` rebuilds hourly+daily for affected dates directly from metric_points (no stale window). Debounced backfill (2 min) runs as safety net to refresh last 48h.
- **ScoreVersion** constant in `scores.go` (currently 2): bump to invalidate all cached readiness scores on next run
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
| `REPORT_TZ` | system local | Timezone for report scheduling (e.g. `Europe/Berlin`) |

## Automatic DB Migrations (run on every startup)

- **Metric name normalization**: renames `heart_rate_variability_sdnn` → `heart_rate_variability`, `oxygen_saturation` → `blood_oxygen_saturation`
- **Fraction→percent normalization**: multiplies ×100 for SpO₂, body fat, walking asymmetry/double support/steadiness values that were imported as fractions (0.0–1.0) from Apple Health XML

## One-time DB Migrations

If migrating from an old schema without `UNIQUE` constraint on `metric_points`:
```bash
make dedup    # run once — rebuilds table and removes duplicates
make migrate  # re-parse existing health_records payloads (e.g. after adding new metric types)
```

After schema changes to `daily_scores`, run `make backfill-force` to rebuild the cache.

## Readiness Scoring

Detailed in `SCORING.md`. Key parameters:
- **Readiness = HRV×40% + RHR×30% + Sleep×30%** (ratio model, 0–100 scale)
- **Recent window**: 7 days for Readiness/Recovery; 3 days for Sleep/Activity/Cardio sections
- **Minimum data**: 9 days (7 recent + 2 baseline)
- **Oversleep penalty**: ≥9h sleep reduces absolute score (U-shaped mortality curve)
- **Health alerts** (not score components): RR anomaly, wrist temp anomaly, HRV CV >15%
- **ScoreVersion**: bump in `scores.go` to invalidate cached scores after formula changes
