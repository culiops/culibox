"use strict";
// Task 6 of UI plan 2 (#172): the desktop sidebar chrome as frames m1-1 / m1-2 draw it. app.js and
// shell.js are classic page scripts that cannot be required, so each function is cut out of its
// source and run against tiny stand-ins (the pattern of railmeters.test.js and quotaage.test.js);
// the markup and the stylesheet are read as text.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const SC = require("../static/shortcuts.js");
const { meterModel } = require("../static/usage.js");
const { acctWinAge, acctAgeNote } = require("../static/accounts.js");
const DS = require("../static/drawersize.js");
const { railItems } = require("../static/steerrail.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const app = read("app.js");
const shell = read("shell.js");
const html = read("index.html");

// fn(src, name): the text of a top-level function, from its "function name(" to the closing "}" at column 0.
function fn(src, name) {
  const start = src.indexOf("function " + name + "(");
  assert.ok(start >= 0, name + " not found");
  return src.slice(start, src.indexOf("\n}\n", start) + 3);
}

class El {
  constructor(tag) {
    this.tag = tag; this.children = []; this.className = ""; this.text = ""; this.attrs = {}; this.style = {};
    this.hidden = false; this.title = ""; this.dataset = {}; this.handlers = {};
    const self = this;
    this.classList = {
      add: (c) => { if (!self.classList.contains(c)) self.className = (self.className + " " + c).trim(); },
      contains: (c) => self.className.split(/\s+/).includes(c),
      toggle: (c, on) => {
        const has = self.classList.contains(c);
        if (on && !has) self.classList.add(c);
        if (!on && has) self.className = self.className.split(/\s+/).filter((x) => x !== c).join(" ");
      },
    };
  }
  set textContent(v) { this.text = String(v); }
  get textContent() { return this.text; }
  append(...cs) { cs.forEach((c) => this.children.push(c)); }
  appendChild(c) { this.children.push(c); return c; }
  replaceChildren(...cs) { this.children = cs; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  addEventListener(t, f) { this.handlers[t] = f; }
}

// ---- F31: the footer hides "0 elevated" and says "N elevated" in red ----------------------------
function paintElevated(n) {
  const el = new El("span");
  el.hidden = false; el.text = "stale";
  const document = { createElement: (t) => new El(t), createTextNode: (t) => ({ textContent: t }) };
  const ctx = { $: () => el, elevatedCount: () => n, lastSessions: [], Date, document };
  vm.runInNewContext(fn(app, "paintSideElevated") + "\npaintSideElevated();", ctx);
  return el;
}
// What the footer reads: its children's text, in order.
const footText = (el) => el.children.map((c) => c.textContent).join("");

test("0 elevated is hidden, not shown as a count that says nothing", () => {
  const el = paintElevated(0);
  assert.equal(el.hidden, true);
  assert.equal(el.classList.contains("hot"), false);
});

test("N elevated shows in the red look while any session has a write window open", () => {
  for (const n of [1, 3]) {
    const el = paintElevated(n);
    assert.equal(el.hidden, false);
    assert.equal(footText(el), "● " + n + " elevated", "as M6.4 draws it: ● then the count");
    assert.equal(el.classList.contains("hot"), true);
    // The ● is decoration: a screen reader says "N elevated" and not "black circle".
    const [dot] = el.children;
    assert.equal(dot.tag, "span");
    assert.equal(dot.attrs["aria-hidden"], "true");
    assert.equal(dot.textContent, "●");
  }
  const back = paintElevated(0); // a lapsed window clears on the next poll
  assert.equal(back.hidden, true);
  assert.equal(footText(back), "", "nothing left to read, not even the dot");
});

// ---- F32: the quota row at 100% says "limit reached" (desktop sidebar only) -----------------------
const NOW = 1_700_000_000;
function render(rl, limitLine) {
  const host = new El("div");
  const ctx = { document: { createElement: (t) => new El(t) }, Date: { now: () => NOW * 1000 }, Math, meterModel, acctWinAge, acctAgeNote };
  vm.runInNewContext(fn(app, "renderRateLimitsInto") + "\nthis.out = renderRateLimitsInto(host, rl, limitLine);", Object.assign(ctx, { host, rl, limitLine }));
  return host.children;
}
const full = { updated_at: NOW - 30, five_hour: { used_percentage: 100, resets_at: NOW + 25 * 60 }, seven_day: { used_percentage: 78, resets_at: NOW + 3 * 86400 } };

test("the sidebar's 100% row hides the percentage and says limit reached with the reset", () => {
  const [five, week] = render(full, true);
  const [, , pc, rs] = five.children;
  assert.equal(pc.className, "pc");
  assert.equal(pc.hidden, true);
  assert.equal(rs.textContent, "limit reached · resets 25m");
  assert.ok(rs.classList.contains("limit"));
  assert.match(five.attrs["aria-label"], /limit reached · resets 25m/);
  // The text can be cut at a narrow sidebar: the full line is also the element's title.
  assert.equal(rs.title, "limit reached · resets 25m");
  // The other window is an ordinary row.
  assert.equal(week.children[2].hidden, false);
  assert.equal(week.children[2].textContent, "78%");
  assert.equal(week.children[3].textContent, "3d 0h left");
  assert.ok(!week.children[3].classList.contains("limit"));
});

test("the same 100% reading anywhere else (header, phone band) keeps its percentage and countdown", () => {
  for (const flag of [false, undefined]) {
    const [five] = render(full, flag);
    const [, , pc, rs] = five.children;
    assert.equal(pc.hidden, false);
    assert.equal(pc.textContent, "100%");
    assert.equal(rs.textContent, "25m left");
    assert.equal(rs.className, "rs");
    assert.equal(rs.title, "", "no title on an ordinary countdown");
    assert.equal(five.attrs["aria-label"], "5h usage 100%, 25m left");
    assert.equal(five.children.length, 4);
  }
});

test("99% and 99.5% are ordinary rows in the sidebar too; an unknown reset time says only limit reached", () => {
  const [ninety] = render({ five_hour: { used_percentage: 99, resets_at: NOW + 60 } }, true);
  assert.equal(ninety.children[2].hidden, false);
  assert.equal(ninety.children[3].textContent, "1m left");
  // 99.5 is drawn "100%" but is not the limit: no limit line.
  const [almost] = render({ five_hour: { used_percentage: 99.5, resets_at: NOW + 25 * 60 } }, true);
  assert.equal(almost.children[2].hidden, false);
  assert.equal(almost.children[2].textContent, "100%");
  assert.equal(almost.children[3].textContent, "25m left");
  assert.equal(almost.children[3].className, "rs");
  assert.equal(almost.children[3].title, "");
  const [noReset] = render({ five_hour: { used_percentage: 100, resets_at: 0 } }, true);
  assert.equal(noReset.children[3].textContent, "limit reached");
});

test("only the desktop sidebar's host is asked for the limit line", () => {
  const body = fn(app, "renderRateLimits");
  assert.match(body, /renderRateLimitsInto\(host, lims, id === "limits-side"\)/);
});

test("the limit line is --fg at weight 600, and nothing amber or warn", () => {
  const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const m = /#limits-side \.mtr \.limit\s*\{([^}]*)\}/.exec(css);
  assert.ok(m, "no #limits-side .mtr .limit rule");
  assert.match(m[1], /color:\s*var\(--fg\)/);
  assert.match(m[1], /font-weight:\s*(var\(--w-semibold\)|600)/);
  assert.ok(!/amber|warn/.test(m[1]));
});

// ---- F33: the palette and collapse buttons name the real chord -------------------------------------
function paintHints(mac) {
  const els = { "side-palette": new El("button"), "side-collapse": new El("button") };
  const ctx = { $: (id) => els[id], hintTitle: SC.hintTitle, IS_MAC: mac };
  vm.runInNewContext(fn(app, "paintSideHints") + "\npaintSideHints();", ctx);
  return els;
}

test("the palette and collapse buttons carry the chord of this platform as title and name", () => {
  const lin = paintHints(false);
  assert.equal(lin["side-palette"].title, "Command palette (Ctrl+Shift+K)");
  assert.equal(lin["side-palette"].attrs["aria-label"], "Command palette (Ctrl+Shift+K)");
  assert.equal(lin["side-collapse"].title, "Hide sidebar (Ctrl+Shift+B)");
  assert.equal(lin["side-collapse"].attrs["aria-label"], "Hide sidebar (Ctrl+Shift+B)");
  const mac = paintHints(true);
  assert.equal(mac["side-palette"].title, "Command palette (⇧⌘K)");
  assert.equal(mac["side-collapse"].title, "Hide sidebar (⇧⌘B)");
});

test("boot paints those titles once, after the sidebar markup exists", () => {
  assert.match(app, /^paintSideHints\(\);$/m);
});

test("the sidebar top's palette and collapse are kit ghost icon buttons; the palette draws a magnifier, not a glyph", () => {
  const tag = (id) => new RegExp('<button id="' + id + '"[^>]*>[\\s\\S]*?</button>').exec(html);
  for (const id of ["side-palette", "side-collapse"]) {
    const m = tag(id);
    assert.ok(m, id + " not found");
    assert.match(m[0], /class="btn btn-ghost btn-icon btn-sm"/, id);
  }
  const pal = tag("side-palette")[0];
  // The tabs.js tabIcon style: 14x14 viewBox, no fill, currentColor, 1.2 stroke, round caps and joins, hidden from AT.
  const svg = /<svg[^>]*>[\s\S]*?<\/svg>/.exec(pal);
  assert.ok(svg, "the palette button draws an inline svg");
  for (const a of ['viewBox="0 0 14 14"', 'aria-hidden="true"', 'fill="none"', 'stroke="currentColor"', 'stroke-width="1.2"',
    'stroke-linecap="round"', 'stroke-linejoin="round"']) assert.ok(svg[0].includes(a), a);
  assert.match(svg[0], /<circle /);
  assert.ok(!/&#8984;|⌘|&#8981;|⌕/.test(pal), "no font glyph is left in the palette button");
  assert.match(tag("side-collapse")[0], /&#171;/, "the « stays the frame's text glyph");
  // #340 S6: the layouts row is gone; New session shares one row with the layout button, the palette and «.
  const row = /<div class="xnewrow">[\s\S]*?\n {8}<\/div>/.exec(html);
  assert.ok(row, "div.xnewrow exists");
  assert.match(row[0], /<button [^>]*class="btn btn-ghost btn-icon btn-sm xlay"/);
  assert.ok(!html.includes('id="side-layouts"'), "no #side-layouts");
  assert.ok(!html.includes('class="side-ctl"'), "no .side-ctl");
  const top = html.slice(html.indexOf('<div id="side-top">'), html.indexOf('<div id="sys-banner-slot"'));
  const at = ["id=\"side-new\"", "xlay\"", "id=\"side-palette\"", "id=\"side-collapse\""].map((s) => top.indexOf(s));
  assert.ok(at.every((i) => i > 0), "all four are in #side-top");
  assert.deepEqual([...at].sort((a, b) => a - b), at, "New session, layout, palette, collapse");
});

// ---- F57: the rail, top to bottom: logo, » toggle, +, dots, meters ----------------------------------
function paintRail() {
  const rail = new El("nav");
  const mark = (n) => { const e = new El("span"); e.className = n; return e; };
  const ctx = {
    $: () => rail, document: { createElement: (t) => new El(t) }, window: { innerWidth: 1440 },
    railItems, jumpOrder: (l) => l, lastSessions: [{ id: "a", name: "alpha", activity: "working" }, { id: "b", name: "beta", activity: "idle" }],
    mobileActive: () => false, selectedSessionId: () => "", IS_MAC: false,
    railLogo: () => mark("rail-logo"), railMeters: () => mark("rail-meters"), paintRailMeters() {},
    toggleSidebar() {}, openSpawnDialog() {}, openTerminal() {},
  };
  vm.runInNewContext('let rail48Key = "";\n' + fn(app, "renderRail48") + "\nrenderRail48();", ctx);
  return rail.children;
}

test("the rail's expand control sits at the top where the sidebar's collapse sits", () => {
  const kids = paintRail();
  const cls = (e) => e.className.split(/\s+/);
  assert.deepEqual(kids.map((e) => (e.tag === "button" ? cls(e).find((c) => ["tog", "add"].includes(c)) : e.className.split(" ")[0])),
    ["rail-logo", "tog", "add", "rin", "rail-meters"]);
  const [, tog, add, dots] = kids;
  assert.equal(tog.textContent, "»");
  assert.equal(tog.title, "Show sidebar (Ctrl+Shift+B)");
  assert.equal(tog.attrs["aria-label"], "Show sidebar (Ctrl+Shift+B)");
  assert.equal(add.title, "New session");
  assert.equal(dots.children.length, 2);
});

test("the rail toggle is the sidebar collapse's twin, and toggleSidebar still hands focus between them", () => {
  const body = fn(app, "toggleSidebar");
  assert.match(body, /rail\.querySelector\("\.ri\.tog"\)/);
  assert.match(body, /\$\("side-collapse"\)/);
  assert.match(fn(app, "renderRail48"), /mac: IS_MAC/);
});

// ---- F71: #split-v is a keyboard separator ------------------------------------------------------------
test("#split-v is a focusable vertical separator with its bounds and a name", () => {
  const m = /<div id="split-v"[^>]*>/.exec(html);
  assert.ok(m, "#split-v not found");
  for (const a of ['role="separator"', 'aria-orientation="vertical"', 'aria-label="Resize sidebar"', 'tabindex="0"',
    'aria-valuemin="200"', 'aria-valuemax="560"', 'aria-valuenow="290"']) assert.ok(m[0].includes(a), a);
});

function wireSplitter(width) {
  const handle = new El("div");
  const sidebar = new El("aside");
  // The drawn width follows --sidebar-w once a step sets it, as it does in the page.
  sidebar.getBoundingClientRect = () => ({ left: 0, width });
  const log = { set: [], saved: [], fit: 0 };
  const ctx = {
    document: {
      getElementById: (id) => (id === "split-v" ? handle : sidebar),
      documentElement: { style: { setProperty: (k, v) => { if (k === "--sidebar-w") width = parseFloat(v); } } },
    },
    stepWidth: DS.stepWidth, widthKeyDir: DS.widthKeyDir,
    fitAll() { log.fit++; }, localStorage: { setItem: (k, v) => log.saved.push([k, v]) },
  };
  // setSidebarWidth + persistSidebarWidth + the constants, then the splitter itself.
  const head = shell.slice(shell.indexOf("const WIDTH_KEY"), shell.indexOf("// minHeightOf"));
  vm.runInNewContext(head + "\n" + fn(shell, "initWidthSplitter") + "\ninitWidthSplitter();", ctx);
  const key = (k, mods) => {
    let prevented = false;
    handle.handlers.keydown(Object.assign({ key: k, preventDefault() { prevented = true; } }, mods));
    return prevented;
  };
  return { handle, log, key, sidebar, setWidth: (w) => { width = w; } };
}

test("ArrowRight widens the sidebar by 16px and ArrowLeft narrows it; the width is kept and the terminals refit", () => {
  const s = wireSplitter(290);
  assert.equal(s.key("ArrowRight"), true);
  assert.deepEqual(s.log.saved, [["agent-box:sidebarWidth", "306"]]);
  assert.equal(s.handle.attrs["aria-valuenow"], "306");
  assert.equal(s.log.fit, 1);
  const l = wireSplitter(290);
  assert.equal(l.key("ArrowLeft"), true);
  assert.deepEqual(l.log.saved, [["agent-box:sidebarWidth", "274"]]);
  assert.equal(l.handle.attrs["aria-valuenow"], "274");
});

test("the keys stop at 200 and 560, and any other key is left alone", () => {
  const max = wireSplitter(560);
  assert.equal(max.key("ArrowRight"), true, "the key is still the splitter's: the page does not scroll");
  assert.deepEqual(max.log.saved, [], "a press at the clamp changes nothing, so nothing is written");
  assert.equal(max.log.fit, 0, "and the terminals do not refit");
  const min = wireSplitter(200);
  assert.equal(min.key("ArrowLeft"), true);
  assert.deepEqual(min.log.saved, []);
  assert.equal(min.log.fit, 0);
  // Steps that land on a bound still count: 550 -> 560 moves, and the next press at 560 does not.
  const edge = wireSplitter(550);
  edge.key("ArrowRight");
  assert.deepEqual(edge.log.saved, [["agent-box:sidebarWidth", "560"]]);
  assert.equal(edge.log.fit, 1);
  const other = wireSplitter(290);
  for (const k of ["ArrowUp", "Enter", "Tab", "Home"]) assert.equal(other.key(k), false, k);
  assert.deepEqual(other.log.saved, []);
  assert.equal(other.log.fit, 0);
  // A chord such as Ctrl+Arrow is the browser's or the page's, not a resize.
  const chord = wireSplitter(290);
  assert.equal(chord.key("ArrowRight", { ctrlKey: true }), false);
  assert.deepEqual(chord.log.saved, []);
});

test("a held key stops writing once the sidebar is at the clamp", () => {
  const s = wireSplitter(540);
  for (let i = 0; i < 6; i++) s.key("ArrowRight"); // 556, 560, then four repeats at 560
  assert.deepEqual(s.log.saved, [["agent-box:sidebarWidth", "556"], ["agent-box:sidebarWidth", "560"]]);
  assert.equal(s.log.fit, 2);
  assert.equal(s.handle.attrs["aria-valuenow"], "560");
  const l = wireSplitter(220);
  for (let i = 0; i < 4; i++) l.key("ArrowLeft"); // 204, 200, then two repeats at 200
  assert.equal(l.log.saved.length, 2);
  assert.equal(l.log.fit, 2);
});

test("the splitter's current width is what it measures when no key has been pressed", () => {
  const s = wireSplitter(333);
  assert.equal(s.handle.attrs["aria-valuenow"], "333");
});

test("a page loaded with the sidebar hidden does not keep announcing the static 290 once the handle is focused", () => {
  const s = wireSplitter(0); // hidden: drawn width 0, nothing to read yet
  assert.equal(s.handle.attrs["aria-valuenow"], undefined);
  s.setWidth(333); // the sidebar is shown at its stored width
  assert.equal(typeof s.handle.handlers.focus, "function", "the handle resyncs when it takes focus");
  s.handle.handlers.focus({});
  assert.equal(s.handle.attrs["aria-valuenow"], "333");
  s.setWidth(0); // hidden again: a focus on a zero-width handle leaves the last value
  s.handle.handlers.focus({});
  assert.equal(s.handle.attrs["aria-valuenow"], "333");
});

test("toggleSidebar's show path resyncs the splitter's aria-valuenow from the drawn width; the hide path does not", () => {
  const handle = new El("div");
  handle.attrs["aria-valuenow"] = "290"; // index.html's static value
  const sidebar = new El("aside");
  let hidden = true, drawn = 0;
  sidebar.getBoundingClientRect = () => ({ left: 0, width: hidden ? 0 : drawn });
  const root = { classList: { toggle: (c) => { hidden = !hidden; return hidden; } } };
  const els = { "split-v": handle, sidebar, rail48: null, "sidebar-toggle": null, "side-collapse": null };
  const ctx = {
    window: { innerWidth: 1440 }, mobileActive: () => false, localStorage: { setItem() {} },
    document: { activeElement: null, documentElement: root, getElementById: (id) => els[id] || null },
    $: (id) => els[id] || null, renderGrid() {}, renderRail48() {},
  };
  const head = shell.slice(shell.indexOf("function syncSplitterAria"), shell.indexOf("function persistSidebarWidth"));
  vm.runInNewContext(head + "\n" + fn(app, "toggleSidebar") + "\nglobalThis.t = toggleSidebar;", ctx);
  drawn = 412;
  ctx.t(); // hidden -> shown
  assert.equal(hidden, false);
  assert.equal(handle.attrs["aria-valuenow"], "412");
  drawn = 500;
  ctx.t(); // shown -> hidden: the sidebar is not drawn, nothing to read
  assert.equal(hidden, true);
  assert.equal(handle.attrs["aria-valuenow"], "412");
});

// ---- #sessions .kbtn: the row's ⋯ takes the kit look (Plan 1 follow-up 1) ------------------------------
test("the desktop row's ⋯ is a kit ghost icon button; the phone row's is not touched", () => {
  const compact = fn(app, "paintCompactRow");
  assert.match(compact, /kb\.className = "kbtn btn btn-ghost btn-icon btn-sm"/);
  const phone = app.slice(app.indexOf('kb.className = "kbtn";'));
  assert.ok(phone.includes('kb.className = "kbtn";'), "the phone row keeps the bare kbtn");
});

// ---- task 9b: the ask on the row reads "Asks to run" + the call in mono -------------------------------
// paintCompactRow is cut out and run against the stand-ins it reads; rowSubline is the real one.
function paintRow(s, over) {
  const D = require("../static/deskshell.js");
  const li = new El("li");
  li.classList.add("drow");
  if (over && over.nested) li.classList.add("nested");
  const sname = new El("div"), grow = new El("div");
  const ctx = {
    document: { createElement: (t) => new El(t) }, Date, boxRuntime: "", window: { HOME_DIR: "" },
    rowChip: () => null, acctBadge: () => null, awsBadge: () => null, fmtSessionCost: () => "", tierBadge: () => null,
    rowSubline: D.rowSubline, rowMenuOpenFor: () => false, openSessionMenu: () => {}, openTerminal: () => {}, openPaneView: () => false, wmCopyLine: () => "", sessionWorkHome: () => "",
  };
  vm.runInNewContext(fn(app, "paintCompactRow") + "\npaintCompactRow(li, sname, grow, s, {});", Object.assign(ctx, { li, sname, grow, s }, over && over.ctx));
  return { li, sname, grow, sub: grow.children[0] };
}
const ASKING = { id: "a", name: "a", activity: "waiting", need: "ask", ask: "Bash(terraform plan -out=tf.plan)" };

// The desktop row draws no per-session cost, as the M1 frames draw it (founder 2026-11-08); the
// cost stays in the pane header's meta and in Analytics.
test("the desktop row shows no session cost, even when the server sends one", () => {
  const { sname } = paintRow({ id: "c", name: "c", activity: "working", cost_usd: 3.12 }, { ctx: { fmtSessionCost: () => "$3.12" } });
  const all = [];
  const walk = (n) => { if (n && typeof n === "object") { all.push(n); (n.children || []).forEach(walk); } };
  walk(sname);
  assert.ok(!all.some((n) => /\bdcost\b/.test(n.className || "")), "no .dcost element");
  assert.ok(!all.some((n) => /\$/.test(n.textContent || "")), "no $ text in the row name line");
});

test("an ask's sub-line is amber 'Asks to run ' and the command alone in a mono span", () => {
  const { sub } = paintRow(ASKING);
  assert.equal(sub.className, "sub amber");
  assert.equal(sub.children.length, 2);
  assert.equal(sub.children[0], "Asks to run ");
  assert.equal(sub.children[1].className, "mono");
  assert.equal(sub.children[1].textContent, "terraform plan -out=tf.plan");
  assert.equal(sub.text, "", "no textContent beside the parts");
});

test("another tool's ask reads Asks to run Tool(args), the call mono; a plain sub-line is still one text", () => {
  const rd = paintRow({ ...ASKING, ask: "Read(/etc/hosts)" }).sub;
  assert.equal(rd.children[0], "Asks to run ");
  assert.equal(rd.children[1].textContent, "Read(/etc/hosts)");
  const plain = paintRow({ id: "w", name: "w", activity: "working", summary: "fixing tests" }).sub;
  assert.equal(plain.className, "sub");
  assert.equal(plain.textContent, "fixing tests");
  assert.equal(plain.children.length, 0);
});

test("a sub-line click still opens the terminal (the span is inside the clickable line)", () => {
  const opened = [];
  const { sub } = paintRow(ASKING, { ctx: { openTerminal: (id, name) => opened.push([id, name]) } });
  sub.onclick();
  assert.deepEqual(opened, [["a", "a"]]);
});

test("the row's ⋯ is built collapsed, and expanded when it is the opener of the open menu (task 9b)", () => {
  const kbOf = (r) => r.sname.children.find((c) => c.className.includes("kbtn"));
  assert.equal(kbOf(paintRow(ASKING)).getAttribute("aria-expanded"), "false");
  assert.equal(kbOf(paintRow(ASKING, { ctx: { rowMenuOpenFor: (id) => id === "a" } })).getAttribute("aria-expanded"), "true");
  assert.equal(kbOf(paintRow(ASKING)).getAttribute("aria-haspopup"), "menu");
});
