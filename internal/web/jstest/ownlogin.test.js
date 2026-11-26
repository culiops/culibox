"use strict";
// #375: the New session form's per-kind "Use this machine's login" choice on a Host session (desktop only). The real
// tier.js, spawnpreview.js and spawnpanel.js run in one scope against the shared fake DOM; app.js is read as source.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const { spawnPayload } = require("../static/spawnform.js");

const BOUND = { name: "infra", runtime: "host", github_owners: ["acme"], aws_connections: ["sandbox"], github_permissions: { contents: "read" } };

function page(over) {
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  const el = doc.createElement("div");
  el.hidden = true;
  const calls = [];
  const box = {
    console, document: doc, window: { innerWidth: 1280 }, el, calls,
    profile: BOUND, desktop: true, boxRuntime: "runc", where: { runtime: "host" },
    drawerDesktop: () => box.desktop,
    currentProfile: () => box.profile,
    spawnWhere: () => box.where,
    $: (id) => (id === "spawn-ownlogin" ? el : null),
    renderTierPick: () => calls.push("renderTierPick"),
  };
  Object.assign(box, over || {});
  const ctx = vm.createContext(box);
  vm.runInContext(read("tier.js") + "\n" + read("spawnpreview.js") + "\n" + read("spawnpanel.js"), ctx);
  // spawnpanel.js declares syncSpawnPreview itself (the whole page's derive step): count the call instead of running it.
  box.syncSpawnPreview = () => calls.push("syncSpawnPreview");
  const run = (code) => {
    const r = vm.runInContext(code, ctx);
    return r && typeof r === "object" ? JSON.parse(JSON.stringify(r)) : r;
  };
  return { box, el, calls, run };
}
const radios = (el) => el.querySelectorAll("input");
const labels = (el) => el.querySelectorAll("label").map((l) => l.textContent);

test("spawnOwnLoginPicked: [] on the phone, on Box, and for a kind the profile does not offer (#375)", () => {
  const pg = page();
  pg.run('spawnOwnLoginSet("github", true); spawnOwnLoginSet("aws", true)');
  assert.deepEqual(pg.run("spawnOwnLoginPicked()"), ["github", "aws"]);
  pg.box.desktop = false;
  assert.deepEqual(pg.run("spawnOwnLoginPicked()"), []);
  pg.box.desktop = true;
  pg.box.where = { runtime: "box" };
  assert.deepEqual(pg.run("spawnOwnLoginPicked()"), []);
  pg.box.where = { runtime: "host" };
  pg.box.profile = { name: "x", aws_connections: ["s"] };
  assert.deepEqual(pg.run("spawnOwnLoginPicked()"), ["aws"], "github is no longer offered");
  pg.run('spawnOwnLoginSet("github", false)');
  pg.run('spawnOwnLoginSet("aws", false)');
  assert.deepEqual(pg.run("spawnOwnLoginPicked()"), []);
});

test("renderSpawnOwnLogin: desktop Host draws a row per kind with 'Use the binding' checked and 'Use this machine's login', the GitHub note when read-only (#375)", () => {
  const pg = page();
  pg.run("renderSpawnOwnLogin()");
  assert.equal(pg.el.hidden, false);
  const text = pg.el.textContent;
  assert.ok(text.includes("Credentials on Host"), text);
  assert.ok(text.includes("GitHub") && text.includes("AWS"), text);
  assert.ok(text.includes("This binding cannot push (contents: read)"), text);
  assert.deepEqual(labels(pg.el).filter((t) => /Use /.test(t)),
    ["Use the binding", "Use this machine's login", "Use the binding", "Use this machine's login"]);
  const gh = radios(pg.el).filter((r) => r.attrs.name === "ownlogin-github");
  assert.equal(gh.length, 2);
  assert.equal(gh[0].checked, true);
  assert.notEqual(gh[1].checked, true);
  // the note goes while GitHub is on this machine's login, and comes back with the binding
  pg.run('spawnOwnLoginSet("github", true)');
  assert.ok(!pg.el.textContent.includes("cannot push"), pg.el.textContent);
  pg.run('spawnOwnLoginSet("github", false)');
  assert.ok(pg.el.textContent.includes("cannot push (contents: read)"), pg.el.textContent);
  // the real contents value, not a fixed "read"
  pg.box.profile = Object.assign({}, BOUND, { github_permissions: {} });
  pg.run("renderSpawnOwnLogin()");
  assert.ok(pg.el.textContent.includes("cannot push (contents: none)"), pg.el.textContent);
  // a writable binding has no note
  pg.box.profile = Object.assign({}, BOUND, { github_write: true });
  pg.run("renderSpawnOwnLogin()");
  assert.ok(!pg.el.textContent.includes("cannot push"));
});

test("renderSpawnOwnLogin: hidden and empty on Box, on the phone and with nothing to choose (#375)", () => {
  const pg = page();
  pg.run("renderSpawnOwnLogin()");
  assert.ok(pg.el.children.length > 0);
  pg.box.where = { runtime: "box" };
  pg.run("renderSpawnOwnLogin()");
  assert.equal(pg.el.hidden, true);
  assert.equal(pg.el.children.length, 0);
  pg.box.where = { runtime: "host" };
  pg.run("renderSpawnOwnLogin()");
  assert.equal(pg.el.hidden, false);
  pg.box.desktop = false;
  pg.run("renderSpawnOwnLogin()");
  assert.equal(pg.el.hidden, true);
  assert.equal(pg.el.children.length, 0);
  pg.box.desktop = true;
  pg.box.profile = { name: "t", tokens: ["x"] };
  pg.run("renderSpawnOwnLogin()");
  assert.equal(pg.el.hidden, true);
});

test("picking 'Use this machine's login' redraws the cards and the reach box; a profile change resets the choice (#375)", () => {
  const pg = page();
  pg.run("renderSpawnOwnLogin()");
  const mine = radios(pg.el).find((r) => r.attrs.name === "ownlogin-aws" && r.attrs.value === "machine");
  assert.ok(mine, "the machine radio");
  mine.checked = true;
  mine.onchange({ target: mine });
  assert.deepEqual(pg.run("spawnOwnLoginPicked()"), ["aws"]);
  assert.ok(pg.calls.includes("renderTierPick") && pg.calls.includes("syncSpawnPreview"), JSON.stringify(pg.calls));
  pg.run("spawnOwnLoginReset()");
  assert.deepEqual(pg.run("spawnOwnLoginPicked()"), []);
});

test("picking a radio keeps keyboard focus on the same option (#375)", () => {
  const pg = page();
  pg.run("renderSpawnOwnLogin()");
  const find = () => radios(pg.el).find((r) => r.attrs.name === "ownlogin-github" && r.attrs.value === "machine");
  const mine = find();
  mine.focus();
  assert.equal(pg.box.document.activeElement, mine);
  mine.checked = true;
  mine.onchange({ target: mine });
  const now = pg.box.document.activeElement;
  assert.ok(now && now.attrs.name === "ownlogin-github" && now.attrs.value === "machine", "focus stays on the changed input");
  assert.ok(pg.el.contains(now), "and that input is in the form");
  assert.equal(now.checked, true);
});

test("the payload carries the pick on desktop Host only (#375)", () => {
  const pg = page();
  pg.run('spawnOwnLoginSet("github", true)');
  const req = (picked) => spawnPayload({ agent: "p", runtime: "host", hostOwnLogin: picked });
  assert.deepEqual(req(pg.run("spawnOwnLoginPicked()")).host_own_login, ["github"]);
  pg.box.desktop = false; // narrowed to phone width after picking
  assert.ok(!("host_own_login" in req(pg.run("spawnOwnLoginPicked()"))));
});

test("the submit sends spawnOwnLoginPicked() as hostOwnLogin and drops it when the copy dialog turns the spawn into a Box clone (#375)", () => {
  const app = read("app.js");
  const at = app.indexOf("const w = spawnWhere();\n  const ownLogin");
  assert.ok(at > 0);
  const tail = app.slice(at);
  assert.match(tail, /const ownLogin = spawnOwnLoginPicked\(\);/);
  assert.ok(tail.indexOf("const ownLogin = spawnOwnLoginPicked();") < tail.indexOf("spawnPayload({"));
  assert.match(tail, /hostOwnLogin: ownLogin/);
  assert.match(tail, /if \(copied\)[\s\S]*?delete req\.host_own_login/);
  assert.match(app, /tierReset\(\);[^\n]*\n\s*spawnOwnLoginReset\(\);/);
});

test("#spawn-ownlogin is hidden on the phone like #spawn-reach and sits before it (#375)", () => {
  const css = read("app.css");
  assert.match(css, /#spawn-plist, #spawn-reach, #spawn-ownlogin,/);
  const html = read("index.html");
  const a = html.indexOf('id="spawn-ownlogin"');
  assert.ok(a > 0 && a < html.indexOf('id="spawn-reach"'));
  assert.match(html, /<div class="span2" id="spawn-ownlogin" role="group" aria-label="Credentials on Host" hidden><\/div>/);
  assert.match(read("desktop.css"), /#spawn > #spawn-ownlogin \{ order: 10; \}/);
});

test("radios in the form have their own small-box rule, so #spawn input { width: 100% } does not stretch them (#375)", () => {
  const css = read("app.css");
  assert.match(css, /#spawn label\.spawn-check input\[type="radio"\] \{[^}]*width: 16px;[^}]*flex: none;/);
});
