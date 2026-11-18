"use strict";
// Source pins for the markdown preview's safety (#62: "Markdown preview runs no script and loads no remote resource; a test pins
// it"). The two previews, the dock's Preview (editormd.js) and the phone's viewer (viewer-tab.js), are DOM code that node cannot run,
// so what is pinned here is the wiring that keeps them on the one safe path: both render through mdRenderFragment (mdsafe.js) and
// neither sanitizes or parses markdown itself, mdRenderFragment passes MD_PURIFY to DOMPurify as a fragment and walks the result
// with the policy functions, and no other script of the page makes HTML out of markdown or feeds DOMPurify. md-render.test.js runs
// the walk on hostile fragments; the browser check of the task report runs the real DOMPurify and marked on the hostile file.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "").replace(/ \/\/ .*$/gm, "");
// The source of a function or method: from its header to the first line that closes it at the same indent.
function body(file, header) {
  const text = stripComments(read(file));
  const i = text.indexOf(header);
  assert.ok(i >= 0, header + " is in " + file);
  const indent = /^ */.exec(text.slice(text.lastIndexOf("\n", i) + 1))[0];
  const end = text.indexOf("\n" + indent + "}\n", i);
  assert.ok(end > i, header + " closes");
  return text.slice(i, end + 2 + indent.length);
}

test("both previews render through mdRenderFragment, with DOMPurify, and neither sanitizes or parses markdown itself", () => {
  for (const [file, header] of [["viewer-tab.js", "renderMarkdown(text) {"], ["editormd.js", "function editorMdBody(tab, text) {"]]) {
    const b = body(file, header);
    assert.match(b, /mdRenderFragment\(text, \{/, file + " calls the one renderer");
    assert.match(b, /purify: DOMPurify,/, file + " hands it the page's DOMPurify");
    assert.match(b, /rewrite: /, file + " says where a relative image is read from");
    assert.ok(!/\.sanitize\(/.test(b), file + " does not call DOMPurify.sanitize itself");
    assert.ok(!/marked\.parse\(/.test(b), file + " does not parse markdown itself");
    assert.ok(!/\.innerHTML\s*=/.test(b), file + " writes no markup of its own");
  }
});

test("mdRenderFragment passes MD_PURIFY, merged with RETURN_DOM_FRAGMENT, to DOMPurify, and walks the fragment before it returns it", () => {
  const b = body("mdsafe.js", "function mdRenderFragment(text, deps) {");
  assert.match(b, /deps\.purify\.sanitize\(html, Object\.assign\(\{\}, MD_PURIFY, \{ RETURN_DOM_FRAGMENT: true \}\)\)/);
  assert.ok(b.indexOf("mdWalkFragment(frag, deps.rewrite)") > b.indexOf("deps.purify.sanitize("), "the walk runs on what DOMPurify returned");
  assert.ok(b.indexOf("return frag;") > b.indexOf("mdWalkFragment("), "and nothing is returned before it");
});

test("the walk checks every element's src, href and xlink:href with mdUnsafeAttr and every attribute for a CSS fetch, and mdIsRelative decides what is rewritten", () => {
  const b = body("mdsafe.js", "function mdWalkFragment(root, rewrite) {");
  assert.match(b, /root\.querySelectorAll\("\*"\)/, "every element, not a list of them");
  assert.match(b, /for \(const name of MS_URL_ATTRS\) \{\s*if \(mdUnsafeAttr\(tag, name, el\.getAttribute\(name\)\)\) el\.removeAttribute\(name\);/);
  assert.match(b, /for \(const attr of Array\.from\(el\.attributes\)\) \{\s*if \(mdUnsafeCss\(attr\.value\)\) el\.removeAttribute\(attr\.name\);/);
  assert.match(b, /if \(tag === "template"\) \{ el\.remove\(\); continue; \}/);
  const m = body("mdsafe.js", "function mdWalkMedia(doc, el, tag, rewrite) {");
  assert.match(m, /if \(mdIsRelative\(src\)\) \{ src = rewrite\(src\); el\.setAttribute\("src", src\); \}\s*if \(!mdUnsafeAttr\(tag, "src", src\)\) return false;/, "rewritten first, then checked");
  // the policy functions answer through isLocalSrc, so one definition of "local" decides every one of them
  const text = stripComments(read("mdsafe.js"));
  for (const fn of ["function mdUnsafeAttr(", "function mdIsRelative(", "function mdUnsafeCss("]) {
    const f = text.slice(text.indexOf(fn), text.indexOf("\n}\n", text.indexOf(fn)));
    assert.match(f, /isLocalSrc\(/, fn + " asks isLocalSrc");
  }
});

test("no other script of the page sanitizes, or makes HTML out of markdown", () => {
  const html = read("index.html");
  const files = [...html.matchAll(/<script\s+src="\/([^"?#]+\.js)"/g)].map((m) => m[1]).filter((f) => !f.startsWith("vendor/"));
  assert.ok(files.length > 50, "found the page scripts");
  const sanitizers = [];
  const parsers = [];
  for (const f of files) {
    const text = stripComments(read(f));
    if (/\.sanitize\(/.test(text)) sanitizers.push(f);
    if (/\bmarked\.parse\(/.test(text)) parsers.push(f);
  }
  assert.deepEqual(sanitizers, ["mdsafe.js"], "DOMPurify is fed in one place");
  assert.deepEqual(parsers, ["mdsafe.js"], "marked is called in one place");
});

test("the page loads mdsafe.js and editormd.js once each, before the dock", () => {
  const html = read("index.html");
  for (const f of ["mdsafe.js", "editormd.js"]) {
    assert.equal(html.split('<script src="/' + f + '"></script>').length - 1, 1, f + " is a page script, once");
    assert.ok(html.indexOf('<script src="/' + f + '"></script>') < html.indexOf('<script src="/filesdrawer.js"></script>'), f + " loads before filesdrawer.js");
  }
  assert.ok(html.includes('<script src="/vendor/purify.min.js"></script>'), "DOMPurify is the vendored copy, same origin");
});

test("the preview re-renders 300 ms after an edit and holds a subscription to the buffer only while it is on screen", () => {
  const text = stripComments(read("editormd.js"));
  assert.match(text, /const EMD_DEBOUNCE_MS = 300;/);
  assert.match(text, /setTimeout\(\(\) => \{[\s\S]*?\}, EMD_DEBOUNCE_MS\);/);
  assert.match(body("editormd.js", "function editorMdDetach(tab) {"), /editorMdStop\(m\)/);
  assert.match(body("editormd.js", "function editorMdDrop(tab) {"), /editorMdStop\(m\)/);
});
