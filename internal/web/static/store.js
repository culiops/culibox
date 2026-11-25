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

// storeUnavailableLabel: the short name of a macOS Keychain failure, by kind.
// The long text (what happened and how to fix it) comes from the server, which
// owns it; this is only the label. An unknown kind still says "unavailable"
// instead of looking like a healthy Keychain.
function storeUnavailableLabel(kind) {
  if (kind === "access") return "Keychain access failed";
  if (kind === "key_lost") return "Keychain key lost";
  if (kind === "store_unreadable") return "Store unreadable";
  return "Keychain unavailable";
}

// storeExpiryText: the Settings status line. nowMs is injected so it is testable.
function storeExpiryText(st, nowMs) {
  if (st && st.unavailable) return storeUnavailableLabel(st.unavailable.kind);
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

// storeLocksSoonText: F77, the sidebar footer's neutral "Store locks in 42 m". Shown only in the
// store's last hour, so the lock does not arrive as a surprise (store-backed agents start failing
// once it does); "" at every other moment: a store that is locked, was never created, has no TTL
// (the macOS Keychain, expires_at null), is unavailable or absent, and an expiry that is
// unparseable, already past, or more than an hour away. expires_at is an RFC 3339 string, parsed
// as storeExpiryText parses it, and the minutes use its floor, so the footer and the Credentials
// strip never disagree. Exactly 60:00 is inside the hour; 60:01 is not.
function storeLocksSoonText(st, nowMs) {
  if (!st || !st.initialized || st.unavailable || st.platform === "darwin" || st.locked || !st.expires_at) return "";
  const left = Date.parse(st.expires_at) - nowMs;
  if (!(left > 0) || left > 60 * 60000) return "";
  const mins = Math.floor(left / 60000);
  return mins < 1 ? "Store locks in under a minute" : "Store locks in " + mins + " m";
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

// storeOutcomeKind: which of the sidebar form's three treatments an unlock failure gets (DD§A9 M8.7b):
// "wrong" = the red .field-error under the field; "throttle" = the muted line (429 is the throttle working,
// not an error); "signedout" = no wording of its own, the signed-out banner says it (F53). Anything else is
// "error" (the server's own line, red). A 204 is not a failure: "".
function storeOutcomeKind(status, text) {
  if (status === 204) return "";
  if (status === 401) return String(text || "") === "wrong passphrase" ? "wrong" : "signedout";
  if (status === 429) return "throttle";
  return "error";
}

// storeBannerModel: the sidebar's store banner (DD§A9 M8.7, R19), or null. Shown for what storeBannerVisible
// allows (so never on darwin) and never while signed out (M8.6b: the store banner gives way to that one).
// `blocked` = sessions whose need is store_locked. The frame draws only the plural line; with none blocked
// no count is invented and the line is left out. There is no "Stays unlocked" line: a locked store has no TTL.
function storeBannerModel(st, blocked, signedOut) {
  if (signedOut || !storeBannerVisible(st)) return null;
  const n = blocked > 0 ? blocked : 0;
  const body = n === 0 ? "" : (n === 1 ? "1 session" : n + " sessions") + " can\u2019t fetch credentials from the store.";
  return { key: "store", title: "Store locked", body };
}

// storeDeskCardModel: what a desktop store card says (DD§A9 strips, M8.7b #6). darwin has no passphrase (R6):
// the Keychain failure, label from storeUnavailableLabel and the server's own fix text, never a field.
// Otherwise the strip sentence plus Unlock…, which only points at the sidebar's one field; when that banner
// cannot show (no store state yet, or the store is not in use) the card keeps its own form so it never dead-ends.
function storeDeskCardModel(st, signedOut) {
  if (st && st.platform === "darwin") {
    const u = st.unavailable;
    return { kind: "keychain", title: storeUnavailableLabel(u && u.kind), text: (u && (u.fix || u.reason)) || "" };
  }
  if (storeBannerVisible(st) || signedOut) {
    return { kind: "pointer", title: "Store locked", text: "Credentials from the store can\u2019t be fetched. Unlock it in the sidebar." };
  }
  return { kind: "form" };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { lockStore, renderUnlockForm, storeUnlockFocused, storeRecentMsg, STORE_HONEST_STATEMENT, storeBannerVisible, storeExpiryText, storeLocksSoonText, paintSideStore, storeUnavailableLabel, storePlaintextText, storeUnlockOutcome, storeOutcomeKind, storeBannerModel, storeDeskCardModel, renderStoreBody, paintStoreBanner, storeFocusField, refreshStore };
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
let storeLastKind = "";
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
//
// opts.kit (the desktop sidebar banner, DD§A9 M8.7b): the kit field, a primary btn-sm, `Unlocking…` while the
// request runs, a wrong passphrase as a red .field-error + .is-invalid under the field, a 429 as a muted line,
// and a 401 with NO wording of its own (the signed-out banner says it, via sysNoteSignedOut).
function renderUnlockForm(container, onDone, opts) {
  const kit = !!(opts && opts.kit);
  const form = storeEl("div", "storeform");
  const field = storeEl("input", kit ? "input storepass" : "fld storepass");
  field.type = "password";
  field.name = "agent-box-store-passphrase";
  field.setAttribute("autocomplete", "off");
  field.setAttribute("autocapitalize", "off");
  field.setAttribute("autocorrect", "off");
  field.spellcheck = false;
  field.placeholder = "Store passphrase";
  field.setAttribute("aria-label", "Store passphrase");
  const btn = storeEl("button", kit ? "btn btn-primary btn-sm" : "btn pri", "Unlock");
  btn.type = "button";
  const err = storeEl("div", kit ? "storeerr" : "dlg-err storeerr");
  // Paints the failure line for the kit form: the class says which treatment (red, muted, none).
  const paintErr = (msg, kind) => {
    err.textContent = msg;
    if (!kit) return;
    err.className = "storeerr" + (msg && kind === "throttle" ? " t-sm muted" : msg ? " field-error" : "");
    field.className = "input storepass" + (msg && kind === "wrong" ? " is-invalid" : "");
    if (msg && kind === "wrong") field.setAttribute("aria-invalid", "true");
    else field.setAttribute("aria-invalid", "false");
  };
  err.setAttribute("role", "alert");
  err.textContent = storeRecentMsg(storeLastMsg, storeLastMsgAt, Date.now());
  if (kit) {
    // Field, its error under it, then the button (the frame's order): the error belongs to the field.
    paintErr(err.textContent, storeLastKind);
    form.append(field, err, btn);
  } else form.append(field, btn, err);

  let busy = false;
  const submit = async () => {
    if (busy) return;
    const passphrase = field.value;
    field.value = ""; // cleared on every submit, success or not
    if (!passphrase) { paintErr("Type the store passphrase.", "error"); return; }
    busy = true;
    storeInflight++;
    btn.disabled = true;
    if (kit) { field.disabled = true; btn.textContent = "Unlocking\u2026"; }
    paintErr("", "");
    storeLastMsg = "";
    let out;
    let kind = "error";
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
      kind = storeOutcomeKind(res.status, text);
    } catch (_) {
      out = storeUnlockOutcome(0, "", "");
    }
    busy = false;
    btn.disabled = false;
    if (kit) { field.disabled = false; btn.textContent = "Unlock"; }
    if (out.ok) {
      field.blur(); // the form is about to be replaced; drop focus explicitly
      storeInflight--;
      await refreshStore();
      if (typeof refreshSessions === "function") refreshSessions();
      if (onDone) onDone();
      return;
    }
    storeInflight--;
    if (kit && kind === "signedout") {
      // The third wording is gone (F53): the signed-out banner, drawn from `sys`, is the one message.
      if (typeof sysNoteSignedOut === "function") sysNoteSignedOut();
      field.focus();
      return;
    }
    storeLastMsg = out.msg;
    storeLastMsgAt = Date.now();
    storeLastKind = kind;
    paintErr(out.msg, kind);
    if (out.signedOut && !kit) {
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

// storeLiveSignedOut / storeLiveBlocked read app.js's `sys` and `lastSessions` (let/const in the shared page
// scope) at call time; before app.js reaches them (or in a node test) they are false and 0.
function storeLiveSignedOut() {
  try { return !!sys.signedOut; } catch (_) { return false; }
}
function storeLiveBlocked() {
  try { return lastSessions.filter((x) => x && x.need === "store_locked").length; } catch (_) { return 0; }
}
function storeIsPhone() {
  return typeof mobileActive === "function" && mobileActive(window.innerWidth);
}

// storeFocusField: `Unlock…` on a strip. Opens the sidebar when it is collapsed to the rail, then focuses the
// ONE passphrase field in the sidebar banner; while signed out, the "Sign in again" button instead. False when
// there is nothing to focus.
function storeFocusField() {
  const root = document.documentElement;
  if (root && root.classList && root.classList.contains("sidebar-hidden") && typeof toggleSidebar === "function") toggleSidebar();
  // Signed out the store banner is gone (M8.6b): Unlock… lands on the signed-out banner's Sign in again (R23).
  if (storeLiveSignedOut() && typeof sysBannerFocusSignIn === "function") return sysBannerFocusSignIn();
  const slot = document.getElementById("store-banner-slot");
  const f = slot && slot.querySelector ? slot.querySelector(".storepass") : null;
  if (!f) return false;
  f.focus();
  return true;
}

// renderStoreBody builds the needs-you card body for a session whose
// store-backed credentials were refused. On a phone: the title, the sentence and
// the passphrase form (unchanged). On the desktop (DD§A9): ONE form lives in the
// sidebar banner, so the card points at it with `Unlock…`; on macOS it names the
// Keychain failure and offers no field (R6). The caller adds its own actions.
// opts.strip: the body of the ask strip, whose label already says "Store locked".
function renderStoreBody(s, opts) {
  const mobile = !!(opts && opts.mobile);
  const inStrip = !!(opts && opts.strip);
  const m = mobile ? { kind: "form" } : storeDeskCardModel(storeState, storeLiveSignedOut());
  const c = cardContent(s);
  const body = storeEl("div", mobile ? "storecard mstorecard" : "nbody storecard");
  if (m.kind === "keychain") {
    body.appendChild(storeEl("div", "storetitle", m.title));
    if (m.text) body.appendChild(storeEl("div", "storetxt", m.text));
    return body;
  }
  if (m.kind === "pointer") {
    if (!inStrip) body.appendChild(storeEl("div", "storetitle", m.title));
    body.appendChild(storeEl("div", "storetxt", m.text));
    const go = storeEl("button", "btn btn-sm", "Unlock\u2026");
    go.type = "button";
    go.title = "Focus the passphrase field in the sidebar";
    go.onclick = () => storeFocusField();
    body.appendChild(go);
    return body;
  }
  body.appendChild(storeEl("div", "storetitle", c.title));
  body.appendChild(storeEl("div", "storetxt", c.text));
  const slot = storeEl("div", "storeslot");
  renderUnlockForm(slot);
  body.appendChild(slot);
  return body;
}

// --- the global banner -----------------------------------------------------------

let storeBannerShown = false;
let storeSideShown = false;
let storeSideBody = null;

// storeSideModel: the sidebar banner's model right now (null on a phone, signed out, or with nothing to say).
function storeSideModel() {
  if (storeIsPhone()) return null;
  return storeBannerModel(storeState, storeLiveBlocked(), storeLiveSignedOut());
}

// paintSideStoreBanner: the desktop store banner, in #store-banner-slot below the system banner (R19). Built once
// per appearance so a repaint never replaces a field being typed into; only its count line moves.
function paintSideStoreBanner() {
  const slot = document.getElementById("store-banner-slot");
  if (!slot) return;
  const m = storeSideModel();
  if (m && !storeSideShown) {
    const root = storeEl("div", "banner is-needs");
    root.setAttribute("role", "alert");
    const title = storeEl("div", "banner-title");
    const dot = storeEl("span", "dot dot-needs");
    dot.setAttribute("aria-hidden", "true");
    title.append(dot, uiIcon(ICON_STATUS.locked, { size: 14 }), document.createTextNode(m.title));
    root.appendChild(title);
    storeSideBody = storeEl("p", "t-sm", m.body);
    storeSideBody.hidden = !m.body;
    root.appendChild(storeSideBody);
    const fslot = storeEl("div", "storeslot");
    renderUnlockForm(fslot, null, { kit: true });
    root.appendChild(fslot);
    slot.replaceChildren(root);
  } else if (!m) {
    if (storeSideShown) slot.replaceChildren();
    storeSideBody = null;
  } else if (storeSideBody && storeSideBody.textContent !== m.body) {
    storeSideBody.textContent = m.body;
    storeSideBody.hidden = !m.body;
  }
  storeSideShown = !!m;
  slot.hidden = !m;
}

// storeRailBanner: the store's input to the collapsed rail's one marker (lowest priority, R19), or null.
function storeRailBanner() {
  const m = storeSideModel();
  return m ? { title: m.title } : null;
}

function paintStoreBanner() {
  paintSideStoreBanner();
  const el = document.getElementById("store-banner");
  if (!el) return;
  // The full-width banner is the phone's; the desktop's one form is the sidebar's.
  const show = storeBannerVisible(storeState) && storeIsPhone();
  // Build the form once per appearance: a repaint with the same visibility must
  // not replace a field the operator is typing into.
  if (show && !storeBannerShown) {
    const msg = storeEl("div", "storebanner-msg", "Store locked \u2014 tokens from the store can't be used");
    const slot = storeEl("div", "storeslot");
    renderUnlockForm(slot);
    el.replaceChildren(msg, slot);
  } else if (!show) {
    el.replaceChildren();
  }
  storeBannerShown = show;
  el.hidden = !show;
}

// paintSideStore: the sidebar footer's "Store locks in N m" (F77), hidden and empty outside the
// store's last hour. Painted by refreshStore, so it rides the existing 30 s store poll: the minute
// count is at most one poll stale and no timer of its own runs. The footer is desktop-only
// (#side-foot is display:none on the phone), so there is nothing to gate on the viewport here.
function paintSideStore(st, nowMs) {
  const el = document.getElementById("side-store");
  if (!el) return;
  const text = storeLocksSoonText(st, nowMs);
  el.textContent = text;
  el.hidden = text === "";
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
  paintSideStore(storeState, Date.now());
}

function initStore() {
  refreshStore();
}
