# barrage

Barrage is a load testing tool that shows which layer is slow: your app, your database, or your cache.

It fires HTTP, database, and Redis load on a single clock, buckets every layer's latencies onto the same timeline, and flags the buckets where a storage spike lines up with an app slowdown. Instead of one latency curve that hides where the time went, you get three correlated curves and a verdict naming the bottleneck.

## Why it exists

When an API slows down under load, the usual question is whether the app itself is the problem or the database and cache underneath it. Single-layer load testers can tell you the endpoint got slow but leave the correlation to you. Barrage runs all three layers from one process so the answer comes out of the run itself.

## Why not k6, Vegeta, JMeter, or Locust

k6, JMeter, and Locust script rich user journeys and report detailed metrics, but each generator runs independently. Correlating an API slowdown with the database behind it is manual work. Vegeta is a fast HTTP generator. It tells you how the endpoint behaved, not why.

Barrage is narrower on purpose: HTTP, database, and Redis load from one binary, aligned onto one timeline, with spike correlation built in. If you need browser testing, WebSocket traffic, or distributed cloud load, use one of the tools above. Barrage runs from a single process and does none of those.

## Features

- HTTP, Postgres/MySQL/SQLite, and Redis runners on one clock.
- Weighted mixed queries per request, with explicit read/write routing.
- Sequential user-journey scenarios with per-user variable extraction (`extract: $.token`, `{{token}}` interpolation).
- Linear rate ramp, worker-pool parallelism, per-bucket P50/P99 timelines.
- Correlated vs masked spike detection with a bottleneck verdict per bucket.
- Capacity finder: first sustained strain point mapped to active users.
- HTML report, JSON export, `compare` subcommand with CI exit codes.
- Live progress line every 5s, CLI summary tables.

## Quick start

Install the binary. This puts `barrage` on your PATH.

```sh
go install github.com/codetesla51/barrage/cmd/barrage@latest
```

Point `config.yaml` at your targets. This example drives HTTP and Postgres for 15 seconds, ramping up over 3 seconds.

```yaml
duration: 15s
bucket_width: 1s
ramp: 3s
concurrency: 10

http:
  rate: 10
  target:
    method: GET
    url: http://localhost:8080/api/todos

db:
  rate: 5
  target:
    driver: postgres
    conn: postgres://user:pass@localhost:5432/mydb?sslmode=disable
    queries:
      - query: SELECT count(*) FROM orders
        weight: 20
        type: read
```

Run it. This executes the config and writes `report.html` in the working directory.

```sh
barrage run -c config.yaml
```

A typical summary looks like this. The `correlated spikes` section is the point of the tool: it names which buckets spiked and whether the app felt it.

```
RUNNER  REQUESTS  SUCCESS  RATE    MEAN     P50      P95       P99
http    135       100.0%   9.5/s   927us    509us    2.5ms     4.4ms
db      67        100.0%   4.5/s   12.9ms   5.5ms    69.0ms    136.2ms

correlated spikes
TIME      RUNNER  HTTP_P99  STORAGE_P99   NOTE
20:52:22  db      <100ms    136.2ms       db-only
```

Compare two runs for regressions. This diffs a baseline JSON export against a current one and exits non-zero if a runner regressed past the budget, which is what CI gates on.

```sh
barrage run --no-report --json base.json
# ... change something ...
barrage run --no-report --json new.json
barrage compare --baseline base.json --current new.json --fail-on 100ms
```

Ready-made profiles live in `examples/`. `light.yaml` is a gentle baseline, `heavy.yaml` is a stress profile, and the `scenario-*.yaml` files show single and weighted user journeys against the demo server in `cmd/demoserver`.

## Notes

> Note: `scenario:` replaces the `http:` runner. You cannot combine them in one config. To mix plain hits with flows, model the plain hit as a one-step scenario.

> Note: `bucket_width` accepts sub-second values (e.g. `500ms`). Buckets align on wall-clock time so all runners stay comparable.

> Warning: Barrage runs from one process. Scale vertically, not across regions. It does no browser, DOM, WebSocket, or streaming traffic.

> Warning: DB `type: read/write` is authoritative for query routing. Omit it and routing falls back to guessing from the SQL text, which misroutes edge cases like `RETURNING` clauses. Set it explicitly.

## Reference

Full flag and field documentation lives in `docs/`. Start with `barrage run --help` for CLI flags.

```sh
barrage run --http-threshold 150ms --db-threshold 250ms --redis-threshold 80ms
barrage run -c staging.yaml --duration 1m --ramp 10s --concurrency 50
```

## Development

```sh
go test ./...
go vet ./...
```

The report template (`templates/report.html`) is embedded in the binary. A template file at `templates/report.html` alongside the binary overrides the embedded one.
