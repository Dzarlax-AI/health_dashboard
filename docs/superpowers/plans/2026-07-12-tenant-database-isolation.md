# Tenant Database Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce cross-tenant and registry isolation with distinct PostgreSQL login roles while preserving existing Health Dashboard behavior and a reversible rollout.

**Architecture:** A provisioning connection creates roles/schemas and reconciles durable setup state; a registry-only connection serves identity/session operations; each active tenant pool authenticates with a dedicated login derived from an immutable tenant ID and deployment master secret. Existing tenants migrate additively behind an explicit isolation mode, canary first, and the legacy shared login retains rollback access until final revocation.

**Tech Stack:** Go 1.26, pgx/v5 + pgxpool, PostgreSQL roles/grants/catalogs, HMAC-SHA-256, GitHub Actions PostgreSQL service, Markdown/HTML operational docs.

**GitHub safety:** This plan implements only issue #198. Do not create or close issues, change labels, create a PR, push, merge, or commit unless the user explicitly authorizes that exact action. Issues #199–#204 remain unchanged.

**Execution update (2026-07-12):** The user subsequently authorized completing the full audit plan in one cumulative application-repository PR. Tasks 1–6 and 8 are implemented in the working tree; local unit/race/vet/YAML checks pass. Task 7 belongs to the separate `personal_ai_stack` repository and remains a distinct deployment change. Production migration/canary and issue closure remain gated on explicit review and CI evidence.

---

## File and Responsibility Map

| File | Responsibility |
|---|---|
| `internal/registry/registry.go` | Active-user lookups and immutable tenant isolation metadata. |
| `internal/registry/provisioning.go` | Durable provisioning state transitions and reconciliation records. |
| `internal/registry/provisioning_test.go` | State-machine unit tests. |
| `internal/registry/provisioning_integration_test.go` | Transaction/ambiguous-state tests on disposable PostgreSQL. |
| `internal/tenants/credentials.go` | Role naming and versioned HMAC password derivation. |
| `internal/tenants/credentials_test.go` | Determinism, separation, versioning, and validation tests. |
| `go.mod`, `go.sum` | Promote the existing `github.com/google/uuid` dependency to direct use for immutable tenant and operation IDs. |
| `internal/tenants/provisioner.go` | Admin-only role/schema creation, marker ownership, grants, migration, reconciliation. |
| `internal/tenants/provisioner_integration_test.go` | Two-role/two-schema denial and provisioning recovery tests. |
| `internal/tenants/manager.go` | Restricted tenant pool construction and identity assertions. |
| `internal/storage/db.go` | Pool construction from already-restricted tenant DSNs; no admin authority. |
| `cmd/server/main.go` | Parse isolation config before side effects and wire three connection classes. |
| `cmd/tenant_isolation/main.go` | Dry-run/apply/verify migration CLI for existing tenant schemas. |
| `cmd/tenant_isolation/main_test.go` | CLI argument and mode contract. |
| `.github/workflows/ci.yml` | Disposable PostgreSQL tenant-isolation security lane. |
| `.env.example`, `README.md` | Configuration, rollout, rollback, and MCP transition. |
| `AGENTS.md`, `CLAUDE.md` | Durable architecture contract, kept in lockstep. |
| `personal_ai_stack/deploy/infra/init.sql` | Provisioning/registry bootstrap roles for fresh infrastructure. Changed in its own repository and reviewed separately. |
| `personal_ai_stack/deploy/health/docker-compose.yml` and `.env` template | Supply separate DSNs/master secret; never commit real secrets. Changed in its own repository and reviewed separately. |

## Task 1: Isolation Configuration and Credential Derivation

**Files:**
- Create: `internal/tenants/credentials.go`
- Create: `internal/tenants/credentials_test.go`
- Modify: `cmd/server/main.go`
- Modify: `.env.example`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Write failing derivation tests**

```go
func TestTenantCredentialDerivationIsStableAndSeparated(t *testing.T) {
    d := CredentialDeriver{Current: SecretVersion{Version: 3, Secret: []byte("test-master-secret-with-32-bytes-min")}}
    idA := uuid.MustParse("11111111-1111-4111-8111-111111111111")
    idB := uuid.MustParse("22222222-2222-4222-8222-222222222222")
    a1, err := d.Derive(idA, "health_t_111111111111", 3)
    if err != nil { t.Fatal(err) }
    a2, _ := d.Derive(idA, "health_t_111111111111", 3)
    b, _ := d.Derive(idB, "health_t_222222222222", 3)
    if a1 != a2 || a1 == b { t.Fatalf("unstable or shared credential") }
    if strings.ContainsAny(a1, ":/@ ") { t.Fatalf("DSN-unsafe credential %q", a1) }
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./internal/tenants -run TestTenantCredentialDerivation -count=1`
Expected: FAIL because `CredentialDeriver` does not exist.

- [ ] **Step 3: Implement immutable role naming and HMAC derivation**

```go
type SecretVersion struct { Version int; Secret []byte }
type CredentialDeriver struct { Current SecretVersion; Previous *SecretVersion }

func TenantRoleName(id uuid.UUID) string {
    return "health_t_" + strings.ReplaceAll(id.String(), "-", "")[:20]
}

func (d CredentialDeriver) Derive(id uuid.UUID, role string, version int) (string, error) {
    secret, err := d.secretFor(version)
    if err != nil { return "", err }
    mac := hmac.New(sha256.New, secret)
    _, _ = mac.Write([]byte("health-tenant-db-v1\x00" + id.String() + "\x00" + role))
    return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
```

Validation requirements: master secrets decode to at least 32 bytes; versions are positive; current and previous versions differ; role name passes a strict lowercase identifier regex.

Run `go mod tidy` after importing `github.com/google/uuid`; verify that it becomes a direct dependency and no unrelated module changes appear.

- [ ] **Step 4: Add a pure startup config parser**

Define `TenantIsolationConfig` with `Enabled`, admin DSN, registry DSN, tenant DSN base, current/previous secret versions. Parse and validate before opening any DB connection. When isolation mode is enabled, missing values return an error; do not log values.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./internal/tenants ./cmd/server -count=1`
Expected: PASS.

- [ ] **Step 6: Review checkpoint**

Run `git diff --check` and show the scoped diff. Do not commit without explicit user permission.

## Task 2: Registry Metadata and Active-Only Lookups

**Files:**
- Modify: `internal/registry/registry.go`
- Create: `internal/registry/provisioning.go`
- Create: `internal/registry/provisioning_test.go`
- Modify: registry query tests

- [ ] **Step 1: Write failing metadata and state tests**

```go
func TestInactiveTenantIsRejectedByAuthLookups(t *testing.T) {
    // Insert a synthetic pending user through the test fixture.
    // GetByUsername, GetByAPIKey and GetByEmail must all return ErrUserNotFound.
}

func TestProvisioningTransitionMatrix(t *testing.T) {
    allowed := map[ProvisioningState][]ProvisioningState{
        StatePending: {StateProvisioning, StateFailed},
        StateProvisioning: {StateActive, StateFailed},
        StateFailed: {StateProvisioning},
    }
    // Assert every allowed transition and reject every other pair.
}
```

- [ ] **Step 2: Verify RED**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./internal/registry -run 'TestInactiveTenant|TestProvisioningTransition' -count=1`
Expected: FAIL because isolation metadata/state are absent.

- [ ] **Step 3: Add idempotent registry columns and operation table**

Add columns to `health_registry.users`:

```sql
tenant_id UUID UNIQUE,
db_role TEXT UNIQUE,
db_credential_version INTEGER,
provisioning_state TEXT NOT NULL DEFAULT 'active'
```

Add `health_registry.tenant_provisioning_operations` keyed by operation UUID with tenant ID, schema, role, state, error, created/updated timestamps. Existing users remain active during additive migration; isolation mode must refuse an active user whose new metadata is still null.

- [ ] **Step 4: Filter every authentication/routing lookup to active users**

Update `GetByUsername`, `GetByAPIKey`, `GetByEmail`, `GetBySchema`, `ListUsers` routing consumers, and session-user resolution so pending/failed identities cannot authenticate or receive pools.

- [ ] **Step 5: Implement transition compare-and-set**

```sql
UPDATE health_registry.tenant_provisioning_operations
SET state=$2, error=$3, updated_at=NOW()
WHERE operation_id=$1 AND state=$4
RETURNING operation_id
```

Return `ErrProvisioningStateConflict` when no row is returned.

- [ ] **Step 6: Run registry tests**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./internal/registry -count=1`
Expected: PASS.

- [ ] **Step 7: Review checkpoint**

Inspect all registry queries with `rg -n 'FROM health_registry.users' internal/registry` and verify active-state handling. Do not commit without explicit permission.

## Task 3: Admin Provisioner and Catalog-Safe Ownership

**Files:**
- Create: `internal/tenants/provisioner.go`
- Create: `internal/tenants/provisioner_integration_test.go`
- Modify: `internal/tenants/manager.go`
- Replace the work-in-progress first-user compensation code in `internal/registry/registry.go`

- [ ] **Step 1: Write failing disposable-DB provisioning tests**

Tests must create two synthetic tenant IDs/roles/schemas and assert:

```go
assertSQLState(t, tenantA, `SELECT count(*) FROM tenant_b.metric_points`, "42501")
assertSQLState(t, tenantA, `SELECT count(*) FROM health_registry.users`, "42501")
assertSQLState(t, tenantA, `CREATE TABLE tenant_b.injected(id int)`, "42501")
```

Also assert tenant A can create/alter/read its own objects and that registry runtime cannot read tenant A.

- [ ] **Step 2: Add a destructive-test database marker**

Before creating/dropping roles, require both `HEALTH_DB_TESTS=1` and a marker such as:

```sql
SELECT value='true' FROM health_test_metadata WHERE key='disposable_database'
```

Missing marker must `t.Fatal`; a populated personal registry must never be modified or merely skipped for these tests.

- [ ] **Step 3: Verify RED against a disposable PostgreSQL service**

Run: `HEALTH_DB_TESTS=1 READINESS_TEST_DSN="$READINESS_TEST_DSN" go test ./internal/tenants -run TestProvisioner -count=1 -v`
Expected: FAIL before provisioner implementation.

- [ ] **Step 4: Implement provision operations**

`Provisioner` owns a one-connection admin pool and exposes narrowly typed methods:

```go
type Provisioner interface {
    EnsureTenant(ctx context.Context, spec TenantSpec) error
    VerifyTenant(ctx context.Context, spec TenantSpec) error
    Reconcile(ctx context.Context, operationID uuid.UUID) error
    RotateCredential(ctx context.Context, spec TenantSpec, nextVersion int) error
}
```

Use `pgx.Identifier.Sanitize()` for all role/schema identifiers. Values remain parameters. Use plain `CREATE ROLE`/`CREATE SCHEMA`; inspect SQLSTATE `42710`/`42P06` and catalog ownership/marker before adopting existing objects.

- [ ] **Step 5: Add marker-scoped provisioning**

Create a schema-local marker containing tenant ID and operation ID before table initialization. Reconciliation may drop or reuse only when the marker, catalog owner, registry operation, and requested tenant all agree. Ambiguous outcomes transition to failed/reconcile-required state, never immediate destructive cleanup.

- [ ] **Step 6: Make table initialization run as tenant role**

After role/schema creation, open the restricted tenant pool and run every existing `Ensure*` twice. This proves idempotency and ensures the tenant role has only the privileges it actually needs.

- [ ] **Step 7: Replace compensating first-user cleanup with durable state**

Reserve the user/operation under advisory lock, provision outside the registry transaction, verify the restricted pool, then compare-and-set both operation and user to active. On crash/restart, startup reconciliation resumes pending/provisioning records. Do not infer commit failure from a transport error; reread durable state first.

- [ ] **Step 8: Run provisioning tests**

Run the disposable-DB command from Step 3.
Expected: PASS, including cross-tenant and registry SQLSTATE `42501` assertions.

- [ ] **Step 9: Review checkpoint**

Search for admin pool escape paths: `rg -n 'Admin|Provisioner|WithDB|ctxdb' internal cmd/server`. Confirm no admin/registry DB is passed to request context. Do not commit without explicit permission.

## Task 4: Restricted Tenant Pool Factory

**Files:**
- Modify: `internal/tenants/manager.go`
- Modify: `internal/storage/db.go`
- Create or modify: `internal/tenants/manager_integration_test.go`

- [ ] **Step 1: Write failing pool identity tests**

```go
func TestManagerRejectsPoolIdentityMismatch(t *testing.T) {
    // Registry metadata expects role/schema A; fixture DSN authenticates as another role.
    // GetOrCreate must fail and must not cache the pool.
}
```

- [ ] **Step 2: Verify RED**

Run: `HEALTH_DB_TESTS=1 READINESS_TEST_DSN="$READINESS_TEST_DSN" go test ./internal/tenants -run TestManagerRejectsPoolIdentityMismatch -count=1`
Expected: FAIL because Manager still uses the shared login.

- [ ] **Step 3: Introduce tenant DSN factory**

Clone base pgx config, set `User`, derived `Password`, and tenant `search_path`; remove shared credentials. Keep current pool budgets. The credential factory receives active registry metadata only.

- [ ] **Step 4: Assert database identity before caching**

```sql
SELECT current_user, current_schema()
```

Both values must match expected role/schema. On mismatch, close the pool and return an error.

- [ ] **Step 5: Remove schema-creation authority from Manager request pools**

Manager delegates new tenant provisioning to `Provisioner`; `GetOrCreate` only opens verified active tenant pools. Delete or make private any generic admin-schema methods that could be called from request paths.

- [ ] **Step 6: Run manager and storage tests**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./internal/tenants ./internal/storage -count=1`
Expected: PASS.

- [ ] **Step 7: Review checkpoint**

Verify pool counts remain registry pool plus `N × tenant MaxConns` plus short-lived admin max one. Do not commit without explicit permission.

## Task 5: Existing-Tenant Migration CLI

**Files:**
- Create: `cmd/tenant_isolation/main.go`
- Create: `cmd/tenant_isolation/main_test.go`
- Create: `internal/tenants/migration.go`
- Create: `internal/tenants/migration_integration_test.go`
- Modify: `Makefile`

- [ ] **Step 1: Write failing CLI contract tests**

Required modes:

```text
--mode inventory|dry-run|apply|verify|rotate
--schema <canonical-registry-schema>
--all
--credential-version <positive-int>
```

Reject `--schema` with `--all`, mutation modes without explicit confirmation flag, and any schema not present in the registry.

- [ ] **Step 2: Verify RED**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./cmd/tenant_isolation -count=1`
Expected: FAIL because the command does not exist.

- [ ] **Step 3: Implement inventory and dry-run first**

Inventory object owners, grants, default privileges, role memberships, schema ACLs, and `SECURITY DEFINER` functions. Unknown/unexpected objects are blockers. Dry-run prints exact intended changes without secrets or DSNs.

- [ ] **Step 4: Implement idempotent apply for one canonical schema**

Apply creates role/password, transfers every table/sequence/function owner as required, sets grants/default privileges, verifies restricted pool, and only then writes registry metadata/version. Never accept a free-form invented schema.

- [ ] **Step 5: Implement verify and rotation**

Verify performs normal own-schema probes plus cross-schema/registry denial. Rotation changes one role password, verifies a next-version pool, and updates the registry version. The running service then fails closed on metadata drift; a successful restart opens new-version pools. It does not hot-replace pools held by tenant workers.

- [ ] **Step 6: Add rollback manifest output**

Before apply, persist a secret-free JSON manifest of prior owners/grants and the immutable pre-change image reference supplied by the operator. Rollback consumes this manifest; it does not restore MCP `sql_query`.

- [ ] **Step 7: Run CLI and migration tests**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./cmd/tenant_isolation ./internal/tenants -count=1`
Expected: PASS.

- [ ] **Step 8: Review checkpoint**

Do not run `apply`, `rotate`, or rollback against production during implementation. Do not commit without explicit permission.

## Task 6: Server Wiring and Reconciliation Gate

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `internal/ui/handler.go`
- Modify: `internal/registry/registry.go`
- Modify: server/handler tests

- [ ] **Step 1: Write failing startup-order tests**

Test that invalid isolation config fails before any DB dial, tenant worker, notification scheduler, migration, or provisioning callback. Test that pending operations reconcile before users are listed or tenant workers start.

- [ ] **Step 2: Verify RED**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./cmd/server ./internal/ui -run 'TestIsolation|TestPendingProvisioning' -count=1`
Expected: FAIL before wiring changes.

- [ ] **Step 3: Wire separate connection classes**

Construct registry from registry DSN, provisioner from admin DSN, and Manager from tenant DSN base plus credential deriver. Eliminate use of the shared application DSN in multi-tenant request pools when isolation mode is enabled.

- [ ] **Step 4: Reconcile before side effects**

After registry schema availability but before migrations, tenant pools, backfills, AI, Telegram, or report schedulers, reconcile nonterminal provisioning operations. Unresolved state blocks readiness and setup mutation.

- [ ] **Step 5: Route setup/admin-user creation through durable provisioning**

Both first admin and later admin-created users use the same reservation/provision/verify/activate workflow. Existing legacy single-user mode remains explicitly separate until migrated last.

- [ ] **Step 6: Run server/UI tests**

Run: `GOCACHE=/tmp/health-go-build-cache go test ./cmd/server ./internal/ui ./internal/registry ./internal/tenants -count=1`
Expected: PASS.

- [ ] **Step 7: Review checkpoint**

Trace every `ctxdb.WithDB` caller and prove only restricted tenant `*storage.DB` values enter request context. Do not commit without explicit permission.

## Task 7: Infrastructure Bootstrap in `personal_ai_stack`

**Files in separate repository:**
- Modify: `deploy/infra/init.sql`
- Modify: `deploy/health/docker-compose.yml`
- Modify/create: safe `.env.example` for health deployment
- Modify: matching `AGENTS.md`/`CLAUDE.md` if architecture rules change

- [ ] **Step 1: Create a separate infrastructure diff**

Do not mix repositories in one commit or PR. Do not copy real `.env` values. Replace the single-role bootstrap with provisioning and registry roles using placeholders/injected secrets, and revoke unsafe PUBLIC defaults only after compatibility checks.

- [ ] **Step 2: Validate SQL on disposable PostgreSQL**

Run init twice to prove idempotency. Assert provisioning role capabilities and registry-role denials. Never run the init script against production as a test.

- [ ] **Step 3: Update compose variable wiring**

Pass `ADMIN_DATABASE_URL`, `REGISTRY_DATABASE_URL`, tenant DSN base, master secret, and version. Keep actual values only in deployment secret storage.

- [ ] **Step 4: Review checkpoint**

Show the infrastructure repository diff separately. Do not deploy, commit, push, or create a PR without explicit permission.

## Task 8: CI Security Lane and Full Verification

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `docs/TEST_COVERAGE.md`
- Modify: `README.md`
- Modify: `AGENTS.md`
- Modify: `CLAUDE.md`
- Update: `docs/ai-plans/2026-07-12-full-service-audit-remediation.html`

- [ ] **Step 1: Add disposable isolation database setup**

CI creates the disposable marker, provisioning/registry roles, and two tenant fixtures. The job runs only synthetic data and never imports secrets.

- [ ] **Step 2: Add required security commands**

```bash
HEALTH_DB_TESTS=1 READINESS_TEST_DSN="$TEST_DSN" go test ./internal/registry ./internal/tenants -run 'Test.*(Isolation|Provision|FirstUser|Migration|Rotation)' -count=1 -v
go test ./... -count=1
go test -race ./internal/mcpserver ./internal/ui ./internal/registry ./internal/tenants -count=1
go vet ./...
```

Expected: all exit zero; no DB integration test is skipped in the isolation lane.

- [ ] **Step 3: Add catalog assertions**

CI queries `pg_roles`, `information_schema.role_table_grants`, schema ACLs, role memberships, object owners, and default privileges. Unexpected cross-tenant or PUBLIC privileges fail the job.

- [ ] **Step 4: Document canary rollout and rollback**

README/runbook must state: migrate a non-primary synthetic/review tenant first; notifications remain disabled; verify ingestion/UI/import/backfill/MCP; soak; migrate remaining tenants one at a time; primary last; revoke legacy access only after explicit approval.

- [ ] **Step 5: Update durable project docs in lockstep**

AGENTS and CLAUDE must describe separate connection classes, immutable identity, credential derivation, provisioning state, and the prohibition on using admin pools in request contexts.

- [ ] **Step 6: Final bypass review**

Search for shared DSN use, arbitrary SQL registration, admin pool context injection, schema-qualified dynamic SQL, unsafe role membership, unbounded result paths, and secret-bearing log messages.

- [ ] **Step 7: Final full verification**

Run all commands from Step 2 plus `git diff --check`. Record exact results and skipped checks in the HTML plan.

- [ ] **Step 8: GitHub checkpoint**

Update issue #198 only after explicit user permission. Do not close it until application and database denial tests pass in CI and the approved production canary succeeds. Do not create a PR, push, merge, or change any other issue/label without explicit permission.

## Execution Boundaries

- Production database operations remain read-only until a separately reviewed migration run is approved.
- Role/schema migration, secret rotation, deployment, notifications, commits, pushes, PRs, issue closure, and label changes each require explicit permission.
- The current application containment (`sql_query` removal) may be released separately, but issue #198 remains open until DB isolation is verified.
- The existing Phase 0 worktree must not be merged while its provisioning implementation conflicts with the durable state-machine design; Task 3 replaces that work-in-progress code.
