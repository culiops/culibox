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
function grantPanel(aws, st) {
  const e = aws && aws.elevate;
  if (!e) return null;
  const max = Number(e.max_minutes) || 60;
  let windows = ELEV_WINDOWS.filter((w) => w <= max);
  if (!windows.length) windows = [max];
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

// grantBody: the POST body of an elevate request. A reason is sent only when
// given; the typed label only when the binding is prod, so a label typed into a
// field that was never required is not sent.
function grantBody(minutes, reason, label, needLabel) {
  const body = { minutes: minutes };
  const r = String(reason || "").trim();
  if (r) body.reason = r;
  if (needLabel) body.confirm_label = String(label || "");
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
function ssoCard(s, st) {
  const sso = (st && st.sso) || {};
  const l = sso.login;
  let login = null;
  if (l) {
    const uri = typeof l.uri === "string" ? l.uri : "";
    const ok = !!l.uri_trusted && trustedSSOURL(uri);
    login = { state: l.state || "", link: ok ? uri : "", plain: ok ? "" : uri, code: l.code || "", expires: l.expires || "" };
  }
  return {
    text: elevSSOText(s),
    command: sso.command || "",
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
  module.exports = { grantPanel, grantBody, grantErrorText, trustedSSOURL, ssoCard, dropToast };
}
