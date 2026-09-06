"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { sheetActions, sheetMeta, SHEET_GLYPHS } = require("../static/sheet.js");

const claude = { id: "a", bin: "claude", cwd: "/home/claude/x", model: "claude-opus-5", effort: "high", startedAt: 1 };
const shell = { id: "b", bin: "sh" };

test("open-terminal is first, end is last and danger", () => {
  const a = sheetActions(claude);
  assert.strictEqual(a[0].id, "open-terminal");
  assert.strictEqual(a[a.length - 1].id, "end");
  assert.strictEqual(a[a.length - 1].danger, true);
});

test("newpane is never offered on mobile", () => {
  assert.ok(!sheetActions(claude).some((x) => x.id === "newpane"));
});

test("every action carries a glyph", () => {
  for (const x of sheetActions(claude)) assert.ok(x.glyph, "missing glyph for " + x.id);
});

test("row order matches the M5 canvas: terminal, files, context, rename, copy, end", () => {
  const full = { ...claude, claude: true };
  const ids = sheetActions(full).map((x) => x.id);
  assert.deepStrictEqual(ids, ["open-terminal", "files", "attribution", "rename", "copycwd", "end"]);
});

test("a non-claude shell session drops the claude-only items but keeps end", () => {
  const ids = sheetActions(shell).map((x) => x.id);
  assert.ok(ids.includes("open-terminal"));
  assert.ok(ids.includes("end"));
  assert.ok(!ids.includes("attribution")); // claude-only, per menuItems
});

test("meta parts omit missing fields", () => {
  const m = sheetMeta(shell);
  assert.strictEqual(m.model, "");
  assert.strictEqual(typeof m.cwd, "string");
});
