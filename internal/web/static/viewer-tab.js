"use strict";

const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg"]);
const MD_EXTS = new Set(["md", "markdown"]);
function extOf(path) { const b = path.split("/").pop() || ""; const i = b.lastIndexOf("."); return i < 0 ? "" : b.slice(i + 1).toLowerCase(); }

// ViewerTab renders one file read-only: code (highlight.js), markdown (marked),
// or image (<img>). It is a Cell tab (body-only); the Cell owns the tab bar.
class ViewerTab {
  constructor(path) {
    this.id = "view:" + path;
    this.kind = "viewer";
    this.path = path;
    this.name = path.split("/").pop() || path;
    this.statusText = "";
    this.onUpdate = null;
    this.auto = false;      // auto-reload poll on/off (per viewer, opt-in)
    this.timer = null;
    this.lastContent = null; // last rendered text, to skip no-op re-renders

    this.el = document.createElement("div");
    this.el.className = "tab-body viewer-body";
    this.toolbar = document.createElement("div");
    this.toolbar.className = "viewer-tools";
    const reload = document.createElement("button");
    reload.className = "viewer-reload"; reload.textContent = "⟳"; reload.title = "Reload";
    reload.onclick = () => this.load();
    this.toolbar.append(reload);
    if (!IMAGE_EXTS.has(extOf(path))) { // auto-reload is pointless for images
      const auto = document.createElement("button");
      auto.className = "viewer-auto"; auto.textContent = "Auto"; auto.title = "Auto-reload every 2s";
      auto.onclick = () => this.toggleAuto(auto);
      this.toolbar.append(auto);
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
    if (IMAGE_EXTS.has(ext)) { if (!poll) { this.renderImage(); this.setStatus(""); } return; }
    if (!poll) this.setStatus("loading…");
    let data;
    try {
      data = await api("/api/fs/read?path=" + encodeURIComponent(this.path));
    } catch (e) { if (!poll) { this.msg(e.message, "err"); this.setStatus("error"); } return; }
    if (!poll) this.setStatus("");
    if (data.kind === "binary") { if (!poll) this.msg(`binary file · ${data.size} bytes`); return; }
    if (poll && data.content === this.lastContent) return; // unchanged → no re-render
    this.lastContent = data.content;
    const scroll = this.content.scrollTop;
    if (MD_EXTS.has(ext)) this.renderMarkdown(data.content);
    else this.renderCode(data.content, data.lang);
    this.content.scrollTop = scroll; // preserve position across reload
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
      // control-char obfuscation, mXSS, on* handlers, svg/math) — a hand-rolled
      // strip-and-reserialize sanitizer is not safe against these.
      frag = DOMPurify.sanitize(marked.parse(text), { RETURN_DOM_FRAGMENT: true });
    } catch (_) { this.renderCode(text, "markdown"); return; }
    // rewrite relative <img src> against the file's dir BEFORE attaching (no wasted 404 fetch)
    frag.querySelectorAll("img").forEach((img) => {
      const src = img.getAttribute("src") || "";
      if (!/^(https?:|data:|\/)/.test(src)) img.setAttribute("src", "/api/fs/read?path=" + encodeURIComponent(dir + src));
    });
    // marked v12 dropped the highlight option — highlight fenced blocks here (hljs v11 API).
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
