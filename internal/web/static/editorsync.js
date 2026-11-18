"use strict";

// Disk changes under the Files dock's editor (spec "Editor (P2)" → "Agent writes while you look"). The file an EditorTab shows
// can change under it: the agent writes it, or a save of the user's own is refused with a 409. This file finds out, asks
// editorref.js conflictChoice what it means for the buffer, and does that:
//   reload   a clean buffer: the new text goes in, in place, keeping the cursor and the scroll (EditorTab.replaceFromDisk);
//   bar      a dirty buffer: the amber bar (Compare, Keep mine, Load the agent's version). Nothing is replaced and nothing is
//            written until the user picks, and a Mod+S meanwhile gets the server's 409 again: the bar stays;
//   deleted  the file is gone: the bar says so, with Copy contents and Close.
//
// The poll. While the dock is on screen (editorSyncShown) the ONE tab it shows is asked about every EDITOR_SYNC_MS, and once
// whenever a tab is shown, with the metadata read (GET /api/fs/read?path=…&meta=1: {kind, size, version}, no text). One
// interval for the whole dock and one request at a time: hidden tabs, tabs of another session and image or binary tabs (no
// version) are never asked. Everything the answer touches is decided when the answer arrives, with the buffer as it is then:
//  - no poll starts while a save is in flight, and an answer that began before a save started or ended is dropped (tab.saveMark):
//    our own write must not look like the agent's;
//  - an answer for a tab that was disposed, hidden, switched away from or re-pointed, or that arrives after the dock was hidden,
//    is dropped;
//  - a clean buffer is reloaded from a full read, and if the user typed meanwhile the full read is decided again (a bar, not a
//    reload);
//  - a bar that is up takes the newest disk version a later answer brings: "Keep mine" will overwrite what the user was told
//    about, never an older one, and an open compare view is read again for the same reason.
//
// editorSyncStart(tab) / editorSyncStop(tab): `tab` is on screen / is not (EditorTab.onAttach, onHide).
// editorSyncOnReplace(tab, fn): fn(newText) after a reload or "Load the agent's version" replaced the model's text (P3 hooks here).

const EDITOR_SYNC_MS = 2000; // today's Follow-file cadence (viewer-tab.js toggleAuto)

let esTab = null;   // the tab on screen, the one asked about
let esTimer = null; // the dock's one interval, null while nothing is asked

// editorSyncShown: the dock is on screen: Files open on the desktop, the page visible, and no page view (Settings, Audit) over it.
function editorSyncShown() {
  if (document.hidden) return false;
  if (typeof currentPage === "function" && currentPage()) return false;
  return drawerOwnsFileClicks();
}

// editorSyncOf: what editorsync.js keeps on a tab: the bar ({kind: "conflict"|"deleted", diskVersion}) or null, the compare
// view ({diff, original}) or null, who hears of a replace, whether a poll or a bar action is out.
function editorSyncOf(tab) {
  if (!tab.sync) tab.sync = { bar: null, cmp: null, subs: new Set(), flight: false, acting: false };
  return tab.sync;
}

// --- the poll -------------------------------------------------------------------------------------------------------

function editorSyncStart(tab) {
  esTab = tab;
  editorSyncSync(true);
}

// A tab that leaves the screen lets its compare view go: the dock has one diff editor at a time (Compare and the Changes view of
// editordiff.js never coexist either). The bar stays, so Compare can be pressed again when the tab is back.
function editorSyncStop(tab) {
  if (esTab === tab) esTab = null;
  editorSyncCloseCompare(tab);
  editorSyncSync();
}

// editorSyncSync starts, keeps or stops the interval to match what is on screen. `now` asks once at once (a tab was just shown);
// so does a start, because a dock that comes back on screen (a page view closed, the page shown again) has not looked for a while.
// Called from every place that changes it: the tab's show and hide, the dock's paint (drawerPaint), a page view opening or
// closing (pagehost.js) and the page being hidden or shown (below).
function editorSyncSync(now) {
  if (!esTab || !editorSyncShown()) {
    if (esTimer) { clearInterval(esTimer); esTimer = null; }
    return;
  }
  if (!esTimer) { esTimer = setInterval(editorSyncTick, EDITOR_SYNC_MS); now = true; }
  if (now) editorSyncTick();
}

// editorSyncTick asks once, for the tab that is on screen. It re-checks the conditions first: whatever changed them and did not
// say so ends the poll here, before a request.
function editorSyncTick() {
  if (!esTab || !editorSyncShown()) { editorSyncSync(); return; }
  editorSyncCheck(esTab);
}

// editorSyncMetaURL: the metadata read of the tab's file (a box file has its own route).
function editorSyncMetaURL(tab) {
  return (tab.box ? boxFileURL(tab.box, tab.path) : "/api/fs/read?path=" + encodeURIComponent(tab.path)) + "&meta=1";
}

// editorSyncCtx / editorSyncStill / editorSyncCurrent: what an answer must still find true to be about now. Still: the tab is alive,
// the same file, no save under way and none ended since the question (`tab.saving` and `saveMark` are the save's race, see the top):
// every answer, a user's read included. Current: and the tab is the one on screen: a poll's.
function editorSyncCtx(tab) {
  return { path: tab.path, load: tab.loadSeq, mark: tab.saveMark };
}

function editorSyncStill(tab, c) {
  return !tab.disposed && !!tab.model && !tab.saving && tab.path === c.path && tab.loadSeq === c.load && tab.saveMark === c.mark;
}

function editorSyncCurrent(tab, c) {
  return editorSyncStill(tab, c) && tab.attached && esTab === tab && editorSyncShown();
}

// editorSyncCheck: one metadata read for `tab`, and what comes of it. One at a time, and not while the tab saves.
async function editorSyncCheck(tab) {
  const s = editorSyncOf(tab);
  if (s.flight || tab.disposed || !tab.attached || !tab.model || tab.inner || tab.saving) return;
  const ctx = editorSyncCtx(tab);
  s.flight = true;
  try {
    const res = await fsRead(tab.path, editorSyncMetaURL(tab));
    if (!editorSyncCurrent(tab, ctx)) return;
    const d = res.ok ? res.data : null;
    if (res.status === 404) await editorSyncAct(tab, 404, "");
    else if (d && d.kind === "text" && typeof d.version === "string") await editorSyncAct(tab, 0, d.version);
    // Any other answer (offline, refused, over the read cap, not text any more) says nothing about the file: nothing changes.
  } finally { s.flight = false; }
}

// editorSyncRead: the whole file, as {ok: true, text, version, utf8} or {ok: false, status, error}. `utf8` is false when the file's
// bytes are not valid UTF-8 (the text then holds U+FFFD for them).
async function editorSyncRead(tab) {
  const res = await fsRead(tab.path, tab.box ? boxFileURL(tab.box, tab.path) : undefined);
  const d = res.ok ? res.data : null;
  if (d && d.kind === "text" && typeof d.version === "string") return { ok: true, text: d.content || "", version: d.version, utf8: d.utf8 !== false };
  const why = (res.data && res.data.error) || (!res.ok ? (res.status ? "HTTP " + res.status : "could not reach the server") : "it is not text any more");
  return { ok: false, status: res.status, error: why };
}

// --- deciding -------------------------------------------------------------------------------------------------------

// editorSyncAct does what conflictChoice says about `tab`, given what was just learned of its file: `status` (404 gone, 409 a save
// was refused, else 0), `diskVersion` ("" when unknown) and, once the whole file has been read, its `text` and whether that is `utf8`.
async function editorSyncAct(tab, status, diskVersion, text, utf8) {
  const choice = conflictChoice({ dirty: tab.dirty(), diskVersion, baseVersion: tab.version, saveStatus: status });
  if (choice === "deleted") editorSyncShowBar(tab, { kind: "deleted", diskVersion: "" });
  else if (choice === "bar") editorSyncShowBar(tab, { kind: "conflict", diskVersion });
  else if (choice === "reload") await editorSyncReload(tab, diskVersion, text, utf8);
  else if (diskVersion && diskVersion === tab.version) editorSyncClearBar(tab); // the file is as the buffer began: a bar for the change is moot
}

// editorSyncReload: a clean buffer whose file changed. With no text yet, the whole file is read first and the decision made
// again on what that read says and on the buffer as it is by then (typed in meanwhile: a bar; the file moved back: nothing).
async function editorSyncReload(tab, diskVersion, text, utf8) {
  if (text !== undefined) { editorSyncReplace(tab, text, diskVersion, utf8); return; }
  const ctx = editorSyncCtx(tab);
  const got = await editorSyncRead(tab);
  if (!editorSyncCurrent(tab, ctx)) return;
  if (!got.ok) {
    if (got.status === 404) await editorSyncAct(tab, 404, "");
    return;
  }
  await editorSyncAct(tab, 0, got.version, got.text, got.utf8);
}

// editorSyncReplace puts the disk's text in the buffer (the one place that does) and tells whoever asked to hear of it.
function editorSyncReplace(tab, text, version, utf8) {
  tab.replaceFromDisk(text, version, utf8);
  editorSyncClearBar(tab);
  for (const fn of [...editorSyncOf(tab).subs]) {
    try { fn(text); } catch (_) { /* one subscriber's fault is neither the tab's nor the others' */ }
  }
}

// editorSyncOnReplace: fn(newText) after the tab's text was replaced by a reload or by "Load the agent's version". Returns the
// unsubscribe function. (A discard of the user's own edits is not a replace: nothing on disk moved.)
function editorSyncOnReplace(tab, fn) {
  if (typeof fn !== "function") return () => {};
  const subs = editorSyncOf(tab).subs;
  subs.add(fn);
  return () => { subs.delete(fn); };
}

// --- the bar --------------------------------------------------------------------------------------------------------

function editorSyncButton(label, cls, run) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "btn btn-sm " + cls;
  b.textContent = label;
  b.onclick = run;
  return b;
}

// editorSyncShowBar puts the bar up, or keeps it: the same kind already showing is not rebuilt (a button the user has focus on
// must stay), it only takes the newer disk version, and a compare view that is open is read again to show it.
function editorSyncShowBar(tab, bar) {
  const s = editorSyncOf(tab);
  if (s.bar && s.bar.kind === bar.kind) {
    if (bar.diskVersion && bar.diskVersion !== s.bar.diskVersion) {
      s.bar.diskVersion = bar.diskVersion;
      if (s.cmp) editorSyncRefreshCompare(tab);
    }
    return;
  }
  editorSyncCloseCompare(tab); // a compare of the other kind of bar is not about this one
  s.bar = bar;
  editorSyncPaintBar(tab);
}

// editorSyncClearBar takes the bar and its compare view away.
function editorSyncClearBar(tab) {
  const s = tab.sync;
  if (!s || !s.bar) return;
  editorSyncCloseCompare(tab);
  s.bar = null;
  editorSyncPaintBar(tab);
}

function editorSyncPaintBar(tab) {
  const s = editorSyncOf(tab);
  const el = tab.barEl;
  if (!s.bar) { el.replaceChildren(); el.hidden = true; return; }
  const node = (tag, cls, text) => {
    const n = document.createElement(tag);
    n.className = cls;
    if (text) n.textContent = text;
    return n;
  };
  const msg = node("div", "dk-conflict-msg");
  const acts = node("div", "dk-conflict-acts");
  if (s.bar.kind === "deleted") {
    msg.textContent = DELETED_TEXT;
    acts.append(
      editorSyncButton("Copy contents", "btn-ghost", () => tab.copyContents()),
      editorSyncButton("Close", "btn-ghost", () => { if (tab.host && tab.host.closeTab) tab.host.closeTab(tab); }),
    );
  } else {
    const session = tab.host && tab.host.sessionName ? tab.host.sessionName() : "";
    const [lead, rest] = conflictText(session, tab.name);
    msg.append(node("b", "", lead), rest);
    acts.append(
      editorSyncButton("Compare", "btn-ghost", () => editorSyncCompare(tab)),
      editorSyncButton("Keep mine", "btn-ghost", () => editorSyncKeepMine(tab)),
      editorSyncButton("Load the agent's version", "btn-yes", () => editorSyncLoad(tab)),
    );
  }
  el.replaceChildren(node("span", "dk-conflict-dot"), msg, acts);
  el.hidden = false;
}

// Keep mine: the next save is guarded by the disk's version instead of the one the buffer began at, so it overwrites the file
// (EditorTab.keepOverDisk, which also keeps the buffer dirty until then). It does not save by itself, and the guard stays: if
// the agent writes again before Mod+S, that save is refused and the bar comes back. The version is the one the bar (and a
// compare view, which is kept current) shows.
function editorSyncKeepMine(tab) {
  const s = editorSyncOf(tab);
  if (!s.bar || s.bar.kind !== "conflict" || tab.saving) return;
  if (!s.bar.diskVersion) {
    showToast("The file on disk is too large to read here, so its version is unknown: copy your edits and reload it", { error: true });
    return;
  }
  tab.keepOverDisk(s.bar.diskVersion);
  editorSyncClearBar(tab);
  tab.focusReader();
}

// Load the agent's version: the buffer becomes what is on disk now, and is clean. A new value, not an edit: the undo history is cleared, so
// no undo can walk back to text the disk's version no longer stands for (replaceFromDisk).
async function editorSyncLoad(tab) {
  const s = editorSyncOf(tab);
  const bar = s.bar;
  if (!bar || bar.kind !== "conflict" || s.acting || tab.saving) return;
  const ctx = editorSyncCtx(tab);
  s.acting = true;
  const got = await editorSyncRead(tab);
  s.acting = false;
  // The answer is for the bar that asked. If the user chose something else meanwhile (Keep mine, and a save) or the file was
  // saved, replaced or re-pointed, it is dropped: it would put an older text over what is there now.
  if (!editorSyncStill(tab, ctx) || s.bar !== bar) return;
  if (!got.ok) { editorSyncReadFailed(tab, got); return; }
  editorSyncReplace(tab, got.text, got.version, got.utf8);
  tab.focusReader();
}

// editorSyncReadFailed: a read the user asked for (Compare, Load) failed. A 404 is the file gone; the rest is said in a toast.
function editorSyncReadFailed(tab, got) {
  if (got.status === 404) { editorSyncAct(tab, 404, ""); return; }
  showToast("Could not read " + tab.name + ": " + got.error, { error: true });
}

// --- compare --------------------------------------------------------------------------------------------------------

// Compare: a read-only Monaco diff in the tab, in place of the editor, until it is dismissed or the tab is hidden: the disk on the
// left (removed lines), the buffer (live, not a copy) on the right (added lines); one inline view when the dock is too narrow for two sides. It is the dock's one diff editor while it is open: a Changes view the tab had is
// left first (Code is what "Back to editing" returns to), and opening Changes closes it (editordiff.js).
async function editorSyncCompare(tab) {
  const s = editorSyncOf(tab);
  const bar = s.bar;
  if (!bar || bar.kind !== "conflict" || s.cmp || s.acting) return;
  const ctx = editorSyncCtx(tab);
  s.acting = true;
  const got = await editorSyncRead(tab);
  s.acting = false;
  // As Load: an answer for a bar that is gone is dropped. So is one for a tab that left the screen meanwhile: no diff editor for it.
  if (!editorSyncStill(tab, ctx) || s.bar !== bar || s.cmp || !tab.attached) return;
  if (!got.ok) { editorSyncReadFailed(tab, got); return; }
  bar.diskVersion = got.version; // what the diff shows is what Keep mine will overwrite
  editorDiffSet(tab, false); // the Changes view, if the tab is in it, goes: one diff editor
  const original = dockMonaco.editor.createModel(got.text, monacoLang(tab.path));
  const body = document.createElement("div");
  body.className = "dk-cmp-body";
  const diff = dockMonaco.editor.createDiffEditor(body, Object.assign({
    readOnly: true,
    originalEditable: false,
    renderSideBySide: true,
    // Left and right when there is room for two columns, one inline view when there is not: Monaco's own rule (its default, stated
    // here because the dock is narrow: about 400 px at 1280, where each side of a split would be 170 px, 19 characters; ruling R15).
    useInlineViewWhenSpaceIsLimited: true,
    renderSideBySideInlineBreakpoint: 900,
    automaticLayout: true,
    minimap: { enabled: false },
    stickyScroll: { enabled: false }, // as Code and Changes have it
    scrollBeyondLastLine: false,
    fixedOverflowWidgets: true,
  }, editorOptions()));
  const computed = diff.onDidUpdateDiff(() => { computed.dispose(); diff.revealFirstDiff(); }); // open at the first difference, not at line 1
  diff.setModel({ original, modified: tab.model });
  const head = document.createElement("div");
  head.className = "dk-cmp-head";
  const label = document.createElement("span");
  label.textContent = "On disk and your buffer";
  head.append(label, editorSyncButton("Back to editing", "btn-ghost", () => { editorSyncCloseCompare(tab); tab.focusReader(); }));
  tab.cmpEl.replaceChildren(head, body);
  tab.cmpEl.hidden = false;
  tab.bodyEl.hidden = true;
  s.cmp = { diff, original };
}

// editorSyncRefreshCompare: the disk moved again while the compare view is open: read it again, so the diff never shows an older
// file than the version Keep mine will take. A read that fails closes the view rather than leave a stale one up.
async function editorSyncRefreshCompare(tab) {
  const s = editorSyncOf(tab);
  const cmp = s.cmp;
  const got = await editorSyncRead(tab);
  if (tab.disposed || s.cmp !== cmp) return;
  if (!got.ok) { editorSyncCloseCompare(tab); return; }
  cmp.original.setValue(got.text);
  if (s.bar) s.bar.diskVersion = got.version;
}

// editorSyncCloseCompare dismisses the compare view: the diff editor and the model of the disk's text are disposed (the buffer's
// model is the tab's, and stays), and the editor is shown again.
function editorSyncCloseCompare(tab) {
  const s = tab.sync;
  const cmp = s && s.cmp;
  if (!cmp) return;
  s.cmp = null;
  cmp.diff.setModel(null);
  cmp.diff.dispose();
  cmp.original.dispose();
  tab.cmpEl.replaceChildren();
  tab.cmpEl.hidden = true;
  tab.bodyEl.hidden = false;
  if (dockEditorOwner === tab && dockEditor) dockEditor.layout(); // it was laid out at no size while hidden
}

// editorSyncDispose: the tab is closed. It stops being asked about, and nothing it made stays: the compare view's editor and model.
function editorSyncDispose(tab) {
  editorSyncStop(tab);
  const s = tab.sync;
  if (!s) return;
  editorSyncCloseCompare(tab);
  s.bar = null;
  s.subs.clear();
}

// A hidden page asks nothing; coming back asks again at once (if the dock is still on screen).
if (typeof document !== "undefined" && document.addEventListener) document.addEventListener("visibilitychange", () => editorSyncSync());
