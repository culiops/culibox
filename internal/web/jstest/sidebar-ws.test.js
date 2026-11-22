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

test("header CSS: the dots give way first, nothing paints over the pip or +", () => {
  const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => {
    const esc = sel.replace(/[.[\]#]/g, "\\$&").replace(/ /g, "\\s+");
    const m = new RegExp("(?:^|[}\\s])" + esc + "\\s*\\{([^}]*)\\}").exec(css);
    return m ? m[1] : null;
  };
  const name = rule("#sessions .wsh .wname") || "";
  assert.match(name, /min-width:\s*0/, "a short name is as wide as its text: the count sits right after it");
  assert.match(name, /flex:\s*0 1 auto/);
  assert.match(name, /text-overflow:\s*ellipsis/);
  const dots = rule("#sessions .wsh .wdots") || "";
  assert.match(dots, /flex:\s*0 1000 auto/, "the dots give up their room before the name does");
  assert.match(dots, /overflow:\s*hidden/);
  assert.match(dots, /min-width:\s*0/);
  assert.match(rule("#sessions .wsh .wtog") || "", /overflow:\s*hidden/, "the toggle clips: nothing paints over the pip");
  assert.match(rule("#sessions .wsh .wpip") || "", /flex:\s*none/);
  assert.match(rule("#sessions.sess-stale .wsh .wpip") || "", /color:\s*var\(--muted\)/);
});

// ---- #340 S5 (Task 4): the row's kind icon, its tier chip's glyph, the aligned sub-line ----------------------
// deskKindEl runs the page's real svgicon.js, claudemark.js and deskshell.js's deskRowKind, with the real
// spawnIsShellProfile from the New session dialog's model.
function kindCtx(profiles) {
  const ctx = vm.createContext({
    document: makeDoc(), String, profiles, deskRowKind: desk.deskRowKind,
  });
  vm.runInContext(read("svgicon.js").replace(/^const /gm, "var ") + "\n" + read("claudemark.js").replace(/^const /gm, "var ") +
    "\n" + fnSrc(read("app.js"), "deskKindEl") + "\nglobalThis.deskKindEl = deskKindEl;", ctx);
  return ctx;
}

test("deskKindEl: no profile, no terminal; the Claude mark comes from the session", () => {
  const ctx = kindCtx([{ name: "sh", bin: "/usr/bin/bash" }]);
  const sh = ctx.deskKindEl({ agent: "sh" });
  assert.strictEqual(sh.className, "ak");
  assert.strictEqual(sh.title, "bash shell");
  assert.ok(sh.querySelector("polyline"), "the terminal's prompt chevron");
  const cl = ctx.deskKindEl({ agent: "gone", claude: true });
  assert.strictEqual(cl.className, "ak");
  assert.strictEqual(cl.title, "Claude Code session");
  assert.strictEqual(cl.children[0].attrs.fill, "currentColor");
  assert.strictEqual(ctx.deskKindEl({ agent: "gone", claude: false }), null);
  assert.doesNotThrow(() => kindCtx([]).deskKindEl({ agent: "sh" }));
  assert.strictEqual(kindCtx([]).deskKindEl({ agent: "sh" }), null);
});

test("the compact row puts the kind icon before the name; the phone row has none", () => {
  const src = fnSrc(read("app.js"), "renderSession");
  const at = src.indexOf("compact ? deskKindEl(s)");
  assert.ok(at > 0, "deskKindEl(s) sits behind compact ?");
  assert.ok(at < src.indexOf("sname.appendChild(nm)"), "and before the name is added");
  assert.strictEqual(src.split("deskKindEl(").length - 1, 1, "once");
});

function chipRow(chip, tier) {
  const li = new Node("li", null);
  li.classList = { contains: () => false };
  const sname = new Node("div", null), grow = new Node("div", null);
  const doc = makeDoc();
  const ctx = vm.createContext({
    document: doc, Date, boxRuntime: "", window: { HOME_DIR: "" },
    rowChip: () => chip, acctBadge: () => null, awsBadge: () => null, tierBadge: () => tier || null,
    rowSubline: () => ({ text: "" }), rowMenuOpenFor: () => false, openSessionMenu: () => {}, openTerminal: () => {},
    openPaneView: () => false, wmCopyLine: () => "", sessionWorkHome: () => "", handbackLine: () => null, li, sname, grow, s: { id: "a" },
  });
  vm.runInContext(read("svgicon.js").replace(/^const /gm, "var ") + "\n" + fnSrc(read("app.js"), "deskChipIcon") + "\n" +
    fnSrc(read("app.js"), "paintCompactRow") + "\npaintCompactRow(li, sname, grow, s, {});", ctx);
  return sname.children[0];
}

test("paintCompactRow: a tier chip is .tchip with its glyph then its word; write is plain", () => {
  const c = chipRow({ text: "clone", tone: "box", icon: "clone", title: "T" });
  assert.strictEqual(c.className, "dchip box tchip");
  assert.strictEqual(c.children[0].tag, "svg");
  assert.strictEqual(c.children[0].attrs.class, "tchip-i");
  assert.strictEqual(c.textContent, "clone");
  assert.strictEqual(c.title, "T");
  // box and host carry no title of their own: it still comes from tierBadge.
  const box = chipRow({ text: "box", tone: "box", icon: "box" }, { title: "Runs in a box" });
  assert.strictEqual(box.title, "Runs in a box");
  assert.strictEqual(box.children[0].attrs.class, "tchip-i");
  const w = chipRow({ text: "write", tone: "write" });
  assert.strictEqual(w.className, "dchip write");
  assert.strictEqual(w.title, "AWS write window open");
  assert.strictEqual(w.querySelector("svg"), null);
  assert.strictEqual(w.textContent, "write");
});

test("rowLocal carries the row kind, and rowSig moves with it, so a late profile list repaints the row", () => {
  assert.ok(fnSrc(read("app.js"), "rowLocal").includes("kind: deskRowKind(s,"));
  const { rowSig } = require("../static/reconcile.js");
  const s = { id: "a", agent: "sh" };
  const shell = { kind: "shell", title: "bash shell" };
  const none = rowSig(s, { kind: null });
  assert.notStrictEqual(rowSig(s, { kind: shell }), none, "a shell kind arriving moves the signature");
  assert.strictEqual(rowSig(s, { kind: { kind: "shell", title: "bash shell" } }), rowSig(s, { kind: shell }), "equal kinds, equal sigs");
  assert.notStrictEqual(rowSig(s, { kind: { kind: "claude", title: "Claude Code session" } }), rowSig(s, { kind: shell }));
  assert.strictEqual(rowSig(s, {}), none, "a caller that sets no kind reads as null");
});

test("row CSS: the sub-line starts under the name", () => {
  const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => {
    const esc = sel.replace(/[.[\]#():]/g, "\\$&").replace(/ /g, "\\s+");
    const m = new RegExp("(?:^|[}\\s])" + esc + "\\s*\\{([^}]*)\\}").exec(css);
    return m ? m[1] : null;
  };
  assert.match(rule("#sessions .drow:not(.nested):has(.ak) .sub") || "", /padding-left:\s*19px/);
  assert.match(rule("#sessions .drow .sname .ak") || "", /flex:\s*none/);
  assert.match(rule("#sessions .drow:not(.nested)") || "", /padding-left:\s*14px/);
  assert.match(rule("#sessions .drow.nested .ak svg") || "", /width:\s*12px/);
  assert.match(rule("#sessions .dchip.tchip") || "", /display:\s*inline-flex/);
  assert.match(rule("#sessions .dchip .tchip-i") || "", /flex:\s*none/);
  assert.match(rule("#sessions .dchip.host .tchip-i") || "", /color:\s*var\(--fg\)/);
});

// ---- #340 S5 (Task 5): the group's +, and its path surviving a profile switch ---------------------------------
test("the group + opens New session on the group's path; Other has none", () => {
  const calls = [];
  const ctx = headerCtx({ openSpawnDialog: (...a) => calls.push(a) });
  const li = ctx.renderDesktopEntry(APP);
  const plus = li.querySelector(".wplus");
  assert.ok(plus, ".wplus");
  assert.strictEqual(plus.tag, "button");
  assert.strictEqual(plus.attrs.type, "button");
  assert.strictEqual(plus.title, "New session in ~/app");
  assert.strictEqual(plus.getAttribute("aria-label"), "New session in app");
  assert.strictEqual(Number(plus.children[0].attrs.width), 13, "the 13px plus glyph");
  assert.strictEqual(plus.children[0].children[0].attrs.d, "M5 12h14");
  assert.strictEqual(li.children[2], plus, "after the pip");
  plus.click();
  assert.strictEqual(calls.length, 1);
  assert.strictEqual(calls[0][0], null);
  assert.deepStrictEqual(Object.assign({}, calls[0][1]), { cwd: "/h/u/app", source: "group" });

  const noPip = ctx.renderDesktopEntry(Object.assign({}, APP, { waiting: 0 }));
  assert.strictEqual(noPip.children[1], noPip.querySelector(".wplus"), "with no pip it follows the toggle");

  const other = ctx.renderDesktopEntry({ id: "grp:", header: true, key: "", path: "", label: "Other", count: 1, waiting: 0, dots: ["dot idle"], collapsed: false });
  assert.strictEqual(other.querySelector(".wplus"), null);
  assert.strictEqual(calls.length, 1, "building a header never opens the dialog");
});

function groupHolds() {
  const ctx = vm.createContext({});
  vm.runInContext(fnSrc(read("spawnpreview.js"), "spawnGroupHolds") + "\nglobalThis.spawnGroupHolds = spawnGroupHolds;", ctx);
  return ctx.spawnGroupHolds;
}

test("spawnGroupHolds: only a group prefill the user has not changed", () => {
  const holds = groupHolds();
  const o = { source: "group", prefill: "/r", cwd: "/r", text: "/r" };
  assert.strictEqual(holds(o), true);
  assert.strictEqual(holds(Object.assign({}, o, { text: "/r/x" })), false);
  assert.strictEqual(holds(Object.assign({}, o, { cwd: "/q" })), false);
  assert.strictEqual(holds(Object.assign({}, o, { source: "profile" })), false);
  assert.strictEqual(holds(Object.assign({}, o, { prefill: "" })), false);
  assert.strictEqual(holds(Object.assign({}, o, { prefill: "", cwd: "", text: "" })), false);
  assert.strictEqual(holds(null), false);
  assert.strictEqual(typeof require("../static/spawnpreview.js").spawnGroupHolds, "function", "exported");
});

test("applyProfileCwd keeps a group's path over the profile's default_cwd", () => {
  const run = (source) => {
    const calls = [];
    const ctx = vm.createContext({
      currentProfile: () => ({ default_cwd: "/home/u/other" }),
      spawnWsSource: source, spawnWsPrefill: "/r/a", getCwd: () => "/r/a", $: () => ({ value: "/r/a" }),
      spawnGroupHolds: groupHolds(),
      setSpawnCwd: (...a) => calls.push(["set"].concat(a)), onSpawnCwdChanged: () => calls.push(["changed"]),
      spawnWsPicker: null, drawerDesktop: () => false,
    });
    vm.runInContext(fnSrc(read("app.js"), "applyProfileCwd") + "\napplyProfileCwd();", ctx);
    return calls;
  };
  assert.deepStrictEqual(run("group"), [], "the group's path stays; nothing is set or repainted");
  assert.deepStrictEqual(run("profile"), [["set", "/home/u/other", "profile"], ["changed"]]);
  assert.match(fnSrc(read("app.js"), "applyProfileCwd"), /if \(!p\) return;\s*\n\s*if \(spawnGroupHolds\(\{ source: spawnWsSource, prefill: spawnWsPrefill, cwd: getCwd\(\), text: \$\("spawn-ws-input"\)\.value \}\)\) return;/);
});

test("+ CSS: hidden until hover or focus, always shown without hover", () => {
  const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => {
    const esc = sel.replace(/[.[\]#:()]/g, "\\$&").replace(/ /g, "\\s+");
    const m = new RegExp("(?:^|[}\\s])" + esc + "\\s*\\{([^}]*)\\}").exec(css);
    return m ? m[1] : null;
  };
  const base = rule("#sessions .wsh .wplus") || "";
  assert.match(base, /width:\s*22px/);
  assert.match(base, /height:\s*22px/);
  assert.match(base, /background:\s*transparent/);
  assert.match(base, /color:\s*var\(--muted\)/);
  assert.match(base, /opacity:\s*0/);
  assert.match(base, /flex:\s*none/);
  assert.match(base, /padding:\s*0/);
  assert.match(base, /border:\s*0/);
  assert.match(css, /#sessions \.wsh:hover \.wplus,\s*#sessions \.wsh:focus-within \.wplus\s*\{\s*opacity:\s*1;\s*\}/);
  const hov = rule("#sessions .wsh .wplus:hover") || "";
  assert.match(hov, /background:\s*var\(--line\)/);
  assert.match(hov, /color:\s*var\(--fg\)/);
  // the hover: none rule sits inside the desktop block (desktop-scope.test.js guards the block itself)
  const desktopAt = css.indexOf("@media (min-width: 701px)");
  const noHoverAt = css.indexOf("@media (hover: none)");
  const m = /@media \(hover: none\)\s*\{([^@]*?\})\s*\}/.exec(css);
  assert.ok(desktopAt >= 0 && noHoverAt > desktopAt, "nested after the desktop block opens");
  assert.match(m[1], /#sessions \.wsh \.wplus\s*\{\s*opacity:\s*1;\s*\}/);
});

// Task 6: only Mod+digit and the palette's digit hints skip a collapsed group. The palette's list, the
// rail, the attention chip and Mod+Shift+J keep every session.
test("Mod+digit numbers the visible rows: a collapsed group is skipped", () => {
  const src = read("app.js");
  const at = src.indexOf('bindShortcut("session.jump"');
  assert.ok(at >= 0, "session.jump handler");
  const handler = src.slice(at, src.indexOf("\n});", at));
  assert.ok(handler.includes("jumpOrder(lastSessions, ctx.mobile, deskWsCollapsed)"), "jump reads the visible order");
});

test("the palette lists every session and numbers only the visible ones", () => {
  const sess = (id, root) => ({ id, name: id, cwd: root, workspace_root: root });
  const ctx = vm.createContext({
    lastSessions: [sess("a1", "/r/a"), sess("a2", "/r/a"), sess("b1", "/r/b"), sess("b2", "/r/b")],
    jumpOrder: desk.jumpOrder,
    deskWsCollapsedSet: () => new Set(["/r/a"]),
    mobileActive: () => false,
    window: { innerWidth: 1280 },
    palReg: () => ({ jumpHint: (n) => "K" + n }),
    shortPath: (p) => p,
    palSessionDot: () => ({ cls: "dot", title: "" }),
    palSessionSub: () => "",
    palSessionMatchText: (s) => s.name,
    palSessionRun: () => () => {},
    isNestedShell: () => false,
    openTerminal: () => {},
    revealShell: () => {},
  });
  vm.runInContext(fnSrc(read("palette.js"), "paletteSessions") + "\nglobalThis.paletteSessions = paletteSessions;", ctx);
  const rows = ctx.paletteSessions(false);
  assert.deepStrictEqual(Array.from(rows, (r) => r.label), ["a1", "a2", "b1", "b2"], "every session, in group order");
  assert.deepStrictEqual(Array.from(rows.slice(2), (r) => r.hint), ["K1", "K2"], "visible rows are numbered from 1");
  assert.deepStrictEqual(Array.from(rows.slice(0, 2), (r) => r.hint), ["/r/a", "/r/a"], "collapsed rows fall back to their cwd");
});

test("the rail, the attention chip and Mod+Shift+J read the full order", () => {
  assert.ok(fnSrc(read("app.js"), "renderRail48").includes("jumpOrder(lastSessions, mobileActive(window.innerWidth))"));
  assert.ok(fnSrc(read("attnchip.js"), "attnChipSync").includes("jumpOrder(lastSessions, false)"));
  assert.ok(fnSrc(read("deskshell.js"), "nextNeedsYou").includes("jumpOrder(sessions, false)"));
  for (const [f, fn] of [["app.js", "renderRail48"], ["attnchip.js", "attnChipSync"], ["deskshell.js", "nextNeedsYou"]]) {
    assert.ok(!fnSrc(read(f), fn).includes("deskWsCollapsed"), fn + " does not read the collapse state");
  }
});
