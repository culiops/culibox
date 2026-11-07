"use strict";
// The reply opt-in rows are offered only while the kill-switch reads true (menuItems).
globalThis.repliesEnabled = true;
// The session row's ⋯ menu on desktop is the Plan 1 kit menu (F12, M5.1): .menu-item buttons with an
// icon slot, a .menu-sep only between groups, End session… red and last. The phone keeps today's
// .mi / .mi.dgr buttons. openSessionMenu and its builders live in app.js, a classic script that cannot
// be required, so they are cut out of the source by name and run against a stand-in DOM.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { menuItems, menuGroups, menuIcon, menuNavIndex } = require("../static/sessionrow.js");

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
function el(tag) {
  return {
    tag, type: "", className: "", textContent: "", title: "", onclick: null, children: [], attrs: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    append(...n) { this.children.push(...n); },
    replaceChildren(...n) { this.children = n; },
  };
}
// shape: one compact, comparable line per node.
function shape(n) {
  const a = n.attrs.role ? "[" + n.attrs.role + "]" : "";
  const kids = n.children.length ? " > (" + n.children.map(shape).join(", ") + ")" : "";
  return n.tag + "." + n.className.split(" ").join(".") + a + (n.textContent ? " '" + n.textContent + "'" : "") + kids;
}

const NAMES = ["kitMenuButton", "kitMenuNodes", "openSessionMenu"];
function setup(width, nestedFn) {
  const menu = el("div");
  const calls = { placed: [], closed: [], ran: [], own: [] };
  const ctx = vm.createContext({
    $: (id) => (id === "session-menu" ? menu : null),
    document: { createElement: el },
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
  const src = NAMES.map((n) => fnSrc(read("app.js"), n)).join("\n") + "\nglobalThis.openSessionMenu = openSessionMenu;";
  new vm.Script(src).runInContext(ctx);
  return { menu, calls, ctx, open: (anchor, s, extra) => ctx.openSessionMenu(anchor, s, extra) };
}
const live = { id: "s1", name: "agent-box", claude: true, cwd: "/x", status: "live", activity: "working", clone_of: "/r", runtime: "box" };
const btn = (icon, text, danger) => "button." + (danger ? "menu-item.is-danger" : "menu-item") + "[menuitem] > (span.menu-icon" + (icon ? " '" + icon + "'" : "") + ", span.menu-text '" + text + "')";
const SEP = "div.menu-sep[separator]";

test("desktop: the row menu is four groups of kit rows, a separator between groups only", () => {
  const t = setup(1440);
  t.open({}, live);
  assert.deepStrictEqual(t.menu.children.map(shape), [
    btn("✎", "Rename…"), SEP,
    btn("▤", "Open files here"), btn("◔", "Context by tool"), btn("⎘", "Copy working directory"), btn("◫", "Open in new pane"), SEP,
    btn("‖", "Interrupt"), btn("", "Allow replies"), btn("↧", "Bring work home now"), btn("⧉", "Duplicate…"), SEP,
    btn("✕", "End session…", true),
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

test("Arrow Up and Down wrap, Home and End jump, and nothing else is handled", () => {
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

test("the menu's keyboard handler walks the .menu-item rows (separators are not items) and the first row takes focus", () => {
  const app = read("app.js");
  const place = fnSrc(app, "placeSessionMenu");
  assert.match(place, /querySelector\("\.menu-item, \.mi"\)/, "focus lands on the first row, kit or legacy");
  const keydown = app.slice(app.indexOf('document.addEventListener("keydown", (e) => {\n  const menu = $("session-menu");'));
  const handler = keydown.slice(0, keydown.indexOf("\n});") + 4);
  assert.match(handler, /querySelectorAll\("\.menu-item, \.mi"\)/);
  assert.match(handler, /menuNavIndex\(/);
  assert.match(handler, /Escape/);
});
