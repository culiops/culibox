"use strict";

// Settings > Trusted hosts (#348): the hosts whose links open without the confirm dialog (linkgate.js). A host gets here from the
// dialog's "Always open links to ..." box; this page only lists them and takes one off. A row's Remove… asks inside the row, as the
// Device tokens page's Revoke… does, and the answer is the whole list again. The gate owns the list and the server (lgLoadTrust,
// lgTrustList, lgTrustCorrupt, lgRemoveTrust); this file reads it and paints.
//
// Desktop only: the Settings tab is hidden on the phone (app.css). Classic script: lt/LT_-prefixed names but loadLinkTrust, the one
// entry point shell.js calls when the tab is selected. Built with textContent only: a host is a string the user's browser parsed
// out of an agent's link.

const LT_HINT = [
  "You add a host from the ", ["b", "Open this link?"], " dialog. A link that carries a warning still asks, even to a trusted host. An IP address, an ",
  ["mono", "xn--"], " host and plain ", ["mono", "http"], " cannot be trusted.",
];
const LT_EMPTY = 'No trusted hosts. Tick "Always open links to …" in the Open this link? dialog to add one.';
const LT_UNDER = "Removing a host only stops links skipping the dialog. Nothing else changes.";
const LT_LOADFAIL = "Could not load trusted hosts. Try again.";
const LT_CORRUPT = "link-trust.json is damaged, so every link asks and no host can be added or removed until it is fixed or removed.";

let ltArmed = "";   // the host whose row shows its in-row confirm
let ltErr = "";     // the line of the last failed Remove
let ltBusy = false; // a Remove is in flight
let ltFocus = null; // { host, which } to focus after the next paint: "arm" | "cancel" | "go" | "retry"; host "" is the card or the Retry button

// ltDate: "Nov 19" for a day this year (the approved mockup), "Nov 19, 2025" for any other year, "" for anything that is not a date.
function ltDate(iso) {
  const d = new Date(iso);
  if (!iso || isNaN(d.getTime())) return "";
  const thisYear = d.getFullYear() === new Date().getFullYear();
  return d.toLocaleDateString(undefined, thisYear ? { month: "short", day: "numeric" } : { year: "numeric", month: "short", day: "numeric" });
}

// ltCovers: what a trusted entry reaches.
function ltCovers(e) {
  return e && e.subdomains ? "This host and its subdomains" : "This host only";
}

function ltEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

// ltRole: an explicit ARIA role on a part of the table. Below a 480px card desktop.css makes the table a block and a browser then drops
// the native semantics; the roles keep them in every width.
function ltRole(el, role) {
  el.setAttribute("role", role);
  return el;
}

function ltBtn(label, cls, host, which, onclick) {
  const b = ltEl("button", cls, label);
  b.type = "button";
  b.dataset.ltHost = host;
  b.dataset.ltAct = which;
  b.onclick = onclick;
  return b;
}

function ltHint() {
  const p = ltEl("p", "lt-hint");
  for (const part of LT_HINT) {
    if (typeof part === "string") p.appendChild(document.createTextNode(part));
    else p.appendChild(ltEl(part[0] === "b" ? "b" : "span", part[0] === "b" ? "" : "mono", part[1]));
  }
  return p;
}

function ltRowEl(e, btnOf) {
  const armed = e.host === ltArmed;
  const tr = ltRole(ltEl("tr", armed ? "arm" : ""), "row");
  const cell = (cls) => ltRole(ltEl("td", cls), "cell");
  const h = cell("lt-host");
  const name = ltEl("span", "mono", e.host);
  name.dataset.ltHost = e.host;
  h.appendChild(name);
  tr.appendChild(h);
  if (armed) {
    const td = cell("lt-arm");
    td.colSpan = 3;
    const box = ltEl("div", "lt-confirm");
    // The question describes both its buttons, so a screen reader on Cancel or Remove hears what is being asked. One row asks at a time.
    const ask = ltEl("span", "lt-ask", "Links to " + e.host + " will ask again.");
    ask.id = "lt-ask";
    const acts = ltEl("div", "lt-acts");
    const cancel = ltBtn("Cancel", "btn btn-sm", e.host, "cancel", () => { ltArmed = ""; ltFocus = { host: e.host, which: "arm" }; ltPaint(); });
    cancel.setAttribute("aria-label", "Cancel removing " + e.host);
    cancel.setAttribute("aria-describedby", ask.id);
    // No ellipsis on Remove: this is the question, not the promise of one.
    const go = ltBtn("Remove", "btn btn-danger btn-sm", e.host, "go", () => ltRemove(e.host));
    go.setAttribute("aria-label", "Remove " + e.host);
    go.setAttribute("aria-describedby", ask.id);
    btnOf.set(e.host, { cancel, go });
    acts.append(cancel, go);
    box.append(ask, acts);
    td.appendChild(box);
    tr.appendChild(td);
    return tr;
  }
  const cov = cell("lt-cov muted");
  cov.textContent = ltCovers(e);
  const added = cell("lt-add tnum");
  added.textContent = ltDate(e.added);
  tr.append(cov, added);
  const act = cell("num");
  const arm = ltBtn("Remove…", "btn btn-danger btn-sm", e.host, "arm", () => { ltArmed = e.host; ltErr = ""; ltFocus = { host: e.host, which: "cancel" }; ltPaint(); });
  arm.setAttribute("aria-label", "Remove " + e.host);
  btnOf.set(e.host, { arm });
  act.appendChild(arm);
  tr.appendChild(act);
  return tr;
}

function ltTable(list, btnOf) {
  const table = ltRole(ltEl("table", "tbl lt-tbl"), "table");
  table.setAttribute("aria-label", "Trusted hosts");
  const cols = ltEl("colgroup");
  for (let i = 0; i < 4; i++) cols.appendChild(ltEl("col"));
  table.appendChild(cols);
  const head = ltRole(ltEl("tr"), "row");
  for (const h of ["Host", "Covers", "Added", ""]) {
    const th = ltRole(ltEl("th", h ? "" : "num", h), "columnheader");
    if (!h) th.appendChild(ltEl("span", "sr-only", "Actions"));
    head.appendChild(th);
  }
  table.appendChild(ltRole(ltEl("thead"), "rowgroup")).appendChild(head);
  const body = ltRole(ltEl("tbody"), "rowgroup");
  for (const e of list) body.appendChild(ltRowEl(e, btnOf));
  table.appendChild(body);
  return table;
}

// ltFocusTo puts focus where the last action says (see ltFocus): that host's button, else the card (the row it named is gone and so
// is every other, or the page is damaged).
function ltFocusTo(want, btnOf, card) {
  const mine = btnOf.get(want.host);
  const el = (mine && mine[want.which]) || card;
  if (el && typeof el.focus === "function") el.focus();
}

function ltPaint() {
  const body = document.getElementById("lt-body");
  if (!body) return;
  const list = typeof lgTrustList === "function" ? lgTrustList() : [];
  const corrupt = typeof lgTrustCorrupt === "function" && lgTrustCorrupt();
  // A failed load leaves the gate's list empty, which here is "unknown", not "none": saying "No trusted hosts" would be false while
  // the server holds some, and the next good load brings them back and their links skip the dialog.
  const failed = typeof lgTrustLoadFailed === "function" && lgTrustLoadFailed();
  if (ltArmed && !list.some((e) => e.host === ltArmed)) ltArmed = ""; // gone in another tab
  const kids = [ltHint()];
  const btnOf = new Map();
  let card = null;
  if (ltErr) {
    const p = ltEl("p", "lt-err", ltErr);
    p.setAttribute("role", "alert");
    kids.push(p);
  }
  if (failed) {
    const box = ltEl("div", "lt-loadfail-box");
    const p = ltEl("p", "lt-loadfail", LT_LOADFAIL);
    p.setAttribute("role", "alert");
    const retry = ltBtn("Retry", "btn btn-sm", "", "retry", () => ltReload({ host: "", which: "retry" }));
    btnOf.set("", { retry });
    box.append(p, retry);
    kids.push(box);
  } else if (corrupt) {
    const p = ltEl("p", "lt-corrupt", LT_CORRUPT);
    p.setAttribute("role", "status");
    kids.push(p);
  } else {
    card = ltEl("div", "lt-card");
    card.tabIndex = -1; // where focus lands when the last row is gone
    if (list.length === 0) card.appendChild(ltEl("p", "lt-empty", LT_EMPTY));
    else card.appendChild(ltTable(list, btnOf));
    kids.push(card);
    if (list.length) kids.push(ltEl("p", "lt-under t-sm muted", LT_UNDER));
  }
  body.replaceChildren(...kids);
  const want = ltFocus;
  ltFocus = null;
  if (want) ltFocusTo(want, btnOf, card);
}

// ltRemove: the in-row Remove. One at a time. A failure shows the server's own sentence and puts the row back; either way the list
// is asked for again, so what shows is what the server holds.
async function ltRemove(host) {
  if (ltBusy) return;
  ltBusy = true;
  // Where focus goes once the row is gone: the next row's Remove…, else the previous one's, else (the last) the card.
  const order = typeof lgTrustList === "function" ? lgTrustList().map((e) => e.host) : [];
  const near = order[order.indexOf(host) + 1] || order[order.indexOf(host) - 1] || "";
  try {
    await lgRemoveTrust(host);
    ltErr = "";
    ltArmed = "";
    ltFocus = { host: near, which: "arm" };
  } catch (e) {
    ltErr = "Could not remove " + host + ": " + (e && e.message ? e.message : "request failed");
    ltArmed = "";
    ltFocus = { host, which: "arm" };
    try { await lgLoadTrust(); } catch (_) {}
  } finally {
    ltBusy = false;
  }
  ltPaint();
}

// ltReload: start clean (nothing armed, no old error), ask the gate for the list and paint it. `focus` is where focus goes after
// the paint (Retry's own button is gone by then), or null.
async function ltReload(focus) {
  ltArmed = "";
  ltErr = "";
  ltFocus = focus;
  if (typeof lgLoadTrust === "function") {
    try { await lgLoadTrust(); } catch (_) {}
  }
  ltPaint();
}

// loadLinkTrust: the tab was selected.
function loadLinkTrust() {
  return ltReload(null);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { ltDate, ltCovers, loadLinkTrust };
}
