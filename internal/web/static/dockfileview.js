"use strict";

// The Files dock's views for a file that is not text (#344): a zoomable image, a video or audio player, and the card (a PDF's, an
// unknown type's, a binary file's, a too-large text file's). One DockFileView per tab; EditorTab mounts it where it used to mount a
// ViewerTab (the phone's viewer, which the dock no longer builds) and calls the same lifecycle on it. It owns its body and its own
// status bar (.dk-sb, the class code's Ln/Col bar wears): the type and size on the left, the image's zoom controls on the right.
// The header (view switch, Follow, ⋯, Full) is the dock's.
//
// What a file is, what to call it and the zoom arithmetic are filekinds.js's (pure, tested on their own). Everything a file can
// name (its name, its extension) goes in with textContent or a property, never as markup: file names are untrusted.
//
// Loaded as a classic script: these top-level names are shared globals, so each helper is prefixed dfv.

// The card's badge for the types whose extension is too long or too cryptic in four characters.
const DFV_BADGES = { sqlite: "DB", sqlite3: "DB", db: "DB", ipynb: "NB" };

// The raw bytes of the file, or a request about it, from the same route a read of it uses (a box file has its own).
function dfvURL(box, path, extra) {
  return (box ? boxFileURL(box, path) : "/api/fs/read?path=" + encodeURIComponent(path)) + (extra || "");
}

// dfvClock: a media length as m:ss (h:mm:ss from an hour). "" for what is not a length (NaN before the metadata, Infinity for a stream).
function dfvClock(sec) {
  if (!Number.isFinite(sec) || sec < 0) return "";
  const t = Math.floor(sec), h = Math.floor(t / 3600), m = Math.floor((t % 3600) / 60), s = t % 60;
  const ss = (s < 10 ? "0" : "") + s;
  return h > 0 ? h + ":" + (m < 10 ? "0" : "") + m + ":" + ss : m + ":" + ss;
}

// dfvEl: an element with a class and, if given, its text.
function dfvEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function dfvButton(cls, text, label, run) {
  const b = dfvEl("button", cls, text);
  b.type = "button";
  if (label) { b.title = label; b.setAttribute("aria-label", label); }
  b.onclick = run;
  return b;
}

class DockFileView {
  // o = {path, box, kind, meta, download, fetchMeta}: `kind` is image | video | audio | pdf | card | binary | toolarge; `meta` is
  // {size, mtime} or null (the request failed: those facts are left out); `download()` is the card's Download button; `fetchMeta()`
  // answers {size, mtime, ...} or null, for the look at the file when the tab is shown again.
  constructor(o) {
    this.path = o.path;
    this.box = o.box || "";
    this.name = String(o.path).split("/").pop() || o.path;
    this.ext = extOf(o.path);
    this.orig = o.kind;                  // what it was made as: a card from an error becomes it again when the file changes
    this.kind = o.kind;                  // what is shown now (a media or image error turns it into "card")
    this.openable = o.kind === "pdf";    // has an "Open in new tab"
    this.meta = o.meta || null;
    this.note = "";
    this.download = o.download || (() => {});
    this.fetchMeta = o.fetchMeta || (async () => null);
    this.onUpdate = null;                // a field EditorTab assigns: the view changed what it is
    this.scroll = 0;                     // the dock's stored scroll: a picture's zoom is not remembered across visits
    this.disposed = false;
    this.attachedOnce = false;
    this.media = null;                   // the <video> / <audio>, while there is one
    this.dim = null;                     // a video's {w, h}, once known
    this.dur = NaN;                      // a media length, once known
    // The image: natural size, scale, Fit mode, the stage's last scroll (kept across a hide), the drag.
    this.nw = 0; this.nh = 0; this.scale = 1; this.fit = true; this.keep = { left: 0, top: 0 }; this.drag = null; this.gesture = 1;
    this.stage = null; this.canvas = null; this.img = null; this.zoom = null; this.ro = null;

    this.el = dfvEl("div", "dk-fv");
    this.el.tabIndex = -1;
    this.bodyEl = dfvEl("div", "dk-fv-body");
    this.sbEl = dfvEl("div", "dk-sb");
    this.factsEl = dfvEl("div", "dk-fv-facts");
    this.rightEl = dfvEl("div", "dk-sb-r");
    this.sbEl.append(this.factsEl, this.rightEl);
    this.el.append(this.bodyEl, this.sbEl);
    this.build();
  }

  // --- what is in the body -------------------------------------------------------------------------------

  // build (re)makes the body and the status bar for this.kind.
  build() {
    this.el.dataset.kind = this.kind;
    this.rightEl.replaceChildren();
    this.zoom = null;
    if (this.kind === "image") this.buildImage();
    else if (this.kind === "video" || this.kind === "audio") this.buildMedia();
    else this.buildCard();
    this.paintFacts();
  }

  // asCard turns the view into the fallback card with `note`: an image or a media file the browser could not use.
  asCard(note) {
    if (this.disposed || this.kind === "card") return;
    this.releaseMedia();
    this.releaseImage();
    this.kind = "card";
    this.note = note;
    this.build();
    if (this.onUpdate) this.onUpdate();
  }

  // badgeText: the card's badge. A database and a notebook have a short word of their own (the long extensions read as broken in four
  // characters); any other type is its extension, upper-cased, four characters at most.
  badgeText(binary) {
    if (!this.ext) return binary ? "BIN" : "FILE";
    return DFV_BADGES[this.ext.toLowerCase()] || this.ext.toUpperCase().slice(0, 4);
  }

  buildCard() {
    const binary = this.kind === "binary";
    const badge = dfvEl("div", "dk-fv-badge", this.badgeText(binary));
    badge.setAttribute("aria-hidden", "true");
    const name = dfvEl("div", "dk-fv-name", this.name);
    name.title = this.name;
    const meta = dfvEl("div", "dk-fv-meta", this.cardMeta());
    const note = dfvEl("div", "dk-fv-note", this.cardNote());
    const acts = dfvEl("div", "dk-fv-acts");
    if (this.openable) acts.append(dfvButton("btn btn-primary btn-sm dk-fv-open", "Open in new tab", "", () => this.open()));
    acts.append(dfvButton("btn btn-sm dk-fv-dl", "Download", "", () => this.download()));
    const head = dfvEl("div", "dk-fv-head");
    head.append(name, meta);
    const card = dfvEl("div", "dk-fv-card");
    card.append(badge, head, note, acts);
    const wrap = dfvEl("div", "dk-fv-cardwrap");
    wrap.append(card);
    this.bodyEl.replaceChildren(wrap);
  }

  // cardMeta: "<type> · <size> · modified <time>", each part left out when it is not known.
  cardMeta() {
    const parts = [fileTypeLabel(this.ext, this.kind === "binary")];
    if (this.meta && typeof this.meta.size === "number") parts.push(humanBytes(this.meta.size));
    const when = this.meta ? fmtModified(this.meta.mtime, Date.now()) : "";
    if (when) parts.push("modified " + when);
    return parts.join(" · ");
  }

  cardNote() {
    if (this.note) return this.note;
    if (this.kind === "pdf") return "Opens in its own browser tab, sandboxed: nothing in the PDF can reach CuliBox.";
    if (this.kind === "binary") return "Not text, and CuliBox has no viewer for this type.";
    if (this.kind === "toolarge") return "Larger than the 2 MB preview limit. Download it to read it.";
    return "CuliBox has no viewer for this type.";
  }

  // open: the PDF in its own tab, as downloadPath saves a file (a hidden same-origin link; noopener, so the tab has no handle on
  // this one). It is the raw route with a sandbox header, never embedded here.
  open() {
    const a = document.createElement("a");
    a.style.display = "none";
    a.href = this.rawURL();
    a.target = "_blank";
    a.rel = "noopener";
    document.body.append(a);
    a.click();
    a.remove();
  }

  // The status bar's left side: facts in spans, as code's bar separates its own. Type is the extension in capitals.
  paintFacts() {
    const facts = [];
    if (this.kind === "image") {
      if (this.nw > 0 && this.nh > 0) facts.push(this.nw + " × " + this.nh);
    } else if (this.kind === "video") {
      if (this.dim && this.dim.w > 0 && this.dim.h > 0) facts.push(this.dim.w + " × " + this.dim.h);
      if (dfvClock(this.dur)) facts.push(dfvClock(this.dur));
    } else if (this.kind === "audio") {
      if (dfvClock(this.dur)) facts.push(dfvClock(this.dur));
    }
    facts.push(fileTypeShort(this.ext));
    if (this.meta && typeof this.meta.size === "number") facts.push(humanBytes(this.meta.size));
    this.factsEl.replaceChildren(...facts.map((t) => dfvEl("span", "dk-fv-fact", t)));
  }

  // --- the URLs -------------------------------------------------------------------------------------------

  // v: the file's modified time as a cache-buster once it is known, so a file that changed under the tab is fetched again.
  vParam() {
    return this.meta && this.meta.mtime ? "&v=" + this.meta.mtime : "";
  }

  // An image is served raw by the plain read route; a video, audio file or PDF only on raw=1.
  imageURL() { return dfvURL(this.box, this.path, this.vParam()); }
  rawURL() { return dfvURL(this.box, this.path, "&raw=1" + this.vParam()); }

  // --- media ----------------------------------------------------------------------------------------------

  buildMedia() {
    const m = dfvEl(this.kind, "dk-fv-media");
    m.controls = true;
    m.preload = "metadata";
    m.setAttribute("aria-label", this.name);
    m.addEventListener("loadedmetadata", () => this.mediaInfo());
    m.addEventListener("durationchange", () => this.mediaInfo());
    m.addEventListener("error", () => { if (m === this.media) this.asCard("This browser cannot play this file."); });
    m.src = this.rawURL();
    this.media = m;
    this.bodyEl.replaceChildren(m);
  }

  mediaInfo() {
    if (this.disposed || !this.media) return;
    this.dur = this.media.duration;
    if (this.kind === "video") this.dim = { w: this.media.videoWidth, h: this.media.videoHeight };
    this.paintFacts();
  }

  // releaseMedia lets the browser drop the stream: no src, then load() (the documented way to stop a fetch that is under way).
  releaseMedia() {
    const m = this.media;
    if (!m) return;
    this.media = null;
    m.pause();
    m.removeAttribute("src");
    m.load();
  }

  // --- the image ------------------------------------------------------------------------------------------

  buildImage() {
    this.nw = this.nh = 0;
    this.scale = 1;
    this.fit = true;
    const stage = dfvEl("div", "dk-fv-stage");
    stage.tabIndex = 0;
    const canvas = dfvEl("div", "dk-fv-canvas");
    const img = dfvEl("img", "dk-fv-img");
    img.alt = this.name;
    img.draggable = false;
    img.addEventListener("load", () => this.imageLoaded());
    img.addEventListener("error", () => { if (img === this.img) this.asCard("Could not load this image."); });
    canvas.append(img);
    stage.append(canvas);
    this.stage = stage; this.canvas = canvas; this.img = img;
    this.wire(stage);
    this.buildZoom();
    img.src = this.imageURL();
    this.bodyEl.replaceChildren(stage);
    if (typeof ResizeObserver === "function") {
      this.ro = new ResizeObserver(() => this.layout());
      this.ro.observe(stage);
    }
  }

  releaseImage() {
    if (this.ro) { this.ro.disconnect(); this.ro = null; }
    if (this.img) { const i = this.img; this.img = null; i.removeAttribute("src"); }
    this.stage = this.canvas = null;
    this.drag = null;
  }

  // The status bar's right side: Fit, 1:1, −, the percentage, +.
  buildZoom() {
    const z = {
      fit: dfvButton("dk-fv-zb dk-fv-fit", "Fit", "Fit to view", () => this.setFit()),
      one: dfvButton("dk-fv-zb dk-fv-one", "1:1", "Actual size (100%)", () => this.zoomTo(1)),
      out: dfvButton("dk-fv-zb dk-fv-out", "−", "Zoom out", () => this.step(-1)),
      pct: dfvEl("span", "dk-fv-pct", ""),
      in: dfvButton("dk-fv-zb dk-fv-in", "+", "Zoom in", () => this.step(1)),
    };
    this.zoom = z;
    this.rightEl.append(z.fit, z.one, z.out, z.pct, z.in);
    this.paintZoom();
  }

  // paintZoom: Fit is pressed in Fit mode; − and + are off at the ends; the percentage is the scale now. An image with no
  // intrinsic size (an SVG with no width) cannot be zoomed: all of it is off.
  paintZoom() {
    const z = this.zoom;
    if (!z) return;
    const sized = this.sized();
    z.fit.setAttribute("aria-pressed", this.fit ? "true" : "false");
    z.fit.disabled = z.one.disabled = !sized;
    z.out.disabled = !sized || zoomStep(this.scale, -1) === null;
    z.in.disabled = !sized || zoomStep(this.scale, 1) === null;
    z.pct.textContent = sized ? Math.round(this.scale * 100) + "%" : "";
  }

  sized() { return this.nw > 0 && this.nh > 0; }

  // measure: the stage's size without its scrollbars, or null while it has none (hidden, or not in the document): a stage measured
  // then must never be fitted to (scale 0).
  measure() {
    const s = this.stage;
    if (!s || !(s.clientWidth > 0) || !(s.clientHeight > 0)) return null;
    return { w: s.clientWidth, h: s.clientHeight };
  }

  // imageLoaded: the picture is in. A reload keeps the zoom when the pixel size is the same, else it starts again at Fit.
  imageLoaded() {
    if (this.disposed || !this.img) return;
    const nw = this.img.naturalWidth || 0, nh = this.img.naturalHeight || 0;
    if (nw !== this.nw || nh !== this.nh) { this.fit = true; this.scale = 1; }
    this.nw = nw;
    this.nh = nh;
    this.paintFacts();
    this.layout();
  }

  // layout puts the canvas and the picture where the scale says: the canvas is max(image, stage) in each direction and the picture is
  // centred in it, so a small picture sits in the middle and a big one scrolls. In Fit mode the scale is the fit first.
  layout() {
    if (this.disposed || !this.stage) return;
    const unsized = !this.sized();
    this.stage.classList.toggle("dk-fv-unsized", unsized);
    if (unsized) {
      for (const n of [this.canvas.style, this.img.style]) n.width = n.height = n.left = n.top = "";
      this.stage.classList.remove("dk-fv-placed", "dk-fv-pan");
      this.paintZoom();
      return;
    }
    const m = this.measure();
    if (!m) { this.paintZoom(); return; } // nothing to lay out in: the next measurement does it (resize, attach)
    if (this.fit) this.scale = fitScale(this.nw, this.nh, m.w, m.h);
    this.place(m);
    this.paintZoom();
  }

  // place sizes the canvas and puts the picture in it. The offsets are zoomScroll's own (centred in the stage when smaller than it, else
  // 0), so its arithmetic and the layout agree to the pixel. The canvas is rounded up, less float noise (a fitted picture is the stage's
  // width, not a pixel more: that pixel would bring the scrollbars).
  place(m) {
    const w = this.nw * this.scale, h = this.nh * this.scale;
    this.canvas.style.width = Math.max(Math.ceil(w - 1e-6), m.w) + "px";
    this.canvas.style.height = Math.max(Math.ceil(h - 1e-6), m.h) + "px";
    const is = this.img.style;
    is.width = w + "px";
    is.height = h + "px";
    is.left = Math.max(0, (m.w - w) / 2) + "px";
    is.top = Math.max(0, (m.h - h) / 2) + "px";
    this.stage.classList.add("dk-fv-placed"); // a picture is not shown before it is placed: it would flash at its natural size
    this.stage.classList.toggle("dk-fv-pan", w > m.w + 1e-6 || h > m.h + 1e-6);
  }

  // zoomTo: scale `s1` (clamped to 5..800 %) around the view point (px, py) in the stage's own pixels, the middle by default. The
  // point of the picture under it stays under it (zoomScroll). Any zoom of the user's leaves Fit mode.
  zoomTo(s1, px, py) {
    const m = this.measure();
    if (!this.sized() || !m) return;
    s1 = Math.min(ZOOM_STEPS[ZOOM_STEPS.length - 1], Math.max(ZOOM_STEPS[0], s1));
    const st = this.stage;
    const at = zoomScroll({
      stageW: m.w, stageH: m.h, nw: this.nw, nh: this.nh, s0: this.scale, s1,
      left: st.scrollLeft, top: st.scrollTop, px: px === undefined ? m.w / 2 : px, py: py === undefined ? m.h / 2 : py,
    });
    this.scale = s1;
    this.fit = false;
    this.place(m);
    st.scrollLeft = at.left; // after the canvas has its new size: a scroll offset past the end would be cut to it
    st.scrollTop = at.top;
    this.paintZoom();
  }

  step(dir) {
    const next = zoomStep(this.scale, dir);
    if (next !== null) this.zoomTo(next);
  }

  setFit() {
    this.fit = true;
    this.layout();
    if (this.stage) { this.stage.scrollLeft = 0; this.stage.scrollTop = 0; }
  }

  // pointer: where an event is, in the stage's own pixels.
  pointer(e) {
    const r = this.stage.getBoundingClientRect();
    return { px: e.clientX - r.left, py: e.clientY - r.top };
  }

  // wire: the stage's mouse, wheel, drag and (Safari) pinch.
  wire(stage) {
    // Ctrl or Cmd + wheel zooms around the pointer, and is the page's zoom no longer. A plain wheel scrolls the stage as ever.
    stage.addEventListener("wheel", (e) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      if (!this.sized()) return;
      const p = this.pointer(e);
      this.zoomTo(wheelScale(this.scale, e.deltaY, e.deltaMode), p.px, p.py);
    }, { passive: false });
    // Safari's trackpad pinch: `scale` is relative to where the gesture began.
    stage.addEventListener("gesturestart", (e) => { e.preventDefault(); this.gesture = this.scale; });
    stage.addEventListener("gesturechange", (e) => {
      e.preventDefault();
      if (!this.sized()) return;
      const p = this.pointer(e);
      this.zoomTo(this.gesture * e.scale, p.px, p.py);
    });
    // Drag pans, when there is more picture than view.
    stage.addEventListener("pointerdown", (e) => {
      if (e.button !== 0 || !stage.classList.contains("dk-fv-pan")) return;
      // Chrome fires pointerdown on the stage for a press on its own scrollbar: that is the scrollbar's, not a pan.
      if (e.offsetX >= stage.clientWidth || e.offsetY >= stage.clientHeight) return;
      this.drag = { x: e.clientX, y: e.clientY, left: stage.scrollLeft, top: stage.scrollTop, id: e.pointerId };
      stage.setPointerCapture(e.pointerId);
      stage.classList.add("dk-fv-grabbing");
    });
    stage.addEventListener("pointermove", (e) => {
      const d = this.drag;
      if (!d) return;
      stage.scrollLeft = d.left - (e.clientX - d.x);
      stage.scrollTop = d.top - (e.clientY - d.y);
    });
    const end = () => { this.drag = null; stage.classList.remove("dk-fv-grabbing"); };
    stage.addEventListener("pointerup", end);
    stage.addEventListener("pointercancel", end);
    // Double-click: Fit and 100 % trade places, around the pointer.
    stage.addEventListener("dblclick", (e) => {
      if (!this.sized()) return;
      if (this.fit) { const p = this.pointer(e); this.zoomTo(1, p.px, p.py); } else this.setFit();
    });
  }

  // --- the inner-viewer interface EditorTab calls ---------------------------------------------------------

  onShow() {}

  // onAttach: the tab is on screen. The first time is the open; after that the file is looked at again (the old toolbar's Reload),
  // and the picture is laid out in the stage as it is now, at the scroll it was left at.
  onAttach() {
    if (this.disposed) return;
    const again = this.attachedOnce;
    this.attachedOnce = true;
    this.layout();
    if (!again) return;
    if (this.stage) { this.stage.scrollLeft = this.keep.left; this.stage.scrollTop = this.keep.top; }
    this.refresh();
  }

  onHide() {
    if (this.stage) this.keep = { left: this.stage.scrollLeft, top: this.stage.scrollTop };
    if (this.media) this.media.pause();
    this.drag = null;
  }

  // refresh asks for the file's size and time again; a changed time reloads the view (a new src, the zoom kept when the pixel size
  // stays), and the card's and the bar's facts are written again. A failed request says nothing.
  async refresh() {
    const was = this.meta && this.meta.mtime;
    const next = await this.fetchMeta();
    if (this.disposed || !next || typeof next !== "object") return;
    const mtime = next.mtime || 0;
    if (!mtime || mtime === was) return;
    this.meta = { size: typeof next.size === "number" ? next.size : (this.meta && this.meta.size), mtime };
    if (this.kind === "image" && this.img) this.img.src = this.imageURL();
    else if (this.media) this.media.src = this.rawURL();
    else {
      // A card the browser's error made (an image or a player that failed) tries again: the file is a new one.
      if (this.kind === "card" && ["image", "video", "audio"].includes(this.orig)) { this.kind = this.orig; this.note = ""; }
      this.build();
    }
    this.paintFacts();
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    this.releaseMedia();
    this.releaseImage();
  }

  focusReader() { this.el.focus({ preventScroll: true }); }
  revealLine() {}
  wrapAnchor() { return () => {}; }
  applyWrap() {}
}
