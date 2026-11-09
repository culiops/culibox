"use strict";
// elevbanner.js: the write-access banner under every pane header (UI plan 6, M6.4), against the shared
// fake DOM (the repo has no jsdom). The page's real accessmodel.js and sessionrow.js run beside it, so the
// banner is the model's own elevBanner; only the accesstab.js globals (accData, accLoad, accDrop, accBusy,
// accViews), mobileActive and positionDrawer are stand-ins.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

const MIN = 60000;
const iso = (min) => new Date(Date.now() + min * MIN + 30000).toISOString(); // +30 s: floor() reads exactly `min`
const BASE = "arn:aws:iam::402100009917:role/agent-ops-read";
const WRITE = "arn:aws:iam::402100009917:role/agent-ops-write";
const conn = (over) => Object.assign({
  name: "culipulse-prod", label: "culipulse-prod", account_id: "402100009917", role_arn: BASE, region: "ap-southeast-1",
  prod: true, write_capable: true, elevate: { role_arn: WRITE, max_minutes: 60, allow_iam: false },
}, over);
const sess = (min, over) => Object.assign({
  id: "s1", agent: "claude", runtime: "box",
  aws_connections: [conn(min === null ? {} : { elevated_until: iso(min) })],
}, over);
const granted = (over) => Object.assign({ ts: Date.now() - 60000, action: "granted", minutes: 30, name: "culipulse-prod", reason: "rotate KMS key" }, over);

// One page: the scripts the banner leans on, in a shared global scope like the browser.
function page() {
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  doc.body = doc.createElement("body");
  const box = {
    document: doc, window: { innerWidth: 1280 }, console, Date, JSON, Map, Set, Math, Promise,
    phone: false, lastSessions: [],
    mobileActive: () => box.phone,
    accData: new Map(), accBusy: new Set(), accViews: new Map(),
    reads: [], drops: [], drawer: 0,
    accLoad: (sid, force) => { box.reads.push([sid, force]); },
    positionDrawer: () => { box.drawer++; },
    // The real accDrop's shape: busy at once with a repaint; the request stays in flight until the test calls
    // box.finish(ok), which does what accDrop's tail does (clear busy, repaint) and then resolves with true on a
    // success, false on a failure (the toast is accDrop's, not the banner's).
    accDrop: (sid, repaint) => {
      box.drops.push(sid); // every call into accDrop, before its own busy guard
      if (box.accBusy.has(sid)) return Promise.resolve(false);
      box.accBusy.add(sid);
      repaint(true);
      return new Promise((resolve) => {
        box.finish = (ok) => { box.accBusy.delete(sid); repaint(true); resolve(ok); };
      });
    },
  };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["sessionrow.js", "accessmodel.js", "elevbanner.js"]) {
    new vm.Script(read(f), { filename: f }).runInContext(ctx);
  }
  // A pane as tabs.js builds it: the header, the ask strip, the body.
  const bar = doc.createElement("div"); bar.className = "cell-bar";
  const strip = doc.createElement("div"); strip.className = "needstrip";
  const body = doc.createElement("div"); body.className = "cell-body";
  const el = doc.createElement("div"); el.className = "cell";
  el.append(bar, strip, body);
  const cell = { bar, _strip: strip, el };
  const sync = (s) => { if (s) box.lastSessions = [s]; box.paneElevSync(cell, s); };
  return { box, doc, cell, sync, bar, strip, body };
}
const kids = (cell) => cell.el.children.map((c) => c.className);
const tick = () => new Promise((r) => setImmediate(r));
// What Chromium does a couple of frames after the focused control becomes :disabled (focus fixup): focus
// falls to <body>. An aria-disabled control keeps it. Call after anything that could have disabled a button.
const settle = (doc) => { const a = doc.activeElement; if (a && a.disabled) doc.activeElement = doc.body; };
const loaded = (box, sid, approvals) => box.accData.set(sid, { aws: { approvals }, loaded: true, failed: false, inflight: false });

test("an open window puts one .elev[role=status] right after .cell-bar and before the ask strip", () => {
  const { cell, sync } = page();
  sync(sess(23));
  assert.deepStrictEqual(kids(cell), ["cell-bar", "elev", "needstrip", "cell-body"]);
  assert.strictEqual(cell._elev.attrs.role, "status");
  sync(sess(23));
  sync(sess(22));
  assert.strictEqual(cell.el.querySelectorAll(".elev").length, 1, "a sync never stacks a second banner");
  assert.deepStrictEqual(kids(cell), ["cell-bar", "elev", "needstrip", "cell-body"]);
});

test("a pane with no ask strip puts the banner right under the header", () => {
  const { cell, sync, strip } = page();
  cell._strip = null;
  strip.remove();
  sync(sess(23));
  assert.deepStrictEqual(kids(cell), ["cell-bar", "elev", "cell-body"]);
});

test("banner text is strong \"Write access to culipulse-prod\" + span detail, and \"Drop now\" is a .btn.btn-danger.btn-sm", () => {
  const { box, cell, sync } = page();
  loaded(box, "s1", [granted()]);
  const s = sess(23);
  sync(s);
  const [strong, span, gap, btn] = cell._elev.children;
  assert.strictEqual(cell._elev.children.length, 4);
  assert.strictEqual(strong.tag, "strong");
  assert.strictEqual(strong.textContent, "Write access to culipulse-prod");
  assert.strictEqual(span.tag, "span");
  const until = box.hhmm(s.aws_connections[0].elevated_until);
  assert.strictEqual(span.textContent, "until " + until + " · 23 min left · “rotate KMS key”");
  assert.strictEqual(span.title, span.textContent, "the full detail rides in the title: the span ellipsizes in a narrow pane");
  assert.strictEqual(gap.className, "ph-spacer");
  assert.strictEqual(btn.tag, "button");
  assert.strictEqual(btn.className, "btn btn-danger btn-sm");
  assert.strictEqual(btn.type, "button");
  assert.strictEqual(btn.textContent, "Drop now");
  assert.strictEqual(btn.disabled, false);
});

test("before the status loads, and after a failed read: no reason segment and no empty quotes", () => {
  const { box, cell, sync } = page();
  sync(sess(23));
  const span = () => cell._elev.children[1];
  assert.doesNotMatch(span().textContent, /“|”| · $/);
  assert.match(span().textContent, /^until \d\d:\d\d · 23 min left$/);
  box.accData.set("s1", { aws: null, loaded: false, failed: true, inflight: false });
  sync(sess(23));
  assert.match(span().textContent, /^until \d\d:\d\d · 23 min left$/);
  loaded(box, "s1", [granted({ reason: "   " })]);
  sync(sess(23));
  assert.doesNotMatch(span().textContent, /“|”/, "a blank reason is no reason");
});

test("the reason arriving, and the minutes moving, repaint in place", () => {
  const { box, cell, sync } = page();
  sync(sess(23));
  const node = cell._elev;
  const [strong, span, gap, btn] = node.children;
  loaded(box, "s1", [granted()]);
  sync(sess(23));
  assert.match(span.textContent, /“rotate KMS key”$/);
  sync(sess(15));
  assert.match(span.textContent, / · 15 min left · “rotate KMS key” · new credentials from the base role$/);
  sync(sess(0));
  assert.match(span.textContent, /<1 min left/);
  assert.ok(cell._elev === node && node.children[0] === strong && node.children[1] === span && node.children[2] === gap && node.children[3] === btn,
    "the same nodes throughout");
});

test("a lapsed window removes the banner on the next sync", () => {
  const { cell, sync } = page();
  sync(sess(23));
  assert.ok(cell._elev);
  sync(sess(-1));
  assert.strictEqual(cell._elev, null);
  assert.deepStrictEqual(kids(cell), ["cell-bar", "needstrip", "cell-body"]);
  sync(sess(23));
  assert.ok(cell._elev, "a new window brings it back");
  sync(sess(null));
  assert.strictEqual(cell._elev, null, "no elevated_until at all");
  sync(sess(23));
  sync(null);
  assert.strictEqual(cell._elev, null, "the session left the list");
  assert.deepStrictEqual(kids(cell), ["cell-bar", "needstrip", "cell-body"]);
});

test("a window that has no binding to name shows nothing", () => {
  const { cell, sync } = page();
  sync({ id: "s1", agent: "claude", runtime: "box" });
  assert.strictEqual(cell._elev == null, true);
  assert.strictEqual(cell.el.querySelectorAll(".elev").length, 0);
});

test("no banner on the phone, and crossing to the phone removes it", () => {
  const { box, cell, sync } = page();
  box.phone = true;
  sync(sess(23));
  assert.ok(!cell._elev, "never created while mobileActive()");
  assert.deepStrictEqual(kids(cell), ["cell-bar", "needstrip", "cell-body"]);
  assert.deepStrictEqual(box.reads, [], "and no status read for a banner nobody sees");
  box.phone = false;
  sync(sess(23));
  assert.ok(cell._elev);
  box.phone = true; // the matchMedia crossing: the same sync that repaints the pane
  sync(sess(23));
  assert.ok(!cell._elev, "the crossing takes it out in that sync");
  assert.deepStrictEqual(kids(cell), ["cell-bar", "needstrip", "cell-body"]);
});

test("Drop now calls accDrop once; in flight it is inert (aria-disabled, never disabled) and says Dropping…, and keeps focus", () => {
  const { box, doc, cell, sync } = page();
  sync(sess(23));
  const btn = cell._elev.children[3];
  btn.focus();
  btn.click();
  assert.deepStrictEqual(box.drops, ["s1"]);
  assert.strictEqual(btn.disabled, false, "a disabled button drops focus to <body>: the control stays focusable");
  assert.strictEqual(btn.attrs["aria-disabled"], "true");
  assert.strictEqual(btn.textContent, "Dropping…");
  assert.strictEqual(btn.attrs["aria-busy"], "true", "a control at work is busy, not closed (the kit does not dim it)");
  settle(doc);
  assert.strictEqual(doc.activeElement, btn, "still the focused control after the browser's focus fixup");
  btn.click();
  btn.click();
  assert.deepStrictEqual(box.drops, ["s1"], "a double press reaches accDrop once, so one DELETE is sent");
  // The poll lands while it is in flight: the button keeps saying so, and keeps its node.
  sync(sess(22));
  settle(doc);
  assert.strictEqual(btn.textContent, "Dropping…");
  assert.strictEqual(cell._elev.children[3], btn);
  assert.strictEqual(doc.activeElement, btn);
});

test("a failed Drop makes the button a button again, with focus still on it", async () => {
  const { box, doc, cell, sync } = page();
  const w = sess(23);
  sync(w);
  const btn = cell._elev.children[3];
  btn.focus();
  btn.click();
  box.finish(false); // the DELETE failed: accDrop toasted the error and cleared busy
  await tick();
  settle(doc);
  assert.ok(cell._elev, "the banner stays until the list says the window is closed");
  assert.strictEqual(btn.attrs["aria-disabled"], undefined);
  assert.strictEqual(btn.attrs["aria-busy"], undefined);
  assert.strictEqual(btn.textContent, "Drop now");
  assert.strictEqual(doc.activeElement, btn);
  btn.click();
  assert.deepStrictEqual(box.drops, ["s1", "s1"], "and it can be pressed again");
});

test("a failed Drop gives focus back to the button when it had fallen to <body>", async () => {
  const { box, doc, cell, sync, body } = page();
  sync(sess(23));
  const btn = cell._elev.children[3];
  btn.focus();
  btn.click();
  doc.activeElement = doc.body;
  box.finish(false);
  await tick();
  assert.strictEqual(doc.activeElement, btn);
  // Focus the user moved somewhere else on purpose is not taken back.
  const other = doc.createElement("input");
  body.appendChild(other);
  btn.click();
  other.focus();
  box.finish(false);
  await tick();
  assert.strictEqual(doc.activeElement, other);
});

test("a successful Drop keeps the button inert (Dropping…) until its window changes or leaves: a further click sends no DELETE", async () => {
  const { box, doc, cell, sync } = page();
  const w = sess(23);
  sync(w);
  const btn = cell._elev.children[3];
  btn.focus();
  btn.click();
  box.finish(true); // 204: accDrop cleared busy, toasted, and asked for the list
  await tick();
  assert.strictEqual(box.accBusy.has("s1"), false);
  assert.strictEqual(btn.textContent, "Dropping…", "not back to Drop now before the poll says the window is gone");
  assert.strictEqual(btn.attrs["aria-disabled"], "true");
  assert.strictEqual(btn.disabled, false);
  btn.click();
  sync(w); // a poll that still carries the window
  btn.click();
  assert.deepStrictEqual(box.drops, ["s1"], "one DELETE in all");
  assert.strictEqual(btn.textContent, "Dropping…");
  settle(doc);
  assert.strictEqual(doc.activeElement, btn);
  // A different window (extended or opened again from elsewhere) is a new question: Drop now is live again.
  sync(sess(40));
  assert.strictEqual(btn.textContent, "Drop now");
  assert.strictEqual(btn.attrs["aria-disabled"], undefined);
  btn.click();
  assert.deepStrictEqual(box.drops, ["s1", "s1"]);
});

test("a successful Drop whose window then leaves: the banner goes and focus goes to the pane, not <body>", async () => {
  const { box, doc, cell, sync } = page();
  cell._viewHost = doc.createElement("div"); // a pane showing Access
  cell.el.appendChild(cell._viewHost);
  const w = sess(23);
  sync(w);
  cell._elev.children[3].focus();
  cell._elev.children[3].click();
  box.finish(true);
  await tick();
  settle(doc);
  sync(sess(null)); // the poll: the window is closed
  assert.ok(!cell._elev);
  assert.strictEqual(doc.activeElement, cell._viewHost);
});

test("focus that already fell to <body> after a press still goes to the pane when the banner leaves; without a press it is left alone", async () => {
  const a = page();
  a.sync(sess(23));
  a.cell._elev.children[3].click();
  a.doc.activeElement = a.doc.body; // fixup, or a click on empty space
  a.box.finish(true);
  await tick();
  a.sync(sess(-1));
  assert.ok(!a.cell._elev);
  assert.strictEqual(a.doc.activeElement, a.cell.el, "pressed, then on <body>: the pane");
  // Never pressed: focus on <body> was not the banner's, and is not taken.
  const b = page();
  b.sync(sess(23));
  b.doc.activeElement = b.doc.body;
  b.sync(sess(-1));
  assert.strictEqual(b.doc.activeElement, b.doc.body);
  // Pressed, but focus is in another pane's input: not taken either.
  const c = page();
  const input = c.doc.createElement("input");
  c.body.appendChild(input);
  c.sync(sess(23));
  c.cell._elev.children[3].click();
  input.focus();
  c.box.finish(true);
  await tick();
  c.sync(sess(-1));
  assert.strictEqual(c.doc.activeElement, input);
});

test("a Drop that rejects leaves a button again, and does not strand the banner inert", async () => {
  const { box, cell, sync } = page();
  box.accDrop = () => Promise.reject(new Error("boom"));
  sync(sess(23));
  const btn = cell._elev.children[3];
  btn.click();
  await tick();
  assert.strictEqual(btn.textContent, "Drop now");
  assert.strictEqual(btn.attrs["aria-disabled"], undefined);
});

test("accDrop answers true only when the daemon dropped the window (the banner reads it)", async () => {
  const toasts = [];
  const box = { document: { createElement: () => ({}) }, console, Map, Set, Date, JSON, Promise, lastSessions: [], paneViewRenderers: {},
    showToast: (m, o) => toasts.push([m, !!(o && o.error)]), dropToast: () => "dropped", refreshSessions: () => {}, fetch: async () => ({}) };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  new vm.Script(read("accessmodel.js"), { filename: "accessmodel.js" }).runInContext(ctx);
  new vm.Script(read("accesstab.js"), { filename: "accesstab.js" }).runInContext(ctx);
  const accBusy = vm.runInContext("accBusy", ctx); // a top-level const: not a property of the global
  let calls = 0;
  const repaints = [];
  box.elevDrop = async () => { calls++; return { dropped: true }; };
  assert.strictEqual(await box.accDrop("s1", (f) => repaints.push(f)), true);
  assert.deepStrictEqual(repaints, [true, true]);
  assert.strictEqual(accBusy.has("s1"), false);
  box.elevDrop = async () => { calls++; throw new Error("nope"); };
  assert.strictEqual(await box.accDrop("s1", () => {}), false, "a failure");
  assert.deepStrictEqual(toasts[toasts.length - 1], ["nope", true]);
  accBusy.add("s1");
  assert.strictEqual(await box.accDrop("s1", () => {}), false, "refused: a request is already in flight");
  assert.strictEqual(calls, 2, "and a refusal sent no DELETE");
});

test("busy for another reason (an Extend in flight) is inert too, but still reads Drop now and sends no Drop", () => {
  const { box, cell, sync } = page();
  const w = sess(23);
  sync(w);
  const btn = cell._elev.children[3];
  box.accBusy.add("s1"); // accGrant's, not a Drop's
  sync(w);
  assert.strictEqual(btn.attrs["aria-disabled"], "true");
  assert.strictEqual(btn.disabled, false);
  assert.strictEqual(btn.textContent, "Drop now", "Dropping… is only for a press of this button");
  assert.strictEqual(btn.attrs["aria-busy"], undefined, "so the kit dims it as closed");
  btn.click();
  assert.deepStrictEqual(box.drops, []);
  box.accBusy.delete("s1");
  sync(w);
  assert.strictEqual(btn.attrs["aria-disabled"], undefined);
  btn.click();
  assert.deepStrictEqual(box.drops, ["s1"]);
});

test("Drop now repaints this pane and the Access view, which is told the repaint is forced", async () => {
  const { box, cell, sync } = page();
  const seen = [];
  box.accViews.set("s1", (force) => seen.push(force));
  sync(sess(23));
  cell._elev.children[3].click();
  assert.deepStrictEqual(seen, [true]);
  assert.strictEqual(cell._elev.children[3].textContent, "Dropping…", "this pane's banner was re-synced by the same repaint");
  box.finish(false);
  await tick();
  assert.strictEqual(seen.length >= 2, true);
  // No Access view mounted: nothing to repaint, no throw.
  box.accViews.clear();
  cell._elev.children[3].click();
  box.finish(false);
  await tick();
});

test("the banner keeps its nodes when nothing visible changed: same key, no replaceChildren, the button keeps focus", () => {
  const { box, doc, cell, sync } = page();
  loaded(box, "s1", [granted()]);
  const first = sess(23);
  sync(first);
  const node = cell._elev;
  const parts = [...node.children];
  let writes = 0;
  const d = Object.getOwnPropertyDescriptor(Node.prototype, "textContent");
  for (const n of parts) {
    Object.defineProperty(n, "textContent", { get() { return d.get.call(n); }, set(v) { writes++; d.set.call(n, v); } });
  }
  node.replaceChildren = () => { throw new Error("replaceChildren on an unchanged banner"); };
  parts[3].focus();
  for (let i = 0; i < 5; i++) sync(first); // five polls
  assert.strictEqual(writes, 0, "no text node is touched: a role=status region is not re-announced every 3 s");
  assert.ok(cell._elev === node && node.children.every((c, i) => c === parts[i]));
  assert.strictEqual(doc.activeElement, parts[3], "focus stays on Drop now");
  // A visible change touches only what changed.
  loaded(box, "s1", [granted({ reason: "second reason" })]);
  sync(first);
  assert.strictEqual(writes, 1, "only the detail span");
  assert.match(parts[1].textContent, /“second reason”$/);
  assert.strictEqual(doc.activeElement, parts[3]);
});

test("removing a banner that holds focus gives focus to the pane", () => {
  // No view and no terminal to hand it to: the pane element itself.
  const a = page();
  a.sync(sess(23));
  a.cell._elev.children[3].focus();
  a.sync(sess(-1));
  assert.ok(!a.cell._elev);
  assert.strictEqual(a.doc.activeElement, a.cell.el, "not <body>");
  // A pane showing Activity or Access hands it to the view host.
  const b = page();
  b.cell._viewHost = b.doc.createElement("div");
  b.cell.el.appendChild(b.cell._viewHost);
  b.sync(sess(23));
  b.cell._elev.children[3].focus();
  b.sync(sess(null));
  assert.strictEqual(b.doc.activeElement, b.cell._viewHost);
  // A pane on its terminal hands it to the terminal's own keyboard path.
  const c = page();
  let shown = 0;
  c.cell.activeTab = { kind: "terminal", onShow: () => { shown++; } };
  c.sync(sess(23));
  c.cell._elev.children[3].focus();
  c.sync(sess(null));
  assert.strictEqual(shown, 1);
  // Focus anywhere else is left where it was.
  const d = page();
  const other = d.doc.createElement("input");
  d.body.appendChild(other);
  d.sync(sess(23));
  other.focus();
  d.sync(sess(null));
  assert.strictEqual(d.doc.activeElement, other);
});

test("a window change asks for one forced status read, and a poll with no change asks for none", () => {
  const { box } = page();
  const reads = () => box.reads.map((r) => r.join(":")).join(" ");
  const watch = (s) => { box.lastSessions = [s]; box.elevWatch(s); };
  watch(sess(null));
  watch(sess(null));
  assert.strictEqual(reads(), "", "a session that never had a window costs nothing");
  watch(sess(23, { aws_connections: [conn({ elevated_until: "2099-01-01T10:00:00Z" })] }));
  assert.strictEqual(reads(), "s1:true", "opened: one forced read");
  const same = () => sess(23, { aws_connections: [conn({ elevated_until: "2099-01-01T10:00:00Z" })] });
  watch(same());
  watch(same());
  assert.strictEqual(reads(), "s1:true", "polls of the same window: none");
  watch(sess(23, { aws_connections: [conn({ elevated_until: "2099-01-01T10:30:00Z" })] }));
  assert.strictEqual(reads(), "s1:true s1:true", "extended: one more");
  watch(sess(null));
  assert.strictEqual(reads(), "s1:true s1:true s1:true", "closed (back to none): one more, for the Access row's lapse line");
  watch(sess(null));
  watch(sess(null));
  assert.strictEqual(box.reads.length, 3);
  // Another session's window is its own change.
  watch(sess(23, { id: "s2", aws_connections: [conn({ elevated_until: "2099-01-01T10:00:00Z" })] }));
  assert.strictEqual(box.reads[3].join(":"), "s2:true");
});

test("a window that lapses by the clock between two polls is a change to none", () => {
  const { box } = page();
  const w = (ms) => ({ id: "s1", aws_connections: [conn({ elevated_until: new Date(ms).toISOString() })] });
  box.elevWatch(w(Date.now() + 5 * MIN));
  assert.strictEqual(box.reads.length, 1);
  box.elevWatch(w(Date.now() - 1000)); // the list still carries it, but it is over
  assert.strictEqual(box.reads.length, 2);
  box.elevWatch(w(Date.now() - 1000));
  assert.strictEqual(box.reads.length, 2, "and stays one read");
});

test("a read already in flight is not lost: the change is asked again on the next poll", () => {
  const { box } = page();
  const open = { id: "s1", aws_connections: [conn({ elevated_until: "2099-01-01T10:00:00Z" })] };
  box.accData.set("s1", { aws: null, loaded: false, failed: false, inflight: true });
  box.elevWatch(open);
  assert.strictEqual(box.reads.length, 0, "accLoad would drop a forced read while one is inflight");
  box.accData.get("s1").inflight = false;
  box.elevWatch(open);
  box.elevWatch(open);
  assert.strictEqual(box.reads.length, 1);
});

test("a pane sync with a window asks for the status read once, not once per sync", () => {
  const { box, sync } = page();
  const w = sess(23); // one window, polled again and again
  sync(w);
  sync(w);
  sync(w);
  assert.deepStrictEqual(box.reads, [["s1", true]]);
  sync(sess(-1));
  assert.strictEqual(box.reads.length, 2, "and once for the lapse");
});

test("a session that left the list is forgotten, so its window is asked about afresh if it returns; an empty list forgets nothing", () => {
  const { box, cell, sync } = page();
  const w = sess(23, { aws_connections: [conn({ elevated_until: "2099-01-01T10:00:00Z" })] });
  box.lastSessions = [w];
  box.elevWatch(w);
  assert.strictEqual(box.reads.length, 1);
  // The poll before the list has loaded says nothing (accPrune's own guard).
  box.lastSessions = [];
  box.paneElevSync(cell, null);
  box.lastSessions = [w];
  box.elevWatch(w);
  assert.strictEqual(box.reads.length, 1, "an empty list is not 'gone'");
  // s1 is gone from a real list: its entry goes.
  const other = { id: "s2", aws_connections: [conn()] };
  box.lastSessions = [other];
  sync(other);
  box.lastSessions = [w];
  box.elevWatch(w);
  assert.strictEqual(box.reads.length, 2, "forgotten while gone, so asked again on its return");
  box.elevWatch(w);
  assert.strictEqual(box.reads.length, 2);
});

test("inserting or removing the banner re-measures the Files drawer; an unchanged sync does not", () => {
  const { box, sync } = page();
  sync(sess(23));
  assert.strictEqual(box.drawer, 1);
  sync(sess(23));
  sync(sess(22));
  assert.strictEqual(box.drawer, 1, "text moved, the box did not");
  sync(sess(-1));
  assert.strictEqual(box.drawer, 2);
  sync(sess(-1));
  assert.strictEqual(box.drawer, 2, "nothing to remove, nothing to measure");
});

test("no phone-path change: app.css names nothing of the banner, and elevbanner.js draws with textContent only", () => {
  assert.doesNotMatch(read("app.css"), /\.elev(?![\w-])|\.ph-spacer/);
  const src = read("elevbanner.js").replace(/\/\/.*$/gm, "");
  assert.doesNotMatch(src, /innerHTML|insertAdjacentHTML|outerHTML/);
  assert.doesNotMatch(src, /setTimeout|setInterval|requestAnimationFrame/, "no timers: the 3 s poll is the clock");
  assert.match(read("index.html"), /<script src="\/elevbanner\.js"><\/script>/);
  const html = read("index.html");
  assert.ok(html.indexOf("/elevbanner.js") > html.indexOf("/accesstab.js"), "after accesstab.js");
});

// ---- the stylesheet --------------------------------------------------------------------------------

const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
const rule = (sel) => {
  const hit = css.split("}").map((r) => r.split("{")).find((p) => p.length === 2 && p[0].trim() === sel);
  return hit ? hit[1].replace(/\s+/g, " ").trim() : null;
};

test("the kit's .elev, .elev strong and .ph-spacer are in the kit fence, verbatim, and the banner narrows to one line", () => {
  const full = read("desktop.css");
  const a = full.indexOf("/* === kit primitives");
  const b = full.indexOf("/* === end kit primitives === */");
  const fence = full.slice(a, b).replace(/\/\*[\s\S]*?\*\//g, "");
  for (const sel of [".elev", ".elev strong", ".ph-spacer"]) {
    assert.ok(fence.includes(sel + " {"), sel + " sits inside the kit fence");
  }
  assert.strictEqual(rule(".elev"), "display: flex; align-items: center; gap: 10px; padding: 8px 12px; background: var(--red-tint); border-bottom: 1px solid rgba(240,106,106,.45); color: var(--fg); font-size: var(--t-sm); flex: none;");
  assert.strictEqual(rule(".elev strong"), "color: var(--red); font-weight: var(--w-semibold);");
  assert.strictEqual(rule(".ph-spacer"), "flex: 1 1 auto;");
  // One line in a narrow pane: the text parts ellipsize, the spacer keeps its grow, Drop now never shrinks.
  const text = rule(".elev > strong, .elev > span:not(.ph-spacer)");
  assert.ok(text, "the banner's text parts have one shared rule");
  for (const d of ["min-width: 0", "overflow: hidden", "text-overflow: ellipsis", "white-space: nowrap"]) assert.ok(text.includes(d), d);
  assert.doesNotMatch(text, /flex:/, "no flex shorthand: it would override .ph-spacer's grow");
  assert.match(rule(".elev .btn"), /flex: none/);
});

test("the old Access-view banner rules are gone", () => {
  assert.doesNotMatch(css, /\.ac-banner|\.ac-card\.elevated|\.ac-btxt|\.ac-dropnow|\.ac-bslot/);
  const tab = read("accesstab.js");
  assert.doesNotMatch(tab, /accBanner|ac-banner|ac-bslot|ac-dropnow|keys\.banner|bslot/);
});

test("the sidebar row's dot gets the kit ring, and with the 'viewed elsewhere' outline both rings show", () => {
  assert.match(read("app.js"), /compact && sessionElevated\(s, [\w.()]+\)\) adot\.classList\.add\("is-elevated"\)/);
  assert.strictEqual(rule(".dot.is-elevated"), "box-shadow: 0 0 0 2px var(--panel), 0 0 0 3.5px var(--red);");
  const both = rule(".dot.is-elevated.watching");
  assert.ok(both, "one combined rule");
  assert.match(both, /outline: (0|none)/, "the outline is drawn as a ring of the same shadow");
  assert.match(both, /box-shadow:[^;]*var\(--red\)[^;]*var\(--fg\)/, "red ring inside, the viewer's ring outside");
});
