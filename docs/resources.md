# Resource sizing

Resource use depends on retained payload size, attachment frequency, remote deferrals, backup work, and history. Acceptance rate is not delivery rate. A fast local sink cannot model slow or rejecting Internet MX servers.

## Planning envelope

| Profile | Starting allocation |
|---|---|
| Modest standalone core | 2 vCPU, 4 GiB RAM, 30 GiB SSD |
| Core + monitoring/backups, or colocated application | 4 vCPU, 8 GiB RAM, 60 GiB SSD plus independent backup storage |
| Development build/tests/large-history drills | Additional memory/CPU and disposable disk beyond runtime needs |

These are engineering sizing recommendations, **not a validated minimum VPS specification or SLA**. Benchmark your payload mix and shared application workload. Leave OS/filesystem cache and backup overhead outside container budgets. A server with 4 GiB cannot accommodate every configured ceiling simultaneously plus the OS.

## Configured steady-service limits

| Service | Memory ceiling | CPU quota | Go heap soft limit |
|---|---:|---:|---:|
| API + submission | 256 MiB | 0.50 | 192 MiB |
| Worker | 512 MiB | 1.00 | 384 MiB |
| Dispatcher | 128 MiB | 0.25 | 96 MiB |
| Reconciler | 256 MiB | 0.50 | 192 MiB |
| Webhooks | 128 MiB | 0.25 | 96 MiB |
| Bounce | 128 MiB | 0.25 | 96 MiB |
| Maintainer | 128 MiB | 0.25 | 96 MiB |
| PostgreSQL | 1024 MiB | 1.00 | — |
| RabbitMQ | 768 MiB | 1.00 | — |
| Redis | 256 MiB | 0.25 | — |
| Postfix | 256 MiB | 0.50 | — |
| Prometheus, optional | 256 MiB | 0.50 | — |
| Alertmanager, optional | 128 MiB | 0.25 | — |
| Backup loop, optional | 256 MiB | 0.50 | — |
| Caddy, optional | 128 MiB | 0.25 | — |

Core ceilings sum to **3840 MiB / 3.75 GiB**; the full optional profile adds 768 MiB for **4.5 GiB**. CPU quotas are maxima per service and can be oversubscribed; they are not reserved cores. Tests/compilers, one-off backup/export, sink, and init jobs are outside this steady profile.

PostgreSQL uses 128 MiB shared buffers, 120 connections, 4 MiB work_mem, and 256 MiB shared-memory allocation. RabbitMQ uses two Erlang schedulers. Redis is limited to 128 MiB data memory with `noeviction`; rate checks fail closed if the store is unavailable/full. Docker service logs rotate at 10 MiB × 3 files. Prometheus retention is 7 days / 512 MiB.

Go `GOMEMLIMIT` is a soft managed-heap target, not an RSS guarantee. Container ceilings cover other memory too; the ingress budget admits two heavy operations and worker MIME work is limited to four in-flight jobs. Raising `WORKER_CONCURRENCY` increases consumers per priority without removing that shared budget. Multiple worker replicas multiply the process budgets and require careful sizing/coordinated deployment.

## Measured local profile

Verification host: Docker 29.7.2, Linux VM with **12 CPUs / about 8 GiB RAM**. These measurements are not from a 2-vCPU VPS. The October 8 repeat sampled Docker statistics throughout a 30-minute mixed-payload run; 899 messages were accepted/delivered, zero duplicates, at approximately 0.5/s. It included other verification work and a PostgreSQL service restart while credential provisioning changed.

| Service | Peak sampled MiB, repeat | Earlier history/backup stress peak MiB |
|---|---:|---:|
| API | 81.83 | 76.42 |
| Worker | 88.15 | 78.24 |
| PostgreSQL | 356.70 | 769.30 |
| RabbitMQ | 154.80 | 172.80 |
| Redis | 16.25 | 25.44 |
| Postfix | 40.46 | 34.36 |
| Reconciler | 21.61 | 16.35 |
| Dispatcher / webhooks / maintainer / bounce | each below 12 | each below 13 |
| Prometheus | 96.21 | 118.40 |
| Alertmanager | 35.10 | 36.34 |
| Backup loop | 16.48 | 24.02 |

The repeat collected 156 sampling cycles from 17:13:25 to 17:45:02 UTC. Sampling has gaps and cannot prove an absolute peak. Docker's reported working set and CPU snapshots differ from total host memory and cgroup accounting. RabbitMQ briefly showed over 100% CPU in samples; quotas are enforced over periods, not on a single display sample.

The earlier 900-message run completed without duplicate copies. Its overlapping resource sample covered backup/PITR and million-row history work, but not the entire successful run. All configured main-container memory limits were inspected; no main-service OOM was observed. A separate deliberately undersized worker was genuinely OOM-killed and tested for ambiguity-safe recovery. See [verification](verification.md).

## Disk and retention budget

The service limits retained **serialized payload bytes to 512 MiB per tenant** by default, and active backlog to 100,000 recipients per tenant. The payload cap is an admission safeguard, not a seven-day storage promise for every allowed daily rate. Increasing it currently requires a reviewed store-policy change. Three tenants do not automatically receive unlimited body storage.

Estimate at least:

```text
SQL payload ≈ accepted batches/day × retained days × serialized average payload
MTA spool   ≈ deferred messages × encoded MIME size
Journal     ≈ retained evidence records × encrypted record/manifest size
Backup      ≈ retained full/diff/incremental data + archived WAL
Staging     ≈ number of unremoved exports × snapshot prefix size
```

JSON/base64 adds roughly one third to binary attachment bytes; include row/index/WAL/backup overhead. For example, 100 receipts/day with 2 MiB PDFs for seven days require about 1.8 GiB of serialized attachment data alone and exceed the default per-tenant cap. Small technical mail and occasional attachments have a very different profile.

Bodies are eligible after seven days of final outcomes. Remote deferrals, unknown handoffs, or protected callbacks extend storage. Queue/dead-letter reference history, Postfix archives, evidence staging, and encrypted backups also require independent disk monitoring. The dead-letter queue and evidence staging have no automatic purge in this release.

`JOURNAL_MAX_BYTES` defaults to 10 GiB (valid range 64 MiB–1 TiB). The journal is append-only and stops new acceptance at capacity; SQL cleanup does not compact it. Admission also checks a 128 MiB filesystem reserve. Postfix relies on its spool/filesystem safeguards and emits disk/queue snapshots; supplied alerts warn below 1 GiB free or above 1 GiB spool size. These thresholds are not filesystem quotas. Use dedicated volume quotas where your host supports them and monitor inodes as well as bytes.

## Scaling decisions

Start with one bounded worker and observe ready-queue age and destination deferrals. Scale worker CPU/concurrency when MIME/signing is the bottleneck; scale SQL when indexed queue/retention work is slow; add space or adjust policy when retained bodies or spool dominate. Increasing workers will not fix blocked port 25, recipient throttling, or IP reputation.

The current FIFO outbox and shared Postfix spool mean priority queues do not constitute a hard end-to-end latency SLA. One recipient-domain bottleneck is paced independently, but public-MX behavior still needs controlled load testing. Multi-host delivery/recovery requires a redesigned coordinator and failure-domain plan; do not replicate local file locks across arbitrary shared storage and assume HA.
