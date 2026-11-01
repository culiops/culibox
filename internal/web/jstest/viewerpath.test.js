"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  ancestorsBetween, pathLineText, relativePath, relativeAvail, insertTargetFor, revealable,
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
  assert.match(o.title, /home/);
  assert.equal(revealable({ box: "", path: "/home/user2/a.go" }, "/home/user").ok, false);
});

test("revealable: unknown home allows (same convention as treeFollowTarget)", () => {
  assert.equal(revealable({ box: "", path: "/etc/x" }, "").ok, true);
});
