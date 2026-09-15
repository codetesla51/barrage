---
name: barrage
description: Load-test with barrage — run HTTP/DB/Redis/scenario load, correlate spikes, compare runs, use the web UI. Use when working in the barrage repo or load-testing a target with it.
---

# Barrage skill

Barrage is a Go CLI that answers one question: **when an app slows down, is it
the app, or the database / cache underneath it?** It fires HTTP, DB, and Redis
load on one clock, buckets every layer's latencies onto the same timeline, and
flags exactly which bucket a storage layer spiked in — and whether the app
felt it.

## Install / build

```sh
# preferred: prebuilt binary from GitHub releases (no Go needed)
curl -fsSL https://raw.githubusercontent.com/codetesla51/barrage/main/install.sh | bash

# pin a version, change the install dir, or build from source instead:
curl -fsSL https://raw.githubusercontent.com/codetesla51/barrage/main/install.sh | bash -s -- --version v0.3.12 --dir ~/.local/bin
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

Seed a real Postgres for DB profiles:

```sh
go run ./cmd/seeddb -conn "postgres://user:pass@localhost:5432/mydb?sslmode=disable" -n 1000000
```

## Config rules (where agents get bitten)

- Default file is `config.yaml`. At least one of `http`, `db`, `redis`,
  `scenario` is required. **Unknown keys are rejected** — a typo fails loudly.
- Durations are Go format: `15s`, `1m30s`, `500ms`.
- `scenario:` (singular) **cannot** combine with `http:` — a scenario *is* your
  HTTP load. `scenarios:` (plural) is rejected with a rename hint.
- `rate` is a *target*. If `concurrency` is too small to keep up, throughput
  settles below target — that is intentional, not a bug.
- `concurrency`: HTTP → vegeta MaxWorkers (0 = autoscale); DB/Redis → pool
  size (0 = 10). `ramp` grows rate linearly 0→full over the window.
- DB `type: read|write` is authoritative (`Query` vs `Exec`); omitted falls back
  to SQL-text sniffing (SELECT/SHOW/EXPLAIN/WITH → read, `RETURNING` → write).
  Prefer explicit `type`. `args` is per-query, not global.
- `driver`: `postgres` | `mysql` | `sqlite` (pure-Go, no CGO). Aliases like
  `postgresql`/`sqlite3` are normalized; anything else fails with the valid list.
- Scenario vars: `extract: {token: $.token}` (gjson path) then
  `{{token}}` in later step `url`/`body`/`headers`. Missing vars stay literal
  `{{var}}` so misconfig is visible. Each VU picks one scenario once
  (weighted), then loops it until `duration` expires.

## CLI reference

```sh
barrage run -c config.yaml --duration 1m --ramp 10s --concurrency 50
barrage run --http-threshold 150ms --db-threshold 250ms --redis-threshold 80ms
barrage run --no-report --json results.json   # CI mode
barrage run -v                                # per-bucket tables
barrage compare --baseline base.json --current new.json --fail-on 100ms
barrage web --addr :8081                      # localhost:7676 by default
barrage version
```

Every `--flag` overrides its config counterpart. `--open` cannot combine with
`--no-report`.

## How it works (read code in this order)

| Piece | File | Note |
|---|---|---|
| Fan-out | `barrage.go` → `Orchestrator()` | one goroutine per runner, `Stats` shared for live progress |
| Config | `config.go` → `LoadConfigBytes()` | single loader; the web UI validates through this too — never fork validation |
| Runners | `http.go`, `db.go`, `redis.go`, `scenario_run.go` | DB/Redis pace at `rate`/s into a pond pool; buckets key on submission time |
| Correlation | `correlation.go` → `Correlate()` | storage P99 > threshold ⇒ **correlated** (HTTP also over) or **masked** (HTTP under, `masked: true`, CLI shows `db-only`/`redis-only`) |
| Capacity knee | `webui/app.js` → `buildCapacity()` | strain = worst journey P99 > 2× median for 3+ buckets; mirrored in report copy |
| Report | `report.go` + `templates/report.html` | template is `go:embed`ded; a `templates/report.html` next to the binary overrides it |
| Compare | `compare.go` | P99 diff per runner + spike diff by ordinal (runs never share a clock); NEW runners never regress |
| Web UI | `ui.go` + `webui/` | `NewUIServer(addr)`; static assets embedded, runs under `~/.barrage/ui/runs/` |

Timeline detail: per-bucket `p99_ms` uses `-1` for "no request in bucket"
(rendered as a chart gap, never as latency). Buckets align on unix start time
across runners — that alignment is the whole product, don't break it.

## Web UI

`barrage web` serves the config builder (simple/advanced modes, presets, live
YAML preview, import), one-at-a-time run execution (`409` if busy), Recent
Runs, story + technical report views, and file-or-run compare. API:

- `POST /api/validate` `{yaml}` → `{ok, runners}` (same loader as CLI)
- `POST /api/runs` `{yaml, *_threshold_ms}` → `{id, duration_s}`
- `GET /api/runs`, `GET /api/runs/{id}/status|report|json`
- `POST /api/compare` `{baseline_id, current_id, fail_on_ms}`
- `POST /api/compare-upload` `{baseline, current, fail_on_ms}`
- `GET /api/version` → `{version}` (baked in via ldflags at release)

`webui/` is vanilla JS, no build step. Open it with `barrage web` — don't
`python3 -m http.server` it and expect runs to work (API won't exist).

## Compare / CI gate

```sh
barrage run --no-report --json baseline.json   # before the change
barrage run --no-report --json current.json    # after
barrage compare --baseline baseline.json --current current.json --fail-on 100ms
# exit non-zero on REGRESSION: current P99 over budget while baseline was under
```

A runner already slow in baseline is *not* re-flagged — only crossings fail.

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

- **One loader.** All config parsing goes through `LoadConfigBytes`. The UI's
  import path and `/api/validate` already use it — keep it that way.
- **Boring Go, stdlib first.** Match existing style: short funcs, explicit
  errors (`fmt.Errorf("...: %w", err)`), no new frameworks for solved problems.
- **Thresholds are per-runner** (`--http-threshold`, `--db-threshold`,
  `--redis-threshold`, default 100ms). New spike logic must stay per-runner.
- **Report data contract.** `NewReportData` / `ExportJSON` feed CLI, HTML, UI,
  and compare — changing the JSON shape breaks all four. Update them together.
- **Version bump = tag.** `cmd/barrage/main.go: version` is stamped by
  `-ldflags -X ...main.version=` in `.github/workflows/build.yml`; pushing a
  `v*` tag builds all platforms and publishes the release `install.sh` pulls.
  After tagging, sync the version string, README example output, and
  `barrage-landing` screenshot/install block.
- **Landing page is a separate checkout** (`../barrage-landing`, static HTML,
  `python3 -m http.server 8890` to preview). Its install block must match this
  repo's `install.sh` one-liner.
