"use strict";

// Layout model: cells are grid windows. Opening a session auto-splits a new
// window (grow 1→2→3→4, capped at `cap`); beyond the cap it stacks as a tab.
// Opening a file always tabs into the focused cell. ⛶ maximizes one cell.
let cells = [];
let focusedCell = null;
let cap = 4; // max windows before opens stack as tabs (the 1/2/4 buttons set it)
let maximized = null;

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
    this.el.addEventListener("mousedown", () => setFocusedCell(this), true);
    this.renderBar();
    this.renderBody();
  }

  addTab(tab) {
    tab.onUpdate = () => this.renderBar();
    this.tabs.push(tab);
    this.activate(tab);
  }

  activate(tab) {
    if (this.activeTab === tab) {
      this.renderBody(); tab.onShow?.();
    } else {
      if (this.activeTab) this.activeTab.onHide?.();
      this.activeTab = tab;
      this.renderBar();
      this.renderBody();
      tab.onShow?.();
    }
    if (this === focusedCell) syncFocusedTree(); // tree follows the focused session
  }

  closeTab(tab) {
    const i = this.tabs.indexOf(tab);
    if (i < 0) return;
    this.tabs.splice(i, 1);
    tab.dispose?.();
    if (this.tabs.length === 0 && cells.length > 1) { removeCell(this); return; } // reflow: drop the emptied window
    if (this.activeTab === tab) {
      this.activeTab = this.tabs[Math.min(i, this.tabs.length - 1)] || null;
      if (this.activeTab) this.activeTab.onShow?.();
    }
    this.renderBar();
    this.renderBody();
    refreshSessions();
  }

  renderBody() {
    if (this.activeTab) this.body.replaceChildren(this.activeTab.el);
    else {
      const ph = document.createElement("div");
      ph.className = "cell-empty";
      ph.textContent = "Click a session or a file to open it here";
      this.body.replaceChildren(ph);
    }
  }

  renderBar() {
    const tabs = this.tabs.map((t) => {
      const b = document.createElement("div");
      b.className = "tab" + (t === this.activeTab ? " act" : "");
      const label = document.createElement("span");
      label.className = "tab-label";
      label.textContent = t.title();
      label.title = t.title();
      b.onclick = () => this.activate(t);
      const st = document.createElement("span");
      st.className = "tab-st";
      st.textContent = t.status();
      const x = document.createElement("button");
      x.className = "tab-x"; x.textContent = "×"; x.title = "Close tab";
      x.onclick = (e) => { e.stopPropagation(); this.closeTab(t); };
      b.append(label, st);
      // Kill lives on the TAB, not the cell: a cell can hold several session tabs
      // and killing is per-session. ⏻ ends the session, × only closes the view.
      if (t.kind === "terminal") {
        const kill = document.createElement("button");
        kill.className = "tab-kill"; kill.textContent = "⏻";
        kill.title = "Kill session (ends the agent)";
        kill.onclick = (e) => {
          e.stopPropagation();
          if (confirm('Kill session "' + t.title() + '"? This cannot be undone.')) killSession(t.id);
        };
        b.append(kill);
      }
      b.append(x);
      return b;
    });
    const spacer = document.createElement("span");
    spacer.className = "cell-sp";
    const sh = document.createElement("button");
    sh.className = "cell-sh"; sh.textContent = "+sh";
    sh.title = "New shell here (Ctrl+Shift+`)";
    sh.hidden = !window.shellProfile; // no detected shell → no quick shell
    // Disable while in flight so the ignored second click is visible, not silent.
    sh.onclick = (e) => {
      e.stopPropagation();
      sh.disabled = true;
      Promise.resolve(spawnShellIn(this)).finally(() => { sh.disabled = false; });
    };
    const max = document.createElement("button");
    max.className = "cell-max"; max.textContent = maximized === this ? "🗗" : "⛶";
    max.title = "Maximize / restore";
    max.onclick = (e) => { e.stopPropagation(); toggleMax(this); };
    const del = document.createElement("button");
    del.className = "cell-del"; del.textContent = "✕"; del.title = "Remove pane";
    del.onclick = (e) => { e.stopPropagation(); removeCell(this); };
    // Controls live in one sticky cluster so they stay reachable when the tab
    // strip overflows (a capped cell stacks many tabs — see openTerminal).
    const ctl = document.createElement("span");
    ctl.className = "cell-ctl";
    ctl.append(sh, max, del);
    this.bar.replaceChildren(...tabs, spacer, ctl);
    this.el.classList.toggle("focused", this === focusedCell);
  }

  dispose() { this.tabs.forEach((t) => t.dispose?.()); }
}

function setFocusedCell(cell) {
  if (focusedCell === cell) return;
  focusedCell = cell;
  cells.forEach((c) => c.renderBar());
  syncFocusedTree(); // tree follows the focused session
}

// focusedTerminal returns the terminal tab active in the focused cell, or null.
function focusedTerminal() {
  const t = focusedCell && focusedCell.activeTab;
  return t && t.kind === "terminal" ? t : null;
}
// eachTerminal / eachViewer iterate live tabs across all cells — used by fontPrefs
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
// else "" — the server falls back to default_cwd.
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

// syncFocusedTree re-roots the file tree to the focused session's cwd. No-op if
// the focused tab isn't a terminal or its cwd hasn't been fetched yet (the
// terminal calls this again once its cwd resolves).
function syncFocusedTree() {
  const t = focusedTerminal();
  if (t && typeof syncTreeToSession === "function") syncTreeToSession(t.cwd, t.id);
}

function ensureFirstCell() {
  if (cells.length === 0) cells.push(new Cell());
  if (!focusedCell) focusedCell = cells[0];
}

function focusedCellOrCreate() {
  if (!focusedCell) ensureFirstCell();
  return focusedCell;
}

function removeCell(cell) {
  cell.dispose();
  cells = cells.filter((c) => c !== cell);
  if (maximized === cell) maximized = null;
  if (focusedCell === cell) focusedCell = null;
  if (cells.length === 0) ensureFirstCell();
  if (!focusedCell) focusedCell = cells[cells.length - 1];
  renderGrid();
  refreshSessions();
}

function toggleMax(cell) {
  maximized = maximized === cell ? null : cell;
  renderGrid();
}

function renderGrid() {
  const grid = document.getElementById("grid");
  grid.className = "cols-" + gridCols() + (maximized ? " maximized" : "");
  const show = maximized ? [maximized] : cells;
  grid.replaceChildren(...show.map((c) => c.el));
  cells.forEach((c) => c.renderBar());
  requestAnimationFrame(fitAll);
}

function fitAll() {
  const visible = maximized ? [maximized] : cells;
  visible.forEach((c) => c.activeTab && c.activeTab.fit && c.activeTab.fit());
}

// applyCap sets the max window count (the 1/2/4 buttons). Lowering it below the
// current window count collapses the extra windows: their tabs move into the last
// kept cell (so "1" instantly stacks everything into one window).
function applyCap(n) {
  cap = n;
  for (const b of document.getElementById("layouts").children) {
    b.classList.toggle("active", Number(b.dataset.cols) === n);
  }
  while (cells.length > cap) {
    const extra = cells.pop();
    const keep = cells[cells.length - 1];
    for (const t of extra.tabs) { t.onUpdate = () => keep.renderBar(); keep.tabs.push(t); }
    if (!keep.activeTab && keep.tabs.length) keep.activeTab = keep.tabs[0];
    if (maximized === extra) maximized = null;
    if (focusedCell === extra) focusedCell = keep;
    extra.tabs = []; // detach — do NOT dispose (tabs live on in `keep`)
  }
  if (!cells.includes(focusedCell)) focusedCell = cells[cells.length - 1] || null;
  ensureFirstCell();
  cells.forEach((c) => { c.renderBar(); c.renderBody(); });
  renderGrid();
  refreshSessions();
}

// openTerminal auto-splits a new window per session up to `cap`; beyond the cap
// it stacks as a tab in the focused cell. (openFile always tabs into the focused
// cell — files never create a window.)
function openTerminal(id, name) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (t) { setFocusedCell(c); c.activate(t); return; }
  }
  ensureFirstCell();
  let cell;
  if (focusedCell && focusedCell.tabs.length === 0) cell = focusedCell; // fill the empty focused cell
  else if (cells.length < cap) { cell = new Cell(); cells.push(cell); } // auto-split a new window
  else cell = focusedCell;                                              // capped → stack as a tab
  setFocusedCell(cell);
  cell.addTab(new TerminalTab(id, name));
  renderGrid();
  refreshSessions();
}

function openFile(path) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "viewer" && t.path === path);
    if (t) { setFocusedCell(c); c.activate(t); return; }
  }
  const cell = focusedCellOrCreate();
  cell.addTab(new ViewerTab(path));
  renderGrid();
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

// terminalOpen(id) → is a session currently open in any cell (for the sidebar)?
function terminalOpen(id) {
  return cells.some((c) => c.tabs.some((t) => t.kind === "terminal" && t.id === id));
}

// closeTerminal(id) → close a session's terminal tab wherever it is (used by kill).
function closeTerminal(id) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (t) { c.closeTab(t); return; }
  }
}
