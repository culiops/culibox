"use strict";

// Changes vs HEAD in the Files dock's editor (spec "Editor (P2: Monaco)": "Bar actions", "Gutter", "HEAD text"). An EditorTab asks the
// server for its file as git has it at the base of the scope the Changes section shows (HEAD, or the merge base in Branch scope) and
// uses that text twice:
//   the gutter   lineDiff (linediff.js) of it against the live buffer, drawn beside the line numbers (dk-g-add, dk-g-mod, dk-g-del) and
//                in the overview ruler, from 300 ms after the last edit. An unsaved edit shows, as in VS Code;
//   Changes      the Code | Changes toggle in the dock's bar swaps the tab's body for Monaco's inline diff: the base text on the removed
//                side, the tab's own model on the new side. It is a review view (founder, 2026-11-17): read-only, editing is in Code,
//                typing in it does nothing (which also keeps single keys free for review later). For a markdown file the toggle is
//                Preview | Source | Changes (Source is Code's place; Preview is editormd.js's).
//
// The base text. POST /api/git/show {path, scope} answers {state: "ok" | "notrepo" | "unavailable", reason, missing, content, truncated}.
// It is asked only for a tab on screen, with git's status known and the dock visible, and kept on the tab as `tab.changes`:
//   {key, scope, baseName, head, missing, why, retry, lineChanges, capped, at}
//   key          what the answer is for: the path, the scope, the HEAD commit the tree's status names and (Branch) the merge base. A
//                status that lands with the same key asks nothing, so an edit of the file, which moves no commit, costs no request;
//                nor does a keystroke, ever (the gutter diffs the text already held);
//   head         the base text; "" for a file the base does not have (a new file: every line is added); null when it could not be read;
//   why          when head is null, the sentence the toggle's title gives after "Changes unavailable:";
//   retry        when head is null: the failure may pass (git unavailable, a request that failed), so it is asked again after
//                DG_RETRY_MS. A definitive answer (not a repository, a base text over the cap) is not asked again until the key moves;
//   lineChanges  lineDiff's changes of head against the buffer (P3 reads them); [] when capped or head is null;
//   capped       lineDiff declined (too big, or too different): no marks, and no cost per keystroke;
//   at           when the answer landed.
// A box tab has no host git: no request, the toggle says so. An image or a binary file has no toggle at all (the EditorTab's inner
// viewer owns the bar).
//
// Everything an answer touches is decided when it arrives: an answer for a tab that was disposed, or for a key the tab has moved
// on from (the scope was switched, HEAD moved, the file was re-pointed), is dropped and the newer key is asked for.
//
// What the editor lets go of. The dock has ONE editor instance, and at most one diff editor at any time: the one of the tab on screen
// while it is in Changes, or its compare view of a conflict (editorsync.js), never both: entering one closes the other. The diff editor and the model of the base text are disposed on leaving Changes, when the tab is hidden
// (it is put back from the base text it holds, with no request, when the tab is shown again: `resume`) and on EditorTab.dispose().
// The gutter's timer and model subscription exist only while the tab is on screen.
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
// The toggle's icons, as svgIcon shapes (14 px grid): a page, angle brackets, a plus over a minus.
const DG_ICON_PREVIEW = [["path", { d: "M3.5 1.5h5l2.5 2.5v8.5h-7.5z" }], ["path", { d: "M5.3 7h3.4M5.3 9.5h3.4" }]];
const DG_ICON_CODE = [["path", { d: "M5 4L2 7l3 3M9 4l3 3-3 3" }]];
const DG_ICON_CHANGES = [["path", { d: "M7 2.5v5M4.5 5h5M4.5 11h5" }]];
const DG_BOX_WHY = "a file in a session's box has no git history";
const DG_RO_MSG = "Changes is a review view. Switch to Code to edit.";

let edTab = null; // the tab on screen with its model: the one a status or a scope change is for

// editorDiffOf: what this file keeps on a tab: the request slot, the debounce, the gutter's decorations (ids, and the text of
// the changes they draw), the line a link asked for, whether the user asked for Changes before the base text was in (`want`), whether
// the tab is to open in Changes when it is next on screen (`resume`) and where its Changes view was scrolled to (`diffTop`).
function editorDiffOf(tab) {
  if (!tab.dg) tab.dg = { flight: false, want: false, resume: false, diffTop: null, timer: null, sub: null, ids: [], last: "", targetIds: [], targetLine: null };
  return tab.dg;
}

// --- pure pieces -------------------------------------------------------------------------------------------------

// editorDiffLabel: what the Changes view says it compares with.
function editorDiffLabel(scope, baseName) {
  if (scope !== "branch") return "vs HEAD";
  return baseName ? "vs " + baseName + " (merge base)" : "vs the merge base";
}

// editorDiffPaintLabel writes the Changes view's header: "vs HEAD", or "vs <base> (merge base)" with the base's name in a span of its
// own, isolated for bidirectional text like every git string the dock draws (a branch name may carry format characters).
function editorDiffPaintLabel(el, scope, baseName) {
  const text = editorDiffLabel(scope, baseName);
  if (el.dataset.text === text) return;
  el.dataset.text = text;
  if (scope === "branch" && baseName) {
    const base = document.createElement("span");
    base.className = "dk-cmp-base";
    base.textContent = baseName;
    el.replaceChildren("vs ", base, " (merge base)");
  } else {
    el.replaceChildren(text);
  }
}

// editorDiffBase turns the server's answer (null: the request failed) into {head, missing, why, retry}: the text to compare with, or
// head null, the reason it cannot be had and whether the failure may pass (retry). A file the base does not have is an empty text, so
// every line of it reads as added. A byte order mark at the start of the text is dropped: Monaco keeps none in the buffer, so a
// file that has one would otherwise read as changed on line 1.
function editorDiffBase(blob) {
  const none = (why, retry) => ({ head: null, missing: false, why, retry });
  if (!blob || typeof blob !== "object") return none("the server did not answer", true);
  if (blob.state === "notrepo") return none("this file is not in a git repository", false);
  if (blob.state !== "ok") return none(typeof blob.reason === "string" && blob.reason ? blob.reason : "git is not available here", true);
  if (blob.truncated === true) return none("the version in git is over 2 MiB", false);
  if (blob.missing === true) return { head: "", missing: true, why: "", retry: false };
  const text = typeof blob.content === "string" ? blob.content : "";
  return { head: text.charCodeAt(0) === 0xfeff ? text.slice(1) : text, missing: false, why: "", retry: false };
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

// editorDiffFresh: does the record answer `k`? The same key, and not a failure that may pass and is old enough to be asked about
// again (a definitive answer stands until the key moves).
function editorDiffFresh(ch, k, now) {
  return !!ch && ch.key === k.key && (ch.head !== null || !ch.retry || now - ch.at < DG_RETRY_MS);
}

// editorDiffWhy: why Changes cannot be shown for this tab, "" when it can (or when nothing has said it cannot yet).
function editorDiffWhy(tab) {
  if (tab.box) return DG_BOX_WHY;
  const ch = tab.changes;
  return ch && ch.head === null ? ch.why : "";
}

// --- the toggle ---------------------------------------------------------------------------------------------------

// editorDiffToggle builds the Code | Changes segmented control for the dock's bar, with Preview in front of them for a markdown file
// (paint hides it for any other, and calls Code "Source" for a markdown file: the one control, not a second one). A press on Changes
// that cannot be honoured does nothing (the button is aria-disabled with its reason as the title, so the hover and the keyboard
// still reach it).
function editorDiffToggle(tab) {
  const el = document.createElement("div");
  el.className = "seg dk-seg";
  el.setAttribute("role", "group");
  el.setAttribute("aria-label", "View");
  // Each item has an icon and its word. The bar shows one of them: the word while the dock is wide, the icon when it is not (a size
  // query in desktop.css), so the tab names keep their room. The word stays in the page for screen readers either way.
  const item = (label, icon, run) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "seg-item";
    const ic = svgIcon(icon, { icon: "view", size: "14" });
    ic.setAttribute("class", "dk-seg-ic");
    const t = document.createElement("span");
    t.className = "dk-seg-t";
    t.textContent = label;
    b.append(ic, t);
    b.title = label;
    b.onclick = () => { if (b.getAttribute("aria-disabled") !== "true") run(); };
    return b;
  };
  const preview = item("Preview", DG_ICON_PREVIEW, () => { editorMdPick(tab, "preview"); tab.focusReader(); });
  const code = item("Code", DG_ICON_CODE, () => {
    if (editorMdIs(tab)) editorMdPick(tab, "source"); else tab.showChanges(false);
    tab.focusReader();
  });
  const changes = item("Changes", DG_ICON_CHANGES, () => { tab.showChanges(true); });
  preview.hidden = true; // until paint knows the file is markdown
  el.append(preview, code, changes);
  tab.seg = { el, preview, code, changes, codeText: code.querySelector(".dk-seg-t") };
  return el;
}

// editorDiffPaintToggle: which of them is on, and whether Changes can be had. Leaving Changes is always possible.
function editorDiffPaintToggle(tab) {
  const t = tab.seg;
  if (!t) return;
  const on = !!tab.diff;
  const why = on ? "" : editorDiffWhy(tab);
  const ch = tab.changes;
  const md = editorMdIs(tab);
  const prev = md && !on && editorMdView(tab) === "preview";
  t.preview.hidden = !md;
  const label = md ? "Source" : "Code";
  if (t.codeText.textContent !== label) t.codeText.textContent = label;
  if (t.code.title !== label) t.code.title = label;
  t.preview.setAttribute("aria-pressed", prev ? "true" : "false");
  t.code.setAttribute("aria-pressed", on || prev ? "false" : "true");
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
  tab.changes = { key: k.key, scope: k.scope, baseName: k.baseName, head: b.head, missing: b.missing, why: b.why, retry: b.retry, lineChanges: [], capped: false, at: Date.now() };
  editorDiffRecompute(tab);
  const want = s.want;
  s.want = false;
  if (b.head === null) {
    if (want || tab.diff || s.resume) showToast("Changes unavailable: " + b.why);
    s.resume = false;
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

// editorDiffDetach: the tab is not on screen any more. Nothing runs for it until it is shown again, and it keeps no diff editor
// (the dock has one at a time): one that was in Changes is let go, with the place it was read at, and comes back in Changes
// (editorDiffResume) from the base text the tab holds.
function editorDiffDetach(tab) {
  if (edTab === tab) edTab = null;
  const s = tab.dg;
  if (!s) return;
  if (s.timer) { clearTimeout(s.timer); s.timer = null; }
  if (s.sub) { s.sub.dispose(); s.sub = null; }
  if (tab.diff) {
    s.diffTop = tab.diff.diff.getModifiedEditor().getScrollTop();
    s.resume = true;
    editorDiffDrop(tab);
    tab.bodyEl.replaceChildren();
  }
}

// editorDiffResume: the tab is on screen. A tab that was in Changes when it left, or was asked for Changes while it was not on
// screen, opens it again, from the base text it holds: no request (a key that moved meanwhile is asked for by the attach after, and
// the open view is refilled). False when the tab was not in Changes, or the base text is gone: the tab shows Code.
function editorDiffResume(tab) {
  const s = editorDiffOf(tab);
  if (!s.resume) return false;
  s.resume = false;
  if (!tab.changes || tab.changes.head === null) return false;
  editorDiffEnter(tab);
  return !!tab.diff;
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
  if (!on) { s.want = false; s.resume = false; editorDiffLeave(tab); return false; }
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
// and its place (stashView), for the way back. Only the tab on screen has a diff editor: for any other tab this only notes that it
// is to open in Changes when it is shown (the dock has one diff editor at a time).
function editorDiffEnter(tab) {
  const ch = tab.changes;
  if (tab.diff || tab.disposed || !tab.model || !ch || ch.head === null) return;
  const s = editorDiffOf(tab);
  if (!tab.attached) { s.resume = true; return; }
  editorSyncCloseCompare(tab); // the compare view of a conflict, if it is open, goes: one diff editor
  editorMdDrop(tab);           // so does a markdown Preview: the body is the diff's
  tab.stashView();
  const original = dockMonaco.editor.createModel(ch.head, monacoLang(tab.path));
  const label = document.createElement("span");
  editorDiffPaintLabel(label, ch.scope, ch.baseName);
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
    stickyScroll: { enabled: false }, // as Code has it: the enclosing scope's rows would be drawn over the first changed line
    scrollBeyondLastLine: false,
    fixedOverflowWidgets: true,
  }, editorOptions()));
  const modified = diff.getModifiedEditor();
  modified.onDidChangeCursorPosition(() => tab.paintStatus());
  // Open where the view was left (a tab that comes back), else where a link asked to be, else at the first change: that is what
  // the view is for.
  const target = s.targetLine;
  const top = s.diffTop;
  s.diffTop = null;
  const computed = diff.onDidUpdateDiff(() => {
    computed.dispose();
    if (top !== null) modified.setScrollTop(top, dockMonaco.editor.ScrollType.Immediate);
    else if (target !== null) modified.revealLineInCenter(target, dockMonaco.editor.ScrollType.Immediate);
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
  editorDiffPaintLabel(d.label, ch.scope, ch.baseName);
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

// editorDiffApplyPrefs: the font size or the wrap changed (editorApplyPrefs): the Changes view on screen follows.
function editorDiffApplyPrefs() {
  if (edTab && edTab.diff) edTab.diff.diff.updateOptions(editorOptions());
}

// editorDiffDispose: the tab is closed. Nothing it made stays: the view and the base text's model, the timer and the subscription.
// Before the tab's model goes: the diff editor holds it.
function editorDiffDispose(tab) {
  editorDiffDetach(tab);
  const s = tab.dg;
  if (s) { s.want = false; s.resume = false; }
  editorDiffDrop(tab);
  tab.changes = null;
}
