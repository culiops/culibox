"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// The page's own dialogs are styled, stack correctly, and cannot be switched
// off. Chrome lets a user tick "Prevent this page from creating additional
// dialogs", which permanently disables alert/confirm/prompt for that tab: a
// silent failure for the typed-name gate on protected folders. Nothing else
// catches a reintroduction, because these are legal browser globals.
//
// Two patterns, because the [^.\w] that keeps askPrompt( and confirmKill( from
// matching also excludes window.confirm( — which is what the code used to say,
// and the most natural way for it to come back.
const DIR = path.join(__dirname, "..", "static");
const NATIVE = /(^|[^.\w])(alert|confirm|prompt)\s*\(|\bwindow\s*\.\s*(alert|confirm|prompt)\s*\(/;

// scan is exported-by-reuse: the "guard bites" test calls it against a fixture
// directory so a green result here is backed by a demonstrated red one.
function scan(dir) {
  const offenders = [];
  for (const f of fs.readdirSync(dir)) {
    if (!f.endsWith(".js")) continue;
    const src = fs.readFileSync(path.join(dir, f), "utf8");
    src.split("\n").forEach((line, i) => {
      // Stripping // also truncates at a URL or a regex literal containing //.
      // That can only hide a call, never invent one: false negatives, never
      // false positives.
      const code = line.replace(/\/\/.*$/, "");
      if (NATIVE.test(code)) offenders.push(f + ":" + (i + 1) + "  " + line.trim());
    });
  }
  return offenders;
}

test("no page script calls a native dialog", () => {
  const offenders = scan(DIR);
  assert.deepStrictEqual(offenders, [], "native dialogs found:\n" + offenders.join("\n"));
});

// A guard that has never been seen to fail proves nothing. Pin both spellings.
test("the guard catches both the bare and the window-qualified form", () => {
  const tmp = fs.mkdtempSync(path.join(require("node:os").tmpdir(), "guard-"));
  try {
    fs.writeFileSync(path.join(tmp, "bare.js"), 'const raw = prompt("name");\n');
    fs.writeFileSync(path.join(tmp, "qualified.js"), 'if (!window.confirm("x")) return;\n');
    fs.writeFileSync(path.join(tmp, "innocent.js"),
      'await askPrompt({}); confirmKill(id); confirmDelete(p); const n = res.data.confirm_name;\n');
    const found = scan(tmp).map((s) => s.split(":")[0]).sort();
    assert.deepStrictEqual(found, ["bare.js", "qualified.js"]);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});
