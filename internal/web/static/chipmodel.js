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
//
// statusOf(tab) (m4) is the same injection pattern for connection status —
// the same t.status() the desktop tab-bar reads (see tabs.js renderBar).
// Consulted ONLY for terminal tabs, like activityOf: a viewer has no
// connection of its own. `status` mirrors the desktop bar's own rule —
// the healthy steady state ("connected") carries no information, so it is
// suppressed to "" the same way; any other value (connecting…,
// reconnecting…, error, taken over, ended, …) is a real signal and passes
// through as-is.
function chipModel(tabs, focused, activityOf, statusOf) {
  return (tabs || []).map((t) => {
    const on = t === focused;
    if (t.kind === "viewer") {
      return { kind: "viewer", label: t.name || t.path, title: t.path, on, wait: false, closable: on, status: "" };
    }
    const activity = activityOf ? activityOf(t) : null;
    const label = t.name || t.id;
    const rawStatus = statusOf ? statusOf(t) : null;
    const status = rawStatus && rawStatus !== "connected" ? rawStatus : "";
    return { kind: "terminal", label, title: label, on, wait: activity === "waiting", closable: on, status };
  });
}

// shouldScrollToFocused: the mobile chip strip's "did the focused chip
// actually change" gate. renderMobileChips (app.js) runs on every session
// poll (~3s), not only on navigation — scrolling on every render fights any
// manual scroll of the overflowing strip, snapping it back to the focused
// chip within one poll cycle even while the operator is browsing the rest of
// it. The caller tracks `lastScrolled` (the tab object last scrolled to) in a
// module-level variable and updates it only when this returns true, so a
// render that reaches the same focused tab as last time is a no-op here.
// Identity comparison (===), not a derived key: tab objects are stable for
// the life of a tab (TerminalTab/ViewerTab instances), so reference equality
// is both correct and needs no DOM/string plumbing to stay pure.
function shouldScrollToFocused(focused, lastScrolled) {
  return !!focused && focused !== lastScrolled;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { chipModel, shouldScrollToFocused };
}
