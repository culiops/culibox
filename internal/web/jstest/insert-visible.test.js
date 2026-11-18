"use strict";
// insertVisible, the one predicate behind every "insert a path at the prompt" (#337): a table of every state in which a
// terminal is NOT on screen, the positive cases, and the fail-closed rule (a field the glue forgot refuses). The glue
// that builds this input from the live layout, run on real Cell objects, is insert-decision.test.js.
const test = require("node:test");
const assert = require("node:assert");
const { insertVisible } = require("../static/insertvisible.js");

// The state of a terminal that is a tab of a pane and is plainly on screen. Each row below changes what it says.
const TAB = { name: "alpha", phone: false, hidden: false, page: "", placed: "tab", full: false, paneShown: true, view: "terminal", front: "self", laidOut: true };
// A shell docked in a pane, showing.
const DOCK = Object.assign({}, TAB, { placed: "dock", view: "terminal" });
const with_ = (base, over) => Object.assign({}, base, over);

test("a terminal on screen is visible: a pane's active tab, and the shell showing in its dock", () => {
  assert.deepStrictEqual(insertVisible(TAB), { ok: true, reason: "" });
  assert.deepStrictEqual(insertVisible(DOCK), { ok: true, reason: "" });
});

// [what the state is, the input, the sentence the user reads]. Every state of the #337 brief (section 3), then the ones
// the hunt for more added.
const REFUSED = [
  ["Full mode (the dock covers the pane area)", with_(TAB, { full: true }), "alpha's terminal is covered by the dock in Full mode; show the terminal first"],
  ["Full mode covers a docked shell too", with_(DOCK, { full: true }), "alpha's terminal is covered by the dock in Full mode; show the terminal first"],
  ["the pane shows Activity", with_(TAB, { view: "activity" }), "alpha's pane is showing Activity, not the terminal; show the terminal first"],
  ["the pane shows Access", with_(TAB, { view: "access" }), "alpha's pane is showing Access, not the terminal; show the terminal first"],
  ["another pane is maximized (this pane is detached)", with_(TAB, { paneShown: false }), "alpha's pane is hidden while another pane is maximized; show it first"],
  ["another pane is maximized, for a docked shell", with_(DOCK, { paneShown: false }), "alpha's pane is hidden while another pane is maximized; show it first"],
  ["an inactive stacked tab (another terminal is in front)", with_(TAB, { front: "tab" }), "alpha is a background tab in its pane; show it first"],
  ["a terminal behind a file viewer tab", with_(TAB, { front: "viewer" }), "alpha's terminal is behind a file tab in its pane; show it first"],
  ["a hidden docked shell", with_(DOCK, { front: "dock-hidden" }), "alpha's shell is hidden with its dock; show it first"],
  ["a docked shell that is not the one showing", with_(DOCK, { front: "shell" }), "alpha is not the shell showing in its dock; show it first"],
  ["Settings is open", with_(TAB, { page: "settings-modal" }), "Settings is open over the panes; close it first"],
  ["Analytics is open", with_(TAB, { page: "analytics-modal" }), "Analytics is open over the panes; close it first"],
  ["Audit is open", with_(TAB, { page: "audit-page" }), "Audit is open over the panes; close it first"],
  ["a page this build does not know is open", with_(TAB, { page: "something-new" }), "A page is open over the panes; close it first"],
  ["a page is open over a docked shell too", with_(DOCK, { page: "audit-page" }), "Audit is open over the panes; close it first"],
  ["the browser tab is in the background", with_(TAB, { hidden: true }), "This browser tab is in the background; come back to it first"],
  ["no box: collapsed, detached or display:none", with_(TAB, { laidOut: false }), "alpha's terminal is not on screen; show it first"],
  ["no box, for a docked shell", with_(DOCK, { laidOut: false }), "alpha's terminal is not on screen; show it first"],
  ["not in any pane", with_(TAB, { placed: "" }), "alpha is not open in a pane; open it first"],
];

for (const [what, state, reason] of REFUSED) {
  test("refused: " + what, () => {
    assert.deepStrictEqual(insertVisible(state), { ok: false, reason });
  });
}

test("a docked shell stays visible while its pane shows Activity or Access: a view covers the pane's own tab only", () => {
  assert.strictEqual(insertVisible(with_(DOCK, { view: "activity" })).ok, true);
  assert.strictEqual(insertVisible(with_(DOCK, { view: "access" })).ok, true);
});

test("with several states at once the refusal still names one of them, never ok", () => {
  const all = with_(TAB, { hidden: true, page: "audit-page", full: true, paneShown: false, view: "access", front: "viewer", laidOut: false });
  const r = insertVisible(all);
  assert.strictEqual(r.ok, false);
  assert.ok(r.reason.length > 0);
  // The page is the outermost thing on screen, so it is the one named.
  assert.match(insertVisible(with_(all, { hidden: false })).reason, /^Audit is open/);
});

test("fail closed: a field the glue forgot to report refuses", () => {
  for (const field of ["placed", "hidden", "page", "full", "paneShown", "front", "view", "laidOut"]) {
    const s = Object.assign({}, TAB);
    delete s[field];
    assert.strictEqual(insertVisible(s).ok, false, "without " + field);
  }
  for (const field of ["hidden", "page", "full", "paneShown", "front", "laidOut"]) {
    const s = Object.assign({}, DOCK);
    delete s[field];
    assert.strictEqual(insertVisible(s).ok, false, "a docked shell without " + field);
  }
  // A docked shell does not need a view: a view does not cover it.
  const noView = Object.assign({}, DOCK);
  delete noView.view;
  assert.strictEqual(insertVisible(noView).ok, true);
  for (const bad of [undefined, null, {}, 0, "", "visible"]) assert.strictEqual(insertVisible(bad).ok, false, JSON.stringify(bad));
});

test("fail closed: an unknown value is not visible either", () => {
  assert.strictEqual(insertVisible(with_(TAB, { front: "yes" })).ok, false);
  assert.strictEqual(insertVisible(with_(TAB, { view: "notes" })).ok, false);
  assert.strictEqual(insertVisible(with_(TAB, { placed: "overlay" })).ok, false);
  assert.strictEqual(insertVisible(with_(TAB, { full: 0 })).ok, false);
  assert.strictEqual(insertVisible(with_(TAB, { paneShown: 1 })).ok, false);
  assert.match(insertVisible(with_(TAB, { front: "yes" })).reason, /not showing/);
});

test("a refusal names the session; without a name it says 'The session'", () => {
  assert.match(insertVisible(with_(TAB, { full: true, name: "api work" })).reason, /^api work's terminal/);
  assert.match(insertVisible(with_(TAB, { full: true, name: "" })).reason, /^The session's terminal/);
});

test("the phone: one screen at a time, so the Files screen inserts at the focused terminal as it always did", () => {
  // The phone's layout fields (Full mode, views, maximize) do not exist there; the cross-screen insert is the feature.
  assert.deepStrictEqual(insertVisible(with_(TAB, { phone: true })), { ok: true, reason: "" });
  assert.deepStrictEqual(insertVisible(with_(TAB, { phone: true, laidOut: false, front: "viewer", hidden: false })), { ok: true, reason: "" });
  // A terminal that is not in any pane has no prompt on the phone either.
  assert.strictEqual(insertVisible(with_(TAB, { phone: true, placed: "" })).ok, false);
  // phone must be exactly true: a stray truthy value does not switch the checks off.
  assert.strictEqual(insertVisible(with_(TAB, { phone: "yes", full: true })).ok, false);
});
