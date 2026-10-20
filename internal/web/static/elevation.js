"use strict";

// Pure models for write elevation and the SSO sign-in card. DOM wiring lives in
// app.js; everything here is testable under node --test. Loaded after
// attention.js (it shares the SSO sentence) and sessionrow.js (roleName, hhmm).

// ELEV_WINDOWS: the window lengths the grant panel offers, capped by the
// binding's own max_minutes.
const ELEV_WINDOWS = [30, 60];

function elevRoleName(arn) {
  return typeof roleName === "function" ? roleName(arn) : require("./sessionrow.js").roleName(arn);
}
function elevHHMM(iso) {
  return typeof hhmm === "function" ? hhmm(iso) : require("./sessionrow.js").hhmm(iso);
}
function elevSSOText(s) {
  return typeof ssoExpiredText === "function" ? ssoExpiredText(s) : require("./attention.js").ssoExpiredText(s);
}

// grantPanel: what the Elevate panel shows, or null when the binding has no
// write role (no panel at all). `st` is the /api status, null until loaded.
//  - windows: the offered lengths, never above the binding's max_minutes
//  - needLabel: a prod binding makes the operator type its label to confirm
//  - hostSessions/blocked: the host-tier warning list and any refusal reason
//
// A session with named connections passes `conns` (the list entry's
// aws_connections) and `pick` (the operator's choice) and gets the same fields
// for the SELECTED connection, plus
//  - conns: the write-capable connections [{name,label,prod,role}], selector:
//    true when there are two or more to choose from
//  - selected: the connection every other field is about
//  - elevatedConn: the connection whose window is open ("" none)
//  - blockedText: why a grant for `selected` would be refused right now because
//    of ANOTHER connection ("Drop <name> first", or the server's leftover
//    sentence from the status), shown verbatim
// A legacy binding returns exactly the first list, so its panel is unchanged.
function grantPanel(aws, st, conns, pick, nowMs = Date.now()) {
  if (Array.isArray(conns) && conns.length) return connGrantPanel(conns, st, pick, nowMs);
  const e = aws && aws.elevate;
  if (!e) return null;
  const max = Number(e.max_minutes) || 60;
  const windows = ELEV_WINDOWS.filter((w) => w <= max);
  const se = (st && st.elevate) || {};
  return {
    role: elevRoleName(e.role_arn),
    windows: windows,
    needLabel: !!aws.prod,
    label: aws.label || "",
    hostSessions: Array.isArray(se.host_sessions) ? se.host_sessions : [],
    blocked: se.blocked || "",
    allowIAM: !!e.allow_iam,
  };
}

function connGrantPanel(conns, st, pick, nowMs) {
  const writable = conns.filter((c) => c && c.write_capable && c.elevate);
  if (!writable.length) return null;
  const se = (st && st.elevate) || {};
  // Which connection holds the window: the status knows first-hand; the list
  // entry covers the moment before the status has loaded.
  let elevatedConn = "";
  if (st && st.elevation && st.elevation.active && st.elevation.name) elevatedConn = st.elevation.name;
  else if (!st || !st.elevation) {
    const live = conns.find((c) => c && c.elevated_until && Date.parse(c.elevated_until) > nowMs);
    if (live) elevatedConn = live.name;
  }
  let sel = writable.find((c) => c.name === pick);
  if (!sel) sel = writable.find((c) => c.name === elevatedConn) || writable[0];
  const max = Number(sel.elevate.max_minutes) || 60;
  let blockedText = "";
  if (elevatedConn && elevatedConn !== sel.name) blockedText = "Drop " + elevatedConn + " first";
  else if (se.conn_blocked && typeof se.conn_blocked[sel.name] === "string") blockedText = se.conn_blocked[sel.name];
  return {
    role: elevRoleName(sel.elevate.role_arn),
    windows: ELEV_WINDOWS.filter((w) => w <= max),
    needLabel: !!sel.prod,
    label: sel.label || "",
    hostSessions: Array.isArray(se.host_sessions) ? se.host_sessions : [],
    blocked: se.blocked || "",
    allowIAM: !!sel.elevate.allow_iam,
    conns: writable.map((c) => ({ name: c.name, label: c.label || "", prod: !!c.prod, role: elevRoleName(c.elevate.role_arn) })),
    selector: writable.length > 1,
    selected: sel.name,
    elevatedConn: elevatedConn,
    blockedText: blockedText,
  };
}

// grantBody: the POST body of an elevate request. A reason is sent only when
// given; the typed label only when the binding is prod, so a label typed into a
// field that was never required is not sent. `name` (the AWS connection) is
// sent only for a session with connections, so the legacy body is unchanged.
function grantBody(minutes, reason, label, needLabel, name) {
  const body = { minutes: minutes };
  const r = String(reason || "").trim();
  if (r) body.reason = r;
  if (needLabel) body.confirm_label = String(label || "");
  if (name) body.name = String(name);
  return body;
}

// grantErrorText: the toast for a refused elevate/extend. The server's own
// sentence is shown for 400/409 (it names what to change).
function grantErrorText(status, msg) {
  if (status === 0) return "Elevate failed: no connection.";
  if (status === 403) return "Only a signed-in browser session can change write access.";
  if ((status === 409 || status === 400) && msg) return msg;
  if (status === 404 || status === 410) return "That session is gone.";
  return "Elevate failed (" + status + ").";
}

// Textually identical to the server's allowlist in internal/ssologin/ssologin.go
// (regionRe, portalRe). Change them together.
const SSO_REGION_RE = /^device\.sso\.[a-z]{2}(-[a-z]+)+-\d{1,2}\.amazonaws\.com$/;
const SSO_PORTAL_RE = /^[a-z0-9-]+\.awsapps\.com$/;

// trustedSSOURL: may this device-sign-in URL be rendered as a link? https, no
// userinfo, no port, and one of the two AWS hosts. Anything else is shown as
// plain text, never as an anchor.
function trustedSSOURL(uri) {
  if (typeof uri !== "string" || !uri) return false;
  let u;
  try { u = new URL(uri); } catch (_) { return false; }
  if (u.protocol !== "https:" || u.username || u.password || u.port) return false;
  return SSO_REGION_RE.test(u.hostname) || SSO_PORTAL_RE.test(u.hostname);
}

// ssoCard: the model of the "AWS sign-in expired" needs-you card. The link is
// rendered only when the server vouched for it (uri_trusted) AND this page's
// own check agrees. `reply` keeps the session's reply box (spec 8.2): the SSO
// card outranks "your turn" for which card shows, not for what you can do.
// ssoLoginOf: the sign-in flow to show. The legacy status carries one at the top
// level; a session with connections carries one per source, and one card shows
// one flow at a time, so a waiting one wins, then any.
function ssoLoginOf(sso) {
  if (!sso) return null;
  if (sso.login) return sso.login;
  const srcs = Array.isArray(sso.sources) ? sso.sources : [];
  const waiting = srcs.find((x) => x && x.login && x.login.state === "waiting");
  const any = waiting || srcs.find((x) => x && x.login);
  return any ? any.login : null;
}

// ssoCommandOf: the host command to offer. The top-level one for the legacy
// binding; for connections, that of the first source still expired.
function ssoCommandOf(sso) {
  if (!sso) return "";
  if (sso.command) return sso.command;
  const srcs = Array.isArray(sso.sources) ? sso.sources : [];
  const x = srcs.find((y) => y && y.expired && y.command);
  return x ? x.command : "";
}

// ssoSignInName: what POST .../aws/sso-login names. "" for the legacy binding
// (the request then carries no body, as before); for connections the first
// expired one by name, so each press moves on to the next source.
function ssoSignInName(s) {
  const cs = s && Array.isArray(s.aws_connections) ? s.aws_connections : [];
  if (!cs.length) return "";
  const names = cs.map((c) => c.name).sort();
  const bad = cs.filter((c) => c.sso_expired).map((c) => c.name).sort();
  return bad.length ? bad[0] : names[0];
}

function ssoCard(s, st) {
  const sso = (st && st.sso) || {};
  const l = ssoLoginOf(sso);
  let login = null;
  if (l) {
    const uri = typeof l.uri === "string" ? l.uri : "";
    const ok = !!l.uri_trusted && trustedSSOURL(uri);
    login = { state: l.state || "", link: ok ? uri : "", plain: ok ? "" : uri, code: l.code || "", expires: l.expires || "" };
  }
  return {
    text: elevSSOText(s),
    command: ssoCommandOf(sso),
    login: login,
    reply: !!(s && s.replyable),
  };
}

// dropToast: after a Drop, when do the credentials already issued stop working.
function dropToast(view) {
  const t = view && view.last_valid ? elevHHMM(view.last_valid) : "";
  return t
    ? "Write access dropped. Credentials already issued stay valid until " + t + "."
    : "Write access dropped. No write credentials were issued.";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { grantPanel, grantBody, grantErrorText, trustedSSOURL, ssoCard, dropToast, ssoLoginOf, ssoCommandOf, ssoSignInName };
}
