"use strict";

// activityClass maps a session's server-computed activity to a dot class and a
// hover title. Unknown/missing (an older payload, or a state we don't model yet)
// is muted, never a thrown error — the dot must degrade, not break the row.
//
// why is the optional reason a session reads "waiting" without the agent
// waiting for input (tabNeedWhy in attention.js): "sso" labels the dot "AWS
// sign-in needed" ("store": "Store locked") so a working agent is not described as waiting for input.
function activityClass(activity, why) {
  if (activity === "waiting" && why === "sso") return { cls: "dot waiting", title: "AWS sign-in needed" };
  if (activity === "waiting" && why === "store") return { cls: "dot waiting", title: "Store locked" };
  switch (activity) {
    case "working":  return { cls: "dot working",  title: "working" };
    case "waiting":  return { cls: "dot waiting",  title: "waiting for input" };
    case "idle":     return { cls: "dot idle",     title: "idle" };
    case "crashed":  return { cls: "dot crashed",  title: "crashed" };
    case "done":     return { cls: "dot done",     title: "exited" };
    case "detached": return { cls: "dot detached", title: "reconnecting" };
    default:         return { cls: "dot unknown",  title: "" };
  }
}

// connStateLabel: the one wording for a session's connection state, on the
// desktop sidebar row and its dot tooltip (F55). "detached" is a session the
// daemon has lost hold of ("reconnecting" alone, the phone's dot title above, left
// out that it is the daemon side); "taken-over" is a session whose terminal in
// this browser another tab or window took over; "offline" is the page having
// lost the server (Plan 7 draws it; it sits in the map so the three share one
// vocabulary). Anything else has no connection word: "". The phone's dot titles
// in activityClass are NOT these and stay as they are. Plan 7 reuses this map on
// its pane bars.
function connStateLabel(state) {
  switch (state) {
    case "detached":   return "detached \u00b7 reconnecting";
    case "taken-over": return "open in another tab";
    case "offline":    return "offline";
    default:           return "";
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { activityClass, connStateLabel };
}
