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
  if (s && (s.activity === "waiting" || s.need === "sso")) out.push("needs");
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

// canInterrupt: when Interrupt is offered. Only a live session has anything to
// stop. A claude also needs a turn to stop — working, or sitting on a prompt:
// Esc on an idle claude prompt does nothing useful, and two in a row open
// Claude Code's rewind menu. The server accepts any live session; this is the
// menu's judgement, not the gate.
function canInterrupt(s) {
  const v = s || {};
  if (v.status !== "live") return false;
  if (!v.claude) return true;
  return v.activity === "working" || v.need === "ask";
}

// menuItems: what the row's actions menu offers for THIS session.
//
// One item needs a claude transcript and one pair needs a working directory, so
// each is dropped where it would open onto nothing — a menu item that always
// fails is worse than an absent one. The control actions follow the session's
// state: Interrupt while there is something to stop (canInterrupt), the reply
// opt-in for a live claude, Restart for an ended one (it is listed, so it is
// still within retention), Duplicate always.
//
// "End session…" is last, alone in carrying danger, and its trailing ellipsis
// promises the confirm that follows. Both are house rules: destructive actions
// sit at the end, past everything reversible, and every destructive trigger says
// that it will ask. "Duplicate…" carries one too: it opens the spawn form.
function menuItems(s) {
  const v = s || {};
  const items = [{ id: "rename", label: "Rename…", danger: false }];
  if (v.cwd) items.push({ id: "files", label: "Open files here", danger: false });
  if (v.claude) items.push({ id: "attribution", label: "Context by tool", danger: false });
  if (v.cwd) items.push({ id: "copycwd", label: "Copy working directory", danger: false });
  items.push({ id: "newpane", label: "Open in new pane", danger: false });
  if (canInterrupt(v)) items.push({ id: "interrupt", label: "Interrupt", danger: false });
  if (v.claude && !v.exited) {
    items.push(v.replyable
      ? { id: "replies-off", label: "Stop replies", danger: false }
      : { id: "replies-on", label: "Allow replies", danger: false });
  }
  if (v.exited) items.push({ id: "restart", label: v.resumable ? "Restart (resume conversation)" : "Restart", danger: false });
  items.push({ id: "duplicate", label: "Duplicate…", danger: false });
  items.push({ id: "end", label: "End session…", danger: true });
  return items;
}

// PALETTE_ACTION_IDS: the menu actions the palette offers for the focused
// session, in the palette's order (spec §2.6). End session keeps its own
// per-session palette row.
const PALETTE_ACTION_IDS = ["interrupt", "rename", "replies-on", "replies-off", "restart", "duplicate", "newpane"];

// paletteActionItems: menuItems' decisions, re-ordered for the palette, with
// labels shorn of the "opens a form" ellipsis (the palette row names its
// session after a " · ").
function paletteActionItems(s) {
  const byId = new Map(menuItems(s).map((m) => [m.id, m]));
  return PALETTE_ACTION_IDS.filter((id) => byId.has(id))
    .map((id) => ({ id: id, label: byId.get(id).label.replace(/…$/, "") }));
}

// actionFailText: the toast for a failed interrupt/restart (status 0 = the
// request never completed).
function actionFailText(action, status) {
  if (status === 0) return "No connection — try again.";
  if (status === 404 || status === 410) return "That session is gone.";
  if (action === "interrupt" && status === 409) return "Nothing to interrupt — the session is not running.";
  if (action === "restart" && status === 409) return "Only an ended session can be restarted.";
  if (status === 429) return "Too soon — try again in a second.";
  return (action === "restart" ? "Restart" : "Interrupt") + " failed (" + status + ").";
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

// AWS binding (credential broker). The session carries s.aws = {label,
// account_id, role_arn, prod} only when its profile has a binding; every
// surface draws it from here so the sidebar, the sheets, the tab and the steer
// rail cannot disagree. The label is operator-typed free text: callers set it
// through textContent, never markup.
function roleName(arn) {
  const i = (arn || "").lastIndexOf("/");
  return i >= 0 ? arn.slice(i + 1) : (arn || "");
}

// hhmm: the local clock face of an ISO instant, "" when unparseable. Same
// local-time idiom as startedText in steerrail.js.
function hhmm(iso) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const d = new Date(t);
  return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
}

// minutesLeft: whole minutes until iso (negative once past), null when iso is
// not a time.
function minutesLeft(iso, nowMs) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return null;
  return Math.floor((t - nowMs) / 60000);
}

// isElevated: the session's write window is open. The list entry carries
// elevated_until only while the daemon holds a window; the comparison guards a
// stale entry between two polls, so a lapsed window never paints as write.
function isElevated(a, nowMs) {
  return !!a && !!a.elevated_until && Date.parse(a.elevated_until) > nowMs;
}

// NEW_CRED_MIN: below this many minutes left the broker mints no more write
// credentials (elevated mints need 16 min), so the row says so.
const NEW_CRED_MIN = 16;
// EXTEND_UNDER: Extend is offered when fewer than this many minutes are left.
const EXTEND_UNDER = 20;

// awsBadge: the short chip. Red (prod) is the operator's own flag on the
// binding, not something inferred from the account. `write` is a separate axis
// (amber outline), so a prod binding that is writing is both.
function awsBadge(s, nowMs = Date.now()) {
  const a = s && s.aws;
  if (!a) return null;
  const base = a.label || ("aws:" + a.account_id);
  if (isElevated(a, nowMs)) {
    return {
      text: base + " · write",
      prod: !!a.prod,
      write: true,
      title: "AWS " + a.account_id + " · " + roleName(a.elevate && a.elevate.role_arn) + " · write until " + hhmm(a.elevated_until),
    };
  }
  return {
    text: base,
    prod: !!a.prod,
    write: false,
    title: "AWS " + a.account_id + " · " + roleName(a.role_arn) + " · read-only",
  };
}

// WRITE_SUFFIX is what awsBadge appends while a window is open.
const WRITE_SUFFIX = " · write";

// badgeParts: the badge text as {label, write}, so the renderer can let the
// label ellipsize while the word "write" never does (it is the one part of the
// chip that must always be readable).
function badgeParts(ab) {
  if (!ab) return null;
  const text = String(ab.text || "");
  if (ab.write && text.endsWith(WRITE_SUFFIX)) return { label: text.slice(0, -WRITE_SUFFIX.length), write: true };
  return { label: text, write: false };
}

// awsSig: everything the AWS chip draws, as one string. A terminal tab keeps its
// strip until something tells it to rebuild; this is what tells it when a grant,
// a drop or a lapsed window changed the chip.
function awsSig(s, nowMs = Date.now()) {
  const a = s && s.aws;
  if (!a) return "";
  return [a.label || "", a.account_id || "", !!a.prod, isElevated(a, nowMs) ? a.elevated_until : ""].join("|");
}

// scopeRow: the scope row of the steer rail. Under a live window it reads the
// clock and minutes left (the elevation view only carries last_valid after a
// drop, so an active window is described from the list entry's elevated_until).
// After a drop, credentials already issued may outlive the grant: say until when.
function scopeRow(a, st, nowMs) {
  if (isElevated(a, nowMs)) {
    const m = minutesLeft(a.elevated_until, nowMs);
    let v = "write · until " + hhmm(a.elevated_until) + " (" + m + " min)";
    if (m < NEW_CRED_MIN) v += " · new credentials read-only";
    return { k: "scope", v: v, chip: "bdg aws write", wrap: true };
  }
  const e = st && st.elevation;
  if (e && e.active === false && e.last_valid && Date.parse(e.last_valid) > nowMs) {
    return { k: "scope", v: "read-only · issued write creds valid until " + hhmm(e.last_valid), wrap: true };
  }
  return { k: "scope", v: "read-only" };
}

// awsRailRows: the steer rail's view of the binding, as short rows rather than
// one long line: the rail's value cell is ~260px, and a single
// "label · account · role · read-only" string was ellipsized so that the end of
// it (read-only, production) was exactly what the reader could not see. The
// identifiers wrap; the scope is its own row; production is a row of its own
// drawn as the red chip, so no label length can push it out of view. While
// elevated, the role row names the write role in use.
function awsRailRows(s, st, nowMs = Date.now()) {
  const a = s && s.aws;
  if (!a) return [];
  const elevated = isElevated(a, nowMs);
  const rows = [
    { k: "aws", v: a.label ? a.label + " · " + a.account_id : String(a.account_id), wrap: true },
    { k: "role", v: roleName(elevated && a.elevate ? a.elevate.role_arn : a.role_arn), wrap: true },
    scopeRow(a, st, nowMs),
  ];
  if (a.prod) rows.push({ k: "prod", v: "production", chip: "bdg aws prod" });
  return rows;
}

// elevActions: which of Elevate / Extend / Drop the rail offers. Elevate needs
// a write role on the binding and no open window; Extend only near the end of a
// window (fewer than EXTEND_UNDER min left); Drop whenever a window is open.
function elevActions(a, nowMs = Date.now()) {
  const on = isElevated(a, nowMs);
  return {
    elevate: !!a && !!a.elevate && !on,
    extend: on && minutesLeft(a.elevated_until, nowMs) < EXTEND_UNDER,
    drop: on,
  };
}

// readAWSFields: the profile editor's AWS block as the object the server
// stores, or undefined when no role is given (an empty block means "no
// binding", not an invalid one). get(id) returns an input's value, or its
// checked state for the checkbox.
function readAWSFields(get) {
  const role = String(get("pf-aws-role") || "").trim();
  if (!role) return undefined;
  const egress = String(get("pf-aws-egress") || "").split("\n").map((x) => x.trim()).filter(Boolean);
  const out = {
    source_profile: String(get("pf-aws-profile") || "").trim(),
    role_arn: role,
    region: String(get("pf-aws-region") || "").trim(),
    s3_read: String(get("pf-aws-s3") || "").split("\n").map((x) => x.trim()).filter(Boolean),
    label: String(get("pf-aws-label") || "").trim(),
    prod: !!get("pf-aws-prod"),
  };
  // Only when there are hosts: an empty list must not reach the server, so a
  // binding without extra hosts keeps its pre-egress hash (and its box name).
  if (egress.length) out.egress_allow = egress;
  // The write role is optional: without one the binding is read-only and the
  // block is absent. A blank or non-numeric max falls back to the 60 min default.
  const elevRole = String(get("pf-aws-elev-role") || "").trim();
  if (elevRole) {
    out.elevate = { role_arn: elevRole, max_minutes: Number(get("pf-aws-elev-max")) || 60 };
    if (get("pf-aws-elev-iam")) out.elevate.allow_iam = true;
  }
  return out;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { attachedElsewhere, rowClasses, sessionMeta, menuItems, jumpTarget, canInterrupt, paletteActionItems, actionFailText, roleName, awsBadge, badgeParts, awsSig, awsRailRows, readAWSFields,
    hhmm, minutesLeft, isElevated, elevActions };
}
