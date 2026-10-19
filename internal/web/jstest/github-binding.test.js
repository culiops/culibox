"use strict";
// N1: the profile editor's GitHub section and the steer rail's token rows. Both
// halves are pure functions so the rules can be read and tested without a DOM.
const test = require("node:test");
const assert = require("node:assert");
const { ghBindingFromForm, ghFormError, ghPermsChecked, GH_PERMS, GH_DEFAULT_PERMS } = require("../static/profileform.js");
const { ghRowText, ghRailRows } = require("../static/steerrail.js");

const allDefaults = () => Object.fromEntries(GH_PERMS.map((k) => [k, GH_DEFAULT_PERMS.includes(k)]));

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

test("ghBindingFromForm sends a changed permission set as read, and only read", () => {
  const perms = allDefaults();
  perms.statuses = true;
  perms.actions = false;
  const got = ghBindingFromForm({ owners: [{ owner: "acme", app: "main", repos: "api" }], permissions: perms });
  assert.deepStrictEqual(got.permissions, {
    checks: "read", contents: "read", pull_requests: "read", issues: "read", metadata: "read", statuses: "read",
  });
  for (const v of Object.values(got.permissions)) assert.strictEqual(v, "read");
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

test("an all-unchecked permission set is refused, not turned into the defaults", () => {
  const none = Object.fromEntries(GH_PERMS.map((k) => [k, false]));
  const rows = [{ owner: "acme", app: "main", repos: "api" }];
  assert.deepStrictEqual(ghBindingFromForm({ owners: rows, permissions: none }).permissions, {});
  assert.match(ghFormError({ owners: rows, permissions: none }), /tick at least one/);
  // The defaults, a changed set, and no owner rows at all are all fine.
  assert.strictEqual(ghFormError({ owners: rows, permissions: allDefaults() }), "");
  assert.strictEqual(ghFormError({ owners: rows, permissions: { ...none, statuses: true } }), "");
  assert.strictEqual(ghFormError({ owners: [{ owner: "", app: "main", repos: "" }], permissions: none }), "");
});

test("ghPermsChecked: a stored set lights exactly its keys, none stored lights the defaults", () => {
  assert.deepStrictEqual(ghPermsChecked(undefined), allDefaults());
  assert.deepStrictEqual(ghPermsChecked({ owners: [] }), allDefaults());
  const got = ghPermsChecked({ permissions: { contents: "read", statuses: "read" } });
  assert.deepStrictEqual(Object.keys(got).filter((k) => got[k]), ["contents", "statuses"]);
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
