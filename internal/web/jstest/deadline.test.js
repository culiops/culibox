"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { deadlineSignal } = require("../static/deadline.js");

test("deadlineSignal uses AbortSignal.timeout when the browser has it", () => {
  const seen = [];
  const sentinel = { sentinel: true };
  const g = { AbortSignal: { timeout: (ms) => { seen.push(ms); return sentinel; } } };
  assert.strictEqual(deadlineSignal(5000, g), sentinel);
  assert.deepStrictEqual(seen, [5000]);
});

test("deadlineSignal falls back to AbortController + timer without AbortSignal.timeout", async () => {
  const g = { AbortSignal: {}, AbortController, setTimeout };
  const sig = deadlineSignal(20, g);
  assert.strictEqual(sig.aborted, false, "must not abort before the deadline");
  await new Promise((r) => setTimeout(r, 80));
  assert.strictEqual(sig.aborted, true, "must abort once the deadline passes");
});
