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

// Group the session list the way the mobile Sessions screen shows it: the ones
// that need a person first, then the ones doing work, then everything at rest.
// "waiting" is the only needs-you state; "working" is running; anything else
// (idle/done/detached/crashed/unknown) is at rest. Empty groups are dropped so
// no header ever sits above nothing.
function sessionGroups(sessions, statusFn) {
  const buckets = { "needs-you": [], running: [], idle: [] };
  for (const s of sessions || []) {
    const st = statusFn ? statusFn(s) : s.status;
    if (st === "waiting") buckets["needs-you"].push(s);
    else if (st === "working") buckets.running.push(s);
    else buckets.idle.push(s);
  }
  const labels = { "needs-you": "Needs you", running: "Running", idle: "Idle" };
  return ["needs-you", "running", "idle"]
    .filter((k) => buckets[k].length)
    .map((k) => ({ key: k, label: labels[k], count: buckets[k].length, sessions: buckets[k] }));
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    MOBILE_MAX_PX, MOBILESHELL_KEY, MOBILE_SCREENS,
    mobileActive, mobileMediaQuery, loadScreen, saveScreen, screenClass, navModel, sessionGroups,
  };
}
