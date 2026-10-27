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
