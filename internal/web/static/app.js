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
    this.openedAt = 0;

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
    ws.onopen = () => { this.openedAt = Date.now(); this.setStatus("connected"); this.sendResize(); this.term.focus(); };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") this.term.write(ev.data);
      else this.term.write(new Uint8Array(ev.data));
    };
    ws.onclose = () => {
      if (this.closed) return;
      // Only a connection that stayed up a while counts as healthy and refills
      // the retry budget. An immediate open→close (e.g. attaching to an exited
      // session) keeps counting, so we give up after 5 tries instead of looping
      // every 1.5s for the whole retention window.
      if (this.openedAt && Date.now() - this.openedAt > 3000) this.retries = 0;
      this.openedAt = 0;
      if (this.retries < 5) {
        this.retries++;
        this.setStatus("reconnecting…");
        setTimeout(() => { if (!this.closed) this.connect(); }, 1500);
      } else {
        this.setStatus("disconnected");
      }
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
let profiles = [];
async function loadProfiles() {
  try {
    profiles = (await api("/api/profiles")) || [];
    $("agent").replaceChildren(...profiles.map((p) => {
      const o = document.createElement("option");
      o.value = p.name; o.textContent = p.name;
      return o;
    }));
    renderOptions();
    renderEditable();
  } catch (_) {}
}
function currentProfile() {
  return profiles.find((p) => p.name === $("agent").value);
}
// renderOptions builds a toggle per option the selected profile exposes
// (checkbox for bool, select for enum), pre-set to the profile's default.
function renderOptions() {
  const p = currentProfile();
  const opts = (p && p.options) || [];
  $("options").replaceChildren(...opts.map((o) => {
    const row = document.createElement("div");
    row.className = "opt";
    const lbl = document.createElement("label");
    lbl.textContent = o.label || o.key;
    lbl.htmlFor = "opt-" + o.key;
    let ctrl;
    if (o.type === "bool") {
      ctrl = document.createElement("input");
      ctrl.type = "checkbox";
      ctrl.checked = o.value === true;
    } else {
      ctrl = document.createElement("select");
      for (const c of o.choices || []) {
        const co = document.createElement("option");
        co.value = c; co.textContent = c;
        if (c === o.value) co.selected = true;
        ctrl.appendChild(co);
      }
    }
    ctrl.id = "opt-" + o.key;
    ctrl.dataset.key = o.key;
    ctrl.dataset.type = o.type;
    row.append(lbl, ctrl);
    return row;
  }));
}
function getOptions() {
  const out = {};
  for (const ctrl of $("options").querySelectorAll("[data-key]")) {
    out[ctrl.dataset.key] = ctrl.dataset.type === "bool" ? ctrl.checked : ctrl.value;
  }
  return out;
}
$("agent").addEventListener("change", renderOptions);

// --- profile management (create / edit / delete runtime profiles) -----------
const PROFILE_TEMPLATE = JSON.stringify({ name: "my-profile", bin: "claude", base: "inherit", args: [] }, null, 2);
function renderEditable() {
  const ul = $("editable");
  ul.replaceChildren(...profiles.filter((p) => p.editable).map((p) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name"; name.textContent = p.name;
    const del = document.createElement("button");
    del.className = "del"; del.textContent = "×"; del.title = "Delete profile";
    del.onclick = () => deleteProfile(p.name);
    li.append(name, del);
    return li;
  }));
}
async function saveProfile() {
  let p;
  try { p = JSON.parse($("profile-json").value.trim()); }
  catch (e) { alert("Invalid JSON: " + e.message); return; }
  if (!p || !p.name) { alert("Profile needs a name"); return; }
  const editing = profiles.some((x) => x.name === p.name && x.editable);
  const path = editing ? "/api/profiles/" + encodeURIComponent(p.name) : "/api/profiles";
  try {
    await api(path, { method: editing ? "PUT" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(p) });
  } catch (e) { alert("Save failed: " + e.message); return; }
  await loadProfiles();
}
async function deleteProfile(name) {
  try { await api("/api/profiles/" + encodeURIComponent(name), { method: "DELETE" }); }
  catch (e) { alert("Delete failed: " + e.message); return; }
  await loadProfiles();
}
$("save-profile").addEventListener("click", saveProfile);
$("profile-json").value = PROFILE_TEMPLATE;
async function loadWorkspaces() {
  try {
    const ws = await api("/api/workspaces"); // [{label, path}] = $HOME + its subfolders
    const opts = (ws || []).map((w) => {
      const o = document.createElement("option");
      o.value = w.path; o.textContent = w.label;
      return o;
    });
    const custom = document.createElement("option");
    custom.value = "__custom__"; custom.textContent = "Custom path…";
    opts.push(custom);
    $("workspace").replaceChildren(...opts);
  } catch (_) {}
}
$("workspace").addEventListener("change", () => {
  $("cwd-custom").style.display = $("workspace").value === "__custom__" ? "block" : "none";
});
function getCwd() {
  const w = $("workspace").value;
  return w === "__custom__" ? $("cwd-custom").value.trim() : (w || "");
}

$("spawn").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    const s = await api("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ agent: $("agent").value, cwd: getCwd(), options: getOptions() }),
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
loadProfiles();
loadWorkspaces();
refreshSessions();
setInterval(refreshSessions, 3000);
