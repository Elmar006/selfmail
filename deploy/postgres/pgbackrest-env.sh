#!/bin/sh
set -eu
passfile=${PGBACKREST_PASSPHRASE_FILE:-/run/secrets/backup_passphrase}
test -s "$passfile" || { echo 'encrypted backup passphrase file required' >&2; exit 1; }
PGBACKREST_REPO1_CIPHER_PASS=$(cat "$passfile")
export PGBACKREST_REPO1_CIPHER_PASS
exec pgbackrest --stanza=selfmail "$@"
