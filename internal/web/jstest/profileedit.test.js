"use strict";
// profileedit.js: the profile page's edit model (spec 5.5, 5.6). Pure: what one
// block's Save writes into the loaded profile, which block a refusal belongs to,
// when a second edit or a leave needs a question, what the host switch removes,
// and what a failed save means. The page (profilepage.js) only draws it.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const {
  pfvMergeBlock, pfvBlockForField, pfvGuard, pfvHostRemovals, pfvToHost, pfvHostRefusal,
  pfvGhPreset, pfvGhPresetLevels, pfvGithubError, pfvSaveError,
} = require("../static/profileedit.js");
const { pfvAdvancedCount } = require("../static/profileview.js");
const { ghPermLevels, ghBindingFromForm, ghFormError, GH_PERMS, GH_DEFAULT_PERMS, GH_WRITABLE } = require("../static/profileform.js");

const clone = (x) => JSON.parse(JSON.stringify(x));

// A stored profile that carries every key some block does not show: the legacy
// inline AWS binding, a GitHub binding's extra hosts, replies on a binary that
// has no replies row, and an option the config no longer defines.
const legacy = {
  name: "ops", bin: "bash", runtime: "box", default_cwd: "/home/you/work/api",
  replyable: true, imports_off: true,
  aws: { source_profile: "old", role_arn: "arn:aws:iam::111111111111:role/agent", region: "eu-west-1", label: "old-aws", egress_allow: ["sts.eu-west-1.amazonaws.com"] },
  github: { owners: [{ owner: "culiops", app: "bot", repos: ["api"] }], egress_allow: ["ghe.example.com"] },
  options: { foo: "bar" },
  rev: "r1",
};

// The aws-ops profile of the other tests: a box profile with something in every block.
const awsOps = {
  name: "aws-ops", bin: "claude", runtime: "box", account: "work", default_cwd: "/home/you/work/payments-api",
  options: { model: "claude-opus-5-5", effort: "high" },
  aws_connections: ["prod-read", "staging-read"], tokens: ["grafana-prod"],
  github: { owners: [{ owner: "culiops", app: "bot", repos: ["payments-api"] }], egress_allow: ["ghe.example.com"] },
  context_paths: ["/home/you/other-repo/docs"], egress_allow: ["api.example.com"], egress_open: true,
  base: "clean", args: ["--verbose"], env: { LOG: "1" }, secrets: ["TOKEN"], replyable: true,
  rev: "r1",
};

// --- pfvMergeBlock -----------------------------------------------------------------

test("mergeBlock keeps keys the block does not own", () => {
  const doc = clone(legacy);
  const out = pfvMergeBlock(doc, "agent", { bin: "bash", account: "", options: { model: "", effort: "" } });
  assert.deepStrictEqual(out, legacy);
  assert.deepStrictEqual(doc, legacy, "the loaded profile is not touched");
  // And the other blocks leave the agent's own keys alone.
  for (const id of ["runs", "reachable", "network"]) {
    const o = pfvMergeBlock(doc, id, {});
    assert.deepStrictEqual(o, legacy, id);
  }
});

test("mergeBlock returns a fresh object, nested values included", () => {
  const doc = clone(awsOps);
  const out = pfvMergeBlock(doc, "agent", {});
  assert.notStrictEqual(out, doc);
  assert.notStrictEqual(out.github, doc.github);
  assert.notStrictEqual(out.github.owners, doc.github.owners);
  out.github.owners[0].repos.push("x");
  out.options.model = "changed";
  assert.deepStrictEqual(doc, awsOps);
});

test("mergeBlock agent changes only model and effort", () => {
  const doc = clone(legacy);
  doc.bin = "claude";
  doc.options = { foo: "bar", model: "old" };
  const out = pfvMergeBlock(doc, "agent", { bin: "claude", account: "work", options: { model: "claude-opus-5-5", effort: "high" } });
  assert.deepStrictEqual(out, Object.assign(clone(doc), { account: "work", options: { foo: "bar", model: "claude-opus-5-5", effort: "high" } }));
  // An option that is not the agent's is not written by the agent block, even when asked.
  const again = pfvMergeBlock(doc, "agent", { options: { foo: "other", bypass: true } });
  assert.deepStrictEqual(again, doc);
});

test("mergeBlock agent clears an option the user emptied, keeps the one it was not given, and the default login leaves no account", () => {
  const doc = clone(awsOps);
  const cleared = pfvMergeBlock(doc, "agent", { account: "default", options: { effort: "" } });
  assert.strictEqual(cleared.account, undefined);
  assert.deepStrictEqual(cleared.options, { model: "claude-opus-5-5" });
  const none = pfvMergeBlock({ name: "p", bin: "claude", options: { model: "m" } }, "agent", { options: { model: "" } });
  assert.strictEqual("options" in none, false, "an empty options map is left out");
  // effortLevel is the same slot as effort in the Agent block.
  const lvl = pfvMergeBlock({ name: "p", bin: "claude" }, "agent", { options: { effortLevel: "low" } });
  assert.deepStrictEqual(lvl.options, { effortLevel: "low" });
});

test("mergeBlock reachable writes context_paths and egress_allow", () => {
  const doc = clone(awsOps);
  const out = pfvMergeBlock(doc, "reachable", {
    context_paths: ["/srv/docs", " /srv/more ", ""],
    egress_allow: ["a.example.com", "*.b.example.org", "a.example.com"],
  });
  assert.deepStrictEqual(out.context_paths, ["/srv/docs", "/srv/more"]);
  assert.deepStrictEqual(out.egress_allow, ["a.example.com", "*.b.example.org"]);
  assert.deepStrictEqual(Object.assign({}, out, { context_paths: doc.context_paths, egress_allow: doc.egress_allow }), doc, "nothing else moved");
  const emptied = pfvMergeBlock(doc, "reachable", { context_paths: [], egress_allow: [] });
  assert.strictEqual("context_paths" in emptied, false);
  assert.strictEqual("egress_allow" in emptied, false);
  const onlyPaths = pfvMergeBlock(doc, "reachable", { context_paths: ["/x"] });
  assert.deepStrictEqual(onlyPaths.egress_allow, ["api.example.com"], "a key the values do not name is kept");
});

test("mergeBlock runs sends the tier explicitly, and stays blank only with no box layer and no stored tier", () => {
  assert.strictEqual(pfvMergeBlock({ name: "p", bin: "claude" }, "runs", { tier: "box" }).runtime, "box");
  assert.strictEqual(pfvMergeBlock({ name: "p", bin: "claude", runtime: "box" }, "runs", { tier: "host" }).runtime, "host");
  const blank = pfvMergeBlock({ name: "p", bin: "claude" }, "runs", { tier: "box", boxAvailable: false });
  assert.strictEqual("runtime" in blank, false);
  const pinned = pfvMergeBlock({ name: "p", bin: "claude", runtime: "box" }, "runs", { tier: "box", boxAvailable: false });
  assert.strictEqual(pinned.runtime, "box", "a stored box stays");
});

test("mergeBlock workspace replaces the four workspace keys as a unit", () => {
  const copy = { name: "p", bin: "claude", runtime: "box", clone_of: "/src/repo", default_cwd: "/home/you/box-clones/repo", clone_share: [".env.example"], clone_no_auto_return: true, tokens: ["t"] };
  const folder = pfvMergeBlock(copy, "workspace", { mode: "folder", folder: "/home/you/work" });
  assert.deepStrictEqual(folder, { name: "p", bin: "claude", runtime: "box", default_cwd: "/home/you/work", tokens: ["t"] });
  const back = pfvMergeBlock(folder, "workspace", { mode: "copy", folder: "/src/repo", copyDir: "/home/you/box-clones/repo", shareText: ".env.example\n", autoReturn: false });
  assert.deepStrictEqual(back, copy);
  const auto = pfvMergeBlock(copy, "workspace", { mode: "copy", folder: "/src/repo", copyDir: "/home/you/box-clones/repo", shareText: "", autoReturn: true });
  assert.strictEqual("clone_no_auto_return" in auto, false);
  assert.strictEqual("clone_share" in auto, false);
  // No mode, no write: the block never guesses between a folder and a copy.
  assert.deepStrictEqual(pfvMergeBlock(copy, "workspace", { folder: "/x" }), copy);
});

test("mergeBlock cloud sorts and de-duplicates names, carries github.egress_allow, and only removes the legacy aws", () => {
  const doc = clone(legacy);
  const out = pfvMergeBlock(doc, "cloud", {
    aws_connections: ["staging-read", "prod-read", "prod-read"],
    tokens: ["b-token", "a-token"],
    github: { owners: [{ owner: "culiops", app: "bot", repos: ["api", "web"] }] },
  });
  assert.deepStrictEqual(out.aws_connections, ["prod-read", "staging-read"]);
  assert.deepStrictEqual(out.tokens, ["a-token", "b-token"]);
  assert.deepStrictEqual(out.github, { owners: [{ owner: "culiops", app: "bot", repos: ["api", "web"] }], egress_allow: ["ghe.example.com"] });
  assert.deepStrictEqual(out.aws, legacy.aws, "the inline binding is not rewritten");
  // A value for aws is not an edit of it; only an explicit null removes it.
  assert.deepStrictEqual(pfvMergeBlock(doc, "cloud", { aws: { region: "x" } }).aws, legacy.aws);
  assert.strictEqual("aws" in pfvMergeBlock(doc, "cloud", { aws: null }), false);
  // Binding extra hosts the values do state are theirs.
  const own = pfvMergeBlock(doc, "cloud", { github: { owners: [{ owner: "culiops", app: "bot", repos: [] }], egress_allow: ["x.example.com"] } });
  assert.deepStrictEqual(own.github.egress_allow, ["x.example.com"]);
  // Removing every owner row removes the binding, and nothing else.
  const gone = pfvMergeBlock(doc, "cloud", { github: null, tokens: [], aws_connections: [] });
  assert.deepStrictEqual(gone, Object.fromEntries(Object.entries(legacy).filter(([k]) => k !== "github")));
});

test("mergeBlock network writes egress_open and nothing else", () => {
  const doc = clone(awsOps);
  const off = pfvMergeBlock(doc, "network", { egress_open: false });
  assert.strictEqual("egress_open" in off, false);
  assert.deepStrictEqual(Object.assign({}, off, { egress_open: true }), doc);
  assert.strictEqual(pfvMergeBlock({ name: "p", bin: "claude" }, "network", { egress_open: true }).egress_open, true);
});

test("mergeBlock advanced writes its own keys, keeps what is hidden, and leaves the agent's options", () => {
  const doc = clone(awsOps);
  doc.options.bypass = true;
  const out = pfvMergeBlock(doc, "advanced", {
    base: "login",
    args: ["--a", " --b ", ""],
    envRows: [{ key: "LOG", value: " 2 ", kind: "literal" }, { key: "TOKEN", value: "", kind: "secret" }, { key: " ", value: "", kind: "literal" }],
    replies: { usable: true, checked: false },
    imports: { usable: true, checked: false },
    options: { bypass: false, model: "claude-haiku-5" },
  });
  assert.strictEqual(out.base, "login");
  assert.deepStrictEqual(out.args, ["--a", "--b"]);
  assert.deepStrictEqual(out.env, { LOG: "2" });
  assert.deepStrictEqual(out.secrets, ["TOKEN"]);
  assert.strictEqual("replyable" in out, false, "an unticked usable row turns replies off");
  assert.strictEqual(out.imports_off, true, "an unticked usable imports row turns imports off");
  assert.deepStrictEqual(out.options, { model: "claude-opus-5-5", effort: "high", bypass: false }, "a stored false stays a false, and model is the agent's");
});

test("mergeBlock advanced keeps the stored value of a row the user cannot use and of a base it did not show", () => {
  const doc = clone(awsOps);
  doc.imports_off = true;
  const out = pfvMergeBlock(doc, "advanced", { replies: { usable: false, checked: false }, imports: { usable: false, checked: true } });
  assert.strictEqual(out.replyable, true);
  assert.strictEqual(out.imports_off, true);
  assert.strictEqual(out.base, "clean", "base is not in the values on a box profile");
  assert.deepStrictEqual(out.args, ["--verbose"]);
  assert.deepStrictEqual(out.env, { LOG: "1" });
  assert.deepStrictEqual(out.secrets, ["TOKEN"]);
  const cleared = pfvMergeBlock(doc, "advanced", { args: [], envRows: [] });
  for (const k of ["args", "env", "secrets"]) assert.strictEqual(k in cleared, false, k);
  // A usable, ticked imports row has nothing to send.
  assert.strictEqual("imports_off" in pfvMergeBlock(doc, "advanced", { imports: { usable: true, checked: true } }), false);
});

test("mergeBlock advanced removes an option it is told to clear, the one a config no longer defines included", () => {
  const out = pfvMergeBlock(legacy, "advanced", { options: { foo: "" } });
  assert.strictEqual("options" in out, false);
  const kept = pfvMergeBlock(legacy, "advanced", { options: {} });
  assert.deepStrictEqual(kept.options, { foo: "bar" });
});

test("mergeBlock refuses a block it does not know instead of saving nothing", () => {
  assert.throws(() => pfvMergeBlock(awsOps, "canreach", {}), /unknown profile block: canreach/);
  assert.throws(() => pfvMergeBlock(awsOps, "", {}), /unknown profile block/);
});

// --- pfvBlockForField --------------------------------------------------------------

test("blockForField maps aws_connections[1] to cloud", () => {
  assert.strictEqual(pfvBlockForField("aws_connections[1]"), "cloud");
  assert.strictEqual(pfvBlockForField("aws_connections"), "cloud");
});

test("blockForField maps context_paths[0] to reachable and clone_share[2] to workspace", () => {
  assert.strictEqual(pfvBlockForField("context_paths[0]"), "reachable");
  assert.strictEqual(pfvBlockForField("clone_share[2]"), "workspace");
});

// Every path the server emits (Task 1), and the block that owns it.
test("blockForField maps every path the server emits to its block", () => {
  const rows = {
    runtime: "runs",
    default_cwd: "workspace", clone_of: "workspace", clone_share: "workspace", "clone_share[0]": "workspace", clone_no_auto_return: "workspace",
    bin: "agent", account: "agent",
    aws: "cloud", aws_connections: "cloud", "aws_connections[3]": "cloud", github: "cloud", "github.owners": "cloud",
    "github.owners[0].owner": "cloud", "github.owners[0].app": "cloud", "github.owners[1].token": "cloud", "github.owners[0].repos": "cloud",
    "github.permissions": "cloud", "github.egress_allow": "cloud", tokens: "cloud", "tokens[0]": "cloud",
    "context_paths[1]": "reachable", egress_allow: "reachable", "egress_allow[2]": "reachable",
    egress_open: "network",
    env: "advanced", "secrets[1]": "advanced", base: "advanced", args: "advanced", replyable: "advanced", imports_off: "advanced",
    name: "",
  };
  for (const [field, block] of Object.entries(rows)) assert.strictEqual(pfvBlockForField(field), block, field);
});

test("blockForField sends options.<key> to the agent for model and effort, otherwise to advanced", () => {
  assert.strictEqual(pfvBlockForField("options.model"), "agent");
  assert.strictEqual(pfvBlockForField("options.effort"), "agent");
  assert.strictEqual(pfvBlockForField("options.effortLevel"), "agent");
  assert.strictEqual(pfvBlockForField("options.bypass"), "advanced");
  assert.strictEqual(pfvBlockForField("options.foo"), "advanced");
  assert.strictEqual(pfvBlockForField("options.permission-mode"), "advanced");
  assert.strictEqual(pfvBlockForField("options"), "", "no key, no owner");
});

test("blockForField says nothing for a path it does not know", () => {
  for (const f of ["", "box.image", "constructor", "__proto__", "toString", "[0]", ".model", null, undefined, 7]) assert.strictEqual(pfvBlockForField(f), "", String(f));
});

// The Agent block's options and the read view's Advanced count are one list: an
// option the Agent block owns must not be counted as an Advanced setting.
test("the agent's option keys are the ones the read view leaves out of Advanced", () => {
  for (const key of ["model", "effort", "effortLevel", "bypass", "foo"]) {
    const countedAsAdvanced = pfvAdvancedCount({ name: "p", bin: "claude", options: { [key]: "x" } }, "box") === 1;
    assert.strictEqual(pfvBlockForField("options." + key) === "advanced", countedAsAdvanced, key);
  }
});

// --- pfvGuard ----------------------------------------------------------------------

test("guard refuses editing a second block while dirty", () => {
  assert.strictEqual(pfvGuard({ openBlock: "agent", dirty: true, action: "edit", target: "network" }), "refuse");
  assert.strictEqual(pfvGuard({ openBlock: "agent", dirty: true, action: "edit", target: "agent" }), "go", "the block that is already open");
  assert.strictEqual(pfvGuard({ openBlock: "agent", dirty: false, action: "edit", target: "network" }), "go");
  assert.strictEqual(pfvGuard({ openBlock: "", dirty: false, action: "edit", target: "agent" }), "go");
});

test("guard confirms leaving while dirty and goes when clean", () => {
  assert.strictEqual(pfvGuard({ openBlock: "agent", dirty: true, action: "leave" }), "confirm");
  assert.strictEqual(pfvGuard({ openBlock: "agent", dirty: true, action: "leave", target: "other-profile" }), "confirm");
  assert.strictEqual(pfvGuard({ openBlock: "agent", dirty: false, action: "leave" }), "go");
  assert.strictEqual(pfvGuard({ openBlock: "", dirty: false, action: "leave" }), "go");
  assert.strictEqual(pfvGuard({ dirty: true, action: "leave" }), "confirm");
  // A change is never dropped on a word the guard does not know.
  assert.strictEqual(pfvGuard({ openBlock: "agent", dirty: true, action: "esc" }), "confirm");
  assert.strictEqual(pfvGuard({ openBlock: "agent", dirty: false, action: "esc" }), "go");
  assert.strictEqual(pfvGuard(), "go");
});

// --- the host switch ---------------------------------------------------------------

test("host removals list every bound kind in order", () => {
  const doc = {
    name: "p", bin: "claude", runtime: "box",
    context_paths: ["/srv/a", "/srv/b"], egress_allow: ["x.example.com"], egress_open: true,
    tokens: ["grafana-prod"], github: { owners: [{ owner: "culiops", app: "bot" }, { owner: "other", token: "pat" }] },
    aws: { source_profile: "s", role_arn: "arn:aws:iam::1:role/r", region: "eu-west-1", label: "old-aws" },
    aws_connections: ["prod-read", "staging-read"],
  };
  assert.deepStrictEqual(pfvHostRemovals(doc), [
    { kind: "AWS connections", names: ["prod-read", "staging-read"] },
    { kind: "Old-style AWS", names: ["old-aws"] },
    { kind: "GitHub", names: ["culiops", "other"] },
    { kind: "Tokens", names: ["grafana-prod"] },
    { kind: "Readable folders", names: ["/srv/a", "/srv/b"] },
    { kind: "Domains", names: ["x.example.com"] },
    { kind: "Network settings", names: [] },
  ]);
});

test("host removals name only the kinds the profile has", () => {
  assert.deepStrictEqual(pfvHostRemovals({ name: "p", bin: "claude" }), []);
  assert.deepStrictEqual(pfvHostRemovals({ name: "p", bin: "claude", tokens: ["t"], egress_open: false }), [{ kind: "Tokens", names: ["t"] }]);
  assert.deepStrictEqual(pfvHostRemovals({ name: "p", bin: "claude", aws: { source_profile: "s", region: "r" } }), [{ kind: "Old-style AWS", names: [] }]);
  assert.deepStrictEqual(pfvHostRemovals({ name: "p", bin: "claude", github: { owners: [] } }), [{ kind: "GitHub", names: [] }]);
  assert.deepStrictEqual(pfvHostRemovals(null), []);
});

test("toHost removes them and sets runtime host", () => {
  const doc = {
    name: "p", bin: "claude", runtime: "box", account: "work", default_cwd: "/home/you/w", options: { model: "m" },
    base: "clean", args: ["-x"], env: { A: "1" }, secrets: ["S"], replyable: true, imports_off: true, rev: "r7",
    context_paths: ["/srv/a"], egress_allow: ["x.example.com"], egress_open: true, tokens: ["t"], github: { owners: [{ owner: "o", app: "a" }] },
    aws: { source_profile: "s" }, aws_connections: ["c"],
  };
  const before = clone(doc);
  const out = pfvToHost(doc);
  assert.deepStrictEqual(doc, before, "the loaded profile is not touched");
  assert.notStrictEqual(out, doc);
  assert.deepStrictEqual(out, {
    name: "p", bin: "claude", runtime: "host", account: "work", default_cwd: "/home/you/w", options: { model: "m" },
    base: "clean", args: ["-x"], env: { A: "1" }, secrets: ["S"], replyable: true, imports_off: true, rev: "r7",
  });
  assert.deepStrictEqual(pfvHostRemovals(out), []);
  assert.strictEqual(pfvToHost({ name: "q", bin: "claude" }).runtime, "host");
});

test("host refusal for a private copy", () => {
  const msg = "A private copy needs a box. Set Workspace to 'Work in this folder' first.";
  assert.strictEqual(pfvHostRefusal({ name: "p", clone_of: "/src/repo", default_cwd: "/home/you/box-clones/repo" }), msg);
  assert.strictEqual(pfvHostRefusal({ name: "p", default_cwd: "/home/you/work" }), "");
  assert.strictEqual(pfvHostRefusal({ name: "p", clone_of: "" }), "");
  assert.strictEqual(pfvHostRefusal(null), "");
});

// --- the GitHub preset -------------------------------------------------------------

test("GitHub preset is read-only for the default set and custom for statuses on", () => {
  assert.strictEqual(pfvGhPreset(ghPermLevels(null)), "read-only");
  assert.strictEqual(pfvGhPreset(ghPermLevels({ permissions: { actions: "read", checks: "read", contents: "read", pull_requests: "read", issues: "read", metadata: "read" } })), "read-only");
  assert.strictEqual(pfvGhPreset(Object.assign(ghPermLevels(null), { statuses: "read" })), "custom");
  assert.strictEqual(pfvGhPreset(Object.assign(ghPermLevels(null), { contents: "write" })), "custom");
  assert.strictEqual(pfvGhPreset(Object.assign(ghPermLevels(null), { issues: "off" })), "custom");
  assert.strictEqual(pfvGhPreset({}), "custom");
  assert.strictEqual(pfvGhPreset(null), "custom");
  // A key the levels leave out is off.
  const { statuses, ...six } = ghPermLevels(null);
  assert.strictEqual(statuses, "off");
  assert.strictEqual(pfvGhPreset(six), "read-only");
});

test("the preset's levels are the default set, every key read and the rest off", () => {
  const levels = pfvGhPresetLevels();
  for (const k of GH_PERMS) assert.strictEqual(levels[k], GH_DEFAULT_PERMS.includes(k) ? "read" : "off", k);
  assert.deepStrictEqual(Object.keys(levels).sort(), [...GH_PERMS].sort());
  assert.strictEqual(pfvGhPreset(levels), "read-only");
  levels.contents = "write";
  assert.strictEqual(pfvGhPresetLevels().contents, "read", "a fresh object every call");
});

// The client's copy of the server's permission lists is what the preset and the
// write toggles stand on. If the server's change, this is the test that says so.
test("the client's GitHub permission lists are the server's", () => {
  const go = fs.readFileSync(path.join(__dirname, "..", "..", "config", "github.go"), "utf8");
  const list = (name) => {
    const m = new RegExp("var " + name + " = \\[\\]string\\{([^}]*)\\}").exec(go);
    assert.ok(m, name + " not found in github.go");
    return [...m[1].matchAll(/"([a-z_]+)"/g)].map((x) => x[1]);
  };
  assert.deepStrictEqual(GH_PERMS, list("githubPermissionKeys"));
  assert.deepStrictEqual(GH_WRITABLE, list("githubWritableKeys"));
  const body = /func DefaultGitHubPermissions\(\) map\[string\]string \{\s*return map\[string\]string\{([^}]*)\}/.exec(go);
  assert.ok(body, "DefaultGitHubPermissions not found in github.go");
  const pairs = [...body[1].matchAll(/"([a-z_]+)":\s*"([a-z]+)"/g)];
  assert.ok(pairs.length > 0);
  assert.ok(pairs.every((p) => p[2] === "read"), "the server's default set is all read");
  assert.deepStrictEqual(GH_DEFAULT_PERMS, pairs.map((p) => p[1]));
  assert.deepStrictEqual(Object.keys(pfvGhPresetLevels()).filter((k) => pfvGhPresetLevels()[k] === "read"), pairs.map((p) => p[1]));
});

// --- pfvSaveError ------------------------------------------------------------------

test("save error 409 profile_changed is stale", () => {
  const body = { error: "this profile was changed after you opened it (another tab, another device or a restart); nothing was saved", code: "profile_changed" };
  assert.deepStrictEqual(pfvSaveError(409, body), { kind: "stale", block: "", field: "", text: "This profile changed elsewhere" });
  // gone is only ever the boolean true (profilerev.go writeSaveErr); anything else is a changed profile.
  for (const gone of [false, "true", 1, undefined, null]) assert.strictEqual(pfvSaveError(409, Object.assign({ gone }, body)).kind, "stale", String(gone));
});

test("save error 409 profile_changed with gone is its own kind, not stale", () => {
  // What the server sends for a profile deleted after the page opened it.
  const body = { error: "this profile no longer exists (it was deleted after you opened it); nothing was saved", code: "profile_changed", gone: true };
  const e = pfvSaveError(409, body);
  assert.deepStrictEqual(e, { kind: "gone", block: "", field: "", text: "This profile was deleted elsewhere" });
  // Only a 409 profile_changed can be gone.
  assert.strictEqual(pfvSaveError(400, { error: "x", gone: true }).kind, "block");
  assert.strictEqual(pfvSaveError(409, { error: "x", code: "profile_exists", gone: true }).kind, "block");
});

test("save error 400 with field maps to its block", () => {
  const body = { error: "context_paths /home/you/old-docs: does not exist", field: "context_paths[1]" };
  assert.deepStrictEqual(pfvSaveError(400, body), { kind: "field", block: "reachable", field: "context_paths[1]", text: "This folder no longer exists. Remove it or pick another." });
  assert.strictEqual(pfvSaveError(400, { error: "x", field: "aws_connections[0]" }).block, "cloud");
  assert.strictEqual(pfvSaveError(400, { error: "x", field: "options.model" }).block, "agent");
  assert.strictEqual(pfvSaveError(400, { error: "x", field: "options.foo" }).block, "advanced");
  assert.strictEqual(pfvSaveError(400, { error: "x", field: "clone_of" }).block, "workspace");
  // A field no block owns is still a field refusal, with no block to open.
  assert.deepStrictEqual(pfvSaveError(400, { error: "name is invalid", field: "name" }), { kind: "field", block: "", field: "name", text: "name is invalid" });
});

test("save error without a field, or of any other kind, is block-level text", () => {
  const plain = { error: 'profile "p": box.image is not per-profile' };
  assert.deepStrictEqual(pfvSaveError(400, plain), { kind: "block", block: "", field: "", text: "box.image is not per-profile" });
  // The other 409s are not cured by a reload of the profile: they keep the server's sentence.
  for (const code of ["profile_exists", "page_outdated", "profiles_file_changed"]) {
    assert.deepStrictEqual(pfvSaveError(409, { error: "the sentence", code }), { kind: "block", block: "", field: "", text: "the sentence" }, code);
  }
  assert.strictEqual(pfvSaveError(428, { error: "this page is out of date" }).text, "this page is out of date");
  // No sentence, no body: still words, never a blank line or a throw.
  for (const body of [undefined, null, {}, "oops", { error: "  " }]) {
    const e = pfvSaveError(500, body);
    assert.strictEqual(e.kind, "block");
    assert.ok(e.text.length > 0);
  }
  // A field with no sentence still says something.
  assert.ok(pfvSaveError(400, { field: "tokens[0]" }).text.length > 0);
});

// --- I1: the GitHub binding is checked before it is merged ------------------------------------

test("githubError passes a binding with no permissions, which the server reads as the six read defaults", () => {
  const owners = [{ owner: "culiops", app: "bot", repos: ["api"] }];
  assert.strictEqual(pfvGithubError({ owners }), "");
  assert.strictEqual(pfvGithubError({ owners, permissions: null }), "");
  assert.strictEqual(pfvGithubError(null), "", "no binding at all is nothing to check");
  assert.strictEqual(pfvGithubError(undefined), "");
});

test("githubError refuses an empty permission set with ghFormError's sentence", () => {
  const owners = [{ owner: "culiops", app: "bot", repos: ["api"] }];
  const sentence = ghFormError({ owners: [{ owner: "culiops", app: "bot", repos: "api" }], permissions: {} });
  assert.ok(sentence.length > 0, "the old form's sentence");
  assert.strictEqual(pfvGithubError({ owners, permissions: {} }), sentence);
  // The builder really does produce that binding when nothing is ticked.
  const built = ghBindingFromForm({ owners: [{ owner: "culiops", app: "bot", repos: "api" }], permissions: { actions: "off", contents: "off" } });
  assert.deepStrictEqual(built.permissions, {});
  assert.strictEqual(pfvGithubError(built), sentence);
  // A permission map that is not a map is not a set either.
  assert.strictEqual(pfvGithubError({ owners, permissions: [] }), sentence);
});

test("githubError lets only contents, pull_requests and issues be write", () => {
  const owners = [{ owner: "culiops", app: "bot", repos: ["api"] }];
  const ok = { contents: "write", pull_requests: "write", issues: "write", actions: "read", checks: "read", metadata: "read", statuses: "read" };
  assert.strictEqual(pfvGithubError({ owners, permissions: ok }), "");
  for (const k of GH_PERMS.filter((x) => !GH_WRITABLE.includes(x))) {
    const msg = pfvGithubError({ owners, permissions: { contents: "read", [k]: "write" } });
    assert.ok(msg.length > 0, k + " can not be write");
    assert.ok(msg.includes(k.replace(/_/g, " ")), "names " + k + ": " + msg);
  }
  // The sentence names every writable key in words, and none as a config key.
  const msg = pfvGithubError({ owners, permissions: { statuses: "write", checks: "write" } });
  assert.ok(msg.includes("statuses") && msg.includes("checks"), msg);
  assert.ok(msg.includes("contents") && msg.includes("pull requests") && msg.includes("issues"), msg);
  assert.ok(!/[_"\[]/.test(msg), "plain words: " + msg);
  // The builder never sends that, so a binding it built passes.
  const built = ghBindingFromForm({ owners: [{ owner: "culiops", app: "bot", repos: "api" }], permissions: { contents: "write", statuses: "write" } });
  assert.strictEqual(built.permissions.statuses, "read");
  assert.strictEqual(pfvGithubError(built), "");
});

// A personal access token is not narrowed by the permission set (the page says so
// on its row), and today's check does not look at it: the set must still be valid.
test("githubError applies to a token owner the same way", () => {
  const pat = [{ owner: "culiops", token: "gh-pat", repos: ["api"] }];
  assert.strictEqual(pfvGithubError({ owners: pat }), "");
  assert.strictEqual(pfvGithubError({ owners: pat, permissions: { contents: "write" } }), "");
  assert.ok(pfvGithubError({ owners: pat, permissions: {} }).length > 0);
  assert.ok(pfvGithubError({ owners: pat, permissions: { actions: "write" } }).length > 0);
});

// The refusal is the caller's: an empty set must reach the server's own check as
// it is, because deleting it here would turn "none ticked" into the six read defaults.
test("mergeBlock cloud does not repair a permission set, it sends what it is given", () => {
  const doc = { name: "p", bin: "claude", runtime: "box" };
  const empty = pfvMergeBlock(doc, "cloud", { github: { owners: [{ owner: "o", app: "a", repos: ["r"] }], permissions: {} } });
  assert.deepStrictEqual(empty.github.permissions, {});
  const bad = pfvMergeBlock(doc, "cloud", { github: { owners: [{ owner: "o", app: "a", repos: ["r"] }], permissions: { actions: "write" } } });
  assert.deepStrictEqual(bad.github.permissions, { actions: "write" });
});

// --- I2: a refusal reads in plain words -----------------------------------------------------------

// Real sentences, as the server words them (internal/config, internal/box, internal/web),
// with the field path each is tagged with.
const refusals = {
  folderGone: [
    { field: "context_paths[1]", error: "context_paths /home/you/old-docs: does not exist" },
    { field: "context_paths[0]", error: 'context_paths "/srv/bad": does not exist' },
  ],
  credentialMissing: [
    { field: "aws_connections[1]", error: 'profile "p": aws_connections[1] "staging-read" is not defined in aws_connections' },
    { field: "tokens[0]", error: 'profile "p": tokens[0] "nope" is not defined in static_tokens' },
    { field: "github.owners[0].app", error: 'profile "p": github.owners[0].app "missing" is not defined in github_apps' },
    { field: "github.owners[2].token", error: 'profile "p": github.owners[2].token "pat" is not defined in static_tokens' },
  ],
  relativePath: [
    { field: "context_paths[1]", error: 'profile "p": context_paths "relative/dir" must be an absolute path' },
    { field: "context_paths[0]", error: "context_paths rel/dir: must be an absolute path" },
    { field: "clone_of", error: 'clone_of "src/repo" must be an absolute path' },
    { field: "default_cwd", error: 'default_cwd "work" must be an absolute path' },
  ],
  needsBox: [
    { field: "aws", error: "aws binding requires runtime: box (set it explicitly); the host tier cannot contain credentials", kind: "Old-style AWS" },
    { field: "aws_connections", error: 'profile "p": aws_connections require runtime: box (set it explicitly); the host tier cannot contain credentials', kind: "AWS connections" },
    { field: "tokens", error: "tokens require runtime: box (set it explicitly); the host tier cannot contain credentials", kind: "Tokens" },
    { field: "github", error: "github binding requires runtime: box (set it explicitly); the host tier cannot contain credentials", kind: "GitHub" },
    { field: "context_paths", error: 'context_paths needs runtime "box"; host sessions can already read any path', kind: "Readable folders" },
    { field: "egress_allow", error: "egress_allow and egress_open need runtime: box (set it explicitly)", kind: "Domains" },
    { field: "egress_open", error: "egress_allow and egress_open need runtime: box (set it explicitly)", kind: "Network settings" },
  ],
  privateCopyOnHost: [
    { field: "clone_of", error: 'clone mode runs only in a box; remove runtime "host" or clone_of' },
    { field: "clone_of", error: 'clone mode needs runtime "box" (this instance defaults to host)' },
  ],
};
const textOf = (r) => pfvSaveError(400, { error: r.error, field: r.field }).text;

test("save error words an undefined credential, a vanished folder and a relative path plainly", () => {
  for (const r of refusals.folderGone) assert.strictEqual(textOf(r), "This folder no longer exists. Remove it or pick another.", r.error);
  for (const r of refusals.credentialMissing) assert.strictEqual(textOf(r), "Not found in Credentials. Remove it or create it.", r.error);
  for (const r of refusals.relativePath) assert.strictEqual(textOf(r), "Use a full path that starts with /", r.error);
});

test("save error words a box-only setting refused on host with the read view's name for it", () => {
  const all = {
    aws: { source_profile: "s" }, aws_connections: ["c"], github: { owners: [] }, tokens: ["t"],
    context_paths: ["/srv/a"], egress_allow: ["x.example.com"], egress_open: true,
  };
  const kinds = pfvHostRemovals(all).map((x) => x.kind);
  for (const r of refusals.needsBox) {
    const text = textOf(r);
    assert.ok(kinds.includes(r.kind), r.kind + " is a read-view word for what the host switch removes");
    assert.strictEqual(text, r.kind + " can't be used when Runs in is On this machine. Set Runs in to Box, or remove the setting.", r.error);
  }
  // A private copy has its own approved sentence.
  for (const r of refusals.privateCopyOnHost) assert.strictEqual(textOf(r), pfvHostRefusal({ clone_of: "/x" }), r.error);
});

test("no known refusal class shows a key, an underscore, an index or a quote", () => {
  let n = 0;
  for (const rows of Object.values(refusals)) {
    for (const r of rows) {
      const text = textOf(r);
      n++;
      assert.ok(text.length > 0, r.error);
      assert.ok(!/[\[\]_"]/.test(text), "plain words: " + text);
    }
  }
  assert.ok(n >= 15);
  for (const text of ["This profile changed elsewhere", "This profile was deleted elsewhere"]) assert.ok(!/[\[\]_"]/.test(text));
});

test("save error recognises a class by the field as well as the sentence", () => {
  // The same words under another field are not that class: the server's own sentence stays.
  const s = 'tokens[0] "nope" is not defined in static_tokens';
  assert.strictEqual(pfvSaveError(400, { error: s, field: "env" }).text, s);
  assert.strictEqual(pfvSaveError(400, { error: "context_paths /a: cannot be read (denied)", field: "context_paths[0]" }).text, "context_paths /a: cannot be read (denied)");
  assert.strictEqual(pfvSaveError(400, { error: "tokens[1] \"t\" is listed twice", field: "tokens[1]" }).text, 'tokens[1] "t" is listed twice');
  // A relative-path sentence is a path class only under the path fields.
  assert.strictEqual(pfvSaveError(400, { error: "env must be an absolute path", field: "env" }).text, "env must be an absolute path");
});

test("save error strips the leading profile name from any other sentence and leaves the rest as the server wrote it", () => {
  const e = (error, field) => pfvSaveError(400, field ? { error, field } : { error }).text;
  assert.strictEqual(e('profile "p": agent accounts are not configured', "account"), "agent accounts are not configured");
  assert.strictEqual(e('profile "p": box.image is not per-profile; every box runs the instance-wide box_image'), "box.image is not per-profile; every box runs the instance-wide box_image");
  // A quote in the name is escaped by the server (%q), and the whole prefix still goes.
  assert.strictEqual(e('profile "a\\"b": env CLAUDE_CONFIG_DIR is set by the agent account, not the profile', "env"), "env CLAUDE_CONFIG_DIR is set by the agent account, not the profile");
  // Only a leading prefix, and only the one: a name in the middle of a sentence stays.
  assert.strictEqual(e('the profile "p": is mentioned here'), 'the profile "p": is mentioned here');
  assert.strictEqual(e('profile "p" has invalid base "x" (want clean|login|inherit)', "base"), 'profile "p" has invalid base "x" (want clean|login|inherit)');
  // Nothing but the prefix leaves the fallback, not a blank line.
  assert.ok(e('profile "p": ').length > 0);
  // Other 400s and the other 409s lose it as well.
  assert.strictEqual(pfvSaveError(409, { error: 'profile "p": the sentence', code: "profile_exists" }).text, "the sentence");
});

// --- M1: omitted means keep, in the workspace too ------------------------------------------------------

test("mergeBlock workspace in copy mode keeps the share list and the auto-return opt-out it was not given", () => {
  const copy = { name: "p", bin: "claude", runtime: "box", clone_of: "/src/repo", default_cwd: "/home/you/box-clones/repo", clone_share: [".env.example", "docs"], clone_no_auto_return: true };
  const out = pfvMergeBlock(copy, "workspace", { mode: "copy", folder: "/src/other", copyDir: "/home/you/box-clones/other" });
  assert.deepStrictEqual(out, Object.assign({}, copy, { clone_of: "/src/other", default_cwd: "/home/you/box-clones/other" }));
  // An undefined is no more a word than an omission is.
  assert.deepStrictEqual(pfvMergeBlock(copy, "workspace", { mode: "copy", folder: "/src/repo", copyDir: copy.default_cwd, shareText: undefined, autoReturn: undefined }), copy);
  // What it is given, it writes: false opts out, true opts back in, "" clears the list.
  assert.strictEqual(pfvMergeBlock(copy, "workspace", { mode: "copy", folder: "/src/repo", copyDir: copy.default_cwd, autoReturn: true }).clone_no_auto_return, undefined);
  assert.strictEqual("clone_share" in pfvMergeBlock(copy, "workspace", { mode: "copy", folder: "/src/repo", copyDir: copy.default_cwd, shareText: "" }), false);
  const plainCopy = Object.fromEntries(Object.entries(copy).filter(([k]) => k !== "clone_no_auto_return" && k !== "clone_share"));
  assert.deepStrictEqual(pfvMergeBlock(plainCopy, "workspace", { mode: "copy", folder: "/src/repo", copyDir: copy.default_cwd }), plainCopy, "nothing stored, nothing invented");
  assert.strictEqual(pfvMergeBlock(plainCopy, "workspace", { mode: "copy", folder: "/src/repo", copyDir: copy.default_cwd, autoReturn: false }).clone_no_auto_return, true);
  // Switching to a plain folder still drops the copy keys: that is what the mode means.
  assert.deepStrictEqual(pfvMergeBlock(copy, "workspace", { mode: "folder", folder: "/home/you/work" }), { name: "p", bin: "claude", runtime: "box", default_cwd: "/home/you/work" });
});

// --- the page's one scope --------------------------------------------------------------

// The page's scripts share ONE global scope: a name declared twice means the later
// file silently wins, or (for const and let) the whole script fails to load.
// profileview.js sits beside this file with its own pfv names, so this is the test
// that says when one is taken.
test("no top-level name in profileedit.js is also declared by another page script", () => {
  const dir = path.join(__dirname, "..", "static");
  const decl = /^(?:async\s+)?(?:function\s+|const\s+|let\s+|var\s+)([A-Za-z_$][\w$]*)/gm;
  const names = (src) => new Set([...src.matchAll(decl)].map((m) => m[1]));
  const mine = names(fs.readFileSync(path.join(dir, "profileedit.js"), "utf8"));
  assert.ok(mine.size > 10);
  const clashes = [];
  for (const f of fs.readdirSync(dir).filter((x) => x.endsWith(".js") && x !== "profileedit.js")) {
    for (const n of names(fs.readFileSync(path.join(dir, f), "utf8"))) if (mine.has(n)) clashes.push(n + " (" + f + ")");
  }
  assert.deepStrictEqual(clashes, []);
});

test("index.html loads profileedit.js once, after profileview.js and before the page script", () => {
  const html = fs.readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");
  const tags = [...html.matchAll(/<script src="\/([a-z-]+\.js)"><\/script>/g)].map((m) => m[1]);
  assert.strictEqual(tags.filter((t) => t === "profileedit.js").length, 1);
  assert.ok(tags.indexOf("profileedit.js") > tags.indexOf("profileview.js"));
  assert.ok(tags.indexOf("profileedit.js") < tags.indexOf("profilepage.js"));
});
