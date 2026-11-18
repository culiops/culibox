"use strict";

// Follow agent in the Files dock's editor (spec "Editor (P2: Monaco)" → "Follow agent", mockup B1's Follow button). Off by default and
// remembered per session. While it is on for the session the dock shows, each edit the agent finishes opens that file as the dock's
// preview tab (the next one replaces it, so Follow never piles up tabs) and reveals the first line that differs from git, or line 1
// when none does (the spec's range: Activity names a file, not a line).
//
// It listens to the dock's watch (dockwatch.js dockWatchSubscribe) and polls nothing of its own; the watch runs while the session
// works, and what the answer means (which file, if any) is editorref.js followPlan. Only files under the dock's root are shown (the
// root the tree has, dwUnderRoot), and the path opened is the one Activity's `files` lists, never an event's masked `detail`.
//
// It never takes the keyboard: the file is opened with drawerShowFile and revealed with revealLine, neither of which focuses
// anything, so a terminal that has the keyboard keeps it. And it never moves things under the user's hands: while the editor has the
// keyboard (the user is typing in it) Follow does nothing at all, because swapping the active tab moves the editor's element and drops
// the caret; the mark stays where it was, so the next answer after the keyboard has left still shows that edit. A buffer that holds an
// unsaved edit is shown but not revealed.
//
// The reveal waits until the tab can answer where the first change is: it is read, the disk was asked once after the agent's write
// (a clean buffer reloads from it, so the lines are the agent's), and git's base text for the file is in (or git cannot give one:
// line 1). That is a few frames of looking at fields, not a request, and it gives up after FOLLOW_SETTLE_MAX_MS.
//
// Persisted as {sid: true} under agent-box:dockFollow. The toggle (`.dk-follow`, an eye) is in each tab's actions in the dock's bar
// (a text file's, an image's, a binary's) and, with no tab open, in the dock's empty state: Follow's main scenario starts from an
// empty dock. The state it shows is the session's, so every toggle says the same.
//
// editorFollowOn(sid), editorFollowSet(sid, on), editorFollowSync() (drawerPaint: subscribe to the session the dock shows, and keep the
// empty dock's toggle), editorFollowButton(tab) and editorFollowPaintTab(tab) (EditorTab).

const FOLLOW_KEY = "agent-box:dockFollow";
const FOLLOW_SETTLE_MS = 100;       // how often a tab being followed is looked at until it can be revealed
const FOLLOW_SETTLE_MAX_MS = 15000; // how long: Monaco's first load, a slow disk; past it the file is left where it is
const FOLLOW_GIT_WAIT_MS = 2000;    // how long a tab waits for git's base text before it reveals line 1

let efMap = null;     // sid -> true, read from storage once
let efSub = null;     // {sid, off, seen}: the one subscription, for the session the dock shows while Follow is on for it
let efSettle = null;  // {tab, waited, timer, synced, checking}: the reveal that is waiting for its tab
let efEmpty = null;   // the toggle in the empty dock's hint (#drawer-vempty), once made

function efStorage() {
  try { return window.localStorage; } catch (_) { return null; }
}

function efRead() {
  if (efMap) return efMap;
  let raw = null;
  try { const st = efStorage(); raw = st ? st.getItem(FOLLOW_KEY) : null; } catch (_) { /* no storage: Follow is off */ }
  efMap = followParse(raw);
  return efMap;
}

// editorFollowOn: is Follow on for this session?
function editorFollowOn(sid) {
  return typeof sid === "string" && efRead()[sid] === true;
}

// editorFollowSet turns Follow on or off for a session, stores it, and moves the subscription and every toggle with it.
function editorFollowSet(sid, on) {
  if (typeof sid !== "string" || sid === "") return;
  const live = (typeof lastSessions !== "undefined" && Array.isArray(lastSessions) ? lastSessions : []).map((s) => s && s.id).filter(Boolean);
  efMap = followWith(efRead(), sid, !!on, live);
  try { const st = efStorage(); if (st) st.setItem(FOLLOW_KEY, JSON.stringify(efMap)); } catch (_) { /* quota or private mode: it lasts until the page goes */ }
  editorFollowSync();
  if (typeof dockViewerList === "function") for (const v of dockViewerList()) editorFollowPaintTab(v);
  efPaintEmpty();
}

// editorFollowSync keeps the subscription for the session the dock shows (Files open on the desktop) when Follow is on for it, and
// none otherwise. Called from drawerPaint, which runs on every change of session, every session poll and a close; a no-op when
// nothing moved.
function editorFollowSync() {
  efMountEmpty();
  efPaintEmpty();
  const shown = typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks() ? drawerState.sessionId : null;
  const want = shown && editorFollowOn(shown) ? shown : "";
  if ((efSub ? efSub.sid : "") === want) return;
  if (efSub) { efSub.off(); efSub = null; efStopSettle(); }
  if (!want) return;
  const sub = { sid: want, off: null, seen: null };
  sub.off = dockWatchSubscribe(want, (files, events) => editorFollowHear(sub, files, events));
  efSub = sub;
}

// editorFollowHear is one answer of the watch for the session: show the file followPlan names, if any.
function editorFollowHear(sub, files, events) {
  if (efSub !== sub) return;
  const plan = followPlan(files, events, (p) => dwUnderRoot(p), sub.seen);
  // The user is typing in the editor: showing another file would swap the editor's element under the caret. Nothing is done, and the
  // mark is kept (a mark that was not set yet becomes 0: "everything up to now is news"), so the next answer after the keyboard has
  // left shows the newest edit, this one included.
  if (plan.path && efKeyboardInEditor()) {
    if (sub.seen === null) sub.seen = 0;
    return;
  }
  sub.seen = plan.seen;
  if (plan.path) editorFollowShow(sub.sid, plan.path);
}

// efKeyboardInEditor: the editor has the keyboard, in Code or in the Changes view's new side.
function efKeyboardInEditor() {
  if (typeof dockEditor !== "undefined" && dockEditor && dockEditor.hasTextFocus()) return true;
  const tab = typeof dockAttached === "function" ? dockAttached() : null;
  return !!(tab && tab.diff && tab.diff.diff.getModifiedEditor().hasTextFocus());
}

// editorFollowShow opens `path` as the dock's preview tab, without the keyboard, and reveals it when it is ready.
function editorFollowShow(sid, path) {
  if (!drawerOwnsFileClicks() || drawerState.sessionId !== sid) return;
  if (!drawerShowFile(path, { preview: true })) return;
  const tab = dockAttached();
  if (!tab || tab.path !== (typeof normalizePath === "function" ? normalizePath(path) : path)) return;
  efStopSettle();
  efSettle = { tab, waited: 0, timer: null, synced: false, checking: false };
  efStep(efSettle);
}

function efStopSettle() {
  if (efSettle && efSettle.timer) clearTimeout(efSettle.timer);
  efSettle = null;
}

// efStep looks at the tab once: gone or not on screen any more (the user went elsewhere), an image or a binary file (no lines), or
// still getting ready, in which case it looks again soon. A tab with a model is first made to compare itself with the disk (the
// poll of editorsync.js would, but up to 2 s later, and that long the lines would be the old ones), then waits for git's base text.
function efStep(s) {
  if (efSettle !== s) return;
  s.timer = null;
  const tab = s.tab;
  if (tab.disposed || !tab.attached || tab.inner) { efStopSettle(); return; }
  let ready = false;
  if (tab.model) {
    if (!s.synced) {
      if (!editorSyncOf(tab).flight) { // one already out began before this edit may have landed: wait for it, then ask
        s.synced = true;
        s.checking = true;
        Promise.resolve(editorSyncCheck(tab)).catch(() => {}).then(() => { s.checking = false; efStep(s); });
        return;
      }
    } else if (!s.checking) {
      ready = !!tab.box || !!tab.changes || s.waited >= FOLLOW_GIT_WAIT_MS;
    }
  }
  if (ready) { efStopSettle(); editorFollowReveal(tab); return; }
  if (s.waited >= FOLLOW_SETTLE_MAX_MS) { efStopSettle(); return; }
  s.waited += FOLLOW_SETTLE_MS;
  s.timer = setTimeout(() => efStep(s), FOLLOW_SETTLE_MS);
}

// editorFollowReveal shows the first line that differs from git (the gutter's own lineDiff of the buffer as it is now), or line 1.
// Not in a buffer that holds an unsaved edit, and not in an editor that has the keyboard: the caret there is the user's.
function editorFollowReveal(tab) {
  if (tab.disposed || !tab.model || tab.dirty()) return;
  const owner = typeof dockEditorOwner !== "undefined" && dockEditorOwner === tab && !tab.previewing ? dockEditor : null; // Preview has the body, not the editor
  const ed = tab.diff ? tab.diff.diff.getModifiedEditor() : owner;
  if (ed && ed.hasTextFocus()) return;
  editorDiffRecompute(tab); // a reload's own recompute is 300 ms away
  const ch = tab.changes && tab.changes.head !== null && !tab.changes.capped ? tab.changes.lineChanges : [];
  const n = tab.model.getLineCount();
  const total = n > 1 && tab.model.getLineLength(n) === 0 ? n - 1 : n; // the empty line after a final newline is not one
  // A removal at the very end sits one past the last line: show the last (revealLine would toast that it is out of range).
  tab.revealLine(Math.max(1, Math.min(ch.length ? ch[0].from : 1, total)));
}

// --- the toggle ---------------------------------------------------------------------------------------------------------------

// efButton builds a Follow toggle: an eye and the word. A press turns Follow on or off for the session `sidOf()` names; efPaint shows
// the session's setting on it.
function efButton(sidOf) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "btn btn-sm dk-follow";
  b.append(svgIcon([["path", { d: "M1 7s2.2-4 6-4 6 4 6 4-2.2 4-6 4-6-4-6-4z" }], ["circle", { cx: "7", cy: "7", r: "1.7" }]], { icon: "eye", size: "14" }));
  const t = document.createElement("span");
  t.textContent = "Follow";
  b.append(t);
  b.onclick = () => {
    const sid = sidOf();
    if (sid) editorFollowSet(sid, !editorFollowOn(sid));
  };
  return b;
}

function efPaint(b, sid) {
  const on = editorFollowOn(sid);
  const pressed = on ? "true" : "false";
  if (b.getAttribute("aria-pressed") !== pressed) b.setAttribute("aria-pressed", pressed);
  const title = on ? "Following the agent: each file it edits opens here. Click to stop"
    : "Follow the agent: open each file it edits, as the preview tab, and show what changed";
  if (b.title !== title) b.title = title;
}

const efTabSid = (tab) => (tab.host && typeof tab.host.sessionId === "function" ? tab.host.sessionId() : null);
const efDockSid = () => (typeof drawerState !== "undefined" ? drawerState.sessionId : null);

// editorFollowButton is a tab's toggle, in its actions in the dock's bar (EditorTab.buildActs).
function editorFollowButton(tab) {
  const b = efButton(() => efTabSid(tab));
  tab.followBtn = b;
  editorFollowPaintTab(tab);
  return b;
}

// editorFollowPaintTab brings a tab's toggle in step with the session's setting.
function editorFollowPaintTab(tab) {
  const b = tab && tab.followBtn;
  if (b) efPaint(b, efTabSid(tab));
}

// The empty dock's toggle: with no tab open the dock's bar is not drawn, and Follow has to be reachable from there (its main scenario
// is turning it on, then waiting for the agent). One button in the hint under the empty viewer (#drawer-vempty, shown while the dock
// has no tab), for the session the dock shows; hidden while it shows none.
function efMountEmpty() {
  if (efEmpty) return;
  const host = document.getElementById("drawer-vempty");
  if (!host) return;
  efEmpty = efButton(efDockSid);
  efEmpty.id = "dock-follow-empty";
  host.append(efEmpty);
}

function efPaintEmpty() {
  if (!efEmpty) return;
  const sid = efDockSid();
  efEmpty.hidden = !sid;
  efPaint(efEmpty, sid);
}
