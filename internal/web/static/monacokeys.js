"use strict";

// Where a chord pressed inside Monaco goes: to Monaco or to the app's registry
// (shortcuts.js). The rule is that the registry wins everywhere, so Mod+S saves
// once, Ctrl+Shift+K opens the palette and Mod+Shift+E toggles the dock even with
// the cursor in the editor, EXCEPT for the two chords that are really editing
// commands: find and go to line. Those belong to Monaco's own widgets while it
// has focus. Pure and Monaco-free: the namespace is passed in, so tests pass a fake.

// Registry ids that defer to Monaco while it has focus.
const MONACO_OWNED = ["viewer.find", "viewer.gotoLine"];

// Monaco command ids to unbind. F1 is Monaco's own command palette; the box has
// one palette (palette.open), so a second one is a bug, not a feature.
const MONACO_UNBIND = ["editor.action.quickCommand"];

// registrySkipsInEditor: whether the registry's dispatcher must leave `id` alone
// because the keydown happened inside Monaco. True only for MONACO_OWNED.
function registrySkipsInEditor(id, targetInMonaco) {
  return !!targetInMonaco && MONACO_OWNED.includes(id);
}

// monacoKeyFor maps a registry chord ({mod, ctrl, shift, alt, code, ...}) to the
// number monaco.editor.addCommand and addAction take, or 0 when Monaco has no
// name for it (a chord that matches on e.key only, or an unknown code).
//
// mod is Cmd on a Mac and Ctrl elsewhere, which is exactly Monaco's KeyMod.CtrlCmd.
// ctrl is a literal Ctrl on every platform, which is Monaco's KeyMod.WinCtrl (the
// name is historical: on a Mac it is the Control key, not the Windows key).
// Monaco's KeyCode enum uses the same names as KeyboardEvent.code for the keys the
// registry binds (KeyS, Digit1, F1, Space, Slash). Looked up with a typeof check
// because a TypeScript enum also maps its numbers back to names: KeyCode["49"] is
// the string "KeyS", which must not pass for a key.
function monacoKeyFor(chord, M) {
  if (!chord || !chord.code || !M || !M.KeyCode || !M.KeyMod) return 0;
  const code = Object.prototype.hasOwnProperty.call(M.KeyCode, chord.code) ? M.KeyCode[chord.code] : undefined;
  if (typeof code !== "number") return 0;
  let keys = code;
  if (chord.mod) keys |= M.KeyMod.CtrlCmd;
  if (chord.ctrl) keys |= M.KeyMod.WinCtrl;
  if (chord.shift) keys |= M.KeyMod.Shift;
  if (chord.alt) keys |= M.KeyMod.Alt;
  return keys;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { monacoKeyFor, MONACO_OWNED, registrySkipsInEditor, MONACO_UNBIND };
}
