"use strict";
// Settings > Trusted hosts (linktrust.js, #348): the hosts whose links open without the confirm dialog, with a Remove that asks inside
// its row. Run in a vm over the shared fake DOM, with the link gate's four calls (lgLoadTrust, lgTrustList, lgTrustCorrupt,
// lgRemoveTrust) stubbed: what the gate does with the server is linkgate.test.js's.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

// The shared fake DOM keeps no listeners; Node is shared, so this file adds the two it needs the way wsroots.test.js does.
Node.prototype.addEventListener = function (t, f) { ((this.listeners = this.listeners || {})[t] = this.listeners[t] || []).push(f); };
Node.prototype.fire = function (t, ev) { for (const f of (this.listeners || {})[t] || []) f(ev); };

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const tick = () => new Promise((r) => setImmediate(r));
// The mockup's "Nov 19" for a day this year, "Nov 19, 2025" for any other year.
const YEAR = new Date().getFullYear();
const fmt = (iso) => {
  const d = new Date(iso);
  return d.toLocaleDateString(undefined, d.getFullYear() === YEAR ? { month: "short", day: "numeric" } : { year: "numeric", month: "short", day: "numeric" });
};

const GH = { host: "github.com", subdomains: false, added: YEAR + "-11-19T12:00:00Z" };
const AWS = { host: "console.aws.amazon.com", subdomains: false, added: YEAR + "-06-20T12:00:00Z" };
const EX = { host: "example.org", subdomains: true, added: (YEAR - 1) + "-11-21T12:00:00Z" };

// One page: linktrust.js alone, a gate stub that holds a list, and the panel as index.html carries it.
function page(over) {
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  doc.body = doc.createElement("body");
  doc.getElementById = (id) => {
    const walk = (n) => { if (n.id === id) return n; for (const c of n.children || []) { const f = c instanceof Node ? walk(c) : null; if (f) return f; } return null; };
    return walk(doc.body);
  };
  const gate = { list: [GH, AWS, EX], corrupt: false, newer: false, failed: false, loads: 0, removed: [], failWith: null, hold: null };
  const box = {
    document: doc, window: { innerWidth: 1280 }, console, Date, JSON, Map, Set, Math, Promise, Array, Object, String, Number, Error,
    lgLoadTrust: async () => { gate.loads++; return gate.list.slice(); },
    lgTrustList: () => gate.list.slice(),
    lgTrustCorrupt: () => gate.corrupt,
    lgTrustNewer: () => gate.newer,
    lgTrustLoadFailed: () => gate.failed,
    lgRemoveTrust: async (host) => {
      gate.removed.push(host);
      if (gate.hold) await gate.hold;
      if (gate.failWith) throw Object.assign(new Error(gate.failWith.message), { status: gate.failWith.status, code: gate.failWith.code });
      gate.list = gate.list.filter((e) => e.host !== host);
    },
  };
  Object.assign(box, over || {});
  box.globalThis = box;
  const ctx = vm.createContext(box);
  new vm.Script(read("linktrust.js"), { filename: "linktrust.js" }).runInContext(ctx);
  const panel = doc.createElement("div");
  panel.id = "linktrust";
  const slot = doc.createElement("div");
  slot.className = "set-head";
  const body = doc.createElement("div");
  body.id = "lt-body";
  panel.append(slot, body);
  doc.body.appendChild(panel);
  return { box, doc, gate, body, lex: (n) => vm.runInContext(n, ctx), load: () => vm.runInContext("loadLinkTrust()", ctx) };
}

const all = (n, sel) => n.querySelectorAll(sel);
const btns = (n) => n.querySelectorAll("button");
const rowOf = (p, host) => all(p.body, "tr").find((r) => r.dataset && all(r, "[data-lt-host]").length && all(r, "[data-lt-host]")[0].dataset.ltHost === host);
const btnOf = (p, host, which) => all(p.body, "button").find((b) => b.dataset.ltHost === host && b.dataset.ltAct === which);
const cells = (tr) => tr.children.filter((c) => c instanceof Node).map((c) => c.textContent);

// ---- the list -------------------------------------------------------------------------------------------------------------------

test("a host per row: Host in mono, what it covers, the day it was added, and Remove…", async () => {
  const p = page();
  await p.load();
  const rows = all(p.body, "tbody tr");
  assert.strictEqual(rows.length, 3);
  assert.deepStrictEqual(cells(rows[0]), ["github.com", "This host only", fmt(GH.added), "Remove…"]);
  assert.deepStrictEqual(cells(rows[1]), ["console.aws.amazon.com", "This host only", fmt(AWS.added), "Remove…"]);
  assert.deepStrictEqual(cells(rows[2]), ["example.org", "This host and its subdomains", fmt(EX.added), "Remove…"]);
  assert.ok(all(rows[0], "span").some((s) => s.className.split(" ").includes("mono") && s.textContent === "github.com"), "the host is mono");
  assert.deepStrictEqual(all(p.body, "th").map((t) => t.textContent), ["Host", "Covers", "Added", "Actions"]);
});

test("the hint says how a host gets here and what cannot be trusted, with the dialog's name in bold and the machine words in mono", async () => {
  const p = page();
  await p.load();
  const hint = all(p.body, "p").find((e) => e.className.split(" ").includes("lt-hint"));
  assert.strictEqual(hint.textContent, "You add a host from the Open this link? dialog. A link that carries a warning still asks, even to a trusted host. An IP address, an xn-- host and plain http cannot be trusted.");
  assert.deepStrictEqual(all(hint, "b").map((b) => b.textContent), ["Open this link?"]);
  assert.deepStrictEqual(all(hint, "span").filter((s) => s.className.includes("mono")).map((s) => s.textContent), ["xn--", "http"]);
});

test("a note under the table says what Remove does and does not", async () => {
  const p = page();
  await p.load();
  assert.ok(all(p.body, "p").some((e) => e.textContent === "Removing a host only stops links skipping the dialog. Nothing else changes."));
});

test("no trusted host: the empty line, no table, and no note under it", async () => {
  const p = page();
  p.gate.list = [];
  await p.load();
  assert.strictEqual(all(p.body, "table").length, 0);
  assert.ok(all(p.body, "p").some((e) => e.textContent === 'No trusted hosts. Tick "Always open links to …" in the Open this link? dialog to add one.'));
  assert.ok(!all(p.body, "p").some((e) => /Removing a host/.test(e.textContent)));
});

test("a damaged file says so, in place of the table and the empty line: add and remove are refused, so nothing offers either", async () => {
  const p = page();
  p.gate.list = [];
  p.gate.corrupt = true;
  await p.load();
  const line = all(p.body, "p").find((e) => e.className.split(" ").includes("lt-corrupt"));
  assert.ok(line, "an error line");
  assert.match(line.textContent, /link-trust\.json is damaged/);
  assert.strictEqual(line.getAttribute("role"), "status");
  assert.strictEqual(all(p.body, "table").length, 0);
  assert.ok(!all(p.body, "p").some((e) => /^No trusted hosts/.test(e.textContent)), "not the empty line: it would invite an add that is refused");
  assert.strictEqual(btns(p.body).length, 0);
});

test("a file from a newer CuliBox says to update, not that it is damaged or to delete it", async () => {
  const p = page();
  p.gate.list = [];
  p.gate.corrupt = true;
  p.gate.newer = true;
  await p.load();
  const line = all(p.body, "p").find((e) => e.className.split(" ").includes("lt-corrupt"));
  assert.ok(line, "a status line");
  assert.strictEqual(line.textContent, "link-trust.json was written by a newer CuliBox. Update CuliBox to change trusted hosts.");
  assert.ok(!/damaged|fixed|removed/.test(line.textContent), line.textContent);
  assert.strictEqual(line.getAttribute("role"), "status");
  assert.strictEqual(all(p.body, "table").length, 0);
  assert.strictEqual(btns(p.body).length, 0);
  // a damaged one (not newer) keeps its own words
  p.gate.newer = false;
  await p.load();
  assert.match(all(p.body, "p").find((e) => e.className.split(" ").includes("lt-corrupt")).textContent, /damaged/);
});

test("everything is text: a hostile host name is a string in a cell, never markup", async () => {
  const p = page();
  p.gate.list = [{ host: "<img src=x onerror=alert(1)>", subdomains: false, added: "" }];
  await p.load();
  const row = all(p.body, "tbody tr")[0];
  assert.strictEqual(row.children[0].textContent, "<img src=x onerror=alert(1)>");
  assert.strictEqual(all(p.body, "img").length, 0);
  assert.strictEqual(cells(row)[2], "", "a missing date is an empty cell, not a made-up day");
});

test("dates: a day this year is 'Nov 19', any other year carries its year, and a bad date is empty", async () => {
  const p = page();
  assert.strictEqual(p.lex("ltDate")(YEAR + "-11-19T12:00:00Z"), new Date(YEAR + "-11-19T12:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric" }));
  assert.doesNotMatch(p.lex("ltDate")(YEAR + "-11-19T12:00:00Z"), /\d{4}/, "no year for this year");
  assert.match(p.lex("ltDate")("2025-11-19T12:00:00Z"), /2025/, "a year for another");
  assert.match(p.lex("ltDate")((YEAR + 1) + "-02-03T12:00:00Z"), new RegExp(String(YEAR + 1)), "and for next year");
  assert.strictEqual(p.lex("ltDate")(""), "");
  assert.strictEqual(p.lex("ltDate")("not a date"), "");
});

test("a list that could not be loaded is not 'No trusted hosts': one line says so, with Retry, and no table, no empty line", async () => {
  const p = page();
  p.gate.list = []; // what lgLoadTrust leaves after a failure
  p.gate.failed = true;
  await p.load();
  const line = all(p.body, "p").find((e) => e.className.split(" ").includes("lt-loadfail"));
  assert.ok(line, "a load-failure line");
  assert.strictEqual(line.textContent, "Could not load trusted hosts. Try again.");
  assert.strictEqual(line.getAttribute("role"), "alert");
  assert.strictEqual(all(p.body, "table").length, 0);
  assert.ok(!all(p.body, "p").some((e) => /^No trusted hosts/.test(e.textContent)), "the empty line would invite an add and say there are none");
  assert.ok(!all(p.body, "p").some((e) => /Removing a host/.test(e.textContent)));
  assert.deepStrictEqual(btns(p.body).map((b) => b.textContent), ["Retry"]);
});

test("Retry asks for the list again and paints what came: the table when it loads, the same line when it does not", async () => {
  const p = page();
  p.gate.list = [];
  p.gate.failed = true;
  await p.load();
  const before = p.gate.loads;
  btns(p.body)[0].click();
  await tick();
  assert.strictEqual(p.gate.loads, before + 1);
  assert.ok(all(p.body, "p").some((e) => e.className.split(" ").includes("lt-loadfail")), "still failing: still says so");
  p.gate.list = [GH];
  p.gate.failed = false;
  btns(p.body)[0].click();
  await tick();
  assert.deepStrictEqual(all(p.body, "tbody tr").map((r) => r.children[0].textContent), ["github.com"]);
  assert.ok(!all(p.body, "p").some((e) => e.className.split(" ").includes("lt-loadfail")));
});

test("a damaged file that also failed to load says the load failed, not that the file is damaged", async () => {
  const p = page();
  p.gate.list = [];
  p.gate.corrupt = true;
  p.gate.failed = true;
  await p.load();
  assert.ok(all(p.body, "p").some((e) => e.className.split(" ").includes("lt-loadfail")));
  assert.ok(!all(p.body, "p").some((e) => e.className.split(" ").includes("lt-corrupt")));
});

test("opening the tab asks the gate for the list afresh each time", async () => {
  const p = page();
  await p.load();
  await p.load();
  assert.strictEqual(p.gate.loads, 2);
});

test("no panel in the page is no error", async () => {
  const p = page();
  p.doc.body.children.length = 0;
  await p.load();
});

// ---- Remove… ------------------------------------------------------------------------------------------------------------------

test("Remove… asks inside the row: the question across the other columns, Cancel focused, Remove beside it", async () => {
  const p = page();
  await p.load();
  btnOf(p, "console.aws.amazon.com", "arm").click();
  const row = all(p.body, "tbody tr")[1];
  assert.ok(row.className.split(" ").includes("arm"));
  assert.strictEqual(row.children[0].textContent, "console.aws.amazon.com");
  assert.strictEqual(row.children[1].textContent, "Links to console.aws.amazon.com will ask again.Cancel" + "Remove");
  assert.deepStrictEqual(btns(row).map((b) => b.textContent), ["Cancel", "Remove"]);
  assert.strictEqual(p.doc.activeElement, btnOf(p, "console.aws.amazon.com", "cancel"), "the safe button has focus, not the destructive one");
  assert.deepStrictEqual(p.gate.removed, [], "arming removes nothing");
  // the other rows are as they were
  assert.deepStrictEqual(cells(all(p.body, "tbody tr")[0]), ["github.com", "This host only", fmt(GH.added), "Remove…"]);
  const ask = all(row, "span").find((s) => s.className.split(" ").includes("lt-ask"));
  assert.strictEqual(btnOf(p, "console.aws.amazon.com", "cancel").getAttribute("aria-describedby"), ask.id, "a screen reader hears the question on both buttons");
  assert.strictEqual(btnOf(p, "console.aws.amazon.com", "go").getAttribute("aria-describedby"), ask.id);
});

test("only one row asks at a time: arming another takes the first back", async () => {
  const p = page();
  await p.load();
  btnOf(p, "github.com", "arm").click();
  btnOf(p, "example.org", "arm").click();
  assert.deepStrictEqual(all(p.body, "tbody tr").map((r) => r.className.split(" ").includes("arm")), [false, false, true]);
});

test("Cancel puts the row back and focus on its Remove…", async () => {
  const p = page();
  await p.load();
  btnOf(p, "github.com", "arm").click();
  btnOf(p, "github.com", "cancel").click();
  assert.strictEqual(all(p.body, "tr").filter((r) => r.className.split(" ").includes("arm")).length, 0);
  assert.strictEqual(p.doc.activeElement, btnOf(p, "github.com", "arm"));
  assert.deepStrictEqual(p.gate.removed, []);
});

test("Remove calls the gate's remove with the host, repaints without the row, and focus moves to the next row's Remove…", async () => {
  const p = page();
  await p.load();
  btnOf(p, "console.aws.amazon.com", "arm").click();
  btnOf(p, "console.aws.amazon.com", "go").click();
  await tick();
  assert.deepStrictEqual(p.gate.removed, ["console.aws.amazon.com"]);
  assert.deepStrictEqual(all(p.body, "tbody tr").map((r) => r.children[0].textContent), ["github.com", "example.org"]);
  assert.strictEqual(p.doc.activeElement, btnOf(p, "example.org", "arm"));
});

test("removing the last row lands focus on the previous one, and removing the only one on the empty state's card", async () => {
  const p = page();
  await p.load();
  btnOf(p, "example.org", "arm").click();
  btnOf(p, "example.org", "go").click();
  await tick();
  assert.strictEqual(p.doc.activeElement, btnOf(p, "console.aws.amazon.com", "arm"));
  p.gate.list = [GH];
  await p.load();
  btnOf(p, "github.com", "arm").click();
  btnOf(p, "github.com", "go").click();
  await tick();
  assert.strictEqual(all(p.body, "table").length, 0);
  assert.ok(all(p.body, "p").some((e) => /^No trusted hosts/.test(e.textContent)));
  assert.ok(p.doc.activeElement && p.body.contains(p.doc.activeElement), "focus stays in the page");
});

test("a second click while Remove is in flight does not send a second request", async () => {
  const p = page();
  let release;
  p.gate.hold = new Promise((r) => { release = r; });
  await p.load();
  btnOf(p, "github.com", "arm").click();
  const go = btnOf(p, "github.com", "go");
  go.click();
  go.click();
  release();
  await tick();
  assert.deepStrictEqual(p.gate.removed, ["github.com"]);
});

test("a failed Remove shows the server's own sentence in an alert line, puts the row back, and the list is asked for again", async () => {
  const p = page();
  await p.load();
  p.gate.failWith = { message: "link-trust.json is damaged; fix or remove it first", status: 409, code: "corrupt" };
  const before = p.gate.loads;
  btnOf(p, "github.com", "arm").click();
  btnOf(p, "github.com", "go").click();
  await tick();
  const line = all(p.body, "p").find((e) => e.className.split(" ").includes("lt-err"));
  assert.ok(line, "an error line");
  assert.strictEqual(line.textContent, "Could not remove github.com: link-trust.json is damaged; fix or remove it first");
  assert.strictEqual(line.getAttribute("role"), "alert");
  assert.strictEqual(all(p.body, "tr").filter((r) => r.className.split(" ").includes("arm")).length, 0, "nothing is left asking");
  assert.ok(btnOf(p, "github.com", "arm"), "the row is still there");
  assert.ok(p.gate.loads > before, "reloaded: what the server holds is what shows");
  assert.strictEqual(p.doc.activeElement, btnOf(p, "github.com", "arm"));
});

test("a failure with no message still says something, and the next successful action clears the line", async () => {
  const p = page();
  await p.load();
  p.gate.failWith = { message: "", status: 500 };
  btnOf(p, "github.com", "arm").click();
  btnOf(p, "github.com", "go").click();
  await tick();
  assert.strictEqual(all(p.body, "p").find((e) => e.className.split(" ").includes("lt-err")).textContent, "Could not remove github.com: request failed");
  p.gate.failWith = null;
  btnOf(p, "github.com", "arm").click();
  assert.ok(!all(p.body, "p").some((e) => e.className.split(" ").includes("lt-err")), "arming again clears the old failure");
  btnOf(p, "github.com", "go").click();
  await tick();
  assert.ok(!all(p.body, "p").some((e) => e.className.split(" ").includes("lt-err")));
});

test("leaving the tab and coming back starts clean: nothing armed, no old error", async () => {
  const p = page();
  await p.load();
  btnOf(p, "github.com", "arm").click();
  await p.load();
  assert.strictEqual(all(p.body, "tr").filter((r) => r.className.split(" ").includes("arm")).length, 0);
});

test("every part of the table has its explicit role, for the narrow stack that drops the native ones", async () => {
  const p = page();
  await p.load();
  const t = all(p.body, "table")[0];
  assert.strictEqual(t.getAttribute("role"), "table");
  assert.strictEqual(t.getAttribute("aria-label"), "Trusted hosts");
  assert.ok(all(p.body, "tr").every((r) => r.getAttribute("role") === "row"));
  assert.ok(all(p.body, "td").every((r) => r.getAttribute("role") === "cell"));
  assert.ok(all(p.body, "th").every((r) => r.getAttribute("role") === "columnheader"));
});

// ---- the page's wiring ----------------------------------------------------------------------------------------------------------

test("linktrust.js is a classic script with lt/LT_ names, and loads in node with no browser", () => {
  const src = read("linktrust.js");
  const names = [...src.matchAll(/^(?:async\s+)?(?:function\s+|const\s+|let\s+)([A-Za-z_$][\w$]*)/gm)].map((m) => m[1]);
  assert.ok(names.length > 5);
  for (const n of names) assert.ok(n === "loadLinkTrust" || /^(?:lt[A-Z]|LT_)/.test(n), n);
  assert.doesNotThrow(() => require("../static/linktrust.js"));
});

test("index.html: the tab button, the panel and the script, after wsroots.js; the nav puts it between Appearance and About", () => {
  const html = read("index.html");
  assert.match(html, /<button type="button" class="t" data-tab="linktrust" role="tab" aria-selected="false" aria-controls="linktrust">(?:<svg[^>]*><\/svg>)?Trusted hosts<\/button>/);
  assert.match(html, /<div class="dbd pad" id="linktrust" role="tabpanel" hidden>\s*<div class="set-head"><\/div>\s*<div id="lt-body"><\/div>\s*<\/div>/);
  assert.ok(html.indexOf('src="/linktrust.js"') > html.indexOf('src="/wsroots.js"'), "after wsroots.js");
  assert.ok(html.indexOf('src="/linktrust.js"') > html.indexOf('src="/linkgate.js"'), "and after the gate it asks");
  assert.ok(html.indexOf('data-tab="linktrust"') < html.indexOf('data-tab="about"'));
  assert.ok(html.indexOf('data-tab="linktrust"') < html.indexOf('id="set-signout"'));
});

test("the head, the tab hook and the phone: Settings names the page with no action, selecting the tab loads the list, the phone hides the tab", () => {
  const { settingsHeadSpec } = require("../static/settingshead.js");
  assert.deepStrictEqual(settingsHeadSpec("linktrust", {}), {
    title: "Trusted hosts",
    desc: "Links an agent shows open without the confirm dialog when they go to one of these hosts.",
  });
  assert.match(read("shell.js"), /name === "linktrust" && typeof loadLinkTrust === "function"\) loadLinkTrust\(\)/);
  assert.match(read("app.css"), /\.tabs \.t\[data-tab="linktrust"\] \{ display: none; \}/);
});

test("app.css carries a plain base for the link dialog's parts, so the phone viewer's dialog reads without desktop.css", () => {
  const css = read("app.css");
  for (const sel of [".lk-url", ".lk-host", ".lk-meta", ".lk-warn", ".lk-ico", ".lk-trust", ".lk-check"]) {
    assert.ok(css.includes("#ask-modal " + sel), sel + " has a base rule");
  }
  assert.match(css, /#ask-modal \.lk-url \{[^}]*font: [^}]*ui-monospace[^}]*overflow-wrap: anywhere;/, "the URL is mono and wraps");
  assert.match(css, /#ask-modal \.lk-warn \{[^}]*border-left: 3px solid var\(--warn\); background: var\(--warn-tint\);/, "the warning row's amber rule");
  assert.doesNotMatch(css.slice(css.indexOf("#ask-modal .lk-url"), css.indexOf("#ask-modal .lk-sub[hidden]")), /\bwidth:/, "no width: desktop.css sizes the card");
  assert.match(css, /#ask-modal \.lk-sub \{[^}]*color: var\(--muted\);/, "the subdomains row is muted on the phone too, as on desktop");
});

// askDialog puts opts.cls on #ask-modal itself. It used to be "link", and the kit's .link (desktop.css: accent text, pointer cursor,
// underline on hover) then styled the WHOLE dialog: title, buttons and notes blue and underlined under the mouse. The dialog's classes are
// lk-ask and lk-ask-warn, one token each (askDialog's classList.toggle throws on a space), and a name the stylesheets use for something else is
// not one of them.
test("the classes linkgate gives askDialog are its own: not the kit's .link, one token each, styled only under #ask-modal", () => {
  const src = read("linkgate.js");
  const m = src.match(/cls: warned \? "([^"]*)" : "([^"]*)"/);
  assert.ok(m, "linkgate.js passes cls: warned ? ... : ...");
  const [, warnCls, cleanCls] = m;
  assert.deepStrictEqual([cleanCls, warnCls], ["lk-ask", "lk-ask-warn"]);
  for (const c of [cleanCls, warnCls]) {
    assert.ok(!/\s/.test(c), c + " is one token");
    assert.notStrictEqual(c, "link");
  }
  for (const f of ["desktop.css", "app.css"]) {
    const css = read(f).replace(/\/\*[\s\S]*?\*\//g, "");
    for (const c of [cleanCls, warnCls]) {
      // every use of the class is on #ask-modal itself: never a bare .lk-ask rule that could reach another element
      const re = new RegExp("\\." + c + "(?![\\w-])", "g");
      let hit;
      while ((hit = re.exec(css))) assert.strictEqual(css.slice(hit.index - "#ask-modal".length, hit.index), "#ask-modal", f + ": ." + c + " outside #ask-modal");
    }
    // and no rule for the card itself behaves like a link: no pointer cursor, no underline
    for (const r of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      if (!/\.lk-ask(?![\w])/.test(r[1]) && !/\.lk-ask-warn/.test(r[1])) continue;
      assert.doesNotMatch(r[2], /cursor\s*:\s*pointer|text-decoration/, f + ": " + r[1].trim());
    }
  }
  // the old names are gone from both sheets
  for (const f of ["desktop.css", "app.css"]) assert.doesNotMatch(read(f), /#ask-modal\.link\b/, f);
  // with the kit class gone the card's text is the dialog's own --fg (app.css `dialog`), so the label needs no colour of its own
  assert.doesNotMatch(read("desktop.css"), /\.lk-trust > \.lk-check \{/);
});
