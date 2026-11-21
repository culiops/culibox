"use strict";
// What EditorTab does with a file that is not text (#344), on editorpage.js's page: the real editortab.js and dockfileview.js against a
// fake page. The kind of an image, a video, an audio file, a PDF and the types with no viewer is decided by the extension, so none of
// them goes through the text read: one small metadata read (GET ...&meta=1), then a DockFileView. A text file's read that says
// `binary`, or 413, mounts the card too. The ⋯ is the dock's for every tab; for these it holds the five rows the spec lists.
const test = require("node:test");
const assert = require("node:assert");
const { page, El } = require("./editorpage.js");

const META = (over) => ({ ok: true, status: 200, data: Object.assign({ kind: "binary", size: 2048, readonly: false, mtime: 1700000000000 }, over) });
const WAIT = async (p) => { await p.settle(); };

test("the extension decides the view: image, video, audio, PDF, and the types with no viewer, each after one metadata read and no text read", async () => {
  for (const [file, kind, mode] of [
    ["/p/pic.png", "image", "image"], ["/p/pic.avif", "image", "image"], ["/p/clip.mp4", "video", "binary"], ["/p/clip.webm", "video", "binary"],
    ["/p/song.mp3", "audio", "binary"], ["/p/song.ogg", "audio", "binary"], ["/p/doc.pdf", "pdf", "binary"], ["/p/data.sqlite", "card", "binary"],
    ["/p/nb.ipynb", "card", "binary"], ["/p/pack.tar", "card", "binary"], ["/p/book.EPUB", "card", "binary"],
  ]) {
    const p = page();
    p.setRead(META());
    const tab = await p.open(file);
    assert.equal(tab.inner.constructor.name, "DockFileView", file);
    assert.equal(tab.inner.kind, kind, file);
    assert.equal(tab.mode, mode, file + ": only an image is the image mode (the Image | Changes toggle, imagediff.js)");
    assert.equal(p.st.reads.length, 1, file + ": one request");
    assert.deepEqual(p.metas(), ["/api/fs/read?path=" + encodeURIComponent(file) + "&meta=1"], file);
    assert.deepEqual(p.fulls(), [], file + ": no text read");
    assert.equal(tab.model, null, file);
    assert.equal(p.st.models.length, 0, file);
    assert.deepEqual(tab.el.children, [tab.inner.el], file + ": the tab holds only its view");
  }
});

test("a text extension is read as today: one full read, no metadata request, an editor", async () => {
  const p = page();
  const tab = await p.open("/p/a.go");
  assert.equal(tab.inner, null);
  assert.deepEqual(p.metas(), []);
  assert.equal(p.fulls().length, 1);
  assert.ok(tab.model);
});

test("a box file's metadata comes from its own route, and the view's URLs go by it", async () => {
  const p = page();
  p.setRead(META({ kind: "image" }));
  const tab = await p.open("/work/a.png", { box: "s1" });
  assert.deepEqual(p.metas(), ["/box/s1/work/a.png&meta=1"]);
  assert.equal(tab.inner.imageURL(), "/box/s1/work/a.png&v=1700000000000");
  const pdf = await p.open("/work/r.pdf", { box: "s1" });
  assert.equal(pdf.inner.rawURL(), "/box/s1/work/r.pdf&raw=1&v=1700000000000");
});

test("the size and time the read gave are the view's facts; a read that failed still mounts the view, with those left out", async () => {
  const p = page();
  p.setRead(META({ size: 5000, mtime: 0 }));
  const ok = await p.open("/p/a.zip");
  assert.deepEqual({ ...ok.inner.meta }, { size: 5000, mtime: 0 });
  assert.deepEqual(ok.inner.factsEl.children.map((c) => c.textContent), ["ZIP", "4.9 KB"]);
  for (const bad of [{ ok: false, status: 0, data: null }, { ok: false, status: 500, data: { error: "boom" } }, { ok: true, status: 200, data: null }]) {
    const q = page();
    q.setRead(bad);
    const tab = await q.open("/p/pic.png");
    assert.ok(tab.inner, "the picture is still shown: its own request says whether there is one");
    assert.equal(tab.inner.meta, null);
    assert.equal(tab.inner.factsEl.children.map((c) => c.textContent).join("|"), "PNG");
  }
});

test("a file with a text extension that the server says is binary gets the binary card with the size the read gave", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "binary", size: 9, mtime: 3 } });
  const tab = await p.open("/p/blob.bin");
  assert.equal(tab.inner.kind, "binary");
  assert.deepEqual({ ...tab.inner.meta }, { size: 9, mtime: 3 });
  assert.equal(tab.mode, "binary");
  assert.deepEqual(p.metas(), []);
  assert.equal(p.fulls().length, 1);
  assert.equal(tab.inner.el.querySelector(".dk-fv-note").textContent, "Not text, and CuliBox has no viewer for this type.");
});

test("a text file over the limit (413) gets the too-large card with its size and time, and Download works", async () => {
  const p = page();
  p.setRead({ ok: false, status: 413, data: { error: "file too large to preview", size: 3 * 1024 * 1024, mtime: 1700000000000 } });
  const tab = await p.open("/p/big.log");
  assert.equal(p.st.reads.length, 1);
  assert.equal(tab.inner.kind, "toolarge");
  assert.equal(tab.model, null);
  const card = tab.inner.el;
  assert.equal(card.querySelector(".dk-fv-name").textContent, "big.log");
  assert.ok(card.querySelector(".dk-fv-meta").textContent.startsWith("LOG file · 3.0 MB · modified "));
  assert.equal(card.querySelector(".dk-fv-note").textContent, "Larger than the 2 MB preview limit. Download it to read it.");
  card.querySelector(".dk-fv-dl").click();
  assert.deepEqual(p.st.downloads, [{ path: "/p/big.log", isDir: false, url: undefined }]);
  // a box file's goes by the box's own download route
  const q = page();
  q.setRead({ ok: false, status: 413, data: { error: "x", size: 3 * 1024 * 1024 } });
  const b = await q.open("/w/big.log", { box: "s1" });
  b.inner.el.querySelector(".dk-fv-dl").click();
  assert.deepEqual(q.st.downloads, [{ path: "/w/big.log", isDir: false, url: "/box/s1/w/big.log&download=1" }]);
});

test("any other failed read still paints the server's sentence (404 not found, 500 ...)", async () => {
  for (const [status, error] of [[404, "not found"], [500, "read error"], [0, undefined]]) {
    const p = page();
    p.setRead({ ok: false, status, data: error ? { error } : null });
    const tab = await p.open("/p/a.go");
    assert.equal(tab.inner, null);
    assert.equal(tab.bodyEl.children[0].textContent, error || "could not reach the server");
  }
});

test("a tab closed while its metadata is on the way mounts nothing", async () => {
  const p = page();
  p.serve("x");
  let release;
  p.st.metaGate = new Promise((r) => { release = r; });
  const tab = new p.EditorTab("/p/pic.png", { host: p.host });
  await p.settle();
  tab.dispose();
  release();
  await p.settle();
  assert.equal(tab.inner, null);
});

test("a tab shown when its view is made gets show then attach; one made off screen gets attach when the dock shows it", async () => {
  const p = page();
  p.setRead(META());
  const tab = new p.EditorTab("/p/a.zip", { host: p.host });
  tab.onAttach(); // on screen while still loading
  await p.settle();
  assert.deepEqual(tab.inner.calls, ["show", "attach"]);
  const hidden = new p.EditorTab("/p/b.zip", { host: p.host });
  await p.settle();
  assert.equal(hidden.inner.calls, undefined, "nothing until it is shown");
  hidden.onAttach();
  assert.deepEqual(hidden.inner.calls, ["attach"]);
});

// --- the header ---------------------------------------------------------------------------------------------

test("the ⋯ is in the bar for every file once read, with Follow; the Code | Changes toggle and the editor's own status bar stay for text", async () => {
  for (const [file, data] of [["/p/pic.png", META({ kind: "image" })], ["/p/c.mp4", META()], ["/p/d.pdf", META()], ["/p/e.sqlite", META()],
    ["/p/f.bin", META()], ["/p/g.log", { ok: false, status: 413, data: { error: "big", size: 9 } }]]) {
    const p = page();
    p.setRead(data);
    const tab = new p.EditorTab(file, { host: p.host });
    assert.equal(tab.moreBtn.hidden, true, file + ": not before it is read");
    assert.equal(tab.followBtn.hidden, false, file + ": Follow, even while it loads");
    await p.settle();
    assert.ok(tab.inner, file);
    assert.equal(tab.moreBtn.hidden, false, file);
    assert.equal(tab.followBtn.hidden, false, file);
    assert.equal(tab.seg.el.hidden, true, file + ": no Code | Changes");
    assert.equal(tab.sbEl.hidden, true, file + ": the editor's own status bar is not for it: the view has its own");
    assert.ok(!tab.el.children.includes(tab.sbEl), file);
  }
  const p = page();
  const txt = await p.open("/p/a.go");
  assert.equal(txt.moreBtn.hidden, false);
  assert.equal(txt.sbEl.hidden, false);
  assert.equal(txt.seg.el.hidden, false);
});

// The ⋯ menu as the kit builds it, with stubs for the pieces that are the page's (editor-review.test.js does the same).
function menuPage(over) {
  const p = page(Object.assign({
    kitMenuButton: (label, icon, danger, run) => { const n = new El("button", null); n.label = label; n.icon = icon; n.run = run; n.attrs = {}; return n; },
    kitMenuSep: () => ({ label: "-" }),
    closeRowMenu: () => {},
    window: { HOME_DIR: "/p" }, // the workspace root the files here are under
    ftShowMenu: (a, nodes) => { p.menus.push(nodes); },
  }, over));
  p.menus = [];
  const menu = new El("div", p.doc);
  menu.querySelector = () => null;
  p.doc.getElementById = (id) => (id === "ft-menu" ? menu : null);
  return p;
}
const labels = (nodes) => nodes.map((n) => n.label);

test("the ⋯ of a file that is not text holds Copy path, Copy relative path, Reveal in tree and Download, and nothing for the editor", async () => {
  for (const file of ["/p/pic.png", "/p/c.mp4", "/p/e.sqlite", "/p/f.bin"]) {
    const p = menuPage();
    p.setRead(META());
    const tab = await p.open(file);
    tab.openMenu();
    assert.deepEqual(labels(p.menus[0]), ["Copy path", "Copy relative path", "Reveal in tree", "Download"], file);
    assert.deepEqual(p.menus[0].map((n) => n.icon), ["copy", "copy", "files", "download"], file);
    assert.ok(p.menus[0].every((n) => !n.getAttribute || n.getAttribute("aria-disabled") !== "true"), file + ": nothing greyed out for no reason");
    p.menus[0][3].run();
    assert.deepEqual(p.st.downloads, [{ path: file, isDir: false, url: undefined }], file + ": Download is the tab's");
  }
});

test("a PDF's ⋯ adds a separator and Open in new tab, which opens the raw URL in a tab of its own", async () => {
  const p = menuPage();
  p.setRead(META());
  const opened = [];
  p.doc.body = { append: (a) => opened.push(a) };
  const tab = await p.open("/p/report.pdf");
  tab.openMenu();
  assert.deepEqual(labels(p.menus[0]), ["Copy path", "Copy relative path", "Reveal in tree", "Download", "-", "Open in new tab"]);
  p.menus[0][5].run();
  assert.equal(opened.length, 1);
  assert.equal(opened[0].href, "/api/fs/read?path=" + encodeURIComponent("/p/report.pdf") + "&raw=1&v=1700000000000");
  assert.equal(opened[0].target, "_blank");
  assert.equal(opened[0].rel, "noopener");
  // a PDF that is not a PDF view any more (it has no other) keeps it; an image never has it
  const q = menuPage();
  q.setRead(META());
  const img = await q.open("/p/a.png");
  img.openMenu();
  assert.ok(!labels(q.menus[0]).includes("Open in new tab"));
});

test("the rows that cannot act keep today's rules: outside the root, and in a box", async () => {
  const p = menuPage();
  p.setRead(META());
  const out = await p.open("/elsewhere/pic.png");
  out.openMenu();
  const rel = p.menus[0][1];
  assert.equal(rel.label, "Copy relative path");
  assert.equal(rel.getAttribute("aria-disabled"), "true");
  assert.equal(rel.title, "This file is outside the tree's root");
  const q = menuPage();
  q.setRead(META());
  const box = await q.open("/work/a.png", { box: "s1" });
  box.openMenu();
  assert.equal(q.menus[0][1].getAttribute("aria-disabled"), "true");
  assert.equal(q.menus[0][2].getAttribute("aria-disabled"), "true");
  assert.equal(q.menus[0][3].getAttribute("aria-disabled"), null, "Download always works");
  q.menus[0][3].run();
  assert.deepEqual(q.st.downloads, [{ path: "/work/a.png", isDir: false, url: "/box/s1/work/a.png&download=1" }]);
});

test("a text file's menu is as it was: Wrap lines, Font size and Copy contents are there", async () => {
  const p = menuPage();
  const tab = await p.open("/p/a.go");
  tab.fontRow = () => ({ label: "Font size" });
  tab.openMenu();
  const l = labels(p.menus[0]);
  assert.ok(l.includes("Wrap lines") && l.includes("Font size") && l.includes("Copy contents") && !l.includes("Open in new tab"), l.join("|"));
});

// --- fresh content and the dock's other callers -------------------------------------------------------------

test("a tab that is shown again looks at its file again: a new modified time reloads the picture", async () => {
  const p = page();
  p.setRead(META({ kind: "image", size: 10, mtime: 5 }));
  const tab = await p.open("/p/pic.png");
  assert.equal(p.metas().length, 1);
  assert.ok(tab.inner.img.src.endsWith("&v=5"));
  tab.onHide();
  p.setRead(META({ kind: "image", size: 20, mtime: 9 }));
  tab.onAttach();
  await WAIT(p);
  assert.equal(p.metas().length, 2, "one more small request, no text read");
  assert.deepEqual(p.fulls(), []);
  assert.equal(p.metas()[1], "/api/fs/read?path=%2Fp%2Fpic.png&meta=1");
  assert.ok(tab.inner.img.src.endsWith("&v=9"));
  assert.equal(tab.inner.meta.size, 20);
  // the dock's own poll never asks about it
  p.setShown(true);
  assert.equal(p.live(), 0);
});

test("the tab keeps the toolbarApi the dock calls, the view has none, and the tab forwards the dock's other calls to it", async () => {
  const p = page();
  p.setRead(META({ kind: "image" }));
  const tab = await p.open("/p/pic.png");
  assert.equal(tab.toolbarApi.paint !== undefined, true, "the tab's own toolbarApi, not the view's");
  assert.equal(tab.inner.toolbarApi, undefined);
  // Image | Changes works on the view (imagediff.js swaps tab.el's children and puts tab.inner.el back)
  const swap = new El("div", p.doc);
  tab.el.replaceChildren(swap);
  tab.el.replaceChildren(tab.inner.el);
  assert.deepEqual(tab.el.children, [tab.inner.el]);
  assert.equal(tab.focusReader(), undefined);
  assert.equal(p.doc.activeElement, tab.inner.el);
  assert.equal(typeof tab.wrapAnchor(), "function");
  tab.applyWrap();
  tab.revealLine(4, 1);
});
