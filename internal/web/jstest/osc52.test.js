"use strict";
// OSC 52 is the agent asking to write the user's clipboard. The agent is
// untrusted, so the terminal honours it only right after the user asked for a
// copy in that same, visible, focused pane (see osc52Decision).
const test = require("node:test");
const assert = require("node:assert");
const { osc52Decision, osc52Gesture, osc52Lines, osc52HintText } = require("../static/terminal-tab.js");

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

// A selection taken off a terminal screen is cells of printable characters and
// line breaks. A CR that is not half of a CRLF returns the cursor to column 0 of
// the shell it is pasted into, and the Unicode format characters (bidi
// overrides and isolates, zero-width joiners) change what a line looks like
// without changing what runs. A paste into a shell without bracketed paste can
// then carry a command the user never saw, so these are refused whole.
test("a write with a lone CR is refused as format", () => {
  for (const s of ["a\rb", "a\r", "\rrm -rf ~\n", "a\r\r\nb", "line one\nline two\r"]) {
    assert.deepStrictEqual(osc52Decision("c;" + b64(s), live), { action: "drop", reason: "format" }, JSON.stringify(s));
  }
});

test("a write with a bidi or zero-width format character is refused as format", () => {
  const cps = [
    0x202a, 0x202b, 0x202c, 0x202d, 0x202e,         // embeddings and overrides
    0x2066, 0x2067, 0x2068, 0x2069,                 // isolates
    0x200b, 0x200c, 0x200d, 0x2060, 0xfeff,         // zero-width space, non-joiner, joiner, word joiner, BOM
    0x200e, 0x200f, 0x061c,                         // LRM, RLM, ALM
  ];
  for (const cp of cps) {
    const s = "echo ok " + String.fromCodePoint(cp) + "# hidden";
    assert.deepStrictEqual(osc52Decision("c;" + b64(s), live), { action: "drop", reason: "format" }, "U+" + cp.toString(16));
  }
});

// U+2028 and U+2029 are line breaks to a renderer that honours them and nothing
// to a shell: a pasted line can look split while the shell sees one line.
test("a line or paragraph separator is refused as format", () => {
  for (const cp of [0x2028, 0x2029]) {
    const s = "echo ok" + String.fromCodePoint(cp) + "rm -rf ~";
    assert.deepStrictEqual(osc52Decision("c;" + b64(s), live), { action: "drop", reason: "format" }, "U+" + cp.toString(16));
  }
});

test("ordinary multi-line text, tabs and non-ASCII still copy", () => {
  const texts = [
    "a\nb\nc",
    "a\r\nb\r\n",                      // CRLF is two characters of one line break
    "col1\tcol2\n",
    "Tiếng Việt: ắ ằ ẳ ẵ ặ đ Đ",          // precomposed
    "Vie\u0302\u0323t Nam e\u0302\u0301",  // decomposed: combining marks are not format characters
    "héllo € 👍 日本語 مرحبا",
  ];
  for (const s of texts) {
    assert.deepStrictEqual(osc52Decision("c;" + b64(s), live), { action: "write", text: s }, JSON.stringify(s));
  }
});

// The screen runs only after the gesture gate, so an agent that never got a
// gesture learns nothing from it and cannot make the page show anything.
test("a format payload outside a live window is dropped by the gate, not refused", () => {
  const data = "c;" + b64("a\rb");
  assert.deepStrictEqual(osc52Decision(data, { ...live, armedAt: 0 }), { action: "drop", reason: "no-gesture" });
  assert.deepStrictEqual(osc52Decision(data, { ...live, focused: false }), { action: "drop", reason: "unfocused" });
  assert.deepStrictEqual(osc52Decision(data, { ...live, visible: false }), { action: "drop", reason: "hidden" });
});

// How many lines a copy had, for the toast. A trailing line break ends the last
// line rather than starting an empty one, so what a user sees as "3 lines" (a
// selection that ends at the end of a row) counts 3.
test("osc52Lines counts lines, and a trailing line break adds none", () => {
  const cases = [["hello", 1], ["a\nb\nc", 3], ["a\nb\n", 2], ["a\r\nb\r\n", 2], ["a\r\nb", 2], ["a\n\nb", 3], ["a\n\n", 2], ["\n", 1], [" ", 1]];
  for (const [s, n] of cases) assert.strictEqual(osc52Lines(s), n, JSON.stringify(s));
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
  assert.deepStrictEqual(log.toasts.map((t) => t[0]), ["Copied 1 line from the agent"]);
  assert.strictEqual(log.focused, 1); // the copy textarea took focus; it is given back
});

test("the toast says how many lines were copied", () => {
  for (const [text, msg] of [
    ["one", "Copied 1 line from the agent"],
    ["a\nb\nc", "Copied 3 lines from the agent"],
    ["a\r\nb\r\n", "Copied 2 lines from the agent"],
  ]) {
    const { log, osc } = harness();
    osc("c;" + b64(text));
    assert.deepStrictEqual(log.toasts.map((t) => t[0]), [msg], JSON.stringify(text));
  }
});

// The toast carries a count and fixed words only. Nothing the agent sent is ever
// put in it, and showToast sets it through textContent, so it is not parsed.
test("the toast never carries the agent's text", () => {
  const { log, osc } = harness();
  osc("c;" + b64("<img src=x onerror=alert(1)>\nsecond"));
  assert.deepStrictEqual(log.toasts.map((t) => t[0]), ["Copied 2 lines from the agent"]);
});

test("showToast renders its message through textContent", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/shell.js"), "utf8");
  const start = src.indexOf("function showToast(");
  assert.ok(start >= 0, "showToast found");
  const body = src.slice(start, src.indexOf("\nfunction ", start + 1));
  assert.match(body, /getElementById\("toast-msg"\)\.textContent = msg/);
  assert.doesNotMatch(body, /innerHTML|outerHTML|insertAdjacentHTML/);
});

// A refused payload is the one case where the page shows something the agent
// caused after the gesture gate has already passed, so it must cost the gesture:
// one click buys at most one toast, however many sequences follow it.
test("a refused write warns once, copies nothing and spends the gesture", () => {
  const { tab, log, osc } = harness();
  const warn = console.warn; console.warn = () => {};
  try {
    osc("c;" + b64("echo hi\r"));
    osc("c;" + b64("echo hi\r"));
    osc("c;" + b64("echo \u202ehi"));
  } finally { console.warn = warn; }
  assert.deepStrictEqual(log.copied, []);
  assert.strictEqual(log.toasts.length, 1);
  assert.strictEqual(log.toasts[0][1].warn, true);
  assert.doesNotMatch(log.toasts[0][0], /^Copied/);
  assert.doesNotMatch(log.toasts[0][0], /echo/);
  assert.strictEqual(tab.gestureAt, 0);
});

test("a refused write names the way out when the page has one", () => {
  const { log, osc } = harness();
  global.IS_MAC = false;
  global.scEntry = () => ({ text: { mac: "Option+drag", other: "Shift+drag" } });
  const warn = console.warn; console.warn = () => {};
  try { osc("c;" + b64("a\rb")); } finally { console.warn = warn; delete global.scEntry; delete global.IS_MAC; }
  assert.match(log.toasts[0][0], /Shift\+drag/);
});

// #111: the way out is a desktop gesture (Shift+drag, Option+drag). A touch screen
// has none, so on touch the toast says what happened and stops there.
test("osc52HintText names the selection gesture, and nothing on touch or without a help row", () => {
  const entry = { text: { mac: "⌥+drag", other: "Shift+drag" } };
  assert.strictEqual(osc52HintText(entry, false, false), " Select with Shift+drag to copy instead.");
  assert.match(osc52HintText(entry, true, false), /⌥\+drag/);
  assert.strictEqual(osc52HintText(entry, false, true), "");
  assert.strictEqual(osc52HintText(entry, true, true), "");
  assert.strictEqual(osc52HintText(null, false, false), "");
  assert.strictEqual(osc52HintText(undefined, false, true), "");
});

test("a refused write on a touch screen says what happened and names no desktop gesture", () => {
  const { log, osc } = harness();
  global.IS_MAC = false;
  global.scEntry = () => ({ text: { mac: "⌥+drag", other: "Shift+drag" } });
  global.touchPrimary = require("../static/mobileterm.js").touchPrimary;
  global.window = { matchMedia: (q) => ({ matches: /hover: none/.test(q) }) };
  const warn = console.warn; console.warn = () => {};
  try { osc("c;" + b64("a\rb")); } finally {
    console.warn = warn; delete global.scEntry; delete global.IS_MAC; delete global.touchPrimary; delete global.window;
  }
  assert.strictEqual(log.toasts[0][0], "Not copied: the agent's text has hidden characters.");
});

test("a blocked copy on a touch screen names no desktop gesture either", () => {
  const { log, osc } = harness(false);
  global.IS_MAC = false;
  global.scEntry = () => ({ text: { mac: "⌥+drag", other: "Shift+drag" } });
  global.touchPrimary = require("../static/mobileterm.js").touchPrimary;
  global.window = { matchMedia: () => ({ matches: true }) };
  try { osc("c;" + b64("hello")); } finally {
    delete global.scEntry; delete global.IS_MAC; delete global.touchPrimary; delete global.window;
  }
  assert.strictEqual(log.toasts[0][0], "The browser blocked the agent's copy.");
});

test("a refused write with a mouse still names the way out", () => {
  const { log, osc } = harness();
  global.IS_MAC = false;
  global.scEntry = () => ({ text: { mac: "⌥+drag", other: "Shift+drag" } });
  global.touchPrimary = require("../static/mobileterm.js").touchPrimary;
  global.window = { matchMedia: () => ({ matches: false }) };
  const warn = console.warn; console.warn = () => {};
  try { osc("c;" + b64("a\rb")); } finally {
    console.warn = warn; delete global.scEntry; delete global.IS_MAC; delete global.touchPrimary; delete global.window;
  }
  assert.strictEqual(log.toasts[0][0], "Not copied: the agent's text has hidden characters. Select with Shift+drag to copy instead.");
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

// --- what arms and disarms the window ----------------------------------------
test("a press that does not open the window drops a release still pending", () => {
  // A drag released outside the browser never delivers its mouseup; the next
  // release anywhere, here a Shift+drag's (the user's own copy), must not arm.
  const listeners = [];
  global.IS_MAC = false;
  global.document = {
    addEventListener: (type, fn) => listeners.push([type, fn]),
    removeEventListener: (type, fn) => { const i = listeners.findIndex((l) => l[0] === type && l[1] === fn); if (i >= 0) listeners.splice(i, 1); },
  };
  const tab = Object.create(TerminalTab.prototype);
  Object.assign(tab, { gestureAt: 0, term: { modes: { mouseTrackingMode: "any" } } });
  try {
    tab.armOsc52(mouse());
    tab.armOsc52(mouse({ shiftKey: true }));
    for (const [type, fn] of listeners) if (type === "mouseup") fn();
    assert.strictEqual(tab.gestureAt, 0);
  } finally { delete global.IS_MAC; }
});

test("a reconnect forgets a gesture made before it", () => {
  // connect() replays the session's output ring, which can hold an old OSC 52
  // (or one the agent planted there): a click just before "Retry now" must not
  // let the replay write the clipboard.
  global.location = { protocol: "http:", host: "h" };
  global.WebSocket = class { constructor(url) { this.url = url; } };
  const tab = Object.create(TerminalTab.prototype);
  Object.assign(tab, {
    id: "s1", gestureAt: Date.now(), term: { reset() {} },
    clearRetry() {}, hideTrouble() {}, setStatus() {},
  });
  try {
    tab.connect();
    assert.strictEqual(tab.gestureAt, 0);
  } finally { delete global.location; delete global.WebSocket; }
});

// --- the replay after a (re)connect -------------------------------------------
// The server answers a new socket with the session's output ring in ONE frame
// (Session.Subscribe + deliver), then live frames, with nothing that marks where
// the replay ends. The ring can hold an old OSC 52, so no gesture may count from
// connect() until xterm has parsed that first frame, and a ring resync mid-stream
// (a frame that starts with RIS, ESC c) holds the same way. The rig models
// xterm's write queue: write(data, cb) parks the frame, and the test decides
// when "xterm" has parsed it (done), which is when its callback fires.
function replayRig() {
  const listeners = [];
  const log = { copied: [], toasts: [] };
  const textarea = {};
  global.IS_MAC = false;
  global.location = { protocol: "http:", host: "h" };
  global.WebSocket = class { constructor(url) { this.url = url; } };
  global.document = {
    hasFocus: () => true, activeElement: textarea, visibilityState: "visible",
    addEventListener: (type, fn) => listeners.push([type, fn]),
    removeEventListener: (type, fn) => { const i = listeners.findIndex((l) => l[0] === type && l[1] === fn); if (i >= 0) listeners.splice(i, 1); },
  };
  global.copyText = (text, done) => { log.copied.push(text); done(true); };
  global.showToast = (msg, opts) => log.toasts.push([msg, opts || {}]);
  const queue = [];
  const term = {
    textarea, modes: { mouseTrackingMode: "none" }, reset() {}, focus() {},
    write(data, cb) { queue.push({ data, cb }); },
  };
  const tab = Object.create(TerminalTab.prototype);
  Object.assign(tab, {
    id: "s1", gestureAt: 0, term, termEl: { clientWidth: 800, clientHeight: 400 },
    clearRetry() {}, hideTrouble() {}, setStatus() {},
  });
  const frame = (s) => ({ data: new TextEncoder().encode(s).buffer });
  const pressRelease = () => {
    tab.armOsc52(mouse());
    for (const [type, fn] of [...listeners]) if (type === "mouseup") fn();
  };
  return {
    tab, log, queue, listeners, frame,
    click: pressRelease,
    // done: xterm finished parsing the oldest queued frame.
    done: () => { const w = queue.shift(); if (w.cb) w.cb(); },
    osc: (text) => tab.onOsc52("c;" + b64(text)),
    cleanup: () => { for (const k of ["IS_MAC", "location", "WebSocket", "document", "copyText", "showToast"]) delete global[k]; },
  };
}

test("a click while the replay is still being parsed never lets the old OSC 52 write", () => {
  const r = replayRig();
  try {
    r.tab.connect();
    r.tab.ws.onmessage(r.frame("\x1b[?1003h...scrolled output..."));
    // Mid-replay the program's mouse mode is on (the replay switched it on, or
    // earlier queued output did) and the user clicks before the parse is over.
    r.tab.term.modes.mouseTrackingMode = "any";
    r.click();
    r.osc("old secret planted in the ring");
    assert.deepStrictEqual(r.log.copied, []);
    assert.strictEqual(r.tab.gestureAt, 0);
    // Replay parsed: now a click counts and the program's reply is honoured.
    r.done();
    r.click();
    r.osc("fresh copy");
    assert.deepStrictEqual(r.log.copied, ["fresh copy"]);
  } finally { r.cleanup(); }
});

test("gestures are ignored from connect() until the first frame has been parsed", () => {
  const r = replayRig();
  try {
    r.tab.connect();
    r.tab.term.modes.mouseTrackingMode = "any";
    r.click();
    r.tab.armOsc52Key(key({ ctrlKey: true }));
    assert.strictEqual(r.tab.gestureAt, 0, "before the first frame has even arrived");
    r.tab.ws.onmessage(r.frame("ring"));
    r.click();
    assert.strictEqual(r.tab.gestureAt, 0, "frame queued, not yet parsed");
    r.done();
    r.click();
    assert.ok(r.tab.gestureAt > 0, "parsed");
  } finally { r.cleanup(); }
});

test("a press made before a reconnect cannot arm on its release during the replay", () => {
  const r = replayRig();
  try {
    r.tab.term.modes.mouseTrackingMode = "any";
    r.tab.armOsc52(mouse());      // press; the release is still to come
    r.tab.connect();              // the socket dropped and came back meanwhile
    r.tab.ws.onmessage(r.frame("ring"));
    for (const [type, fn] of [...r.listeners]) if (type === "mouseup") fn();
    assert.strictEqual(r.tab.gestureAt, 0);
  } finally { r.cleanup(); }
});

test("Ctrl+C while the replay is being parsed does not open the window", () => {
  const r = replayRig();
  try {
    r.tab.connect();
    r.tab.ws.onmessage(r.frame("ring"));
    r.tab.term.modes.mouseTrackingMode = "any";
    r.tab.armOsc52Key(key({ ctrlKey: true }));
    assert.strictEqual(r.tab.gestureAt, 0);
    r.done();
    r.tab.armOsc52Key(key({ ctrlKey: true }));
    assert.ok(r.tab.gestureAt > 0);
    r.tab.gestureAt = 0;
    r.tab.armOsc52Key(key({ ctrlKey: true, shiftKey: true })); // not a gesture at all
    assert.strictEqual(r.tab.gestureAt, 0);
  } finally { r.cleanup(); }
});

test("live frames after the replay do not hold gestures", () => {
  const r = replayRig();
  try {
    r.tab.connect();
    r.tab.ws.onmessage(r.frame("ring"));
    r.done();
    r.tab.term.modes.mouseTrackingMode = "any";
    r.tab.ws.onmessage(r.frame("spinner tick")); // live, queued, not parsed yet
    r.click();
    assert.ok(r.tab.gestureAt > 0);
  } finally { r.cleanup(); }
});

// A viewer that fell behind the ring is resynced with ESC c + the whole ring: the
// same old output, in the middle of the stream.
test("a ring resync mid-stream drops the open window and holds gestures until parsed", () => {
  const r = replayRig();
  try {
    r.tab.connect();
    r.tab.ws.onmessage(r.frame("ring"));
    r.done();
    r.tab.term.modes.mouseTrackingMode = "any";
    r.click();
    assert.ok(r.tab.gestureAt > 0);
    r.tab.ws.onmessage(r.frame("\x1bc" + "ring again"));
    assert.strictEqual(r.tab.gestureAt, 0, "a window opened before the resync is closed");
    r.click();
    assert.strictEqual(r.tab.gestureAt, 0, "held while it is parsed");
    r.done();
    r.click();
    assert.ok(r.tab.gestureAt > 0);
  } finally { r.cleanup(); }
});

// A parse callback that outlives its connection (xterm still had the old
// socket's frame queued when connect() ran again) must not release the new hold.
test("a stale parse callback from an earlier connection does not release the new replay hold", () => {
  const r = replayRig();
  try {
    r.tab.connect();
    r.tab.ws.onmessage(r.frame("first ring"));
    r.tab.connect();              // retry before xterm parsed it
    r.tab.ws.onmessage(r.frame("second ring"));
    r.tab.term.modes.mouseTrackingMode = "any";
    r.done();                     // the FIRST connection's callback
    r.click();
    assert.strictEqual(r.tab.gestureAt, 0);
    r.done();                     // the second one
    r.click();
    assert.ok(r.tab.gestureAt > 0);
  } finally { r.cleanup(); }
});

// The press, not only the release, checks the hold: a press made during the
// replay whose release lands after the replay was parsed must not arm either
// (the user pressed while the screen was still the old output).
test("a press made during the replay cannot arm on a release after it was parsed", () => {
  const r = replayRig();
  try {
    r.tab.connect();
    r.tab.ws.onmessage(r.frame("ring"));
    r.tab.term.modes.mouseTrackingMode = "any";
    r.tab.armOsc52(mouse());      // press while the replay is queued
    r.done();                     // the replay is parsed
    for (const [type, fn] of [...r.listeners]) if (type === "mouseup") fn();
    assert.strictEqual(r.tab.gestureAt, 0);
  } finally { r.cleanup(); }
});

// connect() replaces the socket, but the old one can still deliver a frame it
// had already received. It is not this connection's output: it must not be
// written, and it must not take the place of the first frame (the replay).
test("a frame from a replaced socket is not written and does not touch the new hold", () => {
  const r = replayRig();
  try {
    r.tab.connect();
    const old = r.tab.ws;
    r.tab.connect();
    old.onmessage(r.frame("\x1bc" + "stale frame"));
    assert.strictEqual(r.queue.length, 0, "the stale frame was written");
    r.tab.term.modes.mouseTrackingMode = "any";
    r.tab.ws.onmessage(r.frame("the real ring"));
    assert.strictEqual(r.queue.length, 1);
    r.click();
    assert.strictEqual(r.tab.gestureAt, 0, "the new replay is still being parsed");
    r.done();
    r.click();
    assert.ok(r.tab.gestureAt > 0);
  } finally { r.cleanup(); }
});

// --- a release left pending by a press that never finished --------------------
// Built through the real constructor, with the browser and xterm replaced by
// stubs, because the document listener that clears a stale release is installed
// there and must be gone again after dispose().
function builtTab() {
  const listeners = []; // what the tab has on the document right now
  const element = () => {
    const handlers = {};
    return {
      handlers, className: "", clientWidth: 0, clientHeight: 0, firstChild: null,
      append() {}, insertBefore() {}, remove() {}, closest: () => null, querySelector: () => null,
      addEventListener(type, fn) { (handlers[type] = handlers[type] || []).push(fn); },
      removeEventListener() {},
    };
  };
  const textarea = {};
  global.IS_MAC = false;
  global.window = { innerWidth: 1200 };
  global.location = { protocol: "http:", host: "h" };
  global.WebSocket = class { constructor(url) { this.url = url; } close() {} };
  global.api = () => Promise.resolve([]);
  global.document = {
    createElement: element, hasFocus: () => true, activeElement: textarea, visibilityState: "visible",
    addEventListener: (type, fn, opts) => listeners.push([type, fn, !!(opts === true || (opts && opts.capture))]),
    removeEventListener: (type, fn, opts) => {
      const cap = !!(opts === true || (opts && opts.capture));
      const i = listeners.findIndex((l) => l[0] === type && l[1] === fn && l[2] === cap);
      if (i >= 0) listeners.splice(i, 1);
    },
  };
  global.Terminal = class {
    constructor() { this.modes = { mouseTrackingMode: "any" }; this.parser = { registerOscHandler() {} }; this.textarea = textarea; this.rows = 24; this.cols = 80; }
    loadAddon() {} open() {} onData() {} attachCustomKeyEventHandler() {} registerLinkProvider() {}
    reset() {} dispose() {} focus() {} write() {}
  };
  global.FitAddon = { FitAddon: class { fit() {} } };
  global.WebglAddon = { WebglAddon: class {} };
  const tab = new TerminalTab("s1", "one");
  tab.replay.hold = 0; // connect() holds gestures for the replay; this is about what follows it
  return {
    tab, listeners,
    docDown: () => { for (const [type, fn, cap] of [...listeners]) if (type === "mousedown" && cap) fn({ type: "mousedown" }); },
    docUp: () => { for (const [type, fn, cap] of [...listeners]) if (type === "mouseup" && cap) fn({ type: "mouseup" }); },
    paneDown: (o) => { for (const fn of tab.termEl.handlers.mousedown) fn(mouse(o)); },
    cleanup: () => { for (const k of ["IS_MAC", "window", "location", "WebSocket", "api", "document", "Terminal", "FitAddon", "WebglAddon"]) delete global[k]; },
  };
}

test("a press anywhere in the document drops a release still pending", () => {
  const r = builtTab();
  try {
    r.paneDown();   // a press the program receives: its release would arm the window
    // The release is lost; the user then presses a control that cancels its own
    // mousedown, so no pane sees it. Only the document (capture) can.
    r.docDown();
    r.docUp();      // a later release, e.g. of a drag started in the page chrome
    assert.strictEqual(r.tab.gestureAt, 0);
  } finally { r.cleanup(); }
});

test("a press in this pane still arms its own release", () => {
  const r = builtTab();
  try {
    // A real press reaches the document in the capture phase before the pane.
    r.docDown(); r.paneDown();
    r.docUp();
    assert.ok(r.tab.gestureAt > 0);
  } finally { r.cleanup(); }
});

test("dispose removes every document listener the tab added", () => {
  const r = builtTab();
  try {
    r.paneDown();   // leaves a release pending as well
    assert.ok(r.listeners.length >= 2, "the tab is listening: " + r.listeners.map((l) => l[0]));
    r.tab.dispose();
    assert.deepStrictEqual(r.listeners.map((l) => l[0]), []);
  } finally { r.cleanup(); }
});
