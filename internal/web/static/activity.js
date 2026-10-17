"use strict";

// activityClass maps a session's server-computed activity to a dot class and a
// hover title. Unknown/missing (an older payload, or a state we don't model yet)
// is muted, never a thrown error — the dot must degrade, not break the row.
//
// why is the optional reason a session reads "waiting" without the agent
// waiting for input (tabNeedWhy in attention.js): "sso" labels the dot "AWS
// sign-in needed" so a working agent is not described as waiting for input.
function activityClass(activity, why) {
  if (activity === "waiting" && why === "sso") return { cls: "dot waiting", title: "AWS sign-in needed" };
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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { activityClass };
}
