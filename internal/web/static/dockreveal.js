"use strict";

// The Files dock reveals the open file in its tree (design P4): whenever the dock's active tab changes (any opener, a tab
// switch, a close, a restore) or the tree can be seen again, the file's folders open and its row is selected, without
// taking focus, without opening a collapsed tree or unfolding the explorer, and never while the user is in the tree
// (pointer over it or focus in it: it waits until they leave). A file outside the tree's root leaves the root alone and the
// breadcrumb row says "Outside this tree · Reveal". The explorer's ⋯ holds the toggle, "Reveal open file" (on by default).
//
// The pure half (the preference, the skipped folders, which tab's file) is exported for node; the rest is glue over
// filetree.js (ftReveal, ftRevealTarget), filesdrawer.js (drawerPaint calls dockRevealSync) and the explorer column's
// painted state. Desktop only: drawerOwnsFileClicks is false on the phone, so nothing here runs there.

const REVEAL_PREF_KEY = "agent-box:revealOpenFile";
const REVEAL_DEBOUNCE_MS = 150;  // a burst of tab changes (a restore, arrow keys on the strip) reveals once
const REVEAL_SKIP = new Set(["node_modules", "bower_components"]); // never opened automatically (VS Code's default)
const REVEAL_WHERE_CAP = 200;     // /api/fs/where answers kept for the page

// revealPrefOn: is "Reveal open file" on? Anything but "0" is on, a storage that throws too.
function revealPrefOn(storage) {
  try { return !storage || storage.getItem(REVEAL_PREF_KEY) !== "0"; } catch (_) { return true; }
}

function revealPrefSet(storage, on) {
  try { if (storage) storage.setItem(REVEAL_PREF_KEY, on ? "1" : "0"); } catch (_) { /* the choice lasts this page */ }
}

// revealExcluded: does an automatic reveal of `path` stop short because a folder between the root and it is one of
// REVEAL_SKIP? The root itself is not judged (a root the user picked inside node_modules is theirs), nor a path outside it.
// The explicit Reveal in tree goes in anyway.
function revealExcluded(root, path) {
  const chain = ancestorsBetween(root, path);
  if (!chain) return false;
  return chain.some((d) => REVEAL_SKIP.has(d.slice(d.lastIndexOf("/") + 1)));
}

// revealWant: the file the dock wants shown in the tree, from {open, tab}: the active tab's absolute host path, or null for
// no tab, the All changes tab, a file inside a box (it is in no tree) or a dock that is not showing files.
function revealWant(s) {
  const t = s && s.open ? s.tab : null;
  if (!t || t.review || t.box || typeof t.path !== "string" || t.path[0] !== "/") return null;
  return t.path;
}

// --- glue ---------------------------------------------------------------------------------------------------------

let drvTimer = null;
let drvQueued = "";   // the signature a timer is set for
let drvDone = "";     // the signature last dealt with: the same file, root and tree load is not revealed twice
let drvWait = null;   // the cleanup of the wait for the user to leave the tree
let drvOut = null;    // {path, root}: the open file is outside that root and the tree could show it (the hint)
const drvWhere = new Map(); // path -> /api/fs/where answer {path, top?}, or null when it was refused

function drvStorage() {
  try { return typeof localStorage !== "undefined" ? localStorage : null; } catch (_) { return null; }
}

function dockRevealWant() {
  if (typeof drawerOwnsFileClicks !== "function" || !drawerOwnsFileClicks() || typeof dockStateOf !== "function") return null;
  const st = dockStateOf(drawerState.sessionId);
  const tab = (typeof dockTabsOf === "function" ? dockTabsOf(st) : st.tabs || []).find((t) => t.key === st.active) || null;
  return revealWant({ open: true, tab });
}

function drvSig(want) {
  return want + "\n" + (typeof ftRoot === "function" ? ftRoot() : "") + "\n" + (typeof ftGen !== "undefined" ? ftGen : 0);
}

// drvTreeShown: can the user see the tree's rows? Not while the explorer is folded (unless the strip opened it), nor while
// the folder tree is collapsed (a shut tree with no Changes section above it is still drawn open), nor with no root.
function drvTreeShown() {
  const dr = document.getElementById("drawer");
  if (!dr || typeof ftTreeEl === "undefined" || !ftTreeEl) return false;
  if (dr.dataset.folded === "1" && dr.dataset.treeOpen !== "1") return false;
  if (dr.dataset.noroot === "1") return false;
  const col = document.getElementById("drawer-tree");
  const chg = document.getElementById("dock-changes");
  if (col && col.dataset.treeShut === "1" && !(chg && chg.hidden)) return false;
  return true;
}

// drvUserInTree: the pointer is over the tree or focus is in it: the tree must not move under them.
function drvUserInTree() {
  let hover = false;
  try { hover = !!ftTreeEl.matches(":hover"); } catch (_) { hover = false; }
  const a = document.activeElement;
  return hover || (!!a && ftTreeEl.contains(a));
}

// drvArmWait: try again once the pointer leaves the tree or focus does.
function drvArmWait() {
  if (drvWait) return;
  const again = () => { drvDisarm(); dockRevealSync(); };
  ftTreeEl.addEventListener("pointerleave", again);
  ftTreeEl.addEventListener("focusout", again);
  drvWait = () => {
    ftTreeEl.removeEventListener("pointerleave", again);
    ftTreeEl.removeEventListener("focusout", again);
  };
}

function drvDisarm() {
  if (drvWait) { drvWait(); drvWait = null; }
}

function drvSetOutside(v) {
  const same = (!v && !drvOut) || (v && drvOut && v.path === drvOut.path && v.root === drvOut.root);
  if (same) return;
  drvOut = v;
  if (typeof dockRender === "function") dockRender();
}

// drvWhereOf: /api/fs/where's answer for `path`, asked once per page (a refusal is kept as null).
async function drvWhereOf(path) {
  if (drvWhere.has(path)) return drvWhere.get(path);
  let w = null;
  try {
    const a = await api("/api/fs/where?path=" + encodeURIComponent(path));
    w = a && typeof a.path === "string" ? a : null;
  } catch (_) { w = null; }
  drvWhere.set(path, w);
  if (drvWhere.size > REVEAL_WHERE_CAP) drvWhere.delete(drvWhere.keys().next().value);
  return w;
}

// drvCanShow: is `path`'s folder inside a workspace root, so that the tree could be rooted where it is?
function drvCanShow(path) {
  const cut = path.lastIndexOf("/");
  const dir = cut > 0 ? path.slice(0, cut) : "/";
  return typeof insideAnyRoot === "function" && typeof ftCurrentRoots === "function" && insideAnyRoot(dir, ftCurrentRoots());
}

// dockRevealSync: the dock or the tree changed (drawerPaint, paintDrawerLayout, dockSecPaint, a tree load). Cheap when
// nothing did: one signature compare.
function dockRevealSync() {
  const want = dockRevealWant();
  if (want === null) {
    if (drvTimer) { clearTimeout(drvTimer); drvTimer = null; }
    drvQueued = "";
    drvDisarm();
    drvSetOutside(null);
    return;
  }
  const sig = drvSig(want);
  if (sig === drvDone || sig === drvQueued) return;
  if (drvTimer) clearTimeout(drvTimer);
  drvQueued = sig;
  drvTimer = setTimeout(drvRun, REVEAL_DEBOUNCE_MS);
}

// dockRevealAfterLoad: the tree was drawn again (a refresh, a rename, a new root): the open file is shown again, the
// selection being the tree's own and gone with its rows (P4.6).
function dockRevealAfterLoad() {
  dockRevealSync();
}

async function drvRun() {
  drvTimer = null;
  drvQueued = "";
  const want = dockRevealWant();
  if (want === null) return;
  const sig = drvSig(want);
  if (sig === drvDone) return;
  const root = ftRoot();
  let where = null;
  if (ftRevealTarget(want, null) === null) {
    where = await drvWhereOf(want);
    if (dockRevealWant() !== want || drvSig(want) !== sig) return; // moved on meanwhile: that change has its own run
  }
  const target = ftRevealTarget(want, where);
  if (target === null) {
    drvSetOutside(where && drvCanShow(want) ? { path: want, root } : null);
    drvDone = sig;
    return;
  }
  drvSetOutside(null);
  if (revealExcluded(root, target)) { drvDone = sig; return; }
  if (!revealPrefOn(drvStorage())) {
    // Off: nothing opens, but a row already on screen is the open file's, so it is the one selected.
    const row = ftRowFor(target);
    if (row) ftSelect(row);
    drvDone = sig;
    return;
  }
  if (!drvTreeShown()) return; // the next sync (the tree shown again) tries again
  if (drvUserInTree()) { drvArmWait(); return; }
  drvDisarm();
  drvDone = sig;
  await ftReveal(want, where ? { auto: true, where } : { auto: true });
}

// dockRevealOutside: does the breadcrumb row say "Outside this tree" for `path`? Only for the open file, once the server
// has said where it is and the tree's root (still this one) does not hold it.
function dockRevealOutside(path) {
  return !!drvOut && drvOut.path === path && typeof ftRoot === "function" && drvOut.root === ftRoot();
}

// dockRevealOutsideGo: the hint's Reveal: an explicit reveal of the open file, which roots the tree where the file is
// (ftReveal), opening a folded explorer first.
function dockRevealOutsideGo() {
  const want = dockRevealWant();
  if (want === null) return;
  if (typeof dockTreeReveal === "function") dockTreeReveal();
  const where = drvWhere.get(want);
  ftReveal(want, where ? { where } : undefined).then((ok) => {
    if (!ok && typeof showToast === "function") showToast("Could not show that file in the tree", { error: true });
  });
}

// dockRevealMenuRow: the explorer ⋯ menu's "Reveal open file" checkbox row. `close` puts the menu away first.
function dockRevealMenuRow(close) {
  const on = revealPrefOn(drvStorage());
  const row = kitMenuButton("Reveal open file", on ? "check" : "", false, () => {
    close();
    revealPrefSet(drvStorage(), !on);
    drvDone = ""; // judged again under the new choice
    dockRevealSync();
  });
  row.setAttribute("role", "menuitemcheckbox");
  row.setAttribute("aria-checked", on ? "true" : "false");
  return row;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { revealPrefOn, revealPrefSet, revealExcluded, revealWant, REVEAL_PREF_KEY };
}
