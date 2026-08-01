"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// The file tree follows the focused pane, and syncFocusedTree() is what makes it
// do so. Nothing at runtime checks that a mutation which changes the focused
// pane remembered to call it — the symptom is a tree rooted at a directory
// nobody is looking at, and setFocusedCell()'s equality guard then eats every
// click that would have repaired it ("I clicked the window many times and
// nothing happened"). That is how this bug arrived once already.
//
// So pin the invariant at the source level, the way no-native-dialogs.test.js
// pins the dialog ban. tabs.js needs a DOM and cannot be require()d, and this
// repo carries no npm dependencies at all (no package.json, no jsdom) — see
// scripts/lint.sh, which solves the same problem the same way.
const TABS = path.join(__dirname, "..", "static", "tabs.js");

// A mutation is any function body that re-points focus (setFocusedCellRaw) or
// swaps which tab a cell shows (.activeTab =). Those are the two ways the
// focused TERMINAL can change without setFocusedCell() being reached.
// [^=] so a comparison (`.activeTab === tab`) is not read as an assignment.
const MUTATES = /setFocusedCellRaw\s*\(|\.activeTab\s*=[^=]/;
// It discharges the duty by syncing itself, or by ending in setFocusedCell(),
// which syncs on its own. Note this does NOT match setFocusedCellRaw(: the `(`
// must follow the name directly, and there an `R` does.
const SYNCS = /syncFocusedTree\s*\(|setFocusedCell\s*\(/;

// Exempt, each for a stated reason — an exemption must be a decision, never an
// omission:
//   setFocusedCellRaw  the funnel itself. It is called from inside mutations
//                      that render afterwards, so it MUST stay side-effect-free.
//   ensureFirstCell    a helper, never an entry point: every caller (removeCell,
//                      applyCap, focusedCellOrCreate) is itself covered by this
//                      scan, in its own right. (Not "the cell is always
//                      tabless" — focusedCellOrCreate calls it with `cells`
//                      non-empty, so setFocusedCellRaw(cells[0]) can land on a
//                      cell that already has tabs.)
//   constructor        belt-and-braces, not load-bearing. Cell's constructor
//                      already satisfies SYNCS via the setFocusedCell(this)
//                      inside its mousedown listener, so this exemption pins a
//                      pass that currently happens for an unrelated reason —
//                      it changes no test result today, but it means moving
//                      that listener out of line can't silently turn the
//                      constructor into a false positive.
//
// This is a text scan over function bodies, not a data-flow analysis: it proves
// a mutation MENTIONS a sync call somewhere in its body, not that the sync runs
// on every path through it. That is the bound of what this test buys. Three more
// edges worth knowing:
//   - namedBodies' regex requires `name(...) {` at the start of a line, so an
//     arrow-function mutator (`const swapTabs = (a, b) => {`, or one nested
//     inside another body) is invisible to the scan and the test stays green.
//   - MUTATES and SYNCS both match text inside comments, so a comment reading
//     `// TODO: call syncFocusedTree()` would discharge the duty.
//   - The scan covers tabs.js only. Verified today that `activeTab =`,
//     `setFocusedCellRaw` and `focusedCell =` appear in no other file under
//     internal/web/static/, but nothing enforces that scope going forward.
const EXEMPT = new Set(["setFocusedCellRaw", "ensureFirstCell", "constructor"]);

// Every function the scan must see. Asserted below so a parser that quietly
// stops finding bodies fails loudly instead of reporting a green empty set.
const KNOWN = ["setFocusedCell", "activate", "closeTab", "removeCell", "applyCap", "applyPlan"];

// namedBodies splits a source file into { name, body } for each named function
// or class method. Brace matching is naive — it does not know about braces
// inside strings or comments — which is exactly why KNOWN is asserted.
function namedBodies(src) {
  const KEYWORDS = new Set(["if", "for", "while", "switch", "catch", "return", "typeof"]);
  const re = /(?:^|\n)[ \t]*(?:async[ \t]+)?(?:function[ \t]+)?([A-Za-z_$][\w$]*)[ \t]*\([^)]*\)[ \t]*\{/g;
  const out = [];
  let m;
  while ((m = re.exec(src)) !== null) {
    if (KEYWORDS.has(m[1])) continue;
    const start = m.index + m[0].length - 1; // m[0] ends at the opening brace
    let depth = 0, i = start;
    for (; i < src.length; i++) {
      if (src[i] === "{") depth++;
      else if (src[i] === "}" && --depth === 0) break;
    }
    out.push({ name: m[1], body: src.slice(start, i + 1) });
  }
  return out;
}

// offendersIn is the rule itself, kept separate from the file it is applied to
// so the "guard bites" test below can run it against a fixture.
function offendersIn(src) {
  return namedBodies(src)
    .filter((b) => !EXEMPT.has(b.name) && MUTATES.test(b.body) && !SYNCS.test(b.body))
    .map((b) => b.name);
}

test("every focus mutation in tabs.js re-roots the file tree", () => {
  const src = fs.readFileSync(TABS, "utf8");
  const names = new Set(namedBodies(src).map((b) => b.name));
  for (const k of KNOWN) {
    assert.ok(names.has(k), "the scan did not find " + k + "() in tabs.js — it is not seeing what it claims to");
  }
  assert.deepStrictEqual(offendersIn(src), [],
    "these change the focused pane without re-rooting the file tree");
});

// A guard that has never been seen to fail proves nothing. Pin both that it
// catches the two real shapes and that it clears the four legitimate ones.
test("the guard catches a mutation that forgets to sync", () => {
  const fixture = [
    "function setFocusedCellRaw(cell) { focusedCell = cell; }",
    "function ensureFirstCell() { setFocusedCellRaw(cells[0]); }",
    "function setFocusedCell(cell) { setFocusedCellRaw(cell); syncFocusedTree(); }",
    "function activate(tab) { this.activeTab = tab; syncFocusedTree(); }",
    "function closeTab(tab) { this.activeTab = this.tabs[0]; }",
    "function removeCell(cell) { setFocusedCellRaw(null); }",
    "function applyCap(n) { setFocusedCellRaw(keep); syncFocusedTree(); }",
    "function applyPlan(p) { setFocusedCellRaw(null); setFocusedCell(t); }",
  ].join("\n");
  assert.deepStrictEqual(offendersIn(fixture).sort(), ["closeTab", "removeCell"]);
});

// An equality test must not read as an assignment, or a future `if
// (this.activeTab === tab)` would be reported as an unsynced mutation.
test("comparing activeTab is not a mutation", () => {
  assert.deepStrictEqual(offendersIn("function peek(t) { return this.activeTab === t; }"), []);
});
