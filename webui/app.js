/* barrage web ui — plain js, no build step.
   state -> form -> yaml (preview) -> validate -> run -> report. */

"use strict";

/* ---------- state ---------- */

const defaultState = () => ({
  duration: "15s",
  bucket_width: "1s",
  ramp: "3s",
  concurrency: 10,
  http_threshold: "100ms",
  db_threshold: "100ms",
  redis_threshold: "100ms",
  http: { on: false, rate: 10, method: "GET", url: "", body: "", headers: [] },
  db: { on: false, rate: 5, driver: "postgres", conn: "", queries: [] },
  redis: { on: false, rate: 20, addr: "", password: "", dbnum: 0, queries: [] },
  scenarios: [], // {on, name, weight, steps:[{method,url,body,headers:[],extract:[]}]}
});

let state = defaultState();
let currentRunId = null;
let pollTimer = null;
const runsDirBase = ""; // same origin

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

/* ---------- helpers ---------- */

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const child of children) {
    if (child == null) continue;
    node.append(child);
  }
  return node;
}

function toast(msg, kind = "ok", href = null) {
  const icon = el("i", { class: `ph ${kind === "ok" ? "ph-check-circle" : "ph-warning-circle"}`, "aria-hidden": "true" });
  const body = el("span", {}, msg);
  const t = el("div", { class: `toast ${kind}` }, icon, body);
  if (href) {
    const link = el("a", { href, target: "_blank", rel: "noopener", text: "open" });
    t.append(link);
  }
  $("#toasts").append(t);
  setTimeout(() => t.remove(), 6000);
}

async function api(path, opts = {}) {
  const res = await fetch(runsDirBase + path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

/* ---------- yaml generation ---------- */

// single-quote a string the YAML way ('' escapes ')
function yq(s) {
  return "'" + String(s).replace(/'/g, "''") + "'";
}
function ynum(v) {
  return String(v === "" || v == null ? 0 : v);
}
function ydur(v, fallback) {
  const s = String(v ?? "").trim();
  return s === "" ? fallback : s;
}

function generateYAML() {
  const L = [];
  L.push(`duration: ${ydur(state.duration, "15s")}`);
  L.push(`bucket_width: ${ydur(state.bucket_width, "1s")}`);
  if (String(state.ramp).trim() !== "" && state.ramp !== "0s") L.push(`ramp: ${state.ramp}`);
  L.push(`concurrency: ${ynum(state.concurrency)}`);

  if (state.http.on) {
    L.push("");
    L.push("http:");
    L.push(`  rate: ${ynum(state.http.rate)}`);
    L.push("  target:");
    L.push(`    method: ${state.http.method}`);
    L.push(`    url: ${yq(state.http.url)}`);
    if (state.http.body.trim() !== "") L.push(`    body: ${yq(state.http.body)}`);
    const headers = state.http.headers.filter((h) => h.k.trim() !== "");
    if (headers.length > 0) {
      L.push("    header:");
      for (const h of headers) L.push(`      ${h.k}: [${yq(h.v)}]`);
    }
  }

  if (state.db.on) {
    L.push("");
    L.push("db:");
    L.push(`  rate: ${ynum(state.db.rate)}`);
    L.push("  target:");
    L.push(`    driver: ${state.db.driver}`);
    L.push(`    conn: ${yq(state.db.conn)}`);
    const qs = state.db.queries.filter((q) => q.query.trim() !== "");
    if (qs.length > 0) {
      L.push("    queries:");
      for (const q of qs) {
        L.push(`      - query: ${yq(q.query.replace(/\n/g, " "))}`);
        if (Number(q.weight) > 0 && Number(q.weight) !== 1) L.push(`        weight: ${ynum(q.weight)}`);
        if (q.type) L.push(`        type: ${q.type}`);
      }
    }
  }

  if (state.redis.on) {
    L.push("");
    L.push("redis:");
    L.push(`  rate: ${ynum(state.redis.rate)}`);
    L.push("  target:");
    L.push(`    addr: ${yq(state.redis.addr)}`);
    if (state.redis.password !== "") L.push(`    password: ${yq(state.redis.password)}`);
    L.push(`    db: ${ynum(state.redis.dbnum)}`);
    const qs = state.redis.queries.filter((q) => q.query.trim() !== "");
    if (qs.length > 0) {
      L.push("    queries:");
      for (const q of qs) {
        L.push(`      - query: ${yq(q.query)}`);
        if (Number(q.weight) > 0 && Number(q.weight) !== 1) L.push(`        weight: ${ynum(q.weight)}`);
      }
    }
  }

  if (state.scenarios.some((s) => s.on)) {
    L.push("");
    L.push("scenarios:");
    for (const sc of state.scenarios) {
      if (!sc.on) continue;
      L.push(`  - name: ${yq(sc.name || "scenario")}`);
      if (Number(sc.weight) !== 1) L.push(`    weight: ${ynum(sc.weight)}`);
      L.push("    steps:");
      for (const st of sc.steps) {
        L.push(`      - method: ${st.method}`);
        L.push(`        url: ${yq(st.url)}`);
        if ((st.body || "").trim() !== "") L.push(`        body: ${yq(st.body)}`);
        const headers = (st.headers || []).filter((h) => h.k.trim() !== "");
        if (headers.length > 0) {
          L.push("        headers:");
          for (const h of headers) L.push(`          ${h.k}: ${yq(h.v)}`);
        }
        const ex = (st.extract || []).filter((e) => e.k.trim() !== "");
        if (ex.length > 0) {
          L.push("        extract:");
          for (const e of ex) L.push(`          ${e.k}: ${e.v.includes(" ") ? yq(e.v) : e.v}`);
        }
      }
    }
  }

  return L.join("\n") + "\n";
}

/* ---------- yaml preview ---------- */

// tiny local YAML highlighter — no CDN dependency. we control the generator's
// output shape, so a per-line tokenizer is enough.
function escHtml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function hlValue(v) {
  let out = escHtml(v);
  out = out.replace(/\{\{(\w+)\}\}/g, '<span class="yk-var">{{$1}}</span>');
  out = out.replace(/'[^']*'/g, (m) => `<span class="yk-str">${m}</span>`);
  out = out.replace(/(^|\s)(-?\d+(?:\.\d+)?(?:ms|s|m|h)?)(?=\s|$)/g,
    (m, pre, num) => `${pre}<span class="yk-num">${num}</span>`);
  return out;
}

function hlLine(raw) {
  let line = raw, comment = "";
  const cIdx = raw.search(/(^|\s)#/);
  if (cIdx >= 0) {
    comment = `<span class="yk-com">${escHtml(raw.slice(cIdx))}</span>`;
    line = raw.slice(0, cIdx);
  }
  const km = line.match(/^(\s*)(?:-\s+)?([A-Za-z_][\w.\-]*)(:)(.*)$/);
  if (!km) return escHtml(line) + comment;
  const [, indent, key, colon, rest] = km;
  const dash = /^\s*-\s/.test(line) ? "- " : "";
  return `${indent}${dash}<span class="yk-key">${escHtml(key)}</span><span class="yk-pun">:</span>${hlValue(rest)}${comment}`;
}

function yamlHighlight(code) {
  return code.split("\n").map(hlLine).join("\n");
}

let previewTimer = null;
function updatePreview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(() => {
    $("#yaml-code").innerHTML = yamlHighlight(generateYAML());
  }, 50);
}

/* ---------- validation ---------- */

const DURATION_RE = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;

function isDuration(v) {
  return DURATION_RE.test(String(v).trim()) || /^\d+$/.test(String(v).trim());
}

// returns [{field, msg}] — empty means valid
function validateState() {
  const errors = [];
  const need = (cond, field, msg) => { if (!cond) errors.push({ field, msg }); };

  need(isDuration(state.duration), "duration", "duration must be like 15s / 1m30s");
  need(isDuration(state.bucket_width), "bucket_width", "bucket_width must be a duration");

  const c = Number(state.concurrency);
  need(Number.isInteger(c) && c >= 0, "concurrency", "concurrency must be an integer ≥ 0");

  if (state.http.on) {
    need(Number(state.http.rate) > 0, "http-rate", "http rate must be > 0");
    need(/^https?:\/\/.+/.test(state.http.url.trim()), "http-url", "http url must start with http:// or https://");
    for (const h of state.http.headers) {
      if (h.k.trim() === "" && h.v.trim() !== "") need(false, "http-headers", "header row missing a key");
    }
  }
  if (state.db.on) {
    need(Number(state.db.rate) > 0, "db-rate", "db rate must be > 0");
    need(["postgres", "mysql", "sqlite"].includes(state.db.driver), "db-driver", "unknown driver");
    need(state.db.conn.trim() !== "", "db-conn", "db conn string is required");
    const qs = state.db.queries.filter((q) => q.query.trim() !== "");
    need(qs.length > 0, "db-queries", "db needs at least one query");
  }
  if (state.redis.on) {
    need(Number(state.redis.rate) > 0, "redis-rate", "redis rate must be > 0");
    need(/^\S+:\d+$/.test(state.redis.addr.trim()), "redis-addr", "redis addr must be host:port");
    const qs = state.redis.queries.filter((q) => q.query.trim() !== "");
    need(qs.length > 0, "redis-queries", "redis needs at least one command");
  }

  const anyScenario = state.scenarios.some((s) => s.on);
  if (anyScenario && state.http.on) {
    errors.push({ field: null, msg: "scenarios cannot be combined with http" });
  }
  state.scenarios.forEach((sc, i) => {
    if (!sc.on) return;
    const label = sc.name || `scenario ${i + 1}`;
    need(sc.steps.length > 0, null, `${label} needs at least one step`);
    sc.steps.forEach((st, j) => {
      need(st.url.trim() !== "", null, `${label} step ${j + 1}: url required`);
      if (st.url.trim() !== "") need(/^https?:\/\//.test(st.url.trim()), null, `${label} step ${j + 1}: url must start with http(s)://`);
    });
  });

  need(
    state.http.on || state.db.on || state.redis.on || anyScenario,
    null,
    "include at least one runner"
  );
  return errors;
}

function renderValidation() {
  $$(".invalid").forEach((n) => n.classList.remove("invalid"));
  const errors = validateState();
  const runBtn = $("#btn-run");
  runBtn.disabled = errors.length > 0 || pollTimer !== null;

  // mark inputs invalid by name where mappable
  for (const e of errors) {
    if (!e.field) continue;
    const input = document.querySelector(`[name="${e.field}"]`);
    if (input) input.classList.add("invalid");
  }
  $("#validation-summary").textContent =
    errors.length > 0
      ? `${errors.length} problem${errors.length > 1 ? "s" : ""}: ${errors.map((e) => e.msg).join("; ")}`
      : "";
  updateSectionSummaries();
}

/* ---------- section summaries ---------- */

function updateSectionSummaries() {
  const h = state.http;
  $("#sum-http").textContent = h.on
    ? `${h.rate}/s · ${h.method} ${h.url.replace(/^https?:\/\/[^/]+/, "") || "—"}`
    : "";
  const d = state.db;
  $("#sum-db").textContent = d.on ? `${d.rate}/s · ${d.driver} · ${d.queries.length} queries` : "";
  const r = state.redis;
  $("#sum-redis").textContent = r.on ? `${r.rate}/s · ${r.addr || "—"} · ${r.queries.length} commands` : "";
  const scn = state.scenarios.filter((s) => s.on);
  $("#sum-scenarios").textContent =
    scn.length > 0 ? scn.map((s) => `${s.name || "?"} (${s.steps.length} steps w=${s.weight})`).join(" · ") : "";

  $("#sec-http").classList.toggle("off", !state.http.on);
  $("#sec-db").classList.toggle("off", !state.db.on);
  $("#sec-redis").classList.toggle("off", !state.redis.on);
  $("#sec-scenarios").classList.toggle("off", scn.length === 0);
}

/* ---------- repeatable rows ---------- */

// removes the DOM row and splices state; both must happen or the UI lies
function delButton(rowEl, onRemove) {
  return el("button", {
    class: "row-del", type: "button", title: "remove row", "aria-label": "remove row",
    onclick: () => {
      onRemove();
      rowEl.remove();
      changed();
    },
  }, el("i", { class: "ph ph-x", "aria-hidden": "true" }));
}

// one key -> value input pair; onRemove must splice the pair out of state
function kvRow(pair, placeholderK, placeholderV, onRemove) {
  const row = el("div", { class: "row" });
  const k = el("input", { class: "mono grow", placeholder: placeholderK, value: pair.k, autocomplete: "off" });
  const v = el("input", { class: "mono grow", placeholder: placeholderV, value: pair.v, autocomplete: "off" });
  k.addEventListener("input", () => { pair.k = k.value; changed(); });
  v.addEventListener("input", () => { pair.v = v.value; changed(); });
  row.append(k, v, delButton(row, onRemove));
  return row;
}

// push into state, then render the DOM row
function addHttpHeader() {
  const pair = { k: "", v: "" };
  state.http.headers.push(pair);
  appendHeaderRow($("#http-headers"), pair, state.http.headers);
  changed();
}

function appendHeaderRow(container, pair, ownerArray) {
  container.append(kvRow(pair, "Content-Type", "application/json", () => {
    ownerArray.splice(ownerArray.indexOf(pair), 1);
  }));
}

function addDbQuery(q = { query: "", weight: 1, type: "" }) {
  state.db.queries.push(q);
  appendDbQueryRow($("#db-queries"), q);
  changed();
}

function appendDbQueryRow(container, q) {
  const ta = el("textarea", { class: "mono grow", rows: "2", placeholder: "SELECT count(*) FROM orders", spellcheck: "false" });
  ta.value = q.query;
  ta.addEventListener("input", () => { q.query = ta.value; changed(); });
  const w = el("input", { class: "mono w-small", type: "number", min: "0", value: q.weight, title: "weight" });
  w.setAttribute("aria-label", "query weight");
  w.addEventListener("input", () => { q.weight = w.value; changed(); });
  const t = el("select", { class: "w-med", title: "read/write routing" },
    el("option", { value: "", text: "auto-detect" }),
    el("option", { value: "read", text: "read" }),
    el("option", { value: "write", text: "write" }));
  t.value = q.type;
  t.setAttribute("aria-label", "query type");
  t.addEventListener("change", () => { q.type = t.value; changed(); });
  const row = el("div", { class: "row" });
  row.append(ta, w, t, delButton(row, () => { state.db.queries.splice(state.db.queries.indexOf(q), 1); }));
  container.append(row);
}

function addRedisQuery(q = { query: "PING", weight: 1 }) {
  state.redis.queries.push(q);
  appendRedisQueryRow($("#redis-queries"), q);
  changed();
}

function appendRedisQueryRow(container, q) {
  const inp = el("input", { class: "mono grow", value: q.query, placeholder: "PING", autocomplete: "off" });
  inp.addEventListener("input", () => { q.query = inp.value; changed(); });
  const w = el("input", { class: "mono w-small", type: "number", min: "0", value: q.weight, title: "weight" });
  w.setAttribute("aria-label", "command weight");
  w.addEventListener("input", () => { q.weight = w.value; changed(); });
  const row = el("div", { class: "row" });
  row.append(inp, w, delButton(row, () => { state.redis.queries.splice(state.redis.queries.indexOf(q), 1); }));
  container.append(row);
}

function addScenario() {
  const scenario = { on: true, name: "", weight: 1, steps: [] };
  state.scenarios.push(scenario);
  appendScenarioBox($("#scenario-list"), scenario);
  changed();
}

function appendScenarioBox(container, scenario) {
  const box = el("fieldset", { class: "scenario-box" });
  const headRow = el("div", { class: "row" });
  const nameInp = el("input", { class: "grow", placeholder: "name — e.g. login-flow", value: scenario.name, autocomplete: "off" });
  nameInp.addEventListener("input", () => { scenario.name = nameInp.value; changed(); });
  const wInp = el("input", { class: "mono w-small", type: "number", min: "0", value: scenario.weight, title: "pick weight" });
  wInp.setAttribute("aria-label", "scenario weight");
  wInp.addEventListener("input", () => { scenario.weight = wInp.value; changed(); });
  headRow.append(nameInp, wInp);

  const stepsWrap = el("div", { class: "rows" });
  box.append(headRow, stepsWrap);

  function renderSteps() {
    stepsWrap.innerHTML = "";
    scenario.steps.forEach((st, idx) => stepsWrap.append(stepRow(st, idx)));
  }

  function stepRow(st, idx) {
    const rowBox = el("div", { class: "step-box" });
    const line1 = el("div", { class: "row" });

    // move up/down instead of drag — simple and keyboard accessible
    const order = el("span", { class: "step-order" });
    const up = el("button", { type: "button", title: "move step up", onclick: () => {
      if (idx === 0) return;
      [scenario.steps[idx - 1], scenario.steps[idx]] = [scenario.steps[idx], scenario.steps[idx - 1]];
      renderSteps(); changed();
    }}, el("i", { class: "ph ph-caret-up", "aria-hidden": "true" }));
    const down = el("button", { type: "button", title: "move step down", onclick: () => {
      if (idx >= scenario.steps.length - 1) return;
      [scenario.steps[idx + 1], scenario.steps[idx]] = [scenario.steps[idx], scenario.steps[idx + 1]];
      renderSteps(); changed();
    }}, el("i", { class: "ph ph-caret-down", "aria-hidden": "true" }));
    order.append(up, down);
    up.disabled = idx === 0;
    down.disabled = idx >= scenario.steps.length - 1;

    const methodSel = el("select", { class: "w-med" },
      ["GET", "POST", "PUT", "PATCH", "DELETE"].map((m) => el("option", { value: m, text: m })));
    methodSel.value = st.method;
    methodSel.addEventListener("change", () => { st.method = methodSel.value; changed(); });
    const urlInp = el("input", { class: "mono grow", placeholder: "https://host/path or {{var}} allowed", value: st.url, autocomplete: "off" });
    urlInp.addEventListener("input", () => { st.url = urlInp.value; changed(); });
    const delStep = delButton(rowBox, () => {
      const i = scenario.steps.indexOf(st); if (i >= 0) scenario.steps.splice(i, 1);
    });
    line1.append(order, methodSel, urlInp, delStep);

    const bodyInp = el("textarea", { class: "mono", rows: "2", placeholder: "body — optional, {{var}} allowed", spellcheck: "false" });
    bodyInp.value = st.body || "";
    bodyInp.addEventListener("input", () => { st.body = bodyInp.value; changed(); });

    const extractWrap = el("div", { class: "rows" });
    for (const pair of st.extract)
      extractWrap.append(kvRow(pair, "token", "$.token", () => { st.extract.splice(st.extract.indexOf(pair), 1); }));
    const addExtract = el("button", { class: "btn tiny ghost", type: "button", onclick: () => {
      const pair = { k: "", v: "" };
      st.extract.push(pair);
      extractWrap.append(kvRow(pair, "token", "$.token", () => { st.extract.splice(st.extract.indexOf(pair), 1); }));
      changed();
    }}, el("i", { class: "ph ph-plus", "aria-hidden": "true" }), "extract var");

    const headerWrap = el("div", { class: "rows" });
    for (const pair of st.headers)
      headerWrap.append(kvRow(pair, "Authorization", "Bearer {{token}}", () => { st.headers.splice(st.headers.indexOf(pair), 1); }));
    const addHeader = el("button", { class: "btn tiny ghost", type: "button", onclick: () => {
      const pair = { k: "", v: "" };
      st.headers.push(pair);
      headerWrap.append(kvRow(pair, "Authorization", "Bearer {{token}}", () => { st.headers.splice(st.headers.indexOf(pair), 1); }));
      changed();
    }}, el("i", { class: "ph ph-plus", "aria-hidden": "true" }), "header");

    rowBox.append(line1, bodyInp, headerWrap, addHeader, extractWrap, addExtract);
    return rowBox;
  }

  const addStep = el("button", { class: "btn tiny", type: "button", onclick: () => {
    scenario.steps.push({ method: "GET", url: "", body: "", headers: [], extract: [] });
    renderSteps(); changed();
  }}, el("i", { class: "ph ph-plus", "aria-hidden": "true" }), " step");

  const removeScenario = el("button", { class: "btn tiny ghost", type: "button", onclick: () => {
    const i = state.scenarios.indexOf(scenario); if (i >= 0) state.scenarios.splice(i, 1);
    box.remove(); changed();
  }}, el("i", { class: "ph ph-x", "aria-hidden": "true" }), " remove scenario");

  box.append(addStep, removeScenario);
  container.append(box);
  renderSteps();
}

/* ---------- form <-> state binding ---------- */

function bindStaticFields() {
  const map = {
    duration: (v) => (state.duration = v),
    bucket_width: (v) => (state.bucket_width = v),
    ramp: (v) => (state.ramp = v),
    concurrency: (v) => (state.concurrency = v),
    http_threshold: (v) => (state.http_threshold = v),
    db_threshold: (v) => (state.db_threshold = v),
    redis_threshold: (v) => (state.redis_threshold = v),
    "http-rate": (v) => (state.http.rate = v),
    "http-method": (v) => (state.http.method = v),
    "http-url": (v) => (state.http.url = v),
    "http-body": (v) => (state.http.body = v),
    "db-rate": (v) => (state.db.rate = v),
    "db-driver": (v) => (state.db.driver = v),
    "db-conn": (v) => (state.db.conn = v),
    "redis-rate": (v) => (state.redis.rate = v),
    "redis-addr": (v) => (state.redis.addr = v),
    "redis-password": (v) => (state.redis.password = v),
    "redis-dbnum": (v) => (state.redis.dbnum = v),
  };
  for (const [name, set] of Object.entries(map)) {
    $$(`[name="${name}"]`).forEach((input) =>
      input.addEventListener("input", () => { set(input.value); changed(); }));
  }
  $("#en-http").addEventListener("change", (e) => { state.http.on = e.target.checked; changed(); });
  $("#en-db").addEventListener("change", (e) => { state.db.on = e.target.checked; changed(); });
  $("#en-redis").addEventListener("change", (e) => { state.redis.on = e.target.checked; changed(); });
  $("#en-scenarios").addEventListener("change", (e) => {
    state.scenarios.forEach((s) => (s.on = e.target.checked));
    changed();
  });

  $$("[data-reveal]").forEach((btn) =>
    btn.addEventListener("click", () => {
      const input = document.querySelector(`[name="${btn.dataset.reveal}"]`);
      input.type = input.type === "password" ? "text" : "password";
      btn.querySelector("i").className = input.type === "password" ? "ph ph-eye" : "ph ph-eye-slash";
    }));

  $$("[data-add]").forEach((btn) =>
    btn.addEventListener("click", () => {
      switch (btn.dataset.add) {
        case "http-header": addHttpHeader(); break;
        case "db-query": addDbQuery(); break;
        case "redis-query": addRedisQuery(); break;
        case "scenario": addScenario(); break;
      }
    }));
}

function changed() {
  renderValidation();
  updatePreview();
}

/* ---------- presets ---------- */

const PRESETS = {
  http: {
    http: { on: true, rate: 10, method: "GET", url: "http://localhost:8080/api/orders",
      body: "", headers: [{ k: "Content-Type", v: "application/json" }] },
  },
  "http-db": {
    http: { on: true, rate: 10, method: "POST", url: "http://localhost:8080/api/orders",
      body: '{"customer": 42}', headers: [{ k: "Content-Type", v: "application/json" }] },
    db: { on: true, rate: 5, driver: "postgres",
      conn: "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
      queries: [
        { query: "SELECT customer, amount FROM orders LIMIT 10", weight: 20, type: "read" },
        { query: "INSERT INTO orders (customer, amount) VALUES ('load', 1)", weight: 25, type: "write" },
      ] },
  },
  full: {
    http: { on: true, rate: 10, method: "POST", url: "http://localhost:8080/api/orders",
      body: '{"customer": 42}', headers: [{ k: "Content-Type", v: "application/json" }] },
    db: { on: true, rate: 5, driver: "postgres",
      conn: "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
      queries: [
        { query: "SELECT customer, amount FROM orders LIMIT 10", weight: 20, type: "read" },
        { query: "INSERT INTO orders (customer, amount) VALUES ('load', 1)", weight: 25, type: "write" },
      ] },
    redis: { on: true, rate: 20, addr: "localhost:6379", password: "", dbnum: 0,
      queries: [{ query: "PING", weight: 1 }] },
  },
};

function applyPreset(name) {
  const labels = { http: "HTTP only", "http-db": "HTTP + DB", full: "Full stack", blank: "Blank" };
  if (name === "blank") {
    state = defaultState();
  } else {
    const preset = PRESETS[name];
    if (!preset) return;
    Object.assign(state.http, structuredClone(preset.http));
    state.http.on = true;
    if (preset.db) { Object.assign(state.db, structuredClone(preset.db)); state.db.on = true; }
    if (preset.redis) { Object.assign(state.redis, structuredClone(preset.redis)); state.redis.on = true; }
  }
  hydrateFormFromState();
  toast(`${labels[name]} preset applied`, "ok");
}

/* push state back into the static inputs + rebuild dynamic rows */
/* push state back into the inputs + rebuild dynamic rows.
   flashes each section that got populated so it's obvious what was filled. */
function hydrateFormFromState() {
  const put = (name, v) => { const n = document.querySelector(`[name="${name}"]`); if (n) n.value = v; };
  const flashIf = (on, sectionId) => {
    const sec = $(sectionId);
    sec.classList.remove("flash");
    if (on) {
      void sec.offsetWidth; // restart the transition
      sec.classList.add("flash");
      setTimeout(() => sec.classList.remove("flash"), 1200);
    }
  };

  put("duration", state.duration); put("bucket_width", state.bucket_width);
  put("ramp", state.ramp); put("concurrency", state.concurrency);
  put("http_threshold", state.http_threshold); put("db_threshold", state.db_threshold);
  put("redis_threshold", state.redis_threshold);

  $("#en-http").checked = state.http.on;
  put("http-rate", state.http.rate); put("http-method", state.http.method);
  put("http-url", state.http.url); put("http-body", state.http.body);
  const headerBox = $("#http-headers");
  headerBox.innerHTML = "";
  state.http.headers.forEach((h) => appendHeaderRow(headerBox, h, state.http.headers));
  flashIf(state.http.on, "#sec-http");

  $("#en-db").checked = state.db.on;
  put("db-rate", state.db.rate); put("db-driver", state.db.driver); put("db-conn", state.db.conn);
  $("#db-queries").innerHTML = "";
  state.db.queries.forEach((q) => appendDbQueryRow($("#db-queries"), q));
  flashIf(state.db.on, "#sec-db");

  $("#en-redis").checked = state.redis.on;
  put("redis-rate", state.redis.rate); put("redis-addr", state.redis.addr);
  put("redis-password", state.redis.password); put("redis-dbnum", state.redis.dbnum);
  $("#redis-queries").innerHTML = "";
  state.redis.queries.forEach((q) => appendRedisQueryRow($("#redis-queries"), q));
  flashIf(state.redis.on, "#sec-redis");

  const anyScenario = state.scenarios.some((s) => s.on);
  $("#en-scenarios").checked = anyScenario;
  $("#scenario-list").innerHTML = "";
  state.scenarios.forEach((s) => appendScenarioBox($("#scenario-list"), s));
  flashIf(anyScenario, "#sec-scenarios");

  changed();
}

/* ---------- import yaml ---------- */

function openModal(id) { document.querySelector(id).showModal(); }
function closeModal(id) { document.querySelector(id).close(); }

function importIntoState(raw) {
  let doc;
  if (typeof jsyaml === "undefined") {
    $("#import-errors").textContent = "yaml parser failed to load from CDN — check your connection and reload";
    return false;
  }
  try {
    doc = jsyaml.load(raw);
  } catch (e) {
    $("#import-errors").textContent = "yaml parse error: " + e.message;
    return false;
  }
  if (typeof doc !== "object" || doc === null) {
    $("#import-errors").textContent = "config is empty";
    return false;
  }

  const known = new Set(["duration", "bucket_width", "ramp", "concurrency",
    "http_threshold", "db_threshold", "redis_threshold",
    "http", "db", "redis", "scenarios"]);
  const unknown = Object.keys(doc).filter((k) => !known.has(k));

  const next = defaultState();
  next.duration = String(doc.duration ?? next.duration);
  next.bucket_width = String(doc.bucket_width ?? next.bucket_width);
  next.ramp = doc.ramp == null ? "" : String(doc.ramp);
  next.concurrency = Number(doc.concurrency ?? next.concurrency);

  if (doc.http) {
    next.http.on = true;
    next.http.rate = Number(doc.http.rate ?? 10);
    next.http.method = doc.http.target?.method ?? "GET";
    next.http.url = doc.http.target?.url ?? "";
    next.http.body = doc.http.target?.body ?? "";
    const hdr = doc.http.target?.header ?? {};
    next.http.headers = Object.entries(hdr).map(([k, v]) => ({ k, v: Array.isArray(v) ? v.join(", ") : String(v) }));
  }
  if (doc.db) {
    next.db.on = true;
    next.db.rate = Number(doc.db.rate ?? 5);
    next.db.driver = doc.db.target?.driver ?? "postgres";
    next.db.conn = doc.db.target?.conn ?? "";
    next.db.queries = (doc.db.target?.queries ?? []).map((q) => ({
      query: String(q.query ?? ""), weight: Number(q.weight ?? 1), type: String(q.type ?? ""),
    }));
  }
  if (doc.redis) {
    next.redis.on = true;
    next.redis.rate = Number(doc.redis.rate ?? 20);
    next.redis.addr = doc.redis.target?.addr ?? "";
    next.redis.password = doc.redis.target?.password ?? "";
    next.redis.dbnum = Number(doc.redis.target?.db ?? 0);
    next.redis.queries = (doc.redis.target?.queries ?? []).map((q) => ({
      query: String(q.query ?? ""), weight: Number(q.weight ?? 1),
    }));
  }
  if (Array.isArray(doc.scenarios)) {
    next.scenarios = doc.scenarios.map((sc) => ({
      on: true, name: String(sc.name ?? ""), weight: Number(sc.weight ?? 1),
      steps: (sc.steps ?? []).map((st) => ({
        method: String(st.method ?? "GET"), url: String(st.url ?? ""), body: String(st.body ?? ""),
        headers: Object.entries(st.headers ?? {}).map(([k, v]) => ({ k, v: String(v) })),
        extract: Object.entries(st.extract ?? {}).map(([k, v]) => ({ k, v: String(v) })),
      })),
    }));
    if (!doc.http) next.http.on = false;
  }

  state = next;
  $("#import-errors").textContent =
    unknown.length > 0 ? "ignored unknown keys (rejected server-side too): " + unknown.join(", ") : "";
  closeModal("#import-modal");
  hydrateFormFromState();
  toast("yaml imported — check the highlighted fields", "ok");
  return true;
}

/* ---------- pre-run preview ---------- */

function parseDurSec(v) {
  const m = String(v).trim().match(/^(\d+(?:\.\d+)?)(ms|s|m|h)?$/);
  if (!m) return 0;
  const n = parseFloat(m[1]);
  switch (m[2]) {
    case "ms": return n / 1000;
    case "m": return n * 60;
    case "h": return n * 3600;
    default: return n;
  }
}

function buildPrerunSummary() {
  const secs = parseDurSec(state.duration);
  const parts = [];
  if (state.http.on) parts.push(`about ${Math.round(state.http.rate * secs)} HTTP requests`);
  if (state.db.on) parts.push(`${Math.round(state.db.rate * secs)} DB queries`);
  if (state.redis.on) parts.push(`${Math.round(state.redis.rate * secs)} Redis commands`);
  const scn = state.scenarios.filter((s) => s.on);
  if (scn.length > 0) parts.push(`${state.concurrency || 10} virtual users looping ${scn.map((s) => s.name || "a scenario").join(" / ")}`);

  let text = `This will send ${parts.join(", ")} over ${state.duration}`;
  const ramp = parseDurSec(state.ramp);
  if (ramp > 0 && String(state.ramp).trim() !== "") text += `, ramping up over the first ${state.ramp}`;
  text += ".";
  $("#prerun-summary").textContent = text;

  const targets = $("#prerun-targets");
  targets.innerHTML = "";
  const addTarget = (icon, label, detail) =>
    targets.append(el("li", {},
      el("i", { class: `ph ${icon}`, "aria-hidden": "true" }),
      el("span", {}, `${label} — will connect to `),
      el("code", { text: detail })));
  if (state.http.on) addTarget("ph-globe", "HTTP", state.http.url);
  if (state.db.on) addTarget("ph-database", "DB", state.db.conn);
  if (state.redis.on) addTarget("ph-lightning", "Redis", state.redis.addr);
  for (const sc of scn) addTarget("ph-steps", "Scenario", `${sc.name || "unnamed"} (${sc.steps.length} steps)`);
}

/* ---------- run flow ---------- */

async function startRun() {
  try {
    const data = await api("/api/runs", {
      method: "POST",
      body: JSON.stringify({
        yaml: generateYAML(),
        http_threshold_ms: Math.round(parseDurSec(state.http_threshold) * 1000),
        db_threshold_ms: Math.round(parseDurSec(state.db_threshold) * 1000),
        redis_threshold_ms: Math.round(parseDurSec(state.redis_threshold) * 1000),
      }),
    });
    currentRunId = data.id;
    beginPolling(data.id, data.duration_s);
    toast("run started", "ok");
  } catch (e) {
    toast(e.message, "err");
    renderValidation();
  }
}

function beginPolling(id, durationS) {
  const clock = $("#run-clock");
  clock.hidden = false;
  clock.classList.add("live");
  $("#btn-run").disabled = true;

  clearInterval(pollTimer);
  pollTimer = setInterval(async () => {
    try {
      const st = await api(`/api/runs/${id}/status`);
      const elapsed = Math.floor(st.elapsed_s);
      $("#clock-text").textContent =
        st.state === "running" ? `running ${elapsed}s` : st.state === "done" ? "done" : "error";
      $("#clock-duration").textContent = `of ${Math.round(st.duration_s)}s`;
      const pct = Math.min(100, (st.elapsed_s / Math.max(1, st.duration_s)) * 100);
      $("#progress span").style.width = pct + "%";

      if (st.state !== "running") {
        clearInterval(pollTimer);
        pollTimer = null;
        clock.classList.remove("live");
        $("#progress span").style.width = "0%";
        $("#btn-run").disabled = validateState().length > 0;
        refreshRecentRuns();

        if (st.state === "done") {
          toast("Run complete — view report", "ok");
          showReport(id, `run ${id}`);
        } else {
          toast(`run failed: ${st.error || "unknown error"}`, "err");
        }
        currentRunId = null;
      }
    } catch {
      /* transient polling errors are ignored until next tick */
    }
  }, 500);
}

function showReport(id, title) {
  $("#report-title").textContent = title;
  $("#report-frame").src = `/api/runs/${id}/report`;
  $("#btn-open-report-tab").href = `/api/runs/${id}/report`;
  $("#main-split").hidden = true;
  $("#compare-view").hidden = true;
  $("#report-view").hidden = false;
}

function showForm() {
  $("#report-view").hidden = true;
  $("#compare-view").hidden = true;
  $("#main-split").hidden = false;
}

/* ---------- recent runs ---------- */

let recentCache = [];

async function refreshRecentRuns() {
  try {
    recentCache = await api("/api/runs");
    renderRecentList();
  } catch { /* panel just stays stale */ }
}

function fmtTime(iso) {
  const d = new Date(iso);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) +
    " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function renderRecentList() {
  const list = $("#recent-list");
  list.innerHTML = "";
  if (recentCache.length === 0) {
    list.append(el("li", { class: "empty-note dim", text: "no runs yet — press Run to create the first one" }));
    return;
  }
  for (const run of recentCache) {
    const stats = (run.runners || []).map((r) => `${r.name} p99 ${r.p99_ms}ms`).join(" · ");
    const li = el("li", { class: "recent-row" });
    const cb = el("input", { type: "checkbox", "data-run-id": run.id, "aria-label": `select run ${run.id} for compare` });
    cb.addEventListener("change", updateCompareButton);
    const main = el("div", { class: "recent-main" },
      el("span", { class: "recent-name mono", text: `run ${run.id}` }),
      el("span", { class: "recent-meta", text: `${fmtTime(run.created_at)} · ${run.duration || "?"}` }));
    li.append(cb, main,
      el("span", { class: "recent-stats mono", text: stats || run.state }));
    if (run.state !== "done") li.append(el("span", { class: `state-badge ${run.state}`, text: run.state }));
    else {
      const open = el("button", { class: "btn tiny ghost", type: "button", onclick: () => showReport(run.id, `run ${run.id}`) },
        el("i", { class: "ph ph-file-text", "aria-hidden": "true" }), " report");
      li.append(open);
    }
    list.append(li);
  }
}

function selectedRuns() {
  return $$("#recent-list input[type=checkbox]:checked").map((cb) => cb.dataset.runId);
}

function updateCompareButton() {
  $("#btn-compare-selected").disabled = selectedRuns().length !== 2;
}

/* ---------- compare view ---------- */

async function runCompare() {
  const [a, b] = selectedRuns();
  // older id = baseline (ids are millisecond timestamps)
  const baseline = a < b ? a : b;
  const current = a < b ? b : a;
  const failOnMs = Math.round(parseDurSec($("#fail-on").value) * 1000) || 100;

  let data;
  try {
    data = await api("/api/compare", {
      method: "POST",
      body: JSON.stringify({ baseline_id: baseline, current_id: current, fail_on_ms: failOnMs }),
    });
  } catch (e) {
    toast(e.message, "err");
    return;
  }

  const banner = $("#compare-banner");
  if (data.regressions === 0) {
    banner.className = "banner ok";
    banner.textContent = "No regressions found — this run performed the same or better than baseline.";
  } else {
    const names = data.rows.filter((r) => r.regressed).map((r) => r.name).join(", ");
    banner.className = "banner err";
    banner.textContent = `${data.regressions} regression${data.regressions > 1 ? "s" : ""} found — ${names} got slower than the budget allows.`;
  }

  const tbody = $("#diff-table tbody");
  tbody.innerHTML = "";
  for (const r of data.rows) {
    const verdict = r.regressed
      ? el("span", { class: "verdict-tag" }, el("span", { class: "badge reg", text: "REGRESSION" }), el("span", { class: "spike-plain", text: "Got slower ⚠" }))
      : el("span", { class: "verdict-tag" }, el("span", { class: "badge ok", text: "ok" }), el("span", { class: "spike-plain", text: "Within budget" }));
    tbody.append(el("tr", {},
      el("td", { text: r.name }),
      el("td", { class: "mono", text: `${r.baseline_p99_ms}ms` }),
      el("td", { class: "mono", text: `${r.current_p99_ms}ms` }),
      el("td", { class: "mono", text: `${r.pct_change >= 0 ? "+" : ""}${r.pct_change}%` }),
      el("td", {}, verdict)));
  }

  const spikeText = {
    new: "New slow spot appeared in this run.",
    fixed: "This slow spot is gone compared to last time.",
    worsened: "This slow spot got worse compared to last time.",
    improved: "This slow spot got better compared to last time.",
    unchanged: "Same slow spot as last time.",
  };
  const ul = $("#spike-list");
  ul.innerHTML = "";
  if (data.spikes.length === 0) ul.append(el("li", { class: "spike-plain", text: "No correlated spikes in either run." }));
  for (const sp of data.spikes) {
    ul.append(el("li", { class: "spike-row" },
      el("span", { class: "mono", text: sp.bucket_time }),
      el("span", { class: "badge reg", text: sp.runner }),
      el("span", { class: "badge reg", text: sp.status }),
      el("span", { class: "spike-plain", text: spikeText[sp.status] || "" })));
  }

  $("#main-split").hidden = true;
  $("#report-view").hidden = true;
  $("#compare-view").hidden = false;
}

/* ---------- theme ---------- */

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  localStorage.setItem("barrage-theme", theme);
  $("#theme-icon").className = theme === "dark" ? "ph ph-moon" : "ph ph-sun";
}

/* ---------- copy / download ---------- */

async function copyYAML() {
  await navigator.clipboard.writeText(generateYAML());
  toast("yaml copied to clipboard", "ok");
}

function downloadYAML() {
  const blob = new Blob([generateYAML()], { type: "text/yaml" });
  const a = el("a", { href: URL.createObjectURL(blob), download: "config.yaml" });
  a.click();
  URL.revokeObjectURL(a.href);
}

/* ---------- wiring ---------- */

function init() {
  applyTheme(localStorage.getItem("barrage-theme") || "dark");
  bindStaticFields();

  $("#btn-theme").addEventListener("click", () =>
    applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark"));
  $("#btn-copy-yaml").addEventListener("click", copyYAML);
  $("#btn-download-yaml").addEventListener("click", downloadYAML);

  $$("[data-preset]").forEach((b) => b.addEventListener("click", () => applyPreset(b.dataset.preset)));

  // import
  $("#btn-import").addEventListener("click", () => { $("#import-errors").textContent = ""; openModal("#import-modal"); });
  $("#btn-import-cancel").addEventListener("click", () => closeModal("#import-modal"));
  $("#import-file").addEventListener("change", async (e) => {
    const file = e.target.files[0];
    if (file) $("#import-text").value = await file.text();
  });
  $("#btn-import-apply").addEventListener("click", () => importIntoState($("#import-text").value));

  // run flow
  $("#btn-run").addEventListener("click", () => {
    if (localStorage.getItem("barrage-skip-preview") === "1") { startRun(); return; }
    buildPrerunSummary();
    openModal("#prerun-modal");
  });
  $("#btn-back-edit").addEventListener("click", () => closeModal("#prerun-modal"));
  $("#skip-preview").addEventListener("change", (e) =>
    localStorage.setItem("barrage-skip-preview", e.target.checked ? "1" : "0"));
  $("#btn-start-run").addEventListener("click", () => { closeModal("#prerun-modal"); startRun(); });

  // views
  $("#btn-back-to-form").addEventListener("click", showForm);
  $("#btn-back-from-compare").addEventListener("click", showForm);
  $("#fail-on").addEventListener("change", () => { if (selectedRuns().length === 2) runCompare(); });
  $("#btn-compare-selected").addEventListener("click", () => {
    $("#recent-panel").hidden = true;
    runCompare();
  });

  // recent runs dropdown
  const panel = $("#recent-panel");
  $("#btn-recent").addEventListener("click", async () => {
    panel.hidden = !panel.hidden;
    if (!panel.hidden) await refreshRecentRuns();
  });
  document.addEventListener("click", (e) => {
    if (!panel.hidden && !panel.contains(e.target) && !$("#btn-recent").contains(e.target)) panel.hidden = true;
  });

  // help
  $("#btn-help").addEventListener("click", () => openModal("#help-modal"));
  $("#btn-help-close").addEventListener("click", () => closeModal("#help-modal"));

  // keyboard shortcuts
  document.addEventListener("keydown", (e) => {
    const mod = e.ctrlKey || e.metaKey;
    if (mod && e.key === "Enter") { e.preventDefault(); if (!$("#btn-run").disabled) $("#btn-run").click(); }
    else if (mod && e.key.toLowerCase() === "k") { e.preventDefault(); $("#btn-recent").click(); }
    else if (mod && e.key.toLowerCase() === "i") { e.preventDefault(); $("#btn-import").click(); }
    else if (e.key === "?" && !e.target.matches("input,textarea,select")) { e.preventDefault(); openModal("#help-modal"); }
  });

  refreshRecentRuns();
  changed();

  // deep link for testing/automation: ?preset=http|http-db|full|blank
  const qp = new URLSearchParams(location.search).get("preset");
  if (qp === "blank" || PRESETS[qp]) applyPreset(qp);
}

init();
