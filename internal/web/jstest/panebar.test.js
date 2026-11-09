"use strict";
// Plan 7 Task 8 (#177, DD§A8/B2, DC§6): the desktop pane bars painted by terminal-tab.js from paneBarModel, the
// phone's legacy bars left alone, and the signed-out rule (no reconnect while sys.signedOut, one way back).
// Fixtures use made-up names.
const test = require("node:test");
const assert = require("node:assert");
const { Node, Text } = require("./fakedom.js");
const nm = require("../static/notifymodel.js");
const { TerminalTab } = require("../static/terminal-tab.js");

function setup(over) {
  const o = Object.assign({ phone: false, signedOut: false }, over || {});
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  const calls = { connect: 0, closed: 0, restarted: 0, noted: 0, timers: [] };
  global.document = doc;
  global.window = { innerWidth: o.phone ? 390 : 1280 };
  global.mobileActive = () => o.phone;
  global.paneBarModel = nm.paneBarModel;
  global.sys = { signedOut: o.signedOut };
  global.sysNoteSignedOut = () => { calls.noted++; global.sys.signedOut = true; };
  global.closeTabEverywhere = () => { calls.closed++; };
  global.restartSession = () => { calls.restarted++; };
  global.setTimeout = (fn, ms) => { calls.timers.push({ fn, ms }); return calls.timers.length; };
  global.clearTimeout = () => {};
  global.setInterval = () => 1;
  global.clearInterval = () => {};
  const tab = Object.create(TerminalTab.prototype);
  tab.id = "gap-sh2";
  tab.activityState = o.activity || "idle";
  tab.el = new Node("div", doc);
  tab.el.insertBefore = (c) => { c.parent = tab.el; tab.el.children.unshift(c); };
  tab.fit = () => {};
  tab.setStatus = () => {};
  tab.connect = () => { calls.connect++; };
  return { tab, calls, doc };
}
function teardown() {
  for (const k of ["document", "window", "mobileActive", "paneBarModel", "sys", "sysNoteSignedOut", "closeTabEverywhere",
    "restartSession", "setTimeout", "clearTimeout", "setInterval", "clearInterval", "probeServer", "sessionStatus"]) {
    if (k === "setTimeout") global.setTimeout = require("node:timers").setTimeout;
    else if (k === "clearTimeout") global.clearTimeout = require("node:timers").clearTimeout;
    else if (k === "setInterval") global.setInterval = require("node:timers").setInterval;
    else if (k === "clearInterval") global.clearInterval = require("node:timers").clearInterval;
    else delete global[k];
  }
}
const bar = (tab) => tab.el.children[0];
const labels = (b) => b.children.filter((c) => c.tag === "button").map((c) => c.textContent);
const find = (b, cls) => b.children.find((c) => c.className.split(/\s+/).includes(cls));

test("desktop: a lost connection paints .pane-bar with the hollow dot, a live countdown and Retry now", () => {
  const { tab } = setup();
  try {
    tab.retryAt = Date.now() + 2500;
    tab.showTrouble("offline");
    const b = bar(tab);
    assert.strictEqual(b.className, "pane-bar");
    assert.strictEqual(b.getAttribute("role"), "status");
    assert.strictEqual(find(b, "dot").className, "dot dot-detached");
    assert.strictEqual(find(b, "pane-bar-msg").textContent, "Lost connection · retrying in 3 s");
    assert.deepStrictEqual(labels(b), ["Retry now"]);
    assert.strictEqual(b.children.filter((c) => /pane-trouble/.test(c.className)).length, 0);
    // the same model again leaves the node alone (only the text ticks)
    tab.showTrouble("offline");
    assert.strictEqual(bar(tab), b);
    assert.strictEqual(tab.el.children.length, 1);
    // no retry armed: the line without the countdown
    tab.retryAt = 0; tab.tickTrouble();
    assert.strictEqual(find(b, "pane-bar-msg").textContent, "Lost connection");
  } finally { teardown(); }
});

test("desktop: detached and taken over", () => {
  const { tab, calls } = setup();
  try {
    tab.retryAt = Date.now() + 3000;
    tab.showTrouble("detached");
    assert.strictEqual(find(bar(tab), "pane-bar-msg").textContent, "Detached · reconnecting in 3 s");
    tab.retryAt = 0;
    tab.showTrouble("taken-over");
    const b = bar(tab);
    assert.strictEqual(tab.el.children.length, 1, "one bar, replaced");
    assert.strictEqual(find(b, "dot"), undefined);
    assert.strictEqual(find(b, "pane-bar-msg").textContent, "Another tab or window took over this session.");
    assert.deepStrictEqual(labels(b), ["Take back"]);
    b.children.find((c) => c.tag === "button").click();
    assert.strictEqual(calls.connect, 1, "Take back reconnects");
  } finally { teardown(); }
});

test("desktop: ended is Restart primary + Close ghost with no dot; crashed adds only the red dot; removed has Close alone", () => {
  let s = setup();
  try {
    s.tab.markEnded(true);
    let b = bar(s.tab);
    assert.strictEqual(find(b, "pane-bar-msg").textContent, "Session ended");
    assert.strictEqual(find(b, "dot"), undefined);
    const [restart, close] = b.children.filter((c) => c.tag === "button");
    assert.strictEqual(restart.className, "btn btn-sm btn-primary");
    assert.strictEqual(close.className, "btn btn-sm btn-ghost");
    restart.click(); close.click();
    assert.deepStrictEqual([s.calls.restarted, s.calls.closed], [1, 1]);
    assert.ok(!/pane-ended|danger|exit/.test(b.className + b.textContent), "no legacy red bar, no exit word");
  } finally { teardown(); }
  s = setup({ activity: "crashed" });
  try {
    s.tab.markEnded(true);
    const b = bar(s.tab);
    assert.strictEqual(find(b, "pane-bar-msg").textContent, "Crashed");
    assert.strictEqual(find(b, "dot").className, "dot dot-crashed");
    assert.deepStrictEqual(labels(b), ["Restart", "Close"]);
  } finally { teardown(); }
  s = setup();
  try {
    s.tab.markEnded(false);
    const b = bar(s.tab);
    assert.strictEqual(find(b, "pane-bar-msg").textContent, "Removed");
    assert.deepStrictEqual(labels(b), ["Close"]);
  } finally { teardown(); }
});

test("desktop: an ended bar turns into Crashed when activity=crashed lands after the socket closed", () => {
  const { tab } = setup();
  try {
    tab.markEnded(true);
    assert.strictEqual(find(bar(tab), "pane-bar-msg").textContent, "Session ended");
    tab.setActivity("crashed");
    assert.strictEqual(tab.el.children.length, 1);
    assert.strictEqual(find(bar(tab), "pane-bar-msg").textContent, "Crashed");
  } finally { teardown(); }
});

test("phone: the legacy .pane-trouble and .pane-ended stay, with their old words", () => {
  let s = setup({ phone: true });
  try {
    s.tab.showTrouble("offline");
    const t = bar(s.tab);
    assert.strictEqual(t.className, "pane-trouble");
    assert.strictEqual(t.children[0].textContent, "Lost connection to the server.");
  } finally { teardown(); }
  s = setup({ phone: true });
  try {
    s.tab.markEnded(true);
    const e = bar(s.tab);
    assert.strictEqual(e.className, "pane-ended");
    assert.strictEqual(e.children[0].textContent, "session ended");
    assert.deepStrictEqual(labels(e), ["Restart", "Close"]);
  } finally { teardown(); }
});

test("signed out: scheduleRetry arms nothing and opens no socket; the bar has no countdown and no button", () => {
  const { tab, calls } = setup({ signedOut: true });
  try {
    tab.scheduleRetry();
    assert.strictEqual(tab.retryTimer == null, true);
    assert.strictEqual(calls.timers.length, 0);
    assert.strictEqual(calls.connect, 0);
    const b = bar(tab);
    assert.strictEqual(find(b, "pane-bar-msg").textContent, "Signed out · this terminal reconnects after you sign in");
    assert.deepStrictEqual(labels(b), []);
  } finally { teardown(); }
});

test("signed out after a retry was armed: the timer fires into a parked pane, not a socket; then the flag clearing reconnects", () => {
  const { tab, calls } = setup();
  try {
    tab.scheduleRetry();
    assert.strictEqual(calls.timers.length, 1);
    global.sys.signedOut = true; // a 401 landed on the sessions poll meanwhile
    calls.timers[0].fn();
    assert.strictEqual(calls.connect, 0, "no reconnect while signed out");
    assert.strictEqual(tab.retryAt, 0);
    assert.strictEqual(find(bar(tab), "pane-bar-msg").textContent, "Signed out · this terminal reconnects after you sign in");
    // app.js sysPollOk calls this when an OK poll clears the flag: it wakes the parked pane.
    global.sys.signedOut = false;
    tab.resumeAfterSignIn();
    assert.strictEqual(calls.connect, 1, "resume reconnects the parked pane once");
    tab.resumeAfterSignIn();
    assert.strictEqual(calls.connect, 1, "and only once");
  } finally { teardown(); }
});

test("a 401 probe (E9) clears the retry, tells the page once, and shows the signed-out bar", async () => {
  const { tab, calls } = setup();
  try {
    global.probeServer = async () => "unauthorized";
    global.sessionStatus = async () => { throw new Error("must not be asked"); };
    tab.scheduleRetry();
    assert.ok(tab.retryTimer, "armed");
    await tab.diagnose();
    assert.strictEqual(tab.retryTimer, null, "the retry is cleared on expired");
    assert.strictEqual(tab.retryAt, 0);
    assert.strictEqual(calls.noted, 1, "the one sys flag is set through sysNoteSignedOut");
    assert.strictEqual(find(bar(tab), "pane-bar-msg").textContent, "Signed out · this terminal reconnects after you sign in");
    assert.strictEqual(calls.connect, 0);
  } finally { teardown(); }
});

test("source pins: the pane bar painter reads only the model; app.js wakes parked panes when the flag clears", () => {
  const fs = require("node:fs"), path = require("node:path");
  const tt = fs.readFileSync(path.join(__dirname, "../static/terminal-tab.js"), "utf8");
  const painter = tt.slice(tt.indexOf("paintPaneBar(kind) {"), tt.indexOf("paneBarAct(id, btn) {"));
  assert.ok(!/innerHTML/.test(painter));
  assert.ok(!/["'](Lost connection|Session ended|Crashed|Restart|Close)["']/.test(painter.replace(/id === "(restart|close)"/g, "")), "no copy in the painter");
  const app = fs.readFileSync(path.join(__dirname, "../static/app.js"), "utf8");
  assert.match(app, /wasSignedOut[\s\S]{0,300}resumeAfterSignIn/);
});
