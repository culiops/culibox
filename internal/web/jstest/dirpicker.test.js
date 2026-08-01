"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  classifyPickerQuery, pickerRows,
  pickerReopenMode, pickerShouldSelectAll, pickerCollapseAt,
} = require("../static/dirpicker.js");

// The picker has three input classes and they decide everything downstream:
// what is fetched, what is rendered, and what Enter does.
test("an empty or blank query means the workspace list", () => {
  assert.deepStrictEqual(classifyPickerQuery(""), { mode: "empty", value: "" });
  assert.deepStrictEqual(classifyPickerQuery("   "), { mode: "empty", value: "" });
  assert.deepStrictEqual(classifyPickerQuery(null), { mode: "empty", value: "" });
});

test("a leading slash means an absolute path, and is trimmed", () => {
  assert.deepStrictEqual(classifyPickerQuery("/etc/hosts"), { mode: "path", value: "/etc/hosts" });
  assert.deepStrictEqual(classifyPickerQuery("  /home/claude/x  "), { mode: "path", value: "/home/claude/x" });
});

test("anything else is a fuzzy search", () => {
  assert.deepStrictEqual(classifyPickerQuery("static"), { mode: "fuzzy", value: "static" });
  // "~" is not expanded here: the server indexes relative to home already, and
  // expanding client-side would need HOME_DIR, which makes this impure.
  assert.deepStrictEqual(classifyPickerQuery("~/culipulse"), { mode: "fuzzy", value: "~/culipulse" });
});

test("the empty state lists workspaces, unfiltered and in order", () => {
  const rows = pickerRows({
    mode: "empty",
    workspaces: [
      { label: "~ (home)", path: "/home/claude" },
      { label: "culipulse", path: "/home/claude/culipulse" },
    ],
    hits: [],
  });
  assert.deepStrictEqual(rows, [
    { label: "~ (home)", hint: "", path: "/home/claude", kind: "workspace" },
    { label: "culipulse", hint: "", path: "/home/claude/culipulse", kind: "workspace" },
  ]);
});

test("a fuzzy result shows the basename as the label and its parent as the hint", () => {
  // The whole relative path in both columns truncated the part that identifies
  // the directory — the same lesson the command palette already learned.
  const rows = pickerRows({
    mode: "fuzzy",
    workspaces: [],
    hits: [{ rel: "culiops-agent-box/internal/web/static", path: "/home/claude/culiops-agent-box/internal/web/static" }],
  });
  assert.deepStrictEqual(rows, [{
    label: "static",
    hint: "culiops-agent-box/internal/web",
    path: "/home/claude/culiops-agent-box/internal/web/static",
    kind: "hit",
  }]);
});

test("a top-level fuzzy result has no parent to show", () => {
  const rows = pickerRows({
    mode: "fuzzy", workspaces: [],
    hits: [{ rel: "culipulse", path: "/home/claude/culipulse" }],
  });
  assert.deepStrictEqual(rows, [
    { label: "culipulse", hint: "", path: "/home/claude/culipulse", kind: "hit" },
  ]);
});

test("path mode renders no rows — the server decides what that path is", () => {
  const rows = pickerRows({ mode: "path", workspaces: [{ label: "x", path: "/x" }], hits: [] });
  assert.deepStrictEqual(rows, []);
});

// Focus and click on the input both have to answer two questions, and getting
// either wrong has broken this control repeatedly. Both answers are pure, so
// they are pinned here rather than left to a browser pass to rediscover.

test("reopening shows the workspace list for a blank value and for a settled path", () => {
  // The steady state is a path: setFtRoot mirrors the picked root into the
  // input, so "there is a path in the box" must still offer the workspaces.
  assert.strictEqual(pickerReopenMode("", false), "empty");
  assert.strictEqual(pickerReopenMode("   ", false), "empty");
  assert.strictEqual(pickerReopenMode("/home/claude/culipulse", false), "empty");
  // ...and it does not matter whether the list happened to be open already.
  assert.strictEqual(pickerReopenMode("/home/claude/culipulse", true), "empty");
});

test("reopening onto a fuzzy value re-runs that search instead of the workspaces", () => {
  // Type "culi", click the terminal (the list closes), click back: rendering
  // the workspaces here would leave the list describing a different search
  // from the text, and Enter would then re-root at whichever workspace is
  // first while "culi" is still on screen.
  assert.strictEqual(pickerReopenMode("culi", false), "search");
  assert.strictEqual(pickerReopenMode("~/culipulse", false), "search");
});

test("reopening leaves an open fuzzy list alone", () => {
  // Clicking to fix a typo mid-query must not refetch, rebuild the rows, or
  // drop the highlight the user just arrowed onto.
  assert.strictEqual(pickerReopenMode("culi", true), "none");
});

test("a plain click on a path selects all, so typing replaces it", () => {
  // A click into an already-focused field fires no focus event; without this
  // select-all, typing "XXXXX" spliced into the value:
  // /home/claude/culipulse -> /home/claude/culXXXXXipulse.
  assert.strictEqual(pickerShouldSelectAll("/home/claude/culipulse", true), true);
  assert.strictEqual(pickerShouldSelectAll("", true), true);
});

test("a drag inside the input is never collapsed to select-all", () => {
  // A range selection means the user picked a substring on purpose — even a
  // 14px drag. Selecting all here makes no part of a path editable in place.
  assert.strictEqual(pickerShouldSelectAll("/home/claude/culipulse", false), false);
  assert.strictEqual(pickerShouldSelectAll("culi", false), false);
});

test("a click inside a live fuzzy query keeps the query", () => {
  // Here a click is for moving the caret, not starting over; select-all would
  // discard the search on the very next keystroke.
  assert.strictEqual(pickerShouldSelectAll("culi", true), false);
});

// A drag can only carve out a substring if nothing is selected when it starts:
// a mousedown inside a selection is read as grabbing that text, not as the
// start of a new selection. Since a plain click deliberately leaves the whole
// value selected, every following drag begins in exactly that trap unless the
// selection is cleared first.

test("a selection in the way is collapsed before a pointer gesture starts", () => {
  // The steady state after any click on a 50-character path.
  assert.strictEqual(pickerCollapseAt(0, 50), 50);
  // A range left by an earlier drag is equally in the way.
  assert.strictEqual(pickerCollapseAt(6, 14), 14);
});

test("collapsing lands on the end, so an overflowing field does not scroll", () => {
  // select() scrolls an overflowing field to show its tail, so the end is
  // already on screen. Collapsing to 0 would scroll the text under the pointer
  // between the physical mousedown and the browser's hit-test, landing the
  // caret on a different character than the one clicked.
  assert.strictEqual(pickerCollapseAt(0, 50), 50);
  assert.notStrictEqual(pickerCollapseAt(0, 50), 0);
});

test("an already-collapsed caret is left exactly where it is", () => {
  // Nothing is in the way, so the gesture needs no help — and rewriting the
  // selection here would move a caret the user placed on a previous click.
  assert.strictEqual(pickerCollapseAt(17, 17), null);
  assert.strictEqual(pickerCollapseAt(0, 0), null);
});
