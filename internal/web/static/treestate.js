"use strict";

// Tree expand-state persistence: which directories are open, as a set of
// ABSOLUTE paths, saved to localStorage so a refresh or a focus-switch to
// another session restores the same expanded folders. Keyed on absolute path,
// not "the Nth row", so it is independent of which root the tree shows: the same
// folder re-expands whether reached from its own session or from another.
//
// Mirrors layout.js: pure mutators/decider testable under `node --test`, plus
// two storage touchpoints that swallow their errors so a browser with storage
// disabled loses the feature, not the app.
//
// The runtime structure is a Set, and that is load-bearing: a JS Set iterates in
// insertion order and re-adding an existing key does not move it, so the oldest
// entry is always the first one out — which is exactly the eviction the cap
// wants. Do not replace it with something that loses that order.

const TREESTATE_KEY = "agent-box:tree-expanded";
const TREESTATE_V = 1;
const TREESTATE_CAP = 500; // an upper bound on stored paths, not a UX limit; a
                           // user never opens this many, so trimming is a
                           // backstop against unbounded growth from stale paths.

function addExpanded(set, path) {
  if (!path || set.has(path)) return set;
  set.add(path);
  // One over at most, since we add one at a time: delete the single oldest.
  if (set.size > TREESTATE_CAP) set.delete(set.values().next().value);
  return set;
}

function removeExpanded(set, path) {
  set.delete(path);
  return set;
}

// forgetSubtree removes `path` and everything strictly beneath it. The trailing
// slash is the whole point: without it "/a/b" would also match "/a/bc", which is
// a sibling, not a child.
function forgetSubtree(set, path) {
  const under = path + "/";
  for (const p of set) {
    if (p === path || p.indexOf(under) === 0) set.delete(p);
  }
  return set;
}

function shouldAutoOpen(set, path) {
  return set.has(path);
}

function loadExpanded() {
  try {
    const raw = JSON.parse(localStorage.getItem(TREESTATE_KEY) || "null");
    if (!raw || raw.v !== TREESTATE_V || !Array.isArray(raw.paths)) return new Set();
    // A stored snapshot is user-editable text: keep only strings, drop the rest,
    // so one hand-edited number cannot poison membership tests downstream. Enforce
    // the cap here too — a normal save never exceeds it, but a hand-edited store
    // could, and nothing else trims on load. slice(-CAP) keeps the newest, which
    // matches addExpanded dropping the oldest first.
    const paths = raw.paths.filter((p) => typeof p === "string");
    return new Set(paths.slice(-TREESTATE_CAP));
  } catch (_) { return new Set(); }
}

function saveExpanded(set) {
  try {
    localStorage.setItem(TREESTATE_KEY, JSON.stringify({ v: TREESTATE_V, paths: [...set] }));
  } catch (_) {}
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    loadExpanded, saveExpanded, addExpanded, removeExpanded, forgetSubtree,
    shouldAutoOpen, TREESTATE_KEY,
  };
}
