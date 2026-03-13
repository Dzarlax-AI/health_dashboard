# Health Processing

Self-hosted server that receives data from the [Health Auto Export](https://www.healthyapps.dev) iOS app, stores it in SQLite, and provides a web dashboard and MCP server for AI-assisted analysis.

## How It Works

```mermaid
flowchart TD
    App["📱 Health Auto Export (iOS)"]

    subgraph Server["Go Server"]
        H["/health POST handler"]
        UI["Web Dashboard"]
        MCP["/mcp MCP Server"]
        BF["Backfill Scheduler (debounced 2 min)"]
    end

    subgraph SQLite["SQLite Database"]
        HR[("health_records")]
        MP[("metric_points")]
        MM[("minute_metrics")]
        HM[("hourly_metrics")]
        DS[("daily_scores")]
    end

    Claude["🤖 Claude / AI"]
    Browser["🌐 Browser"]

    App -->|"POST /health X-API-Key"| H
    H --> HR
    H --> MP
    H -->|"trigger"| BF

    BF -->|"Level 1"| MM
    MM -->|"Level 2"| HM
    HM -->|"Level 3"| DS

    Browser -->|"password auth"| UI
    UI -->|"reads"| DS
    UI -->|"fallback"| MP

    Claude -->|"Bearer token"| MCP
    MCP -->|"reads"| DS
    MCP -->|"fallback"| MP
```

Data is stored in layers:

- **`health_records`** — raw JSON payloads, never modified
- **`metric_points`** — parsed time series, append-only
- **`minute_metrics` / `hourly_metrics`** — pre-aggregated caches (auto-maintained)
- **`daily_scores`** — daily rollups + readiness scores (auto-maintained)

The pre-aggregated tables are built automatically on startup and after each sync. They can be wiped and rebuilt at any time from `metric_points`.

## Quick Start

```bash
# Download docker-compose.yml
curl -O https://raw.githubusercontent.com/dzarlax/health_dashboard/main/docker-compose.yml

# Set secrets (edit the file or use environment variables)
# API_KEY=your-secret-key
# UI_PASSWORD=your-dashboard-password

docker compose up -d
```

Web UI will be available at `http://your-server:8080/`.

The image is published on Docker Hub: [`dzarlax/health_dashboard`](https://hub.docker.com/r/dzarlax/health_dashboard).

## Configuration

All configuration is via environment variables in `docker-compose.yml`:

| Variable | Required | Description |
|---|---|---|
| `API_KEY` | Recommended | Protects `/health` (data upload) and `/mcp`. If not set — endpoints are open. |
| `UI_PASSWORD` | Recommended | Password for the web dashboard at `/`. If not set — UI is open. |
| `DB_PATH` | No | Path to SQLite file. Default: `/app/data/health.db` |
| `ADDR` | No | Listen address. Default: `:8080` |
| `BASE_URL` | No | Used in logs for MCP URL. Default: `http://localhost:8080` |
| `TELEGRAM_TOKEN` | No | Telegram bot token for daily reports. If not set — reports disabled. |
| `TELEGRAM_CHAT_ID` | No | Telegram chat/user ID to send reports to. |
| `REPORT_LANG` | No | Report language: `en`, `ru`, `sr`. Default: `en`. |
| `REPORT_MORNING_WEEKDAY` | No | Hour (0–23) for morning sleep report on weekdays. Default: `8`. |
| `REPORT_MORNING_WEEKEND` | No | Hour (0–23) for morning sleep report on weekends. Default: `9`. |
| `REPORT_EVENING_WEEKDAY` | No | Hour (0–23) for evening day summary on weekdays. Default: `20`. |
| `REPORT_EVENING_WEEKEND` | No | Hour (0–23) for evening day summary on weekends. Default: `21`. |

## Health Auto Export Setup

1. Open **Health Auto Export** on iPhone
2. Go to **Automations** → Create new automation
3. Set **Export format**: `JSON`
4. Set **Destination**: `REST API`
5. Set **URL**: `http://your-server:8080/health`
6. Add **Header**: `X-API-Key: your-secret-key` (must match `API_KEY`)
7. Choose metrics and sync frequency

The app will POST data periodically. Supported metric types:
- Standard metrics with `qty` field (steps, calories, distance, etc.)
- `heart_rate` — uses `Avg` field from min/max/avg structure
- `sleep_analysis` — automatically split into `sleep_deep`, `sleep_rem`, `sleep_core`, `sleep_awake`, `sleep_total`

## Web Dashboard

Available at `/` — password protected if `UI_PASSWORD` is set.

Features:
- **Dashboard** — today's metrics with trend vs yesterday, sparklines, and featured 7-day charts
- **Health Briefing** — AI-style daily summary with readiness score, sleep analysis, and insights
- **Metrics view** — full list of available metrics with latest values; click any to open its chart
- **Metric charts** — time series with auto-bucketing (minute / hour / day)
- **Settings** — cache status and backfill controls (gear icon, top-right)
- URL hash state — shareable links like `/#metric=heart_rate&from=2026-01-01&to=2026-01-31`

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
| `get_health_briefing` | Full daily health briefing: readiness score, sleep analysis, HRV/RHR recovery, activity, and insights. Best starting point. Supports `lang` (en/ru/sr). |
| `get_readiness_history` | Daily readiness scores (0–100) for the last N days. Score combines HRV trend, RHR, and sleep vs personal baseline. |
| `list_metrics` | List all available metrics with record counts and date ranges. |
| `get_dashboard` | Today's summary: steps, calories, heart rate, SpO₂, HRV, sleep. Includes trend vs yesterday. |
| `get_metric_data` | Time series for a single metric. Supports minute / hour / day buckets and AVG / SUM / MIN / MAX aggregation. |
| `summarize_metric` | Statistical summary (avg, min, max, count) + daily breakdown for the last N days. |
| `compare_periods` | Compare a metric between two date ranges. Returns values and `change_pct`. Useful for before/after analysis. |
| `get_sleep_summary` | All sleep phases (deep, REM, core, awake, total) per night in one response. |
| `find_anomalies` | Days where a metric was statistically unusual (configurable σ threshold). |
| `get_weekly_summary` | Week-by-week aggregates for one or more metrics. |
| `get_personal_records` | All-time best and worst values per metric with dates. |
| `sql_query` | Run any read-only SQL SELECT directly on the database. Schemas for `daily_scores`, `hourly_metrics`, `minute_metrics`, and `metric_points` are documented in the tool description. |

## Telegram Reports

When `TELEGRAM_TOKEN` and `TELEGRAM_CHAT_ID` are set, the server sends two daily reports:

- **Morning** (weekday 08:00 / weekend 09:00) — sleep duration, phases (deep/REM/core/awake), readiness score, HRV and RHR
- **Evening** (weekday 20:00 / weekend 21:00) — steps, calories, exercise minutes, cardio summary, top insights

Times are configurable per weekday/weekend via env vars. To get your `TELEGRAM_CHAT_ID`, send any message to your bot and call `https://api.telegram.org/bot<TOKEN>/getUpdates`.

## Maintenance

```bash
make dev              # run locally for development
make build            # compile binary to bin/server (requires CGO_ENABLED=1)
make migrate          # re-parse health_records → metric_points (run after adding new metric types)
make dedup            # rebuild metric_points with UNIQUE constraint (run once on old databases)
make backfill         # rebuild pre-aggregated caches from metric_points (incremental)
make backfill-force   # wipe and fully rebuild all caches
make docker-up        # build and start with Docker Compose
make docker-down      # stop all services
```

The cache tables (`minute_metrics`, `hourly_metrics`, `daily_scores`) are also rebuilt automatically:
- On server startup (incremental, fills missing rows only)
- After each `POST /health` sync (debounced, 2-minute delay)
- On demand via the Settings panel in the web UI

## Backups

The entire database is a single file: `./data/health.db`. Back it up by copying that file. For a live backup while the server is running:

```bash
sqlite3 ./data/health.db ".backup ./data/health.db.bak"
```
