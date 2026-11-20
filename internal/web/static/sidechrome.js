// sidechrome.js: the desktop sidebar's chrome (#340 S6): the layout button that shares a row with New
// session, and the cost on the foot's links line. Classic script, loaded after kitmenu.js and before tabs.js
// (applyCap repaints the button through xlayPaint).

// xlayLabel: the layout in words. Any cap other than 1 or 2 reads as 4, clampCap's default (layout.js).
function xlayLabel(n) {
  if (n === 1) return "1 pane";
  if (n === 2) return "2 panes";
  return "4 panes";
}

function xlayTitle(n) {
  return "Layout: " + xlayLabel(n) + " (click to change)";
}

function xlayAria(n) {
  return "Layout: " + xlayLabel(n);
}

// xlayShapes: the layout glyph as svgIcon shapes (24x24): a frame, then one divider per split. The paths are
// Lucide's square, columns-2 and layout-grid (ISC licence), as mockup t1 draws them.
function xlayShapes(n) {
  const frame = ["rect", { width: "18", height: "18", x: "3", y: "3", rx: "2" }];
  if (n === 1) return [frame];
  if (n === 2) return [frame, ["path", { d: "M12 3v18" }]];
  return [frame, ["path", { d: "M3 12h18" }], ["path", { d: "M12 3v18" }]];
}

const SIDE_COST_TITLE = "Token usage today (UTC) priced at API list rates. A subscription does not bill this.";

// sideCostDep: a page global in the browser, the module under node (spawnpreview.js's spawnDep).
function sideCostDep(name, file) {
  return typeof globalThis[name] === "function" ? globalThis[name] : require(file)[name];
}

// sideCostLabel: the foot's cost. The amount is the Analytics page's usd(), so the same number reads the same
// way in both places; "≈" and the tooltip say it is list-price value, not a bill.
function sideCostLabel(cost) {
  return { text: "≈ " + sideCostDep("usd", "./analytics.js")(cost) + " today", title: SIDE_COST_TITLE };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { xlayLabel, xlayTitle, xlayAria, xlayShapes, SIDE_COST_TITLE, sideCostLabel };
}
