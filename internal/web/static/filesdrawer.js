"use strict";

// The desktop Files dock's editor column (spec 5.4): the tab strip, the crumbs and
// the viewer under them. Its actions are the shared viewer toolbar's
// (viewertoolbar.js), inside the column; the dock's header keeps the title, the
// session, maximize and close. The tree column is the existing #files-pane, moved in
// by app.js's applyDrawer; this file adds the ViewerTabs beside it that belong to no
// cell, so a file can be read (and thinly edited) without leaving the session's
// terminal. Mobile never sees any of it: #drawer is display:none at <= 700px
// (app.css), and filetree.js only routes a row click here while
// drawerOwnsFileClicks() says so.
//
// What it owns:
//  - the tab state of every session this page has shown (dockStates, the shape and
//    rules of docktabs.js), and what is stored of it (DOCK_TABS_KEY);
//  - the ViewerTabs of the SHOWN session only (dockViewers). A tab gets its viewer
//    the first time it is shown, and keeps it while its session stays on screen:
//    switching tabs hides the viewer (onHide) and attaches the other, so an edit
//    survives. Another session's tabs are parked: their viewers are disposed and
//    their scroll is remembered in the state;
//  - the dirty-buffer gate every way out of the files goes through
//    (drawerConfirmLeave): switching session, closing the dock and a follow-focus
//    move ask when ANY tab of the shown session holds an edit, and so does closing
//    a dirty tab.
// Thin by design (north star): the viewer's own editor, a few tabs.

// session id -> {tabs, active} (docktabs.js). null is a pane of files, which has no
// session: its tabs live here too but are never stored.
let dockStates = new Map();
// The live viewers by tab key, all for dockViewersSid. A tab key is the same string
// in every session, so the map is meaningless without the sid it belongs to.
const dockViewers = new Map();
let dockViewersSid = null;
// The stored tabs have been read (dockRestore, once the session list is known).
// Nothing is written before: parseDock drops every session that is not in the live
// list, so a write from a page that had not read them yet would replace the stored
// tabs with nothing.
let dockLoaded = false;
let dockUnsaved = false; // a change is not written yet; pagehide writes it
let dockSaveTimer = null;
const DOCK_SAVE_MS = 300;
let drawerAsking = false;     // a discard prompt is on screen: never stack a second
// The user answered Cancel to "discard?" on a focus switch: {sid, gens}, the shown
// session and, for each dirty tab, its buffer's CodeMirror change generation then.
// Until focus is back on that session or a buffer changes (or another becomes
// dirty), a later focus event (a click, a terminal's cwd arriving) holds quietly
// instead of asking again.
let drawerKept = null;

function drawerEl(id) { return document.getElementById(id); }

// drawerOwnsFileClicks: a tree row click shows the file here, not in a pane.
// Only on desktop and only while the drawer shows Files (the tree is visible
// nowhere else on desktop; on mobile it is the Files screen, unchanged).
function drawerOwnsFileClicks() {
  return typeof drawerDesktop === "function" && drawerDesktop() && drawerState.open === "files";
}

// --- state --------------------------------------------------------------------

function dockStateOf(sid) { return dockStates.get(sid) || { tabs: [], active: null }; }

// dockSetState keeps `st` as the session's state (no tab: no entry) and notes that
// there is something to store.
function dockSetState(sid, st) {
  if (dockTabsOf(st).length) dockStates.set(sid, st);
  else dockStates.delete(sid);
  dockSaveSoon();
}

function dockViewerList() { return [...dockViewers.values()]; }
function dockHasViewer(v) { return !!v && dockViewerList().includes(v); }

// dockKeyOf: the key of a live viewer's tab, null for one the dock does not hold.
function dockKeyOf(v) {
  for (const [key, x] of dockViewers) if (x === v) return key;
  return null;
}

function dockDirtyKey(key) {
  const v = dockViewers.get(key);
  return !!(v && v.dirty && v.dirty());
}

// drawerHasUnsaved: does any tab of the session whose viewers are live hold an
// unsaved edit? (dockLeaveNeedsAsk: a background tab's edit counts as much as the
// shown one's.) beforeunload asks it too, wherever the dock is.
function drawerHasUnsaved() {
  return dockLeaveNeedsAsk(dockStateOf(dockViewersSid), dockDirtyKey);
}

// drawerWouldDiscard: would going from `prev` to `next` drawer state throw away
// a dirty buffer? Leaving the files (drawerLeavesFile), or opening Files for a
// session other than the one the buffers belong to (buffers kept by the crossing
// into mobile, see drawerFilesDetach).
function drawerWouldDiscard(prev, next) {
  if (!drawerHasUnsaved()) return false;
  if (drawerLeavesFile(prev, next)) return true;
  return !!next && next.open === "files" && next.sessionId !== dockViewersSid;
}

// dockDirtyNames: the names of the tabs holding an edit, for the ask.
function dockDirtyNames() {
  return dockTabsOf(dockStateOf(dockViewersSid)).filter((t) => dockDirtyKey(t.key)).map((t) => dockTabLabel(t.path, t.box).name);
}

// drawerConfirmLeave resolves true when the dock's files may be let go: none holds
// an edit, or the user said Discard. One prompt at a time: a second ask on the
// shared dialog would hijack the first (memory ask-dialog-reentrancy), so a
// caller arriving while one is open is simply told no.
async function drawerConfirmLeave() {
  if (!drawerHasUnsaved()) return true;
  if (drawerAsking) return false;
  drawerAsking = true;
  try {
    const names = dockDirtyNames();
    return await askConfirm(discardAsk(names.length === 1 ? names[0] : names.length + " files"));
  } finally {
    drawerAsking = false;
  }
}

// drawerDiscard drops the dirty edits (the user confirmed): back to the read view.
function drawerDiscard() {
  drawerKept = null;
  for (const v of dockViewers.values()) if (v.editing) v.exitEdit();
}

// drawerKeptStill: the Cancel still stands: for the same session, every dirty tab
// was dirty then and has not changed since (CodeMirror's change generation).
function drawerKeptStill() {
  const k = drawerKept;
  if (!k || k.sid !== dockViewersSid) return false;
  for (const [key, v] of dockViewers) {
    if (!dockDirtyKey(key)) continue;
    if (!k.gens.has(key) || !v.cm || !v.cm.isClean(k.gens.get(key))) return false;
  }
  return true;
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
    const gens = new Map();
    for (const [key, v] of dockViewers) if (v.cm && dockDirtyKey(key)) gens.set(key, v.cm.changeGeneration());
    drawerKept = { sid: dockViewersSid, gens };
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

// --- tabs -----------------------------------------------------------------------

// drawerShowFile: show a file in the dock, as a tab of the dock's session. Not an
// ask: opening never discards anything (a dirty tab stays open as a tab).
// opts = {line?, col?, alt?, view?, box?, preview?}:
//  - preview (true unless false is given): a single click opens a preview tab, which
//    the next single click replaces; a double-click passes false and pins it;
//  - line, col: where a terminal link asked to be, revealed in a viewer that is
//    already open too;
//  - alt: {base, rel} of a relative terminal link (the viewer looks in other
//    checkouts on a 404);
//  - view: "changes" opens the file's diff vs HEAD (a tree badge's click), in a
//    viewer that is open already too (it refuses, with a toast, while the file is
//    being edited);
//  - box: the session id of a file that exists only in that session's box.
// Returns false only for no path.
function drawerShowFile(path, opts) {
  if (!path) return false;
  if (typeof normalizePath === "function") path = normalizePath(path);
  const o = opts || {};
  const sid = drawerState.sessionId;
  const box = typeof o.box === "string" && o.box ? o.box : null;
  const key = dockTabKey(path, box);
  const open = { path, box, preview: o.preview !== false };
  const before = dockStateOf(sid);
  let st = before;
  let next = dockOpen(st, open);
  // A preview tab is replaced silently, so one holding an edit must not be: an edit pins its tab (the host's
  // onEditStart), so this is the second lock. Pinning it here leaves it a tab of its own beside the new one.
  for (const gone of dockRemoved(before, next)) {
    if (!dockDirtyKey(gone)) continue;
    st = dockPin(st, gone);
    next = dockOpen(st, open);
  }
  const had = sid === dockViewersSid ? dockViewers.get(key) : null;
  dockCommit(sid, next);
  dockShow({ user: key, line: o.line, col: o.col, alt: o.alt, view: o.view });
  if (had) { // an open viewer is told what the link or the badge asked of it
    if (o.line && typeof had.revealLine === "function") had.revealLine(o.line, o.col);
    if (o.view === "changes") had.showChanges(); // refuses (with a toast) while the file is being edited
  }
  dockTreeFold(); // a file chosen from the column opened off the strip: the column folds again
  return true;
}

// dockCommit: the session's tab state is now `next`. Viewers of the tabs that are
// gone go with them (a caller has asked about a dirty one), and the state is kept
// and stored. Showing what is now active is dockShow's.
function dockCommit(sid, next) {
  if (sid === dockViewersSid) for (const key of dockRemoved(dockStateOf(sid), next)) dockDropViewer(key);
  dockSetState(sid, next);
}

// dockDropViewer lets go of one viewer.
function dockDropViewer(key) {
  const v = dockViewers.get(key);
  if (!v) return;
  v.dispose();
  v.onUpdate = null;
  v.el.remove();
  dockViewers.delete(key);
  if (!dockViewers.size) dockViewersSid = null;
}

// dockMount builds the viewer of a tab of the shown session. A tab nobody asked for
// just now (one restored from storage, or one coming back after a park) is
// `restored`: its first read of a file that is gone closes it instead of parking an
// error on a path that no longer exists. `o` says what this very open asked for.
function dockMount(sid, tab, o) {
  const asked = o.user === tab.key;
  const v = new ViewerTab(tab.path, {
    box: tab.box || undefined,
    scroll: asked && o.view === "changes" ? 0 : tab.scroll, // a diff has its own place
    restored: !asked,
    line: asked ? o.line : undefined,
    col: asked ? o.col : undefined,
    alt: asked ? o.alt : undefined,
    view: asked ? o.view : undefined,
    host: drawerToolbarHost,
  });
  v.onUpdate = () => drawerPaint(true);
  dockViewers.set(tab.key, v);
  dockViewersSid = sid;
  return v;
}

// dockAttached: the viewer on screen in the column, or null.
function dockAttached() {
  const host = drawerEl("drawer-vhost");
  const on = host ? host.children[0] : null;
  return dockViewerList().find((v) => v.el === on) || null;
}

// dockShow brings the column in step with the state of the shown session: the
// viewer of its active tab (mounted now if it has none) is the one on screen, and the
// one that was is hidden, kept alive. It runs after every change of state and when
// the dock arrives on a session, and costs nothing when nothing changed.
function dockShow(o) {
  const host = drawerEl("drawer-vhost");
  if (!host || drawerState.open !== "files") { drawerPaint(); return; }
  const sid = drawerState.sessionId;
  if (dockViewersSid !== null && dockViewersSid !== sid) dockParkAll(); // callers have passed the dirty gate
  const st = dockStateOf(sid);
  const tab = dockTabsOf(st).find((t) => t.key === st.active) || null;
  let v = tab ? dockViewers.get(tab.key) || null : null;
  const cur = dockAttached();
  if (cur && cur !== v) {
    dockSyncScroll(sid); // the viewer being left keeps the place it was read at
    cur.onHide();
  }
  if (tab && !v) v = dockMount(sid, tab, o || {});
  if (!v) host.replaceChildren();
  else if (host.children[0] !== v.el) {
    host.replaceChildren(v.el);
    v.onShow();
    v.onAttach();
    if (v.cm) v.cm.refresh(); // CodeMirror measured nothing while detached
  }
  drawerPaint();
}

// dockParkAll lets go of the viewers of the shown session, remembering each one's
// scroll for when it comes back. `keepDirty` (the crossing into mobile) keeps a
// viewer that holds an edit, detached and whole, instead.
function dockParkAll(keepDirty) {
  dockSyncScroll(dockViewersSid);
  for (const [key, v] of [...dockViewers]) {
    if (keepDirty && dockDirtyKey(key)) {
      if (v.auto) v.toggleAuto(); // a poll must not re-read under an edit nobody can see
      v.el.remove();
      continue;
    }
    dockDropViewer(key);
  }
}

// dockSyncScroll copies where each live viewer was read into its tab's state (a
// scroll alone writes nothing by itself: the next save, or the page going away,
// takes it).
function dockSyncScroll(sid) {
  if (sid !== dockViewersSid) return;
  const r = dockSyncScrolls(dockStateOf(sid), (key) => {
    const v = dockViewers.get(key);
    return v ? savedScroll(v) : undefined;
  });
  if (!r.changed) return;
  dockStates.set(sid, r.st);
  dockUnsaved = true;
}

// A tab was pressed, double-clicked or closed (dockstrip.js calls these).

function dockActivateTab(key) {
  const sid = drawerState.sessionId;
  const st = dockStateOf(sid);
  if (st.active !== key) {
    dockSyncScroll(sid);
    dockCommit(sid, dockActivate(st, key));
    dockShow();
  }
  const v = dockViewers.get(key);
  if (v) v.focusReader(); // a click on a tab is the user going to read that file
}

// dockPinTab keeps a preview tab: a double-click on it, or its first edit.
function dockPinTab(key) {
  const sid = drawerState.sessionId;
  const st = dockStateOf(sid);
  if (!dockTabsOf(st).some((t) => t.key === key && t.preview)) return;
  dockCommit(sid, dockPin(st, key));
  dockRender();
}

// dockPinViewer: a viewer began an edit (the toolbar host's onEditStart), so its tab
// is kept. Before any later click can reach dockOpen, which replaces a preview tab
// without asking whether it holds anything.
function dockPinViewer(v) {
  const key = dockKeyOf(v);
  if (key) dockPinTab(key);
}

// dockCloseTab closes a tab. One holding an edit asks first (Discard / Cancel); the
// session or the tab may have gone while the ask was open.
async function dockCloseTab(key) {
  const sid = drawerState.sessionId;
  const v = sid === dockViewersSid ? dockViewers.get(key) : null;
  if (v && dockDirtyKey(key)) {
    if (drawerAsking) return;
    drawerAsking = true;
    let ok = false;
    try { ok = await askConfirm(discardAsk(v.name)); } finally { drawerAsking = false; }
    if (!ok || drawerState.sessionId !== sid || dockViewers.get(key) !== v) return;
  }
  dockCommit(sid, dockClose(dockStateOf(sid), key));
  dockShow();
}

// dockCloseViewer closes the tab of a live viewer without asking: the viewer itself
// found its file gone on a restored first read (closeTabEverywhere).
function dockCloseViewer(v) {
  const key = dockKeyOf(v);
  if (key === null) return;
  dockCommit(dockViewersSid, dockClose(dockStateOf(dockViewersSid), key));
  dockShow();
}

// dockForget drops every tab on `path` or under it, in every session: the file was
// deleted or renamed in the app (closeViewersFor), whatever its viewer holds. A box
// file at the same path is another file and stays.
function dockForget(path) {
  if (!path) return;
  const prefix = path.replace(/\/+$/, "") + "/";
  for (const [sid, st] of [...dockStates]) {
    let next = st;
    for (const t of st.tabs) if (!t.box && (t.path === path || t.path.indexOf(prefix) === 0)) next = dockClose(next, t.key);
    if (next !== st) dockCommit(sid, next);
  }
  dockShow();
}

// dockRepointViewer: a dock viewer was re-pointed at another file (#53, repointViewer
// in tabs.js). Its tab keeps its slot under the new path and the viewer's own name,
// id and path follow. false, like repointViewer, when another tab already shows that
// path: that one is shown at the same line and this one closes.
function dockRepointViewer(tab, path, line) {
  const key = dockKeyOf(tab);
  const to = dockTabKey(path, tab.box || null);
  if (key === null || !to) return true;
  const sid = dockViewersSid;
  const st = dockStateOf(sid);
  if (to !== key && dockTabsOf(st).some((t) => t.key === to)) {
    dockCloseViewer(tab);
    drawerShowFile(path, { preview: false, line: line ? line.line : undefined, col: line ? line.col : undefined, box: tab.box || undefined });
    return false;
  }
  dockViewers.delete(key);
  tab.path = path;
  tab.id = "view:" + path;
  tab.name = path.split("/").pop() || path;
  dockViewers.set(to, tab);
  dockSetState(sid, dockRepointTab(st, key, path));
  tab.changed(); // the toolbar's path label, and the strip through onUpdate
  return true;
}

// --- the strip and the crumbs --------------------------------------------------

const dockStripOn = { activate: dockActivateTab, pin: dockPinTab, close: dockCloseTab };

// dockCrumbsReveal: a crumb was pressed: show that folder in the tree. The tree's
// own root only opens the column and shows its top; a folder below it is revealed
// (ftReveal opens down to it); anywhere else, the "/" of a file outside the root
// included, the dock does not re-root the user's tree, and says so.
function dockCrumbsReveal(path) {
  const root = typeof ftRoot === "function" ? ftRoot() : "";
  const where = crumbReveal(path, root);
  if (where === "outside") { showToast("That folder is outside the tree's root"); return; }
  dockTreeReveal(); // a folded explorer cannot show the row: open its column
  if (where === "root") {
    if (typeof ftTreeEl !== "undefined" && ftTreeEl) ftTreeEl.scrollTop = 0;
    return;
  }
  ftReveal(path).then((ok) => { if (!ok) showToast("Could not show that folder in the tree", { error: true }); });
}

// dockRender repaints the strip and the crumbs from the state. Cheap and idempotent
// (the strip reuses its elements): it runs on every change of tabs and on every
// viewer update, git status, root change and Activity answer.
function dockRender() {
  if (typeof dockStripPaint !== "function") return;
  const sid = drawerState.sessionId;
  const on = drawerState.open === "files" && drawerEl("dock-bar");
  const st = on ? dockStateOf(sid) : { tabs: [], active: null };
  const mine = sid === dockViewersSid;
  dockStripPaint(dockTabItems(st, {
    dirty: (key) => mine && dockDirtyKey(key),
    code: (path) => (typeof gitStatusCodeFor === "function" ? gitStatusCodeFor(path) : ""),
  }), dockStripOn);
  const tab = dockTabsOf(st).find((t) => t.key === st.active) || null;
  if (!tab) { dockCrumbsPaint([], "", null); return; }
  // A box file is in no tree: its crumbs are text, and its path is read from "/".
  const crumbs = crumbsFor(tab.path, tab.box ? null : typeof ftRoot === "function" ? ftRoot() : null);
  const name = typeof drawerSessionName === "function" ? drawerSessionName(sid) : "";
  dockCrumbsPaint(crumbs, editedLabel(drawerChangedFiles.get(sid), tab.path, name, Date.now()), tab.box ? null : { reveal: dockCrumbsReveal });
}

// --- what is stored -------------------------------------------------------------

function dockStorage() {
  return typeof drawerStorage === "function" ? drawerStorage() : null;
}

function dockSaveSoon() {
  dockUnsaved = true;
  if (!dockLoaded || dockSaveTimer) return;
  dockSaveTimer = setTimeout(dockSaveNow, DOCK_SAVE_MS);
}

// dockSaveNow writes the tabs of every session, and whether the dock is open, under
// DOCK_TABS_KEY. Never before dockRestore has read what was there (see dockLoaded).
function dockSaveNow() {
  if (dockSaveTimer) { clearTimeout(dockSaveTimer); dockSaveTimer = null; }
  if (!dockLoaded) return;
  dockSyncScroll(dockViewersSid);
  const store = dockStorage();
  if (!store) return;
  try { store.setItem(DOCK_TABS_KEY, serializeDock(dockStates, drawerState.open === "files")); } catch (_) { /* quota or private mode: the tabs just do not survive a reload */ }
  dockUnsaved = false;
}

// dockRestore reads the stored tabs, once the session list is known (restoreBoot):
// a session that is not in it has nowhere to show its tabs and is dropped (parseDock),
// so reading before the list is in would drop them all and the next write would
// store that. A page that gave up on the list never calls this and never writes. A
// session the page already has tabs for keeps them. The dock itself is not reopened:
// a reload gives the user their panes, and the tabs are back when Files is.
function dockRestore(sessions) {
  const store = dockStorage();
  let raw = null;
  try { raw = store ? store.getItem(DOCK_TABS_KEY) : null; } catch (_) { /* no storage: nothing to restore */ }
  const live = (sessions || []).map((s) => s && s.id).filter((id) => typeof id === "string" && id);
  for (const [sid, st] of parseDock(raw, live).map) if (!dockStates.has(sid)) dockStates.set(sid, st);
  dockLoaded = true;
  if (drawerState.open === "files") dockShow(); // the dock was opened before the list arrived
}

// The last change before a reload is the one most likely still inside the debounce,
// and a scroll schedules no write of its own: pagehide takes both. Only a page with
// something of its own to say writes, so an idle page cannot overwrite what another
// page of the same browser stored.
if (typeof window !== "undefined" && window.addEventListener) {
  window.addEventListener("pagehide", () => {
    if (!dockLoaded) return;
    dockSyncScroll(dockViewersSid);
    if (dockUnsaved) dockSaveNow();
  });
}

// --- mounting with the dock -----------------------------------------------------

// drawerFilesApply: the drawer state changed from `prev` to `next` (app.js's
// applyDrawer and drawerFollowFocus, after the dirty gate). Leaving the
// session's Files view parks its viewers; arriving shows the new session's tabs.
function drawerFilesApply(prev, next) {
  const was = !!prev && prev.open === "files";
  const now = !!next && next.open === "files";
  const moved = !prev || !next || prev.sessionId !== next.sessionId;
  if (was && (!now || moved)) {
    dockParkAll(); drawerGoReset();
    if (prev.sessionId) drawerChangedFiles.delete(prev.sessionId);
  }
  if (now && (!was || moved)) { drawerPaintChanged(); drawerLoadChanged(next.sessionId); }
  else if (!now) drawerPaintChanged();
  if (was !== now) dockSaveSoon(); // the stored form says whether the dock is open
  if (now) dockShow();
  else drawerPaint();
}

// drawerFilesDetach: the viewport crossed into mobile (restoreDrawerNodes). Clean
// viewers are parked like a close; one holding an edit is kept whole, detached, so
// nothing typed is lost (beforeunload still guards it) and the next desktop Files
// drawer for its session puts it back as it was.
function drawerFilesDetach() {
  drawerPaintChanged(); // the mobile tree carries no dots (drawerOwnsFileClicks is false by now)
  dockParkAll(true);
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
  dockRender(); // "Edited by" in the crumbs is read from the same answer
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

// drawerGoChoose opens hit `i` through drawerShowFile, as a tab (a preview one: a
// pick is a look, a double-click on the tab keeps it).
function drawerGoChoose(i) {
  const r = drawerGoRows[i];
  if (!r) return;
  if (!drawerShowFile(r.path)) return;
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

// What the dock adds to the shared toolbar: the path as the tree shows it (relative
// to the root: the column is narrow), and the two things a tab needs to hear from
// its viewer. No extra buttons: the old "Open in pane" went with the pane viewers
// (a file lives in the dock now).
const drawerToolbarHost = {
  pathText: (v) => relativeTo(typeof ftRoot === "function" ? ftRoot() : "", v.path),
  // The first edit keeps the tab: a preview tab is the one the next single click replaces, and it must never hold
  // an unsaved buffer.
  onEditStart: (v) => dockPinViewer(v),
  // The buffer turned dirty or clean: the tab's dot.
  onDirtyChange: () => dockRender(),
};

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
  const active = dockStateOf(drawerState.sessionId).active;
  const v = drawerState.open === "files" && dockViewersSid === drawerState.sessionId && active ? dockViewers.get(active) || null : null;
  if (v && fromViewer !== true) v.toolbarApi.paint();
  const host = drawerEl("drawer-vhost");
  const empty = drawerEl("drawer-vempty");
  if (host) host.hidden = !v;
  if (empty) empty.hidden = !!v;
  dockRender();
}

initDrawerGoto();
