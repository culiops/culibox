// JetBrains Mono is vendored (three woff2 subsets plus LICENSE) from the npm tarball of
// @fontsource-variable/jetbrains-mono pinned at 5.3.0; scripts/vendor-jetbrains-mono.sh writes MANIFEST.sha256 and
// SOURCE.txt beside them. This pins the manifest to the files, so the font cannot drift from the checked tarball
// without the diff showing it, and pins what was taken: latin, latin-ext and Vietnamese, the normal (upright) axis only.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const ROOT = path.join(__dirname, "..", "static", "vendor", "jetbrains-mono");
const FILES = [
  "LICENSE",
  "jetbrains-mono-latin-ext-wght-normal.woff2",
  "jetbrains-mono-latin-wght-normal.woff2",
  "jetbrains-mono-vietnamese-wght-normal.woff2",
];
const INTEGRITY = "sha512-F32xpS2NsGYoQi2ADSkKTgpJj7ozajsGgDJ8woTnqjmIB+dxDIqImjl4pXZVEExu8UFZ2ndhmX18EBS/hdz3Lw==";

test("the vendored files are exactly the manifest's, byte for byte", () => {
  const text = fs.readFileSync(path.join(ROOT, "MANIFEST.sha256"), "utf8");
  assert.ok(text.endsWith("\n"));
  const lines = text.slice(0, -1).split("\n").map((l) => {
    const m = /^([0-9a-f]{64})  (\S+)$/.exec(l);
    assert.ok(m, l);
    return m;
  });
  assert.deepStrictEqual(lines.map((m) => m[2]), FILES);
  for (const m of lines) {
    const sum = crypto.createHash("sha256").update(fs.readFileSync(path.join(ROOT, m[2]))).digest("hex");
    assert.strictEqual(sum, m[1], m[2]);
  }
  assert.deepStrictEqual(fs.readdirSync(ROOT).sort(), [...FILES, "MANIFEST.sha256", "SOURCE.txt"].sort(), "nothing stray");
});

test("the licence is the SIL OFL 1.1, and SOURCE.txt names the package, version and npm integrity", () => {
  assert.match(fs.readFileSync(path.join(ROOT, "LICENSE"), "utf8"), /SIL Open Font License, Version 1\.1/);
  const src = fs.readFileSync(path.join(ROOT, "SOURCE.txt"), "utf8");
  assert.ok(src.includes("@fontsource-variable/jetbrains-mono@5.3.0"));
  assert.ok(src.includes(INTEGRITY));
  assert.match(src, /github\.com\/JetBrains\/JetBrainsMono/);
  assert.ok(fs.readFileSync(path.join(__dirname, "..", "..", "..", "scripts", "vendor-jetbrains-mono.sh"), "utf8").includes(INTEGRITY));
});

test("each subset is a woff2 file", () => {
  for (const f of FILES.filter((f) => f.endsWith(".woff2"))) {
    assert.strictEqual(fs.readFileSync(path.join(ROOT, f)).subarray(0, 4).toString("latin1"), "wOF2", f);
  }
});
