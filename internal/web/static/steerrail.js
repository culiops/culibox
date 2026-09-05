"use strict";

// The two rails' pure layer, kept out of app.js so `node --test` can reach it.
//
// This half is the icon rail: the column the sidebar becomes when it is hidden.
// It is a list of items, not markup, because the only decisions worth testing
// are which items exist, in what order, and what each one is called. Every item
// carries a label: the design gives a name to the toggle alone, and a status
// square with no accessible name is a control a screen reader announces as
// "button" and nothing more.

function railItems(sessions, ctx) {
  const c = ctx || {};
  const out = [{ kind: "toggle", label: "Show sidebar", glyph: "☰", on: false, alert: false }];
  for (const s of sessions || []) {
    const waiting = s && s.activity === "waiting";
    const name = (s && (s.name || s.id)) || "";
    out.push({
      kind: "session",
      id: s && s.id,
      // The state is in the name, not only in the colour: the whole reason this
      // rail exists is to say "this one needs you" with the sidebar shut.
      label: waiting ? name + " — waiting for you" : name,
      activity: (s && s.activity) || "",
      on: !!c.focusedId && !!s && s.id === c.focusedId,
      alert: !!waiting,
    });
  }
  out.push({ kind: "new", label: "New session", glyph: "+", on: false, alert: false });
  return out;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { railItems };
}
