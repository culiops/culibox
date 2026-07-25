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

// sessionStatus returns the server-side status of a session ("live" | "exited" |
// "detached"), or null if it is no longer listed (gone). On a fetch error it
// returns "live" (assume a transient drop → keep reconnecting, never falsely end).
async function sessionStatus(id) {
  try {
    const list = await api("/api/sessions");
    const s = (list || []).find((x) => x.id === id);
    return s ? s.status : null;
  } catch (_) {
    return "live";
  }
}

// Grid + tabs live in tabs.js; terminals in terminal-tab.js; viewers in
// viewer-tab.js; the file tree in filetree.js. This file owns boot and the
// sidebar (spawn form, profile editor, session list).

// Sidebar toggle: fully hide the sidebar so the terminal fills the width. State
// persists across reloads (applied pre-paint by the inline <head> script).
$("sidebar-toggle").onclick = () => {
  const hidden = document.documentElement.classList.toggle("sidebar-hidden");
  try { localStorage.setItem("sidebarHidden", hidden ? "1" : "0"); } catch (_) {}
  renderGrid(); // reflow/refit terminals to the new width
};

// Copy/paste hint — platform-aware, dismissable (persisted). Lives outside the
// sidebar so it survives the sidebar-hide toggle.
(function initHint() {
  const hint = $("hint");
  if (!hint) return;
  if (localStorage.getItem("hintDismissed") === "1") { hint.classList.add("hidden"); return; }
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
  // The select-inside-a-TUI bypass key is platform-specific: Option on macOS,
  // Shift elsewhere (see macOptionClickForcesSelection in terminal-tab.js).
  $("hint-text").textContent = `Select text to copy · ${mac ? "⌘V" : "Ctrl+V"} to paste · ${mac ? "⌥+drag" : "Shift+drag"} to select inside TUIs (e.g. claude)`;
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
    openTerminal(s.id, s.name);
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
  closeTerminal(id);
  await refreshSessions(); // session gone → row removed
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
loadProfiles();
loadWorkspaces();
loadOptionDefs().then(newProfile);
refreshSessions();
initFileTree();
fontPrefs.applyCssVars();
initSettings();
setInterval(refreshSessions, 3000);

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
  const tab = focusedCell && focusedCell.activeTab;
  const canZoom = tab && (tab.kind === "terminal" || (tab.kind === "viewer" && tab.zoomVar));
  if (!canZoom) return;
  e.preventDefault();
  e.stopPropagation();
  if (doReset) tab.resetZoom?.();
  else tab.zoom?.(delta);
}, true);
