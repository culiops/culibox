"use strict";

// The dock's line blame (Files dock P6): for a text file in a git repository, the file's last commit in the breadcrumb
// row (after "Edited by", giving way before it), the current line's blame inline after the line's end when it fits or in
// the status bar when it does not (never both, never a horizontal scrollbar: anti-goal A5), and a card with the commit,
// Copy SHA and links to the commit and its pull request on the git host (through the link gate).
//
// One POST /api/git/blame per file version and HEAD, after the text is shown, at most one per tab every BL_MIN_GAP_MS
// (an agent writing the file), never for a dirty buffer, never in Preview or for a box tab. The answer describes the text
// the tab read (its version); lines edited since are mapped through lineMap and read "Not committed yet".
// Everything git said is set as textContent only (anti-goal A3); links come only from blamemodel.js's builders.

const BL_MIN_GAP_MS = 10000;
const BL_DEBOUNCE_MS = 120;
const BL_FETCH_MS = 60000; // the server may wait for another file's blame (30 s) before its own two runs
const BL_HOVER_MS = 400;
const BL_LEAVE_MS = 300;
const BL_GAP = "\u00a0\u00a0\u00a0";

function blNow() { return Date.now(); }

// editorBlameTrack: called once a text tab has its model (EditorTab.load). The blame follows the buffer from then on.
function editorBlameTrack(tab) {
  if (tab.bl || !tab.model) return;
  tab.bl = {
    res: null, idx: null, text: null, key: "", at: 0, ctl: null, seq: 0,
    map: null, mapAlt: -1, deco: [], timer: 0, gapTimer: 0, slowFor: "",
  };
  tab.blSub = tab.model.onDidChangeContent(() => editorBlameSoon(tab));
}

// editorBlameUntrack: the tab is closing (EditorTab.dispose).
function editorBlameUntrack(tab) {
  const bl = tab.bl;
  if (!bl) return;
  blAbort(bl);
  if (bl.timer) clearTimeout(bl.timer);
  if (bl.gapTimer) clearTimeout(bl.gapTimer);
  blClearLine(tab);
  if (tab.blSub) tab.blSub.dispose();
  if (blCardFor === tab) editorBlameCardClose(false);
  tab.bl = null;
}

// editorBlameHide: another tab is shown (EditorTab.onHide). A request still running is let go (the server stops its
// wait or its run): the way back asks again, without waiting out the gap.
function editorBlameHide(tab) {
  const bl = tab.bl;
  if (!bl) return;
  if (bl.ctl) { blAbort(bl); bl.at = 0; bl.key = ""; }
  blClearLine(tab);
  if (blCardFor === tab) editorBlameCardClose(false);
}

function blAbort(bl) {
  if (bl.ctl) { bl.ctl.abort(); bl.ctl = null; }
  bl.seq++;
}

// editorBlameSoon: something moved (the cursor, the text, the layout, the view): look again after a short pause.
function editorBlameSoon(tab) {
  const bl = tab && tab.bl;
  if (!bl || tab.disposed) return;
  if (!bl.res && !bl.deco.length && !blFetchable(tab)) return; // nothing shown and nothing to ask: no timer
  if (bl.timer) clearTimeout(bl.timer);
  bl.timer = setTimeout(() => { bl.timer = 0; editorBlameSync(tab); }, BL_DEBOUNCE_MS);
}

// editorBlameSyncOwner: the tree's git status landed (HEAD may have moved): the tab on screen looks again.
function editorBlameSyncOwner() {
  if (typeof dockEditorOwner !== "undefined" && dockEditorOwner && dockEditorOwner.bl) editorBlameSoon(dockEditorOwner);
}

function blStatus() {
  return typeof gitStatusCurrent === "function" && typeof ftRoot === "function" ? gitStatusCurrent(ftRoot()) : null;
}

function blHead() {
  const cur = blStatus();
  return cur && cur.status && typeof cur.status.head === "string" ? cur.status.head : "";
}

// blCodeView: the tab is on screen in Code with the shared editor (not Preview, not Changes, not a viewer).
function blCodeView(tab) {
  return !!tab.bl && !tab.disposed && !!tab.model && !tab.inner && !tab.diff && !tab.previewing &&
    typeof dockEditorOwner !== "undefined" && dockEditorOwner === tab && !!dockEditor;
}

// blFetchable: whether this tab may ask now (the dock shows it in Code, it is no box file, its text is as read, and the
// tree's git status has landed once: as for the change gutter, its HEAD is part of the question).
function blFetchable(tab) {
  if (!blCodeView(tab) || tab.box || !tab.version || tab.dirty() || !blStatus()) return false;
  return typeof editorSyncShown !== "function" || editorSyncShown();
}

// editorBlameSync: ask when the question changed (path, version, HEAD) and the gap allows, then paint.
function editorBlameSync(tab) {
  const bl = tab && tab.bl;
  if (!bl || tab.disposed) return;
  if (blFetchable(tab)) {
    const head = blHead();
    const key = tab.path + "\n" + tab.version + "\n" + head;
    const slow = bl.slowFor === tab.path + "\n" + head; // a blame that took too long is not asked again on its own
    if (key !== bl.key && !slow) {
      const wait = bl.at ? bl.at + BL_MIN_GAP_MS - blNow() : 0;
      if (wait > 0) {
        if (!bl.gapTimer) bl.gapTimer = setTimeout(() => { bl.gapTimer = 0; editorBlameSync(tab); }, wait);
      } else {
        blFetch(tab, key, head);
      }
    }
  }
  blPaint(tab);
}

async function blFetch(tab, key, head) {
  const bl = tab.bl;
  blAbort(bl);
  const seq = bl.seq;
  const snap = { version: tab.version, text: tab.diskText };
  const ctl = new AbortController();
  bl.ctl = ctl;
  bl.key = key;
  bl.at = blNow();
  const timer = setTimeout(() => ctl.abort(), BL_FETCH_MS);
  let res = null;
  try {
    const r = await fetch("/api/git/blame", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: tab.path }), signal: ctl.signal,
    });
    res = r.ok ? await r.json() : null;
  } catch (e) {
    res = null;
  }
  clearTimeout(timer);
  if (!tab.bl || bl.seq !== seq || tab.disposed) return; // a newer question, another tab, or closed
  bl.ctl = null;
  if (!res || typeof res.state !== "string") {
    res = { state: "unavailable", reason: "Git status is not available for this folder" };
  }
  if (res.state === "changed" || (res.version && res.version !== snap.version)) {
    // The file moved while git read it, or since this tab read it: keep what is shown and ask again after the gap.
    bl.key = "";
    editorBlameSoon(tab);
    return;
  }
  if (res.state === "slow") bl.slowFor = tab.path + "\n" + head;
  bl.res = res;
  bl.idx = res.state === "ok" ? blameIndex(res) : null;
  bl.text = snap.text;
  bl.mapAlt = -1;
  blPaint(tab);
}

// blLine: the blame of buffer line `line` for the tab, through the map of the edits made since the blamed text.
function blLine(tab, line) {
  const bl = tab.bl;
  if (!bl || !bl.res) return null;
  if (bl.res.state === "untracked") return { commit: null, uncommitted: true };
  if (!bl.idx || typeof bl.text !== "string") return null;
  const alt = tab.model.getAlternativeVersionId();
  if (bl.mapAlt !== alt) {
    const cur = tab.model.getValue();
    if (cur === bl.text) bl.map = null;
    else {
      const m = lineMap(bl.text, cur);
      bl.map = m.capped ? "capped" : m.map;
    }
    bl.mapAlt = alt;
  }
  if (bl.map === "capped") return null;
  return blameAt(bl.res, bl.idx, bl.map, line);
}

// blCols: how many character cells text takes, a wide (CJK, full-width) character counting two.
function blCols(text) {
  let n = 0;
  for (const ch of text) n += /[\u1100-\u115F\u2E80-\uA4CF\uAC00-\uD7A3\uF900-\uFAFF\uFE30-\uFE4F\uFF00-\uFF60\uFFE0-\uFFE6]/.test(ch) ? 2 : 1;
  return n;
}

// blPaint: the current line's blame, inline or in the status bar, and the header when this tab is the one shown.
function blPaint(tab) {
  if (!tab.bl || tab.disposed) return;
  blPaintLine(tab);
  editorBlameHeaderPaint(tab);
}

function blClearLine(tab) {
  const bl = tab.bl;
  if (bl && bl.deco.length && typeof dockEditor !== "undefined" && dockEditor) bl.deco = dockEditor.deltaDecorations(bl.deco, []);
  else if (bl) bl.deco = [];
  if (tab.sbBlame && !tab.sbBlame.hidden) { tab.sbBlame.hidden = true; tab.sbBlame.textContent = ""; }
}

function blPaintLine(tab) {
  const bl = tab.bl;
  const on = viewerPrefs.getBlame();
  const pos = blCodeView(tab) ? dockEditor.getPosition() : null;
  const b = on && pos ? blLine(tab, pos.lineNumber) : null;
  if (!b) { blClearLine(tab); return; }
  const text = blameLineText(b.commit, blNow());
  const L = pos.lineNumber;
  const maxCol = tab.model.getLineMaxColumn(L);
  const short = blameLineFit(b.commit, blNow(), blInlineCols(L, maxCol), blCols);
  if (short) {
    const want = [{
      range: { startLineNumber: L, startColumn: maxCol, endLineNumber: L, endColumn: maxCol },
      // The gap is part of the text (three no-break spaces, the 3 cells the fit counts): Monaco cuts injected text into several
      // spans, so a margin on the class would repeat inside it.
      options: { after: { content: BL_GAP + short, inlineClassName: "dk-blame-inline" }, showIfCollapsed: true },
    }];
    bl.deco = dockEditor.deltaDecorations(bl.deco, want);
    if (tab.sbBlame && !tab.sbBlame.hidden) { tab.sbBlame.hidden = true; tab.sbBlame.textContent = ""; }
    return;
  }
  if (bl.deco.length) bl.deco = dockEditor.deltaDecorations(bl.deco, []);
  if (tab.sbBlame) {
    if (tab.sbBlame.textContent !== text) tab.sbBlame.textContent = text;
    tab.sbBlame.title = text;
    tab.sbBlame.hidden = false;
  }
}

// blInlineCols: how many character cells of inline text fit after the line's end: the line's end, a gap and the text, plus
// the room Monaco keeps past the longest line (scrollBeyondLastColumn), inside the content width at the editor's character
// width (blameFits). 0 when the line is not drawn.
function blInlineCols(line, maxCol) {
  if (typeof dockEditor.getScrolledVisiblePosition !== "function") return 0;
  const p = dockEditor.getScrolledVisiblePosition({ lineNumber: line, column: maxCol });
  if (!p) return 0;
  const lay = dockEditor.getLayoutInfo();
  const opt = dockMonaco && dockMonaco.editor && dockMonaco.editor.EditorOption;
  const fi = opt ? dockEditor.getOption(opt.fontInfo) : null;
  const cw = fi && fi.typicalHalfwidthCharacterWidth > 0 ? fi.typicalHalfwidthCharacterWidth : 7.5;
  // The room Monaco keeps past the longest line matters only when this line could become the longest: when another line
  // already scrolls the editor sideways, staying inside the visible width is enough.
  const scrolls = typeof dockEditor.getScrollWidth === "function" && dockEditor.getScrollWidth() > lay.contentWidth + 1;
  const beyond = scrolls ? 0 : opt ? Number(dockEditor.getOption(opt.scrollBeyondLastColumn)) || 0 : 5;
  const geo = { endLeft: p.left - lay.contentLeft, scrollLeft: dockEditor.getScrollLeft(), contentWidth: lay.contentWidth, gap: 3 * cw };
  let cols = Math.floor((geo.contentWidth - geo.endLeft - geo.scrollLeft - geo.gap) / cw) - beyond - 1; // a cell of slack for rounding
  while (cols > 0 && !blameFits(Object.assign({ textWidth: (cols + beyond) * cw }, geo))) cols--;
  return Math.max(0, cols);
}

// editorBlameToggle: the ⋯ menu's "Show line blame". The header stays: it is the file's, not the line's.
function editorBlameToggle() {
  viewerPrefs.setBlame(!viewerPrefs.getBlame());
  if (typeof dockEditorOwner !== "undefined" && dockEditorOwner && dockEditorOwner.bl) blPaint(dockEditorOwner);
}

// editorBlameMenuRows: the ⋯ menu's rows for a text tab in Code (the menu's own item/check builders).
function editorBlameMenuRows(tab, item, check) {
  if (!tab.bl || tab.diff || tab.previewing || tab.box) return [];
  const pos = blCodeView(tab) ? dockEditor.getPosition() : null;
  const known = !!(pos && blLine(tab, pos.lineNumber));
  return [
    check("Show line blame", viewerPrefs.getBlame(), () => editorBlameToggle()),
    item("Blame of this line", "", () => editorBlameCardFor(tab, "line", tab.moreBtn, true), known, "No blame for this line yet"),
  ];
}

// --- the header -------------------------------------------------------------------------------------------------

// editorBlameHeaderPaint: the breadcrumb row's blame item for the dock's active tab (null: none).
function editorBlameHeaderPaint(tab) {
  const el = typeof document !== "undefined" ? document.getElementById("dock-blame") : null;
  if (!el) return;
  editorBlameWireHeader(el);
  editorBlameHeaderWatch(el.parentNode);
  if (tab && tab.attached === false) return; // a tab off screen does not own the row
  const res = tab && tab.bl && !tab.box ? tab.bl.res : null;
  const h = blHeaderText(res, blNow());
  el.hidden = !h;
  blHeaderTab = h ? tab : null;
  if (!h) return;
  const part = (cls) => el.querySelector("." + cls);
  const set = (n, t) => { if (n && n.textContent !== t) n.textContent = t; };
  set(part("dk-blame-who"), h.who);
  set(part("dk-blame-when"), h.when);
  set(part("dk-blame-short"), h.short);
  el.title = h.title;
  el.setAttribute("aria-label", h.title);
  el.classList.toggle("dk-blame-muted", !!h.muted);
  blHeaderLayout(el);
}

let blHeaderTab = null;

// blHeaderText: what the header says for an answer: the last commit, "Not committed yet", "Blame unavailable", or null.
function blHeaderText(res, now) {
  if (!res) return null;
  if (res.state === "ok" && res.last && typeof res.last.author === "string") {
    const ago = blameAgo(res.last.time, now);
    return { who: res.last.author, when: ago.long, short: ago.short, title: "Last commit: " + res.last.author + ", " + ago.long + " · " + (res.last.summary || "") };
  }
  if (res.state === "untracked") return { who: "Not committed yet", when: "", short: "new", title: "Not committed yet" };
  if (res.state === "unavailable" || res.state === "slow") {
    const why = typeof res.reason === "string" && res.reason ? res.reason : "Blame unavailable";
    return { who: "Blame unavailable", when: "", short: "", title: why, muted: true };
  }
  return null;
}

// blHeaderLayout: wide, short or icon only, by the room the row has once "Edited by" and the outside hint's Reveal have
// theirs and the trail keeps up to 160 px.
function blHeaderLayout(el) {
  const row = el.parentNode;
  if (!row || typeof row.querySelector !== "function") return;
  el.dataset.form = "measure"; // every part drawn, to be measured (no paint happens before the form below is set)
  // A part's own width: what it draws, or what it would draw uncut (the author's name may be ellipsized right now).
  const w = (n) => (n && !n.hidden ? Math.max(typeof n.getBoundingClientRect === "function" ? n.getBoundingClientRect().width : 0, n.scrollWidth || 0, n.offsetWidth || 0) : 0);
  const part = (cls) => el.querySelector("." + cls);
  const icon = w(part("dk-blame-i"));
  const gap = 4;
  const wide = icon + gap + w(part("dk-blame-who")) + (part("dk-blame-when") && part("dk-blame-when").textContent ? gap * 2 + 4 + w(part("dk-blame-when")) : 0);
  const short = icon + (part("dk-blame-short") && part("dk-blame-short").textContent ? gap + w(part("dk-blame-short")) : 0);
  const cs = typeof getComputedStyle === "function" ? getComputedStyle(row) : null;
  const rowGap = cs ? parseFloat(cs.columnGap) || 0 : 0;
  const pad = cs ? (parseFloat(cs.paddingLeft) || 0) + (parseFloat(cs.paddingRight) || 0) : 0;
  const edited = row.querySelector(".dk-edited");
  const outside = row.querySelector(".dk-outside");
  const go = outside && !outside.hidden ? outside.querySelector(".dk-outside-go") : null;
  const trail = row.querySelector(".dk-trail");
  const shown = [edited, outside].filter((n) => n && !n.hidden).length + 1; // the gaps between the trail and what follows it
  const other = pad + rowGap * shown + w(edited) + (go ? go.offsetWidth + 12 : 0);
  const trailNeed = Math.min(trail ? trail.scrollWidth : 0, 160);
  el.dataset.form = blameHeaderForm({ rowWidth: row.clientWidth, otherWidth: other, wideWidth: wide, shortWidth: short, trailNeed });
}

// editorBlameHeaderWatch: the row's width changes (pane split, dock resize): the form follows (dockstrip.js wires it once).
function editorBlameHeaderWatch(row) {
  if (!row || typeof ResizeObserver !== "function" || row.blWatch) return;
  row.blWatch = new ResizeObserver(() => {
    const el = document.getElementById("dock-blame");
    if (el && !el.hidden) blHeaderLayout(el);
  });
  row.blWatch.observe(row);
}

// --- the card ---------------------------------------------------------------------------------------------------

let blCard = null;       // the card's element, made once
let blCardFor = null;    // the tab it is about
let blCardOpener = null; // what opened it, focus goes back there
let blHoverTimer = 0;
let blLeaveTimer = 0;

function blEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function blCardEl() {
  if (blCard) return blCard;
  blCard = blEl("div", "dk-bcard");
  blCard.id = "dk-bcard";
  blCard.setAttribute("role", "dialog");
  blCard.hidden = true;
  blCard.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); editorBlameCardClose(true); }
  });
  blCard.addEventListener("pointerenter", () => { if (blLeaveTimer) { clearTimeout(blLeaveTimer); blLeaveTimer = 0; } });
  blCard.addEventListener("pointerleave", () => blLeaveSoon());
  const host = document.getElementById("drawer") || document.body;
  if (host) host.append(blCard);
  if (typeof document.addEventListener === "function") {
    document.addEventListener("pointerdown", (e) => {
      if (!blCard.hidden && !blCard.contains(e.target) && !(blCardOpener && blCardOpener.contains && blCardOpener.contains(e.target))) editorBlameCardClose(false);
    }, true);
  }
  return blCard;
}

// editorBlameCardFor: open the card for the tab's current line ("line") or its last commit ("file"), next to anchor.
// focus: opened by a click or a key, so the card takes the keyboard (a hover leaves it where it is).
function editorBlameCardFor(tab, which, anchor, focus) {
  if (!tab || !tab.bl || !tab.bl.res) return false;
  let b = null;
  if (which === "file") {
    const res = tab.bl.res;
    if (res.state === "untracked") b = { commit: null, uncommitted: true };
    else if (res.state === "ok" && res.last) b = { commit: res.last, uncommitted: false };
  } else {
    const pos = blCodeView(tab) ? dockEditor.getPosition() : null;
    b = pos ? blLine(tab, pos.lineNumber) : null;
  }
  if (!b) return false;
  const card = blCardEl();
  card.replaceChildren(...(b.commit ? blCommitNodes(tab, b.commit) : blUncommittedNodes(tab, which === "line" && blCodeView(tab) ? dockEditor.getPosition().lineNumber : 1)));
  card.setAttribute("aria-label", b.commit ? "Commit " + b.commit.sha.slice(0, 7) : "Not committed yet");
  card.hidden = false;
  blCardFor = tab;
  blCardOpener = anchor || null;
  blPlace(card, anchor);
  if (focus) {
    const first = card.querySelector("button");
    if (first) first.focus();
  }
  return true;
}

function editorBlameCardClose(restoreFocus) {
  if (blHoverTimer) { clearTimeout(blHoverTimer); blHoverTimer = 0; }
  if (blLeaveTimer) { clearTimeout(blLeaveTimer); blLeaveTimer = 0; }
  if (!blCard || blCard.hidden) return;
  blCard.hidden = true;
  blCard.replaceChildren();
  const back = blCardOpener;
  blCardFor = null;
  blCardOpener = null;
  if (restoreFocus && back && typeof back.focus === "function") back.focus();
}

function blCommitNodes(tab, c) {
  const now = blNow();
  const remote = tab.bl.res.remote || null;
  const head = blEl("div", "dk-bcard-h");
  head.append(blEl("span", "dk-bcard-sha", c.sha.slice(0, 7)), blEl("span", "dk-bcard-who", c.author));
  if (c.email) head.append(blEl("span", "dk-bcard-mail", "<" + c.email + ">"));
  const when = new Date(c.time * 1000);
  const whenText = (Number.isFinite(when.getTime()) ? when.toLocaleString() + " · " : "") + blameAgo(c.time, now).long + (c.boundary ? " · boundary" : "");
  const out = [head, blEl("div", "dk-bcard-when", whenText)];
  if (c.summary) out.push(blEl("div", "dk-bcard-sum", c.summary));
  const acts = blEl("div", "dk-bcard-acts");
  const btn = (label, run) => { const x = blEl("button", "btn btn-sm btn-ghost", label); x.type = "button"; x.addEventListener("click", run); return x; };
  acts.append(btn("Copy SHA", () => {
    if (typeof copyText === "function") copyText(c.sha, (ok) => { if (typeof showToast === "function") showToast(ok ? "Commit SHA copied" : "Could not copy"); });
  }));
  const commitUrl = blameCommitUrl(remote, c.sha);
  if (commitUrl) acts.append(btn("Open commit ↗", () => blOpen(commitUrl)));
  const prUrl = blamePrUrl(remote, c.ref);
  if (prUrl) acts.append(btn((c.ref[0] === "!" ? "MR " : "PR ") + c.ref + " ↗", () => blOpen(prUrl)));
  out.push(acts);
  return out;
}

function blUncommittedNodes(tab, line) {
  const head = blEl("div", "dk-bcard-h");
  head.append(blEl("span", "dk-bcard-who", "Not committed yet"));
  const sid = tab.host && typeof tab.host.sessionId === "function" ? tab.host.sessionId() : null;
  const files = sid && typeof drawerChangedFiles !== "undefined" ? drawerChangedFiles.get(sid) : null;
  if (typeof editedLabel === "function" && editedLabel(files, tab.path, "agent", blNow())) {
    head.append(blEl("span", "dk-bcard-chip", "changed by this session's agent"));
  }
  const out = [head, blEl("div", "dk-bcard-when", "The working copy differs from the last commit on this line.")];
  const acts = blEl("div", "dk-bcard-acts");
  const btn = (label, run) => { const x = blEl("button", "btn btn-sm btn-ghost", label); x.type = "button"; x.addEventListener("click", run); return x; };
  acts.append(btn("Open changes", () => { editorBlameCardClose(false); tab.showChanges(true); }));
  if (typeof editorNotesFromCode === "function") acts.append(btn("Add note", () => { editorBlameCardClose(false); editorNotesFromCode(tab, { a: line, b: line }); }));
  out.push(acts);
  return out;
}

function blOpen(url) {
  editorBlameCardClose(false);
  if (typeof openAgentLink === "function") openAgentLink(url, { source: "editor" });
}

// blPlace: under the anchor (or above it when there is no room), inside the window.
function blPlace(card, anchor) {
  if (!anchor || typeof anchor.getBoundingClientRect !== "function" || typeof window === "undefined") return;
  const r = anchor.getBoundingClientRect();
  const cw = card.offsetWidth || 360;
  const ch = card.offsetHeight || 140;
  const left = Math.max(8, Math.min(r.left, window.innerWidth - cw - 8));
  const below = r.bottom + 4;
  const top = below + ch > window.innerHeight - 8 ? Math.max(8, r.top - ch - 4) : below;
  card.style.left = left + "px";
  card.style.top = top + "px";
}

// Hover: a pointer resting on the header, the status item or the inline text opens the card after BL_HOVER_MS; leaving
// the anchor and the card closes it after BL_LEAVE_MS.
function blHoverOpen(tab, which, anchor) {
  if (blLeaveTimer) { clearTimeout(blLeaveTimer); blLeaveTimer = 0; }
  if (blCard && !blCard.hidden && blCardFor === tab) return;
  if (blHoverTimer) clearTimeout(blHoverTimer);
  blHoverTimer = setTimeout(() => { blHoverTimer = 0; editorBlameCardFor(tab, which, anchor, false); }, BL_HOVER_MS);
}

function blLeaveSoon() {
  if (blHoverTimer) { clearTimeout(blHoverTimer); blHoverTimer = 0; }
  if (!blCard || blCard.hidden || blLeaveTimer) return;
  blLeaveTimer = setTimeout(() => { blLeaveTimer = 0; editorBlameCardClose(false); }, BL_LEAVE_MS);
}

// editorBlameWireStatus: the status bar's blame item (EditorTab.buildStatus makes it, a button).
function editorBlameWireStatus(tab, b) {
  b.addEventListener("click", () => editorBlameCardFor(tab, "line", b, true));
  b.addEventListener("pointerenter", () => blHoverOpen(tab, "line", b));
  b.addEventListener("pointerleave", () => blLeaveSoon());
}

// editorBlameWireHeader: the breadcrumb row's item (static markup, index.html), wired once.
function editorBlameWireHeader(el) {
  if (!el || el.blWired) return;
  el.blWired = true;
  el.addEventListener("click", () => { if (blHeaderTab) editorBlameCardFor(blHeaderTab, "file", el, true); });
  el.addEventListener("pointerenter", () => { if (blHeaderTab) blHoverOpen(blHeaderTab, "file", el); });
  el.addEventListener("pointerleave", () => blLeaveSoon());
}

// editorBlameWireEditor: the shared editor's pointer over the inline text, and its scroll and layout (editorEnsure, once).
function editorBlameWireEditor(ed) {
  if (!ed || ed.blWired) return;
  ed.blWired = true;
  ed.onMouseMove((e) => {
    const t = e && e.target && e.target.element;
    const owner = typeof dockEditorOwner !== "undefined" ? dockEditorOwner : null;
    if (owner && owner.bl && t && t.classList && t.classList.contains("dk-blame-inline")) blHoverOpen(owner, "line", t);
    else if (blHoverTimer || (blCard && !blCard.hidden && blCardOpener && blCardOpener.classList && blCardOpener.classList.contains("dk-blame-inline"))) blLeaveSoon();
  });
  const again = () => { const owner = typeof dockEditorOwner !== "undefined" ? dockEditorOwner : null; if (owner) editorBlameSoon(owner); };
  if (typeof ed.onDidLayoutChange === "function") ed.onDidLayoutChange(again);
  if (typeof ed.onDidScrollChange === "function") ed.onDidScrollChange(again);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { blHeaderText, blCols };
}
