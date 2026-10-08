#!/bin/sh
set -eu
# One full backup every seven days, incremental backups hourly. WAL is archived
# synchronously by PostgreSQL. Failed backup never advances the last-success file.
mkdir -p /var/lib/pgbackrest/status
selfmail-pgbackrest stanza-create
while :; do
  now=$(date +%s)
  last_full=$(cat /var/lib/pgbackrest/status/full 2>/dev/null || echo 0)
  last_diff=$(cat /var/lib/pgbackrest/status/diff 2>/dev/null || echo 0)
  type=incr
  if [ "$((now-last_full))" -ge 604800 ]; then type=full; fi
  if [ "$type" = incr ] && [ "$((now-last_diff))" -ge 86400 ]; then type=diff; fi
  if selfmail-pgbackrest check && selfmail-pgbackrest --type="$type" backup; then
    date +%s > /var/lib/pgbackrest/status/success.tmp
    mv /var/lib/pgbackrest/status/success.tmp /var/lib/pgbackrest/status/success
    if [ "$type" = full ]; then cp /var/lib/pgbackrest/status/success /var/lib/pgbackrest/status/full; fi
    if [ "$type" = diff ] || [ "$type" = full ]; then cp /var/lib/pgbackrest/status/success /var/lib/pgbackrest/status/diff; fi
  else
    echo 'selfmail backup failed; investigate WAL archive and repository' >&2
  fi
  sleep "${BACKUP_INTERVAL_SECONDS:-3600}"
done
