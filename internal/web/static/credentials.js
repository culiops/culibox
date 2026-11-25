"use strict";

// The Settings "Credentials" tab: the encrypted-store strip, then one group per
// kind of credential (AWS, GitHub Apps, each token provider, stored values,
// legacy secrets), each entry with its status. The add/edit/delete flows hook in
// through credActionHandlers; the AWS one (host profiles -> find roles -> check ->
// write acknowledgement -> save) is at the bottom of this file.
//
// Every string from the server is data: it is rendered with textContent, never
// as markup. The pure decisions at the top are node-tested
// (jstest/credentials.test.js); the DOM wiring below them is exercised in the
// browser.

// Exact copy. Do not paraphrase: the D4 sentence is the warning an operator
// reads before an agent gets write access to an AWS account, and the N3 line is
// the honest statement of what a stored token is.
const CRED_D4 = "The agent will be able to change this AWS account: create, modify and delete resources.";
const CRED_N3 = "The agent can read this token. It is long-lived and is not revoked when the session ends.";
// What Save says when the encrypted store does not exist yet, and what a framed host's store card opens with.
const CRED_STORE_FIRST = "Create the encrypted store first. The value is stored in it.";

// The two ways to give a box GitHub, side by side under "Add GitHub".
const GH_ROUTE_CHOICES = [
  { id: "pat", title: "Personal access token \u2014 simple", line: "One token you create on github.com. The agent can read it; it is long-lived and not revoked when the session ends." },
  { id: "app", title: "GitHub App \u2014 safer", line: "CuliBox mints 1-hour tokens limited to each profile's repositories and permissions. A few more minutes to set up." },
];

const GH_PAT_SUGGESTION = 'Suggested: a fine-grained token with only the repositories you need \u00b7 Contents: Read and write \u00b7 Pull requests: Read and write \u00b7 Metadata: Read. Avoid Administration and "All repositories". Add Workflows only if the agent must change CI files.';

// ghPatValueWarning: a classic token (ghp_) reaches every repository the account
// can. Read from the pasted value in the browser only; it goes nowhere new.
function ghPatValueWarning(value) {
  return typeof value === "string" && value.startsWith("ghp_")
    ? "This looks like a classic token: it reaches every repository your account can. A fine-grained token limited to the repos you need is safer."
    : "";
}

const GH_APP_HELP = [
  "On github.com: Settings \u203a Developer settings \u203a GitHub Apps \u203a New GitHub App. Untick Webhook \u203a Active.",
  "Repository permissions: Metadata: Read (required). Contents, Pull requests, Issues: Read, or Read and write if agents should push and open PRs. Actions, Checks, Commit statuses: Read for CI results. Leave Administration and Workflows unset.",
  "Create it, then Install App on the account that owns the repos and choose Only select repositories.",
  "Generate a private key (.pem) and copy the Client ID into this form.",
  "Protect main with branch protection if agents must not push to it.",
];
const CRED_FILE_BAD = "credentials.json has content this CuliBox cannot read, so this screen will not save over it. Fix the file, or run the CuliBox that wrote it.";
// Said under a "writes" base role. Never "read-only" unqualified: a role that
// has no write access found is not a guarantee, and the word promises one.
const CRED_D4_REMEDY = "Use a role without write access as the base and put this one under Elevate.";

const CRED_STATUS = {
  ok: ["ready", "Ready"],
  missing: ["missing", "Missing"],
  stored_locked: ["locked", "Stored (store locked)"],
  stored_unavailable: ["locked", "Stored (Keychain unavailable)"],
  invalid: ["invalid", "Invalid"],
  unset: ["notset", "Not set in the daemon's environment"],
};

// statusText: the chip text for an entry's status. Anything unknown reads
// Invalid rather than looking healthy.
function statusText(status) {
  return (CRED_STATUS[status] || CRED_STATUS.invalid)[1];
}

// statusClass: the chip's colour class (colour is secondary, the text is always there).
function statusClass(status) {
  return (CRED_STATUS[status] || CRED_STATUS.invalid)[0];
}

// baseText: how a base-role verdict reads. `result` is the server's verdict
// ({state, reason, reason_text, ...}); a missing one is "Not checked".
function baseText(result) {
  const st = result && result.state;
  if (st === "writes") return { cls: "bad", text: CRED_D4 };
  if (st === "none_found") return { cls: "ok", text: "No broad write access found" };
  const reason = result && result.reason;
  if (!result || !st || reason === "not_checked") return { cls: "warn", text: "Not checked" };
  return { cls: "warn", text: result.reason_text ? "Not verified: " + result.reason_text : "Not verified" };
}

// credWriteActions: the write actions found, for a "writes" verdict only.
function credWriteActions(result) {
  return result && result.state === "writes" && Array.isArray(result.actions) ? result.actions.filter(Boolean) : [];
}

function usedByText(names) {
  const list = Array.isArray(names) ? names.filter(Boolean) : [];
  return list.length ? list.join(", ") : "nothing";
}

// isSeed: an entry defined in config.json. It can be seen and used, but only
// the file edits it.
function isSeed(entry) {
  return !!entry && entry.source === "config";
}

// entryActions: what a card offers. Seeds and legacy secrets offer nothing.
function entryActions(entry) {
  if (!entry || isSeed(entry) || entry.kind === "legacy" || entry.kind === "value") return [];
  return entry.source === "web" ? ["edit", "delete"] : [];
}

const CRED_PROVIDER_TITLES = { github: "GitHub", pagerduty: "PagerDuty", aws: "AWS", gitlab: "GitLab" };

function providerTitle(provider) {
  const p = String(provider || "");
  if (!p) return "Other tokens";
  return CRED_PROVIDER_TITLES[p.toLowerCase()] || p.charAt(0).toUpperCase() + p.slice(1);
}

// credGroups: the screen's groups in order: aws, github, one per token
// provider (sorted by the title shown), values, legacy. AWS, GitHub Apps and
// Stored values always appear so an empty one can say how to fill it; token
// providers and legacy secrets only when there is something in them.
function credGroups(view) {
  const v = view || {};
  const out = [];
  out.push({ key: "aws", title: "AWS", entries: (v.aws || []).map((e) => ({ ...e, kind: "aws" })) });
  out.push({ key: "github", title: "GitHub Apps", entries: (v.github_apps || []).map((e) => ({ ...e, kind: "github" })) });
  const byProvider = new Map();
  for (const t of v.tokens || []) {
    const key = "token:" + (t.provider || "");
    if (!byProvider.has(key)) byProvider.set(key, { key, title: providerTitle(t.provider), entries: [] });
    byProvider.get(key).entries.push({ ...t, kind: "token" });
  }
  out.push(...[...byProvider.values()].sort((a, b) => a.title.localeCompare(b.title) || a.key.localeCompare(b.key)));
  out.push({ key: "values", title: "Stored values", entries: (v.values || []).map((e) => ({ ...e, kind: "value" })) });
  if ((v.legacy_secrets || []).length) {
    out.push({ key: "legacy", title: "Legacy secrets", entries: v.legacy_secrets.map((e) => ({ ...e, kind: "legacy" })) });
  }
  return out;
}

// credIconMaps: iconmap.js's lookup and maps, a page global in the browser and required under node (as accessmodel.js does).
const credIconMaps = () => (typeof iconOf === "function" ? { iconOf, ICON_RESOURCE, ICON_TOKEN_TEMPLATE } : require("./iconmap.js"));

// credGroupIcon: the glyph that heads a group (#168 G1 13-17), from declared fields only. AWS and GitHub Apps by the group key; a token
// section draws a provider mark only when EVERY entry declares a catalog template (token.template) that maps to one same mark, else
// the generic token glyph. The provider is free text and never picks a mark, and an older server's entry has no template. Stored
// values and Legacy secrets are left out (G1 p, q) and an unknown key has none: "" means no glyph.
function credGroupIcon(group) {
  const m = credIconMaps();
  const key = group && typeof group.key === "string" ? group.key : "";
  if (key === "aws" || key === "github") return m.iconOf(m.ICON_RESOURCE, key);
  if (!key.startsWith("token:")) return "";
  const generic = m.iconOf(m.ICON_RESOURCE, "token");
  const entries = Array.isArray(group.entries) ? group.entries : [];
  const marks = new Set(entries.map((e) => (e && typeof e.template === "string" ? m.iconOf(m.ICON_TOKEN_TEMPLATE, e.template) : "")));
  return marks.size === 1 && !marks.has("") ? [...marks][0] : generic;
}

// credTitleNode: a heading element whose text is `text`, with `glyph` (when there is one) drawn first at the heading size (16).
function credTitleNode(tag, cls, text, glyph) {
  if (!glyph) return credEl(tag, cls, text);
  const el = credEl(tag, cls);
  el.appendChild(uiIcon(glyph, { size: 16 }));
  el.appendChild(document.createTextNode(text));
  return el;
}

const pad2 = (n) => String(n).padStart(2, "0");

// checkedText: "checked 14:02 today" or "checked 2026-10-18", "" when there is no time.
function checkedText(iso, now) {
  if (!iso) return "";
  const t = new Date(iso);
  if (isNaN(t.getTime())) return "";
  const n = now || new Date();
  const day = (d) => d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate());
  if (day(t) === day(n)) return "checked " + pad2(t.getHours()) + ":" + pad2(t.getMinutes()) + " today";
  return "checked " + day(t);
}

// credClock: an instant as the clock time when it is today, else "YYYY-MM-DD HH:MM" (the browser's local time).
function credClock(iso, now) {
  const t = new Date(iso);
  if (isNaN(t.getTime())) return "";
  const n = now || new Date();
  const day = (d) => d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate());
  const hm = pad2(t.getHours()) + ":" + pad2(t.getMinutes());
  return day(t) === day(n) ? hm : day(t) + " " + hm;
}

// credSignIn: what an AWS connection's sign-in cell says (#141). The config's status ("ok") only says the entry parses;
// the server's `signin` says what the host's SSO token cache holds. Green ("ready") is for a signed-in session only: a
// missing, new or garbled state reads Unknown, never ready. Expired and a profile that is not there are amber (they need
// you), never-signed-in, renewable and unknown are neutral. An entry whose config is not ok keeps that status.
// Where to sign in: the existing flow, Edit then Check (an expired or absent sign-in shows its Sign in button there).
const CRED_SIGNIN_EDIT = "Edit, then Check to sign in";
// A config.json entry has no Edit: the sign-in is the host command (the one the Check flow offers when the UI cannot start it).
function credSignInWhere(e) {
  if (entryActions(e).includes("edit")) return CRED_SIGNIN_EDIT;
  return e.source_profile ? "run: aws sso login --profile " + e.source_profile : "sign in on the host";
}

function credSignIn(e, now) {
  const st = e && e.status;
  if (st !== "ok") return { cls: statusClass(st), text: statusText(st), sub: "" };
  const s = (e.signin && e.signin.state) || "";
  const n = now || new Date();
  const until = e.signin && e.signin.until;
  switch (s) {
    case "signed_in": {
      // An answer that was true when the page loaded and has run out since is not shown as green.
      const t = until ? new Date(until) : null;
      // The server may now say renewable or expired, and this page cannot tell which: say that, neutrally, not "expired".
      if (t && !isNaN(t.getTime()) && t.getTime() <= n.getTime()) return { cls: "idle", text: "Sign-in may have expired", sub: "reload to re-check" };
      return { cls: "ready", text: "Signed in", sub: until && credClock(until, n) ? "until " + credClock(until, n) : "" };
    }
    case "expired":
      return { cls: "locked", text: "Sign-in expired", sub: credSignInWhere(e) };
    case "never":
      return { cls: "idle", text: "Not signed in", sub: credSignInWhere(e) };
    case "renewable":
      return { cls: "idle", text: "Renews on use", sub: "token expired, refresh not checked" };
    case "not_sso":
      if (e.mode === "sso_direct") return { cls: "locked", text: "Not an SSO profile", sub: "SSO role directly needs one" };
      return { cls: "idle", text: "Not SSO", sub: "no sign-in needed" };
    case "incomplete":
      return { cls: "locked", text: "SSO profile incomplete", sub: e.source_profile ? e.source_profile + "'s SSO settings are missing or do not match its sso-session" : "SSO settings are missing or do not match the sso-session" };
    case "no_profile":
      return { cls: "locked", text: "Profile not found", sub: e.source_profile ? e.source_profile + " is not in the AWS config" : "not in the AWS config" };
  }
  return { cls: "idle", text: "Unknown", sub: "could not read the sign-in cache" };
}

// credRoleName: the part of a role ARN after "role/".
function credRoleName(arn) {
  const s = String(arn || "");
  const i = s.indexOf(":role/");
  return i >= 0 ? s.slice(i + 6) : s;
}

// credDeleteText: the confirm line for deleting a token or a GitHub App. It says
// what happens to the secret behind the entry, so a store value that does not
// exist must not be promised to stay.
function credDeleteText(kind, e) {
  const app = kind === "github";
  const head = (app ? "Delete the GitHub App " : "Delete the token ") + e.name + "? ";
  const inStore = (app ? e.key_ref_kind : e.value_kind) === "store";
  if (inStore && e.status === "missing") {
    // The view also reports "missing" when the store could not be listed.
    return head + (e.error ? "The store could not be read. Any value in it stays." : "Its store value is already missing.");
  }
  // A failed merge reads "invalid" whether or not the value exists.
  if (inStore && e.status === "invalid") return head + "Any value in the store stays; delete it under Stored values if you no longer need it.";
  const what = app ? "The private key " : "The value ";
  if (inStore) return head + what + "stays in the encrypted store; delete it under Stored values if you no longer need it.";
  return head + what + (app ? "file is not touched." : "is not touched.");
}

// --- the desktop page's tables (pure: the DOM is built in credTable) ---------------
//
// One table per provider group. A cell is {text, sub, cls, kind, mono, subMono,
// tag, seed, title}: `kind` "st" or "pv" is a status / probe verdict coloured by
// `cls`; `tag` is the PROD badge. The actions column is not here: it is DOM.

// CRED_TABLE_MIN_WIDTH: the floor of #creds-body (px) a table is drawn in; below it
// the page draws the cards the modal draws, whatever the content. It is a floor, not
// the answer: the width a table needs depends on its content, and a fixed number
// failed for four content shapes in a row (hyphenated names, hyphen-free names, a
// long Used by, and a PROD badge beside the "from config.json" chip, which holds the
// AWS Connection column at 181px and the table at about 725px). Above the floor the
// painter measures what a drawn table needs (credNeedW). The floor stays because the
// token tables (table-layout: fixed, shares of the width) break "release-bot" and put
// Used by under Edit at about 1000px of window, which no section overflow shows. It is
// the body's width, not the viewport's, because the sidebar is dragged between 200
// and 560px.
const CRED_TABLE_MIN_WIDTH = 700;

// credTablesAt: the one rule for tables or cards. Only the desktop page draws
// tables (a modal resized wide keeps its cards: the dialog must be a page). A body
// that cannot be measured (0 wide: the tab is hidden) keeps tables, as it always
// did, so a hidden tab never flips the mode. Otherwise the body must be at least the
// floor and at least `needW`, the width a drawn table was measured to need (0: none
// measured yet), so a table is drawn only where it fits. Pure, so the painter
// (credTableMode) and the resize observer (credWatchWidth) cannot disagree about it.
function credTablesAt(width, pageMode, needW) {
  if (!pageMode) return false;
  if (!(width > 0)) return true;
  return width >= Math.max(CRED_TABLE_MIN_WIDTH, needW > 0 ? needW : 0);
}

const CRED_TABLE_HEADS = {
  aws: ["Connection", "Account · role", "Sign-in", "Write probe", "Used by", ""],
  github: ["App", "Client ID · key", "Status", "Used by", ""],
  token: ["Token", "Env · agent may reach", "Status", "Used by", ""],
};

function credTableHead(key) {
  return CRED_TABLE_HEADS[key === "aws" || key === "github" ? key : "token"].slice();
}

// credProbeCell: the write-probe verdict, short. The full D4 sentence for a
// "writes" verdict is the warnbox under the row, so the cell only says it.
// CRED_SSO_DIRECT_LABEL / CRED_SSO_DIRECT_GATE_NOTE: an sso_direct connection has
// no base role to probe; its read-only proof is the gate, which runs at every
// save, spawn and mint. No verdict is cached, so none is claimed here.
const CRED_SSO_DIRECT_LABEL = "SSO role directly";
const CRED_SSO_DIRECT_GATE_NOTE = "Read-only gate runs on every use";
// credFullText: sessionrow.js' label for an acknowledged full-access connection (a const there, so not on globalThis).
const credFullText = () => (typeof AWS_FULL_ACCESS_TEXT === "string" ? AWS_FULL_ACCESS_TEXT : require("./sessionrow.js").AWS_FULL_ACCESS_TEXT);
// credFullAckSub: "acknowledged <date> by <actor>" under the label; only what the server recorded is named.
function credFullAckSub(a) {
  const x = a || {};
  const day = typeof x.at === "string" && /^\d{4}-\d{2}-\d{2}/.test(x.at) ? x.at.slice(0, 10) : "";
  return "acknowledged" + (day ? " " + day : "") + (x.actor ? " by " + x.actor : "");
}

function credProbeCell(e) {
  if (e.status !== "ok") return { text: "–", cls: "", kind: "pv", sub: "" };
  if (e.mode === "sso_direct" && e.full_access === true) return { text: credFullText(), cls: "bad", kind: "pv", sub: credFullAckSub(e.full_access_ack) };
  if (e.mode === "sso_direct") return { text: CRED_SSO_DIRECT_GATE_NOTE, cls: "", kind: "pv", sub: "" };
  const v = baseText(e.base);
  return { text: v.cls === "bad" ? "Can write" : v.text, cls: v.cls, kind: "pv", sub: checkedText(e.base && e.base.checked_at, new Date()) };
}

function credElevateText(el) {
  if (!el) return "no elevation";
  return "elevate: " + credRoleName(el.role_arn) + (el.max_minutes ? " · max " + el.max_minutes + "m" : "");
}

// credTableRow: the cells of one row. A name is machine text (mono, frame M2.3 §A6) and "nothing" under Used by is muted.
function credTableRow(key, e) {
  const used = { text: usedByText(e.used_by), muted: !(Array.isArray(e.used_by) && e.used_by.some(Boolean)) };
  const st = { text: statusText(e.status), cls: statusClass(e.status), kind: "st" };
  if (key === "aws") {
    const probe = credProbeCell(e);
    // The third cell is the sign-in state, not the config's status (#141): "Ready" there meant only "the entry parses".
    const si = credSignIn(e, new Date());
    const st = { text: si.text, cls: si.cls, kind: "st", sub: si.sub };
    return {
      cells: [
        { text: e.name, mono: true, tag: e.prod ? "PROD" : "", seed: isSeed(e), sub: credElevateText(e.elevate) },
        { text: e.account_id || "", sub: e.mode === "sso_direct" ? CRED_SSO_DIRECT_LABEL : credRoleName(e.role_arn), mono: true, subMono: e.mode !== "sso_direct" },
        st, probe, used,
      ],
      danger: e.status === "ok" && probe.cls === "bad",
      fullAccess: e.mode === "sso_direct" && e.full_access === true,
      entry: e,
    };
  }
  if (key === "github") {
    return {
      cells: [
        { text: e.name, mono: true, seed: isSeed(e) },
        { text: e.client_id || "", sub: e.key_ref_kind || "", mono: true },
        st, used,
      ],
      danger: false,
      entry: e,
    };
  }
  return {
    cells: [
      { text: e.name, mono: true, sub: e.label || "", title: e.description || "", tag: e.prod ? "PROD" : "", seed: isSeed(e) },
      { text: (e.env || []).join(", "), sub: (e.hosts || []).join(", "), mono: true, subMono: true },
      st, used,
    ],
    danger: false,
    entry: e,
  };
}

// credTableRows: one row per entry of an AWS, GitHub App or token group.
function credTableRows(g) {
  const key = g && g.key === "aws" ? "aws" : g && g.key === "github" ? "github" : "token";
  return ((g && g.entries) || []).map((e) => credTableRow(key, e));
}

const CRED_REF_PREFIXES = [
  [/^static_tokens\/(.+)$/, "token "],
  [/^github_apps\/(.+)$/, "GitHub App "],
  [/^accounts\/(.+)\.token_secret$/, "account "],
  [/^agents\/(.+)\.secrets$/, "profile "],
];

// valueRefsText: what reads a stored value, in words. An unknown shape is shown as is.
function valueRefsText(refs) {
  const list = Array.isArray(refs) ? refs.filter(Boolean) : [];
  if (!list.length) return "nothing";
  return list.map((r) => {
    for (const [re, label] of CRED_REF_PREFIXES) {
      const m = re.exec(r);
      if (m) return label + m[1];
    }
    return r;
  }).join(", ");
}

// valueUpdatedText: the date a value was last set (YYYY-MM-DD, as the server stamped it).
function valueUpdatedText(iso) {
  const m = /^(\d{4}-\d{2}-\d{2})/.exec(String(iso || ""));
  return m ? m[1] : "";
}

// --- Add / edit AWS connections: the pure decisions ---------------------------

const AWS_NAME_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;

// AWS_REGIONS: the AWS commercial regions, offered after the regions the host's own profiles name. Source: the aws partition of
// botocore endpoints.json (AWS CLI 2.38.1). A list of suggestions only: the input stays free text (a new region appears before
// this list does) and the server's format check (awsscope.regionRe) is the one that decides.
const AWS_REGIONS = [
  "us-east-1", "us-east-2", "us-west-1", "us-west-2",
  "af-south-1",
  "ap-east-1", "ap-east-2", "ap-south-1", "ap-south-2",
  "ap-southeast-1", "ap-southeast-2", "ap-southeast-3", "ap-southeast-4", "ap-southeast-5", "ap-southeast-6", "ap-southeast-7",
  "ap-northeast-1", "ap-northeast-2", "ap-northeast-3",
  "ca-central-1", "ca-west-1",
  "eu-central-1", "eu-central-2", "eu-west-1", "eu-west-2", "eu-west-3", "eu-north-1", "eu-south-1", "eu-south-2",
  "il-central-1", "me-south-1", "me-central-1", "mx-central-1", "sa-east-1",
];

// awsRegionSuggest(profiles): the regions the discovered profiles name (in profile order, each once), then
// AWS_REGIONS that are not already listed.
function awsRegionSuggest(profiles) {
  const out = [];
  for (const p of profiles || []) if (p && p.region && !out.includes(p.region)) out.push(p.region);
  for (const r of AWS_REGIONS) if (!out.includes(r)) out.push(r);
  return out;
}

// awsProfileSuggest(profiles): the names of the discovered profiles a connection can sign in with.
function awsProfileSuggest(profiles) {
  return (profiles || []).filter((p) => p && p.selectable && p.name).map((p) => p.name);
}

// awsConnName: a host profile's name as a connection name: lowercase, anything
// outside [a-z0-9-] becomes "-", leading non-alphanumerics dropped, at most 63.
// It can come out empty (a profile named "---"); the operator types one then.
function awsConnName(name) {
  return String(name == null ? "" : name).toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/^[^a-z0-9]+/, "").slice(0, 63);
}

// awsRowFromProfile: the prefilled row for one host profile. An assume-role
// profile hands over its role and the profile that signs for it; an SSO profile
// signs as itself and starts from "<its account>:role/" for the operator to
// finish (Find roles or typing); anything else signs as itself with no role.
function awsRowFromProfile(p) {
  const name = awsConnName(p && p.name);
  const row = { name, source_profile: "", role_arn: "", region: (p && p.region) || "", label: name.slice(0, 40), prod: /prod/i.test(name), needsRole: false };
  if (p && p.kind === "assume-role" && p.role_arn && p.source_profile) {
    row.source_profile = p.source_profile;
    row.role_arn = p.role_arn;
    return row;
  }
  row.source_profile = (p && p.name) || "";
  row.needsRole = true;
  if (p && p.kind === "sso" && p.sso_account_id) row.role_arn = "arn:aws:iam::" + p.sso_account_id + ":role/";
  return row;
}

// rowSaveState: whether a row may be saved. A row is saveable once the base
// role was assumed; a "writes" verdict also needs the operator's "I understand".
// An edit that leaves the role and its signing profile as they were (row.unchanged)
// needs no check: the server does not re-check it either.
function rowSaveState(row) {
  const r = row || {};
  // sso_direct has no assume step and no write "I understand": a read-only gate lets it save, a result the server calls
  // acknowledgeable saves with the full-access tick, and an unchanged edit is not exempt (the server re-runs the gate).
  if (r.mode === "sso_direct") {
    const g = r.gate;
    if (!g) return { canSave: false, needsAck: false };
    if (g.state === "read_only") return { canSave: true, needsAck: false };
    // A non-read-only result saves only when the server calls it acknowledgeable (ack_eligible === true) and
    // the operator ticked the box (or the stored connection is already acknowledged). The UI never judges the reason.
    if (g.ack_eligible !== true) return { canSave: false, needsAck: false };
    const done = r.fullAck === true || (!r.ackStale && !!(r.orig && r.orig.full_access === true));
    return done ? { canSave: true, needsAck: false } : { canSave: false, needsAck: true };
  }
  if (r.unchanged === true) return { canSave: true, needsAck: false };
  if (r.assume !== "ok") return { canSave: false, needsAck: false };
  if (r.verdict === "writes") return r.acked === true ? { canSave: true, needsAck: false } : { canSave: false, needsAck: true };
  return { canSave: true, needsAck: false };
}

// awsSplitList: one-per-line / comma / space separated text as a list.
function awsSplitList(text) {
  return String(text == null ? "" : text).split(/[\s,]+/).filter(Boolean);
}

// awsRoleARN: what the operator typed in the role field as an ARN. A bare role
// name (letters, digits, + = , . @ - _ and path slashes) is expanded with the
// profile's SSO account; anything else is returned as typed for the server to refuse.
function awsRoleARN(accountId, text) {
  const t = String(text == null ? "" : text).trim();
  if (!t || t.startsWith("arn:") || !accountId) return t;
  return /^[A-Za-z0-9+=,.@_\/-]+$/.test(t) ? "arn:aws:iam::" + accountId + ":role/" + t : t;
}

// AWS_AGENT_ROLE_DOC: how to create the agent role a binding should pick.
const AWS_AGENT_ROLE_DOC = "https://github.com/culiops/culibox/blob/main/docs/aws/agent-role/README.md";

// awsIsAgentRole: the agent role from docs/aws/agent-role is named agentbox-*
// (IAM role names ignore case).
function awsIsAgentRole(r) { return String(r && r.name).toLowerCase().startsWith("agentbox-"); }

// awsRolesShown: the roles to render for a filter (a case-insensitive name
// substring), at most `limit` of them, plus the chosen role wherever it sits so
// the selection never disappears. `matched` counts every match.
function awsRolesShown(roles, filter, limit, chosen) {
  const q = String(filter == null ? "" : filter).trim().toLowerCase();
  const hits = (roles || []).filter((r) => !q || String(r.name).toLowerCase().includes(q));
  const list = hits.slice(0, limit);
  if (chosen && !list.some((r) => r.arn === chosen)) {
    const c = (roles || []).find((r) => r.arn === chosen);
    if (c) list.push(c);
  }
  return { list, matched: hits.length };
}

// awsAgentRolePick: the role to preselect after a listing: the one agentbox-*
// role, but only while the role field is empty or the bare "...:role/" prefix,
// so nothing the operator typed is replaced. Several agent roles (say
// agentbox-agent and agentbox-agent-write) are the operator's call.
function awsAgentRolePick(roles, current) {
  const cur = String(current || "").trim();
  if (cur && !/^arn:aws[a-z-]*:iam::[0-9]{12}:role\/$/.test(cur)) return "";
  const agent = (roles || []).filter(awsIsAgentRole);
  return agent.length === 1 ? agent[0].arn : "";
}

// awsRolesNote: the line under a roles listing, and whether it links the agent
// role doc: an empty or capped list, or one without an agentbox-* role.
function awsRolesNote(res) {
  if (!res || res.sso_expired) return { text: "", doc: false };
  if (res.access_denied) return { text: "This profile can't list roles; type the role name", doc: false };
  if (res.failed) return { text: "Could not list the roles. Type the role ARN yourself.", doc: false };
  const roles = res.roles || [];
  if (!roles.length) return { text: "No roles found for this profile. Create the agent role, or type the role ARN yourself.", doc: true };
  if (res.capped) return { text: "Showing the first " + roles.length + " roles, agentbox-* first. If the agent role is not here, create it or type its ARN.", doc: true };
  if (!roles.some(awsIsAgentRole)) return { text: "No agentbox-* role here. Create the agent role, or type the role ARN yourself.", doc: true };
  return { text: "Or type the role ARN yourself if the list does not show it.", doc: false };
}

// awsConnFromRow: the connection object the save routes take. Fields that are
// empty are left out, so an edit that clears them clears them. An sso_direct
// connection has no role, no elevate and no S3 read list, whatever the row still
// holds from the other mode.
function awsConnFromRow(row) {
  const direct = row.mode === "sso_direct";
  const c = { name: row.name, source_profile: row.source_profile, region: row.region };
  if (direct) c.mode = "sso_direct"; else c.role_arn = row.role_arn;
  if (row.label) c.label = row.label;
  if (row.description) c.description = row.description;
  if (row.prod) c.prod = true;
  const egress = awsSplitList(row.egress);
  if (egress.length) c.egress_allow = egress;
  const s3 = awsSplitList(row.s3);
  if (s3.length && !direct) c.s3_read = s3;
  if (row.elevRole && !direct) {
    c.elevate = { role_arn: row.elevRole, max_minutes: parseInt(row.elevMinutes, 10) === 30 ? 30 : 60 };
    if (row.elevIAM) c.elevate.allow_iam = true;
    if (row.elevBoxPrefix) c.elevate.box_bucket_prefix = row.elevBoxPrefix; // no form field: sent back as read
    const pass = (row.elevPass || "").trim();
    if (pass) c.elevate.pass_role_prefix = pass;
  }
  return c;
}

// --- sso_direct: the trade-off text and the gate verdict ----------------------

// AWS_SSO_MAX_HOURS: the longest an SSO permission-set session can last. Before a
// check there is no expiry to read, so the text states this bound rather than a
// shorter number it does not know.
const AWS_SSO_MAX_HOURS = 12;

// awsModeText: the trade-off the operator accepts by picking SSO role directly.
// N is the credentials' remaining life in whole hours, rounded up; unknown or out
// of range reads as the SSO maximum.
function awsModeText(hours) {
  const h = Math.ceil(Number(hours));
  const n = Number.isFinite(h) && h >= 1 && h <= AWS_SSO_MAX_HOURS ? h : AWS_SSO_MAX_HOURS;
  return "SSO role directly: the agent gets your permission set's own credentials, valid for up to " + n +
    " h. CloudTrail shows you, not the agent, and they cannot be revoked when the session ends. A permission set that is not read-only needs an acknowledgement and then has full access.";
}

// awsHoursFromExpiry: whole hours from now to an RFC 3339 expiry, rounded up; 0
// when the expiry is empty, unreadable or past.
function awsHoursFromExpiry(expires, nowMs) {
  const t = Date.parse(expires);
  if (!Number.isFinite(t) || t <= nowMs) return 0;
  return Math.ceil((t - nowMs) / 3600000);
}

// The gate's descriptive data-read actions (awsprobe.DataReadActions) by the
// friendly name the report uses, in the order the spec lists them.
const AWS_DATA_READ_NAMES = [
  ["secret values", (a) => a === "secretsmanager:getsecretvalue"],
  ["SSM parameters", (a) => a.startsWith("ssm:")],
  ["DynamoDB items", (a) => a.startsWith("dynamodb:")],
  ["S3 objects", (a) => a.startsWith("s3:")],
  ["KMS decrypt", (a) => a.startsWith("kms:")],
];

// awsDataReadName: the friendly name of one data-read action; an action this
// table does not know is shown as it is.
function awsDataReadName(action) {
  const a = String(action || "").toLowerCase();
  const hit = AWS_DATA_READ_NAMES.find(([, test]) => test(a));
  return hit ? hit[0] : String(action || "");
}

// awsDataReadSummary: the step-6 report as friendly names. A name is "denied"
// only when every action behind it is denied; one still readable action makes the
// whole name readable, so the report never understates what the agent can read.
function awsDataReadSummary(reads) {
  const by = new Map();
  for (const r of Array.isArray(reads) ? reads : []) {
    if (!r) continue;
    const name = awsDataReadName(r.action);
    by.set(name, (by.get(name) !== false) && r.denied === true);
  }
  const order = AWS_DATA_READ_NAMES.map(([n]) => n);
  const names = [...by.keys()].sort((a, b) => (order.indexOf(a) + 1 || 99) - (order.indexOf(b) + 1 || 99));
  return { denied: names.filter((n) => by.get(n)), readable: names.filter((n) => !by.get(n)) };
}

// awsRefusedLine: one refused item as a line. The server's Verdict has no JSON tags
// (Pattern/Why); lower-case keys are read too.
function awsRefusedLine(v, needsContext) {
  const pat = String((v && (v.pattern != null ? v.pattern : v.Pattern)) || "");
  const why = String((v && (v.why != null ? v.why : v.Why)) || "");
  return pat + (why ? " \u2014 " + (needsContext ? "needs context: " : "") + why : needsContext ? " \u2014 needs context" : "");
}

// awsGateView: how the read-only gate's answer reads on a form row.
//   tone      ok | warn | bad
//   badge, text
//   refused   one line per refusing pattern or action (for missing_context they are
//             labelled "needs context": nothing proved them to be writes)
//   missing   IAM actions the permission set needs to read its own policy
//   signIn    the SSO login expired: offer the in-UI sign-in and a retry
//   dataReads / dataReadText   the descriptive step-6 report
// Anything that is not read_only or not_read_only is a warning: it never saves.
function awsGateView(gate) {
  const g = gate || {};
  const out = { tone: "warn", badge: "Not verified", text: "", refused: [], missing: [], signIn: false,
    dataReads: awsDataReadSummary(g.data_reads), dataReadText: "", ackEligible: false };
  // The server says whether this result can be acknowledged; the reason is never read for it.
  const elig = g.state !== "read_only" && g.ack_eligible === true;
  out.ackEligible = elig;
  const ackNote = " Saving it needs your acknowledgement of full access.";
  const dr = out.dataReads;
  if (Array.isArray(g.data_reads) && g.data_reads.length) {
    out.dataReadText = "Your permission set denies: " + (dr.denied.join(", ") || "nothing") + ". Still readable: " + (dr.readable.join(", ") || "nothing") + ".";
  }
  const list = Array.isArray(g.refused) ? g.refused : [];
  if (g.state === "read_only") {
    out.tone = "ok";
    out.badge = "Read-only";
    out.text = "The read-only gate passed for this permission set.";
    return out;
  }
  if (g.state === "not_read_only") {
    out.tone = "bad";
    out.badge = "Not read-only";
    out.refused = list.map((v) => awsRefusedLine(v, false));
    // g.incomplete (server field): why the check stopped after it had already found these.
    if (g.incomplete === "simulate_denied") {
      out.text = "This permission set can write or reach credentials, and it cannot simulate its own policy, so the gate could not check further." +
        (elig ? ackNote : " Nothing is saved until it can.");
      return out;
    }
    if (g.incomplete) {
      out.signIn = g.incomplete === "sso_expired";
      out.text = "This permission set can write or reach credentials, and the check stopped early, so nothing is saved." +
        (out.signIn ? " Sign in again on this host, then check again." : " Check again.");
      return out;
    }
    out.text = elig
      ? "This permission set can write or reach credentials." + ackNote
      : "This permission set can write or reach credentials, so it cannot be saved as SSO role directly. Use a read-only permission set, or the agent role.";
    return out;
  }
  switch (g.reason) {
    case "sso_expired":
      out.badge = "SSO expired";
      out.text = "Sign in again on this host, then check again.";
      out.signIn = true;
      break;
    case "missing_permissions": {
      out.badge = "Cannot read its own policy";
      out.missing = (Array.isArray(g.missing_perms) ? g.missing_perms : []).map(String);
      out.text = "The gate could not read the permission set's own policy" + (out.missing.length ? "; it needs " + out.missing.join(", ") : "") + "." + (elig ? ackNote : " Nothing is saved until it can.");
      break;
    }
    case "missing_context":
      out.badge = "Needs context";
      out.text = "These actions depend on condition values the gate cannot know, so it could not prove them read-only." + (elig ? ackNote : " Nothing is saved until it can.");
      out.refused = list.map((v) => awsRefusedLine(v, true));
      break;
    case "timeout":
      out.text = "The check timed out. Check again."; break;
    case "simulate_denied":
      out.text = "The permission set cannot simulate its own policy, so the gate could not verify it." + (elig ? ackNote : ""); break;
    case "probe_disabled":
      out.text = "The access check is switched off on this host, so a permission set cannot be verified."; break;
    default:
      out.text = "The check could not finish. Check again.";
      out.refused = list.map((v) => awsRefusedLine(v, false));
  }
  return out;
}

const CRED_TRUST_HELP = "The role does not trust the signer. Ask the account admin for a role without write access that trusts CuliBox's signing identity; CuliBox does not create it.";

// awsCheckView: how a check answer reads on a row. badge/text describe the
// assume step; verdict (assume ok only) is baseText's reading of the write
// probe; actions and needsAck are for a "writes" verdict. An "error" assume also
// covers a probe that assumed fine and then could not finish, so it never claims
// the assume failed.
function awsCheckView(res) {
  const r = res || {};
  const out = { assume: r.assume || "", badge: "", text: "", tone: "warn", verdict: null, actions: [], needsAck: false, signIn: false };
  switch (r.assume) {
    case "ok":
      out.tone = "ok";
      out.text = "Assume worked.";
      out.verdict = baseText({ state: r.verdict, actions: r.actions, reason: r.reason, reason_text: r.reason_text });
      out.actions = credWriteActions({ state: r.verdict, actions: r.actions });
      out.needsAck = r.verdict === "writes";
      break;
    case "denied":
      out.badge = "Trust missing";
      out.tone = "bad";
      out.text = CRED_TRUST_HELP;
      break;
    case "sso_expired":
      out.badge = "SSO expired";
      out.text = "Sign in again on this host.";
      out.signIn = true;
      break;
    default:
      out.badge = "Check not finished";
      out.text = "Could not finish the check" + (r.reason_text ? ": " + r.reason_text : ".");
  }
  return out;
}

// awsRowStateFor: rowSaveState with the edit rule filled in (an edit that leaves
// the role and its signing profile alone needs no check). The mode is part of
// "alone": switching mode is a change, and sso_direct never counts as unchanged.
function awsRowStateFor(row) {
  const unchanged = row.orig ? row.source_profile === row.orig.source_profile && row.role_arn === row.orig.role_arn && (row.mode || "") === (row.orig.mode || "") : undefined;
  return rowSaveState({ assume: row.assume, verdict: row.verdict, acked: row.acked, unchanged, mode: row.mode, gate: row.gate, fullAck: row.fullAck, ackStale: row.ackStale, orig: row.orig });
}

// awsAckBody: the acknowledgement a save sends, or null. Only a ticked box on an acknowledgeable gate produces one;
// it carries what the operator saw (the refusing patterns and the missing grants), which the server checks against its own run.
function awsAckBody(row) {
  const g = row && row.gate;
  if (!row || row.mode !== "sso_direct" || row.fullAck !== true || !g || g.state === "read_only" || g.ack_eligible !== true) return null;
  const pat = (v) => String(v && typeof v === "object" ? (v.pattern != null ? v.pattern : v.Pattern) : v);
  return { refused: (Array.isArray(g.refused) ? g.refused : []).map(pat), missing_perms: (Array.isArray(g.missing_perms) ? g.missing_perms : []).map(String) };
}

// awsErrText: a failed AWS-screen request in words. The server's own sentence is
// used for anything not special-cased; it never carries a secret.
function awsErrText(e) {
  if (!e) return "Something went wrong";
  if (e.status === 429) {
    const n = parseInt(e.retryAfter, 10);
    return "Too many AWS calls, try again in " + (n > 0 ? n + " s" : "a few seconds");
  }
  if (e.code === "no_broker") return "There is no AWS signer on this host (the broker is not running), so accounts cannot be checked or added here.";
  if (e.code === "exists") return "A connection with this name already exists.";
  if (e.code === "seed") return "That name belongs to an entry in config.json.";
  if (e.code === "in_use") {
    const by = e.body && Array.isArray(e.body.used_by) ? e.body.used_by.filter(Boolean) : [];
    return "Used by profile " + (by.length ? by.join(", ") : "(unknown)") + ". Remove it there first.";
  }
  return e.message || "Something went wrong";
}

// --- Tokens, GitHub Apps, stored values, store creation: the pure decisions ---

// Keys that carry a secret on its way to the server. They are stripped from
// anything this module keeps (credKeep): a value lives in the input that holds
// it until the request is sent, then in neither.
const CRED_SECRET_KEYS = ["values", "value", "private_key_value", "passphrase", "key"];

// ghKeyID: the store id a pasted GitHub App key is saved under, as the server
// derives it (defaultAppKeyID in credentials.go): characters outside
// [A-Za-z0-9_-] become "-", and the name is cut so the id fits 64 characters.
function ghKeyID(name) {
  return "gh-" + String(name || "").replace(/[^A-Za-z0-9_-]/g, "-").slice(0, 57) + "-key";
}

// templateForm: what the Add service token form asks for, from one catalog template.
// A select starts on its first option; Sentry's URL shows only when "Self-hosted"
// is chosen (`when`). `values` are the secrets to paste, in the template's order
// (Datadog has two). defaultName is "" when the operator has to name it ("other").
function templateForm(t) {
  const tpl = t || {};
  const fields = (tpl.fields || []).map((f) => {
    const out = { key: f.key, label: f.label, kind: f.kind, options: f.options || [], def: f.kind === "select" && f.options && f.options.length ? f.options[0].value : "" };
    if (tpl.id === "sentry" && f.key === "url") out.when = { key: "region", value: "self" };
    return out;
  });
  return {
    id: tpl.id || "", label: tpl.label || "", fields,
    values: (tpl.values || []).map((label, index) => ({ index, label })),
    defaultName: tpl.id && tpl.id !== "other" ? tpl.id : "",
  };
}

// fromTemplateBody: the POST /api/credentials/tokens/from-template body. `form`
// is { name, label, prod, fields, values }. The values are sent exactly as typed
// (not trimmed: a stored value is verbatim) and the caller clears the inputs
// afterwards; nothing here keeps them.
function fromTemplateBody(t, form) {
  const f = templateForm(t);
  const given = (form && form.fields) || {};
  const resolved = {};
  for (const fd of f.fields) {
    let v = given[fd.key];
    if (fd.kind === "select") {
      if (!fd.options.some((o) => o.value === v)) v = fd.def;
    } else {
      v = v == null ? "" : String(v).trim();
    }
    resolved[fd.key] = v;
  }
  const fields = {};
  for (const fd of f.fields) {
    if (fd.when && resolved[fd.when.key] !== fd.when.value) continue;
    fields[fd.key] = resolved[fd.key];
  }
  const vals = (form && form.values) || [];
  return {
    template: f.id,
    name: String((form && form.name) || "").trim(),
    label: String((form && form.label) || "").trim(),
    prod: !!(form && form.prod),
    fields,
    values: f.values.map((v) => (vals[v.index] == null ? "" : String(vals[v.index]))),
  };
}

// valueFieldCleared: empty a value input, whatever happened to its request.
// Returns true once it is empty (so a caller can assert it).
function valueFieldCleared(el) {
  if (!el) return true;
  el.value = "";
  if (typeof el.removeAttribute === "function") el.removeAttribute("value");
  return el.value === "";
}

// credUniqueName: base, else base-2, base-3 ... the first not in `taken`.
function credUniqueName(base, taken, suffixes) {
  if (!base) return "";
  const used = new Set(taken || []);
  // A template that defines several entries (Datadog: <name>-api, <name>-app)
  // needs every name it creates to be free, not the bare one.
  const sfx = suffixes && suffixes.length ? suffixes : [""];
  const free = (c) => sfx.every((x) => !used.has(c + x));
  if (free(base)) return base;
  for (let n = 2; ; n++) if (free(base + "-" + n)) return base + "-" + n;
}

// credTemplateSuffixes: the suffixes the server adds to a template token's name
// (see valueIdsText). Empty for a one-entry template.
function credTemplateSuffixes(t) {
  return t && t.id === "datadog" ? ["-api", "-app"] : [];
}

// valueIdsText: where the pasted value(s) will be stored (a hint under the field).
// With an index, the id of that one value (Datadog stores two: <name>-api, <name>-app).
function valueIdsText(t, name, index) {
  const n = String(name || "").trim();
  if (!n) return "the token name";
  if (!t || t.id !== "datadog") return n;
  if (index === 0) return n + "-api";
  if (index === 1) return n + "-app";
  return n + "-api and " + n + "-app";
}

function credStripSecrets(x) {
  if (Array.isArray(x)) return x.map(credStripSecrets);
  if (x && typeof x === "object") {
    const out = {};
    for (const k of Object.keys(x)) if (!CRED_SECRET_KEYS.includes(k)) out[k] = credStripSecrets(x[k]);
    return out;
  }
  return x;
}

// credKeep: the one way a form's non-secret state is kept in this module. Any
// secret key is dropped, deeply, so a value cannot ride along by accident.
function credKeep(form) {
  credForm = form == null ? null : credStripSecrets(form);
  return credForm;
}

// credState: what this module is holding (for tests): the read model and the open form.
function credState() {
  return { view: credView, form: credForm };
}

// tokenEditBody: the PUT body for an edit of label, description and prod. The
// entry is the read model's own copy of the saved entry, so every provider
// field goes back exactly as it was; `value` (a replacement) is added only when given.
function tokenEditBody(entry, form) {
  const e = JSON.parse(JSON.stringify(entry || {}));
  const label = String((form && form.label) || "").trim();
  const desc = String((form && form.description) || "").trim();
  if (label) e.label = label; else delete e.label;
  if (desc) e.description = desc; else delete e.description;
  if (form && form.prod) e.prod = true; else delete e.prod;
  const body = { entry: e };
  if (form && form.value) body.value = form.value;
  return body;
}

// appBody: the GitHub App save body. A pasted key goes as private_key_value, a
// file path (absolute, checked by the caller and the server) as a file reference;
// neither leaves the saved reference alone (an edit that does not touch the key).
function appBody(form) {
  const f = form || {};
  const app = { name: String(f.name || "").trim(), client_id: String(f.client_id || "").trim() };
  if (f.useFile) {
    const path = String(f.path || "").trim();
    if (path) app.private_key = { type: "file", path };
    return { app };
  }
  const body = { app };
  if (f.key) body.private_key_value = f.key;
  return body;
}

// storeCreateCheck: whether the Create the encrypted store form may be sent.
// passMode (Linux): the passphrase twice, at least 12 characters, counted in
// characters. Without it (macOS Keychain) there is nothing to check.
function storeCreateCheck(passMode, a, b) {
  if (!passMode) return { ok: true, msg: "" };
  const p = String(a || "");
  if (!p) return { ok: false, msg: "Type a passphrase." };
  if ([...p].length < CRED_PASS_MIN) return { ok: false, msg: "The passphrase must be at least " + CRED_PASS_MIN + " characters." };
  if (p !== String(b || "")) return { ok: false, msg: "The two passphrases do not match." };
  return { ok: true, msg: "" };
}

const CRED_PASS_MIN = 12;

// credErrText: a failed request on the token / GitHub App / value / store screens
// in words. `noun` names what was being saved ("token", "GitHub App", "value").
// The server's own sentence is used for anything not special-cased; it never
// carries a secret.
function credErrText(e, noun) {
  if (!e) return "Something went wrong";
  if (e.status === 429) {
    const n = parseInt(e.retryAfter, 10);
    return "Too many requests, try again in " + (n > 0 ? n + " s" : "a few seconds");
  }
  if (e.code === "exists") return "A " + noun + " with this name already exists.";
  if (e.code === "seed") return "That name belongs to an entry in config.json.";
  if (e.code === "store_missing") return "The encrypted store is not created yet. Create it first.";
  if (e.code === "in_use") {
    const b = e.body || {};
    const by = (Array.isArray(b.used_by) ? b.used_by : Array.isArray(b.by) ? b.by : []).filter(Boolean);
    if (noun === "value") return "Still used by " + valueRefsText(by) + ". Remove those first.";
    return "Used by profile " + (by.length ? by.join(", ") : "(unknown)") + ". Remove it there first.";
  }
  return e.message || "Something went wrong";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    templateForm, fromTemplateBody, valueFieldCleared, credUniqueName, credKeep, credState, credErrText,
    tokenEditBody, appBody, storeCreateCheck, valueIdsText, CRED_SECRET_KEYS, ghKeyID, awsSSOTimedOut, credTemplateSuffixes,
    credGroups, credGroupIcon, statusText, statusClass, baseText, credWriteActions, usedByText, entryActions, isSeed,
    providerTitle, checkedText, credRoleName, valueRefsText, valueUpdatedText, credTableRows, credTableHead, awsGateBoxEmpty,
    awsConnName, awsRowFromProfile, rowSaveState, awsSplitList, awsRoleARN, awsConnFromRow, awsCheckView, awsErrText,
    AWS_REGIONS, awsRegionSuggest, awsProfileSuggest,
    awsGateView, awsAckBody, awsModeText, awsHoursFromExpiry, awsDataReadName, awsRowStateFor,
    awsRolesShown, awsAgentRolePick, awsRolesNote, AWS_AGENT_ROLE_DOC, awsFootNote, awsFootReasons, setDisabledReason,
    CRED_D4, CRED_N3, CRED_FILE_BAD, CRED_D4_REMEDY,
    GH_ROUTE_CHOICES, GH_PAT_SUGGESTION, ghPatValueWarning, GH_APP_HELP, credDeleteText, credColumnCount,
    CRED_TABLE_MIN_WIDTH, credTablesAt, credSignIn, credClock,
  };
}

// --- DOM wiring (browser only) -------------------------------------------------

// The last GET /api/credentials, or null before it has answered. Never holds a
// secret: the read model carries references and states only.
let credView = null;
// The Add / Edit token, GitHub App or stored-value view while it is open, else
// null. Kept only through credKeep, which drops every secret key.
let credForm = null;
// True when a repaint was held back because the unlock field had focus; it is
// done once the field lets go (see credWireHeld).
let credRepaintHeld = false;
let credHeldWired = false;
// The mode (tables or cards) of the last paint, and whether the resize observer
// is wired: see credWatchWidth.
let credPaintedTables = false;
// credNeedW: the width of #creds-body (px) a table was last measured to need, 0 when
// nothing was measured. It only grows while the same data is shown, so tables and
// cards cannot take turns at one width; new data forgets it (renderCredentials).
let credNeedW = 0;
let credWidthWatched = false;
let credFontsWatched = false;
// The Add / Edit AWS view while it is open, else null. See awsOpenAdd.
let awsFlow = null;
// The host a token / GitHub App / GitHub-choice view was opened in (see credFlowStart), or null when
// it is the Credentials tab's. credForm holds the non-secret state and must stay plain data, so the
// elements live here: credFormOpts is the caller's `opts`, credFormBtns the Save and Cancel the view
// drew into opts.foot.
let credFormOpts = null;
let credFormBtns = null;
// Counts the flows dropped (credFlowDrop): a flow in a host that was waiting for the server when its
// host went away sees the number move and opens nothing.
let credHostSeq = 0;

// Later flows register here by entry kind ("aws", "github", "token", "value"):
// { add: fn(), edit: fn(entry), delete: fn(entry) }. A card shows an Edit or
// Delete button only when its kind has that handler, so there is never a dead
// button, and a group shows its Add button only when there is one.
const credActionHandlers = {};

function credEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

function credChip(status) {
  return credEl("span", "st " + statusClass(status), statusText(status));
}

function credBadge(cls, text) {
  return credEl("span", "bdg " + cls, text);
}

// credStatusNode: a status on the page's tables (frame M2.3, F27). "Missing" is a neutral chip: it is a state of the
// config, not a request (the legacy secret nobody uses), so it is not amber and not red. Ready stays green text
// (.st.ready) and every other status keeps its class; "Not set in the daemon's environment" stays text (a chip is one
// line and that sentence is not). A store that is locked is the one amber (.st.locked: it blocks spawns).
function credStatusNode(status) {
  return statusClass(status) === "missing" ? credEl("span", "chip", statusText(status)) : credChip(status);
}

// credMeta: a <dl> of label/value pairs; pairs with an empty value are left out.
function credMeta(pairs) {
  const dl = credEl("dl", "meta");
  for (const [k, v] of pairs) {
    if (v == null || v === "") continue;
    const row = credEl("div");
    row.append(credEl("dt", null, k), credEl("dd", null, v));
    dl.appendChild(row);
  }
  return dl;
}

// credActions: an entry's Edit and Delete. `kit` is the page's table row (frame M2.3): a ghost Edit and a danger
// Delete at the kit's small size, each named for its row. The cards (the phone's, and a narrow page's) keep `btn` and
// `btn dgr`.
function credActions(entry, reload, kit) {
  const wrap = credEl("div", "ent-act");
  const h = credActionHandlers[entry.kind] || {};
  for (const act of entryActions(entry)) {
    if (typeof h[act] !== "function") continue;
    const label = act === "delete" ? "Delete" : "Edit";
    const cls = kit ? (act === "delete" ? "btn btn-danger btn-sm" : "btn btn-ghost btn-sm") : (act === "delete" ? "btn dgr" : "btn");
    const b = credEl("button", cls, label);
    b.type = "button";
    if (kit) b.setAttribute("aria-label", label + " " + entry.name);
    b.onclick = () => h[act](entry, reload, b);
    wrap.appendChild(b);
  }
  return wrap;
}

// credCard: the shared shape of an AWS / GitHub App / token card. `title` and
// `sub` are already-resolved text; body is an array of nodes.
function credCard(entry, title, sub, body, extra) {
  const art = credEl("article", "ent" + (extra && extra.danger ? " danger" : ""));
  const top = credEl("div", "ent-top");
  top.appendChild(credEl("span", "nm", title));
  if (sub) top.appendChild(credEl("span", "lab", sub));
  if (entry.prod) top.appendChild(credBadge("prod", "PROD"));
  if (isSeed(entry)) top.appendChild(credBadge("seed", "from config.json"));
  top.appendChild(extra && extra.chip ? credEl("span", "st " + extra.chip.cls, extra.chip.text) : credChip(entry.status));
  art.append(top, credActions(entry, loadCredentials));
  const bd = credEl("div", "ent-body");
  if (entry.error) bd.appendChild(credEl("p", "ent-err", entry.error));
  for (const n of body) bd.appendChild(n);
  art.appendChild(bd);
  return art;
}

function credAWSCard(e) {
  const verdict = baseText(e.base);
  const direct = e.mode === "sso_direct";
  const meta = [["Account", e.account_id], direct ? ["Mode", CRED_SSO_DIRECT_LABEL] : ["Role", credRoleName(e.role_arn)], ["Region", e.region]];
  if (e.source_profile) meta.push(["Profile", e.source_profile]);
  if (e.elevate) meta.push(["Elevate", credRoleName(e.elevate.role_arn) + (e.elevate.max_minutes ? " (" + e.elevate.max_minutes + " min)" : "")]);
  meta.push(["Used by", usedByText(e.used_by)]);
  // The card's chip is the sign-in state (the table's third cell), not the config's "Ready" (#141); its detail line is a row.
  const si = credSignIn(e, new Date());
  if (e.status === "ok" && si.sub) meta.push(["Sign-in", si.sub]);
  const body = [credMeta(meta)];
  const full = direct && e.full_access === true;
  if (e.status === "ok" && direct) {
    const v = credEl("div", "verdict" + (full ? " bad" : ""), full ? credFullText() : CRED_SSO_DIRECT_GATE_NOTE);
    if (full) v.appendChild(credEl("div", "when", credFullAckSub(e.full_access_ack)));
    body.push(v);
  } else if (e.status === "ok") {
    if (verdict.cls === "bad") {
      const box = credEl("div", "warnbox", verdict.text);
      box.setAttribute("role", "alert");
      const acts = credWriteActions(e.base);
      if (acts.length) box.appendChild(credEl("div", "remedy", "Allowed here: " + acts.join(", ")));
      box.appendChild(credEl("div", "remedy", CRED_D4_REMEDY));
      body.push(box);
    } else {
      const v = credEl("div", "verdict " + verdict.cls);
      v.appendChild(credEl("span", "g", verdict.cls === "ok" ? "✓" : "?"));
      const t = credEl("span");
      t.appendChild(credEl("b", null, verdict.text));
      const when = checkedText(e.base && e.base.checked_at, new Date());
      const note = verdict.cls === "ok" ? (when ? when + ", " : "") + "base role only" : when;
      if (note) t.appendChild(credEl("span", "when", " " + note));
      v.appendChild(t);
      body.push(v);
    }
  }
  return credCard(e, e.name, e.label, body, { danger: e.status === "ok" && (full || verdict.cls === "bad"), chip: si });
}

function credAppCard(e) {
  return credCard(e, e.name, "", [credMeta([
    ["Client ID", e.client_id],
    ["Key", e.key_ref_kind],
    ["Used by", usedByText(e.used_by)],
  ])], null);
}

function credValueRef(e) {
  return e.value_kind === "store" ? "store" : e.value_kind;
}

function credTokenCard(e) {
  const body = [credMeta([
    ["Env", (e.env || []).join(", ")],
    ["Agent may reach", (e.hosts || []).join(", ")],
    ["Value", credValueRef(e)],
    ["Used by", usedByText(e.used_by)],
  ])];
  if (e.description) body.unshift(credEl("p", "ent-desc", e.description));
  body.push(credEl("p", "honest", CRED_N3));
  return credCard(e, e.name, e.label, body, null);
}

// credValueRow: `kit` is the page's Stored values panel: ghost Replace, danger Delete (as the table rows').
function credValueRow(v, kit) {
  const row = credEl("div", "sv");
  const id = credEl("span", "id", v.id);
  const upd = credEl("span");
  upd.append(credEl("span", "k", "Updated"), document.createTextNode(valueUpdatedText(v.updated)));
  const refs = credEl("span", "refs" + (v.referenced_by && v.referenced_by.length ? "" : " none"));
  refs.append(credEl("span", "k", "Referenced by"), document.createTextNode(valueRefsText(v.referenced_by)));
  const acts = credEl("span", "acts");
  const h = credActionHandlers.value || {};
  if (typeof h.edit === "function") {
    const b = credEl("button", kit ? "btn btn-ghost btn-sm" : "btn", "Replace");
    b.type = "button";
    b.onclick = () => h.edit(v, loadCredentials, b);
    acts.appendChild(b);
  }
  if (typeof h.delete === "function") {
    const b = credEl("button", kit ? "btn btn-danger btn-sm" : "btn dgr", "Delete");
    b.type = "button";
    // A value something still reads cannot be deleted; the row says who reads it.
    if (v.referenced_by && v.referenced_by.length) {
      b.disabled = true;
      b.classList.add("off");
      b.title = "Still referenced. Remove the references first.";
    } else {
      b.onclick = () => h.delete(v, loadCredentials, b);
    }
    acts.appendChild(b);
  }
  row.append(id, upd, refs, acts);
  return row;
}

const CRED_VALUES_HINT = "Values are write-only: they go in encrypted and are never shown again. A value that something still references cannot be deleted.";

function credValuesBody(entries, kit) {
  const wrap = credEl("div");
  const head = credEl("div", "sv head");
  for (const t of ["Id", "Updated", "Referenced by", ""]) head.appendChild(credEl("span", null, t));
  wrap.appendChild(head);
  for (const v of entries) wrap.appendChild(credValueRow(v, kit));
  wrap.appendChild(credEl("p", "hint", CRED_VALUES_HINT));
  return wrap;
}

function credLegacyBody(entries) {
  const wrap = credEl("div");
  wrap.appendChild(credEl("p", "hint", CRED_LEGACY_NOTE));
  for (const l of entries) {
    const row = credEl("div", "leg");
    row.append(credEl("b", null, l.name), credChip(l.status));
    wrap.appendChild(row);
  }
  return wrap;
}

const CRED_LEGACY_NOTE = "Defined in config.json. Edit the file to change them, or move the value into the store.";

// credLegacyTable: Legacy secrets on the page (frame M2.3): the note above, then a two-column table with no header row,
// each ENV name in mono beside its status. The cards (the phone's, a narrow page's) keep credLegacyBody's rows.
function credLegacyTable(entries) {
  const note = credEl("p", "hint ctab-note-top", CRED_LEGACY_NOTE);
  const table = credEl("table", "ctab tbl");
  const cols = credEl("colgroup");
  cols.append(credEl("col"), credEl("col"));
  table.appendChild(cols);
  const body = credEl("tbody");
  for (const l of entries) {
    const tr = credEl("tr");
    const name = credEl("td");
    name.appendChild(credEl("span", "mono t-sm", l.name));
    const st = credEl("td");
    st.appendChild(credStatusNode(l.status));
    tr.append(name, st);
    body.appendChild(tr);
  }
  table.appendChild(body);
  return [note, table];
}

const CRED_EMPTY = {
  aws: "No AWS accounts yet. Add one and the agent gets short-lived credentials for its role, never your profile.",
  github: "No GitHub Apps yet. Add GitHub to choose a token or an App.",
  values: "No stored values.",
};

// credPageMode: the Settings dialog is the desktop page, not a modal.
function credPageMode() {
  if (typeof document === "undefined") return false;
  const m = document.getElementById("settings-modal");
  return !!m && m.classList.contains("as-page");
}

// credTableMode: the desktop Settings page draws tables where the modal draws
// cards, once the page has the room for them (the rule is credTablesAt's). Read
// when the screen is painted; a window dragged across the threshold afterwards is
// caught by credWatchWidth, which repaints from the cached view, so a table is
// never left clipping Edit and Delete in a body that has become too narrow.
function credTableMode() {
  const body = document.getElementById("creds-body");
  return credTablesAt(body ? body.clientWidth : 0, credPageMode(), credNeedW);
}

// credTableOverflow: how far the widest table section sticks out of its box (px, 0
// when every one fits). A section clips what does not fit (overflow: hidden), but its
// scrollWidth still counts it, so this reads what the user would not see.
function credTableOverflow(host) {
  let over = 0;
  for (const sec of host.querySelectorAll(".ctab-sec")) {
    const d = sec.scrollWidth - sec.clientWidth;
    if (d > over) over = d;
  }
  return over;
}

// credEntryHost: where an entry's inline confirmation goes. A card is its own
// host; a table entry gets a full-width row under it (made on first use).
function credEntryHost(btn) {
  const card = btn && btn.closest ? btn.closest(".ent") : null;
  if (card) return card;
  const tb = btn && btn.closest ? btn.closest("tbody.crow") : null;
  return tb ? credEntryExtra(tb) : null;
}

// credColumnCount: the columns of the table a tbody belongs to, read from its own
// row because a broken entry's extra row is built before the tbody is attached.
// A larger colSpan is not harmless: under table-layout: fixed it adds phantom
// grid columns, which starve the unsized Used by column to a couple of pixels.
function credColumnCount(tb) {
  const row = tb.rows[0];
  return row ? row.cells.length : 1;
}

function credEntryExtra(tb) {
  let x = tb.querySelector(".ctab-ex");
  if (!x) {
    const tr = credEl("tr", "ctab-x");
    const td = credEl("td");
    td.colSpan = credColumnCount(tb);
    x = credEl("div", "ctab-ex");
    td.appendChild(x);
    tr.appendChild(td);
    tb.appendChild(tr);
  }
  return x;
}

// credCellNode: one cell of a table row (only the page's tables are drawn from it). PROD is the kit's red chip, "from
// config.json" a neutral one, and a Missing status a neutral chip (credStatusNode); "nothing" under Used by is muted.
function credCellNode(c) {
  const td = credEl("td", c.muted ? "muted" : "");
  if (c.title) td.title = c.title;
  const main = credEl("div", "c-main" + (c.mono ? " mono" : ""));
  if (c.kind === "st" && c.cls === "missing") {
    main.appendChild(credEl("span", "chip", c.text));
  } else if (c.kind === "st" || c.kind === "pv") {
    main.appendChild(credEl("span", c.kind + (c.cls ? " " + c.cls : ""), c.text));
  } else {
    // One unit, so a chip after it drops to the next line rather than the
    // name breaking at its hyphen ("legacy-/aws").
    main.appendChild(credEl("span", "c-nm", c.text));
  }
  if (c.tag) main.appendChild(credEl("span", "chip chip-prod", c.tag));
  if (c.seed) main.appendChild(credEl("span", "chip", "from config.json"));
  td.appendChild(main);
  if (c.sub) td.appendChild(c.subMono ? credMonoSub(c.sub) : credEl("div", "c-sub", c.sub));
  return td;
}

// credMonoSub: a machine value's sub-line (hosts, a role). Where its column is too
// narrow it breaks after a dot rather than inside a label ("...example.co" / "m"): a
// <wbr> after each dot gives the line those places first. The text is unchanged.
function credMonoSub(text) {
  const d = credEl("div", "c-sub mono");
  const segs = String(text).split(".");
  segs.forEach((s, i) => {
    const last = i === segs.length - 1;
    d.appendChild(document.createTextNode(last ? s : s + "."));
    if (!last) d.appendChild(document.createElement("wbr"));
  });
  return d;
}

// credTable: the group's table. One <tbody class="crow"> per entry: the row, and
// under it (full width) the write warning, a broken entry's error, or a delete
// confirmation.
function credTable(g) {
  const table = credEl("table", "ctab tbl");
  const heads = credTableHead(g.key);
  // One colgroup for the head cells and every row (G16): the column shares are written once, in desktop.css, on these.
  const cols = credEl("colgroup");
  for (let i = 0; i < heads.length; i++) cols.appendChild(credEl("col"));
  table.appendChild(cols);
  const tr = credEl("tr");
  for (const h of heads) {
    const th = credEl("th", null, h);
    // The actions column has no heading to see; a screen reader still needs a name.
    if (!h) th.appendChild(credEl("span", "sr-only", "Actions"));
    tr.appendChild(th);
  }
  table.appendChild(credEl("thead")).appendChild(tr);
  for (const r of credTableRows(g)) {
    const tb = credEl("tbody", "crow");
    const row = credEl("tr", r.danger ? "danger" : "");
    for (const c of r.cells) row.appendChild(credCellNode(c));
    const act = credEl("td", "ctab-act");
    act.appendChild(credActions(r.entry, loadCredentials, true));
    row.appendChild(act);
    tb.appendChild(row);
    if (r.entry.error) {
      const ex = credEntryExtra(tb);
      ex.appendChild(credEl("p", "ent-err", r.entry.error));
    }
    if (r.danger && !r.fullAccess) {
      const ex = credEntryExtra(tb);
      const box = credEl("div", "warnbox", baseText(r.entry.base).text);
      box.setAttribute("role", "alert");
      const acts = credWriteActions(r.entry.base);
      if (acts.length) box.appendChild(credEl("div", "remedy", "Allowed here: " + acts.join(", ")));
      box.appendChild(credEl("div", "remedy", CRED_D4_REMEDY));
      ex.appendChild(box);
    }
    table.appendChild(tb);
  }
  return table;
}

// Stored values are folded on the page: write-only, so there is little to look at. The fold keeps its state through a
// repaint (Replace and Delete reload).
let credValuesOpen = false;

// credChevron: the disclosure's chevron, the glyph the frame draws (§B4 `span[aria-hidden] ›`, U+203A): hidden from the
// accessible name, 12px and muted. desktop.css gives it its own box (a transform does not apply to an inline one) and
// turns it a quarter when the row is open.
function credChevron() {
  const c = credEl("span", "vfold-chev", "\u203a");
  c.setAttribute("aria-hidden", "true");
  return c;
}

// credValuesFold: Stored values on the page (frame M2.3): a ghost button with aria-expanded (the chevron, the name), a
// count chip and the muted line, drawn closed; its body is the "+ Set a value" link and the list.
function credValuesFold(g) {
  const sec = credEl("section", "sec ctab-fold");
  sec.dataset.group = g.key;
  sec.setAttribute("aria-label", g.title);
  const row = credEl("div", "vfold-h");
  const toggle = credEl("button", "btn btn-ghost btn-sm vfold-t");
  toggle.type = "button";
  toggle.setAttribute("aria-expanded", credValuesOpen ? "true" : "false");
  toggle.append(credChevron(), document.createTextNode(g.title));
  row.append(toggle, credEl("span", "chip tnum", String(g.entries.length)), credEl("span", "t-sm muted", "Write-only: Replace or Delete"));
  const body = credEl("div", "vfold-b");
  body.id = "cred-values-body";
  toggle.setAttribute("aria-controls", body.id);
  body.hidden = !credValuesOpen;
  toggle.onclick = () => {
    credValuesOpen = !credValuesOpen;
    toggle.setAttribute("aria-expanded", credValuesOpen ? "true" : "false");
    body.hidden = !credValuesOpen;
  };
  const add = credActionHandlers.value && credActionHandlers.value.add;
  if (typeof add === "function") {
    // An add inside a block is the accent + link (F10), not a button.
    const b = credEl("button", "link link-add", "Set a value");
    b.type = "button";
    b.onclick = () => add(g, loadCredentials);
    body.appendChild(b);
  }
  body.appendChild(g.entries.length ? credValuesBody(g.entries, true) : credEl("p", "hint", CRED_EMPTY.values));
  sec.append(row, body);
  return sec;
}

// credSecHead: a section's head on the page's tables (frame M2.3): the kit's .card-head with the title and the count as a
// chip beside it. No add button: the page head's "Add credential" menu is the one add.
function credSecHead(title, count, glyph) {
  const h = credEl("div", "sec-h card-head");
  h.append(credTitleNode("h2", "t-md w-600", title, glyph), credEl("span", "chip tnum", String(count)));
  return h;
}

function credSection(g) {
  const tables = credTableMode();
  if (tables && g.key === "values") return credValuesFold(g);
  // The page's card is `ctab-sec` alone: desktop.css gives it the kit's panel fill and radius. Not the kit's `.card` class: that name is
  // also the phone's amber session card (app.css), which would style these nodes if the window crossed to a phone and Settings were
  // reopened before the next fetch replaced them.
  const sec = credEl("section", tables ? "sec ctab-sec" : "sec");
  sec.dataset.group = g.key;
  sec.setAttribute("aria-label", g.title);
  if (tables) {
    sec.appendChild(credSecHead(g.title, g.entries.length, credGroupIcon(g)));
  } else {
    const h = credEl("div", "sec-h");
    const title = credEl("span", "grow", g.title + " ");
    title.appendChild(credEl("span", "count", String(g.entries.length)));
    h.appendChild(title);
    const kind = g.key === "aws" ? "aws" : g.key === "github" ? "github" : g.key === "values" ? "value" : g.key.startsWith("token:") ? "token" : "";
    // On the desktop page a narrow window draws cards, and the head's "Add credential ▾" already offers all three adds (My call 6): the
    // cards carry no add of their own, so the page shows no primary ("Add AWS accounts" is `btn pri` on the phone). "Set a value" stays
    // (an add inside a block). The phone's card mode is unchanged.
    const add = kind && !(credPageMode() && kind !== "value") && credActionHandlers[kind] && credActionHandlers[kind].add;
    if (typeof add === "function") {
      const b = credEl("button", "btn" + (g.key === "aws" ? " pri" : ""), g.key === "aws" ? "Add AWS accounts" : g.key === "values" ? "Set a value" : g.key === "github" ? "Add GitHub" : "Add service token");
      b.type = "button";
      b.onclick = () => add(g, loadCredentials);
      h.appendChild(b);
    }
    sec.appendChild(h);
  }

  if (!g.entries.length) {
    sec.appendChild(credEl("p", "hint", CRED_EMPTY[g.key] || ""));
    return sec;
  }
  if (g.key === "values") { sec.appendChild(credValuesBody(g.entries)); return sec; }
  if (g.key === "legacy") {
    if (tables) sec.append(...credLegacyTable(g.entries));
    else sec.appendChild(credLegacyBody(g.entries));
    return sec;
  }
  if (tables) {
    sec.appendChild(credTable(g));
    if (g.key.startsWith("token:")) sec.appendChild(credEl("p", "honest", CRED_N3));
    return sec;
  }
  const stack = credEl("div", "stack");
  for (const e of g.entries) {
    stack.appendChild(g.key === "aws" ? credAWSCard(e) : g.key === "github" ? credAppCard(e) : credTokenCard(e));
  }
  sec.appendChild(stack);
  return sec;
}

// credStrip: the encrypted store's status, unlock and lock, folded into the top
// of the tab (it used to be a tab of its own). On the desktop page (frame M2.3) the store is said once: a dot, "Encrypted
// store", the muted state beside it (storeExpiryText), Lock now at the right, and the honest statement once as the note
// under it. A store that needs the user (locked, unavailable, not set up) keeps its amber strip and its words.
function credStrip(view) {
  const st = view.store || {};
  // A macOS Keychain failure: the server's reason and fix, nothing to type or lock.
  // It wins over "not set up": a store whose key is missing must never offer to be
  // created over.
  const unavail = st.unavailable || null;
  const locked = !unavail && st.initialized && st.locked && st.platform !== "darwin";
  // On the desktop page the strip is quiet (amber only when it needs you), in cards as in tables.
  const page = credPageMode();
  // `cstrip` alone, not `.card` (the phone's amber card, app.css): desktop.css's .cstrip rules draw its fill and radius.
  const strip = credEl("div", page ? "strip cstrip" + (locked || unavail || !st.initialized ? " need" : "") : "strip");
  if (unavail) {
    strip.appendChild(credEl("span", "st locked", "Keychain"));
    const txt = credEl("div", "txt");
    txt.appendChild(credEl("b", null, "Encrypted store: " + storeExpiryText(st, Date.now()) + ". "));
    txt.appendChild(document.createTextNode([unavail.reason, unavail.fix].filter(Boolean).join(" ")));
    strip.appendChild(txt);
  } else if (!st.initialized) {
    strip.appendChild(credEl("span", "st notset", "Not set up"));
    strip.appendChild(credEl("div", "txt", "There is no encrypted store yet. Without one, tokens can only come from the daemon's environment or a file."));
    const slot = credEl("div", "storeslot");
    const mk = credEl("button", "btn pri", "Create the encrypted store");
    mk.type = "button";
    mk.id = "store-create";
    mk.onclick = () => { slot.replaceChildren(credStoreCreateNode(() => loadCredentials(), true)); };
    slot.appendChild(mk);
    strip.appendChild(slot);
  } else if (page && !locked) {
    const dot = credEl("span", "dot dot-run");
    dot.setAttribute("aria-hidden", "true");
    strip.append(dot, credEl("b", null, "Encrypted store"), credEl("span", "muted", storeExpiryText(st, Date.now())));
  } else {
    strip.appendChild(credEl("span", "st " + (locked ? "locked" : "ready"), locked ? "Locked" : st.platform === "darwin" ? "Keychain" : "Unlocked"));
    const txt = credEl("div", "txt");
    const when = storeExpiryText(st, Date.now());
    txt.appendChild(credEl("b", null, "Encrypted store: " + when + ". "));
    txt.appendChild(document.createTextNode(locked
      ? "You can add and replace values while it is locked. Unlock it so new sessions can use them."
      : "Values you add here are encrypted at rest."));
    strip.appendChild(txt);
  }
  if (locked) {
    const slot = credEl("div", "storeslot");
    renderUnlockForm(slot, () => loadCredentials());
    strip.appendChild(slot);
  } else if (!unavail && st.initialized && st.platform !== "darwin") {
    const b = credEl("button", page ? "btn btn-sm" : "btn", "Lock now");
    b.type = "button";
    b.id = "store-lock";
    // Reload only after a lock that worked: a repaint rebuilds #store-err
    // empty, which would hide why the store is still unlocked.
    b.onclick = async () => { if (await lockStore()) loadCredentials(); };
    strip.appendChild(b);
  }
  const wrap = credEl("div", page ? "stack cstrip-wrap" : "stack");
  wrap.appendChild(strip);
  const pt = typeof storePlaintextText === "function" && storeState ? storePlaintextText(storeState.plaintext_copies) : "";
  if (pt) wrap.appendChild(credEl("p", "hint warnline", pt));
  wrap.appendChild(credEl("p", "hint", STORE_HONEST_STATEMENT));
  const err = credEl("div", "dlg-err");
  err.id = "store-err";
  wrap.appendChild(err);
  return wrap;
}

// --- the page head's "Add credential ▾" menu (frame M2.3) ----------------------------------------------------------------
//
// The tables have no add button of their own: the head's one action opens a kit menu with today's three adds, each the
// handler its section button used to run. settingshead.js paints the trigger (#set-act-creds, a plain secondary .btn with
// aria-haspopup) and hands it to credAddMenuAttach by name on the global object, so that one is a top-level function
// declaration. The pattern is the profile page's ⋯ menu (profilepage.js pfvMoreMenu): kitMenuButton rows, kitMenuKeys for
// the arrows, Esc closes and gives focus back to the trigger without also closing the page, a click elsewhere closes.

// credAddItems: the menu's rows, in order. `kind` names the credActionHandlers entry whose `add` the row runs.
function credAddItems() {
  return [
    { label: "Add AWS accounts", kind: "aws" },
    { label: "Add GitHub", kind: "github" },
    { label: "Add service token", kind: "token" },
  ];
}

// credAddRun: run a row's add with the group it sat in before: AWS's, GitHub's, or the first token provider's (the add
// starts on that provider); with no token yet there is no group, so it starts from { key: "token:" } (the first template).
// It is async because the AWS and token adds are (the AWS one reads the host's profiles, the token one the providers): focus moves
// into the flow when it has drawn, if one opened (credFlowFocus).
async function credAddRun(kind) {
  const h = credActionHandlers[kind];
  if (!h || typeof h.add !== "function") return;
  const groups = credGroups(credView || {});
  const group = kind === "token"
    ? groups.find((g) => g.key.startsWith("token:")) || { key: "token:" }
    : groups.find((g) => g.key === kind);
  await h.add(group, loadCredentials);
  credFlowFocus();
}

// credFlowFocus: a flow opened in the Credentials tab from the head menu has drawn: focus goes to its first control (the provider
// tiles, the GitHub routes, AWS's first profile tick box), not the crumb, which is a way out. A flow with no control yet (AWS with no
// profile to tick) takes the crumb, so focus is never left on <body> when the trigger it came from went away. Nothing when no flow
// opened (the add failed to start): focus is still on the trigger, which is still shown.
function credFlowFocus() {
  if (!credTabFlowOpen()) return;
  const host = document.getElementById("creds-body");
  if (!host) return;
  // Focus the user has put elsewhere while an async flow was drawing (AWS reads the host's profiles first) is theirs: only focus that
  // is lost (<body>) or still on the trigger moves.
  const at = document.activeElement;
  const btn = credAddWrap ? credAddWrap.querySelector("button") : null;
  if (at && at !== document.body && at !== document.documentElement && at !== btn) return;
  const usable = (n) => {
    if (n.disabled || n.type === "hidden") return false;
    for (let x = n; x && x !== host; x = x.parentElement) if (x.hidden) return false;
    return true;
  };
  const first = Array.from(host.querySelectorAll("input, select, textarea, button")).find(usable) || host.querySelector("a.lnk");
  if (first && typeof first.focus === "function") first.focus();
}

// The open menu ({ wrap, btn, pop }) or null, and the wrapper of the trigger the head last drew.
let credAddMenu = null;
let credAddWrap = null;
// A tab flow was open at the last credAddMenuSync, and it has just closed: once the list is drawn again the trigger takes focus
// back if focus was lost (credAddMenuRefocus).
let credAddFlowWas = false;
let credAddRefocus = false;
let credAddMenuWired = false;

function credAddMenuClose(refocus) {
  if (!credAddMenu) return;
  const m = credAddMenu;
  credAddMenu = null;
  m.pop.hidden = true;
  m.btn.setAttribute("aria-expanded", "false");
  if (refocus) m.btn.focus();
}

// credAddMenuSync: an Add / Edit view open in the tab owns the page (its own footer is the way out), so the head's add is
// not offered on top of it: the trigger and the divider after it go, and come back when the list does. Called wherever
// that changes (the views' footers, the list's repaint).
function credAddMenuSync() {
  const wrap = credAddWrap;
  if (!wrap) return;
  const open = credTabFlowOpen();
  wrap.hidden = open;
  const head = wrap.parentElement;
  const sep = head ? head.querySelector(".set-sep") : null;
  if (sep) sep.hidden = open;
  if (open) credAddMenuClose(false);
  // A flow that has just closed: its nodes stay on screen until the list repaints, so focus is given back then, not now.
  if (credAddFlowWas && !open) credAddRefocus = true;
  else if (open) credAddRefocus = false;
  credAddFlowWas = open;
}

// credAddMenuRefocus: the list has been drawn again (renderCredentials) after a flow opened from the head closed. The flow's
// controls are gone, and with them keyboard focus (it is on <body>): the trigger, shown again, takes it. Focus the user has put
// anywhere else in the meantime stays where it is.
function credAddMenuRefocus() {
  if (!credAddRefocus || credTabFlowOpen()) return;
  credAddRefocus = false;
  const wrap = credAddWrap;
  const btn = wrap && !wrap.hidden ? wrap.querySelector("button") : null;
  if (!btn || typeof btn.focus !== "function") return;
  const at = document.activeElement;
  if (!at || at === document.body || at === document.documentElement) btn.focus();
}

// credAddMenuAttach(btn): give the head's trigger its menu. Safe on every head repaint: each paint draws a new trigger,
// so a menu left open by the last one is dropped with its button, one trigger is wrapped once, and the document's
// click-away listener is added once.
function credAddMenuAttach(btn) {
  if (!btn || !btn.parentElement) return;
  credAddMenuClose(false);
  if (btn.parentElement.classList.contains("cred-add")) return;
  const wrap = credEl("div", "cred-add");
  btn.parentElement.insertBefore(wrap, btn);
  const pop = credEl("div", "menu");
  pop.setAttribute("role", "menu");
  pop.setAttribute("aria-label", "Add credential");
  pop.hidden = true;
  for (const it of credAddItems()) {
    pop.appendChild(kitMenuButton(it.label, it.kind, false, () => {
      // Focus goes to the trigger first, so an add that does not open (its error is in the tab's error line) leaves it there and
      // not on the menu row that has just closed. An add that does open draws over the list and hides the trigger
      // (credAddMenuSync): credAddRun then moves focus into the flow once it has drawn (credFlowFocus), and credAddMenuRefocus gives
      // it back to the trigger when the flow is closed (Cancel, Save, the crumb) and the list is drawn again.
      credAddMenuClose(true);
      credAddRun(it.kind);
    }));
  }
  wrap.append(btn, pop);
  btn.onclick = () => {
    if (credAddMenu && credAddMenu.wrap === wrap) { credAddMenuClose(false); return; }
    credAddMenuClose(false);
    credAddMenu = { wrap, btn, pop };
    pop.hidden = false;
    btn.setAttribute("aria-expanded", "true");
    const first = pop.querySelector(".menu-item");
    if (first) first.focus();
  };
  wrap.addEventListener("keydown", (ev) => {
    if (!credAddMenu || credAddMenu.wrap !== wrap) return;
    if (ev.key === "Escape") {
      ev.preventDefault();
      credAddMenuClose(true);
      return;
    }
    kitMenuKeys(pop, ev);
  });
  credAddWrap = wrap;
  if (!credAddMenuWired && typeof document !== "undefined" && typeof document.addEventListener === "function") {
    credAddMenuWired = true;
    document.addEventListener("click", (ev) => {
      if (credAddMenu && !credAddMenu.wrap.contains(ev.target)) credAddMenuClose(false);
    });
  }
  credAddMenuSync();
}

function credBanners(view) {
  const out = [];
  if (view.file_bad) {
    const b = credEl("div", "amberbox", CRED_FILE_BAD);
    b.setAttribute("role", "alert");
    out.push(b);
  }
  if ((view.invalid || []).length) {
    const box = credEl("div", "warnbox");
    box.setAttribute("role", "alert");
    box.appendChild(credEl("div", null, "Some saved entries are not valid and are not in use:"));
    for (const i of view.invalid) {
      box.appendChild(credEl("div", "remedy", i.kind + " " + i.name + ": " + i.error));
    }
    out.push(box);
  }
  return out;
}

// renderCredentials paints a read model into #creds-body. Replaces the whole
// body, so a repaint never keeps a stale card (an unlock form being typed into
// is the one exception: it is kept while it has focus).
function renderCredentials(view) {
  const host = document.getElementById("creds-body");
  if (!host) return;
  // New data forgets what a table was measured to need: the row that did not fit may be
  // gone. The cached view repainted for a new width is the same object and keeps it.
  if (view !== credView) credNeedW = 0;
  credView = view;
  credAddMenuSync();
  // The Add / Edit view owns the body until it is left; it reads credView when it needs it. A view in
  // a host (the drawer) does not own the tab's body.
  if (credTabFlowOpen()) return;
  if (typeof storeUnlockFocused === "function" && storeUnlockFocused()) {
    // Held, not dropped: repaint as soon as the field lets go.
    credRepaintHeld = true;
    return;
  }
  credRepaintHeld = false;
  credWireHeld(host);
  credWatchWidth(host);
  credPaint(host, view);
  // A table that does not fit is drawn once to find that out, then replaced by cards in
  // this same call, so nothing is seen. One retry, never a loop: credNeedW only grew, so
  // the second paint is cards (a body this wide is below the need).
  if (credPaintedTables && credGrowNeed(host)) credPaint(host, view);
  credScrollPending();
  credAddMenuRefocus();
}

// credPaint: one paint of the whole body. The mode this paint draws in (credSection
// and the table builders read the same credTableMode() below, in this same call) is
// kept for credWatchWidth.
function credPaint(host, view) {
  credPaintedTables = credTableMode();
  const nodes = [credStrip(view), ...credBanners(view)];
  const groups = credGroups(view);
  const hasTokens = groups.some((g) => g.key.startsWith("token:"));
  for (const g of groups) {
    // Token providers only appear once there is a token; the first one needs an Add button of its own.
    if (g.key === "values" && !hasTokens) nodes.push(credTokensEmpty());
    nodes.push(credSection(g));
  }
  host.replaceChildren(...nodes);
}

// credGrowNeed: after a table paint, measure the tables and, if any section sticks
// out of its box, make sure credNeedW covers what they need (it only ever grows).
// True when a section sticks out, i.e. the tables do not fit and the caller repaints,
// now in cards; false when they fit. The 2px is slack for rounding. A body that reads
// 0 wide (the tab is hidden) has nothing to measure.
function credGrowNeed(host) {
  if (!(host.clientWidth > 0)) return false;
  const over = credTableOverflow(host);
  if (!(over > 0)) return false;
  credNeedW = Math.max(credNeedW, host.clientWidth + over + 2);
  return true;
}

// credFocusGroup: the profile form's "none defined" links ask for the
// Credentials tab on one group ("aws", "github", "tokens"). The view paints
// after the tab is selected, so the wish is kept until renderCredentials has
// drawn, then the group is scrolled into view. "tokens" is the first token
// provider group, or the empty Tokens section when there is none.
let credFocusWanted = "";
function credFocusGroup(group) { credFocusWanted = String(group || ""); }
function credScrollPending() {
  const want = credFocusWanted;
  if (!want) return;
  credFocusWanted = "";
  const host = document.getElementById("creds-body");
  if (!host) return;
  const sel = want === "tokens" ? '[data-group="tokens"], [data-group^="token:"]' : '[data-group="' + want.replace(/[^a-z0-9:_-]/gi, "") + '"]';
  const el = host.querySelector(sel);
  if (el && typeof el.scrollIntoView === "function") el.scrollIntoView({ block: "start" });
}

// credWireHeld: once, repaint a held view when focus leaves the body.
function credWireHeld(host) {
  if (credHeldWired) return;
  credHeldWired = true;
  host.addEventListener("focusout", () => setTimeout(credFocusLeft, 0));
}

// credFocusLeft: focus left something in the body. A repaint held for the unlock
// field is made now, unless something is typed there (credWidthHold): focus leaves
// the field on the mousedown of the Unlock button, and a repaint before the click
// would wipe the passphrase and the button with it. Otherwise a width repaint that
// was held back gets another look.
function credFocusLeft() {
  const host = document.getElementById("creds-body");
  if (credRepaintHeld && credView) {
    if (!host || !credWidthHold(host)) renderCredentials(credView);
  } else {
    credRepaintForWidth();
  }
}

// credWidthHold: what a repaint for a new width must not replace. A Delete
// confirmation is a question the user is about to answer; a store passphrase is
// text they have typed, and focus may have left the field (dragging the window or
// the sidebar does that), so storeUnlockFocused cannot see it. Read from the DOM,
// so there is no flag to keep in step. This is not credRepaintHeld: that one is
// released by the body's focusout, and the click on the confirmation's own Cancel
// or Delete would fire it first and repaint the button away before it was clicked.
function credWidthHold(host) {
  if (host.querySelector(".confirm")) return true;
  return Array.from(host.querySelectorAll(".storepass")).some((i) => !!i.value);
}

// credRepaintForWidth: repaint from the cached view when the body is in a different
// mode (tables or cards) than its last paint, unless something is held. A held
// repaint is asked for again where the hold ends (credDismissConfirm, the body's
// focusout), and a later resize asks too. A table that is on screen is measured
// first: a window dragged narrower than its content needs, but not below the floor,
// leaves the rule saying "tables" while the section clips (measured: opened at 1440
// and dragged to 1225, the AWS table stuck out 23px and Delete 6px). Raising the
// need makes the mode differ from the paint, so the cards are drawn below.
function credRepaintForWidth() {
  const host = document.getElementById("creds-body");
  // A hidden tab reads 0 wide, which credTablesAt takes for "tables": it must not
  // flip. The observer entry is checked for this; the frame comes later, and the tab
  // may have been hidden in between.
  if (!host || !credView || !(host.clientWidth > 0)) return;
  // Not while a repaint is held (the unlock field has focus): if the hold is for new
  // data, the table on screen is about to be replaced and credNeedW was forgotten for
  // the new view, so measuring the old table would hand its width to data it does not
  // describe. Only the measure waits: the mode check below still runs, and releasing
  // the hold paints and measures anyway.
  const grew = credPaintedTables && !credRepaintHeld && credGrowNeed(host);
  if (!grew && credTableMode() === credPaintedTables) return;
  if (credWidthHold(host)) return;
  renderCredentials(credView);
}

// credWatchWidth: once, repaint from the cached view when the body crosses the
// table/card width while the page stays open. Tables vs cards is decided per paint
// (credTableMode), so a page opened wide and then narrowed kept its tables and
// pushed Edit and Delete past the window. Watching the body, not the window,
// because the sidebar is dragged too. Nothing happens per pixel: only a change of
// mode repaints, and never a refetch; a table on screen is measured on the frame
// (credRepaintForWidth), which reads sizes and changes nothing unless it overflows.
// renderCredentials already declines while the Add / Edit view is open and while the
// unlock field has focus, and the next paint (closing the view, leaving the field)
// draws the right mode. credRepaintForWidth also waits while a Delete confirmation
// is open or a passphrase is typed (#123). A hidden tab reports 0 wide: skipped, it
// must not flip anything.
//
// The repaint runs on the next frame, not inside the observer callback: a repaint
// changes #creds-body's height, which the observer is also watching, and that
// raises "ResizeObserver loop completed with undelivered notifications" on the
// window (measured, Chromium). Frames coalesce: a drag asks once per frame at most.
function credWatchWidth(host) {
  credWatchFonts();
  if (credWidthWatched || typeof ResizeObserver !== "function") return;
  credWidthWatched = true;
  let queued = false;
  const later = typeof requestAnimationFrame === "function" ? requestAnimationFrame : (f) => setTimeout(f, 0);
  new ResizeObserver((entries) => {
    const e = entries[entries.length - 1];
    if (!e || !(e.contentRect.width > 0) || queued) return;
    queued = true;
    later(() => {
      queued = false;
      credRepaintForWidth();
    });
  }).observe(host);
}

// credWatchFonts: once, look at a table again when the web font has loaded. The font
// is font-display: swap (desktop.css), so a table first drawn in the fallback font can
// be widened by Inter afterwards, which no resize reports and which does not change
// the body's height either. credRepaintForWidth measures a table it keeps, and
// changes nothing if it still fits. A browser without document.fonts has nothing to
// wait for.
function credWatchFonts() {
  if (credFontsWatched) return;
  const ready = typeof document !== "undefined" && document.fonts ? document.fonts.ready : null;
  if (!ready || typeof ready.then !== "function") return;
  credFontsWatched = true;
  ready.then(() => credRepaintForWidth());
}

function setCredErr(msg) {
  const el = document.getElementById("creds-err");
  if (el) el.textContent = msg || "";
}

async function loadCredentials() {
  setCredErr("");
  // Re-selecting the tab hides the Add / Edit footer; the view is still there. A flow in a host
  // (the drawer) has its footer in the host, which the tab never hid.
  if (awsFlow && !awsFlow.opts) awsShowFoot(true);
  if (credForm && !credFormOpts) credFormFoot(true);
  // A muted "Loading credentials…" above whatever is already shown, only if the
  // two calls below outlast the pending delay. done() runs on every exit,
  // including the error return, so no line is left behind.
  const done = pendingRegion(document.getElementById("creds-body"), "Loading credentials…");
  let view;
  try {
    // The store strip also reads GET /api/store (plaintext copies, value count).
    if (typeof refreshStore === "function") await refreshStore();
    try {
      view = await api("/api/credentials");
    } catch (e) {
      setCredErr("Could not load credentials: " + e.message);
      return;
    }
  } finally {
    done();
  }
  renderCredentials(view);
  // Every mutation ends in a reload, so this is the one place the profile form's
  // pickers learn that an entry was added, changed or deleted.
  if (typeof refreshCredLists === "function") refreshCredLists(view);
}

// --- Add / edit AWS connections (browser only) ---------------------------------
//
// One view in place of the list, with its own footer (#foot-creds-add):
//   add:  host profiles -> a row per ticked profile -> Find roles / Check -> Save all
//   edit: the same row for one existing web connection.
// Nothing here ever holds a secret: a profile name and a role ARN are not one,
// and the agent never gets the profile (the box mints 15-minute credentials).

const AWS_ROLE_RE = /^arn:aws[a-z-]*:iam::[0-9]{12}:role\/.+$/;
const AWS_SSO_POLL_MS = 2000;
const AWS_SSO_POLL_MAX = 300000;
let awsRowSeq = 0;

function awsEl(tag, cls, text) { return credEl(tag, cls, text); }

// awsSetOptions: `dl` (a <datalist>) holds exactly one <option> per value.
function awsSetOptions(dl, values) {
  dl.replaceChildren(...values.map((v) => { const o = document.createElement("option"); o.value = v; return o; }));
}

// awsFillSuggest: put the flow's current suggestions into every row's lists (a row built before the host's
// profiles were read starts with the built-in regions only).
function awsFillSuggest(f) {
  for (const row of f.rows) {
    if (row.lists && row.lists.source_profile) awsSetOptions(row.lists.source_profile, awsProfileSuggest(f.profiles));
    if (row.lists && row.lists.region) awsSetOptions(row.lists.region, awsRegionSuggest(f.profiles));
  }
}

function awsBtn(label, cls, onclick) {
  const b = awsEl("button", "btn" + (cls ? " " + cls : ""), label);
  b.type = "button";
  b.onclick = awsUnlessOff(b, onclick);
  return b;
}

// awsUnlessOff: `fn` as `btn`'s click, run only while the button is not aria-disabled. On desktop a button
// that cannot act yet stays aria-disabled instead of disabled (setDisabledReason), so its title shows the
// reason and Tab reaches it; Enter and Space on a button are a click, so this is the one place all three
// (click, Enter, Space) are stopped. A real `disabled` button never gets here.
function awsUnlessOff(btn, fn) {
  return (...args) => (btn.getAttribute("aria-disabled") === "true" ? undefined : fn(...args));
}

function awsShowFoot(on) {
  const add = document.getElementById("foot-creds-add");
  const base = document.getElementById("foot-creds");
  if (add) add.hidden = !on;
  if (base) base.hidden = on;
  credAddMenuSync();
}

// awsOpenAdd(opts): the add view in the Credentials tab, or, given opts = { host, foot, onCreated(names,
// kind), onClose(), saveLabel }, in a host of the caller's: the view draws into opts.host and its buttons into
// opts.foot (the primary one reading opts.saveLabel when there is one), leaves the tab's body, footers and
// list alone, and tells opts.onCreated every connection it saved (kind "aws") before opts.onClose. See
// credFlowDone.
function awsOpenAdd(opts) {
  credFlowStart(opts);
  awsFlow = { mode: "add", rows: [], profiles: [], selected: new Set(), sso: {}, timers: new Set(), saving: false, msg: "", loadErr: "", opts: opts || null, created: [] };
  awsPaintFlow();
  // The view is drawn at once; its profile list, once the host's profiles are read. The caller may wait for that (the head menu
  // moves focus to the first tick box); the drawer's starter ignores it.
  return awsLoadProfiles(awsFlow);
}

// awsOpenEdit: the same row for one existing web connection. The read model
// carries every field a save replaces (egress_allow, s3_read, elevate included),
// so an edit never drops one it did not show.
function awsOpenEdit(entry) {
  const el = entry.elevate || null;
  const row = awsNewRow({
    name: entry.name, source_profile: entry.source_profile || "", role_arn: entry.role_arn || "", region: entry.region || "",
    label: entry.label || "", prod: !!entry.prod, needsRole: false,
  }, { kind: "edit", accountId: entry.account_id || "" });
  row.description = entry.description || "";
  row.egress = (entry.egress_allow || []).join("\n");
  row.s3 = (entry.s3_read || []).join("\n");
  row.elevRole = el ? el.role_arn : "";
  row.elevMinutes = el && el.max_minutes === 30 ? "30" : "60";
  row.elevIAM = !!(el && el.allow_iam);
  row.elevBoxPrefix = (el && el.box_bucket_prefix) || "";
  row.elevPass = (el && el.pass_role_prefix) || "";
  row.mode = entry.mode === "sso_direct" ? "sso_direct" : "";
  row.orig = { source_profile: row.source_profile, role_arn: row.role_arn, mode: row.mode, full_access: entry.full_access === true };
  awsFlow = { mode: "edit", rows: [row], profiles: [], selected: new Set(), sso: {}, timers: new Set(), saving: false, msg: "", loadErr: "" };
  awsPaintFlow();
  // The form is drawn at once; its suggestion lists fill when the host's profiles are read. A failure leaves
  // them empty (awsPaintProfiles has no list to show it in on an edit): the fields stay plain free text.
  return awsLoadProfiles(awsFlow);
}

function awsNewRow(base, extra) {
  return {
    ...base, ...extra, id: ++awsRowSeq, description: "", egress: "", s3: "", elevRole: "", elevMinutes: "60", elevIAM: false, elevBoxPrefix: "", elevPass: "",
    assume: null, verdict: null, actions: [], reason: "", reasonText: "", acked: false,
    mode: "", gate: null, hours: 0, fullAck: false, ackStale: false,
    busy: "", err: "", roles: null, rolesNote: "", rolesDoc: false, rolesFilter: "", rolesFilterIn: null, signIn: "", el: null, res: null, rolesBox: null, roleInput: null,
  };
}

// awsRowState: rowSaveState with the edit rule filled in (an edit that leaves
// the role and its signing profile alone needs no check).
function awsRowState(row) {
  return awsRowStateFor(row);
}

async function awsExit() {
  const f = awsFlow;
  // In a host the way out waits for a save in flight, so the connection it is saving is reported
  // (awsSaveAll leaves itself once it has answered).
  if (f && f.opts && f.saving) return;
  if (f && f.opts) {
    credFlowDrop();
    credFlowDone(f.opts, f.created, "aws");
    return;
  }
  if (f) for (const t of f.timers) clearInterval(t);
  awsFlow = null;
  awsShowFoot(false);
  await loadCredentials();
}

function awsPaintFlow() {
  const f = awsFlow;
  const host = f && f.opts ? f.opts.host : document.getElementById("creds-body");
  if (!host || !f) return;
  const edit = f.mode === "edit";
  const crumb = awsEl("div", "crumb");
  const back = awsEl("a", "lnk", "Credentials");
  back.tabIndex = 0;
  back.setAttribute("role", "button");
  // In a host the crumb ends this flow only: once it was dropped or replaced it does nothing, and never
  // takes the tab's way out (a reload of the list).
  const leave = f.opts ? () => (awsFlow === f ? awsExit() : undefined) : () => awsExit();
  back.onclick = leave;
  back.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); leave(); } };
  // A framed host (opts.head, the drawer) has a header of its own: the crumb reads "Credentials › AWS" over the title.
  const framed = !!(f.opts && f.opts.head);
  crumb.append(back, document.createTextNode(framed ? " › AWS" : " / "), awsEl("b", null, edit ? "Edit " + f.rows[0].name : "Add AWS accounts"));
  const nodes = [crumb, awsEl("p", "intro", "The agent never gets these profiles. It gets 15-minute credentials for the role you choose below.")];
  if (!edit) {
    const sec = awsEl("section", "sec");
    sec.setAttribute("aria-label", "Host profiles");
    f.profHead = awsEl("span", "grow");
    const h = awsEl("div", "sec-h");
    h.appendChild(f.profHead);
    f.profHost = awsEl("div", "stack");
    sec.append(h, f.profHost);
    nodes.push(sec);
  }
  const rsec = awsEl("section", "sec");
  rsec.setAttribute("aria-label", "Selected accounts");
  f.rowsHead = awsEl("span", "grow");
  const rh = awsEl("div", "sec-h");
  rh.appendChild(f.rowsHead);
  f.rowsHost = awsEl("div", "stack");
  f.rowsEmpty = awsEl("p", "hint", "Tick a profile above to set up a connection for it.");
  if (framed) {
    // The mockup's line under the head: what ticking several does, and where each connection goes.
    f.rowsTick = awsEl("p", "hint", "Tick several to create several. Each one you save joins this profile as an unsaved row.");
    rsec.append(rh, f.rowsTick, f.rowsHost, f.rowsEmpty);
  } else {
    rsec.append(rh, f.rowsHost, f.rowsEmpty);
  }
  nodes.push(rsec);
  f.msgBox = awsEl("div", "hint");
  f.msgBox.setAttribute("role", "status");
  // The footer's own note is hidden on a phone (the footer is one row of buttons), so the same words sit here.
  f.noteBox = awsEl("div", "hint awsnote");
  nodes.push(f.msgBox, f.noteBox);
  if (framed) {
    f.opts.head.replaceChildren(nodes[0], nodes[1]);
    host.replaceChildren(...nodes.slice(2));
  } else {
    host.replaceChildren(...nodes);
  }
  for (const r of f.rows) f.rowsHost.appendChild(awsBuildRow(r));
  awsPaintProfiles();
  if (f.opts) awsBuildFoot(f);
  awsPaintFoot();
  if (!f.opts) awsShowFoot(true);
}

async function awsLoadProfiles(f) {
  try {
    const res = await api("/api/credentials/aws/host-profiles");
    if (awsFlow !== f) return;
    f.profiles = res.profiles || [];
  } catch (e) {
    if (awsFlow !== f) return;
    f.loadErr = "Could not read the host's AWS profiles: " + awsErrText(e);
  }
  f.loaded = true;
  awsPaintProfiles();
  awsFillSuggest(f);
}

function awsProfileDesc(p) {
  if (p.kind === "sso") {
    return "account " + (p.sso_account_id || "?") + (p.sso_role_name ? ", permission set " + p.sso_role_name : "");
  }
  if (p.kind === "assume-role") {
    return "role_arn " + (p.role_arn || "?") + ", source_profile " + (p.source_profile || "?");
  }
  return "signs in without a role of its own (key, credential process or web identity)";
}

function awsPaintProfiles() {
  const f = awsFlow;
  if (!f || !f.profHost) return;
  f.profHead.replaceChildren(document.createTextNode("Profiles on this host "), awsEl("span", "count", String(f.profiles.length)));
  const nodes = [];
  if (f.loadErr) nodes.push(awsEl("p", "ent-err", f.loadErr));
  else if (!f.loaded) nodes.push(awsEl("p", "hint", "Reading the host's AWS profiles…"));
  else if (!f.profiles.length) nodes.push(awsEl("p", "hint", "No profiles found in this host's AWS config file. Add one with aws configure sso, then come back."));
  for (const p of f.profiles) {
    const lab = awsEl("label", "hp");
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = f.selected.has(p.name);
    cb.disabled = !p.selectable;
    cb.onchange = () => awsToggleProfile(p, cb.checked);
    const tx = awsEl("span", "tx");
    const nm = awsEl("span", "nm" + (p.selectable ? "" : " dim"), p.name + " ");
    nm.appendChild(awsEl("span", "bdg kind", p.kind));
    tx.append(nm, awsEl("span", "d" + (p.selectable ? "" : " dim"), awsProfileDesc(p)));
    if (!p.selectable && p.note) tx.appendChild(awsEl("span", "stop", p.note.charAt(0).toUpperCase() + p.note.slice(1) + "."));
    lab.append(cb, tx);
    nodes.push(lab);
  }
  f.profHost.replaceChildren(...nodes);
  awsPaintRowsHead();
}

function awsPaintRowsHead() {
  const f = awsFlow;
  if (!f) return;
  f.rowsHead.replaceChildren(document.createTextNode((f.mode === "edit" ? "Connection " : "Connections to create ")),
    awsEl("span", "count", String(f.rows.length)));
  f.rowsEmpty.hidden = f.rows.length > 0 || f.mode === "edit";
  if (f.rowsTick) f.rowsTick.hidden = f.rows.length === 0;
}

function awsToggleProfile(p, on) {
  const f = awsFlow;
  if (!f) return;
  if (on) {
    f.selected.add(p.name);
    const row = awsNewRow(awsRowFromProfile(p), { kind: p.kind, accountId: p.sso_account_id || "", pname: p.name });
    f.rows.push(row);
    f.rowsHost.appendChild(awsBuildRow(row));
  } else {
    f.selected.delete(p.name);
    const i = f.rows.findIndex((r) => r.pname === p.name);
    if (i >= 0) { f.rows[i].el.remove(); f.rows.splice(i, 1); }
  }
  awsPaintRowsHead();
  awsPaintFoot();
}

// awsField: a labelled input whose value is written back to row[key] as it changes.
function awsField(row, key, label, opts) {
  const o = opts || {};
  const col = awsEl("div", "fcol" + (o.full ? " full" : ""));
  const id = "aws-r" + row.id + "-" + key;
  const lab = awsEl("label", "flab", label);
  lab.htmlFor = id;
  const inp = document.createElement(o.area ? "textarea" : "input");
  inp.id = id;
  inp.className = "fld";
  if (o.area) inp.rows = 2; else inp.type = "text";
  inp.value = row[key] || "";
  inp.autocomplete = "off";
  inp.spellcheck = false;
  if (o.placeholder) inp.placeholder = o.placeholder;
  if (o.readonly) { inp.readOnly = true; inp.classList.add("ro"); }
  // A suggestion list: a native <datalist>, so the input stays free text. The list is filled by the caller
  // (awsSetOptions) now or later, when what it suggests has been read; row.lists[key] is where to find it.
  let dl = null;
  if (o.suggest) {
    dl = document.createElement("datalist");
    dl.id = id + "-list";
    awsSetOptions(dl, o.suggest);
    inp.setAttribute("list", dl.id);
    (row.lists = row.lists || {})[key] = dl;
  }
  inp.oninput = () => {
    row[key] = inp.value;
    if (o.invalidates) awsInvalidate(row);
    // The drawer's check line says "base role only" while the row has an elevate role: it follows what is typed.
    if (o.repaints && awsFlow && awsFlow.opts && awsFlow.opts.head) awsPaintRes(row);
    awsPaintFoot();
  };
  col.append(lab, inp);
  if (dl) col.appendChild(dl);
  if (o.hint) col.appendChild(awsEl("p", "hint", o.hint));
  return { col, inp, dl };
}

// awsInvalidate: the role, its signing profile or the region changed, so the
// earlier check no longer describes this row.
function awsInvalidate(row) {
  row.assume = null; row.verdict = null; row.actions = []; row.reason = ""; row.reasonText = ""; row.acked = false;
  row.gate = null; row.hours = 0; row.fullAck = false; row.ackStale = false;
  row.err = ""; row.signIn = "";
  awsPaintModeNote(row);
  awsPaintRes(row);
}

function awsBuildRow(row) {
  const edit = !!row.orig;
  const art = awsEl("div", "arow");
  row.el = art;
  const head = awsEl("div", "arow-h");
  head.appendChild(awsEl("span", "nm", row.pname || row.name));
  head.appendChild(awsEl("span", "bdg kind", row.kind === "edit" ? "web" : row.kind));
  row.prodBadge = awsEl("span", "bdg prod", "PROD");
  row.prodBadge.hidden = !row.prod;
  head.appendChild(row.prodBadge);
  row.checkBtn = awsBtn("Check", "", () => awsCheckRow(row));
  head.appendChild(row.checkBtn);
  art.appendChild(head);

  const grid = awsEl("div", "frow");
  const name = awsField(row, "name", "Connection name", { readonly: edit });
  const label = awsField(row, "label", "Label");
  grid.append(name.col, label.col);

  // Role: a text field (a bare name is expanded with the SSO account), Find roles when the profile has no role of its own.
  // Mode: the agent role (AssumeRole) or the operator's own SSO role behind the read-only gate.
  // SSO direct needs an SSO profile, so a row from any other profile shows no choice.
  if (edit || row.kind === "sso") grid.appendChild(awsModeChoice(row));
  const rcol = awsEl("div", "fcol full");
  row.roleCol = rcol;
  const rid = "aws-r" + row.id + "-role_arn";
  const rlab = awsEl("label", "flab", "Role ARN");
  rlab.htmlFor = rid;
  const line = awsEl("div", "roleline");
  const rin = document.createElement("input");
  rin.id = rid; rin.type = "text"; rin.className = "fld"; rin.value = row.role_arn; rin.autocomplete = "off"; rin.spellcheck = false;
  rin.placeholder = "arn:aws:iam::<account>:role/<name>";
  rin.oninput = () => { row.role_arn = rin.value; awsInvalidate(row); awsPaintRoles(row); awsPaintFoot(); };
  rin.onchange = () => {
    const full = awsRoleARN(row.accountId, rin.value);
    if (full !== rin.value) { rin.value = full; row.role_arn = full; awsInvalidate(row); awsPaintRoles(row); awsPaintFoot(); }
  };
  row.roleInput = rin;
  line.appendChild(rin);
  if (row.needsRole) {
    row.rolesBtn = awsBtn("Find roles", "", () => awsFindRoles(row));
    line.appendChild(row.rolesBtn);
  }
  row.rolesBox = awsEl("div", "rolesbox");
  rcol.append(rlab, line, row.rolesBox);
  grid.appendChild(rcol);
  row.modeNote = awsEl("div", "amberbox modenote");
  row.modeNote.setAttribute("role", "note");
  const nfull = awsEl("div", "fcol full");
  nfull.appendChild(row.modeNote);
  grid.appendChild(nfull);
  row.modeNoteCol = nfull;

  const f = awsFlow;
  // Add mode takes the source profile from the ticked host profile and keeps it read-only, so only an edit offers a list.
  grid.appendChild(awsField(row, "source_profile", "Source profile", { readonly: !edit, invalidates: true,
    suggest: edit ? awsProfileSuggest(f && f.profiles) : null,
    hint: "A profile in the host's AWS config file (usually ~/.aws/config) that CuliBox signs in with." }).col);
  grid.appendChild(awsField(row, "region", "Region", { invalidates: true, placeholder: "ap-southeast-1", suggest: awsRegionSuggest(f && f.profiles) }).col);
  if (edit) grid.appendChild(awsField(row, "description", "Description", { full: true }).col);

  const pcol = awsEl("div", "fcol full");
  const plab = awsEl("label", "pf-check");
  const pcb = document.createElement("input");
  pcb.type = "checkbox";
  pcb.checked = !!row.prod;
  pcb.onchange = () => { row.prod = pcb.checked; row.prodBadge.hidden = !row.prod; };
  plab.append(pcb, document.createTextNode(row.prod && !edit && /prod/i.test(row.name) ? " production (ticked because the name contains prod)" : " production"));
  pcol.appendChild(plab);
  grid.appendChild(pcol);
  art.appendChild(grid);

  row.res = awsEl("div", "res");
  art.appendChild(row.res);

  // Advanced (collapsed): egress hosts, S3 read, elevate. Same validation as the server's.
  const det = document.createElement("details");
  row.advSummary = awsEl("summary", null, "Advanced: egress hosts, S3 read, elevate");
  det.appendChild(row.advSummary);
  det.open = !!(row.egress || row.s3 || row.elevRole);
  const adv = awsEl("div", "frow");
  adv.appendChild(awsField(row, "egress", "Extra hosts the agent may reach", { area: true, full: true, placeholder: "one per line, e.g. *.example.com",
    hint: "Added to the allowed hosts of a box that uses this connection, on top of the defaults (AWS, Anthropic, GitHub, package registries and release hosts)." }).col);
  const s3f = awsField(row, "s3", "S3 buckets the agent may read", { area: true, full: true, placeholder: "one per line",
    hint: "The agent may read objects only in these buckets or prefixes (bucket, or bucket/prefix/). Other S3 object reads are denied, except in the box buckets an elevate window may use (set in the config file). Empty means no bucket of your own." });
  adv.appendChild(s3f.col);
  const elf = awsField(row, "elevRole", "Elevate role ARN (optional)", { full: true, repaints: true, placeholder: "arn:aws:iam::<account>:role/<write role>" });
  adv.appendChild(elf.col);
  const mcol = awsEl("div", "fcol");
  const mlab = awsEl("label", "flab", "Elevate window");
  mlab.htmlFor = "aws-r" + row.id + "-mins";
  const sel = document.createElement("select");
  sel.id = "aws-r" + row.id + "-mins";
  sel.className = "fld";
  for (const m of ["30", "60"]) { const o = document.createElement("option"); o.value = m; o.textContent = m + " minutes"; sel.appendChild(o); }
  sel.value = row.elevMinutes;
  sel.onchange = () => { row.elevMinutes = sel.value; };
  mcol.append(mlab, sel);
  adv.appendChild(mcol);
  const icol = awsEl("div", "fcol");
  const ilab = awsEl("label", "pf-check");
  const icb = document.createElement("input");
  icb.type = "checkbox";
  icb.checked = row.elevIAM;
  ilab.append(icb, document.createTextNode(" Elevate may change IAM"));
  icol.appendChild(ilab);
  const iwarn = awsEl("p", "hint", "With IAM changes allowed, the agent can create and use its own roles, so data-read blocks are advisory in this window.");
  iwarn.hidden = !row.elevIAM;
  icb.onchange = () => { row.elevIAM = icb.checked; iwarn.hidden = !icb.checked; };
  icol.appendChild(iwarn);
  adv.appendChild(icol);
  const pf = awsField(row, "elevPass", "Elevate may pass roles named (prefix, optional)", { full: true, placeholder: "e.g. culibox-",
    hint: "Empty: the elevated session cannot pass any IAM role, so Lambda and ECS applies fail. A prefix lets it pass only roles named <prefix>*." });
  adv.appendChild(pf.col);
  let boxHint = null;
  if (row.elevBoxPrefix) {
    boxHint = awsEl("p", "hint full", "This elevate can read objects in, and change the policies, ACLs and public access blocks of, buckets named " + row.elevBoxPrefix + "* (elevate.box_bucket_prefix, set in the config file).");
    adv.appendChild(boxHint);
  }
  // What sso_direct hides and clears: S3 read, elevate and its window and IAM flag.
  row.modeHidden = [s3f.col, elf.col, mcol, icol, pf.col].concat(boxHint ? [boxHint] : []);
  row.modeClear = () => {
    row.s3 = ""; s3f.inp.value = "";
    row.elevRole = ""; elf.inp.value = "";
    row.elevMinutes = "60"; sel.value = "60";
    row.elevIAM = false; icb.checked = false; iwarn.hidden = true;
    row.elevPass = ""; pf.inp.value = "";
  };
  det.appendChild(adv);
  art.appendChild(det);

  awsApplyMode(row);
  awsPaintRoles(row);
  awsPaintRes(row);
  return art;
}

// awsModeChoice: the two-way choice beside the role field.
function awsModeChoice(row) {
  const col = awsEl("div", "fcol full");
  col.appendChild(awsEl("span", "flab", "Credentials the agent gets"));
  const grp = awsEl("div", "modechoice");
  grp.setAttribute("role", "radiogroup");
  grp.setAttribute("aria-label", "Credentials the agent gets");
  for (const [value, label] of [["", "Agent role (recommended)"], ["sso_direct", "SSO role directly"]]) {
    const lab = awsEl("label", "pf-check");
    const rb = document.createElement("input");
    rb.type = "radio";
    rb.name = "aws-r" + row.id + "-mode";
    rb.value = value;
    rb.checked = (row.mode || "") === value;
    rb.onchange = () => { if (rb.checked) awsSetMode(row, value); };
    lab.append(rb, document.createTextNode(" " + label));
    grp.appendChild(lab);
  }
  col.appendChild(grp);
  return col;
}

// awsSetMode: switch a row's mode. Going to SSO direct clears the role, S3 read and
// elevate values (the server refuses them there, and a stale value must not come
// back on the way out); going back starts the role from the account prefix again.
function awsSetMode(row, mode) {
  if ((row.mode || "") === mode) return;
  row.mode = mode;
  if (mode === "sso_direct") {
    row.role_arn = "";
    if (row.modeClear) row.modeClear();
  } else {
    row.role_arn = row.accountId ? "arn:aws:iam::" + row.accountId + ":role/" : "";
  }
  if (row.roleInput) row.roleInput.value = row.role_arn;
  awsInvalidate(row);
  awsApplyMode(row);
  awsPaintRoles(row);
  awsPaintFoot();
}

// awsApplyMode: show or hide what the row's mode does not use.
function awsApplyMode(row) {
  const direct = row.mode === "sso_direct";
  if (row.roleCol) row.roleCol.hidden = direct;
  if (row.modeNoteCol) row.modeNoteCol.hidden = !direct;
  for (const c of row.modeHidden || []) c.hidden = direct;
  if (row.advSummary) row.advSummary.textContent = direct ? "Advanced: egress hosts" : "Advanced: egress hosts, S3 read, elevate";
  if (row.checkBtn) row.checkBtn.textContent = "Check";
  awsPaintModeNote(row);
}

// awsPaintModeNote: the trade-off, always visible in SSO direct mode. N comes from
// the last check's credentials when there is one.
function awsPaintModeNote(row) {
  if (!row.modeNote) return;
  row.modeNote.textContent = awsModeText(row.hours);
}

// AWS_ROLES_SHOWN caps the radios one listing renders; the filter finds the rest.
const AWS_ROLES_SHOWN = 50;

// awsPaintRoles: the filter box, the matching roles as radios, then the note.
// The filter is a fixed child of rolesBox and only the list beside it is
// repainted: detaching a focused input blurs it, so typing would stop.
function awsPaintRoles(row) {
  if (!row.rolesBox) return;
  if (!row.rolesListBox) row.rolesListBox = awsEl("div", null);
  const nodes = [];
  const fixed = [];
  if (row.roles && row.roles.length > 8) {
    if (!row.rolesFilterIn) {
      const fin = document.createElement("input");
      fin.type = "search"; fin.className = "fld rolefilter"; fin.autocomplete = "off"; fin.spellcheck = false;
      fin.setAttribute("aria-label", "Filter roles by name");
      fin.oninput = () => { row.rolesFilter = fin.value; awsPaintRoles(row); };
      row.rolesFilterIn = fin;
    }
    row.rolesFilterIn.placeholder = "Filter " + row.roles.length + " roles by name";
    if (row.rolesFilterIn.value !== (row.rolesFilter || "")) row.rolesFilterIn.value = row.rolesFilter || "";
    fixed.push(row.rolesFilterIn);
  }
  fixed.push(row.rolesListBox);
  const cur = Array.from(row.rolesBox.children);
  if (cur.length !== fixed.length || cur.some((c, i) => c !== fixed[i])) row.rolesBox.replaceChildren(...fixed);
  if (row.roles && row.roles.length) {
    const shown = awsRolesShown(row.roles, row.rolesFilter, AWS_ROLES_SHOWN, row.role_arn);
    const grp = awsEl("div", "roles");
    grp.setAttribute("role", "radiogroup");
    grp.setAttribute("aria-label", "Roles found for " + row.source_profile);
    for (const r of shown.list) {
      const lab = awsEl("label", row.role_arn === r.arn ? "on" : "");
      const rb = document.createElement("input");
      rb.type = "radio";
      rb.name = "aws-roles-" + row.id;
      rb.checked = row.role_arn === r.arn;
      rb.onchange = () => {
        row.role_arn = r.arn;
        row.roleInput.value = r.arn;
        awsInvalidate(row);
        awsPaintRoles(row);
        awsPaintFoot();
      };
      lab.append(rb, document.createTextNode(" " + r.name));
      if (awsIsAgentRole(r)) lab.appendChild(awsEl("span", "bdg rolebdg", "agent role"));
      else if (r.sso_trust) lab.appendChild(awsEl("span", "bdg rolebdg", "trusts your account"));
      grp.appendChild(lab);
    }
    if (!shown.matched) grp.appendChild(awsEl("p", "hint", "No role name matches."));
    nodes.push(grp);
    if (shown.matched > AWS_ROLES_SHOWN) {
      nodes.push(awsEl("p", "hint", "Showing " + AWS_ROLES_SHOWN + " of " + shown.matched + " matching roles; type to narrow."));
    }
  }
  if (row.rolesNote) {
    const p = awsEl("p", "hint", row.rolesNote);
    if (row.rolesDoc) {
      const a = awsEl("a", "lnk", "How to create the agent role");
      a.href = AWS_AGENT_ROLE_DOC; a.target = "_blank"; a.rel = "noopener noreferrer";
      p.append(" ", a);
    }
    nodes.push(p);
  }
  row.rolesListBox.replaceChildren(...nodes);
}

function awsLine(cls, glyph, text) {
  const l = awsEl("div", "l" + (cls ? " " + cls : ""));
  l.appendChild(awsEl("span", null, glyph));
  l.lastChild.setAttribute("aria-hidden", "true");
  l.appendChild(awsEl("span", null, text));
  return l;
}

function awsPaintRes(row) {
  if (!row.res) return;
  const nodes = [];
  if (row.busy) {
    nodes.push(awsEl("div", "l", row.busy === "roles" ? "Looking up roles…" : row.busy === "save" ? "Saving…" : "Checking…"));
  }
  if (row.err) {
    const e = awsEl("div", "ent-err", row.err);
    e.setAttribute("role", "alert");
    nodes.push(e);
  }
  let needSignIn = row.signIn !== "";
  if (row.mode === "sso_direct") {
    if (row.gate) {
      const g = awsGateView(row.gate);
      if (!awsGateBoxEmpty(g)) nodes.push(awsGateNode(g));
      if (g.ackEligible) nodes.push(awsAckNode(row));
      if (g.signIn) needSignIn = true;
    }
  } else if (row.assume) {
    const v = awsCheckView({ assume: row.assume, verdict: row.verdict, actions: row.actions, reason: row.reason, reason_text: row.reasonText });
    if (v.assume === "ok") {
      if (awsFlow && awsFlow.opts && awsFlow.opts.head && !v.needsAck && v.verdict.cls === "ok") {
        // The drawer's one line, as the mockup draws it: "Assume worked. No broad write access found". A row with an elevate role
        // adds what the tab's verdict line always says: the check read the base role only, not the elevate one.
        const line = awsLine("ok", "✓", v.text + " " + v.verdict.text);
        if (row.elevRole) line.lastChild.appendChild(awsEl("span", "when", " base role only"));
        nodes.push(line);
      } else {
        nodes.push(awsLine("ok", "✓", v.text));
        nodes.push(awsVerdictNode(row, v));
      }
    } else if (v.signIn) {
      needSignIn = true; // awsSignInNode carries the badge, beside its Sign in button
    } else {
      const l = awsEl("div", "l");
      l.appendChild(awsEl("span", "bdg " + (v.tone === "bad" ? "prod" : "warnb"), v.badge));
      l.appendChild(awsEl("span", null, v.text));
      nodes.push(l);
    }
  }
  if (needSignIn) nodes.push(awsSignInNode(row));
  row.res.replaceChildren(...nodes);
  if (row.el) row.el.classList.toggle("danger", row.mode === "sso_direct" ? !!row.gate && row.gate.state === "not_read_only" : row.assume === "ok" && row.verdict === "writes");
}

// awsGateBoxEmpty: the sign-in prompt carries its own "SSO expired" badge, so a
// sign-in gate with no refused lines, missing grants or data-read report would
// draw an empty amber box above it.
function awsGateBoxEmpty(g) {
  return g.tone !== "ok" && g.signIn && !g.refused.length && !g.missing.length && !g.dataReadText;
}

// awsGateNode: the read-only gate's verdict, the lines that refused and the data-read report.
function awsGateNode(g) {
  if (g.tone === "ok") {
    const box = awsEl("div", "gate ok");
    box.appendChild(awsLine("ok", "✓", g.badge + ". " + g.text));
    if (g.dataReadText) box.appendChild(awsEl("p", "hint", g.dataReadText));
    return box;
  }
  const box = awsEl("div", "gate " + (g.tone === "bad" ? "bad warnbox" : "warn amberbox"));
  box.setAttribute("role", "alert");
  if (!g.signIn) {
    const l = awsEl("div", "l");
    l.appendChild(awsEl("span", "bdg " + (g.tone === "bad" ? "prod" : "warnb"), g.badge));
    l.appendChild(awsEl("span", null, g.text));
    box.appendChild(l);
  }
  if (g.refused.length) {
    const ul = awsEl("ul", "gatelist");
    for (const r of g.refused) ul.appendChild(awsEl("li", null, r));
    box.appendChild(ul);
  }
  if (g.missing.length) box.appendChild(awsEl("div", "remedy", "Grant: " + g.missing.join(", ")));
  if (g.dataReadText) box.appendChild(awsEl("p", "hint", g.dataReadText));
  return box;
}

// awsAckNode: the full-access acknowledgement. Unticked by default, never pre-ticked; the text covers future grants
// because a result the gate could not read cannot pin what the permission set allows.
const AWS_ACK_TEXT = "I understand: the agent gets everything this permission set allows, now or later, for the whole session, with no read-only default and no write window. CuliBox lists what it found above.";
const AWS_ACK_ALREADY = "Already acknowledged; tick again only to acknowledge a change.";
function awsAckNode(row) {
  const box = awsEl("div", "warnbox");
  const lab = awsEl("label", "pf-check");
  const cb = document.createElement("input");
  cb.type = "checkbox";
  cb.checked = row.fullAck === true;
  cb.onchange = () => { row.fullAck = cb.checked; awsPaintFoot(); };
  lab.append(cb, document.createTextNode(" " + AWS_ACK_TEXT));
  box.appendChild(lab);
  if (row.orig && row.orig.full_access === true && !row.ackStale) box.appendChild(awsEl("p", "hint", AWS_ACK_ALREADY));
  const p = awsEl("p", "hint", "The agent role is the recommended default. ");
  const a = awsEl("a", "lnk", "How to create the agent role");
  a.href = AWS_AGENT_ROLE_DOC; a.target = "_blank"; a.rel = "noopener noreferrer";
  p.appendChild(a);
  box.appendChild(p);
  return box;
}

function awsVerdictNode(row, v) {
  if (v.needsAck) {
    const box = awsEl("div", "warnbox", v.verdict.text);
    box.setAttribute("role", "alert");
    if (v.actions.length) box.appendChild(awsEl("div", "remedy", "Allowed here: " + v.actions.join(", ")));
    box.appendChild(awsEl("div", "remedy", CRED_D4_REMEDY));
    const lab = awsEl("label", "pf-check");
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = row.acked === true;
    cb.onchange = () => { row.acked = cb.checked; awsPaintFoot(); };
    lab.append(cb, document.createTextNode(" I understand"));
    box.appendChild(lab);
    return box;
  }
  if (v.verdict.cls === "ok") {
    const d = awsEl("div", "verdict ok");
    d.appendChild(awsEl("span", "g", "✓"));
    const t = awsEl("span");
    t.appendChild(awsEl("b", null, v.verdict.text));
    t.appendChild(awsEl("span", "when", " base role only"));
    d.appendChild(t);
    return d;
  }
  return awsEl("div", "amberbox", v.verdict.text);
}

function awsSignInNode(row) {
  const wrap = awsEl("div", "ssopanel");
  const sso = awsFlow && awsFlow.sso[row.source_profile];
  if (!sso || sso.state === "gone") {
    const l = awsEl("div", "l");
    l.appendChild(awsEl("span", "bdg warnb", "SSO expired"));
    l.appendChild(awsEl("span", null, "Sign in again on this host."));
    const b = awsBtn("Sign in", "", () => awsSSOStart(row));
    b.disabled = !!row.busy;
    l.appendChild(b);
    wrap.appendChild(l);
    if (sso && sso.msg) wrap.appendChild(awsEl("p", "hint", sso.msg));
    return wrap;
  }
  if (sso.state === "cmd") {
    wrap.appendChild(awsEl("p", "hint", "This page could not start the sign-in here. Run this on the host, then check again:"));
    wrap.appendChild(awsEl("code", "cmd", sso.command));
    return wrap;
  }
  const st = sso.st || {};
  if (st.state === "ok") {
    wrap.appendChild(awsLine("ok", "✓", "Signed in as " + row.source_profile + "."));
  } else if (st.state === "failed") {
    const l = awsEl("div", "l");
    l.appendChild(awsEl("span", "bdg prod", "Sign-in failed"));
    l.appendChild(awsBtn("Try again", "", () => awsSSOStart(row)));
    wrap.appendChild(l);
  } else {
    wrap.appendChild(awsEl("p", "hint", "Open this address in any browser, sign in, and enter the code:"));
    if (st.uri_trusted && /^https:\/\//.test(st.uri || "")) {
      const a = awsEl("a", "lnk", st.uri);
      a.href = st.uri;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      wrap.appendChild(a);
    } else if (st.uri) {
      wrap.appendChild(awsEl("code", "cmd", st.uri));
      wrap.appendChild(awsEl("p", "hint", "That address is not on AWS's usual sign-in domains. Check it before you sign in."));
    }
    if (st.code) wrap.appendChild(awsEl("div", "ssocode", st.code));
    wrap.appendChild(awsEl("p", "hint", "Waiting for you to finish…"));
  }
  return wrap;
}

async function awsSSOStart(row) {
  const f = awsFlow;
  const prof = row.source_profile;
  row.err = "";
  let st;
  try {
    st = await api("/api/credentials/aws/sso-login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ source_profile: prof }) });
  } catch (e) {
    if (awsFlow !== f) return;
    if (e.status === 502 && e.body && e.body.command) f.sso[prof] = { state: "cmd", command: String(e.body.command) };
    else row.err = awsErrText(e);
    awsRepaintProfileRows(prof);
    return;
  }
  if (awsFlow !== f) return;
  f.sso[prof] = { st };
  awsRepaintProfileRows(prof);
  awsSSOPoll(f, prof);
}

// awsRepaintProfileRows: every row signed by this profile shows the same sign-in panel.
function awsRepaintProfileRows(prof) {
  if (!awsFlow) return;
  for (const r of awsFlow.rows) if (r.source_profile === prof) awsPaintRes(r);
}

// awsSSOTimedOut: the panel state once polling gives up. "gone" is the state
// that shows the Sign in button again.
function awsSSOTimedOut() {
  return { state: "gone", msg: "Sign-in timed out. Press Sign in to start another." };
}

function awsSSOPoll(f, prof) {
  const started = Date.now();
  // A poll still in flight when polling stops (time-out, failure, done) must not
  // repaint "Waiting…" over the final state.
  let stopped = false;
  const stop = () => { stopped = true; clearInterval(timer); f.timers.delete(timer); };
  const timer = setInterval(async () => {
    if (stopped) return;
    if (awsFlow !== f) { stop(); return; }
    if (Date.now() - started > AWS_SSO_POLL_MAX) {
      // Back to the "Sign in" state with a note, never stuck on "Waiting…".
      stop();
      f.sso[prof] = awsSSOTimedOut();
      awsRepaintProfileRows(prof);
      return;
    }
    let st;
    try {
      st = await api("/api/credentials/aws/sso-login?profile=" + encodeURIComponent(prof));
    } catch (e) {
      if (awsFlow !== f || stopped) return;
      stop();
      f.sso[prof] = { state: "gone", msg: e.status === 404 ? "No sign-in is in progress any more. Press Sign in to start another." : awsErrText(e) };
      awsRepaintProfileRows(prof);
      return;
    }
    if (awsFlow !== f || stopped) return;
    f.sso[prof] = { st };
    awsRepaintProfileRows(prof);
    if (st.state === "ok" || st.state === "failed") {
      stop();
      if (st.state === "ok") awsAfterSignIn(f, prof);
    }
  }, AWS_SSO_POLL_MS);
  f.timers.add(timer);
}

// awsAfterSignIn: pick up what the expired session interrupted, once per row.
function awsAfterSignIn(f, prof) {
  for (const r of f.rows.filter((x) => x.source_profile === prof)) {
    if (r.signIn === "roles") { r.signIn = ""; awsFindRoles(r); }
    else if (r.assume === "sso_expired") awsCheckRow(r);
    else if (r.mode === "sso_direct" && r.gate && awsGateView(r.gate).signIn) awsCheckRow(r); // also a not_read_only run the expiry cut short
  }
}

async function awsPost(path, body) {
  return api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
}

async function awsFindRoles(row) {
  const f = awsFlow;
  if (row.busy) return;
  if (!row.source_profile) { row.err = "There is no profile to look up roles with."; awsPaintRes(row); return; }
  row.busy = "roles"; row.err = ""; row.signIn = ""; row.rolesNote = "";
  awsPaintRes(row);
  const body = { source_profile: row.source_profile };
  if (row.accountId) body.account_id = row.accountId;
  try {
    const res = await awsPost("/api/credentials/aws/roles", body);
    if (awsFlow !== f) return;
    row.roles = null;
    row.rolesFilter = "";
    if (res.sso_expired) row.signIn = "roles";
    const note = awsRolesNote(res);
    row.rolesNote = note.text;
    row.rolesDoc = note.doc;
    if (res.roles && res.roles.length && !res.sso_expired) {
      row.roles = res.roles;
      const pick = awsAgentRolePick(res.roles, row.role_arn);
      if (pick) {
        row.role_arn = pick;
        row.roleInput.value = pick;
        awsInvalidate(row);
      }
    }
  } catch (e) {
    if (awsFlow !== f) return;
    row.err = awsErrText(e);
  }
  row.busy = "";
  awsPaintRoles(row);
  awsPaintRes(row);
  awsPaintFoot();
}

// awsCheckRow: the broker's own base-role check, as a spawn would make it.
async function awsCheckRow(row) {
  const f = awsFlow;
  if (row.busy) return false;
  if (!row.source_profile) { row.err = "Choose a source profile first."; awsPaintRes(row); return false; }
  const direct = row.mode === "sso_direct";
  if (!direct && !AWS_ROLE_RE.test(row.role_arn)) {
    row.err = row.needsRole ? "Choose or type the role first: arn:aws:iam::<account>:role/<name>." : "The role ARN looks wrong: arn:aws:iam::<account>:role/<name>.";
    awsPaintRes(row);
    return false;
  }
  if (!row.region) { row.err = "Enter a region (for example ap-southeast-1)."; awsPaintRes(row); return false; }
  row.busy = "check"; row.err = ""; row.signIn = "";
  awsPaintRes(row);
  awsPaintFoot();
  let ok = true;
  try {
    if (direct) {
      const res = await awsPost("/api/credentials/aws/check", { mode: "sso_direct", source_profile: row.source_profile, region: row.region });
      if (awsFlow !== f) return false;
      row.gate = res.gate ? { ...res.gate, ack_eligible: res.ack_eligible === true } : { state: "unverified", reason: "error", ack_eligible: false };
      row.fullAck = false; row.ackStale = false; // a new gate result is a new thing to acknowledge
      row.hours = row.gate.state === "read_only" ? awsHoursFromExpiry(res.expires, Date.now()) : 0;
      awsPaintModeNote(row);
      row.busy = "";
      awsPaintRes(row);
      awsPaintFoot();
      return true;
    }
    const res = await awsPost("/api/credentials/aws/check", { source_profile: row.source_profile, role_arn: row.role_arn, region: row.region });
    if (awsFlow !== f) return false;
    row.assume = res.assume || "error";
    row.verdict = res.verdict || null;
    row.actions = res.actions || [];
    row.reason = res.reason || "";
    row.reasonText = res.reason_text || "";
    row.acked = false;
  } catch (e) {
    if (awsFlow !== f) return false;
    row.err = awsErrText(e);
    ok = false;
  }
  row.busy = "";
  awsPaintRes(row);
  awsPaintFoot();
  return ok;
}

async function awsCheckAll() {
  const f = awsFlow;
  if (!f) return;
  for (const row of f.rows) {
    if (row.assume === "ok" || (row.mode === "sso_direct" && row.gate && row.gate.state === "read_only") || row.busy) continue;
    // A rate limit or a missing signer ends the run; the rows left stay as they were.
    if (!(await awsCheckRow(row)) && row.err && /Too many|no AWS signer/.test(row.err)) break;
    if (awsFlow !== f) return;
  }
}

// awsBuildFoot: the add view's buttons in a host's own footer, the same four as #foot-creds-add. Each
// acts on this flow only: a click left over after the flow was dropped or replaced does nothing.
function awsBuildFoot(f) {
  const checkAll = awsBtn("Check all", "", () => (awsFlow === f ? awsCheckAll() : undefined));
  const note = awsEl("span", "grow hint");
  note.setAttribute("role", "status");
  const cancel = awsBtn("Cancel", "", () => (awsFlow === f ? awsExit() : undefined));
  const save = awsBtn(f.opts.saveLabel || "Save all", "pri off", () => (awsFlow === f ? awsSaveAll() : undefined));
  save.disabled = true;
  f.foot = { checkAll, note, cancel, save };
  f.opts.foot.replaceChildren(checkAll, note, cancel, save);
}

// awsWaitingText: the foot's note when rows wait for "I understand", and the Save reason with it.
function awsWaitingText(n) {
  return n + (n === 1 ? " row is" : " rows are") + " waiting for “I understand”.";
}

// awsFootNote: the foot's note line for the rows' states (awsRowState of each, in order). It is the foot's one
// account of why Save is off, which is why awsFootReasons reuses it word for word.
function awsFootNote(f, states) {
  const waiting = states.filter((s) => s.needsAck).length;
  const ready = states.filter((s) => s.canSave).length;
  const unchecked = states.filter((s) => !s.canSave && !s.needsAck).length;
  const edit = f.mode === "edit";
  if (waiting) return awsWaitingText(waiting);
  if (f.rows.length && !edit) return ready + " of " + f.rows.length + " ready to save." + (ready && unchecked ? " Rows that did not pass a check are left as they are." : "");
  if (edit && !ready) return f.rows.some((r) => r.mode === "sso_direct") ? "Check that the permission set is read-only before saving." : "Check the new role before saving.";
  return "";
}

// awsFootReasons: why the foot's two buttons cannot act, in the words of their tooltips (F03, M5.6); "" when
// the button can, or when the reason is plain (a save in flight). Where the note already says why Save is off
// (rows waiting for "I understand", or an edit that has not been checked) the Save reason IS the note's text, so
// the tooltip and the line beside it never disagree; the add view with rows but none ready and none waiting
// ("0 of 2 ready to save.") has no sentence of that kind and says "Nothing ready to save yet". `states` is
// awsRowState of each row, when the caller has it already.
function awsFootReasons(f, states = f.rows.map(awsRowState)) {
  const waiting = states.filter((s) => s.needsAck).length;
  const ready = states.filter((s) => s.canSave).length;
  let save = "";
  if (!f.saving) {
    if (waiting > 0 || (f.mode === "edit" && ready === 0)) save = awsFootNote(f, states);
    else if (ready === 0) save = "Nothing ready to save yet";
  }
  return { check: !f.rows.length ? "Nothing to check yet: tick a profile above" : "", save };
}

// awsDesktop: the desktop layout, as drawerDesktop() (app.js, which loads after this file) decides it.
function awsDesktop() {
  return !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
}

// setDisabledReason: a button that cannot act yet, and why (F03, M5.6). Desktop: aria-disabled="true" and the
// reason as its title, never the disabled attribute: a disabled button gets no hover in some browsers and no
// focus, so its reason could be neither read nor reached. Turned on again, both go. The click guard is
// awsUnlessOff. The phone keeps `disabled`, as it always had, with no title; whichever of the other state a
// window narrowed or widened across 700px left behind is cleared, so a repaint always leaves one state.
function setDisabledReason(el, off, reason, desktop) {
  if (!desktop) {
    el.disabled = !!off;
    el.removeAttribute("aria-disabled");
    el.removeAttribute("title");
    return;
  }
  el.disabled = false;
  if (off) el.setAttribute("aria-disabled", "true");
  else el.removeAttribute("aria-disabled");
  if (off && reason) el.setAttribute("title", reason);
  else el.removeAttribute("title");
}

function awsPaintFoot() {
  const f = awsFlow;
  if (!f) return;
  const fe = f.foot || {
    save: document.getElementById("aws-save"),
    checkAll: document.getElementById("aws-check-all"),
    note: document.getElementById("aws-foot-note"),
  };
  const sv = fe.save, ca = fe.checkAll, note = fe.note;
  if (!sv || !ca || !note) return;
  if (fe.cancel) fe.cancel.disabled = f.saving;
  const states = f.rows.map(awsRowState);
  const waiting = states.filter((s) => s.needsAck).length;
  const ready = states.filter((s) => s.canSave).length;
  const edit = f.mode === "edit";
  ca.textContent = edit ? "Check" : "Check all";
  const desktop = awsDesktop(), why = awsFootReasons(f, states);
  setDisabledReason(ca, !f.rows.length || f.saving, why.check, desktop);
  sv.textContent = edit ? "Save" : (f.opts && f.opts.saveLabel) || "Save all";
  const off = f.saving || ready === 0 || waiting > 0;
  setDisabledReason(sv, off, why.save, desktop);
  sv.classList.toggle("off", off);
  const text = awsFootNote(f, states);
  note.textContent = text;
  if (f.noteBox) f.noteBox.textContent = text;
  if (f.msgBox) f.msgBox.textContent = f.msg || "";
}

async function awsSaveAll() {
  const f = awsFlow;
  if (!f || f.saving) return;
  f.saving = true; f.msg = "";
  awsPaintFoot();
  const seen = new Set();
  let saved = 0, stop = false;
  for (const row of [...f.rows]) {
    if (stop) break;
    const st = awsRowState(row);
    if (!st.canSave) continue;
    if (f.mode === "add" && !AWS_NAME_RE.test(row.name)) { row.err = "The name must be lowercase letters, digits and -, starting with a letter or digit."; awsPaintRes(row); continue; }
    if (seen.has(row.name)) { row.err = "Another row in this list has the same name."; awsPaintRes(row); continue; }
    seen.add(row.name);
    row.busy = "save"; row.err = "";
    awsPaintRes(row);
    try {
      const body = { connection: awsConnFromRow(row), ack_write: row.acked === true };
      const ack = awsAckBody(row);
      if (ack) body.ack_full_access = ack;
      if (f.mode === "edit") await api("/api/credentials/aws/" + encodeURIComponent(row.name), { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      else await awsPost("/api/credentials/aws", body);
      if (awsFlow !== f) return;
      saved++;
      row.busy = "";
      if (f.mode === "add") {
        f.created.push(row.name);
        f.selected.delete(row.pname);
        row.el.remove();
        f.rows.splice(f.rows.indexOf(row), 1);
      }
    } catch (e) {
      if (awsFlow !== f) return;
      row.busy = "";
      const v = e.body && e.body.verdict;
      if (e.status === 409 && (e.code === "not_read_only" || e.code === "unverified") && e.body && e.body.gate) {
        // The server re-ran the gate (a label-only edit does too) and it did not pass.
        row.gate = { ...e.body.gate, ack_eligible: e.body.ack_eligible === true };
        row.err = "";
        if (e.body.ack_stale === true) {
          row.fullAck = false;
          row.ackStale = true; // the stored acknowledgement no longer counts: a new tick is needed
          row.err = "The permission set changed since you checked it; review and acknowledge again.";
        }
        // The gate did not pass, so no credential lifetime is known: a stale
        // "up to N h" from an earlier check must not stay in the note.
        row.hours = 0;
        awsPaintModeNote(row);
      } else if (e.status === 409 && e.code === "needs_ack") {
        // The server checked and found write access the client had not seen (or the role changed).
        row.assume = "ok"; row.verdict = (v && v.state) || "writes"; row.actions = (v && v.actions) || []; row.acked = false;
        row.err = "";
      } else {
        row.err = awsErrText(e);
      }
      if (e.status === 429 || e.code === "no_broker") stop = true;
    }
    awsPaintRes(row);
  }
  f.saving = false;
  if (f.mode === "edit" ? saved > 0 : f.rows.length === 0) { await awsExit(); return; }
  if (saved) f.msg = "Saved " + saved + (saved === 1 ? " connection." : " connections.") + " The rest need attention.";
  awsPaintProfiles();
  awsPaintFoot();
  if (f.opts && typeof f.opts.onIdle === "function") f.opts.onIdle();
}

// awsDelete: an inline confirmation in the card (no browser dialog), then DELETE.
function awsDelete(entry, reload, btn) {
  const card = credEntryHost(btn);
  if (!card || card.querySelector(".confirm")) return;
  const box = awsEl("div", "confirm");
  box.appendChild(awsEl("p", null, "Delete the connection " + entry.name + "? The AWS role and your host profile are not touched."));
  const err = awsEl("div", "ent-err");
  err.setAttribute("role", "alert");
  const btns = awsEl("div", "btns");
  const del = awsBtn("Delete", "dgr", async () => {
    del.disabled = true;
    err.textContent = "";
    try {
      await api("/api/credentials/aws/" + encodeURIComponent(entry.name), { method: "DELETE" });
    } catch (e) {
      err.textContent = awsErrText(e);
      del.disabled = false;
      return;
    }
    reload();
  });
  btns.append(del, awsBtn("Cancel", "", () => credDismissConfirm(box)));
  box.append(err, btns);
  card.appendChild(box);
}

(function awsWire() {
  if (typeof document === "undefined") return;
  credActionHandlers.aws = { add: () => awsOpenAdd(), edit: (entry) => awsOpenEdit(entry), delete: awsDelete };
  const on = (id, fn) => { const e = document.getElementById(id); if (e) e.onclick = awsUnlessOff(e, fn); };
  on("aws-check-all", () => awsCheckAll());
  on("aws-save", () => awsSaveAll());
  on("aws-cancel", () => awsExit());
})();

// --- Tokens, GitHub Apps and stored values: add / edit / delete (browser only) ---
//
// One form view in place of the list, with its own footer (#foot-creds-form):
//   token add:   provider tiles -> the template's fields -> name, label, prod -> the value(s)
//   token edit:  label, description, prod, and a replacement value
//   GitHub App:  name, client ID, the private key (pasted, or a file path under Advanced)
//   value:       an id and a value (Set), or the value alone (Replace)
// A value is typed into a password field, read once when Save is pressed, sent,
// and the field is emptied whatever the answer was. credForm (see credKeep) never
// holds one; a value sits in its input and in the one request body, nowhere else.

let credTemplates = null;
let credFormSubmit = null;
let credFormBusy = false;
let credFormErrEl = null;

async function credLoadTemplates() {
  if (!credTemplates) {
    const res = await api("/api/credentials/templates");
    credTemplates = res.templates || [];
  }
  return credTemplates;
}

// --- the add flows in a host of the caller's (the drawer over the profile page) ---------------
//
// awsOpenAdd, credTokenAdd, credAppForm and credGitHubChoose take opts = { host, foot, onCreated(names,
// kind), onClose(), saveLabel }. saveLabel, when given, is the primary button's text ("Save all" or "Save"
// otherwise). Without opts they are the Credentials tab's views: the body #creds-body, the tab's
// footers, loadCredentials on the way out. With opts they draw into opts.host and put their buttons in
// opts.foot, and on the way out call opts.onCreated(names, kind) when something was created (kind is
// "aws", "token" or "app"; names are the connection or token names the server holds now) and then
// opts.onClose(). They never call loadCredentials or touch the tab's footers: the caller learns what
// was made from onCreated. Leaving a flow without creating anything (Cancel, the crumb) calls only onClose.

// credTabFlowOpen: an Add / Edit view is open in the Credentials tab (a view in a host is not).
function credTabFlowOpen() {
  return !!((awsFlow && !awsFlow.opts) || (credForm && !credFormOpts));
}

// credFlowDrop: forgets whichever Add / Edit view is open, reporting nothing: its timers stop and the
// singletons clear. The caller of a flow in a host uses it when the host closes by itself (the drawer's
// own close, Esc), once credFlowBusy() is false: a flow dropped mid-save never hears the answer. A view left open in the tab loses its Add / Edit footers too (the tab's list footer
// is not put back: when this runs another tab is the one showing; selecting Credentials again
// reloads the list).
function credFlowDrop() {
  credHostSeq++;
  const inTab = credTabFlowOpen();
  const f = awsFlow;
  if (f) for (const t of f.timers) clearInterval(t);
  awsFlow = null;
  credKeep(null);
  credFormSubmit = null;
  credFormErrEl = null;
  credFormOpts = null;
  credFormBtns = null;
  if (inTab) {
    for (const id of ["foot-creds-add", "foot-creds-form"]) {
      const e = document.getElementById(id);
      if (e) e.hidden = true;
    }
  }
}

// credFlowStart: a flow about to open in a host first drops what else is open (a view left open in the
// tab, an earlier one in a host), so two views never share the singletons. The same host's own flow
// moving on (GitHub: the choice, then the route picked) is not dropped.
function credFlowStart(opts) {
  if (opts && credFormOpts !== opts) credFlowDrop();
}

// credFlowDone: the way out of a flow in a host. onClose runs even when onCreated throws (the error
// still surfaces), so the host is never left open on a flow that has already been reset.
function credFlowDone(opts, names, kind) {
  try {
    if (names && names.length && typeof opts.onCreated === "function") opts.onCreated([...names], kind);
  } finally {
    if (typeof opts.onClose === "function") opts.onClose();
  }
}

// credFlowBusy: a flow in a host is saving right now. The host should not close until this is false:
// credFlowDrop reports nothing, and a save whose host is gone has no one left to tell (the connection or
// token is created all the same), so closing mid-save would lose the name.
function credFlowBusy() {
  return !!((awsFlow && awsFlow.opts && awsFlow.saving) || (credFormOpts && credFormBusy));
}

// credFlowCancel: the host closing (the drawer's Esc, scrim or a way out of the page) ends the flow open in
// `opts` the way its own Cancel does: what the visit saved is reported through opts.onCreated, then
// opts.onClose runs. False, and nothing happens, when no flow is open in that host (a flow still reading
// the server has not drawn anything: the host then calls credFlowDrop) or when it is saving (the answer
// ends it, and reports it, by itself). The host's own onClose is where it tears itself down. The tab's
// own views are not the host's to end.
function credFlowCancel(opts) {
  if (!opts || credFlowBusy()) return false;
  if (awsFlow && awsFlow.opts === opts) { awsExit(); return true; }
  if (credFormOpts === opts) { credFormExit(); return true; }
  return false;
}

// credHostErr: why a flow could not start, in the tab's error line or, in a host, in the host with a
// Cancel that closes it.
function credHostErr(opts, msg) {
  if (!opts) { setCredErr(msg); return; }
  const err = credEl("p", "ent-err", msg);
  err.setAttribute("role", "alert");
  if (opts.head) opts.head.replaceChildren();
  opts.host.replaceChildren(err);
  const seq = credHostSeq;
  const cancel = credEl("button", "btn", "Cancel");
  cancel.type = "button";
  cancel.onclick = () => { if (credHostSeq === seq) { credFlowDrop(); credFlowDone(opts); } };
  opts.foot.replaceChildren(credEl("span", "grow"), cancel);
}

// credHostPrime: a flow in a host reads the store and the tokens already there before it draws (the tab's
// own load has done that by then). False means stop: the host went away meanwhile, or the read failed,
// which is said in the host (credRefreshView keeps the last view, and with none the form would offer to
// create a store that may exist).
async function credHostPrime(opts, seq) {
  const ok = await credRefreshView();
  if (credHostSeq !== seq) return false;
  if (!ok) {
    credHostErr(opts, "Could not read the credentials, so it is not known whether the encrypted store exists. Cancel and try again.");
    return false;
  }
  return true;
}

// credSaveBtn: the Save button of the open form view, wherever it is.
function credSaveBtn() {
  return credFormOpts ? (credFormBtns && credFormBtns.save) : document.getElementById("cred-form-save");
}

// credFormBuildFoot: a form view's Cancel and Save in a host's own footer, as #foot-creds-form has them.
// Both act on this view only (`submit` is its own): a click left over after it was dropped or replaced
// does nothing, and never takes the tab's way out.
function credFormBuildFoot(opts, saveLabel, submit) {
  const cancel = credEl("button", "btn", "Cancel");
  cancel.type = "button";
  cancel.onclick = () => (credFormSubmit === submit ? credFormExit() : undefined);
  const save = credEl("button", "btn pri", saveLabel || "Save");
  save.type = "button";
  save.onclick = () => (credFormSubmit === submit ? credFormRun() : undefined);
  // The spacer left of Cancel. In a framed host it is also where a refused Save is said (credFormErrEl).
  const msg = credEl("span", opts.head ? "grow ent-err" : "grow");
  if (opts.head) msg.setAttribute("role", "alert");
  credFormBtns = { save, cancel, msg };
  opts.foot.replaceChildren(msg, cancel, save);
}

function credFormFoot(on) {
  const form = document.getElementById("foot-creds-form");
  const base = document.getElementById("foot-creds");
  const add = document.getElementById("foot-creds-add");
  if (form) form.hidden = !on;
  if (base) base.hidden = on;
  if (add) add.hidden = true;
  credAddMenuSync();
}

function credFormErr(msg) {
  if (credFormErrEl) credFormErrEl.textContent = msg || "";
}

function credLink(text, fn) {
  const a = credEl("a", "lnk", text);
  a.tabIndex = 0;
  a.setAttribute("role", "button");
  a.onclick = fn;
  a.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); fn(); } };
  return a;
}

// credField: a labelled text input (or textarea). Returns the column, the input
// and the hint paragraph (so a caller can reword it as the name changes).
function credField(id, label, opts) {
  const o = opts || {};
  const col = credEl("div", "fcol" + (o.full ? " full" : ""));
  const lab = credEl("label", "flab", label);
  lab.htmlFor = id;
  const inp = document.createElement(o.area ? "textarea" : "input");
  inp.id = id;
  inp.className = "fld";
  if (!o.area) inp.type = "text";
  inp.autocomplete = "off";
  inp.spellcheck = false;
  inp.setAttribute("autocapitalize", "off");
  if (o.value) inp.value = o.value;
  if (o.placeholder) inp.placeholder = o.placeholder;
  if (o.max) inp.maxLength = o.max;
  if (o.readonly) { inp.readOnly = true; inp.classList.add("ro"); }
  col.append(lab, inp);
  let hint = null;
  if (o.hint) { hint = credEl("p", "hint", o.hint); col.appendChild(hint); }
  return { col, inp, hint };
}

// credSecretField: a value field. A password input, or (for a key that spans
// lines, which a password input would flatten on paste) a masked textarea.
function credSecretField(id, label, opts) {
  const o = opts || {};
  const f = credField(id, label, { full: true, area: !!o.area, hint: o.hint });
  const inp = f.inp;
  if (o.area) { inp.classList.add("secret-area"); inp.rows = 5; } else { inp.type = "password"; }
  // A textarea is no password field: "new-password" there can invite a manager to
  // offer (and save) a generated password into the key.
  inp.autocomplete = o.area ? "off" : "new-password";
  inp.setAttribute("autocorrect", "off");
  inp.name = "agent-box-" + id;
  return f;
}

function credCheck(id, text, checked) {
  const lab = credEl("label", "pf-check");
  lab.htmlFor = id;
  const cb = document.createElement("input");
  cb.type = "checkbox";
  cb.id = id;
  cb.checked = !!checked;
  lab.append(cb, document.createTextNode(" " + text));
  const col = credEl("div", "fcol full");
  col.appendChild(lab);
  return { col, cb };
}

function credStoreReady() {
  return !!(credView && credView.store && credView.store.initialized);
}

// credRefreshView: re-read the store and the read model without repainting the
// body (a form is open). Used after the store was created from inside a form.
// It answers whether the read worked; a failed one keeps the last view (the tab's forms go on with it).
async function credRefreshView() {
  if (typeof refreshStore === "function") await refreshStore();
  try { credView = await api("/api/credentials"); } catch (_) { return false; /* the form keeps its last view */ }
  return true;
}

// credStoreCreateNode: "Create the encrypted store" (D6). Linux: a passphrase twice
// (at least 12 characters); macOS: one button (the key goes in the Keychain). A
// seccomp refusal shows the server's own sentence, word for word. The passphrase
// fields are emptied after every attempt. `hold` marks the fields so a repaint of
// the tab waits while one is being typed in. `lead`, when given, is a sentence the card opens with.
// `secondary`: the card sits in a host whose own foot carries the view's primary Save (the
// drawer's opts.foot), so Create is a secondary there (one primary per view, F11 #171). The tab,
// the phone's path too, keeps it primary.
function credStoreCreateNode(onDone, hold, lead, secondary) {
  const st = (credView && credView.store) || {};
  // The server's own answer (the keyring in use), which is what store init checks.
  const passMode = !!st.passphrase_mode;
  const box = credEl("div", "storecreate");
  if (lead) box.appendChild(credEl("p", "lead", lead));
  box.appendChild(credEl("p", "hint", passMode
    ? "Values are stored encrypted. Choose a passphrase of at least 12 characters. You type it to unlock the store, and it cannot be recovered if you lose it."
    : "Values are stored encrypted, with a key kept in the macOS Keychain. There is no passphrase to type."));
  const fields = [];
  if (passMode) {
    for (const [id, label] of [["store-pass1", "Passphrase"], ["store-pass2", "Repeat the passphrase"]]) {
      const f = credField(id, label, {});
      f.inp.type = "password";
      f.inp.autocomplete = "new-password";
      f.inp.name = "agent-box-" + id;
      if (hold) f.inp.classList.add("storepass");
      box.appendChild(f.col);
      fields.push(f.inp);
    }
  }
  const err = credEl("div", "ent-err");
  err.setAttribute("role", "alert");
  const btn = credEl("button", secondary ? "btn" : "btn pri", "Create the encrypted store");
  btn.type = "button";
  btn.onclick = async () => {
    const a = fields[0] ? fields[0].value : "";
    const b = fields[1] ? fields[1].value : "";
    const chk = storeCreateCheck(passMode, a, b);
    if (!chk.ok) { err.textContent = chk.msg; return; }
    btn.disabled = true;
    btn.textContent = "Creating…";
    err.textContent = "";
    let ok = false;
    try {
      await api("/api/store/init", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(passMode ? { passphrase: a } : {}) });
      ok = true;
    } catch (e) {
      if (e.code === "exists") ok = true;
      else if (e.code === "seccomp_off" || e.code === "seccomp_unknown") err.textContent = e.message;
      else err.textContent = credErrText(e, "store");
    } finally {
      for (const f of fields) valueFieldCleared(f);
      btn.disabled = false;
      btn.textContent = "Create the encrypted store";
    }
    if (ok) onDone();
  };
  box.append(err, btn);
  return box;
}

// credOpenForm: show a form view. `meta` is the non-secret state kept (kind and
// the like); `submit` runs on Save and returns what it created once the save worked
// ({ names, kind } for an add, which a flow in a host reports; true for the rest) and
// false when it did not. `opts` is the host the view is drawn in (see above), else the tab.
function credOpenForm(meta, crumbText, nodes, submit, opts) {
  const host = opts ? opts.host : document.getElementById("creds-body");
  if (!host) return;
  if (!opts) setCredErr("");
  credKeep(meta);
  credFormSubmit = submit;
  credFormBusy = false;
  credFormOpts = opts || null;
  // A framed host (opts.head, the drawer) has a header of its own and says a refused Save in its foot.
  const framed = !!(opts && opts.head);
  const crumb = credEl("div", "crumb");
  const leave = opts ? () => (credFormSubmit === submit ? credFormExit() : undefined) : () => credFormExit();
  // The mockup's crumb: "Credentials › Tokens" for a token (it draws no other form), the bare link otherwise.
  const tail = framed ? (meta.kind === "token-add" ? " › Tokens" : "") : " / ";
  crumb.append(credLink("Credentials", leave), ...(tail ? [document.createTextNode(tail)] : []), credEl("b", null, crumbText));
  if (framed) {
    opts.head.replaceChildren(crumb, ...(meta.kind === "token-add" ? [credEl("p", "intro", CRED_N3)] : []));
    host.replaceChildren(...nodes);
  } else {
    const err = credEl("div", "ent-err");
    err.setAttribute("role", "alert");
    credFormErrEl = err;
    host.replaceChildren(crumb, ...nodes, err);
  }
  if (opts) {
    credFormBuildFoot(opts, opts.saveLabel || meta.saveLabel, submit);
    if (framed) credFormErrEl = credFormBtns.msg;
  } else {
    const save = document.getElementById("cred-form-save");
    if (save) { save.textContent = meta.saveLabel || "Save"; save.disabled = false; }
    credFormFoot(true);
  }
  if (host.parentElement) host.parentElement.scrollTop = 0;
}

// credFormExit: leave the form view. `made` is what the submit reported ({ names, kind }), which a
// view in a host passes on to opts.onCreated; the tab reloads its list instead.
async function credFormExit(made) {
  const opts = credFormOpts;
  // In a host the way out waits for a save in flight, so what it creates is reported.
  if (opts && credFormBusy) return;
  if (opts) {
    credFlowDrop();
    credFlowDone(opts, made && made.names, made && made.kind);
    return;
  }
  credKeep(null);
  credFormSubmit = null;
  credFormErrEl = null;
  credFormFoot(false);
  await loadCredentials();
}

async function credFormRun() {
  if (!credFormSubmit || credFormBusy) return;
  const seq = credHostSeq;
  const save = credSaveBtn();
  const cancel = credFormOpts && credFormBtns ? credFormBtns.cancel : null;
  credFormBusy = true;
  if (save) save.disabled = true;
  if (cancel) cancel.disabled = true;
  credFormErr("");
  let done = false;
  try {
    done = await credFormSubmit();
  } finally {
    credFormBusy = false;
    if (save) save.disabled = false;
    if (cancel) cancel.disabled = false;
  }
  // The host dropped this flow while it saved (credFlowDrop): nothing is left to leave or to tell. The
  // tab never drops, so it goes on to leave, as it always did, even after its Cancel was pressed.
  if (credHostSeq !== seq) return;
  if (done) await credFormExit(done);
  else if (credFormOpts && typeof credFormOpts.onIdle === "function") credFormOpts.onIdle();
}

async function credSend(method, path, body) {
  return api(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
}

// --- token add / edit ----------------------------------------------------------

function credTokensEmpty() {
  const tables = credTableMode();
  const sec = credEl("section", tables ? "sec ctab-sec" : "sec"); // page-only class, never the phone's .card (see credSection)
  sec.dataset.group = "tokens";
  sec.setAttribute("aria-label", "Tokens");
  const h = tables ? credSecHead("Tokens", 0) : credEl("div", "sec-h");
  if (!tables) {
    h.appendChild(credEl("span", "grow", "Tokens "));
    h.firstChild.appendChild(credEl("span", "count", "0"));
    // No add of its own on the desktop page's cards: the head's "Add credential ▾" adds a token (see credSection).
    const add = !credPageMode() && credActionHandlers.token && credActionHandlers.token.add;
    if (typeof add === "function") {
      const b = credEl("button", "btn", "Add service token");
      b.type = "button";
      b.onclick = () => add(null, loadCredentials);
      h.appendChild(b);
    }
  }
  sec.append(h, credEl("p", "hint", "No tokens yet. Add a Sentry, Grafana, Datadog, PagerDuty or GitHub token, or your own, and the box gets it as an environment variable."));
  return sec;
}

// credSeg: a radio group of buttons for a select field of up to three options.
function credSeg(fd, labelId, onChange) {
  const seg = credEl("div", "seg");
  seg.setAttribute("role", "radiogroup");
  seg.setAttribute("aria-labelledby", labelId);
  let cur = fd.def;
  const btns = fd.options.map((o) => {
    const b = credEl("button", "", o.label);
    b.type = "button";
    b.setAttribute("role", "radio");
    b.onclick = () => { cur = o.value; paint(); onChange(); };
    seg.appendChild(b);
    return [o, b];
  });
  function paint() {
    for (const [o, b] of btns) { b.classList.toggle("on", o.value === cur); b.setAttribute("aria-checked", o.value === cur ? "true" : "false"); }
  }
  paint();
  return { node: seg, get: () => cur };
}

function credTokenDetails(t, taken, opts) {
  const tf = templateForm(t);
  const uid = "ct-" + t.id;
  const sec = credEl("section", "sec");
  sec.setAttribute("aria-label", t.label + " details");
  const h = credEl("div", "sec-h");
  h.appendChild(credEl("span", "grow", t.label + (t.group === "github" ? "" : " token")));
  const grid = credEl("div", "frow");
  const getters = {};
  const whens = [];
  const refresh = () => { for (const [col, w] of whens) col.hidden = getters[w.key]() !== w.value; };
  for (const fd of tf.fields) {
    let col;
    if (fd.kind === "select") {
      col = credEl("div", "fcol full");
      const labId = uid + "-" + fd.key + "-l";
      const lab = credEl("span", "flab", fd.label);
      lab.id = labId;
      col.appendChild(lab);
      if (fd.options.length <= 3) {
        const seg = credSeg(fd, labId, refresh);
        col.appendChild(seg.node);
        getters[fd.key] = seg.get;
      } else {
        const sel = document.createElement("select");
        sel.className = "fld";
        sel.setAttribute("aria-labelledby", labId);
        for (const o of fd.options) { const op = document.createElement("option"); op.value = o.value; op.textContent = o.label; sel.appendChild(op); }
        sel.value = fd.def;
        sel.onchange = refresh;
        col.appendChild(sel);
        getters[fd.key] = () => sel.value;
      }
    } else {
      const f = credField(uid + "-" + fd.key, fd.label, { full: true, placeholder: fd.kind === "url" ? "https://" : "" });
      getters[fd.key] = () => f.inp.value;
      col = f.col;
    }
    grid.appendChild(col);
    if (fd.when) whens.push([col, fd.when]);
  }
  refresh();

  const nm = credField(uid + "-name", "Name", { value: credUniqueName(tf.defaultName, taken, credTemplateSuffixes(t)), max: 63 });
  const lb = credField(uid + "-label", "Label", { placeholder: t.label, max: 32 });
  const prod = credCheck(uid + "-prod", "production", false);
  grid.append(nm.col, lb.col, prod.col);

  const valuesHost = credEl("div", "fcol full");
  grid.appendChild(valuesHost);
  let valueEls = [];
  const isGHPat = t.id === "github-pat";
  const patWarn = credEl("p", "hint warnline");
  patWarn.hidden = true;
  const hintText = (i) => "Goes into the encrypted store as " + valueIdsText(t, nm.inp.value, i) + " and is never shown again. The field clears after you save. You can add it while the store is locked.";
  let hints = [];
  const paintValues = () => {
    valueEls = [];
    hints = [];
    if (!credStoreReady()) {
      // In a framed host the card opens with the sentence Save would refuse with (the mockup's lead).
      valuesHost.replaceChildren(credStoreCreateNode(async () => { await credRefreshView(); paintValues(); }, false, opts && opts.head ? CRED_STORE_FIRST : "", !!opts));
      return;
    }
    const nodes = [];
    for (const v of tf.values) {
      const f = credSecretField(uid + "-v" + v.index, v.label, { hint: hintText(v.index) });
      valueEls.push(f.inp);
      hints.push(f.hint);
      nodes.push(f.col);
      if (isGHPat) {
        f.inp.addEventListener("input", () => {
          patWarn.textContent = ghPatValueWarning(f.inp.value);
          patWarn.hidden = !patWarn.textContent;
        });
        f.col.appendChild(patWarn);
      }
    }
    if (isGHPat) nodes.unshift(credEl("p", "hint", GH_PAT_SUGGESTION));
    valuesHost.replaceChildren(...nodes);
  };
  nm.inp.addEventListener("input", () => { hints.forEach((hn, i) => { hn.textContent = hintText(i); }); });
  paintValues();

  // The sentence about what a stored token is goes at the foot of the form in the tab, and in the header (credOpenForm) in a framed host.
  if (opts && opts.head) sec.append(h, grid);
  else sec.append(h, grid, credEl("p", "honest", CRED_N3));
  return {
    node: sec,
    ready: () => credStoreReady(),
    collect: () => ({
      name: nm.inp.value, label: lb.inp.value, prod: prod.cb.checked,
      fields: Object.fromEntries(Object.keys(getters).map((k) => [k, getters[k]()])),
      values: valueEls.map((e) => e.value),
    }),
    clearValues: () => { for (const e of valueEls) valueFieldCleared(e); patWarn.textContent = ""; patWarn.hidden = true; },
  };
}

// credTokenAdd: the Add service token view, starting on the template `startID` or the group's provider. Given
// opts (a host, see above) it first reads the store and the tokens already there (credRefreshView), which
// the tab's own load has done by then; a missing store is the inline "Create the encrypted store" step,
// and a locked one does not stop the add. It reports the names the server created, kind "token".
async function credTokenAdd(group, startID, opts) {
  credFlowStart(opts);
  const seq = credHostSeq;
  if (opts && !(await credHostPrime(opts, seq))) return;
  let tpls;
  try {
    tpls = await credLoadTemplates();
  } catch (e) {
    if (credHostSeq !== seq) return;
    credHostErr(opts, "Could not load the token providers: " + credErrText(e, "token"));
    return;
  }
  if (credHostSeq !== seq) return;
  if (!tpls.length) { credHostErr(opts, "There are no token providers to choose from."); return; }
  const want = group && group.key && group.key.startsWith("token:") ? group.key.slice(6) : "";
  const start = tpls.find((t) => startID && t.id === startID) || tpls.find((t) => want && t.provider === want && t.id !== "other") || (want ? tpls.find((t) => t.id === "other") : null) || tpls[0];
  const taken = (credView && credView.tokens ? credView.tokens : []).map((t) => t.name);

  const tilesSec = credEl("section", "sec");
  tilesSec.setAttribute("aria-label", "Provider");
  const th = credEl("div", "sec-h");
  th.appendChild(credEl("span", "grow", "Provider"));
  const tiles = credEl("div", "tiles");
  const detHost = credEl("div");
  const tileBtns = [];
  let cur = null;
  let det = null;
  const select = (t) => {
    cur = t;
    for (const [id, b] of tileBtns) { b.classList.toggle("on", id === t.id); b.setAttribute("aria-pressed", id === t.id ? "true" : "false"); }
    det = credTokenDetails(t, taken, opts);
    detHost.replaceChildren(det.node);
    credKeep({ kind: "token-add", template: t.id, saveLabel: "Save" });
    credFormErr("");
  };
  for (const t of tpls) {
    const b = credEl("button", "tile");
    b.type = "button";
    b.append(credEl("span", "n", t.label), credEl("span", "s", (t.values || []).join(" + ")));
    b.onclick = () => select(t);
    tileBtns.push([t.id, b]);
    tiles.appendChild(b);
  }
  tilesSec.append(th, tiles);

  credOpenForm({ kind: "token-add", template: start.id, saveLabel: "Save" }, "Add service token", [tilesSec, detHost], async () => {
    const body = fromTemplateBody(cur, det.collect());
    if (!body.name) { credFormErr("Give the token a name."); return false; }
    if (!det.ready()) { credFormErr(CRED_STORE_FIRST); return false; }
    if (body.values.some((v) => v === "")) { credFormErr("Paste the value to store."); return false; }
    let res = null;
    try {
      res = await credSend("POST", "/api/credentials/tokens/from-template", body);
    } catch (e) {
      credFormErr(credErrText(e, "token"));
      return false;
    } finally {
      det.clearValues();
      body.values.fill("");
    }
    return { names: (res && res.names) || [], kind: "token" };
  }, opts);
  select(start);
}

// credGitHubChoose: "Add GitHub" -- a token or an App, side by side. Given opts (a host, see above) the
// route picked runs in the same host and reports as it does: a token its names, an App its name.
function credGitHubChoose(opts) {
  credFlowStart(opts);
  const sec = credEl("section", "sec");
  sec.setAttribute("aria-label", "GitHub");
  const h = credEl("div", "sec-h");
  h.appendChild(credEl("span", "grow", "How should the agent reach GitHub?"));
  const tiles = credEl("div", "tiles");
  for (const c of GH_ROUTE_CHOICES) {
    const b = credEl("button", "tile");
    b.type = "button";
    b.append(credEl("span", "n", c.title), credEl("span", "s", c.line));
    b.onclick = () => (c.id === "pat" ? credTokenAdd({ key: "token:github" }, "github-pat", opts) : credAppForm(null, opts));
    tiles.appendChild(b);
  }
  sec.append(h, tiles);
  credOpenForm({ kind: "github-choose", saveLabel: "Save" }, "Add GitHub", [sec], async () => {
    credFormErr("Choose a token or an App above.");
    return false;
  }, opts);
  const save = credSaveBtn();
  if (save) save.disabled = true;
}

function credTokenEdit(entry) {
  const saved = entry.entry;
  if (!saved) { setCredErr("This token cannot be edited here. Reload the page and try again."); return; }
  const uid = "cte";
  const sec = credEl("section", "sec");
  sec.setAttribute("aria-label", "Edit token");
  const h = credEl("div", "sec-h");
  h.appendChild(credEl("span", "grow", entry.name));
  const grid = credEl("div", "frow");
  const meta = credMeta([["Env", (entry.env || []).join(", ")], ["Agent may reach", (entry.hosts || []).join(", ")], ["Value", credValueRef(entry)]]);
  const mcol = credEl("div", "fcol full");
  mcol.appendChild(meta);
  mcol.appendChild(credEl("p", "hint", "To change the provider settings, delete this token and add it again."));
  const lb = credField(uid + "-label", "Label", { value: entry.label || "", max: 32 });
  const ds = credField(uid + "-desc", "Description", { value: entry.description || "", max: 200 });
  const prod = credCheck(uid + "-prod", "production", entry.prod);
  const val = credSecretField(uid + "-value", "Replace the value (leave empty to keep it)", {
    hint: "The new value replaces the stored one under the same id and is never shown again. The field clears after you save.",
  });
  grid.append(mcol, lb.col, ds.col, prod.col, val.col);
  sec.append(h, grid, credEl("p", "honest", CRED_N3));
  credOpenForm({ kind: "token-edit", name: entry.name, saveLabel: "Save" }, "Edit " + entry.name, [sec], async () => {
    const body = tokenEditBody(saved, { label: lb.inp.value, description: ds.inp.value, prod: prod.cb.checked, value: val.inp.value });
    let ok = false;
    try {
      await credSend("PUT", "/api/credentials/tokens/" + encodeURIComponent(entry.name), body);
      ok = true;
    } catch (e) {
      credFormErr(credErrText(e, "token"));
    } finally {
      valueFieldCleared(val.inp);
      delete body.value;
    }
    return ok;
  });
}

// --- GitHub App add / edit -----------------------------------------------------

const CRED_APP_NAME_RE = /^[A-Za-z0-9._-]{1,64}$/;

// credAppForm: the GitHub App view, an Add (entry null) or an Edit. Given opts (a host, see above) it is
// an Add: it first reads the store (credRefreshView) and reports the App's name, kind "app".
async function credAppForm(entry, opts) {
  credFlowStart(opts);
  const seq = credHostSeq;
  if (opts && !(await credHostPrime(opts, seq))) return;
  const edit = !!entry;
  const uid = "cga";
  const sec = credEl("section", "sec");
  sec.setAttribute("aria-label", "GitHub App");
  const h = credEl("div", "sec-h");
  // The Edit heading is only the entry's name: on the desktop page the GitHub kind is said once, by a glyph (G1 21). Not on a phone's modal.
  h.appendChild(credTitleNode("span", "grow", edit ? entry.name : "GitHub App", edit && credPageMode() ? credIconMaps().ICON_RESOURCE.github : ""));
  const grid = credEl("div", "frow");
  const nm = credField(uid + "-name", "App name", { value: edit ? entry.name : "", readonly: edit, max: 64, placeholder: "my-bot" });
  const cid = credField(uid + "-cid", "Client ID", { value: edit ? entry.client_id || "" : "", placeholder: "Iv1.…", max: 100 });
  const keyHost = credEl("div", "fcol full");
  grid.append(nm.col, cid.col, keyHost);
  const help = document.createElement("details");
  help.appendChild(credEl("summary", "", "How to create the App"));
  const steps = document.createElement("ol");
  for (const step of GH_APP_HELP) steps.appendChild(credEl("li", "", step));
  help.appendChild(steps);
  sec.append(h, help, grid);

  let useFile = false;
  let keyEl = null;
  let pathEl = null;
  const paintKey = () => {
    keyEl = null;
    pathEl = null;
    const nodes = [];
    if (useFile) {
      const f = credField(uid + "-path", edit ? "Key file (leave empty to keep the current key)" : "Key file", {
        full: true, placeholder: "/home/you/.config/culibox/app.pem",
        hint: "An absolute path on this host. The file must be a regular file (not a symlink), owned by the user CuliBox runs as, with no group or other permissions (for example chmod 600). CuliBox reads it the first time a session needs a token and keeps the key until it restarts, so restart it after replacing the file. Nothing is copied into the store.",
      });
      pathEl = f.inp;
      nodes.push(f.col, credEl("p", "hint"));
      nodes[1].appendChild(credLink("Paste the key instead", () => { useFile = false; paintKey(); }));
    } else if (!credStoreReady()) {
      nodes.push(credStoreCreateNode(async () => { await credRefreshView(); paintKey(); }, false, "", !!opts));
      const p = credEl("p", "hint");
      p.appendChild(credLink("Advanced: use a file instead", () => { useFile = true; paintKey(); }));
      nodes.push(p);
    } else {
      const f = credSecretField(uid + "-key", edit ? "Replace the private key (leave empty to keep it)" : "Private key", {
        area: true,
        hint: "Paste the whole .pem file, BEGIN and END lines included. It goes into the encrypted store as " + (edit ? ghKeyID(entry.name) : "gh-<name>-key") + " and is never shown again. The field clears after you save.",
      });
      keyEl = f.inp;
      nodes.push(f.col);
      const p = credEl("p", "hint");
      p.appendChild(credLink("Advanced: use a file instead", () => { useFile = true; paintKey(); }));
      nodes.push(p);
    }
    keyHost.replaceChildren(...nodes);
  };
  paintKey();

  credOpenForm({ kind: edit ? "app-edit" : "app-add", name: edit ? entry.name : "", saveLabel: "Save" }, edit ? "Edit " + entry.name : "Add GitHub App", [sec], async () => {
    const form = { name: nm.inp.value, client_id: cid.inp.value, useFile, path: pathEl ? pathEl.value : "", key: keyEl ? keyEl.value : "" };
    if (!CRED_APP_NAME_RE.test(form.name.trim())) { credFormErr("The name may use letters, digits, . _ and -, up to 64 characters."); return false; }
    if (!form.client_id.trim()) { credFormErr("Enter the App's client ID."); return false; }
    if (useFile && form.path.trim() && !form.path.trim().startsWith("/")) { credFormErr("The key file path must be absolute (start with /)."); return false; }
    if (!edit && !(useFile ? form.path.trim() : form.key)) { credFormErr(useFile ? "Enter the path of the key file." : "Paste the App's private key."); return false; }
    if (!useFile && form.key && !credStoreReady()) { credFormErr("Create the encrypted store first. The key is stored in it."); return false; }
    const body = appBody(form);
    const made = form.name.trim();
    form.key = "";
    let ok = false;
    try {
      if (edit) await credSend("PUT", "/api/credentials/github-apps/" + encodeURIComponent(entry.name), body);
      else await credSend("POST", "/api/credentials/github-apps", body);
      ok = true;
    } catch (e) {
      credFormErr(credErrText(e, "GitHub App"));
    } finally {
      valueFieldCleared(keyEl);
      delete body.private_key_value;
    }
    return ok && !edit ? { names: [made], kind: "app" } : ok;
  }, opts);
}

// --- stored values ---------------------------------------------------------------

const CRED_ID_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

// credValueForm: Set a value (replaceId empty: an id and a value) or Replace one
// (the id is fixed). Values are write-only: nothing here ever shows one.
function credValueForm(replaceId) {
  const replace = !!replaceId;
  const uid = "csv";
  const sec = credEl("section", "sec");
  sec.setAttribute("aria-label", replace ? "Replace a value" : "Set a value");
  const h = credEl("div", "sec-h");
  h.appendChild(credEl("span", "grow", replace ? "Replace " + replaceId : "Set a value"));
  const grid = credEl("div", "frow");
  const idf = credField(uid + "-id", "Id", {
    value: replaceId || "", readonly: replace, max: 64, full: true, placeholder: "e.g. MY_SERVICE_TOKEN",
    hint: replace ? "" : "Letters, digits, - and _. A token for an agent goes in Add service token, and a GitHub App names its own value; a stored value is for something else that reads the store by this id. An agent account's token is pasted on the Agent accounts tab.",
  });
  const valHost = credEl("div", "fcol full");
  grid.append(idf.col, valHost);
  sec.append(h, grid);
  let valEl = null;
  const paintVal = () => {
    valEl = null;
    if (!credStoreReady()) {
      valHost.replaceChildren(credStoreCreateNode(async () => { await credRefreshView(); paintVal(); }, false));
      return;
    }
    const f = credSecretField(uid + "-value", replace ? "New value" : "Value", {
      hint: "Write-only: it goes in encrypted and is never shown again. The field clears after you save. You can do this while the store is locked.",
    });
    valEl = f.inp;
    valHost.replaceChildren(f.col);
  };
  paintVal();
  credOpenForm({ kind: replace ? "value-replace" : "value-set", id: replaceId || "", saveLabel: replace ? "Replace" : "Save" }, replace ? "Replace " + replaceId : "Set a value", [sec], async () => {
    const id = idf.inp.value.trim();
    if (!CRED_ID_RE.test(id)) { credFormErr("The id may use letters, digits, - and _, starting with a letter or digit, up to 64 characters."); return false; }
    if (!replace && credView && (credView.values || []).some((v) => v.id === id)) {
      credFormErr("A value with this id exists. Use Replace on its row.");
      return false;
    }
    if (!valEl) { credFormErr("Create the encrypted store first."); return false; }
    const value = valEl.value;
    if (!value) { credFormErr("Paste the value to store."); return false; }
    let ok = false;
    try {
      await credSend("PUT", "/api/store/values/" + encodeURIComponent(id), { value });
      ok = true;
    } catch (e) {
      credFormErr(credErrText(e, "value"));
    } finally {
      valueFieldCleared(valEl);
    }
    return ok;
  });
}

// credDismissConfirm: takes a confirmation away. In table mode it sat in the
// entry's full-width row (credEntryExtra); once that row holds nothing else it
// goes too, or it stays as an empty padded strip under the entry. A row that
// still holds the entry's error or write warning stays.
function credDismissConfirm(box) {
  const host = box.parentElement;
  box.remove();
  if (host && host.classList.contains("ctab-ex") && !host.firstElementChild) {
    const row = host.closest("tr.ctab-x");
    if (row) row.remove();
  }
  // The confirmation may have held back a repaint, for a new width or for new data
  // (one held while the unlock request was in flight): credFocusLeft makes either.
  credFocusLeft();
}

// credConfirmDelete: an inline confirmation in the card or row (no browser
// dialog), then DELETE `path`. `noun` picks the words for a refusal.
function credConfirmDelete(host, text, path, noun, reload) {
  if (!host || host.querySelector(".confirm")) return;
  const box = credEl("div", "confirm");
  box.appendChild(credEl("p", null, text));
  const err = credEl("div", "ent-err");
  err.setAttribute("role", "alert");
  const btns = credEl("div", "btns");
  const del = credEl("button", "btn dgr", "Delete");
  del.type = "button";
  del.onclick = async () => {
    del.disabled = true;
    err.textContent = "";
    try {
      await api(path, { method: "DELETE" });
    } catch (e) {
      err.textContent = credErrText(e, noun);
      del.disabled = false;
      return;
    }
    reload();
  };
  const cancel = credEl("button", "btn", "Cancel");
  cancel.type = "button";
  cancel.onclick = () => credDismissConfirm(box);
  btns.append(del, cancel);
  box.append(err, btns);
  host.appendChild(box);
}

(function credFormsWire() {
  if (typeof document === "undefined") return;
  credActionHandlers.token = {
    add: (g) => credTokenAdd(g),
    edit: (e) => credTokenEdit(e),
    delete: (e, reload, btn) => credConfirmDelete(credEntryHost(btn),
      credDeleteText("token", e),
      "/api/credentials/tokens/" + encodeURIComponent(e.name), "token", reload),
  };
  credActionHandlers.github = {
    add: () => credGitHubChoose(),
    edit: (e) => credAppForm(e),
    delete: (e, reload, btn) => credConfirmDelete(credEntryHost(btn),
      credDeleteText("github", e),
      "/api/credentials/github-apps/" + encodeURIComponent(e.name), "GitHub App", reload),
  };
  credActionHandlers.value = {
    add: () => credValueForm(""),
    edit: (v) => credValueForm(v.id),
    delete: (v, reload, btn) => credConfirmDelete(btn.closest(".sv"),
      "Delete the stored value " + v.id + "? It cannot be recovered.",
      "/api/store/values/" + encodeURIComponent(v.id), "value", reload),
  };
  const on = (id, fn) => { const e = document.getElementById(id); if (e) e.onclick = fn; };
  on("cred-form-save", () => credFormRun());
  on("cred-form-cancel", () => credFormExit());
})();
