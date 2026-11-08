"use strict";

// The Audit page's pure model (spec §5.8, "Prove"): the query the page sends,
// how one audit row reads, the KPI tiles and the footer texts. Descriptive only:
// every word shown is one the server returned. DOM wiring lives in auditpage.js.
//
// Classic script: accTone is a browser global from accessmodel.js; under node it
// is required.

const AUD_RANGES = { "24h": 86400, "7d": 604800, "30d": 2592000 };
const AUD_KINDS = {
  credential: "Credential", elevation: "Elevation", signin: "Sign-in",
  egress: "Egress", settings: "Settings", store: "Store",
};
const AUD_ELEV_BAD = new Set(["granted", "extended", "refused"]);
const AUD_OK = new Set(["allowed", "ok"]);
const AUD_BAD = new Set(["denied", "private_addr", "bad_auth", "bad_method", "dial_error", "failed"]);
// A write elevation is a live grant: one that opened or was extended. Only its row is tinted and its Kind red; a refused,
// dropped, expired or ended elevation opened nothing (UI plan 5, digest-design §B9).
const AUD_ELEV_LIVE = new Set(["granted", "extended"]);

function audTone(outcome) {
  return (typeof accTone === "function" ? accTone : require("./accessmodel.js").accTone)(outcome);
}

// audFilters: the filters shared by the list and the export (no cursor).
function audFilters(state, nowS) {
  const s = state || {};
  const secs = AUD_RANGES[s.range] || AUD_RANGES["7d"];
  const p = [];
  if (s.filter) p.push("kind=" + encodeURIComponent(s.filter));
  if (s.session) p.push("session=" + encodeURIComponent(s.session));
  p.push("from=" + (nowS - secs), "to=" + nowS);
  return p;
}

// auditQuery: the /api/audit query string for the page state.
function auditQuery(state, nowS) {
  const p = audFilters(state, nowS);
  if (state && state.before) p.push("before=" + encodeURIComponent(state.before));
  return p.join("&");
}

// auditCsvHref: the export of the same filters. A page size and a cursor mean
// nothing to an export, so neither is sent.
function auditCsvHref(state, nowS) {
  return "/api/audit.csv?" + audFilters(state, nowS).join("&");
}

function audTime(ts) {
  if (!Number.isFinite(ts)) return "";
  const d = new Date(ts * 1000);
  const p = (n) => String(n).padStart(2, "0");
  return p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " + p(d.getHours()) + ":" + p(d.getMinutes());
}

// auditRowView: one table row. `names` maps a session id to its live name.
// tone is the colour role of the Outcome: an elevation that opened, extended or
// was refused is red; egress and settings words follow the Access view's table
// plus the proxy's failure words; anything else is neutral. elevated is the red
// row itself: an elevation that opened or was extended, and nothing else.
function auditRowView(ev, names) {
  const e = ev || {};
  const o = String(e.outcome || "");
  const isElevation = e.kind === "elevation";
  const elevated = isElevation && AUD_ELEV_LIVE.has(o);
  let tone;
  if (isElevation && AUD_ELEV_BAD.has(o)) tone = "bad";
  else if (AUD_OK.has(o)) tone = "ok";
  else if (AUD_BAD.has(o)) tone = "bad";
  else tone = audTone(o);
  let outcome = o.replace(/_/g, " ");
  if (e.actor) outcome += " · " + e.actor;
  if (Number.isFinite(e.count) && e.count > 1) outcome += " ×" + e.count;
  const id = e.session ? String(e.session) : "";
  const known = id && names && Object.prototype.hasOwnProperty.call(names, id) ? names[id] : "";
  return {
    time: audTime(e.ts),
    session: known || id || "—",
    kind: AUD_KINDS[e.kind] || String(e.kind || ""),
    what: String(e.what || ""),
    reason: e.reason ? String(e.reason) : "",
    outcome, tone, elevated,
  };
}

const AUD_KPI_LABELS = [
  ["issued", "Credentials issued"], ["refused", "Refused"], ["elevations", "Write elevations"],
  ["egress_allowed", "Egress allowed"], ["egress_denied", "Egress denied"],
];

// auditKpis: the five tiles. No summary yet (or a failed read) is a dash, never
// a zero that would pass for a measurement. bad marks the one red value: write
// elevations above zero. A refusal or a denial is containment working, so it is
// never bad, whatever its count (digest-design §B5).
function auditKpis(summary) {
  return AUD_KPI_LABELS.map(([k, label]) => {
    const v = summary ? summary[k] : undefined;
    const known = Number.isFinite(v);
    const tile = { n: known ? v.toLocaleString("en-US") : "—", label };
    if (k === "elevations" && known && v > 0) tile.bad = true;
    return tile;
  });
}

function auditFootText(shown, total) {
  return "Showing " + Number(shown).toLocaleString("en-US") + " of " + Number(total).toLocaleString("en-US") + " · newest first";
}

function auditPolicyText(days) {
  return days > 0 ? "Egress kept " + days + " days · everything else kept forever" : "Everything kept forever";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { auditQuery, auditCsvHref, auditRowView, auditKpis, auditFootText, auditPolicyText };
}
