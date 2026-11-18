"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { isLocalSrc, MD_PURIFY } = require("../static/mdsafe.js");
const { boxFileURL } = require("../static/fsops.js");

test("isLocalSrc: relative paths and same-origin file reads are local", () => {
  for (const u of [
    "img.png", "./a/b.png", "../x.png", "a/b/c.png", "docs/shot 1.png", "a%20b.png", "img.png?x=1#y", "#frag",
    "/api/fs/read?path=%2Fa%2Fb.png",
    "/api/fs/read?path=a.png&download=1",
    boxFileURL("s-123", "/work/shot.png"),
    boxFileURL("s-123", "/work/shot.png", true),
  ]) assert.strictEqual(isLocalSrc(u), true, u);
});

test("isLocalSrc: remote, script and file schemes are not local, in any case or spacing", () => {
  for (const u of [
    "http://evil.example/p.png", "https://evil.example/p.png", "HTTPS://evil.example/p.png", "HtTp://x/y.png",
    "javascript:alert(1)", "JavaScript:alert(1)", "  javascript:alert(1)", "\tjavascript:alert(1)", "\u0001javascript:alert(1)",
    "java\nscript:alert(1)", "java\tscript:alert(1)", "java\rscript:alert(1)", "javascript\n:alert(1)",
    "vbscript:msgbox(1)", "VBSCRIPT:x",
    "blob:https://x/uuid", "file:///etc/passwd", "FILE:///etc/passwd", "ftp://x/y", "mailto:a@b.c", "about:blank",
    "chrome://x", "a:b.png", "x-evil:foo",
  ]) assert.strictEqual(isLocalSrc(u), false, JSON.stringify(u));
});

test("isLocalSrc: protocol-relative URLs are remote, with slashes or backslashes", () => {
  for (const u of [
    "//evil.example/p.png", "  //evil.example/p.png", "///evil.example/p.png",
    "/\\evil.example/p.png", "\\\\evil.example\\p.png", "\\/evil.example/p.png", "\\evil.example/p.png",
    "/\t/evil.example/p.png", "/\n/evil.example/p.png", "a\\b.png", "/api/fs/read?path=a\\..\\b",
  ]) assert.strictEqual(isLocalSrc(u), false, JSON.stringify(u));
});

test("isLocalSrc: only data:image png, gif, jpeg and webp in base64 are local", () => {
  for (const u of [
    "data:image/png;base64,iVBORw0KGgo=", "data:image/gif;base64,R0lGODlhAQABAAAAACw=", "data:image/jpeg;base64,/9j/4AAQSkZJRg==",
    "data:image/webp;base64,UklGRhoAAABXRUJQ", "DATA:IMAGE/PNG;BASE64,iVBORw0KGgo=", "  data:image/png;base64,AAAA",
  ]) assert.strictEqual(isLocalSrc(u), true, u);
  for (const u of [
    "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=", "data:image/svg+xml,<svg onload=alert(1)>", "data:text/html;base64,PHNjcmlwdD4=",
    "data:text/html,<script>alert(1)</script>", "data:application/javascript,alert(1)", "data:image/png,rawbytes",
    "data:image/png;charset=utf-8;base64,AAAA", "data:image/bmp;base64,AAAA", "data:image/x-icon;base64,AAAA", "data:,hello", "data:",
    "data:image/png;base64,AAAA\"onerror=\"alert(1)", "data:image/png;base64,AAAA<script>", "data:image/png;base64,AAAA data:text/html,x",
  ]) assert.strictEqual(isLocalSrc(u), false, u);
});

test("isLocalSrc: an absolute path is local only on the two read routes", () => {
  for (const u of [
    "/", "/etc/passwd", "/favicon.svg", "/api/sessions", "/api/fs/write", "/api/fs/read", "/api/fs/readx?a=1",
    "/api/fs/../sessions?", "/API/FS/READ?path=a", "/api/sessions/x/y/boxfile?path=a", "/api/sessions//boxfile?path=a",
    "/api/sessions/x/boxfile", "/api/sessions/a?b/boxfile?path=x",
  ]) assert.strictEqual(isLocalSrc(u), false, u);
});

test("isLocalSrc: nothing, or not a string, is not local", () => {
  for (const u of ["", "   ", "\n", null, undefined, 5, {}, ["a.png"]]) assert.strictEqual(isLocalSrc(u), false, String(u));
});

test("isLocalSrc: control characters inside the URL are refused", () => {
  assert.strictEqual(isLocalSrc("a\u0000b.png"), false);
  assert.strictEqual(isLocalSrc("a\u007fb.png"), false);
  assert.strictEqual(isLocalSrc("a\u001fb.png"), false);
});

test("MD_PURIFY forbids", () => {
  assert.deepStrictEqual(MD_PURIFY.FORBID_TAGS, ["script", "style", "iframe", "object", "embed", "form", "link", "meta", "base"]);
  assert.deepStrictEqual(MD_PURIFY.FORBID_ATTR, ["srcset", "formaction"]);
  assert.deepStrictEqual(Object.keys(MD_PURIFY).sort(), ["FORBID_ATTR", "FORBID_TAGS"]);
});

test("MD_PURIFY cannot be loosened by a caller", () => {
  assert.throws(() => { MD_PURIFY.FORBID_TAGS.pop(); }, TypeError);
  assert.throws(() => { MD_PURIFY.FORBID_ATTR.length = 0; }, TypeError);
  assert.throws(() => { MD_PURIFY.RETURN_DOM_FRAGMENT = true; }, TypeError);
  assert.strictEqual(MD_PURIFY.FORBID_TAGS.length, 9);
});
