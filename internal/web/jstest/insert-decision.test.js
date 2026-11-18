"use strict";
// insertDecision (insertdecision.js, #337): the one decision behind every insert, run on REAL Cell objects. Each state of
// the #337 brief section 3 is made the way the app makes it (a real pane's tabs, view, dock and maximize; the page and
// Full mode as the real code reads them) and the decision must refuse it with the sentence that names it. insertpage.js
// says what is real and what is stood in. insert-visible.test.js is the predicate's own table.
const test = require("node:test");
const assert = require("node:assert");
const { page } = require("./insertpage.js");

const OK = { ok: true, why: "" };
const refused = (why) => ({ ok: false, why });

// Two panes, A and B, side by side; B has focus. Both sessions idle.
function twoPanes() {
  const p = page();
  p.names({ a: "alpha", b: "beta" });
  const ta = p.term("a", "alpha");
  const tb = p.term("b", "beta");
  const ca = p.pane(ta);
  const cb = p.pane(tb);
  p.focus(cb);
  return Object.assign(p, { ta, tb, ca, cb });
}

// --- the gate, on the target's own session ------------------------------------------------------------------------------

test("insertGate uses the target session: a terminal for A while B has focus is judged by A's state", () => {
  const p = twoPanes();
  p.sessions(p.sess("a", { name: "alpha", activity: "waiting", need: "ask" }), p.sess("b", { name: "beta" }));
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha is waiting on you; answer it first"), "A waits: refused though B (focused) is idle");
  assert.deepStrictEqual(p.decide(p.tb), OK);
  p.sessions(p.sess("a", { name: "alpha" }), p.sess("b", { name: "beta", activity: "waiting", need: "ask" }));
  assert.deepStrictEqual(p.decide(p.ta), OK, "B (focused) waits: A is still fine");
  assert.deepStrictEqual(p.decide(p.tb), refused("beta is waiting on you; answer it first"));
});

test("the gate's rule is editorref's: a finished turn passes, a question or a sign-in does not, an exited session is refused", () => {
  const p = twoPanes();
  const say = (row) => { p.sessions(Object.assign(p.sess("a", { name: "alpha" }), row)); return p.decide(p.ta); };
  assert.deepStrictEqual(say({ activity: "waiting", need: "turn" }), OK);
  assert.deepStrictEqual(say({ activity: "working" }), OK);
  for (const need of ["ask", "sso", "store_locked"]) assert.strictEqual(say({ activity: "waiting", need }).ok, false, need);
  assert.deepStrictEqual(say({ exited: true, activity: "done" }), refused("alpha has exited"));
});

test("a session with no row yet (just spawned) is refused by name, not typed into", () => {
  const p = twoPanes();
  p.sessions("b"); // no row for A
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha is not ready for input"));
});

test("the gate is asked first: a hidden terminal whose session waits on you says so", () => {
  const p = twoPanes();
  p.full(true);
  p.sessions(p.sess("a", { name: "alpha", activity: "waiting", need: "ask" }), "b");
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha is waiting on you; answer it first"));
});

test("no terminal at all is a refusal too", () => {
  const p = twoPanes();
  assert.strictEqual(p.decide(null).ok, false);
  assert.strictEqual(p.decide(undefined).ok, false);
});

// --- visible: the positive cases ------------------------------------------------------------------------------------------

test("a terminal that is the active tab of a pane on screen is visible, focused or not", () => {
  const p = twoPanes();
  assert.deepStrictEqual(p.decide(p.ta), OK, "the unfocused pane's terminal");
  assert.deepStrictEqual(p.decide(p.tb), OK, "the focused pane's");
});

test("a shell showing in its pane's dock is visible", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", sh: "shell 1" });
  const sh = p.ca.dockShell("sh", "shell 1");
  assert.strictEqual(p.ca._shownShell(), sh, "the real dock shows it");
  assert.deepStrictEqual(p.decide(sh), OK);
});

test("an open Files dock that is not in Full mode leaves the panes visible (it is a column beside them)", () => {
  const p = twoPanes();
  p.drawer.hidden = false; // open, pushed: no data-max
  assert.deepStrictEqual(p.decide(p.ta), OK);
  p.full(false);
  assert.deepStrictEqual(p.decide(p.ta), OK);
  // A stale Full flag on a closed dock covers nothing (applyDrawer clears it on close; the glue does not trust it either).
  p.drawer.hidden = true;
  p.drawer.dataset.max = "1";
  assert.deepStrictEqual(p.decide(p.ta), OK);
});

// --- the states of the #337 brief, section 3 ---------------------------------------------------------------------------

test("Full mode: the dock covers the pane area, forced or chosen, and text for a pane tab or a docked shell is refused", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", sh: "shell 1" });
  const sh = p.ca.dockShell("sh", "shell 1");
  p.full(true);
  const why = "alpha's terminal is covered by the dock in Full mode; show the terminal first";
  assert.deepStrictEqual(p.decide(p.ta), refused(why));
  assert.strictEqual(p.decide(p.tb).ok, false);
  assert.strictEqual(p.decide(sh).ok, false, "the dock covers the docked shells too");
  p.drawer.dataset.forced = "1"; // forced Full is the same overlay
  assert.deepStrictEqual(p.decide(p.ta), refused(why));
  p.full(false);
  assert.deepStrictEqual(p.decide(p.ta), OK, "Exit full: visible again");
});

test("a pane's Activity or Access view hides its terminal, not the shell docked beside it", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", sh: "shell 1" });
  const sh = p.ca.dockShell("sh", "shell 1");
  p.ca.setView("activity");
  assert.strictEqual(p.ca.view, "activity", "the real Cell took the view");
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha's pane is showing Activity, not the terminal; show the terminal first"));
  assert.deepStrictEqual(p.decide(sh), OK, "the view covers the pane's agent half; the dock stays");
  assert.deepStrictEqual(p.decide(p.tb), OK, "another pane is not affected");
  p.ca.setView("access");
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha's pane is showing Access, not the terminal; show the terminal first"));
  p.ca.setView("terminal");
  assert.deepStrictEqual(p.decide(p.ta), OK, "back on Terminal");
});

test("another pane maximized detaches this one: only the maximized pane's terminal is visible", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", sh: "shell 1" });
  const sh = p.ca.dockShell("sh", "shell 1");
  p.maximize(p.cb);
  assert.strictEqual(p.ca.el.isConnected, false, "the real renderGrid took pane A out of the document");
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha's pane is hidden while another pane is maximized; show it first"));
  assert.strictEqual(p.decide(sh).ok, false, "and its docked shell");
  assert.deepStrictEqual(p.decide(p.tb), OK);
  p.maximize(p.cb); // un-maximize
  assert.deepStrictEqual(p.decide(p.ta), OK);
});

test("an inactive stacked tab is not visible, even though its socket is open: only the active tab is in the page", () => {
  const p = twoPanes();
  const tc = p.term("c", "gamma");
  p.names({ a: "alpha", b: "beta", c: "gamma" });
  p.cb.addTab(tc); // stacked in B's pane: gamma is now the active tab
  assert.strictEqual(p.cb.activeTab, tc);
  assert.deepStrictEqual(p.decide(p.tb), refused("beta is a background tab in its pane; show it first"));
  assert.deepStrictEqual(p.decide(tc), OK);
  p.cb.activate(p.tb);
  assert.deepStrictEqual(p.decide(p.tb), OK);
  assert.deepStrictEqual(p.decide(tc), refused("gamma is a background tab in its pane; show it first"));
});

test("a terminal behind a file viewer tab is not visible (cellTerminal still names it)", () => {
  const p = twoPanes();
  p.cb.addTab(p.viewer("/work/main.go"));
  assert.strictEqual(p.ctx.cellTerminal(p.cb), p.tb, "the pane still says it is about beta");
  assert.deepStrictEqual(p.decide(p.tb), refused("beta's terminal is behind a file tab in its pane; show it first"));
  p.cb.activate(p.tb);
  assert.deepStrictEqual(p.decide(p.tb), OK);
});

test("a hidden docked shell is not visible, and neither is a docked shell that another shell has replaced", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", sh: "shell 1", sh2: "shell 2" });
  const sh = p.ca.dockShell("sh", "shell 1");
  const sh2 = p.ca.dockShell("sh2", "shell 2");
  assert.strictEqual(p.ca._shownShell(), sh2);
  assert.deepStrictEqual(p.decide(sh), refused("shell 1 is not the shell showing in its dock; show it first"));
  assert.deepStrictEqual(p.decide(sh2), OK);
  p.ca.hideDock();
  assert.strictEqual(p.ca._shownShell(), null, "the real dock is hidden");
  assert.deepStrictEqual(p.decide(sh2), refused("shell 2's shell is hidden with its dock; show it first"));
  assert.strictEqual(p.decide(sh).ok, false);
  p.ca.activateShell("sh");
  assert.deepStrictEqual(p.decide(sh), OK);
});

test("a page view (Settings, Analytics, Audit) covers the panes and the dock", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", sh: "shell 1" });
  const sh = p.ca.dockShell("sh", "shell 1");
  for (const [id, label] of [["settings-modal", "Settings"], ["analytics-modal", "Analytics"], ["audit-page", "Audit"]]) {
    p.openPage(id);
    assert.strictEqual(p.ctx.currentPage(), id, "pagehost.js's own answer");
    const why = label + " is open over the panes; close it first";
    assert.deepStrictEqual(p.decide(p.ta), refused(why));
    assert.deepStrictEqual(p.decide(p.tb), refused(why));
    assert.deepStrictEqual(p.decide(sh), refused(why));
    p.closePage();
    assert.deepStrictEqual(p.decide(p.ta), OK);
  }
});

test("a terminal that is in no pane (closed, or never opened here) has no prompt to insert at", () => {
  const p = twoPanes();
  const lost = p.term("a", "alpha"); // another TerminalTab for A: not the one in any pane
  assert.deepStrictEqual(p.decide(lost), refused("alpha is not open in a pane; open it first"));
  p.cb.closeTab(p.tb);
  assert.strictEqual(p.decide(p.tb).ok, false, "a closed tab");
});

// --- states the hunt for more added ------------------------------------------------------------------------------------

test("the browser tab in the background: nothing is on screen", () => {
  const p = twoPanes();
  p.doc.hidden = true;
  assert.deepStrictEqual(p.decide(p.ta), refused("This browser tab is in the background; come back to it first"));
  p.doc.hidden = false;
  assert.deepStrictEqual(p.decide(p.ta), OK);
});

test("a pane squeezed to nothing, or anything else the CSS hides, has no box: refused as not on screen", () => {
  const p = twoPanes();
  p.ca.el.rect = { width: 0, height: 600 }; // pane A collapsed
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha's terminal is not on screen; show it first"));
  assert.deepStrictEqual(p.decide(p.tb), OK, "its neighbour is untouched");
  p.ca.el.rect = { width: 800, height: 600 };
  p.ta.el.rect = { width: 800, height: 0 };
  assert.strictEqual(p.decide(p.ta).ok, false);
  p.ta.el.rect = { width: 800, height: 600 };
  assert.deepStrictEqual(p.decide(p.ta), OK);
  // The grid hidden as a whole (a rule the glue does not know by name): still refused.
  p.grid.rect = { width: 0, height: 0 };
  assert.strictEqual(p.decide(p.ta).ok, false);
  assert.strictEqual(p.decide(p.tb).ok, false);
});

test("a terminal moved between a tab and a dock is judged where it is now", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", x: "xray" });
  const tx = p.term("x", "xray");
  p.cb.addTab(tx);
  p.cb.activate(p.tb);
  assert.strictEqual(p.decide(tx).ok, false, "a background tab of B's pane");
  const docked = p.ca.dockShell("x", "xray"); // moves the same TerminalTab into A's dock (takeTerminalTab)
  assert.strictEqual(docked, tx);
  assert.strictEqual(p.ctx.terminalTabFor("x"), tx);
  assert.deepStrictEqual(p.decide(tx), OK, "now the shell showing in A's dock");
});

// --- the phone: strict #337, no exemption -------------------------------------------------------------------------------------
// The phone shows one screen at a time and draws no dock. insertpage.js applies app.css's rules for what that does to a
// terminal's box (the Files and Sessions screens hide #grid, the Terminal screen shows only the focused cell, the dock is
// display:none) and the real Cell, tabs and chips do the rest. The decision is the same code as on a desktop.

test("the phone's Terminal screen: the focused cell's active terminal is on screen", () => {
  const p = twoPanes();
  p.phone("terminal");
  assert.deepStrictEqual(p.decide(p.tb), OK);
});

test("the phone's Files screen hides every terminal: the grid has no box", () => {
  const p = twoPanes();
  p.phone("files");
  assert.strictEqual(p.ctx.focusedTerminal(), p.tb, "the focused terminal is the one a tree insert would use");
  assert.deepStrictEqual(p.decide(p.tb), refused("beta's terminal is not on screen; show it first"));
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha's terminal is not on screen; show it first"));
  p.phone("sessions");
  assert.strictEqual(p.decide(p.tb).ok, false, "and the Sessions screen");
  p.phone("terminal");
  assert.deepStrictEqual(p.decide(p.tb), OK);
});

test("on the phone's Terminal screen a pane that is not the focused cell is display:none", () => {
  const p = twoPanes();
  p.phone("terminal");
  assert.deepStrictEqual(p.decide(p.ta), refused("alpha's terminal is not on screen; show it first"));
  p.focus(p.ca);
  assert.deepStrictEqual(p.decide(p.ta), OK, "focus moves, the visible cell moves with it");
  assert.strictEqual(p.decide(p.tb).ok, false);
});

test("a shell docked in a pane is never drawn on the phone: refused, shown or not", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", sh: "shell 1" });
  const sh = p.cb.dockShell("sh", "shell 1");
  p.phone("terminal");
  assert.strictEqual(p.cb._shownShell(), sh, "the real dock model says it is showing");
  assert.deepStrictEqual(p.decide(sh), refused("shell 1's terminal is not on screen; show it first"));
  p.cb.hideDock();
  assert.deepStrictEqual(p.decide(sh), refused("shell 1's shell is hidden with its dock; show it first"));
});

test("an inactive stacked tab on the phone is refused: one chip is showing", () => {
  const p = twoPanes();
  p.names({ a: "alpha", b: "beta", c: "gamma" });
  const tc = p.term("c", "gamma");
  p.cb.addTab(tc);
  p.phone("terminal");
  assert.deepStrictEqual(p.decide(p.tb), refused("beta is a background tab in its pane; show it first"));
  assert.deepStrictEqual(p.decide(tc), OK);
});

test("a file viewer tab in front of the terminal on the phone is refused (the terminal is behind it)", () => {
  const p = twoPanes();
  p.cb.addTab(p.viewer("/work/main.go"));
  p.phone("terminal");
  assert.deepStrictEqual(p.decide(p.tb), refused("beta's terminal is behind a file tab in its pane; show it first"));
});

test("the phone is gated by the session's state too, and a terminal in no pane has no prompt there", () => {
  const p = twoPanes();
  p.phone("terminal");
  p.sessions("a", p.sess("b", { name: "beta", activity: "waiting", need: "ask" }));
  assert.deepStrictEqual(p.decide(p.tb), refused("beta is waiting on you; answer it first"));
  assert.strictEqual(p.decide(p.term("a", "alpha")).ok, false);
});
