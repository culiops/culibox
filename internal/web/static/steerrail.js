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
      // name is the session's own name, undecorated: it is what a newly-opened
      // tab gets titled, and a tab title is permanent until the operator renames
      // it by hand — nothing re-syncs it from a later poll. label carries the
      // "— waiting for you" suffix for the button's accessible name and tooltip
      // only; it must never reach a tab title.
      name: name,
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
