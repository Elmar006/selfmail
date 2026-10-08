# Contributing

Use issues for reproducible bugs and concrete improvements. Include the revision, deployment mode, configuration **without secrets**, expected behavior, and a minimal reproduction. Report vulnerabilities through [SECURITY.md](SECURITY.md).

## Development

```sh
sh scripts/init-local.sh
docker compose up -d --build --wait
docker compose --profile test build tests
docker compose --profile test run --rm tests sh scripts/verify.sh
docker compose --profile test run --rm acceptance
python3 -m pip install PyYAML==6.0.3
python3 scripts/check-api-contract.py
```

Tests use a separate `selfmail_test` database; acceptance creates development tenants and sends to the local sink. Never point these tools at production. [Verification](docs/verification.md) describes drills and resource needs.

## Design expectations

- Keep business policy in `internal/application`, data types/validation in `internal/domain`, and external systems behind interfaces.
- Preserve the PostgreSQL/outbox transaction and durable journal-before-SMTP boundary.
- Treat redelivery and SMTP ambiguity as normal failure cases; a retry cannot bypass a sending fence.
- Keep tenant-scoped SQL inside `TenantTx`; review security-definer functions carefully.
- Add a numbered migration instead of editing an applied migration. Check fresh installation and upgrade.
- Update OpenAPI, SDK, and documentation when a public contract changes.
- Add deterministic regression tests for changed concurrency, security, or failure behavior.
- Keep logs, scan output, secrets, binaries, and test output out of Git; retain required test fixtures.

PRs should explain the trigger, resulting behavior, relevant checks, and remaining operational limits. Run `gofmt`. Branches created by Codex use `codex/`; other contributors may use descriptive names.

## Releases

CI verifies tests, API contracts, alert rules, and fixable HIGH/CRITICAL findings in built images. Reviewed tags matching `v*` additionally publish application/Postfix/PostgreSQL images to GHCR with SBOM and provenance. A passing workflow alone does not establish production readiness.

Contributions use [MIT](LICENSE). Third-party additions retain their notices.
