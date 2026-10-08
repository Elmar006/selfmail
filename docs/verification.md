# Verification and readiness

This document records concise engineering evidence, not raw test logs. Verification took place on October 8, 2026 in isolated Docker/Linux development storage, with mail directed only to the local sink. Public-domain/IP/DNS delivery was not attempted.

## Reproduce the routine checks

```sh
sh scripts/init-local.sh
docker compose up -d --build --wait
docker compose --profile test build tests
docker compose --profile test run --rm tests sh scripts/verify.sh
docker compose --profile test run --rm acceptance
docker compose --profile test run --rm --no-deps \
  -e ACCEPTANCE_API_URL=http://api:8080 -e ACCEPTANCE_SINK_URL=http://sink:8025 \
  tests go run ./cmd/benchmark -count 12 -concurrency 2 -timeout 2m
python3 -m pip install PyYAML==6.0.3
python3 scripts/check-api-contract.py
docker run --rm -v "$PWD/deploy/prometheus:/rules:ro" -w /rules \
  --entrypoint promtool \
  prom/prometheus:v3.13.4@sha256:87861b8cf91579109319ebc300f3f1060e6da9c05d6ae8ad15a20c879e84e32e \
  test rules rules.test.yml
```

`verify.sh` checks formatting, module checksums, all Go packages with `-race -count=1`, `go vet`, command builds, and govulncheck 1.8.0. Integration tests require SQL/Redis/RabbitMQ and are not silently substituted with unit mocks. Test databases, upgrade/history databases, namespaces, and tenant identities are isolated; run only on a development installation.

The final local routine run passed the full race/vet/build/module/vulnerability pipeline. OpenAPI checks passed for 14 paths, including reference integrity and error response shapes. Prometheus rule tests passed. The final running images passed acceptance through REST → SQL outbox → RabbitMQ → worker → Postfix → sink → delivered, plus SMTP AUTH, DKIM, attachments, templates, quotas, RLS, cancellation, relay rejection, hard bounce, late DSN, and signed callbacks.

## Audit remediation coverage

| ID | Addressed defect | Relevant evidence |
|---|---|---|
| A01 | Stale preparing phase during watchdog/worker race | Real-SQL sending recovery-fence regression |
| A02 | Expired lease accepted after lock wait | Lock-wait regression with actual post-lock time |
| A03 | Empty/nested templates consuming unlimited work | Cancellation, operation/iteration/depth/function bounds |
| A04 | Compressed archives skipped | Compressed delivery evidence and corrupt-archive regression |
| A05 | Valid decoded body overflowing encoded MIME | Large UTF-8 preflight/final signed budget tests and load |
| A06 | Poison endpoint starving healthy callbacks | Corrupt-secret neighbor/repair/replay regression |
| A07 | Recipient grouping defeating domain pacing | Configured/runtime Postfix domain grouping; Internet slow-MX behavior remains unvalidated |
| A08 | Missing retention/backup/restore policy | Active/idempotency protection, retirement, encrypted backup/check/restore and PITR drills |
| A09 | Unbounded process/work/storage pressure | Inspected cgroup ceilings, work permits, backlog/storage/journal bounds, OOM and ENOSPC drills |
| A10 | SQL rollback allowing a second SMTP copy | Independent journal, held generations, physical PITR one-copy case, retirement/key-reuse regressions |
| A11 | Historical scans on recovery/metrics | Million-row history + million published outbox references, indexed EXPLAIN and async counters |
| A12 | Reachable Go vulnerabilities and old toolchain | Updated modules/Go, zero callable/imported govuln findings, image scans; vendor OS advisories disclosed |
| A13 | Missing progress/fault alerts | Progress metrics, readiness, rule tests, actual paused-dispatcher firing and resolved alert path |
| A14 | Inconsistent limits/SDK/events contract | UTF-8 byte limits, URL checks, webhook helper, >1100-event pagination, OpenAPI references |
| A15 | Mutable artifacts and unsafe upgrades | Pinned bases/actions, checksummed migration ledger, 005 upgrade across ten states, CI/release definitions |
| A16 | Cached SMTP authorization surviving revoke | Live session AUTH → revoke → rejected DATA; transaction authorization lock |

A second independent read-only source review checked the changes and found additional edge cases before publication. They received regressions for retention tombstones/key reuse, recovery surrogate collision, replacement-host identity import, cleanup continuation beyond one batch, incorrect export keys, and folded/oversized MIME headers. The reviewer did not independently execute the final tests; the local runtime/test evidence above comes from the implementation verification run.

## Throughput evidence for the original release snapshot

The earlier publication highlighted the 900-message soak without making its purpose sufficiently prominent. Its generator intentionally offered only 0.5/s. That figure was **never a measurement of the service's maximum throughput**. The following separate unpaced, concurrent measurements replace it as throughput evidence.

Runs on October 8 used Docker 29.7.2, a Linux/amd64 VM with 12 logical CPUs and approximately 7.67 GiB RAM, on an Intel Core i5-12400F host. Other application containers remained running. All service CPU/memory limits from `compose.yaml` were retained; monitoring/backups/history drills were not active during these timed runs. Runtime core sources were unchanged from `760724b`; the new generator uses the same REST, outbox, RabbitMQ, DKIM, Postfix and reconciliation paths. No delivery provider or Internet mailbox was used. These measurements describe the original release snapshot `d446bc9`, not every build later published under the same version label.

Three fresh tenants per run used rate 10,000/s and one recipient per request; clients had concurrency **2**, and all recipients belonged to one synthetic destination domain. Redis's 20/s destination bucket remained enabled. All recipients routed to the **single development SMTP sink**. Setup/domain-key generation is excluded from timing.

| Profile | Requested / accepted / delivered | API acceptance window | API accepted/s | Time to all delivery confirmations | Confirmed end-to-end/s | API latency p95 / p99 |
|---|---:|---:|---:|---:|---:|---:|
| 1 KiB bodies; stock Postfix destination delay `1s` | 120 / 120 / 120 | 5.145 s | 23.32 | 121.004 s | 0.99 | 247.98 / 335.47 ms |
| 1 KiB bodies; local Postfix destination delay `0s` | 600 / 600 / 600 | 43.785 s | 13.70 | 73.007 s | 8.22 | 445.25 / 939.89 ms |
| 1 KiB bodies; repeat with local Postfix delay `0s` | 1200 / 1200 / 1200 | 69.144 s | 17.36 | 114.012 s | 10.53 | 333.64 / 718.70 ms |
| 1 KiB bodies + 2 MiB random-byte attachment each; local delay `0s` | 96 / 96 / 96 | 14.724 s | 6.52 | 17.796 s | 5.39 | 522.26 / 854.65 ms |

Every row had **zero API errors, zero missing copies, zero extra sink copies**, and all SQL messages in `delivered`. The last row used incompressible synthetic binary data, not repeated-character PDF bytes or actual receipt generation. An exploratory compressible fixture was replaced; its more favorable result is not used as the attachment capacity profile. Postfix concurrency remained 5 and recipient grouping 50 throughout. The temporary `0s` relay pacing was restored to `1s` afterward.

An initial overload probe used 8 API clients and 120 requests: **2 accepted, 118 HTTP 503**, both accepted messages delivered once. It **failed** the benchmark's complete-workload criteria and is not reported as successful throughput. The shared ingress work budget intentionally admits two heavy operations; applications must use backpressure and idempotent retries rather than assume arbitrary concurrency is accepted.

These are finite closed-loop local workloads, not an absolute saturation curve, sustained multi-hour capacity, Internet delivery guarantee, or tested VPS SLA. Different run lengths and host activity produce different observed rates; the results are not averaged into an unsupported headline maximum. Delivery rate includes queue drain and SQL confirmation; acceptance rate describes the API. Detailed definitions, safeguards and commands are in [benchmarking](benchmarking.md). Resource samples are in [resources](resources.md).

## Batching comparison after 0.0.1

The optimized implementation was introduced after the original `d446bc9` release snapshot: core change **`93938aa`**, followed by **`bebce3d`** exposing the destination rate without changing its default. The maintainer has subsequently included these changes and application-neutral documentation in a republished **`v0.0.1`**, keeping the version number. No schema migration, API/SDK contract change, resource-limit increase, or removal of durable-write/confirm checks is required. The historical results below retain their measured commit identities; version labels alone do not identify those earlier runs.

The comparison retained the same Docker/Linux host and service limits, three fresh tenants per workload, 1200 one-recipient requests, 1 KiB bodies, client concurrency two, Redis destination policy 20/s, Postfix destination concurrency 5/grouping 50, and the single controlled development sink. Only local Postfix destination delay was temporarily `0s`. Other application containers remained active; host/storage conditions were not isolated.

| Runtime / storage state | API acceptance window | Accepted/s | All delivery confirmations | Confirmed end-to-end/s | API p95 / p99 |
|---|---:|---:|---:|---:|---:|
| Original `d446bc9` core + timing hooks; fresh storage | 82.899 s | 14.48 | 134.012 s | 8.95 | 418.13 / 828.89 ms |
| Optimized `93938aa`; fresh storage | 77.694 s | 15.45 | 78.701 s | 15.25 | 323.98 / 676.93 ms |
| Optimized `bebce3d`; repeat after preceding workload, same default policy | 120.485 s | 9.96 | 121.496 s | 9.88 | 630.45 / 1192.31 ms |
| Original `d446bc9` core; recheck on accumulated completed history | 111.002 s | 10.81 | 189.022 s | 6.35 | 556.24 / 1075.31 ms |

All four workloads had **1200 accepted, 1200 delivered, zero API errors, zero missing copies, and zero extra sink copies**. Their run IDs were `3ba2b4f5-eb95-42fb-b2b6-7ecb22b6bf7f`, `cb04b2a5-4ae3-45fe-9d13-4ca408f47dec`, `8bfc561d-79c6-4453-928f-596af478a20c`, and `e2cf3abe-9231-477d-b75b-d4c0cfca7a3c`; start times were October 8 at 21:38:15, 21:56:39, 22:06:02, and 22:10:09 UTC, respectively. Both baseline images differ from `d446bc9` only by the same timing hooks used in the optimized code.

The fresh comparison observed **1.70 times** the end-to-end rate and about **41% less completion time**. The slower optimized repeat is retained explicitly: it does not establish a constant 15/s rate or a guaranteed 70% improvement under every host condition. The original release's earlier 114-second run is a separate measurement; it was not substituted for the new baseline.

The later baseline recheck confirmed the same bottleneck under slower conditions: it needed **78.02 seconds of queue drain** and SQL delivery latency p50/p95 of **81.53/99.61 seconds**. The optimized repeat's 121.496 seconds versus this baseline's 189.022 seconds corresponds to **1.56 times** the observed end-to-end rate, with a slower API acceptance window. These sequential runs shared completed history and active host workloads; they are not randomized statistical trials. The legacy baseline also successfully validated/read the completed v1 journal history written by the grouped implementation before this workload.

The initial optimized queue-drain interval fell from **51.11 to 1.01 seconds**. SQL delivery latency p50/p95 fell from **53.09/66.13 seconds** to **1.01/1.88 seconds**; the slower repeat still observed **1.28/2.57 seconds**. The change removes a large confirmation backlog while acceptance throughput remains sensitive to the serialized journal.

Recorded timing sums for the initial comparison were outbox operations **110.66 → 27.94 seconds**, reconciliation **131.80 → 26.50 seconds**, and time holding the journal write lock **103.58 → 71.02 seconds** across the relevant processes. These are operation sums, not isolated component benchmarks; batches/idle polls and overlapping processes must be accounted for. The slower repeat spent **113.33 seconds** in the journal write path. See the scope of [resource samples](resources.md).

An intermediate implementation batched broker/SQL work but still wrote each log journal record separately: it completed 1200/1200 in **97.616 seconds**, with API acceptance **95.610 seconds**, zero errors and extra copies. This exposed increased ingress contention and led to grouped journal durability rather than using its faster delivery rate as the final result.

The final full formatting/module/race/vet/build/govulncheck pipeline passed against real PostgreSQL, RabbitMQ, and Redis. Added regressions cover partial broker success, mandatory returns/Nacks/closed channels/timeouts, replay of all uncommitted outbox references, duplicate claims, waiting-slot cancellation, SQL batch rollback/cursors/terminal outcomes, partial/oversized log tails, mixed journal writers, seven interrupted batch phases, rejection of lost sealed history, and independent-journal reconciliation of older SQL state using grouped log records.

The final `bebce3d` runtime also passed the complete acceptance flow after restoring stock Postfix delay to `1s`: REST/outbox/RabbitMQ/worker/Postfix/sink/delivered, three tenants, SMTP AUTH, DKIM, attachments, templates, idempotency, RLS, quotas, cancellation, relay protection, hard bounce, late DSN, and signed callbacks. Final running containers reported no OOM kill or automatic restart; intentional image replacements are separate from restart counters.

A larger-payload check on `bebce3d` sent **96 messages with a random 2 MiB attachment each**: acceptance **29.325 seconds / 3.27/s**, complete delivery confirmation **31.003 seconds / 3.10/s**, API p95/p99 **1704.31/2587.86 ms**, zero errors/missing/extra copies. Run `03946515-9fe0-41b9-abd6-1ffb2edfdab8` started at 22:14:44 UTC. This is slower than the earlier release snapshot's 17.796-second attachment profile on different host conditions; it is retained as a payload check, and no attachment-throughput improvement is claimed. Benchmark the application's actual document sizes and payload mix rather than extrapolating the small-body comparison.

## Fixed-rate stability and earlier resource evidence

The separate mixed-payload soak generator cycles through three tenants/priorities, includes 2 MiB PDF attachment bytes, and sends a near-5-MiB UTF-8 body periodically. It requires the fixed development API/sink URLs. Its `-interval 2s` schedules 0.5 requests/s and does not search for a throughput ceiling.

```sh
docker compose --profile test run --rm \
  -e ACCEPTANCE_API_URL=http://api:8080 -e ACCEPTANCE_SINK_URL=http://sink:8025 \
  tests go run ./cmd/loadtest -duration 30m -interval 2s
```

- First successful run: **900 accepted, 900 delivered, 0 duplicates**, approximately 0.5 messages/s; maximum observed acceptance 2061 ms. Concurrent work included backup/PITR, large-history verification, and an approximately 110-second dispatcher pause/drain.
- Repeat: **899 accepted, 899 delivered, 0 duplicates in 30m2s**, maximum acceptance 4106 ms, including a short PostgreSQL restart while credential mounts changed and concurrent verification. The slight count difference reflects elapsed-time scheduling.
- The repeat collected 156 Docker sampling cycles covering the run. [Resources](resources.md) reports the samples and their limits; a 10-second sampler cannot establish an absolute peak or production SLA.
- An earlier attempt exposed a journal snapshot export holding the writer lock too long and returning temporary admission errors. The exporter was changed to capture a short locked prefix and copy immutable records outside that lock; the successful runs/evidence export checks followed. Failed attempts are not counted as successful load results.

The repeat runtime preceded the last source-review fixes; those fixes were subsequently validated by the full suite and final acceptance. No claim is made that all latest code paths ran under the entire soak window.

## Fault and recovery drills

| Drill | Observed local result |
|---|---|
| Actual cgroup OOM after SMTP DATA success | Worker killed with exit 137 / OOMKilled; one sink copy; recovery kept the sending attempt unknown and denied a second claim |
| Actual ENOSPC on isolated 8 MiB tmpfs | Admission/journal write failed safely; existing durable evidence remained valid after space was freed |
| Physical encrypted PG backup + WAL/PITR to separate volume | Queued earlier snapshot reconciled to already delivered using current independent journal; no second claim; one sink copy |
| Restic evidence backup/check/restore | Repository read-data check passed and restored files verified; export includes config/key and PostgreSQL backup passphrase |
| Replacement-control identity import | Actual CLI imported the original authenticated instance into new isolated Docker volumes, generation increased and held with no release proof; unit test rejects old proof |
| Dispatcher pause | Actual Prometheus → Alertmanager → local alert receiver firing then resolved |
| Million-row history | Watchdog about 143 ms / metric scrape about 704 ms in the recorded case; indexed workload paths and 32-row counter read |

OOM/ENOSPC tools are restricted to deliberately isolated development storage. Do not run fault helpers against a production host or reuse live volumes. Their code remains available for reproducibility; private scenario files/logs/binaries are excluded from publication.

## What this does and does not establish

The code, local service flow, important concurrency/failure boundaries, and supplied tests are validated locally. CI repeats routine checks in a fresh GitHub runner; release definitions add tagged image publication with SBOM/provenance. Check the actual workflow result for a published commit before treating it as CI-validated; merely checking in a workflow is not that evidence.

Local evidence does **not** establish Internet MX pacing, DNS/IP reputation, SPF/DKIM/DMARC across real providers, inbox placement, a remote SSH/SFTP backup SLA, loss of the entire host, multi-node HA/failover, exact production RPO/RTO, or the recommended VPS throughput. Backup/evidence scheduling, staging/repository retention, target-host TLS permissions/renewal, and monitoring destinations must be configured and tested on your server.

SMTP exactly-once and guaranteed human receipt are not claimed. `submission_unknown` intentionally favors preventing blind duplicate mail over automatic progress. Operators can explicitly override it only after review. The deployment remains one failure domain; production acceptance is documented in [security](security.md) and [recovery](recovery.md).
