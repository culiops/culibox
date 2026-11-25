"use strict";
// One touches line (UI plan 3, #173 Task 7; findings F30 and F16): the empty state's recent-profile rows and
// the New session profile list say what a profile reaches with the SAME function, profileTouchLine, so a
// profile reads the same in both places. The empty state also says "Start session", the verb of the dialog it opens.
// app.js needs a browser and cannot be require()d, so the functions under test are cut out of its source and run
// against a tiny fake DOM, beside the real spawnpreview.js / fsops.js / spawnpanel.js.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const APP = read("app.js");

function fnSource(src, name) {
  const m = new RegExp("(?:async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

class N {
  constructor(tag) { this.tag = tag; this.children = []; this.className = ""; this.attrs = {}; this.dataset = {}; this._text = ""; this.hidden = false; }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); }
  set textContent(t) { this.children = []; this._text = String(t); }
  append(...cs) { for (const c of cs) this.children.push(typeof c === "string" ? Object.assign(new N("#text"), { _text: c }) : c); }
  replaceChildren(...cs) { this.children = []; this._text = ""; this.append(...cs); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  get classList() {
    const n = this;
    const set = () => new Set(n.className.split(/\s+/).filter(Boolean));
    return {
      add: (c) => { n.className = [...set().add(c)].join(" "); },
      toggle: (c, on) => { const s = set(); if (on) s.add(c); else s.delete(c); n.className = [...s].join(" "); },
    };
  }
  querySelector() { return null; }
  find(cls, out = []) { if (this.className.split(/\s+/).includes(cls)) out.push(this); for (const c of this.children) c.find(cls, out); return out; }
}

const FIXTURES = [
  { name: "bash", runtime: "box", bin: "bash" },
  { name: "claude", runtime: "host", bin: "claude", claude: true },
  { name: "agent-box", runtime: "box", bin: "claude", claude: true, clone_of: "/home/u/culiops-agent-box", default_cwd: "/home/u/culiops-agent-box" },
  { name: "elsewhere", runtime: "box", bin: "claude", claude: true, clone_of: "/srv/shared/api" },
  { name: "aws-ops", runtime: "box", bin: "claude", claude: true, aws_connections: ["culipulse-prod", "sandbox"], github_owners: ["culiops"] },
  { name: "tokens", runtime: "box", bin: "claude", claude: true, tokens: ["npm", "sentry"] },
  { name: "hostbash", runtime: "host", bin: "/bin/zsh" },
];

// One page: the real helpers, the profiles, and the two functions that draw a profile's touches.
function page(boxRuntime, metaLoaded = true) {
  const doc = { createElement: (t) => new N(t), createElementNS: (ns, t) => new N(t) };
  const els = { "spawn-plist": new N("div"), agent: Object.assign(new N("select"), { value: "bash" }) };
  const ctx = vm.createContext({
    // The server sends a clone's source as an absolute path; window.HOME_DIR is what every other path on the page is
    // shortened with (fsops.js shortPath), so the touches line reads "~/…" in both places (frame M4.1).
    document: doc, console, profiles: FIXTURES, boxRuntime, boxUnavailableReason: "", boxDaemonReason: "", window: { HOME_DIR: "/home/u" }, PLIST_SEARCH_MIN: 99,
    $: (id) => els[id], notHomeLabel: () => "", notHomeLine: () => "", notHomeTitle: () => "", openSpawnDialog: () => {},
    metaLoaded,
  });
  for (const f of ["fsops.js", "profileview.js", "icons.js", "iconmap.js", "svgicon.js", "claudemark.js", "spawnpreview.js", "spawnpanel.js"]) new vm.Script(read(f), { filename: f }).runInContext(ctx);
  // The panel's own helpers that need more DOM than this fake has; the rows themselves are the real ones.
  vm.runInContext("spawnPanelOn = () => true; spawnPlistFilter = () => {};", ctx);
  const names = ["emptyEl", "emptyHero", "emptyNoneEl", "emptyStateKey"];
  if (/function profileTouches\b/.test(APP)) names.push("profileTouches"); // the old line, while it still exists
  vm.runInContext("function emptyHint() { return document.createElement('div'); }\n" + names.map((n) => fnSource(APP, n)).join("\n"), ctx);
  return { ctx, els };
}

const dialogTouches = (pg) => {
  pg.ctx.renderSpawnPlist();
  const out = {};
  for (const row of pg.els["spawn-plist"].find("sp-item")) out[row.dataset.name] = row.find("sp-touch")[0].textContent;
  return out;
};
const emptyRows = (pg, names) => {
  const root = pg.ctx.emptyNoneEl({ kind: "none", profiles: names });
  return root.find("empst-row").map((r) => ({
    name: r.find("empst-name")[0].textContent,
    touch: r.find("empst-touch")[0].textContent,
    button: r.children.find((c) => c.tag === "button").textContent,
  }));
};

for (const [label, runtime] of [["with a box layer", "sysbox-runc"], ["without one (every session is host)", ""]]) {
  test("the empty state's touch text is profileTouchLine and the New session list's .sp-touch, for every fixture profile, " + label, () => {
    const pg = page(runtime);
    const names = FIXTURES.map((p) => p.name);
    const rows = emptyRows(pg, names);
    assert.deepStrictEqual(rows.map((r) => r.name), names);
    const dialog = dialogTouches(pg);
    for (const r of rows) {
      const p = FIXTURES.find((x) => x.name === r.name);
      const line = pg.ctx.profileTouchLine(p, runtime);
      assert.strictEqual(r.touch, line, r.name + ": empty state");
      assert.strictEqual(dialog[r.name], line, r.name + ": New session list");
      assert.strictEqual(r.touch, dialog[r.name], r.name + ": the same words in both");
    }
  });
}

// Review M1: boxRuntime is "" until /api/meta answers, and "" reads as "no box layer here", so before meta every box
// profile was called "host · not contained" on the landing screen: a false containment claim. Before meta the
// profile's own runtime speaks (spawnTierOf with no boxRuntime); once meta answers, the daemon's word wins.
test("before /api/meta answers a box profile is not called 'host · not contained', in the empty state and the dialog alike", () => {
  const pg = page("", false);
  const names = FIXTURES.map((p) => p.name);
  const rows = emptyRows(pg, names);
  const dialog = dialogTouches(pg);
  for (const r of rows) {
    const p = FIXTURES.find((x) => x.name === r.name);
    const line = pg.ctx.profileTouchLine(p); // no runtime given: the profile's own
    assert.strictEqual(r.touch, line, r.name + ": empty state");
    assert.strictEqual(dialog[r.name], line, r.name + ": New session list");
    assert.strictEqual(r.touch, dialog[r.name], r.name + ": the same words in both");
  }
  const by = Object.fromEntries(rows.map((r) => [r.name, r.touch]));
  assert.strictEqual(by["aws-ops"], "aws culipulse-prod, sandbox · gh culiops");
  assert.strictEqual(by["bash"], "no credentials");
  assert.strictEqual(by["agent-box"], "private copy of ~/culiops-agent-box");
  // A profile that is host by its own setting is still host before meta.
  assert.strictEqual(by["claude"], "no credentials");
  assert.strictEqual(by["hostbash"], "no credentials");
  for (const n of ["aws-ops", "bash", "agent-box", "elsewhere", "tokens"]) assert.notStrictEqual(by[n], "needs a box · unavailable", n);
});

test("after /api/meta answers, the same page reads as before the fix: a host-only machine reads every profile as host, a boxed one keeps box", () => {
  const hostOnly = emptyRows(page("", true), ["aws-ops", "bash", "claude"]);
  assert.deepStrictEqual(hostOnly.map((r) => r.touch), ["aws culipulse-prod, sandbox · gh culiops", "no credentials", "no credentials"]);
  const boxed = emptyRows(page("sysbox-runc", true), ["aws-ops", "bash", "claude"]);
  assert.deepStrictEqual(boxed.map((r) => r.touch), ["aws culipulse-prod, sandbox · gh culiops", "no credentials", "no credentials"]);
});

test("the empty state repaints when meta answers on a host-only machine (the key reads metaLoaded and the touch line)", () => {
  const m = { kind: "none", profiles: ["aws-ops"] };
  const pre = page("", false), post = page("", true);
  assert.notStrictEqual(pre.ctx.emptyStateKey(m), post.ctx.emptyStateKey(m));
  assert.match(pre.ctx.emptyStateKey(m), /"none",false,/);
  assert.match(post.ctx.emptyStateKey(m), /"none",true,/);
});

// The key must carry the touch line itself: for a profile that asks for a box, the line changes when meta answers "no box
// layer" (credentials before, "needs a box · unavailable" after), so a key without the touch line would fail here.
test("the empty state key carries the touch line, which changes when meta answers for a profile that needs a box", () => {
  const needsBox = { name: "nb", runtime: "box", runtime_asked: "box", bin: "claude", claude: true, aws_connections: ["staging"] };
  const m = { kind: "none", profiles: ["nb"] };
  const pre = page("", false), post = page("", true);
  pre.ctx.profiles = [needsBox];
  post.ctx.profiles = [needsBox];
  assert.strictEqual(pre.ctx.profileTouchLine(needsBox, undefined), "aws staging");
  assert.strictEqual(post.ctx.profileTouchLine(needsBox, ""), "needs a box · unavailable");
  assert.ok(pre.ctx.emptyStateKey(m).includes('["nb","aws staging"]'), pre.ctx.emptyStateKey(m));
  assert.ok(post.ctx.emptyStateKey(m).includes('["nb","needs a box · unavailable"]'), post.ctx.emptyStateKey(m));
});

test("the touches line is the dialog's, not the old 'binary · tier · workspace' one", () => {
  const pg = page("sysbox-runc");
  const rows = emptyRows(pg, ["aws-ops", "agent-box", "claude"]);
  assert.deepStrictEqual(rows.map((r) => r.touch), ["aws culipulse-prod, sandbox · gh culiops", "private copy of ~/culiops-agent-box", "no credentials"]);
});

test("a private copy's source is written with ~ for the home prefix in the empty state and the dialog list alike, and a path outside home is left whole", () => {
  const pg = page("sysbox-runc");
  const want = { "agent-box": "private copy of ~/culiops-agent-box", elsewhere: "private copy of /srv/shared/api" };
  const rows = emptyRows(pg, ["agent-box", "elsewhere"]);
  assert.deepStrictEqual(Object.fromEntries(rows.map((r) => [r.name, r.touch])), want);
  const dialog = dialogTouches(pg);
  for (const k of Object.keys(want)) assert.strictEqual(dialog[k], want[k], k + ": New session list");
});

test("a profile row's name starts with its kind icon: a terminal for a shell, the Claude mark for Claude, none for any other", () => {
  const pg = page("sysbox-runc");
  pg.ctx.profiles = [{ name: "bash", bin: "/bin/bash" }, { name: "claude", bin: "claude", claude: true }, { name: "py", bin: "python" }];
  pg.ctx.renderSpawnPlist();
  const nameOf = (n) => pg.els["spawn-plist"].find("sp-item").find((r) => r.dataset.name === n).find("sp-name")[0];
  const sh = nameOf("bash");
  assert.strictEqual(sh.children[0].tag, "svg");
  assert.strictEqual(sh.children[0].attrs["data-icon"], "terminal");
  assert.strictEqual(sh.children[0].attrs.stroke, "currentColor");
  assert.strictEqual(sh.children[0].attrs.width, "12");
  assert.strictEqual(sh.children[1].textContent, "bash", "the name text follows the icon");
  const cl = nameOf("claude");
  assert.strictEqual(cl.children[0].tag, "svg");
  assert.strictEqual(cl.children[0].attrs["data-icon"], "si:claude");
  assert.strictEqual(cl.children[0].attrs.width, "12");
  assert.strictEqual(cl.children[0].attrs.fill, "currentColor");
  assert.strictEqual(cl.children[0].children[0].tag, "path");
  assert.strictEqual(cl.textContent, "claude");
  const py = nameOf("py");
  assert.strictEqual(py.children.length, 1, "no icon, just the text");
  assert.strictEqual(py.textContent, "py");
});

test("each profile row's name carries its full name as the tooltip (a long one ends in an ellipsis, G9)", () => {
  const pg = page("sysbox-runc");
  pg.ctx.renderSpawnPlist();
  const rows = pg.els["spawn-plist"].find("sp-item");
  assert.strictEqual(rows.length, FIXTURES.length);
  for (const row of rows) {
    const name = row.find("sp-name")[0];
    assert.strictEqual(name.textContent, row.dataset.name);
    assert.strictEqual(name.title, row.dataset.name);
  }
});

test("the empty state's button says Start session, and starts the dialog on that profile", () => {
  const pg = page("sysbox-runc");
  assert.ok(rowsOf(pg).every((b) => b === "Start session"));
  const started = [];
  pg.ctx.openSpawnDialog = (n) => started.push(n);
  const root = pg.ctx.emptyNoneEl({ kind: "none", profiles: ["bash", "claude"] });
  for (const r of root.find("empst-row")) r.children.find((c) => c.tag === "button").onclick();
  assert.deepStrictEqual(started, ["bash", "claude"]);
  function rowsOf(p) { return emptyRows(p, ["bash", "claude", "agent-box"]).map((r) => r.button); }
});

test("a recent profile that no longer exists is left out of the empty state", () => {
  const pg = page("sysbox-runc");
  assert.deepStrictEqual(emptyRows(pg, ["gone", "bash"]).map((r) => r.name), ["bash"]);
});

test("the old profileTouches is gone and both empty-state callers use profileTouchLine(p, boxRuntime)", () => {
  assert.doesNotMatch(APP, /function profileTouches\b/);
  assert.doesNotMatch(APP, /\bprofileTouches\(/);
  assert.match(fnSource(APP, "emptyNoneEl"), /profileTouchLine\(p, metaLoaded \? boxRuntime : undefined\)/);
  assert.match(fnSource(APP, "emptyStateKey"), /profileTouchLine\(p, metaLoaded \? boxRuntime : undefined\)/);
  assert.match(read("spawnpanel.js"), /spawnPanelEl\("span", "sp-touch", profileTouchLine\(p, metaLoaded \? boxRuntime : undefined\)\)/);
  assert.doesNotMatch(fnSource(APP, "emptyNoneEl"), /"Start"/);
});

test("the empty state repaints when a touch line changes (its key reads the same line)", () => {
  const pg = page("sysbox-runc");
  const m = { kind: "none", profiles: ["aws-ops"] };
  const before = pg.ctx.emptyStateKey(m);
  assert.strictEqual(pg.ctx.emptyStateKey(m), before);
  FIXTURES.find((p) => p.name === "aws-ops").github_owners = ["other"];
  try {
    assert.notStrictEqual(pg.ctx.emptyStateKey(m), before);
  } finally { FIXTURES.find((p) => p.name === "aws-ops").github_owners = ["culiops"]; }
  // A host-only machine reads every profile as host, so its key differs from the boxed one's.
  assert.notStrictEqual(page("").ctx.emptyStateKey(m), before);
});

test("the empty state is desktop only: the pane area asks for it behind drawerDesktop()", () => {
  const src = fnSource(APP, "desktopEmptyState");
  assert.match(src, /if \(!drawerDesktop\(\)/);
  assert.ok(src.indexOf("drawerDesktop()") < src.indexOf("emptyNoneEl(m)"));
  const callers = APP.match(/emptyNoneEl\(/g) || [];
  assert.strictEqual(callers.length, 2, "its definition and the one call in desktopEmptyState");
  assert.match(read("deskshell.js"), /function emptyStateModel/);
});
