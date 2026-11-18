"use strict";

// IMAGE_EXTS and extOf live in copyfile.js: the copy paths need them too, and a
// pure module there is reachable from `node --test`, which this file is not.
const MD_EXTS = new Set(["md", "markdown"]);
// Rows per block in a big file's read view (renderCode, .vl-chunk in app.css).
const VT_CHUNK = 500;
// A diff over this many characters is shown without syntax highlighting: the
// highlighter runs on the main thread and two passes over a 2 MiB diff would
// freeze the pane. (Measured; see the task report.)
const VD_HL_MAX = 400000;

// ViewerTab renders one file read-only: code (highlight.js), markdown (marked),
// or image (<img>). It is a Cell tab (body-only); the Cell owns the tab bar.
class ViewerTab {
  constructor(path, opts) {
    // Normalise once, here: tabs are keyed and matched by this string, and the
    // paste-path box hands over whatever was typed. "/home/u/./a.txt" would
    // otherwise open a second tab for a file already open, and survive that
    // file's deletion still rendering content that no longer exists.
    if (typeof normalizePath === "function") path = normalizePath(path);
    // A box viewer shows a file that exists only inside one session's box (the
    // Playwright output dir). It reads through boxFileURL, is read-only, and its
    // identity carries the session: two boxes' identically-named screenshots, and
    // a host file at the same path, are different tabs. Same shape as layout.js's
    // `b:` tab key — keep them in step.
    this.box = (opts && typeof opts.box === "string" && opts.box) || "";
    this.id = this.box ? "view:box:" + this.box + ":" + path : "view:" + path;
    this.kind = "viewer";
    this.path = path;
    // The prefix is the operator's cue that this is not a host file (the tree and
    // Edit behave differently, and the path means nothing on the host).
    this.name = (this.box ? "box · " : "") + (path.split("/").pop() || path);
    this.statusText = "";
    this.onUpdate = null;
    this.auto = false;      // auto-reload poll on/off (per viewer, opt-in)
    this.timer = null;
    this.lastContent = null; // last rendered text, to skip no-op re-renders
    this.version = null;    // sha256 of the last-read text, the save's base_version
    this.editing = false;   // true while a CodeMirror editor replaces the read view
    this.readOnly = false;  // the server marked the file read-only (scratch folder, #50)
    this.cm = null;         // the live CodeMirror instance, only while editing
    this.baseVersion = "";  // version this edit started from (what conflict is checked against)
    // Why Copy has nothing to give, or null once there is text. lastContent
    // alone cannot say: load() leaves it untouched for an image, for a binary,
    // for every HTTP error, and for the moment before the first load returns.
    this.copyRefusal = "loading";
    this.showingMsg = false; // true while the pane shows a message, not the file
    // How far down this file was read, in px. It lives here and not in the DOM
    // because every tab switch (Cell.renderBody) and every grid re-render
    // (renderGrid) detaches this element, and an element removed from the
    // document loses its layout box: scrollTop comes back 0. clampScroll is
    // layout.js's, which shares this scope (see the script order in index.html).
    this.scroll = clampScroll(opts && opts.scroll);
    // A tab the layout restore rebuilt, not one anyone clicked. Only such a tab
    // may close itself when its file turns out to be gone (see load).
    this.restored = !!(opts && opts.restored);
    // #53: {base, rel} when this tab came from a relative terminal token. If the
    // first read is a 404, the file may live in another checkout of the repo
    // (findAlternates). Never persisted: a restored tab has none. altPending is
    // true while that lookup is in flight (a poll must not paint over it).
    this.alt = opts && opts.alt && typeof opts.alt.base === "string" && typeof opts.alt.rel === "string" ? opts.alt : null;
    this.altPending = false;
    this.disposed = false;
    // The line (and column) a terminal link such as `x.go:12:5` asked for; the
    // first render consumes it (revealLine). null when the file was opened plainly.
    this.pendingLine = opts && Number.isInteger(opts.line) && opts.line > 0
      ? { line: opts.line, col: Number.isInteger(opts.col) && opts.col > 0 ? opts.col : undefined }
      : null;
    // Read-view bookkeeping for revealLine: whether the pane shows numbered source
    // lines (not markdown / an image / a message), the line `.vl-target` marks, the
    // text that was last rendered (a changed text drops the mark), and a reveal
    // that is waiting for the element to be in the document.
    this.hasLines = false;
    this.lineCount = 0;
    this.targetLine = null;
    this.renderedText = null;
    this.scrollToTarget = false;
    this.firstLoad = true;
    this.applying = false;  // true while applyScroll's own assignment is echoing back
    this.fontPx = null;     // transient live-zoom size (null = follow the global default)
    const ext0 = extOf(path);
    // which CSS var this viewer's text reads (null for images → zoom is a no-op)
    this.zoomVar = IMAGE_EXTS.has(ext0) ? null : (MD_EXTS.has(ext0) ? "--fs-md" : "--fs-code");

    this.el = document.createElement("div");
    this.el.className = "tab-body viewer-body";
    this.content = document.createElement("div");
    this.content.className = "viewer-content";
    // Focusable so a file the user just opened takes the keyboard (arrows, PgDn,
    // Space scroll it) instead of leaving it in the terminal they clicked in.
    this.content.tabIndex = 0;
    // Three things have to be excluded here, because scrollTop cannot say which
    // of them moved it — only this listener knows:
    //
    // 1. isConnected — a scroll event queued just before a detach is dispatched
    //    AFTER it, against an element that now reads 0. Without this, scrolling
    //    and switching tab in the same frame saves the top of the file.
    // 2. showingMsg — msg() replaces the scroller's children with one line, so
    //    the browser CLAMPS the offset to 0 and reports it as a scroll. A poll
    //    that catches a file mid-rewrite ("empty file"), or one failed ⟳, would
    //    otherwise throw away the position the file comes back to.
    // 3. applying — applyScroll's own assignment is echoed back as a scroll
    //    event, and it is clamped when the content is not yet its final height
    //    (markdown whose images have not loaded). Recording the clamp would
    //    ratchet a restored position toward the top on every reload.
    this.content.addEventListener("scroll", () => {
      if (this.applying || this.showingMsg || !this.el.isConnected) return;
      this.scroll = this.content.scrollTop;
      markLayoutUnsaved(); // scrolling schedules no layout write; pagehide needs to know
    }, { passive: true });
    // Find / Go to line. The bar sits BETWEEN the toolbar and the scroller (not
    // inside it), so content.replaceChildren (every render) never removes it, and
    // a later toolbar swap does not have to know about it.
    // One delegated listener for every line number (a file can have 50k rows).
    // Stopping the click keeps filetree.js's document-level closer from shutting
    // the menu it just opened.
    this.content.addEventListener("click", (e) => this.onLineNumClick(e));
    this.content.addEventListener("scroll", () => {
      if (this.lineMenuOpen) { this.lineMenuOpen = false; closeRowMenu(); } // fixed-position: it would leave its number behind
    }, { passive: true });
    this.isImage = IMAGE_EXTS.has(ext0);
    // Changes mode: the pane shows this file's diff vs HEAD instead of the file
    // (spec 5.2). Only a host text file has one. opts.view === "changes" opens
    // straight into it (a tree badge's click, openFile / drawerShowFile).
    this.changes = !!(opts && opts.view === "changes") && !this.isImage && !this.box;
    if (this.changes) this.pendingLine = null; // a diff has no line to reveal
    this.diffSeq = 0;       // a newer diff read overtakes an older one
    this.viewSeq = 0;       // bumped on every switch between the file and its diff (viewReplyPaints)
    this.fileScroll = 0;    // where the file was read, kept while the diff is up
    this.find = new ViewerFind(this);
    this.findBar = this.find.el;
    // The one shared toolbar (viewertoolbar.js). opts.host carries what differs for the Files dock: its shorter
    // path label, and what its tabs hear from this viewer: the dock pins a tab when its first edit starts
    // (onEditStart) and draws its dirty dot when the buffer turns dirty or clean (onDirtyChange, noteDirty).
    // A pane viewer has no host.
    this.host = (opts && opts.host) || null;
    this.dirtyTimer = null;
    this.dirtyShown = false;
    this.toolbarApi = buildViewerToolbar(this, this.host);
    this.toolbar = this.toolbarApi.el;
    // The one-line note over a file opened from another checkout (altOpened). Like
    // the find bar it sits outside the scroller, so no render ever removes it.
    this.noteEl = document.createElement("div");
    this.noteEl.className = "viewer-altnote";
    this.noteEl.hidden = true;
    this.el.append(this.toolbar, this.findBar, this.noteEl, this.content);
    // Bubble phase, on the viewer's own element: it fires for the read scroller,
    // the find bar and CodeMirror's textarea, and never for a terminal or the
    // rest of the page, so Mod+F / Ctrl+G stay the browser's and readline's there.
    // (The global dispatcher in app.js handles only scope "global" entries.)
    this.el.addEventListener("keydown", (e) => this.onViewerKey(e));
    this.load();
  }

  // onViewerKey takes the viewer-scoped keys. A key the viewer cannot act on (an
  // image, rendered markdown for Go to line) is left alone, so the browser's find
  // still works on that page.
  onViewerKey(e) {
    let mode = null;
    if (shortcutMatches("viewer.find", e, IS_MAC)) mode = "find";
    else if (shortcutMatches("viewer.gotoLine", e, IS_MAC)) mode = "line";
    if (mode && this.openFind(mode)) { e.preventDefault(); e.stopPropagation(); return; }
    // Esc closes an open find bar from the reader or the editor too, not only from
    // inside the bar. preventDefault keeps a maximized drawer open (app.js leaves a
    // used Escape alone). With the bar closed Esc is not ours and passes through.
    if (e.key === "Escape" && !mode && this.find.isOpen) { e.preventDefault(); e.stopPropagation(); this.find.close(); }
  }

  // openFind shows the find bar in "find" or "line" (Go to line) mode and says
  // whether it could. Find works on text (code, markdown, the editor); Go to line
  // needs numbered lines: the code view or the editor.
  openFind(mode) {
    if (mode === "line") {
      if (!(this.editing && this.cm) && (!this.hasLines || this.changes)) return false;
    } else if (this.isImage) return false;
    this.find.open(mode);
    return true;
  }

  closeFind() { this.find.close(); }

  // lineTotal: how many lines Go to line accepts (the same count revealLine clamps to).
  lineTotal() { return this.editing && this.cm ? countSourceLines(this.cm.getValue()) : this.lineCount; }

  // revealRange brings a find match into view: its row about a third down the
  // pane when it is off-screen, and the match itself sideways when the lines are
  // not wrapped. `row` is the match's .vl row (or markdown block).
  revealRange(range, row) {
    this.markTarget(null); // a find navigation ends the open-at-line mark (spec D2)
    const c = this.content;
    if (!c.isConnected || !row) return;
    const cr = c.getBoundingClientRect();
    const rr = row.getBoundingClientRect();
    if (rr.top < cr.top || rr.bottom > cr.bottom) {
      this.setScrollTop(Math.max(0, Math.round(rr.top - cr.top + c.scrollTop - c.clientHeight / 3)));
    }
    const m = range.getBoundingClientRect();
    if (m.width) {
      const cr2 = c.getBoundingClientRect();
      if (m.right > cr2.right - 8 || m.left < cr2.left + 8) c.scrollLeft += Math.round(m.left - cr2.left - c.clientWidth / 3);
    }
  }

  // setScrollTop moves the scroller and records where it settled, without the
  // echoing scroll event being read as the reader's (see the scroll listener).
  setScrollTop(y) {
    this.applying = true;
    this.content.scrollTop = y;
    this.scroll = this.content.scrollTop;
    requestAnimationFrame(() => { this.applying = false; });
    markLayoutUnsaved();
  }

  // findRefresh reruns an open find after the view changed under it (a render).
  findRefresh() { if (this.find.isOpen) this.find.refresh(true); }

  // focusReader puts keyboard focus on the scroller. Only user-initiated opens
  // call it (openFile / openBoxFile); a layout restore must not steal focus. A
  // freshly built tab may not be in the document yet (a maximized other pane keeps
  // its cell detached), so when detached it retries once on the next frame.
  focusReader() {
    // Edit mode keeps the editor's focus (spec D8b): a link or tree open of a file
    // being edited must leave the caret where revealLine put it.
    if (this.editing && this.cm) { this.cm.focus(); return; }
    const f = () => { if (this.content.isConnected) this.content.focus({ preventScroll: true }); };
    if (this.content.isConnected) f();
    else requestAnimationFrame(f);
  }

  // --- path actions ----------------------------------------------------------
  // The line-number menu below and Task 7's Path ▾ menu both go through these.

  // insertCandidate: the terminal an insert would go to. A Files dock viewer
  // uses the dock's session (not whichever
  // terminal holds focus); a pane viewer uses the terminal its cell is about.
  // insertTargetFor then applies the box rule.
  insertCandidate() {
    if (typeof dockHasViewer === "function" && dockHasViewer(this)) {
      return drawerState.sessionId ? terminalTabFor(drawerState.sessionId) : null;
    }
    const cell = cells.find((c) => c.tabs.includes(this));
    return cell ? cellTerminal(cell) : null;
  }

  // copyPath("abs"|"rel", line?): copies the absolute path (with :line when
  // given) or the path relative to the tree's root, with copyPath's toast.
  copyPath(kind, line) {
    let text;
    if (kind === "rel") {
      const rel = relativeAvail(this, ftRoot());
      if (!rel.ok) { showToast(rel.title, { error: true }); return; }
      text = rel.text;
    } else {
      const pl = pathLineText(this.path, line);
      text = pl.ok ? pl.text : this.path + (line ? ":" + line : ""); // copyPath refuses it, naming why
    }
    copyPath(text, () => this.focusReader());
  }

  // insertPath(line?): types the shell-quoted path (or path:line) and a space at
  // the target's prompt, never a newline. The target must also pass insertDecision
  // (insertdecision.js): a terminal the user cannot see, or a session waiting on them,
  // refuses with a toast naming why, types nothing and moves no focus (#337).
  insertPath(line) {
    const target = insertTargetFor(this, this.insertCandidate());
    if (!target.ok) { showToast(target.title, { error: true }); return; }
    if (insertRefused(target.term)) return;
    const pl = pathLineText(this.path, line);
    const res = pl.ok ? insertTextAtPrompt(target.term, shellQuote(pl.text) + " ") : { ok: false, reason: pl.reason };
    if (res.ok) { if (target.term.term) target.term.term.focus(); return; }
    showToast(
      res.reason === "unsafe" ? "That path contains control characters, so it was not inserted" :
      res.reason === "noterm" ? "This session has no terminal open here, so the path was not inserted" :
      "That session is not connected, so the path was not inserted",
      { error: true });
    this.focusReader();
  }

  // revealInTree: app.js opens the tree (drawer, or the Files screen on mobile)
  // and ftReveal expands down to this file.
  revealInTree() { return revealInTree(this); }

  // onLineNumClick opens the path:line menu for the number that was clicked.
  onLineNumClick(e) {
    const num = e.target.closest && e.target.closest(".vl-n");
    if (!num || !this.content.contains(num)) return;
    const line = Number(num.parentElement.dataset.n);
    if (!line) return;
    e.stopPropagation();
    this.openLineMenu(num, line);
  }

  openLineMenu(anchor, line) {
    const label = "path:" + line;
    const head = document.createElement("div");
    head.className = "ft-menu-path";
    head.textContent = shortPath(this.path) + ":" + line;
    const copy = document.createElement("button");
    copy.type = "button";
    copy.textContent = "Copy " + label;
    copy.onclick = () => { this.lineMenuOpen = false; closeRowMenu(); this.copyPath("abs", line); };
    const nodes = [head, copy];
    // Not on the phone (insertOffered): this menu is only reached from a pane viewer there, which covers its own terminal, so
    // insertDecision would refuse every press.
    if (insertOffered()) {
      const ins = document.createElement("button");
      ins.type = "button";
      ins.textContent = "Insert " + label + " at prompt";
      const target = insertTargetFor(this, this.insertCandidate());
      if (target.ok) ins.onclick = () => { this.lineMenuOpen = false; closeRowMenu(); this.insertPath(line); };
      else { ins.disabled = true; ins.title = target.title; }
      nodes.push(ins);
    }
    this.lineMenuOpen = true;
    ftShowMenu(anchor, nodes, true);
  }

  title() { return this.name; }
  status() { return this.statusText; }
  onShow() {}
  onHide() { this.toolbarApi.closeMenus(); } // a menu belongs to the pane that is on screen
  // onAttach: the element was just (re)inserted, so the browser has reset the
  // scroller to the top. Idempotent and NON-CONSUMING — it never clears
  // this.scroll and never reads the applied value back — because it also fires
  // on elements that are still detached: openFile's early return activates a
  // tab in a cell that is out of the document while another pane is maximized,
  // where assigning scrollTop is a silent no-op and the next real attach is
  // what has to restore.
  onAttach() {
    this.applyScroll();
    if (this.scrollToTarget) this.scrollTargetIntoView(); // a reveal that came while detached
  }
  // `applying` is cleared on the next frame, not synchronously: a scroll event
  // caused by this assignment is dispatched during that frame's scroll steps,
  // which run BEFORE animation-frame callbacks. Clearing it any sooner would
  // let the echo through.
  applyScroll() {
    if (this.content.scrollTop === this.scroll) return;
    this.applying = true;
    this.content.scrollTop = this.scroll;
    requestAnimationFrame(() => { this.applying = false; });
  }
  dispose() {
    this.disposed = true; // a lookup still in flight must not touch a closed tab
    if (this.timer) clearInterval(this.timer); // stop polling on close (no leak)
    this.toolbarApi.closeMenus();
    if (this.lineMenuOpen) { this.lineMenuOpen = false; closeRowMenu(); } // its buttons would act on a closed viewer
    this.find.close(true); // frees the document-wide highlight names if this bar held them
  }
  // The toolbar's actions as methods (viewertoolbar.js calls these).
  reload() { return this.load(); }
  download() {
    downloadPath(this.path, false, this.box ? boxFileURL(this.box, this.path, true) : undefined);
  }
  copy(btn) { this.copyContents(btn || null); }
  fontStep(d) { this.zoom(d * fontPrefs.STEP); }
  toggleAuto() {
    if (this.changes) return; // the diff is not re-read on a timer: each read starts a container
    this.auto = !this.auto;
    if (this.auto) this.timer = setInterval(() => this.load(true), 2000);
    else { clearInterval(this.timer); this.timer = null; }
    this.changed();
  }
  // Live zoom (transient): override the pane-local CSS var so only this viewer's
  // text resizes; the var lives on this.el, an ancestor of the rendered content,
  // so it survives re-renders (reload / auto-reload) automatically. Not persisted.
  zoom(delta) {
    if (!this.zoomVar) return;
    const base = this.fontPx ?? fontPrefs.get(this.zoomVar === "--fs-md" ? "markdown" : "code");
    this.fontPx = fontPrefs.clamp(base + delta);
    this.el.style.setProperty(this.zoomVar, this.fontPx + "px");
  }
  resetZoom() {
    if (!this.zoomVar) return;
    this.fontPx = null;
    this.el.style.removeProperty(this.zoomVar);
  }
  setStatus(t) { this.statusText = t; this.changed(); }
  // changed repaints the toolbar and tells the host (a pane's tab bar, the
  // drawer header). Call it whenever something the toolbar shows moved.
  changed() { this.toolbarApi?.paint(); this.onUpdate?.(); }

  // showingMsg tracks whether the pane holds a MESSAGE rather than the file, so
  // load() can tell "nothing changed, skip the re-render" from "the text is the
  // same but an error is still on screen over it".
  msg(text, cls) {
    const d = document.createElement("div");
    d.className = "viewer-msg" + (cls ? " " + cls : "");
    d.textContent = text;
    this.content.replaceChildren(d);
    this.showingMsg = true;
    this.hasLines = false;
    this.findRefresh();
  }

  // --- Changes vs HEAD (spec 5.2) ---------------------------------------------

  // toggleChanges flips this tab between the file and its diff vs HEAD.
  toggleChanges() { return this.changes ? this.leaveChanges() : this.showChanges(); }

  // showChanges puts the diff on screen (or re-reads it when it already is: a
  // badge click after the file changed again). Says whether it could.
  showChanges() {
    if (this.isImage || this.box) return false;
    if (this.editing) { showToast("Save or cancel your edit first"); return false; }
    if (!this.changes) {
      this.changes = true;
      this.pendingLine = null;
      if (this.auto) this.toggleAutoOff();
      this.viewSeq++; // a file read still in flight must not paint over the diff
      this.fileScroll = this.scroll;
      this.scroll = 0;
      this.resetForOtherView();
    }
    this.loadDiff();
    return true;
  }

  // leaveChanges goes back to the file, re-read from disk.
  leaveChanges() {
    if (!this.changes) return;
    this.changes = false;
    this.viewSeq++; // a diff still in flight must not paint over the file
    this.scroll = this.fileScroll;
    this.resetForOtherView();
    this.load();
  }

  toggleAutoOff() {
    this.auto = false;
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  // resetForOtherView forgets what the other view showed, so nothing (Copy, the
  // unchanged-text check in load, the line mark) reads the wrong text.
  resetForOtherView() {
    this.lastContent = null;
    this.renderedText = null;
    this.copyRefusal = "loading";
    this.targetLine = null;
    this.changed();
  }

  // loadDiff fetches this file's diff vs HEAD and renders it (or the sentence for
  // a state that has no rows). The server answers 200 for every git outcome, in
  // `state`/`kind`; a non-OK status is the request itself failing.
  async loadDiff(poll) {
    if (poll || !this.changes || this.editing) return;
    const seq = ++this.diffSeq;
    const vseq = this.viewSeq;
    this.setStatus("loading…");
    const res = await fsPost("/api/git/diff", { path: this.path });
    // Overtaken: a newer diff read, or a switch back to the file, owns the pane.
    if (seq !== this.diffSeq || !viewReplyPaints(vseq, this.viewSeq, true, this.changes)) return;
    const o = diffOutcome(res);
    if (o.view === "msg") this.diffMessage(o.text, o.cls, o.refusal, o.back);
    else this.renderDiff(o.rows, o.diff, o.truncated);
    this.applyScroll();
    // Last, after the render has set copyRefusal: setStatus repaints the toolbar,
    // and Copy's enabled state reads copyRefusal (a repaint before it left Copy
    // disabled as "still loading").
    this.setStatus("");
  }

  // diffHeader: the strip that says what the pane shows, so a screenshot is
  // unambiguous, plus the banner when the server cut the diff short.
  diffHeader(truncated) {
    const bar = document.createElement("div");
    bar.className = "vd-bar";
    const h = document.createElement("span");
    h.className = "vd-title";
    h.textContent = "Changes vs HEAD";
    bar.append(h);
    if (truncated) {
      const t = document.createElement("span");
      t.className = "vd-trunc";
      t.textContent = "Diff cut at 2 MiB: only the start is shown";
      bar.append(t);
    }
    return bar;
  }

  // diffMessage shows a state with no rows under the header. `refusal` is the
  // copyRefusal that fits it; `back` adds the button to the file.
  diffMessage(text, cls, refusal, back) {
    const d = document.createElement("div");
    d.className = "viewer-msg" + (cls ? " " + cls : "");
    d.append(text);
    if (back) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "viewer-auto vd-back";
      b.textContent = "Show file";
      b.onclick = () => this.leaveChanges();
      d.append(" ", b);
    }
    this.content.replaceChildren(this.diffHeader(false), d);
    this.showingMsg = true;
    this.hasLines = false;
    this.lastContent = null;
    this.copyRefusal = refusal;
    this.findRefresh();
  }

  // renderDiff draws the rows through the read view's machinery: .vl rows, the
  // gutter, the 500-row blocks. The gutter is the NEW-file line (blank on a removed
  // row), so the line-number menu still points at a real line; a sign column says
  // + or − so colour is not the only cue. Code is highlighted in the file's
  // language (two streams, old side and new side: viewerdiff.js diffRowHtml).
  renderDiff(rows, text, truncated) {
    this.showingMsg = false;
    const lang = this.lang || extOf(this.path);
    const hl = window.hljs && lang && hljs.getLanguage(lang) && text.length <= VD_HL_MAX
      ? (src) => hljs.highlight(src, { language: lang }).value
      : null;
    const html = diffRowHtml(rows, hl, escapeHtml);
    const maxLine = diffMaxLine(rows);
    const pre = this.buildPre(rows.length, Math.max(3, String(maxLine).length), (i) => {
      const r = rows[i];
      const row = document.createElement("div");
      row.className = "vl vd-" + r.t;
      row.dataset.i = String(i);
      if (r.n !== null) row.dataset.n = String(r.n);
      const num = document.createElement("span");
      num.className = "vl-n";
      num.setAttribute("aria-hidden", "true");
      num.textContent = r.n === null ? "" : String(r.n);
      const sign = document.createElement("span");
      sign.className = "vd-sign";
      sign.textContent = diffSign(r.t);
      const code = document.createElement("span");
      code.className = "vl-c";
      code.innerHTML = html[i];
      row.append(num, sign, code);
      return row;
    });
    this.content.replaceChildren(this.diffHeader(truncated), pre);
    this.lineCount = maxLine;
    this.hasLines = true;
    this.targetLine = null;
    this.renderedText = null;
    this.lastContent = text;
    this.copyRefusal = null;
    this.findRefresh();
  }

  // buildPre makes the .viewer-pre of `count` rows, each made by makeRow(i), laid
  // out in VT_CHUNK-row blocks when there are many (renderCode and renderDiff).
  buildPre(count, digits, makeRow) {
    const pre = document.createElement("pre");
    pre.className = "viewer-pre hljs";
    pre.style.setProperty("--vl-digits", String(digits));
    const frag = document.createDocumentFragment();
    // A big file is laid out in blocks of VT_CHUNK rows, each skipped by the
    // browser until it nears the viewport (content-visibility, app.css).
    const chunked = count > VT_CHUNK * 2;
    let chunk = null;
    for (let i = 0; i < count; i++) {
      const row = makeRow(i);
      if (chunked) {
        if (i % VT_CHUNK === 0) {
          chunk = document.createElement("div");
          chunk.className = "vl-chunk";
          // The last block is shorter: say so, or its skipped height is overstated.
          chunk.style.setProperty("--vl-rows", String(Math.min(VT_CHUNK, count - i)));
          frag.append(chunk);
        }
        chunk.append(row);
      } else frag.append(row);
    }
    pre.append(frag);
    return pre;
  }

  // load fetches + renders the file. `poll` (auto-reload tick) suppresses status
  // churn and skips the re-render when the content is unchanged. Scroll is no
  // longer its business: every render path ends in applyScroll().
  async load(poll) {
    // A poll tick or the manual ⟳ must never silently replace a live edit: the
    // success path below ends in renderCode()/renderMarkdown(), both of which
    // call content.replaceChildren(...) and would tear the CodeMirror editor +
    // its Save/Cancel bar out from under a mid-keystroke user with no warning —
    // and, since only exitEdit() resets editing/cm, leave both pointing at a
    // detached editor forever (the Edit button's own guard then permanently
    // no-ops, bricking the tab).
    if (this.editing) {
      if (poll) return; // background tick: leave the live edit untouched, full stop
      // Manual reload: don't discard silently. Ask only when there is
      // something to lose; either way, route through exitEdit() — the only
      // place that resets editing/cm/editBar — which itself reloads from disk
      // once it has cleared edit state.
      if (this.dirty() && !(await askConfirm({
        title: "Discard unsaved changes?",
        body: "Reloading " + this.name + " will discard your unsaved edits.",
        confirmLabel: "Discard & reload",
        danger: true,
      }))) return;
      this.exitEdit();
      return;
    }
    if (this.changes) return this.loadDiff(poll); // Reload in Changes mode re-reads the diff
    // Claimed synchronously so an auto-reload tick that overlaps the first read
    // cannot also count as "first".
    const first = this.firstLoad;
    this.firstLoad = false;
    const vseq = this.viewSeq;
    const ext = extOf(this.path);
    if (IMAGE_EXTS.has(ext)) {
      // Stated, not implied. Copy has no button on an image today, so this is
      // unreachable — but leaving the field at "loading" forever makes the
      // button's absence load-bearing, and IMAGE_EXTS is exactly the kind of
      // set that changes (see the SVG note in the design).
      this.copyRefusal = "image";
      if (!poll) { this.renderImage(); this.setStatus(""); }
      return;
    }
    if (!poll) this.setStatus("loading…");
    const res = await fsRead(this.path, this.box ? boxFileURL(this.box, this.path) : undefined);
    // Changes was clicked while this read (or a Follow tick) was in flight: the pane
    // shows the diff now, and this reply would paint the file over it and overwrite
    // lastContent / copyRefusal.
    if (!viewReplyPaints(vseq, this.viewSeq, false, this.changes)) return;
    // A fallback lookup is in flight: its answer owns the pane (a poll tick that
    // lands now would paint "not found" over it, or re-run the lookup).
    if (this.altPending) return;
    // The alternates lookup is for the FIRST read only: a file that is deleted
    // later is a deletion, not a path that was never here. Consumed either way.
    const alt = first ? this.alt : null;
    this.alt = null;
    // The message paths below drop pendingLine; a fallback to another checkout
    // still wants the line the link asked for.
    const altLine = alt ? this.pendingLine : null;
    // Only a successful read answers the question; a dropped poll keeps the
    // last known answer rather than bringing back an Edit that cannot save.
    if (res.ok) {
      const ro = readOnlyFrom(res);
      if (ro !== this.readOnly) { this.readOnly = ro; this.changed(); } // Edit follows readOnly
    }
    const plan = copyContentPlan(res);
    if (plan.action !== "copy") {
      // A FAILED poll changes nothing. The screen still holds the text it had,
      // so a dropped tick must not make Copy refuse what the user is looking at
      // over an error the viewer never displayed.
      //
      // A SUCCESSFUL one does. `empty` and `binary` are definitive answers about
      // the file, not transport failures: truncating a log you are watching is
      // the most ordinary thing that happens to it, and silently keeping the old
      // text would have Copy hand over content the file no longer has.
      if (poll && !res.ok) return;
      this.pendingLine = null; // there is no line to show in a message
      this.copyRefusal = plan.reason;
      if (plan.reason === "empty") {
        // A successful read of a file with nothing in it is not an error.
        if (poll && this.lastContent === "") return; // already shown; don't churn
        this.lastContent = "";
        this.version = res.data && res.data.version;
        this.lang = res.data && res.data.lang;
        this.msg("empty file");
        this.setStatus("");
      } else if (plan.reason === "binary") {
        // null, not the old text: leaving it would make the unchanged-check
        // below suppress the re-render if the file turns back into that text.
        if (poll && this.lastContent === null) return; // already shown
        this.lastContent = null;
        this.msg(`binary file · ${(res.data && res.data.size) || 0} bytes`);
        this.setStatus("");
      } else {
        // A restored viewer was not asked for by anyone. If its very FIRST read
        // says the file is gone, drop the tab instead of parking a red error on
        // a path that no longer exists. Exactly 404 and nothing else: status 0
        // is "the fetch never completed" (fsops.js), which the rule about failed
        // polls says is not an answer about the file; and a 404 later in the
        // tab's life is a file deleted while open, which closeViewersFor already
        // handles for in-app deletes and which the user should see otherwise.
        //
        // Images never reach here — load() returns before any fsRead for them
        // (see IMAGE_EXTS above) and img.onerror cannot tell 404 from a blip —
        // so a restored image of a deleted file keeps its "could not load
        // image" message and is closed by hand.
        if (first && this.restored && res.status === 404) { closeTabEverywhere(this); return; }
        // #53: a relative terminal link that is not here may be in another
        // checkout. A restored tab never has `alt`, so the auto-close above is
        // never pre-empted by a lookup, and a tab with `alt` is never restored.
        if (alt && res.status === 404 && !this.box) { this.findAlternates(alt, vseq, res, altLine); return; }
        this.showReadError(res);
      }
      return;
    }
    this.copyRefusal = null;
    if (!poll) this.setStatus("");
    // Unchanged text still needs a re-render when a message is covering it:
    // a file that went missing and came back identical otherwise leaves a red
    // "not found" on screen while Copy quietly succeeds behind it.
    if (poll && plan.text === this.lastContent && !this.showingMsg) return;
    if (this.showingMsg) this.setStatus(""); // clear the error the message left
    this.lastContent = plan.text;
    this.version = res.data && res.data.version;
    this.lang = res.data && res.data.lang;
    if (MD_EXTS.has(ext)) this.renderMarkdown(plan.text);
    else this.renderCode(plan.text, res.data.lang);
    // Replacing the scroller's children resets it. The field is already current
    // (the listener keeps it so), and this is also the path that lands a
    // restored offset, since the first read completes long after the attach.
    this.applyScroll();
    // A terminal link asked for a line: it wins over the restored scroll.
    if (this.pendingLine) {
      const p = this.pendingLine;
      this.pendingLine = null;
      this.revealLine(p.line, p.col);
    }
  }

  // showReadError paints a failed read. The server's own sentence, not the
  // clipboard's. This pane is the RENDER failure surface: a 5 MB log opened here
  // must read "file too large to preview", not "too large to copy — use Download
  // instead".
  showReadError(res) {
    this.msg(
      (res.data && res.data.error) || (res.status ? "HTTP " + res.status : "could not reach the server"),
      "err");
    this.setStatus("error");
  }

  // findAlternates (#53): the first read of a relative terminal link was a 404.
  // Look for the same repo-relative path in the repo's other checkouts. None: the
  // ordinary not-found. One: open it, with a note saying where from. Several: an
  // in-pane list to choose from. `res` is the failed read, for the none case.
  async findAlternates(alt, vseq, res, line) {
    this.altPending = true;
    const list = await fsAlternates(alt.base, alt.rel);
    this.altPending = false;
    // Closed, or the pane moved on (Changes was clicked): nothing here is ours to paint.
    if (this.disposed || !viewReplyPaints(vseq, this.viewSeq, false, this.changes)) return;
    const o = alternatesOutcome(list);
    if (o.kind === "one") { this.openAlternate(o.path, o.worktree, line); return; }
    if (o.kind === "pick") { this.renderAltPick(o.items, alt, line); return; }
    this.showReadError(res);
  }

  // openAlternate re-points this tab at a file in another checkout and reads it.
  // `line` ({line, col} or null) is what the link asked for: it goes back into
  // pendingLine, which the read consumes. A tab already showing that file wins
  // and this one closes (two tabs on one path would be stored twice and collapse
  // at the next boot); repointViewer opens it at the same line.
  openAlternate(path, worktree, line) {
    if (typeof repointViewer === "function" && !repointViewer(this, path, line)) return;
    this.pendingLine = line || null;
    this.setAltNote(worktree);
    this.setStatus("loading…");
    this.load();
  }

  // setAltNote shows where the file was opened from; null clears it (altNoteNodes draws it).
  setAltNote(worktree) {
    if (worktree === null) { this.noteEl.hidden = true; this.noteEl.replaceChildren(); return; }
    this.noteEl.replaceChildren(...altNoteNodes(worktree));
    this.noteEl.hidden = false;
  }

  // renderAltPick: several other checkouts hold the file. A list in the pane, not
  // a modal: it works on a phone and cannot collide with #ask-modal's single slot.
  renderAltPick(items, alt, line) {
    this.content.replaceChildren(altPickNode(items, alt, (it) => this.openAlternate(it.path, it.worktree, line)));
    this.showingMsg = true;
    this.hasLines = false;
    this.lastContent = null;
    this.findRefresh();
    this.setStatus("");
  }

  // revealLine shows the 1-based `line` (and, in the editor, column `col`) of
  // this file: the read view marks the line and scrolls it about a third of the
  // way down; edit mode moves the cursor there. A line past the end is clamped to
  // the last one, with a toast saying so. Markdown is rendered, not numbered, so
  // it says that instead; an image, a binary or a message has no lines (no-op).
  revealLine(line, col) {
    if (this.editing && this.cm) {
      // The same count as the read view: CodeMirror's lineCount() adds the empty
      // line after a trailing newline, which is not a line of the file.
      const total = countSourceLines(this.cm.getValue());
      const c = clampLine(line, total);
      if (c.clamped) showToast(linesOnlyText(this.name, total));
      this.cm.setCursor({ line: c.line - 1, ch: (col > 0 ? col : 1) - 1 });
      this.cm.scrollIntoView(null, 100);
      this.cm.focus();
      return;
    }
    if (this.changes) return; // a diff's gutter is not the file's line numbers: nothing to reveal
    if (this.copyRefusal === "loading") { // the first read has not landed: ask again then
      this.pendingLine = { line, col };
      return;
    }
    if (!this.hasLines) {
      if (this.copyRefusal === null && MD_EXTS.has(extOf(this.path))) {
        showToast("Line numbers apply to source files — markdown opens rendered");
      }
      return;
    }
    const c = clampLine(line, this.lineCount);
    if (c.clamped) showToast(linesOnlyText(this.name, this.lineCount));
    this.markTarget(c.line);
    this.scrollToTarget = true;
    if (this.content.isConnected) this.scrollTargetIntoView();
  }

  // markTarget moves the .vl-target mark to line n (1-based), or removes it.
  markTarget(n) {
    this.targetLine = n;
    const old = this.content.querySelector(".vl-target");
    if (old) old.classList.remove("vl-target");
    const row = n && this.content.querySelector('.vl[data-n="' + n + '"]');
    if (row) row.classList.add("vl-target");
  }

  // scrollTargetIntoView puts the marked line about a third of the way down the
  // pane. this.scroll takes the value the browser settled on (it clamps near the
  // end), and `applying` keeps the echoing scroll event from being read as a
  // reader's scroll.
  scrollTargetIntoView() {
    const el = this.targetLine && this.content.querySelector(".vl-target");
    if (!el || !this.content.isConnected) return;
    this.scrollToTarget = false;
    const top = el.getBoundingClientRect().top - this.content.getBoundingClientRect().top + this.content.scrollTop;
    this.setScrollTop(Math.max(0, Math.round(top - this.content.clientHeight / 3)));
  }

  // Copies exactly what is on screen — the raw source this viewer fetched, which
  // for markdown is the markdown and not the rendered HTML. Focus returns to the
  // button: copyText's textarea fallback leaves it on <body>, and pulling it into
  // the terminal pane is not what someone working in this toolbar asked for.
  // `btn` may be null (a caller with no button of its own): focus stays put.
  copyContents(btn) {
    const plan = this.copyRefusal
      ? { action: "refuse", reason: this.copyRefusal }
      : { action: "copy", text: this.lastContent, lines: countLines(this.lastContent) };
    if (plan.action === "refuse") {
      const m = copyResultMessage(plan, this.name, false);
      btn?.focus(); // a refusal leaves focus where a success would, not on nothing
      showToast(m.text, { error: m.error });
      return;
    }
    const diff = this.changes; // what is on screen is the diff: say so, not "Copied the file"
    copyText(plan.text, (ok) => {
      btn?.focus();
      const m = diff
        ? (ok
          ? { text: 'Copied the changes to "' + this.name + '" (' + plan.lines + " line" + (plan.lines === 1 ? "" : "s") + " of diff)", error: false }
          : { text: 'Could not copy the changes to "' + this.name + '" to the clipboard — try again', error: true })
        : copyResultMessage(plan, this.name, ok);
      showToast(m.text, { error: m.error });
    });
  }

  // --- edit mode -------------------------------------------------------------
  // Swaps the read view for a CodeMirror 5 editor. Entered only from the Edit
  // button (text files, and only once a load has actually resolved — copyRefusal
  // starts at "loading" and stays truthy for images/binaries/errors, which keeps
  // those non-editable; "empty" is the one refusal reason that is still editable,
  // since Step 1 above seeds an empty buffer with a real version).
  enterEdit() {
    if (this.editing || this.box || this.readOnly) return; // a box file is read-only (no button either; this guards a stray caller)
    if (this.changes) return; // a diff is read-only too
    if (this.copyRefusal && this.copyRefusal !== "empty") return; // image/binary/loading/error stay non-editable
    // The vendor bundle can fail to reach the browser (blocked, offline, a
    // hiccup loading /vendor/codemirror.min.js). Checked before any state
    // change — mirrors renderMarkdown()'s fallback-before-mutating pattern —
    // so a failed attempt leaves editing/cm/the read view exactly as they
    // were, and the Edit button clickable again on the next try.
    if (!window.CodeMirror) {
      showToast("Editor unavailable — CodeMirror failed to load", { error: true });
      return;
    }
    this.editing = true;
    this.baseVersion = this.version || "";
    const holder = document.createElement("div");
    holder.className = "viewer-edit";
    // The save is guarded (sha256 base version), but the agent beside this
    // editor may write the file meanwhile: say so before the conflict does.
    // Neutral, never amber or red (D23). Hidden on mobile (app.css).
    const warn = document.createElement("div");
    warn.className = "edit-warn";
    warn.textContent = "The agent may change this file again. Saving checks it has not changed since you opened it.";
    this.content.replaceChildren(warn, holder);
    this.showingMsg = false;
    let mode = "text/plain";
    if (window.CodeMirror) {
      mode = resolveEditorMode(this.path)
        || (CodeMirror.findModeByExtension && CodeMirror.findModeByExtension(extOf(this.path)) || {}).mode
        || "text/plain";
    }
    this.cm = CodeMirror(holder, {
      value: this.lastContent || "",
      mode: mode,
      lineNumbers: true,
      lineWrapping: viewerPrefs.getWrap(),
      autofocus: true,
    });
    // Returning false stops CodeMirror's own key handling from also inserting
    // whatever the browser would (nothing, normally, but this is belt-and-braces).
    const saveKeys = {};
    for (const k of cmKeys("editor.save", IS_MAC)) saveKeys[k] = () => { this.save(false); return false; };
    this.cm.setOption("extraKeys", saveKeys);
    this.renderEditBar();
    this.findRefresh(); // an open find now searches the buffer
    this.cm.on("change", () => this.noteDirty());
    // A fresh edit: a "saved" left from the last one says nothing about this
    // buffer. (setStatus repaints the toolbar: Save · Cancel replace Edit.)
    this.setStatus("");
    // Last, once the editor is up: the dock keeps this tab from here on (see the host's onEditStart).
    if (this.host && this.host.onEditStart) this.host.onEditStart(this);
  }

  // noteDirty tells the host when the buffer turned dirty or clean, for the dock tab's dot. It runs on every change,
  // so it only starts a short timer: dirty() reads the whole buffer, which on a large file is not a thing to do per
  // keystroke. The dot may lag a moment; every gate asks dirty() itself when it decides. A pane viewer has no host.
  noteDirty() {
    if (this.dirtyTimer || !this.host || !this.host.onDirtyChange) return;
    this.dirtyTimer = setTimeout(() => {
      this.dirtyTimer = null;
      if (this.disposed) return;
      const d = !!this.dirty();
      if (d === this.dirtyShown) return;
      this.dirtyShown = d;
      this.host.onDirtyChange(this);
    }, 150);
  }

  // renderEditBar builds the message row above the editor: Save and Cancel live
  // in the toolbar. The row stays empty (and hidden, app.css) until showConflict()
  // fills it with the conflict message and adds Reload / Overwrite anyway. It is
  // prepended inside `content` (not inside holder itself) so CodeMirror's own
  // sizing is undisturbed.
  renderEditBar() {
    const bar = document.createElement("div");
    bar.className = "viewer-editbar";
    const msg = document.createElement("span");
    msg.className = "viewer-edit-msg";
    bar.append(msg);
    this.editBar = bar;
    this.editMsg = msg;
    this.conflictBtns = null;
    this.content.prepend(bar);
  }

  // showConflict renders once the server has told us the file moved out from
  // under this edit (409). It never touches the editor's text — the user's
  // draft stays exactly as typed until they pick Reload or Overwrite anyway.
  showConflict() {
    if (!this.editBar) return;
    this.editMsg.textContent = "File changed on disk since you started editing.";
    this.editMsg.classList.add("err");
    // Idempotent: a second conflict (an Overwrite that itself races another
    // writer) must replace the pair, not stack a second one behind it.
    if (this.conflictBtns) this.conflictBtns.forEach((b) => b.remove());
    const reload = document.createElement("button");
    reload.className = "viewer-auto";
    reload.textContent = "Reload";
    reload.title = "Discard your edits and load the current file";
    reload.onclick = () => this.exitEdit();
    const overwrite = document.createElement("button");
    overwrite.className = "viewer-auto";
    overwrite.textContent = "Overwrite anyway";
    overwrite.title = "Save your edits, replacing what's on disk";
    overwrite.onclick = () => this.save(true);
    this.conflictBtns = [reload, overwrite];
    this.editBar.append(reload, overwrite);
  }

  async save(overwrite) {
    if (!this.editing) return;
    const content = this.cm.getValue();
    const res = await fsWrite(this.path, saveBody(this.path, content, this.baseVersion, overwrite));
    const decision = classifySave(res);
    if (decision.outcome === "saved") {
      this.baseVersion = decision.version || "";
      this.version = this.baseVersion;
      this.setStatus("saved");
      if (typeof gitStatusRefreshSoon === "function") gitStatusRefreshSoon(); // the file's git mark may have changed
      this.exitEdit(content); // back to read view showing the saved text
    } else if (decision.outcome === "conflict") {
      this.baseVersion = decision.currentVersion || this.baseVersion;
      this.showConflict(); // offer Reload / Overwrite anyway
    } else {
      this.setStatus("error");
      showToast(decision.message, { error: true });
    }
  }

  // exitEdit leaves edit mode. With text, that text becomes the new read view
  // (a save, no re-fetch needed); with nothing, it's a discard and load()
  // re-fetches from disk — the one path that also recovers from a Reload after
  // a conflict, since the disk copy is exactly what a fresh load() shows.
  exitEdit(savedText) {
    this.find.leaveEdit(); // the editor's marks go with the editor
    this.dirtyShown = false; // the dot goes with the edit (changed() below repaints the host)
    this.editing = false;
    this.cm = null;
    this.editBar = null;
    this.editMsg = null;
    this.conflictBtns = null;
    this.changed(); // back to the read view's actions
    if (typeof savedText === "string") {
      this.lastContent = savedText;
      this.renderCode(savedText, this.lang);
      this.applyScroll();
    } else {
      this.load();
    }
  }

  async cancelEdit() {
    if (this.dirty() && !(await askConfirm({
      title: "Discard unsaved changes?",
      body: "Your edits to " + this.name + " have not been saved.",
      confirmLabel: "Discard",
      danger: true,
    }))) return;
    this.exitEdit();
  }

  dirty() { return this.editing && this.cm && this.cm.getValue() !== this.lastContent; }

  renderCode(text, lang) {
    this.showingMsg = false;
    let html;
    try {
      html = (window.hljs && hljs.getLanguage(lang))
        ? hljs.highlight(text, { language: lang }).value
        : (window.hljs ? hljs.highlightAuto(text).value : escapeHtml(text));
    } catch (_) { html = escapeHtml(text); }
    // One div per source line: a number (textContent, aria-hidden, not selectable)
    // and the highlighted code (hljs output only, which escapes the file's own
    // markup). The container carries hljs, the class its theme colours by.
    const lines = splitHighlightedLines(html);
    const pre = this.buildPre(lines.length, Math.max(3, String(lines.length).length), (i) => {
      const row = document.createElement("div");
      row.className = "vl";
      row.dataset.n = String(i + 1);
      const num = document.createElement("span");
      num.className = "vl-n";
      num.setAttribute("aria-hidden", "true");
      num.textContent = String(i + 1);
      const code = document.createElement("span");
      code.className = "vl-c";
      code.innerHTML = lines[i];
      row.append(num, code);
      return row;
    });
    this.content.replaceChildren(pre);
    this.lineCount = lines.length;
    this.hasLines = true;
    // The mark belongs to the text it was set on: a changed file drops it, an
    // unchanged one (the same text rendered again) keeps it.
    if (this.renderedText !== text) this.targetLine = null;
    this.renderedText = text;
    if (this.targetLine && this.targetLine <= this.lineCount) this.markTarget(this.targetLine);
    else this.targetLine = null;
    this.findRefresh();
  }

  renderMarkdown(text) {
    this.showingMsg = false;
    const dir = this.path.slice(0, this.path.lastIndexOf("/") + 1);
    // Markdown can be semi-untrusted (agent-written reports, cloned-repo READMEs),
    // so require DOMPurify to render HTML; without it, fall back to plain code.
    if (!(window.marked && marked.parse) || !window.DOMPurify) { this.renderCode(text, "markdown"); return; }
    let frag;
    try {
      marked.setOptions({ breaks: false, gfm: true }); // NOTE: marked v12 removed the `highlight` option
      // DOMPurify handles every XSS vector robustly (javascript:/data: schemes incl.
      // control-char obfuscation, mXSS, on* handlers, svg/math): a hand-rolled
      // strip-and-reserialize sanitizer is not safe against these.
      frag = DOMPurify.sanitize(marked.parse(text), { RETURN_DOM_FRAGMENT: true });
    } catch (_) { this.renderCode(text, "markdown"); return; }
    // rewrite relative <img src> against the file's dir BEFORE attaching (no wasted 404 fetch)
    frag.querySelectorAll("img").forEach((img) => {
      const src = img.getAttribute("src") || "";
      if (!/^(https?:|data:|\/)/.test(src)) {
        img.setAttribute("src", this.box
          ? boxFileURL(this.box, dir + src)
          : "/api/fs/read?path=" + encodeURIComponent(dir + src));
      }
      // An <img> here reserves no height until it loads, so the document is
      // shorter than its final self when load() applies the offset and the
      // browser clamps it. Re-apply as each image settles. Safe to repeat: if
      // the reader has scrolled since, this.scroll is already where they are and
      // applyScroll does nothing.
      img.addEventListener("load", () => this.applyScroll());
    });
    // marked v12 dropped the highlight option: highlight fenced blocks here (hljs v11 API).
    if (window.hljs) frag.querySelectorAll("pre code").forEach((el) => { try { hljs.highlightElement(el); } catch (_) {} });
    const wrap = document.createElement("div");
    wrap.className = "md-body";
    wrap.appendChild(frag);
    this.content.replaceChildren(wrap);
    this.hasLines = false;
    this.findRefresh();
  }

  // --- wrap lines (D9) -------------------------------------------------------
  // The read view wraps by the viewer-wrap class on <html> (CSS only, no re-render);
  // an editor wraps by CodeMirror's lineWrapping option. wrapAnchor() is called
  // BEFORE the preference flips and returns the function that, called after,
  // puts the reader back on the same line: a wrapped row is taller, so the same
  // pixel offset would be somewhere else in the file.
  wrapAnchor() {
    const none = () => {};
    if (this.editing && this.cm) {
      const cm = this.cm;
      const top = cm.getScrollInfo().top;
      const line = cm.lineAtHeight(top, "local");
      const off = top - cm.heightAtLine(line, "local");
      return () => cm.scrollTo(null, cm.heightAtLine(line, "local") + off);
    }
    // A hidden tab has no layout to measure, so it keeps its pixel scroll offset
    // (which may map to a different line once wrapping changes row heights).
    if (!this.hasLines || !this.content.isConnected) return none;
    const row = this.firstVisibleRow();
    if (!row) return none;
    // A diff row may have no line number (a removed line); its index always exists.
    const sel = row.dataset.i !== undefined ? '.vl[data-i="' + row.dataset.i + '"]' : '.vl[data-n="' + row.dataset.n + '"]';
    const off = row.getBoundingClientRect().top - this.content.getBoundingClientRect().top;
    return () => {
      const r = this.content.querySelector(sel);
      if (!r) return;
      const top = r.getBoundingClientRect().top - this.content.getBoundingClientRect().top + this.content.scrollTop - off;
      this.setScrollTop(Math.max(0, Math.round(top)));
    };
  }

  // firstVisibleRow: the first .vl whose bottom edge is below the scroller's top.
  // In a chunked file the chunks are found first (a handful of boxes), so only
  // one block of rows is measured, not every row of a 50,000-line file.
  firstVisibleRow() {
    const top = this.content.getBoundingClientRect().top;
    let scope = this.content;
    const chunks = this.content.querySelectorAll(".vl-chunk");
    if (chunks.length) {
      scope = chunks[chunks.length - 1];
      for (const ch of chunks) if (ch.getBoundingClientRect().bottom > top) { scope = ch; break; }
    }
    for (const r of scope.querySelectorAll(".vl")) if (r.getBoundingClientRect().bottom > top) return r;
    return null;
  }

  // applyWrap brings this viewer's editor (if any) in line with the preference.
  // The read view needs nothing: it follows the root class.
  applyWrap() {
    if (!this.cm) return;
    this.cm.setOption("lineWrapping", viewerPrefs.getWrap());
    this.cm.refresh();
  }

  // allViewers: every live viewer, in every pane plus the Files dock's.
  static allViewers() {
    const all = [];
    eachViewer((vt) => all.push(vt));
    if (typeof dockViewerList === "function") for (const dv of dockViewerList()) if (!all.includes(dv)) all.push(dv);
    return all;
  }

  // applyWrapAll: bring every live viewer's editor in line with the preference.
  static applyWrapAll() { for (const vt of ViewerTab.allViewers()) vt.applyWrap(); }

  renderImage() {
    this.showingMsg = false;
    const img = document.createElement("img");
    img.className = "viewer-img";
    img.alt = this.name;
    img.onerror = () => this.msg("could not load image", "err");
    img.src = this.box ? boxFileURL(this.box, this.path) : "/api/fs/read?path=" + encodeURIComponent(this.path);
    this.content.replaceChildren(img);
    this.hasLines = false;
  }
}

function isViewerWrap() { return viewerPrefs.getWrap(); }

// toggleViewerWrap flips "Wrap lines" for every open viewer, read and edit, and
// remembers it for this browser. Each viewer's first visible line is noted
// before the flip and scrolled back to after it, so the reader keeps their place.
function toggleViewerWrap() {
  const restores = ViewerTab.allViewers().map((vt) => vt.wrapAnchor());
  const on = !viewerPrefs.getWrap();
  viewerPrefs.setWrap(on);
  document.documentElement.classList.toggle("viewer-wrap", on);
  ViewerTab.applyWrapAll();
  for (const restore of restores) restore();
  return on;
}

// altNoteNodes: what goes in the one-line note over a file opened from another checkout (#53), for replaceChildren: the
// sentence, with the worktree's name in <code> when there is one. Text only: the name comes from a directory name on disk.
// The phone's ViewerTab and the dock's EditorTab both draw it, so there is one copy of the markup.
function altNoteNodes(worktree) {
  const n = altNote(worktree);
  if (!n.name) return [n.pre];
  const code = document.createElement("code");
  code.textContent = n.name;
  return [n.pre, code, n.post];
}

// altPickNode: the list shown when several other checkouts hold the file a terminal link named (#53): a title, a line saying
// what was not found, and a button per copy (its worktree, or "main checkout", and how long ago it changed). `onPick(item)`
// is called with the item whose button was pressed. Text only. Shared by ViewerTab and EditorTab, like altNoteNodes.
function altPickNode(items, alt, onPick) {
  const box = document.createElement("div");
  box.className = "viewer-msg viewer-alts";
  const h = document.createElement("div");
  h.className = "viewer-alts-title";
  h.textContent = "This file exists in " + items.length + " places";
  const sub = document.createElement("div");
  sub.className = "viewer-alts-sub";
  sub.textContent = alt.rel + " was not found here. Pick the copy to open:";
  box.append(h, sub);
  const now = Date.now() / 1000;
  for (const it of items) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "viewer-alt";
    b.title = it.path;
    const name = document.createElement("span");
    name.className = "viewer-alt-name";
    name.textContent = it.worktree || "main checkout";
    b.append(name);
    const ago = agoText(it.mtime, now);
    if (ago) {
      const t = document.createElement("span");
      t.className = "viewer-alt-ago";
      t.textContent = ago;
      b.append(t);
    }
    b.onclick = () => onPick(it);
    box.append(b);
  }
  return box;
}

function escapeHtml(s) {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// Dirty guard for a page reload/close: `cells` (tabs.js) is the one registry of
// every open tab in every pane. This file loads before tabs.js in index.html,
// but the reference below only ever runs on the beforeunload event — long after
// every page script has finished — so the forward reference is safe (the same
// pattern load() already relies on for closeTabEverywhere).
//
// The desktop Files dock's viewers are in no cell, so they are asked separately
// (drawerHasUnsaved, filesdrawer.js, loaded later on the same terms).
window.addEventListener("beforeunload", (e) => {
  const anyDirty = cells.some((c) => c.tabs.some((t) => t.kind === "viewer" && t.dirty && t.dirty()))
    || (typeof drawerHasUnsaved === "function" && drawerHasUnsaved());
  if (anyDirty) { e.preventDefault(); e.returnValue = ""; }
});
