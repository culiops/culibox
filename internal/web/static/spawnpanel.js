"use strict";

// The desktop half of the New session dialog (spec 5.6): the profile list on the
// left and the "This agent will be able to" box. The decisions are
// spawnpreview.js (pure); the form itself stays app.js's. Picking a profile only
// sets #agent and fires its change event, so every existing per-profile step
// (options, tier, account, replies, workspace) runs as it always did. Profile
// names, workspaces and connection names are server or operator text and go in
// through textContent only.

// What GET /api/credentials said when the dialog opened, or null (never fetched,
// or it failed: the box then simply has no Store row and no window).
let spawnCreds = null;
let spawnCredsSeq = 0;

const SPAWN_NAME_DEFAULT_PLACEHOLDER = "auto: working dir name";

function spawnPanelOn() {
  return typeof drawerDesktop === "function" && drawerDesktop();
}

function spawnPanelEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// spawnCurrentTier: the tier the form is on now. Without a box layer every
// session is host, whatever the profile says.
function spawnCurrentTier() {
  if (!boxRuntime) return "host";
  return segValue($("runtime-seg")) === "host" ? "host" : "box";
}

// spawnPlistPick: a click on a profile. The same two steps the Profile seg takes.
function spawnPlistPick(name) {
  const sel = $("agent");
  if (!sel || sel.value === name) return;
  sel.value = name;
  sel.dispatchEvent(new Event("change"));
}

function spawnPlistMove(btn, dir) {
  const all = Array.from($("spawn-plist").querySelectorAll("button.sp-item"));
  const next = all[all.indexOf(btn) + dir];
  if (next) next.focus();
}

// renderSpawnPlist rebuilds the list from profiles[]. Cheap (a handful of rows);
// called when profiles load and when the dialog opens.
function renderSpawnPlist() {
  const host = $("spawn-plist");
  if (!host || !spawnPanelOn()) return;
  const cur = $("agent").value;
  const rows = profiles.map((p) => {
    const b = spawnPanelEl("button", "sp-item");
    b.type = "button";
    b.dataset.name = p.name;
    b.setAttribute("aria-pressed", p.name === cur ? "true" : "false");
    if (p.name === cur) b.classList.add("sel");
    b.append(spawnPanelEl("span", "sp-name", p.name), spawnPanelEl("span", "sp-touch", profileTouchLine(p, boxRuntime)));
    b.onclick = () => spawnPlistPick(p.name);
    b.onkeydown = (e) => {
      if (e.key === "ArrowDown") { e.preventDefault(); spawnPlistMove(b, 1); }
      else if (e.key === "ArrowUp") { e.preventDefault(); spawnPlistMove(b, -1); }
    };
    return b;
  });
  host.replaceChildren(spawnPanelEl("div", "sp-head", "Profile"), ...rows);
}

// spawnPlistSelect: only the selection moved (a change of #agent).
function spawnPlistSelect() {
  const host = $("spawn-plist");
  if (!host) return;
  const cur = $("agent").value;
  for (const b of host.querySelectorAll("button.sp-item")) {
    const on = b.dataset.name === cur;
    b.classList.toggle("sel", on);
    b.setAttribute("aria-pressed", on ? "true" : "false");
  }
}

// renderSpawnReach paints the "This agent will be able to" box for what the form
// holds right now: the picked profile, the account and tier on screen, the
// workspace typed. The names of the instance's connections come from /api/meta
// (awscEntries); the elevate window and the store state from /api/credentials.
function renderSpawnReach() {
  const box = $("spawn-reach");
  if (!box) return;
  const p = currentProfile();
  if (!p) { box.replaceChildren(); return; }
  const acct = !$("spawn-account-row").hidden ? $("spawn-account").value : "";
  const rows = reachRows({
    profile: acct ? Object.assign({}, p, { account: acct }) : p,
    tier: spawnCurrentTier(),
    cwd: shortPath(getCwd()),
    meta: { aws_connections: awscEntries },
    creds: spawnCreds,
  });
  const kids = [spawnPanelEl("div", "sr-title", "This agent will be able to")];
  for (const r of rows) {
    const row = spawnPanelEl("div", "sr-row");
    if (r.tone) row.classList.add("tone-" + r.tone);
    const v = spawnPanelEl("span", "sr-v");
    if (r.prod && r.name && r.v.startsWith(r.name)) {
      v.append(r.name, spawnPanelEl("span", "sr-prod", "PROD"), r.v.slice(r.name.length));
    } else {
      v.textContent = r.v;
    }
    row.append(spawnPanelEl("span", "sr-k", r.k), v);
    kids.push(row);
  }
  box.replaceChildren(...kids);
}

// syncSpawnPreview: everything the desktop dialog derives from the form. Called
// from the places the form already re-derives itself (profile change, tier or
// workspace change, account change), so it never needs a poll. A no-op on mobile.
function syncSpawnPreview() {
  const nm = $("sess-name");
  if (!spawnPanelOn()) {
    if (nm && nm.placeholder !== SPAWN_NAME_DEFAULT_PLACEHOLDER) nm.placeholder = SPAWN_NAME_DEFAULT_PLACEHOLDER;
    return;
  }
  spawnPlistSelect();
  renderSpawnReach();
  if (nm) nm.placeholder = spawnNamePlaceholder(getCwd());
}

// spawnPanelOpen: the dialog is about to show. Build the list, paint the box from
// what is known, then fetch the credential state (store lock, elevate windows)
// and repaint. A failed or stale read leaves the box as it was: never throws.
function spawnPanelOpen() {
  syncSpawnPreview();
  if (!spawnPanelOn()) return;
  renderSpawnPlist();
  spawnCredsRefetch();
}

async function spawnCredsRefetch() {
  if (!spawnPanelOn()) return;
  const seq = ++spawnCredsSeq;
  let got = null;
  try {
    const v = await api("/api/credentials", { signal: deadlineSignal(5000) });
    if (v && typeof v === "object") got = v;
  } catch (_) { /* no creds answer: no Store row, no window */ }
  if (seq !== spawnCredsSeq) return;
  spawnCreds = got;
  syncSpawnPreview();
}
