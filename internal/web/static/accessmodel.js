"use strict";

// The Access view's pure model (spec 5.7): what one session may touch (the
// containment card), which credentials it holds and can be given (the cloud
// credentials card), and the last credential events. Everything the view draws
// is a field some endpoint really returned; a field the server did not send is
// absent here, never a guess. DOM wiring lives in accesstab.js.
//
// Inputs, all optional so a half-loaded view still renders:
//   session  the list entry (Info): runtime, cwd, created, account, aws /
//            aws_connections, github, tokens, bundle_drops, clone, context_paths,
//            clone_of
//   aws      GET .../aws (null on 204: no binding), with `approvals` for a
//            browser session; ABSENT approvals mean unavailable, not none
//   egress   GET .../egress (an unbound box); the /aws reply carries its own
//   meta     /api/meta (box_runtime)
//   usage    the tab's usage (model, effort)
//
// Classic script: the helpers it leans on are browser globals from the files
// loaded before it; under node they are required.

// ACCESS_EVENTS: how many credential events the card shows.
const ACCESS_EVENTS = 20;

// accDep: a function defined by another page script (a global in the browser),
// or its export when run under node.
function accDep(name, file) {
  return typeof globalThis[name] === "function" ? globalThis[name] : require(file)[name];
}
function accNewCredMin() {
  return typeof NEW_CRED_MIN === "number" ? NEW_CRED_MIN : require("./sessionrow.js").NEW_CRED_MIN;
}

const accValidTime = (iso) => {
  const t = Date.parse(iso);
  return Number.isFinite(t) && t > Date.UTC(2000, 0, 1) ? t : null; // the zero time is "never"
};

// accConns: the session's AWS bindings as one list. A session with named
// connections lists them as they are (conn = the name a request must carry); a
// legacy inline binding becomes one entry named after its label and carries no
// name in a request (conn "").
function accConns(s) {
  if (Array.isArray(s.aws_connections) && s.aws_connections.length) {
    return s.aws_connections.filter(Boolean).map((c) => Object.assign({}, c, { conn: c.name }));
  }
  if (s.aws) return [Object.assign({}, s.aws, { name: s.aws.label || "aws", conn: "" })];
  return [];
}

// accTone: the colour role of an outcome word. Green only for a success, red
// only for a real error; a denial or refusal is containment working, so it is
// neutral like anything else.
const ACC_OK = new Set(["minted", "probe_minted", "fetched"]);
function accTone(outcome) {
  const o = String(outcome || "");
  if (ACC_OK.has(o)) return "ok";
  if (o === "revoked" || o === "") return "";
  if (/_error$|_failed$|^sso_expired$|^rate_limited$|^sts_error$|^store_locked$|^unresolved$|^not_installed$/.test(o)) return "bad";
  return "";
}

function accOutcomeText(o) {
  return String(o || "").replace(/_/g, " ");
}

// accEventRows: the status's audit rows (AWS and GitHub together, told apart by
// provider), the static-token rows and the approval rows, newest first.
function accEventRows(aws) {
  const roleName = accDep("roleName", "./sessionrow.js");
  const out = [];
  const push = (e) => { if (Number.isFinite(e.ts)) out.push(e); };
  for (const r of aws && Array.isArray(aws.recent) ? aws.recent : []) {
    if (!r) continue;
    const github = r.provider === "github";
    push({ ts: Date.parse(r.time), provider: github ? "github" : "aws",
      subject: String(github ? r.subject || "" : r.entry || roleName(r.role_arn) || ""),
      outcome: accOutcomeText(r.outcome), tone: accTone(r.outcome), elevated: !!r.elevated, detail: "",
      tint: !!r.elevated, elevatedText: r.elevated ? "yes" : "no" });
  }
  for (const r of aws && Array.isArray(aws.static_recent) ? aws.static_recent : []) {
    if (!r) continue;
    push({ ts: Date.parse(r.time), provider: "token", subject: String(r.subject || r.entry || ""),
      outcome: accOutcomeText(r.outcome), tone: accTone(r.outcome), elevated: false, detail: "",
      tint: false, elevatedText: "no" });
  }
  for (const a of aws && Array.isArray(aws.approvals) ? aws.approvals : []) {
    if (!a) continue;
    const act = String(a.action || "");
    const label = act === "sso_login_ok" ? "sign-in ok" : act === "sso_login_failed" ? "sign-in failed" : act;
    const mins = (act === "granted" || act === "extended") && Number(a.minutes) > 0 ? " " + Number(a.minutes) + " min" : "";
    const human = act === "granted" || act === "extended" || act === "dropped";
    // Only a granted or extended window is write access (red); the Elevated column
    // is for credentials, so an approval row leaves it blank.
    push({ ts: Number(a.ts), provider: "approval", subject: label + mins + (a.name ? " on " + a.name : ""),
      outcome: human ? "you (browser)" : "", tone: "", elevated: false, detail: String(a.reason || ""),
      tint: act === "granted" || act === "extended", elevatedText: "" });
  }
  out.sort((a, b) => b.ts - a.ts);
  return out.slice(0, ACCESS_EVENTS);
}

// accSSO: what is KNOWN about a connection's SSO sign-in, never a guess. "expired"
// when the daemon flagged it; "valid" only when a non-expired source in the
// status names it (the connections that sign in through each source); "" when
// nothing says it uses SSO (static keys, or the status not loaded yet).
function accSSO(c, aws) {
  if (c.sso_expired) return "expired";
  const srcs = aws && aws.sso && Array.isArray(aws.sso.sources) ? aws.sso.sources : [];
  return srcs.some((src) => src && src.expired === false && Array.isArray(src.names) && src.names.includes(c.name)) ? "valid" : "";
}

// accNewest: the entry of `list` that satisfies `keep` with the greatest ts (the
// first of equals, which is the newer in the broker's newest-first order), or
// null. A row without a usable ts still counts, as the oldest.
function accNewest(list, keep) {
  let best = null;
  let bestTs = -Infinity;
  for (const a of Array.isArray(list) ? list : []) {
    if (!a || !keep(a)) continue;
    const ts = Number.isFinite(Number(a.ts)) ? Number(a.ts) : -Infinity;
    if (best === null || ts > bestTs) { best = a; bestTs = ts; }
  }
  return best;
}

// elevBanner: the session's one open write window as the red banner every pane
// draws, or null (no window, or `until` is not in the future: a lapsed window
// never reads "0 min left"). The window comes from the list entry (fresh each
// poll), falling back to the cached /aws status between a grant and the next
// poll. The reason comes only from a loaded approval of that same connection:
// before the status loads, after a failed read, or with a blank reason there is
// no reason segment at all. `detail` is the span text the banner draws after the
// connection name; `key` changes exactly when something drawn does. `nowMs` is the real
// clock when a caller leaves it out, so a new caller cannot get a banner that never shows.
function elevBanner(s, aws, nowMs = Date.now()) {
  const hhmm = accDep("hhmm", "./sessionrow.js");
  const minutesLeft = accDep("minutesLeft", "./sessionrow.js");
  const conns = accConns(s || {});
  const w = accDep("liveWindowConn", "./sessionrow.js")(conns, aws, nowMs);
  if (!w) return null;
  const mins = minutesLeft(w.until, nowMs);
  const wc = conns.find((c) => c.name === w.name);
  const want = wc ? wc.conn : w.name;
  const ap = accNewest(aws && aws.approvals, (a) => (a.action === "granted" || a.action === "extended") && (a.name || "") === want);
  const reason = ap ? String(ap.reason || "").trim() : "";
  const untilText = hhmm(w.until);
  const leftText = mins < 1 ? "<1 min left" : mins + " min left";
  const newCreds = mins < accNewCredMin();
  const detail = "until " + untilText + " · " + leftText + (reason ? " · “" + reason + "”" : "") +
    (newCreds ? " · new credentials from the base role" : "");
  return { conn: w.name, untilText, minsLeft: mins, leftText, reason, newCreds, detail,
    key: JSON.stringify({ conn: w.name, untilText, leftText, reason, newCreds }) };
}

// The approval actions that open, extend or close a write window. The newest of
// them on a connection says whether its window is over (ACC_WINDOW_ENDS).
const ACC_WINDOW_ENDS = new Set(["expired", "ended", "dropped"]);
const ACC_WINDOW_ACTIONS = new Set(["granted", "extended", ...ACC_WINDOW_ENDS]);

// accWindowNote: the two lines a connection's row keeps once a window is over, or
// null when there is nothing to say (and always null while a window is open on it).
//   ended     "Write access ended HH:MM" (a date first when not today): no window is
//             open on this connection and its newest window approval is an
//             expired / ended / dropped row (a later grant or extend hides it).
//   leftover  "Credentials already issued stay valid until HH:MM": the status's
//             elevation view is inactive, names this connection (a nameless one is
//             the legacy inline binding's, conn "") and its last_valid is ahead.
// Both come from the loaded /aws status, so before it loads (or when it carries no
// approvals) they are empty, never a guess.
function accWindowNote(c, aws, nowMs) {
  const hhmm = accDep("hhmm", "./sessionrow.js");
  // A window open on this connection is the whole story (the banner and the row's "write (elevated)"): the
  // cached status can still say the dropped window's credentials are valid after a quick re-Elevate, and a
  // red leftover line beside a live banner would contradict it.
  if (accDep("isElevated", "./sessionrow.js")(c, nowMs)) return null;
  let ended = "";
  const last = accNewest(aws && aws.approvals, (a) =>
    ACC_WINDOW_ACTIONS.has(a.action) && (a.name || "") === c.conn);
  if (last && ACC_WINDOW_ENDS.has(last.action) && Number.isFinite(Number(last.ts))) {
    ended = "Write access ended " + accEventTime(Number(last.ts), nowMs);
  }
  let leftover = "";
  const e = aws && aws.elevation;
  const valid = e ? accValidTime(e.last_valid) : null;
  if (e && e.active === false && (e.name || "") === c.conn && valid !== null && valid > nowMs) {
    leftover = "Credentials already issued stay valid until " + hhmm(e.last_valid);
  }
  return ended || leftover ? { ended, leftover } : null;
}

// accRow: one AWS connection's row.
function accRow(c, o) {
  const { aws, conns, nowMs, stats, hostWarning, legacy } = o;
  const roleName = accDep("roleName", "./sessionrow.js");
  const roleText = accDep("roleText", "./sessionrow.js");
  const hhmm = accDep("hhmm", "./sessionrow.js");
  const isElevated = accDep("isElevated", "./sessionrow.js");
  const elevActions = accDep("elevActions", "./sessionrow.js");
  const baseScope = accDep("baseScope", "./sessionrow.js");
  const grantPanel = accDep("grantPanel", "./elevation.js");
  const live = isElevated(c, nowMs);
  const acts = elevActions(c, nowMs);
  const list = [];
  if (acts.elevate) list.push("elevate");
  if (acts.extend) list.push("extend");
  // No row Drop: the pane's one Drop is the banner's.
  const gp = acts.elevate || acts.extend
    ? (legacy ? grantPanel(c.elevate ? c : null, aws) : grantPanel(null, aws, conns, c.name, nowMs))
    : null;
  const b = live ? null : baseScope(c);
  const t = stats ? accValidTime(stats.last_mint) : null;
  // An sso_direct binding without a role name shows the mode label instead of an
  // empty "role ".
  const roleArn = live && c.elevate ? c.elevate.role_arn : c.role_arn;
  const sub = [c.label && c.label !== c.name ? c.label : "", "account " + c.account_id,
    roleName(roleArn) ? "role " + roleName(roleArn) : roleText(c, roleArn), c.region].filter(Boolean).join(" · ");
  return {
    kind: "aws", name: c.name, conn: c.conn, prod: !!c.prod, sub,
    scope: live ? "write (elevated)" : "read",
    mode: c.mode === "sso_direct" ? "sso_direct" : "",
    sso: accSSO(c, aws),
    last: stats ? (t ? hhmm(stats.last_mint) : "never") : "",
    actions: list,
    window: accWindowNote(c, aws, nowMs),
    grant: gp ? {
      windows: gp.windows, needLabel: gp.needLabel, label: gp.label, role: gp.role, allowIAM: gp.allowIAM,
      blocked: gp.blocked, blockedText: gp.blockedText || "", extend: !!acts.extend && !acts.elevate,
    } : null,
    hostWarning: gp ? hostWarning : [],
    base: b ? b.text : "", baseWarn: !!b && b.state === "writes", baseTitle: b ? b.title : "",
    stat: stats && stats.last_error ? String(stats.last_error) : "", status: "",
  };
}

// accessModel: see the header. Never throws on a missing or odd input.
function accessModel(o) {
  const x = o || {};
  const s = x.session || {};
  const aws = x.aws || null;
  const now = typeof x.nowMs === "number" ? x.nowMs : Date.now();
  const u = x.usage || {};
  const meta = x.meta || {};
  const hhmm = accDep("hhmm", "./sessionrow.js");

  // Fail loud: only an explicit box tier reads as contained.
  const tier = s.runtime === "box" ? "box" : "host";
  const eg = (x.egress && x.egress.egress) || (aws && aws.egress) || (x.egress && x.egress.mode ? x.egress : null);
  const network = eg
    ? { mode: eg.mode === "open" ? "open" : "allowlist", allowed: Number(eg.allowed) || 0, denied: Number(eg.denied) || 0 }
    : { mode: "", allowed: 0, denied: 0 };
  const drops = accDep("dropsRow", "./steerrail.js")(s);
  const cloneParts = accDep("cloneParts", "./steerrail.js");
  const cp = s.clone ? cloneParts(s.clone, s.agent) : { warn: [], info: [] };
  const containment = {
    tier,
    workspace: s.cwd || "",
    network,
    account: s.account || "default",
    runtime: tier === "box" && meta.box_runtime !== undefined ? accDep("tierRuntimeLabel", "./tier.js")(meta.box_runtime) : "",
    started: accDep("startedText", "./steerrail.js")(s.created, now),
    model: u.model ? String(u.model) : "",
    effort: u.effort ? String(u.effort) : "",
    readable: Array.isArray(s.context_paths) ? s.context_paths.map(String) : [],
    clone: s.clone_of || s.clone ? { of: s.clone_of || "", warn: cp.warn, info: cp.info } : null,
    dropped: drops || null,
  };

  const conns = accConns(s);
  const legacy = !(Array.isArray(s.aws_connections) && s.aws_connections.length) && !!s.aws;
  const byName = new Map((aws && Array.isArray(aws.connections) ? aws.connections : []).map((c) => [c.name, c]));
  const host = aws && aws.elevate && Array.isArray(aws.elevate.host_sessions)
    ? aws.elevate.host_sessions.map((h) => String((h && (h.name || h.id)) || "")).filter(Boolean) : [];

  const rows = conns.slice()
    .sort((a, b) => (b.prod ? 1 : 0) - (a.prod ? 1 : 0) || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
    .map((c) => accRow(c, {
      aws, conns, nowMs: now, legacy, hostWarning: host,
      stats: legacy ? (aws && aws.stats) || null : (byName.get(c.name) || {}).stats || null,
    }));

  // GitHub and static tokens: the broker's rows for them, as the rail showed.
  const recent = aws && Array.isArray(aws.recent) ? aws.recent : [];
  const gh = s.github || (aws && aws.github);
  if (gh) {
    const last = recent.find((r) => r && r.provider === "github");
    const owners = Array.isArray(gh.owners) ? gh.owners : [];
    rows.push({ kind: "github", name: "github", conn: "", prod: false, sub: owners.length ? "owners: " + owners.join(", ") : "",
      scope: "", sso: "", last: last && accValidTime(last.time) ? hhmm(last.time) : "",
      status: last ? (last.outcome === "minted" ? "ok" : accOutcomeText(last.outcome)) : "",
      actions: [], window: null, grant: null, hostWarning: [], base: "", baseWarn: false, baseTitle: "", stat: "" });
  }
  const sr = aws && Array.isArray(aws.static_recent) ? aws.static_recent : [];
  for (const t of Array.isArray(s.tokens) ? s.tokens : []) {
    if (!t) continue;
    const last = sr.find((r) => r && (r.subject === t.name || r.entry === t.name));
    rows.push({ kind: "token", name: String(t.name || ""), conn: "", prod: !!t.prod, sub: String(t.label || t.provider || "static token"),
      scope: "", sso: "", last: last && accValidTime(last.time) ? hhmm(last.time) : "",
      status: last ? (last.outcome === "fetched" ? "ok" : accOutcomeText(last.outcome)) : "",
      actions: [], window: null, grant: null, hostWarning: [], base: "", baseWarn: false, baseTitle: "", stat: "" });
  }

  // The open write window, if any: the one banner model every pane draws.
  const banner = elevBanner(s, aws, now);

  // The SSO card hangs under the connection a Sign in press would target, while
  // a connection's sign-in is expired or a flow is running.
  let ssoFor = "";
  const target = accDep("ssoSignInName", "./elevation.js")(s);
  const expired = rows.filter((r) => r.sso === "expired");
  const loginOf = accDep("ssoLoginOf", "./elevation.js");
  const flow = aws && aws.sso ? loginOf(aws.sso, target || undefined) : null;
  if (legacy) {
    if (expired.length || (flow && flow.state === "waiting")) ssoFor = rows[0] ? rows[0].name : "";
  } else if (expired.length || (flow && flow.state === "waiting")) {
    ssoFor = rows.some((r) => r.name === target) ? target : (expired[0] ? expired[0].name : "");
  }

  const st = aws && aws.stats;
  const mints = st ? Number(st.mints) || 0 : null;
  const lastMint = st && accValidTime(st.last_mint) ? hhmm(st.last_mint) : "";
  return {
    containment,
    banner,
    creds: {
      head: mints === null ? "" : mints + (mints === 1 ? " mint" : " mints") + (lastMint ? " · last " + lastMint : ""),
      rows,
      ssoFor,
    },
    events: accEventRows(aws),
    approvalsKnown: !!aws && Array.isArray(aws.approvals),
  };
}

// accReadReply: one status reply as data. 200 is its JSON, 204 is null (no
// binding); anything else, and a body that is not valid JSON, is undefined
// ("could not read": the caller keeps what it had). Never throws.
async function accReadReply(res) {
  try {
    if (res.status === 204) return null;
    if (res.status !== 200) return undefined;
    return await res.json();
  } catch (_) { return undefined; }
}

// accLoadDone: the load state after one read finished. `r` is {aws, egress, ok};
// a failed read (ok false) keeps the previous data and marks the load failed,
// and the read is over either way (inflight false), so a bad reply can never
// block the next one.
function accLoadDone(prev, r, nowMs) {
  const p = prev || {};
  const ok = !!(r && r.ok);
  return {
    aws: ok ? r.aws : p.aws === undefined ? null : p.aws,
    egress: ok ? r.egress : p.egress === undefined ? null : p.egress,
    at: nowMs, inflight: false,
    loaded: ok || !!p.loaded,
    failed: !ok,
  };
}

// accEventTime: an event's time cell. The clock alone for today, the date and
// clock for an earlier day (the list reaches back past midnight).
const ACC_MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
function accEventTime(ts, nowMs) {
  if (!Number.isFinite(ts)) return "";
  const d = new Date(ts);
  const n = new Date(typeof nowMs === "number" ? nowMs : Date.now());
  const clock = String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
  const today = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
  return today ? clock : d.getDate() + " " + ACC_MONTHS[d.getMonth()] + " " + clock;
}

// grantFormState: is the grant form complete enough to send? `why` names the
// first thing missing, for the hint under the button. The server re-checks all
// of it; this only keeps a half-filled form from reaching it. A prod binding's
// label must match exactly: case-sensitive and untrimmed, like the server's.
function grantFormState(f) {
  const g = f || {};
  if (g.busy) return { valid: false, why: "busy" };
  if (!(Number(g.minutes) > 0)) return { valid: false, why: "minutes" };
  const reason = String(g.reason == null ? "" : g.reason).trim();
  if (!reason) return { valid: false, why: "reason" };
  if (reason.length > 200) return { valid: false, why: "reason-long" };
  if (g.prod && g.typed !== g.label) return { valid: false, why: "label" };
  return { valid: true, why: "" };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { accessModel, elevBanner, grantFormState, accEventTime, accReadReply, accLoadDone, accTone, ACCESS_EVENTS };
}
