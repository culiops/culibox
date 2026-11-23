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
const { MD_PURIFY, mdUnsafeAttr } = require("../static/mdsafe.js");

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

test("every <a> goes through the link gate: the walk gates it, the notice's link is made by the same function, and the dock names the file", () => {
  const b = body("mdsafe.js", "function mdWalkFragment(root, rewrite, where) {");
  assert.match(b, /if \(tag === "a"\) mdWalkLink\(el, where\);/);
  const text = stripComments(read("mdsafe.js"));
  const link = text.slice(text.indexOf("function mdLink("), text.indexOf("\n}\n", text.indexOf("function mdLink(")));
  assert.match(link, /mdGate\(a, url, where\)/, "the blocked-image notice's link is gated too");
  assert.ok(!/setAttribute\("href"/.test(text), "mdsafe.js sets no href on any link: an address is only ever kept in data-href");
  assert.match(body("editormd.js", "function editorMdBody(tab, text) {"), /where: tab\.path\.slice\(tab\.path\.lastIndexOf\("\/"\) \+ 1\),/, "the dock says which file the link is in");
});

test("mdFinish passes MD_PURIFY, merged with RETURN_DOM_FRAGMENT, to DOMPurify, and walks the fragment before it returns it", () => {
  const b = body("mdsafe.js", "function mdFinish(html, deps) {");
  assert.match(b, /deps\.purify\.sanitize\(html, Object\.assign\(\{\}, MD_PURIFY, \{ RETURN_DOM_FRAGMENT: true \}\)\)/);
  assert.ok(b.indexOf("mdWalkFragment(frag, deps.rewrite, deps.where)") > b.indexOf("deps.purify.sanitize("), "the walk runs on what DOMPurify returned");
  assert.ok(b.indexOf("return frag;") > b.indexOf("mdWalkFragment("), "and nothing is returned before it");
});

test("mdsafe.js sanitizes in one place, and every HTML marked makes there goes straight into mdFinish", () => {
  const text = stripComments(read("mdsafe.js"));
  assert.equal(text.split(".sanitize(").length - 1, 1, "DOMPurify is called once, in mdFinish");
  const made = text.match(/marked\.(parse|parser)\([^;]*/g) || [];
  assert.ok(made.length >= 1, "mdsafe.js makes HTML with marked");
  for (const m of made) assert.ok(/^marked\.(parse|parser)\([^)]*\)\s*,\s*deps\)/.test(m) && text.includes("mdFinish(deps." + m), "straight into mdFinish: " + m);
  assert.match(body("mdsafe.js", "function mdRenderFragment(text, deps) {"), /mdFinish\(deps\.marked\.parse\(text\), deps\)/);
});

test("the walk checks every element's src, href and xlink:href with mdUnsafeAttr and every attribute for a CSS fetch, and mdIsRelative decides what is rewritten", () => {
  const b = body("mdsafe.js", "function mdWalkFragment(root, rewrite, where) {");
  assert.match(b, /root\.querySelectorAll\("\*"\)/, "every element, not a list of them");
  assert.match(b, /for \(const attr of Array\.from\(el\.attributes\)\) \{\s*if \(mdUnsafeAttr\(tag, attr\.name, attr\.value\) \|\| mdUnsafeCss\(attr\.name, attr\.value\)\) el\.removeAttribute\(attr\.name\);/, "every attribute of every element, by name, not a list of three");
  assert.match(b, /if \(tag === "template"\) \{ el\.remove\(\); continue; \}/);
  const m = body("mdsafe.js", "function mdWalkMedia(doc, el, tag, rewrite, where) {");
  assert.match(m, /if \(mdIsRelative\(src\)\) \{ src = rewrite\(src\); el\.setAttribute\("src", src\); \}\s*if \(!mdUnsafeAttr\(tag, "src", src\)\) return false;/, "rewritten first, then checked");
  // the policy functions answer through isLocalSrc, so one definition of "local" decides every one of them
  const text = stripComments(read("mdsafe.js"));
  for (const fn of ["function mdUnsafeAttr(", "function mdIsRelative(", "function mdUnsafeCss("]) {
    const f = text.slice(text.indexOf(fn), text.indexOf("\n}\n", text.indexOf(fn)));
    assert.match(f, /isLocalSrc\(/, fn + " asks isLocalSrc");
  }
});

// What the vendored DOMPurify keeps. Its lists are frozen arrays of strings in the minified source; the four attribute lists (HTML, SVG,
// MathML, XML) and the tag lists are found by what they hold. A different layout fails the test on purpose: the audit below is redone.
function purifyLists() {
  const src = read("vendor/purify.min.js");
  const version = /DOMPurify (\d+\.\d+\.\d+)/.exec(src);
  const lists = [...src.matchAll(/\[("[^"\]]*"(?:,"[^"\]]*")+)\]/g)].map((m) => [...m[1].matchAll(/"([^"]*)"/g)].map((x) => x[1]));
  const having = (...names) => lists.filter((l) => l.length > 3 && names.every((n) => l.includes(n)));
  const attrs = [...having("class", "src"), ...having("class", "href", "filter"), ...having("href", "mathvariant"), ...having("xlink:href", "xml:id")];
  const tags = [...having("div", "img", "video"), ...having("svg", "image", "circle"), ...having("feImage", "feBlend"), ...having("math", "mglyph", "mi")];
  assert.equal(attrs.length, 4, "found the HTML, SVG, MathML and XML attribute lists");
  assert.equal(tags.length, 4, "found the HTML, SVG, SVG filter and MathML tag lists");
  return { version: version && version[1], attrs: new Set(attrs.flat()), tags: new Set(tags.flat()) };
}

test("audit of the vendored DOMPurify: every attribute it keeps that can carry a URL is forbidden, walked, or named here as inert", () => {
  const { version, attrs } = purifyLists();
  assert.equal(version, "3.1.6", "a different DOMPurify: redo this audit (the lists below are for 3.1.6)");
  // The attributes the HTML standard (and SVG, MathML) read as a URL, or as a URL list.
  const URL_ATTRS = ["action", "background", "cite", "classid", "codebase", "data", "formaction", "href", "icon", "longdesc", "manifest", "ping",
    "poster", "profile", "src", "srcset", "usemap", "xlink:href", "archive", "dynsrc", "lowsrc"];
  // Kept by DOMPurify and not fetched, and why.
  const INERT = {
    action: "only a form takes one, and form is forbidden: on any other element it is ignored",
    cite: "a blockquote, q, ins or del's source, never fetched",
    usemap: "names an image map in the document, never fetched",
  };
  const kept = URL_ATTRS.filter((a) => attrs.has(a)).sort();
  assert.deepEqual(kept, ["action", "background", "cite", "href", "poster", "src", "srcset", "usemap", "xlink:href"], "the URL attributes DOMPurify 3.1.6 keeps");
  for (const a of kept) {
    const forbidden = MD_PURIFY.FORBID_ATTR.includes(a);
    // walked: the walk drops a value on an element that is not media (an SVG image, say) that is not inert
    const walked = mdUnsafeAttr("image", a, "api/health?probe=1") && mdUnsafeAttr("image", a, "https://evil.example/x");
    assert.ok(forbidden || walked || INERT[a], a + " is kept by DOMPurify, can carry a URL, and nothing deals with it");
    if (INERT[a]) assert.ok(!forbidden && !walked, a + " is in the inert list, but something deals with it: say which");
  }
});

test("audit of the vendored DOMPurify: every element it keeps that can fetch is forbidden or has its src and href walked", () => {
  const { tags } = purifyLists();
  const FETCHES = ["img", "video", "audio", "source", "picture", "track", "input", "iframe", "frame", "frameset", "object", "embed", "applet", "link",
    "script", "style", "image", "feimage", "use", "bgsound", "mglyph", "base", "meta", "form", "button", "area", "textpath", "mpath", "altglyph", "tref"];
  const kept = [...tags].map((t) => t.toLowerCase()).filter((t) => FETCHES.includes(t)).sort();
  // forbidden by MD_PURIFY; or walked: media (src must be local, a placeholder otherwise) or any other (src and href must be inert).
  const walked = (t) => mdUnsafeAttr(t, "src", "https://evil.example/x") && mdUnsafeAttr(t, "href", "https://evil.example/x") || mdUnsafeAttr(t, "src", "api/x");
  for (const t of kept) assert.ok(MD_PURIFY.FORBID_TAGS.includes(t) || walked(t), t + " is kept by DOMPurify, can fetch, and nothing deals with it");
  for (const t of ["image", "feimage", "mglyph", "img", "video", "audio", "source"]) assert.ok(kept.includes(t), t + " is among what DOMPurify keeps (the audit looked)");
  assert.ok(!kept.includes("use") && !kept.includes("script"), "use and script are not kept (DOMPurify's own svg and html lists)");
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
