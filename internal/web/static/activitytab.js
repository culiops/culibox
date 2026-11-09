"use strict";

// The Activity view (desktop pane view, spec 5.8): a live, newest-first timeline
// of the pane's agent session, the context it spent by tool, the files it changed,
// its latest message and the reply box. The model is activitymodel.js; this file
// draws it. Every server- and agent-supplied string (a command, a path, a message,
// a tool name) goes in through textContent: a box agent writes the transcript the
// rows come from, so nothing here is ever parsed as markup.
//
// The host is volatile (a tab close or dock change re-attaches it) and the poll
// calls update() every 3 s, so nothing the operator did lives in the DOM alone:
// the filter, the older pages and the reply draft are in actStates / replyDraft,
// per session, and survive a repaint and a remount. A section repaints only when
// its signature changes, and a control the operator is using (a filter item, Load
// older, Scan) is never rebuilt under their keyboard.
//
// It is drawn from the M6 frames on the kit's own classes (.kpi-row, .seg, .card,
// .tbl, .btn, .meter, .empty-state); desktop.css has only the layout. Rows are
// not controls: a command is wrapped as the frame draws it and its title keeps
// the whole text, so there is nothing to expand.

const ACT_PAGE = 100;

// Per session id, kept across remounts of the view:
//   filter, resp (the newest page, refetched each
//   poll), older (pages loaded by "Load older"), pinned, olderMore, loadingMore,
//   olderErr, loaded / failed / inflight / at (actLoadDone), seq + ctl (the read
//   in flight), attr ({status, data, failed}), attrAsked.
const actStates = new Map();
const actViews = new Map(); // sid -> repaint(force) of the view mounted for it

function actState(sid) {
  let st = actStates.get(sid);
  if (!st) {
    st = {
      filter: "all", resp: null, older: [], pinned: false, olderMore: null,
      loadingMore: false, olderErr: "", loaded: false, failed: false, inflight: false, at: 0,
      seq: 0, ctl: null, attr: { status: "idle", data: null, failed: false }, attrAsked: false,
    };
    actStates.set(sid, st);
  }
  return st;
}

function actEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function actPrune() {
  if (!lastSessions.length) return;
  const live = new Set(lastSessions.map((s) => s.id));
  for (const id of [...actStates.keys()]) {
    if (!live.has(id)) {
      const st = actStates.get(id);
      st.seq++; // a read still out writes nothing
      if (st.ctl) st.ctl.abort();
      actStates.delete(id);
    }
  }
}

const actUrl = (sid, q) => "/api/sessions/" + encodeURIComponent(sid) + "/activity?" + q;

// --- data ----------------------------------------------------------------------

// actFetch: read the newest page for sid, aborting the read before it. Total: a
// non-JSON 200, a refusal and a thrown error all end the read and keep what was
// there; a read that was superseded (or whose view went away) writes nothing.
function actFetch(sid) {
  const st = actState(sid);
  if (st.ctl) st.ctl.abort();
  const ctl = new AbortController();
  st.ctl = ctl;
  const seq = ++st.seq;
  st.inflight = true;
  (async () => {
    let reply;
    try {
      reply = await actReadReply(await fetch(actUrl(sid, "limit=" + ACT_PAGE), { signal: ctl.signal }));
    } catch (_) { reply = undefined; }
    if (seq !== st.seq) return; // another read owns the state now
    const prev = st.resp && Array.isArray(st.resp.events) ? st.resp.events : [];
    if (reply) st.older = actSlide(prev, reply.events, st.older, st.pinned);
    Object.assign(st, actLoadDone(st, reply, Date.now()));
    st.ctl = null;
    const v = actViews.get(sid);
    if (v) v(false);
  })();
}

// actLoadOlder: the page below everything held. before is the oldest ts received,
// and the page is appended as it came: the server finishes a same-ms group, so
// nothing is dropped or deduplicated here.
async function actLoadOlder(sid) {
  const st = actState(sid);
  if (st.loadingMore) return;
  const before = actOldest(st.resp ? st.resp.events : [], st.older);
  if (!before) return;
  st.pinned = true;
  st.loadingMore = true;
  st.olderErr = "";
  const v0 = actViews.get(sid);
  if (v0) v0(false);
  let reply;
  try {
    reply = await actReadReply(await fetch(actUrl(sid, "limit=" + ACT_PAGE + "&before=" + before)));
  } catch (_) { reply = undefined; }
  st.loadingMore = false;
  if (reply) {
    st.older = st.older.concat(reply.events);
    st.olderMore = !!reply.more;
    if (reply.partial && st.resp) st.resp = Object.assign({}, st.resp, { partial: true });
  } else {
    st.olderErr = "Could not load older activity.";
  }
  const v = actViews.get(sid);
  if (v) v(false);
}

// actScan: the context-by-tool read (GET .../attribution, a whole-transcript scan,
// so on first open and on demand, never per poll). It is scanAttribution's request
// and attributionRows' formatting; scanAttribution itself paints the steer rail's
// hidden section and drops a result unless the rail's session is this one, which
// on desktop is whichever pane has focus.
async function actScan(sid) {
  const st = actState(sid);
  if (st.attr.status === "loading") return;
  st.attr = { status: "loading", data: st.attr.data, failed: false };
  const v0 = actViews.get(sid);
  if (v0) v0(false);
  let data = null;
  try {
    const j = await api("/api/sessions/" + encodeURIComponent(sid) + "/attribution");
    if (j && typeof j === "object" && Array.isArray(j.tools)) data = j;
  } catch (_) { data = null; }
  st.attr = data ? { status: "done", data, failed: false } : { status: "done", data: st.attr.data, failed: true };
  const v = actViews.get(sid);
  if (v) v(false);
}

// --- sections --------------------------------------------------------------------

// actTiles: the kit's KPI row, one .kpi per tile. A session with no transcript has no tiles, so no row. The
// label ellipsizes in a narrow pane, so it carries its whole text.
function actTiles(tiles) {
  const row = actEl("div", "kpi-row");
  for (const t of tiles) {
    const c = actEl("div", "kpi");
    const label = actEl("span", "kpi-label", t.label);
    label.title = t.label;
    c.append(actEl("span", "kpi-value", String(t.n)), label);
    row.appendChild(c);
  }
  return row;
}

// The tone class an outcome cell wears (the model's tone words). Green is status, red is an error or write
// (a refusal is containment working: muted), amber is the ask only, and a diff's +a −d has no tone.
const ACT_TONE_CLS = { ok: "c-ok", err: "c-red", amber: "c-amber", muted: "sub", add: "" };

// actOutcome: the right-aligned cell, its text one word of one colour role.
function actOutcome(o) {
  const tone = ACT_TONE_CLS[o.tone];
  return actEl("td", tone ? "num " + tone : "num", o.text);
}

// actRowEl: one event as the frame draws it. Time and Kind are muted, except the You row's Kind (foreground, 500)
// and the ask's (amber). The Detail wraps (a capped, one-line command is never cut off) in mono, except a message
// of yours (sans) and "conversation cleared" (muted). The row is not a control and its title keeps the full text.
function actRowEl(r) {
  const tr = actEl("tr", r.cls === "ask" ? "act-ask" : "");
  tr.title = r.detail;
  const you = r.cls === "you" && r.kind === "You";
  const cleared = r.cls === "you" && !you;
  tr.appendChild(actEl("td", "sub tnum", r.time));
  // The Kind cell is 80px and ellipsizes: an MCP tool is `mcp__server__tool`, so the whole kind rides in its title.
  const kind = actEl("td", you ? "w-500" : r.cls === "ask" ? "c-amber" : "sub", r.kind);
  kind.title = r.kind;
  tr.appendChild(kind);
  const det = actEl("td", "act-det");
  det.appendChild(actEl("span", you ? "w-500" : cleared ? "muted" : "mono t-sm", r.detail));
  tr.appendChild(det);
  tr.appendChild(actOutcome(r.outcome));
  return tr;
}

// actTable: the card around the event table. With no rows its one row says why (`empty`).
function actTable(rows, empty) {
  const card = actEl("div", "card act-card");
  const table = actEl("table", "tbl act-tbl");
  const thead = actEl("thead");
  const hr = actEl("tr");
  for (const [text, cls] of [["Time", ""], ["Kind", ""], ["Detail", ""], ["Outcome", "num"]]) {
    const th = actEl("th", cls, text);
    th.setAttribute("scope", "col");
    hr.appendChild(th);
  }
  thead.appendChild(hr);
  const body = actEl("tbody");
  if (rows.length) {
    for (const r of rows) body.appendChild(actRowEl(r));
  } else {
    const td = actEl("td", "act-empty", empty);
    td.setAttribute("colspan", "4");
    body.appendChild(actEl("tr")).appendChild(td);
  }
  table.append(thead, body);
  card.appendChild(table);
  return card;
}

// actEmptyText: what the table says when it has no rows.
function actEmptyText(st, filter) {
  if (!st.loaded) return st.failed ? "Could not read the activity." : "Loading…";
  return filter === "all" ? "No activity yet." : "Nothing matches this filter.";
}

// actHead: a card's head, as the frame draws it: the title, a spacer, then a count, a share or a button.
function actHead(title, tail) {
  const head = actEl("div", "card-head");
  head.append(actEl("h2", "t-md w-600", title), actEl("span", "ph-spacer"));
  if (tail) head.appendChild(tail);
  return head;
}

function actToolsCard(st, sid) {
  const card = actEl("section", "card");
  // Busy, not [disabled]: a disabled button drops the keyboard the moment it is pressed.
  const loading = st.attr.status === "loading";
  const scan = actEl("button", "btn btn-sm", loading ? "Scanning…" : "Scan");
  scan.type = "button";
  if (loading) { scan.setAttribute("aria-busy", "true"); scan.setAttribute("aria-disabled", "true"); }
  scan.onclick = () => actScan(sid); // actScan ignores a press while a scan is out
  card.appendChild(actHead("Context by tool", scan));
  const body = actEl("div", "act-cardbody");
  const note = (text) => body.appendChild(actEl("p", "t-sm muted", text));
  const t = actToolRows(st.attr.data);
  if (!st.attr.data) {
    note(loading ? "Scanning the transcript…" : st.attr.failed ? "Could not scan the transcript." : "Not scanned yet.");
  } else if (!t.rows.length) {
    note("No tool activity recorded for this session.");
  } else {
    for (const r of t.rows) {
      const row = actEl("div", "act-trow");
      row.title = r.name + " · " + r.calls + (r.calls === 1 ? " call" : " calls") + (r.ctx ? " · " + r.ctx + " tokens of results" : "");
      const meter = actEl("span", "meter");
      const fill = actEl("i");
      fill.style.width = r.pct + "%";
      meter.appendChild(fill);
      row.append(actEl("span", "mono t-sm ellipsis", r.name), meter, actEl("span", "t-sm tnum", r.pct + "%"));
      body.appendChild(row);
    }
    if (t.more) note("and " + t.more + " more");
    if (st.attr.failed) note("The last scan failed; this is the one before.");
    note("Share of tool-result bytes: a proxy for context pressure, not billing.");
  }
  card.appendChild(body);
  return card;
}

function actCtxCard(ctx) {
  if (!ctx) return null;
  const card = actEl("section", "card");
  card.appendChild(actHead("Context", actEl("span", "t-sm muted tnum", ctx.label)));
  const body = actEl("div", "act-cardbody");
  const meter = actEl("span", "meter");
  meter.setAttribute("role", "img");
  meter.setAttribute("aria-label", ctx.text);
  const i = actEl("i");
  i.style.width = ctx.width;
  meter.appendChild(i);
  body.appendChild(meter);
  card.appendChild(body);
  return card;
}

// actFilesCard: the files changed. Until the first reply the count is — and the body says it is loading (or that the
// read failed), never "0" or "No files changed yet." for a list nobody has read.
function actFilesCard(files, st) {
  const card = actEl("section", "card");
  card.appendChild(actHead("Files changed", actEl("span", "t-sm muted tnum", st.loaded ? String(files.length) : "—")));
  const body = actEl("div", "act-cardbody");
  if (!st.loaded) {
    body.appendChild(actEl("p", "t-sm muted", st.failed ? "Could not read the activity." : "Loading…"));
  } else if (!files.length) {
    body.appendChild(actEl("p", "t-sm muted", "No files changed yet."));
  }
  for (const f of files) {
    const row = actEl("div", "act-frow");
    row.title = f.path;
    row.append(actEl("span", "mono t-sm ellipsis", f.path), actEl("span", "t-sm tnum", f.text));
    body.appendChild(row);
  }
  card.appendChild(body);
  return card;
}

// actSyncLive: copy the poll's entry for a session onto the object a composer was
// built around, dropping the fields the entry no longer carries (an omitted `need`
// means "none", and Object.assign alone would keep the stale one).
function actSyncLive(live, s) {
  for (const k of Object.keys(live)) if (!(k in s)) delete live[k];
  Object.assign(live, s);
}

// --- the view --------------------------------------------------------------------

// Two layouts, one host. A Claude session is .act-wrap: the main column (KPI row, latest message, filter bar,
// table, Load older) beside the 320px side column (M6.1). A session with no transcript is .act-plain: one plain
// block centred in the whole view, with no padded column around it (M6.2); a credential event it did have adds
// the table under the block, top-aligned. The slots below move between the two, so the table is built once.
function mountActivity(host, sid0) {
  let sid = sid0;
  let disposed = false;
  let model = null;
  let mode = "";
  const wrap = actEl("div", "act-wrap");
  const main = actEl("div", "act-main");
  const side = actEl("div", "act-side");
  const plain = { root: actEl("div", "act-plain"), block: actEl("div", "empty-state act-empty-state"), list: actEl("div", "act-plainlist") };
  const slots = { msg: actEl("div"), tiles: actEl("div"), bar: actEl("div"), list: actEl("div"), foot: actEl("div"),
    tools: actEl("div"), ctx: actEl("div"), files: actEl("div") };
  const keys = { msg: "", tiles: "", list: "", foot: "", tools: "", ctx: "", files: "", empty: "" };
  side.append(slots.ctx, slots.tools, slots.files);

  // layout: put the slots where this kind of session draws them.
  function layout(next) {
    if (next === mode) return;
    mode = next;
    if (next === "plain") {
      plain.list.replaceChildren(slots.list, slots.foot);
      plain.root.replaceChildren(plain.block, plain.list);
      host.replaceChildren(plain.root);
    } else {
      main.replaceChildren(slots.tiles, slots.msg, slots.bar, slots.list, slots.foot);
      wrap.replaceChildren(main, side);
      host.replaceChildren(wrap);
    }
  }
  layout("claude");

  // The reply box. Built once around `live` (a copy of the session entry the poll
  // keeps current) and re-gated each poll, never rebuilt, so a half-typed reply
  // keeps its caret. The text itself is replyDraft's, shared with every other
  // reply box.
  const msg = { box: null, txt: null, mask: null, compSlot: null, enSlot: null, live: null, comp: null, enable: null };
  const inputOf = () => (msg.comp ? msg.comp.form.querySelector("input") : null);
  // The filter bar and the Load older row are built once and updated in place, so the control the
  // operator pressed is the same node afterwards and keeps the keyboard.
  const bar = { node: null, pills: [], dot: null, live: null, liveTxt: null };
  const foot = { node: actEl("div", "act-bar act-foot"), more: actEl("button", "btn btn-sm", "Load older"), err: actEl("span", "t-sm c-red"), note: actEl("span", "t-sm muted") };
  foot.more.type = "button";
  foot.more.onclick = () => actLoadOlder(sid); // actLoadOlder ignores a press while a page is out
  foot.node.append(foot.more, foot.err, foot.note);
  slots.foot.appendChild(foot.node);
  const put = (slot, node) => { slot.replaceChildren(...(node ? [node] : [])); };

  // resetMsg: drop the latest-message block and its reply box. The box is bound to
  // one session, so it must never outlive a re-target, a session that left the
  // list, or a switch to a session with no reply path.
  function resetMsg() {
    const f = inputOf();
    if (f && document.activeElement === f) replyFocused = false; // its blur will not fire once it is gone
    put(slots.msg, null);
    msg.box = null; msg.comp = null; msg.enable = null; msg.live = null;
  }

  function paintMsg(s) {
    if (!msg.box) {
      msg.box = actEl("section", "card act-msg");
      msg.box.appendChild(actEl("span", "grp-label", "Latest message"));
      msg.txt = actEl("p", "t-md act-msgtxt");
      msg.mask = actEl("p", "t-sm muted");
      msg.compSlot = actEl("div", "act-msgtail");
      msg.enSlot = actEl("div", "act-msgtail");
      msg.box.append(msg.txt, msg.mask, msg.compSlot, msg.enSlot);
      put(slots.msg, msg.box);
    }
    const text = s.summary || "Nothing said yet.";
    msg.txt.textContent = text;
    msg.txt.classList.toggle("muted", !s.summary);
    const hint = typeof maskHint === "function" ? maskHint(text) : "";
    msg.mask.textContent = hint;
    msg.mask.hidden = !hint;

    const on = globalThis.repliesEnabled === true && s.claude === true && !s.exited;
    const wantComp = on && !!s.replyable;
    const wantEnable = on && !s.replyable;
    if (!msg.live) msg.live = Object.assign({}, s); else actSyncLive(msg.live, s);
    if (wantComp && !msg.comp) {
      msg.comp = buildCardComposer(msg.live);
      msg.compSlot.replaceChildren(msg.comp.form, msg.comp.why);
    }
    if (msg.comp) {
      const f = inputOf();
      if (!wantComp && !(f && document.activeElement === f)) {
        msg.comp = null;
        msg.compSlot.replaceChildren();
      } else {
        msg.comp.refresh();
      }
    }
    if (wantEnable && !msg.enable) {
      msg.enable = buildEnableRepliesButton(s.id, () => { const f = inputOf(); if (f && !f.disabled) f.focus(); });
      msg.enSlot.replaceChildren(msg.enable);
    } else if (!wantEnable && msg.enable) {
      msg.enSlot.replaceChildren();
      msg.enable = null;
    }
  }

  // paintBar: the filter and the live label. A press or a poll only moves the pressed item and the label.
  function paintBar(st) {
    if (!bar.node) {
      bar.node = actEl("div", "act-bar");
      const seg = actEl("div", "act-filter seg");
      seg.setAttribute("role", "group");
      seg.setAttribute("aria-label", "Filter");
      bar.pills = ACTIVITY_FILTERS.map((f) => {
        const b = actEl("button", "seg-item", f[0].toUpperCase() + f.slice(1));
        b.type = "button";
        b.onclick = () => { actState(sid).filter = f; paint(false); };
        seg.appendChild(b);
        return [f, b];
      });
      bar.live = actEl("span", "act-live t-sm muted");
      bar.dot = actEl("span", "dot dot-run");
      bar.liveTxt = document.createTextNode("");
      bar.live.append(bar.dot, bar.liveTxt);
      bar.node.append(seg, actEl("span", "ph-spacer"), bar.live);
      put(slots.bar, bar.node);
    }
    // The pressed look is aria-pressed alone: the kit's .seg-item[aria-pressed="true"] draws it.
    for (const [f, b] of bar.pills) b.setAttribute("aria-pressed", f === st.filter ? "true" : "false");
    // Only the dot is green, and only while the read is current: a read that failed is not, whether or not an earlier
    // one landed (the table says "Could not read the activity." for one that never did).
    const stale = st.failed;
    bar.dot.className = stale ? "dot dot-idle" : "dot dot-run";
    bar.liveTxt.textContent = stale ? "Could not refresh · newest first" : "Live · newest first";
  }

  // paintFoot: Load older, its error and the partial-history note. The button is the same node while a page loads
  // (aria-busy and aria-disabled, never [disabled]), so it keeps the keyboard. When the last page lands the button
  // goes, and focus it held goes to the view, not to <body>.
  function paintFoot(m, st) {
    const held = document.activeElement === foot.more;
    const busy = st.loadingMore;
    foot.more.hidden = !m.more;
    foot.more.textContent = busy ? "Loading…" : "Load older";
    if (busy) { foot.more.setAttribute("aria-busy", "true"); foot.more.setAttribute("aria-disabled", "true"); }
    else { foot.more.removeAttribute("aria-busy"); foot.more.removeAttribute("aria-disabled"); }
    foot.err.textContent = st.olderErr;
    foot.err.hidden = !st.olderErr;
    foot.note.textContent = m.partial ? "Earlier history not available." : "";
    foot.note.hidden = !m.partial;
    foot.node.hidden = foot.more.hidden && foot.err.hidden && foot.note.hidden;
    if (held && foot.more.hidden) host.focus({ preventScroll: true });
  }

  // keepFocus: rebuild a slot's content, and give a button the keyboard had in it (the same one by position) back.
  function keepFocus(slot, rebuild) {
    const was = [...slot.querySelectorAll("button")].indexOf(document.activeElement);
    rebuild();
    if (was >= 0) {
      const again = [...slot.querySelectorAll("button")][was];
      if (again) again.focus({ preventScroll: true });
    }
  }

  function paint(force) {
    if (disposed) return;
    const st = actState(sid);
    const s = lastSessions.find((x) => x.id === sid);
    if (!s) {
      for (const k of Object.keys(keys)) keys[k] = "";
      resetMsg();
      layout("claude");
      bar.node = null; put(slots.tiles, null); put(slots.bar, null);
      put(slots.tools, null); put(slots.ctx, null); put(slots.files, null);
      foot.node.hidden = true;
      put(slots.list, actEl("div", "act-empty", "This session is no longer listed."));
      return;
    }
    const now = Date.now();
    // Every page held: the newest, then what "Load older" added.
    let resp = null;
    if (st.resp) {
      const more = st.pinned && st.olderMore !== null ? st.olderMore : st.resp.more;
      resp = Object.assign({}, st.resp, { events: st.resp.events.concat(st.older), more });
    }
    model = activityModel({ session: s, resp, filter: st.filter, nowMs: now, failed: st.failed });

    // Latest message and reply box: only a Claude session has either.
    if (model.notClaude) {
      if (msg.box || slots.msg.firstChild) resetMsg();
    } else {
      paintMsg(s);
    }
    layout(model.notClaude ? "plain" : "claude");

    const k1 = JSON.stringify(model.tiles);
    if (force || k1 !== keys.tiles) { keys.tiles = k1; put(slots.tiles, model.tiles.length ? actTiles(model.tiles) : null); }

    if (model.notClaude) {
      if (bar.node) { bar.node = null; put(slots.bar, null); }
    } else {
      paintBar(st);
    }

    // The plain block of a session with no transcript, and the table under it only when it has rows.
    if (model.notClaude) {
      const ke = JSON.stringify(model.empty);
      if (force || ke !== keys.empty) {
        keys.empty = ke;
        const e = model.empty;
        plain.block.replaceChildren(actEl("h2", "t-lg w-600", e.title), actEl("p", "t-md muted", e.body), actEl("p", "t-sm muted tnum", e.meta));
      }
      plain.root.classList.toggle("act-listed", model.rows.length > 0);
      plain.list.hidden = model.rows.length === 0;
    }

    const kl = JSON.stringify([model.rows, st.loaded, st.failed, st.filter, model.notClaude]);
    if (force || kl !== keys.list) {
      keys.list = kl;
      put(slots.list, model.rows.length ? actTable(model.rows) : model.notClaude ? null : actTable([], actEmptyText(st, st.filter)));
    }

    const kf = JSON.stringify([model.more, model.partial, st.loadingMore, st.olderErr]);
    if (force || kf !== keys.foot) { keys.foot = kf; paintFoot(model, st); }

    if (model.notClaude) {
      for (const k of ["tools", "ctx", "files"]) { keys[k] = ""; put(slots[k], null); }
      return;
    }
    const kt = JSON.stringify([st.attr.status, st.attr.failed, st.attr.data && actToolRows(st.attr.data)]);
    if (force || kt !== keys.tools) { keys.tools = kt; keepFocus(slots.tools, () => put(slots.tools, actToolsCard(st, sid))); }

    const tab = typeof terminalTabFor === "function" ? terminalTabFor(sid) : null;
    const ctx = actContext(tab && typeof tab.usage === "function" ? tab.usage() : null);
    const kc = JSON.stringify(ctx);
    if (force || kc !== keys.ctx) { keys.ctx = kc; put(slots.ctx, actCtxCard(ctx)); }

    const kfl = JSON.stringify([model.files, st.loaded, st.failed]);
    if (force || kfl !== keys.files) { keys.files = kfl; put(slots.files, actFilesCard(model.files, st)); }
  }

  // First open of a Claude session's view: scan once. Later it is the scan button.
  function scanOnce() {
    const s = lastSessions.find((x) => x.id === sid);
    const st = actState(sid);
    if (s && s.claude && !st.attrAsked) { st.attrAsked = true; actScan(sid); }
  }

  actViews.set(sid, paint);
  actFetch(sid);
  scanOnce();
  paint(true);

  return {
    update(next) {
      if (next && next !== sid) {
        if (actViews.get(sid) === paint) actViews.delete(sid);
        const old = actState(sid);
        if (old.ctl) old.ctl.abort();
        old.inflight = false;
        sid = next;
        actViews.set(sid, paint);
        for (const k of Object.keys(keys)) keys[k] = "";
        resetMsg();
        bar.node = null;
        put(slots.bar, null);
        old.seq++;
        scanOnce();
      }
      actPrune();
      // One read per poll, only while the view is really on screen.
      if (!document.hidden && host.isConnected) actFetch(sid);
      paint(false);
    },
    dispose() {
      disposed = true;
      if (actViews.get(sid) === paint) actViews.delete(sid);
      const st = actState(sid);
      st.seq++; // a read still out writes nothing
      if (st.ctl) st.ctl.abort();
      st.ctl = null;
      st.inflight = false;
      const f = inputOf();
      if (f && document.activeElement === f) replyFocused = false; // its blur will not fire once it is gone
    },
  };
}

paneViewRenderers.activity = mountActivity;
