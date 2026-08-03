# barrage

[![Go version](https://img.shields.io/github/go-mod/go-version/codetesla51/barrage)](https://github.com/codetesla51/barrage)
[![CI](https://github.com/codetesla51/barrage/actions/workflows/build.yml/badge.svg)](https://github.com/codetesla51/barrage/actions/workflows/build.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/codetesla51/barrage)](https://goreportcard.com/report/github.com/codetesla51/barrage)
[![Release](https://img.shields.io/github/v/release/codetesla51/barrage)](https://github.com/codetesla51/barrage/releases)

Barrage is a load testing tool built to answer one question: **when an
application slows down, is the cause the application itself, or the database
and cache underneath it?**

It drives concurrent HTTP, database, and Redis load on a single clock, records
every layer's latencies into the same time buckets, and compares them bucket by
bucket. Instead of one latency curve that hides where the time went, you get a
correlated view of the API, the database, and the cache — and a report that
flags exactly which bucket a storage layer spiked in, and whether the
application was affected or not.

Here is what a run looks like:

```
$ barrage run -c config.yaml

barrage v0.1.0
duration 15s · bucket 1s · concurrency 10 · ramp 3s
rates    http 10/s · db 5/s · redis 20/s

RUNNER  REQUESTS  SUCCESS  RATE    MEAN     P50      P95       P99       MAX      STATUS
http    135       100.0%   9.5/s   927µs    509µs    2.5ms     4.4ms     6.6ms    200×135
db      67        100.0%   4.5/s   12.9ms   5.5ms    69.0ms    136.2ms   136.2ms
redis   269       100.0%   17.9/s  797µs    396µs    2.3ms     3.5ms     10.6ms

correlated spikes
TIME      RUNNER  HTTP_P99  STORAGE_P99   NOTE
20:52:22  db      <100ms    136.2ms       db-only
```

<!-- TODO: replace with an actual report screenshot -->
![Barrage HTML Report](./docs/report-screenshot.png)

*load test of an api with production backend patterns - distributed rate limiting, multi-layer caching, high availability.*

## Getting started (30 seconds)

```sh
go install github.com/codetesla51/barrage/cmd@latest
barrage run                      # runs config.yaml, writes report.html
barrage run -o                   # ...and opens the report in your browser
barrage run --no-report --json results.json   # for CI, no browser needed
```

Point `config.yaml` at your targets first (see [Configuration](#configuration)).
The report is self-contained: Chart.js loads from a CDN, but all run data is
embedded in the page.

## What Barrage does

### Find where latency comes from

**Three runners, one clock.** HTTP (via [vegeta](https://github.com/tsenart/vegeta)),
DB, and Redis run concurrently and record their latencies into the same time
buckets, so results are directly comparable — that is the core of the tool.

**Spike correlation.** Every bucket where a storage runner's (DB or Redis) P99
crossed its threshold is flagged. Two outcomes:

- **correlated** — HTTP and the storage runner both crossed their thresholds,
  so the latency jumped together. A verdict names the bottleneck (`HTTP`, `DB`,
  or `Redis`, `EVEN` if they match).
- **masked** — the storage runner spiked while HTTP stayed under its threshold.
  This surfaces a storage bottleneck that does not yet back up the application.

HTTP-only buckets are deliberately not flagged: a slow endpoint that leaves the
data stores idle is an application problem, not a storage problem.

### Generate realistic load

- **Weighted mixed queries.** One query is picked per request, weighted, so a
  config can mix reads and writes the way real traffic does.
- **Read/write routing.** Each DB query's `type` field is authoritative
  (`read` runs through `Query`, `write` through `Exec`); untyped queries fall
  back to a heuristic on the SQL text.
- **Rate ramp.** Rates grow linearly from 0 to full over a configurable window,
  emulating a gradual warm-up instead of hitting the target at full force from
  the first request.
- **Real parallelism.** Requests are submitted to a worker pool, so `rate` is
  not a serial request stream. See [Configuration](#configuration) for how
  `rate`, `concurrency`, and `ramp` interact.

### Export results

- **HTML report** — run summary, correlated spikes, and a full-run latency
  timeline, self-contained in one file.
- **JSON export** — the same data in machine-readable form, for dashboards and
  CI comparison.
- **CLI tables** — aligned per-runner summary and per-bucket tables in the
  terminal.

## Why not k6, Vegeta, JMeter, or Locust?

Those tools excel at **generating** load. Barrage is built around
**interpreting** it:

- **k6, JMeter, Locust** — script complex user journeys and report rich
  metrics, but each generator runs independently. Correlating an API slowdown
  with the database or cache behind it is left to you.
- **Vegeta** — a focused, high-performance HTTP load generator. It tells you
  how the endpoint behaved, not why.

Barrage is narrower on purpose: it generates HTTP, database, and Redis load in
one process and aligns every layer onto one timeline. Where a typical load
tester reports a single latency curve, Barrage reports three — and tells you
which layer spiked.

| Feature | Barrage | Typical Load Tester |
|---|---|---|
| HTTP load | ✅ | ✅ |
| DB load | ✅ | Usually no |
| Redis load | ✅ | Usually no |
| Correlate latency | ✅ | ❌ |
| HTML report | ✅ | Varies |

## When to use Barrage

- Investigating why an API is slow (is it the app, the database, or the cache?).
- Testing database bottlenecks: missing indexes, connection-pool limits,
  query plans.
- Comparing infrastructure changes before/after a migration or tuning pass.
- Performance regression testing across releases.

## When Barrage is not the right tool

- **Browser/E2E testing** — no browser, no DOM, no UI assertions.
- **Complex user journeys** — it fires configured requests; it does not script
  login flows or multi-step state.
- **WebSocket / streaming traffic**.
- **Distributed cloud load** — it runs from one process; scale vertically, not
  across regions.

## Install

```sh
go install github.com/codetesla51/barrage/cmd@latest
```

Or build from source:

```sh
git clone https://github.com/codetesla51/barrage && cd barrage
go build -o barrage ./cmd
```

Requires Go 1.25 or later. The DB runner supports **Postgres**, **MySQL**, and
**SQLite** out of the box; because it sits on `database/sql`, any other driver
can be linked in by adding a blank import and registering its name. HTTP-only
runs require no backing services.

## Configuration

The default config file is `config.yaml`. Any subset of `http`, `db`, and
`redis` is valid; at least one section is required. Durations use Go's
`time.ParseDuration` format (`10s`, `1m30s`, `500ms`). Unknown keys are
rejected so a typo fails loudly instead of being silently ignored.

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
    driver: postgres # postgres | mysql | sqlite (aliases accepted, e.g. postgresql, sqlite3)
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
- `concurrency` for HTTP maps to vegeta's `MaxWorkers`; unset (0) lets vegeta
  scale workers on its own. For DB and Redis it is the pool size; 0 selects the
  default of 10 workers. The run header reports which mode is in effect.
- `ramp` schedules hits so the rate grows linearly from 0 to full across the
  window (a 3s ramp at 2000/s fires roughly 3000 requests during the ramp, then
  holds 2000/s). With no `ramp`, the full rate applies from the first request.
- `type` on each DB query is authoritative for read/write routing: `read` runs
  through `Query`, `write` through `Exec`. If omitted, routing falls back to
  detecting the SQL text (SELECT / SHOW / EXPLAIN / WITH → read; any query
  containing a `RETURNING` clause → write). Prefer an explicit `type`; detection
  is a heuristic.
- `args` (optional) is scoped per query, not global: it binds parameters for
  that query only. Omit it entirely when the query has no placeholders.
- `driver` selects the database backend: `postgres`, `mysql`, or `sqlite`
  (pure-Go, no CGO). Common aliases are normalized (`postgresql`/`pg` →
  `postgres`, `sqlite3` → `sqlite`), and an unsupported name fails loudly with
  the list of compiled-in drivers. Each driver expects its own connection DSN:
  Postgres `postgres://...`, MySQL `user:pass@tcp(host:3306)/db`, SQLite a file
  path such as `/tmp/test.db`.

## CLI

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

<!-- TODO: architecture diagram -->
![Architecture](./docs/architecture.png)

### Spike correlation

1. All runners' buckets are aligned by their unix start time
   (`HTTPBucket.Start.Unix()` == storage `Bucket.Start`).
2. Each storage runner — DB and Redis — is checked independently against the
   HTTP run. A bucket is flagged when the storage runner's **P99 exceeds its
   threshold**, and the spike is either **correlated** (HTTP also crossed
   `http-threshold`, labeled with a bottleneck verdict) or **masked** (storage
   spiked while HTTP stayed under its own).
3. Masked spikes are still reported so a storage bottleneck that does not yet
   back up the application is surfaced. The CLI marks them `db-only` /
   `redis-only`, the HTML report tags them `DB (masked)` / `Redis (masked)`,
   and the JSON export sets `masked: true`. A bucket where both DB and Redis
   spike produces two rows.

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

<!-- TODO: screenshot of the latency timeline chart -->
![Latency timeline](./docs/timeline-chart.png)

<!-- TODO: screenshot of the correlated spikes table -->
![Correlated spikes table](./docs/spikes-table.png)

The JSON export mirrors this structure: `generated_at`, `duration`, `ramp`,
`concurrency`, per-runner metrics (latencies in milliseconds), correlated spikes
(each with `runner`, `http_p99_ms`, `storage_p99_ms`, and `masked`), and the
timeline. In the timeline's `p99_ms` series, `-1` marks a bucket where that
runner had no request (e.g. before the ramp produced its first hit); the report
chart renders these as gaps, not as a latency of -1ms.

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
