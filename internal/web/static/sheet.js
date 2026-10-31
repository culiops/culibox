"use strict";
// Mobile bottom-sheet pure logic. No DOM. The action list is DERIVED from
// sessionrow.menuItems so the "End last / danger" invariant and the conditional
// (cwd/claude) items stay single-sourced; mobile only drops the pane concept and
// adds a jump-to-terminal at the top (M5).
var _sr = typeof require === "function" ? require("./sessionrow.js") : window;
var menuItems = _sr.menuItems || (typeof window !== "undefined" && window.menuItems);

var SHEET_GLYPHS = {
  "open-terminal": "▮",
  restart: "↻",
  files: "▢",
  attribution: "▤",
  rename: "✎",
  copycwd: "⇱",
  end: "⏹",
};

// M5's row order (measured against the canvas) is NOT menuItems' own order —
// the desktop menu lists rename first, the sheet leads with the two
// "go somewhere" actions before the edit/copy ones. ORDER is the single place
// that sequencing is decided; sheetActions only fills it in, so a conditional
// item's absence (no cwd, no claude transcript) just closes the gap.
//
// `attribution` ("Context by tool") is deliberately NOT in ORDER — same way
// `newpane` is dropped. On mobile it opens the desktop steer rail, whose only
// dismiss control (#steer-toggle) is hidden ≤700px, so it would be an
// un-closable dead-end (collides with the "steer/analytics hidden on mobile"
// decision). The desktop menu (openSessionMenu → menuItems) still offers it;
// only this mobile sheet drops it.
//
// `restart` only exists for an exited session (menuItems), where it is the
// main thing to do, so it sits right under Open terminal.
var ORDER = ["open-terminal", "restart", "files", "rename", "copycwd", "end"];

function sheetActions(s) {
  var byId = {};
  menuItems(s).forEach(function (m) { byId[m.id] = m; });
  byId["open-terminal"] = { id: "open-terminal", label: "Open terminal", danger: false };
  var out = [];
  for (var i = 0; i < ORDER.length; i++) {
    var m = byId[ORDER[i]];
    if (m) out.push({ id: m.id, label: m.label, glyph: SHEET_GLYPHS[m.id] || "", danger: !!m.danger });
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
