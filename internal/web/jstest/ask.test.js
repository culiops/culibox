"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { askOutcome, matchesRequired, promptValue, shouldRefocusTerminal, askIsBusy } =
  require("../static/shell.js");

// A <dialog> closed by Esc, by a backdrop click, or by dialog.close() with no
// argument reports returnValue "". Only the confirm button sets it, so anything
// that is not exactly "confirm" must cancel: a dialog that failed open would
// delete a folder nobody agreed to delete.
test("only an explicit confirm counts as confirmation", () => {
  assert.strictEqual(askOutcome("confirm"), "confirm");
  for (const v of ["", "cancel", "Confirm", " confirm", undefined, null]) {
    assert.strictEqual(askOutcome(v), "cancel", String(v));
  }
});

test("the required name must match exactly once trimmed", () => {
  assert.strictEqual(matchesRequired(".git", ".git"), true);
  assert.strictEqual(matchesRequired("  .git  ", ".git"), true);
  assert.strictEqual(matchesRequired(".giT", ".git"), false);
  assert.strictEqual(matchesRequired(".git ext", ".git"), false);
  assert.strictEqual(matchesRequired("", ".git"), false);
});

// An absent requirement must never act as a skeleton key.
test("an absent requirement never auto-matches", () => {
  assert.strictEqual(matchesRequired("", ""), false);
  assert.strictEqual(matchesRequired("anything", ""), false);
  assert.strictEqual(matchesRequired("x", null), false);
  assert.strictEqual(matchesRequired(null, "x"), false);
});

// null means "cancelled" and is distinct from "": promptMkdir has to tell a
// dismissed dialog from a submitted-but-blank one.
test("a cancelled prompt yields null, a confirmed one yields trimmed text", () => {
  assert.strictEqual(promptValue("", "ignored"), null);
  assert.strictEqual(promptValue("cancel", "ignored"), null);
  assert.strictEqual(promptValue("confirm", "  notes.txt  "), "notes.txt");
  assert.strictEqual(promptValue("confirm", ""), "");
  assert.strictEqual(promptValue("confirm", undefined), "");
});

// A second askDialog() while one is open used to reconfigure the dialog in
// place: it overwrote the title, cleared a mustMatch gate, and left BOTH close
// listeners attached, so one click settled two promises. showModal() does not
// throw on an already-open dialog, so nothing surfaced the collision. Reachable
// with the keyboard: the confirm button is focused, so the palette hotkey still
// opens, and running "Kill session" from there re-labelled the live dialog.
test("a second ask is refused while one is already open", () => {
  assert.strictEqual(askIsBusy(true), true);
  assert.strictEqual(askIsBusy(false), false);
});

// focusTerminal runs on every dialog close. With a nested dialog still open it
// would pull the caret into the terminal underneath, and keystrokes would stop
// reaching the pane with nothing on screen to explain it.
test("focus returns to the terminal only when no dialog is left open", () => {
  assert.strictEqual(shouldRefocusTerminal(0), true);
  assert.strictEqual(shouldRefocusTerminal(1), false);
  assert.strictEqual(shouldRefocusTerminal(3), false);
});
