"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { monacoLang } = require("../static/monacolang.js");
const { resolveEditorMode } = require("../static/editmode.js");

test("monacoLang covers the CM5 set", () => {
  const want = {
    "main.go": "go",
    "app.js": "javascript", "a.mjs": "javascript", "a.cjs": "javascript", "c.jsx": "javascript",
    "app.ts": "typescript", "app.tsx": "typescript",
    "task.py": "python",
    "deploy.sh": "shell", "a.bash": "shell", "a.zsh": "shell",
    "app.yaml": "yaml", "k8s.yml": "yaml",
    "data.json": "json",
    "README.md": "markdown",
    "main.tf": "hcl", "x.hcl": "hcl", "vars.tfvars": "hcl",
    "Dockerfile": "dockerfile", "a.dockerfile": "dockerfile",
    "query.sql": "sql",
    "app.ini": "ini", "config.toml": "ini", "redis.conf": "ini", "app.properties": "ini",
    "pom.xml": "xml",
    "index.html": "html",
    "app.css": "css",
    "a.lua": "lua", "a.pl": "perl", "index.php": "php", "app.rb": "ruby", "lib.rs": "rust",
    "script.ps1": "powershell",
    "a.cpp": "cpp", "a.hpp": "cpp", "a.c": "c", "a.h": "c",
    "A.java": "java", "A.kt": "kotlin",
  };
  for (const [file, lang] of Object.entries(want)) assert.strictEqual(monacoLang(file), lang, file);
});

test("monacoLang: Monaco has no makefile or nginx, so they stay plaintext", () => {
  for (const f of ["Makefile", "GNUmakefile", "build.mk", "site.nginx"]) assert.strictEqual(monacoLang(f), "plaintext", f);
});

test("monacoLang: the default is plaintext", () => {
  for (const f of ["notes.txt", "LICENSE", "a.unknownext", "", undefined, null, "dir/.gitignore", ".go", "x."]) {
    assert.strictEqual(monacoLang(f), "plaintext", String(f));
  }
});

test("monacoLang: directories, case and prototype names do not confuse it", () => {
  assert.strictEqual(monacoLang("/srv/app/Dockerfile"), "dockerfile");
  assert.strictEqual(monacoLang("a/b/MAIN.GO"), "go");
  assert.strictEqual(monacoLang(".env"), "ini");
  assert.strictEqual(monacoLang(".bashrc"), "shell");
  assert.strictEqual(monacoLang("dir.go/Makefile"), "plaintext");
  assert.strictEqual(monacoLang("constructor"), "plaintext");
  assert.strictEqual(monacoLang("a.constructor"), "plaintext");
  assert.strictEqual(monacoLang("a.__proto__"), "plaintext");
});

test("monacoLang: every extension the CM5 resolver knows gets a decision here (no silent drop)", () => {
  // editmode.js is the read view's table; a file it highlights must not fall to plaintext by accident.
  // These have no Monaco language and are plaintext on purpose.
  const plain = new Set(["groovy", "gradle", "cmake", "mk", "make", "nginx"]);
  const src = fs.readFileSync(path.join(__dirname, "..", "static", "editmode.js"), "utf8");
  const exts = [...src.slice(src.indexOf("const BY_EXT")).split("};")[0].matchAll(/\b([a-z0-9]+):\s*"/g)].map((m) => m[1]);
  assert.ok(exts.length > 40, "found the CM5 extension table");
  for (const e of exts) {
    if (!resolveEditorMode("x." + e)) continue;
    if (plain.has(e)) assert.strictEqual(monacoLang("x." + e), "plaintext", e);
    else assert.notStrictEqual(monacoLang("x." + e), "plaintext", e);
  }
});

test("monacoLang: every id it returns is a language the vendored Monaco registers", () => {
  const vs = path.join(__dirname, "..", "static", "vendor", "monaco", "vs");
  const files = [path.join(vs, "basic-languages", "monaco.contribution.js")]
    .concat(fs.readdirSync(vs).filter((n) => /^monaco\.contribution-.*\.js$/.test(n)).map((n) => path.join(vs, n)));
  const known = new Set(["plaintext"]);
  for (const f of files) for (const m of fs.readFileSync(f, "utf8").matchAll(/\bid:"([a-z0-9-]+)"/g)) known.add(m[1]);
  const src = fs.readFileSync(path.join(__dirname, "..", "static", "monacolang.js"), "utf8");
  const ids = new Set([...src.matchAll(/:\s*"([a-z0-9-]+)"/g)].map((m) => m[1]));
  ids.delete("plaintext");
  assert.ok(ids.size > 20, "found the ids in the table");
  for (const id of ids) assert.ok(known.has(id), id + " is not registered by the vendored Monaco");
});
