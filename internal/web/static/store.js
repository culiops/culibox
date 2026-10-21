"use strict";

// The encrypted secret store's UI: the "Store locked" banner, the unlock form
// the banner, the needs-you card and the spawn dialog all share, and the
// Settings "Store" tab. The pure decisions at the top are node-tested
// (jstest/store.test.js); the DOM wiring below them is exercised in the browser.
//
// The passphrase is typed into one password field, read once, cleared at once,
// sent in a POST body and never anywhere else: not a URL, not the console, not
// a toast, not a DOM attribute.

// The exact approved statement of what the store does and does not protect
// against. Shown verbatim in Settings; do not paraphrase it.
const STORE_HONEST_STATEMENT =
  "Values are encrypted at rest, against disk snapshots and backups. While unlocked, any process running as your user — host-tier agents included — can read them. Boxed agents cannot.";

// storeBannerVisible: the global banner. Only a store that exists, is locked
// and is referenced by this instance's config can block anything; macOS keeps
// the key in the Keychain, so there is nothing to type.
function storeBannerVisible(st) {
  return !!(st && st.initialized && st.locked && st.in_use && st.platform !== "darwin");
}

// storeExpiryText: the Settings status line. nowMs is injected so it is testable.
function storeExpiryText(st, nowMs) {
  if (!st || !st.initialized) return "not set up";
  if (st.platform === "darwin") return "Keychain (macOS)";
  if (st.locked) return "locked";
  if (!st.expires_at) return "unlocked";
  const left = Date.parse(st.expires_at) - nowMs;
  if (!(left > 0)) return "locked";
  const mins = Math.floor(left / 60000);
  if (mins < 1) return "unlocked · locks in under a minute";
  const d = Math.floor(mins / 1440);
  const h = Math.floor((mins % 1440) / 60);
  const m = mins % 60;
  let span;
  if (d > 0) span = d + " d " + h + " h";
  else if (h > 0) span = h + " h " + m + " m";
  else span = m + " m";
  return "unlocked · locks in " + span;
}

// storePlaintextText: the Settings line naming values that exist both in the
// store and as a plaintext variable in the daemon's environment. "" for none.
function storePlaintextText(names) {
  const list = Array.isArray(names) ? names.filter(Boolean) : [];
  if (!list.length) return "";
  return "Plaintext copy still in the daemon environment: " + list.join(", ") + " — delete it there to get the protection";
}

// storeUnlockOutcome turns an unlock response into what the form shows.
// `text` is the server's error string (or the raw body when it was not JSON).
// 401 is ambiguous: the unlock handler's own answer is exactly "wrong
// passphrase"; authGuard's 401 (a signed-out page) is plain "unauthorized", and
// the cure for that is a reload, not a retry.
function storeUnlockOutcome(status, text, retryAfter) {
  void retryAfter; // the 429 text already carries the wait, rounded up to minutes
  const t = String(text || "");
  if (status === 204) return { ok: true, msg: "", signedOut: false };
  if (status === 401) {
    if (t === "wrong passphrase") return { ok: false, msg: "Wrong passphrase.", signedOut: false };
    return { ok: false, msg: "Signed out — reload this page to sign in again.", signedOut: true };
  }
  if (status === 429) return { ok: false, msg: t || "Too many wrong passphrases — try again later.", signedOut: false };
  if (status === 403) return { ok: false, msg: "Unlocking needs this page's signed-in browser session. Reload the page and try again.", signedOut: false };
  if (status === 0) return { ok: false, msg: "Could not unlock: no connection.", signedOut: false };
  if (t) return { ok: false, msg: t, signedOut: false };
  return { ok: false, msg: "Could not unlock the store (" + status + ").", signedOut: false };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { lockStore, renderUnlockForm, storeUnlockFocused, storeRecentMsg, STORE_HONEST_STATEMENT, storeBannerVisible, storeExpiryText, storePlaintextText, storeUnlockOutcome };
}

// --- DOM wiring (browser only) -------------------------------------------------

// The last GET /api/store, or null when there is no store (404) or it has not
// answered yet. Everything below paints from this.
let storeState = null;
// Unlock requests in flight. refreshSessions holds its list rebuild while any is
// pending: clicking Unlock moves focus off the field, and a poll that rebuilt
// the row mid-request would write the outcome into a detached node.
let storeInflight = 0;
// The last failed unlock's line and when it happened, so a form rebuilt right
// after the failure (a card row the poll replaced) shows it again.
let storeLastMsg = "";
let storeLastMsgAt = 0;
const STORE_MSG_TTL_MS = 15000;

// storeRecentMsg: the remembered failure line while it is fresh, "" after.
function storeRecentMsg(msg, at, nowMs) {
  return msg && nowMs - at >= 0 && nowMs - at < STORE_MSG_TTL_MS ? msg : "";
}

// storeUnlockFocused: whether refreshSessions must hold its list rebuild. Read
// from the DOM, not from focus/blur events: removing a focused element fires no
// blur in every browser, which would leave a flag stuck on and the list frozen.
function storeUnlockFocused() {
  if (storeInflight > 0) return true;
  const a = typeof document !== "undefined" ? document.activeElement : null;
  return !!(a && a.isConnected && a.classList && a.classList.contains("storepass"));
}

function storeEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

// renderUnlockForm fills `container` with the one unlock UI: a password field
// and an Unlock button, stacked on a narrow screen. onDone() runs after a 204.
// The banner, the needs-you card, the spawn dialog and Settings all call this.
//
// Deliberately NOT a <form>: the spawn dialog's slot sits inside <form id=spawn>,
// and a nested form's submit bubbles to the spawn handler (starting a spawn and
// detaching this UI), while Enter in a bare input would submit the outer form.
// So: a div, a type=button button, and Enter handled on keydown with the event
// stopped.
function renderUnlockForm(container, onDone) {
  const form = storeEl("div", "storeform");
  const field = storeEl("input", "fld storepass");
  field.type = "password";
  field.name = "agent-box-store-passphrase";
  field.setAttribute("autocomplete", "off");
  field.setAttribute("autocapitalize", "off");
  field.setAttribute("autocorrect", "off");
  field.spellcheck = false;
  field.placeholder = "Store passphrase";
  field.setAttribute("aria-label", "Store passphrase");
  const btn = storeEl("button", "btn pri", "Unlock");
  btn.type = "button";
  const err = storeEl("div", "dlg-err storeerr");
  err.setAttribute("role", "alert");
  err.textContent = storeRecentMsg(storeLastMsg, storeLastMsgAt, Date.now());
  form.append(field, btn, err);

  let busy = false;
  const submit = async () => {
    if (busy) return;
    const passphrase = field.value;
    field.value = ""; // cleared on every submit, success or not
    if (!passphrase) { err.textContent = "Type the store passphrase."; return; }
    busy = true;
    storeInflight++;
    btn.disabled = true;
    err.textContent = "";
    storeLastMsg = "";
    let out;
    try {
      const res = await fetch("/api/store/unlock", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ passphrase }),
      });
      let text = "";
      try {
        text = await res.text();
        try { text = JSON.parse(text).error || text; } catch (_) { /* not JSON: keep the raw text */ }
      } catch (_) { text = ""; }
      out = storeUnlockOutcome(res.status, text, res.headers.get("Retry-After") || "");
    } catch (_) {
      out = storeUnlockOutcome(0, "", "");
    }
    busy = false;
    btn.disabled = false;
    if (out.ok) {
      field.blur(); // the form is about to be replaced; drop focus explicitly
      storeInflight--;
      await refreshStore();
      if (typeof refreshSessions === "function") refreshSessions();
      if (onDone) onDone();
      return;
    }
    storeInflight--;
    storeLastMsg = out.msg;
    storeLastMsgAt = Date.now();
    err.textContent = out.msg;
    if (out.signedOut) {
      const re = storeEl("button", "btn", "Reload");
      re.type = "button";
      re.onclick = () => location.reload();
      err.appendChild(document.createTextNode(" "));
      err.appendChild(re);
    }
    field.focus();
  };
  btn.addEventListener("click", (ev) => { ev.stopPropagation(); submit(); });
  field.addEventListener("keydown", (ev) => {
    if (ev.key !== "Enter" || ev.isComposing) return;
    ev.preventDefault();  // no implicit submit of an enclosing form
    ev.stopPropagation(); // and no dialog/palette shortcut sees it
    submit();
  });
  container.replaceChildren(form);
  return form;
}

// renderStoreBody builds the needs-you card body for a session whose
// store-backed credentials were refused. Same shape on desktop and mobile; the
// caller adds its own actions.
function renderStoreBody(s, opts) {
  const mobile = !!(opts && opts.mobile);
  const c = cardContent(s);
  const body = storeEl("div", mobile ? "storecard mstorecard" : "nbody storecard");
  body.appendChild(storeEl("div", "storetitle", c.title));
  body.appendChild(storeEl("div", "storetxt", c.text));
  const slot = storeEl("div", "storeslot");
  renderUnlockForm(slot);
  body.appendChild(slot);
  return body;
}

// --- the global banner -----------------------------------------------------------

let storeBannerShown = false;

function paintStoreBanner() {
  const el = document.getElementById("store-banner");
  if (!el) return;
  const show = storeBannerVisible(storeState);
  // Build the form once per appearance: a repaint with the same visibility must
  // not replace a field the operator is typing into.
  if (show && !storeBannerShown) {
    const msg = storeEl("div", "storebanner-msg", "Store locked — tokens from the store can't be used");
    const slot = storeEl("div", "storeslot");
    renderUnlockForm(slot);
    el.replaceChildren(msg, slot);
  } else if (!show) {
    el.replaceChildren();
  }
  storeBannerShown = show;
  el.hidden = !show;
}

// --- Settings "Credentials" tab ------------------------------------------------
// The store's status, unlock and lock live at the top of the Credentials tab
// (credentials.js credStrip); there is no longer a Store tab of its own.

function setStoreErr(msg) {
  const el = document.getElementById("store-err");
  if (el) el.textContent = msg || "";
}

// lockStore: true when the store is now locked. On failure it returns false and
// leaves the reason in #store-err, so a caller must not repaint that element away.
async function lockStore() {
  setStoreErr("");
  try {
    const res = await fetch("/api/store/lock", { method: "POST" });
    if (res.status !== 204) {
      let t = "";
      try { t = (await res.json()).error || ""; } catch (_) { /* keep generic */ }
      setStoreErr(t || "Could not lock the store (" + res.status + ").");
      return false;
    }
  } catch (_) {
    setStoreErr("Could not lock the store: no connection.");
    return false;
  }
  await refreshStore();
  if (typeof refreshSessions === "function") refreshSessions();
  return true;
}

// refreshStore: GET /api/store and repaint every surface. 404 = no store here:
// everything renders nothing. A failed poll keeps the last state (a flaky
// moment must not flash the banner off or on).
async function refreshStore() {
  try {
    const res = await fetch("/api/store");
    if (res.status === 404) storeState = null;
    else if (res.ok) storeState = await res.json();
    else return;
  } catch (_) {
    return;
  }
  paintStoreBanner();
}

function initStore() {
  refreshStore();
}
