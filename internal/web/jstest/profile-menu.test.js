"use strict";
// The profile page's ⋯ menu (frame M5.4) and its "+ Add" pickers are the kit menu on desktop (#173 task 2):
// profilepage.js's pfvMoreMenu builds `.menu` > kitMenuButton rows (Duplicate…, a separator, a red Delete…),
// pfv-cloud.js's pfcPicker draws `.pfv-pick.menu` with `.menu-label.grp-label` headings and `.menu-item.pfv-opt`
// options. The repo has no jsdom, so both scripts run whole in a vm against a small stand-in document (focus
// obeys what a browser's does: a control inside a hidden menu takes none), with kitmenu.js beside them. The
// picker's behaviour (filter, Added, picking) is profilepage.test.js's; this file is the menu, and the markup
// and CSS both of them now share with the rest of the kit.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

class Text { constructor(t) { this.text = String(t); this.parent = null; } get textContent() { return this.text; } }
class Node {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.dataset = {}; this.hidden = false; this.disabled = false; this.handlers = {};
    this.style = { setProperty() {} };
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "class") this.className = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  appendChild(c) { if (typeof c === "string") c = new Text(c); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  addEventListener(ev, fn) { (this.handlers[ev] = this.handlers[ev] || []).push(fn); }
  contains(n) { for (let x = n; x; x = x.parent) if (x === this) return true; return false; }
  // As a browser: only a control takes focus, and not one that is disabled or inside a hidden subtree.
  focus() {
    if (!["input", "select", "textarea", "button"].includes(this.tag) || this.disabled) return;
    for (let x = this; x; x = x.parent) if (x.hidden) return;
    Node.focused = this;
  }
  click() { if (this.onclick) return this.onclick({ target: this }); }
  all(pred, out = []) { for (const c of this.children) { if (!(c instanceof Node)) continue; if (pred(c)) out.push(c); c.all(pred, out); } return out; }
  // The selectors kitMenuKeys reads: a comma list of `.class` with an optional :not([aria-disabled="true"]). Any
  // other selector is one the real page may read differently, so it fails loudly.
  querySelectorAll(sel) {
    const wanted = sel.split(",").map((p) => p.trim());
    for (const p of wanted) assert.match(p, /^\.[\w-]+(:not\(\[aria-disabled="true"\]\))?$/, "unexpected selector part " + p);
    return this.all((n) => wanted.some((p) => {
      const cls = /^\.([\w-]+)/.exec(p)[1];
      if (!n.className.split(/\s+/).includes(cls)) return false;
      return !(p.includes(':not([aria-disabled="true"])') && n.attrs["aria-disabled"] === "true");
    }));
  }
}
const hasCls = (cls) => (n) => n.className.split(/\s+/).includes(cls);
const byCls = (root, cls) => root.all(hasCls(cls));

function world() {
  const calls = [];
  const listeners = {};
  const doc = {
    createElement: (t) => new Node(t), createElementNS: (_ns, t) => new Node(t), createTextNode: (t) => new Text(t),
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    get activeElement() { return Node.focused; },
  };
  Node.focused = null;
  // tabs.js draws the dock's and the pane header's glyphs; one tagged shape stands in for each here.
  const icon = (k) => { const s = new Node("svg"); s.setAttribute("data-kind", k); return s; };
  const ctx = vm.createContext({
    document: doc, console, Promise, JSON, Math, Array, Object, Set, Map, String, Number, Date, setTimeout, clearTimeout,
    tabIcon: icon, dockIcon: icon, pageLeaveHook: null, window: { HOME_DIR: "/home/you" },
  });
  for (const f of ["svgicon.js", "kitmenu.js", "profilepage.js", "pfv-cloud.js"]) new vm.Script(read(f), { filename: f }).runInContext(ctx);
  // The two things a menu row hands over to (profilepage.js's own, replaced so the click is what is measured).
  // Where focus is at the moment each runs: Delete… opens a confirm that hands focus back to whatever had it when it opened.
  const focusAt = [];
  ctx.pfvDuplicate = () => { calls.push("duplicate"); focusAt.push(activeNow()); };
  ctx.pfvDelete = () => { calls.push("delete"); focusAt.push(activeNow()); };
  const docClick = (target) => { for (const f of listeners.click || []) f({ target }); };
  const key = (node, k) => {
    // A keydown bubbles: the wrapper's listener hears it from the row or the ⋯ that has focus.
    const ev = { key: k, target: node, prevented: 0, preventDefault() { this.prevented++; } };
    for (let n = node; n; n = n.parent) for (const f of (n.handlers.keydown || [])) f(ev);
    return ev;
  };
  return { ctx, calls, focusAt, docClick, key };
}

// document.activeElement as a browser has it: a focused control that is hidden (its menu closed under it) gives focus
// up to the body (null here), which is how a keyboard user loses their place.
function activeNow() {
  for (let x = Node.focused; x; x = x.parent) if (x.hidden) return null;
  return Node.focused;
}

// The ⋯ wrapper, as the header draws it: [trigger, menu].
function more(name) {
  const w = world();
  const wrap = w.ctx.pfvMoreMenu(name || "agent-box");
  const [btn, pop] = wrap.children;
  return Object.assign(w, { wrap, btn, pop, items: byCls(pop, "menu-item") });
}

// ---- the menu: frame M5.4 ---------------------------------------------------------------------------------------------------

test("pfvMoreMenu: the kit menu, exactly Duplicate…, a separator and a red Delete…, closed until the ⋯ opens it", () => {
  const { wrap, btn, pop } = more("agent-box");
  assert.strictEqual(wrap.className, "pfv-more");
  assert.deepStrictEqual(wrap.children.map((c) => c.tag), ["button", "div"]);
  assert.strictEqual(pop.className, "menu", "the kit's .menu, not the page's own popover");
  assert.strictEqual(pop.getAttribute("role"), "menu");
  assert.strictEqual(pop.getAttribute("aria-label"), "agent-box profile actions");
  assert.strictEqual(pop.hidden, true);
  assert.deepStrictEqual(pop.children.map((c) => [c.tag, c.className, c.getAttribute("role")]), [
    ["button", "menu-item", "menuitem"],
    ["div", "menu-sep", "separator"],
    ["button", "menu-item is-danger", "menuitem"],
  ]);
  assert.deepStrictEqual(pop.children.filter((c) => c.tag === "button").map((c) => byCls(c, "menu-text")[0].textContent), ["Duplicate…", "Delete…"]);
  // Each row is .menu-icon (an inline SVG, aria-hidden slot) + .menu-text, as kitMenuButton draws it.
  assert.deepStrictEqual(pop.children[0].children.map((c) => c.className), ["menu-icon", "menu-text"]);
  assert.deepStrictEqual(byCls(pop, "menu-icon").map((s) => s.children[0].getAttribute("data-icon")), ["duplicate", "delete"]);
  assert.strictEqual(btn.getAttribute("aria-haspopup"), "menu");
  assert.strictEqual(btn.getAttribute("aria-expanded"), "false");
  assert.strictEqual(btn.getAttribute("aria-label"), "More actions for agent-box");
  assert.strictEqual(btn.getAttribute("title"), "More actions", "frame M5.4: the trigger is titled More actions");
});

test("pfvMoreMenu: no leftover page-menu classes", () => {
  const { wrap } = more();
  const classes = [wrap, ...wrap.all(() => true)].flatMap((n) => n.className.split(/\s+/)).filter(Boolean);
  assert.deepStrictEqual(classes.filter((c) => /^pfv-menu/.test(c)), []);
});

test("opening sets aria-expanded and focuses Duplicate…; a second press closes it", () => {
  const { btn, pop, items } = more();
  btn.click();
  assert.strictEqual(pop.hidden, false);
  assert.strictEqual(btn.getAttribute("aria-expanded"), "true");
  assert.strictEqual(Node.focused, items[0], "focus is on Duplicate…, in the open menu");
  btn.click();
  assert.strictEqual(pop.hidden, true);
  assert.strictEqual(btn.getAttribute("aria-expanded"), "false");
});

test("Escape closes the menu, sets aria-expanded false and gives focus back to the ⋯; the key is the menu's", () => {
  const { btn, pop, items, key } = more();
  btn.click();
  const ev = key(items[0], "Escape");
  assert.strictEqual(ev.prevented, 1, "default prevented: the page's own Esc must not close the page too");
  assert.strictEqual(pop.hidden, true);
  assert.strictEqual(btn.getAttribute("aria-expanded"), "false");
  assert.strictEqual(Node.focused, btn);
});

test("Escape with the menu closed is not handled (it is the page's)", () => {
  const { btn, key } = more();
  btn.focus();
  const ev = key(btn, "Escape");
  assert.strictEqual(ev.prevented, 0);
});

test("arrow keys: Down from Duplicate… lands on Delete… (the separator is no stop), and wraps; Up, Home and End too", () => {
  const { btn, items, key } = more();
  btn.click();
  assert.strictEqual(Node.focused, items[0]);
  assert.strictEqual(key(items[0], "ArrowDown").prevented, 1);
  assert.strictEqual(Node.focused, items[1], "Delete…");
  key(items[1], "ArrowDown");
  assert.strictEqual(Node.focused, items[0], "wraps to Duplicate…");
  key(items[0], "ArrowUp");
  assert.strictEqual(Node.focused, items[1], "Up wraps to the last row");
  key(items[1], "Home");
  assert.strictEqual(Node.focused, items[0]);
  key(items[0], "End");
  assert.strictEqual(Node.focused, items[1]);
});

test("arrow keys with the menu closed keep their default (the ⋯ does not steal them)", () => {
  const { btn, key } = more();
  btn.focus();
  assert.strictEqual(key(btn, "ArrowDown").prevented, 0);
  assert.strictEqual(Node.focused, btn);
});

test("a click outside closes the menu and leaves focus where the click put it; a click inside does not", () => {
  const { btn, wrap, pop, items, docClick } = more();
  btn.click();
  docClick(items[1]);
  assert.strictEqual(pop.hidden, false, "inside the wrapper: not outside");
  const elsewhere = new Node("button");
  elsewhere.focus();
  docClick(elsewhere);
  assert.strictEqual(pop.hidden, true);
  assert.strictEqual(btn.getAttribute("aria-expanded"), "false");
  assert.strictEqual(Node.focused, elsewhere, "not pulled back to the ⋯");
  assert.ok(wrap.contains(btn));
});

test("Duplicate… and Delete… close the menu and run what they ran before", () => {
  const w = more();
  w.btn.click();
  w.items[0].click();
  assert.deepStrictEqual(w.calls, ["duplicate"]);
  assert.strictEqual(w.pop.hidden, true);
  assert.strictEqual(w.btn.getAttribute("aria-expanded"), "false");
  w.btn.click();
  w.items[1].click();
  assert.deepStrictEqual(w.calls, ["duplicate", "delete"]);
  assert.strictEqual(w.pop.hidden, true);
});

// Review M3: the menu used to close with focus still on the row it was hiding, so focus fell to the body and a cancelled
// Delete… confirm had nothing to hand focus back to. It now closes with focus on the ⋯ first, then acts.
test("Duplicate… and Delete…, activated from the keyboard, leave focus on the ⋯ (not the body) before they run", () => {
  const w = more();
  w.btn.click();
  assert.strictEqual(activeNow(), w.items[0]);
  w.items[0].click(); // Enter on Duplicate…
  assert.deepStrictEqual(w.focusAt, [w.btn], "focus was on the ⋯ when Duplicate… ran");
  assert.strictEqual(activeNow(), w.btn);
  w.btn.click();
  w.key(w.items[0], "ArrowDown");
  assert.strictEqual(activeNow(), w.items[1], "Delete…");
  w.items[1].click(); // Enter on Delete…
  assert.deepStrictEqual(w.focusAt, [w.btn, w.btn], "and on the ⋯ when Delete… ran, so a cancelled confirm lands on the ⋯");
  assert.strictEqual(activeNow(), w.btn);
  assert.strictEqual(w.pop.hidden, true);
  assert.strictEqual(w.btn.getAttribute("aria-expanded"), "false");
});

// ---- the "+ Add" picker ------------------------------------------------------------------------------------------------------

function picker(spec) {
  const w = world();
  const rec = { ui: {}, fields: {}, first: null };
  const pk = w.ctx.pfcPicker(rec, spec || {
    label: "Add access", filter: "Filter connections and tokens",
    groups: [
      { title: "AWS connections", items: [
        { title: "prod-read", tag: "PROD", sub: "Read only", add: { kind: "aws", name: "prod-read" } },
        { title: "sandbox", sub: "Read only", added: true },
        { title: "bare", add: { kind: "aws", name: "bare" } },
      ] },
      { title: "Tokens", note: "Tokens are long-lived, and the agent can read them while it runs.", items: [
        { title: "New token…", create: "token" },
      ] },
    ],
  });
  return Object.assign(w, { rec, pk, pop: pk.node, opts: byCls(pk.node, "pfv-opt") });
}

test("pfcPicker: the root is .pfv-pick.menu, a dialog with its label", () => {
  const { pop } = picker();
  assert.strictEqual(pop.className, "pfv-pick menu");
  assert.strictEqual(pop.getAttribute("role"), "dialog");
  assert.strictEqual(pop.getAttribute("aria-label"), "Add access");
});

test("pfcPicker: group headings are .menu-label.grp-label, options are button.menu-item.pfv-opt", () => {
  const { pop, opts } = picker();
  assert.deepStrictEqual(byCls(pop, "grp-label").map((n) => [n.tag, n.className, n.textContent]), [
    // A div: the page's reset zeroes the padding of every p, and the kit label's padding is what lines it up with the rows.
    ["div", "menu-label grp-label", "AWS connections"], ["div", "menu-label grp-label", "Tokens"],
  ]);
  assert.strictEqual(byCls(pop, "pfv-pick-g").length, 0, "the page's own heading class is gone");
  assert.deepStrictEqual(opts.map((o) => [o.tag, o.className, o.type]), [
    ["button", "menu-item pfv-opt", "button"], ["button", "menu-item pfv-opt", "button"], ["button", "menu-item pfv-opt", "button"],
    ["button", "menu-item pfv-opt pfv-create", "button"],
  ]);
});

test("pfcPicker: a two-line option keeps its title (with the tag) and its line in one .menu-text; a one-line option has no second line", () => {
  const { opts } = picker();
  const [prod, , bare] = opts;
  assert.deepStrictEqual(prod.children.map((c) => c.className), ["menu-text"]);
  assert.deepStrictEqual(prod.children[0].children.map((c) => c.className), ["pfv-opt-t", "pfv-opt-s"]);
  assert.strictEqual(byCls(prod, "pfv-opt-t")[0].textContent, "prod-read PROD");
  assert.strictEqual(byCls(prod, "pfv-tag")[0].textContent, "PROD");
  assert.strictEqual(byCls(prod, "pfv-opt-s")[0].textContent, "Read only");
  assert.deepStrictEqual(bare.children[0].children.map((c) => c.className), ["pfv-opt-t"]);
});

test("pfcPicker: an Added option is a disabled button with the word at its right, and does nothing", () => {
  const { opts } = picker();
  const added = opts[1];
  assert.strictEqual(added.disabled, true);
  assert.strictEqual(added.onclick, undefined);
  assert.deepStrictEqual(added.children.map((c) => c.className), ["menu-text", "pfv-opt-r"], "the word follows the text, so the row lays out as a flex line");
  assert.strictEqual(byCls(added, "pfv-opt-r")[0].textContent, "Added");
  assert.ok(opts.filter((o) => o !== added).every((o) => o.disabled !== true));
});

test("pfcPicker: the filter, the list, the group notes and 'Nothing matches.' are as they were", () => {
  const { pop, pk } = picker();
  assert.strictEqual(byCls(pop, "pfv-pick-find").length, 1);
  assert.strictEqual(pk.list.className, "pfv-pick-list");
  assert.deepStrictEqual(byCls(pop, "pfv-pick-grp").length, 2);
  assert.deepStrictEqual(byCls(pop, "pfv-pick-note").map((n) => n.textContent), ["Tokens are long-lived, and the agent can read them while it runs."]);
  assert.strictEqual(byCls(pop, "pfv-pick-none")[0].hidden, true);
  assert.strictEqual(pk.first, byCls(pop, "pfv-pick-find")[0], "focus starts on the filter");
});

// ---- the CSS ------------------------------------------------------------------------------------------------------------------

const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
// Every `selectors { declarations }` of the sheet (the @media wrapper is walked through: only the innermost rules match).
const RULES = [];
for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
  RULES.push({
    sels: m[1].trim().split(/,(?![^()]*\))/).map((s) => s.trim().replace(/\s+/g, " ")),
    decls: m[2].split(";").map((d) => d.trim()).filter(Boolean).map((d) => { const i = d.indexOf(":"); return [d.slice(0, i).trim(), d.slice(i + 1).trim().replace(/\s+/g, " ")]; }),
  });
}
const ruleFor = (sel) => RULES.find((r) => r.sels.includes(sel));
const decl = (r, p) => ((r && r.decls.find(([k]) => k === p)) || [])[1];
const PFV = "#page-host > #settings-modal.as-page #pfv";

test("desktop.css: the page's own ⋯ popover is gone; the kit menu is placed under the ⋯ as frame M5.4's .m5-pop-r", () => {
  assert.deepStrictEqual(RULES.flatMap((r) => r.sels).filter((s) => /\.pfv-menu/.test(s)), [], "no .pfv-menu, .pfv-menu-item or .pfv-menu-red rule");
  const place = ruleFor(PFV + " .pfv-more > .menu");
  assert.ok(place, "a placement rule for the script-placed kit menu");
  assert.strictEqual(decl(place, "position"), "absolute");
  assert.strictEqual(decl(place, "top"), "calc(100% + var(--sp-1))");
  assert.strictEqual(decl(place, "right"), "0");
  assert.strictEqual(decl(place, "width"), "200px", "the frame's width; the kit's min-width: 220px is what shows");
  assert.strictEqual(decl(place, "z-index"), "30", "above the page's content");
  assert.deepStrictEqual(place.decls.map(([p]) => p).filter((p) => /^(background|border|box-shadow|padding|color|font|display)/.test(p)), [],
    "the surface is the kit's, and display stays the kit's so [hidden] still closes it");
  assert.strictEqual(decl(ruleFor(PFV + " .pfv-more"), "position"), "relative");
});

test("desktop.css: the ⋯ shows pressed (--raise) while its menu is open", () => {
  const r = ruleFor(PFV + ' .pfv-more .btn-icon[aria-expanded="true"]');
  assert.ok(r, "an aria-expanded rule on the trigger");
  assert.strictEqual(decl(r, "background"), "var(--raise)");
  assert.strictEqual(decl(r, "color"), "var(--fg)");
  assert.strictEqual(decl(r, "box-shadow"), "inset 0 0 0 1px var(--faint)", "the kit's pressed look (.btn.is-pressed)");
});

test("desktop.css: the picker's container carries the kit menu's values with its own selector, and its options the kit's row values", () => {
  const box = ruleFor(PFV + " .pfv-pick");
  assert.strictEqual(decl(box, "background"), "var(--panel)");
  assert.strictEqual(decl(box, "border"), "1px solid var(--line)");
  assert.strictEqual(decl(box, "border-radius"), "var(--r-card)");
  assert.strictEqual(decl(box, "box-shadow"), "var(--e-pop)");
  assert.strictEqual(decl(box, "padding"), "4px");
  assert.strictEqual(decl(box, "display"), "block", "app.css's legacy .menu is display: none until .open");
  assert.strictEqual(decl(box, "position"), "absolute");
  const opt = ruleFor(PFV + " .pfv-opt");
  assert.strictEqual(decl(opt, "height"), "auto", "the kit's fixed 30px gives way: a two-line option is taller");
  assert.strictEqual(decl(opt, "min-height"), "var(--h-menu)");
  assert.strictEqual(decl(opt, "padding"), undefined, "the kit's 0 10px, not the page's old 7px 10px");
  assert.strictEqual(decl(opt, "display"), undefined, "the kit's flex row, not the page's old grid");
  const two = ruleFor(PFV + " .pfv-opt:has(.pfv-opt-s)");
  assert.strictEqual(decl(two, "padding"), "6px 10px", "block padding only on a two-line option");
  // Hover is the kit's (--raise); an Added option does not take it.
  assert.strictEqual(decl(ruleFor(PFV + " .pfv-opt:disabled:hover"), "background"), "transparent");
  assert.strictEqual(decl(ruleFor(PFV + " .pfv-opt:focus-visible"), "outline-offset"), "-2px", "the list scrolls: an inset ring, as desktop-focus.test.js allows");
});

test("desktop.css: every kit menu's icons are 14px (the rule moved off #session-menu onto the kit's .menu-icon)", () => {
  const kit = ruleFor(".menu-icon svg");
  assert.ok(kit, "a kit rule on .menu-icon svg");
  assert.strictEqual(decl(kit, "width"), "14px");
  assert.strictEqual(decl(kit, "height"), "14px");
  assert.strictEqual(decl(kit, "display"), undefined, "the slot is a flex box: the icon is a block in it, and a kit display needs a [hidden] partner");
  assert.ok(css.indexOf(".menu-icon svg {") < css.indexOf("#session-menu:has("), "in the kit section, before the page rules");
  assert.strictEqual(ruleFor("#session-menu .menu-item > .menu-icon svg"), undefined, "no second copy on #session-menu");
  assert.strictEqual(decl(ruleFor("#session-menu .menu-check svg"), "width"), "14px", "the tab switcher's check keeps its own");
});
