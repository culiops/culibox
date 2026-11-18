"use strict";
// insertpage.js: the page the insert tests run on (#337). Not a test itself (the runner takes *.test.js only).
//
// The REAL tabs.js runs here: Cell with its tabs, dock, views and renderBody, the cells array, renderGrid, toggleMax,
// currentMaximized, focusedTerminal, terminalTabFor. So do the real pagehost.js (currentPage), mobileshell.js (the phone
// breakpoint), shelldock.js, paneview.js, editorref.js (insertGate), insertvisible.js and insertdecision.js. P1's hand-made
// fakes of the layout hid two of the bugs, so the layout is never faked here: a pane's tabs, its active tab, its view,
// its docked shells and which pane is maximized are what the real Cell makes of the calls a test makes.
//
// What is stood in, and why it cannot matter to "is this terminal on screen": the DOM (fakedom.js plus a document tree,
// so an element is connected exactly when it hangs under the body), TerminalTab and ViewerTab (the real ones need xterm
// and a socket; Cell only needs their kind, id, element and lifecycle), the pane header and the dock's bar paint
// (renderBar, _renderDock: markup, no state) and the tree and rail followers (syncFocusedTree, renderSteerRail).
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// A fake element that knows where it hangs: connected when its chain of parents ends at the document body, and given a
// box (`rect`) only then. A test collapses a pane by setting rect on an element.
function makeNodeClass(doc) {
  return class DNode extends Node {
    constructor(tag) {
      super(tag, doc);
      Object.defineProperty(this, "isConnected", { get: () => { for (let n = this; n; n = n.parent) if (n === doc.body) return true; return false; } });
      this.rect = { width: 800, height: 600 };
      this.style = { setProperty() {}, removeProperty() {} };
    }
    addEventListener() {}
    // A box only while connected, and none inside an ancestor that has collapsed to nothing (a pane squeezed to zero).
    getBoundingClientRect() {
      if (!this.isConnected) return { width: 0, height: 0 };
      for (let n = this; n; n = n.parent) if (n.rect && (n.rect.width === 0 || n.rect.height === 0)) return { width: 0, height: 0 };
      return this.rect;
    }
  };
}

function page(over) {
  const doc = { hidden: false, activeElement: null };
  const DNode = makeNodeClass(doc);
  doc.body = new DNode("body");
  doc.createElement = (t) => new DNode(t);
  const els = {};
  const mount = (id, parent) => { const n = new DNode("div"); n.id = id; els[id] = n; (parent || doc.body).appendChild(n); return n; };
  const grid = mount("grid");
  const drawer = mount("drawer");
  drawer.hidden = true; // the Files dock starts closed
  doc.getElementById = (id) => els[id] || null;
  doc.addEventListener = () => {};

  const st = { toasts: [], sent: {}, focused: [], refreshes: 0 };
  const ctx = vm.createContext(Object.assign({
    console, Array, Object, Set, Map, Math, Number, JSON, String, Error, Date, RegExp, Promise,
    document: doc,
    window: { innerWidth: 1280, addEventListener() {}, shellProfile: "bash" },
    requestAnimationFrame: () => 0,
    renderSteerRail() {},
    showToast: (m, opt) => { st.toasts.push([m, !!(opt && opt.error)]); },
    refreshSessions: () => { st.refreshes++; },
    lastSessions: [],
  }, over));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);

  // The stand-in tabs: the surface Cell and the insert glue read.
  load(`
    class TerminalTab {
      constructor(id, name) {
        this.kind = "terminal"; this.id = id; this.name = name || id; this.el = document.createElement("div");
        this.el.className = "tab-body"; this.cwd = "/work/" + id; this.disposed = false;
        this.term = { focus: () => __focus(this.id) };
      }
      title() { return this.name; }
      setTitle(n) { this.name = n; }
      status() { return "connected"; }
      sendText(t) { return __send(this.id, t); }
      grabFocus() { __focus(this.id); }
      onShow() {} onHide() {} onAttach() {} fit() {} dispose() { this.disposed = true; }
    }
    class ViewerTab {
      constructor(p) { this.kind = "viewer"; this.path = p; this.name = p.split("/").pop(); this.el = document.createElement("div"); this.el.className = "tab-body"; }
      title() { return this.name; }
      onShow() {} onHide() {} onAttach() {} dispose() {}
    }
  `, "tabs-standins");
  ctx.__send = (id, t) => { (st.sent[id] = st.sent[id] || []).push(t); return true; };
  ctx.__focus = (id) => { st.focused.push(id); };

  for (const f of ["mobileshell.js", "shelldock.js", "paneview.js", "pagehost.js", "tabs.js", "editorref.js", "insertvisible.js", "insertdecision.js"]) load(src(f), f);
  // The header and the dock's bar are markup; the tree and the rail follow focus, which is not what is asked here.
  load(`
    Cell.prototype.renderBar = function () {};
    Cell.prototype._renderDock = function (shell) {
      if (!this._dockEl) {
        this._dockEl = document.createElement("div");
        this._dockEl.className = "dock";
        this._dockBody = document.createElement("div");
        this._dockEl.append(this._dockBody);
        this._dockSplit = document.createElement("div");
      }
      if (this._dockBody.firstChild !== shell.el) this._dockBody.replaceChildren(shell.el);
    };
    syncFocusedTree = function () {};
  `, "tabs-stubs");
  const run = (code) => load(code, "test");
  const sess = (id, over2) => Object.assign({ id, name: id, activity: "idle", need: "" }, over2);
  const p = {
    ctx, st, doc, els, grid, drawer, run, DNode,
    sessions: (...rows) => { ctx.lastSessions = rows.map((r) => (typeof r === "string" ? sess(r) : r)); },
    sess,
    // Rows named as the user sees the sessions: names({a: "alpha"}) is one idle session "alpha".
    names: (map) => { ctx.lastSessions = Object.entries(map).map(([id, name]) => sess(id, { name })); },
    term: (id, name) => { ctx.__a = [id, name || id]; return run("new TerminalTab(__a[0], __a[1])"); },
    viewer: (p2) => { ctx.__a = p2; return run("new ViewerTab(__a)"); },
    // A pane holding these tabs (the last one active, as addTab leaves it), laid out in the grid.
    pane: (...tabs) => {
      const cell = run("new Cell()");
      ctx.__c = cell;
      run("cells.push(__c)");
      for (const t of tabs) cell.addTab(t);
      run("renderGrid()");
      return cell;
    },
    focus: (cell) => { ctx.__c = cell; run("setFocusedCellRaw(__c)"); },
    maximize: (cell) => { ctx.__c = cell; run("toggleMax(__c)"); },
    // The Files dock in Full mode (what paintDrawerMax writes), open or closed.
    full: (on) => { drawer.hidden = !on; if (on) drawer.dataset.max = "1"; else delete drawer.dataset.max; },
    // A page view open (Settings, Analytics, Audit): pagehost.js's own currentPage() reads this.
    openPage: (id) => { ctx.__d = { id }; run("pageNow = { dlg: __d, mark: null }"); },
    closePage: () => { run("pageNow = null"); },
    width: (w) => { ctx.window.innerWidth = w; },
    // Through JSON: an object made inside the vm has the vm's Object.prototype, which deepStrictEqual does not take for ours.
    decide: (term) => JSON.parse(JSON.stringify(ctx.insertDecision(term))),
  };
  return p;
}

module.exports = { page };
