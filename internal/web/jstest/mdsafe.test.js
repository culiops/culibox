"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { isLocalSrc, mdUnsafeAttr, MD_PURIFY, mdIsRelative, mdInertRef, mdUnsafeCss, mdUnsafeLink } = require("../static/mdsafe.js");
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
  assert.deepStrictEqual(MD_PURIFY.FORBID_TAGS,
    ["script", "style", "iframe", "object", "embed", "form", "link", "meta", "base", "input", "track"]);
  assert.deepStrictEqual(MD_PURIFY.FORBID_ATTR, ["srcset", "formaction", "style", "poster", "background", "ping"]);
  assert.deepStrictEqual(Object.keys(MD_PURIFY).sort(), ["FORBID_ATTR", "FORBID_TAGS"]);
});

test("MD_PURIFY closes the remote-load channels the src walk does not see", () => {
  // each of these is a request the page would make for the author of the file
  const channel = {
    "style attribute (url(...))": ["FORBID_ATTR", "style"],
    "<video poster>": ["FORBID_ATTR", "poster"],
    "background attribute": ["FORBID_ATTR", "background"],
    "<img srcset>": ["FORBID_ATTR", "srcset"],
    "<a ping>": ["FORBID_ATTR", "ping"],
    "formaction": ["FORBID_ATTR", "formaction"],
    "<input type=image src>": ["FORBID_TAGS", "input"],
    "<track src>": ["FORBID_TAGS", "track"],
    "<style> element": ["FORBID_TAGS", "style"],
  };
  for (const [what, [list, name]] of Object.entries(channel)) assert.ok(MD_PURIFY[list].includes(name), what);
});

test("mdUnsafeAttr: a media element's src must be local and no longer relative; the walk rewrites a relative one to a read route first", () => {
  for (const tag of ["img", "video", "audio", "source"]) {
    for (const url of ["https://evil.example/p.png", "//evil.example/p.png", "http://x/y", "javascript:alert(1)", "data:image/svg+xml,x", "/etc/x.png", "",
      "a.png", "../x.png", "./a/b.webm", "api/health?probe=1", "#frag"]) {
      assert.strictEqual(mdUnsafeAttr(tag, "src", url), true, tag + " src " + JSON.stringify(url));
    }
    for (const url of ["/api/fs/read?path=a.png", "/api/sessions/s1/boxfile?path=%2Fa.png", "data:image/png;base64,iVBORw0KGgo="]) {
      assert.strictEqual(mdUnsafeAttr(tag, "src", url), false, tag + " src " + url);
    }
  }
});

const NOT_INERT = [
  "api/health?probe=1", "api/sessions/probe2", "../probe3.png", "./a.png", "a.png", "x.svg#f", "?x=1", "", "   ",
  "/api/health", "/api/fs/read?path=a.png", "//evil.example/p.png", "https://evil.example/p.png", "javascript:alert(1)",
  "data:image/svg+xml,x", "data:text/html,x", "data:image/png,raw", "\\\\evil.example\\a",
];

test("mdUnsafeAttr: on any element but an SVG image an href or xlink:href may only point into the document or hold a small inline image: a relative path is a GET of a route of the app", () => {
  for (const tag of ["feImage", "use", "math", "mglyph", "area", "textPath", "linearGradient", "mpath", "altGlyph", "span"]) {
    for (const attr of ["href", "xlink:href", "XLINK:HREF", "Href", "xml:href"]) {
      for (const url of NOT_INERT) assert.strictEqual(mdUnsafeAttr(tag, attr, url), true, tag + " " + attr + " " + JSON.stringify(url));
      for (const url of ["#shape", "#", " #a", "\t#a", "data:image/png;base64,iVBORw0KGgo="]) {
        assert.strictEqual(mdUnsafeAttr(tag, attr, url), false, tag + " " + attr + " " + JSON.stringify(url));
      }
    }
  }
});

test("mdUnsafeAttr: an SVG image fetches its href, even a fragment (the page itself), so only a small inline image is safe on it", () => {
  for (const attr of ["href", "xlink:href", "XLINK:HREF"]) {
    for (const url of [...NOT_INERT, "#x", "#", " #x"]) assert.strictEqual(mdUnsafeAttr("image", attr, url), true, attr + " " + JSON.stringify(url));
    for (const url of ["data:image/png;base64,iVBORw0KGgo=", "DATA:IMAGE/GIF;BASE64,AAAA"]) assert.strictEqual(mdUnsafeAttr("image", attr, url), false, attr + " " + url);
  }
});

test("mdUnsafeAttr: a src on an element that is not media goes whatever it holds: a frame, an embed, a track, mglyph", () => {
  for (const tag of ["mglyph", "input", "embed", "track", "frame", "iframe", "bgsound", "image", "feImage", "span"]) {
    for (const url of [...NOT_INERT, "#ok", "data:image/png;base64,iVBORw0KGgo=", "/api/fs/read?path=a.png"]) assert.strictEqual(mdUnsafeAttr(tag, "src", url), true, tag + " " + JSON.stringify(url));
  }
});

test("mdUnsafeAttr: an a keeps its remote href, and other attributes are not its business", () => {
  assert.strictEqual(mdUnsafeAttr("a", "href", "https://example.com/doc"), false);
  assert.strictEqual(mdUnsafeAttr("A", "href", "https://example.com/doc"), false);
  assert.strictEqual(mdUnsafeAttr("a", "xlink:href", "https://example.com/doc"), false);
  assert.strictEqual(mdUnsafeAttr("img", "alt", "https://example.com/x"), false);
  assert.strictEqual(mdUnsafeAttr("img", "title", "https://example.com/x"), false);
  assert.strictEqual(mdUnsafeAttr("div", "class", "//host"), false);
  assert.strictEqual(mdUnsafeAttr("a", "href", "api/health"), false, "a relative link is a click");
  assert.strictEqual(mdUnsafeAttr("image", "width", "api/health"), false, "only src and href are URLs");
  assert.strictEqual(mdUnsafeAttr("image", "data-src", "api/health"), false, "a name that merely ends in src is not one (no colon)");
});

test("mdUnsafeAttr: a tag name that is a property of Object is no media element", () => {
  for (const tag of ["constructor", "toString", "__proto__", "hasOwnProperty", "valueOf"]) {
    assert.strictEqual(mdUnsafeAttr(tag, "src", "/api/fs/read?path=a.png"), true, tag + ": not media, so its src goes");
    assert.strictEqual(mdUnsafeAttr(tag, "href", "#a"), false, tag + ": and its href may reach into the document");
  }
});

test("mdInertRef: a reference into the document or a small inline image, nothing else", () => {
  for (const u of ["#a", "#", "  #a", "\t#a", "#a b", "data:image/png;base64,AAAA", "DATA:IMAGE/GIF;BASE64,AAAA"]) assert.strictEqual(mdInertRef(u), true, JSON.stringify(u));
  for (const u of [
    "a.png", "./a.png", "x.svg#f", "?x=1", "/api/fs/read?path=a", "//evil.example/x", "https://evil.example/x", "data:image/svg+xml;base64,AAAA",
    "data:text/html,x", "javascript:alert(1)", "", "  ", null, undefined, 5,
  ]) assert.strictEqual(mdInertRef(u), false, JSON.stringify(u));
});

test("mdUnsafeAttr: tag and attribute names are read case-insensitively; a missing attribute is nothing to remove", () => {
  assert.strictEqual(mdUnsafeAttr("IMG", "SRC", "https://evil.example/p.png"), true);
  assert.strictEqual(mdUnsafeAttr("Use", "XLINK:HREF", "https://evil.example/s.svg#a"), true);
  assert.strictEqual(mdUnsafeAttr("IMAGE", "Xlink:Href", "api/health"), true);
  assert.strictEqual(mdUnsafeAttr("img", "src", null), false);
  assert.strictEqual(mdUnsafeAttr("img", "src", undefined), false);
});

test("MD_PURIFY cannot be loosened by a caller", () => {
  assert.throws(() => { MD_PURIFY.FORBID_TAGS.pop(); }, TypeError);
  assert.throws(() => { MD_PURIFY.FORBID_ATTR.length = 0; }, TypeError);
  assert.throws(() => { MD_PURIFY.RETURN_DOM_FRAGMENT = true; }, TypeError);
  assert.strictEqual(MD_PURIFY.FORBID_TAGS.length, 11);
});

test("mdIsRelative: a path with no scheme and no leading slash, and nothing the browser would read as something else", () => {
  for (const u of ["a.png", "./a.png", "../a/b.png", "docs/shot 1.png", "img.png?x=1#y", "#frag"]) assert.strictEqual(mdIsRelative(u), true, u);
  for (const u of [
    "/a.png", "/api/fs/read?path=a", "//evil.example/a.png", "https://evil.example/a.png", "data:image/png;base64,AAAA", "a:b.png",
    "java\nscript:alert(1)", "  \\\\evil.example\\a.png", "", "   ", null, undefined,
  ]) assert.strictEqual(mdIsRelative(u), false, JSON.stringify(u));
});

test("mdUnsafeCss: on a CSS attribute a url() is safe only when inert, whatever the quoting, case or spacing", () => {
  for (const v of [
    "url(https://evil.example/f.svg#f)", "url('https://evil.example/f.svg#f')", 'url("https://evil.example/f.svg#f")', "URL(https://evil.example/x)",
    "url( https://evil.example/x )", "url ( //evil.example/x )", "none url(//evil.example/x) repeat", "url(javascript:alert(1))",
    "url(data:image/svg+xml,x)", "url(/etc/x)", "url()", "url('", 'url("https://evil.example/x', "url(#a) url(https://evil.example/x)",
    "url(api/health?probe=1)", "url('../probe3.png')", "url(a.png)", "url(x.svg#f)", "url(/api/fs/read?path=a.png)", "url(/api/health)",
  ]) for (const name of ["filter", "mask", "clip-path", "fill", "stroke", "marker-start", "cursor", "style", "FILL"]) assert.strictEqual(mdUnsafeCss(name, v), true, name + " " + v);
});

test("mdUnsafeCss: a reference into the same document or a small inline image is safe on a CSS attribute", () => {
  for (const v of ["url(#grad)", "url('#grad')", 'url( "#a" )', "url(data:image/png;base64,iVBORw0KGgo=)", "url(#a) url(#b)", "url(#a) none"]) {
    assert.strictEqual(mdUnsafeCss("filter", v), false, v);
  }
});

test("mdUnsafeCss: on any other attribute a url() is still looked at, and refused when its target is not local", () => {
  for (const name of ["alt", "title", "aria-label", "data-x", "class", "href"]) {
    assert.strictEqual(mdUnsafeCss(name, "see url(https://evil.example/x)"), true, name);
    assert.strictEqual(mdUnsafeCss(name, "see url(foo.png)"), false, name + ": a sentence that names a relative file");
  }
});

test("mdUnsafeCss: escapes are undone before the name is read", () => {
  for (const v of ["u\\72l(https://evil.example/x)", "\\75 rl(https://evil.example/x)", "\\000075rl(https://evil.example/x)", "url(https\\3a //evil.example/x)", "\\75\\72\\6c(https://evil.example/x)"]) {
    assert.strictEqual(mdUnsafeCss("mask", v), true, v);
  }
  assert.strictEqual(mdUnsafeCss("mask", "url(\\23 a)"), false, "an escape that spells a same-document reference is one");
  assert.strictEqual(mdUnsafeCss("mask", "url\\28 https://evil.example/x)"), false, "an escaped parenthesis is no call (and has no paren to find)");
});

test("mdUnsafeCss: the other CSS functions that take an image by address are unsafe on a CSS attribute; a function that fetches nothing and plain values are not", () => {
  for (const v of ["image-set('https://evil.example/x.png' 1x)", "-webkit-image-set(url(#a) 1x)", "image('x.png')", "src('x.png')", "cross-fade(url(#a), url(#b), 50%)", "element(#a)"]) {
    for (const name of ["mask", "fill", "filter", "style", "cursor"]) assert.strictEqual(mdUnsafeCss(name, v), true, name + " " + v);
  }
  for (const v of ["rotate(45)", "translate(10px, 5px)", "rgb(1, 2, 3)", "calc(1px + 2px)", "0.5", "M0 0L1 1", "none", "", null, undefined, 5]) {
    assert.strictEqual(mdUnsafeCss("transform", v), false, String(v));
  }
});

test("mdUnsafeCss: free text that happens to hold a function name is a sentence, not CSS: alt, title, aria-*, class, a link's address", () => {
  for (const name of ["alt", "title", "aria-label", "aria-description", "class", "id", "name", "lang", "placeholder", "summary", "label", "href", "xlink:href", "src"]) {
    for (const v of ["Architecture image (v2)", "Page element (x)", "see src(1) and element(x)", "https://example.com/image(1).png", "image-set(x)", "A (src) B"]) {
      assert.strictEqual(mdUnsafeCss(name, v), false, name + ": " + v);
    }
  }
});

test("mdUnsafeCss: every attribute DOMPurify keeps that is parsed as CSS is known as one (the SVG presentation attributes of the vendored list)", () => {
  for (const name of ["clip-path", "filter", "mask", "fill", "stroke", "marker-start", "marker-mid", "marker-end", "stop-color", "flood-color", "lighting-color",
    "color", "opacity", "transform", "font-family", "text-decoration", "visibility", "display", "overflow", "clip", "paint-order", "image-rendering"]) {
    assert.strictEqual(mdUnsafeCss(name, "image('https://evil.example/x')"), true, name);
  }
});

test("mdUnsafeLink: javascript:, vbscript: and data: hrefs, however they are spelled; every other link is the user's to follow", () => {
  for (const u of ["javascript:alert(1)", "JaVaScRiPt:alert(1)", "  javascript:alert(1)", "java\nscript:alert(1)", "\u0001javascript:x", "vbscript:msgbox(1)", "data:text/html,x", "DATA:text/html,x"]) {
    assert.strictEqual(mdUnsafeLink(u), true, JSON.stringify(u));
  }
  for (const u of ["https://example.com/", "http://example.com/", "mailto:a@b.c", "tel:+1", "ftp://x/y", "#top", "other.md", "/docs/a.md", "//example.com/", "", null, undefined]) {
    assert.strictEqual(mdUnsafeLink(u), false, JSON.stringify(u));
  }
});

test("isLocalSrc: a kept file's read route is local, a look-alike or off-origin one is not", () => {
  assert.strictEqual(isLocalSrc("/api/profiles/my%20prof/kept/file?key=aaaaaaaaaaaa&path=.culiops%2Fa.png"), true);
  for (const u of [
    "/api/profiles/x/kept/forget", "/api/profiles/x/kept/forget?key=a", "/api/profiles/x/kept/list?key=a",
    "/api/profiles/x/y/kept/file?key=a", "/api/profiles//kept/file?key=a", "/api/profiles/x/kept/file",
    "https://evil.example/api/profiles/x/kept/file?key=a", "//evil.example/api/profiles/x/kept/file?key=a",
  ]) assert.strictEqual(isLocalSrc(u), false, u);
});
