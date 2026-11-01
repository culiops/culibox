"use strict";

// Command palette. Sessions and commands are filtered locally from state the app
// already polls; files come from /api/fs/search. Loaded before app.js, so this
// file only DEFINES things: initPalette() is called from app.js boot.

const PAL_DEBOUNCE_MS = 150;
// Deadline as well as the supersede-abort: this page's fetches can wedge behind
// a stale basic-auth challenge (the same failure PROBE_MS exists for in app.js),
// and a palette that hangs forever on stale results is worse than one that says
// the search is unavailable.
const PAL_FETCH_MS = 4000;

let palItems = [];
let palSel = 0;
let palSeq = 0;   // guards against an older file response overwriting a newer one
let palTimer = null;
let palAbort = null;
// Parallel to palItems: the actual <li> DOM node rendered for each row, in the
// same order. palSelect and palRender read/write selection through this array
// rather than `ul.children[i]` — the <ul> also holds non-selectable heading
// <li>s (see palSection/palRender), so a plain child index would drift off the
// real rows the moment a heading is inserted ahead of it.
let palRowEls = [];

// isInsertHotkey: palette.insert in the shortcut registry (Ctrl+Enter, ⌘+Enter
// on mac): insert the selected file's path at the focused prompt instead of
// opening it in a viewer. The registry match is exact on modifiers, so plain
// Enter stays the open action and AltGr / IME composition never match.
function isInsertHotkey(e, mac) {
  const matches = typeof shortcutMatches === "function" ? shortcutMatches : require("./shortcuts.js").shortcutMatches;
  return matches("palette.insert", e, mac);
}

function paletteOpen() {
  const d = document.getElementById("palette");
  return !!(d && d.open);
}

function openPalette() {
  const d = document.getElementById("palette");
  if (!d || d.open) return;
  const inp = document.getElementById("pal-input");
  inp.value = "";
  d.showModal();
  inp.focus();
  palRefresh();
}

function closePalette() {
  const d = document.getElementById("palette");
  if (d && d.open) d.close();
}

// palFocusedSession: the session whose terminal the focused pane shows, or
// null (a file viewer, or no tabs) — the one the per-session commands act on.
function palFocusedSession() {
  const t = typeof focusedTerminal === "function" ? focusedTerminal() : null;
  return t ? lastSessions.find((s) => s.id === t.id) || null : null;
}

// palPreferDir: where file search looks first — the focused terminal
// session's cwd, else the file tree's root.
function palPreferDir(session, treeRoot) {
  return (session && session.cwd) || treeRoot || "";
}

// palSessionMatchText: a session row matches on its name AND its summary, so
// "the login test" finds the agent that was working on it.
function palSessionMatchText(s) {
  const v = s || {};
  return [v.name || v.id || "", v.summary || ""].filter((x) => x !== "").join(" ");
}

// palReg: the registry's hint renderers. In the browser they are globals of
// shortcuts.js (loaded first); under node --test they come from the module.
function palReg() {
  return typeof shortcutHint === "function" ? { shortcutHint, jumpHint } : require("./shortcuts.js");
}

// palModHint: what ⌘↵ / Ctrl+↵ does on the selected row. A session opens in a
// new pane; a file keeps inserting its path (the shipped behaviour).
function palModHint(kind, mac) {
  const k = palReg().shortcutHint("palette.insert", mac);
  if (kind === "session") return k + " open in new pane";
  if (kind === "file") return k + " insert path";
  return "";
}

// palPaintModHint shows the selected row's ⌘↵ meaning, and hides the hint
// (with its separator) where ⌘↵ does nothing.
function palPaintModHint() {
  const hint = document.getElementById("pal-insert-hint");
  const sep = document.getElementById("pal-mod-sep");
  if (!hint) return;
  const it = palItems[palSel];
  hint.textContent = it ? palModHint(it.kind, IS_MAC) : "";
  hint.hidden = hint.textContent === "";
  if (sep) sep.hidden = hint.hidden;
}

// runItem closes the palette BEFORE acting. Several actions open another dialog
// or raise the undo toast, and a fixed-position toast paints *beneath* a modal
// dialog's backdrop: an Undo the user cannot click is worse than none.
// Awaited via Promise.resolve because some actions are now async (confirmKill
// opens a dialog). A bare try/catch around a promise-returning call would let a
// throw escape as an unhandled rejection instead of reaching the toast.
async function runItem(item) {
  if (!item) return;
  closePalette();
  try { await Promise.resolve(item.run()); }
  catch (e) { showToast("Failed: " + e.message, { error: true }); }
}

// palSessionRun: what picking a session row does. A shell nested under its
// parent (isNestedShell, the sidebar row's test) is revealed beside the parent
// like a sidebar click; anything else is opened. Its runNewPane stays the
// explicit "own pane" override.
function palSessionRun(s, nested, act) {
  return nested ? () => act.reveal(s.id) : () => act.open(s.id, s.name || s.id);
}

// --- item sources ------------------------------------------------------------
// The first nine rows carry their jump shortcut instead of the cwd: past nine
// there is no key to advertise, and a hint that names a key nothing is bound
// to is worse than no hint. mac is threaded in from the caller (palRefresh)
// rather than computed again here — see the mac note on paletteCommands.
function paletteSessions(mac) {
  // In the order the jump shortcut uses, so the hint on a row is the key that opens it.
  const mobile = typeof mobileActive === "function" && mobileActive(window.innerWidth);
  return jumpOrder(lastSessions, mobile).map((s, i) => {
    const name = s.name || s.id;
    // No digit hint on mobile: the jump shortcut has no keyboard there.
    const hintN = !mobile && i < 9 ? palReg().jumpHint(i + 1, mac) : "";
    // Reuses the same activityClass() the sidebar's own status dot is built
    // from (activity.js), so a session row here and its sidebar row always
    // agree on colour/label for the same state.
    const act = activityClass(s.activity);
    return {
      kind: "session", label: name, hint: hintN || (s.cwd ? shortPath(s.cwd) : ""),
      sub: s.summary || "", match: palSessionMatchText(s),
      dotCls: act.cls, dotTitle: act.title,
      run: palSessionRun(s, isNestedShell(s, lastSessions), { reveal: revealShell, open: openTerminal }),
      runNewPane: () => openTerminal(s.id, name, { newPane: true }),
    };
  });
}

// paletteShellCommands: the view commands whose set differs between desktop and
// mobile. Desktop has the Files drawer, the Activity/Access pane views and the
// shortcuts dialog; the tree no longer lives in a collapsible pane there, so
// "Collapse/expand files" would be a dead entry (R17) and is mobile-only. `act` carries the callbacks, so this
// stays a pure function of its inputs.
function paletteShellCommands(mac, desktop, act) {
  if (!desktop) {
    return [
      { id: "steer-rail", kind: "view", label: "Toggle session detail rail", hint: "", run: act.steerRail },
      { id: "toggle-files", kind: "view", label: "Collapse/expand files", hint: palReg().shortcutHint("files.toggle", mac), run: act.toggleFiles },
    ];
  }
  return [
    { id: "activity-view", kind: "view", label: "Activity", hint: "", run: act.activityView },
    { id: "access-view", kind: "view", label: "Access", hint: "", run: act.accessView },
    { id: "files-drawer", kind: "view", label: "Toggle files drawer", hint: palReg().shortcutHint("files.toggle", mac), run: act.filesDrawer },
    { id: "shortcuts", kind: "view", label: "Keyboard shortcuts", hint: palReg().shortcutHint("help.open", mac), run: act.shortcuts },
  ];
}

// paletteShellSpawn: the "+ Shell" entries. Desktop names the three places a
// shell can go (spawnShellIn's placements); the shortcut docks below. Mobile
// draws no dock, so it keeps its one v0.2.0 entry.
function paletteShellSpawn(mac, desktop, spawn) {
  const below = palReg().shortcutHint("shell.below", mac);
  if (!desktop) return [{ kind: "new", label: "New shell here", hint: below, run: () => spawn("below") }];
  return [
    { kind: "new", label: "+ Shell below", hint: below, run: () => spawn("below") },
    { kind: "new", label: "+ Shell right", hint: "", run: () => spawn("right") },
    { kind: "new", label: "+ Shell in own pane", hint: "", run: () => spawn("own") },
  ];
}

// mac is read once by the caller (palRefresh) from IS_MAC (shortcuts.js) and
// threaded through here and into paletteSessions(), so the pure builders take
// it as a parameter and tests can inject either platform.
// One "New session · X" entry per profile. The hint stays empty on both
// layouts (v0.3.0): ProfileView.bin is for the spawn dialog, and an absolute
// shell path is no use as a right-aligned palette hint.
function paletteProfileSpawns(list, open) {
  return list.map((p) => ({ kind: "new", label: "New session · " + p.name, hint: "", run: () => open(p.name) }));
}

// palettePageCommands: the page entries (S4). Desktop opens Audit, Analytics
// and each Settings section as a page; Audit and Analytics only when the
// server has them (`flags` mirrors /api/meta's audit/analytics, as their
// sidebar links do). Mobile keeps exactly the two v0.4.0 Settings entries.
// h = { audit(), analytics(), settings(tab) }; the tab ids are the panel ids.
function palettePageCommands(desktop, flags, h) {
  const settings = (label, tab) => ({ kind: "settings", label: "Settings · " + label, hint: "", run: () => h.settings(tab) });
  if (!desktop) return [settings("Profiles", "manage"), settings("Font sizes", "settings")];
  const out = [];
  if (flags && flags.audit) out.push({ kind: "view", label: "Audit", hint: "", run: () => h.audit() });
  if (flags && flags.analytics) out.push({ kind: "view", label: "Analytics", hint: "", run: () => h.analytics() });
  out.push(
    settings("Profiles", "manage"), settings("Agent accounts", "accounts"), settings("Credentials", "creds"),
    settings("API tokens", "tokens"), settings("Appearance", "settings"), settings("About", "about"),
  );
  return out;
}

function paletteCommands(mac) {
  const cmds = [];
  // Every spawn goes through the form: under containment the workspace choice
  // IS the security boundary, so the palette preselects the profile and lets
  // the form ask the rest rather than starting an agent from a keystroke.
  cmds.push(...paletteProfileSpawns(profiles, openSpawnDialog));
  cmds.push({ kind: "new", label: "New session…", hint: "", run: () => openSpawnDialog() });
  if (window.shellProfile) {
    const deskShell = !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
    cmds.push(...paletteShellSpawn(mac, deskShell, (placement) => spawnShellIn(focusedCell, placement)));
  }
  // The focused session's control commands, through the same dispatch the
  // row menu uses (runSessionAction) — one handler per action.
  const focused = palFocusedSession();
  if (focused) {
    const fname = focused.name || focused.id;
    for (const a of paletteActionItems(focused)) {
      cmds.push({ kind: "session-action", label: a.label + " · " + fname, hint: "", run: () => runSessionAction(a.id, focused) });
    }
  }
  for (const s of lastSessions) {
    const name = s.name || s.id;
    cmds.push({ kind: "kill", label: "End session · " + name, hint: "", run: () => confirmKill(s.id, name) });
  }
  for (const n of [1, 2, 4]) {
    cmds.push({ kind: "layout", label: "Layout · " + n + (n === 1 ? " window" : " windows"), hint: "", run: () => applyCap(n) });
  }
  {
    const deskPages = !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
    const linkShown = (id) => { const b = document.getElementById(id); return !!b && !b.hidden; };
    cmds.push(...palettePageCommands(deskPages, { audit: linkShown("side-audit"), analytics: linkShown("side-analytics") },
      { audit: () => openAudit(), analytics: openAnalytics, settings: (tab) => openSettings(tab) }));
  }
  // toggleSidebar is a no-op on mobile, so don't offer it there.
  if (!(typeof mobileActive === "function" && mobileActive(window.innerWidth))) {
    cmds.push({ kind: "view", label: "Toggle sidebar", hint: palReg().shortcutHint("sidebar.toggle", mac), run: toggleSidebar });
  }
  // Desktop: the rail's content lives in the pane's Access view (R6: one
  // entry), beside the Activity view. Both toggle on the focused pane's agent.
  const desktop = typeof mobileActive === "function" && !mobileActive(window.innerWidth) && typeof toggleDrawer === "function";
  const focusedId = () => (cellTerminal(focusedCell) || {}).id;
  const shell = paletteShellCommands(mac, desktop, {
    shortcuts: showShortcuts,
    filesDrawer: () => toggleDrawer("files", focusedId()), // toggleDrawer closes a page itself
    // A page hides the panes: the view must open where the user can see it.
    activityView: () => { closePage(); if (focusedCell) focusedCell.toggleView("activity"); },
    accessView: () => { closePage(); if (focusedCell) focusedCell.toggleView("access"); },
    steerRail: toggleSteerRail,
    toggleFiles: () => togglePane("files"),
  });
  const shellCmd = (id) => shell.find((c) => c.id === id);
  for (const id of ["activity-view", "access-view", "steer-rail"]) if (shellCmd(id)) cmds.push(shellCmd(id));
  if (!desktop) cmds.push({ kind: "view", label: "Collapse/expand sessions", hint: palReg().shortcutHint("sidebar.toggle", mac), run: () => togglePane("sessions") });
  for (const id of ["toggle-files", "files-drawer", "shortcuts"]) if (shellCmd(id)) cmds.push(shellCmd(id));
  cmds.push({ kind: "view", label: "Refresh files", hint: "re-read the tree", run: () => ftLoadRoot({ fresh: true }) });
  cmds.push({ kind: "view", label: "Increase font size", hint: palReg().shortcutHint("zoom.in", mac), run: () => zoomFocused(+fontPrefs.STEP) });
  cmds.push({ kind: "view", label: "Decrease font size", hint: palReg().shortcutHint("zoom.out", mac), run: () => zoomFocused(-fontPrefs.STEP) });
  cmds.push({ kind: "view", label: "Reset font size", hint: palReg().shortcutHint("zoom.reset", mac), run: () => resetZoomFocused() });
  return cmds;
}

// --- local filtering ---------------------------------------------------------
// Substring hits rank above scattered subsequence hits; earlier hits rank above
// later ones. Deliberately simpler than the server-side file scorer: these
// lists are dozens of items, not tens of thousands.
function palMatch(text, q) {
  const t = text.toLowerCase();
  const i = t.indexOf(q);
  if (i >= 0) return 100 - Math.min(i, 90);
  let qi = 0;
  for (let k = 0; k < t.length && qi < q.length; k++) if (t[k] === q[qi]) qi++;
  return qi === q.length ? 1 : -1;
}

function palFilter(items, q) {
  if (!q) return items;
  return items
    .map((it) => ({ it, s: palMatch(it.match || it.label, q) }))
    .filter((x) => x.s >= 0)
    .sort((a, b) => b.s - a.s)
    .map((x) => x.it);
}

// --- query pipeline ----------------------------------------------------------
function palRefresh() {
  const raw = document.getElementById("pal-input").value;
  const seq = ++palSeq;
  palSel = 0;
  clearTimeout(palTimer);
  if (palAbort) { palAbort.abort(); palAbort = null; }
  // Computed once here and threaded into paletteSessions()/paletteCommands()
  // rather than each recomputing it.
  const mac = IS_MAC;

  if (raw.charAt(0) === ">") { // VS Code convention: > means commands only
    palRender(palFilter(paletteCommands(mac), raw.slice(1).trim().toLowerCase()), "");
    return;
  }
  const q = raw.trim();
  if (!q) {
    palRender(paletteSessions(mac).concat(paletteCommands(mac)), "");
    return;
  }
  const lq = q.toLowerCase();
  const local = palFilter(paletteSessions(mac), lq).concat(palFilter(paletteCommands(mac), lq));
  palRender(local, "searching files…");
  palTimer = setTimeout(() => palSearchFiles(q, seq, local), PAL_DEBOUNCE_MS);
}

async function palSearchFiles(q, seq, local) {
  const ac = new AbortController();
  palAbort = ac;
  const timer = setTimeout(() => ac.abort(), PAL_FETCH_MS);
  let data = null, failed = false;
  try {
    const prefer = palPreferDir(palFocusedSession(), typeof ftRootPath === "string" ? ftRootPath : "");
    const url = "/api/fs/search?limit=40&q=" + encodeURIComponent(q) + (prefer ? "&prefer=" + encodeURIComponent(prefer) : "");
    const res = await fetch(url, { signal: ac.signal });
    if (res.ok) data = await res.json();
    else failed = true;
  } catch (_) {
    failed = true;
  } finally {
    clearTimeout(timer);
  }
  if (seq !== palSeq) return;   // a newer query already won
  if (!paletteOpen()) return;   // closed while in flight: nothing to render into
  palAbort = null;
  if (failed || !data) { palRender(local, "file search unavailable"); return; }
  // Basename in the label column, directory in the hint column. Showing the
  // whole relative path in both truncated the part that identifies the file.
  const files = (data.results || []).map((r) => {
    const cut = r.rel.lastIndexOf("/");
    return {
      kind: "file",
      label: cut >= 0 ? r.rel.slice(cut + 1) : r.rel,
      hint: cut >= 0 ? tailDirs(r.rel.slice(0, cut)) : "",
      title: shortPath(r.path),
      // The absolute path, so the copy button and Ctrl+Enter do not have to
      // reconstruct it from the label and hint (both of which are truncated for
      // display and cannot be reassembled).
      path: r.path,
      run: () => openFile(r.path),
    };
  });
  palRender(local.concat(files),
    data.truncated ? "index truncated: some files are not listed" : "");
}

// tailDirs keeps the last few components of a directory path. CSS would ellipsis
// the END of a long path, cutting exactly the part that identifies where a file
// lives ("…/internal/web/static" is useful; "culiops-agent-box/inter…" is not).
function tailDirs(dir, keep) {
  const parts = dir.split("/");
  keep = keep || 3;
  return parts.length <= keep ? dir : "…/" + parts.slice(-keep).join("/");
}

// --- rendering ---------------------------------------------------------------
// palSection maps an item's kind to the heading it renders under. Sessions and
// files each get their own named section; every other kind (the "new"/"kill"/
// "layout"/"settings"/"view" commands) shares one COMMANDS section — that chip
// text is gone from the row, so the heading is the only thing that used to say
// what a command row was.
const PAL_SECTIONS = ["SESSIONS", "FILES", "COMMANDS"];
function palSection(kind) {
  if (kind === "session") return "SESSIONS";
  if (kind === "file") return "FILES";
  return "COMMANDS";
}

// palGroup buckets items by section, preserving each item's relative order
// within its bucket, then concatenates the buckets in the fixed section order
// above. The caller's own order (sessions/commands interleaved, files appended
// once the async search returns) does not need to already match — this is
// what makes SESSIONS/FILES/COMMANDS render in that order regardless of when
// each list was produced.
function palGroup(items) {
  const buckets = { SESSIONS: [], FILES: [], COMMANDS: [] };
  for (const it of items) buckets[palSection(it.kind)].push(it);
  return PAL_SECTIONS.flatMap((s) => buckets[s].map((it) => ({ it, section: s })));
}

// palGlyph builds the leading per-row marker (mockup D4): a session's own
// activity dot (the exact <span class="dot ..."> the sidebar renders, built
// from the same activityClass() classes — paletteSessions already attached
// dotCls/dotTitle for this), a file's ▸, or a command's >. The > here is a
// separate, decorative glyph from the ">" the user can TYPE into #pal-input
// to switch to commands-only filtering (see palRefresh's `raw.charAt(0) ===
// ">"` check) — one lives in the search field, this one in a COMMANDS row, so
// neither is "the same >" doing two jobs.
function palGlyph(it) {
  const g = document.createElement("span");
  g.className = "pal-glyph";
  g.setAttribute("aria-hidden", "true"); // decorative; the row's own label/hint carry the meaning
  if (it.kind === "session") {
    const dot = document.createElement("span");
    dot.className = it.dotCls || "dot unknown";
    if (it.dotTitle) dot.title = it.dotTitle; // mouse-hover only; the glyph column itself is aria-hidden
    g.append(dot);
  } else if (it.kind === "file") {
    g.textContent = "▸";
  } else {
    g.textContent = ">";
  }
  return g;
}

function palBuildRow(it, idx, selected) {
  const li = document.createElement("li");
  li.className = "pal-row" + (selected ? " sel" : "");
  li.append(palGlyph(it));
  const label = document.createElement("span");
  label.className = "pal-label";
  label.textContent = it.label;
  label.title = it.title || it.label;
  li.append(label);
  if (it.sub) {
    // One-line summary beside the session's name (spec §2.6); clipped by CSS.
    const sub = document.createElement("span");
    sub.className = "pal-sub";
    sub.textContent = it.sub;
    sub.title = it.sub;
    li.append(sub);
  }
  if (it.hint) {
    const h = document.createElement("span");
    h.className = "pal-hint";
    h.textContent = it.hint;
    li.append(h);
  }
  if (it.path) {
    // Only on the selected row. li.onmouseenter already selects the row under
    // the pointer, so this needs no separate hover state — the button appears
    // wherever the mouse is.
    const cp = document.createElement("button");
    cp.type = "button";
    cp.className = "pal-copy";
    cp.textContent = "⧉";
    cp.title = "Copy path";
    cp.setAttribute("aria-label", "Copy path");
    cp.onclick = (e) => {
      e.stopPropagation(); // must not also run the item
      // Close FIRST: showModal() makes everything outside the dialog inert, so
      // copyText's off-screen-textarea fallback (this page is plain HTTP, so
      // navigator.clipboard is usually absent) cannot focus and copies nothing.
      closePalette();
      copyPath(it.path);
    };
    li.append(cp);
  }
  li.onmouseenter = () => palSelect(idx);
  li.onclick = () => runItem(it);
  return li;
}

function palRender(items, note) {
  const grouped = palGroup(items);
  palItems = grouped.map((g) => g.it);
  if (palSel >= palItems.length) palSel = 0;

  const ul = document.getElementById("pal-list");
  const children = [];
  const rowEls = [];
  let section = null;
  grouped.forEach(({ it, section: s }, idx) => {
    if (s !== section) {
      section = s;
      const head = document.createElement("li");
      head.className = "pal-head";
      head.textContent = s;
      children.push(head);
    }
    const li = palBuildRow(it, idx, idx === palSel);
    children.push(li);
    rowEls.push(li);
  });
  ul.replaceChildren(...children);
  palRowEls = rowEls;

  document.getElementById("pal-empty").hidden = palItems.length > 0;
  document.getElementById("pal-note").textContent = note || "";
  palPaintModHint();
}

function palSelect(i) {
  if (!palItems.length) return;
  palSel = (i + palItems.length) % palItems.length;
  for (let k = 0; k < palRowEls.length; k++) palRowEls[k].classList.toggle("sel", k === palSel);
  const el = palRowEls[palSel];
  if (el) el.scrollIntoView({ block: "nearest" });
  palPaintModHint();
}

// --- wiring ------------------------------------------------------------------
function initPalette() {
  const d = document.getElementById("palette");
  const inp = document.getElementById("pal-input");
  if (!d || !inp) return;
  const combo = shortcutHint("palette.open", IS_MAC);
  // The fallback is written next to the primary everywhere the combo appears:
  // when another app has grabbed the primary system-wide, the user needs to be
  // told a second one exists: they cannot guess it.
  const combos = combo + " or " + shortcutHint("palette.open.alt", IS_MAC);

  const openBtn = document.getElementById("palette-open");
  if (openBtn) {
    // The button is labelled with the shortcut itself rather than a magnifier
    // glyph: it doubles as the only always-visible place the combo is written
    // down (the hint bar can be dismissed for good).
    //
    // Write into .pal-long, NOT the button: the markup carries a second span
    // (.pal-short) that the narrow breakpoint swaps in, because a keyboard
    // shortcut means nothing on a phone and its width is what lets the product
    // name stay in the header. Assigning textContent here would delete it.
    const long = openBtn.querySelector(".pal-long");
    if (long) long.textContent = combo;
    else openBtn.textContent = combo;
    openBtn.title = "Command palette (" + combos + ")";
    openBtn.onclick = openPalette;
  }
  // The mobile Files screen hides the global header (and its palette button)
  // and promotes the Files pane header into the screen's topbar, so it carries
  // its own ⌘ trigger (M6). Same handler, no shortcut label — a phone has no
  // keyboard to write down. Invisible on desktop (CSS), harmless if absent.
  const filesPal = document.getElementById("ft-palette");
  if (filesPal) filesPal.onclick = openPalette;

  inp.addEventListener("input", palRefresh);
  inp.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); palSelect(palSel + 1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); palSelect(palSel - 1); }
    else if (isInsertHotkey(e, IS_MAC) && palItems[palSel] && palItems[palSel].runNewPane) {
      e.preventDefault();
      const it = palItems[palSel];
      closePalette();
      it.runNewPane();
    }
    else if (isInsertHotkey(e, IS_MAC) && palItems[palSel] && palItems[palSel].path) {
      e.preventDefault();
      const p = palItems[palSel].path;
      closePalette(); // see the copy button: a modal dialog makes the rest of the page inert
      insertPathAtPrompt(p);
    }
    else if (e.key === "Enter") { e.preventDefault(); runItem(palItems[palSel]); }
  });
  // Esc, backdrop click and programmatic close all land here: drop any in-flight
  // search so a late response cannot render into a closed palette.
  d.addEventListener("close", () => {
    clearTimeout(palTimer);
    if (palAbort) { palAbort.abort(); palAbort = null; }
  });

  // Hotkeys: the chords and why they are what they are (Ctrl+K is readline's
  // kill-line, so non-mac needs Shift; Ctrl+Shift+Space is the fallback when
  // another app owns a letter combo system-wide) live in the shortcuts.js
  // registry. The ⌕ header button is the mouse path, and the guaranteed one
  // when something outside the page claims a combo. The registry's allow table
  // already keeps a real text field out; opening over a terminal is the point.
  const openFromKey = () => (paletteOpen() ? false : (openPalette(), true));
  bindShortcut("palette.open", openFromKey);
  bindShortcut("palette.open.alt", openFromKey);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { isInsertHotkey, palSection, palGroup, palPreferDir, palSessionMatchText, palModHint, paletteShellCommands, paletteShellSpawn, palSessionRun, paletteProfileSpawns, palettePageCommands };
}
