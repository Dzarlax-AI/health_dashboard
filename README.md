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
git clone <repo>
cd health_processing

# Copy and edit config
cp docker-compose.yml docker-compose.override.yml
# Edit docker-compose.override.yml — set API_KEY and UI_PASSWORD

docker compose up -d
```

Web UI will be available at `http://your-server:8080/`.

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

Available tools: `list_metrics`, `get_dashboard`, `get_metric_data`, `summarize_metric`, `sql_query`.

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
