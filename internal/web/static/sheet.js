"use strict";
// Mobile bottom-sheet pure logic. No DOM. The action list is DERIVED from
// sessionrow.menuItems so the "End last / danger" invariant and the conditional
// (cwd/claude) items stay single-sourced; mobile only drops the pane concept and
// adds a jump-to-terminal at the top (M5).
var _sr = typeof require === "function" ? require("./sessionrow.js") : window;
var menuItems = _sr.menuItems || (typeof window !== "undefined" && window.menuItems);

var SHEET_GLYPHS = {
  "open-terminal": "▮",
  files: "▤",
  attribution: "▤",
  rename: "✎",
  copycwd: "⇱",
  end: "⏹",
};

function sheetActions(s) {
  var base = menuItems(s).filter(function (m) { return m.id !== "newpane"; });
  var out = [{ id: "open-terminal", label: "Open terminal", glyph: SHEET_GLYPHS["open-terminal"], danger: false }];
  for (var i = 0; i < base.length; i++) {
    out.push({ id: base[i].id, label: base[i].label, glyph: SHEET_GLYPHS[base[i].id] || "", danger: !!base[i].danger });
  }
  return out;
}

function sheetMeta(s) {
  return {
    model: s && s.model ? String(s.model) : "",
    effort: s && s.effort ? "effort " + s.effort : "",
    cwd: s && s.cwd ? String(s.cwd) : "",
    started: s && s.startedAt ? "started " + s.startedAt : "", // renderer humanises
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { SHEET_GLYPHS: SHEET_GLYPHS, sheetActions: sheetActions, sheetMeta: sheetMeta };
}
