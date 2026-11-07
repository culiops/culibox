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
  if (s && (s.activity === "waiting" || s.need === "sso" || s.need === "store_locked")) out.push("needs");
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
//
// opts.nested: the session is a companion shell listed under its parent (desktop
// sidebar). Only the actions that mean something for a shell stay (rename,
// files, copy cwd, new pane, end); restart, duplicate, interrupt, the reply
// opt-in and context-by-tool do not.
const NESTED_SHELL_ACTIONS = ["rename", "files", "copycwd", "newpane", "end"];

function menuItems(s, opts) {
  const v = s || {};
  const items = [{ id: "rename", label: "Rename…", danger: false }];
  if (v.cwd) items.push({ id: "files", label: "Open files here", danger: false });
  if (v.claude) items.push({ id: "attribution", label: "Context by tool", danger: false });
  if (v.cwd) items.push({ id: "copycwd", label: "Copy working directory", danger: false });
  items.push({ id: "newpane", label: "Open in new pane", danger: false });
  if (canInterrupt(v)) items.push({ id: "interrupt", label: "Interrupt", danger: false });
  // Kill-switch: no reply opt-in item (the palette derives from this list).
  if (globalThis.repliesEnabled === true && v.claude && !v.exited) {
    items.push(v.replyable
      ? { id: "replies-off", label: "Stop replies", danger: false }
      : { id: "replies-on", label: "Allow replies", danger: false });
  }
  if (v.exited) items.push({ id: "restart", label: v.resumable ? "Restart (resume conversation)" : "Restart", danger: false });
  // A clone's work can be brought home on request; the card only exists in a box.
  if (v.clone_of && v.runtime === "box") items.push({ id: "bringhome", label: "Bring work home now", danger: false });
  items.push({ id: "duplicate", label: "Duplicate…", danger: false });
  items.push({ id: "end", label: "End session…", danger: true });
  if (opts && opts.nested) return items.filter((m) => NESTED_SHELL_ACTIONS.indexOf(m.id) >= 0);
  return items;
}

// MENU_GROUP_OF: which of the kit menu's four groups an item sits in (frame M5.1): the name on
// its own, where the session is (files, context, copy cwd, new pane), what can be done to it
// (interrupt, replies, restart, bring home, duplicate), and the way out. menuItems already
// returns the items in this order; the groups only say where the hairlines go.
const MENU_GROUP_OF = {
  rename: 0,
  files: 1, attribution: 1, copycwd: 1, newpane: 1,
  interrupt: 2, "replies-on": 2, "replies-off": 2, restart: 2, bringhome: 2, duplicate: 2,
  end: 3,
};

// menuGroups: menuItems' list split into the groups the desktop menu separates, empty ones
// dropped, so a separator is only ever BETWEEN two groups that both have something in them (no
// double line, none first or last). Order within a group is the input's. An item whose id the
// table does not know joins the control actions, or the last group when it is the danger one:
// a menu item must not vanish because it was added without a row here.
function menuGroups(items) {
  const groups = [[], [], [], []];
  for (const it of items || []) {
    // Own properties only: an id such as "constructor" must not find Object.prototype's member.
    const known = Object.prototype.hasOwnProperty.call(MENU_GROUP_OF, it.id);
    const g = it.danger ? 3 : known ? MENU_GROUP_OF[it.id] : 2;
    groups[g].push(it);
  }
  return groups.filter((g) => g.length > 0);
}

// MENU_ICON: the drawing in each row's 16px icon slot, as a KIND the DOM code (menuIconSvg in
// app.js) draws as inline SVG in the product's icon style. A font glyph is not used: the frame's
// placeholder characters are tofu wherever no installed font has them. Reply opt-in and Restart
// have none drawn, so they keep an empty slot and their text lines up with the rest. One kind per
// id; Copy working directory and Duplicate are neighbours, so they differ (Duplicate adds a plus).
const MENU_ICON = {
  rename: "pencil", files: "files", attribution: "pie", copycwd: "copy", newpane: "split",
  interrupt: "pause", bringhome: "tray", duplicate: "duplicate", end: "end",
};

// menuIcon: the icon kind for a menuItems id, "" where there is none.
function menuIcon(id) {
  return Object.prototype.hasOwnProperty.call(MENU_ICON, id) ? MENU_ICON[id] : "";
}

// menuNavIndex: where the arrow keys move focus inside the open menu. `count` rows, focus on row
// `at` (-1 when it is on none of them, e.g. a tab switcher's close button). Down and Up wrap; from
// outside the rows Down lands on the first and Up on the last; Home and End jump. null means the
// key is not the menu's, so the caller leaves it alone.
function menuNavIndex(count, at, key) {
  if (!(count > 0)) return null;
  if (key === "Home") return 0;
  if (key === "End") return count - 1;
  if (key === "ArrowDown") return at < 0 ? 0 : (at + 1) % count;
  if (key === "ArrowUp") return at < 0 ? count - 1 : (at - 1 + count) % count;
  return null;
}

// MENU_GAP / ROW_MENU_GAP: the space between what a menu hangs from and the menu. A ⋯ button leaves 4px
// (the kit's popover offset); a sidebar row leaves 5: the 1px between two rows plus the same 4px, where
// m5-1's anchor sits (a zero-height sibling after the row, the menu 4px below it).
const MENU_GAP = 4;
const ROW_MENU_GAP = 5;

// menuPlace: where #session-menu goes ({left, top} in px). `r` is what it hangs from ({right, top,
// bottom}): the ⋯ that opened it, or the whole row when the ⋯ sits in a sidebar row. The menu's right
// edge is r's, `gap` px below it; it opens above r instead when it would reach into the viewport's last
// 8px, and keeps 8px from the viewport's left and right edges. w and h are the menu's own size.
function menuPlace(r, w, h, vw, vh, gap) {
  const left = Math.max(8, Math.min(vw - w - 8, r.right - w));
  const below = r.bottom + gap;
  const top = below + h <= vh - 8 ? below : Math.max(8, r.top - h - 4);
  return { left, top };
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
  const verb = action === "restart" ? "Restart" : action === "bringhome" ? "Bring work home" : "Interrupt";
  return verb + " failed (" + status + ").";
}

// restartNotice: the toast for a restart that could not continue the old
// conversation (a boxed claude whose transcript is no longer in its box). The
// server sets resume_skipped on the new session's info; the wording here is the
// UI's own. "" when there is nothing to say.
function restartNotice(info) {
  if (!info || !info.resume_skipped) return "";
  return "Started a new conversation: the previous one is not in this box anymore";
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

// roleText: the role a binding shows. An sso_direct binding may have no role
// name (the spawn carries the account and the hours, not the ARN), so the mode
// label stands in for it rather than leaving an empty role.
function roleText(a, arn) {
  const r = roleName(arn);
  return r || (a && a.mode === SSO_DIRECT ? SSO_DIRECT_LABEL : "");
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

// BASE_REASON mirrors awsprobe.ReasonText (and the table in the box's
// agentbox-env.js; a Go test keeps the three equal). Any other value reads
// "check failed": a reason is only ever one of these seven short words.
const BASE_REASON = {
  sso_expired: "SSO expired",
  assume_denied: "assume-role denied",
  simulate_denied: "role cannot simulate itself",
  timeout: "timed out",
  error: "check failed",
  not_checked: "not checked",
  probe_disabled: "probe disabled",
};

function baseReasonText(r) {
  return Object.prototype.hasOwnProperty.call(BASE_REASON, r) ? BASE_REASON[r] : "check failed";
}

// SSO_DIRECT is the AWS binding mode that hands the agent the operator's own SSO
// permission-set credentials behind the read-only gate. The wire's base_access is
// "none_found" for it (there is no base-role probe); every surface keys on mode.
const SSO_DIRECT = "sso_direct";
const SSO_DIRECT_LABEL = "SSO role directly";

// baseState: the base role's measured write access of a binding, from the wire
// fields base_access / base_write_actions / base_access_reason. A missing or
// unknown state (an older daemon, a sidecar from before the probe) is
// unverified, never "no writes".
function baseState(a) {
  const x = a || {};
  if (x.mode === SSO_DIRECT) return SSO_DIRECT;
  if (x.base_access === "writes" || x.base_access === "none_found") return x.base_access;
  return "unverified";
}

function baseActions(a) {
  return a && Array.isArray(a.base_write_actions) ? a.base_write_actions.filter((x) => typeof x === "string") : [];
}

// baseUnverifiedReason: why the state is unverified. Only an unverified record
// has a reason; anything else (missing, unknown state) was never checked.
function baseUnverifiedReason(a) {
  return a && a.base_access === "unverified" && a.base_access_reason ? a.base_access_reason : "not_checked";
}

// baseScope: what the base role can do, as {state, text, title}. A clean result
// is "no broad writes found", never a claim of read-only: the probe lists a
// short fixed set of broad write actions and does not see scoped grants.
function baseScope(a) {
  const state = baseState(a);
  if (state === SSO_DIRECT) {
    const h = Number(a.hours) > 0 ? " \u00b7 up to " + Math.ceil(Number(a.hours)) + " h" : "";
    return { state, text: "read-only gate passed" + h + " \u00b7 " + SSO_DIRECT_LABEL,
      title: "the permission set passed the read-only gate at spawn \u00b7 CloudTrail shows the operator, not the agent \u00b7 not revocable when the session ends" };
  }
  const at = hhmm(a && a.base_checked_at);
  const checked = at ? "checked " + at + " \u00b7 " : "";
  if (state === "writes") {
    const acts = baseActions(a);
    return { state, text: acts.length ? "role can write: " + acts.join(", ") : "role can write",
      title: checked + "IAM policy simulation" };
  }
  if (state === "none_found") {
    return { state, text: "no broad writes found", title: checked + "scoped write grants are not detected" };
  }
  return { state, text: "unverified (" + baseReasonText(baseUnverifiedReason(a)) + ")",
    title: "the base role's write access could not be checked" };
}

// baseChipTail: the end of the chip's title for a binding with no open window.
function baseChipTail(a) {
  const b = baseScope(a);
  if (b.state === "writes" || b.state === SSO_DIRECT) return b.text;
  if (b.state === "none_found") return b.text + " (scoped write grants are not detected)";
  return "base role unverified (" + baseReasonText(baseUnverifiedReason(a)) + ")";
}

// ROLE_WRITE_SUFFIX is what awsBadge appends while the base role can write and
// no window is open (dashed amber; the solid " \u00b7 write" is an open window).
const ROLE_WRITE_SUFFIX = " \u00b7 can write";

// awsBadge: the short chip. Red (prod) is the operator's own flag on the
// binding, not something inferred from the account. `write` is a separate axis
// (amber outline), so a prod binding that is writing is both.
function awsBadge(s, nowMs = Date.now()) {
  const p = awsBadgeParts(s, nowMs);
  if (p) return { text: p.label + (p.write ? WRITE_SUFFIX : p.roleWrite ? ROLE_WRITE_SUFFIX : ""), prod: p.prod, write: p.write, roleWrite: p.roleWrite, title: p.title };
  const a = s && s.aws;
  if (!a) return null;
  const base = a.label || ("aws:" + a.account_id);
  if (isElevated(a, nowMs)) {
    return {
      text: base + " · write",
      prod: !!a.prod,
      write: true,
      roleWrite: false,
      title: ["AWS " + a.account_id, roleName(a.elevate && a.elevate.role_arn), "write until " + hhmm(a.elevated_until)].filter(Boolean).join(" · "),
    };
  }
  const roleWrite = baseState(a) === "writes";
  return {
    text: base + (roleWrite ? ROLE_WRITE_SUFFIX : ""),
    prod: !!a.prod,
    write: false,
    roleWrite,
    // The role part is dropped when empty (sso_direct): the tail names the mode.
    title: ["AWS " + a.account_id, roleName(a.role_arn), baseChipTail(a)].filter(Boolean).join(" \u00b7 "),
  };
}

// connList: the session's named AWS connections ([] for a legacy or unbound
// session, which carry s.aws or nothing).
function connList(s) {
  return s && Array.isArray(s.aws_connections) ? s.aws_connections : [];
}

// awsBadgeParts: the chip of a session with named connections, null without
// any. One connection reads as its own label (its name when unlabelled); two or
// more as "AWS ×N", the names in the title. Red when ANY bound connection is
// production, amber when any has a window open: the chip is the one place a
// reader sees either without opening the rail. Dashed amber (roleWrite) when no
// window is open and a base role can write; an open window wins that chip.
function awsBadgeParts(s, nowMs = Date.now()) {
  const cs = connList(s);
  if (!cs.length) return null;
  const prod = cs.some((c) => c.prod);
  const live = cs.filter((c) => isElevated(c, nowMs));
  const write = live.length > 0;
  const idle = cs.filter((c) => !isElevated(c, nowMs));
  const roleWrite = !write && idle.some((c) => baseState(c) === "writes");
  if (cs.length === 1) {
    const c = cs[0];
    const base = ["AWS " + c.account_id, roleName(write && c.elevate ? c.elevate.role_arn : c.role_arn)].filter(Boolean).join(" \u00b7 ");
    return { label: c.label || c.name, prod, write, roleWrite,
      title: base + (write ? " \u00b7 write until " + hhmm(c.elevated_until) : " \u00b7 " + baseChipTail(c)) };
  }
  const names = (state) => idle.filter((c) => baseState(c) === state).map((c) => c.name).join(", ");
  const canWrite = names("writes");
  const unverified = names("unverified");
  const direct = names(SSO_DIRECT);
  return { label: "AWS \u00d7" + cs.length, prod, write, roleWrite,
    title: "AWS connections: " + cs.map((c) => c.name).join(", ") +
      live.map((c) => " \u00b7 write on " + c.name + " until " + hhmm(c.elevated_until)).join("") +
      (canWrite ? " \u00b7 role can write on " + canWrite : "") +
      (unverified ? " \u00b7 unverified on " + unverified : "") +
      (direct ? " \u00b7 " + SSO_DIRECT_LABEL + " on " + direct : "") };
}

// WRITE_SUFFIX is what awsBadge appends while a window is open.
const WRITE_SUFFIX = " · write";

// badgeParts: the badge text as {label, write, roleWrite}, so the renderer can
// let the label ellipsize while the suffix (" \u00b7 write" or " \u00b7 can write") never
// does (it is the one part of the chip that must always be readable).
function badgeParts(ab) {
  if (!ab) return null;
  const text = String(ab.text || "");
  if (ab.write && text.endsWith(WRITE_SUFFIX)) return { label: text.slice(0, -WRITE_SUFFIX.length), write: true, roleWrite: false };
  if (ab.roleWrite && text.endsWith(ROLE_WRITE_SUFFIX)) return { label: text.slice(0, -ROLE_WRITE_SUFFIX.length), write: false, roleWrite: true };
  return { label: text, write: false, roleWrite: false };
}

// baseSig: the part of the chip's signature that the base-role result decides.
// The state, the actions and the reason move it; the check time does not, so a
// re-check that finds the same thing does not rebuild every tab.
function baseSig(a) {
  const st = baseState(a);
  return [st, st === "writes" ? baseActions(a).join(",") : "", st === "unverified" ? baseUnverifiedReason(a) : st === SSO_DIRECT ? String(Number(a.hours) || 0) : ""].join("/");
}

// awsSig: everything the AWS chip draws, as one string. A terminal tab keeps its
// strip until something tells it to rebuild; this is what tells it when a grant,
// a drop or a lapsed window changed the chip.
function awsSig(s, nowMs = Date.now()) {
  const cs = connList(s);
  if (cs.length) {
    return cs.map((c) => [c.name, c.label || "", c.account_id || "", !!c.prod, isElevated(c, nowMs) ? c.elevated_until : "", baseSig(c)].join("|")).join(";");
  }
  const a = s && s.aws;
  if (!a) return "";
  return [a.label || "", a.account_id || "", !!a.prod, isElevated(a, nowMs) ? a.elevated_until : "", baseSig(a)].join("|");
}

// scopeRow: the scope row of the steer rail. Under a live window it reads the
// clock and minutes left (the elevation view only carries last_valid after a
// drop, so an active window is described from the list entry's elevated_until).
// After a drop, credentials already issued may outlive the grant: say until when.
// With no window the row is the base role's measured access (baseScope), and a
// base role that can write is drawn as the dashed amber chip.
function scopeRow(a, st, nowMs) {
  if (isElevated(a, nowMs)) {
    const m = minutesLeft(a.elevated_until, nowMs);
    let v = "write · until " + hhmm(a.elevated_until) + " (" + m + " min)";
    if (m < NEW_CRED_MIN) v += " · new credentials from the base role";
    return { k: "scope", v: v, chip: "bdg aws write", wrap: true };
  }
  const b = baseScope(a);
  const row = { k: "scope", v: b.text, title: b.title };
  if (b.state === "writes") { row.chip = "bdg aws rolew"; row.wrap = true; }
  if (b.state === "unverified") row.wrap = true;
  const e = st && st.elevation;
  if (e && e.active === false && e.last_valid && Date.parse(e.last_valid) > nowMs) {
    row.v = b.text + " · issued write creds valid until " + hhmm(e.last_valid);
    row.wrap = true;
  }
  return row;
}

// awsRailRows: the steer rail's view of the binding, as short rows rather than
// one long line: the rail's value cell is ~260px, and a single
// "label · account · role · scope" string was ellipsized so that the end of
// it (the scope, production) was exactly what the reader could not see. The
// identifiers wrap; the scope is its own row; production is a row of its own
// drawn as the red chip, so no label length can push it out of view. While
// elevated, the role row names the write role in use.
function awsRailRows(s, st, nowMs = Date.now()) {
  const a = s && s.aws;
  if (!a) return [];
  const elevated = isElevated(a, nowMs);
  const rows = [
    { k: "aws", v: a.label ? a.label + " · " + a.account_id : String(a.account_id), wrap: true },
    { k: "role", v: roleText(a, elevated && a.elevate ? a.elevate.role_arn : a.role_arn), wrap: true },
    scopeRow(a, st, nowMs),
  ];
  if (a.prod) rows.push({ k: "prod", v: "production", chip: "bdg aws prod" });
  return rows;
}

// liveWindowConn: which connection holds the session's one write window, as
// {name, until}, or null. The list entry (fresh each poll) is asked first; the
// cached /aws status is the fallback for the moment between a grant and the next
// poll, and counts only while its own `until` is in the future: the status is
// refetched on focus and after a grant or drop, not as time passes, so its
// `active` flag alone goes stale when a window lapses.
function liveWindowConn(conns, st, nowMs) {
  const cs = Array.isArray(conns) ? conns : [];
  const c = cs.find((x) => isElevated(x, nowMs));
  if (c) return { name: c.name, until: c.elevated_until };
  const e = st && st.elevation;
  if (e && e.active && e.name && Date.parse(e.until) > nowMs) return { name: e.name, until: e.until };
  return null;
}

// connWindowLine: the Write access section's line for the connection that has a
// window, or whose issued write credentials outlive a drop: {name, text}, null
// with neither. The text is the legacy scopeRow's, not a copy of it, so the
// countdown, the "new credentials from the base role" note under NEW_CRED_MIN and the
// after-drop "issued write creds valid until" cannot drift from the single-
// binding rail. The status's elevation view is for one connection (its name);
// it is only offered to scopeRow for that one.
function connWindowLine(conns, st, nowMs) {
  const w = liveWindowConn(conns, st, nowMs);
  if (w) return { name: w.name, text: scopeRow({ elevated_until: w.until }, null, nowMs).v };
  const e = st && st.elevation;
  if (e && e.active === false && e.name && e.last_valid && Date.parse(e.last_valid) > nowMs) {
    const named = (Array.isArray(conns) ? conns : []).find((x) => x.name === e.name);
    return { name: e.name, text: scopeRow(named || {}, { elevation: e }, nowMs).v };
  }
  return null;
}

// CONN_COLLAPSE: how many connection blocks the rail draws before it offers
// "Show all N".
const CONN_COLLAPSE = 6;

// connStatLine: line 3 of a connection block, from the broker's counters for it
// (GET .../aws connections[].stats). The last error wins and is shown whole: it
// carries the actionable sign-in hint. "" until the counters are loaded, so a
// block never claims "0 mints" it was not told.
function connStatLine(stats, nowMs) {
  if (!stats) return { stat: "", err: false };
  if (stats.last_error) return { stat: String(stats.last_error), err: true };
  const n = Number(stats.mints) || 0;
  const t = Date.parse(stats.last_mint);
  // The legacy AWS row's own helper (clock and age, "05:20 \u00b7 3m ago"), so the two read alike.
  const started = typeof startedText === "function" ? startedText : require("./steerrail.js").startedText;
  const last = Number.isFinite(t) && t > Date.UTC(2000, 0, 1) ? started(stats.last_mint, nowMs) : "never";
  return { stat: n + (n === 1 ? " mint" : " mints") + " \u00b7 last mint " + last, err: false };
}

// awsConnRailRows: the steer rail's blocks, one per named connection: prod
// first, then by name. Each block is
//   line 1  name + chips [prod] [write until HH:MM] [SSO expired]
//   line 2  label · account · role-name · region   (muted; the write role while elevated)
//   line 3  the base role's measured write access (none while a window is open)
//   line 4  N mints · last mint HH:MM, or the last error
// Past CONN_COLLAPSE blocks and not `expanded`, the first six and a "more" row
// whose n is the TOTAL ("Show all N"); expanded, every block and a "less" row.
// `k`/`v` make every row comparable for the rail's repaint key: v carries all
// that the block draws. Elevation and the SSO flag come from the list entry
// (fresh each poll); only the counters come from the /aws status `st`.
function awsConnRailRows(s, st, nowMs = Date.now(), expanded = false) {
  const cs = connList(s).slice().sort((a, b) => (b.prod ? 1 : 0) - (a.prod ? 1 : 0) || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  const byName = new Map((st && Array.isArray(st.connections) ? st.connections : []).map((c) => [c.name, c]));
  const blocks = cs.map((c) => {
    const live = isElevated(c, nowMs);
    const chips = [];
    if (c.prod) chips.push({ text: "prod", chip: "bdg aws prod" });
    if (live) chips.push({ text: "write until " + hhmm(c.elevated_until), chip: "bdg aws write" });
    if (c.sso_expired) chips.push({ text: "SSO expired", chip: "bdg dgr" });
    const detail = [c.label, c.account_id, roleText(c, live && c.elevate ? c.elevate.role_arn : c.role_arn), c.region].filter(Boolean).join(" \u00b7 ");
    const { stat, err } = connStatLine((byName.get(c.name) || {}).stats, nowMs);
    // While a window is open the base role is not what the session holds.
    const b = live ? null : baseScope(c);
    const base = b ? b.text : "";
    const baseTitle = b ? b.title : "";
    const baseWarn = !!b && b.state === "writes";
    return { kind: "conn", k: "aws", name: c.name, chips, detail, base, baseWarn, baseTitle, stat, err,
      v: [c.name, chips.map((x) => x.text).join(","), detail, base, baseTitle, stat].join("\u241f") };
  });
  if (blocks.length <= CONN_COLLAPSE) return blocks;
  if (expanded) return blocks.concat([{ kind: "less", k: "aws", v: "less" }]);
  return blocks.slice(0, CONN_COLLAPSE).concat([{ kind: "more", k: "aws", n: blocks.length, v: "more " + blocks.length }]);
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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { SSO_DIRECT_LABEL, NEW_CRED_MIN, attachedElsewhere, rowClasses, sessionMeta, menuItems, menuGroups, menuIcon, menuNavIndex, menuPlace, MENU_GAP, ROW_MENU_GAP, jumpTarget, canInterrupt, paletteActionItems, actionFailText, restartNotice, roleName, roleText, awsBadge, badgeParts, baseScope, baseReasonText, BASE_REASON, ROLE_WRITE_SUFFIX, awsSig, awsRailRows, awsConnRailRows, awsBadgeParts, liveWindowConn, connWindowLine,
    hhmm, minutesLeft, isElevated, elevActions };
}
