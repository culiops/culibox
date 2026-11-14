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
function elevLiveWindow(conns, st, nowMs) {
  return typeof liveWindowConn === "function" ? liveWindowConn(conns, st, nowMs) : require("./sessionrow.js").liveWindowConn(conns, st, nowMs);
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
  if (!aws || !aws.elevate) return null;
  return grantFields(aws.elevate, aws.prod, aws.label, st);
}

// grantFields: the panel fields common to both shapes, from one elevate block.
function grantFields(e, prod, label, st) {
  const max = Number(e.max_minutes) || 60;
  const se = (st && st.elevate) || {};
  return {
    role: elevRoleName(e.role_arn),
    windows: ELEV_WINDOWS.filter((w) => w <= max),
    needLabel: !!prod,
    label: label || "",
    hostSessions: Array.isArray(se.host_sessions) ? se.host_sessions : [],
    blocked: se.blocked || "",
    allowIAM: !!e.allow_iam,
    boxPrefix: e.box_bucket_prefix || "",
    passRolePrefix: e.pass_role_prefix || "",
  };
}

// connPickerKind: how Write access offers the connections. One is shown as its
// name; two fit side by side as a segmented control; three or more would not fit
// the rail (a long name widens it), so they are a select.
function connPickerKind(n) {
  return n > 2 ? "select" : n === 2 ? "seg" : "text";
}

function connGrantPanel(conns, st, pick, nowMs) {
  const writable = conns.filter((c) => c && c.write_capable && c.elevate);
  if (!writable.length) return null;
  const se = (st && st.elevate) || {};
  const elevatedConn = (elevLiveWindow(conns, st, nowMs) || {}).name || "";
  let sel = writable.find((c) => c.name === pick);
  if (!sel) sel = writable.find((c) => c.name === elevatedConn) || writable[0];
  let blockedText = "";
  if (elevatedConn && elevatedConn !== sel.name) blockedText = "Drop " + elevatedConn + " first";
  else if (!elevatedConn && typeof (se.conn_blocked || {})[sel.name] === "string" && leftoverLive(st, sel.name, nowMs)) {
    blockedText = se.conn_blocked[sel.name];
  }
  return Object.assign(grantFields(sel.elevate, sel.prod, sel.label, st), {
    conns: writable.map((c) => ({ name: c.name, label: c.label || "", prod: !!c.prod, role: elevRoleName(c.elevate.role_arn) })),
    selector: writable.length > 1,
    picker: connPickerKind(writable.length),
    selected: sel.name,
    elevatedConn: elevatedConn,
    blockedText: blockedText,
  });
}

// leftoverLive: write credentials issued before a drop, for a connection other
// than `name`, are still valid. The server's conn_blocked sentence is as old as
// the cached status and has no clock of its own, so it is kept only while the
// status's last_valid is in the future.
function leftoverLive(st, name, nowMs) {
  const e = st && st.elevation;
  return !!e && e.active === false && !!e.name && e.name !== name && Date.parse(e.last_valid) > nowMs;
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

// dropErrorText: the toast for a failed Drop.
function dropErrorText(status) {
  if (status === 0) return "Drop failed: no connection.";
  if (status === 404 || status === 410) return "That session is gone.";
  return "Drop failed (" + status + ").";
}

// elevCall: one request to .../aws/elevate. Resolves {status, body}; a network
// failure is status 0 and an unreadable body is null, never a throw.
async function elevCall(sid, init) {
  let status = 0;
  let body = null;
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(sid) + "/aws/elevate", init);
    status = res.status;
    try { body = await res.json(); } catch (_) { body = null; }
  } catch (_) { status = 0; }
  return { status, body };
}

// elevError: the rejection of elevGrant / elevDrop. message is the text to show
// the operator (the server's own sentence where it names what to change);
// status is the HTTP status, 0 for no connection.
function elevError(status, message) {
  return Object.assign(new Error(message), { status });
}

// elevGrant: POST a grant (an extension is the same endpoint). Resolves the
// elevation view; rejects with an elevError. `confirm_label` is sent only when
// given (the caller passes it for a prod binding alone), `name` only for a
// session with connections, so a legacy body is unchanged. The browser adds
// Origin itself; the server accepts only a signed-in browser.
async function elevGrant(sid, o) {
  const g = o || {};
  const { status, body } = await elevCall(sid, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(grantBody(g.minutes, g.reason, g.confirm_label, g.confirm_label !== undefined, g.name)),
  });
  if (status === 200) return (body && body.elevation) || null;
  throw elevError(status, grantErrorText(status, body && body.error));
}

// elevDrop: lowering power needs no panel and no label. A drop names no
// connection (the server never reads a body), so `name` is not sent; it is
// accepted so a caller can say which row it pressed.
async function elevDrop(sid) {
  const { status, body } = await elevCall(sid, { method: "DELETE" });
  if (status === 200) return (body && body.elevation) || null;
  throw elevError(status, dropErrorText(status));
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

// ssoLoginOf: the sign-in flow to show. The legacy status carries one at the top
// level. A session with connections carries one per source; with `name` (the
// connection the Sign in button targets) only that connection's source counts,
// so a flow of another source is never shown against it. Without a name, a
// waiting flow wins, then any.
function ssoLoginOf(sso, name) {
  if (!sso) return null;
  if (sso.login) return sso.login;
  const srcs = Array.isArray(sso.sources) ? sso.sources : [];
  if (name) {
    const src = srcs.find((x) => x && Array.isArray(x.names) && x.names.includes(name));
    return (src && src.login) || null;
  }
  const waiting = srcs.find((x) => x && x.login && x.login.state === "waiting");
  const any = waiting || srcs.find((x) => x && x.login);
  return any ? any.login : null;
}

// ssoCommandOf: the host command to offer. The top-level one for the legacy
// binding. For connections, that of the source the target connection `name`
// signs in through (the one the Sign in button will start), else the first
// source still expired.
function ssoCommandOf(sso, name) {
  if (!sso) return "";
  if (sso.command) return sso.command;
  const srcs = Array.isArray(sso.sources) ? sso.sources : [];
  if (name) {
    const src = srcs.find((x) => x && Array.isArray(x.names) && x.names.includes(name));
    if (src) return src.command || "";
  }
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

// ssoCard: the model of the "AWS sign-in expired" needs-you card. The link is
// rendered only when the server vouched for it (uri_trusted) AND this page's
// own check agrees. `reply` keeps the session's reply box (spec 8.2): the SSO
// card outranks "your turn" for which card shows, not for what you can do.
function ssoCard(s, st) {
  const sso = (st && st.sso) || {};
  const target = ssoSignInName(s);
  const l = ssoLoginOf(sso, target);
  let login = null;
  if (l) {
    const uri = typeof l.uri === "string" ? l.uri : "";
    const ok = !!l.uri_trusted && trustedSSOURL(uri);
    login = { state: l.state || "", link: ok ? uri : "", plain: ok ? "" : uri, code: l.code || "", expires: l.expires || "" };
  }
  return {
    text: elevSSOText(s),
    command: ssoCommandOf(sso, target),
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
  module.exports = { connPickerKind, grantPanel, grantBody, grantErrorText, dropErrorText, elevGrant, elevDrop, trustedSSOURL, ssoCard, dropToast, ssoLoginOf, ssoCommandOf, ssoSignInName };
}
