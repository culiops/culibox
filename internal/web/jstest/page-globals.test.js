"use strict";
// Page scripts are classic scripts that share ONE global scope, so a top-level name declared in two of them
// silently lets the later one win (Plan 7 gate A: the attention chip's chipModel was shadowed by the rail's).
// Each other jstest loads one script, so none could see it. This reads index.html, collects the local
// <script src> files, and fails on any top-level function/const/let/var/class name declared in two of them.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");

// Intentional duplicates: name -> reason. Empty unless a file really means to redefine another's name.
const ALLOW = {
  // sheet.js rebinds sessionrow.js's function with `var menuItems = _sr.menuItems || window.menuItems`: the same value, on purpose.
  menuItems: "sheet.js aliases sessionrow.menuItems",
};

function localScripts(html) {
  const out = [];
  for (const m of html.matchAll(/<script\s+src="\/([^"?#]+\.js)"/g)) {
    if (!m[1].startsWith("vendor/") && !out.includes(m[1])) out.push(m[1]);
  }
  return out;
}

function topLevelNames(src) {
  const names = new Set();
  // Column 0 only: the repo's top-level declarations are never indented; nested ones are.
  const re = /^(?:async\s+)?(?:function\s*\*?\s*([A-Za-z_$][\w$]*)|(?:const|let|var|class)\s+([A-Za-z_$][\w$]*))/gm;
  for (const m of src.matchAll(re)) names.add(m[1] || m[2]);
  return names;
}

for (const page of ["index.html", "login.html"]) {
  test("no top-level name is declared in two scripts of " + page, () => {
    const files = localScripts(fs.readFileSync(path.join(STATIC, page), "utf8"));
    if (page === "index.html") assert.ok(files.length > 50, "found the page scripts");
    const seen = new Map();
    const dup = [];
    for (const f of files) {
      for (const n of topLevelNames(fs.readFileSync(path.join(STATIC, f), "utf8"))) {
        if (seen.has(n) && !ALLOW[n]) dup.push(n + ": " + seen.get(n) + " and " + f);
        else if (!seen.has(n)) seen.set(n, f);
      }
    }
    assert.deepStrictEqual(dup, []);
  });
}
