"use strict";
// The kit menu module (#173 task 1): the builders every desktop menu shares (kitmenu.js), and the pane
// header's tab switcher, which is the kit menu on desktop and keeps its legacy rows on the phone.
// kitmenu.js is a classic script that cannot be required, so it runs whole against a stand-in document;
// openTabSwitcher lives in app.js and is cut out of the source by name, like session-menu.test.js does.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");
function fnSrc(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

const SVG_NS = "http://www.w3.org/2000/svg";
// A stand-in element: just what the builders touch. `focus` records which one took focus.
function el(tag, ns) {
  return {
    tag, namespaceURI: ns || "", type: "", className: "", textContent: "", title: "", onclick: null, children: [], attrs: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    append(...n) { this.children.push(...n); },
    replaceChildren(...n) { this.children = n; },
    focus() { focused.push(this); },
  };
}
let focused = [];

function world(extra) {
  const ctx = vm.createContext(Object.assign({
    document: { createElement: (t) => el(t), createElementNS: (ns, t) => el(t, ns), activeElement: null },
    // The product's own glyph builders live in tabs.js; here they draw one shape each, tagged by kind.
    tabIcon: (k) => { const s = el("svg", SVG_NS); s.attrs.kind = "tab:" + k; s.append(el("path", SVG_NS)); return s; },
    dockIcon: (k) => { const s = el("svg", SVG_NS); s.attrs.kind = "dock:" + k; s.append(el("path", SVG_NS)); return s; },
  }, extra || {}));
  const run = (src) => new vm.Script(src).runInContext(ctx);
  run(read("icons.js"));
  run(read("iconmap.js"));
  run(read("svgicon.js"));
  run(read("kitmenu.js"));
  return { ctx, run };
}

// ---- menuNavIndex (moved from sessionrow.js; the cases are session-menu.test.js's, unchanged) -----------------------------
test("Arrow Up and Down wrap, Home and End jump, and nothing else is handled", () => {
  const { ctx } = world();
  const menuNavIndex = ctx.menuNavIndex;
  assert.strictEqual(menuNavIndex(5, -1, "ArrowDown"), 0, "from outside the items, Down lands on the first");
  assert.strictEqual(menuNavIndex(5, -1, "ArrowUp"), 4, "and Up on the last");
  assert.strictEqual(menuNavIndex(5, 4, "ArrowDown"), 0, "Down wraps");
  assert.strictEqual(menuNavIndex(5, 0, "ArrowUp"), 4, "Up wraps");
  assert.strictEqual(menuNavIndex(5, 2, "ArrowDown"), 3);
  assert.strictEqual(menuNavIndex(5, 2, "ArrowUp"), 1);
  assert.strictEqual(menuNavIndex(5, 2, "Home"), 0);
  assert.strictEqual(menuNavIndex(5, 2, "End"), 4);
  assert.strictEqual(menuNavIndex(5, -1, "End"), 4);
  for (const k of ["Tab", "Enter", "a", "PageDown", "Escape"]) assert.strictEqual(menuNavIndex(5, 2, k), null, k);
  assert.strictEqual(menuNavIndex(0, -1, "ArrowDown"), null, "an empty menu has nowhere to go");
});

test("menuNavIndex is also what node can require from kitmenu.js", () => {
  const m = require("../static/kitmenu.js");
  assert.strictEqual(typeof m.menuNavIndex, "function");
  assert.strictEqual(m.menuNavIndex(3, 2, "ArrowDown"), 0);
  assert.strictEqual(typeof require("../static/sessionrow.js").menuNavIndex, "undefined", "sessionrow.js no longer carries it");
});

// ---- kitMenuKeys --------------------------------------------------------------------------------------------------------
// A menu whose querySelectorAll understands the selectors kitMenuKeys uses: comma lists of `.class` with an optional
// :not([aria-disabled="true"]). Anything else is a selector the real page may read differently, so it fails loudly.
function keyMenu(rows) {
  const asked = [];
  return {
    asked,
    querySelectorAll(sel) {
      asked.push(sel);
      const wanted = sel.split(",").map((p) => p.trim());
      for (const p of wanted) assert.match(p, /^\.[\w-]+(:not\(\[aria-disabled="true"\]\))?$/, "unexpected selector part " + p);
      return rows.filter((r) => wanted.some((p) => {
        const cls = /^\.([\w-]+)/.exec(p)[1];
        if (!r.className.split(" ").includes(cls)) return false;
        return !(p.includes(':not([aria-disabled="true"])') && r.attrs["aria-disabled"] === "true");
      }));
    },
  };
}
const row = (cls, disabled) => { const r = el("button"); r.className = cls; if (disabled) r.setAttribute("aria-disabled", "true"); return r; };
const press = (key) => { const e = { key, prevented: 0, preventDefault() { this.prevented++; } }; return e; };

test("kitMenuKeys: Down and Up step over .menu-item rows, wrap, and skip an aria-disabled one", () => {
  const { ctx } = world();
  const a = row("menu-item"), b = row("menu-item", true), c = row("menu-item"), sep = row("menu-sep");
  const menu = keyMenu([a, sep, b, c]);
  focused = [];
  ctx.document.activeElement = a;
  const down = press("ArrowDown");
  assert.strictEqual(ctx.kitMenuKeys(menu, down), true);
  assert.deepStrictEqual(focused, [c], "the disabled row is stepped over");
  assert.strictEqual(down.prevented, 1);
  ctx.document.activeElement = c;
  assert.strictEqual(ctx.kitMenuKeys(menu, press("ArrowDown")), true);
  assert.strictEqual(focused[1], a, "Down wraps from the last row to the first");
  ctx.document.activeElement = a;
  assert.strictEqual(ctx.kitMenuKeys(menu, press("ArrowUp")), true);
  assert.strictEqual(focused[2], c, "Up wraps from the first row to the last");
});

test("kitMenuKeys: Home and End jump to the first and last enabled row; focus off the rows lands on an end", () => {
  const { ctx } = world();
  const a = row("menu-item", true), b = row("menu-item"), c = row("menu-item"), d = row("menu-item", true);
  const menu = keyMenu([a, b, c, d]);
  focused = [];
  ctx.document.activeElement = c;
  assert.strictEqual(ctx.kitMenuKeys(menu, press("Home")), true);
  assert.strictEqual(ctx.kitMenuKeys(menu, press("End")), true);
  assert.deepStrictEqual(focused, [b, c], "Home is the first enabled row, End the last");
  const close = el("button"); // a tab switcher's close button is no row
  ctx.document.activeElement = close;
  ctx.kitMenuKeys(menu, press("ArrowDown"));
  ctx.kitMenuKeys(menu, press("ArrowUp"));
  assert.deepStrictEqual(focused.slice(2), [b, c], "from outside the rows Down lands on the first and Up on the last");
});

test("kitMenuKeys: any other key, or a menu with no enabled row, is left alone", () => {
  const { ctx } = world();
  const a = row("menu-item");
  focused = [];
  ctx.document.activeElement = a;
  for (const k of ["Tab", "Enter", " ", "Escape", "a", "ArrowLeft"]) {
    const e = press(k);
    assert.strictEqual(ctx.kitMenuKeys(keyMenu([a]), e), false, k);
    assert.strictEqual(e.prevented, 0, k + " keeps its default");
  }
  const none = press("ArrowDown");
  assert.strictEqual(ctx.kitMenuKeys(keyMenu([row("menu-item", true)]), none), false);
  assert.strictEqual(none.prevented, 0);
  assert.deepStrictEqual(focused, []);
});

test("kitMenuKeys steps the phone's legacy .mi rows the same way (the phone keeps its flat list)", () => {
  const { ctx } = world();
  const a = row("mi dgr"), b = row("mi");
  const menu = keyMenu([a, b]);
  focused = [];
  ctx.document.activeElement = a;
  assert.strictEqual(ctx.kitMenuKeys(menu, press("ArrowDown")), true);
  assert.deepStrictEqual(focused, [b]);
});

test("the document keydown handler hands the arrow keys to kitMenuKeys and still owns Escape", () => {
  const app = read("app.js");
  const at = app.indexOf('document.addEventListener("keydown", (e) => {\n  const menu = $("session-menu");');
  assert.ok(at >= 0, "the session menu's keydown handler");
  const handler = app.slice(at, app.indexOf("\n});", at) + 4);
  assert.match(handler, /kitMenuKeys\(menu, e\)/);
  assert.match(handler, /Escape/);
  assert.ok(!/menuNavIndex\(/.test(handler), "the stepping itself lives in kitmenu.js");
});

// ---- kitMenuSep, kitMenuButton ------------------------------------------------------------------------------------------
test("kitMenuSep is a .menu-sep separator div", () => {
  const { ctx } = world();
  const sep = ctx.kitMenuSep();
  assert.strictEqual(sep.tag, "div");
  assert.strictEqual(sep.className, "menu-sep");
  assert.strictEqual(sep.getAttribute("role"), "separator");
  assert.notStrictEqual(ctx.kitMenuSep(), sep, "a new node each time");
});

test("kitMenuButton: .menu-item > .menu-icon + .menu-text, an empty slot with no icon, red when danger, the click is its run", () => {
  const { ctx } = world();
  const ran = [];
  const b = ctx.kitMenuButton("Rename…", "rename", false, () => ran.push("r"));
  assert.strictEqual(b.tag, "button");
  assert.strictEqual(b.type, "button");
  assert.strictEqual(b.className, "menu-item");
  assert.strictEqual(b.getAttribute("role"), "menuitem");
  assert.deepStrictEqual(b.children.map((c) => c.className), ["menu-icon", "menu-text"]);
  assert.strictEqual(b.children[0].getAttribute("aria-hidden"), "true");
  assert.strictEqual(b.children[0].children[0].getAttribute("data-icon"), "pencil-line");
  assert.strictEqual(b.children[1].textContent, "Rename…");
  b.onclick();
  assert.deepStrictEqual(ran, ["r"]);
  const bare = ctx.kitMenuButton("Restart", "", true, () => {});
  assert.strictEqual(bare.className, "menu-item is-danger");
  assert.strictEqual(bare.children[0].children.length, 0, "no icon: the 16px slot stays an empty box");
});

// ---- menuIconSvg: every kind is an ICON_MENU glyph of the vendored set ----------------------------------------------------
test("menu kinds draw ICON_MENU glyphs", () => {
  const { ctx } = world();
  for (const [kind, name] of [["rename", "pencil-line"], ["copy", "clipboard-copy"], ["duplicate", "copy-plus"], ["end", "power"],
    ["delete", "trash-2"], ["check", "check"], ["refresh", "refresh-cw"], ["collapse", "chevrons-down-up"], ["insert", "arrow-right-to-line"],
    ["interrupt", "pause"], ["bringhome", "arrow-down-to-line"], ["folder-plus", "folder-plus"], ["files", "folder-tree"],
    ["attribution", "chart-pie"], ["split", "columns-2"], ["download", "download"], ["upload", "upload"], ["edit", "pencil"]]) {
    const svg = ctx.menuIconSvg(kind);
    assert.strictEqual(svg.getAttribute("data-icon"), name, kind);
    assert.strictEqual(svg.getAttribute("width"), "14", kind);
    assert.strictEqual(svg.getAttribute("viewBox"), "0 0 24 24", kind);
    assert.strictEqual(svg.getAttribute("aria-hidden"), "true", kind);
    assert.ok(svg.children.length >= 1, kind + " has a shape");
  }
  assert.strictEqual(ctx.menuIconSvg(""), null);
  assert.strictEqual(ctx.menuIconSvg("constructor").children.length, 0, "a kind named like an Object member is a bare icon, not a crash");
  assert.strictEqual(ctx.menuIconSvg("nope").children.length, 0, "an unknown kind is a bare icon");
});

test("menu kinds each have their own glyph, and kitmenu.js carries no shapes of its own", () => {
  const { ctx } = world();
  const kinds = ["rename", "copy", "duplicate", "end", "delete", "check", "refresh", "collapse", "insert", "interrupt", "bringhome",
    "folder-plus", "files", "attribution", "split", "download", "upload", "edit"];
  const names = kinds.map((k) => ctx.menuIconSvg(k).getAttribute("data-icon"));
  assert.strictEqual(new Set(names).size, kinds.length, "no two kinds share a glyph");
  const src = read("kitmenu.js");
  assert.ok(!/MENU_SHAPES|svgIcon\(|svgShape\(/.test(src));
});

// A kind that is no ICON_MENU key draws an empty slot, so every kind a caller passes is checked against the map here.
test("every menu icon kind a page script passes is an ICON_MENU key", () => {
  const { ICON_MENU } = require("../static/iconmap.js");
  const found = new Map();
  const note = (kind, where) => { if (kind) found.set(where + ":" + kind, kind); };
  for (const f of ["app.js", "dockchanges.js", "dockreveal.js", "editornotes.js", "editorselbar.js", "editortab.js", "profilepage.js", "sidechrome.js", "credentials.js"]) {
    const src = read(f);
    for (const m of src.matchAll(/kitMenuButton\([^,]+,\s*"([^"]*)"/g)) note(m[1], f);
    for (const m of src.matchAll(/menuIconSvg\("([^"]+)"\)/g)) note(m[1], f);
    for (const m of src.matchAll(/\bitem\("[^"]*",\s*"([^"]*)"/g)) note(m[1], f);
    for (const m of src.matchAll(/esbButton\((?:true|false),\s*[^,]+,\s*"([^"]*)"/g)) note(m[1], f);
    for (const m of src.matchAll(/selBarAddAction\(\{[^}]*icon: "([^"]*)"/g)) note(m[1], f);
  }
  for (const [f, name] of [["sessionrow.js", "MENU_ICON"], ["filetree.js", "ROW_MENU_ICONS"]]) {
    const src = read(f);
    const body = new RegExp("const " + name + " = \\{([^}]*)\\}").exec(src);
    assert.ok(body, name);
    for (const m of body[1].matchAll(/:\s*"([^"]+)"/g)) note(m[1], f);
  }
  assert.ok(found.size >= 15, "the scan found the callers (" + found.size + ")");
  for (const [where, kind] of found) assert.ok(Object.prototype.hasOwnProperty.call(ICON_MENU, kind), where + " is not an ICON_MENU key");
});

// ---- the tab switcher ---------------------------------------------------------------------------------------------------
function switcher(width, tabs, activeIdx, sessions, profiles) {
  const menu = el("div");
  const calls = { placed: [], closed: [], focusedCell: [], activated: [], closedTab: [] };
  const cell = {
    tabs, activeTab: tabs[activeIdx == null ? 0 : activeIdx],
    activate: (t) => calls.activated.push(t.id), closeTab: (t) => calls.closedTab.push(t.id),
  };
  const classes = new Set();
  menu.classList = { contains: (c) => classes.has(c) };
  const { ctx, run } = world({
    $: (id) => (id === "session-menu" ? menu : null),
    window: { innerWidth: width },
    mobileActive: (w) => Number(w) <= 700,
    closeSessionMenu: (r) => calls.closed.push(r),
    setFocusedCell: (c) => calls.focusedCell.push(c === cell),
    placeSessionMenu: (m, a) => calls.placed.push(a),
    sessionMenuAnchor: null, sessionMenuSid: "x",
    // The row's facts are deskshell.js's own (tested there); the Files tree's glyph is a tagged stand-in, and the
    // Claude mark, the terminal and the tier chip's glyph are uiIcon's own (the real icons.js and iconmap.js).
    lastSessions: sessions || [], profiles: profiles || [],
    tabSwitchFacts: require("../static/deskshell.js").tabSwitchFacts,
    iconFor: (name, isDir, open) => "G:" + name + ":" + isDir + ":" + open,
  });
  const app = read("app.js");
  run(fnSrc(app, "deskChipIcon") + "\n" + fnSrc(app, "kitTabRow") + "\n" + fnSrc(app, "openTabSwitcher") + "\nglobalThis.openTabSwitcher = openTabSwitcher;");
  return { menu, calls, ctx, cell, classes, open: (anchor) => ctx.openTabSwitcher(anchor || {}, cell) };
}
const term = { id: "t1", kind: "terminal", title: () => "agent-box" };
const view = { id: "v1", kind: "viewer", path: "/src/main.go", title: () => "main.go" };
const files = { id: "v2", kind: "viewer", path: "", title: () => "notes" };

test("desktop: one .menu-row per tab, a kit .menu-item with its tab icon plus a .menu-x close, and no legacy .mi", () => {
  const t = switcher(1440, [term, view, files]);
  t.open();
  const rows = t.menu.children;
  assert.strictEqual(rows.length, 3);
  for (const r of rows) {
    assert.strictEqual(r.tag, "div");
    assert.strictEqual(r.className, "menu-row");
    const [item, x] = r.children;
    assert.ok(item.className.split(" ").includes("menu-item"));
    assert.strictEqual(item.getAttribute("role"), "menuitem");
    assert.strictEqual(x.className.split(" ")[0], "menu-x");
    assert.match(x.getAttribute("aria-label"), /^Close /);
  }
  const all = [];
  const walk = (n) => { all.push(n); (n.children || []).forEach(walk); };
  rows.forEach(walk);
  assert.ok(all.every((n) => !/(^| )(mi|mi-row|mi-x|cur)( |$)/.test(n.className || "")), "no legacy row classes on desktop");
  assert.strictEqual(t.calls.placed.length, 1);
  assert.strictEqual(t.ctx.sessionMenuSid, null, "not about one session: the list poll must not close it");
});

// A pane holding a Claude session, a shell, a file, a session of an agent that is neither, and one the list lost (#365).
const S_CLAUDE = { id: "c1", kind: "terminal", title: () => "bash" };
const S_SHELL = { id: "s1", kind: "terminal", title: () => "claude" };
const S_OTHER = { id: "o1", kind: "terminal", title: () => "codex" };
const S_GONE = { id: "g1", kind: "terminal", title: () => "gone" };
const S_CLONE = { id: "k1", kind: "terminal", title: () => "fix-auth" };
const SESSIONS = [
  { id: "c1", agent: "cc", claude: true, runtime: "box" },
  { id: "s1", agent: "sh", claude: false, runtime: "host" },
  { id: "o1", agent: "codex", claude: false, runtime: "box" },
  { id: "k1", agent: "cc", claude: true, runtime: "box", clone_of: "/home/u/repo" },
];
const PROFILES = [{ name: "cc", bin: "claude" }, { name: "sh", bin: "/bin/bash" }];
const iconOf = (svg) => svg.getAttribute("data-icon");

test("desktop: the .menu-icon slot shows the tab's kind: the Claude mark, the shell's terminal, the file's type, else the pane header's glyph", () => {
  const t = switcher(1440, [S_CLAUDE, S_SHELL, view, files, S_OTHER, S_GONE], 0, SESSIONS, PROFILES);
  t.open();
  const slot = (i) => t.menu.children[i].children[0].children[0];
  for (let i = 0; i < 6; i++) {
    assert.strictEqual(slot(i).className, "menu-icon");
    assert.strictEqual(slot(i).getAttribute("aria-hidden"), "true", "row " + i + ": the glyph is decoration");
    assert.strictEqual(slot(i).children.length, 1, "row " + i + ": one glyph");
  }
  assert.strictEqual(iconOf(slot(0).children[0]), "si:claude", "a Claude session titled bash is still Claude");
  const shell = slot(1).children[0];
  assert.strictEqual(shell.tag, "svg");
  assert.strictEqual(shell.getAttribute("viewBox"), "0 0 24 24");
  assert.strictEqual(iconOf(shell), "terminal", "the sidebar's shell glyph, for a shell titled claude");
  for (const i of [0, 1]) assert.strictEqual(slot(i).children[0].getAttribute("width"), "14", "the menu row's size");
  const file = slot(2).children[0];
  assert.strictEqual(file.className, "menu-ficon", "a file wears the Files tree's type glyph, in its font");
  assert.strictEqual(file.textContent, "G:main.go:false:false", "by the path's file name");
  assert.strictEqual(slot(3).children[0].textContent, "G:notes:false:false", "a file with no path goes by its title");
  assert.strictEqual(iconOf(slot(4).children[0]), "square-terminal", "neither Claude nor a shell: the pane header's terminal glyph");
  assert.strictEqual(iconOf(slot(5).children[0]), "square-terminal", "a session the list does not hold: the same");
  assert.strictEqual(t.menu.children[0].children[0].children[1].textContent, "bash", "the title is the .menu-text");
  assert.strictEqual(t.menu.children[2].children[0].children[1].textContent, "main.go");
});

test("desktop: a session's row names its tier (Host, Box, Box clone) with the sidebar's glyph; a file and an unknown session have none", () => {
  const t = switcher(1440, [S_CLAUDE, S_SHELL, S_CLONE, view, S_GONE], 3, SESSIONS, PROFILES);
  t.open();
  const item = (i) => t.menu.children[i].children[0];
  const chip = (i) => item(i).children.find((c) => (c.className || "").split(" ").includes("menu-tier"));
  const want = [["box", "Box"], ["host", "Host"], ["clone", "Box clone"]];
  want.forEach(([key, label], i) => {
    const c = chip(i);
    assert.ok(c, "row " + i + " has a tier chip");
    assert.deepStrictEqual(c.className.split(" ").sort(), ["dchip", key === "host" ? "host" : "box", "menu-tier"].sort(), "row " + i + ": the sidebar chip's tone");
    assert.strictEqual(c.getAttribute("data-tier"), key);
    const [ic, word] = c.children;
    assert.strictEqual(ic.tag, "svg");
    assert.strictEqual(ic.getAttribute("aria-hidden"), "true");
    assert.strictEqual(iconOf(ic), { box: "box", clone: "copy", host: "triangle-alert" }[key], "row " + i + ": the " + key + " glyph");
    assert.strictEqual(ic.getAttribute("width"), "11");
    assert.ok(ic.getAttribute("class").split(" ").includes("tchip-i"));
    assert.strictEqual(word.textContent, label, "row " + i + ": the word is text, so it is part of the row's name");
  });
  assert.deepStrictEqual(item(0).children.map((c) => c.className.split(" ")[0]), ["menu-icon", "menu-text", "menu-tier"], "the chip follows the title");
  assert.ok(!chip(3), "a file runs nowhere");
  assert.ok(!chip(4), "a session the list does not hold: no tier is guessed");
  assert.deepStrictEqual(item(3).children.map((c) => c.className), ["menu-icon", "menu-text", "menu-check"], "the shown file: the check, no chip");
  const cur = switcher(1440, [S_CLAUDE], 0, SESSIONS, PROFILES);
  cur.open();
  assert.deepStrictEqual(cur.menu.children[0].children[0].children.map((c) => c.className.split(" ")[0]), ["menu-icon", "menu-text", "menu-tier", "menu-check"], "the check stays last");
});

test("desktop: only the current tab wears aria-current and the check; the rest have neither", () => {
  const t = switcher(1440, [term, view, files], 1);
  t.open();
  const item = (i) => t.menu.children[i].children[0];
  assert.strictEqual(item(1).getAttribute("aria-current"), "true");
  assert.strictEqual(item(0).getAttribute("aria-current"), null);
  assert.strictEqual(item(2).getAttribute("aria-current"), null);
  const check = (i) => item(i).children.find((c) => c.className === "menu-check");
  assert.ok(check(1), "the current tab carries a check");
  assert.strictEqual(check(1).getAttribute("aria-hidden"), "true");
  assert.strictEqual(check(1).children[0].getAttribute("data-icon"), "check");
  assert.ok(!check(0) && !check(2));
  assert.deepStrictEqual(item(1).children.map((c) => c.className), ["menu-icon", "menu-text", "menu-check"], "the check is last, after the title");
});

test("desktop: a tab's row shows its path, or its title, on hover; Close says whether the session keeps running", () => {
  const t = switcher(1440, [term, view, files]);
  t.open();
  const title = (i) => t.menu.children[i].children[0].title;
  assert.strictEqual(title(0), "agent-box");
  assert.strictEqual(title(1), "/src/main.go", "a viewer tab shows its file's path");
  assert.strictEqual(title(2), "notes", "and its title when it has none");
  const x = (i) => t.menu.children[i].children[1];
  assert.strictEqual(x(0).title, "Close tab (the session keeps running)");
  assert.strictEqual(x(1).title, "Close tab");
  assert.strictEqual(x(0).getAttribute("aria-label"), "Close agent-box");
  assert.strictEqual(x(1).getAttribute("aria-label"), "Close main.go");
  assert.strictEqual(x(0).type, "button");
  assert.strictEqual(x(0).textContent, "×");
});

test("desktop: a row closes the menu, focuses the pane and activates its tab; a close closes the menu and that tab", () => {
  const t = switcher(1440, [term, view]);
  t.open();
  t.menu.children[1].children[0].onclick();
  assert.deepStrictEqual(t.calls.closed, [false]);
  assert.deepStrictEqual(t.calls.focusedCell, [true]);
  assert.deepStrictEqual(t.calls.activated, ["v1"]);
  t.menu.children[0].children[1].onclick();
  assert.deepStrictEqual(t.calls.closed, [false, false]);
  assert.deepStrictEqual(t.calls.closedTab, ["t1"]);
});

test("phone: the legacy .mi-row > .mi[.cur] + .mi-x rows, byte for byte as before", () => {
  const t = switcher(390, [term, view, files], 1);
  t.open();
  const rows = t.menu.children;
  assert.strictEqual(rows.length, 3);
  const shape = (r) => r.className + " > " + r.children.map((c) => c.tag + "." + c.className + "[" + c.getAttribute("role") + "] '" + c.textContent + "' title=" + JSON.stringify(c.title) + " aria=" + c.getAttribute("aria-label")).join(" + ");
  assert.deepStrictEqual(rows.map(shape), [
    "mi-row > button.mi[menuitem] '▸ agent-box' title=\"agent-box\" aria=null + button.mi-x[null] '×' title=\"Close tab (the session keeps running)\" aria=Close agent-box",
    "mi-row > button.mi cur[menuitem] 'main.go' title=\"/src/main.go\" aria=null + button.mi-x[null] '×' title=\"Close tab\" aria=Close main.go",
    "mi-row > button.mi[menuitem] 'notes' title=\"notes\" aria=null + button.mi-x[null] '×' title=\"Close tab\" aria=Close notes",
  ]);
  assert.ok(rows.every((r) => r.children.every((c) => c.children.length === 0)), "no SVG on the phone");
  assert.ok(!rows.some((r) => r.children[0].getAttribute("aria-current")), "the phone marks the current tab with .cur only");
  rows[1].children[0].onclick();
  rows[0].children[1].onclick();
  assert.deepStrictEqual(t.calls.activated, ["v1"]);
  assert.deepStrictEqual(t.calls.closedTab, ["t1"]);
  assert.strictEqual(t.calls.placed.length, 1);
});

test("a second press on the same anchor closes the open switcher instead of rebuilding it (both widths)", () => {
  for (const w of [1440, 390]) {
    const t = switcher(w, [term, view]);
    const anchor = {};
    t.ctx.sessionMenuAnchor = anchor;
    t.classes.add("open");
    t.open(anchor);
    assert.deepStrictEqual(t.calls.closed, [true], "width " + w);
    assert.strictEqual(t.menu.children.length, 0, "not rebuilt");
    assert.strictEqual(t.calls.placed.length, 0);
  }
});
