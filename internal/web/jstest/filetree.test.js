"use strict";
const test = require("node:test");
const assert = require("node:assert");
const FT = require("../static/filetree.js");
const { iconFor, treeBranch, actionsLabel } = FT;

// Codepoints are asserted by value, not by name. A glyph that silently changes
// is a tofu box in the tree, and only the number can catch that.
const FOLDER = "", FOLDER_OPEN = "", FILE = "";

test("a directory reports its open state", () => {
  assert.strictEqual(iconFor("internal", true, false), FOLDER);
  assert.strictEqual(iconFor("internal", true, true), FOLDER_OPEN);
});

test("known extensions map to their own glyph", () => {
  assert.strictEqual(iconFor("web.go", false), "");
  assert.strictEqual(iconFor("palette.js", false), "");
  assert.strictEqual(iconFor("README.md", false), "");
  assert.strictEqual(iconFor("config.json", false), "");
  assert.strictEqual(iconFor("index.html", false), "");
  assert.strictEqual(iconFor("xterm.css", false), "");
  assert.strictEqual(iconFor("logo.png", false), "");
  assert.strictEqual(iconFor("test.sh", false), "");
});

// The trap: .gitignore has no extension in the usual sense, and a naive
// "split on the last dot" treats "gitignore" as its extension.
test("exact names are matched before extensions", () => {
  assert.strictEqual(iconFor(".gitignore", false), "");
  assert.strictEqual(iconFor(".gitattributes", false), "");
});

test("matching ignores case", () => {
  assert.strictEqual(iconFor("README.MD", false), "");
  assert.strictEqual(iconFor("LOGO.PNG", false), "");
});

test("an unknown extension falls back to the plain file glyph", () => {
  assert.strictEqual(iconFor("notes.xyz", false), FILE);
  assert.strictEqual(iconFor("Makefile", false), FILE);
  assert.strictEqual(iconFor("", false), FILE);
});

test("a dotfile with no second dot is not read as an extension", () => {
  assert.strictEqual(iconFor(".env", false), FILE);
});

test("every glyph is exactly one character", () => {
  for (const n of ["web.go", ".gitignore", "x.unknown", "d"]) {
    assert.strictEqual(Array.from(iconFor(n, false)).length, 1, n);
    assert.strictEqual(Array.from(iconFor(n, true, false)).length, 1, n);
  }
});

// A prototype-chain lookup returns a function or an object, and the DOM would
// coerce either into a multi-character string that shifts the whole name column.
test("names that collide with Object.prototype fall back to the file glyph", () => {
  for (const n of ["constructor", "__proto__", "toString", "valueOf", "hasOwnProperty"]) {
    assert.strictEqual(iconFor(n, false), FILE, n);
    assert.strictEqual(Array.from(iconFor(n, false)).length, 1, n);
  }
});

test("extensions that collide with Object.prototype fall back too", () => {
  assert.strictEqual(iconFor("x.constructor", false), FILE);
  assert.strictEqual(iconFor("x.toString", false), FILE);
});

// The tree's shape is pure string work, so it is testable even though the tree
// itself is DOM-bound. The case that matters is a last child whose ANCESTOR is
// not last: its parent's vertical must continue past it.
test("a root-level entry has no ancestry", () => {
  assert.deepStrictEqual(treeBranch("", false), { rowPrefix: "├ ", childPrefix: "│ " });
  assert.deepStrictEqual(treeBranch("", true),  { rowPrefix: "└ ", childPrefix: "  " });
});

test("a non-last ancestor keeps drawing its vertical", () => {
  const parent = treeBranch("", false);          // "│ "
  assert.deepStrictEqual(treeBranch(parent.childPrefix, true),
    { rowPrefix: "│ └ ", childPrefix: "│   " });
});

test("a last ancestor stops drawing its vertical", () => {
  const parent = treeBranch("", true);           // "  "
  assert.deepStrictEqual(treeBranch(parent.childPrefix, true),
    { rowPrefix: "  └ ", childPrefix: "    " });
});

test("row prefixes grow exactly two characters per level", () => {
  let p = "";
  for (let depth = 0; depth < 5; depth++) {
    const b = treeBranch(p, false);
    assert.strictEqual(b.rowPrefix.length, depth * 2 + 2, "depth " + depth);
    p = b.childPrefix;
  }
});

test("childPrefix is always the same width as rowPrefix", () => {
  for (const [prefix, last] of [["", false], ["", true], ["│ ", false], ["  ", true]]) {
    const b = treeBranch(prefix, last);
    assert.strictEqual(b.childPrefix.length, b.rowPrefix.length, prefix + "/" + last);
  }
});

// U6: the row's ⋯ menu button names itself by what it acts on, matching the
// session row's kebab ("Session actions" on both aria-label and title).
test("the row menu button is labeled by kind", () => {
  assert.strictEqual(actionsLabel(true), "Folder actions");
  assert.strictEqual(actionsLabel(false), "File actions");
});

// --- #55: a read-only listing hides what the server would refuse ---------------

test("listingReadOnly: only the exact boolean true counts", () => {
  assert.strictEqual(FT.listingReadOnly({ path: "/tmp/claude-1", entries: [], readonly: true }), true);
  assert.strictEqual(FT.listingReadOnly({ path: "/h", entries: [] }), false, "key absent = writable");
  assert.strictEqual(FT.listingReadOnly({ readonly: false }), false);
  assert.strictEqual(FT.listingReadOnly({ readonly: "true" }), false, "a string is not the server's answer");
  assert.strictEqual(FT.listingReadOnly({ readonly: 1 }), false);
  assert.strictEqual(FT.listingReadOnly(null), false);
  assert.strictEqual(FT.listingReadOnly(undefined), false);
});

// The labels are copied from what openRowMenu printed before this change, not
// read back from the code under test, so a reworded or reordered item fails here.
const TODAY = {
  dir: ["Insert path at prompt", "Copy path", "Copy relative path", null, "Download .tar.gz", "New folder here\u2026", null, "Rename\u2026", "Delete\u2026"],
  file: ["Insert path at prompt", "Copy path", "Copy relative path", "Copy contents", null, "Download", null, "Rename\u2026", "Delete\u2026"],
  image: ["Insert path at prompt", "Copy path", "Copy relative path", null, "Download", null, "Rename\u2026", "Delete\u2026"],
};
const labelled = (ids, isDir) => ids.map((id) => (id === null ? null : FT.rowMenuLabel(id, isDir)));

test("rowMenuIds: a writable listing keeps today's menu exactly", () => {
  assert.deepStrictEqual(FT.rowMenuIds({ isDir: true, isImage: false, readOnly: false }),
    ["insert", "copy", "copyrel", null, "download", "newdir", null, "rename", "delete"]);
  assert.deepStrictEqual(FT.rowMenuIds({ isDir: false, isImage: false, readOnly: false }),
    ["insert", "copy", "copyrel", "copycontents", null, "download", null, "rename", "delete"]);
  assert.deepStrictEqual(FT.rowMenuIds({ isDir: false, isImage: true, readOnly: false }),
    ["insert", "copy", "copyrel", null, "download", null, "rename", "delete"]);
  assert.deepStrictEqual(labelled(FT.rowMenuIds({ isDir: true, isImage: false, readOnly: false }), true), TODAY.dir);
  assert.deepStrictEqual(labelled(FT.rowMenuIds({ isDir: false, isImage: false, readOnly: false }), false), TODAY.file);
  assert.deepStrictEqual(labelled(FT.rowMenuIds({ isDir: false, isImage: true, readOnly: false }), false), TODAY.image);
});

test("rowMenuIds: omitting readOnly is the writable menu", () => {
  assert.deepStrictEqual(FT.rowMenuIds({ isDir: true, isImage: false }), FT.rowMenuIds({ isDir: true, isImage: false, readOnly: false }));
});

test("rowMenuIds: a read-only listing drops New folder, Rename, Delete and their rule", () => {
  assert.deepStrictEqual(FT.rowMenuIds({ isDir: true, isImage: false, readOnly: true }),
    ["insert", "copy", "copyrel", null, "download"]);
  assert.deepStrictEqual(FT.rowMenuIds({ isDir: false, isImage: false, readOnly: true }),
    ["insert", "copy", "copyrel", "copycontents", null, "download"]);
  assert.deepStrictEqual(FT.rowMenuIds({ isDir: false, isImage: true, readOnly: true }),
    ["insert", "copy", "copyrel", null, "download"]);
  assert.deepStrictEqual(labelled(FT.rowMenuIds({ isDir: true, isImage: false, readOnly: true }), true),
    ["Insert path at prompt", "Copy path", "Copy relative path", null, "Download .tar.gz"]);
  assert.deepStrictEqual(labelled(FT.rowMenuIds({ isDir: false, isImage: false, readOnly: true }), false),
    ["Insert path at prompt", "Copy path", "Copy relative path", "Copy contents", null, "Download"]);
});

test("rowMenuIds: no menu ends on a rule or has two in a row", () => {
  for (const isDir of [true, false]) for (const isImage of [true, false]) for (const readOnly of [true, false]) {
    const ids = FT.rowMenuIds({ isDir, isImage: isImage && !isDir, readOnly });
    assert.notStrictEqual(ids[ids.length - 1], null);
    assert.notStrictEqual(ids[0], null);
    ids.forEach((id, i) => { if (id === null) assert.notStrictEqual(ids[i - 1], null); });
  }
});

test("rowMenuIds: noInsert leaves out Insert path at prompt and nothing else; omitted it is today's menu", () => {
  for (const isDir of [true, false]) for (const readOnly of [true, false]) {
    const o = { isDir, isImage: false, readOnly };
    const today = FT.rowMenuIds(o);
    assert.strictEqual(today[0], "insert");
    assert.deepStrictEqual(FT.rowMenuIds(Object.assign({ noInsert: true }, o)), today.slice(1));
    assert.deepStrictEqual(FT.rowMenuIds(Object.assign({ noInsert: false }, o)), today);
  }
});

// --- the menu itself, on a fake DOM ---------------------------------------------

function fakeNode(tag) {
  return { tag, className: "", textContent: "", type: "", onclick: null };
}
// The phone's width: these tests pin the plain path header, buttons and rules the phone's menu has always had.
// The desktop's menu is the kit's (kitmenu.js rows), and filetree-keys.test.js is where it is checked.
function installMenuDom() {
  const menu = {
    nodes: [], style: {}, offsetWidth: 180, offsetHeight: 200,
    classList: { add() {}, remove() {}, contains: () => false },
    setAttribute() {}, removeAttribute() {},
    replaceChildren(...n) { this.nodes = n; },
  };
  globalThis.document = {
    getElementById: (id) => (id === "ft-menu" ? menu : null),
    createElement: (tag) => fakeNode(tag),
  };
  globalThis.window = { innerWidth: 390, innerHeight: 900, HOME_DIR: "/home/u" };
  globalThis.mobileActive = (w) => Number(w) <= 700;
  Object.assign(globalThis, require("../static/copyfile.js"), require("../static/fsops.js"), { insertOffered: require("../static/insertdecision.js").insertOffered });
  return menu;
}
const anchor = { getBoundingClientRect: () => ({ left: 0, right: 100, top: 10, bottom: 30 }) };
const menuText = (menu) => menu.nodes.map((n) => (n.tag === "hr" ? "---" : n.textContent));

test("openRowMenu: a read-only file menu is the path header and the safe actions", () => {
  const menu = installMenuDom();
  FT.openRowMenu(anchor, "/tmp/claude-1/x/readme.txt", "readme.txt", false, true);
  assert.deepStrictEqual(menuText(menu), ["/tmp/claude-1/x/readme.txt", "Copy path", "Copy relative path", "Copy contents", "---", "Download"]);
});

test("openRowMenu: a read-only folder menu has no New folder here", () => {
  const menu = installMenuDom();
  FT.openRowMenu(anchor, "/tmp/claude-1/x/dir1", "dir1", true, true);
  assert.deepStrictEqual(menuText(menu), ["/tmp/claude-1/x/dir1", "Copy path", "Copy relative path", "---", "Download .tar.gz"]);
});

test("openRowMenu: no flag (or false) is the full phone menu, image rule included, with no Insert path at prompt (the Files screen hides every terminal)", () => {
  const menu = installMenuDom();
  FT.openRowMenu(anchor, "/home/u/w/a.txt", "a.txt", false);
  assert.deepStrictEqual(menuText(menu), ["~/w/a.txt", "Copy path", "Copy relative path", "Copy contents", "---", "Download", "---", "Rename\u2026", "Delete\u2026"]);
  FT.openRowMenu(anchor, "/home/u/w", "w", true, false);
  assert.deepStrictEqual(menuText(menu), ["~/w", "Copy path", "Copy relative path", "---", "Download .tar.gz", "New folder here\u2026", "---", "Rename\u2026", "Delete\u2026"]);
  FT.openRowMenu(anchor, "/home/u/w/p.PNG", "p.PNG", false, false);
  assert.ok(!menuText(menu).includes("Copy contents"), "an image has no text to copy");
  assert.ok(menuText(menu).includes("Rename\u2026"));
});

// --- ftApplyReadOnly / ftRenderDir ----------------------------------------------

test("ftApplyReadOnly hides #ft-newdir and #ft-upload, and only those", () => {
  const els = { "ft-newdir": { hidden: false }, "ft-upload": { hidden: false }, "ft-refresh": { hidden: false } };
  globalThis.document = { getElementById: (id) => els[id] || null };
  FT.ftApplyReadOnly(true);
  assert.deepStrictEqual([els["ft-newdir"].hidden, els["ft-upload"].hidden, els["ft-refresh"].hidden], [true, true, false]);
  FT.ftApplyReadOnly(false);
  assert.deepStrictEqual([els["ft-newdir"].hidden, els["ft-upload"].hidden, els["ft-refresh"].hidden], [false, false, false]);
  els["ft-upload"] = null; // a layout without the button must not throw
  FT.ftApplyReadOnly(true);
});

test("ftApplyReadOnly hides the dock's New file with the other write buttons", () => {
  const els = { "dock-newfile": { hidden: false }, "dock-more": { hidden: false } };
  globalThis.document = { getElementById: (id) => els[id] || null };
  FT.ftApplyReadOnly(true);
  assert.deepStrictEqual([els["dock-newfile"].hidden, els["dock-more"].hidden], [true, false], "the ⋯ stays: Refresh and Collapse all still read");
  FT.ftApplyReadOnly(false);
  assert.strictEqual(els["dock-newfile"].hidden, false);
});

// --- promptNewFile (the dock's New file) ------------------------------------------------------------------------
// The globals it reaches in the page (askPrompt, fsNewFile, showToast, drawerShowFile, ...) are the test's; its own
// ftLoadRoot returns at once here (no tree is built under node). The order prompt, create, refresh, open is
// dock-changes-wire.test.js's source pin.
async function newFile(o) {
  const log = { prompts: [], creates: [], toasts: [], shown: [], opened: [], renames: [], deletes: [], confirms: [], closed: [] };
  const was = {};
  const set = (k, v) => { was[k] = Object.prototype.hasOwnProperty.call(globalThis, k) ? globalThis[k] : undefined; globalThis[k] = v; };
  set("askPrompt", async (opts) => { log.prompts.push(opts); return Array.isArray(o.name) ? o.name[Math.min(log.prompts.length, o.name.length) - 1] : o.name; });
  set("fsNewFile", async (dir, name) => { log.creates.push([dir, name]); if (o.throws) throw new TypeError("Failed to fetch"); return o.reply; });
  set("fsMkdir", async (dir, name) => { log.creates.push([dir, name]); if (o.throws) throw new TypeError("Failed to fetch"); return o.reply; });
  // Rename and delete: each call is logged and answered from o.replies in turn (the last one repeats); o.throwAt is the
  // 1-based call that never completes.
  const answer = (log0, args) => {
    log0.push(args);
    if (o.throwAt === log0.length) throw new TypeError("Failed to fetch");
    const rs = o.replies || [];
    return rs[Math.min(log0.length, rs.length) - 1];
  };
  set("fsRename", async (...args) => answer(log.renames, args));
  set("fsDelete", async (...args) => answer(log.deletes, args));
  set("askConfirm", async (opts) => { log.confirms.push(opts); return o.confirm !== false; });
  set("closeViewersFor", (p) => { log.closed.push(p); });
  set("forgetSubtree", () => {}); // treestate.js: the expand state the real page keeps
  set("saveExpanded", () => {});
  set("fsErrText", (res) => (res && res.data && res.data.error) || "HTTP " + res.status);
  set("showToast", (m, opts) => { log.toasts.push([m, !!(opts && opts.error)]); });
  set("drawerOwnsFileClicks", () => o.docked !== false);
  set("drawerShowFile", (p, opts) => { log.shown.push([p, opts]); return true; });
  set("openFile", (p) => { log.opened.push(p); });
  try { await FT[o.fn || "promptNewFile"](...(o.args || [o.dir === undefined ? "/w" : o.dir])); } finally {
    for (const k of Object.keys(was)) { if (was[k] === undefined) delete globalThis[k]; else globalThis[k] = was[k]; }
  }
  return log;
}

test("promptNewFile: a name creates the file in the folder and opens it as a pinned tab of the dock", async () => {
  const log = await newFile({ name: "notes.md", reply: { ok: true, status: 200, data: { path: "/w/notes.md" } } });
  assert.deepStrictEqual(log.creates, [["/w", "notes.md"]]);
  assert.strictEqual(log.prompts[0].title, "New file");
  assert.deepStrictEqual(log.shown, [["/w/notes.md", { preview: false }]]);
  assert.deepStrictEqual(log.opened, []);
  assert.deepStrictEqual(log.toasts, [["Created notes.md", false]]);
});

test("promptNewFile: a name that exists is the 'name already exists' toast and nothing opens", async () => {
  const log = await newFile({ name: "a.go", reply: { ok: false, status: 409, data: { error: "name already exists" } } });
  assert.deepStrictEqual(log.toasts, [["Could not create: name already exists", true]]);
  assert.deepStrictEqual(log.shown, []);
  const bare = await newFile({ name: "a.go", reply: { ok: false, status: 409, data: null } });
  assert.deepStrictEqual(bare.toasts, [["Could not create: name already exists", true]], "also when the body is not JSON");
});

test("promptNewFile: any other refusal says why, and a dismissed or empty prompt asks nothing", async () => {
  const bad = await newFile({ name: "x/y", reply: { ok: false, status: 400, data: { error: "bad name" } } });
  assert.deepStrictEqual(bad.toasts, [["Could not create: bad name", true]]);
  assert.deepStrictEqual(bad.shown, []);
  for (const name of [null, ""]) {
    const log = await newFile({ name, reply: { ok: true, status: 200, data: { path: "/w/z" } } });
    assert.deepStrictEqual([log.creates, log.toasts, log.shown], [[], [], []], JSON.stringify(name));
  }
  const none = await newFile({ name: "n", dir: "", reply: { ok: true, status: 200, data: { path: "/w/n" } } });
  assert.deepStrictEqual([none.prompts.length, none.creates], [0, []], "no folder, no prompt");
});

test("promptNewFile and promptMkdir: a request that never completes is the error toast, not an unhandled rejection", async () => {
  for (const fn of ["promptNewFile", "promptMkdir"]) {
    const log = await newFile({ fn, name: "x", throws: true });
    assert.deepStrictEqual(log.creates, [["/w", "x"]], fn);
    assert.deepStrictEqual(log.toasts, [["Could not create: network error", true]], fn);
    assert.deepStrictEqual(log.shown, [], fn + " opens nothing");
  }
});

// promptRename and confirmDelete reach the server twice when the folder is protected, so each request is guarded, not only the first.
const NET = (verb) => [["Could not " + verb + ": network error", true]];
const PROTECTED = { ok: false, status: 409, data: { confirm_name: "keep" } };
test("promptRename: a request that never completes is the error toast, on the first request and on the protected-folder retry", async () => {
  const first = await newFile({ fn: "promptRename", args: ["/w/a.txt", "a.txt"], name: "b.txt", throwAt: 1 });
  assert.deepStrictEqual(first.renames, [["/w/a.txt", "b.txt"]]);
  assert.deepStrictEqual(first.toasts, NET("rename"));
  assert.deepStrictEqual(first.closed, [], "nothing is torn down for a rename that did not happen");
  const retry = await newFile({ fn: "promptRename", args: ["/w/keep", "keep"], name: ["keep2", "keep"], replies: [PROTECTED], throwAt: 2 });
  assert.deepStrictEqual(retry.renames, [["/w/keep", "keep2"], ["/w/keep", "keep2", "keep"]]);
  assert.deepStrictEqual(retry.toasts, NET("rename"));
  assert.deepStrictEqual(retry.closed, []);
});

test("promptRename: a rename that works still says so, and a refusal says why", async () => {
  const ok = await newFile({ fn: "promptRename", args: ["/w/a.txt", "a.txt"], name: "b.txt", replies: [{ ok: true, status: 200, data: {} }] });
  assert.deepStrictEqual(ok.toasts, [["Renamed to b.txt", false]]);
  assert.deepStrictEqual(ok.closed, ["/w/a.txt"]);
  const bad = await newFile({ fn: "promptRename", args: ["/w/a.txt", "a.txt"], name: "b.txt", replies: [{ ok: false, status: 400, data: { error: "bad name" } }] });
  assert.deepStrictEqual(bad.toasts, [["Could not rename: bad name", true]]);
});

test("confirmDelete: a request that never completes is the error toast, on the first request and on each confirmed retry", async () => {
  const first = await newFile({ fn: "confirmDelete", args: ["/w/a.txt", "a.txt", false], throwAt: 1 });
  assert.deepStrictEqual(first.deletes, [["/w/a.txt"]]);
  assert.deepStrictEqual(first.toasts, NET("delete"));
  assert.deepStrictEqual(first.closed, [], "nothing is torn down for a delete that did not happen");
  const ask = await newFile({ fn: "confirmDelete", args: ["/w/d", "d", true], replies: [{ ok: false, status: 409, data: { entries: 3 } }], throwAt: 2 });
  assert.deepStrictEqual(ask.deletes.length, 2);
  assert.strictEqual(ask.confirms.length, 1);
  assert.deepStrictEqual(ask.toasts, NET("delete"));
  const prot = await newFile({ fn: "confirmDelete", args: ["/w/keep", "keep", true], name: "keep", replies: [PROTECTED], throwAt: 2 });
  assert.deepStrictEqual(prot.deletes.length, 2);
  assert.deepStrictEqual(prot.toasts, NET("delete"));
  assert.deepStrictEqual(prot.closed, []);
});

test("confirmDelete: a delete that works still says so, and a refusal says why", async () => {
  const ok = await newFile({ fn: "confirmDelete", args: ["/w/a.txt", "a.txt", false], replies: [{ ok: true, status: 200, data: {} }] });
  assert.deepStrictEqual(ok.toasts, [["Deleted a.txt", false]]);
  assert.deepStrictEqual(ok.closed, ["/w/a.txt"]);
  const bad = await newFile({ fn: "confirmDelete", args: ["/w/a.txt", "a.txt", false], replies: [{ ok: false, status: 403, data: { error: "protected" } }] });
  assert.deepStrictEqual(bad.toasts, [["Could not delete: protected", true]]);
});

test("promptMkdir: a name creates the folder and says so; a refusal says why", async () => {
  const ok = await newFile({ fn: "promptMkdir", name: "sub", reply: { ok: true, status: 200, data: {} } });
  assert.deepStrictEqual(ok.creates, [["/w", "sub"]]);
  assert.deepStrictEqual(ok.toasts, [["Created sub", false]]);
  const bad = await newFile({ fn: "promptMkdir", name: "sub", reply: { ok: false, status: 409, data: { error: "name already exists" } } });
  assert.deepStrictEqual(bad.toasts, [["Could not create: name already exists", true]]);
});

test("promptNewFile: without the dock the file opens where a tree row's click would", async () => {
  const log = await newFile({ name: "n.txt", docked: false, reply: { ok: true, status: 200, data: { path: "/w/n.txt" } } });
  assert.deepStrictEqual(log.shown, []);
  assert.deepStrictEqual(log.opened, ["/w/n.txt"]);
});

function fakeRow() {
  const el = {
    tag: "div", className: "", textContent: "", title: "", children: [], attrs: {}, style: { setProperty() {} },
    classList: { add() {}, remove() {}, contains(c) { return el.className.split(/\s+/).includes(c); } },
    append(...n) { this.children.push(...n); }, after() {}, remove() {},
    setAttribute(k, v) { this.attrs[k] = v; }, querySelector: () => null,
    getBoundingClientRect: () => ({ left: 0, right: 100, top: 10, bottom: 30 }),
  };
  return el;
}

// The globals ftRenderDir reads, with `data` as every listing; the zones array collects each folder row's drop zone.
function renderSetup(data, zones) {
  const menu = installMenuDom();
  globalThis.document.createElement = () => fakeRow();
  Object.assign(globalThis, require("../static/treestate.js")); // shouldAutoOpen: nothing is remembered open
  globalThis.pendingRegion = () => () => {};
  globalThis.api = async () => data;
  globalThis.makeDropZone = (el, destFn, opts) => { if (zones) zones.push({ el, dest: destFn(), opts }); };
  return menu;
}

async function renderListing(data) {
  const zones = [];
  const container = fakeRow();
  const menu = renderSetup(data, zones);
  const ro = await FT.ftRenderDir("/tmp/claude-1/x", container, "");
  // Press each row's \u22ef and keep what its menu printed.
  const menus = {};
  for (const row of container.children) {
    const more = row.children.find((c) => /\bft-more\b/.test(c.className));
    const name = row.children.find((c) => c.className === "ft-name").textContent;
    more.onclick({ stopPropagation() {} });
    menus[name] = menuText(menu);
  }
  return { ro, zones, menus };
}

test("ftRenderDir returns the listing's read-only flag and gives every folder row's drop zone the same answer", async () => {
  const entries = [{ name: "dir1", is_dir: true, size: 0 }, { name: "readme.txt", is_dir: false, size: 3 }];
  const ro = await renderListing({ path: "/tmp/claude-1/x", entries, readonly: true });
  assert.strictEqual(ro.ro, true);
  assert.strictEqual(ro.zones.length, 1, "one directory row, one drop zone");
  assert.strictEqual(ro.zones[0].dest, "/tmp/claude-1/x/dir1");
  assert.strictEqual(typeof ro.zones[0].opts.readOnly, "function");
  assert.strictEqual(ro.zones[0].opts.readOnly(), true);

  const rw = await renderListing({ path: "/h/w", entries });
  assert.strictEqual(rw.ro, false);
  assert.strictEqual(rw.zones[0].opts.readOnly(), false);
});

test("ftRenderDir: every row's menu follows its own listing's flag", async () => {
  const entries = [{ name: "dir1", is_dir: true, size: 0 }, { name: "readme.txt", is_dir: false, size: 3 }];
  const ro = await renderListing({ path: "/tmp/claude-1/x", entries, readonly: true });
  for (const name of ["dir1", "readme.txt"]) {
    const m = ro.menus[name];
    assert.ok(m.includes("Download") || m.includes("Download .tar.gz"), name);
    for (const gone of ["New folder here\u2026", "Rename\u2026", "Delete\u2026"]) assert.ok(!m.includes(gone), name + " must not offer " + gone);
  }
  const rw = await renderListing({ path: "/h/w", entries });
  assert.ok(rw.menus["dir1"].includes("New folder here\u2026") && rw.menus["dir1"].includes("Rename\u2026") && rw.menus["dir1"].includes("Delete\u2026"));
  assert.ok(rw.menus["readme.txt"].includes("Rename\u2026") && rw.menus["readme.txt"].includes("Delete\u2026"));
});

// The desktop dock draws depth with one custom property per row (desktop.css: indent and guides read --lvl), and a
// level is two characters of prefix, the same arithmetic aria-level already uses. The pending line of a folder in
// flight sits where the children will, so its box carries the same number.
test("ftRenderDir sets --lvl per depth", async () => {
  const entries = [{ name: "dir1", is_dir: true, size: 0 }, { name: "a.txt", is_dir: false, size: 3 }];
  const vars = (el) => el.vars;
  const recording = () => {
    const el = fakeRow();
    el.vars = {};
    el.style = { setProperty(k, v) { el.vars[k] = String(v); } };
    return el;
  };
  renderSetup({ path: "/h/w", entries });
  globalThis.document.createElement = () => recording();
  for (const [prefix, lvl] of [["", "0"], ["  ", "1"], ["    ", "2"]]) {
    const box = recording();
    await FT.ftRenderDir("/h/w", box, prefix);
    assert.strictEqual(vars(box)["--lvl"], lvl, "the box the rows go in, prefix " + JSON.stringify(prefix));
    assert.deepStrictEqual(box.children.map((r) => vars(r)["--lvl"]), [lvl, lvl], "every row of the listing, prefix " + JSON.stringify(prefix));
  }
});

test("ftRenderDir: a failed listing is not read-only (the buttons stay; the server still refuses)", async () => {
  globalThis.document = { createElement: () => fakeRow() };
  globalThis.pendingRegion = () => () => {};
  globalThis.api = async () => { throw new Error("403"); };
  assert.strictEqual(await FT.ftRenderDir("/x", fakeRow(), ""), false);
});

// ftLoadRoot's DOM half is verified in the browser; pin the one ordering that
// matters here the way pending-sites.test.js does: a superseded load must not
// touch the toolbar.
test("source pin: only the newest root load sets the toolbar", () => {
  const fs = require("node:fs");
  const src = fs.readFileSync(require("node:path").join(__dirname, "..", "static", "filetree.js"), "utf8");
  const load = src.slice(src.indexOf("async function ftLoadRoot"));
  const gate = load.indexOf("if (gen !== ftGen) return;");
  assert.ok(gate > 0);
  assert.ok(load.indexOf("ftApplyReadOnly(", gate) > gate, "the toolbar is set after the staleness check");
  assert.ok(load.slice(0, gate).indexOf("ftApplyReadOnly(") < 0, "not before it");
});
