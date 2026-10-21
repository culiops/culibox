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
const CRED_FILE_BAD = "credentials.json could not be read; it will be kept as a .bad copy on your next save";
// Said under a "writes" base role. Never "read-only" unqualified: a role that
// has no write access found is not a guarantee, and the word promises one.
const CRED_D4_REMEDY = "Use a role without write access as the base and put this one under Elevate.";

const CRED_STATUS = {
  ok: ["ready", "Ready"],
  missing: ["missing", "Missing"],
  stored_locked: ["locked", "Stored (store locked)"],
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

// credRoleName: the part of a role ARN after "role/".
function credRoleName(arn) {
  const s = String(arn || "");
  const i = s.indexOf(":role/");
  return i >= 0 ? s.slice(i + 6) : s;
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

// awsConnFromRow: the connection object the save routes take. Fields that are
// empty are left out, so an edit that clears them clears them.
function awsConnFromRow(row) {
  const c = { name: row.name, source_profile: row.source_profile, role_arn: row.role_arn, region: row.region };
  if (row.label) c.label = row.label;
  if (row.description) c.description = row.description;
  if (row.prod) c.prod = true;
  const egress = awsSplitList(row.egress);
  if (egress.length) c.egress_allow = egress;
  const s3 = awsSplitList(row.s3);
  if (s3.length) c.s3_read = s3;
  if (row.elevRole) {
    c.elevate = { role_arn: row.elevRole, max_minutes: parseInt(row.elevMinutes, 10) === 30 ? 30 : 60 };
    if (row.elevIAM) c.elevate.allow_iam = true;
  }
  return c;
}

const CRED_TRUST_HELP = "The role does not trust the signer. Ask the account admin for a role without write access that trusts agent-box's signing identity; agent-box does not create it.";

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

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    credGroups, statusText, statusClass, baseText, credWriteActions, usedByText, entryActions, isSeed,
    providerTitle, checkedText, credRoleName, valueRefsText, valueUpdatedText,
    awsConnName, awsRowFromProfile, rowSaveState, awsSplitList, awsRoleARN, awsConnFromRow, awsCheckView, awsErrText,
    CRED_D4, CRED_N3, CRED_FILE_BAD, CRED_D4_REMEDY,
  };
}

// --- DOM wiring (browser only) -------------------------------------------------

// The last GET /api/credentials, or null before it has answered. Never holds a
// secret: the read model carries references and states only.
let credView = null;
// True when a repaint was held back because the unlock field had focus; it is
// done once the field lets go (see credWireHeld).
let credRepaintHeld = false;
let credHeldWired = false;
// The Add / Edit AWS view while it is open, else null. See awsOpenAdd.
let awsFlow = null;

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

function credActions(entry, reload) {
  const wrap = credEl("div", "ent-act");
  const h = credActionHandlers[entry.kind] || {};
  for (const act of entryActions(entry)) {
    if (typeof h[act] !== "function") continue;
    const b = credEl("button", act === "delete" ? "btn dgr" : "btn", act === "delete" ? "Delete" : "Edit");
    b.type = "button";
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
  top.appendChild(credChip(entry.status));
  art.append(top, credActions(entry, loadCredentials));
  const bd = credEl("div", "ent-body");
  if (entry.error) bd.appendChild(credEl("p", "ent-err", entry.error));
  for (const n of body) bd.appendChild(n);
  art.appendChild(bd);
  return art;
}

function credAWSCard(e) {
  const verdict = baseText(e.base);
  const meta = [["Account", e.account_id], ["Role", credRoleName(e.role_arn)], ["Region", e.region]];
  if (e.source_profile) meta.push(["Profile", e.source_profile]);
  if (e.elevate) meta.push(["Elevate", credRoleName(e.elevate.role_arn) + (e.elevate.max_minutes ? " (" + e.elevate.max_minutes + " min)" : "")]);
  meta.push(["Used by", usedByText(e.used_by)]);
  const body = [credMeta(meta)];
  if (e.status === "ok") {
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
  return credCard(e, e.name, e.label, body, { danger: e.status === "ok" && verdict.cls === "bad" });
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

function credValueRow(v) {
  const row = credEl("div", "sv");
  const id = credEl("span", "id", v.id);
  const upd = credEl("span");
  upd.append(credEl("span", "k", "Updated"), document.createTextNode(valueUpdatedText(v.updated)));
  const refs = credEl("span", "refs" + (v.referenced_by && v.referenced_by.length ? "" : " none"));
  refs.append(credEl("span", "k", "Referenced by"), document.createTextNode(valueRefsText(v.referenced_by)));
  const acts = credEl("span", "acts");
  row.append(id, upd, refs, acts);
  return row;
}

function credValuesBody(entries) {
  const wrap = credEl("div");
  const head = credEl("div", "sv head");
  for (const t of ["Id", "Updated", "Referenced by", ""]) head.appendChild(credEl("span", null, t));
  wrap.appendChild(head);
  for (const v of entries) wrap.appendChild(credValueRow(v));
  return wrap;
}

function credLegacyBody(entries) {
  const wrap = credEl("div");
  wrap.appendChild(credEl("p", "hint", "Defined in config.json. Edit the file to change them, or move the value into the store."));
  for (const l of entries) {
    const row = credEl("div", "leg");
    row.append(credEl("b", null, l.name), credChip(l.status));
    wrap.appendChild(row);
  }
  return wrap;
}

const CRED_EMPTY = {
  aws: "No AWS accounts yet. Add one and the agent gets short-lived credentials for its role, never your profile.",
  github: "No GitHub Apps yet.",
  values: "No stored values.",
};

function credSection(g) {
  const sec = credEl("section", "sec");
  sec.setAttribute("aria-label", g.title);
  const h = credEl("div", "sec-h");
  const title = credEl("span", "grow", g.title + " ");
  title.appendChild(credEl("span", "count", String(g.entries.length)));
  h.appendChild(title);
  const kind = g.key === "aws" ? "aws" : g.key === "github" ? "github" : g.key === "values" ? "value" : g.key.startsWith("token:") ? "token" : "";
  const add = kind && credActionHandlers[kind] && credActionHandlers[kind].add;
  if (typeof add === "function") {
    const b = credEl("button", "btn" + (g.key === "aws" ? " pri" : ""), g.key === "aws" ? "Add AWS accounts" : g.key === "values" ? "Set a value" : g.key === "github" ? "Add GitHub App" : "Add token");
    b.type = "button";
    b.onclick = () => add(g, loadCredentials);
    h.appendChild(b);
  }
  sec.appendChild(h);

  if (!g.entries.length) {
    sec.appendChild(credEl("p", "hint", CRED_EMPTY[g.key] || ""));
    return sec;
  }
  if (g.key === "values") { sec.appendChild(credValuesBody(g.entries)); return sec; }
  if (g.key === "legacy") { sec.appendChild(credLegacyBody(g.entries)); return sec; }
  const stack = credEl("div", "stack");
  for (const e of g.entries) {
    stack.appendChild(g.key === "aws" ? credAWSCard(e) : g.key === "github" ? credAppCard(e) : credTokenCard(e));
  }
  sec.appendChild(stack);
  return sec;
}

// credStrip: the encrypted store's status, unlock and lock, folded into the top
// of the tab (it used to be a tab of its own).
function credStrip(view) {
  const st = view.store || {};
  const strip = credEl("div", "strip");
  const locked = st.initialized && st.locked && st.platform !== "darwin";
  if (!st.initialized) {
    strip.appendChild(credEl("span", "st notset", "Not set up"));
    strip.appendChild(credEl("div", "txt", "There is no encrypted store yet. Without one, tokens can only come from the daemon's environment or a file."));
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
  } else if (st.initialized && st.platform !== "darwin") {
    const b = credEl("button", "btn", "Lock now");
    b.type = "button";
    b.id = "store-lock";
    // Reload only after a lock that worked: a repaint rebuilds #store-err
    // empty, which would hide why the store is still unlocked.
    b.onclick = async () => { if (await lockStore()) loadCredentials(); };
    strip.appendChild(b);
  }
  const wrap = credEl("div", "stack");
  wrap.appendChild(strip);
  const pt = typeof storePlaintextText === "function" && storeState ? storePlaintextText(storeState.plaintext_copies) : "";
  if (pt) wrap.appendChild(credEl("p", "hint warnline", pt));
  wrap.appendChild(credEl("p", "hint", STORE_HONEST_STATEMENT));
  const err = credEl("div", "dlg-err");
  err.id = "store-err";
  wrap.appendChild(err);
  return wrap;
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
  credView = view;
  // The Add / Edit view owns the body until it is left; it reads credView when it needs it.
  if (awsFlow) return;
  if (typeof storeUnlockFocused === "function" && storeUnlockFocused()) {
    // Held, not dropped: repaint as soon as the field lets go.
    credRepaintHeld = true;
    return;
  }
  credRepaintHeld = false;
  credWireHeld(host);
  const nodes = [credStrip(view), ...credBanners(view)];
  for (const g of credGroups(view)) nodes.push(credSection(g));
  host.replaceChildren(...nodes);
}

// credWireHeld: once, repaint a held view when focus leaves the body.
function credWireHeld(host) {
  if (credHeldWired) return;
  credHeldWired = true;
  host.addEventListener("focusout", () => {
    setTimeout(() => { if (credRepaintHeld && credView) renderCredentials(credView); }, 0);
  });
}

function setCredErr(msg) {
  const el = document.getElementById("creds-err");
  if (el) el.textContent = msg || "";
}

async function loadCredentials() {
  setCredErr("");
  // Re-selecting the tab hides the Add / Edit footer; the view is still there.
  if (awsFlow) awsShowFoot(true);
  // The store strip also reads GET /api/store (plaintext copies, value count).
  if (typeof refreshStore === "function") await refreshStore();
  let view;
  try {
    view = await api("/api/credentials");
  } catch (e) {
    setCredErr("Could not load credentials: " + e.message);
    return;
  }
  renderCredentials(view);
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

function awsBtn(label, cls, onclick) {
  const b = awsEl("button", "btn" + (cls ? " " + cls : ""), label);
  b.type = "button";
  b.onclick = onclick;
  return b;
}

function awsShowFoot(on) {
  const add = document.getElementById("foot-creds-add");
  const base = document.getElementById("foot-creds");
  if (add) add.hidden = !on;
  if (base) base.hidden = on;
}

function awsOpenAdd() {
  awsFlow = { mode: "add", rows: [], profiles: [], selected: new Set(), sso: {}, timers: new Set(), saving: false, msg: "", loadErr: "" };
  awsPaintFlow();
  awsLoadProfiles(awsFlow);
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
  row.orig = { source_profile: row.source_profile, role_arn: row.role_arn };
  awsFlow = { mode: "edit", rows: [row], profiles: [], selected: new Set(), sso: {}, timers: new Set(), saving: false, msg: "", loadErr: "" };
  awsPaintFlow();
}

function awsNewRow(base, extra) {
  return {
    ...base, ...extra, id: ++awsRowSeq, description: "", egress: "", s3: "", elevRole: "", elevMinutes: "60", elevIAM: false,
    assume: null, verdict: null, actions: [], reason: "", reasonText: "", acked: false,
    busy: "", err: "", roles: null, rolesNote: "", signIn: "", el: null, res: null, rolesBox: null, roleInput: null,
  };
}

// awsRowState: rowSaveState with the edit rule filled in (an edit that leaves
// the role and its signing profile alone needs no check).
function awsRowState(row) {
  const unchanged = row.orig ? row.source_profile === row.orig.source_profile && row.role_arn === row.orig.role_arn : undefined;
  return rowSaveState({ assume: row.assume, verdict: row.verdict, acked: row.acked, unchanged });
}

async function awsExit() {
  const f = awsFlow;
  if (f) for (const t of f.timers) clearInterval(t);
  awsFlow = null;
  awsShowFoot(false);
  await loadCredentials();
}

function awsPaintFlow() {
  const host = document.getElementById("creds-body");
  const f = awsFlow;
  if (!host || !f) return;
  const edit = f.mode === "edit";
  const crumb = awsEl("div", "crumb");
  const back = awsEl("a", "lnk", "Credentials");
  back.tabIndex = 0;
  back.setAttribute("role", "button");
  back.onclick = () => awsExit();
  back.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); awsExit(); } };
  crumb.append(back, document.createTextNode(" / "), awsEl("b", null, edit ? "Edit " + f.rows[0].name : "Add AWS accounts"));
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
  rsec.append(rh, f.rowsHost, f.rowsEmpty);
  nodes.push(rsec);
  f.msgBox = awsEl("div", "hint");
  f.msgBox.setAttribute("role", "status");
  // The footer's own note is hidden on a phone (the footer is one row of buttons), so the same words sit here.
  f.noteBox = awsEl("div", "hint awsnote");
  nodes.push(f.msgBox, f.noteBox);
  host.replaceChildren(...nodes);
  for (const r of f.rows) f.rowsHost.appendChild(awsBuildRow(r));
  awsPaintProfiles();
  awsPaintFoot();
  awsShowFoot(true);
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
  inp.oninput = () => {
    row[key] = inp.value;
    if (o.invalidates) awsInvalidate(row);
    awsPaintFoot();
  };
  col.append(lab, inp);
  if (o.hint) col.appendChild(awsEl("p", "hint", o.hint));
  return { col, inp };
}

// awsInvalidate: the role, its signing profile or the region changed, so the
// earlier check no longer describes this row.
function awsInvalidate(row) {
  row.assume = null; row.verdict = null; row.actions = []; row.reason = ""; row.reasonText = ""; row.acked = false;
  row.err = ""; row.signIn = "";
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
  const rcol = awsEl("div", "fcol full");
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

  grid.appendChild(awsField(row, "source_profile", "Source profile", { readonly: !edit, invalidates: true }).col);
  grid.appendChild(awsField(row, "region", "Region", { invalidates: true, placeholder: "ap-southeast-1" }).col);
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
  det.appendChild(awsEl("summary", null, "Advanced: egress hosts, S3 read, elevate"));
  det.open = !!(row.egress || row.s3 || row.elevRole);
  const adv = awsEl("div", "frow");
  adv.appendChild(awsField(row, "egress", "Extra hosts the agent may reach", { area: true, full: true, placeholder: "one per line, e.g. *.example.com" }).col);
  adv.appendChild(awsField(row, "s3", "S3 buckets the agent may read", { area: true, full: true, placeholder: "one per line" }).col);
  adv.appendChild(awsField(row, "elevRole", "Elevate role ARN (optional)", { full: true, placeholder: "arn:aws:iam::<account>:role/<write role>" }).col);
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
  icb.onchange = () => { row.elevIAM = icb.checked; };
  ilab.append(icb, document.createTextNode(" Elevate may change IAM"));
  icol.appendChild(ilab);
  adv.appendChild(icol);
  det.appendChild(adv);
  art.appendChild(det);

  awsPaintRoles(row);
  awsPaintRes(row);
  return art;
}

function awsPaintRoles(row) {
  if (!row.rolesBox) return;
  const nodes = [];
  if (row.roles && row.roles.length) {
    const grp = awsEl("div", "roles");
    grp.setAttribute("role", "radiogroup");
    grp.setAttribute("aria-label", "Roles found for " + row.source_profile);
    for (const r of row.roles) {
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
      grp.appendChild(lab);
    }
    nodes.push(grp);
  }
  if (row.rolesNote) nodes.push(awsEl("p", "hint", row.rolesNote));
  row.rolesBox.replaceChildren(...nodes);
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
  if (row.assume) {
    const v = awsCheckView({ assume: row.assume, verdict: row.verdict, actions: row.actions, reason: row.reason, reason_text: row.reasonText });
    if (v.assume === "ok") {
      nodes.push(awsLine("ok", "✓", v.text));
      nodes.push(awsVerdictNode(row, v));
    } else {
      const l = awsEl("div", "l");
      l.appendChild(awsEl("span", "bdg " + (v.tone === "bad" ? "prod" : "warnb"), v.badge));
      l.appendChild(awsEl("span", null, v.text));
      nodes.push(l);
      if (v.signIn) needSignIn = true;
    }
  }
  if (needSignIn) nodes.push(awsSignInNode(row));
  row.res.replaceChildren(...nodes);
  if (row.el) row.el.classList.toggle("danger", row.assume === "ok" && row.verdict === "writes");
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

function awsSSOPoll(f, prof) {
  const started = Date.now();
  const timer = setInterval(async () => {
    if (awsFlow !== f || Date.now() - started > AWS_SSO_POLL_MAX) { clearInterval(timer); f.timers.delete(timer); return; }
    let st;
    try {
      st = await api("/api/credentials/aws/sso-login?profile=" + encodeURIComponent(prof));
    } catch (e) {
      if (awsFlow !== f) return;
      clearInterval(timer); f.timers.delete(timer);
      f.sso[prof] = { state: "gone", msg: e.status === 404 ? "No sign-in is in progress any more. Press Sign in to start another." : awsErrText(e) };
      awsRepaintProfileRows(prof);
      return;
    }
    if (awsFlow !== f) return;
    f.sso[prof] = { st };
    awsRepaintProfileRows(prof);
    if (st.state === "ok" || st.state === "failed") {
      clearInterval(timer); f.timers.delete(timer);
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
    if (res.sso_expired) row.signIn = "roles";
    else if (res.access_denied) row.rolesNote = "This profile can't list roles; type the role name";
    else if (res.failed) row.rolesNote = "Could not list the roles. Type the role ARN yourself.";
    else if (!res.roles || !res.roles.length) row.rolesNote = "No roles found for this profile. Type the role ARN yourself.";
    else { row.roles = res.roles; row.rolesNote = "Or type the role ARN yourself if the list does not show it."; }
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
  if (!AWS_ROLE_RE.test(row.role_arn)) {
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
    if (row.assume === "ok" || row.busy) continue;
    // A rate limit or a missing signer ends the run; the rows left stay as they were.
    if (!(await awsCheckRow(row)) && row.err && /Too many|no AWS signer/.test(row.err)) break;
    if (awsFlow !== f) return;
  }
}

function awsPaintFoot() {
  const f = awsFlow;
  if (!f) return;
  const sv = document.getElementById("aws-save");
  const ca = document.getElementById("aws-check-all");
  const note = document.getElementById("aws-foot-note");
  if (!sv || !ca || !note) return;
  const states = f.rows.map(awsRowState);
  const waiting = states.filter((s) => s.needsAck).length;
  const ready = states.filter((s) => s.canSave).length;
  const unchecked = f.rows.filter((r, i) => !states[i].canSave && !states[i].needsAck).length;
  const edit = f.mode === "edit";
  ca.textContent = edit ? "Check" : "Check all";
  ca.disabled = !f.rows.length || f.saving;
  sv.textContent = edit ? "Save" : "Save all";
  const off = f.saving || ready === 0 || waiting > 0;
  sv.disabled = off;
  sv.classList.toggle("off", off);
  let text = "";
  if (waiting) text = waiting + (waiting === 1 ? " row is" : " rows are") + " waiting for “I understand”.";
  else if (f.rows.length && !edit) text = ready + " of " + f.rows.length + " ready to save." + (ready && unchecked ? " Rows that did not pass a check are left as they are." : "");
  else if (edit && !ready) text = "Check the new role before saving.";
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
      if (f.mode === "edit") await api("/api/credentials/aws/" + encodeURIComponent(row.name), { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      else await awsPost("/api/credentials/aws", body);
      if (awsFlow !== f) return;
      saved++;
      row.busy = "";
      if (f.mode === "add") {
        f.selected.delete(row.pname);
        row.el.remove();
        f.rows.splice(f.rows.indexOf(row), 1);
      }
    } catch (e) {
      if (awsFlow !== f) return;
      row.busy = "";
      const v = e.body && e.body.verdict;
      if (e.status === 409 && e.code === "needs_ack") {
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
}

// awsDelete: an inline confirmation in the card (no browser dialog), then DELETE.
function awsDelete(entry, reload, btn) {
  const card = btn && btn.closest ? btn.closest(".ent") : null;
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
  btns.append(del, awsBtn("Cancel", "", () => box.remove()));
  box.append(err, btns);
  card.appendChild(box);
}

(function awsWire() {
  if (typeof document === "undefined") return;
  credActionHandlers.aws = { add: () => awsOpenAdd(), edit: (entry) => awsOpenEdit(entry), delete: awsDelete };
  const on = (id, fn) => { const e = document.getElementById(id); if (e) e.onclick = fn; };
  on("aws-check-all", () => awsCheckAll());
  on("aws-save", () => awsSaveAll());
  on("aws-cancel", () => awsExit());
})();
