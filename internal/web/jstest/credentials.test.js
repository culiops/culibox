"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  credGroups, statusText, statusClass, baseText, credWriteActions, usedByText, entryActions, isSeed,
  providerTitle, checkedText, credRoleName, valueRefsText, valueUpdatedText, CRED_D4, CRED_N3, CRED_FILE_BAD,
  awsConnName, awsRowFromProfile, rowSaveState, awsSplitList, awsConnFromRow, awsRoleARN, awsCheckView, awsErrText, CRED_D4_REMEDY,
  templateForm, fromTemplateBody, valueFieldCleared, credUniqueName, credKeep, credState, credErrText,
  tokenEditBody, appBody, storeCreateCheck, valueIdsText, CRED_SECRET_KEYS, ghKeyID, awsSSOTimedOut, credTemplateSuffixes,
  credTableRows, credTableHead, awsRolesShown, awsAgentRolePick, awsRolesNote, AWS_AGENT_ROLE_DOC,
  awsGateView, awsGateBoxEmpty, awsModeText, awsAckBody, awsHoursFromExpiry, awsDataReadName, awsRowStateFor,
  GH_ROUTE_CHOICES, GH_PAT_SUGGESTION, ghPatValueWarning, GH_APP_HELP, credDeleteText, credColumnCount,
  credTablesAt, AWS_REGIONS, awsRegionSuggest, awsProfileSuggest, credGroupIcon,
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
  assert.strictEqual(statusText("stored_unavailable"), "Stored (Keychain unavailable)");
  assert.strictEqual(statusClass("stored_unavailable"), "locked");
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
  assert.strictEqual(CRED_FILE_BAD, "credentials.json has content this CuliBox cannot read, so this screen will not save over it. Fix the file, or run the CuliBox that wrote it.");
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
  assert.strictEqual(credRoleName("arn:aws:iam::210987654321:role/agentbox-base"), "agentbox-base");
  assert.strictEqual(credRoleName("arn:aws:iam::210987654321:role/path/to/r"), "path/to/r");
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
  const r = awsRowFromProfile({ name: "Prod_Admin", kind: "assume-role", role_arn: "arn:aws:iam::444455556666:role/ops-admin", source_profile: "base-user", region: "ap-southeast-1", selectable: true });
  assert.deepStrictEqual(r, {
    name: "prod-admin", source_profile: "base-user", role_arn: "arn:aws:iam::444455556666:role/ops-admin",
    region: "ap-southeast-1", label: "prod-admin", prod: true, needsRole: false,
  });
});

test("awsRowFromProfile: sso starts from the account's role/ prefix and needs a role", () => {
  const r = awsRowFromProfile({ name: "sandbox", kind: "sso", sso_account_id: "210987654321", sso_role_name: "Lab", region: "ap-southeast-1", selectable: true });
  assert.deepStrictEqual(r, {
    name: "sandbox", source_profile: "sandbox", role_arn: "arn:aws:iam::210987654321:role/",
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
  assert.strictEqual(awsRoleARN("210987654321", "agentbox-agent"), "arn:aws:iam::210987654321:role/agentbox-agent");
  assert.strictEqual(awsRoleARN("210987654321", "  path/r "), "arn:aws:iam::210987654321:role/path/r");
  assert.strictEqual(awsRoleARN("210987654321", "arn:aws:iam::1:role/x"), "arn:aws:iam::1:role/x");
  assert.strictEqual(awsRoleARN("", "agentbox-agent"), "agentbox-agent");
  assert.strictEqual(awsRoleARN("210987654321", ""), "");
  assert.strictEqual(awsRoleARN("210987654321", "not a role!"), "not a role!");
});

test("the editor's read-only box-prefix hint is hidden with the other elevate fields in sso_direct mode (#211)", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/credentials.js"), "utf8");
  assert.match(src, /row\.modeHidden = \[[^\]]*\]\.concat\(boxHint \? \[boxHint\] : \[\]\)/);
  // #307: the prefix also lets the window change policies, ACLs and public access blocks there.
  assert.match(src, /"This elevate can read objects in, and change the policies, ACLs and public access blocks of, buckets named " \+ row\.elevBoxPrefix \+ "\* \(elevate\.box_bucket_prefix, set in the config file\)\."/);
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
  // #211: the form has no field for box_bucket_prefix, so it carries the saved one back unchanged.
  const boxed = awsConnFromRow({ ...row, elevRole: "arn:aws:iam::1:role/w", elevMinutes: "60", elevBoxPrefix: "culibox-" });
  assert.deepStrictEqual(boxed.elevate, { role_arn: "arn:aws:iam::1:role/w", max_minutes: 60, box_bucket_prefix: "culibox-" });
  assert.strictEqual("box_bucket_prefix" in awsConnFromRow({ ...row, elevRole: "arn:aws:iam::1:role/w" }).elevate, false);
  // #302: pass_role_prefix is an editor field, trimmed, sent only when set
  assert.deepStrictEqual(awsConnFromRow({ ...row, elevRole: "arn:aws:iam::1:role/w", elevPass: " lab- " }).elevate,
    { role_arn: "arn:aws:iam::1:role/w", max_minutes: 60, pass_role_prefix: "lab-" });
  assert.strictEqual("pass_role_prefix" in awsConnFromRow({ ...row, elevRole: "arn:aws:iam::1:role/w", elevPass: "  " }).elevate, false);
  // with no write role there is no elevate block to carry it in
  assert.strictEqual("elevate" in awsConnFromRow({ ...row, elevBoxPrefix: "culibox-" }), false);
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
  assert.match(d.text, /CuliBox does not create it/);
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
  name, kind: "aws", account_id: "210987654321", role_arn: "arn:aws:iam::210987654321:role/CuliShopLabAccess",
  region: "ap-southeast-1", status: "ok", source: "web", prod: false, used_by: [], base: null,
  signin: { state: "signed_in", until: "2030-01-02T15:04:00Z" }, ...extra,
});

test("credTableHead: the columns of each provider table, then the actions column", () => {
  assert.deepStrictEqual(credTableHead("aws"), ["Connection", "Account · role", "Sign-in", "Write probe", "Used by", ""]);
  assert.deepStrictEqual(credTableHead("github"), ["App", "Client ID · key", "Status", "Used by", ""]);
  assert.deepStrictEqual(credTableHead("token:datadog"), ["Token", "Env · agent may reach", "Status", "Used by", ""]);
});

test("credTableRows aws: PROD with elevate, no elevation, and a can-write row marked danger", () => {
  const g = credGroups(view({
    aws: [
      awsE("culipulse-prod", {
        prod: true, role_arn: "arn:aws:iam::111122223333:role/agent-investigate", account_id: "111122223333",
        elevate: { role_arn: "arn:aws:iam::111122223333:role/agent-ops", max_minutes: 60 },
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
  assert.deepStrictEqual([a.cells[1].text, a.cells[1].sub, a.cells[1].mono], ["111122223333", "agent-investigate", true]);
  assert.deepStrictEqual([a.cells[2].text, a.cells[2].cls], ["Signed in", "ready"]);
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

test("table mode needs the as-page class and room in #creds-body; the card path never looks at it", () => {
  const fs = require("node:fs");
  const src = fs.readFileSync(require("node:path").join(__dirname, "..", "static", "credentials.js"), "utf8");
  const body = (name) => {
    const i = src.indexOf("function " + name + "(");
    assert.ok(i >= 0, name);
    return src.slice(i, src.indexOf("\n}\n", i));
  };
  assert.ok(/as-page/.test(body("credPageMode")));
  assert.ok(/credPageMode\(\)/.test(body("credTableMode")));
  // The room is the body's own width (the sidebar is draggable), never the window's.
  // The rule itself is credTablesAt's (pure, and what the resize observer asks too).
  assert.ok(/creds-body/.test(body("credTableMode")) && /credTablesAt\(/.test(body("credTableMode")));
  assert.ok(/CRED_TABLE_MIN_WIDTH/.test(body("credTablesAt")));
  // ... and the content decides above that floor: the measured need goes in with the width (#27).
  assert.ok(/credNeedW/.test(body("credTableMode")), "credTableMode hands the measured need to the rule");
  assert.ok(!/innerWidth|matchMedia/.test(body("credTableMode")), "not keyed on the window width");
  for (const n of ["credAWSCard", "credAppCard", "credTokenCard", "credCard"]) assert.ok(!/credTableMode/.test(body(n)), n);
  assert.ok(/credTableMode\(\)/.test(body("credSection")));
});

// Desktop Credentials table: a status cell wraps between words, never inside
// one, so a long status ("Stored (store locked)", "Not set in the daemon's
// environment") stays inside its column instead of printing over Used by.
test("desktop creds table: status wraps at word boundaries only", () => {
  const css = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "desktop.css"), "utf8");
  const rule = css.match(/:is\(#creds, #cred-drawer\) \.ctab \.st \{([^}]*)\}/);
  assert.ok(rule, "a #creds .ctab .st rule exists (written for the drawer too)");
  assert.match(rule[1], /white-space:\s*normal/);
  assert.match(rule[1], /overflow-wrap:\s*break-word/);
  assert.doesNotMatch(rule[1], /nowrap|anywhere/);
});

// A card on the desktop page has the room of #creds-body, not of the window: at 701px
// that is 178px, and the two-column card (info | Edit Delete) left the info 18px. The
// phone's single-column card rule is applied to the page by a container query on the
// same element the table/card switch measures.
test("desktop creds cards stack when #creds-body is narrow, keyed on the body not the window", () => {
  const css = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "desktop.css"), "utf8");
  assert.match(css, /#creds-body, [^{}]*#cred-drawer-body\s*\{[^}]*container-type:\s*inline-size/, "the drawer's body is a container too");
  const q = /@container\s*\(max-width:\s*(\d+)px\)\s*\{([^@]*?\n\s*\})/g;
  let found = null, m;
  while ((m = q.exec(css))) if (/:is\(#creds, #cred-drawer\) \.ent \{[^}]*grid-template-columns:\s*1fr/.test(m[2])) found = m;
  assert.ok(found, "a @container rule stacks #creds .ent");
  const width = Number(found[1]);
  assert.ok(width > 227 && width < 700, "above the measured two-column floor (227px), below the table switch (700px)");
  assert.match(found[2], /:is\(#creds, #cred-drawer\) \.ent-act \{[^}]*align-self:\s*stretch/);
  assert.match(found[2], /:is\(#creds, #cred-drawer\) \.ent-act \.btn \{[^}]*flex:\s*1/);
});

// The fixed-layout tables size every column as a share of the table: a px
// column left Used by 0px wide in a ~1000px window. The shares are on the colgroup's <col>s, one set for the head
// cells and the rows (G16, UI plan 4 Task 5).
test("desktop creds table: fixed-layout column widths are percentages", () => {
  const css = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "desktop.css"), "utf8");
  const widths = [...css.matchAll(/\.ctab-sec:not\(\[data-group="aws"\]\) \.ctab col:nth-child\(\d\) \{ width: ([^;]+); \}/g)].map((m) => m[1]);
  assert.strictEqual(widths.length, 5, "every column of the one colgroup has a share");
  for (const w of widths) assert.match(w, /^\d+(\.\d+)?%$/);
  assert.strictEqual(Math.round(widths.reduce((n, w) => n + parseFloat(w), 0) * 10) / 10, 100, "the shares are the whole table, Used by's included");
  assert.match(css, /\.ctab-sec:not\(\[data-group="aws"\]\) \.ctab th \{ white-space: normal;/, "headers wrap in share-sized columns");
});

const roleList = (...names) => names.map((n) => ({ name: n, arn: "arn:aws:iam::210987654321:role/" + n }));

test("awsRolesShown: filters by name, case-insensitively, and caps what it renders", () => {
  const roles = roleList("agentbox-agent", "AWSServiceRoleForECS", "ecs-task-api", "Lab-reader");
  assert.deepStrictEqual(awsRolesShown(roles, "", 50).list.map((r) => r.name), roles.map((r) => r.name));
  const ecs = awsRolesShown(roles, "  ECS ", 50);
  assert.deepStrictEqual(ecs.list.map((r) => r.name), ["AWSServiceRoleForECS", "ecs-task-api"]);
  assert.strictEqual(ecs.matched, 2);
  const capped = awsRolesShown(roles, "", 2);
  assert.strictEqual(capped.list.length, 2);
  assert.strictEqual(capped.matched, 4);
  assert.strictEqual(awsRolesShown(roles, "nothing", 50).matched, 0);
});

test("awsRolesShown: the chosen role stays shown even when the filter or the cap hides it", () => {
  const roles = roleList("a", "b", "c", "d");
  const out = awsRolesShown(roles, "a", 50, roles[3].arn);
  assert.deepStrictEqual(out.list.map((r) => r.name), ["a", "d"]);
  assert.deepStrictEqual(awsRolesShown(roles, "", 2, roles[3].arn).list.map((r) => r.name), ["a", "b", "d"]);
});

test("awsAgentRolePick: preselects the one agentbox-* role over an unfinished role field", () => {
  const roles = roleList("agentbox-agent", "Admin", "ecs-task");
  assert.strictEqual(awsAgentRolePick(roles, ""), roles[0].arn);
  assert.strictEqual(awsAgentRolePick(roles, "arn:aws:iam::210987654321:role/"), roles[0].arn);
  assert.strictEqual(awsAgentRolePick(roleList("AgentBox-Agent"), ""), "arn:aws:iam::210987654321:role/AgentBox-Agent");
});

test("awsAgentRolePick: never overrides a typed role, never guesses between several", () => {
  const roles = roleList("agentbox-agent", "Admin");
  assert.strictEqual(awsAgentRolePick(roles, "arn:aws:iam::210987654321:role/Admin"), "");
  assert.strictEqual(awsAgentRolePick(roles, "Admin"), "");
  assert.strictEqual(awsAgentRolePick(roles, "arn:aws:iam::21098765432:role/Ad"), "");
  assert.strictEqual(awsAgentRolePick(roleList("agentbox-agent", "agentbox-agent-write"), ""), "");
  assert.strictEqual(awsAgentRolePick(roleList("Admin"), ""), "");
  assert.strictEqual(awsAgentRolePick([], ""), "");
});

test("awsRolesNote: an empty, capped or agent-role-less list points at the agent role doc", () => {
  assert.ok(AWS_AGENT_ROLE_DOC.endsWith("docs/aws/agent-role/README.md"));
  const empty = awsRolesNote({ roles: [] });
  assert.match(empty.text, /No roles/);
  assert.strictEqual(empty.doc, true);
  const capped = awsRolesNote({ roles: roleList("agentbox-agent"), capped: true });
  assert.match(capped.text, /first 1/);
  assert.strictEqual(capped.doc, true);
  const noAgent = awsRolesNote({ roles: roleList("Admin") });
  assert.match(noAgent.text, /agentbox-/);
  assert.strictEqual(noAgent.doc, true);
  const fine = awsRolesNote({ roles: roleList("agentbox-agent", "Admin") });
  assert.strictEqual(fine.doc, false);
  assert.match(fine.text, /type the role ARN/);
});

test("awsRolesNote: denied, failed and SSO-expired listings keep their own notes, no doc link", () => {
  assert.deepStrictEqual(awsRolesNote({ access_denied: true }), { text: "This profile can't list roles; type the role name", doc: false });
  assert.deepStrictEqual(awsRolesNote({ failed: true }), { text: "Could not list the roles. Type the role ARN yourself.", doc: false });
  assert.deepStrictEqual(awsRolesNote({ sso_expired: true }), { text: "", doc: false });
});

// --- sso_direct: the mode text, the gate verdict, the connection body ----------

const MODE_TEXT = (n) => "SSO role directly: the agent gets your permission set's own credentials, valid for up to " + n +
  " h. CloudTrail shows you, not the agent, and they cannot be revoked when the session ends. A permission set that is not read-only needs an acknowledgement and then has full access.";

test("awsModeText fills N, rounded up, and falls back to the 12 h SSO maximum", () => {
  assert.strictEqual(awsModeText(1), MODE_TEXT(1));
  assert.strictEqual(awsModeText(12), MODE_TEXT(12));
  assert.strictEqual(awsModeText(2.2), MODE_TEXT(3));
  assert.strictEqual(awsModeText(0), MODE_TEXT(12));
  assert.strictEqual(awsModeText(undefined), MODE_TEXT(12));
  assert.strictEqual(awsModeText(99), MODE_TEXT(12));
});

test("awsHoursFromExpiry: whole hours until the expiry, rounded up, 0 when unknown or past", () => {
  const now = Date.parse("2027-01-15T08:00:00Z");
  assert.strictEqual(awsHoursFromExpiry("2027-01-15T16:00:00Z", now), 8);
  assert.strictEqual(awsHoursFromExpiry("2027-01-15T08:20:00Z", now), 1);
  assert.strictEqual(awsHoursFromExpiry("", now), 0);
  assert.strictEqual(awsHoursFromExpiry("nope", now), 0);
  assert.strictEqual(awsHoursFromExpiry("2027-01-15T07:00:00Z", now), 0);
});

test("awsDataReadName maps actions to the friendly names", () => {
  assert.strictEqual(awsDataReadName("secretsmanager:GetSecretValue"), "secret values");
  assert.strictEqual(awsDataReadName("ssm:GetParametersByPath"), "SSM parameters");
  assert.strictEqual(awsDataReadName("ssm:GetParameterHistory"), "SSM parameters");
  assert.strictEqual(awsDataReadName("dynamodb:GetRecords"), "DynamoDB items");
  assert.strictEqual(awsDataReadName("DynamoDB:Scan"), "DynamoDB items");
  assert.strictEqual(awsDataReadName("s3:GetObject"), "S3 objects");
  assert.strictEqual(awsDataReadName("kms:Decrypt"), "KMS decrypt");
  assert.strictEqual(awsDataReadName("ec2:Foo"), "ec2:Foo");
});

test("awsGateView: read_only is ok and reports the data reads", () => {
  const v = awsGateView({
    state: "read_only", role_arn: "arn:aws:iam::1:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_ReadOnly_abc",
    data_reads: [
      { action: "secretsmanager:GetSecretValue", denied: true }, { action: "ssm:GetParameter", denied: true },
      { action: "dynamodb:GetItem", denied: true }, { action: "dynamodb:Scan", denied: true },
      { action: "s3:GetObject", denied: false }, { action: "kms:Decrypt", denied: false },
    ],
  });
  assert.strictEqual(v.tone, "ok");
  assert.strictEqual(v.badge, "Read-only");
  assert.strictEqual(v.signIn, false);
  assert.deepStrictEqual(v.refused, []);
  assert.deepStrictEqual(v.dataReads, { denied: ["secret values", "SSM parameters", "DynamoDB items"], readable: ["S3 objects", "KMS decrypt"] });
  assert.strictEqual(v.dataReadText, "Your permission set denies: secret values, SSM parameters, DynamoDB items. Still readable: S3 objects, KMS decrypt.");
});

test("awsGateView: a friendly name with any action still readable counts as readable", () => {
  const v = awsGateView({ state: "read_only", data_reads: [
    { action: "dynamodb:GetItem", denied: true }, { action: "dynamodb:Scan", denied: false } ] });
  assert.deepStrictEqual(v.dataReads, { denied: [], readable: ["DynamoDB items"] });
  assert.strictEqual(v.dataReadText, "Your permission set denies: nothing. Still readable: DynamoDB items.");
  assert.strictEqual(awsGateView({ state: "read_only" }).dataReadText, "");
});

test("awsGateView: not_read_only lists every refusing pattern with its reason", () => {
  const v = awsGateView({ state: "not_read_only", refused: [
    { pattern: "*", why: "allows every action" }, { Pattern: "s3:Put*", Why: "writes data" } ] });
  assert.strictEqual(v.tone, "bad");
  assert.strictEqual(v.badge, "Not read-only");
  assert.deepStrictEqual(v.refused, ["* — allows every action", "s3:Put* — writes data"]);
  assert.strictEqual(v.signIn, false);
  assert.match(v.text, /cannot be saved/);
});

test("awsGateView: unverified sso_expired offers the sign-in", () => {
  const v = awsGateView({ state: "unverified", reason: "sso_expired" });
  assert.strictEqual(v.tone, "warn");
  assert.strictEqual(v.badge, "SSO expired");
  assert.strictEqual(v.signIn, true);
});

test("awsGateView: missing_permissions lists the IAM actions to grant", () => {
  const v = awsGateView({ state: "unverified", reason: "missing_permissions", missing_perms: ["iam:GetRolePolicy", "iam:GetPolicy"] });
  assert.strictEqual(v.badge, "Cannot read its own policy");
  assert.deepStrictEqual(v.missing, ["iam:GetRolePolicy", "iam:GetPolicy"]);
  assert.strictEqual(v.signIn, false);
  assert.match(v.text, /iam:GetRolePolicy, iam:GetPolicy/);
});

test("awsGateView: missing_context labels the actions as needing context, not as writes", () => {
  const v = awsGateView({ state: "unverified", reason: "missing_context", refused: [{ pattern: "s3:Put*", why: "needs a condition value" }] });
  assert.strictEqual(v.tone, "warn");
  assert.strictEqual(v.badge, "Needs context");
  assert.deepStrictEqual(v.refused, ["s3:Put* — needs context: needs a condition value"]);
  assert.doesNotMatch(v.text, /can write/);
});

test("awsGateView: other unverified reasons are warnings, never ok; unknown input is unverified", () => {
  for (const reason of ["timeout", "error", "simulate_denied", "probe_disabled", "weird"]) { // simulate_denied is only ackable when the server says ack_eligible
    const v = awsGateView({ state: "unverified", reason });
    assert.strictEqual(v.tone, "warn", reason);
    assert.strictEqual(v.signIn, false, reason);
  }
  assert.strictEqual(awsGateView(null).tone, "warn");
  assert.strictEqual(awsGateView({ state: "read_only_maybe" }).tone, "warn");
});

test("awsConnFromRow in sso_direct sends the mode and none of role, elevate or s3 read", () => {
  const row = { name: "a", source_profile: "p", role_arn: "arn:aws:iam::1:role/x", region: "r", mode: "sso_direct", label: "L",
    s3: "b", elevRole: "arn:aws:iam::1:role/w", elevMinutes: "30", elevIAM: true, egress: "h.example.com" };
  assert.deepStrictEqual(awsConnFromRow(row), { name: "a", source_profile: "p", region: "r", mode: "sso_direct", label: "L", egress_allow: ["h.example.com"] });
  const ar = awsConnFromRow({ name: "a", source_profile: "p", role_arn: "arn:aws:iam::1:role/x", region: "r" });
  assert.strictEqual("mode" in ar, false);
  assert.strictEqual(ar.role_arn, "arn:aws:iam::1:role/x");
});

test("rowSaveState: sso_direct saves only on a read_only gate, and an unchanged edit gets no exemption", () => {
  assert.deepStrictEqual(rowSaveState({ mode: "sso_direct" }), { canSave: false, needsAck: false });
  assert.deepStrictEqual(rowSaveState({ mode: "sso_direct", unchanged: true }), { canSave: false, needsAck: false });
  for (const state of ["not_read_only", "unverified", "x"]) {
    assert.deepStrictEqual(rowSaveState({ mode: "sso_direct", gate: { state } }), { canSave: false, needsAck: false }, state);
  }
  assert.deepStrictEqual(rowSaveState({ mode: "sso_direct", gate: { state: "read_only" } }), { canSave: true, needsAck: false });
  assert.deepStrictEqual(rowSaveState({ mode: "sso_direct", assume: "ok", verdict: "writes" }), { canSave: false, needsAck: false });
  // assume_role behaves as before
  assert.deepStrictEqual(rowSaveState({ assume: "ok", verdict: "none_found" }), { canSave: true, needsAck: false });
});

test("awsRowStateFor: an edit that only changes the label still needs the gate in sso_direct", () => {
  const row = { mode: "sso_direct", source_profile: "p", role_arn: "", orig: { source_profile: "p", role_arn: "" }, gate: null };
  assert.strictEqual(awsRowStateFor(row).canSave, false);
  row.gate = { state: "read_only" };
  assert.strictEqual(awsRowStateFor(row).canSave, true);
  const ar = { source_profile: "p", role_arn: "arn:aws:iam::1:role/x", orig: { source_profile: "p", role_arn: "arn:aws:iam::1:role/x" } };
  assert.strictEqual(awsRowStateFor(ar).canSave, true);
});

test("credTableRows aws: an sso_direct row shows the mode, the account and a gate note, never the AssumeRole 'Not checked'", () => {
  const e = awsE("sbx", { mode: "sso_direct", role_arn: "", account_id: "123456789012", base: { state: "not_checked" } });
  const [r] = credTableRows({ key: "aws", entries: [e] });
  assert.deepStrictEqual([r.cells[1].text, r.cells[1].sub], ["123456789012", "SSO role directly"]);
  assert.deepStrictEqual([r.cells[3].text, r.cells[3].cls, r.cells[3].sub], ["Read-only gate runs on every use", "", ""]);
  assert.doesNotMatch(JSON.stringify(r.cells), /Not checked/);
  assert.strictEqual(r.danger, false);
  // an unreadable profile leaves the account empty, not a made-up one
  const [u] = credTableRows({ key: "aws", entries: [awsE("x", { mode: "sso_direct", role_arn: "", account_id: "" })] });
  assert.strictEqual(u.cells[1].text, "");
  // an invalid entry keeps the plain dash, as for any row
  const [bad] = credTableRows({ key: "aws", entries: [awsE("y", { mode: "sso_direct", status: "invalid" })] });
  assert.strictEqual(bad.cells[3].text, "–");
  // assume_role rows are unchanged
  const [a] = credTableRows({ key: "aws", entries: [awsE("z", { base: { state: "none_found" } })] });
  assert.strictEqual(a.cells[1].sub, "CuliShopLabAccess");
});

test("credAWSCard: an sso_direct connection names the mode and the gate, not an empty Role and 'Not checked'", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/credentials.js"), "utf8");
  const card = src.slice(src.indexOf("function credAWSCard"), src.indexOf("function credAWSCard") + 1800);
  assert.match(card, /direct \? \["Mode", CRED_SSO_DIRECT_LABEL\] : \["Role"/);
  assert.match(card, /e\.status === "ok" && direct/);
});

test("awsGateBoxEmpty: a bare sign-in gate draws no empty amber box; anything else to show keeps it", () => {
  const bare = awsGateView({ state: "unverified", reason: "sso_expired" });
  assert.strictEqual(bare.signIn, true);
  assert.strictEqual(awsGateBoxEmpty(bare), true);
  const withReads = awsGateView({ state: "unverified", reason: "sso_expired", data_reads: [{ action: "secretsmanager:GetSecretValue", denied: true }] });
  if (withReads.dataReadText) assert.strictEqual(awsGateBoxEmpty(withReads), false);
  assert.strictEqual(awsGateBoxEmpty(awsGateView({ state: "unverified", reason: "timeout" })), false);
  assert.strictEqual(awsGateBoxEmpty(awsGateView({ state: "not_read_only", refused: [{ pattern: "s3:Put*", why: "writes" }] })), false);
  assert.strictEqual(awsGateBoxEmpty(awsGateView({ state: "read_only" })), false);
});

test("the save 409 branch for a gate that did not pass resets the stale hours and repaints the mode note", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/credentials.js"), "utf8");
  const i = src.indexOf('e.code === "not_read_only" || e.code === "unverified"');
  const branch = src.slice(i, src.indexOf("needs_ack", i));
  assert.match(branch, /row\.hours = 0;\s*awsPaintModeNote\(row\);/);
  assert.match(src, /if \(!awsGateBoxEmpty\(g\)\) nodes\.push\(awsGateNode\(g\)\)/);
});

test("Add GitHub offers a token or an App side by side", () => {
  assert.deepStrictEqual(GH_ROUTE_CHOICES.map((c) => c.id), ["pat", "app"]);
  assert.deepStrictEqual(GH_ROUTE_CHOICES.map((c) => c.title), ["Personal access token \u2014 simple", "GitHub App \u2014 safer"]);
  assert.strictEqual(GH_ROUTE_CHOICES[0].line, "One token you create on github.com. The agent can read it; it is long-lived and not revoked when the session ends.");
  assert.strictEqual(GH_ROUTE_CHOICES[1].line, "CuliBox mints 1-hour tokens limited to each profile's repositories and permissions. A few more minutes to set up.");
});

test("the PAT suggestion names the minimal fine-grained setup, and the N3 warning is unchanged", () => {
  assert.strictEqual(GH_PAT_SUGGESTION, 'Suggested: a fine-grained token with only the repositories you need \u00b7 Contents: Read and write \u00b7 Pull requests: Read and write \u00b7 Metadata: Read. Avoid Administration and "All repositories". Add Workflows only if the agent must change CI files.');
  assert.strictEqual(CRED_N3, "The agent can read this token. It is long-lived and is not revoked when the session ends.");
});

test("ghPatValueWarning flags a classic token only", () => {
  assert.strictEqual(ghPatValueWarning("ghp_x"), "This looks like a classic token: it reaches every repository your account can. A fine-grained token limited to the repos you need is safer.");
  assert.strictEqual(ghPatValueWarning("github_pat_x"), "");
  assert.strictEqual(ghPatValueWarning(""), "");
  assert.strictEqual(ghPatValueWarning(undefined), "");
});

test("GH_APP_HELP walks through creating the App with read or write choices", () => {
  assert.strictEqual(GH_APP_HELP.length, 5);
  const all = GH_APP_HELP.join("\n");
  for (const w of ["Metadata", "Contents", "Pull requests", "Issues", "Only select repositories", "Client ID", "branch protection"]) assert.ok(all.includes(w), w);
  assert.ok(!all.includes("Administration: Read"));
  assert.strictEqual(GH_APP_HELP[0], "On github.com: Settings \u203a Developer settings \u203a GitHub Apps \u203a New GitHub App. Untick Webhook \u203a Active.");
  assert.strictEqual(GH_APP_HELP[2], "Create it, then Install App on the account that owns the repos and choose Only select repositories.");
});

test("credDeleteText: a store value only 'stays in the store' when it is there", () => {
  const stays = "stays in the encrypted store";
  const tok = (status, value_kind) => credDeleteText("token", { name: "t", status, value_kind });
  const app = (status, key_ref_kind) => credDeleteText("github", { name: "a", status, key_ref_kind });
  assert.ok(tok("ok", "store").includes(stays));
  assert.ok(tok("stored_locked", "store").includes(stays), "locked is not missing");
  assert.ok(!tok("missing", "store").includes(stays));
  assert.match(tok("missing", "store"), /already missing/);
  assert.ok(tok("ok", "env").includes("is not touched"));
  assert.ok(app("ok", "store").includes(stays));
  assert.ok(!app("missing", "store").includes(stays));
  assert.match(app("missing", "store"), /already missing/);
  assert.ok(app("ok", "file").includes("file is not touched"));
  assert.ok(tok("ok", "store").startsWith("Delete the token t?"));
  assert.ok(app("ok", "file").startsWith("Delete the GitHub App a?"));
});

// A full-width row must span the real columns. Under table-layout: fixed a larger
// colSpan adds phantom grid columns, which starved the unsized Used by column to
// 2px (it wrapped one letter per line) as soon as a delete confirm opened.
test("credColumnCount: the cells of the entry's own row, with a safe floor", () => {
  const tbodyWith = (n) => ({ rows: [{ cells: { length: n } }] });
  assert.strictEqual(credColumnCount(tbodyWith(5)), 5);
  assert.strictEqual(credColumnCount(tbodyWith(6)), 6);
  assert.strictEqual(credColumnCount({ rows: [] }), 1);
});

// status "missing" also covers a store that could not be listed, and a broken
// entry reads "invalid" whether or not its value exists. Neither may state a fact
// nobody checked: a live secret must not be reported gone, or promised present.
test("credDeleteText: says nothing it does not know about the store value", () => {
  const unread = { name: "t", status: "missing", value_kind: "store", error: "could not read the store" };
  assert.ok(!credDeleteText("token", unread).includes("already missing"));
  assert.match(credDeleteText("token", unread), /could not be read/);
  const brokenApp = { name: "a", status: "missing", key_ref_kind: "store", error: "could not read the store" };
  assert.ok(!credDeleteText("github", brokenApp).includes("already missing"));
  const invalid = { name: "t", status: "invalid", value_kind: "store" };
  assert.ok(!credDeleteText("token", invalid).includes("stays in the encrypted store"));
  assert.match(credDeleteText("token", invalid), /Any value in the store stays/);
});

// --- #27: the table/card choice as one pure rule --------------------------------

test("credTablesAt: never outside the page, whatever the width", () => {
  assert.strictEqual(credTablesAt(1440, false), false);
  assert.strictEqual(credTablesAt(300, false), false);
  assert.strictEqual(credTablesAt(0, false), false);
  assert.strictEqual(credTablesAt(NaN, false), false);
});

test("credTablesAt: a body that cannot be measured keeps tables (the tab is hidden, today's rule)", () => {
  assert.strictEqual(credTablesAt(0, true), true);
  assert.strictEqual(credTablesAt(NaN, true), true);
  assert.strictEqual(credTablesAt(undefined, true), true);
  assert.strictEqual(credTablesAt(-1, true), true);
});

test("credTablesAt: tables from 700 up, cards below (the measured threshold)", () => {
  assert.strictEqual(credTablesAt(699, true), false);
  assert.strictEqual(credTablesAt(700, true), true);
  assert.strictEqual(credTablesAt(1440, true), true);
  assert.strictEqual(credTablesAt(1, true), false);
});

// --- #27: the width a table needs is measured, the 700 is only a floor ---------------

test("credTablesAt: a measured need raises the bar above the floor", () => {
  assert.strictEqual(credTablesAt(800, true, 0), true, "nothing measured: the floor alone");
  assert.strictEqual(credTablesAt(800, true, 850), false, "the content needs 850, the body has 800");
  assert.strictEqual(credTablesAt(849, true, 850), false);
  assert.strictEqual(credTablesAt(850, true, 850), true);
  assert.strictEqual(credTablesAt(900, true, 850), true);
});

test("credTablesAt: the floor stays under a measured need (the token tables' fixed layout needs it)", () => {
  assert.strictEqual(credTablesAt(650, true, 0), false);
  assert.strictEqual(credTablesAt(650, true, 500), false, "a need below the floor does not lower it");
  assert.strictEqual(credTablesAt(700, true, 500), true);
  assert.strictEqual(credTablesAt(650, true, undefined), false);
  assert.strictEqual(credTablesAt(650, true, NaN), false);
});

test("credTablesAt: a hidden tab and a modal ignore the measured need", () => {
  assert.strictEqual(credTablesAt(0, true, 850), true, "0 wide: the tab is hidden, tables as before");
  assert.strictEqual(credTablesAt(NaN, true, 850), true);
  assert.strictEqual(credTablesAt(900, false, 0), false, "a modal never draws tables");
  assert.strictEqual(credTablesAt(900, false, 850), false);
});

// The add flows (credentials.js) also run in the drawer over the profile page (#cred-drawer,
// spec 5.7), where they draw the same markup. Every rule written for #creds therefore names
// :is(#creds, #cred-drawer), which has #creds's own specificity (an id), so no cascade changes
// for the tab; #creds-body has a twin, #cred-drawer-body. The phone's block stays #creds only:
// the drawer is desktop-only and the phone must not change.
test("every #creds rule also applies to #cred-drawer", () => {
  const fs = require("node:fs"), path = require("node:path");
  const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const app = read("app.css"), desk = read("desktop.css");
  // The credentials rules' phone block: the @media (max-width: 700px) that holds "#creds .btn { min-height".
  const at = app.indexOf("#creds .btn { min-height");
  const from = app.lastIndexOf("@media (max-width: 700px) {", at);
  let depth = 0, to = from;
  for (let i = app.indexOf("{", from); i < app.length; i++) {
    if (app[i] === "{") depth++;
    else if (app[i] === "}" && --depth === 0) { to = i + 1; break; }
  }
  assert.ok(at > 0 && from > 0 && to > at, "found the phone block of the credentials rules");
  const phone = app.slice(from, to);
  const base = app.slice(0, from) + app.slice(to);
  const DRAWER = ":is(#creds, #cred-drawer)";
  const bare = (css) => css.split(DRAWER).join("").match(/#creds(?![-\w])/g);
  assert.strictEqual(bare(base), null, "app.css: no #creds selector outside the phone block that lacks the drawer");
  assert.strictEqual(bare(desk), null, "desktop.css: no #creds selector that lacks the drawer");
  assert.ok(base.split(DRAWER).length - 1 >= 100, "app.css writes its credentials rules for both");
  assert.ok(desk.split(DRAWER).length - 1 >= 60, "desktop.css writes its credentials rules for both");
  assert.ok(/#creds \.btn \{/.test(phone) && !/cred-drawer/.test(phone), "the phone block still says #creds only");
  // #creds-body: every rule that names it names #cred-drawer-body in the same selector list.
  for (const [name, css] of [["app.css", base], ["desktop.css", desk]]) {
    const re = /([^{}]+)\{[^{}]*\}/g;
    let m, seen = 0;
    while ((m = re.exec(css))) {
      if (!/#creds-body(?![-\w])/.test(m[1])) continue;
      seen++;
      assert.ok(/#cred-drawer-body(?![-\w])/.test(m[1]), name + ": #creds-body has a #cred-drawer-body twin in " + m[1].trim());
    }
    assert.ok(seen >= (name === "app.css" ? 1 : 2), name + ": the #creds-body rules were found");
  }
  // Same specificity as before: the drawer's selector is an id plus what followed it.
  assert.match(base, /:is\(#creds, #cred-drawer\) \.ent \{/);
  assert.match(desk, /#page-host > #settings-modal\.as-page :is\(#creds, #cred-drawer\) \.ent \{/);
});

test("awsRegionSuggest: the host's regions first (each once, in order), then the built-in list without repeats", () => {
  const out = awsRegionSuggest([{ region: "eu-west-1" }, { region: "" }, { region: "eu-west-1" }, { region: "ap-southeast-1" }]);
  assert.deepStrictEqual(out.slice(0, 2), ["eu-west-1", "ap-southeast-1"]);
  assert.strictEqual(out.filter((r) => r === "us-east-1").length, 1);
  assert.strictEqual(out.filter((r) => r === "eu-west-1").length, 1);
  assert.strictEqual(out.length, AWS_REGIONS.length + (AWS_REGIONS.includes("eu-west-1") ? 0 : 1), "nothing else is added");
  assert.deepStrictEqual(awsRegionSuggest(undefined), AWS_REGIONS);
  assert.deepStrictEqual(awsRegionSuggest([]), AWS_REGIONS);
});

test("AWS_REGIONS: every entry passes the server's region format (awsscope.regionRe), none repeats", () => {
  const goSrc = require("node:fs").readFileSync(require("node:path").join(__dirname, "../../awsscope/awsscope.go"), "utf8");
  assert.match(goSrc, /regionRe\s*=\s*regexp\.MustCompile\(`\^\[a-z\]\{2\}\(-\[a-z\]\+\)\+-\\d\{1,2\}\$`\)/, "the server regex is the one this test mirrors");
  for (const r of AWS_REGIONS) assert.match(r, /^[a-z]{2}(-[a-z]+)+-\d{1,2}$/);
  assert.strictEqual(new Set(AWS_REGIONS).size, AWS_REGIONS.length);
});

test("awsProfileSuggest: selectable profile names only", () => {
  assert.deepStrictEqual(awsProfileSuggest([{ name: "a", selectable: true }, { name: "b", selectable: false }, { name: "c", selectable: true }]), ["a", "c"]);
  assert.deepStrictEqual(awsProfileSuggest(undefined), []);
});

// --- #168 G1: the section head's glyph, from declared fields only ------------------------------------------------------------
const tokGroup = (entries) => ({ key: "token:x", title: "X", entries });

test("section glyph from template: a token section shows a provider mark only when every entry has a catalog template of one glyph", () => {
  assert.strictEqual(credGroupIcon(tokGroup([{ template: "sentry" }])), "si:sentry");
  assert.strictEqual(credGroupIcon(tokGroup([{ template: "github-pat" }])), "si:github");
  assert.strictEqual(credGroupIcon(tokGroup([{ template: "grafana" }, { template: "grafana" }])), "si:grafana");
  for (const [t, g] of [["datadog", "si:datadog"], ["pagerduty", "si:pagerduty"]]) assert.strictEqual(credGroupIcon(tokGroup([{ template: t }])), g);
  assert.strictEqual(credGroupIcon(tokGroup([{ template: "sentry" }, { template: "other" }])), "key-round", "a mixed section is generic");
  assert.strictEqual(credGroupIcon(tokGroup([{ template: "sentry" }, {}])), "key-round", "an entry from an older server (no template) is generic");
  assert.strictEqual(credGroupIcon(tokGroup([{ template: "sentry" }, { template: "grafana" }])), "key-round", "two different marks are generic");
  assert.strictEqual(credGroupIcon(tokGroup([{ template: "other" }])), "key-round");
  assert.strictEqual(credGroupIcon(tokGroup([])), "key-round");
});

test("section glyph: the free-text provider never picks a mark, and hostile templates fall back", () => {
  assert.strictEqual(credGroupIcon(tokGroup([{ provider: "sentry" }])), "key-round");
  assert.strictEqual(credGroupIcon(tokGroup([{ provider: "sentry", template: "" }])), "key-round");
  for (const t of ["constructor", "__proto__", "toString", "hasOwnProperty", "SENTRY", null, 7, {}, ["sentry"]]) {
    assert.strictEqual(credGroupIcon(tokGroup([{ template: t }])), "key-round", String(t));
  }
  // the key is the group's own, whatever the entries say
  assert.strictEqual(credGroupIcon({ key: "token:sentry", title: "Sentry", entries: [{ provider: "sentry" }] }), "key-round");
});

test("section glyph: AWS and GitHub Apps by group key, Stored values and Legacy secrets none, unknown keys none", () => {
  assert.strictEqual(credGroupIcon({ key: "aws", entries: [] }), "cloud");
  assert.strictEqual(credGroupIcon({ key: "github", entries: [] }), "si:github");
  assert.strictEqual(credGroupIcon({ key: "values", entries: [] }), "", "G1 left-out p");
  assert.strictEqual(credGroupIcon({ key: "legacy", entries: [] }), "", "G1 left-out q");
  for (const k of ["constructor", "__proto__", "", "tokens", undefined]) assert.strictEqual(credGroupIcon({ key: k, entries: [] }), "", String(k));
  assert.strictEqual(credGroupIcon(null), "");
  assert.strictEqual(credGroupIcon({ key: "aws" }), "cloud", "no entries field");
  const gs = credGroups(view({ tokens: [tok("a", "sentry", { template: "sentry" }), tok("b", "acme")] }));
  assert.deepStrictEqual(gs.map((g) => credGroupIcon(g)), ["cloud", "si:github", "key-round", "si:sentry", ""]);
});

// --- #374: acknowledging a non-read-only SSO permission set ---------------------------------------------------

const { AWS_FULL_ACCESS_TEXT } = require("../static/sessionrow.js");
const ELIG = { state: "not_read_only", ack_eligible: true, refused: [{ pattern: "iam:*", why: "writes" }], missing_perms: [] };

test("rowSaveState sso_direct: read_only saves; an eligible gate needs the tick; anything else does not save", () => {
  const sd = (o) => rowSaveState({ mode: "sso_direct", ...o });
  assert.deepStrictEqual(sd({ gate: { state: "read_only" } }), { canSave: true, needsAck: false });
  assert.deepStrictEqual(sd({ gate: ELIG }), { canSave: false, needsAck: true });
  assert.deepStrictEqual(sd({ gate: ELIG, fullAck: false }), { canSave: false, needsAck: true });
  assert.deepStrictEqual(sd({ gate: ELIG, fullAck: true }), { canSave: true, needsAck: false });
  assert.deepStrictEqual(sd({ gate: ELIG, orig: { full_access: true } }), { canSave: true, needsAck: false });
  assert.deepStrictEqual(sd({ gate: { state: "unverified", reason: "timeout", ack_eligible: false }, fullAck: true }), { canSave: false, needsAck: false });
  assert.deepStrictEqual(sd({ gate: { state: "not_read_only" }, fullAck: true }), { canSave: false, needsAck: false }, "ack_eligible missing");
  assert.deepStrictEqual(sd({ gate: { state: "not_read_only", ack_eligible: "yes" }, fullAck: true }), { canSave: false, needsAck: false });
});

test("awsRowStateFor passes the tick and the stored full_access to the save rule", () => {
  const row = { mode: "sso_direct", source_profile: "p", role_arn: "", orig: { source_profile: "p", role_arn: "", mode: "sso_direct", full_access: true }, gate: ELIG };
  assert.strictEqual(awsRowStateFor(row).canSave, true);
  const fresh = { mode: "sso_direct", source_profile: "p", role_arn: "", gate: ELIG, fullAck: false };
  assert.deepStrictEqual(awsRowStateFor(fresh), { canSave: false, needsAck: true });
  fresh.fullAck = true;
  assert.strictEqual(awsRowStateFor(fresh).canSave, true);
});

test("awsGateView: an eligible result says what was found and that saving needs the acknowledgement; transient does not", () => {
  const v = awsGateView(ELIG);
  assert.strictEqual(v.ackEligible, true);
  assert.deepStrictEqual(v.refused, ["iam:* \u2014 writes"]);
  assert.match(v.text, /acknowledg/i);
  assert.doesNotMatch(v.text, /cannot be saved/);
  for (const reason of ["missing_permissions", "simulate_denied"]) {
    const e = awsGateView({ state: "unverified", reason, ack_eligible: true, missing_perms: ["iam:GetPolicy"] });
    assert.strictEqual(e.ackEligible, true, reason);
    assert.match(e.text, /acknowledg/i, reason);
    assert.doesNotMatch(e.text, /Nothing is saved until/, reason);
  }
  for (const reason of ["timeout", "error", "sso_expired", "probe_disabled", "missing_permissions", "simulate_denied"]) {
    const t = awsGateView({ state: "unverified", reason, ack_eligible: false });
    assert.strictEqual(t.ackEligible, false, reason);
    assert.doesNotMatch(t.text, /acknowledg/i, reason);
  }
  assert.strictEqual(awsGateView({ state: "unverified", reason: "simulate_denied" }).ackEligible, false, "no field, no box");
  assert.strictEqual(awsGateView({ state: "read_only", ack_eligible: true }).ackEligible, false);
  assert.strictEqual(awsGateView({ state: "not_read_only" }).ackEligible, false);
  assert.match(awsGateView({ state: "not_read_only" }).text, /cannot be saved/);
});

test("awsAckBody: ack_full_access only with the tick, carrying the pattern strings and missing perms", () => {
  const g = { ...ELIG, refused: [{ pattern: "iam:*", why: "w" }, { Pattern: "s3:Put*", Why: "w" }], missing_perms: ["iam:GetPolicy"] };
  assert.deepStrictEqual(awsAckBody({ mode: "sso_direct", fullAck: true, gate: g }), { refused: ["iam:*", "s3:Put*"], missing_perms: ["iam:GetPolicy"] });
  assert.strictEqual(awsAckBody({ mode: "sso_direct", fullAck: false, gate: g }), null);
  assert.strictEqual(awsAckBody({ mode: "sso_direct", fullAck: true, gate: { state: "not_read_only" } }), null);
  assert.strictEqual(awsAckBody({ mode: "", fullAck: true, gate: g }), null);
  assert.deepStrictEqual(awsAckBody({ mode: "sso_direct", fullAck: true, gate: { ...ELIG, refused: undefined, missing_perms: undefined } }), { refused: [], missing_perms: [] });
});

test("awsModeText: a non-read-only permission set needs an acknowledgement and then has full access", () => {
  const t = awsModeText(3);
  assert.doesNotMatch(t, /Read-only permission sets only/);
  assert.ok(t.endsWith("A permission set that is not read-only needs an acknowledgement and then has full access."), t);
});

test("the card: full_access (and only it) swaps the gate note for the label, with the ack line and the danger tone", () => {
  const base = awsE("sbx", { mode: "sso_direct", role_arn: "", account_id: "123456789012", base: { state: "not_checked" } });
  const [plain] = credTableRows({ key: "aws", entries: [base] });
  assert.strictEqual(plain.cells[3].text, "Read-only gate runs on every use");
  assert.strictEqual(plain.danger, false);
  const full = { ...base, full_access: true, full_access_ack: { state: "acknowledged", at: "2026-11-25T10:00:00Z", actor: "founder" } };
  const [r] = credTableRows({ key: "aws", entries: [full] });
  assert.strictEqual(r.cells[3].text, AWS_FULL_ACCESS_TEXT);
  assert.match(r.cells[3].sub, /^acknowledged 2026-11-25 by founder$/);
  assert.strictEqual(r.danger, true);
  // the label follows full_access, not an ack record alone
  const [only] = credTableRows({ key: "aws", entries: [{ ...base, full_access_ack: { state: "acknowledged", at: "2026-11-25T10:00:00Z", actor: "a" } }] });
  assert.strictEqual(only.cells[3].text, "Read-only gate runs on every use");
});
