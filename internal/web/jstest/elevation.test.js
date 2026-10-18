"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { grantPanel, grantBody, grantErrorText, trustedSSOURL, ssoCard, dropToast } = require("../static/elevation.js");

const WRITE = "arn:aws:iam::226198813800:role/agentbox-agent-write";
const aws = { label: "sandbox", account_id: "226198813800", prod: false, elevate: { role_arn: WRITE, max_minutes: 60, allow_iam: false } };

test("grantPanel: windows up to the binding's max, role, host-tier warning, prod label", () => {
  const st = { elevate: { host_sessions: [{ id: "zsh-1", name: "zsh" }], blocked: "" } };
  assert.deepStrictEqual(grantPanel(aws, st), {
    role: "agentbox-agent-write", windows: [30, 60], needLabel: false, label: "sandbox",
    hostSessions: [{ id: "zsh-1", name: "zsh" }], blocked: "", allowIAM: false,
  });
  assert.deepStrictEqual(grantPanel({ ...aws, elevate: { ...aws.elevate, max_minutes: 30 } }, st).windows, [30]);
  assert.strictEqual(grantPanel({ ...aws, prod: true }, st).needLabel, true);
  assert.deepStrictEqual(grantPanel(aws, null).hostSessions, []); // status not loaded yet: no list, not a crash
  assert.strictEqual(grantPanel(aws, { elevate: { blocked: "a box started by an older agent-box (instance PROD) uses /w" } }).blocked,
    "a box started by an older agent-box (instance PROD) uses /w");
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
