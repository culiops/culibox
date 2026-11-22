"use strict";
// tokens.js on the Device tokens page (UI plan 4, #174, Task 7; frame M2.4, digest-design §A7 and §B5): a kit table in a
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

// The card width below which the rows stack (desktop.css): above it the four columns fit.
const STACK_PX = 480;
// Below this the name leaves no room beside Revoke… and the button goes under the dates (the 701px floor: a 135px card).
const FLOOR_PX = 180;
const CREATED = 1793404800;   // 2026-10-31
const USED = 1793923200;      // 2026-11-06
const ARM_COPY = "Revoke? That device is signed out on its next request. This cannot be undone.";
const FOOTNOTE = "A device token is shown once, when you create it. Revoking one signs that device out on its next request.";

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
  removeAttribute(k) { delete this.attrs[k]; }
  // The element that has keyboard focus: a real one loses it when it leaves the document, which is what the
  // list's repaint does to the button that was pressed, so a test reads Node.focused only after the repaint.
  focus() { Node.focused = this; }
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
function page({ items, asPage = true, api } = {}) {
  const calls = [];
  const copied = [];
  Node.focused = null;
  const els = {
    "set-act-tokens": Object.assign(new Node("button"), { id: "set-act-tokens" }),
    "settings-modal": Object.assign(new Node("dialog"), { className: asPage ? "as-page" : "", open: true }),
    "tok-list": new Node("ul"),
    "tok-err": new Node("div"),
    "tok-reveal": Object.assign(new Node("div"), { hidden: true }),
    "tok-reveal-val": new Node("div"),
    "tok-new": new Node("button"),
    "tok-copy": Object.assign(new Node("button"), { own: "Copy" }),
    "tok-done": new Node("button"),
  };
  const ctx = {
    document: {
      readyState: "complete", addEventListener() {},
      getElementById: (id) => els[id] || null,
      createElement: (tag) => new Node(tag),
    },
    api: api ? async (url, opts) => { calls.push([url, (opts && opts.method) || "GET"]); return api(url, (opts && opts.method) || "GET"); }
      : async (url, opts) => { calls.push([url, (opts && opts.method) || "GET"]); return items; },
    window: {},
    askPrompt: async () => "laptop",
    copyText: (text, done) => { ctx.window.__lastCopied = text; copied.push(text); done(true); },
    setTimeout: () => 0,
    console,
  };
  vm.createContext(ctx);
  new vm.Script(SRC, { filename: "tokens.js" }).runInContext(ctx);
  return { ctx, els, list: els["tok-list"], calls, copied };
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
  assert.ok(card.has("tok-card"), "one card holds the table");
  assert.ok(!card.has("card"), "and it is not the phone's .card class (app.css's amber session card): its fill and radius are desktop.css's (final review M5)");
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
  assert.strictEqual(FOOTNOTE, "A device token is shown once, when you create it. Revoking one signs that device out on its next request.");
});

test("no token yet: the card holds the line 'No device tokens yet.' and the note stays", async () => {
  const { ctx, list } = page({ items: [] });
  await ctx.loadTokens();
  const card = list.children[0];
  assert.ok(card.has("tok-card") && !card.has("card"));
  assert.strictEqual(card.byTag("table").length, 0, "no table of nothing");
  assert.strictEqual(card.textContent, "No device tokens yet.");
  assert.ok(card.children[0].has("tokempty"));
  assert.strictEqual(list.children[1].textContent, FOOTNOTE);
  const none = page({ items: null });
  await none.ctx.loadTokens();
  assert.strictEqual(none.list.children[0].textContent, "No device tokens yet.", "a null list is an empty one");
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
  assert.strictEqual(list.children[0].textContent, "No device tokens yet.");
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
// The narrow-card step is an @container block (pinned below on its own); the rules above every step are the table as the frame draws it.
function containerBlocks(src) {
  const out = [];
  const re = /@container\s*\(max-width:\s*(\d+)px\)\s*\{/g;
  let m;
  while ((m = re.exec(src))) {
    let depth = 1, i = re.lastIndex;
    while (depth && i < src.length) { const ch = src[i++]; if (ch === "{") depth++; else if (ch === "}") depth--; }
    out.push({ px: +m[1], body: src.slice(re.lastIndex, i - 1), start: m.index, end: i });
  }
  return out;
}
const STEPS = containerBlocks(DESK);
const withoutSteps = STEPS.reduceRight((src, b) => src.slice(0, b.start) + src.slice(b.end), DESK);
const RULES = rules(withoutSteps);
const decls = (sel) => {
  const merged = new Map();
  for (const [s, d] of RULES) if (s === sel || s.endsWith("} " + sel) || s.endsWith(") " + sel)) for (const [k, v] of d) merged.set(k, v);
  return merged;
};
const has = (sel) => RULES.some(([s]) => s === sel || s.endsWith(" " + sel));

test("the table is the kit's: fixed columns at 25% each (the approved render), rows 7px above and below, the first cell 16px in", () => {
  const T = P + " #tok-list .tok-tbl";
  assert.strictEqual(decls(T).get("table-layout"), "fixed");
  // T1 (ruling: the approved render wins): the canvas colgroup (38 / 22 / 22 / 18) is lost in the render and in the PNG, which draws four equal
  // quarters of the 820px table (Created at x 739, Last used 944, Actions 1149).
  assert.deepStrictEqual([1, 2, 3, 4].map((n) => decls(T + " col:nth-child(" + n + ")").get("width")), ["25%", "25%", "25%", "25%"]);
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

// D3 (gate 3, B6 FAIL at 760): the card is 194px there (760 less the 290 sidebar, the 212 nav and 64 of padding) and four columns do not fit it: the card
// overran (242 vs 194), Created and Last used overprinted each other and Revoke… was clipped. Below a card width the rows stack, with every datum kept.
test("a narrow card stacks each row: the name and Revoke… on line 1, Created and Last used under, nothing dropped, no horizontal room needed (D3)", () => {
  const T = P + " #tok-list .tok-tbl";
  assert.strictEqual(decls(P + " #tok-list").get("container-type"), "inline-size", "the list is the container the step reads");
  const steps = STEPS.filter((b) => /#tok-list/.test(b.body));
  assert.deepStrictEqual(steps.map((b) => b.px), [STACK_PX, FLOOR_PX], "the stack, then the 701px floor");
  assert.ok(STACK_PX >= 420, "the four columns need ~420px: 25% of it must hold 'Nov 27, 2026' (86px) and 24px of padding");
  const st = new Map();
  for (const [sel, d] of rules(steps[0].body)) { const m = st.get(sel) || new Map(); for (const [k, v] of d) m.set(k, v); st.set(sel, m); }
  const at = (sel) => st.get(P + " #tok-list .tok-tbl" + sel) || new Map();
  // The table is a stack of grid rows; the head row is not drawn (the date lines carry their own words) and the colgroup does nothing.
  for (const sel of ["", " tbody"]) assert.strictEqual(at(sel).get("display"), "block", "the table" + sel + " is a block");
  // The column heads are not drawn but stay exposed: a visually-hidden clip, not display:none (final review M11), so a screen reader still has them.
  assert.strictEqual(at(" colgroup").get("display"), "none", "the colgroup has nothing to expose");
  const head = at(" thead");
  assert.strictEqual(head.get("display"), undefined, "the heads are not removed from the accessibility tree");
  assert.deepStrictEqual([head.get("position"), head.get("width"), head.get("height"), head.get("margin"), head.get("padding"), head.get("border"), head.get("overflow"), head.get("clip"), head.get("white-space")],
    ["absolute", "1px", "1px", "-1px", "0", "0", "hidden", "rect(0 0 0 0)", "nowrap"], "the kit's visually-hidden clip (app.css .sr-only)");
  const row = at(" tr");
  assert.deepStrictEqual([row.get("display"), row.get("grid-template-columns"), row.get("column-gap"), row.get("padding")], ["grid", "minmax(0, 1fr) auto", "12px", "7px 12px 7px 16px"]);
  assert.strictEqual(at(" tr + tr").get("border-top"), "1px solid var(--line)", "the hairline between rows moves from the cells to the rows");
  for (const sel of [" td", " td:first-child", " td:last-child"]) assert.deepStrictEqual([at(sel).get("padding"), at(sel).get("border"), at(sel).get("height")], ["0", "0", "auto"], sel + ": the row owns the padding");
  assert.strictEqual(at(" .tok-nm").get("grid-column"), "1");
  assert.deepStrictEqual([at(" td.num").get("grid-column"), at(" td.num").get("grid-row"), at(" td.num").get("margin-top")], ["2", "1", "-2px"], "Revoke… at the row's end, on the name's line");
  // Created and Last used: 12/18 muted lines under the name, across the card, each with its own word (the column heads are gone).
  const dates = at(" td.tnum");
  assert.deepStrictEqual([dates.get("grid-column"), dates.get("font-size"), dates.get("line-height"), dates.get("color"), dates.get("white-space")], ["1 / -1", "var(--t-sm)", "var(--lh-sm)", "var(--muted)", "normal"]);
  // On the date cells only: the armed row's question is the second cell of its row and must not say "Created".
  assert.strictEqual(at(" td.tnum:nth-child(2):not(:empty)::before").get("content"), '"Created "');
  assert.strictEqual(at(" td.tnum:nth-child(3):not(.muted)::before").get("content"), '"Last used "', "'Never used' says it itself");
  assert.ok(![...st.keys()].some((k) => /td:nth-child\(\d\)/.test(k) && !/td\.tnum:nth-child/.test(k)), "no bare td:nth-child rule: it would reach the armed row's question cell");
  // Armed: the confirm takes the card's width under the name; the question has its own lines and the buttons wrap under it rather than leave the card.
  assert.strictEqual(at(" .tok-arm").get("grid-column"), "1 / -1");
  assert.strictEqual(at(" .tok-confirm").get("flex-wrap"), "wrap");
  assert.strictEqual(at(" .tok-ask").get("flex"), "1 1 100%");
  assert.deepStrictEqual([at(" .tok-acts").get("flex"), at(" .tok-acts").get("min-width"), at(" .tok-acts").get("flex-wrap")], ["0 1 auto", "0", "wrap"], "the pair may shrink to the card and wrap between themselves (it was flex: none, which overran a 135px card by 3px)");
  assert.strictEqual(at(" tr.arm").get("box-shadow"), "inset 2px 0 0 var(--red)", "the red edge is the row's now");
  assert.strictEqual(at(" tr.arm > td:first-child").get("box-shadow"), "none");
  assert.strictEqual(at(" tr.arm").get("background"), undefined, "the tint is the base rule's");
  // Every value is a kit token / no px font, no hex, nothing wider than the card, and the card still clips at its corners.
  for (const [sel, d] of st) for (const [k, v] of d) {
    assert.doesNotMatch(v, /#[0-9a-f]{3,8}\b/i, sel + " { " + k + " } uses a hex");
    if (k === "font-size") assert.match(v, /^var\(--t-/, sel);
    // The one exception is the heads' visually-hidden clip (1px: a hiding technique, not a layout width).
    assert.ok(sel.endsWith(".tok-tbl thead") || !/^(min-)?width$/.test(k) || !/\d+px/.test(v), sel + " { " + k + ": " + v + " } sets no fixed width");
  }
  assert.strictEqual(decls(P + " #tok-list > .tok-card").get("overflow"), "hidden");
  assert.strictEqual(decls(P + " #tok-list > .tok-card").get("position"), "relative", "the card holds the clipped heads, so they cannot sit anywhere else on the page");
  assert.ok([...st.keys()].every((sel) => sel.startsWith(P + " #tok-list")), "scoped to the page's list");
  // The floor: at the 701px window the card is 135px and Revoke… beside the name would leave it 20px, so it drops under the dates.
  const fl = new Map();
  for (const [sel, d] of rules(steps[1].body)) for (const [k, v] of d) fl.set(sel + "|" + k, v);
  assert.strictEqual(fl.get(P + " #tok-list .tok-tbl td.num|grid-column"), "1 / -1");
  assert.strictEqual(fl.get(P + " #tok-list .tok-tbl td.num|grid-row"), "auto");
  assert.strictEqual(fl.get(P + " #tok-list .tok-tbl td.num|justify-self"), "start");
  assert.ok(FLOOR_PX >= 135 && FLOOR_PX < 194, "above the 701px floor's 135px card, below the 760px window's 194px one");
  assert.ok(!decls(T + " td").has("white-space") && !decls(T + " .tok-nm").has("min-width"), "the wide table's own cells are unchanged");
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

// --- keyboard focus -----------------------------------------------------------------------------------------

// loadTokens rebuilds every row, so the button that was pressed leaves the document and focus would fall to <body>:
// a keyboard or screen-reader user pressing Enter on "Revoke macbook" would see and hear nothing. The page puts it
// back: Cancel when a row is armed (the safe default), Revoke… after Cancel, the next row's Revoke… after a revoke.
const rowsOf = (list) => list.children[0].byTag("tbody")[0].byTag("tr");
const rowFor = (list, name) => rowsOf(list).find((tr) => tr.children[0].textContent === name);
const THREE = () => [
  { id: "a1", name: "macbook", created_at: CREATED, last_used_at: USED },
  { id: "b2", name: "gate-test-token", created_at: CREATED + 86400 },
  { id: "c3", name: "ci", created_at: CREATED + 86400 * 2 },
];
// A server that remembers: DELETE /api/tokens/<id> takes the token out of what the next GET returns.
function server(items, { order = (l) => l } = {}) {
  let live = items.slice();
  return (url, method) => {
    if (method === "DELETE") { live = live.filter((t) => t.id !== decodeURIComponent(url.split("/").pop())); return undefined; }
    return order(live.slice());
  };
}

test("arming a row puts focus on that row's Cancel, which names the token", async () => {
  const { ctx, list } = page({ items: THREE() });
  await ctx.loadTokens();
  assert.strictEqual(Node.focused, null, "drawing the page takes nobody's focus");
  rowFor(list, "gate-test-token").byTag("button")[0].onclick();
  await settle();
  const row = rowFor(list, "gate-test-token");
  assert.ok(row.has("arm"));
  const [cancel, go] = row.byTag("button");
  assert.strictEqual(Node.focused, cancel, "Cancel, not the destructive Revoke");
  assert.strictEqual(cancel.textContent, "Cancel");
  assert.strictEqual(cancel.getAttribute("aria-label"), "Cancel revoking gate-test-token");
  assert.strictEqual(go.getAttribute("aria-label"), "Revoke gate-test-token");
  assert.ok(row.byTag("button").every((b) => b.parent), "and it is in the new DOM, not the one the repaint threw away");
});

test("Cancel puts focus back on that row's Revoke…", async () => {
  const { ctx, list } = page({ items: THREE() });
  await ctx.loadTokens();
  rowFor(list, "gate-test-token").byTag("button")[0].onclick();
  await settle();
  rowFor(list, "gate-test-token").byTag("button")[0].onclick();
  await settle();
  const row = rowFor(list, "gate-test-token");
  assert.ok(!row.has("arm"));
  assert.strictEqual(Node.focused, row.byTag("button")[0]);
  assert.strictEqual(Node.focused.textContent, "Revoke…");
  assert.strictEqual(Node.focused.getAttribute("aria-label"), "Revoke gate-test-token");
});

test("after a revoke focus goes to the next row's Revoke…, else the previous row's, else New device token", async () => {
  const { ctx, list, els, calls } = page({ items: THREE(), api: server(THREE()) });
  await ctx.loadTokens();
  const revoke = async (name) => {
    rowFor(list, name).byTag("button")[0].onclick();            // Revoke…
    await settle();
    rowFor(list, name).byTag("button")[1].onclick();            // Revoke, in the row
    await settle();
  };
  // The middle one: the next row (ci).
  await revoke("gate-test-token");
  assert.deepStrictEqual(rowsOf(list).map((tr) => tr.children[0].textContent), ["macbook", "ci"]);
  assert.strictEqual(Node.focused, rowFor(list, "ci").byTag("button")[0], "the next row");
  assert.strictEqual(Node.focused.getAttribute("aria-label"), "Revoke ci");
  // The last one: no next row, so the previous (macbook).
  await revoke("ci");
  assert.deepStrictEqual(rowsOf(list).map((tr) => tr.children[0].textContent), ["macbook"]);
  assert.strictEqual(Node.focused, rowFor(list, "macbook").byTag("button")[0], "the previous row");
  // The only one: nothing left, so the head's New token.
  await revoke("macbook");
  assert.strictEqual(list.children[0].textContent, "No device tokens yet.");
  assert.strictEqual(Node.focused, els["set-act-tokens"], "New device token");
  assert.deepStrictEqual(calls.filter(([, m]) => m === "DELETE").map(([u]) => u), ["/api/tokens/b2", "/api/tokens/c3", "/api/tokens/a1"]);
});

test("focus finds its row by token id, not by position: the list is fetched anew", async () => {
  // The server answers newest first once something has been revoked, so "the next row" by index would be another token.
  const { ctx, list } = page({ items: THREE(), api: server(THREE(), { order: (l) => (l.length < 3 ? l.reverse() : l) }) });
  await ctx.loadTokens();
  rowFor(list, "macbook").byTag("button")[0].onclick();
  await settle();
  rowFor(list, "macbook").byTag("button")[1].onclick();
  await settle();
  assert.deepStrictEqual(rowsOf(list).map((tr) => tr.children[0].textContent), ["ci", "gate-test-token"], "the repaint put the rows in another order");
  assert.strictEqual(Node.focused, rowFor(list, "gate-test-token").byTag("button")[0], "macbook's next row was gate-test-token, wherever it is now");
});

test("a row that has gone elsewhere hands focus to New device token instead of dropping it", async () => {
  const gone = server(THREE());
  let drop = false;
  const { ctx, list, els } = page({ items: THREE(), api: (url, m) => { const l = gone(url, m); return drop ? l.filter((t) => t.id !== "b2") : l; } });
  await ctx.loadTokens();
  rowFor(list, "gate-test-token").byTag("button")[0].onclick();
  await settle();
  drop = true;                       // revoked in another tab meanwhile
  rowFor(list, "gate-test-token").byTag("button")[0].onclick();   // Cancel
  await settle();
  assert.strictEqual(rowFor(list, "gate-test-token"), undefined);
  assert.strictEqual(Node.focused, els["set-act-tokens"]);
});

test("a failed revoke leaves focus where it is and says so", async () => {
  const items = THREE();
  const { ctx, list, els } = page({ items, api: (url, m) => { if (m === "DELETE") throw new Error("boom"); return items; } });
  await ctx.loadTokens();
  rowFor(list, "macbook").byTag("button")[0].onclick();
  await settle();
  const before = Node.focused;
  rowFor(list, "macbook").byTag("button")[1].onclick();
  await settle();
  assert.match(els["tok-err"].textContent, /Revoke failed: boom/);
  assert.strictEqual(Node.focused, before, "no repaint, so nothing to move");
});

test("the phone moves no focus and gives its buttons no labels it did not have", async () => {
  const { ctx, list } = page({ items: THREE(), asPage: false, api: server(THREE()) });
  await ctx.loadTokens();
  list.children[0].children[2].onclick();                       // Revoke…
  await settle();
  const [cancel, go] = list.children[0].children.slice(2);
  assert.strictEqual(Node.focused, null, "arming focuses nothing on a phone");
  assert.strictEqual(cancel.getAttribute("aria-label"), null);
  assert.strictEqual(go.getAttribute("aria-label"), null);
  cancel.onclick();
  await settle();
  assert.strictEqual(Node.focused, null);
  list.children[0].children[2].onclick();
  await settle();
  list.children[0].children[3].onclick();                       // Revoke
  await settle();
  assert.strictEqual(list.children.length, 2, "revoked");
  assert.strictEqual(Node.focused, null, "and still nothing focused: the phone's rows are as they were");
});

// --- list semantics -----------------------------------------------------------------------------------------

test("on the page the list is a card and a note, not 'a list of 2 items': role none on the list and its items", async () => {
  const { ctx, list } = page({ items: THREE() });
  await ctx.loadTokens();
  assert.strictEqual(list.getAttribute("role"), "none");
  assert.deepStrictEqual(list.children.map((li) => [li.tag, li.getAttribute("role")]), [["li", "none"], ["li", "none"]]);
  assert.strictEqual(list.children[0].byTag("table")[0].getAttribute("role"), "table", "the table is still a table: its role is explicit, because the narrow stack's display values would drop the native one (final review M11)");
  const none = page({ items: [] });
  await none.ctx.loadTokens();
  assert.strictEqual(none.list.getAttribute("role"), "none", "also with no token");
  assert.deepStrictEqual(none.list.children.map((li) => li.getAttribute("role")), ["none", "none"]);
});

// Final review M11: below a 480px card the table is a block, its rows grids and its cells blocks (desktop.css), which makes browsers
// drop the table semantics of the native elements. The page's table says what each part is, so a screen reader still reads a table with
// column heads in every width.
test("on the page the table, its row groups, rows, heads and cells carry their explicit roles (final review M11)", async () => {
  const { ctx, list } = page({ items: TOKENS });
  await ctx.loadTokens();
  const table = list.children[0].byTag("table")[0];
  assert.strictEqual(table.getAttribute("role"), "table");
  assert.strictEqual(table.getAttribute("aria-label"), "Device tokens", "and keeps its name");
  assert.deepStrictEqual(table.byTag("thead").map((n) => n.getAttribute("role")), ["rowgroup"]);
  assert.deepStrictEqual(table.byTag("tbody").map((n) => n.getAttribute("role")), ["rowgroup"]);
  const rows = table.byTag("tr");
  assert.strictEqual(rows.length, 3, "the head row and two tokens");
  assert.ok(rows.every((tr) => tr.getAttribute("role") === "row"), "every row");
  assert.deepStrictEqual(table.byTag("th").map((n) => n.getAttribute("role")), ["columnheader", "columnheader", "columnheader", "columnheader"]);
  assert.ok(table.byTag("td").length === 8 && table.byTag("td").every((n) => n.getAttribute("role") === "cell"), "every cell");
  // The armed row: its question cell spans the other three and is a cell too.
  table.byTag("tbody")[0].byTag("tr")[0].byTag("button")[0].onclick();
  await settle();
  const armed = list.children[0].byTag("tbody")[0].byTag("tr")[0];
  assert.strictEqual(armed.getAttribute("role"), "row");
  assert.deepStrictEqual(armed.byTag("td").map((n) => n.getAttribute("role")), ["cell", "cell"]);
  // The phone has no table at all, and so no role attribute on anything it draws.
  const phone = page({ items: TOKENS, asPage: false });
  await phone.ctx.loadTokens();
  assert.strictEqual(phone.list.byTag("table").length, 0);
  assert.ok(phone.list.walk(() => true).every((n) => n.getAttribute("role") === null));
});

// Final review M10: the armed row's question was visible text only; a screen reader that lands on Cancel or Revoke heard just the name.
test("the armed row's question is the description of its Cancel and Revoke (final review M10)", async () => {
  const { ctx, list } = page({ items: TOKENS });
  await ctx.loadTokens();
  list.children[0].byTag("tbody")[0].byTag("tr")[1].byTag("button")[0].onclick();
  await settle();
  const row = list.children[0].byTag("tbody")[0].byTag("tr")[1];
  const ask = row.byClass("tok-ask")[0];
  assert.strictEqual(ask.textContent, ARM_COPY, "the question is today's confirm copy, unchanged");
  assert.ok(ask.id && ask.id.includes("b2"), "it has an id of its own, made from the token's");
  const [cancel, go] = row.byTag("button");
  assert.strictEqual(cancel.getAttribute("aria-describedby"), ask.id);
  assert.strictEqual(go.getAttribute("aria-describedby"), ask.id);
  assert.strictEqual(cancel.getAttribute("aria-label"), "Cancel revoking gate-test-token", "the names are as they were");
  assert.strictEqual(go.getAttribute("aria-label"), "Revoke gate-test-token");
  // Only one row is armed at a time, so the id is unique on the page; a disarmed list has none.
  assert.strictEqual(list.byClass("tok-ask").length, 1);
  cancel.onclick();
  await settle();
  assert.strictEqual(list.byClass("tok-ask").length, 0);
  assert.ok(list.walk((n) => n.getAttribute("aria-describedby") !== null).length === 0, "nothing points at a question that is gone");
});

// Final review M5: the phone's .card (app.css) is an amber session card. A page node that carries it would be styled by it when the
// window crosses to a phone and Settings is reopened before /api/tokens answers (loadTokens clears the list only after the fetch).
test("a page-built node never carries the phone's .card class, so the stale list is not amber while the phone's fetch is out (final review M5)", async () => {
  const { ctx, list, els } = page({ items: TOKENS });
  await ctx.loadTokens();
  assert.deepStrictEqual(list.walk((n) => n.has("card")).map((n) => n.className), [], "no .card on the page's card, table or rows");
  els["settings-modal"].className = "";                         // the window crossed to a phone's
  let release;
  ctx.api = () => new Promise((r) => { release = r; });
  const loading = ctx.loadTokens();                             // the fetch is out: the page's nodes are still on screen
  assert.ok(list.children.length > 0, "fixture: the stale page nodes are still there");
  assert.deepStrictEqual(list.walk((n) => n.has("card")).map((n) => n.className), [], "none of them matches .card while the fetch is out");
  release(TOKENS);
  await loading;
  assert.ok(list.children.every((li) => li.className === "tokrow"), "and the phone's rows replace them");
});

test("the page's card has its panel fill and radius from a desktop.css rule, not from the .card class (final review M5)", () => {
  const c = decls(P + " #tok-list > .tok-card");
  assert.strictEqual(c.get("background"), "var(--panel)");
  assert.strictEqual(c.get("border-radius"), "var(--r-card)");
  assert.strictEqual(c.get("overflow"), "hidden", "clipped to its corners, as before");
});

test("the phone's list has no role, even after the page drew it (the mode can flip)", async () => {
  const { ctx, list, els } = page({ items: THREE() });
  await ctx.loadTokens();
  assert.strictEqual(list.getAttribute("role"), "none");
  els["settings-modal"].className = "";                         // the window narrowed to a phone's
  await ctx.loadTokens();
  assert.strictEqual(list.getAttribute("role"), null);
  assert.strictEqual(list.children.length, 3, "the phone's three rows");
  assert.ok(list.children.every((li) => li.getAttribute("role") === null && li.className === "tokrow"));
});

// --- the one-time reveal leaves no secret in the DOM (#208) ---------------------------------------------------------

const SECRET = "abox_one-time-secret";
// create(): a page whose POST answers with SECRET, the list refresh answering TOKENS; resolves once the reveal is up.
async function create(opts = {}) {
  const p = page({ items: TOKENS, api: async (url, method) => (method === "POST" ? { token: SECRET } : TOKENS), ...opts });
  await p.els["tok-new"].onclick();
  return p;
}
const shown = (p) => !p.els["tok-reveal"].hidden;
const value = (p) => p.els["tok-reveal-val"].textContent;

test("a created token is shown in the reveal and stays there while it is visible", async () => {
  const p = await create();
  assert.ok(shown(p), "the reveal is up");
  assert.strictEqual(value(p), SECRET, "with the value, so it can be read and copied");
  await p.els["tok-copy"].onclick();
  assert.deepStrictEqual(p.copied, [SECRET], "Copy still copies it");
});

test("Done hides the reveal and empties the value node (#208)", async () => {
  const p = await create();
  p.els["tok-done"].onclick();
  assert.ok(!shown(p));
  assert.strictEqual(value(p), "", "no bearer token text is left in the DOM");
});

test("a repaint that hides the reveal empties the value node (#208)", async () => {
  const p = await create();
  await p.ctx.loadTokens();
  assert.ok(!shown(p));
  assert.strictEqual(value(p), "");
});

test("leaving the tab empties the value node (#208)", async () => {
  const p = await create();
  p.ctx.tokLeave();
  assert.ok(!shown(p));
  assert.strictEqual(value(p), "");
});

test("the copy hook does not keep the token once the reveal hides, and Copy then copies nothing (#208)", async () => {
  const p = await create();
  await p.els["tok-copy"].onclick();
  assert.strictEqual(p.ctx.window.__lastCopied, SECRET, "fixture: the hook holds the copied token");
  p.els["tok-done"].onclick();
  assert.strictEqual(p.ctx.window.__lastCopied, "", "cleared with the reveal");
  p.copied.length = 0;
  await p.els["tok-copy"].onclick();
  assert.deepStrictEqual(p.copied, [], "a hidden reveal has nothing to copy");
});

test("the hook is left alone when it holds something else (#208)", async () => {
  const p = await create();
  p.ctx.window.__lastCopied = "git status";
  p.els["tok-done"].onclick();
  assert.strictEqual(p.ctx.window.__lastCopied, "git status");
});

// shellPage: the page above with the two shell.js functions that leave the tab (selectSettingsTab, initDialogs) lifted out
// of the real source into the same context, over a Settings dialog with two panels, so a tab switch and a dialog close
// are driven rather than read off the source.
function shellPage(opts) {
  const p = page({ items: TOKENS, ...opts });
  const dlg = p.els["settings-modal"];
  dlg.id = "settings-modal";
  const listeners = {};
  dlg.addEventListener = (type, fn) => { (listeners[type] = listeners[type] || []).push(fn); };
  Object.defineProperty(dlg, "classList", { value: { contains: (c) => dlg.has(c), toggle() {} } });
  p.els.manage = Object.assign(new Node("div"), { id: "manage" });
  p.els.tokens = Object.assign(new Node("div"), { id: "tokens" });
  const panels = [p.els.manage, p.els.tokens];
  p.ctx.document.querySelectorAll = (sel) => (sel === "dialog" ? [dlg] : sel === "#settings-modal .dbd" ? panels : []);
  p.ctx.setDialogError = () => {};
  p.ctx.pageClosed = () => {};
  p.ctx.focusTerminal = () => {};
  const shell = read("shell.js");
  for (const name of ["selectSettingsTab", "initDialogs"]) {
    const at = shell.indexOf("function " + name + "(");
    new vm.Script(shell.slice(at, shell.indexOf("\n}\n", at) + 2), { filename: "shell.js#" + name }).runInContext(p.ctx);
  }
  p.ctx.initDialogs();
  const select = (tab) => p.ctx.selectSettingsTab(tab);
  select("tokens");
  return { ...p, dlg, select, fire: (type) => (listeners[type] || []).forEach((fn) => fn()) };
}

test("choosing another Settings tab empties the reveal (#208)", async () => {
  const p = shellPage({ api: async (url, method) => (method === "POST" ? { token: SECRET } : TOKENS) });
  await p.els["tok-new"].onclick();
  assert.strictEqual(value(p), SECRET, "fixture: the reveal is up on the Access-tokens tab");
  p.select("manage");
  assert.ok(!shown(p));
  assert.strictEqual(value(p), "");
});

test("closing Settings empties the reveal (#208)", async () => {
  const p = shellPage({ api: async (url, method) => (method === "POST" ? { token: SECRET } : TOKENS) });
  await p.els["tok-new"].onclick();
  assert.strictEqual(value(p), SECRET, "fixture");
  p.dlg.open = false;
  p.fire("close");
  assert.ok(!shown(p));
  assert.strictEqual(value(p), "");
});

// A create is slow (the POST, then the list refresh). Whether its token is painted is decided when it is ready.
function slowCreate() {
  let release;
  const p = shellPage({ api: (url, method) => (method === "POST" ? new Promise((r) => { release = r; }) : Promise.resolve(TOKENS)) });
  const creating = p.els["tok-new"].onclick();
  return { p, creating, finish: async () => { await settle(); release({ token: SECRET }); await creating; } };
}

test("a create that finishes after the user left the tab, and did not come back, never paints the token (#208)", async () => {
  const { p, finish } = slowCreate();
  await settle();
  p.select("manage");                                // the tab is left while the POST is out
  await finish();
  assert.ok(!shown(p));
  assert.strictEqual(value(p), "", "the value never reaches a panel nobody is looking at");
});

test("a create that finishes after Settings was closed never paints the token (#208)", async () => {
  const { p, finish } = slowCreate();
  await settle();
  p.dlg.open = false;
  p.fire("close");
  await finish();
  assert.strictEqual(value(p), "");
});

test("a create still shows its token when the user left the tab and came back while it was in flight (#208)", async () => {
  const { p, finish } = slowCreate();
  await settle();
  p.select("manage");
  p.select("tokens");
  await finish();
  assert.ok(shown(p));
  assert.strictEqual(value(p), SECRET);
});
