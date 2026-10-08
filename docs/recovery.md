# Backup and recovery

Recovery has two independent responsibilities: reconstruct application data from PostgreSQL/WAL, and retain knowledge of mail already handed to Postfix. An old SQL snapshot alone can turn a delivered message back into queued work. selfmail's independent journal and recovery hold prevent a blind resend when their evidence is preserved and the documented procedure is followed.

## Storage inventory

| Storage | Why it matters |
|---|---|
| `postgres_data` | Jobs, payloads, tenant policy, domain keys, events, idempotency |
| `backup_repo` or remote pgBackRest repository | Encrypted physical backups and WAL |
| `runtime_journal` | Immutable encrypted acceptance/handoff/outcome/DSN/log/retirement evidence |
| `runtime_control` | Deployment identity, generation, hold and reconciliation proof, independent sealed journal head |
| `postfix_spool` | Mail already owned by Postfix, including deferred delivery |
| `postfix_logs` | Current/archived evidence and ingestion markers |
| Private `.env` / secrets | MASTER_KEY, service credentials, repository passphrases, TLS pair |
| `evidence_staging` / credentials | Private plaintext staging/config for encrypted Restic backups |

Journal records contain identifiers, addresses, timestamps, and diagnostics, **not bodies or attachments**. If SQL restores before acceptance and its body is lost, reconciliation can preserve the original accepted IDs as blocked unknown work; it cannot reconstruct the payload from evidence. Never treat the journal as a replacement for frequent database/WAL backups.

## Database/WAL backups

```sh
docker compose -p selfmail-prod --env-file .env.production \
  -f compose.yaml -f compose.production.yaml -f compose.backup.yaml \
  up -d --build
```

pgBackRest encrypts the repository with a separate passphrase. PostgreSQL archives WAL synchronously with an `archive_timeout` of 60 seconds. The backup loop checks the stanza, creates weekly full / daily differential / hourly incremental backups, and advances an atomic success timestamp only after success. Seven full and seven differential backups are retained according to pgBackRest's dependency rules. [pgBackRest reference](https://pgbackrest.org/user-guide.html).

```sh
docker compose exec -u postgres postgres selfmail-pgbackrest check
docker compose exec -u postgres postgres selfmail-pgbackrest info
docker compose exec -u postgres postgres selfmail-pgbackrest --type=full backup
```

The default repository is on the same Docker host. For independence, provision a separate pgBackRest SSH repository server, known_hosts, an unprivileged repository account, and matching pgBackRest version; adapt `compose.backup.remote.yaml.example`. That remote infrastructure is not provisioned or validated by this repository.

Local WAL configuration is not a measured one-minute RPO. Confirm archive health, independent storage freshness, off-host loss/recovery, and actual backup retention on the deployed server.

## Independent evidence and key backup

Set the actual environment file **explicitly**; `--env-file` alone does not select the export source:

```sh
export SELFMAIL_ENV_FILE=.env.production
docker compose -p selfmail-prod --env-file "$SELFMAIL_ENV_FILE" \
  -f compose.yaml -f compose.production.yaml -f compose.backup.yaml -f compose.evidence.yaml \
  run --rm evidence-repository init
```

`init` is a one-time command for a new repository. For subsequent snapshots:

```sh
docker compose -p selfmail-prod --env-file "$SELFMAIL_ENV_FILE" \
  -f compose.yaml -f compose.production.yaml -f compose.backup.yaml -f compose.evidence.yaml \
  run --rm evidence-export
docker compose -p selfmail-prod --env-file "$SELFMAIL_ENV_FILE" \
  -f compose.yaml -f compose.production.yaml -f compose.backup.yaml -f compose.evidence.yaml \
  run --rm evidence-repository backup /evidence --tag selfmail-evidence
docker compose -p selfmail-prod --env-file "$SELFMAIL_ENV_FILE" \
  -f compose.yaml -f compose.production.yaml -f compose.backup.yaml -f compose.evidence.yaml \
  run --rm evidence-repository check --read-data
```

The exporter captures a consistent authenticated manifest prefix and sealed head without holding the live writer lock throughout copying. It includes the original `instance.json`, private deployment env, and current PostgreSQL backup passphrase. The active MASTER_KEY/APP_ENV must match the file, preventing a successful export with the wrong deployment key.

Restic encrypts this export with **a different repository password**. The default repository remains local; for off-host storage configure a controlled SFTP repository in `EVIDENCE_REPOSITORY`, SSH keys, and pinned known_hosts. Keep the Restic password offline/independently recoverable; it must not depend on the server it protects. The SMTP certificate/key are not in this export: back them up privately or document reissuance before reopening submission.

Evidence export is a manual workflow in this version. Schedule it on the chosen host at a tested interval, alert on failure/freshness, prevent overlapping runs, and remove obsolete staging exports only after a verified encrypted backup. Staging copies include secrets and the entire selected journal prefix; disk use can grow quickly without maintenance. Restic repository pruning/retention is an operator policy separate from PostgreSQL retention. Do not prune the only snapshot containing keys/evidence needed by retained SQL/WAL backups.

## Managed SQL restore on the same host

Use the same project/environment/overlay arguments on all commands. Prevent application traffic from being sent directly to an old database while workers are active.

1. Run `selfmail recovery hold --reason 'Planned PostgreSQL point-in-time restore'`. Verify held state; the hold drains existing delivery permits and rejects new acceptance/claims. Its state is outside SQL.
2. Stop the API, dispatcher, worker, maintainer, webhooks, bounce, and reconciler before replacing SQL. Preserve **current** control/journal, spool, and logs. Quiesce/snapshot spool where needed.
3. Restore PostgreSQL to a **new volume** and validate the target/backup/WAL before switching connections. Never let a restoration command overwrite live evidence volumes.
4. Start SQL and compatible CLI/reconciler in held mode. Check schema and MASTER_KEY. Run `selfmail recovery reconcile`.
5. Review imported/unknown/missing-payload counts and the returned digest. Investigate uncertain messages using spool/archives; missing evidence does not authorize resending.
6. Run `selfmail recovery release --report SHA256_FROM_RECONCILE`, or add `--keep-unknown` to retain unresolved work blocked. If the journal changed, repeat reconciliation.
7. Restart delivery processes, check readiness/progress/oldest queues, and verify a known business sample.

The image's entrypoint is `selfmail`. Admin examples while the ordinary worker is stopped:

```sh
docker compose run --rm --no-deps maintainer recovery status
docker compose run --rm --no-deps maintainer recovery reconcile
docker compose run --rm --no-deps maintainer recovery release --report REPORT_HASH --keep-unknown
```

`exec worker selfmail ...` includes the executable; `run maintainer ...` replaces the command and keeps the entrypoint. The maintainer's private network also avoids a one-off worker colliding with the running worker's fixed MTA address. Preserve this distinction in automation.

## Physical restore drill

The supplied script restores only into a newly named owned volume:

```sh
export COMPOSE_PROJECT_NAME=selfmail-prod
export SELFMAIL_ENV_FILE=.env.production
sh scripts/restore-drill.sh '2026-10-08 12:00:00+00'
```

It prints the new volume identity. Start it under a separate container/network alias with the backup repository and `backup_credentials` mounted, the same PostgreSQL major version, and settings at least as large as the backed-up server (`max_connections=120` in the supplied profile). Keep archive_mode off in the drill and use the wrapper restore_command so the encrypted WAL passphrase remains readable. Do not attach application traffic to the drill.

`cmd/recoverydrill` provides development-only prepare/deliver/verify stages for a queued snapshot that later reached the local sink; verification requires the isolated `restorelab:5432` alias. The script does not automatically deploy that laboratory or make a restored database live. Record a separate recovery-time measurement and application consistency checks on your server.

## Replacement-host recovery

After host/control-volume loss, restore the encrypted evidence and keys into **new isolated storage** before starting normal services.

1. Restore a consistent Restic export and its deployment env. Recover the independently retained Restic password first. Restore PostgreSQL/WAL into a new volume; restore or inspect the newest surviving Postfix spool/logs.
2. Copy the export's `journal/` into the new `runtime_journal`, and `anchor/journal-head.enc` into the new `runtime_control`. Grant runtime UID 10001 private access. Do not copy a previous unheld `state.enc` and resume blindly.
3. Mount the export's `instance.json` into a one-off runtime CLI. Start only SQL/storage initialization, without the general `initialize`/delivery service chain.
4. Run `recovery import-control --instance-file /restore/instance.json --reason 'Replacement host recovery from verified export'` through `docker compose run --rm --no-deps -v /PRIVATE_EXPORT:/restore:ro maintainer ...`.
5. Import authenticates the original instance against the restored encrypted head, validates the complete journal and SQL key guard, then creates a **new held generation**, with no old release proof. A mismatched key/export, corrupt/missing evidence, or different existing deployment fails.
6. Reconcile, review unknown/missing-payload work, and release using the newly generated report, as in the same-host procedure. Reissue/restore SMTP TLS, verify DNS/ports, and validate mail service before reopening traffic.

An export may omit deliveries after its sealed boundary. If the newest evidence/spool is lost, identify the uncovered time window and keep affected operations uncertain; exactly-once cannot be reconstructed from stale backups. Application outbox IDs/order records help review but are not independent proof of a remote SMTP rejection.

## Interrupted/corrupt evidence

The journal fails closed if a pending write, missing record, altered manifest, lost tail/head, or wrong key is detected. Under hold, `recovery repair-journal` can complete the supported authenticated interrupted-write state; it is not a general command to discard arbitrary corruption. Restore a consistent independently verified snapshot when evidence cannot be repaired.

Capacity is checked against `JOURNAL_MAX_BYTES` and filesystem reserve. There is no automatic evidence compaction or deletion. Increase storage/capacity through a reviewed held operation or implement a verified future compaction strategy covering all retained backups. Deleting `.enc` files or the sealed head to make space destroys the safety proof.

## Readiness boundary

Local checks exercise physical PITR into a separate volume, one-copy reconciliation, encrypted evidence backup/check/restore, and replacement-identity import on temporary storage. They do not prove remote SSH/SFTP behavior, whole-host-loss RPO/RTO, replica failover, Internet delivery, or multi-host filesystem safety. Measure those properties on your deployment; one host remains one failure domain.
