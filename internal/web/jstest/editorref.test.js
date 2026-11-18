"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { rangeRef, insertGate, editorMode, conflictChoice, DELETED_TEXT, conflictText, textEOL } = require("../static/editorref.js");

test("rangeRef single and range", () => {
  assert.strictEqual(rangeRef("src/a.go", 7, 7), "src/a.go:7");
  assert.strictEqual(rangeRef("src/a.go", 7, 12), "src/a.go:7-12");
  assert.strictEqual(rangeRef("a", 1, 2), "a:1-2");
  // a selection made upwards still reads low-high
  assert.strictEqual(rangeRef("src/a.go", 12, 7), "src/a.go:7-12");
});

test("insertGate blocks waiting and need", () => {
  const why = "web is waiting on you; answer it first";
  const blocked = [
    { name: "web", activity: "waiting", need: "ask" },
    { name: "web", activity: "waiting", need: "sso" },
    { name: "web", activity: "waiting", need: "store_locked" },
    { name: "web", activity: "waiting", need: "something-new" },
    { name: "web", activity: "waiting" },
    { name: "web", activity: "waiting", need: "" },
    { name: "web", activity: "working", need: "ask" },
    { name: "web", activity: "idle", need: "sso" },
    { name: "web", activity: "idle", need: "store_locked" },
  ];
  for (const s of blocked) assert.deepStrictEqual(insertGate(s), { ok: false, why }, JSON.stringify(s));
});

test("insertGate allows an idle or working session with no need", () => {
  for (const s of [
    { name: "web", activity: "idle" },
    { name: "web", activity: "working" },
    { name: "web", activity: "idle", need: "" },
    { name: "web", activity: "working", need: "" },
  ]) assert.deepStrictEqual(insertGate(s), { ok: true, why: "" }, JSON.stringify(s));
});

test("insertGate allows need turn: a finished turn is the open prompt (ruling R1)", () => {
  // internal/session/peek.go: a Claude session that finished its turn is {activity: "waiting", need: "turn"}
  assert.deepStrictEqual(insertGate({ name: "web", activity: "waiting", need: "turn" }), { ok: true, why: "" });
  assert.deepStrictEqual(insertGate({ name: "web", activity: "idle", need: "turn" }), { ok: true, why: "" });
  assert.deepStrictEqual(insertGate({ name: "web", activity: "working", need: "turn" }), { ok: true, why: "" });
});

test("insertGate refuses an exited session and a missing one", () => {
  for (const s of [
    { name: "web", activity: "done" },
    { name: "web", activity: "crashed" },
    { name: "web", exited: true, activity: "idle" },
    { name: "web", exited: true, activity: "waiting", need: "turn" },
  ]) assert.deepStrictEqual(insertGate(s), { ok: false, why: "web has exited" }, JSON.stringify(s));
  for (const s of [null, undefined]) {
    const g = insertGate(s);
    assert.strictEqual(g.ok, false);
    assert.ok(g.why.length > 0);
  }
});

test("insertGate refuses a session that is not ready for input, naming it", () => {
  assert.deepStrictEqual(insertGate({ name: "web", activity: "detached" }), { ok: false, why: "web is not ready for input" });
  assert.deepStrictEqual(insertGate({ name: "web" }), { ok: false, why: "web is not ready for input" });
});

test("insertGate names the session by its label, then its id, then a plain word", () => {
  assert.strictEqual(insertGate({ name: "api work", activity: "waiting", need: "ask" }).why, "api work is waiting on you; answer it first");
  assert.strictEqual(insertGate({ id: "s-42", activity: "waiting", need: "ask" }).why, "s-42 is waiting on you; answer it first");
  assert.strictEqual(insertGate({ activity: "waiting", need: "ask" }).why, "the session is waiting on you; answer it first");
});

test("editorMode readonly and box", () => {
  assert.strictEqual(editorMode({ kind: "text", readonly: false, box: false }), "edit");
  assert.strictEqual(editorMode({ kind: "text" }), "edit");
  assert.strictEqual(editorMode({ kind: "text", readonly: true }), "read");
  // a box file is read-only today (viewer-tab.js enterEdit), writable or not
  assert.strictEqual(editorMode({ kind: "text", box: "sess-1" }), "read");
  assert.strictEqual(editorMode({ kind: "text", box: true, readonly: false }), "read");
  assert.strictEqual(editorMode({ kind: "image" }), "image");
  assert.strictEqual(editorMode({ kind: "image", box: "sess-1" }), "image");
  assert.strictEqual(editorMode({ kind: "image", readonly: true }), "image");
  assert.strictEqual(editorMode({ kind: "binary" }), "binary");
  assert.strictEqual(editorMode({ kind: "binary", box: "sess-1", readonly: true }), "binary");
});

test("editorMode: a kind it does not know is never editable", () => {
  assert.strictEqual(editorMode({ kind: "" }), "read");
  assert.strictEqual(editorMode({}), "read");
  assert.strictEqual(editorMode({ kind: "loading" }), "read");
  assert.strictEqual(editorMode(undefined), "read");
});

test("conflictChoice", () => {
  const c = (o) => conflictChoice(Object.assign({ dirty: false, diskVersion: "v1", baseVersion: "v1", saveStatus: 0 }, o));
  // nothing happened
  assert.strictEqual(c({}), "none");
  assert.strictEqual(c({ dirty: true }), "none");
  // a clean buffer whose disk version changed reloads in place
  assert.strictEqual(c({ diskVersion: "v2" }), "reload");
  // a dirty buffer whose disk version changed gets the bar: never silently replaced
  assert.strictEqual(c({ dirty: true, diskVersion: "v2" }), "bar");
  // a 409 gets the bar and keeps it, whatever else is true (Review Focus 1)
  assert.strictEqual(c({ saveStatus: 409 }), "bar");
  assert.strictEqual(c({ dirty: true, saveStatus: 409 }), "bar");
  assert.strictEqual(c({ dirty: true, diskVersion: "v2", saveStatus: 409 }), "bar");
  assert.strictEqual(c({ dirty: true, diskVersion: "v1", saveStatus: 409 }), "bar");
  // a 404 is the file gone, dirty or not
  assert.strictEqual(c({ saveStatus: 404 }), "deleted");
  assert.strictEqual(c({ dirty: true, saveStatus: 404 }), "deleted");
  assert.strictEqual(c({ dirty: true, diskVersion: "v2", saveStatus: 404 }), "deleted");
  // a version we do not have yet is not a change
  assert.strictEqual(c({ diskVersion: "" }), "none");
  assert.strictEqual(c({ dirty: true, diskVersion: undefined }), "none");
  assert.strictEqual(c({ baseVersion: "" , diskVersion: "v2" }), "none");
  assert.strictEqual(c({ dirty: true, baseVersion: undefined, diskVersion: "v2" }), "none");
  // other save statuses are not conflicts (the save path reports those itself)
  assert.strictEqual(c({ saveStatus: 200 }), "none");
  assert.strictEqual(c({ saveStatus: 500 }), "none");
  assert.strictEqual(c({ saveStatus: 0, dirty: true }), "none");
  assert.strictEqual(conflictChoice({}), "none");
  assert.strictEqual(conflictChoice(undefined), "none");
});

test("conflictText: the bar's sentence names the session and the file, and without a session says the file changed on disk", () => {
  assert.deepStrictEqual(conflictText("fix-fs-cap", "CONFIG.md"), ["fix-fs-cap changed CONFIG.md", " after you started editing. Your edits are not saved yet."]);
  assert.strictEqual(conflictText("fix-fs-cap", "CONFIG.md").join(""), "fix-fs-cap changed CONFIG.md after you started editing. Your edits are not saved yet.");
  for (const none of ["", "  ", undefined, null, 7]) {
    assert.strictEqual(conflictText(none, "a.go")[0], "a.go changed on disk", JSON.stringify(none));
  }
  assert.strictEqual(conflictText("  notes ", "a.go")[0], "notes changed a.go", "a name with spaces round it is trimmed");
  assert.strictEqual(DELETED_TEXT, "This file was deleted on disk.");
});

test("textEOL: Monaco's rule for the line ending a text gives a model; null when there is no line break", () => {
  assert.strictEqual(textEOL(""), null);
  assert.strictEqual(textEOL("one line"), null);
  assert.strictEqual(textEOL("a\nb\n"), "\n");
  assert.strictEqual(textEOL("a\r\nb\r\n"), "\r\n");
  assert.strictEqual(textEOL("a\r\nb\r\nc\n"), "\r\n", "most breaks are CRLF");
  assert.strictEqual(textEOL("a\r\nb\nc\n"), "\n", "most breaks are LF");
  assert.strictEqual(textEOL("a\r\nb\nc"), "\n", "half is not more than half");
});
