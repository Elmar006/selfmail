# Measuring throughput

`cmd/benchmark` measures a finite, concurrent workload against the development installation. `cmd/loadtest` is a separate fixed-rate stability soak. Its default two-second interval intentionally offers only **0.5 messages/s**; the historical 900-message, 30-minute run does **not** measure maximum capacity.

## Method and success criteria

The benchmark creates three fresh tenants and sender domains. Their configured tenant rate is 10,000/s to avoid mistaking a low project quota for service capacity; this does not change process budgets, Redis's 20/s destination bucket, CPU/memory ceilings, or Postfix settings. Each request has one recipient, a unique idempotency key, a fixed-size synthetic body, and an optional random-byte binary attachment. Random data avoids favorable TOAST compression of repeated-character fixtures; it is not a generated purchase receipt. Requests cycle through the three priorities. Setup/domain-key generation is excluded from the timed workload.

Clients send continuously without a fixed interval, with at most `-concurrency` requests in flight. This is a **closed-loop** measurement: request latency affects offered load. Latency starts at each request, not at hypothetical arrival times in an open-loop schedule. It does not establish a latency SLA under arbitrary arrivals or the absolute maximum capacity.

The output separates:

- `api_accepted_per_second`: successful API requests divided by time from the first request to the last response, including unsuccessful responses in that window.
- `confirmed_end_to_end_per_second`: delivered messages divided by time from the first request to completion of SQL delivery confirmation, including the queue-drain period. One-second polling makes this conservative.
- Successful API latency percentiles, and SQL confirmation latency measured from message `created_at` to `updated_at` in the final delivered state. SQL timestamps describe transaction/reconciliation observations, not the exact instant of remote SMTP acknowledgement.
- HTTP errors, SQL outcomes, missing sink copies, and extra copies, independently of accepted responses.

The command exits successfully only when every requested message has a successful API response, exists in SQL as `delivered`, and has exactly one sink copy. It never retries failed/ambiguous requests to conceal errors. SQL verification covers all messages belonging to the new tenants, including an acceptance whose HTTP response may have been lost. Copy verification is an observation at the end of the run, not a promise against every future duplicate.

## Reproduce with stock Postfix pacing

Use a disposable development Compose project. Mail is routed to `[sink]:1025`; the tool refuses production mode and API/sink URLs other than the Compose names below. Do not run benchmarks alongside user traffic.

```sh
sh scripts/init-local.sh
docker compose up -d --build --wait
docker compose --profile test build tests
docker compose exec -T postfix postconf \
  relayhost smtp_destination_rate_delay smtp_destination_concurrency_limit

docker compose --profile test run --rm --no-deps \
  -e ACCEPTANCE_API_URL=http://api:8080 -e ACCEPTANCE_SINK_URL=http://sink:8025 \
  tests go run ./cmd/benchmark -count 120 -concurrency 2 -timeout 5m
```

The default API admits **two simultaneous heavy operations** and returns temporary `503` responses above that budget. Increasing client concurrency without backpressure is an overload probe, not a successful capacity result. The default Go SDK does not automatically retry; applications must honor retry/backoff guidance and reuse the same idempotency key. See [integration](integration.md).

Stock Postfix uses `smtp_destination_rate_delay=1s`, destination concurrency 5, and recipient grouping 50. All development destinations share one relay. This pacing can dominate end-to-end delivery even while the API accepts messages much faster. Production delivery goes to recipient MX servers and must be measured with their actual policies and your IP reputation.

## Isolate local pipeline throughput

For a separate **development-only** run, remove only the local relay pacing after the previous run has drained. Keep all CPU/memory budgets and other safeguards. This profile is explicitly different from stock delivery pacing.

```sh
# Check development mode and the sink relay before changing the test container.
test "$(docker compose exec -T postfix printenv APP_ENV | tr -d '\r')" = development
test "$(docker compose exec -T postfix postconf -h relayhost | tr -d '\r')" = '[sink]:1025'
docker compose exec -T postfix postconf -e smtp_destination_rate_delay=0s
docker compose exec -T postfix postfix reload

docker compose --profile test run --rm --no-deps \
  -e ACCEPTANCE_API_URL=http://api:8080 -e ACCEPTANCE_SINK_URL=http://sink:8025 \
  tests go run ./cmd/benchmark -count 600 -concurrency 2 -timeout 5m

# Restore the stock pacing before other tests or operation.
docker compose exec -T postfix postconf -e smtp_destination_rate_delay=1s
docker compose exec -T postfix postfix reload
```

Changing `-recipient-domains` varies the Redis destination buckets; it does not create multiple SMTP relays. Record this setting rather than implying that a many-domain profile applies to a single recipient domain. For larger attachment work, use `-attachment-bytes 2097152` and reduce the count. The command bounds concurrency, message count, payload sizes and per-tenant retained bytes. Sink copy counters retain up to 10,000 distinct IDs; the tool checks remaining capacity before sending. Use a fresh isolated sink when that capacity would be exceeded.

## Report a result

Record the source revision, host/VM resources, service CPU/memory quotas, concurrent host workloads, request count, payload bytes, client concurrency, tenant/destination limits, exact Postfix settings, API errors, both rates, latency percentiles, drained outcomes, copy counts, and resource observations. Short bursts, soak tests, overload probes and Internet delivery tests answer different questions. Do not extrapolate a short local rate into guaranteed daily delivery or a VPS SLA. Measured runs and their scope are recorded in [verification](verification.md) and [resources](resources.md).
