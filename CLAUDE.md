# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
DATABASE_URL=postgres://... make dev       # run server locally
DATABASE_URL=postgres://... make build     # pure Go binary → bin/server (no CGO)
DATABASE_URL=postgres://... make backfill  # rebuild pre-aggregated caches incrementally
DATABASE_URL=postgres://... make backfill-force  # wipe and fully rebuild all caches
make docker-up        # docker compose up -d --build
make docker-down      # docker compose down
make test             # send a test POST to localhost:8080/health
DATABASE_URL=postgres://... make import FILE=export.zip  # import Apple Health export
```

Build is pure Go (no CGO). Uses `jackc/pgx/v5` for PostgreSQL. Docker images built via GitHub Actions.

## Architecture

Single binary HTTP server (`cmd/server/main.go`) that wires together several packages:

- **`internal/handler`** — receives health data from the Health Auto Export iOS app via `POST /health`, `/health/hourly`, `/health/vitals`. Uses **accept-then-process**: `InsertRaw` saves raw JSON to `health_records` synchronously and responds 200 immediately; a goroutine then parses, calls `InsertPoints` (chunked `pgx.Batch`, 500/chunk), rebuilds cache, and fires `onNewData()`. Auth via `X-API-Key` header (env `API_KEY`).

- **`internal/ai`** — Gemini API integration. `gemini.go`: `GenerateMorningBriefing(apiKey, model, maxTokens, rawMetricsJSON)` and `ListModels(apiKey)` (for dynamic model dropdown). Prompt embedded from `prompt.txt` via `//go:embed`.

- **`internal/ui`** — web dashboard SPA at `/`. Auth: Authentik ForwardAuth (headers `X-authentik-username`/`X-authentik-email`) or username+password cookie. `guard()` in `handler.go` checks ForwardAuth first, then API key, then session cookie. On ForwardAuth success, issues a 30-day local `auth` cookie (`username|passwordHash`) so sessions survive Authentik token expiry. Login page at `/login`. `/settings` (all users): Telegram config, import. `/admin` (admin-only): cache/backfill, Gemini config, user management. API endpoints: `/api/dashboard`, `/api/metrics`, `/api/metrics/data`, `/api/metrics/latest`, `/api/metrics/range`, `/api/health-briefing`, `/api/readiness-history`, `/api/settings`, `/api/import/upload`, `/api/import/status`, `/api/admin/status`, `/api/admin/backfill`, `/api/admin/settings`, `/api/admin/gaps`, `/api/admin/ai-models`, `/api/admin/users`. The entire frontend is embedded Go strings in `internal/ui/` (template, scripts, styles). Uses Chart.js 4 from CDN. **Static assets** (`static/app.js`, `static/charts.js`) embedded via `embed.FS`, served with `Cache-Control: max-age=3600` — no cache-busting by URL yet; after deploys users need hard-refresh (Cmd+Shift+R).

- **`internal/mcpserver`** — MCP Streamable HTTP server at `/mcp` (mark3labs/mcp-go v0.44.1). Auth via `Authorization: Bearer <key>` or `X-API-Key` header (same `API_KEY` env). Tools: `get_health_briefing`, `get_readiness_history`, `list_metrics`, `get_dashboard`, `get_metric_data`, `summarize_metric`, `compare_periods`, `get_sleep_summary`, `find_anomalies`, `get_weekly_summary`, `get_personal_records`, `sql_query`.

- **`internal/health`** — pure business logic for health analysis (no I/O). Readiness scoring (`scoring.go`, `readiness.go`), health anomaly alerts (`alerts.go`), cardio analysis (`cardio.go`), sleep breakdowns (`sleep.go`), activity analysis (`activity.go`), insights generation (`insights.go`), and i18n (`i18n_en.go`, `i18n_ru.go`, `i18n_sr.go`). Core types in `types.go`.

- **`internal/storage`** — PostgreSQL via `jackc/pgx/v5` connection pool. Tables: `health_records`, `metric_points`, three pre-aggregated cache tables (`minute_metrics`, `hourly_metrics`, `daily_scores`), `settings` (key-value store for Telegram config), and `ai_briefings` (per-day AI insight cache). Also includes `admin.go` (data gap detection), `settings.go` (notification + AI config persistence), and `ai_briefing.go` (AI briefing CRUD + `EnsureAIBriefingsTable`). Schema managed externally via `init.sql` (except `ai_briefings`, auto-created on startup).

- **`internal/notify`** — Telegram notification subsystem. Bot client (`telegram.go`) and report scheduler (`report.go`) with timezone-aware morning/evening scheduling. Config loaded from env vars with DB overrides.

- **`internal/applehealth`** — streaming XML parser for Apple Health export files (`export.xml` or `.zip`). Memory-efficient, maps 100+ HK metric types to internal metric names. Normalizes fraction-based percentage metrics (SpO₂, body fat, etc.) to 0–100 scale during import.

- **`cmd/backfill`** — standalone CLI to rebuild caches. Flags: `--force` / `-f`.

- **`cmd/import`** — standalone CLI to import Apple Health export files. Flags: `--file`, `--batch`, `--pause`, `--dry-run`. Streams XML to avoid memory overload.

## Data Flow

```
POST /health → InsertRaw → health_records → 200 to client (sync, fast)
                               ↓ goroutine
                         InsertPoints (chunked pgx.Batch)
                               ↓
                         metric_points → UpsertRecentCache → hourly_metrics → daily_scores
                                              ↓ [debounced]
                                         backfill scheduler
```

`health_records` is the **source of truth** — metric_points and all cache tables are derived and can be fully rebuilt via backfill.

Reads are cache-first: `daily_scores` → `hourly_metrics` → `minute_metrics` → `metric_points` (fallback).

Payload structure from Health Auto Export:
```json
{"data": {"metrics": [{"name": "...", "units": "...", "data": [...]}]}}
```

Special metric handling in `internal/handler/health.go::extractPoints`:
- `heart_rate` → reads `Avg` field (not `qty`)
- `sleep_analysis` → expands to 5 metrics: `sleep_deep`, `sleep_rem`, `sleep_core`, `sleep_awake`, `sleep_total`
- All others → read `qty` field

## Database

**PostgreSQL** (shared instance, schema `health`). Connection via `DATABASE_URL` env var.

Schema managed by `init.sql` (not by the application). Tables:

```
health_records     — raw JSON payloads, never modified
metric_points      — parsed time series, append-only, UNIQUE(metric_name, date, source)
                     date stored as TEXT (YYYY-MM-DD HH:MM:SS ±TZ)
minute_metrics     — Level 1 cache (no longer actively populated)
hourly_metrics     — Level 2 cache: per-hour per-source aggregates
daily_scores       — Level 3 cache: per-day rollups (hrv_avg, rhr_avg,
                     sleep_*, steps, calories, exercise_min, spo2_avg, vo2_avg, resp_avg)
                     + Level 4: readiness score (0–100) with score_version
settings           — key-value store for Telegram config
ai_briefings       — per-day AI briefing cache: insight TEXT, created_at, sent_at
                     Auto-created via EnsureAIBriefingsTable() on startup (not in init.sql)
```

**Expression indexes** (critical for performance with 3.7M+ rows in metric_points):
- `idx_mp_name_day` — `(metric_name, SUBSTRING(date, 1, 10))`
- `idx_mp_day` — `(SUBSTRING(date, 1, 10) DESC)` — for MAX(date) queries
- `idx_mp_name_units` — partial index for units lookup
- `idx_hm_name_day` — `(metric_name, SUBSTRING(hour, 1, 10))`
- `idx_hm_day_desc` — `(SUBSTRING(hour, 1, 10) DESC)`

**pgx specifics**:
- Connection pool: MaxConns=20, MinConns=5
- SimpleProtocol mode (avoids prepared statement lock contention)
- All SQL uses `SUBSTRING()` (not `substr()`), `$1/$2/$3` placeholders (not `?`)

## Aggregation Rules

Defined in `internal/storage/aggregates.go::SumMetrics` (exported):
- **SUM metrics**: step_count, active_energy, basal_energy_burned, apple_exercise_time, apple_stand_time, flights_climbed, walking_running_distance, time_in_daylight, apple_stand_hour, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake
- **Source priority**: Apple Watch (Ultra) > iPhone > other (RingConn). For dashboard and daily_scores, preferred source is selected via `preferredSourceSQL`. Falls back to MAX(source_total) if no Apple device present.
- **Multi-device dedup (charts)**: MAX(per-source daily sum) across sources
- **All others**: AVG

## Cache Invalidation & Backfill

- **On startup**: incremental backfill (refreshes last 48h). Only force-rebuilds when caches are empty (first import). Use `cmd/backfill --force` for manual full rebuild.
- **After `POST /health`**: inline `UpsertRecentCache()` rebuilds hourly+daily for affected dates directly from metric_points (no stale window). Debounced backfill (2 min) runs as safety net to refresh last 48h.
- **ScoreVersion** constant in `scores.go` (currently 2): bump to invalidate all cached readiness scores on next run
- **Force rebuild**: wipes cache tables, recomputes everything from `metric_points`

## Sleep Dedup

`sleepDedupClause()` in `aggregates.go` excludes midnight summary records (00:00:00) when real sleep fragments exist for the same day+source. Applied to all sleep_* metrics in raw queries and hourly cache building.

## Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | — (required) | PostgreSQL connection string (e.g. `postgres://health_user:pass@host/db?search_path=health`) |
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
| `REPORT_TZ` | system local | Timezone for report scheduling (e.g. `Europe/Belgrade`) |
| `GEMINI_API_KEY` | — | Gemini API key; enables AI morning briefing |
| `GEMINI_MODEL` | `gemini-2.5-flash` | Gemini model; overridable in Admin UI |
| `GEMINI_MAX_TOKENS` | `5000` | Max output tokens for AI briefing; overridable in Admin UI |

## Readiness Scoring

Detailed in `SCORING.md`. Key parameters:
- **Readiness = HRV×40% + RHR×30% + Sleep×30%** (ratio model, 0–100 scale)
- **Recent window**: 7 days for Readiness/Recovery; 3 days for Sleep/Activity/Cardio sections
- **Minimum data**: 9 days (7 recent + 2 baseline)
- **Oversleep penalty**: ≥9h sleep reduces absolute score (U-shaped mortality curve)
- **Health alerts** (not score components): RR anomaly, wrist temp anomaly, HRV CV >15%
- **ScoreVersion**: bump in `scores.go` to invalidate cached scores after formula changes
