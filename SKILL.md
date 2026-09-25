---
name: barrage
description: Diagnose app slowness with barrage — run HTTP/DB/Redis/scenario load, correlate spikes to find bottlenecks, compare runs for regressions, estimate capacity. Use when a project is slow, when asked where latency comes from, or when working in the barrage repo itself.
---

# Barrage skill

Barrage is a Go CLI that answers one question: **when an app slows down, is it
the app, or the database / cache underneath it?** It fires HTTP, DB, and Redis
load on one clock, buckets every layer's latencies onto the same timeline, and
flags exactly which bucket a storage layer spiked in — and whether the app
felt it.

Use this skill whenever:

- someone says "the API is slow", "p99 is high", "it works locally but not
  under load", "find the bottleneck"
- you need to check a project for perf problems (N+1, missing index,
  pool exhaustion, cache miss, rate limiter, slow journey step)
- you need a before/after proof for a migration, tuning pass, or release
- you are editing the barrage repo itself

## Install / build

```sh
# preferred: prebuilt binary from GitHub releases (no Go needed)
curl -fsSL https://raw.githubusercontent.com/codetesla51/barrage/main/install.sh | bash

# pin a version, change the install dir, or build from source instead:
curl -fsSL https://raw.githubusercontent.com/codetesla51/barrage/main/install.sh | bash -s -- --version v0.7.0 --dir ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/codetesla51/barrage/main/install.sh | bash -s -- --from-source

# from a checkout (requires Go 1.25+):
go build -o barrage ./cmd/barrage
go install github.com/codetesla51/barrage/cmd/barrage@latest  # fallback only
```

## Fastest run (30 seconds)

```sh
# 1. start the demo backend (login/me/products/orders/checkout on :8080)
go run ./cmd/demoserver &

# 2. run a canned profile against it
./barrage run -c examples/light.yaml
./barrage run -c examples/scenarios-weighted.yaml   # user journeys, not plain HTTP

# 3. artifacts land next to you
ls report.html results.json   # --report PATH, --json PATH, --no-report to skip
./barrage run -c config.yaml -o   # ...and open the report in a browser
```

Seed a real Postgres for DB profiles (`POSTGRES_DSN` env works too):

```sh
go run ./cmd/seeddb -conn "postgres://user:pass@localhost:5432/mydb?sslmode=disable" -n 1000000
```

The demo server reads the same env (`POSTGRES_DSN`, `DATABASE_URL`, or
`PGUSER`/`PGPASSWORD`/`PGHOST`/`PGPORT`/`PGDATABASE`): with it set,
products/orders hit real tables; without it, stub responses. Never hardcode
auth — env only.

## Bottleneck-hunting workflow (follow this order)

Do not jump straight to a heavy run. Inspect first, ask second, isolate
third, then stress. **Never assume URLs, ports, DB conns, query text,
rates, or thresholds — derive them from the project or ask.**

### 0. Inspect the CURRENT project first (mandatory, before any YAML)

The target is the user's current working directory, not the barrage
repo and not the demo server. Before writing any config:

- List cwd: README, routes/handlers, DB migrations/schema, Redis usage,
  `docker-compose*.yml`, `.env*`, `config.yaml`, existing barrage
  YAMLs, `package.json`/`go.mod`/`requirements.txt`.
- Extract real values only: actual route paths + methods, actual ports
  from code/compose (not 8080 by default), actual DB driver + DSN shape
  (never real passwords — use placeholders and ask), actual Redis addr,
  limiter/proxy settings, pool sizes.
- If the cwd IS the barrage repo itself, say so and use the demo stack
  (`cmd/demoserver`, `examples/`) only after confirming with the user.

Do not copy `examples/light.yaml` URLs blindly. Every URL, DSN, query,
and rate in your YAML must trace to a file you read or an answer the
user gave. Cite the source (`path:line`) in your plan.

### 1. Ask the user what to test (mandatory question gate)

After inspecting, ask via the question tool and wait. Do not run
barrage until the user confirms at minimum the target + scope. Ask:

1. Which endpoint(s) or journey(s) feel slow? (exact paths or flow)
2. Which environment may I hit? (local / staging — never prod without
   explicit OK) and what base URL / port?
3. What does "slow" mean here? (SLO: e.g. P99 < 200ms; current symptom)
4. What traffic shape? (expected req/s, how many users, read vs write mix)
5. Which layers are in scope? (HTTP/app, DB with which queries, Redis
   with which commands) — and may I read your query files to copy
   exact SQL?
6. Any recent change to prove? (migration, index, tuning, release —
   determines baseline vs current compare)

If the user skips a question, mark it UNKNOWN and pick the safest
default (local, light rate, short duration) — never silently invent
production values. Re-ask rather than assume.

### 2. Recon the target project (2 min, no load yet)

Before running anything, answer:

- Stack: what framework, what DB driver/ORM, is Redis in the request
  path or side-cache, is there a rate limiter / reverse proxy?
- Request path: which endpoint or journey is reported slow? What DB
  tables/queries does it touch? Any `SELECT` in a loop (N+1)?
- Pools/limits: DB `max_connections` / pool size, Redis `maxclients`,
  app `concurrency`, limiter req/s. Note them — you will compare
  barrage `concurrency` and `rate` against these.
- Data volume: empty local DB lies. Seed (`cmd/seeddb`) or point at a
  staging-sized dataset before trusting any DB verdict.

### 3. Baseline: light run, all layers

```sh
barrage run -c examples/light.yaml --json baseline.json
```

Goal is a clean reference, not a failure. Expect `SUCCESS ~100%`,
`RATE` near configured `rate`, `correlated spikes: none`.

### 4. Isolate: one runner at a time

Copy the config and comment out runners so each layer is tested alone:

- `http:` only → measures app + full stack through the endpoint.
- `db:` only → measures raw query mix (bypasses HTTP). Use the exact
  queries the slow endpoint runs, weighted like production.
- `redis:` only → measures raw commands.
- `scenario:` only → measures real user journeys (login → me →
  checkout). Prefer this over `http:` when auth/token or multi-step
  flows matter.

If DB-only is slow but HTTP-only is fast at low rate, the app is
currently masking a storage problem (see verdicts below).

### 5. Stress: raise rate + concurrency, add ramp

```sh
barrage run -c examples/heavy.yaml --json stressed.json
# or override inline:
barrage run -c config.yaml --duration 1m --ramp 10s --concurrency 50 --json stressed.json
```

Keep `bucket_width: 1s`, `duration >= 20s` so buckets are meaningful.
Use `ramp` (e.g. `10s`) to separate cold-start noise from real
saturation — spikes only inside the ramp window usually mean warm-up,
not a bottleneck.

### 6. Read the output in this order

1. **Verdict hero** in `report.html` (or `story` in JSON) — title, bottleneck,
   capacity line, next steps. Normal runs and ramp searches both get one.
2. **Runner table** (`RUNNER REQUESTS SUCCESS RATE MEAN P50 P95 P99 MAX`).
   Ramp mode prints `CONCURRENCY REQUESTS P99 SUCCESS VERDICT CAUSE` instead —
   one row per level, cause names the breaker.
3. **Correlated spikes table** (`TIME RUNNER HTTP_P99 STORAGE_P99 NOTE`).
4. **Latency timeline** in `report.html` — which curve bent first?
5. **Capacity story** in the report — strain point in users.
6. **`results.json`** for exact numbers / CI diffing.

### 7. Report a verdict, not raw numbers

Every diagnosis must end with this block:

```text
Bottleneck: DB | Redis | HTTP/app | EVEN | None (masked only / clean)
Evidence: <which runner, which bucket time, HTTP P99 xms vs storage P99 yms, timeline direction>
Impact: correlated (users feel it) | masked (ticking bomb) | none yet
Next fix: <one concrete change: add index on X, raise pool N→M, cache Y, raise/fix limiter, split journey step Z>
Re-test: <exact barrage command + config change to prove the fix>
```

If you cannot fill `Evidence`, you have not run enough isolation —
say so and run the missing profile instead of guessing.

## How to interpret results

### The four verdicts

| Signal | Meaning | What to say |
|---|---|---|
| `correlated` (HTTP + DB/Redis both over threshold, same bucket) | Storage spike dragged the app with it | Bottleneck is that storage runner. Fix the query/cache, not the handler. |
| `masked` / `db-only` / `redis-only` (storage over, HTTP under) | Storage is spiking but app still absorbs it | Ticking bomb. Report it as "masked DB/Redis bottleneck at HH:MM:SS, app unaffected yet". Common with small rates or big app pools. |
| HTTP-only slow (HTTP P99 high, no storage spike) | App problem | Do NOT blame the DB. Look at handler code, serialization, middleware, limiter, GC, downstream HTTP. |
| No spikes, but P99 climbs with rate | Saturation without a sharp spike | Check `RATE` vs target (see below) + capacity knee. Usually pool/connection/limiter ceiling. |

Scenario mode: the "HTTP" reference is synthesized as the worst
per-bucket journey P99 across scenarios, so the same table applies —
`login-flow P99 12ms vs db P99 136ms masked` still means DB is hot but
journeys absorbed it.

### Fields agents misread

- `SUCCESS` < 100% + `STATUS 429×N` → rate limiter absorbed the burst,
  not the backend. The README TodoAPI example is exactly this: flat
  ~5ms p50 with all 429s means the limiter did its job; boost the
  limiter to test the real backend.
- `STATUS 5xx×N` → app errors under load. Treat as HTTP/app verdict
  even if DB also spikes (app is falling over first).
- `RATE` well below configured `rate` → `concurrency` starved. The pool
  backed up; throughput settled below target **by design**. Raise
  `concurrency` and re-run before calling anything a bottleneck.
- `MEAN` << `P99` → tail problem (a few buckets/queries explode).
  Look at per-bucket `-v` output and the timeline, not the mean.
- `P50` fine, `P95/P99` bad → classic storage tail (missing index,
  lock contention, big SCAN). Check which weighted query dominates the
  slow buckets with `-v`.
- `capacity_search.steps[].errors` → *what* failed at a level, bucketed
  by cause: `5xx`/`4xx` are HTTP answers, `dial_timeout`/`read_timeout`/
  `connection_refused`/`conn_reset`/`transport`/`timeout` come from the
  client stack (stdlib `errors.As` peeling `*url.Error`). A level whose
  failures are all `dial_timeout`/`connection_refused` is an unreachable
  app (shared-box/network), not necessarily an app verdict.
- Timeline `-1` → no request **completed** in that bucket (gap before
  ramp produced hits, or a fault window where everything was stuck
  waiting and nothing finished). Never read it as 0ms latency. The line
  deliberately breaks instead of drawing through it.
- Latency *drops* during an outage → fast failure, not a speedup. Failed
  requests return immediately, so p99 falls while the error count climbs.
  Check `success_percent` before celebrating a clean curve.
- First-bucket spike only → cold start / connection warm-up, not a
  bottleneck. Require 3+ sustained buckets (same rule as the capacity
  knee: worst journey P99 > 2× median for 3+ buckets).

### Thresholds

Defaults are 100ms per runner (`--http-threshold`, `--db-threshold`,
`--redis-threshold`). Tune to the SLO, not to make spikes disappear:

```sh
barrage run --http-threshold 150ms --db-threshold 250ms --redis-threshold 80ms
```

- API SLO 200ms → set `--http-threshold` near it so `correlated`
  means "users breach SLO".
- Cache should be single-digit ms → `--redis-threshold 20ms` surfaces
  cache regressions early.
- Never set storage thresholds far above HTTP's to hide `masked`
  spikes — masked is the early warning.

## Config rules (where agents get bitten)

- Default file is `config.yaml`. At least one of `http`, `db`, `redis`,
  `scenario` is required. **Unknown keys are rejected** — a typo fails loudly.
- **Never invent values.** Every `url`, `conn`, `addr`, `query`, `rate`,
  and threshold must come from Step 0 inspection or Step 1 user answers.
  No guessing ports (not always 8080), no fake tables, no placeholder
  queries presented as results.
- Durations are Go format: `15s`, `1m30s`, `500ms`.
- `scenario:` (singular) **cannot** combine with `http:` — a scenario *is* your
  HTTP load. `scenarios:` (plural) is rejected with a rename hint. To mix
  plain hits with flows, model the plain hit as a one-step scenario.
- `capacity:` (`max_concurrency`, `step_duration`, default 10s) swaps one run
  for a capacity sweep: double from `concurrency` to max, then loop the
  fine fill until ok and broken are adjacent (exact knee, not bracket).
  Start = run's `concurrency`; `ramp:`/`duration:` are ignored while it runs.
  Verdict = lowest broken level + highest ok strictly below it. A level breaks
  on any runner's P99 over its threshold or success < 95%, attributed per
  runner (`CAUSE` column, `broken_by` JSON). Paced rates scale with the level;
  scenario load comes from VUs. Same mode via `--capacity` /
  `--capacity-max-concurrency` / `--capacity-step-duration` flags.
  The pre-0.6 name `auto_ramp:` (and `--auto-ramp`/`--ramp-max-concurrency`/
  `--ramp-step-duration`) still loads with a rename warning.
  See `examples/capacity-pg.yaml`.
- `rate` is a *target*. If `concurrency` is too small to keep up, throughput
  settles below target — that is intentional, not a bug.
- `concurrency`: HTTP → vegeta MaxWorkers (0 = autoscale); DB/Redis → pool
  size (0 = 10). `ramp` grows rate linearly 0→full over the window.
- DB `type: read|write` is authoritative (`Query` vs `Exec`); omitted falls back
  to SQL-text sniffing (SELECT/SHOW/EXPLAIN/WITH → read, `RETURNING` → write).
  Prefer explicit `type`. `args` is per-query, not global.
- `driver`: `postgres` | `mysql` | `sqlite` (pure-Go, no CGO). Aliases like
  `postgresql`/`sqlite3` are normalized; anything else fails with the valid list.
  DSN per driver: Postgres `postgres://...`, MySQL
  `user:pass@tcp(host:3306)/db`, SQLite file path `/tmp/test.db`.
- Scenario vars: `extract: {token: $.token}` (gjson path) then
  `{{token}}` in later step `url`/`body`/`headers`. Missing vars stay literal
  `{{var}}` so misconfig is visible. Each VU picks one scenario once
  (weighted by `weight`), then loops it until `duration` expires.
- Every `--flag` overrides its config counterpart. `--open` cannot combine
  with `--no-report`. Use `-v` for per-bucket tables when a tail needs
  explaining.

## How to construct the YAML (full reference)

Start from a minimal file, then add runners. Validate with
`barrage run -c file.yaml --json out.json` —
same strict loader, unknown keys fail.

Top-level skeleton (every run needs this + at least one runner):

```yaml
duration: 30s       # required, Go format: 15s, 1m30s, 500ms
bucket_width: 1s    # correlation bucket; keep 1s unless run > 5min
ramp: 5s            # 0 = full rate from first request; 5-10s for stress runs
concurrency: 20     # 0 = HTTP autoscale, DB/Redis pool defaults to 10

http: {}    # OR db: {} OR redis: {} OR scenario: []
```

Minimal valid configs:

```yaml
# http-only (simplest smoke test)
duration: 15s
concurrency: 10
http:
  rate: 10
  target:
    method: GET
    url: http://localhost:8080/api/products
```

```yaml
# scenario-only (auth flow, no http: section allowed alongside)
duration: 15s
concurrency: 10
scenario:
  - name: login-flow
    steps:
      - method: POST
        url: http://localhost:8080/api/login
        body: '{"user":"alice"}'
        headers: {Content-Type: application/json}
```

### `http:` — plain endpoint load

```yaml
http:
  rate: 50   # req/s target; if RATE in output is far lower, raise concurrency
  target:
    method: GET                    # GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS (empty = GET)
    url: http://localhost:8080/api/todos
    body: '{"customer": 42}'       # optional, plain string
    header:                        # optional; string OR list both work
      content-type: [application/json]
      authorization: Bearer some-token   # single string also OK
```

### `db:` — weighted query mix

```yaml
db:
  rate: 80   # total q/s across the weighted list, not per query
  target:
    driver: postgres   # postgres|mysql|sqlite (postgresql/pg/sqlite3 normalized)
    conn: postgres://user:pass@localhost:5432/mydb?sslmode=disable
    # mysql: user:pass@tcp(localhost:3306)/mydb
    # sqlite: /tmp/test.db
    # pool (all optional): unset counts default to concurrency so the tool
    # never holds more conns than workers; unset lifetimes = driver default.
    # max_open_conns: -1 means unlimited connections (0 in database/sql);
    # with -1, an unset max_idle_conns defaults to concurrency (SetMaxIdleConns(0)
    # means zero idle, not unlimited). Values below -1 are rejected.
    # max_open_conns: 20
    # max_idle_conns: 20
    # conn_max_lifetime: 5m
    # conn_max_idle_time: 30s
    queries:           # at least 1; one picked per request by weight
      - query: SELECT customer, amount FROM orders LIMIT 10
        weight: 70    # 0 = never picked; omit → 0, so always set it
        type: read    # read → Query, write → Exec; omit = sniff SQL text
        # args: [42]  # optional, per-query bind params only
      - query: INSERT INTO orders (customer, amount) VALUES ('load', 1)
        weight: 30
        type: write
```

Copy the slow endpoint's real queries here with prod-like weights
(70% reads / 30% writes). `type` is authoritative — always set it;
the fallback sniffs `SELECT/SHOW/EXPLAIN/WITH → read`, anything with
`RETURNING` → write. `type` also drives shutdown accounting: a read
cancelled when the run ends counts as a clean abort, while a write's
error is surfaced (it may have executed server-side). Keep
`max_open_conns` at or below the database's `max_connections` (use `-1`
only when you truly want no cap) — above that, the errors you measure
are the tool's, not the target's.

### `redis:` — weighted command mix

```yaml
redis:
  rate: 300
  target:
    addr: localhost:6379
    password: ""   # optional
    db: 0
    queries:
      - {query: PING, weight: 1}
      - {query: GET sess:loadtest, weight: 5}   # splitCommand on spaces
      - {query: SET sess:loadtest ok, weight: 1}
```

### `chaos:` — fault injection (opt-in, pairs with any runner)

Breaks dependencies on a schedule via Toxiproxy while the load runs. Purely
additive: omit the block and nothing about the run changes. Barrage dials the
**proxy** addresses, never the real ones, so faults only touch barrage's own
traffic unless you point the app at a proxy too.

```yaml
chaos:
  api: localhost:8474          # Toxiproxy API (default)
  proxies:                     # created/updated at run start
    - {name: db-proxy, listen: localhost:26000, upstream: localhost:5432}
    - {name: redis-proxy, listen: localhost:26001, upstream: localhost:6379}
  faults:
    - {at: 10s, duration: 8s, proxy: db-proxy, type: down}
    - {at: 30s, duration: 6s, proxy: db-proxy, type: latency, attrs: {latency: 500}}
    - {at: 30s, duration: 6s, proxy: redis-proxy, type: reset_peer, attrs: {timeout: 0}}
```

Each runner needs the proxy address, not the real one — three separate
overrides, no DSN parsing:

| Runner | Field | Example |
|---|---|---|
| `db:` | `chaos_conn` | `postgres://user:pass@localhost:26000/mydb?sslmode=disable` |
| `redis:` | `chaos_addr` | `localhost:26001` |
| `http:` / `scenario:` | `chaos_url` | `http://localhost:26002/api/products` |

- **Types:** `down`, `latency`, `timeout`, `bandwidth`, `slow_close`,
  `slicer`, `limit_data`, `reset_peer`. `down` maps to proxy
  disable/enable, so it is a clean refusal, not a hang. `packet_loss` is
  **rejected** — not in the pinned Toxiproxy v2.12.0 server.
- `at` is from run start; faults overlap freely, and one file may hold
  several faults on the same proxy.
- Every toxic is removed at `at+duration` and again on cleanup, including
  on failure. A fault left behind would silently poison later runs.
- If the Toxiproxy API is unreachable, barrage spawns its own
  `toxiproxy-server` (must be in PATH) and kills it on exit. A server that
  is already running is reused untouched.
- Reports carry `chaos_events` (every add/remove) and `chaos_windows`
  (paired spans, shaded on the latency timeline). A spike **inside** a
  shaded band is the fault; a spike outside one is yours.

**Testing the app instead of the load generator:** point the *app's* DSNs at
the proxies and let `scenario:` VUs drive it — then the faults hit the app's
dependencies, not barrage's. The app must already be running when the run
starts, because it read its DSN at boot. See `examples/chaos-app.yaml`.

### `scenario:` — user journeys (the part agents get wrong)

A scenario is a list of named flows. Each virtual user picks **one**
flow once at launch (weighted), then loops it until `duration` expires.
It **replaces** `http:` — never put both in one file (`scenarios:`
plural is also rejected).

```yaml
scenario:
  - name: browse          # defaults to "scenario" if empty
    weight: 70            # default 1 if omitted; negative = error
    steps:                # at least 1 step, run in order
      - method: GET
        url: http://localhost:8080/api/products
      - method: GET
        url: http://localhost:8080/api/orders

  - name: checkout-flow
    weight: 30
    steps:
      - method: POST
        url: http://localhost:8080/api/login
        body: '{"user":"alice"}'
        headers:
          Content-Type: application/json
        extract:
          token: $.token        # gjson path on JSON response → Vars[token]
          uid: $.user.id        # nested paths OK; missing/non-JSON = var unset
      - method: GET
        url: http://localhost:8080/api/checkout?token={{token}}
        headers:
          Authorization: Bearer {{token}}   # {{var}} in url/body/headers
```

Rules that bite:

- `method` + `url` are required per step. `headers` is
  `map[string]string` (unlike `http.header` which allows lists).
- `{{var}}` interpolates from that VU's own Vars only — no sharing
  between users. Missing var stays literal `{{var}}` so you see the
  misconfig in logs; check `extract` path and that the prior step
  returned JSON + 2xx.
- Success = every step 2xx with no error. One 429/500 poisons the
  whole iteration — that is intentional, check per-step status.
- To mix plain hits with flows, write the plain hit as a one-step
  scenario. To test one slow step, split it into its own scenario and
  load it alone.
- Scenario **can** run alongside `db:`/`redis:` (buckets share the
  same unix-second scheme) — that is how you prove a journey is
  DB-bound.

### Full combined example (journey + storage on one clock)

```yaml
duration: 30s
bucket_width: 1s
ramp: 5s
concurrency: 20
scenario:
  - name: checkout-flow
    weight: 1
    steps:
      - method: POST
        url: http://localhost:8080/api/login
        body: '{"user":"alice"}'
        headers: {Content-Type: application/json}
        extract: {token: $.token}
      - method: GET
        url: http://localhost:8080/api/me
        headers: {Authorization: Bearer {{token}}}
db:
  rate: 80
  target:
    driver: postgres
    conn: postgres://user:pass@localhost:5432/mydb?sslmode=disable
    queries:
      - {query: SELECT amount FROM orders WHERE id = 1, weight: 1, type: read}
redis:
  rate: 100
  target:
    addr: localhost:6379
    queries:
      - {query: PING, weight: 1}
```

### Validation errors you will hit

| Error | Fix |
|---|---|
| `unknown field "X"` | typo — check key spelling against this doc |
| `must specify at least one runner` | empty file or all runners commented out |
| `scenario mode cannot be combined with http` | delete one; scenario *is* HTTP load |
| `scenarios: is renamed to scenario:` | singular |
| `weight must not be negative` / `total weight must be > 0` | set positive weights |
| `url must not be empty` / `invalid method` | fill per-step `method`+`url` |
| `unsupported driver` | use postgres\|mysql\|sqlite (+ aliases) |
| `capacity max_concurrency ...` | max must exceed start `concurrency`; step must cover ≥ 1 bucket |

## Diagnosis recipes (copy/paste)

**Slow endpoint, unknown cause:**

```yaml
# step 1: http-only against the real endpoint
duration: 30s
bucket_width: 1s
ramp: 5s
concurrency: 20
http:
  rate: 50
  target: {method: GET, url: http://localhost:8080/api/todos}
```

```yaml
# step 2: db-only with the endpoint's real queries, weighted like prod
duration: 30s
bucket_width: 1s
ramp: 5s
concurrency: 20
db:
  rate: 80
  target:
    driver: postgres
    conn: postgres://user:pass@localhost:5432/mydb?sslmode=disable
    queries:
      - {query: SELECT customer, amount FROM orders LIMIT 10, weight: 70, type: read}
      - {query: INSERT INTO orders (customer, amount) VALUES ('load', 1), weight: 30, type: write}
```

Run both with `--json`, then run combined and read `correlated spikes`.
DB spikes alone in step 2 + correlated in combined = DB bottleneck proven.

**Suspected N+1 / slow journey:**

```yaml
duration: 30s
bucket_width: 1s
concurrency: 20
scenario:
  - name: checkout-flow
    weight: 30
    steps:
      - method: POST
        url: http://localhost:8080/api/login
        body: '{"user":"alice"}'
        headers: {Content-Type: application/json}
        extract: {token: $.token}
      - method: GET
        url: http://localhost:8080/api/checkout?token={{token}}
        headers: {Authorization: Bearer {{token}}}
```

If one step's latency dominates, split the scenario and load that step
alone to confirm.

**Cache not helping:**

```yaml
redis:
  rate: 300
  target:
    addr: localhost:6379
    queries:
      - {query: PING, weight: 1}
      - {query: GET sess:loadtest, weight: 5}
```

Redis P99 > threshold while HTTP stays flat = masked Redis issue
(evictions, big values, single-thread saturation). Redis P99 flat while
HTTP climbs = cache miss path hitting DB — check DB buckets next.

## Compare / CI gate (prove a fix or catch a regression)

```sh
barrage run --no-report --json baseline.json   # before the change
# ... apply fix / ship release ...
barrage run --no-report --json current.json    # after
barrage compare --baseline baseline.json --current current.json --fail-on 100ms
# exit non-zero on REGRESSION: current P99 over budget while baseline was under
```

- A runner already slow in baseline is *not* re-flagged — only budget
  crossings fail. Brand-new runners show `NEW`, never `REGRESSION`.
- Spike diff matches by ordinal per runner (1st DB spike vs 1st DB
  spike) because two runs never share a clock: `new` / `fixed` /
  `worsened` / `improved` / `unchanged`.
- Overlaid timeline (baseline dashed, current solid) shows *where* in
  the run latency drifted — use it to distinguish ramp noise from
  sustained regression.

## False bottlenecks checklist (check before blaming code)

- [ ] Limiter/proxy returning 429/503 — check `STATUS` column first.
- [ ] `concurrency` too low — `RATE` below target means starved pool,
      not slow backend.
- [ ] Empty/unseeded DB — fast queries on 100 rows prove nothing.
      Seed with `cmd/seeddb` (COPY, 100k-row chunks).
- [ ] Ramp window only — ignore single early-bucket spikes.
- [ ] Wrong `type:` on DB queries — writes routed through `Query`
      (or vice versa) distort latency; set explicit `read`/`write`.
- [ ] `{{var}}` left literal in scenario URLs — extract path wrong or
      non-JSON response; var stays `{{var}}` deliberately so you see it.
- [ ] miniredis/httptest vs real service — unit-test backends don't
      saturate like prod; confirm against staging before verdicting.

## Repo case studies (proven here — don't re-learn these)

The demo stack is a *real-ish* backend, not a stub: bcrypt login with
HMAC-signed tokens (`AUTH_SECRET`, 15 min expiry) and **session reuse on
repeat logins** (bcrypt once per session, then a fast HMAC password check),
indexed reads (orders list
scans the PK for the newest 20 rows — **no `COUNT(*)` over the 1M-row seed**),
Redis-cached read routes with write-invalidation, and seeded users
(`alice`/`bob`/`carol`, password `secret`). Capacity profiles live in
`docker/configs/` and sweep concurrency 5→200 in levels. The full progression,
all on a 2-vCPU GitHub runner with the same journey mix:

1. **The `COUNT(*)` wall.** Original stack did `SELECT count(*) FROM orders`
   over 1M rows; DB P99 sat at ~100ms doing nothing and the sweep broke at
   0–1 users with DB blamed. Fix: read newest-20 via the PK index, add the
   `(customer, id DESC)` / `(created_at)` indexes the read paths need, cache
   the read routes. Knee went 0→10 users; tail at 5 users 646ms→22ms.
2. **Cache moves the boundary, it does not remove it.** A 5s TTL read cache
   took the old-schema knee 0→3, and the DB only broke again because the
   synthetic `db:` runner still pounded the store directly.
3. **Once the DB is fixed, the app is the wall.** At 11+ users the blame
   becomes the app's own cost — bcrypt CPU + synchronous writes — not a
   store. That is the honest shape of a working backend.
4. **Synthetic load can crowd out the app.** The VU-only profile
   (`real-app.yaml`, scenario with no `db:`/`redis:` sections) gives the
   clean whole-app knee: ~7–8 users, `scenario` blamed alone, ~1–3K req/s of
   real journey traffic.
5. **Every break here was latency, not errors** — success held 98–100%.
   A "broken" level means the P99 crossed the threshold, not that requests
   failed.
6. **Label the failure, don't guess it (v0.7.0).** Capacity-step exports
   carry an `errors` map bucketed by cause (`5xx`/`4xx` from HTTP answers;
   `dial_timeout`/`read_timeout`/`connection_refused`/`conn_reset`/
   `transport`/`timeout` from the client stack). A run that breaks with a
   `dial_timeout`/`connection_refused` flood is an *unreachable* app —
   shared-box/network fabric — not an app verdict. A knee with clean buckets
   is a real latency crossing. Example from the session-reuse runs: a 0%
   success level previously written off as "9,639 err" was in fact the app
   unreachable; the next run with buckets clean across levels broke at 12→13
   users with p99 climbing 19ms→1s — a genuine app-latency knee.
7. **Separated runs exist now.** `.github/workflows/demo-stack-separated.yml`
   moves the load to a second runner VM through a cloudflared quick tunnel
   (no account) for the decisive whole-app figure. Same caveat as below —
   VM-level separation, numbers still relative — but removes the shared-box
   blast radius that killed old single-box runs at high firehose rates.

### GitHub-runner numbers are RELATIVE — say so

`demo-stack.yml` runs the whole stack (load generator + app + Postgres +
Redis) in containers on one runner, and runner VMs are **not reproducible**:
size and neighbours vary between runs. Same-profile knees have wobbled 7 vs 10
users; a TTL cache-expiry herd collapses isolated levels. Consequences to
state in every verdict:

- absolute knees / req/s describe *that* box on *that* day — they are
  relative shapes, never a reproducible capacity spec;
- the trustworthy outputs are: which layer broke, and whether the boundary
  moved between configs;
- for a real capacity figure, load from a separate machine on pinned
  hardware and repeat. Same-machine sweeps prove shape, not scale.

## CLI reference

```sh
barrage run -c config.yaml --duration 1m --ramp 10s --concurrency 50
barrage run --http-threshold 150ms --db-threshold 250ms --redis-threshold 80ms
barrage run --no-report --json results.json   # CI mode
barrage run -v                                # per-bucket tables
barrage run -c config.yaml --capacity --capacity-max-concurrency 160 --capacity-step-duration 10s
barrage compare --baseline base.json --current new.json --fail-on 100ms
barrage version
```

## How it works (read code in this order)

| Piece | File | Note |
|---|---|---|
| Fan-out | `barrage.go` → `Orchestrator()` | one goroutine per runner, `Stats` shared for live progress |
| Config | `config.go` → `LoadConfigBytes()` | single loader for everything — never fork validation |
| Runners | `http.go`, `db.go`, `redis.go`, `scenario_run.go` | DB/Redis pace at `rate`/s into a pond pool; buckets key on submission time |
| Capacity sweep | `capacity.go` → `RunCapacitySweep()` | coarse double + fine fill over concurrency; `fireDB`/`fireRedis` shared with normal runs; pools stay warm across levels |
| Correlation | `correlation.go` → `Correlate()` | storage P99 > threshold ⇒ **correlated** (HTTP also over) or **masked** (HTTP under, `masked: true`, CLI shows `db-only`/`redis-only`) |
| Capacity knee | `story.go` → `capacityLine()` | strain = worst journey P99 > 2× median for 3+ buckets |
| Story verdict | `story.go` → `BuildStory()` | title/detail/bottleneck/next-steps for normal + ramp runs; rendered in `report.html`, exported as `story` JSON |
| Report | `report.go` + `templates/report.html` | template is `go:embed`ded; a `templates/report.html` next to the binary overrides it |
| Compare | `compare.go` | P99 diff per runner + spike diff by ordinal (runs never share a clock); NEW runners never regress |
Timeline detail: per-bucket `p99_ms` uses `-1` for "no request in bucket"
(rendered as a chart gap, never as latency). Buckets align on unix start time
across runners — that alignment is the whole product, don't break it.

## Tests

```sh
go test ./...
go vet ./...
```

miniredis covers Redis, httptest covers HTTP, no external services needed.
Cover new behavior: ramp schedule, pool pacing, read/write routing, config
rejection (unknown keys, scenario+http, bad weights), correlation verdicts,
report/JSON shape. `report.html` / `compare.html` are build artifacts — never
commit them.

## Making it better (house rules)

- **One loader.** All config parsing goes through `LoadConfigBytes` — keep it that way.
- **Boring Go, stdlib first.** Match existing style: short funcs, explicit
  errors (`fmt.Errorf("...: %w", err)`), no new frameworks for solved problems.
- **Thresholds are per-runner** (`--http-threshold`, `--db-threshold`,
  `--redis-threshold`, default 100ms). New spike logic must stay per-runner.
- **Report data contract.** `NewReportData` / `ExportJSON` feed CLI, HTML,
  and compare — changing the JSON shape breaks all three. Update them together.
- **Version bump = tag.** `cmd/barrage/main.go: version` is stamped by
  `-ldflags -X ...main.version=` in `.github/workflows/build.yml`; pushing a
  `v*` tag builds all platforms and publishes the release `install.sh` pulls.
  After tagging, sync the version string, README example output, and
  `barrage-landing` screenshot/install block.
- **Landing page is a separate checkout** (`../barrage-landing`, static HTML,
  `python3 -m http.server 8890` to preview). Its install block must match this
  repo's `install.sh` one-liner.
