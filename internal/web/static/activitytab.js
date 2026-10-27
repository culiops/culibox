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
// the filter, the expanded rows, the older pages and the reply draft are in
// actStates / replyDraft, per session, and survive a repaint and a remount. A
// section repaints only when its signature changes.

const ACT_PAGE = 100;

// Per session id, kept across remounts of the view:
//   filter, expanded (Set of row keys), resp (the newest page, refetched each
//   poll), older (pages loaded by "Load older"), pinned, olderMore, loadingMore,
//   olderErr, loaded / failed / inflight / at (actLoadDone), seq + ctl (the read
//   in flight), attr ({status, data, failed}), attrAsked.
const actStates = new Map();
const actViews = new Map(); // sid -> repaint(force) of the view mounted for it

function actState(sid) {
  let st = actStates.get(sid);
  if (!st) {
    st = {
      filter: "all", expanded: new Set(), resp: null, older: [], pinned: false, olderMore: null,
      loadingMore: false, olderErr: "", loaded: false, failed: false, inflight: false, at: 0,
      seq: 0, ctl: null, focusKey: "", attr: { status: "idle", data: null, failed: false }, attrAsked: false,
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

function actTiles(tiles) {
  const wrap = actEl("div", "act-tiles");
  for (const t of tiles) {
    const c = actEl("div", "act-tile");
    c.appendChild(actEl("div", "act-n", String(t.n)));
    c.appendChild(actEl("div", "act-tl", t.label));
    wrap.appendChild(c);
  }
  return wrap;
}

// actOutcome: the right-aligned cell. A diff's +a −d is neutral text (green is
// status and red is write/error only, D23); any other outcome is one word of one
// colour role.
function actOutcome(o) {
  const cell = actEl("div", "act-out");
  if (o.tone === "add") {
    const [a, d] = o.text.split(" ");
    cell.append(actEl("span", "act-add", a), document.createTextNode(" "), actEl("span", "act-del", d || ""));
  } else {
    cell.className = "act-out act-" + o.tone;
    cell.textContent = o.text;
  }
  return cell;
}

function actRows(m, st, repaint) {
  const list = actEl("div", "act-list");
  for (const r of m.rows) {
    const open = st.expanded.has(r.key);
    const row = actEl("div", "act-row" + (r.cls ? " " + r.cls : "") + (open ? " open" : ""));
    row.setAttribute("role", "button");
    row.tabIndex = 0;
    row.setAttribute("aria-expanded", open ? "true" : "false");
    row.title = r.detail;
    row.appendChild(actEl("div", "act-time", r.time));
    row.appendChild(actEl("div", "act-kind", r.kind));
    row.appendChild(actEl("div", "act-detail", r.detail));
    row.appendChild(actOutcome(r.outcome));
    row._key = r.key;
    const toggle = () => {
      if (st.expanded.has(r.key)) st.expanded.delete(r.key); else st.expanded.add(r.key);
      if (document.activeElement === row) st.focusKey = r.key; // the repaint gives the keyboard back
      repaint(false);
    };
    row.onclick = () => {
      // Selecting text to copy is not a request to expand the row.
      const sel = typeof window.getSelection === "function" ? window.getSelection() : null;
      if (sel && !sel.isCollapsed && row.contains(sel.anchorNode)) return;
      toggle();
    };
    row.onkeydown = (e) => {
      if (e.target !== row) return;
      if (e.key === "Enter" || e.key === " ") { e.preventDefault(); toggle(); }
    };
    list.appendChild(row);
  }
  return list;
}

// actEmpty: what the list says when it has no rows.
function actEmptyText(m, st, filter) {
  if (m.notClaude) return "No credential events yet.";
  if (!st.loaded) return st.failed ? "Could not read the activity." : "Loading…";
  return filter === "all" ? "No activity yet." : "Nothing matches this filter.";
}

function actFoot(m, st, sid) {
  const foot = actEl("div", "act-foot");
  if (m.more) {
    const b = actEl("button", "btn", st.loadingMore ? "Loading…" : "Load older");
    b.type = "button";
    b.disabled = st.loadingMore;
    b.classList.toggle("off", st.loadingMore);
    b.onclick = () => actLoadOlder(sid);
    foot.appendChild(b);
  }
  if (st.olderErr) foot.appendChild(actEl("div", "act-err", st.olderErr));
  if (m.partial) foot.appendChild(actEl("div", "act-note", "Earlier history not available."));
  return foot;
}

function actToolsCard(st, sid) {
  const card = actEl("section", "act-sec");
  const head = actEl("div", "act-sech");
  head.appendChild(actEl("span", "act-st", "Context by tool"));
  const scan = actEl("button", "btn", st.attr.status === "loading" ? "scanning…" : "scan");
  scan.type = "button";
  scan.disabled = st.attr.status === "loading";
  scan.classList.toggle("off", scan.disabled);
  scan.onclick = () => actScan(sid);
  head.appendChild(scan);
  card.appendChild(head);
  const t = actToolRows(st.attr.data);
  if (!st.attr.data) {
    card.appendChild(actEl("div", "act-note", st.attr.status === "loading" ? "Scanning the transcript…"
      : st.attr.failed ? "Could not scan the transcript." : "Not scanned yet."));
  } else if (!t.rows.length) {
    card.appendChild(actEl("div", "act-note", "No tool activity recorded for this session."));
  } else {
    for (const r of t.rows) {
      const row = actEl("div", "act-tool");
      row.title = r.name + " · " + r.calls + (r.calls === 1 ? " call" : " calls") + (r.ctx ? " · " + r.ctx + " tokens of results" : "");
      row.appendChild(actEl("span", "act-tname", r.name));
      const bar = actEl("span", "act-tbar");
      const fill = actEl("i");
      fill.style.width = r.pct + "%";
      bar.appendChild(fill);
      row.appendChild(bar);
      row.appendChild(actEl("span", "act-tpct", r.pct + "%"));
      card.appendChild(row);
    }
    if (t.more) card.appendChild(actEl("div", "act-note", "and " + t.more + " more"));
    if (st.attr.failed) card.appendChild(actEl("div", "act-note", "The last scan failed; this is the one before."));
    card.appendChild(actEl("div", "act-note", "Share of tool-result bytes: a proxy for context pressure, not billing."));
  }
  return card;
}

function actCtxCard(ctx) {
  if (!ctx) return null;
  const wrap = actEl("div", "act-ctx");
  const bar = actEl("div", "act-ctxbar");
  const i = actEl("i");
  i.style.width = ctx.width;
  bar.appendChild(i);
  bar.setAttribute("role", "img");
  bar.setAttribute("aria-label", ctx.text);
  wrap.append(bar, actEl("div", "act-note", ctx.text));
  return wrap;
}

function actFilesCard(files) {
  const card = actEl("section", "act-sec");
  card.appendChild(actEl("div", "act-sech", "Files changed"));
  if (!files.length) {
    card.appendChild(actEl("div", "act-note", "No files changed yet."));
    return card;
  }
  for (const f of files) {
    const row = actEl("div", "act-file");
    row.title = f.path;
    row.appendChild(actEl("span", "act-fpath", f.path));
    const d = actEl("span", "act-fdelta");
    const [a, del] = f.text.split(" ");
    d.append(actEl("span", "act-add", a), document.createTextNode(" "), actEl("span", "act-del", del));
    row.appendChild(d);
    card.appendChild(row);
  }
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

function mountActivity(host, sid0) {
  let sid = sid0;
  let disposed = false;
  let model = null;
  const wrap = actEl("div", "act-wrap");
  const main = actEl("div", "act-main");
  const side = actEl("div", "act-side");
  const slots = { msg: actEl("div"), tiles: actEl("div"), bar: actEl("div"), note: actEl("div"), list: actEl("div"), foot: actEl("div"),
    tools: actEl("div"), ctx: actEl("div"), files: actEl("div") };
  const keys = { msg: "", tiles: "", note: "", list: "", foot: "", tools: "", ctx: "", files: "" };
  main.append(slots.tiles, slots.msg, slots.bar, slots.note, slots.list, slots.foot);
  side.append(slots.tools, slots.ctx, slots.files);
  wrap.append(main, side);
  host.replaceChildren(wrap);

  // The reply box. Built once around `live` (a copy of the session entry the poll
  // keeps current) and re-gated each poll, never rebuilt, so a half-typed reply
  // keeps its caret. The text itself is replyDraft's, shared with every other
  // reply box.
  const msg = { box: null, txt: null, mask: null, compSlot: null, enSlot: null, live: null, comp: null, enable: null };
  const inputOf = () => (msg.comp ? msg.comp.form.querySelector("input") : null);
  const bar = { node: null, pills: [], live: null, liveTxt: null };
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
      msg.box = actEl("section", "act-msg");
      msg.box.appendChild(actEl("div", "act-lab", "Latest message"));
      msg.txt = actEl("div", "act-msgtxt");
      msg.mask = actEl("div", "act-note");
      msg.compSlot = actEl("div", "act-msgtail");
      msg.enSlot = actEl("div", "act-msgtail");
      msg.box.append(msg.txt, msg.mask, msg.compSlot, msg.enSlot);
      put(slots.msg, msg.box);
    }
    const text = s.summary || "Nothing said yet.";
    msg.txt.textContent = text;
    msg.txt.classList.toggle("act-quiet", !s.summary);
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

  function paint(force) {
    if (disposed) return;
    const st = actState(sid);
    const s = lastSessions.find((x) => x.id === sid);
    if (!s) {
      for (const k of Object.keys(keys)) keys[k] = "";
      resetMsg();
      bar.node = null; put(slots.tiles, null); put(slots.bar, null); put(slots.note, null);
      put(slots.foot, null); put(slots.tools, null); put(slots.ctx, null); put(slots.files, null);
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
    model = activityModel({ session: s, resp, filter: st.filter, nowMs: now });

    // Latest message and reply box: only a Claude session has either.
    if (model.notClaude) {
      if (msg.box || slots.msg.firstChild) resetMsg();
    } else {
      paintMsg(s);
    }

    const k1 = JSON.stringify(model.tiles);
    if (force || k1 !== keys.tiles) { keys.tiles = k1; put(slots.tiles, actTiles(model.tiles)); }

    // The filter bar is built once; a press or a poll only moves the pressed pill
    // and the live label, so the pill that was pressed keeps the keyboard.
    if (model.notClaude) {
      if (bar.node) { bar.node = null; put(slots.bar, null); }
    } else {
      if (!bar.node) {
        bar.node = actEl("div", "act-bar");
        const pills = actEl("div", "act-pills");
        pills.setAttribute("role", "group");
        pills.setAttribute("aria-label", "Filter");
        bar.pills = ACTIVITY_FILTERS.map((f) => {
          const b = actEl("button", "act-pill", f[0].toUpperCase() + f.slice(1));
          b.type = "button";
          b.onclick = () => { actState(sid).filter = f; paint(false); };
          pills.appendChild(b);
          return [f, b];
        });
        bar.live = actEl("div", "act-live");
        bar.live.appendChild(actEl("span", "act-dot"));
        bar.liveTxt = document.createTextNode("");
        bar.live.appendChild(bar.liveTxt);
        bar.node.append(pills, bar.live);
        put(slots.bar, bar.node);
      }
      for (const [f, b] of bar.pills) {
        b.classList.toggle("on", f === st.filter);
        b.setAttribute("aria-pressed", f === st.filter ? "true" : "false");
      }
      const stale = st.failed && st.loaded;
      bar.live.classList.toggle("stale", stale);
      bar.liveTxt.textContent = stale ? "could not refresh · newest first" : "live · newest first";
    }

    const kn = JSON.stringify([model.notClaude]);
    if (force || kn !== keys.note) {
      keys.note = kn;
      put(slots.note, model.notClaude ? actEl("div", "act-note act-lead", "Activity is available for Claude sessions.") : null);
    }

    const kl = JSON.stringify([model.rows, [...st.expanded].filter((k) => model.rows.some((r) => r.key === k)), st.loaded, st.failed, st.filter, model.notClaude]);
    if (force || kl !== keys.list) {
      keys.list = kl;
      put(slots.list, model.rows.length ? actRows(model, st, paint) : actEl("div", "act-empty", actEmptyText(model, st, st.filter)));
    }
    if (st.focusKey) {
      for (const el of slots.list.querySelectorAll(".act-row")) if (el._key === st.focusKey) el.focus({ preventScroll: true });
      st.focusKey = "";
    }

    const kf = JSON.stringify([model.more, model.partial, st.loadingMore, st.olderErr]);
    if (force || kf !== keys.foot) { keys.foot = kf; put(slots.foot, actFoot(model, st, sid)); }

    if (model.notClaude) {
      for (const k of ["tools", "ctx", "files"]) { keys[k] = ""; put(slots[k], null); }
      return;
    }
    const kt = JSON.stringify([st.attr.status, st.attr.failed, st.attr.data && actToolRows(st.attr.data)]);
    if (force || kt !== keys.tools) { keys.tools = kt; put(slots.tools, actToolsCard(st, sid)); }

    const tab = typeof terminalTabFor === "function" ? terminalTabFor(sid) : null;
    const ctx = actContext(tab && typeof tab.usage === "function" ? tab.usage() : null);
    const kc = JSON.stringify(ctx);
    if (force || kc !== keys.ctx) { keys.ctx = kc; put(slots.ctx, actCtxCard(ctx)); }

    const kfl = JSON.stringify(model.files);
    if (force || kfl !== keys.files) { keys.files = kfl; put(slots.files, actFilesCard(model.files)); }
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
