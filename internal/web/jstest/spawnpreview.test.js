"use strict";
// spawnpreview.js: the New session dialog's pure decisions (spec 5.6).
const test = require("node:test");
const assert = require("node:assert");
const { profileTouchLine, reachRows, reachModel, pickSpawnProfile, pickSpawnCwd, spawnNamePlaceholder, spawnHotkeyAction, ghReachValue } = require("../static/spawnpreview.js");

const sre = {
  name: "culipulse-sre", runtime: "box", bin: "claude", claude: true,
  aws_connections: ["culipulse-prod"], github_owners: ["culiops"], network: "allowlist",
};
const meta = { aws_connections: [{ name: "culipulse-prod", prod: true, write_capable: true }] };
const creds = (over) => Object.assign({
  store: { initialized: true, locked: false, platform: "linux" },
  aws: [{ name: "culipulse-prod", prod: true, elevate: { role_arn: "arn:x", max_minutes: 60, allow_iam: false } }],
}, over);
const byK = (rows, k) => rows.filter((r) => r.k === k);

test("touch line: box with aws and gh", () => {
  assert.strictEqual(profileTouchLine(sre), "box · aws culipulse-prod · gh culiops");
});

test("touch line: box with several names, clone and nothing", () => {
  assert.strictEqual(profileTouchLine({ runtime: "box", aws_connections: ["a", "b"] }), "box · aws a, b");
  assert.strictEqual(profileTouchLine({ runtime: "box", clone_of: "learning-hubs" }), "box · private copy of learning-hubs");
  assert.strictEqual(profileTouchLine({ runtime: "box" }), "box · no credentials");
  assert.strictEqual(profileTouchLine({}), "box · no credentials");
});

test("touch line: host claude is not contained, host bash says bash", () => {
  assert.strictEqual(profileTouchLine({ runtime: "host", bin: "claude", claude: true }), "host · not contained");
  assert.strictEqual(profileTouchLine({ runtime: "host", bin: "bash" }), "host · bash");
  assert.strictEqual(profileTouchLine({ runtime: "host", bin: "/bin/zsh" }), "host · zsh");
});

test("touch line: no box layer means host whatever the profile says", () => {
  assert.strictEqual(profileTouchLine(sre, ""), "host · not contained");
  assert.strictEqual(profileTouchLine(sre, "sysbox-runc"), "box · aws culipulse-prod · gh culiops");
});

test("reach rows: a boxed claude session with a PROD connection (max window 60)", () => {
  const rows = reachRows({ profile: sre, tier: "box", cwd: "~/culipulse", meta, creds: creds() });
  assert.deepStrictEqual(rows.map((r) => r.k), ["Files", "AWS", "GitHub", "Network", "Account"]);
  assert.strictEqual(byK(rows, "Files")[0].v, "~/culipulse read/write · nothing else on this machine");
  const aws = byK(rows, "AWS")[0];
  assert.strictEqual(aws.v, "culipulse-prod · read · write only after you elevate (max 60 min)");
  assert.strictEqual(aws.prod, true);
  assert.strictEqual(aws.name, "culipulse-prod");
  assert.strictEqual(byK(rows, "GitHub")[0].v, "culiops/* · read, 1-hour tokens");
  assert.strictEqual(byK(rows, "Network")[0].v, "allowlist only");
  assert.strictEqual(byK(rows, "Account")[0].v, "default (~/.claude)");
});

test("reach rows: a connection that cannot write says read only; not PROD has no tag", () => {
  const rows = reachRows({
    profile: { claude: true, aws_connections: ["billing"] }, tier: "box", cwd: "~/x",
    meta: { aws_connections: [{ name: "billing", prod: false, write_capable: false }] }, creds: null,
  });
  const aws = byK(rows, "AWS")[0];
  assert.strictEqual(aws.v, "billing · read only");
  assert.ok(!aws.prod);
});

test("reach rows: write-capable with no credentials answer omits the window, never invents one", () => {
  const rows = reachRows({ profile: sre, tier: "box", cwd: "~/x", meta, creds: null });
  assert.strictEqual(byK(rows, "AWS")[0].v, "culipulse-prod · read · write only after you elevate");
  assert.strictEqual(byK(rows, "Store").length, 0, "no creds answer, no Store row, no throw");
});

test("reach rows: a locked store shows before spawn when the profile has a binding", () => {
  const locked = creds({ store: { initialized: true, locked: true, platform: "linux" } });
  const rows = reachRows({ profile: sre, tier: "box", cwd: "~/x", meta, creds: locked });
  const st = byK(rows, "Store");
  assert.strictEqual(st.length, 1);
  assert.strictEqual(st[0].v, "credential store locked — unlock before spawn");
  assert.strictEqual(st[0].tone, "warn");
});

test("reach rows: a locked store is silent for a profile with no binding, an unlocked one, a keychain", () => {
  const locked = creds({ store: { initialized: true, locked: true, platform: "linux" } });
  const bare = { name: "p", claude: true, runtime: "box" };
  assert.strictEqual(byK(reachRows({ profile: bare, tier: "box", cwd: "~/x", meta, creds: locked }), "Store").length, 0);
  assert.strictEqual(byK(reachRows({ profile: sre, tier: "box", cwd: "~/x", meta, creds: creds() }), "Store").length, 0);
  const mac = creds({ store: { initialized: true, locked: true, platform: "darwin" } });
  assert.strictEqual(byK(reachRows({ profile: sre, tier: "box", cwd: "~/x", meta, creds: mac }), "Store").length, 0);
});

test("reach rows: host reaches the whole account and is not contained", () => {
  const rows = reachRows({ profile: { name: "claude", runtime: "host", claude: true }, tier: "host", cwd: "~/x", meta, creds: null });
  assert.strictEqual(byK(rows, "Files")[0].v, "your whole user account");
  assert.strictEqual(byK(rows, "Network")[0].v, "not contained");
  assert.strictEqual(byK(rows, "AWS").length, 0);
});

test("reach rows: containment is never coloured (D23): host rows carry no tone; red is only the PROD flag", () => {
  const host = reachRows({ profile: { name: "claude", runtime: "host", claude: true }, tier: "host", cwd: "~/x" });
  for (const r of host) assert.ok(!r.tone, r.k + " must be neutral");
  const box = reachRows({ profile: sre, tier: "box", cwd: "~/x", meta, creds: creds() });
  for (const r of box) assert.ok(!r.tone, r.k);
  assert.ok(!/\.sr-row[^{]*\{[^}]*--red/.test(read("desktop.css")));
});

test("reach rows: network open, named account, tokens, non-claude has no Account row", () => {
  const rows = reachRows({
    profile: { claude: true, account: "work", network: "open", tokens: ["npm"] }, tier: "box", cwd: "~/x", meta, creds: null,
  });
  assert.strictEqual(byK(rows, "Network")[0].v, "open");
  assert.strictEqual(byK(rows, "Account")[0].v, "work");
  assert.strictEqual(byK(rows, "Tokens")[0].v, "npm");
  assert.strictEqual(byK(reachRows({ profile: { name: "codex" }, tier: "box", cwd: "~/x" }), "Account").length, 0);
});

test("reach rows: nothing at all is still a list, never a throw", () => {
  assert.ok(Array.isArray(reachRows({})));
  assert.ok(Array.isArray(reachRows()));
});

test("pickSpawnProfile: the last used one when it still exists", () => {
  const ps = [{ name: "bash" }, { name: "claude" }, { name: "sre" }];
  assert.strictEqual(pickSpawnProfile(ps, { agent: "sre" }, "bash"), "sre");
});

test("pickSpawnProfile: never the shell profile when another exists", () => {
  const ps = [{ name: "bash" }, { name: "claude" }, { name: "sre" }];
  assert.strictEqual(pickSpawnProfile(ps, { agent: "bash" }, "bash"), "claude", "last used was the shell");
  assert.strictEqual(pickSpawnProfile(ps, null, "bash"), "claude", "no last used");
  assert.strictEqual(pickSpawnProfile(ps, { agent: "gone" }, "bash"), "claude", "last used no longer exists");
});

test("pickSpawnProfile: only the shell profile exists, or nothing", () => {
  assert.strictEqual(pickSpawnProfile([{ name: "bash" }], null, "bash"), "bash");
  assert.strictEqual(pickSpawnProfile([], null, "bash"), "");
  assert.strictEqual(pickSpawnProfile(undefined, null, ""), "");
});

const WS = [{ path: "/home/u/proj", label: "~/proj", boxable: true }];

test("pickSpawnCwd: the last spawn of ANOTHER profile is not prefilled", () => {
  const last = { agent: "bash", cwd: "~/ab-trust-verify/ws-host" };
  const cwd = pickSpawnCwd({ profile: { name: "claude" }, last, workspaces: WS, tier: "box" });
  assert.notStrictEqual(cwd, "~/ab-trust-verify/ws-host");
  assert.strictEqual(cwd, "/home/u/proj", "falls to the first usable workspace");
});

test("pickSpawnCwd: the same profile's last directory, the profile default beating it", () => {
  const last = { agent: "claude", cwd: "/home/u/other" };
  assert.strictEqual(pickSpawnCwd({ profile: { name: "claude" }, last, workspaces: WS, tier: "box" }), "/home/u/other");
  assert.strictEqual(pickSpawnCwd({ profile: { name: "claude", default_cwd: "/home/u/d" }, last, workspaces: WS, tier: "box" }), "/home/u/d");
});

test("pickSpawnCwd: nothing usable is an empty string", () => {
  assert.strictEqual(pickSpawnCwd({ profile: { name: "claude" }, last: null, workspaces: [], tier: "box" }), "");
});

test("name placeholder is the workspace's last component", () => {
  assert.strictEqual(spawnNamePlaceholder("/home/u/culipulse"), "auto: culipulse");
  assert.strictEqual(spawnNamePlaceholder("~/culipulse/"), "auto: culipulse");
  assert.strictEqual(spawnNamePlaceholder(""), "auto: working dir name");
});

test("the pure module touches no DOM and no network", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/spawnpreview.js"), "utf8");
  assert.ok(!/document\.|window\.|fetch\(|innerHTML/.test(src));
});

const fs = require("node:fs");
const path = require("node:path");
const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

test("the DOM half draws every server string through textContent only", () => {
  assert.ok(!/innerHTML|insertAdjacentHTML|outerHTML/.test(read("spawnpanel.js")));
});

test("index.html: the desktop nodes exist and the scripts load in dependency order", () => {
  const html = read("index.html");
  for (const id of ["spawn-plist", "spawn-reach", "spawn-foot-hint", "spawn-agent-row"]) assert.ok(html.includes('id="' + id + '"'), id);
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("tier.js") > 0 && at("tier.js") < at("spawnpreview.js"));
  assert.ok(at("spawnpreview.js") < at("spawnpanel.js") && at("spawnpanel.js") < at("app.js"));
});

test("base CSS hides the desktop-only nodes, so a phone never draws them", () => {
  const css = read("app.css");
  assert.match(css, /#spawn-plist,\s*#spawn-reach,\s*#spawn-foot-hint,\s*\.spawn-lbl-model\s*\{\s*display:\s*none;\s*\}/);
});

test("Ctrl/Cmd+Enter is wired on the whole spawn form, not only the first message", () => {
  const app = read("app.js");
  assert.match(app, /\$\("spawn"\)\.addEventListener\("keydown"[\s\S]{0,400}isSubmitHotkey/);
  assert.ok(!/\$\("spawn-prompt"\)\.addEventListener\("keydown"/.test(app));
});

test("hotkey from the workspace field spawns only when it shows the directory in use", () => {
  assert.strictEqual(spawnHotkeyAction({ text: "/home/u/proj", cwd: "/home/u/proj", listOpen: false }), "submit");
  assert.strictEqual(spawnHotkeyAction({ text: " /home/u/proj ", cwd: "/home/u/proj", listOpen: false }), "submit");
  assert.strictEqual(spawnHotkeyAction({ text: "", cwd: "", listOpen: false }), "submit");
});

test("hotkey from the workspace field picks/resolves instead of spawning in the wrong directory", () => {
  // a fuzzy query typed over a resolved directory: the picker's value still holds the old one
  assert.strictEqual(spawnHotkeyAction({ text: "culi", cwd: "/home/u/old", listOpen: false }), "pick");
  assert.strictEqual(spawnHotkeyAction({ text: "~/proj", cwd: "/home/u/proj", listOpen: false }), "pick");
  // an open list with a highlighted row takes that row, even when the text matches
  assert.strictEqual(spawnHotkeyAction({ text: "/home/u/proj", cwd: "/home/u/proj", listOpen: true, highlighted: true }), "pick");
  assert.strictEqual(spawnHotkeyAction({}), "submit");
});

test("the picker's keydown ignores Ctrl/Cmd+Enter so the form hotkey is the only handler, and it asks the guard", () => {
  const app = read("app.js");
  const src = read("wspicker.js");
  const body = src.slice(src.indexOf("function onKeydown"), src.indexOf("function enter"));
  assert.match(body, /if \(e\.ctrlKey \|\| e\.metaKey\) return;/);
  assert.ok(body.indexOf("e.ctrlKey || e.metaKey") < body.indexOf("e.preventDefault();\n    enter();"));
  assert.match(app, /spawnHotkeyAction\(\{ text: \$\("spawn-ws-input"\)\.value, cwd: getCwd\(\)/);
});

// #20 review: a list left open (tabbing past the field opens it) must not block
// a submit from another field when the field already shows the directory in use.
test("a settled field submits with its list open and nothing highlighted", () => {
  assert.strictEqual(spawnHotkeyAction({ text: "/home/u/proj", cwd: "/home/u/proj", listOpen: true, highlighted: false }), "submit");
  assert.strictEqual(spawnHotkeyAction({ text: "", cwd: "", listOpen: true }), "submit");
  assert.strictEqual(spawnHotkeyAction({ text: "culi", cwd: "/home/u/proj", listOpen: true }), "pick");
});

// #20: the Spawn button and every other submit (hotkey from any field, Retry
// after unlock) go through the form's submit handler, so the guard lives there.
test("every submit of the spawn form asks the guard before spawning", () => {
  const app = read("app.js");
  const at = app.indexOf('$("spawn").addEventListener("submit"');
  assert.ok(at > 0);
  const body = app.slice(at, app.indexOf("spawnBusy = true;", at));
  assert.match(body, /spawnHotkeyAction\(\{ text: \$\("spawn-ws-input"\)\.value, cwd: getCwd\(\), listOpen: spawnWsPicker\.isOpen\(\), highlighted: spawnWsPicker\.highlighted\(\) \}\) === "pick"/);
  assert.match(body, /spawnWsPicker\.settle\(\);\s*return;/);
  const src = read("wspicker.js");
  const settle = src.slice(src.indexOf("function settle"), src.indexOf("function destroy"));
  assert.match(settle, /enter\(\);/);
});

// A legacy inline AWS binding (profile.aws_inline: name, prod, elevate_max_minutes)
// is shown like a named connection everywhere the dialog summarises reach.
const legacy = { name: "legacy", runtime: "box", bin: "claude", claude: true, aws_inline: { name: "legacy-prod", prod: true, elevate_max_minutes: 30 } };

test("touch line: an inline AWS binding is named like a connection", () => {
  assert.strictEqual(profileTouchLine(legacy), "box · aws legacy-prod");
  assert.strictEqual(profileTouchLine({ runtime: "box", aws_connections: ["a"], aws_inline: { name: "b" } }), "box · aws a, b");
  assert.strictEqual(profileTouchLine({ runtime: "box", aws_inline: {} }), "box · no credentials", "an unnamed inline entry is left out, never guessed");
});

test("reach rows: an inline AWS binding gets an AWS row with its PROD flag and elevate window", () => {
  const rows = reachRows({ profile: legacy, tier: "box", cwd: "~/x", meta: null, creds: null });
  const aws = byK(rows, "AWS");
  assert.strictEqual(aws.length, 1);
  assert.strictEqual(aws[0].v, "legacy-prod · read · write only after you elevate (max 30 min)");
  assert.strictEqual(aws[0].name, "legacy-prod");
  assert.strictEqual(aws[0].prod, true);
  const plain = reachRows({ profile: { claude: true, aws_inline: { name: "old" } }, tier: "box", cwd: "~/x" });
  assert.strictEqual(byK(plain, "AWS")[0].v, "old · read only");
  assert.ok(!byK(plain, "AWS")[0].prod);
});

test("reach rows: a locked store warns for a profile whose only binding is the inline AWS one", () => {
  const locked = creds({ store: { initialized: true, locked: true, platform: "linux" } });
  const rows = reachRows({ profile: legacy, tier: "box", cwd: "~/x", meta: null, creds: locked });
  assert.strictEqual(byK(rows, "Store").length, 1);
});

test("reach rows: an sso_direct connection says so and never claims write after elevate", () => {
  const p = { runtime: "box", bin: "claude", claude: true, aws_connections: ["sbx"], network: "allowlist" };
  const rows = reachRows({ profile: p, tier: "box", cwd: "/w",
    meta: { aws_connections: [{ name: "sbx", prod: false, write_capable: false, mode: "sso_direct" }] },
    creds: { store: { initialized: true, locked: false, platform: "linux" }, aws: [{ name: "sbx", mode: "sso_direct" }] } });
  assert.deepStrictEqual(byK(rows, "AWS").map((r) => r.v), ["sbx · read only · SSO role directly"]);
  // the credentials list alone is enough when meta lacks the entry
  const r2 = reachRows({ profile: p, tier: "box", cwd: "/w", meta: {}, creds: { aws: [{ name: "sbx", mode: "sso_direct" }] } });
  assert.strictEqual(byK(r2, "AWS")[0].v, "sbx · read only · SSO role directly");
  // assume_role entries are unchanged
  const r3 = reachRows({ profile: p, tier: "box", cwd: "/w", meta: { aws_connections: [{ name: "sbx" }] }, creds: { aws: [{ name: "sbx" }] } });
  assert.strictEqual(byK(r3, "AWS")[0].v, "sbx · read only");
});

test("reach rows: an inline sso_direct binding ends with the mode label", () => {
  const p = { runtime: "box", bin: "claude", claude: true, network: "allowlist", aws_inline: { name: "sbx", mode: "sso_direct" } };
  const rows = reachRows({ profile: p, tier: "box", cwd: "/w", meta: null, creds: { store: { initialized: true, locked: false, platform: "linux" }, aws: [] } });
  assert.strictEqual(byK(rows, "AWS")[0].v, "sbx · read only · SSO role directly");
  const q = { ...p, aws_inline: { name: "old" } };
  const rows2 = reachRows({ profile: q, tier: "box", cwd: "/w", meta: null, creds: { store: { initialized: true, locked: false, platform: "linux" }, aws: [] } });
  assert.strictEqual(byK(rows2, "AWS")[0].v, "old · read only");
});

test("reach rows: a private copy names the read-only original right after Files", () => {
  const rows = reachRows({ profile: { clone_of: "/h/r" }, tier: "box", cwd: "~/box-clones/r" });
  assert.deepStrictEqual(rows[1], { k: "Original", v: "/h/r/.git read-only · the box cannot change your history" });
  assert.strictEqual(rows[0].k, "Files");
});

test("reach rows: no Original row on the host tier or without a copy", () => {
  assert.strictEqual(byK(reachRows({ profile: { clone_of: "/h/r" }, tier: "host" }), "Original").length, 0);
  assert.strictEqual(byK(reachRows({ profile: {}, tier: "box", cwd: "~/x" }), "Original").length, 0);
});

test("ghReachValue: read, read and write, and personal access token wording", () => {
  assert.strictEqual(ghReachValue(["culiops"], false, false), "culiops/* · read, 1-hour tokens");
  assert.strictEqual(ghReachValue(["culiops"], true, false), "culiops/* · read and write, 1-hour tokens");
  assert.strictEqual(ghReachValue(["culiops"], true, true), "culiops/* · personal access token: what the token allows");
  assert.strictEqual(ghReachValue(["a", "b"], false, false), "a/*, b/* · read, 1-hour tokens");
});

test("reach rows: a write App and a PAT binding say so on the GitHub row", () => {
  const wr = reachRows({ profile: { ...sre, github_write: true }, tier: "box", cwd: "~/x", meta, creds: creds() });
  assert.strictEqual(byK(wr, "GitHub")[0].v, "culiops/* · read and write, 1-hour tokens");
  const pat = reachRows({ profile: { ...sre, github_write: true, github_pat: true }, tier: "box", cwd: "~/x", meta, creds: creds() });
  assert.strictEqual(byK(pat, "GitHub")[0].v, "culiops/* · personal access token: what the token allows");
});

// The BRIEF's aws-ops profile (profile-mockups/BRIEF.md, "Truth-shaped data"): the
// one fixture the read view and New session must both draw from reachModel.
const awsOps = {
  name: "aws-ops", runtime: "box", bin: "claude", claude: true,
  aws_connections: ["prod-read", "staging-read"],
  github_owners: ["culiops"],
  github_repos: [{ owner: "culiops", repos: ["payments-api"] }],
  github_permissions: { contents: "read", pull_requests: "read", issues: "read", metadata: "read" },
  tokens: ["grafana-prod", "sentry-staging"],
  context_paths: ["/home/you/other-repo/docs"],
  egress_allow: ["api.example.com", "*.example.org"],
  network: "allowlist",
};
const awsOpsMeta = {
  aws_connections: [
    { name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" },
    { name: "staging-read", prod: false, write_capable: false, account_id: "111111111111", region: "eu-west-1" },
  ],
  static_tokens: [
    { name: "grafana-prod", prod: true, description: "Grafana service account", env: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"], hosts: [{ host: "grafana.acme.example.com", port: 443 }] },
    { name: "sentry-staging", description: "", env: ["SENTRY_ACCESS_TOKEN"], hosts: [{ host: "sentry.io", port: 443 }] },
  ],
};
const awsOpsCreds = () => creds({ aws: [{ name: "prod-read", prod: true, elevate: { max_minutes: 30 } }, { name: "staging-read" }] });
const awsOpsIn = (over) => Object.assign({ profile: awsOps, tier: "box", cwd: "~/work/payments-api", meta: awsOpsMeta, creds: awsOpsCreds(), bundleImports: true }, over);
const pick = (it, keys) => Object.fromEntries(keys.map((k) => [k, it[k]]));

test("reachModel lists aws-ops as the mockup does", () => {
  const m = reachModel(awsOpsIn());
  assert.deepStrictEqual(m.cloud.map((i) => i.kind), ["AWS", "AWS", "GitHub", "Token", "Token"]);
  assert.deepStrictEqual(pick(m.cloud[0], ["kind", "name", "tag", "sub", "scope", "missing"]), {
    kind: "AWS", name: "prod-read", tag: "PROD", sub: "account 222222222222, eu-west-1",
    scope: "Read only. Write only after you grant a window, max 30 min.", missing: false,
  });
  assert.deepStrictEqual(pick(m.cloud[1], ["name", "tag", "sub", "scope"]), { name: "staging-read", tag: "", sub: "account 111111111111, eu-west-1", scope: "Read only" });
  assert.deepStrictEqual(pick(m.cloud[2], ["name", "tag", "sub", "scope"]), {
    name: "culiops", tag: "", sub: "repo payments-api", scope: "Read only: contents, pull requests, issues, metadata. 1-hour tokens.",
  });
  assert.deepStrictEqual(pick(m.cloud[3], ["name", "tag", "sub", "scope"]), {
    name: "grafana-prod", tag: "production", sub: "Grafana service account, GRAFANA_SERVICE_ACCOUNT_TOKEN", scope: "Reaches grafana.acme.example.com",
  });
  assert.deepStrictEqual(pick(m.cloud[4], ["name", "tag", "sub", "scope"]), { name: "sentry-staging", tag: "", sub: "SENTRY_ACCESS_TOKEN", scope: "Reaches sentry.io" });
  assert.strictEqual(m.tokenNote, "Long-lived; the agent can read it while it runs");

  assert.deepStrictEqual(m.reachable.map((i) => i.kind), ["Folder", "Files", "Domain", "Domain"]);
  assert.deepStrictEqual(pick(m.reachable[0], ["name", "scope"]), { name: "/home/you/other-repo/docs", scope: "Read only" });
  assert.deepStrictEqual(pick(m.reachable[1], ["name", "sub", "scope"]), {
    name: "CLAUDE.md imports", sub: "The files your CLAUDE.md pulls in with @", scope: "Read-only copies",
  });
  assert.deepStrictEqual(pick(m.reachable[2], ["name", "scope"]), { name: "api.example.com", scope: "HTTPS" });
  assert.deepStrictEqual(pick(m.reachable[3], ["name", "scope"]), { name: "*.example.org", scope: "HTTPS, its subdomains" });

  assert.strictEqual(m.network.line, "Internet only through CuliBox's proxy, HTTPS to hostnames");
  assert.strictEqual(m.network.sub, "Because this profile holds cloud access and tokens, it is limited to Anthropic, AWS, GitHub and package registries, plus the domains and token hosts above.");
  assert.strictEqual(m.files, "~/work/payments-api read/write · nothing else on this machine");
  assert.strictEqual(m.original, "");
  assert.strictEqual(m.account, "default (~/.claude)");
  assert.strictEqual(m.store, null);
});

test("reachModel marks a bound name missing from meta, and never guesses its details", () => {
  const meta = { aws_connections: [awsOpsMeta.aws_connections[0]], static_tokens: [awsOpsMeta.static_tokens[0]] };
  const m = reachModel(awsOpsIn({ meta }));
  const staging = m.cloud.find((i) => i.name === "staging-read");
  assert.deepStrictEqual(pick(staging, ["kind", "missing", "tag", "sub", "scope"]), { kind: "AWS", missing: true, tag: "", sub: "", scope: "" });
  const sentry = m.cloud.find((i) => i.name === "sentry-staging");
  assert.strictEqual(sentry.missing, true);
  assert.strictEqual(sentry.scope, "");
  assert.strictEqual(m.cloud.find((i) => i.name === "prod-read").missing, false);
  assert.strictEqual(m.cloud.find((i) => i.name === "culiops").missing, false, "GitHub owners have no meta list to be absent from");
  assert.strictEqual(m.tokenNote, "Long-lived; the agent can read it while it runs", "a bound token is still a bound token");
  // No list in meta at all: nothing is known, so nothing is called missing.
  const none = reachModel(awsOpsIn({ meta: null }));
  assert.ok(none.cloud.every((i) => i.missing === false));
});

test("reachModel omits the imports item when bundling is off, imports are off, or the tier is host", () => {
  const kinds = (o) => reachModel(awsOpsIn(o)).reachable.map((i) => i.kind);
  assert.deepStrictEqual(kinds({ bundleImports: false }), ["Folder", "Domain", "Domain"]);
  assert.deepStrictEqual(kinds({ bundleImports: undefined }), ["Folder", "Domain", "Domain"]);
  assert.deepStrictEqual(kinds({ profile: { ...awsOps, imports_off: true } }), ["Folder", "Domain", "Domain"]);
  assert.deepStrictEqual(kinds({ tier: "host" }), [], "a host session has no folders, imports or domains to list");
});

test("reachModel: a private copy lists the paths shared from the original after the folders", () => {
  const m = reachModel(awsOpsIn({ profile: { ...awsOps, clone_of: "/h/k8s-gitops", clone_share: ["CLAUDE.md"] } }));
  // The b-clone shot's row: kind Files, the path, "From the original, read only". The CLAUDE.md imports item is
  // also a Files item, so the two are told apart by `original`, not by the kind string.
  assert.deepStrictEqual(m.reachable.map((i) => i.kind), ["Folder", "Files", "Files", "Domain", "Domain"]);
  assert.deepStrictEqual(pick(m.reachable[1], ["name", "scope", "original"]), { name: "CLAUDE.md", scope: "From the original, read only", original: true });
  assert.deepStrictEqual(pick(m.reachable[2], ["name", "original"]), { name: "CLAUDE.md imports", original: undefined });
  assert.strictEqual(m.original, "/h/k8s-gitops/.git read-only · the box cannot change your history");
});

test("reachModel: GitHub write permissions and a PAT are said in words", () => {
  const gh = (over) => reachModel(awsOpsIn({ profile: { ...awsOps, ...over } })).cloud.find((i) => i.kind === "GitHub");
  const write = gh({ github_write: true, github_permissions: { actions: "read", contents: "write", pull_requests: "write", issues: "read", metadata: "read" } });
  assert.strictEqual(write.scope, "Can write: contents, pull requests. Read only: actions, issues, metadata. 1-hour tokens.");
  const only = gh({ github_write: true, github_permissions: { contents: "write" } });
  assert.strictEqual(only.scope, "Can write: contents. 1-hour tokens.");
  const pat = gh({ github_write: true, github_pat: true });
  assert.strictEqual(pat.scope, "Personal access token, not narrowed by CuliBox: it reaches whatever the token allows.");
  const many = gh({ github_repos: [{ owner: "culiops", repos: ["a", "b"] }] });
  assert.strictEqual(many.sub, "repo a, b");
  const bare = gh({ github_repos: undefined, github_permissions: undefined });
  assert.deepStrictEqual(pick(bare, ["sub", "scope"]), { sub: "", scope: "1-hour tokens." }, "no repos or permissions sent: left out, not guessed");
});

test("reachModel: an sso_direct connection, a port, a missing window and the old-style AWS setting", () => {
  const p = { claude: true, aws_connections: ["sbx"], aws_inline: { name: "legacy-prod", prod: true, elevate_max_minutes: 30 }, tokens: ["t"] };
  const meta = {
    aws_connections: [{ name: "sbx", mode: "sso_direct", account_id: "333333333333", region: "us-east-1" }],
    static_tokens: [{ name: "t", description: "Custom", env: ["A", "B"], hosts: [{ host: "h.example.com", port: 8443 }, { host: "i.example.com", port: 443 }] }],
  };
  const m = reachModel({ profile: p, tier: "box", cwd: "~/x", meta, creds: null });
  assert.deepStrictEqual(m.cloud.map((i) => i.kind), ["AWS", "Old-style AWS", "Token"]);
  assert.strictEqual(m.cloud[0].scope, "Read only. Uses the SSO role directly.");
  assert.deepStrictEqual(pick(m.cloud[1], ["name", "tag", "scope"]), { name: "legacy-prod", tag: "PROD", scope: "Read only. Write only after you grant a window, max 30 min." });
  assert.strictEqual(m.cloud[2].sub, "Custom, A, B");
  assert.strictEqual(m.cloud[2].scope, "Reaches h.example.com:8443, i.example.com");
  const noWindow = reachModel({ profile: { aws_connections: ["prod-read"] }, tier: "box", meta: awsOpsMeta, creds: null });
  assert.strictEqual(noWindow.cloud[0].scope, "Read only. Write only after you grant a window.", "no credentials answer: no window length invented");
});

test("reachModel: the network sentence, an open posture shown plainly, and no network block on host or without a posture", () => {
  const net = (over, tier) => reachModel(awsOpsIn({ profile: { ...awsOps, ...over }, tier })).network;
  assert.strictEqual(net({ network: "open" }).line, "Internet only through CuliBox's proxy, HTTPS to hostnames");
  assert.strictEqual(net({ network: "open" }).sub, "All internet hosts (no allowlist).");
  assert.strictEqual(net({ network: "" }), null);
  assert.strictEqual(net({}, "host"), null);
});

// Three postures, told apart by what the profile holds (the approved b-aws, b-created
// shots and internal/egress: an unbound box is the proxy's open mode, any public host on 443).
test("reachModel: nothing bound says any public website, not an allowlist (b-created)", () => {
  const bare = { name: "payments-readonly", runtime: "box", bin: "claude", claude: true, network: "open" };
  const m = reachModel({ profile: bare, tier: "box", cwd: "~/work/payments-api", meta: awsOpsMeta, creds: null, bundleImports: true });
  assert.deepStrictEqual(m.cloud, []);
  assert.deepStrictEqual(m.network, {
    line: "Any public website over HTTPS, through CuliBox's proxy",
    sub: "An allowlist starts once it holds cloud access or a token. Private, tailnet and host addresses stay blocked.",
    open: true,
  });
  // The same unbound profile with a domain of its own listed: still nothing is bound.
  assert.strictEqual(reachModel({ profile: { ...bare, egress_allow: ["api.example.com"] }, tier: "box" }).network.line, "Any public website over HTTPS, through CuliBox's proxy");
  // The New session row is unchanged: an unbound box is open.
  assert.deepStrictEqual(byK(reachRows({ profile: bare, tier: "box", cwd: "~/x" }), "Network"), [{ k: "Network", v: "open" }]);
});

test("reachModel: egress_open on a bound profile says all internet hosts, plainly", () => {
  // Bound (AWS, GitHub or a token) and open: the profile lifted its allowlist.
  for (const bound of [{ aws_connections: ["prod-read"] }, { github_owners: ["culiops"] }, { tokens: ["sentry-staging"] }, { aws_inline: { name: "legacy" } }]) {
    const n = reachModel({ profile: { claude: true, network: "open", ...bound }, tier: "box", meta: awsOpsMeta }).network;
    assert.deepStrictEqual(n, { line: "Internet only through CuliBox's proxy, HTTPS to hostnames", sub: "All internet hosts (no allowlist).", open: true }, JSON.stringify(bound));
  }
  // A bound name that no longer exists in Credentials is still bound.
  assert.strictEqual(reachModel({ profile: { network: "open", tokens: ["gone"] }, tier: "box", meta: awsOpsMeta }).network.sub, "All internet hosts (no allowlist).");
});

test("reachModel: any credential bound gives the allowlist sentence, whatever it is bound to", () => {
  for (const bound of [{ aws_connections: ["prod-read"] }, { github_owners: ["culiops"] }, { tokens: ["sentry-staging"] }]) {
    const n = reachModel({ profile: { claude: true, network: "allowlist", ...bound }, tier: "box", meta: awsOpsMeta }).network;
    assert.strictEqual(n.line, "Internet only through CuliBox's proxy, HTTPS to hostnames");
    assert.match(n.sub, /^Because this profile holds cloud access and tokens, it is limited to Anthropic, AWS, GitHub and package registries/);
    assert.strictEqual(n.open, false);
  }
});

test("reachModel: the store lock and the account", () => {
  const locked = creds({ store: { initialized: true, locked: true, platform: "linux" } });
  const m = reachModel(awsOpsIn({ creds: locked, profile: { ...awsOps, account: "work" } }));
  assert.deepStrictEqual(m.store, { v: "credential store locked — unlock before spawn", tone: "warn" });
  assert.strictEqual(m.account, "work");
  assert.strictEqual(reachModel({ profile: { name: "codex" }, tier: "box" }).account, "");
  assert.strictEqual(reachModel({ profile: {}, tier: "host" }).files, "your whole user account");
  assert.deepStrictEqual(reachModel(), reachModel({}), "nothing at all is still a model, never a throw");
});

test("reachRows keeps today's rows and appends Reads, Imports, Domains", () => {
  const rows = reachRows(awsOpsIn());
  assert.deepStrictEqual(rows.map((r) => r.k), ["Files", "AWS", "AWS", "GitHub", "Tokens", "Reads", "Imports", "Domains", "Network", "Account"]);
  assert.strictEqual(byK(rows, "AWS")[0].v, "prod-read · read · write only after you elevate (max 30 min)");
  assert.strictEqual(byK(rows, "AWS")[0].prod, true);
  assert.strictEqual(byK(rows, "AWS")[1].v, "staging-read · read only");
  assert.strictEqual(byK(rows, "GitHub")[0].v, "culiops/* · read, 1-hour tokens");
  assert.strictEqual(byK(rows, "Tokens")[0].v, "grafana-prod, sentry-staging");
  assert.deepStrictEqual(byK(rows, "Reads")[0], { k: "Reads", v: "/home/you/other-repo/docs" });
  assert.deepStrictEqual(byK(rows, "Imports")[0], { k: "Imports", v: "CLAUDE.md imports, read-only copies" });
  assert.deepStrictEqual(byK(rows, "Domains")[0], { k: "Domains", v: "api.example.com, *.example.org" });
  assert.strictEqual(byK(rows, "Network")[0].v, "allowlist only");
  // Reads is folders and the original's shared paths, in one list.
  const copy = reachRows(awsOpsIn({ profile: { ...awsOps, clone_of: "/h/r", clone_share: ["CLAUDE.md", "docs"] } }));
  assert.strictEqual(byK(copy, "Reads")[0].v, "/home/you/other-repo/docs, CLAUDE.md, docs");
  // The original's files are not the CLAUDE.md imports item: no Imports row unless that item is there.
  const noImports = reachRows(awsOpsIn({ bundleImports: false, profile: { ...awsOps, clone_of: "/h/r", clone_share: ["CLAUDE.md"] } }));
  assert.strictEqual(byK(noImports, "Reads")[0].v, "/home/you/other-repo/docs, CLAUDE.md");
  assert.strictEqual(byK(noImports, "Imports").length, 0);
  assert.strictEqual(byK(copy, "Imports").length, 1);
  // Without bundleImports the Imports row never appears; on host none of the three do.
  assert.strictEqual(byK(reachRows(awsOpsIn({ bundleImports: false })), "Imports").length, 0);
  const host = reachRows(awsOpsIn({ tier: "host" }));
  for (const k of ["Reads", "Imports", "Domains"]) assert.strictEqual(byK(host, k).length, 0, k);
  assert.strictEqual(byK(host, "Network")[0].v, "not contained");
});

test("reachModel text never contains arn: or a token value", () => {
  const dirty = creds({
    aws: [{ name: "prod-read", prod: true, role_arn: "arn:aws:iam::222222222222:role/prod", elevate: { role_arn: "arn:aws:iam::222222222222:role/write", max_minutes: 30 } }],
  });
  const meta = {
    aws_connections: awsOpsMeta.aws_connections.map((c) => ({ ...c, role_arn: "arn:aws:iam::" + c.account_id + ":role/x", source_profile: "sso-secret-profile" })),
    static_tokens: awsOpsMeta.static_tokens.map((t) => ({ ...t, value: "ghp_SECRETVALUE", ref: "keychain:SECRETREF" })),
  };
  const o = awsOpsIn({ creds: dirty, meta });
  const text = JSON.stringify(reachModel(o)) + JSON.stringify(reachRows(o));
  assert.ok(!/arn:/i.test(text), "no ARN");
  assert.ok(!/SECRETVALUE|SECRETREF|sso-secret-profile/.test(text), "no token value, reference or source profile");
});

// The red PROD flag is a safety signal: credentials alone may know a connection is
// prod (meta lists nothing yet, e.g. awscEntries is [] before /api/meta answers).
test("reachRows keeps PROD from credentials when meta lacks the connection", () => {
  const p = { claude: true, aws_connections: ["culipulse-prod"] };
  const c = creds({ aws: [{ name: "culipulse-prod", prod: true, elevate: { max_minutes: 60 } }] });
  for (const meta of [{ aws_connections: [] }, {}, null]) {
    const aws = byK(reachRows({ profile: p, tier: "box", cwd: "~/x", meta, creds: c }), "AWS")[0];
    assert.strictEqual(aws.prod, true, JSON.stringify(meta));
    assert.strictEqual(aws.v, "culipulse-prod · read · write only after you elevate (max 60 min)");
  }
  // The model keeps its blank-for-missing rule for display, yet the fact is not lost.
  const m = reachModel({ profile: p, tier: "box", meta: { aws_connections: [] }, creds: c });
  assert.deepStrictEqual(pick(m.cloud[0], ["missing", "tag"]), { missing: true, tag: "" });
  assert.strictEqual(m.cloud[0].facts.prod, true);
  // Not prod anywhere: no flag.
  const plain = byK(reachRows({ profile: p, tier: "box", meta: { aws_connections: [] }, creds: creds({ aws: [{ name: "culipulse-prod" }] }) }), "AWS")[0];
  assert.ok(!plain.prod);
});
