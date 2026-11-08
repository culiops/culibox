"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

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
  assert.deepStrictEqual(kids(esc).map(sig), ["span.kbd-hint"]);
  assert.strictEqual(text(kids(esc)[0]), "Esc");
  assert.strictEqual(text(esc), "Esc✕", "the Esc hint, then the ✕ (U+2715) as plain text");
  assert.ok(esc.children.some((c) => c.text && c.text.includes("✕")));

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
// Audit dialog markup. `run` evaluates a snippet in it (the scripts' top-level consts are visible to it).
function auditEnv() {
  const dlg = fromParsed(auditTree());
  const document = {
    getElementById: (id) => findId(dlg, id),
    querySelectorAll: (sel) => queryAll(dlg, sel),
    createElement: (tag) => new El(tag),
    createTextNode: (t) => new TextNode(t),
  };
  const ctx = { document, window: { innerWidth: 1440 }, lastSessions: [], console, AbortController, fetch: async () => ({}) };
  vm.createContext(ctx);
  for (const f of ["accessmodel.js", "auditmodel.js", "auditpage.js"]) new vm.Script(read(f), { filename: f }).runInContext(ctx);
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

  // An elevation that opened nothing is a plain row: no tint, a muted Kind. A refused one keeps its red Outcome
  // (the tones are Plan 6's), a dropped one is muted.
  for (const outcome of ["dropped", "expired", "ended"]) {
    const tr = row({ ...base, outcome, actor: "you" });
    assert.strictEqual(tr.className, "", outcome + ": no tint");
    assert.deepStrictEqual(cells(tr).map((x) => x[0]), ["au-time", "au-sess", "au-kind", "au-what", "au-out"], outcome);
  }
  const refused = row({ ...base, outcome: "refused", actor: "you" });
  assert.strictEqual(refused.className, "");
  assert.deepStrictEqual(cells(refused).map((x) => x[0]), ["au-time", "au-sess", "au-kind", "au-what", "au-out au-bad"], "refused: not tinted, Outcome still red until Plan 6");

  // Other rows: the Outcome keeps its tone class from auditRowView.
  const minted = row({ ts: 1_760_000_000, kind: "credential", what: "aws sandbox", outcome: "minted" });
  assert.strictEqual(minted.className, "");
  assert.deepStrictEqual(cells(minted).map((x) => x[0]), ["au-time", "au-sess", "au-kind", "au-what", "au-out au-ok"]);
  const denied = row({ ts: 1_760_000_000, kind: "egress", what: "pypi.org:443", outcome: "denied", count: 3 });
  assert.strictEqual(cells(denied)[4][0], "au-out au-bad");
  assert.strictEqual(cells(denied)[4][1], "denied ×3");
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
  const heads = RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(AU) && /\bth\b/.test(s) && !/nth-child/.test(s)));
  assert.deepStrictEqual(heads.map((r) => r.selector), []);
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
  // One @container, on the card, below the fixed columns plus ~240px for What.
  const queries = [...new Set(CONTAINER_RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(AU + " .au-tbl"))).flatMap((r) => r.at.filter((a) => a.startsWith("@container"))))];
  assert.deepStrictEqual(queries, ["@container au-card (max-width: 769px)"]);
  const max = Number(/max-width:\s*(\d+)px/.exec(queries[0])[1]);
  assert.ok(max + 1 >= fixed + 240, "What keeps about 240px (" + (max + 1 - fixed) + "px)");
  assert.ok(max + 1 <= fixed + 260, "and the fixed layout is not given up sooner than that");
  const inside = (sel) => decls(sel, CONTAINER_RULES);
  assert.strictEqual(inside(AU + " .au-tbl").get("table-layout"), "auto");
  for (const i of [1, 2, 3, 5]) assert.strictEqual(inside(AU + " .au-tbl th:nth-child(" + i + ")").get("width"), "auto", "th " + i);
  // Nothing else changes in the narrow card: What stays the one column with no width of its own, and the wrap cells keep
  // wrapping (the base rules above are not repeated or undone here).
  assert.deepStrictEqual(CONTAINER_RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(AU))).flatMap((r) => [...r.decls.keys()]).sort(),
    ["table-layout", "width"]);
  assert.strictEqual(decls(AU + " .au-tbl td.au-what").get("white-space"), "normal");
  assert.strictEqual(decls(AU + " .au-tbl td.au-what").get("overflow-wrap"), "anywhere");
  // The base rules come first: a container rule has the same weight as the base one it undoes, so the later one wins.
  const src = stripComments(read("desktop.css"));
  assert.ok(src.indexOf("@container au-card") > src.indexOf(AU + " .au-tbl th:nth-child(5) { width: 150px; }"));
});

test("What and Session wrap, never clip: normal white-space, anywhere, 20px lines, 6px above and below", () => {
  for (const cell of ["au-sess", "au-what", "au-out"]) {
    const c = decls(AU + " .au-tbl td." + cell);
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
  assert.deepStrictEqual(kids(kids(anActions).pop()).map(shape), kids(kids(auActions).pop()).map(shape), "both Esc ✕ are span.kbd-hint then ✕");
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
  // The KPI row. Audit's builder emits the kit's kpi-row > kpi > kpi-value + kpi-label; Analytics' joins this check with
  // its own KPI row in Task 5 (anKpiRow), which this task does not touch.
  const { run, el } = auditEnv();
  run("auData.summary = { issued: 1, refused: 0, elevations: 0, egress_allowed: 0, egress_denied: 0 }; auPaintKpis();");
  assert.deepStrictEqual(classes(kids(kids(au)[2])[0]), ["kpi-row"], "the KPI tiles' row is the kit's .kpi-row");
  assert.deepStrictEqual([...new Set(el("au-kpis").elements.map((t) => t.className))], ["kpi"]);
  assert.deepStrictEqual([...new Set(el("au-kpis").elements.flatMap((t) => t.elements.map((c) => c.className)))], ["kpi-value", "kpi-label"]);
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
  assert.deepStrictEqual(kids(esc).map(sig), ["span.kbd-hint"]);
  assert.strictEqual(text(esc), "Esc✕");

  // The toolbar: View seg | spacer | Range seg | Compare (§B2).
  assert.deepStrictEqual(kids(tools).map(sig), ["div.seg", "span.grow", "div.seg", "button.btn"]);
  const [view, , range, compare] = kids(tools);
  assert.strictEqual(view.attrs.id, "an-pview");
  assert.strictEqual(view.attrs.role, "group");
  assert.strictEqual(view.attrs["aria-label"], "View");
  assert.deepStrictEqual(kids(view).map((b) => b.attrs["data-antab"]), ["overview", "models", "projects", "sessions", "tools"]);
  assert.deepStrictEqual(kids(view).map(text), ["Overview · Cost", "Models", "Projects", "Sessions", "Tools"], "the words of today's tabs");
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
  assert.strictEqual(el("an-pdesc").textContent, "Spend read from Claude Code transcripts · 2026-10-09 → 2026-11-07 (UTC)", "§A1, from AN before a window has loaded");
  same();

  run('AN.tab = "sessions"; AN.range = "90d"; AN.compare = true; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["false", "false", "false", "true", "false"]);
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "false", "true", "false", "false"]);
  assert.strictEqual(el("an-compare").attrs["aria-pressed"], "true");
  same();

  run('AN.range = "all"; anSyncPageControls();');
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "false", "false", "true", "false"]);
  run('AN.tab = "tools"; AN.range = "7d"; AN.compare = false; anSyncPageControls();');
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

  // The description follows the window that loaded, like the page-only append it replaces (the summary's own from and to).
  run('AN.data = { from: "2026-10-01", to: "2026-10-30", costByDay: { days: [] } }; anSyncPageControls();');
  assert.strictEqual(el("an-pdesc").textContent, "Spend read from Claude Code transcripts · 2026-10-01 → 2026-10-30 (UTC)");
  same();
  // Nothing is ever built or removed by the sync: the head and the toolbar keep their children.
  assert.strictEqual(nodes.head.elements.length, 2);
  assert.strictEqual(nodes.tools.elements.length, 4);
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

test("a phone modal's page nodes are never touched: anSyncPageControls is a no-op unless the dialog is the page", () => {
  const { run, el, page } = analyticsEnv();
  page(false);
  run('AN.tab = "tools"; AN.range = "7d"; AN.compare = true; AN.customOpen = true; AN.from = "2026-10-24"; AN.to = "2026-10-30"; anSyncPageControls();');
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
  assert.ok(calls.includes("/api/analytics/summary?from=2026-10-05&to=2026-11-01"));
  await settle();
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "false", "false", "false", "true"]);
  assert.strictEqual(el("an-papply").parent.hidden, false, "a custom range keeps its row open");
  assert.strictEqual(run("document.activeElement"), el("an-papply"), "Apply keeps focus");
  assert.strictEqual(el("an-pdesc").textContent, "Spend read from Claude Code transcripts · 2026-10-05 → 2026-11-01 (UTC)");
  // A preset closes the row again; reopened, the fields show the window then.
  const seven = el("an-prange").elements[0];
  el("an-prange").onclick({ target: seven });
  assert.strictEqual(el("an-papply").parent.hidden, true);
  await settle();
  el("an-prange").onclick({ target: el("an-prange").elements[4] });
  assert.deepStrictEqual([from.value, to.value], [run("AN.from"), run("AN.to")]);
});

test("openAnalytics resets the page's controls with the state it resets", async () => {
  const { run, el, page } = analyticsEnv();
  page(true);
  run('AN.tab = "tools"; AN.range = "custom"; AN.compare = true; AN.customOpen = true; anSyncPageControls();');
  await run("openAnalytics()");
  assert.deepStrictEqual(pressedOf(el("an-pview")), ["true", "false", "false", "false", "false"]);
  assert.deepStrictEqual(pressedOf(el("an-prange")), ["false", "true", "false", "false", "false"]);
  assert.strictEqual(el("an-compare").attrs["aria-pressed"], "false");
  assert.strictEqual(el("an-papply").parent.hidden, true);
  assert.match(el("an-pdesc").textContent, /^Spend read from Claude Code transcripts · \d{4}-\d\d-\d\d → \d{4}-\d\d-\d\d \(UTC\)$/);
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
  const tools = BASE_RULES.filter((r) => parts(r.selector).includes(AU + " > .toolbar.au-tools"));
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
