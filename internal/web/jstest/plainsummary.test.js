"use strict";
// The one-line summary is the agent's last message, raw markdown, and every desktop
// surface that shows it on one line (the row sub-line, the ask strip, the palette)
// wrote the `**` through as text: "your turn: The last commit subject is: **merge: …**"
// in amber on the most-watched line of the product (#169 F04). plainSummary takes the
// markup out; the expanded message keeps the raw text. An ask is a tool call, not
// markdown, so it is never touched: a stripped `__pycache__` would show a command
// the agent is not going to run.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { plainSummary, cardContent } = require("../static/attention.js");
const { rowSubline } = require("../static/deskshell.js");
const { palSessionSub } = require("../static/palette.js");

test("plainSummary drops bold and strong-underscore markers", () => {
  assert.strictEqual(
    plainSummary("The last commit subject is: **merge: version Culibox from vX.Y.Z tags**"),
    "The last commit subject is: merge: version Culibox from vX.Y.Z tags");
  assert.strictEqual(plainSummary("__Done.__ Tests pass."), "Done. Tests pass.");
  // A summary cut off mid-bold still loses its marker.
  assert.strictEqual(plainSummary("Fixed **the flaky"), "Fixed the flaky");
});

test("plainSummary drops a bold that wraps code, and leaves link-shaped code alone", () => {
  assert.strictEqual(plainSummary("**Updated `config.go`**"), "Updated config.go");
  assert.strictEqual(plainSummary("The last commit subject is: **merge: `fix/ui` into main**"),
    "The last commit subject is: merge: fix/ui into main");
  assert.strictEqual(plainSummary("Fixed **`internal/web/app.js`** and tests"), "Fixed internal/web/app.js and tests");
  assert.strictEqual(plainSummary("**Fixed `nil` deref in `Rename`.** Tests pass."), "Fixed nil deref in Rename. Tests pass.");
  assert.strictEqual(plainSummary("Calls `handlers[type](event)` on each message"), "Calls handlers[type](event) on each message");
  // The placeholder characters cannot be smuggled in to pull out another span.
  assert.strictEqual(plainSummary("a \uE0000\uE001 b `c`"), "a 0 b c");
});

test("plainSummary keeps a ** that is not emphasis: globs, exponents, a lone pair", () => {
  assert.strictEqual(plainSummary("Run tests on **/*.test.js"), "Run tests on **/*.test.js");
  assert.strictEqual(plainSummary("2**10 = 1024"), "2**10 = 1024");
  assert.strictEqual(plainSummary("a ** b"), "a ** b");
  // The glob's ** does not pair with the bold that follows it.
  assert.strictEqual(plainSummary("Run **/*.go and **bold**"), "Run **/*.go and bold");
});

test("plainSummary keeps code text but not its backticks, and leaves code untouched", () => {
  assert.strictEqual(plainSummary("Run `go test ./...` again"), "Run go test ./... again");
  assert.strictEqual(plainSummary("Edited `__init__.py` and `a**b`"), "Edited __init__.py and a**b");
  assert.strictEqual(plainSummary("```\nmake build\n```"), "make build");
  assert.strictEqual(plainSummary("a stray ` tick"), "a stray tick");
});

test("plainSummary keeps a link's or image's text and drops the address", () => {
  assert.strictEqual(plainSummary("See [the PR](https://github.com/x/y/pull/1) for details"), "See the PR for details");
  assert.strictEqual(plainSummary("![diagram](a.png) attached"), "diagram attached");
  // Square brackets that are not a link stay (the display mask writes [masked:…]).
  assert.strictEqual(plainSummary("token [masked:aws-key] left"), "token [masked:aws-key] left");
  // A mask token that happens to sit before "(" is not a link either: the strip's
  // mask note looks for "[masked:".
  assert.strictEqual(plainSummary("key [masked:aws-key-id](rotated)"), "key [masked:aws-key-id](rotated)");
  // A code label (how agents link files), and one level of parentheses in the address.
  assert.strictEqual(plainSummary("See [`config.go`](internal/config.go)"), "See config.go");
  assert.strictEqual(plainSummary("[docs](https://x/a_(b)) ok"), "docs ok");
});

test("plainSummary drops heading markers and collapses whitespace", () => {
  assert.strictEqual(plainSummary("## Summary\n\nAll   green.\n# Next"), "Summary All green. Next");
  assert.strictEqual(plainSummary("Issue #169 is fixed"), "Issue #169 is fixed");
});

test("plainSummary stays fast on long agent text", () => {
  // The server sends at most 120 runes, but nothing stops a later caller passing
  // more, and this runs on every 3 s repaint. Worst cases for the patterns: brackets,
  // backticks, unclosed openers.
  for (const unit of ["[", "`", " __a", " **a", "[a](", "`a"]) {
    const t0 = process.hrtime.bigint();
    const out = plainSummary(unit.repeat(Math.ceil(100000 / unit.length)));
    const ms = Number(process.hrtime.bigint() - t0) / 1e6;
    assert.ok(out.length <= 1000, unit + ": " + out.length);
    assert.ok(ms < 250, unit + ": " + ms.toFixed(1) + " ms");
  }
});

test("plainSummary leaves snake_case and globs alone and tolerates nothing", () => {
  assert.strictEqual(plainSummary("set max__retries in *.yaml"), "set max__retries in *.yaml");
  assert.strictEqual(plainSummary(""), "");
  assert.strictEqual(plainSummary(null), "");
  assert.strictEqual(plainSummary(undefined), "");
});

const turn = { id: "t", activity: "waiting", need: "turn", claude: true, summary: "Done: **merge: x** in `main`" };
const ask = { id: "a", activity: "waiting", need: "ask", claude: true, ask: "Bash(rm -rf __pycache__ **/x.pyc)", summary: "**bold** summary" };

test("cardContent with plain strips the summary and never the ask", () => {
  assert.strictEqual(cardContent(turn, { plain: true }).text, "Done: merge: x in main");
  assert.strictEqual(cardContent(ask, { plain: true }).text, "Bash(rm -rf __pycache__ **/x.pyc)");
  // An ask with no tool call falls back to the summary, which is stripped.
  assert.strictEqual(cardContent({ ...ask, ask: "" }, { plain: true }).text, "bold summary");
  assert.strictEqual(cardContent({ activity: "waiting", summary: "**x**" }, { plain: true }).text, "x");
});

test("cardContent without plain is unchanged (the mobile card keeps today's text)", () => {
  assert.strictEqual(cardContent(turn).text, "Done: **merge: x** in `main`");
  assert.strictEqual(cardContent(ask).text, "Bash(rm -rf __pycache__ **/x.pyc)");
});

test("rowSubline (the desktop sidebar's second line) shows plain text", () => {
  // A finished turn (F21-B) reads its plain summary, muted: no "your turn:" prefix, no amber.
  assert.deepStrictEqual(rowSubline(turn), { text: "Done: merge: x in main", amber: false });
  assert.deepStrictEqual(rowSubline(ask), { text: "asks: Bash(rm -rf __pycache__ **/x.pyc)", amber: true });
  assert.deepStrictEqual(rowSubline({ activity: "working", summary: "## Plan\n**step 1**" }), { text: "Plan step 1", amber: false });
});

test("palSessionSub: plain on desktop, today's raw text on mobile", () => {
  assert.strictEqual(palSessionSub(turn, false), "Done: merge: x in main");
  assert.strictEqual(palSessionSub(turn, true), "Done: **merge: x** in `main`");
  assert.strictEqual(palSessionSub({}, false), "");
  assert.strictEqual(palSessionSub({}, true), "");
});

// app.js needs a DOM, so the two call sites are pinned at the source level: the desktop
// ask strip (renderNeedsBody with strip) asks for plain text and its expanded view still
// writes the raw peek; the mobile needs-you card (renderNeedsCard) writes the raw summary.
function appFn(name) {
  const app = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");
  const start = app.indexOf("function " + name + "(");
  assert.ok(start >= 0, name + " not found");
  return app.slice(start, app.indexOf("\nfunction ", start + 1));
}

test("the ask strip's collapsed line is plain, its expanded view raw", () => {
  const body = appFn("renderNeedsBody");
  assert.match(body, /const c = cardContent\(s, \{ plain: inStrip \}\);/);
  assert.match(body, /txt\.textContent = cached \? cached\.text : "Loading…";/);
});

test("the mobile needs-you card keeps the raw summary", () => {
  assert.match(appFn("renderNeedsCard"), /msg\.textContent = s\.summary \|\| "No summary";/);
});
