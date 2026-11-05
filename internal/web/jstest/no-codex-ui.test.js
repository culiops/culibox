"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// Claude Code is the only agent the UI offers (#89). The server still loads a
// stored Codex account or a `bin: codex` profile and refuses to spawn the
// account, but nothing on the page lets a user pick or create one. The page is
// static markup plus classic scripts that need a DOM, so this is pinned at the
// source level, the way settings-page.test.js does it.
const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// The text of `function name(...) { ... }` by brace matching from its opening line.
function body(src, name) {
  const m = new RegExp("(?:async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("the page markup offers and names no codex or openai", () => {
  for (const f of ["index.html", "login.html"]) {
    assert.doesNotMatch(read(f), /codex|openai/i, f + " still mentions codex or openai");
  }
});

test("the profile Binary suggestions are claude and bash", () => {
  const m = /<datalist id="pf-bins">([\s\S]*?)<\/datalist>/.exec(read("index.html"));
  assert.ok(m, "pf-bins datalist not found");
  assert.deepStrictEqual([...m[1].matchAll(/value="([^"]*)"/g)].map((x) => x[1]), ["claude", "bash"]);
});

test("New account has no provider choice and always creates a claude account", () => {
  assert.doesNotMatch(read("index.html"), /id="acct-provider"/);
  const b = body(read("accounts.js"), "acctNew");
  assert.doesNotMatch(b, /acct-provider/);
  assert.match(b, /provider:\s*"claude"/);
});
