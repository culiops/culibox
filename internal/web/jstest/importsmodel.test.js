"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { importsRowState, importsOffToSend } = require("../static/importsmodel.js");

const f = (name, box, warnings) => Object.assign({ import: "~/.claude/context/" + name, name, box: box || "/home/agent/.claude/context/" + name, host: "/h/" + name, size: 10, sha256: "x" }, warnings ? { warnings } : {});
const four = { files: ["projects.md", "infra.md", "skills.md", "data.md"].map((n) => f(n)) };
const base = (o) => Object.assign({ tier: "box", account: "", accounts: [], set: four, failed: false, off: false }, o || {});

test("host tier hides the row", () => {
  assert.equal(importsRowState(base({ tier: "host" })).hidden, true);
  assert.equal(importsRowState(base()).hidden, false);
});

test("box tier, default account, four files", () => {
  const s = importsRowState(base());
  assert.equal(s.checked, true);
  assert.equal(s.disabled, false);
  assert.equal(s.list, "Your CLAUDE.md imports: projects.md, infra.md, skills.md, data.md (read-only in the box)");
  assert.equal(s.warn, "");
  assert.equal(s.refused, "");
});

test("off unchecks and says what is withheld", () => {
  const s = importsRowState(base({ off: true }));
  assert.equal(s.checked, false);
  assert.equal(s.disabled, false);
  assert.equal(s.list, "Not given to the box: projects.md, infra.md, skills.md, data.md");
});

test("same-named files show their ~ box paths", () => {
  const set = { files: [f("notes.md", "/home/agent/a/notes.md"), f("notes.md", "/home/agent/b/notes.md")] };
  assert.equal(importsRowState(base({ set })).list, "Your CLAUDE.md imports: ~/a/notes.md, ~/b/notes.md (read-only in the box)");
});

test("warnings join with newlines", () => {
  const set = { files: [f("a.md", null, ["w1", "w2"]), f("b.md", null, ["w3"]), f("c.md")] };
  assert.equal(importsRowState(base({ set })).warn, "w1\nw2\nw3");
});

test("refusals are one line each", () => {
  const set = { files: [], refused: [
    { import: "~/.claude/context/x.md", reason: "is a symlink that leads out of its folder; only files stay" },
    { import: "~/y.md", reason: "is larger than 256 KiB" },
  ] };
  assert.equal(importsRowState(base({ set })).refused,
    "Left out: ~/.claude/context/x.md (is a symlink that leads out of its folder; only files stay)\nLeft out: ~/y.md (is larger than 256 KiB)");
});

test("an account that does not use personal config disables the row", () => {
  const accounts = [{ name: "work", inherit_personal: false }, { name: "pers", inherit_personal: true }];
  const s = importsRowState(base({ account: "work", accounts }));
  assert.equal(s.disabled, true);
  assert.equal(s.list, "This account does not use your personal config, so its boxes get no CLAUDE.md imports.");
  assert.equal(importsRowState(base({ account: "pers", accounts })).disabled, false);
  assert.equal(importsRowState(base({ account: "default", accounts })).disabled, false);
  assert.equal(importsRowState(base({ account: "ghost", accounts })).disabled, false);
});

test("a failed read says so", () => {
  const s = importsRowState(base({ set: null, failed: true }));
  assert.equal(s.list, "Could not read your CLAUDE.md imports.");
  assert.equal(s.hidden, false);
});

test("no answer yet shows an empty list line, not an error", () => {
  const s = importsRowState(base({ set: null, failed: false }));
  assert.equal(s.list, "");
});

test("an empty set", () => {
  assert.equal(importsRowState(base({ set: { files: [] } })).list, "Your CLAUDE.md imports no files.");
});

test("importsOffToSend", () => {
  assert.equal(importsOffToSend(true, false, false), true);
  assert.equal(importsOffToSend(true, true, true), false);
  assert.equal(importsOffToSend(false, true, true), true);
  assert.equal(importsOffToSend(false, false, false), false);
});
