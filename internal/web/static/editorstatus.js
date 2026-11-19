"use strict";

// The words the dock's editor shows around its buffer: the status bar under it, the
// "Unsaved" note on the crumbs row, and the reason a file is read-only. Pure and
// DOM-free so `node --test` reaches all of it; editortab.js only draws what these
// return. Classic script: browser globals, required under node.

// statusBarParts: the right-hand side of the status bar as texts, in the order they
// are drawn: "Ln 169, Col 1", the indentation ("Spaces: 4" when Tab inserts spaces,
// "Tab Size: 4" when it inserts a tab, as VS Code says it), the encoding, the line
// ending and the language. s = {line, col, insertSpaces, tabSize, eol, lang, utf8}.
//  - eol is the model's own string ("\r\n" is CRLF, anything else LF);
//  - the encoding is UTF-8, which is also what the buffer is saved as, unless the server
//    said the file is not (`utf8: false`: it is shown with U+FFFD for its invalid bytes
//    and opened read-only), which reads "Not UTF-8";
//  - a language the page has no name for reads "Plain Text".
function statusBarParts(s) {
  const o = s || {};
  const n = (v, d) => (Number.isFinite(v) && v > 0 ? Math.floor(v) : d);
  return {
    pos: "Ln " + n(o.line, 1) + ", Col " + n(o.col, 1),
    indent: (o.insertSpaces === false ? "Tab Size: " : "Spaces: ") + n(o.tabSize, 4),
    enc: o.utf8 === false ? "Not UTF-8" : "UTF-8",
    eol: o.eol === "\r\n" ? "CRLF" : "LF",
    lang: typeof o.lang === "string" && o.lang ? o.lang : "Plain Text",
  };
}

// langLabel: the name of a Monaco language id for the status bar. `languages` is
// monaco.languages.getLanguages() ([{id, aliases}]); the first alias is the name
// Monaco itself shows ("Go", "Markdown", "Plain Text"). An id with no alias is shown
// as the id, and no id at all is "Plain Text".
function langLabel(id, languages) {
  if (!id) return "Plain Text";
  const hit = (Array.isArray(languages) ? languages : []).find((l) => l && l.id === id);
  const alias = hit && Array.isArray(hit.aliases) ? hit.aliases.find((a) => typeof a === "string" && a) : "";
  return alias || id;
}

// unsavedNote: the crumbs row's right side while the buffer holds an edit, in place
// of "Edited by". `keyText` is the registry's own text for the save chord on this
// platform (shortcutHint("editor.save", IS_MAC): "Ctrl+S", or the Command glyph and S
// on a Mac), so the note never names a key the platform does not have. With no key text it is just "Unsaved": no
// script spells a key by hand (shortcut-hints.test.js).
function unsavedNote(keyText) {
  return keyText ? "Unsaved · " + keyText + " to save" : "Unsaved";
}

// readOnlyReason: the one line under the crumbs of a file the editor will not change,
// or "" when it is editable. A file in a session's box exists only inside that box
// and is never saved back; `readonly` is the server's flag (a scratch folder it
// refuses to write); `notUtf8` is a text file whose bytes are not valid UTF-8 (the
// buffer shows U+FFFD for them, and a save would write those instead); `unknown` is a
// kind of file this build does not know, opened read-only rather than risk saving
// something that is not text; `gone` is a file opened from a Changes row that is deleted from disk (an empty buffer, only for the
// Changes view to compare with the base text).
function readOnlyReason(f) {
  const { box, readonly, notUtf8, unknown, gone } = f || {};
  if (gone) return "This file was deleted from disk. Changes shows what was removed.";
  if (box) return "This file is inside a session's box, so it is read-only here.";
  if (readonly) return "This file is read-only: the server never saves to its folder.";
  if (notUtf8) return "This file is not UTF-8; saving it here would change its bytes.";
  if (unknown) return "This file is opened read-only: it is not plain text.";
  return "";
}

// fontStepped: the code font size one step up (+1) or down (-1) from `px`, clamped
// to `bounds` ({min, max}), or null when it is already at that end (the menu row
// then does nothing, rather than saving the same size again).
function fontStepped(px, dir, step, bounds) {
  const b = bounds || { min: 8, max: 32 };
  const next = Math.min(b.max, Math.max(b.min, px + (dir < 0 ? -1 : 1) * (step || 1)));
  return next === px ? null : next;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { statusBarParts, langLabel, unsavedNote, readOnlyReason, fontStepped };
}
