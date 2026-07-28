"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { isPaletteHotkey, isInsertHotkey } = require("../static/palette.js");

// A KeyboardEvent stand-in: only the fields the matcher reads.
const ev = (code, mods) => Object.assign(
  { code, ctrlKey: false, shiftKey: false, altKey: false, metaKey: false }, mods || {});

test("mac opens on Cmd+K", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { metaKey: true }), true), true);
});

test("mac ignores Ctrl+Shift+K — Cmd is the platform convention", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true, shiftKey: true }), true), false);
});

test("non-mac opens on Ctrl+Shift+K", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true, shiftKey: true }), false), true);
});

// Ctrl+Shift+K is a magnet for global hotkeys registered by other desktop apps
// (Notion took it on the reporter's Windows machine, so Chrome never saw the
// keypress at all). The page cannot win that fight, so it offers a second combo.
test("non-mac also opens on Ctrl+Shift+Space", () => {
  assert.strictEqual(isPaletteHotkey(ev("Space", { ctrlKey: true, shiftKey: true }), false), true);
});

test("mac also accepts Ctrl+Shift+Space, so one combo works everywhere", () => {
  assert.strictEqual(isPaletteHotkey(ev("Space", { ctrlKey: true, shiftKey: true }), true), true);
});

test("plain Ctrl+K is never the palette — it is readline's kill-to-end-of-line", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true }), false), false);
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true }), true), false);
});

test("plain Space and Shift+Space are left alone", () => {
  assert.strictEqual(isPaletteHotkey(ev("Space"), false), false);
  assert.strictEqual(isPaletteHotkey(ev("Space", { shiftKey: true }), false), false);
});

test("adding Alt or the other platform modifier disqualifies the combo", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true, shiftKey: true, altKey: true }), false), false);
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true, shiftKey: true, metaKey: true }), false), false);
  assert.strictEqual(isPaletteHotkey(ev("Space", { ctrlKey: true, shiftKey: true, altKey: true }), false), false);
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { metaKey: true, ctrlKey: true }), true), false);
});

test("another key with the right modifiers is not the palette", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyJ", { ctrlKey: true, shiftKey: true }), false), false);
});

// Ctrl+Enter (⌘+Enter on mac) inserts the selected file's path at the prompt.
// Plain Enter must keep opening the viewer, so the modifier is what separates
// them and a bare Enter must never match.
test("ctrl+enter is the insert hotkey off mac", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter", ctrlKey: true }, false), true);
  assert.strictEqual(isInsertHotkey({ key: "Enter", metaKey: true }, false), false);
});

test("cmd+enter is the insert hotkey on mac", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter", metaKey: true }, true), true);
  assert.strictEqual(isInsertHotkey({ key: "Enter", ctrlKey: true }, true), false);
});

test("a bare enter is never the insert hotkey", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter" }, false), false);
  assert.strictEqual(isInsertHotkey({ key: "Enter" }, true), false);
});

test("a modified non-enter key is never the insert hotkey", () => {
  assert.strictEqual(isInsertHotkey({ key: "k", ctrlKey: true }, false), false);
});
