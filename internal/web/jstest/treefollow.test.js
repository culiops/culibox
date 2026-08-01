"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { treeFollowTarget } = require("../static/filetree.js");

// treeFollowTarget answers one question for the focused tab: which directory
// should the tree be rooted at, and under what key does the follow-guard record
// that it is already there. Returning null means "hold the current root".
//
// The key exists because a manual root pick has to survive until you focus a
// DIFFERENT tab. Terminals key on their session id; viewers have no session, so
// they key on the directory they resolve to.

const term = (id, cwd) => ({ kind: "terminal", id, cwd });
const viewer = (path) => ({ kind: "viewer", path });

test("a terminal follows its cwd, keyed on the session", () => {
  assert.deepStrictEqual(treeFollowTarget(term("s1", "/home/claude/culipulse")), {
    root: "/home/claude/culipulse",
    key: "sess:s1",
  });
});

test("a terminal whose cwd has not resolved yet is not followed", () => {
  // TerminalTab starts with cwd "" and fills it in from an async fetch. Rooting
  // at "" would send /api/fs/list a request it must reject.
  assert.strictEqual(treeFollowTarget(term("s1", "")), null);
});

test("a viewer follows the directory holding the file", () => {
  assert.deepStrictEqual(treeFollowTarget(viewer("/home/claude/culipulse/README.md")), {
    root: "/home/claude/culipulse",
    key: "view:/home/claude/culipulse",
  });
});

test("two files in one directory are the same follow target", () => {
  // Keyed on the directory, not the path: paging through a folder must not
  // re-root on every file, and must not discard a manual pick each time.
  const a = treeFollowTarget(viewer("/home/claude/x/a.md"));
  const b = treeFollowTarget(viewer("/home/claude/x/b.md"));
  assert.deepStrictEqual(a, b);
});

test("a viewer on a RELATIVE path is not followed", () => {
  // Three producers can hand over a relative path: the paste input (whose
  // "absolute" is a placeholder, not a check), TerminalTab.resolvePath while
  // its cwd is still empty, and a restored snapshot that only tested for a
  // truthy string. Rooting there 400s and paints an error over a tree that was
  // fine, so the guard is on this function, not on its callers.
  assert.strictEqual(treeFollowTarget(viewer("README.md")), null);
  assert.strictEqual(treeFollowTarget(viewer("./notes/a.md")), null);
});

test("a file at the filesystem root keeps a usable root", () => {
  // dirname("/a.md") must be "/", never "".
  assert.deepStrictEqual(treeFollowTarget(viewer("/a.md")), { root: "/", key: "view:/" });
});

test("no tab, an unknown kind, or a viewer with no path is not followed", () => {
  assert.strictEqual(treeFollowTarget(null), null);
  assert.strictEqual(treeFollowTarget(undefined), null);
  assert.strictEqual(treeFollowTarget({ kind: "terminal" }), null);
  assert.strictEqual(treeFollowTarget({ kind: "viewer" }), null);
  assert.strictEqual(treeFollowTarget({ kind: "something-new", path: "/x" }), null);
});
