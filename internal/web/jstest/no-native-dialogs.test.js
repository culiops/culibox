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

// scanSource reports offending lines in one file's worth of text. Line numbers
// are absolute in the file, so an offender inside an inline <script> still points
// at the line a human edits.
function scanSource(name, src) {
  const offenders = [];
  src.split("\n").forEach((line, i) => {
    // Stripping // also truncates at a URL or a regex literal containing //.
    // That can only hide a call, never invent one: false negatives, never
    // false positives.
    const code = line.replace(/\/\/.*$/, "");
    if (NATIVE.test(code)) offenders.push(name + ":" + (i + 1) + "  " + line.trim());
  });
  return offenders;
}

// scan is exported-by-reuse: the "guard bites" test calls it against a fixture
// directory so a green result here is backed by a demonstrated red one.
//
// index.html is scanned as well as the .js files. Its inline <script> blocks are
// real page code (the pre-paint layout restore among them) and were the one place
// a native dialog could come back unnoticed. Scanning the whole file rather than
// extracting the blocks keeps it simple; the cost is that a native call written
// in markup or CSS would also be reported, which is not a false alarm worth
// engineering around.
function scan(dir) {
  const offenders = [];
  for (const f of fs.readdirSync(dir)) {
    if (!f.endsWith(".js") && f !== "index.html") continue;
    offenders.push(...scanSource(f, fs.readFileSync(path.join(dir, f), "utf8")));
  }
  return offenders;
}

test("no page script calls a native dialog", () => {
  const offenders = scan(DIR);
  assert.deepStrictEqual(offenders, [], "native dialogs found:\n" + offenders.join("\n"));
});

// The four inline <script> blocks in index.html are page code too. They were
// outside the guard until now, so pin that they are covered.
test("inline scripts in index.html are covered", () => {
  const html = fs.readFileSync(path.join(DIR, "index.html"), "utf8");
  assert.ok(/<script>/.test(html), "expected inline <script> blocks to still exist");
  const withCall = html.replace("<script>", '<script>\nif (!confirm("x")) { }\n');
  const found = scanSource("index.html", withCall);
  assert.strictEqual(found.length, 1, "a native call inside an inline script must be caught");
  assert.match(found[0], /confirm/);
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
