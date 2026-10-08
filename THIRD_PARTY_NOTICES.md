# Third-party software

selfmail's own code, documentation, and original artwork use [MIT](LICENSE). This does not relicense connected software or container contents.

| Component | Upstream license reference |
|---|---|
| Go | [LICENSE](https://go.dev/LICENSE) |
| PostgreSQL | [PostgreSQL license](https://www.postgresql.org/about/licence/) |
| RabbitMQ | [Licensing](https://www.rabbitmq.com/docs/licensing) |
| Redis 8 | [Redis licenses](https://redis.io/legal/licenses/) — AGPLv3 alongside RSALv2 and SSPLv1 |
| Postfix | [License](https://www.postfix.org/LICENSE.html) |
| pgBackRest | [MIT](https://github.com/pgbackrest/pgbackrest/blob/main/LICENSE) |
| Restic | [BSD-2-Clause](https://github.com/restic/restic/blob/master/LICENSE) |
| Prometheus / Alertmanager | [Prometheus](https://github.com/prometheus/prometheus/blob/main/LICENSE), [Alertmanager](https://github.com/prometheus/alertmanager/blob/main/LICENSE) |
| Caddy | [Apache-2.0](https://github.com/caddyserver/caddy/blob/master/LICENSE) |
| gosu | [License](https://github.com/tianon/gosu/blob/master/LICENSE) |

Module versions are in `go.mod`/`go.sum`; each module retains its license. Base images contain OS packages with additional licenses. Consult package metadata and release SBOMs when redistributing images.

Redis 8's AGPL option permits an open-source deployment. Choosing a Redis license and complying with modification/redistribution obligations are separate from selfmail's MIT grant; review the upstream terms for your distribution model.
