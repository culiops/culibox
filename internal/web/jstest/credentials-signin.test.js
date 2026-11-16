"use strict";
// #141: the AWS table's sign-in column says what the SSO token cache says, not that the config parses.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { credSignIn, credTableRows } = require("../static/credentials.js");

const NOW = new Date(2026, 10, 16, 12, 0, 0); // local time, so the clock-time expectations hold in any zone
const iso = (d) => d.toISOString();
const sin = (state, extra) => ({ state, ...extra });
const awsE = (name, extra) => ({
  name, kind: "aws", account_id: "210987654321", role_arn: "arn:aws:iam::210987654321:role/Role",
  region: "ap-southeast-1", status: "ok", source: "web", prod: false, used_by: [], base: null, ...extra,
});

test("credSignIn: green only for a signed-in session, with its expiry in words", () => {
  const today = new Date(2026, 10, 16, 15, 4, 0);
  assert.deepStrictEqual(credSignIn({ status: "ok", signin: sin("signed_in", { until: iso(today) }) }, NOW),
    { cls: "ready", text: "Signed in", sub: "until 15:04" });
  const tomorrow = new Date(2026, 10, 17, 9, 5, 0);
  assert.strictEqual(credSignIn({ status: "ok", signin: sin("signed_in", { until: iso(tomorrow) }) }, NOW).sub, "until 2026-11-17 09:05");
  // no readable expiry: still signed in, no invented time
  assert.deepStrictEqual(credSignIn({ status: "ok", signin: sin("signed_in") }, NOW), { cls: "ready", text: "Signed in", sub: "" });
});

test("credSignIn: signed in is not shown once its expiry has passed, even if the server's answer is older", () => {
  const past = new Date(2026, 10, 16, 11, 0, 0);
  const s = credSignIn({ status: "ok", signin: sin("signed_in", { until: iso(past) }) }, NOW);
  assert.notStrictEqual(s.cls, "ready");
  assert.strictEqual(s.text, "Sign-in expired");
});

test("credSignIn: expired is amber, never signed in, renewable and unknown are neutral, none of them green", () => {
  const exp = credSignIn({ status: "ok", signin: sin("expired", { until: iso(new Date(2026, 10, 16, 8, 0)) }) }, NOW);
  assert.deepStrictEqual([exp.cls, exp.text], ["locked", "Sign-in expired"]);
  const never = credSignIn({ status: "ok", signin: sin("never") }, NOW);
  assert.deepStrictEqual([never.cls, never.text], ["idle", "Not signed in"]);
  const ren = credSignIn({ status: "ok", signin: sin("renewable", { until: iso(new Date(2026, 10, 16, 8, 0)) }) }, NOW);
  assert.deepStrictEqual([ren.cls, ren.text], ["idle", "Renews on use"]);
  const unk = credSignIn({ status: "ok", signin: sin("unknown") }, NOW);
  assert.deepStrictEqual([unk.cls, unk.text], ["idle", "Unknown"]);
  for (const s of [exp, never, ren, unk]) assert.notStrictEqual(s.cls, "ready");
});

test("credSignIn: expired and never-signed-in say where the sign-in is: Edit for a web entry, the host command for a config.json one", () => {
  const web = { kind: "aws", source: "web", source_profile: "corp" }, seed = { kind: "aws", source: "config", source_profile: "corp" };
  for (const st of ["expired", "never"]) {
    assert.strictEqual(credSignIn({ ...web, status: "ok", signin: sin(st) }, NOW).sub, "Edit, then Check to sign in");
    assert.strictEqual(credSignIn({ ...seed, status: "ok", signin: sin(st) }, NOW).sub, "run: aws sso login --profile corp");
  }
  assert.strictEqual(credSignIn({ ...seed, source_profile: "", status: "ok", signin: sin("never") }, NOW).sub, "sign in on the host");
});

test("credSignIn: a web entry names Edit in its sign-in hint", () => {
  for (const st of ["expired", "never"]) {
    assert.match(credSignIn({ kind: "aws", source: "web", status: "ok", signin: sin(st) }, NOW).sub, /Edit/);
  }
});

test("credSignIn: a missing, new or garbled signin never reads as ready", () => {
  for (const e of [{ status: "ok" }, { status: "ok", signin: null }, { status: "ok", signin: {} }, { status: "ok", signin: sin("ready") }, { status: "ok", signin: sin("ok") }]) {
    const s = credSignIn(e, NOW);
    assert.deepStrictEqual([s.cls, s.text], ["idle", "Unknown"], JSON.stringify(e));
  }
});

test("credSignIn: a profile that is not SSO says so; sso_direct needs one", () => {
  assert.deepStrictEqual(credSignIn({ status: "ok", signin: sin("not_sso") }, NOW), { cls: "idle", text: "Not SSO", sub: "no sign-in needed" });
  const d = credSignIn({ status: "ok", mode: "sso_direct", signin: sin("not_sso") }, NOW);
  assert.deepStrictEqual([d.cls, d.text], ["locked", "Not an SSO profile"]);
});

test("credSignIn: a source profile missing from the AWS config is amber, not ready", () => {
  const s = credSignIn({ status: "ok", source_profile: "gone", signin: sin("no_profile") }, NOW);
  assert.deepStrictEqual([s.cls, s.text], ["locked", "Profile not found"]);
  assert.match(s.sub, /gone/);
});

test("credSignIn: an entry whose config is not ok keeps the config status, whatever the cache says", () => {
  assert.deepStrictEqual(credSignIn({ status: "invalid", signin: sin("signed_in") }, NOW), { cls: "invalid", text: "Invalid", sub: "" });
  assert.deepStrictEqual(credSignIn({ status: "missing" }, NOW), { cls: "missing", text: "Missing", sub: "" });
});

test("credTableRows aws: the third cell is the sign-in state, or the config status when the config is not ok", () => {
  const rows = credTableRows({ key: "aws", entries: [
    awsE("a", { signin: sin("never") }),
    awsE("b", { signin: sin("expired") }),
    awsE("c", { status: "invalid" }),
  ] });
  assert.deepStrictEqual(rows.map((r) => [r.cells[2].kind, r.cells[2].text, r.cells[2].cls]),
    [["st", "Not signed in", "idle"], ["st", "Sign-in expired", "locked"], ["st", "Invalid", "invalid"]]);
  assert.match(rows[0].cells[2].sub, /Edit/);
});

test("credAWSCard: the phone card shows the same sign-in state as the table", () => {
  const src = fs.readFileSync(path.join(__dirname, "../static/credentials.js"), "utf8");
  const card = src.slice(src.indexOf("function credAWSCard"), src.indexOf("function credAppCard"));
  assert.match(card, /credSignIn\(e/);
  assert.match(card, /chip:/);
});

test("css: the neutral sign-in chip exists on the phone and on the page, scoped to the credentials screen", () => {
  const app = fs.readFileSync(path.join(__dirname, "../static/app.css"), "utf8");
  const desk = fs.readFileSync(path.join(__dirname, "../static/desktop.css"), "utf8");
  assert.match(app, /:is\(#creds, #cred-drawer\) \.st\.idle \{/);
  assert.match(desk, /:is\(#creds, #cred-drawer\) \.st\.idle \{/);
});
