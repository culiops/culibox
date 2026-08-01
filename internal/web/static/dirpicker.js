"use strict";

// One control for "which directory is the file tree rooted at". It replaces a
// <select> that could only offer $HOME's direct children and a text input that,
// despite its placeholder, opened FILES and never re-rooted anything.
//
// The pure functions below are exported for node --test; everything that
// touches the DOM lives beneath them and is covered by the behavioural pass.

// classifyPickerQuery decides which of three worlds the typed text is in. It is
// the single place that decision is made, because every consumer downstream —
// what to fetch, what to render, what Enter does — branches on it.
function classifyPickerQuery(q) {
  const v = String(q == null ? "" : q).trim();
  if (!v) return { mode: "empty", value: "" };
  // Only a leading slash. "~/x" is deliberately NOT treated as a path: the
  // index is relative to home already, so it fuzzy-matches fine, and expanding
  // it here would need HOME_DIR and make this function impure.
  if (v[0] === "/") return { mode: "path", value: v };
  return { mode: "fuzzy", value: v };
}

// pickerRows turns state into the rows to render. Basename in the label column
// and parent in the hint column: showing the whole relative path in both
// truncated the part that identifies the directory, which is the lesson
// palette.js already records for files.
function pickerRows(state) {
  const s = state || {};
  if (s.mode === "empty") {
    return (s.workspaces || []).map((w) => ({
      label: w.label, hint: "", path: w.path, kind: "workspace",
    }));
  }
  if (s.mode === "fuzzy") {
    return (s.hits || []).map((h) => {
      const cut = h.rel.lastIndexOf("/");
      return {
        label: cut >= 0 ? h.rel.slice(cut + 1) : h.rel,
        hint: cut >= 0 ? h.rel.slice(0, cut) : "",
        path: h.path,
        kind: "hit",
      };
    });
  }
  // "path" mode renders nothing: only the server can say whether that string is
  // a directory to root at or a file to open, and it is asked on Enter.
  return [];
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { classifyPickerQuery, pickerRows };
}
