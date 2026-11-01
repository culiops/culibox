"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// Hint drift guard: no script may spell a key in a string. Every hint a user
// reads comes from shortcuts.js (shortcutHint / jumpHint / renderChord), so a
// binding change cannot leave a stale hint behind (issue #52). Source-level,
// like spawn-refetch.test.js: app.js needs a DOM and cannot be require()d.
const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// Drop whole-line // comments and trailing // comments (a // inside a quoted
// string such as "https://" is kept: only a // preceded by whitespace counts).
function stripComments(src) {
  return src.split("\n")
    .filter((l) => !/^\s*\/\//.test(l))
    .map((l) => l.replace(/\s\/\/.*$/, ""))
    .join("\n");
}

// A key literal inside quotes: "Ctrl+, "Ctrl , "Alt+, or a glyph used as a key.
const KEY_LITERAL = /["'`][^"'`\n]*(Ctrl\+|Ctrl |Alt\+|⌘|⌥|\^⇧|\^↵|\\u2318|\\u2325)/;

function bodyOf(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("no key is spelled as a literal in palette, viewer-tab, filesdrawer, spawnpanel or spawnform", () => {
  for (const f of ["palette.js", "viewer-tab.js", "filesdrawer.js", "spawnpanel.js", "spawnform.js"]) {
    const lines = stripComments(read(f)).split("\n");
    lines.forEach((l, i) => {
      assert.ok(!KEY_LITERAL.test(l), f + ":" + (i + 1) + " spells a key: " + l.trim());
    });
  }
});

test("no key is spelled as a literal in the app.js hint functions", () => {
  const app = read("app.js");
  for (const fn of ["hintShortcuts", "emptyHint", "showShortcuts"]) {
    const text = stripComments(bodyOf(app, fn));
    assert.ok(!KEY_LITERAL.test(text), fn + " spells a key");
  }
  const spawn = stripComments(app).split("\n").filter((l) => /spawn-(prompt|foot)-hint/.test(l));
  assert.ok(spawn.length >= 2, "spawn hint block not found");
  for (const l of spawn) assert.ok(!KEY_LITERAL.test(l), "spawn hint spells a key: " + l.trim());
});

test("the platform regex lives in shortcuts.js only", () => {
  for (const f of fs.readdirSync(STATIC).filter((n) => n.endsWith(".js") && n !== "shortcuts.js")) {
    assert.ok(!/Mac\|iPhone\|iPad/.test(read(f)), f + " keeps its own platform test; use IS_MAC");
  }
});

test("index.html writes no key by hand, bar the phone palette glyph", () => {
  const html = read("index.html").replace('<span class="pal-short">⌘</span>', "");
  for (const bad of ["Ctrl+", "^⇧", "⌘", "Ctrl "]) assert.ok(!html.includes(bad), "index.html still spells " + bad);
  assert.match(read("index.html"), /<span class="pal-long"><\/span>/);
});

test("the registry renders the strings the palette and status bar use", () => {
  const { shortcutHint, jumpHint } = require("../static/shortcuts.js");
  assert.equal(shortcutHint("palette.open", true), "⇧⌘K");
  assert.equal(shortcutHint("palette.open", false), "Ctrl+Shift+K");
  assert.equal(jumpHint(3, false), "Ctrl+3");
  assert.equal(jumpHint(3, true), "⌘3");
});

test("app.js and the drawer set their hints from the registry", () => {
  const app = read("app.js");
  assert.match(app, /shortcutHint\("spawn\.submit", IS_MAC\) \+ " spawns"/);
  assert.match(app, /shortcutHint\("spawn\.submit", IS_MAC\) \+ " to spawn"/);
  assert.match(read("filesdrawer.js"), /shortcutHint\("files\.goto", IS_MAC\)/);
  assert.match(read("filesdrawer.js"), /shortcutHint\("editor\.save", IS_MAC\)/);
  assert.match(read("viewer-tab.js"), /shortcutHint\("editor\.save", IS_MAC\)/);
  assert.match(read("viewer-tab.js"), /shortcutHint\("zoom\.out", IS_MAC\)/);
  assert.match(read("viewer-tab.js"), /shortcutHint\("zoom\.in", IS_MAC\)/);
});

test("emptyHint splits the palette.open rendering into key caps", () => {
  const { shortcutHint } = require("../static/shortcuts.js");
  const caps = (mac) => { const r = shortcutHint("palette.open", mac); return mac ? Array.from(r) : r.split("+"); };
  assert.deepEqual(caps(true), ["⇧", "⌘", "K"]);
  assert.deepEqual(caps(false), ["Ctrl", "Shift", "K"]);
  assert.match(bodyOf(read("app.js"), "emptyHint"), /shortcutHint\("palette\.open", IS_MAC\)/);
});
