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

test("deskGroups: sessions with no workspace_root share one Other group, blocked rows first", () => {
  const g = d.deskGroups([idle, working, waiting, sso]);
  assert.equal(g.length, 1);
  assert.equal(g[0].key, "");
  assert.equal(g[0].label, "Other");
  assert.deepEqual(g[0].sessions.map((s) => s.id), ["s", "w", "r", "i"], "both are blocked, so their names decide; an SSO need counts");
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

test("rowChip: box, clone and host carry an icon; write does not", () => {
  const live = { ...working, aws_connections: [{ name: "a", elevated_until: FUTURE }] };
  assert.deepEqual(d.rowChip(live, NOW, "runc"), { text: "write", tone: "write" });
  assert.deepEqual(d.rowChip(working, NOW, "runc"), { text: "box", tone: "box", icon: "box" });
  assert.deepEqual(d.rowChip({ ...working, clone_of: "/r" }, NOW, "runc"), { text: "clone", tone: "box", icon: "clone", title: "Contained: runs in a box on a private copy of the repo." });
  assert.equal(d.rowChip(idle, NOW, "runc").icon, "host");
  assert.equal(d.rowChip({ ...idle, clone_of: "/r" }, NOW, "runc").text, "host", "a clone chip is for a boxed session only");
});

test("deskRowKind: terminal for a shell profile, the Claude mark from the session, else nothing", () => {
  assert.deepEqual(d.deskRowKind({ claude: false }, { bin: "/bin/zsh" }), { kind: "shell", title: "zsh shell" });
  assert.deepEqual(d.deskRowKind({ claude: true }, { bin: "claude" }), { kind: "claude", title: "Claude Code session" });
  assert.deepEqual(d.deskRowKind({ claude: true }, undefined), { kind: "claude", title: "Claude Code session" });
  assert.equal(d.deskRowKind({ claude: false }, undefined), null);
  assert.equal(d.deskRowKind({ claude: false }, { bin: "codex" }), null);
});

test("svgicon: the Lucide glyphs are 24-unit shape lists", () => {
  const icons = require("../static/svgicon.js");
  for (const k of ["SVG_FOLDER_24", "SVG_CHEVRON_DOWN_24", "SVG_CHEVRON_RIGHT_24", "SVG_PLUS_24", "SVG_BOX_24", "SVG_CLONE_24", "SVG_HOST_24"]) {
    const g = icons[k];
    assert.ok(Array.isArray(g) && g.length > 0, k);
    for (const sh of g) assert.ok(Array.isArray(sh) && typeof sh[0] === "string" && typeof sh[1] === "object", k);
  }
  assert.equal(icons.SVG_CLONE_24[0][0], "rect");
  assert.equal(typeof icons.svgIcon24, "function");
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "svgicon.js"), "utf8");
  const notes = src.split("\n").filter((l) => l.includes("lucide.dev"));
  assert.equal(notes.filter((l) => l.includes("ISC")).length >= 1, true);
  assert.equal(src.split("\n").filter((l) => /^\/\/.*lucide\.dev/.test(l) && /ISC/.test(l) && /SVG_FOLDER_24/.test(l)).length, 1, "one ISC comment line covers the new glyphs");
});

test("rowSubline: amber needs-you text, else summary or the activity word", () => {
  const n = d.rowSubline({ activity: "waiting", need: "ask", ask: "aws logs tail" });
  assert.equal(n.amber, true);
  assert.ok(n.text.includes("aws logs tail"));
  assert.ok(n.text.startsWith("Asks to run "));
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
  assert.ok(ask.text.startsWith("Asks to run "));
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

test("deskGroups lists a shell right after its parent and counts it", () => {
  const shell = { id: "sh", name: "sh", activity: "waiting", need: "ask", ask: "x", parent_id: "i", created: "2026-10-27T09:00:00Z" };
  const g = d.deskGroups([shell, idle, working]);
  assert.equal(g.length, 1);
  assert.deepEqual(g[0].sessions.map((s) => s.id), ["r", "i", "sh"], "the shell is listed after i, not ranked on its own");
  assert.equal(g[0].count, 3);
});

test("paneHeaderModel: full slots in order for a wide pane", () => {
  const tab = { kind: "terminal", usage: () => ({ model: "claude-haiku-4-5-20251001", pct: 35 }) };
  const s = { ...working, name: "api", aws_connections: [{ name: "prod", label: "prod", account_id: "1", role_arn: "arn:aws:iam::1:role/r", elevated_until: FUTURE }] };
  const m = d.paneHeaderModel({ tab, session: s, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.equal(m.name, "api");
  assert.deepEqual(m.chips.map((c) => c.tone), ["box", "write"]);
  assert.equal(m.chips[1].text, "aws prod \u00b7 WRITE \u00b7 30m");
  assert.equal(m.model, "haiku-4.5", "the short label, not the raw id (G6)");
  assert.equal(m.modelTitle, "claude-haiku-4-5-20251001", "the raw id stays one hover away");
  assert.equal(m.ctx, "ctx 35%", "the word says what the number is");
  assert.ok(m.dot && m.dot.cls);
});

// modelLabel: the model as the pane header shows it (F13). analytics.js's shortModel
// drops "claude-" and a trailing -YYYYMMDD; a "-N-M" version tail then reads N.M.
// Analytics' own output is unchanged.
test("modelLabel: short model names with a dotted version", () => {
  assert.equal(d.modelLabel("claude-haiku-4-5-20251001"), "haiku-4.5");
  assert.equal(d.modelLabel("opus-5-5"), "opus-5.5");
  assert.equal(d.modelLabel("claude-opus-5-5"), "opus-5.5");
  assert.equal(d.modelLabel("claude-sonnet-4-20250514"), "sonnet-4");
  assert.equal(d.modelLabel("mixed"), "mixed");
  assert.equal(d.modelLabel("gpt-4o"), "gpt-4o");
});

test("modelLabel: nothing to show is null, never Analytics' \"(unknown)\"", () => {
  assert.equal(d.modelLabel(""), null);
  assert.equal(d.modelLabel(null), null);
  assert.equal(d.modelLabel(undefined), null);
});

test("modelLabel: a build tag is not a version (provider-neutral ids)", () => {
  assert.equal(d.modelLabel("gpt-4-0613"), "gpt-4-0613", "a 4-digit build tag is not minor version 613");
  // A dated id ends in -MM-DD: only a -<1 digit>-<1-2 digits> pair is a version, so "-08-06" is not 8.06.
  assert.equal(d.modelLabel("gpt-4o-2024-08-06"), "gpt-4o-2024-08-06");
  assert.equal(d.modelLabel("gpt-4o-mini-2024-07-18"), "gpt-4o-mini-2024-07-18");
  assert.equal(d.modelLabel("claude-opus-4-10"), "opus-4.10", "a two-digit minor is still a version");
  assert.equal(d.modelLabel("claude-opus-4-1-20250805"), "opus-4.1");
});

// metaUnitsOff: the units of the meta that the wrap pushed off its one line. They are clipped, and
// tabs.js makes them visibility:hidden, which takes them out of the tab order and out of what a
// screen reader announces. A unit is off when it starts at or below the line's bottom; a header
// with no layout (a detached pane, display:none) has no line, so nothing is off there.
test("metaUnitsOff: units below the first line are off, units on it are not", () => {
  const box = { top: 100, bottom: 116 };
  assert.deepEqual(d.metaUnitsOff([100, 100, 100], box), [false, false, false], "all on the line");
  assert.deepEqual(d.metaUnitsOff([100, 100, 116], box), [false, false, true], "the model wrapped to the second row");
  assert.deepEqual(d.metaUnitsOff([100, 116, 132], box), [false, true, true], "ctx and model wrapped");
  assert.deepEqual(d.metaUnitsOff([116, 116, 116], box), [true, true, true], "no room at all");
  assert.deepEqual(d.metaUnitsOff([100, 115.6, 116], box), [false, true, true], "sub-pixel: 0.5px of tolerance either way");
  assert.deepEqual(d.metaUnitsOff([100, 115.4, 116], box), [false, false, true]);
});

test("metaUnitsOff: a header with no layout hides nothing, and an empty meta is empty", () => {
  assert.deepEqual(d.metaUnitsOff([0, 0, 0], { top: 0, bottom: 0 }), [false, false, false]);
  assert.deepEqual(d.metaUnitsOff([5], null), [false]);
  assert.deepEqual(d.metaUnitsOff([], { top: 0, bottom: 16 }), []);
});

test("paneHeaderModel: no usage, or a usage with no model, leaves the meta slots empty", () => {
  const s = { ...working, cost_usd: 3.12 };
  const none = d.paneHeaderModel({ tab: { kind: "terminal" }, session: s, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.equal(none.model, null);
  assert.equal(none.modelTitle, null);
  assert.equal(none.ctx, null);
  assert.equal(none.cost, "$3.12");
  const empty = d.paneHeaderModel({ tab: { kind: "terminal", usage: () => ({ model: "", pct: 12 }) }, session: s, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.equal(empty.model, null, "an empty model id is no model, not (unknown)");
  assert.equal(empty.ctx, "ctx 12%");
});

test("paneHeaderModel narrow on an elevated session: exactly one chip, the write chip", () => {
  const tab = { kind: "terminal", usage: () => ({ model: "opus-5", pct: 42 }) };
  const s = { ...working, aws: { elevated_until: FUTURE }, aws_connections: [{ name: "a", label: "a", account_id: "1", role_arn: "arn:aws:iam::1:role/r", elevated_until: FUTURE }] };
  const m = d.paneHeaderModel({ tab, session: s, nowMs: NOW, boxRuntime: "runc", narrow: true });
  assert.equal(m.chips.length, 1);
  assert.equal(m.chips[0].tone, "write");
  assert.equal(m.model, null);
  assert.equal(m.ctx, null);
  assert.equal(m.cost, null);
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
  const builtin = [{ name: "claude", source: "builtin" }];
  assert.equal(d.emptyStateModel({ sessions: [], profiles: builtin, recent: [] }).kind, "first");
  assert.equal(d.emptyStateModel({ sessions: [working], profiles: builtin, recent: [] }).kind, "none");
  // the real /api/profiles marks a built-in with source:"builtin"
  assert.equal(d.emptyStateModel({ sessions: [], profiles: [{ name: "claude", source: "builtin", editable: false }], recent: [] }).kind, "first");
  assert.equal(d.emptyStateModel({ sessions: [], profiles: [{ name: "claude", source: "builtin", editable: false }, { name: "mine", source: "runtime", editable: true }], recent: [] }).kind, "none", "custom profiles, no sessions: never a blank pane");
  const r = d.emptyStateModel({ sessions: [working], profiles: [{ name: "claude", source: "builtin", editable: false }, { name: "hubs-dev", source: "runtime", editable: true }], recent: ["gone", "hubs-dev"] });
  assert.deepEqual(r.profiles, ["hubs-dev"]);
  const many = ["a", "b", "c", "d"].map((name) => ({ name, source: "runtime", editable: true }));
  assert.deepEqual(d.emptyStateModel({ sessions: [working], profiles: many, recent: ["a", "b", "c", "d"] }).profiles, ["a", "b", "c"]);
  assert.deepEqual(d.emptyStateModel({ sessions: [], profiles: null, recent: null }).profiles, []);
});

// #15: the server says where each profile came from (source), and only a
// built-in counts as "nothing set up yet". A profile from config.json is the
// user's own work even though the editor cannot edit it.
test("emptyStateModel: config.json profiles are the user's own", () => {
  const cfg = [{ name: "claude", source: "config", editable: false }];
  assert.equal(d.emptyStateModel({ sessions: [], profiles: cfg, recent: [] }).kind, "none");
});

test("emptyStateModel: every profile built in is first run", () => {
  const builtin = [{ name: "claude", source: "builtin" }, { name: "zsh", source: "builtin" }];
  assert.equal(d.emptyStateModel({ sessions: [], profiles: builtin, recent: [] }).kind, "first");
});

test("emptyStateModel: one built-in and one config profile is not first run", () => {
  const mixed = [{ name: "claude", source: "builtin" }, { name: "ops", source: "config", editable: false }];
  assert.equal(d.emptyStateModel({ sessions: [], profiles: mixed, recent: [] }).kind, "none");
});

test("emptyStateModel: a runtime profile next to a built-in is not first run", () => {
  const mixed = [{ name: "claude", source: "builtin" }, { name: "mine", source: "runtime", editable: true }];
  assert.equal(d.emptyStateModel({ sessions: [], profiles: mixed, recent: [] }).kind, "none");
});

test("emptyStateModel: a profile with no source is not built-in", () => {
  assert.equal(d.emptyStateModel({ sessions: [], profiles: [{ name: "claude", editable: false }], recent: [] }).kind, "none");
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

// G4-D1: the pane header's dot rings in red while a write window is open, on the sidebar row dot's own predicate
// (sessionElevated), so the model says so once and tabs.js only adds the class. The header's rebuild key already moves
// with the window (its chip text is empty exactly when sessionElevated is false), so the ring appears and goes with it.
test("paneHeaderModel.elevated is sessionElevated(session): a live window on a connection or the legacy record, wide or narrow, and never on a lapsed one or a viewer", () => {
  const model = (session, over) => d.paneHeaderModel({ tab: { kind: "terminal" }, session, nowMs: NOW, boxRuntime: "runc", narrow: false, ...over });
  const live = new Date(NOW + 23 * 60000).toISOString();
  const lapsed = new Date(NOW - 60000).toISOString();
  const open = { ...working, aws_connections: [conn({ elevated_until: live })] };
  assert.equal(model(open).elevated, true);
  assert.equal(model(open, { narrow: true }).elevated, true, "a narrow header keeps its dot, and its ring");
  assert.equal(model({ ...working, aws: { elevated_until: live } }).elevated, true, "the legacy single record");
  assert.equal(model(working).elevated, false);
  assert.equal(model({ ...working, aws_connections: [conn({ elevated_until: lapsed })] }).elevated, false, "the list still carries a lapsed window");
  assert.equal(model(null).elevated, false, "no session record");
  assert.equal(d.paneHeaderModel({ tab: { kind: "viewer", path: "/a/b.md" }, session: open, nowMs: NOW, boxRuntime: "", narrow: false }).elevated, false, "a viewer has no dot");
  for (const s of [open, working, { ...working, aws: { elevated_until: live } }]) {
    assert.equal(model(s).elevated, d.sessionElevated(s, NOW), "the same predicate as the sidebar row dot");
  }
});

test("the header's rebuild key moves when the dot's ring does (a window opens or lapses)", () => {
  const live = new Date(NOW + 23 * 60000).toISOString();
  const open = { ...working, aws_connections: [conn({ elevated_until: live })] };
  assert.notEqual(d.headerSyncKey("api", open, NOW), d.headerSyncKey("api", working, NOW), "opened");
  assert.notEqual(d.headerSyncKey("api", open, NOW), d.headerSyncKey("api", open, NOW + 25 * 60000), "lapsed by the clock");
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
  const builtin = [{ name: "claude", source: "builtin", editable: false }];
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

test("headerSyncKey changes when the write chip's minutes change", () => {
  const at = (min) => ({ ...working, aws_connections: [{ name: "culipulse-prod", label: "culipulse-prod", elevated_until: new Date(NOW + min * 60000 + 30000).toISOString() }] });
  const s = at(23);
  const k = d.headerSyncKey("api", s, NOW);
  assert.equal(d.headerSyncKey("api", s, NOW + 20000), k, "same minute: no repaint per poll");
  assert.notEqual(d.headerSyncKey("api", s, NOW + 60000), k, "the chip said 23m and now says 22m");
  assert.notEqual(d.headerSyncKey("api", s, NOW + 22 * 60000 + 10000), k, "<1m");
  assert.notEqual(d.headerSyncKey("api", s, NOW + 25 * 60000), k, "the window lapsed: the chip is gone");
  assert.notEqual(d.headerSyncKey("api", { ...working }, NOW), k, "a window opened where there was none");
  assert.equal(d.headerSyncKey("api", { ...working }, NOW), d.headerSyncKey("api", { ...working }, NOW + 3600000), "no window: time does not matter");
  // The key follows the very chip the header draws.
  const chip = (now) => d.paneHeaderModel({ tab: { kind: "terminal" }, session: s, nowMs: now, boxRuntime: "runc", narrow: false }).chips.find((c) => c.tone === "write");
  assert.equal(chip(NOW).text, "aws culipulse-prod · WRITE · 23m");
  assert.equal(chip(NOW + 60000).text, "aws culipulse-prod · WRITE · 22m");
  // A caller that leaves the clock out gets the real one, never a silently chipless key.
  const live = { ...working, aws_connections: [{ name: "x", elevated_until: new Date(Date.now() + 600000).toISOString() }] };
  assert.notEqual(d.headerSyncKey("api", live), d.headerSyncKey("api", { ...working }));
});

// paneTabs: the pane header's tabs, in order, each with the label its button
// shows and the icon it shows instead of the label in a narrow pane (#13).
test("paneTabs lists the three views of a pane with a session, each with its own icon; Files is the toggle after them (G6)", () => {
  const t = d.paneTabs(true);
  assert.deepEqual(t.map((x) => x.id), ["terminal", "activity", "access"]);
  assert.deepEqual(t.map((x) => x.label), ["Terminal", "Activity", "Access"]);
  assert.ok(t.every((x) => typeof x.icon === "string" && x.icon), "every tab names an icon");
  assert.strictEqual(new Set(t.map((x) => x.icon)).size, t.length, "no two tabs share an icon");
  assert.ok(!t.some((x) => x.id === "files"), "Files is a drawer toggle, never a view tab of a terminal pane");
});

test("paneFilesToggle is the Files button's one definition, the same as the files-only pane's tab", () => {
  const f = d.paneFilesToggle();
  assert.deepEqual([f.id, f.label, f.icon], ["files", "Files", "files"]);
  assert.deepEqual(d.paneTabs(false)[0], f);
});

test("paneTabs for a pane of files has Files alone: no session to describe", () => {
  const t = d.paneTabs(false);
  assert.deepEqual(t.map((x) => x.id), ["files"]);
  assert.deepEqual(t.map((x) => x.label), ["Files"]);
  assert.strictEqual(t[0].icon, "files");
});

// paneTabsClass: the class of the tab group. A group of ONE tab (a pane of files) is
// marked, so the narrow-pane rule that hides four icons leaves it alone (#123).
test("paneTabsClass marks a one-tab group, and only that", () => {
  assert.strictEqual(d.paneTabsClass(d.paneTabs(false)), "ph-tabs one");
  assert.strictEqual(d.paneTabsClass(d.paneTabs(true)), "ph-tabs");
  assert.strictEqual(d.paneTabsClass([]), "ph-tabs");
});

// --- F21-B (#172): a finished turn is "Done · your turn", not amber Needs you ---
// ask, sso, store_locked and a finished turn, as the server sends them (with
// `attention`) and as an older payload without it.
const A = {
  ask: { id: "ask", name: "ask", activity: "waiting", need: "ask", ask: "rm -rf build", claude: true, attention: "block" },
  sso: { id: "sso", name: "sso", activity: "working", need: "sso", aws_connections: [{ name: "dev", sso_expired: "acme" }], attention: "block" },
  store: { id: "store", name: "store", activity: "working", need: "store_locked", attention: "block" },
  turn: { id: "turn", name: "turn", activity: "waiting", need: "turn", claude: true, summary: "**Merged** `fix/ui`; your move", attention: "done" },
  working: { id: "work", name: "work", activity: "working" },
  idle: { id: "idle", name: "idle", activity: "idle" },
};
const noAttn = (s) => { const c = { ...s }; delete c.attention; return c; };
const mixed = [A.ask, A.sso, A.store, A.turn, A.working, A.idle];

test("deskGroups: inside a group a finished turn follows the blocked rows and precedes working", () => {
  const g = d.deskGroups(mixed);
  assert.equal(g.length, 1);
  assert.deepEqual(g[0].sessions.map((s) => s.id), ["ask", "sso", "store", "turn", "work", "idle"]);
  assert.equal(g[0].count, 6);
});

test("deskGroups: the same list without the server's attention field groups the same", () => {
  const g = d.deskGroups(mixed.map(noAttn));
  assert.deepEqual(g.map((x) => [x.key, x.count, x.sessions.map((s) => s.id)]),
    d.deskGroups(mixed).map((x) => [x.key, x.count, x.sessions.map((s) => s.id)]));
});

test("deskGroups: Mod+N order follows the sidebar", () => {
  assert.deepEqual(d.jumpOrder(mixed, false).map((s) => s.id), ["ask", "sso", "store", "turn", "work", "idle"]);
});

test("deskGroups: an exited session with a stale sign-in need is at rest, not waiting", () => {
  const dead = { id: "dead", name: "dead", exited: true, activity: "done", need: "sso", aws_connections: [{ name: "dev", sso_expired: "acme" }] };
  const g = d.deskGroups([dead]);
  assert.equal(g[0].waiting, 0);
});

test("a nested shell rides with its parent, after it", () => {
  const sh = { id: "sh", name: "sh", activity: "working", parent_id: "turn", created: "2026-10-27T09:00:00Z" };
  const g = d.deskGroups([A.turn, sh]);
  assert.deepEqual(g[0].sessions.map((s) => s.id), ["turn", "sh"]);
  assert.equal(g[0].count, 2);
});

// --- #340 S5: the sidebar groups by workspace ---
const ws = (id, root, extra) => ({ id, name: id, activity: "idle", workspace_root: root, created: "2026-11-20T08:00:00Z", ...extra });
const idsOf = (list) => list.map((s) => s.id);

test("deskWsGroups: one group per workspace_root, alphabetical, Other last", () => {
  const g = d.deskWsGroups([ws("z1", "/r/zeta"), ws("a1", "/r/alpha"), ws("o1", ""), ws("a2", "/r/alpha", { activity: "working" })]);
  assert.deepEqual(g.map((x) => x.key), ["/r/alpha", "/r/zeta", ""]);
  assert.deepEqual(g.map((x) => x.label), ["alpha", "zeta", "Other"]);
  assert.deepEqual(g.map((x) => x.count), [2, 1, 1]);
  assert.deepEqual(idsOf(g[0].sessions), ["a2", "a1"]);
  assert.ok(g.every((x) => x.path === x.key));
});

test("deskWsGroups: a group holding a waiting row comes first, Other last within its tier", () => {
  const g = d.deskWsGroups([ws("c", "/r/c"), ws("b", "/r/b", { activity: "waiting" }), ws("o", "", { activity: "waiting" }), ws("a", "/r/a", { attention: "done", need: "turn" })]);
  assert.deepEqual(g.map((x) => x.key), ["/r/b", "", "/r/a", "/r/c"]);
  assert.deepEqual(g.map((x) => x.waiting), [1, 1, 0, 0], "a finished turn does not lift its group");
});

test("deskWsGroups: inside a group, block, done, working, the rest, then name", () => {
  const g = d.deskWsGroups([ws("b", "/r"), ws("c", "/r", { activity: "working" }), ws("d", "/r", { need: "turn", attention: "done" }), ws("e", "/r", { activity: "waiting" }), ws("a", "/r")]);
  assert.deepEqual(idsOf(g[0].sessions), ["e", "d", "c", "a", "b"]);
});

test("deskWsGroups: a nested shell rides with its parent and counts; an orphan shell groups by its own root", () => {
  const p = ws("p", "/r/x");
  const sh = { ...ws("sh", "/r/y"), parent_id: "p", created: "2026-11-20T09:00:00Z" };
  const g = d.deskWsGroups([p, sh]);
  assert.equal(g.length, 1);
  assert.equal(g[0].key, "/r/x");
  assert.deepEqual(idsOf(g[0].sessions), ["p", "sh"]);
  assert.equal(g[0].count, 2);
  assert.equal(g[0].dots.length, 2);
  const o = d.deskWsGroups([p, { ...sh, parent_id: "gone" }]);
  assert.deepEqual(o.map((x) => x.key), ["/r/x", "/r/y"]);
});

test("deskWsLabels: a shared basename reads name · parent on every colliding root", () => {
  const roots = ["/a/x/app", "/b/y/app", "/c/tools", ""];
  const m = d.deskWsLabels(roots);
  assert.deepEqual(roots.map((r) => m.get(r)), ["app · x", "app · y", "tools", "Other"]);
  const g = d.deskWsGroups(roots.map((r, i) => ws("s" + i, r)));
  assert.deepEqual(g.map((x) => x.label), ["app · x", "app · y", "tools", "Other"]);
  assert.equal(d.deskWsLabels(["/a/app", "/a/app"]).get("/a/app"), "app", "the same root twice is no collision");
});

test("deskWsGroups: a collapsed group keeps its waiting count and dots, and sorts first", () => {
  const list = [ws("w", "/r/m", { activity: "waiting" }), ws("k", "/r/m", { activity: "working" }), ws("i", "/r/a")];
  const g = d.deskWsGroups(list, new Set(["/r/m"]));
  assert.equal(g[0].key, "/r/m");
  assert.equal(g[0].collapsed, true);
  assert.equal(g[0].waiting, 1);
  assert.deepEqual(g[0].dots, ["dot waiting", "dot working"]);
  assert.deepEqual(idsOf(g[0].sessions), ["w", "k"]);
  assert.equal(g[1].key, "/r/a");
  assert.equal(g[1].collapsed, false);
  const other = d.deskWsGroups([ws("o", "")], new Set([""]));
  assert.equal(other[0].collapsed, true, "Other is a valid key");
});

test("jumpOrder skips collapsed groups only when given the set", () => {
  const list = [ws("w", "/r/m", { activity: "waiting" }), ws("k", "/r/m", { activity: "working" }), ws("i", "/r/a")];
  assert.deepEqual(idsOf(d.jumpOrder(list, false)), ["w", "k", "i"]);
  assert.deepEqual(idsOf(d.jumpOrder(list, false, new Set(["/r/m"]))), ["i"]);
  assert.equal(d.jumpOrder(list, true, new Set(["/r/m"])), list);
  assert.equal(d.nextNeedsYou(list, null).id, "w", "the waiting session in a collapsed group is still reached");
});

test("jumpOrder: a waiting nested shell lifts its group here as it does in the sidebar, and is not itself a number", () => {
  const list = [ws("a", "/r/a"), ws("b", "/r/b"), { ...ws("sh", "/r/b", { activity: "waiting" }), parent_id: "b", created: "2026-11-20T09:00:00Z" }];
  const side = d.deskGroups(list).map((g) => [g.key, g.waiting, idsOf(g.sessions)]);
  assert.deepEqual(side, [["/r/b", 1, ["b", "sh"]], ["/r/a", 0, ["a"]]]);
  assert.deepEqual(idsOf(d.jumpOrder(list, false)), ["b", "a"], "the first row on screen is Mod+1");
  assert.deepEqual(idsOf(d.jumpOrder(list, false, new Set(["/r/b"]))), ["a"]);
  assert.deepEqual(idsOf(d.jumpOrder(list, true)), ["a", "b"], "mobile: API order, no shell");
});

test("deskWsHeaderSig moves with collapse and waiting, and with the dots only while collapsed", () => {
  const it = { key: "/r", label: "r", path: "/r", count: 2, waiting: 0, dots: ["dot idle", "dot idle"], collapsed: false };
  const sig = d.deskWsHeaderSig(it);
  assert.equal(d.deskWsHeaderSig({ ...it, dots: ["dot working", "dot idle"] }), sig);
  assert.notEqual(d.deskWsHeaderSig({ ...it, waiting: 1 }), sig);
  assert.notEqual(d.deskWsHeaderSig({ ...it, collapsed: true }), sig);
  const col = { ...it, collapsed: true };
  assert.notEqual(d.deskWsHeaderSig({ ...col, dots: ["dot working", "dot idle"] }), d.deskWsHeaderSig(col));
});

test("wsCollapsed: toggle, write, read round-trip; bad stores read empty; the cap keeps the newest", () => {
  const fake = () => { const m = new Map(); return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)) }; };
  const set = new Set();
  assert.equal(d.wsCollapsedToggle(set, "/r/a"), true);
  assert.equal(d.wsCollapsedToggle(set, "/r/a"), false);
  assert.equal(d.wsCollapsedToggle(set, ""), true);
  const st = fake();
  d.wsCollapsedWrite(st, set);
  assert.deepEqual(JSON.parse(st.getItem(d.DESK_WS_COLLAPSED_KEY)), { v: 1, keys: [""] });
  assert.deepEqual([...d.wsCollapsedRead(st)], [""]);
  const reads = (raw) => [...d.wsCollapsedRead({ getItem: () => raw })];
  assert.deepEqual(reads('{"v":2,"keys":["/x"]}'), []);
  assert.deepEqual(reads("not json"), []);
  assert.deepEqual(reads('{"v":1,"keys":[1,"/y"]}'), ["/y"]);
  assert.deepEqual(reads('{"v":1,"keys":"/y"}'), []);
  assert.deepEqual(reads(null), []);
  const boom = { getItem: () => { throw new Error("blocked"); }, setItem: () => { throw new Error("full"); } };
  assert.equal(d.wsCollapsedRead(boom).size, 0);
  assert.doesNotThrow(() => d.wsCollapsedWrite(boom, set));
  assert.equal(d.wsCollapsedRead(null).size, 0);
  assert.doesNotThrow(() => d.wsCollapsedWrite(null, set));
  const big = new Set();
  for (let i = 0; i <= d.DESK_WS_COLLAPSED_CAP; i++) d.wsCollapsedToggle(big, "/k" + i);
  assert.equal(big.size, 500);
  assert.equal(big.has("/k0"), false);
  assert.equal(big.has("/k500"), true);
  const stored = fake();
  stored.setItem(d.DESK_WS_COLLAPSED_KEY, JSON.stringify({ v: 1, keys: Array.from({ length: 600 }, (_, i) => "/p" + i) }));
  const back = d.wsCollapsedRead(stored);
  assert.equal(back.size, 500);
  assert.equal(back.has("/p99"), false);
  assert.equal(back.has("/p599"), true);
});

test("rowSubline: a finished turn reads its summary, muted, no amber and no 'your turn:' prefix", () => {
  assert.deepEqual(d.rowSubline(A.turn), { text: "Merged fix/ui; your move", amber: false });
  assert.deepEqual(d.rowSubline(noAttn(A.turn)), { text: "Merged fix/ui; your move", amber: false });
  assert.deepEqual(d.rowSubline({ ...A.turn, summary: "" }), { text: "your turn", amber: false });
  assert.deepEqual(d.rowSubline({ ...A.turn, summary: undefined }), { text: "your turn", amber: false });
});

test("rowSubline: a blocked session is still amber", () => {
  assert.equal(d.rowSubline(A.ask).amber, true);
  assert.ok(d.rowSubline(A.ask).text.startsWith("Asks to run "));
  assert.equal(d.rowSubline(A.sso).amber, true);
  assert.equal(d.rowSubline(A.store).amber, true);
});

test("rowSubline: an exited session's stale sign-in need is not an amber line", () => {
  const dead = { id: "dead", exited: true, activity: "done", need: "sso", aws_connections: [{ name: "dev", sso_expired: "acme" }] };
  assert.equal(d.rowSubline(dead).amber, false);
  assert.equal(d.rowSubline(dead).text, "ended", "the row word for a dead session (M8.6c, was \"exited\"), not 'AWS sign-in needed'");
});

test("deskDot: a finished turn is the neutral idle dot titled 'your turn'; blocked stays the amber waiting dot", () => {
  const idleDot = require("../static/activity.js").activityClass("idle");
  assert.deepEqual(d.deskDot(A.turn), { cls: idleDot.cls, title: "your turn" });
  assert.deepEqual(d.deskDot(noAttn(A.turn)), { cls: idleDot.cls, title: "your turn" });
  assert.equal(d.deskDot(A.turn).cls.includes("waiting"), false);
  assert.equal(d.deskDot(A.ask).cls, "dot waiting");
  assert.equal(d.deskDot(A.sso).cls, "dot waiting");
  assert.equal(d.deskDot(A.sso).title, "AWS sign-in needed");
  assert.equal(d.deskDot(A.store).title, "Store locked");
  assert.equal(d.deskDot(A.working).cls, "dot working");
  assert.equal(d.deskDot(A.idle).cls, "dot idle");
});

test("deskDot: an exited session with a stale sign-in need is not an amber dot", () => {
  const dead = { id: "dead", exited: true, activity: "done", need: "sso", aws_connections: [{ name: "dev", sso_expired: "acme" }] };
  assert.equal(d.deskDot(dead).cls, "dot done");
});

test("needStripModel: a finished turn's strip is tone done, a blocked one is tone block; the label stays", () => {
  const t = d.needStripModel(A.turn);
  assert.equal(t.tone, "done");
  assert.equal(t.label, "your turn");
  assert.equal(d.stripLabel(t.label), "Your turn");
  assert.equal(d.needStripModel(A.ask).tone, "block");
  assert.equal(d.needStripModel(A.sso).tone, "block");
  assert.equal(d.needStripModel(A.store).tone, "block");
  assert.equal(d.needStripModel(A.working), null);
  assert.equal(d.needStripModel({ id: "dead", exited: true, activity: "done", need: "sso" }), null, "an exited session has no strip");
});

test("stripKey: the same session as blocked and as done is not the same strip", () => {
  const base = { id: "w", activity: "waiting", need: "turn", summary: "x", replyable: true, claude: true };
  assert.notEqual(d.stripKey({ ...base, attention: "block" }, "terminal", true), d.stripKey({ ...base, attention: "done" }, "terminal", true));
  assert.equal(d.stripKey({ ...base, attention: "done" }, "terminal", true), d.stripKey({ ...base, attention: "done" }, "viewer", true), "tab kind still does not matter");
});

test("stripHoldKey does not move with attention alone (a typed reply survives)", () => {
  const base = { id: "w", activity: "waiting", need: "turn", replyable: true, claude: true };
  assert.equal(d.stripHoldKey({ ...base, attention: "block" }, true), d.stripHoldKey({ ...base, attention: "done" }, true));
});

test("paneHeaderModel: a finished turn's dot is the neutral one; a blocked one stays amber", () => {
  const tab = { kind: "terminal" };
  const m = d.paneHeaderModel({ tab, session: A.turn, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.deepEqual(m.dot, d.deskDot(A.turn));
  const b = d.paneHeaderModel({ tab, session: A.ask, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.equal(b.dot.cls, "dot waiting");
});

// The header's dot: the session record (deskDot, through paneHeaderModel) wins
// over the terminal tab's own pushed activity whenever the record exists; the
// tab's activity is only the fallback for a pane whose session is not in the list.
test("headerDot: the session record wins over the tab's pushed activity", () => {
  const pushedWaiting = { kind: "terminal", activity: () => "waiting", needWhy: () => "" };
  const header = (session, tab = pushedWaiting) => d.headerDot(d.paneHeaderModel({ tab, session, nowMs: NOW, boxRuntime: "runc", narrow: false }), tab);
  assert.equal(header(A.turn).cls.includes("waiting"), false, "a finished turn is neutral even though the tab says waiting");
  assert.equal(header(A.turn).title, "your turn");
  assert.equal(header(A.ask).cls, "dot waiting");
  assert.equal(header(A.sso).title, "AWS sign-in needed");
  const dead = { id: "dead", name: "dead", exited: true, activity: "done", need: "sso", aws_connections: [{ name: "dev", sso_expired: "acme" }] };
  assert.equal(header(dead).cls, "dot done", "an exited session with a stale sign-in need is not amber, whatever the tab pushed");
  assert.equal(header(dead).cls.includes("waiting"), false);
  assert.equal(header(A.working, { kind: "terminal", activity: () => "waiting", needWhy: () => "" }).cls, "dot working");
});

test("headerDot: with no session record the terminal's pushed activity is the dot; a viewer has none", () => {
  const tab = { kind: "terminal", activity: () => "waiting", needWhy: () => "sso" };
  const m = d.paneHeaderModel({ tab, session: null, nowMs: NOW, boxRuntime: "runc", narrow: false });
  assert.deepEqual(d.headerDot(m, tab), { cls: "dot waiting", title: "AWS sign-in needed" });
  const noPush = { kind: "terminal" };
  assert.equal(d.headerDot(d.paneHeaderModel({ tab: noPush, session: null, nowMs: NOW, boxRuntime: "", narrow: false }), noPush), null);
  const viewer = { kind: "viewer", path: "/a/b.md", name: "b.md" };
  assert.equal(d.headerDot(d.paneHeaderModel({ tab: viewer, session: null, nowMs: NOW, boxRuntime: "", narrow: false }), viewer), null);
});

test("headerSyncKey moves when a session goes from blocked to done (the header's dot changes)", () => {
  assert.notEqual(d.headerSyncKey("api", { ...A.turn, attention: "block" }), d.headerSyncKey("api", A.turn));
  assert.equal(d.headerSyncKey("api", { ...A.turn, attention: "done" }), d.headerSyncKey("api", A.turn));
});

// --- F55 (#172): detached and taken over read the same on the row and on the dot ---
const DETACHED = "detached \u00b7 reconnecting";

test("rowSubline: a detached session reads 'detached · reconnecting', ahead of its summary", () => {
  assert.deepEqual(d.rowSubline({ id: "x", activity: "detached" }), { text: DETACHED, amber: false });
  assert.deepEqual(d.rowSubline({ id: "x", activity: "detached", summary: "fixing tests" }), { text: DETACHED, amber: false });
});

test("rowSubline: a session taken over by another tab reads 'open in another tab'", () => {
  assert.deepEqual(d.rowSubline(A.working, { takenOver: true }), { text: "open in another tab", amber: false });
  assert.deepEqual(d.rowSubline({ ...A.working, summary: "fixing tests" }, { takenOver: true }), { text: "open in another tab", amber: false });
  assert.deepEqual(d.rowSubline(A.turn, { takenOver: true }), { text: "open in another tab", amber: false });
  // No opts, or takenOver false: nothing changes.
  assert.deepEqual(d.rowSubline({ ...A.working, summary: "fixing tests" }), { text: "fixing tests", amber: false });
  assert.deepEqual(d.rowSubline({ ...A.working, summary: "fixing tests" }, { takenOver: false }), { text: "fixing tests", amber: false });
  assert.deepEqual(d.rowSubline(A.turn, {}), d.rowSubline(A.turn));
});

test("rowSubline: a blocked session keeps its amber ask whatever the connection says", () => {
  assert.equal(d.rowSubline(A.ask, { takenOver: true }).amber, true);
  assert.ok(d.rowSubline(A.ask, { takenOver: true }).text.startsWith("Asks to run "));
  assert.equal(d.rowSubline({ ...A.ask, activity: "detached" }).amber, true);
});

test("deskDot: the tooltip says detached / open in another tab; the dot's colour class is the one it had", () => {
  const det = d.deskDot({ id: "x", activity: "detached" });
  assert.deepEqual(det, { cls: "dot detached", title: DETACHED });
  const away = d.deskDot(A.working, { takenOver: true });
  assert.deepEqual(away, { cls: "dot working", title: "open in another tab" });
  const awayTurn = d.deskDot(A.turn, { takenOver: true });
  assert.equal(awayTurn.title, "open in another tab");
  assert.equal(awayTurn.cls.includes("waiting"), false, "a finished turn is still not amber");
  // A blocked session keeps its blocked wording (amber means blocked on you).
  assert.deepEqual(d.deskDot(A.ask, { takenOver: true }), { cls: "dot waiting", title: "waiting for input" });
  // No opts: the old answers.
  assert.equal(d.deskDot(A.working).title, "working");
  assert.equal(d.deskDot(A.idle).title, "idle");
});

// --- F69 (#172): Next that needs you ---
const ids = (s) => (s ? s.id : s);

test("nextNeedsYou: blocked sessions first, then done, each in sidebar order; the next after the current one", () => {
  // Sidebar order (the list's order within a group): needs you (ask, sso, store), done (turn), running, idle.
  const list = [A.idle, A.turn, A.working, A.ask, A.sso, A.store];
  assert.deepEqual(d.jumpOrder(list, false).map((s) => s.id), ["ask", "sso", "store", "turn", "work", "idle"]);
  assert.equal(ids(d.nextNeedsYou(list, null)), "ask", "no current session: the first one that needs you");
  assert.equal(ids(d.nextNeedsYou(list, "ask")), "sso");
  assert.equal(ids(d.nextNeedsYou(list, "sso")), "store");
  assert.equal(ids(d.nextNeedsYou(list, "store")), "turn", "blocked ones first, then done");
  assert.equal(ids(d.nextNeedsYou(list, "turn")), "ask", "wraps");
});

test("nextNeedsYou: a current session that does not need you starts from the first that does", () => {
  const list = [A.ask, A.turn, A.working, A.idle];
  assert.equal(ids(d.nextNeedsYou(list, "work")), "ask");
  assert.equal(ids(d.nextNeedsYou(list, "idle")), "ask");
  assert.equal(ids(d.nextNeedsYou(list, "gone")), "ask");
});

test("nextNeedsYou: the current session when it is the only one, null when none", () => {
  assert.equal(ids(d.nextNeedsYou([A.ask, A.working, A.idle], "ask")), "ask");
  assert.equal(ids(d.nextNeedsYou([A.turn, A.working], "turn")), "turn");
  assert.equal(ids(d.nextNeedsYou([A.ask, A.working], "work")), "ask");
  assert.equal(d.nextNeedsYou([A.working, A.idle], "work"), null);
  assert.equal(d.nextNeedsYou([], "x"), null);
  assert.equal(d.nextNeedsYou(null, null), null);
});

test("nextNeedsYou: an exited session never asks, and a nested shell is not a target", () => {
  const dead = { id: "dead", name: "dead", exited: true, activity: "done", need: "sso", aws_connections: [{ name: "dev", sso_expired: "acme" }] };
  assert.equal(d.nextNeedsYou([dead, A.working], null), null);
  const sh = { id: "sh", name: "sh", activity: "waiting", parent_id: "work", created: "2026-10-27T09:00:00Z" };
  assert.equal(d.nextNeedsYou([A.working, sh], null), null, "a shell waiting under its parent is not an agent that needs you");
  assert.equal(ids(d.nextNeedsYou([A.working, sh, A.turn], null)), "turn");
});

test("nextNeedsYou reads the same list without the server's attention field", () => {
  const list = [A.idle, A.turn, A.working, A.ask, A.sso, A.store];
  for (const cur of [null, "ask", "sso", "store", "turn"]) {
    assert.equal(ids(d.nextNeedsYou(list.map(noAttn), cur)), ids(d.nextNeedsYou(list, cur)), String(cur));
  }
});

// --- the Bash ask: the command alone after "Asks to run" (frame m1-1, task 9b) ---------------------------------------
test("bashCommand: Bash(<cmd>) gives <cmd>, anything else gives null", () => {
  assert.equal(d.bashCommand("Bash(git push origin main)"), "git push origin main");
  assert.equal(d.bashCommand("Bash(terraform plan -out=tf.plan -var-file=prod.tfvars)"), "terraform plan -out=tf.plan -var-file=prod.tfvars");
  // Not a Bash call: the Tool(args) text stays as the server wrote it.
  assert.equal(d.bashCommand("Read(/etc/hosts)"), null);
  assert.equal(d.bashCommand("WebFetch"), null);
  assert.equal(d.bashCommand("Bash"), null, "the tool name alone has no command");
  assert.equal(d.bashCommand("bash(ls)"), null, "the tool name is exact");
  assert.equal(d.bashCommand("MyBash(ls)"), null, "and not a suffix of another tool's name");
  assert.equal(d.bashCommand(""), null);
  assert.equal(d.bashCommand(undefined), null);
  assert.equal(d.bashCommand(null), null);
});

test("bashCommand: only the outer parenthesis goes, nested ones stay", () => {
  assert.equal(d.bashCommand("Bash(echo $(date))"), "echo $(date)");
  assert.equal(d.bashCommand("Bash(f() { g(1); })"), "f() { g(1); }");
  assert.equal(d.bashCommand("Bash((cd a && make))"), "(cd a && make)");
  assert.equal(d.bashCommand("Bash(echo ))"), "echo )", "a command's own closing paren is not the outer one");
});

test("bashCommand: an empty command has nothing to show; a cut-off one keeps its words and its ellipsis", () => {
  assert.equal(d.bashCommand("Bash()"), null);
  assert.equal(d.bashCommand("Bash(   )"), null);
  // The server cuts a long call at askMax and ends it with an ellipsis, so the closing paren is gone.
  assert.equal(d.bashCommand("Bash(find / -name '*.log' -exec rm {} \\;…"), "find / -name '*.log' -exec rm {} \\;…");
  assert.equal(d.bashCommand("Bash(echo $(date"), "echo $(date", "no closing paren at all: nothing is taken off the end");
});

test("rowSubline: a Bash ask reads Asks to run + the command alone, the command is the mono part", () => {
  const r = d.rowSubline({ activity: "waiting", need: "ask", ask: "Bash(terraform plan -out=tf.plan)" });
  assert.deepEqual(r, { text: "Asks to run terraform plan -out=tf.plan", amber: true, lead: "Asks to run ", mono: "terraform plan -out=tf.plan" });
  // A cut-off call keeps its ellipsis.
  assert.equal(d.rowSubline({ activity: "waiting", need: "ask", ask: "Bash(sleep 1; echo a…" }).mono, "sleep 1; echo a…");
});

test("rowSubline: another tool reads Asks to run Tool(args), also mono; an empty Bash() stays as written", () => {
  assert.deepEqual(d.rowSubline({ activity: "waiting", need: "ask", ask: "Read(/etc/hosts)" }),
    { text: "Asks to run Read(/etc/hosts)", amber: true, lead: "Asks to run ", mono: "Read(/etc/hosts)" });
  assert.equal(d.rowSubline({ activity: "waiting", need: "ask", ask: "Bash()" }).mono, "Bash()");
  assert.equal(d.rowSubline({ activity: "waiting", need: "ask", ask: "WebFetch" }).text, "Asks to run WebFetch");
});

test("rowSubline: an ask with no tool call keeps the previous row text, 'asks: <text>', no claim that anything runs", () => {
  const r = d.rowSubline({ activity: "waiting", need: "ask", summary: "Needs a go-ahead **now**" });
  assert.deepEqual(r, { text: "asks: Needs a go-ahead now", amber: true });
  // Nothing to say at all: the waiting line, as before.
  assert.deepEqual(d.rowSubline({ activity: "waiting", need: "ask" }), { text: "asks: Waiting on a prompt in the terminal.", amber: true });
  assert.deepEqual(d.rowSubline({ activity: "waiting", need: "ask", ask: "" }), { text: "asks: Waiting on a prompt in the terminal.", amber: true });
  // A tool call (even a bare tool name) is what "Asks to run" is for.
  assert.equal(d.rowSubline({ activity: "waiting", need: "ask", ask: "WebFetch" }).lead, "Asks to run ");
});

test("rowSubline: the other needs and every non-ask line keep their shape", () => {
  const s = d.rowSubline(A.sso);
  assert.ok(s.text.startsWith("AWS sign-in: "));
  assert.equal(s.mono, undefined);
  assert.equal(d.rowSubline(A.store).mono, undefined);
  assert.deepEqual(Object.keys(d.rowSubline({ activity: "working", summary: "fixing tests" })), ["text", "amber"]);
});

test("rowSubline: an ended session reads 'ended', a crashed one 'crashed' (the pane bars' vocabulary, M8.6c)", () => {
  assert.deepEqual(d.rowSubline({ activity: "done", exited: true, summary: "all fixed" }), { text: "ended", amber: false });
  assert.deepEqual(d.rowSubline({ activity: "crashed", exited: true, summary: "x" }), { text: "crashed", amber: false });
  assert.deepEqual(d.rowSubline({ activity: "crashed" }), { text: "crashed", amber: false });
  assert.equal(d.deskDot({ activity: "crashed", exited: true }).title, "crashed");
  // a live session is untouched: summary or the activity word
  assert.deepEqual(d.rowSubline({ activity: "working", summary: "fixing" }), { text: "fixing", amber: false });
  assert.deepEqual(d.rowSubline({ activity: "done", summary: "all fixed" }), { text: "all fixed", amber: false });
});
