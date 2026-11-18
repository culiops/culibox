"use strict";

// The Monaco theme for the dock's editor, built from the desktop tokens
// (desktop.css) so the editor sits in the same dark as the rest of the box, with
// the syntax colours of the read view's GitHub Dark theme (vendor/highlight-theme.css)
// so a file does not change colour when it moves between the two. Pure data: the
// loader hands it to monaco.editor.defineTheme once.
//
// Two hex forms on purpose. A colour in `colors` is a CSS colour ("#101317"); a
// rule's `foreground` is hex WITHOUT the hash, because Monaco builds it with
// Color.fromHex("#" + foreground) and a hash would make "##ff7b72".
//
// inherit:true keeps vs-dark's rules for every token this file does not name.
// The base theme also has rules MORE specific than keyword, number and string
// (keyword.flow, number.hex, string.sql); a more specific rule beats a general
// one, so those three are overridden too or `if` and `0xff` would keep the
// base colours next to the GitHub ones.
//
// A function so each caller gets its own object: defineTheme and a test can
// change what they are handed without changing the next caller's.
function culiboxTheme() {
  return {
    base: "vs-dark",
    inherit: true,
    rules: [
      { token: "keyword", foreground: "ff7b72" },
      { token: "keyword.flow", foreground: "ff7b72" },
      { token: "string", foreground: "a5d6ff" },
      { token: "string.sql", foreground: "a5d6ff" },
      { token: "number", foreground: "79c0ff" },
      { token: "number.hex", foreground: "79c0ff" },
      { token: "comment", foreground: "8b949e" },
      { token: "type", foreground: "d2a8ff" },
      { token: "function", foreground: "d2a8ff" },
    ],
    colors: {
      "editor.background": "#101317",
      "editor.foreground": "#e6e8eb",
      "editorLineNumber.foreground": "#4b525c",
      "editorLineNumber.activeForeground": "#e6e8eb",
      "editor.selectionBackground": "#283a54",
      "editorGutter.addedBackground": "#4ccf8a",
      "editorGutter.modifiedBackground": "#6ea8fe",
      "editorGutter.deletedBackground": "#f06a6a",
      // The overview ruler's marks for the lines that differ from git (editordiff.js): the same three colours. Monaco has no
      // default for these names, so the theme is where they come from.
      "editorOverviewRuler.addedForeground": "#4ccf8a",
      "editorOverviewRuler.modifiedForeground": "#6ea8fe",
      "editorOverviewRuler.deletedForeground": "#f06a6a",
      // The diff editors (Changes, Compare): the tinted green and red lines of mockup B2 (#10261a and #2a1517) in place of Monaco's
      // olive and pure red, and, for the characters that changed inside a line, the same two blended with the gutter's colours
      // (#4ccf8a and #f06a6a at 25 %, over the line tint) so a changed word stands out from its line.
      "diffEditor.insertedLineBackground": "#10261a",
      "diffEditor.removedLineBackground": "#2a1517",
      "diffEditor.insertedTextBackground": "#1f5036",
      "diffEditor.removedTextBackground": "#5c2a2c",
      "diffEditorGutter.insertedLineBackground": "#10261a",
      "diffEditorGutter.removedLineBackground": "#2a1517",
      "diffEditorOverview.insertedForeground": "#4ccf8a",
      "diffEditorOverview.removedForeground": "#f06a6a",
    },
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { culiboxTheme };
}
