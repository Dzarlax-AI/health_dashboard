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
	shift

	if "$manifest" "$@" >/dev/null 2>&1; then
		fail "$name: manifest generation unexpectedly succeeded"
	fi

	tests_run=$((tests_run + 1))
}

valid_digest_backend=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
valid_digest_frontend=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

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
		--backend-digest "$valid_digest_backend" \
		--frontend-digest "$valid_digest_frontend" \
		--backend-revision 0123456789abcdef \
		--frontend-revision 0123456789abcdef \
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
    "revision": "0123456789abcdef",
    "schema_version": 1,
}
if document != expected:
    raise SystemExit(f"unexpected manifest: {document!r}")

deterministic = json.dumps(expected, indent=2, sort_keys=True)
if os.environ["VALID_OUTPUT"] != deterministic:
    raise SystemExit("manifest output is not deterministic sorted/indented JSON")
PY
tests_run=$((tests_run + 1))

assert_manifest_fails "invalid digest" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-digest sha256:ABCDEF \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision same \
	--frontend-revision same \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "revision mismatch" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision backend-revision \
	--frontend-revision frontend-revision \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "contract mismatch" \
	--backend-image ghcr.io/example/health-backend \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision same \
	--frontend-revision same \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.5.0 \
	--created-at 2026-08-03T10:15:30Z

assert_manifest_fails "empty image reference" \
	--backend-image '' \
	--frontend-image ghcr.io/example/health-frontend \
	--backend-digest "$valid_digest_backend" \
	--frontend-digest "$valid_digest_frontend" \
	--backend-revision same \
	--frontend-revision same \
	--backend-contract-version 2.4.0 \
	--frontend-contract-version 2.4.0 \
	--created-at 2026-08-03T10:15:30Z

echo "PASS: $tests_run release contract helper tests"
