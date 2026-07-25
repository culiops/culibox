"use strict";

// Layout model: cells are grid slots. setLayout(n) ensures ≥ n slots and sets
// the column count. Opening a session/file adds a *tab* to the focused cell.
// ⛶ maximizes one cell to fill the grid for full-screen reading.
let cells = [];
let focusedCell = null;
let cols = 1;
let maximized = null;

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
      b.append(label, st, x);
      return b;
    });
    const spacer = document.createElement("span");
    spacer.className = "cell-sp";
    const max = document.createElement("button");
    max.className = "cell-max"; max.textContent = maximized === this ? "🗗" : "⛶";
    max.title = "Maximize / restore";
    max.onclick = (e) => { e.stopPropagation(); toggleMax(this); };
    const del = document.createElement("button");
    del.className = "cell-del"; del.textContent = "✕"; del.title = "Remove pane";
    del.onclick = (e) => { e.stopPropagation(); removeCell(this); };
    this.bar.replaceChildren(...tabs, spacer, max, del);
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
// syncFocusedTree re-roots the file tree to the focused session's cwd. No-op if
// the focused tab isn't a terminal or its cwd hasn't been fetched yet (the
// terminal calls this again once its cwd resolves).
function syncFocusedTree() {
  const t = focusedTerminal();
  if (t && typeof syncTreeToSession === "function") syncTreeToSession(t.cwd, t.id);
}

function ensureCells(n) {
  while (cells.length < n) cells.push(new Cell());
  if (!focusedCell) focusedCell = cells[0];
}

function focusedCellOrCreate() {
  if (!focusedCell) ensureCells(1);
  return focusedCell;
}

function removeCell(cell) {
  cell.dispose();
  cells = cells.filter((c) => c !== cell);
  if (maximized === cell) maximized = null;
  if (focusedCell === cell) focusedCell = null;
  if (cells.length === 0) ensureCells(1);
  if (!focusedCell) focusedCell = cells[0];
  renderGrid();
  refreshSessions();
}

function toggleMax(cell) {
  maximized = maximized === cell ? null : cell;
  renderGrid();
}

function renderGrid() {
  const grid = document.getElementById("grid");
  grid.className = "cols-" + cols + (maximized ? " maximized" : "");
  const show = maximized ? [maximized] : cells;
  grid.replaceChildren(...show.map((c) => c.el));
  cells.forEach((c) => c.renderBar());
  requestAnimationFrame(fitAll);
}

function fitAll() {
  const visible = maximized ? [maximized] : cells;
  visible.forEach((c) => c.activeTab && c.activeTab.fit && c.activeTab.fit());
}

function setLayout(n) {
  cols = n;
  ensureCells(n);
  for (const b of document.getElementById("layouts").children) {
    b.classList.toggle("active", Number(b.dataset.cols) === n);
  }
  renderGrid();
}

// openTerminal / openFile add a tab to the focused cell.
function openTerminal(id, name) {
  for (const c of cells) {
    const t = c.tabs.find((t) => t.kind === "terminal" && t.id === id);
    if (t) { setFocusedCell(c); c.activate(t); return; }
  }
  const cell = focusedCellOrCreate();
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
