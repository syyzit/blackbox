const $ = (id) => document.getElementById(id);

let lastSignature = "";

const state = {
  sessions: [],
  meta: null,
  events: [],
  stats: null,
  selected: -1,
  compare: null,
  decisions: [],
  coverage: null,
  traceId: null,
  search: "",
  filter: "",
  tab: "raw",
};

async function api(path) {
  const res = await fetch(path);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

const pad = (n) => String(n).padStart(2, "0");

function fmtTime(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fmtOffset(ms) {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

function pretty(s) {
  if (!s) return "";
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

async function loadSessions() {
  try {
    const { sessions } = await api("/api/sessions");
    state.sessions = sessions || [];
    renderSessions();
    if (!state.meta && state.sessions.length) {
      const wanted = new URLSearchParams(location.search).get("session");
      const match = wanted
        ? state.sessions.find((s) => s.id === wanted || s.id.startsWith(wanted))
        : null;
      openSession(match ? match.id : state.sessions[0].id);
    }
  } catch (err) {
    console.error(err);
  }
}

function renderSessions() {
  if (state.search.trim().length >= 2) return;
  const list = $("sessionList");
  $("sessionCount").textContent = state.sessions.length ? `(${state.sessions.length})` : "";
  if (!state.sessions.length) {
    list.innerHTML = '<div class="empty">no sessions yet<br><span class="muted">blackbox run -- your-server</span></div>';
    return;
  }
  list.innerHTML = "";
  for (const s of state.sessions) {
    const active = state.meta && s.id === state.meta.id;
    const el = document.createElement("div");
    el.className = "session" + (active ? " active" : "");
    const b =
      state.compare === s.id
        ? '<span class="pill" style="color:var(--blue);border-color:rgba(122,162,255,.45)">B</span>'
        : `<button class="markB" data-id="${esc(s.id)}" title="set as diff B">B</button>`;
    el.innerHTML = `
      <div class="sid">${esc(s.id)}</div>
      <div class="cmd">${esc((s.argv || []).join(" "))}</div>
      <div class="row">
        <span class="pill">${s.events} frames</span>
        <span class="pill">${esc(fmtTime(s.startedAt))}</span>
        ${b}
      </div>`;
    el.addEventListener("click", (e) => {
      if (e.target.classList.contains("markB")) return;
      openSession(s.id);
    });
    const btn = el.querySelector(".markB");
    if (btn) {
      btn.addEventListener("click", (e) => {
        e.stopPropagation();
        state.compare = s.id;
        renderSessions();
        updateDiffButton();
      });
    }
    list.appendChild(el);
  }
}

async function openSession(id, seq) {
  try {
    const data = await api("/api/sessions/" + encodeURIComponent(id));
    state.meta = data.meta;
    state.events = data.events || [];
    state.stats = data.stats;
    state.decisions = data.decisions || [];
    state.coverage = data.coverage || null;
    state.traceId = data.traceId || null;
    state.selected = state.events.length ? 0 : -1;
    if (seq != null) {
      const idx = state.events.findIndex((e) => e.seq === seq);
      if (idx >= 0) state.selected = idx;
    }
    state.tab = "raw";
    lastSignature = signature(data);
    history.replaceState(null, "", "?session=" + encodeURIComponent(data.meta.id));
    renderSessions();
    renderAll();
    updateDiffButton();
    $("exportBtn").disabled = false;
    $("traceBtn").disabled = !state.traceId;
  } catch (err) {
    console.error(err);
  }
}

function signature(data) {
  return `${data.events ? data.events.length : 0}:${data.stats ? data.stats.calls : 0}:${
    data.decisions ? data.decisions.length : 0
  }`;
}

// Follow a session while it is being recorded. The selection is pinned by sequence
// number, so incoming frames never move the frame the user is reading.
async function refreshCurrent() {
  if (!state.meta || document.hidden) return;
  const id = state.meta.id;
  const prevSeq = state.events[state.selected] ? state.events[state.selected].seq : null;
  try {
    const data = await api("/api/sessions/" + encodeURIComponent(id));
    if (!state.meta || state.meta.id !== id) return;
    const sig = signature(data);
    if (sig === lastSignature) return;
    lastSignature = sig;

    state.events = data.events || [];
    state.stats = data.stats;
    state.decisions = data.decisions || [];
    if (prevSeq != null) {
      const idx = state.events.findIndex((e) => e.seq === prevSeq);
      state.selected = idx >= 0 ? idx : Math.min(state.selected, Math.max(0, state.events.length - 1));
    }
    renderAll();
    updateDiffButton();
  } catch (err) {
    console.error(err);
  }
}

function renderAll() {
  const meta = state.meta;
  $("title").textContent = meta ? meta.id : "no session selected";
  $("subtitle").textContent = meta
    ? `${(meta.argv || []).join(" ")} · ${new Date(meta.startedAt).toLocaleString()}`
    : "";
  renderStats();
  renderCoverageBadge();
  renderTimeline();
  renderScrubber();
  renderDetail();
}

function renderCoverageBadge() {
  const badge = $("coverageBadge");
  const cov = state.coverage;
  if (!cov || (!cov.unrecorded && !cov.unverified)) {
    badge.classList.add("hidden");
    return;
  }
  const parts = [];
  if (cov.unrecorded) parts.push(`${cov.unrecorded} unrecorded`);
  if (cov.unverified) parts.push(`${cov.unverified} unverified`);
  badge.textContent = "coverage gap · " + parts.join(" · ");
  badge.classList.remove("hidden");
}

function coverageText(cov) {
  const lines = [
    `root       ${cov.root}`,
    `changed    ${cov.changed} · covered ${cov.covered} · unverified ${cov.unverified} · unrecorded ${cov.unrecorded}`,
  ];
  if (cov.truncated) lines.push("note       snapshot was truncated; results are partial");
  const interesting = (cov.files || []).filter((f) => f.status !== "covered");
  if (interesting.length) {
    lines.push("");
    for (const f of interesting) lines.push(`${f.status.padEnd(10)} ${f.kind.padEnd(9)} ${f.path}`);
    if (cov.more) lines.push(`… ${cov.more} more`);
  }
  return lines.join("\n");
}

function renderStats() {
  const s = state.stats;
  if (!s) {
    $("stats").innerHTML = "";
    return;
  }
  const errClass = s.errors ? "bad" : "";
  const blocked = state.decisions.filter((d) => d.action === "deny" && d.mode !== "shadow").length;
  const would = state.decisions.filter((d) => d.mode === "shadow" && d.action !== "allow").length;
  $("stats").innerHTML = `
    <div class="stat"><div class="v ${errClass}">${s.calls}</div><div class="k">calls</div></div>
    <div class="stat"><div class="v ${errClass}">${s.errors}</div><div class="k">errors</div></div>
    ${blocked ? `<div class="stat"><div class="v bad">${blocked}</div><div class="k">blocked</div></div>` : ""}
    ${would ? `<div class="stat"><div class="v warn">${would}</div><div class="k">would block</div></div>` : ""}
    <div class="stat"><div class="v">${Math.round(s.avgMs)}ms</div><div class="k">avg</div></div>
    <div class="stat"><div class="v">${Math.round(s.maxMs)}ms</div><div class="k">max</div></div>
    <div class="stat"><div class="v">${(s.bytes / 1024).toFixed(1)}k</div><div class="k">bytes</div></div>
    <div class="stat"><div class="v">$${Number(s.costUsd || 0).toFixed(3)}</div><div class="k">cost</div></div>`;
}


function renderTimeline() {
  const tbody = $("timeline");
  tbody.innerHTML = "";
  const events = state.events;
  $("empty").classList.toggle("hidden", events.length > 0);

  const start = state.meta ? new Date(state.meta.startedAt).getTime() : 0;
  const filter = state.filter.trim().toLowerCase();
  const maxDur = Math.max(1, ...events.map((e) => e.durationMs || 0));
  const callRounds = {};
  for (const e of events) {
    if (e.direction === "c2s" && e.callId) callRounds[e.callId] = (callRounds[e.callId] || 0) + 1;
  }

  events.forEach((e, i) => {
    const hay = `${e.method || ""} ${e.tool || ""} ${e.status || ""}`.toLowerCase();
    if (filter && !hay.includes(filter)) return;

    const tr = document.createElement("tr");
    tr.dataset.i = i;
    if (i === state.selected) tr.classList.add("sel");
    if (state.selected >= 0 && i > state.selected) tr.classList.add("beyond");

    const at = new Date(e.at).getTime() - start;
    const dir = e.direction === "c2s" ? "&rarr;" : "&larr;";
    const dirClass = e.direction === "c2s" ? "dir" : "dir out";
    const changed = (e.toolDefs || []).some((d) => d.changed);
    const redacted = (e.raw || "").includes("[REDACTED");
    const rounds = e.callId ? callRounds[e.callId] || 0 : 0;
    const barW = e.durationMs ? Math.max(2, Math.round((e.durationMs / maxDur) * 42)) : 0;

    tr.innerHTML = `
      <td>${e.seq}</td>
      <td class="muted">${fmtOffset(at)}</td>
      <td class="${dirClass}">${dir}</td>
      <td><span class="status st-${esc(e.status || "none")}"><span class="dot"></span>${esc(e.status || "—")}</span></td>
      <td class="method">${esc(e.method || "—")}${
        e.toolDefs && e.toolDefs.length ? `<span class="badge">${e.toolDefs.length} tools</span>` : ""
      }${changed ? '<span class="badge bad">rug-pull</span>' : ""}${
        rounds > 1 ? `<span class="badge">mrtr ×${rounds}</span>` : ""
      }${redacted ? '<span class="badge redact">redacted</span>' : ""}</td>
      <td class="tool">${esc(e.tool || "")}</td>
      <td class="ms">${e.durationMs ? `<span class="bar" style="width:${barW}px"></span>${e.durationMs}` : ""}</td>`;

    tr.addEventListener("click", () => select(i));
    tbody.appendChild(tr);
  });
}

function renderScrubber() {
  const sc = $("scrubber");
  const n = state.events.length;
  sc.max = Math.max(0, n - 1);
  sc.value = Math.max(0, state.selected);
  sc.disabled = n === 0;
  $("scrubLabel").textContent = n ? `${state.selected + 1} / ${n}` : "0 / 0";
}

function select(i) {
  if (i < 0 || i >= state.events.length) return;
  state.selected = i;
  state.tab = "raw";
  document.querySelectorAll("#timeline tr").forEach((tr) => {
    const idx = Number(tr.dataset.i);
    tr.classList.toggle("sel", idx === i);
    tr.classList.toggle("beyond", idx > i);
  });
  renderScrubber();
  renderDetail();
  const row = document.querySelector(`#timeline tr[data-i="${i}"]`);
  if (row) row.scrollIntoView({ block: "nearest" });
}

function fields(e) {
  const out = [["raw", e.raw]];
  if (e.args) out.push(["args", pretty(e.args)]);
  if (e.result) out.push(["result", pretty(e.result)]);
  if (e.error) out.push(["error", e.error]);
  if (e.toolDefs && e.toolDefs.length) out.push(["tools", pretty(JSON.stringify(e.toolDefs, null, 2))]);
  if (state.coverage) out.push(["coverage", coverageText(state.coverage)]);
  if (state.decisions.length) {
    out.push([
      "policy",
      state.decisions
        .map(
          (d) =>
            `${String(d.action).toUpperCase().padEnd(16)} ${String(d.mode || "enforce").padEnd(7)} ${
              d.tool || "-"
            }  ${d.reason || ""}`
        )
        .join("\n"),
    ]);
  }
  return out;
}

function renderDetail() {
  const e = state.events[state.selected];
  const tabs = $("tabs");
  const body = $("detailBody");
  tabs.innerHTML = "";
  if (!e) {
    body.textContent = "select a frame";
    return;
  }
  if (!fields(e).some(([k]) => k === state.tab)) state.tab = "raw";

  const head = [
    `seq ${e.seq}`,
    e.method || "—",
    e.tool || "",
    e.status || "",
    e.durationMs ? `${e.durationMs}ms` : "",
    `${e.size || 0}B`,
    e.toolHash ? `hash ${e.toolHash}` : "",
    e.callId ? `call ${e.callId}` : "",
    e.subscriptionId ? `sub ${e.subscriptionId}` : "",
  ]
    .filter(Boolean)
    .join("  ·  ");

  for (const [name, value] of fields(e)) {
    const b = document.createElement("button");
    b.className = "tab" + (state.tab === name ? " active" : "");
    b.textContent = name;
    b.onclick = () => {
      state.tab = name;
      renderDetail();
    };
    tabs.appendChild(b);
  }

  const chosen = fields(e).find(([name]) => name === state.tab);
  body.textContent = `${head}\n\n${chosen ? chosen[1] : ""}`;
}

function updateDiffButton() {
  const btn = $("diffBtn");
  const ok = state.meta && state.compare && state.compare !== state.meta.id;
  btn.disabled = !ok;
  btn.textContent = ok ? `Diff with ${state.compare.slice(-6)}` : "Diff with B";
}

async function renderSearch(query) {
  const list = $("sessionList");
  list.innerHTML = '<div class="empty">searching…</div>';
  try {
    const data = await api("/api/search?q=" + encodeURIComponent(query) + "&limit=40");
    if (state.search.trim() !== query) return;
    const hits = data.hits || [];
    $("sessionCount").textContent = hits.length ? `(${hits.length})` : "";
    if (!hits.length) {
      list.innerHTML = '<div class="empty">no matches</div>';
      return;
    }
    list.innerHTML = "";
    for (const h of hits) {
      const el = document.createElement("div");
      el.className = "hit";
      el.innerHTML = `
        <div class="top"><span class="sid">${esc(h.sessionId)}</span><span class="seq">#${h.seq}</span></div>
        <div class="snip">${esc(h.tool || h.method || "")} · ${esc(h.snippet || "")}</div>`;
      el.addEventListener("click", () => openSession(h.sessionId, h.seq));
      list.appendChild(el);
    }
  } catch (err) {
    console.error(err);
    list.innerHTML = '<div class="empty">search failed</div>';
  }
}

function sourceOf(event) {
  return String(event.sessionId || "").startsWith("oc-") ? "builtin" : "mcp";
}

async function refreshApprovals() {
  try {
    const data = await api("/api/approvals");
    const n = (data.approvals || []).length;
    $("approvalsBtn").textContent = n ? `Approvals (${n})` : "Approvals";
    $("approvalsBtn").classList.toggle("attention", n > 0);
  } catch (err) {
    // The server may not have approvals endpoints yet; keep the button quiet.
  }
}

function approvalRow(a) {
  return `
    <div class="approval">
      <div class="approval-head">
        <span class="mono">#${a.id} · ${esc(a.tool)}</span>
        <span class="muted mono">${esc(shortId(a.sessionId))} · ${esc(fmtTime(a.createdAt))}</span>
      </div>
      <div class="muted">${esc(a.reason || "")}</div>
      <pre class="mono">${esc(a.args || "")}</pre>
      <div class="approval-actions">
        <button class="btn" data-approve="${a.id}">approve</button>
        <button class="btn ghost" data-deny="${a.id}">deny</button>
      </div>
    </div>`;
}

async function showApprovals() {
  try {
    const data = await api("/api/approvals");
    const list = data.approvals || [];
    $("modalTitle").textContent = "Approvals";
    $("diffBody").innerHTML = list.length
      ? list.map(approvalRow).join("")
      : '<div class="diff-ok">no pending approvals</div>';
    $("diffModal").classList.remove("hidden");

    for (const btn of document.querySelectorAll("[data-approve]")) {
      btn.addEventListener("click", () => decideApproval(btn.dataset.approve, "allow"));
    }
    for (const btn of document.querySelectorAll("[data-deny]")) {
      btn.addEventListener("click", () => decideApproval(btn.dataset.deny, "deny"));
    }
  } catch (err) {
    console.error(err);
  }
}

async function decideApproval(id, decision) {
  try {
    await fetch("/api/approvals/" + encodeURIComponent(id), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ decision, by: "ui" }),
    });
  } catch (err) {
    console.error(err);
  }
  await refreshApprovals();
  await showApprovals();
  await refreshCurrent();
}

function shortId(id) {
  return id && id.length > 18 ? id.slice(0, 8) + "…" + id.slice(-6) : id || "";
}

async function showTrace() {
  if (!state.traceId) return;
  try {
    const data = await api("/api/traces/" + encodeURIComponent(state.traceId));
    const tr = data.trace;
    const start = new Date(tr.startedAt).getTime();
    const lines = (tr.events || []).map((e) => {
      const offset = Math.max(0, new Date(e.at).getTime() - start);
      const tool = e.tool || e.method || "-";
      return `<div class="diff-line"><span class="kind">${esc(fmtOffset(offset))}</span>${esc(
        sourceOf(e)
      )} · ${esc(tool)} · ${esc(e.status || "-")}${e.durationMs ? ` · ${e.durationMs}ms` : ""}</div>`;
    });
    const header = `${tr.events.length} events · ${tr.sessions.length} session(s) · ${new Date(
      tr.startedAt
    ).toLocaleString()}`;
    $("modalTitle").textContent = "Trace " + tr.externalId;
    $("diffBody").innerHTML = `<div class="muted mono" style="margin-bottom:12px">${esc(header)}</div>` + lines.join("");
    $("diffModal").classList.remove("hidden");
  } catch (err) {
    console.error(err);
  }
}

async function showDiff() {
  if (!state.meta || !state.compare) return;
  $("modalTitle").textContent = "Run diff";
  try {
    const data = await api(`/api/diff?a=${encodeURIComponent(state.meta.id)}&b=${encodeURIComponent(state.compare)}`);
    const r = data.report;
    const body = $("diffBody");
    if (r.identical) {
      body.innerHTML = `<div class="diff-ok">identical · ${r.callsA} calls · no behavioral divergence</div>`;
    } else {
      body.innerHTML =
        `<div class="muted mono" style="margin-bottom:12px">${r.callsA} calls &rarr; ${r.callsB} calls · ${r.changes.length} changes</div>` +
        r.changes
          .map(
            (c) =>
              `<div class="diff-line"><span class="kind k-${esc(c.kind)}">${esc(c.kind)}</span>${esc(
                c.method || ""
              )} ${esc(c.tool || "")} <span class="muted">${esc(c.detail || "")}</span></div>`
          )
          .join("");
    }
    $("diffModal").classList.remove("hidden");
  } catch (err) {
    console.error(err);
  }
}

function bind() {
  $("scrubber").addEventListener("input", (e) => select(Number(e.target.value)));
  $("filter").addEventListener("input", (e) => {
    state.filter = e.target.value;
    renderTimeline();
  });
  let searchTimer;
  $("search").addEventListener("input", (e) => {
    state.search = e.target.value;
    clearTimeout(searchTimer);
    const query = state.search.trim();
    if (query.length < 2) {
      renderSessions();
      return;
    }
    searchTimer = setTimeout(() => renderSearch(query), 250);
  });

  $("diffBtn").addEventListener("click", showDiff);
  $("traceBtn").addEventListener("click", showTrace);
  $("approvalsBtn").addEventListener("click", showApprovals);
  $("exportBtn").addEventListener("click", () => {
    if (!state.meta) return;
    window.location.href = "/api/sessions/" + encodeURIComponent(state.meta.id) + "/export";
  });
  $("closeDiff").addEventListener("click", () => $("diffModal").classList.add("hidden"));
  $("diffModal").addEventListener("click", (e) => {
    if (e.target.id === "diffModal") $("diffModal").classList.add("hidden");
  });

  document.addEventListener("keydown", (e) => {
    if (e.target.tagName === "INPUT") return;
    if (e.key === "Escape") {
      $("diffModal").classList.add("hidden");
      return;
    }
    if (e.key === "j" || e.key === "ArrowDown") {
      e.preventDefault();
      select(Math.min(state.events.length - 1, state.selected + 1));
    }
    if (e.key === "k" || e.key === "ArrowUp") {
      e.preventDefault();
      select(Math.max(0, state.selected - 1));
    }
  });
}

bind();
loadSessions();
setInterval(loadSessions, 5000);
setInterval(refreshCurrent, 2000);
setInterval(refreshApprovals, 2000);
refreshApprovals();
