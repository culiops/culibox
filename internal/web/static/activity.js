"use strict";

// activityClass maps a session's server-computed activity to a dot class and a
// hover title. Unknown/missing (an older payload, or a state we don't model yet)
// is muted, never a thrown error — the dot must degrade, not break the row.
function activityClass(activity) {
  switch (activity) {
    case "working":  return { cls: "act-working",  title: "working" };
    case "waiting":  return { cls: "act-waiting",  title: "waiting for input" };
    case "crashed":  return { cls: "act-crashed",  title: "crashed" };
    case "done":     return { cls: "act-done",     title: "exited" };
    case "detached": return { cls: "act-detached", title: "reconnecting" };
    default:         return { cls: "act-unknown",  title: "" };
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { activityClass };
}
