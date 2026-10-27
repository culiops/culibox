// internal/web/jstest/paneview.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const p = require("../static/paneview.js");

const on = (view) => ({ view, hasSession: true });

test("paneViewFor: a header tab opens its view, the same tab again returns to Terminal", () => {
  assert.equal(p.paneViewFor(on("terminal"), "access"), "access");
  assert.equal(p.paneViewFor(on("access"), "access"), "terminal", "terminal → access → access = terminal");
  assert.equal(p.paneViewFor(on("activity"), "activity"), "terminal");
});

test("paneViewFor: switching between the two views takes one press", () => {
  assert.equal(p.paneViewFor(on("activity"), "access"), "access");
  assert.equal(p.paneViewFor(on("access"), "activity"), "activity");
});

test("paneViewFor: Terminal is always Terminal, pressed from anywhere", () => {
  for (const v of ["terminal", "activity", "access"]) assert.equal(p.paneViewFor(on(v), "terminal"), "terminal", v);
});

test("paneViewFor: a pane with no session has only Terminal", () => {
  for (const k of ["terminal", "activity", "access"]) {
    assert.equal(p.paneViewFor({ view: "terminal", hasSession: false }, k), "terminal", k);
    assert.equal(p.paneViewFor({ view: "access", hasSession: false }, k), "terminal", "a stale view closes: " + k);
  }
});

test("paneViewFor: an unknown kind or state falls back to Terminal", () => {
  assert.equal(p.paneViewFor(on("terminal"), "files"), "terminal", "Files is the drawer, not a view");
  assert.equal(p.paneViewFor(on("bogus"), "access"), "access");
  assert.equal(p.paneViewFor(on("activity"), "bogus"), "activity", "an unknown press leaves the view alone");
  assert.equal(p.paneViewFor(null, "access"), "terminal");
  assert.equal(p.paneViewFor(undefined, undefined), "terminal");
});

test("paneViewKind: only the two views and Terminal are kinds", () => {
  assert.deepEqual(p.PANE_VIEWS, ["terminal", "activity", "access"]);
  assert.equal(p.paneViewKind("activity"), "activity");
  assert.equal(p.paneViewKind("files"), "terminal");
  assert.equal(p.paneViewKind(null), "terminal");
});

// The grid as tabs.js hands it over: each cell's terminal (cellTerminal), its
// tabs and its dock.
const grid = () => ({
  cells: [
    { term: "agent-a", tabs: [{ kind: "terminal", id: "agent-a" }], dock: { ids: ["sh-1", "sh-2"], active: "sh-2" } },
    { term: "agent-b", tabs: [{ kind: "viewer", id: "/w/x.go" }, { kind: "terminal", id: "agent-b" }], dock: null },
    { term: null, tabs: [{ kind: "viewer", id: "/w/y.go" }], dock: null },
  ],
});

test("paneViewTarget: a docked shell resolves to the agent its dock hangs off", () => {
  assert.deepEqual(p.paneViewTarget(grid(), "sh-2", 0), { cellIndex: 0, sid: "agent-a" });
  assert.deepEqual(p.paneViewTarget(grid(), "sh-1", 1), { cellIndex: 0, sid: "agent-a" }, "wherever focus is");
});

test("paneViewTarget: a pane's own session resolves to that pane", () => {
  assert.deepEqual(p.paneViewTarget(grid(), "agent-a", 1), { cellIndex: 0, sid: "agent-a" });
  assert.deepEqual(p.paneViewTarget(grid(), "agent-b", 0), { cellIndex: 1, sid: "agent-b" });
});

test("paneViewTarget: no id means the focused pane's agent, never its docked shell", () => {
  assert.deepEqual(p.paneViewTarget(grid(), null, 0), { cellIndex: 0, sid: "agent-a" });
  assert.deepEqual(p.paneViewTarget(grid(), undefined, 1), { cellIndex: 1, sid: "agent-b" });
  assert.equal(p.paneViewTarget(grid(), null, 2), null, "a pane of files has no session");
  assert.equal(p.paneViewTarget(grid(), null, -1), null, "no focused pane");
});

test("paneViewTarget: a session open nowhere has no pane yet", () => {
  assert.equal(p.paneViewTarget(grid(), "agent-z", 0), null);
  assert.equal(p.paneViewTarget({ cells: [] }, "agent-a", 0), null);
  assert.equal(p.paneViewTarget(null, "agent-a", 0), null);
});

test("paneViewTarget: a viewer path is never a session", () => {
  assert.equal(p.paneViewTarget(grid(), "/w/x.go", 0), null);
});
