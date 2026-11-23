"use strict";
const test = require("node:test");
const assert = require("node:assert");
const m = require("../static/blamemodel.js");

const A = "a".repeat(40), B = "b".repeat(40), Z = "0".repeat(40);
const res = {
  state: "ok",
  commits: [
    { sha: A, author: "Minh", email: "m@x", time: 1000, summary: "add f (#12)", ref: "#12" },
    { sha: B, author: "Lan Tran", email: "l@x", time: 2000, summary: "edit" },
    { sha: Z, author: "Not Committed Yet", time: 3000, summary: "Version of f" },
  ],
  lines: [[0, 2], [1, 1], [2, 1]],
};

test("blameIndex expands runs and refuses what does not add up", () => {
  assert.deepStrictEqual(Array.from(m.blameIndex(res)), [0, 0, 1, 2]);
  assert.strictEqual(m.blameIndex({ commits: res.commits, lines: [[3, 1]] }), null);
  assert.strictEqual(m.blameIndex({ commits: res.commits, lines: [[0, 0]] }), null);
  assert.strictEqual(m.blameIndex({ commits: res.commits, lines: [[0, -1]] }), null);
  assert.strictEqual(m.blameIndex({ commits: res.commits, lines: [["0", 1]] }), null);
  assert.strictEqual(m.blameIndex({ commits: res.commits }), null);
  assert.strictEqual(m.blameIndex(null), null);
});

test("blameAt reads through the line map", () => {
  const idx = m.blameIndex(res);
  assert.strictEqual(m.blameAt(res, idx, null, 1).commit.author, "Minh");
  assert.strictEqual(m.blameAt(res, idx, null, 3).commit.author, "Lan Tran");
  assert.deepStrictEqual(m.blameAt(res, idx, null, 4), { commit: null, uncommitted: true });
  assert.strictEqual(m.blameAt(res, idx, null, 9), null);
  // A line inserted at the top: buffer line 2 is blamed line 1, line 1 is new.
  const map = Int32Array.from([0, 1, 2, 3, 4]);
  assert.deepStrictEqual(m.blameAt(res, idx, map, 1), { commit: null, uncommitted: true });
  assert.strictEqual(m.blameAt(res, idx, map, 4).commit.author, "Lan Tran");
});

test("blameAgo: long and short forms", () => {
  const now = 10_000_000 * 1000;
  const at = (sec) => m.blameAgo(10_000_000 - sec, now);
  assert.deepStrictEqual(at(10), { long: "just now", short: "now" });
  assert.deepStrictEqual(at(120), { long: "2 minutes ago", short: "2m" });
  assert.deepStrictEqual(at(60), { long: "1 minute ago", short: "1m" });
  assert.deepStrictEqual(at(3 * 3600), { long: "3 hours ago", short: "3h" });
  assert.deepStrictEqual(at(3 * 86400), { long: "3 days ago", short: "3d" });
  assert.deepStrictEqual(at(45 * 86400), { long: "1 month ago", short: "1mo" });
  assert.deepStrictEqual(at(800 * 86400), { long: "2 years ago", short: "2y" });
  assert.deepStrictEqual(at(-50), { long: "just now", short: "now" });
});

test("blameLineText cuts the summary", () => {
  const now = 2000 * 1000 + 3 * 86400 * 1000;
  assert.strictEqual(m.blameLineText(res.commits[1], now), "Lan Tran, 3 days ago · edit");
  const long = { author: "A", time: 2000, summary: "x".repeat(100) };
  const t = m.blameLineText(long, now);
  assert.ok(t.endsWith("…") && t.length < 80, t);
  assert.strictEqual(m.blameLineText(null, now), "Not committed yet");
});

test("link builders only from a checked base and checked ids", () => {
  const gh = { kind: "github", base: "https://github.com/o/r" };
  const gl = { kind: "gitlab", base: "https://gitlab.com/g/s/p" };
  const bb = { kind: "bitbucket", base: "https://bitbucket.org/o/r" };
  assert.strictEqual(m.blameCommitUrl(gh, A), "https://github.com/o/r/commit/" + A);
  assert.strictEqual(m.blameCommitUrl(gl, A), "https://gitlab.com/g/s/p/-/commit/" + A);
  assert.strictEqual(m.blameCommitUrl(bb, A), "https://bitbucket.org/o/r/commits/" + A);
  assert.strictEqual(m.blameCommitUrl(gh, "c".repeat(64)), "https://github.com/o/r/commit/" + "c".repeat(64));
  assert.strictEqual(m.blamePrUrl(gh, "#12"), "https://github.com/o/r/pull/12");
  assert.strictEqual(m.blamePrUrl(bb, "#12"), "https://bitbucket.org/o/r/pull-requests/12");
  assert.strictEqual(m.blamePrUrl(gl, "!34"), "https://gitlab.com/g/s/p/-/merge_requests/34");
  for (const [r, sha] of [
    [gh, A.toUpperCase()], [gh, Z], [gh, "a".repeat(39)], [gh, A + "/../x"], [null, A],
    [{ kind: "github", base: "javascript:alert(1)//github.com/o/r" }, A],
    [{ kind: "github", base: "https://user:tok@github.com/o/r" }, A],
    [{ kind: "github", base: "https://github.com/o/r/x" }, A],
    [{ kind: "github", base: "https://github.com.evil.com/o/r" }, A],
    [{ kind: "gitlab", base: "https://github.com/o/r" }, A],
    [{ kind: "github", base: "https://github.com/o/r?x" }, A],
    [{ kind: "other", base: "https://github.com/o/r" }, A],
  ]) assert.strictEqual(m.blameCommitUrl(r, sha), "", JSON.stringify([r, sha]));
  for (const [r, ref] of [[gl, "#12"], [gh, "!12"], [bb, "!12"], [gh, "#0"], [gh, "#12x"], [gh, "12"], [gh, ""], [gh, "#1234567890"]]) {
    assert.strictEqual(m.blamePrUrl(r, ref), "", JSON.stringify([r, ref]));
  }
});

test("blameLineFit: the summary gives way to the room, down to a floor", () => {
  const now = 2000 * 1000 + 3 * 86400 * 1000;
  const c = { author: "Lan Tran", time: 2000, summary: "fix: raise RDS storage, disk hit 91%" };
  assert.strictEqual(m.blameLineFit(c, now, 200), "Lan Tran, 3 days ago · fix: raise RDS storage, disk hit 91%");
  assert.strictEqual(m.blameLineFit(c, now, 40), "Lan Tran, 3 days ago · fix: raise RDS s…");
  assert.ok(m.blameLineFit(c, now, 40).length <= 40);
  assert.strictEqual(m.blameLineFit(c, now, 30), "", "author and time with under 8 summary characters: not inline");
  assert.strictEqual(m.blameLineFit(null, now, 40), "Not committed yet");
  assert.strictEqual(m.blameLineFit(null, now, 10), "");
  // Wide characters count two cells.
  const wide = (t) => [...t].reduce((n, ch) => n + (/[\u3000-\u9fff]/.test(ch) ? 2 : 1), 0);
  const cj = { author: "山田", time: 2000, summary: "修正修正修正修正修正修正" };
  const t = m.blameLineFit(cj, now, 36, wide);
  assert.ok(wide(t) <= 36 && t.endsWith("…"), t);
});

test("blameFits: the line end plus the text inside the content width", () => {
  assert.strictEqual(m.blameFits({ endLeft: 100, scrollLeft: 0, textWidth: 200, contentWidth: 400, gap: 24 }), true);
  assert.strictEqual(m.blameFits({ endLeft: 200, scrollLeft: 0, textWidth: 200, contentWidth: 400, gap: 24 }), false);
  // Scrolled right: the line end is further along than it looks.
  assert.strictEqual(m.blameFits({ endLeft: 100, scrollLeft: 150, textWidth: 200, contentWidth: 400, gap: 24 }), false);
  assert.strictEqual(m.blameFits({ endLeft: NaN, scrollLeft: 0, textWidth: 1, contentWidth: 400, gap: 0 }), false);
});

test("blameHeaderForm: wide, then short, then the icon", () => {
  const f = (rowWidth) => m.blameHeaderForm({ rowWidth, otherWidth: 100, wideWidth: 160, shortWidth: 40, trailNeed: 120 });
  assert.strictEqual(f(400), "wide");
  assert.strictEqual(f(300), "short");
  assert.strictEqual(f(200), "icon");
});
