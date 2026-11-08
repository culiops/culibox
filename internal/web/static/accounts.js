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

// acctOptionLabel: the spawn select's text for one account. A reading older than
// ACCT_STALE_SEC (or with no timestamp) carries its age, so a stale reading is
// never mistaken for a current one. The age belongs to each window's own reading
// (acctBestLimits can mix a fresh five-hour number with a two-day-old weekly
// one): one trailing note when every live window is equally old, else per window.
function acctOptionLabel(name, limits, nowSec) {
  limits = acctBestLimits(limits, nowSec);
  const wins = [];
  if (limits && limits.five_hour) wins.push(["5h", limits.five_hour]);
  if (limits && limits.seven_day) wins.push(["wk", limits.seven_day]);
  if (!wins.length) return name + " · no quota data";
  // A window past its reset shows no number, so it carries no age either.
  const notes = wins.map(([, w]) => (acctWinReset(w, nowSec) ? "" : acctAgeNote(acctWinAge(w, limits, nowSec))));
  const live = notes.filter((_, i) => !acctWinReset(wins[i][1], nowSec));
  if (live.length && live.every((n) => n && n === live[0])) {
    return name + " · " + wins.map(([l, w]) => acctWinText(l, w, nowSec)).join(" · ") + " (" + live[0] + ")";
  }
  return name + " · " + wins.map(([l, w], i) => acctWinText(l, w, nowSec) + (notes[i] ? " (" + notes[i] + ")" : "")).join(" · ");
}

// acctWinAge: the age of one window's reading. acctBestLimits stamps each window
// with its own updated_at; a window without one (a legacy snapshot) takes the
// snapshot's.
function acctWinAge(win, limits, nowSec) {
  const at = win && win.updated_at !== undefined ? win.updated_at : limits && limits.updated_at;
  return acctLimitsAge(at, nowSec);
}

// acctWinReset: the window's reset time has passed (see meterModel).
function acctWinReset(win, nowSec) {
  return win.resets_at > 0 && win.resets_at <= nowSec;
}

// acctAgeNote: the visible age text for a stale reading ("7h old", "no data"),
// "" for a fresh one. A tooltip alone is not enough: the number must not be
// mistaken for a current one.
function acctAgeNote(age) {
  return age && age.stale ? age.text.replace(" ago", " old") : "";
}

// acctWinText: one window's text in the spawn select. A window past its reset
// time has rolled over, so its old percentage is not shown (see meterModel).
function acctWinText(label, win, nowSec) {
  if (acctWinReset(win, nowSec)) return label + " reset";
  return label + " " + Math.round(win.used_percentage) + "%";
}

// acctBestLimits: the one reading of an account's quota that every meter shows
// (header, mobile/side band, rail bars, spawn picker, Settings). Whether the
// quota is per account or per model bucket is not known, and the snapshot's top
// level is only whichever session wrote last, so two sessions on one token made
// the meters flip. Each window therefore shows the HIGHEST used_percentage among
// the top level and every limits.by_model bucket whose window has not reset yet,
// with that reading's own resets_at. Within one window usage only rises, so per
// account the highest is the freshest, and per model it is the most constraining
// limit: it never understates. updated_at is the oldest reading shown. A window
// no reading of which is live is returned as the top level has it (or the
// freshest bucket's), so meterModel renders it as reset.
//
// The readings the daemon remembered (limits.seen, one per window and resets_at)
// join the candidates: a host file written by sessions on different models only
// ever holds the last writer's numbers, and by_model is absent from it.
function acctBestLimits(limits, nowSec) {
  if (!limits) return limits;
  const byModel = limits.by_model && typeof limits.by_model === "object" ? limits.by_model : null;
  const seen = limits.seen && typeof limits.seen === "object" ? limits.seen : null;
  if (!byModel && !seen) return limits;
  const pick = (key) => {
    const cands = [];
    const add = (w, at) => {
      if (w && typeof w.used_percentage === "number") cands.push({ w, at: Number(at) || 0 });
    };
    add(limits[key], limits.updated_at);
    for (const b of Object.values(byModel || {})) if (b && typeof b === "object") add(b[key], b.updated_at);
    for (const r of Array.isArray(seen && seen[key]) ? seen[key] : []) if (r && typeof r === "object") add(r, r.updated_at);
    const live = cands.filter((c) => !(c.w.resets_at > 0 && c.w.resets_at <= nowSec));
    if (live.length) return live.reduce((m, c) => (c.w.used_percentage > m.w.used_percentage || (c.w.used_percentage === m.w.used_percentage && c.at > m.at) ? c : m));
    if (limits[key]) return cands.find((c) => c.w === limits[key]) || null;
    return cands.length ? cands.reduce((m, c) => (c.at > m.at ? c : m)) : null;
  };
  const out = {};
  const ats = [];
  for (const key of ["five_hour", "seven_day"]) {
    const c = pick(key);
    if (!c) continue;
    out[key] = Object.assign({}, c.w, { updated_at: c.at }); // each window keeps its own age
    ats.push(c.at);
  }
  out.updated_at = ats.length ? Math.min(...ats) : limits.updated_at; // the oldest shown
  return out;
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
// claude profile, else any claude profile not pinned to an account. The spawn
// is sent with login:true, so the server drops the profile's own auth secrets
// rather than refusing them; any other refusal is shown by the caller.
function acctLoginProfile(profiles) {
  const ps = profiles || [];
  return ps.find((x) => x.claude && x.name === "claude") || ps.find((x) => x.claude && !x.account) || null;
}

function acctLoginText(state) {
  if (!state) return "login unknown";
  return {
    token: "token", token_locked: "token (store locked)", token_unavailable: "token (Keychain unavailable)",
    token_missing: "token not found",
    logged_in: "logged in", unknown: "login unknown",
  }[state] || state;
}

// acctTokenId mirrors account.TokenID (Go): the store id an account's pasted
// token lives under. The server reads only this id straight from the store, so
// the client never chooses another one.
function acctTokenId(name) {
  return "ACCT_" + String(name || "").replace(/-/g, "_").toUpperCase() + "_TOKEN";
}

// acctTokenOwned: the account's token is the one this tab stored (its own id),
// so Remove may delete the value too. A token_secret set by hand names a value
// something else may read; Remove only unlinks it.
function acctTokenOwned(a) {
  return !!(a && a.name && a.token_secret && a.token_secret === acctTokenId(a.name));
}

// acctTokenStateText: the token line's sentence for a linked token.
function acctTokenStateText(state, id) {
  if (state === "token") return "Token saved";
  if (state === "token_locked") return "Token saved · unlock the store to start sessions";
  if (state === "token_unavailable") return "Token saved · the macOS Keychain did not give the store key";
  return "Token not found" + (id ? " (" + id + ")" : "");
}

// acctCanToken: the account's provider signs in with a pasted token and an
// interactive login (claude). Any other stored provider is listed and deletable only.
function acctCanToken(a) {
  return !!a && a.provider === "claude";
}

// acctTokenMode: what the token line of an account row shows. "set": a token
// is linked (Replace / Remove). "paste": the paste field. "store": the store
// must be created first. A locked store still takes a new value.
function acctTokenMode(a, store) {
  if (a && a.token_secret) return "set";
  return store && store.initialized ? "paste" : "store";
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

// awsBadgeEl is the AWS binding chip, drawn beside acctBadgeEl on every surface
// that names a session (sidebar row, mobile sheet headers, terminal tab). It
// lives here only because this is the file that already builds the other badge;
// the verdict (text, prod, title) is sessionrow.js's awsBadge. The label is
// operator-typed, so it goes in through textContent.
function awsBadgeEl(s) {
  const ab = awsBadge(s);
  if (!ab) return null;
  const b = document.createElement("span");
  // write is a separate axis from prod: an open write window is amber, and a
  // production binding that is writing is red with the amber ring. rolew (dashed
  // amber) is the base role being able to write with no window open.
  b.className = "bdg aws" + (ab.prod ? " prod" : "") + (ab.write ? " write" : "") + (ab.roleWrite ? " rolew" : "") + (hasConns(s) ? " conns" : "");
  b.title = ab.title;
  const parts = badgeParts(ab);
  const lab = document.createElement("span");
  lab.className = "awslab";
  lab.textContent = parts.label;
  b.appendChild(lab);
  // The write state is its own element so a long label ellipsizes first and the
  // word "write" is never the part that is cut.
  if (parts.write) {
    const w = document.createElement("span");
    w.className = "awsw";
    w.textContent = " · write";
    b.appendChild(w);
  } else if (parts.roleWrite) {
    const w = document.createElement("span");
    w.className = "awsw";
    w.textContent = ROLE_WRITE_SUFFIX;
    b.appendChild(w);
  }
  return b;
}

async function loadAccounts() {
  const list = document.getElementById("acct-list");
  if (!list) return;
  setAcctErr("");
  // The token line depends on whether the store exists: read it fresh, in
  // parallel with the list.
  const [, ok] = await Promise.all([typeof refreshStore === "function" ? refreshStore() : null, fetchAccounts()]);
  if (!ok) {
    setAcctErr("Could not load agent accounts.");
    return;
  }
  const lims = acctLimitsMap(lastRateLimits);
  const now = Math.floor(Date.now() / 1000);
  list.replaceChildren();
  for (const a of accountsCache) list.appendChild(acctRow(a, acctBestLimits(lims[a.name], now), now));
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
  ag.textContent = n ? "updated " + age.text : "no quota data yet: it appears after a session runs under this account";
  meters.appendChild(ag);
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
    // Only a claude account signs in, so any other stored provider gets no
    // Log in button.
    if (acctCanToken(a)) {
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
    if (acctCanToken(a)) li.appendChild(acctTokenBox(a, false));
  }
  return li;
}

// acctPageMode: Settings is the desktop page (#settings-modal.as-page), not the
// modal the phone uses.
function acctPageMode() {
  const m = document.getElementById("settings-modal");
  return !!m && m.classList.contains("as-page");
}

// acctDeskNote(text): on the desktop page the paragraph at the top of Agent accounts is split. Its first sentence is
// the page head's description (settingshead.js accountsLead) and the rest, `text`, stays here as a note of its own.
// index.html's paragraph is shared with the phone, which shows all four sentences as written, so the note is a
// sibling built after it and the paragraph is never rewritten; the note is hidden outside the page (app.css) and
// the paragraph is hidden on it (desktop.css). settingshead.js paints it with the head.
function acctDeskNote(text) {
  const panel = document.getElementById("accounts");
  const src = panel ? panel.querySelector(".hint") : null;
  if (!src) return;
  let note = document.getElementById("acct-desk-note");
  if (!note) {
    if (!text) return;
    note = document.createElement("p");
    note.id = "acct-desk-note";
    note.className = "hint";
    src.after(note);
  }
  note.textContent = text || "";
  note.hidden = !text;
}

function acctBtn(text, cls, onclick) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = cls;
  b.textContent = text;
  b.onclick = onclick;
  return b;
}

// acctTokenBox: the row's token line. `editing` shows the paste field on an
// account that already has a token (Replace). The field is write-only: it
// is never filled from the server and is emptied after every attempt.
function acctTokenBox(a, editing) {
  const box = document.createElement("div");
  box.className = "acct-token";
  const store = typeof storeState === "undefined" ? null : storeState;
  const mode = editing ? (store && store.initialized ? "paste" : "store") : acctTokenMode(a, store);
  if (mode === "set") {
    const t = document.createElement("span");
    t.className = "acct-token-state";
    t.textContent = acctTokenStateText(a.login_state, a.token_secret);
    box.append(t,
      acctBtn("Replace token", "btn", () => box.replaceWith(acctTokenBox(a, true))),
      acctBtn("Remove token", "btn dgr", () => acctTokenRemove(a)));
    return box;
  }
  if (mode === "store") {
    const p = document.createElement("span");
    p.className = "hint";
    p.textContent = "To use a token from claude setup-token, create the encrypted store first.";
    box.append(p, acctBtn("Open Credentials", "btn", () => {
      if (typeof selectSettingsTab === "function") selectSettingsTab("creds");
    }));
    if (editing) box.appendChild(acctBtn("Cancel", "btn", () => box.replaceWith(acctTokenBox(a, false))));
    return box;
  }
  const inp = document.createElement("input");
  inp.type = "password";
  inp.autocomplete = "new-password";
  inp.setAttribute("autocorrect", "off");
  inp.spellcheck = false;
  inp.className = "fld";
  inp.name = "agent-box-acct-token";
  inp.placeholder = "Paste a setup-token";
  inp.title = "A token printed by claude setup-token";
  inp.setAttribute("aria-label", "Token for agent account " + a.name);
  // A row action, one per account without a token: on the desktop page a
  // secondary, so the view keeps one primary (F11). The modal keeps it primary.
  const save = acctBtn("Save token", acctPageMode() ? "btn" : "btn pri", () => acctTokenSave(a, inp, save));
  inp.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); save.click(); } };
  box.append(inp, save);
  if (editing) box.appendChild(acctBtn("Cancel", "btn", () => { inp.value = ""; box.replaceWith(acctTokenBox(a, false)); }));
  return box;
}

// acctTokenSave stores the pasted token under the account's own id, then links
// it. The value is only ever in the field and the one request body.
async function acctTokenSave(a, inp, btn) {
  setAcctErr("");
  setAcctNote("");
  const value = inp.value.trim();
  if (!value) { setAcctErr("Paste the token first."); return; }
  const id = acctTokenId(a.name);
  btn.disabled = true;
  try {
    await api("/api/store/values/" + encodeURIComponent(id), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ value }),
    });
  } catch (e) {
    btn.disabled = false;
    setAcctErr("Saving the token failed: " + (e.code === "store_missing"
      ? "the encrypted store is not created yet. Create it on the Credentials tab."
      : e.message));
    return;
  } finally {
    inp.value = "";
  }
  if (a.token_secret !== id) {
    try {
      await api("/api/accounts/" + encodeURIComponent(a.name), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(acctPutBody(a, { token_secret: id })),
      });
    } catch (e) {
      await loadAccounts();
      setAcctErr("The token is stored but could not be linked to \"" + a.name + "\": " + e.message);
      return;
    }
  }
  await loadAccounts(); // also re-reads the store, which is in use now
  setAcctNote("Token saved for \"" + a.name + "\". New sessions under it use the token. The quota meter can keep showing the old token's numbers while its sessions run, until that window resets.");
}

// acctTokenRemove unlinks the token and, when it is the account's own value,
// deletes it from the store. Running sessions keep the token they started with.
async function acctTokenRemove(a) {
  setAcctErr("");
  setAcctNote("");
  const owned = acctTokenOwned(a);
  const ok = await askConfirm({
    title: "Remove token",
    body: "Remove the token from \"" + a.name + "\"" + (owned ? " and delete it from the encrypted store" : "") +
      ". Running sessions keep working; new sessions under this account need a token or a login.",
    confirmLabel: "Remove",
    danger: true,
  });
  if (!ok) return;
  try {
    await api("/api/accounts/" + encodeURIComponent(a.name), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(acctPutBody(a, { token_secret: "" })),
    });
  } catch (e) {
    await loadAccounts();
    setAcctErr("Remove failed: " + e.message);
    return;
  }
  if (owned) {
    try {
      await api("/api/store/values/" + encodeURIComponent(a.token_secret), { method: "DELETE" });
    } catch (e) {
      await loadAccounts();
      setAcctErr("The token is unlinked, but its stored value " + a.token_secret + " could not be deleted: " + e.message);
      return;
    }
  }
  await loadAccounts();
  setAcctNote("Token removed from \"" + a.name + "\".");
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
  const name = await askPrompt({
    title: "New agent account",
    body: "A separate claude login. Lowercase letters, digits and '-'. Example: work",
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
      body: JSON.stringify({ name: name, provider: "claude" }),
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
  const acct = accountsCache.find((x) => x.name === name);
  const owned = acctTokenOwned(acct);
  const ok = await askConfirm({
    title: "Delete agent account",
    body: "Unregister \"" + name + "\". Its folder (login + transcripts) stays on disk; you can delete it by hand." +
      (owned ? " Its saved token is deleted from the encrypted store." : ""),
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
  // The account's own token goes with it, so an account created later under
  // the same name never inherits it. The account no longer reads it, so the
  // store allows the delete.
  let tokenLeft = "";
  if (owned) {
    try {
      await api("/api/store/values/" + encodeURIComponent(acct.token_secret), { method: "DELETE" });
    } catch (e) {
      tokenLeft = " Its stored token " + acct.token_secret + " could not be deleted (" + e.message + "); remove it on the Credentials tab.";
    }
  }
  await loadAccounts();
  setAcctNote("Deleted \"" + name + "\". Its folder is left at " + ((r && r.left_on_disk) || "its old path") + "." + tokenLeft);
}

// acctLogin starts a claude login-mode session under the account, on the host
// tier (a box keeps its own config dir, so a login there would not land in
// the account); the user runs /login in it. login:true makes the launcher drop
// the profile's personal auth so only the account's CLAUDE_CONFIG_DIR applies.
// A refusal stays in the dialog rather than vanishing.
async function acctLogin(name) {
  setAcctErr("");
  setAcctNote("");
  const p = acctLoginProfile(profiles);
  if (!p) { setAcctErr("Log in needs a claude profile with no agent account set."); return; }
  let started;
  try {
    started = await spawnSession(spawnPayload({
      agent: p.name, account: name, runtime: "host", login: true, name: "login: " + name,
      cwd: "", options: {}, profile: p,
    }), { toast: false });
  } catch (e) {
    setAcctErr("Log in failed: " + e.message);
    return;
  }
  // null: the open profile page asked about unsaved changes and they were kept, so no session was started.
  if (!started) return;
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
    acctAgeNote, acctBestLimits, acctWinAge,
    acctChoices, acctPutBody, acctSpawnPick, acctLoginProfile, acctLoginText,
    acctTokenId, acctTokenOwned, acctTokenMode, acctTokenStateText, acctCanToken,
  };
}
