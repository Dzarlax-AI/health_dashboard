# Focused Test Coverage Roadmap

This roadmap tracks high-value Health Dashboard test coverage without turning the suite into a slow infrastructure exercise.

## Constraints

- Default CI must stay pure and fast under `make test-unit` / `go test ./...`.
- Fixtures must be synthetic or anonymized. Do not commit personal health exports, screenshots, logs, or raw metric dumps.
- DB-backed tests should skip cleanly unless `HEALTH_DB_TESTS=1` is set. When that flag is set, missing or unreachable Postgres is a failure.
- Coverage thresholds are intentionally deferred until the active product contracts have a useful baseline.

## Test Lanes

| Lane | Command | Contract |
|---|---|---|
| Unit/default | `make test-unit` | Fast pure suite. DB tests skip even if libpq env vars are present. |
| Routine DB | `HEALTH_DB_TESTS=1 make test-db` | Fast sequential DB contract aggregate: UI smoke, Energy smoke, and targeted Readiness DB checks. This is the normal DB edit-loop. |
| UI DB smoke | `HEALTH_DB_TESTS=1 make test-db-ui-fast` | Representative UI/admin DB handler checks. |
| Energy DB smoke | `HEALTH_DB_TESTS=1 make test-db-energy-smoke` | One EnergyBank verdict-band DB contract check. |
| Readiness DB | `HEALTH_DB_TESTS=1 make test-db-readiness` | Runs an explicit readiness storage regex covering baseline persistence/schema drift and chip calibration recompute. Monitoring and broad readiness-adjacent writer families stay out until measured or migrated. |
| Tenant security DB | `HEALTH_DB_TESTS=1 make test-db-security` | Disposable-marker-gated registry/provisioning/migration tests, including real restricted logins and cross-tenant/registry denial. Required before image publication. |
| Import/delivery DB | `HEALTH_DB_TESTS=1 make test-db-import` | XML point/workout precedence, persistent import lease/staging, accepted-record replay state, and at-most-once delivery reservation. Required before image publication. |
| Container restart smoke | CI `container-smoke` job | Builds the runtime image, starts disposable PostgreSQL, ingests synthetic data, sends SIGTERM, restarts, and verifies the derived row. Required before image publication. |
| UI DB full | `HEALTH_DB_TESTS=1 make test-db-ui` | Full `./internal/ui` DB package sweep. Manual/domain verification; currently around 41s on the remote Postgres path. |
| Energy DB full | `HEALTH_DB_TESTS=1 make test-db-energy` | Full EnergyBank verdict-band DB group: `go test ./internal/storage -run "TestComputeUserVerdictBands" -count=1 -timeout=90s`. Domain verification; currently around 22s after fixture narrowing and batched seed inserts. |
| Full Storage DB | `HEALTH_DB_TESTS=1 make test-db-storage` | Full `./internal/storage` DB package run. Manual/pre-merge only; not part of `make test-db`. |

## Current Coverage Map

| Area | Current state | Next useful coverage |
|---|---|---|
| `internal/health` formulas | Strong coverage for readiness, evidence payloads, EnergyBank v2, stress, i18n labels, and anomaly helpers. | Add tests only when formula or evidence contracts change. |
| Apple Health import | Synthetic XML fixtures cover mapped records, empty input, malformed input, sleep-stage mapping, and focused parser edge cases. | Add fixtures for real bug classes after reducing them to minimal synthetic XML. |
| UI and admin APIs | Contract tests cover admin pages, auth/session behavior, webhook dispatch, dashboard sections, and tenant scope. | Pin response-shape changes before frontend code starts depending on them. |
| Storage writers | Readiness redesign, EnergyBank, sleep gates, freshness, calibration, and tenant helpers have focused tests, with DB-backed tests gated. | Prefer bug-driven storage contract tests over broad repository-level sweeps. |
| Notifications | Morning/evening report, freshness banners, smart retry, Telegram webhook, and proactive framework tests cover key behavior. | Add regression tests when notification timing or skip conditions change. |
| CI policy | Default CI runs build, vet, pure tests, tenant-security DB, routine DB, full UI DB, full Energy DB, and container restart smoke. Image jobs depend on all required lanes. Full Storage DB and race detector remain manual. | Move full storage into default CI only after the chronic/acute writer families fit a bounded timeout. |

## Delivered First Batch

Issue #149 starts with a small Apple Health import safety batch:

- `internal/applehealth/testdata/focused_edge_export.xml` is a synthetic fixture with no personal data.
- `TestParseXMLFocusedEdgeFixturePinsImportSafety` checks percent normalization boundaries, duration-derived category metrics, stand-hour mapping, unknown quantity fallback, invalid duration rejection, and unsupported correlation skipping.

## Candidate Follow-Ups

- Dashboard API response stability tests for fields consumed by `static/app.js` and `static/charts.js`.
- Import ZIP wrapper tests around missing `export.xml`, progress status, and parse error propagation.
- Storage contract tests for freshness/readiness behavior when a bug or product change proves the need.
- One informational `go test -cover` baseline after the focused lanes are less sparse, without enforcing a threshold.
