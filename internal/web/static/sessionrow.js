"use strict";

// One decision about a session row, kept out of app.js so `node --test` can
// reach it. Shared logic in app.js is unreachable by every test in this repo,
// which is how a deleted helper silently broke three files earlier this week.

// attachedElsewhere reports that a viewer holds this session and it is not this
// page. The server says only THAT someone is subscribed — it has no identity to
// report — so whether that someone is us is a question only the page can answer,
// which is why the caller passes it in.
//
// An exited session never qualifies: who was watching something already over is
// not something anyone can act on. A response with no `attached` field (an older
// daemon behind a newer page) reads as not attached, so the badge can never
// appear on a claim the server never made.
function attachedElsewhere(session, heldHere) {
  if (!session || session.exited) return false;
  return session.attached === true && !heldHere;
}

// rowClasses: the row's full class list. Two states sit above the base and they
// are DIFFERENT axes — "needs you" is a fact about the agent, "selected" is a
// fact about where your keystrokes go — so a row can legitimately carry both.
//
// A teardown in flight outranks both: the row is disabled while it happens, and
// painting it as answerable advertises a session that is already going away.
//
// ctx.selectedId is null whenever the focused pane is not showing an agent's
// terminal (a file viewer, or no tabs at all). That is a real state, not a
// missing value: nothing is selected, and no row lights up.
function rowClasses(s, ctx) {
  const c = ctx || {};
  const out = ["srow"];
  if (c.killing) { out.push("killing"); return out; }
  if (s && c.selectedId && s.id === c.selectedId) out.push("sel");
  if (s && s.activity === "waiting") out.push("needs");
  return out;
}

// sessionMeta: the row's second line — what it is, what it is doing, where it
// is. One line, clipped by CSS, never wrapped.
//
// The status segment is the activity's human phrase, not an exit code: the
// session record carries no exit code, so a numeric one would have to be
// invented. Empty segments are dropped rather than rendered as a dangling
// separator, which is what "claude ·  · ~/x" would look like.
function sessionMeta(s, cwdText, statusTitle) {
  const parts = [
    s && s.agent ? String(s.agent) : "",
    statusTitle ? String(statusTitle) : "",
    cwdText ? String(cwdText) : "",
  ];
  return parts.filter((p) => p !== "").join(" · ");
}

// menuItems: what the row's actions menu offers for THIS session.
//
// Two items need a claude transcript and one pair needs a working directory, so
// each is dropped where it would open onto nothing — a menu item that always
// fails is worse than an absent one.
//
// "End session…" is last, alone in carrying danger, and its trailing ellipsis
// promises the confirm that follows. Both are house rules: destructive actions
// sit at the end, past everything reversible, and every destructive trigger says
// that it will ask.
function menuItems(s) {
  const v = s || {};
  const items = [{ id: "rename", label: "Rename…", danger: false }];
  if (v.cwd) items.push({ id: "files", label: "Open files here", danger: false });
  if (v.claude) items.push({ id: "attribution", label: "Context by tool", danger: false });
  if (v.cwd) items.push({ id: "copycwd", label: "Copy working directory", danger: false });
  items.push({ id: "newpane", label: "Open in new pane", danger: false });
  items.push({ id: "end", label: "End session…", danger: true });
  return items;
}

// jumpTarget: which session a number key addresses. 1-based over the sidebar
// list, because the sidebar is the numbered thing on screen; the grid's panes
// are not a usable index (there can be fewer panes than sessions, and one pane
// can stack several).
function jumpTarget(n, sessions) {
  const list = Array.isArray(sessions) ? sessions : [];
  const i = Number(n);
  if (!Number.isInteger(i) || i < 1 || i > list.length) return null;
  return list[i - 1] || null;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { attachedElsewhere, rowClasses, sessionMeta, menuItems, jumpTarget };
}
