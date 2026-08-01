"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { classifyPickerQuery, pickerRows } = require("../static/dirpicker.js");

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
