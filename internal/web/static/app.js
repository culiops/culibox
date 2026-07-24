"use strict";

// --- fetch helpers -----------------------------------------------------------
async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

const $ = (id) => document.getElementById(id);
const enc = new TextEncoder();

const panes = new Map(); // id -> Pane
let cols = 1;

// --- Pane: one xterm.js terminal bound to one session's /ws/{id} -------------
class Pane {
  constructor(id) {
    this.id = id;
    this.closed = false;
    this.retries = 0;

    this.el = document.createElement("div");
    this.el.className = "pane";
    const head = document.createElement("div");
    head.className = "pane-head";
    const pid = document.createElement("span");
    pid.className = "pid";
    pid.textContent = id;
    this.status = document.createElement("span");
    this.status.className = "st";
    this.status.textContent = "…";
    const x = document.createElement("button");
    x.className = "x";
    x.title = "Close pane (session keeps running)";
    x.textContent = "×";
    x.onclick = () => this.close();
    head.append(pid, this.status, x);
    const termEl = document.createElement("div");
    termEl.className = "pane-term";
    this.el.append(head, termEl);

    this.term = new Terminal({
      cursorBlink: true, fontFamily: "ui-monospace, Menlo, monospace",
      fontSize: 13, scrollback: 5000, theme: { background: "#000000" },
    });
    this.fitAddon = new FitAddon.FitAddon();
    this.term.loadAddon(this.fitAddon);
    this.term.open(termEl);
    try {
      this.term.loadAddon(new WebglAddon.WebglAddon()); // fast renderer
    } catch (_) { /* canvas fallback */ }
    this.fitAddon.fit();
    this.term.onData((d) => { if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(enc.encode(d)); });

    this.connect();
  }

  connect() {
    // Reconnect contract: reset before (re)play so the server's ring replay +
    // forced redraw land on a clean screen (no double-render).
    this.term.reset();
    this.setStatus("connecting…");
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/ws/${encodeURIComponent(this.id)}`);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    ws.onopen = () => { this.retries = 0; this.setStatus("connected"); this.sendResize(); this.term.focus(); };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") this.term.write(ev.data);
      else this.term.write(new Uint8Array(ev.data));
    };
    ws.onclose = () => {
      if (this.closed) return;
      this.setStatus("disconnected");
      if (this.retries < 5) { this.retries++; setTimeout(() => { if (!this.closed) this.connect(); }, 1500); }
    };
    ws.onerror = () => this.setStatus("error");
  }

  setStatus(t) { this.status.textContent = t; }

  fit() {
    try { this.fitAddon.fit(); this.sendResize(); } catch (_) {}
  }
  sendResize() {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: "resize", rows: this.term.rows, cols: this.term.cols }));
    }
  }

  close() {
    this.closed = true;
    try { this.ws && this.ws.close(); } catch (_) {}
    try { this.term.dispose(); } catch (_) {}
    this.el.remove();
    panes.delete(this.id);
    renderGrid();
    refreshSessions();
  }
}

// --- grid --------------------------------------------------------------------
function renderGrid() {
  const grid = $("grid");
  grid.className = "cols-" + cols;
  if (panes.size === 0) {
    grid.classList.add("empty");
    if (!grid.querySelector(".placeholder")) {
      const p = document.createElement("div");
      p.className = "placeholder";
      p.textContent = "Spawn or open a session — panes appear here";
      grid.replaceChildren(p);
    }
    return;
  }
  const ph = grid.querySelector(".placeholder");
  if (ph) ph.remove();
  // fit all panes after the grid reflows
  requestAnimationFrame(() => panes.forEach((p) => p.fit()));
}

function openSession(id) {
  if (panes.has(id)) { panes.get(id).term.focus(); return; }
  const pane = new Pane(id);
  panes.set(id, pane);
  $("grid").appendChild(pane.el);
  renderGrid();
  refreshSessions();
}

function setLayout(n) {
  cols = n;
  for (const b of $("layouts").children) b.classList.toggle("active", Number(b.dataset.cols) === n);
  renderGrid();
}
$("layouts").addEventListener("click", (e) => {
  if (e.target.dataset && e.target.dataset.cols) setLayout(Number(e.target.dataset.cols));
});

// --- sidebar: session list ---------------------------------------------------
function emptyLi(text) {
  const li = document.createElement("li");
  li.className = "empty";
  li.textContent = text;
  return li;
}
function renderSession(s) {
  const li = document.createElement("li");
  if (panes.has(s.id)) li.classList.add("open");
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = s.id;
  name.onclick = () => openSession(s.id);
  li.appendChild(name);
  if (s.exited) {
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = "exited";
    li.appendChild(badge);
  }
  const kill = document.createElement("button");
  kill.className = "kill";
  kill.title = "Kill session";
  kill.textContent = "×";
  kill.onclick = (ev) => { ev.stopPropagation(); killSession(s.id); };
  li.appendChild(kill);
  return li;
}
async function refreshSessions() {
  const ul = $("sessions");
  let sessions;
  try { sessions = await api("/api/sessions"); }
  catch (e) { ul.replaceChildren(emptyLi("Error: " + e.message)); return; }
  if (!sessions || sessions.length === 0) { ul.replaceChildren(emptyLi("No sessions yet")); return; }
  ul.replaceChildren(...sessions.map(renderSession));
}

// --- spawn / kill ------------------------------------------------------------
async function loadAgents() {
  try {
    const agents = await api("/api/agents");
    $("agent").replaceChildren(...(agents || []).map((a) => {
      const opt = document.createElement("option");
      opt.value = a; opt.textContent = a;
      return opt;
    }));
  } catch (_) {}
}
$("spawn").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    const s = await api("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ agent: $("agent").value, cwd: $("cwd").value.trim() }),
    });
    await refreshSessions();
    openSession(s.id);
  } catch (e) { alert("Spawn failed: " + e.message); }
});
async function killSession(id) {
  try { await api("/api/sessions/" + encodeURIComponent(id), { method: "DELETE" }); }
  catch (e) { alert("Kill failed: " + e.message); return; }
  if (panes.has(id)) panes.get(id).close();
  refreshSessions();
}

// --- boot --------------------------------------------------------------------
window.addEventListener("resize", () => panes.forEach((p) => p.fit()));
loadAgents();
refreshSessions();
setInterval(refreshSessions, 3000);
