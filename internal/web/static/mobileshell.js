"use strict";

// Mobile presentation shell. At <=700px the app shows ONE screen at a time
// (Sessions / Terminal / Files) switched by a bottom nav, instead of the
// desktop's stacked sidebar + terminal grid. This file is pure decision logic
// with no DOM; the controller in app.js does the wiring. Classic script:
// every top-level name below is a browser global — keep them unique.

const MOBILE_MAX_PX = 700;                        // must match the @media (max-width:700px) edge in app.css
const MOBILESHELL_KEY = "agent-box:mobileScreen";
const MOBILE_SCREENS = ["sessions", "terminal", "files"];

// One source of truth for the breakpoint so the CSS media query and the JS
// controller cannot disagree about the edge pixel.
function mobileActive(width) {
  return Number(width) <= MOBILE_MAX_PX;
}

// U8: the string a matchMedia() breakpoint listener watches, built from the
// same MOBILE_MAX_PX the CSS's `@media (max-width: 700px)` and mobileActive()
// above agree on — so a resize/rotate/devtools crossing never hard-codes a
// second "700" that could drift from the other two.
function mobileMediaQuery() {
  return "(max-width: " + MOBILE_MAX_PX + "px)";
}

// I1 fix: mobile has no un-maximize control (the desktop .cell-bar, and its
// ❐, is hidden <=700px — see app.css), and the mobile Terminal screen already
// shows exactly one pane on its own (#grid > .cell:not(.focus) { display:
// none }). A persisted `maximized` cell would otherwise blank the screen for
// every OTHER chip, with no way back short of clearing site data or widening
// past the breakpoint. So the grid ignores `maximized` while mobile is
// active — tabs.js's renderGrid/fitAll call this rather than reading the
// `maximized` module variable directly. The persisted value itself is left
// untouched by the caller, so a viewport that widens back past the
// breakpoint restores it for desktop (see the U8 matchMedia listener in
// app.js, which re-runs renderGrid() on that crossing).
function effectiveMaximized(maximized, isMobile) {
  return isMobile ? null : maximized;
}

// Stored screen is hostile input — hand-edited, written by an older version, or
// quota-truncated. Anything not one of the three known screens degrades to the
// landing screen rather than throwing: this is read in the pre-paint <script>,
// where an exception blocks the rest of head parsing.
function loadScreen(raw) {
  return MOBILE_SCREENS.indexOf(raw) >= 0 ? raw : "sessions";
}

function saveScreen(screen) {
  return loadScreen(screen);
}

// The single <html> class the 700px CSS keys screen visibility off.
function screenClass(screen) {
  return "mobile-screen-" + loadScreen(screen);
}

// The bottom-nav model: three items, the active one flagged, the waiting count
// carried on Sessions as a pip. Zero waiting => pip 0 (no dot): a badge
// announcing zero is worse than no badge — the same rule the collapsed Sessions
// bar uses in panes.js.
function navModel(activeScreen, waitingCount) {
  const active = loadScreen(activeScreen);
  const n = Number(waitingCount) || 0;
  return MOBILE_SCREENS.map(function (s) {
    return { screen: s, active: s === active, pip: s === "sessions" && n > 0 ? n : 0 };
  });
}

// The shell-nesting helpers (shellnest.js) are browser globals; under node they
// are required. Resolved at call time so load order cannot matter.
function mobileNestDep() {
  return typeof shellChildren === "function" && typeof isNestedShell === "function"
    ? { shellChildren, isNestedShell }
    : require("./shellnest.js");
}

// Group the session list the way the mobile Sessions screen shows it: the ones
// that need a person first, then the ones doing work, then everything at rest.
// "waiting" is the only needs-you state; "working" is running; anything else
// (idle/done/detached/crashed/unknown) is at rest. Empty groups are dropped so
// no header ever sits above nothing. The desktop sidebar's statusFn also says
// "your-turn" for a session that finished its turn (deskGroups, F21-B): its
// neutral "Done · your turn" group sits right after Needs you. The phone's
// statusFn never returns it, so a phone list has no such group.
function sessionGroups(sessions, statusFn) {
  const nest = mobileNestDep();
  const list = sessions || [];
  const kids = nest.shellChildren(list);
  const buckets = { "needs-you": [], "your-turn": [], running: [], idle: [] };
  for (const s of list) {
    // A nested shell rides with its parent (below), never in a bucket of its own.
    if (nest.isNestedShell(s, list)) continue;
    const st = statusFn ? statusFn(s) : s.status;
    // statusFn maps a session that needsYou (waiting, or an expired AWS sign-in) to "waiting".
    const key = st === "waiting" ? "needs-you" : st === "your-turn" ? "your-turn" : st === "working" ? "running" : "idle";
    buckets[key].push(s);
  }
  const labels = { "needs-you": "Needs you", "your-turn": "Done \u00b7 your turn", running: "Running", idle: "Idle" };
  return ["needs-you", "your-turn", "running", "idle"]
    .filter((k) => buckets[k].length)
    .map((k) => {
      // count is the agents; the shells listed after each are not counted.
      const rows = [];
      for (const s of buckets[k]) rows.push(s, ...(kids.get(s.id) || []));
      return { key: k, label: labels[k], count: buckets[k].length, sessions: rows };
    });
}

// #111: a toast sits at the viewport bottom, which on a phone is the snippet
// row, the key bar and the nav. mobileToastBottom is the offset from the viewport
// bottom that clears whatever of them is showing: `tops` are the rects' top edges
// (a screen shows a different set, and the soft keyboard removes the nav), the
// highest one wins, `gap` is the air above it. A top that is not a positive
// number inside the viewport (a hidden element reports 0) is no chrome at all;
// null when none is left, so the caller keeps the CSS fallback. showToast (shell.js)
// reads the rects and applies the number.
function mobileToastBottom(innerHeight, tops, gap) {
  if (typeof innerHeight !== "number" || !Number.isFinite(innerHeight) || !Array.isArray(tops)) return null;
  const valid = tops.filter((t) => typeof t === "number" && Number.isFinite(t) && t > 0 && t <= innerHeight);
  if (!valid.length) return null;
  return innerHeight - Math.min(...valid) + gap;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    MOBILE_MAX_PX, MOBILESHELL_KEY, MOBILE_SCREENS,
    mobileActive, mobileMediaQuery, effectiveMaximized, loadScreen, saveScreen, screenClass, navModel, sessionGroups,
    mobileToastBottom,
  };
}
