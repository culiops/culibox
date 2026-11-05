"use strict";

// Pure decision logic for the mobile terminal typing affordances: the byte a
// key/snippet puts on the input WS, the ctrl-latch transform, the snippet and
// key-bar tables, and the touch-gesture math (swipe vs scroll, keyboard-open).
// NO DOM and NO socket here — the controller in app.js and TerminalTab do the
// wiring. Classic script: every top-level name is a browser global — keep unique.

// Wire bytes for the fixed accessory keys. Arrows are the xterm/DEC sequences the
// PTY expects; enter is CR (\r), the byte a real Return sends. `ctrl` is NOT here
// because it is a latch (arms the next char), not a byte of its own.
const KEY_BYTES = {
  esc: "\x1b", tab: "\t", enter: "\r",
  up: "\x1b[A", down: "\x1b[B", right: "\x1b[C", left: "\x1b[D",
};
function keyByte(name) {
  return Object.prototype.hasOwnProperty.call(KEY_BYTES, name) ? KEY_BYTES[name] : "";
}

// ctrlByte turns one printable char into the control byte a physical Ctrl+<char>
// would send: letters map to 0x01..0x1a (Ctrl-A..Ctrl-Z, case-insensitive), and
// @ [ \ ] ^ _ map to 0x00..0x1f. Anything else (digits, punctuation, multibyte,
// empty, >1 char) returns "" — the caller then sends the char literally rather
// than a surprise byte. This is what the latched ctrl consumes in onData.
function ctrlByte(ch) {
  if (typeof ch !== "string" || ch.length !== 1) return "";
  const code = ch.toUpperCase().charCodeAt(0);
  if (code >= 0x40 && code <= 0x5f) return String.fromCharCode(code & 0x1f);
  return "";
}

// The snippet row (M2). ^C is FIRST and red: a panic stop is one tap, and it is a
// control byte sent immediately (not inserted text). continue/yes/no// are TEXT:
// they go through sendText(), which refuses to submit, so the user still presses
// Enter. `more` opens a second snippet popover (not built until a later slice adds
// its contents — the button exists, its menu is a no-op placeholder handled in JS).
const SNIPPETS = [
  { key: "sigint", label: "^C", kind: "control", bytes: "\x03", danger: true },
  { key: "continue", label: "continue", kind: "text", text: "continue" },
  { key: "yes", label: "yes", kind: "text", text: "yes" },
  { key: "no", label: "no", kind: "text", text: "no" },
  { key: "slash", label: "/", kind: "text", text: "/", ghost: true },
  { key: "more", label: "⋯", kind: "more", ghost: true }, // ⋯
];

// The seven-key accessory bar (M2/M3), fixed order, never reshaped. `more` (⋯)
// opens a popover of the overflow keys below — left/right arrows and a few
// symbols — so the main row keeps muscle-memory positions with the keyboard up.
const KEYBAR = ["esc", "tab", "ctrl", "up", "down", "enter", "more"];
const KEYBAR_MORE = ["left", "right", "pipe", "slash", "dash", "under"];
const SYMBOL_TEXT = { pipe: "|", slash: "/", dash: "-", under: "_" };

// gestureAxis locks a touch to one axis the first time it travels past `slop` in
// either direction: whichever of |dx|/|dy| is larger wins. Below slop it is null
// (undecided) so a tap or micro-jitter starts nothing. The controller calls this
// once per gesture and then commits to scroll (vertical) or swipe (horizontal).
function gestureAxis(dx, dy, slop) {
  if (Math.abs(dx) < slop && Math.abs(dy) < slop) return null;
  return Math.abs(dx) >= Math.abs(dy) ? "horizontal" : "vertical";
}

// swipeIntent fires only past `threshold` total travel: dragging left (dx<0)
// reveals the NEXT session (content slides left, like a carousel); dragging right
// reveals the PREV. Under threshold it is null (treated as a cancelled swipe).
function swipeIntent(dx, threshold) {
  if (dx <= -threshold) return "next";
  if (dx >= threshold) return "prev";
  return null;
}

// keyboardOpen decides the soft keyboard is up when the visual viewport is at
// least `minDelta` shorter than the layout viewport. A small delta (address bar,
// browser chrome) is NOT a keyboard — minDelta (~120) filters it out.
function keyboardOpen(innerHeight, vvHeight, minDelta) {
  return Number(innerHeight) - Number(vvHeight) >= minDelta;
}

// touchPrimary: is the primary input a finger (no hover, a coarse pointer)? The
// toast that names a mouse gesture (Shift+drag) asks it, because a touch screen
// has no such gesture to name (#111). matchMedia is passed in so this runs
// without a window; a missing or throwing one is "no", the desktop default.
function touchPrimary(matchMediaFn) {
  try {
    const q = matchMediaFn("(hover: none) and (pointer: coarse)");
    return !!(q && q.matches);
  } catch (_) {
    return false;
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    KEY_BYTES, keyByte, ctrlByte, SNIPPETS, KEYBAR, KEYBAR_MORE, SYMBOL_TEXT,
    gestureAxis, swipeIntent, keyboardOpen, touchPrimary,
  };
}
