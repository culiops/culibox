"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { fmtCtx, attributionRows } = require("../static/attribution.js");

test("fmtCtx converts bytes→approx tokens (÷4)", () => {
  assert.equal(fmtCtx(0), "");
  assert.equal(fmtCtx(400), "≈100"); // 400/4
  assert.equal(fmtCtx(1614490), "≈404k"); // /4 = 403622 → 404k
  assert.equal(fmtCtx(8000000), "≈2.0M"); // /4 = 2,000,000
});

test("attributionRows maps tools; null-safe", () => {
  assert.deepEqual(attributionRows(null), []);
  assert.deepEqual(attributionRows({ tools: [] }), []);
  const rows = attributionRows({
    tools: [
      { name: "Read", kind: "tool", calls: 33, ctxBytes: 1614490 },
      { name: "playwright", kind: "mcp", calls: 28, ctxBytes: 12884 },
    ],
  });
  assert.equal(rows.length, 2);
  assert.deepEqual(rows[0], { name: "Read", kind: "tool", calls: 33, ctx: "≈404k" });
  assert.equal(rows[1].kind, "mcp");
});
