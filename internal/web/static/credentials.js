"use strict";

// The Settings "Credentials" tab: the encrypted-store strip, then one group per
// kind of credential (AWS, GitHub Apps, each token provider, stored values,
// legacy secrets), each entry with its status. Read-only in this file's first
// cut: the add/edit/delete flows hook in through credActionHandlers.
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

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    credGroups, statusText, statusClass, baseText, credWriteActions, usedByText, entryActions, isSeed,
    providerTitle, checkedText, credRoleName, valueRefsText, valueUpdatedText,
    CRED_D4, CRED_N3, CRED_FILE_BAD, CRED_D4_REMEDY,
  };
}

// --- DOM wiring (browser only) -------------------------------------------------

// The last GET /api/credentials, or null before it has answered. Never holds a
// secret: the read model carries references and states only.
let credView = null;

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
    b.onclick = () => h[act](entry, reload);
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
  if (typeof storeUnlockFocused === "function" && storeUnlockFocused()) return;
  const nodes = [credStrip(view), ...credBanners(view)];
  for (const g of credGroups(view)) nodes.push(credSection(g));
  host.replaceChildren(...nodes);
}

function setCredErr(msg) {
  const el = document.getElementById("creds-err");
  if (el) el.textContent = msg || "";
}

async function loadCredentials() {
  setCredErr("");
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
