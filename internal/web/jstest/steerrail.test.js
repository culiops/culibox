// The two rails' pure layer. This half is the icon rail's item list: what a
// 52px column of 40×40 buttons contains, in order, with an accessible name on
// every one of them. The design gives only the sidebar toggle a title; a button
// a screen reader cannot name is not a button anyone can use.
const test = require("node:test");
const assert = require("node:assert");
const { railItems, railMeta, startedText, ctxFill, awsStatsRows, egressRow } = require("../static/steerrail.js");
// The real tierBadge, not a hand-written stand-in: the point of the change these
// tests cover is that the rail consumes tier.js's verdict rather than deriving a
// second one, so a fixture that agreed with the rail but not with tier.js would
// pass while the two surfaces disagreed on screen.
const { tierBadge } = require("../static/tier.js");

// startedText prepends a clock read in the machine's own local time (same as
// the rest of the app), so the expected clock half is computed the same way
// here rather than hard-coded — that keeps the test passing under any TZ.
function hhmm(iso) {
  const d = new Date(Date.parse(iso));
  return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
}

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
  const s = { id: "a", name: "review", activity: "working", runtime: "box", created: "2026-09-05T10:00:00Z" };
  const m = railMeta(s, { model: "opus-5", effort: "high" }, "~/culiops-agent-box",
    Date.parse("2026-09-05T13:12:00Z"), tierBadge(s, "runc"));
  assert.deepStrictEqual(m.rows, [
    { k: "model", v: "opus-5" },
    { k: "effort", v: "high" },
    { k: "cwd", v: "~/culiops-agent-box" },
    { k: "started", v: hhmm("2026-09-05T10:00:00Z") + " · 3h 12m ago" },
  ]);
  // A contained session takes .bdg.box, matching tierBadge's neutral tone —
  // the host colour is reserved for the tier that has none.
  assert.strictEqual(m.tier.label, "box");
  assert.strictEqual(m.tier.cls, "bdg box");
  assert.match(m.tier.title, /Contained \(runc\)/);
  assert.strictEqual(m.name, "review");
});

test("no usage, no cwd, no created — the rail renders no rows, and invents none", () => {
  const m = railMeta({ id: "a", activity: "done" }, null, "", 0, null);
  assert.deepStrictEqual(m.rows, []);
  assert.strictEqual(m.name, "a");
  assert.strictEqual(m.tier, null); // no badge from the caller, no badge drawn
});

test("an uncontained session is drawn in tier.js's words, at tier.js's severity", () => {
  const s = { id: "a", runtime: "host" };
  const t = railMeta(s, null, "", 0, tierBadge(s, "runc")).tier;
  assert.strictEqual(t.label, "host");
  assert.strictEqual(t.cls, "bdg host"); // danger tone reads as the canvas's warn-coloured host chip
  assert.match(t.title, /NOT contained/);
});

test("an unknown or absent runtime reads as host, never as no tier at all", () => {
  // The fail-loud fallback. A rail that draws nothing here would tell the
  // operator there is nothing to worry about, on the surface they steer from,
  // while the sidebar shouts danger about the same session.
  for (const s of [{ id: "a" }, { id: "b", runtime: "" }, { id: "c", runtime: "mystery" }]) {
    const t = railMeta(s, null, "", 0, tierBadge(s, "runc")).tier;
    assert.strictEqual(t.label, "host", JSON.stringify(s));
    assert.strictEqual(t.cls, "bdg host", JSON.stringify(s));
  }
});

test("an exited session shows no tier row: a dead session's containment is not actionable", () => {
  const s = { id: "a", runtime: "host", exited: true };
  assert.strictEqual(tierBadge(s, "runc"), null); // tier.js's decision...
  assert.strictEqual(railMeta(s, null, "", 0, tierBadge(s, "runc")).tier, null); // ...carried through
});

test("an absent session is null, never a throw", () => {
  assert.strictEqual(railMeta(null, null, "", 0, null), null);
});

test("started is relative, and degrades rather than lying", () => {
  const created = "2026-09-05T10:00:00Z";
  const t0 = Date.parse(created);
  const clock = hhmm(created);
  assert.strictEqual(startedText(created, t0 + 30_000), clock + " · just now");
  assert.strictEqual(startedText(created, t0 + 9 * 60_000), clock + " · 9m ago");
  assert.strictEqual(startedText(created, t0 + 3 * 3600_000 + 12 * 60_000), clock + " · 3h 12m ago");
  assert.strictEqual(startedText(created, t0 + 2 * 86400_000 + 4 * 3600_000), clock + " · 2d 4h ago");
  assert.strictEqual(startedText(created, t0 - 5000), clock + " · just now"); // clock skew is not "-1m ago"
  assert.strictEqual(startedText("", t0), "");
  assert.strictEqual(startedText("not a date", t0), "");
});

test("started leads with a zero-padded HH:MM clock, joined by ·", () => {
  // A separate, format-focused assertion (independent of the relative-time
  // fixture above): the clock half is always two digits each side of the
  // colon, even for a single-digit hour/minute.
  const created = "2026-09-05T00:05:00Z";
  const text = startedText(created, Date.parse(created) + 60_000);
  const [clockPart, relPart] = text.split(" · ");
  assert.match(clockPart, /^\d{2}:\d{2}$/);
  assert.strictEqual(clockPart, hhmm(created));
  assert.strictEqual(relPart, "1m ago");
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

test("railMeta appends the caller's extra rows after its own", () => {
  const s = { id: "a", activity: "done", created: "2026-09-05T10:00:00Z" };
  const m = railMeta(s, { model: "opus-5" }, "", Date.parse("2026-09-05T10:00:30Z"), null,
    [{ k: "aws", v: "sandbox · 123456789012" }, null]);
  assert.deepStrictEqual(m.rows.map((r) => r.k), ["model", "started", "aws"]);
  assert.deepStrictEqual(railMeta(s, null, "", 0, null, null).rows.map((r) => r.k), ["started"]);
});

test("awsStatsRows: never before the first mint, the zero time is not year 1", () => {
  assert.deepStrictEqual(awsStatsRows(null, 0), []);
  assert.deepStrictEqual(awsStatsRows({ stats: { mints: 0, last_mint: "0001-01-01T00:00:00Z" } }, Date.now()),
    [{ k: "minted", v: "0 mints · last mint never" }]);
  const now = Date.parse("2026-10-16T12:10:00Z");
  const rows = awsStatsRows({ stats: { mints: 1, last_mint: "2026-10-16T12:05:00Z" } }, now);
  assert.strictEqual(rows.length, 1);
  assert.strictEqual(rows[0].v, "1 mint · last mint " + startedText("2026-10-16T12:05:00Z", now));
});

test("awsStatsRows: last error is its own wrapping row, verbatim", () => {
  const err = "aws sso login --profile sandbox-admin (session expired)";
  const rows = awsStatsRows({ stats: { mints: 3, last_mint: "0001-01-01T00:00:00Z", last_error: err } }, 0);
  assert.deepStrictEqual(rows[1], { k: "error", v: err, wrap: true });
  assert.strictEqual(awsStatsRows({ stats: { mints: 3, last_mint: "0001-01-01T00:00:00Z", last_error: "" } }, 0).length, 1);
});

test("egressRow: absent without the egress object; allowlist and the denied count otherwise", () => {
  assert.strictEqual(egressRow(null), null);
  assert.strictEqual(egressRow({ stats: {} }), null);
  assert.deepStrictEqual(egressRow({ egress: { mode: "allowlist", denied: 0 } }), { k: "egress", v: "allowlist \u00b7 0 denied since start", wrap: true });
  assert.deepStrictEqual(egressRow({ egress: { mode: "allowlist", denied: 3 } }), { k: "egress", v: "allowlist \u00b7 3 denied since start", wrap: true });
});

test("egressRow: a missing or non-numeric denied count reads as zero, never NaN", () => {
  assert.strictEqual(egressRow({ egress: { mode: "allowlist" } }).v, "allowlist \u00b7 0 denied since start");
  assert.strictEqual(egressRow({ egress: { mode: "allowlist", denied: "x" } }).v, "allowlist \u00b7 0 denied since start");
});
