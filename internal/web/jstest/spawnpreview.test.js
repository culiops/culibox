"use strict";
// spawnpreview.js: the New session dialog's pure decisions (spec 5.6).
const test = require("node:test");
const assert = require("node:assert");
const { profileTouchLine, profileKind, reachRows, reachModel, pickSpawnProfile, pickSpawnCwd, spawnCwdChoice, spawnCwdReselect, spawnGroupHolds, spawnBoxOwnedHit, spawnBoxOwnedLine, spawnWsNoteFor, spawnAcctHint, spawnNamePlaceholder, spawnHotkeyAction, ghReachValue } = require("../static/spawnpreview.js");
const { spawnWsIdleNote } = require("../static/dirpicker.js");

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
  assert.strictEqual(profileTouchLine(sre), "aws culipulse-prod · gh culiops");
});

test("touch line: box with several names, clone and nothing", () => {
  assert.strictEqual(profileTouchLine({ runtime: "box", aws_connections: ["a", "b"] }), "aws a, b");
  assert.strictEqual(profileTouchLine({ runtime: "box", clone_of: "learning-hubs" }), "private copy of learning-hubs");
  assert.strictEqual(profileTouchLine({ runtime: "box" }), "no credentials");
  assert.strictEqual(profileTouchLine({}), "no credentials");
});

// The server sends clone_of as an absolute path; the page writes home as ~ everywhere else (fsops.js shortPath reads
// window.HOME_DIR), and frame M4.1 draws "private copy of ~/culiops-agent-box".
test("touch line: a private copy's source is written with ~ for the home prefix, whole components only", () => {
  const had = Object.prototype.hasOwnProperty.call(globalThis, "window"), prev = globalThis.window;
  globalThis.window = { HOME_DIR: "/home/me" };
  try {
    const line = (clone_of) => profileTouchLine({ runtime: "box", clone_of });
    assert.strictEqual(line("/home/me/culiops-agent-box"), "private copy of ~/culiops-agent-box");
    assert.strictEqual(line("/home/me"), "private copy of ~");
    assert.strictEqual(line("/home/me2/x"), "private copy of /home/me2/x", "/home/me2 is not inside /home/me");
    assert.strictEqual(line("/srv/shared/api"), "private copy of /srv/shared/api");
    assert.strictEqual(line("~/culiops-agent-box"), "private copy of ~/culiops-agent-box", "already short");
    assert.strictEqual(line("learning-hubs"), "private copy of learning-hubs");
    assert.strictEqual(profileTouchLine({ runtime: "box", aws_connections: ["a"], clone_of: "/home/me/w" }), "aws a · private copy of ~/w");
  } finally {
    if (had) globalThis.window = prev; else delete globalThis.window;
  }
});

test("touch line: before the home directory is known the path is left as the server sent it", () => {
  assert.strictEqual(profileTouchLine({ runtime: "box", clone_of: "/home/me/w" }), "private copy of /home/me/w");
});

test("touch line: a host profile reads like any other: its credentials, or none", () => {
  assert.strictEqual(profileTouchLine({ runtime: "host", bin: "claude", claude: true }), "no credentials");
  assert.strictEqual(profileTouchLine({ runtime: "host", bin: "bash" }), "no credentials");
  assert.strictEqual(profileTouchLine({ runtime: "host", bin: "/bin/zsh" }), "no credentials");
});

test("touch line: box only is appended when the server blocks Host, after whatever else the line says", () => {
  const blocked = "this profile has credential bindings (AWS sandbox, GitHub), so it runs only in a box";
  assert.strictEqual(profileTouchLine({ runtime: "box", aws_connections: ["sandbox"], host_block: blocked }), "aws sandbox · box only");
  assert.strictEqual(profileTouchLine({ runtime: "box", host_block: blocked }), "no credentials · box only");
  assert.strictEqual(profileTouchLine({ runtime: "box", host_block: "" }), "no credentials");
  assert.strictEqual(profileTouchLine({ runtime: "box", host_block: undefined }), "no credentials");
  assert.strictEqual(profileTouchLine({ runtime: "box", runtime_asked: "box", host_block: blocked }, ""), "needs a box · unavailable", "no layer: the needs-a-box line is unchanged");
});

test("profileKind: a terminal for bash and zsh, the Claude mark for a claude profile, else nothing", () => {
  assert.strictEqual(profileKind({ bin: "/bin/bash" }), "shell");
  assert.strictEqual(profileKind({ bin: "zsh" }), "shell");
  assert.strictEqual(profileKind({ bin: "claude", claude: true }), "claude");
  assert.strictEqual(profileKind({ bin: "python" }), "");
  assert.strictEqual(profileKind({}), "");
});

test("touch line: no box layer does not change a profile that does not need one", () => {
  assert.strictEqual(profileTouchLine(sre, ""), "aws culipulse-prod · gh culiops");
  assert.strictEqual(profileTouchLine(sre, "sysbox-runc"), "aws culipulse-prod · gh culiops");
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
  assert.strictEqual(profileTouchLine(legacy), "aws legacy-prod");
  assert.strictEqual(profileTouchLine({ runtime: "box", aws_connections: ["a"], aws_inline: { name: "b" } }), "aws a, b");
  assert.strictEqual(profileTouchLine({ runtime: "box", aws_inline: {} }), "no credentials", "an unnamed inline entry is left out, never guessed");
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
  assert.deepStrictEqual(rows[1], { k: "Original", id: "original", v: "/h/r/.git read-only · the box cannot change your history", path: "/h/r/.git" });
  assert.strictEqual(rows[0].k, "Files");
});

test("reach rows: no Original row on the host tier or without a copy", () => {
  assert.strictEqual(byK(reachRows({ profile: { clone_of: "/h/r" }, tier: "host" }), "Original").length, 0);
  assert.strictEqual(byK(reachRows({ profile: {}, tier: "box", cwd: "~/x" }), "Original").length, 0);
});

const cloneProf = { clone_of: "/home/u/repo", default_cwd: "/home/u/clones/repo", clone_share: ["notes"], auto_return: true };

test("reachRows with clone:false drops Original, clone shares and Work home", () => {
  const rows = reachRows({ profile: cloneProf, tier: "box", clone: false, cwd: "~/repo" });
  assert.strictEqual(byK(rows, "Original").length, 0);
  assert.strictEqual(byK(rows, "Work home").length, 0);
  const reads = byK(rows, "Reads");
  assert.ok(reads.every((r) => !r.v.includes("notes")), "the clone shares are not read in place");
  assert.ok(!JSON.stringify(rows).includes("notes"));
});

test("reachRows with clone:true adds Work home after Original", () => {
  const had = Object.prototype.hasOwnProperty.call(globalThis, "window"), prev = globalThis.window;
  globalThis.window = { HOME_DIR: "/home/u" }; // shortPath writes home as ~
  try {
    workHomeRows();
  } finally {
    if (had) globalThis.window = prev; else delete globalThis.window;
  }
});

function workHomeRows() {
  const keys = (o) => reachRows(Object.assign({ tier: "box", clone: true, cwd: "~/clones/repo" }, o)).map((r) => r.k);
  const rows = reachRows({ profile: cloneProf, tier: "box", clone: true, cwd: "~/clones/repo" });
  const k = rows.map((r) => r.k);
  assert.strictEqual(k[k.indexOf("Original") + 1], "Work home");
  assert.strictEqual(byK(rows, "Work home")[0].v, "as box/<branch> branches in ~/repo");
  const off = reachRows({ profile: { ...cloneProf, auto_return: undefined }, tier: "box", clone: true, cwd: "~/clones/repo" });
  assert.strictEqual(byK(off, "Work home")[0].v, "work stays in the copy (auto-return off)");
  const pr = reachRows({ profile: { ...cloneProf, github_write: true }, tier: "box", clone: true, cwd: "~/clones/repo" });
  assert.ok(byK(pr, "Work home")[0].v.startsWith("work"), byK(pr, "Work home")[0].v);
  assert.deepStrictEqual(keys({ profile: {} }).filter((x) => x === "Work home"), [], "no copy, no Work home");
}

test("reachRows with clone undefined is today's", () => {
  const o = { profile: cloneProf, tier: "box", cwd: "~/clones/repo" };
  const rows = reachRows(o);
  assert.deepStrictEqual(rows, reachRows(Object.assign({}, o, { clone: undefined })));
  assert.strictEqual(rows[1].k, "Original");
  assert.strictEqual(byK(rows, "Work home").length, 0);
  assert.ok(byK(rows, "Reads")[0].v.includes("notes"));
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

// A GitHub owner signs in with an App or a token entry. ProfileView carries neither name, so the caller that holds the
// stored profile says which login each owner uses (o.githubLogins, owner -> "app:<name>" | "token:<name>"); the model
// marks the owner when /api/meta no longer lists that login (spec 5.6 rule 2).
test("reachModel marks a GitHub owner whose App or token is no longer in meta, and keeps its place and repositories", () => {
  const meta = { ...awsOpsMeta, github_apps: ["acme-app"] };
  const gh = (o) => reachModel(awsOpsIn(o)).cloud.find((i) => i.kind === "GitHub");
  const gone = gh({ meta, githubLogins: { culiops: "app:acme-bot" } });
  assert.deepStrictEqual(pick(gone, ["name", "missing", "scope", "tag", "sub"]), { name: "culiops", missing: true, scope: "", tag: "", sub: "repo payments-api" });
  assert.deepStrictEqual(reachModel(awsOpsIn({ meta, githubLogins: { culiops: "app:acme-bot" } })).cloud.map((i) => i.name), ["prod-read", "staging-read", "culiops", "grafana-prod", "sentry-staging"], "the row keeps its place");
  assert.strictEqual(gh({ meta, githubLogins: { culiops: "app:acme-app" } }).missing, false);
  // A token owner: gone when /api/meta's static_tokens lacks the entry. Any defined entry counts (the server only asks that it is defined).
  assert.strictEqual(gh({ meta, githubLogins: { culiops: "token:gh-pat" } }).missing, true);
  assert.strictEqual(gh({ meta, githubLogins: { culiops: "token:grafana-prod" } }).missing, false);
  // The facts reachRows needs stay on the item.
  assert.deepStrictEqual(gone.facts, { write: false, pat: false });
  // Nothing known, nothing called missing: no login named, no github_apps list, no static_tokens list.
  assert.strictEqual(gh({ meta }).missing, false, "New session names no login");
  assert.strictEqual(gh({ meta: awsOpsMeta, githubLogins: { culiops: "app:acme-bot" } }).missing, false, "meta without github_apps");
  assert.strictEqual(gh({ meta: { ...meta, static_tokens: undefined }, githubLogins: { culiops: "token:gh-pat" } }).missing, false, "meta without static_tokens");
  assert.strictEqual(gh({ meta: null, githubLogins: { culiops: "app:acme-bot" } }).missing, false);
  assert.strictEqual(gh({ meta, githubLogins: { other: "app:acme-bot" } }).missing, false, "a login for another owner says nothing about this one");
});

test("reach rows (New session) are the same with or without a login map: the row keeps its words", () => {
  const meta = { ...awsOpsMeta, github_apps: [] };
  const plain = reachRows(awsOpsIn({ meta }));
  assert.deepStrictEqual(reachRows(awsOpsIn({ meta, githubLogins: { culiops: "app:acme-bot" } })), plain);
  assert.strictEqual(byK(plain, "GitHub")[0].v, "culiops/* · read, 1-hour tokens");
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
  assert.deepStrictEqual(byK(reachRows({ profile: bare, tier: "box", cwd: "~/x" }), "Network"), [{ k: "Network", id: "network", v: "open" }]);
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
  assert.deepStrictEqual(byK(rows, "Reads")[0], { k: "Reads", id: "reads", v: "/home/you/other-repo/docs", path: "/home/you/other-repo/docs" });
  assert.deepStrictEqual(byK(rows, "Imports")[0], { k: "Imports", id: "imports", v: "CLAUDE.md imports, read-only copies" });
  assert.deepStrictEqual(byK(rows, "Domains")[0], { k: "Domains", id: "domains", v: "api.example.com, *.example.org" });
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

// --- Task 7 (#173): where the workspace came from, and the help line that says so (F38, M4.1, M4.2) ------

test("spawnCwdChoice: the profile's own default wins and says so", () => {
  const last = { agent: "claude", cwd: "/home/u/other" };
  assert.deepStrictEqual(spawnCwdChoice({ profile: { name: "claude", default_cwd: "/home/u/d" }, last, workspaces: WS, tier: "box" }), { cwd: "/home/u/d", source: "profile" });
});

test("spawnCwdChoice: the same profile's last directory is the 'last' source", () => {
  const last = { agent: "claude", cwd: "/home/u/p" };
  assert.deepStrictEqual(spawnCwdChoice({ profile: { name: "claude" }, last, workspaces: WS, tier: "box" }), { cwd: "/home/u/p", source: "last" });
});

test("spawnCwdChoice with skipScratch (the desktop): a last directory under /tmp is not prefilled; the default workspace is", () => {
  const last = { agent: "claude", cwd: "/tmp/claude-1001/x" };
  const got = spawnCwdChoice({ profile: { name: "claude" }, last, workspaces: WS, tier: "box", skipScratch: true });
  assert.deepStrictEqual(got, { cwd: "/home/u/proj", source: "default" });
  // /tmp itself, a nested path and a trailing slash are all under /tmp; /tmpfoo and /var/tmp are not.
  for (const c of ["/tmp", "/tmp/", "/tmp/a/b"]) assert.strictEqual(spawnCwdChoice({ profile: { name: "claude" }, last: { agent: "claude", cwd: c }, workspaces: WS, tier: "box", skipScratch: true }).source, "default", c);
  for (const c of ["/tmpfoo/x", "/var/tmp/x", "/home/u/tmp"]) assert.strictEqual(spawnCwdChoice({ profile: { name: "claude" }, last: { agent: "claude", cwd: c }, workspaces: WS, tier: "box", skipScratch: true }).source, "last", c);
});

test("spawnCwdChoice without skipScratch (the phone): a /tmp last directory is prefilled exactly as before", () => {
  const profile = { name: "claude" };
  for (const c of ["/tmp/claude-1001/x", "/tmp", "/tmp/", "/tmp/a/b"]) {
    const last = { agent: "claude", cwd: c };
    for (const flag of [undefined, false]) {
      const o = { profile, last, workspaces: WS, tier: "box" };
      if (flag !== undefined) o.skipScratch = flag;
      assert.deepStrictEqual(spawnCwdChoice(o), { cwd: c, source: "last" }, c + " skipScratch " + flag);
      assert.strictEqual(pickSpawnCwd(o), c, c + " pickSpawnCwd");
    }
  }
  // Only the opt-in flag changes the answer: truthy-but-not-true values do not.
  assert.strictEqual(spawnCwdChoice({ profile, last: { agent: "claude", cwd: "/tmp/x" }, workspaces: WS, tier: "box", skipScratch: 1 }).source, "last");
});

test("spawnCwdChoice: the profile default still beats a /tmp last directory, with or without skipScratch", () => {
  const last = { agent: "claude", cwd: "/tmp/claude-1001/x" };
  for (const skipScratch of [true, false]) assert.deepStrictEqual(spawnCwdChoice({ profile: { name: "claude", default_cwd: "/home/u/d" }, last, workspaces: WS, tier: "box", skipScratch }), { cwd: "/home/u/d", source: "profile" });
});

test("spawnCwdChoice: another profile's last directory, or none, falls to the default workspace", () => {
  assert.deepStrictEqual(spawnCwdChoice({ profile: { name: "claude" }, last: { agent: "bash", cwd: "/home/u/p" }, workspaces: WS, tier: "box" }), { cwd: "/home/u/proj", source: "default" });
  assert.deepStrictEqual(spawnCwdChoice({ profile: { name: "claude" }, last: null, workspaces: WS, tier: "box" }), { cwd: "/home/u/proj", source: "default" });
  assert.deepStrictEqual(spawnCwdChoice({ profile: { name: "claude" }, last: null, workspaces: [], tier: "box" }), { cwd: "", source: "default" });
  assert.deepStrictEqual(spawnCwdChoice({}), { cwd: "", source: "default" });
});

test("pickSpawnCwd is spawnCwdChoice's cwd, for every case above", () => {
  const cases = [
    { profile: { name: "claude", default_cwd: "/home/u/d" }, last: { agent: "claude", cwd: "/home/u/other" }, workspaces: WS, tier: "box" },
    { profile: { name: "claude" }, last: { agent: "claude", cwd: "/home/u/p" }, workspaces: WS, tier: "box" },
    { profile: { name: "claude" }, last: { agent: "claude", cwd: "/tmp/claude-1001/x" }, workspaces: WS, tier: "box" },
    { profile: { name: "claude" }, last: { agent: "claude", cwd: "/tmp/claude-1001/x" }, workspaces: WS, tier: "box", skipScratch: true },
    { profile: { name: "claude" }, last: { agent: "bash", cwd: "/home/u/p" }, workspaces: WS, tier: "box" },
    { profile: { name: "claude" }, last: null, workspaces: [], tier: "box" },
    {},
  ];
  for (const c of cases) assert.strictEqual(pickSpawnCwd(c), spawnCwdChoice(c).cwd, JSON.stringify(c));
});

test("spawnWsNoteFor: the frames' help lines (M4.1 'From the profile', M4.2 'Last used'), the plain caption otherwise", () => {
  assert.strictEqual(spawnWsNoteFor("last", 48), "Last used · 48 project roots · type to filter · ↵ to pick");
  assert.strictEqual(spawnWsNoteFor("last", 1), "Last used · 1 project root · type to filter · ↵ to pick");
  assert.strictEqual(spawnWsNoteFor("profile", 48), "From the profile · type to filter · ↵ to pick", "no count in the profile line");
  assert.strictEqual(spawnWsNoteFor("default", 1), spawnWsIdleNote(1));
  assert.strictEqual(spawnWsNoteFor("default", 48), spawnWsIdleNote(48));
  assert.strictEqual(spawnWsNoteFor("", 3), spawnWsIdleNote(3));
  assert.strictEqual(spawnWsNoteFor(undefined, 3), spawnWsIdleNote(3));
  assert.strictEqual(spawnWsNoteFor("group", 3, "/home/u/culiops-agent-box"), "From the culiops-agent-box group's + · type to change");
  assert.strictEqual(spawnWsNoteFor("last", 3, "/home/u/x"), "Last used · " + spawnWsIdleNote(3), "cwd is ignored for the other sources");
});

// --- the reach rows keep the full text for the tooltip and mark the paths (G7, M4.1) --------------------

test("reach rows: Files and Original carry the un-abbreviated text and the path that is drawn mono", () => {
  const rows = reachRows({
    profile: { name: "agent-box", runtime: "box", clone_of: "/h/r" }, tier: "box",
    cwd: "~/box-clones/r", cwdFull: "/home/me/box-clones/r",
  });
  const files = byK(rows, "Files")[0];
  assert.strictEqual(files.v, "~/box-clones/r read/write · nothing else on this machine");
  assert.strictEqual(files.full, "/home/me/box-clones/r read/write · nothing else on this machine");
  assert.strictEqual(files.path, "~/box-clones/r");
  const orig = byK(rows, "Original")[0];
  assert.strictEqual(orig.v, "/h/r/.git read-only · the box cannot change your history");
  assert.strictEqual(orig.path, "/h/r/.git");
  assert.ok(orig.v.startsWith(orig.path));
});

test("reach rows: a workspace not yet chosen, and the host's whole account, name no path", () => {
  const none = byK(reachRows({ profile: { runtime: "box" }, tier: "box", cwd: "", cwdFull: "" }), "Files")[0];
  assert.strictEqual(none.v, "the workspace read/write · nothing else on this machine");
  assert.ok(!none.path && !none.full);
  const host = byK(reachRows({ profile: { runtime: "host" }, tier: "host", cwd: "~/x", cwdFull: "/home/me/x" }), "Files")[0];
  assert.deepStrictEqual(host, { k: "Files", id: "files", v: "your whole user account" });
});

test("reach rows: without cwdFull the text is its own full text (nothing is invented)", () => {
  const f = byK(reachRows({ profile: { runtime: "box" }, tier: "box", cwd: "~/x" }), "Files")[0];
  assert.strictEqual(f.v, "~/x read/write · nothing else on this machine");
  assert.ok(f.full === undefined || f.full === f.v);
});

test("reach rows: Reads is all machine text, and only the default account's ~/.claude is a path", () => {
  const reads = byK(reachRows({ profile: { runtime: "box", context_paths: ["CLAUDE.md", "docs/superpowers"] }, tier: "box", cwd: "~/x" }), "Reads")[0];
  assert.strictEqual(reads.v, "CLAUDE.md, docs/superpowers");
  assert.strictEqual(reads.path, reads.v);
  const dflt = byK(reachRows({ profile: { claude: true, runtime: "box" }, tier: "box", cwd: "~/x" }), "Account")[0];
  assert.strictEqual(dflt.v, "default (~/.claude)");
  assert.strictEqual(dflt.path, "~/.claude");
  const named = byK(reachRows({ profile: { claude: true, runtime: "box", account: "work" }, tier: "box", cwd: "~/x" }), "Account")[0];
  assert.deepStrictEqual(named, { k: "Account", id: "account", v: "work" });
});

// paintReachRows: the DOM half, run against a tiny fake DOM with the real spawnpanel.js.
class RNode {
  constructor(tag) { this.tag = tag; this.children = []; this.className = ""; this.attrs = {}; this.title = ""; this._text = ""; }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); }
  set textContent(t) { this.children = []; this._text = String(t); }
  append(...cs) { for (const c of cs) this.children.push(typeof c === "string" ? Object.assign(new RNode("#text"), { _text: c }) : c); }
  replaceChildren(...cs) { this.children = []; this._text = ""; this.append(...cs); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "class") this.className = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  get classList() { const n = this; return { add: (c) => { n.className = (n.className + " " + c).trim(); } }; }
  has(cls) { return this.className.split(/\s+/).includes(cls); }
  find(cls, out = []) { if (this.has(cls)) out.push(this); for (const c of this.children) c.find(cls, out); return out; }
}
function reachPanel() {
  const vm = require("node:vm");
  const ctx = vm.createContext({ document: { createElement: (t) => new RNode(t), createElementNS: (ns, t) => new RNode(t) }, console });
  for (const f of ["icons.js", "iconmap.js", "svgicon.js"]) new vm.Script(read(f).replace(/^const /gm, "var "), { filename: f }).runInContext(ctx);
  new vm.Script(read("spawnpanel.js"), { filename: "spawnpanel.js" }).runInContext(ctx);
  return ctx;
}

test("paintReachRows: a title, one grid, a row per fact, and every value says its full text in a tooltip", () => {
  const ctx = reachPanel();
  const box = new RNode("div");
  const rows = [
    { k: "Files", v: "~/w read/write · nothing else on this machine", full: "/home/me/w read/write · nothing else on this machine", path: "~/w" },
    { k: "AWS", v: "culipulse-prod · read only", name: "culipulse-prod", prod: true },
    { k: "Reads", v: "CLAUDE.md, docs", path: "CLAUDE.md, docs" },
    { k: "Account", v: "default (~/.claude)", path: "~/.claude" },
    { k: "Network", v: "open" },
    { k: "Store", v: "credential store locked — unlock before spawn", tone: "warn" },
  ];
  ctx.paintReachRows(box, rows, { title: "This agent will be able to" });
  assert.strictEqual(box.children.length, 2, "the title, then one grid");
  assert.ok(box.children[0].has("sr-title") && box.children[0].textContent === "This agent will be able to");
  const grid = box.children[1];
  assert.ok(grid.has("sr-grid"));
  const vals = grid.find("sr-v");
  assert.strictEqual(vals.length, rows.length, "every row has a value element");
  assert.deepStrictEqual(grid.find("sr-k").map((n) => n.textContent), rows.map((r) => r.k));
  vals.forEach((v, i) => assert.strictEqual(v.title, rows[i].full || rows[i].v, rows[i].k + " title"));
  assert.strictEqual(vals[0].title, "/home/me/w read/write · nothing else on this machine", "the tooltip is the un-abbreviated path");
  assert.strictEqual(vals[0].textContent, rows[0].v, "the text on screen is the short form");
  assert.ok(vals.every((v) => v.title !== ""));
});

test("paintReachRows: a path is its own mono span inside the value, the text around it plain", () => {
  const ctx = reachPanel();
  const box = new RNode("div");
  ctx.paintReachRows(box, [
    { k: "Files", v: "~/w read/write · nothing else", path: "~/w" },
    { k: "Account", v: "default (~/.claude)", path: "~/.claude" },
    { k: "Reads", v: "CLAUDE.md", path: "CLAUDE.md" },
    { k: "Network", v: "open" },
    { k: "Files", v: "no such path here", path: "~/zzz" },
  ], {});
  const vals = box.find("sr-v");
  const paths = (v) => v.find("sr-path").map((n) => n.textContent);
  assert.deepStrictEqual(paths(vals[0]), ["~/w"]);
  assert.strictEqual(vals[0].textContent, "~/w read/write · nothing else");
  assert.deepStrictEqual(paths(vals[1]), ["~/.claude"]);
  assert.strictEqual(vals[1].textContent, "default (~/.claude)");
  assert.deepStrictEqual(paths(vals[2]), ["CLAUDE.md"]);
  assert.deepStrictEqual(paths(vals[3]), []);
  assert.deepStrictEqual(paths(vals[4]), [], "a path the text does not hold draws nothing extra");
  assert.strictEqual(vals[4].textContent, "no such path here");
});

test("paintReachRows: the PROD flag stays red text after the connection's name, and the row keeps its tone", () => {
  const ctx = reachPanel();
  const box = new RNode("div");
  ctx.paintReachRows(box, [
    { k: "AWS", v: "culipulse-prod · read only", name: "culipulse-prod", prod: true },
    { k: "Store", v: "locked", tone: "warn" },
  ], {});
  const [aws, store] = box.find("sr-row");
  assert.deepStrictEqual(aws.find("sr-prod").map((n) => n.textContent), ["PROD"]);
  assert.ok(store.has("tone-warn"));
  assert.strictEqual(box.find("sr-title").length, 0, "no title unless one is asked for");
});

// The reach box and the workspace list as CSS (frames M4.1, M4.3); read as text like spawn-frame.test.js.
const stripC = (t) => t.replace(/\/\*[\s\S]*?\*\//g, "");
const deskBlock = (() => { const c = stripC(read("desktop.css")); return c.slice(c.indexOf("@media (min-width: 701px) {")); })();
function splitSel(list) {
  const out = [];
  let depth = 0, from = 0;
  for (let i = 0; i < list.length; i++) {
    const c = list[i];
    if (c === "(" || c === "[") depth++;
    else if (c === ")" || c === "]") depth--;
    else if (c === "," && depth === 0) { out.push(list.slice(from, i)); from = i + 1; }
  }
  out.push(list.slice(from));
  return out.map((x) => x.trim().replace(/\s+/g, " "));
}
function ruleOf(sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(deskBlock))) if (splitSel(m[1]).includes(sel)) out.push(m[2].replace(/\s+/g, " ").trim());
  return out.join(" ");
}

test("desktop.css: the reach box is the frame's .m4-reach: --bg, 12px 16px, card radius, 8px gap, no border", () => {
  const r = ruleOf("#spawn-reach");
  assert.match(r, /display:\s*flex/);
  assert.match(r, /flex-direction:\s*column/);
  assert.match(r, /gap:\s*var\(--sp-2\)/);
  assert.match(r, /padding:\s*var\(--sp-3\) var\(--sp-4\)/);
  assert.match(r, /background:\s*var\(--bg\)/);
  assert.match(r, /border-radius:\s*var\(--r-card\)/);
  assert.doesNotMatch(r, /border:\s*1px/, "the frame's .s-bg box has no border");
  const t = ruleOf(".sr-title");
  assert.match(t, /font-size:\s*var\(--t-sm\)/);
  assert.match(t, /line-height:\s*var\(--lh-sm\)/);
  assert.match(t, /font-weight:\s*(600|var\(--w-semibold\))/);
});

test("desktop.css: the reach grid is 88px | 1fr (room for a 14px glyph and Domains), 16px between the columns, 2px between rows, baseline aligned", () => {
  const g = ruleOf(".sr-grid");
  assert.match(g, /display:\s*grid/);
  assert.match(g, /grid-template-columns:\s*88px minmax\(0, 1fr\)/);
  assert.match(g, /column-gap:\s*var\(--sp-4\)/);
  assert.match(g, /row-gap:\s*2px/);
  assert.match(g, /align-items:\s*baseline/);
  assert.match(ruleOf(".sr-row"), /display:\s*contents/, "a row's two cells are cells of the one grid");
  const k = ruleOf(".sr-k");
  assert.match(k, /color:\s*var\(--muted\)/);
  assert.match(k, /font-size:\s*var\(--t-sm\)/);
  const v = ruleOf(".sr-v");
  assert.match(v, /min-width:\s*0/);
  assert.match(v, /overflow-wrap:\s*anywhere/);
  assert.match(v, /font-size:\s*var\(--t-md\)/);
  const path = ruleOf(".sr-path");
  assert.match(path, /font-family:\s*var\(--font-mono\)/);
  assert.match(path, /font-size:\s*var\(--t-sm\)/);
  assert.match(ruleOf(".sr-prod"), /color:\s*var\(--red\)/, "PROD stays red");
});

test("the reach box is painted on the desktop only (the phone hides it and never draws it)", () => {
  assert.match(read("app.css"), /#spawn-plist,\s*#spawn-reach,\s*#spawn-foot-hint,\s*\.spawn-lbl-model\s*\{\s*display:\s*none;\s*\}/);
  const sync = read("spawnpanel.js");
  const at = sync.indexOf("function syncSpawnPreview");
  const fn = sync.slice(at, sync.indexOf("\n}\n", at));
  assert.ok(fn.indexOf("if (!spawnPanelOn())") < fn.indexOf("renderSpawnReach()"), "renderSpawnReach is behind the desktop gate");
  assert.match(sync.slice(sync.indexOf("function renderSpawnReach"), sync.indexOf("function paintReachRows")), /cwdFull:\s*spawnCwdFor\(w\)/);
});

test("desktop.css: the Workspace list is a kit menu under the field, capped at 8 rows (frame M4.3)", () => {
  assert.match(ruleOf("#spawn-ws-row"), /position:\s*relative/);
  const l = ruleOf("#spawn-ws-list");
  assert.match(l, /position:\s*absolute/);
  assert.match(l, /left:\s*0/);
  assert.match(l, /right:\s*0/);
  assert.match(l, /top:\s*58px/, "the frame's .m4-pop: 18px label + 6px + 32px input + 2px");
  assert.match(l, /z-index:\s*3/);
  assert.match(l, /max-height:\s*calc\(8 \* var\(--h-menu\) \+ 8px\)/);
  assert.match(l, /overflow-y:\s*auto/);
  assert.match(l, /padding:\s*var\(--sp-1\)/);
  assert.match(l, /background:\s*var\(--panel\)/);
  assert.match(l, /border:\s*1px solid var\(--line\)/);
  assert.match(l, /border-radius:\s*var\(--r-card\)/);
  assert.match(l, /box-shadow:\s*var\(--e-pop\)/);
  assert.match(l, /min-width:\s*0/, "the kit's 220px minimum would overflow a narrow field");
  assert.match(l, /display:\s*flex/);
  assert.match(l, /flex-direction:\s*column/);
  assert.match(ruleOf("#spawn-ws-list[hidden]"), /display:\s*none/);
});

test("desktop.css: a Workspace option is a 30px kit row, mono --t-sm, raised under the keyboard and the pointer", () => {
  const o = ruleOf("#spawn-ws-list .menu-item");
  assert.match(o, /height:\s*var\(--h-menu\)/);
  assert.match(o, /flex:\s*none/, "a row of a capped, scrolling list must not shrink");
  assert.match(o, /cursor:\s*default/);
  assert.match(ruleOf("#spawn-ws-list .menu-item[aria-selected=\"true\"]"), /background:\s*var\(--raise\)/);
  const t = ruleOf("#spawn-ws-list .menu-text");
  assert.match(t, /font-family:\s*var\(--font-mono\)/);
  assert.match(t, /font-size:\s*var\(--t-sm\)/);
  assert.match(t, /text-overflow:\s*ellipsis/);
  assert.match(ruleOf("#spawn-ws-list .wsp-hint"), /flex:\s*0 1 auto/, "the parent folder gives way before the name does");
  // The parent folder of a nested hit is machine text like the frame's `.mono.t-xs.muted` span (gate fix, DIFF M4.3).
  const hint = ruleOf("#spawn-ws-list .wsp-hint");
  assert.match(hint, /font-family:\s*var\(--font-mono\)/);
  assert.match(hint, /font-size:\s*var\(--t-xs\)/);
  assert.match(hint, /line-height:\s*var\(--lh-xs\)/);
  assert.match(hint, /color:\s*var\(--muted\)/);
  const last = ruleOf("#spawn-ws-list .wsp-last");
  assert.match(last, /flex:\s*none/);
  assert.match(last, /font-size:\s*var\(--t-sm\)/);
  assert.match(last, /color:\s*var\(--muted\)/);
});

test("desktop.css: the workspace help lines read 'Private copy of…' first, then the source line, as the frame draws them", () => {
  assert.match(ruleOf("#spawn #spawn-ws-row #spawn-copy-line"), /order:\s*1/);
  assert.match(ruleOf("#spawn #spawn-ws-row #spawn-ws-note"), /order:\s*2/);
  // The 2px under the first line belongs to the second one, whichever it is.
  assert.match(ruleOf("#spawn #spawn-ws-row #spawn-copy-line"), /margin-top:\s*0/);
  assert.match(ruleOf("#spawn #spawn-ws-row:has(> #spawn-copy-line:not([hidden])) #spawn-ws-note"), /margin-top:\s*-4px/);
  // No markup change: the phone keeps its DOM order and its pixels.
  const html = read("index.html");
  assert.ok(html.indexOf('id="spawn-ws-note"') < html.indexOf('id="spawn-copy-line"'));
});

// --- app.js wiring: the source of the prefill and the caption under the field -------------------------------

function appFn(name) {
  const src = read("app.js");
  const m = new RegExp("function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found in app.js");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

// The three functions that carry the source, run with a stand-in picker and the real caption words.
function captionPage(over) {
  const vm = require("node:vm");
  // typed: the text in the box when it is not the resolved folder (the user is mid-edit); null = the box shows the folder.
  const picker = { v: "", typed: null, sets: [], idleNotes: 0, value() { return this.v; }, set(x) { this.v = x; this.typed = null; this.sets.push(x); }, showIdleNote() { this.idleNotes++; } };
  const ctx = vm.createContext(Object.assign({
    console, spawnWsPicker: picker, desktop: true, drawerDesktop: () => ctx.desktop, currentProfile: () => ctx.profile, profile: null,
    onSpawnCwdChanged: () => { ctx.changed = (ctx.changed || 0) + 1; }, spawnWsNoteFor, spawnWsIdleNote,
    // applyProfileCwd's inputs: the box's text, the remembered last spawn, the roots, the tier, and the pure choice.
    $: (id) => (id === "spawn-ws-input" ? { value: picker.typed === null ? picker.v : picker.typed } : null),
    loadLastSpawn: () => ctx.last, last: null, workspacesCache: WS, segValue: () => ctx.tier, spawnWhere: () => ({ runtime: ctx.tier }), tier: "host", spawnCwdReselect, spawnGroupHolds,
  }, over));
  vm.runInContext("let spawnWsSource = '', spawnWsPrefill = '';\n" + ["setSpawnCwd", "spawnWsCaption", "getCwd", "applyProfileCwd"].map(appFn).join("\n"), ctx);
  return { ctx, picker, state: () => Array.from(vm.runInContext("[spawnWsSource, spawnWsPrefill]", ctx)) };
}

test("the caption names the source while the field still holds the prefilled value, on the desktop only", () => {
  const pg = captionPage();
  pg.ctx.setSpawnCwd("/home/u/p", "last");
  assert.deepStrictEqual(pg.state(), ["last", "/home/u/p"]);
  assert.deepStrictEqual(pg.picker.sets, ["/home/u/p"]);
  assert.strictEqual(pg.ctx.spawnWsCaption(48), "Last used · 48 project roots · type to filter · ↵ to pick");
  pg.picker.v = "/home/u/other"; // a pick, a typed path
  assert.strictEqual(pg.ctx.spawnWsCaption(48), "48 project roots · type to filter · ↵ to pick", "any other value drops the prefix");
  pg.picker.v = "/home/u/p";
  assert.strictEqual(pg.ctx.spawnWsCaption(48), "Last used · 48 project roots · type to filter · ↵ to pick", "picking the same folder again keeps it true");
  pg.ctx.desktop = false;
  assert.strictEqual(pg.ctx.spawnWsCaption(48), "48 project roots · type to filter · ↵ to pick", "the phone keeps its caption");
  pg.ctx.desktop = true;
  pg.ctx.setSpawnCwd("/home/u/d", "profile");
  pg.picker.v = "/home/u/d";
  assert.strictEqual(pg.ctx.spawnWsCaption(48), "From the profile · type to filter · ↵ to pick");
  pg.ctx.setSpawnCwd("/home/u/x");
  pg.picker.v = "/home/u/x";
  assert.deepStrictEqual(pg.state(), ["", "/home/u/x"], "no source: a Duplicate's folder claims nothing");
  assert.strictEqual(pg.ctx.spawnWsCaption(2), "2 project roots · type to filter · ↵ to pick");
});

test("picking a profile with a default folder says 'From the profile'; one without leaves the folder and takes back a claim that is no longer true", () => {
  const pg = captionPage();
  pg.ctx.desktop = true;
  pg.ctx.profile = { name: "agent-box", default_cwd: "/home/u/ab" };
  pg.ctx.applyProfileCwd();
  assert.deepStrictEqual(pg.state(), ["profile", "/home/u/ab"]);
  assert.strictEqual(pg.picker.idleNotes, 1, "the line is redrawn at once");
  assert.strictEqual(pg.ctx.changed, 1);
  // A profile with no default, while the field still holds the previous profile's folder: that folder is a leftover, so the
  // choice made at open is made again (new-session cwd): no last spawn of this profile, so the tier's default workspace.
  pg.ctx.profile = { name: "bash" };
  pg.ctx.applyProfileCwd();
  assert.deepStrictEqual(pg.state(), ["default", "/home/u/proj"]);
  assert.deepStrictEqual(pg.picker.sets, ["/home/u/ab", "/home/u/proj"], "the field is refilled, not kept");
  assert.strictEqual(pg.picker.idleNotes, 2);
  assert.strictEqual(pg.ctx.changed, 2, "the tier line and the warnings follow the new folder");
  // That profile's own last spawn wins over the default...
  pg.ctx.setSpawnCwd("/home/u/ab", "profile");
  pg.ctx.last = { agent: "bash", cwd: "/home/u/l" };
  pg.ctx.applyProfileCwd();
  assert.deepStrictEqual(pg.state(), ["last", "/home/u/l"]);
  // ...and the last spawn of ANOTHER profile is never prefilled.
  pg.ctx.setSpawnCwd("/home/u/ab", "profile");
  pg.ctx.last = { agent: "claude", cwd: "/home/u/l" };
  pg.ctx.applyProfileCwd();
  assert.deepStrictEqual(pg.state(), ["default", "/home/u/proj"]);
  // A 'last used' folder of the previous profile is a leftover too.
  pg.ctx.last = { agent: "claude", cwd: "/home/u/l" };
  pg.ctx.setSpawnCwd("/home/u/l", "last");
  pg.ctx.applyProfileCwd();
  assert.deepStrictEqual(pg.state(), ["default", "/home/u/proj"]);
  // The user's own text is never touched: a path typed or picked after the prefill (the resolved folder differs) ...
  pg.ctx.setSpawnCwd("/home/u/ab", "profile");
  pg.picker.v = "/home/u/mine"; // a pick or a resolved typed path
  const sets = pg.picker.sets.length, idle = pg.picker.idleNotes;
  pg.ctx.applyProfileCwd();
  assert.strictEqual(pg.picker.v, "/home/u/mine");
  assert.strictEqual(pg.picker.sets.length, sets, "nothing is written over it");
  assert.deepStrictEqual(pg.state(), ["", "/home/u/ab"], "it is no more 'from the profile'");
  assert.strictEqual(pg.picker.idleNotes, idle + 1);
  // ... and text still being typed over the prefilled folder (the field and the folder differ).
  pg.ctx.setSpawnCwd("/home/u/ab", "last");
  pg.picker.typed = "/home/u/mi";
  pg.ctx.applyProfileCwd();
  assert.strictEqual(pg.picker.v, "/home/u/ab");
  assert.strictEqual(pg.picker.typed, "/home/u/mi", "the half-typed text survives the switch");
  // A folder no source claims (a Duplicate's) stays as it is.
  pg.picker.typed = null;
  pg.ctx.setSpawnCwd("/home/u/dup");
  pg.ctx.applyProfileCwd();
  assert.deepStrictEqual(pg.state(), ["", "/home/u/dup"]);
  assert.strictEqual(pg.picker.v, "/home/u/dup");
  // The same re-choice on the box tier.
  pg.ctx.setSpawnCwd("/home/u/ab", "profile");
  pg.ctx.tier = "box";
  pg.ctx.applyProfileCwd();
  assert.deepStrictEqual(pg.state(), ["default", "/home/u/proj"]);
  // No profile at all: nothing happens.
  pg.ctx.setSpawnCwd("/home/u/l", "last");
  pg.ctx.profile = null;
  pg.ctx.applyProfileCwd();
  assert.deepStrictEqual(pg.state(), ["last", "/home/u/l"]);
});

test("the dialog's profile switch takes the profile's default; the box-owned line under the field is rebuilt with the folder", () => {
  // The line is repainted by updateRuntimeWarning, which onSpawnCwdChanged calls: the re-choice goes through it.
  assert.match(appFn("onSpawnCwdChanged"), /updateRuntimeWarning\(\)/);
  assert.match(appFn("updateRuntimeWarning"), /updateSpawnBoxOwned\(\)/);
  assert.match(appFn("updateSpawnBoxOwned"), /spawnBoxOwnedHit\(getCwd\(\), host \? "host" : "box", profiles\)/);
  assert.match(read("index.html"), /id="spawn-ws-owned"[^>]*hidden/);
});

test("the phone's profile switch does not redraw the caption (its logic is as it was)", () => {
  const pg = captionPage();
  pg.ctx.desktop = false;
  pg.ctx.profile = { name: "agent-box", default_cwd: "/home/u/ab" };
  pg.ctx.applyProfileCwd();
  assert.strictEqual(pg.picker.sets[0], "/home/u/ab");
  assert.strictEqual(pg.picker.idleNotes, 0);
});

test("New session builds its picker inline, with the last used folder and the source-aware caption, and opens on spawnCwdChoice", () => {
  const init = appFn("initSpawnWsPicker");
  assert.match(init, /inline:\s*drawerDesktop\b/);
  assert.match(init, /lastUsed:\s*\(\) =>[^}]*loadLastSpawn\(\)/);
  assert.match(init, /idleText:\s*spawnWsCaption\b/);
  const open = appFn("openSpawnDialog");
  assert.match(open, /const choice = spawnCwdChoice\(\{ profile: currentProfile\(\), last, workspaces: workspacesCache, tier: spawnWhere\(\)\.runtime \|\| "", skipScratch: drawerDesktop\(\) \}\);/);
  assert.match(open, /setSpawnCwd\(choice\.cwd, choice\.source\);/);
  assert.match(open, /spawnWsPicker\.showIdleNote\(\)/);
  assert.ok(open.indexOf("setSpawnCwd(choice.cwd") < open.indexOf("spawnWsPicker.showIdleNote()"));
  assert.match(appFn("applyProfileCwd"), /setSpawnCwd\(p\.default_cwd, "profile"\)/);
  // The profile page's picker passes none of these: its list stays fixed and its rows stay as the server sent them.
  assert.doesNotMatch(read("pfv-workspace.js"), /inline:|lastUsed:|idleText:/);
});

test("scripts: the picker's helpers load before the picker, the pure choice after both of its inputs", () => {
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("dirpicker.js") > 0 && at("dirpicker.js") < at("wspicker.js"));
  assert.ok(at("spawnpreview.js") > 0 && at("spawnpreview.js") < at("app.js"));
});

// --- new-session cwd: a host profile never keeps a box-owned copy's folder --------------------------------------

test("spawnCwdReselect: a folder the dialog filled in is chosen again for the new profile; the user's is kept", () => {
  const base = { profile: { name: "zsh" }, last: { agent: "zsh", cwd: "/home/u/z" }, workspaces: WS, tier: "host" };
  const filled = (source, cwd) => Object.assign({ source, prefill: cwd, cwd, text: cwd }, base);
  assert.deepStrictEqual(spawnCwdReselect(filled("profile", "/home/u/copy")), { cwd: "/home/u/z", source: "last" });
  assert.deepStrictEqual(spawnCwdReselect(filled("last", "/home/u/copy")), { cwd: "/home/u/z", source: "last" });
  // Another profile's last directory is never prefilled; the tier's default is.
  assert.deepStrictEqual(spawnCwdReselect(Object.assign(filled("profile", "/home/u/copy"), { last: { agent: "claude", cwd: "/home/u/c" } })), { cwd: "/home/u/proj", source: "default" });
  assert.deepStrictEqual(spawnCwdReselect(Object.assign(filled("profile", "/home/u/copy"), { last: null })), { cwd: "/home/u/proj", source: "default" });
  // The user's: a Duplicate's (no source), a default, anything that no longer equals the prefill, in the box or in the field.
  assert.strictEqual(spawnCwdReselect(filled("", "/home/u/copy")), null);
  assert.strictEqual(spawnCwdReselect(filled("default", "/home/u/proj")), null);
  assert.strictEqual(spawnCwdReselect(Object.assign(filled("profile", "/home/u/copy"), { cwd: "/home/u/picked" })), null);
  assert.strictEqual(spawnCwdReselect(Object.assign(filled("profile", "/home/u/copy"), { text: "/home/u/co" })), null);
  assert.strictEqual(spawnCwdReselect(null), null);
});

test("spawnBoxOwnedHit: a host folder at or inside a private copy's folder, whole components, deepest copy, host tier only", () => {
  const profiles = [
    { name: "claude" },
    { name: "box-a", clone_of: "/home/u/infra", default_cwd: "/home/u/box-clones/infra" },
    { name: "box-b", clone_of: "/home/u/infra/deep", default_cwd: "/home/u/box-clones/infra/deep" },
    { name: "plain", default_cwd: "/home/u/plain" }, // no clone_of: an ordinary default folder
    { name: "half", clone_of: "/home/u/half" }, // no folder yet: nothing to match
  ];
  const hit = (cwd, tier) => spawnBoxOwnedHit(cwd, tier === undefined ? "host" : tier, profiles);
  assert.deepStrictEqual(hit("/home/u/box-clones/infra"), { dir: "/home/u/box-clones/infra", of: "/home/u/infra" });
  assert.deepStrictEqual(hit("/home/u/box-clones/infra/"), { dir: "/home/u/box-clones/infra", of: "/home/u/infra" }, "a trailing slash");
  assert.deepStrictEqual(hit("/home/u/box-clones//infra/src/x"), { dir: "/home/u/box-clones/infra", of: "/home/u/infra" }, "inside, and a doubled slash");
  assert.deepStrictEqual(hit("/home/u/box-clones/infra/deep/y"), { dir: "/home/u/box-clones/infra/deep", of: "/home/u/infra/deep" }, "the deepest copy names its own original");
  assert.strictEqual(hit("/home/u/box-clones/infra2"), null, "a shared prefix is not inside");
  assert.strictEqual(hit("/home/u/box-clones"), null, "the parent of a copy is not a copy");
  assert.strictEqual(hit("/home/u/infra"), null, "the original is exactly where to go");
  assert.strictEqual(hit("/home/u/plain"), null);
  assert.strictEqual(hit("/home/u/box-clones/infra", "box"), null, "a boxed session in a copy is what a copy is for");
  assert.strictEqual(hit(""), null);
  assert.strictEqual(spawnBoxOwnedHit("/home/u/box-clones/infra", "host", null), null);
  assert.strictEqual(spawnBoxOwnedHit("/home/u/box-clones/infra", "host", [{ clone_of: "/o", default_cwd: "/" }]).dir, "/", "a copy at / holds everything");
});

test("spawnBoxOwnedLine: says why, and names the original folder to use", () => {
  const had = Object.prototype.hasOwnProperty.call(globalThis, "window"), prev = globalThis.window;
  globalThis.window = { HOME_DIR: "/home/u" };
  try {
    assert.strictEqual(spawnBoxOwnedLine({ dir: "/home/u/box-clones/infra", of: "/home/u/infra" }),
      "Host sessions can't run in a box's private copy (~/box-clones/infra). Use the original folder instead: ~/infra.");
  } finally {
    if (had) globalThis.window = prev; else delete globalThis.window;
  }
});

test("spawnAcctHint: only an exact no-login, the locked store, nothing else", () => {
  const accounts = [{ name: "default", default: true, login_state: "logged_in" }, { name: "claude-box", login_state: "token" }, { name: "work", login_state: "token_locked" }];
  const box = { runtime: "box", clone: false }, host = { runtime: "host", clone: false };
  const locked = { kind: "locked", text: "This account needs the store unlocked." };
  const h = (profile, chosen, where, acc) => spawnAcctHint({ profile, chosen, accounts: acc || accounts, where });
  assert.deepStrictEqual(h({ box_claude_credential: "no" }, "default", box), { kind: "nologin", uses: ["claude-box", "work"] });
  assert.strictEqual(h({ box_claude_credential: "no" }, "default", host), null, "the Host card has no box login to lack");
  assert.strictEqual(h({ box_claude_credential: "no" }, "default", {}), null, "no box layer: no tier");
  assert.strictEqual(h({ account: "claude-box", box_claude_credential: "yes" }, "default", box), null, "default is not the profile's own account");
  assert.strictEqual(h({ account: "claude-box", box_claude_credential: "no" }, "default", box), null, "inexact: the field describes claude-box");
  assert.strictEqual(h({ box_claude_credential: "" }, "default", box), null, "a shell or non-claude profile");
  assert.strictEqual(h({}, "default", box), null);
  assert.deepStrictEqual(h({ box_claude_credential: "locked" }, "default", box), locked);
  assert.strictEqual(h({ box_claude_credential: "locked" }, "default", host), null);
  assert.deepStrictEqual(h({ box_claude_credential: "yes" }, "work", box), locked);
  assert.deepStrictEqual(h({ box_claude_credential: "" }, "work", host), locked, "a locked account is locked on any card");
  assert.strictEqual(h({ account: "work", box_claude_credential: "yes" }, "claude-box", box), null);
  assert.deepStrictEqual(h({ box_claude_credential: "no" }, "default", box, [{ name: "default", default: true }]), { kind: "nologin", uses: [] });
});

// #168 G1 22-28: a reach row and a profile item declare their kind, and the glyph is drawn from that id, never from the label.
const RICH = {
  runtime: "box", aws_connections: ["c1", "gone"], aws_inline: { name: "old" }, github_owners: ["culiops"], tokens: ["t1", "t-gone"],
  context_paths: ["/p"], clone_share: ["CLAUDE.md"], clone_of: "/h/orig", egress_allow: ["api.example.com"], network: "allowlist", claude: true,
};
const RICH_META = { aws_connections: [{ name: "c1", account_id: "1" }], static_tokens: [{ name: "t1" }] };

test("items declare kindId: aws_legacy, aws, github, token, folder, files, domain; the kind text is unchanged", () => {
  const m = reachModel({ profile: RICH, tier: "box", cwd: "~/x", meta: RICH_META, creds: null, bundleImports: true });
  const ids = (l) => l.map((i) => [i.kind, i.name, i.kindId]);
  assert.deepStrictEqual(ids(m.cloud), [
    ["AWS", "c1", "aws"], ["AWS", "gone", "aws"], ["Old-style AWS", "old", "aws_legacy"],
    ["GitHub", "culiops", "github"], ["Token", "t1", "token"], ["Token", "t-gone", "token"],
  ]);
  assert.deepStrictEqual(ids(m.reachable), [
    ["Folder", "/p", "folder"], ["Files", "CLAUDE.md", "files"], ["Files", "CLAUDE.md imports", "files"], ["Domain", "api.example.com", "domain"],
  ]);
  assert.ok(m.cloud.find((i) => i.name === "gone").missing, "the missing AWS item declares its kind too");
  const gone = reachModel({ profile: { runtime: "box", github_owners: ["o"], tokens: ["t"] }, tier: "box", meta: { static_tokens: [], github_apps: [] }, githubLogins: { o: "app:x" } });
  assert.deepStrictEqual(gone.cloud.map((i) => [i.kind, i.missing, i.kindId]), [["GitHub", true, "github"], ["Token", true, "token"]]);
});

test("reach rows declare id: files, original, workhome, aws, github, tokens, reads, imports, domains, network, account, store", () => {
  const rows = reachRows({
    profile: Object.assign({}, RICH, { account: "work" }), tier: "box", clone: true, cwd: "~/x", meta: RICH_META,
    creds: { store: { initialized: true, locked: true, platform: "linux" }, aws: [] }, bundleImports: true,
  });
  assert.deepStrictEqual(rows.map((r) => [r.k, r.id]), [
    ["Files", "files"], ["Original", "original"], ["Work home", "workhome"], ["AWS", "aws"], ["AWS", "aws"], ["AWS", "aws"],
    ["GitHub", "github"], ["Tokens", "tokens"], ["Reads", "reads"], ["Imports", "imports"], ["Domains", "domains"],
    ["Network", "network"], ["Account", "account"], ["Store", "store"],
  ]);
  const host = reachRows({ profile: { runtime: "host" }, tier: "host", cwd: "~/x" });
  assert.ok(host.length >= 2 && host.every((r) => typeof r.id === "string" && r.id !== ""), "the host rows declare an id too");
});

const reachIcons = (rows) => {
  const box = new RNode("div");
  reachPanel().paintReachRows(box, rows, { title: "t" });
  return box.children[1].children.map((row) => {
    const k = row.children[0];
    return { text: k.textContent, icons: k.children.filter((c) => c.tag === "svg"), cls: k.className, k };
  });
};

test("paintReachRows: AWS, GitHub, Tokens, Reads, Imports, Domains and Store draw their glyph; Files, Original, Work home, Network, Account stay plain", () => {
  const rows = reachRows({
    profile: Object.assign({}, RICH, { account: "work" }), tier: "box", clone: true, cwd: "~/x", meta: RICH_META,
    creds: { store: { initialized: true, locked: true, platform: "linux" }, aws: [] }, bundleImports: true,
  });
  const drawn = reachIcons(rows);
  assert.deepStrictEqual(drawn.map((d) => [d.text, d.icons.map((s) => s.attrs["data-icon"])]), [
    ["Files", []], ["Original", []], ["Work home", []], ["AWS", ["cloud"]], ["AWS", ["cloud"]], ["AWS", ["cloud"]],
    ["GitHub", ["si:github"]], ["Tokens", ["key-round"]], ["Reads", ["folder"]], ["Imports", ["files"]], ["Domains", ["globe"]],
    ["Network", []], ["Account", []], ["Store", ["lock"]],
  ]);
  for (const d of drawn) {
    assert.strictEqual(d.cls.split(/\s+/).includes("has-ic"), d.icons.length === 1, d.text + " class");
    if (d.icons.length) {
      assert.strictEqual(d.icons[0].attrs.width, "14");
      assert.strictEqual(d.k.children[0], d.icons[0], "the glyph comes before the label");
    }
  }
});

test("renaming a label does not move a glyph: the row's id decides", () => {
  const [a, b] = reachIcons([{ k: "Something else", id: "tokens", v: "x" }, { k: "Tokens", id: "network", v: "x" }]);
  assert.strictEqual(a.text, "Something else");
  assert.deepStrictEqual(a.icons.map((s) => s.attrs["data-icon"]), ["key-round"]);
  assert.deepStrictEqual(b.icons, [], "the word Tokens with the id network draws nothing");
});

test("row without an id draws no glyph (the old shape), and a hostile id draws none", () => {
  for (const id of [undefined, "", "nope", "constructor", "__proto__", "toString", "hasOwnProperty"]) {
    const [d] = reachIcons([{ k: "AWS", id, v: "x" }]);
    assert.deepStrictEqual(d.icons, [], String(id));
    assert.strictEqual(d.text, "AWS");
    assert.ok(!d.cls.split(/\s+/).includes("has-ic"), String(id));
  }
});

test("desktop.css: the reach label without a glyph is indented 20px, the glyph has 6px after it, and nothing of it reaches the phone", () => {
  assert.match(ruleOf(".sr-k:not(.has-ic)"), /padding-left:\s*20px/);
  assert.match(ruleOf(".sr-k .ui-icon"), /margin-right:\s*6px/);
  assert.match(ruleOf(".sr-k.has-ic"), /white-space:\s*nowrap/);
  const all = stripC(read("desktop.css")), app = stripC(read("app.css"));
  assert.ok(all.indexOf(".sr-k:not(.has-ic)") > all.indexOf("@media (min-width: 701px) {"), "inside the desktop block");
  assert.doesNotMatch(app, /\.sr-(k|grid)/, "the phone's stylesheet has no reach rule");
});
