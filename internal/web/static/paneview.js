"use strict";

// Pane views (desktop): a pane's body shows its Terminal, or one of two views of
// the pane's AGENT session over it, Activity or Access (spec 5.7, 5.8). Files
// is not a view: it stays the right drawer. Pure decisions, no DOM and no I/O.
// Classic script: the names below are browser globals used by tabs.js and
// app.js (so this loads before both); under node they are required.
//
// A view is page-local: it is never written to the layout snapshot, so a reload
// opens every pane on Terminal.

const PANE_VIEWS = ["terminal", "activity", "access"];

// paneViewRenderers: what fills a view's host, by kind. Each later task that
// builds a view's content registers one here from its own script:
//
//   paneViewRenderers.access = (host, sid) => ({ update(sid) {}, dispose() {} });
//
// `host` is the pane's empty div.pane-view, `sid` the agent session it shows.
// The renderer returns a handle (update and dispose both optional) or a bare
// teardown function. The host calls update(sid) on every session-list poll
// while the view stays up, and dispose() before it empties the host (back to
// Terminal, another session, another kind, the pane closing, the viewport
// crossing into mobile). A kind with no renderer gets a placeholder (tabs.js).
const paneViewRenderers = {};

// paneViewKind: `kind` if it names a view, else "terminal".
function paneViewKind(kind) {
  return PANE_VIEWS.includes(kind) ? kind : "terminal";
}

// paneViewFor: the pane's next view when a header tab is pressed. cellState is
// { view, hasSession }: the view showing now, and whether the pane has a
// terminal tab (cellTerminal) whose session a view could describe. Without one
// there is only Terminal. Terminal is always Terminal; the view already showing
// returns to Terminal; any other view opens; a press that names no view (Files
// is the drawer's) leaves the view where it is.
function paneViewFor(cellState, kind) {
  const st = cellState || {};
  if (!st.hasSession) return "terminal";
  const cur = paneViewKind(st.view);
  if (!PANE_VIEWS.includes(kind)) return cur;
  if (kind === "terminal" || kind === cur) return "terminal";
  return kind;
}

// paneViewTarget: which pane, and which session in it, a view opened for `sid`
// lands on. model.cells are the grid's panes, each { term, tabs, dock }: term is
// the pane's agent (tabs.js cellTerminal), tabs its tabs ({kind, id}), dock its
// companion-shell dock ({ids}) or null. A docked shell resolves to the agent its
// dock hangs off: a view always describes the pane's agent, never a shell docked
// beside it. No `sid` means the focused pane (focusIndex). null when no pane
// shows the session (the caller opens it first) or the pane has no agent.
function paneViewTarget(model, sid, focusIndex) {
  const cells = (model && model.cells) || [];
  if (sid == null) {
    const c = cells[focusIndex];
    return c && c.term ? { cellIndex: focusIndex, sid: c.term } : null;
  }
  for (let i = 0; i < cells.length; i++) {
    const c = cells[i];
    if (!c) continue;
    const docked = !!(c.dock && Array.isArray(c.dock.ids) && c.dock.ids.includes(sid));
    const tab = (c.tabs || []).some((t) => t && t.kind === "terminal" && t.id === sid);
    if (docked) return c.term ? { cellIndex: i, sid: c.term } : null;
    if (tab) return { cellIndex: i, sid };
  }
  return null;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { PANE_VIEWS, paneViewRenderers, paneViewKind, paneViewFor, paneViewTarget };
}
