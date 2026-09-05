// The two rails' pure layer. This half is the icon rail's item list: what a
// 52px column of 40×40 buttons contains, in order, with an accessible name on
// every one of them. The design gives only the sidebar toggle a title; a button
// a screen reader cannot name is not a button anyone can use.
const test = require("node:test");
const assert = require("node:assert");
const { railItems, railMeta, startedText, ctxFill } = require("../static/steerrail.js");

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

// The steer rail's own half: the panel beside ONE session. Every assertion here
// is about a field the daemon really returns — the rail invents nothing, and a
// row it cannot fill is a row it does not draw.

test("the rail shows a row only for a field that has a value", () => {
  const m = railMeta({ id: "a", name: "review", activity: "working", runtime: "box", created: "2026-09-05T10:00:00Z" },
    { model: "opus-5", effort: "high" }, "~/culiops-agent-box", Date.parse("2026-09-05T13:12:00Z"));
  assert.deepStrictEqual(m.rows, [
    { k: "model", v: "opus-5" },
    { k: "effort", v: "high" },
    { k: "cwd", v: "~/culiops-agent-box" },
    { k: "started", v: "3h 12m ago" },
  ]);
  assert.deepStrictEqual(m.tier, { label: "box", cls: "bdg box" });
  assert.strictEqual(m.name, "review");
});

test("no usage, no cwd, no created — the rail renders no rows, and invents none", () => {
  const m = railMeta({ id: "a", activity: "done" }, null, "", 0);
  assert.deepStrictEqual(m.rows, []);
  assert.strictEqual(m.name, "a");
  assert.strictEqual(m.tier, null); // an absent runtime is not "box"
});

test("host is drawn as host", () => {
  assert.deepStrictEqual(railMeta({ id: "a", runtime: "host" }, null, "", 0).tier,
    { label: "host", cls: "bdg host" });
});

test("an absent session is null, never a throw", () => {
  assert.strictEqual(railMeta(null, null, "", 0), null);
});

test("started is relative, and degrades rather than lying", () => {
  const t0 = Date.parse("2026-09-05T10:00:00Z");
  assert.strictEqual(startedText("2026-09-05T10:00:00Z", t0 + 30_000), "just now");
  assert.strictEqual(startedText("2026-09-05T10:00:00Z", t0 + 9 * 60_000), "9m ago");
  assert.strictEqual(startedText("2026-09-05T10:00:00Z", t0 + 3 * 3600_000 + 12 * 60_000), "3h 12m ago");
  assert.strictEqual(startedText("2026-09-05T10:00:00Z", t0 + 2 * 86400_000 + 4 * 3600_000), "2d 4h ago");
  assert.strictEqual(startedText("2026-09-05T10:00:00Z", t0 - 5000), "just now"); // clock skew is not "-1m ago"
  assert.strictEqual(startedText("", t0), "");
  assert.strictEqual(startedText("not a date", t0), "");
});

test("the context bar prefers the server's percentage to its own arithmetic", () => {
  // usage.go omits pct when the max is unknown or already exceeded, so a
  // present pct is the server's verdict and must win over a local division.
  assert.deepStrictEqual(ctxFill({ contextTokens: 134982, contextMax: 200000, pct: 67 }),
    { pct: 67, width: "67%", used: 134982, max: 200000 });
});

test("with no percentage it computes one, and clamps it", () => {
  assert.strictEqual(ctxFill({ contextTokens: 100000, contextMax: 200000 }).pct, 50);
  assert.strictEqual(ctxFill({ contextTokens: 300000, contextMax: 200000 }).pct, 100);
  assert.strictEqual(ctxFill({ contextTokens: 5000, contextMax: 0 }).pct, null);
  assert.strictEqual(ctxFill({}).pct, null);
  assert.strictEqual(ctxFill(null).pct, null);
});

test("a null percentage draws no fill rather than a zero-width one", () => {
  assert.strictEqual(ctxFill(null).width, "0%");
});
