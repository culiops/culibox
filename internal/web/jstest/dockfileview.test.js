"use strict";
// DockFileView (dockfileview.js, #344): the Files dock's view of a file that is not text, run for real on the fake DOM (fakedom.js's
// EvNode, which takes listeners and events) with filekinds.js, copyfile.js (extOf) and upload.js (humanBytes) loaded beside it.
// What a test sets up for the browser: the stage's size (clientWidth/Height), an <img>'s natural size, a media element's metadata;
// what it does: dispatches the events the browser would (load, wheel, pointerdown, dblclick, ...) and presses buttons.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { EvNode } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");

// A media element's calls, as the page would see them: pause(), and load() after the src was taken off.
class Media extends EvNode {
  constructor(tag, doc) { super(tag, doc); this.calls = []; this.duration = NaN; this.videoWidth = 0; this.videoHeight = 0; }
  pause() { this.calls.push("pause"); }
  load() { this.calls.push("load"); }
}

function page(over) {
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => (t === "video" || t === "audio" ? new Media(t, doc) : new EvNode(t, doc));
  const appended = [];
  doc.body = new EvNode("body", doc);
  doc.body.append = (a) => { appended.push(a); }; // the hidden link a PDF's Open makes: kept, to look at
  const ros = [];
  class RO { constructor(cb) { this.cb = cb; this.gone = false; ros.push(this); } observe(el) { this.el = el; } disconnect() { this.gone = true; } }
  const ctx = vm.createContext(Object.assign({
    console, Array, Object, Set, Map, Math, Number, JSON, Date, Promise, String, Error, document: doc,
    boxFileURL: (sid, p) => "/api/sessions/" + sid + "/boxfile?path=" + encodeURIComponent(p),
    ResizeObserver: RO,
  }, over));
  for (const f of ["copyfile.js", "filekinds.js", "upload.js", "dockfileview.js"]) new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  const get = (n) => new vm.Script(n, { filename: "get" }).runInContext(ctx);
  const DockFileView = get("DockFileView");
  const calls = { download: 0 };
  const make = (o) => new DockFileView(Object.assign({ path: "/p/x.bin", kind: "binary", meta: { size: 2048, mtime: 0 }, download: () => { calls.download++; } }, o));
  return { doc, ctx, appended, ros, calls, make, DockFileView, get };
}

// Every element under `n`, depth first.
const walk = (n, out = []) => { for (const c of n.children) { if (c.tag) { out.push(c); walk(c, out); } } return out; };
const q = (v, sel) => v.el.querySelector(sel);
const text = (v, sel) => { const n = q(v, sel); return n ? n.textContent : null; };
const facts = (v) => v.factsEl.children.map((c) => c.textContent);
const press = (n) => n.onclick({ target: n });

// An image view whose stage is `sw` x `sh` and whose picture has loaded at `nw` x `nh`.
function image(p, { nw = 2000, nh = 2000, sw = 800, sh = 600, size = 209203, mtime = 5, path: pth = "/p/big.png", fetchMeta } = {}) {
  const v = p.make({ path: pth, kind: "image", meta: { size, mtime }, fetchMeta });
  v.stage.clientWidth = sw; v.stage.clientHeight = sh;
  v.stage.rect = { left: 100, top: 50 };
  v.img.naturalWidth = nw; v.img.naturalHeight = nh;
  v.img.dispatchEvent({ type: "load" });
  v.onAttach();
  return v;
}
const settle = () => new Promise((r) => setImmediate(r));
const pct = (v) => text(v, ".dk-fv-pct");
const btn = (v, c) => q(v, ".dk-fv-" + c);
const wheel = (v, o) => { const e = Object.assign({ type: "wheel", ctrlKey: true, metaKey: false, deltaY: -100, deltaMode: 0, clientX: 300, clientY: 200 }, o); v.stage.dispatchEvent(e); return e; };

// --- the card -----------------------------------------------------------------------------------------------

test("a card shows a badge, the name, a meta line, the note and Download; Download is the tab's", () => {
  const p = page();
  const mtime = new Date(2020, 0, 2, 3, 4).getTime();
  const v = p.make({ path: "/p/data/app.sqlite", kind: "card", meta: { size: 3.4 * 1024 * 1024, mtime } });
  assert.equal(text(v, ".dk-fv-badge"), "DB", "a database is DB");
  assert.equal(text(v, ".dk-fv-name"), "app.sqlite");
  assert.equal(text(v, ".dk-fv-meta"), "SQLite database · 3.4 MB · modified 2020-01-02 03:04");
  assert.equal(text(v, ".dk-fv-note"), "CuliBox has no viewer for this type.");
  assert.equal(q(v, ".dk-fv-open"), null, "only a PDF opens in a tab");
  assert.equal(text(v, ".dk-fv-dl"), "Download");
  press(q(v, ".dk-fv-dl"));
  assert.equal(p.calls.download, 1);
  assert.deepEqual(facts(v), ["SQLITE", "3.4 MB"], "the status bar: type, size");
  assert.equal(v.el.dataset.kind, "card");
});

test("a card leaves out what is not known: no size, no modified time", () => {
  const p = page();
  assert.equal(text(p.make({ path: "/p/a.zip", kind: "card", meta: null }), ".dk-fv-meta"), "ZIP archive");
  assert.equal(text(p.make({ path: "/p/a.zip", kind: "card", meta: { size: 10, mtime: 0 } }), ".dk-fv-meta"), "ZIP archive · 10 bytes");
  const v = p.make({ path: "/p/a.zip", kind: "card", meta: null });
  assert.deepEqual(facts(v), ["ZIP"]);
});

test("a binary file with no extension is BIN and a Binary file; a too-large one with none is FILE and a File, with the limit note", () => {
  const p = page();
  const b = p.make({ path: "/p/blob", kind: "binary", meta: { size: 9, mtime: 0 } });
  assert.equal(text(b, ".dk-fv-badge"), "BIN");
  assert.equal(text(b, ".dk-fv-meta"), "Binary file · 9 bytes");
  assert.equal(text(b, ".dk-fv-note"), "Not text, and CuliBox has no viewer for this type.");
  assert.deepEqual(facts(b), ["FILE", "9 bytes"]);
  const t = p.make({ path: "/p/server", kind: "toolarge", meta: { size: 3 * 1024 * 1024, mtime: 0 } });
  assert.equal(text(t, ".dk-fv-badge"), "FILE");
  assert.equal(text(t, ".dk-fv-meta"), "File · 3.0 MB");
  assert.equal(text(t, ".dk-fv-note"), "Larger than the 2 MB preview limit. Download it to read it.");
  const log = p.make({ path: "/p/a.log", kind: "toolarge", meta: { size: 3 * 1024 * 1024, mtime: 0 } });
  assert.equal(text(log, ".dk-fv-meta"), "LOG file · 3.0 MB");
});

test("a file name is text, never markup: a hostile name and a hostile extension add no element", () => {
  const p = page();
  for (const name of ["<img src=x onerror=alert(1)>.bin", "a.<svg onload=alert(1)>", "<b onclick=x>.zip", "<script>x;.zip"]) {
    const v = p.make({ path: "/p/" + name, kind: "binary", meta: null });
    const tags = walk(v.el).map((n) => n.tag);
    for (const bad of ["img", "svg", "script", "b", "iframe"]) assert.ok(!tags.includes(bad), name + ": no <" + bad + ">");
    assert.equal(text(v, ".dk-fv-name"), name, "the name as typed, as text");
    assert.equal(q(v, ".dk-fv-badge").children.length, 0, "the badge holds text only");
    assert.ok(q(v, ".dk-fv-badge").textContent.length <= 4);
  }
  // and the one place an element is made with the name in a property, the image's alt, keeps it a string
  const v = p.make({ path: "/p/<img src=x onerror=alert(1)>.png", kind: "image", meta: null });
  assert.equal(v.img.alt, "<img src=x onerror=alert(1)>.png");
  assert.equal(walk(v.el).filter((n) => n.tag === "img").length, 1, "the one image of the view");
});

// --- the PDF ------------------------------------------------------------------------------------------------

test("a PDF is a card with Open in new tab (primary) then Download, its note and its status bar", () => {
  const p = page();
  const v = p.make({ path: "/p/docs/report.pdf", kind: "pdf", meta: { size: 1.2 * 1024 * 1024, mtime: 0 } });
  assert.equal(text(v, ".dk-fv-badge"), "PDF");
  assert.equal(text(v, ".dk-fv-meta"), "PDF document · 1.2 MB");
  assert.equal(text(v, ".dk-fv-note"), "Opens in its own browser tab, sandboxed: nothing in the PDF can reach CuliBox.");
  const acts = q(v, ".dk-fv-acts").children.map((c) => c.textContent);
  assert.deepEqual(acts, ["Open in new tab", "Download"]);
  assert.deepEqual(facts(v), ["PDF", "1.2 MB"]);
  assert.equal(v.openable, true);
  assert.deepEqual(walk(v.el).map((n) => n.tag).filter((t) => ["iframe", "object", "embed"].includes(t)), [], "a PDF is never embedded");
});

test("Open in new tab is a hidden same-origin link to the raw URL, _blank and noopener, clicked and removed", () => {
  const p = page();
  const v = p.make({ path: "/p/docs/my report.pdf", kind: "pdf", meta: { size: 5, mtime: 77 } });
  let clicked = 0;
  // the link is made by the view: watch its click through document.createElement
  const make = p.doc.createElement;
  p.doc.createElement = (t) => { const e = make(t); if (t === "a") e.click = () => { clicked++; }; return e; };
  press(q(v, ".dk-fv-open"));
  assert.equal(p.appended.length, 1);
  const a = p.appended[0];
  assert.equal(a.tag, "a");
  assert.equal(a.href, "/api/fs/read?path=" + encodeURIComponent("/p/docs/my report.pdf") + "&raw=1&v=77");
  assert.ok(/[?&]raw=1(&|$)/.test(a.href));
  assert.equal(a.target, "_blank");
  assert.equal(a.rel, "noopener");
  assert.equal(clicked, 1);
  assert.equal(a.parent, null, "not left in the page");
  // a box file goes by the box route
  const b = p.make({ path: "/p/r.pdf", box: "s1", kind: "pdf", meta: null });
  b.open();
  assert.equal(p.appended[1].href, "/api/sessions/s1/boxfile?path=" + encodeURIComponent("/p/r.pdf") + "&raw=1");
});

// --- video and audio ----------------------------------------------------------------------------------------

test("a video is a native <video controls preload=metadata> on the raw URL; its bar fills in as the metadata arrives", () => {
  const p = page();
  const v = p.make({ path: "/p/run-1.webm", kind: "video", meta: { size: 3.1 * 1024 * 1024, mtime: 9 } });
  const m = v.media;
  assert.equal(m.tag, "video");
  assert.equal(m.controls, true);
  assert.equal(m.preload, "metadata");
  assert.equal(m.src, "/api/fs/read?path=" + encodeURIComponent("/p/run-1.webm") + "&raw=1&v=9");
  assert.deepEqual(facts(v), ["WEBM", "3.1 MB"], "nothing about the picture before the metadata");
  m.duration = 42.4; m.videoWidth = 1280; m.videoHeight = 720;
  m.dispatchEvent({ type: "loadedmetadata" });
  assert.deepEqual(facts(v), ["1280 × 720", "0:42", "WEBM", "3.1 MB"]);
  m.duration = 3725;
  m.dispatchEvent({ type: "durationchange" });
  assert.equal(facts(v)[1], "1:02:05");
  m.duration = Infinity;
  m.dispatchEvent({ type: "durationchange" });
  assert.deepEqual(facts(v), ["1280 × 720", "WEBM", "3.1 MB"], "a stream has no length to show");
});

test("an audio file is <audio controls> with its length, type and size; no pixel size", () => {
  const p = page();
  const v = p.make({ path: "/p/voice.mp3", box: "s1", kind: "audio", meta: { size: 640 * 1024, mtime: 0 } });
  assert.equal(v.media.tag, "audio");
  assert.equal(v.media.src, "/api/sessions/s1/boxfile?path=" + encodeURIComponent("/p/voice.mp3") + "&raw=1");
  v.media.duration = 42;
  v.media.dispatchEvent({ type: "loadedmetadata" });
  assert.deepEqual(facts(v), ["0:42", "MP3", "640.0 KB"]);
  assert.equal(q(v, ".dk-sb-r").children.length, 0, "no zoom controls");
});

test("media pauses when the tab is hidden, and on dispose its src is taken off and load() called", () => {
  const p = page();
  const v = p.make({ path: "/p/a.mp4", kind: "video", meta: null });
  const m = v.media;
  v.onAttach();
  v.onHide();
  assert.deepEqual(m.calls, ["pause"]);
  assert.notEqual(m.getAttribute("src"), null);
  v.dispose();
  assert.equal(m.getAttribute("src"), null, "the stream is let go");
  assert.equal(m.calls[m.calls.length - 1], "load", "load() after the src is off, so the browser drops the fetch");
  v.dispose(); // twice is harmless
});

test("a media error (a codec the browser cannot play) turns the view into the card with the reason, and releases the media", () => {
  const p = page();
  const v = p.make({ path: "/p/a.mp4", kind: "video", meta: { size: 100, mtime: 0 } });
  const m = v.media;
  let updates = 0;
  v.onUpdate = () => { updates++; };
  m.dispatchEvent({ type: "error" });
  assert.equal(v.kind, "card");
  assert.equal(v.el.dataset.kind, "card");
  assert.equal(text(v, ".dk-fv-note"), "This browser cannot play this file.");
  assert.equal(text(v, ".dk-fv-meta"), "MP4 file · 100 bytes");
  assert.equal(text(v, ".dk-fv-dl"), "Download");
  assert.equal(q(v, ".dk-fv-media"), null);
  assert.equal(m.getAttribute("src"), null);
  assert.ok(m.calls.includes("load"));
  assert.equal(updates, 1);
  assert.deepEqual(facts(v), ["MP4", "100 bytes"]);
  // a late event from the dropped element changes nothing
  m.dispatchEvent({ type: "error" });
  assert.equal(updates, 1);
});

// --- the image: the bar, Fit, steps -------------------------------------------------------------------------

test("an image's bar: pixel size, type and size on the left; Fit, 1:1, -, the percentage, + on the right; Fit is pressed and the picture is fitted", () => {
  const p = page();
  const v = image(p);
  assert.deepEqual(facts(v), ["2000 × 2000", "PNG", "204.3 KB"]);
  assert.deepEqual(q(v, ".dk-sb-r").children.map((c) => c.textContent), ["Fit", "1:1", "−", "30%", "+"]);
  assert.equal(btn(v, "fit").getAttribute("aria-pressed"), "true");
  assert.equal(v.scale, 0.3, "min(1, 800/2000, 600/2000)");
  assert.equal(v.img.style.width, "600px");
  assert.equal(v.img.style.height, "600px");
  assert.equal(v.canvas.style.width, "800px", "the canvas is max(picture, stage)");
  assert.equal(v.img.style.left, "100px", "centred: (800 - 600) / 2");
  assert.equal(v.img.style.top, "0px");
  assert.ok(v.stage.classList.contains("dk-fv-placed"));
  assert.ok(!v.stage.classList.contains("dk-fv-pan"), "fitted: nothing to pan");
  assert.equal(v.img.src, "/api/fs/read?path=" + encodeURIComponent("/p/big.png") + "&v=5");
  for (const b of ["fit", "one", "out", "in"]) assert.equal(btn(v, b).disabled, false, b);
  assert.equal(btn(v, "fit").getAttribute("aria-label"), "Fit to view");
  assert.equal(btn(v, "out").getAttribute("aria-label"), "Zoom out");
});

test("a small picture is not enlarged: Fit is 100 % and it sits in the middle of the stage", () => {
  const p = page();
  const v = image(p, { nw: 320, nh: 200 });
  assert.equal(pct(v), "100%");
  assert.equal(v.img.style.left, "240px");
  assert.equal(v.img.style.top, "200px");
  assert.equal(v.canvas.style.height, "600px");
});

test("1:1, then + and - walk the steps from the scale now, leave Fit mode, and are off at the ends", () => {
  const p = page();
  const v = image(p);
  press(btn(v, "in")); // 30 % -> 33 %
  assert.equal(pct(v), "33%");
  assert.equal(btn(v, "fit").getAttribute("aria-pressed"), "false", "an explicit zoom leaves Fit mode");
  press(btn(v, "one"));
  assert.equal(pct(v), "100%");
  press(btn(v, "in"));
  assert.equal(pct(v), "125%");
  press(btn(v, "out")); press(btn(v, "out"));
  assert.equal(pct(v), "75%");
  assert.ok(v.stage.classList.contains("dk-fv-pan"), "larger than the view");
  for (let i = 0; i < 20; i++) press(btn(v, "out"));
  assert.equal(pct(v), "5%");
  assert.equal(btn(v, "out").disabled, true);
  assert.equal(btn(v, "in").disabled, false);
  for (let i = 0; i < 20; i++) press(btn(v, "in"));
  assert.equal(pct(v), "800%");
  assert.equal(btn(v, "in").disabled, true);
  assert.equal(btn(v, "out").disabled, false);
  press(btn(v, "fit"));
  assert.equal(pct(v), "30%");
  assert.equal(btn(v, "fit").getAttribute("aria-pressed"), "true");
  assert.equal(btn(v, "in").disabled, false);
});

test("a resize of the dock refits in Fit mode and leaves an explicit zoom alone", () => {
  const p = page();
  const v = image(p);
  assert.equal(p.ros.length, 1);
  assert.equal(p.ros[0].el, v.stage, "the stage is what is watched");
  v.stage.clientWidth = 400; v.stage.clientHeight = 300;
  p.ros[0].cb();
  assert.equal(pct(v), "15%", "min(400/2000, 300/2000)");
  press(btn(v, "one"));
  v.stage.clientWidth = 1000;
  p.ros[0].cb();
  assert.equal(pct(v), "100%");
  assert.equal(v.canvas.style.width, "2000px");
});

test("a stage that has no size (hidden) is never fitted to: no scale 0, no NaN; the next measurement fits", () => {
  const p = page();
  const v = image(p, { sw: 0, sh: 0 });
  assert.equal(v.scale, 1, "unfitted, not 0");
  assert.equal(v.img.style.width, undefined, "nothing was laid out");
  assert.ok(!v.stage.classList.contains("dk-fv-placed"), "and the picture is not shown unplaced");
  wheel(v, { deltaY: -300 }); // a zoom over a stage with no size does nothing, rather than computing with it
  press(btn(v, "one"));
  assert.equal(v.scale, 1);
  assert.ok(!Number.isNaN(v.stage.scrollLeft || 0));
  v.stage.clientWidth = 800; v.stage.clientHeight = 600;
  p.ros[0].cb();
  assert.equal(v.scale, 0.3);
  assert.equal(v.img.style.width, "600px");
  assert.ok(v.stage.classList.contains("dk-fv-placed"));
});

test("a picture with no size of its own (an SVG with no width) is shown as it comes and cannot be zoomed", () => {
  const p = page();
  const v = image(p, { nw: 0, nh: 0, path: "/p/logo.svg" });
  assert.ok(v.stage.classList.contains("dk-fv-unsized"));
  assert.deepEqual(facts(v), ["SVG", "204.3 KB"], "no pixel size to say");
  assert.equal(pct(v), "");
  for (const b of ["fit", "one", "out", "in"]) assert.equal(btn(v, b).disabled, true, b);
  wheel(v, {});
  v.stage.dispatchEvent({ type: "dblclick", clientX: 5, clientY: 5 });
  assert.equal(v.scale, 1);
});

test("an image that cannot be loaded becomes the card with the reason", () => {
  const p = page();
  const v = p.make({ path: "/p/broken.png", kind: "image", meta: { size: 12, mtime: 0 } });
  const img = v.img;
  img.dispatchEvent({ type: "error" });
  assert.equal(v.kind, "card");
  assert.equal(text(v, ".dk-fv-note"), "Could not load this image.");
  assert.equal(text(v, ".dk-fv-meta"), "PNG file · 12 bytes");
  assert.equal(p.ros[0].gone, true, "its observer is let go");
  assert.deepEqual(facts(v), ["PNG", "12 bytes"]);
  assert.equal(q(v, ".dk-sb-r").children.length, 0, "the zoom controls go with the picture");
});

// --- the image: wheel, pinch, drag, double-click ------------------------------------------------------------

// The picture point (natural pixels) under the view point (px, py), from the stage's scroll and the layout the view made.
const under = (v, px, py) => {
  const left = parseFloat(v.img.style.left), top = parseFloat(v.img.style.top);
  return { x: (v.stage.scrollLeft + px - left) / v.scale, y: (v.stage.scrollTop + py - top) / v.scale };
};

test("Ctrl+wheel zooms around the pointer and stops the page's own zoom; a plain wheel is left to scroll", () => {
  const p = page();
  const v = image(p);
  const plain = wheel(v, { ctrlKey: false });
  assert.equal(plain.defaultPrevented, false, "a plain wheel is not touched");
  assert.equal(v.scale, 0.3);
  assert.equal(v.fit, true);
  // zoom to 100 % first so there is something to scroll, scroll somewhere, then zoom with the pointer at (200, 150) in the stage
  press(btn(v, "one"));
  v.stage.scrollLeft = 500; v.stage.scrollTop = 400;
  const before = under(v, 200, 150);
  const e = wheel(v, { deltaY: -300, clientX: 100 + 200, clientY: 50 + 150 }); // the stage's rect starts at (100, 50)
  assert.equal(e.defaultPrevented, true);
  assert.ok(Math.abs(v.scale - Math.E) < 1e-9, "scale x e^(300/300)");
  assert.equal(v.fit, false);
  const after = under(v, 200, 150);
  assert.ok(Math.abs(after.x - before.x) < 1e-6 && Math.abs(after.y - before.y) < 1e-6, "the point under the pointer stays under it");
  assert.equal(pct(v), "272%");
  // Cmd (metaKey) does the same, and the scale stops at 800 %
  const m = wheel(v, { ctrlKey: false, metaKey: true, deltaY: -3000 });
  assert.equal(m.defaultPrevented, true);
  assert.equal(v.scale, 8);
  wheel(v, { deltaY: 100000 });
  assert.equal(v.scale, 0.05);
});

test("Safari's pinch (gesturestart / gesturechange) zooms from where the gesture began, and is not the page's pinch", () => {
  const p = page();
  const v = image(p);
  press(btn(v, "one"));
  const start = { type: "gesturestart", scale: 1 };
  v.stage.dispatchEvent(start);
  assert.equal(start.defaultPrevented, true);
  const a = { type: "gesturechange", scale: 1.5, clientX: 300, clientY: 200 };
  v.stage.dispatchEvent(a);
  assert.equal(a.defaultPrevented, true);
  assert.equal(v.scale, 1.5);
  const b = { type: "gesturechange", scale: 2, clientX: 300, clientY: 200 };
  v.stage.dispatchEvent(b);
  assert.equal(v.scale, 2, "relative to the start (1), not to the last change");
});

test("a drag pans (pointer capture, grabbing) only when the picture is larger than the view", () => {
  const p = page();
  const v = image(p);
  const down = (x, y) => v.stage.dispatchEvent({ type: "pointerdown", button: 0, pointerId: 7, clientX: x, clientY: y });
  const move = (x, y) => v.stage.dispatchEvent({ type: "pointermove", pointerId: 7, clientX: x, clientY: y });
  const up = () => v.stage.dispatchEvent({ type: "pointerup", pointerId: 7 });
  // fitted: nothing to pan
  down(300, 300); move(250, 250);
  assert.equal(v.stage.captured, undefined);
  assert.equal(v.stage.scrollLeft || 0, 0);
  up();
  // larger: a drag to the left and up scrolls right and down by the same amount
  press(btn(v, "one"));
  assert.ok(v.stage.classList.contains("dk-fv-pan"));
  v.stage.scrollLeft = 500; v.stage.scrollTop = 400;
  down(300, 300);
  assert.equal(v.stage.captured, 7);
  assert.ok(v.stage.classList.contains("dk-fv-grabbing"));
  move(250, 270);
  assert.equal(v.stage.scrollLeft, 550);
  assert.equal(v.stage.scrollTop, 430);
  up();
  assert.ok(!v.stage.classList.contains("dk-fv-grabbing"));
  move(100, 100);
  assert.equal(v.stage.scrollLeft, 550, "no drag after the button is up");
  // only the primary button
  v.stage.dispatchEvent({ type: "pointerdown", button: 2, pointerId: 8, clientX: 1, clientY: 1 });
  assert.ok(!v.stage.classList.contains("dk-fv-grabbing"));
  assert.equal(v.drag, null);
});

test("a double-click toggles Fit and 100 % around the pointer", () => {
  const p = page();
  const v = image(p);
  v.stage.dispatchEvent({ type: "dblclick", clientX: 100 + 400, clientY: 50 + 300 });
  assert.equal(pct(v), "100%");
  assert.equal(v.fit, false);
  assert.equal(btn(v, "fit").getAttribute("aria-pressed"), "false");
  v.stage.dispatchEvent({ type: "dblclick", clientX: 300, clientY: 200 });
  assert.equal(pct(v), "30%", "back to Fit");
  assert.equal(v.fit, true);
  assert.equal(btn(v, "fit").getAttribute("aria-pressed"), "true");
  assert.equal(v.stage.scrollLeft, 0);
});

test("zooming in with the pointer keeps the point under it (the picture's middle stays in the middle)", () => {
  const p = page();
  const v = image(p);
  press(btn(v, "one")); // around the view's middle
  const mid = under(v, 400, 300);
  assert.ok(Math.abs(mid.x - 1000) < 1, "the middle of a 2000 px picture, at scale 1 around the view's middle");
  press(btn(v, "in"));
  const now = under(v, 400, 300);
  assert.ok(Math.abs(now.x - mid.x) < 1e-6 && Math.abs(now.y - mid.y) < 1e-6);
});

// --- fresh content ------------------------------------------------------------------------------------------

test("the first attach asks nothing; every later one looks at the file again, and a changed time reloads the picture, keeping the zoom", async () => {
  const p = page();
  const asked = [];
  let answer = { size: 300, mtime: 5 };
  const v = image(p, { fetchMeta: async () => { asked.push(1); return answer; } });
  assert.equal(asked.length, 0, "the open's own meta was the tab's");
  press(btn(v, "one"));
  v.onHide();
  v.onAttach();
  await settle();
  assert.equal(asked.length, 1);
  assert.equal(v.img.src, "/api/fs/read?path=" + encodeURIComponent("/p/big.png") + "&v=5", "same time: the same picture");
  assert.equal(pct(v), "100%");
  answer = { size: 999, mtime: 8 };
  v.onHide();
  v.onAttach();
  await settle();
  assert.equal(v.img.src, "/api/fs/read?path=" + encodeURIComponent("/p/big.png") + "&v=8", "a new time busts the cache");
  assert.deepEqual(facts(v), ["2000 × 2000", "PNG", "999 bytes"], "the new size");
  v.img.dispatchEvent({ type: "load" }); // the same pixel size: the zoom is kept
  assert.equal(pct(v), "100%");
  assert.equal(v.fit, false);
  // a picture of another size starts again at Fit
  answer = { size: 10, mtime: 9 };
  v.onHide(); v.onAttach();
  await settle();
  v.img.naturalWidth = 1000; v.img.naturalHeight = 1000;
  v.img.dispatchEvent({ type: "load" });
  assert.equal(v.fit, true);
  assert.equal(pct(v), "60%");
  assert.deepEqual(facts(v)[0], "1000 × 1000");
});

test("a failed look at the file changes nothing; an answer for a disposed view is dropped", async () => {
  const p = page();
  let answer = null;
  const v = image(p, { fetchMeta: async () => answer });
  v.onHide(); v.onAttach();
  await settle();
  assert.ok(v.img.src.endsWith("&v=5"));
  answer = { size: 1, mtime: 99 };
  v.onHide(); v.onAttach();
  v.dispose();
  await settle();
  assert.equal(v.img, null, "the picture is let go, and the late answer did not bring one back");
});

test("a card's and a player's facts follow a changed time too; media gets the new src", async () => {
  const p = page();
  let answer = { size: 4096, mtime: 20 };
  const c = p.make({ path: "/p/a.zip", kind: "card", meta: { size: 10, mtime: 5 }, fetchMeta: async () => answer });
  c.onAttach(); c.onHide(); c.onAttach();
  await settle();
  assert.deepEqual(facts(c), ["ZIP", "4.0 KB"]);
  assert.ok(text(c, ".dk-fv-meta").startsWith("ZIP archive · 4.0 KB"));
  const m = p.make({ path: "/p/a.mp3", kind: "audio", meta: { size: 10, mtime: 5 }, fetchMeta: async () => answer });
  m.onAttach(); m.onHide(); m.onAttach();
  await settle();
  assert.ok(m.media.src.endsWith("&raw=1&v=20"));
});

test("a card that an error made tries the file again when its time changes: a repaired image is an image again", async () => {
  const p = page();
  const v = p.make({ path: "/p/broken.png", kind: "image", meta: { size: 12, mtime: 1 }, fetchMeta: async () => ({ size: 3000, mtime: 2 }) });
  v.img.dispatchEvent({ type: "error" });
  assert.equal(v.kind, "card");
  v.onAttach();
  v.onHide(); v.onAttach();
  await settle();
  assert.equal(v.kind, "image");
  assert.equal(v.el.dataset.kind, "image");
  assert.equal(v.img.src, "/api/fs/read?path=" + encodeURIComponent("/p/broken.png") + "&v=2");
  assert.equal(q(v, ".dk-fv-note"), null);
  assert.equal(q(v, ".dk-sb-r").children.length, 5, "the zoom controls are back");
  // a card that was always a card stays one
  const c = p.make({ path: "/p/a.zip", kind: "card", meta: { size: 1, mtime: 1 }, fetchMeta: async () => ({ size: 3000, mtime: 2 }) });
  c.onAttach(); c.onHide(); c.onAttach();
  await settle();
  assert.equal(c.kind, "card");
});

test("the image's scroll is kept across a hide and a show", () => {
  const p = page();
  const v = image(p);
  press(btn(v, "one"));
  v.stage.scrollLeft = 120; v.stage.scrollTop = 80;
  v.onHide();
  v.stage.scrollLeft = 0; v.stage.scrollTop = 0; // a detached element loses it
  v.onAttach();
  assert.equal(v.stage.scrollLeft, 120);
  assert.equal(v.stage.scrollTop, 80);
});

// --- the inner-viewer interface -----------------------------------------------------------------------------

test("the interface EditorTab calls on its inner viewer: all there, the ones that make no sense for a picture are no-ops", () => {
  const p = page();
  const v = image(p);
  assert.equal(v.scroll, 0);
  assert.equal(typeof v.wrapAnchor(), "function");
  v.wrapAnchor()();
  v.applyWrap();
  v.revealLine(3, 1);
  v.onShow();
  v.focusReader();
  assert.equal(p.doc.activeElement, v.el);
  assert.equal(v.onUpdate, null, "a field the tab assigns");
  assert.equal(v.toolbarApi, undefined, "no toolbarApi: gitPaintTree and drawerPaint call it where it exists");
  v.dispose();
  assert.equal(p.ros[0].gone, true);
  v.onHide(); // after a dispose, harmless
});

test("the card's badge says DB for a database and NB for a notebook; any other type is its extension, 4 characters at most", () => {
  const p = page();
  for (const [file, badge] of [
    ["/p/a.sqlite", "DB"], ["/p/a.sqlite3", "DB"], ["/p/a.db", "DB"], ["/p/A.SQLITE", "DB"], ["/p/n.ipynb", "NB"],
    ["/p/a.zip", "ZIP"], ["/p/a.epub", "EPUB"], ["/p/a.docx", "DOCX"], ["/p/a.xlsm", "XLSM"],
  ]) {
    const v = p.make({ path: file, kind: "card", meta: { size: 1, mtime: 1 } });
    assert.equal(text(v, ".dk-fv-badge"), badge, file);
  }
});

test("a press on the stage's own scrollbar does not pan; a press on the picture does, whatever offsetX says (the target is the <img>)", () => {
  const p = page();
  const v = image(p);
  press(btn(v, "one"));
  v.stage.scrollLeft = 500; v.stage.scrollTop = 400;
  // the stage is 800 x 600 at (50, 20) on the page; the gutter is what lies past that, in the stage's own box
  v.stage.getBoundingClientRect = () => ({ left: 50, top: 20, width: 815, height: 615 });
  const press1 = (clientX, clientY, offsetX, offsetY) =>
    v.stage.dispatchEvent({ type: "pointerdown", button: 0, pointerId: 9, clientX, clientY, offsetX, offsetY });
  press1(50 + 805, 120, 5, 100);
  assert.equal(v.stage.captured, undefined, "the vertical scrollbar");
  assert.equal(v.drag, null);
  assert.ok(!v.stage.classList.contains("dk-fv-grabbing"));
  press1(150, 20 + 610, 100, 5);
  assert.equal(v.stage.captured, undefined, "the horizontal scrollbar");
  assert.equal(v.drag, null);
  // the target is the 2000 px <img>: its offsets run past the stage's size, and the press is still a pan
  press1(150, 120, 1500, 1200);
  assert.equal(v.stage.captured, 9);
  assert.ok(v.drag);
});
