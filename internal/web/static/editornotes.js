"use strict";

// Line notes in the Files dock's Changes view (spec "Review loop (P3)", mockup B2): the reviewer's comments on lines of the file, kept on
// the page and pasted to the agent as one message (the paste bar is a later task's). notesmodel.js is the pure half: what a note is,
// how it moves with the file (reanchor, reanchorOld), what is stored. This file is the DOM: the store the page holds, and the cards (Monaco overlay
// widgets, each over a view zone that only holds its room) that show and edit the notes in a diff editor.
//
// The store. One Map, session id -> the session's notes (a list that is replaced, never edited in place), read once by notesLoad from
// the first successful session list (app.js, next to reviewMarksLoad: loadNotes drops every session it is not told is alive, so it
// must never see an empty or a failed list). A note is made only after that read: before it, a save would replace what is stored.
// Every change goes through ntSet: the store, saveNotes 300 ms after the last change (and at once when the page is hidden or closed),
// the Changes section's notes chip (dockChangesRender) and every diff editor showing a file of that session. notesFor(sid) and
// notesPending(sid) are for the Changes list and the paste bar; notesUpdate(sid, fn) is the one way for them to change a note
// (markSent); notesShowSent(sid, on) says whether sent notes are drawn.
//
// A host. A diff editor takes notes through a host object, not through the EditorTab, so an All-changes section can supply one too:
//   {key, sid(), path(), rel(), diff, onReplace(fn) -> unsubscribe, takeWant() -> {line} | null}
//   key        what identifies the host (the tab, a section); one attach per key;
//   sid()      the session the notes belong to, null for a file that is not open in one (no note can be made);
//   path()     the file's absolute path (what the notes are stored under), rel() the name the agent is told;
//   diff       the Monaco diff editor: the file as it is now on the modified side, the base text on the original side;
//   onReplace  fn(newText) is called when the buffer was replaced from disk (editorSyncOnReplace): the notes move with their text;
//   takeWant   optional: a line a note was asked for before the view existed (Add note from Code).
// editorNotesAttachHost(host) / editorNotesDetachHost(key) are the pair; editorNotesAttach(tab) / editorNotesDetach(tab) are the
// EditorTab's, called by editordiff.js when the Changes view is built and when it is let go. Several hosts can be attached at once.
//
// Which line a note is on. A note is about ONE line: for a selection of several lines it is the FIRST line of the selection, and the
// quote is that line's text (the quote is what reanchor looks for, and one line is what the agent is shown). A note made on the
// modified side is side "new" (the line as the file is now); one made on a removed line of the original side (Side by side only) is
// side "old", a line of the base text. In Inline the original editor is not on screen, so an old note is drawn on the modified side,
// after the change that holds its line; it can only be made in Side by side.
//
// Moving. When the buffer is replaced from disk (reload, "Load the agent's version"), and whenever a Changes view opens (the file
// may have changed while it was closed), reanchor follows the new-side notes: the quote at its line stays, else the nearest line with
// the same text takes it, else the note says "outdated" and keeps its line. Old-side notes never move: the base only changes when
// HEAD or the scope does, and then reanchorOld only checks the quote against it (editorNotesBaseMoved).
//
// Where the keys are. `c` and Mod+Shift+A (the registry's editor.addNote, bound by editortab.js on Monaco) add a note on the cursor's
// line (the first line of a selection) of the diff editor that has the keyboard; in Code Mod+Shift+A, and the selection bar's Add note,
// open Changes at that line with the note box up (a note is read where the diff is). Esc cancels the note box and Mod+Enter saves
// (editor.noteSave); neither reaches the page, so Esc cannot leave Full mode from here. Note text is only ever set as textContent.

const NT_SAVE_MS = 300;       // saveNotes follows the last change by this long
const NT_TICK_MS = 60000;     // "just now" and "3 min ago" are refreshed when the pointer moves over a diff, at most this often
const NT_WANT_MS = 10000;     // a note asked for from Code that the view did not take (Changes could not open) is forgotten after this
const NT_MAX_TEXT = 2000;     // one note's text: ten of them still fit the paste bar's 16000 characters
const NT_COMPOSE_H = 132;     // first guesses of a zone's height, in px, until it is measured
const NT_NOTE_H = 68;

let ntStore = null;           // Map: session id -> notes (a list); null until notesLoad read the stored record
const ntShowSent = new Set(); // the sessions whose sent notes are drawn
const ntDirty = new Set();    // the sessions whose notes were changed and are not saved yet
let ntTimer = null;           // the debounced save
const ntHosts = new Map();    // host key -> state (see editorNotesAttachHost)
const ntDrafts = new Map();   // "<sid>\n<path>" -> {side, line, text}: a note box that was open when its view went (kept for the next one)
let ntTickAt = 0;             // when the notes' times were last refreshed
let ntSeq = 0;

// --- the store ------------------------------------------------------------------------------------------------------

function ntStorage() {
  try { return typeof localStorage !== "undefined" ? localStorage : null; } catch (_) { return null; }
}

// notesLoad: the first successful session list is here (app.js), `sids` the ids in it. Read once; a list with nothing in it says
// nothing about which sessions are gone, so it reads nothing and the next list tries again.
function notesLoad(sids) {
  if (ntStore !== null || !Array.isArray(sids) || !sids.length) return;
  const st = ntStorage();
  ntStore = st ? loadNotes(st, sids) : new Map();
  for (const tab of [...ntTabs]) ntRefreshTab(tab); // tabs that were open before the notes were: their lines follow the text first
  ntChanged(null);
}

const NT_NONE = Object.freeze([]);

// ntRaw: the session's notes as the store holds them, with no sync of the open tabs' lines first (the sync itself reads it).
function ntRaw(sid) {
  return (ntStore && sid && ntStore.get(sid)) || NT_NONE;
}

// notesFor: the session's notes, pending and sent (a list: never changed in place); empty while the store is not read. The lines are
// the file's as it is now, edits of the user's own in Code included: the tracked tabs are synced first (ntSyncAll).
function notesFor(sid) {
  ntSyncAll();
  return ntRaw(sid);
}

function notesPending(sid) {
  return notesFor(sid).filter((n) => n.status === "pending").length;
}

// notesUpdate(sid, fn): the notes become fn(list) (notesmodel.js functions return a new list), if it is a different one. false when
// the store is not read yet or fn gave the same list.
function notesUpdate(sid, fn) {
  if (!ntStore || !sid) return false;
  const cur = notesFor(sid);
  const next = fn(cur);
  if (!Array.isArray(next) || next === cur) return false;
  ntSet(sid, next);
  return true;
}

// notesShowSent(sid, on): sent notes are drawn in the diff (they are kept to check the fix against) or hidden.
function notesShowSent(sid, on) {
  if (!sid) return;
  if (on) ntShowSent.add(sid); else ntShowSent.delete(sid);
  ntChanged(sid);
}

function notesShowSentOf(sid) {
  return ntShowSent.has(sid);
}

function ntSet(sid, list) {
  ntSetQuiet(sid, list);
  ntChanged(sid);
}

// ntSetQuiet: the store takes the list and it is saved on the next debounce, with no repaint (the sync inside notesFor must not repaint a
// list that is being drawn; it asks for one after).
function ntSetQuiet(sid, list) {
  if (list.length) ntStore.set(sid, list); else ntStore.delete(sid);
  ntDirty.add(sid);
  ntSchedule();
}

function ntSchedule() {
  if (ntTimer !== null) clearTimeout(ntTimer);
  ntTimer = setTimeout(ntFlush, NT_SAVE_MS);
}

// ntFlush: the lines of the open tabs are read back from their decorations, then what changed is saved.
function ntFlush() {
  if (ntTimer !== null) { clearTimeout(ntTimer); ntTimer = null; }
  ntSyncAll();
  if (ntTimer !== null) { clearTimeout(ntTimer); ntTimer = null; }
  const st = ntStorage();
  if (st && ntStore) for (const sid of ntDirty) saveNotes(st, sid, ntRaw(sid));
  ntDirty.clear();
}

// Nothing is lost to a page that goes before the 300 ms are up.
if (typeof window !== "undefined" && typeof window.addEventListener === "function") window.addEventListener("pagehide", ntFlush);

// ntChanged: the notes of `sid` (every session's, for null) changed: the Changes section's chip, and the diff editors that show them.
function ntChanged(sid) {
  if (typeof dockChangesRender === "function") { try { dockChangesRender(); } catch (_) { /* the list's fault is not the note's */ } }
  for (const tab of [...ntTabs]) ntDecorate(tab);
  for (const hs of [...ntHosts.values()]) if (sid === null || hs.h.sid() === sid) ntPaint(hs);
  if (typeof reviewBarRender === "function") reviewBarRender(sid); // the review bar's count, and its Sent toggle (reviewbar.js)
}

// --- following the file for as long as its tab is open --------------------------------------------------------------

// A note's line has to follow the file whether or not a Changes view is open: notesFor feeds the paste bar. So every EditorTab is tracked
// from the moment its model exists until it is disposed (editortab.js: editorNotesTrack / editorNotesUntrack):
//   - a replace from disk (editorSyncOnReplace: a reload, "Load the agent's version") re-anchors the file's notes by their quote (reanchor);
//   - the user's own edits in Code are followed by a model decoration per new-side note (the spec: "a Monaco decoration tracks the line
//     through edits"), a whole-line range that never grows when typing at its edges. They are read back, debounced with the save and
//     before notesFor answers (ntSyncAll), into the store: the note takes the decoration's line, and the line's text as its quote (the
//     note is about the line as it is). A decoration that collapsed (its line was deleted or replaced whole) falls back to the quote
//     search, which moves the note to the same text nearby or says "outdated" (an outdated note has no decoration).
// A new value of the whole text (setValue: a replace from disk, discarding the user's edits) clears the model's decorations: they are
// put again from the re-anchored lines.
const ntTabs = new Set();
let ntSyncing = false;

function ntTabSid(tab) {
  return tab.host && typeof tab.host.sessionId === "function" ? tab.host.sessionId() : null;
}

function editorNotesTrack(tab) {
  if (!tab || !tab.model || tab.ntTr || tab.disposed) return;
  const tr = { ids: new Map(), sig: "", dirty: false, flushed: false, sub: null, unsub: null };
  tab.ntTr = tr;
  ntTabs.add(tab);
  tr.sub = tab.model.onDidChangeContent((e) => ntModelChanged(tab, e));
  tr.unsub = editorSyncOnReplace(tab, () => ntRefreshTab(tab));
  ntRefreshTab(tab);
}

// editorNotesUntrack: the tab is closed; its decorations and listeners go, before its model does.
function editorNotesUntrack(tab) {
  const tr = tab && tab.ntTr;
  if (!tr) return;
  tab.ntTr = null;
  ntTabs.delete(tab);
  if (tr.sub) tr.sub.dispose();
  if (tr.unsub) tr.unsub();
  try { if (tab.model && tr.ids.size) tab.model.deltaDecorations([...tr.ids.values()], []); } catch (_) { /* the model is gone */ }
}

// ntModelChanged: the text changed. Nothing is done per keystroke but a flag and the debounce of the save, and only for a file that has
// notes on it.
function ntModelChanged(tab, e) {
  const tr = tab.ntTr;
  if (!tr || !tr.ids.size) return;
  if (e && (e.isFlush || e.replaced)) { tr.ids = new Map(); tr.sig = ""; tr.flushed = true; }
  tr.dirty = true;
  ntSchedule();
}

function ntSyncAll() {
  if (ntSyncing) return;
  ntSyncing = true;
  try { for (const tab of [...ntTabs]) ntSyncTab(tab); } finally { ntSyncing = false; }
}

// ntSyncTab reads the tab's decorations back into the store.
function ntSyncTab(tab) {
  const tr = tab.ntTr;
  if (!tr || !tr.dirty) return;
  tr.dirty = false;
  if (tr.flushed) { ntRefreshTab(tab); return; }
  const sid = ntTabSid(tab);
  const m = tab.model;
  if (!sid || !m || ntStore === null) return;
  const cur = ntRaw(sid);
  const gone = new Set();
  let next = cur.map((n) => {
    const id = tr.ids.get(n.id);
    if (!id) return n;
    const r = m.getDecorationRange(id);
    if (!r || (n.quote !== "" && r.startLineNumber === r.endLineNumber && r.startColumn === r.endColumn)) { gone.add(n.id); return n; }
    const text = m.getLineContent(r.startLineNumber);
    return r.startLineNumber === n.line && text === n.quote ? n : Object.assign({}, n, { line: r.startLineNumber, quote: text });
  });
  if (gone.size) {
    const re = reanchor(next, tab.path, m.getValue());
    next = next.map((n, i) => (gone.has(n.id) ? re[i] : n));
  }
  if (next.some((n, i) => n !== cur[i])) {
    ntSetQuiet(sid, next);
    ntLater(() => ntChanged(sid));
  }
  ntDecorate(tab);
}

// ntRefreshTab: the text was replaced (or the notes were just read): the file's new-side notes are re-anchored by their quote and the
// decorations are made again.
function ntRefreshTab(tab) {
  const tr = tab.ntTr;
  const m = tab.model;
  if (!tr || !m) return;
  try { if (tr.ids.size) m.deltaDecorations([...tr.ids.values()], []); } catch (_) { /* cleared with the text */ }
  tr.ids = new Map();
  tr.sig = "";
  tr.dirty = false;
  tr.flushed = false;
  const sid = ntTabSid(tab);
  if (sid && ntStore !== null) {
    const cur = ntRaw(sid);
    if (cur.some((n) => n.path === tab.path)) {
      const next = reanchor(cur, tab.path, m.getValue());
      if (next.some((n, i) => n !== cur[i])) ntSet(sid, next);
    }
  }
  ntDecorate(tab);
}

// ntDecorate: one decoration per current new-side note of the tab's file, at its line; written only when the set or a line differs.
function ntDecorate(tab) {
  const tr = tab.ntTr;
  const m = tab.model;
  if (!tr || !m) return;
  const sid = ntTabSid(tab);
  const count = m.getLineCount();
  const want = sid ? ntRaw(sid).filter((n) => n.path === tab.path && n.side === "new" && !n.outdated && n.line >= 1 && n.line <= count) : [];
  const sig = want.map((n) => n.id + ":" + n.line).join("|");
  if (sig === tr.sig) return;
  const M = ntMonaco();
  const stick = M && M.editor && M.editor.TrackedRangeStickiness ? M.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges : 1;
  const ids = m.deltaDecorations([...tr.ids.values()], want.map((n) => ({
    range: { startLineNumber: n.line, startColumn: 1, endLineNumber: n.line, endColumn: m.getLineMaxColumn(n.line) },
    options: { stickiness: stick },
  })));
  tr.ids = new Map(want.map((n, i) => [n.id, ids[i]]));
  tr.sig = sig;
}

// --- hosts ----------------------------------------------------------------------------------------------------------

function ntEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function ntMonaco() {
  return typeof dockMonaco !== "undefined" ? dockMonaco : null;
}

// ntWhy: why a note cannot be made now, "" when it can.
function ntWhy(hs) {
  if (!hs.h.sid()) return "Notes belong to a session; this file is not open in one";
  if (ntStore === null) return "Waiting for the session list; notes can be made once it is here";
  return "";
}

function ntToast(text) {
  if (typeof showToast === "function") showToast(text);
}

// The lines the base has that the file has not (or has changed): the original side's, as Monaco's own diff computed them.
function ntRemoved(hs, line) {
  const d = hs.h.diff;
  const ch = typeof d.getLineChanges === "function" ? d.getLineChanges() : null;
  return !!ch && ch.some((c) => c.originalEndLineNumber > 0 && line >= c.originalStartLineNumber && line <= c.originalEndLineNumber);
}

// Is the original editor on screen? In Inline (which a narrow dock also gives) Monaco leaves it as a strip with its line numbers only,
// 38 px wide: its text area is negative (measured in Chromium, 0.57.0).
function ntOrigShown(hs) {
  try { return hs.orig.getLayoutInfo().contentWidth > 40; } catch (_) { return true; }
}

function ntLineCount(ed) {
  const m = ed.getModel();
  return m ? m.getLineCount() : 0;
}

// ntPlace: the editor and the line a note of `side` at `line` is drawn after. An old-side note is on the original editor, or, in Inline,
// on the modified one after the change that holds its line (the last changed line of it, or the line a pure removal follows).
function ntPlace(hs, side, line) {
  if (side !== "old") return { ed: hs.mod, line: Math.max(0, Math.min(line, ntLineCount(hs.mod))) };
  if (ntOrigShown(hs)) return { ed: hs.orig, line: Math.max(0, Math.min(line, ntLineCount(hs.orig))) };
  const d = hs.h.diff;
  const ch = (typeof d.getLineChanges === "function" ? d.getLineChanges() : null) || [];
  const c = ch.find((x) => x.originalEndLineNumber > 0 && line >= x.originalStartLineNumber && line <= x.originalEndLineNumber);
  const at = c ? (c.modifiedEndLineNumber > 0 ? c.modifiedEndLineNumber : c.modifiedStartLineNumber) : line;
  return { ed: hs.mod, line: Math.max(0, Math.min(at, ntLineCount(hs.mod))) };
}

// editorNotesAttachHost: the diff editor of `h` shows and takes notes from now until editorNotesDetachHost. Idempotent per key.
function editorNotesAttachHost(h) {
  if (!h || !h.diff || ntHosts.has(h.key)) return false;
  const hs = { h, mod: h.diff.getModifiedEditor(), orig: h.diff.getOriginalEditor(), zones: new Map(), subs: [], unsub: null, dead: false,
    hover: { mod: { ids: [], line: 0 }, orig: { ids: [], line: 0 } }, compose: null, editId: null, focusSoon: null, sbs: true };
  ntHosts.set(h.key, hs);
  const M = ntMonaco();
  const T = M && M.editor && M.editor.MouseTargetType ? M.editor.MouseTargetType : {};
  for (const [name, ed] of [["mod", hs.mod], ["orig", hs.orig]]) {
    hs.subs.push(ed.onMouseMove((e) => ntMouseMove(hs, name, ed, e, T)));
    hs.subs.push(ed.onMouseLeave(() => ntSetHover(hs, name, 0)));
    hs.subs.push(ed.onMouseDown((e) => ntMouseDown(hs, name, e, T)));
    // The original editor is on screen or not (Inline, a narrow dock) without anything of ours having changed: its notes move.
    hs.subs.push(ed.onDidLayoutChange(() => { ntFitAll(hs); const s = ntOrigShown(hs); if (s !== hs.sbs) { hs.sbs = s; ntPaint(hs); } }));
  }
  hs.sbs = ntOrigShown(hs);
  // A new diff of the two texts (the base moved, the file changed) moves where an Inline old-side note is drawn.
  hs.subs.push(h.diff.onDidUpdateDiff(() => ntPaint(hs)));
  if (typeof h.onReplace === "function") hs.unsub = h.onReplace((text) => { ntCheck(hs, text); ntPaint(hs, true); });
  ntCheck(hs); // the file (or the base) may have moved while no Changes view was open
  const want = typeof h.takeWant === "function" ? h.takeWant() : null;
  const dk = ntDraftKey(hs);
  const draft = dk ? ntDrafts.get(dk) : null;
  if (want) {
    ntCompose(hs, "new", want.line, "", true);
    // A view that was just built shows its first change when Monaco's diff is computed, and that moves the lines: the box is shown after.
    const c = hs.compose;
    if (c) {
      const sub = h.diff.onDidUpdateDiff(() => { sub.dispose(); ntLater(() => ntShowCompose(hs, c)); });
      hs.subs.push(sub);
    }
  }
  else if (draft && ntWhy(hs) === "") ntCompose(hs, draft.side, draft.line, draft.text, false);
  else ntPaint(hs);
  return true;
}

function ntDraftKey(hs) {
  const sid = hs.h.sid();
  return sid ? sid + "\n" + hs.h.path() : "";
}

// editorNotesDetachHost: the diff editor is going (or is let go): its zones, hover marks and listeners are disposed. A note box that
// holds text is kept as a draft for the file's next Changes view.
function editorNotesDetachHost(key) {
  const hs = ntHosts.get(key);
  if (!hs) return false;
  hs.dead = true;
  ntHosts.delete(key);
  const dk = ntDraftKey(hs);
  if (dk && hs.compose && hs.compose.ta.value.trim()) ntDrafts.set(dk, { side: hs.compose.side, line: hs.compose.line, text: hs.compose.ta.value });
  for (const s of hs.subs) { try { s.dispose(); } catch (_) { /* gone with its editor */ } }
  hs.subs = [];
  if (hs.unsub) { hs.unsub(); hs.unsub = null; }
  for (const name of ["mod", "orig"]) { try { ntSetHover(hs, name, 0); } catch (_) { /* editor already gone */ } }
  for (const z of hs.zones.values()) ntDrop(z, false);
  hs.zones.clear();
  hs.compose = null;
  return true;
}

// The EditorTab's host (R4): editordiff.js calls these when its Changes view is built and when it is let go.
function editorNotesAttach(tab) {
  if (!tab || !tab.diff || tab.disposed) return false;
  return editorNotesAttachHost({
    key: tab,
    sid: () => (tab.host && typeof tab.host.sessionId === "function" ? tab.host.sessionId() : null),
    path: () => tab.path,
    rel: () => (typeof relativePath === "function" && typeof ftRoot === "function" ? relativePath(ftRoot(), tab.path) : null) || tab.path,
    diff: tab.diff.diff,
    onReplace: (fn) => editorSyncOnReplace(tab, fn),
    takeWant: () => {
      const w = tab.ntWant;
      tab.ntWant = null;
      return w && Date.now() - w.at < NT_WANT_MS ? w : null;
    },
  });
}

function editorNotesDetach(tab) {
  return editorNotesDetachHost(tab);
}

// editorNotesBaseMoved: the base text of the tab's open Changes view was replaced (a commit, the other scope): its old-side notes are
// checked against the new one.
function editorNotesBaseMoved(tab) {
  const hs = ntHosts.get(tab);
  if (hs) ntCheck(hs);
}

// ntCheck: follow the host's file. `text` is the buffer's new text, or the model's when not given.
function ntCheck(hs, text) {
  const sid = hs.h.sid();
  if (!sid || ntStore === null) return;
  const path = hs.h.path();
  const cur = notesFor(sid);
  if (!cur.some((n) => n.path === path)) return;
  const mm = hs.mod.getModel();
  const om = hs.orig.getModel();
  let next = cur;
  if (mm || text !== undefined) next = reanchor(next, path, text !== undefined ? text : mm.getValue());
  if (om) next = reanchorOld(next, path, om.getValue());
  if (next.some((n, i) => n !== cur[i])) ntSet(sid, next);
}

// --- the note boxes -------------------------------------------------------------------------------------------------

// ntBox: the text box of a new note and of an edit: a textarea.dk-note-in, Save (Mod+Enter) and Cancel (Esc). Empty text cannot be
// saved (the button says so and a press does nothing). {el, ta}.
function ntBox(initial, label, onSave, onCancel) {
  const el = ntEl("div", "dk-note dk-note-edit");
  const ta = ntEl("textarea", "dk-note-in");
  ta.rows = 3;
  ta.maxLength = NT_MAX_TEXT;
  ta.value = initial;
  ta.placeholder = "A note for the agent. The notes are pasted to its prompt as one message.";
  ta.setAttribute("aria-label", label);
  const foot = ntEl("div", "dk-note-foot");
  const hint = ntEl("span", "dk-note-hint", hintTitle("Save", "editor.noteSave", IS_MAC) + " · Esc cancels");
  const cancel = ntEl("button", "btn btn-sm btn-ghost", "Cancel");
  cancel.type = "button";
  cancel.onclick = () => onCancel();
  const save = ntEl("button", "btn btn-sm btn-primary", "Save note");
  save.type = "button";
  let empty = null;
  const sync = () => {
    const now = ta.value.trim() === "";
    if (now === empty) return;
    empty = now;
    save.setAttribute("aria-disabled", now ? "true" : "false");
    save.title = now ? "Write the note first" : "";
  };
  sync();
  save.onclick = () => { if (ta.value.trim() !== "") onSave(ta.value.trim()); else ta.focus(); };
  ta.oninput = sync;
  // Keys typed here are the note's: not Monaco's (a `c` is a letter) and not the page's (Esc here must not leave Full mode).
  ta.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Escape") { e.preventDefault(); onCancel(); return; }
    if (shortcutMatches("editor.noteSave", e, IS_MAC)) {
      e.preventDefault();
      if (ta.value.trim() !== "") onSave(ta.value.trim());
    }
  };
  foot.append(hint, cancel, save);
  el.append(ta, foot);
  return { el, ta };
}

function ntNoteWhere(n) {
  return (n.side === "old" ? "removed line " : "line ") + n.line;
}

function ntMeta(n) {
  const rel = typeof relTime === "function" ? relTime(n.at, Date.now()) : "";
  return (n.outdated ? "was on " : "on ") + ntNoteWhere(n) + (rel ? " · " + rel : "");
}

// ntNoteView: one note as the mockup draws it: "You · on line N · <time>", the text, Edit and Delete. An outdated note says so and
// shows its quote (the line it was made on); a sent one says so (it is only drawn when Show sent is on).
function ntNoteView(hs, n) {
  const el = ntEl("div", "dk-note" + (n.outdated ? " dk-note-out" : "") + (n.status === "sent" ? " dk-note-sent" : ""));
  el.setAttribute("data-note", n.id);
  const head = ntEl("div", "dk-note-h");
  const meta = ntEl("span", "dk-note-meta", ntMeta(n));
  head.append(ntEl("span", "dk-note-av", "Y"), ntEl("span", "dk-note-who", "You"), meta);
  if (n.outdated) head.append(ntEl("span", "dk-note-badge", "outdated"));
  if (n.status === "sent") head.append(ntEl("span", "dk-note-badge dk-note-badge-sent", "sent"));
  const act = ntEl("span", "dk-note-act");
  const edit = ntEl("button", "dk-note-b-act", "Edit");
  edit.type = "button";
  edit.setAttribute("aria-label", "Edit the note on " + ntNoteWhere(n));
  edit.onclick = () => ntEdit(hs, n.id);
  const del = ntEl("button", "dk-note-b-act", "Delete");
  del.type = "button";
  del.setAttribute("aria-label", "Delete the note on " + ntNoteWhere(n));
  del.onclick = () => ntDelete(hs, n.id);
  act.append(edit, del);
  head.append(act);
  el.append(head);
  if (n.outdated) {
    const q = ntEl("div", "dk-note-q", n.quote);
    q.title = "The line this note was made on";
    el.append(q);
  }
  el.append(ntEl("div", "dk-note-body", n.text));
  return { el, label: "Note on " + ntNoteWhere(n), tick: () => { const t = ntMeta(n); if (meta.textContent !== t) meta.textContent = t; } };
}

// ntTickAll: the notes' times are refreshed (a label that said "just now" an hour ago is a wrong one). Not on a timer of its own (the
// dock has one interval, and a diff nobody touches needs none): when the pointer moves over a diff, at most once a minute.
function ntTickAll() {
  const now = Date.now();
  if (now - ntTickAt < NT_TICK_MS) return;
  ntTickAt = now;
  for (const hs of ntHosts.values()) for (const z of hs.zones.values()) if (z.tick) z.tick();
}

// --- the view zones -------------------------------------------------------------------------------------------------

// ntWanted: what the host's editors should hold: a zone per visible note (pending, and sent ones while Show sent is on), the note
// box of a new note and the box of the one being edited. Each has a signature: a zone whose signature did not change keeps its DOM.
function ntWanted(hs) {
  const out = [];
  const sid = hs.h.sid();
  const path = hs.h.path();
  const sent = sid ? ntShowSent.has(sid) : false;
  for (const n of sid ? notesFor(sid) : NT_NONE) {
    if (n.path !== path || (n.status !== "pending" && !sent)) continue;
    const at = ntPlace(hs, n.side, n.line);
    const editing = hs.editId === n.id;
    out.push({
      key: "n:" + n.id, ed: at.ed, line: at.line, h0: editing ? NT_COMPOSE_H : NT_NOTE_H,
      sig: editing ? "edit" : [n.text, n.status, n.outdated, n.side, n.line].join("\u0001"),
      build: () => (editing ? ntEditView(hs, n) : ntNoteView(hs, n)),
    });
  }
  if (hs.compose) {
    const c = hs.compose;
    const at = ntPlace(hs, c.side, c.line);
    out.push({ key: c.key, ed: at.ed, line: at.line, h0: NT_COMPOSE_H, sig: c.key, build: () => ({ el: c.box.el, label: "New note on " + (c.side === "old" ? "removed line " : "line ") + c.line }) });
  }
  return out;
}

// ntPaint brings the host's zones in step with ntWanted: added, removed, filled again when their signature changed, moved when their
// line did. `force` puts every zone back (the text under them was replaced, and Monaco does not follow it).
function ntPaint(hs, force) {
  if (hs.dead) return;
  const want = ntWanted(hs);
  const byKey = new Map(want.map((w) => [w.key, w]));
  const groups = new Map();
  const grp = (ed) => { if (!groups.has(ed)) groups.set(ed, { del: [], re: [], mv: [], add: [] }); return groups.get(ed); };
  for (const z of [...hs.zones.values()]) {
    const w = byKey.get(z.key);
    if (!w || w.ed !== z.ed) { hs.zones.delete(z.key); grp(z.ed).del.push(z); continue; }
    if (w.sig !== z.sig) ntFill(z, w);
    if (force) grp(z.ed).re.push([z, w]);
    else if (w.line !== z.line) grp(z.ed).mv.push([z, w]);
  }
  for (const w of want) if (!hs.zones.has(w.key)) grp(w.ed).add.push(w);
  for (const [ed, g] of groups) {
    ed.changeViewZones((acc) => {
      for (const z of g.del) { z.dead = true; acc.removeZone(z.id); ed.removeOverlayWidget(z.widget); ntUnwatch(z); }
      for (const [z, w] of g.mv) { z.zone.afterLineNumber = w.line; z.line = w.line; acc.layoutZone(z.id); }
      for (const [z, w] of g.re) { acc.removeZone(z.id); z.zone.afterLineNumber = w.line; z.line = w.line; z.id = acc.addZone(z.zone); }
      for (const w of g.add) {
        // Two parts (VS Code's ZoneWidget): the view zone is only the room, an empty spacer of the card's height that Monaco keeps under
        // the line; the card is an overlay widget, put at the spacer's top (onDomNodeTop). Monaco marks the view zones' own container
        // aria-hidden, so a card inside it would be invisible to assistive technology and its buttons would be focusable under
        // aria-hidden (measured in 0.57.0); the overlay layer is not hidden.
        const spacer = ntEl("div", "dk-nz-room");
        const node = ntEl("div", "dk-nz");
        const box = ntEl("div", "dk-nz-in");
        node.setAttribute("role", "group");
        node.setAttribute("data-nz", w.key);
        spacer.setAttribute("data-nz", w.key);
        node.style.top = "-100000px"; // until Monaco has said where the room is
        node.append(box);
        const z = { key: w.key, ed, node, box, zone: null, widget: null, line: w.line, sig: "", h: w.h0, top: null, id: 0, tick: null, ro: null, dead: false, pending: false };
        z.zone = { afterLineNumber: w.line, heightInPx: w.h0, domNode: spacer, onDomNodeTop: (top) => ntTop(z, top) };
        z.widget = { getId: () => "dk.note." + w.key, getDomNode: () => node, getPosition: () => null };
        z.id = acc.addZone(z.zone);
        ed.addOverlayWidget(z.widget);
        node.addEventListener("wheel", (e) => ntWheel(z, e), { passive: false });
        hs.zones.set(w.key, z);
        ntFill(z, w);
        ntFit(z);
        ntWatch(z);
      }
    });
  }
  if (hs.focusSoon) { const f = hs.focusSoon; hs.focusSoon = null; ntLater(f); }
}

// ntFit: the card is as wide as the editor's text area and starts where it does. An overlay widget does not scroll with the lines, so a
// text box stays in view when a long line is scrolled sideways.
function ntFit(z) {
  const info = z.ed.getLayoutInfo();
  const w = Math.max(160, info.contentWidth);
  if (z.fitW === w && z.fitL === info.contentLeft) return;
  z.fitW = w;
  z.fitL = info.contentLeft;
  z.node.style.width = w + "px";
  z.node.style.left = info.contentLeft + "px";
}

// ntTop: Monaco says, on every frame it draws, where the room under the line is (in the editor's own pixels: a room scrolled out of
// view is told a far negative one, and the card goes there too).
function ntTop(z, top) {
  if (z.top === top) return;
  z.top = top;
  z.node.style.top = top + "px";
}

// ntWheel: the card is not in the scrolled element, so the wheel over it would stop; it goes to the editor, except over a text box that
// has text of its own to scroll.
function ntWheel(z, e) {
  const t = e.target;
  if (t && t.tagName === "TEXTAREA" && t.scrollHeight > t.clientHeight) return;
  e.preventDefault();
  const px = e.deltaMode === 1 ? 19 : e.deltaMode === 2 ? z.ed.getLayoutInfo().height : 1;
  z.ed.setScrollPosition({ scrollTop: z.ed.getScrollTop() + e.deltaY * px, scrollLeft: z.ed.getScrollLeft() + e.deltaX * px });
}

function ntFitAll(hs) {
  for (const z of hs.zones.values()) ntFit(z);
}

function ntFill(z, w) {
  const v = w.build();
  z.box.replaceChildren(v.el);
  z.node.setAttribute("aria-label", v.label);
  z.tick = v.tick || null;
  z.sig = w.sig;
}

function ntLater(fn) {
  if (typeof requestAnimationFrame === "function") requestAnimationFrame(() => fn()); else setTimeout(fn, 0);
}

// ntWatch: a zone is as tall as what is in it, and that changes with the dock's width (the text wraps) and with the text.
function ntWatch(z) {
  if (typeof ResizeObserver !== "function") return;
  z.ro = new ResizeObserver(() => {
    if (z.pending) return;
    z.pending = true;
    ntLater(() => { z.pending = false; ntSize(z); });
  });
  z.ro.observe(z.box);
}

function ntUnwatch(z) {
  if (z.ro) { z.ro.disconnect(); z.ro = null; }
}

function ntSize(z) {
  if (z.dead || typeof z.box.offsetHeight !== "number") return;
  const h = z.box.offsetHeight;
  if (!(h > 0) || h === z.h) return;
  z.h = h;
  z.zone.heightInPx = h;
  z.ed.changeViewZones((acc) => acc.layoutZone(z.id));
}

// ntDrop: a zone goes with its editor, or on its own (`remove`: the editor stays and is told).
function ntDrop(z, remove) {
  z.dead = true;
  ntUnwatch(z);
  if (!remove) { try { z.ed.changeViewZones((acc) => acc.removeZone(z.id)); z.ed.removeOverlayWidget(z.widget); } catch (_) { /* the editor is gone */ } }
}

// --- the glyph margin's "+" -----------------------------------------------------------------------------------------

// ntSetHover puts the "+" in the glyph margin of one editor's line (0: none). One decoration, moved with the pointer, written only when
// the line changes.
function ntSetHover(hs, name, line) {
  const st = hs.hover[name];
  if (st.line === line) return;
  st.line = line;
  const ed = name === "orig" ? hs.orig : hs.mod;
  st.ids = ed.deltaDecorations(st.ids, line ? [{
    range: { startLineNumber: line, startColumn: 1, endLineNumber: line, endColumn: 1 },
    options: { glyphMarginClassName: "dk-note-add", glyphMarginHoverMessage: { value: hintTitle("Add a note", "editor.addNote", IS_MAC) } },
  }] : []);
}

// The pointer over a text line or its margins (not over a zone or a widget) shows the "+" there; on the original side only on a line
// the base has and the file has not.
function ntMouseMove(hs, name, ed, e, T) {
  ntTickAll();
  const t = e && e.target;
  const ok = t && t.position && [T.GUTTER_GLYPH_MARGIN, T.GUTTER_LINE_NUMBERS, T.GUTTER_LINE_DECORATIONS, T.CONTENT_TEXT, T.CONTENT_EMPTY].includes(t.type);
  const line = ok ? t.position.lineNumber : 0;
  ntSetHover(hs, name, line && (name === "mod" || ntRemoved(hs, line)) ? line : 0);
}

function ntMouseDown(hs, name, e, T) {
  const t = e && e.target;
  if (!t || t.type !== T.GUTTER_GLYPH_MARGIN || !t.position || (e.event && e.event.leftButton === false)) return;
  if (hs.hover[name].line !== t.position.lineNumber) return; // the "+" is not on this line
  ntCompose(hs, name === "orig" ? "old" : "new", t.position.lineNumber, "", true);
}

// --- composing, editing, deleting -----------------------------------------------------------------------------------

// ntCompose opens the box of a new note under `line` of `side` (a box that already holds text is kept and taken to: it is not
// thrown away for a second request). `focus`: the textarea takes the keyboard (not for a draft that comes back with its view).
function ntCompose(hs, side, line, text, focus) {
  const why = ntWhy(hs);
  if (why) { ntToast(why); return false; }
  if (hs.compose) {
    if (hs.compose.ta.value.trim() !== "") { hs.focusSoon = () => hs.compose && hs.compose.ta.focus(); ntPaint(hs); return true; }
    hs.compose = null;
  }
  ntSeq += 1;
  const c = { key: "compose:" + ntSeq, side, line, box: null, ta: null };
  const close = (back) => {
    if (hs.compose !== c) return;
    hs.compose = null;
    const dk = ntDraftKey(hs);
    if (dk) ntDrafts.delete(dk);
    ntPaint(hs);
    if (back) ntLater(() => { try { (c.side === "old" ? hs.orig : hs.mod).focus(); } catch (_) { /* editor gone */ } });
  };
  c.box = ntBox(text || "", "Note on " + (side === "old" ? "removed line " : "line ") + line, (txt) => ntSaveNew(hs, c, txt), () => close(true));
  c.ta = c.box.ta;
  hs.compose = c;
  if (focus) hs.focusSoon = () => ntShowCompose(hs, c);
  ntPaint(hs);
  return true;
}

// ntShowCompose: the note box is brought into view and takes the keyboard.
function ntShowCompose(hs, c) {
  if (hs.dead || hs.compose !== c) return;
  const at = ntPlace(hs, c.side, c.line);
  if (typeof at.ed.revealLineInCenterIfOutsideViewport === "function") at.ed.revealLineInCenterIfOutsideViewport(Math.max(1, at.line));
  ntLater(() => ntTakeKeyboard(hs, c, 12)); // after Monaco has drawn the view the reveal moved
}

// A zone that was scrolled out of view is hidden by Monaco until its next frame, and a hidden element cannot take the keyboard (measured:
// the first focus after Changes opened was lost when the diff, once computed, moved the view). So the focus waits for the text box to be
// on screen and is tried on the next frames until it has stuck.
function ntTakeKeyboard(hs, c, tries) {
  if (hs.dead || hs.compose !== c) return;
  // On screen: Monaco has put its room in view (a card of a room that is scrolled out is far above the editor) and it has a size.
  const z = hs.zones.get(c.key);
  const real = typeof c.ta.offsetHeight === "number";
  const placed = !!z && (typeof z.top === "number" ? z.top > -50000 : !real);
  const shown = placed && (!real || c.ta.offsetHeight > 0);
  if (shown) c.ta.focus();
  if (tries > 0 && (!shown || document.activeElement !== c.ta)) ntLater(() => ntTakeKeyboard(hs, c, tries - 1));
}

function ntSaveNew(hs, c, text) {
  const why = ntWhy(hs);
  if (why) { ntToast(why); return; }
  const sid = hs.h.sid();
  const model = (c.side === "old" ? hs.orig : hs.mod).getModel();
  const quote = model && c.line >= 1 && c.line <= model.getLineCount() ? model.getLineContent(c.line) : "";
  const o = { path: hs.h.path(), rel: hs.h.rel(), side: c.side, line: c.line, quote, text };
  hs.compose = null; // the box goes with the paint ntSet does
  const dk = ntDraftKey(hs);
  if (dk) ntDrafts.delete(dk);
  ntSet(sid, noteAdd(notesFor(sid), o));
  ntLater(() => { try { (c.side === "old" ? hs.orig : hs.mod).focus(); } catch (_) { /* editor gone */ } });
}

// An edit is the note's own zone showing a box in place of the note.
function ntEditView(hs, n) {
  const box = ntBox(n.text, "Edit the note on " + ntNoteWhere(n), (txt) => {
    hs.editId = null;
    notesUpdate(hs.h.sid(), (l) => noteEdit(l, n.id, txt));
    ntPaint(hs);
    ntLater(() => { try { hs.mod.focus(); } catch (_) { /* editor gone */ } });
  }, () => { hs.editId = null; ntPaint(hs); ntLater(() => { try { hs.mod.focus(); } catch (_) { /* editor gone */ } }); });
  hs.focusSoon = () => box.ta.focus();
  return { el: box.el, label: "Editing the note on " + ntNoteWhere(n) };
}

function ntEdit(hs, id) {
  hs.editId = id;
  ntPaint(hs);
}

function ntDelete(hs, id) {
  if (hs.editId === id) hs.editId = null;
  notesUpdate(hs.h.sid(), (l) => noteDelete(l, id));
  ntPaint(hs);
  ntLater(() => { try { hs.mod.focus(); } catch (_) { /* editor gone */ } });
}

// --- the keys and the selection bar ---------------------------------------------------------------------------------

// editorNotesKey: Add note was asked for by a key (the registry's editor.addNote, bound on Monaco by editortab.js). The diff editor
// that has the keyboard takes it at its cursor (the first line of a selection); with the keyboard in the dock's Code editor it opens
// Changes at that line. Anywhere else nothing happens.
function editorNotesKey() {
  for (const hs of ntHosts.values()) {
    for (const [name, ed] of [["new", hs.mod], ["old", hs.orig]]) {
      if (!ed.hasTextFocus()) continue;
      const sel = typeof selectionLines === "function" ? selectionLines(ed.getSelection()) : null;
      const line = sel ? sel.a : ed.getPosition().lineNumber;
      if (name === "old" && !ntRemoved(hs, line)) {
        ntToast("Only a removed line takes a note on this side. Use the right side for a line as it is now.");
        return;
      }
      ntCompose(hs, name, line, "", true);
      return;
    }
  }
  const tab = typeof dockEditorOwner !== "undefined" ? dockEditorOwner : null;
  if (tab && typeof dockEditor !== "undefined" && dockEditor && dockEditor.hasTextFocus() && !tab.diff && !tab.previewing && tab.model && !tab.inner) {
    const sel = typeof selectionLines === "function" ? selectionLines(dockEditor.getSelection()) : null;
    editorNotesFromCode(tab, sel || { a: dockEditor.getPosition().lineNumber, b: dockEditor.getPosition().lineNumber });
  }
}

// editorNotesFromCode: Add note in Code (the selection bar's button, Mod+Shift+A). Notes are read in the diff, so Changes opens (the
// base text is asked for if it is not here yet) with the note box under the first line of the selection. Refused with the reason
// when notes cannot be made or Changes cannot be had.
function editorNotesFromCode(tab, range) {
  const sid = tab.host && typeof tab.host.sessionId === "function" ? tab.host.sessionId() : null;
  const why = !sid ? "Notes belong to a session; this file is not open in one" : ntStore === null ? "Waiting for the session list; notes can be made once it is here" : "";
  if (why) { ntToast(why); return false; }
  tab.ntWant = { line: range.a, at: Date.now() };
  if (!tab.showChanges(true)) { tab.ntWant = null; return false; }
  return true;
}

selBarAddAction({ key: "note", label: "Add note", icon: "pencil", run: (tab, range) => { editorNotesFromCode(tab, range); } });
