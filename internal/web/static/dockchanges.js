"use strict";

// The Files dock's explorer, above the tree (spec 5.4, mockup B1): the top row (Go to file, New file, the ⋯ menu), the
// Changes section and the root header. DOM and requests only; what the section says is changesmodel.js's changesView, which
// node tests run, and this file paints it.
//
// The Changes section lists the workspace's git changes: a status letter on every row and `+a −d` where git said it, the
// 2 px session bar on a file the session changed (Activity's files), a scope toggle (Uncommitted | Branch) and, in Branch
// scope, the `branch → base · +a −d` row. Git stays the primary signal. The rows come from the status the tree already
// holds (gitstatus.js); this file asks for what status cannot say:
//   - /api/git/numstat {path, scope, fresh?}: the counts, and Branch's whole list. Asked after each status lands
//     (gitStatusLoad calls dockChangesOnStatus, so it rides the same 1 s debounce), at most one at a time: a status that
//     lands while one is out waits for it, and only the newest is asked for next;
//   - /api/git/base {path}: the branch, the base the SERVER picked (no ref is ever sent from here) and whether HEAD is
//     ahead. Asked when the root or HEAD changes, and again for a failure once it has been a while (changesBaseStale).
// Nothing is asked while the dock is closed, folded away from view or the page is hidden (dkcVisible), and coming back
// asks (dockChangesRefresh). No answer is waited for with a spinner: Uncommitted shows the status rows at once, a status
// that is unavailable is one calm line ("Status unavailable", the reason on hover), and a request that never answers is
// abandoned after DKC_FETCH_MS.
//
// Every path, name, branch and base name goes to the page with textContent: a branch name may hold Unicode format
// characters, and a file name anything at all.

const DKC_FETCH_MS = 20000; // a request that never answers must not hold the one numstat slot for good
const DKC_CODE_TITLES = { M: "Modified", A: "Added (staged)", U: "Untracked", D: "Deleted", C: "Conflict" };

let dkcPrefs = null;          // Map: the remembered scope per session, read from storage on first use
let dkcNum = null;            // {root, scope, status, reply}: the last numstat answer; reply null when the request failed
let dkcBase = null;           // {root, head, reply, at}: the last base answer; reply null when the request failed
let dkcNumFlight = false;
let dkcBaseFlight = false;
let dkcFresh = false;         // a status that was asked for fresh has landed since the last numstat went out
let dkcDrawn = null;          // what the last paint was made from (identity), so a repaint with nothing new costs nothing
let dkcView = null;           // the view on screen: Review and a row's click read it
let dkcRootShown = null;      // the root the header names: a root change ends a root edit

function dkcEl(id) { return document.getElementById(id); }
function dkcRoot() { return typeof ftRoot === "function" ? ftRoot() : ""; }
function dkcSid() { return typeof drawerState !== "undefined" && drawerState ? drawerState.sessionId : null; }

// dkcPrefsMap: the remembered scopes. Read once; every change replaces the map (changesScopePut), so identity says "new".
function dkcPrefsMap() {
  if (!dkcPrefs) {
    let raw = null;
    try { raw = localStorage.getItem(CHANGES_SCOPE_KEY); } catch (_) { /* storage off: nothing remembered */ }
    dkcPrefs = changesScopeParse(raw);
  }
  return dkcPrefs;
}

// gitBaseFor: the last /api/git/base reply for `root` (a BaseInfo), or null when none has landed for it or the request failed.
function gitBaseFor(root) {
  return dkcBase && root && dkcBase.root === root ? dkcBase.reply : null;
}

// dockScope: the scope the Changes section shows for a session, "head" (Uncommitted) or "branch". The editor's diff
// follows it (P2) and so does the combined review (P3).
function dockScope(sid) {
  return changesScopeFor(dkcPrefsMap(), sid, gitBaseFor(dkcRoot()));
}

// dkcVisible: the section can be seen, so asking is worth it. The dock is open on Files with a workspace to show, the
// page is not hidden. A folded explorer counts: its strip carries the count.
function dkcVisible() {
  if (typeof document === "undefined" || document.hidden) return false;
  const dr = dkcEl("drawer");
  if (!dr || dr.hidden || dr.dataset.noroot === "1") return false;
  return typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks();
}

// dkcPost: a POST that answers with the reply object, or null for anything else (an error status, the network, a body that is
// not an object, a request that took longer than DKC_FETCH_MS). Never throws.
async function dkcPost(url, body) {
  const ac = typeof AbortController === "function" ? new AbortController() : null;
  const timer = setTimeout(() => { if (ac) ac.abort(); }, DKC_FETCH_MS);
  try {
    const res = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal: ac ? ac.signal : undefined,
    });
    if (!res.ok) return null;
    const data = await res.json();
    return data && typeof data === "object" ? data : null;
  } catch (_) {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

// dockChangesOnStatus: a status landed (gitStatusLoad). `fresh`: it was read after something changed on disk, so the
// counts are too.
function dockChangesOnStatus(opts) {
  if (opts && opts.fresh) dkcFresh = true;
  dockChangesSync();
}

// dockChangesRefresh: the dock may have just appeared or changed (drawerPaint, once per session poll while it is open):
// paint, and ask for whatever is missing. Idempotent and cheap when nothing moved.
function dockChangesRefresh() {
  dockChangesRender();
  dockChangesSync();
}

// dockChangesSync asks for what the section lacks: the base when the root or HEAD moved, and the numstat of the scope
// shown for the status held now. Each at most once at a time; an answer ends in another call, which asks for what that
// answer left stale (the base landing can turn the default scope to Branch, a newer status can have come in meanwhile).
function dockChangesSync() {
  if (!dkcVisible()) return;
  const root = dkcRoot();
  const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(root) : null;
  if (!cur || cur.status.state !== "ok") return; // not a repository, too many, unavailable: nothing more to ask
  const head = typeof cur.status.head === "string" ? cur.status.head : "";
  if (!dkcBaseFlight && changesBaseStale(dkcBase, root, head, Date.now())) dkcAskBase(root, head);
  const scope = dockScope(dkcSid());
  if (dkcNum && dkcNum.root === root && dkcNum.scope === scope && dkcNum.status === cur.status) return;
  if (dkcNumFlight) return;
  dkcAskNumstat(root, scope, cur.status);
}

async function dkcAskBase(root, head) {
  dkcBaseFlight = true;
  const reply = await dkcPost("/api/git/base", { path: root });
  dkcBaseFlight = false;
  dkcBase = { root, head, reply, at: Date.now() };
  dockChangesRefresh();
}

async function dkcAskNumstat(root, scope, status) {
  dkcNumFlight = true;
  const fresh = dkcFresh;
  dkcFresh = false;
  const reply = await dkcPost("/api/git/numstat", fresh ? { path: root, scope, fresh: true } : { path: root, scope });
  dkcNumFlight = false;
  // Kept even when it is for a root or scope no longer shown: changesView matches records by root and scope.
  dkcNum = { root, scope, status, reply };
  dockChangesRefresh();
}

// --- painting ---------------------------------------------------------------------------------------------

function dkcSetText(el, text) {
  if (el && el.textContent !== text) el.textContent = text;
}

// dkcCounts: the `+a −d` pair as a span, or null when git gave no count (never a guess). A zero side is still said:
// "+0 −12" is a deleted file's own count.
function dkcCounts(add, del) {
  if (add === null || add === undefined || del === null || del === undefined) return null;
  const ct = document.createElement("span");
  ct.className = "dk-ct";
  const a = document.createElement("span");
  a.className = "dk-add";
  a.textContent = "+" + add;
  const d = document.createElement("span");
  d.className = "dk-del";
  d.textContent = "−" + del;
  ct.append(a, d);
  return ct;
}

// dkcRow: one changed file. The whole row is a button: its click shows the file (a preview tab), as a tree row's does.
function dkcRow(r, label) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = r.mine ? "dk-row dk-mine" : "dk-row";
  const ic = document.createElement("span");
  ic.className = "dk-ic";
  ic.setAttribute("aria-hidden", "true");
  ic.textContent = typeof iconFor === "function" ? iconFor(r.name, r.folder, false) : "";
  const nm = document.createElement("span");
  nm.className = "dk-nm";
  nm.textContent = r.name;
  const dir = document.createElement("span");
  dir.className = "dk-dir";
  dir.textContent = r.dir;
  const st = document.createElement("span");
  st.className = "dk-st dk-st-" + r.code.toLowerCase();
  st.textContent = r.code;
  const ct = dkcCounts(r.add, r.del);
  b.append(ic, nm, dir);
  if (ct) b.append(ct);
  b.append(st);
  // The session bar and the letter are colour and a glyph; the words are for assistive technology and the hover.
  const words = [DKC_CODE_TITLES[r.code] || r.code];
  if (r.add !== null && r.del !== null) words.push("+" + r.add + " −" + r.del);
  if (r.mine) words.push(label);
  b.setAttribute("aria-label", r.name + (r.dir ? " in " + r.dir : "") + ", " + words.join(", "));
  b.title = (typeof shortPath === "function" ? shortPath(r.path) : r.path) + "\n" + words.join(", ");
  b.onclick = () => dkcOpenRow(r);
  return b;
}

// dkcOpenRow: a row's click. A file opens as a preview tab; a deleted one has nothing to read, so its diff opens; an
// untracked folder is a place in the tree, not a file, so the tree shows it.
function dkcOpenRow(r) {
  if (r.folder) {
    if (typeof ftReveal === "function") ftReveal(r.path).then((ok) => { if (!ok) showToast("Could not show that folder in the tree", { error: true }); });
    return;
  }
  if (typeof drawerShowFile !== "function") return;
  if (r.code === "D") drawerShowFile(r.path, { view: "changes" });
  else drawerShowFile(r.path);
}

// dkcReview: Review opens the Changes view of the first changed file, pinned (P1; the combined view is P3's).
function dkcReview() {
  const first = dkcView && dkcView.kind === "rows" ? dkcView.rows.find((r) => !r.folder) : null;
  if (first && typeof drawerShowFile === "function") drawerShowFile(first.path, { view: "changes", preview: false });
}

// dkcPaintBranch: `<branch> → <base> · +a −d`. The two names are isolated for bidirectional text: a branch name may carry
// format characters that would reorder what is drawn around it.
function dkcPaintBranch(el, b) {
  el.hidden = !b;
  if (!b) { el.replaceChildren(); el.title = ""; return; }
  const from = document.createElement("span");
  from.className = "dk-br-name";
  from.textContent = b.from;
  const arrow = document.createElement("span");
  arrow.className = "dk-br-sep";
  arrow.textContent = "→";
  const to = document.createElement("span");
  to.className = "dk-br-name";
  to.textContent = b.to;
  el.replaceChildren(from, arrow, to);
  const ct = dkcCounts(b.add, b.del);
  if (ct) {
    const dot = document.createElement("span");
    dot.className = "dk-br-sep";
    dot.textContent = "·";
    el.append(dot, ct);
  }
  el.title = b.from + " → " + b.to;
}

function dkcPaintList(el, view, label) {
  if (view.kind !== "rows") { el.replaceChildren(); return; }
  const nodes = view.rows.map((r) => dkcRow(r, label));
  if (view.more > 0) {
    const more = document.createElement("div");
    more.className = "dk-more";
    more.textContent = "and " + view.more + " more";
    nodes.push(more);
  }
  el.replaceChildren(...nodes);
}

// dkcPaintStrip: the folded explorer's Changes button carries the count (the same N as the section's header).
function dkcPaintStrip(n) {
  const el = dkcEl("drawer-strip-count");
  if (el) {
    el.hidden = n === 0;
    dkcSetText(el, n > 99 ? "99+" : String(n));
  }
  const btn = dkcEl("drawer-strip-changes");
  if (btn) {
    const words = n > 0 ? "Changes, " + n + " changed" : "Changes";
    btn.title = words;
    btn.setAttribute("aria-label", n > 0 ? "Open the explorer at Changes, " + n + " changed" : "Open the explorer at Changes");
  }
}

// dkcRootName: the last segment of the root ("/" for the root of the file system).
function dkcRootName(root) {
  if (!root) return "";
  return root.replace(/\/+$/, "").split("/").pop() || "/";
}

// dkcPaintRoot: the root header, the root's name and its path in muted type. A click edits the root (dkcRootEdit); a box
// session's tree is its workspace only, so there is nothing to edit.
function dkcPaintRoot(root) {
  const btn = dkcEl("dock-root");
  if (!btn) return;
  if (root !== dkcRootShown) { // another root: the field's work is done
    dkcRootShown = root;
    dkcRootEdit(false);
  }
  dkcSetText(dkcEl("dock-root-name"), dkcRootName(root));
  dkcSetText(dkcEl("dock-root-path"), root ? (typeof shortPath === "function" ? shortPath(root) : root) : "");
  const dr = dkcEl("drawer");
  const box = !!dr && dr.dataset.box === "1";
  btn.setAttribute("aria-disabled", box ? "true" : "false");
  btn.tabIndex = box ? -1 : 0;
  btn.title = box ? root : "Change the root folder\n" + root;
  const tree = dkcEl("drawer-tree");
  const open = !!tree && tree.dataset.rootedit === "1";
  if (box) btn.removeAttribute("aria-expanded"); else btn.setAttribute("aria-expanded", open ? "true" : "false");
}

// dockChangesRender brings the section, the strip's count and the root header in step with what is known. It runs on every
// status, base and numstat answer, root change, Activity answer and session poll, so it does nothing unless an input is
// a different object than the last time.
function dockChangesRender() {
  const sec = dkcEl("dock-changes");
  if (!sec) return;
  const root = dkcRoot();
  const sid = dkcSid();
  const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(root) : null;
  const files = typeof drawerChangedFiles !== "undefined" && sid ? drawerChangedFiles.get(sid) || null : null;
  const name = typeof drawerSessionName === "function" ? drawerSessionName(sid) : "";
  const dr = dkcEl("drawer");
  const inputs = [root, cur && cur.status, dkcNum, dkcBase, dkcPrefsMap(), sid, files, name, dr ? dr.dataset.box || "" : "", dkcOwns()];
  if (dkcDrawn && inputs.every((x, i) => x === dkcDrawn[i])) return;
  dkcDrawn = inputs;

  dkcPaintRoot(root);
  const view = dkcOwns()
    ? changesView({ root, status: cur, numstat: dkcNum, base: dkcBase, prefs: dkcPrefsMap(), sid, files })
    : { kind: "none", count: 0 };
  dkcView = view;
  const rows = view.kind === "rows";
  sec.hidden = view.kind === "none";
  dkcSetText(dkcEl("dock-chg-count"), rows && view.count > 0 ? view.count + " changed" : "");
  const first = rows ? view.rows.find((r) => !r.folder) : null;
  dkcEl("dock-review").hidden = !first;
  const scope = dkcEl("dock-scope");
  scope.hidden = !view.showScope;
  for (const b of scope.querySelectorAll("[data-scope]")) b.setAttribute("aria-pressed", b.dataset.scope === view.scope ? "true" : "false");
  dkcPaintBranch(dkcEl("dock-branch"), rows ? view.branch : null);
  // One calm line where there is no list: the section's own words, or "No changes" for a clean workspace.
  const note = dkcEl("dock-chg-note");
  const say = view.kind === "note" ? view.text : rows && view.count === 0 ? "No changes" : "";
  note.hidden = say === "";
  dkcSetText(note, say);
  note.title = view.kind === "note" ? view.title : "";
  const who = name ? "changed by " + name : "changed by this session";
  dkcPaintList(dkcEl("dock-chg-list"), view, who);
  const legend = dkcEl("dock-chg-legend");
  const anyMine = rows && view.rows.some((r) => r.mine);
  legend.hidden = !anyMine;
  dkcSetText(legend.querySelector("span"), who);
  dkcPaintStrip(rows ? view.count : 0);
}

// dkcOwns: the dock is what shows this tree (the phone's tree has no Changes section).
function dkcOwns() {
  return typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks();
}

// --- actions ----------------------------------------------------------------------------------------------

// dkcPickScope: a press on Uncommitted or Branch. Remembered for the session, and Branch's list is asked for now.
function dkcPickScope(scope) {
  const sid = dkcSid();
  if (dockScope(sid) === scope) return;
  dkcPrefs = changesScopePut(dkcPrefsMap(), sid, scope);
  try { localStorage.setItem(CHANGES_SCOPE_KEY, changesScopeSave(dkcPrefs)); } catch (_) { /* storage off: this page only */ }
  dockChangesRefresh();
}

// dkcRootEdit: show (or put away) today's root field, the directory picker, under the root header.
function dkcRootEdit(on, focus) {
  const tree = dkcEl("drawer-tree");
  if (!tree) return;
  const was = tree.dataset.rootedit === "1";
  if (on) tree.dataset.rootedit = "1"; else delete tree.dataset.rootedit;
  const btn = dkcEl("dock-root");
  if (btn && btn.getAttribute("aria-disabled") !== "true") btn.setAttribute("aria-expanded", on ? "true" : "false");
  if (on && !was && typeof ftRootSel !== "undefined" && ftRootSel) { ftRootSel.focus(); ftRootSel.select(); }
  if (!on && was && focus && btn) btn.focus();
}

function dkcRootPress() {
  const dr = dkcEl("drawer");
  if (!dr || dr.dataset.box === "1") return;
  const tree = dkcEl("drawer-tree");
  dkcRootEdit(!(tree && tree.dataset.rootedit === "1"));
}

// dkcCollapseAll: forget every folder opened under the root (the stored set too), and draw the tree again.
function dkcCollapseAll() {
  const root = dkcRoot();
  if (!root) return;
  forgetSubtree(ftExpanded, root);
  saveExpanded(ftExpanded);
  ftLoadRoot();
}

// dkcOpenMore: the ⋯ menu, the kit's, as the row menu is. A read-only folder offers what reads only.
function dkcOpenMore() {
  const more = dkcEl("dock-more");
  const root = dkcRoot();
  if (!more || !root || typeof ftShowMenu !== "function") return;
  const ro = typeof ftRootIsReadOnly === "function" && ftRootIsReadOnly();
  // Close first and give focus back to the ⋯, then act: a dialog hands focus back to what had it when it opened.
  const item = (label, icon, run) => kitMenuButton(label, icon, false, () => { closeRowMenu(true); run(); });
  const nodes = [];
  if (!ro) {
    nodes.push(item("New folder…", "folder-plus", () => promptMkdir(root)));
    nodes.push(item("Upload…", "upload", () => { const b = dkcEl("ft-upload"); if (b) b.click(); }));
    nodes.push(kitMenuSep());
  }
  nodes.push(item("Refresh", "refresh", () => ftLoadRoot({ fresh: true })));
  nodes.push(item("Collapse all", "collapse", dkcCollapseAll));
  ftShowMenu(more, nodes, false, "Explorer actions");
  const first = document.getElementById("ft-menu").querySelector(".menu-item");
  if (first) first.focus();
}

function dockChangesInit() {
  const kbd = dkcEl("drawer-goto-kbd");
  if (kbd && typeof shortcutHint === "function" && typeof IS_MAC !== "undefined") kbd.textContent = shortcutHint("files.goto", IS_MAC);
  const on = (id, fn) => { const el = dkcEl(id); if (el) el.onclick = fn; };
  on("dock-newfile", () => promptNewFile(dkcRoot()));
  // stopPropagation: the document's closer for the tree's menu would shut the menu this press opens.
  on("dock-more", (e) => { if (e && e.stopPropagation) e.stopPropagation(); dkcOpenMore(); });
  on("dock-review", dkcReview);
  on("dock-root", dkcRootPress);
  const scope = dkcEl("dock-scope");
  if (scope) for (const b of scope.querySelectorAll("[data-scope]")) b.onclick = () => dkcPickScope(b.dataset.scope);
  // Escape in the root field puts the field away, unless it is the picker's open list that Escape is closing.
  const field = dkcEl("ft-root-input");
  if (field) field.addEventListener("keydown", (e) => { if (e.key === "Escape" && field.getAttribute("aria-expanded") !== "true") dkcRootEdit(false, true); });
  // The page was hidden and is back: ask for what it missed.
  document.addEventListener("visibilitychange", dockChangesRefresh);
}

if (typeof document !== "undefined" && document.addEventListener) dockChangesInit();
