"use strict";

// Git awareness for the Files tree: a letter badge on each changed file, a dot on
// each folder with changes below it, and the untracked-folder U. The server does
// the git work (POST /api/git/status, a throwaway container); this file turns its
// reply into per-row marks and keeps them fresh.
//
// gitMarks is pure and tested under `node --test`. The rest is glue and reads the
// tree's globals (ftRoot, ftTreeEl, ftRowPaths) only inside typeof guards, so the
// module also loads where there is no page.

const GIT_RANK = { C: 3, D: 3, M: 2, A: 1, U: 1 };
const GIT_TITLES = { M: "Modified", A: "Added (staged)", U: "Untracked", C: "Conflict" };

// gitPlace puts one reply entry's repo-relative `p` under the tree root: its
// absolute path (`base` is the root without trailing slashes), its segments below
// the root, and whether it is a collapsed untracked folder (a trailing "/"). Null
// for an entry outside the shown tree, the root itself, a path with an empty
// segment, or a reply with no usable rootRel ("." is the checkout's top itself).
// One rule for every reader of a status or numstat reply, so the tree's marks and
// the Changes rows land on the same row paths (changesmodel.js reuses it).
function gitPlace(p, rootRel, base) {
  if (typeof p !== "string" || typeof rootRel !== "string" || rootRel === "") return null;
  const isDir = p.endsWith("/");
  let rel = isDir ? p.slice(0, -1) : p;
  if (rootRel !== ".") {
    if (!rel.startsWith(rootRel + "/")) return null; // outside the shown tree (or the root itself)
    rel = rel.slice(rootRel.length + 1);
  }
  if (rel === "") return null;
  const segs = rel.split("/");
  if (segs.some((s) => s === "")) return null;
  return { abs: base + "/" + rel, segs, isDir };
}

// gitMarks: Map<absolute row path, {code, dir, title, untracked?}> for the tree
// rooted at `root`, from one status reply. Files and untracked folders get their
// own code; every folder strictly between the root and a changed entry gets
// dir:true with the strongest code below it and a "N changes inside" title.
// A deleted file has no row (the listing does not show it), so it only counts
// toward its folders. The reply's rootRel says where `root` sits inside the
// checkout; `top` is not used because it is symlink-resolved and need not match
// the paths the tree's rows carry.
function gitMarks(status, root) {
  const out = new Map();
  if (!status || status.state !== "ok" || typeof status.rootRel !== "string" || status.rootRel === "") return out;
  if (!Array.isArray(status.entries) || typeof root !== "string" || root === "") return out;
  const base = root.replace(/\/+$/, "");
  const rr = status.rootRel;
  const folders = new Map(); // abs folder path -> {n, del, code}
  for (const e of status.entries) {
    if (!e || typeof e.p !== "string" || !GIT_RANK[e.x]) continue;
    const at = gitPlace(e.p, rr, base);
    if (!at) continue;
    const { abs, segs, isDir } = at;
    if (isDir) out.set(abs, { code: e.x, dir: true, untracked: true, title: "Untracked folder" });
    else if (e.x !== "D") out.set(abs, { code: e.x, dir: false, title: GIT_TITLES[e.x] });
    for (let i = 1; i < segs.length; i++) {
      const fp = base + "/" + segs.slice(0, i).join("/");
      let f = folders.get(fp);
      if (!f) { f = { n: 0, del: 0, code: e.x }; folders.set(fp, f); }
      f.n++;
      if (e.x === "D") f.del++;
      if (GIT_RANK[e.x] > GIT_RANK[f.code]) f.code = e.x;
    }
  }
  for (const [fp, f] of folders) {
    const title = f.n + (f.n === 1 ? " change" : " changes") + " inside" + (f.del ? " (" + f.del + " deleted)" : "");
    out.set(fp, { code: f.code, dir: true, title });
  }
  return out;
}

// --- state -------------------------------------------------------------------

let gitSeq = 0;          // a reply is dropped unless it is the newest ask
let gitState = null;     // {root, sid, status, marks} of the last reply that landed
let gitTimer = null;
let gitTimerFresh = false; // any refreshSoon in the pending window asked for fresh
const GIT_DEBOUNCE_MS = 1000;

function gitStatusReset() {
  gitSeq++;
  gitState = null;
  if (gitTimer) { clearTimeout(gitTimer); gitTimer = null; }
  gitTimerFresh = false;
}

// gitStatusLoad asks for the tree root's status and paints the answer. A root
// change drops the old marks at once; a re-ask of the same root keeps them on
// screen until the reply lands, so a refresh does not flicker. A failed request
// keeps whatever was shown. opts.fresh asks the server for a result read after
// this request, not a reused or already-running one: send it when something was
// just changed on disk (a save, an upload, a file op, a finished turn, the
// refresh button), and not for the plain load that follows a tree paint.
async function gitStatusLoad(root, sid, opts) {
  if (!root) return null;
  const seq = ++gitSeq;
  if (gitState && gitState.root !== root) { gitState = null; gitPaintTree(); }
  let status = null;
  try {
    const res = await fetch("/api/git/status", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(opts && opts.fresh ? { path: root, fresh: true } : { path: root }),
    });
    if (res.ok) status = await res.json();
  } catch (_) { /* offline: keep the marks */ }
  if (seq !== gitSeq) return null;
  if (!status || typeof status !== "object") return gitState ? gitState.marks : null;
  gitState = { root, sid: sid || null, status, marks: gitMarks(status, root) };
  gitPaintTree();
  // The Changes section's counts and base follow each status that lands (dockchanges.js), fresh when this one was.
  if (typeof dockChangesOnStatus === "function") dockChangesOnStatus({ fresh: !!(opts && opts.fresh) });
  return gitState.marks;
}

// gitStatusCurrent: the last status that landed and the root it was for, {root, status}, or null when there is none or it
// was for another root than `root` (a late or older one is nobody's). The Changes section reads it; it never reads
// gitState itself.
function gitStatusCurrent(root) {
  return gitState && root && gitState.root === root ? { root: gitState.root, status: gitState.status } : null;
}

// gitStatusTreeSid: the session whose files the tree shows right now.
function gitStatusTreeSid() {
  if (typeof drawerState !== "undefined" && drawerState && drawerState.open === "files") return drawerState.sessionId || null;
  const t = typeof focusedTab === "function" ? focusedTab() : null;
  return t && t.id ? t.id : null;
}

// gitStatusRefreshSoon: the one entry point for "something may have changed".
// Debounced, and never a timer poll. Every caller is a reaction to a change, so
// it asks for a fresh result; the debounced request is fresh if any caller in
// its window was (opts.fresh === false opts out).
function gitStatusRefreshSoon(opts) {
  if (gitTimer) clearTimeout(gitTimer);
  if (!opts || opts.fresh !== false) gitTimerFresh = true;
  gitTimer = setTimeout(() => {
    gitTimer = null;
    const fresh = gitTimerFresh;
    gitTimerFresh = false;
    const root = typeof ftRoot === "function" ? ftRoot() : "";
    if (root) gitStatusLoad(root, gitStatusTreeSid(), { fresh });
  }, GIT_DEBOUNCE_MS);
}

// gitStatusOnActivity: a session finished a turn (working -> anything else) and
// it is the one the tree shows: that is when "what did the agent change?" gets
// asked. Called from TerminalTab.setActivity with the previous and new state.
function gitStatusOnActivity(tab, prev, next) {
  if (prev !== "working" || next === "working") return;
  if (!tab || !tab.id || tab.id !== gitStatusTreeSid()) return;
  gitStatusRefreshSoon();
}

// gitStatusCodeFor: the viewer's question — what is this file's git code?
function gitStatusCodeFor(path) {
  const m = gitState && gitState.marks.get(path);
  return m && !m.dir ? m.code : "";
}

// gitStatusReason: why status is unavailable ("" when it is not), for the ↻ title.
function gitStatusReason() {
  return gitState && gitState.status.state === "unavailable" ? (gitState.status.reason || "unavailable") : "";
}

// gitStatusNote: the line over the tree when there are too many changes to mark.
function gitStatusNote() {
  return gitState && gitState.status.state === "toomany" ? "Too many changes to mark (>5000)" : "";
}

// --- painting ----------------------------------------------------------------

function gitCurrentMarks() {
  const root = typeof ftRoot === "function" ? ftRoot() : "";
  return gitState && gitState.root === root ? gitState.marks : null;
}

// gitMarkRow puts (or takes off) the row's badge or dot. A file's badge is a
// button that opens its changes; a folder's dot is just a mark, and the row's own
// click still toggles the folder. The untracked folder's U is a letter, not a dot.
function gitMarkRow(row, path) {
  const marks = gitCurrentMarks();
  const m = marks && marks.get(path);
  const have = row.querySelector(":scope > .ft-git, :scope > .ft-git-dot");
  if (!m) { if (have) { have.remove(); gitNameTitle(row); } return; }
  const dot = m.dir && !m.untracked;
  const key = (dot ? "d" : "b") + m.code + "|" + m.title;
  if (have && have.dataset.gitKey === key) return;
  if (have) have.remove();
  const sfx = " ft-git-" + m.code.toLowerCase();
  let el;
  if (dot) {
    el = document.createElement("span");
    el.className = "ft-git-dot" + sfx;
    el.textContent = "●";
    el.title = m.title;
    el.setAttribute("role", "img");
    el.setAttribute("aria-label", m.title);
  } else if (m.untracked) {
    el = document.createElement("span");
    el.className = "ft-git" + sfx;
    el.textContent = m.code;
    el.title = m.title;
    el.setAttribute("role", "img");
    el.setAttribute("aria-label", m.title);
  } else {
    el = document.createElement("button");
    el.type = "button";
    el.className = "ft-git" + sfx;
    el.textContent = m.code;
    const label = m.title + " — open changes";
    el.title = label;
    el.setAttribute("aria-label", label);
    el.onclick = (e) => {
      // Not the row's click: it would select the row and open the file as a plain read.
      e.stopPropagation();
      gitOpenChanges(path);
    };
  }
  el.dataset.gitKey = key;
  const name = row.querySelector(":scope > .ft-name");
  if (name) name.after(el); else row.append(el);
  gitNameTitle(row);
}

// gitNameTitle: the row's hover text lists what marks it, and a folder's dot is one of the marks (filetree.js composes the
// text). The dock hides the dot visually, so its "N changes inside" is read from the name too.
function gitNameTitle(row) {
  if (typeof ftNameTitle === "function") ftNameTitle(row);
}

// gitOpenChanges opens the file where a row click would, asking for its Changes
// view (viewer-tab.js showChanges).
function gitOpenChanges(path) {
  const opts = { view: "changes" };
  if (typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks() && typeof drawerShowFile === "function") drawerShowFile(path, opts);
  else if (typeof openFile === "function") openFile(path, opts);
}

function gitPaintTree() {
  if (typeof document === "undefined") return;
  // The dock's tabs wear the same letters.
  if (typeof dockRender === "function") dockRender();
  // And the Changes section lists the same status (a root change took it away: it repaints, with nothing).
  if (typeof dockChangesRender === "function") dockChangesRender();
  // An open viewer's Changes button follows the same marks: repaint the toolbars.
  if (typeof ViewerTab !== "undefined" && ViewerTab.allViewers) {
    for (const v of ViewerTab.allViewers()) if (v.toolbarApi) v.toolbarApi.paint();
  }
  if (typeof ftTreeEl !== "undefined" && ftTreeEl && typeof ftRowPaths !== "undefined") {
    for (const row of ftTreeEl.querySelectorAll(".ft-node")) {
      const p = ftRowPaths.get(row);
      if (p) gitMarkRow(row, p);
    }
    gitPaintNote();
  }
  const refresh = document.getElementById("ft-refresh");
  if (refresh) {
    if (refresh.dataset.baseTitle === undefined) refresh.dataset.baseTitle = refresh.title;
    const why = gitStatusReason();
    refresh.title = refresh.dataset.baseTitle + (why ? " — git status unavailable: " + why : "");
  }
}

// gitPaintNote keeps one line above the tree for the too-many-changes message.
function gitPaintNote() {
  const text = gitStatusNote();
  let note = document.getElementById("ft-git-note");
  if (!text) { if (note) note.remove(); return; }
  if (!note) {
    note = document.createElement("div");
    note.id = "ft-git-note";
    note.className = "ft-note";
    ftTreeEl.before(note);
  }
  note.textContent = text;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { gitMarks, gitPlace, gitStatusLoad, gitStatusRefreshSoon, gitStatusOnActivity, gitStatusCodeFor, gitStatusCurrent, gitStatusReason, gitStatusNote, gitStatusReset };
}
