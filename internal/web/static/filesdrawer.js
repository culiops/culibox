"use strict";

// The desktop Files drawer's viewer column and header actions (spec 5.4). The
// tree column is the existing #files-pane, moved in by app.js's applyDrawer;
// this file adds a ViewerTab beside it that belongs to no cell, so a file can be
// read (and thinly edited) without leaving the session's terminal. Mobile never
// sees any of it: #drawer is display:none at <= 700px (app.css), and filetree.js
// only routes a row click here while drawerOwnsFileClicks() says so.
//
// What it owns: one ViewerTab (drawerViewer) for the drawer's session, the
// per-session {path, scroll} memory (drawerfiles.js), and the dirty-buffer
// gate every way out of the file goes through (drawerConfirmLeave).
// Thin by design (north star): one file at a time, the viewer's own editor.

let drawerViewer = null;      // the ViewerTab in the viewer column, or null
let drawerViewerSid = null;   // the session it was opened for
let drawerFileMem = new Map(); // session id -> {path, scroll[, box]}
let drawerAsking = false;     // a discard prompt is on screen: never stack a second
let drawerActBtns = null;     // data-act -> button
let drawerMenuBtns = null;    // data-m -> menu item

function drawerEl(id) { return document.getElementById(id); }

// drawerOwnsFileClicks: a tree row click shows the file here, not in a pane.
// Only on desktop and only while the drawer shows Files (the tree is visible
// nowhere else on desktop; on mobile it is the Files screen, unchanged).
function drawerOwnsFileClicks() {
  return typeof drawerDesktop === "function" && drawerDesktop() && drawerState.open === "files";
}

function drawerViewerTab() { return drawerViewer; }

function drawerHasUnsaved() { return !!(drawerViewer && drawerViewer.dirty && drawerViewer.dirty()); }

// drawerWouldDiscard: would going from `prev` to `next` drawer state throw away
// a dirty buffer? Leaving the file (drawerLeavesFile), or opening Files for a
// session other than the one the buffer belongs to (a buffer parked by the
// crossing into mobile, see drawerFilesDetach).
function drawerWouldDiscard(prev, next) {
  if (!drawerHasUnsaved()) return false;
  if (drawerLeavesFile(prev, next)) return true;
  return !!next && next.open === "files" && next.sessionId !== drawerViewerSid;
}

// drawerConfirmLeave resolves true when the drawer's file may be let go: it is
// clean, or the user said Discard. One prompt at a time: a second ask on the
// shared dialog would hijack the first (memory ask-dialog-reentrancy), so a
// caller arriving while one is open is simply told no.
async function drawerConfirmLeave() {
  if (!drawerHasUnsaved()) return true;
  if (drawerAsking) return false;
  drawerAsking = true;
  try {
    return await askConfirm(discardAsk(drawerViewer.name));
  } finally {
    drawerAsking = false;
  }
}

// drawerDiscard drops the dirty edit (the user confirmed): back to the read view.
function drawerDiscard() {
  if (drawerViewer && drawerViewer.editing) drawerViewer.exitEdit();
}

// drawerShowFile: a tree row was clicked while the drawer owns file clicks.
// Resolves false when the user kept a dirty buffer instead.
async function drawerShowFile(path, opts) {
  if (!path) return false;
  if (typeof normalizePath === "function") path = normalizePath(path);
  const box = (opts && opts.box) || "";
  const v = drawerViewer;
  if (v && v.path === path && (v.box || "") === box && drawerViewerSid === drawerState.sessionId) return true;
  if (!(await drawerConfirmLeave())) return false;
  drawerParkViewer(); // remembers the file being left, for this session
  drawerMountViewer(drawerState.sessionId, path, { box, scroll: 0 });
  return true;
}

// drawerMountViewer builds the viewer for `path` and puts it in the column.
function drawerMountViewer(sid, path, opts) {
  const host = drawerEl("drawer-vhost");
  if (!host) return;
  const v = new ViewerTab(path, opts);
  v.onUpdate = drawerPaint;
  drawerViewer = v;
  drawerViewerSid = sid;
  host.replaceChildren(v.el);
  v.onAttach();
  drawerPaint();
}

// drawerParkViewer lets go of the viewer, remembering where it was for its
// session. Callers have already passed the dirty gate.
function drawerParkViewer() {
  const v = drawerViewer;
  if (!v) return;
  drawerFileMem = rememberDrawerFile(drawerFileMem, drawerViewerSid, { path: v.path, scroll: v.scroll, box: v.box });
  v.dispose();
  v.onUpdate = null;
  v.el.remove();
  drawerViewer = null;
  drawerViewerSid = null;
}

// drawerClearViewer empties the column and forgets the file for its session:
// Open in pane took it, or the file is gone (closeTabEverywhere /
// closeViewersFor reach here, as they reach a pane's viewer).
function drawerClearViewer() {
  const v = drawerViewer;
  if (!v) return;
  drawerFileMem = rememberDrawerFile(drawerFileMem, drawerViewerSid, { path: null });
  v.dispose();
  v.onUpdate = null;
  v.el.remove();
  drawerViewer = null;
  drawerViewerSid = null;
  drawerPaint();
}

// drawerRecall shows the session's remembered file at its remembered scroll,
// or the empty column. `restored` lets a file deleted since then close itself
// on its first read, as a restored pane viewer does.
function drawerRecall(sid) {
  if (drawerViewer && drawerViewerSid === sid) { // parked by the mobile crossing
    const host = drawerEl("drawer-vhost");
    if (host && drawerViewer.el.parentElement !== host) host.replaceChildren(drawerViewer.el);
    drawerViewer.onAttach();
    if (drawerViewer.cm) drawerViewer.cm.refresh(); // CodeMirror measured nothing while detached
    drawerPaint();
    return;
  }
  drawerParkViewer();
  const f = drawerFileFor(drawerFileMem, sid);
  if (f) drawerMountViewer(sid, f.path, { scroll: f.scroll, restored: true, box: f.box });
  else drawerPaint();
}

// drawerFilesApply: the drawer state changed from `prev` to `next` (app.js's
// applyDrawer and drawerFollowFocus, after the dirty gate). Leaving the
// session's Files view parks its file; arriving recalls the new session's.
function drawerFilesApply(prev, next) {
  const was = !!prev && prev.open === "files";
  const now = !!next && next.open === "files";
  const moved = !prev || !next || prev.sessionId !== next.sessionId;
  if (was && (!now || moved)) drawerParkViewer();
  if (now && (!was || moved || !drawerViewer)) drawerRecall(next.sessionId);
  else drawerPaint();
}

// drawerFilesDetach: the viewport crossed into mobile (restoreDrawerNodes). A
// clean viewer is parked like a close; a dirty one is kept whole, detached, so
// nothing typed is lost (beforeunload still guards it) and the next desktop
// Files drawer for its session puts it back as it was.
function drawerFilesDetach() {
  const v = drawerViewer;
  if (!v) return;
  if (!drawerHasUnsaved()) { drawerParkViewer(); return; }
  if (v.auto) v.toggleAuto();
  drawerFileMem = rememberDrawerFile(drawerFileMem, drawerViewerSid, { path: v.path, scroll: v.scroll, box: v.box });
  v.el.remove();
}

// --- header actions ----------------------------------------------------------

function drawerInsertPath() {
  const v = drawerViewer;
  if (!v) return;
  // The drawer's session, not whichever terminal has the keys: the header
  // names this session, and a docked shell or another pane may hold focus.
  const term = drawerState.sessionId ? terminalTabFor(drawerState.sessionId) : null;
  const res = insertTextAtPrompt(term, shellQuote(v.path) + " ");
  if (res.ok) { if (term.term) term.term.focus(); return; }
  showToast(
    res.reason === "noterm" ? "This session has no terminal open here, so the path was not inserted" :
    res.reason === "unsafe" ? "That path contains control characters, so it was not inserted" :
    "That session is not connected, so the path was not inserted",
    { error: true });
}

// drawerOpenInPane hands the file to a pane at the scroll it is read at, then
// clears the drawer's viewer (openFile itself closes the Files drawer).
function drawerOpenInPane() {
  const v = drawerViewer;
  if (!v || v.editing) return;
  const path = v.path, box = v.box, scroll = v.scroll;
  drawerClearViewer();
  if (box) openBoxFile(box, path); else openFile(path);
  for (const c of cells) {
    const t = c.tabs.find((x) => x.kind === "viewer" && (x.box || "") === (box || "") && x.path === path);
    if (t) { t.scroll = scroll; t.applyScroll(); break; }
  }
}

function drawerMenuOpen(open) {
  const menu = drawerEl("drawer-menu");
  if (!menu || !drawerActBtns) return;
  menu.hidden = !open;
  drawerActBtns.overflow.setAttribute("aria-expanded", open ? "true" : "false");
}

function drawerAct(k, btn) {
  const v = drawerViewer;
  if (!v) return;
  if (k === "copy") v.copy(btn);
  else if (k === "insert") drawerInsertPath();
  else if (k === "edit") v.enterEdit();
  else if (k === "openInPane") drawerOpenInPane();
  else if (k === "overflow") drawerMenuOpen(drawerEl("drawer-menu").hidden);
  else if (k === "cancel") v.cancelEdit();
  else if (k === "save") v.save(false);
}

function drawerMenuAct(k) {
  const v = drawerViewer;
  if (!v) return;
  if (k === "reload") v.reload();
  else if (k === "download") v.download();
  else if (k === "auto") v.toggleAuto();
  else if (k === "smaller") v.fontStep(-1);
  else if (k === "bigger") v.fontStep(+1);
  // A−/A+ are pressed in a row: the menu stays for them.
  if (k !== "smaller" && k !== "bigger") drawerMenuOpen(false);
  drawerPaint();
}

// drawerPaint brings the header actions, the path line and the column in step
// with the viewer. Cheap and idempotent: it runs on every viewer update.
function drawerPaint() {
  const dr = drawerEl("drawer");
  if (!dr || !drawerActBtns) return;
  const sess = drawerState.sessionId ? (lastSessions || []).find((x) => x.id === drawerState.sessionId) : null;
  // A box session's tree shows only its workspace: the root picker goes.
  dr.dataset.box = sess && sess.runtime === TIER_BOX ? "1" : "";
  const v = drawerState.open === "files" && drawerViewerSid === drawerState.sessionId ? drawerViewer : null;
  const image = !!v && IMAGE_EXTS.has(extOf(v.path));
  const a = drawerActions({ path: v ? v.path : null, editing: !!(v && v.editing), box: !!(v && v.box), dirty: drawerHasUnsaved(), image });
  const editing = a.cancel;
  for (const [k, b] of Object.entries(drawerActBtns)) {
    const editRow = k === "cancel" || k === "save";
    b.hidden = editRow ? !editing : editing;
    b.disabled = !a[k];
  }
  if (!a.overflow) drawerMenuOpen(false);
  if (drawerMenuBtns) {
    const text = !!v && !image;
    drawerMenuBtns.auto.disabled = !text;
    drawerMenuBtns.auto.classList.toggle("on", !!(v && v.auto));
    drawerMenuBtns.auto.setAttribute("aria-checked", v && v.auto ? "true" : "false");
    drawerMenuBtns.smaller.disabled = !text;
    drawerMenuBtns.bigger.disabled = !text;
  }
  const pathEl = drawerEl("drawer-vpath");
  const stEl = drawerEl("drawer-vst");
  const host = drawerEl("drawer-vhost");
  const empty = drawerEl("drawer-vempty");
  if (pathEl) {
    const shown = v ? (v.box ? "box · " + v.path : relativeTo(typeof ftRoot === "function" ? ftRoot() : "", v.path)) : "";
    pathEl.textContent = shown;
    pathEl.title = v ? v.path : "";
  }
  if (stEl) stEl.textContent = v && v.statusText ? "· " + v.statusText : "";
  if (host) host.hidden = !v;
  if (empty) empty.hidden = !!v;
}

// initFilesDrawer builds the header's action buttons and the overflow menu
// (labels as text, never markup) and wires them. Runs once, at load: the
// drawer's markup is already in the page above the scripts.
function initFilesDrawer() {
  const acts = drawerEl("drawer-acts");
  const menu = drawerEl("drawer-menu");
  if (!acts || !menu || drawerActBtns) return;
  const mk = (k, label, title, extra) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "dr-act" + (extra ? " " + extra : "");
    b.dataset.act = k;
    b.textContent = label;
    b.title = title;
    b.onclick = (e) => { e.stopPropagation(); drawerAct(k, b); };
    // Nothing to act on until a file is shown (drawerPaint takes over then;
    // it cannot run here, before app.js has declared the drawer's state).
    b.disabled = true;
    b.hidden = k === "cancel" || k === "save";
    return b;
  };
  drawerActBtns = {
    copy: mk("copy", "Copy", "Copy this file's contents"),
    insert: mk("insert", "Insert path", "Type this file's path at this session's prompt"),
    edit: mk("edit", "Edit", "Edit this file"),
    openInPane: mk("openInPane", "Open in pane", "Open this file in a pane, at the same place"),
    overflow: mk("overflow", "⋯", "More file actions"),
    cancel: mk("cancel", "Cancel", "Discard changes and return to the read view"),
    save: mk("save", "Save", "Save (Ctrl/Cmd-S)", "pri"),
  };
  drawerActBtns.overflow.setAttribute("aria-label", "More file actions");
  drawerActBtns.overflow.setAttribute("aria-haspopup", "menu");
  drawerActBtns.overflow.setAttribute("aria-expanded", "false");
  acts.prepend(...Object.values(drawerActBtns));
  const item = (k, label, title, role) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "dr-mi";
    b.setAttribute("role", role || "menuitem");
    b.textContent = label;
    b.title = title;
    b.onclick = (e) => { e.stopPropagation(); drawerMenuAct(k); };
    return b;
  };
  drawerMenuBtns = {
    reload: item("reload", "Reload", "Re-read this file"),
    download: item("download", "Download", "Download this file"),
    auto: item("auto", "Auto 2s", "Auto-reload every 2s", "menuitemcheckbox"),
    smaller: item("smaller", "A−", "Smaller text"),
    bigger: item("bigger", "A+", "Bigger text"),
  };
  menu.replaceChildren(...Object.values(drawerMenuBtns));
  document.addEventListener("click", (e) => { if (!menu.hidden && !menu.contains(e.target)) drawerMenuOpen(false); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !menu.hidden) drawerMenuOpen(false); });
}

initFilesDrawer();
