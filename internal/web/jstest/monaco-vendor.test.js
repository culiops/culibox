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

// scripts/vendor-monaco.sh copies out vs/ and LICENSE and writes the manifest beside them. Nothing else belongs at the root.
test("monaco-vendor root holds only vs, LICENSE and MANIFEST.sha256", () => {
  assert.deepStrictEqual(fs.readdirSync(ROOT).sort(), ["LICENSE", "MANIFEST.sha256", "vs"]);
});

// The script trims the TypeScript, CSS and HTML language services (about 16.7 MB of workers and the chunks that start them).
// The dock highlights those languages with the Monarch tokenizers, which stay, as does JSON (no Monarch JSON exists).
test("the TypeScript, CSS and HTML language services are trimmed; JSON and the Monarch languages stay", () => {
  const rels = readManifest().map((e) => e.rel);
  const gone = rels.filter((r) =>
    /^vs\/language\/(typescript|css|html)\//.test(r) || /^vs\/assets\/(ts|css|html)\.worker-/.test(r) || /^vs\/(ts|css|html)Mode-/.test(r));
  assert.deepStrictEqual(gone, [], "no file of a trimmed service is vendored");
  const kept = [
    /^vs\/language\/json\/monaco\.contribution\.js$/, /^vs\/jsonMode-[\w-]+\.js$/, /^vs\/assets\/json\.worker-[\w-]+\.js$/,
    /^vs\/assets\/editor\.worker-[\w-]+\.js$/, /^vs\/basic-languages\/monaco\.contribution\.js$/,
    ...["go", "yaml", "hcl", "markdown", "javascript", "typescript", "css", "html", "less", "scss"].map((l) => new RegExp("^vs/" + l + "-[\\w-]+\\.js$")),
  ];
  for (const re of kept) assert.ok(rels.some((r) => re.test(r)), "still vendored: " + re);
  assert.ok(!rels.some((r) => r.startsWith("vs/nls/")), "the locale bundles are dropped: monacoload.js sets no vs/nls language");
  assert.ok(rels.includes("vs/nls.messages-loader.js"), "the loader plugin that would ask for one stays, as editor.main.js depends on it");
});

// editor.main.js names the three chunks that hook a service up (an onLanguage listener that loads a mode and starts a worker),
// so the script keeps them as stubs of the same id that export an empty register. The fourth chunk is JSON's, the real one.
test("the chunks of the trimmed services are stubs that register nothing", () => {
  const STUB = /^define\("vs\/([\w.-]+)",\["exports"\],\(function\(e\)\{"use strict";e\.register=Object\.freeze\(Object\.create\(null\)\)\}\)\);\n$/;
  const chunks = readManifest().map((e) => e.rel).filter((r) => /^vs\/monaco\.contribution-[\w-]+\.js$/.test(r));
  const real = [];
  let stubs = 0;
  for (const rel of chunks) {
    const text = fs.readFileSync(path.join(ROOT, rel), "utf8");
    const m = STUB.exec(text);
    if (m) { stubs++; assert.strictEqual("vs/" + m[1] + ".js", rel, "a stub defines the module id of its own file"); }
    else real.push(text);
  }
  assert.strictEqual(stubs, 3, "css, html and typescript are stubbed");
  assert.strictEqual(real.length, 1, "one real contribution chunk remains");
  assert.ok(real[0].includes('onLanguage("json"'), "and it is JSON's");
  for (const text of [...real, fs.readFileSync(path.join(ROOT, "vs", "editor", "editor.main.js"), "utf8")]) {
    assert.ok(!/onLanguage\("(typescript|javascript|css|scss|less|html|handlebars|razor)"/.test(text) && !text.includes("registerHTMLLanguageService"), "nothing hooks a trimmed service");
  }
});

// What the loader asks for. A module's dependencies are the array in its define() header and the lazy require([...]) calls
// inside it (the Monarch language chunks load that way); "../x" resolves against the requiring module's own id.
function moduleDeps(rel, text) {
  const ids = [];
  const head = /^define\("[^"]+",\[([^\]]*)\]/.exec(text);
  if (head) for (const m of head[1].matchAll(/"([^"]+)"/g)) ids.push(m[1]);
  for (const m of text.matchAll(/[\w$]\(\["([^"]+)"\],[\w$]+,[\w$]+\)/g)) ids.push(m[1]);
  return ids
    .filter((id) => id !== "exports" && id !== "require")
    .map((id) => id.replace(/!$/, ""))
    .map((id) => (id.startsWith("vs/") ? id : path.posix.join(path.posix.dirname(rel), id)) + ".js");
}

// The trim must leave no hole: everything editor.main can load, now or lazily, is vendored. The one deliberate gap is the
// URL of a trimmed worker, which a worker-wrapper module computes but nothing ever starts, as no service is left to ask.
test("everything editor.main.js can load is vendored, apart from the trimmed workers' URLs", () => {
  const listed = new Set(readManifest().map((e) => e.rel));
  const seen = new Set();
  const missingAssets = new Set();
  const visit = (rel) => {
    if (seen.has(rel)) return;
    seen.add(rel);
    assert.ok(listed.has(rel), "a module the loader would fetch is not vendored: " + rel);
    const text = fs.readFileSync(path.join(ROOT, rel), "utf8");
    for (const dep of moduleDeps(rel, text)) visit(dep);
    for (const m of text.matchAll(/toUrl\("\.\/(assets\/[\w.-]+\.js)"\)/g)) if (!listed.has("vs/" + m[1])) missingAssets.add(m[1]);
  };
  visit("vs/editor/editor.main.js");
  assert.ok(seen.size > 100, "the walk reached the editor, the workers' wrappers and the Monarch languages: " + seen.size);
  assert.deepStrictEqual([...missingAssets].map((a) => a.replace(/-[\w-]+\.js$/, "")).sort(), ["assets/css.worker", "assets/html.worker", "assets/ts.worker"]);
});
