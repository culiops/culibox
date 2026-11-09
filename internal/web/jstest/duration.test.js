"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { fmtDuration } = require("../static/duration.js");
const { actAge, outcomeText, activityModel } = require("../static/activitymodel.js");
const { startedText } = require("../static/steerrail.js");
const { accessModel } = require("../static/accessmodel.js");

// F25: one formatter for "how long". Seconds under a minute, minutes under an
// hour, hours and minutes under 48h, then days and hours. The day unit starts at
// 48h, not 24h: "30h 0m" is still a number a reader can hold, "1d 6h" is not
// better, and "277h 53m" is the failure this replaces.

test("fmtDuration: seconds, minutes, hours and minutes", () => {
  assert.strictEqual(fmtDuration(0), "0s");
  assert.strictEqual(fmtDuration(59), "59s");
  assert.strictEqual(fmtDuration(60), "1m");
  assert.strictEqual(fmtDuration(3599), "59m");
  assert.strictEqual(fmtDuration(3600), "1h 0m");
  assert.strictEqual(fmtDuration(47 * 3600 + 59 * 60), "47h 59m");
});

test("fmtDuration: days start at 48 hours", () => {
  assert.strictEqual(fmtDuration(48 * 3600), "2d 0h");
  assert.strictEqual(fmtDuration(11 * 86400 + 13 * 3600 + 5), "11d 13h");
});

test("fmtDuration: a negative or non-finite input is 0s, never a throw", () => {
  assert.strictEqual(fmtDuration(-5), "0s");
  assert.strictEqual(fmtDuration(NaN), "0s");
  assert.strictEqual(fmtDuration(Infinity), "0s");
  assert.strictEqual(fmtDuration(undefined), "0s");
});

test("fmtDuration: a fractional second floors", () => {
  assert.strictEqual(fmtDuration(59.9), "59s");
  assert.strictEqual(fmtDuration(125.7), "2m");
});

test("actAge delegates: the same words as fmtDuration", () => {
  assert.strictEqual(actAge(48 * 3600), "2d 0h");
  assert.strictEqual(actAge(277 * 3600 + 53 * 60), "11d 13h");
  assert.strictEqual(actAge(30 * 3600), "30h 0m");
  assert.strictEqual(actAge(41 * 60), "41m");
  assert.strictEqual(actAge(-3), "0s");
  assert.strictEqual(outcomeText({ kind: "ask", ts: 1_000 }, 1_000 + 48 * 3600_000), "waiting 2d 0h");
});

test("startedText reads days from 48h, hours and minutes before that", () => {
  const created = "2026-09-05T10:00:00Z";
  const t0 = Date.parse(created);
  const clock = (() => {
    const d = new Date(t0);
    return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
  })();
  assert.strictEqual(startedText(created, t0 + 49 * 3600_000), clock + " · 2d 1h ago");
  assert.strictEqual(startedText(created, t0 + 30 * 3600_000), clock + " · 30h 0m ago");
  assert.strictEqual(startedText(created, t0 + 59_000), clock + " · just now");
  assert.strictEqual(startedText(created, t0 + 60_000), clock + " · 1m ago");
});

// F25 across surfaces: the Activity "Session age" tile, the Access "started" line
// and the steer rail's started line are one number written one way. Under a minute
// the two "started" lines read "just now" on purpose (startedText: a clock a few
// seconds behind the daemon must never read "-1m ago"), while the tile, which is
// the server's own running_s, keeps counting seconds.
test("session age reads the same on Activity, Access and the steer rail", () => {
  const NOW = Date.parse("2026-11-09T12:00:00Z");
  const secs = [35, 41 * 60, 72 * 60, 47 * 3600 + 59 * 60, 48 * 3600, 11 * 86400 + 13 * 3600];
  for (const sec of secs) {
    const created = new Date(NOW - sec * 1000).toISOString();
    const want = fmtDuration(sec);
    const tile = activityModel({
      session: { id: "s1", claude: true }, resp: { events: [], summary: { running_s: sec } }, filter: "all", nowMs: NOW,
    }).tiles.find((t) => t.label === "Session age");
    assert.strictEqual(tile.n, want, "Activity tile at " + sec + "s");
    const access = accessModel({ session: { id: "s1", created }, nowMs: NOW }).containment.started;
    const rail = startedText(created, NOW);
    const tail = sec < 60 ? "just now" : want + " ago";
    assert.ok(access.endsWith(" \u00b7 " + tail), "Access at " + sec + "s: " + access);
    assert.ok(rail.endsWith(" \u00b7 " + tail), "rail at " + sec + "s: " + rail);
    assert.strictEqual(access, rail, "Access and the rail agree at " + sec + "s");
  }
});
