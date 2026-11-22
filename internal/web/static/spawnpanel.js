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
  return spawnWhere().runtime === "host" ? "host" : "box";
}

// spawnGateInput: what spawnBoxGate reads, from the page's own state.
function spawnGateInput() {
  return { profile: currentProfile(), metaLoaded, boxRuntime, boxUnavailableReason, boxDaemonReason, tier: spawnWhere().runtime || "" };
}

// The sentence a blocked submit wrote on the dialog's error line (spawnGateErr), so syncSpawnGate clears it, and only it, once the
// profile or tier on screen is no longer blocked.
let spawnGateErr = "";

// syncSpawnGate: what the dialog says about boxes (#142). #spawn-nobox shows "Boxes are unavailable on this machine: <reason>."
// (red when the profile on screen needs one, quiet otherwise), and Spawn is aria-disabled with the reason as its title while it is
// blocked: still hoverable, as every desktop control that cannot act yet (setDisabledReason); the submit handler refuses it. The
// desktop only: on the phone the block stays hidden and Spawn stays live, as it was. Returns the gate, for the submit handler.
function syncSpawnGate() {
  let g = spawnBoxGate(spawnPanelOn() ? spawnGateInput() : undefined);
  // The workspace line's own block (app.js): the box tier is picked and the box would refuse the folder (a Mac's VM cannot see it).
  // Not a boxes-unavailable case, so the submit handler need not re-read the daemon: noLayer says so.
  const wsWhy = typeof spawnWsCheckBlock === "function" ? spawnWsCheckBlock() : "";
  if (!g.blocked && wsWhy) g = Object.assign({}, g, { blocked: true, noLayer: true, text: wsWhy });
  const el = $("spawn-nobox");
  if (el) {
    el.hidden = !g.show;
    el.classList.toggle("err", g.tone === "error");
    const kids = [];
    if (g.show) {
      kids.push(spawnPanelEl("span", "nb-line", g.line));
      if (g.more) kids.push(spawnPanelEl("span", "nb-more", g.more));
    }
    el.replaceChildren(...kids);
  }
  // Not while a spawn is in flight: the submit handler holds the button disabled and puts its label back itself.
  const go = $("spawn").querySelector('button[type="submit"]');
  if (go && !spawnBusy) setDisabledReason(go, g.blocked, g.text, true);
  if (spawnGateErr && g.text !== spawnGateErr) {
    const err = $("spawn-err");
    if (err && err.textContent === spawnGateErr) setDialogError("spawn-modal", "");
    spawnGateErr = "";
  }
  return g;
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
  if (isSubmitHotkey(e, IS_MAC)) {
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
    // Empty search: Enter is a no-op, so it cannot drop the last-used
    // preselection (and flip boxed -> host by accident).
    if (first && input.value.trim() !== "") spawnPlistPick(first.dataset.name);
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
    const touch = spawnPanelEl("span", "sp-touch", profileTouchLine(p, metaLoaded ? boxRuntime : undefined));
    const nhLabel = notHomeLabel(p.not_home);
    if (nhLabel) {
      const nh = spawnPanelEl("span", "sp-nothome", nhLabel);
      nh.title = notHomeTitle(p.not_home);
      touch.append(" ", nh);
    }
    // A long name ends in an ellipsis (desktop.css), so its whole text is the tooltip (frame M4.1's title attributes).
    const nm = spawnPanelEl("span", "sp-name");
    // The kind icon comes first: a terminal for a shell, the Claude mark for a Claude profile, nothing for any other.
    const kind = profileKind(p);
    if (kind === "shell") nm.append(svgIcon(SVG_TERMINAL_24, { viewBox: "0 0 24 24", strokeWidth: "2", size: 12 }));
    else if (kind === "claude") nm.append(claudeMarkSvg(12));
    nm.append(p.name);
    nm.title = p.name;
    b.append(nm, touch);
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
// workspace typed. The instance's connections and tokens come from /api/meta
// (awscEntries, stEntries); the elevate window and the store state from
// /api/credentials; the imports row from /api/meta's bundle_imports.
function renderSpawnReach() {
  const box = $("spawn-reach");
  if (!box) return;
  const p = currentProfile();
  if (!p) { box.replaceChildren(); return; }
  // A profile that needs a box when there is none: the host rows below would say "your whole user account" and "not contained"
  // for a session that will not start there (#142).
  const gate = spawnBoxGate(spawnGateInput());
  if (gate.noLayer && gate.blocked) {
    paintReachRows(box, spawnReachNeedsBox(boxUnavailableReason), { title: "This agent will be able to" });
    return;
  }
  const acct = !$("spawn-account-row").hidden ? $("spawn-account").value : "";
  const w = spawnWhere(); // clone is the card's: false drops the copy's rows, true adds Work home, absent (the phone) is the profile's
  const rows = reachRows({
    profile: acct ? Object.assign({}, p, { account: acct }) : p,
    tier: spawnCurrentTier(),
    clone: w.clone,
    cwd: shortPath(spawnCwdFor(w)),
    cwdFull: spawnCwdFor(w),
    meta: { aws_connections: awscEntries, static_tokens: stEntries },
    creds: spawnCreds,
    bundleImports,
    desktop: !mobileActive(window.innerWidth), // "starting" on the desktop, the phone's "spawn" otherwise
  });
  paintReachRows(box, rows, { title: "This agent will be able to" });
}

// paintReachRows draws reachRows' rows into `box`, replacing what it held: the title, then one label/value grid
// (frame M4.1) with a row per key. A value's tooltip is its full text (`full` when the row abbreviates a path);
// a path inside it is its own mono span; an AWS row on a prod connection carries the red PROD tag after its name.
// Every string is server or operator text, so it goes in through textContent only.
function paintReachRows(box, rows, opts) {
  const o = opts || {};
  const value = (r, cls) => {
    const v = spawnPanelEl("span", cls);
    v.title = r.full || r.v;
    const at = r.path ? r.v.indexOf(r.path) : -1;
    if (r.prod && r.name && r.v.startsWith(r.name)) {
      v.append(r.name, spawnPanelEl("span", "sr-prod", "PROD"), r.v.slice(r.name.length));
    } else if (at >= 0) {
      const before = r.v.slice(0, at), after = r.v.slice(at + r.path.length);
      v.append(...(before ? [before] : []), spawnPanelEl("span", "sr-path", r.path), ...(after ? [after] : []));
    } else {
      v.textContent = r.v;
    }
    return v;
  };
  const kids = [];
  if (o.title) kids.push(spawnPanelEl("div", "sr-title", o.title));
  const grid = spawnPanelEl("div", "sr-grid");
  for (const r of rows || []) {
    const row = spawnPanelEl("div", "sr-row");
    if (r.tone) row.classList.add("tone-" + r.tone);
    row.append(spawnPanelEl("span", "sr-k", r.k), value(r, "sr-v"));
    grid.append(row);
  }
  kids.push(grid);
  box.replaceChildren(...kids);
}

// syncSpawnPreview: everything the desktop dialog derives from the form. Called
// from the places the form already re-derives itself (profile change, tier or
// workspace change, account change), so it never needs a poll. A no-op on mobile.
function syncSpawnPreview() {
  syncSpawnGate();
  syncSpawnCopyLine();
  const nm = $("sess-name");
  if (!spawnPanelOn()) {
    if (nm && nm.placeholder !== SPAWN_NAME_DEFAULT_PLACEHOLDER) nm.placeholder = SPAWN_NAME_DEFAULT_PLACEHOLDER;
    return;
  }
  spawnPlistSelect();
  renderSpawnReach();
  if (nm) nm.placeholder = spawnNamePlaceholder(getCwd());
}

// syncSpawnCopyLine: "Private copy of ~/x" under the workspace field when the
// picked profile is a private copy; hidden otherwise. Runs before the
// spawnPanelOn() early return so the phone layout gets it too. textContent only.
function syncSpawnCopyLine() {
  const el = $("spawn-copy-line");
  if (!el) return;
  const p = currentProfile() || {};
  const line = wmCopyLine(p.clone_of || "", window.HOME_DIR);
  // The suffix is workHomeSuffix's alone; no profile means no line at all.
  const text = line ? line + workHomeSuffix(p, !!(p.clone_of && p.github_write)) : "";
  // Box · in place edits the repo itself, not a private copy: on the desktop the line has nothing to say. The phone's is as it was.
  const inPlace = drawerDesktop() && spawnWhere().clone === false;
  el.textContent = inPlace ? "" : text;
  el.hidden = inPlace || text === "";
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
