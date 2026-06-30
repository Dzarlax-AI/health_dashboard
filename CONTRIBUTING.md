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
make test-unit
go vet ./...
```

Dependencies are managed through normal Go module resolution. Review dependency changes in `go.mod` and `go.sum`; the repository does not commit a `vendor/` tree.

DB-backed integration tests are explicit. They skip unless `HEALTH_DB_TESTS=1` is set, even when `PGHOST` or other libpq variables are present in your shell. To run them, provide libpq environment variables or `READINESS_TEST_DSN`, then use:

```bash
HEALTH_DB_TESTS=1 make test-db-storage
HEALTH_DB_TESTS=1 make test-db-ui
HEALTH_DB_TESTS=1 make test-db-ui-fast
HEALTH_DB_TESTS=1 make test-db-energy
HEALTH_DB_TESTS=1 make test-db-energy-smoke
HEALTH_DB_TESTS=1 make test-db-readiness
HEALTH_DB_TESTS=1 make test-db
```

`test-db` is the routine DB contract aggregate: fast UI DB smoke, EnergyBank DB smoke, and targeted Readiness DB checks. It intentionally does not run the full UI package, full EnergyBank verdict-band suite, or full `./internal/storage` package. Use the full domain lanes when a change touches that area.

Lane commands:

```bash
make test-db           # routine DB contract: ui-fast + energy-smoke + readiness
make test-db-ui-fast   # representative UI/admin DB handler checks
make test-db-energy-smoke # one EnergyBank verdict-band DB contract check
make test-db-readiness # readiness schema/calibration DB checks
make test-db-ui        # full internal/ui DB package sweep
make test-db-energy    # full EnergyBank verdict-band DB group
make test-db-storage   # full internal/storage DB package sweep
```

When `HEALTH_DB_TESTS=1` is set, missing or unreachable Postgres is a test failure, not a skip. Tests create throwaway schemas with test prefixes and drop them during cleanup.
Readiness monitoring and broad readiness writer families are intentionally outside the routine `test-db-readiness` lane until their fixtures are narrowed or runtime is measured separately. Full UI, full Energy, and full Storage lanes are manual/domain sweeps, not the normal edit-loop.

See `docs/TEST_COVERAGE.md` for the current focused coverage roadmap and fixture rules.

### CI and race detector policy

The default required-looking CI job is `Build, Vet & Test`. Keep that job name stable unless repository branch protection is updated at the same time.

Default PR and push CI uses `CGO_ENABLED=0` for build, vet, and unit tests, matching the production binary, and also runs the stable DB lanes against an isolated Postgres service container: routine `make test-db`, full UI DB, and full Energy DB. Full Storage DB is still manual because the current full storage package exceeds the 120s lane timeout in chronic-load writer tests.

Manual DB runs are available through the `CI` workflow: set `run_db_tests=true` and choose `stable`, `routine`, `ui`, `energy`, `readiness`, or `storage`.

Race detector tests are also manual: start the `CI` workflow with `run_race=true`. Race-only and DB-only dispatches do not publish Docker images, even when the image build inputs are left at their defaults. Use the race detector before merging changes that touch tenant management, scheduler goroutines, async AI generation, recompute coordination, Telegram webhook dispatch, or notification loops.

For local checks on a machine with a CGO toolchain:

```bash
CGO_ENABLED=1 go test -race ./...
```

Branch protection is managed in GitHub repository settings, not in this tree. As of 2026-06-06, the GitHub API reports `main` as not protected. Enabling required checks is a separate admin decision; if enabled, require the `Build, Vet & Test` check first and only require `Race Detector` after deciding that every PR should pay the CGO/race runtime cost.

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
