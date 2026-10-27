"use strict";

// Layout model: cells are grid windows. Opening a session auto-splits a new
// window (grow 1→2→3→4, capped at `cap`); beyond the cap it stacks as a tab.
// Opening a file always tabs into the focused cell. ⛶ maximizes one cell.
let cells = [];
// INVARIANT. The focus ring is painted by whoever last touched the model:
// every path that assigns focusedCell must end in setFocusedCell (which toggles
// the class itself) or in renderGrid (whose renderBar sweep toggles it). Several
// places assign it directly (removeCell, applyCap, ensureFirstCell, applyPlan),
// and each is safe only because it renders afterwards. Deliberately NOT routed
// through setFocusedCell: that would fire syncFocusedTree and a layout save from
// inside those mutations. Assign directly if you must, then render.
//
// Every assignment — direct or via setFocusedCell — funnels through
// setFocusedCellRaw below so the status line's cwd field never goes stale. That
// funnel is intentionally the ONLY thing it adds: it must stay side-effect-free
// (no syncFocusedTree, no scheduleSaveLayout) or the invariant above breaks.
//
// SECOND INVARIANT. Every mutation that can change the focused cell OR its
// active tab ends in syncFocusedTree(), at its TAIL, after the render. The tree
// used to follow the *event* of focus changing rather than the *state* of what
// the focused pane shows, so removeCell / closeTab / applyCap left it rooted at
// a directory nobody was looking at — and setFocusedCell's equality guard then
// swallowed every click that would have fixed it.
// internal/web/jstest/tree-follows-focus.test.js pins this at the source level;
// if you add a mutation, it will tell you.
//
// The sync follows the focused TAB: syncFocusedTree() reads focusedTab() and
// hands it to treeFollowTarget(), so a terminal follows its cwd and a cell
// showing a file follows that file's directory. The tree holds its last root
// only when treeFollowTarget returns null — an unresolved cwd, a relative path,
// or a directory outside $HOME, which the server refuses to list anyway.
//
// It used to read focusedTerminal(), which is null whenever the active tab is a
// viewer; that left the tree showing project Y while the focused pane displayed
// a file from project X.
let focusedCell = null;

// setFocusedCellRaw is the sole place `focusedCell` is written. Direct-assignment
// call sites (removeCell, applyCap, ensureFirstCell, applyPlan) route through
// this instead of `focusedCell = x` so a future assignment site can't forget to
// keep the status line current — but it does none of setFocusedCell's other
// work, so it stays safe to call from inside a mutation that renders afterwards.
function setFocusedCellRaw(cell) {
  focusedCell = cell;
  if (typeof updateStatusLine === "function") updateStatusLine();
}
let cap = 4; // max windows before opens stack as tabs (the 1/2/4 buttons set it)
let maximized = null;
// Below this width a pane's desktop header keeps dot + name + one chip (D9).
const NARROW_PANE_PX = 560;

// `restoring` is what protects the stored snapshot during boot: every layout call
// boot makes (applyCap(4) and friends) would otherwise write an empty grid over it
// before the restore has read it. bootLayout is captured at script-evaluation time
// as well, so the snapshot in hand can never depend on when the flag is cleared.
const bootLayout = typeof readLayout === "function" ? readLayout() : null;
let restoring = true;
let saveTimer = null;
// gaveUp latches when boot could not read the live session list twice over
// (endRestore(false), from app.js's restoreBoot). Such a page-load never
// restored anything, so its grid is the empty one applyCap(4) built, and
// serializing THAT yields null — which would REMOVE the user's real snapshot.
//
// It suppresses only the null write, not every write: once the user has opened
// something by hand the grid is theirs and is worth storing, whatever boot did
// or did not learn. Latching writes off for the whole page-load would silently
// discard hours of layout after one transient blip at boot.
let gaveUp = false;
// unsaved = this page-load holds a change it has not written yet. pagehide
// consults it so an idle tab that has nothing new to say cannot overwrite the
// snapshot a DIFFERENT, still-open tab wrote. Scrolling a file sets it directly:
// it changes what must be stored but is not a layout change, so unlike every
// other edit it schedules no write of its own.
let unsaved = false;
function markLayoutUnsaved() { unsaved = true; }

// scheduleSaveLayout debounces the write: applyCap collapses several cells in one
// loop, and one user action should cost one write, not one per step.
function scheduleSaveLayout() {
  if (restoring) return;
  unsaved = true;
  if (saveTimer) return;
  saveTimer = setTimeout(saveLayoutNow, 200);
}

function saveLayoutNow() {
  if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
  if (restoring || typeof writeLayout !== "function") return;
  const snapshot = serializeLayout({ cells, focused: focusedCell, maximized, cap });
  // A boot that never learned the session list may not DELETE the snapshot: its
  // empty grid says nothing about what the user had. It may still write a real
  // one — see gaveUp.
  if (gaveUp && !snapshot) return;
  writeLayout(snapshot);
  unsaved = false;
}

// Flush on unload: the last change before a reload is exactly the one the
// restore needs, and it is the one most likely to still be inside the debounce.
// pagehide (not unload) is the event that also fires for the bfcache path.
//
// Gated on `unsaved`, not on `saveTimer`. The timer alone was too narrow —
// scrolling a file changes what must be stored but schedules no write, so a
// scroll-only visit persisted nothing. Flushing UNCONDITIONALLY is too wide in
// the other direction: a tab left open since morning would, on being closed,
// serialize its own stale grid over the layout another live tab had just
// written. `unsaved` restores the property that made the old guard right — a
// page with nothing new to say writes nothing — while covering scroll.
window.addEventListener("pagehide", () => { if (unsaved) saveLayoutNow(); });

// gridCols → columns/rows the grid uses, derived from the live cell count.
function gridCols() { return Math.min(Math.max(cells.length, 1), 4); }

class Cell {
  constructor() {
    this.tabs = [];
    this.activeTab = null;
    this.el = document.createElement("div");
    this.el.className = "cell";
    this.bar = document.createElement("div");
    this.bar.className = "cell-bar";
    this.body = document.createElement("div");
    this.body.className = "cell-body";
    this.el.append(this.bar, this.body);
    // Registered BEFORE the focus listener below, so it sees focus as it was
    // when the press began. The desktop header's Files/Access tabs need that:
    // a press on an unfocused pane moves focus first, the open drawer follows
    // focus to this pane's session, and only then does the click land, which
    // would read "already open here" and close it. See headerDrawerPress.
    this.el.addEventListener("mousedown", () => { this._pressFocused = focusedCell === this; }, true);
    this.el.addEventListener("mousedown", () => setFocusedCell(this), true);
    // Desktop header narrow rule (D9): below NARROW_PANE_PX the header keeps
    // one chip. A ResizeObserver, not a media query: a pane narrows with the
    // layout (1/2/4, sidebar, window) and only the cell knows its own width.
    // It re-renders only when the verdict flips, so a drag costs nothing per
    // pixel. dispose() disconnects it.
    this._narrow = false;
    this._ro = null;
    if (typeof ResizeObserver === "function") {
      this._ro = new ResizeObserver((entries) => {
        const w = entries.length ? entries[entries.length - 1].contentRect.width : 0;
        if (!w) return; // detached (another pane is maximized): keep the last verdict
        const narrow = w < NARROW_PANE_PX;
        if (narrow === this._narrow) return;
        this._narrow = narrow;
        if (!(typeof mobileActive === "function" && mobileActive(window.innerWidth))) this.renderBar();
      });
      this._ro.observe(this.el);
    }
    // Explicit so the sentinel is deliberate, not an implicit `undefined`
    // that syncNeeds' first call happens to treat correctly.
    this._needsKey = "";
    this._strip = null;
    this.renderBar();
    this.renderBody();
  }

  addTab(tab) {
    tab.onUpdate = () => this.renderBar();
    this.tabs.push(tab);
    this.activate(tab);
    scheduleSaveLayout();
  }

  activate(tab) {
    if (tab && tab.kind === "terminal") this._lastTerm = tab; // see cellTerminal
    if (this.activeTab === tab) {
      this.renderBody(); tab.onShow?.();
    } else {
      if (this.activeTab) this.activeTab.onHide?.();
      this.activeTab = tab;
      this.renderBar();
      this.renderBody();
      tab.onShow?.();
    }
    if (this === focusedCell) { syncFocusedTree(); renderSteerRail(); } // tree and rail follow the focused tab
    if (typeof syncSelectedRow === "function") syncSelectedRow();
    scheduleSaveLayout();
  }

  closeTab(tab) {
    const i = this.tabs.indexOf(tab);
    if (i < 0) return;
    this.tabs.splice(i, 1);
    if (this._lastTerm === tab) this._lastTerm = null;
    tab.dispose?.();
    if (this.tabs.length === 0 && cells.length > 1) { removeCell(this); return; } // reflow: drop the emptied window
    if (this.activeTab === tab) {
      this.activeTab = this.tabs[Math.min(i, this.tabs.length - 1)] || null;
      if (this.activeTab) this.activeTab.onShow?.();
    }
    this.renderBar();
    this.renderBody();
    refreshSessions();
    scheduleSaveLayout();
    // The active tab changed without going through activate(), so if this cell
    // holds focus the tree is still rooted at the session whose tab just closed.
    // Gated like activate() for the same reason: an unfocused cell's close says
    // nothing about where the tree belongs.
    if (this === focusedCell) { syncFocusedTree(); renderSteerRail(); }
    // The desktop drawer may name this very session even from an unfocused pane
    // (Open files here); it must not keep naming a closed one. (The focused case
    // is already covered: syncFocusedTree ends in drawerFollowFocus.)
    if (typeof drawerTabClosed === "function") drawerTabClosed(tab.id);
  }

  // detachTab takes a tab OUT of this cell without ending it: same bookkeeping
  // as closeTab, minus tab.dispose(). Moving a terminal tab between panes must
  // NOT dispose it — dispose tears down the socket, and a session admits one
  // subscriber, so a "move" that closed and reopened would evict itself.
  // The emptied-cell case routes through the same removeCell closeTab uses, so
  // a move can never leave a stray empty pane behind.
  detachTab(tab) {
    const i = this.tabs.indexOf(tab);
    if (i < 0) return;
    this.tabs.splice(i, 1);
    if (this._lastTerm === tab) this._lastTerm = null;
    if (this.tabs.length === 0 && cells.length > 1) { removeCell(this); return; } // reflow: drop the emptied window
    if (this.activeTab === tab) {
      this.activeTab = this.tabs[Math.min(i, this.tabs.length - 1)] || null;
      if (this.activeTab) this.activeTab.onShow?.();
    }
    this.renderBar();
    this.renderBody();
    scheduleSaveLayout();
    if (this === focusedCell) { syncFocusedTree(); renderSteerRail(); }
  }

  renderBody() {
    if (this.activeTab) {
      this.body.replaceChildren(this.activeTab.el);
      // The element just lost and regained its layout box, so anything inside it
      // that scrolls is back at the top: let the tab put its position back.
      this.activeTab.onAttach?.();
    } else {
      const ph = document.createElement("div");
      ph.className = "cell-empty";
      ph.textContent = "Click a session or a file to open it here";
      this.body.replaceChildren(ph);
    }
  }

  renderBar() {
    // Desktop: the one-line pane header. Mobile hides .cell-bar altogether and
    // keeps the tab strip below exactly as it was.
    if (typeof mobileActive === "function" && !mobileActive(window.innerWidth)) {
      this.renderHeader();
      this.bar.hidden = this.tabs.length === 0;
      this.el.classList.toggle("focus", this === focusedCell);
      this.syncNeeds();
      return;
    }
    const tabs = this.tabs.map((t) => {
      const b = document.createElement("div");
      b.className = "tab" + (t === this.activeTab ? " act" : "");
      // Activity dot leads a terminal tab (viewer tabs have no activity). Re-
      // emitted here every rebuild, so it survives renderBar wiping the strip.
      if (t.kind === "terminal" && typeof t.activity === "function") {
        const a = activityClass(t.activity(), typeof t.needWhy === "function" ? t.needWhy() : "");
        const dot = document.createElement("span");
        dot.className = a.cls;
        if (a.title) { dot.title = a.title; dot.setAttribute("role", "img"); dot.setAttribute("aria-label", a.title); }
        b.append(dot);
      }
      const label = document.createElement("span");
      label.className = "tab-label";
      label.textContent = t.title();
      label.title = t.title();
      b.onclick = () => this.activate(t);
      const st = document.createElement("span");
      st.className = "tab-st";
      // The healthy steady state ("connected") is the common case and carries no
      // information beyond "this tab works" — the mockup (D1) shows just the name
      // + context%, so suppress that one word. Every other status ("connecting…",
      // "reconnecting…", "error", "taken over", "ended", …) is a real signal and
      // still renders.
      st.textContent = t.status() === "connected" ? "" : t.status();
      const x = document.createElement("button");
      x.className = "tab-x"; x.textContent = "×"; x.title = "Close tab";
      x.onclick = (e) => { e.stopPropagation(); this.closeTab(t); };
      b.append(label, st);
      // The session's agent account, when it is not the default: a tab is
      // where you type into it, so the account must be visible right there.
      if (t.kind === "terminal") {
        const ts = (lastSessions || []).find((x) => x.id === t.id);
        const ab = acctBadgeEl(ts);
        if (ab) b.append(ab);
        const awsb = awsBadgeEl(ts);
        if (awsb) b.append(awsb);
      }
      // Per-window context readout (claude only; usageTab is "" otherwise). Re-
      // emitted every rebuild from the tab's stored usage, like the dot above.
      if (t.kind === "terminal" && typeof t.usage === "function") {
        const ustr = usageTab(t.usage());
        if (ustr) {
          const us = document.createElement("button");
          us.type = "button";
          us.className = "tab-usage";
          us.textContent = ustr;
          const title = usageTitle(t.usage());
          us.title = (title ? title + " — " : "") + "click for context by tool";
          us.onclick = (e) => { e.stopPropagation(); openSteerFor(t.id); };
          b.append(us);
        }
      }
      // Kill lives on the TAB, not the cell: a cell can hold several session tabs
      // and killing is per-session. ⏻ ends the session, × only closes the view.
      if (t.kind === "terminal") {
        const kill = document.createElement("button");
        kill.className = "tab-kill"; kill.textContent = "⏻";
        kill.title = "End session (ends the agent)";
        kill.onclick = (e) => { e.stopPropagation(); confirmKill(t.id, t.title()); };
        b.append(kill);
      }
      b.append(x);
      return b;
    });
    const spacer = document.createElement("span");
    spacer.className = "cell-sp";
    const sh = document.createElement("button");
    sh.className = "cell-sh"; sh.textContent = "+sh";
    sh.title = "New shell beside this session, same directory and tier (Ctrl+Shift+`)";
    sh.hidden = !window.shellProfile; // no detected shell → no quick shell
    // Disable while in flight so the ignored second click is visible, not silent.
    sh.onclick = (e) => {
      e.stopPropagation();
      sh.disabled = true;
      Promise.resolve(spawnShellIn(this)).finally(() => { sh.disabled = false; });
    };
    const max = document.createElement("button");
    max.className = "cell-max"; max.textContent = maximized === this ? "❐" : "⛶";
    max.title = "Maximize / restore";
    max.onclick = (e) => { e.stopPropagation(); toggleMax(this); };
    const del = document.createElement("button");
    del.className = "cell-del"; del.textContent = "×"; del.title = "Remove pane";
    del.onclick = (e) => { e.stopPropagation(); removeCell(this); };
    // Controls live in one sticky cluster so they stay reachable when the tab
    // strip overflows (a capped cell stacks many tabs: see openTerminal).
    const ctl = document.createElement("span");
    ctl.className = "cell-ctl";
    ctl.append(sh, max, del);
    this.bar.replaceChildren(...tabs, spacer, ctl);
    // An empty cell used to wear the full +sh / ⛶ / × strip over a black
    // rectangle, which reads as a broken terminal rather than an empty pane.
    // Quick-shell stays reachable from the palette ("New shell here") and
    // Ctrl+Shift+`. NOTE: .cell-bar carries an author `display: flex`, so the
    // hidden attribute alone does nothing: see .cell-bar[hidden] in the stylesheet.
    this.bar.hidden = this.tabs.length === 0;
    this.el.classList.toggle("focus", this === focusedCell);
    this.syncNeeds();
  }

  // renderHeader builds the desktop pane header (spec 5.3): one line, fixed
  // slot order, empty slots collapse. Left: dot, name (a switcher when the pane
  // holds several tabs), chips, model, context %. Right: Terminal · Files ·
  // Access, + Shell, then ⋯ ⛶ × on hover. paneHeaderModel (deskshell.js) owns
  // what shows; this only paints it. Rebuilt on every renderBar like the strip
  // it replaces, so no listener outlives its node.
  renderHeader() {
    if (this.tabs.length === 0) { this.bar.replaceChildren(); return; }
    const t = this.activeTab;
    const term = cellTerminal(this);
    const sid = term ? term.id : null;
    const s = sid ? (lastSessions || []).find((x) => x.id === sid) || null : null;
    const m = paneHeaderModel({
      tab: t, session: t && t.kind === "terminal" ? s : null, nowMs: Date.now(),
      boxRuntime: typeof boxRuntime === "string" ? boxRuntime : "", narrow: this._narrow,
    });
    // Classes are assigned as literals at each call site (not passed in), so
    // classnames.test.js can see every one of them.
    const el = (tag, text) => {
      const n = document.createElement(tag);
      if (text != null) n.textContent = text;
      if (tag === "button") n.type = "button";
      return n;
    };

    const main = el("div");
    main.className = "ph-main";
    // The terminal's own pushed activity, like the old tab dot; a viewer has none.
    const a = t && t.kind === "terminal" && typeof t.activity === "function"
      ? activityClass(t.activity(), typeof t.needWhy === "function" ? t.needWhy() : "") : m.dot;
    if (a) {
      const dot = el("span");
      dot.className = a.cls + " ph-dot";
      // Narrow: the connection state (see .ph-st below) joins the title.
      const st = this._narrow && t && t.kind === "terminal" && typeof t.status === "function" && t.status() !== "connected" ? t.status() : "";
      const title = [a.title, st].filter(Boolean).join(" \u00B7 ");
      if (title) { dot.title = title; dot.setAttribute("role", "img"); dot.setAttribute("aria-label", title); }
      main.append(dot);
    }
    // Several tabs in one pane: the name is a switcher, `name ▾ · N` (spec 7).
    let name;
    if (this.tabs.length > 1) {
      name = el("button");
      name.className = "ph-name ph-switch";
      const nm = el("span", m.name);
      nm.className = "ph-nm";
      const cnt = el("span", "\u25BE \u00B7 " + this.tabs.length);
      cnt.className = "ph-cnt";
      name.append(nm, cnt);
      name.title = m.name + " (" + this.tabs.length + " tabs in this pane)";
      name.setAttribute("aria-haspopup", "menu");
      name.onclick = (e) => { e.stopPropagation(); openTabSwitcher(name, this); };
    } else {
      name = el("span", m.name);
      name.className = "ph-name";
      name.title = m.name;
    }
    main.append(name);
    // A connection state other than the healthy one is a real signal (the old
    // strip showed it too): connecting…, reconnecting…, ended, taken over. A
    // narrow header is dot + name + ONE chip (D9), so there it rides in the
    // dot's title instead.
    const stText = t && t.kind === "terminal" && typeof t.status === "function" && t.status() !== "connected" ? t.status() : "";
    if (stText && !this._narrow) {
      const st = el("span", stText);
      st.className = "ph-st";
      main.append(st);
    }
    for (const c of m.chips) {
      const chip = el("span", c.text);
      chip.className = "ph-chip " + c.tone;
      if (c.title) chip.title = c.title;
      main.append(chip);
    }
    // The agent account, when it is not the default: where you type into a
    // session, the account must be visible (it was on the old tab).
    if (!this._narrow && t && t.kind === "terminal") {
      const ab = acctBadgeEl(s);
      if (ab) main.append(ab);
    }
    if (m.model) {
      const mo = el("span", m.model);
      mo.className = "ph-model mono";
      main.append(mo);
    }
    if (m.ctx && sid) {
      // R2: the same action as the old tab's usage chip, Context by tool.
      const ctx = el("button", m.ctx);
      ctx.className = "ph-ctx";
      const ut = typeof usageTitle === "function" ? usageTitle(t.usage()) : "";
      ctx.title = (ut ? ut + " \u2014 " : "") + "click for context by tool";
      ctx.onclick = (e) => { e.stopPropagation(); openSteerFor(sid); };
      main.append(ctx);
    }

    const side = el("div");
    side.className = "ph-side";
    const tabs = el("div");
    tabs.className = "ph-tabs";
    tabs.setAttribute("role", "group");
    tabs.setAttribute("aria-label", "Pane views");
    if (term) {
      const tt = el("button", "Terminal");
      tt.className = "ph-tab" + (t === term ? " on" : "");
      tt.onclick = (e) => {
        e.stopPropagation();
        // Terminal means "show me the agent": its tab, and no drawer over it.
        if (typeof closeDrawerFor === "function") closeDrawerFor(sid);
        if (this.activeTab !== term) this.activate(term);
      };
      tabs.append(tt);
    }
    for (const [kind, label] of [["files", "Files"], ["access", "Access"]]) {
      if (kind === "access" && !term) continue; // a pane of files has no session to describe
      const b = el("button", label);
      b.className = "ph-tab";
      b.dataset.k = kind;
      b.onclick = (e) => { e.stopPropagation(); headerDrawerPress(this, kind, sid, e); };
      tabs.append(b);
    }
    const sep = el("span");
    sep.className = "ph-sep";
    side.append(tabs, sep);
    const sh = el("button", "+ Shell");
    sh.className = "ph-shell";
    sh.title = "New shell beside this session, same directory and tier (Ctrl+Shift+`)";
    sh.hidden = !window.shellProfile; // no detected shell → no quick shell
    sh.onclick = (e) => {
      e.stopPropagation();
      sh.disabled = true;
      Promise.resolve(spawnShellIn(this)).finally(() => { sh.disabled = false; });
    };
    side.append(sh);
    if (s) {
      // The same menu as the sidebar row's ⋯, End session included (spec 7).
      const more = el("button", "\u22EF");
      more.className = "ph-ic ph-more";
      more.title = "Session actions";
      more.setAttribute("aria-label", "Session actions");
      more.setAttribute("aria-haspopup", "menu");
      more.onclick = (e) => { e.stopPropagation(); toggleSessionMenuAt(more, s); };
      side.append(more);
    }
    // ⛶ stays visible while maximized: a stored maximized pane must always have
    // its way back (spec 7, localStorage hazards).
    const max = el("button", maximized === this ? "\u2750" : "\u26F6");
    max.className = "ph-ic ph-max" + (maximized === this ? " pin" : "");
    max.title = maximized === this ? "Restore" : "Maximize";
    max.setAttribute("aria-label", max.title);
    max.onclick = (e) => { e.stopPropagation(); toggleMax(this); };
    const del = el("button", "\u00D7");
    del.className = "ph-ic ph-del";
    del.title = "Close pane (the session keeps running)";
    del.setAttribute("aria-label", "Close pane");
    del.onclick = (e) => { e.stopPropagation(); removeCell(this); };
    side.append(max, del);

    this.bar.replaceChildren(main, side);
    paintDrawerTabs(this);
  }

  // syncNeeds paints the pane's "this agent needs you" state: an amber frame,
  // and an answer strip so the question can be answered without leaving the
  // wall. Driven by the same activity push that moves the tab's dot.
  //
  // The strip is rebuilt only when the state or the session changes, never on
  // every render — it holds a text field, and rebuilding it under a typing
  // operator drops the caret.
  syncNeeds() {
    // Desktop: the ask strip under the pane header (spec 5.3), on every tab.
    if (typeof mobileActive === "function" && !mobileActive(window.innerWidth)) { this.syncStrip(); return; }
    const t = this.activeTab;
    // The tab's activity is fed through needsYou (app.js refreshSessions), so this
    // frames a session with an expired AWS sign-in as well as a waiting one.
    const needs = !!(t && t.kind === "terminal" && typeof t.activity === "function" && t.activity() === "waiting");
    this.el.classList.toggle("needs", needs);
    // The strip exists only while replies are on, and /api/meta answers after the
    // first render, so the flag is part of the key; the amber frame above is not.
    const key = needs ? String(t.id) + (globalThis.repliesEnabled === true ? ":r" : "") : "";
    if (key === this._needsKey) return;
    this._needsKey = key;
    if (this._strip) { this._strip.remove(); this._strip = null; }
    if (needs) {
      // buildReplyStrip returns null for a non-claude session: it can still be
      // seen to be waiting (the frame), it just cannot be answered this way.
      const strip = buildReplyStrip(t);
      if (strip) { this._strip = strip; this.el.appendChild(this._strip); }
    }
  }

  // syncStrip paints the desktop ask strip: `div.needstrip` directly under
  // .cell-bar, for the pane's SESSION (its terminal tab, even while a viewer tab
  // is active, so the strip stays when Files is open). It is rebuilt only when
  // its key moves, never per poll or per tab switch: an expanded strip, a
  // half-typed reply and an SSO form all live in it.
  syncStrip() {
    const term = cellTerminal(this);
    const s = term ? (lastSessions || []).find((x) => x.id === term.id) || null : null;
    const model = s ? needStripModel(s) : null;
    this.el.classList.toggle("needs", !!model);
    // The page-local inputs the body reads beyond the session record (the same
    // list rowLocal feeds rowSig), so a flow moving on or the full text
    // arriving repaints it. The active tab kind is deliberately absent.
    const local = model ? [cardExpanded.has(s.id), cardPeekCache.has(s.id), pendingAnswers.has(s.id),
      pendingSends.has(s.id), ssoLocalSig(s.id)].join("|") : "";
    const key = model ? "d:" + stripKey(s, this.activeTab && this.activeTab.kind, globalThis.repliesEnabled === true) + "|" + local : "";
    if (key === this._needsKey) return;
    this._needsKey = key;
    if (this._strip) { this._strip._dispose?.(); this._strip.remove(); this._strip = null; }
    if (model) {
      this._strip = buildNeedStrip(s, model, () => positionDrawer());
      this.bar.after(this._strip);
    }
    // The drawer hangs below the strip too (positionDrawer), so it moves with it.
    if (typeof positionDrawer === "function") positionDrawer();
  }

  // repaintReply refreshes the answer strip's disabled/.off/title state in
  // place, if this pane currently has one — otherwise a no-op. Driven once
  // per poll from refreshSessions (app.js), because syncNeeds only rebuilds
  // on an activity transition (setActivity is a no-op when the value is
  // unchanged), so a session whose replyable flag is revoked, or that enters
  // a tool call, while its activity stays "waiting" would otherwise never
  // repaint until activity next moves — the strip would keep looking
  // answerable after the real gate had already closed.
  //
  // Never rebuilds the strip, never replaces a DOM node, never touches the
  // field's value: it calls the strip's own paint() (see buildReplyStrip),
  // which only sets disabled/class/title on existing elements. A caret
  // mid-reply is untouched.
  repaintReply() {
    // Desktop: the poll may have changed what the strip shows (the ask, the
    // need, replies on/off); syncStrip rebuilds only when its key moved.
    if (typeof mobileActive === "function" && !mobileActive(window.innerWidth)) { this.syncStrip(); return; }
    this._strip?._paint();
  }

  dispose() {
    if (this._ro) { this._ro.disconnect(); this._ro = null; }
    if (this._strip) this._strip._dispose?.();
    this.tabs.forEach((t) => t.dispose?.());
  }
}

// buildNeedStrip: the desktop ask strip (spec 5.3). The amber label, then
// renderNeedsBody's blocks (app.js): the one-line mono text (click expands it to
// the full message), Yes/No and the composer ONLY when replies are on and the
// session is replyable, and Open terminal. D24: with replies off that is the
// whole strip. Every string in it is agent-controlled and goes in through
// textContent (renderNeedsBody and the label here); nothing is parsed as HTML.
// An SSO or store-locked session shows its own sign-in / unlock body instead of
// the ask text (cardContent decides) and gets the .rich layout.
// onSize runs when the strip's height changes, so the drawer under it follows.
function buildNeedStrip(s, model, onSize) {
  const strip = document.createElement("div");
  strip.className = "needstrip";
  strip.dataset.sid = s.id;
  const c = cardContent(s);
  const rich = !!(c.sso || c.unlock);
  if (rich) strip.classList.add("rich");
  if (cardExpanded.has(s.id)) strip.classList.add("open");
  const label = document.createElement("span");
  label.className = "ns-label";
  label.textContent = stripLabel(model.label);
  strip.append(label, renderNeedsBody(s, { strip: true }));
  let ro = null;
  if (typeof ResizeObserver === "function" && typeof onSize === "function") {
    ro = new ResizeObserver(() => onSize());
    ro.observe(strip);
  }
  strip._dispose = () => { if (ro) { ro.disconnect(); ro = null; } };
  return strip;
}

// buildReplyStrip: the answer strip under a waiting pane. Three quick replies,
// a field and send. It reuses the sidebar's gating functions (peek.js) rather
// than restating them: their order mirrors the server's own guard, and a
// second copy would drift from it. Returns null when the session behind `tab`
// is not a claude session (no /peek, /message or /replyable for it), or is
// not found at all, or when replies are switched off server-side.
function buildReplyStrip(tab) {
  // Replies off (server kill-switch, /api/meta): no strip, so no hidden field.
  if (globalThis.repliesEnabled !== true) return null;
  const rec = lastSessions.find((x) => x.id === tab.id);
  if (!rec || !rec.claude) return null;

  const strip = document.createElement("div");
  strip.className = "replystrip";
  const digitBtns = [];
  for (const d of ["1", "2", "3"]) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn";
    b.textContent = d;
    b.title = "Send " + d;
    digitBtns.push(b);
    strip.appendChild(b);
  }
  const fld = document.createElement("input");
  fld.type = "text";
  fld.className = "fld";
  fld.placeholder = "Reply (one line)";
  const send = document.createElement("button");
  send.type = "button";
  send.className = "btn pri";
  send.textContent = "send";
  strip.append(fld, send);
  // The strip's reason lives in a title nobody reads before clicking; when it
  // is the missing opt-in, offer the fix itself.
  const enable = buildEnableRepliesButton(tab.id, () => {
    // Focus only once the re-gate (refreshSessions, run synchronously inside
    // buildEnableRepliesButton's onclick before this callback fires) has
    // actually opened the field — a session that stopped waiting or entered a
    // tool call in the meantime must not receive a focused-but-disabled input.
    if (!fld.disabled) fld.focus();
  });
  strip.appendChild(enable);

  // knownPending tracks the most recently observed /peek "pending" flag (a
  // tool call in flight). It starts false — the common case — and is
  // refreshed by post() below. It only feeds the PAINTED state; post() always
  // re-fetches /peek right before it actually sends, so a stale value here
  // can make a control look more open than the real gate, never send while
  // the session is genuinely closed.
  let knownPending = false;

  // paint applies sendState's verdict to every control: the real disabled
  // attribute, the .off look, and the reason in the title. The pane has no
  // room for a caption line, so the title carries the reason — a closed
  // control with no reason anywhere is worse than a missing one.
  //
  // sending latches for the duration of one send, and paint folds it in. It has
  // to go through paint rather than set send.disabled directly, because
  // repaintReply() runs paint() from the 3s poll and paint writes the disabled
  // flags UNCONDITIONALLY from the gate: a poll landing mid-send (activity is
  // still "waiting", so the gate is still open) would re-enable the very
  // controls the send had just closed. It closes every control, not only send —
  // the three quick replies are the ones an operator taps fast, and a
  // double-tapped "2" answers two different questions on a numbered prompt.
  // The server does not dedupe.
  let sending = false;
  const paint = () => {
    const s = lastSessions.find((x) => x.id === tab.id);
    const st = sendState(s ? { replyable: s.replyable, activity: s.activity, pending: knownPending || s.need === "ask" } : null);
    // st is the GATE's verdict and is what post() checks; `closed` is what the
    // controls wear. They differ only while a send is in flight.
    const closed = !st.enabled || sending;
    fld.disabled = closed;
    send.disabled = closed;
    fld.classList.toggle("off", closed);
    send.classList.toggle("off", closed);
    send.title = st.reason;
    enable.hidden = !offerEnableReplies(s);
    for (const b of digitBtns) {
      b.disabled = closed;
      b.classList.toggle("off", closed);
      b.title = st.enabled ? "Send " + b.textContent : st.reason;
    }
    return st;
  };

  // post is shared by the composer's Send button and the three quick replies:
  // one send path, one error mapping, one gate — fails closed. It is the
  // re-entrancy wrapper; postOnce below does the work.
  const post = async (text) => {
    // Latched FIRST, before the /peek await below: two taps a few milliseconds
    // apart both reach this line before either has finished awaiting anything,
    // so a flag set any later would not stop the second one.
    if (sending) return;
    sending = true;
    paint();
    try {
      await postOnce(text);
    } finally {
      sending = false;
      paint();
    }
  };

  // postOnce re-fetches /peek for the CURRENT pending flag and re-reads the
  // CURRENT session record from lastSessions rather than trusting anything
  // captured when the strip was built: lastSessions refreshes every 3s, and a
  // value from build time goes stale within one poll. It does not repaint on
  // its way out — post's finally does that once the in-flight flag is clear.
  const postOnce = async (text) => {
    try {
      const res = await fetch("/api/sessions/" + encodeURIComponent(tab.id) + "/peek");
      knownPending = res.status === 200 && !!(await res.json()).pending;
    } catch (_) {
      knownPending = false;
    }
    const st = paint();
    if (!st.enabled) { showToast(st.reason, { error: true }); return; }
    const err = replyTextError(text);
    if (err) {
      showToast(
        err === "empty" ? "Type something first."
          : err === "newline" ? "One line only — newlines are not sent."
          : "That text contains control characters and was not sent.",
        { error: true },
      );
      return;
    }
    // No direct send.disabled here: the in-flight flag above already closed
    // every control through paint(), and a direct set would be undone by the
    // next poll's repaint.
    try {
      const res = await fetch("/api/sessions/" + encodeURIComponent(tab.id) + "/message", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: text }),
      });
      if (res.status === 204) {
        fld.value = "";
        showToast("Sent");
        return;
      }
      // Same mapping as the sidebar composer's sendReply (app.js): distinct
      // statuses carry distinct meanings, and two composers with two
      // paraphrases of the same failure is exactly what this reuse avoids.
      const msg =
        res.status === 403 ? "Replies are not enabled for this session." :
        res.status === 409 ? "Session is not ready for a message right now." :
        res.status === 410 ? "That session is gone." :
        res.status === 400 ? "That text was rejected." :
        "Send failed (" + res.status + ").";
      showToast(msg, { error: true });
    } catch (_) {
      showToast("Send failed: no connection.", { error: true });
    }
  };

  for (const b of digitBtns) { const d = b.textContent; b.onclick = () => post(d); }
  send.onclick = () => post(fld.value);
  // Exposed so Cell.repaintReply() can re-run the same paint from the poll,
  // without rebuilding this strip or reaching into its closure any other way.
  strip._paint = paint;
  paint();
  return strip;
}

function setFocusedCell(cell) {
  if (focusedCell === cell) return;
  setFocusedCellRaw(cell);
  // Only the focus ring changes, so only the ring is touched. renderBar() here
  // rebuilt every bar, including the button under the pointer, and this runs
  // from the cell's mousedown, so mouseup landed on a fresh node and the click
  // never fired: the first press of ⛶ / +sh / × on an unfocused pane was eaten.
  // renderBar keeps its own toggle, so a later rebuild still paints the ring.
  cells.forEach((c) => c.el.classList.toggle("focus", c === focusedCell));
  syncFocusedTree(); // tree follows the focused tab
  renderSteerRail(); // and so does the rail: it describes the focused session
  if (typeof syncSelectedRow === "function") syncSelectedRow();
  scheduleSaveLayout();
}

// focusedTerminal returns the terminal tab active in the focused cell, or null.
function focusedTerminal() {
  const t = focusedTab();
  return t && t.kind === "terminal" ? t : null;
}
// focusedTab is the tab showing in the focused cell, of any kind. focusedTerminal
// narrows it; the file tree needs the unnarrowed one, because a viewer is also
// something to follow.
function focusedTab() {
  return (focusedCell && focusedCell.activeTab) || null;
}
// eachTerminal / eachViewer iterate live tabs across all cells: used by fontPrefs
// to apply a changed default to every open terminal / viewer.
function eachTerminal(cb) {
  for (const c of cells) for (const t of c.tabs) if (t.kind === "terminal") cb(t);
}
function eachViewer(cb) {
  for (const c of cells) for (const t of c.tabs) if (t.kind === "viewer") cb(t);
}
// cellCwd → the working dir a new shell for this cell should start in: the active
// terminal's cwd, else the most recent terminal tab in the cell that resolved one
// (so a cell showing a file viewer still opens the shell where its agent lives),
// else "": the server falls back to default_cwd.
function cellCwd(cell) {
  if (!cell) return "";
  const act = cell.activeTab;
  if (act && act.kind === "terminal" && act.cwd) return act.cwd;
  for (let i = cell.tabs.length - 1; i >= 0; i--) {
    const t = cell.tabs[i];
    if (t.kind === "terminal" && t.cwd) return t.cwd;
  }
  return "";
}

// cellTerminal → the terminal tab a pane is about: the active tab when it is
// one, else the terminal last shown in the cell (a file opened over an agent
// still belongs to that agent), else the newest terminal tab, else null.
function cellTerminal(cell) {
  if (!cell) return null;
  const act = cell.activeTab;
  if (act && act.kind === "terminal") return act;
  if (cell._lastTerm && cell.tabs.includes(cell._lastTerm)) return cell._lastTerm;
  for (let i = cell.tabs.length - 1; i >= 0; i--) if (cell.tabs[i].kind === "terminal") return cell.tabs[i];
  return null;
}

// syncFocusedTree re-roots the file tree to whatever the focused TAB resolves
// to — a terminal's cwd, or the directory holding the file a viewer is showing.
// treeFollowTarget owns that decision and returns null when there is nothing to
// follow, in which case the tree holds its current root. No-op while a
// terminal's cwd is unresolved; the terminal calls this again once it arrives.
//
// It reads focusedTab(), not focusedTerminal(): keying on terminals alone left
// the tree showing project Y while the focused pane displayed a file from X.
function syncFocusedTree() {
  if (typeof treeFollowTarget !== "function" || typeof syncTreeToRoot !== "function") return;
  const target = treeFollowTarget(focusedTab());
  if (target) syncTreeToRoot(target.root, target.key);
  // The desktop drawer describes the focused pane too, so it follows the same
  // change. It only repaints classes and text: this runs from a cell's
  // mousedown, where a rebuilt header would eat the click.
  if (typeof drawerFollowFocus === "function") drawerFollowFocus();
}

function ensureFirstCell() {
  if (cells.length === 0) cells.push(new Cell());
  if (!focusedCell) setFocusedCellRaw(cells[0]);
}

function focusedCellOrCreate() {
  if (!focusedCell) ensureFirstCell();
  return focusedCell;
}

function removeCell(cell) {
  cell.dispose();
  cells = cells.filter((c) => c !== cell);
  if (maximized === cell) maximized = null;
  if (focusedCell === cell) setFocusedCellRaw(null);
  if (cells.length === 0) ensureFirstCell();
  if (!focusedCell) setFocusedCellRaw(cells[cells.length - 1]);
  renderGrid();
  refreshSessions();
  scheduleSaveLayout();
  // Focus moved via setFocusedCellRaw, so nothing has re-rooted the tree — and
  // because the surviving cell is ALREADY focused by the time the user clicks
  // it, setFocusedCell's equality guard would eat the repair click too. This is
  // the call that makes closing a pane leave a correct tree behind.
  syncFocusedTree();
  renderSteerRail();
}

function toggleMax(cell) {
  maximized = maximized === cell ? null : cell;
  renderGrid();
  scheduleSaveLayout();
}

// currentMaximized: the `maximized` cell to actually honour right now. Mobile
// has no un-maximize control (the desktop .cell-bar's ❐ is hidden <=700px —
// see app.css), so a persisted maximized cell must not blank every other
// chip on that viewport; effectiveMaximized (mobileshell.js) is the pure
// decision, this just supplies it the live inputs. `maximized` itself is
// left untouched — desktop still restores it once the viewport widens back
// past the breakpoint. typeof-guarded because tabs.js is a classic script
// loaded before mobileshell.js (see index.html's script order).
function currentMaximized() {
  const isMobile = typeof mobileActive === "function" && mobileActive(window.innerWidth);
  if (typeof effectiveMaximized === "function") return effectiveMaximized(maximized, isMobile);
  return isMobile ? null : maximized;
}

function renderGrid() {
  const grid = document.getElementById("grid");
  const max = currentMaximized();
  grid.className = "cols-" + gridCols() + (max ? " maximized" : "");
  const show = max ? [max] : cells;
  grid.replaceChildren(...show.map((c) => c.el));
  // `show`, not `cells`: with a pane maximized the others are NOT in the
  // document, and the renderBar sweep on the next line is a trap to copy —
  // restoring scroll on a detached element is a silent no-op.
  show.forEach((c) => c.activeTab?.onAttach?.());
  cells.forEach((c) => c.renderBar());
  requestAnimationFrame(fitAll);
  // The desktop drawer hangs below the focused pane's header, which just moved.
  if (typeof positionDrawer === "function") positionDrawer();
}

function fitAll() {
  const max = currentMaximized();
  const visible = max ? [max] : cells;
  visible.forEach((c) => c.activeTab && c.activeTab.fit && c.activeTab.fit());
}

// applyCap sets the max window count (the 1/2/4 buttons). Lowering it below the
// current window count collapses the extra windows: their tabs move into the last
// kept cell (so "1" instantly stacks everything into one window).
function applyCap(n) {
  cap = n;
  // Every .layouts group: the header's (mobile) and the sidebar top's (desktop).
  for (const b of document.querySelectorAll(".layouts button")) {
    b.classList.toggle("active", Number(b.dataset.cols) === n);
  }
  while (cells.length > cap) {
    const extra = cells.pop();
    const keep = cells[cells.length - 1];
    for (const t of extra.tabs) { t.onUpdate = () => keep.renderBar(); keep.tabs.push(t); }
    if (!keep.activeTab && keep.tabs.length) keep.activeTab = keep.tabs[0];
    if (maximized === extra) maximized = null;
    if (focusedCell === extra) setFocusedCellRaw(keep);
    extra.tabs = []; // detach: do NOT dispose (tabs live on in `keep`)
    if (extra._ro) { extra._ro.disconnect(); extra._ro = null; } // the cell itself is gone
  }
  if (!cells.includes(focusedCell)) setFocusedCellRaw(cells[cells.length - 1] || null);
  ensureFirstCell();
  cells.forEach((c) => { c.renderBar(); c.renderBody(); });
  renderGrid();
  refreshSessions();
  scheduleSaveLayout();
  // Collapsing panes re-parks focus via setFocusedCellRaw, and the cell it lands
  // on can show a different tab than the one the tree is following.
  // Safe during restore, where applyPlan calls this before its own
  // setFocusedCell: a fresh TerminalTab has no cwd yet, and syncTreeToRoot
  // returns on !root BEFORE it writes lastSyncedKey, so it cannot poison
  // the follow-guard.
  syncFocusedTree();
  renderSteerRail();
}

// openTerminal auto-splits a new window per session up to `cap`; beyond the cap
// it stacks as a tab in the focused cell. (openFile always tabs into the focused
// cell: files never create a window.)
//
// opts.newPane means "give this session a pane of its own". A session already on
// screen is MOVED into a fresh window, tab and socket intact — never opened a
// second time. Two tabs for one session cannot both live: the server admits one
// subscriber and marks the older one superseded, so the pair would sit there
// evicting each other, and the lookups below (which stop at their first match)
// could only ever reach one of them. moveToOwnPane owns that path.
// openTerminal is the single chokepoint every "open or focus this session"
// caller funnels through (row tap, spawn success, "open in new pane"), so the
// mobile guard lives here — once — covering all three of its return points,
// rather than at each call site. A viewer tab never goes through openTerminal
// at all, so this guard naturally excludes it; openFile (below) carries the
// same mobile switch-to-Terminal-screen call on its own two return points
// instead (U1: a file's chip now lives in the same switcher a terminal's
// does, so opening one also switches screens).
// typeof-guarded because tabs.js is a classic script loaded before the mobile
// globals (mobileActive/setMobileScreen live in mobileshell.js/app.js).
function openTerminal(id, name, opts) {
  opts = opts || {};
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (t) {
      if (opts.newPane) {
        moveToOwnPane(c, t);
        if (typeof mobileActive === "function" && typeof setMobileScreen === "function" && mobileActive(window.innerWidth)) setMobileScreen("terminal");
        return;
      }
      setFocusedCell(c); c.activate(t);
      if (typeof mobileActive === "function" && typeof setMobileScreen === "function" && mobileActive(window.innerWidth)) setMobileScreen("terminal");
      return;
    }
  }
  ensureFirstCell();
  let cell;
  if (opts.newPane) {
    if (cells.length < cap) { cell = new Cell(); cells.push(cell); } // new pane, as asked
    else { cell = focusedCell; showToast("Pane limit reached: opened alongside the current tabs."); }
  } else if (focusedCell && focusedCell.tabs.length === 0) cell = focusedCell; // fill the empty focused cell
  else if (cells.length < cap) { cell = new Cell(); cells.push(cell); } // auto-split a new window
  else cell = focusedCell;                                              // capped → stack as a tab
  setFocusedCell(cell);
  cell.addTab(new TerminalTab(id, name));
  renderGrid();
  refreshSessions();
  if (typeof mobileActive === "function" && typeof setMobileScreen === "function" && mobileActive(window.innerWidth)) setMobileScreen("terminal");
}

// moveToOwnPane moves an already-open terminal tab out of the cell it shares
// into a window of its own. The tab object is carried across untouched, so its
// WebSocket, scrollback and usage all survive the move.
//
// Two cases end without a new window, and neither is a failure worth inventing
// a new noise for: a tab that is alone in its cell already HAS its own pane, and
// at the pane cap there is no window to move into. Both leave the tab where it
// is and focus it, which is the honest outcome; only the capped one says so out
// loud, because that is the one the operator asked for and did not get.
function moveToOwnPane(from, tab) {
  if (from.tabs.length === 1) { setFocusedCell(from); from.activate(tab); return; }
  if (cells.length >= cap) {
    setFocusedCell(from); from.activate(tab);
    showToast("Pane limit reached: the tab stays where it is.");
    return;
  }
  // renderGrid shows only the maximized cell on desktop (`show = max ? [max]
  // : cells`, via currentMaximized() — see its comment for the mobile
  // exception), so a cell created while one is maximized never enters the
  // document — the tab this action just gave "its own pane" would disappear,
  // focused but unrendered. Giving the tab a pane of its own is the point of
  // this action, so drop out of maximized rather than block the request or
  // silently lose the tab; the operator can re-maximize afterwards.
  maximized = null;
  const cell = new Cell();
  cells.push(cell);
  from.detachTab(tab);
  cell.addTab(tab); // re-points tab.onUpdate at its new cell
  setFocusedCell(cell);
  renderGrid();
  refreshSessions();
}

// U1: opening a file switches the mobile shell to the Terminal screen, the
// same way openTerminal already does above (typeof-guarded — tabs.js loads
// before the mobile globals in mobileshell.js/app.js) — covering both
// branches below (already-open and newly-opened), since a file's chip now
// lives in the same #term-chips strip a terminal's does and there is no
// separate "viewer" screen to land on. renderMobileChips is likewise
// typeof-guarded and re-painted here rather than waiting for the next
// session poll, so a newly opened file's chip appears at once.
function openFile(path) {
  // Normalise BEFORE matching, on the same rule ViewerTab's constructor uses.
  // Comparing the raw argument meant "/home/u/./a.md" missed the tab already
  // showing "/home/u/a.md" and opened a second one carrying an identical path —
  // which the snapshot then stored twice and the next boot silently collapsed.
  if (typeof normalizePath === "function") path = normalizePath(path);
  for (const c of cells) {
    // !t.box: a box file at the same path is a different file (see openBoxFile).
    const t = c.tabs.find((t) => t.kind === "viewer" && !t.box && t.path === path);
    if (t) {
      setFocusedCell(c); c.activate(t);
      if (typeof closeDrawerFor === "function") closeDrawerFor(undefined, "files");
      if (typeof mobileActive === "function" && typeof setMobileScreen === "function" && mobileActive(window.innerWidth)) setMobileScreen("terminal");
      if (typeof renderMobileChips === "function") renderMobileChips();
      return;
    }
  }
  const cell = focusedCellOrCreate();
  cell.addTab(new ViewerTab(path));
  renderGrid();
  // Desktop: a file opened from the Files drawer opens in the pane UNDER the
  // drawer, so the drawer gets out of its way.
  if (typeof closeDrawerFor === "function") closeDrawerFor(undefined, "files");
  if (typeof mobileActive === "function" && typeof setMobileScreen === "function" && mobileActive(window.innerWidth)) setMobileScreen("terminal");
  if (typeof renderMobileChips === "function") renderMobileChips();
}

// openBoxFile opens (or focuses) a read-only viewer for a file inside a
// session's box — today the Playwright output dir. terminal-tab.js calls it
// through window.openBoxFile, typeof-guarded. Identical to openFile except for
// identity: a box tab is matched on (session, path), never on the path alone, so
// it cannot be mistaken for a host file at the same path or another box's.
function openBoxFile(sessionId, path) {
  if (typeof normalizePath === "function") path = normalizePath(path);
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "viewer" && t.box === sessionId && t.path === path);
    if (t) {
      setFocusedCell(c); c.activate(t);
      if (typeof mobileActive === "function" && typeof setMobileScreen === "function" && mobileActive(window.innerWidth)) setMobileScreen("terminal");
      if (typeof renderMobileChips === "function") renderMobileChips();
      return;
    }
  }
  const cell = focusedCellOrCreate();
  cell.addTab(new ViewerTab(path, { box: sessionId }));
  renderGrid();
  if (typeof mobileActive === "function" && typeof setMobileScreen === "function" && mobileActive(window.innerWidth)) setMobileScreen("terminal");
  if (typeof renderMobileChips === "function") renderMobileChips();
}

function closeTabEverywhere(tab) {
  for (const c of cells) if (c.tabs.includes(tab)) { c.closeTab(tab); return; }
}

function retitleTerminal(id, name) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (t) t.setTitle(name);
  }
}

// terminalTabFor(id) → the TerminalTab showing a session, or null. Callers
// that must know whether THIS page still holds the session need the tab
// itself, because an evicted tab stays open while its socket is gone.
function terminalTabFor(id) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (t) return t;
  }
  return null;
}

// closeTerminal(id) → close a session's terminal tab wherever it is (used by kill).
function closeTerminal(id) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (t) { c.closeTab(t); return; }
  }
}

// closeViewersFor closes viewer tabs showing a path that no longer exists, or no
// longer means what it did. Without it a deleted file keeps a tab rendering
// stale content whose ⤓ 404s, and a renamed one keeps a tab pointing nowhere.
// Matches the path itself and anything under it, so deleting a folder clears the
// tabs for every file it held.
function closeViewersFor(path) {
  if (!path) return;
  const prefix = path.replace(/\/+$/, "") + "/";
  const hit = (p) => p === path || (p && p.indexOf(prefix) === 0);
  for (const c of cells.slice()) {
    // !t.box: a host delete/rename says nothing about a file inside a box.
    for (const t of c.tabs.slice()) if (t.kind === "viewer" && !t.box && hit(t.path)) c.closeTab(t);
  }
}

// --- restore (see layout.js) -------------------------------------------------

// applyPlan rebuilds the grid from a reconciled snapshot: terminals and the
// files that were open beside them. Session names come from the live list, not
// the snapshot, so a session renamed since the last visit comes back under its
// current name.
function applyPlan(plan, sessions) {
  const nameOf = new Map((sessions || []).map((s) => [s.id, s.name || s.id]));
  cells.forEach((c) => c.dispose());
  cells = [];
  setFocusedCellRaw(null);
  maximized = null;
  for (const pc of plan.cells) {
    const cell = new Cell();
    cells.push(cell);
    for (const t of pc.tabs) {
      // `restored: true` marks a tab nobody clicked for: see ViewerTab.load,
      // which closes it rather than parking a red error on a deleted path.
      // `box` rides along for a box viewer: if its session or file is gone, the
      // first read 404s and the restored tab closes itself like any dead file.
      if (t.path) cell.addTab(new ViewerTab(t.path, { scroll: t.scroll, restored: true, box: t.box }));
      else cell.addTab(new TerminalTab(t.id, nameOf.get(t.id)));
    }
    cell.activate(cell.tabs[pc.active] || cell.tabs[0]);
  }
  // applyCap first: it is what collapses extra panes if a snapshot ever holds
  // more of them than its own cap allows, and the focus below must be picked
  // from the cells that survive that.
  applyCap(plan.cap);
  // applyCap parks focus on the last cell, so clear it first: setFocusedCell
  // early-returns on an unchanged cell, and that would skip the tree sync.
  const target = cells[plan.focused] || cells[0];
  setFocusedCellRaw(null);
  setFocusedCell(target);
  target.activeTab?.onShow?.(); // fit + keyboard focus land on the restored pane
  const max = plan.maximized == null ? null : cells[plan.maximized];
  if (max) toggleMax(max);
}

// restoreLayout replays the boot snapshot against the live session list. Called
// once from boot; endRestore re-arms saving whether or not anything was restored.
// The finally matters: if building the grid throws, `restoring` must still clear,
// or every later layout change is silently never persisted.
function restoreLayout(sessions) {
  try {
    const plan = typeof reconcileLayout === "function" ? reconcileLayout(bootLayout, sessions) : null;
    if (plan) applyPlan(plan, sessions);
  } finally {
    endRestore();
  }
}

// endRestore re-arms saving. `save` is false on the give-up path, where the
// session list could not be fetched: the grid is empty there only because we
// never restored it, and persisting that would delete the user's real layout.
function endRestore(save = true) {
  restoring = false;
  if (!save) { gaveUp = true; return; }
  scheduleSaveLayout(); // fold in whatever boot already built
}
