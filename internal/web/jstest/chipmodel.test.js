// internal/web/jstest/chipmodel.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const m = require("../static/chipmodel.js");

function term(id, name) { return { kind: "terminal", id, name }; }
function viewer(path) { return { kind: "viewer", id: "view:" + path, path, name: path.split("/").pop() }; }

test("terminal chip carries kind/label/title from the tab's name", () => {
  const t = term("s1", "worker");
  const model = m.chipModel([t], null, () => null);
  assert.deepEqual(model[0], { kind: "terminal", label: "worker", title: "worker", on: false, wait: false, closable: false, status: "" });
});

test("terminal chip falls back to id when unnamed", () => {
  const t = term("s1", "");
  const model = m.chipModel([t], null, () => null);
  assert.equal(model[0].label, "s1");
  assert.equal(model[0].title, "s1");
});

test("viewer chip: label/title are the basename / full path, never waits", () => {
  const v = viewer("/home/u/project/README.md");
  const model = m.chipModel([v], null, () => null);
  assert.deepEqual(model[0], {
    kind: "viewer", label: "README.md", title: "/home/u/project/README.md",
    on: false, wait: false, closable: false, status: "",
  });
});

test("the focused tab (any kind) is 'on' and the ONLY closable chip", () => {
  const t1 = term("s1", "a"), v1 = viewer("/x/y.md"), t2 = term("s2", "b");
  const model = m.chipModel([t1, v1, t2], v1, () => null);
  assert.deepEqual(model.map((c) => c.on), [false, true, false]);
  assert.deepEqual(model.map((c) => c.closable), [false, true, false]);
});

test("a terminal chip waits only when activityOf reports 'waiting'; a viewer never waits even if focused", () => {
  const t1 = term("s1", "a"), v1 = viewer("/x/y.md");
  const activityOf = (t) => (t === t1 ? "waiting" : null);
  const model = m.chipModel([t1, v1], v1, activityOf);
  assert.equal(model[0].wait, true);
  assert.equal(model[1].wait, false);
});

test("order is preserved exactly as given — the caller owns cell-then-tab ordering", () => {
  const tabs = [term("s3", "c"), viewer("/a.md"), term("s1", "a")];
  const model = m.chipModel(tabs, null, () => null);
  assert.deepEqual(model.map((c) => c.kind), ["terminal", "viewer", "terminal"]);
});

test("empty/missing input yields an empty list", () => {
  assert.deepEqual(m.chipModel([], null, () => null), []);
  assert.deepEqual(m.chipModel(null, null, () => null), []);
});

test("activityOf is never consulted for a viewer tab", () => {
  const v = viewer("/x.md");
  let called = false;
  m.chipModel([v], v, () => { called = true; return "waiting"; });
  assert.equal(called, false);
});

test("activityOf missing (undefined) never throws — a terminal chip just never waits", () => {
  const t = term("s1", "a");
  assert.doesNotThrow(() => m.chipModel([t], t, undefined));
  assert.equal(m.chipModel([t], t, undefined)[0].wait, false);
});

// m4: mobile loses the desktop tab-bar's connection readout once .cell-bar is
// hidden — chipModel carries the same t.status() so the mobile chip strip
// and topbar can show it too.
test("terminal chip's status mirrors statusOf, suppressed at the healthy steady state", () => {
  const t = term("s1", "a");
  assert.equal(m.chipModel([t], null, () => null, () => "connected")[0].status, "");
  assert.equal(m.chipModel([t], null, () => null, () => "reconnecting…")[0].status, "reconnecting…");
  assert.equal(m.chipModel([t], null, () => null, () => "error")[0].status, "error");
});

test("a viewer chip's status is always empty — statusOf is never consulted for it", () => {
  const v = viewer("/x.md");
  let called = false;
  const model = m.chipModel([v], v, () => null, () => { called = true; return "error"; });
  assert.equal(called, false);
  assert.equal(model[0].status, "");
});

test("statusOf missing (undefined) never throws — a terminal chip's status is just empty", () => {
  const t = term("s1", "a");
  assert.doesNotThrow(() => m.chipModel([t], t, () => null, undefined));
  assert.equal(m.chipModel([t], t, () => null, undefined)[0].status, "");
});

// Fix round 2: renderMobileChips runs on every ~3s session poll, not only on
// navigation. Scrolling the strip on every render fights a manual scroll —
// shouldScrollToFocused gates it on an actual focus change since the last
// scroll.
test("shouldScrollToFocused: true the first time (lastScrolled is null/undefined)", () => {
  const t = term("s1", "a");
  assert.equal(m.shouldScrollToFocused(t, null), true);
  assert.equal(m.shouldScrollToFocused(t, undefined), true);
});

test("shouldScrollToFocused: false once the same tab is already the last-scrolled one", () => {
  const t = term("s1", "a");
  assert.equal(m.shouldScrollToFocused(t, t), false);
});

test("shouldScrollToFocused: true again when focus actually moves to a different tab", () => {
  const t1 = term("s1", "a"), t2 = term("s2", "b");
  assert.equal(m.shouldScrollToFocused(t2, t1), true);
});

test("shouldScrollToFocused: false when nothing is focused, regardless of lastScrolled", () => {
  const t = term("s1", "a");
  assert.equal(m.shouldScrollToFocused(null, null), false);
  assert.equal(m.shouldScrollToFocused(null, t), false);
});
