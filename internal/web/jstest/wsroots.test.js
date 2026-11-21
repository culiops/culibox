"use strict";
// Settings > Workspace roots (#343 Task 8): the pure model and notes, the add card's gate, and the list's DOM (Remove sends DELETE, the
// share button on a Mac, the damaged-list banner), against the shared fake DOM (the repo has no jsdom). wspicker.js's real wsCheckLine
// runs beside wsroots.js; createWsPicker, api and the other pages' functions are stubs.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

// The shared fake DOM keeps no listeners; the add card listens for Enter and Escape, so this file's copy of Node does (store.test.js
// keys its own `handlers` shape on the same class, hence not in fakedom.js).
Node.prototype.addEventListener = function (t, f) { ((this.listeners = this.listeners || {})[t] = this.listeners[t] || []).push(f); };
Node.prototype.fire = function (t, ev) { for (const f of (this.listeners || {})[t] || []) f(ev); };

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const tick = () => new Promise((r) => setImmediate(r));

const DAY = "2026-11-20T09:00:00Z";
const fmt = (iso) => new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
const view = (over) => Object.assign({
  roots: [
    { path: "/home/me", available: true, always: true },
    { path: "/srv/api", added: DAY, last_used: "2026-11-21T08:00:00Z", available: true },
    { path: "/mnt/disk/proj", added: DAY, available: false },
  ],
  elsewhere: [], corrupt: false,
}, over);

// One page: the scripts in a shared global scope like the browser, a fetch that answers from `server`, and the pages' stubs.
function page(over) {
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  doc.createElementNS = (_ns, t) => new Node(t, doc);
  doc.body = doc.createElement("body");
  doc.getElementById = (id) => {
    const walk = (n) => { if (n.id === id) return n; for (const c of n.children || []) { const f = c instanceof Node ? walk(c) : null; if (f) return f; } return null; };
    return walk(doc.body);
  };
  const server = { calls: [], replies: {}, fail: null, check: {} };
  const box = {
    document: doc, window: { innerWidth: 1280 }, console, Date, JSON, Map, Set, Math, Promise, Array, Object, String, Number, encodeURIComponent,
    toasts: [], refreshed: 0, vmAdds: [],
    showToast: (m, o) => box.toasts.push([m, !!(o && o.error)]),
    loadWorkspaceRoots: async () => { box.refreshed++; },
    loadWorkspaces: async () => { box.refreshed++; },
    openVMFoldersAdd: (p, mode, from) => box.vmAdds.push([p, mode, from]),
    openSettings: () => {},
    api: async (url, opts) => {
      const o = opts || {};
      server.calls.push({ url, method: o.method || "GET", body: o.body ? JSON.parse(o.body) : null });
      if (server.fail) { const e = new Error(server.fail.error); e.status = server.fail.status; e.code = server.fail.code; throw e; }
      if (url.startsWith("/api/workspace-roots")) return server.replies.roots;
      throw new Error("unexpected api " + url);
    },
    fetch: async (url) => {
      server.calls.push({ url, method: "GET", body: null, raw: true });
      const m = /path=([^&]*)/.exec(url);
      const p = m ? decodeURIComponent(m[1]) : "";
      return { ok: true, json: async () => server.check[p] || {} };
    },
  };
  Object.assign(box, over || {});
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["wspicker.js", "wsroots.js"]) new vm.Script(read(f), { filename: f }).runInContext(ctx);
  // The real factory is a function declaration, so it lands on the context and is replaced here, after it, by the stub.
  box.createWsPicker = (o) => {
    box.picker = { opts: o, set: (v) => { box.picker.value = v; o.input.value = v; }, destroy() { box.picker.gone = true; }, open() {}, isOpen: () => false };
    return box.picker;
  };
  // The panel as index.html carries it.
  const panel = doc.createElement("div");
  panel.id = "wsroots";
  const slot = doc.createElement("div");
  slot.className = "set-head";
  const body = doc.createElement("div");
  body.id = "wsr-body";
  panel.append(slot, body);
  doc.body.appendChild(panel);
  const head = doc.createElement("button");
  head.id = "set-act-wsroots";
  doc.body.appendChild(head);
  return { box, doc, server, body, head, lex: (n) => vm.runInContext(n, ctx) };
}

const btns = (n) => n.querySelectorAll("button");
const rowBtn = (p, path_) => btns(p.body).find((b) => b.dataset.wsrAct === path_);

// ---- the pure model ----------------------------------------------------------------------------------------------------------

test("wsrModel: home first, then the added roots in the server's order, with what each row can do", () => {
  const p = page();
  const m = p.box.wsrModel(view());
  assert.strictEqual(m.corrupt, false);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(m.rows)), [
    { path: "/home/me", label: "/home/me", always: true, available: true, added: "", lastUsed: "", removable: false },
    { path: "/srv/api", label: "/srv/api", always: false, available: true, added: DAY, lastUsed: "2026-11-21T08:00:00Z", removable: true },
    { path: "/mnt/disk/proj", label: "/mnt/disk/proj", always: false, available: false, added: DAY, lastUsed: "", removable: true },
  ]);
});

test("wsrModel: home is first even when the server sent it later; a damaged list is flagged; junk is empty", () => {
  const p = page();
  const swapped = view({ roots: view().roots.slice().reverse() });
  assert.strictEqual(p.box.wsrModel(swapped).rows[0].always, true);
  assert.strictEqual(p.box.wsrModel(view({ corrupt: true })).corrupt, true);
  for (const junk of [null, undefined, {}, { roots: "x" }, { roots: [null, { path: 3 }, { path: "" }] }]) {
    assert.deepStrictEqual(JSON.parse(JSON.stringify(p.box.wsrModel(junk))), { rows: [], corrupt: false });
  }
});

test("wsrRowNote: always, not available, or the added and last used dates (never when unused)", () => {
  const p = page();
  const note = (r) => p.box.wsrRowNote(r);
  assert.strictEqual(note({ always: true, available: true }), "always");
  assert.strictEqual(note({ always: true, available: false }), "always", "home is always, even when its folder is gone");
  assert.strictEqual(note({ always: false, available: false, added: DAY }), "not available");
  assert.strictEqual(note({ always: false, available: true, added: DAY, lastUsed: "2026-11-21T08:00:00Z" }), "added " + fmt(DAY) + " · last used " + fmt("2026-11-21T08:00:00Z"));
  assert.strictEqual(note({ always: false, available: true, added: DAY, lastUsed: "" }), "added " + fmt(DAY) + " · last used never");
  assert.strictEqual(note({ always: false, available: true, added: "", lastUsed: "" }), "added · last used never", "a hand-edited row with no dates");
  assert.strictEqual(note({ always: false, available: true, added: "not a date", lastUsed: "" }), "added · last used never");
});

test("wsrNoteParts: the note as the pieces that may wrap apart; wsrRowNote is the pieces joined by the separator", () => {
  const p = page();
  const parts = (r) => JSON.parse(JSON.stringify(p.box.wsrNoteParts(r)));
  assert.deepStrictEqual(parts({ always: true, available: true }), ["always"]);
  assert.deepStrictEqual(parts({ always: false, available: false, added: DAY }), ["not available"]);
  assert.deepStrictEqual(parts({ always: false, available: true, added: DAY, lastUsed: "2026-11-21T08:00:00Z" }), ["added " + fmt(DAY), "last used " + fmt("2026-11-21T08:00:00Z")]);
  assert.deepStrictEqual(parts({ always: false, available: true, added: DAY, lastUsed: "" }), ["added " + fmt(DAY), "last used never"]);
  for (const r of [{ always: true }, { available: false }, { available: true, added: DAY, lastUsed: "" }, { available: true, added: "", lastUsed: "" }]) {
    assert.strictEqual(p.box.wsrRowNote(r), parts(r).join(" \u00b7 "));
  }
});

test("the note cell holds one nowrap span per piece with the separator between, so it wraps only at the separator (R16)", () => {
  const p = page();
  p.box.wsrSetView(view());
  const rows = p.body.querySelectorAll("tr").slice(1);
  const note = (tr) => tr.querySelectorAll("td")[1];
  const spans = note(rows[1]).querySelectorAll("span");
  assert.deepStrictEqual(spans.map((s) => s.textContent), ["added " + fmt(DAY), "last used " + fmt("2026-11-21T08:00:00Z")]);
  assert.ok(spans.every((s) => s.className.split(" ").includes("wsr-nb")), "each piece is nowrap");
  assert.strictEqual(note(rows[1]).textContent, "added " + fmt(DAY) + " \u00b7 last used " + fmt("2026-11-21T08:00:00Z"), "the cell reads as the old one-line string");
  assert.strictEqual(note(rows[0]).textContent, "always");
  assert.strictEqual(note(rows[2]).textContent, "not available");
});

test("desktop.css: the note cell itself may wrap; only the pieces are nowrap (R16)", () => {
  const css = read("desktop.css");
  assert.doesNotMatch(css, /td\.wsr-note\s*\{[^}]*white-space:\s*nowrap/, "no cell-level nowrap on the note");
  assert.match(css, /#wsroots \.wsr-tbl td\.wsr-note \.wsr-nb\s*\{[^}]*white-space:\s*nowrap/, "each piece is nowrap");
});

test("wsrAddState: Add waits for would_add and posts the repo top the line named", () => {
  const p = page();
  const st = (c) => JSON.parse(JSON.stringify(p.box.wsrAddState(c)));
  assert.deepStrictEqual(st(null), { line: { kind: "none", text: "", share: false }, addDisabled: true, addPath: "" });
  const ok = { path: "/srv/api/web", exists: true, is_dir: true, root: "", would_add: true, add_root_path: "/srv/api", refused: "", box: { ok: true }, vm_shared: true };
  let s = st(ok);
  assert.strictEqual(s.addDisabled, false);
  assert.strictEqual(s.addPath, "/srv/api", "the repo top, not the typed folder");
  assert.strictEqual(s.line.text, "Starting here also lets CuliBox's Files, git and search open /srv/api (the repo this folder is in).");
  s = st(Object.assign({}, ok, { path: "/srv/api", add_root_path: "/srv/api" }));
  assert.strictEqual(s.line.text, "Starting here also lets CuliBox's Files, git and search open /srv/api.");
  s = st(Object.assign({}, ok, { would_add: false, refused: "a system folder", add_root_path: "/etc", path: "/etc" }));
  assert.strictEqual(s.addDisabled, true);
  assert.strictEqual(s.line.kind, "warn");
  s = st({ path: "/nope", exists: false, is_dir: false, would_add: false });
  assert.deepStrictEqual([s.addDisabled, s.line.kind], [true, "error"]);
  s = st({ path: "/home/me/x", exists: true, is_dir: true, root: "/home/me", would_add: false, add_root_path: "/home/me/x", refused: "", box: { ok: true }, vm_shared: true });
  assert.strictEqual(s.addDisabled, true);
  assert.strictEqual(s.line.text, "Already covered by /home/me. Nothing to add.");
  s = st(Object.assign({}, ok, { vm_shared: false }));
  assert.strictEqual(s.line.share, true, "the VM cannot see it: the share button is asked for");
  assert.strictEqual(s.addDisabled, false, "sharing is the user's call; the root can still be added");
});

// ---- the list ----------------------------------------------------------------------------------------------------------------

test("the list: home is marked always with no Remove; added roots show their dates and Remove; an unavailable one says so and stays removable", () => {
  const p = page();
  p.box.wsrSetView(view());
  const rows = p.body.querySelectorAll("tr").slice(1); // after the header row
  assert.strictEqual(rows.length, 3);
  assert.match(rows[0].textContent, /\/home\/me/);
  assert.match(rows[0].textContent, /always/);
  assert.strictEqual(btns(rows[0]).length, 0, "home has no Remove");
  assert.match(rows[1].textContent, new RegExp("added " + fmt(DAY).replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + " · last used"));
  assert.ok(rowBtn(p, "/srv/api"), "Remove on an added root");
  assert.strictEqual(rowBtn(p, "/srv/api").textContent, "Remove");
  assert.match(rows[2].textContent, /not available/);
  assert.ok(rowBtn(p, "/mnt/disk/proj"), "an unavailable root stays removable");
});

test("Remove sends DELETE with the path, repaints from the answer and refreshes the page's roots", async () => {
  const p = page();
  p.box.wsrSetView(view());
  p.server.replies.roots = view({ roots: [view().roots[0], view().roots[2]] });
  rowBtn(p, "/srv/api").click();
  await tick();
  assert.deepStrictEqual(p.server.calls.map((c) => [c.method, c.url, c.body]), [["DELETE", "/api/workspace-roots", { path: "/srv/api" }]]);
  assert.strictEqual(rowBtn(p, "/srv/api"), undefined, "the row is gone");
  assert.ok(rowBtn(p, "/mnt/disk/proj"));
  assert.strictEqual(p.box.refreshed, 2, "window.WORKSPACE_ROOTS and the workspaces list are reloaded");
  assert.deepStrictEqual(p.box.toasts, []);
});

test("a failed Remove is a toast and a reload; nothing is repainted from the failure", async () => {
  const p = page();
  p.box.wsrSetView(view());
  p.server.fail = { status: 409, code: "list_corrupt", error: "The workspace roots list is damaged." };
  rowBtn(p, "/srv/api").click();
  await tick();
  assert.deepStrictEqual(p.box.toasts, [["The workspace roots list is damaged.", true]]);
  assert.ok(p.server.calls.some((c) => c.method === "GET" && c.url === "/api/workspace-roots"), "the list is asked again");
});

test("a damaged list shows the banner with the exact words, and the head's Add waits", () => {
  const p = page();
  p.box.wsrSetView(view({ corrupt: true, roots: [view().roots[0]] }));
  const banner = p.body.querySelector(".banner");
  assert.ok(banner, "the banner");
  assert.strictEqual(banner.textContent, "workspace-roots.json is damaged; CuliBox is using your home folder only until it is fixed or removed.");
  assert.strictEqual(p.head.disabled, true);
  p.box.wsrSetView(view());
  assert.strictEqual(p.body.querySelector(".banner"), null);
  assert.strictEqual(p.head.disabled, false);
});

test("a Mac row outside the VM shares carries the share button, which opens the prefilled VM folders card", async () => {
  const p = page({ vmFoldersOn: true });
  p.server.check["/srv/api"] = { path: "/srv/api", exists: true, is_dir: true, vm_shared: false };
  p.server.check["/mnt/disk/proj"] = { path: "/mnt/disk/proj", exists: false, is_dir: false, vm_shared: false };
  p.box.wsrSetView(view());
  await tick(); await tick();
  const share = btns(p.body).filter((b) => b.textContent === "Share this folder…");
  assert.strictEqual(share.length, 1, "one row: home is never offered, an unavailable root has nothing to share");
  share[0].click();
  assert.deepStrictEqual(p.box.vmAdds, [["/srv/api", "rw", "wsroots"]]);
});

test("without a VM (Linux) there is no share button and no per-row check", async () => {
  const p = page();
  p.box.wsrSetView(view());
  await tick();
  assert.strictEqual(btns(p.body).filter((b) => b.textContent === "Share this folder…").length, 0);
  assert.strictEqual(p.server.calls.filter((c) => c.raw).length, 0);
});

// ---- the add card ------------------------------------------------------------------------------------------------------------

const CHECK = { path: "/srv/api/web", exists: true, is_dir: true, root: "", would_add: true, add_root_path: "/srv/api", refused: "", box: { ok: true }, vm_shared: true };
const card = (p) => ({ input: p.doc.getElementById("wsr-path-in"), msg: p.doc.getElementById("wsr-msg"), add: btns(p.body).find((b) => b.textContent === "Add folder" && b.className.includes("btn-primary")) });

test("Add folder opens the card with Add off; a would_add check turns it on and POSTs the check's add_root_path", async () => {
  const p = page();
  p.box.wsrSetView(view());
  p.box.openWsRootsAdd("");
  let c = card(p);
  assert.ok(c.input && c.add, "the card");
  assert.strictEqual(c.add.disabled, true);
  p.box.picker.opts.onCheck(CHECK);
  c = card(p);
  assert.strictEqual(c.add.disabled, false);
  assert.match(c.msg.textContent, /open \/srv\/api \(the repo this folder is in\)\.$/);
  p.server.replies.roots = view({ roots: view().roots.concat([{ path: "/srv/api2", added: DAY, available: true }]) });
  c.add.click();
  await tick();
  assert.deepStrictEqual(p.server.calls.map((x) => [x.method, x.body]), [["POST", { path: "/srv/api" }]]);
  assert.ok(rowBtn(p, "/srv/api2"), "the list repaints from the answer");
  assert.strictEqual(card(p).input, null, "the card closes");
  assert.strictEqual(p.box.picker.gone, true, "the picker is taken down with it");
  assert.strictEqual(p.box.refreshed, 2);
});

test("Add stays off for a refused, covered or missing folder, and while the check is unanswered", () => {
  const p = page();
  p.box.wsrSetView(view());
  p.box.openWsRootsAdd("");
  const on = () => card(p).add.disabled === false;
  p.box.picker.opts.onCheck(Object.assign({}, CHECK, { would_add: false, refused: "a system folder", path: "/etc", add_root_path: "/etc" }));
  assert.strictEqual(on(), false);
  p.box.picker.opts.onCheck(Object.assign({}, CHECK, { would_add: false, root: "/home/me" }));
  assert.strictEqual(on(), false);
  assert.match(card(p).msg.textContent, /Already covered by \/home\/me\. Nothing to add\./);
  p.box.picker.opts.onCheck({ path: "/gone", exists: false, is_dir: false });
  assert.strictEqual(on(), false);
  p.box.picker.opts.onCheck(CHECK);
  assert.strictEqual(on(), true);
  p.box.picker.opts.onCheck(null); // the field changed: the old answer was about another folder
  assert.strictEqual(on(), false);
});

test("a refused Add keeps the card open and says why in it", async () => {
  const p = page();
  p.box.wsrSetView(view());
  p.box.openWsRootsAdd("");
  p.box.picker.opts.onCheck(CHECK);
  p.server.fail = { status: 409, code: "cap", error: "too many workspace roots (32)" };
  card(p).add.click();
  await tick();
  const c = card(p);
  assert.ok(c.input, "still open");
  assert.match(c.msg.textContent, /too many workspace roots \(32\)/);
  assert.strictEqual(c.msg.getAttribute("role"), "alert");
  assert.deepStrictEqual(p.box.toasts, []);
});

test("the picker is asked for a check, on demand only, and a prefilled path is set into it", () => {
  const p = page();
  p.box.wsrSetView(view());
  p.box.openWsRootsAdd("/Volumes/disk/proj");
  const o = p.box.picker.opts;
  assert.strictEqual(typeof o.onCheck, "function");
  assert.strictEqual(o.openOnFocus(), false);
  assert.strictEqual(o.openOnClick(), false);
  assert.strictEqual(o.openOnType(), false);
  assert.strictEqual(p.box.picker.value, "/Volumes/disk/proj");
});

test("Enter in the field adds, Escape closes the card", async () => {
  const p = page();
  const cardEl = () => { let n = card(p).input; while (n && !n.className.split(" ").includes("wsr-add")) n = n.parent; return n; };
  const key = (k) => ({ key: k, target: card(p).input, preventDefault() {}, stopPropagation() {} });
  p.box.wsrSetView(view());
  p.box.openWsRootsAdd("");
  p.box.picker.opts.onCheck(CHECK);
  p.server.replies.roots = view();
  cardEl().fire("keydown", key("Enter"));
  await tick();
  assert.deepStrictEqual(p.server.calls.map((x) => x.method), ["POST"]);
  p.box.openWsRootsAdd("");
  cardEl().fire("keydown", key("Escape"));
  assert.strictEqual(card(p).input, null);
});

test("an Escape the picker already took (it closed its list) leaves the card open", () => {
  const p = page();
  p.box.wsrSetView(view());
  p.box.openWsRootsAdd("");
  let n = card(p).input;
  while (n && !n.className.split(" ").includes("wsr-add")) n = n.parent;
  n.fire("keydown", { key: "Escape", target: card(p).input, defaultPrevented: true, preventDefault() {}, stopPropagation() {} });
  assert.ok(card(p).input, "still open");
  n.fire("keydown", { key: "Escape", target: card(p).input, defaultPrevented: false, preventDefault() {}, stopPropagation() {} });
  assert.strictEqual(card(p).input, null, "an Esc nobody took closes it");
});

test("hostile paths and error strings are drawn as text: no element is made from them", async () => {
  const BAD = "<img src=x onerror=alert(1)>";
  const tags = (n, out = []) => { out.push(n.tag); for (const c of n.children || []) if (c instanceof Node) tags(c, out); return out; };
  const p = page({ vmFoldersOn: true });
  p.server.check["/srv/" + BAD] = { path: "/srv/" + BAD, exists: true, is_dir: true, vm_shared: false };
  p.box.wsrSetView(view({ roots: [view().roots[0], { path: "/srv/" + BAD, added: DAY, available: true }] }));
  await tick(); await tick();
  assert.match(p.body.textContent, /<img src=x onerror=alert\(1\)>/, "the path is on the page as text");
  p.box.openWsRootsAdd("");
  p.box.picker.opts.onCheck(Object.assign({}, CHECK, { path: "/x/" + BAD, add_root_path: "/x/" + BAD, vm_shared: false }));
  assert.match(card(p).msg.textContent, /<img src=x/);
  p.server.fail = { status: 409, code: "cap", error: BAD };
  card(p).add.click();
  await tick();
  assert.strictEqual(card(p).msg.textContent, BAD);
  p.server.fail = { status: 409, code: "list_corrupt", error: BAD };
  rowBtn(p, "/srv/" + BAD).click();
  await tick();
  assert.deepStrictEqual(p.box.toasts, [[BAD, true]], "a toast takes the text as it is");
  assert.ok(!tags(p.body).includes("img"), "no <img> element exists: " + tags(p.body).join(","));
});

test("Cancel closes the card and the head's Add comes back", () => {
  const p = page();
  p.box.wsrSetView(view());
  p.box.openWsRootsAdd("");
  assert.strictEqual(p.head.disabled, true, "the head's Add waits while the card is open");
  btns(p.body).find((b) => b.textContent === "Cancel").click();
  assert.strictEqual(card(p).input, null);
  assert.strictEqual(p.head.disabled, false);
});

// ---- wiring ------------------------------------------------------------------------------------------------------------------

test("the Settings page carries the tab after VM folders, the panel, its footer and the script, in order", () => {
  const html = read("index.html");
  const tabs = [...html.matchAll(/<button type="button" class="t[^"]*" data-tab="([a-z]+)"([^>]*)>([^<]*)</g)].map((m) => [m[1], m[2], m[3]]);
  const names = tabs.map((t) => t[0]);
  assert.strictEqual(names.indexOf("wsroots"), names.indexOf("vmfolders") + 1);
  const t = tabs[names.indexOf("wsroots")];
  assert.strictEqual(t[2], "Workspace roots");
  assert.doesNotMatch(t[1], /\bhidden\b/, "always visible");
  assert.match(html, /<div class="dbd pad" id="wsroots" role="tabpanel" hidden>\s*<div class="set-head"><\/div>\s*<div id="wsr-body"><\/div>\s*<\/div>/);
  assert.match(html, /<div class="dft" id="foot-wsroots" hidden>[\s\S]*?data-close="settings-modal">Done<\/button>/);
  assert.ok(html.indexOf("/wsroots.js") > html.indexOf("/vmfolders.js") && html.indexOf("/wsroots.js") < html.indexOf('/app.js"'), "script order");
  assert.ok(html.indexOf("/wsroots.js") > html.indexOf("/wspicker.js"), "wspicker.js first");
});

test("the head, the tab hook and the shell load: Settings names the page, the action opens the card, selecting the tab loads the list", () => {
  const { settingsHeadSpec } = require("../static/settingshead.js");
  assert.deepStrictEqual(settingsHeadSpec("wsroots", {}), {
    title: "Workspace roots",
    desc: "Workspace roots are the folders CuliBox's Files, git and search can open. Home is always one. Starting a session in a folder outside them adds that folder here. Removing one does not stop sessions running in it.",
    action: { id: "set-act-wsroots", label: "Add folder" },
  });
  const head = read("settingshead.js");
  assert.match(head, /"set-act-wsroots": \(\) => openWsRootsAdd\(""\)/);
  assert.match(head, /tab === "wsroots" && typeof wsrSync === "function"\) wsrSync\(\)/);
  assert.match(read("shell.js"), /name === "wsroots" && typeof loadWsRoots === "function"\) loadWsRoots\(\)/);
});
