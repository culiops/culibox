// Monaco is vendored (min/vs plus LICENSE) from the npm tarball pinned at 0.57.0, and
// scripts/vendor-monaco.sh writes MANIFEST.sha256 beside it. This pins the manifest to the
// files: an edited, deleted or stray file under vendor/monaco fails here, so the editor
// engine cannot drift from the checked tarball without the diff showing it.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const ROOT = path.join(__dirname, "..", "static", "vendor", "monaco");
const MANIFEST = path.join(ROOT, "MANIFEST.sha256");

// walk: every regular file under dir, as a path relative to ROOT with "/" separators.
function walk(dir) {
  const out = [];
  for (const ent of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, ent.name);
    if (ent.isDirectory()) out.push(...walk(p));
    else out.push(path.relative(ROOT, p).split(path.sep).join("/"));
  }
  return out;
}

function readManifest() {
  assert.ok(fs.existsSync(MANIFEST), "vendor/monaco/MANIFEST.sha256 exists (run scripts/vendor-monaco.sh)");
  const text = fs.readFileSync(MANIFEST, "utf8");
  assert.ok(text.endsWith("\n"), "the manifest ends with a newline");
  return text.slice(0, -1).split("\n").map((line) => {
    const m = /^([0-9a-f]{64})  (\S+)$/.exec(line);
    assert.ok(m, "manifest line is '<sha256>  <relpath>': " + JSON.stringify(line));
    return { sum: m[1], rel: m[2] };
  });
}

test("monaco-vendor manifest matches files", () => {
  const entries = readManifest();
  assert.ok(entries.length > 0, "the manifest lists files");
  for (const { sum, rel } of entries) {
    const file = path.join(ROOT, rel);
    assert.ok(fs.existsSync(file), "listed file exists: " + rel);
    const got = crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
    assert.strictEqual(got, sum, "sha256 of " + rel);
  }
  const listed = new Set(entries.map((e) => e.rel));
  assert.strictEqual(listed.size, entries.length, "no file is listed twice");
  const stray = walk(path.join(ROOT, "vs")).filter((rel) => !listed.has(rel));
  assert.deepStrictEqual(stray, [], "every file under vendor/monaco/vs is listed");
  assert.ok(listed.has("LICENSE"), "the licence travels with the code");
});

test("monaco-vendor manifest is sorted bytewise and carries only vs/ and LICENSE", () => {
  const rels = readManifest().map((e) => e.rel);
  const sorted = [...rels].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  assert.deepStrictEqual(rels, sorted, "manifest is sorted");
  for (const rel of rels) {
    assert.ok(rel === "LICENSE" || rel.startsWith("vs/"), "unexpected manifest entry: " + rel);
  }
  assert.ok(rels.includes("vs/loader.js"), "the AMD loader is vendored");
});

// monacoload.js (the dock's lazy loader) names the files it asks the browser for. Each must be a file of the pinned tree.
test("monacoload.js asks only for files the manifest lists", () => {
  const ML = require("../static/monacoload.js");
  const listed = new Set(readManifest().map((e) => e.rel));
  const prefix = "/vendor/monaco/";
  for (const url of [ML.MONACO_BASE + "/loader.js", ML.MONACO_BASE + "/editor/editor.main.js", ML.MONACO_BASE + "/editor/editor.main.css"]) {
    assert.ok(url.startsWith(prefix), url + " is under " + prefix);
    assert.ok(listed.has(url.slice(prefix.length)), url + " is in the manifest");
  }
  assert.ok(!/^https?:|^\/\//.test(ML.MONACO_BASE), "never a CDN");
});

// The workers. Measured in 0.57.0: editor.main.js replaces MonacoEnvironment with its own {getWorker} when it runs, so a
// getWorkerUrl the page set before it is lost, and the workers are the ones this file's getWorker starts. They are same-origin
// only while every file it names is in the vendored tree. A re-vendor that changes how workers are found fails here, and
// monacoload.js (which sets no MonacoEnvironment, and says why) is looked at again.
test("the vendored editor.main.js starts its workers itself, from files the manifest lists", () => {
  const listed = new Set(readManifest().map((e) => e.rel));
  const main = fs.readFileSync(path.join(ROOT, "vs", "editor", "editor.main.js"), "utf8");
  assert.ok(/self\.MonacoEnvironment=\{getWorker:function/.test(main), "editor.main.js sets MonacoEnvironment.getWorker itself");
  const worker = /toUrl\("\.\.\/(assets\/editor\.worker-[\w-]+\.js)"\)/.exec(main);
  assert.ok(worker, "it names the editor worker as an asset next to it");
  assert.ok(listed.has("vs/" + worker[1]), "the editor worker is in the manifest: vs/" + worker[1]);
  for (const dep of main.match(/"\.\.\/[a-z]+\.worker-[\w-]+"/g) || []) {
    assert.ok(listed.has("vs/" + dep.slice(4, -1) + ".js"), "a language worker it depends on is in the manifest: " + dep);
  }
  assert.ok(/document\.createElement\("link"\)[^;]*;?[^;]*editor\.main\.css|toUrl\("vs\/editor\/editor\.main\.css"\)/.test(main), "it adds its own stylesheet link, so monacoload.js only waits for it");
});

test("every command monacokeys.js says to unbind has its key in the loader's table", () => {
  const ML = require("../static/monacoload.js");
  const { MONACO_UNBIND } = require("../static/monacokeys.js");
  assert.deepStrictEqual(ML.MONACO_UNBOUND_KEYS.map((k) => k.command).sort(), [...MONACO_UNBIND].sort());
});
