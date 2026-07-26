"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { scanLinks } = require("../static/links.js");

const kinds = (s) => scanLinks(s).map((t) => t.kind + ":" + t.text);

test("a URL yields one url token and no path token inside it", () => {
  assert.deepStrictEqual(
    kinds("see https://culiops.dev/learn/x.html now"),
    ["url:https://culiops.dev/learn/x.html"],
  );
});

test("a hyphenated host and path survive intact", () => {
  assert.deepStrictEqual(
    kinds("https://my-site.dev/a-b/c-d.html"),
    ["url:https://my-site.dev/a-b/c-d.html"],
  );
});

test("trailing sentence punctuation is not part of the URL", () => {
  assert.deepStrictEqual(kinds("go to https://culiops.dev/a."), ["url:https://culiops.dev/a"]);
  assert.deepStrictEqual(kinds("(see https://culiops.dev/a)."), ["url:https://culiops.dev/a"]);
});

test("a closing paren that belongs to the URL is kept", () => {
  assert.deepStrictEqual(
    kinds("https://en.wikipedia.org/wiki/Foo_(bar)"),
    ["url:https://en.wikipedia.org/wiki/Foo_(bar)"],
  );
});

// Asserts the absence of a URL token, not the absence of any token: PATH_RE is
// deliberately unchanged, so `ftp://x.dev/a.txt` still yields the PATH token
// `//x.dev/a.txt`. That is pre-existing behaviour and it is contained server-side
// by containedPath() in fsapi.go, which refuses anything outside $HOME.
test("no non-http scheme is ever tokenized as a url", () => {
  for (const s of [
    "javascript:alert(1)",
    "javascript:alert(1)//x.dev/a.html",
    "data:text/html,<b>x</b>",
    "file:///etc/passwd",
    "file:///etc/passwd.bak",
    "ftp://x.dev/a.txt",
    "hxxps://x.dev/a.html",
  ]) {
    assert.ok(scanLinks(s).every((t) => t.kind !== "url"), "no url token for " + s);
  }
});

test("file paths still tokenize exactly as before", () => {
  assert.deepStrictEqual(kinds("edited /home/claude/a.go"), ["path:/home/claude/a.go"]);
  assert.deepStrictEqual(kinds("open ~/notes/x.md"), ["path:~/notes/x.md"]);
  assert.deepStrictEqual(kinds("see ./rel.txt"), ["path:./rel.txt"]);
  assert.deepStrictEqual(kinds("in internal/web/api.go"), ["path:internal/web/api.go"]);
});

test("a mixed line resolves every token, in order", () => {
  assert.deepStrictEqual(
    kinds("./a.go and https://culiops.dev/b.html and ~/c.md"),
    ["path:./a.go", "url:https://culiops.dev/b.html", "path:~/c.md"],
  );
});

test("tokens never overlap and are sorted by start", () => {
  const toks = scanLinks("./a.go and https://culiops.dev/b.html and ~/c.md");
  for (let i = 1; i < toks.length; i++) {
    assert.ok(toks[i - 1].start < toks[i].start, "sorted");
    assert.ok(toks[i - 1].end <= toks[i].start, "non-overlapping");
  }
});

const { rewriteLocalhost } = require("../static/links.js");

test("localhost forms are rewritten to the host the console is viewed on", () => {
  for (const h of ["localhost", "127.0.0.1", "0.0.0.0"]) {
    assert.strictEqual(
      rewriteLocalhost("http://" + h + ":3030/deck?x=1#s", "100.76.118.121"),
      "http://100.76.118.121:3030/deck?x=1#s",
    );
  }
});

test("ipv6 loopback is rewritten too", () => {
  assert.strictEqual(
    rewriteLocalhost("http://[::1]:9000/a", "100.76.118.121"),
    "http://100.76.118.121:9000/a",
  );
});

test("a non-local host is returned byte-identical", () => {
  const u = "https://culiops.dev/a?b=1#c";
  assert.strictEqual(rewriteLocalhost(u, "100.76.118.121"), u);
});

test("no rewrite when the console itself is viewed at localhost", () => {
  const u = "http://localhost:3030/deck";
  assert.strictEqual(rewriteLocalhost(u, "localhost"), u);
});

test("unparseable input is returned unchanged", () => {
  assert.strictEqual(rewriteLocalhost("http://", "100.76.118.121"), "http://");
  assert.strictEqual(rewriteLocalhost("not a url", "100.76.118.121"), "not a url");
});

// Documents URL.toString() normalisation on the rewrite path. It only ever fires
// when a rewrite actually happened (a non-local host returns early, byte-identical),
// so it cannot cause a spurious "opened X instead of localhost" toast.
test("rewriting also normalises, and that is accepted", () => {
  assert.strictEqual(rewriteLocalhost("http://localhost:3030", "1.2.3.4"), "http://1.2.3.4:3030/");
  assert.strictEqual(rewriteLocalhost("http://localhost:80/a", "1.2.3.4"), "http://1.2.3.4/a");
});

const { readLogicalLine, cellFromIndex, linksForRow } = require("../static/links.js");

// fakeTerm models xterm's buffer. `wrapped` marks a row as the continuation of the
// row above it. Rows are padded to `cols` EXCEPT where a test deliberately returns
// a shorter string, which is what a double-width character really does.
function fakeTerm(rows, cols) {
  return {
    cols,
    buffer: {
      active: {
        getLine(i) {
          const r = rows[i];
          if (!r) return undefined;
          return {
            isWrapped: !!r.wrapped,
            translateToString: () => (r.exact ? r.text : r.text.padEnd(cols, " ").slice(0, cols)),
          };
        },
      },
    },
  };
}

test("an unwrapped row is returned on its own", () => {
  const t = fakeTerm([{ text: "hello" }, { text: "world" }], 9);
  const got = readLogicalLine(t, 2);
  assert.strictEqual(got.startY, 2);
  assert.strictEqual(got.text.trimEnd(), "world");
});

test("wrapped rows join into one line, asked from either row", () => {
  const t = fakeTerm([{ text: "https://a" }, { text: ".dev/x.md", wrapped: true }], 9);
  for (const y of [1, 2]) {
    const got = readLogicalLine(t, y);
    assert.strictEqual(got.startY, 1);
    assert.strictEqual(got.text, "https://a.dev/x.md");
    assert.deepStrictEqual(got.rowStarts, [0, 9]);
  }
});

// Hazard 2: buffer.getLine is a CircularList whose get() is
// _array[(_startIndex + i) % _maxLength], so getLine(-1) returns a REAL but
// unrelated evicted row. Row 1 is routinely marked isWrapped once the ring wraps.
test("the upward walk stops at row 1 and never reads a negative index", () => {
  const asked = [];
  const t = fakeTerm([{ text: "aaaaaaaaa", wrapped: true }], 9);
  const inner = t.buffer.active.getLine.bind(t.buffer.active);
  t.buffer.active.getLine = (i) => { asked.push(i); return inner(i); };
  const got = readLogicalLine(t, 1);
  assert.strictEqual(got.startY, 1);
  assert.ok(asked.every((i) => i >= 0), "negative index requested: " + asked.join(","));
});

// Hazard 3: translateToString does not return `cols` chars for rows holding
// double-width or combining characters, so index -> cell by division drifts and
// eventually lands on the wrong ROW. rowStarts makes it a lookup instead.
test("a short row still maps the following row correctly", () => {
  const t = fakeTerm(
    [{ text: "ab", exact: true }, { text: "https://x.dev/a.md", wrapped: true, exact: true }],
    9,
  );
  const { text, startY, cols, rowStarts } = readLogicalLine(t, 1);
  assert.strictEqual(text, "abhttps://x.dev/a.md");
  assert.deepStrictEqual(rowStarts, [0, 2]);
  const tok = scanLinks(text).find((k) => k.kind === "url");
  assert.strictEqual(cellFromIndex(tok.start, rowStarts, startY, cols).y, 2, "starts on row 2");
});

test("cellFromIndex gives 1-based columns and clamps to cols", () => {
  assert.deepStrictEqual(cellFromIndex(0, [0, 9], 1, 9), { x: 1, y: 1 });
  assert.deepStrictEqual(cellFromIndex(8, [0, 9], 1, 9), { x: 9, y: 1 });
  assert.deepStrictEqual(cellFromIndex(9, [0, 9], 1, 9), { x: 1, y: 2 });
  assert.deepStrictEqual(cellFromIndex(17, [0, 9], 1, 9), { x: 9, y: 2 });
  assert.deepStrictEqual(cellFromIndex(40, [0, 9], 1, 9), { x: 9, y: 2 }, "clamped");
});

test("a URL split across rows produces one link spanning both", () => {
  const t = fakeTerm([{ text: "https://a" }, { text: ".dev/x.md", wrapped: true }], 9);
  const links = linksForRow(t, 2);
  assert.strictEqual(links.length, 1);
  assert.strictEqual(links[0].text, "https://a.dev/x.md");
  assert.deepStrictEqual(links[0].range, { start: { x: 1, y: 1 }, end: { x: 9, y: 2 } });
});

// Hazard 1: Linkifier._removeIntersectingLinks(y, ...) assumes every returned link
// touches row y. A link entirely ABOVE y is treated as covering columns 0..end.x OF
// ROW y, which then evicts a legitimate link there — the link under the cursor
// vanishes intermittently on wrapped lines.
test("only links touching the queried row are returned", () => {
  const t = fakeTerm(
    [
      { text: "~/aaa.md " },
      { text: "https://", wrapped: true },
      { text: "b.dev/c.md", wrapped: true },
    ],
    9,
  );
  const all = linksForRow(t, 1).map((l) => l.text);
  assert.ok(all.includes("~/aaa.md"), "row 1 sees the path");
  for (const y of [1, 2, 3]) {
    for (const l of linksForRow(t, y)) {
      assert.ok(l.range.start.y <= y && l.range.end.y >= y,
        "link " + l.text + " does not touch row " + y);
    }
  }
});

test("joining is bounded by maxRows", () => {
  const rows = [{ text: "aaaaaaaaa" }];
  for (let i = 0; i < 200; i++) rows.push({ text: "aaaaaaaaa", wrapped: true });
  const got = readLogicalLine(fakeTerm(rows, 9), 150, 5);
  assert.ok(got.text.length <= 5 * 9, "joined at most maxRows rows, got " + got.text.length);
});
