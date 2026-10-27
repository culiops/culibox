// internal/web/jstest/deskshell.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const d = require("../static/deskshell.js");

const NOW = Date.parse("2026-10-27T10:00:00Z");
const FUTURE = "2026-10-27T10:30:00Z";
const PAST = "2026-10-27T09:00:00Z";

const waiting = { id: "w", name: "w", activity: "waiting", need: "ask", ask: "aws logs tail", runtime: "box" };
const working = { id: "r", name: "r", activity: "working", runtime: "box" };
const idle = { id: "i", name: "i", activity: "idle", runtime: "host", summary: "" };
const sso = { id: "s", name: "s", activity: "working", need: "sso", runtime: "box" };

test("deskGroups keeps Needs you, Running, Idle and drops empty groups", () => {
  const g = d.deskGroups([idle, working, waiting, sso]);
  assert.deepEqual(g.map((x) => x.label), ["Needs you", "Running", "Idle"]);
  assert.deepEqual(g[0].sessions.map((s) => s.id), ["w", "s"], "an SSO need counts as needs-you");
  assert.deepEqual(d.deskGroups([working]).map((x) => x.key), ["running"]);
  assert.deepEqual(d.deskGroups([]), []);
  assert.deepEqual(d.deskGroups(null), []);
});

test("deskOrder flattens the groups in visible order", () => {
  const order = d.deskOrder(d.deskGroups([idle, working, waiting]));
  assert.deepEqual(order.map((s) => s.id), ["w", "r", "i"]);
  assert.deepEqual(d.deskOrder([]), []);
});

test("sessionElevated reads the legacy aws record and any named connection", () => {
  assert.equal(d.sessionElevated({ aws: { elevated_until: FUTURE } }, NOW), true);
  assert.equal(d.sessionElevated({ aws: { elevated_until: PAST } }, NOW), false);
  assert.equal(d.sessionElevated({ aws_connections: [{ name: "a" }, { name: "b", elevated_until: FUTURE }] }, NOW), true);
  assert.equal(d.sessionElevated({ aws_connections: [{ name: "a", elevated_until: PAST }] }, NOW), false);
  assert.equal(d.sessionElevated({}, NOW), false);
  assert.equal(d.sessionElevated(null, NOW), false);
});

test("elevatedCount counts a session once even with several live connections", () => {
  const two = { id: "x", aws: { elevated_until: FUTURE }, aws_connections: [{ elevated_until: FUTURE }, { elevated_until: FUTURE }] };
  assert.equal(d.elevatedCount([two, working], NOW), 1);
  const viaConns = { id: "y", aws_connections: [{ elevated_until: FUTURE }, { elevated_until: FUTURE }] };
  assert.equal(d.elevatedCount([viaConns, two, idle], NOW), 2);
  assert.equal(d.elevatedCount(null, NOW), 0);
});

test("rowChip: write when elevated, else the tier", () => {
  const live = { ...working, aws_connections: [{ name: "a", elevated_until: FUTURE }] };
  assert.deepEqual(d.rowChip(live, NOW, "runc"), { text: "write", tone: "write" });
  const host = d.rowChip(idle, NOW, "runc");
  assert.equal(host.tone, "host");
  assert.equal(host.text, "host");
  const box = d.rowChip(working, NOW, "runc");
  assert.equal(box.tone, "box");
  assert.equal(box.text, "box");
  assert.equal(d.rowChip({ ...working, exited: true }, NOW, "runc"), null);
});

test("rowSubline: amber needs-you text, else summary or the activity word", () => {
  const n = d.rowSubline({ activity: "waiting", need: "ask", ask: "aws logs tail" });
  assert.equal(n.amber, true);
  assert.ok(n.text.includes("aws logs tail"));
  assert.ok(n.text.startsWith("asks: "));
  assert.deepEqual(d.rowSubline({ activity: "working", summary: "fixing tests" }), { text: "fixing tests", amber: false });
  assert.deepEqual(d.rowSubline({ activity: "idle" }), { text: "idle", amber: false });
  assert.deepEqual(d.rowSubline({}), { text: "", amber: false });
});

test("paneHeaderModel: full slots in order for a wide pane", () => {
  const tab = { kind: "terminal", usage: () => ({ model: "opus-5", pct: 42 }) };
  const s = { ...working, name: "api", aws_connections: [{ name: "prod", label: "prod", account_id: "1", role_arn: "arn:aws:iam::1:role/r", elevated_until: FUTURE }] };
  const m = d.paneHeaderModel({ tab, session: s, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.equal(m.name, "api");
  assert.deepEqual(m.chips.map((c) => c.tone), ["box", "write"]);
  assert.equal(m.chips[1].text, "prod");
  assert.equal(m.model, "opus-5");
  assert.equal(m.ctx, "42%");
  assert.ok(m.dot && m.dot.cls);
});

test("paneHeaderModel narrow on an elevated session: exactly one chip, the write chip", () => {
  const tab = { kind: "terminal", usage: () => ({ model: "opus-5", pct: 42 }) };
  const s = { ...working, aws: { elevated_until: FUTURE }, aws_connections: [{ name: "a", label: "a", account_id: "1", role_arn: "arn:aws:iam::1:role/r", elevated_until: FUTURE }] };
  const m = d.paneHeaderModel({ tab, session: s, nowMs: NOW, boxRuntime: "runc", narrow: true });
  assert.equal(m.chips.length, 1);
  assert.equal(m.chips[0].tone, "write");
  assert.equal(m.model, null);
  assert.equal(m.ctx, null);
});

test("paneHeaderModel narrow without a write chip keeps containment; a 200-char name passes through", () => {
  const long = "n".repeat(200);
  const tab = { kind: "terminal" };
  const m = d.paneHeaderModel({ tab, session: { ...idle, name: long }, nowMs: NOW, boxRuntime: "", narrow: true });
  assert.equal(m.name, long, "truncation is CSS");
  assert.equal(m.chips.length, 1);
  assert.equal(m.chips[0].tone, "host");
  const wide = d.paneHeaderModel({ tab, session: { ...idle, name: long }, nowMs: NOW, boxRuntime: "", narrow: false });
  assert.ok(wide.chips.length <= 2);
  assert.equal(wide.model, null);
  assert.equal(wide.ctx, null);
});

test("paneHeaderModel: viewer tab is its basename with no chips", () => {
  const m = d.paneHeaderModel({ tab: { kind: "viewer", path: "/home/u/proj/README.md", name: "README.md" }, session: null, nowMs: NOW, boxRuntime: "", narrow: false });
  assert.equal(m.name, "README.md");
  assert.deepEqual(m.chips, []);
  assert.equal(m.model, null);
  assert.equal(m.ctx, null);
});

test("needStripModel: null unless the session needs you", () => {
  assert.equal(d.needStripModel(working), null);
  assert.equal(d.needStripModel(null), null);
  const a = d.needStripModel({ activity: "waiting", need: "ask", ask: "rm -rf build" });
  assert.equal(a.label, "asks");
  assert.equal(a.text, "rm -rf build");
  const t = d.needStripModel({ activity: "waiting", need: "turn", claude: true, summary: "done, your move" });
  assert.equal(t.label, "your turn");
  assert.equal(t.text, "done, your move");
});

test("emptyStateModel: first run, none open, and recent profiles that still exist", () => {
  const builtin = [{ name: "claude", builtin: true }];
  assert.equal(d.emptyStateModel({ sessions: [], profiles: builtin, recent: [] }).kind, "first");
  assert.equal(d.emptyStateModel({ sessions: [working], profiles: builtin, recent: [] }).kind, "none");
  // the real /api/profiles marks a built-in with editable:false
  assert.equal(d.emptyStateModel({ sessions: [], profiles: [{ name: "claude", editable: false }], recent: [] }).kind, "first");
  assert.equal(d.emptyStateModel({ sessions: [], profiles: [{ name: "claude", editable: false }, { name: "mine", editable: true }], recent: [] }).kind, null);
  const r = d.emptyStateModel({ sessions: [working], profiles: [{ name: "claude", editable: false }, { name: "hubs-dev", editable: true }], recent: ["gone", "hubs-dev"] });
  assert.deepEqual(r.profiles, ["hubs-dev"]);
  const many = ["a", "b", "c", "d"].map((name) => ({ name, editable: true }));
  assert.deepEqual(d.emptyStateModel({ sessions: [working], profiles: many, recent: ["a", "b", "c", "d"] }).profiles, ["a", "b", "c"]);
  assert.deepEqual(d.emptyStateModel({ sessions: [], profiles: null, recent: null }).profiles, []);
});

test("pushRecentProfile: most recent first, de-duplicated, max three", () => {
  assert.deepEqual(d.pushRecentProfile(["a", "b", "c"], "b"), ["b", "a", "c"]);
  assert.deepEqual(d.pushRecentProfile(["a", "b", "c"], "d"), ["d", "a", "b"]);
  assert.deepEqual(d.pushRecentProfile(null, "a"), ["a"]);
  assert.deepEqual(d.pushRecentProfile(["a"], ""), ["a"]);
});
