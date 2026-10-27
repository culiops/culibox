"use strict";
// accessmodel.js: the Access view's pure model (spec 5.7) and the grant form's gate.
const test = require("node:test");
const assert = require("node:assert");
const { accessModel, grantFormState, ACCESS_EVENTS } = require("../static/accessmodel.js");

const NOW = Date.parse("2026-10-27T15:19:00Z");
const iso = (minFromNow) => new Date(NOW + minFromNow * 60000).toISOString();
const WRITE = "arn:aws:iam::402100009917:role/agent-write";
const BASE = "arn:aws:iam::402100009917:role/agent-investigate";

const conn = (over) => Object.assign({
  name: "culipulse-prod", label: "culipulse-prod", account_id: "402100009917", role_arn: BASE, region: "ap-southeast-1",
  prod: true, write_capable: true, elevate: { role_arn: WRITE, max_minutes: 60, allow_iam: false },
}, over);
const sess = (over) => Object.assign({
  id: "s1", agent: "claude", cwd: "/home/u/culipulse", created: iso(-80), runtime: "box", account: "",
  aws_connections: [conn()],
}, over);
const status = (over) => Object.assign({
  stats: { mints: 12, last_mint: iso(-48) },
  connections: [{ name: "culipulse-prod", stats: { mints: 12, last_mint: iso(-48) } }],
  recent: [], elevate: { host_sessions: [] },
}, over);
const model = (o) => accessModel(Object.assign({ nowMs: NOW, meta: { box_runtime: "sysbox-runc" } }, o));

test("a session with no AWS binding (204): containment only, no rows, no throw", () => {
  const m = model({ session: sess({ aws_connections: undefined }), aws: null });
  assert.deepStrictEqual(m.creds.rows, []);
  assert.strictEqual(m.banner, null);
  assert.deepStrictEqual(m.events, []);
  assert.strictEqual(m.containment.tier, "box");
  assert.strictEqual(m.containment.workspace, "/home/u/culipulse");
  assert.strictEqual(m.creds.head, "");
});

test("nothing at all loaded: still a model, never a throw", () => {
  const m = accessModel({});
  assert.strictEqual(m.containment.tier, "host");
  assert.deepStrictEqual(m.creds.rows, []);
  assert.deepStrictEqual(m.events, []);
  assert.strictEqual(m.banner, null);
});

test("a + Shell session: tier and workspace, a host tier reads as host", () => {
  const m = model({ session: { id: "sh", agent: "bash", cwd: "/w", runtime: "host", parent_id: "s1" }, aws: null });
  assert.strictEqual(m.containment.tier, "host");
  assert.strictEqual(m.containment.workspace, "/w");
  assert.strictEqual(m.containment.runtime, "");
  assert.deepStrictEqual(m.creds.rows, []);
  assert.strictEqual(m.banner, null);
});

test("an unknown runtime is host, never box (fail loud)", () => {
  assert.strictEqual(model({ session: sess({ runtime: "" }) }).containment.tier, "host");
  assert.strictEqual(model({ session: sess({ runtime: "weird" }) }).containment.tier, "host");
});

test("containment: network, account, runtime, started, model, effort, readable paths, clone, drops", () => {
  const egress = { egress: { mode: "allowlist", allowed: 41, denied: 2 } };
  const m = model({
    session: sess({
      account: "work", context_paths: ["/home/u/notes", "/home/u/ref"], clone_of: "/home/u/culipulse-src",
      bundle_drops: [{ kind: "settings", name: "*", reason: "not copied" }],
    }),
    aws: null, egress, usage: { model: "opus-5.5", effort: "high" },
  });
  const c = m.containment;
  assert.deepStrictEqual(c.network, { mode: "allowlist", allowed: 41, denied: 2 });
  assert.strictEqual(c.account, "work");
  assert.strictEqual(c.runtime, "sysbox");
  assert.match(c.started, /1h 20m ago$/);
  assert.strictEqual(c.model, "opus-5.5");
  assert.strictEqual(c.effort, "high");
  assert.deepStrictEqual(c.readable, ["/home/u/notes", "/home/u/ref"]);
  assert.strictEqual(c.clone.of, "/home/u/culipulse-src");
  assert.match(c.dropped.v, /settings/);
});

test("containment: egress comes from the /aws reply when /egress was not asked, an empty account reads default", () => {
  const m = model({ session: sess(), aws: status({ egress: { mode: "open", allowed: 0, denied: 3 } }) });
  assert.deepStrictEqual(m.containment.network, { mode: "open", allowed: 0, denied: 3 });
  assert.strictEqual(m.containment.account, "default");
  assert.deepStrictEqual(model({ session: sess(), aws: null }).containment.network, { mode: "", allowed: 0, denied: 0 });
});

test("containment: optional rows are absent, not empty strings, when the session has none", () => {
  const c = model({ session: sess(), aws: null }).containment;
  assert.deepStrictEqual(c.readable, []);
  assert.strictEqual(c.clone, null);
  assert.strictEqual(c.dropped, null);
  assert.strictEqual(c.model, "");
});

test("read connection: one row, Elevate offered, SSO unknown, last credential time, PROD flag", () => {
  const m = model({ session: sess(), aws: status() });
  assert.strictEqual(m.creds.rows.length, 1);
  const r = m.creds.rows[0];
  assert.strictEqual(r.name, "culipulse-prod");
  assert.strictEqual(r.prod, true);
  assert.strictEqual(r.scope, "read");
  assert.strictEqual(r.sso, "", "nothing says this connection uses SSO, so the row claims nothing");
  assert.deepStrictEqual(r.actions, ["elevate"]);
  assert.match(r.sub, /402100009917/);
  assert.match(r.sub, /agent-investigate/);
  assert.match(r.last, /^\d\d:\d\d$/);
  assert.match(m.creds.head, /^12 mints · last \d\d:\d\d$/);
  assert.strictEqual(m.banner, null);
});

test("a connection without a write role offers no action and no grant panel", () => {
  const r = model({ session: sess({ aws_connections: [conn({ write_capable: false, elevate: undefined })] }), aws: status() }).creds.rows[0];
  assert.deepStrictEqual(r.actions, []);
  assert.strictEqual(r.grant, null);
});

test("elevated session: red banner with minutes left, Extend + Drop near the end of the window", () => {
  const s = sess({ aws_connections: [conn({ elevated_until: iso(12) })] });
  const ap = [{ ts: NOW - 60000, action: "granted", minutes: 30, until: NOW + 12 * 60000, reason: "restart stuck worker", name: "culipulse-prod" }];
  const m = model({ session: s, aws: status({ approvals: ap }) });
  assert.strictEqual(m.banner.conn, "culipulse-prod");
  assert.strictEqual(m.banner.minsLeft, 12);
  assert.match(m.banner.untilText, /^\d\d:\d\d$/);
  assert.strictEqual(m.banner.reason, "restart stuck worker");
  assert.strictEqual(m.banner.newCreds, true, "under 16 min the broker mints no more write credentials");
  const r = m.creds.rows[0];
  assert.strictEqual(r.scope, "write (elevated)");
  assert.deepStrictEqual(r.actions, ["extend", "drop"]);
});

test("elevated with plenty of time left: Drop only (the rail's own Extend rule)", () => {
  const m = model({ session: sess({ aws_connections: [conn({ elevated_until: iso(23) })] }), aws: status() });
  assert.strictEqual(m.banner.minsLeft, 23);
  assert.strictEqual(m.banner.newCreds, false);
  assert.strictEqual(m.banner.reason, "");
  assert.deepStrictEqual(m.creds.rows[0].actions, ["drop"]);
});

test("a lapsed window is not a banner", () => {
  const m = model({ session: sess({ aws_connections: [conn({ elevated_until: iso(-1) })] }), aws: status() });
  assert.strictEqual(m.banner, null);
  assert.strictEqual(m.creds.rows[0].scope, "read");
});

test("the window held by another connection closes Elevate here and says why", () => {
  const a = conn({ name: "a", prod: false, elevated_until: iso(25) });
  const b = conn({ name: "b", prod: false });
  const m = model({ session: sess({ aws_connections: [a, b] }), aws: status({ connections: [] }) });
  const rb = m.creds.rows.find((r) => r.name === "b");
  assert.deepStrictEqual(rb.actions, ["elevate"]);
  assert.strictEqual(rb.grant.blockedText, "Drop a first");
});

test("the legacy inline binding is one row; its grant carries no connection name", () => {
  const s = { id: "s2", runtime: "box", aws: { label: "sandbox", account_id: "226198813800", role_arn: BASE, prod: false,
    elevate: { role_arn: WRITE, max_minutes: 30 } } };
  const r = model({ session: s, aws: { stats: { mints: 1, last_mint: iso(-5) }, recent: [] } }).creds.rows[0];
  assert.strictEqual(r.name, "sandbox");
  assert.strictEqual(r.conn, "");
  assert.deepStrictEqual(r.actions, ["elevate"]);
  assert.deepStrictEqual(r.grant.windows, [30]);
});

test("rows: prod first, then by name", () => {
  const cs = [conn({ name: "z-dev", prod: false }), conn({ name: "a-dev", prod: false }), conn({ name: "m-prod" })];
  assert.deepStrictEqual(model({ session: sess({ aws_connections: cs }), aws: status() }).creds.rows.map((r) => r.name), ["m-prod", "a-dev", "z-dev"]);
});

test("expired SSO: the row says so and the card hangs under the connection a Sign in press targets", () => {
  const cs = [conn({ name: "a", prod: false }), conn({ name: "b", prod: false, sso_expired: "src" })];
  const m = model({ session: sess({ aws_connections: cs }), aws: status({ connections: [] }) });
  assert.strictEqual(m.creds.rows.find((r) => r.name === "b").sso, "expired");
  assert.strictEqual(m.creds.rows.find((r) => r.name === "a").sso, "", "a connection nothing names is not called valid");
  assert.strictEqual(m.creds.ssoFor, "b");
  assert.strictEqual(model({ session: sess(), aws: status() }).creds.ssoFor, "");
});

test("SSO state is never a guess: valid only when a non-expired source names the connection", () => {
  const cs = [conn({ name: "a", prod: false }), conn({ name: "b", prod: false }), conn({ name: "c", prod: false, sso_expired: "src" })];
  const sso = { sources: [
    { names: ["a"], expired: false, login: { state: "waiting", code: "ABCD" } },
    { names: ["c"], expired: true },
  ] };
  const rows = model({ session: sess({ aws_connections: cs }), aws: status({ connections: [], sso }) }).creds.rows;
  const by = (n) => rows.find((r) => r.name === n).sso;
  assert.strictEqual(by("a"), "valid");
  assert.strictEqual(by("b"), "", "static keys / unlisted: unknown");
  assert.strictEqual(by("c"), "expired", "the expired flag wins over any source naming it");
  const none = model({ session: sess({ aws_connections: cs }), aws: null }).creds.rows;
  assert.deepStrictEqual(none.map((r) => [r.name, r.sso]), [["a", ""], ["b", ""], ["c", "expired"]], "before /aws loads nothing is claimed");
  const odd = model({ session: sess({ aws_connections: cs }), aws: status({ sso: { sources: [null, { names: "a" }, { expired: false }] } }) }).creds.rows;
  assert.ok(odd.every((r) => r.sso !== "valid"), "malformed sources claim nothing");
});

test("a running sign-in flow keeps the card even before the flag flips", () => {
  const sso = { sources: [{ names: ["culipulse-prod"], expired: false, login: { state: "waiting", code: "ABCD" } }] };
  assert.strictEqual(model({ session: sess(), aws: status({ sso }) }).creds.ssoFor, "culipulse-prod");
});

test("host sessions listed in the status become the grant panel's warning", () => {
  const st = status({ elevate: { host_sessions: [{ id: "x", name: "zsh-1" }, { id: "y" }] } });
  assert.deepStrictEqual(model({ session: sess(), aws: st }).creds.rows[0].hostWarning, ["zsh-1", "y"]);
});

test("GitHub and static-token rows follow the AWS ones", () => {
  const s = sess({ github: { owners: ["culiops"], repos: 3 }, tokens: [{ name: "sentry", prod: false }] });
  const st = status({ recent: [{ provider: "github", subject: "culiops", outcome: "minted", time: iso(-77), repos: 3 }],
    static_recent: [{ provider: "static", subject: "sentry", outcome: "fetched", time: iso(-9) }] });
  const rows = model({ session: s, aws: st }).creds.rows;
  assert.deepStrictEqual(rows.map((r) => r.name), ["culipulse-prod", "github", "sentry"]);
  const gh = rows[1];
  assert.strictEqual(gh.kind, "github");
  assert.match(gh.sub, /culiops/);
  assert.strictEqual(gh.status, "ok");
  assert.match(gh.last, /^\d\d:\d\d$/);
  assert.deepStrictEqual(gh.actions, []);
  assert.strictEqual(rows[2].kind, "token");
  assert.strictEqual(rows[2].status, "ok");
});

test("a status with no github recent row says nothing about its outcome", () => {
  const rows = model({ session: sess({ github: { owners: ["o"], repos: 1 } }), aws: status() }).creds.rows;
  assert.strictEqual(rows[1].status, "");
  assert.strictEqual(rows[1].last, "");
});

test("events: aws + github + static + approvals merge, newest first, at most 20", () => {
  const recent = [];
  for (let i = 0; i < 15; i++) recent.push({ provider: i % 2 ? "github" : "", subject: i % 2 ? "o/r" : "", entry: "culipulse-prod", outcome: "minted", time: iso(-100 + i), elevated: i === 14 });
  const static_recent = [];
  for (let i = 0; i < 6; i++) static_recent.push({ provider: "static", subject: "sentry", outcome: "fetched", time: iso(-50 + i) });
  const approvals = [];
  for (let i = 0; i < 4; i++) approvals.push({ ts: NOW - (10 - i) * 60000, action: "granted", minutes: 30, name: "culipulse-prod", reason: "r" + i });
  const m = model({ session: sess({ tokens: [{ name: "sentry" }] }), aws: status({ recent, static_recent, approvals }) });
  assert.strictEqual(m.events.length, ACCESS_EVENTS);
  assert.strictEqual(ACCESS_EVENTS, 20);
  const ts = m.events.map((e) => e.ts);
  assert.deepStrictEqual(ts, ts.slice().sort((a, b) => b - a));
  assert.strictEqual(m.events[0].provider, "approval");
  assert.ok(m.events.every((e) => typeof e.ts === "number" && e.provider && "subject" in e && "outcome" in e && "elevated" in e));
});

test("events: aws row names its connection, an inline binding names its role; elevated flag; tone", () => {
  const recent = [
    { provider: "", entry: "culipulse-prod", outcome: "minted", time: iso(-1), elevated: true },
    { provider: "aws", entry: "", role_arn: BASE, outcome: "sso_expired", time: iso(-2) },
  ];
  const ev = model({ session: sess(), aws: status({ recent }) }).events;
  assert.deepStrictEqual([ev[0].provider, ev[0].subject, ev[0].outcome, ev[0].elevated, ev[0].tone], ["aws", "culipulse-prod", "minted", true, "ok"]);
  assert.deepStrictEqual([ev[1].subject, ev[1].outcome, ev[1].tone], ["agent-investigate", "sso expired", "bad"]);
});

test("events: an approval reads as a decision by you, its reason kept as detail", () => {
  const approvals = [{ ts: NOW - 1000, action: "granted", minutes: 30, reason: "restart stuck worker deployment", name: "culipulse-prod" }];
  const e = model({ session: sess(), aws: status({ approvals }) }).events[0];
  assert.strictEqual(e.provider, "approval");
  assert.strictEqual(e.subject, "granted 30 min · culipulse-prod");
  assert.strictEqual(e.outcome, "you (browser)");
  assert.strictEqual(e.detail, "restart stuck worker deployment");
  const x = model({ session: sess(), aws: status({ approvals: [{ ts: NOW, action: "expired", name: "c" }] }) }).events[0];
  assert.strictEqual(x.outcome, "");
  assert.strictEqual(x.subject, "expired · c");
});

test("events: absent approvals are unavailable, not none; the other rows still show", () => {
  const m = model({ session: sess(), aws: status({ recent: [{ provider: "aws", entry: "c", outcome: "minted", time: iso(-1) }] }) });
  assert.strictEqual(m.approvalsKnown, false);
  assert.strictEqual(m.events.length, 1);
  assert.strictEqual(model({ session: sess(), aws: status({ approvals: [] }) }).approvalsKnown, true);
});

test("hostile strings stay plain data: nothing is interpreted or throws", () => {
  const m = model({ session: sess({ aws_connections: [conn({ name: "<img onerror=x>", label: "<b>" })] }),
    aws: status({ recent: [{ provider: "github", subject: "<script>", outcome: "minted", time: "garbage" }], approvals: [{ ts: "x", action: "granted" }] }) });
  assert.strictEqual(m.creds.rows[0].name, "<img onerror=x>");
  assert.ok(Array.isArray(m.events));
});

// --- grantFormState -----------------------------------------------------------

const form = (over) => Object.assign({ minutes: 30, reason: "restart worker", typed: "", prod: false, label: "", busy: false }, over);

test("grantFormState: a valid non-prod form needs no label", () => {
  assert.deepStrictEqual(grantFormState(form()), { valid: true, why: "" });
  assert.strictEqual(grantFormState(form({ typed: "whatever" })).valid, true);
});

test("grantFormState: PROD needs the label typed exactly, case-sensitive, untrimmed", () => {
  const p = (typed) => grantFormState(form({ prod: true, label: "culipulse-prod", typed }));
  assert.strictEqual(p("culipulse-prod").valid, true);
  assert.strictEqual(p("culipulse-pr").valid, false);
  assert.strictEqual(p("Culipulse-prod").valid, false);
  assert.strictEqual(p(" culipulse-prod").valid, false);
  assert.strictEqual(p("culipulse-prod ").valid, false);
  assert.strictEqual(p("").valid, false);
  assert.strictEqual(p("culipulse-pr").why, "label");
});

test("grantFormState: a reason is required and at most 200 characters", () => {
  assert.deepStrictEqual(grantFormState(form({ reason: "" })), { valid: false, why: "reason" });
  assert.strictEqual(grantFormState(form({ reason: "   " })).valid, false);
  assert.strictEqual(grantFormState(form({ reason: "x".repeat(200) })).valid, true);
  assert.deepStrictEqual(grantFormState(form({ reason: "x".repeat(201) })), { valid: false, why: "reason-long" });
  assert.strictEqual(grantFormState(form({ reason: undefined })).valid, false);
});

test("grantFormState: a request in flight blocks, and so does no window", () => {
  assert.deepStrictEqual(grantFormState(form({ busy: true })), { valid: false, why: "busy" });
  assert.strictEqual(grantFormState(form({ minutes: 0 })).valid, false);
  assert.strictEqual(grantFormState(form({ minutes: NaN })).valid, false);
  assert.strictEqual(grantFormState(undefined).valid, false);
});

test("accEventTime: the clock for today, the date and clock for an earlier day", () => {
  const { accEventTime } = require("../static/accessmodel.js");
  const now = new Date(2026, 9, 27, 15, 19).getTime();
  assert.strictEqual(accEventTime(new Date(2026, 9, 27, 14, 31).getTime(), now), "14:31");
  assert.strictEqual(accEventTime(new Date(2026, 9, 26, 9, 5).getTime(), now), "26 Oct 09:05");
  assert.strictEqual(accEventTime(NaN, now), "");
});

test("accesstab.js writes operator and server text with textContent only", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/accesstab.js"), "utf8");
  assert.doesNotMatch(src.replace(/\/\/.*$/gm, ""), /innerHTML|outerHTML|insertAdjacentHTML|document\.write/);
});

// --- the load path is total ---------------------------------------------------

test("accReadReply: 200 is the JSON, 204 is null, anything else or a bad body is undefined, never a throw", async () => {
  const { accReadReply } = require("../static/accessmodel.js");
  const res = (status, json) => ({ status, json: json || (async () => { throw new SyntaxError("Unexpected token"); }) });
  assert.deepStrictEqual(await accReadReply(res(200, async () => ({ a: 1 }))), { a: 1 });
  assert.strictEqual(await accReadReply(res(204)), null);
  assert.strictEqual(await accReadReply(res(500)), undefined);
  assert.strictEqual(await accReadReply(res(200)), undefined, "a 200 whose body is not JSON");
  assert.strictEqual(await accReadReply({ status: 200, json() { throw new Error("sync"); } }), undefined);
});

test("accLoadDone: a failed read ends the read, keeps the old data and marks the load failed", () => {
  const { accLoadDone } = require("../static/accessmodel.js");
  const prev = { aws: { stats: 1 }, egress: null, at: 1, inflight: true, loaded: true, failed: false };
  const bad = accLoadDone(prev, { ok: false }, 99);
  assert.deepStrictEqual(bad, { aws: { stats: 1 }, egress: null, at: 99, inflight: false, loaded: true, failed: true });
  const good = accLoadDone(bad, { ok: true, aws: null, egress: { egress: {} } }, 100);
  assert.deepStrictEqual([good.aws, good.inflight, good.loaded, good.failed], [null, false, true, false]);
  const first = accLoadDone({ inflight: true }, { ok: false }, 5);
  assert.deepStrictEqual([first.aws, first.inflight, first.loaded, first.failed], [null, false, false, true]);
});

// mountAccess is DOM-bound (no pure seam), so this pins the shape of its
// re-target branch: focus in the old session's grant panel is dropped and the
// whole view repaints for the new session, rather than waiting on typing().
test("accesstab.js: re-targeting to another session blurs the grant panel and force-repaints", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/accesstab.js"), "utf8");
  const i = src.indexOf("if (next && next !== sid) {");
  assert.ok(i > 0, "update() has a re-target branch");
  const branch = src.slice(i, src.indexOf("accPrune();", i));
  assert.match(branch, /\.blur\(\)/, "focus leaves the old grant panel");
  assert.match(branch, /paint\(true\)/, "the whole view repaints, past typing()");
  assert.match(branch, /return;/, "the soft paint(false) does not follow");
});
