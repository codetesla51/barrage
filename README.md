# barrage

Barrage is a single-binary load testing tool that drives concurrent traffic
against **HTTP**, **Postgres**, and **Redis** targets, aligns each runner's
latencies into shared time buckets, and attributes latency spikes to the layer
that caused them.

It answers a question that most load testing tools leave open: when an
application slows down, is the cause in the application layer itself or in the
data store underneath it?

```
$ barrage run -c config.yaml
...
=== Correlated Spikes ===
  17:20:29  http_p99=162.6ms  db_p99=3.9s       <- both spiked in the same bucket
  17:21:05  db_p99=2.1s  (db-only: http stayed under 100ms)
  17:22:12  http_p99=88ms  redis_p99=401ms      <- redis flagged like db
```

## Capabilities

- **Three runners, one clock.** HTTP (via [vegeta](https://github.com/tsenart/vegeta)),
  DB, and Redis run concurrently and record their latencies into the same time
  buckets, so results are directly comparable.
- **Spike correlation.** Both storage runners — DB and Redis — are checked
  against the HTTP run. Any bucket where a storage runner's P99 crossed its
  threshold is flagged, labeled either **dual** (HTTP and the storage runner
  both crossed their thresholds — a correlated latency jump) or **masked**
  (storage spiked while HTTP stayed under its threshold, meaning the spike
  cannot be attributed to the application layer). A verdict
  (HTTP / DB / Redis / EVEN) is assigned per dual spike. HTTP-only buckets are
  deliberately not flagged: a slow endpoint that leaves the data stores idle is
  an application problem, not a storage problem.
- **Parallel execution.** DB and Redis requests run on a worker pool rather
  than sequentially. `rate` controls how many requests to send; `concurrency`
  controls how many may be in flight. This distinguishes a genuine load test
  from a serial request stream.
- **Rate ramp.** Rates increase linearly from 0 to the configured rate over a
  configurable window, emulating a gradual warm-up instead of applying the full
  rate from the first request.
- **Read/write routing.** A DB config may mix reads and writes. Each query's
  `type` field is authoritative (`read` → `Query`, `write` → `Exec`); when
  unset, routing falls back to a heuristic on the SQL text.
- **Two output formats.** A self-contained HTML report (run summary, correlated
  spikes, full-run latency timeline) and a machine-readable JSON export for
  dashboards and cross-run comparison.

## Requirements

- Go 1.25 or later.
- A Postgres (or any `database/sql` driver) and/or Redis target. HTTP-only runs
  require no backing services.

## Install

```sh
go install github.com/codetesla51/barrage/cmd@latest
```

Or build from source:

```sh
git clone <this-repo> && cd barrage
go build -o barrage ./cmd
```

## Quick start

1. Point `config.yaml` at your targets (see Configuration).
2. Run:

```sh
barrage run
```

3. Open the report:

```sh
barrage run -o          # runs, then opens report.html in your browser
```

The report is self-contained: Chart.js is loaded from a CDN, but all run data
is embedded in the page, including the JSON export.

## Configuration

The default config file is `config.yaml`. Any subset of `http`, `db`, and
`redis` is valid; at least one section is required. Durations use Go's
`time.ParseDuration` format (`10s`, `1m30s`, `500ms`). Unknown keys are
rejected so that a typo fails loudly rather than being silently ignored.

```yaml
duration: 15s        # how long to run
bucket_width: 1s     # correlation/timeline bucket size
ramp: 3s             # ramp rate from 0 to full over this window (0 = no ramp)
concurrency: 10      # in-flight requests per runner (db/redis pools, http workers)

http:
  rate: 10                        # requests per second
  target:
    method: POST                  # default GET
    url: http://localhost:8080/api/orders
    body: '{"customer": 42}'      # optional request body
    header:                       # optional; value is a string or list
      content-type: [application/json]
      authorization: [Bearer some-token]

db:
  rate: 5            # queries per second (total, across the weighted list)
  target:
    driver: postgres
    conn: postgres://user:pass@localhost:5432/mydb?sslmode=disable
    queries:          # one query is picked per request, weighted
      - query: SELECT count(*) FROM orders
        weight: 20
        type: read
      - query: SELECT customer, amount FROM orders LIMIT 10
        weight: 20
        type: read
      - query: SELECT amount FROM orders WHERE id = 1
        weight: 15
        type: read
      - query: INSERT INTO orders (customer, amount) VALUES ('load', 1)
        weight: 25
        type: write
      - query: UPDATE orders SET amount = amount + 1 WHERE id = 1
        weight: 20
        type: write

redis:
  rate: 20            # commands per second
  target:
    addr: localhost:6379
    password: ""       # optional
    db: 0
    queries:           # one command is picked per request, weighted
      - query: PING
        weight: 1
```

### Field reference

- `rate` is the *target* rate. If `concurrency` is too small to keep up, the
  pool backs up and throughput settles below target. This is intentional: a
  real load test should expose the target's limits rather than silently
  serializing requests.
- `ramp` schedules hits so the rate grows linearly from 0 to full across the
  window (a 3s ramp at 2000/s fires roughly 3000 requests during the ramp, then
  holds 2000/s). With no `ramp`, the full rate applies from the first request.
- `concurrency` for HTTP maps to vegeta's `MaxWorkers`. When left unset (0),
  vegeta scales workers on its own. For DB and Redis, 0 selects the default
  pool of 10 workers — sufficient for typical local rates while still running
  several requests in parallel. The CLI banner reports which mode is in effect.
- `type` on each DB query is authoritative for read/write routing: `read` runs
  through `Query`, `write` through `Exec`. If omitted, routing falls back to
  detection of the SQL text (SELECT / SHOW / EXPLAIN / WITH → read; any query
  containing a `RETURNING` clause → write). An explicit `type` is preferred;
  detection is a heuristic.
- `args` (optional) is scoped per query, not global: bind the parameters for
  that query only. Omit it entirely when the query has no placeholders.

## Command line

```
$ barrage run --help

Flags:
  -b, --bucket-width duration     override the bucket width from the config
      --concurrency int           worker count for the db/redis pools and http attackers
  -c, --config string             path to the config file (default "config.yaml")
      --db-threshold duration     DB spike threshold for correlation (default 100ms)
  -d, --duration duration         override the run duration from the config
      --http-threshold duration   HTTP spike threshold for correlation (default 100ms)
      --json string               also write a JSON summary of the run to this path
      --no-report                 skip writing the HTML report
  -o, --open                      open the report in a browser after the run
      --ramp duration             ramp the rate from 0 up to full over this duration
      --redis-threshold duration  Redis spike threshold for correlation (default 100ms)
      --report string             path for the HTML report (default "report.html")
  -v, --verbose                   print per-bucket detail
```

Examples:

```sh
barrage run -c staging.yaml --duration 1m --ramp 10s --concurrency 50
barrage run --http-threshold 150ms --db-threshold 250ms --redis-threshold 80ms  # adjust spike thresholds
barrage run --no-report --json results.json                 # for CI pipelines
barrage version                                            # print the version
```

Every `--` flag overrides its config counterpart.

## How it works

### Runners

| Runner | Engine | Parallelism |
|---|---|---|
| HTTP | vegeta attacker | vegeta workers (bounded by `concurrency`) |
| DB | `database/sql` + [pond](https://github.com/alitto/pond) worker pool | `concurrency` workers |
| Redis | go-redis client + pond worker pool | `concurrency` workers |

The DB and Redis runners pace requests at `rate` per second, submitting each to
a pool capped at `concurrency` workers. Results carry the submission timestamp,
so buckets reflect when load was generated, not when responses completed.

### Spike correlation

1. All runners' buckets are aligned by their unix start time
   (`HTTPBucket.Start.Unix()` == storage `Bucket.Start`).
2. Each storage runner — DB and Redis — is checked independently against the
   HTTP run. A bucket is a spike if the storage runner's **P99 exceeds its
   threshold**. Buckets fall into two categories:
   - **dual** — HTTP P99 also crossed `http-threshold`; labeled with its
     bottleneck: `HTTP` if http_p99 > storage_p99, `DB` or `Redis` if the
     reverse, `EVEN` if they match.
   - **masked** — the storage runner crossed its threshold while HTTP stayed
     under its own. These are reported so a storage bottleneck that does not
     visibly back up the application is still surfaced. The CLI prints them
     with a `(db-only: ...)` / `(redis-only: ...)` marker; the HTML report tags
     them `DB (masked)` / `Redis (masked)`; the JSON export sets `masked: true`.
     A bucket where both DB and Redis spike produces two rows.

Thresholds default to 100ms each and apply per runner (`--http-threshold`,
`--db-threshold`, `--redis-threshold`).

### Report

`report.html` contains:

- **Run summary** — requests, success %, P50/P95/P99/max/mean, rate, throughput,
  and the HTTP status-code histogram for each runner.
- **Correlated spikes** — a table of flagged buckets (runner, per-bucket P99
  values, bottleneck verdict) and an overall "bottleneck lean" readout.
- **Latency timeline** — every runner's per-bucket P99 on a shared x-axis so
  storage and HTTP latency can be compared directly.
- **Export JSON button** — in the top bar; downloads the run as the same JSON
  the `--json` flag writes, so a report opened in a browser can still feed a
  dashboard or a CI comparison.

The JSON export mirrors this structure: `generated_at`, `duration`, `ramp`,
`concurrency`, per-runner metrics (latencies in milliseconds), correlated spikes
(each with `runner`, `http_p99_ms`, `storage_p99_ms`, and `masked`), and the
timeline.

## Demo stack

Two helpers for exercising a local reference backend:

- `cmd/demoserver` — a minimal HTTP application (`:8080/api/orders`) with no
  artificial latency. Run it, point the `http` target at it, and load it.
- `cmd/seeddb` — bulk-seeds an `orders` table (COPY, 100k-row chunks) so DB
  queries have real work to do:

```sh
go run ./cmd/seeddb -conn "postgres://user:pass@localhost:5432/mydb?sslmode=disable" -n 1000000
```

## Development

```sh
go test ./...     # unit + integration (miniredis for Redis, httptest for HTTP)
go vet ./...
```

Tests cover the ramp schedule, pool pacing, read/write detection, config
parsing (including unknown-key rejection), correlation, report rendering, and
JSON export. `report.html` is a build artifact and is intentionally not
committed.
