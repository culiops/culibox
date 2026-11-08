"use strict";
// tokens.js on the API tokens page (UI plan 4, #174, Task 7; frame M2.4, digest-design §A7 and §B5): a kit table in a
// card, Name · Created · Last used and a Revoke… on every row, the approved footnote under the card. The phone's rows
// are untouched ("used never", the in-row confirm), so both skins are pinned here, against a minimal fake DOM (the
// repo has no jsdom, see credentials-dom.test.js) and, for the look, against the rules desktop.css carries.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { tokWhen, tokLastUsed, tokCreated } = require("../static/tokens.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const SRC = read("tokens.js");

const CREATED = 1793404800;   // 2026-10-31
const USED = 1793923200;      // 2026-11-06
const ARM_COPY = "Revoke? That device is signed out on its next request. This cannot be undone.";
const FOOTNOTE = "A token is shown once, when you create it. Revoking one signs that device out on its next request.";

// --- the dates -----------------------------------------------------------------------------------------------

test("Last used says Never used for a token that has not connected", () => {
  assert.strictEqual(tokLastUsed({}), "Never used");
  assert.strictEqual(tokLastUsed({ last_used_at: 0 }), "Never used");
  assert.strictEqual(tokLastUsed({ last_used_at: null }), "Never used");
  assert.strictEqual(tokLastUsed({ last_used_at: 1793404800 }), tokWhen(1793404800));
  assert.notStrictEqual(tokLastUsed({ last_used_at: 1793404800 }), "Never used");
});

test("Created is the day the token was made, and empty when the list sent none: never invented", () => {
  assert.strictEqual(tokCreated({ created_at: 1793404800 }), tokWhen(1793404800));
  assert.strictEqual(tokCreated({}), "");
  assert.strictEqual(tokCreated({ created_at: 0 }), "");
  assert.strictEqual(tokCreated({ created_at: null }), "");
});

test("a date is the day, in the unix seconds the API sends", () => {
  // internal/auth/tokens.go stamps time.Now().Unix(), so the value is seconds, and tokWhen scales it to ms.
  assert.match(tokWhen(CREATED), /2026/);
  assert.strictEqual(tokWhen(CREATED), new Date(CREATED * 1000).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }));
});

test("the phone row still says used never", () => {
  assert.strictEqual(tokWhen(0), "never");
  assert.strictEqual(tokWhen(undefined), "never");
  assert.match(SRC, /meta\.textContent = "used " \+ tokWhen\(t\.last_used_at\);/, "the phone's meta line is built as before");
});

// --- a fake DOM ----------------------------------------------------------------------------------------------

class Node {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.hidden = false; this.title = ""; this.colSpan = 1;
    this.type = ""; this.onclick = null; this.id = "";
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  appendChild(c) { if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  has(cls) { return this.className.split(/\s+/).includes(cls); }
  get classList() { return { contains: (c) => this.has(c) }; }
  walk(pred, out = []) { for (const c of this.children) { if (pred(c)) out.push(c); c.walk(pred, out); } return out; }
  byTag(tag) { return this.walk((n) => n.tag === tag); }
  byClass(cls) { return this.walk((n) => n.has(cls)); }
}

const settle = () => new Promise((r) => setImmediate(r));

// page({ items, asPage }): tokens.js in a context whose document is the Settings dialog, the page (as-page) or a
// phone's modal. `calls` records the requests the code makes.
function page({ items, asPage = true, armed = false } = {}) {
  const calls = [];
  const els = {
    "settings-modal": Object.assign(new Node("dialog"), { className: asPage ? "as-page" : "" }),
    "tok-list": new Node("ul"),
    "tok-err": new Node("div"),
    "tok-reveal": Object.assign(new Node("div"), { hidden: true }),
  };
  const ctx = {
    document: {
      readyState: "complete", addEventListener() {},
      getElementById: (id) => els[id] || null,
      createElement: (tag) => new Node(tag),
    },
    api: async (url, opts) => { calls.push([url, (opts && opts.method) || "GET"]); return items; },
    console,
  };
  vm.createContext(ctx);
  new vm.Script(SRC, { filename: "tokens.js" }).runInContext(ctx);
  return { ctx, els, list: els["tok-list"], calls };
}

const TOKENS = [
  { id: "a1", name: "macbook", created_at: CREATED, last_used_at: USED },
  { id: "b2", name: "gate-test-token", created_at: CREATED + 86400 * 6 },
];
const cells = (tr) => tr.children.map((td) => td.textContent);

// --- the page ------------------------------------------------------------------------------------------------

test("the page renders a table with Name, Created, Last used and a Revoke… per row", async () => {
  const { ctx, list } = page({ items: TOKENS });
  await ctx.loadTokens();
  const card = list.children[0];
  assert.ok(card.has("card") && card.has("tok-card"), "one kit card holds the table");
  const tables = card.byTag("table");
  assert.strictEqual(tables.length, 1);
  assert.ok(tables[0].has("tbl"), "the kit's .tbl");
  assert.strictEqual(tables[0].byTag("col").length, 4, "one colgroup, four columns");
  const heads = tables[0].byTag("th");
  assert.deepStrictEqual(heads.map((th) => th.textContent), ["Name", "Created", "Last used", "Actions"]);
  const act = heads[3].children[0];
  assert.ok(act.has("sr-only"), "the actions column has a name for a screen reader and none to see");
  assert.strictEqual(heads[3].own, "");

  const rows = tables[0].byTag("tbody")[0].byTag("tr");
  assert.strictEqual(rows.length, 2);
  assert.deepStrictEqual(cells(rows[0]), ["macbook", tokWhen(CREATED), tokWhen(USED), "Revoke…"]);
  assert.deepStrictEqual(cells(rows[1]), ["gate-test-token", tokWhen(CREATED + 86400 * 6), "Never used", "Revoke…"]);

  for (const [i, tr] of rows.entries()) {
    const btn = tr.byTag("button");
    assert.strictEqual(btn.length, 1, "Revoke… is the row's only action");
    assert.strictEqual(btn[0].className, "btn btn-danger btn-sm");
    assert.strictEqual(btn[0].type, "button");
    assert.strictEqual(btn[0].getAttribute("aria-label"), "Revoke " + TOKENS[i].name);
    assert.ok(!/(^| )pri( |$)|btn-primary/.test(btn[0].className), "no primary button on the page");
  }
});

test("dates are tabular, and Never used is muted", async () => {
  const { ctx, list } = page({ items: TOKENS });
  await ctx.loadTokens();
  const rows = list.children[0].byTag("tbody")[0].byTag("tr");
  const [name0, created0, used0] = rows[0].children;
  assert.ok(created0.has("tnum") && used0.has("tnum"));
  assert.ok(!created0.has("muted") && !used0.has("muted"), "a date is the text colour");
  assert.ok(!name0.has("tnum"), "a name is the sans text, not a figure");
  const [, , never] = rows[1].children;
  assert.ok(never.has("tnum") && never.has("muted"), "Never used is a muted figure cell");
});

test("a token with no created_at leaves its Created cell empty", async () => {
  const { ctx, list } = page({ items: [{ id: "c3", name: "old", last_used_at: USED }] });
  await ctx.loadTokens();
  const [tr] = list.children[0].byTag("tbody")[0].byTag("tr");
  assert.deepStrictEqual(cells(tr), ["old", "", tokWhen(USED), "Revoke…"]);
});

test("a token's name is text, never markup", async () => {
  const name = '<img src=x onerror="alert(1)">';
  const { ctx, list } = page({ items: [{ id: "d4", name, created_at: CREATED }] });
  await ctx.loadTokens();
  const tr = list.children[0].byTag("tbody")[0].byTag("tr")[0];
  assert.strictEqual(tr.children[0].textContent, name);
  assert.strictEqual(tr.children[0].children.length, 0, "no element was made from it");
  assert.strictEqual(tr.byTag("button")[0].getAttribute("aria-label"), "Revoke " + name);
  assert.doesNotMatch(SRC, /innerHTML|insertAdjacentHTML|outerHTML/, "tokens.js builds nodes, never markup strings");
});

test("the footnote is the approved copy", async () => {
  const { ctx, list } = page({ items: TOKENS });
  await ctx.loadTokens();
  assert.strictEqual(list.children.length, 2, "the card, then the note under it");
  const note = list.children[1];
  assert.strictEqual(note.textContent, FOOTNOTE);
  assert.ok(note.has("tok-under") && note.has("t-sm") && note.has("muted"), "the kit's 12px muted line, outside the card");
  assert.strictEqual(list.children[0].byClass("tok-under").length, 0, "outside the card");
  assert.strictEqual(FOOTNOTE, "A token is shown once, when you create it. Revoking one signs that device out on its next request.");
});

test("no token yet: the card holds the line 'No tokens yet.' and the note stays", async () => {
  const { ctx, list } = page({ items: [] });
  await ctx.loadTokens();
  const card = list.children[0];
  assert.ok(card.has("card"));
  assert.strictEqual(card.byTag("table").length, 0, "no table of nothing");
  assert.strictEqual(card.textContent, "No tokens yet.");
  assert.ok(card.children[0].has("tokempty"));
  assert.strictEqual(list.children[1].textContent, FOOTNOTE);
  const none = page({ items: null });
  await none.ctx.loadTokens();
  assert.strictEqual(none.list.children[0].textContent, "No tokens yet.", "a null list is an empty one");
});

test("Revoke… arms the row, which keeps today's confirm copy inside itself", async () => {
  const { ctx, list, calls } = page({ items: TOKENS });
  await ctx.loadTokens();
  let rows = list.children[0].byTag("tbody")[0].byTag("tr");
  rows[0].byTag("button")[0].onclick();
  await settle();
  rows = list.children[0].byTag("tbody")[0].byTag("tr");
  assert.strictEqual(rows.length, 2, "still one row per token");
  assert.ok(rows[0].has("arm") && !rows[1].has("arm"), "only the chosen token's row is armed");
  assert.strictEqual(rows[0].children[0].textContent, "macbook", "the row still names the token");
  assert.strictEqual(rows[0].textContent.includes(ARM_COPY), true, "today's confirm copy, in the row");
  assert.strictEqual(rows[0].children[1].colSpan, 3, "the question has the row's other three columns");
  const [cancel, go] = rows[0].byTag("button");
  assert.strictEqual(cancel.textContent, "Cancel");
  assert.strictEqual(cancel.className, "btn btn-sm");
  assert.strictEqual(go.textContent, "Revoke", "no ellipsis: this is the question, not the promise of one");
  assert.strictEqual(go.className, "btn btn-danger btn-sm");
  assert.deepStrictEqual(cells(rows[1]), ["gate-test-token", tokWhen(CREATED + 86400 * 6), "Never used", "Revoke…"], "the other rows are as they were");

  cancel.onclick();
  await settle();
  rows = list.children[0].byTag("tbody")[0].byTag("tr");
  assert.ok(!rows[0].has("arm"), "Cancel puts the row back");
  assert.strictEqual(rows[0].textContent.includes(ARM_COPY), false);
  assert.ok(!calls.some(([, m]) => m === "DELETE"), "nothing was revoked");

  rows[0].byTag("button")[0].onclick();
  await settle();
  list.children[0].byTag("tbody")[0].byTag("tr")[0].byTag("button")[1].onclick();
  await settle();
  assert.deepStrictEqual(calls.filter(([, m]) => m === "DELETE"), [["/api/tokens/a1", "DELETE"]], "Revoke deletes that one token");
});

// --- the phone -----------------------------------------------------------------------------------------------

test("the phone keeps its rows: a list item per token, 'used never', and the in-row confirm", async () => {
  const { ctx, list } = page({ items: TOKENS, asPage: false });
  await ctx.loadTokens();
  assert.strictEqual(list.children.length, 2, "two rows and no card or footnote");
  assert.strictEqual(list.byTag("table").length, 0);
  const [a, b] = list.children;
  assert.strictEqual(a.tag, "li");
  assert.strictEqual(a.className, "tokrow");
  assert.deepStrictEqual(a.children.map((c) => [c.tag, c.className]), [["span", "nm"], ["span", "meta"], ["button", "btn dgr"]]);
  assert.strictEqual(a.children[0].textContent, "macbook");
  assert.strictEqual(a.children[1].textContent, "used " + tokWhen(USED));
  assert.strictEqual(a.children[1].title, "Created " + tokWhen(CREATED));
  assert.strictEqual(b.children[1].textContent, "used never", "a token that never connected");
  const btn = a.children[2];
  assert.strictEqual(btn.textContent, "Revoke…");
  assert.strictEqual(btn.getAttribute("aria-label"), "Revoke macbook");

  btn.onclick();
  await settle();
  const armed = list.children[0];
  assert.strictEqual(armed.className, "tokrow arm");
  assert.deepStrictEqual(armed.children.map((c) => [c.tag, c.className, c.textContent]), [
    ["span", "nm", "macbook"],
    ["span", "meta", ARM_COPY],
    ["button", "btn", "Cancel"],
    ["button", "btn dgr", "Revoke"],
  ]);
});

test("the phone's empty list is a bare line", async () => {
  const { ctx, list } = page({ items: [], asPage: false });
  await ctx.loadTokens();
  assert.strictEqual(list.children.length, 1);
  assert.strictEqual(list.children[0].tag, "li");
  assert.strictEqual(list.children[0].className, "tokempty");
  assert.strictEqual(list.children[0].textContent, "No tokens yet.");
});

// --- the look ------------------------------------------------------------------------------------------------

const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const DESK = stripComments(read("desktop.css"));
const BASE = stripComments(read("app.css"));
const P = "#page-host > #settings-modal.as-page";

// rules: [selector, Map(decl)] for every plain `a, b { c: d }` in the sheet (the @media / @container heads are
// folded into the first selector they precede, which no rule asked for here is).
function rules(src) {
  const out = [];
  for (const m of src.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const decls = new Map();
    for (const d of m[2].split(";")) {
      const i = d.indexOf(":");
      if (i > 0) decls.set(d.slice(0, i).trim(), d.slice(i + 1).replace(/\s+/g, " ").trim());
    }
    let depth = 0, cur = "";
    const sels = [];
    for (const ch of m[1]) {
      if (ch === "(") depth++;
      if (ch === ")") depth--;
      if (ch === "," && depth === 0) { sels.push(cur.replace(/\s+/g, " ").trim()); cur = ""; } else cur += ch;
    }
    sels.push(cur.replace(/\s+/g, " ").trim());
    for (const s of sels) out.push([s, decls]);
  }
  return out;
}
const RULES = rules(DESK);
const decls = (sel) => {
  const merged = new Map();
  for (const [s, d] of RULES) if (s === sel || s.endsWith("} " + sel) || s.endsWith(") " + sel)) for (const [k, v] of d) merged.set(k, v);
  return merged;
};
const has = (sel) => RULES.some(([s]) => s === sel || s.endsWith(" " + sel));

test("the table is the kit's: fixed columns at 38 / 22 / 22 / 18, rows 7px above and below, the first cell 16px in", () => {
  const T = P + " #tok-list .tok-tbl";
  assert.strictEqual(decls(T).get("table-layout"), "fixed");
  assert.deepStrictEqual([1, 2, 3, 4].map((n) => decls(T + " col:nth-child(" + n + ")").get("width")), ["38%", "22%", "22%", "18%"]);
  const td = decls(T + " td");
  assert.strictEqual(td.get("height"), "auto");
  assert.strictEqual(td.get("padding-top"), "7px");
  assert.strictEqual(td.get("padding-bottom"), "7px");
  assert.strictEqual(td.get("vertical-align"), "top");
  assert.strictEqual(decls(T + " th:first-child").get("padding-left"), "16px");
  assert.strictEqual(decls(T + " td:first-child").get("padding-left"), "16px");
  assert.strictEqual(decls(T + " th:last-child").get("padding-right"), "12px");
  assert.strictEqual(decls(T + " td:last-child").get("padding-right"), "12px");
  const num = decls(T + " td.num");
  assert.strictEqual(num.get("padding-top"), "5px", "a 24px button in a 34px row");
  assert.strictEqual(num.get("padding-bottom"), "5px");
  assert.strictEqual(decls(T + " .tok-nm").get("overflow-wrap"), "anywhere", "a long name wraps in its column");
});

test("the card clips its table to the corners, and the footnote sits 8px under it, 2px in", () => {
  assert.strictEqual(decls(P + " #tok-list > .tok-card").get("overflow"), "hidden");
  const n = decls(P + " #tok-list > .tok-under");
  assert.strictEqual(n.get("margin-top"), "8px");
  assert.strictEqual(n.get("padding"), "0 2px");
  const e = decls(P + " #tok-list > .tok-card > .tokempty");
  assert.strictEqual(e.get("padding"), "12px 16px");
  assert.strictEqual(e.get("margin"), "0");
  assert.strictEqual(e.get("font-size"), "var(--t-sm)");
});

test("Revoke… is the kit's small danger button: the panel's .btn rules give the small values and the ring back", () => {
  // The panel's body rule paints every `.btn` under #tokens at 0 12px, the secondary fill and a transparent border;
  // the row's buttons restate the kit (§C3 `.btn-sm`, `.btn-danger`), one id heavier.
  const B = P + " > #tokens #tok-list .btn";
  const sm = decls(B + ".btn-sm");
  assert.strictEqual(sm.get("padding"), "0 8px");
  assert.strictEqual(sm.get("border-radius"), "var(--r-chip)");
  assert.strictEqual(sm.get("font-size"), "var(--t-sm)");
  const d = decls(B + ".btn-danger:where(:not(.pri))");
  assert.strictEqual(d.get("background"), "transparent");
  assert.strictEqual(d.get("color"), "var(--red)");
  assert.strictEqual(d.get("border-color"), "rgba(240,106,106,.45)");
  const h = decls(B + ".btn-danger:where(:not(.pri)):hover");
  assert.strictEqual(h.get("background"), "var(--red-tint)");
  assert.strictEqual(h.get("border-color"), "var(--red)");
  assert.strictEqual(decls(B + ".btn-sm:where(:not(.pri)):where(:not(.btn-danger)):hover").get("background"), "var(--line)", "Cancel has a hover");
  // The panel's own rule is one id lighter than these four.
  const panel = P + " > :is(#tokens, #accounts, #settings, #about) > :not(.set-head) .btn";
  assert.strictEqual(decls(panel).get("padding"), "0 12px", "the rule being answered is still there");
  const at = (sel) => DESK.indexOf(sel + " {");
  assert.ok(at(B + ".btn-danger:where(:not(.pri))") > at(panel + ":where(:not(.pri))"), "written after it");
});

test("an armed row is the kit's red row: a tint, a 2px red edge, the question in red beside Cancel and Revoke", () => {
  const T = P + " #tok-list .tok-tbl";
  assert.strictEqual(decls(T + " tr.arm").get("background"), "var(--red-tint)");
  assert.strictEqual(decls(T + " tr.arm > td:first-child").get("box-shadow"), "inset 2px 0 0 var(--red)");
  const c = decls(T + " .tok-confirm");
  assert.strictEqual(c.get("display"), "flex");
  assert.strictEqual(c.get("align-items"), "center");
  assert.strictEqual(decls(T + " .tok-ask").get("color"), "var(--red)");
  assert.strictEqual(decls(T + " .tok-ask").get("flex"), "1 1 auto");
  assert.strictEqual(decls(T + " .tok-acts").get("gap"), "4px");
  assert.ok(has(T + " .tok-arm"));
});

test("the one-time reveal's buttons are the kit's, with a hover (the panel's fill rule leaves none)", () => {
  const R = P + " > #tokens > #tok-reveal .btn";
  assert.strictEqual(decls(R + ":where(:not(.pri)):where(:not(.gh)):hover").get("background"), "var(--line)", "Copy: the kit secondary hover");
  const gh = decls(R + ".gh:hover");
  assert.strictEqual(gh.get("background"), "var(--hover)", "Done: the kit ghost hover");
  assert.strictEqual(gh.get("color"), "var(--fg)");
  const markup = read("index.html");
  assert.match(markup, /<button id="tok-copy" type="button" class="btn">Copy<\/button>/, "the phone's markup is untouched");
  assert.match(markup, /<button id="tok-done" type="button" class="btn gh">Done<\/button>/);
});

test("every new rule is the page's and every kit value is a token: nothing for the phone", () => {
  for (const sel of [".tok-card", ".tok-tbl", ".tok-nm", ".tok-arm", ".tok-confirm", ".tok-ask", ".tok-acts", ".tok-under"]) {
    assert.ok(RULES.some(([s]) => s.includes(sel)), sel + " has a desktop rule");
    assert.ok(!BASE.includes(sel), sel + " is not in app.css: the phone never draws it");
  }
  const mine = RULES.filter(([s]) => /tok-(card|tbl|nm|arm|confirm|ask|acts|under)|#tok-reveal/.test(s));
  assert.ok(mine.length >= 20);
  for (const [s] of mine) assert.ok(s.startsWith(P), "scoped to the page: " + s);
  for (const [s, d] of mine) for (const [k, v] of d) {
    assert.doesNotMatch(v, /#[0-9a-f]{3,8}\b/i, s + " { " + k + " } uses a hex");
    assert.doesNotMatch(v, /--faint/, s + " { " + k + " } is faint");
    if (k === "font-weight") assert.ok(Number(v) <= 600 || /var\(--w-/.test(v), s + " weight " + v);
    if (k === "font-size") assert.match(v, /^var\(--t-/, s + " takes a kit size");
  }
});
