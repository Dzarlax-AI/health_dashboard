.PHONY: dev build backfill backfill-force energy-backfill energy-backfill-dry tenant-isolation import docker-up docker-down test test-unit test-db-storage test-db-ui test-db-ui-fast test-db-energy test-db-energy-smoke test-db-readiness test-db-import test-db-security test-db smoke-health-post

ADDR ?= :8080

dev:
	DATABASE_URL=$(DATABASE_URL) ADDR=$(ADDR) go run ./cmd/server

build:
	CGO_ENABLED=0 go build -o bin/server ./cmd/server

backfill:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/backfill

backfill-force:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/backfill --force

# One-shot retrospective backfill for EnergyBank v2 snapshots. Optional
# vars: TZ (defaults to REPORT_TZ or UTC), FROM, TO, SCHEMA. See
# cmd/energy_backfill/main.go for the full flag set.
energy-backfill:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/energy_backfill \
		$(if $(TZ),--tz $(TZ),) \
		$(if $(FROM),--from $(FROM),) \
		$(if $(TO),--to $(TO),) \
		$(if $(SCHEMA),--schema $(SCHEMA),)

energy-backfill-dry:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/energy_backfill --dry-run \
		$(if $(TZ),--tz $(TZ),) \
		$(if $(FROM),--from $(FROM),) \
		$(if $(TO),--to $(TO),) \
		$(if $(SCHEMA),--schema $(SCHEMA),)

import:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/import --file $(FILE) --batch 500 --pause 150ms

tenant-isolation:
	go run ./cmd/tenant_isolation $(ARGS)

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

test-unit:
	HEALTH_DB_TESTS= CGO_ENABLED=0 go test ./...

test-db-storage:
	HEALTH_DB_TESTS=1 go test ./internal/storage -count=1 -timeout=120s

test-db-ui:
	HEALTH_DB_TESTS=1 go test ./internal/ui -count=1 -timeout=120s

test-db-ui-fast:
	HEALTH_DB_TESTS=1 go test ./internal/ui -run "^(TestAdminCheckinCoverage_JSONShape|TestOnboardingWizard_RejectsSchemaAll|TestFragmentAdminReadinessMonitoring_Renders)$$" -count=1 -timeout=60s

test-db-energy:
	HEALTH_DB_TESTS=1 go test ./internal/storage -run "TestComputeUserVerdictBands" -count=1 -timeout=90s

test-db-energy-smoke:
	HEALTH_DB_TESTS=1 go test ./internal/storage -run "^TestComputeUserVerdictBands_UsesCompatibleFormulaWarmup$$" -count=1 -timeout=30s

test-db-readiness:
	HEALTH_DB_TESTS=1 go test ./internal/storage -run '^(TestSaveNaiveBaseline_NilValueWithValidReasonPersists|TestVerifyReadinessRedesignSchema_DetectsReasonColumnDrift|TestRecomputeChipCalibrations_Integration_(RejectsCrossEpochLabels|PercentileP80|InsufficientData))$$' -count=1 -timeout=90s

test-db-security:
	HEALTH_DB_TESTS=1 go test ./internal/registry -count=1 -timeout=90s
	HEALTH_DB_TESTS=1 go test ./internal/tenants -count=1 -timeout=120s
	HEALTH_DB_TESTS=1 go test ./internal/storage -run '^(TestVerifySchemaContractContextHonorsCancellation|TestEnsureSchemaContractContextHonorsCancellation|TestEnsureSchemaContractIgnoresImportHousekeepingFailure|TestVerifySchemaContractRejectsViewSpoofingRequiredTable|TestVerifySchemaContractRejectsWrongIndexDefinition|TestVerifySchemaContractRejectsWrongPartialIndexLiteralCase|TestVerifySchemaContractRejectsWrongRuntimeColumnShape|TestVerifySchemaContractRejectsWrongArrayElementType|TestVerifySchemaContractRejectsWrongCompositeConflictKey|TestVerifySchemaContractRejectsMalformedBootstrapTuple|TestEnsureTenantIdentityUpgradesLegacyMarker|TestRestoreTenantIdentityMarkerContractRetriesOlderVersion|TestEnsureTenantIdentityRejectsIdentityMismatch|TestEnsureTenantIdentityRejectsNonLegacyContractMismatch)$$' -count=1 -timeout=120s

test-db-import:
	HEALTH_DB_TESTS=1 go test ./internal/storage -run '^(TestAppleHealthXMLImport|TestBeginAppleHealthXMLImport|TestHealthRecordProcessing|TestNotificationDelivery|TestQualityReclassification)' -count=1 -timeout=120s

test-db: test-db-ui-fast test-db-energy-smoke test-db-readiness

test:
	$(MAKE) smoke-health-post

smoke-health-post:
	curl -s -X POST http://localhost$(ADDR)/health \
		-H "Content-Type: application/json" \
		-H "automation-name: Test" \
		-H "automation-id: test-001" \
		-H "session-id: sess-001" \
		-d '{"data":{"metrics":[{"name":"step_count","units":"count","data":[{"date":"2026-03-04 00:00:00 +0000","qty":8234,"source":"iPhone"}]}]}}' \
		| python3 -m json.tool
