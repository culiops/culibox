"use strict";
// N1: the profile editor's GitHub section and the steer rail's token rows. Both
// halves are pure functions so the rules can be read and tested without a DOM.
const test = require("node:test");
const assert = require("node:assert");
const { ghBindingFromForm, ghFormError, ghPermLevels, ghWriteWarning, ghRouteOf, ghUsableTokens, GH_PERMS, GH_DEFAULT_PERMS, GH_WRITABLE } = require("../static/profileform.js");
const { ghRowText, ghRailRows } = require("../static/steerrail.js");

const allDefaults = () => Object.fromEntries(GH_PERMS.map((k) => [k, GH_DEFAULT_PERMS.includes(k) ? "read" : "off"]));

test("the GitHub permission list and its defaults are the server's", () => {
  assert.deepStrictEqual(GH_PERMS, ["actions", "checks", "contents", "pull_requests", "issues", "metadata", "statuses"]);
  assert.deepStrictEqual(GH_DEFAULT_PERMS, ["actions", "checks", "contents", "pull_requests", "issues", "metadata"]);
});

test("ghBindingFromForm omits permissions when they are the defaults", () => {
  const got = ghBindingFromForm({
    owners: [{ owner: "acme", app: "main", repos: "api" }],
    permissions: allDefaults(),
  });
  assert.deepStrictEqual(got, { owners: [{ owner: "acme", app: "main", repos: ["api"] }] });
  assert.ok(!("permissions" in got));
});

test("ghBindingFromForm trims, splits repos on commas or whitespace, drops empty rows", () => {
  const got = ghBindingFromForm({
    owners: [
      { owner: "  acme ", app: " main ", repos: " api, web\n  docs ,, \tinfra " },
      { owner: "", app: "main", repos: "  " },
      { owner: "  ", app: "", repos: "" },
    ],
    permissions: allDefaults(),
  });
  assert.deepStrictEqual(got, { owners: [{ owner: "acme", app: "main", repos: ["api", "web", "docs", "infra"] }] });
});

test("ghBindingFromForm keeps a half-filled row so the server can name the problem", () => {
  const got = ghBindingFromForm({ owners: [{ owner: "acme", app: "main", repos: "" }], permissions: allDefaults() });
  assert.deepStrictEqual(got.owners, [{ owner: "acme", app: "main", repos: [] }]);
});

test("ghBindingFromForm is undefined when every row is empty", () => {
  assert.strictEqual(ghBindingFromForm({ owners: [{ owner: "", app: "main", repos: "" }], permissions: allDefaults() }), undefined);
  assert.strictEqual(ghBindingFromForm({ owners: [], permissions: allDefaults() }), undefined);
  assert.strictEqual(ghBindingFromForm(undefined), undefined);
});

test("ghBindingFromForm sends a changed permission set as read", () => {
  const perms = allDefaults();
  perms.statuses = "read";
  perms.actions = "off";
  const got = ghBindingFromForm({ owners: [{ owner: "acme", app: "main", repos: "api" }], permissions: perms });
  assert.deepStrictEqual(got.permissions, {
    checks: "read", contents: "read", pull_requests: "read", issues: "read", metadata: "read", statuses: "read",
  });
});

test("ghBindingFromForm: write is sent for contents, pull_requests and issues only", () => {
  const rows = [{ owner: "acme", app: "main", repos: "api" }];
  const got = ghBindingFromForm({ owners: rows, permissions: { ...allDefaults(), contents: "write" } });
  assert.strictEqual(got.permissions.contents, "write");
  for (const k of ["actions", "checks", "pull_requests", "issues", "metadata"]) assert.strictEqual(got.permissions[k], "read");
  assert.ok(!("statuses" in got.permissions));
  // The UI never offers write on the others: a stray one goes out as read.
  const stray = ghBindingFromForm({ owners: rows, permissions: { ...allDefaults(), actions: "write", metadata: "write", statuses: "read" } });
  assert.strictEqual(stray.permissions.actions, "read");
  assert.strictEqual(stray.permissions.metadata, "read");
  // Stray writes alone leave the six reads: the default shape, no key.
  assert.ok(!("permissions" in ghBindingFromForm({ owners: rows, permissions: { ...allDefaults(), actions: "write" } })));
});

test("ghBindingFromForm: write back to read stores read again, and the warning goes", () => {
  const rows = [{ owner: "acme", app: "main", repos: "api" }];
  const on = { ...allDefaults(), pull_requests: "write" };
  assert.strictEqual(ghBindingFromForm({ owners: rows, permissions: on }).permissions.pull_requests, "write");
  assert.notStrictEqual(ghWriteWarning(on, false), "");
  const back = { ...on, pull_requests: "read" };
  // Six reads and statuses off is the default again: no permissions key at all.
  assert.ok(!("permissions" in ghBindingFromForm({ owners: rows, permissions: back })));
  assert.strictEqual(ghWriteWarning(back, false), "");
});

test("ghBindingFromForm: a token row sends {owner, token, repos} with no app key", () => {
  const got = ghBindingFromForm({
    owners: [
      { owner: " acme ", app: "main", token: " gh-pat-acme ", repos: "api web" },
      { owner: "org", app: "main", token: "", repos: "x" },
    ],
    permissions: allDefaults(),
  });
  assert.deepStrictEqual(got.owners[0], { owner: "acme", token: "gh-pat-acme", repos: ["api", "web"] });
  assert.ok(!("app" in got.owners[0]));
  // An App row keeps its shape and gains no token key.
  assert.deepStrictEqual(got.owners[1], { owner: "org", app: "main", repos: ["x"] });
  assert.ok(!("token" in got.owners[1]));
});

test("ghBindingFromForm carries the stored extra egress hosts the form has no field for", () => {
  const rows = [{ owner: "acme", app: "main", repos: "api" }];
  const hosts = ["ghe.example.com"];
  const got = ghBindingFromForm({ owners: rows, permissions: allDefaults(), egress_allow: hosts });
  assert.deepStrictEqual(got.egress_allow, ["ghe.example.com"]);
  assert.notStrictEqual(got.egress_allow, hosts); // a copy
  assert.ok(!("egress_allow" in ghBindingFromForm({ owners: rows, permissions: allDefaults(), egress_allow: [] })));
  assert.ok(!("egress_allow" in ghBindingFromForm({ owners: rows, permissions: allDefaults() })));
});

test("an all-off permission set is refused, not turned into the defaults", () => {
  const none = Object.fromEntries(GH_PERMS.map((k) => [k, "off"]));
  const rows = [{ owner: "acme", app: "main", repos: "api" }];
  assert.deepStrictEqual(ghBindingFromForm({ owners: rows, permissions: none }).permissions, {});
  assert.strictEqual(ghFormError({ owners: rows, permissions: none }), "GitHub: tick at least one permission, or the agent gets none.");
  // The defaults, a changed set, and no owner rows at all are all fine.
  assert.strictEqual(ghFormError({ owners: rows, permissions: allDefaults() }), "");
  assert.strictEqual(ghFormError({ owners: rows, permissions: { ...none, statuses: "read" } }), "");
  assert.strictEqual(ghFormError({ owners: [{ owner: "", app: "main", repos: "" }], permissions: none }), "");
});

test("GH_WRITABLE is the server's writable set", () => {
  assert.deepStrictEqual(GH_WRITABLE, ["contents", "pull_requests", "issues"]);
});

test("ghPermLevels: a stored set lights its keys at their level, none stored lights the six reads", () => {
  assert.deepStrictEqual(ghPermLevels(undefined), allDefaults());
  assert.deepStrictEqual(ghPermLevels({ owners: [] }), allDefaults());
  const got = ghPermLevels({ permissions: { contents: "write", statuses: "read" } });
  assert.strictEqual(got.contents, "write");
  assert.strictEqual(got.statuses, "read");
  assert.strictEqual(got.actions, "off");
  assert.deepStrictEqual(Object.keys(got), GH_PERMS);
});

test("ghWriteWarning: fixed copy for a private copy and for a folder profile, empty with no write", () => {
  assert.strictEqual(ghWriteWarning({ contents: "write" }, false),
    "Write: the agent can push and open PRs on these repositories for the session's lifetime; tokens expire in 1 hour.");
  assert.strictEqual(ghWriteWarning({ issues: "write" }, true),
    "Write: this profile works in your folder, but the agent can still push branches, change files and open PRs and issues on these repositories for the session's lifetime; tokens expire in 1 hour.");
  assert.strictEqual(ghWriteWarning({ contents: "read", issues: "read" }, false), "");
  assert.strictEqual(ghWriteWarning({}, true), "");
  assert.strictEqual(ghWriteWarning(undefined, true), "");
  // Only the writable keys count.
  assert.strictEqual(ghWriteWarning({ actions: "write" }, false), "");
});

test("ghRouteOf mirrors Go PushRoute/HasPAT: a PAT owner or contents write is the write route", () => {
  const app = { owners: [{ owner: "acme", app: "main", repos: ["api"] }] };
  assert.deepStrictEqual(ghRouteOf(undefined), { write: false, pat: false });
  assert.deepStrictEqual(ghRouteOf(app), { write: false, pat: false });
  assert.deepStrictEqual(ghRouteOf({ ...app, permissions: { contents: "write" } }), { write: true, pat: false });
  // Write on the other keys does not open the push route.
  assert.deepStrictEqual(ghRouteOf({ ...app, permissions: { contents: "read", issues: "write" } }), { write: false, pat: false });
  const pat = { owners: [{ owner: "acme", app: "main", repos: [] }, { owner: "org", token: "gh-pat", repos: ["x"] }] };
  assert.deepStrictEqual(ghRouteOf(pat), { write: true, pat: true });
  assert.deepStrictEqual(ghRouteOf({ owners: [{ owner: "o", token: "", repos: [] }] }), { write: false, pat: false });
});

function localHHMM(iso) {
  const d = new Date(Date.parse(iso));
  return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
}

test("ghRowText: a minted row names the owner, repo count, outcome and expiry", () => {
  const row = { provider: "github", subject: "acme", repos: 3, outcome: "minted", expires: "2026-10-19T14:05:00Z" };
  assert.strictEqual(ghRowText(row), "GitHub · acme · 3 repos · minted · until " + localHHMM(row.expires));
});

test("ghRowText: one repo reads singular", () => {
  const row = { provider: "github", subject: "acme", repos: 1, outcome: "minted", expires: "2026-10-19T14:05:00Z" };
  assert.match(ghRowText(row), /^GitHub · acme · 1 repo · minted/);
});

test("ghRowText: a revoked row is short", () => {
  assert.strictEqual(
    ghRowText({ provider: "github", subject: "acme", repos: 3, outcome: "revoked", expires: "2026-10-19T14:05:00Z" }),
    "GitHub · acme · revoked");
});

test("ghRowText: a refusal has its outcome and no expiry", () => {
  const t = ghRowText({ provider: "github", subject: "acme", repos: 0, outcome: "denied_scope", expires: "0001-01-01T00:00:00Z" });
  assert.strictEqual(t, "GitHub · acme · 0 repos · denied scope");
  assert.doesNotMatch(t, /until/);
});

test("ghRowText prints only the fields it names", () => {
  const t = ghRowText({ provider: "github", subject: "acme", repos: 2, outcome: "minted", expires: "2026-10-19T14:05:00Z", error: "ghs_SECRET" });
  assert.doesNotMatch(t, /ghs_/);
});

test("ghRowText: a fetched PAT row names the entry and never a value", () => {
  const row = { provider: "github", subject: "acme", entry: "gh-pat-acme", repos: 2, outcome: "fetched", error: "ghp_SECRET" };
  assert.strictEqual(ghRowText(row), "GitHub \u00b7 acme \u00b7 2 repos \u00b7 fetched \u00b7 PAT gh-pat-acme");
});

test("ghRailRows: only github rows, in the order given, capped at five", () => {
  const recent = [
    { provider: "", outcome: "minted" },
    { provider: "aws", outcome: "minted" },
    ...Array.from({ length: 8 }, (_, i) => ({ provider: "github", subject: "o" + i, repos: 1, outcome: "revoked" })),
  ];
  const rows = ghRailRows({ recent });
  assert.strictEqual(rows.length, 5);
  assert.deepStrictEqual(rows[0], { k: "github", v: "GitHub · o0 · revoked", wrap: true });
  assert.deepStrictEqual(ghRailRows(null), []);
  assert.deepStrictEqual(ghRailRows({}), []);
});

test("ghRowText: PAT <entry> only for a github row; an aws row names its connection plainly", () => {
  assert.match(ghRowText({ provider: "github", subject: "acme", repos: 1, outcome: "minted", entry: "ci", expires: "0001-01-01T00:00:00Z" }), / · PAT ci/);
  const aws = ghRowText({ provider: "aws", subject: "acme", repos: 1, outcome: "minted", entry: "sbx-a", expires: "0001-01-01T00:00:00Z" });
  assert.match(aws, / · sbx-a/);
  assert.doesNotMatch(aws, /PAT/);
  assert.doesNotMatch(ghRowText({ provider: "aws", outcome: "minted", entry: "sbx-a" }), /PAT/);
});

test("ghUsableTokens keeps GitHub tokens by provider or by host, in input order", () => {
  const got = ghUsableTokens([
    { name: "sentry", provider: "sentry", hosts: [{ host: "sentry.io" }] },
    { name: "gh-pat", provider: "github", hosts: [] },
    { name: "legacy", hosts: [{ host: "api.github.com" }] },
  ]);
  assert.deepStrictEqual(got.map((e) => e.name), ["gh-pat", "legacy"]);
  assert.deepStrictEqual(ghUsableTokens(undefined), []);
});
