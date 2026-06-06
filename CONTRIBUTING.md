# Contributing

Thanks for taking the time to improve Health Dashboard.

This project is a self-hosted health-data system. Changes should preserve the core guarantees: raw data stays rebuildable, missing data is surfaced honestly, and scoring changes are backed by tests or documented methodology.

## Good First Contributions

- Documentation fixes, setup notes, and clearer operator guidance.
- Tests for pure health formulas, storage edge cases, and admin API contracts.
- Bug reports with exact metric names, date ranges, logs, and reproduction steps.
- Small UI improvements that make calibration, freshness, or data quality easier to inspect.

## Development

Use Go 1.24+ and PostgreSQL.

```bash
go mod tidy
go test ./...
go vet ./...
```

Dependencies are managed through normal Go module resolution. Review dependency changes in `go.mod` and `go.sum`; the repository does not commit a `vendor/` tree.

DB-backed integration tests skip when no Postgres connection is configured. To run them, provide libpq environment variables or `READINESS_TEST_DSN`. Tests create throwaway schemas and drop them during cleanup.

## Pull Request Guidelines

- Keep behavioral changes focused and explain the data contract they affect.
- Add or update tests for formula, storage, admin API, and UI contract changes.
- Avoid changing generated/build output directly.
- For scoring or calibration changes, update the relevant methodology document, usually `SCORING.md`, `ENERGY_BANK.md`, or `STRESS_MEASUREMENT.md`.
- Do not include real personal health data in issues, PRs, fixtures, screenshots, or logs.

## Reporting Bugs

Please include:

- Server version or commit SHA.
- Deployment mode: Docker, binary, local dev, or other.
- Whether the install is single-user or multi-user.
- Relevant endpoint, metric name, date range, and source device if applicable.
- Sanitized logs or SQL snippets when possible.

## Philosophy

Health Dashboard should prefer a clear "not enough data" state over a fabricated score. If a change makes the system more confident, it should also make the evidence for that confidence easier to inspect.
