# Security and trust boundaries

selfmail assumes trusted server operators and isolated infrastructure processes, with untrusted tenant HTTP/SMTP inputs and untrusted network responses. It is a community project without a security/compliance certification.

## Public interfaces and tenant isolation

- API keys are random secrets; SQL stores digests, scopes, tenant identity, and revocation.
- Authorization is rechecked inside the enqueue transaction. SMTP sessions do not retain send permission indefinitely after revoke.
- Tenant transactions establish RLS context; cross-project resource lookup does not expose another project's data.
- Sender domains require ownership TXT and exact DKIM public-key verification in production.
- Postfix relays to arbitrary destinations only from the trusted worker address. External access is restricted to the bounce domain/null-sender DSN path and constrained recipient format.
- Header/envelope validation, MIME/line-size checks, shared work permits, template budgets, body limits, storage/backlog quotas, and rate limits bound expensive tenant work.
- Webhook production targets require HTTPS, resolved-address policy checks, and redirect rejection. A receiver inside another private Docker network needs a permitted public HTTPS route or polling; production's private-target bypass cannot be enabled.

RLS does not make the worker/admin role untrusted. Compromising that role or the Docker host compromises service administration and data. Do not expose operator CLI or database superuser credentials as tenant endpoints.

## Secrets and encryption

`MASTER_KEY` is 32 random bytes encoded as base64. It encrypts DKIM/webhook secrets and independent journal/control evidence. Startup validates it against the SQL key guard and journal identity; a wrong global key stops operation rather than dead-lettering every project's secrets.

Bodies/attachments are stored in SQL without application-level encryption. pgBackRest encrypts its backup repository, and Restic encrypts evidence/configuration snapshots using a separate passphrase. Configure host/storage encryption and transport security according to your requirements. Internal DB/AMQP/Redis links in the supplied single-host topology are plaintext private-network links; distributed use needs TLS.

Keep `.env`, passphrases, TLS private keys, backup SSH keys, and generated diagnostic output outside Git. Restrict host permissions and Windows ACLs. Backup/export initializers copy required files into UID-specific private Docker credential volumes; runtime TLS read permissions still need explicit provisioning for UID 10001. Never make the entire secrets directory public to solve a permissions error.

Store the Restic recovery password independently of the host. Rotate API keys through issue/revoke. Rotating MASTER_KEY requires re-encrypting the SQL keys **and the complete journal/control evidence** through a separately reviewed process; replacing the environment value alone is unsupported. There is no public DKIM/MASTER_KEY rotation workflow in this release.

## Logs, callbacks, and retention

Address/diagnostic data can appear in Postfix logs, encrypted evidence, operator audit, and callbacks. Metrics deliberately avoid address/tenant labels. Protect log volumes, the monitoring endpoint, receiver secrets, and backups.

Verify HMAC against original webhook bytes, enforce timestamp tolerance, cap receiver body size, and deduplicate event IDs transactionally. A webhook signature does not establish user consent or authorize an unrelated business action. DSNs use attempt/recipient correlation; remote mail feedback remains untrusted ecosystem input.

Default retention is a technical policy, not a legal compliance conclusion. Protected work can extend retention. The append-only journal retains correlation beyond SQL's body/metadata horizon and requires capacity/retention planning. Publish a policy appropriate to the data you send.

## Dependency and supply-chain policy

CI runs the pinned Go vulnerability scanner and blocks callable/imported source findings. Trivy blocks **fixable** HIGH/CRITICAL findings in application/Postfix/PostgreSQL images. Bases are digest-pinned, actions commit-pinned, modules checksum-verified, and release image jobs attach SBOM and provenance. Transitive apt packages still come from upstream repositories; a fully versioned apt snapshot/bit-reproducible OS build is not implemented.

On the October 8, 2026 local scan:

| Image/code | Result |
|---|---|
| selfmail Go source | 0 callable findings; 0 imported-package findings; 1 unused-module advisory |
| Application Alpine runtime | 0 HIGH/CRITICAL findings |
| Postfix Debian runtime | 43 package findings / 8 unique advisories; no fixed stable-package version available in that scan |
| Backup-capable PostgreSQL Debian runtime | 69 package findings / 26 unique advisories; no fixed stable-package version available in that scan |

The PostgreSQL image rebuilds pinned gosu source using the current Go toolchain to remove findings from the older bundled Go standard library. The module-only OpenPGP advisory concerns a package not imported by selfmail; it is not counted as a callable source fix.

Vendor-unfixed OS findings remain a review/patch gate, not a declaration that images are vulnerability-free. Examples of upstream tracking are [util-linux CVE-2026-76642](https://security-tracker.debian.org/tracker/CVE-2026-76642), [Perl Archive::Tar CVE-2026-9538](https://security-tracker.debian.org/tracker/CVE-2026-9538), and [libacl CVE-2026-54369](https://security-tracker.debian.org/tracker/CVE-2026-54369). Evaluate reachable behavior, host/container privileges, and vendor updates; do not silently ignore all scan severities. Redis/RabbitMQ/observability/Restic base-image inventory needs the operator's broader deployment scan policy too.

These counts are a dated scan snapshot. Repeat after dependency/image changes or new advisories, update digests through reviewed checks, and rescan the actual deployed image. A passing `--ignore-unfixed` gate only proves its stated fixable-finding policy.

## Production acceptance gates

Before real traffic validate DNS ownership, SPF/DKIM/DMARC alignment, SMTP/API TLS and renewal, public port restrictions, relay rejection, bounce/receiver endpoints, IP reputation, independent backups, fresh restore/reconciliation, alert delivery, capacity growth, and your application's outbox/idempotency behavior.

One host and one node per stateful component cannot tolerate loss of that host without recovery downtime. Managed restore must retain current independent evidence; stale/missing evidence limits what can be proven. Multi-node failover, RPO/RTO, remote repository availability, and public inbox placement are not established by local tests. [Recovery](recovery.md) documents the controlled protocol.

Report actual vulnerabilities according to [SECURITY.md](../SECURITY.md). For licensing of external components, see [third-party notices](../THIRD_PARTY_NOTICES.md); selfmail's MIT license does not change Redis/Postfix/OS terms.
