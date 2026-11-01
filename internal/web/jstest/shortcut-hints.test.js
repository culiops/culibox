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

// A key literal inside quotes: "Ctrl+", "Ctrl ", "Ctrl/" or "Ctrl-" (Ctrl/Cmd-S),
// "Cmd", "Shift+", "Alt+", or a glyph used as a key. \bCmd\b keeps identifiers
// such as shellCmd out of it.
const KEY_LITERAL = /["'`][^"'`\n]*(Ctrl\+|Ctrl |Ctrl[/-]|\bCmd\b|Shift\+|Alt\+|⌘|⌥|\^⇧|\^↵|\\u2318|\\u2325)/;

// Files the scan skips. shortcuts.js is the registry itself.
const SCAN_SKIP = new Set(["shortcuts.js"]);

// Strings that match KEY_LITERAL but are not key hints. Keyed "file: trimmed
// line", each with the reason. Empty today: add an entry only with a reason.
const NOT_A_HINT = {};

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

test("no key is spelled as a literal in any static script", () => {
  const files = fs.readdirSync(STATIC).filter((n) => n.endsWith(".js") && !SCAN_SKIP.has(n));
  assert.ok(files.includes("tabs.js") && files.includes("palette.js"), "scan list looks wrong");
  for (const f of files) {
    stripComments(read(f)).split("\n").forEach((l, i) => {
      if (NOT_A_HINT[f + ": " + l.trim()]) return;
      assert.ok(!KEY_LITERAL.test(l), f + ":" + (i + 1) + " spells a key: " + l.trim());
    });
  }
});

test("the shell-below titles in tabs.js come from the registry", () => {
  const tabs = read("tabs.js");
  assert.equal((tabs.match(/shortcutHint\("shell\.below", IS_MAC\)/g) || []).length, 2);
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
  // The viewer's buttons (pane and drawer alike) are the shared toolbar's.
  for (const id of ["editor.save", "zoom.out", "zoom.in", "viewer.find", "viewer.gotoLine"]) {
    assert.match(read("viewertoolbar.js"), new RegExp('shortcutHint\\("' + id.replace(".", "\\.") + '", IS_MAC\\)'), id);
  }
});

test("emptyHint splits the palette.open rendering into key caps", () => {
  const { shortcutHint } = require("../static/shortcuts.js");
  const caps = (mac) => { const r = shortcutHint("palette.open", mac); return mac ? Array.from(r) : r.split("+"); };
  assert.deepEqual(caps(true), ["⇧", "⌘", "K"]);
  assert.deepEqual(caps(false), ["Ctrl", "Shift", "K"]);
  assert.match(bodyOf(read("app.js"), "emptyHint"), /shortcutHint\("palette\.open", IS_MAC\)/);
});

test("showShortcuts builds its body from the registry and never touches innerHTML", () => {
  const body = stripComments(bodyOf(read("app.js"), "showShortcuts"));
  assert.match(body, /helpRows\(/);
  assert.match(body, /helpText\(/);
  assert.match(body, /tierRuntimeLabel\(boxRuntime\)/);
  assert.ok(!/innerHTML/.test(body), "help body must go in through textContent");
});

test("Settings and About each carry a Keyboard shortcuts button wired to showShortcuts", () => {
  const html = read("index.html");
  assert.match(html, /<button type="button" class="btn" id="kbd-open-settings">Keyboard shortcuts<\/button>/);
  assert.match(html, /<button type="button" class="btn" id="kbd-open-about">Keyboard shortcuts<\/button>/);
  assert.ok(html.indexOf("kbd-open-settings") < html.indexOf('id="tokens"'), "settings button belongs to the Appearance panel");
  assert.ok(html.indexOf("kbd-open-about") < html.indexOf("bug-open"), "about button comes before the bug-report paragraph");
  const app = read("app.js");
  for (const id of ["kbd-open-settings", "kbd-open-about"]) {
    assert.match(app, new RegExp('\\$\\("' + id + '"\\)\\.onclick = \\(\\) => showShortcuts\\(\\)'), id + " not wired");
  }
});

test("deskshell.js no longer owns a shortcut list", () => {
  const d = require("../static/deskshell.js");
  assert.equal(d.shortcutRows, undefined);
  assert.equal(d.shortcutText, undefined);
  assert.ok(!/function shortcut(Rows|Text)\b/.test(read("deskshell.js")));
});
