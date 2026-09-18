# barrage

[![Go version](https://img.shields.io/github/go-mod/go-version/codetesla51/barrage)](https://github.com/codetesla51/barrage)
[![CI](https://github.com/codetesla51/barrage/actions/workflows/build.yml/badge.svg)](https://github.com/codetesla51/barrage/actions/workflows/build.yml)
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

     ________  ________  ________  ________  ________  ________  _______
    |\   __  \|\   __  \|\   __  \|\   __  \|\   __  \|\  ____\|\  ___ \
    \ \  \|\ /\ \  \|\  \ \  \|\  \ \  \|\  \ \  \|\  \ \  \___|\ \   __/|
     \ \   __  \ \   __  \ \   _  _\ \   _  _\ \   __  \ \  \  __\ \  \_|/__
      \ \  \|\  \ \  \ \  \ \  \\  \\ \  \\  \\ \  \ \  \ \  \|\  \ \  \_|\ \
       \ \_______\ \__\ \__\ \__\\ _\\ \__\\ _\\ \__\ \__\ \_______\ \_______\
        \|_______|\|__|\|__\|__|\|__|\|__|\|__|\|__|\|__\|_______|\|_______|

barrage v0.4.0
duration 15s · bucket 1s · concurrency 10 · ramp 3s
rates    http 10/s · db 5/s · redis 20/s

[barrage] done ·  │ http 135 0 err │ db 67 0 err │ redis 269 0 err
RUNNER  REQUESTS  SUCCESS  RATE    MEAN     P50      P95       P99       MAX      STATUS
http    135       100.0%   9.5/s   927µs    509µs    2.5ms     4.4ms     6.6ms    200×135
db      67        100.0%   4.5/s   12.9ms   5.5ms    69.0ms    136.2ms   136.2ms
redis   269       100.0%   17.9/s  797µs    396µs    2.3ms     3.5ms     10.6ms

correlated spikes
TIME      RUNNER  HTTP_P99  STORAGE_P99   NOTE
20:52:22  db      <100ms    136.2ms       db-only
Report written to report.html
```

![Barrage HTML Report](./docs/todo-api-run-1.png)

![Barrage HTML Report — latency timeline](./docs/todo-api-run-2.png)

*A 3-minute heavy run against the TodoAPI stack (Gin + Postgres + Redis): `GET /api/todos` over HTTP at 120/s, a weighted read/write query mix against Postgres at 80/s, and Redis commands at 300/s, with a 60s ramp and concurrency 50. With the app's rate limiter left at production settings it absorbed nearly the whole HTTP burst as 429s — the API stayed flat at ~5ms p50 while the real load landed on the data stores. With the limiter boosted, every request reached the backend and latency dropped straight through to the database: Postgres saturates and drags HTTP P99 to multi-second territory, while Redis stays under 100ms P99. One bottleneck, three correlated curves.*

## Getting started (30 seconds)

```sh
curl -fsSL https://raw.githubusercontent.com/codetesla51/barrage/main/install.sh | bash
barrage run                      # runs config.yaml, writes report.html
barrage run -o                   # ...and opens the report in your browser
barrage run --no-report --json results.json   # for CI, no browser needed
barrage compare --baseline base.json --current new.json   # diff two runs
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
crossed its threshold is flagged — including in **scenario mode**, where the
app-side reference is synthesized from the worst per-bucket journey latency.
Two outcomes:

- **correlated** — HTTP and the storage runner both crossed their thresholds,
  so the latency jumped together. A verdict names the bottleneck (`HTTP`, `DB`,
  or `Redis`, `EVEN` if they match).
- **masked** — the storage runner spiked while HTTP stayed under its threshold.
  This surfaces a storage bottleneck that does not yet back up the application.

HTTP-only buckets are deliberately not flagged: a slow endpoint that leaves the
data stores idle is an application problem, not a storage problem.

### Auto-ramp

The capacity question, answered by experiment instead of guessing. Auto-ramp
searches concurrency for the first level where latency breaks:

```yaml
concurrency: 5 # search start
auto_ramp:
  max_concurrency: 160 # search cap
  step_duration: 10s # burst per level, not the full duration
```

```sh
barrage run -c examples/auto-ramp-pg.yaml
barrage run -c config.yaml --auto-ramp --ramp-max-concurrency 160 --ramp-step-duration 10s
```

How it works: coarse doubling finds the rough zone fast (5→10→20→40…),
then a fine linear fill pins it down between the last ok level and the first
broken one (80→160 becomes 100, 120, 140). Each level runs a short burst;
DB and Redis connections open once and stay warm across levels so early
buckets measure strain, not reconnect cost. Paced runners (`http`/`db`/
`redis`) scale their rate with concurrency so bigger crowds push more load;
scenario load comes from the VUs themselves. A level breaks when any runner's
P99 crosses its threshold or success drops under 95% — the verdict names the
culprit (`CAUSE db`, `http,redis`, …), and the report charts concurrency vs
P99 so you see a cliff or a slope, not just one number. `ramp:` and
`duration:` are ignored while auto-ramp runs (set them `0s`/anything; the
loader still requires the keys). The web UI exposes the same mode as an
auto-ramp toggle in run settings.

### Capacity finder

The story-style report answers "at how many users does my app struggle?" It
maps each time bucket to an estimated active-user count (growing linearly
during ramp, flat after), then scans for the first **sustained** jump — worst
journey P99 exceeding 2× its median for 3+ consecutive buckets. You get either
a strain point (`~N users — where it starts straining`) or a clean `no strain
up to ~N users`, plus the concurrency to re-test at next. The latency chart
plots active users on a second axis with the ramp window shaded.

### Live run progress

Runs are no longer silent. Barrage prints one structured status line every 5s:

```
  00:45/03:00 │ http 3,900 · 0 err │ db 900 · 0 err │ redis 1,350 · 0 err
```

with thousands separators, an mm:ss clock, and semantic colors (amber counts,
red error counts). A totals line lands when the run completes.

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
| HTTP load | Yes | Yes |
| DB load | Yes | Usually no |
| Redis load | Yes | Usually no |
| Scenario user journeys | Yes | Varies |
| Correlate latency | Yes | No |
| Compare runs / CI gate | Yes | No |
| HTML report | Yes | Varies |

## When to use Barrage

- Investigating why an API is slow (is it the app, the database, or the cache?).
- Testing database bottlenecks: missing indexes, connection-pool limits,
  query plans.
- Comparing infrastructure changes before/after a migration or tuning pass.
- Performance regression testing across releases: run a baseline, change the
  code or infra, run again, and `barrage compare` the two JSON exports — with
  `--fail-on`, a regression fails the pipeline.

## When Barrage is not the right tool

- **Browser/E2E testing** — no browser, no DOM, no UI assertions.
- **WebSocket / streaming traffic**.
- **Distributed cloud load** — it runs from one process; scale vertically, not
  across regions.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/codetesla51/barrage/main/install.sh | bash
```

That grabs a prebuilt binary from GitHub releases — no Go toolchain needed.
Pin a version or change the target dir with
`bash -s -- --version v0.4.0 --dir ~/.local/bin`, or build from source with
`--from-source`. See `./install.sh --help` for all flags.

Or build from source (requires Go 1.25 or later):

```sh
git clone https://github.com/codetesla51/barrage && cd barrage
go build -o barrage ./cmd/barrage
# fallback without a checkout:
# go install github.com/codetesla51/barrage/cmd/barrage@latest
```

Requires Go 1.25 or later for source builds only. The DB runner supports **Postgres**, **MySQL**, and
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

# scenario: sequential user journeys (list) (alternative to http/db/redis, can run with db/redis)
# NOTE: scenario replaces the http runner — you cannot combine `scenario:` with
# an `http:` section in the same config. A scenario is your HTTP load: to mix
# plain hits with flows, model the plain hit as a one-step scenario.
scenario:
  - name: login-flow
    weight: 1                         # pick weight, default 1
    steps:
      - method: POST
        url: http://localhost:8080/api/login
        body: '{"user":"alice"}'
        headers:
          Content-Type: application/json
        extract:
          token: $.token              # pull $.token from JSON response into Vars
      - method: GET
        url: http://localhost:8080/api/me
        headers:
          Authorization: Bearer {{token}}  # {{var}} interpolated per virtual user
      - method: GET
        url: http://localhost:8080/api/checkout?token={{token}}
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
- `max_open_conns`, `max_idle_conns`, `conn_max_lifetime`, `conn_max_idle_time`
  (all optional, under `db.target`) tune the `database/sql` connection pool.
  Unset counts default to the run's `concurrency` so the tool never holds more
  connections than it has workers; unset lifetimes leave the driver default.
  Negative values are rejected. Set `max_open_conns` at or below the database's
  `max_connections` or the errors you measure are the tool's, not the target's.
- `scenario:` runs sequential HTTP steps per virtual user — it *is* your HTTP
  load, in journey form instead of single shots. Each VU picks one scenario
  once at launch (weighted by `weight`), then loops it until `duration`
  expires. `extract` maps a var name to a JSON path (`$.token`,
  `$.user.id` via gjson); the value is stored per VU and `{{var}}` is
  interpolated into later step `url`, `body`, and `headers`. Missing vars stay
  as `{{var}}` so misconfig is visible; non-JSON or missing paths leave the
  var unset.
- `scenario:` cannot be combined with `http:` — and that is deliberate, not a
  limitation. Correlation needs exactly one app-side reference timeline per
  bucket: either the `http` runner's P99, or the worst journey P99 synthesized
  from scenarios. Two app curves would double-count rates, progress, and every
  verdict. To mix plain hits with flows, model the plain hit as a one-step
  scenario. Note `rate` also means different things per runner (`http`/`db`/
  `redis` are paced per-second targets; scenario throughput emerges from VUs
  looping). Scenarios can run alongside `db`/`redis` — buckets use the same
  `Start.Unix()/bucket_width` scheme so timelines align.
- `auto_ramp:` replaces a single run with a search: `max_concurrency` caps it,
  `step_duration` (default 10s) sizes each level's burst. Start is the run's
  `concurrency`. While it runs, `ramp:` and `duration:` are ignored — bursts
  force the inner ramp off and use `step_duration` instead.

## CLI

```
$ barrage run --help

Flags:
      --auto-ramp                     ramp concurrency (double, then fine fill) to find the break point
  -b, --bucket-width duration         override the bucket width from the config
      --concurrency int               worker count for the db/redis pools and http attackers
  -c, --config string                 path to the config file (default "config.yaml")
      --db-threshold duration         DB spike threshold for correlation (default 100ms)
  -d, --duration duration             override the run duration from the config
      --http-threshold duration       HTTP spike threshold for correlation (default 100ms)
      --json string                   also write a JSON summary of the run to this path
      --no-progress                   disable the live progress view (plain log lines instead)
      --no-report                     skip writing the HTML report
  -o, --open                          open the report in a browser after the run
      --ramp duration                 ramp the rate from 0 up to full over this duration
      --ramp-max-concurrency int      cap for auto-ramp concurrency search
      --ramp-step-duration duration   per-level burst time for auto-ramp (default 10s)
      --redis-threshold duration      Redis spike threshold for correlation (default 100ms)
      --report string                 path for the HTML report (default "report.html")
  -v, --verbose                       print per-bucket detail
```

Examples:

```sh
barrage run -c staging.yaml --duration 1m --ramp 10s --concurrency 50
barrage run --http-threshold 150ms --db-threshold 250ms --redis-threshold 80ms  # adjust spike thresholds
barrage run --no-report --json results.json                 # for CI pipelines
barrage version                                            # print the version
```

### Web UI

`barrage web` starts a localhost server (default `http://localhost:7676`)
with a config builder, live YAML preview, and inline reports — the same
runners and validation as the CLI, no auth. Use `--addr` to change the
listen address.

```sh
barrage web                 # build configs and run tests from the browser
barrage web --addr :8081    # listen on a different address
```

### Compare runs

`barrage compare` diffs two runs produced by `barrage run --json`, so an earlier
baseline can be checked against a later run — the missing piece for CI gating
and regression checking across releases.

Runners present only in one side are labeled **NEW** (or counted as fixed) —
they never show as regressions just because the baseline didn't have them, so
renaming or adding scenarios mid-project doesn't produce false alarms. Spike
diffs match by ordinal position per runner rather than wall-clock timestamps,
since two runs never share a clock.

```
$ barrage compare --baseline base.json --current new.json --fail-on 100ms

comparing base.json -> new.json (fail-on 100ms)
RUNNER  BASELINE_P99  CURRENT_P99  CHANGE  VERDICT
DB      80ms          100ms        +25%    ok
HTTP    30ms          70ms         +133%   REGRESSION
Redis   20ms          22ms         +10%    ok
```

Flags:

```
  -b, --baseline string      path to the baseline JSON report
  -c, --current string       path to the current JSON report
      --fail-on duration     fail (exit non-zero) if a runner regresses above this latency budget (default 100ms)
  -o, --open                 open the report in a browser after comparing
      --report string        path for the HTML comparison report (default "compare.html")
```

How it works:

- **Per-runner diff.** Each runner's P99 is compared baseline→current with a
  percentage change. A runner is flagged **REGRESSION** when its current P99
  exceeds the `--fail-on` budget while its baseline was at or under it, so a
  runner that was already slow isn't re-flagged every run. Any regression makes
  `barrage compare` exit non-zero — the signal CI used to gate a deployment.
- **Spike diff.** Each correlated spike in both runs is matched by
  (runner, bucket time) and classified as **new**, **fixed**, **worsened**,
  **improved**, or **unchanged**, so you can see both newly-introduced storage
  bottlenecks and ones that were resolved.
- **Overlaid timeline.** Both runs' per-bucket P99 latencies are aligned onto
  one label axis in the HTML report (baseline dashed, current solid), so you can
  see *where* in the run latency drifted.
- **Exit code as CI gate.** Combined with `barrage run --no-report --json
  baseline.json` and `--fail-on`, you can make an unstaged regression fail a
  pipeline before it ships.

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

![Latency timeline](./docs/todo-api-run-2.png)

![Correlated spikes table](./docs/todo-api-run-1.png)

The JSON export mirrors this structure: `generated_at`, `duration`, `ramp`,
`concurrency`, per-runner metrics (latencies in milliseconds), correlated spikes
(each with `runner`, `http_p99_ms`, `storage_p99_ms`, and `masked`), and the
timeline. In the timeline's `p99_ms` series, `-1` marks a bucket where that
runner had no request (e.g. before the ramp produced its first hit); the report
chart renders these as gaps, not as a latency of -1ms.

## Demo stack

Helpers for exercising a local reference backend:

- `cmd/demoserver` — HTTP app on `:8080` with routes for scenarios:
  `POST /api/login` → `{"token":"tok-123"}`, `GET /api/me` (checks
  `Authorization: Bearer {{token}}`), `GET /api/products`, `POST /api/orders`,
  `GET /api/checkout?token={{token}}`. See `examples/scenario-login.yaml`
  (single flow) and `examples/scenarios-weighted.yaml` (weighted browse vs
  checkout) for examples.
- `cmd/seeddb` — bulk-seeds an `orders` table (COPY, 100k-row chunks) so DB
  queries have real work to do:

```sh
go run ./cmd/seeddb -conn "postgres://user:pass@localhost:5432/mydb?sslmode=disable" -n 1000000
```

## Example configs

Ready-to-run profiles live in [`examples/`](examples/), all targeting the demo
server on `:8080`:

| File | What it shows |
|---|---|
| `light.yaml` | gentle baseline: HTTP + Redis at ~15 req/s |
| `heavy.yaml` | stress profile: HTTP + SQLite + Redis at ~4x light, higher concurrency |
| `scenario-login.yaml` | single journey: login, extract token, interpolate into later steps |
| `scenarios-weighted.yaml` | multiple journeys with weights (browse vs checkout traffic mix) |
| `scenario-full.yaml` | full stack: weighted journeys + SQLite (with pool caps) + Redis on one clock |

Run any of them against the demo stack:

```sh
barrage run -c examples/scenarios-weighted.yaml
```

A scenario run looks like this: the `rates` line names each journey with its
step count and weight, the runner column carries the scenario name, and there
is no STATUS column content (scenarios record success as 2xx-per-step, not
status histograms) — the header still prints, the cells are empty.

```
$ barrage run -c examples/scenario-login.yaml

     ________  ________  ________  ________  ________  ________  _______
    |\   __  \|\   __  \|\   __  \|\   __  \|\   __  \|\  ____\|\  ___ \
    \ \  \|\ /\ \  \|\  \ \  \|\  \ \  \|\  \ \  \|\  \ \  \___|\ \   __/|
     \ \   __  \ \   __  \ \   _  _\ \   _  _\ \   __  \ \  \  __\ \  \_|/__
      \ \  \|\  \ \  \ \  \ \  \\  \\ \  \\  \\ \  \ \  \ \  \|\  \ \  \_|\ \
       \ \_______\ \__\ \__\ \__\\ _\\ \__\\ _\\ \__\ \__\ \_______\ \_______\
        \|_______|\|__|\|__\|__|\|__|\|__|\|__|\|__|\|__\|_______|\|_______|

barrage v0.4.0
duration 10s · bucket 1s · concurrency 10 · ramp 0s
rates    login-flow 3 steps w=1

[barrage] done ·  │ scen 32,989 3 err
RUNNER      REQUESTS  SUCCESS  RATE      MEAN        P50         P95         P99          MAX           STATUS
login-flow  32989     100.0%   3298.9/s  3.030392ms  2.272002ms  8.125266ms  11.957243ms  35.747684ms
Report written to report.html
```

## Development

```sh
go test ./...     # unit + integration (miniredis for Redis, httptest for HTTP)
go vet ./...
```

Tests cover the ramp schedule, pool pacing, read/write detection, config
parsing (including unknown-key rejection), correlation, report rendering, and
JSON export. `report.html` is a build artifact and is intentionally not
committed. The report template (`templates/report.html`) is embedded in the
binary via `go:embed`, so reports render from any working directory; a template
file at `templates/report.html` alongside the binary overrides the embedded one.

## Agents

AI agents: read [SKILL.md](SKILL.md) first — bottleneck-hunting workflow
(inspect project, ask user, never assume), YAML construction, result
interpretation, and repo house rules. It is the single skill for barrage;
the landing page (`../barrage-landing`) points back here too.
