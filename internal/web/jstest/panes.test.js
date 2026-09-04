"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { PANES_KEY, loadCollapsed, saveCollapsed, collapsedSummary, splitterLive } =
  require("../static/panes.js");

test("PANES_KEY is the stored key", () => {
  assert.equal(PANES_KEY, "agent-box:panesCollapsed");
});

// index.html's pre-paint <script> hand-mirrors loadCollapsed's parse, because
// it runs before panes.js has loaded and cannot call in here. A key rename on
// one side and not the other would silently split the boot-time class from
// the runtime one — this catches that at the one point they can still be
// compared: the literal key string.
test("index.html's pre-paint script uses the same storage key", () => {
  const html = fs.readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");
  assert.ok(html.includes(PANES_KEY), "index.html does not reference " + PANES_KEY);
});

// Storage is a hostile input: a hand-edited value, a value from an older
// version, or a quota-truncated string must all degrade to "nothing collapsed"
// rather than throwing during the pre-paint script and blocking the page.
test("loadCollapsed defaults both panes to open on anything unusable", () => {
  const open = { sessions: false, files: false };
  assert.deepEqual(loadCollapsed(null), open);
  assert.deepEqual(loadCollapsed(""), open);
  assert.deepEqual(loadCollapsed("{"), open);
  assert.deepEqual(loadCollapsed("[]"), open);
  assert.deepEqual(loadCollapsed('"sessions"'), open);
  assert.deepEqual(loadCollapsed("{}"), open);
});

test("loadCollapsed reads each pane independently and coerces to boolean", () => {
  assert.deepEqual(loadCollapsed('{"sessions":true,"files":false}'),
    { sessions: true, files: false });
  assert.deepEqual(loadCollapsed('{"sessions":false,"files":true}'),
    { sessions: false, files: true });
  assert.deepEqual(loadCollapsed('{"sessions":true,"files":true}'),
    { sessions: true, files: true });
  // A truthy non-boolean is still a collapse; an absent key is not.
  assert.deepEqual(loadCollapsed('{"sessions":1}'), { sessions: true, files: false });
});

test("saveCollapsed round-trips through loadCollapsed", () => {
  const s = { sessions: true, files: false };
  assert.deepEqual(loadCollapsed(saveCollapsed(s)), s);
  const both = { sessions: true, files: true };
  assert.deepEqual(loadCollapsed(saveCollapsed(both)), both);
});

// The collapsed Sessions bar's summary is only the waiting count: the total
// session count lives in #sessions-count, which #sessions-phd keeps on screen
// (it isn't one of the elements a collapsed pane hides), so collapsedSummary
// has no reason to carry it too.
test("collapsedSummary describes a collapsed Sessions bar", () => {
  assert.deepEqual(collapsedSummary("sessions", { waiting: 1 }),
    { text: "1 waiting", dot: true });
  assert.deepEqual(collapsedSummary("sessions", { waiting: 3 }),
    { text: "3 waiting", dot: true });
});

// No agent is waiting: the pip would be a yellow dot announcing nothing.
test("collapsedSummary shows no pip when nothing is waiting", () => {
  assert.deepEqual(collapsedSummary("sessions", { waiting: 0 }),
    { text: "", dot: false });
  assert.deepEqual(collapsedSummary("sessions", {}),
    { text: "", dot: false });
});

test("collapsedSummary describes a collapsed Files bar with its root", () => {
  assert.deepEqual(collapsedSummary("files", { root: "~/culiops-agent-box" }),
    { text: "~/culiops-agent-box", dot: false });
  assert.deepEqual(collapsedSummary("files", { root: "" }), { text: "", dot: false });
  assert.deepEqual(collapsedSummary("files", {}), { text: "", dot: false });
});

// The splitter resizes one pane against the other. With either collapsed to a
// fixed 36px bar there is nothing to resize, and the drag math divides by a
// degenerate rect.
test("splitterLive is false unless both panes are open", () => {
  assert.equal(splitterLive({ sessions: false, files: false }), true);
  assert.equal(splitterLive({ sessions: true, files: false }), false);
  assert.equal(splitterLive({ sessions: false, files: true }), false);
  assert.equal(splitterLive({ sessions: true, files: true }), false);
});
