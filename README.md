# Health Dashboard

Self-hosted server that receives data from the [Health Auto Export](https://www.healthyapps.dev) iOS app, stores it in PostgreSQL, and provides a web dashboard and MCP server for AI-assisted analysis.

## How It Works

```mermaid
flowchart TD
    App["Health Auto Export (iOS)"]

    subgraph Server["Go Server (pure Go, no CGO)"]
        H["/health POST handler"]
        UI["Web Dashboard"]
        MCP["/mcp MCP Server"]
        BF["Backfill Scheduler (debounced 2 min)"]
    end

    subgraph PG["PostgreSQL (shared instance, schema: health)"]
        HR[("health_records")]
        MP[("metric_points")]
        HM[("hourly_metrics")]
        DS[("daily_scores")]
    end

    Claude["Claude / AI"]
    Browser["Browser"]

    App -->|"POST /health X-API-Key"| H
    H --> HR
    H --> MP
    H -->|"trigger"| BF

    BF -->|"aggregate"| HM
    HM -->|"rollup"| DS

    Browser -->|"password auth"| UI
    UI -->|"reads"| DS
    UI -->|"fallback"| MP

    Claude -->|"Bearer token"| MCP
    MCP -->|"reads"| DS
    MCP -->|"fallback"| MP
```

Data is stored in layers:

- **`health_records`** -- raw JSON payloads, never modified
- **`metric_points`** -- parsed time series, append-only (3.7M+ rows)
- **`hourly_metrics`** -- pre-aggregated cache: per-hour per-source aggregates
- **`daily_scores`** -- daily rollups + readiness scores (auto-maintained)

The pre-aggregated tables are built automatically on startup and after each sync. They can be wiped and rebuilt at any time from `metric_points`.

## Quick Start

Requires a running PostgreSQL instance with the `health` schema (see `init.sql` in the [personal-ai-stack](https://github.com/dzarlax/personal_ai_stack) monorepo).

```bash
# Pull and run
docker pull dzarlax/health_dashboard:latest

docker run -d \
  -e DATABASE_URL="postgres://health_user:pass@postgres:5432/aistack?search_path=health" \
  -e API_KEY=your-secret-key \
  -e UI_PASSWORD=your-dashboard-password \
  -p 8080:8080 \
  dzarlax/health_dashboard:latest
```

Web UI will be available at `http://your-server:8080/`.

The image is published on Docker Hub: [`dzarlax/health_dashboard`](https://hub.docker.com/r/dzarlax/health_dashboard).

## Configuration

All configuration is via environment variables:

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | **Yes** | PostgreSQL connection string (e.g. `postgres://health_user:pass@host/db?search_path=health`) |
| `API_KEY` | Recommended | Protects `/health` (data upload) and `/mcp`. If not set -- endpoints are open. |
| `UI_PASSWORD` | Recommended | Password for the web dashboard at `/`. If not set -- UI is open. |
| `ADDR` | No | Listen address. Default: `:8080` |
| `BASE_URL` | No | Used in logs for MCP URL. Default: `http://localhost:8080` |
| `TELEGRAM_TOKEN` | No | Telegram bot token for daily reports. If not set -- reports disabled. |
| `TELEGRAM_CHAT_ID` | No | Telegram chat/user ID to send reports to. |
| `REPORT_LANG` | No | Report language: `en`, `ru`, `sr`. Default: `en`. |
| `REPORT_MORNING_WEEKDAY` | No | Hour (0-23) for morning sleep report on weekdays. Default: `8`. |
| `REPORT_MORNING_WEEKEND` | No | Hour (0-23) for morning sleep report on weekends. Default: `9`. |
| `REPORT_EVENING_WEEKDAY` | No | Hour (0-23) for evening day summary on weekdays. Default: `20`. |
| `REPORT_EVENING_WEEKEND` | No | Hour (0-23) for evening day summary on weekends. Default: `21`. |
| `REPORT_TZ` | No | Timezone for report scheduling (e.g. `Europe/Belgrade`). Default: system local. |

## Health Auto Export Setup

You need **two automations** in [Health Auto Export](https://www.healthyapps.dev) with different Time Grouping, both sending to filtered endpoints. The server automatically keeps only the right metric types from each endpoint -- no manual metric selection needed.

Ready-to-import automation files are in the [`automations/`](automations/) folder:

1. Transfer `automations/hourly.json` and `automations/vitals.json` to your iPhone (AirDrop, iCloud, email)
2. Open each file -- it will import into Health Auto Export automatically
3. Edit each automation in the app:
   - **URL**: replace `YOUR_SERVER:8080` with your actual server address
   - **Headers**: replace `YOUR_API_KEY` with your `API_KEY` value
4. Delete the old single automation (if upgrading from a previous setup)

### Automation 1 -- Hourly (activity & sleep)

1. Open **Health Auto Export** > **Automations** > **Create new**
2. Set **Destination**: `REST API`
3. Set **URL**: `http://your-server:8080/health/hourly`
4. Add **Header**: `X-API-Key: your-secret-key`
5. **Select Health Metrics**: `All Selected` (server filters automatically)
6. **Export Settings**:
   - Export Format: `JSON`
   - Export Version: `v2`
   - **Date Range: `Today`**
   - **Summarize Data: ON**
   - **Time Grouping: `Hours`**
7. **Sync Cadence**: Quantity `5`, Interval `Minutes`

### Automation 2 -- Vitals (heart rate, HRV, SpO2)

1. Create another automation
2. Set **URL**: `http://your-server:8080/health/vitals`
3. Same **Header**: `X-API-Key: your-secret-key`
4. **Select Health Metrics**: `All Selected` (server filters automatically)
5. **Export Settings**:
   - Export Format: `JSON`
   - Export Version: `v2`
   - **Date Range: `Since Last Sync`**
   - **Summarize Data: ON**
   - **Time Grouping: `Default`** (minute-level granularity for heart rate)
6. **Sync Cadence**: Quantity `5`, Interval `Minutes`

The `/health` endpoint (no suffix) still accepts all metrics unfiltered for backward compatibility.

> **Why two automations?** HealthKit redistributes cumulative metrics (steps, calories) across time buckets with fractional values. With minute-level grouping, the sum of these fractions exceeds the actual total by ~20-30%. Hourly grouping uses `HKStatisticsQuery` which returns correctly deduplicated totals. Instantaneous metrics (heart rate, SpO2) don't have this problem -- they are averaged, not summed -- so minute-level grouping preserves useful granularity.

> **Why `/health/hourly` and `/health/vitals`?** Both automations send all metrics, but the server filters: `/health/hourly` keeps only SUM metrics (steps, calories, sleep, distance), `/health/vitals` keeps only AVG metrics (heart rate, HRV, SpO2, temperature). This way you don't need to manually select metrics in the app -- the server handles separation.

## Web Dashboard

Available at `/` -- password protected if `UI_PASSWORD` is set.

Features:
- **Dashboard** -- today's metrics with trend vs yesterday, sparklines, and featured 7-day charts
- **Health Briefing** -- daily summary with z-score readiness, sleep analysis, insights, and health alerts
- **Metrics view** -- full list of available metrics with latest values; click any to open its chart
- **Metric charts** -- time series with auto-bucketing (minute / hour / day)
- **Settings** -- cache status, backfill controls, Telegram notification config, data gap detection, and Apple Health import
- URL hash state -- shareable links like `/#metric=heart_rate&from=2026-01-01&to=2026-01-31`

## MCP Server

Available at `/mcp` for AI analysis via Claude or other MCP-compatible clients.

Authentication: `Authorization: Bearer your-api-key` or `X-API-Key: your-api-key` header.

Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "health": {
      "url": "http://your-server:8080/mcp",
      "headers": {
        "Authorization": "Bearer your-secret-key"
      }
    }
  }
}
```

Available tools:

| Tool | Description |
|---|---|
| `get_health_briefing` | Daily health briefing: composite readiness score (z-score based), sleep analysis, activity, insights, and alerts. Supports `lang` (en/ru/sr). |
| `get_readiness_history` | Composite readiness scores (0-100) for the last N days. |
| `list_metrics` | List all available metrics with record counts and date ranges. |
| `get_dashboard` | Today's summary with trend vs yesterday. |
| `get_metric_data` | Time series for a single metric with minute/hour/day buckets. |
| `summarize_metric` | Statistical summary + daily breakdown for the last N days. |
| `compare_periods` | Compare a metric between two date ranges. |
| `get_sleep_summary` | All sleep phases per night in one response. |
| `find_anomalies` | Days where a metric was statistically unusual. |
| `get_weekly_summary` | Week-by-week aggregates for one or more metrics. |
| `get_personal_records` | All-time best and worst values per metric. |
| `sql_query` | Run any read-only SQL SELECT on the PostgreSQL database. |

## Telegram Reports

When `TELEGRAM_TOKEN` and `TELEGRAM_CHAT_ID` are set, the server sends two daily reports:

- **Morning** (weekday 08:00 / weekend 09:00) -- sleep duration, phases, readiness score, HRV and RHR
- **Evening** (weekday 20:00 / weekend 21:00) -- steps, calories, exercise minutes, cardio summary, top insights

Times are configurable per weekday/weekend via env vars or through the Settings panel in the web UI (DB settings take priority). To get your `TELEGRAM_CHAT_ID`, send any message to your bot and call `https://api.telegram.org/bot<TOKEN>/getUpdates`. Test reports can be sent from the Settings panel.

## Apple Health Import

Import a full Apple Health export (from iPhone Settings > Health > Export All Health Data):

**Via web UI**: Settings > Import (upload the `.zip` file)

**Via CLI**:
```bash
DATABASE_URL=postgres://... go run ./cmd/import --file path/to/export.zip
```

The import streams the XML to avoid memory issues with large files. Percentage metrics (SpO2, body fat, walking asymmetry, etc.) are automatically normalized from Apple Health's fraction format (0.96) to percentage scale (96%).

## Multi-Device Source Priority

When multiple devices record overlapping data (Apple Watch, iPhone, RingConn), the system selects one source per metric per day:

**Activity metrics** (steps, calories, distance, exercise):
1. **Apple Watch** (Ultra / Apple Watch) -- preferred
2. **iPhone** -- fallback
3. **Other** -- last resort

**Sleep metrics** (sleep_total, sleep_deep, sleep_rem, etc.):
1. **RingConn** -- preferred (more accurate sleep tracking than Watch)
2. **Apple Watch** -- fallback
3. **Other** -- last resort

This priority is applied consistently across dashboard, daily scores, readiness computation, and briefing API. For chart visualizations, MAX of per-source totals is used.

### Apple Health Import & Auto Export Conflict Resolution

When re-importing from Apple Health (e.g. to fill gaps from missed Auto Export days), the system automatically removes Auto Export (`Health dash - Hourly`/`Vitals`) data for overlapping dates. Apple Health export is treated as ground truth. A full cache rebuild (force backfill) runs after each import.

## Readiness Score

A composite 0-100 score reflecting current recovery state, computed from z-scores against a 30-day personal baseline.

### Methodology

Each metric is converted to a **z-score** (standard deviations from personal mean), then blended:

- **Today** (60%) -- immediate reactivity to last night's sleep, current HRV
- **7-day trend** (40%) -- accumulated fatigue, sleep debt, training load

Component weights:

| Component | Weight | Direction | Reference |
|-----------|--------|-----------|-----------|
| HRV | 40% | Higher = better | Plews et al. (2013), Buchheit (2014) |
| Resting HR | 25% | Lower = better | Buchheit (2014) |
| Sleep | 35% | Duration + consistency | Walker (2017), Huang et al. (2020) |

Sleep scoring includes:
- **Duration z-score** vs personal baseline
- **Absolute penalty** for <6h or >9.5h (U-shaped mortality curve, Li et al. 2025)
- **Consistency penalty** when 7-day CV >15% (Huang et al. 2020)

Score mapping: `score = 70 + z × 15`, clamped to [0, 100].

| Score | z-score | Meaning |
|-------|---------|---------|
| 70 | 0 | Your personal baseline ("normal you") |
| 85 | +1 | One SD above baseline (good day) |
| 55 | -1 | One SD below baseline (rough day) |
| 100 | +2 | Exceptional (capped) |

### References

- Plews et al. (2013). *Training adaptation and heart rate variability in elite endurance athletes.* IJSPP.
- Buchheit (2014). *Monitoring training status with HR measures.* IJSPP.
- Bellenger et al. (2016). *HRV-guided training improves performance.* MSSE.
- Walker (2017). *Why We Sleep.* Scribner.
- Huang et al. (2020). *Sleep irregularity and cardiovascular disease.* JAHA.
- Li et al. (2025). *Sleep duration and all-cause mortality.* Sleep Medicine Reviews.

## Maintenance

```bash
DATABASE_URL=postgres://... make dev              # run locally for development
DATABASE_URL=postgres://... make build            # compile binary (pure Go, no CGO)
DATABASE_URL=postgres://... make backfill         # rebuild caches incrementally
DATABASE_URL=postgres://... make backfill-force   # wipe and fully rebuild all caches
DATABASE_URL=postgres://... make import FILE=...  # import Apple Health export
make docker-up        # build and start with Docker Compose
make docker-down      # stop all services
```

Cache tables are rebuilt automatically on server startup (incremental, last 48h) and after each sync (debounced, 2-minute delay). Use `make backfill-force` only after code changes to aggregation logic.

## Backups

Data is in PostgreSQL (shared instance). Backup the entire database:

```bash
docker exec infra-postgres-1 pg_dump -U health_user aistack --schema=health > health_backup.sql
```

Or use the unified backup strategy from the personal-ai-stack monorepo (`pg_dumpall`).
