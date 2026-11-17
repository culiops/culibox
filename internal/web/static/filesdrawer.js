"use strict";

// The desktop Files drawer's viewer column (spec 5.4). Its actions are the shared
// viewer toolbar's (viewertoolbar.js), inside the column; the header keeps the title,
// the session, maximize and close. The
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
let drawerFileMem = new Map(); // session id -> {path, scroll}
let drawerAsking = false;     // a discard prompt is on screen: never stack a second
// The user answered Cancel to "discard?" on a focus switch: {sid, gen}, the
// buffer's session and its CodeMirror change generation then. Until focus is
// back on that session or the buffer changes, a later focus event (a click,
// a terminal's cwd arriving) holds quietly instead of asking again.
let drawerKept = null;

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
  drawerKept = null;
  if (drawerViewer && drawerViewer.editing) drawerViewer.exitEdit();
}

function drawerKeptStill() {
  const v = drawerViewer;
  return !!drawerKept && !!v && !!v.cm && drawerKept.sid === drawerViewerSid && v.cm.isClean(drawerKept.gen);
}

// drawerHoldOnFollow: focus moved to `next.sessionId` (drawerFollowFocus). True
// when the drawer must stay on its session instead: a dirty buffer, asked
// about now (once), or already kept by a Cancel. Discard follows focus, tree
// included (syncFocusedTree); Cancel records the keep.
function drawerHoldOnFollow(next) {
  if (!drawerWouldDiscard(drawerState, next)) { drawerKept = null; return false; }
  if (drawerAsking || drawerKeptStill()) return true;
  drawerConfirmLeave().then((ok) => {
    if (ok) { drawerDiscard(); syncFocusedTree(); return; }
    const v = drawerViewer;
    drawerKept = v && v.cm ? { sid: drawerViewerSid, gen: v.cm.changeGeneration() } : null;
  });
  return true;
}

// drawerFocusSame: focus is on the drawer's own session (again). A keep only
// lasts while focus is away from it.
function drawerFocusSame() {
  drawerKept = null;
  if (drawerState.open === "files") drawerPaint();
}

// drawerKeepsTree: the Files drawer is holding its session against the focused
// pane (a dirty buffer, drawerHoldOnFollow; or a pin on a session that is still
// live, dockFollow), so the tree must not follow focus either: header, viewer
// and tree name one session. syncFocusedTree asks before re-rooting.
function drawerKeepsTree() {
  if (!drawerOwnsFileClicks()) return false;
  const t = typeof cellTerminal === "function" ? cellTerminal(focusedCell) : null;
  const sid = t ? t.id : null;
  if (sid === drawerState.sessionId) return false;
  if (drawerState.pinned && dockFollow({ sid: drawerState.sessionId, pinned: true }, sid, drawerLiveSids()).pinned) return true;
  return drawerHasUnsaved();
}

// drawerSessionCwd: the drawer's session's workspace as far as the page knows
// (its terminal's resolved cwd, else the session list's), "" when unknown.
function drawerSessionCwd(sid) {
  if (!sid) return "";
  const t = typeof terminalTabFor === "function" ? terminalTabFor(sid) : null;
  if (t && t.cwd) return t.cwd;
  const s = (lastSessions || []).find((x) => x.id === sid);
  return (s && s.cwd) || "";
}

// drawerShowFile: a tree row was clicked while the drawer owns file clicks.
// Resolves false when the user kept a dirty buffer instead.
// opts = {view?}: "changes" (a tree badge's click) opens the file's diff vs HEAD,
// whether the file is already in the column or about to be mounted.
async function drawerShowFile(path, opts) {
  if (!path) return false;
  if (typeof normalizePath === "function") path = normalizePath(path);
  const changes = !!opts && opts.view === "changes";
  const v = drawerViewer;
  if (v && v.path === path && drawerViewerSid === drawerState.sessionId) {
    if (changes) v.showChanges(); // refuses (with a toast) while the file is being edited
    dockTreeFold(); // a file chosen from the column opened off the strip: the column folds again
    return true;
  }
  if (!(await drawerConfirmLeave())) return false;
  drawerParkViewer(); // remembers the file being left, for this session
  drawerMountViewer(drawerState.sessionId, path, changes ? { scroll: 0, view: "changes" } : { scroll: 0 });
  dockTreeFold();
  return true;
}

// drawerMountViewer builds the viewer for `path` and puts it in the column.
function drawerMountViewer(sid, path, opts) {
  const host = drawerEl("drawer-vhost");
  if (!host) return;
  const v = new ViewerTab(path, Object.assign({}, opts, { host: drawerToolbarHost }));
  v.onUpdate = () => drawerPaint(true);
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
  drawerFileMem = rememberDrawerFile(drawerFileMem, drawerViewerSid, { path: v.path, scroll: savedScroll(v) });
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
  if (f) drawerMountViewer(sid, f.path, { scroll: f.scroll, restored: true });
  else drawerPaint();
}

// drawerFilesApply: the drawer state changed from `prev` to `next` (app.js's
// applyDrawer and drawerFollowFocus, after the dirty gate). Leaving the
// session's Files view parks its file; arriving recalls the new session's.
function drawerFilesApply(prev, next) {
  const was = !!prev && prev.open === "files";
  const now = !!next && next.open === "files";
  const moved = !prev || !next || prev.sessionId !== next.sessionId;
  if (was && (!now || moved)) {
    drawerParkViewer(); drawerGoReset();
    if (prev.sessionId) drawerChangedFiles.delete(prev.sessionId);
  }
  if (now && (!was || moved)) { drawerPaintChanged(); drawerLoadChanged(next.sessionId); }
  else if (!now) drawerPaintChanged();
  if (now && (!was || moved || !drawerViewer)) drawerRecall(next.sessionId);
  else drawerPaint();
}

// drawerFilesDetach: the viewport crossed into mobile (restoreDrawerNodes). A
// clean viewer is parked like a close; a dirty one is kept whole, detached, so
// nothing typed is lost (beforeunload still guards it) and the next desktop
// Files drawer for its session puts it back as it was.
function drawerFilesDetach() {
  drawerPaintChanged(); // the mobile tree carries no dots (drawerOwnsFileClicks is false by now)
  const v = drawerViewer;
  if (!v) return;
  if (!drawerHasUnsaved()) { drawerParkViewer(); return; }
  if (v.auto) v.toggleAuto();
  drawerFileMem = rememberDrawerFile(drawerFileMem, drawerViewerSid, { path: v.path, scroll: savedScroll(v) });
  v.el.remove();
}

// --- agent-changed dots ------------------------------------------------------
// The tree marks the files the agent changed (and the folders above them). The
// list comes from the Activity endpoint's `files` (Task 3: absolute paths as the
// agent wrote them, most recent first), asked when the drawer opens on a session
// and on every tree refresh. The answer is only a hint: any failure leaves the
// tree as it was, with no dot, and a box session whose paths differ from the
// tree's just shows none.

let drawerChangedFiles = new Map(); // session id -> [{path, add, del}]
let drawerChangedSeq = 0;           // a reply is dropped unless it is the newest ask

// drawerChangedFor: the set of tree paths to mark under `root` for the
// drawer's session (empty when nothing is known).
function drawerChangedFor(root) {
  return changedSet(drawerChangedFiles.get(drawerState.sessionId), root);
}

// drawerPaintChanged brings the dots on the tree in step with what is known,
// and takes them all off whenever the drawer is not what shows the tree.
function drawerPaintChanged() {
  if (typeof ftPaintChanged !== "function") return;
  ftPaintChanged(drawerOwnsFileClicks() && typeof ftRoot === "function" ? drawerChangedFor(ftRoot()) : null);
}

async function drawerLoadChanged(sid) {
  if (!sid) return;
  const seq = ++drawerChangedSeq;
  let files;
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(sid) + "/activity?limit=1");
    const body = await accReadReply(res);
    if (body && Array.isArray(body.files)) files = body.files;
  } catch (_) { /* offline: no dots */ }
  // Newer ask, or the drawer moved on (another session, closed): not for now.
  if (seq !== drawerChangedSeq || !drawerOwnsFileClicks() || drawerState.sessionId !== sid) return;
  if (!files) return; // could not read it: keep what was shown
  drawerChangedFiles.set(sid, files);
  drawerPaintChanged();
}

// --- Go to file… (Ctrl+P) ----------------------------------------------------
// A filename jump at the top of the tree column, over the existing bounded file
// search (goToFileURL). Thin by design (north star): it finds a FILE BY NAME
// under the tree's root and shows it in the viewer; it is not search-in-files.
// The debounce + AbortController are the directory picker's pattern.

const DRAWER_GO_DEBOUNCE_MS = 150, DRAWER_GO_FETCH_MS = 4000;
let drawerGoSeq = 0, drawerGoTimer = null, drawerGoAbort = null;
let drawerGoRows = [], drawerGoActive = -1;

function drawerGoStop() {
  drawerGoSeq++; // a late reply is for a query nobody is waiting on
  if (drawerGoTimer) { clearTimeout(drawerGoTimer); drawerGoTimer = null; }
  if (drawerGoAbort) { drawerGoAbort.abort(); drawerGoAbort = null; }
}

function drawerGoClose() {
  const list = drawerEl("drawer-goto-list");
  const input = drawerEl("drawer-goto-in");
  drawerGoRows = []; drawerGoActive = -1;
  if (list) { list.hidden = true; list.replaceChildren(); }
  if (input) { input.setAttribute("aria-expanded", "false"); input.removeAttribute("aria-activedescendant"); }
}

// drawerGoReset: the query and its list are for one session's tree; leaving it
// (close, another session) drops both.
function drawerGoReset() {
  drawerGoStop();
  drawerGoClose();
  const input = drawerEl("drawer-goto-in");
  if (input) input.value = "";
}

function drawerGoPosition() {
  const input = drawerEl("drawer-goto-in");
  const list = drawerEl("drawer-goto-list");
  if (!input || !list) return;
  const r = input.getBoundingClientRect();
  // The explorer is the dock's right-hand column, so the field sits at the window's right edge: the list is 340px at
  // least, and slides left to stay on screen rather than shrinking to the few pixels that remain beside the field.
  const w = Math.min(Math.max(r.width, 340), window.innerWidth - 16);
  const left = Math.max(8, Math.min(r.left, window.innerWidth - w - 8));
  list.style.left = Math.round(left) + "px";
  list.style.top = Math.round(r.bottom + 2) + "px";
  list.style.width = Math.round(w) + "px";
}

// drawerGoRender: rows are {label, hint, path}; `note` (no rows, or the search
// failed) is one muted line that is not an option, so arrows and Enter skip it.
function drawerGoRender(rows, note) {
  const list = drawerEl("drawer-goto-list");
  const input = drawerEl("drawer-goto-in");
  if (!list || !input) return;
  drawerGoRows = rows; drawerGoActive = -1;
  input.removeAttribute("aria-activedescendant");
  const els = rows.map((r, i) => {
    const el = document.createElement("div");
    el.className = "ft-opt";
    el.id = "drawer-goto-opt-" + i;
    el.setAttribute("role", "option");
    el.setAttribute("aria-selected", "false");
    el.title = r.path;
    const lab = document.createElement("span");
    lab.textContent = r.label;
    const hint = document.createElement("span");
    hint.className = "hint";
    hint.textContent = r.hint;
    el.append(lab, hint);
    el.onclick = () => drawerGoChoose(i);
    return el;
  });
  if (note) {
    const el = document.createElement("div");
    el.className = "ft-note";
    el.setAttribute("role", "option");
    el.setAttribute("aria-disabled", "true");
    el.textContent = note;
    els.push(el);
  }
  list.replaceChildren(...els);
  // A click on a row must not blur the input first (blur closes the list).
  list.onmousedown = (e) => e.preventDefault();
  drawerGoPosition();
  list.hidden = els.length === 0;
  input.setAttribute("aria-expanded", list.hidden ? "false" : "true");
}

function drawerGoMove(delta) {
  const list = drawerEl("drawer-goto-list");
  const input = drawerEl("drawer-goto-in");
  const n = drawerGoRows.length;
  if (!list || !input || !n) return;
  const was = list.children[drawerGoActive];
  if (was) was.setAttribute("aria-selected", "false");
  drawerGoActive = drawerGoActive < 0 ? (delta > 0 ? 0 : n - 1) : (drawerGoActive + delta + n) % n;
  const el = list.children[drawerGoActive];
  el.setAttribute("aria-selected", "true");
  input.setAttribute("aria-activedescendant", el.id);
  el.scrollIntoView({ block: "nearest" });
}

// drawerGoChoose opens hit `i` through drawerShowFile, so a dirty buffer still
// asks before it is replaced. If the user keeps the buffer, nothing opens and
// the query stays in the box.
async function drawerGoChoose(i) {
  const r = drawerGoRows[i];
  if (!r) return;
  if (!(await drawerShowFile(r.path))) return;
  drawerGoReset();
  const input = drawerEl("drawer-goto-in");
  if (input) input.blur();
}

async function drawerGoSearch(q, seq) {
  const ac = new AbortController();
  drawerGoAbort = ac;
  const timer = setTimeout(() => ac.abort(), DRAWER_GO_FETCH_MS);
  const root = typeof ftRoot === "function" ? ftRoot() : "";
  let data = null, failed = false;
  try {
    const res = await fetch(goToFileURL(q, root), { signal: ac.signal });
    if (res.ok) data = await res.json();
    else failed = true;
  } catch (_) {
    failed = true; // offline, timed out, or aborted by a newer query (guarded below)
  } finally { clearTimeout(timer); }
  if (seq !== drawerGoSeq) return;
  drawerGoAbort = null;
  if (failed || !data) { drawerGoRender([], "file search unavailable"); return; }
  const rows = hitsUnderRoot(data.results || [], root).map((h) => {
    const rel = relativeTo(root, h.path);
    const cut = rel.lastIndexOf("/");
    return { label: cut >= 0 ? rel.slice(cut + 1) : rel, hint: cut >= 0 ? rel.slice(0, cut) : "", path: h.path };
  });
  drawerGoRender(rows, rows.length ? "" : data.truncated ? "no match yet, the index is still filling" : "no matching files");
}

function drawerGoInput() {
  const input = drawerEl("drawer-goto-in");
  if (!input) return;
  drawerGoStop();
  // The rows on screen are for the previous text: Enter during the debounce
  // must not open one of them.
  drawerGoRows = []; drawerGoActive = -1;
  input.removeAttribute("aria-activedescendant");
  const q = input.value.trim();
  if (!q) { drawerGoClose(); return; }
  const seq = drawerGoSeq;
  drawerGoTimer = setTimeout(() => { drawerGoTimer = null; drawerGoSearch(q, seq); }, DRAWER_GO_DEBOUNCE_MS);
}

function drawerGoKey(e) {
  const input = e.currentTarget;
  const list = drawerEl("drawer-goto-list");
  const open = !!list && !list.hidden;
  if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    if (!open) return;
    e.preventDefault();
    drawerGoMove(e.key === "ArrowDown" ? 1 : -1);
  } else if (e.key === "Enter") {
    e.preventDefault();
    if (!drawerGoRows.length) return;
    drawerGoChoose(drawerGoActive >= 0 ? drawerGoActive : 0);
  } else if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation(); // clears the query; the drawer itself stays
    if (input.value || open) drawerGoReset();
    else input.blur();
  }
}

// drawerGoFocus: Ctrl+P. True when it took the key. Only while the Files
// drawer is open on desktop with a tree to search; anywhere else the browser
// keeps its own Ctrl+P.
function drawerGoFocus() {
  const input = drawerEl("drawer-goto-in");
  if (!input || !drawerOwnsFileClicks()) return false;
  const dr = drawerEl("drawer");
  if (!dr || dr.hidden || dr.dataset.noroot === "1") return false;
  dockTreeReveal(); // a folded explorer opens as its column first: a field that is not on screen cannot take focus
  input.focus();
  input.select();
  return true;
}

function initDrawerGoto() {
  const input = drawerEl("drawer-goto-in");
  if (!input) return;
  input.title = "Go to file (" + shortcutHint("files.goto", IS_MAC) + ")";
  const strip = drawerEl("drawer-strip-goto"); // the folded explorer's own Go to file
  if (strip) strip.title = input.title;
  input.addEventListener("input", drawerGoInput);
  input.addEventListener("keydown", drawerGoKey);
  // Stop first: a debounce or fetch still pending would re-open the list over
  // the viewer after the input has lost focus, and nothing would close it.
  const dismiss = () => { drawerGoStop(); drawerGoClose(); };
  input.addEventListener("blur", dismiss);
  input.addEventListener("focus", () => { if (input.value.trim()) drawerGoInput(); });
  window.addEventListener("resize", dismiss);
  // Ctrl+P goes through the shortcut dispatcher (app.js), which keeps it off a
  // terminal (readline's previous-history), a dialog and a page. drawerGoFocus
  // returns false when there is no tree to search, so the browser keeps its print.
  bindShortcut("files.goto", () => drawerGoFocus());
}

// --- toolbar host -------------------------------------------------------------

// What the drawer adds to the shared toolbar: Open in pane on the primary row, and
// the path as the tree shows it (relative to the root: the column is narrow).
const drawerToolbarHost = {
  extras: [{ key: "openInPane", label: "Open in pane", title: "Open this file in a pane, at the same place", run: () => drawerOpenInPane() }],
  pathText: (v) => relativeTo(typeof ftRoot === "function" ? ftRoot() : "", v.path),
};

// drawerOpenInPane hands the file to a pane at the scroll it is read at, then
// clears the drawer's viewer (openFile itself closes the Files drawer).
function drawerOpenInPane() {
  const v = drawerViewer;
  if (!v || v.editing) return;
  const path = v.path, scroll = v.scroll, changes = !!v.changes, fileScroll = v.fileScroll;
  drawerClearViewer();
  openFile(path, changes ? { view: "changes" } : undefined); // "at the same place" includes the diff
  for (const c of cells) {
    const t = c.tabs.find((x) => x.kind === "viewer" && !x.box && x.path === path);
    if (t) {
      t.scroll = scroll;
      // The file's own offset comes along, so Show file / Changes off lands where it was read.
      if (t.changes && Number.isFinite(fileScroll)) t.fileScroll = fileScroll;
      t.applyScroll();
      break;
    }
  }
}

// drawerPaint brings the column and the dataset flags in step with the viewer.
// Cheap and idempotent: it runs on every viewer update (`fromViewer`: the viewer
// has just repainted its own toolbar) and when the session list or the tree root
// moves, which change the path label.
function drawerPaint(fromViewer) {
  const dr = drawerEl("drawer");
  if (!dr) return;
  const sess = drawerState.sessionId ? (lastSessions || []).find((x) => x.id === drawerState.sessionId) : null;
  // A box session's tree shows only its workspace: the root picker goes.
  // (Repainted on every session poll: at a boot restore the list arrives
  // after the drawer has opened.)
  dr.dataset.box = sess && sess.runtime === TIER_BOX ? "1" : "";
  // A workspace the fs API refuses is never a root (S1), so instead of the
  // tree some other place would leave there, the column says so.
  const cwd = drawerSessionCwd(drawerState.sessionId);
  const blocked = drawerState.open === "files" &&
    drawerTreeBlocked(drawerState.sessionId, cwd, typeof rootableCwd === "function" && rootableCwd(cwd));
  dr.dataset.noroot = blocked ? "1" : "";
  const v = drawerState.open === "files" && drawerViewerSid === drawerState.sessionId ? drawerViewer : null;
  if (v && fromViewer !== true) v.toolbarApi.paint();
  const host = drawerEl("drawer-vhost");
  const empty = drawerEl("drawer-vempty");
  if (host) host.hidden = !v;
  if (empty) empty.hidden = !!v;
}

initDrawerGoto();
