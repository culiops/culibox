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
    // Which half of the pane the press landed in, the dock or the rest: see
    // focusedShell. Before setFocusedCell, so a focus move already sees it.
    this.el.addEventListener("mousedown", (e) => this._noteDockFocus(e.target), true);
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
    // Companion-shell dock (spec 5.5): shells docked below or right of this
    // pane. `dock` is shelldock.js's pure model ({placement, ids, active,
    // ratio}, null when there is none), dockTabs the live TerminalTab of each
    // docked id. A docked shell is attached HERE and nowhere else: never also in
    // this.tabs, never in another cell (dockShell moves, it never re-attaches).
    this.dock = null;
    this.dockTabs = new Map();
    this._dockHidden = false; // Hide: out of view, tabs and sockets kept (page-local)
    this._dockFocus = false;  // the last press in this pane landed in the dock
    this._dockPrefs = null;   // placement + ratio of the last dock, for the next one
    this._dockEl = null;
    this._dockBarKey = "";
    // Pane view (spec 5.7, 5.8; paneview.js): "terminal", or Activity / Access
    // of the pane's agent drawn in div.pane-view over the agent's half of the
    // body. Page-local: never in the layout snapshot. See setView.
    this.view = "terminal";
    this._viewHost = null;
    this._viewHandle = null; // the renderer's {update, dispose}
    this._viewKey = "";      // kind + session the host was filled for
    // A view covers the agent's terminal but leaves it attached and sized.
    // desktop.css hides it (visibility), which also keeps its textarea from
    // taking focus; should anything under the host still take it (a reconnect,
    // onShow), keys typed into Activity would reach the agent unseen, so it is
    // handed to the host. The dock is not covered.
    this.body.addEventListener("focusin", (e) => {
      const h = this._viewHost;
      if (this.view === "terminal" || !h || h.contains(e.target)) return;
      if (this._dockEl && this._dockEl.contains(e.target)) return;
      if (e.target && typeof e.target.blur === "function") e.target.blur();
      h.focus({ preventScroll: true });
    });
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
    this._dockFocus = false; // showing a pane tab is asking for it, not the dock
    // ...and not a view over it: the row's open, the strip's Open terminal and
    // the Terminal tab all land here. openPaneView sets its view after this.
    const viewWas = this.view;
    this.view = "terminal";
    if (this.activeTab === tab) {
      if (viewWas !== "terminal") this.renderBar();
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
    // A dock hangs off a terminal in its own pane. With the last one closed, its
    // views close too, as when the pane itself is closed; the shells keep running
    // and are reachable from the sidebar (layout.js drops such a dock the same way).
    if (!this.tabs.some((t) => t.kind === "terminal")) this._dropDock();
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
    // Same rule as closeTab: no terminal left, no dock. A caller that wants the
    // dock to follow the tab (moveToOwnPane) moves it first.
    if (!this.tabs.some((t) => t.kind === "terminal")) this._dropDock();
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
    let main;
    if (this.activeTab) {
      main = this.activeTab.el;
    } else {
      main = document.createElement("div");
      main.className = "cell-empty";
      main.textContent = "Click a session or a file to open it here";
    }
    // [main | dock]: the dock follows the pane's own tab in the body, which
    // desktop.css stacks ("below") or sets side by side ("right"). Its splitter
    // rides inside it, so the one rule `.dock { display: none }` in app.css
    // leaves a mobile body exactly what it was: the tab alone.
    const shell = this._shownShell();
    const dockOn = !!shell;
    this.body.classList.toggle("dock-below", dockOn && this.dock.placement === "below");
    this.body.classList.toggle("dock-right", dockOn && this.dock.placement === "right");
    if (dockOn) {
      this.body.style.setProperty("--dock-ratio", String(this.dock.ratio));
      this._renderDock(shell);
      this.body.replaceChildren(main, this._dockEl);
    } else {
      this.body.style.removeProperty("--dock-ratio");
      this.body.replaceChildren(main);
    }
    // replaceChildren took the view host out with the rest: put it back (or
    // drop it, if the pane lost what it described).
    this._syncView();
    // The element just lost and regained its layout box, so anything inside it
    // that scrolls is back at the top: let the tab put its position back.
    this.activeTab?.onAttach?.();
  }

  // --- pane views: Activity / Access (spec 5.7, 5.8) -------------------------
  // The API the view contents build on: setView(kind), toggleView(kind),
  // updateView() (per poll), and paneViewRenderers (paneview.js) for what fills
  // the host. A view always describes cellTerminal(this), the pane's agent,
  // never a shell docked beside it. Desktop only: mobile is always Terminal.

  // setView shows `kind` ("terminal", "activity", "access"). A pane with no
  // terminal tab, or a mobile viewport, has only Terminal.
  setView(kind) {
    const mobile = typeof mobileActive === "function" && mobileActive(window.innerWidth);
    let next = paneViewKind(kind);
    if (mobile || !cellTerminal(this)) next = "terminal";
    if (next === this.view) { this._syncView(); return; }
    const was = this.view;
    this.view = next;
    this.renderBar(); // the header's .on
    this._syncView();
    if (next !== "terminal") {
      // Off the terminal's textarea and onto the view, so keys stop reaching
      // the agent (see the focusin guard in the constructor).
      if (this._viewHost && this === focusedCell) this._viewHost.focus({ preventScroll: true });
    } else if (was !== "terminal" && this === focusedCell && this.activeTab && this.activeTab.kind === "terminal") {
      this.activeTab.onShow?.(); // back on Terminal: the keyboard goes with it
    }
  }

  // toggleView: a header tab's press (paneViewFor owns the rule).
  toggleView(kind) {
    this.setView(paneViewFor({ view: this.view, hasSession: !!cellTerminal(this) }, kind));
  }

  // updateView: the session list was just refreshed (app.js refreshSessions).
  // The pane may now be about another session; the renderer repaints itself.
  updateView() {
    if (this.view === "terminal") return;
    this._syncView();
    const term = cellTerminal(this);
    if (this._viewHandle && typeof this._viewHandle.update === "function" && term) this._viewHandle.update(term.id);
  }

  // _syncView makes the host match this.view: absent on Terminal; otherwise in
  // the body, filled by the kind's renderer for the pane's agent, refilled only
  // when the kind or the session changes (a renderer's half-typed field must
  // survive every other repaint).
  _syncView() {
    const term = cellTerminal(this);
    const mobile = typeof mobileActive === "function" && mobileActive(window.innerWidth);
    if (this.view !== "terminal" && (!term || mobile)) this.view = "terminal";
    this.body.classList.toggle("has-view", this.view !== "terminal"); // hides the tab under it (desktop.css)
    if (typeof paintSteerToggle === "function") paintSteerToggle(); // ◧ follows the focused pane's Access
    if (this.view === "terminal") { this._dropView(); return; }
    if (!this._viewHost) {
      const h = document.createElement("div");
      h.className = "pane-view";
      h.tabIndex = -1;
      h.setAttribute("role", "region");
      this._viewHost = h;
    }
    const host = this._viewHost;
    if (host.parentElement !== this.body) this.body.appendChild(host);
    const key = this.view + "\n" + term.id;
    if (key === this._viewKey) return;
    this._disposeView();
    host.replaceChildren();
    host.dataset.view = this.view;
    host.setAttribute("aria-label", this.view === "access" ? "Access" : "Activity");
    this._viewKey = key;
    const render = paneViewRenderers[this.view];
    let h = null;
    try {
      h = typeof render === "function" ? render(host, term.id) : paintPaneViewPlaceholder(host, this.view, term);
    } catch (err) {
      // A broken renderer must not take the pane (or the poll) down with it.
      console.error("pane view " + this.view + ":", err);
      host.replaceChildren();
      paintPaneViewPlaceholder(host, this.view, term);
    }
    this._viewHandle = typeof h === "function" ? { dispose: h } : h || null;
  }

  _disposeView() {
    const h = this._viewHandle;
    this._viewHandle = null;
    this._viewKey = "";
    if (h && typeof h.dispose === "function") {
      try { h.dispose(); } catch (err) { console.error("pane view dispose:", err); }
    }
  }

  _dropView() {
    this._disposeView();
    if (this._viewHost) { this._viewHost.remove(); this._viewHost = null; }
  }

  // --- companion-shell dock ------------------------------------------------
  // The public dock API (Task 7 builds on it): dockShell, activateShell,
  // undockShell, detachShell, removeShell, setDockPlacement, setDockRatio,
  // hideDock, dockTabFor, focusedShell. Every one keeps the rule that a shell
  // id is attached in exactly one TerminalTab on the page.

  // _shownShell → the docked TerminalTab on screen, or null (no dock, or hidden).
  _shownShell() {
    if (!this.dock || this._dockHidden) return null;
    return this.dockTabs.get(this.dock.active) || null;
  }

  // dockTabFor(id) → the TerminalTab docked here for that session, or null.
  dockTabFor(id) { return this.dockTabs.get(id) || null; }

  // focusedShell → the docked shell the operator last pressed into, while it is
  // on screen; null otherwise, and always on mobile, where the dock is not drawn.
  // focusedTerminal() asks this first, so insert path, paste and the per-session
  // commands reach the shell. The pane's focus ring stays on the cell.
  focusedShell() {
    if (!this._dockFocus) return null;
    if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) return null;
    return this._shownShell();
  }

  _noteDockFocus(target) {
    // The splitter is the dock's edge, not the shell: a drag leaves focus alone.
    if (this._dockSplit && this._dockSplit.contains(target)) return;
    const inDock = !!(this._dockEl && this._shownShell() && this._dockEl.contains(target));
    if (inDock === this._dockFocus) return;
    this._dockFocus = inDock;
    if (this === focusedCell) this._dockFocusMoved();
  }

  // _dockFocusMoved: focusedTerminal() changed without the focused cell
  // changing, so the selected row that follows it is repainted. Not the rail:
  // it stays on the pane's agent (focusedPaneTerminal). Never a header
  // rebuild: this runs from a mousedown (see setFocusedCell).
  _dockFocusMoved() {
    if (typeof syncSelectedRow === "function") syncSelectedRow();
  }

  // _adoptShell files a live tab in the dock, without rendering. The base dock
  // (placement + ratio) is the one already here, else `base`, else the last one
  // this pane had, else newDock's.
  _adoptShell(tab, base) {
    tab.onUpdate = () => this.syncDockBar();
    this.dockTabs.set(tab.id, tab);
    const start = this.dock || base || (this._dockPrefs && Object.assign(newDock(), this._dockPrefs)) || null;
    this.dock = dockAdd(start, tab.id);
  }

  // dockShell(id, name) docks a session in this pane, shows it with the
  // keyboard (activateShell) and returns its TerminalTab. The single way a shell enters a dock: it never builds a second
  // TerminalTab for a session already open. One docked here is only activated;
  // one open in another pane (its tab or its dock) is MOVED, socket intact; one
  // that is a tab of this very pane stays that tab and is activated (it may be
  // the terminal the dock would hang off). The caller decides where (placeShell).
  dockShell(id, name) {
    if (this.dockTabs.has(id)) { this.activateShell(id); return this.dockTabs.get(id); }
    const own = this.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (own) { this.activate(own); return own; }
    const tab = takeTerminalTab(id) || new TerminalTab(id, name);
    this._adoptShell(tab);
    // Shown, focused and saved exactly as a click on its strip tab would.
    this.activateShell(id);
    if (typeof syncSelectedRow === "function") syncSelectedRow();
    return tab;
  }

  // activateShell(id) shows that docked shell (un-hiding the dock) and gives it
  // the keyboard and focusedTerminal(). false when it is not docked here.
  activateShell(id) {
    const tab = this.dockTabs.get(id);
    if (!tab) return false;
    this.dock = dockAdd(this.dock, id);
    this._dockHidden = false;
    this._dockFocus = true;
    this.renderBody();
    tab.onShow?.(); // fit + keyboard
    this._fitDock();
    if (this === focusedCell) this._dockFocusMoved();
    scheduleSaveLayout();
    return true;
  }

  // detachShell(id) takes a shell out of the dock WITHOUT ending its view: the
  // tab comes back with its socket open, for a caller that re-homes it (own
  // pane, undock). null when it is not docked here. The emptied dock goes, and
  // its placement and ratio are kept for this pane's next one.
  detachShell(id) {
    const tab = this.dockTabs.get(id);
    if (!tab) return null;
    this.dockTabs.delete(id);
    tab.onUpdate = null;
    const before = this.dock;
    this.dock = dockRemove(this.dock, id);
    if (!this.dock) {
      this._dockPrefs = { placement: before.placement, ratio: before.ratio };
      this._dockHidden = false;
    }
    const wasFocus = this._dockFocus;
    if (!this._shownShell()) this._dockFocus = false;
    this.renderBody();
    this._fitDock();
    scheduleSaveLayout();
    if (wasFocus && this === focusedCell) this._dockFocusMoved();
    return tab;
  }

  // removeShell(id) closes a docked shell's view (its socket closes; the
  // session keeps running, like closing a tab). Used for a killed or ended shell.
  removeShell(id) {
    const tab = this.detachShell(id);
    if (tab) tab.dispose?.();
  }

  // undockShell(id) turns a docked shell into an ordinary tab of this pane, the
  // active one (the v0.2.0 shape: mobile opens it this way). The same tab
  // object, so nothing re-attaches. Returns it, or null when not docked here.
  undockShell(id) {
    const tab = this.detachShell(id);
    if (tab) this.addTab(tab);
    return tab;
  }

  // setDockPlacement("below" | "right"): anything else is "below" (shelldock.js).
  setDockPlacement(p) {
    if (!this.dock) return;
    this.dock = dockSetPlacement(this.dock, p);
    this._dockHidden = false;
    this.renderBody();
    this._fitDock();
    scheduleSaveLayout();
  }

  // setDockRatio(r): the dock's share of the pane, clamped to what the splitter
  // can draw. The splitter calls it once, when the drag ends.
  setDockRatio(r) {
    if (!this.dock) return;
    this.dock = Object.assign({}, this.dock, { ratio: clampDockRatio(r) });
    this.body.style.setProperty("--dock-ratio", String(this.dock.ratio));
    this._fitDock();
    markLayoutUnsaved();
    scheduleSaveLayout();
  }

  // hideDock takes the dock out of view. The shells keep running AND keep
  // their tabs and sockets here, so the sidebar row (openTerminal) brings the
  // dock back without a second attach. Page-local: a reload shows it again.
  hideDock() {
    if (!this.dock) return;
    this._dockHidden = true;
    const wasFocus = this._dockFocus;
    this._dockFocus = false;
    this.renderBody();
    this._fitDock();
    if (wasFocus && this === focusedCell) this._dockFocusMoved();
    if (typeof syncSelectedRow === "function") syncSelectedRow();
  }

  // _restoreDock rebuilds a reconciled snapshot's dock (layout.js has already
  // dropped ids that are gone or open as tabs). Placement, ratio and the
  // active shell come back exactly as stored.
  _restoreDock(d, nameOf) {
    for (const id of d.ids) this._adoptShell(new TerminalTab(id, nameOf.get(id)));
    this.dock = { placement: d.placement, ids: d.ids.slice(), active: d.active, ratio: d.ratio };
  }

  // _moveDockTo hands this pane's whole dock to another (applyCap folding a
  // pane, a parent moved to a pane of its own). Tabs are carried, never
  // disposed. The receiver keeps its own active shell and placement if it has
  // a dock already.
  _moveDockTo(other) {
    if (!this.dock) return;
    const had = !!other.dock;
    const keepActive = had ? other.dock.active : this.dock.active;
    const base = { placement: this.dock.placement, ids: [], active: null, ratio: this.dock.ratio };
    for (const id of this.dock.ids) {
      const t = this.dockTabs.get(id);
      if (t) other._adoptShell(t, base);
    }
    if (other.dock) other.dock = dockAdd(other.dock, keepActive);
    if (!had) other._dockHidden = this._dockHidden; // a hidden dock stays hidden
    this.dockTabs = new Map();
    this.dock = null;
    this._dockHidden = false;
    this._dockFocus = false;
  }

  // _dropDock closes every docked view (sockets close, sessions keep running).
  _dropDock() {
    if (!this.dock && !this.dockTabs.size) return;
    for (const t of this.dockTabs.values()) t.dispose?.();
    this.dockTabs = new Map();
    this.dock = null;
    this._dockHidden = false;
    this._dockFocus = false;
  }

  // _fitDock refits both halves through the terminals' own fit path, once
  // layout has settled. (Each terminal's ResizeObserver also catches it; this
  // covers a change that leaves the element's box where it was.)
  _fitDock() {
    requestAnimationFrame(() => {
      this.activeTab?.fit?.();
      this._shownShell()?.fit();
    });
  }

  // syncDockBar repaints the shell bar when what it shows moved (labels come
  // from the session list, so a poll can renumber them). Keyed, never per
  // poll: a bar rebuilt between mousedown and click would eat the click.
  syncDockBar() {
    const shell = this._shownShell();
    if (shell && this._dockEl) this._renderDockBar(shell);
  }

  // _renderDock builds the dock node once and refreshes its bar and body.
  _renderDock(shell) {
    if (!this._dockEl) this._buildDock();
    this._renderDockBar(shell);
    if (this._dockBody.firstChild !== shell.el) this._dockBody.replaceChildren(shell.el);
    const right = this.dock.placement === "right";
    this._dockSplit.setAttribute("aria-orientation", right ? "vertical" : "horizontal");
  }

  _buildDock() {
    const dock = document.createElement("div");
    dock.className = "dock";
    const split = document.createElement("div");
    split.className = "dock-split";
    split.setAttribute("role", "separator");
    split.title = "Drag to resize the shell, double-click to reset";
    const bar = document.createElement("div");
    bar.className = "dock-bar";
    const body = document.createElement("div");
    body.className = "dock-body";
    dock.append(split, bar, body);
    this._dockEl = dock;
    this._dockSplit = split;
    this._dockBar = bar;
    this._dockBody = body;
    // The drag writes one CSS variable per move and nothing else: the ratio is
    // committed (saved, terminals refit) once, on release. The body's box is
    // measured once per drag, at the press.
    split.addEventListener("pointerdown", (e) => {
      if (!this.dock || e.button !== 0) return;
      e.preventDefault();
      split.setPointerCapture(e.pointerId);
      split.classList.add("dragging");
      const rect = this.body.getBoundingClientRect();
      const placement = this.dock.placement;
      let r = null;
      const move = (ev) => {
        r = dockRatioFromPointer(placement, rect, ev.clientX, ev.clientY);
        this.body.style.setProperty("--dock-ratio", String(r));
      };
      const up = (ev) => {
        try { split.releasePointerCapture(ev.pointerId); } catch (_) {}
        split.classList.remove("dragging");
        split.removeEventListener("pointermove", move);
        split.removeEventListener("pointerup", up);
        split.removeEventListener("pointercancel", up);
        if (r !== null) this.setDockRatio(r);
      };
      split.addEventListener("pointermove", move);
      split.addEventListener("pointerup", up);
      split.addEventListener("pointercancel", up);
    });
    split.addEventListener("dblclick", () => this.setDockRatio(newDock().ratio));
  }

  // _dockBarModel: what the shell bar shows, as plain data (also its repaint key).
  _dockBarModel(shell) {
    const list = lastSessions || [];
    const find = (id) => list.find((x) => x.id === id) || null;
    const tabs = this.dock.ids.map((id) => {
      const s = find(id);
      const t = this.dockTabs.get(id);
      const st = t && t.status() !== "connected" ? t.status() : "";
      return { id, label: s ? shellLabel(s, list) : (t && t.name) || "shell", on: id === this.dock.active, st };
    });
    const s = find(shell.id);
    const chip = s && typeof deskContainmentChip === "function"
      ? deskContainmentChip(s, typeof boxRuntime === "string" ? boxRuntime : "") : null;
    const cwd = (s && s.cwd) || shell.cwd || "";
    return { tabs, chip, cwd, placement: this.dock.placement, canAdd: !!window.shellProfile };
  }

  // _renderDockBar paints the shell bar (spec 5.5; sl-1.png, v5-3.png): the
  // strip `shell 1 · shell 2 · +`, the containment chip, the cwd, then Below,
  // Right, Own pane and Hide. Every label goes in through textContent.
  _renderDockBar(shell) {
    const m = this._dockBarModel(shell);
    const key = JSON.stringify(m);
    if (key === this._dockBarKey && this._dockBar.firstChild) return;
    this._dockBarKey = key;
    const el = (tag, text) => {
      const n = document.createElement(tag);
      if (text != null) n.textContent = text;
      if (tag === "button") n.type = "button";
      return n;
    };
    const strip = el("div");
    strip.className = "dock-tabs";
    strip.setAttribute("role", "tablist");
    strip.setAttribute("aria-label", "Shells");
    m.tabs.forEach((t, i) => {
      if (i) { const dot = el("span", "·"); dot.className = "dock-dot"; dot.setAttribute("aria-hidden", "true"); strip.append(dot); }
      const b = el("button", t.label);
      b.className = "dock-tab" + (t.on ? " on" : "");
      b.setAttribute("role", "tab");
      b.setAttribute("aria-selected", t.on ? "true" : "false");
      b.title = t.st ? t.label + " · " + t.st : t.label;
      b.onclick = (e) => { e.stopPropagation(); this.activateShell(t.id); };
      strip.append(b);
      if (t.st) { const st = el("span", t.st); st.className = "dock-st"; strip.append(st); }
    });
    if (m.canAdd) {
      const dot = el("span", "·"); dot.className = "dock-dot"; dot.setAttribute("aria-hidden", "true");
      const add = el("button", "+");
      add.className = "dock-add";
      add.title = "+ Shell for the same session, docked here";
      add.setAttribute("aria-label", add.title);
      add.onclick = (e) => {
        e.stopPropagation();
        add.disabled = true;
        Promise.resolve(spawnShellIn(this, this.dock ? this.dock.placement : "below")).finally(() => { add.disabled = false; });
      };
      strip.append(dot, add);
    }
    const kids = [strip];
    if (m.chip) {
      const chip = el("span", m.chip.text);
      chip.className = "ph-chip " + m.chip.tone;
      if (m.chip.title) chip.title = m.chip.title;
      kids.push(chip);
    }
    const cwd = el("span", typeof shortPath === "function" ? shortPath(m.cwd) : m.cwd);
    cwd.className = "dock-cwd mono";
    cwd.title = m.cwd;
    kids.push(cwd);
    const ics = el("span");
    ics.className = "dock-ics";
    const ic = (kind, label, on, run) => {
      const b = el("button");
      b.className = "dock-ic" + (on ? " on" : "");
      b.title = label;
      b.setAttribute("aria-label", label);
      if (on !== null) b.setAttribute("aria-pressed", on ? "true" : "false");
      b.append(dockIcon(kind));
      b.onclick = (e) => { e.stopPropagation(); run(); };
      return b;
    };
    ics.append(
      ic("below", "Below", m.placement === "below", () => this.setDockPlacement("below")),
      ic("right", "Right", m.placement === "right", () => this.setDockPlacement("right")),
      ic("own", "Own pane", null, () => { const t = this._shownShell(); if (t) moveToOwnPane(this, t); }),
      ic("hide", "Hide (the shells keep running)", null, () => this.hideDock()),
    );
    kids.push(ics);
    this._dockBar.replaceChildren(...kids);
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
    // Quick-shell stays reachable from the palette ("+ Shell below") and
    // Ctrl+Shift+`. NOTE: .cell-bar carries an author `display: flex`, so the
    // hidden attribute alone does nothing: see .cell-bar[hidden] in the stylesheet.
    this.bar.hidden = this.tabs.length === 0;
    this.el.classList.toggle("focus", this === focusedCell);
    this.syncNeeds();
  }

  // renderHeader builds the desktop pane header (spec 5.3): one line, fixed
  // slot order, empty slots collapse. Left: dot, name (a switcher when the pane
  // holds several tabs), chips, model, context %. Right: Terminal · Files ·
  // Activity · Access, + Shell, then ⋯ ⛶ × on hover. paneHeaderModel (deskshell.js) owns
  // what shows; this only paints it. Rebuilt on every renderBar like the strip
  // it replaces, so no listener outlives its node.
  renderHeader() {
    if (this.tabs.length === 0) { this.bar.replaceChildren(); return; }
    const t = this.activeTab;
    const term = cellTerminal(this);
    const sid = term ? term.id : null;
    if (!term) this.view = "terminal"; // a view needs the agent it describes (_syncView drops the host)
    const s = sid ? (lastSessions || []).find((x) => x.id === sid) || null : null;
    const m = paneHeaderModel({
      tab: t, session: t && t.kind === "terminal" ? s : null, nowMs: Date.now(),
      boxRuntime: typeof boxRuntime === "string" ? boxRuntime : "", narrow: this._narrow,
    });
    this._hdrName = m.name; // see syncHeaderName
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
      name.title = paneTitle(m.name, t, s, this.tabs.length);
      name.setAttribute("aria-haspopup", "menu");
      name.onclick = (e) => { e.stopPropagation(); openTabSwitcher(name, this); };
    } else {
      name = el("span", m.name);
      name.className = "ph-name";
      // The focused session's cwd: the one status-bar fact with no other home.
      name.title = paneTitle(m.name, t, s, 1);
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
      ctx.onclick = (e) => { e.stopPropagation(); openPaneView(sid, "activity"); };
      main.append(ctx);
    }
    if (m.cost) {
      const cost = el("span", m.cost);
      cost.className = "ph-cost mono";
      cost.title = "Session cost (all conversations since spawn, as Analytics counts it)";
      main.append(cost);
    }

    const side = el("div");
    side.className = "ph-side";
    const tabs = el("div");
    tabs.className = "ph-tabs";
    tabs.setAttribute("role", "group");
    tabs.setAttribute("aria-label", "Pane views");
    // Terminal, Activity and Access are the pane's views (.on, one of the
    // three); Files is the drawer's toggle (.open, paintDrawerTabs).
    if (term) {
      const tt = el("button", "Terminal");
      tt.className = "ph-tab" + (t === term && this.view === "terminal" ? " on" : "");
      tt.onclick = (e) => {
        e.stopPropagation();
        // Terminal means "show me the agent": its tab, and no drawer or view over it.
        if (typeof closeDrawerFor === "function") closeDrawerFor(sid);
        if (this.activeTab !== term) this.activate(term);
        else this.setView("terminal");
      };
      tabs.append(tt);
    }
    const files = el("button", "Files");
    files.className = "ph-tab";
    files.dataset.k = "files";
    files.onclick = (e) => { e.stopPropagation(); headerDrawerPress(this, "files", sid, e); };
    tabs.append(files);
    // A pane of files has no session to describe: no Activity, no Access.
    for (const [kind, label] of term ? [["activity", "Activity"], ["access", "Access"]] : []) {
      const b = el("button", label);
      b.className = "ph-tab" + (this.view === kind ? " on" : "");
      b.dataset.v = kind;
      b.setAttribute("aria-pressed", this.view === kind ? "true" : "false");
      b.onclick = (e) => {
        e.stopPropagation();
        // A keyboard press on an unfocused pane focuses it first (a mouse press
        // already has), so the view and the keyboard land on the same pane.
        if (focusedCell !== this) setFocusedCell(this);
        this.toggleView(kind);
      };
      tabs.append(b);
    }
    const sep = el("span");
    sep.className = "ph-sep";
    side.append(tabs, sep);
    const sh = el("button", "+ Shell");
    sh.className = "ph-shell";
    sh.title = "New shell for this session, same directory and tier, docked below by default (Ctrl+Shift+`)";
    sh.hidden = !window.shellProfile; // no detected shell → no quick shell
    sh.onclick = (e) => {
      e.stopPropagation();
      sh.disabled = true;
      // Joins an open dock where it is: the button never flips Right to Below.
      Promise.resolve(spawnShellIn(this, this.dock ? this.dock.placement : "below")).finally(() => { sh.disabled = false; });
    };
    side.append(sh);
    // A narrow header keeps ONE control, ⋯, always in the flow: Maximize and
    // Close pane move into its menu, because a hover overlay would sit on top of
    // the Access tab and + Shell. ⛶ stays in the flow only while pinned, so the
    // way back from a maximized pane is never hidden.
    const narrow = this._narrow;
    const pinned = maximized === this;
    let more = null;
    if (s || narrow) {
      // The same menu as the sidebar row's ⋯, End session included (spec 7).
      more = el("button", "\u22EF");
      more.className = "ph-ic ph-more";
      more.title = narrow ? "Pane and session actions" : "Session actions";
      more.setAttribute("aria-label", more.title);
      more.setAttribute("aria-haspopup", "menu");
      const paneItems = narrow ? [
        { label: pinned ? "Restore pane" : "Maximize pane", run: () => toggleMax(this) },
        { label: "Close pane (the session keeps running)", run: () => removeCell(this) },
      ] : null;
      more.onclick = (e) => { e.stopPropagation(); toggleSessionMenuAt(more, s, paneItems); };
    }
    // ⛶ stays visible while maximized: a stored maximized pane must always have
    // its way back (spec 7, localStorage hazards).
    const max = el("button", pinned ? "\u2750" : "\u26F6");
    max.className = "ph-ic ph-max" + (pinned ? " pin" : "");
    max.title = pinned ? "Restore" : "Maximize";
    max.setAttribute("aria-label", max.title);
    max.onclick = (e) => { e.stopPropagation(); toggleMax(this); };
    const del = el("button", "\u00D7");
    del.className = "ph-ic ph-del";
    del.title = "Close pane (the session keeps running)";
    del.setAttribute("aria-label", "Close pane");
    del.onclick = (e) => { e.stopPropagation(); removeCell(this); };
    // One wrapper for the icons: a wide header lays them out in the flow
    // (display: contents), hover-revealed; a narrow one shows ⋯ alone (D9).
    const ics = el("span");
    ics.className = "ph-ics" + (narrow ? " nar" : "");
    ics.append(...(narrow ? (pinned ? [more, max] : [more]) : more ? [more, max, del] : [max, del]));
    side.append(ics);

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
    if (this._strip) { this._strip._dispose?.(); this._strip.remove(); this._strip = null; }
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
    // A half-typed reply outranks a repaint: while the reply FIELD has focus, a
    // poll that moved only something incidental (summary, AWS connections, SSO
    // state) leaves the strip alone, as the old card's holdList did. Focus on any
    // other control (the expand text, Enable replies) holds nothing: those clicks
    // change what the strip must show. The key is NOT stored, so the repaint lands
    // on the first sync after focus leaves. A new need or ask, or a change to the
    // reply gate (stripHoldKey), still replaces it at once.
    const repOn = globalThis.repliesEnabled === true;
    const holdSig = model ? stripHoldKey(s, repOn) : "";
    const ae = document.activeElement;
    if (this._strip && model && holdSig === this._holdSig && ae && ae.matches && ae.matches(".fld") && this._strip.contains(ae)) return;
    this._needsKey = key;
    this._holdSig = holdSig;
    this.dropStrip();
    if (model) {
      this._strip = buildNeedStrip(s, model, () => positionDrawer());
      this.bar.after(this._strip);
    }
    // The drawer hangs below the strip too (positionDrawer), so it moves with it.
    if (typeof positionDrawer === "function") positionDrawer();
  }

  // dropStrip removes the pane's strip. A focused reply field removed from the
  // page may or may not fire its blur, and replyFocused (app.js) holds the
  // session list's rebuild, so it is cleared here rather than left to the event.
  dropStrip() {
    if (!this._strip) return;
    if (this._strip.contains(document.activeElement) && typeof replyFocused !== "undefined") replyFocused = false;
    this._strip._dispose?.();
    this._strip.remove();
    this._strip = null;
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
    if (typeof mobileActive === "function" && !mobileActive(window.innerWidth)) { this.syncHeaderName(); this.syncStrip(); this.syncDockBar(); return; }
    this._strip?._paint();
  }

  // syncHeaderName: the desktop header paints the session's name from the
  // polled list, so a rename (this tab's PATCH resolves before the next poll)
  // must repaint it. Rebuilds only when the name moved: a header rebuilt on
  // every poll would eat the press that lands during it (see setFocusedCell).
  syncHeaderName() {
    const t = this.activeTab;
    if (!t || t.kind !== "terminal") return;
    const s = (lastSessions || []).find((x) => x.id === t.id) || null;
    const name = (s && (s.name || s.id)) || t.name || "";
    if (name !== this._hdrName) this.renderBar();
  }

  dispose() {
    if (this._ro) { this._ro.disconnect(); this._ro = null; }
    this.dropStrip();
    this._dropView();
    this.tabs.forEach((t) => t.dispose?.());
    this._dropDock(); // the docked views go with the pane; the shells keep running
  }
}

// dockIcon: the shell bar's placement glyphs as inline SVG (sl-1.png), built
// node by node. Below and Right draw the pane with the dock's half filled; Own
// pane two overlapping windows; Hide a cross.
function dockIcon(kind) {
  const NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 14 14");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.2");
  const add = (tag, attrs) => {
    const n = document.createElementNS(NS, tag);
    for (const k in attrs) n.setAttribute(k, attrs[k]);
    svg.append(n);
  };
  if (kind === "below" || kind === "right") {
    add("rect", { x: "1.5", y: "1.5", width: "11", height: "11", rx: "1.5" });
    add("rect", kind === "below"
      ? { x: "1.5", y: "8", width: "11", height: "4.5", fill: "currentColor", stroke: "none" }
      : { x: "7", y: "1.5", width: "5.5", height: "11", fill: "currentColor", stroke: "none" });
  } else if (kind === "own") {
    add("rect", { x: "1.5", y: "4.5", width: "8", height: "8", rx: "1.2" });
    add("path", { d: "M4.5 4.5V2.7c0-.7.5-1.2 1.2-1.2h5.6c.7 0 1.2.5 1.2 1.2v5.6c0 .7-.5 1.2-1.2 1.2H9.5" });
  } else {
    add("path", { d: "M3.5 3.5l7 7M10.5 3.5l-7 7" });
  }
  return svg;
}

// takeTerminalTab(id) takes an open session's TerminalTab out of wherever it
// is (a pane's tabs or a dock) WITHOUT disposing it, for a caller that re-homes
// it: a session admits one socket, so moving is the only safe way to show it
// somewhere else. null when it is not open on this page.
function takeTerminalTab(id) {
  for (const c of cells) {
    if (c.dockTabs.has(id)) return c.detachShell(id);
    const t = c.tabs.find((x) => x.kind === "terminal" && x.id === id);
    if (t) { c.detachTab(t); return t; }
  }
  return null;
}

// dockHolding(id) → the cell whose dock holds that session, or null.
function dockHolding(id) {
  return cells.find((c) => c.dockTabs.has(id)) || null;
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
  if (typeof paintSteerToggle === "function") paintSteerToggle(); // ◧: the new pane's Access view or not
  if (typeof syncSelectedRow === "function") syncSelectedRow();
  scheduleSaveLayout();
}

// focusedTerminal returns the terminal receiving the operator's keys: a docked
// shell they pressed into (Cell.focusedShell), else the terminal tab active in
// the focused cell, else null. Insert path, paste, zoom and the selected row
// follow it, so they reach the shell.
function focusedTerminal() {
  const sh = focusedCell ? focusedCell.focusedShell() : null;
  return sh || focusedPaneTerminal();
}
// focusedPaneTerminal: the terminal tab active in the focused cell, ignoring
// its dock: the pane's AGENT. The steer rail and the pane views describe this
// one, because the pane header (cellTerminal) names it: a grant or a reply must
// go to the session the pane is titled with, never to a shell docked beside it.
function focusedPaneTerminal() {
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
// to apply a changed default to every open terminal / viewer. Docked shells are
// terminals too (font, activity, usage, "in a pane"): each cell's tabs, then its dock.
function eachTerminal(cb) {
  for (const c of cells) {
    for (const t of c.tabs) if (t.kind === "terminal") cb(t);
    for (const t of c.dockTabs.values()) cb(t);
  }
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

// openPaneView shows the Activity or Access view (`kind`) of session `sid` in
// the pane that holds it, opening its terminal first when no pane does; no
// `sid` means the focused pane's agent. A docked shell's id lands on the agent
// its dock hangs off (paneViewTarget): a view never describes a shell docked
// beside it. Opens, never toggles (the header tab toggles: Cell.toggleView).
// The palette, the header's context %, the ⋯ menu's Context by tool and the
// sidebar's "SSO expired · Sign in" use it. false on mobile, which has no views.
function openPaneView(sid, kind) {
  if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) return false;
  closePage();
  const model = () => ({ cells: cells.map((c) => ({ term: (cellTerminal(c) || {}).id || null, tabs: c.tabs, dock: c.dock })) });
  let at = paneViewTarget(model(), sid, cells.indexOf(focusedCell));
  if (!at && sid != null) {
    const s = (lastSessions || []).find((x) => x.id === sid);
    openTerminal(sid, (s && (s.name || s.id)) || sid);
    at = paneViewTarget(model(), sid, cells.indexOf(focusedCell));
  }
  if (!at) return false;
  const cell = cells[at.cellIndex];
  const t = cell.tabs.find((x) => x.kind === "terminal" && x.id === at.sid);
  setFocusedCell(cell);
  // Another of the pane's terminals in front: bring this one up first (activate
  // returns the pane to Terminal, so the view is set after it).
  if (t && cellTerminal(cell) !== t) cell.activate(t);
  cell.setView(kind);
  return true;
}

// paintPaneViewPlaceholder fills a view whose kind has no renderer yet
// (paneViewRenderers): its name and the session it describes.
function paintPaneViewPlaceholder(host, kind, term) {
  const s = (lastSessions || []).find((x) => x.id === term.id);
  const wrap = document.createElement("div");
  wrap.className = "pv-ph";
  const h = document.createElement("h2");
  h.className = "pv-ph-t";
  h.textContent = kind === "access" ? "Access" : "Activity";
  const n = document.createElement("div");
  n.className = "pv-ph-s";
  n.textContent = (s && (s.name || s.id)) || term.title();
  wrap.append(h, n);
  host.append(wrap);
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
  // The Files drawer holding a dirty buffer for another session (it asks, or
  // the user kept it) keeps the tree there too, so tree, header and viewer
  // name one session. A Discard runs this again, unheld.
  const held = typeof drawerKeepsTree === "function" && drawerKeepsTree();
  if (target && !held) syncTreeToRoot(target.root, target.key);
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
  closePage();
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
  // Desktop with no tab anywhere: the empty state stands in for the panes.
  const empty = typeof desktopEmptyState === "function" ? desktopEmptyState() : null;
  if (empty) grid.classList.add("empty");
  const show = empty ? [] : max ? [max] : cells;
  grid.replaceChildren(...(empty ? [empty] : show.map((c) => c.el)));
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
  closePage(); // the layout the user just chose must be on screen
  cap = n;
  // Every .layouts group: the header's (mobile) and the sidebar top's (desktop).
  for (const b of document.querySelectorAll(".layouts button")) {
    b.classList.toggle("active", Number(b.dataset.cols) === n);
  }
  while (cells.length > cap) {
    const extra = cells.pop();
    const keep = cells[cells.length - 1];
    for (const t of extra.tabs) { t.onUpdate = () => keep.renderBar(); keep.tabs.push(t); }
    extra._moveDockTo(keep); // docked shells fold in with their pane, sockets intact
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
  closePage(); // a page hides the panes: showing a session closes it first
  opts = opts || {};
  // One terminal, one place: a docked shell is already attached, in its dock.
  // placeShell finds where (a dock or a tab: those never share an id).
  const at = placeShell({ cells: cells.map((c) => ({ tabs: c.tabs, dock: c.dock })) }, id, null);
  const dc = at.action === "activate" ? cells[at.cellIndex] : null;
  if (dc && dc.dockTabs.has(id)) {
    if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) {
      // Mobile draws no dock: the shell becomes an ordinary tab of its pane
      // (v0.2.0), and the loop below focuses it like any other. A new pane tab
      // means a new chip: repaint now, as the new-tab path below does, not on
      // the next poll.
      dc.undockShell(id);
      refreshSessions();
    } else if (opts.newPane) {
      moveToOwnPane(dc, dc.dockTabFor(id));
      return;
    } else {
      setFocusedCell(dc);
      dc.activateShell(id);
      return;
    }
  }
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

// dockShellBesideParent shows shell session `s` ({id, name, parent_id}) beside
// its parent: docked in the pane that holds the parent, with `placement`
// ("below"/"right") when given, else that pane's own. A shell already open
// anywhere is only activated where it is (openTerminal), never attached twice.
// false, touching nothing, when no pane holds the parent: the caller decides.
function dockShellBesideParent(s, placement) {
  closePage(); // the shell docks beside a pane a page would hide
  const at = placeShell({ cells: cells.map((c) => ({ tabs: c.tabs, dock: c.dock })) }, s.id, s.parent_id || null);
  if (at.action === "own") return false;
  if (at.action === "activate") { openTerminal(s.id, s.name); return true; }
  const c = cells[at.cellIndex];
  setFocusedCell(c);
  c.dockShell(s.id, s.name);
  if (placement) c.setDockPlacement(placement);
  return true;
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
  // A docked shell (the shell bar's Own pane) takes the same path; only the
  // way out of `from` differs. The dock is not a tab, so "alone in its cell"
  // does not apply to it.
  const docked = from.dockTabFor(tab.id) === tab;
  const stay = () => { setFocusedCell(from); if (docked) from.activateShell(tab.id); else from.activate(tab); };
  if (!docked && from.tabs.length === 1) { stay(); return; }
  if (cells.length >= cap) {
    stay();
    showToast(docked ? "Pane limit reached: the shell stays docked." : "Pane limit reached: the tab stays where it is.");
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
  if (docked) {
    from.detachShell(tab.id); // out of the dock, socket open
  } else {
    // The dock hangs off a terminal of its pane: if this tab is the last one
    // there, the dock moves with it instead of closing.
    if (from.dock && !from.tabs.some((t) => t !== tab && t.kind === "terminal")) from._moveDockTo(cell);
    from.detachTab(tab);
  }
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
  // The desktop Files drawer's viewer is in no cell (filesdrawer.js).
  if (tab && typeof drawerViewerTab === "function" && drawerViewerTab() === tab) { drawerClearViewer(); return; }
  for (const c of cells) {
    if (c.tabs.includes(tab)) { c.closeTab(tab); return; }
    if (tab && c.dockTabFor(tab.id) === tab) { c.removeShell(tab.id); return; }
  }
}

function retitleTerminal(id, name) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id) || c.dockTabFor(id);
    if (t) t.setTitle(name);
  }
}

// terminalTabFor(id) → the TerminalTab showing a session, or null. Callers
// that must know whether THIS page still holds the session need the tab
// itself, because an evicted tab stays open while its socket is gone.
function terminalTabFor(id) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id) || c.dockTabFor(id);
    if (t) return t;
  }
  return null;
}

// closeTerminal(id) → close a session's terminal tab wherever it is (used by kill).
function closeTerminal(id) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (t) { c.closeTab(t); return; }
    if (c.dockTabFor(id)) { c.removeShell(id); return; }
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
  // The Files drawer's viewer too, on the same rule.
  const dv = typeof drawerViewerTab === "function" ? drawerViewerTab() : null;
  if (dv && !dv.box && hit(dv.path)) drawerClearViewer();
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
    // A stored dock id becomes a TerminalTab in the dock, never a pane tab:
    // reconcileLayout already removed every id that is gone or open as a tab.
    if (pc.dock) cell._restoreDock(pc.dock, nameOf);
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
