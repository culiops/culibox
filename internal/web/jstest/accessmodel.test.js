"use strict";
// accessmodel.js: the Access view's pure model (spec 5.7) and the grant form's gate.
const test = require("node:test");
const assert = require("node:assert");
const { accessModel, grantFormState, accTone, accEventTime, elevBanner, ACCESS_EVENTS } = require("../static/accessmodel.js");
const { hhmm } = require("../static/sessionrow.js");

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
});

test("a + Shell session: tier and workspace, a host tier reads as host", () => {
  const m = model({ session: { id: "sh", agent: "bash", cwd: "/w", runtime: "host", parent_id: "s1" }, aws: null });
  assert.strictEqual(m.containment.tier, "host");
  assert.strictEqual(m.containment.workspace, "/w");
  assert.strictEqual(m.containment.runtime, "");
  assert.deepStrictEqual(m.creds.rows, []);
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
});

test("a connection without a write role offers no action and no grant panel", () => {
  const r = model({ session: sess({ aws_connections: [conn({ write_capable: false, elevate: undefined })] }), aws: status() }).creds.rows[0];
  assert.deepStrictEqual(r.actions, []);
  assert.strictEqual(r.grant, null);
});

test("elevated session: red banner with minutes left, Extend (and no row Drop) near the end of the window", () => {
  const s = sess({ aws_connections: [conn({ elevated_until: iso(12) })] });
  const ap = [{ ts: NOW - 60000, action: "granted", minutes: 30, until: NOW + 12 * 60000, reason: "restart stuck worker", name: "culipulse-prod" }];
  const aws = status({ approvals: ap });
  const m = model({ session: s, aws });
  const banner = elevBanner(s, aws, NOW);
  assert.strictEqual(banner.conn, "culipulse-prod");
  assert.strictEqual(banner.minsLeft, 12);
  assert.match(banner.untilText, /^\d\d:\d\d$/);
  assert.strictEqual(banner.reason, "restart stuck worker");
  assert.strictEqual(banner.newCreds, true, "under 16 min the broker mints no more write credentials");
  const r = m.creds.rows[0];
  assert.strictEqual(r.scope, "write (elevated)");
  assert.deepStrictEqual(r.actions, ["extend"], "the pane's banner carries the one Drop");
});

test("elevated with plenty of time left: no row action (the rail's own Extend rule; Drop lives on the banner)", () => {
  const s = sess({ aws_connections: [conn({ elevated_until: iso(23) })] });
  const m = model({ session: s, aws: status() });
  const banner = elevBanner(s, status(), NOW);
  assert.strictEqual(banner.minsLeft, 23);
  assert.strictEqual(banner.newCreds, false);
  assert.strictEqual(banner.reason, "");
  assert.deepStrictEqual(m.creds.rows[0].actions, []);
});

test("a lapsed window is not a banner", () => {
  const s = sess({ aws_connections: [conn({ elevated_until: iso(-1) })] });
  const m = model({ session: s, aws: status() });
  assert.strictEqual(elevBanner(s, status(), NOW), null);
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

// A denial or a refusal is containment working: neutral text. Red is for something that broke (UI plan 6, E6).
test("accTone: containment refusals are neutral, errors stay red", () => {
  for (const o of ["denied_scope", "denied_box", "denied_token", "denied_session", "denied_not_read_only", "refused"]) {
    assert.strictEqual(accTone(o), "", o);
  }
  for (const o of ["sts_error", "scope_error", "github_error", "revoke_failed", "probe_failed", "rate_limited", "sso_expired", "store_locked", "unresolved", "not_installed"]) {
    assert.strictEqual(accTone(o), "bad", o);
  }
  for (const o of ["minted", "probe_minted", "fetched"]) assert.strictEqual(accTone(o), "ok", o);
  for (const o of ["revoked", "", undefined]) assert.strictEqual(accTone(o), "", String(o));
});

test("accessModel: a refused approval row has no tone", () => {
  const approvals = [
    { ts: NOW - 1000, action: "refused", name: "culipulse-prod" },
    { ts: NOW - 2000, action: "granted", minutes: 30, name: "culipulse-prod" },
  ];
  const ev = model({ session: sess(), aws: status({ approvals }) }).events;
  assert.deepStrictEqual([ev[0].subject, ev[0].tone], ["refused on culipulse-prod", ""]);
  assert.strictEqual(ev[1].tone, "", "an approval row never carries a tone of its own");
});

test("events: an approval reads as a decision by you, its reason kept as detail", () => {
  const approvals = [{ ts: NOW - 1000, action: "granted", minutes: 30, reason: "restart stuck worker deployment", name: "culipulse-prod" }];
  const e = model({ session: sess(), aws: status({ approvals }) }).events[0];
  assert.strictEqual(e.provider, "approval");
  assert.strictEqual(e.subject, "granted 30 min on culipulse-prod");
  assert.strictEqual(e.outcome, "you (browser)");
  assert.strictEqual(e.detail, "restart stuck worker deployment");
  const x = model({ session: sess(), aws: status({ approvals: [{ ts: NOW, action: "expired", name: "c" }] }) }).events[0];
  assert.strictEqual(x.outcome, "");
  assert.strictEqual(x.subject, "expired on c");
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

// --- the write window: one banner model, the lapse and leftover lines (UI plan 6) ---

const granted = (over) => Object.assign({ ts: NOW - 60000, action: "granted", minutes: 30, name: "culipulse-prod", reason: "rotate KMS key" }, over);
const open = (min, over) => sess({ aws_connections: [conn(Object.assign({ elevated_until: iso(min) }, over))] });

test("elevBanner: an open window names the connection, the clock, minutes left and the reason", () => {
  const aws = status({ approvals: [granted()] });
  const b = elevBanner(open(23), aws, NOW);
  assert.strictEqual(b.conn, "culipulse-prod");
  assert.strictEqual(b.untilText, hhmm(iso(23)));
  assert.strictEqual(b.minsLeft, 23);
  assert.strictEqual(b.leftText, "23 min left");
  assert.strictEqual(b.reason, "rotate KMS key");
  assert.strictEqual(b.newCreds, false);
  assert.strictEqual(b.detail, "until " + hhmm(iso(23)) + " · 23 min left · “rotate KMS key”");
  assert.strictEqual(typeof b.key, "string");
  // The Access view carries no banner of its own: the pane's banner (elevbanner.js) is the only one.
  assert.strictEqual("banner" in model({ session: open(23), aws }), false);
});

test("elevBanner: the reason is this connection's own newest grant, never another connection's", () => {
  const aws = status({ approvals: [ // deliberately not newest-first
    granted({ ts: NOW - 900000, reason: "first reason" }),
    granted({ name: "other", reason: "someone else's reason" }),
    granted({ ts: NOW - 120000, action: "extended", reason: "still rotating" }),
  ] });
  assert.strictEqual(elevBanner(open(23), aws, NOW).reason, "still rotating");
  assert.strictEqual(elevBanner(open(23), status({ approvals: [granted({ name: "other" })] }), NOW).reason, "");
});

test("elevBanner: no reason before the status loads, and no empty quotes after a failed read", () => {
  const b = elevBanner(open(23), null, NOW);
  assert.ok(b.detail.endsWith("23 min left"), b.detail);
  assert.ok(!b.detail.includes("“") && !b.detail.includes("”"), b.detail);
  assert.strictEqual(b.reason, "");
  // A failed read leaves aws null or undefined; a reply without approvals, with an empty
  // reason, or with a blank one says nothing either.
  for (const aws of [undefined, {}, status(), status({ approvals: [] }),
    status({ approvals: [granted({ reason: "" })] }), status({ approvals: [granted({ reason: undefined })] }),
    status({ approvals: [granted({ reason: "   " })] })]) {
    const x = elevBanner(open(23), aws, NOW);
    assert.strictEqual(x.detail, "until " + hhmm(iso(23)) + " · 23 min left", JSON.stringify(aws));
  }
});

test("elevBanner: under a minute reads <1 min left; under 16 min adds the base-role tail", () => {
  const soon = new Date(NOW + 30000).toISOString();
  const b = elevBanner(sess({ aws_connections: [conn({ elevated_until: soon })] }), null, NOW);
  assert.strictEqual(b.minsLeft, 0);
  assert.strictEqual(b.leftText, "<1 min left");
  assert.strictEqual(b.newCreds, true);
  assert.strictEqual(b.detail, "until " + hhmm(soon) + " · <1 min left · new credentials from the base role");
  const t = (min) => elevBanner(open(min), status({ approvals: [granted()] }), NOW);
  assert.strictEqual(t(15).detail, "until " + hhmm(iso(15)) + " · 15 min left · “rotate KMS key” · new credentials from the base role");
  assert.strictEqual(t(15).newCreds, true);
  assert.strictEqual(t(16).newCreds, false, "16 min is the broker's minimum: no tail from there up");
  assert.ok(!t(16).detail.includes("base role"));
  assert.strictEqual(t(1).leftText, "1 min left");
});

test("elevBanner: key follows exactly what is drawn", () => {
  const aws = status({ approvals: [granted()] });
  const a = elevBanner(open(23), aws, NOW);
  // 23 min 30 s left: seconds inside the minute change nothing visible, the next minute does.
  const half = sess({ aws_connections: [conn({ elevated_until: new Date(NOW + 23.5 * 60000).toISOString() })] });
  assert.strictEqual(elevBanner(half, aws, NOW + 20000).key, elevBanner(half, aws, NOW).key);
  assert.notStrictEqual(elevBanner(half, aws, NOW + 40000).key, elevBanner(half, aws, NOW).key, "the next minute repaints");
  assert.notStrictEqual(elevBanner(open(23), status({ approvals: [granted({ reason: "other" })] }), NOW).key, a.key);
  assert.notStrictEqual(elevBanner(open(23), null, NOW).key, a.key, "the reason arriving repaints");
  assert.notStrictEqual(elevBanner(open(23, { name: "z", label: "z" }), aws, NOW).key, a.key);
});

test("elevBanner: null once the window lapsed, and with no binding", () => {
  assert.strictEqual(elevBanner(open(-1), null, NOW), null);
  assert.strictEqual(elevBanner(sess({ aws_connections: [conn({ elevated_until: new Date(NOW).toISOString() })] }), null, NOW), null, "until == now is over: no 0 min left");
  assert.strictEqual(elevBanner(sess(), status(), NOW), null);
  assert.strictEqual(elevBanner(sess({ aws_connections: undefined }), null, NOW), null);
  assert.strictEqual(elevBanner({}, null, NOW), null);
  assert.strictEqual(elevBanner(undefined, undefined, NOW), null);
  // The status fallback (between a grant and the next poll) lapses the same way.
  const el = (min) => status({ elevation: { active: true, name: "culipulse-prod", until: iso(min), reason: "rotate KMS key" } });
  assert.strictEqual(elevBanner(sess(), el(-1), NOW), null);
  assert.strictEqual(elevBanner(sess(), el(0), NOW), null);
  const b = elevBanner(sess(), el(5), NOW);
  assert.strictEqual(b.conn, "culipulse-prod");
  assert.strictEqual(b.leftText, "5 min left");
});

test("elevBanner: a legacy inline binding is named by its label", () => {
  const inline = (aws) => ({ id: "s2", runtime: "box", aws: Object.assign({ label: "sandbox", account_id: "402100009917", role_arn: BASE, prod: false,
    elevate: { role_arn: WRITE, max_minutes: 30 }, elevated_until: iso(23) }, aws) });
  const named = { ts: NOW - 60000, action: "granted", minutes: 30, reason: "load test" };
  const b = elevBanner(inline(), status({ approvals: [named] }), NOW);
  assert.strictEqual(b.conn, "sandbox");
  assert.strictEqual(b.reason, "load test", "an unnamed approval belongs to the inline binding");
  assert.strictEqual(b.detail, "until " + hhmm(iso(23)) + " · 23 min left · “load test”");
  assert.strictEqual(elevBanner(inline(), status({ approvals: [Object.assign({}, named, { name: "culipulse-prod" })] }), NOW).reason, "", "a named connection's grant is not this binding's");
  assert.strictEqual(elevBanner(inline({ label: "" }), null, NOW).conn, "aws");
  assert.strictEqual(elevBanner(inline(), null, NOW).detail, "until " + hhmm(iso(23)) + " · 23 min left");
});

test("access row: an elevated connection offers no Drop", () => {
  const acts = (s) => model({ session: s, aws: status() }).creds.rows[0].actions;
  assert.deepStrictEqual(acts(open(19)), ["extend"]);
  assert.deepStrictEqual(acts(open(23)), []);
  assert.deepStrictEqual(acts(sess()), ["elevate"]);
  for (const min of [1, 12, 19, 23, 59]) assert.ok(!acts(open(min)).includes("drop"), String(min));
});

test("access row: a lapsed window leaves \"Write access ended HH:MM\"", () => {
  const ts = NOW - 10 * 60000;
  for (const action of ["expired", "ended", "dropped"]) {
    const approvals = [{ ts, action, name: "culipulse-prod" }, granted({ ts: NOW - 40 * 60000 })];
    const w = model({ session: sess(), aws: status({ approvals }) }).creds.rows[0].window;
    assert.strictEqual(w.ended, "Write access ended " + accEventTime(ts, NOW), action);
    assert.strictEqual(w.leftover, "", action);
  }
  // Newest wins whatever the array order, and a refusal or sign-in row is not a window event.
  const mixed = [granted({ ts: NOW - 40 * 60000 }), { ts: NOW - 2 * 60000, action: "refused", name: "culipulse-prod" },
    { ts, action: "expired", name: "culipulse-prod" }, { ts: NOW - 20 * 60000, action: "sso_login_ok" }];
  assert.strictEqual(model({ session: sess(), aws: status({ approvals: mixed }) }).creds.rows[0].window.ended, "Write access ended " + accEventTime(ts, NOW));
  // An earlier day carries its date, like the events table.
  const old = NOW - 26 * 3600000;
  assert.strictEqual(model({ session: sess(), aws: status({ approvals: [{ ts: old, action: "expired", name: "culipulse-prod" }] }) }).creds.rows[0].window.ended,
    "Write access ended " + accEventTime(old, NOW));
  assert.match(accEventTime(old, NOW), /^\d+ [A-Z][a-z]{2} \d\d:\d\d$/);
});

test("access row: no note without a lapse, while a window is open, or before the status loads", () => {
  assert.strictEqual(model({ session: sess(), aws: status({ approvals: [] }) }).creds.rows[0].window, null);
  assert.strictEqual(model({ session: sess(), aws: status() }).creds.rows[0].window, null, "approvals absent: unavailable, not none");
  assert.strictEqual(model({ session: sess(), aws: null }).creds.rows[0].window, null);
  const stale = status({ approvals: [{ ts: NOW - 10 * 60000, action: "expired", name: "culipulse-prod" }] });
  assert.strictEqual(model({ session: open(23), aws: stale }).creds.rows[0].window, null, "an open window is not 'ended'");
  // GitHub and token rows carry no window.
  const rows = model({ session: sess({ github: { owners: ["culiops"], repos: 1 }, tokens: [{ name: "sentry" }] }), aws: status() }).creds.rows;
  assert.deepStrictEqual(rows.slice(1).map((r) => r.window), [null, null]);
});

test("access row: a later grant hides the ended line, and another connection's end does not show here", () => {
  const regrant = [{ ts: NOW - 40 * 60000, action: "expired", name: "culipulse-prod" }, granted({ ts: NOW - 5 * 60000 })];
  assert.strictEqual(model({ session: sess(), aws: status({ approvals: regrant }) }).creds.rows[0].window, null);
  const extended = [{ ts: NOW - 40 * 60000, action: "dropped", name: "culipulse-prod" }, granted({ ts: NOW - 5 * 60000, action: "extended" })];
  assert.strictEqual(model({ session: sess(), aws: status({ approvals: extended }) }).creds.rows[0].window, null);
  const a = conn({ name: "a", prod: false }), b = conn({ name: "b", prod: false });
  const ended = status({ connections: [], approvals: [{ ts: NOW - 10 * 60000, action: "expired", name: "a" }] });
  const rows = model({ session: sess({ aws_connections: [a, b] }), aws: ended }).creds.rows;
  assert.match(rows.find((r) => r.name === "a").window.ended, /^Write access ended /);
  assert.strictEqual(rows.find((r) => r.name === "b").window, null);
  // A nameless approval is the inline binding's; it does not end a named connection's window.
  const nameless = status({ approvals: [{ ts: NOW - 10 * 60000, action: "expired" }] });
  assert.strictEqual(model({ session: sess(), aws: nameless }).creds.rows[0].window, null);
  const inline = { id: "s2", runtime: "box", aws: { label: "sandbox", account_id: "402100009917", role_arn: BASE, elevate: { role_arn: WRITE, max_minutes: 30 } } };
  assert.match(model({ session: inline, aws: nameless }).creds.rows[0].window.ended, /^Write access ended /);
});

test("access row: credentials outliving a drop are named while last_valid is in the future", () => {
  const leftover = (elevation, s) => {
    const r = model({ session: s || sess(), aws: status({ elevation }) }).creds.rows[0];
    return r.window ? r.window.leftover : "";
  };
  const el = (over) => Object.assign({ active: false, name: "culipulse-prod", last_valid: iso(8) }, over);
  assert.strictEqual(leftover(el()), "Credentials already issued stay valid until " + hhmm(iso(8)));
  assert.strictEqual(leftover(el({ last_valid: iso(-1) })), "", "past last_valid: nothing left to say");
  assert.strictEqual(leftover(el({ last_valid: new Date(NOW).toISOString() })), "");
  assert.strictEqual(leftover(el({ name: "other" })), "", "another connection's credentials");
  assert.strictEqual(leftover(el({ name: undefined })), "", "a nameless elevation is the inline binding's only");
  assert.strictEqual(leftover(el({ active: true })), "", "an open window has its own banner");
  assert.strictEqual(leftover(el({ last_valid: undefined })), "");
  assert.strictEqual(leftover(el({ last_valid: "0001-01-01T00:00:00Z" })), "", "the zero time is never");
  assert.strictEqual(leftover(undefined), "");
  const inline = { id: "s2", runtime: "box", aws: { label: "sandbox", account_id: "402100009917", role_arn: BASE, elevate: { role_arn: WRITE, max_minutes: 30 } } };
  assert.strictEqual(leftover(el({ name: undefined }), inline), "Credentials already issued stay valid until " + hhmm(iso(8)));
  assert.strictEqual(leftover(el(), inline), "", "a named elevation is not the inline binding's");
});

test("access row: a quick re-Elevate after a Drop shows the live window only, never the red leftover line beside it", () => {
  // The cached status still says the dropped window's credentials are valid (active:false, a future last_valid),
  // while the list entry already carries the new window: the row and the banner agree there is a window.
  const aws = status({ approvals: [{ ts: NOW - 3 * 60000, action: "dropped", name: "culipulse-prod" }],
    elevation: { active: false, name: "culipulse-prod", last_valid: iso(8) } });
  assert.strictEqual(model({ session: open(23), aws }).creds.rows[0].window, null);
  assert.strictEqual(model({ session: sess(), aws }).creds.rows[0].window.leftover, "Credentials already issued stay valid until " + hhmm(iso(8)),
    "the same status with the window closed still names them");
});

test("elevBanner: without a clock it reads the real one, never a silent null", () => {
  const live = sess({ aws_connections: [conn({ elevated_until: new Date(Date.now() + 23 * 60000).toISOString() })] });
  const b = elevBanner(live, null);
  assert.ok(b, "a window open now shows");
  assert.match(b.leftText, /^2[23] min left$/);
  assert.strictEqual(elevBanner(sess({ aws_connections: [conn({ elevated_until: new Date(Date.now() - 60000).toISOString() })] }), null), null);
});

test("access row: the leftover line is per connection: a window open on B does not hide A's issued credentials", () => {
  const a = conn({ name: "a", label: "a", prod: false });
  const b = conn({ name: "b", label: "b", prod: false, elevated_until: iso(23) });
  const aws = status({ connections: [], elevation: { active: false, name: "a", last_valid: iso(8) } });
  const rows = model({ session: sess({ aws_connections: [a, b] }), aws }).creds.rows;
  const ra = rows.find((r) => r.name === "a");
  const rb = rows.find((r) => r.name === "b");
  assert.strictEqual(ra.window.leftover, "Credentials already issued stay valid until " + hhmm(iso(8)), "true: they were issued on a, and a's window is over");
  assert.strictEqual(rb.window, null, "b has its own live window and says nothing");
  assert.strictEqual(rb.scope, "write (elevated)");
});

test("access row: after a Drop both lines can show, the leftover with it", () => {
  const ts = NOW - 2 * 60000;
  const aws = status({ approvals: [{ ts, action: "dropped", name: "culipulse-prod" }, granted({ ts: NOW - 20 * 60000 })],
    elevation: { active: false, name: "culipulse-prod", last_valid: iso(8) } });
  const r = model({ session: sess(), aws }).creds.rows[0];
  assert.deepStrictEqual({ ...r.window }, { ended: "Write access ended " + accEventTime(ts, NOW),
    leftover: "Credentials already issued stay valid until " + hhmm(iso(8)) });
  assert.deepStrictEqual(r.actions, ["elevate"], "a closed window offers Elevate again");
});

test("events: the elevated mint and the granted approval are tinted; Elevated reads yes/no, blank for approvals; the approval subject is \"granted 30 min on culipulse-prod\"", () => {
  const recent = [
    { provider: "", entry: "culipulse-prod", outcome: "minted", time: iso(-1), elevated: true },
    { provider: "", entry: "culipulse-prod", outcome: "minted", time: iso(-2), elevated: false },
    { provider: "github", subject: "culiops", outcome: "minted", time: iso(-3) },
    { provider: "aws", entry: "culipulse-prod", outcome: "denied_scope", time: iso(-4) },
  ];
  const static_recent = [{ provider: "static", subject: "sentry", outcome: "fetched", time: iso(-5) }];
  const approvals = [
    granted({ ts: NOW - 6 * 60000 }),
    granted({ ts: NOW - 7 * 60000, action: "extended", minutes: 15 }),
    { ts: NOW - 8 * 60000, action: "expired", name: "culipulse-prod" },
    { ts: NOW - 9 * 60000, action: "dropped", name: "culipulse-prod" },
    { ts: NOW - 10 * 60000, action: "ended", name: "culipulse-prod" },
    { ts: NOW - 11 * 60000, action: "refused", name: "culipulse-prod" },
    { ts: NOW - 12 * 60000, action: "sso_login_ok" },
  ];
  const ev = model({ session: sess({ tokens: [{ name: "sentry" }] }), aws: status({ recent, static_recent, approvals }) }).events;
  const by = (n) => ev[n];
  assert.deepStrictEqual([by(0).tint, by(0).elevatedText], [true, "yes"], "the elevated mint");
  assert.deepStrictEqual([by(1).tint, by(1).elevatedText], [false, "no"]);
  assert.deepStrictEqual([by(2).tint, by(2).elevatedText], [false, "no"], "github");
  assert.deepStrictEqual([by(3).tint, by(3).elevatedText], [false, "no"], "a refused mint");
  assert.deepStrictEqual([by(4).tint, by(4).elevatedText], [false, "no"], "a static token");
  const ap = ev.filter((e) => e.provider === "approval");
  assert.deepStrictEqual(ap.map((e) => e.tint), [true, true, false, false, false, false, false], "granted and extended only");
  assert.ok(ap.every((e) => e.elevatedText === ""), "approval rows leave Elevated blank");
  assert.strictEqual(ap[0].subject, "granted 30 min on culipulse-prod");
  assert.strictEqual(ap[0].detail, "rotate KMS key", "the reason stays a separate part");
  assert.strictEqual(ap[1].subject, "extended 15 min on culipulse-prod");
  assert.strictEqual(ap[2].subject, "expired on culipulse-prod");
  assert.strictEqual(ap[5].subject, "refused on culipulse-prod");
  assert.strictEqual(ap[6].subject, "sign-in ok", "no connection, no ' on '");
  assert.ok(ev.every((e) => typeof e.tint === "boolean"));
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

test("accessModel: an sso_direct row shows the mode in its base line, not the AssumeRole wording", () => {
  const conn = { name: "sbx", label: "sbx", account_id: "123456789012", role_arn: "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_RO_abc", mode: "sso_direct", hours: 4, base_access: "none_found" };
  const m = model({ session: sess({ aws_connections: [conn] }) });
  const row = m.creds.rows[0];
  assert.ok(row.base.endsWith("· SSO role directly"), row.base);
  assert.strictEqual(row.baseWarn, false);
  assert.doesNotMatch(JSON.stringify(row), /no broad writes found/);
});

test("accessModel: an sso_direct row without a role name shows the mode label, not an empty role", () => {
  const conn = { name: "sbx", label: "sbx", account_id: "123456789012", role_arn: "", mode: "sso_direct", region: "eu-west-1", hours: 4 };
  const row = model({ session: sess({ aws_connections: [conn] }) }).creds.rows[0];
  assert.strictEqual(row.sub, "account 123456789012 · SSO role directly · eu-west-1");
  assert.doesNotMatch(row.sub, /role\s+·/);
});
