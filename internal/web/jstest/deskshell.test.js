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

test("rowSubline: a connection-level SSO expiry reads SSO expired, never a bare Waiting", () => {
  const conn = { name: "dev", sso_expired: "dev-sso" };
  const r = d.rowSubline({ activity: "working", aws_connections: [conn] });
  assert.deepEqual(r, { text: "SSO expired \u00b7 Sign in", amber: true });
  // The legacy single-record need keeps its own named text.
  assert.equal(d.rowSubline({ activity: "working", need: "sso", aws: { sso_expired: "x" } }).text.startsWith("AWS sign-in: "), true);
  // An agent that is itself asking keeps the ask text even with an expired connection.
  assert.ok(d.rowSubline({ activity: "waiting", need: "ask", ask: "ls", aws_connections: [conn] }).text.startsWith("asks: "));
});

test("jumpOrder: Alt+1..9 and the palette follow the sidebar order on desktop, the API order on mobile", () => {
  const { jumpTarget } = require("../static/sessionrow.js");
  const list = [idle, working, waiting];
  assert.equal(jumpTarget(1, d.jumpOrder(list, false)).id, "w", "the needs-you session is 1");
  assert.deepEqual(d.jumpOrder(list, false).map((s) => s.id), ["w", "r", "i"]);
  assert.equal(d.jumpOrder(list, true), list, "mobile keeps the API order");
  assert.deepEqual(d.jumpOrder(null, false), []);
});

test("paneHeaderModel: full slots in order for a wide pane", () => {
  const tab = { kind: "terminal", usage: () => ({ model: "opus-5", pct: 42 }) };
  const s = { ...working, name: "api", aws_connections: [{ name: "prod", label: "prod", account_id: "1", role_arn: "arn:aws:iam::1:role/r", elevated_until: FUTURE }] };
  const m = d.paneHeaderModel({ tab, session: s, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.equal(m.name, "api");
  assert.deepEqual(m.chips.map((c) => c.tone), ["box", "write"]);
  assert.equal(m.chips[1].text, "aws prod \u00b7 WRITE \u00b7 30m");
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

test("stripLabel: an ask reads Asks to run, other needs keep the model's words", () => {
  assert.equal(d.stripLabel("asks"), "Asks to run");
  assert.equal(d.stripLabel("your turn"), "your turn");
  assert.equal(d.stripLabel("AWS sign-in"), "AWS sign-in");
  assert.equal(d.stripLabel(""), "waiting");
});

test("stripKey: stable across tab kinds, moves with the ask, the need and replies", () => {
  const a = { id: "w", activity: "waiting", need: "ask", ask: "ls", ask_full: "ls -la", replyable: true, claude: true };
  // Review Focus 3: a viewer tab becoming active must not repaint (and so not
  // collapse) the strip.
  assert.equal(d.stripKey(a, "terminal", true), d.stripKey(a, "viewer", true));
  assert.notEqual(d.stripKey(a, "terminal", true), d.stripKey({ ...a, ask: "rm x" }, "terminal", true));
  assert.notEqual(d.stripKey(a, "terminal", true), d.stripKey({ ...a, ask_full: "ls -la /" }, "terminal", true));
  assert.notEqual(d.stripKey(a, "terminal", true), d.stripKey({ ...a, need: "turn" }, "terminal", true));
  assert.notEqual(d.stripKey(a, "terminal", true), d.stripKey({ ...a, replyable: false }, "terminal", true));
  assert.notEqual(d.stripKey(a, "terminal", false), d.stripKey(a, "terminal", true));
  assert.equal(d.stripKey(a, "terminal", true), d.stripKey({ ...a }, "terminal", true), "equal input, equal key");
  assert.equal(d.stripKey(null, "terminal", true), "", "no session, no strip");
});

test("emptyStateModel: first run, none open, and recent profiles that still exist", () => {
  const builtin = [{ name: "claude", builtin: true }];
  assert.equal(d.emptyStateModel({ sessions: [], profiles: builtin, recent: [] }).kind, "first");
  assert.equal(d.emptyStateModel({ sessions: [working], profiles: builtin, recent: [] }).kind, "none");
  // the real /api/profiles marks a built-in with editable:false
  assert.equal(d.emptyStateModel({ sessions: [], profiles: [{ name: "claude", editable: false }], recent: [] }).kind, "first");
  assert.equal(d.emptyStateModel({ sessions: [], profiles: [{ name: "claude", editable: false }, { name: "mine", editable: true }], recent: [] }).kind, "none", "custom profiles, no sessions: never a blank pane");
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

const conn = (extra) => ({ name: "dev", label: "dev", account_id: "1", role_arn: "arn:aws:iam::1:role/r", ...extra });

test("write chip carries the connection and the minutes left", () => {
  const exp = new Date(NOW + 23 * 60000).toISOString();
  const m = d.paneHeaderModel({ tab: { kind: "terminal" }, session: { ...working, aws_connections: [conn({ elevated_until: exp })] }, nowMs: NOW, boxRuntime: "runc", narrow: false });
  const c = m.chips.find((x) => x.tone === "write");
  assert.equal(c.text, "aws dev \u00b7 WRITE \u00b7 23m");
  assert.ok(c.text.endsWith("\u00b7 WRITE \u00b7 23m"));
});

test("write chip with several live windows shows the count and the soonest expiry", () => {
  const soon = new Date(NOW + 5 * 60000).toISOString();
  const later = new Date(NOW + 40 * 60000).toISOString();
  const s = { ...working, aws_connections: [conn({ name: "a", elevated_until: later }), conn({ name: "b", elevated_until: soon }), conn({ name: "c" })] };
  const m = d.paneHeaderModel({ tab: { kind: "terminal" }, session: s, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.equal(m.chips.find((x) => x.tone === "write").text, "aws \u00d72 \u00b7 WRITE \u00b7 5m");
});

test("a legacy-only elevated session still gets a timed write chip", () => {
  const exp = new Date(NOW + 12 * 60000).toISOString();
  const m = d.paneHeaderModel({ tab: { kind: "terminal" }, session: { ...working, aws: { elevated_until: exp } }, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.equal(m.chips.find((x) => x.tone === "write").text, "aws \u00b7 WRITE \u00b7 12m");
});

test("a read credential chip is neutral, even for a production connection", () => {
  const s = { ...working, aws_connections: [conn({ prod: true })] };
  const m = d.paneHeaderModel({ tab: { kind: "terminal" }, session: s, nowMs: NOW, boxRuntime: "runc", narrow: false });
  const c = m.chips[1];
  assert.equal(c.tone, "cloud");
  assert.equal(c.text, "dev");
  assert.ok(!m.chips.some((x) => x.tone === "prod" || x.tone === "write"));
});

test("narrow with a read cloud chip keeps containment; with no containment, no chip", () => {
  const s = { ...working, aws_connections: [conn({})] };
  const a = d.paneHeaderModel({ tab: { kind: "terminal" }, session: s, nowMs: NOW, boxRuntime: "runc", narrow: true });
  assert.deepEqual(a.chips.map((c) => c.tone), ["box"]);
  const b = d.paneHeaderModel({ tab: { kind: "terminal" }, session: { ...s, exited: true }, nowMs: NOW, boxRuntime: "runc", narrow: true });
  assert.deepEqual(b.chips, [], "a read cloud chip is never surfaced when narrow");
});

test("a waiting session with no named need has no dangling label", () => {
  const s = { id: "x", name: "x", activity: "waiting", summary: "Waiting for input." };
  assert.deepEqual(d.rowSubline({ activity: "waiting" }), { text: "Waiting for input.", amber: true });
  const strip = d.needStripModel({ activity: "waiting" });
  assert.equal(strip.label, "waiting for input");
  assert.ok(strip.label.length > 0);
  assert.equal(strip.text, "Waiting for input.");
  assert.equal(d.rowSubline(s).text.startsWith(":"), false);
});

test("emptyStateModel says nothing about first run until profiles have loaded", () => {
  assert.equal(d.emptyStateModel({ sessions: [], profiles: [], recent: [] }).kind, null);
  assert.equal(d.emptyStateModel({ sessions: [], profiles: null, recent: [] }).kind, null);
  assert.equal(d.emptyStateModel({ sessions: [], profiles: undefined, recent: [] }).kind, null);
});

test("emptyStateModel with sessions present is \"none\" even while profiles load", () => {
  for (const profiles of [[], null, undefined]) {
    const m = d.emptyStateModel({ sessions: [working], profiles, recent: ["a"] });
    assert.equal(m.kind, "none");
    assert.deepEqual(m.profiles, [], "no profiles known yet, so no recent rows");
  }
});

test("emptyStateModel is silent while the session list is unknown", () => {
  const builtin = [{ name: "claude", editable: false }];
  assert.equal(d.emptyStateModel({ sessions: null, profiles: builtin, recent: [] }).kind, null, "no first-run flash for a user with live sessions");
  assert.equal(d.emptyStateModel({ sessions: undefined, profiles: builtin, recent: [] }).kind, null);
  assert.equal(d.emptyStateModel({ sessions: [], profiles: builtin, recent: [] }).kind, "first", "a known empty list still means first run");
});

test("deskStorage returns null when reading localStorage throws, and the helpers cope", () => {
  const had = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { configurable: true, get() { throw new Error("SecurityError"); } });
  try {
    let st;
    assert.doesNotThrow(() => { st = d.deskStorage(); });
    assert.equal(st, null);
    assert.deepEqual(d.readRecent(st), []);
    assert.doesNotThrow(() => d.writeRecent(st, ["a"]));
  } finally {
    if (had) Object.defineProperty(globalThis, "localStorage", had); else delete globalThis.localStorage;
  }
  assert.equal(d.deskStorage(), null, "absent storage is null too");
});

test("readRecent/writeRecent round-trip in order and never throw", () => {
  const mem = {};
  const ok = { getItem: (k) => (k in mem ? mem[k] : null), setItem: (k, v) => { mem[k] = String(v); } };
  assert.deepEqual(d.readRecent(ok), []);
  d.writeRecent(ok, ["b", "a", "c"]);
  assert.deepEqual(d.readRecent(ok), ["b", "a", "c"]);
  assert.ok("agent-box:recentProfiles" in mem);
  mem["agent-box:recentProfiles"] = "{not json";
  assert.deepEqual(d.readRecent(ok), []);
  mem["agent-box:recentProfiles"] = JSON.stringify({ a: 1 });
  assert.deepEqual(d.readRecent(ok), [], "a non-array is not a list");
  mem["agent-box:recentProfiles"] = JSON.stringify(["a", 3, "", null, "b"]);
  assert.deepEqual(d.readRecent(ok), ["a", "b"], "only non-empty strings survive");
  const bad = { getItem() { throw new Error("denied"); }, setItem() { throw new Error("full"); } };
  assert.deepEqual(d.readRecent(bad), []);
  assert.doesNotThrow(() => d.writeRecent(bad, ["a"]));
  assert.deepEqual(d.readRecent(null), []);
  assert.doesNotThrow(() => d.writeRecent(null, ["a"]));
});

test("costTodayRange is today's UTC date for both ends, the convention analytics.js uses", () => {
  const r = d.costTodayRange(Date.parse("2026-10-27T10:00:00Z"));
  assert.deepEqual(r, { from: "2026-10-27", to: "2026-10-27" });
  // 23:30 UTC on the 27th is already the 28th in UTC+7: UTC is what the
  // Analytics page (anTodayUTC) calls today, so the footer must agree with it.
  assert.deepEqual(d.costTodayRange(Date.parse("2026-10-27T23:30:00Z")), { from: "2026-10-27", to: "2026-10-27" });
  assert.deepEqual(d.costTodayRange(Date.parse("2026-10-28T00:00:00Z")), { from: "2026-10-28", to: "2026-10-28" });
});

test("drawerFor opens a closed drawer on the asked kind and session", () => {
  assert.deepEqual(d.drawerFor({ open: null, sessionId: null }, "files", "a"), { open: "files", sessionId: "a" });
  assert.deepEqual(d.drawerFor(undefined, "access", "a"), { open: "access", sessionId: "a" }, "no state yet = closed");
});

test("drawerFor closes when the same kind is asked for the same session", () => {
  assert.deepEqual(d.drawerFor({ open: "files", sessionId: "a" }, "files", "a"), { open: null, sessionId: "a" });
});

test("drawerFor switches drawers when a different kind is asked", () => {
  assert.deepEqual(d.drawerFor({ open: "files", sessionId: "a" }, "access", "a"), { open: "access", sessionId: "a" });
});

test("drawerFor keeps the drawer open on a different session with the same kind", () => {
  assert.deepEqual(d.drawerFor({ open: "files", sessionId: "a" }, "files", "b"), { open: "files", sessionId: "b" });
});
