#!/bin/sh

set -eu

if test "$1" = pull; then
	exit 0
fi

test "$1" = image
test "$2" = inspect
test "$3" = --format
format=$4
target=$5

case "$target" in
	*-pair:compatible)
		case "$format" in
			*io.health-dashboard.image-role*)
				value=${FAKE_PAIR_ROLE:-compatibility-pair}
				;;
			*io.health-dashboard.pair-revision*)
				value=${FAKE_PAIR_REVISION:-}
				;;
			*io.health-dashboard.api-contract-version*)
				value=${FAKE_PAIR_CONTRACT:-}
				;;
			*io.health-dashboard.backend-image* | *io.health-dashboard.frontend-image*)
				value=${FAKE_PAIR_COMPONENT_IMAGE:-}
				;;
			*io.health-dashboard.backend-digest* | *io.health-dashboard.frontend-digest*)
				value=${FAKE_PAIR_COMPONENT_DIGEST:-}
				;;
			*io.health-dashboard.backend-revision* | *io.health-dashboard.frontend-revision*)
				value=${FAKE_PAIR_COMPONENT_REVISION:-}
				;;
			*)
				exit 1
				;;
		esac
		;;
	*)
		case "$format" in
			*org.opencontainers.image.revision*)
				value=${FAKE_COMPONENT_REVISION:-}
				;;
			*io.health-dashboard.api-contract-version*)
				value=${FAKE_COMPONENT_CONTRACT:-}
				;;
			*io.health-dashboard.image-role*)
				value=${FAKE_COMPONENT_ROLE:-}
				;;
			*)
				exit 1
				;;
		esac
		;;
esac

printf '%s\n' "$value"
