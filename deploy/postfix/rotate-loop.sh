#!/bin/sh
set -eu
log=/var/log/postfix/mail.log
maximum=${POSTFIX_LOG_MAX_BYTES:-10485760}
interval=${POSTFIX_LOG_INTERVAL_SECONDS:-3600}
retention_days=${POSTFIX_LOG_RETENTION_DAYS:-90}
case "$maximum:$interval:$retention_days" in *[!0-9:]*|:*) exit 1 ;; esac
[ "$retention_days" -ge 90 ] || { echo 'log retention must cover the 90-day recovery window' >&2; exit 1; }
mkdir -p /var/log/postfix/.identities /var/log/postfix/.ingested
chown 10001:10001 /var/log/postfix/.ingested
last=$(date +%s)
while sleep 10; do
 now=$(date +%s)
 if [ "$((now%30))" -lt 10 ]; then
  bytes=$(du -sb /var/spool/postfix/active /var/spool/postfix/deferred /var/spool/postfix/maildrop | awk '{sum+=$1} END {print sum}')
  count=$(find /var/spool/postfix/active /var/spool/postfix/deferred /var/spool/postfix/maildrop -type f | wc -l)
  free=$(df -B1 --output=avail /var/spool/postfix | tail -n 1 | tr -d ' ')
  printf 'observed_at=%s\nbytes=%s\nmessages=%s\ndisk_available=%s\n' "$now" "$bytes" "$count" "$free" > /var/log/postfix/.spool.snapshot.tmp
  chmod 644 /var/log/postfix/.spool.snapshot.tmp
  mv /var/log/postfix/.spool.snapshot.tmp /var/log/postfix/.spool.snapshot
 fi
 [ -s "$log" ] || continue
 size=$(stat -c %s "$log")
 if [ "$size" -ge "$maximum" ] || [ "$((now-last))" -ge "$interval" ]; then
  postfix logrotate
  for archive in "$log".*; do
   [ -f "$archive" ] || continue
   case "$archive" in *.gz) continue ;; esac
   meta="/var/log/postfix/.identities/$(basename "$archive").id"
   if [ ! -f "$meta" ]; then
    identity="$(stat -c '%d:%i:' "$archive")$(head -n 1 "$archive" | sha256sum | cut -d ' ' -f 1)"
    printf '%s\n' "$identity" > "$meta"
   fi
  done
  last=$now
 fi
 for archive in "$log".*; do
  [ -f "$archive" ] || continue
  case "$archive" in *.gz) continue ;; esac
  meta="/var/log/postfix/.identities/$(basename "$archive").id"
  [ -f "$meta" ] || continue
  identity=$(cat "$meta")
  marker="/var/log/postfix/.ingested/$(printf '%s' "$identity" | sha256sum | cut -d ' ' -f 1).cursor"
  [ -f "$marker" ] || continue
  [ "$(cat "$marker")" = "$(stat -c %s "$archive")" ] || continue
  [ "$((now-$(stat -c %Y "$archive")))" -ge 90 ] || continue
  gzip -- "$archive"
 done
 for archive in "$log".*.gz; do
  [ -f "$archive" ] || continue
  [ "$((now-$(stat -c %Y "$archive")))" -ge "$((retention_days*86400))" ] || continue
  meta="/var/log/postfix/.identities/$(basename "$archive" .gz).id"
  [ -f "$meta" ] || continue
  identity=$(cat "$meta")
  marker="/var/log/postfix/.ingested/$(printf '%s' "$identity" | sha256sum | cut -d ' ' -f 1).cursor"
  [ -f "$marker" ] || continue
  # Compression is permitted only after ingestion; unread archives have no
  # marker and remain protected regardless of their age.
  rm -f -- "$archive" "$meta" "$marker"
 done
done
