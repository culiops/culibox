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

// gitIgnored: Set<absolute path> of the reply's gitignored paths under the tree rooted at `root` (a folder's without its
// trailing slash). Empty unless the reply is ok with a usable rootRel; anything but a string that places is left out.
function gitIgnored(status, root) {
  const out = new Set();
  if (!status || status.state !== "ok" || typeof status.rootRel !== "string" || status.rootRel === "") return out;
  if (!Array.isArray(status.ignored) || typeof root !== "string" || root === "") return out;
  const base = root.replace(/\/+$/, "");
  for (const p of status.ignored) {
    const at = gitPlace(p, status.rootRel, base);
    if (at) out.add(at.abs);
  }
  return out;
}

// gitIgnoredAt: is `path` in `set`, or below a folder that is (only folders strictly under `root` are looked at)?
function gitIgnoredAt(set, root, path) {
  if (!set || set.size === 0 || typeof path !== "string") return false;
  if (set.has(path)) return true;
  const base = String(root).replace(/\/+$/, "");
  if (!path.startsWith(base + "/")) return false;
  for (let cut = path.lastIndexOf("/"); cut > base.length; cut = path.lastIndexOf("/", cut - 1)) {
    if (set.has(path.slice(0, cut))) return true;
  }
  return false;
}

// --- state -------------------------------------------------------------------

let gitSeq = 0;          // the number of the newest ask: asks are numbered in the order they were sent
let gitFloor = 0;        // replies to asks up to this number are dropped (a reset)
let gitRoot = "";        // the root of the newest ask
let gitState = null;     // {root, sid, status, marks, at, seq} of the status applied (`at`: gitStatusAt, `seq`: the ask it answers)
let gitTimer = null;
let gitTimerFresh = false; // any refreshSoon in the pending window asked for fresh
const gitOut = new Set(); // the status requests out, as {ac, timer}: what the debounced asks wait for (a reset empties it)
let gitHeld = null;       // {fresh, watch}: the one trailing ask the debounced asks collapsed into while a request was out
let gitTimerWatch = true; // every ask in the pending window was the dock watch's (one explicit ask makes the window explicit)
let gitWatchAt = -Infinity; // when the last watch-driven status was sent
let gitWatchTimer = null;   // the wait for GIT_WATCH_MIN_MS to pass, with a watch-driven ask due; later ones join it
const GIT_DEBOUNCE_MS = 1000;
// The least gap between two status requests the dock watch asks for (dockwatch.js passes {watch: true}). The server starts a
// docker run for every fresh status after the first has launched (internal/gitview/runner.go StatusFresh) and the numstat that
// follows each landing is another, so without a floor a turn that edits a file every second or two costs a status run and a
// numstat run per edit, bounded only by the round trip. 3 s is the floor the final review of the dock settled on (it caps this
// at about 20 status runs a minute; the real per-run cost on a host is not measured here); the last edit's ask is still sent
// when the floor ends (it is trailing, never dropped). Explicit asks (a save, a file op, the end of a turn, ↻, a tree paint) are
// not subject to it.
const GIT_WATCH_MIN_MS = 3000;
// A status request that never answers is abandoned after this, so it cannot hold the debounced asks back for good. The
// server gives a run up to 5 s to get one of its slots (defaultSlotWait) and then 10 s of its own (defaultTimeout,
// internal/gitview/runner.go): its worst answer comes after 15 s.
const GIT_FETCH_MS = 17000;
// How old an answer to a PLAIN (non-fresh) ask may be: the server answers it from a finished run up to this old, and joins a
// run already in flight (internal/gitview/runner.go defaultReuse, 2 s, used by Status; StatusFresh never takes that cache).
const GIT_REUSE_MS = 2000;

function gitStatusReset() {
  gitFloor = ++gitSeq;
  gitState = null;
  if (gitTimer) { clearTimeout(gitTimer); gitTimer = null; }
  gitTimerFresh = false;
  gitTimerWatch = true;
  gitHeld = null;
  if (gitWatchTimer) { clearTimeout(gitWatchTimer); gitWatchTimer = null; }
  gitWatchAt = -Infinity;
  for (const o of gitOut) { clearTimeout(o.timer); if (o.ac) o.ac.abort(); }
  gitOut.clear();
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
  const out = { ac: typeof AbortController === "function" ? new AbortController() : null, timer: null };
  out.timer = setTimeout(() => { if (out.ac) out.ac.abort(); }, GIT_FETCH_MS);
  gitOut.add(out);
  try {
    return await gitStatusRead(root, sid, opts, out.ac ? out.ac.signal : undefined);
  } finally {
    // Whatever became of it (landed, dropped, failed, timed out), a debounced ask that was held behind it goes out once no
    // request is left. A reset emptied the set already, cleared the timer and forgot the held ask: nothing is left to do.
    if (gitOut.delete(out)) {
      clearTimeout(out.timer);
      gitRelease();
    }
  }
}

// gitRelease sends the trailing ask the debounced ones collapsed into, when no status request is out any more. It asks for the
// root and session of the moment (the tree may have moved while it waited), fresh if any collapsed ask was. A trailing ask that
// was only the dock watch's still keeps the floor between watch-driven requests (gitAskNow).
function gitRelease() {
  if (gitOut.size > 0 || !gitHeld) return;
  const held = gitHeld;
  gitHeld = null;
  gitAskNow(held.fresh, held.watch);
}

// gitStatusRead is one status request and what comes of its answer (see gitStatusLoad).
async function gitStatusRead(root, sid, opts, signal) {
  const seq = ++gitSeq;
  gitRoot = root;
  const sentAt = Date.now();
  if (gitState && gitState.root !== root) { gitState = null; gitPaintTree(); }
  let status = null;
  try {
    const req = fetch("/api/git/status", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(opts && opts.fresh ? { path: root, fresh: true } : { path: root }),
      signal,
    });
    // The Changes section asks for its counts (and a first base) beside this status, not after it (dockchanges.js).
    try {
      if (typeof dockChangesOnStatusAsk === "function") dockChangesOnStatusAsk({ root, seq, fresh: !!(opts && opts.fresh) });
    } catch (_) { /* the section's trouble is not the tree's */ }
    const res = await req;
    if (res.ok) status = await res.json();
  } catch (_) { /* offline, or abandoned after GIT_FETCH_MS: keep the marks */ }
  // Statuses land in the order they were sent. A reply is dropped when a reset came after its ask, when the newest ask is for
  // another root, or when it is OLDER than the status already applied for this root (a slow one arriving after a newer one).
  // Any other reply is applied, even with newer asks still out: while edits keep coming and git is slower than the gap between
  // asks, each answer lands in turn, and the tree and the Changes section never wait for a pause that does not come.
  if (seq <= gitFloor || root !== gitRoot) return null;
  if (gitState && gitState.root === root && seq < gitState.seq) return null;
  if (!status || typeof status !== "object") return gitState ? gitState.marks : null;
  // When the answer is as new as. A fresh ask: its send time (the server read the disk after it). A plain one: the server may
  // answer it from a run up to GIT_REUSE_MS old, so only back to that before its send time; and never earlier than the status
  // held for this root, which the server's cache is at least as new as (so the time does not go backwards and a "…" row
  // that was hidden as clean does not come back).
  const prevAt = gitState && gitState.root === root ? gitState.at : 0;
  const at = opts && opts.fresh ? sentAt : Math.max(sentAt - GIT_REUSE_MS, prevAt);
  gitState = { root, sid: sid || null, status, marks: gitMarks(status, root), at, seq };
  // Kept for the tab's life (gitlast.js), so a reload or a session switch paints these marks at once.
  if ((status.state === "ok" || status.state === "toomany") && typeof gitLastSet === "function") gitLastSet(root, "status", status);
  gitPaintTree();
  // The Changes section's counts and base follow each status that lands (dockchanges.js), fresh when this one was.
  if (typeof dockChangesOnStatus === "function") dockChangesOnStatus({ fresh: !!(opts && opts.fresh) });
  return gitState.marks;
}

// gitStatusCurrent: the last status that landed, the root it was for and the number of the ask it answers, {root, status,
// seq}, or null when there is none or it was for another root than `root` (a late or older one is nobody's). The Changes
// section reads it; it never reads gitState itself. When it was asked for is gitStatusAt's.
function gitStatusCurrent(root) {
  return gitState && root && gitState.root === root ? { root: gitState.root, status: gitState.status, seq: gitState.seq } : null;
}

// gitStatusAt: the status held is as new as this (ms), 0 when there is none. For a fresh ask it is when gitStatusLoad SENT the
// request the status answers (not when the answer landed, and not the newest ask still out): git has seen every change made
// before it, and none made after. For a plain ask it is GIT_REUSE_MS earlier (see gitStatusRead). The Changes section's
// provisional rows compare an agent edit's `ts` with it.
function gitStatusAt() {
  return gitState ? gitState.at : 0;
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
//
// One status request at a time comes of these asks. While a request is out the debounced ask is HELD: the asks that
// come meanwhile collapse into one trailing ask, sent when the request is done (it landed, was dropped, failed or timed
// out), fresh if any collapsed ask was. So a git slower than the gap between asks costs about one run per round trip, not
// one per ask. The server does not do this for us: a fresh call never takes its reuse cache and joins a run only until that
// run has launched, so each fresh ask after a launch starts a docker run of its own (gitview StatusFresh), and the runs
// share MaxParallel slots with every other session's git. Explicit loads (gitStatusLoad: a tree paint, ↻) are not held.
//
// opts.watch marks the dock watch's asks (dockwatch.js: the agent edited a file). Those also keep GIT_WATCH_MIN_MS between one
// status request and the next: an ask that falls inside the floor waits for it, and every ask that comes meanwhile joins that
// one wait. A window with any other caller in it (a save, a file op, the end of a turn) is explicit and is never delayed.
function gitStatusRefreshSoon(opts) {
  if (gitTimer) clearTimeout(gitTimer);
  if (!opts || opts.fresh !== false) gitTimerFresh = true;
  if (!opts || !opts.watch) gitTimerWatch = false;
  gitTimer = setTimeout(() => {
    gitTimer = null;
    const fresh = gitTimerFresh;
    const watch = gitTimerWatch;
    gitTimerFresh = false;
    gitTimerWatch = true;
    gitAskNow(fresh, watch);
  }, GIT_DEBOUNCE_MS);
}

// gitAskNow is where a debounced ask becomes a request, is held behind the one that is out, or (a watch ask) waits for the
// floor. The floor is measured from the last watch-driven send, whenever it was made: a held ask released after a slow answer
// goes at once, one released inside the floor waits for the rest of it.
function gitAskNow(fresh, watch) {
  const root = typeof ftRoot === "function" ? ftRoot() : "";
  if (!root) return;
  if (watch) {
    // Date.now() is the wall clock: a step back (NTP, a laptop waking) would leave the last send in the future and stall the asks
    // for as long as it stepped. The last send is taken as no later than now, so the wait is never more than the floor.
    const now = Date.now();
    if (gitWatchAt > now) gitWatchAt = now;
    const wait = gitWatchAt + GIT_WATCH_MIN_MS - now;
    if (wait > 0) {
      if (!gitWatchTimer) gitWatchTimer = setTimeout(() => { gitWatchTimer = null; gitAskNow(true, true); }, wait);
      return;
    }
  }
  if (fresh && gitWatchTimer) {
    // A fresh status that goes out now (or is held for the one out) is read after the edit the waiting watch ask is about: it
    // has nothing left to ask. This is also what keeps a floor that ends at the same moment as a debounce from asking twice.
    clearTimeout(gitWatchTimer);
    gitWatchTimer = null;
  }
  if (gitOut.size > 0) {
    gitHeld = { fresh: fresh || (gitHeld ? gitHeld.fresh : false), watch: watch && (!gitHeld || gitHeld.watch) };
    return;
  }
  if (watch) gitWatchAt = Date.now();
  gitStatusLoad(root, gitStatusTreeSid(), { fresh });
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

// gitCurrentMarks: the marks the tree draws: the live status's for the root, else, until one lands, those of the status kept
// for the root (gitlast.js) from before a reload or a session switch. Drawing only: gitStatusCodeFor and the Changes section
// read the live status alone, so nothing acts on a kept one.
let gitKept = null; // {root, status, marks}: the kept status's marks, made once per kept status
function gitCurrentMarks() {
  const root = typeof ftRoot === "function" ? ftRoot() : "";
  if (gitState && gitState.root === root) return gitState.marks;
  const rec = root && typeof gitLastGet === "function" ? gitLastGet(root) : null;
  if (!rec || !rec.status) return null;
  if (!gitKept || gitKept.root !== root || gitKept.status !== rec.status) gitKept = { root, status: rec.status, marks: gitMarks(rec.status, root) };
  return gitKept.marks;
}

// gitIgnoredNow: is `path` gitignored, by the status the tree draws (the live one for the root, else the kept one)? Drawing
// and the automatic reveal only.
let gitIgnKept = null; // {root, status, set}: the set of the status last asked about, made once per status
function gitIgnoredNow(path) {
  const root = typeof ftRoot === "function" ? ftRoot() : "";
  let status = gitState && gitState.root === root ? gitState.status : null;
  if (!status) {
    const rec = root && typeof gitLastGet === "function" ? gitLastGet(root) : null;
    status = rec && rec.status;
  }
  if (!status) return false;
  if (!gitIgnKept || gitIgnKept.root !== root || gitIgnKept.status !== status) gitIgnKept = { root, status, set: gitIgnored(status, root) };
  return gitIgnoredAt(gitIgnKept.set, root, path);
}

// gitIgnoreRow marks a gitignored row with class ft-ign, whether or not the dock is open now (a tree drawn before it opens is the
// one it shows). Only desktop.css's #drawer rules draw it: the phone's tree looks as it did.
function gitIgnoreRow(row, path) {
  const on = gitIgnoredNow(path);
  if (row.classList.contains("ft-ign") !== on) row.classList.toggle("ft-ign", on);
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
  // The editor's gutter and Changes view compare with git's HEAD: a status that names another one asks for the new text.
  if (typeof editorDiffSync === "function") editorDiffSync();
  if (typeof editorBlameSyncOwner === "function") editorBlameSyncOwner(); // HEAD may have moved: the open file's blame follows
  // And the Changes section lists the same status (a root change took it away: it repaints, with nothing).
  if (typeof dockChangesRender === "function") dockChangesRender();
  // An open viewer's Changes button follows the same marks: repaint the toolbars.
  if (typeof ViewerTab !== "undefined" && ViewerTab.allViewers) {
    for (const v of ViewerTab.allViewers()) if (v.toolbarApi) v.toolbarApi.paint();
  }
  gitRepaintRows();
  const refresh = document.getElementById("ft-refresh");
  if (refresh) {
    if (refresh.dataset.baseTitle === undefined) refresh.dataset.baseTitle = refresh.title;
    const why = gitStatusReason();
    refresh.title = refresh.dataset.baseTitle + (why ? " — git status unavailable: " + why : "");
  }
}

// gitRepaintRows brings every row of the tree to the marks of the moment, and the note above it. The tree calls it after it
// swaps a newly drawn level in (filetree.js), since a status may have landed while the rows were drawn off the page.
function gitRepaintRows() {
  if (typeof ftTreeEl === "undefined" || !ftTreeEl || typeof ftRowPaths === "undefined") return;
  for (const row of ftTreeEl.querySelectorAll(".ft-node")) {
    const p = ftRowPaths.get(row);
    if (p) { gitMarkRow(row, p); gitIgnoreRow(row, p); }
  }
  gitPaintNote();
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
  module.exports = { gitMarks, gitPlace, gitIgnored, gitIgnoredAt, gitIgnoredNow, gitIgnoreRow, gitStatusLoad, gitStatusRefreshSoon, gitStatusOnActivity, gitStatusCodeFor, gitStatusCurrent, gitStatusAt, gitStatusReason, gitStatusNote, gitStatusReset, gitCurrentMarks, GIT_REUSE_MS, GIT_WATCH_MIN_MS, GIT_FETCH_MS };
}
