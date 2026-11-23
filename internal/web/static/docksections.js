"use strict";

// The Files dock explorer's two sections, Changes and the folder tree (P3, mockup 5): each collapses with the chevron in its
// header, and a divider between them sets how tall Changes may grow. One state for the dock, in localStorage, not per session
// (the column is one place): {changes, tree} open or not, and `share`, the section's cap as a fraction of the column (null:
// today's rule, as tall as its rows up to 45%). It is painted as data-chg-shut / data-tree-shut and --dk-chg-max on
// #drawer-tree, and desktop.css does the rest. Nothing here asks the server: the Changes section keeps drawing, and asking,
// exactly as it does when it is open (dockchanges.js), so collapsing it never stops or starts a git request.
//
// Pure decisions first (node tests require them), then the DOM glue. Classic script: the names are browser globals.

const DOCK_SECTIONS_KEY = "agent-box:dockSections";
const DOCK_SEC_STEP = 0.02;   // a divider key press moves it 2% of the column, as the column splitter's does
const DOCK_SEC_ROWS = 3;      // either side keeps its header and this many rows when the divider is dragged
const DOCK_SEC_ROW = 22;      // a row's height when none is on screen to measure
const DOCK_SEC_TREE_PAD = 14; // the tree body's padding (6 above, 8 below), which a tree row needs as well

// normalizeDockSections: anything storage may hold → {changes, tree, share}. Only `false` shuts a section; a share is kept
// when it is a number between 5% and 95%, to 3 decimals.
function normalizeDockSections(raw) {
  const o = raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {};
  const share = typeof o.share === "number" && Number.isFinite(o.share) && o.share >= 0.05 && o.share <= 0.95
    ? Math.round(o.share * 1000) / 1000 : null;
  return { changes: o.changes !== false, tree: o.tree !== false, share };
}

// Storage can be missing, full or throw (private windows, blocked site data): the sections then just open with the defaults.
function loadDockSections(storage) {
  try {
    const s = storage && storage.getItem(DOCK_SECTIONS_KEY);
    return normalizeDockSections(s ? JSON.parse(s) : null);
  } catch (_) {
    return normalizeDockSections(null);
  }
}

function saveDockSections(storage, s) {
  try {
    if (storage) storage.setItem(DOCK_SECTIONS_KEY, JSON.stringify(normalizeDockSections(s)));
  } catch (_) { /* a choice that is not kept is only a convenience lost */ }
}

// dockSecBounds: how tall the section may be dragged, in px. m: col (the column's height), top (the section's top below the
// column's), fixed (the section's parts that are not its list: header, scope, branch row, note, legend), row (a list row),
// treeMin (the root header and DOCK_SEC_ROWS tree rows). In a column too short for both, the section's minimum wins.
function dockSecBounds(m) {
  const lo = m.fixed + DOCK_SEC_ROWS * m.row;
  return { lo, hi: Math.max(lo, m.col - m.top - m.treeMin) };
}

// dockSecShare: a section height in px, held inside the bounds, as the share of the column; null without a column.
function dockSecShare(px, m) {
  if (!m || !(m.col > 0) || !Number.isFinite(px)) return null;
  const b = dockSecBounds(m);
  return Math.round(Math.min(b.hi, Math.max(b.lo, px)) / m.col * 1000) / 1000;
}

// dockSecStep: the divider's keys, from `px` (what is drawn). ArrowUp moves it up (Changes shorter), ArrowDown down,
// Home and End to the bounds; null for any other key.
function dockSecStep(px, key, m) {
  if (!m || !(m.col > 0)) return null;
  const b = dockSecBounds(m);
  if (key === "Home") return dockSecShare(b.lo, m);
  if (key === "End") return dockSecShare(b.hi, m);
  if (key === "ArrowUp") return dockSecShare(px - DOCK_SEC_STEP * m.col, m);
  if (key === "ArrowDown") return dockSecShare(px + DOCK_SEC_STEP * m.col, m);
  return null;
}

// --- DOM ---------------------------------------------------------------------------------------------------------------

let dockSec = null; // the state, read from storage on first use

function dockSecEl(id) { return document.getElementById(id); }
function dockSecStorage() {
  try { return typeof localStorage !== "undefined" ? localStorage : null; } catch (_) { return null; }
}
function dockSecState() {
  if (!dockSec) dockSec = loadDockSections(dockSecStorage());
  return dockSec;
}
function dockSecPct(share) { return Math.round(share * 1000) / 10 + "%"; }

// dockSecMeasure: the column as drawn, for the divider (dockSecBounds' m), or null when it is not on screen.
function dockSecMeasure() {
  const col = dockSecEl("drawer-tree");
  const sec = dockSecEl("dock-changes");
  const list = dockSecEl("dock-chg-list");
  const chev = dockSecEl("dock-tree-chev");
  const pane = dockSecEl("files-pane");
  if (!col || !sec || !list) return null;
  const h = (el) => (el ? el.getBoundingClientRect().height : 0);
  const c = col.getBoundingClientRect();
  const s = sec.getBoundingClientRect();
  if (!(c.height > 0)) return null;
  const dkRow = list.querySelector(".dk-row");
  const ftRow = pane ? pane.querySelector(".ft-node") : null;
  return {
    col: c.height,
    top: s.top - c.top,
    fixed: s.height - h(list),
    row: h(dkRow) || DOCK_SEC_ROW,
    treeMin: h(chev && chev.parentNode) + DOCK_SEC_ROWS * (h(ftRow) || DOCK_SEC_ROW) + DOCK_SEC_TREE_PAD,
    secTop: s.top,
    drawn: s.height,
  };
}

// dockSecShareVar: the section's cap on the column, live (a drag) or from the state.
function dockSecShareVar(share) {
  const col = dockSecEl("drawer-tree");
  if (!col) return;
  if (share === null) col.style.removeProperty("--dk-chg-max");
  else col.style.setProperty("--dk-chg-max", dockSecPct(share));
}

// dockSecAriaSet: the divider's aria values, whole percents of the column; `now` is a share.
function dockSecAriaSet(el, now, m, b) {
  const pct = (px) => String(Math.round(px / m.col * 100));
  el.setAttribute("aria-valuenow", String(Math.round(now * 100)));
  el.setAttribute("aria-valuemin", pct(b.lo));
  el.setAttribute("aria-valuemax", pct(b.hi));
}

// dockSecAria: the divider's aria values from what is drawn (a layout read: not for every pointer move).
function dockSecAria() {
  const el = dockSecEl("dock-secsplit");
  const m = dockSecMeasure();
  if (!el || !m) return;
  const b = dockSecBounds(m);
  dockSecAriaSet(el, Math.min(b.hi, Math.max(b.lo, m.drawn)) / m.col, m, b);
}

// dockSecPaint: the state on the column and the chevrons.
function dockSecPaint() {
  const col = dockSecEl("drawer-tree");
  if (!col) return;
  const s = dockSecState();
  if (s.changes) delete col.dataset.chgShut; else col.dataset.chgShut = "1";
  if (s.tree) delete col.dataset.treeShut; else col.dataset.treeShut = "1";
  dockSecShareVar(s.share);
  const chev = (id, open, what) => {
    const b = dockSecEl(id);
    if (!b) return;
    b.setAttribute("aria-expanded", open ? "true" : "false");
    b.title = (open ? "Collapse " : "Expand ") + what;
  };
  chev("dock-chg-chev", s.changes, "Changes");
  chev("dock-tree-chev", s.tree, "the folder tree");
  dockSecAria();
  // A tree opened again shows the open file it could not show collapsed (dockreveal.js).
  if (typeof dockRevealSync === "function") dockRevealSync();
}

// dockSecSave: a new state, kept and painted.
function dockSecSave(next) {
  dockSec = normalizeDockSections(next);
  saveDockSections(dockSecStorage(), dockSec);
  dockSecPaint();
}

// dockSecSet: open or shut one section ("changes" or "tree"). Focus inside what is about to hide (a Changes row, the scope
// toggle, a tree row) goes to that section's chevron, so it never falls to the page; the Changes header stays, so Review
// keeps it.
function dockSecSet(which, open) {
  const key = which === "tree" ? "tree" : "changes";
  const s = dockSecState();
  if (s[key] === open) return;
  if (!open) {
    const a = document.activeElement;
    const part = key === "tree" ? dockSecEl("files-pane") : dockSecEl("dock-changes");
    const head = key === "tree" ? null : dockSecEl("dock-chg-chev") && dockSecEl("dock-chg-chev").parentNode;
    const chev = dockSecEl(key === "tree" ? "dock-tree-chev" : "dock-chg-chev");
    if (a && part && part.contains(a) && !(head && head.contains(a)) && chev) chev.focus();
    // The root field lives in the tree: put it away with it, or the next root press would only close what cannot be seen.
    const col = dockSecEl("drawer-tree");
    if (key === "tree" && col && col.dataset.rootedit === "1" && typeof dkcRootEdit === "function") dkcRootEdit(false);
  }
  dockSecSave(Object.assign({}, s, { [key]: open }));
}

function dockSecToggle(which) {
  dockSecSet(which, !dockSecState()[which === "tree" ? "tree" : "changes"]);
}

// dockSecOpen: an explicit ask to see a section (Reveal in tree, the root field, the strip's Changes button) opens it.
function dockSecOpen(which) {
  dockSecSet(which, true);
}

// dockSecWireDivider: drag, keys and double-click (reset) for the divider. As the column splitter does (app.js
// wireDrawerSplit), a drag writes only the CSS variable per move, against a column measured once at the press, and the share
// is saved once, on release.
function dockSecWireDivider(el) {
  el.addEventListener("pointerdown", (e) => {
    if (e.button !== 0 || el.classList.contains("dragging")) return; // one drag at a time
    const m = dockSecMeasure();
    if (!m) return;
    e.preventDefault();
    const pid = e.pointerId;
    el.setPointerCapture(pid);
    el.classList.add("dragging");
    const b = dockSecBounds(m);
    let r = null;
    const move = (ev) => {
      if (ev.pointerId !== pid) return;
      const v = dockSecShare(ev.clientY - m.secTop, m);
      if (v === null) return;
      r = v;
      dockSecShareVar(r);
      dockSecAriaSet(el, r, m, b);
    };
    const up = (ev) => {
      if (ev.pointerId !== pid) return;
      try { el.releasePointerCapture(pid); } catch (_) {}
      el.classList.remove("dragging");
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
      el.removeEventListener("lostpointercapture", up);
      if (r !== null) dockSecSave(Object.assign({}, dockSecState(), { share: r }));
      dockSecAria();
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
    // A capture lost without pointerup (window blur, the column hidden) still ends the drag.
    el.addEventListener("lostpointercapture", up);
  });
  el.addEventListener("dblclick", () => {
    dockSecSave(Object.assign({}, dockSecState(), { share: null }));
    dockSecAria();
  });
  el.addEventListener("keydown", (e) => {
    const m = dockSecMeasure();
    const v = m ? dockSecStep(m.drawn, e.key, m) : null;
    if (v === null) return;
    e.preventDefault();
    dockSecSave(Object.assign({}, dockSecState(), { share: v }));
    dockSecAria();
  });
  el.addEventListener("focus", dockSecAria);
}

function dockSecInit() {
  const chg = dockSecEl("dock-chg-chev");
  const tree = dockSecEl("dock-tree-chev");
  if (chg) chg.onclick = () => dockSecToggle("changes");
  if (tree) tree.onclick = () => dockSecToggle("tree");
  // The rest of the Changes header toggles too; its buttons (the chevron, Review) are their own.
  const head = chg && chg.parentNode;
  if (head && head.addEventListener) {
    head.addEventListener("click", (e) => {
      const t = e.target;
      if (t && typeof t.closest === "function" && t.closest("button")) return;
      dockSecToggle("changes");
    });
  }
  const split = dockSecEl("dock-secsplit");
  if (split && split.addEventListener) dockSecWireDivider(split);
  dockSecPaint();
}

if (typeof document !== "undefined" && document.getElementById) dockSecInit();

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    DOCK_SECTIONS_KEY, DOCK_SEC_STEP, normalizeDockSections, loadDockSections, saveDockSections, dockSecBounds, dockSecShare, dockSecStep,
  };
}
