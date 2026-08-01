"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  classifyPickerQuery, pickerRows,
  pickerReopenMode, pickerShouldSelectAll, pickerCollapseAt,
  pickerNextIndex, pickerEnterAction, dirPickerNote,
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

test("collapsing lands on the end, which cannot scroll the field under the pointer", () => {
  // The end is where select() already left the caret, so moving it there asks
  // the field to scroll nowhere. Collapsing to 0 asks it to jump to its head,
  // and a field that scrolls between the physical mousedown and the browser's
  // hit-test lands the caret on a different character than the one clicked.
  //
  // What was NOT observed: the browser pass measured scrollLeft = 0 throughout
  // on a 50-character value in a ~242px box, so that field never auto-scrolled
  // to its tail and the hazard was not reproduced there. The end is pinned here
  // because it is the free choice, not because the scroll was seen.
  assert.strictEqual(pickerCollapseAt(0, 50), 50);
  assert.notStrictEqual(pickerCollapseAt(0, 50), 0);
});

test("an already-collapsed caret is left exactly where it is", () => {
  // Nothing is in the way, so the gesture needs no help — and rewriting the
  // selection here would move a caret the user placed on a previous click.
  assert.strictEqual(pickerCollapseAt(17, 17), null);
  assert.strictEqual(pickerCollapseAt(0, 0), null);
});

// Arrow-key wrapping. Every fresh render leaves the highlight at -1, so "no
// highlight yet" is the COMMON starting state here, not an edge case.

test("the first ArrowUp lands on the last row, not the second-to-last", () => {
  // The naive (from + delta + n) % n reads -1 as a real position: (-1-1+5)%5
  // is 3, which silently skipped the last row on every fresh list.
  assert.strictEqual(pickerNextIndex(-1, -1, 5), 4);
  assert.strictEqual(pickerNextIndex(-1, -1, 1), 0);
});

test("the first ArrowDown still lands on the first row", () => {
  assert.strictEqual(pickerNextIndex(-1, 1, 5), 0);
  assert.strictEqual(pickerNextIndex(-1, 1, 1), 0);
});

test("wrapping from a real position is unchanged in both directions", () => {
  assert.strictEqual(pickerNextIndex(0, 1, 5), 1);
  assert.strictEqual(pickerNextIndex(4, 1, 5), 0);
  assert.strictEqual(pickerNextIndex(0, -1, 5), 4);
  assert.strictEqual(pickerNextIndex(3, -1, 5), 2);
});

test("an empty list has nothing to highlight", () => {
  assert.strictEqual(pickerNextIndex(-1, 1, 0), -1);
  assert.strictEqual(pickerNextIndex(-1, -1, 0), -1);
});

// Enter with NO row highlighted. "Take row 0" is only defensible when row 0 is
// the best match for something the user actually typed.

test("Enter on a typed absolute path asks the server what it is", () => {
  assert.strictEqual(pickerEnterAction("path", 0), "resolve");
  // Even with rows on screen, a path outranks them.
  assert.strictEqual(pickerEnterAction("path", 7), "resolve");
});

test("Enter on a fuzzy query takes the best match", () => {
  assert.strictEqual(pickerEnterAction("fuzzy", 3), "first");
});

test("Enter on a fuzzy query with no hits does nothing", () => {
  assert.strictEqual(pickerEnterAction("fuzzy", 0), "none");
});

test("Enter on a blank box does NOT re-root at the first workspace", () => {
  // "empty" mode renders the workspace list, which the user never searched
  // for. Falling through to row 0 there re-rooted at home the instant Enter
  // was pressed on an empty box, with nothing highlighted to explain the jump.
  assert.strictEqual(pickerEnterAction("empty", 4), "none");
  assert.strictEqual(pickerEnterAction("empty", 0), "none");
});

// The picker must never answer "no matches", "search broke" and "offline" with
// the same silent disappearance — especially since the index suppresses vendor/
// subtrees (~39% of directories), making zero hits a NORMAL outcome.

test("no matches says so instead of closing the list", () => {
  assert.strictEqual(dirPickerNote(0, false, false), "no matching folders");
});

test("a failed search is distinguishable from an empty one", () => {
  assert.strictEqual(dirPickerNote(0, false, true), "folder search unavailable");
  // Failure wins even if the walk had reported truncation earlier.
  assert.strictEqual(dirPickerNote(0, true, true), "folder search unavailable");
});

test("a truncated index is surfaced alongside real hits", () => {
  // dirsTruncated was computed, bug-fixed and unit-tested server-side, then
  // discarded by the client. Hits plus a warning, not hits alone.
  assert.strictEqual(dirPickerNote(12, true, false),
    "index truncated: some folders are not listed");
});

test("a complete result set carries no note at all", () => {
  assert.strictEqual(dirPickerNote(12, false, false), "");
});
