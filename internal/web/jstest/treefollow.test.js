"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { treeFollowTarget } = require("../static/filetree.js");

// Every case passes HOME explicitly. treeFollowTarget also reads window.HOME_DIR
// when the argument is omitted (the shortPath/relativeTo convention in
// fsops.js), but a test that relied on a global would be testing the browser.
const HOME = "/home/claude";

// treeFollowTarget answers one question for the focused tab: which directory
// should the tree be rooted at, and under what key does the follow-guard record
// that it is already there. Returning null means "hold the current root".
//
// The key exists because a manual root pick has to survive until you focus a
// DIFFERENT tab. Terminals key on their session id; viewers have no session, so
// they key on the directory they resolve to.

const term = (id, cwd) => ({ kind: "terminal", id, cwd });
const viewer = (path) => ({ kind: "viewer", path });
const follow = (tab) => treeFollowTarget(tab, HOME);

test("a terminal follows its cwd, keyed on the session", () => {
  assert.deepStrictEqual(follow(term("s1", "/home/claude/culipulse")), {
    root: "/home/claude/culipulse",
    key: "sess:s1",
  });
});

test("a terminal whose cwd has not resolved yet is not followed", () => {
  // TerminalTab starts with cwd "" and fills it in from an async fetch. Rooting
  // at "" would send /api/fs/list a request it must reject.
  assert.strictEqual(follow(term("s1", "")), null);
});

test("a viewer follows the directory holding the file", () => {
  assert.deepStrictEqual(follow(viewer("/home/claude/culipulse/README.md")), {
    root: "/home/claude/culipulse",
    key: "view:/home/claude/culipulse",
  });
});

test("two files in one directory are the same follow target", () => {
  // Keyed on the directory, not the path: paging through a folder must not
  // re-root on every file, and must not discard a manual pick each time.
  const a = follow(viewer("/home/claude/x/a.md"));
  const b = follow(viewer("/home/claude/x/b.md"));
  assert.deepStrictEqual(a, b);
});

test("a viewer on a RELATIVE path is not followed", () => {
  // Three producers can hand over a relative path: the paste input (whose
  // "absolute" is a placeholder, not a check), TerminalTab.resolvePath while
  // its cwd is still empty, and a restored snapshot that only tested for a
  // truthy string. Rooting there 400s and paints an error over a tree that was
  // fine, so the guard is on this function, not on its callers.
  assert.strictEqual(follow(viewer("README.md")), null);
  assert.strictEqual(follow(viewer("./notes/a.md")), null);
});

test("a viewer outside $HOME is not followed", () => {
  // /api/fs/list is confined to $HOME and answers 403 outside it, so rooting
  // there does not degrade — ftLoadRoot replaces the whole tree with one
  // "forbidden" line AND the follow-key is already recorded, so re-focusing the
  // tab will not retry. Reachable by clicking an absolute path in any terminal:
  // LINK_PATH_RE matches /etc/nginx/nginx.conf.
  assert.strictEqual(follow(viewer("/etc/nginx/nginx.conf")), null);
  assert.strictEqual(follow(viewer("/opt/culiops/staging/docker-compose.yml")), null);
  // "/" is the one root guaranteed to 403, so dirname("/a.md") === "/" is not a
  // usable target either.
  assert.strictEqual(follow(viewer("/a.md")), null);
  // A sibling of home is not inside it: whole-component compare, not prefix.
  assert.strictEqual(follow(viewer("/home/claude2/x.md")), null);
});

test("a terminal whose cwd is outside $HOME is not followed", () => {
  // Predates viewer support and fails the same way; the guard covers both.
  assert.strictEqual(follow(term("s1", "/opt/culiops/staging")), null);
});

test("home itself is a legitimate root", () => {
  assert.deepStrictEqual(follow(viewer(HOME + "/notes.md")), { root: HOME, key: "view:" + HOME });
  assert.deepStrictEqual(follow(term("s1", HOME)), { root: HOME, key: "sess:s1" });
});

test("with no home known the containment check cannot run, so it allows", () => {
  // Pre-/api/workspaces. initFileTree re-asserts the root once the list lands.
  assert.deepStrictEqual(treeFollowTarget(viewer("/etc/nginx/nginx.conf"), ""),
    { root: "/etc/nginx", key: "view:/etc/nginx" });
});

test("no tab, an unknown kind, or a viewer with no path is not followed", () => {
  assert.strictEqual(follow(null), null);
  assert.strictEqual(follow(undefined), null);
  assert.strictEqual(follow({ kind: "terminal" }), null);
  assert.strictEqual(follow({ kind: "viewer" }), null);
  assert.strictEqual(follow({ kind: "something-new", path: "/x" }), null);
});

test("a box viewer is never followed: its path does not exist on the host", () => {
  // /tmp/playwright-mcp/… is a box path. Rooting the tree there would 403, or
  // show an unrelated host directory that happens to share the name.
  const boxed = { kind: "viewer", path: "/tmp/playwright-mcp/a.png", box: "s1" };
  assert.strictEqual(treeFollowTarget(boxed, "/h"), null);
  assert.strictEqual(treeFollowTarget(boxed, ""), null);
});
