"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { treeFollowTarget, rootableCwd, insideAnyRoot } = require("../static/filetree.js");

// Every case passes HOME explicitly. treeFollowTarget also reads window.HOME_DIR
// when the argument is omitted (the shortPath/relativeTo convention in
// fsops.js), but a test that relied on a global would be testing the browser.
const HOME = "/home/user";

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
  assert.deepStrictEqual(follow(term("s1", "/home/user/culipulse")), {
    root: "/home/user/culipulse",
    key: "sess:s1",
  });
});

test("a terminal whose cwd has not resolved yet is not followed", () => {
  // TerminalTab starts with cwd "" and fills it in from an async fetch. Rooting
  // at "" would send /api/fs/list a request it must reject.
  assert.strictEqual(follow(term("s1", "")), null);
});

test("a viewer follows the directory holding the file", () => {
  assert.deepStrictEqual(follow(viewer("/home/user/culipulse/README.md")), {
    root: "/home/user/culipulse",
    key: "view:/home/user/culipulse",
  });
});

test("two files in one directory are the same follow target", () => {
  // Keyed on the directory, not the path: paging through a folder must not
  // re-root on every file, and must not discard a manual pick each time.
  const a = follow(viewer("/home/user/x/a.md"));
  const b = follow(viewer("/home/user/x/b.md"));
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
  assert.strictEqual(follow(viewer("/home/user2/x.md")), null);
});

test("a terminal whose cwd is outside $HOME is not followed", () => {
  // Predates viewer support and fails the same way; the guard covers both.
  assert.strictEqual(follow(term("s1", "/opt/culiops/staging")), null);
});

// An agent that entered a git worktree (Claude Code's EnterWorktree moves it
// without a chdir) works in agentCwd, the worktree root the usage poll reports.
// The tree must show THAT checkout: rooted at the spawn cwd it listed the main
// checkout's copies, and a click opened those instead of the agent's (#49).
const wterm = (id, cwd, agentCwd) => ({ kind: "terminal", id, cwd, agentCwd });
const WT = "/home/user/repo/.claude/worktrees/t49";

test("a terminal whose agent is in a worktree follows the worktree", () => {
  assert.deepStrictEqual(follow(wterm("s1", "/home/user/repo", WT)), { root: WT, key: "sess:s1@" + WT });
});

test("entering and leaving a worktree change the key, so the tree re-roots both ways", () => {
  const before = follow(wterm("s1", "/home/user/repo", ""));
  const inside = follow(wterm("s1", "/home/user/repo", WT));
  const after = follow(wterm("s1", "/home/user/repo", ""));
  assert.notStrictEqual(before.key, inside.key);
  assert.deepStrictEqual(after, { root: "/home/user/repo", key: "sess:s1" });
});

test("the worktree is known before the spawn cwd has resolved", () => {
  assert.deepStrictEqual(follow(wterm("s1", "", WT)), { root: WT, key: "sess:s1@" + WT });
});

test("an agent cwd /api/fs/list would refuse falls back to the spawn cwd", () => {
  assert.deepStrictEqual(follow(wterm("s1", "/home/user/repo", "/srv/wt")), { root: "/home/user/repo", key: "sess:s1" });
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

test("rootableCwd: a session cwd is a root only inside $HOME (the drawer must not root where fs/list 403s)", () => {
  assert.strictEqual(rootableCwd("/home/user", HOME), true);
  assert.strictEqual(rootableCwd("/home/user/culipulse", HOME), true);
  assert.strictEqual(rootableCwd("/tmp/abvws", HOME), false);
  assert.strictEqual(rootableCwd("/home/username", HOME), false);
  assert.strictEqual(rootableCwd("", HOME), false);
  assert.strictEqual(rootableCwd(undefined, HOME), false);
});

// #343: the tree follows every workspace root, not only home. `roots` is an array (home first); a string is
// still one root, for callers that pass home.
const ROOTS = ["/home/u", "/srv/code"];

test("treeFollowTarget with a roots array accepts a cwd or a viewer file under any root and rejects the rest", () => {
  assert.deepStrictEqual(treeFollowTarget(term("s1", "/srv/code/api"), ROOTS), { root: "/srv/code/api", key: "sess:s1" });
  assert.deepStrictEqual(treeFollowTarget(term("s1", "/srv/code"), ROOTS), { root: "/srv/code", key: "sess:s1" });
  assert.deepStrictEqual(treeFollowTarget(viewer("/srv/code/api/main.go"), ROOTS), { root: "/srv/code/api", key: "view:/srv/code/api" });
  assert.deepStrictEqual(treeFollowTarget(term("s1", "/home/u/x"), ROOTS), { root: "/home/u/x", key: "sess:s1" });
  assert.strictEqual(treeFollowTarget(term("s1", "/opt/x"), ROOTS), null);
  assert.strictEqual(treeFollowTarget(viewer("/opt/x/a.md"), ROOTS), null);
  // Whole components: /srv/code2 is not under /srv/code.
  assert.strictEqual(treeFollowTarget(term("s1", "/srv/code2"), ROOTS), null);
  assert.strictEqual(treeFollowTarget(viewer("/srv/code2/a.md"), ROOTS), null);
  // The agent's worktree outside every root falls back to the spawn cwd, as before.
  assert.deepStrictEqual(treeFollowTarget(wterm("s1", "/srv/code/repo", "/opt/wt"), ROOTS), { root: "/srv/code/repo", key: "sess:s1" });
  assert.deepStrictEqual(treeFollowTarget(wterm("s1", "/srv/code/repo", "/srv/code/repo/.claude/worktrees/w"), ROOTS),
    { root: "/srv/code/repo/.claude/worktrees/w", key: "sess:s1@/srv/code/repo/.claude/worktrees/w" });
});

test("a string is one root, an empty array knows no root yet (allows), and a trailing slash does not matter", () => {
  assert.deepStrictEqual(treeFollowTarget(term("s1", "/home/u/x"), "/home/u"), { root: "/home/u/x", key: "sess:s1" });
  assert.strictEqual(treeFollowTarget(term("s1", "/srv/code"), "/home/u"), null);
  assert.deepStrictEqual(treeFollowTarget(viewer("/etc/a.conf"), []), { root: "/etc", key: "view:/etc" });
  assert.deepStrictEqual(treeFollowTarget(term("s1", "/srv/code/x"), ["/srv/code/"]), { root: "/srv/code/x", key: "sess:s1" });
  assert.strictEqual(insideAnyRoot("/srv/code/x", ["", "/home/u"]), false, "an empty entry is no root");
});

test("rootableCwd with a roots array", () => {
  assert.strictEqual(rootableCwd("/srv/code/api", ROOTS), true);
  assert.strictEqual(rootableCwd("/home/u", ROOTS), true);
  assert.strictEqual(rootableCwd("/opt/x", ROOTS), false);
  assert.strictEqual(rootableCwd("", ROOTS), false);
});

test("with no argument the page's window.WORKSPACE_ROOTS and HOME_DIR decide; home's own spelling still counts", () => {
  const had = Object.getOwnPropertyDescriptor(globalThis, "window");
  try {
    globalThis.window = { WORKSPACE_ROOTS: ["/data/u", "/srv/code"], HOME_DIR: "/home/u" };
    assert.strictEqual(rootableCwd("/srv/code/x"), true);
    assert.strictEqual(rootableCwd("/data/u/x"), true);
    assert.strictEqual(rootableCwd("/home/u/x"), true, "HOME_DIR is the spelling session paths use when home is a symlink");
    assert.strictEqual(rootableCwd("/opt/x"), false);
    globalThis.window = { HOME_DIR: "/home/u" }; // roots not loaded yet: home alone, as before
    assert.strictEqual(rootableCwd("/srv/code/x"), false);
    assert.strictEqual(rootableCwd("/home/u/x"), true);
    globalThis.window = {}; // nothing known: allowed
    assert.strictEqual(rootableCwd("/anything"), true);
  } finally {
    if (had) Object.defineProperty(globalThis, "window", had); else delete globalThis.window;
  }
});
