# Deployment and operations

Use Linux for production and Docker Desktop/WSL for Windows development. Compose requires version 2.24.4+ for `!reset`/`!override`. Set a distinct project name for independent installations; volumes and network addresses must remain distinct too.

## Local installation

```sh
sh scripts/init-local.sh
docker compose up -d --build --wait
docker compose exec worker selfmail tenant create --name coffee --domain coffee.example.test
docker compose --profile observability up -d prometheus alertmanager
```

The initializer creates random `.env` credentials and backup passphrases only when absent. On Windows use `pwsh -File scripts/init-local.ps1` if allowed by the machine's execution policy. Preserve `.env`, `secrets`, and named volumes; ordinary `docker compose down` keeps volumes, while `down -v` destroys them.

Development's `COMPOSE_PROFILES=development` enables the sink. Postfix uses `[sink]:1025` for all destinations. The sink is a local verification tool, has no authentication, and must not be exposed publicly. Test/acceptance tools require development mode and the controlled Compose endpoints.

## Production prerequisites

Provision a Linux host with a static public IPv4, a permitted outgoing TCP/25 path, reverse DNS, persistent SSD storage, synchronized time, and independent backup storage. Obtain domain/DNS control and SMTP TLS certificates. Review [resource sizing](resources.md) before colocating with another backend.

| Network access | Purpose |
|---|---|
| Outgoing TCP/25 | Direct SMTP delivery to recipient MX |
| Incoming TCP/25 | DSNs for the controlled bounce domain |
| Incoming TCP/587, if used | Authenticated STARTTLS submission |
| HTTPS TCP/443 | REST API and receiver webhooks |
| TCP/80, optional | Caddy ACME challenge |
| PostgreSQL/Redis/AMQP/metrics | Private Docker network, not public host ports |

The provided production overlay publishes Postfix 25 and SMTP submission 587. HTTP remains loopback-bound unless served through your reverse proxy. RabbitMQ management loses its development host port.

## DNS and domains

Replace every example below with controlled names/IPs. `mail.example.org` is the MTA; `bounce.example.org` receives returns; `mail-api.example.org` exposes the API; projects own their Header From domains.

| Record | Purpose/example |
|---|---|
| `mail.example.org A` | Static public MTA IPv4 |
| IPv4 PTR | `mail.example.org`; forward A must match |
| `bounce.example.org MX` | `10 mail.example.org.` |
| `bounce.example.org TXT` | `v=spf1 ip4:YOUR_PUBLIC_IP -all` |
| `mail.example.org TXT` | SPF for HELO identity |
| `_selfmail.PROJECT_DOMAIN TXT` | Exact ownership token returned by the API |
| `SELECTOR._domainkey.PROJECT_DOMAIN TXT` | Exact generated DKIM public key |
| `_dmarc.PROJECT_DOMAIN TXT` | Begin with reporting policy, e.g. `v=DMARC1; p=none; rua=mailto:dmarc@CONTROLLED_DOMAIN` |

The envelope sender uses the bounce domain; Header From uses the project's verified domain. DKIM `d=` matches Header From for alignment. A shared bounce-domain SPF identity may have different alignment from a project's domain. Evaluate real headers and DMARC reports before tightening DMARC policy.

TXT segments longer than 255 characters must be split into quoted segments of **one** record. Obtain records through the domain API rather than inventing a selector/token. Call `/verify` after DNS propagates. DMARC reporting and Reply-To mailboxes must exist in a separate incoming-mail system; selfmail does not host personal inboxes.

## Production configuration

Create a private `.env.production` with independently generated credentials. Use long random hexadecimal passwords to avoid DSN escaping problems. These entries describe required settings; placeholder passwords are not usable credentials:

```dotenv
APP_ENV=production
COMPOSE_PROFILES=
POSTGRES_PASSWORD=RANDOM_HEX
APP_DB_PASSWORD=RANDOM_HEX
WORKER_DB_PASSWORD=RANDOM_HEX
RABBITMQ_USER=selfmail
RABBITMQ_PASSWORD=RANDOM_HEX
REDIS_PASSWORD=RANDOM_HEX
MASTER_KEY=BASE64_OF_32_RANDOM_BYTES
SMTP_HOSTNAME=mail.your-domain.tld
BOUNCE_DOMAIN=bounce.your-domain.tld
MAIL_API_HOST=mail-api.your-domain.tld
PUBLIC_URL=https://mail-api.your-domain.tld
ALLOW_PLAIN_SMTP=false
ALLOW_UNVERIFIED_DOMAINS=false
ALLOW_PRIVATE_WEBHOOKS=false
SMTP_TLS_CERT=/secrets/fullchain.pem
SMTP_TLS_KEY=/secrets/privkey.pem
WORKER_CONCURRENCY=2
DESTINATION_RATE_PER_SECOND=20
JOURNAL_MAX_BYTES=10737418240
MAX_WIRE_BYTES=10485760
```

Production refuses development bypass flags and requires HTTPS `PUBLIC_URL`, persistent recovery storage, and submission TLS. Postfix removes the development relay and uses MX delivery.

Place the SMTP pair in `secrets/`. **The API runs as UID 10001:** grant that UID read access to the exact certificate/private key and traversal of parent directories using ownership or a narrow ACL. A root-owned `0600` ACME private key is otherwise unreadable by the API. Keep the key private; do not make all secrets world-readable. The backup overlay provisions separate private credential volumes for PostgreSQL UID 999 and evidence-export UID 10001; host `.env` and source passphrases can remain `0600`.

Start core services and the optional backup profile:

```sh
docker compose -p selfmail-prod --env-file .env.production \
  -f compose.yaml -f compose.production.yaml -f compose.backup.yaml \
  up -d --build --wait
```

If this server has no existing HTTPS proxy, add `--profile edge` to enable Caddy. If the site's proxy already owns 80/443, route `mail-api.your-domain.tld` to loopback `18080` through that proxy and keep the optional edge disabled. Caddy's API certificate does not replace the SMTP certificate. Renew the SMTP pair atomically and restart the API/reload Postfix after renewal.

Create a tenant, publish/verify its DNS records, then test with a small number of controlled mailboxes. Check SPF/DKIM/DMARC, return path, bounce, queue, and destination responses before increasing volume. Host/IP reputation can limit delivery independently of acceptance throughput.

For two stacks on one Docker host, override `MTA_SUBNET`, `MTA_DYNAMIC_RANGE`, `MTA_WORKER_IP`, `MTA_POSTFIX_IP`, published ports, and RabbitMQ hostname/node name. Internal SQL/AMQP/Redis traffic is plaintext in the private single-host network. Distributed deployments require separately configured TLS and a different recovery-coordination design.

## Health and monitoring

- `/healthz`: API process liveness.
- `/readyz`: SQL, Redis, and admission/recovery-journal readiness. Broker availability is observed through consumer/dispatcher metrics; accepted outbox work can wait for it.
- Internal port `9090`: Go process metrics, scraped by Prometheus.

Enable observability with `--profile observability`. The repository supplies progress/queue-age/consumer/unknown/dead-webhook/WAL/spool alerts and Alertmanager routing. Development routes alerts to the local sink's receiver. **Replace that receiver with your monitored alert channel in production**, and test a firing and resolved alert.

Monitor API error rate/acceptance latency, oldest ready job/outbox age, consumer count, component progress, unknown attempts, unresolved dead callbacks, WAL archive failures, MTA log freshness, spool disk availability/growth, journal capacity, backup success time, filesystem/inode capacity, certificate expiry, and destination bounce/deferral trends. Backup success is recorded in `/var/lib/pgbackrest/status/success`; a dedicated backup-freshness/certificate/journal-capacity alert integration is operator work in this version.

Metric labels avoid recipient addresses and tenant IDs. An idle component's last log line is not sufficient proof of failure; combine progress with pending work. Counters aggregate asynchronously rather than scanning all message history for each scrape.

The `selfmail_work_seconds` histogram has stages `journal_lock_wait`, `journal_write`, `outbox_publish`, and `reconciliation`. Read its `_sum`/`_count` or quantiles from `_bucket` per process; sums from concurrent processes can exceed elapsed wall time. A journal observation describes one operation, which can now contain several log records. These metrics contain no recipient or tenant labels.

## Routine controls

```sh
docker compose exec postfix postqueue -p
docker compose exec postfix postqueue -f
docker compose logs --tail 100 api worker dispatcher reconciler webhooks
docker compose exec worker selfmail tenant pause --id TENANT_UUID
docker compose exec worker selfmail tenant resume --id TENANT_UUID
docker compose exec worker selfmail cleanup
docker compose exec worker selfmail cleanup --apply
```

Use the same `-p`, `--env-file`, and overlay list as deployment for every production command; examples above omit them only for readability. `cleanup` defaults to a bounded dry run. The maintainer executes bounded cleanup automatically, drains work with short continuation, and backs off hourly when idle.

Inspect `submission_unknown` by message/attempt ID, current queue, all retained logs, and journal evidence. A delayed status is not a reason to resend a submitted message. `selfmail resolve --id MESSAGE_UUID --reason 'Reviewed spool and archives; accepted duplicate risk' --confirm-not-in-queue` is an audited operator override and can produce a duplicate if the conclusion is wrong.

### Webhook repair

Correct the receiver secret first, then repair/replay:

```sh
docker compose exec worker selfmail webhook repair \
  --tenant TENANT_UUID --endpoint ENDPOINT_UUID \
  --secret-file /secrets/webhook-replacement --reason 'Receiver and secret repaired'
docker compose exec worker selfmail webhook replay \
  --tenant TENANT_UUID --endpoint ENDPOINT_UUID --limit 100 \
  --reason 'Replay original events after receiver repair'
docker compose exec worker selfmail webhook resolve-dead \
  --tenant TENANT_UUID --endpoint ENDPOINT_UUID \
  --reason 'Business state reconciled; remaining callback failures acknowledged'
```

Make the replacement file readable by the command's UID through a targeted mount/ACL. Replay preserves event IDs; the receiver must deduplicate. Unresolved dead jobs protect related metadata from cleanup.

## Retention

| Data | Default eligibility |
|---|---|
| Body/raw MIME/attachments | All batch recipients final for at least 7 days |
| Message/attempt/event metadata | All final for at least 30 days; no pending/unresolved-dead callback |
| Batch response/fingerprint/key digest | At least 90 days; no retained message/payload |
| Published final outbox references | 7 days |
| Delivered/resolved-dead callback jobs | 30 days |
| Audit records | 365 days |
| MTA logs/receipts | At least 90 days; unread/active evidence protected |
| Completed file dedupe/cursors | 92 days |
| Independent journal | Append-only; capacity bound, no automatic compaction |

These are minimum ages for eligibility, not statutory or exact deletion deadlines. Active work, unknown handoffs, pending callbacks, and cleanup failure extend retention. Change the policy through a reviewed implementation/configuration change for your legal/business requirements. Retention horizons are not environment-configurable; journal/MIME capacities and, in current development code, the destination delivery rate are.

Retention retirement decisions are durable outside SQL before commit. If the cleanup transaction fails after recording a retirement, normal SQL may temporarily retain eligible rows; subsequent reconciliation applies the recorded decision. A 90-day key can be reused only after its tombstone is actually retired/deleted. SQL metadata returns `410` while the tombstone remains.

Postfix rotates at approximately 10 MiB or hourly, waits for ingestion before compression, and retains archives for at least 90 days. Never apply external `copytruncate` or blindly gzip unread files. Logs can contain addresses/diagnostics; protect their volumes and backup access.

## Upgrades and rollback

Pin a reviewed source revision/image digest. Back up keys, independent evidence, and SQL/WAL first. Establish a recovery hold, stop processes that write mail state, apply migrations with `selfmail migrate`, deploy compatible binaries, reconcile if required, then release through the recovery protocol. A migration checksum/version mismatch stops incompatible runtime binaries.

Migrations are numbered immutable SQL files with advisory locking and a checksum ledger. The current schema includes `011_digest_idempotency.sql`. Upgrade tests cover the earlier 005 schema with all ten delivery statuses. Initial legacy adoption uses `recovery seed-upgrade` only under reviewed hold after comparing actual MTA history; it cannot reconstruct evidence already lost.

Rollback is not accomplished by simply starting an older binary against a newer schema. Restore a compatible database through the controlled [recovery procedure](recovery.md), preserving the newest independent delivery evidence. A Docker restart policy is process recovery, not a host-failover strategy.
