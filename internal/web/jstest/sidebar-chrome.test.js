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
  const ctx = { $: () => el, elevatedCount: () => n, lastSessions: [], Date };
  vm.runInNewContext(fn(app, "paintSideElevated") + "\npaintSideElevated();", ctx);
  return el;
}

test("0 elevated is hidden, not shown as a count that says nothing", () => {
  const el = paintElevated(0);
  assert.equal(el.hidden, true);
  assert.equal(el.classList.contains("hot"), false);
});

test("N elevated shows in the red look while any session has a write window open", () => {
  for (const n of [1, 3]) {
    const el = paintElevated(n);
    assert.equal(el.hidden, false);
    assert.equal(el.textContent, n + " elevated");
    assert.equal(el.classList.contains("hot"), true);
  }
  const back = paintElevated(0); // a lapsed window clears on the next poll
  assert.equal(back.hidden, true);
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
    assert.equal(five.attrs["aria-label"], "5h usage 100%, 25m left");
    assert.equal(five.children.length, 4);
  }
});

test("99% is an ordinary row in the sidebar too; an unknown reset time says only limit reached", () => {
  const [ninety] = render({ five_hour: { used_percentage: 99, resets_at: NOW + 60 } }, true);
  assert.equal(ninety.children[2].hidden, false);
  assert.equal(ninety.children[3].textContent, "1m left");
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

test("the sidebar top's palette and collapse are kit ghost icon buttons; the palette is a search icon, not the Mac key", () => {
  const tag = (id) => new RegExp('<button id="' + id + '"[^>]*>[^<]*(?:<svg[\\s\\S]*?</svg>)?[^<]*</button>').exec(html);
  for (const id of ["side-palette", "side-collapse"]) {
    const m = tag(id);
    assert.ok(m, id + " not found");
    assert.match(m[0], /class="btn btn-ghost btn-icon btn-sm"/, id);
  }
  assert.ok(!/id="side-palette"[^>]*>[^<]*(&#8984;|⌘)/.test(html), "the palette button still draws the Mac key");
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
  sidebar.getBoundingClientRect = () => ({ left: 0, width });
  const log = { set: [], saved: [], fit: 0 };
  const ctx = {
    document: { getElementById: (id) => (id === "split-v" ? handle : sidebar), documentElement: { style: { setProperty() {} } } },
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
  return { handle, log, key };
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
  max.key("ArrowRight");
  assert.deepEqual(max.log.saved, [["agent-box:sidebarWidth", "560"]]);
  const min = wireSplitter(200);
  min.key("ArrowLeft");
  assert.deepEqual(min.log.saved, [["agent-box:sidebarWidth", "200"]]);
  const other = wireSplitter(290);
  for (const k of ["ArrowUp", "Enter", "Tab", "Home"]) assert.equal(other.key(k), false, k);
  assert.deepEqual(other.log.saved, []);
  assert.equal(other.log.fit, 0);
  // A chord such as Ctrl+Arrow is the browser's or the page's, not a resize.
  const chord = wireSplitter(290);
  assert.equal(chord.key("ArrowRight", { ctrlKey: true }), false);
  assert.deepEqual(chord.log.saved, []);
});

test("the splitter's current width is what it measures when no key has been pressed", () => {
  const s = wireSplitter(333);
  assert.equal(s.handle.attrs["aria-valuenow"], "333");
});

// ---- #sessions .kbtn: the row's ⋯ takes the kit look (Plan 1 follow-up 1) ------------------------------
test("the desktop row's ⋯ is a kit ghost icon button; the phone row's is not touched", () => {
  const compact = fn(app, "paintCompactRow");
  assert.match(compact, /kb\.className = "kbtn btn btn-ghost btn-icon btn-sm"/);
  const phone = app.slice(app.indexOf('kb.className = "kbtn";'));
  assert.ok(phone.includes('kb.className = "kbtn";'), "the phone row keeps the bare kbtn");
});
