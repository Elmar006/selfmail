#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
project=${COMPOSE_PROJECT_NAME:?set the isolated Compose project name}
case "$project" in *[!a-z0-9_-]*|'') echo 'invalid project name' >&2; exit 1 ;; esac
envfile=${SELFMAIL_ENV_FILE:-.env}
target=${1:?usage: restore-drill.sh 'UTC target time'}
volume="${project}_restore_drill_$(date +%s)"
image=${SELFMAIL_POSTGRES_IMAGE:-selfmail-postgres:local}
docker volume create --label "selfmail.restore-drill=$project" "$volume" >/dev/null
# This command restores into a NEW named volume. It cannot overwrite the active
# PostgreSQL volume or external recovery-control/journal state.
docker run --rm --user root --entrypoint sh \
 -v "$volume:/restore" -v "${project}_backup_repo:/var/lib/pgbackrest" \
 -v "${project}_backup_credentials:/run/secrets:ro" "$image" -c \
 'set -eu; mkdir -p /restore/18/docker; chown -R postgres:postgres /restore; exec gosu postgres selfmail-pgbackrest --pg1-path=/restore/18/docker --type=time --target="$1" --target-action=promote --archive-mode=off --recovery-option="restore_command=selfmail-pgbackrest archive-get %f %p" restore' sh "$target"
printf 'Restored volume: %s\nStart it on a separate network/name; verify recovery before any traffic.\n' "$volume"
