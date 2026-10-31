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

// spawnPlistMove: ArrowDown/Up between the visible rows (a row the filter hid is
// skipped). ArrowUp from the first visible row goes back to the search box.
function spawnPlistMove(btn, dir) {
  const all = Array.from($("spawn-plist").querySelectorAll("button.sp-item:not([hidden])"));
  const i = all.indexOf(btn);
  if (dir < 0 && i <= 0) {
    const s = $("sp-search");
    if (s) s.focus();
    return;
  }
  const next = all[i + dir];
  if (next) next.focus();
}

// spawnPlistFilter hides the rows the search box does not match (the hidden
// attribute, never removal: spawnPlistSelect still finds the selected row, which
// keeps its .sel while filtered out) and shows the muted no-match line. No box
// (fewer than PLIST_SEARCH_MIN profiles) means an empty query: every row shows.
function spawnPlistFilter() {
  const host = $("spawn-plist");
  const box = host && host.querySelector("#sp-rows");
  if (!box) return;
  const input = host.querySelector("#sp-search");
  const q = input ? input.value : "";
  let shown = 0;
  for (const b of box.querySelectorAll("button.sp-item")) {
    const touch = b.querySelector(".sp-touch");
    const ok = profileMatches(b.dataset.name, touch ? touch.textContent : "", q);
    b.hidden = !ok;
    if (ok) shown++;
  }
  const old = box.querySelector(".sp-empty");
  if (old) old.remove();
  if (shown === 0 && q.trim() !== "") box.append(spawnPanelEl("div", "sp-empty", 'No profile matches "' + q.trim() + '"'));
}

// spawnPlistResetSearch: a fresh open starts unfiltered.
function spawnPlistResetSearch() {
  const s = $("sp-search");
  if (!s || s.value === "") return;
  s.value = "";
  spawnPlistFilter();
}

// spawnPlistSearchKey: the search box lives outside <form id="spawn">, so it has
// its own keys. Real focus moves (no aria-activedescendant): ArrowDown goes to the
// first visible row, whose own keys take over. Enter picks the first visible match
// and focus stays in the box. Escape with text clears it and stops there, so the
// dialog stays open (a second Escape, on an empty box, closes it as today).
function spawnPlistSearchKey(e) {
  const input = e.currentTarget;
  if (isSubmitHotkey(e, /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || ""))) {
    e.preventDefault();
    $("spawn").requestSubmit();
    return;
  }
  if (e.isComposing) return;
  if (e.key === "ArrowDown") {
    const first = $("spawn-plist").querySelector("button.sp-item:not([hidden])");
    if (first) { e.preventDefault(); first.focus(); }
  } else if (e.key === "Enter") {
    const first = $("spawn-plist").querySelector("button.sp-item:not([hidden])");
    e.preventDefault();
    if (first) spawnPlistPick(first.dataset.name);
  } else if (e.key === "Escape" && input.value !== "") {
    // keydown's default action is what asks the <dialog> to close; stopping it
    // here keeps the dialog (and any document-level Escape handler) out of it.
    e.preventDefault();
    e.stopPropagation();
    input.value = "";
    spawnPlistFilter();
  }
}

function spawnPlistSearchInput() {
  const input = spawnPanelEl("input");
  input.id = "sp-search";
  input.type = "text";
  input.placeholder = "Filter profiles";
  input.autocomplete = "off";
  input.spellcheck = false;
  input.setAttribute("aria-label", "Filter profiles");
  input.setAttribute("aria-controls", "sp-rows");
  input.addEventListener("input", spawnPlistFilter);
  input.addEventListener("keydown", spawnPlistSearchKey);
  return input;
}

// renderSpawnPlist rebuilds the list from profiles[]. Cheap (a handful of rows);
// called when profiles load and when the dialog opens. Layout: heading, the search
// box (only from PLIST_SEARCH_MIN rows up), then #sp-rows. Once the box exists a
// re-render replaces only the rows, so the box keeps its node, value and focus,
// and the current filter is applied to the new rows.
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
  const wantSearch = rows.length >= PLIST_SEARCH_MIN;
  let box = host.querySelector("#sp-rows");
  if (!box || wantSearch !== !!host.querySelector("#sp-search")) {
    box = spawnPanelEl("div");
    box.id = "sp-rows";
    const kids = [spawnPanelEl("div", "sp-head", "Profile")];
    if (wantSearch) kids.push(spawnPlistSearchInput());
    host.replaceChildren(...kids, box);
  }
  box.replaceChildren(...rows);
  spawnPlistFilter();
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
  paintReachRows(box, rows, { title: "This agent will be able to" });
}

// paintReachRows draws reachRows' rows into `box`, replacing what it held. The
// one renderer for both places that show reach: the New session box (a row per
// key) and the Settings page's "Can touch" line (opts.inline: the label, then
// every row's v in one flowing line joined by " · "). An AWS row on a prod
// connection carries the red PROD tag after its name either way. Every string
// is server or operator text, so it goes in through textContent only.
function paintReachRows(box, rows, opts) {
  const o = opts || {};
  const value = (r, cls) => {
    const v = spawnPanelEl("span", cls);
    if (r.prod && r.name && r.v.startsWith(r.name)) {
      v.append(r.name, spawnPanelEl("span", "sr-prod", "PROD"), r.v.slice(r.name.length));
    } else {
      v.textContent = r.v;
    }
    return v;
  };
  const kids = [];
  if (o.inline) {
    if (o.title) kids.push(spawnPanelEl("span", "sr-ik", o.title));
    const line = spawnPanelEl("span", "sr-iv");
    (rows || []).forEach((r, i) => {
      if (i) line.append(" · ");
      const part = value(r, "sr-part");
      if (r.tone) part.classList.add("tone-" + r.tone);
      line.append(part);
    });
    kids.push(line);
  } else {
    if (o.title) kids.push(spawnPanelEl("div", "sr-title", o.title));
    for (const r of rows || []) {
      const row = spawnPanelEl("div", "sr-row");
      if (r.tone) row.classList.add("tone-" + r.tone);
      row.append(spawnPanelEl("span", "sr-k", r.k), value(r, "sr-v"));
      kids.push(row);
    }
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
