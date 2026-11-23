"use strict";
// mdRenderFragment's chunked output (mdsafe.js, Files dock P1): a big markdown file is lexed in pieces and handed back as a run of
// fragments, each through the one sanitize-and-walk function (mdFinish), so no single step of the page's main thread takes long
// (measured on a 2 MiB file: marked.lexer alone was one 715 ms task). These tests run the REAL vendored marked (12.0.2): the pieces
// must lex to exactly the tokens of one marked.lexer over the whole text, and the groups must parse to exactly marked.parse's HTML.
// DOMPurify and the DOM are stand-ins (no DOM in node); that the chunked DOM equals the one-shot DOM in a browser is the Task 8 check.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { mdRenderFragment, mdLexPieces, mdChunkGroups, mdLexSteps } = require("../static/mdsafe.js");
const marked = require("../static/vendor/marked.min.js");

const ROOT = path.join(__dirname, "..", "..", "..");
const doc = (f) => fs.readFileSync(path.join(ROOT, f), "utf8");

// Deps whose sanitizer records the HTML it was given and returns an empty fragment (the walk then has nothing to do).
function deps(over) {
  const seen = [];
  return {
    seen,
    deps: Object.assign({
      marked,
      purify: { sanitize: (html) => { seen.push(html); return { html, querySelectorAll: () => [] }; } },
      hljs: null,
      rewrite: (x) => x,
    }, over),
  };
}

// Drain the chunked render: the HTML of every fragment it yielded, and how many steps it took.
function drain(text, chunkChars) {
  const d = deps({ chunkChars });
  const it = mdRenderFragment(text, d.deps);
  assert.equal(typeof it.next, "function", "chunkChars set: an iterator");
  let steps = 0;
  const frags = [];
  for (const f of it) { steps++; if (f) frags.push(f.html); }
  return { html: frags.join(""), frags, steps, sanitized: d.seen };
}

const oneShot = (text) => { marked.setOptions({ breaks: false, gfm: true }); return marked.parse(text); };
// Tokens without the lexer's object identity, for deepEqual.
const plain = (t) => JSON.parse(JSON.stringify(t));
function lexChunked(text, chunkChars) {
  marked.setOptions({ breaks: false, gfm: true });
  const it = mdLexSteps(text, marked, chunkChars);
  for (;;) { const r = it.next(); if (r.done) return r.value; }
}

const REFS = "# Top\n\nSee [the guide][g] and [x].\n\n## Middle\n\n1. one\n2. two\n\n## Lists\n\n- a\n\n- b\n\n# Defs\n\n[g]: https://example.com/guide \"Guide\"\n[x]: /x\n";
const FENCE = "# A\n\n```md\n# not a heading\n\n# still code\n```\n\n# B\n\n~~~\n\n# code too\n~~~~\n\n# C\n\ntext\n";
const TABLE = "# T\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n# U\n\n> quote\n>\n> more\n\n# V\n\n    indented code\n\n# W\n\nPara with *em*\nand a second line.\n";
// A fence inside an HTML block of type 7 (a custom tag alone on a line, which runs to the next blank line): mdLexPieces takes the
// ``` for a fence and cuts where marked sees code; the check after lexing the piece catches it (found in a real skill doc).
const HTMLFENCE = "# a\n\nIntro.\n\n<Good>\n```markdown\nx\n\ny\n```\n</Good>\n\n### Address\n\ntext\n\n```markdown\nz\n```\n\n## c\n\nend\n";
// An unclosed fence: everything after it is code.
const UNCLOSED = "# a\n\n```\ncode\n\n# b\n\nmore\n";
const CASES = { HTMLFENCE, UNCLOSED, "README.md": doc("README.md"), "docs/MACOS.md": doc("docs/MACOS.md"), "docs/CONFIG.md": doc("docs/CONFIG.md"), REFS, FENCE, TABLE, CRLF: REFS.replace(/\n/g, "\r\n") };

test("the pieces lex to exactly the tokens of one marked.lexer over the whole text", () => {
  for (const [name, text] of Object.entries(CASES)) {
    marked.setOptions({ breaks: false, gfm: true });
    const whole = plain(marked.lexer(text));
    for (const n of [1, 200, 1e9]) assert.deepEqual(plain(lexChunked(text, n)), whole, name + " at " + n);
  }
});

test("the chunks' HTML, joined, is marked.parse's HTML, and every chunk went through the sanitizer", () => {
  for (const [name, text] of Object.entries(CASES)) {
    const want = oneShot(text);
    for (const n of [1, 200, 1e9]) {
      const got = drain(text, n);
      assert.equal(got.html, want, name + " at " + n);
      assert.equal(got.sanitized.length, got.frags.length, name + ": one sanitize per chunk");
    }
  }
});

test("a reference defined at the end resolves in a chunk at the start", () => {
  const got = drain(REFS, 1);
  assert.ok(got.frags.length > 3, "split into several chunks");
  assert.match(got.frags[1], /<a href="https:\/\/example\.com\/guide" title="Guide">the guide<\/a>/);
});

test("mdLexPieces cuts only before a heading that follows a blank line, outside fences and HTML blocks that span blank lines", () => {
  assert.deepEqual(mdLexPieces("# a\n\ntext\n\n# b\n\nmore\n", 1), ["# a\n\ntext\n\n", "# b\n\nmore\n"]);
  assert.deepEqual(mdLexPieces("# a\ntext\n# b\n", 1), ["# a\ntext\n# b\n"], "no blank line before # b: no cut");
  assert.deepEqual(mdLexPieces("a\n\n#tag\n\n#\n", 1), ["a\n\n#tag\n\n", "#\n"], "#tag is no heading; a bare # is");
  assert.deepEqual(mdLexPieces("a\n\n ## b\n", 1), ["a\n\n ## b\n"], "an indented heading is not cut at (it may belong to the block above)");
  assert.deepEqual(mdLexPieces(FENCE, 1), ["# A\n\n```md\n# not a heading\n\n# still code\n```\n\n", "# B\n\n~~~\n\n# code too\n~~~~\n\n", "# C\n\ntext\n"]);
  for (const open of ["<pre>", "<script>", "<style>", "<textarea>", "<!--", "<?php", "<!DOCTYPE x", "<![CDATA["]) {
    const close = { "<pre>": "</pre>", "<script>": "</script>", "<style>": "</style>", "<textarea>": "</textarea>", "<!--": "-->", "<?php": "?>", "<!DOCTYPE x": ">", "<![CDATA[": "]]>" }[open];
    const t = "# a\n\n" + open + "\n\n# inside\n\nstill " + close + "\n\n# b\n";
    assert.deepEqual(mdLexPieces(t, 1), ["# a\n\n" + open + "\n\n# inside\n\nstill " + close + "\n\n", "# b\n"], open);
  }
  assert.deepEqual(mdLexPieces("# a\n\nx\n\n# b\n\ny\n\n# c\n", 9), ["# a\n\nx\n\n# b\n\ny\n\n", "# c\n"], "pieces grow to chunkChars");
  assert.deepEqual(mdLexPieces("no headings\n\nat all\n", 1), ["no headings\n\nat all\n"]);
});

test("mdChunkGroups never closes a group while an HTML block is open, so <details> spanning paragraphs nests as in one shot", () => {
  const text = "# a\n\n<details>\n\n# inside\n\ntext\n\n</details>\n\n# b\n\nend\n";
  marked.setOptions({ breaks: false, gfm: true });
  const tokens = marked.lexer(text);
  const groups = mdChunkGroups(tokens, 1);
  const where = groups.findIndex((g) => g.some((t) => t.type === "html" && /<details>/.test(t.raw)));
  assert.ok(groups[where].some((t) => t.type === "html" && /<\/details>/.test(t.raw)), "open and close in the same group");
  assert.ok(groups.length >= 3, "still split around it");
  for (const g of groups) assert.equal(g.links, tokens.links, "every group carries the reference links");
  assert.equal(drain(text, 1).html, oneShot(text));
  // Void, self-closing, comments and declarations do not open anything.
  const flat = "# a\n\n<br>\n\n# b\n\n<img src=x />\n\n# c\n\n<!-- c -->\n\n# d\n\n<hr>\n\n# e\n";
  assert.equal(mdChunkGroups(marked.lexer(flat), 1).length, marked.lexer(flat).length, "every token is its own group");
  // A formatting tag left open in one paragraph (the parser reopens it in the next) and closed in a later one keeps them together too.
  const inline = "# a\n\nsome <em>open\n\nstill</em> closed\n\n# b\n";
  const ig = mdChunkGroups(marked.lexer(inline), 1);
  const sp = ig.find((g) => g.some((t) => t.raw && t.raw.includes("<em>")));
  assert.ok(sp.some((t) => t.raw && t.raw.includes("</em>")));
});

test("without chunkChars the render is one fragment, exactly as before; with it, the first steps lex and the rest hand back fragments", () => {
  const d = deps();
  const f = mdRenderFragment("# a\n\nb\n", d.deps);
  assert.equal(typeof f.next, "undefined");
  assert.equal(f.html, oneShot("# a\n\nb\n"));
  const got = drain(REFS, 1);
  assert.ok(got.steps > got.frags.length, "some steps only lex (they yield null)");
});

test("a big generated file is cut into many pieces and groups of about chunkChars", () => {
  let big = "";
  for (let i = 0; big.length < 400000; i++) big += "## Section " + i + "\n\nSome text with a [link](https://example.com/" + i + ").\n\n```go\nfunc f() {}\n```\n\n| a |\n|---|\n| " + i + " |\n\n";
  const pieces = mdLexPieces(big, 16384);
  assert.ok(pieces.length >= 20 && pieces.length <= 26, "pieces: " + pieces.length);
  assert.equal(pieces.join(""), big);
  const got = drain(big, 16384);
  assert.ok(got.frags.length >= 20 && got.frags.length <= 26, "chunks: " + got.frags.length);
  assert.equal(got.html, oneShot(big));
});

test("a cut that falls inside a block mdLexPieces could not see is caught after lexing, and the rest is lexed whole", () => {
  assert.ok(mdLexPieces(HTMLFENCE, 1).length > 2, "the scanner does cut inside what marked reads as code");
  marked.setOptions({ breaks: false, gfm: true });
  assert.deepEqual(plain(lexChunked(HTMLFENCE, 1)), plain(marked.lexer(HTMLFENCE)));
  assert.equal(drain(HTMLFENCE, 1).html, oneShot(HTMLFENCE));
});

// --- review round 1 ---------------------------------------------------------------------------------------------------------

test("a fence of 200,000 blank lines before a cut takes linear time, not quadratic (a hostile file must not freeze the page)", () => {
  const text = "x".repeat(9000) + "\n\n```\n" + "\n".repeat(200000) + "y\n```\n\n# h\n\nend\n";
  const t0 = Date.now();
  const got = drain(text, 8192);
  const ms = Date.now() - t0;
  assert.ok(ms < 3000, "took " + ms + " ms");
  assert.equal(got.html, oneShot(text));
});

test("prose with a generic type (Vec<T>) or an unclosed span does not hold the rest of the file in one chunk", () => {
  let body = "";
  for (let i = 0; body.length < 200000; i++) body += "## S" + i + "\n\nSome text " + i + " here.\n\n";
  // (An unknown tag alone on its line, an HTML block, is kept together on purpose: the round-3 test below.)
  for (const head of ["Use a Vec<T> here.\n\n", "An <span>open span.\n\n", "Option<String> and HashMap<K, V>.\n\n"]) {
    const text = "# Top\n\n" + head + body;
    const got = drain(text, 8192);
    assert.ok(got.frags.length > 15, head.trim() + ": " + got.frags.length + " chunks");
    assert.equal(got.html, oneShot(text), head.trim() + ": the same HTML");
  }
});

test("an element that really nests across blocks (<details>, an unclosed <b>) is kept together, up to 8 chunks' worth", () => {
  marked.setOptions({ breaks: false, gfm: true });
  const b = "# a\n\nsome <b>bold\n\nstill bold</b> here\n\n# b\n";
  const gb = mdChunkGroups(marked.lexer(b), 1);
  assert.ok(gb.some((g) => g.some((t) => t.raw.includes("<b>")) && g.some((t) => t.raw.includes("</b>"))), "<b> stays with its close");
  // Formatting left open for good: a group is closed anyway once it holds 8 × chunkChars (64 KB at the least).
  let rest = "";
  for (let i = 0; rest.length < 600000; i++) rest += "## S" + i + "\n\ntext\n\n";
  const open = "# a\n\nsome <b>bold\n\n" + rest; // formatting left open for good (a container is the next test's)
  const groups = mdChunkGroups(marked.lexer(open), 8192);
  assert.ok(groups.length >= 9, "groups: " + groups.length);
  for (const g of groups) assert.ok(g.reduce((n, t) => n + t.raw.length, 0) < 8 * 8192 + 200, "no group holds much more than 8 chunks");
});

// --- review round 2 ---------------------------------------------------------------------------------------------------------

test("a <details> of 100 KB (a collapsed changelog) is never cut: only inline formatting left open is force-cut at 64 KB", () => {
  let rel = "";
  for (let i = 0; rel.length < 100000; i++) rel += "## v1." + i + "\n\n- fix\n\n";
  const text = "# Changelog\n\n<details>\n<summary>Older releases</summary>\n\n" + rel + "</details>\n\n## After\n\ntext\n";
  marked.setOptions({ breaks: false, gfm: true });
  const groups = mdChunkGroups(marked.lexer(text), 8192);
  const g = groups.find((x) => x.some((t) => t.type === "html" && t.raw.includes("<details>")));
  assert.ok(g.some((t) => t.type === "html" && t.raw.includes("</details>")), "open and close in one group");
  // An inline </div> closes a block <div>.
  const inl = "# a\n\n<div>\n\n" + rel + "text</div> after\n\n" + rel;
  const gi = mdChunkGroups(marked.lexer(inl), 8192);
  assert.ok(gi.length > 5, "after the inline </div> the file is cut again: " + gi.length);
  // A <div> left open for good is cut at 1 MiB, not 64 KB.
  let big = "";
  for (let i = 0; big.length < 300000; i++) big += "## S" + i + "\n\ntext\n\n";
  const gd = mdChunkGroups(marked.lexer("# a\n\n<div>\n\n" + big), 8192);
  assert.equal(gd.length, 1, "kept whole below 1 MiB");
  const gb = mdChunkGroups(marked.lexer("# a\n\nsome <b>bold\n\n" + big), 8192);
  assert.ok(gb.length >= 4, "an inline <b> left open is cut at 64 KB: " + gb.length);
});

// --- review round 3: keep together whenever unsure ---------------------------------------------------------------------------

test("any element an HTML block leaves open (span, del, h1, an unknown tag) keeps its paragraphs together", () => {
  marked.setOptions({ breaks: false, gfm: true });
  const paras = (n, w) => { let s = ""; for (let i = 0; i < n; i++) s += "Paragraph " + i + " " + "word ".repeat(w) + "\n\n"; return s; };
  for (const open of ['<span style="color:red">', "<del>", '<h1 align="center">', "<foo-bar>", "<sup>"]) {
    const name = /<([a-z0-9-]+)/.exec(open)[1];
    const text = paras(10, 200) + open + "\n\n" + paras(20, 200) + "</" + name + ">\n\n" + paras(30, 200);
    const groups = mdChunkGroups(marked.lexer(text), 8192);
    const g = groups.find((x) => x.some((t) => t.type === "html" && t.raw.startsWith(open)));
    assert.ok(g.some((t) => t.type === "html" && t.raw.includes("</" + name + ">")), open + ": open and close in one group");
    assert.ok(groups.length >= 3, open + ": still cut before and after: " + groups.length);
  }
});

test("optional end tags and void elements hold nothing open; an unrelated close never cancels an open container", () => {
  marked.setOptions({ breaks: false, gfm: true });
  let big = "";
  for (let i = 0; big.length < 200000; i++) big += "## S" + i + "\n\ntext\n\n";
  const tbody = "<table><tbody>\n<tr><td>x</td></tr>\n</table>\n\n" + big;
  assert.ok(mdChunkGroups(marked.lexer(tbody), 8192).length > 10, "tbody/tr/td are closed by the parser");
  const voids = "<p>\n<img src=x>\n<br>\n<hr>\n\n" + big;
  assert.ok(mdChunkGroups(marked.lexer(voids), 8192).length > 10, "p, img, br, hr open nothing");
  // A <div> around 30 KB holding paragraphs with balanced and unbalanced inline spans: the stray </span> is not the div's close.
  let inner = "";
  for (let i = 0; inner.length < 30000; i++) inner += "Para " + i + " with <span>a span</span> and a stray </span>.\n\n";
  const div = "# a\n\n<div>\n\n" + inner + "</div>\n\n" + big;
  const groups = mdChunkGroups(marked.lexer(div), 8192);
  const g = groups.find((x) => x.some((t) => t.type === "html" && t.raw.startsWith("<div>")));
  assert.ok(g.some((t) => t.type === "html" && t.raw.includes("</div>")), "the div stays whole");
  assert.ok(groups.length > 10, "and the file is cut after it");
});
