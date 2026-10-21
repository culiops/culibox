"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  credGroups, statusText, baseText, credWriteActions, usedByText, entryActions, isSeed,
  providerTitle, checkedText, credRoleName, valueRefsText, valueUpdatedText, CRED_D4, CRED_N3, CRED_FILE_BAD,
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
