"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  classifyPickerQuery, pickerRows,
  pickerReopenMode, pickerShouldSelectAll, pickerCollapseAt,
  pickerNextIndex, pickerEnterAction, dirPickerNote, spawnWsIdleNote, pinLastUsed,
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
  assert.deepStrictEqual(classifyPickerQuery("  /home/user/x  "), { mode: "path", value: "/home/user/x" });
});

test("anything else is a fuzzy search", () => {
  assert.deepStrictEqual(classifyPickerQuery("static"), { mode: "fuzzy", value: "static" });
  // "~" is not expanded here: the server indexes relative to home already, and
  // expanding client-side would need HOME_DIR, which makes this impure.
  assert.deepStrictEqual(classifyPickerQuery("~/culipulse"), { mode: "fuzzy", value: "~/culipulse" });
});

test("the empty state lists workspaces, in order", () => {
  const rows = pickerRows({
    mode: "empty",
    workspaces: [
      { label: "~ (home)", path: "/home/user" },
      { label: "culipulse", path: "/home/user/culipulse" },
    ],
    hits: [],
  });
  assert.deepStrictEqual(rows, [
    { label: "~ (home)", hint: "", path: "/home/user", kind: "workspace" },
    { label: "culipulse", hint: "", path: "/home/user/culipulse", kind: "workspace" },
  ]);
});

// listWorkspaces (server) offers every non-hidden depth-1 directory of $HOME —
// it was NOT changed by the project-root filtering, because it also feeds the
// New-session working-dir form, which is out of scope here (brain #77). So
// without this filter the picker contradicts itself: open it and "sdk" is on
// screen; type "sdk" and dirPickerNote reports nothing found, because the dir
// index doesn't record it. Filtered here, not server-side.
//
// "vendor" is included even though it never appears on THIS box's $HOME
// (no ~/vendor here): dpSkipWorkspaceNames is meant to be a superset of
// skipDirRecord's full key set, not just the names Task 1 introduced, so a
// literal $HOME/vendor doesn't reproduce the identical sdk/code-server
// self-contradiction this test exists to close.
test("the empty state list is filtered by the same names skipDirRecord suppresses", () => {
  const rows = pickerRows({
    mode: "empty",
    workspaces: [
      { label: "~ (home)", path: "/home/user" },
      { label: "code-server", path: "/home/user/code-server" },
      { label: "culipulse", path: "/home/user/culipulse" },
      { label: "sdk", path: "/home/user/sdk" },
      { label: "vendor", path: "/home/user/vendor" },
      { label: "worktrees", path: "/home/user/worktrees" },
      { label: ".zinit", path: "/home/user/.zinit" },
      { label: ".tmux", path: "/home/user/.tmux" },
      { label: ".open-next", path: "/home/user/.open-next" },
      { label: ".worktrees", path: "/home/user/.worktrees" },
    ],
    hits: [],
  });
  assert.deepStrictEqual(rows, [
    { label: "~ (home)", hint: "", path: "/home/user", kind: "workspace" },
    { label: "culipulse", hint: "", path: "/home/user/culipulse", kind: "workspace" },
  ]);
});

test("a fuzzy result shows the basename as the label and its parent as the hint", () => {
  // The whole relative path in both columns truncated the part that identifies
  // the directory — the same lesson the command palette already learned.
  const rows = pickerRows({
    mode: "fuzzy",
    workspaces: [],
    hits: [{ rel: "culiops-agent-box/internal/web/static", path: "/home/user/culiops-agent-box/internal/web/static" }],
  });
  assert.deepStrictEqual(rows, [{
    label: "static",
    hint: "culiops-agent-box/internal/web",
    path: "/home/user/culiops-agent-box/internal/web/static",
    kind: "hit",
  }]);
});

test("a top-level fuzzy result has no parent to show", () => {
  const rows = pickerRows({
    mode: "fuzzy", workspaces: [],
    hits: [{ rel: "culipulse", path: "/home/user/culipulse" }],
  });
  assert.deepStrictEqual(rows, [
    { label: "culipulse", hint: "", path: "/home/user/culipulse", kind: "hit" },
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
  assert.strictEqual(pickerReopenMode("/home/user/culipulse", false), "empty");
  // ...and it does not matter whether the list happened to be open already.
  assert.strictEqual(pickerReopenMode("/home/user/culipulse", true), "empty");
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
  // /home/user/culipulse -> /home/user/culXXXXXipulse.
  assert.strictEqual(pickerShouldSelectAll("/home/user/culipulse", true), true);
  assert.strictEqual(pickerShouldSelectAll("", true), true);
});

test("a drag inside the input is never collapsed to select-all", () => {
  // A range selection means the user picked a substring on purpose — even a
  // 14px drag. Selecting all here makes no part of a path editable in place.
  assert.strictEqual(pickerShouldSelectAll("/home/user/culipulse", false), false);
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
// the same silent disappearance. After Task 1 the dir index admits only two
// kinds of directory (10,049 -> 91 on a real home): a PROJECT ROOT anywhere
// under $HOME, or a direct, non-hidden CHILD of $HOME regardless of what it
// contains (Rule 1, mirroring listWorkspaces). So "I typed a real, visible
// directory that is neither" — two levels deep and not a project — is now the
// dominant way to get zero rows. The note has to name BOTH admitted kinds, not
// just "project folders" (that wording is false for ~/bin or ~/Downloads, a
// bug caught in review — it understates what the index actually offers), plus
// the escape hatch (a full path).

test("no matches names both admitted kinds and the escape hatch", () => {
  const note = dirPickerNote(0, false, false);
  assert.match(note, /project/i);
  assert.match(note, /top-level/i);
  assert.match(note, /full path/i);
});

test("no matches while truncated still reads as the admission rule, not as a broken index", () => {
  // Same zero-count copy whether or not the walk was truncated: truncation
  // does not explain a real, visible directory being absent — only "neither
  // admission rule matched it" does.
  assert.strictEqual(dirPickerNote(0, true, false), dirPickerNote(0, false, false));
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

// --- spawn dialog's idle caption (D5: "N project roots · type to filter ·
// ↵ to pick") -----------------------------------------------------------

test("spawnWsIdleNote pluralizes on the count", () => {
  assert.strictEqual(spawnWsIdleNote(92), "92 project roots · type to filter · ↵ to pick");
  assert.strictEqual(spawnWsIdleNote(1), "1 project root · type to filter · ↵ to pick");
  assert.strictEqual(spawnWsIdleNote(0), "0 project roots · type to filter · ↵ to pick");
});

test("spawnWsIdleNote treats a missing count as zero rather than throwing", () => {
  assert.strictEqual(spawnWsIdleNote(undefined), "0 project roots · type to filter · ↵ to pick");
});

// --- "Last used" first in the New session list (F38, frame M4.3): a reorder of rows the
// server returned, never a row the server did not list ------------------------------------

const lastRows = (n) => Array.from({ length: n }, (_, i) => ({ label: "p" + i, hint: "", path: "/home/u/p" + i, kind: "workspace" }));

test("pinLastUsed moves the last-used row from index 9 to index 0, marked, the others keep their order", () => {
  const rows = lastRows(12);
  const out = pinLastUsed(rows, "/home/u/p9");
  assert.strictEqual(out.length, 12);
  assert.deepStrictEqual(out[0], { label: "p9", hint: "", path: "/home/u/p9", kind: "workspace", lastUsed: true });
  assert.deepStrictEqual(out.slice(1).map((r) => r.label), ["p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p10", "p11"]);
  assert.ok(out.slice(1).every((r) => r.lastUsed === undefined), "only the pinned row is marked");
});

test("pinLastUsed returns a new array and copies the row: the input is never mutated", () => {
  const rows = lastRows(4);
  const snapshot = JSON.stringify(rows);
  const out = pinLastUsed(rows, "/home/u/p2");
  assert.notStrictEqual(out, rows);
  assert.notStrictEqual(out[0], rows[2], "the matched row is a copy");
  assert.strictEqual(JSON.stringify(rows), snapshot);
});

test("pinLastUsed with the row already first still marks it", () => {
  const out = pinLastUsed(lastRows(3), "/home/u/p0");
  assert.deepStrictEqual(out.map((r) => r.label), ["p0", "p1", "p2"]);
  assert.strictEqual(out[0].lastUsed, true);
});

test("pinLastUsed never invents a row: a last cwd the list does not hold changes nothing", () => {
  const rows = lastRows(5);
  assert.deepStrictEqual(pinLastUsed(rows, "/home/u/gone"), rows);
  assert.deepStrictEqual(pinLastUsed(rows, "/tmp/claude-1001/x"), rows);
  assert.deepStrictEqual(pinLastUsed([], "/home/u/p1"), []);
});

test("pinLastUsed: an empty, missing or non-string last cwd changes nothing", () => {
  const rows = lastRows(5);
  for (const v of ["", undefined, null, 0, {}]) assert.deepStrictEqual(pinLastUsed(rows, v), rows, String(v));
  assert.deepStrictEqual(pinLastUsed(undefined, "/home/u/p1"), []);
});

test("pinLastUsed matches a path with or without a trailing slash, on either side", () => {
  const rows = lastRows(5);
  assert.strictEqual(pinLastUsed(rows, "/home/u/p3/")[0].label, "p3");
  const slashed = [{ label: "a", hint: "", path: "/home/u/a/", kind: "hit" }, { label: "b", hint: "", path: "/home/u/b", kind: "hit" }];
  assert.strictEqual(pinLastUsed(slashed, "/home/u/b/")[0].label, "b");
  assert.strictEqual(pinLastUsed(slashed, "/home/u/a")[0].label, "a");
  // Whole paths only: "/home/u/p1" is not "/home/u/p10", and the root alone stays itself.
  assert.deepStrictEqual(pinLastUsed(lastRows(12), "/home/u/p1").map((r) => r.label).slice(0, 2), ["p1", "p0"]);
  assert.strictEqual(pinLastUsed([{ label: "/", hint: "", path: "/", kind: "hit" }, { label: "x", hint: "", path: "/x", kind: "hit" }], "/")[0].label, "/");
});

test("pinLastUsed pins fuzzy hits the same way as workspace rows", () => {
  const hits = pickerRows({ mode: "fuzzy", hits: [{ rel: "a/one", path: "/home/u/a/one" }, { rel: "b/two", path: "/home/u/b/two" }] });
  const out = pinLastUsed(hits, "/home/u/b/two");
  assert.deepStrictEqual(out.map((r) => [r.label, r.hint, !!r.lastUsed]), [["two", "b", true], ["one", "a", false]]);
});

// --- the Files drawer's root list closes when focus leaves it (gate fix #173, GATE3 finding 1) ----------------------------------
// initDirPicker is DOM wiring on module globals bound to #ft-root-input, so it runs in a vm context on a tiny fake DOM,
// with the real dirpicker.js. drawerDesktop() is app.js's gate for "not the phone"; it is a function here so each test
// picks the layout.
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

class FEl {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.attrs = {}; this.handlers = {};
    this.style = {}; this.hidden = false; this.className = ""; this.id = ""; this.title = ""; this.textContent = "";
    this.value = ""; this.selectionStart = 0; this.selectionEnd = 0;
  }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  removeAttribute(k) { delete this.attrs[k]; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  addEventListener(type, fn) { (this.handlers[type] = this.handlers[type] || []).push(fn); }
  append(...cs) { for (const c of cs) { c.parent = this; this.children.push(c); } }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.append(...cs); }
  contains(n) { for (let x = n; x; x = x.parent) if (x === this) return true; return false; }
  getBoundingClientRect() { return { left: 10, top: 100, bottom: 130, width: 300 }; }
  setSelectionRange(a, b) { this.selectionStart = a; this.selectionEnd = b; }
  select() { this.selectionStart = 0; this.selectionEnd = this.value.length; }
  scrollIntoView() {}
}
const fireOn = (el, type, e) => {
  const ev = Object.assign({ type, target: el, prevented: false, preventDefault() { this.prevented = true; } }, e || {});
  for (const f of (el.handlers[type] || []).slice()) f(ev);
  return ev;
};

function rootListEnv(desktop) {
  const doc = new FEl("document");
  const win = new FEl("window");
  win.innerHeight = 800;
  const input = new FEl("input"), list = new FEl("div");
  list.hidden = true;
  const els = { "ft-root-input": input, "ft-root-list": list };
  doc.getElementById = (id) => els[id] || null;
  doc.createElement = (t) => new FEl(t);
  const picks = [];
  const state = { desktop };
  const ctx = vm.createContext({
    document: doc, window: win, console, Promise, JSON, Math, Array, Object, String, AbortController,
    setTimeout: () => 1, clearTimeout: () => {}, fetch: async () => ({ ok: true, status: 200, json: async () => ({ results: [] }) }),
    drawerDesktop: () => state.desktop,
  });
  new vm.Script(fs.readFileSync(path.join(__dirname, "..", "static", "dirpicker.js"), "utf8"), { filename: "dirpicker.js" }).runInContext(ctx);
  ctx.initDirPicker({ workspaces: [{ label: "~ (home)", path: "/home/u" }, { label: "notes", path: "/home/u/notes" }], onPick: (p) => picks.push(p) });
  // The tree is a sibling of the input in the drawer: its row is the next stop of Tab. The drawer holds the whole pick bar.
  const drawer = new FEl("div");
  const bar = new FEl("div");
  drawer.append(bar);
  bar.append(input);
  bar.append(list);
  const treeRow = new FEl("div");
  drawer.append(treeRow);
  const open = () => { fireOn(input, "focus"); assert.strictEqual(list.hidden, false, "focus opens the list"); };
  return { ctx, input, list, drawer, bar, treeRow, picks, state, open };
}

test("desktop: Tab from the root input into the tree (focus moves to a focusable outside the input and the list) closes the list", () => {
  const t = rootListEnv(true);
  t.open();
  assert.strictEqual(t.input.attrs["aria-expanded"], "true");
  fireOn(t.input, "focusout", { relatedTarget: t.treeRow });
  assert.strictEqual(t.list.hidden, true);
  assert.strictEqual(t.input.attrs["aria-expanded"], "false");
  assert.strictEqual(t.input.attrs["aria-activedescendant"], undefined);
});

test("desktop: Shift+Tab out of the input (to a control before it) closes the list too", () => {
  const t = rootListEnv(true);
  t.open();
  fireOn(t.input, "focusout", { relatedTarget: new FEl("button") });
  assert.strictEqual(t.list.hidden, true);
});

test("desktop: a pointer pick still works: focus going nowhere (relatedTarget null), into the list, back to the input, or to a container around the list keeps the list", () => {
  const t = rootListEnv(true);
  for (const to of [null, undefined, t.list, t.list.children[0], t.input, t.bar, t.drawer]) {
    t.open();
    fireOn(t.input, "focusout", { relatedTarget: to });
    assert.strictEqual(t.list.hidden, false, "kept open for relatedTarget " + (to && to.tag));
    t.input.attrs["aria-expanded"] = "true";
  }
  // The row the pointer pressed is picked: the click handler runs on the rows the list still holds.
  assert.ok(t.list.children.length > 0);
  t.list.children[1].onclick();
  assert.deepStrictEqual(t.picks, ["/home/u/notes"]);
  assert.strictEqual(t.list.hidden, true);
});

test("desktop: the list's own focusout (a focusable list in some browsers) follows the same rule", () => {
  const t = rootListEnv(true);
  t.open();
  fireOn(t.list, "focusout", { relatedTarget: t.input });
  assert.strictEqual(t.list.hidden, false, "back to the input: stays");
  fireOn(t.list, "focusout", { relatedTarget: t.treeRow });
  assert.strictEqual(t.list.hidden, true, "out to the tree: closes");
});

test("desktop: Escape and an outside click behave as before", () => {
  const t = rootListEnv(true);
  t.open();
  fireOn(t.input, "keydown", { key: "Escape" });
  assert.strictEqual(t.list.hidden, true);
  t.open();
  fireOn(t.input, "click"); // the field's own click re-derives the list, it never closes it
  assert.strictEqual(t.list.hidden, false);
});

test("the phone is unchanged: focus leaving the input does not close the list (only Escape, a pick or an outside click do)", () => {
  const t = rootListEnv(false);
  t.open();
  fireOn(t.input, "focusout", { relatedTarget: t.treeRow });
  assert.strictEqual(t.list.hidden, false);
  assert.strictEqual(t.input.attrs["aria-expanded"], "true");
  t.state.desktop = true; // the same session across the breakpoint: the gate is read when focus moves
  fireOn(t.input, "focusout", { relatedTarget: t.treeRow });
  assert.strictEqual(t.list.hidden, true);
});

test("without the page's drawerDesktop (a bare script, a test) the list keeps today's behaviour", () => {
  const t = rootListEnv(true);
  t.open();
  t.ctx.drawerDesktop = undefined;
  fireOn(t.input, "focusout", { relatedTarget: t.treeRow });
  assert.strictEqual(t.list.hidden, false);
});
