// internal/web/jstest/dockcrumbs.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { crumbsFor, relTime, editedLabel } = require("../static/dockcrumbs.js");

test("crumbsFor under root", () => {
  assert.deepStrictEqual(crumbsFor("/home/u/proj/src/a.go", "/home/u/proj"), [
    { name: "proj", path: "/home/u/proj" },
    { name: "src", path: "/home/u/proj/src" },
    { name: "a.go", path: "/home/u/proj/src/a.go" },
  ]);
  assert.deepStrictEqual(crumbsFor("/home/u/proj/a.go", "/home/u/proj/"), [
    { name: "proj", path: "/home/u/proj" },
    { name: "a.go", path: "/home/u/proj/a.go" },
  ]);
});

test("crumbsFor equals root", () => {
  assert.deepStrictEqual(crumbsFor("/home/u/proj", "/home/u/proj"), [{ name: "proj", path: "/home/u/proj" }]);
  assert.deepStrictEqual(crumbsFor("/home/u/proj/", "/home/u/proj"), [{ name: "proj", path: "/home/u/proj" }]);
  assert.deepStrictEqual(crumbsFor("/home/u/proj", "/home/u/proj//"), [{ name: "proj", path: "/home/u/proj" }]);
});

test("crumbsFor outside root", () => {
  // Segments run from "/", each with its absolute path, so a click reveals a real directory.
  assert.deepStrictEqual(crumbsFor("/etc/nginx/nginx.conf", "/home/u/proj"), [
    { name: "/", path: "/" },
    { name: "etc", path: "/etc" },
    { name: "nginx", path: "/etc/nginx" },
    { name: "nginx.conf", path: "/etc/nginx/nginx.conf" },
  ]);
  // A box file (its path means something only in that box) is outside a host root like any other path.
  assert.deepStrictEqual(crumbsFor("/workspace/a.txt", "/home/u/proj"), [
    { name: "/", path: "/" },
    { name: "workspace", path: "/workspace" },
    { name: "a.txt", path: "/workspace/a.txt" },
  ]);
  // The root's parent is outside it, and so is a sibling that only shares its prefix.
  assert.deepStrictEqual(crumbsFor("/home/u", "/home/u/proj").map((c) => c.path), ["/", "/home", "/home/u"]);
  assert.deepStrictEqual(crumbsFor("/home/u/proj2/x", "/home/u/proj").map((c) => c.path), ["/", "/home", "/home/u", "/home/u/proj2", "/home/u/proj2/x"]);
});

test("crumbsFor spaces and unicode", () => {
  assert.deepStrictEqual(crumbsFor("/home/u/My Project/Tài liệu/ghi chú #1?.md", "/home/u/My Project"), [
    { name: "My Project", path: "/home/u/My Project" },
    { name: "Tài liệu", path: "/home/u/My Project/Tài liệu" },
    { name: "ghi chú #1?.md", path: "/home/u/My Project/Tài liệu/ghi chú #1?.md" },
  ]);
  assert.deepStrictEqual(crumbsFor("/srv/データ/📁 notes/a%20b.txt", "").map((c) => c.name), ["/", "srv", "データ", "📁 notes", "a%20b.txt"]);
});

test("crumbsFor root of the filesystem and no root", () => {
  assert.deepStrictEqual(crumbsFor("/etc/x", "/"), [{ name: "/", path: "/" }, { name: "etc", path: "/etc" }, { name: "x", path: "/etc/x" }]);
  assert.deepStrictEqual(crumbsFor("/", "/"), [{ name: "/", path: "/" }]);
  for (const root of [null, undefined, "", 5]) {
    assert.deepStrictEqual(crumbsFor("/a/b", root).map((c) => c.path), ["/", "/a", "/a/b"]);
  }
});

test("crumbsFor normalizes doubled and trailing slashes and dots", () => {
  assert.deepStrictEqual(crumbsFor("/home/u//proj/./src/", "/home/u/proj").map((c) => c.path), ["/home/u/proj", "/home/u/proj/src"]);
  assert.deepStrictEqual(crumbsFor("/home/u/proj/src/../a.go", "/home/u/proj").map((c) => c.path), ["/home/u/proj", "/home/u/proj/a.go"]);
});

test("crumbsFor gives no crumbs for what is not an absolute path", () => {
  for (const p of ["", "a/b", null, undefined, 7, "/.."]) assert.deepStrictEqual(crumbsFor(p, "/r"), []);
});

test("relTime bands", () => {
  const now = 1700000000000;
  const S = 1000, M = 60 * S, H = 60 * M, D = 24 * H;
  const at = (ago) => relTime(now - ago, now);
  assert.equal(at(0), "just now");
  assert.equal(at(59 * S + 999), "just now");
  assert.equal(at(60 * S), "1 min ago");
  assert.equal(at(90 * S), "1 min ago"); // floored, not rounded
  assert.equal(at(5 * M), "5 min ago");
  assert.equal(at(59 * M + 59 * S), "59 min ago");
  assert.equal(at(60 * M), "1 h ago");
  assert.equal(at(23 * H + 59 * M), "23 h ago");
  assert.equal(at(24 * H), "1 d ago");
  assert.equal(at(3 * D + 5 * H), "3 d ago");
  assert.equal(at(400 * D), "400 d ago");
});

test("relTime reads a time in the future as just now and junk as nothing", () => {
  const now = 1700000000000;
  assert.equal(relTime(now + 5000, now), "just now"); // a clock a little ahead of ours
  assert.equal(relTime(NaN, now), "");
  assert.equal(relTime(undefined, now), "");
  assert.equal(relTime(now - 1000, NaN), "");
});

test("editedLabel with ts", () => {
  const now = 1700000000000;
  const files = [{ path: "/r/a.go", add: 1, del: 0, ts: now - 5 * 60000 }, { path: "/r/b.go", add: 1, del: 0 }];
  assert.equal(editedLabel(files, "/r/a.go", "builder", now), "Edited by builder · 5 min ago");
  assert.equal(editedLabel([{ path: "/r/a.go", ts: now - 2000 }], "/r/a.go", "Claude 2", now), "Edited by Claude 2 · just now");
  // The same path written with a doubled or trailing slash is the same file.
  assert.equal(editedLabel([{ path: "/r/a.go", ts: now - 3 * 3600000 }], "/r//a.go/", "builder", now), "Edited by builder · 3 h ago");
});

test("editedLabel without ts", () => {
  const files = [{ path: "/r/b.go", add: 1, del: 0 }, { path: "/r/c.go", ts: 0 }, { path: "/r/d.go", ts: "yesterday" }];
  for (const p of ["/r/b.go", "/r/c.go", "/r/d.go"]) assert.equal(editedLabel(files, p, "builder", 1700000000000), "Edited by builder");
});

test("editedLabel absent", () => {
  const files = [{ path: "/r/a.go", ts: 1 }];
  assert.equal(editedLabel(files, "/r/other.go", "builder", 2), "");
  assert.equal(editedLabel([], "/r/a.go", "builder", 2), "");
  assert.equal(editedLabel(null, "/r/a.go", "builder", 2), "");
  assert.equal(editedLabel(undefined, "/r/a.go", "builder", 2), "");
  assert.equal(editedLabel(files, "", "builder", 2), "");
  assert.equal(editedLabel(files, null, "builder", 2), "");
  assert.equal(editedLabel([null, {}, { path: 3 }], "/r/a.go", "builder", 2), "");
});

test("editedLabel gives no label without a session name", () => {
  const files = [{ path: "/r/a.go", ts: 1 }, { path: "/r/b.go" }];
  for (const name of [undefined, null, "", "   ", 5, {}]) {
    assert.equal(editedLabel(files, "/r/a.go", name, 2), "");
    assert.equal(editedLabel(files, "/r/b.go", name, 2), "");
  }
});
