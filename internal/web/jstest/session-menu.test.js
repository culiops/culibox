"use strict";
// The reply opt-in rows are offered only while the kill-switch reads true (menuItems).
globalThis.repliesEnabled = true;
// The session row's ⋯ menu on desktop is the Plan 1 kit menu (F12, M5.1): .menu-item buttons with an
// icon slot, a .menu-sep only between groups, End session… red and last. The phone keeps today's
// .mi / .mi.dgr buttons. openSessionMenu and its builders live in app.js, a classic script that cannot
// be required, so they are cut out of the source by name and run against a stand-in DOM. The row icons
// are inline SVG (never a font glyph, which is tofu where no installed font has it). The shared builders
// (kitMenuButton, kitMenuSep, menuIconSvg) are kitmenu.js's and svgIcon is svgicon.js's: both run whole.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { menuItems, menuGroups, menuIcon, menuPlace, MENU_GAP, ROW_MENU_GAP } = require("../static/sessionrow.js");

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

// A stand-in element: just what the builders touch.
function el(tag, ns) {
  return {
    tag, namespaceURI: ns || "", type: "", className: "", textContent: "", title: "", onclick: null, children: [], attrs: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    append(...n) { this.children.push(...n); },
    replaceChildren(...n) { this.children = n; },
  };
}
const SVG_NS = "http://www.w3.org/2000/svg";
// shape: one compact, comparable line per node. An icon is "svg:<kind>" (the builder tags it data-icon).
function shape(n) {
  if (n.tag === "svg") return "svg:" + n.attrs["data-icon"];
  const a = n.attrs.role ? "[" + n.attrs.role + "]" : "";
  const kids = n.children.length ? " > (" + n.children.map(shape).join(", ") + ")" : "";
  return n.tag + "." + n.className.split(" ").join(".") + a + (n.textContent ? " '" + n.textContent + "'" : "") + kids;
}

const NAMES = ["kitMenuNodes", "openSessionMenu"];
function setup(width, nestedFn) {
  const menu = el("div");
  const calls = { placed: [], closed: [], ran: [] };
  const ctx = vm.createContext({
    $: (id) => (id === "session-menu" ? menu : null),
    document: { createElement: (t) => el(t), createElementNS: (ns, t) => el(t, ns) },
    window: { innerWidth: width },
    mobileActive: (w) => Number(w) <= 700,
    isNestedShell: nestedFn || (() => false),
    lastSessions: [],
    menuItems, menuGroups, menuIcon,
    closeSessionMenu: (r) => calls.closed.push(r),
    runSessionAction: (id, s) => calls.ran.push([id, s.id]),
    placeSessionMenu: (m, a) => calls.placed.push(a),
    sessionMenuSid: null,
  });
  for (const f of ["icons.js", "iconmap.js", "svgicon.js"]) new vm.Script(read(f)).runInContext(ctx);
  new vm.Script(read("kitmenu.js")).runInContext(ctx);
  const src = NAMES.map((n) => fnSrc(read("app.js"), n)).join("\n") + "\nglobalThis.openSessionMenu = openSessionMenu;";
  new vm.Script(src).runInContext(ctx);
  return { menu, calls, ctx, open: (anchor, s, extra) => ctx.openSessionMenu(anchor, s, extra) };
}
const live = { id: "s1", name: "agent-box", claude: true, cwd: "/x", status: "live", activity: "working", clone_of: "/r", runtime: "box" };
const btn = (icon, text, danger) => "button." + (danger ? "menu-item.is-danger" : "menu-item") + "[menuitem] > (span.menu-icon" + (icon ? " > (svg:" + icon + ")" : "") + ", span.menu-text '" + text + "')";
const SEP = "div.menu-sep[separator]";

test("desktop: the row menu is four groups of kit rows, a separator between groups only", () => {
  const t = setup(1440);
  t.open({}, live);
  assert.deepStrictEqual(t.menu.children.map(shape), [
    btn("pencil-line", "Rename…"), SEP,
    btn("folder-tree", "Open files here"), btn("chart-pie", "Context by tool"), btn("clipboard-copy", "Copy working directory"), btn("columns-2", "Open in new pane"), SEP,
    btn("pause", "Interrupt"), btn("message-square-reply", "Allow replies"), btn("arrow-down-to-line", "Bring work home now"), btn("copy-plus", "Duplicate…"), SEP,
    btn("power", "End session…", true),
  ]);
  const kids = t.menu.children;
  assert.ok(kids[0].tag === "button" && kids[kids.length - 1].tag === "button", "no separator first or last");
  assert.ok(!kids.some((n, i) => n.attrs.role === "separator" && kids[i + 1] && kids[i + 1].attrs.role === "separator"), "never two in a row");
  assert.strictEqual(kids.filter((n) => n.className.includes("is-danger")).length, 1, "only End session… is red");
  // Each icon is decoration; the row's name is its text.
  const icon = kids[0].children[0];
  assert.strictEqual(icon.attrs["aria-hidden"], "true");
  assert.strictEqual(kids[0].type, "button");
  assert.strictEqual(t.calls.placed.length, 1, "placed under the anchor once");
});

test("desktop: no row wears the legacy .mi class, so no divider is drawn under each item", () => {
  const t = setup(1440);
  t.open({}, live);
  assert.ok(t.menu.children.every((n) => !/\bmi\b|\bdgr\b/.test(n.className)));
});

test("desktop: every icon is a vendored glyph (uiIcon, 14px), never a font glyph", () => {
  const t = setup(1440);
  t.open({}, live);
  const slots = t.menu.children.filter((n) => n.tag === "button").map((b) => b.children[0]);
  const drawn = slots.filter((sl) => sl.children.length);
  assert.strictEqual(drawn.length, 10, "all ten rows draw an icon");
  for (const sl of slots) {
    assert.strictEqual(sl.textContent, "", "no glyph text in the icon slot");
    assert.strictEqual(sl.attrs["aria-hidden"], "true");
  }
  for (const sl of drawn) {
    assert.strictEqual(sl.children.length, 1);
    const svg = sl.children[0];
    const kind = svg.attrs["data-icon"];
    assert.strictEqual(svg.tag, "svg", kind);
    assert.strictEqual(svg.namespaceURI, SVG_NS, kind);
    assert.deepStrictEqual(
      ["viewBox", "aria-hidden", "fill", "stroke", "stroke-width", "stroke-linecap", "stroke-linejoin", "width", "height"].map((a) => svg.attrs[a]),
      ["0 0 24 24", "true", "none", "currentColor", "2", "round", "round", "14", "14"], kind);
    assert.ok(svg.children.length >= 1, kind + " has a shape");
  }
});

test("restart and replies kinds: each draws its own vendored glyph in the menu", () => {
  const { menuIconSvg } = setup(1440).ctx;
  assert.strictEqual(menuIcon("restart"), "restart");
  assert.strictEqual(menuIcon("replies-on"), "replies-on");
  assert.strictEqual(menuIcon("replies-off"), "replies-off");
  assert.strictEqual(menuIconSvg("restart").attrs["data-icon"], "rotate-cw");
  assert.strictEqual(menuIconSvg("replies-on").attrs["data-icon"], "message-square-reply");
  assert.strictEqual(menuIconSvg("replies-off").attrs["data-icon"], "message-square-off");
  // The rows themselves: an exited session offers Restart, a live reply-enabled one offers Stop replies.
  const iconOfRow = (t, text) => t.menu.children.filter((n) => n.tag === "button").find((r) => r.children[1].textContent.startsWith(text)).children[0].children[0].attrs["data-icon"];
  const ended = setup(1440);
  ended.open({}, { id: "s3", claude: true, cwd: "/x", status: "exited", exited: true });
  assert.strictEqual(iconOfRow(ended, "Restart"), "rotate-cw");
  const on = setup(1440);
  on.open({}, { id: "s4", claude: true, cwd: "/x", status: "live", replyable: true });
  assert.strictEqual(iconOfRow(on, "Stop replies"), "message-square-off");
});

test("desktop: Duplicate and Copy working directory are two glyphs, so the two rows read apart", () => {
  const t = setup(1440);
  t.open({}, live);
  const svgOf = (text) => t.menu.children.filter((n) => n.tag === "button").find((b) => b.children[1].textContent === text).children[0].children[0];
  assert.notStrictEqual(svgOf("Copy working directory").attrs["data-icon"], svgOf("Duplicate…").attrs["data-icon"]);
});

test("phone: no SVG is built for the legacy menu", () => {
  const t = setup(390);
  t.open({}, live);
  assert.ok(t.menu.children.every((n) => n.tag !== "svg"));
  assert.ok(t.menu.children.every((n) => n.children.length === 0));
});

test("desktop: an empty group leaves no hole: a bare shell is rename | new pane | interrupt, duplicate | end", () => {
  const t = setup(1440);
  t.open({}, { id: "s2", claude: false, status: "live", activity: "idle" });
  const texts = t.menu.children.map((n) => (n.attrs.role === "separator" ? "-" : n.children[1].textContent));
  assert.deepStrictEqual(texts, ["Rename…", "-", "Open in new pane", "-", "Interrupt", "Duplicate…", "-", "End session…"]);
});

test("desktop: a nested shell gets the short menu, in three groups", () => {
  const t = setup(1440, () => true);
  t.open({}, { id: "sh", claude: false, cwd: "/x", status: "live", parent_id: "p" });
  const texts = t.menu.children.map((n) => (n.attrs.role === "separator" ? "-" : n.children[1].textContent));
  assert.deepStrictEqual(texts, ["Rename…", "-", "Open files here", "Copy working directory", "Open in new pane", "-", "End session…"]);
});

test("desktop: each row closes the menu, then runs its action for that session", () => {
  const t = setup(1440);
  t.open({}, live);
  const rows = t.menu.children.filter((n) => n.tag === "button");
  rows[rows.length - 1].onclick();
  assert.deepStrictEqual(t.calls.closed, [false]);
  assert.deepStrictEqual(t.calls.ran, [["end", "s1"]]);
  rows[0].onclick();
  assert.deepStrictEqual(t.calls.ran[1], ["rename", "s1"]);
});

test("desktop: the narrow pane header's own rows lead, as a group of their own, with an empty icon slot", () => {
  const t = setup(1440);
  const ran = [];
  t.open({}, live, [{ label: "Maximize pane", run: () => ran.push("max") }, { label: "Close pane (the session keeps running)", run: () => ran.push("close") }]);
  const kids = t.menu.children;
  assert.deepStrictEqual(kids.slice(0, 3).map(shape), [btn("", "Maximize pane"), btn("", "Close pane (the session keeps running)"), SEP]);
  assert.strictEqual(kids[3].children[1].textContent, "Rename…");
  kids[1].onclick();
  assert.deepStrictEqual(t.calls.closed, [false]);
  assert.deepStrictEqual(ran, ["close"]);
  assert.strictEqual(kids.filter((n) => n.className.includes("is-danger")).length, 1, "closing a pane is not red: the session keeps running");
});

test("desktop: a pane with no session (files) shows only its own rows, no separator", () => {
  const t = setup(1440);
  t.open({}, null, [{ label: "Restore pane", run() {} }, { label: "Close pane (the session keeps running)", run() {} }]);
  assert.deepStrictEqual(t.menu.children.map(shape), [btn("", "Restore pane"), btn("", "Close pane (the session keeps running)")]);
  assert.strictEqual(t.ctx.sessionMenuSid, null);
});

test("phone: the legacy .mi / .mi.dgr buttons, flat, exactly as before", () => {
  const t = setup(390);
  t.open({}, live, [{ label: "Maximize pane", run() {} }]);
  const legacy = (text, dgr) => "button." + (dgr ? "mi.dgr" : "mi") + "[menuitem] '" + text + "'";
  assert.deepStrictEqual(t.menu.children.map(shape), [
    legacy("Maximize pane"), legacy("Rename…"), legacy("Open files here"), legacy("Context by tool"), legacy("Copy working directory"),
    legacy("Open in new pane"), legacy("Interrupt"), legacy("Allow replies"), legacy("Bring work home now"), legacy("Duplicate…"), legacy("End session…", true),
  ]);
  t.menu.children[t.menu.children.length - 1].onclick();
  assert.deepStrictEqual(t.calls.ran, [["end", "s1"]]);
});

test("phone: a shell under its parent keeps the full menu (the short one is desktop only)", () => {
  const t = setup(390, () => true);
  t.open({}, { id: "sh", claude: false, cwd: "/x", status: "live", parent_id: "p" });
  assert.ok(t.menu.children.some((n) => n.textContent === "Duplicate…"));
});

test("the menu's keyboard handler steps the menu's rows through kitMenuKeys (separators are not items) and the first row takes focus", () => {
  const app = read("app.js");
  const place = fnSrc(app, "placeSessionMenu");
  assert.match(place, /querySelector\("\.menu-item, \.mi"\)/, "focus lands on the first row, kit or legacy");
  const keydown = app.slice(app.indexOf('document.addEventListener("keydown", (e) => {\n  const menu = $("session-menu");'));
  const handler = keydown.slice(0, keydown.indexOf("\n});") + 4);
  assert.match(handler, /kitMenuKeys\(menu, e\)/);
  assert.match(handler, /Escape/);
  // The rows it steps are the kit's (an aria-disabled one is skipped) and the phone's legacy .mi: separators are neither.
  const keys = fnSrc(read("kitmenu.js"), "kitMenuKeys");
  assert.match(keys, /querySelectorAll\('\.menu-item:not\(\[aria-disabled="true"\]\), \.mi'\)/);
  assert.match(keys, /menuNavIndex\(/);
});

// ---- task 9b: where the menu hangs, and the ⋯ that stays pressed while it is open ----------------------------------------
test("menuPlace: a ⋯ leaves 4px below it, right-aligned; a sidebar row leaves 5px (m5-1: the row gap and the 4px)", () => {
  assert.strictEqual(MENU_GAP, 4);
  assert.strictEqual(ROW_MENU_GAP, 5);
  // The frame's sidebar: the row spans x 8-282 and ends at y 380; the menu is 240 wide, its right edge on the row's.
  const row = { right: 282, top: 330, bottom: 380 };
  assert.deepStrictEqual(menuPlace(row, 240, 340, 1440, 900, ROW_MENU_GAP), { left: 42, top: 385 });
  // The same menu from the row's own 24px ⋯ (right edge 274): 9px further left, 19px up into the row's lines.
  const dots = { right: 274, top: 332, bottom: 356 };
  assert.deepStrictEqual(menuPlace(dots, 240, 340, 1440, 900, MENU_GAP), { left: 34, top: 360 });
});

test("menuPlace: opens above when it would reach the viewport's last 8px, and never closer than 8px to an edge", () => {
  const row = { right: 282, top: 700, bottom: 750 };
  assert.deepStrictEqual(menuPlace(row, 240, 340, 1440, 900, ROW_MENU_GAP), { left: 42, top: 356 }, "above: the row's top - height - 4");
  // Exactly fitting below stays below; one pixel more flips.
  assert.strictEqual(menuPlace({ right: 282, top: 500, bottom: 547 }, 240, 340, 1440, 900, ROW_MENU_GAP).top, 552);
  assert.strictEqual(menuPlace({ right: 282, top: 500, bottom: 548 }, 240, 340, 1440, 900, ROW_MENU_GAP).top, 500 - 340 - 4);
  assert.strictEqual(menuPlace({ right: 100, top: 10, bottom: 40 }, 240, 900, 1440, 900, 4).top, 8, "never above the viewport");
  assert.strictEqual(menuPlace({ right: 100, top: 10, bottom: 40 }, 240, 100, 1440, 900, 4).left, 8, "never left of 8px");
  assert.strictEqual(menuPlace({ right: 1500, top: 10, bottom: 40 }, 240, 100, 1440, 900, 4).left, 1440 - 240 - 8, "nor right of it");
});

// placeSessionMenu / closeSessionMenu / syncRowMenuExpanded run against stand-ins: two sidebar rows' ⋯ buttons, one pane header's.
function rowsPage() {
  const attrs = (init) => { const a = Object.assign({}, init); return { get: (k) => (k in a ? a[k] : null), set: (k, v) => { a[k] = String(v); }, has: (k) => k in a, a }; };
  const mkRow = (sid, rect) => {
    const li = { dataset: { sid }, getBoundingClientRect: () => rect };
    const at = attrs({ "aria-expanded": "false" });
    const kb = { getAttribute: at.get, setAttribute: at.set, hasAttribute: at.has, isConnected: true, focus() {}, closest: (sel) => (sel === ".drow" || sel === "#sessions .drow" ? li : null), getBoundingClientRect: () => ({ right: rect.right - 8, top: rect.top + 2, bottom: rect.top + 26 }), at };
    return { li, kb };
  };
  const a = mkRow("a", { right: 282, top: 100, bottom: 150 });
  const b = mkRow("b", { right: 282, top: 160, bottom: 210 });
  const header = { closest: () => null, getBoundingClientRect: () => ({ right: 900, top: 0, bottom: 36 }) };
  const open = new Set();
  const menu = { classList: { add: (c) => open.add(c), remove: (c) => open.delete(c), contains: (c) => open.has(c) }, style: {}, offsetWidth: 240, offsetHeight: 300, querySelector: () => null };
  const ctx = vm.createContext({
    $: (id) => (id === "session-menu" ? menu : null),
    document: { querySelectorAll: (sel) => { assert.strictEqual(sel, "#sessions .drow .kbtn[aria-expanded]"); return [a.kb, b.kb]; } },
    window: { innerWidth: 1440, innerHeight: 900 },
    menuPlace, MENU_GAP, ROW_MENU_GAP, sessionMenuSid: null,
  });
  const app = read("app.js");
  const src = ["let sessionMenuAnchor = null;", "let sessionMenuRowSid = null;", "let sessionMenuSid = null;", fnSrc(app, "rowMenuOpenFor"), fnSrc(app, "syncRowMenuExpanded"), fnSrc(app, "closeSessionMenu"), fnSrc(app, "placeSessionMenu"),
    "globalThis.place = placeSessionMenu; globalThis.close = closeSessionMenu; globalThis.openFor = rowMenuOpenFor;"].join("\n");
  new vm.Script(src).runInContext(ctx);
  return { a, b, header, menu, ctx, state: (k) => a.kb.getAttribute("aria-expanded") + "/" + b.kb.getAttribute("aria-expanded") };
}

test("a row ⋯ opens its menu from the row, reads expanded while it is open, and not once it closes", () => {
  const p = rowsPage();
  p.ctx.place(p.menu, p.a.kb);
  assert.strictEqual(p.menu.style.left, "42px", "the menu's right edge on the row's (x 282)");
  assert.strictEqual(p.menu.style.top, "155px", "5px under the row's bottom (150), not 19px into it");
  assert.strictEqual(p.state(), "true/false");
  assert.strictEqual(p.ctx.openFor("a"), true);
  p.ctx.close(false);
  assert.strictEqual(p.state(), "false/false");
  assert.strictEqual(p.ctx.openFor("a"), false);
});

test("opening another row's menu moves the expanded state; a pane header's ⋯ expands no row", () => {
  const p = rowsPage();
  p.ctx.place(p.menu, p.a.kb);
  p.ctx.place(p.menu, p.b.kb);
  assert.strictEqual(p.state(), "false/true");
  p.ctx.place(p.menu, p.header);
  assert.strictEqual(p.state(), "false/false", "the menu hangs from the pane header now");
  assert.strictEqual(p.menu.style.top, "40px", "a ⋯ outside the list keeps the 4px under it");
  assert.strictEqual(p.menu.style.left, "660px");
});

test("a row rebuilt under an open menu is built expanded (paintCompactRow reads rowMenuOpenFor)", () => {
  const app = read("app.js");
  const paint = fnSrc(app, "paintCompactRow");
  assert.match(paint, /kb\.setAttribute\("aria-expanded", rowMenuOpenFor\(s\.id\) \? "true" : "false"\);/);
  assert.match(paint, /kb\.setAttribute\("aria-haspopup", "menu"\);/);
  // The phone's row ⋯ opens the sheet and has no such state.
  const phone = app.slice(app.indexOf('kb.className = "kbtn";'));
  assert.ok(!/aria-expanded/.test(phone.slice(0, phone.indexOf("right.appendChild(kb)"))), "the phone row's ⋯ keeps its attributes");
});

// The document click that closes the row menu (app.js): a click on the glyph inside the ⋯ button lands on the <svg> (or
// its <circle>), not on the button, and must count as a click on the anchor. Before the fix `e.target !== sessionMenuAnchor`
// closed the menu the same click had just opened. The listener is anonymous, so it is cut out by its opening line and run
// against a stand-in document that keeps the handler it is given.
function closerPage(open) {
  const src = read("app.js");
  const start = src.indexOf('document.addEventListener("click", (e) => {\n  const menu = $("session-menu");');
  assert.ok(start >= 0, "the row menu's click closer is in app.js");
  const end = src.indexOf("\n});\n", start) + 4;
  const menu = { classList: { contains: (c) => c === "open" && open }, contains: (n) => n === menu };
  const kb = { kids: [], contains(n) { return n === this || this.kids.includes(n); } };
  const svg = { parent: kb }, circle = { parent: svg };
  kb.kids.push(svg, circle);
  svg.contains = (n) => n === svg || n === circle;
  const closed = [];
  let handler = null;
  const ctx = vm.createContext({
    document: { addEventListener: (t, h) => { if (t === "click") handler = h; } },
    $: (id) => (id === "session-menu" ? menu : null),
    sessionMenuAnchor: kb,
    closeSessionMenu: (r) => closed.push(r),
  });
  new vm.Script(src.slice(start, end)).runInContext(ctx);
  assert.ok(handler, "the closer registered a click listener");
  return { click: (target) => handler({ target }), closed, kb, svg, circle, menu };
}

test("a click on the glyph inside the open menu's ⋯ button keeps the menu open (#168)", () => {
  const p = closerPage(true);
  p.click(p.kb);
  p.click(p.svg);
  p.click(p.circle);
  p.click(p.menu);
  assert.deepStrictEqual(p.closed, [], "the button, its svg and the svg's shape are all the anchor; the menu itself is inside");
});

test("a click elsewhere still closes the open row menu, and a closed menu ignores every click", () => {
  const open = closerPage(true);
  open.click({});
  assert.deepStrictEqual(open.closed, [false], "outside: closes without returning focus");
  const shut = closerPage(false);
  shut.click({});
  assert.deepStrictEqual(shut.closed, []);
});
