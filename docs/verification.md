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

## Throughput evidence for 0.0.1

The earlier publication highlighted the 900-message soak without making its purpose sufficiently prominent. Its generator intentionally offered only 0.5/s. That figure was **never a measurement of the service's maximum throughput**. The following separate unpaced, concurrent measurements replace it as throughput evidence.

Runs on October 8 used Docker 29.7.2, a Linux/amd64 VM with 12 logical CPUs and approximately 7.67 GiB RAM, on an Intel Core i5-12400F host. Other coffee/jewelry application containers remained running. All service CPU/memory limits from `compose.yaml` were retained; monitoring/backups/history drills were not active during these timed runs. Runtime core sources were unchanged from `760724b`; the new generator uses the same REST, outbox, RabbitMQ, DKIM, Postfix and reconciliation paths. No delivery provider or Internet mailbox was used.

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
