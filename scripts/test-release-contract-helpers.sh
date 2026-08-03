#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
classifier="$script_dir/classify-image-changes.sh"
manifest="$script_dir/create-compatibility-manifest.py"
tests_run=0

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

assert_classification() {
	name=$1
	input=$2
	expected_backend=$3
	expected_frontend=$4

	output=$(printf '%s' "$input" | "$classifier")
	actual_backend=$(printf '%s\n' "$output" | sed -n 's/^backend=//p')
	actual_frontend=$(printf '%s\n' "$output" | sed -n 's/^frontend=//p')

	[ "$actual_backend" = "$expected_backend" ] ||
		fail "$name: expected backend=$expected_backend, got backend=$actual_backend"
	[ "$actual_frontend" = "$expected_frontend" ] ||
		fail "$name: expected frontend=$expected_frontend, got frontend=$actual_frontend"

	tests_run=$((tests_run + 1))
}

assert_manifest_fails() {
	name=$1
	expected_error=$2
	shift 2

	if error_output=$("$manifest" "$@" 2>&1 >/dev/null); then
		fail "$name: manifest generation unexpectedly succeeded"
	fi
	case "$error_output" in
		*"$expected_error"*)
			;;
		*)
			fail "$name: expected error containing '$expected_error', got '$error_output'"
			;;
	esac

	tests_run=$((tests_run + 1))
}

valid_digest_backend=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
valid_digest_frontend=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
valid_revision=0123456789abcdef0123456789abcdef01234567

assert_classification "frontend app" 'apps/web/src/routes/+page.svelte
' false true
assert_classification "frontend root metadata" 'package.json
pnpm-lock.yaml
pnpm-workspace.yaml
' false true
assert_classification "backend source" 'cmd/server/main.go
internal/ui/handler.go
root.go
go.mod
go.sum
init.sql
Dockerfile.backend
' true false
assert_classification "contract path" 'contracts/openapi.json
' true true
assert_classification "shared CI path" '.github/workflows/ci.yml
' true true
assert_classification "unknown path" 'README.md
' true true
assert_classification "mixed paths" 'apps/web/src/app.html
internal/ui/handler.go
' true true
assert_classification "empty input" '' true true

valid_output=$(
	"$manifest" \
		--backend-image ghcr.io/example/health-backend \
		--frontend-image ghcr.io/example/health-frontend \
		--backend-role backend \
		--frontend-role frontend \
		--backend-digest "$valid_digest_backend" \
		--frontend-digest "$valid_digest_frontend" \
		--backend-revision "$valid_revision" \
		--frontend-revision "$valid_revision" \
		--backend-contract-version 2.4.0 \
		--frontend-contract-version 2.4.0 \
		--created-at 2026-08-03T10:15:30Z
)
VALID_OUTPUT=$valid_output python3 - <<'PY'
import json
import os

document = json.loads(os.environ["VALID_OUTPUT"])
expected = {
    "api_contract_version": "2.4.0",
    "backend": {
        "digest": "sha256:" + "a" * 64,
        "image": "ghcr.io/example/health-backend",
        "role": "backend",
    },
    "created_at": "2026-08-03T10:15:30Z",
    "frontend": {
        "digest": "sha256:" + "b" * 64,
        "image": "ghcr.io/example/health-frontend",
        "role": "frontend",
    },
    "revision": "0123456789abcdef0123456789abcdef01234567",
    "schema_version": 1,
}
if document != expected:
    raise SystemExit(f"unexpected manifest: {document!r}")

deterministic = json.dumps(expected, indent=2, sort_keys=True)
if os.environ["VALID_OUTPUT"] != deterministic:
    raise SystemExit("manifest output is not deterministic sorted/indented JSON")
PY
tests_run=$((tests_run + 1))

assert_manifest_fails "invalid digest" "backend digest must be sha256:" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role frontend \
	--backend-digest sha256:ABCDEF \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision "$valid_revision" \
	--frontend-revision "$valid_revision" \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "revision mismatch" "backend and frontend revisions must match" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
	--frontend-revision bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "contract mismatch" "backend and frontend contract versions must match" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision "$valid_revision" \
	--frontend-revision "$valid_revision" \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.5.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "empty image reference" "backend image must be non-empty" \
	--backend-image '' \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision "$valid_revision" \
	--frontend-revision "$valid_revision" \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "swapped roles" "backend role must be exactly 'backend'" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role frontend \
	--frontend-role backend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision "$valid_revision" \
	--frontend-revision "$valid_revision" \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "invalid frontend role" "frontend role must be exactly 'frontend'" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role backend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision "$valid_revision" \
	--frontend-revision "$valid_revision" \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "invalid revision" "backend revision must be 40 lowercase hex chars" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision not-a-git-object \
	--frontend-revision not-a-git-object \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "unknown contract" "backend contract version must not be a sentinel value" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision "$valid_revision" \
	--frontend-revision "$valid_revision" \
	--backend-contract-version unknown \
	--frontend-contract-version unknown \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "non-UTC created-at" "created-at must use canonical RFC3339 UTC format" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision "$valid_revision" \
	--frontend-revision "$valid_revision" \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T12:15:30+02:00

assert_manifest_fails "invalid calendar created-at" "created-at must use canonical RFC3339 UTC format" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-role backend \
	--frontend-role frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision "$valid_revision" \
	--frontend-revision "$valid_revision" \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-02-30T10:15:30Z

echo "PASS: $tests_run release contract helper tests"
