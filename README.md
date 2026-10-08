<p align="center"><img src="docs/assets/selfmail-hero.png" alt="selfmail — self-hosted transactional email" width="100%"></p>
<p align="center">
  <a href="https://github.com/Elmar006/selfmail/actions/workflows/ci.yaml"><img src="https://github.com/Elmar006/selfmail/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-16b8a6" alt="MIT"></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go 1.26">
  <img src="https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white" alt="PostgreSQL 18">
  <img src="https://img.shields.io/badge/RabbitMQ-4.3-FF6600?logo=rabbitmq&logoColor=white" alt="RabbitMQ 4.3">
  <img src="https://img.shields.io/badge/Redis-8-DC382D?logo=redis&logoColor=white" alt="Redis 8">
</p>
<p align="center"><strong>Your applications. Your mail queue. Your infrastructure.</strong><br>
  <a href="README.ru.md">Русский</a> · <a href="docs/architecture.md">Architecture</a> · <a href="docs/integration.md">Integration</a> · <a href="docs/operations.md">Deployment</a> · <a href="api/openapi.yaml">OpenAPI</a>
</p>

# selfmail

**selfmail is a reusable, self-hosted transactional email service.** Connect any backend through REST, authenticated SMTP submission, or the Go SDK. Run one installation for several projects with separate credentials, domains, quotas, templates, and delivery events.

Developed by **Альмар 06 / [Elmar006](https://github.com/Elmar006)**. The project is free software under **[MIT](LICENSE)**: use, modify, and integrate it in personal or commercial projects. No paid delivery provider is required. You operate the server and domains; infrastructure and third-party software retain their own costs and licenses.

## Who it is for

| Project | Example emails |
|---|---|
| Coffee shop | Purchase receipts, password resets, account changes, login notifications |
| Jewelry store | Order confirmations, PDF invoices, payment and delivery updates |
| SaaS or internal platform | Verification links, security alerts, technical notifications |
| Several applications | One installation with a tenant and restricted API key per project |

Install selfmail beside your application on the same Linux server or on a separate mail host. The network contract supports any backend language. Receipt generation, payment processing, and token validation stay in your application.

## What is included

- **Postfix delivery** directly to recipient MX servers in production; a local mail sink in development.
- **Durable jobs** with PostgreSQL, transactional outbox, RabbitMQ quorum queues, publisher confirms, and manual acknowledgements.
- **Project isolation** with row-level security, scoped/revocable keys, sender verification, and encrypted DKIM keys.
- **Controlled sending** with three priorities, Redis rate limits, UTC daily quotas, scheduling, expiration, attachments, and bounded templates.
- **Delivery tracking** from Postfix logs and DSNs, recipient suppression, signed webhooks, pagination, and audited operator actions.
- **Recovery tooling** with an independent encrypted handoff journal, restoration hold, reconciliation, encrypted physical backups, and WAL/PITR.
- **Operational limits** for memory, CPU, backlog, payload storage, MIME size, journal capacity, and logs.
- **Monitoring and verification** with Prometheus, Alertmanager, integration tests, race detection, fault drills, dependency checks, and release workflows.

**Delivery semantics:** `202` means durable acceptance; `submitted` means Postfix accepted the message; `delivered` means the destination SMTP server returned success. SMTP cannot guarantee exactly-once delivery, inbox placement, or reading. Ambiguous handoff becomes `submission_unknown` and is reconciled before an operator-authorized retry. See [delivery guarantees](docs/architecture.md#delivery-guarantees).

## Stack

| Component | Purpose |
|---|---|
| Go 1.26 | API, SMTP submission, workers, CLI, SDK |
| PostgreSQL 18 / pgx | Authoritative state, outbox, quotas, RLS, migrations |
| RabbitMQ 4.3 | Durable quorum queues: `critical`, `normal`, `bulk`, dead letters |
| Redis 8 | Atomic ingress, project, and recipient-domain rate limits |
| Postfix 3.10 | SMTP queue, destination pacing, MX delivery, retry, DSNs |
| pgBackRest 2.59.3 / Restic 0.18.1 | Encrypted database/WAL and independent evidence backups |
| Prometheus / Alertmanager | Metrics, progress alerts, alert routing |
| Docker Compose / optional Caddy | Single-host deployment and HTTPS |

Image bases and CI actions are pinned by digest/commit. Module versions are in [go.mod](go.mod). [Security notes](docs/security.md) explain the scan policy and remaining vendor advisories.

## Run locally

Install Git and Docker with Compose **2.24.4+**. Linux/WSL initialization also needs OpenSSL. Docker Desktop works for Windows development.

```sh
git clone https://github.com/Elmar006/selfmail.git
cd selfmail
sh scripts/init-local.sh
docker compose up -d --build --wait
docker compose exec worker selfmail tenant create --name coffee --domain coffee.example.test
```

On Windows, use `pwsh -File scripts/init-local.ps1` instead of the shell initializer when script execution is permitted, or use WSL. Initialization generates secrets and preserves existing configuration. Save the API key from `tenant create`; it is shown once. Set `SELFMAIL_API_KEY` in your shell to that key for the request below.

```sh
curl http://localhost:18080/v1/messages \
  -H "Authorization: Bearer $SELFMAIL_API_KEY" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: coffee:receipt:order-42:v1" \
  -d '{"from":"receipts@coffee.example.test","to":["customer@example.net"],"subject":"Your receipt","text":"Thank you for your purchase.","priority":"normal"}'
```

Open the [local inbox](http://localhost:18025). Development routes every message to the sink, including messages addressed to real domains.

| Local endpoint | Address |
|---|---|
| REST API | `http://localhost:18080` |
| SMTP submission | `localhost:1587` — tenant ID / API key |
| Mail sink | `http://localhost:18025` |
| RabbitMQ management | `http://localhost:15673` — credentials from private `.env` |
| Prometheus, optional | `http://localhost:19090` |

## Integrate a backend

Create a tenant for each application. Issue a key with `messages:write,messages:read` and store it on the backend. Use a stable idempotency key per business operation, reusing it after a timeout.

```go
import "github.com/Elmar006/selfmail/pkg/client"

mailer, err := client.New("http://localhost:18080", apiKey)
if err != nil { return err }
result, err := mailer.Send(ctx, "coffee:password-reset:"+requestID, client.SendRequest{
    From: "accounts@coffee.example.test",
    To: []string{userEmail},
    Subject: "Reset your password",
    Text: "Open your single-use reset link: " + resetURL,
    Priority: "critical", TTLSeconds: 600,
})
```

For receipts, commit the order and an email task in **your application's transactional outbox**, then call selfmail asynchronously. The [integration guide](docs/integration.md) covers Go, SMTP/Nodemailer, webhooks, domain onboarding, retries, and this business flow.

## Resources and requirements

**Planning baseline:** 2 vCPU / 4 GiB RAM / 30 GiB SSD for a modest standalone installation. Start with **4 vCPU / 8 GiB RAM / 60 GiB SSD** when enabling backup/monitoring or sharing the machine with an application. These are sizing recommendations, not tested throughput guarantees. Allocate independent backup storage and monitor journal growth.

Configured memory ceilings sum to **3.75 GiB for core services**, or **4.5 GiB with monitoring, backup, and Caddy**, excluding builds/tests and the OS. Consumption is normally below those ceilings. [Resource sizing](docs/resources.md) gives measured samples, individual limits, and disk assumptions.

A local test delivered **900/900 messages without duplicates in 30 minutes**, at 0.5 messages/s across three tenants, including large UTF-8 bodies and 2 MiB attachments. A separate million-row history test exercised indexed recovery and metrics. These results describe the local sink, not Internet delivery capacity. See [verification scope](docs/verification.md).

Internet delivery requires a static public IP, outgoing TCP/25, PTR/A records, sender domains, SPF/DKIM/DMARC, SMTP TLS, incoming DSNs, and IP reputation management. The application and mail service can use related subdomains; a working website alone does not satisfy these requirements.

## Documentation

| Guide | Contents |
|---|---|
| [Architecture](docs/architecture.md) | Components, state machine, fencing, queues, failure modes |
| [Integration](docs/integration.md) | REST/SMTP/SDK, tenants, templates, webhooks, business outbox |
| [Operations](docs/operations.md) | Deployment, DNS, TLS, monitoring, retention, upgrades |
| [Recovery](docs/recovery.md) | Holds, evidence, backups, PITR, safe release |
| [Resources](docs/resources.md) | Memory/CPU limits, measured profile, disk sizing |
| [Security](docs/security.md) | Trust boundaries, secrets, dependencies, infrastructure gates |
| [Verification](docs/verification.md) | Reproducible checks and validated/unvalidated scenarios |
| [OpenAPI](api/openapi.yaml) | HTTP contract and byte limits |

## Contributing and license

See [CONTRIBUTING.md](CONTRIBUTING.md) for checks and [SECURITY.md](SECURITY.md) for responsible reporting.

Copyright © 2026 **Альмар 06**. selfmail's code, documentation, and original artwork are distributed under [MIT](LICENSE). Dependencies and container software use their own licenses; see [third-party notices](THIRD_PARTY_NOTICES.md).
