"use strict";

// The F6 / Shift+F6 region cycle (#177, plan call 12, frame M8.8). Pure: app.js collects which regions are on
// screen, this says in which order they come and which one a step lands on; app.js then moves focus.
//
// Order: sidebar (the collapsed rail stands in for it), the focused pane's header + strip, that pane's terminal,
// the drawer. A region that is not on screen is skipped. An open page (Settings, Analytics, Audit) hides the grid,
// so the page is one region in place of the pane header and the terminal.

// regionOrder(v): v = { sidebar, rail, header, terminal, drawer, page } booleans -> ["sidebar" | "rail", "header",
// "terminal", "drawer"] or, with a page open, ["sidebar" | "rail", "page", "drawer"]. Names that are not shown are left out.
function regionOrder(v) {
  const s = v || {};
  const out = [];
  if (s.sidebar) out.push("sidebar");
  else if (s.rail) out.push("rail");
  if (s.page) out.push("page");
  else {
    if (s.header) out.push("header");
    if (s.terminal) out.push("terminal");
  }
  if (s.drawer) out.push("drawer");
  return out;
}

// regionStep(order, current, back): the region to go to. `current` is the region holding focus, or null when focus is
// in none of them (or in one that is no longer shown): forward then lands on the first, back on the last. Wraps.
// null when nothing is shown.
function regionStep(order, current, back) {
  if (!order || order.length === 0) return null;
  const at = order.indexOf(current);
  if (at < 0) return back ? order[order.length - 1] : order[0];
  const n = order.length;
  return order[(at + (back ? n - 1 : 1)) % n];
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { regionOrder, regionStep };
}
