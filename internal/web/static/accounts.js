"use strict";
// Agent accounts: pure helpers (node-tested) + the Settings tab (DOM, below).
// Names are ACCT_/acct-prefixed: classic scripts share one global scope.

const ACCT_DEFAULT = "default";
const ACCT_STALE_SEC = 5 * 3600;

// acctLimitsMap accepts /api/meta.rate_limits in either shape: the per-account
// map, or the pre-accounts flat object (one poll can straddle a deploy).
function acctLimitsMap(rl) {
  if (!rl) return {};
  if (rl.five_hour || rl.seven_day || rl.updated_at) return { default: rl };
  return rl;
}

function acctFor(session) {
  return (session && session.account) || ACCT_DEFAULT;
}

function acctLimitsAge(updatedAt, nowSec) {
  if (!updatedAt) return { text: "no data", stale: true };
  const age = Math.max(0, nowSec - updatedAt);
  const stale = age > ACCT_STALE_SEC;
  if (age < 60) return { text: "just now", stale };
  if (age < 3600) return { text: Math.floor(age / 60) + "m ago", stale };
  return { text: Math.floor(age / 3600) + "h ago", stale };
}

function acctBadge(session) {
  const a = acctFor(session);
  if (a === ACCT_DEFAULT) return null;
  return { text: a, title: "Agent account: " + a };
}

// acctOptionLabel: the spawn select's text for one account. Quota numbers
// older than ACCT_STALE_SEC (or with no timestamp) carry their age, so a
// stale reading is never mistaken for a current one.
function acctOptionLabel(name, limits, nowSec) {
  const parts = [];
  if (limits && limits.five_hour) parts.push("5h " + Math.round(limits.five_hour.used_percentage) + "%");
  if (limits && limits.seven_day) parts.push("wk " + Math.round(limits.seven_day.used_percentage) + "%");
  if (!parts.length) return name + " · no quota data";
  let label = name + " · " + parts.join(" · ");
  const age = acctLimitsAge(limits.updated_at, nowSec);
  if (age.stale) label += " (" + age.text.replace(" ago", " old") + ")";
  return label;
}

// acctChoices: the names an account <select> offers, default first. `keep` is
// the value the select must be able to show; it is added when the list lacks
// it, so an empty or failed /api/accounts fetch can never quietly turn a
// pinned profile back into a default-account one on its next Save.
function acctChoices(list, keep) {
  const out = [ACCT_DEFAULT];
  for (const a of list || []) if (a && !a.default && a.name && !out.includes(a.name)) out.push(a.name);
  if (keep && !out.includes(keep)) out.push(keep);
  return out;
}

// acctPutBody: PUT /api/accounts/{name} replaces token_secret AND
// inherit_personal wholesale, so every update sends both, the untouched one
// copied from the row as last listed.
function acctPutBody(a, patch) {
  return Object.assign({ token_secret: (a && a.token_secret) || "", inherit_personal: !!(a && a.inherit_personal) }, patch || {});
}

// acctSpawnPick: the spawn select's starting value. A Duplicate's prefill
// (string, "" = the default account) wins over the profile's own account;
// either way the default is the explicit "default" (ruling C2), so a pick of
// it overrides a profile pinned elsewhere.
function acctSpawnPick(prefillAccount, profileAccount) {
  if (prefillAccount !== undefined) return prefillAccount || ACCT_DEFAULT;
  return profileAccount || ACCT_DEFAULT;
}

// acctLoginProfile: the profile "Log in" spawns under the account — the bare
// claude profile, else any claude profile not pinned to an account. The
// server still refuses one carrying its own auth secret; the caller shows that.
function acctLoginProfile(profiles) {
  const ps = profiles || [];
  return ps.find((x) => x.claude && x.name === "claude") || ps.find((x) => x.claude && !x.account) || null;
}

function acctLoginText(state) {
  if (!state) return "login unknown";
  return { token: "token", logged_in: "logged in", unknown: "login unknown" }[state] || state;
}

// ---- DOM (browser only) -----------------------------------------------------
// Declarations stay top-level so the other page scripts can see them (a
// function declared inside a block is block-scoped in strict mode); only the
// boot wiring at the foot is guarded, so node can still require this file.

let accountsCache = [];   // GET /api/accounts, as last fetched
let lastRateLimits = null; // /api/meta.rate_limits (per-account map), as last polled
let acctHeaderShown = null; // the account the header meters were last drawn for

// fetchAccounts refreshes accountsCache. A failed fetch keeps the previous
// list rather than wiping it (a transient error must not hide every account
// from the spawn form) and reports false.
async function fetchAccounts() {
  try {
    accountsCache = (await api("/api/accounts")) || [];
    return true;
  } catch (_) {
    return false;
  }
}

function setAcctErr(msg) {
  const el = document.getElementById("acct-err");
  if (el) el.textContent = msg || "";
}

// setAcctNote is the success line. Not a toast: one would paint beneath the
// Settings dialog's backdrop and never be seen.
function setAcctNote(msg) {
  const el = document.getElementById("acct-note");
  if (!el) return;
  el.textContent = msg || "";
  el.hidden = !msg;
}

// acctBadgeEl is the one account badge: sidebar row, mobile sheet header and
// terminal tab all draw it. Null for the default account (no badge at all).
// The full name is in the title; a long one is ellipsized (.bdg.acct).
function acctBadgeEl(s) {
  const ab = acctBadge(s);
  if (!ab) return null;
  const b = document.createElement("span");
  b.className = "bdg acct";
  b.textContent = ab.text;
  b.title = ab.title;
  return b;
}

async function loadAccounts() {
  const list = document.getElementById("acct-list");
  if (!list) return;
  setAcctErr("");
  if (!(await fetchAccounts())) {
    setAcctErr("Could not load agent accounts.");
    return;
  }
  const lims = acctLimitsMap(lastRateLimits);
  const now = Math.floor(Date.now() / 1000);
  list.replaceChildren();
  for (const a of accountsCache) list.appendChild(acctRow(a, lims[a.name], now));
}

function acctRow(a, limits, now) {
  const li = document.createElement("li");
  li.className = "acct-row";
  const head = document.createElement("div");
  head.className = "acct-head";
  const nm = document.createElement("b");
  nm.textContent = a.name;
  const meta = document.createElement("span");
  meta.className = "acct-meta";
  meta.textContent = a.provider + " · " + acctLoginText(a.login_state) + " · " + a.config_dir;
  head.append(nm, meta);
  li.appendChild(head);

  const meters = document.createElement("div");
  meters.className = "acct-meters";
  const n = renderRateLimitsInto(meters, limits || null);
  const age = acctLimitsAge(limits && limits.updated_at, now);
  const ag = document.createElement("span");
  ag.className = "acct-age";
  ag.textContent = n ? "updated " + age.text : "no quota data yet";
  meters.appendChild(ag);
  // Only numbers can be stale; "no quota data yet" is already the honest text.
  if (n && age.stale) meters.classList.add("stale");
  li.appendChild(meters);

  if (!a.default) {
    const row = document.createElement("div");
    row.className = "acct-actions";
    const inh = document.createElement("label");
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = !!a.inherit_personal;
    cb.onchange = () => acctUpdate(a, { inherit_personal: cb.checked });
    inh.append(cb, document.createTextNode(" inherit personal config (CLAUDE.md, skills, MCP)"));
    row.appendChild(inh);
    // Codex accounts are stored but cannot be spawned yet, so there is
    // nothing to log in with.
    if (a.provider === "claude") {
      const login = document.createElement("button");
      login.type = "button";
      login.className = "btn";
      login.textContent = "Log in";
      login.onclick = () => acctLogin(a.name);
      row.appendChild(login);
    }
    const del = document.createElement("button");
    del.type = "button";
    del.className = "btn dgr";
    del.textContent = "Delete…";
    del.setAttribute("aria-label", "Delete agent account " + a.name);
    del.onclick = () => acctDelete(a.name);
    row.appendChild(del);
    li.appendChild(row);
  }
  return li;
}

async function acctUpdate(a, patch) {
  setAcctErr("");
  setAcctNote("");
  try {
    await api("/api/accounts/" + encodeURIComponent(a.name), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(acctPutBody(a, patch)),
    });
  } catch (e) {
    await loadAccounts(); // repaint the checkbox from the server's truth first
    setAcctErr("Update failed: " + e.message);
    return;
  }
  loadAccounts();
}

async function acctNew() {
  setAcctErr("");
  setAcctNote("");
  const provSel = document.getElementById("acct-provider");
  const provider = (provSel && provSel.value) || "claude";
  const name = await askPrompt({
    title: "New agent account",
    body: "A separate " + provider + " login. Lowercase letters, digits and '-'. Example: work",
    label: "Name",
    placeholder: "work",
    confirmLabel: "Create",
  });
  if (name === null) return; // dismissed
  if (!name) { setAcctErr("A name is required."); return; }
  try {
    await api("/api/accounts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: name, provider: provider }),
    });
  } catch (e) {
    setAcctErr("Create failed: " + e.message);
    return;
  }
  loadAccounts();
}

async function acctDelete(name) {
  setAcctErr("");
  setAcctNote("");
  const ok = await askConfirm({
    title: "Delete agent account",
    body: "Unregister \"" + name + "\". Its folder (login + transcripts) stays on disk; you can delete it by hand.",
    confirmLabel: "Delete",
    danger: true,
  });
  if (!ok) return;
  let r;
  try {
    r = await api("/api/accounts/" + encodeURIComponent(name), { method: "DELETE" });
  } catch (e) {
    // 409 while a profile or a live session uses it: the server's message
    // says which, and it must stay on screen.
    setAcctErr("Delete failed: " + e.message);
    return;
  }
  await loadAccounts();
  setAcctNote("Deleted \"" + name + "\". Its folder is left at " + ((r && r.left_on_disk) || "its old path") + ".");
}

// acctLogin starts an ordinary claude session under the account, on the host
// tier (a box keeps its own config dir, so a login there would not land in
// the account); the user runs /login in it. No new endpoint: the launcher sets
// CLAUDE_CONFIG_DIR. A refusal (say, the profile carries its own auth secret)
// stays in the dialog rather than vanishing.
async function acctLogin(name) {
  setAcctErr("");
  setAcctNote("");
  const p = acctLoginProfile(profiles);
  if (!p) { setAcctErr("Log in needs a claude profile with no agent account set."); return; }
  try {
    await spawnSession(spawnPayload({
      agent: p.name, account: name, runtime: "host", name: "login: " + name,
      cwd: "", options: {}, profile: p,
    }), { toast: false });
  } catch (e) {
    setAcctErr("Log in failed: " + e.message);
    return;
  }
  closeDialog("settings-modal");
  showToast("Run /login in the new session");
}

// syncRateLimitsAccount redraws the header meters when the account they
// should show (the focused session's) differs from the one drawn. Called on
// every focus change (renderSteerRail) and every session poll, so a session
// list that arrives after the focus still lands the right account.
function syncRateLimitsAccount() {
  if (typeof renderRateLimits !== "function") return;
  if (headerAccount() !== acctHeaderShown) renderRateLimits(lastRateLimits);
}

// headerAccount: whose quota the header shows — the focused session's
// account, else the default.
function headerAccount() {
  const t = typeof focusedTerminal === "function" ? focusedTerminal() : null;
  const s = t ? (lastSessions || []).find((x) => x.id === t.id) : null;
  return acctFor(s);
}

function initAccounts() {
  const b = document.getElementById("acct-new");
  if (b) b.onclick = acctNew;
  // Warm the cache so the first New-session dialog can offer the accounts
  // without waiting on a fetch.
  fetchAccounts();
}

if (typeof document !== "undefined") {
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initAccounts);
  } else {
    initAccounts();
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    ACCT_DEFAULT, ACCT_STALE_SEC, acctLimitsMap, acctFor, acctLimitsAge, acctBadge, acctOptionLabel,
    acctChoices, acctPutBody, acctSpawnPick, acctLoginProfile, acctLoginText,
  };
}
