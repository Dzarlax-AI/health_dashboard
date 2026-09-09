#!/bin/sh

set -eu

backend=false
frontend=false
uncertain=false
seen=false

while IFS= read -r path || [ -n "$path" ]; do
	[ -n "$path" ] || continue
	seen=true

	case "$path" in
		apps/web/* | package.json | pnpm-lock.yaml | pnpm-workspace.yaml)
			frontend=true
			;;
		cmd/* | internal/* | go.mod | go.sum | init.sql | Dockerfile.backend)
			backend=true
			;;
		*)
			case "$path" in
				*/*)
					uncertain=true
					;;
				*.go)
					backend=true
					;;
				*)
					uncertain=true
					;;
			esac
			;;
	esac
done

if [ "$seen" = false ] ||
	[ "$uncertain" = true ] ||
	{ [ "$backend" = true ] && [ "$frontend" = true ]; }; then
	backend=true
	frontend=true
fi

if [ "$backend" = false ] && [ "$frontend" = false ]; then
	backend=true
	frontend=true
fi

printf 'backend=%s\n' "$backend"
printf 'frontend=%s\n' "$frontend"
