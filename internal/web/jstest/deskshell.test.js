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
  assert.deepEqual(r, { text: "SSO expired \u00b7 Sign in", amber: true, view: "access" });
  // The legacy single-record need keeps its own named text, and opens Access too.
  const legacy = d.rowSubline({ activity: "working", need: "sso", aws: { sso_expired: "x" } });
  assert.equal(legacy.text.startsWith("AWS sign-in: "), true);
  assert.equal(legacy.view, "access");
  // An agent that is itself asking keeps the ask text even with an expired
  // connection, and its line opens the terminal (no view).
  const ask = d.rowSubline({ activity: "waiting", need: "ask", ask: "ls", aws_connections: [conn] });
  assert.ok(ask.text.startsWith("asks: "));
  assert.equal(ask.view, undefined);
});

test("jumpOrder: Mod+1…9 and the palette follow the sidebar order on desktop, the API order on mobile", () => {
  const { jumpTarget } = require("../static/sessionrow.js");
  const list = [idle, working, waiting];
  assert.equal(jumpTarget(1, d.jumpOrder(list, false)).id, "w", "the needs-you session is 1");
  assert.deepEqual(d.jumpOrder(list, false).map((s) => s.id), ["w", "r", "i"]);
  assert.equal(d.jumpOrder(list, true), list, "mobile keeps the API order");
  assert.deepEqual(d.jumpOrder(null, false), []);
});

test("jumpOrder skips nested shells, so Mod+1…9 and the rail target agents only", () => {
  const shell = { id: "sh", name: "sh", activity: "working", parent_id: "i", created: "2026-10-27T09:00:00Z" };
  const list = [idle, shell, working, waiting];
  assert.deepEqual(d.jumpOrder(list, false).map((s) => s.id), ["w", "r", "i"]);
  assert.deepEqual(d.jumpOrder(list, true).map((s) => s.id), ["i", "r", "w"], "mobile: API order, no shell");
  const orphan = { ...shell, parent_id: "gone" };
  assert.ok(d.jumpOrder([idle, orphan], false).some((s) => s.id === "sh"), "an orphan shell is an ordinary row");
});

test("deskGroups lists a shell right after its parent and does not count it", () => {
  const shell = { id: "sh", name: "sh", activity: "waiting", need: "ask", ask: "x", parent_id: "i", created: "2026-10-27T09:00:00Z" };
  const g = d.deskGroups([shell, idle, working]);
  assert.deepEqual(g.map((x) => x.key), ["running", "idle"], "the waiting shell makes no Needs you group");
  const idleGroup = g.find((x) => x.key === "idle");
  assert.equal(idleGroup.count, 1);
  assert.deepEqual(idleGroup.sessions.map((s) => s.id), ["i", "sh"]);
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

test("stripLabel: an ask reads Asks to run, other needs are sentence case", () => {
  assert.equal(d.stripLabel("asks"), "Asks to run");
  assert.equal(d.stripLabel("your turn"), "Your turn");
  assert.equal(d.stripLabel("store locked"), "Store locked");
  assert.equal(d.stripLabel("AWS sign-in"), "AWS sign-in");
  assert.equal(d.stripLabel(""), "Waiting");
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

test("stripKey moves with reply_ready; stripHoldKey does not (#77)", () => {
  // A strip built in the seconds after a turn ends (reply_ready false) must
  // rebuild once the server is ready, or its composer never opens.
  const a = { id: "w", activity: "waiting", need: "turn", summary: "done", replyable: true, claude: true, reply_ready: false };
  assert.notEqual(d.stripKey(a, "terminal", true), d.stripKey({ ...a, reply_ready: true }, "terminal", true));
  assert.equal(d.stripKey(a, "terminal", true), d.stripKey({ ...a, reply_ready: undefined }, "terminal", true), "absent reads as not ready");
  // A disabled field cannot hold focus, and a flap to false while typing must
  // not tear the field down (the server's 409 covers that send).
  assert.equal(d.stripHoldKey({ ...a, reply_ready: true }, true), d.stripHoldKey(a, true));
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

test("stripHoldKey moves with the session, need, ask and the reply gate, not with incidental fields", () => {
  const a = { id: "s1", need: "turn", ask: "ls", ask_full: "ls -la", summary: "x", aws_connections: null, replyable: true, claude: true };
  assert.equal(d.stripHoldKey(a, true), d.stripHoldKey({ ...a, summary: "y", aws_connections: [{ name: "p" }] }, true));
  assert.notEqual(d.stripHoldKey(a, true), d.stripHoldKey({ ...a, ask_full: "rm -rf x" }, true));
  assert.notEqual(d.stripHoldKey(a, true), d.stripHoldKey({ ...a, need: "ask" }, true));
  assert.notEqual(d.stripHoldKey(a, true), d.stripHoldKey({ ...a, id: "s2" }, true));
  // The gate: revoked replyability and the kill-switch must rebuild a strip being typed in.
  assert.notEqual(d.stripHoldKey(a, true), d.stripHoldKey({ ...a, replyable: false }, true));
  assert.notEqual(d.stripHoldKey(a, true), d.stripHoldKey({ ...a, claude: false }, true));
  assert.notEqual(d.stripHoldKey(a, true), d.stripHoldKey(a, false));
  assert.equal(d.stripHoldKey(a, false), d.stripHoldKey(a, undefined));
  assert.equal(d.stripHoldKey(null, true), "");
});

// --- bootFlags: legacy localStorage keys (spec 7, Task 9) ----------------------

const store = (obj) => ({ getItem: (k) => (k in obj ? obj[k] : null) });
const LEGACY = {
  sidebarHidden: "1",
  "agent-box:steerRail": "1",
  "agent-box:panesCollapsed": '{"sessions":true,"files":true}',
  "agent-box:sidebarSplit": "40",
};

test("bootFlags on desktop never opens Access and ignores the legacy pane keys", () => {
  assert.deepEqual(d.bootFlags(store(LEGACY), false),
    { sidebarHidden: true, openAccess: false, ignoreCollapsed: true, ignoreSplit: true });
});

test("bootFlags on mobile keeps the stored values as they are", () => {
  const f = d.bootFlags(store(LEGACY), true);
  assert.equal(f.openAccess, true);
  assert.equal(f.ignoreCollapsed, false);
  assert.equal(f.ignoreSplit, false);
  assert.equal(f.sidebarHidden, true);
});

test("bootFlags reads off values as off", () => {
  const f = d.bootFlags(store({ sidebarHidden: "0", "agent-box:steerRail": "0" }), true);
  assert.deepEqual(f, { sidebarHidden: false, openAccess: false, ignoreCollapsed: false, ignoreSplit: false });
  assert.deepEqual(d.bootFlags(store({}), false),
    { sidebarHidden: false, openAccess: false, ignoreCollapsed: true, ignoreSplit: true });
});

test("bootFlags with a throwing or missing storage gives the defaults", () => {
  const boom = { getItem() { throw new Error("blocked"); } };
  assert.deepEqual(d.bootFlags(boom, true), { sidebarHidden: false, openAccess: false, ignoreCollapsed: false, ignoreSplit: false });
  assert.deepEqual(d.bootFlags(boom, false), { sidebarHidden: false, openAccess: false, ignoreCollapsed: true, ignoreSplit: true });
  assert.deepEqual(d.bootFlags(null, true), { sidebarHidden: false, openAccess: false, ignoreCollapsed: false, ignoreSplit: false });
});

// --- paneTitle: the pane-header name's tooltip (R22a) ---------------------------

test("paneTitle adds the cwd only for a terminal tab", () => {
  const s = { cwd: "/home/u/proj" };
  assert.equal(d.paneTitle("api", { kind: "terminal" }, s, 1), "api · /home/u/proj");
  assert.equal(d.paneTitle("notes.md", { kind: "viewer" }, s, 1), "notes.md");
  assert.equal(d.paneTitle("api", { kind: "terminal" }, { cwd: "" }, 1), "api");
  assert.equal(d.paneTitle("api", { kind: "terminal" }, null, 1), "api");
  assert.equal(d.paneTitle("api", null, s, 1), "api");
});

test("paneTitle appends the tab count only when the pane has several", () => {
  assert.equal(d.paneTitle("api", { kind: "terminal" }, { cwd: "/x" }, 3), "api · /x (3 tabs in this pane)");
  assert.equal(d.paneTitle("a.md", { kind: "viewer" }, null, 2), "a.md (2 tabs in this pane)");
});

test("fmtSessionCost: sub-cent, zero, two decimals, whole dollars from $100", () => {
  assert.equal(d.fmtSessionCost(0.004), "<$0.01");
  assert.equal(d.fmtSessionCost(0.0099), "<$0.01");
  assert.equal(d.fmtSessionCost(0), "$0.00");
  assert.equal(d.fmtSessionCost(0.01), "$0.01");
  assert.equal(d.fmtSessionCost(3.1), "$3.10");
  assert.equal(d.fmtSessionCost(99.994), "$99.99");
  assert.equal(d.fmtSessionCost(123.4), "$123");
  assert.equal(d.fmtSessionCost(1234.5), "$1235");
  assert.equal(d.fmtSessionCost(undefined), null);
  assert.equal(d.fmtSessionCost(null), null);
  assert.equal(d.fmtSessionCost("3.1"), null);
  assert.equal(d.fmtSessionCost(NaN), null);
});

test("fmtSessionCost: a value that rounds to 100 renders as $100, never $100.00", () => {
  assert.equal(d.fmtSessionCost(99.996), "$100");
  assert.equal(d.fmtSessionCost(99.999), "$100");
  assert.equal(d.fmtSessionCost(100), "$100");
  assert.equal(d.fmtSessionCost(99.99), "$99.99");
});

test("paneHeaderModel: cost is the last slot on a wide pane, null when narrow, in a viewer, or when absent", () => {
  const tab = { kind: "terminal", usage: () => ({ model: "opus-5", pct: 42 }) };
  const s = { ...working, cost_usd: 1.84 };
  assert.equal(d.paneHeaderModel({ tab, session: s, nowMs: NOW, boxRuntime: "runc", narrow: false }).cost, "$1.84");
  assert.equal(d.paneHeaderModel({ tab, session: s, nowMs: NOW, boxRuntime: "runc", narrow: true }).cost, null);
  assert.equal(d.paneHeaderModel({ tab: { kind: "viewer", path: "/a/b.md" }, session: s, nowMs: NOW, boxRuntime: "", narrow: false }).cost, null);
  assert.equal(d.paneHeaderModel({ tab, session: working, nowMs: NOW, boxRuntime: "runc", narrow: false }).cost, null);
  assert.equal(d.paneHeaderModel({ tab, session: { ...working, cost_usd: 0 }, nowMs: NOW, boxRuntime: "runc", narrow: false }).cost, "$0.00");
});

test("headerSyncKey moves with the name and with cost_usd, and not with anything else", () => {
  const k = d.headerSyncKey("api", { ...working, cost_usd: 1.5 });
  assert.notEqual(d.headerSyncKey("api2", { ...working, cost_usd: 1.5 }), k, "rename");
  assert.notEqual(d.headerSyncKey("api", { ...working, cost_usd: 1.6 }), k, "cost moved (idle session, no other signal)");
  assert.notEqual(d.headerSyncKey("api", { ...working }), k, "cost appeared where there was none");
  assert.notEqual(d.headerSyncKey("api", { ...working, cost_usd: 0 }), d.headerSyncKey("api", { ...working }), "0 is shown, absent is not");
  assert.equal(d.headerSyncKey("api", { ...working, cost_usd: 1.5, activity: "idle" }), k, "unrelated field: no repaint");
  assert.equal(d.headerSyncKey("api", { ...working, cost_usd: 1.5004 }), k, "same displayed cents: no repaint per poll");
  assert.equal(d.headerSyncKey("api", null), d.headerSyncKey("api", null));
});
