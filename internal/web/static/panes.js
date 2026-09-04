"use strict";

// Per-pane collapse state for the sidebar's Sessions and Files panes.
//
// The two panes collapse independently, and both collapsed at once is a legal
// state — you get two header bars and an empty rail.
//
// Every function here is pure: the caller does the localStorage I/O and passes
// the raw string in. That keeps the whole decision layer testable without a
// DOM, and it means the pre-paint inline script and the runtime toggle share
// one definition of what a stored value means instead of two that drift.
const PANES_KEY = "agent-box:panesCollapsed";

// Stored state is a hostile input — hand-edited, written by an older version,
// or truncated by a full quota. Anything unreadable degrades to "nothing
// collapsed" rather than throwing: this runs in the pre-paint script, where an
// exception would block the rest of head parsing.
function loadCollapsed(raw) {
  const out = { sessions: false, files: false };
  if (!raw) return out;
  let v;
  try { v = JSON.parse(raw); } catch (_) { return out; }
  if (!v || typeof v !== "object" || Array.isArray(v)) return out;
  out.sessions = !!v.sessions;
  out.files = !!v.files;
  return out;
}

function saveCollapsed(state) {
  return JSON.stringify({ sessions: !!state.sessions, files: !!state.files });
}

// What a collapsed bar still says. A collapsed pane is not a hidden pane: it
// keeps the one fact you would have re-opened it to check.
//
// Sessions keeps its waiting count, because that is the only thing in the pane
// that demands action. With nothing waiting there is no pip at all — a dot
// announcing zero is worse than no dot.
//
// Files keeps its current root, because "which tree am I looking at" is the
// question a collapsed file pane otherwise hides.
function collapsedSummary(kind, info) {
  const i = info || {};
  if (kind === "sessions") {
    const n = Number(i.waiting) || 0;
    return n > 0 ? { text: n + " waiting", dot: true } : { text: "", dot: false };
  }
  return { text: i.root || "", dot: false };
}

// The splitter sizes one pane against the other. Collapsed, a pane is a fixed
// header bar, so there is nothing left to resize and the drag math would clamp
// against a degenerate rect.
function splitterLive(state) {
  return !state.sessions && !state.files;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { PANES_KEY, loadCollapsed, saveCollapsed, collapsedSummary, splitterLive };
}
