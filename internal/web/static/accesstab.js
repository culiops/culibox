"use strict";

// The Access view (desktop pane view, spec 5.7): what the pane's agent session
// may touch. Containment, cloud credentials with Elevate / Extend and the SSO
// sign-in card, and the last credential events. The model is accessmodel.js;
// this file draws it. Every server- and operator-supplied string (a connection
// name, a label, a reason, a path, an error) goes in through textContent, and
// the SSO card is app.js renderSSOBody's.
//
// It is drawn from the M6 frames (M6.3 read, M6.4 elevated) on the kit's own
// classes (.card, .card-head, .chip, .btn, .link, .tbl, .seg, .input); desktop.css
// has only the layout. The view carries no Drop: the pane's one Drop now is the
// banner's (elevbanner.js), which reads accData, accLoad, accDrop, accBusy and
// accViews from here. While a window is open the connection row says
// "write (elevated)" and offers Extend near the end; once it is over the row keeps
// "Write access ended HH:MM" and, while write credentials already issued are
// still valid, says until when.
//
// The host is volatile (a tab close or dock change re-attaches it) and the poll
// repaints every 3 s, so nothing the operator types lives in the DOM alone: the
// grant panel's draft is in accDrafts, per session, and survives both a repaint
// and a remount. A section repaints only when its signature changes, and the
// credentials section does not repaint at all while one of its fields has focus.
// A button the keyboard may be on is never [disabled] (a focused button that turns
// disabled drops focus to <body>): while a request is out it is aria-disabled and
// its click handler refuses, and a rebuilt section hands the keyboard back to the
// control it held, found by its identity (accKey: the connection and the action).

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

// accFill: put `content` (a string, or a list of nodes and strings) into `el`.
function accFill(el, content) {
  if (typeof content === "string") { el.textContent = content; return el; }
  for (const n of content) el.appendChild(typeof n === "string" ? document.createTextNode(n) : n);
  return el;
}

// accKey: mark a button with what it IS, a connection (or none) and an action, so a rebuilt section can hand the keyboard
// back to the same control however the buttons around it moved (fill below). Not a field: typing() reads data-ac only.
function accKey(btn, conn, act) {
  btn.dataset.acConn = conn || "";
  btn.dataset.acAct = act;
  return btn;
}

// accCell: a small label over a value, one column of a card row (§A3: span.t-sm.muted over span.t-md). The value is
// text or a list of nodes (a chip, a tone span); `title` rides on the value.
function accCell(label, content, title) {
  const c = accEl("div", "ac-cell");
  c.appendChild(accEl("span", "t-sm muted", label));
  const v = accFill(accEl("span", "t-md"), content);
  if (title) v.title = title;
  c.appendChild(v);
  return c;
}

// accCardHead: h2, the spacer, then whatever sits at the right end (§A3).
function accCardHead(title, ...rest) {
  const h = accEl("div", "card-head");
  h.appendChild(accEl("h2", "t-md w-600", title));
  h.appendChild(accEl("span", "ph-spacer"));
  for (const n of rest) if (n) h.appendChild(n);
  return h;
}

// --- data ----------------------------------------------------------------------

function accBound(s) {
  return !!s && (!!s.aws || (Array.isArray(s.aws_connections) && s.aws_connections.length > 0) || !!s.github ||
    (Array.isArray(s.tokens) && s.tokens.length > 0));
}

// accLoad: read the broker status (and the egress line of an unbound box) for
// sid. At most once per ACCESS_REFRESH_MS unless forced. A failed read keeps what
// was there: only a 200 or a 204 replaces it. It returns the read's promise, which
// settles once the answer is stored and the Access view repainted (elevbanner.js
// re-syncs the panes then), and undefined when no read was started.
function accLoad(sid, force) {
  let d = accData.get(sid);
  if (!d) { d = { aws: null, egress: null, at: 0, inflight: false, loaded: false, failed: false }; accData.set(sid, d); }
  if (d.inflight || (!force && d.at && Date.now() - d.at < ACCESS_REFRESH_MS)) return;
  const s = lastSessions.find((x) => x.id === sid);
  if (!s) return;
  d.inflight = true;
  const get = (tail) => fetch("/api/sessions/" + encodeURIComponent(sid) + tail).then(accReadReply, () => undefined);
  return (async () => {
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
  const card = accEl("section", "card ac-cont");
  card.appendChild(accCardHead("Containment", accEl("span", "mono t-sm muted", c.tier + (c.runtime ? " · " + c.runtime : ""))));
  const cells = accEl("div", "ac-cells");
  const host = c.tier === "host";
  const badge = typeof tierBadge === "function" ? tierBadge(s, typeof boxRuntime === "string" ? boxRuntime : "") : null;
  const chip = accEl("span", "chip chip-fg " + (host ? "chip-host" : "chip-box"), c.tier);
  if (badge) chip.title = badge.title;
  const tier = accCell("Tier", [chip]);
  if (host) tier.appendChild(accEl("span", "t-sm muted", "not contained"));
  cells.appendChild(tier);
  cells.appendChild(accCell("Workspace", c.workspace ? [accEl("span", "mono t-sm", shortPath(c.workspace))] : "—", c.workspace));
  const n = c.network;
  const net = !n.mode ? "no egress control"
    : n.mode === "open" ? "open · " + n.denied + " denied"
      : "allowlist · " + n.allowed + " allowed · " + n.denied + " denied";
  cells.appendChild(accCell("Network", net, n.mode ? "Counted since the daemon started." : ""));
  cells.appendChild(accCell("Agent account", c.account));
  card.appendChild(cells);

  // Label / value pairs under the cells (§A3: Started, Model). The product adds a row only when the session has
  // something to say: effort without a model, the readable host paths, a clone's source and its warnings, what the
  // bundle left out. A multi-line value is a block; the rest are spans.
  const kvs = accEl("div", "ac-kvs");
  const kv = (label, content, cls, block) => {
    kvs.appendChild(accEl("span", "t-sm muted", label));
    kvs.appendChild(accFill(accEl(block ? "div" : "span", "t-md" + (cls ? " " + cls : "")), content));
  };
  if (c.started) kv("Started", c.started, "tnum");
  if (c.model) kv("Model", [accEl("span", "mono t-sm", c.model)].concat(c.effort ? [" · " + c.effort] : []));
  else if (c.effort) kv("Effort", c.effort);
  if (c.readable.length) kv("Readable host paths", c.readable.map((p) => accEl("div", "mono t-sm", p)), "", true);
  if (c.clone) {
    const lines = [];
    if (c.clone.of) lines.push(accEl("div", "mono t-sm", c.clone.of));
    for (const w of c.clone.warn) lines.push(accEl("div", "t-sm w-600", w));
    for (const i of c.clone.info) lines.push(accEl("div", "t-sm muted", i));
    kv("Cloned from", lines, "", true);
  }
  if (c.dropped) kv("Left out of the box", c.dropped.v);
  if (c.hostDropped) kv(c.hostDropped.k, c.hostDropped.v);
  if (c.locked) kv("Locked files", c.locked.v);
  // #95: a profile edit changed this session's egress while it runs; a widening reads bold, as a clone warning does.
  if (c.policy) kv("Egress policy", c.policy.v, c.policy.chip ? "w-600" : "");
  if (kvs.children.length) card.appendChild(kvs);
  return card;
}

// accBtn: a row button. While the session has a request out it is aria-disabled and refuses the press; it is never
// [disabled], which would drop the keyboard to <body>. `why` is the reason it cannot act, in its title.
function accBtn(text, off, why, fn, sid, conn, act) {
  const b = accKey(accEl("button", "btn btn-sm", text), conn, act);
  b.type = "button";
  if (off) {
    b.setAttribute("aria-disabled", "true");
    if (why) b.title = why;
  }
  b.onclick = () => { if (!off && !accBusy.has(sid)) fn(); };
  return b;
}

function accRowBlock(r, ctx) {
  const { sid, repaint, rowByName } = ctx;
  const blk = accEl("div", "ac-blk");
  const row = accEl("div", "ac-conn");

  // The first cell: the name (and PROD), the sub-line, then the window lines. The lines say what the account's
  // write access did last: it ended (muted), and credentials issued before still work (red: they are write).
  const id = accEl("div", "ac-cell");
  const nm = accEl("span", "ac-name");
  const name = accEl("span", "mono t-md w-500 ellipsis", r.name);
  name.title = r.name;
  nm.appendChild(name);
  if (r.prod) nm.appendChild(accEl("span", "chip chip-prod", "PROD"));
  id.appendChild(nm);
  if (r.sub) {
    const sub = accEl("span", "t-sm muted ellipsis", r.sub);
    sub.title = r.sub;
    id.appendChild(sub);
  }
  if (r.window && r.window.ended) id.appendChild(accEl("span", "ac-note t-sm muted", r.window.ended));
  if (r.window && r.window.leftover) id.appendChild(accEl("span", "ac-note t-sm c-red", r.window.leftover));
  row.appendChild(id);

  const tone = (cls, text) => [accEl("span", cls, text)];
  const time = (t) => (t ? tone("tnum", t) : "—");
  if (r.kind === "aws") {
    row.appendChild(accCell("Scope", r.scope === "read" ? r.scope : tone("c-red w-500", r.scope)));
    // An expired sign-in is an error (the broker's sso_expired), so it is red; amber is only for an agent blocked on the user.
    row.appendChild(accCell("SSO", r.sso === "valid" ? tone("c-ok", r.sso) : r.sso === "expired" ? tone("c-red", r.sso) : r.sso || "—"));
    row.appendChild(accCell("Last credential", time(r.last)));
  } else {
    row.appendChild(accCell("Scope", r.scope || "—"));
    row.appendChild(accCell("Status", r.status === "ok" ? tone("c-ok", r.status) : r.status || "—"));
    row.appendChild(accCell(r.kind === "github" ? "Last token" : "Last fetch", time(r.last)));
  }

  // No Drop here: the pane's one Drop now is the banner's. A closed connection offers Elevate…, an open one near
  // its end offers Extend.
  const acts = accEl("div", "ac-acts");
  const busy = accBusy.has(sid);
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
  if (r.actions.includes("elevate")) acts.appendChild(accBtn("Elevate…", busy || !!(g && g.blockedText), g && g.blockedText, openPanel, sid, r.name, "elevate"));
  if (r.actions.includes("extend")) acts.appendChild(accBtn("Extend", busy, "", openPanel, sid, r.name, "extend"));
  row.appendChild(acts);
  blk.appendChild(row);

  // Under the row, as the frame draws the base-role note: one line each, pulled up into the row's padding.
  const line = (cls, text, title) => {
    const n = accEl("div", "ac-connnote");
    const t = accEl("span", cls, text);
    if (title) t.title = title;
    n.appendChild(t);
    blk.appendChild(n);
  };
  if (r.kind === "aws") {
    if (r.base) line("t-sm muted", "Base role: " + r.base, r.baseTitle);
    if (r.stat) line("t-sm c-red", r.stat);
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

  const lead = accEl("p", "t-md");
  lead.append(document.createTextNode((g.extend ? "Extend write on " : "Grant write on ")));
  lead.appendChild(accEl("span", "mono w-500", r.name));
  lead.append(document.createTextNode(" to this session. Credentials already issued stay valid until they expire."));
  box.appendChild(lead);

  if (!g.windows.includes(d.minutes)) d.minutes = g.windows[0] || 0;
  const fields = accEl("div", "ac-pfields");

  const wcol = accEl("div", "ac-pcol");
  wcol.appendChild(accEl("span", "t-sm muted", "Window"));
  const seg = accEl("div", "seg");
  seg.setAttribute("role", "group");
  seg.setAttribute("aria-label", "Window");
  const segBtns = g.windows.map((w) => {
    const b = accKey(accEl("button", "seg-item", w + " min"), r.name, "window-" + w);
    b.type = "button";
    b.onclick = () => { d.minutes = w; sync(); };
    seg.appendChild(b);
    return [w, b];
  });
  wcol.appendChild(seg);
  fields.appendChild(wcol);

  const rcol = accEl("div", "ac-pcol ac-pgrow");
  const rlab = accEl("label", "t-sm muted", "Reason (recorded)");
  const reason = accEl("input", "input");
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
    const lcol = accEl("div", "ac-pcol ac-pgrow");
    const llab = accEl("label", "t-sm muted");
    llab.append(document.createTextNode("Type "));
    llab.appendChild(accEl("span", "mono", g.label));
    llab.append(document.createTextNode(" to confirm"));
    typed = accEl("input", "input mono");
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
  box.appendChild(accEl("p", "t-sm muted", "Role: " + g.role));
  // What this window can reach beyond the base role: the user must see the opt-ins and the PassRole limit.
  const reach = (lead, name, tail) => {
    const p = accEl("p", "t-sm muted");
    p.append(document.createTextNode(lead));
    p.appendChild(accEl("span", "mono", name));
    if (tail) p.append(document.createTextNode(tail));
    box.appendChild(p);
  };
  if (g.boxPrefix) reach("Can read objects in buckets named ", g.boxPrefix + "*");
  if (g.passRolePrefix) reach("Can pass roles named ", g.passRolePrefix + "*", ". Code running as one of these roles is not bound by this window's blocks");
  else box.appendChild(accEl("p", "t-sm muted", "Cannot pass IAM roles (set pass_role_prefix to allow Lambda/ECS applies)"));
  // #307: bucket policies, ACLs and public access blocks are denied in the window except on box buckets.
  if (g.boxPrefix) reach("Can change bucket policies, ACLs and public access blocks only on buckets named ", g.boxPrefix + "*", ", so those can be made public");
  else box.appendChild(accEl("p", "t-sm muted", "Cannot change bucket policies, ACLs or public access blocks"));
  // #324: the other denied data's resource policies are denied in every window, with no exception; KMS is not.
  box.appendChild(accEl("p", "t-sm muted", "Cannot change the resource policies of DynamoDB tables, SSM parameters or secrets. KMS key policies and grants can still change"));
  if (g.allowIAM) box.appendChild(accEl("div", "ac-warn", "With IAM changes allowed, the agent can create and use its own roles, so data-read blocks are advisory in this window."));

  const hint = accEl("p", "ac-hint t-sm muted");
  box.appendChild(hint);
  const err = accEl("p", "ac-err t-sm", accErrs.get(sid) || "");
  err.setAttribute("role", "alert");
  err.hidden = !err.textContent;
  box.appendChild(err);

  // Both buttons are aria-disabled, never [disabled], while a request is out or the form is incomplete: the keyboard
  // may be on either, and a disabled button would drop it to <body>. Cancel refuses a press while busy; Grant's
  // refusal is accGrant's own (it re-checks the form and the busy set).
  const btns = accEl("div", "ac-pbtns");
  const cancel = accKey(accEl("button", "btn", "Cancel"), r.name, "cancel");
  cancel.type = "button";
  if (accBusy.has(sid)) cancel.setAttribute("aria-disabled", "true");
  cancel.onclick = () => { if (accBusy.has(sid)) return; accDrafts.delete(sid); accErrs.delete(sid); repaint(true); };
  const grant = accKey(accEl("button", "btn btn-primary"), r.name, "grant");
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
  // the real state cannot disagree. A double click sends one request: accGrant
  // refuses while accBusy holds the session.
  function sync() {
    for (const [w, b] of segBtns) b.setAttribute("aria-pressed", w === d.minutes ? "true" : "false");
    const busy = accBusy.has(sid);
    const fs = grantFormState({ minutes: d.minutes, reason: d.reason, typed: d.typed, prod: g.needLabel, label: g.label, busy });
    const ok = fs.valid && !g.blocked && !g.blockedText;
    grant.textContent = (g.extend ? "Extend" : "Grant write") + (d.minutes ? " · " + d.minutes + " min" : "");
    if (ok) grant.removeAttribute("aria-disabled"); else grant.setAttribute("aria-disabled", "true");
    // At work, not closed: the kit dims aria-disabled but not aria-busy.
    if (busy) grant.setAttribute("aria-busy", "true"); else grant.removeAttribute("aria-busy");
    hint.textContent = ok ? "" : HINTS[fs.why] || "";
  }
  const onEnter = (e) => { if (e.key === "Enter" && grant.getAttribute("aria-disabled") !== "true") { e.preventDefault(); doGrant(); } };
  reason.onkeydown = onEnter;
  if (typed) typed.onkeydown = onEnter;
  sync();
  return box;
}

function accCreds(m, s, ctx) {
  const { sid } = ctx;
  const card = accEl("section", "card ac-creds");
  card.appendChild(accCardHead("Cloud credentials", m.creds.head ? accEl("span", "t-sm muted tnum", m.creds.head) : null));
  const d = accDrafts.get(sid);
  for (const r of m.creds.rows) {
    const blk = accRowBlock(r, ctx);
    if (m.creds.ssoFor === r.name && r.kind === "aws") {
      const wrap = accEl("div", "ac-sso");
      if (s.need === "sso") wrap.appendChild(renderSSOBody(s, { bare: true }));
      else wrap.appendChild(accEl("p", "t-sm muted", "AWS sign-in expired. The sign-in card appears here once the agent is no longer waiting on a prompt."));
      blk.appendChild(wrap);
    }
    if (d && d.conn === r.name && r.grant) blk.appendChild(accPanel(r, d, ctx));
    card.appendChild(blk);
  }
  return card;
}

// accEventRow: one credential event (§A3, §E4). The tone of an Outcome sits on a span in an unclassed cell: a mint is
// green, a broker error red, any other word (a refusal is containment working) muted; a person's decision is plain
// text. "yes" in Elevated is red 500, "no" plain, an approval's blank. Only the model's tint rows are tinted.
function accEventRow(e, nowMs) {
  const tr = accEl("tr", e.tint ? "is-elevated" : "");
  tr.appendChild(accEl("td", "sub tnum", accEventTime(e.ts, nowMs)));
  tr.appendChild(accEl("td", "sub", e.provider));
  const subj = accEl("td", "ac-subj");
  if (e.provider === "approval") {
    // "granted 30 min on culipulse-prod": the name is the part after the first " on ", mono as everywhere else.
    const at = e.subject.indexOf(" on ");
    if (at >= 0) accFill(subj, [e.subject.slice(0, at + 4), accEl("span", "mono t-sm", e.subject.slice(at + 4))]);
    else subj.textContent = e.subject;
  } else {
    subj.appendChild(accEl("span", "mono t-sm", e.subject));
  }
  if (e.detail) {
    subj.appendChild(accEl("span", "muted", " · “" + e.detail + "”"));
    subj.title = e.detail;
  }
  // A mint made under a full-access acknowledgement: a tag, and the gate's verdict in the cell's title.
  if (e.fullAccess) {
    subj.appendChild(accEl("span", "c-red t-sm w-500", " \u00b7 full access"));
    subj.title = e.fullAccess;
  }
  tr.appendChild(subj);
  const out = accEl("td");
  if (e.provider === "approval") out.textContent = e.outcome;
  else if (e.outcome) out.appendChild(accEl("span", e.tone === "ok" ? "c-ok" : e.tone === "bad" ? "c-red" : "muted", e.outcome));
  tr.appendChild(out);
  const el = accEl("td");
  if (e.elevatedText === "yes") el.appendChild(accEl("span", "c-red w-500", "yes"));
  else el.textContent = e.elevatedText;
  tr.appendChild(el);
  return tr;
}

function accEventsCard(m, d, nowMs, sid) {
  const card = accEl("section", "card ac-events");
  const full = accKey(accEl("button", "link t-sm", "Full history in Audit"), "", "audit");
  full.type = "button";
  full.onclick = () => openAudit({ session: sid });
  card.appendChild(accCardHead("Recent credential events", accEl("span", "t-sm muted", "Last " + ACCESS_EVENTS + " ·"), full));
  if (!m.events.length) {
    const msg = d && d.failed && !d.loaded ? "Could not read the credential log." : d && d.loaded ? "No credential events yet." : "Loading…";
    card.appendChild(accEl("div", "ac-empty", msg));
    return card;
  }
  const table = accEl("table", "tbl ac-tbl");
  const thead = accEl("thead");
  const hr = accEl("tr");
  for (const h of ["Time", "Provider", "Subject", "Outcome", "Elevated"]) {
    const th = accEl("th", "", h);
    th.setAttribute("scope", "col");
    hr.appendChild(th);
  }
  thead.appendChild(hr);
  const body = accEl("tbody");
  for (const e of m.events) body.appendChild(accEventRow(e, nowMs));
  table.append(thead, body);
  card.appendChild(table);
  if (d && d.loaded && !m.approvalsKnown && m.creds.rows.length) {
    card.appendChild(accEl("div", "ac-empty", "Write-window decisions are unavailable right now."));
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
  // fill: rebuild a section, and give the control the keyboard was in back to the same one in the new content, by what
  // it is and not by where it sat: a grant field by its data-ac, a button by its connection and action (accKey). A
  // button of the SSO card (app.js's, so it has no key) is found by its place among the section's unkeyed buttons. A
  // control that is gone goes to its connection's own Elevate… or Extend (a closed panel's Cancel lands on its row's
  // action), and failing that to the view, never to <body> and never to another connection's button.
  const keyed = (b) => !!b.dataset && b.dataset.acAct !== undefined;
  const fill = (slot, node) => {
    const a = document.activeElement;
    const held = !!a && slot.contains(a);
    const field = held && a.dataset ? a.dataset.ac || "" : "";
    const id = held && !field && keyed(a) ? { conn: a.dataset.acConn || "", act: a.dataset.acAct } : null;
    const at = held && !field && !id ? [...slot.querySelectorAll("button")].filter((b) => !keyed(b)).indexOf(a) : -1;
    slot.replaceChildren(...(node ? [node] : []));
    if (!held) return;
    const buttons = [...slot.querySelectorAll("button")];
    const find = (conn, act) => buttons.find((b) => keyed(b) && (b.dataset.acConn || "") === conn && b.dataset.acAct === act);
    let next = null;
    if (field) next = slot.querySelector('[data-ac="' + field + '"]');
    else if (id) next = find(id.conn, id.act) || (id.conn ? find(id.conn, "elevate") || find(id.conn, "extend") : null);
    else if (at >= 0) next = buttons.filter((b) => !keyed(b))[at];
    (next || host).focus({ preventScroll: true });
  };

  function paint(force) {
    if (disposed) return;
    const s = lastSessions.find((x) => x.id === sid);
    if (!s) {
      keys.cont = keys.creds = keys.events = "";
      fill(slots.cont, accEl("div", "ac-empty", "This session is no longer listed."));
      fill(slots.creds, null);
      fill(slots.events, null);
      return;
    }
    const d = accData.get(sid) || null;
    const tab = typeof terminalTabFor === "function" ? terminalTabFor(sid) : null;
    const usage = tab && typeof tab.usage === "function" ? tab.usage() : null;
    const now = Date.now();
    model = accessModel({ session: s, aws: d && d.aws, egress: d && d.egress,
      meta: { box_runtime: typeof boxRuntime === "string" ? boxRuntime : "" }, usage, nowMs: now, desktop: true }); // pane views are desktop-only
    ctx.sid = sid;
    ctx.repaint = paint;

    const k1 = JSON.stringify([model.containment, s.exited ? 1 : 0, s.runtime]);
    if (force || k1 !== keys.cont) { keys.cont = k1; fill(slots.cont, accContainment(model.containment, s)); }

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
      if (keys.creds !== "-") { keys.creds = "-"; fill(slots.creds, null); }
    } else if ((force || k2 !== keys.creds) && (force || !typing())) {
      keys.creds = k2;
      fill(slots.creds, accCreds(model, s, ctx));
      if (ctx.focusReason) {
        ctx.focusReason = false;
        const f = host.querySelector('[data-ac="reason"]');
        if (f) f.focus();
      }
    }

    const k3 = JSON.stringify([model.events, model.approvalsKnown, hasCreds, d ? [d.loaded, d.failed] : 0, new Date(now).getDate()]);
    if (!hasCreds && !model.events.length) {
      if (keys.events !== "-") { keys.events = "-"; fill(slots.events, null); }
    } else if (force || k3 !== keys.events) { keys.events = k3; fill(slots.events, accEventsCard(model, d, now, sid)); }

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
