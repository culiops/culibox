"use strict";

// The ONE decision behind every "insert a path at the prompt" (#337): may text be typed into this terminal now?
// Two questions, asked in this order, answered with one sentence:
//   1. the agent-state gate (editorref.js insertGate): is the session waiting on the user for anything but a finished
//      turn? Text typed into a permission prompt or a sign-in lands where an answer is expected.
//   2. the visibility predicate (insertvisible.js insertVisible): is the terminal on screen, where the user sees the text
//      land? This file only reads the live objects into the predicate's input; it decides nothing about visibility.
// The tree's row menu, the palette, the viewers' Path menu and line-number insert and an upload's inserted paths all ask
// first, through insertRefused(term) below, and so must every insert added later (the editor's selection bar, the agent
// strip); on a refusal they show the sentence as a toast, type nothing and move no focus. insert-callers.test.js fails any
// insertTextAtPrompt call that does not ask first. The phone has no exemption: the same fields, read from the same objects.

// insertPlace: where `term` lives on this page, by identity. {cell, placed}: "tab" (a tab of a pane) or "dock" (a shell
// docked in a pane), or null for a terminal that is in no pane (closed, or evicted from this page).
function insertPlace(term) {
  for (const c of cells) {
    if (c.tabs.includes(term)) return { cell: c, placed: "tab" };
    for (const t of c.dockTabs.values()) if (t === term) return { cell: c, placed: "dock" };
  }
  return null;
}

// insertLaidOut: the terminal's element is in the document and has a box. The backstop under the named states: a pane
// the layout collapsed, or anything the CSS hides later, reads as no box here without this file knowing about it.
function insertLaidOut(term) {
  const el = term && term.el;
  if (!el || !el.isConnected || typeof el.getBoundingClientRect !== "function") return false;
  const r = el.getBoundingClientRect();
  return r.width > 0 && r.height > 0;
}

// insertViewState: the input of insertVisible for `term`, read from the real objects: the cells (tabs, dock, view),
// the maximized pane, the Files dock's Full mode, the open page and the page's own visibility.
function insertViewState(term, name) {
  const at = term ? insertPlace(term) : null;
  const dr = document.getElementById("drawer");
  const state = {
    name,
    hidden: !!document.hidden,
    page: typeof currentPage === "function" ? currentPage() : "",
    placed: at ? at.placed : "",
    // #drawer[data-max="1"] is Full mode, chosen or forced; a closed dock has no data-max (applyDrawer clears it).
    full: !!(dr && !dr.hidden && dr.dataset.max === "1"),
    paneShown: false,
    view: "",
    front: "",
    laidOut: insertLaidOut(term),
  };
  if (!at) return state;
  const cell = at.cell;
  const max = currentMaximized(); // the phone has none (effectiveMaximized): there the cells it hides are display:none, which laidOut reads
  state.paneShown = !max || max === cell;
  state.view = cell.view;
  if (at.placed === "tab") {
    const front = cell.activeTab;
    state.front = front === term ? "self" : front && front.kind === "viewer" ? "viewer" : "tab";
  } else {
    const shown = cell._shownShell(); // null while the dock is hidden
    state.front = shown === term ? "self" : shown ? "shell" : "dock-hidden";
  }
  return state;
}

// insertDecision(term) -> {ok, why}: the gate, then visibility. `why` is the toast's sentence ("" when ok). The session is
// the TARGET terminal's, never the focused one's: a path typed at A while B has the keyboard is judged by A's row. A
// session this page has no row for yet (just spawned, before the next poll) has an unknown state, which the gate refuses.
function insertDecision(term) {
  if (!term) return { ok: false, why: "There is no terminal to insert into" };
  const label = typeof term.title === "function" ? term.title() : term.name || term.id;
  const row = (lastSessions || []).find((s) => s.id === term.id) || { id: term.id, name: label };
  const gate = insertGate(row);
  if (!gate.ok) return { ok: false, why: gate.why };
  const seen = insertVisible(insertViewState(term, row.name || label));
  return seen.ok ? { ok: true, why: "" } : { ok: false, why: seen.reason };
}

// insertRefused(term, lead): the one line every insert path starts with, `if (insertRefused(term)) return;`. It asks
// insertDecision and, when that refuses, shows the sentence as a toast (after `lead`, for a caller whose own wording leads:
// the upload's "Uploaded, but the path was not inserted: ") and answers true. The caller then returns at once, having typed
// nothing and moved no focus.
function insertRefused(term, lead) {
  const seen = insertDecision(term);
  if (seen.ok) return false;
  showToast((lead || "") + seen.why, { error: true });
  return true;
}

// insertOffered: should a menu offer "Insert path at prompt" at all? Not on the phone: the tree is on the Files screen, which
// hides the terminals, and a pane viewer covers its own terminal, so insertDecision refuses every insert a phone menu could
// make. Offering a dead action is worse than not offering it. The palette's Ctrl/Cmd+Enter is not a menu: it goes through
// the decision on any screen (it passes on the Terminal screen, refuses with its toast elsewhere).
function insertOffered() {
  return !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { insertDecision, insertRefused, insertOffered };
}
