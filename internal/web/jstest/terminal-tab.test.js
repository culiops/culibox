"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { isBrokenMouseReport, hasSize } = require("../static/terminal-tab.js");

test("an SGR mouse report with NaN coordinates is dropped", () => {
  assert.strictEqual(isBrokenMouseReport("\x1b[<0;NaN;NaNm"), true);
  assert.strictEqual(isBrokenMouseReport("\x1b[<0;NaN;NaNM"), true);
  assert.strictEqual(isBrokenMouseReport("\x1b[<32;NaN;5M"), true);
});

test("a well-formed mouse report is left alone", () => {
  assert.strictEqual(isBrokenMouseReport("\x1b[<0;11;2M"), false);
  assert.strictEqual(isBrokenMouseReport("\x1b[<0;11;2m"), false);
  assert.strictEqual(isBrokenMouseReport("\x1b[<64;138;40M"), false);
});

// Typing N, a, N reaches onData as three one-character chunks, so real input
// cannot be mistaken for a broken report — but pin it anyway.
test("typed text is never treated as a mouse report", () => {
  for (const s of ["N", "a", "NaN", "NaN;NaNm", "echo NaN", ""]) {
    assert.strictEqual(isBrokenMouseReport(s), false, s);
  }
});

test("another escape sequence carrying NaN is not touched", () => {
  assert.strictEqual(isBrokenMouseReport("\x1b[NaN;NaNH"), false); // not a CSI < report
});

test("non-strings never match", () => {
  assert.strictEqual(isBrokenMouseReport(null), false);
  assert.strictEqual(isBrokenMouseReport(undefined), false);
});

test("hasSize is true only for positive width AND height", () => {
  assert.strictEqual(hasSize(100, 40), true);
});

test("hasSize is false when either dimension is zero or negative", () => {
  assert.strictEqual(hasSize(0, 40), false);
  assert.strictEqual(hasSize(100, 0), false);
  assert.strictEqual(hasSize(0, 0), false);
  assert.strictEqual(hasSize(-5, 40), false);
  assert.strictEqual(hasSize(100, -1), false);
});

// --- touch scrolling -------------------------------------------------------
// xterm.js ships no touch handling, and its scroller (.xterm-scrollable-element)
// is a synthetic one: MEASURED on a live session, the element under a finger in
// the middle of the terminal is .xterm-link-layer and NOTHING in its ancestor
// chain is a scrollable box. So a drag has nothing to scroll and the terminal is
// frozen on a phone. These cover the gesture that replaces it.
const { cellHeightOf, touchScrollStep, installTouchScroll } =
  require("../static/terminal-tab.js");

function fakeEl() {
  const ls = {};
  return {
    addEventListener(type, fn) { (ls[type] = ls[type] || []).push(fn); },
    removeEventListener(type, fn) { ls[type] = (ls[type] || []).filter((f) => f !== fn); },
    fire(type, e) { for (const fn of (ls[type] || []).slice()) fn(e); return e; },
    count(type) { return (ls[type] || []).length; },
  };
}
function fakeTerm() {
  return { rows: 24, scrolled: [], scrollLines(n) { this.scrolled.push(n); } };
}
function touches(...ys) { return ys.map((y, i) => ({ identifier: i + 1, clientX: 0, clientY: y })); }
function tev(list) {
  return { touches: list, changedTouches: list, cancelable: true, prevented: false,
           preventDefault() { this.prevented = true; } };
}
// drag runs a whole gesture: down at ys[0], move through the rest, then up.
function drag(el, ys) {
  const evs = [];
  evs.push(el.fire("touchstart", tev(touches(ys[0]))));
  for (const y of ys.slice(1)) evs.push(el.fire("touchmove", tev(touches(y))));
  evs.push(el.fire("touchend", tev([])));
  return evs;
}

test("cellHeightOf divides the screen box by its rows", () => {
  assert.strictEqual(cellHeightOf(340, 20), 17);
});

test("cellHeightOf is 0 when either measurement is missing", () => {
  for (const [px, rows] of [[0, 20], [340, 0], [-5, 20], [340, -1], [NaN, 20]]) {
    assert.strictEqual(cellHeightOf(px, rows), 0, px + "/" + rows);
  }
});

// A finger moving DOWN drags the content down, which means going BACK through
// history — the sign xterm's scrollLines() wants for that is negative.
test("dragging down scrolls back through history, and keeps the remainder", () => {
  assert.deepEqual(touchScrollStep(40, 17), { lines: -2, rest: 6 });
});

test("dragging up scrolls forward", () => {
  assert.deepEqual(touchScrollStep(-40, 17), { lines: 2, rest: -6 });
});

test("a drag shorter than one line scrolls nothing and carries the whole distance", () => {
  assert.deepEqual(touchScrollStep(10, 17), { lines: 0, rest: 10 });
});

test("an unknown cell height scrolls nothing", () => {
  assert.deepEqual(touchScrollStep(40, 0), { lines: 0, rest: 40 });
});

test("a one-finger drag scrolls the buffer and takes the gesture from the browser", () => {
  const el = fakeEl(), term = fakeTerm();
  installTouchScroll(term, el, { cellHeight: () => 17 });
  const evs = drag(el, [300, 290, 270, 240]);
  assert.deepEqual(term.scrolled, [1, 2]); // 10px is under a line; 30 and 60 are not
  assert.ok(evs.slice(1, -1).some((e) => e.prevented), "the move must be preventDefault'd");
});

// A tap is how you focus the terminal and raise the keyboard. It must reach the
// browser untouched.
test("a tap is not a scroll and is never preventDefault'd", () => {
  const el = fakeEl(), term = fakeTerm();
  installTouchScroll(term, el, { cellHeight: () => 17 });
  const evs = drag(el, [300, 302, 301]);
  assert.deepEqual(term.scrolled, []);
  assert.ok(!evs.some((e) => e.prevented));
});

test("two fingers are left alone so pinch-zoom still works", () => {
  const el = fakeEl(), term = fakeTerm();
  installTouchScroll(term, el, { cellHeight: () => 17 });
  el.fire("touchstart", tev(touches(300, 400)));
  const e = el.fire("touchmove", tev(touches(240, 460)));
  assert.deepEqual(term.scrolled, []);
  assert.ok(!e.prevented);
});

// Without a reset the next touchstart would measure against the last gesture's
// position and jump the buffer by the distance between two taps.
test("each gesture starts from its own finger position", () => {
  const el = fakeEl(), term = fakeTerm();
  installTouchScroll(term, el, { cellHeight: () => 17 });
  drag(el, [300, 240]);
  term.scrolled.length = 0;
  drag(el, [600, 602]); // a tap far from where the last drag ended
  assert.deepEqual(term.scrolled, []);
});

test("a cancelled touch ends the gesture", () => {
  const el = fakeEl(), term = fakeTerm();
  installTouchScroll(term, el, { cellHeight: () => 17 });
  el.fire("touchstart", tev(touches(300)));
  el.fire("touchcancel", tev([]));
  const e = el.fire("touchmove", tev(touches(200)));
  assert.deepEqual(term.scrolled, []);
  assert.ok(!e.prevented);
});

// Nothing can be scrolled without a cell height, so swallowing the gesture would
// only break the page's own behaviour for no gain.
test("an unmeasurable terminal leaves the gesture to the browser", () => {
  const el = fakeEl(), term = fakeTerm();
  installTouchScroll(term, el, { cellHeight: () => 0 });
  const evs = drag(el, [300, 240, 200]);
  assert.deepEqual(term.scrolled, []);
  assert.ok(!evs.some((e) => e.prevented));
});

test("dispose removes every listener it added", () => {
  const el = fakeEl(), term = fakeTerm();
  const dispose = installTouchScroll(term, el, { cellHeight: () => 17 });
  dispose();
  for (const t of ["touchstart", "touchmove", "touchend", "touchcancel"]) {
    assert.strictEqual(el.count(t), 0, t);
  }
});

// clientHeight forces a layout flush, and touchmove fires at frame rate on a
// phone. The line height cannot change mid-gesture, so measure it once.
test("the line height is measured once per gesture, not on every move", () => {
  const el = fakeEl(), term = fakeTerm();
  let calls = 0;
  installTouchScroll(term, el, { cellHeight: () => { calls++; return 17; } });
  drag(el, [300, 280, 260, 240, 220]);
  assert.strictEqual(calls, 1);
  drag(el, [300, 240]);
  assert.strictEqual(calls, 2, "a new gesture measures again — the pane may have been resized");
});
