// internal/web/jstest/drawerfiles.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const d = require("../static/drawerfiles.js");

const NONE = { copy: false, insert: false, edit: false, openInPane: false, overflow: false, cancel: false, save: false };

test("drawerActions: with no file everything is disabled (only the close button remains)", () => {
  assert.deepEqual(d.drawerActions({ path: null, editing: false, box: false, dirty: false }), NONE);
  assert.deepEqual(d.drawerActions({}), NONE);
  assert.deepEqual(d.drawerActions(null), NONE);
});

test("drawerActions: a host file in the read view enables every action", () => {
  assert.deepEqual(d.drawerActions({ path: "/h/u/a.go", editing: false, box: false, dirty: false }),
    { copy: true, insert: true, edit: true, openInPane: true, overflow: true, cancel: false, save: false });
});

test("drawerActions: a box file cannot be edited, everything else stays", () => {
  assert.deepEqual(d.drawerActions({ path: "/tmp/playwright-mcp/a.txt", editing: false, box: true, dirty: false }),
    { copy: true, insert: true, edit: false, openInPane: true, overflow: true, cancel: false, save: false });
});

test("drawerActions: an image has nothing to copy or edit", () => {
  const a = d.drawerActions({ path: "/h/u/shot.png", editing: false, box: false, dirty: false, image: true });
  assert.equal(a.copy, false);
  assert.equal(a.edit, false);
  assert.equal(a.insert, true);
  assert.equal(a.openInPane, true);
  assert.equal(a.overflow, true);
});

test("drawerActions: while editing the header shows only Cancel and Save, dirty or not", () => {
  for (const dirty of [false, true]) {
    assert.deepEqual(d.drawerActions({ path: "/h/u/a.go", editing: true, box: false, dirty }),
      { copy: false, insert: false, edit: false, openInPane: false, overflow: false, cancel: true, save: true });
  }
});

test("rememberDrawerFile / drawerFileFor: per-session memory survives a session switch", () => {
  let m = new Map();
  m = d.rememberDrawerFile(m, "A", { path: "/h/u/a.go", scroll: 120 });
  m = d.rememberDrawerFile(m, "B", { path: "/h/u/b.md", scroll: 40 });
  assert.deepEqual(d.drawerFileFor(m, "A"), { path: "/h/u/a.go", scroll: 120 });
  assert.deepEqual(d.drawerFileFor(m, "B"), { path: "/h/u/b.md", scroll: 40 });
  // Switching file inside A replaces A's entry only.
  m = d.rememberDrawerFile(m, "A", { path: "/h/u/c.txt", scroll: 0 });
  assert.deepEqual(d.drawerFileFor(m, "A"), { path: "/h/u/c.txt", scroll: 0 });
  assert.deepEqual(d.drawerFileFor(m, "B"), { path: "/h/u/b.md", scroll: 40 });
  assert.equal(d.drawerFileFor(m, "C"), null);
});

test("rememberDrawerFile is pure: the map passed in is not changed", () => {
  const m0 = new Map();
  const m1 = d.rememberDrawerFile(m0, "A", { path: "/x", scroll: 3 });
  assert.equal(m0.size, 0);
  assert.equal(m1.size, 1);
});

test("rememberDrawerFile: no path forgets the session; a bad scroll reads as 0", () => {
  let m = d.rememberDrawerFile(new Map(), "A", { path: "/x", scroll: 9 });
  m = d.rememberDrawerFile(m, "A", { path: null, scroll: 0 });
  assert.equal(d.drawerFileFor(m, "A"), null);
  m = d.rememberDrawerFile(m, "A", { path: "/y", scroll: -5 });
  assert.deepEqual(d.drawerFileFor(m, "A"), { path: "/y", scroll: 0 });
  m = d.rememberDrawerFile(m, "A", { path: "/y", scroll: NaN });
  assert.deepEqual(d.drawerFileFor(m, "A"), { path: "/y", scroll: 0 });
  m = d.rememberDrawerFile(m, "A", { path: "/y", scroll: 33.6 });
  assert.deepEqual(d.drawerFileFor(m, "A"), { path: "/y", scroll: 34 });
});

test("rememberDrawerFile: a pane of files (no session) has its own slot", () => {
  let m = d.rememberDrawerFile(new Map(), null, { path: "/n", scroll: 5 });
  m = d.rememberDrawerFile(m, "A", { path: "/a", scroll: 1 });
  assert.deepEqual(d.drawerFileFor(m, null), { path: "/n", scroll: 5 });
  assert.deepEqual(d.drawerFileFor(m, "A"), { path: "/a", scroll: 1 });
  assert.equal(d.drawerFileFor(null, "A"), null);
});

test("drawerLeavesFile: closing, switching kind or switching session leaves the drawer's file", () => {
  const files = (sid) => ({ open: "files", sessionId: sid });
  assert.equal(d.drawerLeavesFile(files("A"), { open: null, sessionId: "A" }), true, "close");
  assert.equal(d.drawerLeavesFile(files("A"), { open: "access", sessionId: "A" }), true, "kind");
  assert.equal(d.drawerLeavesFile(files("A"), files("B")), true, "session");
  assert.equal(d.drawerLeavesFile(files("A"), files("A")), false, "same");
  assert.equal(d.drawerLeavesFile({ open: "access", sessionId: "A" }, files("B")), false, "files was not open");
  assert.equal(d.drawerLeavesFile({ open: null, sessionId: null }, files("A")), false);
});

test("drawerTreeBlocked: a session whose known workspace cannot be browsed blocks the tree", () => {
  assert.equal(d.drawerTreeBlocked("A", "/tmp/ab-x/ws", false), true, "refused cwd");
  assert.equal(d.drawerTreeBlocked("A", "/h/u/ws", true), false, "rootable cwd");
  assert.equal(d.drawerTreeBlocked("A", "", false), false, "cwd not known yet: hold, do not block");
  assert.equal(d.drawerTreeBlocked(null, "/tmp/x", false), false, "a pane of files has no session workspace");
});

test("discardAsk: the prompt names the file, exactly", () => {
  const o = d.discardAsk("app.go");
  assert.equal(o.title, "Discard unsaved changes to app.go?");
  assert.equal(o.danger, true);
  assert.equal(typeof o.confirmLabel, "string");
});

test("goToFileURL: the bounded file search, preferring the tree's root", () => {
  assert.strictEqual(d.goToFileURL("main", "/home/u/a"),
    "/api/fs/search?limit=40&q=main&prefer=%2Fhome%2Fu%2Fa");
});

test("goToFileURL: the query and the root are encoded", () => {
  assert.strictEqual(d.goToFileURL("a b&c/d", "/home/u/my proj"),
    "/api/fs/search?limit=40&q=a%20b%26c%2Fd&prefer=%2Fhome%2Fu%2Fmy%20proj");
});

test("goToFileURL: with no root there is nothing to prefer", () => {
  assert.strictEqual(d.goToFileURL("x", ""), "/api/fs/search?limit=40&q=x");
  assert.strictEqual(d.goToFileURL("x", null), "/api/fs/search?limit=40&q=x");
});

test("hitsUnderRoot keeps only paths under the root", () => {
  const hits = [
    { path: "/home/u/a/main.go", rel: "a/main.go" },
    { path: "/home/u/a/sub/x.go", rel: "a/sub/x.go" },
    { path: "/home/u/b/main.go", rel: "b/main.go" },
  ];
  assert.deepEqual(d.hitsUnderRoot(hits, "/home/u/a").map((h) => h.path),
    ["/home/u/a/main.go", "/home/u/a/sub/x.go"]);
});

test("hitsUnderRoot: root /home/u/a does NOT match /home/u/ab/x", () => {
  const hits = [{ path: "/home/u/ab/x" }, { path: "/home/u/a/x" }, { path: "/home/u/a" }];
  assert.deepEqual(d.hitsUnderRoot(hits, "/home/u/a").map((h) => h.path), ["/home/u/a/x"]);
});

test("hitsUnderRoot: a trailing slash on the root changes nothing", () => {
  const hits = [{ path: "/home/u/a/x" }, { path: "/home/u/ab/x" }];
  assert.deepEqual(d.hitsUnderRoot(hits, "/home/u/a/").map((h) => h.path), ["/home/u/a/x"]);
});

test("hitsUnderRoot: no root, no hits; bad input is empty, never a throw", () => {
  assert.deepEqual(d.hitsUnderRoot([{ path: "/x" }], ""), []);
  assert.deepEqual(d.hitsUnderRoot(null, "/home/u"), []);
  assert.deepEqual(d.hitsUnderRoot([null, {}, { path: 5 }, { path: "/home/u/a" }], "/home/u"), [{ path: "/home/u/a" }]);
});

test("hitsUnderRoot: root / holds every absolute path", () => {
  const hits = [{ path: "/home/u/a" }, { path: "/etc/x" }, { path: "/" }];
  assert.deepEqual(d.hitsUnderRoot(hits, "/").map((h) => h.path), ["/home/u/a", "/etc/x"]);
});

const sorted = (s) => [...s].sort();

test("changedSet: a changed file and each folder above it under the root, never the root", () => {
  assert.deepEqual(sorted(d.changedSet([{ path: "/w/a/b.go" }], "/w")), ["/w/a", "/w/a/b.go"]);
  assert.deepEqual(sorted(d.changedSet([{ path: "/w/a/b/c.go" }], "/w/")), ["/w/a", "/w/a/b", "/w/a/b/c.go"]);
  assert.deepEqual(sorted(d.changedSet([{ path: "/w/top.go" }, { path: "/w/a/x" }, { path: "/w/a/y" }], "/w")), ["/w/a", "/w/a/x", "/w/a/y", "/w/top.go"]);
});

test("changedSet: outside the root, the root itself and a sibling that shares its prefix are ignored", () => {
  assert.equal(d.changedSet([{ path: "/w2/x" }, { path: "/etc/x" }, { path: "/w" }, { path: "/w/" }, { path: "/" }], "/w").size, 0);
});

test("changedSet: paths are normalized before they are compared", () => {
  assert.deepEqual(sorted(d.changedSet([{ path: "/w//a/./b/../c.go/" }], "/w")), ["/w/a", "/w/a/c.go"]);
  assert.equal(d.changedSet([{ path: "/w/../etc/x" }], "/w").size, 0);
});

test("changedSet: root / holds every absolute path; a relative path is never a match", () => {
  assert.deepEqual(sorted(d.changedSet([{ path: "/a/b" }], "/")), ["/a", "/a/b"]);
  assert.equal(d.changedSet([{ path: "a/b" }], "/w").size, 0);
});

test("changedSet: no root or bad input is an empty set, never a throw", () => {
  assert.equal(d.changedSet([{ path: "/w/a" }], "").size, 0);
  assert.equal(d.changedSet(null, "/w").size, 0);
  assert.equal(d.changedSet({}, "/w").size, 0);
  assert.deepEqual(sorted(d.changedSet([null, {}, { path: 5 }, "/w/z", { path: "/w/a" }], "/w")), ["/w/a"]);
});
