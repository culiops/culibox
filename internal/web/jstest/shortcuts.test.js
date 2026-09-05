// A global shortcut must not act on the app while a modal is on screen, and
// must not steal a keystroke aimed at a real text field. showModal() makes the
// page inert to POINTER input only: a keydown from a focused descendant of the
// dialog still reaches document in the capture phase.
const test = require("node:test");
const assert = require("node:assert");
const { shortcutsBlocked } = require("../static/shell.js");

const el = (tagName, className) => ({ tagName, classList: { contains: (c) => c === className } });

test("blocked while any dialog is open", () => {
  assert.strictEqual(shortcutsBlocked(el("BUTTON", ""), 1), true);
  assert.strictEqual(shortcutsBlocked(el("SELECT", ""), 2), true);
  assert.strictEqual(shortcutsBlocked(null, 1), true);
});

test("blocked on a real text field", () => {
  assert.strictEqual(shortcutsBlocked(el("INPUT", ""), 0), true);
  assert.strictEqual(shortcutsBlocked(el("TEXTAREA", ""), 0), true);
});

test("xterm's own textarea is not a real text field", () => {
  assert.strictEqual(shortcutsBlocked(el("TEXTAREA", "xterm-helper-textarea"), 0), false);
});

test("open otherwise", () => {
  assert.strictEqual(shortcutsBlocked(el("BUTTON", ""), 0), false);
  assert.strictEqual(shortcutsBlocked(el("BODY", ""), 0), false);
  assert.strictEqual(shortcutsBlocked(null, 0), false);
});

test("a negative or absent count is treated as none open", () => {
  assert.strictEqual(shortcutsBlocked(el("BUTTON", ""), undefined), false);
  assert.strictEqual(shortcutsBlocked(el("BUTTON", ""), 0), false);
});
