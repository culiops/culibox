"use strict";
const test = require("node:test");
const assert = require("node:assert");
const m = require("../static/mobileterm.js");

test("keyByte maps the fixed keys to their wire bytes; unknown -> empty", () => {
  assert.equal(m.keyByte("esc"), "\x1b");
  assert.equal(m.keyByte("tab"), "\t");
  assert.equal(m.keyByte("up"), "\x1b[A");
  assert.equal(m.keyByte("down"), "\x1b[B");
  assert.equal(m.keyByte("right"), "\x1b[C");
  assert.equal(m.keyByte("left"), "\x1b[D");
  assert.equal(m.keyByte("enter"), "\r");
  assert.equal(m.keyByte("ctrl"), "");   // ctrl is a latch, not a byte
  assert.equal(m.keyByte("bogus"), "");
});

test("ctrlByte turns a letter into its control byte, case-insensitively", () => {
  assert.equal(m.ctrlByte("c"), "\x03"); // Ctrl-C / SIGINT
  assert.equal(m.ctrlByte("C"), "\x03");
  assert.equal(m.ctrlByte("a"), "\x01");
  assert.equal(m.ctrlByte("z"), "\x1a");
});

test("ctrlByte maps the classic non-letter control chars", () => {
  assert.equal(m.ctrlByte("["), "\x1b"); // Ctrl-[ == ESC
  assert.equal(m.ctrlByte("\\"), "\x1c");
  assert.equal(m.ctrlByte("]"), "\x1d");
  assert.equal(m.ctrlByte("_"), "\x1f");
});

test("ctrlByte returns empty for a char with no control meaning (send it literally instead)", () => {
  assert.equal(m.ctrlByte("1"), "");
  assert.equal(m.ctrlByte("é"), "");
  assert.equal(m.ctrlByte(""), "");
  assert.equal(m.ctrlByte("ab"), ""); // must be exactly one char
});

test("SNIPPETS: ^C is first, control kind, red, sends SIGINT; the rest are text/more", () => {
  assert.equal(m.SNIPPETS[0].key, "sigint");
  assert.equal(m.SNIPPETS[0].label, "^C");
  assert.equal(m.SNIPPETS[0].kind, "control");
  assert.equal(m.SNIPPETS[0].bytes, "\x03");
  assert.equal(m.SNIPPETS[0].danger, true);
  const byKey = Object.fromEntries(m.SNIPPETS.map((s) => [s.key, s]));
  assert.equal(byKey.continue.kind, "text");
  assert.equal(byKey.continue.text, "continue");
  assert.equal(byKey.more.kind, "more");
});

test("KEYBAR is exactly seven keys in the fixed order and never carries a bare ^C", () => {
  assert.deepEqual(m.KEYBAR, ["esc", "tab", "ctrl", "up", "down", "enter", "more"]);
  assert.equal(m.KEYBAR.length, 7);
  assert.equal(m.KEYBAR.includes("sigint"), false);
});

test("gestureAxis stays null until slop is crossed, then picks the dominant axis", () => {
  assert.equal(m.gestureAxis(3, 3, 8), null);       // inside slop
  assert.equal(m.gestureAxis(20, 4, 8), "horizontal");
  assert.equal(m.gestureAxis(4, 20, 8), "vertical");
  assert.equal(m.gestureAxis(-20, 4, 8), "horizontal"); // sign-independent
});

test("swipeIntent: drag left => next, drag right => prev, small => null", () => {
  assert.equal(m.swipeIntent(-60, 50), "next");
  assert.equal(m.swipeIntent(60, 50), "prev");
  assert.equal(m.swipeIntent(-10, 50), null);
});

test("keyboardOpen is true only when the viewport shrank past minDelta", () => {
  assert.equal(m.keyboardOpen(844, 544, 120), true);  // 300px shorter -> keyboard
  assert.equal(m.keyboardOpen(844, 800, 120), false); // 44px -> browser chrome, not a keyboard
  assert.equal(m.keyboardOpen(844, 844, 120), false);
});
