"use strict";
// The desktop pane header as m1-1 / m1-2 draw it (#172 Task 3: F13 F14 F18 G6): the meta
// `haiku-4.5 · ctx 35% · $3.12`, Files as a toggle after the view tabs, "Open terminal" a
// small secondary button. tabs.js and app.js are classic scripts that cannot be required,
// so renderHeader / paintDrawerTabs / stripOpenButton are cut out by name and run in vm
// against a tiny DOM, with the real pure scripts (deskshell, usage, analytics) beside them.
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
// The Cell class's renderHeader() method, as a function taking `this`.
function methodSrc(src, name) {
  const start = src.indexOf("  " + name + "() {");
  assert.ok(start >= 0, name + " not found");
  const end = src.indexOf("\n  }\n", start);
  return "(function " + src.slice(start, end + 4).trim().replace(/^\w+\(\)/, "()") + ")";
}

// A DOM node reduced to what the cut-out code touches.
function el(tag) {
  const e = {
    tag, children: [], attrs: {}, dataset: {}, title: "", hidden: false, onclick: null, _cls: "",
    get className() { return e._cls; },
    set className(v) { e._cls = v; },
    classList: {
      contains: (c) => e._cls.split(/\s+/).includes(c),
      add: (c) => { if (!e.classList.contains(c)) e._cls = (e._cls + " " + c).trim(); },
      toggle: (c, on) => {
        const has = e.classList.contains(c);
        const want = on === undefined ? !has : !!on;
        if (want && !has) e._cls = (e._cls + " " + c).trim();
        if (!want && has) e._cls = e._cls.split(/\s+/).filter((x) => x !== c).join(" ");
      },
    },
    append(...kids) { for (const k of kids) e.children.push(typeof k === "string" ? { tag: "#text", textContent: k, children: [], attrs: {}, classList: { contains: () => false } } : k); },
    appendChild(k) { e.children.push(k); return k; },
    replaceChildren(...kids) { e.children = []; e.append(...kids); },
    setAttribute(k, v) { e.attrs[k] = String(v); },
    getAttribute(k) { return k in e.attrs ? e.attrs[k] : null; },
    get textContent() { return e.children.map((k) => k.textContent).join(""); },
    set textContent(v) { e.children = []; if (v !== "") e.append(String(v)); },
    all() { return e.children.flatMap((k) => (k.all ? [k, ...k.all()] : [k])); },
    // Only the selectors the cut-out code uses: a single `.class`, and `[data-k]`.
    querySelector(sel) {
      assert.match(sel, /^\.[\w-]+$/, "unexpected selector " + sel);
      return e.all().find((n) => n.classList && n.classList.contains(sel.slice(1))) || null;
    },
    querySelectorAll(sel) {
      assert.strictEqual(sel, "[data-k]", "unexpected selector " + sel);
      return e.all().filter((n) => n.dataset && n.dataset.k !== undefined);
    },
  };
  return e;
}
const svgIcon = () => { const s = el("svg"); s.setAttribute("aria-hidden", "true"); return s; };

const desk = require("../static/deskshell.js");
const usage = require("../static/usage.js");
require("../static/analytics.js");

const TAB_SRC = read("tabs.js");
const APP_SRC = read("app.js");

// calls that are the header's own bookkeeping (the meta observer and fit), not a press
const userCalls = (c) => !/^(ro\.|fitPaneMeta)/.test(c);

// A header for one cell. `opts`: usage, session, narrow, tabs (kinds), view, drawer, maximized.
function header(opts = {}) {
  const sess = opts.session === undefined ? { id: "s1", name: "agent-box", activity: "working", runtime: "box", cost_usd: 3.12 } : opts.session;
  const u = opts.usage === undefined ? { model: "claude-opus-5-5", effort: "high", pct: 35, contextTokens: 70000 } : opts.usage;
  const term = opts.files ? null : { kind: "terminal", id: "s1", name: "agent-box", usage: () => u, status: () => "connected", activity: () => "working" };
  const fileTab = { kind: "viewer", path: "/work/README.md", name: "README.md" };
  const active = term || fileTab;
  const calls = [];
  const cell = {
    tabs: [active], activeTab: active, view: opts.view || "terminal", _narrow: !!opts.narrow, _hdrKey: "",
    bar: el("div"), activate() {}, setView() {}, toggleView(v) { calls.push("toggleView:" + v); }, dock: null,
    _metaRo: { disconnect() { calls.push("ro.disconnect"); }, observe(n) { calls.push("ro.observe:" + n.className); } },
  };
  const ctx = vm.createContext({
    document: { createElement: el }, window: { shellProfile: "bash" }, Date, String, JSON, Math, Number, Array, Object,
    lastSessions: sess ? [sess] : [], boxRuntime: "runc", maximized: null, focusedCell: cell, IS_MAC: false,
    paneHeaderModel: desk.paneHeaderModel, headerSyncKey: desk.headerSyncKey, headerDot: desk.headerDot,
    paneTabs: desk.paneTabs, paneFilesToggle: desk.paneFilesToggle, paneTabsClass: desk.paneTabsClass, paneTitle: desk.paneTitle,
    usageTitle: usage.usageTitle, tabIcon: () => svgIcon(), acctBadgeEl: () => null,
    cellTerminal: (c) => (c.activeTab && c.activeTab.kind === "terminal" ? c.activeTab : null),
    shortcutHint: (id, mac) => (id === "files.toggle" ? "Ctrl+Shift+E" : "Ctrl+Shift+`"),
    openPaneView: (sid, v) => calls.push("openPaneView:" + sid + ":" + v), closeDrawerFor() {}, setFocusedCell() {},
    headerDrawerPress: (c, kind, sid) => calls.push("drawerPress:" + kind + ":" + sid),
    spawnShellIn() {}, toggleSessionMenuAt() {}, toggleMax() {}, removeCell() {}, openTabSwitcher() {},
    paintDrawerTabs: undefined, fitPaneMeta: (m) => calls.push("fitPaneMeta:" + m.className),
  });
  ctx.paintDrawerTabs = (c) => {
    // The real one, over the real bar: the drawer is open on s1 when opts.drawer.
    const run = vm.runInContext("(" + fnSrc(APP_SRC, "paintDrawerTabs") + ")", vm.createContext({
      cellTerminal: ctx.cellTerminal, drawerState: { open: opts.drawer ? "files" : null, sessionId: "s1" }, focusedCell: cell,
    }));
    run(c);
  };
  vm.runInContext("globalThis.renderHeader = " + methodSrc(TAB_SRC, "renderHeader"), ctx);
  ctx.renderHeader.call(cell);
  const all = cell.bar.all();
  const byClass = (c) => all.filter((n) => n.classList && n.classList.contains(c));
  return { cell, bar: cell.bar, all, byClass, calls, ctx };
}

test("a wide terminal pane's meta reads haiku-4.5 · ctx 35% · $3.12, in the kit's .ph-meta, in header order", () => {
  const h = header({ usage: { model: "claude-haiku-4-5-20251001", effort: "high", pct: 35, contextTokens: 70000 } });
  const [meta] = h.byClass("ph-meta");
  assert.ok(meta, "a .ph-meta wraps the three");
  assert.strictEqual(meta.textContent, "haiku-4.5 · ctx 35% · $3.12");
  const main = h.bar.children[0];
  assert.deepStrictEqual(main.children.map((n) => n.classList.contains("ph-meta")).lastIndexOf(true), main.children.length - 1, "the meta is the last slot of the left column");
  const [model] = h.byClass("ph-model");
  assert.strictEqual(model.textContent, "haiku-4.5");
  assert.strictEqual(model.title, "claude-haiku-4-5-20251001", "the raw id is the model's title");
  const [cost] = h.byClass("ph-cost");
  assert.strictEqual(cost.textContent, "$3.12");
  assert.ok(cost.title, "the cost keeps its explanatory title");
});

test("the ctx stays a button into Activity: text `ctx 35%`, a title starting `Context used`", () => {
  const h = header();
  const [ctx] = h.byClass("ph-ctx");
  assert.strictEqual(ctx.tag, "button");
  assert.strictEqual(ctx.textContent, "ctx 35%");
  assert.match(ctx.title, /^Context used 35%/);
  assert.match(ctx.title, /click for context by tool/);
  ctx.onclick({ stopPropagation() {} });
  assert.deepStrictEqual(h.calls.filter(userCalls), ["openPaneView:s1:activity"]);
});

test("the meta separators are decoration, and each unit carries its own so CSS can drop a unit whole", () => {
  const h = header();
  const seps = h.all.filter((n) => n.classList && n.classList.contains("ph-msep"));
  assert.strictEqual(seps.length, 2);
  assert.ok(seps.every((n) => n.textContent === " · " && n.attrs["aria-hidden"] === "true"));
  const [meta] = h.byClass("ph-meta");
  assert.deepStrictEqual(meta.children.map((u) => u.className), ["ph-mi mi-model", "ph-mi mi-ctx", "ph-mi mi-cost"], "model, ctx, cost: the reading order");
  assert.deepStrictEqual(meta.children.map((u) => u.children.some((k) => k.classList.contains("ph-msep"))), [true, true, false], "a separator trails every unit but the last");
  assert.deepStrictEqual(meta.children.map((u) => u.children[0].className), ["ph-model", "ph-ctx", "ph-cost"]);
});

test("no usage and no cost: no meta slot at all; a lone cost keeps no stray separator", () => {
  const none = header({ usage: null, session: { id: "s1", name: "x", activity: "working", runtime: "box" } });
  assert.strictEqual(none.byClass("ph-meta").length, 0);
  const costOnly = header({ usage: null });
  assert.strictEqual(costOnly.byClass("ph-meta")[0].textContent, "$3.12");
  assert.strictEqual(costOnly.all.filter((n) => n.classList && n.classList.contains("ph-msep")).length, 0);
});

test("a narrow pane is dot, name and ONE chip: no meta, no model, no ctx, no cost", () => {
  const h = header({ narrow: true });
  assert.strictEqual(h.byClass("ph-meta").length, 0);
  assert.strictEqual(h.byClass("ph-model").length + h.byClass("ph-ctx").length + h.byClass("ph-cost").length, 0);
  assert.strictEqual(h.byClass("ph-chip").length, 1);
  assert.strictEqual(h.byClass("ph-name").length, 1);
  assert.strictEqual(h.byClass("ph-dot").length, 1);
});

test("a terminal pane's tabs are Terminal, Activity, Access; Files is a button after the separator, before + Shell", () => {
  const h = header();
  const tabs = h.byClass("ph-tab");
  assert.deepStrictEqual(tabs.map((b) => b.textContent), ["Terminal", "Activity", "Access"]);
  const side = h.bar.children[1];
  const order = side.children.map((n) => n.classList.contains("ph-tabs") ? "tabs" : n.classList.contains("ph-sep") ? "sep" : n.classList.contains("ph-files") ? "files" : n.classList.contains("ph-shell") ? "shell" : n.classList.contains("ph-ics") ? "ics" : "?");
  assert.deepStrictEqual(order, ["tabs", "sep", "files", "shell", "ics"]);
  const [files] = h.byClass("ph-files");
  assert.strictEqual(files.tag, "button");
  assert.strictEqual(files.className, "btn btn-ghost btn-sm ph-files");
  assert.strictEqual(files.title, "Files drawer (Ctrl+Shift+E)");
  assert.strictEqual(files.attrs["aria-label"], "Files");
  assert.strictEqual(files.attrs["aria-pressed"], "false");
  assert.strictEqual(files.dataset.k, "files");
  assert.deepStrictEqual(files.children.map((n) => n.className), ["ph-ico", "ph-lbl"], "icon and word both kept: the narrow rule picks");
  assert.strictEqual(files.textContent, "Files");
  files.onclick({ stopPropagation() {} });
  assert.deepStrictEqual(h.calls.filter(userCalls), ["drawerPress:files:s1"]);
});

test("with the Files drawer open exactly ONE tab looks selected, and Files is the pressed button", () => {
  const h = header({ drawer: true });
  const selected = h.byClass("ph-tab").filter((b) => b.classList.contains("on"));
  assert.strictEqual(selected.length, 1);
  assert.strictEqual(selected[0].textContent, "Terminal");
  const [files] = h.byClass("ph-files");
  assert.strictEqual(files.attrs["aria-pressed"], "true");
  assert.ok(!files.classList.contains("ph-tab"), "Files is not a tab: no tab look on it");
  assert.ok(!files.classList.contains("open"), "and no second selected class");
  const closed = header({ drawer: false });
  assert.strictEqual(closed.byClass("ph-files")[0].attrs["aria-pressed"], "false");
});

test("Activity open and the drawer open: still one selected tab (Activity), Files pressed beside it", () => {
  const h = header({ drawer: true, view: "activity" });
  const selected = h.byClass("ph-tab").filter((b) => b.classList.contains("on"));
  assert.deepStrictEqual(selected.map((b) => b.textContent), ["Activity"]);
  assert.strictEqual(h.byClass("ph-files")[0].attrs["aria-pressed"], "true");
});

test("a pane of files is unchanged: Files alone as its one tab, no toggle button, no meta", () => {
  const h = header({ files: true, session: null, usage: null });
  const tabs = h.byClass("ph-tab");
  assert.deepStrictEqual(tabs.map((b) => b.textContent), ["Files"]);
  assert.strictEqual(tabs[0].dataset.k, "files");
  assert.strictEqual(h.byClass("ph-tabs")[0].className, "ph-tabs one");
  assert.strictEqual(h.byClass("ph-files").length, 0);
  assert.strictEqual(h.byClass("ph-meta").length, 0);
  const open = header({ files: true, session: null, usage: null, drawer: false });
  assert.strictEqual(open.byClass("ph-tab")[0].attrs["aria-pressed"], "false");
});

test("paintDrawerTabs sets aria-pressed on the Files control and nothing else, tab or toggle", () => {
  const run = (drawer) => {
    const cell = { bar: el("div") };
    const tab = el("button"); tab.className = "ph-tab"; tab.dataset.k = "files";
    const tog = el("button"); tog.className = "btn btn-ghost btn-sm ph-files"; tog.dataset.k = "files";
    const view = el("button"); view.className = "ph-tab on"; view.dataset.v = "activity"; view.setAttribute("aria-pressed", "true");
    cell.bar.append(tab, tog, view);
    const ctx = vm.createContext({ cellTerminal: () => ({ id: "s1" }), drawerState: drawer, focusedCell: cell });
    vm.runInContext("globalThis.p = " + fnSrc(APP_SRC, "paintDrawerTabs"), ctx);
    ctx.p(cell);
    return { tab, tog, view };
  };
  const open = run({ open: "files", sessionId: "s1" });
  assert.strictEqual(open.tab.attrs["aria-pressed"], "true");
  assert.strictEqual(open.tog.attrs["aria-pressed"], "true");
  assert.strictEqual(open.view.attrs["aria-pressed"], "true", "a view tab is untouched");
  assert.ok(!open.tab.classList.contains("open") && !open.tog.classList.contains("open"), "no .open class: the pressed state is aria-pressed");
  const closed = run({ open: null, sessionId: "s1" });
  assert.strictEqual(closed.tog.attrs["aria-pressed"], "false");
  const other = run({ open: "files", sessionId: "s2" });
  assert.strictEqual(other.tog.attrs["aria-pressed"], "false", "the drawer is open on another session's pane");
});

test("the ask strip's Open terminal is a small secondary button, as drawn", () => {
  const calls = [];
  const ctx = vm.createContext({ document: { createElement: el }, closeDrawerFor: (id) => calls.push("close:" + id), openTerminal: (id, n) => calls.push("open:" + id + ":" + n) });
  vm.runInContext("globalThis.f = " + fnSrc(APP_SRC, "stripOpenButton"), ctx);
  const b = ctx.f({ id: "s1", name: "agent-box" });
  assert.strictEqual(b.className, "btn btn-sm");
  assert.strictEqual(b.textContent, "Open terminal");
  assert.strictEqual(b.type, "button");
  b.onclick();
  assert.deepStrictEqual(calls, ["close:s1", "open:s1:agent-box"]);
});

test("+ Shell is the kit's ghost button, not a bespoke one, and keeps its hooks", () => {
  const h = header();
  const [sh] = h.byClass("ph-shell");
  assert.strictEqual(sh.className, "btn btn-ghost btn-sm ph-shell");
  assert.strictEqual(sh.textContent, "+ Shell");
  assert.match(sh.title, /^New shell for this session/);
  assert.strictEqual(sh.hidden, false);
  const none = header({ files: true, session: null, usage: null });
  assert.strictEqual(none.byClass("ph-shell")[0].className, "btn btn-ghost btn-sm ph-shell", "a pane of files too");
});

// A unit the wrap pushed off the first line is clipped; fitPaneMeta marks it .mi-off (visibility:
// hidden: out of the tab order and out of the accessibility tree). renderHeader runs it after every
// build and keeps ONE observer on the current meta, so a resize that moves a unit re-runs it.
test("renderHeader fits the meta after building it and observes it, replacing the old observation", () => {
  const h = header();
  const [meta] = h.byClass("ph-meta");
  assert.deepStrictEqual(h.calls.filter((c) => /^(ro|fitPaneMeta)/.test(c)), ["ro.disconnect", "ro.observe:ph-meta", "fitPaneMeta:ph-meta"]);
  assert.ok(meta.children.every((u) => !u.classList.contains("mi-off")), "built visible; the fit decides");
  const narrow = header({ narrow: true });
  assert.deepStrictEqual(narrow.calls.filter((c) => /^(ro|fitPaneMeta)/.test(c)), ["ro.disconnect"], "no meta: the old observation is dropped, nothing new");
});

test("fitPaneMeta toggles .mi-off on the units the wrap left below the line, using metaUnitsOff", () => {
  const mk = (tops) => tops.map((t) => { const u = { top: t, classes: new Set(), getBoundingClientRect() { return { top: t, bottom: t + 16 }; } }; u.classList = { toggle: (c, on) => { if (on) u.classes.add(c); else u.classes.delete(c); } }; return u; });
  const ctx = vm.createContext({ metaUnitsOff: desk.metaUnitsOff, Array });
  vm.runInContext("globalThis.f = " + fnSrc(TAB_SRC, "fitPaneMeta"), ctx);
  const run = (tops, box) => {
    const units = mk(tops);
    ctx.f({ children: units, getBoundingClientRect: () => box });
    return units.map((u) => u.classes.has("mi-off"));
  };
  assert.deepStrictEqual(run([100, 100, 100], { top: 100, bottom: 116 }), [false, false, false]);
  assert.deepStrictEqual(run([100, 100, 116], { top: 100, bottom: 116 }), [false, false, true]);
  assert.deepStrictEqual(run([116, 116, 116], { top: 100, bottom: 116 }), [true, true, true]);
  assert.deepStrictEqual(run([0, 0, 0], { top: 0, bottom: 0 }), [false, false, false], "no layout: nothing hidden");
  // And back: a unit that fits again is shown again (the same units, re-fitted).
  const units = mk([100, 116, 132]);
  const meta = { children: units, getBoundingClientRect: () => ({ top: 100, bottom: 116 }) };
  ctx.f(meta);
  assert.deepStrictEqual(units.map((u) => u.classes.has("mi-off")), [false, true, true]);
  units.forEach((u, i) => { u.getBoundingClientRect = () => ({ top: 100, bottom: 116 }); });
  ctx.f(meta);
  assert.deepStrictEqual(units.map((u) => u.classes.has("mi-off")), [false, false, false]);
});
