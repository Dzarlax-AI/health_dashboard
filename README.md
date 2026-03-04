# Health Processing

Self-hosted server that receives data from the [Health Auto Export](https://www.healthyapps.dev) iOS, stores it in SQLite, and provides a web dashboard and MCP server for AI-assisted analysis.

## How It Works

```
iPhone (Health Auto Export) → POST /health → SQLite → Web UI / MCP
```

Data is stored in two layers:
- **`health_records`** — raw JSON payloads, never modified
- **`metric_points`** — parsed time series, used for queries and charts

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

Example `docker-compose.yml` environment section:

```yaml
environment:
  - DB_PATH=/app/data/health.db
  - API_KEY=your-secret-key
  - UI_PASSWORD=your-dashboard-password
```

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
- **Metric charts** — time series with auto-bucketing (minute / hour / day)
- **Sidebar** — metrics grouped by category (Heart, Activity, Fitness, Sleep, Environment)
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
| `list_metrics` | List all available metrics with record counts and date ranges. Good starting point. |
| `get_dashboard` | Today's summary: steps, calories, heart rate, SpO₂, HRV, sleep. Includes trend vs yesterday. |
| `get_metric_data` | Time series for a single metric. Supports minute / hour / day buckets and AVG / SUM / MIN / MAX aggregation. |
| `summarize_metric` | Statistical summary (avg, min, max, count) + daily breakdown for the last N days. |
| `compare_periods` | Compare a metric between two date ranges. Returns values and `change_pct`. Useful for before/after analysis. |
| `get_sleep_summary` | All sleep phases (deep, REM, core, awake, total) per night in one response. |
| `find_anomalies` | Days where a metric was statistically unusual (configurable σ threshold). |
| `get_weekly_summary` | Week-by-week aggregates for one or more metrics. |
| `get_personal_records` | All-time best and worst values per metric with dates. |
| `sql_query` | Run any read-only SQL SELECT directly on the database for custom analysis. |

## Data Downsampling

To keep the database fast on low-power hardware (NAS, Raspberry Pi), a `downsample` service runs daily at 03:00 and aggregates old data:

- Data older than **14 days** → aggregated to hourly granularity
- Data older than **90 days** → aggregated to daily granularity

This reduces ~25M rows/year (heart rate at per-minute resolution) to ~50k rows, with no loss of raw data — raw payloads remain intact in `health_records` and can be re-parsed at any time.

**To disable downsampling**, comment out the `downsample` service block in `docker-compose.yml`.

**To change thresholds**:
```yaml
environment:
  - DOWNSAMPLE_PASS1_DAYS=14   # minutes → hours
  - DOWNSAMPLE_PASS2_DAYS=90   # hours → days
```

**To run manually**:
```bash
make downsample-dry   # preview only
make downsample       # apply
```

## Maintenance Commands

```bash
make dev              # run locally for development
make migrate          # re-parse health_records → metric_points (run after adding new metric types)
make dedup            # rebuild metric_points with UNIQUE constraint (run once on old databases)
make downsample       # aggregate old data manually
make docker-up        # build and start all services
make docker-down      # stop all services
```

## Backups

The entire database is a single file: `./data/health.db`. Back it up by copying that file. For live backups while the server is running:

```bash
sqlite3 ./data/health.db ".backup ./data/health.db.bak"
```
