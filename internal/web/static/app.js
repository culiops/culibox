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

// copyText copies to the clipboard. The app is served over HTTP (not a secure
// context), so navigator.clipboard is usually unavailable — fall back to a
// hidden off-screen textarea + execCommand("copy"), which works over plain HTTP.
// `done` restores terminal focus (the textarea steals it). window.__lastCopied
// is a test hook.
function copyText(text, done) {
  window.__lastCopied = text;
  const finish = () => { if (done) done(); };
  if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(finish, () => { execCopy(text); finish(); });
    return;
  }
  execCopy(text);
  finish();
}
function execCopy(text) {
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed"; ta.style.top = "0"; ta.style.left = "0"; ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.focus(); ta.select();
  try { document.execCommand("copy"); } catch (_) { /* best effort */ }
  document.body.removeChild(ta);
}

// sessionIsLive reports whether the server still has this session running, used to
// tell a real session end from a transient WS drop. On error, assume transient
// (return true) so we never falsely "end" a live session.
async function sessionIsLive(id) {
  try {
    const list = await api("/api/sessions");
    const s = (list || []).find((x) => x.id === id);
    return !!s && s.status !== "exited";
  } catch (_) {
    return true;
  }
}

const panes = new Map(); // id -> Pane
let cols = 1;

// --- Pane: one xterm.js terminal bound to one session's /ws/{id} -------------
class Pane {
  constructor(id, name) {
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
    pid.textContent = name || id;
    this.pid = pid;
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
    // Copy the selection with Ctrl+Shift+C (Linux/Win) or ⌘C (Mac). The canvas
    // renderer's selection isn't a DOM selection, so native copy can't see it.
    this.term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown" || e.code !== "KeyC") return true;
      const isCopy = (e.ctrlKey && e.shiftKey) || e.metaKey;
      if (!isCopy) return true;              // plain Ctrl+C must still send SIGINT
      const sel = this.term.getSelection();
      if (!sel) return true;                 // nothing selected → don't clobber clipboard
      e.preventDefault();                    // suppress DevTools (Ctrl+Shift+C) / native copy
      copyText(sel, () => this.term.focus());
      return false;
    });

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
      // Tell a real session end from a transient drop (daemon restart / blip): if
      // the server no longer has the session live, stop retrying and mark ended,
      // rather than looping on "reconnecting…" against a dead session.
      sessionIsLive(this.id).then((alive) => {
        if (this.closed) return;
        if (!alive) { this.markEnded(); return; }
        // Only a connection that stayed up a while counts as healthy and refills
        // the retry budget. An immediate open→close keeps counting, so we give up
        // after 5 tries instead of looping for the whole retention window.
        if (this.openedAt && Date.now() - this.openedAt > 3000) this.retries = 0;
        this.openedAt = 0;
        if (this.retries < 5) {
          this.retries++;
          this.setStatus("reconnecting…");
          setTimeout(() => { if (!this.closed) this.connect(); }, 1500);
        } else {
          this.setStatus("disconnected");
        }
      });
    };
    ws.onerror = () => this.setStatus("error");
  }

  setStatus(t) { this.status.textContent = t; }
  setTitle(name) { this.pid.textContent = name || this.id; }

  fit() {
    try { this.fitAddon.fit(); this.sendResize(); } catch (_) {}
  }
  sendResize() {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: "resize", rows: this.term.rows, cols: this.term.cols }));
    }
  }

  // markEnded: the session is gone for good — stop reconnecting and show a clear
  // "session ended" banner with a Close button (the final output stays visible).
  markEnded() {
    if (this.ended) return;
    this.ended = true;
    this.setStatus("ended");
    const bar = document.createElement("div");
    bar.className = "pane-ended";
    const msg = document.createElement("span");
    msg.textContent = "session ended";
    const btn = document.createElement("button");
    btn.textContent = "Close";
    btn.onclick = () => this.close();
    bar.append(msg, btn);
    this.el.insertBefore(bar, this.el.querySelector(".pane-term"));
    this.fit(); // the banner shrank the terminal area — reflow to the new height
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

function openSession(id, name) {
  if (panes.has(id)) { panes.get(id).term.focus(); return; }
  const pane = new Pane(id, name);
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

// Sidebar toggle: fully hide the sidebar so the terminal fills the width. State
// persists across reloads (applied pre-paint by the inline <head> script).
$("sidebar-toggle").onclick = () => {
  const hidden = document.documentElement.classList.toggle("sidebar-hidden");
  try { localStorage.setItem("sidebarHidden", hidden ? "1" : "0"); } catch (_) {}
  renderGrid(); // reflow/refit terminals to the new width
};

// --- sidebar: session list ---------------------------------------------------
function emptyLi(text) {
  const li = document.createElement("li");
  li.className = "empty";
  li.textContent = text;
  return li;
}
// killing holds ids whose teardown is in flight, so the killing state is
// re-applied on every poll re-render (not just at click time).
const killing = new Set();

function renderSession(s) {
  const li = document.createElement("li");
  if (panes.has(s.id)) li.classList.add("open");
  li.title = s.id; // tiebreaker when two labels match
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = s.name || s.id;
  if (killing.has(s.id)) {
    // teardown in flight (can take a few seconds) — disabled row + "killing…"
    li.classList.add("killing");
    const st = document.createElement("span");
    st.className = "badge";
    st.textContent = "killing…";
    li.append(name, st);
    return li;
  }
  name.onclick = () => openSession(s.id, s.name);
  name.ondblclick = (e) => { e.stopPropagation(); startRename(li, name, s); };
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
// editing guards the inline rename: while true, the background poll must NOT
// re-render the list (that would destroy the input + focus) and a stray click
// must not open a pane.
let editing = false;

// startRename swaps the name span for an input; Enter/blur commit, Esc cancels.
// A single finish(commit) guarded by `finished` prevents Enter+blur double-commit
// and Esc+blur commit-on-cancel.
function startRename(li, span, s) {
  if (editing) return;
  editing = true;
  const cur = s.name || s.id;
  const input = document.createElement("input");
  input.type = "text";
  input.className = "ren-input";
  input.value = cur;
  let finished = false;
  const finish = async (commit) => {
    if (finished) return;
    finished = true;
    editing = false;
    const v = input.value.trim();
    if (commit && v && v !== cur) {
      try {
        await api("/api/sessions/" + encodeURIComponent(s.id), {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: v }),
        });
        panes.get(s.id)?.setTitle(v);
      } catch (e) { alert("Rename failed: " + e.message); }
    }
    refreshSessions();
  };
  input.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); finish(true); }
    else if (e.key === "Escape") { e.preventDefault(); finish(false); }
  };
  input.onblur = () => finish(true);
  li.replaceChild(input, span);
  input.focus();
  input.select();
}

async function refreshSessions() {
  if (editing) return; // don't clobber an in-progress inline rename
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

// --- profile editor (structured create / edit / delete of runtime profiles) --
let optionDefs = [];
async function loadOptionDefs() {
  try { optionDefs = (await api("/api/options")) || []; } catch (_) {}
}
function renderPfOptions(current) {
  current = current || {};
  $("pf-options").replaceChildren(...optionDefs.map((o) => {
    const row = document.createElement("div");
    row.className = "pf-row";
    const lbl = document.createElement("label");
    lbl.textContent = o.label || o.key;
    let ctrl;
    if (o.type === "bool") {
      ctrl = document.createElement("input");
      ctrl.type = "checkbox";
      ctrl.checked = current[o.key] === true;
    } else {
      ctrl = document.createElement("select");
      const none = document.createElement("option");
      none.value = ""; none.textContent = "(not set)";
      ctrl.appendChild(none);
      for (const c of o.choices || []) {
        const co = document.createElement("option");
        co.value = c; co.textContent = c;
        if (current[o.key] === c) co.selected = true;
        ctrl.appendChild(co);
      }
    }
    ctrl.dataset.optkey = o.key;
    ctrl.dataset.opttype = o.type;
    row.append(lbl, ctrl);
    return row;
  }));
}
function fillForm(p) {
  $("pf-name").value = p.name || "";
  $("pf-bin").value = p.bin || "";
  $("pf-base").value = p.base || "inherit";
  $("pf-args").value = (p.args || []).join("\n");
  $("pf-env").value = Object.entries(p.env || {}).map(([k, v]) => k + "=" + v).join("\n");
  $("pf-secrets").value = (p.secrets || []).join(", ");
  renderPfOptions(p.options || {});
}
function newProfile() { fillForm({ base: "inherit" }); $("pf-name").focus(); }
function readForm() {
  const p = { name: $("pf-name").value.trim(), bin: $("pf-bin").value.trim(), base: $("pf-base").value };
  const args = $("pf-args").value.split("\n").map((s) => s.trim()).filter(Boolean);
  if (args.length) p.args = args;
  const env = {};
  for (const line of $("pf-env").value.split("\n")) {
    const t = line.trim(); if (!t) continue;
    const i = t.indexOf("="); if (i < 0) continue;
    env[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  if (Object.keys(env).length) p.env = env;
  const secrets = $("pf-secrets").value.split(",").map((s) => s.trim()).filter(Boolean);
  if (secrets.length) p.secrets = secrets;
  const opts = {};
  for (const ctrl of $("pf-options").querySelectorAll("[data-optkey]")) {
    if (ctrl.dataset.opttype === "bool") opts[ctrl.dataset.optkey] = ctrl.checked;
    else if (ctrl.value) opts[ctrl.dataset.optkey] = ctrl.value;
  }
  if (Object.keys(opts).length) p.options = opts;
  return p;
}
async function saveProfile() {
  const p = readForm();
  if (!p.name || !p.bin) { alert("Name and Command are required"); return; }
  const editing = profiles.some((x) => x.name === p.name && x.editable);
  const path = editing ? "/api/profiles/" + encodeURIComponent(p.name) : "/api/profiles";
  try {
    await api(path, { method: editing ? "PUT" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(p) });
  } catch (e) { alert("Save failed: " + e.message); return; }
  await loadProfiles();
}
async function editProfile(name) {
  try { fillForm(await api("/api/profiles/" + encodeURIComponent(name))); $("manage").open = true; }
  catch (e) { alert("Load failed: " + e.message); }
}
async function deleteProfile(name) {
  try { await api("/api/profiles/" + encodeURIComponent(name), { method: "DELETE" }); }
  catch (e) { alert("Delete failed: " + e.message); return; }
  await loadProfiles();
}
function renderEditable() {
  $("editable").replaceChildren(...profiles.filter((p) => p.editable).map((p) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name"; name.textContent = p.name; name.title = "Edit"; name.style.cursor = "pointer";
    name.onclick = () => editProfile(p.name);
    const del = document.createElement("button");
    del.className = "del"; del.textContent = "×"; del.title = "Delete profile";
    del.onclick = () => deleteProfile(p.name);
    li.append(name, del);
    return li;
  }));
}
$("pf-save").addEventListener("click", saveProfile);
$("pf-new").addEventListener("click", newProfile);
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
      body: JSON.stringify({ agent: $("agent").value, name: $("sess-name").value.trim(), cwd: getCwd(), options: getOptions() }),
    });
    $("sess-name").value = "";
    await refreshSessions();
    openSession(s.id, s.name);
  } catch (e) { alert("Spawn failed: " + e.message); }
});
async function killSession(id) {
  if (killing.has(id)) return; // already tearing down — ignore repeat clicks
  killing.add(id);
  await refreshSessions(); // re-render → row shows "killing…"
  try {
    await api("/api/sessions/" + encodeURIComponent(id), { method: "DELETE" });
  } catch (e) {
    killing.delete(id);
    await refreshSessions();
    alert("Kill failed: " + e.message);
    return;
  }
  killing.delete(id);
  if (panes.has(id)) panes.get(id).close();
  await refreshSessions(); // session gone → row removed
}

// --- boot --------------------------------------------------------------------
window.addEventListener("resize", () => panes.forEach((p) => p.fit()));
loadProfiles();
loadWorkspaces();
loadOptionDefs().then(newProfile);
refreshSessions();
setInterval(refreshSessions, 3000);
