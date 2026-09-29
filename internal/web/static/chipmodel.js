"use strict";

// chipModel: the mobile Terminal screen's single tab-switcher (U1). Pure
// mapping from the open tabs — terminal AND viewer, in whatever order the
// caller hands over (app.js's openMobileTabs() walks cells in cell order
// then tab order, the same order Cell.tabs already holds and the desktop
// cell-bar itself paints in) — plus the currently focused tab, to the
// paintable chip list. renderMobileChips (app.js) does nothing but turn this
// model into DOM; no decision logic belongs there.
//
// Only the FOCUSED chip is `closable` — a separate close control mirroring
// what the desktop cell-bar's own per-tab × does today (closes the VIEW,
// never ends a session; End stays in the ⋯ actions sheet). `on` and
// `closable` are therefore always true on exactly the same one chip.
//
// activityOf(tab) is injected rather than the caller reading tab.activity()
// itself so this file stays DOM-free and node-testable; it is consulted
// ONLY for terminal tabs — a viewer has no activity of its own and its chip
// never carries the `wait` accent, however activityOf answers.
function chipModel(tabs, focused, activityOf) {
  return (tabs || []).map((t) => {
    const on = t === focused;
    if (t.kind === "viewer") {
      return { kind: "viewer", label: t.name || t.path, title: t.path, on, wait: false, closable: on };
    }
    const activity = activityOf ? activityOf(t) : null;
    const label = t.name || t.id;
    return { kind: "terminal", label, title: label, on, wait: activity === "waiting", closable: on };
  });
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { chipModel };
}
