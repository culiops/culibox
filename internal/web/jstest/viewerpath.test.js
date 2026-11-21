"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  ancestorsBetween, pathLineText, relativePath, relativeAvail, insertTargetFor, revealable, alternatesOutcome, altNote, agoText,
} = require("../static/viewerpath.js");
const { shellQuote } = require("../static/upload.js");

test("ancestorsBetween: dirs strictly between root and file, outermost first", () => {
  assert.deepEqual(ancestorsBetween("/h/p", "/h/p/a/b/c.go"), ["/h/p/a", "/h/p/a/b"]);
  assert.deepEqual(ancestorsBetween("/h/p", "/h/p/c.go"), []);
});

test("ancestorsBetween: outside the root is null, including the prefix trap", () => {
  assert.equal(ancestorsBetween("/h/p", "/h/q/c.go"), null);
  assert.equal(ancestorsBetween("/h/p", "/h/pp/c.go"), null);
  assert.equal(ancestorsBetween("/h/p", "/h/p"), null);
  assert.equal(ancestorsBetween("", "/h/p/c.go"), null);
});

test("ancestorsBetween: root / and a trailing slash on the root", () => {
  assert.deepEqual(ancestorsBetween("/", "/a/b.go"), ["/a"]);
  assert.deepEqual(ancestorsBetween("/h/p/", "/h/p/a/c.go"), ["/h/p/a"]);
});

test("pathLineText: path alone, or path:N", () => {
  assert.deepEqual(pathLineText("/h/a.go"), { ok: true, text: "/h/a.go" });
  assert.deepEqual(pathLineText("/h/a.go", 3), { ok: true, text: "/h/a.go:3" });
});

test("pathLineText refuses control characters", () => {
  assert.deepEqual(pathLineText("/h/a\nb.go", 3), { ok: false, reason: "unsafe" });
  assert.deepEqual(pathLineText("/h/a\x7fb.go"), { ok: false, reason: "unsafe" });
});

test("pathLineText then shellQuote: spaces get quoted, plain paths do not", () => {
  // shellQuote keeps ":" bare (it is in its safe set), so only the space forces quotes.
  assert.equal(shellQuote(pathLineText("/h/my dir/a.go", 3).text), "'/h/my dir/a.go:3'");
  assert.equal(shellQuote(pathLineText("/h/a/b.go", 3).text), "/h/a/b.go:3");
});

test("relativePath: under the root, else null", () => {
  assert.equal(relativePath("/h/p", "/h/p/a/c.go"), "a/c.go");
  assert.equal(relativePath("/h/p", "/h/pp/c.go"), null);
  assert.equal(relativePath("/h/p", "/h/p"), null);
  assert.equal(relativePath("/", "/a/c.go"), "a/c.go");
  assert.equal(relativePath("", "/a/c.go"), null);
});

test("relativeAvail: host file under the root is ok; box and outside-root say why", () => {
  assert.deepEqual(relativeAvail({ box: "", path: "/h/p/c.go" }, "/h/p"), { ok: true, text: "c.go" });
  const box = relativeAvail({ box: "s1", path: "/tmp/x.png" }, "/h/p");
  assert.equal(box.ok, false);
  assert.match(box.title, /box/);
  const out = relativeAvail({ box: "", path: "/h/q/c.go" }, "/h/p");
  assert.equal(out.ok, false);
  assert.match(out.title, /outside/);
});

test("insertTargetFor: a box viewer accepts only its own session", () => {
  const t = { id: "s2" };
  assert.deepEqual(insertTargetFor({ box: "s2" }, t), { ok: true, term: t });
  assert.deepEqual(insertTargetFor({ box: "s2" }, { id: "s1" }),
    { ok: false, title: "This file is inside another session's box" });
});

test("insertTargetFor: a host viewer accepts any candidate", () => {
  const t = { id: "s1" };
  assert.deepEqual(insertTargetFor({ box: "" }, t), { ok: true, term: t });
});

test("insertTargetFor: no candidate", () => {
  const want = { ok: false, title: "Open this file from a session's pane to insert at its prompt" };
  assert.deepEqual(insertTargetFor({ box: "" }, null), want);
  assert.deepEqual(insertTargetFor({ box: "s1" }, null), want);
});

test("revealable: host file under home is ok", () => {
  assert.deepEqual(revealable({ box: "", path: "/home/u/p/a.go" }, "/home/u"), { ok: true });
});

test("revealable: box file and outside-home file are refused with a reason", () => {
  const b = revealable({ box: "s1", path: "/home/u/a.go" }, "/home/u");
  assert.equal(b.ok, false);
  assert.match(b.title, /box/);
  const o = revealable({ box: "", path: "/etc/nginx/nginx.conf" }, "/home/u");
  assert.equal(o.ok, false);
  assert.match(o.title, /workspace roots/);
  assert.equal(revealable({ box: "", path: "/home/user2/a.go" }, "/home/user").ok, false);
});

test("revealable: unknown home allows (same convention as treeFollowTarget)", () => {
  assert.equal(revealable({ box: "", path: "/etc/x" }, "").ok, true);
  assert.equal(revealable({ box: "", path: "/etc/x" }, []).ok, true);
});

test("revealable: a roots array lets a file under any root be shown and refuses the rest", () => {
  const roots = ["/home/u", "/srv/code"];
  assert.deepEqual(revealable({ box: "", path: "/srv/code/api/a.go" }, roots), { ok: true });
  assert.deepEqual(revealable({ box: "", path: "/home/u/a.go" }, roots), { ok: true });
  const o = revealable({ box: "", path: "/opt/x/a.go" }, roots);
  assert.equal(o.ok, false);
  assert.match(o.title, /workspace roots/);
  assert.equal(revealable({ box: "", path: "/srv/code2/a.go" }, roots).ok, false, "whole components");
  assert.equal(revealable({ box: "", path: "/srv/code/a.go" }, ["/srv/code/"]).ok, true, "a trailing slash does not matter");
  assert.equal(revealable({ box: "s1", path: "/srv/code/a.go" }, roots).ok, false, "a box file is never revealable");
});

test("revealable: with no argument the page's window.WORKSPACE_ROOTS and HOME_DIR decide", () => {
  const had = Object.getOwnPropertyDescriptor(globalThis, "window");
  try {
    globalThis.window = { WORKSPACE_ROOTS: ["/data/u", "/srv/code"], HOME_DIR: "/home/u" };
    assert.equal(revealable({ box: "", path: "/srv/code/a.go" }).ok, true);
    assert.equal(revealable({ box: "", path: "/home/u/a.go" }).ok, true);
    assert.equal(revealable({ box: "", path: "/opt/a.go" }).ok, false);
    globalThis.window = { HOME_DIR: "/home/u" };
    assert.equal(revealable({ box: "", path: "/srv/code/a.go" }).ok, false);
  } finally {
    if (had) Object.defineProperty(globalThis, "window", had); else delete globalThis.window;
  }
});

// alternatesOutcome: what the viewer does with GET /api/fs/alternates after a 404.
test("alternatesOutcome: nothing, or anything unusable, is none", () => {
  assert.deepEqual(alternatesOutcome([]), { kind: "none" });
  assert.deepEqual(alternatesOutcome(null), { kind: "none" });
  assert.deepEqual(alternatesOutcome(undefined), { kind: "none" });
  assert.deepEqual(alternatesOutcome([{ worktree: "w" }, null, { path: 5 }]), { kind: "none" });
});

test("alternatesOutcome: one candidate names the path and worktree", () => {
  assert.deepEqual(alternatesOutcome([{ path: "/r/.claude/worktrees/w/a.md", worktree: "w", mtime: 5 }]),
    { kind: "one", path: "/r/.claude/worktrees/w/a.md", worktree: "w" });
  // the main checkout is worktree ""
  assert.deepEqual(alternatesOutcome([{ path: "/r/a.md", worktree: "", mtime: 5 }]),
    { kind: "one", path: "/r/a.md", worktree: "" });
});

test("alternatesOutcome: several candidates are a pick list, newest first", () => {
  const o = alternatesOutcome([
    { path: "/r/w1/a.md", worktree: "w1", mtime: 100 },
    { path: "/r/a.md", worktree: "", mtime: 300 },
    { path: "/r/w2/a.md", worktree: "w2", mtime: 200 },
  ]);
  assert.equal(o.kind, "pick");
  assert.deepEqual(o.items.map((i) => i.worktree), ["", "w2", "w1"]);
  assert.deepEqual(o.items[0], { path: "/r/a.md", worktree: "", mtime: 300 });
});

test("alternatesOutcome: ties keep the server order, and the list is capped at 64", () => {
  const o = alternatesOutcome([
    { path: "/a", worktree: "a", mtime: 1 }, { path: "/b", worktree: "b", mtime: 1 },
  ]);
  assert.deepEqual(o.items.map((i) => i.worktree), ["a", "b"]);
  const many = Array.from({ length: 70 }, (_, i) => ({ path: "/p" + i, worktree: "w" + i, mtime: i }));
  assert.equal(alternatesOutcome(many).items.length, 64);
});

test("alternatesOutcome: a missing mtime sorts last and does not throw", () => {
  const o = alternatesOutcome([{ path: "/a", worktree: "a" }, { path: "/b", worktree: "b", mtime: 9 }]);
  assert.deepEqual(o.items.map((i) => i.worktree), ["b", "a"]);
});

test("altNote: the sentence for a single candidate, with the worktree name split out", () => {
  assert.deepEqual(altNote("w"), { pre: "Opened from worktree ", name: "w", post: " \u2014 not found here." });
  assert.deepEqual(altNote(""), { pre: "Opened from the main checkout \u2014 not found in this worktree.", name: "", post: "" });
});

test("agoText: coarse relative time", () => {
  assert.equal(agoText(1000, 1030), "just now");
  assert.equal(agoText(1000, 1000 + 5 * 60), "5 min ago");
  assert.equal(agoText(1000, 1000 + 3 * 3600), "3 h ago");
  assert.equal(agoText(1000, 1000 + 2 * 86400), "2 d ago");
  assert.equal(agoText(2000, 1000), "just now"); // clock skew
  assert.equal(agoText(0, 1000), "");              // unknown mtime
});
