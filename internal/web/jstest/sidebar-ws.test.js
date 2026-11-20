"use strict";
// #340 S5 (Task 3): the desktop sidebar's workspace group header, its collapse toggle and the one
// reconcile that paints the list. app.js is a classic script that cannot be required, so the functions
// are cut out by name and run in vm against fakedom.js; desktop.css is read as text.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

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

const desk = require("../static/deskshell.js");

// svgicon.js is a classic script too: run its real source in the context, so svgIcon24 and the glyph
// constants are the page's own and read the context's document.
function makeDoc() {
  const doc = {};
  doc.createElement = (t) => new Node(t, doc);
  doc.createElementNS = (ns, t) => doc.createElement(t);
  doc.createTextNode = (t) => new Text(t);
  return doc;
}
function headerCtx(extra) {
  const ctx = vm.createContext(Object.assign({
    document: makeDoc(), String, shortPath: (p) => p.replace("/h/u", "~"), toggleWsGroup: () => {},
  }, extra));
  vm.runInContext(read("svgicon.js").replace(/^const /gm, "var ") + "\n" + fnSrc(read("app.js"), "renderDesktopEntry") +
    "\nglobalThis.renderDesktopEntry = renderDesktopEntry;", ctx);
  return ctx;
}

const APP = { id: "grp:/h/u/app", header: true, key: "/h/u/app", label: "app", path: "/h/u/app", count: 3, waiting: 1,
  dots: ["dot waiting", "dot idle", "dot idle"], collapsed: false };

test("renderDesktopEntry: an expanded header has the chevron, folder, name, count and pip; no dots", () => {
  const ctx = headerCtx();
  const li = ctx.renderDesktopEntry(APP);
  assert.strictEqual(li.className, "wsh");
  assert.strictEqual(li.dataset.sid, "grp:/h/u/app");
  assert.strictEqual(li.title, "~/app");
  const tog = li.querySelector(".wtog");
  assert.ok(tog, ".wtog");
  assert.strictEqual(tog.tag, "button");
  assert.strictEqual(tog.attrs.type, "button");
  assert.strictEqual(tog.getAttribute("aria-expanded"), "true");
  const kids = tog.children;
  assert.strictEqual(kids[0].className, "wcv");
  assert.strictEqual(kids[1].attrs.class, "wf");
  assert.strictEqual(kids[2].className, "wname");
  assert.strictEqual(kids[2].textContent, "app");
  assert.strictEqual(kids[3].className, "wcount");
  assert.strictEqual(kids[3].textContent, "3");
  assert.strictEqual(kids.length, 4, "no .wdots while open");
  assert.strictEqual(li.querySelector(".wdots"), null);
  const pip = li.querySelector(".wpip");
  assert.ok(pip, ".wpip");
  assert.strictEqual(pip.textContent, "1 waiting");
  assert.strictEqual(pip.title, "1 waiting on you");
  assert.strictEqual(pip.children[0].className, "dot waiting");
  assert.strictEqual(li.children[1], pip, "the pip is the toggle's sibling");
});

test("renderDesktopEntry: a collapsed header draws one dot per row, and Other's tooltip says why", () => {
  const ctx = headerCtx();
  const li = ctx.renderDesktopEntry(Object.assign({}, APP, { collapsed: true }));
  assert.strictEqual(li.querySelector(".wtog").getAttribute("aria-expanded"), "false");
  const dots = li.querySelector(".wdots");
  assert.ok(dots, ".wdots");
  assert.deepStrictEqual(dots.children.map((d) => d.className), ["dot waiting wdot", "dot idle wdot", "dot idle wdot"]);
  const chev = li.querySelector(".wcv").children[0];
  assert.strictEqual(chev.children[0].attrs.d, "m9 18 6-6-6-6");

  const other = ctx.renderDesktopEntry({ id: "grp:", header: true, key: "", path: "", label: "Other", count: 1, waiting: 0, dots: ["dot idle"], collapsed: false });
  assert.strictEqual(other.title, "outside a git repo");
  assert.strictEqual(other.querySelector(".wpip"), null);
});

test("renderDesktopEntry: the toggle calls toggleWsGroup with the group's key", () => {
  const calls = [];
  const ctx = headerCtx({ toggleWsGroup: (k) => calls.push(k) });
  ctx.renderDesktopEntry(APP).querySelector(".wtog").click();
  assert.deepStrictEqual(calls, ["/h/u/app"]);
});

test("renderDesktopEntry: a hostile repo name is text", () => {
  const ctx = headerCtx();
  const evil = "<img src=x onerror=alert(1)>";
  const li = ctx.renderDesktopEntry(Object.assign({}, APP, { label: evil }));
  assert.strictEqual(li.querySelector(".wname").textContent, evil);
  assert.ok(!/innerHTML/.test(fnSrc(read("app.js"), "renderDesktopEntry")), "no innerHTML in renderDesktopEntry");
});

function toggleCtx(over) {
  let wrote = 0;
  const store = { getItem: () => null, setItem: () => { wrote++; } };
  const focus = { n: 0 };
  const painted = { n: 0 };
  const ctx = vm.createContext(Object.assign({
    wsCollapsedToggle: desk.wsCollapsedToggle, wsCollapsedWrite: desk.wsCollapsedWrite, deskStorage: () => store,
    window: { innerWidth: 1440 }, mobileActive: () => false, editing: false, replyFocused: false,
    storeUnlockFocused: () => false, lastSessions: [{ id: "a" }], synced: 0,
    syncSelectedRow: () => { ctx.synced++; },
    deskListPaint: () => {
      painted.n++;
      const li = new Node("li", null);
      const tog = new Node("button", null);
      tog.className = "wtog";
      tog.focus = () => { focus.n++; };
      li.appendChild(tog);
      return new Map([["grp:/r", li]]);
    },
  }, over));
  vm.runInContext("let deskWsCollapsed = new Set();\n" + fnSrc(read("app.js"), "toggleWsGroup") + "\nglobalThis.toggleWsGroup = toggleWsGroup;", ctx);
  return { ctx, focus, painted, wrote: () => wrote, set: () => vm.runInContext("deskWsCollapsed", ctx) };
}

test("toggleWsGroup: repaints, then focuses the new header's toggle", () => {
  const t = toggleCtx();
  t.ctx.toggleWsGroup("/r");
  assert.ok(t.set().has("/r"));
  assert.strictEqual(t.painted.n, 1);
  assert.strictEqual(t.ctx.synced, 1);
  assert.strictEqual(t.focus.n, 1);
  assert.strictEqual(t.wrote(), 1);
});

test("toggleWsGroup: stores but does not repaint while the list is held or on the phone", () => {
  for (const over of [{ editing: true }, { replyFocused: true }, { storeUnlockFocused: () => true }, { mobileActive: () => true }, { lastSessions: [] }]) {
    const t = toggleCtx(over);
    t.ctx.toggleWsGroup("/r");
    assert.ok(t.set().has("/r"), JSON.stringify(Object.keys(over)) + ": the set changes");
    assert.strictEqual(t.wrote(), 1, "the storage was written");
    assert.strictEqual(t.painted.n, 0, "no repaint");
    assert.strictEqual(t.focus.n, 0);
  }
});

test("deskEntries: a collapsed group emits its header and no rows", () => {
  const ctx = vm.createContext({ deskGroups: desk.deskGroups });
  vm.runInContext("let deskWsCollapsed = new Set(['/r/a']);\n" + fnSrc(read("app.js"), "deskEntries") + "\nglobalThis.deskEntries = deskEntries;", ctx);
  const mk = (id, root) => ({ id, name: id, workspace_root: root, activity: "idle" });
  const items = ctx.deskEntries([mk("a1", "/r/a"), mk("a2", "/r/a"), mk("b1", "/r/b")]);
  assert.deepStrictEqual([...items.map((i) => i.id)], ["grp:/r/a", "grp:/r/b", "b1"]);
  assert.strictEqual(items[0].collapsed, true);
  assert.strictEqual(items[0].count, 2);
  assert.strictEqual(items[1].collapsed, false);
});

test("the desktop list has one reconcile, deskListPaint, with the header signature", () => {
  const app = read("app.js");
  const refresh = fnSrc(app, "refreshSessionsRun");
  assert.ok(refresh.includes("rowById = deskListPaint();"));
  assert.ok(!refresh.includes("reconcileRows("), "refreshSessionsRun reconciles through deskListPaint only");
  assert.ok(fnSrc(app, "deskListPaint").includes("deskWsHeaderSig(it)"));
});

test("header CSS: the name keeps room, the dots clip, the pip and + never shrink", () => {
  const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => {
    const esc = sel.replace(/[.[\]#]/g, "\\$&").replace(/ /g, "\\s+");
    const m = new RegExp("(?:^|[}\\s])" + esc + "\\s*\\{([^}]*)\\}").exec(css);
    return m ? m[1] : null;
  };
  assert.match(rule("#sessions .wsh .wname") || "", /min-width:\s*48px/);
  const dots = rule("#sessions .wsh .wdots") || "";
  assert.match(dots, /overflow:\s*hidden/);
  assert.match(dots, /min-width:\s*0/);
  assert.match(rule("#sessions .wsh .wpip") || "", /flex:\s*none/);
  assert.match(rule("#sessions.sess-stale .wsh .wpip") || "", /color:\s*var\(--muted\)/);
});
