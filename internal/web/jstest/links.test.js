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

// Claude Code keeps worktrees under <repo>/.claude/worktrees/. A dot-directory
// segment is not a file extension: cutting the token there linked
// `/repo/.claude/worktrees/t49` as `/repo/.claude`, a directory, which is what
// Claude Code prints on every EnterWorktree (#49).
test("a dot-directory segment never ends a path token", () => {
  assert.deepStrictEqual(kinds("/home/u/r/.claude/worktrees/t49"), []);
  assert.deepStrictEqual(kinds("~/r/.claude/worktrees/t49"), []);
  assert.deepStrictEqual(kinds("/r/.claude/worktrees/x/Makefile"), []);
  assert.deepStrictEqual(kinds("node_modules/.bin/eslint"), []);
});

test("a file inside a worktree still links in full", () => {
  assert.deepStrictEqual(
    kinds("/home/u/r/.claude/worktrees/t49/src/a.go"),
    ["path:/home/u/r/.claude/worktrees/t49/src/a.go"],
  );
  assert.deepStrictEqual(
    kinds(".claude/worktrees/x/web/app.js:12"),
    ["path:.claude/worktrees/x/web/app.js"],
  );
  assert.deepStrictEqual(kinds("see ~/.claude/settings.json"), ["path:~/.claude/settings.json"]);
  assert.deepStrictEqual(kinds("v1.2/notes.md and a.tar.gz/"), ["path:v1.2/notes.md"]);
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
          // A real BufferLine keeps its ALLOCATED width, which stays wider than
          // term.cols after the terminal shrinks until reflow catches up —
          // translateToString then returns that stale width, space-padded. `width`
          // models it, and honouring the start/end arguments is what lets a caller
          // clamp back to the real terminal width.
          const w = r.width || cols;
          // A row is modelled as one string PER COLUMN, because start/end are
          // COLUMN indices, not string indices. A cell may hold more than one JS
          // character (combining marks) or none at all (the second half of a
          // double-width character), which is why a row's translated length is not
          // its column count. Columns beyond `cols` are the stale padding a
          // BufferLine keeps after the terminal shrinks.
          const cells = new Array(w).fill(" ");
          if (r.exact) cells[0] = r.text; // whole string in one cell: length != cols
          else for (let c = 0; c < cols; c++) cells[c] = r.text[c] === undefined ? " " : r.text[c];
          for (let c = cols; c < w; c++) cells[c] = " ";
          if (r.exact) for (let c = 1; c < cols; c++) cells[c] = "";
          return {
            isWrapped: !!r.wrapped,
            length: w,
            translateToString: (_trim, start, end) =>
              cells.slice(start || 0, end === undefined ? w : end).join(""),
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

// Regression, found only by running against a real terminal: after the terminal
// shrinks (138 -> 51 cols here), a BufferLine keeps its old allocated width, so
// translateToString(false) returns the STALE width padded with spaces. Joining
// wrapped rows then splices ~87 spaces into the middle of the logical line, and a
// URL spanning the wrap is truncated at that gap. The join must clamp each row to
// the terminal's current width.
test("a stale over-wide row does not splice spaces into the joined line", () => {
  const t = fakeTerm(
    [
      { text: "docs: https://culiops.dev/learn/quan-tri-he", width: 138 },
      { text: "-thong-linux/bai-1.html", wrapped: true, width: 138 },
    ],
    43,
  );
  const { text } = readLogicalLine(t, 1);
  assert.ok(!/\s{2,}/.test(text.trimEnd()), "no space gap inside the joined line: " + JSON.stringify(text));
  const tok = scanLinks(text).find((k) => k.kind === "url");
  assert.strictEqual(tok.text, "https://culiops.dev/learn/quan-tri-he-thong-linux/bai-1.html");
});

const { trimUrl } = require("../static/links.js");

test("trimUrl strips prose punctuation but keeps balanced brackets", () => {
  assert.strictEqual(trimUrl("https://a.dev/x."), "https://a.dev/x");
  assert.strictEqual(trimUrl("https://a.dev/x),"), "https://a.dev/x");
  assert.strictEqual(trimUrl("https://a.dev/F_(b)"), "https://a.dev/F_(b)");
  assert.strictEqual(trimUrl("https://a.dev/[i]"), "https://a.dev/[i]");
  assert.strictEqual(trimUrl("https://a.dev/x]}"), "https://a.dev/x");
});

// provideLinks runs on every hover row change over semi-trusted terminal output, so
// a quadratic trim is a freeze-the-tab vector: counting brackets by rescanning the
// whole string per stripped character measured 2.36s for 20k closers.
// Measured explicitly rather than via node:test's `timeout`, which is timer-based
// and cannot interrupt a synchronous function — it reported PASS on a call that
// really took 2.3s. Quadratic measured 150ms / 587ms / 2302ms / 9083ms at
// 5k / 10k / 20k / 40k closers; linear is ~1ms, so 500ms separates them by ~1000x.
test("trimUrl stays fast on a pathological run of closers", () => {
  const s = "https://a.dev/" + ")".repeat(20000);
  const t0 = process.hrtime.bigint();
  assert.strictEqual(trimUrl(s), "https://a.dev/");
  const ms = Number(process.hrtime.bigint() - t0) / 1e6;
  assert.ok(ms < 500, "trimUrl took " + ms.toFixed(1) + "ms, expected linear (<500ms)");
});

// The dot-directory rule must not cost a re-scan per backtrack step: written as a
// lookahead inside LINK_PATH_RE it took 15s on this line.
test("the dot-directory rule stays fast on a crafted line", () => {
  const s = "a/" + ".b/".repeat(20000);
  const t0 = process.hrtime.bigint();
  assert.deepStrictEqual(scanLinks(s), []);
  const ms = Number(process.hrtime.bigint() - t0) / 1e6;
  assert.ok(ms < 500, "scanLinks took " + ms.toFixed(1) + "ms, expected <500ms");
});

test("a scheme with no host is not a link", () => {
  assert.deepStrictEqual(scanLinks("use https:// not http://").filter((t) => t.kind === "url"), []);
  assert.deepStrictEqual(scanLinks("see https://; done").filter((t) => t.kind === "url"), []);
});

test("a URL carrying credentials is never rewritten", () => {
  const u = "https://evil.com@localhost:9000/a";
  assert.strictEqual(rewriteLocalhost(u, "100.76.118.121"), u);
});

test("cellFromIndex survives an empty rowStarts", () => {
  assert.deepStrictEqual(cellFromIndex(0, [], 7, 80), { x: 1, y: 7 });
});

// The up-walk is capped one row SHORTER than the read loop, which is the only thing
// guaranteeing the queried row ends up inside the joined window. Start it at 0 and a
// wrapped line longer than maxRows joins the rows ABOVE y, the row filter drops
// everything, and the bottom of the line silently has no links at all.
test("a wrapped line longer than maxRows still links its last row", () => {
  const rows = [{ text: "aaaaaaaaa" }];
  for (let i = 1; i < 59; i++) rows.push({ text: "aaaaaaaaa", wrapped: true });
  rows.push({ text: "http://b/", wrapped: true }); // index 59 -> 1-based row 60
  const links = linksForRow(fakeTerm(rows, 9), 60);
  assert.deepStrictEqual(links.map((l) => l.text), ["http://b/"]);
  assert.ok(links[0].range.end.y >= 60, "link must reach the queried row");
});

test("a token ending mid-row reports the inclusive end column", () => {
  const t = fakeTerm([{ text: "x https://a.dev y" }], 20);
  const link = linksForRow(t, 1).find((l) => l.kind === "url");
  // "x " then the 13-char URL: columns 3..15, inclusive end — and 15 < cols, so the
  // clamp cannot mask an off-by-one here the way an end-of-row token would.
  assert.deepStrictEqual(link.range, { start: { x: 3, y: 1 }, end: { x: 15, y: 1 } });
});

test("each row returns exactly the links that cross it", () => {
  const t = fakeTerm(
    [
      { text: "~/aaa.md " },
      { text: "http://b." , wrapped: true },
      { text: "dev/c.md  ", wrapped: true },
    ],
    9,
  );
  assert.deepStrictEqual(linksForRow(t, 1).map((l) => l.text), ["~/aaa.md"]);
  assert.deepStrictEqual(linksForRow(t, 2).map((l) => l.text), ["http://b.dev/c.md"]);
  assert.deepStrictEqual(linksForRow(t, 3).map((l) => l.text), ["http://b.dev/c.md"]);
});

// ---- path:line[:col] suffix (v0.17 viewer tier 1, D1) ----

const paths = (s) => scanLinks(s).filter((t) => t.kind === "path").map((t) => [t.text, t.line, t.col, s.slice(t.start, t.end)]);

test("suffix: line", () => {
  assert.deepStrictEqual(paths("see internal/web/x.go:123 here"), [["internal/web/x.go", 123, undefined, "internal/web/x.go:123"]]);
});

test("suffix: line and col", () => {
  assert.deepStrictEqual(paths("./a/b.ts:12:5"), [["./a/b.ts", 12, 5, "./a/b.ts:12:5"]]);
});

test("suffix: grep -n output", () => {
  assert.deepStrictEqual(paths("src/a.go:12:func x"), [["src/a.go", 12, undefined, "src/a.go:12"]]);
  assert.deepStrictEqual(paths("src/a.go:12:5: error"), [["src/a.go", 12, 5, "src/a.go:12:5"]]);
});

test("suffix: rejected", () => {
  for (const s of ["a/b.go:0", "a/b.go:12345678", "a/b.go:12abc", "a/b.go:3.5"]) {
    assert.deepStrictEqual(paths(s), [["a/b.go", undefined, undefined, "a/b.go"]], s);
  }
});

test("suffix: a col that fails the lookahead falls back to the line alone", () => {
  assert.deepStrictEqual(paths("a/b.go:12:5x"), [["a/b.go", 12, undefined, "a/b.go:12"]]);
});

test("suffix: trailing prose punctuation", () => {
  assert.deepStrictEqual(paths("(see a/b.go:7)."), [["a/b.go", 7, undefined, "a/b.go:7"]]);
});

test("suffix: a URL still wins", () => {
  assert.deepStrictEqual(paths("https://x.dev/a/b.go:12"), []);
});

test("suffix: tokens never overlap", () => {
  const toks = scanLinks("a/b.go:1/c.ts and d/e.go:2");
  for (let i = 1; i < toks.length; i++) assert.ok(toks[i - 1].end <= toks[i].start);
});

test("suffix: linksForRow carries line and col through", () => {
  const t = fakeTerm([{ text: "x src/a.go:12:5 y" }], 30);
  const l = linksForRow(t, 1)[0];
  assert.strictEqual(l.text, "src/a.go");
  assert.strictEqual(l.line, 12);
  assert.strictEqual(l.col, 5);
  // the clickable range covers the suffix too
  assert.deepStrictEqual(l.range, { start: { x: 3, y: 1 }, end: { x: 15, y: 1 } });
});

test("suffix: adversarial line is fast", () => {
  const s = "a/b.go" + ":1".repeat(30000);
  const t0 = performance.now();
  scanLinks(s);
  assert.ok(performance.now() - t0 < 200, "scan took " + (performance.now() - t0) + "ms");
});
