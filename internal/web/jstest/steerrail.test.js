// The two rails' pure layer. This half is the icon rail's item list: what a
// 52px column of 40×40 buttons contains, in order, with an accessible name on
// every one of them. The design gives only the sidebar toggle a title; a button
// a screen reader cannot name is not a button anyone can use.
const test = require("node:test");
const assert = require("node:assert");
const { railItems } = require("../static/steerrail.js");

test("the toggle leads and the add trails, even with no sessions", () => {
  const items = railItems([], {});
  assert.deepStrictEqual(items.map((i) => i.kind), ["toggle", "new"]);
  assert.strictEqual(items[0].label, "Show sidebar");
  assert.strictEqual(items[1].label, "New session");
});

test("one item per session, in the list's own order", () => {
  const items = railItems(
    [{ id: "a", name: "review", activity: "working" }, { id: "b", name: "drain", activity: "done" }], {});
  assert.deepStrictEqual(items.map((i) => i.kind), ["toggle", "session", "session", "new"]);
  assert.deepStrictEqual(items.slice(1, 3).map((i) => i.id), ["a", "b"]);
});

test("a waiting session is alert; the focused one is on; both at once is legal", () => {
  const [, w] = railItems([{ id: "a", name: "n", activity: "waiting" }], { focusedId: "a" });
  assert.strictEqual(w.alert, true);
  assert.strictEqual(w.on, true);
  const [, q] = railItems([{ id: "a", name: "n", activity: "working" }], { focusedId: "b" });
  assert.strictEqual(q.alert, false);
  assert.strictEqual(q.on, false);
});

test("alert follows the activity, never the focus", () => {
  const [, s] = railItems([{ id: "a", name: "n", activity: "working" }], { focusedId: "a" });
  assert.strictEqual(s.on, true);
  assert.strictEqual(s.alert, false);
});

test("every item carries a non-empty label", () => {
  const items = railItems([{ id: "a", name: "", activity: "waiting" }, { id: "b" }], { focusedId: "" });
  for (const i of items) assert.ok(i.label.length > 0, i.kind + " has no label");
});

test("an unnamed session falls back to its id, and a waiting one says so", () => {
  const [, a, b] = railItems([{ id: "abc123", activity: "waiting" }, { id: "d", name: "drain" }], {});
  assert.strictEqual(a.label, "abc123 — waiting for you");
  assert.strictEqual(b.label, "drain");
});

test("a waiting session's name stays undecorated even though its label carries the suffix", () => {
  const [, a] = railItems([{ id: "abc123", name: "review", activity: "waiting" }], {});
  assert.strictEqual(a.name, "review");
  assert.strictEqual(a.label, "review — waiting for you");
});

test("an absent list is empty, never a throw", () => {
  assert.deepStrictEqual(railItems(null, null).map((i) => i.kind), ["toggle", "new"]);
});
