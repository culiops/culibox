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

// PROBE_MS bounds every request on the reconnect path. A page whose fetches are
// wedged (e.g. behind an unanswered basic-auth challenge) leaves promises pending
// forever; without a deadline that silently killed the reconnect loop.
const PROBE_MS = 4000;

// timedFetch is fetch with a hard deadline. AbortSignal.timeout is not in every
// browser this may be opened from, so fall back to an AbortController + timer.
function timedFetch(path, ms) {
  if (AbortSignal.timeout) return fetch(path, { signal: AbortSignal.timeout(ms) });
  const ac = new AbortController();
  const t = setTimeout(() => ac.abort(), ms);
  return fetch(path, { signal: ac.signal }).finally(() => clearTimeout(t));
}

// sessionStatus returns the server-side status of a session ("live" | "exited" |
// "detached"), null if it is no longer listed (gone), or "unknown" when the server
// could not be asked. "unknown" must NOT be confused with null: only a definite
// "this session is not listed" may end a terminal.
async function sessionStatus(id) {
  try {
    const res = await timedFetch("/api/sessions", PROBE_MS);
    if (!res.ok) return "unknown";
    const list = await res.json();
    const s = (list || []).find((x) => x.id === id);
    return s ? s.status : null;
  } catch (_) {
    return "unknown";
  }
}

// probeServer classifies why we cannot reach the socket: "ok" (server answered),
// "unauthorized" (the login/cookie is stale — a reload is the cure), "unreachable"
// (down, or this page's requests are wedged). A successful probe also re-mints the
// WS cookie server-side, so a stale-cookie tab heals itself here.
async function probeServer() {
  try {
    const res = await timedFetch("/api/meta", PROBE_MS);
    if (res.status === 401 || res.status === 403) return "unauthorized";
    return res.ok ? "ok" : "unreachable";
  } catch (_) {
    return "unreachable";
  }
}

// Grid + tabs live in tabs.js; terminals in terminal-tab.js; viewers in
// viewer-tab.js; the file tree in filetree.js. This file owns boot and the
// sidebar (spawn form, profile editor, session list).

// Sidebar toggle: fully hide the sidebar so the terminal fills the width. State
// persists across reloads (applied pre-paint by the inline <head> script).
// Named so the command palette can invoke the same path as the ☰ button rather
// than duplicating it (or synthesising a click on the DOM node).
function toggleSidebar() {
  const hidden = document.documentElement.classList.toggle("sidebar-hidden");
  try { localStorage.setItem("sidebarHidden", hidden ? "1" : "0"); } catch (_) {}
  renderGrid(); // reflow/refit terminals to the new width
}
$("sidebar-toggle").onclick = toggleSidebar;

// Copy/paste hint — platform-aware, dismissable (persisted). Lives outside the
// sidebar so it survives the sidebar-hide toggle.
(function initHint() {
  const hint = $("hint");
  if (!hint) return;
  if (localStorage.getItem("hintDismissed") === "1") { hint.classList.add("hidden"); return; }
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
  // The select-inside-a-TUI bypass key is platform-specific: Option on macOS,
  // Shift elsewhere (see macOptionClickForcesSelection in terminal-tab.js).
  // Non-mac uses Ctrl+Shift+K, not Ctrl+K: Ctrl+K is readline's kill-to-end-of-line
  // inside the shells and TUIs these panes host (see initPalette).
  $("hint-text").textContent = `${mac ? "⌘K" : "Ctrl+Shift+K"} for commands · Select text to copy · ${mac ? "⌘V" : "Ctrl+V"} to paste · ${mac ? "⌥+drag" : "Shift+drag"} to select inside TUIs (e.g. claude)`;
  $("hint-x").onclick = () => {
    hint.classList.add("hidden");
    try { localStorage.setItem("hintDismissed", "1"); } catch (_) {}
  };
})();

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
  if (terminalOpen(s.id)) li.classList.add("open");
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
  name.onclick = () => openTerminal(s.id, s.name);
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
        retitleTerminal(s.id, v);
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

// lastSessions caches the most recent poll so the command palette can list
// sessions without issuing its own request on every keystroke.
let lastSessions = [];

// Returns the live session list, or null if the request failed — the boot-time
// restore must be able to tell "the server says there are none" (a valid answer:
// restore nothing) from "the server could not be asked" (retry, restore nothing
// yet). An empty array is the former.
async function refreshSessions() {
  if (editing) return lastSessions; // don't clobber an in-progress inline rename
  const ul = $("sessions");
  let sessions;
  try { sessions = await api("/api/sessions"); }
  catch (e) { ul.replaceChildren(emptyLi("Error: " + e.message)); return null; }
  lastSessions = sessions || [];
  const cnt = $("sessions-count");
  if (cnt) cnt.textContent = String(lastSessions.length);
  if (lastSessions.length === 0) { ul.replaceChildren(emptyLi("No sessions yet")); return lastSessions; }
  ul.replaceChildren(...lastSessions.map(renderSession));
  return lastSessions;
}

// --- spawn / kill ------------------------------------------------------------
let profiles = [];
// profilesLoaded distinguishes "the server has no profiles" from "we could not
// ask". Without it, one transient failure at boot leaves the primary spawn
// button disabled under a caption that blames the config — and in dev, `air`
// restarts the daemon under an open tab routinely. Retries once, like initEnv.
let profilesLoaded = false;
async function loadProfiles(retry = true) {
  try {
    profiles = (await api("/api/profiles")) || [];
    profilesLoaded = true;
    $("agent").replaceChildren(...profiles.map((p) => {
      const o = document.createElement("option");
      o.value = p.name; o.textContent = p.name;
      return o;
    }));
    renderOptions();
    renderEditable();
    updateSpawnTarget(); // the one-click button's caption depends on this list
  } catch (_) {
    updateSpawnTarget();
    if (retry) setTimeout(() => loadProfiles(false), 2000);
  }
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
// optApplies mirrors config.OptionDef.AppliesTo: no bins → all agents; else match
// the bin or its basename. Keeps the editor's option list scoped to the agent.
function optApplies(o, bin) {
  if (!o.bins || o.bins.length === 0) return true;
  const base = (bin || "").split("/").pop();
  return o.bins.includes(bin) || o.bins.includes(base);
}
function renderPfOptions(current) {
  current = current || {};
  const bin = $("pf-bin").value.trim();
  $("pf-options").replaceChildren(...optionDefs.filter((o) => optApplies(o, bin)).map((o) => {
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
// The editor lives in a closed <dialog> at boot, where focus() is a silent
// no-op; only grab focus when the dialog is actually on screen.
function newProfile() {
  fillForm({ base: "inherit" });
  if ($("settings-modal").open) $("pf-name").focus();
}
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
  try { fillForm(await api("/api/profiles/" + encodeURIComponent(name))); openSettings("manage"); }
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
// Re-scope the option list when the agent binary changes.
$("pf-bin").addEventListener("input", () => renderPfOptions(readForm().options));
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

// --- the "+ New session" split button ----------------------------------------
// Spawning is the most frequent action in this app, so the primary control does
// it in one click using the last-used profile and working dir. The caption below
// the button is the safety rail: an instant-spawn button whose target is
// invisible is a trap, so what it will do is always written out.
const LAST_SPAWN_KEY = "agent-console:lastSpawn";

function loadLastSpawn() {
  try {
    const v = JSON.parse(localStorage.getItem(LAST_SPAWN_KEY) || "null");
    if (v && typeof v.agent === "string") return v;
  } catch (_) {}
  return null;
}

// spawnTarget resolves what a one-click spawn does right now. The stored profile
// is validated against the live list, so one deleted from under the button can
// never wedge it.
function spawnTarget() {
  const last = loadLastSpawn();
  const valid = last && profiles.some((p) => p.name === last.agent);
  return {
    agent: valid ? last.agent : (profiles[0] && profiles[0].name) || "",
    cwd: (valid && last.cwd) || "",
    options: (valid && last.options) || {},
  };
}

// shortPath renders an absolute path as ~/… when it sits under home.
function shortPath(p) {
  const home = window.HOME_DIR || "";
  return home && p.indexOf(home) === 0 ? "~" + p.slice(home.length) : p;
}

function updateSpawnTarget() {
  const el = $("spawn-target"), btn = $("spawn-go");
  if (!el || !btn) return;
  const t = spawnTarget();
  if (!t.agent) {
    // Never blame the config for a failed request — say which one it is.
    el.textContent = profilesLoaded ? "no profiles configured" : "profiles unavailable — retrying";
    el.title = "";
    btn.disabled = true;
    return;
  }
  // Stay disabled while a one-click spawn is in flight, so the button does not
  // briefly look clickable while the guard silently swallows the click.
  btn.disabled = quickSpawning;
  const where = t.cwd ? (t.cwd.replace(/\/$/, "").split("/").pop() || t.cwd) : "default dir";
  el.textContent = t.agent + " · " + where;
  el.title = t.agent + " in " + (t.cwd ? shortPath(t.cwd) : "the server's default working dir");
  btn.title = "Spawn " + el.title;
}

// spawnSession is the single POST path for every *agent* spawn entry point (one
// click, the ▾ menu, the advanced form, the palette) so "last used" and the undo
// toast are recorded in exactly one place. spawnShellIn deliberately does NOT
// route through here: a quick shell must never become what "+ New session" does.
async function spawnSession(req, opts) {
  opts = opts || {};
  const s = await api("/api/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
  try {
    localStorage.setItem(LAST_SPAWN_KEY, JSON.stringify({
      agent: req.agent, cwd: req.cwd || "", options: req.options || {},
    }));
  } catch (_) {}
  updateSpawnTarget();
  await refreshSessions();
  openTerminal(s.id, s.name);
  if (opts.toast !== false) {
    // Report the directory the SERVER chose, not the one requested: a one-click
    // spawn usually sends an empty cwd and lets default_cwd decide, and "the
    // default dir" tells the user nothing about where their agent just landed.
    const where = s.cwd || req.cwd;
    showToast('Spawned "' + (s.name || s.id) + '" in ' + (where ? shortPath(where) : "the default dir"),
      { undo: () => killSession(s.id) });
  }
  return s;
}

let quickSpawning = false;
async function quickSpawn(agentName) {
  if (quickSpawning) return;
  const t = spawnTarget();
  const agent = agentName || t.agent;
  if (!agent) { showToast("No profiles configured", { error: true }); return; }
  // Stored options belong to the profile they were captured from; carrying them
  // onto a different profile would apply flags it never declared.
  const options = agent === t.agent ? t.options : {};
  quickSpawning = true;
  $("spawn-go").disabled = true;
  try {
    await spawnSession({ agent, name: "", cwd: t.cwd, options });
  } catch (e) {
    showToast("Spawn failed: " + e.message, { error: true });
  } finally {
    quickSpawning = false;
    updateSpawnTarget(); // re-enables the button
  }
}

function closeSpawnMenu() { $("spawn-menu").classList.remove("open"); }

function openSpawnMenu() {
  const menu = $("spawn-menu"), anchor = $("spawn-more");
  const rows = profiles.map((p) => {
    const b = document.createElement("button");
    b.type = "button"; b.className = "mono"; b.textContent = p.name;
    b.onclick = () => { closeSpawnMenu(); quickSpawn(p.name); };
    return b;
  });
  if (!rows.length) {
    const b = document.createElement("button");
    b.type = "button"; b.textContent = "No profiles yet"; b.disabled = true;
    rows.push(b);
  }
  const sep = document.createElement("div");
  sep.className = "sep";
  const adv = document.createElement("button");
  adv.type = "button"; adv.textContent = "Advanced…";
  adv.onclick = () => { closeSpawnMenu(); openSpawnDialog(); };
  menu.replaceChildren(...rows, sep, adv);
  menu.classList.add("open"); // must be visible before it can be measured
  const r = anchor.getBoundingClientRect();
  const w = menu.offsetWidth;
  menu.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, r.right - w)) + "px";
  menu.style.top = (r.bottom + 6) + "px";
}

// openSpawnDialog preselects whatever the one-click button would have done, so
// "Advanced…" elaborates the visible target instead of presenting a fresh form.
function openSpawnDialog() {
  const t = spawnTarget();
  if (t.agent) { $("agent").value = t.agent; renderOptions(); }
  const ws = $("workspace");
  if (t.cwd && Array.prototype.some.call(ws.options, (o) => o.value === t.cwd)) ws.value = t.cwd;
  $("cwd-custom").style.display = ws.value === "__custom__" ? "block" : "none";
  openDialog("spawn-modal");
  $("sess-name").focus();
}

function initSpawnControl() {
  $("spawn-go").onclick = () => quickSpawn();
  $("spawn-more").onclick = (e) => {
    e.stopPropagation();
    if ($("spawn-menu").classList.contains("open")) closeSpawnMenu();
    else openSpawnMenu();
  };
  document.addEventListener("click", (e) => {
    const menu = $("spawn-menu");
    if (menu.classList.contains("open") && !menu.contains(e.target)) closeSpawnMenu();
  });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeSpawnMenu(); });
  updateSpawnTarget();
}

$("spawn").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const req = { agent: $("agent").value, name: $("sess-name").value.trim(), cwd: getCwd(), options: getOptions() };
  // Close first: a fixed-position toast renders *beneath* a modal dialog's
  // backdrop, so an Undo offered while the dialog is still up is unreachable.
  closeDialog("spawn-modal");
  $("sess-name").value = "";
  try {
    await spawnSession(req);
  } catch (e) { showToast("Spawn failed: " + e.message, { error: true }); }
});
// confirmKill is the guarded entry point (tab ⏻ button, command palette).
// killSession itself stays unguarded: the sidebar × has always been immediate,
// and Undo on a fresh spawn must not ask twice.
function confirmKill(id, name) {
  if (confirm('Kill session "' + (name || id) + '"? This cannot be undone.')) killSession(id);
}

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
  closeTerminal(id);
  await refreshSessions(); // session gone → row removed
}

// --- quick shell -------------------------------------------------------------
// Spawn the detected shell profile in a cell's working dir, then hand off to the
// normal openTerminal path so auto-split/cap placement stays in one place.
// Per-cell (not global) in-flight guard: a double click on one window is ignored,
// but +sh on another window still works instead of dying silently.
const shellSpawning = new WeakSet();
async function spawnShellIn(cell) {
  if (!window.shellProfile || !cell || shellSpawning.has(cell)) return;
  shellSpawning.add(cell);
  try {
    // Re-read the profile name instead of trusting the boot-time cache: the server
    // withholds it once a stored profile takes over the seed's name, and profiles
    // can be created after this page loaded.
    const profile = await currentShellProfile();
    if (!profile) { alert("No shell profile available — check the console log."); return; }
    const cwd = await resolveCellCwd(cell);
    // Always prefix the label: with no cwd the server would name the session after
    // default_cwd's basename, which reads exactly like an agent session.
    const base = cwd.replace(/\/$/, "").split("/").pop();
    const s = await api("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ agent: profile, cwd, name: "sh:" + (base || profile) }),
    });
    await refreshSessions();
    // The cell can be destroyed while we await (applyCap collapses windows, a pane
    // can be removed). Attaching to a detached cell would leak a live xterm + socket
    // that is never rendered, so fall back to normal placement instead.
    if (cells.includes(cell)) setFocusedCell(cell);
    openTerminal(s.id, s.name);
  } catch (e) {
    alert("Shell failed: " + e.message);
  } finally {
    shellSpawning.delete(cell);
  }
}

// resolveCellCwd falls back to the server when a terminal tab exists but hasn't
// resolved its cwd yet (TerminalTab fetches it asynchronously). Without this, a
// +sh right after opening a session silently lands in default_cwd instead of the
// session's directory — the failure the feature exists to prevent.
async function resolveCellCwd(cell) {
  const known = cellCwd(cell);
  if (known) return known;
  // Mirror cellCwd's precedence exactly — active tab first, then newest backwards.
  // Picking a different terminal here would open the shell in another session's
  // directory, which is the bug this function exists to prevent.
  const act = cell.activeTab;
  let term = act && act.kind === "terminal" && act.id ? act : null;
  for (let i = cell.tabs.length - 1; !term && i >= 0; i--) {
    const t = cell.tabs[i];
    if (t.kind === "terminal" && t.id) term = t;
  }
  if (!term) return "";
  try {
    const list = await api("/api/sessions");
    const s = (list || []).find((x) => x.id === term.id);
    return (s && s.cwd) || "";
  } catch (_) {
    return "";
  }
}

// currentShellProfile re-fetches the server's verdict, falling back to the cached
// value only if the request fails (so a network blip doesn't disable the button).
async function currentShellProfile() {
  try {
    const meta = await api("/api/meta");
    window.shellProfile = (meta && meta.shell_profile) || "";
    cells.forEach((c) => c.renderBar()); // reflect a profile that appeared/disappeared
  } catch (_) { /* keep the cached value */ }
  return window.shellProfile;
}

// --- settings: font sizes ----------------------------------------------------
const FS_SURFACES = ["terminal", "code", "markdown", "filetree"];
function bumpFs(surface, delta) {
  fontPrefs.set(surface, fontPrefs.get(surface) + delta);
  const val = document.querySelector('.fs-val[data-fs="' + surface + '"]');
  if (val) val.value = fontPrefs.get(surface);
}
function initSettings() {
  for (const s of FS_SURFACES) {
    const val = document.querySelector('.fs-val[data-fs="' + s + '"]');
    if (val) val.value = fontPrefs.get(s);
  }
  document.querySelectorAll("#settings [data-fs]").forEach((el) => {
    const s = el.dataset.fs;
    if (el.classList.contains("fs-inc")) el.onclick = () => bumpFs(s, +fontPrefs.STEP);
    else if (el.classList.contains("fs-dec")) el.onclick = () => bumpFs(s, -fontPrefs.STEP);
    else if (el.classList.contains("fs-val")) el.onchange = () => { if (el.value.trim() !== "") fontPrefs.set(s, el.value); el.value = fontPrefs.get(s); };
  });
  const reset = $("fs-reset");
  if (reset) reset.onclick = () => { fontPrefs.reset(); initSettings(); };
}

// --- boot --------------------------------------------------------------------
window.addEventListener("resize", fitAll);
// Layout buttons 1/2/4 → set the max-window cap (auto-split grows up to it).
$("layouts").addEventListener("click", (e) => {
  if (e.target.dataset && e.target.dataset.cols) applyCap(Number(e.target.dataset.cols));
});
applyCap(4); // default: auto-split up to 4 windows
initDialogs();
initSplitter();
initSpawnControl();
initPalette();
loadProfiles();
loadWorkspaces();
loadOptionDefs().then(newProfile);
// Restore the grid from the last visit, once the live session list is known.
// A failed list is retried once (the loadProfiles/initEnv pattern): restoring
// against an unknown server would open tabs for sessions that may be gone, so
// on a second failure we give up on restoring and just re-arm saving.
(async function restoreBoot(retry = true) {
  const sessions = await refreshSessions();
  if (sessions === null) {
    if (retry) { setTimeout(() => restoreBoot(false), 2000); return; }
    endRestore(false); // never persist the empty boot grid over a real snapshot
    return;
  }
  restoreLayout(sessions);
})();
initFileTree();
initUpload();

// Env signal: show a colored DEV/PROD badge + top strip + tab title, so the
// active environment is unmistakable. Config-driven via /api/meta; empty label
// (no env configured) leaves the UI unchanged.
// One retry: this fetch also carries shell_profile, and a single transient failure
// would otherwise hide the +sh button for the whole page session.
(async function initEnv(retry = true) {
  try {
    const meta = await api("/api/meta");
    // Quick-shell profile first: it is independent of the env badge, so it must
    // be captured before the no-badge early return below.
    window.shellProfile = (meta && meta.shell_profile) || "";
    if (window.shellProfile) cells.forEach((c) => c.renderBar()); // reveal +sh
    if (!meta || !meta.env_label) return;
    const badge = $("env-badge"), strip = $("env-strip");
    badge.textContent = meta.env_label;
    if (meta.env_color) { badge.style.background = meta.env_color; strip.style.background = meta.env_color; }
    badge.hidden = false;
    strip.hidden = false;
    document.title = "[" + meta.env_label + "] Agent Console";
  } catch (_) {
    if (retry) setTimeout(() => initEnv(false), 2000); // else: no badge, no +sh
  }
})();

fontPrefs.applyCssVars();
initSettings();
setInterval(refreshSessions, 3000);

// Ctrl+Shift+` → new shell in the focused window. Capture phase + stopPropagation
// for the same reason as the zoom handler below: xterm listens on its textarea and
// would otherwise swallow the combo while a terminal has focus.
document.addEventListener("keydown", (e) => {
  if (!e.ctrlKey || !e.shiftKey || e.altKey || e.metaKey) return;
  if (e.code !== "Backquote") return; // .code is layout/shift independent
  // Don't steal the combo from a real text field (session name, inline rename) —
  // but xterm's own focus target IS a textarea, so exempt that one.
  const ae = document.activeElement;
  if (ae && (ae.tagName === "INPUT" ||
      (ae.tagName === "TEXTAREA" && !ae.classList.contains("xterm-helper-textarea")))) return;
  e.preventDefault();
  e.stopPropagation();
  spawnShellIn(focusedCell);
}, true);

// Live zoom the focused pane: Ctrl/Cmd + '='/'+' bigger, '-'/'_' smaller, '0' reset.
// Capture phase + stopPropagation so xterm (which listens on its textarea) never
// sees the combo; preventDefault stops the browser's own page zoom.
document.addEventListener("keydown", (e) => {
  if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
  let delta = null, doReset = false;
  if (e.key === "=" || e.key === "+") delta = +fontPrefs.STEP;
  else if (e.key === "-") delta = -fontPrefs.STEP; // not "_": Ctrl+_ is readline undo in terminals
  else if (e.key === "0") doReset = true;
  else return;
  if (!(doReset ? resetZoomFocused() : zoomFocused(delta))) return;
  e.preventDefault();
  e.stopPropagation();
}, true);

// zoomFocused / resetZoomFocused are the named zoom path, shared by the keyboard
// handler above and the command palette. Return false when the focused tab has
// nothing to zoom, so the caller can leave the key event alone.
function zoomableTab() {
  const tab = focusedCell && focusedCell.activeTab;
  return tab && (tab.kind === "terminal" || (tab.kind === "viewer" && tab.zoomVar)) ? tab : null;
}
function zoomFocused(delta) {
  const tab = zoomableTab();
  if (!tab) return false;
  tab.zoom?.(delta);
  return true;
}
function resetZoomFocused() {
  const tab = zoomableTab();
  if (!tab) return false;
  tab.resetZoom?.();
  return true;
}
