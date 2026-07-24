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

    this.el = document.createElement("div");
    this.el.className = "tab-body viewer-body";
    this.toolbar = document.createElement("div");
    this.toolbar.className = "viewer-tools";
    const reload = document.createElement("button");
    reload.className = "viewer-reload"; reload.textContent = "⟳"; reload.title = "Reload";
    reload.onclick = () => this.load();
    const pathLbl = document.createElement("span");
    pathLbl.className = "viewer-path"; pathLbl.textContent = path; pathLbl.title = path;
    this.toolbar.append(reload, pathLbl);
    this.content = document.createElement("div");
    this.content.className = "viewer-content";
    this.el.append(this.toolbar, this.content);
    this.load();
  }

  title() { return this.name; }
  status() { return this.statusText; }
  onShow() {}
  onHide() {}
  dispose() {}
  setStatus(t) { this.statusText = t; this.onUpdate?.(); }

  msg(text, cls) {
    const d = document.createElement("div");
    d.className = "viewer-msg" + (cls ? " " + cls : "");
    d.textContent = text;
    this.content.replaceChildren(d);
  }

  async load() {
    const ext = extOf(this.path);
    this.setStatus("loading…");
    if (IMAGE_EXTS.has(ext)) { this.renderImage(); this.setStatus(""); return; }
    let data;
    try {
      data = await api("/api/fs/read?path=" + encodeURIComponent(this.path));
    } catch (e) { this.msg(e.message, "err"); this.setStatus("error"); return; }
    this.setStatus("");
    if (data.kind === "binary") { this.msg(`binary file · ${data.size} bytes`); return; }
    if (MD_EXTS.has(ext)) this.renderMarkdown(data.content);
    else this.renderCode(data.content, data.lang);
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
    let html;
    try {
      if (window.marked && marked.parse) {
        marked.setOptions({ breaks: false, gfm: true }); // NOTE: marked v12 removed the `highlight` option
        html = marked.parse(text);
      } else { this.renderCode(text, "markdown"); return; }
    } catch (_) { this.renderCode(text, "markdown"); return; }
    const wrap = document.createElement("div");
    wrap.className = "md-body";
    wrap.innerHTML = sanitizeHtml(html);
    // marked v12 dropped the highlight option — highlight fenced blocks here (hljs v11 API).
    if (window.hljs) {
      wrap.querySelectorAll("pre code").forEach((el) => { try { hljs.highlightElement(el); } catch (_) {} });
    }
    // resolve relative <img src> against the file's directory via /api/fs/read
    wrap.querySelectorAll("img").forEach((img) => {
      const src = img.getAttribute("src") || "";
      if (!/^(https?:|data:|\/)/.test(src)) {
        img.src = "/api/fs/read?path=" + encodeURIComponent(dir + src);
      }
    });
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

function sanitizeHtml(html) {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  tpl.content.querySelectorAll("script, style, iframe, object, embed").forEach((n) => n.remove());
  tpl.content.querySelectorAll("*").forEach((el) => {
    [...el.attributes].forEach((a) => {
      const n = a.name.toLowerCase();
      if (n.startsWith("on")) el.removeAttribute(a.name);
      if ((n === "href" || n === "src") && /^\s*javascript:/i.test(a.value)) el.removeAttribute(a.name);
    });
  });
  return tpl.innerHTML;
}
