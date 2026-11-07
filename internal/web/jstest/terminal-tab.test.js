"use strict";
const test = require("node:test");
const assert = require("node:assert");
// classifyTermPath normalises through fsops.js's global, as in the browser.
global.normalizePath = require("../static/fsops.js").normalizePath;
const { isBrokenMouseReport, hasSize, resolveTermPath, classifyTermPath, termKeyAction, termTheme } = require("../static/terminal-tab.js");

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
const { cellHeightOf, touchScrollStep, installTouchScroll, emitWheel } =
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

// --- apps that own the mouse ----------------------------------------------
// MEASURED on a live claude session: buffer.type === "alternate", length ===
// rows (zero scrollback), modes.mouseTrackingMode === "any". scrollLines() has
// nothing to scroll there. The wheel works because xterm turns it into
// ESC[<64;x;yM and the APP scrolls itself — verified: 6 notches moved claude's
// transcript from line 172 to 165. A finger has to do the same.
function trackingTerm() {
  const t = fakeTerm();
  t.modes = { mouseTrackingMode: "any" };
  return t;
}

test("a mouse-tracking app is sent wheel notches instead of a buffer scroll", () => {
  const el = fakeEl(), term = trackingTerm(), wheels = [];
  installTouchScroll(term, el, { cellHeight: () => 17, wheel: (lines) => wheels.push(lines) });
  drag(el, [300, 360]); // 60px down = 3 lines back
  assert.deepEqual(wheels, [-3]);
  assert.deepEqual(term.scrolled, [], "the alternate buffer has nothing to scroll");
});

test("a terminal whose app ignores the mouse still scrolls its own buffer", () => {
  const el = fakeEl(), term = fakeTerm(), wheels = [];
  term.modes = { mouseTrackingMode: "none" };
  installTouchScroll(term, el, { cellHeight: () => 17, wheel: (lines) => wheels.push(lines) });
  drag(el, [300, 360]);
  assert.deepEqual(term.scrolled, [-3]);
  assert.deepEqual(wheels, []);
});

test("wheel notches carry the drag direction", () => {
  const el = fakeEl(), term = trackingTerm(), wheels = [];
  installTouchScroll(term, el, { cellHeight: () => 17, wheel: (lines) => wheels.push(lines) });
  drag(el, [300, 240]); // dragging up = forward
  assert.deepEqual(wheels, [3]);
});

// node has no WheelEvent; emitWheel builds real ones in the browser. The shim
// only has to carry deltaY — what is under test is the count and the direction.
if (typeof globalThis.WheelEvent !== "function") {
  globalThis.WheelEvent = class WheelEvent { constructor(type, init) { Object.assign(this, { type }, init); } };
}

test("emitWheel sends one notch per line, in the drag's direction", () => {
  const sent = [];
  const term = { element: { querySelector: () => ({ dispatchEvent: (e) => sent.push(e.deltaY) }) } };
  emitWheel(term, -3, 10, 20);
  assert.deepEqual(sent, [-120, -120, -120]);
  sent.length = 0;
  emitWheel(term, 2, 10, 20);
  assert.deepEqual(sent, [120, 120]);
});

// A single touchmove reporting a huge jump (a fling, or a coalesced event) must
// not turn into a hundred escape sequences on the wire at once.
test("emitWheel caps the notches one move can produce", () => {
  const sent = [];
  const term = { element: { querySelector: () => ({ dispatchEvent: (e) => sent.push(e.deltaY) }) } };
  emitWheel(term, -500, 0, 0);
  assert.strictEqual(sent.length, 20);
});

// --- mobile: latched ctrl ---------------------------------------------------
// The DOM/WS wiring (onData's latch branch) is smoke-tested on DEV; this pins
// the intended byte so a future edit to ctrlByte can't silently change what a
// latched key sends without a test noticing.
test("latched-ctrl transform matches what onData will send", () => {
  const mt = require("../static/mobileterm.js");
  // The onData latch sends ctrlByte(d) for a 1-char payload, else the char as-is.
  assert.equal(mt.ctrlByte("c"), "\x03"); // ^C via keyboard while armed
  assert.equal(mt.ctrlByte("d"), "\x04"); // ^D
  assert.equal(mt.ctrlByte("5"), "");     // digit -> sent literally, not a byte
});

// --- mobile: swipe gesture math ---------------------------------------------
// installSwipe (below) is built entirely on these two pure helpers; pinning
// the math here, beside the touch-scroll tests it shares a gesture with,
// keeps the axis-lock and threshold behavior visible from this file too.
test("swipe gesture: horizontal past slop + past threshold picks prev/next", () => {
  const mt = require("../static/mobileterm.js");
  assert.equal(mt.gestureAxis(30, 6, 8), "horizontal"); // committed to swipe
  assert.equal(mt.gestureAxis(6, 30, 8), "vertical");   // committed to scroll
  assert.equal(mt.swipeIntent(-70, 50), "next");        // dragged left
  assert.equal(mt.swipeIntent(70, 50), "prev");
  assert.equal(mt.swipeIntent(-30, 50), null);          // not far enough -> no switch
});

// A Claude session that entered a worktree prints paths relative to the
// worktree, while its spawn cwd is still the main checkout. Joining onto the
// spawn cwd 404s a worktree-only file and silently opens the MAIN copy of one
// that exists in both.
test("a relative path resolves against the agent's live cwd when known", () => {
  assert.strictEqual(resolveTermPath("docs/a.md", "/r", "/r/.claude/worktrees/wt", "/h"), "/r/.claude/worktrees/wt/docs/a.md");
  assert.strictEqual(resolveTermPath("./docs/a.md", "/r", "/r/wt/", "/h"), "/r/wt/docs/a.md");
});

test("a relative path falls back to the spawn cwd without a live cwd", () => {
  assert.strictEqual(resolveTermPath("docs/a.md", "/r/", "", "/h"), "/r/docs/a.md");
  assert.strictEqual(resolveTermPath("docs/a.md", "", "", "/h"), "docs/a.md");
});

test("absolute and home paths ignore both cwds", () => {
  assert.strictEqual(resolveTermPath("/etc/x", "/r", "/r/wt", "/h"), "/etc/x");
  assert.strictEqual(resolveTermPath("~/x.md", "/r", "/r/wt", "/h"), "/h/x.md");
  assert.strictEqual(resolveTermPath("~/x.md", "/r", "/r/wt", ""), "~/x.md");
});

// classifyTermPath: where a clicked path lives for this session. A boxed
// session's `~` and /tmp are the BOX's, not the host's.
test("classifyTermPath: host session unchanged", () => {
  assert.deepStrictEqual(classifyTermPath("~/a.md", "/r", "", "/h", false), { kind: "host", path: "/h/a.md" });
  assert.deepStrictEqual(classifyTermPath("/tmp/playwright-mcp/p.png", "/r", "", "/h", false), { kind: "host", path: "/tmp/playwright-mcp/p.png" });
});

test("classifyTermPath: boxed, ~ is the box home", () => {
  assert.deepStrictEqual(classifyTermPath("~/a.md", "/r", "", "/h", true), { kind: "boxonly", path: "/home/agent/a.md" });
});

test("classifyTermPath: boxed, absolute out-dir path", () => {
  assert.deepStrictEqual(classifyTermPath("/tmp/playwright-mcp/page-1.png", "/r", "", "/h", true), { kind: "box", path: "/tmp/playwright-mcp/page-1.png" });
});

test("classifyTermPath: boxed, ../ prefix maps to the out dir at any depth", () => {
  const want = { kind: "box", path: "/tmp/playwright-mcp/p.png" };
  assert.deepStrictEqual(classifyTermPath("../../tmp/playwright-mcp/p.png", "/r", "/r/.claude/worktrees/w", "/h", true), want);
  assert.deepStrictEqual(classifyTermPath("../../../../../../tmp/playwright-mcp/p.png", "/r", "/r/.claude/worktrees/w", "/h", true), want);
});

test("classifyTermPath: boxed, other /tmp and /home/agent are boxonly", () => {
  assert.strictEqual(classifyTermPath("/tmp/claude-1001/x.txt", "/r", "", "/h", true).kind, "boxonly");
  assert.strictEqual(classifyTermPath("/home/agent/.claude.json", "/r", "", "/h", true).kind, "boxonly");
});

test("classifyTermPath: boxed, a workspace under /tmp wins over the toast", () => {
  assert.deepStrictEqual(classifyTermPath("/tmp/ab-x/src/a.go", "/tmp/ab-x", "", "/h", true), { kind: "host", path: "/tmp/ab-x/src/a.go" });
  assert.deepStrictEqual(classifyTermPath("src/a.go", "/tmp/ab-x", "", "/h", true), { kind: "host", path: "/tmp/ab-x/src/a.go" });
  // the live agent cwd counts too
  assert.strictEqual(classifyTermPath("/tmp/ab-x/wt/a.go", "/tmp/ab-x", "/tmp/ab-x/wt", "/h", true).kind, "host");
});

test("classifyTermPath: boxed, a host workspace path stays host", () => {
  assert.deepStrictEqual(classifyTermPath("docs/a.md", "/home/u/repo", "", "/h", true), { kind: "host", path: "/home/u/repo/docs/a.md" });
});

test("classifyTermPath: boxed, traversal out of the out dir is not box", () => {
  assert.deepStrictEqual(classifyTermPath("/tmp/playwright-mcp/../x.png", "/r", "", "/h", true), { kind: "boxonly", path: "/tmp/x.png" });
});

test("classifyTermPath: a sibling dir sharing the cwd prefix is not the workspace", () => {
  assert.strictEqual(classifyTermPath("/tmp/ab-xyz/a.go", "/tmp/ab-x", "", "/h", true).kind, "boxonly");
});

// The usage poll is the only thing that learns the agent entered (or left) a
// git worktree. When agentCwd moves, the tree has to hear about it: until the
// next focus change it would otherwise keep listing the main checkout (#49).
test("setUsage re-syncs the tree exactly when the agent's worktree changes", () => {
  const { TerminalTab } = require("../static/terminal-tab.js");
  const tab = Object.create(TerminalTab.prototype);
  tab.agentCwd = "";
  let syncs = 0;
  global.syncFocusedTree = () => { syncs++; };
  try {
    const WT = "/home/u/repo/.claude/worktrees/t49";
    tab.setUsage({ contextTokens: 1, model: "m", effort: "low", cwd: WT });
    assert.strictEqual(tab.agentCwd, WT);
    assert.strictEqual(syncs, 1, "entering a worktree re-syncs");
    tab.setUsage({ contextTokens: 2, model: "m", effort: "low", cwd: WT });
    assert.strictEqual(syncs, 1, "a poll that only moves the numbers does not");
    // A 204 (no usage line in the transcript tail: right after /clear, or after
    // a tool result over 256 KiB) says nothing about where the agent is. Taking
    // it as "left the worktree" flipped the tree to the main checkout and back,
    // dropping a root the user picked by hand.
    tab.setUsage(null);
    assert.strictEqual(tab.agentCwd, WT, "a 204 keeps the last known worktree");
    assert.strictEqual(syncs, 1, "and does not re-sync");
    tab.setUsage({ contextTokens: 3, model: "m", effort: "low", cwd: "" });
    assert.strictEqual(tab.agentCwd, "");
    assert.strictEqual(syncs, 2, "leaving it re-syncs");
  } finally {
    delete global.syncFocusedTree;
  }
});

// #50: a box-only click says what IS shared, not just "no".
test("box-only click toast says only /tmp/playwright-mcp is shared", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/terminal-tab.js"), "utf8");
  assert.ok(src.includes('showToast("This file is inside the box only — just /tmp/playwright-mcp is shared out")'));
  assert.ok(!src.includes("This file is only inside the box"));
});

// --- termKeyAction: what the xterm key handler does with a keydown ---------
const kev = (code, m) => Object.assign({ type: "keydown", code, key: code.replace(/^Key/, "").toLowerCase(),
  ctrlKey: false, shiftKey: false, altKey: false, metaKey: false, isComposing: false }, m);

test("termKeyAction: Ctrl+Shift+C off mac copies a selection, else is consumed", () => {
  const e = kev("KeyC", { ctrlKey: true, shiftKey: true });
  assert.strictEqual(termKeyAction(e, false, true), "copy");
  assert.strictEqual(termKeyAction(e, false, false), "consume");
});

test("termKeyAction: Cmd+C on mac copies a selection, else reaches the PTY", () => {
  const e = kev("KeyC", { metaKey: true });
  assert.strictEqual(termKeyAction(e, true, true), "copy");
  assert.strictEqual(termKeyAction(e, true, false), "pass");
});

test("termKeyAction: Cmd+Shift+C on mac with a selection copies", () => {
  assert.strictEqual(termKeyAction(kev("KeyC", { metaKey: true, shiftKey: true }), true, true), "copy");
});

test("termKeyAction: Ctrl+V pastes off mac and reaches the PTY on mac", () => {
  const e = kev("KeyV", { ctrlKey: true });
  assert.strictEqual(termKeyAction(e, false, false), "paste");
  assert.strictEqual(termKeyAction(e, true, false), "pass");
});

test("termKeyAction: Ctrl+Shift+V off mac is not swallowed", () => {
  assert.strictEqual(termKeyAction(kev("KeyV", { ctrlKey: true, shiftKey: true }), false, false), "pass");
});

test("termKeyAction: TUI keys always reach the PTY", () => {
  for (const mac of [false, true]) {
    for (const sel of [false, true]) {
      assert.strictEqual(termKeyAction(kev("KeyC", { ctrlKey: true }), mac, sel), "pass", "Ctrl+C");
      assert.strictEqual(termKeyAction(kev("KeyR", { ctrlKey: true }), mac, sel), "pass", "Ctrl+R");
      assert.strictEqual(termKeyAction(kev("KeyW", { ctrlKey: true }), mac, sel), "pass", "Ctrl+W");
      assert.strictEqual(termKeyAction(kev("KeyB", { altKey: true }), mac, sel), "pass", "Alt+B");
      assert.strictEqual(termKeyAction(kev("Escape", { key: "Escape" }), mac, sel), "pass", "Esc");
    }
  }
});

test("termKeyAction: AltGr and IME composition never copy or paste", () => {
  const ag = kev("KeyV", { ctrlKey: true, altKey: true });
  assert.strictEqual(termKeyAction(ag, false, true), "pass");
  const gm = Object.assign(kev("KeyV", { ctrlKey: true }), { getModifierState: (k) => k === "AltGraph" });
  assert.strictEqual(termKeyAction(gm, false, true), "pass");
  assert.strictEqual(termKeyAction(kev("KeyC", { ctrlKey: true, shiftKey: true, isComposing: true }), false, true), "pass");
});

// The key handler runs on EVERY keydown. getSelection() builds the whole
// selection string; hasSelection() is a cheap boolean. Only the copy branch
// needs the text.
test("the terminal key handler asks hasSelection(), and reads getSelection() only to copy", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/terminal-tab.js"), "utf8");
  const m = /attachCustomKeyEventHandler\(\(e\) => \{([\s\S]*?)\n    \}\);/.exec(src);
  assert.ok(m, "key handler found");
  const body = m[1];
  assert.match(body, /termKeyAction\(e, IS_MAC, this\.term\.hasSelection\(\)\)/);
  assert.strictEqual((body.match(/getSelection\(\)/g) || []).length, 1, "getSelection once, in the copy branch");
  assert.ok(body.indexOf("getSelection()") > body.indexOf('act === "copy"'));
});

// #53: a relative token carries {base, rel} so the viewer can look for the file
// in the repo's OTHER checkouts when the first read is a 404. Whether the file
// is in the main checkout or a worktree depends on where the agent was when it
// printed the path, which the click cannot know: the lookup is symmetric, so
// every relative host token gets it, with or without a live agentCwd.
function clickWith(tok, cwd, agentCwd, opts, boxed) {
  const { TerminalTab } = require("../static/terminal-tab.js");
  const tab = Object.create(TerminalTab.prototype);
  tab.cwd = cwd; tab.agentCwd = agentCwd; tab.runtime = boxed ? "box" : "";
  const calls = [];
  global.window = { HOME_DIR: "/h" };
  global.openFile = (p, o) => calls.push([p, o]);
  try { tab.openClickedPath(tok, opts); } finally { delete global.openFile; delete global.window; }
  return calls;
}

test("a relative token passes alt with no agentCwd: base is the spawn cwd", () => {
  const calls = clickWith("./docs/a.md", "/r/", "", { line: 3, col: 2 });
  assert.deepStrictEqual(calls, [["/r/docs/a.md", { line: 3, col: 2, alt: { base: "/r", rel: "docs/a.md" } }]]);
});

test("a relative token passes alt with agentCwd set: base is the agent's live cwd", () => {
  const calls = clickWith("docs/a.md", "/r", "/r/.claude/worktrees/w", undefined);
  assert.deepStrictEqual(calls, [["/r/.claude/worktrees/w/docs/a.md", { alt: { base: "/r/.claude/worktrees/w", rel: "docs/a.md" } }]]);
});

test("a :line:col suffix left on the token is not part of rel", () => {
  const calls = clickWith("src/a.go:12:5", "/r", "", undefined);
  assert.strictEqual(calls[0][1].alt.rel, "src/a.go");
});

test("absolute and home tokens carry no alt, and the caller's opts are not mutated", () => {
  assert.deepStrictEqual(clickWith("/etc/x", "/r", "", { line: 1 }), [["/etc/x", { line: 1 }]]);
  assert.deepStrictEqual(clickWith("~/x.md", "/r", "/r/w", undefined), [["/h/x.md", undefined]]);
  const o = { line: 4 };
  clickWith("a.md", "/r", "", o);
  assert.deepStrictEqual(o, { line: 4 });
});

test("a relative token with no cwd at all has no base to look up from", () => {
  const calls = clickWith("a.md", "", "", undefined);
  assert.deepStrictEqual(calls, [["a.md", undefined]]);
});

test("a boxed session's workspace-relative token also carries alt", () => {
  const calls = clickWith("docs/a.md", "/home/u/repo", "", undefined, true);
  assert.deepStrictEqual(calls, [["/home/u/repo/docs/a.md", { alt: { base: "/home/u/repo", rel: "docs/a.md" } }]]);
});

// --- WebGL renderers (#127) ---------------------------------------------------
// MEASURED in Chromium: 16 live WebGL contexts per page, and the 17th kills the
// OLDEST, which were the panes on screen while hidden tabs kept theirs. A lost
// pane painted nothing until reload. dispose() alone does not free a slot (the
// context lives until GC); loseContext() does.
const { makeWebglPool, webglCapFor, webglPool, loadWebgl, dropWebgl } = require("../static/terminal-tab.js");

function member(name, shown = true, on = true) {
  return {
    name, isShown: shown, gl: false, offs: 0,
    shown() { return this.isShown; },
    webglOn() { if (on) this.gl = true; return on; },
    webglOff() { this.gl = false; this.offs++; },
  };
}

const DESKTOP_UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36";
const ANDROID_UA = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36";

test("the WebGL cap leaves room for every on-screen pane under the browser's 16", () => {
  assert.ok(webglCapFor(DESKTOP_UA) >= 8, "4 panes, each with a docked shell, are on screen at once");
  assert.ok(webglCapFor(DESKTOP_UA) < 16, "Chromium kills the oldest context past 16");
});

test("on Android the WebGL cap stays under Chromium's 8 there", () => {
  assert.ok(webglCapFor(ANDROID_UA) < 8);
  assert.ok(webglCapFor(ANDROID_UA) >= 1);
  assert.strictEqual(webglCapFor(""), webglCapFor(DESKTOP_UA), "no user agent: the desktop cap");
});

// dropWebgl reaches the context through the vendored addon's private fields.
// If a re-vendor renames them it silently falls back to a plain dispose, the
// slot waits for GC, and #127 comes back under churn: pin them here.
test("the vendored WebGL addon still keeps its context where dropWebgl looks", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/xterm/addon-webgl.js"), "utf8");
  assert.ok(src.includes("this._renderer=this._register(new"), "addon._renderer");
  assert.ok(src.includes('this._gl=this._canvas.getContext("webgl2"'), "renderer._gl");
});

test("a shown tab gets WebGL while the pool has room", () => {
  const pool = makeWebglPool(2);
  const a = member("a");
  assert.strictEqual(pool.acquire(a), true);
  assert.strictEqual(a.gl, true);
  assert.strictEqual(pool.size(), 1);
  assert.strictEqual(pool.acquire(a), true, "showing it again keeps it");
  assert.strictEqual(pool.size(), 1);
});

test("past the cap the least recently shown hidden tab gives its WebGL up", () => {
  const pool = makeWebglPool(2);
  const a = member("a"), b = member("b"), c = member("c");
  pool.acquire(a); pool.acquire(b);
  a.isShown = false; b.isShown = false;
  pool.acquire(a); // a shown again more recently than b
  assert.strictEqual(pool.acquire(c), true);
  assert.strictEqual(b.gl, false, "b was the least recently shown");
  assert.strictEqual(a.gl, true);
  assert.strictEqual(c.gl, true);
  assert.strictEqual(pool.size(), 2);
});

test("a pane on screen never loses its WebGL to a newer tab", () => {
  const pool = makeWebglPool(2);
  const a = member("a"), b = member("b", false), c = member("c");
  pool.acquire(a); pool.acquire(b); // a is older but on screen, b is hidden
  assert.strictEqual(pool.acquire(c), true);
  assert.strictEqual(a.gl, true, "on screen: kept although oldest");
  assert.strictEqual(b.gl, false);
});

test("when every holder is on screen the new tab keeps the DOM renderer", () => {
  const pool = makeWebglPool(2);
  const a = member("a"), b = member("b"), c = member("c");
  pool.acquire(a); pool.acquire(b);
  assert.strictEqual(pool.acquire(c), false);
  assert.strictEqual(c.gl, false);
  assert.strictEqual(a.gl && b.gl, true);
  assert.strictEqual(pool.size(), 2);
});

test("a tab whose WebGL cannot start takes no slot", () => {
  const pool = makeWebglPool(1);
  const bad = member("bad", true, false), a = member("a");
  assert.strictEqual(pool.acquire(bad), false);
  assert.strictEqual(pool.size(), 0);
  assert.strictEqual(pool.acquire(a), true);
});

test("release turns WebGL off and frees the slot", () => {
  const pool = makeWebglPool(1);
  const a = member("a"), b = member("b");
  pool.acquire(a);
  pool.release(a);
  assert.strictEqual(a.gl, false);
  assert.strictEqual(pool.size(), 0);
  assert.strictEqual(pool.acquire(b), true);
  assert.strictEqual(a.offs, 1);
});

function fakeAddonClass(log) {
  return class {
    constructor() { this.disposed = false; this._renderer = { _gl: { getExtension: (n) => (n === "WEBGL_lose_context" ? { loseContext: () => log.push("lose") } : null) } }; }
    onContextLoss(fn) { this.lossFn = fn; return { dispose() {} }; }
    dispose() { this.disposed = true; log.push("dispose"); }
  };
}

test("loadWebgl loads the renderer and reports its context loss", () => {
  const loaded = [];
  const term = { loadAddon(a) { loaded.push(a); } };
  let lost = null;
  const a = loadWebgl(term, fakeAddonClass([]), (x) => { lost = x; });
  assert.ok(a);
  assert.deepStrictEqual(loaded, [a]);
  a.lossFn(new Event("webglcontextlost"));
  assert.strictEqual(lost, a, "the loss names the addon that lost it");
});

test("loadWebgl answers null when the browser has no WebGL2", () => {
  const log = [];
  const term = { loadAddon() { throw new Error("WebGL2 not supported"); } };
  assert.strictEqual(loadWebgl(term, fakeAddonClass(log), () => {}), null);
  assert.deepStrictEqual(log, ["dispose", "lose"], "a half-made renderer is still released");
});

test("dropWebgl disposes the renderer and then frees its context slot", () => {
  const log = [];
  const A = fakeAddonClass(log);
  dropWebgl(new A());
  assert.deepStrictEqual(log, ["dispose", "lose"]);
});

test("dropWebgl tolerates no addon, no renderer and a lost context", () => {
  dropWebgl(null);
  dropWebgl({ dispose() {} });
  dropWebgl({ dispose() {}, _renderer: { _gl: { getExtension: () => null } } });
  dropWebgl({ dispose() { throw new Error("already disposed"); }, _renderer: null });
});

// TerminalTab as a pool member: shown when sized, a lost renderer gives its
// slot back, a late loss from a renderer already dropped changes nothing.
function webglTab() {
  const { TerminalTab } = require("../static/terminal-tab.js");
  const tab = Object.create(TerminalTab.prototype);
  tab.termEl = { clientWidth: 800, clientHeight: 400 };
  tab.term = { rows: 24, cols: 80, loadAddon() {} };
  tab.fitAddon = { fit() {} };
  tab.ws = null;
  tab.closed = false;
  tab.webgl = null;
  tab.webglBroken = false;
  return tab;
}

test("a shown terminal draws with WebGL and falls back when its context is lost", () => {
  global.WebglAddon = { WebglAddon: fakeAddonClass([]) };
  try {
    const tab = webglTab();
    const before = webglPool.size();
    tab.fit();
    assert.ok(tab.webgl, "a sized fit takes a renderer");
    assert.strictEqual(webglPool.size(), before + 1);
    tab.webgl.lossFn();
    assert.strictEqual(tab.webgl, null, "lost: back to the DOM renderer");
    assert.strictEqual(webglPool.size(), before, "and the slot is free");
    tab.fit();
    assert.ok(tab.webgl, "retried the next time it shows");
    webglPool.release(tab);
  } finally { delete global.WebglAddon; }
});

test("a loss reported by a renderer the tab already dropped is ignored", () => {
  global.WebglAddon = { WebglAddon: fakeAddonClass([]) };
  try {
    const tab = webglTab();
    tab.fit();
    const old = tab.webgl;
    webglPool.release(tab);
    tab.fit();
    const now = tab.webgl;
    old.lossFn();
    assert.strictEqual(tab.webgl, now);
    webglPool.release(tab);
  } finally { delete global.WebglAddon; }
});

test("a closed tab, or one whose WebGL already failed, takes no slot and evicts nobody", () => {
  const log = [];
  let made = 0;
  global.WebglAddon = { WebglAddon: class extends fakeAddonClass(log) { constructor() { super(); made++; } } };
  try {
    const before = webglPool.size();
    const broken = webglTab();
    broken.term.loadAddon = () => { throw new Error("WebGL2 not supported"); };
    broken.fit(); // the first try is the only one
    assert.strictEqual(broken.webglBroken, true);
    assert.strictEqual(webglPool.size(), before);
    // A full pool of hidden holders: any acquire would evict one of them.
    const cap = webglCapFor(typeof navigator !== "undefined" ? navigator.userAgent : "");
    const holders = [];
    while (webglPool.size() < cap) {
      const h = member("h" + holders.length, false);
      webglPool.acquire(h);
      holders.push(h);
    }
    const full = webglPool.size();
    const closed = webglTab();
    closed.closed = true;
    made = 0;
    closed.fit();
    broken.fit();
    assert.strictEqual(made, 0, "neither tries to start a renderer");
    assert.strictEqual(webglPool.size(), full);
    assert.ok(holders.every((h) => h.gl), "no hidden holder was evicted for them");
    for (const h of holders) webglPool.release(h);
    assert.strictEqual(webglPool.size(), before);
  } finally { delete global.WebglAddon; }
});

// One resize per size (#126). MEASURED: a size change reached the server three
// times (ResizeObserver, font load, fitAll), 60 messages for 20 sizes. A new
// socket must still send its first: the server repaints the TUI on it.
const { resizeToSend } = require("../static/terminal-tab.js");

test("a socket that has sent nothing sends the size", () => {
  assert.deepStrictEqual(resizeToSend(null, 24, 80), { type: "resize", rows: 24, cols: 80 });
});

test("the size this socket already sent is not sent again", () => {
  assert.strictEqual(resizeToSend({ rows: 24, cols: 80 }, 24, 80), null);
});

test("a changed row or column count is sent", () => {
  assert.deepStrictEqual(resizeToSend({ rows: 24, cols: 80 }, 24, 81), { type: "resize", rows: 24, cols: 81 });
  assert.deepStrictEqual(resizeToSend({ rows: 24, cols: 80 }, 25, 80), { type: "resize", rows: 25, cols: 80 });
});

test("sendResize sends each size once per socket and again on a new one", () => {
  const { TerminalTab } = require("../static/terminal-tab.js");
  const sockets = [];
  global.location = { protocol: "http:", host: "127.0.0.1:8899" };
  global.WebSocket = class {
    constructor() { this.readyState = 0; this.sent = []; sockets.push(this); }
    send(m) { this.sent.push(JSON.parse(m)); }
    close() {}
  };
  global.WebSocket.OPEN = 1;
  try {
    const tab = Object.create(TerminalTab.prototype);
    tab.term = { rows: 24, cols: 80, reset() {}, focus() {} };
    tab.connect();
    sockets[0].readyState = 1;
    sockets[0].onopen();
    tab.sendResize(); tab.sendResize();
    assert.strictEqual(sockets[0].sent.length, 1, "onopen sent the size; repeats are dropped");
    tab.term.cols = 100;
    tab.sendResize(); tab.sendResize();
    assert.strictEqual(sockets[0].sent.length, 2);
    tab.connect(); // reconnect: a new socket must hear the size once
    sockets[1].readyState = 1;
    sockets[1].onopen();
    tab.sendResize();
    assert.deepStrictEqual(sockets[1].sent, [{ type: "resize", rows: 24, cols: 100 }]);
  } finally { delete global.WebSocket; delete global.location; }
});

// termTheme (F05): the xterm theme, built from the kit tokens on a desktop and
// pinned to the old black on a phone. get(name) reads one CSS custom property.
const TOKENS = {
  "--bg": "#0d0f12", "--term-bg": "#0d0f12", "--term-fg": "#e6e9ee", "--term-cursor": "#6ea8fe", "--term-sel": "#283a54",
};
for (let i = 0; i < 16; i++) TOKENS["--ansi-" + i] = "#a0" + (i < 10 ? "0" : "") + i;
const fakeGet = (name) => (name in TOKENS ? TOKENS[name] : "");
const ANSI_NAMES = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
  "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite"];

test("termTheme on a phone is exactly the old black, whatever the tokens say", () => {
  assert.deepStrictEqual(termTheme(fakeGet, true), { background: "#000000" });
  assert.deepStrictEqual(termTheme(() => { throw new Error("a phone never reads a token"); }, true), { background: "#000000" });
});

test("termTheme on a desktop maps the kit tokens to the xterm option names", () => {
  const t = termTheme(fakeGet, false);
  assert.strictEqual(t.background, "#0d0f12");
  assert.strictEqual(t.foreground, "#e6e9ee");
  assert.strictEqual(t.cursor, "#6ea8fe");
  assert.strictEqual(t.cursorAccent, "#0d0f12");
  assert.strictEqual(t.selectionBackground, "#283a54");
  assert.strictEqual(ANSI_NAMES.length, 16);
  ANSI_NAMES.forEach((name, i) => assert.strictEqual(t[name], TOKENS["--ansi-" + i], name + " = --ansi-" + i));
  assert.deepStrictEqual(Object.keys(t).sort(),
    ["background", "foreground", "cursor", "cursorAccent", "selectionBackground", ...ANSI_NAMES].sort());
});

test("termTheme trims a token read from getComputedStyle", () => {
  const t = termTheme((n) => "  " + fakeGet(n) + " ", false);
  assert.strictEqual(t.background, "#0d0f12");
  assert.strictEqual(t.brightWhite, "#a015");
});

test("termTheme leaves out an empty token, and the background falls back to the old black", () => {
  const t = termTheme((n) => (n === "--term-bg" || n === "--ansi-3" || n === "--term-sel" ? "" : fakeGet(n)), false);
  assert.strictEqual(t.background, "#000000");
  assert.ok(!("yellow" in t), "an empty --ansi-3 sets no yellow");
  assert.ok(!("selectionBackground" in t), "an empty --term-sel sets no selection colour");
  assert.strictEqual(t.red, "#a001");
  assert.deepStrictEqual(termTheme(() => "", false), { background: "#000000" });
});

test("termTheme without --bg sets no cursorAccent", () => {
  const t = termTheme((n) => (n === "--bg" ? "" : fakeGet(n)), false);
  assert.ok(!("cursorAccent" in t));
  assert.strictEqual(t.cursor, "#6ea8fe");
});

// currentTermTheme / applyTermThemeAll: the tokens are read once per desktop
// entry, a phone crossing drops them, and the crossing re-themes every terminal.
function withDesktopGlobals(width, fn) {
  const saved = ["window", "document", "getComputedStyle", "mobileActive", "eachTerminal"].map((k) => [k, global[k], k in global]);
  const reads = { n: 0 };
  global.window = { innerWidth: width.v };
  global.document = { documentElement: {} };
  global.getComputedStyle = () => ({ getPropertyValue: (name) => { reads.n++; return " " + fakeGet(name) + " "; } });
  global.mobileActive = (w) => Number(w) <= 700;
  try { fn(reads); } finally {
    for (const [k, v, had] of saved) { if (had) global[k] = v; else delete global[k]; }
  }
}

test("currentTermTheme reads the tokens once per desktop entry and hands out copies", () => {
  const { currentTermTheme } = require("../static/terminal-tab.js");
  const w = { v: 1440 };
  withDesktopGlobals(w, (reads) => {
    const a = currentTermTheme();
    assert.strictEqual(a.background, "#0d0f12");
    assert.strictEqual(a.brightWhite, "#a015");
    const reads1 = reads.n;
    assert.ok(reads1 > 0);
    const b = currentTermTheme();
    assert.strictEqual(reads.n, reads1, "the second desktop call does not read the stylesheet again");
    assert.notStrictEqual(a, b, "each terminal gets its own object");
    assert.deepStrictEqual(a, b);
    a.background = "#123456";
    assert.strictEqual(currentTermTheme().background, "#0d0f12", "editing a copy never edits the cache");

    w.v = 390; global.window.innerWidth = 390;
    assert.deepStrictEqual(currentTermTheme(), { background: "#000000" });
    assert.strictEqual(reads.n, reads1, "a phone reads no token");

    w.v = 1440; global.window.innerWidth = 1440;
    assert.strictEqual(currentTermTheme().background, "#0d0f12");
    assert.ok(reads.n > reads1, "the next desktop entry reads the tokens again");
  });
});

test("applyTermThemeAll themes every terminal eachTerminal yields, pane tabs and docked shells", () => {
  const { applyTermThemeAll, TerminalTab } = require("../static/terminal-tab.js");
  const w = { v: 1440 };
  withDesktopGlobals(w, () => {
    const mk = () => { const t = Object.create(TerminalTab.prototype); t.term = { options: { theme: { background: "#000000" } } }; return t; };
    const pane = mk(), dock = mk(), noTerm = Object.create(TerminalTab.prototype);
    global.eachTerminal = (cb) => { for (const t of [pane, dock, noTerm]) cb(t); };
    applyTermThemeAll();
    assert.strictEqual(pane.term.options.theme.background, "#0d0f12");
    assert.strictEqual(dock.term.options.theme.background, "#0d0f12");
    assert.notStrictEqual(pane.term.options.theme, dock.term.options.theme);
    w.v = 390; global.window.innerWidth = 390;
    applyTermThemeAll();
    assert.deepStrictEqual(pane.term.options.theme, { background: "#000000" });
    assert.deepStrictEqual(dock.term.options.theme, { background: "#000000" });
  });
});

test("the breakpoint listener re-themes the terminals, and the constructor builds its theme from termTheme", () => {
  const fs = require("node:fs"), path = require("node:path");
  const app = fs.readFileSync(path.join(__dirname, "../static/app.js"), "utf8");
  const at = app.indexOf("matchMedia(mobileMediaQuery()).addEventListener");
  assert.ok(at > 0);
  const handler = app.slice(at, app.indexOf("\n  });\n}", at));
  assert.match(handler, /applyTermThemeAll\(\)/);
  const tt = fs.readFileSync(path.join(__dirname, "../static/terminal-tab.js"), "utf8");
  assert.match(tt, /scrollback: 5000, theme: currentTermTheme\(\),/);
  assert.ok(!/theme: \{ background: "#000000" \}/.test(tt), "no hard-coded theme left in the constructor");
});

test("desktop.css frames the terminal edge to edge: graphite cell, kit padding, content-box so fit() sees the content size", () => {
  const fs = require("node:fs"), path = require("node:path");
  const css = fs.readFileSync(path.join(__dirname, "../static/desktop.css"), "utf8");
  assert.match(css, /^\s*\.cell \{ background: var\(--bg\); \}$/m);
  assert.match(css, /^\s*\.pane-term \{ box-sizing: content-box; padding: 6px 10px; background: var\(--term-bg\); \}$/m);
  // The phone's black cell and 4px padding are the shared rules, untouched.
  const app = fs.readFileSync(path.join(__dirname, "../static/app.css"), "utf8");
  assert.match(app, /\.cell \{[^}]*background: #000;/);
  assert.match(app, /\.pane-term \{ flex: 1; min-height: 0; padding: 4px; \}/);
});
