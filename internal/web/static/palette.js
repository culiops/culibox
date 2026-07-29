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

// isPaletteHotkey matches the key events that open the palette.
//
// ⌘K is free on macOS, but on every other platform Ctrl+K is readline's
// kill-to-end-of-line, which the shells and agent TUIs running in these panes
// actually use, so the non-mac combo is Ctrl+Shift+K, matching the Ctrl+Shift+`
// convention already used for the quick shell.
//
// Ctrl+Shift+Space is accepted everywhere as a second way in. A letter combo is
// exactly what other desktop apps register as a SYSTEM-WIDE hotkey (Notion took
// Ctrl+Shift+K on a Windows machine, so Chrome never received the keypress at
// all: no page can defend against that). Space is a far less popular target,
// and it carries no meaning inside a terminal either.
function isPaletteHotkey(e, mac) {
  if (e.altKey) return false;
  if (e.ctrlKey && e.shiftKey && !e.metaKey && (e.code === "Space" || (!mac && e.code === "KeyK"))) return true;
  return !!(mac && e.metaKey && !e.ctrlKey && !e.shiftKey && e.code === "KeyK");
}

// isInsertHotkey matches Ctrl+Enter (⌘+Enter on mac): insert the selected file's
// path at the focused prompt instead of opening it in a viewer. Plain Enter must
// stay the open action, so the modifier is the whole distinction. Alt is
// excluded like in isPaletteHotkey: AltGr (common off-mac) reports as both
// ctrlKey and altKey, and without the exclusion AltGr+Enter would insert a path
// instead of opening the file it looks like it should open.
function isInsertHotkey(e, mac) {
  if (e.key !== "Enter" || e.altKey) return false;
  return mac ? !!e.metaKey && !e.ctrlKey : !!e.ctrlKey && !e.metaKey;
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

// --- item sources ------------------------------------------------------------
function paletteSessions() {
  return lastSessions.map((s) => {
    const name = s.name || s.id;
    return {
      kind: "session", label: name, hint: s.cwd ? shortPath(s.cwd) : "",
      run: () => openTerminal(s.id, name),
    };
  });
}

function paletteCommands() {
  const cmds = [];
  // Every spawn goes through the form: under containment the workspace choice
  // IS the security boundary, so the palette preselects the profile and lets
  // the form ask the rest rather than starting an agent from a keystroke.
  for (const p of profiles) {
    cmds.push({ kind: "new", label: "New session · " + p.name, hint: p.bin || "", run: () => openSpawnDialog(p.name) });
  }
  cmds.push({ kind: "new", label: "New session…", hint: "", run: () => openSpawnDialog() });
  if (window.shellProfile) {
    cmds.push({ kind: "new", label: "New shell here", hint: "Ctrl+Shift+`", run: () => spawnShellIn(focusedCell) });
  }
  for (const s of lastSessions) {
    const name = s.name || s.id;
    cmds.push({ kind: "kill", label: "Kill session · " + name, hint: "", run: () => confirmKill(s.id, name) });
  }
  for (const n of [1, 2, 4]) {
    cmds.push({ kind: "layout", label: "Layout · " + n + (n === 1 ? " window" : " windows"), hint: "", run: () => applyCap(n) });
  }
  cmds.push({ kind: "settings", label: "Settings · Profiles", hint: "", run: () => openSettings("manage") });
  cmds.push({ kind: "settings", label: "Settings · Font sizes", hint: "", run: () => openSettings("settings") });
  cmds.push({ kind: "view", label: "Toggle sidebar", hint: "", run: toggleSidebar });
  cmds.push({ kind: "view", label: "Refresh files", hint: "re-read the tree", run: () => ftLoadRoot() });
  cmds.push({ kind: "view", label: "Increase font size", hint: "Ctrl +", run: () => zoomFocused(+fontPrefs.STEP) });
  cmds.push({ kind: "view", label: "Decrease font size", hint: "Ctrl -", run: () => zoomFocused(-fontPrefs.STEP) });
  cmds.push({ kind: "view", label: "Reset font size", hint: "Ctrl 0", run: () => resetZoomFocused() });
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
    .map((it) => ({ it, s: palMatch(it.label, q) }))
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

  if (raw.charAt(0) === ">") { // VS Code convention: > means commands only
    palRender(palFilter(paletteCommands(), raw.slice(1).trim().toLowerCase()), "");
    return;
  }
  const q = raw.trim();
  if (!q) {
    palRender(paletteSessions().concat(paletteCommands()), "");
    return;
  }
  const lq = q.toLowerCase();
  const local = palFilter(paletteSessions(), lq).concat(palFilter(paletteCommands(), lq));
  palRender(local, "searching files…");
  palTimer = setTimeout(() => palSearchFiles(q, seq, local), PAL_DEBOUNCE_MS);
}

async function palSearchFiles(q, seq, local) {
  const ac = new AbortController();
  palAbort = ac;
  const timer = setTimeout(() => ac.abort(), PAL_FETCH_MS);
  let data = null, failed = false;
  try {
    const res = await fetch("/api/fs/search?limit=40&q=" + encodeURIComponent(q), { signal: ac.signal });
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
// lives ("…/internal/web/static" is useful; "culiops-agent-console/inter…" is not).
function tailDirs(dir, keep) {
  const parts = dir.split("/");
  keep = keep || 3;
  return parts.length <= keep ? dir : "…/" + parts.slice(-keep).join("/");
}

// --- rendering ---------------------------------------------------------------
function palRender(items, note) {
  palItems = items;
  if (palSel >= items.length) palSel = 0;
  const ul = document.getElementById("pal-list");
  ul.replaceChildren(...items.map((it, i) => {
    const li = document.createElement("li");
    if (i === palSel) li.className = "sel";
    const kind = document.createElement("span");
    kind.className = "pal-kind";
    kind.textContent = it.kind;
    const label = document.createElement("span");
    label.className = "pal-label";
    label.textContent = it.label;
    label.title = it.title || it.label;
    li.append(kind, label);
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
    li.onmouseenter = () => palSelect(i);
    li.onclick = () => runItem(it);
    return li;
  }));
  document.getElementById("pal-empty").hidden = items.length > 0;
  document.getElementById("pal-note").textContent = note || "";
}

function palSelect(i) {
  if (!palItems.length) return;
  const ul = document.getElementById("pal-list");
  palSel = (i + palItems.length) % palItems.length;
  for (let k = 0; k < ul.children.length; k++) ul.children[k].classList.toggle("sel", k === palSel);
  const el = ul.children[palSel];
  if (el) el.scrollIntoView({ block: "nearest" });
}

// --- wiring ------------------------------------------------------------------
function initPalette() {
  const d = document.getElementById("palette");
  const inp = document.getElementById("pal-input");
  if (!d || !inp) return;
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
  const combo = mac ? "⌘K" : "Ctrl+Shift+K";
  // The fallback is written next to the primary everywhere the combo appears:
  // when another app has grabbed the primary system-wide, the user needs to be
  // told a second one exists: they cannot guess it.
  const combos = combo + " or Ctrl+Shift+Space";

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
    if (long) long.textContent = mac ? "⌘K" : "^⇧K";
    else openBtn.textContent = mac ? "⌘K" : "^⇧K";
    openBtn.title = "Command palette (" + combos + ")";
    openBtn.onclick = openPalette;
  }

  // ^ is wrong on mac, where the same binding is ⌘. The hint is the only place
  // this shortcut is written down.
  const insHint = document.getElementById("pal-insert-hint");
  if (insHint) insHint.textContent = (mac ? "⌘↵" : "^↵") + " insert path";

  inp.addEventListener("input", palRefresh);
  inp.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); palSelect(palSel + 1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); palSelect(palSel - 1); }
    else if (isInsertHotkey(e, mac) && palItems[palSel] && palItems[palSel].path) {
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

  // Hotkey: see isPaletteHotkey for which combos and why. The ⌕ header button
  // is the mouse path, and the guaranteed one when something outside the page
  // claims a combo: a browser shortcut, or another app's system-wide hotkey.
  document.addEventListener("keydown", (e) => {
    if (!isPaletteHotkey(e, mac) || paletteOpen()) return;
    // Never steal the combo from a real text field (inline rename, path box,
    // profile editor). xterm's own focus target IS a textarea, so exempt that
    // one: opening the palette over a terminal is the whole point.
    const ae = document.activeElement;
    if (ae && (ae.tagName === "INPUT" ||
        (ae.tagName === "TEXTAREA" && !ae.classList.contains("xterm-helper-textarea")))) return;
    e.preventDefault();
    e.stopPropagation();
    openPalette();
  }, true);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { isPaletteHotkey, isInsertHotkey };
}
