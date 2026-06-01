# Health Dashboard

Health Dashboard is a self-hosted personal health intelligence system built around Apple Health / HealthKit data. It combines this Go server with the first-party iOS app, [health-sync](https://github.com/Dzarlax-AI/health-sync), which streams HealthKit metrics in the background and provides a lightweight native dashboard.

It is not just a charting dashboard. The goal is to turn raw sensor history into a daily decision layer for recovery, energy, sleep, stress, activity, and long-term trends.

Apple Health is excellent at collecting data, but it is weak at answering operational questions:

- Is today's data fresh enough to trust?
- Am I recovered enough to train, or should I back off?
- Is my energy capacity actually low, or am I just looking at a noisy readiness score?
- Did last night's sleep improve recovery, or only increase time in bed?
- Are stress signals sustained, acute, or just a missing-data artifact?
- How do today's signals compare with my own baseline, not a generic population average?
- Can an AI assistant answer questions from my real health history without sending everything to a third-party wellness app?

This project keeps the raw data under your control, stores it in PostgreSQL, and builds derived layers on top of it:

- **Readiness** — a recovery-oriented signal based on HRV, resting heart rate, sleep, and data freshness.
- **EnergyBank** — a separate capacity indicator for how much load you can reasonably spend today.
- **Sleep and recovery analysis** — staged sleep, coarse sleep sources, wake timing, and recovery stability.
- **Stress and anomaly detection** — respiratory rate, wrist temperature, HRV variability, sustained load, and illness-style flags.
- **Daily briefings** — Telegram morning/evening reports with rule-based metrics plus optional AI interpretation.
- **Web dashboard** — private browser UI for metrics, trends, admin tools, imports, and calibration.
- **MCP server** — an AI-agent interface for asking questions against your own health database.

The product direction is deliberately conservative: raw data is preserved, derived tables can be rebuilt, missing or stale inputs are surfaced instead of hidden, and new readiness features are promoted only when they beat simple baselines on real data.

For the product and methodology story behind the project, see the [Health Dashboard article series](https://dzarlax.dev/series/health-dashboard/): an 8-part build log covering how the Apple Health receiver evolved into a dashboard that refuses to fabricate numbers when inputs are missing.

## Why This Matters for OSS

Most consumer health apps keep the interesting derived metrics behind closed algorithms and cloud accounts. Health Dashboard takes the opposite route:

Health Dashboard is designed to make personal health analytics auditable: every score should be traceable to raw inputs, formula version, confidence label, and human-readable explanation.

- **Self-hosted Apple Health / HealthKit analytics** — ingest your own data into your own PostgreSQL database.
- **Privacy-first operation** — the server can run without sending raw health history to a third-party wellness platform.
- **Reproducible derived metrics** — caches, daily scores, EnergyBank snapshots, and readiness redesign tables are rebuildable from stored inputs.
- **Evidence-based methodology** — scoring and calibration choices are documented in `SCORING.md`, `ENERGY_BANK.md`, and `STRESS_MEASUREMENT.md`.
- **Explicit missing-data states** — stale, imputed, and low-coverage inputs are surfaced instead of being silently turned into confident scores.
- **MCP interface** — AI assistants can query your health database through authenticated tools without needing a separate export workflow.

## Clients

Two iOS clients can stream data into this server:

- **[health-sync](https://github.com/Dzarlax-AI/health-sync)** — first-party native client (Swift / SwiftUI). Sends 100+ HealthKit metrics in the background, includes a built-in read-only dashboard (Today / Sleep / Trends / Metrics) backed by the server's JSON API, and supports en / ru / sr UI locales.
- **[Health Auto Export](https://www.healthyapps.dev)** — third-party app, original integration path. Still fully supported via `/health`, `/health/hourly`, `/health/vitals`, `/health/workouts`.

Pick one — both write to the same tables and either can be used in isolation. The server doesn't care which client is talking to it as long as the `X-API-Key` header is right.

## How It Works

```mermaid
flowchart TD
    App["Health Auto Export (iOS)"]

    subgraph Server["Go Server (pure Go, no CGO)"]
        H["/health POST handler"]
        UI["Web Dashboard"]
        MCP["/mcp MCP Server"]
        BF["Backfill Scheduler (debounced 2 min)"]
        AI["AI Briefing (internal/ai)"]
        TG["Telegram Notifier"]
    end

    subgraph PG["PostgreSQL (shared instance)"]
        REG[("health_registry.users")]
        HR[("health_&lt;user&gt;.health_records")]
        MP[("health_&lt;user&gt;.metric_points")]
        HM[("health_&lt;user&gt;.hourly_metrics")]
        DS[("health_&lt;user&gt;.daily_scores")]
        AB[("health_&lt;user&gt;.ai_briefings")]
        ABB[("health_&lt;user&gt;.ai_briefing_blocks")]
    end

    Gemini["Gemini API"]
    Claude["Claude / AI"]
    Browser["Browser"]
    Telegram["Telegram"]

    App -->|"POST /health X-API-Key"| H
    H --> HR
    H --> MP
    H -->|"trigger"| BF

    BF -->|"aggregate"| HM
    HM -->|"rollup"| DS

    DS -->|"raw metrics"| AI
    AI -->|"generateContent"| Gemini
    Gemini -->|"briefing text"| AI
    AI -->|"cache"| AB

    AB -->|"ai insight"| UI
    AB -->|"prepend to report"| TG
    TG -->|"morning/evening"| Telegram

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
| `REPORT_MORNING_CAP` | No | Smart-retry deadline (hour 0-23). Past this time the morning report force-sends with a stale-data banner regardless of whether sleep data has settled. Default: `morning_hour + 4` (floor 11). |
| `REPORT_TZ` | No | Timezone for report scheduling (e.g. `Europe/Belgrade`). Default: system local. |
| `GEMINI_API_KEY` | No | Gemini API key for AI morning briefing. Get free at [aistudio.google.com](https://aistudio.google.com/apikey). If not set — AI briefing disabled. |
| `GEMINI_MODEL` | No | Gemini model to use. Default: `gemini-2.5-flash`. Configurable in Admin UI (dropdown fetched from Google API). |
| `GEMINI_MAX_TOKENS` | No | Max output tokens for AI briefing. Default: `5000`. Configurable in Admin UI. |
| `HEALTH_HR_ZONES_BPM` | No | Four ascending BPM borders defining the five HR training zones (Z1<B1, Z2<B2, Z3<B3, Z4<B4, Z5≥B4), e.g. `116,135,154,173`. When set, `/health/workouts` ingest computes time-in-zone per workout. When unset, the `hr_z*_sec` columns stay NULL and ingest still succeeds. |

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

### Automation 3 -- Workouts (optional)

If you want individual workouts (runs, rides, strength sessions) ingested into the `workouts` table — for "how much did I run last month" or "time in Z4 last week" questions — add a third automation. This is independent of Automations 1 and 2: those handle metric points (steps, sleep, HR), this one handles per-workout summaries.

1. Create another automation
2. Set **URL**: `http://your-server:8080/health/workouts`
3. Same **Header**: `X-API-Key: your-secret-key`
4. **Export Type**: `Workouts` (not `Health Metrics`)
5. **Export Settings**:
   - Export Format: `JSON`
   - Date Range: `Since Last Sync`
6. **Sync Cadence**: e.g. `1 Hour` (workouts don't need minute-level freshness)

The endpoint stores summary fields only (duration, distance, energy, average / max HR, etc.) — per-second route polylines and per-minute HR samples are intentionally **not** persisted, since the goal is text-mode analysis, not map rendering. Re-uploads of the same workout are upserted by the workout's stable UUID, so it's safe to re-send the same range.

If `HEALTH_HR_ZONES_BPM` is configured, the ingest path also computes time-in-zone (Z1..Z5) from the per-minute HR samples in the payload and stores five integer columns per workout. Pick zone borders that match your physiology — the Karvonen (HR Reserve) method using observed MaxHR and resting HR is more accurate than the textbook `220 - age` formula.

The `/health` endpoint (no suffix) still accepts all metrics unfiltered for backward compatibility.

> **Why two automations?** HealthKit redistributes cumulative metrics (steps, calories) across time buckets with fractional values. With minute-level grouping, the sum of these fractions exceeds the actual total by ~20-30%. Hourly grouping uses `HKStatisticsQuery` which returns correctly deduplicated totals. Instantaneous metrics (heart rate, SpO2) don't have this problem -- they are averaged, not summed -- so minute-level grouping preserves useful granularity.

> **Why `/health/hourly` and `/health/vitals`?** Both automations send all metrics, but the server filters: `/health/hourly` keeps only SUM metrics (steps, calories, sleep, distance), `/health/vitals` keeps only AVG metrics (heart rate, HRV, SpO2, temperature). This way you don't need to manually select metrics in the app -- the server handles separation.

## Multi-User Mode

The server supports multiple independent users, each with fully isolated data in their own PostgreSQL schema.

### How It Works

- A `health_registry` schema holds the users table (`health_registry.users`).
- Each registered user gets their own schema: `health_<username>` (e.g. `health_alice`).
- All metric data, caches, and settings are isolated per user — no data crosses schema boundaries.
- The server detects multi-user mode automatically when `health_registry.users` is non-empty.

### Prerequisites

The database role must be able to create schemas:

```sql
GRANT CREATE ON DATABASE aistack TO health_user;
```

### First-Time Setup

On first run with an empty registry, the server redirects all traffic to `/setup` — a wizard that creates the first admin user and provisions their schema.

### Adding More Users

Admin users can manage accounts via the **Admin** panel (`/admin` > Users section):
- Create users with username, optional email, and password
- Each new user gets an auto-generated API key and an isolated `health_<username>` schema

### Authentication

Three methods are supported, checked in order:

| Method | How |
|---|---|
| **Authentik ForwardAuth** | Set `TRUST_FORWARD_AUTH=true` (legacy alias: `TRUST_FWD_AUTH=true`). Traefik passes `X-authentik-username` / `X-authentik-email` headers. By default those headers are trusted only from loopback/private proxy addresses; set `TRUSTED_FORWARD_AUTH_NETWORK=<cidr>[,<cidr>...]` or `TRUSTED_FORWARD_AUTH_NETWORKS=<cidr>[,<cidr>...]` when the trusted proxy is outside those ranges. The proxy must strip any client-supplied `X-authentik-*` headers before forwarding. On first request the server also issues a 30-day local cookie, so sessions survive Authentik token expiry. |
| **Username + password** | Login form at `/login`. Cookie valid for 30 days. |
| **API key** | `X-API-Key` header (for iOS app sync and MCP). |

### Legacy Single-User Mode

If no `health_registry` schema exists (fresh install without setup wizard), the server falls back to env-var credentials: `API_KEY` + `UI_PASSWORD`. Data lives in the `health` schema. Backward compatible with existing single-user deployments.

---

## Web Dashboard

Available at `/` -- authentication required.

Features:
- **Dashboard** -- today's metrics with trend vs yesterday, sparklines, and featured 7-day charts
- **Health Briefing** -- daily summary with z-score readiness, sleep analysis, insights, and health alerts
- **Metrics view** -- full list of available metrics with latest values; click any to open its chart
- **Metric charts** -- time series with auto-bucketing (minute / hour / day)
- **Settings** (`/settings`, all users) -- Telegram notification config, Apple Health import
- **Admin** (`/admin`, admins only) -- cache status, backfill controls, AI briefing config (Gemini key, model, max tokens), data gap detection, **data quality audit** (impossible-value scan + soft-suspect z-score sweep + manual weekly digest trigger), user management
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
| `list_workout_types` | Discover the distinct workout activity names recorded (e.g. "Outdoor Run", "Indoor Cycling") with counts and date ranges — call before `list_workouts` / `workout_stats` to find the exact name to filter on. |
| `list_workouts` | List Apple Health workouts (runs, rides, strength) in a date range with summary fields and time-in-HR-zone. Optional name filter. |
| `get_workout` | One workout by its HAE UUID. |
| `workout_stats` | Aggregate counters for workouts in a range: count, total duration, distance, energy, avg/max HR, total time-in-HR-zone. |
| `sql_query` | Run any read-only SQL SELECT on the PostgreSQL database. |

## Telegram Reports

When `TELEGRAM_TOKEN` and `TELEGRAM_CHAT_ID` are set, the server sends two daily reports:

- **Morning** (weekday 08:00 / weekend 09:00) -- Headline (most notable cross-metric signal of the day), Energy Bank verdict ("push hard / moderate / active recovery / rest"), Readiness, Alerts, Sleep, Yesterday (full activity + cardio retrospective), Recovery, plus an AI-generated `🎯 Plan for today`
- **Evening** (weekday 20:00 / weekend 21:00) -- "today so far" snapshot: dashboard cards (steps, calories, exercise), Energy Bank current vs morning capacity, Readiness trend, top insights

Rule-based bullets are always rendered (grounded in medical literature); when Gemini is configured, AI text is layered underneath each section as `🤖 ...` annotation, never as a replacement.

**Smart-retry**: the morning report waits until last night's sleep data has "settled" — record exists, last segment ended ≥45min ago (handles the wake-walk-dog-sleep-again case), no fragments ingested in the last 20min. If conditions aren't met, sending is deferred and retried on each new data batch (or every 15min by the scheduler) until `REPORT_MORNING_CAP`. At the cap, a stale-data banner is prepended and the report fires anyway, so a watch-off day still gets a notification.

**Per-metric freshness banners**: when sensor data is silent, sections are replaced with banners instead of showing days-old numbers — `🔕 Apple Watch off` (HRV+RHR silent ≥36h), `📵 Phone not syncing` (step_count silent ≥24h), `😴 No sleep recorded` (sleep_total silent ≥36h).

Times are configurable per weekday/weekend via env vars or through the Settings panel in the web UI (DB settings take priority). To get your `TELEGRAM_CHAT_ID`, send any message to your bot and call `https://api.telegram.org/bot<TOKEN>/getUpdates`. Test reports can be sent from the Settings panel.

### AI Morning Briefing

When `GEMINI_API_KEY` is set (or configured in the Admin UI), the server generates a personalized morning briefing via Gemini. The four blocks — `SLEEP` / `YESTERDAY` / `RECOVERY` / `RECOMMENDATION` — are produced **per-block in parallel** (3 leaves) plus a recommendation root that consumes the leaf texts. Each block is cached in `ai_briefing_blocks` keyed by an `inputs_hash` over the metric subset that drove it, so a late HRV update only invalidates the blocks that read HRV (RECOVERY + RECOMMENDATION). SLEEP / YESTERDAY stay cached until their own inputs change.

The briefing is triggered after 05:00 when today's step count exceeds 300 (confirming the user is awake). Smart-retry runs every 15 min until `MorningCapHour` is reached (or the adaptive cap from `GetTypicalWakeTime + 60min` when ≥7 days of per-segment sleep data are present); each tick re-resolves block hashes and only the leaves whose hashes changed hit Gemini.

The briefing covers four blocks: sleep quality (phases, fragmentation), yesterday's full day (activity, SpO2, breathing, wrist temperature), recovery (HRV/RHR vs 7-day personal baseline), and a concrete recommendation with specific numbers.

The AI insight also appears in the dashboard hero section, served by a dedicated `/api/ai-briefing` endpoint (returns `{insight, blocks, generating, disabled}`) so a cold cache never blocks `/api/health-briefing`. The web dashboard polls every 60s while the cache is warming and surfaces a ✨ marker on fresh updates. The prompt template is in `internal/ai/prompt.txt`; per-block instructions live in `internal/ai/blocks.go::blockInstructions`.

Key and model are stored in the `settings` table and configurable in Admin UI — the model dropdown is populated dynamically from the Google API (only models supporting `generateContent` are shown). Env vars serve as defaults if DB has no value.

### Weekly Data-Quality Digest

In addition to the daily reports, the server sends a **weekly data-quality digest** every Monday morning (configurable via the `weekly_digest_dow` setting, 0=Sunday … 6=Saturday). The digest counts impossible/suspect values per metric, missed sleep nights, and watch-off windows over the past 7 days. Suppressed when there are no findings to avoid notification fatigue.

Trigger manually from the Admin UI's "Data quality audit" section (also exposes `Run audit` and `Mark suspects + clean impossibles` buttons).

## Apple Health Import

Import a full Apple Health export (from iPhone Settings > Health > Export All Health Data):

**Via web UI**: Settings > Import (upload the `.zip` file)

**Via CLI**:
```bash
DATABASE_URL=postgres://... go run ./cmd/import --file path/to/export.zip
```

The import streams the XML to avoid memory issues with large files. Percentage metrics (SpO2, body fat, walking asymmetry, etc.) are automatically normalized from Apple Health's fraction format (0.96) to percentage scale (96%).

After import, run `make energy-backfill` (CLI) or open **Settings → Historical EnergyBank** (web UI) to compute retrospective EnergyBank snapshots from the imported daily scores. This unlocks per-user verdict band calibration; without it, the cold-start defaults are used until the live orchestrator has accumulated 30+ days of snapshots on its own.

## Multi-Device Source Priority

When multiple devices record overlapping data (Apple Watch, iPhone, RingConn), the system selects one source per metric per day:

**Activity metrics** (steps, calories, distance, exercise):
1. **Apple Watch** (Ultra / Apple Watch) -- preferred
2. **iPhone** -- fallback
3. **Other** -- last resort

**Sleep metrics** (sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_unspecified, sleep_awake):
1. **RingConn** -- preferred (more accurate sleep tracking than Watch)
2. **Apple Watch** -- fallback
3. **Other** -- last resort

Sources that report classified stages (Apple Watch with sleep tracking on) emit `sleep_deep` / `sleep_rem` / `sleep_core`. Sources without stage tracking (iPhone Sleep Schedule, older Apple Watch, RingConn before firmware Y) emit `sleep_unspecified` instead — coarse "just asleep" time that pre-rollout used to inflate `sleep_core`. Stacked sleep charts render `sleep_unspecified` as its own 5th band so a "Core" row only ever means actual Core Sleep stage time.

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

### See also

- [`SCORING.md`](./SCORING.md) — full methodology for all scores (Readiness, Sleep, Activity, Cardio, Energy Bank), with thresholds, references, and per-section calculation logic.
- [`ENERGY_BANK.md`](./ENERGY_BANK.md) — design spec for Energy Bank v2 (asymptotic state machine, cross-user validation methodology, personalization roadmap).

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

Schema migrations (new columns on `daily_scores`, etc.) auto-apply on startup via `EnsureIndexes()` — uses `ADD COLUMN IF NOT EXISTS`, so existing deployments pick them up without manual SQL. Fresh installs get the full schema from `CREATE TABLE IF NOT EXISTS` paths in `internal/storage/db.go`.

### One-off: seed the persistent review tenant

Use `cmd/seed_review_tenant` to provision the local review/screenshot tenant (`review` / `health_review`) with synthetic-only data. The seed is anchored to tenant-local today by default, keeps a rolling 120-day window so today and the previous month are always populated, and rebuilds the derived review tables after inserting points.

```bash
DATABASE_URL=postgres://... go run ./cmd/seed_review_tenant --dry-run
DATABASE_URL=postgres://... go run ./cmd/seed_review_tenant --days 120 --anchor today --reset-window
```

The command refuses non-review usernames/schemas, does not enable Telegram or AI settings, and prints rollback SQL after a successful run.

### One-off: backfill historical `sleep_unspecified`

The v2.3 split routes coarse-asleep time (RingConn, iPhone Sleep Schedule, older Apple Watch) into a dedicated `sleep_unspecified` metric instead of inflating `sleep_core`. New data lands correctly automatically once the v2.3 iOS client (or `cmd/import` with the updated XML mapping) is in use. **Historical rows imported before v2.3 still sit under `sleep_core`** — to retroactively split them:

```bash
DATABASE_URL=postgres://... go run ./cmd/migrate_sleep_unspecified --dry-run  # count candidates
DATABASE_URL=postgres://... go run ./cmd/migrate_sleep_unspecified            # apply + targeted cache rebuild
```

The script does the full job: UPDATE the `metric_points` rows in place, then rebuild **only** the affected `hourly_metrics` and `daily_scores` rows via the same per-date code path the server uses on every `/health` ingest. No `make backfill` step needed — and crucially no `--force` rebuild of the whole history, which would take hours for the sake of a few thousand sleep rows. Predicate widens "no stage siblings" to a **±1 calendar day** window per source so Apple Watch staged nights crossing midnight are correctly recognised and left alone. Idempotent — re-running after a successful pass is a no-op. Skip it entirely if you'd rather keep old days "as-they-were" and only have new days correct.

**Multi-user installs:** the script touches the single schema resolved from `search_path` in `DATABASE_URL`. Run it once per tenant — point `search_path` at each `health_<user>` schema in turn:

```bash
# tenant 1
DATABASE_URL="host=... search_path=health_alice ..." go run ./cmd/migrate_sleep_unspecified

# tenant 2
DATABASE_URL="host=... search_path=health_bob ..." go run ./cmd/migrate_sleep_unspecified
```

(Single-tenant legacy installs keep using the `health` schema and run once.)

**Edge case** ([#76](https://github.com/Dzarlax-AI/health_dashboard/issues/76)): the `±1 calendar day` window protects cross-midnight Apple Watch nights, but as a side effect, if a source ever emitted any `sleep_deep` / `sleep_rem` fragments — even on a single night — then `±1 day` around those nights stays in `sleep_core` regardless of whether those specific rows are coarse. Re-running the script does not retry these. False negatives are silent. For an external operator who wants a stricter pass after a clean re-import, a `--strict-same-day` flag may be added later; the current loose predicate is sized for the common case (Apple Health export + iOS sync).

## Backups

Data is in PostgreSQL. In multi-user mode each user has their own schema. Backup all health schemas at once:

```bash
# All schemas (registry + all user schemas)
docker exec infra-postgres-1 pg_dump -U health_user aistack \
  --schema=health_registry --schema="health*" > health_backup.sql

# Single user schema
docker exec infra-postgres-1 pg_dump -U health_user aistack --schema=health_alice > alice_backup.sql
```

Or use the unified backup strategy from the personal-ai-stack monorepo (`pg_dumpall`).
