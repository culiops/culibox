"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  credGroups, statusText, baseText, credWriteActions, usedByText, entryActions, isSeed,
  providerTitle, checkedText, credRoleName, valueRefsText, valueUpdatedText, CRED_D4, CRED_N3, CRED_FILE_BAD,
  awsConnName, awsRowFromProfile, rowSaveState, awsSplitList, awsConnFromRow, awsRoleARN, awsCheckView, awsErrText, CRED_D4_REMEDY,
  templateForm, fromTemplateBody, valueFieldCleared, credUniqueName, credKeep, credState, credErrText,
  tokenEditBody, appBody, storeCreateCheck, valueIdsText, CRED_SECRET_KEYS, ghKeyID, awsSSOTimedOut, credTemplateSuffixes,
  credTableRows, credTableHead,
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
  assert.match(d.text, /Culibox does not create it/);
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


// --- token templates, GitHub Apps, stored values, store creation -----------------

const TPLS = {
  sentry: { id: "sentry", provider: "sentry", label: "Sentry", group: "tokens", values: ["Auth token"], fields: [
    { key: "region", label: "Region", kind: "select", options: [{ value: "us", label: "US" }, { value: "eu", label: "EU" }, { value: "self", label: "Self-hosted" }] },
    { key: "url", label: "Self-hosted URL", kind: "url" },
  ] },
  datadog: { id: "datadog", provider: "datadog", label: "Datadog", group: "tokens", values: ["API key", "Application key"], fields: [
    { key: "site", label: "Site", kind: "select", options: [{ value: "datadoghq.com", label: "US1" }, { value: "datadoghq.eu", label: "EU" }] },
  ] },
  other: { id: "other", provider: "other", label: "Other", group: "tokens", values: ["Token"], fields: [
    { key: "provider", label: "Provider name", kind: "text" },
    { key: "env", label: "Variable name(s)", kind: "text" },
    { key: "hosts", label: "Host(s)", kind: "text" },
  ] },
  "github-pat": { id: "github-pat", provider: "github", label: "GitHub personal access token", group: "github", values: ["Personal access token"], fields: [] },
};

test("templateForm: select defaults to its first option, sentry's url shows only for self-hosted, values keep the template's order", () => {
  const f = templateForm(TPLS.sentry);
  assert.deepStrictEqual(f.fields.map((x) => [x.key, x.kind, x.def]), [["region", "select", "us"], ["url", "url", ""]]);
  assert.deepStrictEqual(f.fields[1].when, { key: "region", value: "self" });
  assert.strictEqual(f.fields[0].when, undefined);
  assert.deepStrictEqual(f.values, [{ index: 0, label: "Auth token" }]);
  const d = templateForm(TPLS.datadog);
  assert.deepStrictEqual(d.values, [{ index: 0, label: "API key" }, { index: 1, label: "Application key" }]);
  assert.strictEqual(d.defaultName, "datadog");
  assert.strictEqual(templateForm(TPLS.other).defaultName, "");
  assert.deepStrictEqual(templateForm(TPLS["github-pat"]).fields, []);
  assert.deepStrictEqual(templateForm(null).fields, []);
});

test("fromTemplateBody: datadog sends its two values in the template's order", () => {
  const body = fromTemplateBody(TPLS.datadog, { name: " dd ", label: "Datadog prod", prod: true, fields: { site: "datadoghq.eu" }, values: ["api-key-value", "app-key-value"] });
  assert.deepStrictEqual(body, {
    template: "datadog", name: "dd", label: "Datadog prod", prod: true,
    fields: { site: "datadoghq.eu" }, values: ["api-key-value", "app-key-value"],
  });
});

test("fromTemplateBody: a field whose condition is not met, or that the template does not have, is left out; values are not trimmed", () => {
  const body = fromTemplateBody(TPLS.sentry, { name: "sentry", label: "", prod: false, fields: { region: "us", url: "https://x.example.com", junk: "1" }, values: ["tok\n"] });
  assert.deepStrictEqual(body.fields, { region: "us" });
  assert.deepStrictEqual(body.values, ["tok\n"]);
  assert.strictEqual(body.prod, false);
  const self = fromTemplateBody(TPLS.sentry, { name: "s", fields: { region: "self", url: " https://sentry.example.com " }, values: ["t"] });
  assert.deepStrictEqual(self.fields, { region: "self", url: "https://sentry.example.com" });
});

test("fromTemplateBody: a missing select falls back to the default; missing values send as empty strings the server refuses", () => {
  const body = fromTemplateBody(TPLS.datadog, { name: "dd", fields: {}, values: ["only-one"] });
  assert.deepStrictEqual(body.fields, { site: "datadoghq.com" });
  assert.deepStrictEqual(body.values, ["only-one", ""]);
});

test("valueFieldCleared empties the field and says so", () => {
  const el = { value: "n2-dummy-secret", removeAttribute(n) { this.removed = n; } };
  assert.strictEqual(valueFieldCleared(el), true);
  assert.strictEqual(el.value, "");
  assert.strictEqual(valueFieldCleared(null), true);
});

test("credUniqueName: base when free, then base-2, base-3", () => {
  assert.strictEqual(credUniqueName("sentry", []), "sentry");
  assert.strictEqual(credUniqueName("sentry", ["sentry"]), "sentry-2");
  assert.strictEqual(credUniqueName("sentry", ["sentry", "sentry-2"]), "sentry-3");
  assert.strictEqual(credUniqueName("", ["x"]), "");
});

test("credUniqueName with suffixes: every created name must be free (Datadog <name>-api/-app)", () => {
  const sfx = credTemplateSuffixes({ id: "datadog" });
  assert.deepStrictEqual(sfx, ["-api", "-app"]);
  assert.strictEqual(credUniqueName("datadog", [], sfx), "datadog");
  assert.strictEqual(credUniqueName("datadog", ["datadog-api", "datadog-app"], sfx), "datadog-2");
  assert.strictEqual(credUniqueName("datadog", ["datadog-app"], sfx), "datadog-2");
  // The bare name is not what the server creates, so it being taken does not matter.
  assert.strictEqual(credUniqueName("datadog", ["datadog"], sfx), "datadog");
  assert.deepStrictEqual(credTemplateSuffixes({ id: "sentry" }), []);
  assert.strictEqual(credUniqueName("sentry", ["sentry"], credTemplateSuffixes({ id: "sentry" })), "sentry-2");
});

test("valueIdsText: the store ids a template's values will be stored under", () => {
  assert.strictEqual(valueIdsText(TPLS.sentry, "sentry"), "sentry");
  assert.strictEqual(valueIdsText(TPLS.datadog, "dd"), "dd-api and dd-app");
  assert.strictEqual(valueIdsText(TPLS.sentry, ""), "the token name");
  assert.strictEqual(valueIdsText(TPLS.datadog, "dd", 0), "dd-api");
  assert.strictEqual(valueIdsText(TPLS.datadog, "dd", 1), "dd-app");
  assert.strictEqual(valueIdsText(TPLS.sentry, "s", 0), "s");
});

test("module state never holds a value: credKeep strips every secret key, deeply", () => {
  const kept = credKeep({
    kind: "token-add", name: "sentry", label: "x",
    values: ["n2-dummy-aaa"], value: "n2-dummy-bbb", private_key_value: "n2-dummy-ccc", passphrase: "n2-dummy-ddd",
    key: "n2-dummy-ggg",
    nested: { value: "n2-dummy-eee", ok: 1 }, list: [{ private_key_value: "n2-dummy-fff", id: 2 }],
  });
  assert.strictEqual(credState().form, kept);
  const dump = JSON.stringify(credState());
  assert.ok(!dump.includes("n2-dummy"), dump);
  assert.strictEqual(kept.name, "sentry");
  assert.strictEqual(kept.nested.ok, 1);
  assert.strictEqual(kept.list[0].id, 2);
  for (const k of ["values", "value", "private_key_value", "passphrase", "key"]) assert.ok(CRED_SECRET_KEYS.includes(k));
  credKeep(null);
  assert.strictEqual(credState().form, null);
});

test("credState().view is the read model, which has no value in it", () => {
  assert.ok(!JSON.stringify(credState()).includes("n2-dummy"));
});

test("tokenEditBody changes only label, description and prod; the rest of the entry goes back as it was", () => {
  const entry = { name: "sentry", value: { type: "store", id: "sentry" }, env: ["SENTRY_AUTH_TOKEN"], egress: [{ host: "sentry.io" }], provider: "sentry", template: "sentry", label: "old", prod: false };
  const b = tokenEditBody(entry, { label: " new ", description: "d", prod: true });
  assert.deepStrictEqual(b.entry, { ...entry, label: "new", description: "d", prod: true });
  assert.strictEqual("value" in b, false);
  assert.notStrictEqual(b.entry, entry);
  assert.strictEqual(entry.label, "old");
  const c = tokenEditBody(entry, { label: "", description: "", prod: false, value: "v2" });
  assert.strictEqual(c.value, "v2");
  assert.strictEqual(c.entry.label, undefined);
  assert.strictEqual(c.entry.prod, undefined);
  assert.deepStrictEqual(c.entry.value, { type: "store", id: "sentry" });
});

test("appBody: pasted key goes as private_key_value, a file path as a file reference, neither keeps the key", () => {
  assert.deepStrictEqual(appBody({ name: " bot ", client_id: " Iv1.abc ", key: "-----BEGIN-----\nx\n" }),
    { app: { name: "bot", client_id: "Iv1.abc" }, private_key_value: "-----BEGIN-----\nx\n" });
  assert.deepStrictEqual(appBody({ name: "bot", client_id: "Iv1.abc", useFile: true, path: " /home/x/key.pem ", key: "ignored" }),
    { app: { name: "bot", client_id: "Iv1.abc", private_key: { type: "file", path: "/home/x/key.pem" } } });
  assert.deepStrictEqual(appBody({ name: "bot", client_id: "Iv1.abc" }), { app: { name: "bot", client_id: "Iv1.abc" } });
});

test("storeCreateCheck: Linux needs the passphrase twice and 12 characters; macOS needs nothing", () => {
  assert.deepStrictEqual(storeCreateCheck(true, "", ""), { ok: false, msg: "Type a passphrase." });
  assert.strictEqual(storeCreateCheck(true, "short", "short").ok, false);
  assert.match(storeCreateCheck(true, "short", "short").msg, /at least 12 characters/);
  assert.deepStrictEqual(storeCreateCheck(true, "a-long-enough-pass", "a-long-enough-pasz"), { ok: false, msg: "The two passphrases do not match." });
  assert.deepStrictEqual(storeCreateCheck(true, "a-long-enough-pass", "a-long-enough-pass"), { ok: true, msg: "" });
  assert.deepStrictEqual(storeCreateCheck(false, "", ""), { ok: true, msg: "" });
  // counted in characters, not bytes
  assert.strictEqual(storeCreateCheck(true, "ééééééééééé", "ééééééééééé").ok, false);
  assert.strictEqual(storeCreateCheck(true, "éééééééééééé", "éééééééééééé").ok, true);
});

test("credErrText: specific refusals read in words, the server's sentence otherwise", () => {
  assert.strictEqual(credErrText({ status: 429, retryAfter: "7", message: "x" }, "token"), "Too many requests, try again in 7 s");
  assert.strictEqual(credErrText({ status: 409, code: "exists", message: "x" }, "token"), "A token with this name already exists.");
  assert.strictEqual(credErrText({ status: 409, code: "seed", message: "x" }, "GitHub App"), "That name belongs to an entry in config.json.");
  assert.match(credErrText({ status: 409, code: "store_missing", message: "x" }, "token"), /not created yet/);
  assert.strictEqual(credErrText({ status: 413, message: "the value is larger than 64 KiB" }, "value"), "the value is larger than 64 KiB");
  assert.strictEqual(credErrText({ status: 409, code: "in_use", message: "x", body: { used_by: ["static_tokens/sentry", "agents/p.secrets"] } }, "value"),
    "Still used by token sentry, profile p. Remove those first.");
  assert.strictEqual(credErrText({ status: 409, code: "in_use", message: "x", body: { used_by: ["p1"] } }, "token"), "Used by profile p1. Remove it there first.");
  assert.strictEqual(credErrText({ status: 400, message: "the value is empty" }, "token"), "the value is empty");
  assert.strictEqual(credErrText(null, "token"), "Something went wrong");
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

test("ghKeyID mirrors the server: odd characters become -, the id stays within 64", () => {
  assert.strictEqual(ghKeyID("bot"), "gh-bot-key");
  assert.strictEqual(ghKeyID("my.app"), "gh-my-app-key");
  const long = ghKeyID("a".repeat(64));
  assert.strictEqual(long, "gh-" + "a".repeat(57) + "-key");
  assert.strictEqual(long.length, 64);
  assert.ok(/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(ghKeyID(".".repeat(64))));
});

test("a timed-out sign-in goes back to the Sign in state with a note", () => {
  const s = awsSSOTimedOut();
  assert.strictEqual(s.state, "gone");
  assert.ok(/timed out/i.test(s.msg));
});

// --- Settings page: one table per provider (desktop only) ---------------------

const awsE = (name, extra) => ({
  name, kind: "aws", account_id: "226198813800", role_arn: "arn:aws:iam::226198813800:role/CuliShopLabAccess",
  region: "ap-southeast-1", status: "ok", source: "web", prod: false, used_by: [], base: null, ...extra,
});

test("credTableHead: the columns of each provider table, then the actions column", () => {
  assert.deepStrictEqual(credTableHead("aws"), ["Connection", "Account · role", "SSO", "Write probe", "Used by", ""]);
  assert.deepStrictEqual(credTableHead("github"), ["App", "Client ID · key", "Status", "Used by", ""]);
  assert.deepStrictEqual(credTableHead("token:datadog"), ["Token", "Env · agent may reach", "Status", "Used by", ""]);
});

test("credTableRows aws: PROD with elevate, no elevation, and a can-write row marked danger", () => {
  const g = credGroups(view({
    aws: [
      awsE("culipulse-prod", {
        prod: true, role_arn: "arn:aws:iam::402100009917:role/agent-investigate", account_id: "402100009917",
        elevate: { role_arn: "arn:aws:iam::402100009917:role/agent-ops", max_minutes: 60 },
        used_by: ["a", "b"], base: { state: "none_found", checked_at: "2020-01-02T03:04:00Z" },
      }),
      awsE("sandbox", { status: "missing" }),
      awsE("wr", { base: { state: "writes", actions: ["s3:PutObject"], checked_at: "2020-01-02T03:04:00Z" } }),
    ],
  })).find((x) => x.key === "aws");
  const rows = credTableRows(g);
  assert.strictEqual(rows.length, 3);
  const [a, b, c] = rows;
  assert.strictEqual(a.cells[0].text, "culipulse-prod");
  assert.strictEqual(a.cells[0].tag, "PROD");
  assert.strictEqual(a.cells[0].sub, "elevate: agent-ops · max 60m");
  assert.deepStrictEqual([a.cells[1].text, a.cells[1].sub, a.cells[1].mono], ["402100009917", "agent-investigate", true]);
  assert.deepStrictEqual([a.cells[2].text, a.cells[2].cls], ["Ready", "ready"]);
  assert.deepStrictEqual([a.cells[3].text, a.cells[3].cls], ["No broad write access found", "ok"]);
  assert.ok(/^checked 2020-01-0[123]$/.test(a.cells[3].sub), a.cells[3].sub);
  assert.strictEqual(a.cells[4].text, "a, b");
  assert.strictEqual(a.danger, false);
  assert.strictEqual(a.entry.name, "culipulse-prod");
  // Not prod, no elevation; a missing connection is a "needs you" status and has no probe to show.
  assert.strictEqual(b.cells[0].tag, "");
  assert.strictEqual(b.cells[0].sub, "no elevation");
  assert.deepStrictEqual([b.cells[2].text, b.cells[2].cls], ["Missing", "missing"]);
  assert.strictEqual(b.cells[3].text, "–");
  assert.strictEqual(b.cells[4].text, "nothing");
  assert.strictEqual(b.danger, false);
  // can write: red verdict, short text (the full D4 sentence is the warnbox under the row), row is danger.
  assert.deepStrictEqual([c.cells[3].text, c.cells[3].cls], ["Can write", "bad"]);
  assert.strictEqual(c.danger, true);
});

test("credTableRows aws: an elevate role without a max just names the role", () => {
  const g = { key: "aws", entries: [awsE("x", { elevate: { role_arn: "arn:aws:iam::1:role/ops" } })] };
  assert.strictEqual(credTableRows(g)[0].cells[0].sub, "elevate: ops");
});

test("credTableRows github: app, client id with key kind, status, used by", () => {
  const g = credGroups(view({ github_apps: [{ name: "culibox-agents", client_id: "Iv23li", key_ref_kind: "store", status: "ok", source: "web", used_by: ["p1"] }] })).find((x) => x.key === "github");
  const [r] = credTableRows(g);
  assert.strictEqual(r.cells.length, 4);
  assert.strictEqual(r.cells[0].text, "culibox-agents");
  assert.deepStrictEqual([r.cells[1].text, r.cells[1].sub, r.cells[1].mono], ["Iv23li", "store", true]);
  assert.deepStrictEqual([r.cells[2].text, r.cells[2].cls], ["Ready", "ready"]);
  assert.strictEqual(r.cells[3].text, "p1");
  assert.strictEqual(r.danger, false);
});

test("credTableRows token: name with label, env with hosts, status, used by; a missing value is flagged", () => {
  const g = credGroups(view({ tokens: [
    tok("datadog-read", "datadog", { label: "Datadog", env: ["DD_API_KEY", "DD_APP_KEY"], hosts: ["api.datadoghq.eu"], used_by: ["p"], description: "read only" }),
    tok("cf", "cloudflare", { status: "missing" }),
  ] })).find((x) => x.key === "token:datadog");
  const [r] = credTableRows(g);
  assert.strictEqual(r.cells.length, 4);
  assert.deepStrictEqual([r.cells[0].text, r.cells[0].sub, r.cells[0].title], ["datadog-read", "Datadog", "read only"]);
  assert.deepStrictEqual([r.cells[1].text, r.cells[1].sub, r.cells[1].mono, r.cells[1].subMono], ["DD_API_KEY, DD_APP_KEY", "api.datadoghq.eu", true, true]);
  assert.deepStrictEqual([r.cells[2].text, r.cells[2].cls], ["Ready", "ready"]);
  const g2 = credGroups(view({ tokens: [tok("cf", "cloudflare", { status: "missing" })] })).find((x) => x.key === "token:cloudflare");
  assert.deepStrictEqual([credTableRows(g2)[0].cells[2].text, credTableRows(g2)[0].cells[2].cls], ["Missing", "missing"]);
});

test("table mode keys on the as-page class, and the card path never looks at it", () => {
  const fs = require("node:fs");
  const src = fs.readFileSync(require("node:path").join(__dirname, "..", "static", "credentials.js"), "utf8");
  const body = (name) => {
    const i = src.indexOf("function " + name + "(");
    assert.ok(i >= 0, name);
    return src.slice(i, src.indexOf("\n}\n", i));
  };
  assert.ok(/as-page/.test(body("credTableMode")));
  assert.ok(!/innerWidth|matchMedia/.test(body("credTableMode")), "table mode is not width-based");
  for (const n of ["credAWSCard", "credAppCard", "credTokenCard", "credCard"]) assert.ok(!/credTableMode/.test(body(n)), n);
  assert.ok(/credTableMode\(\)/.test(body("credSection")));
});

// Desktop Credentials table: a status cell wraps between words, never inside
// one, so a long status ("Stored (store locked)", "Not set in the daemon's
// environment") stays inside its column instead of printing over Used by.
test("desktop creds table: status wraps at word boundaries only", () => {
  const css = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "desktop.css"), "utf8");
  const rule = css.match(/#creds \.ctab \.st \{([^}]*)\}/);
  assert.ok(rule, "a #creds .ctab .st rule exists");
  assert.match(rule[1], /white-space:\s*normal/);
  assert.match(rule[1], /overflow-wrap:\s*break-word/);
  assert.doesNotMatch(rule[1], /nowrap|anywhere/);
});

// The fixed-layout tables size every column as a share of the table: a px
// column left Used by 0px wide in a ~1000px window.
test("desktop creds table: fixed-layout column widths are percentages", () => {
  const css = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "desktop.css"), "utf8");
  const widths = [...css.matchAll(/\.ctab-sec:not\(\[data-group="aws"\]\) \.ctab th:nth-child\(\d\) \{ width: ([^;]+); \}/g)].map((m) => m[1]);
  assert.ok(widths.length >= 4, "column widths are declared");
  for (const w of widths) assert.match(w, /^\d+%$/);
  assert.ok(widths.reduce((n, w) => n + parseInt(w, 10), 0) < 100, "Used by keeps a share");
  assert.match(css, /\.ctab-sec:not\(\[data-group="aws"\]\) \.ctab th \{ white-space: normal;/, "headers wrap in share-sized columns");
});
