"use strict";

// IMAGE_EXTS and extOf live in copyfile.js: the copy paths need them too, and a
// pure module there is reachable from `node --test`, which this file is not.
const MD_EXTS = new Set(["md", "markdown"]);

// ViewerTab renders one file read-only: code (highlight.js), markdown (marked),
// or image (<img>). It is a Cell tab (body-only); the Cell owns the tab bar.
class ViewerTab {
  constructor(path) {
    // Normalise once, here: tabs are keyed and matched by this string, and the
    // paste-path box hands over whatever was typed. "/home/u/./a.txt" would
    // otherwise open a second tab for a file already open, and survive that
    // file's deletion still rendering content that no longer exists.
    if (typeof normalizePath === "function") path = normalizePath(path);
    this.id = "view:" + path;
    this.kind = "viewer";
    this.path = path;
    this.name = path.split("/").pop() || path;
    this.statusText = "";
    this.onUpdate = null;
    this.auto = false;      // auto-reload poll on/off (per viewer, opt-in)
    this.timer = null;
    this.lastContent = null; // last rendered text, to skip no-op re-renders
    // Why Copy has nothing to give, or null once there is text. lastContent
    // alone cannot say: load() leaves it untouched for an image, for a binary,
    // for every HTTP error, and for the moment before the first load returns.
    this.copyRefusal = "loading";
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
    dl.onclick = () => downloadPath(this.path, false);
    this.toolbar.append(dl);
    // A word, not a glyph: a button's accessible name comes from its text
    // content before its title, and an unprobed codepoint ships as a tofu box no
    // test catches. `viewer-auto` is the existing text-button style.
    if (!IMAGE_EXTS.has(ext0)) { // an image has no text to copy
      const cp = document.createElement("button");
      cp.className = "viewer-auto";
      cp.textContent = "Copy";
      cp.title = "Copy this file's contents";
      cp.onclick = () => this.copyContents(cp);
      this.toolbar.append(cp);
    }
    if (!IMAGE_EXTS.has(ext0)) { // auto-reload is pointless for images
      const auto = document.createElement("button");
      auto.className = "viewer-auto"; auto.textContent = "Auto"; auto.title = "Auto-reload every 2s";
      auto.onclick = () => this.toggleAuto(auto);
      this.toolbar.append(auto);
    }
    if (this.zoomVar) { // text viewers only: images have nothing to size
      const zout = document.createElement("button");
      zout.className = "viewer-zoom zout"; zout.textContent = "A−"; zout.title = "Smaller (Ctrl+-)";
      zout.onclick = () => this.zoom(-fontPrefs.STEP);
      const zin = document.createElement("button");
      zin.className = "viewer-zoom zin"; zin.textContent = "A+"; zin.title = "Bigger (Ctrl+=)";
      zin.onclick = () => this.zoom(+fontPrefs.STEP);
      this.toolbar.append(zout, zin);
    }
    const pathLbl = document.createElement("span");
    pathLbl.className = "viewer-path"; pathLbl.textContent = path; pathLbl.title = path;
    this.toolbar.append(pathLbl);
    this.content = document.createElement("div");
    this.content.className = "viewer-content";
    this.el.append(this.toolbar, this.content);
    this.load();
  }

  title() { return this.name; }
  status() { return this.statusText; }
  onShow() {}
  onHide() {}
  dispose() { if (this.timer) clearInterval(this.timer); } // stop polling on close (no leak)
  toggleAuto(btn) {
    this.auto = !this.auto;
    btn.classList.toggle("on", this.auto);
    if (this.auto) this.timer = setInterval(() => this.load(true), 2000);
    else { clearInterval(this.timer); this.timer = null; }
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

  msg(text, cls) {
    const d = document.createElement("div");
    d.className = "viewer-msg" + (cls ? " " + cls : "");
    d.textContent = text;
    this.content.replaceChildren(d);
  }

  // load fetches + renders the file. `poll` (auto-reload tick) suppresses status
  // churn, skips re-render when the content is unchanged, and preserves scroll.
  async load(poll) {
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
    const res = await fsRead(this.path);
    const plan = copyContentPlan(res);
    if (plan.action !== "copy") {
      // Record WHY there is no text, so the Copy button can say something true
      // instead of guessing "binary or too large" at a file that was deleted.
      //
      // Only on a real load. A failed auto-reload tick leaves the screen exactly
      // as it was — same rendered body, same lastContent — so it must not change
      // what Copy believes about that text. Otherwise one dropped poll makes the
      // button refuse to copy what the user is looking at, citing an error the
      // viewer never showed.
      if (poll) return;
      this.copyRefusal = plan.reason;
      if (plan.reason === "empty") {
        // A successful read of a file with nothing in it is not an error.
        this.lastContent = "";
        this.msg("empty file");
        this.setStatus("");
      } else if (plan.reason === "binary") {
        this.msg(`binary file · ${(res.data && res.data.size) || 0} bytes`);
        this.setStatus("");
      } else {
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
    if (poll && plan.text === this.lastContent) return; // unchanged → no re-render
    this.lastContent = plan.text;
    const scroll = this.content.scrollTop;
    if (MD_EXTS.has(ext)) this.renderMarkdown(plan.text);
    else this.renderCode(plan.text, res.data.lang);
    this.content.scrollTop = scroll; // preserve position across reload
  }

  // Copies exactly what is on screen — the raw source this viewer fetched, which
  // for markdown is the markdown and not the rendered HTML. Focus returns to the
  // button: copyText's textarea fallback leaves it on <body>, and pulling it into
  // the terminal pane is not what someone working in this toolbar asked for.
  copyContents(btn) {
    const plan = this.copyRefusal
      ? { action: "refuse", reason: this.copyRefusal }
      : { action: "copy", text: this.lastContent, lines: countLines(this.lastContent) };
    if (plan.action === "refuse") {
      const m = copyResultMessage(plan, this.name, false);
      showToast(m.text, { error: m.error });
      return;
    }
    copyText(plan.text, (ok) => {
      btn.focus();
      const m = copyResultMessage(plan, this.name, ok);
      showToast(m.text, { error: m.error });
    });
  }

  renderCode(text, lang) {
    const pre = document.createElement("pre");
    const code = document.createElement("code");
    let html;
    try {
      html = (window.hljs && hljs.getLanguage(lang))
        ? hljs.highlight(text, { language: lang }).value
        : (window.hljs ? hljs.highlightAuto(text).value : escapeHtml(text));
    } catch (_) { html = escapeHtml(text); }
    code.className = "hljs";
    code.innerHTML = html;
    pre.className = "viewer-pre";
    pre.append(code);
    this.content.replaceChildren(pre);
  }

  renderMarkdown(text) {
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
      if (!/^(https?:|data:|\/)/.test(src)) img.setAttribute("src", "/api/fs/read?path=" + encodeURIComponent(dir + src));
    });
    // marked v12 dropped the highlight option: highlight fenced blocks here (hljs v11 API).
    if (window.hljs) frag.querySelectorAll("pre code").forEach((el) => { try { hljs.highlightElement(el); } catch (_) {} });
    const wrap = document.createElement("div");
    wrap.className = "md-body";
    wrap.appendChild(frag);
    this.content.replaceChildren(wrap);
  }

  renderImage() {
    const img = document.createElement("img");
    img.className = "viewer-img";
    img.alt = this.name;
    img.onerror = () => this.msg("could not load image", "err");
    img.src = "/api/fs/read?path=" + encodeURIComponent(this.path);
    this.content.replaceChildren(img);
  }
}

function escapeHtml(s) {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
