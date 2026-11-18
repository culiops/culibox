"use strict";

// insertVisible answers one question for every "insert a path at the prompt" action: is the terminal that would get
// the text on screen, where the user can see it land? Pure and DOM-free, so `node --test` reaches every state;
// insertdecision.js builds the input from the live layout, and no caller ever decides visibility itself (#337).
//
// Why one predicate: the first attempt (Files dock P1) patched one hidden state at a time, and four review rounds each
// found another place where text landed in a terminal nobody could see: the palette sharing the tree's function, a
// "same pane" rule that took another session's stacked tab, an inactive stacked tab (not in the page, socket open),
// then Full mode, a pane's Activity or Access view and a maximized neighbour. Each is a field below, and a field that
// is missing refuses, so a state the glue forgets to report can never read as visible.
//
// The input, a plain object (all of it is required; see the fail-closed note on insertVisible):
//   name     the session's label, for the sentence a refusal gives.
//   phone    the phone shell is showing (one screen at a time); see the phone rule below.
//   hidden   document.hidden: this browser tab is in the background.
//   page     the open page view ("settings-modal", "analytics-modal", "audit-page") or "".
//   placed   where the terminal lives: "tab" (a tab of a pane), "dock" (a shell docked in a pane) or "" (nowhere).
//   full     the Files dock is in Full mode, over the whole pane area (forced or chosen).
//   paneShown the terminal's pane is in the grid: false while another pane is maximized, which detaches it.
//   view     the pane's view: "terminal", "activity" or "access". A view covers the pane's own tab, not its dock.
//   front    whether this terminal is the one showing in its place:
//              "self"         it is.
//              "viewer"       a tab: a file tab is in front of it.
//              "tab"          a tab: another terminal tab is in front of it (an inactive stacked tab).
//              "dock-hidden"  a shell: the pane's dock is hidden.
//              "shell"        a shell: another docked shell is the one showing.
//   laidOut  the terminal's element is in the document and has a box. Not a state of its own so much as the backstop for
//            every state this list does not name (a collapsed pane, a display:none the CSS adds later).
//
// The phone is the one exception to the rule that "visible" means "on screen now", and it is deliberate: the phone
// shows one screen at a time (Sessions, Terminal, Files) and the Files screen inserts at the focused terminal that
// sits on the Terminal screen. That cross-screen insert is what the phone's tree is for, so it keeps working exactly as
// it did; the agent-state gate still applies to it (insertDecision). To refuse it too, delete the phone line below.

const INSERT_PAGE_NAMES = { "settings-modal": "Settings", "analytics-modal": "Analytics", "audit-page": "Audit" };

// insertVisible -> {ok, reason}. `reason` is the sentence a toast shows, empty when ok. It is fail-closed: it answers ok
// only when every field above says so explicitly (hidden false, page "", full false, paneShown true, laidOut true,
// front "self", and for a tab view "terminal"); a missing or unknown value refuses.
function insertVisible(state) {
  const s = state || {};
  const name = s.name || "The session";
  const no = (reason) => ({ ok: false, reason });
  if (s.placed !== "tab" && s.placed !== "dock") return no(name + " is not open in a pane; open it first");
  if (s.phone === true) return { ok: true, reason: "" };
  if (s.hidden !== false) return no("This browser tab is in the background; come back to it first");
  if (s.page !== "") return no((INSERT_PAGE_NAMES[s.page] || "A page") + " is open over the panes; close it first");
  if (s.full !== false) return no(name + "'s terminal is covered by the dock in Full mode; show the terminal first");
  if (s.paneShown !== true) return no(name + "'s pane is hidden while another pane is maximized; show it first");
  if (s.front !== "self") {
    const why = {
      viewer: name + "'s terminal is behind a file tab in its pane; show it first",
      tab: name + " is a background tab in its pane; show it first",
      "dock-hidden": name + "'s shell is hidden with its dock; show it first",
      shell: name + " is not the shell showing in its dock; show it first",
    }[s.front];
    return no(why || name + "'s terminal is not showing; show it first");
  }
  // A pane's Activity or Access view covers its own tab and leaves the dock showing.
  if (s.placed === "tab" && s.view !== "terminal") {
    const what = s.view === "activity" ? "Activity" : s.view === "access" ? "Access" : "a view";
    return no(name + "'s pane is showing " + what + ", not the terminal; show the terminal first");
  }
  if (s.laidOut !== true) return no(name + "'s terminal is not on screen; show it first");
  return { ok: true, reason: "" };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { insertVisible, INSERT_PAGE_NAMES };
}
