"use strict";
// mdRenderFragment and mdWalkFragment (mdsafe.js): what the preview does to the fragment DOMPurify hands back, in the dock and on
// the phone. The repo has no real DOM in node (no jsdom; fakedom.js is not a parser), so DOMPurify itself and the browser's loading
// of an image are NOT reached here: the fragments below are built by hand as what DOMPurify could leave (a stand-in sanitizer
// returns them), and the walk is run on them for real. That DOMPurify and marked, the real vendored ones, leave nothing that loads
// or runs in the hostile file is the browser check's (the task 9 report: the hostile cases with the network log); this file pins
// what the walk does with each thing that could get past, including things a DOMPurify upgrade might one day allow.
const test = require("node:test");
const assert = require("node:assert");
const { mdWalkFragment, mdRenderFragment, MD_PURIFY } = require("../static/mdsafe.js");

// The few parts of the DOM the walk touches, as a tree of elements: attributes in order, children, and the node operations the
// walk uses (replaceWith, before, remove, append of strings and nodes, contains, querySelectorAll("*") in document order).
class Node {
  constructor(localName, doc) {
    this.localName = localName;
    this.ownerDocument = doc;
    this.attributes = [];
    this.children = [];
    this.parentNode = null;
    this.text = "";
  }
  getAttribute(n) { const a = this.attributes.find((x) => x.name === n); return a ? a.value : null; }
  hasAttribute(n) { return this.getAttribute(n) !== null; }
  setAttribute(n, v) { const a = this.attributes.find((x) => x.name === n); if (a) a.value = String(v); else this.attributes.push({ name: n, value: String(v) }); }
  removeAttribute(n) { this.attributes = this.attributes.filter((x) => x.name !== n); }
  get textContent() { return this.text + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parentNode = null; }); this.children = []; this.text = String(v); }
  appendChild(c) { if (c.parentNode) c.remove(); c.parentNode = this; this.children.push(c); return c; }
  append(...cs) { for (const c of cs) this.appendChild(typeof c === "string" ? Object.assign(new Node("#text", this.ownerDocument), { text: c }) : c); }
  remove() { if (this.parentNode) { this.parentNode.children.splice(this.parentNode.children.indexOf(this), 1); this.parentNode = null; } }
  replaceWith(n) { const p = this.parentNode; const i = p.children.indexOf(this); this.remove(); n.remove(); n.parentNode = p; p.children.splice(i, 0, n); }
  before(n) { const p = this.parentNode; const i = p.children.indexOf(this); n.remove(); n.parentNode = p; p.children.splice(i, 0, n); }
  contains(n) { for (let x = n; x; x = x.parentNode) if (x === this) return true; return false; }
  querySelectorAll(sel) {
    assert.equal(sel, sel === "*" ? "*" : "pre code", "only the two selectors the walk and the render use");
    const out = [];
    const visit = (n) => { for (const c of n.children) { if (c.localName !== "#text" && (sel === "*" || (c.localName === "code" && n.localName === "pre"))) out.push(c); visit(c); } };
    visit(this);
    return out;
  }
  // The element as a string, to compare trees: <tag attr="v">children</tag>, text as itself.
  get html() {
    if (this.localName === "#text") return this.text;
    if (this.localName === "#frag") return this.children.map((c) => c.html).join("");
    const attrs = this.attributes.map((a) => " " + a.name + '="' + a.value + '"').join("");
    return "<" + this.localName + attrs + ">" + this.text + this.children.map((c) => c.html).join("") + "</" + this.localName + ">";
  }
}
const doc = { createElement: (t) => new Node(t, doc) };

// h("img", {src: "x"}, child, ...) builds an element; a string child is text.
function h(tag, attrs, ...kids) {
  const el = new Node(tag, doc);
  for (const [k, v] of Object.entries(attrs || {})) el.setAttribute(k, v);
  el.append(...kids);
  return el;
}
const frag = (...kids) => { const f = new Node("#frag", doc); f.append(...kids); return f; };

const REWRITE = (rel) => "/api/fs/read?path=" + encodeURIComponent("/work/docs/" + rel);
const walk = (...kids) => { const f = frag(...kids); mdWalkFragment(f, REWRITE); return f; };
const find = (root, tag) => root.querySelectorAll("*").filter((e) => e.localName === tag);

test("a remote image becomes a notice that links to it, and no <img> is left", () => {
  const f = walk(h("p", {}, h("img", { src: "https://example.com/p.png", alt: "t" })));
  assert.equal(find(f, "img").length, 0);
  const [span] = find(f, "span");
  assert.equal(span.className, "md-blocked");
  assert.equal(span.title, "https://example.com/p.png");
  assert.equal(span.children[0].text, "remote image blocked");
  const [a] = find(f, "a");
  assert.equal(a.getAttribute("href"), "https://example.com/p.png");
  assert.equal(a.getAttribute("target"), "_blank");
  assert.equal(a.getAttribute("rel"), "noopener noreferrer");
  assert.equal(a.textContent, "https://example.com/p.png");
  assert.equal(span.parentNode.localName, "p", "in the place the image was");
});

test("the notice's link text is cut for a long address, and the title keeps all of it", () => {
  const url = "https://example.com/" + "a".repeat(100) + ".png";
  const f = walk(h("img", { src: url }));
  const [a] = find(f, "a");
  assert.equal(a.textContent.length, 60);
  assert.ok(a.textContent.endsWith("…"));
  assert.equal(find(f, "span")[0].title, url);
});

test("only an http(s) address gets a link; any other thing the image asked for is named and not offered", () => {
  for (const [src, label] of [
    ["//example.com/p.png", "remote image blocked"],
    ["javascript:alert(1)", "remote image blocked"],
    ["java\nscript:alert(1)", "remote image blocked"],
    ["data:text/html,<script>alert(1)</script>", "remote image blocked"],
    ["data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=", "remote image blocked"],
    ["file:///etc/passwd", "remote image blocked"],
    ["/images/a.png", "image blocked"],
    ["", "image blocked"],
  ]) {
    const f = walk(h("img", { src }));
    assert.equal(find(f, "img").length, 0, JSON.stringify(src));
    assert.equal(find(f, "a").length, 0, "no link for " + JSON.stringify(src));
    assert.equal(find(f, "span")[0].children[0].text, label, JSON.stringify(src));
  }
});

test("an <img> with no src (DOMPurify took its srcset) is a notice too, with nothing to link to", () => {
  const f = walk(h("img", { alt: "x" }));
  assert.equal(find(f, "img").length, 0);
  assert.equal(find(f, "span")[0].children[0].text, "image blocked");
  assert.ok(!find(f, "span")[0].title, "no title: there is no address to name");
  assert.equal(find(f, "a").length, 0);
});

test("a relative src is rewritten to the read route before it is checked, and stays an image", () => {
  const f = walk(h("img", { src: "img/a b.png", alt: "t" }));
  const [img] = find(f, "img");
  assert.equal(img.getAttribute("src"), REWRITE("img/a b.png"));
  assert.equal(img.getAttribute("alt"), "t");
  assert.equal(find(f, "span").length, 0);
  const g = walk(h("video", { src: "clip.mp4" }), h("audio", { src: "../a.mp3" }));
  assert.deepEqual(find(g, "video").map((v) => v.getAttribute("src")), [REWRITE("clip.mp4")]);
  assert.deepEqual(find(g, "audio").map((v) => v.getAttribute("src")), [REWRITE("../a.mp3")]);
});

test("a src that is already local is left as it is: the read routes and a small inline image", () => {
  const same = ["/api/fs/read?path=%2Fa.png", "/api/sessions/s1/boxfile?path=%2Fa.png", "data:image/png;base64,iVBORw0KGgo="];
  for (const src of same) {
    const f = walk(h("img", { src }));
    assert.equal(find(f, "img")[0].getAttribute("src"), src);
  }
});

test("a scheme or a slash hidden by whitespace or control characters is not a relative path to rewrite", () => {
  for (const src of ["java\tscript:alert(1)", "\u0001javascript:alert(1)", "/\t/example.com/p.png", "  https://example.com/p.png", "\\\\example.com\\p.png"]) {
    const f = walk(h("img", { src }));
    assert.equal(find(f, "img").length, 0, JSON.stringify(src));
    assert.equal(find(f, "span")[0].className, "md-blocked");
  }
});

test("video and audio with a remote src become a notice; a failing <source> goes and the notice goes before its video", () => {
  const f = walk(
    h("video", { src: "https://example.com/v.mp4", controls: "" }),
    h("audio", { src: "http://example.com/a.mp3" }),
    h("video", {}, h("source", { src: "https://example.com/v.webm" }), h("source", { src: "clip.mp4" })),
  );
  assert.equal(find(f, "video").length, 1, "the two with a remote src of their own are gone");
  assert.equal(find(f, "audio").length, 0);
  assert.deepEqual(find(f, "span").map((s) => s.children[0].text), ["remote video blocked", "remote audio blocked", "remote media blocked"]);
  const [video] = find(f, "video");
  assert.deepEqual(find(video, "source").map((s) => s.getAttribute("src")), [REWRITE("clip.mp4")], "the local source stays, rewritten");
  assert.equal(f.children.at(-2).className, "md-blocked", "the notice stands just before the video");
  assert.equal(f.children.at(-1), video);
});

test("a video or audio with no src of its own is kept: its sources are what carry the address", () => {
  const f = walk(h("video", { controls: "" }, h("source", { src: "clip.mp4" })));
  assert.equal(find(f, "video").length, 1);
  assert.equal(find(f, "span").length, 0);
});

test("any other element loses a src, href or xlink:href that is not local, whatever its name (SVG image and feImage, MathML, area)", () => {
  const f = walk(
    h("svg", {}, h("image", { href: "https://example.com/i.png", width: "10" }), h("feImage", { "xlink:href": "https://example.com/f.png" }),
      h("image", { href: "img/a.png" })),
    h("math", { href: "https://example.com/m" }),
    h("map", {}, h("area", { href: "https://example.com/area", shape: "rect" })),
    h("iframe", { src: "https://example.com/" }), // not one DOMPurify leaves; the walk does not rely on that
  );
  const images = find(f, "image");
  assert.equal(images[0].getAttribute("href"), null);
  assert.equal(images[0].getAttribute("width"), "10", "the element and its other attributes stay");
  assert.equal(find(f, "feImage")[0].getAttribute("xlink:href"), null);
  assert.equal(images[1].getAttribute("href"), "img/a.png", "a relative one is local (the walk rewrites src on media only)");
  assert.equal(find(f, "math")[0].getAttribute("href"), null);
  assert.equal(find(f, "area")[0].getAttribute("href"), null);
  assert.equal(find(f, "area")[0].getAttribute("shape"), "rect");
  assert.equal(find(f, "iframe")[0].getAttribute("src"), null);
});

test("an attribute that carries a CSS url() to something not local is removed; a reference into the same document is kept", () => {
  const f = walk(h("svg", {},
    h("rect", { filter: "url(https://example.com/f.svg#f)", id: "r1" }),
    h("rect", { fill: "url(#grad)", stroke: 'url("#s")' }),
    h("rect", { "clip-path": "url('https://example.com/c.svg#c')" }),
    h("rect", { mask: "URL( //example.com/m.svg#m )" }),
    h("path", { "marker-start": "url(https://example.com/m.svg#a)", "marker-end": "url(#b)", d: "M0 0L1 1" }),
  ));
  const [r1, r2, r3, r4] = find(f, "rect");
  assert.equal(r1.getAttribute("filter"), null);
  assert.equal(r1.getAttribute("id"), "r1");
  assert.equal(r2.getAttribute("fill"), "url(#grad)");
  assert.equal(r2.getAttribute("stroke"), 'url("#s")');
  assert.equal(r3.getAttribute("clip-path"), null);
  assert.equal(r4.getAttribute("mask"), null);
  const [path] = find(f, "path");
  assert.equal(path.getAttribute("marker-start"), null);
  assert.equal(path.getAttribute("marker-end"), "url(#b)");
  assert.equal(path.getAttribute("d"), "M0 0L1 1", "a value with no function is not looked at");
});

test("CSS escapes cannot hide the function: u\\72l( is url(", () => {
  const f = walk(h("svg", {}, h("rect", { filter: "u\\72l(https://example.com/f.svg#f)" }), h("rect", { mask: "\\75 rl(https://example.com/m)" }),
    h("rect", { fill: "url(https\\3a //example.com/p)" }), h("rect", { filter: "url(\\23 ok)" })));
  const [a, b, c, d] = find(f, "rect");
  assert.equal(a.getAttribute("filter"), null);
  assert.equal(b.getAttribute("mask"), null);
  assert.equal(c.getAttribute("fill"), null);
  assert.equal(d.getAttribute("filter"), "url(\\23 ok)", "an escape that makes a same-document reference is still one");
});

test("the CSS functions that take an image by address other than url() go too", () => {
  const f = walk(h("svg", {}, h("rect", { mask: "image-set('https://example.com/x.png' 1x)" }), h("rect", { mask: "-webkit-image-set(url(#a) 1x)" }),
    h("rect", { fill: "src('https://example.com/x')" }), h("rect", { transform: "rotate(45)", opacity: "0.5" })));
  const [a, b, c, d] = find(f, "rect");
  assert.equal(a.getAttribute("mask"), null);
  assert.equal(b.getAttribute("mask"), null);
  assert.equal(c.getAttribute("fill"), null);
  assert.equal(d.getAttribute("transform"), "rotate(45)", "a function that fetches nothing stays");
});

test("a link stays a link and opens in a new tab with no opener; an <a> with no href is left alone", () => {
  const f = walk(
    h("a", { href: "https://example.com/doc" }, "x"),
    h("a", { href: "#top" }, "y"),
    h("a", { href: "other.md" }, "z"),
    h("a", { name: "n" }, "w"),
    h("svg", {}, h("a", { "xlink:href": "https://example.com/s" }, h("text", {}, "t"))),
  );
  const [a1, a2, a3, a4, a5] = find(f, "a");
  for (const a of [a1, a2, a3]) {
    assert.equal(a.getAttribute("target"), "_blank", a.getAttribute("href"));
    assert.equal(a.getAttribute("rel"), "noopener noreferrer");
  }
  assert.equal(a1.getAttribute("href"), "https://example.com/doc", "a link is a click, not a load: its remote address stays");
  assert.equal(a4.getAttribute("target"), null);
  assert.equal(a5.getAttribute("xlink:href"), "https://example.com/s");
  assert.equal(a5.getAttribute("target"), "_blank");
});

test("a link that would run script has no href left, and so no target", () => {
  const f = walk(h("a", { href: "javascript:alert(1)" }, "x"), h("a", { href: " JaVa\nScRiPt:alert(1)" }, "y"), h("a", { href: "vbscript:x" }, "v"),
    h("a", { href: "data:text/html,<script>alert(1)</script>" }, "d"), h("svg", {}, h("a", { "xlink:href": "javascript:alert(1)" })));
  for (const a of find(f, "a")) {
    assert.equal(a.getAttribute("href"), null);
    assert.equal(a.getAttribute("xlink:href"), null);
    assert.equal(a.getAttribute("target"), null);
  }
  assert.equal(find(f, "a")[0].textContent, "x", "the text stays");
});

test("a link that wraps a blocked image keeps its own address, with the notice and its link inside", () => {
  const f = walk(h("a", { href: "https://example.com/build" }, h("img", { src: "https://img.example.com/badge.svg" })));
  const [outer, inner] = find(f, "a");
  assert.equal(outer.getAttribute("href"), "https://example.com/build");
  assert.equal(outer.getAttribute("target"), "_blank");
  assert.equal(inner.getAttribute("href"), "https://img.example.com/badge.svg");
  assert.equal(inner.getAttribute("rel"), "noopener noreferrer");
});

test("a <template> goes whole: its content is a second tree no query reaches", () => {
  const f = walk(h("p", {}, "kept"), h("template", {}, h("img", { src: "https://example.com/p.png" })));
  assert.equal(find(f, "template").length, 0);
  assert.equal(f.html, "<p>kept</p>");
});

test("nothing inside what was replaced is visited again, and a tree with nothing to do is untouched", () => {
  const clean = frag(h("h1", { id: "a" }, "T"), h("p", {}, h("a", { href: "x.md", target: "_blank", rel: "noopener noreferrer" }, "l")),
    h("pre", {}, h("code", { class: "hljs" }, "x")), h("table", {}, h("tr", {}, h("td", { colspan: "2" }, "c"))));
  const before = clean.html;
  mdWalkFragment(clean, REWRITE);
  assert.equal(clean.html, before);
  const f = walk(h("video", { src: "https://example.com/v.mp4" }, h("source", { src: "https://example.com/v.webm" })));
  assert.deepEqual(f.children.map((c) => c.className), ["md-blocked"], "one notice for the video, none for the source inside it");
});

// --- mdRenderFragment ----------------------------------------------------------------------------------------------

// The collaborators the page passes: marked, DOMPurify and highlight.js, as spies. `out` is what the stand-in sanitizer returns.
function deps(out, over) {
  const calls = { marked: [], parse: [], sanitize: [], hljs: [] };
  return {
    calls,
    deps: Object.assign({
      marked: { setOptions: (o) => calls.marked.push(o), parse: (t) => { calls.parse.push(t); return "<html:" + t + ">"; } },
      purify: { sanitize: (html, cfg) => { calls.sanitize.push([html, cfg]); return out; } },
      hljs: { highlightElement: (el) => calls.hljs.push(el) },
      rewrite: REWRITE,
    }, over),
  };
}

test("mdRenderFragment: marked, then DOMPurify with MD_PURIFY as a fragment, then the code highlighted, then the walk", () => {
  const code = h("code", {}, "x := 1");
  const out = frag(h("pre", {}, code), h("img", { src: "https://example.com/p.png" }), h("img", { src: "n.png" }));
  const d = deps(out);
  const got = mdRenderFragment("# md", d.deps);
  assert.equal(got, out, "the fragment DOMPurify returned, walked in place");
  assert.deepEqual(d.calls.marked, [{ breaks: false, gfm: true }]);
  assert.deepEqual(d.calls.parse, ["# md"]);
  assert.equal(d.calls.sanitize.length, 1, "one sanitize call");
  const [html, cfg] = d.calls.sanitize[0];
  assert.equal(html, "<html:# md>", "what marked made is what is sanitized");
  assert.deepEqual(cfg, { FORBID_TAGS: [...MD_PURIFY.FORBID_TAGS], FORBID_ATTR: [...MD_PURIFY.FORBID_ATTR], RETURN_DOM_FRAGMENT: true });
  assert.equal(Object.isFrozen(cfg), false, "a copy: MD_PURIFY itself is not touched");
  assert.equal(Object.isFrozen(MD_PURIFY), true);
  assert.equal("RETURN_DOM_FRAGMENT" in MD_PURIFY, false);
  assert.deepEqual(d.calls.hljs, [code]);
  assert.deepEqual(find(got, "img").map((i) => i.getAttribute("src")), [REWRITE("n.png")]);
  assert.equal(find(got, "span").length, 1, "the remote image is a notice");
});

test("mdRenderFragment: a highlighter that throws on a block does not stop the render, and no highlighter is fine", () => {
  const out = frag(h("pre", {}, h("code", {}, "a")), h("pre", {}, h("code", {}, "b")));
  const seen = [];
  const d = deps(out, { hljs: { highlightElement: (el) => { seen.push(el.textContent); throw new Error("boom"); } } });
  assert.equal(mdRenderFragment("x", d.deps), out);
  assert.deepEqual(seen, ["a", "b"]);
  assert.equal(mdRenderFragment("x", deps(frag(), { hljs: null }).deps).children.length, 0);
});

test("mdRenderFragment: markup that cannot be made throws, so the caller can show the file as text", () => {
  assert.throws(() => mdRenderFragment("x", deps(frag(), { marked: { setOptions() {}, parse: () => Promise.resolve("<p>") } }).deps), /did not render to a string/);
  assert.throws(() => mdRenderFragment("x", deps("<p>already a string</p>").deps), /no fragment/);
  assert.throws(() => mdRenderFragment("x", deps(null).deps), /no fragment/);
  const boom = deps(frag(), { purify: { sanitize: () => { throw new Error("DOMPurify says no"); } } });
  assert.throws(() => mdRenderFragment("x", boom.deps), /DOMPurify says no/);
});
