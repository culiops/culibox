"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { shouldRefreshTree, insertTextAtPrompt } = require("../static/upload.js");

test("an upload into the directory the tree is showing refreshes it", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/proj", "/home/u/proj"), true);
});

test("a trailing slash on either side is the same directory", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/proj/", "/home/u/proj"), true);
  assert.strictEqual(shouldRefreshTree("/home/u/proj", "/home/u/proj/"), true);
});

test("an upload elsewhere leaves the tree alone", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/other", "/home/u/proj"), false);
});

// Deliberate: re-listing the root would not reveal a file nested one level down,
// and it would collapse every folder the user had expanded. Not worth it.
test("an upload into a subfolder of the root does not re-list the root", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/proj/sub", "/home/u/proj"), false);
});

test("an upload into the parent of the root does not re-list the root", () => {
  assert.strictEqual(shouldRefreshTree("/home/u", "/home/u/proj"), false);
});

test("a prefix that is not a path boundary is not a match", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/proj2", "/home/u/proj"), false);
});

test("a missing destination or root never refreshes", () => {
  assert.strictEqual(shouldRefreshTree("", "/home/u/proj"), false);
  assert.strictEqual(shouldRefreshTree("/home/u/proj", ""), false);
  assert.strictEqual(shouldRefreshTree(null, null), false);
  assert.strictEqual(shouldRefreshTree(undefined, "/home/u/proj"), false);
});

test("the filesystem root is not mistaken for an empty path", () => {
  assert.strictEqual(shouldRefreshTree("/", "/"), true);
  assert.strictEqual(shouldRefreshTree("/", "/home"), false);
});

// A fake terminal: sendText's real contract is "returns false when it refuses".
const fakeTerm = (accept) => ({ sent: [], sendText(t) { if (!accept) return false; this.sent.push(t); return true; } });

test("insertTextAtPrompt sends the text and reports success", () => {
  const t = fakeTerm(true);
  assert.deepStrictEqual(insertTextAtPrompt(t, "/tmp/a.js "), { ok: true, reason: "" });
  assert.deepStrictEqual(t.sent, ["/tmp/a.js "]);
});

// The guarantee the whole feature rests on: nothing this app inserts may submit
// a line. A directory name may legally contain a newline on Linux.
test("insertTextAtPrompt refuses text carrying a newline", () => {
  const t = fakeTerm(true);
  assert.deepStrictEqual(insertTextAtPrompt(t, "/tmp/a\nrm -rf /"), { ok: false, reason: "unsafe" });
  assert.deepStrictEqual(t.sent, []);
});

test("insertTextAtPrompt refuses C1 control bytes", () => {
  const t = fakeTerm(true);
  assert.strictEqual(insertTextAtPrompt(t, "/tmp/ax").reason, "unsafe");
});

test("insertTextAtPrompt reports a missing terminal", () => {
  assert.deepStrictEqual(insertTextAtPrompt(null, "/tmp/a.js "), { ok: false, reason: "noterm" });
  assert.deepStrictEqual(insertTextAtPrompt({}, "/tmp/a.js "), { ok: false, reason: "noterm" });
});

test("insertTextAtPrompt reports a refused send", () => {
  assert.deepStrictEqual(insertTextAtPrompt(fakeTerm(false), "/tmp/a.js "), { ok: false, reason: "disconnected" });
});

const { dropHint } = require("../static/upload.js");

test("dropHint: mobile keeps today's exact strings", () => {
  assert.strictEqual(dropHint("nodest", false), "No destination folder yet. Drop onto a folder in Files instead");
  assert.strictEqual(dropHint("miss", false), "Drop onto a terminal pane, or onto a folder in Files");
});

test("dropHint: desktop names the Files drawer", () => {
  assert.strictEqual(dropHint("nodest", true), "No destination folder yet. Drop onto a folder in the Files drawer instead");
  assert.strictEqual(dropHint("miss", true), "Drop onto a terminal pane, or onto a folder in the Files drawer");
});

test("dropHint: a read-only folder says so, on desktop and mobile alike", () => {
  assert.strictEqual(dropHint("readonly", true), "This folder is read-only: nothing can be dropped here");
  assert.strictEqual(dropHint("readonly", false), "This folder is read-only: nothing can be dropped here");
});

// --- makeDropZone with opts.readOnly (#55) --------------------------------------

const { makeDropZone } = require("../static/upload.js");

function zoneFixture(opts) {
  const handlers = {}, classes = new Set();
  const el = {
    addEventListener: (type, fn) => { handlers[type] = fn; },
    classList: { add: (c) => classes.add(c), remove: (c) => classes.delete(c) },
  };
  const toasts = [], posts = [];
  globalThis.showToast = (msg, o) => toasts.push({ msg, o });
  globalThis.XMLHttpRequest = class {
    constructor() { this.upload = {}; }
    open(method, url) { posts.push(method + " " + url); }
    send() {}
  };
  makeDropZone(el, () => "/dest", opts);
  // A file drag, as the browser reports it.
  const drag = () => {
    const ev = { dataTransfer: { types: ["Files"], files: [new File(["x"], "f.txt")], dropEffect: "" }, prevented: false, stopped: false };
    ev.preventDefault = () => { ev.prevented = true; };
    ev.stopPropagation = () => { ev.stopped = true; };
    return ev;
  };
  return { handlers, classes, toasts, posts, drag };
}

test("a writable zone still lights up, copies and uploads (regression)", () => {
  const z = zoneFixture({ readOnly: () => false });
  const enter = z.drag(); z.handlers.dragenter(enter);
  assert.ok(z.classes.has("drop-hot"));
  const over = z.drag(); z.handlers.dragover(over);
  assert.strictEqual(over.dataTransfer.dropEffect, "copy");
  z.handlers.drop(z.drag());
  assert.ok(!z.classes.has("drop-hot"));
  assert.deepStrictEqual(z.posts, ["POST /api/fs/upload?dir=%2Fdest"]);
  assert.deepStrictEqual(z.toasts.map((t) => t.msg).filter((m) => /read-only/.test(m)), []);
});

test("a zone with no readOnly option behaves as before", () => {
  const z = zoneFixture(undefined);
  z.handlers.dragenter(z.drag());
  assert.ok(z.classes.has("drop-hot"));
  z.handlers.drop(z.drag());
  assert.strictEqual(z.posts.length, 1);
});

test("a read-only zone never lights up, shows the no-drop cursor and swallows a drop with the reason", () => {
  const z = zoneFixture({ readOnly: () => true });
  const enter = z.drag(); z.handlers.dragenter(enter);
  assert.ok(!z.classes.has("drop-hot"), "no drop-hot");
  assert.ok(enter.prevented && enter.stopped, "still claimed, so the window-level miss toast does not also fire");
  const over = z.drag(); z.handlers.dragover(over);
  assert.strictEqual(over.dataTransfer.dropEffect, "none");
  assert.ok(over.stopped);
  const drop = z.drag(); z.handlers.drop(drop);
  assert.ok(drop.prevented && drop.stopped, "the browser must not open the file");
  assert.deepStrictEqual(z.posts, [], "nothing is uploaded");
  assert.deepStrictEqual(z.toasts, [{ msg: "This folder is read-only: nothing can be dropped here", o: { error: true } }]);
  assert.ok(!z.classes.has("drop-hot"));
});

test("readOnly is asked at event time: a listing that turns read-only mid-drag stops the drop", () => {
  let ro = false;
  const z = zoneFixture({ readOnly: () => ro });
  z.handlers.dragenter(z.drag());
  assert.ok(z.classes.has("drop-hot"));
  ro = true;
  z.handlers.drop(z.drag());
  assert.ok(!z.classes.has("drop-hot"), "the highlight is cleared by the drop");
  assert.deepStrictEqual(z.posts, []);
  assert.strictEqual(z.toasts.length, 1);
});

test("a read-only zone ignores a drag that carries no files (text selection)", () => {
  const z = zoneFixture({ readOnly: () => true });
  const ev = { dataTransfer: { types: ["text/plain"] }, preventDefault() { throw new Error("must not claim"); }, stopPropagation() { throw new Error("must not claim"); } };
  z.handlers.dragenter(ev); z.handlers.dragover(ev); z.handlers.drop(ev);
  assert.deepStrictEqual(z.toasts, []);
});
