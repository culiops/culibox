"use strict";

// The Access view (desktop pane view, spec 5.7): what the pane's agent session
// may touch. Containment, cloud credentials with Elevate / Extend / Drop and the
// SSO sign-in card, and the last credential events. The model is accessmodel.js;
// this file draws it. Every server- and operator-supplied string (a connection
// name, a label, a reason, a path, an error) goes in through textContent, and
// the SSO card is app.js renderSSOBody's.
//
// The host is volatile (a tab close or dock change re-attaches it) and the poll
// repaints every 3 s, so nothing the operator types lives in the DOM alone: the
// grant panel's draft is in accDrafts, per session, and survives both a repaint
// and a remount. A section repaints only when its signature changes, and the
// credentials section does not repaint at all while one of its fields has focus.

// ACCESS_REFRESH_MS: how often the broker status is re-read while the view is up
// (not every poll). A grant, a drop and a sign-in re-read it at once.
const ACCESS_REFRESH_MS = 15000;

// Per session id, kept across remounts of the view:
const accData = new Map();   // sid -> {aws, egress, at, inflight, loaded, failed}
const accDrafts = new Map(); // sid -> the open grant panel's draft {conn, minutes, reason, typed}
const accErrs = new Map();   // sid -> the panel's inline error text
const accBusy = new Set();   // sids with a grant or drop in flight
const accViews = new Map();  // sid -> repaint(force) of the view mounted for it

function accEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// accCell: a small label over a value, one column of a card row.
function accCell(label, value, valCls, title) {
  const c = accEl("div", "ac-cell");
  c.appendChild(accEl("div", "ac-lab", label));
  const v = accEl("div", "ac-val" + (valCls ? " " + valCls : ""), value);
  if (title) v.title = title;
  c.appendChild(v);
  return c;
}

function accCardHead(title, meta) {
  const h = accEl("div", "ac-head");
  h.appendChild(accEl("span", "ac-title", title));
  if (meta) h.appendChild(accEl("span", "ac-meta", meta));
  return h;
}

// --- data ----------------------------------------------------------------------

function accBound(s) {
  return !!s && (!!s.aws || (Array.isArray(s.aws_connections) && s.aws_connections.length > 0) || !!s.github ||
    (Array.isArray(s.tokens) && s.tokens.length > 0));
}

// accLoad: read the broker status (and the egress line of an unbound box) for
// sid. At most once per ACCESS_REFRESH_MS unless forced. A failed read keeps what
// was there: only a 200 or a 204 replaces it.
function accLoad(sid, force) {
  let d = accData.get(sid);
  if (!d) { d = { aws: null, egress: null, at: 0, inflight: false, loaded: false, failed: false }; accData.set(sid, d); }
  if (d.inflight || (!force && d.at && Date.now() - d.at < ACCESS_REFRESH_MS)) return;
  const s = lastSessions.find((x) => x.id === sid);
  if (!s) return;
  d.inflight = true;
  const get = (tail) => fetch("/api/sessions/" + encodeURIComponent(sid) + tail).then(accReadReply, () => undefined);
  (async () => {
    let r = { ok: false };
    try {
      let aws = null;
      let ok = true;
      if (accBound(s)) {
        aws = await get("/aws");
        if (aws === undefined) ok = false;
      }
      let egress = null;
      if (ok && !aws && s.egress) {
        egress = await get("/egress");
        if (egress === undefined) ok = false;
      }
      r = { ok, aws, egress };
    } catch (_) { r = { ok: false }; }
    // Whatever happened, the read is over: inflight is reset by accLoadDone.
    Object.assign(d, accLoadDone(d, r, Date.now()));
    const v = accViews.get(sid);
    if (v) v(false);
  })();
}

function accPrune() {
  if (!lastSessions.length) return;
  const live = new Set(lastSessions.map((s) => s.id));
  for (const m of [accData, accDrafts, accErrs]) for (const id of [...m.keys()]) if (!live.has(id)) m.delete(id);
}

// --- actions -------------------------------------------------------------------

async function accGrant(sid, row, repaint) {
  const d = accDrafts.get(sid);
  if (accBusy.has(sid) || !d || !row || !row.grant) return;
  const g = row.grant;
  const fs = grantFormState({ minutes: d.minutes, reason: d.reason, typed: d.typed, prod: g.needLabel, label: g.label, busy: false });
  if (!fs.valid || g.blocked || g.blockedText) return;
  accBusy.add(sid);
  accErrs.delete(sid);
  repaint(true);
  try {
    await elevGrant(sid, { name: row.conn, minutes: d.minutes, reason: d.reason, confirm_label: g.needLabel ? d.typed : undefined });
    accDrafts.delete(sid);
    accLoad(sid, true);
    if (typeof refreshSessions === "function") refreshSessions();
  } catch (e) {
    accErrs.set(sid, e.message);
  } finally {
    accBusy.delete(sid);
  }
  repaint(true);
}

// accDrop: true when the daemon dropped the window, false when the press was refused (a request already
// in flight) or failed (an error toast says why). The pane's banner (elevbanner.js) reads it to tell a Drop
// that went through, whose button stays inert until the list catches up, from one that did not.
async function accDrop(sid, repaint) {
  if (accBusy.has(sid)) return false;
  accBusy.add(sid);
  repaint(true);
  let ok = false;
  try {
    showToast(dropToast(await elevDrop(sid)));
    ok = true;
    accLoad(sid, true);
    if (typeof refreshSessions === "function") refreshSessions();
  } catch (e) {
    showToast(e.message, { error: true });
  } finally {
    accBusy.delete(sid);
  }
  repaint(true);
  return ok;
}

// --- sections --------------------------------------------------------------------

function accContainment(c, s) {
  const card = accEl("section", "ac-card");
  card.appendChild(accCardHead("Containment", c.tier + (c.runtime ? " · " + c.runtime : "")));
  const cols = accEl("div", "ac-cols");
  const badge = typeof tierBadge === "function" ? tierBadge(s, typeof boxRuntime === "string" ? boxRuntime : "") : null;
  const tier = accCell("Tier", c.tier, "ac-tier" + (c.tier === "host" ? " host" : ""), badge ? badge.title : "");
  if (c.tier === "host") tier.appendChild(accEl("div", "ac-sub", "not contained"));
  cols.appendChild(tier);
  cols.appendChild(accCell("Workspace", c.workspace ? shortPath(c.workspace) : "—", "mono", c.workspace));
  const n = c.network;
  const net = !n.mode ? "no egress control"
    : n.mode === "open" ? "open · " + n.denied + " denied"
      : "allowlist · " + n.allowed + " allowed · " + n.denied + " denied";
  cols.appendChild(accCell("Network", net, "", n.mode ? "Counted since the daemon started." : ""));
  cols.appendChild(accCell("Agent account", c.account));
  card.appendChild(cols);

  // Optional rows: each shows only when the session has something to say.
  const kv = [];
  if (c.started) kv.push(["Started", c.started]);
  if (c.model) kv.push(["Model", c.effort ? c.model + " · " + c.effort : c.model]);
  else if (c.effort) kv.push(["Effort", c.effort]);
  const rows = accEl("div", "ac-kvs");
  const kvRow = (k, nodes) => {
    const r = accEl("div", "ac-kv");
    r.appendChild(accEl("div", "ac-lab", k));
    const v = accEl("div", "ac-kvv");
    for (const nd of nodes) v.appendChild(nd);
    r.appendChild(v);
    rows.appendChild(r);
  };
  for (const [k, v] of kv) kvRow(k, [accEl("div", "", v)]);
  if (c.readable.length) kvRow("Readable host paths", c.readable.map((p) => accEl("div", "mono", p)));
  if (c.clone) {
    const lines = [];
    if (c.clone.of) lines.push(accEl("div", "mono", c.clone.of));
    for (const w of c.clone.warn) lines.push(accEl("div", "ac-cloneline warn", w));
    for (const i of c.clone.info) lines.push(accEl("div", "ac-cloneline", i));
    kvRow("Cloned from", lines);
  }
  if (c.dropped) kvRow("Left out of the box", [accEl("div", "", c.dropped.v)]);
  if (rows.childNodes.length) card.appendChild(rows);
  return card;
}

function accRowBlock(r, ctx) {
  const { sid, repaint, rowByName } = ctx;
  const blk = accEl("div", "ac-rowblk");
  const row = accEl("div", "ac-row");
  const id = accEl("div", "ac-id");
  const nm = accEl("div", "ac-nameline");
  nm.appendChild(accEl("span", "ac-name", r.name));
  if (r.prod) nm.appendChild(accEl("span", "ac-prod", "PROD"));
  id.appendChild(nm);
  if (r.sub) id.appendChild(accEl("div", "ac-sub", r.sub));
  row.appendChild(id);

  if (r.kind === "aws") {
    row.appendChild(accCell("Scope", r.scope, r.scope === "read" ? "" : "ac-write"));
    const sso = accCell("SSO", r.sso || "—", r.sso === "expired" ? "ac-need" : r.sso === "valid" ? "ac-ok" : "");
    row.appendChild(sso);
    row.appendChild(accCell("Last credential", r.last || "—"));
  } else {
    row.appendChild(accCell("Scope", r.scope || "—"));
    row.appendChild(accCell("Status", r.status || "—", r.status === "ok" ? "ac-ok" : ""));
    row.appendChild(accCell(r.kind === "github" ? "Last token" : "Last fetch", r.last || "—"));
  }

  const acts = accEl("div", "ac-acts");
  const busy = accBusy.has(sid);
  const mk = (text, cls, fn, off) => {
    const b = accEl("button", "btn" + (cls ? " " + cls : ""), text);
    b.type = "button";
    b.disabled = !!off;
    b.classList.toggle("off", !!off);
    b.onclick = fn;
    acts.appendChild(b);
  };
  const openPanel = () => {
    const cur = rowByName(r.name);
    const w = cur && cur.grant ? cur.grant.windows : [];
    accDrafts.set(sid, { conn: r.name, minutes: w[0] || 0, reason: "", typed: "" });
    accErrs.delete(sid);
    ctx.focusReason = true;
    accLoad(sid, true); // the host-session list and any blocker are read when the panel opens
    repaint(true);
  };
  const g = r.grant;
  if (r.actions.includes("elevate")) mk("Elevate…", "", openPanel, busy || !!(g && g.blockedText));
  if (r.actions.includes("extend")) mk("Extend", "", openPanel, busy);
  if (r.actions.includes("drop")) mk("Drop", "dgr", () => accDrop(sid, repaint), busy);
  row.appendChild(acts);
  blk.appendChild(row);

  if (r.kind === "aws") {
    if (r.base) {
      const n = accEl("div", "ac-note", r.base);
      if (r.baseTitle) n.title = r.baseTitle;
      blk.appendChild(n);
    }
    if (r.stat) blk.appendChild(accEl("div", "ac-note err", r.stat));
  }
  return blk;
}

// accPanel: the inline grant panel under a connection's row. The draft is the
// module's, so a repaint (or a remount) rebuilds it with what was typed.
function accPanel(r, d, ctx) {
  const { sid, repaint, rowByName } = ctx;
  const g = r.grant;
  const box = accEl("div", "ac-panel");
  box.setAttribute("role", "group");
  box.setAttribute("aria-label", (g.extend ? "Extend write access" : "Grant write access") + " · " + r.name);

  const lead = accEl("div", "ac-plead");
  lead.append(document.createTextNode((g.extend ? "Extend write on " : "Grant write on ")));
  lead.appendChild(accEl("strong", "", r.name));
  lead.appendChild(document.createTextNode(" to this session. Credentials already issued stay valid until they expire."));
  box.appendChild(lead);

  if (!g.windows.includes(d.minutes)) d.minutes = g.windows[0] || 0;
  const fields = accEl("div", "ac-pfields");

  const wcol = accEl("div", "ac-pcol");
  wcol.appendChild(accEl("span", "ac-lab", "Window"));
  const seg = accEl("div", "seg");
  seg.setAttribute("role", "group");
  seg.setAttribute("aria-label", "Window");
  const segBtns = g.windows.map((w) => {
    const b = accEl("button", "", w + " min");
    b.type = "button";
    b.onclick = () => { d.minutes = w; sync(); };
    seg.appendChild(b);
    return [w, b];
  });
  wcol.appendChild(seg);
  fields.appendChild(wcol);

  const rcol = accEl("div", "ac-pcol grow");
  const rlab = accEl("label", "ac-lab", "Reason (recorded)");
  const reason = accEl("input", "fld");
  reason.type = "text";
  reason.maxLength = 200;
  reason.autocomplete = "off";
  reason.dataset.ac = "reason";
  reason.id = "ac-reason-" + sid;
  rlab.htmlFor = reason.id;
  reason.value = d.reason;
  reason.oninput = () => { d.reason = reason.value; sync(); };
  rcol.append(rlab, reason);
  fields.appendChild(rcol);

  let typed = null;
  if (g.needLabel) {
    const lcol = accEl("div", "ac-pcol grow");
    const llab = accEl("label", "ac-lab");
    llab.append(document.createTextNode("Type "));
    llab.appendChild(accEl("span", "mono", g.label));
    llab.append(document.createTextNode(" to confirm"));
    typed = accEl("input", "fld mono");
    typed.type = "text";
    typed.autocomplete = "off";
    typed.spellcheck = false;
    typed.dataset.ac = "typed";
    typed.id = "ac-typed-" + sid;
    llab.htmlFor = typed.id;
    typed.value = d.typed;
    typed.oninput = () => { d.typed = typed.value; sync(); };
    lcol.append(llab, typed);
    fields.appendChild(lcol);
  }
  box.appendChild(fields);

  if (r.hostWarning.length) {
    const warn = accEl("div", "ac-warn");
    warn.appendChild(accEl("div", "", "These sessions run on the host and can bypass elevation controls:"));
    const ul = accEl("ul");
    for (const h of r.hostWarning) ul.appendChild(accEl("li", "", h));
    warn.appendChild(ul);
    box.appendChild(warn);
  }
  for (const why of [g.blocked, g.blockedText]) if (why) box.appendChild(accEl("div", "ac-warn", why));
  if (!g.windows.length) box.appendChild(accEl("div", "ac-warn", "This binding allows no window of 30 minutes or more."));
  box.appendChild(accEl("div", "ac-prole", "Role: " + g.role));

  const hint = accEl("div", "ac-hint");
  box.appendChild(hint);
  const err = accEl("div", "ac-err", accErrs.get(sid) || "");
  err.setAttribute("role", "alert");
  err.hidden = !err.textContent;
  box.appendChild(err);

  const btns = accEl("div", "ac-pbtns");
  const cancel = accEl("button", "btn", "Cancel");
  cancel.type = "button";
  cancel.disabled = accBusy.has(sid);
  cancel.classList.toggle("off", cancel.disabled);
  cancel.onclick = () => { accDrafts.delete(sid); accErrs.delete(sid); repaint(true); };
  const grant = accEl("button", "btn pri");
  grant.type = "button";
  const doGrant = () => accGrant(sid, rowByName(r.name), repaint);
  grant.onclick = doGrant;
  btns.append(cancel, grant);
  box.appendChild(btns);

  const HINTS = {
    reason: "A reason is required. It is recorded in the audit log.",
    "reason-long": "The reason is at most 200 characters.",
    label: "Type the label exactly, including its case.",
    busy: "Sending…",
    minutes: "Pick a window.",
  };
  // One place decides what is pressed and whether Grant is live, so the look and
  // the real disabled attribute cannot disagree. A double click sends one
  // request: accGrant refuses while accBusy holds the session.
  function sync() {
    for (const [w, b] of segBtns) {
      b.classList.toggle("on", w === d.minutes);
      b.setAttribute("aria-pressed", w === d.minutes ? "true" : "false");
    }
    const fs = grantFormState({ minutes: d.minutes, reason: d.reason, typed: d.typed, prod: g.needLabel, label: g.label, busy: accBusy.has(sid) });
    const ok = fs.valid && !g.blocked && !g.blockedText;
    grant.textContent = (g.extend ? "Extend" : "Grant write") + (d.minutes ? " · " + d.minutes + " min" : "");
    grant.disabled = !ok;
    grant.classList.toggle("off", !ok);
    hint.textContent = ok ? "" : HINTS[fs.why] || "";
  }
  const onEnter = (e) => { if (e.key === "Enter" && !grant.disabled) { e.preventDefault(); doGrant(); } };
  reason.onkeydown = onEnter;
  if (typed) typed.onkeydown = onEnter;
  sync();
  return box;
}

function accCreds(m, s, ctx) {
  const { sid } = ctx;
  const card = accEl("section", "ac-card");
  card.appendChild(accCardHead("Cloud credentials", m.creds.head));
  const d = accDrafts.get(sid);
  for (const r of m.creds.rows) {
    const blk = accRowBlock(r, ctx);
    if (m.creds.ssoFor === r.name && r.kind === "aws") {
      const wrap = accEl("div", "ac-sso");
      if (s.need === "sso") wrap.appendChild(renderSSOBody(s, { bare: true }));
      else wrap.appendChild(accEl("div", "ac-note", "AWS sign-in expired. The sign-in card appears here once the agent is no longer waiting on a prompt."));
      blk.appendChild(wrap);
    }
    if (d && d.conn === r.name && r.grant) blk.appendChild(accPanel(r, d, ctx));
    card.appendChild(blk);
  }
  return card;
}

function accEventsCard(m, d, nowMs, sid) {
  const card = accEl("section", "ac-card");
  const top = accCardHead("Recent credential events", "last " + ACCESS_EVENTS + " · ");
  const full = accEl("button", "ac-link", "full history in Audit");
  full.type = "button";
  full.addEventListener("click", () => openAudit({ session: sid }));
  top.lastChild.appendChild(full);
  card.appendChild(top);
  if (!m.events.length) {
    const msg = d && d.failed && !d.loaded ? "Could not read the credential log." : d && d.loaded ? "No credential events yet." : "Loading…";
    card.appendChild(accEl("div", "ac-empty", msg));
    return card;
  }
  const tbl = accEl("div", "ac-ev");
  tbl.setAttribute("role", "table");
  const head = accEl("div", "ac-evrow ac-evhead");
  head.setAttribute("role", "row");
  for (const h of ["Time", "Provider", "Subject", "Outcome", "Elevated"]) head.appendChild(accEl("div", "ac-lab", h));
  tbl.appendChild(head);
  for (const e of m.events) {
    const r = accEl("div", "ac-evrow");
    r.setAttribute("role", "row");
    r.appendChild(accEl("div", "ac-evt", accEventTime(e.ts, nowMs)));
    r.appendChild(accEl("div", "", e.provider));
    const subj = accEl("div", "ac-evs mono", e.subject);
    if (e.detail) {
      subj.title = e.detail;
      subj.appendChild(accEl("div", "ac-sub", e.detail));
    }
    r.appendChild(subj);
    r.appendChild(accEl("div", e.tone === "ok" ? "ac-ok" : e.tone === "bad" ? "ac-bad" : "", e.outcome));
    r.appendChild(accEl("div", e.elevated ? "ac-write" : "", e.provider === "approval" ? "" : e.elevated ? "yes" : "no"));
    tbl.appendChild(r);
  }
  card.appendChild(tbl);
  if (d && d.loaded && !m.approvalsKnown && m.creds.rows.length) {
    card.appendChild(accEl("div", "ac-note", "Write-window decisions are unavailable right now."));
  }
  return card;
}

// --- the view --------------------------------------------------------------------

function mountAccess(host, sid0) {
  let sid = sid0;
  let disposed = false;
  let model = null;
  let lastSsoFor = "";
  const ctx = { sid, focusReason: false, repaint: null, rowByName: (name) => (model ? model.creds.rows.find((r) => r.name === name) : null) };
  const wrap = accEl("div", "ac-wrap");
  const slots = { cont: accEl("div"), creds: accEl("div"), events: accEl("div") };
  const keys = { cont: "", creds: "", events: "" };
  wrap.append(slots.cont, slots.creds, slots.events);
  host.replaceChildren(wrap);

  // A field of the grant panel has focus: leave the credentials section alone.
  const typing = () => {
    const a = document.activeElement;
    return !!a && host.contains(a) && !!a.dataset && !!a.dataset.ac;
  };
  const put = (slot, node) => { slot.replaceChildren(...(node ? (Array.isArray(node) ? node : [node]) : [])); };

  function paint(force) {
    if (disposed) return;
    const s = lastSessions.find((x) => x.id === sid);
    if (!s) {
      keys.cont = keys.creds = keys.events = "";
      put(slots.cont, accEl("div", "ac-empty", "This session is no longer listed."));
      put(slots.creds, null);
      put(slots.events, null);
      return;
    }
    const d = accData.get(sid) || null;
    const tab = typeof terminalTabFor === "function" ? terminalTabFor(sid) : null;
    const usage = tab && typeof tab.usage === "function" ? tab.usage() : null;
    const now = Date.now();
    model = accessModel({ session: s, aws: d && d.aws, egress: d && d.egress,
      meta: { box_runtime: typeof boxRuntime === "string" ? boxRuntime : "" }, usage, nowMs: now });
    ctx.sid = sid;
    ctx.repaint = paint;

    const k1 = JSON.stringify([model.containment, s.exited ? 1 : 0, s.runtime]);
    if (force || k1 !== keys.cont) { keys.cont = k1; put(slots.cont, accContainment(model.containment, s)); }

    // A draft whose connection can no longer be elevated is dropped.
    const dr = accDrafts.get(sid);
    if (dr) {
      const r = model.creds.rows.find((x) => x.name === dr.conn);
      if (!r || !r.grant) accDrafts.delete(sid);
    }
    const sso = s.need === "sso" && model.creds.ssoFor ? JSON.stringify([ssoModel(s), ssoStarting.has(sid)]) : "";
    const hasCreds = model.creds.rows.length > 0;
    const k2 = JSON.stringify([model.creds, sso, s.need === "sso", accDrafts.get(sid) ? accDrafts.get(sid).conn : "",
      accBusy.has(sid), accErrs.get(sid) || ""]);
    if (!hasCreds) {
      if (keys.creds !== "-") { keys.creds = "-"; put(slots.creds, null); }
    } else if ((force || k2 !== keys.creds) && (force || !typing())) {
      keys.creds = k2;
      put(slots.creds, accCreds(model, s, ctx));
      if (ctx.focusReason) {
        ctx.focusReason = false;
        const f = host.querySelector('[data-ac="reason"]');
        if (f) f.focus();
      }
    }

    const k3 = JSON.stringify([model.events, model.approvalsKnown, hasCreds, d ? [d.loaded, d.failed] : 0, new Date(now).getDate()]);
    if (!hasCreds && !model.events.length) {
      if (keys.events !== "-") { keys.events = "-"; put(slots.events, null); }
    } else if (force || k3 !== keys.events) { keys.events = k3; put(slots.events, accEventsCard(model, d, now, sid)); }

    if (lastSsoFor && !model.creds.ssoFor) accLoad(sid, true); // signed in: re-read the status
    lastSsoFor = model.creds.ssoFor;
  }

  accViews.set(sid, paint);
  accLoad(sid, true);
  paint(true);

  return {
    update(next) {
      if (next && next !== sid) {
        if (accViews.get(sid) === paint) accViews.delete(sid);
        sid = next;
        accViews.set(sid, paint);
        keys.cont = keys.creds = keys.events = "";
        // Focus in the old session's grant panel would make typing() hold the
        // credentials section back, leaving that panel (and its Enter) under
        // the new session's containment card. Drop the focus and repaint it
        // all; drafts are keyed by sid, so each session keeps its own.
        const a = document.activeElement;
        if (a && host.contains(a) && typeof a.blur === "function") a.blur();
        ctx.focusReason = false;
        accLoad(sid, false);
        paint(true);
        return;
      }
      accPrune();
      accLoad(sid, false);
      paint(false);
    },
    dispose() {
      disposed = true;
      if (accViews.get(sid) === paint) accViews.delete(sid);
    },
  };
}

paneViewRenderers.access = mountAccess;
