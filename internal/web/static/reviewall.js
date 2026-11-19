"use strict";

// The "All changes" tab of the Files dock (spec "Review loop (P3)"): every changed file of the session's Changes list in one scroll,
// a section per file. DOM and requests only; which rows get a section, which sections have an editor, how F7 steps from section to
// section and how tall a section is are reviewmodel.js's (reviewAllRows, reviewAllPlan, reviewAllStepPlan, reviewAllHeight), which node
// tests run.
//
// The tab is a dock tab of kind `review`, key "r:" + the session id (docktabs.js dockOpenReview), opened by the Changes section's Review
// link and by View all in the single-file diff header. It is a viewer in the dock's sense (filesdrawer.js mounts it like an EditorTab:
// el, onShow / onAttach / onHide, dispose, dirty, ...) but holds no file: it is a view of the Changes list, so it is not stored and not
// restored after a reload (Review opens it again).
//
// A section (.dk-sec) is a sticky header (caret, icon, name, folder, git letter, `+a −d`, the reviewed circle, the notes chip, Open) over
// a body. The body holds a read-only Monaco diff editor with the unchanged regions collapsed (hideUnchangedRegions), as tall as its
// content up to REVIEW_ALL_HEIGHT and scrolling inside past that; an image gets imagediff.js's comparer; a binary file says so.
//
// Lazy, and nothing survives a hidden tab (ruling R2: P2's one-diff-editor rule binds EditorTabs, this tab owns its section editors).
//  - Two IntersectionObservers on the scroll area, with a screen of margin (near) and two (far). A section that comes within a screen
//    gets its editor and its two requests (POST /api/git/show for the base, GET /api/fs/read for the working tree); an editor whose
//    section is more than two screens away is disposed with its models (a moment after its editor, see RA_GRACE_MS), its notes host, its
//    image mount and its requests. Between the
//    two it keeps what it has. A section with no editor holds a placeholder as tall as it was last, so the scroll does not jump.
//    Nothing else runs on a scroll.
//  - The tab hidden (another tab, the dock closed or folded away, a page over it, the page hidden): every section editor is disposed,
//    the observers go, nothing is asked, and an answer that arrives late is dropped (each section's sequence number, and its requests are
//    aborted). Coming back asks again, section by section.
//  - A section's request key is what the Changes view's is (editorDiffKey: path, scope, HEAD, merge base) plus the row's code and counts;
//    a status that moves it makes that section ask again.
//
// The Changes list is the tab's file tree: while the tab is on screen a row's click scrolls to its section (reviewAllScrollTo). F7 and
// Shift+F7 (reviewAllStep, bound with the Changes view's keys in editortab.js) step through the changes of the section the keyboard is in
// and on into the next one. Notes work as in the single-file view: each section's editor attaches its own notes host.

const RA_FETCH_MS = 20000;     // a section's two requests together; one that never answers must not hold a section for good
const RA_EST_BASE = 72;        // px: a section with no editor and no measure yet is this tall, plus a line's worth per changed line
const RA_EST_LINE = 18;
const RA_EST_MAX = 480;
const RA_LAND_MS = 10000;      // a landing that was not taken (the section scrolled away before its diff came) is forgotten after this
const RA_ICON_CARET = [["path", { d: "M4 5.5l3 3 3-3" }]];

let raTab = null;       // the tab on screen, the one F7 and a row's click are for

function raEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// raCall: one request, answered {ok, status, data}; status 0 for anything that did not complete (the network, an abort). Never throws.
async function raCall(url, init, ac) {
  try {
    const res = await fetch(url, Object.assign({ signal: ac.signal }, init));
    let data = null;
    try { data = await res.json(); } catch (_) { /* not JSON: no data */ }
    return { ok: res.ok, status: res.status, data };
  } catch (_) {
    return { ok: false, status: 0, data: null };
  }
}

// raEstimate: how tall a section with no editor is until one has been measured.
function raEstimate(row) {
  const n = (Number.isFinite(row.add) ? row.add : 0) + (Number.isFinite(row.del) ? row.del : 0);
  return Math.min(RA_EST_MAX, RA_EST_BASE + n * RA_EST_LINE);
}

class ReviewAllTab {
  // opts = {host}: the dock's toolbar host (it names the session the tab belongs to).
  constructor(sid, opts) {
    const o = opts || {};
    this.sid = sid;
    this.id = "review:" + sid;
    this.kind = "review";
    // What the dock asks of any viewer (filesdrawer.js, viewer-tab.js allViewers, editordiff.js): a tab that holds no file and no text.
    this.path = "";
    this.box = "";
    this.name = "All changes";
    this.host = o.host || null;
    this.onUpdate = null;
    this.disposed = false;
    this.attached = false;
    this.readOnly = false;
    this.inner = null;
    this.model = null;
    this.diff = null;
    this.changes = null;
    this.dg = null;
    this.img = null;
    this.secs = new Map();   // path -> section
    this.byEl = new Map();   // section element -> section (the observers' targets)
    this.order = [];         // the paths with a section, in order
    this.io = null;          // {near, far}: the observers, while the tab is on screen
    this.nav = null;         // {path, line}: where F7 last landed
    this.want = null;        // a section to scroll to once the tab is on screen
    this.top = 0;            // where the scroll area was left, for the way back
    this.sig = "";

    this.el = raEl("div", "tab-body dk-ed dk-ra");
    this.headEl = raEl("div", "dk-ra-head");
    this.titleEl = raEl("span", "dk-ra-title", "All changes");
    this.cmpEl = raEl("span", "dk-ra-cmp");
    this.countEl = raEl("span", "dk-ra-count");
    this.ctEl = raEl("span", "dk-ct");
    this.ctAdd = raEl("span", "dk-add");
    this.ctDel = raEl("span", "dk-del");
    this.ctEl.append(this.ctAdd, this.ctDel);
    this.revEl = raEl("span", "dk-ra-rev");
    this.revEl.setAttribute("aria-live", "polite");
    this.headEl.append(this.titleEl, this.countEl, this.ctEl, this.cmpEl, this.revEl);
    this.scrollEl = raEl("div", "dk-ra-scroll");
    this.scrollEl.tabIndex = 0;
    this.scrollEl.setAttribute("role", "region");
    this.scrollEl.setAttribute("aria-label", "All changes");
    this.noteEl = raEl("div", "dk-chg-note dk-ra-note");
    this.noteEl.hidden = true;
    this.listEl = raEl("div", "dk-ra-list");
    this.moreEl = raEl("div", "dk-more dk-ra-more");
    this.moreEl.hidden = true;
    this.scrollEl.append(this.noteEl, this.listEl, this.moreEl);
    this.el.append(this.headEl, this.scrollEl);
    if (typeof reviewBarMount === "function") reviewBarMount(this); // the review bar (reviewbar.js): first in the tab, while notes wait
    // F7 / Shift+F7 pressed on a header's own buttons (the circle, Open, the caret): the same as inside an editor.
    this.el.addEventListener("keydown", (e) => {
      if (e.defaultPrevented) return;
      const dir = shortcutMatches("editor.nextChange", e, IS_MAC) ? 1 : shortcutMatches("editor.prevChange", e, IS_MAC) ? -1 : 0;
      if (dir) { e.preventDefault(); e.stopPropagation(); this.step(dir); }
    });
    this.buildActs();
    this.toolbarApi = { paint: () => this.paintMode(), closeMenus: () => {} };
  }

  // --- what the dock calls on a viewer -----------------------------------------------------------------------

  dirty() { return false; }
  generation() { return null; }
  discardEdits() {}
  crumbNote() { return ""; }
  revealLine() {}
  showChanges() { return false; }
  applyWrap() {}
  wrapAnchor() { return () => {}; }
  get scroll() { return 0; } // the tab is not stored, so there is nothing to come back to after a reload
  get fileScroll() { return 0; }
  changed() { if (this.onUpdate) this.onUpdate(); }
  focusReader() { this.scrollEl.focus({ preventScroll: true }); }
  onShow() {}

  onAttach() {
    if (this.disposed) return;
    this.attached = true;
    raTab = this;
    const acts = document.getElementById("dock-acts");
    if (acts) acts.insertBefore(this.actsEl, document.getElementById("drawer-max"));
    this.sync();
    this.scrollEl.scrollTop = this.top;
    if (this.want) { const p = this.want; this.want = null; this.scrollTo(p); }
  }

  // onHide: another tab is about to be shown. No section editor outlives it; the place in the scroll area is kept for the way back.
  onHide() {
    this.top = this.scrollEl.scrollTop;
    this.attached = false;
    if (raTab === this) raTab = null;
    this.actsEl.remove();
    this.stop();
  }

  dispose() {
    if (this.disposed) return;
    if (this.attached) this.onHide(); else this.stop();
    this.disposed = true;
    for (const sec of this.secs.values()) sec.el.remove();
    this.secs.clear();
    this.byEl.clear();
    this.order = [];
    if (typeof reviewBarUnmount === "function") reviewBarUnmount(this);
    this.actsEl.remove();
  }

  // buildActs: the Inline | Side by side control in the dock's bar (the single-file view's, the same preference).
  buildActs() {
    this.actsEl = raEl("div", "dk-vact");
    const el = editorDiffSeg("Diff layout");
    const inline = editorDiffSegItem("Inline", DG_ICON_INLINE, () => diffModeSet("inline"));
    const side = editorDiffSegItem("Side by side", DG_ICON_SIDE, () => diffModeSet("side"));
    side.title = "Side by side (shows inline by itself when the dock is too narrow for two sides)";
    el.append(inline, side);
    this.modeSeg = { el, inline, side };
    this.actsEl.append(el);
    this.paintMode();
  }

  paintMode() {
    const m = this.modeSeg;
    if (!m) return;
    const side = viewerPrefs.getDiffMode() === "side";
    m.inline.setAttribute("aria-pressed", side ? "false" : "true");
    m.side.setAttribute("aria-pressed", side ? "true" : "false");
  }

  // --- sections ---------------------------------------------------------------------------------------------

  // live: the tab may ask and hold editors: it is on screen and so is the dock (not closed, not folded away, no page over it, the page
  // not hidden).
  live() {
    return !this.disposed && this.attached && typeof editorSyncShown === "function" && editorSyncShown();
  }

  // sync brings the tab in step with what is known: the Changes rows and view, the scope, the marks, the notes, and whether the tab may
  // hold editors. Idempotent and cheap when nothing moved (it runs on every status, mark and note change, and with every drawer paint).
  sync() {
    if (this.disposed) return;
    const view = typeof dockChangesView === "function" ? dockChangesView() : null;
    const all = reviewAllRows(view && view.kind === "rows" ? view.rows : []);
    this.reconcile(all, view);
    if (!this.live()) { this.stop(); return; }
    this.start();
    this.plan();
  }

  // The state a section header draws: the key of each row (what an editor is for), the marks, the notes.
  reconcile(all, view) {
    const sid = this.sid;
    const scope = typeof dockScope === "function" ? dockScope(sid) : "head";
    const marks = typeof reviewMarksOf === "function" ? reviewMarksOf(sid) : {};
    const files = typeof reviewFilesOf === "function" ? reviewFilesOf(sid) : null;
    let pend = {};
    try { pend = notesPendingByPath(typeof notesFor === "function" ? notesFor(sid) : null); } catch (_) { /* no notes */ }
    const keep = new Set();
    const nodes = [];
    const order = [];
    for (const row of all.rows) {
      let sec = this.secs.get(row.path);
      if (!sec) { sec = this.newSection(row.path); this.secs.set(row.path, sec); this.byEl.set(sec.el, sec); if (this.io) this.watch(sec); }
      const k = editorDiffKey({ path: row.path, host: this.host });
      const key = [k.key, row.code, row.add, row.del].join("\n");
      if (sec.key !== key) { // another version of the file: what the editor shows is not it any more
        this.drop(sec);
        sec.key = key;
        sec.info = null;
        sec.h = 0;
        this.setBodyHeight(sec, raEstimate(row));
      }
      sec.row = row;
      sec.scope = scope;
      this.paintHead(sec, row, marks, files, pend);
      keep.add(row.path);
      nodes.push(sec.el);
      order.push(row.path);
    }
    for (const [path, sec] of [...this.secs]) {
      if (keep.has(path)) continue;
      this.drop(sec);
      if (this.io) { this.io.near.unobserve(sec.el); this.io.far.unobserve(sec.el); }
      sec.el.remove();
      this.byEl.delete(sec.el);
      this.secs.delete(path);
    }
    this.order = order;
    nodes.forEach((n, i) => { if (this.listEl.children[i] !== n) this.listEl.insertBefore(n, this.listEl.children[i] || null); });
    // The cap: the first REVIEW_ALL_MAX, and the way to the rest.
    this.moreEl.hidden = all.more === 0;
    if (all.more > 0) this.setText(this.moreEl, "Showing the first " + order.length + " of " + (order.length + all.more) + " changed files. Open files one by one from the Changes list");
    // One calm line where there is no list: git's own words, or "No changes".
    const say = view && view.kind === "note" ? view.text : order.length === 0 ? "No changes" : "";
    this.noteEl.hidden = say === "";
    this.setText(this.noteEl, say);
    this.noteEl.title = view && view.kind === "note" && view.title ? view.title : "";
    this.paintHeadBar(all.rows, marks, files, scope);
    this.paintMode();
  }

  setText(el, text) {
    if (el.textContent !== text) el.textContent = text;
  }

  paintHeadBar(rows, marks, files, scope) {
    let add = 0;
    let del = 0;
    let known = false;
    for (const r of rows) {
      if (Number.isFinite(r.add) && Number.isFinite(r.del)) { add += r.add; del += r.del; known = true; }
    }
    this.setText(this.countEl, rows.length + (rows.length === 1 ? " file" : " files"));
    this.ctEl.hidden = !known;
    this.setText(this.ctAdd, "+" + add);
    this.setText(this.ctDel, "−" + del);
    const base = typeof gitBaseFor === "function" && typeof ftRoot === "function" ? gitBaseFor(ftRoot()) : null;
    this.setText(this.cmpEl, editorDiffHeadLabel(scope, base && typeof base.baseName === "string" ? base.baseName : ""));
    const c = reviewedCount(rows, marks, files);
    this.setText(this.revEl, rows.length ? "reviewed " + c.done + " of " + c.total : "");
  }

  newSection(path) {
    const sec = {
      path, row: null, key: "", scope: "head", el: raEl("div", "dk-sec"), state: "idle", seq: 0, ac: null, timer: null, ed: null, img: null,
      info: null, h: 0, collapsed: false, near: false, far: false, settled: false, land: null,
    };
    const el = sec.el;
    el.setAttribute("role", "group");
    const head = raEl("div", "dk-sec-h");
    const caret = raEl("button", "btn btn-ghost btn-icon btn-sm dk-sec-caret");
    caret.type = "button";
    caret.append(svgIcon(RA_ICON_CARET, { icon: "caret", size: "14" }));
    caret.onclick = () => this.collapse(sec, !sec.collapsed);
    const ck = raEl("button", "dk-ck dk-sec-ck");
    ck.type = "button";
    ck.onclick = () => this.toggleMark(sec);
    const ic = raEl("span", "dk-ic");
    ic.setAttribute("aria-hidden", "true");
    const nm = raEl("span", "dk-nm");
    const dir = raEl("span", "dk-dir");
    const ct = raEl("span", "dk-ct");
    const add = raEl("span", "dk-add");
    const del = raEl("span", "dk-del");
    ct.append(add, del);
    const nt = raEl("span", "dk-nt");
    const st = raEl("span", "dk-st");
    const open = raEl("button", "btn btn-sm dk-sec-open", "Open");
    open.type = "button";
    open.onclick = () => { if (typeof drawerShowFile === "function" && sec.row) drawerShowFile(sec.row.path, { preview: false }); };
    head.append(caret, ck, ic, nm, dir, st, ct, nt, open);
    const body = raEl("div", "dk-sec-b");
    const inner = raEl("div", "dk-sec-in");
    body.append(inner);
    el.append(head, body);
    sec.parts = { head, caret, ck, ic, nm, dir, ct, add, del, nt, st, open };
    sec.body = body;
    sec.bodyIn = inner;
    return sec;
  }

  // paintHead writes a section's header from its row, the marks and the notes. Only what differs.
  paintHead(sec, row, marks, files, pend) {
    const p = sec.parts;
    const set = (n, t) => { if (n.textContent !== t) n.textContent = t; };
    set(p.ic, typeof iconFor === "function" ? iconFor(row.name, false, false) : "");
    set(p.nm, row.name);
    set(p.dir, row.dir);
    const full = typeof shortPath === "function" ? shortPath(row.path) : row.path;
    if (sec.el.getAttribute("aria-label") !== "Changes in " + row.name) sec.el.setAttribute("aria-label", "Changes in " + row.name);
    if (p.nm.title !== full) p.nm.title = full;
    const code = row.code || "";
    const stCls = "dk-st dk-st-" + code.toLowerCase();
    if (p.st.className !== stCls) p.st.className = stCls;
    set(p.st, code);
    p.st.title = DKC_CODE_TITLES[code] || code;
    const have = Number.isFinite(row.add) && Number.isFinite(row.del);
    p.ct.hidden = !have;
    if (have) { set(p.add, "+" + row.add); set(p.del, "−" + row.del); }
    const n = pend[row.path] || 0;
    p.nt.hidden = n === 0;
    if (n > 0) set(p.nt, n + (n === 1 ? " note" : " notes"));
    // The reviewed circle: a toggle, with the same reasons for not being pressable as the single-file header's Mark reviewed.
    const why = reviewWhy(this.sid, row);
    const done = !why && Object.prototype.hasOwnProperty.call(marks, row.path) && reviewedState(marks[row.path], row, files);
    const ckCls = done ? "dk-ck dk-sec-ck dk-ck-done" : "dk-ck dk-sec-ck";
    if (p.ck.className !== ckCls) p.ck.className = ckCls;
    p.ck.setAttribute("aria-pressed", done ? "true" : "false");
    p.ck.setAttribute("aria-disabled", why ? "true" : "false");
    const label = (done ? "Reviewed: " : "Mark reviewed: ") + row.name;
    if (p.ck.getAttribute("aria-label") !== label) p.ck.setAttribute("aria-label", label);
    const title = why || (done ? "Reviewed. Press to clear the mark." : "Mark this file reviewed. The mark clears if the agent edits it again.");
    if (p.ck.title !== title) p.ck.title = title;
    this.paintCaret(sec);
  }

  paintCaret(sec) {
    const c = sec.parts.caret;
    c.setAttribute("aria-expanded", sec.collapsed ? "false" : "true");
    const label = (sec.collapsed ? "Expand " : "Collapse ") + (sec.row ? sec.row.name : "");
    if (c.getAttribute("aria-label") !== label) c.setAttribute("aria-label", label);
    if (c.title !== label) c.title = label;
    sec.body.hidden = sec.collapsed;
  }

  toggleMark(sec) {
    const row = sec.row;
    if (!row || sec.parts.ck.getAttribute("aria-disabled") === "true") {
      if (sec.parts.ck.title && typeof showToast === "function") showToast(sec.parts.ck.title);
      return;
    }
    const marks = reviewMarksOf(this.sid);
    const done = Object.prototype.hasOwnProperty.call(marks, row.path) && reviewedState(marks[row.path], row, reviewFilesOf(this.sid));
    if (done) reviewUnmark(this.sid, row.path); else reviewMark(this.sid, row.path, row);
  }

  collapse(sec, on) {
    if (sec.collapsed === on) return;
    sec.collapsed = on;
    if (on) this.drop(sec);
    this.paintCaret(sec);
    if (!on) this.plan();
  }

  // --- the observers: which sections hold an editor ---------------------------------------------------------

  start() {
    if (this.io) return;
    if (typeof IntersectionObserver !== "function") { // no observer to ask: the first few sections, and the rest by a click on the list
      for (const p of this.order.slice(0, 5)) { const s = this.secs.get(p); s.near = s.far = true; }
      this.io = { near: null, far: null };
      return;
    }
    const mk = (margin, which) => new IntersectionObserver((entries) => this.seen(entries, which), { root: this.scrollEl, rootMargin: margin, threshold: 0 });
    this.io = { near: mk("100% 0px 100% 0px", "near"), far: mk("200% 0px 200% 0px", "far") };
    for (const sec of this.secs.values()) this.watch(sec);
  }

  watch(sec) {
    if (!this.io.near) return;
    this.io.near.observe(sec.el);
    this.io.far.observe(sec.el);
  }

  // stop: the tab is not on screen (or not allowed to hold anything): every editor and its requests go, and so do the observers.
  stop() {
    if (this.io) {
      if (this.io.near) { this.io.near.disconnect(); this.io.far.disconnect(); }
      this.io = null;
    }
    for (const sec of this.secs.values()) { sec.near = false; sec.far = false; this.drop(sec); }
  }

  seen(entries, which) {
    if (!this.io || this.disposed) return;
    for (const e of entries) {
      const sec = this.byEl.get(e.target);
      if (sec) sec[which] = e.isIntersecting;
    }
    this.plan();
  }

  // plan makes the editors the sections near the viewport lack and lets go of the ones more than two screens away (reviewAllPlan).
  plan() {
    if (!this.live() || !this.io) return;
    const near = new Set();
    const far = new Set();
    const have = new Set();
    for (const p of this.order) {
      const s = this.secs.get(p);
      if (s.near) near.add(p);
      if (s.far) far.add(p);
      if (s.state !== "idle") have.add(p); // an editor, a message or a request in flight: it holds something
    }
    const pl = reviewAllPlan(this.order, have, near, far);
    for (const p of pl.dispose) this.drop(this.secs.get(p));
    for (const p of pl.create) this.make(this.secs.get(p));
  }

  // --- one section ------------------------------------------------------------------------------------------

  setBodyHeight(sec, px) {
    sec.body.style.height = px > 0 ? px + "px" : "";
  }

  say(sec, text, err) {
    const d = raEl("div", "viewer-msg dk-ra-msg" + (err ? " err" : ""), text);
    sec.bodyIn.replaceChildren(d);
    this.setBodyHeight(sec, 0);
  }

  // drop: the section holds nothing any more: its requests are aborted and an answer that still comes is dropped (the sequence moved),
  // its image mount is disposed, its notes host and its editors and models go, and a placeholder as tall as it was stands in.
  drop(sec) {
    sec.seq++;
    if (sec.timer) { clearTimeout(sec.timer); sec.timer = null; }
    if (sec.ac) { sec.ac.abort(); sec.ac = null; }
    if (sec.img) { sec.img.dispose(); sec.img = null; }
    const ed = sec.ed;
    if (ed) {
      sec.ed = null;
      editorNotesDetachHost(sec); // before the editors go: its zones, hover marks and listeners are theirs to remove
      for (const s of ed.subs) { try { s.dispose(); } catch (_) { /* gone with its editor */ } }
      ed.diff.setModel(null);
      ed.diff.dispose();
      // The view model first, then the models: Monaco 0.57 never releases the view model setModel({original, modified}) makes, so its diff
      // in the worker was never cancelled, and a model gone when the worker answered made it throw "no diff result available" (an uncaught
      // error in the console). Owning the view model (createViewModel) and disposing it cancels that diff.
      ed.vm.dispose();
      ed.orig.dispose();
      ed.mod.dispose();
    }
    if (sec.state === "idle") return;
    const hh = sec.body.offsetHeight; // what it was, for the placeholder (0 while the tab is out of the document: the last measure stands)
    if (hh > 0) sec.h = hh;
    sec.state = "idle";
    sec.settled = false;
    sec.bodyIn.replaceChildren();
    this.setBodyHeight(sec, sec.h || (sec.row ? raEstimate(sec.row) : 0));
  }

  // make: the section is near the viewport and holds nothing: ask for its two sides and show the diff. An image gets the comparer.
  async make(sec) {
    const row = sec.row;
    if (!row || sec.collapsed || sec.state !== "idle" || !this.live()) return;
    const scope = sec.scope;
    if (imageDiffIs(row.path)) {
      sec.state = "live";
      sec.info = { kind: "stop" };
      sec.img = imageDiffMount(sec.bodyIn, row.path, scope, { key: sec.key, deleted: row.code === "D" });
      this.setBodyHeight(sec, 0);
      this.applyLand(sec);
      return;
    }
    const seq = ++sec.seq;
    sec.state = "loading";
    sec.bodyIn.replaceChildren(raEl("div", "viewer-msg dk-ra-msg", "loading…"));
    const ac = typeof AbortController === "function" ? new AbortController() : { signal: undefined, abort() {} };
    sec.ac = ac;
    sec.timer = setTimeout(() => ac.abort(), RA_FETCH_MS);
    const post = raCall("/api/git/show", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: row.path, scope }) }, ac);
    // A deleted file has no new side to read: it is empty by definition.
    const read = row.code === "D" ? Promise.resolve({ ok: false, status: 404, data: null }) : raCall("/api/fs/read?path=" + encodeURIComponent(row.path), {}, ac);
    const [blob, rd, mon] = await Promise.all([post, read, loadMonaco().then((m) => ({ m }), (err) => ({ err }))]);
    if (sec.seq !== seq || this.disposed) return; // the section was let go meanwhile: the answer is for nobody
    if (!this.live()) { this.drop(sec); return; } // the tab left the screen while the answer was on its way: nothing is kept for it
    if (sec.timer) { clearTimeout(sec.timer); sec.timer = null; }
    sec.ac = null;
    this.show(sec, row, scope, blob, rd, mon);
  }

  // show turns the two answers into what the section holds: the diff editor, or a sentence saying why not.
  show(sec, row, scope, blob, rd, mon) {
    const stop = (text, err) => { sec.state = "msg"; sec.info = { kind: "stop" }; sec.h = 40; this.say(sec, text, err); this.applyLand(sec); };
    const d = rd.ok ? rd.data : null;
    if (d && d.kind === "binary") return stop("binary file changed");
    if (d && d.kind === "image") { // an image the extension did not say: the comparer takes it as it does any
      sec.state = "live";
      sec.info = { kind: "stop" };
      sec.bodyIn.replaceChildren();
      sec.img = imageDiffMount(sec.bodyIn, row.path, scope, { key: sec.key, deleted: false });
      this.setBodyHeight(sec, 0);
      this.applyLand(sec);
      return;
    }
    let text = "";
    if (rd.ok && d && d.kind === "text") text = typeof d.content === "string" ? d.content : "";
    else if (rd.status === 413) return stop("too large to show here. Open the file to read it.");
    else if (rd.status !== 404) return stop("could not read the file: " + ((rd.data && rd.data.error) || (rd.status ? "HTTP " + rd.status : "no answer")), true);
    const b = editorDiffBase(blob.ok ? blob.data : null);
    if (b.head === null) return stop("Changes unavailable: " + b.why, true);
    if (mon.err) return stop("The editor could not be loaded: " + (mon.err.message || "unknown error"), true);
    if (!dockMonaco) dockMonaco = mon.m;
    const M = dockMonaco;
    const lang = monacoLang(row.path);
    const orig = M.editor.createModel(b.head, lang);
    const mod = M.editor.createModel(text.charCodeAt(0) === 0xfeff ? text.slice(1) : text, lang);
    const host = raEl("div", "dk-sec-mon");
    sec.bodyIn.replaceChildren(host);
    // ignoreTrimWhitespace off, as the single-file view: a whitespace-only change is shown, as git diff does.
    const diff = M.editor.createDiffEditor(host, Object.assign({
      theme: MONACO_THEME,
      readOnly: true,
      readOnlyMessage: { value: "Changes is a review view. Open the file and switch to Code to edit." },
      originalEditable: false,
      ignoreTrimWhitespace: false,
      automaticLayout: true,
      minimap: { enabled: false },
      stickyScroll: { enabled: false },
      scrollBeyondLastLine: false,
      fixedOverflowWidgets: true,
      hideUnchangedRegions: { enabled: true },
      scrollbar: { alwaysConsumeMouseWheel: false }, // a wheel the editor cannot use goes on to the tab's scroll
    }, editorDiffOptions()));
    const mEd = diff.getModifiedEditor();
    const oEd = diff.getOriginalEditor();
    const vm = diff.createViewModel({ original: orig, modified: mod });
    const ed = { diff, orig, mod, mEd, oEd, vm, subs: [] };
    sec.ed = ed;
    sec.state = "live";
    const ld = lineDiff(b.head, mod.getValue());
    sec.info = ld.capped ? { kind: "stop" } : { kind: "text", changes: ld.changes, lineCount: mod.getLineCount() };
    editorBindChangeKeys(mEd); // F7 / Shift+F7 are the page's keys, bound once on whichever editor exists first
    editorBindNoteKeys(mEd);   // likewise Add note
    // The section is as tall as its content (the collapsed regions included), up to REVIEW_ALL_HEIGHT, then it scrolls inside.
    // Not before Monaco's diff is computed: until then nothing is collapsed and the content is the whole file (up to the cap), which would
    // push the sections after it far down, out of the two screens, and they would be let go and made again when the regions fold.
    const fit = () => {
      if (sec.ed !== ed || !sec.settled) return;
      const h = reviewAllHeight(Math.max(mEd.getContentHeight(), oEd.getContentHeight()));
      if (h === sec.h && sec.body.style.height === h + "px") return;
      sec.h = h;
      this.setBodyHeight(sec, h);
      diff.layout();
    };
    ed.fit = fit;
    ed.subs.push(mEd.onDidContentSizeChange(fit), oEd.onDidContentSizeChange(fit));
    ed.subs.push(mEd.onDidFocusEditorText(() => { this.nav = { path: sec.path, line: (mEd.getPosition() || { lineNumber: 0 }).lineNumber, stop: false }; }));
    const computed = diff.onDidUpdateDiff(() => {
      computed.dispose();
      if (sec.ed !== ed) return;
      sec.settled = true;
      fit();
      this.applyLand(sec);
    });
    ed.subs.push(computed);
    diff.setModel(vm);
    editorNotesAttachHost({
      key: sec,
      sid: () => this.sid,
      path: () => sec.path,
      rel: () => (typeof relativePath === "function" && typeof ftRoot === "function" ? relativePath(ftRoot(), sec.path) : null) || sec.path,
      diff,
    });
    fit();
  }

  // --- scrolling and F7 --------------------------------------------------------------------------------------

  // scrollTo: a Changes row was pressed (reviewAllScrollTo): the section of `path` goes to the top of the scroll area, open. False when
  // the path has no section (a folder, one past the cap).
  scrollTo(path) {
    const sec = this.secs.get(path);
    if (!sec) return false;
    if (!this.attached) { this.want = path; return true; }
    if (sec.collapsed) this.collapse(sec, false);
    this.scrollEl.scrollTop = sec.el.offsetTop;
    return true;
  }

  // reveal puts the cursor of a section's new side on `line` and shows it: inside the section's own scroll, then the tab's scroll puts
  // the line a third of the way down.
  reveal(sec, line, focus) {
    const ed = sec.ed;
    if (!ed) return;
    if (focus) ed.mEd.focus();
    ed.mEd.setPosition({ lineNumber: line, column: 1 });
    ed.mEd.revealLineInCenter(line, dockMonaco.editor.ScrollType.Immediate);
    const rel = ed.mEd.getTopForLineNumber(line) - ed.mEd.getScrollTop();
    const at = sec.body.getBoundingClientRect().top - this.scrollEl.getBoundingClientRect().top + this.scrollEl.scrollTop + rel;
    this.scrollEl.scrollTop = Math.max(0, at - this.scrollEl.clientHeight / 3);
    this.nav = { path: sec.path, line, stop: false };
  }

  // applyLand does what F7 asked of a section that had no diff when it asked: the line it named, or the first / last change.
  applyLand(sec) {
    const land = sec.land;
    sec.land = null;
    if (!land || Date.now() - land.ts > RA_LAND_MS || !this.live()) return;
    const info = sec.info;
    if (info && info.kind === "stop") this.nav = { path: sec.path, line: 0, stop: true };
    if (info && info.kind === "text" && sec.ed && info.changes.length) {
      let line = land.line;
      if (!Number.isFinite(line)) line = reviewLineOf(land.at === "last" ? info.changes[info.changes.length - 1] : info.changes[0], info.lineCount);
      this.reveal(sec, line, true);
    } else if (info && info.kind === "text" && !info.changes.length) { // nothing to read in it: on to the next one, the same way
      this.nav = { path: sec.path, line: land.at === "last" ? Infinity : 0 };
      this.step(land.at === "last" ? -1 : 1);
    }
  }

  // fromPoint: where F7 starts from: the section whose editor has the keyboard (and its cursor line), else where F7 last landed while
  // that is still near the viewport, else the section at the top of the scroll area, before its first line.
  fromPoint() {
    // A stop (an image, a binary file) has no editor to hold the keyboard, which stays where it was: where F7 landed is the place.
    if (this.nav && this.nav.stop && this.secs.get(this.nav.path) && this.secs.get(this.nav.path).near) return { idx: this.order.indexOf(this.nav.path), line: 0 };
    for (let i = 0; i < this.order.length; i++) {
      const s = this.secs.get(this.order[i]);
      if (s.ed && (s.ed.mEd.hasTextFocus() || s.ed.oEd.hasTextFocus())) {
        const p = s.ed.mEd.getPosition();
        return { idx: i, line: p ? p.lineNumber : 0 };
      }
    }
    if (this.nav) {
      const i = this.order.indexOf(this.nav.path);
      if (i >= 0 && this.secs.get(this.nav.path).near) return { idx: i, line: this.nav.line };
    }
    const top = this.scrollEl.scrollTop;
    const i = this.order.findIndex((p) => { const e = this.secs.get(p).el; return e.offsetTop + e.offsetHeight > top + 1; });
    return { idx: i < 0 ? 0 : i, line: 0 };
  }

  // step: F7 (dir 1) / Shift+F7 (dir -1). "moved", "pending" (a section that has no diff yet was scrolled to and will land) or "none".
  step(dir) {
    if (this.disposed || !this.order.length) return "none";
    const infos = this.order.map((p) => { const i = this.secs.get(p).info; return i && i.kind ? i : { kind: "unknown" }; });
    const plan = reviewAllStepPlan(infos, this.fromPoint(), dir);
    if (!plan) {
      if (typeof showToast === "function") showToast(dir === 1 ? "That was the last change" : "That was the first change");
      return "none";
    }
    const sec = this.secs.get(this.order[plan.idx]);
    if (sec.collapsed) this.collapse(sec, false);
    if (plan.at === "change" && sec.ed && sec.settled) { this.reveal(sec, plan.line, true); return "moved"; }
    sec.land = plan.at === "change" ? { line: plan.line, ts: Date.now() } : { at: plan.land, ts: Date.now() };
    this.scrollEl.scrollTop = sec.el.offsetTop;
    if (sec.info && sec.info.kind === "stop") { // an image, a binary file, one that could not be read: the scroll is the landing
      sec.land = null;
      this.nav = { path: sec.path, line: 0, stop: true };
    }
    else if (sec.ed && sec.settled) this.applyLand(sec);
    return "pending";
  }

  // applyPrefs: the layout, wrap, line numbers or font size changed: every section editor follows.
  applyPrefs() {
    this.paintMode();
    for (const sec of this.secs.values()) {
      if (!sec.ed) continue;
      sec.ed.diff.updateOptions(editorDiffOptions());
      sec.ed.fit();
    }
  }

  focused() {
    for (const sec of this.secs.values()) if (sec.ed && (sec.ed.mEd.hasTextFocus() || sec.ed.oEd.hasTextFocus())) return true;
    return false;
  }
}

// --- the page's calls ---------------------------------------------------------------------------------------

// reviewAllOpen: show the session's All changes tab, and the section of `path` when one is named. false when the dock is not on that session.
function reviewAllOpen(sid, path) {
  return typeof dockShowReview === "function" ? dockShowReview(sid || (typeof drawerState !== "undefined" ? drawerState.sessionId : null), path) : false;
}

// reviewAllScrollTo: a Changes row was pressed while the tab is the one on screen: its section is brought to the top. false when the tab is
// not on screen or the path has no section there: the press then opens the file as it always did.
function reviewAllScrollTo(path) {
  return !!raTab && raTab.attached && !raTab.disposed && raTab.scrollTo(path);
}

// reviewAllStep: F7 / Shift+F7. true when the tab is the one on screen (it handled the key, even where there was nothing left to step to).
function reviewAllStep(dir) {
  if (!raTab || !raTab.attached || raTab.disposed) return false;
  raTab.step(dir);
  return true;
}

// reviewAllSync: something the tab shows moved (a status, a mark, a note, the dock opening or closing, the page hidden).
function reviewAllSync() {
  if (raTab) raTab.sync();
}

// reviewAllActive: the tab is the one on screen.
function reviewAllActive() {
  return !!raTab && raTab.attached && !raTab.disposed;
}

// reviewAllApplyPrefs / reviewAllFocused: the single-file view's layout, wrap and Alt+Z (editordiff.js) reach the sections too.
function reviewAllApplyPrefs() {
  if (raTab) raTab.applyPrefs();
}

function reviewAllFocused() {
  return !!raTab && raTab.attached && raTab.focused();
}

if (typeof document !== "undefined" && document.addEventListener) document.addEventListener("visibilitychange", reviewAllSync);
