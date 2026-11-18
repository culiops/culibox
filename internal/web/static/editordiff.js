"use strict";

// Changes vs HEAD in the Files dock's editor (spec "Editor (P2: Monaco)": "Bar actions", "Gutter", "HEAD text"). An EditorTab asks the
// server for its file as git has it at the base of the scope the Changes section shows (HEAD, or the merge base in Branch scope) and
// uses that text twice:
//   the gutter   lineDiff (linediff.js) of it against the live buffer, drawn beside the line numbers (dk-g-add, dk-g-mod, dk-g-del) and
//                in the overview ruler, from 300 ms after the last edit. An unsaved edit shows, as in VS Code;
//   Changes      the Code | Changes toggle in the dock's bar swaps the tab's body for Monaco's inline diff: the base text on the removed
//                side, the tab's own model on the new side. It is a review view (founder, 2026-11-17): read-only, editing is in Code,
//                typing in it does nothing (which also keeps single keys free for review later).
//
// The base text. POST /api/git/show {path, scope} answers {state: "ok" | "notrepo" | "unavailable", reason, missing, content, truncated}.
// It is asked only for a tab on screen, with git's status known and the dock visible, and kept on the tab as `tab.changes`:
//   {key, scope, baseName, head, missing, why, lineChanges, capped, at}
//   key          what the answer is for: the path, the scope, the HEAD commit the tree's status names and (Branch) the merge base. A
//                status that lands with the same key asks nothing, so an edit of the file, which moves no commit, costs no request;
//                nor does a keystroke, ever (the gutter diffs the text already held);
//   head         the base text; "" for a file the base does not have (a new file: every line is added); null when it could not be read;
//   why          when head is null, the sentence the toggle's title gives after "Changes unavailable:";
//   lineChanges  lineDiff's changes of head against the buffer (P3 reads them); [] when capped or head is null;
//   capped       lineDiff declined (too big, or too different): no marks, and no cost per keystroke;
//   at           when the answer landed. One that could not be read is asked again after DG_RETRY_MS, not on every status.
// A box tab has no host git: no request, the toggle says so. An image or a binary file has no toggle at all (the EditorTab's inner
// viewer owns the bar).
//
// Everything an answer touches is decided when it arrives: an answer for a tab that was disposed, or for a key the tab has moved
// on from (the scope was switched, HEAD moved, the file was re-pointed), is dropped and the newer key is asked for.
//
// What the editor lets go of. The diff editor and the model of the base text exist only while Changes is up: leaving it and
// EditorTab.dispose() dispose both. The gutter's timer and model subscription exist only while the tab is on screen.
//
// editorDiffAttach(tab) / editorDiffDetach(tab): `tab` is on screen with its model / is not (EditorTab.show, onHide).
// editorDiffSync(): the tree's git status landed, or the base or the scope moved (gitPaintTree, dockChangesRefresh).

const DG_DEBOUNCE_MS = 300;  // the gutter follows an edit this long after the last keystroke
const DG_RETRY_MS = 30000;   // a base that could not be read is asked for again after this (changesmodel.js CHANGES_BASE_RETRY_MS: the same patience)
const DG_FETCH_MS = 20000;   // a request that never answers must not hold the tab's one slot for good
// A decoration's end column: Monaco clamps it to the line, so a range with it covers its last line whole.
const DG_END_COL = 1073741824;
const DG_CLASS = { add: "dk-g-add", mod: "dk-g-mod", del: "dk-g-del" };
// The theme colours of the overview ruler marks (monacotheme.js defines them from the desktop tokens).
const DG_RULER = {
  add: "editorOverviewRuler.addedForeground",
  mod: "editorOverviewRuler.modifiedForeground",
  del: "editorOverviewRuler.deletedForeground",
};
const DG_BOX_WHY = "a file in a session's box has no git history";
const DG_RO_MSG = "Changes is a review view. Switch to Code to edit.";

let edTab = null; // the tab on screen with its model: the one a status or a scope change is for

// editorDiffOf: what this file keeps on a tab: the request slot, the debounce, the gutter's decorations (ids, and the text of
// the changes they draw), the line a link asked for, and whether the user asked for Changes before the base text was in.
function editorDiffOf(tab) {
  if (!tab.dg) tab.dg = { flight: false, want: false, timer: null, sub: null, ids: [], last: "", targetIds: [], targetLine: null };
  return tab.dg;
}

// --- pure pieces -------------------------------------------------------------------------------------------------

// editorDiffLabel: what the Changes view says it compares with.
function editorDiffLabel(scope, baseName) {
  if (scope !== "branch") return "vs HEAD";
  return baseName ? "vs " + baseName + " (merge base)" : "vs the merge base";
}

// editorDiffBase turns the server's answer (null: the request failed) into {head, missing, why}: the text to compare with, or
// head null and the reason it cannot be had. A file the base does not have is an empty text, so every line of it reads as added.
function editorDiffBase(blob) {
  const none = (why) => ({ head: null, missing: false, why });
  if (!blob || typeof blob !== "object") return none("the server did not answer");
  if (blob.state === "notrepo") return none("this file is not in a git repository");
  if (blob.state !== "ok") return none(typeof blob.reason === "string" && blob.reason ? blob.reason : "git is not available here");
  if (blob.truncated === true) return none("the version in git is over 2 MiB");
  if (blob.missing === true) return { head: "", missing: true, why: "" };
  return { head: typeof blob.content === "string" ? blob.content : "", missing: false, why: "" };
}

// editorDiffDecorations: the gutter, as deltaDecorations takes it, for lineDiff's `changes` over a buffer of `lines` lines. A
// removal is a marker on the line it sits in front of; one at the very end of the text sits one past the last line, so it is
// drawn on the last. `lane` is monaco.editor.OverviewRulerLane.Left.
function editorDiffDecorations(changes, lines, lane) {
  const out = [];
  for (const c of changes) {
    const from = Math.min(c.from, lines);
    const to = c.kind === "del" ? from : Math.min(c.to, lines);
    out.push({
      range: { startLineNumber: from, startColumn: 1, endLineNumber: to, endColumn: DG_END_COL },
      options: { isWholeLine: true, linesDecorationsClassName: DG_CLASS[c.kind], overviewRuler: { color: { id: DG_RULER[c.kind] }, position: lane } },
    });
  }
  return out;
}

// editorDiffKey: what an answer for `tab` would be for now (see the top), and whether git's status is known yet (without it the
// HEAD part of the key is not, and asking now would only be asking twice).
function editorDiffKey(tab) {
  const root = typeof ftRoot === "function" ? ftRoot() : "";
  const sid = tab.host && tab.host.sessionId ? tab.host.sessionId() : null;
  const scope = typeof dockScope === "function" ? dockScope(sid) : "head";
  const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(root) : null;
  const head = cur && typeof cur.status.head === "string" ? cur.status.head : "";
  const base = scope === "branch" && typeof gitBaseFor === "function" ? gitBaseFor(root) : null;
  const mergeBase = base && typeof base.mergeBase === "string" ? base.mergeBase : "";
  return {
    known: !!cur,
    scope,
    baseName: base && typeof base.baseName === "string" ? base.baseName : "",
    key: [tab.path, scope, head, mergeBase].join("\n"),
  };
}

// editorDiffFresh: does the record answer `k`? The same key, and not a failure old enough to be asked about again.
function editorDiffFresh(ch, k, now) {
  return !!ch && ch.key === k.key && (ch.head !== null || now - ch.at < DG_RETRY_MS);
}

// editorDiffWhy: why Changes cannot be shown for this tab, "" when it can (or when nothing has said it cannot yet).
function editorDiffWhy(tab) {
  if (tab.box) return DG_BOX_WHY;
  const ch = tab.changes;
  return ch && ch.head === null ? ch.why : "";
}

// --- the toggle ---------------------------------------------------------------------------------------------------

// editorDiffToggle builds the Code | Changes segmented control for the dock's bar. A press on Changes that cannot be honoured
// does nothing (the button is aria-disabled with its reason as the title, so the hover and the keyboard still reach it).
function editorDiffToggle(tab) {
  const el = document.createElement("div");
  el.className = "seg dk-seg";
  el.setAttribute("role", "group");
  el.setAttribute("aria-label", "View");
  const item = (label, run) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "seg-item";
    b.textContent = label;
    b.onclick = () => { if (b.getAttribute("aria-disabled") !== "true") run(); };
    return b;
  };
  const code = item("Code", () => { tab.showChanges(false); tab.focusReader(); });
  const changes = item("Changes", () => { tab.showChanges(true); });
  el.append(code, changes);
  tab.seg = { el, code, changes };
  return el;
}

// editorDiffPaintToggle: which of the two is on, and whether Changes can be had. Leaving Changes is always possible.
function editorDiffPaintToggle(tab) {
  const t = tab.seg;
  if (!t) return;
  const on = !!tab.diff;
  const why = on ? "" : editorDiffWhy(tab);
  const ch = tab.changes;
  t.code.setAttribute("aria-pressed", on ? "false" : "true");
  t.changes.setAttribute("aria-pressed", on ? "true" : "false");
  t.changes.setAttribute("aria-disabled", why ? "true" : "false");
  const title = why ? "Changes unavailable: " + why : "Changes " + editorDiffLabel(ch ? ch.scope : "head", ch ? ch.baseName : "");
  if (t.changes.title !== title) t.changes.title = title;
}

// --- asking ---------------------------------------------------------------------------------------------------------

// editorDiffPost: POST /api/git/show, answered with the reply object, or null for anything else (an error status, the
// network, a body that is not an object, a request that took longer than DG_FETCH_MS). Never throws.
async function editorDiffPost(body) {
  const ac = typeof AbortController === "function" ? new AbortController() : null;
  const timer = setTimeout(() => { if (ac) ac.abort(); }, DG_FETCH_MS);
  try {
    const res = await fetch("/api/git/show", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal: ac ? ac.signal : undefined,
    });
    if (!res.ok) return null;
    const data = await res.json();
    return data && typeof data === "object" ? data : null;
  } catch (_) {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

// editorDiffSync asks for what the tab on screen lacks: the base text for the key it has now. Idempotent and cheap when nothing
// moved (a key is a few lookups), so every place that can move it calls this. `force`: the user asked for Changes, so git's status
// being unknown yet is no reason to wait.
function editorDiffSync(force) {
  const tab = edTab;
  if (!tab || tab.disposed || !tab.attached || !tab.model || tab.inner) return;
  if (tab.box) return; // no host git for a file in a box
  const s = editorDiffOf(tab);
  if (s.flight) return;
  // A dock that is not on screen (closed, a page over it, the page hidden) asks nothing: the container run would be for nobody.
  if (!force && typeof editorSyncShown === "function" && !editorSyncShown()) return;
  const k = editorDiffKey(tab);
  if (!k.known && !force) return;
  if (editorDiffFresh(tab.changes, k, Date.now())) return;
  editorDiffAsk(tab, k);
}

async function editorDiffAsk(tab, k) {
  const s = editorDiffOf(tab);
  if (s.flight) return;
  s.flight = true;
  const blob = await editorDiffPost({ path: tab.path, scope: k.scope });
  s.flight = false;
  if (tab.disposed || !tab.model) return;
  if (editorDiffKey(tab).key !== k.key) { // about something the tab has moved on from: ask for what it has now
    if (edTab === tab) editorDiffSync(s.want); else s.want = false;
    return;
  }
  editorDiffLand(tab, k, blob);
}

// editorDiffLand keeps an answer as the tab's `changes`, draws the gutter for it and does what it was for: opens Changes if the
// user asked, brings an open Changes view up to date, or leaves it when the base text can no longer be had.
function editorDiffLand(tab, k, blob) {
  const s = editorDiffOf(tab);
  const b = editorDiffBase(blob);
  tab.changes = { key: k.key, scope: k.scope, baseName: k.baseName, head: b.head, missing: b.missing, why: b.why, lineChanges: [], capped: false, at: Date.now() };
  editorDiffRecompute(tab);
  const want = s.want;
  s.want = false;
  if (b.head === null) {
    if (want || tab.diff) showToast("Changes unavailable: " + b.why);
    editorDiffLeave(tab);
  } else if (want) {
    editorDiffEnter(tab);
  } else if (tab.diff) {
    editorDiffRefill(tab);
  }
  tab.paint();
}

// --- the gutter -----------------------------------------------------------------------------------------------------

// editorDiffRecompute: lineDiff of the base text against the buffer as it is now, and the gutter drawn from it.
function editorDiffRecompute(tab) {
  if (tab.disposed || !tab.model) return;
  const ch = tab.changes;
  if (ch) {
    const r = ch.head === null ? { changes: [], capped: false } : lineDiff(ch.head, tab.model.getValue());
    ch.lineChanges = r.changes;
    ch.capped = r.capped;
  }
  editorDiffPaintGutter(tab);
}

// editorDiffPaintGutter draws the changes on the shared editor, which holds them for as long as it shows this tab's model (another
// tab's claim of the editor takes them off: editorDiffAttach draws them again). A capped diff, or no base text, is no marks.
function editorDiffPaintGutter(tab) {
  const s = editorDiffOf(tab);
  if (!dockEditor || dockEditorOwner !== tab || !tab.model) return;
  const ch = tab.changes;
  const changes = ch && ch.head !== null && !ch.capped ? ch.lineChanges : [];
  const sig = JSON.stringify(changes);
  if (sig === s.last) return; // the same marks: nothing to write
  s.last = sig;
  if (!changes.length && !s.ids.length) return; // none to draw and none to take off
  s.ids = dockEditor.deltaDecorations(s.ids, editorDiffDecorations(changes, tab.model.getLineCount(), dockMonaco.editor.OverviewRulerLane.Left));
}

// editorDiffSoon: the buffer changed. The gutter follows 300 ms after the last edit, never on each keystroke, and the band a
// link put on a line goes with the first change of the text (as the read view's did).
function editorDiffSoon(tab) {
  const s = editorDiffOf(tab);
  editorDiffClearTarget(tab);
  const ch = tab.changes;
  if (!ch || ch.head === null) return; // nothing to compare with (git is not here): typing costs nothing; a base text that lands diffs the buffer as it is then
  if (s.timer) clearTimeout(s.timer);
  s.timer = setTimeout(() => { s.timer = null; editorDiffRecompute(tab); }, DG_DEBOUNCE_MS);
}

// editorDiffAttach: the tab is on screen with its model (EditorTab.show). The gutter is drawn from what it holds, the base text
// is asked for if the tab lacks it, and edits are followed until it is hidden.
function editorDiffAttach(tab) {
  if (tab.disposed || !tab.model || tab.inner) return;
  const s = editorDiffOf(tab);
  edTab = tab;
  s.last = ""; // another tab's claim of the editor took the marks off this model: they are drawn again (ids that are gone are ignored)
  if (!s.sub) s.sub = tab.model.onDidChangeContent(() => editorDiffSoon(tab));
  editorDiffRecompute(tab);
  editorDiffSync();
}

// editorDiffDetach: the tab is not on screen any more. Nothing runs for it until it is shown again.
function editorDiffDetach(tab) {
  if (edTab === tab) edTab = null;
  const s = tab.dg;
  if (!s) return;
  if (s.timer) { clearTimeout(s.timer); s.timer = null; }
  if (s.sub) { s.sub.dispose(); s.sub = null; }
}

// --- the line a link asked for ----------------------------------------------------------------------------------------

// editorDiffTarget puts the band on `line`, the one a path:line link asked for (the read view's .vl-target). It is a decoration of
// the model, not of one editor, so Code and Changes both show it.
function editorDiffTarget(tab, line) {
  const s = editorDiffOf(tab);
  s.targetLine = line;
  s.targetIds = tab.model.deltaDecorations(s.targetIds, [{
    range: { startLineNumber: line, startColumn: 1, endLineNumber: line, endColumn: DG_END_COL },
    options: { isWholeLine: true, className: "dk-target", marginClassName: "dk-target-m" },
  }]);
}

function editorDiffClearTarget(tab) {
  const s = editorDiffOf(tab);
  if (s.targetLine === null) return;
  s.targetLine = null;
  if (tab.model) s.targetIds = tab.model.deltaDecorations(s.targetIds, []);
}

// --- the Changes view -----------------------------------------------------------------------------------------------

// editorDiffSet: showChanges(on). Turning it on opens Changes now when the base text is in, and otherwise asks for it and opens
// when it lands (or says why not, in a toast); turning it off is leaving. Returns whether Changes is up or on its way.
function editorDiffSet(tab, on) {
  if (tab.disposed || tab.inner) return false;
  const s = editorDiffOf(tab);
  if (!on) { s.want = false; editorDiffLeave(tab); return false; }
  if (tab.diff) return true;
  if (!tab.model) { tab.pendingChanges = true; return true; } // still reading: load() opens it when the model is there
  const why = editorDiffWhy(tab);
  const k = editorDiffKey(tab);
  const ch = tab.changes;
  if (why && (tab.box || editorDiffFresh(ch, k, Date.now()))) { showToast("Changes unavailable: " + why); return false; }
  if (ch && ch.head !== null && ch.key === k.key) { editorDiffEnter(tab); return true; }
  s.want = true;
  if (edTab === tab) editorDiffSync(true);
  else editorDiffAsk(tab, k); // not on screen (a tab asked for from outside): ask for it all the same
  return true;
}

// editorDiffEnter swaps the tab's body for the inline diff of the base text and the live buffer. The Code editor keeps its model
// and its place (stashView), for the way back.
function editorDiffEnter(tab) {
  const ch = tab.changes;
  if (tab.diff || tab.disposed || !tab.model || !ch || ch.head === null) return;
  const s = editorDiffOf(tab);
  tab.stashView();
  const original = dockMonaco.editor.createModel(ch.head, monacoLang(tab.path));
  const label = document.createElement("span");
  label.textContent = editorDiffLabel(ch.scope, ch.baseName);
  const head = document.createElement("div");
  head.className = "dk-cmp-head";
  head.append(label);
  const body = document.createElement("div");
  body.className = "dk-cmp-body";
  const wrap = document.createElement("div");
  wrap.className = "dk-ed-cmp";
  wrap.append(head, body);
  // ignoreTrimWhitespace off: a review view shows a whitespace-only change, as git diff does and the gutter does.
  const diff = dockMonaco.editor.createDiffEditor(body, Object.assign({
    theme: MONACO_THEME,
    readOnly: true,
    readOnlyMessage: { value: DG_RO_MSG },
    originalEditable: false,
    renderSideBySide: false,
    ignoreTrimWhitespace: false,
    automaticLayout: true,
    minimap: { enabled: false },
    scrollBeyondLastLine: false,
    fixedOverflowWidgets: true,
  }, editorOptions()));
  const modified = diff.getModifiedEditor();
  modified.onDidChangeCursorPosition(() => tab.paintStatus());
  // Open where a link asked to be, else at the first change: that is what the view is for.
  const target = s.targetLine;
  const computed = diff.onDidUpdateDiff(() => {
    computed.dispose();
    if (target !== null) modified.revealLineInCenter(target, dockMonaco.editor.ScrollType.Immediate);
    else diff.revealFirstDiff();
  });
  diff.setModel({ original, modified: tab.model });
  tab.diff = { diff, original, wrap, label };
  tab.bodyEl.replaceChildren(wrap);
  diff.layout();
  if (tab.attached) tab.takeWantedFocus(modified);
  tab.paint();
}

// editorDiffRefill: the base text of the open Changes view moved (a commit, another scope): the view shows the new one.
function editorDiffRefill(tab) {
  const ch = tab.changes;
  const d = tab.diff;
  if (!d || !ch || ch.head === null) return;
  if (d.original.getValue() !== ch.head) d.original.setValue(ch.head);
  const text = editorDiffLabel(ch.scope, ch.baseName);
  if (d.label.textContent !== text) d.label.textContent = text;
}

// editorDiffDrop disposes the Changes view: the diff editor and the model of the base text. The tab's own model is not its to dispose.
function editorDiffDrop(tab) {
  const d = tab.diff;
  if (!d) return false;
  tab.diff = null;
  d.diff.setModel(null);
  d.diff.dispose();
  d.original.dispose();
  return true;
}

// editorDiffLeave goes back to Code: the view is disposed and the shared editor takes the body again, at the place it was left.
function editorDiffLeave(tab) {
  if (!editorDiffDrop(tab)) return;
  tab.bodyEl.replaceChildren();
  if (tab.attached && tab.model) tab.show(); // claims the editor, restores its view and draws the gutter again
  else tab.paint();
}

// editorDiffShow: a tab that was left in Changes is on screen again. Its body already holds the view; it only needs to be put
// back in the document, in the preferences of now, and measured (it was laid out at no size while the tab was hidden).
function editorDiffShow(tab) {
  const d = tab.diff;
  tab.bodyEl.replaceChildren(d.wrap);
  d.diff.updateOptions(editorOptions());
  d.diff.layout();
}

// editorDiffApplyPrefs: the font size or the wrap changed (editorApplyPrefs): the Changes view on screen follows.
function editorDiffApplyPrefs() {
  if (edTab && edTab.diff) edTab.diff.diff.updateOptions(editorOptions());
}

// editorDiffDispose: the tab is closed. Nothing it made stays: the view and the base text's model, the timer and the subscription.
// Before the tab's model goes: the diff editor holds it.
function editorDiffDispose(tab) {
  editorDiffDetach(tab);
  const s = tab.dg;
  if (s) s.want = false;
  editorDiffDrop(tab);
  tab.changes = null;
}
