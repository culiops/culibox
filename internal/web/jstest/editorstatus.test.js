// editorstatus.js: the words around the dock's editor buffer (status bar, the
// Unsaved note, the read-only reason, the font step).
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { statusBarParts, langLabel, unsavedNote, readOnlyReason, fontStepped } = require("../static/editorstatus.js");
const { shortcutHint } = require("../static/shortcuts.js");

test("statusBarParts: position, indentation, encoding, line ending and language, as the mockup reads", () => {
  assert.deepStrictEqual(
    statusBarParts({ line: 169, col: 1, insertSpaces: false, tabSize: 4, eol: "\n", lang: "Go" }),
    { pos: "Ln 169, Col 1", indent: "Tab Size: 4", enc: "UTF-8", eol: "LF", lang: "Go" });
});

test("statusBarParts: spaces say Spaces, tabs say Tab Size, and CRLF is CRLF", () => {
  assert.strictEqual(statusBarParts({ insertSpaces: true, tabSize: 2 }).indent, "Spaces: 2");
  assert.strictEqual(statusBarParts({ insertSpaces: false, tabSize: 8 }).indent, "Tab Size: 8");
  assert.strictEqual(statusBarParts({ eol: "\r\n" }).eol, "CRLF");
  assert.strictEqual(statusBarParts({ eol: "\n" }).eol, "LF");
  assert.strictEqual(statusBarParts({ eol: "\r" }).eol, "LF", "anything but the model's CRLF string is LF");
});

test("statusBarParts: a missing or unusable value falls back, never NaN or undefined on screen", () => {
  assert.deepStrictEqual(statusBarParts(), { pos: "Ln 1, Col 1", indent: "Spaces: 4", enc: "UTF-8", eol: "LF", lang: "Plain Text" });
  const bad = statusBarParts({ line: NaN, col: -3, tabSize: 0, lang: 7 });
  assert.strictEqual(bad.pos, "Ln 1, Col 1");
  assert.strictEqual(bad.indent, "Spaces: 4");
  assert.strictEqual(bad.lang, "Plain Text");
  assert.strictEqual(statusBarParts({ line: 12.9, col: 3.2 }).pos, "Ln 12, Col 3");
});

test("langLabel: the first alias Monaco gives, else the id, else Plain Text", () => {
  const langs = [{ id: "go", aliases: ["Go"] }, { id: "plaintext", aliases: ["Plain Text", "text"] }, { id: "shell", aliases: [] }, { id: "ini", aliases: ["", "Ini"] }];
  assert.strictEqual(langLabel("go", langs), "Go");
  assert.strictEqual(langLabel("plaintext", langs), "Plain Text");
  assert.strictEqual(langLabel("shell", langs), "shell");
  assert.strictEqual(langLabel("ini", langs), "Ini", "an empty alias is skipped");
  assert.strictEqual(langLabel("zig", langs), "zig");
  assert.strictEqual(langLabel("", langs), "Plain Text");
  assert.strictEqual(langLabel("go", null), "go");
});

test("unsavedNote: named from the registry's save chord on each platform", () => {
  assert.strictEqual(unsavedNote(shortcutHint("editor.save", false)), "Unsaved · Ctrl+S to save");
  const mac = unsavedNote(shortcutHint("editor.save", true));
  assert.ok(mac.startsWith("Unsaved · ") && mac.endsWith(" to save"));
  assert.ok(!/Ctrl/.test(mac), "a Mac is never told to press Ctrl: " + mac);
  assert.ok(/⌘/.test(mac), "it names Command: " + mac);
  assert.strictEqual(unsavedNote(""), "Unsaved", "no key text: no key named");
  assert.strictEqual(unsavedNote(), "Unsaved");
});

test("readOnlyReason: one line per reason, empty for an editable file", () => {
  assert.match(readOnlyReason({ box: "s1" }), /box/);
  assert.match(readOnlyReason({ readonly: true }), /read-only/);
  assert.match(readOnlyReason({ unknown: true }), /not plain text/);
  assert.match(readOnlyReason({ box: "s1", readonly: true }), /box/, "the box wins: it is the more specific reason");
  assert.strictEqual(readOnlyReason({}), "");
  assert.strictEqual(readOnlyReason(), "");
});

test("fontStepped: one step, clamped, and null at an end", () => {
  const b = { min: 8, max: 32 };
  assert.strictEqual(fontStepped(12.5, 1, 1, b), 13.5);
  assert.strictEqual(fontStepped(12.5, -1, 1, b), 11.5);
  assert.strictEqual(fontStepped(8, -1, 1, b), null);
  assert.strictEqual(fontStepped(32, 1, 1, b), null);
  assert.strictEqual(fontStepped(8.5, -1, 1, b), 8, "clamped to the bound, not past it");
  assert.strictEqual(fontStepped(12, 1), 13);
});
