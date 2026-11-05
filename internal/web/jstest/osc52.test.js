"use strict";
// OSC 52 is the agent asking to write the user's clipboard. The agent is
// untrusted, so the terminal honours it only right after the user asked for a
// copy in that same, visible, focused pane (see osc52Decision).
const test = require("node:test");
const assert = require("node:assert");
const { osc52Decision, osc52Gesture } = require("../static/terminal-tab.js");

// b64 builds the payload the way Claude Code does: base64 of the UTF-8 bytes.
const b64 = (s) => Buffer.from(s, "utf8").toString("base64");
// A pane the user just released a drag in, 200 ms before the sequence arrived.
const live = { now: 10200, armedAt: 10000, focused: true, visible: true };

test("a write right after a gesture in a focused, visible pane is copied", () => {
  assert.deepStrictEqual(osc52Decision("c;" + b64("hello"), live), { action: "write", text: "hello" });
});

// `sleep 3; printf '\e]52;...'` after the user's last click, a write with no
// gesture ever, and a clock that stepped backwards (armedAt in the future).
test("a write with no recent gesture is dropped", () => {
  const data = "c;" + b64("hello");
  for (const [now, armedAt] of [[13000, 10000], [11600, 10000], [10000, 0], [9000, 10000]]) {
    assert.deepStrictEqual(osc52Decision(data, { ...live, now, armedAt }), { action: "drop", reason: "no-gesture" }, `now=${now} armedAt=${armedAt}`);
  }
});

// Inside the window, but the user has moved on: another pane, another window,
// a background tab or an inactive tab. Only the pane in front of the user may
// write, so a session they are not looking at cannot ride another pane's gesture.
test("an unfocused pane never writes", () => {
  assert.deepStrictEqual(osc52Decision("c;" + b64("hello"), { ...live, focused: false }), { action: "drop", reason: "unfocused" });
});

test("a hidden pane never writes", () => {
  assert.deepStrictEqual(osc52Decision("c;" + b64("hello"), { ...live, visible: false }), { action: "drop", reason: "hidden" });
});

// `Pd = ?` asks the terminal to send the clipboard back to the program. That
// would hand the user's clipboard (a password, a token) to the agent, so it is
// refused even in the most permissive state: just after a gesture, focused.
test("a clipboard read query is never answered", () => {
  for (const data of ["c;?", ";?", "p;?", "s0;?"]) {
    assert.deepStrictEqual(osc52Decision(data, live), { action: "drop", reason: "read" }, data);
  }
});

// The cap is on the decoded bytes: 1 MiB is far more than any screen of text a
// user drags over, so only a payload nobody selected can hit it.
test("a write over 1 MiB is dropped, 1 MiB exactly is copied", () => {
  const mib = 1024 * 1024;
  assert.deepStrictEqual(osc52Decision("c;" + b64("a".repeat(mib + 1)), live), { action: "drop", reason: "too-large" });
  assert.strictEqual(osc52Decision("c;" + b64("a".repeat(mib)), live).text.length, mib);
  // Multi-byte text counts bytes, not characters: 349526 three-byte "€" = 1048578 bytes.
  assert.deepStrictEqual(osc52Decision("c;" + b64("€".repeat(349526)), live), { action: "drop", reason: "too-large" });
});

// A payload far past the cap is refused on its length alone, before decoding:
// an agent must not be able to make the page decode megabytes per sequence.
test("a huge write is refused without being decoded", () => {
  const huge = "c;" + "A".repeat(8 * 1024 * 1024) + "!"; // not even valid base64
  assert.deepStrictEqual(osc52Decision(huge, live), { action: "drop", reason: "too-large" });
});

// Strict: atob() forgives missing padding and embedded spaces, and happily turns
// bytes that are not UTF-8 into mojibake. A malformed payload is refused, never
// guessed at. Every case here is in a live window, so only decoding can drop it.
test("a write that is not strict base64 of UTF-8 is dropped", () => {
  const cases = {
    "unpadded": "aGVsbG8",
    "embedded space": "aGVs bG8=",
    "bad character": "aGVsbG8!",
    "url-safe alphabet": "-_-_",
    "padding mid-string": "aG=sbG8=",
    "too much padding": "a===",
    "not UTF-8 (0xFF)": "/w==",
    "truncated UTF-8 (half a euro sign)": "4oI=",
  };
  for (const [name, pd] of Object.entries(cases)) {
    assert.deepStrictEqual(osc52Decision("c;" + pd, live), { action: "drop", reason: "invalid" }, name);
  }
});

// Pc names the selection: empty, or letters from c p q s 0-7. Anything else is
// not an OSC 52 this terminal understands, including one with no Pc at all.
test("a sequence with a malformed selection field is dropped", () => {
  for (const data of [b64("hello"), "x;" + b64("hello"), "c\x1b;" + b64("hello"), "c 1;" + b64("hello")]) {
    assert.deepStrictEqual(osc52Decision(data, live), { action: "drop", reason: "invalid" }, JSON.stringify(data));
  }
});

test("every valid selection field writes the one browser clipboard", () => {
  for (const pc of ["", "c", "p", "s", "0", "cp7"]) {
    assert.deepStrictEqual(osc52Decision(pc + ";" + b64("hi"), live), { action: "write", text: "hi" }, pc);
  }
});

// Text selected off a screen never contains ESC or other control bytes, and ESC
// is what a pasted payload uses to break out of bracketed paste in a terminal
// that does not filter it. Tabs and line breaks are ordinary multi-line text.
test("a write carrying control characters is dropped", () => {
  for (const s of ["\x1b[201~rm -rf ~\r", "a\x00b", "a\x7fb", "a\u0085b", "a\u009bb"]) {
    assert.deepStrictEqual(osc52Decision("c;" + b64(s), live), { action: "drop", reason: "control" }, JSON.stringify(s));
  }
  assert.deepStrictEqual(osc52Decision("c;" + b64("a\tb\r\nc\nd"), live), { action: "write", text: "a\tb\r\nc\nd" });
  assert.deepStrictEqual(osc52Decision("c;" + b64("héllo € 👍"), live), { action: "write", text: "héllo € 👍" });
});

// An empty payload is a "clear the clipboard" request. Nothing the user asked
// for, and wiping what they copied elsewhere is the agent's call only in name.
test("an empty write is not honoured", () => {
  assert.deepStrictEqual(osc52Decision("c;", live), { action: "drop", reason: "empty" });
});

// --- which user input opens the window --------------------------------------
// Claude Code copies through OSC 52 only for its OWN selection, which exists only
// while it has mouse reporting on, and only at two moments: the release of the
// drag and Ctrl+C. Every other input would open a window it never uses.
const mouse = (o) => ({ type: "mousedown", button: 0, shiftKey: false, altKey: false, ctrlKey: false, metaKey: false, ...o });
const key = (o) => ({ type: "keydown", key: "c", code: "KeyC", keyCode: 67, shiftKey: false, altKey: false, ctrlKey: false, metaKey: false, ...o });

test("a press the program receives opens the window; a forced selection does not", () => {
  for (const mode of ["vt200", "drag", "any"]) {
    assert.strictEqual(osc52Gesture(mouse(), mode, false), true, mode);
    assert.strictEqual(osc52Gesture(mouse(), mode, true), true, mode + " mac");
  }
  // Shift (Win/Linux) and Option (macOS) make xterm select instead of
  // forwarding: that is CuliBox's own copy, which the agent must not overwrite.
  assert.strictEqual(osc52Gesture(mouse({ shiftKey: true }), "any", false), false);
  assert.strictEqual(osc52Gesture(mouse({ altKey: true }), "any", true), false);
  // On a Mac, Shift does not force a selection, so the press reaches the program.
  assert.strictEqual(osc52Gesture(mouse({ shiftKey: true }), "any", true), true);
  // Only the left button makes a selection.
  assert.strictEqual(osc52Gesture(mouse({ button: 2 }), "any", false), false);
});

test("a click in a program without mouse reporting never opens the window", () => {
  // A plain shell: the click never reached the program, so it cannot be
  // answering it. Same for Ctrl+C interrupting a command there.
  assert.strictEqual(osc52Gesture(mouse(), "none", false), false);
  assert.strictEqual(osc52Gesture(key({ ctrlKey: true }), "none", false), false);
  assert.strictEqual(osc52Gesture(mouse(), undefined, false), false);
});

test("only an exact Ctrl+C opens the window from the keyboard", () => {
  assert.strictEqual(osc52Gesture(key({ ctrlKey: true }), "any", false), true);
  assert.strictEqual(osc52Gesture(key({ ctrlKey: true }), "any", true), true); // Mac Ctrl, not Cmd
  // Dvorak: the key labelled C is physically KeyI, and xterm sends ^C by keyCode.
  assert.strictEqual(osc52Gesture(key({ ctrlKey: true, code: "KeyI" }), "any", false), true);
  const no = {
    "plain c": key(),
    "Enter": key({ key: "Enter", code: "Enter", keyCode: 13 }),
    "Ctrl+D": key({ ctrlKey: true, key: "d", code: "KeyD", keyCode: 68 }),
    "Ctrl+Shift+C (no bytes reach the PTY)": key({ ctrlKey: true, shiftKey: true }),
    "Cmd+C (no bytes reach the PTY)": key({ metaKey: true }),
    "AltGr+C": key({ ctrlKey: true, altKey: true }),
    "keyup": key({ type: "keyup", ctrlKey: true }),
  };
  for (const [name, e] of Object.entries(no)) assert.strictEqual(osc52Gesture(e, "any", false), false, name);
});

// --- the handler on a tab ------------------------------------------------------
// onOsc52 runs on a stand-in tab with the page globals it touches (document,
// copyText, showToast) replaced by recorders. copyText itself is app.js's, and
// covered by execcopy.test.js.
const { TerminalTab } = require("../static/terminal-tab.js");

function harness(copyOK = true) {
  const textarea = {};
  const log = { copied: [], toasts: [], sent: [], focused: 0 };
  global.document = { hasFocus: () => true, activeElement: textarea, visibilityState: "visible" };
  global.copyText = (text, done) => { log.copied.push(text); done(copyOK); };
  global.showToast = (msg, opts) => log.toasts.push([msg, opts || {}]);
  const tab = {
    gestureAt: Date.now() - 100,
    termEl: { clientWidth: 800, clientHeight: 400 },
    term: { textarea, focus: () => log.focused++, input: (d) => log.sent.push(d) },
    ws: { readyState: 1, send: (d) => log.sent.push(d) },
  };
  return { tab, log, osc: (data) => TerminalTab.prototype.onOsc52.call(tab, data) };
}
test.afterEach(() => { delete global.document; delete global.copyText; delete global.showToast; });

test("one gesture allows one write, and the copy is announced", () => {
  const { log, osc } = harness();
  osc("c;" + b64("hello"));
  osc("c;" + b64("swapped"));
  assert.deepStrictEqual(log.copied, ["hello"]);
  assert.deepStrictEqual(log.toasts.map((t) => t[0]), ["Copied 5 characters from the agent"]);
  assert.strictEqual(log.focused, 1); // the copy textarea took focus; it is given back
});

test("the toast counts characters, not UTF-16 units", () => {
  const { log, osc } = harness();
  osc("c;" + b64("👍"));
  assert.deepStrictEqual(log.toasts.map((t) => t[0]), ["Copied 1 character from the agent"]);
});

test("a write the browser refuses says so instead of claiming a copy", () => {
  const { log, osc } = harness(false);
  osc("c;" + b64("hello"));
  assert.strictEqual(log.toasts.length, 1);
  assert.strictEqual(log.toasts[0][1].warn, true);
  assert.doesNotMatch(log.toasts[0][0], /^Copied/);
});

test("a read query sends nothing back and leaves the window open", () => {
  const { log, osc } = harness();
  osc("c;?");
  assert.deepStrictEqual(log.sent, []);
  assert.deepStrictEqual(log.copied, []);
  osc("c;" + b64("hello")); // the gesture was not spent on the query
  assert.deepStrictEqual(log.copied, ["hello"]);
});

test("a dropped write copies nothing and shows nothing", () => {
  const { tab, log, osc } = harness();
  tab.gestureAt = 0;
  const warn = console.warn; console.warn = () => {};
  try { osc("c;" + b64("hello")); } finally { console.warn = warn; }
  assert.deepStrictEqual(log.copied, []);
  assert.deepStrictEqual(log.toasts, []);
});

test("a pane that is not the focused element never writes", () => {
  const { log, osc } = harness();
  global.document.activeElement = {}; // focus is in another pane
  const warn = console.warn; console.warn = () => {};
  try { osc("c;" + b64("hello")); } finally { console.warn = warn; }
  assert.deepStrictEqual(log.copied, []);
});
