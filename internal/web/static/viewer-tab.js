"use strict";

// IMAGE_EXTS and extOf live in copyfile.js: the copy paths need them too, and a
// pure module there is reachable from `node --test`, which this file is not.
const MD_EXTS = new Set(["md", "markdown"]);
// Rows per block in a big file's read view (renderCode, .vl-chunk in app.css).
const VT_CHUNK = 500;

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
    this.toolbar = document.createElement("div");
    this.toolbar.className = "viewer-tools";
    const reload = document.createElement("button");
    reload.className = "viewer-reload"; reload.textContent = "⟳"; reload.title = "Reload";
    reload.onclick = () => this.load();
    this.toolbar.append(reload);
    // Download works for files this viewer refuses to render: binaries and
    // anything over the 2 MiB preview cap, which are exactly the ones worth
    // fetching, so the button is never conditional on the render succeeding.
    const dl = document.createElement("button");
    dl.className = "viewer-reload";
    dl.textContent = "⤓";
    dl.title = "Download this file";
    dl.onclick = () => this.download();
    this.toolbar.append(dl);
    // A word, not a glyph: a button's accessible name comes from its text
    // content before its title, and an unprobed codepoint ships as a tofu box no
    // test catches. `viewer-auto` is the existing text-button style.
    if (!IMAGE_EXTS.has(ext0)) { // an image has no text to copy and nothing to poll
      const cp = document.createElement("button");
      cp.className = "viewer-auto";
      cp.textContent = "Copy";
      cp.title = "Copy this file's contents";
      cp.onclick = () => this.copyContents(cp);
      this.toolbar.append(cp);
      if (!this.box) { // a box file is read-only: the host cannot write into the box
        const edit = document.createElement("button");
        edit.className = "viewer-auto";
        edit.textContent = "Edit";
        edit.title = "Edit this file";
        edit.onclick = () => this.enterEdit();
        this.toolbar.append(edit);
        this.editBtn = edit; // hidden once a read says the file is read-only
      }
      const auto = document.createElement("button");
      auto.className = "viewer-auto"; auto.textContent = "Auto"; auto.title = "Auto-reload every 2s";
      auto.onclick = () => this.toggleAuto();
      this.autoBtn = auto; // toggleAuto keeps its "on" state, whoever toggles
      this.toolbar.append(auto);
    }
    if (this.zoomVar) { // text viewers only: images have nothing to size
      const zout = document.createElement("button");
      zout.className = "viewer-zoom"; zout.textContent = "A−"; zout.title = "Smaller (" + shortcutHint("zoom.out", IS_MAC) + ")";
      zout.onclick = () => this.fontStep(-1);
      const zin = document.createElement("button");
      zin.className = "viewer-zoom"; zin.textContent = "A+"; zin.title = "Bigger (" + shortcutHint("zoom.in", IS_MAC) + ")";
      zin.onclick = () => this.fontStep(+1);
      this.toolbar.append(zout, zin);
    }
    const pathLbl = document.createElement("span");
    pathLbl.className = "viewer-path"; pathLbl.textContent = path; pathLbl.title = path;
    this.toolbar.append(pathLbl);
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
    this.isImage = IMAGE_EXTS.has(ext0);
    this.find = new ViewerFind(this);
    this.findBar = this.find.el;
    this.el.append(this.toolbar, this.findBar, this.content);
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
    if (mode && this.openFind(mode)) { e.preventDefault(); e.stopPropagation(); }
  }

  // openFind shows the find bar in "find" or "line" (Go to line) mode and says
  // whether it could. Find works on text (code, markdown, the editor); Go to line
  // needs numbered lines: the code view or the editor.
  openFind(mode) {
    if (mode === "line") {
      if (!(this.editing && this.cm) && !this.hasLines) return false;
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
    const f = () => { if (this.content.isConnected) this.content.focus({ preventScroll: true }); };
    if (this.content.isConnected) f();
    else requestAnimationFrame(f);
  }

  title() { return this.name; }
  status() { return this.statusText; }
  onShow() {}
  onHide() {}
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
    if (this.timer) clearInterval(this.timer); // stop polling on close (no leak)
    this.find.close(true); // frees the document-wide highlight names if this bar held them
  }
  // The toolbar buttons' actions as methods, so a host that hides this toolbar
  // (the desktop Files drawer has its own header) can offer the same ones.
  reload() { return this.load(); }
  download() {
    downloadPath(this.path, false, this.box ? boxFileURL(this.box, this.path, true) : undefined);
  }
  copy(btn) { this.copyContents(btn || null); }
  fontStep(d) { this.zoom(d * fontPrefs.STEP); }
  toggleAuto() {
    this.auto = !this.auto;
    if (this.autoBtn) this.autoBtn.classList.toggle("on", this.auto);
    if (this.auto) this.timer = setInterval(() => this.load(true), 2000);
    else { clearInterval(this.timer); this.timer = null; }
    this.onUpdate?.();
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
  setStatus(t) { this.statusText = t; this.onUpdate?.(); }

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
    // Claimed synchronously so an auto-reload tick that overlaps the first read
    // cannot also count as "first".
    const first = this.firstLoad;
    this.firstLoad = false;
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
    // Only a successful read answers the question; a dropped poll keeps the
    // last known answer rather than bringing back an Edit that cannot save.
    if (res.ok) {
      this.readOnly = readOnlyFrom(res);
      if (this.editBtn) this.editBtn.hidden = this.readOnly;
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
        // The server's own sentence, not the clipboard's. This pane is the
        // RENDER failure surface: a 5 MB log opened here must read "file too
        // large to preview", not "too large to copy — use Download instead".
        this.msg(
          (res.data && res.data.error) || (res.status ? "HTTP " + res.status : "could not reach the server"),
          "err");
        this.setStatus("error");
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
    copyText(plan.text, (ok) => {
      btn?.focus();
      const m = copyResultMessage(plan, this.name, ok);
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
    // A fresh edit: a "saved" left from the last one says nothing about this
    // buffer. setStatus also tells a host header to show Cancel · Save.
    this.setStatus("");
  }

  // renderEditBar builds the action row above the editor: Save · Cancel, plus a
  // status/conflict message that showConflict() fills in and adds Reload /
  // Overwrite anyway to. It is appended as holder's sibling inside `content`
  // (not inside holder itself) so CodeMirror's own sizing is undisturbed.
  renderEditBar() {
    const bar = document.createElement("div");
    bar.className = "viewer-editbar";
    const save = document.createElement("button");
    save.className = "viewer-auto viewer-save";
    save.textContent = "Save";
    save.title = "Save (" + shortcutHint("editor.save", IS_MAC) + ")";
    save.onclick = () => this.save(false);
    const cancel = document.createElement("button");
    cancel.className = "viewer-auto viewer-cancel";
    cancel.textContent = "Cancel";
    cancel.title = "Discard changes and return to the read view";
    cancel.onclick = () => this.cancelEdit();
    const msg = document.createElement("span");
    msg.className = "viewer-edit-msg";
    bar.append(save, cancel, msg);
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
    this.editing = false;
    this.cm = null;
    this.editBar = null;
    this.editMsg = null;
    this.conflictBtns = null;
    this.onUpdate?.(); // back to the read view's actions
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
    const pre = document.createElement("pre");
    pre.className = "viewer-pre hljs";
    pre.style.setProperty("--vl-digits", String(Math.max(3, String(lines.length).length)));
    const frag = document.createDocumentFragment();
    // A big file is laid out in blocks of VT_CHUNK rows, each skipped by the
    // browser until it nears the viewport (content-visibility, app.css).
    const chunked = lines.length > VT_CHUNK * 2;
    let chunk = null;
    for (let i = 0; i < lines.length; i++) {
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
      if (chunked) {
        if (i % VT_CHUNK === 0) {
          chunk = document.createElement("div");
          chunk.className = "vl-chunk";
          // The last block is shorter: say so, or its skipped height is overstated.
          chunk.style.setProperty("--vl-rows", String(Math.min(VT_CHUNK, lines.length - i)));
          frag.append(chunk);
        }
        chunk.append(row);
      } else frag.append(row);
    }
    pre.append(frag);
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
    const n = row.dataset.n;
    const off = row.getBoundingClientRect().top - this.content.getBoundingClientRect().top;
    return () => {
      const r = this.content.querySelector('.vl[data-n="' + n + '"]');
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

  // allViewers: every live viewer, in every pane plus the Files drawer's.
  static allViewers() {
    const all = [];
    eachViewer((vt) => all.push(vt));
    const dv = typeof drawerViewerTab === "function" ? drawerViewerTab() : null;
    if (dv && !all.includes(dv)) all.push(dv);
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

function escapeHtml(s) {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// Dirty guard for a page reload/close: `cells` (tabs.js) is the one registry of
// every open tab in every pane. This file loads before tabs.js in index.html,
// but the reference below only ever runs on the beforeunload event — long after
// every page script has finished — so the forward reference is safe (the same
// pattern load() already relies on for closeTabEverywhere).
//
// The desktop Files drawer's viewer is in no cell, so it is asked separately
// (drawerHasUnsaved, filesdrawer.js, loaded later on the same terms).
window.addEventListener("beforeunload", (e) => {
  const anyDirty = cells.some((c) => c.tabs.some((t) => t.kind === "viewer" && t.dirty && t.dirty()))
    || (typeof drawerHasUnsaved === "function" && drawerHasUnsaved());
  if (anyDirty) { e.preventDefault(); e.returnValue = ""; }
});
