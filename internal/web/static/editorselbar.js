"use strict";

// The selection bar of the Files dock's editor (spec "Editor (P2: Monaco)" → "Selection actions", mockup B1), and the one place that
// types a file reference at a session's prompt.
//
// Select lines in the editor and a small bar floats above the start of the selection, with
//   Insert at prompt   types `<path>:a-b` (`<path>:a` for one line) and a space at the dock's session's prompt: no Enter, ever;
//   Copy <name>:a-b    copies the same reference.
// A click on a line number selects that line (Monaco's selectOnLineNumbers, set in editorEnsure), so the same bar offers the
// reference of one line: it replaced the read view's line-number menu. P3 adds its Add note button through selBarAddAction.
//
// The bar is one Monaco content widget of the dock's one editor. It shows only while the editor has the keyboard and the selection is
// not empty, and Esc hides it until the selection changes. A press on it does not take the keyboard from the editor (the editor
// losing focus is what hides the bar, and the selection would be gone with it). It is desktop only because the dock is.
//
// The reference is the file's ABSOLUTE path, as today's line-number insert and copy (viewer-tab.js insertPath, copyPath) and the
// tree's insert type it: a path relative to the tree's root is wrong the moment the root and the session's cwd differ, and a manual
// root pick survives until another tab is focused (filetree.js insertPathAtPrompt). What it points at is the buffer as it is on
// screen: lines of an unsaved edit are not the lines of the file the agent reads.
//
// Every insert asks insertRefused (insertdecision.js) before it types, and types only through insertTextAtPrompt, so the session
// waiting on the user, or a terminal that is not on screen, refuses with a toast; insert-callers.test.js pins both. In Full mode
// the terminal is covered, so the bar's Insert, like the Full-mode agent strip's (dockagent.js), leaves Full mode first and asks the
// decision after (editorInsertLeavingFull); both read editorRefFor before leaving.

let esbEd = null;         // the editor the widget belongs to, once installed
let esbEl = null;         // the bar's element (the widget's DOM node)
let esbWidget = null;     // the Monaco content widget
let esbShown = false;     // the widget is added to the editor
let esbTab = null;        // the tab the bar is for
let esbLines = null;      // {a, b}: the lines it is for
let esbKey = "";          // the selection it is drawn for: path and range
let esbStart = null;      // {lineNumber, column}: where it floats (the start of the selection)
let esbDismissed = "";    // the selection an Esc hid the bar for
let esbCopy = null;       // [the Copy button, the span of its label]: the label names the lines
let esbBuilt = null;      // the buttons the bar was last built with, to build again only when an action was added
const esbActions = [];    // selBarAddAction's, in the order they came

// selBarAddAction({key, label, icon, run(tab, range)}) appends a button to the selection bar (P3's Add note). `key` names it (a second
// action with the same key replaces the first), `icon` is a kitmenu.js menuIconSvg kind (optional), and run is called with the tab
// and {a, b}, the lines selected, when the button is pressed. A bar that is up is drawn again at once.
function selBarAddAction(action) {
  if (!action || typeof action.key !== "string" || action.key === "" || typeof action.run !== "function") return false;
  const at = esbActions.findIndex((x) => x.key === action.key);
  const entry = { key: action.key, label: String(action.label || action.key), icon: typeof action.icon === "string" ? action.icon : "", run: action.run };
  if (at >= 0) esbActions[at] = entry; else esbActions.push(entry);
  esbBuilt = null;
  if (esbShown && esbTab && esbLines) editorSelBarPaint();
  return true;
}

// editorTabSelection: the lines a tab has selected, {a, b}, or null: in the Changes view the new side's, else the dock's editor's
// when this tab has it. A tab that is not on screen has no live selection.
function editorTabSelection(tab) {
  if (!tab || tab.disposed || !tab.model || tab.inner) return null;
  let ed = null;
  if (tab.diff) ed = tab.diff.diff.getModifiedEditor();
  else if (typeof dockEditor !== "undefined" && dockEditorOwner === tab) ed = dockEditor;
  return ed ? selectionLines(ed.getSelection()) : null;
}

// editorRefFor: what Insert at prompt would type for a tab, captured as plain data (a tab can go while the caller waits, the forced
// Full mode's dock closes before the strip's insert asks): the reference of `lines` ({a, b}; the tab's own selection when not
// given), or the file's path when nothing is selected. null for no tab or a tab with no path.
function editorRefFor(tab, lines) {
  if (!tab || typeof tab.path !== "string" || tab.path === "") return null;
  const sel = lines || editorTabSelection(tab);
  const sid = tab.host && typeof tab.host.sessionId === "function" ? tab.host.sessionId() : null;
  return { ref: sel ? rangeRef(tab.path, sel.a, sel.b) : tab.path, box: tab.box || "", sid, ranged: !!sel };
}

// editorInsertRef types `want.ref` (editorRefFor's) and a space at the prompt of the dock's session, never a newline: the
// user presses Enter. The box rule first (a file in one session's box means nothing in another's), then insertRefused for the
// target's own state: it toasts why and answers true when the session is waiting on the user or its terminal is not on screen,
// and then nothing is typed and no focus moves. Typed text is quoted for a shell when it needs it, like the tree's insert.
// On success the session's terminal takes the keyboard, as it does after the tree's and the viewer's inserts. Answers whether it typed.
function editorInsertRef(want) {
  if (!want || typeof want.ref !== "string") return false;
  const term = want.sid ? terminalTabFor(want.sid) : null;
  const target = insertTargetFor({ box: want.box }, term);
  if (!target.ok) { showToast(target.title, { error: true }); return false; }
  if (insertRefused(target.term)) return false;
  const res = insertTextAtPrompt(target.term, shellQuote(want.ref) + " ");
  if (res.ok) { target.term.grabFocus(); return true; }
  showToast(insertFailText(res.reason), { error: true });
  return false;
}

// editorInsertLeavingFull: Insert at prompt from where the terminal may be covered, the selection bar's and the strip's. Full mode
// covers the terminal, and a covered terminal is a state the insert decision refuses (#337); no exception is made for it (founder,
// 2026-11-18), so Full mode is left first, by the action of Show terminal (dockShowTerminal: Ctrl+`; a forced Full mode closes the
// dock), and the decision is asked on the prompt that is on screen then, from `then`, which dockShowTerminal runs once Full mode is
// really over (for the close, after applyDrawer applied it: never after a Cancel of its discard ask). `want` is editorRefFor's, read
// before leaving, because a forced Full closes the dock and its editor goes with it. Outside Full mode nothing is left, and the
// insert is asked at once.
function editorInsertLeavingFull(want) {
  const run = () => { editorInsertRef(want); };
  if (!dockShowTerminal(run)) run();
}

// --- the widget -------------------------------------------------------------------------------------------------------------

// esbButton: [the button, the span of its label], in the kit's look: the primary or a ghost. The icon is decoration (menuIconSvg draws
// it aria-hidden).
function esbButton(primary, label, icon, run) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "btn btn-sm dk-selbar-b " + (primary ? "btn-primary" : "btn-ghost");
  const svg = icon && typeof menuIconSvg === "function" ? menuIconSvg(icon) : null;
  if (svg) b.append(svg);
  const t = document.createElement("span");
  t.textContent = label;
  b.append(t);
  b.onclick = run;
  return [b, t];
}

// editorSelBarBuild makes the buttons: Insert at prompt, Copy, then P3's. Copy's label is the lines', set on every paint.
function editorSelBarBuild() {
  const [insert] = esbButton(true, "Insert at prompt", "insert", () => {
    if (esbTab && esbLines) editorInsertLeavingFull(editorRefFor(esbTab, esbLines));
  });
  esbCopy = esbButton(false, "Copy", "copy", () => {
    if (!esbTab || !esbLines) return;
    const tab = esbTab;
    // copyPath (filetree.js) refuses a path with a control character, says what it copied and hands the keyboard back.
    copyPath(rangeRef(tab.path, esbLines.a, esbLines.b), () => tab.focusReader());
  });
  const extras = esbActions.map((a) => esbButton(false, a.label, a.icon, () => { if (esbTab && esbLines) a.run(esbTab, { a: esbLines.a, b: esbLines.b }); })[0]);
  esbEl.replaceChildren(insert, esbCopy[0], ...extras);
  esbBuilt = esbActions.length;
}

// editorSelBarPaint names the lines in Copy's label ("Copy fsapi.go:168-169", as mockup B1) and its title carries what is copied.
function editorSelBarPaint() {
  if (esbBuilt !== esbActions.length) editorSelBarBuild();
  const full = rangeRef(esbTab.path, esbLines.a, esbLines.b);
  const label = "Copy " + rangeRef(esbTab.path.split("/").pop() || esbTab.path, esbLines.a, esbLines.b);
  const [button, span] = esbCopy;
  if (span.textContent !== label) span.textContent = label;
  if (button.title !== "Copy " + full) button.title = "Copy " + full;
}

function editorSelBarHide() {
  if (!esbShown) return;
  esbShown = false;
  if (esbEd) esbEd.removeContentWidget(esbWidget);
}

// editorSelBarUpdate shows, moves or hides the bar to match the editor: the tab that has it is on screen in Code, the editor has the
// keyboard, and the selection is not empty (and is not one an Esc hid the bar for). Called on every change of selection or focus.
function editorSelBarUpdate() {
  if (!esbEd) return;
  const tab = typeof dockEditorOwner !== "undefined" ? dockEditorOwner : null;
  // The editor holds this tab's model (setModel runs before the owner is set, so a selection of another tab's text is never ours).
  const live = !!(tab && tab.attached && !tab.diff && !tab.disposed && tab.model && !tab.inner && esbEd.getModel() === tab.model);
  const sel = live ? esbEd.getSelection() : null;
  const lines = sel ? selectionLines(sel) : null;
  const focused = live && esbEd.hasTextFocus();
  if (!lines) {
    if (focused) esbDismissed = ""; // the selection is gone: the next one shows its bar
    editorSelBarHide();
    return;
  }
  const key = [tab.path, sel.startLineNumber, sel.startColumn, sel.endLineNumber, sel.endColumn].join(":");
  if (!focused || key === esbDismissed) { editorSelBarHide(); return; }
  if (esbShown && esbTab === tab && esbKey === key) return; // nothing moved (EditorTab.paint asks on every repaint)
  esbKey = key;
  esbTab = tab;
  esbLines = lines;
  esbStart = { lineNumber: sel.startLineNumber, column: sel.startColumn };
  editorSelBarPaint();
  if (esbShown) esbEd.layoutContentWidget(esbWidget);
  else { esbEd.addContentWidget(esbWidget); esbShown = true; }
}

// editorSelBarInstall puts the bar on the dock's editor, once (editorEnsure).
function editorSelBarInstall(ed, monaco) {
  esbEd = ed;
  esbEl = document.createElement("div");
  esbEl.className = "dk-selbar";
  esbEl.setAttribute("role", "toolbar");
  esbEl.setAttribute("aria-label", "Selection actions");
  esbEl.addEventListener("mousedown", (e) => e.preventDefault());
  const pref = monaco.editor.ContentWidgetPositionPreference;
  esbWidget = {
    getId: () => "dk.selbar",
    getDomNode: () => esbEl,
    getPosition: () => ({ position: esbStart, preference: [pref.ABOVE, pref.BELOW] }),
    allowEditorOverflow: true,
  };
  ed.onDidChangeCursorSelection(() => editorSelBarUpdate());
  ed.onDidFocusEditorText(() => editorSelBarUpdate());
  ed.onDidBlurEditorText(() => editorSelBarUpdate());
  ed.onDidChangeModel(() => editorSelBarUpdate());
  // Esc hides the bar until the selection changes. Monaco still gets the key (the find widget closes on it too).
  ed.onKeyDown((e) => {
    if (!esbShown || e.keyCode !== monaco.KeyCode.Escape) return;
    esbDismissed = esbKey;
    editorSelBarHide();
  });
}
