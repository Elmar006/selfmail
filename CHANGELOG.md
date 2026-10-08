# Changelog

## 0.0.1 — 2026-10-08

- Initial tagged MIT release of selfmail, developed by **Эльмар**.
- Reusable REST/SMTP/Go SDK integration with tenant isolation, PostgreSQL outbox, RabbitMQ quorum queues, Redis limits, DKIM signing and self-hosted Postfix delivery.
- Durable submission evidence, guarded recovery, operator handling of ambiguous SMTP outcomes, retention, monitoring and backup/restore tooling.
- Separate parallel development throughput benchmark with API/delivery rates, latency percentiles, exact SMTP copy checks, workload bounds and a CI smoke run.
- Corrected attribution and performance documentation: the historical 900-message run was an intentionally paced stability soak. Published measurements now identify payloads, runtime limits, Postfix pacing, rejected overload and the local-only validation scope.

See [verification](docs/verification.md) for measured results and remaining production dependencies. Tagged image workflows publish the application, Postfix and PostgreSQL images with build provenance and SBOMs after CI verification.
