"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { connPickerKind, grantPanel, grantBody, grantErrorText, trustedSSOURL, ssoCard, dropToast, ssoLoginOf, ssoCommandOf, ssoSignInName } = require("../static/elevation.js");

const WRITE = "arn:aws:iam::123456789012:role/agentbox-agent-write";
const aws = { label: "sandbox", account_id: "123456789012", prod: false, elevate: { role_arn: WRITE, max_minutes: 60, allow_iam: false } };

test("grantPanel: windows up to the binding's max, role, host-tier warning, prod label", () => {
  const st = { elevate: { host_sessions: [{ id: "zsh-1", name: "zsh" }], blocked: "" } };
  assert.deepStrictEqual(grantPanel(aws, st), {
    role: "agentbox-agent-write", windows: [30, 60], needLabel: false, label: "sandbox",
    hostSessions: [{ id: "zsh-1", name: "zsh" }], blocked: "", allowIAM: false, boxPrefix: "", passRolePrefix: "",
  });
  assert.deepStrictEqual(grantPanel({ ...aws, elevate: { ...aws.elevate, max_minutes: 30 } }, st).windows, [30]);
  assert.strictEqual(grantPanel({ ...aws, prod: true }, st).needLabel, true);
  assert.deepStrictEqual(grantPanel(aws, null).hostSessions, []); // status not loaded yet: no list, not a crash
  assert.strictEqual(grantPanel(aws, { elevate: { blocked: "a box started by an older CuliBox (instance PROD) uses /w" } }).blocked,
    "a box started by an older CuliBox (instance PROD) uses /w");
  assert.strictEqual(grantPanel({ label: "x" }, st), null); // no elevate block: no panel at all
  assert.strictEqual(grantPanel(null, st), null);
});

test("grantBody: minutes always, reason and label only when given or required", () => {
  assert.deepStrictEqual(grantBody(60, "", "", false), { minutes: 60 });
  assert.deepStrictEqual(grantBody(30, "  apply vpc  ", "", false), { minutes: 30, reason: "apply vpc" });
  assert.deepStrictEqual(grantBody(30, "", "prod-acct", true), { minutes: 30, confirm_label: "prod-acct" });
  assert.deepStrictEqual(grantBody(30, "", "typed", false), { minutes: 30 }); // a label is never sent unasked
});

test("grantErrorText", () => {
  assert.match(grantErrorText(403, "x"), /signed-in/);
  assert.strictEqual(grantErrorText(409, "this session's box is shared; restart it to get its own box"),
    "this session's box is shared; restart it to get its own box");
  assert.match(grantErrorText(0, ""), /no connection/);
  assert.match(grantErrorText(500, ""), /\(500\)/);
});

test("trustedSSOURL mirrors the server's allowlist", () => {
  for (const u of ["https://device.sso.ap-southeast-1.amazonaws.com/", "https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH",
    "https://d-0000000000.awsapps.com/start/#/device"]) assert.ok(trustedSSOURL(u), u);
  for (const u of ["http://device.sso.ap-southeast-1.amazonaws.com/", "https://device.sso.ap-southeast-1.amazonaws.com.evil.example/",
    "https://awsapps.com/", "https://user@d-0.awsapps.com/", "https://x.awsapps.com:444/", "javascript:alert(1)", "",
    null, undefined, "https://a.b.awsapps.com/", "https://evil.example/?h=x.awsapps.com"]) {
    assert.ok(!trustedSSOURL(u), String(u));
  }
});

test("ssoCard: text names the sso session; link only when trusted; command from the status", () => {
  const s = { id: "aws-1", need: "sso", aws: { sso_expired: "example-sso" } };
  const st = { sso: { expired: true, source: "example-sso", command: "aws sso login --profile sandbox-admin",
    login: { state: "waiting", uri: "https://device.sso.ap-southeast-1.amazonaws.com/?user_code=ABCD-EFGH", uri_trusted: true, code: "ABCD-EFGH", expires: "2027-01-15T08:25:00Z" } } };
  const c = ssoCard(s, st);
  assert.strictEqual(c.text, "AWS sign-in expired (example-sso). This session's AWS calls fail until you sign in.");
  assert.strictEqual(c.command, "aws sso login --profile sandbox-admin");
  assert.deepStrictEqual(c.login, { state: "waiting", link: st.sso.login.uri, plain: "", code: "ABCD-EFGH", expires: "2027-01-15T08:25:00Z" });
  const evil = { sso: { ...st.sso, login: { ...st.sso.login, uri: "https://evil.example/", uri_trusted: true } } };
  assert.deepStrictEqual([ssoCard(s, evil).login.link, ssoCard(s, evil).login.plain], ["", "https://evil.example/"]); // the client re-checks
  const unvouched = { sso: { ...st.sso, login: { ...st.sso.login, uri_trusted: false } } };
  assert.deepStrictEqual([ssoCard(s, unvouched).login.link, ssoCard(s, unvouched).login.plain], ["", st.sso.login.uri]); // the server's word is required too
  assert.strictEqual(ssoCard(s, null).command, "");
  assert.strictEqual(ssoCard(s, null).login, null);
});

test("ssoCard keeps the reply affordance of a replyable session (spec 8.2)", () => {
  const sso = { id: "aws-1", need: "sso", claude: true, aws: { sso_expired: "example-sso" } };
  assert.strictEqual(ssoCard({ ...sso, replyable: true }, null).reply, true);
  assert.strictEqual(ssoCard({ ...sso, replyable: false }, null).reply, false);
  assert.strictEqual(ssoCard({ id: "x", need: "sso" }, null).text,
    "AWS sign-in expired. This session's AWS calls fail until you sign in."); // no session name known
});

test("dropToast names when the issued credentials stop working", () => {
  assert.match(dropToast({ last_valid: "2027-01-15T08:21:00Z" }), /^Write access dropped\. Credentials already issued stay valid until \d\d:\d\d\.$/);
  assert.strictEqual(dropToast({}), "Write access dropped. No write credentials were issued.");
  assert.strictEqual(dropToast(null), "Write access dropped. No write credentials were issued.");
});

// --- #155 named AWS connections ---------------------------------------------
const NOW = Date.parse("2027-01-15T08:00:00Z");
const iso = (min) => new Date(NOW + min * 60000).toISOString();
const wc = (name, over) => Object.assign({ name: name, label: name.toUpperCase(), account_id: "123456789012", role_arn: "arn:aws:iam::123456789012:role/ro-" + name,
  region: "ap-southeast-1", prod: false, write_capable: true,
  elevate: { role_arn: "arn:aws:iam::123456789012:role/w-" + name, max_minutes: 60, allow_iam: false } }, over);
const ro = (name) => ({ name: name, label: name, account_id: "1", role_arn: "arn:aws:iam::1:role/r", region: "us-east-1", prod: false, write_capable: false });

test("grantPanel (connections): the selector exists only with two or more write-capable connections", () => {
  const st = { elevate: { host_sessions: [] } };
  const one = grantPanel(null, st, [wc("a"), ro("b")], "", NOW);
  assert.deepStrictEqual(one.conns.map((c) => c.name), ["a"]);
  assert.strictEqual(one.selector, false);
  assert.strictEqual(one.selected, "a");
  assert.strictEqual(one.role, "w-a");
  const two = grantPanel(null, st, [wc("a"), wc("b"), ro("c")], "b", NOW);
  assert.strictEqual(two.selector, true);
  assert.deepStrictEqual(two.conns.map((c) => c.name), ["a", "b"]);
  assert.strictEqual(two.selected, "b");
  assert.strictEqual(grantPanel(null, st, [wc("a"), wc("b")], "nope", NOW).selected, "a", "an unknown pick falls back to the first");
  assert.strictEqual(grantPanel(null, st, [ro("a"), ro("b")], "", NOW), null, "nothing write-capable: no panel");
});

test("grantPanel (connections): windows, prod label and role follow the selected connection", () => {
  const conns = [wc("a", { elevate: { role_arn: "arn:aws:iam::1:role/w-a", max_minutes: 30 } }), wc("b", { prod: true, label: "prod-b" })];
  const a = grantPanel(null, null, conns, "a", NOW);
  assert.deepStrictEqual(a.windows, [30]);
  assert.strictEqual(a.needLabel, false);
  const b = grantPanel(null, null, conns, "b", NOW);
  assert.deepStrictEqual(b.windows, [30, 60]);
  assert.strictEqual(b.needLabel, true);
  assert.strictEqual(b.label, "prod-b");
  assert.deepStrictEqual(b.hostSessions, []);
});

test("grantPanel (connections): another connection's window blocks this one with Drop <name> first", () => {
  const conns = [wc("a"), wc("b")];
  const st = { elevation: { active: true, until: iso(20), name: "a" }, elevate: { host_sessions: [] } };
  const onB = grantPanel(null, st, conns, "b", NOW);
  assert.strictEqual(onB.elevatedConn, "a");
  assert.strictEqual(onB.blockedText, "Drop a first");
  const onA = grantPanel(null, st, conns, "a", NOW);
  assert.strictEqual(onA.blockedText, "", "re-granting the elevated connection is allowed");
  assert.strictEqual(grantPanel(null, st, conns, "", NOW).selected, "a", "no pick: the elevated one");
  // the list entry alone (status not loaded yet) is enough to know
  const listOnly = grantPanel(null, null, [wc("a", { elevated_until: iso(20) }), wc("b")], "b", NOW);
  assert.strictEqual(listOnly.elevatedConn, "a");
  assert.strictEqual(listOnly.blockedText, "Drop a first");
  assert.strictEqual(grantPanel(null, null, [wc("a", { elevated_until: iso(-1) }), wc("b")], "b", NOW).blockedText, "");
});

test("grantPanel (connections): the leftover refusal of the status is the blocked text, verbatim", () => {
  const msg = "a's write credentials stay valid until 14:32 \u2014 b can be granted then";
  const st = { elevation: { active: false, name: "a", last_valid: iso(10) }, elevate: { host_sessions: [], conn_blocked: { b: msg } } };
  assert.strictEqual(grantPanel(null, st, [wc("a"), wc("b")], "b", NOW).blockedText, msg);
  assert.strictEqual(grantPanel(null, st, [wc("a"), wc("b")], "a", NOW).blockedText, "");
});

test("grantPanel carries the two opt-in prefixes, empty when unset", () => {
  assert.strictEqual(grantPanel(aws, null).boxPrefix, "");
  assert.strictEqual(grantPanel(aws, null).passRolePrefix, "");
  const on = { ...aws, elevate: { ...aws.elevate, box_bucket_prefix: "culibox-", pass_role_prefix: "lab-" } };
  assert.strictEqual(grantPanel(on, null).boxPrefix, "culibox-");
  assert.strictEqual(grantPanel(on, null).passRolePrefix, "lab-");
});

test("grantPanel: the legacy shape is untouched by the connection arguments", () => {
  assert.deepStrictEqual(Object.keys(grantPanel(aws, null)).sort(),
    ["allowIAM", "blocked", "boxPrefix", "hostSessions", "label", "needLabel", "passRolePrefix", "role", "windows"]);
  assert.deepStrictEqual(Object.keys(grantPanel(aws, null, [], "")).sort(),
    ["allowIAM", "blocked", "boxPrefix", "hostSessions", "label", "needLabel", "passRolePrefix", "role", "windows"]);
});

test("grantBody: name only when the session has connections", () => {
  assert.deepStrictEqual(grantBody(60, "", "", false), { minutes: 60 });
  assert.deepStrictEqual(grantBody(60, "", "", false, ""), { minutes: 60 });
  assert.deepStrictEqual(grantBody(60, "why", "", false, undefined), { minutes: 60, reason: "why" });
  assert.deepStrictEqual(grantBody(30, "", "prod-b", true, "b"), { minutes: 30, confirm_label: "prod-b", name: "b" });
});

test("ssoSignInName: the first expired connection by name, nothing for the legacy binding", () => {
  assert.strictEqual(ssoSignInName({ aws: { sso_expired: "corp" } }), "");
  assert.strictEqual(ssoSignInName({}), "");
  assert.strictEqual(ssoSignInName({ aws_connections: [wc("b", { sso_expired: "corp" }), wc("a", { sso_expired: "corp" }), wc("c")] }), "a");
  assert.strictEqual(ssoSignInName({ aws_connections: [wc("a")] }), "a", "none flagged: still names one, the server refuses a missing name");
});

test("ssoLoginOf and ssoCommandOf read the legacy fields, then the per-source ones", () => {
  const login = { state: "waiting", code: "ABCD" };
  assert.strictEqual(ssoLoginOf({ login: login }), login);
  assert.strictEqual(ssoLoginOf(undefined), null);
  const sso = { sources: [{ source: "x", expired: true, command: "aws sso login --profile x" }, { source: "y", expired: true, command: "cmd-y", login: login }] };
  assert.strictEqual(ssoLoginOf(sso), login);
  assert.strictEqual(ssoCommandOf({ command: "top" }), "top");
  assert.strictEqual(ssoCommandOf(sso), "aws sso login --profile x", "the first expired source");
  assert.strictEqual(ssoCommandOf({ sources: [{ source: "x", expired: false, command: "idle" }] }), "");
  assert.strictEqual(ssoCommandOf(null), "");
});

test("ssoCard (connections): the command comes from the expired source; the text names the sources", () => {
  const s = { aws_connections: [wc("a", { sso_expired: "corp" }), wc("b", { sso_expired: "corp" }), wc("c", { sso_expired: "dev" })] };
  const m = ssoCard(s, { sso: { command: "", login: null } });
  assert.match(m.text, /^AWS sign-in expired \(corp, dev\)\./);
  const m2 = ssoCard(s, { sso: { command: "aws sso login --profile corp", login: null } });
  assert.strictEqual(m2.command, "aws sso login --profile corp");
});

// --- #155 fix round 1 --------------------------------------------------------
test("grantPanel (connections): a lapsed window in a cached status blocks nothing", () => {
  const conns = [wc("a"), wc("b")];
  const stale = { elevation: { active: true, until: iso(-1), name: "a" }, elevate: { host_sessions: [], conn_blocked: { b: "drop a first" } } };
  const p = grantPanel(null, stale, conns, "b", NOW);
  assert.strictEqual(p.elevatedConn, "", "the clock, not the cached flag, decides");
  assert.strictEqual(p.blockedText, "", "the server's 'drop a first' is as old as the status");
  // a window still running in the same status does block
  const live = { elevation: { active: true, until: iso(5), name: "a" }, elevate: { host_sessions: [] } };
  assert.strictEqual(grantPanel(null, live, conns, "b", NOW).blockedText, "Drop a first");
  // the fresh list entry wins over a status that has not seen the grant yet
  const none = { elevation: { active: false }, elevate: { host_sessions: [] } };
  assert.strictEqual(grantPanel(null, none, [wc("a", { elevated_until: iso(20) }), wc("b")], "b", NOW).blockedText, "Drop a first");
});

test("grantPanel (connections): the leftover sentence goes once its time has passed", () => {
  const msg = "a's write credentials stay valid until 14:32 \u2014 b can be granted then";
  const mk = (lastValid) => ({ elevation: { active: false, name: "a", last_valid: lastValid }, elevate: { host_sessions: [], conn_blocked: { b: msg } } });
  assert.strictEqual(grantPanel(null, mk(iso(10)), [wc("a"), wc("b")], "b", NOW).blockedText, msg);
  assert.strictEqual(grantPanel(null, mk(iso(-1)), [wc("a"), wc("b")], "b", NOW).blockedText, "");
  assert.strictEqual(grantPanel(null, mk(""), [wc("a"), wc("b")], "b", NOW).blockedText, "");
  // the dropped connection itself may be granted again at once
  assert.strictEqual(grantPanel(null, { ...mk(iso(10)), elevate: { host_sessions: [], conn_blocked: { b: msg } } }, [wc("a"), wc("b")], "a", NOW).blockedText, "");
});

test("ssoCard targets ONE source: the one the Sign in button will sign in", () => {
  // a signs in through dev, b through corp; both are expired. The button names a.
  const s = { aws_connections: [wc("b", { sso_expired: "corp" }), wc("a", { sso_expired: "dev" })] };
  const sso = { sources: [
    { source: "corp", names: ["b"], expired: true, command: "aws sso login --profile corp", login: { state: "waiting", code: "CORP-1" } },
    { source: "dev", names: ["a"], expired: true, command: "aws sso login --profile dev" },
  ] };
  assert.strictEqual(ssoSignInName(s), "a");
  assert.strictEqual(ssoCommandOf(sso, "a"), "aws sso login --profile dev");
  assert.strictEqual(ssoLoginOf(sso, "a"), null, "corp's flow is not a's");
  const m = ssoCard(s, { sso });
  assert.strictEqual(m.command, "aws sso login --profile dev");
  assert.strictEqual(m.login, null);
  // dev's own flow is shown
  const sso2 = { sources: [sso.sources[0], { ...sso.sources[1], login: { state: "waiting", code: "DEV-9" } }] };
  assert.strictEqual(ssoCard(s, { sso: sso2 }).login.code, "DEV-9");
  // legacy top-level fields are unchanged
  assert.strictEqual(ssoCommandOf({ command: "top", sources: sso.sources }, "a"), "top");
});

// --- #155 visual fix round ---------------------------------------------------
test("connPickerKind: text for one, segmented for two, a select from three", () => {
  assert.deepStrictEqual([1, 2, 3, 4, 9].map(connPickerKind), ["text", "seg", "select", "select", "select"]);
  const st = { elevate: { host_sessions: [] } };
  const kind = (n) => grantPanel(null, st, Array.from({ length: n }, (_, i) => wc("c" + i)), "", NOW).picker;
  assert.strictEqual(kind(1), "text");
  assert.strictEqual(kind(2), "seg");
  assert.strictEqual(kind(3), "select");
  assert.strictEqual(kind(4), "select");
  // read-only connections do not count toward the switch
  assert.strictEqual(grantPanel(null, st, [wc("a"), wc("b"), wc("c", { write_capable: false, elevate: undefined })], "", NOW).picker, "seg");
});

// --- elevGrant / elevDrop: the requests the rail and the Access view share ----

function stubFetch(res) {
  const calls = [];
  global.fetch = async (url, init) => {
    calls.push({ url, init });
    if (res instanceof Error) throw res;
    return { status: res.status, json: async () => { if (res.body === undefined) throw new Error("no json"); return res.body; } };
  };
  return calls;
}

test("elevGrant: POSTs the grant body and resolves the elevation view", async () => {
  const { elevGrant } = require("../static/elevation.js");
  const calls = stubFetch({ status: 200, body: { elevation: { active: true, name: "c" } } });
  const v = await elevGrant("s/1", { name: "c", minutes: 30, reason: " why ", confirm_label: "prod" });
  assert.deepStrictEqual(v, { active: true, name: "c" });
  assert.strictEqual(calls[0].url, "/api/sessions/s%2F1/aws/elevate");
  assert.strictEqual(calls[0].init.method, "POST");
  assert.deepStrictEqual(JSON.parse(calls[0].init.body), { minutes: 30, reason: "why", confirm_label: "prod", name: "c" });
});

test("elevGrant: a label and a name are sent only when given, as the rail always did", async () => {
  const { elevGrant } = require("../static/elevation.js");
  const calls = stubFetch({ status: 200, body: { elevation: null } });
  await elevGrant("s", { name: "", minutes: 60, reason: "", confirm_label: undefined });
  assert.deepStrictEqual(JSON.parse(calls[0].init.body), { minutes: 60 });
  await elevGrant("s", { minutes: 30, reason: "r", confirm_label: "" }); // a prod binding, label still empty
  assert.deepStrictEqual(JSON.parse(calls[1].init.body), { minutes: 30, reason: "r", confirm_label: "" });
});

test("elevGrant: a refusal rejects with the server's sentence and the status", async () => {
  const { elevGrant } = require("../static/elevation.js");
  stubFetch({ status: 409, body: { error: "Drop b first" } });
  await assert.rejects(elevGrant("s", { minutes: 30 }), (e) => e.message === "Drop b first" && e.status === 409);
  stubFetch({ status: 403, body: { error: "x" } });
  await assert.rejects(elevGrant("s", { minutes: 30 }), (e) => e.message === grantErrorText(403) && e.status === 403);
  stubFetch(new Error("offline"));
  await assert.rejects(elevGrant("s", { minutes: 30 }), (e) => e.message === "Elevate failed: no connection." && e.status === 0);
  stubFetch({ status: 500 }); // no JSON body
  await assert.rejects(elevGrant("s", { minutes: 30 }), (e) => e.message === "Elevate failed (500)." && e.status === 500);
});

test("elevDrop: DELETE with no body; failures carry the toast text", async () => {
  const { elevDrop, dropErrorText } = require("../static/elevation.js");
  const calls = stubFetch({ status: 200, body: { elevation: { active: false, last_valid: "2026-10-27T15:40:00Z" } } });
  assert.strictEqual((await elevDrop("s", "c")).active, false);
  assert.strictEqual(calls[0].init.method, "DELETE");
  assert.strictEqual(calls[0].init.body, undefined, "a drop names no connection");
  stubFetch({ status: 410, body: {} });
  await assert.rejects(elevDrop("s"), (e) => e.message === "That session is gone." && e.status === 410);
  assert.strictEqual(dropErrorText(0), "Drop failed: no connection.");
  assert.strictEqual(dropErrorText(500), "Drop failed (500).");
});
