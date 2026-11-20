"use strict";

// The review loop in the Files dock's Changes view (spec "Review loop (P3)"): the header row over the diff, F7 / Shift+F7 across the
// session's changed files, and the reviewed marks. DOM and the page's state only; what a mark is, how to step from change to change
// and what the header counts are reviewmodel.js's, which node tests run.
//
// The header row (.dk-dhead, mockup B2): the file's name, what the two sides are, `+a −d` (git's count, from the Changes row of the
// file), "Change N of M" with the previous and next buttons and the F7 hint, and Mark reviewed. editorDiffEnter builds it
// (editorReviewHead) and everything that can move what it says calls editorReviewPaint: the cursor, a new diff, a mark, the Changes
// rows, Activity's list. It writes only what differs.
//
// F7 / Shift+F7 (the registry's editor.nextChange / editor.prevChange; editortab.js binds them on Monaco, which has an F7 of its own in
// a diff editor, "next difference": a command added with addCommand outranks it, measured). The next change of the file's lineDiff
// changes (the same list the gutter and "N of M" use, not Monaco's own) after the cursor; past the last one the next row of the Changes
// section (dockChangesRows) opens in Changes at its first change; Shift+F7 before the first opens the previous row on its LAST
// change. A folder and a provisional row are skipped; past the last row (or before the first) a toast says so and nothing moves.
//
// Reviewed marks. A mark is made only for a Changes row of a session whose Activity list the page holds (drawerChangedFiles): the mark
// takes its baseline, the newest agent edit of the file, from that list, and one made without it would clear on the first older edit
// (reviewmodel.js). While the list is not known the button says so and does nothing. The stored marks are read once, by
// reviewMarksLoad, after the first successful session list: loadReviewed drops every session it is not told is alive, so it must
// never see an empty or a failed list. Until then Mark reviewed is disabled too. The set of a session is replaced on every change
// (never edited in place), so identity says "new" to dockChangesRender.

const REVIEW_NONE = Object.freeze({});
const REVIEW_LAND_MS = 10000; // a landing asked for that the view did not take (Changes could not open) is forgotten after this
const DG_ICON_UP = [["path", { d: "M3.5 8.5L7 5l3.5 3.5" }]];
const DG_ICON_DOWN = [["path", { d: "M3.5 5.5L7 9l3.5-3.5" }]];

let reviewMarks = null; // Map: session id -> {<abs path>: mark}; null until the stored marks were read

function reviewStorage() {
  try { return typeof localStorage !== "undefined" ? localStorage : null; } catch (_) { return null; }
}

// reviewMarksLoad: the first successful session list is here (app.js), `sids` the ids in it. Read once; a list with nothing in it
// says nothing about which sessions are gone, so it reads nothing and the next list tries again.
function reviewMarksLoad(sids) {
  if (reviewMarks !== null || !Array.isArray(sids) || !sids.length) return;
  const st = reviewStorage();
  reviewMarks = st ? loadReviewed(st, sids) : new Map();
  editorReviewChanged();
}

// reviewMarksOf: the session's marks, {<abs path>: mark}; the same frozen empty object while it has none.
function reviewMarksOf(sid) {
  return (reviewMarks && sid && reviewMarks.get(sid)) || REVIEW_NONE;
}

// reviewFilesOf: the session's Activity files the page holds, or null while they are not known (before the first Activity answer).
function reviewFilesOf(sid) {
  const files = typeof drawerChangedFiles !== "undefined" && sid ? drawerChangedFiles.get(sid) : null;
  return Array.isArray(files) ? files : null;
}

// reviewWhy: why `row` of the session cannot be marked now, "" when it can.
function reviewWhy(sid, row) {
  if (!sid) return "Reviewed marks belong to a session; this file is not open in one";
  if (reviewMarks === null) return "Waiting for the session list; marks can be made once it is here";
  if (!reviewFilesOf(sid)) return "Waiting for the session's activity; a mark made now could not tell a later edit from an earlier one";
  if (!row) return "Only a changed file can be marked, and this one has no changes in this scope";
  return "";
}

// reviewMark: mark the file reviewed (what it shows now, and the agent's edits so far). false when it cannot (reviewWhy).
function reviewMark(sid, path, row) {
  if (reviewWhy(sid, row) || !row || row.path !== path) return false;
  const marks = Object.assign({}, reviewMarksOf(sid), { [path]: reviewMarkFor(row, reviewFilesOf(sid), Date.now()) });
  reviewMarks.set(sid, marks);
  const st = reviewStorage();
  if (st) saveReviewed(st, sid, marks);
  editorReviewChanged();
  return true;
}

// reviewUnmark: take the mark off (the user pressed Reviewed again).
function reviewUnmark(sid, path) {
  const cur = reviewMarksOf(sid);
  if (!reviewMarks || !Object.prototype.hasOwnProperty.call(cur, path)) return false;
  const marks = Object.assign({}, cur);
  delete marks[path];
  reviewMarks.set(sid, marks);
  const st = reviewStorage();
  if (st) saveReviewed(st, sid, marks);
  editorReviewChanged();
  return true;
}

// editorReviewChanged: something the header or the Changes list reads moved (a mark, Changes opened or closed, the marks loaded).
function editorReviewChanged() {
  if (typeof dockChangesRender === "function") dockChangesRender();
  editorReviewRefresh();
}

// editorReviewRefresh: the Changes view on screen (if any) repaints its header: the rows or Activity's list moved.
function editorReviewRefresh() {
  if (edTab && edTab.diff) editorReviewPaint(edTab);
  if (typeof reviewAllSync === "function") reviewAllSync(); // the All changes tab's headers and sections follow the same rows and marks
  editorImageSync(); // an image tab's toggle and comparer follow the same rows (imagediff.js)
}

function editorReviewSid(tab) {
  return tab.host && typeof tab.host.sessionId === "function" ? tab.host.sessionId() : null;
}

// editorReviewRow: the Changes row of the tab's file, or null (an unchanged file, or a provisional row).
function editorReviewRow(tab) {
  const rows = typeof dockChangesRows === "function" ? dockChangesRows() : [];
  return rows.find((r) => r.path === tab.path && !r.folder && !r.provisional) || null;
}

// --- the header ---------------------------------------------------------------------------------------------------

// editorReviewHead builds the header row of the Changes view: {el, label, ...} (label: the span editorDiffPaintLabel writes).
function editorReviewHead(tab) {
  const span = (cls, text) => { const e = document.createElement("span"); e.className = cls; if (text !== undefined) e.textContent = text; return e; };
  const iconBtn = (icon, label, dir) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn btn-ghost btn-icon btn-sm dk-dh-step";
    b.setAttribute("aria-label", label);
    b.append(svgIcon(icon, { icon: "view", size: "14" }));
    b.onclick = () => { if (b.getAttribute("aria-disabled") !== "true") editorReviewStep(tab, dir); };
    return b;
  };
  const el = document.createElement("div");
  el.className = "dk-dhead";
  const name = span("dk-dh-name", tab.name);
  name.title = tab.path;
  const label = span("dk-dh-cmp");
  const ct = span("dk-ct dk-dh-ct");
  const add = span("dk-add");
  const del = span("dk-del");
  ct.append(add, del);
  const right = span("dk-dh-r");
  const pos = span("dk-dh-pos");
  pos.setAttribute("aria-live", "polite");
  const prev = iconBtn(DG_ICON_UP, "Previous change", -1);
  const next = iconBtn(DG_ICON_DOWN, "Next change", 1);
  const kbd = span("dk-dh-kbd", typeof shortcutHint === "function" ? shortcutHint("editor.nextChange", IS_MAC) : "");
  const mark = document.createElement("button");
  mark.type = "button";
  mark.className = "btn btn-sm dk-dh-mark";
  mark.textContent = "Mark reviewed";
  mark.onclick = () => {
    if (mark.getAttribute("aria-disabled") === "true") return;
    const sid = editorReviewSid(tab);
    const row = editorReviewRow(tab);
    if (!row) return;
    const marks = reviewMarksOf(sid);
    const done = Object.prototype.hasOwnProperty.call(marks, tab.path) && reviewedState(marks[tab.path], row, reviewFilesOf(sid));
    if (done) reviewUnmark(sid, tab.path); else reviewMark(sid, tab.path, row);
  };
  // View all: the All changes tab (reviewall.js), at this file's section.
  const all = document.createElement("button");
  all.type = "button";
  all.className = "btn btn-ghost btn-sm dk-dh-all";
  all.textContent = "View all";
  all.title = "Show every changed file in one scroll";
  all.onclick = () => { if (typeof reviewAllOpen === "function") reviewAllOpen(editorReviewSid(tab), tab.path); };
  right.append(pos, prev, next, kbd, all, mark);
  el.append(name, label, ct, right);
  return { el, label, ct, add, del, pos, prev, next, mark, sig: "" };
}

// editorReviewPaint writes the header from what the page knows now. Text and attributes are written only when they differ, because the
// cursor moves it on every click and arrow.
function editorReviewPaint(tab) {
  const d = tab.diff;
  if (!d || !d.head || tab.disposed || !tab.model) return;
  const h = d.head;
  const set = (n, t) => { if (n.textContent !== t) n.textContent = t; };
  const row = editorReviewRow(tab);
  const sid = editorReviewSid(tab);
  // +a −d: git's count for the file, as the Changes row says it; none where git gave none.
  const known = !!row && row.add !== null && row.add !== undefined && row.del !== null && row.del !== undefined;
  h.ct.hidden = !known;
  if (known) { set(h.add, "+" + row.add); set(h.del, "−" + row.del); }
  // Change N of M, from the cursor on the new side.
  const modified = d.diff.getModifiedEditor();
  const p = modified.getPosition();
  const ch = tab.changes;
  const capped = !!ch && ch.capped;
  const lc = ch && ch.head !== null && !capped ? ch.lineChanges : [];
  const pos = reviewChangePos(lc, p ? p.lineNumber : 1, tab.dg ? tab.dg.navIndex : null, tab.model.getLineCount());
  set(h.pos, capped ? "Too many changes to number" : pos.text);
  // Previous and next: dimmed when there is neither a change nor a changed file in that direction.
  const rows = typeof dockChangesRows === "function" ? dockChangesRows() : [];
  const line = p ? p.lineNumber : 1;
  const moves = (dir) => !!nextChangeClamped(lc, line, dir, tab.model.getLineCount()) || !!reviewNeighbour(rows, tab.path, dir);
  const canNext = moves(1);
  const canPrev = moves(-1);
  h.next.setAttribute("aria-disabled", canNext ? "false" : "true");
  h.prev.setAttribute("aria-disabled", canPrev ? "false" : "true");
  const nextTitle = canNext ? hintTitle("Next change", "editor.nextChange", IS_MAC) : "No later change or changed file";
  const prevTitle = canPrev ? hintTitle("Previous change", "editor.prevChange", IS_MAC) : "No earlier change or changed file";
  if (h.next.title !== nextTitle) h.next.title = nextTitle;
  if (h.prev.title !== prevTitle) h.prev.title = prevTitle;
  // Mark reviewed: a toggle that says why when it cannot be pressed.
  const why = reviewWhy(sid, row);
  const marks = reviewMarksOf(sid);
  const done = !why && Object.prototype.hasOwnProperty.call(marks, tab.path) && reviewedState(marks[tab.path], row, reviewFilesOf(sid));
  const text = done ? "Reviewed" : "Mark reviewed";
  set(h.mark, text);
  h.mark.setAttribute("aria-disabled", why ? "true" : "false");
  h.mark.setAttribute("aria-pressed", done ? "true" : "false");
  const title = why || (done ? "Reviewed. Press to clear the mark." : "Mark this file reviewed. The mark clears if the agent edits it again.");
  if (h.mark.title !== title) h.mark.title = title;
}

// nextChangeClamped: nextChange over the changes as they are shown, each at its line clamped into the buffer. A removal at the very end
// of the text carries lastLine + 1 (reviewmodel.js nextChange), and the cursor cannot be there: unclamped it would be the "next" change
// of the cursor that sits on it, and F7 would never leave the file.
function nextChangeClamped(lineChanges, cursorLine, dir, lineCount) {
  const shown = lineChanges.map((c) => Object.assign({}, c, { from: reviewLineOf(c, lineCount) }));
  return nextChange(shown, cursorLine, dir);
}

// --- going to a change ----------------------------------------------------------------------------------------------

// editorReviewGo puts the cursor on change `index` of the tab's file and shows it, remembering it as the one the reader is at.
function editorReviewGo(tab, index) {
  const ch = tab.changes;
  const d = tab.diff;
  if (!d || !ch || !ch.lineChanges[index]) return;
  const line = reviewLineOf(ch.lineChanges[index], tab.model.getLineCount());
  const modified = d.diff.getModifiedEditor();
  editorDiffOf(tab).navIndex = index;
  modified.setPosition({ lineNumber: line, column: 1 });
  modified.revealLineInCenter(line, dockMonaco.editor.ScrollType.Immediate);
  editorReviewPaint(tab);
}

// editorReviewWantsFocus: the opening of this tab's Changes view was asked (by F7) to take the keyboard.
function editorReviewWantsFocus(tab) {
  const land = tab.dg && tab.dg.land;
  return !!land && land.focus === true && Date.now() - land.ts <= REVIEW_LAND_MS;
}

// editorReviewLanding: where the opening of this tab's Changes view was asked to land ("first", "last"), once; null when nothing was
// asked, it was asked long ago, or the file has no change to land on.
function editorReviewLanding(tab) {
  const s = editorDiffOf(tab);
  const land = s.land;
  s.land = null;
  const ch = tab.changes;
  if (!land || Date.now() - land.ts > REVIEW_LAND_MS || !ch || !ch.lineChanges.length) return null;
  return land.at;
}

// editorReviewStep: F7 (dir 1) / Shift+F7 (dir -1) in the Changes view of `tab`. The next change of the file after the cursor; none
// left: the neighbouring changed file opens in Changes. Returns "moved", "file" (another file is opening) or "none".
function editorReviewStep(tab, dir) {
  if (!tab || tab.disposed || !tab.diff || !tab.model) return "none";
  const ch = tab.changes;
  const lc = ch && ch.head !== null && !ch.capped ? ch.lineChanges : [];
  const modified = tab.diff.diff.getModifiedEditor();
  const p = modified.getPosition();
  const hit = nextChangeClamped(lc, p ? p.lineNumber : 1, dir, tab.model.getLineCount());
  if (hit) {
    editorReviewGo(tab, hit.index);
    modified.focus();
    return "moved";
  }
  const sid = editorReviewSid(tab);
  // After this command: it runs inside the diff editor's own keydown, and the tab it opens may replace this one (a preview tab).
  Promise.resolve().then(() => { if (!tab.disposed) reviewOpenNext(sid, tab.path, dir); });
  return "file";
}

// reviewOpenNext opens the changed file after (dir 1) or before (dir -1) `fromPath` among the Changes section's rows, in Changes, with
// the keyboard in it (`focus` of the landing, not `wantFocus`: a tab still being read gives the keyboard to the Code editor it claims first,
// which the Changes view then replaces): at its first change going forward and at its last going back. Past either end a toast says so.
function reviewOpenNext(sid, fromPath, dir) {
  const rows = typeof dockChangesRows === "function" ? dockChangesRows() : [];
  const next = reviewNeighbour(rows, fromPath, dir);
  if (typeof drawerState !== "undefined" && drawerState.sessionId !== sid) return false; // the dock moved to another session meanwhile
  if (!next) {
    const known = reviewRows(rows).some((r) => r.path === fromPath);
    showToast(!known ? "No other changed files" : dir === 1 ? "That was the last changed file" : "That was the first changed file");
    return false;
  }
  if (typeof drawerShowFile !== "function") return false;
  // The landing is told to the tab twice over: before the open, for a tab the dock already has (a hidden one opens its Changes view
  // inside the call), and after it, for one the call has just made (it reads its file later).
  const land = { at: dir === 1 ? "first" : "last", ts: Date.now(), focus: true };
  const tell = () => {
    const tab = typeof dockViewers !== "undefined" ? dockViewers.get(dockTabKey(next.path, null)) : null;
    if (tab) editorDiffOf(tab).land = land;
  };
  tell();
  if (!drawerShowFile(next.path, { view: "changes" })) return false;
  tell();
  return true;
}

// editorReviewKey: F7, Shift+F7 and Alt+Z pressed in the tab but outside the editor (on the header's buttons): the same as inside it.
// true when it did something (the caller stops the key).
function editorReviewKey(tab, e) {
  if (editorImageOn(tab)) { // an image's Changes view: no change inside it to step to, so F7 goes straight to the next file
    const dir = shortcutMatches("editor.nextChange", e, IS_MAC) ? 1 : shortcutMatches("editor.prevChange", e, IS_MAC) ? -1 : 0;
    if (dir) editorImageStep(tab, dir);
    return dir !== 0;
  }
  if (!tab.diff) return false;
  if (shortcutMatches("editor.nextChange", e, IS_MAC)) { editorReviewStep(tab, 1); return true; }
  if (shortcutMatches("editor.prevChange", e, IS_MAC)) { editorReviewStep(tab, -1); return true; }
  if (shortcutMatches("editor.wrap", e, IS_MAC)) { diffWrapToggle(); return true; }
  return false;
}
