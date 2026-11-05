"use strict";
// Staleness is per reading, not per account: acctBestLimits can show a weekly
// number from a model that last reported two days ago next to a five-hour number
// that is a minute old, and only the old one may be marked old.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { acctOptionLabel, acctBestLimits, acctWinAge, acctAgeNote } = require("../static/accounts.js");
const { meterModel, fmtCountdown } = require("../static/usage.js");

const NOW = 1_000_000;
const DAY = 86400;
const win = (pct, resets) => ({ used_percentage: pct, resets_at: resets });
const seen = (pct, resets, at) => ({ used_percentage: pct, resets_at: resets, updated_at: at });

// The reviewer's case: a fresh opus reading (9% five-hour, 76% week) and a haiku
// weekly reading of 94% that is two days old but still inside its window.
const lim = {
  updated_at: NOW - 60,
  five_hour: win(9, NOW + 3600),
  seven_day: win(76, NOW + 5 * DAY),
  seen: { seven_day: [seen(94, NOW + 3 * DAY, NOW - 2 * DAY), seen(76, NOW + 5 * DAY, NOW - 60)] },
};

test("acctBestLimits carries each window's own updated_at", () => {
  const b = acctBestLimits(lim, NOW);
  assert.equal(b.five_hour.updated_at, NOW - 60);
  assert.equal(b.seven_day.used_percentage, 94);
  assert.equal(b.seven_day.updated_at, NOW - 2 * DAY);
  // the input is not touched
  assert.equal(lim.five_hour.updated_at, undefined);
});

test("acctWinAge: only the two-day-old weekly reading is marked old, not the fresh five-hour one", () => {
  const b = acctBestLimits(lim, NOW);
  assert.equal(acctWinAge(b.five_hour, b, NOW).stale, false);
  assert.equal(acctAgeNote(acctWinAge(b.five_hour, b, NOW)), "");
  assert.equal(acctWinAge(b.seven_day, b, NOW).stale, true);
  assert.equal(acctAgeNote(acctWinAge(b.seven_day, b, NOW)), "48h old");
});

test("acctWinAge falls back to the snapshot's updated_at for a window with none of its own", () => {
  const legacy = { updated_at: NOW - 6 * 3600, five_hour: win(5, NOW + 60) };
  assert.deepEqual(acctWinAge(legacy.five_hour, legacy, NOW), { text: "6h ago", stale: true });
});

test("acctOptionLabel (spawn picker) puts the age on the window that is old", () => {
  assert.equal(acctOptionLabel("work", lim, NOW), "work · 5h 9% · wk 94% (48h old)");
});

test("acctOptionLabel gives each stale window its own age when they differ", () => {
  const l = {
    updated_at: NOW - 6 * 3600, five_hour: win(9, NOW + 3600), seven_day: win(76, NOW + 5 * DAY),
    seen: { seven_day: [seen(94, NOW + 3 * DAY, NOW - 2 * DAY)] },
  };
  assert.equal(acctOptionLabel("work", l, NOW), "work · 5h 9% (6h old) · wk 94% (48h old)");
});

test("acctOptionLabel keeps one trailing age when every shown window is equally old", () => {
  const l = { updated_at: NOW - 6 * 3600, five_hour: win(42, NOW + 3600), seven_day: win(7, NOW + 5 * DAY) };
  assert.equal(acctOptionLabel("work", l, NOW), "work · 5h 42% · wk 7% (6h old)");
});

test("acctOptionLabel gives a window past its reset no age at all", () => {
  const l = { updated_at: NOW - 6 * 3600, five_hour: win(42, NOW - 60), seven_day: win(7, NOW + 5 * DAY) };
  assert.equal(acctOptionLabel("work", l, NOW), "work · 5h reset · wk 7% (6h old)");
});

// renderRateLimitsInto (app.js) against a tiny fake DOM: each meter row carries
// its own stale class and age text.
const src = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");
const start = src.indexOf("function renderRateLimitsInto(");
assert.ok(start >= 0, "renderRateLimitsInto not found in app.js");
const code = src.slice(start, src.indexOf("\n}\n", start) + 3);

class El {
  constructor(tag) {
    this.tag = tag; this.children = []; this.className = ""; this.text = ""; this.attrs = {}; this.style = {};
    const self = this;
    this.classList = {
      add: (c) => { if (!self.classList.contains(c)) self.className = (self.className + " " + c).trim(); },
      contains: (c) => self.className.split(/\s+/).includes(c),
    };
  }
  set textContent(v) { this.text = String(v); }
  get textContent() { return this.text; }
  append(...cs) { cs.forEach((c) => this.children.push(c)); }
  appendChild(c) { this.children.push(c); return c; }
  replaceChildren() { this.children = []; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
}

function render(rl) {
  const host = new El("div");
  const ctx = {
    document: { createElement: (t) => new El(t) },
    Date: { now: () => NOW * 1000 },
    Math, meterModel, acctWinAge, acctAgeNote,
  };
  vm.runInNewContext(code + "\nthis.out = renderRateLimitsInto(host, rl);", Object.assign(ctx, { host, rl }));
  return host.children;
}

test("renderRateLimitsInto marks only the stale meter row and writes its age as text", () => {
  const [five, week] = render(acctBestLimits(lim, NOW));
  assert.equal(five.classList.contains("stale"), false);
  assert.equal(five.children.length, 4, "a fresh row has no age span");
  assert.equal(week.classList.contains("stale"), true);
  const age = week.children[4];
  assert.equal(age.className, "acct-age");
  assert.equal(age.textContent, "48h old");
  assert.match(week.attrs["aria-label"], /48h old/);
});

test("renderRateLimitsInto does not mark a window past its reset as stale", () => {
  const l = { updated_at: NOW - 6 * 3600, five_hour: win(42, NOW - 60) };
  const [five] = render(l);
  assert.equal(five.classList.contains("unk"), true);
  assert.equal(five.classList.contains("stale"), false);
  assert.equal(five.children.length, 4);
});
