"use strict";
// spawnpreview.js: the New session dialog's pure decisions (spec 5.6).
const test = require("node:test");
const assert = require("node:assert");
const { profileTouchLine, reachRows, pickSpawnProfile, pickSpawnCwd, spawnNamePlaceholder } = require("../static/spawnpreview.js");

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
  assert.strictEqual(profileTouchLine({ runtime: "box", clone_of: "learning-hubs" }), "box · clone of learning-hubs");
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
