"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { isBrokenMouseReport } = require("../static/terminal-tab.js");

test("an SGR mouse report with NaN coordinates is dropped", () => {
  assert.strictEqual(isBrokenMouseReport("\x1b[<0;NaN;NaNm"), true);
  assert.strictEqual(isBrokenMouseReport("\x1b[<0;NaN;NaNM"), true);
  assert.strictEqual(isBrokenMouseReport("\x1b[<32;NaN;5M"), true);
});

test("a well-formed mouse report is left alone", () => {
  assert.strictEqual(isBrokenMouseReport("\x1b[<0;11;2M"), false);
  assert.strictEqual(isBrokenMouseReport("\x1b[<0;11;2m"), false);
  assert.strictEqual(isBrokenMouseReport("\x1b[<64;138;40M"), false);
});

// Typing N, a, N reaches onData as three one-character chunks, so real input
// cannot be mistaken for a broken report — but pin it anyway.
test("typed text is never treated as a mouse report", () => {
  for (const s of ["N", "a", "NaN", "NaN;NaNm", "echo NaN", ""]) {
    assert.strictEqual(isBrokenMouseReport(s), false, s);
  }
});

test("another escape sequence carrying NaN is not touched", () => {
  assert.strictEqual(isBrokenMouseReport("\x1b[NaN;NaNH"), false); // not a CSI < report
});

test("non-strings never match", () => {
  assert.strictEqual(isBrokenMouseReport(null), false);
  assert.strictEqual(isBrokenMouseReport(undefined), false);
});
