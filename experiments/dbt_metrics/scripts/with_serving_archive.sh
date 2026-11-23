#!/usr/bin/env bash
# Usage: with_serving_archive.sh /path/to/trusted-profile.dump
# Requires local PostgreSQL client tools, Docker, Go, and the locked dbt environment.
set -euo pipefail
umask 077
[[ $# == 1 && -s "$1" && -f "$1" ]] || { echo "Pass a trusted, local PostgreSQL custom-format archive." >&2; exit 2; }
archive=$1
script_dir=$(cd "$(dirname "$0")" && pwd)
# Preload once with docker pull postgres@sha256:f2f98726820d0342055351c6e9d36e705f5bfffb3dd53ff556325114b08afb6d
image=postgres@sha256:f2f98726820d0342055351c6e9d36e705f5bfffb3dd53ff556325114b08afb6d
python3 "$script_dir/run_serving_archive.py" --preflight-baseline
endpoint=${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}')}
[[ "$endpoint" == unix://* ]] || { echo "A local Unix-socket Docker engine is required." >&2; exit 1; }
# Pin every subsequent operation to the inspected socket, including cleanup.
unset DOCKER_CONTEXT
export DOCKER_HOST="$endpoint"
for cmd in psql pg_restore pg_isready python3 go; do command -v "$cmd" >/dev/null; done
pg_restore --list "$archive" >/dev/null
scratch=$(mktemp -d)
id=''
watchdog=''
cleanup() {
    local code=$?
    trap - EXIT
    if [[ -n "$watchdog" ]]; then
        kill "$watchdog" 2>/dev/null || true
        wait "$watchdog" 2>/dev/null || true
    fi
    if [[ -n "$id" ]] && ! docker stop -t 5 "$id" >/dev/null 2>&1; then
        # The reserve watchdog may already have stopped the --rm container.
        if ! docker rm -fv "$id" >/dev/null 2>&1; then
            local remaining
            if ! remaining=$(docker ps -aq --no-trunc --filter "id=$id") || [[ -n "$remaining" ]]; then
                echo "Unable to verify removal of the disposable PostgreSQL container $id." >&2
                code=1
            fi
        fi
    fi
    rm -rf -- "$scratch" || code=1
    exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
unset PGSERVICE PGSERVICEFILE PGHOSTADDR PGOPTIONS
export PGHOST=127.0.0.1 PGDATABASE=serving_archive PGUSER=local_serving_owner PGSSLMODE=disable
PGPASSWORD=$(python3 -c 'import secrets; print(secrets.token_urlsafe(36))')
export PGPASSWORD
printf 'POSTGRES_DB=%s\nPOSTGRES_USER=%s\nPOSTGRES_PASSWORD=%s\n' "$PGDATABASE" "$PGUSER" "$PGPASSWORD" >"$scratch/postgres.env"
id=$(docker run -d --rm --pull=never --memory=2g --cpus=3 -p 127.0.0.1::5432 \
    --env-file "$scratch/postgres.env" "$image" -c fsync=off -c jit=off -c max_wal_size=256MB \
    -c shared_buffers=384MB -c maintenance_work_mem=192MB)
binding=$(docker port "$id" 5432/tcp)
[[ "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]]
export PGPORT="${BASH_REMATCH[1]}"
export DATABASE_URL="postgresql://$PGUSER:$PGPASSWORD@$PGHOST:$PGPORT/$PGDATABASE?sslmode=disable"
# Keep reserves while restoring a full archive; compression ratios vary.
free_host() { df -Pk "$scratch" | awk 'NR==2{print $4}'; }
free_db() { docker exec "$id" df -Pk /var/lib/postgresql/data | awk 'NR==2{print $4}'; }
archive_kb=$(du -k "$archive" | cut -f1)
(( $(free_db) > archive_kb * 20 + 1572864 && $(free_host) > archive_kb * 20 + 8388608 )) || {
    echo "Insufficient local free space for this archive and reserves." >&2; exit 1;
}
(
    while sleep 4; do
        if (( $(free_host) < 8388608 || $(free_db) < 1572864 )); then
            echo "Archive rehearsal stopped at disk reserve." >&2
            docker stop -t 3 "$id" >/dev/null; exit
        fi
    done
) & watchdog=$!
for _ in {1..90}; do if pg_isready -q; then break; fi; sleep 1; done
# Restore under a transient owner so the isolated profile subsequently owns its objects.
psql -Xq -v ON_ERROR_STOP=1 <<'SQL'
create role archive_loader nologin;
grant create on database serving_archive to archive_loader;
SQL
# PostgreSQL 17+ emits a session parameter that PostgreSQL 16 does not implement.
if ! { pg_restore --role=archive_loader --no-owner --no-privileges --file=- "$archive" | sed '/^SET transaction_timeout = 0;$/d' |
    psql -Xq -v ON_ERROR_STOP=1; } >"$scratch/restore.log" 2>&1; then
    echo "Private archive restore failed." >&2; exit 1
fi
export SERVING_ARCHIVE_SCRATCH="$scratch"
export SERVING_ARCHIVE_CONTAINER="$id"
python3 "$script_dir/run_serving_archive.py"
