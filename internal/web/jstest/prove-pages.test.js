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
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
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
