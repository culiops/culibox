// internal/web/jstest/mobileshell.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const m = require("../static/mobileshell.js");

test("mobileActive is true at and below 700, false above", () => {
  assert.equal(m.mobileActive(700), true);
  assert.equal(m.mobileActive(390), true);
  assert.equal(m.mobileActive(701), false);
  assert.equal(m.mobileActive(1440), false);
});

// U8: the matchMedia() breakpoint listener's query must be derived from
// MOBILE_MAX_PX, not a second hard-coded "700" that could drift from it or
// from the CSS's own @media (max-width: 700px).
test("mobileMediaQuery is built from MOBILE_MAX_PX", () => {
  assert.equal(m.mobileMediaQuery(), "(max-width: " + m.MOBILE_MAX_PX + "px)");
  assert.equal(m.mobileMediaQuery(), "(max-width: 700px)");
});

test("loadScreen keeps a known screen, degrades unknown/empty to sessions", () => {
  assert.equal(m.loadScreen("terminal"), "terminal");
  assert.equal(m.loadScreen("files"), "files");
  assert.equal(m.loadScreen("sessions"), "sessions");
  assert.equal(m.loadScreen("bogus"), "sessions");
  assert.equal(m.loadScreen(null), "sessions");
  assert.equal(m.loadScreen(undefined), "sessions");
});

test("saveScreen normalizes before persisting", () => {
  assert.equal(m.saveScreen("files"), "files");
  assert.equal(m.saveScreen("bogus"), "sessions");
});

test("screenClass is the html-class the CSS keys off", () => {
  assert.equal(m.screenClass("terminal"), "mobile-screen-terminal");
  assert.equal(m.screenClass("bogus"), "mobile-screen-sessions");
});

test("navModel flags the active screen and carries a Sessions pip only when >0", () => {
  const nav = m.navModel("terminal", 2);
  assert.deepEqual(nav.map((n) => n.screen), ["sessions", "terminal", "files"]);
  assert.equal(nav.find((n) => n.screen === "terminal").active, true);
  assert.equal(nav.find((n) => n.screen === "sessions").active, false);
  assert.equal(nav.find((n) => n.screen === "sessions").pip, 2);
  assert.equal(nav.find((n) => n.screen === "terminal").pip, 0);
});

test("navModel shows no pip at zero waiting (a dot announcing zero is worse than none)", () => {
  const nav = m.navModel("sessions", 0);
  assert.equal(nav.find((n) => n.screen === "sessions").pip, 0);
});

test("sessionGroups partitions into needs-you / running / idle and drops empties", () => {
  const statusFn = (s) => s.status;
  const sessions = [
    { id: "a", status: "waiting" },
    { id: "b", status: "working" },
    { id: "c", status: "working" },
    { id: "d", status: "detached" },
    { id: "e", status: "crashed" },
  ];
  const g = m.sessionGroups(sessions, statusFn);
  assert.deepEqual(g.map((x) => x.key), ["needs-you", "running", "idle"]);
  assert.equal(g.find((x) => x.key === "needs-you").count, 1);
  assert.equal(g.find((x) => x.key === "running").count, 2);
  assert.equal(g.find((x) => x.key === "idle").count, 2); // detached + crashed
});

test("sessionGroups omits a group with no members", () => {
  const g = m.sessionGroups([{ id: "a", status: "working" }], (s) => s.status);
  assert.deepEqual(g.map((x) => x.key), ["running"]);
});

// v0.2.0 mobile list, frozen: a list without parent_id must group exactly so.
test("sessionGroups without parent_id is byte-identical to v0.2.0 (mobile parity)", () => {
  const sessions = [
    { id: "a", status: "detached" },
    { id: "b", status: "working" },
    { id: "c", status: "waiting" },
    { id: "d", status: "crashed" },
    { id: "e", status: "working" },
    { id: "f", status: "waiting" },
    { id: "g", status: "idle" },
  ];
  const g = m.sessionGroups(sessions, (s) => s.status);
  assert.deepEqual(g, [
    { key: "needs-you", label: "Needs you", count: 2, sessions: [sessions[2], sessions[5]] },
    { key: "running", label: "Running", count: 2, sessions: [sessions[1], sessions[4]] },
    { key: "idle", label: "Idle", count: 3, sessions: [sessions[0], sessions[3], sessions[6]] },
  ]);
  const noStatusFn = m.sessionGroups([{ id: "x", status: "working" }]);
  assert.deepEqual(noStatusFn.map((x) => x.key), ["running"]);
  assert.deepEqual(m.sessionGroups(null), []);
});

test("sessionGroups nests a shell right after its parent, in the parent's group", () => {
  const p = { id: "p", status: "idle" };
  const other = { id: "o", status: "idle" };
  const child = { id: "c", status: "working", parent_id: "p", created: "2026-10-27T09:00:00Z" };
  const g = m.sessionGroups([child, p, other], (s) => s.status);
  assert.deepEqual(g.map((x) => x.key), ["idle"], "the running shell does not make a Running group");
  assert.deepEqual(g[0].sessions.map((s) => s.id), ["p", "c", "o"]);
});

test("sessionGroups: group count excludes nested shells", () => {
  const p = { id: "p", status: "working" };
  const c1 = { id: "c1", status: "idle", parent_id: "p", created: "2026-10-27T09:00:00Z" };
  const c2 = { id: "c2", status: "waiting", parent_id: "p", created: "2026-10-27T09:05:00Z" };
  const g = m.sessionGroups([p, c2, c1], (s) => s.status);
  assert.equal(g.length, 1);
  assert.equal(g[0].count, 1);
  assert.deepEqual(g[0].sessions.map((s) => s.id), ["p", "c1", "c2"], "children in created order");
});

test("sessionGroups: an orphan shell is a normal row bucketed by its own status", () => {
  const p = { id: "p", status: "idle" };
  const orphan = { id: "o", status: "waiting", parent_id: "gone" };
  const g = m.sessionGroups([p, orphan], (s) => s.status);
  assert.deepEqual(g.map((x) => x.key), ["needs-you", "idle"]);
  assert.deepEqual(g[0].sessions, [orphan]);
});

// I1: mobile has no un-maximize control, so the grid must ignore a persisted
// `maximized` cell while mobile is active — but must still honour it on
// desktop, where the ❐ control lives and restore-on-refresh is the point.
test("effectiveMaximized: mobile always sees no maximized cell", () => {
  const cell = { id: "c1" };
  assert.equal(m.effectiveMaximized(cell, true), null);
  assert.equal(m.effectiveMaximized(null, true), null);
});

test("effectiveMaximized: desktop passes the maximized cell through unchanged", () => {
  const cell = { id: "c1" };
  assert.equal(m.effectiveMaximized(cell, false), cell);
  assert.equal(m.effectiveMaximized(null, false), null);
});

// #111: a toast sits at the viewport bottom, over the key rows and the nav on a
// phone. mobileToastBottom is the pixel offset from the viewport bottom that
// clears the visible bottom chrome: innerHeight - (the highest top) + gap.
test("mobileToastBottom clears the highest visible chrome top by the gap", () => {
  // The measured 390x800 Terminal screen: snippets row 627, key bar 683, nav 740.
  assert.equal(m.mobileToastBottom(800, [627, 683, 740], 8), 181);
  assert.equal(m.mobileToastBottom(800, [740, 683, 627], 8), 181); // order does not matter
  assert.equal(m.mobileToastBottom(800, [740], 8), 68);            // Sessions/Files screen: nav only
  assert.equal(m.mobileToastBottom(800, [683], 0), 117);           // gap is added as given
});

test("mobileToastBottom is null when there is no usable top", () => {
  assert.equal(m.mobileToastBottom(800, [], 8), null);
  assert.equal(m.mobileToastBottom(800, [0, NaN, 900], 8), null);  // 0, NaN, below the viewport
  assert.equal(m.mobileToastBottom(800, [-5, Infinity, "740", null, undefined], 8), null);
  assert.equal(m.mobileToastBottom(800, undefined, 8), null);
});

test("mobileToastBottom ignores the invalid tops and keeps the valid ones", () => {
  assert.equal(m.mobileToastBottom(800, [0, NaN, 900, 740], 8), 68);
  assert.equal(m.mobileToastBottom(800, [NaN, 683, -1], 8), 125);
});

test("mobileToastBottom: a top at the viewport bottom (a zero-height chrome edge) leaves just the gap", () => {
  assert.equal(m.mobileToastBottom(800, [800], 8), 8);
  assert.equal(m.mobileToastBottom(800, [800, 900], 8), 8);
});

// showToast places itself above the phone's bottom chrome (#111). The DOM here is
// a stand-in: each element has the rect and the "is it displayed" answer the real
// one would give (getClientRects is empty for display: none).
function toastHarness(width, height, chrome) {
  const box = { style: {}, classList: { toggle() {}, add() {}, remove() {} } };
  const els = {
    toast: box,
    "toast-msg": {},
    "toast-undo": { hidden: true },
  };
  for (const [id, c] of Object.entries(chrome)) {
    els[id] = {
      getClientRects: () => (c.shown ? [{}] : []),
      getBoundingClientRect: () => ({ top: c.top, bottom: c.top + c.h, height: c.h }),
    };
  }
  global.document = { getElementById: (id) => els[id] || null };
  global.window = { innerWidth: width, innerHeight: height };
  global.mobileActive = m.mobileActive;
  global.mobileToastBottom = m.mobileToastBottom;
  return box;
}
function toastCleanup() {
  for (const k of ["document", "window", "mobileActive", "mobileToastBottom"]) delete global[k];
}
const { showToast, hideToast } = require("../static/shell.js");
const PHONE_TERMINAL = {
  "term-snips": { shown: true, top: 627, h: 56 },
  "term-keybar": { shown: true, top: 683, h: 57 },
  "mobile-nav": { shown: true, top: 740, h: 60 },
};

test("showToast on a phone sits above the highest visible chrome", () => {
  const box = toastHarness(390, 800, PHONE_TERMINAL);
  try {
    showToast("hello", { sticky: true });
    assert.equal(box.style.bottom, "181px");
  } finally { hideToast(); toastCleanup(); }
});

test("showToast on a phone skips chrome that is not displayed", () => {
  const box = toastHarness(390, 800, {
    "term-snips": { shown: false, top: 0, h: 0 },
    "term-keybar": { shown: false, top: 0, h: 0 },
    "mobile-nav": { shown: true, top: 740, h: 60 },
  });
  try {
    showToast("hello", { sticky: true });
    assert.equal(box.style.bottom, "68px"); // Sessions / Files screen: the nav only
    // The soft keyboard removes the nav too: nothing to clear, so the CSS fallback stands.
    toastHarness(390, 800, {
      "term-snips": { shown: false, top: 0, h: 0 },
      "term-keybar": { shown: true, top: 683, h: 57 },
      "mobile-nav": { shown: false, top: 0, h: 0 },
    });
    const kbd = global.document.getElementById("toast");
    kbd.style.bottom = "stale";
    showToast("hello", { sticky: true });
    assert.equal(kbd.style.bottom, "125px");
  } finally { hideToast(); toastCleanup(); }
});

test("showToast on a phone with no chrome measured clears the inline bottom (the CSS fallback applies)", () => {
  const box = toastHarness(390, 800, {});
  box.style.bottom = "181px";
  try {
    showToast("hello", { sticky: true });
    assert.equal(box.style.bottom, "");
  } finally { hideToast(); toastCleanup(); }
});

test("showToast above the phone breakpoint leaves the desktop position alone", () => {
  const box = toastHarness(1280, 900, PHONE_TERMINAL);
  box.style.bottom = "181px"; // left over from a phone-width toast before the window grew
  try {
    showToast("hello", { sticky: true });
    assert.equal(box.style.bottom, "");
  } finally { hideToast(); toastCleanup(); }
});

test("showToast without the mobile shell loaded (shell.js loads first) does not throw", () => {
  const box = toastHarness(390, 800, PHONE_TERMINAL);
  delete global.mobileActive;
  try {
    showToast("hello", { sticky: true });
    assert.equal(box.style.bottom, "");
  } finally { hideToast(); toastCleanup(); }
});
