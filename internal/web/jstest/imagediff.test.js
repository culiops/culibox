"use strict";
// The image comparer (imagediff.js) as it runs on the page, in a vm: the real imagediff.js, editordiff.js (its segmented control and
// label helpers), reviewmodel.js (swipeClip, onionOpacity), viewerprefs.js and copyfile.js, against the fake DOM of fakedom.js and a fake
// fetch, URL and timer. It proves what the browser check cannot see cheaply: what is asked (and not), that every blob URL made is revoked
// and every request aborted when a mount goes, that no markup is ever built from an image, what each mode puts on the page and what a
// key or a pointer does to the divider and the blend. The look is the visual check's.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const settle = async () => { for (let i = 0; i < 8; i++) await new Promise((r) => setImmediate(r)); };

function page() {
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => {
    const n = new Node(t, doc);
    if (t === "img") { n.naturalWidth = 0; n.naturalHeight = 0; n.src = ""; }
    // A string child is a text node, as in a browser (editorDiffPaintLabel passes one).
    const replace = n.replaceChildren;
    n.replaceChildren = (...cs) => replace.apply(n, cs.map((c) => (typeof c === "string" ? new Text(c) : c)));
    return n;
  };
  const st = { fetches: [], created: [], revoked: [], timers: [], toasts: [], store: new Map(), nBlob: 0 };
  const fetch = (url, init) => new Promise((resolve, reject) => {
    const f = { url, init, body: JSON.parse(init.body), resolve, reject, signal: init.signal };
    if (init.signal) init.signal.addEventListener("abort", () => reject(Object.assign(new Error("aborted"), { name: "AbortError" })));
    st.fetches.push(f);
  });
  const ctx = vm.createContext({
    console, Array, Object, Set, Map, Math, Number, JSON, Date, Promise, String, Error, AbortController, RegExp,
    document: doc,
    fetch,
    localStorage: { getItem: (k) => (st.store.has(k) ? st.store.get(k) : null), setItem: (k, v) => { st.store.set(k, v); } },
    URL: { createObjectURL: (b) => { st.created.push(b); return "blob:t/" + ++st.nBlob; }, revokeObjectURL: (u) => { st.revoked.push(u); } },
    setTimeout: (fn, ms) => { st.timers.push({ fn, ms, live: true }); return st.timers.length; },
    clearTimeout: (id) => { if (st.timers[id - 1]) st.timers[id - 1].live = false; },
    svgIcon: () => new Node("svg", doc),
    showToast: (m) => { st.toasts.push(m); },
    IS_MAC: false,
  });
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  for (const f of ["copyfile.js", "viewerprefs.js", "reviewmodel.js", "editordiff.js", "imagediff.js"]) load(src(f), f);
  return { ctx, st, doc, run: (code) => load(code, "test") };
}

// A response as fetch gives it: an image (a Blob-like body), or the JSON of the text form.
const imgRes = (type) => ({ ok: true, status: 200, headers: { get: (h) => (h.toLowerCase() === "content-type" ? type || "image/png" : null) }, blob: async () => ({ blob: true, type: type || "image/png" }) });
const jsonRes = (body) => ({ ok: true, status: 200, headers: { get: () => "application/json" }, json: async () => body });
const statusRes = (status) => ({ ok: false, status, headers: { get: () => "application/json" }, json: async () => ({ error: "x" }) });

function mount(p, opts, scope, file) {
  const el = new Node("div", p.doc);
  const h = p.ctx.imageDiffMount(el, file || "/w/img/logo.png", scope || "head", opts);
  return { el, h };
}
const texts = (el, sel) => el.querySelectorAll(sel).map((n) => n.textContent);
const loadImg = (img, w, h) => { img.naturalWidth = w; img.naturalHeight = h; img.onload(); };

test("imageDiffIs is the viewer's image set, case aside", () => {
  const p = page();
  for (const f of ["/a/x.png", "/a/x.JPG", "/a/x.jpeg", "/a/x.gif", "/a/x.webp", "/a/x.svg"]) assert.equal(p.ctx.imageDiffIs(f), true, f);
  for (const f of ["/a/x.go", "/a/png", "/a/x.png.txt", "/a/x.pdf"]) assert.equal(p.ctx.imageDiffIs(f), false, f);
});

test("a changed image asks for its old side once, raw, with the scope, and shows the new side from today's file", async () => {
  const p = page();
  const { el } = mount(p, { key: "k1" });
  assert.equal(p.st.fetches.length, 1);
  assert.equal(p.st.fetches[0].url, "/api/git/show");
  assert.equal(p.st.fetches[0].init.method, "POST");
  assert.deepEqual(p.st.fetches[0].body, { path: "/w/img/logo.png", scope: "head", raw: true });
  assert.equal(el.querySelectorAll("img").length, 1, "the new side is on the page at once, the old is being fetched");
  assert.equal(el.querySelector("img").src, "/api/fs/read?path=%2Fw%2Fimg%2Flogo.png", "that one is the new side");
  assert.equal(texts(el, ".dk-imgd-msg")[0], "Loading the old version…");
  p.st.fetches[0].resolve(imgRes());
  await settle();
  assert.deepEqual(p.st.created.length, 1);
  assert.equal(el.querySelectorAll("img").length, 2);
  assert.deepEqual(texts(el, ".dk-imgd-lab"), ["Old · HEAD", "New · working tree"]);
});

test("the new side is the server's raw image route, the path encoded; nothing else reads it", async () => {
  const p = page();
  const { el } = mount(p, {}, "head", "/w/my dir/a&b.svg");
  const [first] = el.querySelectorAll("img");
  assert.equal(p.st.fetches.length, 1, "only the old side is a request of the comparer's");
  // The new image is the one in the page already (the old is not yet): its src is the route.
  const news = el.querySelectorAll("img").filter((i) => i.src);
  assert.equal(news.length, 1);
  assert.equal(news[0].src, "/api/fs/read?path=%2Fw%2Fmy%20dir%2Fa%26b.svg");
  assert.ok(first);
});

test("Branch scope says merge base, and the request carries the scope", async () => {
  const p = page();
  const { el } = mount(p, {}, "branch");
  assert.equal(p.st.fetches[0].body.scope, "branch");
  p.st.fetches[0].resolve(imgRes());
  await settle();
  assert.deepEqual(texts(el, ".dk-imgd-lab"), ["Old · merge base", "New · working tree"]);
});

test("under each image sits its width x height, from the loaded picture", async () => {
  const p = page();
  const { el } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  const [a, b] = el.querySelectorAll("img");
  assert.deepEqual(texts(el, ".dk-imgd-cap"), ["", ""], "nothing before they load");
  loadImg(a, 640, 480);
  assert.deepEqual(texts(el, ".dk-imgd-cap"), ["640 × 480", ""]);
  loadImg(b, 800, 600);
  assert.deepEqual(texts(el, ".dk-imgd-cap"), ["640 × 480", "800 × 600"]);
});

test("an SVG with no natural size says so rather than 0 × 0", async () => {
  const p = page();
  const { el } = mount(p, {}, "head", "/w/i.svg");
  p.st.fetches[0].resolve(imgRes("image/svg+xml"));
  await settle();
  const [a, b] = el.querySelectorAll("img");
  loadImg(a, 0, 0);
  loadImg(b, 24, 24);
  assert.deepEqual(texts(el, ".dk-imgd-cap"), ["no fixed size", "24 × 24"]);
});

test("an SVG goes only through <img>: the blob's type is kept, no markup is built, no text is read", async () => {
  const p = page();
  const { el } = mount(p, {}, "head", "/w/i.svg");
  let textRead = false;
  const res = imgRes("image/svg+xml");
  res.text = async () => { textRead = true; return "<svg onload=alert(1)>"; };
  p.st.fetches[0].resolve(res);
  await settle();
  assert.equal(textRead, false);
  assert.equal(p.st.created[0].type, "image/svg+xml");
  assert.deepEqual(el.querySelectorAll("img").length, 2);
  assert.equal(el.querySelector(".dk-imgd-stage").querySelectorAll("svg").length, 0, "no <svg> element in the comparer's picture area");
  const code = src("imagediff.js").replace(/\/\/.*$/gm, "");
  for (const bad of ["innerHTML", "outerHTML", "insertAdjacentHTML", "DOMParser", "document.write", "createContextualFragment", "srcdoc", "<svg"]) {
    assert.ok(!code.includes(bad), bad + " is not used");
  }
  assert.ok(!/res\.text\(|\.text\(\)/.test(code), "the response is never read as text");
});

test("an added image (the base has no such file) shows only its new side, with no mode to choose", async () => {
  const p = page();
  const { el } = mount(p, { key: "k" });
  p.st.fetches[0].resolve(jsonRes({ state: "ok", missing: true }));
  await settle();
  assert.equal(p.st.created.length, 0);
  assert.deepEqual(texts(el, ".dk-imgd-lab"), ["New · working tree"]);
  assert.equal(el.querySelector(".dk-imgd-bar").hidden, true);
  assert.equal(el.querySelectorAll("img").length, 1);
  assert.ok(el.querySelector(".dk-imgd-side").className.includes("one"));
});

test("a deleted image shows only its old side and does not ask for the new one", async () => {
  const p = page();
  const { el } = mount(p, { deleted: true });
  assert.equal(el.querySelectorAll("img").filter((i) => i.src).length, 0, "no request for today's file");
  p.st.fetches[0].resolve(imgRes());
  await settle();
  assert.deepEqual(texts(el, ".dk-imgd-lab"), ["Old · HEAD"]);
  assert.equal(el.querySelector(".dk-imgd-bar").hidden, true);
  assert.equal(el.querySelectorAll("img").length, 1);
});

test("what cannot be had is said in the side's place, and the other side is still shown", async () => {
  const cases = [
    [statusRes(413), "Could not load the old version: it is over 10 MiB, too big to compare"],
    [statusRes(500), "Could not load the old version: git could not give it (HTTP 500)"],
    [jsonRes({ state: "notrepo" }), "Could not load the old version: this file is not in a git repository"],
    [jsonRes({ state: "unavailable", reason: "git is not installed" }), "Could not load the old version: git is not installed"],
    [jsonRes({ state: "unavailable" }), "Could not load the old version: git is not available here"],
  ];
  for (const [res, want] of cases) {
    const p = page();
    const { el } = mount(p, {});
    p.st.fetches[0].resolve(res);
    await settle();
    assert.deepEqual(texts(el, ".dk-imgd-msg"), [want]);
    assert.equal(el.querySelectorAll("img").length, 1, "the new side is there");
    assert.equal(el.querySelector(".dk-imgd-bar").hidden, true, "no stacking with a side that failed");
    assert.equal(p.st.created.length, 0);
  }
});

test("a request that fails says so; one that never answers is aborted after the timeout", async () => {
  let p = page();
  let m = mount(p, {});
  p.st.fetches[0].reject(new Error("net"));
  await settle();
  assert.deepEqual(texts(m.el, ".dk-imgd-msg"), ["Could not load the old version: the request failed"]);

  p = page();
  m = mount(p, {});
  assert.equal(p.st.timers[0].ms, 20000);
  p.st.timers[0].fn();
  await settle();
  assert.equal(p.st.fetches[0].signal.aborted, true);
  assert.deepEqual(texts(m.el, ".dk-imgd-msg"), ["Could not load the old version: git took too long"]);
  assert.equal(p.st.timers[0].live, false, "the timer is cleared when the request ends");
});

test("a new side that does not load says so, and the old one is shown beside it", async () => {
  const p = page();
  const { el } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  const imgs = el.querySelectorAll("img");
  const news = imgs.filter((i) => i.src && !i.src.startsWith("blob:"))[0];
  news.onerror();
  assert.match(texts(el, ".dk-imgd-msg")[0], /^Could not load the new version/);
});

test("dispose aborts the request, revokes the blob URL once and empties the element", async () => {
  const p = page();
  const { el, h } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  assert.deepEqual(p.st.revoked, []);
  h.dispose();
  assert.deepEqual(p.st.revoked, ["blob:t/1"]);
  assert.equal(p.st.fetches[0].signal.aborted, true);
  assert.equal(el.children.length, 0);
  h.dispose();
  assert.deepEqual(p.st.revoked, ["blob:t/1"], "a second dispose revokes nothing again");
});

test("a mount disposed while its request is out makes no blob URL, whenever the answer comes", async () => {
  let p = page();
  let m = mount(p, {});
  m.h.dispose();
  assert.equal(p.st.fetches[0].signal.aborted, true, "the request is aborted");
  p.st.fetches[0].resolve(imgRes()); // an answer that was already on its way
  await settle();
  assert.equal(p.st.created.length, 0);

  // Disposed between the headers and the body.
  p = page();
  m = mount(p, {});
  let release;
  const res = imgRes();
  res.blob = () => new Promise((r) => { release = () => r({ blob: true, type: "image/png" }); });
  p.st.fetches[0].resolve(res);
  await settle();
  m.h.dispose();
  release();
  await settle();
  assert.equal(p.st.created.length, 0, "no URL is made for a mount that is gone");
  assert.deepEqual(p.st.revoked, []);
});

test("every blob URL made is revoked: mount, dispose, remount, dispose", async () => {
  const p = page();
  for (let i = 0; i < 3; i++) {
    const m = mount(p, {});
    p.st.fetches[i].resolve(imgRes());
    await settle();
    m.h.dispose();
  }
  assert.equal(p.st.created.length, 3);
  assert.deepEqual(p.st.revoked, ["blob:t/1", "blob:t/2", "blob:t/3"]);
});

test("the three modes, in the control, are Side by side, Swipe and Onion skin, and the choice is kept", async () => {
  const p = page();
  const { el } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  const seg = el.querySelector(".dk-imgmode");
  assert.ok(seg);
  assert.deepEqual(seg.querySelectorAll(".dk-seg-t").map((n) => n.textContent), ["Side by side", "Swipe", "Onion skin"]);
  const pressed = () => seg.querySelectorAll("button").map((b) => b.getAttribute("aria-pressed"));
  assert.deepEqual(pressed(), ["true", "false", "false"], "side by side is the default");
  assert.equal(el.querySelector(".dk-imgd-stage").dataset.mode, "side");
  seg.querySelectorAll("button")[1].click();
  assert.deepEqual(pressed(), ["false", "true", "false"]);
  assert.equal(el.querySelector(".dk-imgd-stage").dataset.mode, "swipe");
  assert.equal(JSON.parse(p.st.store.get("agent-box:viewerPrefs")).imgDiffMode, "swipe");
  seg.querySelectorAll("button")[2].click();
  assert.equal(el.querySelector(".dk-imgd-stage").dataset.mode, "onion");
  assert.equal(JSON.parse(p.st.store.get("agent-box:viewerPrefs")).imgDiffMode, "onion");
  // The same two <img> elements are moved between modes: nothing is loaded again.
  assert.equal(el.querySelectorAll("img").length, 2);
  assert.equal(p.st.fetches.length, 1);
  assert.equal(p.st.created.length, 1);
});

test("a stored mode is the one a new mount opens in", async () => {
  const p = page();
  p.st.store.set("agent-box:viewerPrefs", JSON.stringify({ imgDiffMode: "onion" }));
  const { el } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  assert.equal(el.querySelector(".dk-imgd-stage").dataset.mode, "onion");
});

test("Swipe: a slider with its values, the new image cut at the divider, moved by the arrow keys, Home and End", async () => {
  const p = page();
  p.st.store.set("agent-box:viewerPrefs", JSON.stringify({ imgDiffMode: "swipe" }));
  const { el } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  const div = el.querySelector(".dk-imgd-div");
  assert.equal(div.getAttribute("role"), "slider");
  assert.equal(div.tabIndex, 0);
  assert.equal(div.getAttribute("aria-valuemin"), "0");
  assert.equal(div.getAttribute("aria-valuemax"), "100");
  assert.equal(div.getAttribute("aria-valuenow"), "50");
  assert.match(div.getAttribute("aria-label"), /old image on the left/);
  const layers = el.querySelectorAll(".dk-imgd-layer");
  assert.equal(layers[1].style.clipPath, "inset(0 0 0 50%)");
  assert.equal(div.style.left, "50%");
  const key = (k, shift) => { let stopped = false; div.onkeydown({ key: k, shiftKey: !!shift, preventDefault() { stopped = true; } }); return stopped; };
  assert.equal(key("ArrowRight"), true);
  assert.equal(div.getAttribute("aria-valuenow"), "51");
  key("ArrowLeft"); key("ArrowLeft");
  assert.equal(div.getAttribute("aria-valuenow"), "49");
  key("ArrowRight", true);
  assert.equal(div.getAttribute("aria-valuenow"), "59");
  assert.equal(layers[1].style.clipPath, "inset(0 0 0 59%)");
  key("End");
  assert.equal(div.getAttribute("aria-valuenow"), "100");
  key("ArrowRight", true);
  assert.equal(div.getAttribute("aria-valuenow"), "100", "clamped at the right edge");
  key("Home");
  assert.equal(div.getAttribute("aria-valuenow"), "0");
  key("ArrowLeft", true);
  assert.equal(div.getAttribute("aria-valuenow"), "0", "clamped at the left edge");
  assert.equal(key("a"), false, "other keys are left alone");
  let stopped = false;
  div.onkeydown({ key: "ArrowRight", ctrlKey: true, preventDefault() { stopped = true; } });
  assert.equal(stopped, false, "a chord is not the slider's");
});

test("Swipe: a pointer drag moves the divider by its place in the stage, holds the pointer, clamps, and stops on release", async () => {
  const p = page();
  p.st.store.set("agent-box:viewerPrefs", JSON.stringify({ imgDiffMode: "swipe" }));
  const { el } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  const stack = el.querySelector(".dk-imgd-swipe");
  const div = el.querySelector(".dk-imgd-div");
  stack.getBoundingClientRect = () => ({ left: 100, width: 400 });
  const log = [];
  stack.setPointerCapture = (id) => log.push("capture " + id);
  stack.releasePointerCapture = (id) => log.push("release " + id);
  let prevented = false;
  stack.onpointerdown({ button: 0, pointerId: 7, clientX: 200, preventDefault() { prevented = true; } });
  assert.equal(div.getAttribute("aria-valuenow"), "25");
  assert.equal(prevented, true, "no native image drag, no text selection");
  assert.equal(p.doc.activeElement, div, "the divider takes the keyboard, so the arrow keys carry on from here");
  stack.onpointermove({ clientX: 300 });
  assert.equal(div.getAttribute("aria-valuenow"), "50");
  stack.onpointermove({ clientX: 9000 });
  assert.equal(div.getAttribute("aria-valuenow"), "100", "past the right edge");
  stack.onpointermove({ clientX: -50 });
  assert.equal(div.getAttribute("aria-valuenow"), "0", "past the left edge");
  stack.onpointerup({ pointerId: 7 });
  stack.onpointermove({ clientX: 300 });
  assert.equal(div.getAttribute("aria-valuenow"), "0", "a move with no button down does nothing");
  assert.deepEqual(log, ["capture 7", "release 7"]);
  // Not the primary button, and a stage with no size yet (the middle).
  stack.onpointerdown({ button: 2, pointerId: 8, clientX: 400 });
  assert.equal(div.getAttribute("aria-valuenow"), "0");
  stack.getBoundingClientRect = () => ({ left: 0, width: 0 });
  stack.onpointerdown({ button: 0, pointerId: 9, clientX: 10, preventDefault() {} });
  assert.equal(div.getAttribute("aria-valuenow"), "50", "no width: the middle, not NaN");
});

test("Onion skin: a range input blends the new image over the old, through onionOpacity", async () => {
  const p = page();
  p.st.store.set("agent-box:viewerPrefs", JSON.stringify({ imgDiffMode: "onion" }));
  const { el } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  const range = el.querySelector("input");
  assert.equal(range.type, "range");
  assert.equal(range.min, "0");
  assert.equal(range.max, "100");
  assert.equal(range.value, "50");
  assert.match(range.getAttribute("aria-label"), /Onion skin/);
  const news = el.querySelectorAll("img").filter((i) => i.src && !i.src.startsWith("blob:"))[0];
  assert.equal(news.style.opacity, "0.5");
  range.value = "20"; range.oninput();
  assert.equal(news.style.opacity, "0.2");
  range.value = "100"; range.oninput();
  assert.equal(news.style.opacity, "1");
  range.value = "0"; range.oninput();
  assert.equal(news.style.opacity, "0");
  assert.deepEqual(texts(el, ".dk-imgd-blend span"), ["Old", "New"]);
  // Going back to Side by side leaves no blend behind on the picture.
  el.querySelector(".dk-imgmode").querySelectorAll("button")[0].click();
  assert.equal(news.style.opacity, "");
});

test("Swipe and Onion skin show both sizes in one line under the stack", async () => {
  const p = page();
  p.st.store.set("agent-box:viewerPrefs", JSON.stringify({ imgDiffMode: "swipe" }));
  const { el } = mount(p, {});
  p.st.fetches[0].resolve(imgRes());
  await settle();
  assert.deepEqual(texts(el, ".dk-imgd-stackcap"), ["Old … · New …"]);
  const [a, b] = el.querySelectorAll("img");
  loadImg(a, 10, 20);
  loadImg(b, 30, 40);
  assert.deepEqual(texts(el, ".dk-imgd-stackcap"), ["Old 10 × 20 · New 30 × 40"]);
});

test("until the old picture is in, Swipe and Onion lay the sides out one beside the other", () => {
  const p = page();
  p.st.store.set("agent-box:viewerPrefs", JSON.stringify({ imgDiffMode: "swipe" }));
  const { el } = mount(p, {});
  assert.equal(el.querySelector(".dk-imgd-stage").dataset.mode, "side");
  assert.equal(el.querySelector(".dk-imgd-bar").hidden, false, "the control is there; the pick is honoured when the picture lands");
});

// --- the tab's glue ------------------------------------------------------------------------------------------------------------

function tabPage(over) {
  const p = page();
  const s = { shown: true, known: true, head: "h1", scope: "head", row: { path: "/w/logo.png", code: "M" }, rows: [] };
  p.s = s;
  p.run(`
    let __s = null;
    function ftRoot() { return "/w"; }
    function gitStatusCurrent() { return __s.known ? { status: { head: __s.head } } : null; }
    function dockScope() { return __s.scope; }
    function gitBaseFor() { return null; }
    function editorSyncShown() { return __s.shown; }
    function editorReviewRow() { return __s.row; }
    function editorReviewWantsFocus() { return false; }
    function editorReviewSid() { return "s1"; }
    function editorReviewChanged() { __s.changed = (__s.changed || 0) + 1; }
    function reviewOpenNext(sid, path, dir) { __s.opened = [sid, path, dir]; }
    function editorDiffOf() { return {}; }
    let __attached = null;
    function dockAttached() { return __attached; }
  `);
  p.ctx.__s = Object.assign(s, over || {});
  p.run("__s = this.__s;");
  const mk = () => {
    const tab = {
      name: "logo.png", path: "/w/logo.png", mode: "image", box: null, disposed: false, attached: true, host: { sessionId: () => "s1" },
      inner: { el: new Node("div", p.doc) }, el: new Node("div", p.doc), paints: 0, paint() { this.paints++; }, focusReader() {},
    };
    tab.el.append(tab.inner.el);
    p.ctx.editorImageToggle(tab);
    p.ctx.__tab = tab;
    p.run("__attached = this.__tab;");
    return tab;
  };
  p.mk = mk;
  return p;
}

test("an image the Changes section lists gets an Image | Changes toggle; one it does not list, none", () => {
  const p = tabPage();
  const tab = p.mk();
  assert.equal(tab.img.seg.el.hidden, true, "hidden until painted");
  p.ctx.editorImagePaint(tab);
  assert.equal(tab.img.seg.el.hidden, false);
  assert.deepEqual(tab.img.seg.el.querySelectorAll("button").map((b) => b.getAttribute("aria-pressed")), ["true", "false"]);
  p.s.row = null;
  p.ctx.editorImagePaint(tab);
  assert.equal(tab.img.seg.el.hidden, true, "no row, no Changes");
  tab.mode = "binary";
  p.s.row = { path: tab.path, code: "M" };
  p.ctx.editorImagePaint(tab);
  assert.equal(tab.img.seg.el.hidden, true, "a binary file is not an image");
  tab.mode = "image";
  tab.box = "s1";
  p.ctx.editorImagePaint(tab);
  assert.equal(tab.img.seg.el.hidden, true, "a file in a box has no git");
});

test("Changes swaps the viewer for the comparer and asks once for the old side; Image puts the viewer back and lets go", async () => {
  const p = tabPage();
  const tab = p.mk();
  assert.equal(p.ctx.editorImageSet(tab, true), true);
  assert.equal(tab.img.on, true);
  assert.notEqual(tab.el.children[0], tab.inner.el, "the comparer is in the tab");
  assert.equal(tab.el.querySelector(".dk-dh-name").textContent, "logo.png");
  assert.equal(tab.el.querySelector(".dk-dh-cmp").textContent, "HEAD ↔ working tree");
  assert.equal(p.st.fetches.length, 1);
  assert.deepEqual(p.st.fetches[0].body, { path: "/w/logo.png", scope: "head", raw: true });
  assert.deepEqual(tab.img.seg.el.querySelectorAll("button").map((b) => b.getAttribute("aria-pressed")), ["false", "true"]);
  p.st.fetches[0].resolve(imgRes());
  await settle();
  p.ctx.editorImageSet(tab, false);
  assert.equal(tab.img.on, false);
  assert.equal(tab.el.children[0], tab.inner.el, "the viewer is back");
  assert.deepEqual(p.st.revoked, ["blob:t/1"]);
});

test("the same key asks nothing more; a moved HEAD or scope makes the pictures again and lets the old ones go", async () => {
  const p = tabPage();
  const tab = p.mk();
  p.ctx.editorImageSet(tab, true);
  p.st.fetches[0].resolve(imgRes());
  await settle();
  p.ctx.editorImageSync();
  p.ctx.editorImageSync();
  assert.equal(p.st.fetches.length, 1, "a status that lands with the same key");
  p.s.head = "h2"; // a commit
  p.ctx.editorImageSync();
  assert.equal(p.st.fetches.length, 2);
  assert.deepEqual(p.st.revoked, ["blob:t/1"], "the first picture's URL went with its mount");
  p.s.scope = "branch";
  p.ctx.editorImageSync();
  assert.equal(p.st.fetches.length, 3);
  assert.equal(p.st.fetches[2].body.scope, "branch");
  assert.equal(p.st.fetches[1].signal.aborted, true, "the request that was still out is aborted");
  assert.equal(tab.el.querySelector(".dk-dh-cmp").textContent, "the merge base ↔ working tree");
});

test("a dock that is not on screen, or git's status not known yet, asks nothing; it asks when they are", () => {
  const p = tabPage({ shown: false });
  const tab = p.mk();
  p.ctx.editorImageSet(tab, true);
  assert.equal(p.st.fetches.length, 0, "the dock is hidden");
  p.ctx.editorImageSync();
  assert.equal(p.st.fetches.length, 0);
  p.s.shown = true;
  p.s.known = false;
  p.ctx.editorImageSync();
  assert.equal(p.st.fetches.length, 0, "the key is not known yet");
  assert.equal(tab.el.querySelector(".dk-imgd-msg").textContent, "Waiting for git…");
  p.s.known = true;
  p.ctx.editorImageSync();
  assert.equal(p.st.fetches.length, 1);
  assert.equal(tab.el.querySelectorAll(".dk-imgd-msg").length, 0 + tab.el.querySelectorAll(".dk-imgd-msg").length, "the placeholder is replaced by the comparer");
  assert.ok(!tab.el.querySelector(".dk-imgd-body").textContent.includes("Waiting for git"));
});

test("a D row opens the comparer with the old side only, and asks for nothing of the working tree", async () => {
  const p = tabPage({ row: { path: "/w/logo.png", code: "D" } });
  const tab = p.mk();
  p.ctx.editorImageSet(tab, true);
  p.st.fetches[0].resolve(imgRes());
  await settle();
  assert.equal(tab.el.querySelectorAll("img").filter((i) => i.src && !i.src.startsWith("blob:")).length, 0);
  assert.deepEqual(texts(tab.el, ".dk-imgd-lab"), ["Old · HEAD"]);
});

test("a tab that is not on screen is in Changes when it is shown, and asked nothing before", () => {
  const p = tabPage();
  const tab = p.mk();
  tab.attached = false;
  p.ctx.editorImageSet(tab, true);
  assert.equal(tab.img.on, true);
  assert.equal(tab.img.view, null, "no comparer for a tab nobody sees");
  assert.equal(p.st.fetches.length, 0);
  tab.attached = true;
  p.ctx.editorImageEnter(tab);
  assert.equal(p.st.fetches.length, 1);
});

test("a box tab cannot have Changes, and says why", () => {
  const p = tabPage();
  const tab = p.mk();
  tab.box = "s1";
  assert.equal(p.ctx.editorImageSet(tab, true), false);
  assert.equal(tab.img.on, false);
  assert.match(p.st.toasts[0], /^Changes unavailable: .*no git history/);
  assert.equal(p.st.fetches.length, 0);
});

test("closing the tab lets go of the request, the blob URL and the comparer", async () => {
  const p = tabPage();
  const tab = p.mk();
  p.ctx.editorImageSet(tab, true);
  p.st.fetches[0].resolve(imgRes());
  await settle();
  p.ctx.editorImageDispose(tab);
  assert.deepEqual(p.st.revoked, ["blob:t/1"]);
  assert.equal(tab.img.view, null);
  assert.equal(p.ctx.editorImageOn(tab), false);
  // Disposed with a request still out.
  const q = tabPage();
  const t2 = q.mk();
  q.ctx.editorImageSet(t2, true);
  q.ctx.editorImageDispose(t2);
  assert.equal(q.st.fetches[0].signal.aborted, true);
});

test("F7 and Shift+F7 in an image's Changes view go to the next and the previous changed file", async () => {
  const p = tabPage();
  const tab = p.mk();
  p.ctx.editorImageStep(tab, 1);
  await settle();
  assert.deepEqual(p.s.opened, ["s1", "/w/logo.png", 1]);
  p.ctx.editorImageStep(tab, -1);
  await settle();
  assert.deepEqual(p.s.opened, ["s1", "/w/logo.png", -1]);
});

test("the page loads imagediff.js after editordiff.js, and the tab, the review keys and the sync call it", () => {
  const html = src("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("imagediff.js") > at("editordiff.js") && at("editordiff.js") > 0, "after the segmented control it builds with");
  assert.ok(at("imagediff.js") < at("filesdrawer.js") && at("imagediff.js") < at("app.js"));
  assert.match(src("editortab.js"), /editorImageToggle\(this\)/);
  assert.match(src("editortab.js"), /editorImageSet\(this, on !== false\)/);
  assert.match(src("editortab.js"), /editorImageDispose\(this\)/);
  assert.match(src("editorreview.js"), /editorImageStep\(tab, dir\)/);
  assert.match(src("editordiff.js"), /editorImageSync\(\)/);
});

test("an image tab's conflict bar leaves the dock's slot without throwing: its el holds only the viewer, so the compare view is no child of it", () => {
  const { page: editorPage } = require("./editorpage.js");
  const { Node: N } = require("./fakedom.js");
  const p = editorPage();
  const doc = p.doc;
  const slot = new N("div", doc);
  const fake = { el: new N("div", doc), barEl: new N("div", doc), cmpEl: new N("div", doc) };
  slot.append(fake.barEl);
  fake.el.append(new N("div", doc)); // the viewer: mountInner replaced the editor's own children with it
  p.EditorTab.prototype.placeBar.call(fake, false);
  assert.equal(fake.barEl.parent, null, "taken out of the slot");
  // A text tab still puts it back in front of its compare view.
  const text = { el: new N("div", doc), barEl: new N("div", doc), cmpEl: new N("div", doc) };
  text.el.append(text.cmpEl);
  slot.append(text.barEl);
  p.EditorTab.prototype.placeBar.call(text, false);
  assert.deepEqual(text.el.children, [text.barEl, text.cmpEl]);
});
