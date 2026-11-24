"use strict";
// activitytab.js: the view's lifecycle against a minimal fake DOM (the repo has no
// jsdom). Covers what the pure model cannot: a view re-targeted to another session
// must not keep the previous session's reply box.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");

const { Node, Text } = require("./fakedom.js");

// The page's Date, with "now" fixed (see NOW below); every other Date behaves as usual.
class PinnedDate extends Date {
  constructor(...a) { if (a.length === 0) super(PINNED_NOW); else super(...a); }
  static now() { return PINNED_NOW; }
}
const PINNED_NOW = new Date(2026, 10, 9, 12, 0, 0).getTime();

// One page: scripts run as classic scripts in a shared global scope, like the browser.
function page() {
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  doc.createElementNS = (_ns, t) => new Node(t, doc);
  const box = {
    document: doc, window: { getSelection: () => null }, console, AbortController, Promise, JSON, Date: PinnedDate,
    lastSessions: [], paneViewRenderers: {}, replyFocused: false, replyDraft: null, repliesEnabled: true,
    composers: [],
    fetch: async () => ({ status: 200, json: async () => ({ claude: true, events: [], more: false, files: [], summary: {} }) }),
    api: async () => ({ tools: [] }),
    terminalTabFor: () => null,
    maskHint: () => "",
    // Stand-ins for app.js: the composer is bound to the session object it was built for.
    buildCardComposer(s) {
      const form = doc.createElement("div");
      form.appendChild(doc.createElement("input"));
      const c = { form, why: doc.createElement("span"), refresh() {}, sid: s.id };
      box.composers.push(c);
      return c;
    },
    buildEnableRepliesButton() { return doc.createElement("button"); },
  };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["icons.js", "svgicon.js", "iconmap.js", "attribution.js", "duration.js", "activitymodel.js", "activitytab.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  return { box, doc, host: doc.createElement("div") };
}
const tick = () => new Promise((r) => setImmediate(r));
const claude = (id, over) => Object.assign({ id, claude: true, summary: "said " + id, replyable: true, activity: "idle" }, over);

test("a mounted Claude view shows the latest message and a reply box", async () => {
  const { box, host } = page();
  box.lastSessions = [claude("s1")];
  box.paneViewRenderers.activity(host, "s1");
  await tick();
  assert.match(host.textContent, /Latest message/);
  assert.match(host.textContent, /said s1/);
  assert.strictEqual(host.querySelectorAll("input").length, 1);
  assert.strictEqual(box.composers.length, 1);
});

test("re-targeted to a non-Claude session: no stale message, reply box or filter bar", async () => {
  const { box, host } = page();
  box.lastSessions = [claude("s1"), { id: "s2", claude: false }];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  assert.strictEqual(host.querySelectorAll("input").length, 1);
  h.update("s2");
  await tick();
  assert.strictEqual(host.querySelectorAll("input").length, 0, "the old session's reply box is gone");
  assert.doesNotMatch(host.textContent, /Latest message|said s1/);
  assert.strictEqual(host.querySelectorAll(".seg-item").length, 0);
  assert.match(host.textContent, /Activity is for Claude sessions/);
  // And back: a fresh box, built for the session now shown.
  h.update("s1");
  await tick();
  assert.strictEqual(host.querySelectorAll("input").length, 1);
  assert.strictEqual(box.composers[box.composers.length - 1].sid, "s1");
});

test("re-targeted to another Claude session: one reply box, bound to the new session", async () => {
  const { box, host } = page();
  box.lastSessions = [claude("s1"), claude("s2")];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  h.update("s2");
  await tick();
  assert.strictEqual(host.querySelectorAll("input").length, 1);
  assert.strictEqual(box.composers[box.composers.length - 1].sid, "s2");
  assert.match(host.textContent, /said s2/);
  assert.doesNotMatch(host.textContent, /said s1/);
});

test("a session that leaves the list takes its reply box with it", async () => {
  const { box, host } = page();
  box.lastSessions = [claude("s1")];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  box.lastSessions = [claude("zz")];
  h.update("s1");
  assert.strictEqual(host.querySelectorAll("input").length, 0);
  assert.match(host.textContent, /no longer listed/);
});

test("removing a focused reply box releases the poll's reply-focus hold", async () => {
  const { box, doc, host } = page();
  box.lastSessions = [claude("s1"), { id: "s2", claude: false }];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  host.querySelector("input").focus();
  box.replyFocused = true;
  h.update("s2");
  assert.strictEqual(box.replyFocused, false);
  assert.ok(doc.activeElement);
});

test("the New session dialog fits the viewport and keeps its action row pinned", () => {
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#spawn-modal\s*\{[^}]*max-height:\s*calc\(100vh - 32px\)/);
  // Pinned by being outside the form's scroll box (frame M4.1, #173): out of flow on the card's bottom edge, with the
  // body (a fixed min(640px, ...) tall) keeping its band free. It was sticky inside the form before.
  assert.match(css, /#spawn \.spawn-actions\s*\{[^}]*position:\s*absolute;[^}]*bottom:\s*0/);
  assert.match(css, /#spawn-modal \.dlg-body\s*\{[^}]*height:\s*min\(640px,[^}]*margin-bottom:\s*var\(--ns-foot\)/);
});

// ---- the view on the kit (M6.1, M6.2; #176) ---------------------------------------------------------
// Fixtures use made-up ids and names. Times are relative to now, so a row reads as today's clock.
// A fixed mid-day instant, and the page's clock pinned to it (PinnedDate above): rows up to 90 s old must stay on the same
// day as "now", or the HH:MM:SS assert flakes within 90 s of local midnight (the model compares each row's day with the clock's).
const NOW = PINNED_NOW;
const ago = (s) => NOW - s * 1000;
const okFetch = (reply) => async () => ({ status: 200, json: async () => reply });
const REPLY = (over) => Object.assign({
  claude: true, more: false, partial: false, waiting_since: 0,
  summary: { tool_calls: 46, files_changed: 3, credentials: 2, running_s: 2 * 86400 + 5 * 3600 },
  files: [{ path: "infra/alarms.tf", add: 18, del: 2 }, { path: "infra/variables.tf", add: 6, del: 0 }, { path: "README.md", add: 3, del: 1 }],
  events: [
    { ts: ago(10), kind: "tool", tool: "Bash", detail: "terraform plan -out=tf.plan -var-file=prod.tfvars", state: "pending" },
    { ts: ago(20), kind: "tool", tool: "Edit", detail: "infra/alarms.tf", state: "ok", add: 18, del: 2 },
    { ts: ago(30), kind: "tool", tool: "Read", detail: "infra/alarms.tf", state: "ok", lines: 142 },
    { ts: ago(40), kind: "credential", detail: "aws culipulse-prod", state: "minted" },
    { ts: ago(50), kind: "tool", tool: "Bash", detail: "aws cloudwatch describe-alarms --alarm-name-prefix api-", state: "ok", ms: 2100 },
    { ts: ago(60), kind: "tool", tool: "Bash", detail: "go test ./...", state: "error", exit: 1 },
    { ts: ago(70), kind: "credential", detail: "aws agent-ops-read", state: "refused" },
    { ts: ago(80), kind: "you", detail: "Add a p99 latency alarm for the API and plan it against prod." },
    { ts: ago(90), kind: "clear" },
  ],
}, over);
const classes = (n) => n.className.split(/\s+/).filter(Boolean);
const rowsOf = (host) => host.querySelectorAll("table.tbl.act-tbl tbody tr");
const cellsOf = (tr) => tr.querySelectorAll("td");
const rowFor = (host, detail) => {
  const tr = rowsOf(host).find((r) => r.title === detail);
  assert.ok(tr, "a row for " + detail);
  return tr;
};
// A mounted, loaded Claude view.
async function claudeView(over, sess, reply) {
  const p = page();
  p.box.fetch = okFetch(reply || REPLY());
  p.box.lastSessions = [claude("s1", sess)];
  Object.assign(p.box, over || {});
  p.handle = p.box.paneViewRenderers.activity(p.host, "s1");
  await tick();
  return p;
}

test("Activity draws the kit KPI row: five .kpi with .kpi-value and .kpi-label", async () => {
  const { host } = await claudeView();
  const row = host.querySelector(".kpi-row");
  assert.ok(row, "the KPI row");
  const tiles = row.querySelectorAll(".kpi");
  assert.strictEqual(tiles.length, 5);
  assert.deepStrictEqual(
    tiles.map((t) => [t.querySelector(".kpi-value").textContent, t.querySelector(".kpi-label").textContent]),
    [["46", "Tool calls"], ["3", "Files changed"], ["2", "Credentials"], ["0", "Waiting for you"], ["2d 5h", "Session age"]],
  );
  assert.ok(tiles.every((t) => !classes(t.querySelector(".kpi-value")).includes("is-bad")), "no value is red");
});

test("the KPI values read — until the first reply, never a fake 0", () => {
  const { box, host } = page();
  box.fetch = () => new Promise(() => {}); // the reply never comes
  box.lastSessions = [claude("s1")];
  box.paneViewRenderers.activity(host, "s1");
  assert.deepStrictEqual(host.querySelectorAll(".kpi-value").map((v) => v.textContent), ["—", "—", "—", "—", "—"]);
});

test("the view is the two-column grid: main (KPI row, message, filter, table) beside the side column", async () => {
  const { host } = await claudeView();
  const wrap = host.querySelector(".act-wrap");
  assert.ok(wrap);
  assert.deepStrictEqual(wrap.children.map((c) => classes(c)[0]), ["act-main", "act-side"]);
  const main = wrap.children[0];
  assert.ok(main.querySelector(".kpi-row") && main.querySelector(".act-filter") && main.querySelector("table.tbl.act-tbl"));
  assert.ok(!wrap.children[1].querySelector(".kpi-row"));
});

test("the latest message is a kit card: a group label, then the text on the 13px line", async () => {
  const { host } = await claudeView();
  const card = host.querySelector("section.card.act-msg");
  assert.ok(card);
  assert.strictEqual(card.querySelector(".grp-label").textContent, "Latest message");
  const p = card.querySelector("p.t-md");
  assert.ok(p, "the text is a p.t-md");
  assert.strictEqual(p.textContent, "said s1");
  assert.strictEqual(host.querySelectorAll("input").length, 1, "the reply box stays");
});

test("the filter is a .seg of five .seg-item buttons with aria-pressed; pressing one keeps focus on it", async () => {
  const { doc, host } = await claudeView();
  const seg = host.querySelector(".act-filter.seg");
  assert.ok(seg, "the filter group");
  assert.strictEqual(seg.getAttribute("role"), "group");
  assert.strictEqual(seg.getAttribute("aria-label"), "Filter");
  const items = seg.querySelectorAll(".seg-item");
  assert.deepStrictEqual(items.map((b) => b.textContent), ["All", "Commands", "Files", "Cloud", "You"]);
  assert.ok(items.every((b) => b.tag === "button" && b.type === "button"));
  assert.deepStrictEqual(items.map((b) => b.getAttribute("aria-pressed")), ["true", "false", "false", "false", "false"]);
  assert.ok(items.every((b) => !classes(b).includes("on")), "the pressed look is aria-pressed alone");

  assert.strictEqual(rowsOf(host).length, 9);
  items[1].focus();
  items[1].click();
  assert.strictEqual(doc.activeElement, items[1], "focus stays on the pressed item");
  assert.ok(seg.contains(items[1]), "the item was not rebuilt");
  assert.deepStrictEqual(items.map((b) => b.getAttribute("aria-pressed")), ["false", "true", "false", "false", "false"]);
  assert.deepStrictEqual(rowsOf(host).map((r) => r.title), [
    "terraform plan -out=tf.plan -var-file=prod.tfvars", "aws cloudwatch describe-alarms --alarm-name-prefix api-", "go test ./...",
  ], "Commands shows the Bash rows");
});

test('"Live · newest first", and "Could not refresh · newest first" when stale', async () => {
  const { box, host, handle } = await claudeView();
  const live = host.querySelector(".act-live");
  assert.ok(live);
  assert.strictEqual(live.textContent, "Live · newest first");
  assert.ok(classes(live.querySelector(".dot")).includes("dot-run"), "only the dot is green");
  assert.ok(classes(live).includes("muted"), "the words are muted");
  box.fetch = async () => ({ status: 500 });
  handle.update();
  await tick();
  assert.strictEqual(live.textContent, "Could not refresh · newest first");
  assert.ok(!classes(live.querySelector(".dot")).includes("dot-run"), "a stale read is not a green dot");
  assert.ok(classes(live.querySelector(".dot")).includes("dot-idle"));
  assert.ok(host.querySelector(".act-filter") && host.querySelector(".act-live") === live, "the bar was not rebuilt");
});

// G3-D5: a failure before anything loaded used to keep the green dot and "Live", beside a table that said it could not read.
test("a first read that failed is not Live: the bar says Could not refresh with the muted dot, and recovers when a read lands (G3-D5)", async () => {
  const p = page();
  p.box.fetch = async () => ({ status: 500 });
  p.box.lastSessions = [claude("s1")];
  const handle = p.box.paneViewRenderers.activity(p.host, "s1");
  await tick();
  const live = p.host.querySelector(".act-live");
  assert.ok(live);
  assert.match(p.host.querySelector("table.tbl.act-tbl").textContent, /Could not read the activity\./, "the table already says it could not read");
  assert.strictEqual(live.textContent, "Could not refresh · newest first", "the shipped stale label, no new copy");
  assert.ok(classes(live.querySelector(".dot")).includes("dot-idle"), "the muted dot");
  assert.ok(!classes(live.querySelector(".dot")).includes("dot-run"), "never the green one");
  p.box.fetch = okFetch(REPLY());
  handle.update();
  await tick();
  assert.strictEqual(live.textContent, "Live · newest first");
  assert.ok(classes(live.querySelector(".dot")).includes("dot-run"));
  assert.ok(p.host.querySelector(".act-live") === live, "the bar was not rebuilt");
});

// Task 5 minor: an MCP tool's kind (`mcp__server__tool`) is far wider than the 80px Kind cell and ellipsizes there.
test("the Kind cell carries the full kind in its title, so an ellipsized MCP tool name can be read", async () => {
  const kind = "mcp__culipulse__describe_alarms";
  const reply = REPLY({ events: [
    { ts: ago(10), kind: "tool", tool: kind, detail: "api-p99", state: "ok" },
    { ts: ago(20), kind: "tool", tool: "Edit", detail: "infra/alarms.tf", state: "ok", add: 1, del: 0 },
    { ts: ago(30), kind: "you", detail: "go on" },
    { ts: ago(40), kind: "credential", detail: "aws culipulse-prod", state: "minted" },
  ] });
  const { host } = await claudeView(null, null, reply);
  const rows = rowsOf(host);
  assert.strictEqual(rows.length, 4);
  for (const tr of rows) {
    const k = cellsOf(tr)[1];
    assert.ok(k.title, "every Kind cell has a title: " + k.textContent);
    assert.strictEqual(k.title, k.textContent, "the title is the kind, whole");
  }
  assert.strictEqual(cellsOf(rows[0])[1].title, kind);
  assert.strictEqual(rows[0].title, "api-p99", "the row's own title (the detail) is kept");
});

test("events are a table.tbl with Time / Kind / Detail / Outcome heads and no row is a button", async () => {
  const { host } = await claudeView();
  const table = host.querySelector("table.tbl.act-tbl");
  assert.ok(table);
  const heads = table.querySelectorAll("thead th");
  assert.deepStrictEqual(heads.map((h) => h.textContent), ["Time", "Kind", "Detail", "Outcome"]);
  assert.deepStrictEqual(heads.map((h) => classes(h).includes("num")), [false, false, false, true], "Outcome is right-aligned over its cells");
  assert.ok(table.parent && classes(table.parent).includes("card"), "the table sits in a card");
  const rows = rowsOf(host);
  assert.strictEqual(rows.length, 9);
  for (const r of rows) {
    assert.strictEqual(cellsOf(r).length, 4);
    assert.strictEqual(r.getAttribute("role"), null, "a row has no button role");
    assert.strictEqual(r.getAttribute("aria-expanded"), null);
    assert.ok(r.tabIndex < 0, "a row is not focusable");
    assert.ok(!r.onclick && !r.onkeydown, "a row takes no click or key");
  }
  assert.strictEqual(table.querySelectorAll("button").length, 0);
  assert.strictEqual(rows[0].title, "terraform plan -out=tf.plan -var-file=prod.tfvars", "title keeps the full text");
});

test("a row wraps as drawn: Time sub tnum, Kind sub, Detail wraps in mono t-sm, You is sans, Cleared is muted", async () => {
  const { host } = await claudeView();
  const bash = cellsOf(rowFor(host, "terraform plan -out=tf.plan -var-file=prod.tfvars"));
  assert.deepStrictEqual([classes(bash[0]).join(" "), classes(bash[1]).join(" ")], ["sub tnum", "sub"]);
  assert.strictEqual(bash[1].textContent, "Bash");
  assert.ok(classes(bash[2]).includes("act-det"), "the detail cell is the wrapping one");
  const code = bash[2].querySelector("span");
  assert.deepStrictEqual(classes(code), ["mono", "t-sm"]);
  assert.strictEqual(code.textContent, "terraform plan -out=tf.plan -var-file=prod.tfvars");
  assert.match(bash[0].textContent, /^\d\d:\d\d:\d\d$/);

  const you = cellsOf(rowFor(host, "Add a p99 latency alarm for the API and plan it against prod."));
  assert.deepStrictEqual([classes(you[1]).join(" "), you[1].textContent], ["w-500", "You"], "You's Kind is fg 500");
  assert.deepStrictEqual(classes(you[2].querySelector("span")), ["w-500"], "You's Detail is sans, not mono");
  assert.strictEqual(you[3].textContent, "message");
  assert.deepStrictEqual(classes(you[3]), ["num", "sub"]);

  const clr = cellsOf(rowFor(host, "conversation cleared"));
  assert.strictEqual(clr[1].textContent, "Cleared");
  assert.deepStrictEqual(classes(clr[2].querySelector("span")), ["muted"]);
  assert.strictEqual(clr[3].textContent, "");
  assert.deepStrictEqual(classes(clr[3]), ["num", "sub"]);
});

test("outcome cells carry their tone class: c-ok minted, c-red error · exit 1, muted refused, amber ask", async () => {
  const { host } = await claudeView();
  const out = (detail) => cellsOf(rowFor(host, detail))[3];
  const minted = out("aws culipulse-prod");
  assert.deepStrictEqual([minted.textContent, classes(minted)], ["minted", ["num", "c-ok"]]);
  const ok = out("aws cloudwatch describe-alarms --alarm-name-prefix api-");
  assert.deepStrictEqual([ok.textContent, classes(ok)], ["ok · 2.1s", ["num", "c-ok"]]);
  const err = out("go test ./...");
  assert.deepStrictEqual([err.textContent, classes(err)], ["error · exit 1", ["num", "c-red"]]);
  const refused = out("aws agent-ops-read");
  assert.deepStrictEqual([refused.textContent, classes(refused)], ["refused", ["num", "sub"]], "a refusal is muted, never red");
  const running = out("terraform plan -out=tf.plan -var-file=prod.tfvars");
  assert.deepStrictEqual([running.textContent, classes(running)], ["running", ["num", "sub"]]);
  const lines = out("infra/alarms.tf");
  assert.ok(lines.textContent === "+18 −2" || lines.textContent === "142 lines");
  const diff = rowsOf(host).map((r) => cellsOf(r)[3]).find((c) => c.textContent === "+18 −2");
  assert.deepStrictEqual(classes(diff), ["num"], "a diff has no tone");

  const asking = await claudeView(null, { need: "ask", activity: "waiting", ask_full: "Bash(rm -rf build)" });
  const ask = cellsOf(rowsOf(asking.host)[0]);
  assert.strictEqual(ask[1].textContent, "Asks to run");
  assert.deepStrictEqual(classes(ask[3]).slice(0, 2), ["num", "c-amber"]);
  assert.ok(classes(ask[1]).includes("c-amber"));
  const others = rowsOf(asking.host).slice(1).flatMap((r) => cellsOf(r).map(classes).flat());
  assert.ok(!others.includes("c-amber"), "the ask row is the only amber");
});

test("an empty list says why inside the table, and a failed read says so", async () => {
  const none = await claudeView(null, null, REPLY({ events: [] }));
  assert.match(rowsOf(none.host)[0].textContent, /No activity yet\./);
  const { box, host } = page();
  box.fetch = async () => ({ status: 500 });
  box.lastSessions = [claude("s1")];
  box.paneViewRenderers.activity(host, "s1");
  await tick();
  assert.match(rowsOf(host)[0].textContent, /Could not read the activity\./);
});

test("Load older is a .btn.btn-sm", async () => {
  const { host } = await claudeView(null, null, REPLY({ more: true }));
  const b = host.querySelector(".act-foot .btn");
  assert.ok(b);
  assert.deepStrictEqual(classes(b), ["btn", "btn-sm"]);
  assert.strictEqual(b.textContent, "Load older");
  const none = await claudeView();
  assert.strictEqual(none.host.querySelector(".act-foot").hidden, true, "nothing to load, nothing in the foot");
});

test("Load older keeps keyboard focus while it loads and after, and hands focus to the view when the history ends", async () => {
  for (const more of [true, false]) {
    let release;
    const gate = new Promise((r) => { release = r; });
    const { box, doc, host } = page();
    box.lastSessions = [claude("s1")];
    box.fetch = async (url) => {
      if (/before=/.test(url)) {
        await gate;
        return { status: 200, json: async () => REPLY({ more, events: [{ ts: ago(500), kind: "tool", tool: "Read", detail: "old.tf", state: "ok", lines: 3 }] }) };
      }
      return { status: 200, json: async () => REPLY({ more: true }) };
    };
    box.paneViewRenderers.activity(host, "s1");
    await tick();
    const b = host.querySelector(".act-foot .btn");
    b.focus();
    b.click();
    assert.strictEqual(doc.activeElement, b, "focus is on the button while it loads");
    assert.strictEqual(b.textContent, "Loading…");
    assert.strictEqual(b.getAttribute("aria-disabled"), "true", "busy, but still focusable: not [disabled]");
    assert.strictEqual(b.disabled, false);
    b.click(); // a second press while loading is ignored
    release();
    await tick(); await tick();
    assert.strictEqual(rowsOf(host).length, 10, "the older page was appended (once)");
    if (more) {
      assert.ok(host.querySelector(".act-foot .btn") === b && !b.hidden, "the same button is still there");
      assert.strictEqual(doc.activeElement, b);
      assert.strictEqual(b.textContent, "Load older");
      assert.strictEqual(b.getAttribute("aria-disabled"), null);
    } else {
      assert.ok(b.hidden || !host.querySelector(".act-foot .btn"), "no older history: the button goes");
      assert.strictEqual(doc.activeElement, host, "focus went to the view (tabindex -1), not <body>");
    }
  }
});

test("the side column has Context, Context by tool (Scan) and Files changed cards", async () => {
  const tools = { tools: [
    { name: "Read", calls: 20, ctxBytes: 41000, kind: "tool" }, { name: "Bash", calls: 11, ctxBytes: 27000, kind: "tool" },
    { name: "WebFetch", calls: 4, ctxBytes: 14000, kind: "tool" }, { name: "Grep", calls: 6, ctxBytes: 9000, kind: "tool" },
    { name: "Edit", calls: 5, ctxBytes: 6000, kind: "tool" }, { name: "Glob", calls: 2, ctxBytes: 3000, kind: "tool" },
  ] };
  const { host } = await claudeView({
    api: async () => tools,
    terminalTabFor: () => ({ usage: () => ({ pct: 61, contextTokens: 122000, contextMax: 200000 }) }),
  });
  await tick();
  const side = host.querySelector(".act-side");
  assert.ok(side);
  const cards = side.querySelectorAll("section.card");
  assert.strictEqual(cards.length, 3);
  assert.deepStrictEqual(cards.map((c) => c.querySelector(".card-head h2").textContent), ["Context", "Context by tool", "Files changed"]);
  assert.ok(cards.every((c) => classes(c.querySelector("h2")).join(" ") === "t-md w-600"));
  // Context: the share beside the title, one meter, nothing else.
  const ctx = cards[0];
  assert.strictEqual(ctx.querySelector(".card-head .t-sm.muted.tnum").textContent, "61% of 200k");
  assert.ok(ctx.querySelector(".card-head .ph-spacer"));
  assert.strictEqual(ctx.querySelector(".meter i").style.width, "61%");
  // Context by tool: Scan in the head, a row per tool, the proxy note.
  const scan = cards[1].querySelector(".card-head button");
  assert.deepStrictEqual([scan.textContent, classes(scan)], ["Scan", ["btn", "btn-sm"]]);
  const trows = cards[1].querySelectorAll(".act-trow");
  assert.deepStrictEqual(trows.slice(0, 2).map((r) => r.children.map((c) => c.textContent)), [["Read", "", "41%"], ["Bash", "", "27%"]]);
  assert.deepStrictEqual(classes(trows[0].children[0]), ["mono", "t-sm", "ellipsis"]);
  assert.deepStrictEqual(classes(trows[0].children[2]), ["t-sm", "tnum"]);
  assert.strictEqual(trows[0].querySelector(".meter i").style.width, "41%");
  assert.match(cards[1].textContent, /Share of tool-result bytes: a proxy for context pressure, not billing\./);
  // Files changed: the count, then path and delta (no tone) per file.
  const files = cards[2];
  assert.strictEqual(files.querySelector(".card-head .t-sm.muted.tnum").textContent, "3");
  const frows = files.querySelectorAll(".act-frow");
  assert.deepStrictEqual(frows.map((r) => r.children.map((c) => c.textContent)), [
    ["infra/alarms.tf", "+18 −2"], ["infra/variables.tf", "+6 −0"], ["README.md", "+3 −1"],
  ]);
  assert.deepStrictEqual(classes(frows[0].children[0]), ["mono", "t-sm", "ellipsis"]);
  assert.deepStrictEqual(classes(frows[0].children[1]), ["t-sm", "tnum"]);
});

test("without a context reading the Context card is left out, and the scan button says Scanning… while it runs", async () => {
  let done;
  const p = page();
  p.box.fetch = okFetch(REPLY());
  p.box.api = () => new Promise((r) => { done = r; });
  p.box.lastSessions = [claude("s1")];
  p.box.paneViewRenderers.activity(p.host, "s1");
  await tick();
  const heads = p.host.querySelectorAll(".act-side .card-head h2").map((h) => h.textContent);
  assert.deepStrictEqual(heads, ["Context by tool", "Files changed"]);
  const scan = p.host.querySelector(".act-side .card-head button");
  assert.strictEqual(scan.textContent, "Scanning…");
  done({ tools: [] });
  await tick();
  assert.strictEqual(p.host.querySelector(".act-side .card-head button").textContent, "Scan");
});

test("Scan keeps the keyboard while the card is rebuilt for the scan, and a second press is ignored", async () => {
  const { box, doc, host } = await claudeView({ api: async () => ({ tools: [] }) });
  await tick();
  const first = host.querySelector(".act-side .card-head button");
  assert.strictEqual(first.textContent, "Scan");
  let asked = 0;
  box.api = () => { asked++; return new Promise(() => {}); };
  first.focus();
  first.click();
  const busy = host.querySelector(".act-side .card-head button");
  assert.strictEqual(busy.textContent, "Scanning…");
  assert.strictEqual(doc.activeElement, busy, "the keyboard is on the rebuilt button");
  assert.strictEqual(busy.getAttribute("aria-disabled"), "true");
  assert.strictEqual(busy.disabled, false);
  busy.click();
  assert.strictEqual(asked, 1, "one scan at a time");
});

test("before the first reply the table says Loading…, and the Files card shows — rather than a 0 or an empty list", () => {
  const { box, host } = page();
  box.fetch = () => new Promise(() => {});
  box.lastSessions = [claude("s1")];
  box.paneViewRenderers.activity(host, "s1");
  assert.match(rowsOf(host)[0].textContent, /Loading…/);
  const files = host.querySelectorAll(".act-side section.card")[1];
  assert.strictEqual(files.querySelector(".card-head h2").textContent, "Files changed");
  assert.strictEqual(files.querySelector(".card-head .t-sm.muted.tnum").textContent, "—");
  assert.doesNotMatch(files.textContent, /No files changed yet/);
});

test("a non-Claude session draws the empty-state block with no .kpi-row, no filter and no side column", async () => {
  const { box, host } = page();
  box.lastSessions = [{ id: "s2", name: "audit-shell", agent: "bash", claude: false }];
  box.fetch = okFetch({ claude: false, events: [], more: false, summary: { running_s: 11 * 86400 + 13 * 3600 } });
  box.paneViewRenderers.activity(host, "s2");
  await tick();
  const block = host.querySelector(".act-empty-state");
  assert.ok(block);
  assert.ok(classes(block).includes("empty-state"));
  assert.deepStrictEqual(block.children.map((c) => c.tag), ["h2", "p", "p"]);
  assert.deepStrictEqual(block.children.map((c) => classes(c).join(" ")), ["t-lg w-600", "t-md muted", "t-sm muted tnum"]);
  assert.deepStrictEqual(block.children.map((c) => c.textContent), [
    "Activity is for Claude sessions",
    "audit-shell runs bash, so there is no transcript to show. If this session uses a credential, the event still appears here.",
    "Session age 11d 13h · no credential events yet",
  ]);
  for (const sel of [".kpi-row", ".act-filter", ".seg", ".act-side", ".act-wrap", "table", ".card", ".act-msg"]) {
    assert.strictEqual(host.querySelector(sel), null, sel + " is not drawn");
  }
  assert.strictEqual(block.parent, host.children[0], "the block is not inside a padded column");
  assert.ok(!classes(host.children[0]).includes("act-wrap"));
});

test("a non-Claude session whose credential read failed says so in the block", async () => {
  const { box, host } = page();
  box.lastSessions = [{ id: "s2", name: "audit-shell", agent: "bash", claude: false }];
  box.fetch = async () => ({ status: 500 });
  box.paneViewRenderers.activity(host, "s2");
  await tick();
  assert.match(host.querySelector(".act-empty-state").children[2].textContent, /^Session age — · could not read credential events$/);
});

test("a non-Claude session with credential events draws the block and the table", async () => {
  const { box, host } = page();
  box.lastSessions = [{ id: "s2", name: "audit-shell", agent: "bash", claude: false }];
  box.fetch = okFetch({
    claude: false, more: false, summary: { running_s: 3600, credentials: 1 },
    events: [{ ts: ago(60), kind: "credential", detail: "aws culipulse-prod", state: "minted" }],
  });
  box.paneViewRenderers.activity(host, "s2");
  await tick();
  const block = host.querySelector(".act-empty-state");
  assert.ok(block);
  assert.match(block.children[2].textContent, /· 1 credential event$/);
  const rows = rowsOf(host);
  assert.strictEqual(rows.length, 1);
  assert.deepStrictEqual(cellsOf(rows[0]).map((c) => c.textContent).slice(1), ["Credential", "aws culipulse-prod", "minted"]);
  for (const sel of [".kpi-row", ".act-filter", ".act-side", ".act-wrap"]) assert.strictEqual(host.querySelector(sel), null, sel);
  const kids = host.children[0].children;
  assert.ok(kids.indexOf(block) < kids.findIndex((k) => k.querySelector && k.querySelector("table")), "the block comes first, top-aligned");
});

test("re-targeting between a Claude and a non-Claude session swaps the layout and keeps one reply box", async () => {
  const { box, host } = page();
  box.fetch = okFetch(REPLY());
  box.lastSessions = [claude("s1"), { id: "s2", name: "audit-shell", agent: "bash", claude: false }];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  assert.ok(host.querySelector(".act-wrap") && host.querySelector(".kpi-row"));
  h.update("s2");
  await tick();
  assert.strictEqual(host.querySelector(".act-wrap"), null);
  assert.ok(host.querySelector(".act-empty-state"));
  h.update("s1");
  await tick();
  assert.ok(host.querySelector(".act-wrap") && host.querySelector(".kpi-row") && host.querySelector("table.tbl.act-tbl"));
  assert.strictEqual(host.querySelector(".act-empty-state"), null);
  assert.strictEqual(host.querySelectorAll("input").length, 1);
});

test("the source keeps no expand toggle, no focus-restore key and no markup parsing, and passes the failed read to the model", () => {
  const src = fs.readFileSync(path.join(STATIC, "activitytab.js"), "utf8");
  for (const dead of [/focusKey/, /\bexpanded\b/, /aria-expanded/, /innerHTML/, /role",\s*"button"/]) assert.doesNotMatch(src, dead);
  assert.match(src, /activityModel\(\{[^}]*failed: st\.failed/);
});

// Every class the view puts on screen is defined by a stylesheet (classnames.test.js cannot see a class that
// goes through actEl()), so a typo or a rename never renders unstyled.
test("every class on the drawn view is defined in a stylesheet", async () => {
  const strip = (c) => c.replace(/\/\*[\s\S]*?\*\//g, "");
  const defined = new Set();
  for (const f of ["tokens.css", "primitives.css", "app.css", "desktop.css"]) {
    for (const m of strip(fs.readFileSync(path.join(STATIC, f), "utf8")).matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)) defined.add(m[1]);
  }
  const used = new Set();
  const walk = (n) => { for (const c of n.children) { if (c instanceof Node) { classes(c).forEach((k) => used.add(k)); walk(c); } } };
  const tools = { tools: [{ name: "Read", calls: 2, ctxBytes: 10, kind: "tool" }] };
  const views = [
    await claudeView({ api: async () => tools, terminalTabFor: () => ({ usage: () => ({ pct: 61, contextTokens: 1, contextMax: 200000 }) }) },
      { need: "ask", activity: "waiting", ask_full: "Bash(ls)" }, REPLY({ more: true, partial: true })),
    await claudeView({ api: async () => { throw new Error("x"); } }, null, REPLY({ events: [] })),
  ];
  const plain = page();
  plain.box.lastSessions = [{ id: "s2", name: "audit-shell", agent: "bash", claude: false }];
  plain.box.fetch = okFetch({ claude: false, events: [{ ts: ago(5), kind: "approval", detail: "expired", state: "expired" }], summary: {} });
  plain.box.paneViewRenderers.activity(plain.host, "s2");
  await tick();
  views.push(plain);
  const gone = await claudeView();
  gone.box.lastSessions = [];
  gone.handle.update();
  views.push(gone);
  for (const v of views) walk(v.host);
  const missing = [...used].filter((k) => !defined.has(k));
  assert.deepStrictEqual(missing, [], "classes with no CSS rule");
  assert.ok(used.has("act-wrap") && used.has("act-tbl") && used.has("act-empty-state"), "the walk reached the view");
});

// #168 G2 21, 22, 27-37: a row's Kind cell draws its glyph (14) before the kind word, its Outcome cell one only for a refusal or an
// error. The words, the cell classes and the Kind cell's title are what they were; a success draws no glyph.
test("a Kind cell draws its glyph first and keeps its word; an Outcome cell draws one only for a refusal or an error", async () => {
  const { host } = await claudeView(null, null, REPLY({ events: [
    { ts: ago(10), kind: "tool", tool: "Bash", detail: "go test ./...", state: "error", exit: 1 },
    { ts: ago(20), kind: "tool", tool: "mcp__github__list", detail: "list prs", state: "ok", ms: 900 },
    { ts: ago(30), kind: "credential", provider: "github", detail: "github org", state: "refused" },
    { ts: ago(40), kind: "credential", detail: "aws old-server", state: "minted" },
    { ts: ago(50), kind: "approval", detail: "write 30 min", state: "granted" },
    { ts: ago(60), kind: "you", detail: "hello" },
    { ts: ago(70), kind: "clear" },
  ] }));
  const glyphs = (td) => td.children.filter((c) => c.tag === "svg");
  const rows = rowsOf(host).map((tr) => cellsOf(tr));
  const first = (c) => glyphs(c)[0];
  assert.deepStrictEqual(rows.map((c) => glyphs(c[1]).length), [1, 1, 1, 1, 1, 1, 1], "one glyph per Kind cell");
  assert.deepStrictEqual(rows.map((c) => first(c[1]).attrs["data-icon"]),
    ["terminal", "plug", "si:github", "key-round", "shield-alert", "message-square", "eraser"]);
  for (const c of rows) {
    const g = first(c[1]);
    assert.deepStrictEqual([g.attrs.width, g.attrs.height, g.attrs["aria-hidden"], g.className, c[1].children[0] === g], ["14", "14", "true", "ui-icon", true]);
  }
  assert.deepStrictEqual(rows.map((c) => c[1].textContent), ["Bash", "mcp__github__list", "Credential", "Credential", "Approval", "You", "Cleared"]);
  assert.deepStrictEqual(rows.map((c) => c[1].title), ["Bash", "mcp__github__list", "Credential", "Credential", "Approval", "You", "Cleared"]);
  assert.deepStrictEqual(rows.map((c) => glyphs(c[3]).map((g) => [g.attrs["data-icon"], g.attrs.width, c[3].children[0] === g])),
    [[["circle-x", "14", true]], [], [["ban", "14", true]], [], [], [], []]);
  assert.deepStrictEqual(rows.map((c) => c[3].textContent), ["error · exit 1", "ok · 0.9s", "refused", "minted", "granted", "message", ""]);
  assert.deepStrictEqual(rows.map((c) => c[3].className), ["num c-red", "num c-ok", "num sub", "num c-ok", "num c-red", "num sub", "num sub"]);
});

test("the pending ask's row draws the hand before its amber word", async () => {
  const { host } = await claudeView(null, { need: "ask", ask: "Run it?" }, REPLY({ waiting_since: ago(90) }));
  const kind = cellsOf(rowsOf(host)[0])[1];
  assert.strictEqual(kind.textContent, "Asks to run");
  assert.strictEqual(kind.className, "c-amber");
  assert.strictEqual(kind.children[0].attrs["data-icon"], "hand");
});

// ---- the view's rules (desktop.css) -----------------------------------------------------------------
const desktopCss = () => fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const activityCss = () => {
  const css = desktopCss();
  const a = css.indexOf(".pane-view[data-view=\"activity\"]");
  const b = css.indexOf("#spawn-modal", a);
  assert.ok(a > 0 && b > a, "the Activity block is findable");
  return css.slice(a, b);
};

test("the Activity rules replace the legacy ones: nothing of the old row, pill or tile layout is left", () => {
  const css = desktopCss();
  for (const old of [".act-row", ".act-pill", ".act-pills", ".act-tiles", ".act-tile", ".act-sec", ".act-sech", ".act-tool", ".act-tbar", ".act-ctxbar", ".act-file", ".act-fpath", ".act-fdelta", ".act-add", ".act-del", ".act-lab", ".act-dot", ".act-tname", ".act-tpct"]) {
    assert.ok(!new RegExp("\\" + old + "(?![\\w-])").test(css), old + " is gone");
  }
});

test("the view's CSS uses kit values only: no hex, no color-mix, no heavy weight, no --faint text", () => {
  const block = activityCss();
  assert.doesNotMatch(block, /#[0-9a-fA-F]{3,8}\b/);
  assert.doesNotMatch(block, /color-mix|@layer|\(\?<[=!]/);
  assert.doesNotMatch(block, /font-weight:\s*(bold|[789]00)/);
  assert.doesNotMatch(block, /(^|[^-])color:\s*var\(--faint\)/);
  assert.doesNotMatch(block, /cursor:\s*pointer/, "no row or card is clickable any more");
});

test("the layout is the frame's: a 1fr + 320px grid with a 24px gap and 20px 24px padding, 16px between blocks", () => {
  const block = activityCss();
  assert.match(block, /\.act-wrap\s*\{[^}]*display:\s*grid[^}]*gap:\s*var\(--sp-6\)[^}]*align-items:\s*start[^}]*padding:\s*var\(--sp-5\) var\(--sp-6\)/);
  assert.match(block, /@container \(min-width: 1100px\)\s*\{[^}]*\.act-wrap\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\) 320px/);
  assert.match(block, /\.act-main,\s*\.act-side\s*\{[^}]*flex-direction:\s*column[^}]*gap:\s*var\(--sp-4\)[^}]*min-width:\s*0/);
});

test("the port traps are handled: the view root sets the 20px line, h2 and p lose their margin, the empty block fills the whole view", () => {
  const block = activityCss();
  assert.match(block, /\.act-wrap,\s*\.act-plain\s*\{[^}]*line-height:\s*var\(--lh-md\)/);
  assert.match(block, /\.act-wrap :where\(h2, p\),\s*\.act-plain :where\(h2, p\)\s*\{\s*margin:\s*0/);
  assert.match(block, /\.act-plain\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*min-height:\s*100%/);
  assert.match(block, /\.act-plain\.act-listed > \.act-empty-state\s*\{[^}]*flex:\s*none/);
});

test("the table is the frame's: fixed layout, 88 / 124 (104 + the Kind glyph) / 124 columns, a wrapping 6px-padded detail cell", () => {
  const block = activityCss();
  assert.match(block, /\.act-tbl\s*\{[^}]*table-layout:\s*fixed/);
  for (const [n, w] of [[1, 88], [2, 124], [4, 124]]) {
    assert.match(block, new RegExp("\\.act-tbl th:nth-child\\(" + n + "\\)\\s*\\{[^}]*width:\\s*" + w + "px"));
  }
  assert.match(block, /\.act-tbl td\.act-det\s*\{[^}]*white-space:\s*normal[^}]*overflow-wrap:\s*anywhere[^}]*height:\s*auto[^}]*padding-top:\s*6px[^}]*padding-bottom:\s*6px[^}]*line-height:\s*var\(--lh-md\)/);
  assert.match(block, /\.act-card\s*\{[^}]*overflow:\s*hidden/);
  // Not drawn: an earlier day's "28 Nov 14:12" wraps in the 88px Time column, and a long tool name ellipsizes in Kind.
  assert.match(block, /\.act-tbl td:first-child\s*\{[^}]*white-space:\s*normal[^}]*padding-top:\s*6px[^}]*padding-bottom:\s*6px/);
  assert.match(block, /\.act-tbl td:nth-child\(2\)\s*\{[^}]*overflow:\s*hidden[^}]*text-overflow:\s*ellipsis/);
  assert.ok(block.indexOf(".act-tbl td:first-child") < block.indexOf(".act-tbl td.act-empty"), "the empty row's own height comes after");
});

test("a narrow pane (under 648px: five 120px tiles and the view's 24px sides) wraps the KPI tiles and lets the table scroll in its card instead of clipping Outcome", () => {
  const block = activityCss();
  const m = /@container \(max-width: 647px\)\s*\{([\s\S]*?\n  \})/.exec(block);
  assert.ok(m, "a narrow-pane container query");
  assert.ok(block.indexOf("@container (max-width: 647px)") > block.indexOf(".act-tbl th:nth-child(4)"), "it comes after the base rules it overrides (equal weight, later wins)");
  assert.match(m[1], /\.act-wrap \.kpi-row\s*\{[^}]*repeat\(auto-fill, minmax\(120px, 1fr\)\)/);
  assert.match(m[1], /\.act-card\s*\{[^}]*overflow-x:\s*auto/);
  assert.match(m[1], /\.act-tbl\s*\{[^}]*table-layout:\s*auto/);
});

// #168 G2: the glyph's margin and the narrow rule. The pane view is the size container (named `act`, beside the unnamed queries
// above, which still read it); a pane narrower than the table with its kind glyphs drops the kind glyphs, and only those: the
// outcome glyphs and every word stay. Desktop only (the Activity view never opens on a phone).
test("a narrow pane drops the kind glyphs, keeps the outcome glyphs and every word", () => {
  const css = desktopCss();
  const open = css.indexOf("@media (min-width: 701px)");
  assert.ok(open > 0, "the desktop block");
  const block = activityCss();
  assert.match(block, /\.pane-view\[data-view="activity"\]\s*\{\s*container:\s*act \/ inline-size;\s*\}/);
  const m = /@container act \(max-width: (\d+)px\)\s*\{([^}]*\})\s*\}/.exec(block);
  assert.ok(m, "a named narrow-pane rule");
  assert.strictEqual(m[1], "496", "the pane width at which the table with both glyphs stops scrolling in its card (measured: 497px)");
  assert.strictEqual(m[2].replace(/\s+/g, " ").trim(), ".act-tbl td:nth-child(2) > .ui-icon { display: none; }");
  assert.ok(css.indexOf("@container act (max-width: 496px)") > open, "inside the desktop media block");
  assert.match(block, /\.act-tbl td:is\(:nth-child\(2\), :nth-child\(4\)\) > \.ui-icon\s*\{\s*margin-right:\s*6px/);
  assert.doesNotMatch(m[2], /nth-child\(4\)/, "the outcome glyph is not hidden");
  assert.ok(block.indexOf("@container act") > block.indexOf("margin-right: 6px"), "the narrow rule follows the base rule it overrides");
});
