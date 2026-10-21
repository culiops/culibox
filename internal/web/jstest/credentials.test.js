"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  credGroups, statusText, baseText, credWriteActions, usedByText, entryActions, isSeed,
  providerTitle, checkedText, credRoleName, valueRefsText, valueUpdatedText, CRED_D4, CRED_N3, CRED_FILE_BAD,
  awsConnName, awsRowFromProfile, rowSaveState, awsSplitList, awsConnFromRow, awsRoleARN, awsCheckView, awsErrText, CRED_D4_REMEDY,
} = require("../static/credentials.js");

const view = (over) => ({
  store: { initialized: true, locked: false, expires_at: null, platform: "linux", passphrase_mode: true },
  aws: [], github_apps: [], tokens: [], values: [], legacy_secrets: [], file_bad: false, invalid: [],
  ...over,
});
const tok = (name, provider, extra) => ({ name, provider, template: "", label: "", description: "", prod: false, env: [], hosts: [], value_kind: "store", source: "web", status: "ok", used_by: [], ...extra });

test("groups come in the order aws, github, token providers by title, values, legacy", () => {
  const v = view({
    aws: [{ name: "a" }],
    github_apps: [{ name: "g" }],
    tokens: [tok("t1", "sentry"), tok("t2", "grafana"), tok("t3", "datadog"), tok("t4", "grafana")],
    values: [{ id: "v" }],
    legacy_secrets: [{ name: "l", status: "ok" }],
  });
  const gs = credGroups(v);
  assert.deepStrictEqual(gs.map((g) => g.key), ["aws", "github", "token:datadog", "token:grafana", "token:sentry", "values", "legacy"]);
  assert.deepStrictEqual(gs.map((g) => g.title), ["AWS", "GitHub Apps", "Datadog", "Grafana", "Sentry", "Stored values", "Legacy secrets"]);
  assert.strictEqual(gs[3].entries.length, 2);
  assert.strictEqual(gs[0].entries[0].kind, "aws");
});

test("an empty view still shows AWS, GitHub Apps and Stored values, and no legacy or token group", () => {
  assert.deepStrictEqual(credGroups(view({})).map((g) => g.key), ["aws", "github", "values"]);
  assert.deepStrictEqual(credGroups(null).map((g) => g.key), ["aws", "github", "values"]);
});

test("token providers sort by their shown title, not their raw id", () => {
  const gs = credGroups(view({ tokens: [tok("a", "pagerduty"), tok("b", "acme")] }));
  assert.deepStrictEqual(gs.filter((g) => g.key.startsWith("token:")).map((g) => g.title), ["Acme", "PagerDuty"]);
});

test("a token without a provider lands in 'Other tokens'", () => {
  const gs = credGroups(view({ tokens: [tok("x", "")] }));
  assert.strictEqual(gs.find((g) => g.key.startsWith("token:")).title, "Other tokens");
  assert.strictEqual(providerTitle("github"), "GitHub");
});

test("statusText has the five exact texts", () => {
  assert.strictEqual(statusText("ok"), "Ready");
  assert.strictEqual(statusText("missing"), "Missing");
  assert.strictEqual(statusText("stored_locked"), "Stored (store locked)");
  assert.strictEqual(statusText("invalid"), "Invalid");
  assert.strictEqual(statusText("unset"), "Not set in the daemon's environment");
  assert.strictEqual(statusText("weird"), "Invalid");
});

test("baseText: writes is bad with the exact D4 sentence", () => {
  assert.deepStrictEqual(baseText({ state: "writes", actions: ["s3:PutObject"] }), {
    cls: "bad", text: "The agent will be able to change this AWS account: create, modify and delete resources.",
  });
  assert.strictEqual(CRED_D4, "The agent will be able to change this AWS account: create, modify and delete resources.");
});

test("baseText: none_found is ok and never says a bare 'read-only'", () => {
  assert.deepStrictEqual(baseText({ state: "none_found" }), { cls: "ok", text: "No broad write access found" });
  const all = [
    baseText({ state: "writes" }), baseText({ state: "none_found" }),
    baseText({ state: "unverified", reason: "sso_expired", reason_text: "SSO expired" }),
    baseText({ state: "unverified", reason: "not_checked", reason_text: "not checked" }),
    baseText(null), baseText({}),
  ];
  for (const r of all) assert.ok(!/read-?\s?only/i.test(r.text), r.text);
});

test("baseText: unverified is warn with the reason text; not_checked reads 'Not checked'", () => {
  assert.deepStrictEqual(baseText({ state: "unverified", reason: "sso_expired", reason_text: "SSO expired" }), { cls: "warn", text: "Not verified: SSO expired" });
  assert.deepStrictEqual(baseText({ state: "unverified", reason: "not_checked", reason_text: "not checked" }), { cls: "warn", text: "Not checked" });
  assert.deepStrictEqual(baseText({ state: "unverified", reason: "timeout" }), { cls: "warn", text: "Not verified" });
  assert.deepStrictEqual(baseText(null), { cls: "warn", text: "Not checked" });
});

test("credWriteActions lists the write actions only for a writes verdict", () => {
  assert.deepStrictEqual(credWriteActions({ state: "writes", actions: ["s3:PutObject", "iam:CreateUser"] }), ["s3:PutObject", "iam:CreateUser"]);
  assert.deepStrictEqual(credWriteActions({ state: "none_found", actions: ["x"] }), []);
  assert.deepStrictEqual(credWriteActions(null), []);
});

test("usedByText", () => {
  assert.strictEqual(usedByText([]), "nothing");
  assert.strictEqual(usedByText(null), "nothing");
  assert.strictEqual(usedByText(["a", "b"]), "a, b");
});

test("seeds from config.json have no actions; web entries get edit and delete", () => {
  assert.strictEqual(isSeed({ source: "config" }), true);
  assert.deepStrictEqual(entryActions({ kind: "token", source: "config" }), []);
  assert.deepStrictEqual(entryActions({ kind: "aws", source: "config" }), []);
  assert.deepStrictEqual(entryActions({ kind: "github", source: "config" }), []);
  assert.deepStrictEqual(entryActions({ kind: "token", source: "web" }), ["edit", "delete"]);
  assert.deepStrictEqual(entryActions({ kind: "legacy", name: "x" }), []);
});

test("the honest N3 line and the file-bad banner are exact", () => {
  assert.strictEqual(CRED_N3, "The agent can read this token. It is long-lived and is not revoked when the session ends.");
  assert.strictEqual(CRED_FILE_BAD, "credentials.json could not be read; it will be kept as a .bad copy on your next save");
});

test("checkedText: today shows the time, earlier shows the date, none is empty", () => {
  const now = new Date(2026, 9, 20, 18, 0, 0);
  assert.strictEqual(checkedText(new Date(2026, 9, 20, 14, 2, 0).toISOString(), now), "checked 14:02 today");
  assert.strictEqual(checkedText(new Date(2026, 9, 18, 9, 5, 0).toISOString(), now), "checked 2026-10-18");
  assert.strictEqual(checkedText("", now), "");
  assert.strictEqual(checkedText(null, now), "");
  assert.strictEqual(checkedText("garbage", now), "");
});

test("credRoleName is the part after role/ in an ARN", () => {
  assert.strictEqual(credRoleName("arn:aws:iam::226198813800:role/agentbox-base"), "agentbox-base");
  assert.strictEqual(credRoleName("arn:aws:iam::226198813800:role/path/to/r"), "path/to/r");
  assert.strictEqual(credRoleName(""), "");
  assert.strictEqual(credRoleName("nonsense"), "nonsense");
});

test("value rows: referenced-by and updated text", () => {
  assert.strictEqual(valueRefsText([]), "nothing");
  assert.strictEqual(valueRefsText(["static_tokens/sentry"]), "token sentry");
  assert.strictEqual(valueRefsText(["github_apps/bot", "accounts/coding.token_secret", "agents/ops.secrets"]), "GitHub App bot, account coding, profile ops");
  assert.strictEqual(valueRefsText(["something/else"]), "something/else");
  assert.strictEqual(valueUpdatedText("2026-10-14T09:00:00Z"), "2026-10-14");
  assert.strictEqual(valueUpdatedText(null), "");
});

test("awsConnName: lowercases into the entry-name shape", () => {
  assert.strictEqual(awsConnName("Prod_Admin"), "prod-admin");
  assert.strictEqual(awsConnName("_-Sand.box"), "sand-box");
  assert.strictEqual(awsConnName("a".repeat(80)), "a".repeat(63));
  assert.strictEqual(awsConnName("9lives"), "9lives");
  assert.strictEqual(awsConnName("---"), "");
  assert.strictEqual(awsConnName(null), "");
  for (const n of ["Prod_Admin", "x y z", "UPPER.case"]) assert.match(awsConnName(n), /^[a-z0-9][a-z0-9-]{0,62}$/);
});

test("awsRowFromProfile: assume-role copies the role and its source profile", () => {
  const r = awsRowFromProfile({ name: "Prod_Admin", kind: "assume-role", role_arn: "arn:aws:iam::481203957716:role/ops-admin", source_profile: "base-user", region: "ap-southeast-1", selectable: true });
  assert.deepStrictEqual(r, {
    name: "prod-admin", source_profile: "base-user", role_arn: "arn:aws:iam::481203957716:role/ops-admin",
    region: "ap-southeast-1", label: "prod-admin", prod: true, needsRole: false,
  });
});

test("awsRowFromProfile: sso starts from the account's role/ prefix and needs a role", () => {
  const r = awsRowFromProfile({ name: "sandbox", kind: "sso", sso_account_id: "226198813800", sso_role_name: "Lab", region: "ap-southeast-1", selectable: true });
  assert.deepStrictEqual(r, {
    name: "sandbox", source_profile: "sandbox", role_arn: "arn:aws:iam::226198813800:role/",
    region: "ap-southeast-1", label: "sandbox", prod: false, needsRole: true,
  });
});

test("awsRowFromProfile: any other kind signs as itself and needs a role; prod is a case-insensitive name match", () => {
  const r = awsRowFromProfile({ name: "PRODuction", kind: "other", selectable: true });
  assert.strictEqual(r.source_profile, "PRODuction");
  assert.strictEqual(r.role_arn, "");
  assert.strictEqual(r.region, "");
  assert.strictEqual(r.needsRole, true);
  assert.strictEqual(r.prod, true);
  assert.strictEqual(awsRowFromProfile({ name: "dev", kind: "other" }).prod, false);
  assert.strictEqual(awsRowFromProfile({ name: "x".repeat(60), kind: "other" }).label.length, 40);
});

test("rowSaveState: saving needs an ok assume; writes also needs the ack", () => {
  assert.deepStrictEqual(rowSaveState({}), { canSave: false, needsAck: false });
  assert.deepStrictEqual(rowSaveState({ assume: null }), { canSave: false, needsAck: false });
  assert.deepStrictEqual(rowSaveState({ assume: "ok", verdict: "none_found" }), { canSave: true, needsAck: false });
  assert.deepStrictEqual(rowSaveState({ assume: "ok", verdict: "unverified" }), { canSave: true, needsAck: false });
  assert.deepStrictEqual(rowSaveState({ assume: "ok", verdict: "writes", acked: false }), { canSave: false, needsAck: true });
  assert.deepStrictEqual(rowSaveState({ assume: "ok", verdict: "writes" }), { canSave: false, needsAck: true });
  assert.deepStrictEqual(rowSaveState({ assume: "ok", verdict: "writes", acked: true }), { canSave: true, needsAck: false });
  for (const a of ["denied", "sso_expired", "error"]) {
    assert.deepStrictEqual(rowSaveState({ assume: a, verdict: "writes", acked: true }), { canSave: false, needsAck: false });
  }
});

test("rowSaveState: an edit that leaves the role and profile alone needs no check", () => {
  assert.deepStrictEqual(rowSaveState({ unchanged: true }), { canSave: true, needsAck: false });
  // changed: it falls back to the check rules
  assert.deepStrictEqual(rowSaveState({ unchanged: false }), { canSave: false, needsAck: false });
});

test("awsSplitList splits on lines, commas and spaces and drops blanks", () => {
  assert.deepStrictEqual(awsSplitList("a.example.com,\n b.example.com  *.c.io\n\n"), ["a.example.com", "b.example.com", "*.c.io"]);
  assert.deepStrictEqual(awsSplitList(""), []);
  assert.deepStrictEqual(awsSplitList(null), []);
});

test("awsRoleARN expands a bare role name with the SSO account, and leaves an ARN alone", () => {
  assert.strictEqual(awsRoleARN("226198813800", "agentbox-agent"), "arn:aws:iam::226198813800:role/agentbox-agent");
  assert.strictEqual(awsRoleARN("226198813800", "  path/r "), "arn:aws:iam::226198813800:role/path/r");
  assert.strictEqual(awsRoleARN("226198813800", "arn:aws:iam::1:role/x"), "arn:aws:iam::1:role/x");
  assert.strictEqual(awsRoleARN("", "agentbox-agent"), "agentbox-agent");
  assert.strictEqual(awsRoleARN("226198813800", ""), "");
  assert.strictEqual(awsRoleARN("226198813800", "not a role!"), "not a role!");
});

test("awsConnFromRow sends only what is set", () => {
  const row = { name: "sandbox", label: "Lab", source_profile: "sandbox", role_arn: "arn:aws:iam::1:role/r", region: "ap-southeast-1", prod: false, egress: "a.example.com\nb.example.com", s3: "", elevRole: "", elevMinutes: "60", elevIAM: false };
  assert.deepStrictEqual(awsConnFromRow(row), {
    name: "sandbox", label: "Lab", source_profile: "sandbox", role_arn: "arn:aws:iam::1:role/r", region: "ap-southeast-1",
    egress_allow: ["a.example.com", "b.example.com"],
  });
  const full = awsConnFromRow({ ...row, prod: true, description: "d", s3: "bucket-a", elevRole: "arn:aws:iam::1:role/w", elevMinutes: "30", elevIAM: true });
  assert.strictEqual(full.prod, true);
  assert.strictEqual(full.description, "d");
  assert.deepStrictEqual(full.s3_read, ["bucket-a"]);
  assert.deepStrictEqual(full.elevate, { role_arn: "arn:aws:iam::1:role/w", max_minutes: 30, allow_iam: true });
});

test("awsCheckView: ok + none_found says 'No broad write access found' and nothing about read-only", () => {
  const v = awsCheckView({ assume: "ok", verdict: "none_found", actions: [] });
  assert.strictEqual(v.assume, "ok");
  assert.strictEqual(v.verdict.cls, "ok");
  assert.strictEqual(v.verdict.text, "No broad write access found");
  assert.strictEqual(v.needsAck, false);
});

test("awsCheckView: writes carries the exact D4 sentence, the actions and the remedy, and wants the ack", () => {
  const v = awsCheckView({ assume: "ok", verdict: "writes", actions: ["s3:PutObject", "iam:CreateUser"] });
  assert.strictEqual(v.verdict.cls, "bad");
  assert.strictEqual(v.verdict.text, CRED_D4);
  assert.deepStrictEqual(v.actions, ["s3:PutObject", "iam:CreateUser"]);
  assert.strictEqual(v.needsAck, true);
  assert.strictEqual(CRED_D4_REMEDY, "Use a role without write access as the base and put this one under Elevate.");
});

test("awsCheckView: unverified is amber and needs no ack; the simulate failure is not 'could not assume'", () => {
  const u = awsCheckView({ assume: "ok", verdict: "unverified", reason: "timeout", reason_text: "the check timed out" });
  assert.strictEqual(u.verdict.cls, "warn");
  assert.match(u.verdict.text, /Not verified/);
  assert.strictEqual(u.needsAck, false);
  const e = awsCheckView({ assume: "error", verdict: "unverified", reason: "x", reason_text: "something broke" });
  assert.strictEqual(e.badge, "Check not finished");
  assert.match(e.text, /Could not finish the check/);
  assert.match(e.text, /something broke/);
  assert.doesNotMatch(e.text, /could not assume/i);
});

test("awsCheckView: denied explains the trust, sso_expired offers sign-in; neither says bare 'read-only'", () => {
  const d = awsCheckView({ assume: "denied", verdict: "unverified", reason: "assume_denied" });
  assert.strictEqual(d.badge, "Trust missing");
  assert.match(d.text, /role without write access/);
  assert.match(d.text, /agent-box does not create it/);
  const s = awsCheckView({ assume: "sso_expired", verdict: "unverified" });
  assert.strictEqual(s.badge, "SSO expired");
  assert.strictEqual(s.signIn, true);
  for (const v of [d, s, awsCheckView({ assume: "ok", verdict: "none_found" }), awsCheckView({ assume: "error" }), awsCheckView(null)]) {
    assert.ok(!/read-?\s?only/i.test(v.text + " " + (v.verdict ? v.verdict.text : "")));
  }
});

test("awsErrText: rate limit, no broker, exists, in_use, plain", () => {
  assert.strictEqual(awsErrText({ status: 429, retryAfter: "7", message: "x" }), "Too many AWS calls, try again in 7 s");
  assert.strictEqual(awsErrText({ status: 429, message: "x" }), "Too many AWS calls, try again in a few seconds");
  assert.match(awsErrText({ status: 409, code: "no_broker", message: "x" }), /no AWS signer/i);
  assert.match(awsErrText({ status: 409, code: "exists", message: "dup" }), /already exists/);
  assert.match(awsErrText({ status: 409, code: "seed", message: "x" }), /config\.json/);
  assert.strictEqual(awsErrText({ status: 409, code: "in_use", message: "x", body: { used_by: ["p1", "p2"] } }), "Used by profile p1, p2. Remove it there first.");
  assert.strictEqual(awsErrText({ status: 400, message: "bad region" }), "bad region");
  assert.strictEqual(awsErrText(null), "Something went wrong");
});

// The page's scripts share ONE global scope: a function declared twice means the
// later file silently wins (this file's baseActions and roleName were once
// shadowed by sessionrow.js, and nothing failed). lint.sh cannot see that.
test("no top-level name in credentials.js is also declared by another page script", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const dir = path.join(__dirname, "..", "static");
  const decl = /^(?:async\s+)?(?:function\s+|const\s+|let\s+|var\s+)([A-Za-z_$][\w$]*)/gm;
  const names = (src) => new Set([...src.matchAll(decl)].map((m) => m[1]));
  const mine = names(fs.readFileSync(path.join(dir, "credentials.js"), "utf8"));
  assert.ok(mine.size > 10);
  const clashes = [];
  for (const f of fs.readdirSync(dir).filter((x) => x.endsWith(".js") && x !== "credentials.js")) {
    for (const n of names(fs.readFileSync(path.join(dir, f), "utf8"))) if (mine.has(n)) clashes.push(n + " (" + f + ")");
  }
  assert.deepStrictEqual(clashes, []);
});
