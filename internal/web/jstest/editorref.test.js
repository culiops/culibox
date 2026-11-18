"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  rangeRef, insertGate, editorMode, conflictChoice, DELETED_TEXT, conflictText,
  selectionLines, FOLLOW_TOOLS, followFile, followPlan, followParse, followWith,
} = require("../static/editorref.js");

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

// --- the selection bar's lines ---------------------------------------------------------------------------------------

const sel = (sl, sc, el, ec) => ({ startLineNumber: sl, startColumn: sc, endLineNumber: el, endColumn: ec });

test("selectionLines: an empty selection is nothing, a one-line or several-line one is its lines", () => {
  assert.strictEqual(selectionLines(sel(4, 7, 4, 7)), null, "a caret");
  assert.deepStrictEqual(selectionLines(sel(4, 2, 4, 9)), { a: 4, b: 4 }, "part of one line");
  assert.deepStrictEqual(selectionLines(sel(3, 1, 5, 12)), { a: 3, b: 5 }, "a drag over three lines");
  assert.deepStrictEqual(selectionLines(sel(3, 8, 5, 2)), { a: 3, b: 5 }, "from the middle of a line to the middle of another");
});

test("selectionLines: a selection that ends at the start of a line does not count that line (a line-number click selects the line and its break)", () => {
  assert.deepStrictEqual(selectionLines(sel(8, 1, 9, 1)), { a: 8, b: 8 }, "a click on line 8's number");
  assert.deepStrictEqual(selectionLines(sel(3, 1, 6, 1)), { a: 3, b: 5 }, "three whole lines");
  assert.deepStrictEqual(selectionLines(sel(8, 1, 8, 20)), { a: 8, b: 8 }, "the last line of a file with no final newline");
  assert.deepStrictEqual(selectionLines(sel(8, 5, 9, 1)), { a: 8, b: 8 }, "the rest of a line and its break");
});

test("selectionLines: anything that is not a selection is nothing", () => {
  for (const bad of [null, undefined, 3, "x", {}, sel(0, 1, 2, 1), sel(5, 1, 4, 1), sel(1.5, 1, 2, 1), sel(1, "1", 2, 1), sel(2, 5, 2, 3)]) {
    assert.strictEqual(selectionLines(bad), null, JSON.stringify(bad));
  }
});

// --- Follow agent: what to show for one answer of the watch -----------------------------------------------------------

const inW = (p) => p.startsWith("/w/");
const ev = (over) => Object.assign({ ts: 100, kind: "tool", tool: "Edit", detail: "/w/a.go", state: "ok" }, over);
const file = (path, ts) => ({ path, add: 1, del: 0, ts });

test("followPlan: a finished edit of a file under the root is the one shown, whatever the tool that wrote it", () => {
  for (const tool of ["Edit", "Write", "MultiEdit", "NotebookEdit"]) {
    assert.deepStrictEqual(followPlan([file("/w/a.go", 100)], [ev({ tool })], inW, 0), { path: "/w/a.go", seen: 100 }, tool);
  }
  assert.deepStrictEqual(FOLLOW_TOOLS, ["Edit", "Write", "MultiEdit", "NotebookEdit"]);
});

test("followPlan: reads, searches, commands, calls still pending or failed, and other kinds of row show nothing", () => {
  const files = [file("/w/a.go", 100)];
  for (const e of [
    ev({ tool: "Read" }), ev({ tool: "Bash", detail: "go test ./..." }), ev({ tool: "Grep" }), ev({ tool: "Task" }), ev({ tool: "mcp__x__edit" }),
    ev({ state: "pending" }), ev({ state: "error" }), ev({ state: undefined }), ev({ kind: "you" }), ev({ kind: "clear" }), ev({ kind: "credential" }),
    ev({ tool: "edit" }), null, "x", 7,
  ]) assert.strictEqual(followPlan(files, [e], inW, 100).path, "", JSON.stringify(e));
});

test("followPlan: the newest qualifying event wins, and one that does not qualify is passed over", () => {
  const files = [file("/w/c.go", 130), file("/w/b.go", 120), file("/w/a.go", 100)];
  const events = [ev({ ts: 140, tool: "Bash", detail: "make" }), ev({ ts: 130, detail: "/w/c.go" }), ev({ ts: 120, detail: "/w/b.go" })];
  assert.deepStrictEqual(followPlan(files, events, inW, 0), { path: "/w/c.go", seen: 130 });
  assert.strictEqual(followPlan(files, [ev({ ts: 130, detail: "/w/c.go", state: "pending" }), ev({ ts: 120, detail: "/w/b.go" })], inW, 0).path, "/w/b.go");
});

test("followPlan: a path outside the dock's root is ignored, and so is its mark in the choice (the next one inside is shown)", () => {
  const files = [file("/other/x.go", 130), file("/w/b.go", 120)];
  const events = [ev({ ts: 130, detail: "/other/x.go" }), ev({ ts: 120, detail: "/w/b.go" })];
  assert.deepStrictEqual(followPlan(files, events, inW, 0), { path: "/w/b.go", seen: 130 });
  assert.strictEqual(followPlan([file("/other/x.go", 130)], [ev({ ts: 130, detail: "/other/x.go" })], inW, 0).path, "", "only elsewhere: nothing");
  assert.strictEqual(followPlan([file("/w/a.go", 100)], [ev()], undefined, 0).path, "", "no way to tell the root: nothing (fails closed)");
});

test("followPlan: the path comes from Activity's files, never from the (masked) detail alone", () => {
  // A detail that files does not have names nothing: the edit is not known as applied, and the text cannot be trusted as a path.
  assert.strictEqual(followPlan([file("/w/a.go", 100)], [ev({ detail: "/w/zzz.go" })], inW, 100).path, "");
  assert.strictEqual(followPlan([], [ev()], inW, 100).path, "");
  // A masked detail and a cut one are not paths, even when files carries the very same string.
  const masked = "/w/[masked:github-token].go";
  assert.strictEqual(followPlan([file(masked, 100)], [ev({ detail: masked })], inW, 100).path, "");
  const cut = "/w/" + "d/".repeat(140) + "…";
  assert.strictEqual(followPlan([file(cut, 100)], [ev({ detail: cut })], inW, 100).path, "");
  // What is opened is the entry's own path.
  assert.strictEqual(followFile([file("/w/a.go", 1)], "/w/a.go").path, "/w/a.go");
});

test("followFile: a relative detail names the one entry that ends with it; two or none name nothing", () => {
  const files = [file("/w/src/a.go", 1), file("/w/other/b.go", 2)];
  assert.strictEqual(followFile(files, "src/a.go").path, "/w/src/a.go");
  assert.strictEqual(followFile(files, "./src/a.go").path, "/w/src/a.go");
  assert.strictEqual(followFile(files, "a.go").path, "/w/src/a.go");
  assert.strictEqual(followFile(files, "x/a.go"), null);
  assert.strictEqual(followFile([file("/w/x/a.go", 1), file("/w/y/a.go", 2)], "a.go"), null, "ambiguous");
  assert.strictEqual(followFile(files, "/w/src"), null, "an absolute detail must match exactly");
  assert.strictEqual(followFile(files, ""), null);
  assert.strictEqual(followFile(files, 5), null);
  assert.strictEqual(followFile("nope", "a.go"), null);
});

test("followPlan: the first answer only sets the mark; later, an edit the events did not carry (a call seen pending, finished since) is shown once", () => {
  const files = [file("/w/b.go", 220), file("/w/a.go", 100)];
  // No mark yet (the first answer after Follow subscribed): the files are what was already there.
  assert.deepStrictEqual(followPlan(files, [], inW, null), { path: "", seen: 220 });
  // The call was pending at the last answer; now its file is in `files` with a newer time and the event is not new.
  assert.deepStrictEqual(followPlan(files, [], inW, 100), { path: "/w/b.go", seen: 220 });
  // That edit is looked at once: the same answer again shows nothing.
  assert.deepStrictEqual(followPlan(files, [], inW, 220), { path: "", seen: 220 });
  // The newest of several that finished meanwhile; one outside the root is skipped.
  assert.strictEqual(followPlan([file("/z/q.go", 400), file("/w/c.go", 300), file("/w/b.go", 220)], [], inW, 100).path, "/w/c.go");
  // A first answer that also carries a new, finished event still shows it: events are new by the watch's own rule.
  assert.strictEqual(followPlan(files, [ev({ ts: 220, detail: "/w/b.go" })], inW, null).path, "/w/b.go");
});

test("followPlan: bad input is nothing, and the mark never goes back", () => {
  for (const [f, e] of [[null, null], [undefined, undefined], ["x", 3], [[null, 4, {}], [undefined]], [{}, {}]]) {
    assert.deepStrictEqual(followPlan(f, e, inW, 50), { path: "", seen: 50 }, JSON.stringify([f, e]));
  }
  assert.strictEqual(followPlan([file("/w/a.go", 10)], [], inW, 50).seen, 50, "an older list does not lower it");
  assert.strictEqual(followPlan([{ path: "/w/a.go", ts: "late" }, { path: "/w/b.go" }], [], inW, 50).seen, 50, "a ts that is not a number is not a time");
});

test("followParse and followWith: only {sid: true} is read, a toggle adds or removes one session, and gone sessions are dropped", () => {
  assert.deepStrictEqual({ ...followParse('{"a":true,"b":false,"c":1,"d":"yes","":true,"e":true}') }, { a: true, e: true });
  for (const bad of [null, undefined, "", "not json", "[true]", "3", '"x"', "null", 5]) assert.deepStrictEqual({ ...followParse(bad) }, {}, String(bad));
  assert.strictEqual(Object.getPrototypeOf(followParse("{}")), null, "a session id is a key: no prototype to collide with");
  assert.strictEqual(followParse('{"__proto__":true}').__proto__, true, "and it stays an own key");
  const base = followParse('{"a":true,"b":true}');
  assert.deepStrictEqual({ ...followWith(base, "c", true) }, { a: true, b: true, c: true });
  assert.deepStrictEqual({ ...followWith(base, "a", false) }, { b: true });
  assert.deepStrictEqual({ ...base }, { a: true, b: true }, "the map it was given is not changed");
  assert.deepStrictEqual({ ...followWith(base, "c", true, ["a", "c"]) }, { a: true, c: true }, "b is gone from the session list");
  assert.deepStrictEqual({ ...followWith(base, "c", true, []) }, { a: true, b: true, c: true }, "no list yet: nothing is dropped");
  assert.deepStrictEqual({ ...followWith(base, "", true) }, { a: true, b: true }, "no session, no entry");
  assert.deepStrictEqual({ ...followWith(undefined, "a", true) }, { a: true });
});
