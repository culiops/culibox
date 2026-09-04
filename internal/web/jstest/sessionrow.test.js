"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { attachedElsewhere, rowClasses, sessionMeta, menuItems, jumpTarget } =
  require("../static/sessionrow.js");

const sess = (over) => Object.assign({ id: "claude-1", attached: false, exited: false }, over);

test("a session nobody is attached to is never elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess(), false), false);
  assert.strictEqual(attachedElsewhere(sess(), true), false);
});

test("attached and held by this page is not elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true }), true), false);
});

test("attached and not held by this page is elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true }), false), true);
});

// An exited session's viewer is not something anyone can act on, and the row
// already says "exited".
test("an exited session is never elsewhere, even while attached", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true, exited: true }), false), false);
});

// An older daemon behind a newer page sends no `attached` at all. The badge must
// never appear on a claim the server did not make.
test("a response with no attached field reads as not attached", () => {
  assert.strictEqual(attachedElsewhere({ id: "claude-1", exited: false }, false), false);
});

test("a missing session is not elsewhere rather than a crash", () => {
  assert.strictEqual(attachedElsewhere(null, false), false);
  assert.strictEqual(attachedElsewhere(undefined, false), false);
});

// Guards against a truthy-but-not-true value ever being treated as attached.
test("only a real boolean true counts as attached", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: "yes" }), false), false);
  assert.strictEqual(attachedElsewhere(sess({ attached: 1 }), false), false);
});

// The row has exactly two states above its base, and they are different axes:
// "needs you" is about the agent, "selected" is about where your keystrokes go.
// A row can carry both at once and must then carry both classes.
test("rowClasses marks a waiting session as needing you", () => {
  assert.deepEqual(rowClasses({ activity: "waiting" }, {}), ["srow", "needs"]);
  assert.deepEqual(rowClasses({ activity: "working" }, {}), ["srow"]);
  assert.deepEqual(rowClasses({ activity: "crashed" }, {}), ["srow"]);
});

test("rowClasses marks the focused pane's session as selected", () => {
  assert.deepEqual(rowClasses({ id: "a", activity: "working" }, { selectedId: "a" }),
    ["srow", "sel"]);
  assert.deepEqual(rowClasses({ id: "a", activity: "working" }, { selectedId: "b" }),
    ["srow"]);
  // Nothing is selected when the focused pane shows a viewer or holds no tabs.
  assert.deepEqual(rowClasses({ id: "a", activity: "working" }, { selectedId: null }),
    ["srow"]);
});

test("rowClasses carries both states at once", () => {
  assert.deepEqual(rowClasses({ id: "a", activity: "waiting" }, { selectedId: "a" }),
    ["srow", "sel", "needs"]);
});

// A teardown in flight is neither: the row is disabled, and painting it amber
// would advertise a session you can no longer answer.
test("rowClasses marks a killing row and drops the other states", () => {
  assert.deepEqual(rowClasses({ id: "a", activity: "waiting" }, { selectedId: "a", killing: true }),
    ["srow", "killing"]);
});

// The meta line is one line, three segments, and any missing segment is dropped
// rather than leaving a dangling separator.
test("sessionMeta joins agent, status and directory", () => {
  assert.equal(sessionMeta({ agent: "claude" }, "~/culifeed", "working"),
    "claude · working · ~/culifeed");
  assert.equal(sessionMeta({ agent: "sh" }, "~/x", "exited"), "sh · exited · ~/x");
});

test("sessionMeta drops empty segments instead of leaving separators", () => {
  assert.equal(sessionMeta({ agent: "claude" }, "", "working"), "claude · working");
  assert.equal(sessionMeta({}, "~/x", ""), "~/x");
  assert.equal(sessionMeta({}, "", ""), "");
  assert.equal(sessionMeta(null, null, null), "");
});

// The menu's contents are fixed, but one item is meaningless on a non-claude
// session and a pair of them is meaningless without a working directory.
// "End session…" is always last and always the only dangerous one.
test("menuItems offers the full list for a claude session with a cwd", () => {
  const ids = menuItems({ claude: true, cwd: "/home/x" }).map((i) => i.id);
  assert.deepEqual(ids, ["rename", "files", "attribution", "copycwd", "newpane", "end"]);
});

test("menuItems drops context-by-tool for a non-claude session", () => {
  const ids = menuItems({ claude: false, cwd: "/home/x" }).map((i) => i.id);
  assert.deepEqual(ids, ["rename", "files", "copycwd", "newpane", "end"]);
});

test("menuItems drops the directory actions when there is no cwd", () => {
  const ids = menuItems({ claude: true }).map((i) => i.id);
  assert.deepEqual(ids, ["rename", "attribution", "newpane", "end"]);
});

test("menuItems always ends with the one dangerous item", () => {
  for (const s of [{ claude: true, cwd: "/x" }, { claude: false }, {}]) {
    const items = menuItems(s);
    const last = items[items.length - 1];
    assert.equal(last.id, "end");
    assert.equal(last.label, "End session…");
    assert.equal(last.danger, true);
    assert.equal(items.filter((i) => i.danger).length, 1);
  }
});

// The number keys address the sidebar list the operator is looking at, 1-based.
test("jumpTarget maps 1..9 onto the session list", () => {
  const list = [{ id: "a" }, { id: "b" }, { id: "c" }];
  assert.equal(jumpTarget(1, list).id, "a");
  assert.equal(jumpTarget(3, list).id, "c");
});

test("jumpTarget returns null for anything out of range", () => {
  const list = [{ id: "a" }];
  assert.equal(jumpTarget(2, list), null);
  assert.equal(jumpTarget(0, list), null);
  assert.equal(jumpTarget(1, []), null);
  assert.equal(jumpTarget(1, null), null);
  assert.equal(jumpTarget(NaN, list), null);
});
