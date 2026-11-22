"use strict";
// #343: the New session form and the workspace roots. What the form sends (add_root), what it does with the
// answer's `root` (refresh the roots, or say why not), and the line + Start gate drawn from the check answer. The
// functions are big files' own, cut out and run against stubs (as closepage-callers.test.js does).
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
function fnSource(file, name) {
  const text = src(file);
  const m = new RegExp("^(?:async )?function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function of " + file);
  const end = text.indexOf("\n}\n", m.index);
  assert.ok(end > m.index, name + " ends at column 0");
  return text.slice(m.index, end + 2);
}
function world(fns, over) {
  const box = Object.assign({ console, Promise, Array, Object, JSON, window: { innerWidth: 1280 } }, over);
  const ctx = vm.createContext(box);
  for (const [file, name] of fns) new vm.Script(fnSource(file, name), { filename: file + ":" + name }).runInContext(ctx);
  return box;
}

// --- spawnSession ---------------------------------------------------------------------------------------------
function spawnWorld(reply) {
  const log = [];
  const box = world([["app.js", "spawnSession"]], {
    closePage: () => true,
    api: async () => reply,
    LAST_SPAWN_KEY: "k", localStorage: { setItem() {} }, deskStorage: () => ({}), writeRecent() {}, readRecent: () => [], pushRecentProfile: () => [],
    refreshSessions: async () => { log.push("sessions"); },
    openTerminal: () => { log.push("open"); },
    loadWorkspaces: async () => { log.push("workspaces"); },
    loadWorkspaceRoots: async () => { log.push("roots"); },
    showToast: (m, o) => { log.push(["toast", m, !!(o && o.warn === true)]); },
    mobileActive: () => false, shortPath: (p) => p, toastDropsSuffix: () => "", toastCloneSuffix: () => "", toastReturnSuffix: () => "", cloneLevel: () => "",
    killSession() {},
  });
  return { box, log };
}

test("spawnSession: a Start that added a root refreshes the workspaces and the roots before the pane opens", async () => {
  const t = spawnWorld({ id: "s1", name: "n", cwd: "/srv/code", root: { added: true, path: "/srv/code" } });
  await t.box.spawnSession({ agent: "claude" });
  const open = t.log.indexOf("open");
  assert.ok(open > 0);
  assert.ok(t.log.indexOf("workspaces") > t.log.indexOf("sessions") && t.log.indexOf("workspaces") < open, JSON.stringify(t.log));
  assert.ok(t.log.indexOf("roots") > 0 && t.log.indexOf("roots") < open, JSON.stringify(t.log));
  assert.ok(!t.log.some((e) => Array.isArray(e) && /not added/.test(e[1])), "no failure toast");
});

test("spawnSession: a Start that added a root says so, naming the folder, after the session opens", async () => {
  const t = spawnWorld({ id: "s1", name: "n", cwd: "/srv/code/api", root: { added: true, path: "/srv/code" } });
  await t.box.spawnSession({ agent: "claude" }, { toast: false });
  const toast = t.log.find((e) => Array.isArray(e) && /workspace root/.test(e[1]));
  assert.deepStrictEqual(toast, ["toast", "/srv/code is now a workspace root. Remove it in Settings › Workspace roots.", false]);
  assert.ok(t.log.indexOf(toast) > t.log.indexOf("open"), "after the pane opens, like the failure toast");
});

test("spawnSession: a root that was not added says why in the words of the spec, after the session opens", async () => {
  const t = spawnWorld({ id: "s1", name: "n", cwd: "/srv/code", root: { added: false, path: "/srv/code", reason: "the 32-root limit is reached" } });
  await t.box.spawnSession({ agent: "claude" });
  assert.deepStrictEqual(t.log.filter((e) => e === "workspaces" || e === "roots"), [], "nothing was added: nothing to refresh");
  const toast = t.log.find((e) => Array.isArray(e) && /not added/.test(e[1]));
  assert.deepStrictEqual(toast, ["toast", "Session started. /srv/code was not added as a workspace root: the 32-root limit is reached.", true]);
  assert.ok(t.log.indexOf(toast) > t.log.indexOf("open"));
});

test("spawnSession: no `root` in the answer changes nothing", async () => {
  const t = spawnWorld({ id: "s1", name: "n", cwd: "/home/u/x" });
  await t.box.spawnSession({ agent: "claude" });
  assert.deepStrictEqual(t.log.filter((e) => e === "workspaces" || e === "roots" || (Array.isArray(e) && /workspace root/.test(e[1]))), []);
});

// --- who sends add_root -----------------------------------------------------------------------------------------
test("only the New session form's submit sends add_root; the account login and the payload builder do not", () => {
  const app = src("app.js");
  const at = app.indexOf('$("spawn").addEventListener("submit"');
  const body = app.slice(at, app.indexOf("spawnSession(req", at));
  assert.match(body, /if \(spawnAddsRoot\(\)\) req\.add_root = true;/);
  assert.ok(body.indexOf("req.add_root = true") > body.indexOf("const req = spawnPayload("), "after the payload is built");
  assert.ok(!/add_root/.test(src("accounts.js")), "log in never asks to add a root");
  assert.ok(!/add_root/.test(src("spawnform.js")), "spawnPayload (also Duplicate's) never sets it");
  assert.strictEqual((app.match(/req\.add_root/g) || []).length, 1, "one sender");
});

// The phone shows no line saying what Start would open (#340 freeze), so it must not add a root either.
test("spawnAddsRoot: the desktop form adds a root, the phone form never does", () => {
  const run = (mobile) => world([["app.js", "drawerDesktop"], ["app.js", "spawnAddsRoot"]], { mobileActive: () => mobile }).spawnAddsRoot();
  assert.strictEqual(run(false), true, "desktop");
  assert.strictEqual(run(true), false, "phone");
});

// --- the line and the Start gate ---------------------------------------------------------------------------------
class Node {
  constructor(tag) { this.tag = tag; this.children = []; this.className = ""; this.textContent = ""; this.hidden = false; this.onclick = null; this.classList = { toggle() {} }; }
  replaceChildren(...cs) { this.children = cs; }
}
const chk = (over) => Object.assign({
  path: "/srv/x", exists: true, is_dir: true, root: "", would_add: true, add_root_path: "/srv/x", refused: "",
  box: { ok: true, reason: "", code: "" }, vm_shared: true,
}, over);

function lineWorld(state) {
  const el = new Node("div");
  el.hidden = true;
  const calls = [];
  const wsp = fs.readFileSync(path.join(STATIC, "wspicker.js"), "utf8");
  const box = world([
    ["app.js", "spawnWsCheckOn"], ["app.js", "spawnWsCheckBlock"], ["app.js", "setSpawnWsCheck"], ["app.js", "paintSpawnWsCheck"],
  ], Object.assign({
    $: (id) => (id === "spawn-ws-check" ? el : null),
    document: { createElement: (t) => new Node(t) },
    drawerDesktop: () => true,
    spawnWhere: () => ({ runtime: "box", clone: false }),
    spawnCurrentTier: () => "box",
    syncSpawnGate: () => calls.push("gate"),
    closeDialog: (id) => calls.push(["close", id]),
    openSettings: (p) => calls.push(["settings", p]),
    openVMFoldersAdd: (...a) => calls.push(["add", ...a]),
  }, state));
  vm.runInContext("var spawnWsCheck = null;", box);
  vm.runInContext(wsp.slice(wsp.indexOf("function wsCheckLine"), wsp.indexOf("function createWsPicker")), box);
  return { el, box, calls };
}
const text = (el) => el.children.filter((c) => c.tag === "span").map((c) => c.textContent).join("");

test("the check answer is drawn as one line with its kind; the clone card and the phone draw nothing", () => {
  const t = lineWorld({ spawnWhere: () => ({ runtime: "host", clone: false }), spawnCurrentTier: () => "host" });
  t.box.setSpawnWsCheck(chk());
  assert.strictEqual(t.el.hidden, false);
  assert.strictEqual(t.el.className, "spawn-ws-check ck-info");
  assert.strictEqual(text(t.el), "Starting here also lets CuliBox's Files, git and search open /srv/x.");
  assert.deepStrictEqual(t.calls, ["gate"], "the Start gate is re-read with every answer");
  t.box.setSpawnWsCheck(chk({ path: "/etc/x", add_root_path: "/etc", would_add: false, refused: "system folder" }));
  assert.strictEqual(t.el.className, "spawn-ws-check ck-warn");
  t.box.setSpawnWsCheck(chk({ exists: false, is_dir: false, would_add: false }));
  assert.strictEqual(t.el.className, "spawn-ws-check ck-error");
  t.box.setSpawnWsCheck(chk({ root: "/srv", would_add: false }));
  assert.strictEqual(t.el.hidden, true, "a covered folder: no line");
  t.box.setSpawnWsCheck(null);
  assert.strictEqual(t.el.hidden, true);

  const clone = lineWorld({ spawnWhere: () => ({ runtime: "box", clone: true }) });
  clone.box.setSpawnWsCheck(chk());
  assert.strictEqual(clone.el.hidden, true, "a clone starts in its own copy and adds no root");
  assert.strictEqual(clone.box.spawnWsCheckBlock(), "");
  const phone = lineWorld({ drawerDesktop: () => false });
  phone.box.setSpawnWsCheck(chk());
  assert.strictEqual(phone.el.hidden, true);
});

test("an unshared folder on the box tier: the line warns, Start is blocked, and the button opens the add card for the repo top", () => {
  const unseen = chk({ path: "/srv/x/api", add_root_path: "/srv/x", vm_shared: false, box: { ok: false, reason: "not shared", code: "not_shared" } });
  const t = lineWorld({});
  t.box.setSpawnWsCheck(unseen);
  assert.strictEqual(t.el.className, "spawn-ws-check ck-warn");
  assert.match(t.box.spawnWsCheckBlock(), /The box VM can't see \/srv\/x, so git status and boxes won't work there\.$/);
  const btn = t.el.children.find((c) => c.tag === "button");
  assert.ok(btn && btn.textContent === "Share this folder…");
  t.calls.length = 0;
  btn.onclick();
  assert.deepStrictEqual(t.calls, [["close", "spawn-modal"], ["settings", "vmfolders"], ["add", "/srv/x", "rw", "spawn"]]);
  // Switching to host: the same answer no longer blocks, and the line stays informative.
  const host = lineWorld({ spawnCurrentTier: () => "host", spawnWhere: () => ({ runtime: "host", clone: false }) });
  host.box.setSpawnWsCheck(unseen);
  assert.strictEqual(host.box.spawnWsCheckBlock(), "");
  assert.strictEqual(host.el.className, "spawn-ws-check ck-info");
});

test("syncSpawnGate: the workspace line's block disables Start with its text, without asking the box daemon again", () => {
  const el = new Node("div");
  const go = { disabled: false, attrs: {}, setAttribute(k, v) { this.attrs[k] = v; }, removeAttribute(k) { delete this.attrs[k]; } };
  const errs = [];
  const box = world([["spawnpanel.js", "syncSpawnGate"]], {
    spawnPanelOn: () => true, spawnGateInput: () => ({}),
    spawnBoxGate: () => ({ show: false, tone: "", line: "", more: "", blocked: false, noLayer: false, text: "" }),
    $: (id) => (id === "spawn-nobox" ? el : id === "spawn" ? { querySelector: () => go } : id === "spawn-err" ? { textContent: "" } : null),
    spawnPanelEl: () => new Node("span"), spawnBusy: false,
    setDisabledReason: (b, off, reason) => { b.off = off; b.reason = reason; },
    setDialogError: (id, t) => errs.push(t),
    spawnWsCheckBlock: () => "why not",
  });
  vm.runInContext("var spawnGateErr = '';", box);
  const g = box.syncSpawnGate();
  assert.strictEqual(g.blocked, true);
  assert.strictEqual(g.text, "why not");
  assert.strictEqual(g.noLayer, true, "the submit handler need not re-read the daemon for it");
  assert.deepStrictEqual([go.off, go.reason], [true, "why not"]);
  // No block: as before.
  box.spawnWsCheckBlock = () => "";
  assert.strictEqual(box.syncSpawnGate().blocked, false);
  // The page may not define it at all (older load order, tests): no block.
  delete box.spawnWsCheckBlock;
  assert.strictEqual(box.syncSpawnGate().blocked, false);
});

test("the picker is built with the check line on the desktop only, and the markup has the line's element", () => {
  const app = src("app.js");
  const at = app.indexOf("spawnWsPicker = createWsPicker({");
  const call = app.slice(at, app.indexOf("});", at));
  assert.match(call, /onCheck: setSpawnWsCheck, checkOn: drawerDesktop/);
  assert.match(src("index.html"), /<div class="spawn-ws-check" id="spawn-ws-check" role="status" hidden><\/div>/);
  assert.match(src("desktop.css"), /\.spawn-ws-check\[hidden\] \{ display: none; \}/);
});

test("both pickers' 403 text is the new msgOutside wording, and the old home wording is gone", () => {
  const server = fs.readFileSync(path.join(__dirname, "..", "fsmsg.go"), "utf8");
  const msg = /msgOutside\s*=\s*"([^"]+)"/.exec(server)[1];
  assert.match(src("dirpicker.js"), new RegExp("That path is " + msg.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  for (const f of ["dirpicker.js", "wspicker.js", "viewerpath.js"]) assert.ok(!/outside your home/.test(src(f)), f);
});
