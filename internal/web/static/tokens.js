"use strict";

// Access-token management (the "Access tokens" settings tab). Bearer tokens let
// a client app / CLI authenticate to this box without the browser session; each
// is named and independently revocable. The plaintext is shown exactly once, at
// creation — the server only ever stores its hash.

function setTokErr(msg) {
  const el = document.getElementById("tok-err");
  if (el) el.textContent = msg || "";
}

function tokWhen(unix) {
  if (!unix) return "never";
  const d = new Date(unix * 1000);
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

// The desktop page's two date columns (UI plan 4, #174; frame M2.4). The API sends unix seconds: created_at always,
// last_used_at only once the token has authenticated (internal/auth/tokens.go TokenInfo). A token that never connected
// says so, in words ("Never used"), and a missing created_at is an empty cell, never a made-up day. The phone's row
// keeps tokWhen's "used never".
function tokLastUsed(t) {
  return t && t.last_used_at ? tokWhen(t.last_used_at) : "Never used";
}

function tokCreated(t) {
  return t && t.created_at ? tokWhen(t.created_at) : "";
}

// What the in-row confirm says, on the phone's row and on the page's alike.
const TOK_ARM_COPY = "Revoke? That device is signed out on its next request. This cannot be undone.";
// The approved footnote under the page's card (design digest §A7).
const TOK_FOOTNOTE = "A token is shown once, when you create it. Revoking one signs that device out on its next request.";

// The Settings dialog as the desktop page (pagehost.js sets as-page), not a phone's modal.
function tokPageMode() {
  if (typeof document === "undefined") return false;
  const m = document.getElementById("settings-modal");
  return !!m && m.classList.contains("as-page");
}

// tokEl: an element with a class and, when given, its text. Text only (textContent): a token's name is user input.
function tokEl(tag, cls, text) {
  const el = document.createElement(tag);
  if (cls) el.className = cls;
  if (text != null) el.textContent = text;
  return el;
}

// tokBtn: one of a page row's buttons (kit small: "btn btn-sm", or "btn btn-danger btn-sm").
function tokBtn(label, cls, onclick) {
  const b = tokEl("button", cls, label);
  b.type = "button";
  b.onclick = onclick;
  return b;
}

// Where keyboard focus lands once the page's list is repainted. loadTokens rebuilds every row, so the button that was
// pressed is gone and focus would fall to <body>: { id, which } names a token's "cancel" or "revoke" button, found again
// by the token's id (never by position: the list is fetched anew). An id that is not in the list (the last token was just
// revoked, or it went away in another tab) falls back to the head's New token. Set only on the page, never on a phone.
let tokFocus = null;
// The ids of the tokens the page last drew, in order: after a revoke, the neighbour to land on.
let tokIds = [];

function tokFocusTo(btns, want) {
  const row = btns.get(want.id);
  const el = (row && row[want.which]) || document.getElementById("set-act-tokens");
  if (el && typeof el.focus === "function") el.focus();
}

// tokNextFocus(id): after token `id` is revoked, its next row's Revoke…, else the previous row's, else (no id) New token.
function tokNextFocus(id) {
  const i = tokIds.indexOf(id);
  return { id: i < 0 ? "" : (tokIds[i + 1] || tokIds[i - 1] || ""), which: "revoke" };
}

// tokPageRow: one token's row of the page's table. Name, Created and Last used (a muted "Never used"), then Revoke…, or,
// armed, the confirm inside the row: the question across the other three columns with Cancel and Revoke beside it.
// `btns` collects the row's buttons by token id, for tokFocusTo. Arming focuses Cancel (the safe default, not the
// destructive one); Cancel focuses the row's Revoke… again.
function tokPageRow(t, btns) {
  const armed = t.id === armedToken;
  const tr = tokEl("tr", armed ? "arm" : "");
  tokRole(tr, "row");
  tr.appendChild(tokRole(tokEl("td", "tok-nm", t.name), "cell"));
  if (armed) {
    const td = tokRole(tokEl("td", "tok-arm"), "cell");
    td.colSpan = 3;
    const box = tokEl("div", "tok-confirm");
    // The question is the description of both its buttons: a screen reader on Cancel or Revoke hears what is being asked (only one
    // row is armed at a time, so the id is unique; a token's id is hex).
    const ask = tokEl("span", "tok-ask", TOK_ARM_COPY);
    ask.id = "tok-ask-" + t.id;
    box.appendChild(ask);
    const acts = tokEl("div", "tok-acts");
    const cancel = tokBtn("Cancel", "btn btn-sm", () => { armedToken = ""; tokFocus = { id: t.id, which: "revoke" }; loadTokens(); });
    cancel.setAttribute("aria-label", "Cancel revoking " + t.name);
    cancel.setAttribute("aria-describedby", ask.id);
    // No ellipsis on Revoke: this IS the question, not the promise of one.
    const go = tokBtn("Revoke", "btn btn-danger btn-sm", () => revokeToken(t.id));
    go.setAttribute("aria-label", "Revoke " + t.name);
    go.setAttribute("aria-describedby", ask.id);
    btns.set(t.id, { cancel, revoke: go });
    acts.append(cancel, go);
    box.appendChild(acts);
    td.appendChild(box);
    tr.appendChild(td);
    return tr;
  }
  tr.appendChild(tokRole(tokEl("td", "tnum", tokCreated(t)), "cell"));
  tr.appendChild(tokRole(tokEl("td", "tnum" + (t.last_used_at ? "" : " muted"), tokLastUsed(t)), "cell"));
  const act = tokRole(tokEl("td", "num"), "cell");
  const arm = tokBtn("Revoke…", "btn btn-danger btn-sm", () => { armedToken = t.id; tokFocus = { id: t.id, which: "cancel" }; loadTokens(); });
  arm.setAttribute("aria-label", "Revoke " + t.name);
  btns.set(t.id, { revoke: arm });
  act.appendChild(arm);
  tr.appendChild(act);
  return tr;
}

// tokRole: an explicit ARIA role on a part of the page's table. Below a 480px card desktop.css makes the table a block, its rows grids
// and its cells blocks, and a browser then drops the table semantics of the native elements; the roles keep them in every width.
function tokRole(el, role) {
  el.setAttribute("role", role);
  return el;
}

// tokPageTable: the kit's .tbl, one colgroup (the shares are desktop.css's), the actions column named for a screen reader. Every part
// carries its explicit role (tokRole): the table, its two row groups, its rows, heads and cells.
function tokPageTable(items, btns) {
  const table = tokRole(tokEl("table", "tbl tok-tbl"), "table");
  table.setAttribute("aria-label", "API tokens");
  const cols = tokEl("colgroup");
  for (let i = 0; i < 4; i++) cols.appendChild(tokEl("col"));
  table.appendChild(cols);
  const head = tokRole(tokEl("tr"), "row");
  for (const h of ["Name", "Created", "Last used", ""]) {
    const th = tokRole(tokEl("th", h ? "" : "num", h), "columnheader");
    if (!h) th.appendChild(tokEl("span", "sr-only", "Actions"));
    head.appendChild(th);
  }
  table.appendChild(tokRole(tokEl("thead"), "rowgroup")).appendChild(head);
  const body = tokRole(tokEl("tbody"), "rowgroup");
  for (const t of items) body.appendChild(tokPageRow(t, btns));
  table.appendChild(body);
  return table;
}

// tokPaintPage: the desktop page's list: one card holding the table (or the line "No tokens yet."), the approved
// footnote under it. Both are items of #tok-list, so a repaint clears them with the rows. They are a card and a note, not
// a list of two things, so the list and its items carry role="none" (the phone's branch takes it off again: the mode can
// flip). `focus` is where keyboard focus goes after the repaint (see tokFocus).
function tokPaintPage(list, items, focus) {
  const btns = new Map();
  list.setAttribute("role", "none");
  // `tok-card` alone, not the kit's `.card`: that name is also the phone's amber session card (app.css), and the list is only cleared
  // after the phone's fetch answers, so a page card left behind would show amber until then. desktop.css gives .tok-card the fill.
  const card = tokEl("li", "tok-card");
  card.setAttribute("role", "none");
  if (!items || items.length === 0) card.appendChild(tokEl("p", "tokempty", "No tokens yet."));
  else card.appendChild(tokPageTable(items, btns));
  const note = tokEl("li", "tok-under t-sm muted", TOK_FOOTNOTE);
  note.setAttribute("role", "none");
  list.append(card, note);
  tokIds = (items || []).map((t) => t.id);
  if (focus) tokFocusTo(btns, focus);
}

// Which token's row, if any, is showing its in-row revoke confirm.
let armedToken = "";

async function loadTokens() {
  // Where focus goes after this repaint, if the caller asked (the page's arm, Cancel and Revoke); taken now so a
  // later load does not inherit it.
  const focus = tokFocus;
  tokFocus = null;
  const list = document.getElementById("tok-list");
  if (!list) return;
  setTokErr("");
  // Hide any one-time reveal from a previous create: the secret must not linger
  // in the DOM once the user has moved on (reopened the tab, refreshed the list).
  tokHideReveal();
  let items;
  try {
    items = await api("/api/tokens");
  } catch (e) {
    setTokErr("Could not load tokens: " + e.message);
    return;
  }
  // A refresh can never leave a row armed against a token that is gone —
  // revoked from another tab, or the "gone" case of the list being empty.
  if (armedToken && !(items || []).some((t) => t.id === armedToken)) armedToken = "";
  list.textContent = "";
  // Checked now, after the request: the first open of Settings selects this tab before the dialog becomes the page.
  if (tokPageMode()) { tokPaintPage(list, items, focus); return; }
  list.removeAttribute("role");
  if (!items || items.length === 0) {
    const li = document.createElement("li");
    li.className = "tokempty";
    li.textContent = "No tokens yet.";
    list.appendChild(li);
    return;
  }
  for (const t of items) {
    const li = document.createElement("li");
    li.className = "tokrow" + (t.id === armedToken ? " arm" : "");

    const name = document.createElement("span");
    name.className = "nm";
    name.textContent = t.name;

    const meta = document.createElement("span");
    meta.className = "meta";
    if (t.id === armedToken) {
      meta.textContent = TOK_ARM_COPY;
    } else {
      meta.textContent = "used " + tokWhen(t.last_used_at);
      meta.title = "Created " + tokWhen(t.created_at);
    }

    li.append(name, meta);
    if (t.id === armedToken) {
      const cancel = document.createElement("button");
      cancel.type = "button"; cancel.className = "btn"; cancel.textContent = "Cancel";
      cancel.onclick = () => { armedToken = ""; loadTokens(); };
      const go = document.createElement("button");
      // No ellipsis: this IS the question, not the promise of one.
      go.type = "button"; go.className = "btn dgr"; go.textContent = "Revoke";
      go.onclick = () => revokeToken(t.id);
      li.append(cancel, go);
    } else {
      const arm = document.createElement("button");
      arm.type = "button"; arm.className = "btn dgr"; arm.textContent = "Revoke…";
      arm.setAttribute("aria-label", "Revoke " + t.name);
      arm.onclick = () => { armedToken = t.id; loadTokens(); };
      li.append(arm);
    }
    list.appendChild(li);
  }
}

// tokHideReveal: hide the one-time reveal and take the secret out of the page with it: the value node's text, and the
// copy hook's copy of it when that still holds this value. Every way the reveal goes (Done, a repaint, leaving) ends here.
function tokHideReveal() {
  const box = document.getElementById("tok-reveal");
  const val = document.getElementById("tok-reveal-val");
  if (val) {
    const held = val.textContent;
    if (held && typeof window !== "undefined" && window.__lastCopied === held) window.__lastCopied = "";
    val.textContent = "";
  }
  if (box) box.hidden = true;
}

// tokOnScreen: is the Access-tokens tab what the user is looking at (Settings open, this tab selected)? Asked when a
// created token is ready to paint: a create that was in flight when the tab was left and came back still shows it, one
// that was left for good never paints the secret into a panel nobody sees.
function tokOnScreen() {
  const dlg = document.getElementById("settings-modal");
  const panel = document.getElementById("tokens");
  if (dlg && !dlg.open) return false;
  return !(panel && panel.hidden);
}

// tokLeave: the Access-tokens tab (or Settings) is no longer on screen; shell.js calls it from selectSettingsTab and the
// Settings dialog's close.
function tokLeave() {
  tokHideReveal();
}

function revealToken(plain) {
  const box = document.getElementById("tok-reveal");
  const val = document.getElementById("tok-reveal-val");
  if (!box || !val) return;
  val.textContent = plain;
  box.hidden = false;
}

async function createToken() {
  setTokErr("");
  const name = await askPrompt({
    title: "New access token",
    body: "A name to recognise this token later (e.g. \"macbook\", \"cli\").",
    label: "Name",
    placeholder: "macbook",
    confirmLabel: "Create",
  });
  if (name === null) return; // dismissed
  if (!name) { setTokErr("A name is required."); return; }
  try {
    const res = await api("/api/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    // The list refreshes first, so the new row is on screen; the reveal is the
    // last thing painted and the first thing the next refresh takes away.
    await loadTokens();
    if (!tokOnScreen()) return; // the tab was left meanwhile and not come back to: the token is painted nowhere
    revealToken(res.token);
  } catch (e) {
    setTokErr("Create failed: " + e.message);
  }
}

// The confirm lives in the row rather than in the shared dialog. That is a
// deliberate divergence from every other destructive action here, and it earns
// it: a modal that names a token is a worse identification of WHICH token than
// the row it is drawn in. It also means this path is not subject to the shared
// dialog's one-question-at-a-time rule.
async function revokeToken(id) {
  setTokErr("");
  armedToken = "";
  // On the page, focus moves to the next row's Revoke… (the previous one's, or New token) once the row is gone.
  const next = tokPageMode() ? tokNextFocus(id) : null;
  try {
    await api("/api/tokens/" + encodeURIComponent(id), { method: "DELETE" });
    if (next) tokFocus = next;
    await loadTokens();
  } catch (e) {
    setTokErr("Revoke failed: " + e.message);
  }
}

// logout: the sign-out question, then the sign-out. `opts.focusCancel` (only when it is exactly true) opens the question on
// Cancel instead of the destructive button: the desktop nav's Sign out… passes it (below); the phone's footer button is
// this function itself, handed its click event, so its question carries no such key and is what it always was.
async function logout(opts) {
  const ask = {
    title: "Sign out of this box",
    body: "This browser will need the password again. Your running agents keep " +
          "running, and any access tokens you have created keep working — revoke " +
          "those individually if you want them gone.",
    confirmLabel: "Sign out",
    danger: true,
  };
  if (opts && opts.focusCancel === true) ask.focusCancel = true;
  const ok = await askConfirm(ask);
  if (!ok) return;
  // fetch (not api): the handler 303s to /login, which we do explicitly anyway.
  try { await fetch("/logout", { method: "POST" }); } catch (_) {}
  window.location.href = "/login";
}

// navSignOut: the desktop Settings nav's Sign out… (final review I1). It leaves the Profiles page like every other way off it
// (a nav tab, another profile, Esc): through profilepage.js's pfvLeaving (shell.js settingsLeave -> pfvTabLeave -> pfvLeaving),
// which asks about unsaved changes first. Only a "go" (nothing changed, or Discard) reaches the sign-out confirm; Keep editing
// stays on the page and signs nobody out. The sign-out question opens on Cancel (a destructive default is the wrong one for a
// question Esc and Enter can both reach). Not wired on a phone: the item is hidden there and the footer button is logout.
async function navSignOut() {
  const go = typeof pfvLeaving === "function" ? pfvLeaving() : true;
  if (go !== true && !(await go)) return;
  return logout({ focusCancel: true });
}

function initTokens() {
  // The list is (re)loaded whenever the Access-tokens tab is shown — that hook
  // lives in selectSettingsTab (shell.js), so it fires however the tab is
  // reached (button click or openSettings("tokens")), not just on a click here.
  const newBtn = document.getElementById("tok-new");
  if (newBtn) newBtn.onclick = createToken;

  const copyBtn = document.getElementById("tok-copy");
  if (copyBtn) {
    copyBtn.onclick = () => {
      const val = document.getElementById("tok-reveal-val");
      if (val && val.textContent) copyText(val.textContent, (ok) => {
        copyBtn.textContent = ok ? "Copied" : "Copy failed";
        setTimeout(() => { copyBtn.textContent = "Copy"; }, 1500);
      });
    };
  }

  const logoutBtn = document.getElementById("tok-logout");
  if (logoutBtn) logoutBtn.onclick = logout;
  // The desktop Settings nav's foot item (hidden on a phone): the same sign-out and confirm, behind the page's leave guard.
  const navLogout = document.getElementById("set-signout");
  if (navLogout) navLogout.onclick = navSignOut;

  const done = document.getElementById("tok-done");
  if (done) done.onclick = tokHideReveal;
}

if (typeof document !== "undefined") {
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initTokens);
  } else {
    initTokens();
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { tokWhen, tokLastUsed, tokCreated };
}
