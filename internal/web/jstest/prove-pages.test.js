"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { ICON_SRC } = require("./iconpage.js");

// The two "Prove" pages (UI plan 5, #175; frames M3.1-M3.3): desktop Audit and Analytics wear one page head, one toolbar,
// one KPI row and one table style, all of them the kit's. This file pins the Audit page (Task 2; digest-design §B1-§B5,
// §B9, §G8); the Analytics page joins it with the same-classes test. Source-pinning like settings-shell.test.js: the
// stylesheets need a browser to draw, so each value is read off the rule that carries it, and the page script runs in a
// vm against a fake DOM built from the real index.html markup, so markup and script cannot drift apart.
const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const HTML = read("index.html");
const DESK_CSS = stripComments(read("desktop.css"));
const APP_CSS = stripComments(read("app.css"));

// --- the CSS, as rules ----------------------------------------------------------------------------------------

// parseRules: every style rule of `src` (comments stripped) as { selector, decls: Map, at: [the at-rules around it] }. A ";"
// or a brace inside quotes or parentheses is part of the value. At-rule containers (@media, @container) are walked through.
function parseRules(src) {
  const rules = [];
  const stack = [];
  let buf = "";
  let paren = 0;
  let quote = null;
  const flush = () => {
    const d = buf.trim();
    buf = "";
    if (!d || !stack.length) return;
    const i = d.indexOf(":");
    if (i < 0) return;
    stack[stack.length - 1].decls.set(d.slice(0, i).trim().toLowerCase(), d.slice(i + 1).replace(/\s+/g, " ").trim());
  };
  for (const ch of src) {
    if (quote) { buf += ch; if (ch === quote) quote = null; continue; }
    if (ch === '"' || ch === "'") { quote = ch; buf += ch; continue; }
    if (ch === "(") paren++;
    else if (ch === ")") paren--;
    if (paren === 0 && ch === "{") {
      stack.push({ selector: buf.trim().replace(/\s+/g, " "), decls: new Map(), at: stack.map((n) => n.selector).filter((x) => x.startsWith("@")) });
      buf = "";
    } else if (paren === 0 && ch === ";") {
      flush();
    } else if (paren === 0 && ch === "}") {
      flush();
      const done = stack.pop();
      if (done && !done.selector.startsWith("@")) rules.push(done);
    } else {
      buf += ch;
    }
  }
  return rules;
}

// parts: a selector list split on its top-level commas (not the ones inside :is() / :not()).
function parts(list) {
  const out = [];
  let depth = 0;
  let cur = "";
  for (const ch of list) {
    if (ch === "(" || ch === "[") depth++;
    else if (ch === ")" || ch === "]") depth--;
    if (ch === "," && depth === 0) { out.push(cur.trim()); cur = ""; } else cur += ch;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}

const RULES = parseRules(DESK_CSS);
// Desktop.css is one @media block; a rule inside an @container (the Audit table's narrow card) is a different cascade.
const inContainer = (r) => r.at.some((a) => a.startsWith("@container"));
const BASE_RULES = RULES.filter((r) => !inContainer(r));
const CONTAINER_RULES = RULES.filter(inContainer);
// decls: the cascade of every desktop rule outside an @container that lists exactly `sel` (later rules win), as one Map.
// Empty when none does.
function decls(sel, rules = BASE_RULES) {
  const out = new Map();
  for (const r of rules) if (parts(r.selector).includes(sel)) for (const [k, v] of r.decls) out.set(k, v);
  return out;
}

const AU = "#page-host > #audit-page.as-page";

// --- the markup, as a tree ------------------------------------------------------------------------------------

const VOID = new Set(["area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr"]);
const ENTITIES = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&times;": "×", "&middot;": "·", "&hellip;": "…" };
const decode = (s) => s
  .replace(/&#x([0-9a-f]+);/gi, (_, h) => String.fromCodePoint(parseInt(h, 16)))
  .replace(/&#(\d+);/g, (_, d) => String.fromCodePoint(Number(d)))
  .replace(/&\w+;/g, (m) => ENTITIES[m] || m);

// parseHtml: a fragment as { tag, attrs, children } nodes; a text child is { text }. Enough HTML for index.html's markup:
// comments, void elements, quoted and bare attributes.
function parseHtml(src) {
  const root = { tag: "#root", attrs: {}, children: [] };
  const stack = [root];
  const re = /<!--[\s\S]*?-->|<(\/?)([a-zA-Z][\w-]*)((?:\s+[^\s=>/]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+))?)*)\s*\/?>|([^<]+)/g;
  for (const m of src.matchAll(re)) {
    if (m[0].startsWith("<!--")) continue;
    const top = stack[stack.length - 1];
    if (m[4] !== undefined) { top.children.push({ text: decode(m[4]) }); continue; }
    const tag = m[2].toLowerCase();
    if (m[1]) {
      const i = stack.map((n) => n.tag).lastIndexOf(tag);
      if (i > 0) stack.length = i;
      continue;
    }
    const attrs = {};
    for (const a of m[3].matchAll(/([^\s=>/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) {
      attrs[a[1]] = decode(a[2] !== undefined ? a[2] : a[3] !== undefined ? a[3] : a[4] !== undefined ? a[4] : "");
    }
    const node = { tag, attrs, children: [] };
    top.children.push(node);
    if (!VOID.has(tag) && !m[0].endsWith("/>")) stack.push(node);
  }
  return root;
}

const kids = (n) => n.children.filter((c) => c.tag);
const classes = (n) => (n.attrs.class || "").split(/\s+/).filter(Boolean);
const text = (n) => n.children.map((c) => (c.tag ? text(c) : c.text)).join("").replace(/\s+/g, " ").trim();
// sig: "div.page-head" for <div class="page-head">.
const sig = (n) => n.tag + classes(n).map((c) => "." + c).join("");

function dialogHtml(id) {
  const at = HTML.indexOf('<dialog id="' + id + '"');
  assert.ok(at >= 0, id + " is in index.html");
  return HTML.slice(at, HTML.indexOf("</dialog>", at) + "</dialog>".length);
}
const auditTree = () => kids(parseHtml(dialogHtml("audit-page")))[0];

// byId: the node with `id` anywhere under `n`.
function byId(n, id) {
  if (n.attrs && n.attrs.id === id) return n;
  for (const c of kids(n)) { const f = byId(c, id); if (f) return f; }
  return null;
}

// --- Audit: the markup ----------------------------------------------------------------------------------------

test("Audit head, toolbar and KPI row are the kit components", () => {
  const dlg = auditTree();
  assert.strictEqual(dlg.tag, "dialog");
  assert.strictEqual(dlg.attrs.id, "audit-page");
  assert.strictEqual(dlg.attrs["aria-label"], "Audit", "the dialog keeps its exact opening tag");
  assert.match(HTML, /<dialog id="audit-page" aria-label="Audit">/);
  assert.deepStrictEqual(kids(dlg).map(sig), ["div.page-head", "div.toolbar.au-tools", "div.au-scroll"]);
  const [head, tools, scroll] = kids(dlg);

  // The head: title and line on the left, Export CSV and Esc ✕ on the right (digest-design §B1).
  assert.deepStrictEqual(kids(head).map(sig), ["div.page-head-text", "div.page-actions"]);
  const [headText, actions] = kids(head);
  assert.deepStrictEqual(kids(headText).map(sig), ["h1.page-title", "p.page-desc"]);
  assert.strictEqual(text(kids(headText)[0]), "Audit");
  assert.strictEqual(text(kids(headText)[1]), "Every credential issued, write elevation, egress connection and settings change, newest first.");
  assert.deepStrictEqual(kids(actions).map(sig), ["a.btn", "button.btn.btn-ghost.page-esc"]);
  const [csv, esc] = kids(actions);
  assert.strictEqual(csv.attrs.id, "au-csv");
  assert.strictEqual(csv.attrs.href, "/api/audit.csv");
  assert.ok("download" in csv.attrs, "an anchor that downloads");
  assert.strictEqual(text(csv), "Export CSV");
  assert.strictEqual(esc.attrs.id, "au-esc");
  assert.strictEqual(esc.attrs.type, "button");
  assert.strictEqual(esc.attrs["data-close"], "audit-page", "initDialogs wires every [data-close] button: no builder");
  assert.strictEqual(esc.attrs["aria-label"], "Close (Esc)");
  assert.strictEqual(esc.attrs.title, "Close (Esc)");
  // #168: the Esc hint, then the shared x glyph (a static-chrome placeholder at 12), no ✕ text.
  assert.deepStrictEqual(kids(esc).map(sig), ["span.kbd-hint", "svg"]);
  assert.strictEqual(text(kids(esc)[0]), "Esc");
  assert.strictEqual(text(esc), "Esc", "no ✕ text glyph");
  assert.strictEqual(kids(esc)[1].attrs["data-uicon"], "x");
  assert.strictEqual(kids(esc)[1].attrs.width, "12");

  // The toolbar: Kind seg, Session select, spacer, Range seg (§B2).
  assert.deepStrictEqual(kids(tools).map(sig), ["div.seg", "select.input", "span.grow", "div.seg"]);
  const [kind, session, , range] = kids(tools);
  assert.strictEqual(kind.attrs.id, "au-filters");
  assert.strictEqual(kind.attrs.role, "group");
  assert.strictEqual(kind.attrs["aria-label"], "Kind");
  assert.deepStrictEqual(kids(kind).map(sig), Array(5).fill("button.seg-item"));
  assert.deepStrictEqual(kids(kind).map((b) => b.attrs["data-filter"]), ["", "credentials", "elevations", "egress", "settings"]);
  assert.deepStrictEqual(kids(kind).map(text), ["All", "Credentials", "Elevations", "Egress", "Settings changes"]);
  assert.deepStrictEqual(kids(kind).map((b) => b.attrs["aria-pressed"]), ["true", "false", "false", "false", "false"], "All is pressed before the first paint");
  assert.ok(kids(kind).every((b) => b.attrs.type === "button"));
  assert.strictEqual(session.attrs.id, "au-session");
  assert.strictEqual(session.attrs["aria-label"], "Session");
  assert.strictEqual(range.attrs.id, "au-range");
  assert.strictEqual(range.attrs.role, "group");
  assert.strictEqual(range.attrs["aria-label"], "Range");
  assert.deepStrictEqual(kids(range).map(sig), Array(3).fill("button.seg-item"));
  assert.deepStrictEqual(kids(range).map((b) => b.attrs["data-range"]), ["24h", "7d", "30d"]);
  assert.deepStrictEqual(kids(range).map(text), ["24h", "7d", "30d"]);
  assert.deepStrictEqual(kids(range).map((b) => b.attrs["aria-pressed"]), ["false", "true", "false"], "7d is pressed before the first paint");
  assert.ok(kids(range).every((b) => b.attrs.type === "button"));

  // Everything from the KPI row down scrolls (the plan's call 9): KPI row, the table's card, the footer.
  assert.deepStrictEqual(kids(scroll).map(sig), ["div.kpi-row", "div.au-card", "div.au-foot"]);
  const [kpis, card, foot] = kids(scroll);
  assert.strictEqual(scroll.attrs.id, "au-scroll");
  assert.strictEqual(kpis.attrs.id, "au-kpis");
  assert.strictEqual(kids(kpis).length, 0, "the tiles are painted by auditpage.js");
  assert.deepStrictEqual(kids(card).map(sig), ["table.tbl.au-tbl"]);
  const table = kids(card)[0];
  assert.strictEqual(table.attrs.id, "au-table");
  assert.deepStrictEqual(kids(table).map((n) => n.tag), ["thead", "tbody"]);
  assert.deepStrictEqual(kids(kids(kids(table)[0])[0]).map(text), ["Time", "Session", "Kind", "What", "Outcome"]);
  assert.strictEqual(kids(table)[1].attrs.id, "au-body");
  assert.strictEqual(foot.attrs.id, "au-foot");
  assert.deepStrictEqual(kids(foot).map(sig), ["span", "button.btn.btn-sm", "span.grow", "span"]);
  assert.deepStrictEqual(kids(foot).map((n) => n.attrs.id), ["au-count", "au-more", undefined, "au-policy"]);
  const more = kids(foot)[1];
  assert.strictEqual(more.attrs.type, "button");
  assert.ok("hidden" in more.attrs, "Show more starts hidden: auPaintFoot shows it when the reply has a next page");
  assert.strictEqual(text(more), "Show more");
});

test("the Audit markup has none of the old page's classes", () => {
  const html = dialogHtml("audit-page");
  const used = new Set([...html.matchAll(/class="([^"]*)"/g)].flatMap((m) => m[1].split(/\s+/)));
  for (const gone of ["dhd", "au-kpis", "au-filters", "fld", "gh", "on", "au-kpi", "au-elev", "au-n", "au-l"]) {
    assert.ok(!used.has(gone), "." + gone + " is gone from the Audit markup");
  }
  assert.ok(!/<h2/.test(html), "the title is the page's h1, which the page focuses when it opens (PAGE_FOCUS)");
});

// --- Audit: the script, run against that markup ------------------------------------------------------------------

class TextNode {
  constructor(text) { this.text = String(text); this.parent = null; }
  get textContent() { return this.text; }
}

// The element with focus (a browser has one per document); reset by each env.
let focused = null;

// A fake DOM element: what auditpage.js touches, plus a record of every classList call so a test can see that no
// `.on` is ever toggled.
class El {
  constructor(tag) {
    this.tag = String(tag).toLowerCase();
    this.tagName = this.tag.toUpperCase();
    this.children = [];
    this.parent = null;
    this.attrs = {};
    this.dataset = {};
    this.listeners = {};
    this.hidden = false;
    this.disabled = false;
    this.value = "";
    this.colSpan = 1;
    this.cls = [];
    this.classCalls = [];
    this.style = {};
    const self = this;
    this.classList = {
      toggle(c, force) {
        self.classCalls.push(["toggle", c, force]);
        const on = force === undefined ? !self.cls.includes(c) : !!force;
        self.cls = self.cls.filter((x) => x !== c);
        if (on) self.cls.push(c);
        return on;
      },
      add(...cs) { for (const c of cs) { self.classCalls.push(["add", c]); if (!self.cls.includes(c)) self.cls.push(c); } },
      remove(...cs) { for (const c of cs) { self.classCalls.push(["remove", c]); self.cls = self.cls.filter((x) => x !== c); } },
      contains: (c) => self.cls.includes(c),
    };
  }
  get className() { return this.cls.join(" "); }
  set className(v) { this.cls = String(v).split(/\s+/).filter(Boolean); }
  get textContent() { return this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children = []; if (String(v) !== "") this.append(String(v)); }
  setAttribute(k, v) { if (k === "class") this.className = v; else this.attrs[k] = String(v); }
  getAttribute(k) { return k === "class" ? this.className : this.attrs[k] === undefined ? null : this.attrs[k]; }
  removeAttribute(k) { delete this.attrs[k]; }
  append(...nodes) {
    for (const n of nodes) {
      const c = typeof n === "string" ? new TextNode(n) : n;
      c.parent = this;
      this.children.push(c);
    }
  }
  appendChild(n) { this.append(n); return n; }
  prepend(...nodes) {
    const kids = nodes.map((n) => (typeof n === "string" ? new TextNode(n) : n));
    for (const c of kids) c.parent = this;
    this.children.unshift(...kids);
  }
  // A node taken out of its parent is no longer in the page (a browser drops focus from it).
  replaceChildren(...nodes) { for (const c of this.children) c.parent = null; this.children = []; this.append(...nodes); }
  focus() { focused = this; }
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
  closest(sel) { for (let n = this; n; n = n.parent) if (n.tag && matches(n, sel)) return n; return null; }
  querySelectorAll(sel) { return queryAll(this, sel); }
  querySelector(sel) { return queryAll(this, sel)[0] || null; }
  get elements() { return this.children.filter((c) => c instanceof El); }
}

// matches: one compound selector (tag, #id, .class, [attr], [attr=value]).
function matches(el, compound) {
  const m = /^([a-z][\w-]*)?((?:[#.][\w-]+|\[[^\]]+\])*)$/i.exec(compound);
  if (!m) throw new Error("fake DOM: unsupported selector " + compound);
  if (m[1] && el.tag !== m[1].toLowerCase()) return false;
  for (const t of m[2].matchAll(/#([\w-]+)|\.([\w-]+)|\[([\w-]+)(?:="?([^"\]]*)"?)?\]/g)) {
    if (t[1] !== undefined && el.attrs.id !== t[1]) return false;
    if (t[2] !== undefined && !el.cls.includes(t[2])) return false;
    if (t[3] !== undefined) {
      const key = t[3].startsWith("data-") ? t[3].slice(5).replace(/-(\w)/g, (_, c) => c.toUpperCase()) : null;
      const v = key ? el.dataset[key] : el.attrs[t[3]];
      if (v === undefined || (t[4] !== undefined && v !== t[4])) return false;
    }
  }
  return true;
}

// queryAll: descendants of `root` matching `sel` ("a b" is a descendant, "a, b" a list; no other combinators).
function queryAll(root, sel) {
  const out = [];
  for (const one of sel.split(",")) {
    const chain = one.trim().split(/\s+/);
    const walk = (n, from) => {
      for (const c of n.elements) {
        let next = from;
        if (matches(c, chain[from])) {
          if (from === chain.length - 1) { if (!out.includes(c)) out.push(c); } else next = from + 1;
        }
        walk(c, next);
      }
    };
    walk(root, 0);
  }
  return out;
}

// fromParsed: the parsed index.html markup as fake elements.
function fromParsed(node) {
  const el = new El(node.tag);
  for (const [k, v] of Object.entries(node.attrs)) {
    if (k === "class") el.className = v;
    else if (k === "hidden") el.hidden = true;
    else if (k.startsWith("data-")) el.dataset[k.slice(5).replace(/-(\w)/g, (_, c) => c.toUpperCase())] = v;
    else el.attrs[k] = v;
  }
  for (const c of node.children) el.append(c.tag ? fromParsed(c) : c.text);
  return el;
}

function findId(root, id) {
  if (root.attrs.id === id) return root;
  for (const c of root.elements) { const f = findId(c, id); if (f) return f; }
  return null;
}

// auditEnv: accessmodel.js, auditmodel.js and auditpage.js, in their page order, in a context whose document is the real
// Audit dialog markup (the icon scripts load first, as in index.html). `run` evaluates a snippet in it (the scripts' top-level consts are visible to it).
function auditEnv() {
  focused = null;
  const dlg = fromParsed(auditTree());
  const body = new El("body");
  const inPage = (n) => { for (let x = n; x; x = x.parent) if (x === dlg) return true; return false; };
  const document = {
    getElementById: (id) => findId(dlg, id),
    querySelectorAll: (sel) => queryAll(dlg, sel),
    createElement: (tag) => new El(tag),
    createElementNS: (_ns, tag) => new El(tag),
    createTextNode: (t) => new TextNode(t),
    body,
    get activeElement() { return focused && inPage(focused) ? focused : body; },
  };
  const ctx = { document, window: { innerWidth: 1440 }, lastSessions: [], console, AbortController, fetch: async () => ({}) };
  vm.createContext(ctx);
  for (const f of ["icons.js", "svgicon.js", "iconmap.js", "accessmodel.js", "auditmodel.js", "auditpage.js"]) new vm.Script(read(f), { filename: f }).runInContext(ctx);
  return { ctx, dlg, run: (code) => vm.runInContext(code, ctx), el: (id) => findId(dlg, id) };
}

const stripJsComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/.*$/gm, "");

test("Audit controls mark the active item with aria-pressed, not .on", () => {
  const { run, el } = auditEnv();
  const pressed = (id) => el(id).elements.map((b) => b.attrs["aria-pressed"]);
  run('auState.range = "30d"; auState.filter = "egress"; auPaintControls();');
  assert.deepStrictEqual(pressed("au-range"), ["false", "false", "true"]);
  assert.deepStrictEqual(pressed("au-filters"), ["false", "false", "false", "true", "false"]);
  // In place: the same nodes, flipped (focus stays on the control that was clicked, Review Focus 5).
  const before = [...el("au-filters").elements];
  run('auState.filter = ""; auState.range = "24h"; auPaintControls();');
  assert.deepStrictEqual(pressed("au-range"), ["true", "false", "false"]);
  assert.deepStrictEqual(pressed("au-filters"), ["true", "false", "false", "false", "false"]);
  assert.ok(el("au-filters").elements.every((b, i) => b === before[i]), "the seg items are updated, never rebuilt");
  // No class is toggled on a seg item: primitives.css `.seg button.on` is weight 700 and accent text.
  for (const b of [...el("au-range").elements, ...el("au-filters").elements]) {
    assert.ok(b.cls.includes("seg-item"));
    assert.ok(!b.cls.includes("on") && !b.classCalls.some(([, c]) => c === "on"), b.textContent + ": no .on");
  }
  // And the export link follows the same state.
  assert.match(el("au-csv").attrs.href, /^\/api\/audit\.csv\?from=\d+&to=\d+$/);
  assert.ok(!/classList\.(?:toggle|add|remove|contains)\(\s*["']on["']/.test(stripJsComments(read("auditpage.js"))), 'no classList…("on" in auditpage.js');
  assert.ok(!/["']on["']/.test(stripJsComments(read("auditpage.js"))), 'no "on" class string anywhere in auditpage.js');
});

test("Audit KPI tiles are .kpi with .kpi-value/.kpi-label; Write elevations > 0 is .is-bad", () => {
  const { run, el } = auditEnv();
  const tiles = () => el("au-kpis").elements;
  const read5 = () => tiles().map((t) => ({
    cls: t.className,
    kids: t.elements.map((c) => [c.tag, c.className, c.textContent]),
    title: t.elements[1] && t.elements[1].attrs.title,
  }));

  run("auData.summary = null; auPaintKpis();");
  assert.strictEqual(tiles().length, 5);
  assert.deepStrictEqual(read5().map((t) => t.kids[0][2]), ["—", "—", "—", "—", "—"], "no summary: dashes");

  run("auData.summary = { issued: 37, refused: 2, elevations: 2, egress_allowed: 1284, egress_denied: 20 }; auPaintKpis();");
  const got = read5();
  assert.deepStrictEqual(got.map((t) => t.cls), Array(5).fill("kpi"));
  assert.deepStrictEqual(got.map((t) => t.kids.map((k) => k[0])), Array(5).fill(["span", "span"]));
  assert.deepStrictEqual(got.map((t) => t.kids[1][1]), Array(5).fill("kpi-label"));
  assert.deepStrictEqual(got.map((t) => t.kids[0][2]), ["37", "2", "2", "1,284", "20"]);
  assert.deepStrictEqual(got.map((t) => t.kids[1][2]), ["Credentials issued", "Refused", "Write elevations", "Egress allowed", "Egress denied"]);
  assert.deepStrictEqual(got.map((t) => t.kids[0][1]), ["kpi-value", "kpi-value", "kpi-value is-bad", "kpi-value", "kpi-value"], "red only on Write elevations");
  assert.deepStrictEqual(got.map((t) => t.title), got.map((t) => t.kids[1][2]), "an ellipsized label keeps its full text in title");

  run("auData.summary = { issued: 1, refused: 5, elevations: 0, egress_allowed: 1, egress_denied: 5 }; auPaintKpis();");
  assert.deepStrictEqual(read5().map((t) => t.kids[0][1]), Array(5).fill("kpi-value"), "no write elevation, nothing red; refused and denied are never red");
  // The tiles are replaced, not appended to.
  assert.strictEqual(tiles().length, 5);
});

test("an elevated row is tr.is-elevated with a red Kind cell", () => {
  const { run } = auditEnv();
  const row = (ev) => run("auRow(" + JSON.stringify(ev) + ", {})");
  const cells = (tr) => tr.elements.map((td) => [td.className, td.textContent]);
  const base = { ts: 1_760_000_000, kind: "elevation", what: "write on sandbox · 30 min", session: "" };

  const granted = row({ ...base, outcome: "granted", actor: "you", reason: "rotate the key" });
  assert.strictEqual(granted.className, "is-elevated");
  const c = cells(granted);
  assert.deepStrictEqual(c.map((x) => x[0]), ["au-time", "au-sess", "au-kind au-bad", "au-what", "au-out au-bad"]);
  assert.strictEqual(c[2][1], "Elevation");
  assert.strictEqual(c[1][1], "—");
  assert.strictEqual(c[3][1], "write on sandbox · 30 min · “rotate the key”");
  assert.strictEqual(c[4][1], "granted · you");
  const what = granted.elements[3];
  assert.deepStrictEqual(what.elements.map((s) => [s.tag, s.className, s.textContent]), [["span", "au-reason", " · “rotate the key”"]], "the reason: today's muted span");

  const extended = row({ ...base, outcome: "extended", actor: "you" });
  assert.strictEqual(extended.className, "is-elevated");
  assert.strictEqual(cells(extended)[2][0], "au-kind au-bad");

  // An elevation that opened nothing is a plain row: no tint, a muted Kind. A refused one is containment working
  // (UI plan 6, #176), so its Outcome is neutral like a dropped one's.
  for (const outcome of ["dropped", "expired", "ended"]) {
    const tr = row({ ...base, outcome, actor: "you" });
    assert.strictEqual(tr.className, "", outcome + ": no tint");
    assert.deepStrictEqual(cells(tr).map((x) => x[0]), ["au-time", "au-sess", "au-kind", "au-what", "au-out"], outcome);
  }
  const refused = row({ ...base, outcome: "refused", actor: "you" });
  assert.strictEqual(refused.className, "");
  assert.deepStrictEqual(cells(refused).map((x) => x[0]), ["au-time", "au-sess", "au-kind", "au-what", "au-out"], "refused: not tinted, Outcome neutral");

  // Other rows: the Outcome keeps its tone class from auditRowView.
  const minted = row({ ts: 1_760_000_000, kind: "credential", what: "aws sandbox", outcome: "minted" });
  assert.strictEqual(minted.className, "");
  assert.deepStrictEqual(cells(minted).map((x) => x[0]), ["au-time", "au-sess", "au-kind", "au-what", "au-out au-ok"]);
  const denied = row({ ts: 1_760_000_000, kind: "egress", what: "pypi.org:443", outcome: "denied", count: 3 });
  assert.strictEqual(cells(denied)[4][0], "au-out");
  assert.strictEqual(cells(denied)[4][1], "denied ×3");
  const dialError = row({ ts: 1_760_000_000, kind: "egress", what: "pypi.org:443", outcome: "dial_error" });
  assert.strictEqual(cells(dialError)[4][0], "au-out au-bad");
  const created = row({ ts: 1_760_000_000, kind: "settings", what: "token e2e created", outcome: "created", actor: "you" });
  assert.strictEqual(cells(created)[4][0], "au-out");
});

// --- Audit: the What cell (Task 3; digest-design §B9) --------------------------------------------------------------

// whatCell: the What td of one row, as [kind, tag, className, text] per child node: a text node is ["text", …], an element
// ["el", tag, className, text].
function whatCell(run, ev) {
  const tr = run("auRow(" + JSON.stringify(ev) + ", {})");
  const td = tr.elements[3];
  assert.strictEqual(td.className, "au-what");
  return {
    td,
    nodes: td.children.map((c) => (c instanceof El ? ["el", c.tag, c.className, c.textContent] : ["text", c.textContent])),
    monos: td.elements.filter((c) => c.tag === "span" && c.cls.includes("mono")),
  };
}

test("What renders ids in span.mono.t-sm and text as text nodes, then the muted reason", () => {
  const { run } = auditEnv();
  const ev = {
    ts: 1_760_000_000, kind: "elevation", outcome: "granted", actor: "you", reason: "rotate KMS key",
    what: "write on culipulse-prod · 30 min",
    parts: [{ text: "write on " }, { text: "culipulse-prod", id: true }, { text: " · 30 min" }],
  };
  const { td, nodes, monos } = whatCell(run, ev);
  assert.deepStrictEqual(nodes, [
    ["text", "write on "],
    ["el", "span", "mono t-sm", "culipulse-prod"],
    ["text", " · 30 min"],
    ["el", "span", "au-reason", " · “rotate KMS key”"],
  ], "identifiers are spans, every other run is a text node, the reason comes last in its muted span");
  assert.strictEqual(td.textContent, "write on culipulse-prod · 30 min · “rotate KMS key”", "the cell reads as the joined runs plus the reason");
  assert.strictEqual(monos.length, ev.parts.filter((p) => p.id).length, "one mono span per id run");

  // Two identifiers, one text run between them, no reason.
  const aws = whatCell(run, {
    ts: 1_760_000_000, kind: "credential", outcome: "minted", what: "aws culipulse-prod · role agent-ops-write · elevated",
    parts: [{ text: "aws " }, { text: "culipulse-prod", id: true }, { text: " · role " }, { text: "agent-ops-write", id: true }, { text: " · elevated" }],
  });
  assert.strictEqual(aws.monos.length, 2);
  assert.deepStrictEqual(aws.monos.map((m) => m.textContent), ["culipulse-prod", "agent-ops-write"]);
  assert.strictEqual(aws.td.textContent, "aws culipulse-prod · role agent-ops-write · elevated");

  // A host:port alone is one mono span and nothing else; a masked name arrives as an id part and is mono like any other.
  const host = whatCell(run, { ts: 1_760_000_000, kind: "egress", outcome: "allowed", what: "registry.terraform.io:443", parts: [{ text: "registry.terraform.io:443", id: true }] });
  assert.deepStrictEqual(host.nodes, [["el", "span", "mono t-sm", "registry.terraform.io:443"]]);
  const masked = whatCell(run, { ts: 1_760_000_000, kind: "settings", outcome: "created", what: "token [masked:a1b2] created", parts: [{ text: "token " }, { text: "[masked:a1b2]", id: true }, { text: " created" }] });
  assert.deepStrictEqual(masked.monos.map((m) => m.textContent), ["[masked:a1b2]"]);
});

test("no What span without parts", () => {
  const { run } = auditEnv();
  // An old server, an empty list, and parts the page cannot use: the text as it came, in one text node, no mono guessed.
  const base = { ts: 1_760_000_000, kind: "credential", outcome: "minted", what: "aws culipulse-prod · role agent-ops-write · elevated" };
  for (const extra of [{}, { parts: [] }, { parts: {} }, { parts: [{ id: true }] }, { parts: [{ text: 5 }] }, { parts: null }]) {
    const c = whatCell(run, { ...base, ...extra });
    assert.strictEqual(c.monos.length, 0, JSON.stringify(extra));
    assert.deepStrictEqual(c.nodes, [["text", base.what]], JSON.stringify(extra));
  }
  // The reason still follows an old row's text.
  const old = whatCell(run, { ts: 1_760_000_000, kind: "elevation", outcome: "granted", actor: "you", reason: "why", what: "write on prod · 30 min" });
  assert.deepStrictEqual(old.nodes, [["text", "write on prod · 30 min"], ["el", "span", "au-reason", " · “why”"]]);
  // No what at all is an empty cell, not a throw.
  assert.strictEqual(whatCell(run, { ts: 1_760_000_000, kind: "credential", outcome: "minted" }).td.textContent, "");
});

test("a part with empty text draws no empty span, and parts that draw nothing fall back to what", () => {
  const { run } = auditEnv();
  const base = { ts: 1_760_000_000, kind: "credential", outcome: "minted", what: "aws sandbox" };
  const some = whatCell(run, { ...base, parts: [{ text: "aws " }, { text: "", id: true }, { text: "sandbox" }] });
  assert.strictEqual(some.monos.length, 0, "no empty mono span");
  assert.strictEqual(some.td.textContent, "aws sandbox");
  const none = whatCell(run, { ...base, parts: [{ text: "" }, { text: "", id: true }] });
  assert.strictEqual(none.monos.length, 0);
  assert.deepStrictEqual(none.nodes, [["text", "aws sandbox"]], "never a blank cell when what has text");
});

test("the store unlock row reads for 7 days", () => {
  const { run } = auditEnv();
  // The server keeps what = "… · ttl=168h0m0s" (the CSV export is raw) and spells the parts in words.
  const ev = {
    ts: 1_760_000_000, kind: "store", outcome: "unlocked", actor: "cli",
    what: "encrypted store unlocked · ttl=168h0m0s",
    parts: [{ text: "encrypted store unlocked" }, { text: " · for 7 days" }],
  };
  const c = whatCell(run, ev);
  assert.strictEqual(c.td.textContent, "encrypted store unlocked · for 7 days");
  assert.ok(!c.td.textContent.includes("ttl="));
  assert.strictEqual(c.monos.length, 0, "no mono span at all: 7 days is plain text");
  // An older server sends no parts: the ttl stays as the server wrote it (never reworded by the page).
  const old = whatCell(run, { ...ev, parts: undefined });
  assert.strictEqual(old.td.textContent, "encrypted store unlocked · ttl=168h0m0s");
  assert.strictEqual(old.monos.length, 0);
});

test("the What cell is sans 13px fg; identifiers take the kit's .mono.t-sm and the reason is muted", () => {
  // The cell inherits the kit table's 13px --fg sans; the page adds no font or colour of its own to it.
  assert.strictEqual(decls(".tbl").get("font-size"), "var(--t-md)");
  assert.strictEqual(decls(".tbl").get("color"), "var(--fg)");
  const what = decls(AU + " .au-tbl td.au-what");
  for (const k of ["font-family", "font-size", "color", "font-weight"]) assert.strictEqual(what.get(k), undefined, "td.au-what sets no " + k);
  // The identifier spans carry the kit's helpers: mono family, 12px / 18px.
  assert.strictEqual(decls(".mono").get("font-family"), "var(--font-mono)");
  assert.strictEqual(decls(".t-sm").get("font-size"), "var(--t-sm)");
  assert.strictEqual(decls(".t-sm").get("line-height"), "var(--lh-sm)");
  // The reason is a sans muted span at the cell's 13px, not the 12px of an identifier.
  const reason = decls(AU + " .au-tbl td.au-what .au-reason");
  assert.strictEqual(reason.get("color"), "var(--muted)");
  assert.strictEqual(reason.get("font-family"), undefined);
  assert.strictEqual(reason.get("font-size"), undefined);
  // auditpage.js builds the cell from createTextNode and textContent only, with the kit's classes.
  const js = stripJsComments(read("auditpage.js"));
  assert.match(js, /document\.createTextNode\(/);
  assert.match(js, /"mono t-sm"/);
});

test("Show more is hidden unless the reply has a next page, and the page script never writes HTML", () => {
  const { run, el } = auditEnv();
  run('auData.next = ""; auData.listState = "ok"; auPaintFoot();');
  assert.strictEqual(el("au-more").hidden, true);
  run('auData.next = "17.2.9"; auPaintFoot();');
  assert.strictEqual(el("au-more").hidden, false);
  assert.strictEqual(el("au-more").disabled, false);
  run('auData.listState = "loading"; auPaintFoot();');
  assert.strictEqual(el("au-more").disabled, true, "disabled while the next page loads");
  run('auData.next = ""; auPaintFoot();');
  assert.strictEqual(el("au-more").hidden, true);
  assert.ok(!/innerHTML|insertAdjacentHTML|outerHTML/.test(stripJsComments(read("auditpage.js"))));
});

// Gate 3 D8: Show more pressed by keyboard loads the last page and hides itself, and a hidden control cannot keep focus:
// it fell to <body>. The footer's count ("Showing N of N") takes it instead, so the keyboard user hears where they are.
test("Show more hiding while it has focus hands focus to the footer count; elsewhere nothing moves", () => {
  const { run, el } = auditEnv();
  const more = el("au-more"), count = el("au-count");
  assert.strictEqual(count.attrs.tabindex, "-1", "the count takes focus by script only: it is not a tab stop");
  assert.strictEqual(byId(auditTree(), "au-count").attrs.tabindex, "-1", "...in the markup, not set by the script");
  const active = () => run("document.activeElement");
  run('auData.listState = "ok"; auData.total = 147; auData.events = new Array(100).fill({}); auData.next = "17.2.9"; auPaintFoot();');
  assert.strictEqual(more.hidden, false);

  // Show more stays (another page follows): focus stays on it, the count is not touched.
  more.focus();
  run("auPaintFoot();");
  assert.strictEqual(active(), more, "no change while Show more stays");
  // The last page arrives: it hides, and the count has focus.
  run('auData.events = new Array(147).fill({}); auData.next = ""; auPaintFoot();');
  assert.strictEqual(more.hidden, true);
  assert.strictEqual(active(), count, "focus moves to the count");
  assert.strictEqual(count.textContent, "Showing 147 of 147 · newest first", "which now says where the user is");

  // Focus somewhere else (a Kind item, say) is never stolen when Show more hides.
  run('auData.next = "17.2.9"; auPaintFoot();');
  const kind = el("au-filters").elements[1];
  kind.focus();
  run('auData.next = ""; auPaintFoot();');
  assert.strictEqual(active(), kind, "focus the user moved on stays where they put it");
  // And with focus on nothing in the page, nothing is pulled in.
  run('auData.next = "17.2.9"; auPaintFoot();');
  focused = null;
  run('auData.next = ""; auPaintFoot();');
  assert.strictEqual(active(), run("document.body"), "no focus before, none after");
});

// What Chromium really does (measured on the review instance, Gate fix wave): the button is disabled while the page loads, and that
// already drops focus to <body>, so at the moment the last page hides it nothing is focused. Pressing it remembers the focus.
test("Show more pressed by keyboard: focus lost to the load goes to the count, or back to Show more when more pages follow", () => {
  const { run, el, ctx } = auditEnv();
  ctx.fetch = () => new Promise(() => {}); // the load never answers: the test plays its reply
  const more = el("au-more"), count = el("au-count");
  const active = () => run("document.activeElement");
  const press = () => {
    run('auData.listState = "ok"; auData.events = new Array(100).fill({}); auData.total = 300; auData.next = "17.2.9"; auPaintFoot();');
    more.focus();
    more.listeners.click[0]();
    assert.strictEqual(more.disabled, true, "disabled for the load");
    focused = null; // the browser's focus fixup
    assert.strictEqual(active(), run("document.body"));
  };
  // The last page arrives: Show more hides and the count has focus.
  press();
  run('auData.events = new Array(147).fill({}); auData.total = 147; auData.next = ""; auData.listState = "ok"; auPaintFoot();');
  assert.strictEqual(more.hidden, true);
  assert.strictEqual(active(), count);
  // Another page follows: the button comes back enabled and takes its focus back.
  press();
  run('auData.events = new Array(200).fill({}); auData.next = "17.2.8"; auData.listState = "ok"; auPaintFoot();');
  assert.strictEqual(more.hidden, false);
  assert.strictEqual(more.disabled, false);
  assert.strictEqual(active(), more, "still the control the keyboard user was on");
  // Painting while it loads, or the summary's paint, never moves anything.
  press();
  run('auPaintFoot();');
  assert.strictEqual(active(), run("document.body"), "no focus is given while the page loads");
  // The user moved on while it loaded (a Kind item): their focus stays, and the next pages are no longer theirs to hand back.
  const kind = el("au-filters").elements[2];
  kind.focus();
  run('auData.next = ""; auData.listState = "ok"; auPaintFoot();');
  assert.strictEqual(active(), kind);
  run('auData.next = "x"; auPaintFoot();');
  focused = null;
  run('auData.next = ""; auPaintFoot();');
  assert.strictEqual(active(), run("document.body"), "the press was answered once; a later hide does not pull focus");
  // A new filter, range or session forgets a press in flight.
  press();
  run("auReload(false);");
  run('auData.listState = "ok"; auData.next = "z"; auPaintFoot();');
  assert.strictEqual(active(), run("document.body"));
});

// --- Audit: the CSS ----------------------------------------------------------------------------------------------

test("no rule styles the old Audit page: #au-filters button.on, .au-kpi, .au-elev, .dhd", () => {
  const sels = [...RULES, ...parseRules(APP_CSS)].flatMap((r) => parts(r.selector));
  const gone = [
    /#au-(?:filters|range)[^,]*button\.on\b/, /\.au-kpis?(?![\w-])/, /\.au-elev(?![\w-])/, /\.au-(?:n|l)(?![\w-])/,
    /\.au-filters(?![\w-])/, /#audit-page[^,]*\.dhd(?![\w-])/, /#au-range\b/, /#au-esc\b/, /#au-csv\b/,
  ];
  for (const re of gone) assert.deepStrictEqual(sels.filter((s) => re.test(s)), [], String(re));
  // No color-mix in any Audit rule: the tint is the --red-tint token (Safari 16 floor).
  const audit = RULES.filter((r) => parts(r.selector).some((s) => /#audit-page|#au-|\.au-/.test(s)));
  assert.ok(audit.length > 10, "the Audit rules are found");
  for (const r of audit) {
    for (const [prop, v] of r.decls) assert.ok(!/color-mix/.test(v), r.selector + " { " + prop + " } uses color-mix");
  }
});

test("the Audit rules sit after the kit fence, prefixed with the page", () => {
  const raw = read("desktop.css");
  const open = raw.indexOf("/* === kit primitives");
  const close = raw.indexOf("/* === end kit primitives === */");
  assert.ok(open > 0 && close > open);
  const mine = (sel) => /#audit-page|#au-|\.au-/.test(sel);
  // Inside the fence: nothing of this page (the kit has no page prefix and no page classes).
  const fence = parseRules(stripComments(raw.slice(open, close))).flatMap((r) => parts(r.selector));
  assert.deepStrictEqual(fence.filter(mine), []);
  // After it: every Audit selector starts at the page.
  const after = parseRules(stripComments(raw.slice(close))).flatMap((r) => parts(r.selector)).filter(mine);
  assert.ok(after.length > 20, "the Audit rules are found: " + after.length);
  assert.deepStrictEqual(after.filter((s) => !s.startsWith(AU)), []);
});

test("the Audit head is the kit .page-head with M3's padding, 24px 32px 12px, and stays put", () => {
  const head = decls(AU + " > .page-head");
  assert.strictEqual(head.get("padding"), "var(--sp-6) var(--sp-8) var(--sp-3)", "digest-design §B1: .m3-pagehead, not the kit's 20px under the head");
  assert.strictEqual(head.get("flex"), "none", "the head does not scroll (the plan's call 9)");
  // The kit's own rule is what draws it: 18/24/600 title, 13/20 muted line, 8px between the buttons.
  assert.strictEqual(decls(".page-title").get("font-size"), "var(--t-xl)");
  assert.strictEqual(decls(".page-desc").get("color"), "var(--muted)");
  assert.strictEqual(decls(".page-actions").get("gap"), "var(--sp-2)");
  // No divider before Esc ✕ (M2's .set-sep is Settings'), and the Esc button is the kit's 28px ghost button.
  assert.deepStrictEqual(RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(AU) && /sep|divider/.test(s))).map((r) => r.selector), []);
  assert.strictEqual(decls(".btn").get("min-height"), "var(--h-ctl)");
  assert.strictEqual(decls(".btn-ghost").get("color"), "var(--muted)");
});

test("the Audit toolbar is the kit .toolbar with M3's padding, 0 32px 16px, and the Session select is 180px", () => {
  const tools = decls(AU + " > .toolbar.au-tools");
  assert.strictEqual(tools.get("padding"), "0 var(--sp-8) var(--sp-4)", "digest-design §B2: .m3-tools");
  assert.strictEqual(tools.get("flex-wrap"), "wrap", "a narrow window wraps the row instead of clipping it");
  const bare = decls(".toolbar");
  assert.strictEqual(bare.get("display"), "flex");
  assert.strictEqual(bare.get("gap"), "var(--sp-3)");
  assert.strictEqual(bare.get("flex"), "none");
  const sel = decls(AU + " #au-session");
  assert.strictEqual(sel.get("width"), "180px", "§B4: the frame's inline width");
  assert.strictEqual(sel.get("flex"), "none");
  assert.strictEqual(decls("select.input").get("appearance"), "none");
  assert.strictEqual(decls(".input").get("height"), "var(--h-lg)", "§G3: 32px as drawn, not the 28px .input-sm");
  // The seg is the kit's: 28px group, 24px items, pressed = --raise with no weight change.
  const pressed = decls('.seg > .seg-item[aria-pressed="true"]');
  assert.strictEqual(pressed.get("background"), "var(--raise)");
  assert.strictEqual(pressed.get("font-weight"), undefined);
  assert.strictEqual(decls(".seg > .seg-item").get("height"), "var(--h-sm)");
});

test("everything from the KPI row down scrolls in .au-scroll: 32px sides, 24px under, 16px between", () => {
  const sc = decls(AU + " > .au-scroll");
  assert.strictEqual(sc.get("flex"), "1 1 0");
  assert.strictEqual(sc.get("min-height"), "0");
  assert.strictEqual(sc.get("overflow"), "auto");
  assert.strictEqual(sc.get("padding"), "0 var(--sp-8) var(--sp-6)", "§B0: .m3-body");
  assert.strictEqual(sc.get("display"), "flex");
  assert.strictEqual(sc.get("flex-direction"), "column");
  assert.strictEqual(sc.get("gap"), "var(--sp-4)");
  assert.strictEqual(decls(AU + " > .au-scroll > *").get("flex"), "none", "a column flex scroller would shrink its children instead of scrolling");
  // The page itself is the base page rule's column: head and toolbar above, the scroller taking the rest.
  assert.strictEqual(decls("#page-host > dialog.as-page").get("flex-direction"), "column");
  const card = decls(AU + " .au-card");
  assert.strictEqual(card.get("background"), "var(--panel)");
  assert.strictEqual(card.get("border-radius"), "var(--r-card)");
  assert.strictEqual(card.get("overflow"), "hidden", "clips the row tint to the 8px corners (§D2)");
});

test("the Audit KPI row is the kit's, and its labels ellipsize", () => {
  const row = decls(".kpi-row");
  assert.strictEqual(row.get("grid-auto-flow"), "column");
  assert.strictEqual(row.get("grid-auto-columns"), "minmax(0, 1fr)");
  assert.strictEqual(decls(".kpi-value").get("font-size"), "var(--t-kpi)");
  assert.strictEqual(decls(".kpi-value.is-bad").get("color"), "var(--red)");
  assert.strictEqual(decls(".kpi-label").get("color"), "var(--muted)");
  const label = decls(AU + " .kpi-label");
  assert.strictEqual(label.get("overflow"), "hidden");
  assert.strictEqual(label.get("text-overflow"), "ellipsis");
  assert.strictEqual(label.get("white-space"), "nowrap");
});

test("the Audit table is fixed at 116/150/112/auto/150, and only the Time cell is tabular", () => {
  const tbl = decls(AU + " .au-tbl");
  assert.strictEqual(tbl.get("table-layout"), "fixed");
  const w = (n) => decls(AU + " .au-tbl th:nth-child(" + n + ")").get("width");
  assert.deepStrictEqual([1, 2, 3, 4, 5].map(w), ["116px", "150px", "112px", undefined, "150px"], "§B9: Time, Session, Kind, What (auto), Outcome");
  // One table style: the kit's. The page does not restyle the heads (no uppercase, no 600) or the cell height.
  const heads = BASE_RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(AU) && /\bth\b/.test(s) && !/nth-child/.test(s)));
  assert.deepStrictEqual(heads.map((r) => r.selector), [], "(the narrow card's tighter cell padding is the one container rule that names th)");
  assert.strictEqual(decls(".tbl th").get("font-size"), "var(--t-sm)");
  assert.strictEqual(decls(".tbl th").get("text-transform"), undefined);
  // Time: sans, muted, tabular (not the mono 12px it was).
  const time = decls(AU + " .au-tbl td.au-time");
  assert.strictEqual(time.get("color"), "var(--muted)");
  assert.strictEqual(time.get("font-variant-numeric"), "tabular-nums");
  assert.strictEqual(time.get("font-family"), undefined, "sans, inherited");
  assert.strictEqual(decls(AU + " .au-tbl td.au-what").get("font-family"), undefined, "What is sans too; Task 3 marks the identifiers mono");
  // Kind and Outcome rest muted.
  assert.strictEqual(decls(AU + " .au-tbl td.au-kind").get("color"), "var(--muted)");
  assert.strictEqual(decls(AU + " .au-tbl td.au-out").get("color"), "var(--muted)");
});

// A window under about 1124px leaves the card under 770px, where the fixed columns (528px) would leave What nothing: the
// table then sizes its columns to their content (Review Focus 2: never clip mid-cell, nothing scrolls sideways).
test("a card too narrow for the fixed columns sizes them to content, in a container query", () => {
  const card = decls(AU + " .au-card");
  assert.strictEqual(card.get("container-type"), "inline-size");
  assert.strictEqual(card.get("container-name"), "au-card");
  assert.strictEqual(card.get("overflow"), "hidden", "still clips the row tint to the corners where there is room");
  const fixed = [1, 2, 3, 5].reduce((n, i) => n + parseInt(decls(AU + " .au-tbl th:nth-child(" + i + ")").get("width"), 10), 0);
  assert.strictEqual(fixed, 528);
  // Three @containers, all on the card: the first below the fixed columns plus ~240px for What, the second (the narrow tier) below,
  // the third at and above the fixed layout (the Outcome's one line, next test).
  const queries = [...new Set(CONTAINER_RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(AU + " .au-tbl"))).flatMap((r) => r.at.filter((a) => a.startsWith("@container"))))];
  assert.deepStrictEqual(queries, ["@container au-card (width < 770px)", "@container au-card (max-width: 479px)", "@container au-card (width >= 770px)"]);
  // The pair is range syntax so a fractional card width (769.5px) falls in exactly one of them.
  const limit = Number(/width < (\d+)px/.exec(queries[0])[1]);
  assert.ok(limit >= fixed + 240, "What keeps about 240px (" + (limit - fixed) + "px)");
  assert.ok(limit <= fixed + 260, "and the fixed layout is not given up sooner than that");
  const tier = (q) => CONTAINER_RULES.filter((r) => r.at.includes(q === 769 ? "@container au-card (width < 770px)" : "@container au-card (max-width: " + q + "px)"));
  const inside = (sel, q = 769) => decls(sel, tier(q));
  assert.strictEqual(inside(AU + " .au-tbl").get("table-layout"), "auto");
  for (const i of [1, 2, 3, 5]) assert.strictEqual(inside(AU + " .au-tbl th:nth-child(" + i + ")").get("width"), "auto", "th " + i);
  // Nothing else changes in the narrow card: What stays the one column with no width of its own, and the wrap cells keep
  // wrapping (the base rules above are not repeated or undone here).
  assert.deepStrictEqual(tier(769).filter((r) => parts(r.selector).some((s) => s.startsWith(AU))).flatMap((r) => [...r.decls.keys()]).sort(),
    ["display", "table-layout", "width"]);
  assert.strictEqual(inside(AU + " .au-tbl :is(td.au-kind, td.au-out) > .ui-icon").get("display"), "none", "no glyph where What would pay for it");

  // Under 480px of card (a window of 840px or less) the columns' own minimums still overran it (Gate 3 D1: What 54px at 701px, the
  // table 53px wider than its card, Outcome cut). Time, Session, Kind and Outcome are held to their narrowest (1%: a column never
  // gets less than its content's minimum) so What, the one column with no width of its own, takes the rest.
  for (const i of [1, 2, 3, 5]) assert.strictEqual(inside(AU + " .au-tbl th:nth-child(" + i + ")", 479).get("width"), "1%", "narrow th " + i);
  assert.strictEqual(inside(AU + " .au-tbl th:nth-child(4)", 479).get("width"), undefined, "What takes the rest");
  // The cells give up 4px of side padding (the kit's --sp-2 for --sp-3), heads and cells alike, and the Time cell wraps at its space.
  const pad = inside(AU + " .au-tbl :is(th, td)", 479);
  assert.strictEqual(pad.get("padding-left"), "var(--sp-2)");
  assert.strictEqual(pad.get("padding-right"), "var(--sp-2)");
  assert.strictEqual(pad.get("padding-top"), undefined, "the wrap cells' 6px above and below stay");
  assert.strictEqual(inside(AU + " .au-tbl td.au-time", 479).get("white-space"), "normal", "date over time, not one line of 107px");
  assert.strictEqual(decls(AU + " .au-tbl .au-nb").get("white-space"), "nowrap", "...at the space only: the date is one run, a hyphen would be a break of its own");
  assert.strictEqual(inside(AU + " .au-tbl td.au-time").get("white-space"), undefined, "wider than that the time stays one line");
  // Kind stays on one line: nothing in either tier lets it wrap (the kit's td is nowrap and no rule undoes it).
  assert.strictEqual(inside(AU + " .au-tbl td.au-kind", 479).get("white-space"), undefined);
  assert.strictEqual(decls(".tbl td").get("white-space"), "nowrap");
  assert.deepStrictEqual(tier(479).filter((r) => parts(r.selector).some((s) => s.startsWith(AU))).flatMap((r) => [...r.decls.keys()]).sort(),
    ["padding-left", "padding-right", "white-space", "width"]);
  assert.strictEqual(decls(AU + " .au-tbl td.au-what").get("white-space"), "normal");
  assert.strictEqual(decls(AU + " .au-tbl td.au-what").get("overflow-wrap"), "anywhere");
  // The base rules come first: a container rule has the same weight as the base one it undoes, so the later one wins.
  const src = stripComments(read("desktop.css"));
  assert.ok(src.indexOf("@container au-card") > src.indexOf(AU + " .au-tbl th:nth-child(5) { width: 150px; }"));
});

// The Outcome stays on one line only in the fixed layout (a card of 770px or more), where its glyph shows and the 150px column has
// no room to spare (#168 G2 22). The cascade is simulated: every rule that lists the Outcome cell, in source order, whose container
// condition holds at that card width; the last white-space wins. A container rule has no extra weight, so this fails if the
// nowrap comes before the narrow card's wrap or is a base rule.
test("the Outcome is one line at a card of 770px or more and wraps below, whatever the rule order", () => {
  const sel = AU + " .au-tbl td.au-out";
  // The condition is one of (min-width: N), (max-width: N), (width < N) or (width >= N); anything else fails closed (holds nothing).
  const holds = (r, w) => r.at.filter((a) => a.startsWith("@container")).every((a) => {
    const m = /\((?:(min|max)-width:\s*(\d+)px|width (<|>=) (\d+)px)\)$/.exec(a);
    if (!m) return false;
    if (m[1]) return m[1] === "min" ? w >= Number(m[2]) : w <= Number(m[2]);
    return m[3] === "<" ? w < Number(m[4]) : w >= Number(m[4]);
  });
  const ws = (w) => {
    let v;
    for (const r of RULES) if (parts(r.selector).includes(sel) && r.decls.has("white-space") && holds(r, w)) v = r.decls.get("white-space");
    return v;
  };
  for (const w of [340, 399, 479, 480, 600, 769, 769.5]) assert.strictEqual(ws(w), "normal", w + "px card wraps");
  for (const w of [770, 919, 1079]) assert.strictEqual(ws(w), "nowrap", w + "px card is one line");
  // The nowrap is a container rule on the card, not a base rule.
  assert.ok(!BASE_RULES.some((r) => parts(r.selector).includes(sel) && r.decls.get("white-space") === "nowrap"));
  // Its glyph is hidden exactly where the Outcome wraps.
  const hidden = (w) => CONTAINER_RULES.some((r) => parts(r.selector).includes(AU + " .au-tbl :is(td.au-kind, td.au-out) > .ui-icon") && r.decls.get("display") === "none" && holds(r, w));
  assert.strictEqual(hidden(769), true);
  assert.strictEqual(hidden(769.5), true);
  assert.strictEqual(hidden(770), false);
});

test("the Time cell keeps its text and holds the date as one run, so a narrow card wraps it at the space", () => {
  const { run } = auditEnv();
  const td = run("auRow({ ts: 1_760_000_000, kind: \"credential\", what: \"aws sandbox\", outcome: \"minted\" }, {})").elements[0];
  assert.strictEqual(td.className, "au-time");
  assert.match(td.textContent, /^\d\d-\d\d \d\d:\d\d$/, "the text is MM-DD HH:MM, as ever");
  assert.deepStrictEqual(td.elements.map((n) => [n.tag, n.className, /^\d\d-\d\d$/.test(n.textContent)]), [["span", "au-nb", true]], "the date alone is the unbreakable run");
  // No timestamp: the cell is empty text, not a crash.
  assert.strictEqual(run("auRow({ kind: \"credential\", what: \"x\", outcome: \"minted\" }, {})").elements[0].textContent, "");
});

// #168 G2 14-23: the Kind and Outcome cells carry a 14px glyph before their word (the text is unchanged), the kind filters and
// Export CSV carry theirs as static placeholders; the page is the desktop's (openAudit returns on a phone).
test("an audit row's Kind and Outcome cells draw their glyph first and keep their words", () => {
  const { run } = auditEnv();
  const cells = (ev) => run("auRow(" + JSON.stringify(ev) + ", {})").elements;
  const glyph = (td) => { const g = td.elements.filter((n) => n.tag === "svg"); return g.length ? [g[0].attrs["data-icon"], g[0].attrs.width, td.children[0] === g[0]] : null; };
  let c = cells({ ts: 1_760_000_000, kind: "credential", provider: "github", what: "github x", outcome: "minted" });
  assert.deepStrictEqual(glyph(c[2]), ["si:github", "14", true]);
  assert.strictEqual(c[2].textContent, "Credential");
  assert.deepStrictEqual(glyph(c[4]), ["circle-check", "14", true]);
  assert.strictEqual(c[4].textContent, "minted");
  assert.strictEqual(c[2].className, "au-kind");
  assert.strictEqual(c[4].className, "au-out au-ok");
  // A granted elevation: its Kind glyph inherits the red cell, and no Outcome glyph.
  c = cells({ ts: 1_760_000_000, kind: "elevation", what: "write", outcome: "granted", actor: "you" });
  assert.deepStrictEqual(glyph(c[2]), ["shield-alert", "14", true]);
  assert.strictEqual(c[2].className, "au-kind au-bad");
  assert.strictEqual(glyph(c[4]), null);
  assert.strictEqual(c[4].textContent, "granted · you");
  // An older server: no provider, key-round; an unknown kind and a neutral outcome draw no glyph, never "undefined".
  assert.strictEqual(glyph(cells({ ts: 1, kind: "credential", what: "x", outcome: "minted" })[2])[0], "key-round");
  c = cells({ ts: 1, kind: "mystery", what: "x", outcome: "created" });
  assert.strictEqual(glyph(c[2]), null);
  assert.strictEqual(c[2].textContent, "mystery");
  assert.strictEqual(glyph(c[4]), null);
});

test("the Audit page's kind filters and Export CSV hold their glyph placeholders; All stays plain", () => {
  const { el } = auditEnv();
  const holes = (b) => b.elements.filter((n) => n.tag === "svg").map((n) => [n.dataset.uicon, n.attrs.width, n.attrs.height, n.attrs["aria-hidden"]]);
  const want = { "": null, credentials: "key-round", elevations: "shield-alert", egress: "globe", settings: "settings" };
  for (const b of el("au-filters").elements) {
    const g = want[b.dataset.filter];
    assert.deepStrictEqual(holes(b), g ? [[g, "14", "14", "true"]] : [], b.dataset.filter || "All");
  }
  assert.deepStrictEqual(holes(el("au-csv")), [["download", "14", "14", "true"]]);
  assert.strictEqual(el("au-csv").textContent, "Export CSV");
  // The glyphs are the desktop page's: the Audit page does not open on a phone (no app.css rule names it).
  assert.match(stripJsComments(read("auditpage.js")), /function openAudit[\s\S]*?mobileActive\(window\.innerWidth\)\) return;/);
});

// Gate 3 D1 (toolbar half) and D4: a seg cannot wrap, so a toolbar narrower than its widest seg lets the seg run past the page.
test("both pages' toolbars tighten their seg items when narrower than the widest seg, so 701px never scrolls sideways", () => {
  const shared = BASE_RULES.filter((r) => parts(r.selector).includes(AU + " > .toolbar.au-tools") && r.decls.has("container-type"));
  assert.strictEqual(shared.length, 1);
  assert.deepStrictEqual(parts(shared[0].selector), [AU + " > .toolbar.au-tools", ANP + " > .toolbar.an-ptools"], "the same rule makes Audit's toolbar and Analytics' a container (not the Custom range row)");
  assert.strictEqual(shared[0].decls.get("container-type"), "inline-size");
  assert.strictEqual(shared[0].decls.get("container-name"), "page-tools");
  const narrow = CONTAINER_RULES.filter((r) => r.at.includes("@container page-tools (max-width: 388px)"));
  assert.strictEqual(narrow.length, 1);
  assert.deepStrictEqual(parts(narrow[0].selector), [AU + " > .toolbar .seg > .seg-item", ANP + " > .toolbar .seg > .seg-item"]);
  assert.deepStrictEqual([...narrow[0].decls], [["padding", "0 var(--sp-1)"]], "10px a side becomes the kit's 4px, nothing else");
  assert.strictEqual(decls(".seg > .seg-item").get("padding"), "0 10px", "the kit's own padding is what the query takes 6px a side from");
  // The Kind seg is 389px (5 items: text 277 + 20 a side each + gaps and padding 12); under the query it is 389 - 5 * 12 = 329.
  // The page is 404px at 701 (the window less the sidebar's 290px and its 7px edge) and the toolbar's content 340px.
  assert.ok(329 <= 404 - 64 && 389 > 404 - 64, "329px fits the 340px a 701px window leaves; 389px does not");
});

// Gate 4 L1: the pages' text sits on the frame's 20px line.
test("the Audit and Analytics pages' text sits on the kit's 20px line, not the body's 19.5px", () => {
  assert.strictEqual(decls(AU).get("line-height"), "var(--lh-md)", "inherited by every cell, the Session select and the buttons");
  assert.strictEqual(decls("#page-host > #analytics-modal.as-page").get("line-height"), "var(--lh-md)", "and by Analytics' tables, bands and legends");
  const root = BASE_RULES.filter((r) => parts(r.selector).includes(AU) && r.decls.has("line-height"));
  assert.strictEqual(root.length, 1, "one rule");
  assert.deepStrictEqual(parts(root[0].selector), [AU, "#page-host > #analytics-modal.as-page"], "for both pages, so they cannot drift");
  assert.strictEqual(decls(":root").get("--lh-md"), "20px");
  // Cells that wrapped already set it, and the type utilities set their own: the page rule only reaches one-line text.
  assert.strictEqual(decls(AU + " .au-tbl td.au-what").get("line-height"), "var(--lh-md)");
  assert.strictEqual(decls(".t-sm").get("line-height"), "var(--lh-sm)");
});

test("Session, What and Outcome wrap by default: normal white-space, anywhere, 20px lines, 6px above and below", () => {
  for (const cell of ["au-sess", "au-what", "au-out"]) {
    const c = decls(AU + " .au-tbl td." + cell);
    // The base rule: the Outcome's one line in the fixed layout is a container rule on top of this (next test).
    assert.strictEqual(c.get("white-space"), "normal", cell);
    assert.strictEqual(c.get("overflow-wrap"), "anywhere", cell);
    assert.strictEqual(c.get("line-height"), "var(--lh-md)", cell);
    assert.strictEqual(c.get("padding-top"), "6px", cell);
    assert.strictEqual(c.get("padding-bottom"), "6px", cell);
    assert.strictEqual(c.get("height"), "auto", cell + ": beats .tbl td's 32px so a second line grows the row");
    assert.strictEqual(c.get("text-overflow"), undefined, cell + ": no ellipsis");
    assert.strictEqual(c.get("overflow"), undefined, cell);
  }
  // A 32px row when the text is one line: 6 + 20 + 6, as the frame's 33px pitch with the 1px rule.
  assert.strictEqual(decls(".tbl td").get("height"), "var(--h-row)");
  assert.strictEqual(decls(AU + " .au-tbl td.au-what .au-reason").get("color"), "var(--muted)", "the reason is muted sans");
  assert.strictEqual(decls(AU + " .au-tbl td.au-what .au-reason").get("font-family"), undefined);
  // The message rows (Loading…, empty, failed) are one centred cell.
  assert.strictEqual(decls(AU + " .au-tbl td.au-empty").get("text-align"), "center");
});

test("an elevated row is --red-tint and keeps it under the pointer (§G8)", () => {
  assert.strictEqual(decls(".tbl tr.is-elevated").get("background"), "var(--red-tint)");
  const hover = decls(AU + " .au-tbl tbody tr.is-elevated:hover");
  assert.strictEqual(hover.get("background"), "var(--red-tint)", "the kit's .tbl tbody tr:hover would turn it --hover");
  assert.strictEqual(decls(".tbl tbody tr:hover").get("background"), "var(--hover)", "every other row still lights up");
  // The tint is the token (red 10% over --bg, precomputed), not a mix.
  assert.strictEqual(decls(":root").get("--red-tint"), "#24181b");
});

test("Outcome tones: .au-ok is --ok, .au-bad is --red at 500 (Kind and Outcome only)", () => {
  assert.strictEqual(decls(AU + " .au-tbl td.au-ok").get("color"), "var(--ok)");
  for (const cell of ["au-kind", "au-out"]) {
    const bad = decls(AU + " .au-tbl td." + cell + ".au-bad");
    assert.strictEqual(bad.get("color"), "var(--red)", cell);
    assert.strictEqual(bad.get("font-weight"), "var(--w-medium)", cell);
  }
  assert.strictEqual(decls(".w-500").get("font-weight"), "var(--w-medium)");
  // The failure message keeps its red without the weight.
  const fail = decls(AU + " .au-tbl td.au-empty.au-bad");
  assert.strictEqual(fail.get("color"), "var(--red)");
  assert.strictEqual(fail.get("font-weight"), undefined);
  // The page never reaches for amber.
  const audit = RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(AU)));
  for (const r of audit) for (const [, v] of r.decls) assert.ok(!/--amber/.test(v), r.selector);
});

test("the footer is in flow under the table: Showing N of M, Show more, a spacer, the retention line", () => {
  const foot = decls(AU + " .au-foot");
  assert.strictEqual(foot.get("display"), "flex");
  assert.strictEqual(foot.get("align-items"), "center");
  assert.strictEqual(foot.get("gap"), "var(--sp-3)", "§B9: .m3-foot");
  assert.strictEqual(foot.get("font-size"), "var(--t-sm)");
  assert.strictEqual(foot.get("color"), "var(--muted)");
  assert.strictEqual(foot.get("min-height"), "var(--h-sm)", "the 24px a Show more button makes, so the row does not jump when it hides");
  assert.strictEqual(foot.get("position"), undefined, "not sticky: it scrolls with the table");
  assert.strictEqual(decls(AU + " #au-count").get("font-variant-numeric"), "tabular-nums");
  // The kit .btn sets a display, which defeats the UA [hidden] rule and the product has no global one: this page rule
  // is what keeps a hidden Show more hidden (and it out-weighs .btn).
  const hidden = decls(AU + " #au-more[hidden]");
  assert.strictEqual(hidden.get("display"), "none");
  assert.strictEqual(decls(".btn").get("display"), "inline-flex");
});

test("every class the Audit markup and script use has a rule or is a named hook", () => {
  const used = new Set();
  for (const m of dialogHtml("audit-page").matchAll(/class="([^"]*)"/g)) for (const c of m[1].split(/\s+/)) used.add(c);
  // The script's class names: any string literal word that looks like one of the page's (au-…, kpi…, is-…) and is not
  // an element id of the markup (au-csv, au-body…).
  const ids = new Set([...dialogHtml("audit-page").matchAll(/\bid="([^"]+)"/g)].map((m) => m[1]));
  const js = stripJsComments(read("auditpage.js"));
  for (const m of js.matchAll(/"([^"\n]*)"/g)) {
    for (const c of m[1].split(/\s+/)) if (/^(?:au-|kpi|is-)[a-z-]*$/.test(c) && !ids.has(c)) used.add(c);
  }
  for (const c of ["kpi", "kpi-value", "kpi-label", "is-bad", "is-elevated", "au-bad", "au-ok", "au-empty", "au-reason"]) assert.ok(used.has(c), c + " is used");
  // page-esc is a hook (like set-esc): it marks the Esc ✕ button for the tests and the pages' same-classes check; its
  // whole look is the kit's .btn.btn-ghost, so no rule names it.
  const hooks = new Set(["page-esc"]);
  const classnames = fs.readFileSync(path.join(__dirname, "classnames.test.js"), "utf8");
  assert.match(classnames, /"page-esc"/, "page-esc is listed in classnames.test.js HOOKS");
  const css = DESK_CSS + APP_CSS + stripComments(read("primitives.css"));
  const defined = new Set([...css.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)].map((m) => m[1]));
  const undef = [...used].filter((c) => !defined.has(c) && !hooks.has(c));
  assert.deepStrictEqual(undef, [], "classes with no rule");
});

// =====================================================================================================================
// Analytics: the head and the toolbar (Task 4; frames M3.1/M3.2, digest-design §A1, §B1-§B4, §F3)
// =====================================================================================================================

const ANP = "#page-host > #analytics-modal.as-page";
const analyticsTree = () => kids(parseHtml(dialogHtml("analytics-modal")))[0];

// The phone's markup of #analytics-modal, from the title to the end: the page's nodes are added in front of it and none of
// it is edited, so the modal the phone opens is the same bytes as on main c47442b.
const PHONE_MARKUP = [
  "    <h2 class=\"pane-head dlg-title\">Analytics</h2>",
  "    <div class=\"dlg-head\">",
  "      <div class=\"dlg-tabs\" id=\"an-tabs\">",
  "        <button type=\"button\" data-antab=\"overview\" class=\"act\">Overview · Cost</button>",
  "        <button type=\"button\" data-antab=\"models\">Models</button>",
  "        <button type=\"button\" data-antab=\"projects\">Projects</button>",
  "        <button type=\"button\" data-antab=\"sessions\">Sessions</button>",
  "        <button type=\"button\" data-antab=\"tools\">Tools</button>",
  "      </div>",
  "      <span class=\"spacer\"></span>",
  "      <span id=\"analytics-range\" class=\"an-caption\" style=\"margin:0\"></span>",
  "      <span id=\"an-ctl\"></span>",
  "      <button id=\"analytics-refresh\" type=\"button\" title=\"Re-scan transcripts now\">Re-scan</button>",
  "      <button class=\"dlg-x\" type=\"button\" data-close=\"analytics-modal\" title=\"Close\">×</button>",
  "    </div>",
  "    <div class=\"dlg-body\">",
  "      <div id=\"analytics-body\"></div>",
  "    </div>",
  "  </dialog>",
].join("\n");

// stripPage: a node's class list without the classes that belong to one page, so both pages' components compare equal.
const PAGE_ONLY = new Set(["au-tools", "an-phead", "an-ptools", "an-pcustom"]);
const shape = (n) => n.tag + classes(n).filter((c) => !PAGE_ONLY.has(c)).map((c) => "." + c).join("");

test("Analytics and Audit share one head, toolbar and KPI component", () => {
  const au = auditTree();
  const an = analyticsTree();
  const [auHead, auTools] = kids(au);
  const [anHead, anTools] = kids(an);
  // The same class sequence: page-head > page-head-text > h1.page-title + p.page-desc; page-actions > … page-esc.
  assert.strictEqual(shape(anHead), "div.page-head");
  assert.strictEqual(shape(anHead), shape(auHead));
  const [anText, anActions] = kids(anHead);
  const [auText, auActions] = kids(auHead);
  assert.deepStrictEqual(kids(anText).map(shape), ["h1.page-title", "p.page-desc"]);
  assert.deepStrictEqual(kids(anText).map(shape), kids(auText).map(shape));
  assert.strictEqual(shape(anActions), "div.page-actions");
  assert.strictEqual(shape(anActions), shape(auActions));
  assert.strictEqual(shape(kids(anActions).pop()), "button.btn.btn-ghost.page-esc");
  assert.strictEqual(shape(kids(anActions).pop()), shape(kids(auActions).pop()));
  assert.deepStrictEqual(kids(kids(anActions).pop()).map(shape), kids(kids(auActions).pop()).map(shape), "both Esc buttons are span.kbd-hint then the x glyph");
  // The toolbar: .toolbar > .seg[role=group] > button.seg-item[aria-pressed], on both.
  assert.strictEqual(shape(anTools), "div.toolbar");
  assert.strictEqual(shape(anTools), shape(auTools));
  for (const tools of [auTools, anTools]) {
    const segs = kids(tools).filter((n) => classes(n).includes("seg"));
    assert.ok(segs.length >= 2, "a toolbar of segmented controls");
    for (const seg of segs) {
      assert.strictEqual(shape(seg), "div.seg");
      assert.strictEqual(seg.attrs.role, "group");
      assert.ok(seg.attrs["aria-label"], "a group is named");
      assert.ok(kids(seg).length >= 3);
      for (const b of kids(seg)) {
        assert.strictEqual(shape(b), "button.seg-item");
        assert.strictEqual(b.attrs.type, "button");
        assert.ok(b.attrs["aria-pressed"] === "true" || b.attrs["aria-pressed"] === "false", "aria-pressed, not .on");
      }
    }
  }
  // The KPI row. Audit's builder emits the kit's kpi-row > kpi > kpi-value + kpi-label, and so does Analytics' (anKpiRow):
  // the same tags and the same class sequence, one component on both pages.
  const { run, el } = auditEnv();
  run("auData.summary = { issued: 1, refused: 0, elevations: 0, egress_allowed: 0, egress_denied: 0 }; auPaintKpis();");
  assert.deepStrictEqual(classes(kids(kids(au)[2])[0]), ["kpi-row"], "the KPI tiles' row is the kit's .kpi-row");
  assert.deepStrictEqual([...new Set(el("au-kpis").elements.map((t) => t.className))], ["kpi"]);
  assert.deepStrictEqual([...new Set(el("au-kpis").elements.flatMap((t) => t.elements.map((c) => c.className)))], ["kpi-value", "kpi-label"]);
  const auShape = (tile) => [tile.tag, tile.className, tile.elements.map((c) => [c.tag, c.className])];
  const auTile = auShape(el("au-kpis").elements[0]);
  const aenv = analyticsEnv();
  aenv.page(true);
  const arow = aenv.run('anKpiRow([{ value: "1", label: "A" }, { value: "2", label: "B", title: "Bee" }])');
  assert.strictEqual(arow.tag, "div");
  assert.strictEqual(arow.className, "kpi-row", "the same row class as Audit's #au-kpis");
  assert.strictEqual(arow.elements.length, 2);
  for (const t of arow.elements) assert.deepStrictEqual(auShape(t), auTile, "an Analytics tile is an Audit tile: div.kpi > span.kpi-value + span.kpi-label");
  assert.deepStrictEqual(arow.elements.map((t) => t.elements.map((c) => c.textContent)), [["1", "A"], ["2", "B"]]);
  assert.deepStrictEqual(arow.elements.map((t) => t.elements[1].attrs.title), ["A", "Bee"], "an ellipsized label keeps its full text in title (its own, else the label)");
});

test("the Analytics markup: the page's nodes in front, the phone's modal after them byte for byte", () => {
  const html = dialogHtml("analytics-modal");
  assert.ok(html.startsWith('<dialog id="analytics-modal" box-="square" shear-="top">\n'), "the opening tag is untouched");
  assert.ok(html.endsWith(PHONE_MARKUP), "everything the phone shows is the markup of main c47442b");
  const an = analyticsTree();
  assert.deepStrictEqual(kids(an).map(sig), ["div.page-head.an-phead", "div.toolbar.an-ptools", "div.toolbar.an-pcustom", "h2.pane-head.dlg-title", "div.dlg-head", "div.dlg-body"]);
  const [head, tools, custom] = kids(an);

  // The head (digest-design §A1, the copy of M3.1 and M3.2).
  assert.deepStrictEqual(kids(head).map(sig), ["div.page-head-text", "div.page-actions"]);
  const [headText, actions] = kids(head);
  assert.deepStrictEqual(kids(headText).map(sig), ["h1.page-title", "p.page-desc"]);
  assert.strictEqual(text(kids(headText)[0]), "Analytics");
  assert.strictEqual(kids(headText)[1].attrs.id, "an-pdesc");
  assert.strictEqual(text(kids(headText)[1]), "", "the description is painted from the window (anSyncPageControls)");
  assert.deepStrictEqual(kids(actions).map(sig), ["button.btn", "button.btn.btn-ghost.page-esc"]);
  const [rescan, esc] = kids(actions);
  assert.strictEqual(rescan.attrs.id, "an-rescan");
  assert.strictEqual(rescan.attrs.type, "button");
  assert.strictEqual(rescan.attrs.title, "Re-scan transcripts now");
  assert.strictEqual(text(rescan), "Re-scan");
  assert.strictEqual(esc.attrs.type, "button");
  assert.strictEqual(esc.attrs["data-close"], "analytics-modal", "initDialogs wires every [data-close] button");
  assert.strictEqual(esc.attrs["aria-label"], "Close (Esc)");
  assert.strictEqual(esc.attrs.title, "Close (Esc)");
  assert.deepStrictEqual(kids(esc).map(sig), ["span.kbd-hint", "svg"]);
  assert.strictEqual(text(esc), "Esc", "no ✕ text glyph (#168)");
  assert.strictEqual(kids(esc)[1].attrs["data-uicon"], "x");
  assert.strictEqual(kids(esc)[1].attrs.width, "12");

  // The toolbar: View seg | Where | spacer | Range seg | Compare (§B2, §3.0).
  assert.deepStrictEqual(kids(tools).map(sig), ["div.seg", "label.an-where", "span.grow", "div.seg", "button.btn"]);
  const [view, where, , range, compare] = kids(tools);
  assert.strictEqual(view.attrs.id, "an-pview");
  assert.strictEqual(view.attrs.role, "group");
  assert.strictEqual(view.attrs["aria-label"], "View");
  assert.deepStrictEqual(kids(view).map((b) => b.attrs["data-antab"]), ["overview", "workspaces", "drivers", "sessions", "models"]);
  assert.deepStrictEqual(kids(view).map(text), ["Overview", "Workspaces", "Cost drivers", "Sessions", "Models"], "§3.0: the five views");
  // The Where select (§3.0): "Where:" in its label, a native select inside it with All as its one option before the first load.
  assert.deepStrictEqual(kids(where).map(sig), ["span.t-sm.muted", "select.input.input-sm.an-wsel"]);
  assert.strictEqual(text(kids(where)[0]), "Where:");
  assert.strictEqual(kids(where)[1].attrs.id, "an-where");
  assert.deepStrictEqual(kids(kids(where)[1]).map((o) => [o.attrs.value, text(o)]), [["", "All"]]);
  assert.deepStrictEqual(kids(view).map((b) => b.attrs["aria-pressed"]), ["true", "false", "false", "false", "false"], "Overview is pressed before the first paint");
  assert.strictEqual(range.attrs.id, "an-prange");
  assert.strictEqual(range.attrs.role, "group");
  assert.strictEqual(range.attrs["aria-label"], "Range");
  assert.deepStrictEqual(kids(range).map((b) => b.attrs["data-range"]), ["7d", "30d", "90d", "all", "custom"], "the keys of RANGES, then custom");
  assert.deepStrictEqual(kids(range).map(text), ["7d", "30d", "90d", "All", "Custom…"]);
  assert.deepStrictEqual(kids(range).map((b) => b.attrs["aria-pressed"]), ["false", "true", "false", "false", "false"], "30d is pressed before the first paint");
  for (const b of [...kids(view), ...kids(range)]) assert.strictEqual(b.attrs.type, "button");
  assert.strictEqual(compare.attrs.id, "an-compare");
  assert.strictEqual(compare.attrs.type, "button");
  assert.strictEqual(compare.attrs["aria-pressed"], "false");
  assert.strictEqual(compare.attrs.title, "Compare this window to the equal-length window before it", "§A1: the frame's title, not today's `immediately`");
  assert.strictEqual(text(compare), "Compare", "no ⇄: the glyph is gone from the frame's label");

  // The Custom range row (not designed, §F3): hidden until Custom… opens it, built from the kit's parts.
  assert.ok("hidden" in custom.attrs, "hidden before the first paint");
  assert.deepStrictEqual(kids(custom).map(sig), ["span.t-sm.muted", "input.input.input-sm.an-date", "input.input.input-sm.an-date", "button.btn"]);
  assert.strictEqual(text(kids(custom)[0]), "Custom range");
  assert.deepStrictEqual(kids(custom).slice(1, 3).map((n) => [n.attrs.type, n.attrs["aria-label"]]), [["date", "From"], ["date", "To"]]);
  assert.ok(custom.children.some((c) => c.text && c.text.includes("→")), "the arrow between the dates");
  assert.strictEqual(kids(custom)[3].attrs.id, "an-papply");
  assert.strictEqual(kids(custom)[3].attrs.type, "button");
  assert.strictEqual(text(kids(custom)[3]), "Apply");
});

// --- Analytics: the script, run against that markup ---------------------------------------------------------------

// analyticsEnv: analytics.js in a context whose document is the real Analytics dialog markup, with a focus model (a node
// taken out of the page loses focus, as in a browser) and an api() that records its calls. `page(true)` is pagehost
// re-parenting the dialog (the as-page class); a phone modal never has it.
function analyticsEnv(file = "analytics.js") {
  focused = null;
  const dlg = fromParsed(analyticsTree());
  const body = new El("body");
  const inPage = (n) => { for (let x = n; x; x = x.parent) if (x === dlg) return true; return false; };
  const document = {
    getElementById: (id) => findId(dlg, id),
    querySelectorAll: (sel) => queryAll(dlg, sel),
    querySelector: (sel) => queryAll(dlg, sel)[0] || null,
    createElement: (tag) => new El(tag),
    // The chart builders make SVG nodes; the fake DOM does not tell them apart.
    createElementNS: (_ns, tag) => new El(tag),
    createTextNode: (t) => new TextNode(t),
    body,
    get activeElement() { return focused && inPage(focused) ? focused : body; },
  };
  const calls = [];
  const gates = [];
  const api = (path, opts) => {
    calls.push(path);
    if (opts && opts.method === "POST") return new Promise((r) => gates.push(r));
    const q = /from=([\d-]+)&to=([\d-]+)/.exec(path);
    return Promise.resolve(q ? { from: q[1], to: q[2] } : {});
  };
  const ctx = { document, window: { innerWidth: 1440 }, api, openDialog() {}, console };
  vm.createContext(ctx);
  // The page's icon scripts come first: the Tools card heads draw a glyph (#168 G3), as the browser has them in the shared scope.
  new vm.Script(ICON_SRC, { filename: "icons" }).runInContext(ctx);
  new vm.Script(read(file), { filename: file }).runInContext(ctx);
  return {
    ctx, dlg, calls, gates, document,
    run: (code) => vm.runInContext(code, ctx),
    el: (id) => findId(dlg, id),
    page: (on) => (on ? dlg.classList.add("as-page") : dlg.classList.remove("as-page")),
  };
}
const settle = () => new Promise((r) => setTimeout(r, 0));
const pressedOf = (group) => group.elements.map((b) => b.attrs["aria-pressed"]);

// The same node objects remain, and aria-pressed follows AN.tab, AN.range and AN.compare (Custom… and the row as the phone's `.on`).
test("page controls are updated in place", () => {
  const { run, el, page } = analyticsEnv();
  page(true);
  const nodes = {
    view: [...el("an-pview").elements], range: [...el("an-prange").elements], compare: el("an-compare"), desc: el("an-pdesc"),
    custom: el("an-papply").parent, dates: el("an-papply").parent.elements.filter((n) => n.tag === "input"), apply: el("an-papply"),
    head: el("an-pdesc").parent.parent, tools: el("an-pview").parent,
  };
  const same = () => {
    assert.ok(el("an-pview").elements.every((b, i) => b === nodes.view[i]), "the view items are the same nodes");
    assert.ok(el("an-prange").elements.every((b, i) => b === nodes.range[i]), "the range items are the same nodes");
    assert.strictEqual(el("an-compare"), nodes.compare);
    assert.strictEqual(el("an-pdesc"), nodes.desc);
    assert.strictEqual(el("an-papply").parent, nodes.custom);
    assert.deepStrictEqual(nodes.dates.map((n) => n.parent === nodes.custom), [true, true]);
  };

  run('AN.tab = "overview"; AN.range = "30d"; AN.compare = false; AN.customOpen = false; AN.from = "2026-10-09"; AN.to = "2026-11-07"; AN.data = null; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["true", "false", "false", "false", "false"]);
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "true", "false", "false", "false"]);
  assert.strictEqual(el("an-compare").attrs["aria-pressed"], "false");
  assert.strictEqual(nodes.custom.hidden, true);
  assert.strictEqual(el("an-pdesc").textContent, "API value of your Claude Code usage at list rates, not what a subscription bills · 2026-10-09 → 2026-11-07 (UTC)", "§A1, from AN before a window has loaded");
  same();

  run('AN.tab = "sessions"; AN.range = "90d"; AN.compare = true; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["false", "false", "false", "true", "false"]);
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "false", "true", "false", "false"]);
  assert.strictEqual(el("an-compare").attrs["aria-pressed"], "true");
  same();

  run('AN.range = "all"; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "false", "false", "true", "false"]);
  run('AN.tab = "models"; AN.range = "7d"; AN.compare = false; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["false", "false", "false", "false", "true"]);
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["true", "false", "false", "false", "false"]);
  assert.strictEqual(el("an-compare").attrs["aria-pressed"], "false");

  // Custom… mirrors today's `.on` exactly: lit by an applied custom range, not by the row being open.
  run('AN.range = "custom"; AN.customOpen = false; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "false", "false", "false", "true"]);
  assert.strictEqual(nodes.custom.hidden, false, "an applied custom range keeps its row");
  run('AN.range = "30d"; AN.customOpen = true; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "true", "false", "false", "false"], "open but not applied: Custom… is not pressed");
  assert.strictEqual(nodes.custom.hidden, false);
  run('AN.customOpen = false; anSyncPageControls();');
  assert.strictEqual(nodes.custom.hidden, true);

  // The description names the window that was asked for (AN.from, AN.to), whatever window the body last loaded: a pressed range
  // must not read as the one before it while it loads, or after its load failed (Gate 3 D9).
  run('AN.data = { from: "2026-10-01", to: "2026-10-30", costByDay: { days: [] } }; anSyncPageControls();');
  assert.strictEqual(el("an-pdesc").textContent, "API value of your Claude Code usage at list rates, not what a subscription bills · 2026-10-09 → 2026-11-07 (UTC)", "AN's window, not the loaded data's");
  run('AN.from = "2026-10-05"; AN.to = "2026-11-01"; anSyncPageControls();');
  assert.strictEqual(el("an-pdesc").textContent, "API value of your Claude Code usage at list rates, not what a subscription bills · 2026-10-05 → 2026-11-01 (UTC)", "follows AN at once");
  same();
  // Nothing is ever built or removed by the sync: the head and the toolbar keep their children.
  assert.strictEqual(nodes.head.elements.length, 2);
  assert.strictEqual(nodes.tools.elements.length, 5);
  assert.ok(!/replaceChildren|appendChild|createElement|innerHTML|\bel\(/.test(fnBody(stripJsComments(read("analytics.js")), "anSyncPageControls")), "the sync only sets attributes, hidden and text");
});

// fnBody: the source of function `name` (its braces and everything between them).
function fnBody(src, name) {
  const m = new RegExp("(?:async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " is defined");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("the head names the window a range press asked for while it loads and after its load failed", async () => {
  const { run, el, page, ctx } = analyticsEnv();
  page(true);
  run("anWirePage();");
  run('AN.from = "2026-10-09"; AN.to = "2026-11-07"; AN.data = { from: AN.from, to: AN.to, costByDay: { days: [] } };');
  const desc = () => el("an-pdesc").textContent;
  // A load that has not answered yet: the 7d window is in the head already, the body says Loading….
  let release;
  ctx.api = () => new Promise((r) => { release = r; });
  el("an-prange").onclick({ target: el("an-prange").elements[0] });
  const seven = `${run("AN.from")} → ${run("AN.to")}`;
  assert.notStrictEqual(seven, "2026-10-09 → 2026-11-07");
  assert.strictEqual(desc(), `API value of your Claude Code usage at list rates, not what a subscription bills · ${seven} (UTC)`, "the window being loaded, not the one before it");
  assert.strictEqual(el("analytics-body").textContent, "Loading…");
  release({});
  await settle();
  // A load that fails: the body says so and the head still names the pressed window, not the last loaded one.
  ctx.api = () => Promise.reject(new Error("down"));
  el("an-prange").onclick({ target: el("an-prange").elements[2] });
  const ninety = `${run("AN.from")} → ${run("AN.to")}`;
  await settle();
  assert.strictEqual(el("analytics-body").textContent, "Could not load analytics.");
  assert.strictEqual(desc(), `API value of your Claude Code usage at list rates, not what a subscription bills · ${ninety} (UTC)`);
  assert.notStrictEqual(ninety, seven);
});

test("a phone modal's page nodes are never touched: anSyncPageControls is a no-op unless the dialog is the page", () => {
  const { run, el, page } = analyticsEnv();
  page(false);
  run('AN.tab = "models"; AN.range = "7d"; AN.compare = true; AN.customOpen = true; AN.from = "2026-10-24"; AN.to = "2026-10-30"; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["true", "false", "false", "false", "false"], "markup as shipped");
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "true", "false", "false", "false"]);
  assert.strictEqual(el("an-compare").attrs["aria-pressed"], "false");
  assert.strictEqual(el("an-papply").parent.hidden, true);
  assert.strictEqual(el("an-pdesc").textContent, "");
});

test("a click on a view, range, Compare or Custom… item leaves focus on that control", async () => {
  const { run, el, page, calls } = analyticsEnv();
  page(true);
  run("anWirePage();");
  run('AN.from = "2026-10-09"; AN.to = "2026-11-07"; AN.data = { from: AN.from, to: AN.to, costByDay: { days: [] } };');
  // Each click: focus the item (what a pointer or Enter does), run its handler, let the load finish and the body render.
  const press = async (group, key, attr) => {
    const b = el(group).elements.find((x) => x.dataset[attr] === key) || el(group);
    b.focus();
    el(group).onclick({ target: b });
    await settle();
    assert.strictEqual(vm_active(), b, group + " " + key + ": focus is still on the control");
    return b;
  };
  const vm_active = () => run("document.activeElement");

  let b = await press("an-pview", "sessions", "antab");
  assert.strictEqual(run("AN.tab"), "sessions");
  assert.strictEqual(b.attrs["aria-pressed"], "true");
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["false", "false", "false", "true", "false"]);
  // The phone's tab strip follows the same state (anSetTab syncs both).
  assert.deepStrictEqual(el("an-tabs").elements.map((t) => t.cls.includes("act")), [false, false, false, true, false]);

  calls.length = 0;
  b = await press("an-prange", "7d", "range");
  assert.strictEqual(run("AN.range"), "7d");
  assert.strictEqual(b.attrs["aria-pressed"], "true");
  assert.strictEqual(el("an-prange").elements.filter((x) => x.attrs["aria-pressed"] === "true").length, 1);
  assert.strictEqual(run("AN.from"), run("anRangeFrom('7d')"));
  assert.ok(calls.some((c) => c.startsWith("/api/analytics/summary?from=" + run("AN.from"))), "a range pick loads that window");

  const cmp = el("an-compare");
  cmp.focus();
  cmp.onclick({ target: cmp });
  assert.strictEqual(cmp.attrs["aria-pressed"], "true", "pressed at once, not after the load");
  await settle();
  assert.strictEqual(run("AN.compare"), true);
  assert.strictEqual(run("document.activeElement"), cmp, "Compare keeps focus");
  assert.strictEqual(el("an-compare"), cmp);

  b = await press("an-prange", "custom", "range");
  assert.strictEqual(run("AN.customOpen"), true);
  assert.strictEqual(el("an-papply").parent.hidden, false, "Custom… opens the row");
  assert.deepStrictEqual(el("an-papply").parent.elements.filter((n) => n.tag === "input").map((n) => n.value), [run("AN.from"), run("AN.to")], "the dates default to the window");
  assert.strictEqual(b.attrs["aria-pressed"], "false", "not applied yet: as today's chip");
  // Custom… again closes the row it opened.
  b.focus();
  el("an-prange").onclick({ target: b });
  assert.strictEqual(el("an-papply").parent.hidden, true);
  assert.strictEqual(run("document.activeElement"), b);
});

test("Apply on the page's Custom range row swaps a reversed range, loads it and lights Custom…", async () => {
  const { run, el, page, calls } = analyticsEnv();
  page(true);
  run("anWirePage();");
  run('AN.from = "2026-10-09"; AN.to = "2026-11-07"; AN.customOpen = true; anSyncPageControls();');
  const [from, to] = el("an-papply").parent.elements.filter((n) => n.tag === "input");
  // An empty field applies nothing.
  from.value = "2026-11-01"; to.value = "";
  calls.length = 0;
  el("an-papply").onclick({ target: el("an-papply") });
  assert.deepStrictEqual(calls, []);
  assert.strictEqual(run("AN.range"), "30d");
  // A reversed range is ordered (clampRange), written back into the fields and loaded.
  from.value = "2026-11-01"; to.value = "2026-10-05";
  el("an-papply").focus();
  el("an-papply").onclick({ target: el("an-papply") });
  assert.deepStrictEqual([run("AN.range"), run("AN.from"), run("AN.to")], ["custom", "2026-10-05", "2026-11-01"]);
  assert.deepStrictEqual([from.value, to.value], ["2026-10-05", "2026-11-01"]);
  assert.ok(calls.includes("/api/analytics/summary?from=2026-10-05&to=2026-11-01&where="));
  await settle();
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "false", "false", "false", "true"]);
  assert.strictEqual(el("an-papply").parent.hidden, false, "a custom range keeps its row open");
  assert.strictEqual(run("document.activeElement"), el("an-papply"), "Apply keeps focus");
  assert.strictEqual(el("an-pdesc").textContent, "API value of your Claude Code usage at list rates, not what a subscription bills · 2026-10-05 → 2026-11-01 (UTC)");
  // A preset closes the row again; reopened, the fields show the window then.
  const seven = el("an-prange").elements[0];
  el("an-prange").onclick({ target: seven });
  assert.strictEqual(el("an-papply").parent.hidden, true);
  await settle();
  el("an-prange").onclick({ target: el("an-prange").elements[4] });
  assert.deepStrictEqual([from.value, to.value], [run("AN.from"), run("AN.to")]);
});

test("anLoad fetches summary, models, workspaces, session-costs, drivers and where; every one but where carries the where filter", async () => {
  const { run, calls } = analyticsEnv();
  run('AN.from = "2026-10-05"; AN.to = "2026-11-01";');
  assert.strictEqual(run("AN.where"), "", "All is the default");
  assert.strictEqual(run("JSON.stringify(AN.whereOpts)"), "[]");
  await run("anLoad()");
  assert.deepStrictEqual([...calls].sort(), [
    "/api/analytics/drivers?from=2026-10-05&to=2026-11-01&where=",
    "/api/analytics/models?from=2026-10-05&to=2026-11-01&where=",
    "/api/analytics/session-costs?from=2026-10-05&to=2026-11-01&where=",
    "/api/analytics/summary?from=2026-10-05&to=2026-11-01&where=",
    "/api/analytics/where?from=2026-10-05&to=2026-11-01",
    "/api/analytics/workspaces?from=2026-10-05&to=2026-11-01&where=",
  ], "one code path: an empty where is still sent; no projects or tools");
  // A key is encoded, hostile or not.
  calls.length = 0;
  run("AN.where = \"box:my profile/;drop\";");
  await run("anLoad()");
  const enc = "&where=" + encodeURIComponent("box:my profile/;drop");
  assert.strictEqual(calls.filter((c) => c.endsWith(enc)).length, 5);
  assert.ok(calls.includes("/api/analytics/where?from=2026-10-05&to=2026-11-01"), "the options list is the whole window, whatever is chosen");
  // The prepared figures are on AN.data: an empty answer is an empty window, not an exception.
  assert.strictEqual(run("JSON.stringify([AN.data.workspaces, AN.data.drivers.empty, AN.data.median, AN.data.cache])"), "[[],true,null,null]");
});

test("anLoadCompare fetches the window before: summary, models and session-costs only, with the where filter", async () => {
  const { run, calls } = analyticsEnv();
  run('AN.from = "2026-10-05"; AN.to = "2026-11-01"; AN.where = "host"; AN.compare = true;');
  await run("anLoad()");
  assert.deepStrictEqual(calls.filter((c) => /from=2026-09-07&to=2026-10-04/.test(c)).sort(), [
    "/api/analytics/models?from=2026-09-07&to=2026-10-04&where=host",
    "/api/analytics/session-costs?from=2026-09-07&to=2026-10-04&where=host",
    "/api/analytics/summary?from=2026-09-07&to=2026-10-04&where=host",
  ]);
  assert.strictEqual(run("AN.data.cmp.days"), 28);
  assert.deepStrictEqual({ ...run("AN.data.cmp.prev") }, { cost: 0, sessions: 0, median: null, cache: null });
});

test("openAnalytics resets the page's controls with the state it resets", async () => {
  const { run, el, page } = analyticsEnv();
  page(true);
  run('AN.tab = "models"; AN.range = "custom"; AN.compare = true; AN.customOpen = true; anSyncPageControls();');
  await run("openAnalytics()");
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["true", "false", "false", "false", "false"]);
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "true", "false", "false", "false"]);
  assert.strictEqual(el("an-compare").attrs["aria-pressed"], "false");
  assert.strictEqual(el("an-papply").parent.hidden, true);
  assert.match(el("an-pdesc").textContent, /^API value of your Claude Code usage at list rates, not what a subscription bills · \d{4}-\d\d-\d\d → \d{4}-\d\d-\d\d \(UTC\)$/);
});

test("the page's body holds no controls and no window line: the head carries them", async () => {
  const { run, el, page } = analyticsEnv();
  page(true);
  run('AN.from = "2026-10-09"; AN.to = "2026-11-07"; AN.customOpen = true; AN.range = "custom"; AN.data = { from: AN.from, to: AN.to, costByDay: { days: [] } }; anRender();');
  const body = el("analytics-body");
  assert.deepStrictEqual(body.elements.map((n) => n.className), ["an-empty"], "an empty window is the one line, with no control strip, custom row or window hint");
  assert.strictEqual(body.textContent, "No usage recorded in this window. Try a wider range or Re-scan.");
  assert.strictEqual(el("an-ctl").children.length, 0, "#an-ctl stays empty on the page");
  const src = stripJsComments(read("analytics.js"));
  assert.ok(!/anCustomRow|an-customrow/.test(src), "the JS-built Custom row is gone: the static .an-pcustom replaces it");
  assert.ok(!/hint\.textContent \+= /.test(src), "the window is not appended to the first hint any more");
  assert.ok(!/anControls\(true\)/.test(src));
});

// --- Analytics: the phone's strip and buttons are unchanged ---------------------------------------------------------

// serialize: [tag, class, type, title, style width, value, …children] (a text node is its text).
const serialize = (n) => (n instanceof TextNode ? n.text : [n.tag, n.className, n.attrs.type || n.type || "", n.title || "", n.style.width || "", n.value || "", ...n.children.map(serialize)]);
const CMP_TITLE = "Compare this window to the equal-length window immediately before it";
const chip = (cls, text) => ["button", cls, "button", "", "", "", text];
// The control strip anControls(false) drew on main c47442b for three states of AN, taken from that file (pinned here).
const PHONE_STRIP = {
  default: ["div", "an-controls", "", "", "", "",
    ["span", "an-clabel", "", "", "", "", "Range"], chip("an-chip", "7d"), chip("an-chip on", "30d"), chip("an-chip", "90d"), chip("an-chip", "All"), chip("an-chip", "Custom…"),
    ["span", "", "", "", "12px", ""],
    ["button", "an-chip", "button", CMP_TITLE, "", "", "⇄ Compare vs prev period"]],
  custom: ["div", "an-controls", "", "", "", "",
    ["span", "an-clabel", "", "", "", "", "Range"], chip("an-chip", "7d"), chip("an-chip", "30d"), chip("an-chip", "90d"), chip("an-chip", "All"), chip("an-chip on", "Custom…"),
    ["input", "an-date", "date", "", "", "2026-10-01"], ["span", "an-clabel", "", "", "", "", "→"], ["input", "an-date", "date", "", "", "2026-10-30"], chip("an-chip", "Apply"),
    ["span", "", "", "", "12px", ""],
    ["button", "an-chip on", "button", CMP_TITLE, "", "", "⇄ Compare vs prev period"]],
  seven: ["div", "an-controls", "", "", "", "",
    ["span", "an-clabel", "", "", "", "", "Range"], chip("an-chip on", "7d"), chip("an-chip", "30d"), chip("an-chip", "90d"), chip("an-chip", "All"), chip("an-chip", "Custom…"),
    ["span", "", "", "", "12px", ""],
    ["button", "an-chip on", "button", CMP_TITLE, "", "", "⇄ Compare vs prev period"]],
};

test("the phone's control strip, anControls(false), is the strip main drew: classes, copy, order", () => {
  const { run } = analyticsEnv();
  const strip = (code) => serialize(run(code));
  assert.deepStrictEqual(strip('AN.from = "2026-10-01"; AN.to = "2026-10-30"; AN.range = "30d"; AN.compare = false; AN.customOpen = false; anControls(false)'), PHONE_STRIP.default);
  assert.deepStrictEqual(strip('AN.range = "custom"; AN.compare = true; AN.customOpen = true; anControls(false)'), PHONE_STRIP.custom);
  assert.deepStrictEqual(strip('AN.from = "2026-10-24"; AN.to = "2026-10-30"; AN.range = "7d"; AN.customOpen = false; anControls(false)'), PHONE_STRIP.seven);
});

test("the phone's chips, tabs and Re-scan still work on the modal", async () => {
  const { run, el, page, calls, gates } = analyticsEnv();
  page(false);
  run('AN.data = { from: "2026-10-09", to: "2026-11-07", costByDay: { days: [] } }; AN.from = "2026-10-09"; AN.to = "2026-11-07";');
  // The strip is the first child of the body on the modal, and each chip sets AN as it did.
  run("anRender();");
  const body = el("analytics-body");
  assert.deepStrictEqual(body.elements.map((n) => n.className), ["an-controls", "an-empty"]);
  const chips = () => body.elements[0].elements.filter((n) => n.cls.includes("an-chip"));
  chips()[0].onclick();
  assert.deepStrictEqual([run("AN.range"), run("AN.customOpen")], ["7d", false]);
  await settle();
  assert.ok(calls.some((c) => c.startsWith("/api/analytics/summary?from=")), "a chip loads its window");
  chips()[4].onclick();
  assert.strictEqual(run("AN.customOpen"), true, "Custom… opens the inline inputs on the phone");
  assert.strictEqual(body.elements[0].elements.filter((n) => n.tag === "input").length, 2);
  // Apply on the inline inputs: swapped, custom, loaded.
  const [f, t] = body.elements[0].elements.filter((n) => n.tag === "input");
  f.value = "2026-11-01"; t.value = "2026-10-05";
  body.elements[0].elements.find((n) => n.textContent === "Apply").onclick();
  assert.deepStrictEqual([run("AN.range"), run("AN.from"), run("AN.to")], ["custom", "2026-10-05", "2026-11-01"]);
  await settle();
  // The tab strip: .act follows AN.tab, the page's seg is left alone.
  run('anSetTab("models");');
  assert.strictEqual(run("AN.tab"), "models");
  assert.deepStrictEqual(el("an-tabs").elements.map((n) => n.cls.includes("act")), [false, true, false, false, false]);
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["true", "false", "false", "false", "false"], "the hidden page seg keeps its markup on the phone");
  // Re-scan: disabled and "Scanning…" while it runs, the same words after.
  const btn = el("analytics-refresh");
  calls.length = 0;
  const done = run("anRescan(document.getElementById('analytics-refresh'))");
  assert.strictEqual(btn.disabled, true);
  assert.strictEqual(btn.textContent, "Scanning…");
  assert.deepStrictEqual(calls, ["/api/analytics/ingest"]);
  gates[0]({});
  await done;
  assert.strictEqual(btn.disabled, false);
  assert.strictEqual(btn.textContent, "Re-scan");
  assert.ok(calls.some((c) => c.startsWith("/api/analytics/summary?from=2026-10-05&to=2026-11-01")), "and reloads the window");
  // A phone never steals focus back.
  assert.strictEqual(run("document.activeElement"), run("document.body"));
});

test("Re-scan on the page: the same body, and focus returns to it when the browser dropped it", async () => {
  const { run, el, page, gates, document } = analyticsEnv();
  page(true);
  run('AN.from = "2026-10-09"; AN.to = "2026-11-07";');
  const btn = el("an-rescan");
  btn.focus();
  const done = run("anRescan(document.getElementById('an-rescan'))");
  assert.strictEqual(btn.disabled, true);
  assert.strictEqual(btn.textContent, "Scanning…");
  focused = null; // a disabled button loses focus in browsers that apply the focus fixup rule
  gates[0]({});
  await done;
  assert.strictEqual(btn.disabled, false);
  assert.strictEqual(btn.textContent, "Re-scan");
  assert.strictEqual(document.activeElement, btn, "back on Re-scan");
  // Focus the user moved elsewhere in the meantime is not taken back.
  const other = el("an-compare");
  btn.focus();
  const again = run("anRescan(document.getElementById('an-rescan'))");
  other.focus();
  gates[1]({});
  await again;
  assert.strictEqual(document.activeElement, other);
  // A failed scan still re-enables it.
  const failing = run("anRescan(document.getElementById('an-rescan'))");
  gates[2](Promise.reject(new Error("409")));
  await failing;
  assert.strictEqual(btn.disabled, false);
  assert.strictEqual(btn.textContent, "Re-scan");
});

test("the page controls are wired once, the phone's tab strip and Re-scan through the same functions", () => {
  const app = stripJsComments(read("app.js"));
  assert.match(app, /anRescan\(anRefresh\)/, "the phone's Re-scan");
  assert.match(app, /anRescan\(\$\("an-rescan"\)\)|anRescan\(anRescanBtn\)/, "the page's Re-scan");
  assert.match(app, /anSetTab\(b\.dataset\.antab\)/, "the phone's tab strip goes through anSetTab");
  assert.match(app, /anWirePage\(\)/, "the page's seg, Compare and Apply are wired by analytics.js");
  assert.ok(!/AN\.tab = b\.dataset\.antab/.test(app), "no second copy of the tab logic");
  assert.ok(!/api\("\/api\/analytics\/ingest"/.test(app), "the scan lives in anRescan");
  // Nothing queries [data-antab] broadly: the page's seg items carry it too and must not be wired as phone tabs.
  for (const f of fs.readdirSync(STATIC).filter((x) => x.endsWith(".js"))) {
    assert.ok(!/querySelectorAll\(["'`]\[data-antab/.test(read(f)), f + " queries every [data-antab]");
    assert.ok(!/querySelector(?:All)?\(["'`]button\[data-antab/.test(read(f)) || f === "app.js", f);
  }
  const wired = (read("analytics.js").match(/function anWirePage[\s\S]*?\n}\n/) || [""])[0];
  assert.match(wired, /an-pview/);
  assert.match(wired, /an-prange/);
  assert.match(wired, /an-compare/);
  assert.match(wired, /an-papply/);
});

// --- Analytics: the CSS ------------------------------------------------------------------------------------------

test("the desktop-only Analytics nodes are hidden at base", () => {
  // app.css, outside any @media: the phone's modal holds the nodes and never shows them. The selector carries the dialog's
  // id: desktop.css's kit .page-head and .toolbar (display: flex, one class, later in the cascade) would out-rank a bare
  // class in a modal widened past 700px, which pagehost.js leaves a modal.
  const app = parseRules(APP_CSS);
  const hide = app.filter((r) => parts(r.selector).includes("#analytics-modal .an-phead"));
  assert.strictEqual(hide.length, 1);
  assert.deepStrictEqual(parts(hide[0].selector), ["#analytics-modal .an-phead", "#analytics-modal .an-ptools", "#analytics-modal .an-pcustom"]);
  assert.strictEqual(hide[0].decls.get("display"), "none");
  assert.deepStrictEqual(hide[0].at, [], "a base rule: not inside @media");
  assert.ok(!app.some((r) => r !== hide[0] && parts(r.selector).some((s) => /an-phead|an-ptools|an-pcustom|an-pview|an-prange|an-compare|an-rescan|an-pdesc/.test(s))), "app.css says nothing else about them");

  // desktop.css shows them only under the page, and hides the phone's title and head there.
  const mine = RULES.filter((r) => parts(r.selector).some((s) => /an-phead|an-ptools|an-pcustom|an-pview|an-prange|an-compare|an-rescan|an-pdesc|an-papply/.test(s)));
  assert.ok(mine.length >= 3);
  for (const r of mine) for (const s of parts(r.selector)) if (/an-phead|an-ptools|an-pcustom|an-pview|an-prange|an-compare|an-rescan|an-pdesc|an-papply/.test(s)) assert.ok(s.startsWith(ANP), s + " is not prefixed with the page");
  for (const c of [".an-phead", ".an-ptools", ".an-pcustom"]) assert.strictEqual(decls(ANP + " > " + c).get("display"), "flex", c + " is shown on the page");
  assert.strictEqual(decls(ANP + " > .dlg-title").get("display"), "none", "the phone's legend title is not drawn on the page");
  assert.strictEqual(decls(ANP + " > .dlg-head").get("display"), "none", "nor its head: tabs, caption, #an-ctl, Re-scan and ×");
  // Page-weight rules for the phone's nodes: the closing × and the strip are gone from the page's view, not restyled.
  assert.deepStrictEqual(RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(ANP) && /#an-ctl|#an-tabs|#analytics-refresh|\.dlg-x|#analytics-range/.test(s))).map((r) => r.selector), []);
  // The kit gives .toolbar and .btn a display, which beats the UA's [hidden]: the custom row hides through its own rule.
  const shown = BASE_RULES.findIndex((r) => parts(r.selector).includes(ANP + " > .an-pcustom"));
  const hidden = BASE_RULES.findIndex((r) => parts(r.selector).includes(ANP + " > .an-pcustom[hidden]"));
  assert.ok(shown >= 0 && hidden > shown, "the [hidden] rule follows the show rule");
  assert.strictEqual(decls(ANP + " > .an-pcustom[hidden]").get("display"), "none");
  // The page is the base page rule's column (head, toolbar, custom row, then the body scrolling): not Analytics' old grid.
  assert.strictEqual(decls(ANP).get("display"), undefined);
  assert.strictEqual(decls(ANP).get("grid-template-columns"), undefined);
  assert.strictEqual(decls("#page-host > dialog.as-page").get("flex-direction"), "column");
});

test("Analytics and Audit set their head and toolbar in one shared rule each", () => {
  const head = BASE_RULES.filter((r) => parts(r.selector).includes(AU + " > .page-head"));
  assert.strictEqual(head.length, 1, "one rule for the Audit head");
  assert.ok(parts(head[0].selector).includes(ANP + " > .page-head"), "the same rule sets the Analytics head");
  assert.strictEqual(head[0].decls.get("padding"), "var(--sp-6) var(--sp-8) var(--sp-3)", "§B1: 24px 32px 12px, not the kit's 20px");
  assert.strictEqual(head[0].decls.get("flex"), "none", "the head stays put");
  const tools = BASE_RULES.filter((r) => parts(r.selector).includes(AU + " > .toolbar.au-tools") && r.decls.has("padding"));
  assert.strictEqual(tools.length, 1);
  assert.deepStrictEqual(parts(tools[0].selector), [AU + " > .toolbar.au-tools", ANP + " > .toolbar.an-ptools", ANP + " > .toolbar.an-pcustom"], "the same rule sets Audit's toolbar and both Analytics rows");
  assert.strictEqual(tools[0].decls.get("padding"), "0 var(--sp-8) var(--sp-4)", "§B2: 0 32px 16px");
  assert.strictEqual(tools[0].decls.get("flex-wrap"), "wrap", "760px never scrolls sideways");
  // The wrap does not undo the toolbar's own layout.
  const kit = decls(".toolbar");
  assert.strictEqual(kit.get("display"), "flex");
  assert.strictEqual(kit.get("align-items"), "center");
  assert.strictEqual(kit.get("gap"), "var(--sp-3)");
  // Everything under the toolbars scrolls and the dialog's own body is that scroller (base .dlg-body: flex 1, min-height 0).
  const body = decls(ANP + " > .dlg-body");
  assert.strictEqual(body.get("padding"), "0 var(--sp-8) var(--sp-6)", "§B0: .m3-body, so the body lines up with the toolbar");
  assert.strictEqual(body.get("grid-column"), undefined);
  assert.strictEqual(body.get("grid-row"), undefined);
  const base = decls(".dlg-body", parseRules(APP_CSS));
  assert.strictEqual(base.get("flex"), "1");
  assert.strictEqual(base.get("min-height"), "0");
  assert.strictEqual(base.get("overflow-y"), "auto");
});

test("the Custom range row is kit .toolbar parts: 28px date fields that keep their width and the kit focus", () => {
  const field = decls(ANP + " > .an-pcustom .input");
  assert.strictEqual(field.get("width"), "auto", "the kit's .input is width: 100%");
  assert.strictEqual(field.get("flex"), "none");
  assert.strictEqual(decls(".input.input-sm").get("height"), "var(--h-ctl)", "28px beside 28px buttons");
  // No page rule of its own for the dates' border or focus: the global field focus covers the kit .input.
  const own = RULES.filter((r) => parts(r.selector).some((s) => /\.an-date/.test(s)));
  assert.deepStrictEqual(own.map((r) => r.selector), [".an-date:focus"], "no page rule names .an-date: only the one that answers app.css's focus opt-out");
  assert.deepStrictEqual([...own[0].decls], [["box-shadow", "0 0 0 1px var(--accent)"]]);
  // The Analytics dialog's JS-built phone inputs keep app.css's .an-date, and the page's static ones wear it as a hook too.
  assert.ok(parseRules(APP_CSS).some((r) => parts(r.selector).includes(".an-date")));
});

test("no Analytics head rule uses color-mix, nesting or @layer, and none sets a type size of its own", () => {
  const rules = RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(ANP + " > .page-head") || s.startsWith(ANP + " > .toolbar") || s.startsWith(ANP + " > .an-p")));
  assert.ok(rules.length >= 4);
  for (const r of rules) for (const [prop, v] of r.decls) {
    assert.ok(!/color-mix/.test(v), r.selector + " { " + prop + " } uses color-mix");
    assert.ok(!/^font/.test(prop), r.selector + " sets " + prop + ": the kit's head and toolbar parts draw the type");
  }
  assert.ok(!/@layer/.test(DESK_CSS));
});

// =====================================================================================================================
// Analytics: the KPI row, the kit tables and the Sessions view (Task 5; frame M3.2, digest-design §A2, §A3, §B5, §B8, §C8,
// §C9, §F4, §G5, §G9, §G11)
// =====================================================================================================================

// stripAnalytics: analytics.js without its comments, line comments first: the file's own first line mentions "/api/analytics/*",
// which stripJsComments would take for the start of a block comment and swallow the next four hundred lines.
const stripAnalytics = (s) => s.replace(/\/\/.*$/gm, "").replace(/\/\*[\s\S]*?\*\//g, "");

// A made-up window, in the shape /api/analytics/* returns it: two days of one model, two unpriced models, three projects,
// four sessions (one in a long-named project, one with a single model) and tools of two kinds.
const ROW = (o) => ({ input: 0, output: 0, cacheRead: 0, cacheCreation: 0, prompts: 0, costUsd: 0, ...o });
const FIXTURE = {
  summary: [
    ROW({ day: "2026-10-30", model: "claude-opus-5-5", input: 4000000000, output: 100000000, cacheRead: 6000000000, prompts: 2000, costUsd: 3000 }),
    ROW({ day: "2026-10-31", model: "claude-opus-5-5", input: 100000000, output: 100000000, cacheRead: 100000000, prompts: 1994, costUsd: 1300 }),
  ],
  models: [
    ROW({ model: "claude-opus-5-5", input: 4100000000, output: 200000000, cacheRead: 6100000000, prompts: 3994, sessions: 4, costUsd: 4300 }),
    ROW({ model: "<synthetic>", prompts: 41, sessions: 3 }),
    ROW({ model: "", prompts: 27, sessions: 1 }),
  ],
  projects: [
    ROW({ project: "-home-dev-acme-api", model: "claude-opus-5-5", input: 10, prompts: 11, costUsd: 120.5 }),
    ROW({ project: "-home-dev-acme-terraform-course-eu-west", model: "claude-opus-5-5", input: 10, prompts: 5, costUsd: 160.14 }),
    ROW({ project: "-home-dev-acme-docs", model: "claude-opus-5-5", input: 10, prompts: 2, costUsd: 4.2 }),
  ],
  sessions: [
    ROW({ sessionId: "3d6b98e8aaaa", project: "-home-dev-acme-api", day: "2026-10-30", model: "claude-opus-5-5", input: 634300000, prompts: 10, costUsd: 120, workspace: "acme-api", runtime: "box", profile: "prd", branch: "feat/370-rework", liveId: "live-1" }),
    ROW({ sessionId: "3d6b98e8aaaa", project: "-home-dev-acme-api", day: "2026-10-31", model: "claude-sonnet-5-5", input: 100, prompts: 1, costUsd: 0.5 }),
    ROW({ sessionId: "2cf1c405bbbb", project: "-home-dev-acme-terraform-course-eu-west", day: "2026-10-30", model: "claude-opus-5-5", input: 410700000, prompts: 116, costUsd: 90, workspace: "acme-terraform-course-eu-west", runtime: "host", branch: "main" }),
    ROW({ sessionId: "9d3dc2afcccc", project: "-home-dev-acme-terraform-course-eu-west", day: "2026-10-26", model: "claude-opus-5-5", input: 124300000, prompts: 92, costUsd: 70.14, liveId: "live-gone" }),
    ROW({ sessionId: "0174c7b1dddd", project: "-home-dev-acme-docs", day: "2026-10-15", model: "claude-opus-5-5", input: 5000, prompts: 2, costUsd: 4.2 }),
  ],
  // /workspaces: acme (a host and a box half) and an unmatched folder; /session-costs' per-(session, model) costs.
  workspaces: { workspaces: [
    { workspace: "/home/u2/acme", name: "acme", unmatched: false, costUsd: 100, sessions: 3, hostCost: 31, boxCost: 69, byModel: { "claude-opus-5-5": 100 }, cacheRead: 90, cacheDenom: 100, sessionCosts: [10, 20, 70], children: [] },
    { workspace: "-home-x-old", name: "old", unmatched: true, costUsd: 0, sessions: 1, hostCost: 0, boxCost: 0, byModel: {}, cacheRead: 0, cacheDenom: 0, sessionCosts: [], children: [] },
  ] },
  modelCosts: [
    { sessionId: "3d6b98e8aaaa", project: "-home-dev-acme-api", model: "claude-opus-5-5", costUsd: 120 },
    { sessionId: "3d6b98e8aaaa", project: "-home-dev-acme-api", model: "claude-sonnet-5-5", costUsd: 0.5 },
    { sessionId: "2cf1c405bbbb", project: "-home-dev-acme-terraform-course-eu-west", model: "claude-opus-5-5", costUsd: 90 },
    { sessionId: "9d3dc2afcccc", project: "-home-dev-acme-terraform-course-eu-west", model: "claude-opus-5-5", costUsd: 70.14 },
    { sessionId: "0174c7b1dddd", project: "-home-dev-acme-docs", model: "claude-opus-5-5", costUsd: 4.2 },
  ],
  tools: [
    { kind: "skill", name: "review", records: 40 }, { kind: "skill", name: "deploy", records: 7 },
    { kind: "mcp_server", name: "docs", records: 3 },
  ],
};

// load: AN.data as anLoad folds it from FIXTURE (the page's views read it), with the session count overridable.
function loadWindow(env, over = {}) {
  env.ctx.FX = { ...FIXTURE, ...over };
  env.run(`(function () {
    AN.from = "2026-10-09"; AN.to = "2026-11-07";
    const sess = FX.sessions;
    // byProject as the old per-project fold made it (anLoad no longer fetches projects): per project, cost and the model mix.
    const byProj = new Map();
    for (const r of FX.projects) {
      const p = byProj.get(r.project) || { project: r.project, cost: 0, tokens: 0, prompts: 0, byModel: {} };
      p.cost += r.costUsd; p.tokens += r.input + r.output + r.cacheRead + r.cacheCreation; p.prompts += r.prompts;
      p.byModel[r.model] = (p.byModel[r.model] || 0) + r.costUsd;
      byProj.set(r.project, p);
    }
    AN.data = {
      from: AN.from, to: AN.to, costByDay: foldCostByDay(FX.summary), byModel: normModels(FX.models),
      byProject: [...byProj.values()].sort((a, b) => b.cost - a.cost).slice(0, 14),
      sessions: sess, topSessions: topSessionsByCost(sess, 18), sessionCount: distinctSessions(sess),
      workspaces: anWorkspaceRows(FX.workspaces), modelMedians: anModelMedians(FX.modelCosts), median: anSessionMedian(sess),
      drivers: anDriverCards(null), cmp: null,
    };
    AN.data.cache = anCacheOfModels(AN.data.byModel);
    AN.data.models = AN.data.costByDay.models;
    AN.data.pricedModels = pricedModelIds(AN.data.byModel);
  })();`);
}
// view: a view function run into a fresh root, whose children are what anRender would put in the body.
function view(env, fn) {
  const root = new El("div");
  env.ctx.ROOT = root;
  env.run(fn + "(ROOT);");
  return root;
}
const tilesOf = (row) => row.elements.map((t) => ({ value: t.elements[0].textContent, label: t.elements[1].textContent }));
const THS = ["Session", "Day", "Project", "Model", "Tokens", "Prompts", "Cost"];
// the table of a card (a tablewrap holds it), as { head: [th…], rows: [[td…]…] }
function tableOf(card) {
  const wrap = card.elements.find((n) => n.cls.includes("an-tablewrap"));
  assert.ok(wrap, "the card holds the table in its .an-tablewrap");
  const tbl = wrap.elements[0];
  assert.strictEqual(tbl.tag, "table");
  const [head, ...rows] = tbl.elements;
  return { tbl, head: head.elements, rows: rows.map((r) => r.elements) };
}

test("overview KPI labels on the page are the four tiles, with no change line while Compare is off", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  env.run("AN.data.sessionCount = 1420;");
  const root = view(env, "anOverview");
  const row = root.elements[0];
  assert.strictEqual(row.className, "kpi-row", "the page's KPI row is the kit's, first in the body");
  assert.deepStrictEqual(tilesOf(row), [
    { value: "$4.3k", label: "API value" },
    { value: "1,420", label: "Sessions" },
    { value: "$80.07", label: "Median per session" },
    { value: "59.8%", label: "Cache hit" },
  ], "§3.1: the Tokens and Prompts tiles are gone; the counts take thousands separators");
  // #340 S6: list-price token value is not a bill; the label's tooltip says so, range-neutral (the tile covers
  // the chosen range, not today).
  assert.strictEqual(row.elements[0].elements[1].attrs.title,
    "Token usage priced at API list rates. A subscription does not bill this.");
  assert.strictEqual(row.elements[0].elements[1].attrs.title, require("../static/analytics.js").AN_API_VALUE_TITLE);
  assert.ok(!root.elements.some((n) => n.cls.includes("an-kpis")), "no legacy KPI grid beside it");
  assert.ok(row.elements.every((t) => t.elements.length === 2), "Compare off: no change line on any tile");
  assert.ok(root.elements.length > 1);
});

test("overview with Compare on: each tile shows its change, the first one the window it is against; Compare adds nothing else", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  env.run("AN.compare = true; AN.data.cmp = { days: 30, prev: { cost: 3000, sessions: 3, median: 100, cache: 0.602 } };");
  const row = view(env, "anOverview").elements[0];
  const line = (t) => text(t.elements[2]);
  assert.deepStrictEqual(row.elements.map(line), ["▲ 43% vs the 30 days before", "▲ 33%", "▼ 20%", "▼ 0.4 pt"]);
  // The window off again: the line goes with it, even with the previous window still in memory.
  env.run("AN.compare = false;");
  assert.ok(view(env, "anOverview").elements[0].elements.every((t) => t.elements.length === 2));
  // The Overview has the chart, the two cards and the footnote after Compare as before: no block of its own.
  env.run("AN.compare = true;");
  const root = view(env, "anOverview");
  assert.deepStrictEqual(root.elements.map((n) => n.tag + "." + n.className), ["div.kpi-row", "div.an-psec", "div.an-pgrid", "p.an-foot t-sm muted"]);
});

test("overview: cost by model and cost by workspace, each row with its share; the workspace bar is split host / box", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  const root = view(env, "anOverview");
  const [mc, wc] = root.elements[2].elements;
  assert.deepStrictEqual(mc.elements[0].elements.map((n) => n.textContent), ["Cost by model"]);
  assert.deepStrictEqual(wc.elements[0].elements.map((n) => n.textContent), ["Cost by workspace", "top 8 · bar split = host / box"]);
  const rows = (card) => nodesOf(card, "div").filter((n) => n.cls.includes("an-hrow"));
  assert.ok(rows(mc).every((r) => r.cls.includes("an-pct") && r.elements.length === 4));
  assert.deepStrictEqual(rows(mc)[0].elements.map((n) => n.textContent), ["opus-5-5", "", "$4.3k", "100%"]);
  const w = rows(wc);
  assert.deepStrictEqual(w.map((r) => r.elements[0].textContent), ["acme", "old"]);
  assert.deepStrictEqual(w[0].elements.slice(2).map((n) => n.textContent), ["$100", "2%"]);
  // acme's bar: host 31 of its 100 in --an-6, then box 69 in --an-1, each a share of the longest bar (acme's).
  assert.deepStrictEqual(w[0].elements[1].elements.map((f) => [f.style.left, f.style.width, f.style.background]), [["0%", "31%", "var(--an-6)"], ["31%", "69%", "var(--an-1)"]]);
  assert.ok(w.every((r) => r.cls.includes("an-wide")), "the workspace card has the wide label column");
  const key = nodesOf(wc, "div").find((n) => n.cls.includes("an-wkey"));
  assert.deepStrictEqual(key.elements.map((i) => [i.elements[0].style.background, i.textContent]), [["var(--an-6)", "host"], ["var(--an-1)", "box"]]);
  assert.strictEqual(root.elements[3].textContent, "Descriptive only: a difference between models reflects the tasks you gave each one, not model quality.");
});

test("overview: only the top 8 workspaces are drawn, dearest first", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  env.run(`AN.data.workspaces = Array.from({ length: 11 }, (_, i) => ({ name: "w" + i, cost: 10 + i, hostCost: 10 + i, boxCost: 0 }));`);
  const wc = view(env, "anOverview").elements[2].elements[1];
  const names = nodesOf(wc, "div").filter((n) => n.cls.includes("an-hrow")).map((r) => r.elements[0].textContent);
  assert.deepStrictEqual(names, ["w10", "w9", "w8", "w7", "w6", "w5", "w4", "w3"]);
});

test("overview KPI labels on the phone are unchanged", () => {
  const env = analyticsEnv();
  env.page(false);
  loadWindow(env);
  env.run("AN.data.sessionCount = 1420;");
  const root = view(env, "anOverview");
  const grid = root.elements[0];
  assert.strictEqual(grid.className, "an-kpis");
  assert.deepStrictEqual(grid.elements.map((t) => [t.className, t.elements.map((c) => [c.className, c.textContent])]), [
    ["an-kpi", [["v cost", "$4.3k"], ["l", "total cost"]]],
    ["an-kpi", [["v", "10.4B"], ["l", "tokens"]]],
    ["an-kpi", [["v", "1420"], ["l", "sessions"]]],
    ["an-kpi", [["v", "3994"], ["l", "prompts"]]],
    ["an-kpi", [["v cost", "$3.03"], ["l", "avg / session"]]],
  ]);
});

test("F01 pin: a numeric column's header and cells are .num together, on the phone and on the page", () => {
  for (const page of [false, true]) {
    const env = analyticsEnv();
    env.page(page);
    const flags = (cols, firstNum) => [...env.run(`anHead(${JSON.stringify(cols)}, ${firstNum}).children.map((th) => th.className === "num")`)];
    assert.deepStrictEqual(flags(["session", "day", "project", "model", "tokens", "prompts", "cost"], 4), [false, false, false, false, true, true, true], "th.num on exactly the last three");
    assert.deepStrictEqual(flags(["model", "cost", "% cost", "tokens", "prompts", "out/prompt", "cache-read"], 1), [false, true, true, true, true, true, true]);
    assert.deepStrictEqual(flags(["project", "cost", "tokens", "prompts"], 1), [false, true, true, true]);
    // Every table the views draw: a column's th carries num exactly when its cells do.
    loadWindow(env);
    const numCls = (n) => n.cls.includes("num");
    for (const fn of ["anModels", "anProjects", "anSessions"]) {
      const tables = [];
      const walk = (n) => { if (n.tag === "table") tables.push(n); n.elements.forEach(walk); };
      walk(view(env, fn));
      assert.ok(tables.length >= 1, fn + " draws a table");
      for (const t of tables) {
        const [head, ...rows] = t.elements;
        assert.ok(rows.length >= 1);
        const want = head.elements.map(numCls);
        assert.ok(want.includes(true) && want.includes(false));
        for (const r of rows) assert.deepStrictEqual(r.elements.map(numCls), want, fn + (page ? " (page)" : " (phone)") + ": " + r.textContent);
      }
    }
  }
  // CSS: .num is right-aligned and tabular on th and td alike, the text columns left.
  const num = decls(".an-table .num");
  assert.strictEqual(num.get("text-align"), "right");
  assert.strictEqual(num.get("font-variant-numeric"), "tabular-nums");
  assert.strictEqual(decls(".an-table th, .an-table td").get("text-align") || decls(".an-table th").get("text-align"), "left");
});

test("page tables use the kit head", () => {
  // JS: the header labels are capitalised on the page, and only there.
  const page = analyticsEnv();
  page.page(true);
  const ths = (env, cols, n) => [...env.run(`anHead(${JSON.stringify(cols)}, ${n}).children.map((th) => th.textContent)`)];
  assert.deepStrictEqual(ths(page, ["session", "day", "project", "model", "tokens", "prompts", "cost"], 4), THS);
  assert.deepStrictEqual(ths(page, ["model", "cost", "% cost", "tokens", "prompts", "out/prompt", "cache-read"], 1), ["Model", "Cost", "% cost", "Tokens", "Prompts", "Out/prompt", "Cache-read"]);
  const phone = analyticsEnv();
  phone.page(false);
  assert.deepStrictEqual(ths(phone, ["session", "day", "project", "model", "tokens", "prompts", "cost"], 4), THS.map((t) => t.toLowerCase()), "the phone stays lowercase");
  // CSS: §C9 on .an-table. 12px / 500 / muted / sentence case, 32px rows, a hairline under each.
  const th = decls(ANP + " .an-table th");
  assert.strictEqual(th.get("font-size"), "var(--t-sm)");
  assert.strictEqual(th.get("font-weight"), "var(--w-medium)");
  assert.strictEqual(th.get("color"), "var(--muted)");
  assert.strictEqual(th.get("text-transform"), "none", "app.css's uppercase 10px head is the phone's");
  assert.strictEqual(th.get("letter-spacing"), "0");
  assert.strictEqual(th.get("height"), "var(--h-row)");
  assert.strictEqual(th.get("padding"), "0 var(--sp-3)");
  assert.strictEqual(th.get("border-bottom"), "1px solid var(--line)");
  assert.strictEqual(th.get("white-space"), "nowrap");
  const td = decls(ANP + " .an-table td");
  assert.strictEqual(td.get("height"), "var(--h-row)");
  assert.strictEqual(td.get("padding"), "0 var(--sp-3)");
  assert.strictEqual(td.get("border-bottom"), "1px solid var(--line)");
  assert.strictEqual(td.get("white-space"), "nowrap");
  assert.strictEqual(decls(ANP + " .an-table").get("font-size"), "var(--t-md)", "13px body cells, not app.css's 12px");
  assert.strictEqual(decls(ANP + " .an-table tr:last-child td").get("border-bottom"), "0");
  assert.strictEqual(decls(ANP + " .an-table tr + tr:hover").get("background"), "var(--hover)");
  assert.strictEqual(decls(ANP + " .an-table td.sub").get("color"), "var(--muted)");
});

test("Sessions on the page follow §3.4", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  env.run("AN.data.sessionCount = 1420;");
  const root = view(env, "anSessions");
  assert.deepStrictEqual(root.elements.map((n) => n.tag + "." + n.className), ["div.kpi-row", "section.an-psec"]);
  const [row, sec] = root.elements;

  // Three tiles: the count, the costliest session by its workspace, the median per session.
  assert.deepStrictEqual(tilesOf(row), [
    { value: "1,420", label: "Sessions in this window" },
    { value: "$121", label: "Costliest session · acme-api" },
    { value: "$80.07", label: "Median per session" },
  ]);

  // One card: its head says a row opens the session; then the table. No band card, no section title outside the card.
  assert.deepStrictEqual(sec.elements.map((n) => n.tag + "." + n.className), ["div.an-card"]);
  const card = sec.elements[0];
  assert.deepStrictEqual(card.elements[0].elements.map((n) => [n.tag, n.className, n.textContent]), [
    ["h2", "t-md w-600", "Top sessions by cost"],
    ["span", "t-sm muted", "click a row to open the session"],
  ]);
  const { tbl, head: ths, rows } = tableOf(card);
  assert.strictEqual(tbl.className, "an-table an-stbl");
  assert.deepStrictEqual(ths.map((t) => t.textContent), ["Session", "Last day", "Workspace", "Where", "Branch", "Model", "Prompts", "API value"]);
  assert.deepStrictEqual(ths.map((t) => t.className), ["", "", "", "", "", "", "num", "num"]);
  assert.strictEqual(rows.length, 4);
  const cell = (c) => [c.className, c.elements.map((s) => [s.tag, s.className, s.textContent]), c.textContent];
  assert.deepStrictEqual(rows[0].map(cell), [
    ["", [["span", "mono t-sm", "3d6b98e8"]], "3d6b98e8"],
    ["sub tnum", [], "2026-10-31"],
    ["an-wrap", [["span", "mono t-sm", "acme-api"]], "acme-api"],
    ["", [["span", "chip chip-box an-wbox", "box · prd"]], "box · prd"],
    ["an-ell", [["span", "mono t-sm", "feat/370-rework"]], "feat/370-rework"],
    ["sub", [], "mixed"],
    ["num", [], "11"],
    ["num w-500", [], "$121"],
  ]);
  assert.strictEqual(rows[0][4].attrs.title, "feat/370-rework", "a branch ellipsizes; its full name is the cell's title");
  assert.deepStrictEqual(rows[1].map(cell).map((c) => c[2]), ["2cf1c405", "2026-10-30", "acme-terraform-course-eu-west", "host", "main", "opus-5-5", "116", "$90.00"]);
  assert.deepStrictEqual(cell(rows[1][3])[1], [["span", "chip chip-host", "host"]]);
  assert.deepStrictEqual(cell(rows[1][5]), ["", [["span", "mono t-sm", "opus-5-5"]], "opus-5-5"], "a single model is mono, not muted");
  // A session with no recorded branch or runtime: a dash, and the plain tag.
  assert.deepStrictEqual(cell(rows[2][4]), ["an-ell sub", [], "—"]);
  assert.strictEqual(rows[2][4].attrs.title, undefined);
  assert.deepStrictEqual(cell(rows[2][3])[1], [["span", "chip", "unknown"]]);
  assert.deepStrictEqual(rows.map((r) => r[0].textContent), ["3d6b98e8", "2cf1c405", "9d3dc2af", "0174c7b1"]);

  // The old boxes and the band card are gone from the page.
  const all = [];
  const walk = (n) => { all.push(n); n.elements.forEach(walk); };
  walk(root);
  assert.ok(!all.some((n) => /an-box|an-fleet|an-grid2|an-section|an-split|an-band/.test(n.className)));
  assert.ok(!/Fleet at a glance|Session shape|Session cost|band/i.test(root.textContent));
});

test("Sessions on the page: a row culibox still holds opens that session like its sidebar row; the others are plain rows", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  const opened = [];
  env.ctx.lastSessions = [{ id: "live-1", name: "culipulse" }, { id: "live-other", name: "x" }];
  env.ctx.openTerminal = (id, name) => opened.push([id, name]);
  const { rows } = tableOf(view(env, "anSessions").elements[1].elements[0]);
  // 3d6b98e8 and 9d3dc2af carry a liveId in the fixture (one of them has since left the live list).
  const live = rows.filter((r) => r[0].parent.cls.includes("an-live"));
  assert.deepStrictEqual(live.map((r) => r[0].textContent), ["3d6b98e8", "9d3dc2af"]);
  for (const tr of live.map((r) => r[0].parent)) {
    assert.strictEqual(tr.attrs.role, "button");
    assert.strictEqual(tr.attrs.tabindex, "0");
  }
  const plain = rows.filter((r) => !r[0].parent.cls.includes("an-live")).map((r) => r[0].parent);
  assert.strictEqual(plain.length, 2);
  for (const tr of plain) {
    assert.strictEqual(tr.attrs.role, undefined, "no role");
    assert.strictEqual(tr.attrs.tabindex, undefined, "no focus stop");
    assert.strictEqual(tr.onclick, undefined);
    assert.strictEqual(tr.onkeydown, undefined);
    assert.deepStrictEqual(tr.cls, [], "no class that could carry a hover or a cursor");
  }
  const [open, gone] = live.map((r) => r[0].parent);
  open.onclick();
  assert.deepStrictEqual(opened, [["live-1", "culipulse"]], "the sidebar row's own call: openTerminal(id, name)");
  // Enter and Space do it; another key does not, and the key's default is held.
  let prevented = 0;
  for (const key of ["Enter", " ", "a", "Tab"]) open.onkeydown({ key, preventDefault: () => prevented++ });
  assert.strictEqual(opened.length, 3);
  assert.strictEqual(prevented, 2);
  // A session the live list no longer holds opens nothing.
  gone.onclick();
  assert.strictEqual(opened.length, 3);
});

test("Sessions on the phone: lowercase heads, the Session shape section is gone", () => {
  const env = analyticsEnv();
  env.page(false);
  loadWindow(env);
  const root = view(env, "anSessions");
  const all = [];
  const walk = (n) => { all.push(n); n.elements.forEach(walk); };
  walk(root);
  const text = (cls) => all.filter((n) => n.cls.includes(cls)).map((n) => n.textContent);
  assert.deepStrictEqual(text("an-section").map((t) => t.slice(0, 20)), ["Top sessions by cost"]);
  assert.ok(text("an-hint").includes("Spot the runaway session burning the budget. Counts include subagent / synthetic transcripts."));
  assert.deepStrictEqual(text("an-fleet"), [], "the Session shape and Fleet at a glance boxes are gone");
  assert.deepStrictEqual(all.filter((n) => n.tag === "th").map((n) => n.textContent), ["session", "day", "project", "model", "tokens", "prompts", "cost"]);
  assert.ok(!all.some((n) => /kpi-row|an-card|an-psec|an-split|section-title/.test(n.className)), "none of the page's nodes");
});

test("the Sessions table's columns are fixed and let go below 1020px of body; the split and the cost bands are gone", () => {
  // The body is the container; at 1440 it is 1086 wide (290px sidebar, 32px sides).
  const body = decls(ANP + " #analytics-body");
  assert.strictEqual(body.get("container-type"), "inline-size");
  assert.strictEqual(body.get("container-name"), "an-body");
  assert.ok(!RULES.some((r) => parts(r.selector).some((x) => /\.an-split|\.an-band/.test(x))), "no rule is left for the removed histogram card");
  // The fixed columns: 88/100/auto/132/200/112/76/96 (sum of the fixed ones 804).
  assert.strictEqual(decls(ANP + " .an-stbl").get("table-layout"), "fixed");
  const w = (n) => decls(ANP + " .an-stbl th:nth-child(" + n + ")").get("width");
  assert.deepStrictEqual([1, 2, 3, 4, 5, 6, 7, 8].map(w), ["88px", "100px", undefined, "132px", "200px", "112px", "76px", "96px"]);
  // A body too narrow for them (804px and 200px for Workspace) sizes the columns to the content (Workspace keeps room) and the
  // card's wrapper scrolls, not the page.
  const narrow = CONTAINER_RULES.filter((r) => r.at.includes("@container an-body (max-width: 1019px)"));
  assert.strictEqual(narrow.find((r) => parts(r.selector).includes(ANP + " .an-stbl")).decls.get("table-layout"), "auto");
  assert.ok(narrow.some((r) => parts(r.selector).includes(ANP + " .an-stbl th:nth-child(n)") && r.decls.get("width") === "auto"), "the columns let go of their widths");
  assert.ok(narrow.some((r) => parts(r.selector).includes(ANP + " .an-stbl :is(th, td):nth-child(3)") && /^\d+px$/.test(r.decls.get("min-width"))), "Workspace keeps a minimum");
  // The two cards stack in a body under 672px.
  const stack = CONTAINER_RULES.filter((r) => r.at.includes("@container an-body (max-width: 671px)") && parts(r.selector).includes(ANP + " .an-pgrid"));
  assert.strictEqual(stack.length, 1);
  assert.strictEqual(stack[0].decls.get("grid-template-columns"), "minmax(0, 1fr)");
  // A branch ellipsizes (the cell is nowrap through the table's rule); only a workspace wraps (§B8 .m3-wrap), one line still the 32px row.
  const ell = decls(ANP + " .an-table td.an-ell");
  assert.strictEqual(ell.get("overflow"), "hidden");
  assert.strictEqual(ell.get("text-overflow"), "ellipsis");
  const wrap = decls(ANP + " .an-table td.an-wrap");
  assert.strictEqual(wrap.get("white-space"), "normal");
  assert.strictEqual(wrap.get("overflow-wrap"), "anywhere");
  assert.strictEqual(wrap.get("height"), "auto");
  assert.strictEqual(wrap.get("padding-top"), "6px");
  assert.strictEqual(wrap.get("padding-bottom"), "6px");
  assert.strictEqual(wrap.get("line-height"), "var(--lh-md)");
  // A row that opens has a pointer and the row hover; the plain rows of this table lose the generic hover and have no pointer.
  assert.strictEqual(decls(ANP + " .an-stbl tr.an-live").get("cursor"), "pointer");
  assert.strictEqual(decls(ANP + " .an-stbl tr:not(.an-live):hover").get("background"), "transparent");
  assert.strictEqual(decls(ANP + " .an-table tr + tr:hover").get("background"), "var(--hover)", "the generic hover stays for the other tables and for a live row");
  assert.ok(!RULES.some((r) => parts(r.selector).some((x) => /\.an-stbl/.test(x) && !/\.an-live/.test(x) && /cursor/.test([...r.decls.keys()].join(" ")))), "no cursor on a plain row");
});

test("the page's cards and sections: a panel with the kit's radius, a title row on one baseline, 16px apart", () => {
  const card = decls(ANP + " .an-card");
  assert.strictEqual(card.get("background"), "var(--panel)");
  assert.strictEqual(card.get("border-radius"), "var(--r-card)");
  assert.strictEqual(card.get("border"), undefined, "the kit's .card has no outline");
  assert.strictEqual(decls(ANP + " .an-card > .an-tablewrap").get("border-radius"), "var(--r-card)", "a table card clips its row hover to the corners");
  assert.strictEqual(decls(ANP + " .an-card-body").get("padding"), "var(--sp-3) var(--sp-4) var(--sp-4)", "§D1 .m3-cardbody: 12px 16px 16px");
  assert.strictEqual(decls(ANP + " .an-card .card-head > :is(h2, h3)").get("margin"), "0");
  const head = decls(ANP + " .an-psec-head");
  assert.strictEqual(head.get("display"), "flex");
  assert.strictEqual(head.get("align-items"), "baseline");
  assert.strictEqual(head.get("gap"), "10px");
  assert.strictEqual(head.get("flex-wrap"), "wrap", "a long caption drops under the title instead of overflowing");
  const sec = decls(ANP + " .an-psec");
  assert.strictEqual(sec.get("display"), "flex");
  assert.strictEqual(sec.get("flex-direction"), "column");
  assert.strictEqual(sec.get("gap"), "var(--sp-2)");
  assert.strictEqual(sec.get("min-width"), "0");
  assert.strictEqual(decls(ANP + " #analytics-body > .kpi-row").get("margin-bottom"), "var(--sp-4)", "§B0: 16px between the KPI row and what follows");
  // The KPI labels ellipsize on both pages through one rule.
  const label = RULES.filter((r) => parts(r.selector).includes(AU + " .kpi-label"));
  assert.strictEqual(label.length, 1);
  assert.ok(parts(label[0].selector).includes(ANP + " .kpi-label"));
  assert.strictEqual(label[0].decls.get("text-overflow"), "ellipsis");
  // The phone's amber .card is never worn: the page's card has its own name.
  const WEARS_CARD = /["'`](?:[^"'`\n]*\s)?card(?:\s[^"'`\n]*)?["'`]/;
  assert.ok(WEARS_CARD.test('el("div", "card")') && WEARS_CARD.test('el("div", "x card y")') && !WEARS_CARD.test('el("div", "an-card")'), "the matcher sees a class word");
  assert.ok(!WEARS_CARD.test(stripAnalytics(read("analytics.js"))), "no element of analytics.js wears .card");
});

test("Models, Projects and Cost drivers on the page: cards, kit tables and section titles", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  const cards = (n) => n.elements.filter((c) => c.cls.includes("an-card"));
  const head = (sec) => sec.elements[0].elements.map((n) => [n.tag, n.className, n.textContent]);

  const models = view(env, "anModels");
  assert.deepStrictEqual(models.elements.map((n) => n.tag + "." + n.className), ["section.an-psec"], "the share charts are gone");
  assert.deepStrictEqual(models.elements[0].elements.map((n) => n.tag + "." + n.className), ["div.an-card", "p.an-foot t-sm muted"], "one card, then the footnote");
  const m = tableOf(cards(models.elements[0])[0]);
  assert.deepStrictEqual(m.head.map((t) => t.textContent), ["Model", "API value", "% of value", "Sessions", "Median / session", "Per prompt", "Cache hit", "Tokens"]);
  assert.deepStrictEqual(m.head.map((t) => t.className), ["", "num", "num", "num", "num", "num", "num", "num"]);
  assert.strictEqual(m.head[3].attrs.title, "A session that used several models counts once in each.", "§3.5: the Sessions header says a session counts once in each model");
  assert.strictEqual(m.head.filter((t) => t.attrs.title).length, 1);
  assert.deepStrictEqual(m.rows.map((r) => r.map((c) => c.textContent)), [
    ["opus-5-5", "$4.3k", "100%", "4", "$80.07", "$1.08", "59.8%", "10.4B"],
    ["other (unpriced)", "—", "—", "4", "—", "—", "—", "0"],
  ], "§3.5: the median is over each model's own cost per session; other (unpriced) has dashes for every value");
  assert.deepStrictEqual(m.rows[0][0].elements.map((c) => [c.tag, c.className]), [["span", "an-swatch"], ["span", "mono t-sm"]], "a model: its swatch and its id in mono");
  assert.deepStrictEqual(m.rows[1][0].elements.map((c) => [c.tag, c.className]), [["span", "an-swatch"], ["span", "muted"]], "the unpriced row is a label, not a model id: sans, muted");
  assert.deepStrictEqual(m.rows.map((r) => r[1].className), ["num w-500", "num w-500"]);
  assert.deepStrictEqual(m.rows.map((r) => r[7].className), ["num sub", "num sub"], "Tokens is muted");
  assert.ok(models.textContent.endsWith("their tokens are counted, their value is not."), "the footnote says what other (unpriced) is");

  const projects = view(env, "anProjects");
  assert.deepStrictEqual(projects.elements.map((n) => n.tag + "." + n.className), ["section.an-psec", "section.an-psec"]);
  assert.deepStrictEqual(head(projects.elements[0]), [["h2", "section-title", "Cost by project"], ["span", "t-sm muted", "Bar split shows the model mix inside each project"]]);
  assert.deepStrictEqual(head(projects.elements[1]), [["h2", "section-title", "Detail"]]);
  const p = tableOf(cards(projects.elements[1])[0]);
  assert.deepStrictEqual(p.head.map((t) => t.textContent), ["Project", "Cost", "Tokens", "Prompts"]);
  assert.deepStrictEqual(p.rows.map((r) => r[0].textContent), ["acme-terraform-course-eu-west", "acme-api", "acme-docs"]);
  assert.deepStrictEqual(p.rows[0][0].elements.map((c) => [c.tag, c.className]), [["span", "mono t-sm"]]);
  assert.strictEqual(p.rows[0][0].className, "an-wrap", "a long project wraps, never clips");
  assert.deepStrictEqual(p.rows.map((r) => r[1].textContent), ["$160", "$121", "$4.20"]);

  // Cost drivers: the old kinds are gone; with no driver data the section says why.
  const drivers = view(env, "anTools");
  assert.deepStrictEqual(drivers.elements.map((n) => n.tag + "." + n.className), ["section.an-psec"]);
  assert.deepStrictEqual(head(drivers.elements[0]), [["h2", "section-title", "Cost drivers"]]);
  assert.strictEqual(drivers.textContent.endsWith("No cost-driver data in this window: these sessions' transcripts are no longer on disk."), true);
});

test("no off-scale analytics text on desktop", () => {
  // app.css sets these in raw px for the phone and nothing in desktop.css answered them (Plan 1 follow-up 1): each gets a
  // kit step under the page.
  const app = parseRules(APP_CSS);
  const raw = (sel) => app.filter((r) => parts(r.selector).includes(sel)).map((r) => r.decls.get("font-size")).filter(Boolean).pop();
  const STEP = /^var\(--t-(?:xs|sm|md|lg|xl|kpi)\)$/;
  for (const sel of [".an-empty", ".an-hint", ".an-badge", ".an-bar .cap", ".an-hbar", ".an-legend", ".an-kdelta", ".an-divrow", ".an-mixlabel"]) {
    assert.match(raw(sel) || "", /px$/, sel + " is a px size on the phone (so the page must answer it)");
    const size = decls(ANP + " " + sel).get("font-size");
    assert.match(size || "", STEP, sel + " has a kit size under the page");
  }
  assert.strictEqual(decls(ANP + " .an-empty").get("line-height"), "var(--lh-md)");
  // Every analytics rule the page wrote in this task sets a size only through a token (desktop-type.test.js covers the whole file).
  for (const r of RULES) {
    if (!parts(r.selector).some((s) => s.startsWith(ANP) && /\.an-(?:card|psec|split|band|table|empty|hint|badge|bar)|\.kpi/.test(s))) continue;
    for (const [k, v] of r.decls) if (k === "font-size") assert.match(v, STEP, r.selector);
  }
});

// digest: a node tree hashed (the cost chart's pin below).
const crypto = require("node:crypto");
const fullSig = (n) => (n instanceof TextNode ? n.text : [n.tag, n.className, n.attrs, n.style, n.title || "", n.innerHTML || "", ...n.children.map(fullSig)]);
const digest = (root) => crypto.createHash("sha256").update(JSON.stringify(fullSig(root))).digest("hex").slice(0, 16);

test("every class the Analytics views build has a rule or is a named hook", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  const used = new Set();
  const walk = (n) => { n.cls.forEach((c) => used.add(c)); n.elements.forEach(walk); };
  for (const fn of ["anOverview", "anModels", "anProjects", "anSessions", "anTools"]) walk(view(env, fn));
  for (const c of ["kpi-row", "kpi", "kpi-value", "kpi-label", "an-card", "an-card-body", "an-psec", "an-psec-head", "an-stbl", "an-wrap", "an-pgrid", "section-title", "card-head", "meter"]) assert.ok(used.has(c), c + " is built");
  const hooks = new Set(["an-timewrap"]);
  const css = DESK_CSS + APP_CSS + stripComments(read("primitives.css"));
  const defined = new Set([...css.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)].map((m) => m[1]));
  assert.deepStrictEqual([...used].filter((c) => !defined.has(c) && !hooks.has(c)), [], "classes with no rule");
});

test("Model cells are nowrap cells: only Project wraps, so Model keeps its word width in a narrow body", () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  const { rows } = tableOf(view(env, "anSessions").elements[1].elements[0]);
  assert.ok(rows.length >= 2);
  for (const r of rows) {
    assert.deepStrictEqual(r.map((c) => c.cls.includes("an-wrap")), [false, false, true, false, false, false, false, false], "§B8: Workspace is the wrap cell, no other");
  }
  // The cascade for a Model cell (the 4th): no rule that reaches an .an-table cell by position or by class other than .an-wrap
  // sets a wrap, and the base cell is nowrap; so a nowrap word can't be squeezed to one glyph by the auto layout.
  assert.strictEqual(decls(ANP + " .an-table td").get("white-space"), "nowrap");
  const wrapping = [...RULES].filter((r) => (r.decls.get("white-space") === "normal" || r.decls.get("overflow-wrap") === "anywhere") && parts(r.selector).some((s) => s.startsWith(ANP) && /\.an-(?:table|stbl)/.test(s)));
  assert.deepStrictEqual(wrapping.map((r) => r.selector), [ANP + " .an-table td.an-wrap"], "only the .an-wrap cell wraps");
  // The narrow layout gives Workspace a floor and no other column one: the shortage goes to the wrap cell, never to Model.
  const narrow = CONTAINER_RULES.filter((r) => r.at.includes("@container an-body (max-width: 1019px)") && parts(r.selector).some((s) => /\.an-stbl/.test(s)));
  const floors = narrow.filter((r) => r.decls.has("min-width")).flatMap((r) => parts(r.selector));
  assert.deepStrictEqual(floors, [ANP + " .an-stbl :is(th, td):nth-child(3)"]);
  assert.ok(!narrow.some((r) => r.decls.has("white-space") || r.decls.has("overflow-wrap")), "the narrow layout changes no cell's wrapping");
});

// --- the page's views and the phone's share their data preparation ------------------------------------------------

test("the page and the phone say the same thing: figures, colours, caps and words come from one preparation", () => {
  const collect = (root) => {
    const out = { titles: [], hints: [], cardTitles: [], bars: [], tables: [] };
    const walk = (n) => {
      const c = n.cls;
      if (n.tag === "h3" && !c.length && n.parent && n.parent.cls.includes("an-section")) out.titles.push(n.textContent);
      if (n.tag === "h2" && c.includes("section-title")) out.titles.push(n.textContent);
      if (c.includes("an-hint")) out.hints.push(n.textContent);
      if (n.tag === "span" && c.includes("muted") && n.parent && n.parent.cls.includes("an-psec-head")) out.hints.push(n.textContent);
      if (n.tag === "h4" || (n.tag === "h3" && c.includes("w-600"))) out.cardTitles.push(n.textContent);
      if (c.includes("an-hbar")) out.bars.push([n.elements[0].textContent, n.elements[2].textContent, n.elements[1].elements.map((f) => [f.style.width, f.style.background])]);
      if (n.tag === "table") out.tables.push(n.elements.map((r) => r.elements.map((x) => x.textContent)));
      n.elements.forEach(walk);
    };
    walk(root);
    return out;
  };
  const both = (fn, over) => {
    const phone = analyticsEnv(); phone.page(false); loadWindow(phone, over);
    const page = analyticsEnv(); page.page(true); loadWindow(page, over);
    return [collect(view(phone, fn)), collect(view(page, fn))];
  };
  const lower = (rows) => rows.map((r, i) => (i ? r : r.map((t) => t.toLowerCase())));
  for (const fn of ["anProjects", "anTools"]) {
    const [ph, pg] = both(fn);
    assert.ok(ph.titles.length >= 1, fn);
    assert.deepStrictEqual(pg.titles, ph.titles, fn + ": section titles");
    // The window is in the page's head, so the phone's " · window" (a word that pointed at the date range beside it) is not on the page.
    assert.deepStrictEqual(pg.hints, ph.hints.map((h) => h.replace(/ · window$/, "")), fn + ": captions, without the dangling window");
    assert.ok(!pg.hints.some((h) => /·\s*window\s*$/.test(h)), fn + ": no dangling separator on the page");
    assert.deepStrictEqual(pg.cardTitles, ph.cardTitles, fn + ": card titles");
    assert.deepStrictEqual(pg.bars, ph.bars, fn + ": bars (names, values, widths, colours)");
    assert.deepStrictEqual(pg.tables.map(lower), ph.tables.map(lower), fn + ": table rows (the page capitalises the heads)");
  }
  // Sessions and Models: the page's tables have the columns of §3.4 and §3.5 (the phone moves to them in its own task), so the
  // two skins are compared on what they share: the same rows, in the same order, under the same ids.
  const [ph, pg] = both("anSessions");
  assert.ok(ph.titles.includes("Top sessions by cost"));
  assert.deepStrictEqual(pg.tables[0].slice(1).map((r) => r[0]), ph.tables[0].slice(1).map((r) => r[0]));
  const [phm, pgm] = both("anModels");
  assert.deepStrictEqual(pgm.tables[0].slice(1).map((r) => r[0]), phm.tables[0].slice(1).map((r) => r[0]));
  // No driver data: the same sentence.
  const [pn, gn] = both("anTools");
  assert.deepStrictEqual(gn.hints, pn.hints);

  // One definition each: the words of the views are written once in analytics.js.
  const src = stripAnalytics(read("analytics.js"));
  assert.ok(src.includes("function anModelsPage") && src.includes("function anToolsPage"), "the strip kept the whole file");
  const once = (lit) => assert.strictEqual(src.split(lit).length - 1, 1, lit + " is written once");
  for (const lit of ["No cost-driver data in this window", "Per-model breakdown", "Cost, volume & efficiency",
    "Bar split shows the model mix", "\"model\", \"cost\", \"% cost\"", "\"project\", \"cost\", \"tokens\", \"prompts\"", "\"session\", \"day\", \"project\", \"model\""]) once(lit);
});

// =====================================================================================================================
// Analytics Overview: the cost chart, its legend and footnote, the two cost cards, the Compare block's parts and the CSS of
// the palette (Task 6; frame M3.1, digest-design §A2, §B6, §B7, §D1, §E, §F2)
// =====================================================================================================================

// A 30-day window as the frame draws it (§B6): two models, 10-19 the peak at $421 (opus 281 on sonnet 140), 10-09 opus 45 on
// sonnet 95, two opus-only days and 10-29 a gap with no cost.
function chartRows() {
  const rows = [];
  for (let i = 0; i < 30; i++) {
    const day = new Date(Date.UTC(2026, 9, 9 + i)).toISOString().slice(0, 10);
    if (day === "2026-10-29") continue;
    const opus = day === "2026-10-09" ? 45 : day === "2026-10-19" ? 281 : 150 + ((i * 7) % 40);
    const sonnet = day === "2026-10-12" || day === "2026-10-22" ? 0 : day === "2026-10-09" ? 95 : day === "2026-10-19" ? 140 : 40 + ((i * 11) % 30);
    rows.push(ROW({ day, model: "claude-opus-5-5", costUsd: opus }));
    if (sonnet) rows.push(ROW({ day, model: "claude-sonnet-5-5", costUsd: sonnet }));
  }
  return rows;
}
// drawChart: anStackedTime for `rows` in `env` (the page or the phone), as the wrapper it returns.
function drawChart(env, rows, from, to, fitW) {
  env.ctx.ROWS = rows;
  return env.run(`anStackedTime(foldCostByDay(ROWS), ${JSON.stringify(from)}, ${JSON.stringify(to)}${fitW ? ", " + fitW : ""})`);
}
const nodesOf = (root, tag) => {
  const out = [];
  const walk = (n) => n.elements.forEach((c) => { if (c.tag === tag) out.push(c); walk(c); });
  walk(root);
  return out;
};
const num = (v) => Number(v);
// An upper estimate of a glyph at 11px (a digit is about 6.1px in Inter, tabular), to box a label without a browser.
const GLYPH = 6.6;
const textBox = (t) => {
  const x = num(t.attrs.x), y = num(t.attrs.y), w = t.textContent.length * GLYPH, a = t.attrs["text-anchor"];
  const l = a === "end" ? x - w : a === "middle" ? x - w / 2 : x;
  return { l, r: l + w, t: y - 8, b: y + 3, what: "“" + t.textContent + "”" };
};
const rectBox = (r) => ({ l: num(r.attrs.x), r: num(r.attrs.x) + num(r.attrs.width), t: num(r.attrs.y), b: num(r.attrs.y) + num(r.attrs.height), what: "bar" });
const overlaps = (a, c) => a.l < c.r && c.l < a.r && a.t < c.b && c.t < a.b;
// collisions: every pair of boxes (labels and bars) that overlap, as text.
function collisions(svg) {
  const boxes = [...nodesOf(svg, "text").map(textBox), ...nodesOf(svg, "rect").map(rectBox)];
  const out = [];
  for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
    if (boxes[i].what === "bar" && boxes[j].what === "bar") continue;
    if (overlaps(boxes[i], boxes[j])) out.push(boxes[i].what + " / " + boxes[j].what);
  }
  return out;
}
// No attribute of any node says NaN or Infinity.
function badNumbers(root) {
  const out = [];
  const walk = (n) => { for (const [k, v] of Object.entries(n.attrs)) if (/NaN|Infinity/.test(v)) out.push(n.tag + " " + k + "=" + v); n.elements.forEach(walk); };
  walk(root);
  return out;
}

test("the page chart has no zero label and its labels clear the bars", () => {
  const env = analyticsEnv();
  env.page(true);
  const C = { ...env.run("AN_PAGE_CHART") };
  const wrap = drawChart(env, chartRows(), "2026-10-09", "2026-11-07", 1054);
  assert.ok(wrap.cls.includes("an-timewrap"), "still the wrapper anRender scrolls and anFitTimeChart replaces");
  const svg = wrap.elements[0];
  assert.strictEqual(svg.tag, "svg");
  assert.strictEqual(svg.attrs.overflow, "visible", "the top label may poke above the plot");
  assert.strictEqual(svg.attrs.width, "1054", "the fit width is the svg's width: the plot is that less the 44px gutter, 1010px");
  assert.strictEqual(svg.attrs.role, "img");
  assert.strictEqual(svg.attrs["aria-label"], "Daily cost stacked by model, 2026-10-09 to 2026-11-07, peak $421 on 10-19", "§A2");
  const texts = nodesOf(svg, "text");
  const yl = texts.filter((t) => t.attrs["text-anchor"] === "end");
  const xl = texts.filter((t) => t.attrs["text-anchor"] === "middle");
  assert.strictEqual(texts.length, yl.length + xl.length, "no other anchor");

  // Y labels: three, the peak and its thirds, no $0.00; right-aligned at the label box's edge (38) with the plot at 44.
  assert.deepStrictEqual(yl.map((t) => t.textContent), ["$421", "$281", "$140"]);
  assert.ok(!texts.some((t) => /\$0\.00|NaN/.test(t.textContent)));
  const lines = nodesOf(svg, "line");
  assert.strictEqual(lines.length, 4, "gridlines at the peak, 2/3, 1/3 and the baseline");
  assert.deepStrictEqual(lines.map((l) => l.attrs.x1), Array(4).fill(String(C.gutter)), "the plot starts at x = 44");
  assert.deepStrictEqual(lines.map((l) => l.attrs.x2), Array(4).fill("1054"));
  assert.ok(lines.every((l) => l.style.stroke === "var(--line)" && l.attrs["stroke-width"] === "1"), "1px --line, as §B6");
  const gy = lines.map((l) => num(l.attrs.y1));
  const plotTop = gy[0] - 0.5, plotBottom = gy[3] - 0.5;
  assert.strictEqual(plotBottom - plotTop, C.plotH, "150px plot");
  assert.deepStrictEqual(gy.map((y) => y - gy[0]), [0, 50, 100, 150], "§B6: 0, 50, 100, 150");
  yl.forEach((t, k) => {
    assert.strictEqual(t.attrs.x, String(C.labelBox), "text-anchor end at x = 38");
    assert.ok(Math.abs(num(t.attrs.y) - 4 - gy[k]) <= 0.5, "label " + t.textContent + " is centred on its gridline (an 11px digit is 8px tall: its baseline is 4px under the centre)");
  });
  assert.ok(num(lines[0].attrs.x1) - num(yl[0].attrs.x) >= 6, "6px between the label box and the plot");

  // X labels: every 3rd of 30 slots (§A2's list), centred under their slot, their baseline well under the plot.
  assert.deepStrictEqual(xl.map((t) => t.textContent), ["10-09", "10-12", "10-15", "10-18", "10-21", "10-24", "10-27", "10-30", "11-02", "11-05"]);
  const slotW = (1054 - C.gutter) / 30;
  xl.forEach((t, k) => assert.ok(Math.abs(num(t.attrs.x) - (C.gutter + k * 3 * slotW + slotW / 2)) < 0.01, t.textContent + " is centred on its slot"));
  for (const t of xl) {
    assert.ok(num(t.attrs.y) >= C.plotH + C.xLabelGap, "the baseline is at least plotH + 6 down");
    assert.ok(num(t.attrs.y) - 8 >= plotBottom + C.xLabelGap, "and the glyph tops clear the plot's bottom by 6px");
  }

  // Bars: 16px, centred in their slot, stacked cost-ascending with a 1px gap; the frame's pixel numbers (§B6).
  const rects = nodesOf(svg, "rect");
  const day = (d) => rects.filter((r) => r.elements[0].textContent.startsWith(d + " · "));
  const [o9a, o9b] = day("2026-10-09");
  assert.deepStrictEqual([o9a, o9b].map((r) => r.elements[0].textContent), ["2026-10-09 · opus-5-5 $45.00", "2026-10-09 · sonnet-5-5 $95.00"], "the smaller segment (opus, $45) is at the bottom: the stack is cost-ascending");
  assert.deepStrictEqual([o9a, o9b].map((r) => num(r.attrs.height)), [16, 34], "10-09: opus 16px, sonnet 34px");
  assert.strictEqual(num(o9a.attrs.y) + num(o9a.attrs.height), plotBottom, "the first segment sits on the plot's bottom");
  assert.strictEqual(num(o9b.attrs.y) + num(o9b.attrs.height), num(o9a.attrs.y) - 1, "a 1px gap between the segments");
  assert.strictEqual(num(o9a.attrs.width), C.bar);
  assert.ok(Math.abs(num(o9a.attrs.x) - (C.gutter + (slotW - C.bar) / 2)) < 0.01, "centred in the first slot");
  assert.deepStrictEqual([o9a, o9b].map((r) => r.style.fill), ["var(--an-1)", "var(--an-2)"], "opus is slot 1, sonnet slot 2");
  const [p1, p2] = day("2026-10-19");
  assert.deepStrictEqual([p1, p2].map((r) => num(r.attrs.height)), [50, 100], "10-19, the peak: sonnet 50px under opus 100px");
  assert.strictEqual(num(p2.attrs.y), plotTop - 1, "151px with the gap: one px over the plot, as the frame draws it");
  assert.strictEqual(day("2026-10-12").length, 1, "an opus-only day is one segment");
  assert.strictEqual(day("2026-10-29").length, 0, "no cost, no bar: the gap");
  assert.ok(rects.every((r) => num(r.attrs.width) <= C.bar && num(r.attrs.x) >= C.gutter && num(r.attrs.height) >= 1));
  // The 4th gridline is drawn first and the bars over it; the labels clear every bar.
  assert.deepStrictEqual(collisions(svg), []);
  assert.deepStrictEqual(badNumbers(svg), []);
});

test("the page chart's data shapes: none, one day, one model, unpriced only, a narrow card, a very wide window", () => {
  const env = analyticsEnv();
  env.page(true);
  const C = { ...env.run("AN_PAGE_CHART") };
  const shape = (rows, from, to, fitW) => {
    const svg = drawChart(env, rows, from, to, fitW).elements[0];
    assert.deepStrictEqual(badNumbers(svg), [], "no NaN or Infinity");
    assert.deepStrictEqual(collisions(svg), [], "no overlap");
    const texts = nodesOf(svg, "text");
    assert.ok(!texts.some((t) => /NaN|\$0\.00/.test(t.textContent)), "no NaN or $0.00 label");
    return { svg, texts, yl: texts.filter((t) => t.attrs["text-anchor"] === "end"), rects: nodesOf(svg, "rect"), lines: nodesOf(svg, "line") };
  };
  // An empty window draws the plot's baseline and the window's dates under it, and nothing else: no scale, no y label, no bar.
  let s = shape([], "2026-10-09", "2026-11-07", 1054);
  assert.deepStrictEqual([s.yl.length, s.rects.length, s.lines.length], [0, 0, 1]);
  assert.deepStrictEqual(s.texts.map((t) => t.textContent), ["10-09", "10-12", "10-15", "10-18", "10-21", "10-24", "10-27", "10-30", "11-02", "11-05"], "the dates of the window's slots, bars or none (Gate 4 X1)");
  // Only unpriced models: max is 0, so no y labels and no bars, however many days.
  s = shape([ROW({ day: "2026-10-10", model: "<synthetic>", prompts: 3 }), ROW({ day: "2026-10-11", model: "", prompts: 2 })], "2026-10-09", "2026-11-07", 1054);
  assert.deepStrictEqual([s.yl.length, s.rects.length, s.lines.length], [0, 0, 1], "an all-unpriced page window draws no y labels");
  // A one-day window: one slot, the bar in the middle of the plot, its date under it.
  s = shape([ROW({ day: "2026-10-09", model: "claude-opus-5-5", costUsd: 12.5 })], "2026-10-09", "2026-10-09", 1054);
  assert.deepStrictEqual(s.yl.map((t) => t.textContent), ["$12.50", "$8.33", "$4.17"]);
  assert.strictEqual(s.rects.length, 1);
  assert.strictEqual(num(s.rects[0].attrs.height), C.plotH, "the peak day fills the plot");
  assert.ok(Math.abs(num(s.rects[0].attrs.x) + C.bar / 2 - (C.gutter + (1054 - C.gutter) / 2)) < 0.01, "centred");
  assert.deepStrictEqual(s.texts.filter((t) => t.attrs["text-anchor"] === "middle").map((t) => t.textContent), ["10-09"]);
  // A day that cost cents beside a $421 peak is still a bar, 1px (§B6: min 1px); a segment that cost nothing is not drawn at all.
  s = shape([ROW({ day: "2026-10-09", model: "claude-opus-5-5", costUsd: 421 }), ROW({ day: "2026-10-10", model: "claude-opus-5-5", costUsd: 0.05 }), ROW({ day: "2026-10-10", model: "claude-sonnet-5-5", costUsd: 0 })], "2026-10-09", "2026-10-11", 1054);
  assert.deepStrictEqual(s.rects.map((r) => num(r.attrs.height)), [C.plotH, 1], "the peak fills the plot, the cents are one px, the $0 segment has no rect");
  // One model, then eight (the 8th wraps to slot 1): every segment is a var(--an-N) of the seven.
  const eight = [];
  for (let m = 0; m < 8; m++) eight.push(ROW({ day: "2026-10-10", model: "claude-model-" + m, costUsd: 100 - m }));
  s = shape(eight, "2026-10-09", "2026-10-15", 1054);
  assert.deepStrictEqual(s.rects.map((r) => r.style.fill), ["an-1", "an-7", "an-6", "an-5", "an-4", "an-3", "an-2", "an-1"].map((n) => "var(--" + n + ")"), "cost-ascending: the cheapest model (the 8th, wrapped to slot 1) is at the bottom and the dearest (slot 1) on top");
  // A narrow card: slots of 8px, the bars thin and the labels thinned out until none touches its neighbour.
  s = shape(chartRows(), "2026-10-09", "2026-11-07", 300);
  assert.strictEqual(s.svg.attrs.width, "300");
  assert.ok(s.rects.every((r) => num(r.attrs.width) < C.bar && num(r.attrs.width) >= 2));
  assert.ok(s.texts.filter((t) => t.attrs["text-anchor"] === "middle").length < 10, "fewer date labels than at full width, never touching");
  // Below the per-day floor the chart scrolls (a 200-day span is index mode: 8px a slot) instead of squeezing.
  const wide = [];
  for (let i = 0; i < 200; i++) wide.push(ROW({ day: new Date(Date.UTC(2026, 0, 1 + i)).toISOString().slice(0, 10), model: "claude-opus-5-5", costUsd: 1 + (i % 9) }));
  s = shape(wide, "2026-01-01", "2026-07-19", 1054);
  assert.strictEqual(num(s.svg.attrs.width), Math.max(200 * 8 + C.gutter, 1054), "8px a slot plus the gutter, wider than the card: the wrapper scrolls");
});

// Gate 4 X1 (= Gate 3 D6): the date labels sit on every round(slots/12)-th slot of the window, from the first, whether or not that
// day has a bar; each says its own slot's date.
test("the page chart labels every Nth slot of the window, a day with no cost included", () => {
  const env = analyticsEnv();
  env.page(true);
  const C = { ...env.run("AN_PAGE_CHART") };
  const sparse = [0, 6, 12, 21, 24, 27].map((slot) => ROW({ day: new Date(Date.UTC(2026, 9, 11 + slot)).toISOString().slice(0, 10), model: "claude-opus-5-5", costUsd: 10 + slot }));
  const svg = drawChart(env, sparse, "2026-10-11", "2026-11-09", 1054).elements[0];
  const xl = nodesOf(svg, "text").filter((t) => t.attrs["text-anchor"] === "middle");
  assert.deepStrictEqual(xl.map((t) => t.textContent), ["10-11", "10-14", "10-17", "10-20", "10-23", "10-26", "10-29", "11-01", "11-04", "11-07"], "10 labels at a pitch of 3 slots, not the 6 that had a bar");
  const slotW = (1054 - C.gutter) / 30;
  xl.forEach((t, k) => assert.ok(Math.abs(num(t.attrs.x) - (C.gutter + k * 3 * slotW + slotW / 2)) < 0.01, t.textContent + " is centred on slot " + k * 3));
  assert.strictEqual(nodesOf(svg, "rect").length, 6, "only the six days that cost anything are drawn");
  assert.deepStrictEqual(collisions(svg), []);
  // An empty window has its dates too, under the baseline.
  const empty = drawChart(env, [], "2026-10-09", "2026-10-15", 1054).elements[0];
  assert.deepStrictEqual(nodesOf(empty, "text").map((t) => t.textContent), ["10-09", "10-10", "10-11", "10-12", "10-13", "10-14", "10-15"], "7 slots of 143px: every slot is labelled");
  // A very wide window is index mode: the labels follow the bars, as before.
  const wide = [];
  for (let i = 0; i < 200; i++) wide.push(ROW({ day: new Date(Date.UTC(2026, 0, 1 + i)).toISOString().slice(0, 10), model: "claude-opus-5-5", costUsd: 1 + (i % 9) }));
  const idx = drawChart(env, wide, "2026-01-01", "2026-07-19", 1054).elements[0];
  const il = nodesOf(idx, "text").filter((t) => t.attrs["text-anchor"] !== "end");
  assert.strictEqual(il[0].textContent, "01-01");
  assert.ok(il.length >= 8 && il.length <= 20, "about one label in sixteen bars: " + il.length);
});

// Gate 3 D5: a label centred on the last slot poked 13px past the svg and was clipped ("11-(") on a 97-day window.
test("every x label of the page chart stays inside the svg, and none touches its neighbour", () => {
  const env = analyticsEnv();
  env.page(true);
  const rows = [];
  for (let i = 0; i < 97; i += 4) rows.push(ROW({ day: new Date(Date.UTC(2026, 7, 5 + i)).toISOString().slice(0, 10), model: "claude-opus-5-5", costUsd: 5 + (i % 11) }));
  rows.push(ROW({ day: "2026-11-09", model: "claude-opus-5-5", costUsd: 12 }));
  for (const W of [1054, 887, 700, 420]) {
    const svg = drawChart(env, rows, "2026-08-05", "2026-11-09", W).elements[0];
    const width = num(svg.attrs.width);
    const xl = nodesOf(svg, "text").filter((t) => t.attrs["text-anchor"] !== "end" || num(t.attrs.y) > 150);
    assert.ok(xl.length >= 2, "W " + W + ": labels drawn");
    for (const t of xl) {
      const b = textBox(t);
      assert.ok(b.l >= 0 && b.r <= width, "W " + W + ": " + t.textContent + " spans " + b.l.toFixed(1) + ".." + b.r.toFixed(1) + " of " + width);
    }
    assert.deepStrictEqual(collisions(svg), [], "W " + W);
  }
  // The label of the window's last day is still there, ending at the svg's right edge.
  const svg = drawChart(env, rows, "2026-08-05", "2026-11-09", 1054).elements[0];
  const last = nodesOf(svg, "text").find((t) => t.textContent === "11-09");
  assert.ok(last, "11-09 is labelled");
  assert.strictEqual(last.attrs["text-anchor"], "end");
  assert.strictEqual(num(last.attrs.x), 1054);
  // A window whose last slot is not a label slot keeps its centred labels.
  const mid = drawChart(env, rows.slice(0, 5), "2026-08-05", "2026-11-08", 1054).elements[0];
  assert.ok(nodesOf(mid, "text").filter((t) => /^\d\d-\d\d$/.test(t.textContent)).every((t) => t.attrs["text-anchor"] === "middle"));
});

test("the phone's chart keeps its geometry: 170px, a 24px pad, four y labels with $0.00 at x = 2, 9px axis text", () => {
  const env = analyticsEnv();
  env.page(false);
  const wrap = drawChart(env, chartRows(), "2026-10-09", "2026-11-07");
  const svg = wrap.elements[0];
  assert.strictEqual(svg.attrs.viewBox, "0 0 720 170");
  assert.strictEqual(svg.attrs.overflow, undefined);
  assert.strictEqual(svg.attrs.role, undefined);
  const y = nodesOf(svg, "text").filter((t) => t.attrs["text-anchor"] === "start");
  assert.deepStrictEqual(y.map((t) => [t.textContent, t.attrs.x]), [["$421", "2"], ["$281", "2"], ["$140", "2"], ["$0.00", "2"]]);
  assert.deepStrictEqual(nodesOf(svg, "line").map((l) => [l.attrs.x1, l.style.stroke, l.attrs["stroke-width"]]), Array(4).fill(["24", "var(--rule)", "0.5"]));
  assert.strictEqual(digest(wrap), "3b92301a50afd6aa", "the whole chart, hashed: measured on main's analytics.js (c47442b) and on the branch before Task 6");
  // An all-unpriced phone window still shows its four ticks (max is floored at 1, exactly as before).
  const flat = drawChart(env, [ROW({ day: "2026-10-10", model: "<synthetic>", prompts: 3 })], "2026-10-09", "2026-11-07").elements[0];
  assert.deepStrictEqual(nodesOf(flat, "text").filter((t) => t.attrs["text-anchor"] === "start").map((t) => t.textContent), ["$1.00", "$0.67", "$0.33", "$0.00"]);
  // app.css keeps the phone's 9px; desktop.css's 11px is the page's alone.
  assert.strictEqual(decls(".an-axis text", parseRules(APP_CSS)).get("font-size"), "9px");
});

test("axis text is --t-xs on the page", () => {
  assert.strictEqual(decls(ANP + " .an-axis text").get("font-size"), "var(--t-xs)");
  assert.strictEqual(decls(ANP + " .an-axis text").get("font-variant-numeric"), "tabular-nums", "§B6: the labels are tabular, like the frame's .tnum");
  assert.strictEqual(decls(ANP + " .an-timewrap > svg").get("display"), "block", "no descender gap under the chart: the card body is 12 + 150 + 22 + 16 as drawn");
  assert.deepStrictEqual(RULES.filter((r) => parts(r.selector).some((s) => /\.an-axis/.test(s) && !s.startsWith(ANP))).map((r) => r.selector), [], "no rule outside the page names the axis");
});

test("the fit width still redraws the chart at the card's width, with the new geometry", () => {
  // The fake DOM has no layout: a node measures 0 wide until a test says otherwise (a browser says the same of a detached one).
  Object.defineProperty(El.prototype, "clientWidth", { value: 0, writable: true, configurable: true });
  try {
    fitWidthTest();
  } finally {
    delete El.prototype.clientWidth;
  }
});
function fitWidthTest() {
  const env = analyticsEnv();
  env.page(true);
  const C = { ...env.run("AN_PAGE_CHART") };
  loadWindow(env, { summary: chartRows() });
  env.run("anRender();");
  const body = env.el("analytics-body");
  const old = body.querySelector(".an-timewrap");
  assert.ok(old, "the Overview draws the chart");
  const swaps = [];
  old.clientWidth = 900.4;
  old.replaceWith = (n) => { swaps.push(n); n.parent = old.parent; old.parent.children[old.parent.children.indexOf(old)] = n; };
  env.run("anFitTimeChart();");
  assert.strictEqual(swaps.length, 1);
  assert.strictEqual(swaps[0].dataset.fitw, "900");
  assert.strictEqual(swaps[0].elements[0].attrs.width, "900", "the svg is the card's width: the plot 900 - 44");
  assert.ok(swaps[0].cls.includes("an-timewrap"), "the redrawn chart is still the wrapper the next fit finds");
  assert.deepStrictEqual(nodesOf(swaps[0], "line").map((l) => l.attrs.x1), Array(4).fill(String(C.gutter)));
  swaps[0].clientWidth = 900;
  env.run("anFitTimeChart();");
  assert.strictEqual(swaps.length, 1, "the same width again redraws nothing");
  swaps[0].clientWidth = 640;
  swaps[0].replaceWith = (n) => swaps.push(n);
  env.run("anFitTimeChart();");
  assert.strictEqual(swaps.length, 2, "a new width redraws");
  assert.strictEqual(swaps[1].dataset.fitw, "640");
  // The phone never fits: its chart is the fixed 720px floor.
  const phone = analyticsEnv();
  phone.page(false);
  loadWindow(phone, { summary: chartRows() });
  phone.run("anRender();");
  const pw = phone.el("analytics-body").querySelector(".an-timewrap");
  pw.clientWidth = 900;
  pw.replaceWith = () => assert.fail("the phone's chart is never redrawn at a fit width");
  phone.run("anFitTimeChart();");
}

// A made-up window for the Overview: three priced models (one only on two days), an unpriced one, twelve projects, one of them
// split over two models.
function overviewWindow(env) {
  const rows = chartRows();
  rows.push(ROW({ day: "2026-10-10", model: "claude-haiku-5-5", costUsd: 1.2 }), ROW({ day: "2026-10-11", model: "claude-haiku-5-5", costUsd: 0.85 }));
  const projects = [
    ROW({ project: "-home-dev-acme-api", model: "claude-opus-5-5", costUsd: 120.5 }),
    ROW({ project: "-home-dev-acme-api", model: "claude-sonnet-5-5", costUsd: 40 }),
    ROW({ project: "-home-dev-acme-terraform-course-eu-west", model: "claude-opus-5-5", costUsd: 380 }),
    ROW({ project: "-home-dev-acme-docs", model: "claude-haiku-5-5", costUsd: 2.05 }),
  ];
  for (let i = 0; i < 9; i++) projects.push(ROW({ project: "-home-dev-proj-" + String.fromCharCode(97 + i), model: "claude-opus-5-5", costUsd: 100 - i * 5 }));
  loadWindow(env, {
    summary: rows,
    models: [
      ROW({ model: "claude-haiku-5-5", prompts: 9, costUsd: 2.05 }), ROW({ model: "claude-opus-5-5", prompts: 3994, costUsd: 3000 }),
      ROW({ model: "claude-sonnet-5-5", prompts: 80, costUsd: 1300 }), ROW({ model: "<synthetic>", prompts: 41 }),
    ],
    projects,
  });
  env.run("AN.data.sessionCount = 1420;");
}
const sigOf = (n) => n.tag + "." + n.className;

test("the legend sits in the card head; the footnote is a plain muted line under the card", () => {
  const env = analyticsEnv();
  env.page(true);
  overviewWindow(env);
  const root = view(env, "anOverview");
  assert.deepStrictEqual(root.elements.map(sigOf), ["div.kpi-row", "div.an-psec", "div.an-pgrid", "p.an-foot t-sm muted"], "the KPI row, the chart, the two cost cards, the footnote last (the mockup)");
  const sec = root.elements[1];
  assert.deepStrictEqual(sec.elements.map(sigOf), ["div.an-card"], "the chart's card alone");
  const card = sec.elements[0];
  const foot = root.elements[3];
  assert.strictEqual(foot.textContent, "Descriptive only: a difference between models reflects the tasks you gave each one, not model quality.", "§A2, final copy");
  assert.strictEqual(foot.children.length, 1, "plain text: no <b>, no <i>");
  assert.ok(!foot.innerHTML, "and nothing set as HTML");
  // The head: title, caption, a spacer, the legend.
  assert.deepStrictEqual(card.elements.map(sigOf), ["div.card-head", "div.an-card-body an-chartbody"]);
  const head = card.elements[0];
  assert.deepStrictEqual(head.elements.map(sigOf), ["h2.t-md w-600", "span.t-sm muted", "span.grow", "div.an-plegend"]);
  assert.deepStrictEqual(head.elements.slice(0, 2).map((n) => n.textContent), ["Cost over time", "Stacked by model · daily"]);
  const legend = head.elements[3];
  // Three priced models, in cost order, each its colour on the chart; the unpriced ones are not in it.
  assert.deepStrictEqual(legend.elements.map((s) => s.elements.map(sigOf)), Array(3).fill(["i.an-sw", "span.mono t-xs fg"]));
  assert.deepStrictEqual(legend.elements.map((s) => s.elements[1].textContent), ["opus-5-5", "sonnet-5-5", "haiku-5-5"]);
  assert.deepStrictEqual(legend.elements.map((s) => s.elements[0].style.background), ["var(--an-1)", "var(--an-2)", "var(--an-3)"], "the colours the bars wear");
  // The chart is in the body, and the page has no boxed disclaimer, no old section heading, no legacy box.
  assert.deepStrictEqual(card.elements[1].elements.map((n) => n.cls.includes("an-timewrap")), [true]);
  const all = [];
  const walk = (n) => { all.push(n); n.elements.forEach(walk); };
  walk(root);
  assert.ok(!all.some((n) => /an-cmpnote|an-box|an-section|an-grid2|an-legend\b/.test(n.className)), "none of the phone's nodes");
  assert.ok(!/Where the money goes|Cost by project \(top\)/.test(root.textContent));
  // An unpriced-only window has an empty legend item list, not a head with empty swatches.
  loadWindow(env, { summary: [ROW({ day: "2026-10-10", model: "<synthetic>", prompts: 3 })], models: [ROW({ model: "<synthetic>", prompts: 3 })], projects: [] });
  const bare = view(env, "anOverview");
  assert.strictEqual(bare.elements[1].elements[0].elements[0].elements[3].elements.length, 0, "no legend items when no model is priced");
});

test("the phone keeps its boxed disclaimer, its legend under the chart and its section headings", () => {
  const env = analyticsEnv();
  env.page(false);
  overviewWindow(env);
  const root = view(env, "anOverview");
  const all = [];
  const walk = (n) => { all.push(n); n.elements.forEach(walk); };
  walk(root);
  assert.strictEqual(all.filter((n) => n.cls.includes("an-cmpnote")).length, 1);
  assert.match(all.find((n) => n.cls.includes("an-cmpnote")).innerHTML, /^Descriptive only\. Cross-model differences reflect the <b>task<\/b>/);
  assert.ok(root.textContent.includes("Where the money goes") && root.textContent.includes("Cost by project (top)"));
  assert.ok(all.some((n) => n.cls.includes("an-legend")) && !all.some((n) => /an-plegend|an-foot|an-hrow|an-pgrid|an-card/.test(n.className)), "none of the page's nodes");
});

test("cost by model / by workspace are two cards (§B7, §3.1)", () => {
  const env = analyticsEnv();
  env.page(true);
  overviewWindow(env);
  const root = view(env, "anOverview");
  const grid = root.elements[2];
  assert.strictEqual(grid.className, "an-pgrid");
  const [mc, wc] = grid.elements;
  assert.deepStrictEqual(grid.elements.map(sigOf), ["div.an-card", "div.an-card"]);
  const heads = (c) => c.elements[0].elements.map((n) => [sigOf(n), n.textContent]);
  assert.deepStrictEqual(heads(mc), [["h2.t-md w-600", "Cost by model"]], "the head holds only the title");
  assert.deepStrictEqual(heads(wc), [["h2.t-md w-600", "Cost by workspace"], ["span.t-sm muted", "top 8 · bar split = host / box"]]);
  assert.deepStrictEqual([mc, wc].map((c) => c.elements[1].className), ["an-card-body", "an-card-body"]);
  const rowsOf = (c) => c.elements[1].elements[0];
  assert.deepStrictEqual([mc, wc].map((c) => rowsOf(c).className), ["an-hrows", "an-hrows"]);

  // Model rows: a mono label, the track (the kit's .meter), a right-aligned tabular value and the model's share of the window; the
  // unpriced row is sans, muted, with an empty track.
  const mrows = rowsOf(mc).elements;
  assert.deepStrictEqual(mrows.map((r) => r.className), Array(4).fill("an-hrow an-pct"));
  assert.deepStrictEqual(mrows.map((r) => r.elements.map(sigOf)), [
    ["span.mono t-sm ellipsis", "span.meter", "span.t-sm tnum an-hv", "span.t-sm tnum muted an-hp"],
    ["span.mono t-sm ellipsis", "span.meter", "span.t-sm tnum an-hv", "span.t-sm tnum muted an-hp"],
    ["span.mono t-sm ellipsis", "span.meter", "span.t-sm tnum an-hv", "span.t-sm tnum muted an-hp"],
    ["span.t-sm muted ellipsis", "span.meter", "span.t-sm tnum an-hv muted", "span.t-sm tnum muted an-hp"],
  ]);
  assert.deepStrictEqual(mrows.map((r) => [r.elements[0].textContent, r.elements[2].textContent, r.elements[3].textContent]), [["opus-5-5", "$3.0k", "70%"], ["sonnet-5-5", "$1.3k", "30%"], ["haiku-5-5", "$2.05", "0%"], ["other (unpriced)", "$0.00", "0%"]]);
  assert.deepStrictEqual(mrows.map((r) => r.elements[0].title), ["opus-5-5", "sonnet-5-5", "haiku-5-5", "other (unpriced)"], "a long id ellipsizes, so its title keeps it whole");
  const fills = (row) => row.elements[1].elements.map((i) => ({ left: i.style.left, width: i.style.width, background: i.style.background, title: i.title }));
  assert.deepStrictEqual(mrows.map(fills).map((f) => f.map((x) => [x.left, x.background])), [[["0%", "var(--an-1)"]], [["0%", "var(--an-2)"]], [["0%", "var(--an-3)"]], []], "the unpriced row has an empty track");
  assert.strictEqual(fills(mrows[0])[0].width, "100%", "the dearest model is the full track");
  assert.strictEqual(fills(mrows[1])[0].width, 1300 / 3000 * 100 + "%");

  // Workspace rows: the window's workspaces (acme, then the unmatched old folder with no cost), the bar split host then box on the
  // --an-6 and --an-1 slots, scaled to the costliest workspace, with each one's share of the window.
  const wrows = rowsOf(wc).elements;
  assert.deepStrictEqual(wrows.map((r) => r.className), Array(2).fill("an-hrow an-wide an-pct"));
  assert.deepStrictEqual(wrows[0].elements.map(sigOf), ["span.mono t-sm ellipsis", "span.meter", "span.t-sm tnum an-hv", "span.t-sm tnum muted an-hp"]);
  const total = env.run("AN.data.costByDay.totals.cost");
  assert.deepStrictEqual(wrows.map((r) => [r.elements[0].textContent, r.elements[2].textContent, r.elements[3].textContent]), [["acme", "$100", Math.round(100 / total * 100) + "%"], ["old", "$0.00", "0%"]]);
  const acme = fills(wrows[0]);
  assert.deepStrictEqual(acme.map((f) => [f.background, f.title]), [["var(--an-6)", "host $31.00"], ["var(--an-1)", "box $69.00"]]);
  assert.deepStrictEqual(acme.map((f) => [f.left, f.width]), [["0%", "31%"], ["31%", "69%"]], "the second segment starts where the first ends; a share of the costliest workspace");
  assert.deepStrictEqual(fills(wrows[1]), [], "no cost, no fill");

  // The page draws the model bars as the phone does: the same names, values, widths and colours.
  const phone = analyticsEnv();
  phone.page(false);
  overviewWindow(phone);
  const hbars = [];
  const walk = (n) => { if (n.cls.includes("an-hbar")) hbars.push(n); n.elements.forEach(walk); };
  walk(view(phone, "anOverview"));
  hbars.slice(0, 4).forEach((h, k) => {
    assert.strictEqual(mrows[k].elements[0].textContent, h.elements[0].textContent, "name " + k);
    assert.strictEqual(mrows[k].elements[2].textContent, h.elements[2].textContent, "value " + k);
    assert.deepStrictEqual(fills(mrows[k]).map((f) => [f.width, f.background, f.title]), h.elements[1].elements.filter((f) => parseFloat(f.style.width) > 0).map((f) => [f.style.width, f.style.background, f.title]), "bars " + k);
  });
});

test("the Overview's rows, legend and footnote are drawn by the CSS the brief gives (§B7, §D1)", () => {
  // §B7: 150 | 1fr | 64 and 184 | 1fr | 64, 22px rows 12px apart, no row separators; the project card is the wide one.
  const row = decls(ANP + " .an-hrow");
  assert.strictEqual(row.get("display"), "grid");
  assert.strictEqual(row.get("grid-template-columns"), "150px minmax(0, 1fr) 64px");
  assert.strictEqual(row.get("column-gap"), "var(--sp-3)");
  assert.strictEqual(row.get("height"), "22px");
  assert.strictEqual(row.get("align-items"), "center");
  assert.strictEqual(decls(ANP + " .an-hrow.an-wide").get("grid-template-columns"), "184px minmax(0, 1fr) 64px");
  assert.strictEqual(decls(ANP + " .an-hrows").get("display"), "flex");
  assert.strictEqual(decls(ANP + " .an-hrows").get("flex-direction"), "column");
  assert.strictEqual(decls(ANP + " .an-hv").get("text-align"), "right", "values right-aligned (tabular comes from .tnum)");
  assert.strictEqual(decls(".tnum").get("font-variant-numeric"), "tabular-nums");
  // The track is the kit's .meter: 4px, radius 2, on --line; nothing on the page changes it.
  const meter = decls(".meter");
  assert.strictEqual(meter.get("height"), "4px");
  assert.strictEqual(meter.get("border-radius"), "2px");
  assert.strictEqual(meter.get("background"), "var(--line)");
  assert.ok(!RULES.some((r) => parts(r.selector).some((s) => s.startsWith(ANP) && /\.meter/.test(s) && /\.an-hrow/.test(s))), "no page rule re-colours or re-sizes the track");
  // §D1: the legend is 16px apart, an item a swatch and a label 6px apart; the swatch is an 8px square with no radius.
  const lg = decls(ANP + " .an-plegend");
  assert.strictEqual(lg.get("display"), "flex");
  assert.strictEqual(lg.get("align-items"), "center");
  assert.strictEqual(lg.get("flex-wrap"), "wrap", "a narrow head wraps the legend instead of squeezing the title");
  assert.strictEqual(lg.get("gap"), "var(--sp-1) var(--sp-4)");
  const item = decls(ANP + " .an-plegend > span");
  assert.strictEqual(item.get("display"), "inline-flex");
  assert.strictEqual(item.get("gap"), "6px");
  const sw = decls(ANP + " .an-sw");
  assert.strictEqual(sw.get("width"), "8px");
  assert.strictEqual(sw.get("height"), "8px");
  assert.strictEqual(sw.get("border-radius"), undefined, "no radius: the frame's swatches are square");
  assert.strictEqual(sw.get("flex"), "none");
  // The card body under the head is §D1's .m3-cardbody (12px 16px 16px): the svg carries 8px of its own above the plot for the top
  // label, so the body gives 4px.
  assert.strictEqual(decls(ANP + " .an-chartbody").get("padding-top"), "var(--sp-1)");
  // The footnote is flat: no box, no border, no padding; the section's 8px gap sets it off.
  const foot = decls(ANP + " .an-foot");
  assert.strictEqual(foot.get("margin"), "0");
  for (const k of ["border", "border-left", "padding", "background"]) assert.strictEqual(foot.get(k), undefined, k);
  // Every size a rule of this task sets is a token (the page's px-sized text is answered in "no off-scale analytics text on desktop"),
  // and none of them uses color-mix, nesting or @layer.
  const STEP = /^var\(--t-(?:xs|sm|md|lg|xl|kpi)\)$/;
  for (const r of RULES) {
    if (!parts(r.selector).some((s) => s.startsWith(ANP) && /\.an-(?:hrow|hrows|hv|plegend|sw|foot|chartbody|axis|hbar|legend|kdelta|divrow|mixlabel)/.test(s))) continue;
    for (const [k, v] of r.decls) {
      if (k === "font-size") assert.match(v, STEP, r.selector);
      assert.ok(!/color-mix/.test(v), r.selector + " uses color-mix");
    }
  }
});

// =====================================================================================================================
// Gate fix wave: the narrow window (Gate 3 D2, D3) and the kit's 20px line (Gate 4 L2, L3)
// =====================================================================================================================

// D2: at 701-760px five tiles were 68-80px wide, their labels cut to five characters and the last Analytics value ($44.59, 77px)
// spilling out of its tile.
test("a KPI row too narrow for 120px tiles wraps onto more rows, on both pages, with a hairline at every row start", () => {
  // Wide: the kit's one row of equal columns, untouched.
  const kit = decls(".kpi-row");
  assert.strictEqual(kit.get("grid-auto-flow"), "column");
  assert.strictEqual(kit.get("grid-auto-columns"), "minmax(0, 1fr)");
  const WRAP = "@container (max-width: 599px)";
  const wrap = CONTAINER_RULES.filter((r) => r.at.includes(WRAP));
  assert.strictEqual(wrap.length, 2, "one rule for the row, one for its tiles");
  const [row, tile] = wrap;
  assert.deepStrictEqual(parts(row.selector), [AU + " .kpi-row", ANP + " .kpi-row"], "one rule for both pages (the shared list)");
  assert.deepStrictEqual(parts(tile.selector), [AU + " .kpi-row > .kpi", ANP + " .kpi-row > .kpi"]);
  assert.strictEqual(row.decls.get("grid-auto-flow"), "row");
  assert.strictEqual(row.decls.get("grid-template-columns"), "repeat(auto-fill, minmax(120px, 1fr))", "a tile is never under 120px: its 22px value and 16px sides need about that");
  assert.strictEqual(row.decls.get("overflow"), "hidden", "the row's own edge hides the hairlines that sit on it, and keeps its 8px corners");
  // Five tiles of 120px want 600px: the query starts one pixel under that.
  // A left and a top hairline on every tile, drawn one pixel outside its cell, so a row start has no stray line and every row break has one.
  assert.strictEqual(tile.decls.get("margin"), "-1px 0 0 -1px");
  assert.strictEqual(tile.decls.get("box-shadow"), "inset 1px 0 0 var(--line), inset 0 1px 0 var(--line)");
  assert.ok(![...row.decls.values(), ...tile.decls.values()].some((v) => /color-mix/.test(v)));
  // The query is answered by the container the row sits in: Analytics' body (which a container already), Audit's scroller.
  assert.strictEqual(decls(ANP + " #analytics-body").get("container-type"), "inline-size");
  assert.strictEqual(decls(AU + " > .au-scroll").get("container-type"), "inline-size");
  assert.strictEqual(decls(AU + " > .au-scroll").get("flex"), "1 1 0", "making it a container leaves it the scroller it was");
  // The labels still ellipsize with their full text in title (the tiles are narrower, not taller).
  assert.strictEqual(decls(AU + " .kpi-label").get("text-overflow"), "ellipsis");
  assert.strictEqual(decls(ANP + " .kpi-label").get("white-space"), "nowrap");
  // Outside a container there is no wrap rule: the kit's row, tiles and hairline are the wide layout.
  assert.ok(!BASE_RULES.some((r) => parts(r.selector).some((x) => x.startsWith(AU + " .kpi-row") || x.startsWith(ANP + " .kpi-row"))), "the wrap lives in the query alone");
  assert.strictEqual(decls(".kpi + .kpi").get("box-shadow"), "inset 1px 0 0 var(--line)");
});

// D3: "Cost over time" broke to one to three characters a line when the legend was long (701, 760, 1280 with eight models).
test("a card head keeps its title and caption whole and wraps the legend onto a line of its own", () => {
  assert.strictEqual(decls(ANP + " .an-card .card-head").get("flex-wrap"), "wrap", "the head wraps");
  const keep = decls(ANP + " .an-card .card-head > :is(h2, h3, .muted)");
  assert.strictEqual(keep.get("flex"), "none", "the title and its caption never shrink");
  assert.strictEqual(keep.get("white-space"), "nowrap", "or wrap");
  const title = decls(ANP + " .an-card .card-head > :is(h2, h3)");
  assert.strictEqual(title.get("margin"), "0");
  assert.strictEqual(title.get("overflow-wrap"), undefined, "no `anywhere` to break a title a character at a time");
  assert.strictEqual(title.get("min-width"), undefined);
  // The legend wraps its own items, with a row gap, instead of pushing the title: it is the one child that may shrink.
  const lg = decls(ANP + " .an-plegend");
  assert.strictEqual(lg.get("flex-wrap"), "wrap");
  assert.strictEqual(lg.get("gap"), "var(--sp-1) var(--sp-4)");
  assert.strictEqual(lg.get("min-width"), "0");
  assert.strictEqual(lg.get("flex"), undefined, "so it takes its own width and the head wraps it when that does not fit beside the title");
  // The head's own children: title, caption, spacer, legend (the spacer pushes the legend right when they share a row).
  const env = analyticsEnv();
  env.page(true);
  overviewWindow(env);
  const head = view(env, "anOverview").elements[1].elements[0].elements[0];
  assert.deepStrictEqual(head.elements.map(sigOf), ["h2.t-md w-600", "span.t-sm muted", "span.grow", "div.an-plegend"]);
});

// L2 and L3 (Gate 4): the frame's seg labels and "Esc" hint are on the kit's 20px line; the kit's `.seg button` reset the first to
// `normal` and the hint's own font rule left the second there, so both drew one pixel low.
test("the kit's seg items and Esc hint sit on the 20px line", () => {
  assert.strictEqual(decls(".seg > .seg-item").get("line-height"), "var(--lh-md)");
  assert.strictEqual(decls(".seg > .seg-item").get("height"), "var(--h-sm)", "the 24px item the 20px line is centred in is unchanged");
  assert.strictEqual(decls(".btn .kbd-hint").get("line-height"), "var(--lh-md)");
  assert.strictEqual(decls(".btn .kbd-hint").get("font-size"), "var(--t-xs)", "the hint's 11px mono is unchanged");
  assert.strictEqual(decls(":root").get("--lh-md"), "20px");
  // Both are kit rules: inside the fence, with no page prefix and no hex.
  const raw = read("desktop.css");
  const fence = raw.slice(raw.indexOf("/* === kit primitives"), raw.indexOf("/* === end kit primitives === */"));
  for (const sel of [".seg > .seg-item {", ".btn .kbd-hint {"]) assert.ok(fence.includes(sel), sel + " is in the kit fence");
  const inFence = (sel) => parseRules(stripComments(fence)).find((r) => parts(r.selector).includes(sel));
  for (const sel of [".seg > .seg-item", ".btn .kbd-hint"]) assert.ok(![...inFence(sel).decls.values()].some((v) => /#[0-9a-f]{3,8}\b/i.test(v)), sel + " has no hex");
});

// B1 (Gate 4 of plan 6, #176): Scan, Elevate…, Extend, Drop now and Load older drew one pixel below the frame's. primitives.css's
// legacy `.btn { font: 400 12px ... }` resets line-height to `normal`, so a label sat on the font's own line, not the frame's 20px
// one. A kit-wide `.btn { line-height: var(--lh-md) }` was tried and reversed: it grew some Settings buttons (the Credentials
// tab's padded .btn, "+ new profile", a wrapping .btn), and Settings is not this plan's. The line is set on Plan 6's own surfaces
// only: the banner (.elev .btn), the Activity view (.act-wrap, .act-plain) and the Access view (.ac-wrap). The kit .btn is as it was.
const VIEW_BTNS = [".elev .btn", ".ac-wrap .btn", ".act-wrap .btn", ".act-plain .btn"];
test("only the banner, Activity and Access buttons sit on the 20px line; the kit .btn and every other page keep theirs", () => {
  for (const sel of VIEW_BTNS) assert.strictEqual(decls(sel).get("line-height"), "var(--lh-md)", sel + " is on the 20px line");
  // The kit .btn (and its sizes) set no line: that is what moved Settings' boxes.
  for (const sel of [".btn", ".btn-sm", ".btn-lg", ".btn-icon", ".btn-primary", ".btn-ghost", ".btn-danger", ".btn-yes"]) {
    assert.strictEqual(decls(sel).get("line-height"), undefined, sel + " sets no line of its own");
  }
  const raw = read("desktop.css");
  const fence = raw.slice(raw.indexOf("/* === kit primitives"), raw.indexOf("/* === end kit primitives === */"));
  const kit = parseRules(stripComments(fence)).find((r) => parts(r.selector).includes(".btn"));
  assert.ok(kit && !kit.decls.has("line-height"), "the kit .btn rule, inside the fence, has no line-height");
  assert.strictEqual(kit.decls.get("min-height"), "var(--h-ctl)");
  assert.strictEqual(decls(".btn-sm").get("min-height"), "var(--h-sm)");
  // No rule in the whole sheet that is a bare .btn selector (any kit button, on any page) sets a line.
  const bare = BASE_RULES.filter((r) => r.decls.has("line-height") && parts(r.selector).some((x) => /^\.btn[\w.:()-]*$/.test(x)));
  assert.deepStrictEqual(bare.map((r) => r.selector), [], "no unscoped .btn rule sets line-height");
  // Every other rule that gives a .btn a line is one of the view's own, or an older context's that already set it itself.
  const scoped = BASE_RULES.filter((r) => r.decls.has("line-height") && parts(r.selector).some((x) => /(^|[\s>])\.btn[\w.:()\[\]="-]*$/.test(x))).flatMap((r) => parts(r.selector).filter((x) => /\.btn/.test(x)));
  for (const sel of scoped) {
    assert.ok(VIEW_BTNS.includes(sel) || /^(\.empst \.btn|dialog:not\(\.as-page\):not\(\[box-\]\):not\(\.sheet-dialog\) (\.dhd )?\.btn(\.gh)?|#store-banner \.btn|#bug-modal \.dhd \.btn\.gh\[data-close="bug-modal"\]|\.btn \.kbd-hint)$/.test(sel), "unexpected .btn line rule: " + sel);
  }
  // The root rules are in the views' own blocks: inside the fence only the banner's, which is the kit sheet's own addition.
  assert.ok(fence.includes(".elev .btn {") && !fence.includes(".ac-wrap") && !fence.includes(".act-wrap"));
  assert.ok(![...VIEW_BTNS].some((sel) => [...decls(sel).values()].some((v) => /#[0-9a-f]{3,8}\b/i.test(v))), "no hex");
});

// A scoped button cannot change box height: the 20px line plus the two 1px borders is 22px, under the 24px (.btn-sm) and 28px
// (.btn) floors, and no rule inside the four roots gives a .btn vertical padding, a height or a second line. Drop now, Scan, Load
// older, Elevate… and Extend are .btn-sm; Cancel, Grant, Enable replies, the reply's send and the SSO card's buttons are .btn.
test("the scoped buttons keep their boxes: 22px of line and border under the 24px and 28px floors, nothing else sized inside the roots", () => {
  const LH = 20;
  const border = decls(".btn").get("border");
  assert.strictEqual(border, "1px solid transparent");
  assert.ok(LH + 2 <= 24, "a .btn-sm is 24px with the 20px line");
  assert.ok(LH + 2 <= 28, "a .btn is 28px with the 20px line");
  assert.strictEqual(decls(":root").get("--lh-md"), LH + "px");
  assert.strictEqual(decls(".btn").get("padding"), "0 12px", "no vertical padding on the kit .btn");
  assert.strictEqual(decls(".btn-sm").get("padding"), "0 8px", "or the small one");
  assert.strictEqual(decls(".btn").get("white-space"), "nowrap", "a label never takes a second line");
  const roots = [".elev ", ".ac-wrap ", ".act-wrap ", ".act-plain "];
  const inside = BASE_RULES.flatMap((r) => parts(r.selector).filter((x) => roots.some((q) => x.startsWith(q)) && /\.btn\b/.test(x)).map((x) => [x, r]));
  assert.ok(inside.length >= 4, "the four scoped rules are among them");
  for (const [sel, r] of inside) {
    const props = [...r.decls.keys()].filter((k) => !["flex", "line-height"].includes(k));
    assert.deepStrictEqual(props, [], sel + " sets only the line (and the banner's flex): no padding, height or wrap");
  }
});

// =====================================================================================================================
// Analytics v3, Task 10: the page's head, Where, the Overview's changes, Sessions that open, the Models table
// =====================================================================================================================

test("anKpiRow: a tile's change line carries the figure in its direction's class and the window it is against, as text", () => {
  const aenv = analyticsEnv();
  aenv.page(true);
  const row = aenv.run(`anKpiRow([
    { value: "$4.3k", label: "API value", delta: { text: "▲ 12% vs the 30 days before", dir: "up" } },
    { value: "$0.84", label: "Median per session", delta: { text: "▼ 3%", dir: "down" } },
    { value: "96.1%", label: "Cache hit", delta: { text: "0.0 pt", dir: "flat" } },
    { value: "1", label: "Plain", delta: null },
  ])`);
  const [first, second, third, plain] = row.elements;
  const delta = (tile) => tile.elements.find((c) => /an-kdelta/.test(c.className));
  assert.strictEqual(text(delta(first)), "▲ 12% vs the 30 days before");
  assert.deepStrictEqual(delta(first).elements.map((c) => [c.className, c.textContent]), [["an-dlead an-dup", "▲ 12%"]]);
  assert.strictEqual(text(delta(second)), "▼ 3%");
  assert.deepStrictEqual(delta(second).elements.map((c) => c.className), ["an-dlead an-ddown"]);
  assert.strictEqual(text(delta(third)), "0.0 pt");
  assert.deepStrictEqual(delta(third).elements.map((c) => c.className), ["an-dlead an-dflat"]);
  assert.strictEqual(delta(plain), undefined, "no Compare, no change line");
  assert.ok(!/undefined/.test(JSON.stringify(row.elements.map((t) => t.elements.map((c) => c.className)))), "no stale cls from the removed anDelta");
});

test("the Where select: its options are the API's with their window value; a change filters every view, reloads and keeps focus", async () => {
  const env = analyticsEnv();
  env.page(true);
  const { run, el, calls, ctx } = env;
  run("anWirePage();");
  run('AN.from = "2026-10-09"; AN.to = "2026-11-07";');
  const sel = el("an-where");
  // The API answers /where; a load paints the options into the one static select.
  ctx.api = (path) => {
    calls.push(path);
    const q = /from=([\d-]+)&to=([\d-]+)/.exec(path);
    if (path.startsWith("/api/analytics/where")) return Promise.resolve({ options: [{ key: "", label: "All", costUsd: 4300 }, { key: "host", label: "Host", costUsd: 1300 }, { key: "box:prd", label: "Box · prd", costUsd: 2100 }] });
    return Promise.resolve({ from: q[1], to: q[2] });
  };
  await run("anLoad()");
  assert.deepStrictEqual(sel.elements.map((o) => [o.value, o.textContent]), [["", "All · $4.3k"], ["host", "Host · $1.3k"], ["box:prd", "Box · prd · $2.1k"]]);
  assert.ok(sel.elements.every((o) => o.children.length === 1 && o.children[0] instanceof TextNode), "option text is text, not markup");
  const same = [...sel.elements];
  await run("anLoad()");
  assert.ok(sel.elements.every((o, i) => o === same[i]), "options that did not change are not rebuilt");

  // Choosing Box · prd: AN.where, a reload with it on every view's request, focus still on the select.
  calls.length = 0;
  sel.focus();
  sel.value = "box:prd";
  sel.onchange();
  assert.strictEqual(run("AN.where"), "box:prd");
  await settle();
  assert.strictEqual(calls.filter((c) => c.endsWith("&where=box%3Aprd")).length, 5, "summary, models, workspaces, session-costs and drivers carry it");
  assert.strictEqual(run("document.activeElement"), sel, "focus stays on the select");
  assert.strictEqual(sel.value, "box:prd");
  assert.strictEqual(el("an-where"), sel, "the same node: never rebuilt");

  // A profile the narrower window no longer lists is still a choice, so the select never says All over a filtered view.
  ctx.api = (path) => (path.startsWith("/api/analytics/where") ? Promise.resolve({ options: [{ key: "", label: "All", costUsd: 5 }] }) : Promise.resolve({}));
  await run("anLoad()");
  assert.deepStrictEqual(sel.elements.map((o) => [o.value, o.textContent]), [["", "All · $5.00"], ["box:prd", "Box · prd · $0.00"]]);
  assert.strictEqual(sel.value, "box:prd");

  // A hostile label is text: nothing in the select is markup.
  ctx.api = (path) => (path.startsWith("/api/analytics/where") ? Promise.resolve({ options: [{ key: "box:<img src=x onerror=1>", label: "Box · <img src=x onerror=1>", costUsd: 1 }] }) : Promise.resolve({}));
  await run("anLoad()");
  assert.ok(sel.elements.some((o) => o.textContent === "Box · <img src=x onerror=1> · $1.00"));
  assert.ok(sel.elements.every((o) => o.elements.length === 0), "no element is made from a label");
});

test("openAnalytics puts the Where select back to All", async () => {
  const { run, el, page } = analyticsEnv();
  page(true);
  run('AN.where = "host"; AN.whereOpts = [{ key: "", label: "All", costUsd: 1 }, { key: "host", label: "Host", costUsd: 1 }]; anSyncPageControls();');
  assert.strictEqual(el("an-where").value, "host");
  await run("openAnalytics()");
  assert.strictEqual(run("AN.where"), "");
  assert.strictEqual(el("an-where").value, "");
});

test("a saved projects or tools tab opens Workspaces or Cost drivers; the phone's strip follows", () => {
  const { run, el, page } = analyticsEnv();
  page(true);
  run("anSetTab('projects')");
  assert.strictEqual(run("AN.tab"), "workspaces");
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["false", "true", "false", "false", "false"]);
  assert.deepStrictEqual(el("an-tabs").elements.map((t) => t.cls.includes("act")), [false, false, true, false, false], "the phone's Projects item is lit for Workspaces");
  run("anSetTab('tools')");
  assert.strictEqual(run("AN.tab"), "drivers");
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["false", "false", "true", "false", "false"]);
  assert.deepStrictEqual(el("an-tabs").elements.map((t) => t.cls.includes("act")), [false, false, false, false, true]);
  run("anSetTab('nonsense')");
  assert.strictEqual(run("AN.tab"), "overview");
});

test("Workspaces and Cost drivers still draw through a builder until their own views come", async () => {
  const env = analyticsEnv();
  env.page(true);
  loadWindow(env);
  for (const tab of ["workspaces", "drivers"]) {
    env.run(`AN.tab = ${JSON.stringify(tab)};`);
    env.run("anRender();");
    assert.ok(env.el("analytics-body").children.length >= 1, tab + " draws something");
  }
});

test("the head, the Where select and the tile changes have the CSS they need, on the page only", () => {
  const wh = decls(ANP + " .an-where");
  assert.strictEqual(wh.get("height"), "var(--h-ctl)", "the seg's 28px");
  assert.strictEqual(wh.get("display"), "inline-flex");
  assert.strictEqual(decls(ANP + " .an-wsel").get("border"), "0");
  assert.ok(RULES.some((r) => parts(r.selector).includes(ANP + " .an-where:has(.an-wsel:focus-visible)")), "the control shows keyboard focus");
  // Palette hues, never red or green: --an-up / --an-down are desktop.css's blue and grey.
  assert.strictEqual(decls(ANP + " .an-dup").get("color"), "var(--an-up)");
  assert.strictEqual(decls(ANP + " .an-ddown").get("color"), "var(--an-down)");
  assert.strictEqual(decls(ANP + " .an-dflat").get("color"), "var(--muted)");
  assert.strictEqual(decls(ANP + " .an-wbox").get("color"), "var(--an-1)", "box is the --an-1 slot, like its bars");
  // The phone never sees the page's head: app.css hides the toolbar that holds the select.
  assert.match(APP_CSS, /#analytics-modal \.an-phead, #analytics-modal \.an-ptools, #analytics-modal \.an-pcustom \{ display: none; \}/);
  for (const r of RULES) for (const [prop, v] of r.decls) {
    if (!parts(r.selector).some((x) => /\.an-(?:where|wsel|dlead|dup|ddown|dflat|wbox|wkey|hp|live|ell)/.test(x))) continue;
    assert.ok(!/color-mix|var\(--(?:red|amber|ok|warn|danger)/i.test(v), r.selector + " " + prop + " uses no status colour and no color-mix");
  }
});
