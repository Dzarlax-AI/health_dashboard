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

- **`internal/ai`** — Gemini API integration. `gemini.go::generateWithPrompt` is the shared HTTP path (60s client timeout); `blocks.go` orchestrates per-block generation: `GenerateLeafBlocks` runs SLEEP/YESTERDAY/RECOVERY in parallel via `sync.WaitGroup`, then `GenerateRecommendation` consumes their texts. Each block has its own `inputs_hash` (sha256 over its metric subset) so a late HRV update only invalidates blocks that read HRV. `prompt.txt` is a single template with `{{BLOCK_INSTRUCTIONS}}` placeholder; per-block fragments live in `blockInstructions` map. `ListModels(apiKey)` powers the admin model dropdown.

- **`internal/ui`** — web dashboard SPA at `/`. Auth: Authentik ForwardAuth (headers `X-authentik-username`/`X-authentik-email`) or username+password cookie. `guard()` in `handler.go` checks ForwardAuth first, then API key, then session cookie. On ForwardAuth success, issues a 30-day local `auth` cookie (`username|passwordHash`) so sessions survive Authentik token expiry. Login page at `/login`. `/settings` (all users): Telegram config, import. `/admin` (admin-only): cache/backfill, Gemini config, user management. API endpoints: `/api/dashboard`, `/api/metrics`, `/api/metrics/data`, `/api/metrics/latest`, `/api/metrics/range`, `/api/health-briefing`, `/api/ai-briefing`, `/api/readiness-history`, `/api/settings`, `/api/import/upload`, `/api/import/status`, `/api/admin/status`, `/api/admin/backfill`, `/api/admin/settings`, `/api/admin/gaps`, `/api/admin/ai-models`, `/api/admin/users`. The entire frontend is embedded Go strings in `internal/ui/` (template, scripts, styles). Uses Chart.js 4 from CDN. **Static assets** (`static/app.js`, `static/charts.js`) embedded via `embed.FS`. Templates emit URLs as `/static/<file>?v=<hash>` where the hash is computed at process start over the embedded contents (`computeStaticVer` in `render.go`); `BasePage.StaticVer` carries it into every page render. `serveStatic` returns `Cache-Control: public, max-age=31536000, immutable` on a matching `?v=` and the previous 1h policy otherwise — so each deploy invalidates the cached bundle without a hard-refresh. All `lang` query inputs are clamped to `en/ru/sr` via `supportedLang()` so junk values cannot pollute caches.

  **`/api/ai-briefing` is non-blocking by design.** Returns `{insight, blocks, generating, disabled}` and kicks `EnsureTodayAIInsightAsync` on cold cache; `/api/health-briefing` reads AI text from cache only and never waits on Gemini. The web dashboard polls `/api/ai-briefing` every 60s while the tab is visible and the cache is cold (sparkle ✨ marks fresh updates), stops on `disabled` or after the cache is stable for ~10 min.

- **`internal/mcpserver`** — MCP Streamable HTTP server at `/mcp` (mark3labs/mcp-go v0.44.1). Auth via `Authorization: Bearer <key>` or `X-API-Key` header (same `API_KEY` env). Tools: `get_health_briefing`, `get_readiness_history`, `list_metrics`, `get_dashboard`, `get_metric_data`, `summarize_metric`, `compare_periods`, `get_sleep_summary`, `find_anomalies`, `get_weekly_summary`, `get_personal_records`, `sql_query`.

- **`internal/health`** — pure business logic for health analysis (no I/O). Readiness scoring (`scoring.go`, `readiness.go`), health anomaly alerts (`alerts.go`), cardio analysis (`cardio.go`), sleep breakdowns (`sleep.go`), activity analysis (`activity.go`), insights generation (`insights.go`), and i18n (`i18n_en.go`, `i18n_ru.go`, `i18n_sr.go`). Core types in `types.go`.

- **`internal/storage`** — PostgreSQL via `jackc/pgx/v5` connection pool. Tables: `health_records`, `metric_points`, three pre-aggregated cache tables (`minute_metrics`, `hourly_metrics`, `daily_scores`), `settings` (key-value store for Telegram config), `ai_briefing_blocks` (per-block AI cache: SLEEP/YESTERDAY/RECOVERY/RECOMMENDATION × lang × date with `inputs_hash`), and `ai_briefings` (kept as the morning-report `sent_at` lock only — content lives in `ai_briefing_blocks`). Also includes `admin.go` (data gap detection), `settings.go` (notification + AI config persistence), `ai_briefing.go` (legacy `sent_at` CRUD), `ai_briefing_blocks.go` (per-block CRUD + idempotent migration from legacy `ai_briefings.insight`), `ai_orchestrator.go` (`EnsureTodayAIInsight` + `EnsureTodayAIInsightAsync` with single-flight `aiRegenInFlight sync.Map` and 5-min failure backoff `aiRegenLastFailAt`), and `typical_wake.go` (`GetTypicalWakeTime` for adaptive `MorningCapTime`). Schema managed externally via `init.sql` (except `ai_briefings` and `ai_briefing_blocks`, auto-created on startup).

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
ai_briefings       — sent_at lock for HasSentMorningReport (insight column
                     deprecated; content moved to ai_briefing_blocks). Auto-
                     created via EnsureAIBriefingsTable() on startup.
ai_briefing_blocks — per-block AI cache, PRIMARY KEY (date, lang, block).
                     blocks: SLEEP / YESTERDAY / RECOVERY / RECOMMENDATION.
                     inputs_hash = sha256 over the metric subset that drove
                     the block; 'legacy' marker on rows migrated from the
                     old ai_briefings.insight blob. Auto-created via
                     EnsureAIBriefingBlocksTable() on startup.
```

**Expression indexes** (critical for performance with 3.7M+ rows in metric_points):
- `idx_mp_name_day` — `(metric_name, SUBSTRING(date, 1, 10))`
- `idx_mp_day` — `(SUBSTRING(date, 1, 10) DESC)` — for MAX(date) queries
- `idx_mp_name_units` — partial index for units lookup
- `idx_hm_name_day` — `(metric_name, SUBSTRING(hour, 1, 10))`
- `idx_hm_day_desc` — `(SUBSTRING(hour, 1, 10) DESC)`

**pgx specifics**:
- Connection pool **per tenant** (one pool per schema in multi-tenant mode):
  - Multi-tenant: `MaxConns=8, MinConns=2`
  - Single-tenant legacy: `MaxConns=12, MinConns=2`
  - `MaxConnIdleTime=5min` so transient burst conns release promptly
- Pool budget rationale: shared Postgres has `max_connections=50` and is used by authentik + other services. Each tenant's pool is sized so that `tenants × MaxConns + concurrency overhead` stays under ~30, leaving headroom for the rest of the stack. Bumping MaxConns has historically starved authentik (incidents 2026-05-05 morning + afternoon) — change with care.
- SimpleProtocol mode (avoids prepared statement lock contention)
- All SQL uses `SUBSTRING()` (not `substr()`), `$1/$2/$3` placeholders (not `?`)
- Backfill parallelism (`backfillConcurrency`, `dailyConcurrency` in aggregates.go) is `2` to fit the per-tenant budget — raise only after raising MaxConns

## Aggregation Rules

Defined in `internal/storage/aggregates.go::SumMetrics` (exported):
- **SUM metrics**: step_count, active_energy, basal_energy_burned, apple_exercise_time, apple_stand_time, flights_climbed, walking_running_distance, time_in_daylight, apple_stand_hour, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake
- **Source priority**: Apple Watch (Ultra) > iPhone > other (RingConn). For dashboard and daily_scores, preferred source is selected via `preferredSourceSQL`. Falls back to MAX(source_total) if no Apple device present.
- **Multi-device dedup (charts)**: MAX(per-source daily sum) across sources
- **All others**: AVG

## Cache Invalidation & Backfill

- **On startup**: incremental backfill (refreshes last 48h). Only force-rebuilds when caches are empty (first import). Use `cmd/backfill --force` for manual full rebuild.
- **After `POST /health`**: inline `UpsertRecentCache()` rebuilds hourly+daily for affected dates directly from metric_points (no stale window). Debounced backfill (2 min) runs as safety net to refresh last 48h.
- **ScoreVersion** constant in `scores.go` (currently 3): bump to invalidate all cached readiness scores on next run
- **Force rebuild**: wipes cache tables, recomputes everything from `metric_points`

## Sleep Dedup

`sleepDedupClause()` in `aggregates.go` excludes midnight summary records (00:00:00) when real sleep fragments exist for the same day+source. Applied to all sleep_* metrics in raw queries and hourly cache building.

## Sleep Source Cross-Validation

Sleep cross-validation SQL (Apple Watch > RingConn with MIN > 1h-gated take-MIN-when-divergent rule) lives in helper `sleepCrossValidationPickExpr(valCol string)` in `internal/storage/aggregates.go`. **Never copy this SQL inline** — there were five parallel copies that drifted apart over PRs #8 → #9 → #10 (point-fix per copy). The MIN > 1h gate is critical: without it, a RingConn 0.x-hour daily-summary stub satisfies "MAX > MIN×1.4" and clobbers Apple Watch's real 7+h night.

Three callsites for the rule, by SQL shape:
- `sleepCrossValidationPickExpr(valCol)` (value-twin, aggregate over GROUP BY): used by **read paths** — `metricDataDayFromHourly`, `metricDataRaw`, `briefing.go::fetch`, plus the separately-shaped `preferredSleepSourceSQL` CTE constant for `source_totals` callers.
- `sleepCrossValidationPickSourceExpr(table, valCol)` (source-twin, scalar subquery): used by `upsertDailyForDate` — picks ONE source per night so all five sleep_* stages come from the same device, with a `sleep_picked_complete` gate so the block is written atomically (all-or-nothing) to avoid mixing sources across reruns.
- `buildDailySleepBlock` (multi-day, force/backfill writes): mirrors the source-twin's logic but inlined because the multi-day GROUP BY shape doesn't fit the helper's flat-table assumption. **Threshold and source-priority constants (1.0h floor, 1.4× divergence, Apple Watch > RingConn > MAX) MUST be kept in lockstep with both helpers above** — comment in `buildDailySleepBlock` is a load-bearing reminder.

`buildDailyMetricCol` no longer handles sleep_* — it short-circuits with a log line if asked. All daily sleep writes go through `upsertDailyForDate` (single-day inline) or `buildDailySleepBlock` (multi-day backfill).

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
| `REPORT_MORNING_CAP` | `morningHour+4` (floor 11) | Smart-retry deadline for morning report — past this hour the report force-sends with a stale-data banner regardless of `SleepSettled`. Server prefers an adaptive cap from `GetTypicalWakeTime(14) + 60min` when ≥7 days of per-segment sleep data are available; falls back to this env value otherwise. Override via env or `report_morning_cap` setting |
| `REPORT_TZ` | system local | Timezone for report scheduling (e.g. `Europe/Belgrade`) |
| `GEMINI_API_KEY` | — | Gemini API key; enables AI morning briefing |
| `GEMINI_MODEL` | `gemini-2.5-flash` | Gemini model; overridable in Admin UI |
| `GEMINI_MAX_TOKENS` | `5000` | Max output tokens for AI briefing; overridable in Admin UI |

## Telegram Report Layout

Morning + evening reports built in `internal/notify/report.go` (`formatMorning` / `formatEvening`). Two design rules that have been hard-won — don't break them without conscious reason:

- **Rule-based bullets are authoritative; AI complements, never replaces.** The numeric bullets (from `internal/health/scoreSleep`/`scoreActivity`/`scoreRecovery`/`scoreCardio`) come from medical-literature heuristics. Gemini's prose is layered underneath as `🤖 <i>...</i>` italic. If AI hallucinates, the user can sanity-check against the bullets above. Applies to all sections; `🎯 Recommendation` is AI-only because there's no rule-based equivalent.
- **AI insight is generated and stored per block, not as a single blob.** `internal/ai/blocks.go` runs SLEEP/YESTERDAY/RECOVERY in parallel and RECOMMENDATION afterwards; each block lands as its own row in `ai_briefing_blocks` keyed by `inputs_hash`. `formatMorning` reads the four blocks via `db.GetAIBlocks(today, lang)` — no parser needed. Each block lands under its matching rule-based section.

Morning order: Header → Headline → stale warning → EnergyBank → Readiness → Alerts → Sleep → Yesterday (activity+cardio) → Recovery → Recommendation. Evening is intentionally slimmer: dashboard cards (today so far) → EnergyBank (drained capacity) → Readiness → Alerts → Insights. The evening report does NOT show activity/cardio sections — those describe yesterday and would duplicate the morning report.

**Smart-retry gate** (`internal/storage/sleep_settled.go::SleepSettled`): morning report only fires when last night's sleep data is "settled" — record exists for today, last segment ended ≥45min ago (handles wake-walk-dog-sleep-again), no fragments ingested in last 20min. Two paths in `cmd/server/main.go`: opportunistic ingest-driven trigger uses `force=false` (defers and waits for next ingest); scheduler poll loop ticks every 15min until `MorningCapHour`, then force-sends with a localised banner. Dedup via `db.HasSentMorningReport(today)`.

**Per-metric freshness banners** (`internal/notify/report.go::computeFreshness` + `internal/storage/freshness.go::MetricsLastSeen`): when sensor data is stale, replace whole sections with banners instead of showing days-old numbers. Thresholds: sleep silence ≥36h → 😴 banner; HRV+RHR both silent ≥36h → 🔕 watch-off banner; step_count silent ≥24h → 📵 phone-off banner.

## Quality Validation

Three layers of data-quality safety on `metric_points`:

1. **Hard-impossible drop on ingest.** `internal/health/quality.go` defines per-metric physiological ranges (HRV 4–300ms, RHR 28–150bpm, SpO2 70–100%, etc). `internal/handler/health.go::filterImpossible` drops out-of-range points before insert with rate-limited logging (`[QUALITY] drop ...`). Metrics without a configured range pass through unvalidated.
2. **`quality` column flag** on `metric_points` (`TEXT NOT NULL DEFAULT 'ok'`, added via ALTER in `internal/storage/indexes.go::EnsureIndexes`). Values: `ok` | `impossible` | `suspect`. Partial index `idx_points_quality_metric` covers the hot path `WHERE quality='ok'`.
3. **Soft-suspect z-score sweep** (`internal/storage/quality_audit.go::MarkSuspectPoints`): for autonomic metrics only (HRV, RHR, SpO2, Resp, wrist_temperature, VO2, body_mass), flags points >3σ from a 30-day baseline as `suspect`. Behavioural metrics (steps, calories, exercise) are excluded — their bimodal rest/active distribution makes z-score noisy. Legacy cleanup: `MarkExistingImpossible` retroactively flags points outside ranges.

Baseline reads filter `quality='ok'` (5 sites in `aggregates.go`, 9 in `briefing.go`); suspect/impossible flags exclude points from scoring. `ScoreVersion=3` was bumped when this filter was wired in to force cached `daily_scores` to be rebuilt cleanly. `runDailyQualityScan` goroutine (started per-tenant in `cmd/server/main.go`) ticks at 03:00 in the tenant's `REPORT_TZ` and calls `MarkSuspectPoints(7, 3)` so flags accumulate without manual `/admin` button presses.

Admin UI section "Data quality audit" on `/admin` exposes three buttons: `GET /api/admin/quality-audit` (read-only), `POST /api/admin/quality-fix` (runs MarkExistingImpossible + MarkSuspectPoints), `POST /api/admin/quality-digest` (force-sends weekly Telegram digest). Weekly digest auto-fires on the configured day-of-week (`weekly_digest_dow` setting, default Monday=1) before the morning report; suppressed when there are no findings.

## Readiness Scoring

Detailed in `SCORING.md`. Key parameters:
- **Readiness = HRV×40% + RHR×30% + Sleep×30%** (ratio model, 0–100 scale)
- **Recent window**: 7 days for Readiness/Recovery; 3 days for Sleep/Activity/Cardio sections
- **Minimum data**: 9 days (7 recent + 2 baseline)
- **Oversleep penalty**: ≥9h sleep reduces absolute score (U-shaped mortality curve)
- **Health alerts** (not score components): RR anomaly, wrist temp anomaly, HRV CV >15%
- **ScoreVersion**: bump in `scores.go` to invalidate cached scores after formula changes
