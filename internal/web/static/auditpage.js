"use strict";

// The Audit page (desktop only, spec §5.8): a descriptive, read-only table over
// the broker's audit trail. The dialog is re-parented into #page-host by
// pagehost.js; this file loads and paints it. Every audit string reaches the DOM
// through textContent. No polling: it loads when opened and on each filter,
// range, session or "Show more" action. Pure parts live in auditmodel.js.

const AUDIT_PAGE_ID = "audit-page";

// The page's state. before/nowS belong to the list in view: "Show more" reuses
// the nowS the first page was read with, so the range does not slide under it.
const auState = { range: "7d", filter: "", session: "", before: "", nowS: 0 };
const auData = { summary: null, events: [], total: 0, next: "", listState: "idle", moreFailed: false };
let auListCtl = null; // the one list request in flight
let auSumCtl = null;  // the one summary request in flight

const auEl = (id) => document.getElementById(id);

function auNames() {
  const out = {};
  for (const s of lastSessions || []) if (s && s.id) out[s.id] = s.name || s.id;
  return out;
}

function auText(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// --- paint -------------------------------------------------------------------

// auPaintControls: the pressed item of each switch is marked with aria-pressed alone: the kit's
// .seg-item[aria-pressed="true"] draws it, and a legacy .on class would bring primitives.css's weight 700
// back. The items are updated in place, never rebuilt, so the one the user clicked keeps keyboard focus.
function auPaintControls() {
  for (const b of document.querySelectorAll("#au-range .seg-item")) {
    b.setAttribute("aria-pressed", b.dataset.range === auState.range ? "true" : "false");
  }
  for (const b of document.querySelectorAll("#au-filters .seg-item")) {
    b.setAttribute("aria-pressed", b.dataset.filter === auState.filter ? "true" : "false");
  }
  const csv = auEl("au-csv");
  if (csv) csv.setAttribute("href", auditCsvHref(auState, auState.nowS || Math.floor(Date.now() / 1000)));
}

function auPaintKpis() {
  const box = auEl("au-kpis");
  if (!box) return;
  // The kit's tile: .kpi > .kpi-value + .kpi-label. The label ellipsizes (desktop.css), so it carries its full text.
  box.replaceChildren(...auditKpis(auData.summary).map((k) => {
    const t = auText("div", "kpi");
    const label = auText("span", "kpi-label", k.label);
    label.setAttribute("title", k.label);
    t.append(auText("span", k.bad ? "kpi-value is-bad" : "kpi-value", k.n), label);
    return t;
  }));
}

// auPaintSessions: "All sessions" plus the range's session ids under their live
// names; the chosen session stays listed even when the range has no row for it.
function auPaintSessions() {
  const sel = auEl("au-session");
  if (!sel) return;
  const names = auNames();
  const ids = auData.summary && Array.isArray(auData.summary.sessions) ? auData.summary.sessions.slice() : [];
  if (auState.session && !ids.includes(auState.session)) ids.unshift(auState.session);
  const opts = [Object.assign(document.createElement("option"), { value: "", textContent: "All sessions" })];
  for (const id of ids) {
    opts.push(Object.assign(document.createElement("option"), { value: id, textContent: names[id] || id }));
  }
  sel.replaceChildren(...opts);
  sel.value = auState.session;
}

const AU_TONE_CLASS = { ok: "au-ok", bad: "au-bad" };

// auWhatCell: sans text, with the server's identifiers in mono (digest-design §B9): each id run is the kit's
// span.mono.t-sm, every other run a text node, then the reason in its muted span. A run with no text draws nothing, and
// runs that draw nothing at all fall back to the plain `what`, so a row never reads blank.
function auWhatCell(ev, v) {
  const td = auText("td", "au-what");
  let drawn = false;
  for (const r of auditWhatRuns(ev)) {
    if (!r.text) continue;
    drawn = true;
    td.appendChild(r.id ? auText("span", "mono t-sm", r.text) : document.createTextNode(r.text));
  }
  if (!drawn && v.what) td.appendChild(document.createTextNode(v.what));
  if (v.reason) td.appendChild(auText("span", "au-reason", " · “" + v.reason + "”"));
  return td;
}

function auRow(ev, names) {
  const v = auditRowView(ev, names);
  const tr = document.createElement("tr");
  // The red row is the kit's tr.is-elevated: a granted or extended write elevation. Its Kind is red too; the Outcome
  // takes its own tone from auditRowView (a refused elevation's is red as well, until Plan 6).
  if (v.elevated) tr.className = "is-elevated";
  const what = auWhatCell(ev, v);
  tr.append(
    auText("td", "au-time", v.time),
    auText("td", "au-sess", v.session),
    auText("td", v.elevated ? "au-kind au-bad" : "au-kind", v.kind),
    what,
    auText("td", "au-out" + (AU_TONE_CLASS[v.tone] ? " " + AU_TONE_CLASS[v.tone] : ""), v.outcome),
  );
  return tr;
}

function auMessageRow(text, cls) {
  const tr = document.createElement("tr");
  const td = auText("td", cls, text);
  td.colSpan = 5;
  tr.appendChild(td);
  return tr;
}

function auPaintTable() {
  const body = auEl("au-body");
  if (!body) return;
  if (auData.listState === "loading" && !auData.events.length) {
    body.replaceChildren(auMessageRow("Loading…", "au-empty"));
  } else if (auData.listState === "failed" && !auData.events.length) {
    body.replaceChildren(auMessageRow("Could not read the audit log.", "au-empty au-bad"));
  } else if (!auData.events.length) {
    body.replaceChildren(auMessageRow("No audit events in this range.", "au-empty"));
  } else {
    const names = auNames();
    body.replaceChildren(...auData.events.map((e) => auRow(e, names)));
  }
}

function auPaintFoot() {
  const count = auEl("au-count");
  const more = auEl("au-more");
  const policy = auEl("au-policy");
  if (count) {
    count.textContent = auData.moreFailed ? "Could not load more."
      : auData.listState === "ok" ? auditFootText(auData.events.length, auData.total) : "";
  }
  if (more) {
    more.hidden = !auData.next;
    more.disabled = auData.listState === "loading";
  }
  if (policy) {
    const days = auData.summary && Number.isFinite(auData.summary.retention_days) ? auData.summary.retention_days : null;
    policy.textContent = days === null ? "" : auditPolicyText(days);
  }
}

function auPaintAll() {
  auPaintControls();
  auPaintKpis();
  auPaintSessions();
  auPaintTable();
  auPaintFoot();
}

// --- load --------------------------------------------------------------------

// auFetch: one GET as data. undefined for anything that is not a 200 JSON body
// (including an abort); never throws.
async function auFetch(url, ctl) {
  try {
    const res = await fetch(url, { signal: ctl.signal, credentials: "same-origin", cache: "no-store" });
    return await accReadReply(res);
  } catch (_) { return undefined; }
}

async function auLoadSummary() {
  if (auSumCtl) auSumCtl.abort();
  const ctl = (auSumCtl = new AbortController());
  const qs = auditQuery({ range: auState.range }, auState.nowS);
  const r = await auFetch("/api/audit/summary?" + qs, ctl);
  if (ctl !== auSumCtl) return; // a newer request owns the page now
  auSumCtl = null;
  // A failed read keeps the tiles it had (a dash on first load).
  if (r && typeof r === "object") auData.summary = r;
  auPaintKpis();
  auPaintSessions();
  auPaintFoot();
}

// auLoadList: the first page (append false) or the next one (append true, from
// the cursor the last reply gave).
async function auLoadList(append) {
  if (auListCtl) auListCtl.abort();
  const ctl = (auListCtl = new AbortController());
  if (!append) {
    auState.before = "";
    auData.events = [];
    auData.total = 0;
    auData.next = "";
  } else {
    auState.before = auData.next;
  }
  auData.moreFailed = false;
  auData.listState = "loading";
  auPaintTable();
  auPaintFoot();
  const r = await auFetch("/api/audit?" + auditQuery(auState, auState.nowS), ctl);
  if (ctl !== auListCtl) return;
  auListCtl = null;
  if (!r || typeof r !== "object" || !Array.isArray(r.events)) {
    auData.listState = append ? "ok" : "failed";
    auData.moreFailed = append;
  } else {
    auData.events = append ? auData.events.concat(r.events) : r.events;
    auData.total = Number.isFinite(r.total) ? r.total : auData.events.length;
    auData.next = typeof r.next === "string" ? r.next : "";
    auData.listState = "ok";
  }
  auPaintTable();
  auPaintFoot();
}

// auReload: a new filter, range or session: back to the top.
function auReload(withSummary) {
  auState.nowS = Math.floor(Date.now() / 1000);
  auPaintControls();
  if (withSummary) auLoadSummary();
  auLoadList(false);
}

function auAbortAll() {
  if (auListCtl) { auListCtl.abort(); auListCtl = null; }
  if (auSumCtl) { auSumCtl.abort(); auSumCtl = null; }
}

// openAudit: the sidebar link, the palette entry and the Access view's "full
// history" link. opts.session pre-selects a session. There is no Audit page on a
// phone.
function openAudit(opts) {
  if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) return;
  const o = opts || {};
  auState.range = "7d";
  auState.filter = "";
  auState.session = typeof o.session === "string" ? o.session : "";
  auData.summary = null;
  openDialog(AUDIT_PAGE_ID);
  auReload(true);
  auPaintAll();
}

// --- wiring ------------------------------------------------------------------

function initAuditPage() {
  const dlg = auEl(AUDIT_PAGE_ID);
  if (!dlg) return;
  dlg.addEventListener("close", auAbortAll);
  const range = auEl("au-range");
  if (range) range.addEventListener("click", (ev) => {
    const b = ev.target.closest("button[data-range]");
    if (!b || b.dataset.range === auState.range) return;
    auState.range = b.dataset.range;
    auReload(true);
  });
  const filters = auEl("au-filters");
  if (filters) filters.addEventListener("click", (ev) => {
    const b = ev.target.closest("button[data-filter]");
    if (!b || b.dataset.filter === auState.filter) return;
    auState.filter = b.dataset.filter;
    auReload(false);
  });
  const sel = auEl("au-session");
  if (sel) sel.addEventListener("change", () => {
    auState.session = sel.value;
    auReload(false);
  });
  const more = auEl("au-more");
  if (more) more.addEventListener("click", () => { if (auData.next) auLoadList(true); });
  const csv = auEl("au-csv");
  // The download reads href after this handler, so a page left open for hours
  // still exports up to now.
  if (csv) csv.addEventListener("click", () => {
    csv.setAttribute("href", auditCsvHref(auState, Math.floor(Date.now() / 1000)));
  });
}

initAuditPage();
