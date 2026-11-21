"use strict";

// EditorTab: one file of the desktop Files dock, on Monaco. It is the dock's tab body (filesdrawer.js
// dockMount) and speaks the surface the dock already uses on a viewer: el, path, box, name, kind, dirty(),
// onUpdate, onAttach / onHide / onShow, dispose(), revealLine(), focusReader(), savedScroll's `scroll`.
//
// The editor is always editable: no Edit button, a manual save (Mod+S, the registry's editor.save) guarded by
// base_version, and a dirty dot in the tab strip while the buffer differs from what was last saved. A file in a
// session's box, or one the server marks readonly, opens locked. A file that is not text (an image, a video, a PDF, a binary
// file, a text file too large to read) gets a DockFileView (dockfileview.js) mounted inside the tab, which gets the lifecycle
// calls (this.inner): the same header (Follow, ⋯, Full), a view and a status bar of its own.
//
// There is ONE Monaco editor for the whole dock (dockEditor). A tab owns a model (monaco.editor.createModel) and
// the view state of the editor over it (cursor, selection, scroll); showing a tab puts its model into the shared
// editor and restores that state, hiding it saves the state. dispose() disposes the model: that is the only
// place an edit is lost, and the dock has asked before it lets one go (dockLeaveNeedsAsk). A dirty tab the dock
// only detaches (the crossing into mobile) keeps its model.
//
// The file can change on disk under the buffer (the agent writes it, or a save is refused with a 409): editorsync.js
// polls the shown tab, reloads a clean buffer in place (replaceFromDisk) and draws the amber conflict bar over a dirty one.
//
// The tab's body is the editor (Code), Monaco's diff against git (Changes, editordiff.js, a read-only review view) or, for a
// markdown file, its rendered Preview (editormd.js, the view such a tab opens in); the gutter marks of the lines that differ from git
// are drawn on the editor.
//
// Monaco itself is loaded lazily by loadMonaco (monacoload.js) when the first tab needs it, never at page boot.
// Later tasks build on three things this file produces: the class, `dockEditor`, and `tab.model`.

let dockMonaco = null;      // window.monaco, once the first tab has loaded it
let dockEditor = null;      // the one shared editor, created when the first tab with a model is shown
let dockEditorEl = null;    // its element: moved into the shown tab's .dk-ed-body
let dockEditorOwner = null; // the EditorTab whose model is in the editor now, or null

// How long "Saved" stays in the status bar.
const EDITOR_SAVED_MS = 2000;
const EDITOR_FONT = "ui-monospace, Menlo, monospace"; // the read view's own (app.css .viewer-pre), so a file does not change face
const EDITOR_LINE = 1.55;                             // and its line height (the read view's, in font sizes)

// editorOptions: what the editor reads from the user's preferences, as updateOptions takes it: the code font size
// (fontPrefs surface "code") and wrap lines (viewerPrefs), both shared with the read view and the phone.
function editorOptions() {
  const px = fontPrefs.get("code");
  return {
    fontSize: px,
    lineHeight: Math.round(px * EDITOR_LINE),
    fontFamily: EDITOR_FONT,
    wordWrap: viewerPrefs.getWrap() ? "on" : "off",
  };
}

// editorApplyPrefs brings the shared editor, if there is one, in line with the preferences (fontPrefs.set and
// reset call it, as does the wrap toggle through EditorTab.applyWrap).
function editorApplyPrefs() {
  if (dockEditor) dockEditor.updateOptions(editorOptions());
  editorDiffApplyPrefs(); // the Changes view on screen, if any
}

// editorBind runs `run` on the registry's chords for `id` while the editor has focus. The registry owns the chords
// (editor.save, editor.wrap are scope "editor": the global dispatcher never sees them), Monaco only gets them as
// commands; a chord Monaco has no name for is skipped.
function editorBind(ed, id, run) {
  const entry = scEntry(id);
  for (const chord of entry ? activeChords(entry, IS_MAC) : []) {
    const keys = monacoKeyFor(chord, dockMonaco, IS_MAC);
    if (keys) ed.addCommand(keys, run);
  }
}

// editorBindChangeKeys binds F7 / Shift+F7 (the Changes view's next and previous change, editorreview.js), once. Monaco's addCommand
// makes a keybinding of the page, not of the editor it is called on (measured in the vendored 0.57.0: addDynamicKeybinding on the
// shared service, never disposed, outranking the built-in F7 "next difference" of a diff editor), so the same call serves the Code
// editor and the Changes view's, and a second one would only stack another. It is called with whichever editor exists first.
let editorChangeKeys = false;
function editorBindChangeKeys(ed) {
  if (editorChangeKeys) return;
  editorChangeKeys = true;
  // The All changes tab (reviewall.js) steps across its sections first; the single-file view is the one tab with a diff otherwise.
  editorBind(ed, "editor.nextChange", () => { if (!(typeof reviewAllStep === "function" && reviewAllStep(1))) editorReviewStep(edTab, 1); });
  editorBind(ed, "editor.prevChange", () => { if (!(typeof reviewAllStep === "function" && reviewAllStep(-1))) editorReviewStep(edTab, -1); });
}

// editorBindNoteKeys binds Add note (the registry's editor.addNote: `c` and Mod+Shift+A, editornotes.js), once, for the same reason as
// editorBindChangeKeys: addCommand makes a page keybinding. The plain `c` is typing everywhere but in a read-only diff editor that has
// its text focus (the Changes view; not its find widget, not a note's text box, which are not the editor's text), so its context says so
// and Monaco never runs the command (nor swallows the key) anywhere else. A chord with a modifier types nothing, and the command asks
// where the keyboard is (a Changes view, or the Code editor).
let editorNoteKeys = false;
function editorBindNoteKeys(ed) {
  if (editorNoteKeys) return;
  editorNoteKeys = true;
  const entry = scEntry("editor.addNote");
  for (const chord of entry ? activeChords(entry, IS_MAC) : []) {
    const keys = monacoKeyFor(chord, dockMonaco, IS_MAC);
    const plain = !chord.mod && !chord.ctrl && !chord.alt && !chord.shift;
    if (keys) ed.addCommand(keys, () => editorNotesKey(), plain ? "isInDiffEditor && editorReadonly && editorTextFocus" : undefined);
  }
}

// editorEnsure creates the shared editor once. Options that are decisions, not defaults:
//  - automaticLayout: the dock resizes (drag, Full mode, the explorer folding) and the editor follows by itself;
//  - no minimap, no sticky scroll: mockup B1 shows neither;
//  - no automatic suggestion popups: with them on, Enter accepts a suggestion instead of ending the line, which is
//    the wrong thing in a box where the agent writes the code and a human fixes a line;
//  - fixedOverflowWidgets: the find widget and hovers are not cut by the dock's edge;
//  - selectOnLineNumbers (Monaco's default, stated because the selection bar depends on it): a click on a line number selects that
//    line, and the bar then offers its path:line (the read view's line-number menu is gone from the dock).
function editorEnsure() {
  if (dockEditor) return dockEditor;
  dockEditorEl = document.createElement("div");
  dockEditorEl.className = "dk-ed-mon";
  dockEditor = dockMonaco.editor.create(dockEditorEl, Object.assign({
    model: null,
    theme: MONACO_THEME,
    automaticLayout: true,
    minimap: { enabled: false },
    stickyScroll: { enabled: false },
    scrollBeyondLastLine: false,
    fixedOverflowWidgets: true,
    selectOnLineNumbers: true,
    quickSuggestions: false,
    suggestOnTriggerCharacters: false,
    wordBasedSuggestions: "off",
    parameterHints: { enabled: false },
    lineNumbersMinChars: 3,
  }, editorOptions()));
  editorBind(dockEditor, "editor.save", () => { if (dockEditorOwner) dockEditorOwner.save(); });
  editorBind(dockEditor, "editor.wrap", () => editorWrapKey()); // Wrap lines, or Wrap diff while the Changes view has the keyboard
  editorBindChangeKeys(dockEditor);
  editorBindNoteKeys(dockEditor);
  dockEditor.onDidChangeCursorPosition(() => { if (dockEditorOwner) dockEditorOwner.paintStatus(); });
  editorSelBarInstall(dockEditor, dockMonaco); // editorselbar.js: the bar over a selection
  return dockEditor;
}

// editorAccessOptions: what the shared editor is told about whether the tab it shows may be typed into, and what it says when
// someone tries.
function editorAccessOptions(tab) {
  return { readOnly: tab.mode !== "edit", readOnlyMessage: { value: tab.readOnlyWhy() || "This file is read-only." } };
}

// editorClaim puts `tab`'s model into the shared editor and restores its view. The tab that had it stashes its
// view first, wherever it was hidden from (a detach that never called onHide still loses nothing).
function editorClaim(tab) {
  const ed = editorEnsure();
  if (dockEditorOwner && dockEditorOwner !== tab) dockEditorOwner.stashView();
  tab.bodyEl.replaceChildren(dockEditorEl);
  ed.setModel(tab.model);
  dockEditorOwner = tab;
  ed.updateOptions(Object.assign({ ariaLabel: tab.name }, editorAccessOptions(tab)));
  ed.layout();
  tab.restoreView();
}

class EditorTab {
  // opts = {box, line, col, alt, view, scroll, restored, host}: what ViewerTab takes, for the same callers.
  constructor(path, opts) {
    const o = opts || {};
    if (typeof normalizePath === "function") path = normalizePath(path);
    this.box = (typeof o.box === "string" && o.box) || "";
    this.id = this.box ? "view:box:" + this.box + ":" + path : "view:" + path;
    this.kind = "viewer";
    this.path = path;
    this.name = (this.box ? "box · " : "") + (path.split("/").pop() || path);
    this.statusText = "";       // "Saved" for EDITOR_SAVED_MS after a save
    this.onUpdate = null;       // a field the dock assigns (drawerPaint); changed() calls it
    this.host = o.host || null; // {pathText, onEditStart, onDirtyChange}: what the dock hears from its tabs
    this.restored = !!o.restored; // a tab nobody asked for just now: its first read of a gone file closes it
    this.alt = o.alt && typeof o.alt.base === "string" && typeof o.alt.rel === "string" ? o.alt : null; // #53
    this.altPending = false;
    this.disposed = false;
    this.firstLoad = true;
    this.loadSeq = 0;
    // "edit" | "read" | "image" | "binary" once read (editorMode); null until then.
    this.mode = null;
    this.readOnly = !!this.box; // true for a tab shown locked: a box file is known so before any read, the rest after it
    this.gone = false;          // opened from a Changes row of a file deleted from disk: an empty read-only buffer for the Changes view (load)
    this.serverReadOnly = false; // the server's own flag (a scratch folder), as read
    this.notUtf8 = false;       // the server's `utf8: false`: the bytes are not UTF-8, the buffer shows U+FFFD for them, so no save
    this.model = null;          // the monaco text model, once the file is read and Monaco is loaded
    this.langName = "";         // its language as the status bar says it
    this.version = "";          // sha256 of the text on disk as last read or saved: the save's base_version
    this.diskText = "";         // that text, for a discard
    this.savedAlt = 0;          // the model's alternative version id at the last save (dirty() compares); -1 after Keep mine: dirty for good
    this.keptFrom = null;       // after Keep mine: the version the buffer began at (diskText goes with it), for a discard
    this.dirtyShown = false;    // what the host was last told
    this.applying = false;      // a programmatic replace of the buffer is under way: not an edit
    this.saving = false;
    this.saveMark = 0;          // moves when a save ends: a disk poll that began before it is not about now (none begins during one)
    this.sync = null;           // editorsync.js: the bar, the compare view and the replace subscribers of this tab
    this.attached = false;      // el is in the document
    this.viewState = null;      // the editor's view of this model, saved when it is hidden
    this.lastScroll = clampScroll(o.scroll);
    this.pendingLine = Number.isInteger(o.line) && o.line > 0 ? { line: o.line, col: Number.isInteger(o.col) && o.col > 0 ? o.col : undefined } : null;
    this.pendingChanges = o.view === "changes"; // open in Changes as soon as the model is there (load)
    this.wantFocus = false;
    this.inner = null;          // a DockFileView, for a file that is not text
    this.contentSub = null;
    this.optionsSub = null;
    this.savedTimer = null;
    this.changes = null;        // editordiff.js: the file at git's base and the lines that differ ({head, lineChanges, ...}), once asked for
    this.diff = null;           // the Changes view while it is up: {diff, original, wrap, label}
    this.followBtn = null;      // editorfollow.js: the Follow toggle in the bar
    this.dg = null;             // editordiff.js: the request slot, the gutter's debounce and decorations
    this.modeSeg = null;        // the Inline | Side by side control, shown while in Changes: {el, inline, side}
    this.seg = null;            // the Code | Changes toggle in the bar (Preview | Source | Changes for markdown): {el, preview, code, changes}
    this.md = null;             // editormd.js: a markdown file's view and its Preview pane ({view, pane, text, timer, sub, top, live})
    this.img = null;            // imagediff.js: an image's Image | Changes toggle and its comparer ({seg, on, view}), built with the bar

    this.el = document.createElement("div");
    this.el.className = "tab-body dk-ed";
    this.altEl = document.createElement("div"); // the one-line note over a file opened from another checkout
    this.altEl.className = "viewer-altnote";
    this.altEl.hidden = true;
    this.roEl = document.createElement("div");  // why the file is read-only, under the crumbs
    this.roEl.className = "dk-ed-note";
    this.roEl.hidden = true;
    this.barEl = document.createElement("div"); // the amber conflict bar (editorsync.js fills it)
    this.barEl.className = "dk-conflict";
    this.barEl.setAttribute("role", "alert");
    this.barEl.hidden = true;
    this.cmpEl = document.createElement("div"); // the read-only compare of the disk with the buffer, in place of the editor
    this.cmpEl.className = "dk-ed-cmp";
    this.cmpEl.hidden = true;
    this.bodyEl = document.createElement("div"); // the editor, or a message, fills this
    this.bodyEl.className = "dk-ed-body";
    this.buildStatus();
    this.el.append(this.altEl, this.roEl, this.barEl, this.cmpEl, this.bodyEl, this.sbEl);
    if (typeof reviewBarMount === "function") reviewBarMount(this); // the review bar (reviewbar.js): first in the tab, while notes wait
    // Mod+S saves from anywhere in the tab, not only from the editor: a key Monaco handled is stopped before here.
    this.el.addEventListener("keydown", (e) => {
      if (e.defaultPrevented) return;
      if (shortcutMatches("editor.save", e, IS_MAC)) { e.preventDefault(); e.stopPropagation(); this.save(); }
      else if (editorReviewKey(this, e)) { e.preventDefault(); e.stopPropagation(); } // F7 and Alt+Z on the Changes header's own buttons
    });

    this.buildActs();
    this.toolbarApi = { paint: () => this.paint(), closeMenus: () => this.closeMenu() }; // gitPaintTree, drawerPaint
    this.setBodyMessage("loading…");
    this.load();
  }

  // --- the bar: ⋯ in the dock's action area, the status bar under the editor ---------------------------------

  // buildActs makes the actions this tab adds to the dock's bar (#dock-acts, left of Full): the Code | Changes toggle (Preview |
  // Source | Changes for a markdown file), Follow (the session's setting, so every tab's says the same: editorfollow.js) and the ⋯
  // menu. The toggle is for a file that is read as text; the ⋯ for every file once it is read (a file that is not text has a shorter
  // menu); Follow stays for every tab, an image and a binary file's included, and while a file is still being read (paint).
  buildActs() {
    this.actsEl = document.createElement("div");
    this.actsEl.className = "dk-vact";
    const more = document.createElement("button");
    more.type = "button";
    more.className = "btn btn-ghost btn-icon btn-sm";
    more.title = "More file actions";
    more.setAttribute("aria-label", "More file actions");
    more.setAttribute("aria-haspopup", "menu");
    more.setAttribute("aria-expanded", "false");
    more.dataset.menuFrom = "1"; // ftShowMenu: this button owns the menu while it is open
    const dot = (cx) => ["circle", { cx, cy: "7", r: "1.1", fill: "currentColor", stroke: "none" }];
    more.append(svgIcon([dot("2.5"), dot("7"), dot("11.5")], { icon: "more", size: "14" }));
    // stopPropagation: the document's closer for the kit menu would shut the menu this press opens.
    more.onclick = (e) => { e.stopPropagation(); this.openMenu(); };
    this.moreBtn = more;
    this.actsEl.append(editorDiffToggle(this), editorDiffModeToggle(this), editorImageToggle(this), editorFollowButton(this), more);
    this.seg.el.hidden = true; // until the file is read as text (paint)
    more.hidden = true;
  }

  // buildStatus makes the status bar (mockup B1): the branch and the "Saved" word on the left, then Ln/Col,
  // indentation, encoding, line ending and language on the right. Text only.
  buildStatus() {
    const span = (cls) => { const s = document.createElement("span"); s.className = cls; return s; };
    this.sbEl = document.createElement("div");
    this.sbEl.className = "dk-sb";
    this.sbEl.hidden = true; // not a live region: its Ln/Col changes with every key and click, and a screen reader would read out each
    this.sbBranch = span("dk-sb-branch");
    this.sbSaved = span("dk-sb-saved");
    const right = span("dk-sb-r");
    this.sbParts = {};
    for (const k of ["pos", "indent", "enc", "eol", "lang"]) { this.sbParts[k] = span("dk-sb-" + k); right.append(this.sbParts[k]); }
    this.sbEl.append(this.sbBranch, this.sbSaved, right);
  }

  // paintStatus writes the status bar from the editor and the model. Cheap: text is written only when it changed.
  paintStatus() {
    if (this.disposed || !this.model) return;
    const set = (n, t) => { if (n.textContent !== t) n.textContent = t; };
    // Where the cursor is: in the Changes view, on its new side; else in the editor, if this tab has it.
    const ed = this.diff ? this.diff.diff.getModifiedEditor() : dockEditorOwner === this && dockEditor;
    const pos = ed ? ed.getPosition() : null;
    const mo = this.model.getOptions();
    const p = statusBarParts({
      line: pos ? pos.lineNumber : 1, col: pos ? pos.column : 1,
      insertSpaces: mo.insertSpaces, tabSize: mo.tabSize, eol: this.model.getEOL(),
      lang: this.langName, utf8: !this.notUtf8,
    });
    for (const k of Object.keys(this.sbParts)) set(this.sbParts[k], p[k]);
    // Preview has no cursor and no indentation to speak of (mockup B4's bar says only Markdown, UTF-8 and LF).
    this.sbParts.pos.hidden = this.sbParts.indent.hidden = editorMdUp(this);
    // R6: the branch, only when the git base for the tree's root has said one.
    const base = typeof gitBaseFor === "function" && typeof ftRoot === "function" ? gitBaseFor(ftRoot()) : null;
    const branch = base && typeof base.branch === "string" ? base.branch : "";
    set(this.sbBranch, branch);
    this.sbBranch.hidden = !branch;
    set(this.sbSaved, this.statusText);
    this.sbSaved.hidden = !this.statusText;
  }

  // paint repaints everything this tab shows outside the editor: the read-only note, the toggle and the ⋯, the status bar.
  paint() {
    if (this.disposed) return;
    const ready = !!this.model && !this.inner;
    this.seg.el.hidden = !ready;
    this.moreBtn.hidden = !(ready || this.inner); // a view that is not text has its own status bar (DockFileView), so sbEl is for text only
    this.sbEl.hidden = !ready;
    const why = this.readOnlyWhy();
    if (this.roEl.textContent !== why) this.roEl.textContent = why;
    this.roEl.hidden = !why || !ready;
    this.paintStatus();
    editorDiffPaintToggle(this);
    editorReviewPaint(this);
    editorImagePaint(this);
    editorFollowPaintTab(this);
    editorSelBarUpdate(); // the Changes view or Preview took the editor's place, or gave it back: the bar is for Code only
    this.paintReviewBar();
  }

  // paintReviewBar: whether Paste may act depends on this tab's unsaved edit, for the bar of every tab of the session, not only this one's.
  paintReviewBar() {
    const sid = this.host && typeof this.host.sessionId === "function" ? this.host.sessionId() : null;
    if (sid && typeof reviewBarRender === "function") reviewBarRender(sid);
  }

  // changed: something the dock shows moved (the lock, the dirty dot, the label). Like ViewerTab.changed.
  changed() {
    this.paint();
    if (this.onUpdate) this.onUpdate();
  }

  readOnlyWhy() {
    if (this.mode === "image" || this.mode === "binary") return "";
    return readOnlyReason({ box: this.box, readonly: this.serverReadOnly, notUtf8: this.notUtf8, unknown: this.mode === "read", gone: this.gone });
  }

  // previewing: a markdown file's Preview is in the body (editormd.js), so the editor is not on screen even while this tab owns it.
  get previewing() {
    return editorMdUp(this);
  }

  // crumbNote: the right side of the crumbs row while the buffer holds an edit, in place of "Edited by" (dockRender).
  crumbNote() {
    return this.dirty() ? unsavedNote(shortcutHint("editor.save", IS_MAC)) : "";
  }

  // --- reading the file -------------------------------------------------------------------------------------

  setBodyMessage(text, cls) {
    const d = document.createElement("div");
    d.className = "viewer-msg" + (cls ? " " + cls : "");
    d.textContent = text;
    this.bodyEl.replaceChildren(d);
  }

  // load reads the file and, in parallel, loads Monaco (the first tab pays for it; the rest share it). An image, a video, an audio
  // file, a PDF and the types with no viewer are known by their extension and need no text read: one small metadata read, then the
  // view (loadView). Everything else is decided by what the server says it is (editorMode).
  async load() {
    const seq = ++this.loadSeq;
    const first = this.firstLoad;
    this.firstLoad = false;
    const dock = dockKindOf(extOf(this.path));
    if (dock !== "text") { this.loadView(dock, seq); return; }
    const url = this.box ? boxFileURL(this.box, this.path) : undefined;
    let [res, mon] = await Promise.all([fsRead(this.path, url), loadMonaco().then((m) => ({ m }), (err) => ({ err }))]);
    if (this.disposed || seq !== this.loadSeq) return;
    const alt = first ? this.alt : null; // the lookup is for the first read only: a file deleted later is a deletion
    this.alt = null;
    const altLine = alt ? this.pendingLine : null;
    // A D row of the Changes section opens the file in Changes though it is gone: the 404 is the file's state, not an error (openDeleted).
    // The tab gets an empty read-only buffer to compare the base text with; nothing reads or polls the file after this (editorsync.js).
    if (!res.ok && openDeleted({ status: res.status, changes: this.pendingChanges, restored: first && this.restored, alt: !!alt, box: !!this.box })) {
      this.gone = true;
      res = { ok: true, status: 200, data: { kind: "text", readonly: false, utf8: true, content: "", version: "" } };
      editorSyncSync(); // a tab on screen is not asked about its file any more
    }
    if (!res.ok || res.data == null) {
      // A restored tab nobody asked for closes on a gone file instead of parking an error on it. Exactly 404: status 0
      // is "the fetch never completed", which says nothing about the file.
      if (first && this.restored && res.status === 404) { closeTabEverywhere(this); return; }
      if (alt && res.status === 404 && !this.box) { this.findAlternates(alt, res, altLine); return; }
      // Text over the limit: the card, with the size and time the server's refusal carries, and Download.
      if (res.status === 413 && res.data) { this.mountInner("toolarge", res.data); return; }
      this.showReadError(res);
      return;
    }
    const mode = this.gone ? "read" : editorMode({ kind: res.data.kind, readonly: res.data.readonly === true, box: !!this.box, utf8: res.data.utf8 });
    if (mode === "image" || mode === "binary") { this.mountInner(mode, res.data); return; }
    if (mon.err) {
      // The next open tries again (loadMonaco forgets a failure), so say what failed rather than only "reload".
      showToast("The editor could not be loaded: " + (mon.err.message || "unknown error"), { error: true });
      this.setBodyMessage("The editor could not be loaded. Close this tab and open the file again, or reload the page.", "err");
      this.statusText = "";
      return;
    }
    dockMonaco = mon.m;
    this.mode = mode;
    this.readOnly = mode !== "edit";
    this.serverReadOnly = res.data.readonly === true;
    this.notUtf8 = res.data.utf8 === false;
    this.langName = langLabel(monacoLang(this.path), dockMonaco.languages.getLanguages());
    this.version = res.data.version || "";
    this.diskText = res.data.content || "";
    this.model = dockMonaco.editor.createModel(this.diskText, monacoLang(this.path));
    this.savedAlt = this.model.getAlternativeVersionId();
    if (typeof editorNotesTrack === "function") editorNotesTrack(this); // the file's notes follow its text for as long as the tab is open (editornotes.js)
    this.contentSub = this.model.onDidChangeContent(() => this.onContent());
    this.optionsSub = this.model.onDidChangeOptions(() => this.paintStatus());
    if (this.attached) this.show();
    this.changed(); // the lock in the tab, the note, the bar
    if (this.pendingChanges) { this.pendingChanges = false; this.showChanges(true); }
  }

  // showReadError paints a failed read with the server's own sentence.
  showReadError(res) {
    this.setBodyMessage((res.data && res.data.error) || (res.status ? "HTTP " + res.status : "could not reach the server"), "err");
    this.paint();
  }

  // loadView: a file known by its extension to be an image, a video, an audio file, a PDF or a type with no viewer. The metadata read
  // (size, modified time) is the only request; one that failed (null) still mounts the view, which leaves those facts out.
  async loadView(kind, seq) {
    const meta = await this.fetchMeta();
    if (this.disposed || seq !== this.loadSeq) return;
    this.mountInner(kind, meta);
  }

  // fetchMeta: GET ...&meta=1 for this file ({kind, size, readonly, mtime}, no content), or null when it did not answer. The view asks
  // again when its tab is shown again (DockFileView.refresh).
  async fetchMeta() {
    const res = await fsRead(this.path, editorSyncMetaURL(this));
    return res.ok && res.data && typeof res.data === "object" ? res.data : null;
  }

  // mountInner opens a file that is not text inside this tab: `kind` is image | video | audio | pdf | card | binary | toolarge, `meta`
  // what the server said of the file ({size, mtime}) or null. The dock sees one viewer: this one forwards the lifecycle and the
  // dock's other calls (dockHolder finds the view by its holder). Only an image has the Image | Changes mode (imagediff.js).
  mountInner(kind, meta) {
    this.mode = kind === "image" ? "image" : "binary";
    const v = new DockFileView({
      path: this.path,
      box: this.box,
      kind,
      meta: meta ? { size: meta.size, mtime: meta.mtime } : null,
      download: () => this.download(),
      fetchMeta: () => this.fetchMeta(),
    });
    v.onUpdate = () => this.changed();
    this.inner = v;
    this.pendingLine = null;
    this.el.replaceChildren(v.el);
    if (this.attached) { v.onShow(); v.onAttach(); }
    this.changed(); // the view has its own status bar: the dock's bar keeps Follow, the ⋯ (and, for an image git lists, Image | Changes: imagediff.js)
    // A D row, or a badge, asked for the Changes view: an image's is the comparer (a deleted one has only its old side).
    if (kind === "image" && this.pendingChanges) { this.pendingChanges = false; editorImageSet(this, true); }
  }

  // findAlternates (#53): the first read of a relative terminal link was a 404. Look for the same repo-relative path
  // in the repo's other checkouts: none is the ordinary not-found, one re-points this tab, several are listed.
  async findAlternates(alt, res, line) {
    this.altPending = true;
    const list = await fsAlternates(alt.base, alt.rel);
    this.altPending = false;
    if (this.disposed) return;
    const o = alternatesOutcome(list);
    if (o.kind === "one") { this.openAlternate(o.path, o.worktree, line); return; }
    if (o.kind === "pick") { this.showAltPick(o.items, alt, line); return; }
    this.showReadError(res);
  }

  // openAlternate re-points this tab at a file in another checkout and reads it. When another tab already shows that
  // file, repointViewer opens it at the same line and closes this one (false: do not touch this tab again).
  openAlternate(path, worktree, line) {
    if (!repointViewer(this, path, line)) return;
    this.pendingLine = line || null;
    this.setAltNote(worktree);
    this.setBodyMessage("loading…");
    this.load();
  }

  // setAltNote shows where the file was opened from (altNoteNodes, viewer-tab.js: the phone's viewer draws the same).
  setAltNote(worktree) {
    this.altEl.replaceChildren(...altNoteNodes(worktree));
    this.altEl.hidden = false;
  }

  // showAltPick lists the other checkouts that hold the file, in the tab: a list, not a modal (altPickNode, viewer-tab.js).
  showAltPick(items, alt, line) {
    this.bodyEl.replaceChildren(altPickNode(items, alt, (it) => this.openAlternate(it.path, it.worktree, line)));
  }

  // --- the dock's lifecycle ---------------------------------------------------------------------------------

  onShow() {}

  // onAttach: el was just put in the document. The editor moves in once there is a model to show.
  onAttach() {
    if (this.disposed) return;
    this.attached = true;
    const acts = document.getElementById("dock-acts");
    if (acts) acts.insertBefore(this.actsEl, document.getElementById("drawer-max"));
    this.placeBar(true);
    if (this.inner) { this.inner.onAttach(); if (editorImageOn(this)) editorImageEnter(this); return; }
    if (this.model) this.show();
    editorSyncStart(this); // the file on disk is asked about while this tab is the one on screen
  }

  // placeBar puts the conflict bar where mockup B4 draws it while this tab is on screen: in the dock's slot between the tab strip and
  // the crumbs (#dock-conflict). The bar is about the file of the tab that is shown, so it follows a tab switch as the bar's actions
  // do (attach, hide). Off screen, and in a page with no slot, it is back in the tab's own element.
  placeBar(on) {
    const slot = on ? document.getElementById("dock-conflict") : null;
    if (slot) slot.replaceChildren(this.barEl);
    else if (this.barEl.parentNode !== this.el) {
      // An image or a binary file's tab (mountInner) holds only its viewer: cmpEl is no child of el, and insertBefore would throw out of
      // dispose(). The bar is for a text file's conflicts: with no place in el it is simply taken out of the dock's slot.
      if (this.cmpEl.parentNode === this.el) this.el.insertBefore(this.barEl, this.cmpEl); else this.barEl.remove();
    }
  }

  // onHide: another tab is about to be shown. The view is kept for the way back.
  onHide() {
    this.attached = false;
    editorSyncStop(this);
    this.closeMenu();
    this.actsEl.remove();
    this.placeBar(false);
    if (this.inner) { this.inner.onHide(); return; }
    editorMdDetach(this);
    editorDiffDetach(this);
    this.stashView();
    editorSelBarUpdate(); // this tab is off screen: its selection bar goes with it
  }

  // show: this tab is on screen with a model, so it takes the shared editor, or, if it was left in Changes, opens that view again
  // (its diff editor was let go when it was hidden: the dock has one at a time), or, for a markdown file in Preview, shows that.
  show() {
    if (!editorDiffResume(this) && !editorMdAttach(this)) editorClaim(this);
    this.paint();
    editorDiffAttach(this);
  }

  // stashView saves where the editor is over this model. Only the tab that has the editor in its body has a live view: one that
  // went to Changes saved its view when it left Code, and the editor has not moved since (a detached editor has no size to keep it by).
  stashView() {
    if (dockEditorOwner !== this || !dockEditor || !this.model || dockEditorEl.parentNode !== this.bodyEl) return;
    this.viewState = dockEditor.saveViewState();
    this.lastScroll = dockEditor.getScrollTop();
  }

  // restoreView: the saved view, or the stored scroll of a tab restored from the last visit; then a line a link
  // asked for, and the keyboard if it was asked for while the file was still loading and is still free.
  restoreView() {
    const ed = dockEditor;
    if (this.viewState) ed.restoreViewState(this.viewState);
    else if (this.lastScroll > 0) ed.setScrollTop(this.lastScroll, dockMonaco.editor.ScrollType.Immediate);
    if (this.pendingLine) { const p = this.pendingLine; this.pendingLine = null; this.revealNow(p.line, p.col); }
    this.takeWantedFocus(ed);
  }

  // takeWantedFocus gives `ed` the keyboard if it was asked for while the file was still loading and is still free.
  takeWantedFocus(ed) {
    if (!this.wantFocus) return;
    this.wantFocus = false;
    const ae = document.activeElement;
    if (!ae || ae === document.body || (ae.closest && ae.closest("#drawer"))) ed.focus();
  }

  // scroll: where the file is read, in px, for the dock's stored state (layout.js savedScroll). Live for the tab
  // that has the editor and on screen; the value saved at the last hide (or at the move to Changes) otherwise.
  get scroll() {
    if (this.inner) return this.inner.scroll;
    if (editorMdUp(this)) return clampScroll(editorMdScroll(this)); // Preview's own, in its own px
    if (this.attached && !this.diff && dockEditorOwner === this && dockEditor) return clampScroll(dockEditor.getScrollTop());
    return this.lastScroll;
  }

  // fileScroll: savedScroll stores this instead of `scroll` while `changes` is set, since a viewer showing its diff stores where the
  // FILE was read (the diff is not persisted). Here `changes` is also set in Code, where the gutter's data lives, and the Changes
  // view has no scroll of its own to store, so the two are one answer: where the file is read.
  get fileScroll() {
    return this.scroll;
  }

  focusReader() {
    if (this.img && this.img.view) { this.img.view.el.focus({ preventScroll: true }); return; } // an image's comparer
    if (this.inner) { this.inner.focusReader(); return; }
    if (this.diff) { this.diff.diff.getModifiedEditor().focus(); return; }
    if (editorMdUp(this)) { this.md.pane.focus({ preventScroll: true }); return; }
    if (this.attached && dockEditorOwner === this && dockEditor) dockEditor.focus();
    else this.wantFocus = true;
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    editorSyncDispose(this); // before the model goes: a compare view holds it
    editorDiffDispose(this); // and so does the Changes view
    if (typeof editorNotesUntrack === "function") editorNotesUntrack(this); // its note decorations and its listener
    if (typeof reviewBarUnmount === "function") reviewBarUnmount(this); // its review bar
    editorMdDispose(this);   // and the Preview's subscription
    if (this.savedTimer) clearTimeout(this.savedTimer);
    this.closeMenu();
    this.actsEl.remove();
    this.placeBar(false);
    editorImageDispose(this); // the comparer's request and blob URL, before the viewer goes
    if (this.inner) this.inner.dispose();
    if (this.contentSub) this.contentSub.dispose();
    if (this.optionsSub) this.optionsSub.dispose();
    if (this.model) {
      if (dockEditorOwner === this) { dockEditor.setModel(null); dockEditorOwner = null; }
      this.model.dispose();
      this.model = null;
    }
  }

  // --- the buffer --------------------------------------------------------------------------------------------

  // dirty: the buffer differs from what was last read or saved. The alternative version id returns to its saved
  // value when an undo walks back to it, so the dot goes away then too, and it costs nothing to ask.
  dirty() {
    return !!this.model && this.mode === "edit" && this.model.getAlternativeVersionId() !== this.savedAlt;
  }

  // generation: a token that differs whenever the buffer does, for the dock's "Cancel stands until it changes".
  generation() {
    return this.model ? this.model.getAlternativeVersionId() : null;
  }

  // onContent: the model changed. A typed edit that makes it dirty pins a preview tab (host.onEditStart); any flip of
  // dirty tells the dock (the dot, the crumbs). A programmatic replace (applyText) is not an edit.
  onContent() {
    if (this.disposed || this.applying) return;
    this.syncDirty(true);
    this.paintStatus();
  }

  // syncDirty tells the host when the dirty state flipped. `edit` is true only for a change the user made.
  syncDirty(edit) {
    const d = this.dirty();
    if (d === this.dirtyShown) return;
    this.dirtyShown = d;
    this.paintReviewBar();
    if (d && edit && this.host && this.host.onEditStart) this.host.onEditStart(this);
    if (this.host && this.host.onDirtyChange) this.host.onDirtyChange(this);
  }

  // applyText replaces the whole buffer with `text` as the clean state: a discard. Not an edit: the host is not told to pin
  // the tab. (A load of what is on disk is replaceFromDisk.)
  applyText(text) {
    if (!this.model) return;
    this.applying = true;
    try { this.model.setValue(text); } finally { this.applying = false; }
    this.diskText = text;
    this.savedAlt = this.model.getAlternativeVersionId();
    this.syncDirty(false);
    this.paintStatus();
  }

  // discardEdits drops what was typed (the user said Discard to the dock's ask).
  discardEdits() {
    if (!this.dirty()) return;
    if (this.keptFrom !== null) { this.version = this.keptFrom; this.keptFrom = null; } // diskText is the text of that version
    this.applyText(this.diskText);
  }

  // replaceFromDisk puts `text`, the file as it is on disk at `version`, in the buffer as its clean state: a clean buffer
  // reloading, or "Load the agent's version" over a dirty one (editorsync.js). It is a new value (setValue), and that is the
  // point: Monaco clears the model's undo history with it, so no undo can walk back past the replace to text the disk's
  // version no longer stands for. (An undo that did would leave a dirty buffer whose base is the agent's version, and its
  // save would overwrite the agent's text with no 409 and no bar.) The history is the price. The cursor and the scroll stay
  // where they were: a new value sends them to the top, so the view is saved first and put back. Not an edit of the user's:
  // the host is not told to pin the tab.
  // `utf8` is the encoding the disk's file has now (false: not UTF-8). The file that was written under an open tab may have
  // become one that the buffer cannot hold byte for byte, or stopped being one, so the tab's lock follows it (setEncoding).
  replaceFromDisk(text, version, utf8) {
    if (!this.model) return;
    const owner = dockEditorOwner === this && dockEditor;
    const view = owner ? dockEditor.saveViewState() : null;
    this.applyText(text);
    this.keptFrom = null;
    this.version = version;
    this.setEncoding(utf8 !== false);
    if (view) dockEditor.restoreViewState(view);
  }

  // setEncoding: the file on disk is valid UTF-8 (true) or is not. A file that is not opens (or turns) read-only, and one that
  // is valid again is editable again unless something else locks it (a box, the server's flag). The shared editor, the note
  // under the crumbs, the status bar and the dock's lock are told when anything changed.
  setEncoding(valid) {
    if (this.notUtf8 === !valid) return;
    this.notUtf8 = !valid;
    this.mode = editorMode({ kind: "text", readonly: this.serverReadOnly, box: !!this.box, utf8: valid });
    this.readOnly = this.mode !== "edit";
    if (dockEditorOwner === this && dockEditor) dockEditor.updateOptions(editorAccessOptions(this));
    this.changed();
  }

  // keepOverDisk is "Keep mine": `version`, the disk's, becomes the base of the next save, so it overwrites the file. The
  // buffer is dirty from here until a save succeeds, whatever an undo does to its text: an undo that walked back to the
  // text the buffer began with would otherwise read as clean, with the agent's file under it, nothing to ask about and a
  // base that matches the disk. What the buffer began at is kept for a discard (diskText is the text that goes with it).
  keepOverDisk(version) {
    if (this.keptFrom === null) this.keptFrom = this.version;
    this.version = version;
    this.savedAlt = -1;
    this.syncDirty(false);
    this.paintStatus();
  }

  // save writes the buffer with the version it was read at (base_version), once per press. A 409 never overwrites:
  // it goes to conflictBar, and the bar stays until the user picks (editorsync.js).
  async save() {
    if (this.disposed || !this.model || this.inner) return;
    if (this.mode !== "edit") { showToast(this.readOnlyWhy() || "This file is read-only"); return; }
    if (this.saving || !this.dirty()) return;
    // preserveBOM: a file that starts with a byte order mark is saved with it, as it was read.
    const text = this.model.getValue(dockMonaco.editor.EndOfLinePreference.TextDefined, true);
    const alt = this.model.getAlternativeVersionId();
    this.saving = true;
    const res = await fsWrite(this.path, saveBody(this.path, text, this.version, false));
    this.saving = false;
    this.saveMark++;
    if (this.disposed) return;
    const d = classifySave(res);
    if (d.outcome === "saved") {
      this.version = d.version || "";
      this.keptFrom = null;
      this.diskText = text;
      this.savedAlt = alt; // an edit made while the write was in flight stays dirty
      this.syncDirty(false);
      this.flashSaved();
      editorSyncClearBar(this); // a bar over a buffer that is now what is on disk (the agent had put the file back) has nothing to ask
      if (typeof gitStatusRefreshSoon === "function") gitStatusRefreshSoon(); // the file's git mark may have changed
    } else if (d.outcome === "conflict") {
      this.conflictBar(d.currentVersion);
    } else if (res && res.status === 404) {
      editorSyncAct(this, 404, ""); // the file is gone: the deleted bar says so, with what can still be done
    } else {
      showToast(d.message, { error: true });
    }
  }

  // flashSaved shows "Saved" in the status bar for EDITOR_SAVED_MS. The dot has already gone.
  flashSaved() {
    this.statusText = "Saved";
    this.paintStatus();
    if (this.savedTimer) clearTimeout(this.savedTimer);
    this.savedTimer = setTimeout(() => {
      this.savedTimer = null;
      this.statusText = "";
      this.paintStatus();
    }, EDITOR_SAVED_MS);
  }

  // conflictBar: the save got a 409, the file changed on disk after this buffer was read. Nothing was written and the buffer
  // is untouched: the amber bar (Compare, Keep mine, Load the agent's version) goes up, or stays up, and `currentVersion`
  // (the server's, "" for a file past the read cap) is what Keep mine will take as the next base version.
  conflictBar(currentVersion) {
    editorSyncAct(this, 409, currentVersion || "");
  }

  // --- lines ---------------------------------------------------------------------------------------------------

  // revealLine puts the cursor on `line` (1-based; and `col`) and shows it, without taking the keyboard: Follow and a
  // terminal link both come here and neither may steal focus from where the user is. A tab that is not on screen, or
  // not read yet, keeps the line for when it is.
  revealLine(line, col) {
    if (this.inner) { this.inner.revealLine(line, col); return; }
    // In Preview there is no line to show: it is kept for Source (restoreView), which is where the user can ask for it.
    if (!this.model || !this.attached || (!this.diff && (dockEditorOwner !== this || editorMdUp(this)))) { this.pendingLine = { line, col }; return; }
    this.revealNow(line, col);
  }

  // revealNow shows the line in the editor the user is looking at (Code's, or Changes' new side) and bands it, as the read view's
  // target band did (editordiff.js).
  revealNow(line, col) {
    const n = this.model.getLineCount();
    const total = n > 1 && this.model.getLineLength(n) === 0 ? n - 1 : n; // the empty line after a final newline is not one
    const c = clampLine(line, total);
    if (c.clamped) showToast(linesOnlyText(this.name, total));
    const ed = this.diff ? this.diff.diff.getModifiedEditor() : dockEditor;
    ed.setPosition({ lineNumber: c.line, column: col > 0 ? col : 1 });
    ed.revealLineInCenter(c.line, dockMonaco.editor.ScrollType.Immediate);
    editorDiffTarget(this, c.line);
  }

  // showChanges(on): the Changes view (editordiff.js), the file against git's base, read-only; false goes back to Code. A call with
  // no argument turns it on, as ViewerTab.showChanges does (filesdrawer.js, a badge's click). Returns whether it is up or on its way.
  showChanges(on) {
    if (this.inner && this.mode === "image") return editorImageSet(this, on !== false); // no Monaco: the comparer (imagediff.js)
    return editorDiffSet(this, on !== false);
  }

  // --- preferences ---------------------------------------------------------------------------------------------

  wrapAnchor() { return this.inner ? this.inner.wrapAnchor() : () => {}; } // Monaco keeps the first visible line itself
  applyWrap() { if (this.inner) this.inner.applyWrap(); else editorApplyPrefs(); }

  // --- the ⋯ menu ----------------------------------------------------------------------------------------------

  closeMenu() {
    if (this.moreBtn && this.moreBtn.getAttribute("aria-expanded") === "true") closeRowMenu();
  }

  // openMenu: Copy path, Copy relative path, Reveal in tree, Download, Wrap lines (Alt+Z; in Changes Wrap diff and Line numbers), Font size, Copy contents.
  // A file that is not text has only the first four (and a PDF, Open in new tab): nothing in it is for an editor.
  // The kit's menu, as the explorer's ⋯ is; a second press on the ⋯ closes it.
  openMenu() {
    const more = this.moreBtn;
    const menu = document.getElementById("ft-menu");
    if (!menu || typeof ftShowMenu !== "function") return;
    if (more.getAttribute("aria-expanded") === "true") { closeRowMenu(true); return; }
    const rel = relativeAvail(this, typeof ftRoot === "function" ? ftRoot() : "");
    const reveal = revealable(this);
    // A row that cannot act stays in the menu, aria-disabled with its reason as the title (the arrow keys step over it).
    const item = (label, icon, run, can, why) => {
      const b = kitMenuButton(label, icon, false, () => {
        if (b.getAttribute("aria-disabled") === "true") return;
        closeRowMenu(true);
        run();
      });
      if (can === false) { b.setAttribute("aria-disabled", "true"); b.title = why || ""; }
      return b;
    };
    // A checkbox row with the chord that toggles it (the registry's editor.wrap: Wrap lines in Code, Wrap diff in Changes).
    const check = (label, on, run, kbdId) => {
      const row = item(label, on ? "check" : "", run);
      if (kbdId) {
        const kbd = document.createElement("span");
        kbd.className = "menu-kbd";
        kbd.textContent = shortcutHint(kbdId, IS_MAC);
        row.append(kbd);
      }
      row.setAttribute("role", "menuitemcheckbox");
      row.setAttribute("aria-checked", on ? "true" : "false");
      return row;
    };
    // In Changes the view has its own two: Wrap diff (separate from the editor's wrap, so Wrap lines has nothing to say there) and Line numbers.
    const viewRows = () => this.diff
      ? [check("Wrap diff", viewerPrefs.getDiffWrap(), () => diffWrapToggle(), "editor.wrap"), check("Line numbers", dgLineNumbers, () => diffLineNumbersToggle())]
      : [check("Wrap lines", viewerPrefs.getWrap(), () => toggleViewerWrap(), "editor.wrap")];
    const common = [
      item("Copy path", "copy", () => this.copyPath("abs")),
      item("Copy relative path", "copy", () => this.copyPath("rel"), rel.ok, rel.title),
      item("Reveal in tree", "files", () => this.revealInTree(), reveal.ok, reveal.title),
      item("Download", "download", () => this.download()),
    ];
    const nodes = this.inner
      ? (this.inner.openable ? [...common, kitMenuSep(), item("Open in new tab", "", () => this.inner.open())] : common)
      : [...common, kitMenuSep(), ...viewRows(), this.fontRow(), kitMenuSep(), item("Copy contents", "copy", () => this.copyContents())];
    ftShowMenu(more, nodes, false, "File actions");
    const first = menu.querySelector(".menu-item");
    if (first) first.focus();
  }

  // fontRow: "Font size  A−  12.5  A+", the code size every file view shares (fontPrefs surface "code"), or the markdown size
  // while Preview is up (surface "markdown": the document reads --fs-md). A press steps it and leaves the menu open, so it can
  // be pressed again.
  fontRow() {
    const surface = editorMdUp(this) ? "markdown" : "code";
    const row = document.createElement("div");
    row.className = "dk-fs";
    row.setAttribute("role", "group");
    row.setAttribute("aria-label", "Font size");
    const lbl = document.createElement("span");
    lbl.className = "menu-text";
    lbl.textContent = "Font size";
    const val = document.createElement("span");
    val.className = "dk-fs-val";
    const paint = () => { val.textContent = String(fontPrefs.get(surface)); };
    const step = (dir, label, aria) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "menu-item dk-fs-btn";
      b.setAttribute("role", "menuitem");
      b.setAttribute("aria-label", aria);
      b.textContent = label;
      b.onclick = () => {
        const next = fontStepped(fontPrefs.get(surface), dir, fontPrefs.STEP, fontPrefs.BOUNDS);
        if (next !== null) fontPrefs.set(surface, next);
        paint();
      };
      return b;
    };
    paint();
    row.append(lbl, step(-1, "A−", "Smaller text"), val, step(1, "A+", "Bigger text"));
    return row;
  }

  copyPath(kind) {
    let text;
    if (kind === "rel") {
      const rel = relativeAvail(this, ftRoot());
      if (!rel.ok) { showToast(rel.title, { error: true }); return; }
      text = rel.text;
    } else {
      const pl = pathLineText(this.path);
      text = pl.ok ? pl.text : this.path; // copyPath refuses a path with a control character, naming why
    }
    copyPath(text, () => this.focusReader());
  }

  revealInTree() { return revealInTree(this); }

  download() {
    downloadPath(this.path, false, this.box ? boxFileURL(this.box, this.path, true) : undefined);
  }

  // copyContents copies the buffer as it is on screen, an unsaved edit included.
  copyContents() {
    if (!this.model) { showToast('"' + this.name + '" is still loading — try again', { error: true }); return; }
    const text = this.model.getValue();
    if (text === "") { showToast(copyResultMessage({ action: "refuse", reason: "empty" }, this.name, false).text, { error: true }); return; }
    const plan = { action: "copy", text, lines: countLines(text) };
    copyText(text, (ok) => {
      this.focusReader();
      const m = copyResultMessage(plan, this.name, ok);
      showToast(m.text, { error: m.error });
    });
  }
}

