"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// Regex lookbehind is a SyntaxError at parse time on Safari before 16.4, and macOS 13 (supported, docs/MACOS.md) ships
// 16.0 to 16.3 until it is updated. A pattern inside new RegExp("...") is only parsed when that line runs, so one in a
// page script that every Settings › Profiles draw reaches blanks the page, and no Chromium-based test can see it.
// The page scripts must not hold one. (The profile page's machine-text marking did: profilepage.js, PFV_MACHINE.)
//
// Only the top-level page scripts are scanned: vendor/ and xterm/ are third-party builds with their own support story.
const DIR = path.join(__dirname, "..", "static");
const LOOKBEHIND = /\(\?<[=!]/;

// scanSource reports the lines of one file's text that spell a lookbehind, in a regex literal or in a string for new RegExp.
function scanSource(name, src) {
  const offenders = [];
  src.split("\n").forEach((line, i) => {
    if (LOOKBEHIND.test(line)) offenders.push(name + ":" + (i + 1) + "  " + line.trim());
  });
  return offenders;
}

function scan(dir) {
  const offenders = [];
  for (const f of fs.readdirSync(dir).filter((n) => n.endsWith(".js")).sort()) {
    offenders.push(...scanSource(f, fs.readFileSync(path.join(dir, f), "utf8")));
  }
  return offenders;
}

test("no page script contains a regex lookbehind", () => {
  assert.deepStrictEqual(scan(DIR), []);
});

test("the guard bites: a lookbehind in a literal and in a RegExp string is found, a lookahead and a named group are not", () => {
  const src = [
    "const a = /(?<=x)y/;",
    "const b = new RegExp(\"(?<!x)y\");",
    "const ok = /x(?=y)(?!z)(?<name>q)/;",
  ].join("\n");
  const got = scanSource("bad.js", src);
  assert.strictEqual(got.length, 2, got.join("\n"));
  assert.match(got[0], /^bad\.js:1 /);
  assert.match(got[1], /^bad\.js:2 /);
});
