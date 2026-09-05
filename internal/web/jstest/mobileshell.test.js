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
