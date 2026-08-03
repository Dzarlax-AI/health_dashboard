#!/bin/sh

set -eu

fail() {
	echo "release image resolver: $*" >&2
	exit 1
}

component_changed=${COMPONENT_CHANGED:?COMPONENT_CHANGED is required}
published_digest=${PUBLISHED_DIGEST-}
image=$(printf '%s' "${IMAGE:?IMAGE is required}" | tr '[:upper:]' '[:lower:]')
pair_image=$(printf '%s' "${PAIR_IMAGE:?PAIR_IMAGE is required}" | tr '[:upper:]' '[:lower:]')
component_key=${COMPONENT_KEY:?COMPONENT_KEY is required}
expected_role=${EXPECTED_ROLE:?EXPECTED_ROLE is required}
github_output=${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}
docker_bin=${DOCKER_BIN:-docker}

case "$component_key" in
	backend | frontend)
		;;
	*)
		fail "COMPONENT_KEY must be backend or frontend"
		;;
esac
test "$expected_role" = "$component_key" ||
	fail "EXPECTED_ROLE must match COMPONENT_KEY"

inspect_label() {
	label=$1
	target=$2
	"$docker_bin" image inspect --format "{{index .Config.Labels \"$label\"}}" "$target"
}

valid_digest() {
	printf '%s\n' "$1" | grep -Eq '^sha256:[0-9a-f]{64}$'
}

valid_revision() {
	printf '%s\n' "$1" | grep -Eq '^[0-9a-f]{40}$'
}

valid_contract() {
	case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
		"" | "<no value>" | n/a | none | null | unknown | unset)
			return 1
			;;
	esac
}

case "$component_changed" in
	true)
		digest=$published_digest
		valid_digest "$digest" || fail "published digest is missing or invalid"
		"$docker_bin" pull --platform linux/amd64 "$image@$digest"
		;;
	false)
		pair_ref="${pair_image}:compatible"
		"$docker_bin" pull "$pair_ref"
		pair_role=$(inspect_label io.health-dashboard.image-role "$pair_ref")
		pair_revision=$(inspect_label io.health-dashboard.pair-revision "$pair_ref")
		contract=$(inspect_label io.health-dashboard.api-contract-version "$pair_ref")
		selected_image=$(inspect_label "io.health-dashboard.${component_key}-image" "$pair_ref")
		digest=$(inspect_label "io.health-dashboard.${component_key}-digest" "$pair_ref")
		revision=$(inspect_label "io.health-dashboard.${component_key}-revision" "$pair_ref")

		test "$pair_role" = compatibility-pair ||
			fail "compatible pointer has the wrong image role"
		valid_revision "$pair_revision" ||
			fail "compatible pointer pair revision is missing or invalid"
		test "$selected_image" = "$image" ||
			fail "compatible pointer selected an unexpected component image"
		valid_digest "$digest" ||
			fail "compatible pointer component digest is missing or invalid"
		valid_revision "$revision" ||
			fail "compatible pointer component revision is missing or invalid"
		valid_contract "$contract" ||
			fail "compatible pointer contract is missing or invalid"
		"$docker_bin" pull --platform linux/amd64 "$image@$digest"
		;;
	*)
		fail "COMPONENT_CHANGED must be true or false"
		;;
esac

valid_digest "$digest" || fail "component digest is missing or invalid"
component_ref="$image@$digest"
actual_revision=$(inspect_label org.opencontainers.image.revision "$component_ref")
actual_contract=$(inspect_label io.health-dashboard.api-contract-version "$component_ref")
role=$(inspect_label io.health-dashboard.image-role "$component_ref")
valid_revision "$actual_revision" || fail "component revision is missing or invalid"
valid_contract "$actual_contract" || fail "component contract is missing or invalid"
test "$role" = "$expected_role" || fail "component image role does not match $expected_role"

if test "$component_changed" = true; then
	revision=$actual_revision
	contract=$actual_contract
else
	test "$actual_revision" = "$revision" ||
		fail "component revision does not match compatible pointer"
	test "$actual_contract" = "$contract" ||
		fail "component contract does not match compatible pointer"
fi

{
	echo "image=$image"
	echo "digest=$digest"
	echo "revision=$revision"
	echo "contract=$contract"
	echo "role=$role"
} >>"$github_output"
