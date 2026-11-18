"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { monacoKeyFor, MONACO_OWNED, registrySkipsInEditor, MONACO_UNBIND } = require("../static/monacokeys.js");
const { SHORTCUTS } = require("../static/shortcuts.js");

// Monaco's real KeyMod values (CtrlCmd 2048, Shift 1024, Alt 512, WinCtrl 256) and a few of its KeyCode names.
const M = {
  KeyMod: { CtrlCmd: 2048, Shift: 1024, Alt: 512, WinCtrl: 256 },
  KeyCode: { KeyS: 49, KeyG: 34, KeyF: 36, KeyK: 41, Digit1: 22, F1: 59, Space: 10, Slash: 90 },
};
const chordOf = (id) => SHORTCUTS.find((s) => s.id === id).chords[0];

test("monacoKeys maps Mod+S", () => {
  // mod is Cmd on a Mac and Ctrl elsewhere: CtrlCmd on both
  for (const mac of [true, false]) {
    assert.strictEqual(monacoKeyFor({ mod: true, code: "KeyS" }, M, mac), M.KeyMod.CtrlCmd | M.KeyCode.KeyS, "mac=" + mac);
    // the registry's own save chord, so a change there is seen here
    assert.strictEqual(monacoKeyFor(chordOf("editor.save"), M, mac), 2048 | 49, "mac=" + mac);
  }
});

test("monacoKeys: ctrl is the Control key on a Mac and CtrlCmd elsewhere (Monaco's WinCtrl is Meta off a Mac)", () => {
  // Ctrl+G is the registry's viewer.gotoLine chord
  assert.strictEqual(monacoKeyFor({ ctrl: true, code: "KeyG" }, M, true), M.KeyMod.WinCtrl | M.KeyCode.KeyG);
  assert.strictEqual(monacoKeyFor(chordOf("viewer.gotoLine"), M, true), 256 | 34);
  assert.strictEqual(monacoKeyFor({ ctrl: true, code: "KeyG" }, M, false), M.KeyMod.CtrlCmd | M.KeyCode.KeyG);
  assert.strictEqual(monacoKeyFor(chordOf("viewer.gotoLine"), M, false), 2048 | 34);
  // a chord that wants both: two keys on a Mac, one off it (CtrlCmd is Ctrl there and there is no Cmd)
  assert.strictEqual(monacoKeyFor({ mod: true, ctrl: true, code: "KeyS" }, M, true), 2048 | 256 | 49);
  assert.strictEqual(monacoKeyFor({ mod: true, ctrl: true, code: "KeyS" }, M, false), 2048 | 49);
  // never the Meta key off a Mac
  assert.strictEqual(monacoKeyFor({ ctrl: true, shift: true, code: "KeyG" }, M, false) & M.KeyMod.WinCtrl, 0);
});

test("monacoKeys: a missing isMac is not a Mac", () => {
  assert.strictEqual(monacoKeyFor({ ctrl: true, code: "KeyG" }, M), 2048 | 34);
});

test("monacoKeys: shift and alt add their modifier on both platforms, a bare code is the code alone", () => {
  for (const mac of [true, false]) {
    assert.strictEqual(monacoKeyFor({ mod: true, shift: true, code: "KeyK" }, M, mac), 2048 | 1024 | 41, "mac=" + mac);
    assert.strictEqual(monacoKeyFor({ alt: true, code: "KeyS" }, M, mac), 512 | 49, "mac=" + mac);
    assert.strictEqual(monacoKeyFor({ code: "F1" }, M, mac), 59, "mac=" + mac);
    assert.strictEqual(monacoKeyFor({ mod: true, code: "Digit1" }, M, mac), 2048 | 22, "mac=" + mac);
  }
  assert.strictEqual(monacoKeyFor({ ctrl: true, shift: true, code: "Space" }, M, true), 256 | 1024 | 10);
  assert.strictEqual(monacoKeyFor({ ctrl: true, shift: true, code: "Space" }, M, false), 2048 | 1024 | 10);
});

test("monacoKeys: a chord Monaco cannot name maps to 0, on both platforms", () => {
  for (const mac of [true, false]) {
    assert.strictEqual(monacoKeyFor({ ctrl: true, code: "KeyZZ" }, M, mac), 0, "mac=" + mac);
    assert.strictEqual(monacoKeyFor({ mod: true, keys: ["Enter"] }, M, mac), 0, "mac=" + mac);
  }
  assert.strictEqual(monacoKeyFor({ mod: true, code: "KeyZZ" }, M), 0);
  assert.strictEqual(monacoKeyFor({ mod: true, code: "constructor" }, M), 0);
  assert.strictEqual(monacoKeyFor({ mod: true, code: "49" }, M), 0, "a reverse-enum name is not a code");
  assert.strictEqual(monacoKeyFor({ mod: true, keys: ["Enter"] }, M), 0, "a chord that matches on e.key only");
  assert.strictEqual(monacoKeyFor({ mod: true, codes: "Digit1-9" }, M), 0);
  assert.strictEqual(monacoKeyFor({ mod: true }, M), 0);
  assert.strictEqual(monacoKeyFor(null, M), 0);
  assert.strictEqual(monacoKeyFor({ mod: true, code: "KeyS" }, null), 0);
});

test("registrySkipsInEditor only for find and gotoLine", () => {
  assert.deepStrictEqual(MONACO_OWNED, ["viewer.find", "viewer.gotoLine"]);
  for (const id of MONACO_OWNED) {
    assert.strictEqual(registrySkipsInEditor(id, true), true, id);
    assert.strictEqual(registrySkipsInEditor(id, false), false, id);
    assert.strictEqual(registrySkipsInEditor(id, undefined), false, id);
  }
  // Review Focus 3: every other registry chord keeps winning inside Monaco (one save, app globals work).
  const others = SHORTCUTS.map((s) => s.id).filter((id) => !MONACO_OWNED.includes(id));
  assert.ok(others.includes("editor.save") && others.includes("palette.open") && others.includes("files.toggle"));
  for (const id of others) assert.strictEqual(registrySkipsInEditor(id, true), false, id);
  assert.strictEqual(registrySkipsInEditor("no.such.id", true), false);
  assert.strictEqual(registrySkipsInEditor(undefined, true), false);
});

test("MONACO_OWNED ids are rows of the registry (a rename there fails here)", () => {
  for (const id of MONACO_OWNED) assert.ok(SHORTCUTS.some((s) => s.id === id), id);
});

test("MONACO_UNBIND takes F1 away so there is one palette", () => {
  assert.deepStrictEqual(MONACO_UNBIND, ["editor.action.quickCommand"]);
});
