# Focused Test Coverage Roadmap

This roadmap tracks high-value Health Dashboard test coverage without turning the suite into a slow infrastructure exercise.

## Constraints

- Default CI must stay pure and fast under `go test ./...`.
- Fixtures must be synthetic or anonymized. Do not commit personal health exports, screenshots, logs, or raw metric dumps.
- DB-backed tests should skip cleanly unless a Postgres connection is configured.
- Coverage thresholds are intentionally deferred until the active product contracts have a useful baseline.

## Current Coverage Map

| Area | Current state | Next useful coverage |
|---|---|---|
| `internal/health` formulas | Strong coverage for readiness, evidence payloads, EnergyBank v2, stress, i18n labels, and anomaly helpers. | Add tests only when formula or evidence contracts change. |
| Apple Health import | Synthetic XML fixtures cover mapped records, empty input, malformed input, sleep-stage mapping, and focused parser edge cases. | Add fixtures for real bug classes after reducing them to minimal synthetic XML. |
| UI and admin APIs | Contract tests cover admin pages, auth/session behavior, webhook dispatch, dashboard sections, and tenant scope. | Pin response-shape changes before frontend code starts depending on them. |
| Storage writers | Readiness redesign, EnergyBank, sleep gates, freshness, calibration, and tenant helpers have focused tests, with DB-backed tests gated. | Prefer bug-driven storage contract tests over broad repository-level sweeps. |
| Notifications | Morning/evening report, freshness banners, smart retry, Telegram webhook, and proactive framework tests cover key behavior. | Add regression tests when notification timing or skip conditions change. |
| CI policy | Default CI runs build, vet, and pure tests; race detector is manual and documented. | Keep race detector separate from default PR checks unless the runtime cost becomes acceptable. |

## Delivered First Batch

Issue #149 starts with a small Apple Health import safety batch:

- `internal/applehealth/testdata/focused_edge_export.xml` is a synthetic fixture with no personal data.
- `TestParseXMLFocusedEdgeFixturePinsImportSafety` checks percent normalization boundaries, duration-derived category metrics, stand-hour mapping, unknown quantity fallback, invalid duration rejection, and unsupported correlation skipping.

## Candidate Follow-Ups

- Dashboard API response stability tests for fields consumed by `static/app.js` and `static/charts.js`.
- Import ZIP wrapper tests around missing `export.xml`, progress status, and parse error propagation.
- Storage contract tests for freshness/readiness behavior when a bug or product change proves the need.
- One informational `go test -cover` baseline after the focused lanes are less sparse, without enforcing a threshold.
