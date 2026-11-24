"use strict";

// The Files dock's explorer, above the tree (spec 5.4, mockup B1): the top row (Go to file, New file, the ⋯ menu), the
// Changes section and the root header. DOM and requests only; what the section says is changesmodel.js's changesView, which
// node tests run, and this file paints it.
//
// The Changes section lists the workspace's git changes: a status letter on every row and `+a −d` where git said it, the
// 2 px session bar on a file the session changed (Activity's files), a scope toggle (Uncommitted | Branch) and, in Branch
// scope, the `branch → base · +a −d` row. Git stays the primary signal. The rows come from the status the tree already
// holds (gitstatus.js); this file asks for what status cannot say:
//   - /api/git/numstat {path, scope, fresh?}: the counts, and Branch's whole list. Asked BESIDE each status, not after it
//     (gitStatusLoad calls dockChangesOnStatusAsk as it sends one, so it rides the same 1 s debounce), for the scope the
//     user picked, else the default once the base is known, else "auto" (the server picks Branch or Uncommitted and the
//     reply names it). The exception is Uncommitted after a status with no entries: that one is asked when the status
//     lands, and only if it has entries. At most one is out at a time: a status that lands while one is out waits for it,
//     and only the newest is asked for next (dockChangesOnStatus, when the status lands);
//   - /api/git/base {path}: the branch, the base the SERVER picked (no ref is ever sent from here) and whether HEAD is
//     ahead. Asked beside the first status of a root, again when the root, HEAD or branch changes, and again for a failure
//     once it has been a while (changesBaseStale); a failure keeps the last good answer for that root on screen
//     (changesBaseRecord).
// Until the first answers of a root land, the list it showed last (gitlast.js: a reload, a session switch) is drawn, with
// "Updating…" in the header. It is a picture: Review is hidden, and dockChangesRows / dockChangesView say nothing, so the
// review loop, F7 and All changes act only on git's answer.
// Nothing is asked while the dock is closed, folded away from view or the page is hidden (dkcVisible), and coming back
// asks (dockChangesRefresh). No answer is waited for with a spinner: Uncommitted shows the status rows at once, a status
// that is unavailable is one calm line ("Status unavailable", the reason on hover), and a request that never answers is
// abandoned after DKC_FETCH_MS.
//
// While the agent works its edits arrive from Activity before git has been asked about them (dockwatch.js): such a file is a
// provisional row (changesmodel.js), drawn with a muted "…" (class dk-prov) where the letter will be, and the letter takes
// its place in the same element when a status asked for after the edit lands. A file git then calls clean goes away.
//
// The review loop (P3, editorreview.js). Each row has a check circle (dk-ck) that is filled while the file's reviewed mark still
// stands (reviewedState: the row's git fingerprint is unchanged and the agent has not edited the file since), and a chip (dk-nt)
// with the file's pending notes. While any tab of the session is in Changes the header reads "reviewed n of m" (reviewedCount, over
// the file rows) instead of "N changed", and Review opens the first file whose mark does not stand, in Changes.
//
// Every path, name, branch and base name goes to the page with textContent: a branch name may hold Unicode format
// characters, and a file name anything at all.

/* global notesFor -- the notes store (editornotes.js): asked behind typeof so this section still draws where it is not loaded, with no notes */
const DKC_FETCH_MS = 20000; // a request that never answers must not hold the one numstat slot for good
const DKC_CODE_TITLES = { M: "Modified", A: "Added (staged)", U: "Untracked", D: "Deleted", C: "Conflict" };

let dkcPrefs = null;          // Map: the remembered scope per session, read from storage on first use
let dkcNum = null;            // {root, scope, seq, reply, auto}: the last numstat answer, for the status ask `seq`; reply null when the request failed; auto: asked as "auto"
let dkcNumAsk = null;         // {root, seq}: the numstat out, while dkcNumFlight
let dkcLastAsk = null;        // {root, asked, seq}: the last numstat asked: dockChangesSync never asks the same one twice in a row
let dkcBase = null;           // {root, head, reply, at}: the last base answer; reply null when the request failed
let dkcNumFlight = false;
let dkcBaseFlight = false;
let dkcFresh = false;         // a status that was asked for fresh has landed since the last numstat went out
let dkcDrawn = null;          // what the last repaint check was made from (identity), so a repaint with nothing new costs nothing
let dkcPainted = null;        // what is on screen, as text (the view and what else the paint reads): equal means no write at all
const dkcRows = new Map();    // path -> the row element in the list on screen (rows are updated in place, see dkcPaintList)
const dkcParts = new WeakMap(); // row element -> its parts and the row it shows
let dkcMoreRow = null;        // the "and N more" element, made once
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

// dockChangesRows: the rows the section shows now, in its order (folders and provisional rows included: reviewRows picks the files). F7
// across files (editorreview.js) walks them.
function dockChangesRows() {
  return dkcView && dkcView.kind === "rows" ? dkcView.rows : [];
}

// dockChangesView: the view the section shows now (changesView's {kind, rows, ...}), null before the first paint. The All changes tab
// (reviewall.js) draws its sections from the same rows and says the same note where there is no list.
function dockChangesView() {
  return dkcView;
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
// counts asked for it must be too, unless they were asked beside it (dockChangesOnStatusAsk sent its freshness along).
function dockChangesOnStatus(opts) {
  if (opts && opts.fresh) {
    const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(dkcRoot()) : null;
    const seq = cur ? cur.seq : -1;
    if (!(dkcNumAsk && dkcNumAsk.seq === seq) && !(dkcNum && dkcNum.seq === seq)) dkcFresh = true;
  }
  dockChangesSync();
}

// dockChangesOnStatusAsk: a status is being asked for `root` (gitStatusRead, `seq` its number). The counts are asked beside
// it, and the base too when the root has none yet, so neither waits for the status to land first.
function dockChangesOnStatusAsk(o) {
  if (!o || !dkcVisible() || !o.root || o.root !== dkcRoot()) return;
  const root = o.root;
  // The last status said git cannot list this root (unavailable, too many): nothing is asked until one says it can.
  const last = dkcLastStatus(root);
  if (last && last.state !== "ok") return;
  const scope = dkcAskScope(root);
  // The base beside the status, when the list needs it (Branch, or a scope still to pick). Uncommitted lists without it, and a
  // base asked now would take a git slot ahead of the numstat: the landing asks it (the toggle and the branch row).
  if (scope !== "head" && !dkcBaseFlight && !(dkcBase && dkcBase.root === root)) dkcAskBase(root, null, null);
  if (dkcNumFlight) return; // the landing asks for the newest once this one is back
  if (scope === "head" && dkcLastClean(root)) return; // a clean tree has nothing to count: the landing decides
  dkcAskNumstat(root, scope, o.seq, !!o.fresh);
}

// dkcAskScope: the scope to ask a numstat for before the status lands: the scope shown once the root's base is known (a
// Branch pick with no ok base shows Uncommitted: a Branch run would not be shown), else the session's pick, else "auto".
function dkcAskScope(root) {
  const sid = dkcSid();
  if (dkcBase && dkcBase.root === root) return dockScope(sid);
  const pick = dkcPrefsMap().get(typeof sid === "string" ? sid : "");
  return pick === "head" || pick === "branch" ? pick : "auto";
}

// dkcLastStatus: the last status known for the root, the live one, else the one kept from before (gitlast.js), else null.
function dkcLastStatus(root) {
  const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(root) : null;
  if (cur) return cur.status;
  const kept = typeof gitLastGet === "function" ? gitLastGet(root) : null;
  return kept ? kept.status : null;
}

// dkcLastClean: the last status known for the root had no entries.
function dkcLastClean(root) {
  const st = dkcLastStatus(root);
  return !!st && st.state === "ok" && Array.isArray(st.entries) && st.entries.length === 0;
}

// dockChangesRefresh: the dock may have just appeared or changed (drawerPaint, once per session poll while it is open):
// paint, and ask for whatever is missing. Idempotent and cheap when nothing moved.
function dockChangesRefresh() {
  dockChangesRender();
  dockChangesSync();
  // The editor's diff follows the scope shown here, and the merge base the Branch scope names (editordiff.js).
  if (typeof editorDiffSync === "function") editorDiffSync();
}

// dockChangesSync asks for what the section lacks: the base when the root, HEAD or branch moved, and the numstat of the scope
// shown for the status held now. Each at most once at a time; an answer ends in another call, which asks for what that
// answer left stale (the base landing can turn the default scope to Branch, a newer status can have come in meanwhile).
function dockChangesSync() {
  if (!dkcVisible()) return;
  const root = dkcRoot();
  const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(root) : null;
  if (!cur || cur.status.state !== "ok") return; // not a repository, too many, unavailable: nothing more to ask
  const head = typeof cur.status.head === "string" ? cur.status.head : "";
  const branch = changesBranchOf(cur.status);
  if (!dkcBaseFlight && changesBaseStale(dkcBase, root, head, branch, Date.now())) dkcAskBase(root, head, branch);
  const scope = dockScope(dkcSid());
  // Up to date: the answer for this status, or for a newer ask whose status has not landed (or failed), in the scope shown.
  // A newer answer is never replaced by an older one (dkcAskNumstat), so asking for the older status again would loop. Before
  // the root's base is known the scope shown is a guess (Uncommitted), so an "auto" answer stands until the base lands.
  // The scope to ask for, and to hold the answer against: the one shown once the base is known; before it, the pick or "auto"
  // (the scope shown is then a guess, and asking one scope while checking another would ask without end).
  const baseKnown = !!dkcBase && dkcBase.root === root;
  const want = baseKnown ? scope : dkcAskScope(root);
  // Before the base, an "auto" ask is met by any answer for this status (the server's pick or a scope asked beside it).
  if (dkcNum && dkcNum.root === root && dkcNum.seq >= cur.seq && (dkcNum.scope === want || (!baseKnown && (dkcNum.auto || want === "auto")))) return;
  // Uncommitted scope and a status with no entries: the numstat of nothing is nothing, so it is not asked for (a clean tree is
  // the common case, and each numstat is a container run). The record stands for the answer, at once even with a numstat out
  // (asked beside this status, before anyone knew it was clean), so a numstat from before the tree went clean cannot leave its
  // rows on screen, and the next status with entries asks as usual. Branch scope still asks: a clean working tree can sit on a
  // branch that differs from its base.
  if (scope !== "branch" && Array.isArray(cur.status.entries) && cur.status.entries.length === 0) {
    dkcNum = { root, scope, seq: cur.seq, reply: { state: "ok", rootRel: ".", entries: [] } };
    // The record no longer holds the last ask's answer, so that ask may be needed again (Branch picked back on this status).
    dkcLastAsk = null;
    dockChangesRender();
    return;
  }
  if (dkcNumFlight) return; // its answer asks again for whatever it left stale
  // The same numstat as the last one asked has its answer already: recorded, or dropped for a newer one of its scope. Asking
  // it again could only get the same answer, so whatever the record rule above misses, sync cannot loop.
  if (dkcLastAsk && dkcLastAsk.root === root && dkcLastAsk.asked === want && dkcLastAsk.seq === cur.seq) return;
  dkcAskNumstat(root, want, cur.seq, dkcFresh);
}

// dkcAskBase asks for the base of `root`. `head` and `branch` are the status's it is asked after; null for one asked beside
// the first status of a root, whose reply says the HEAD and branch it is for (so the status that lands next does not ask again).
async function dkcAskBase(root, head, branch) {
  dkcBaseFlight = true;
  const reply = await dkcPost("/api/git/base", { path: root });
  dkcBaseFlight = false;
  if (head === null || branch === null) {
    // Asked beside the first status: an answer says what it is for; a failure takes the status that has landed meanwhile,
    // so the retry pause of changesBaseStale holds instead of a new ask at the next landing.
    const said = !!reply && reply.state !== "unavailable";
    const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(root) : null;
    head = said && typeof reply.head === "string" ? reply.head : cur && typeof cur.status.head === "string" ? cur.status.head : "";
    branch = said && typeof reply.branch === "string" ? reply.branch : cur ? changesBranchOf(cur.status) : "";
  }
  dkcBase = changesBaseRecord(dkcBase, root, head, branch, reply, Date.now());
  dockChangesRefresh();
}

// dkcAskNumstat asks for the counts of `root` in `asked` ("head", "branch" or "auto") for the status ask `seq`. The record
// takes the scope the reply names for "auto" (Uncommitted when it names none, a failure included).
async function dkcAskNumstat(root, asked, seq, fresh) {
  dkcNumFlight = true;
  dkcNumAsk = { root, seq };
  dkcLastAsk = { root, asked, seq };
  if (fresh) dkcFresh = false;
  const reply = await dkcPost("/api/git/numstat", fresh ? { path: root, scope: asked, fresh: true } : { path: root, scope: asked });
  dkcNumFlight = false;
  dkcNumAsk = null;
  const scope = asked === "auto" ? (reply && reply.scope === "branch" ? "branch" : "head") : asked;
  // Kept even when it is for a root or scope no longer shown: changesView matches records by root and scope. Never over a
  // record for a newer status of the same root in the same scope (the clean-status record, made while this one was out). A
  // record in another scope is replaced: dockChangesSync asked for this scope because it is the one shown, and keeping the
  // other would make it ask again, without end.
  if (!(dkcNum && dkcNum.root === root && dkcNum.scope === scope && dkcNum.seq > seq)) dkcNum = { root, scope, seq, reply, auto: asked === "auto" };
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

// dkcNewRow: the element of one changed file, empty. The whole row is a button: its click shows the file (a preview tab),
// as a tree row's does. It is built once for a path and updated in place by dkcFillRow, so the keyboard focus on it, a click
// between press and release and the pointer over it all survive a repaint.
function dkcNewRow() {
  const b = document.createElement("button");
  b.type = "button";
  const part = (cls) => { const e = document.createElement("span"); e.className = cls; return e; };
  const p = { ck: part("dk-ck"), ic: part("dk-ic"), nm: part("dk-nm"), dir: part("dk-dir"), nt: part("dk-nt"), ct: part("dk-ct"), add: part("dk-add"), del: part("dk-del"), st: part("dk-st"), r: null };
  p.ck.setAttribute("aria-hidden", "true");
  p.ic.setAttribute("aria-hidden", "true");
  p.ct.append(p.add, p.del);
  b.append(p.ck, p.ic, p.nm, p.dir, p.st);
  b.onclick = () => dkcOpenRow(p.r); // the row it shows now, not the one it was made for
  dkcParts.set(b, p);
  return b;
}

// dkcFillRow brings a row element to `r`, writing only what differs. `rv` is the review state: {done: {<path>: true}, notes: {<path>: n}}.
function dkcFillRow(b, r, label, rv) {
  const p = dkcParts.get(b);
  p.r = r;
  const done = !!rv.done[r.path];
  const ckCls = done ? "dk-ck dk-ck-done" : "dk-ck";
  if (p.ck.className !== ckCls) p.ck.className = ckCls;
  const notes = rv.notes[r.path] || 0;
  if (notes > 0) {
    dkcSetText(p.nt, notes + (notes === 1 ? " note" : " notes"));
    if (p.nt.dataset.n !== String(notes)) p.nt.dataset.n = String(notes); // the number a narrow list shows alone (desktop.css)
    if (p.nt.parentNode !== b) b.insertBefore(p.nt, p.ct.parentNode === b ? p.ct : p.st); // before the counts, else before the letter
  } else if (p.nt.parentNode === b) {
    p.nt.remove();
  }
  const cls = r.mine ? "dk-row dk-mine" : "dk-row";
  if (b.className !== cls) b.className = cls;
  fileIconInto(p.ic, r.name, !!r.folder);
  dkcSetText(p.nm, r.name);
  dkcSetText(p.dir, r.dir);
  // The counts are there only where git gave both (never a guess); a zero side is still said: "+0 −12" is a deleted file's own count.
  const known = r.add !== null && r.add !== undefined && r.del !== null && r.del !== undefined;
  if (known) {
    dkcSetText(p.add, "+" + r.add);
    dkcSetText(p.del, "−" + r.del);
    if (p.ct.parentNode !== b) b.insertBefore(p.ct, p.st);
  } else if (p.ct.parentNode === b) {
    p.ct.remove();
  }
  // A provisional row has no letter yet: a muted "…", and the words say what it waits for.
  const prov = r.provisional === true;
  const stCls = prov ? "dk-st dk-prov" : "dk-st dk-st-" + r.code.toLowerCase();
  if (p.st.className !== stCls) p.st.className = stCls;
  dkcSetText(p.st, prov ? "…" : r.code);
  // The session bar and the letter are colour and a glyph; the words are for assistive technology and the hover.
  const words = [prov ? "Waiting for git" : DKC_CODE_TITLES[r.code] || r.code];
  if (known) words.push("+" + r.add + " −" + r.del);
  if (r.mine) words.push(label);
  if (done) words.push("reviewed");
  if (notes > 0) words.push(notes + (notes === 1 ? " note" : " notes"));
  const aria = r.name + (r.dir ? " in " + r.dir : "") + ", " + words.join(", ");
  if (b.getAttribute("aria-label") !== aria) b.setAttribute("aria-label", aria);
  const title = (typeof shortPath === "function" ? shortPath(r.path) : r.path) + "\n" + words.join(", ");
  if (b.title !== title) b.title = title;
}

// dkcOpenRow: a row's click. A file opens as a preview tab; a deleted one has nothing to read, so its diff opens; an
// untracked folder is a place in the tree, not a file, so the tree shows it. While the All changes tab is the one on screen the
// Changes list is its file tree: the click scrolls to the file's section instead (a file past the tab's cap has none, and opens as usual).
function dkcOpenRow(r) {
  if (r.folder) {
    if (typeof ftReveal === "function") ftReveal(r.path).then((ok) => { if (!ok) showToast("Could not show that folder in the tree", { error: true }); });
    return;
  }
  if (typeof reviewAllScrollTo === "function" && reviewAllScrollTo(r.path)) {
    if (typeof dockTreeFold === "function") dockTreeFold(); // an explorer opened from the strip folds again, as after a file
    return;
  }
  if (typeof drawerShowFile !== "function") return;
  if (r.code === "D") drawerShowFile(r.path, { view: "changes" });
  else drawerShowFile(r.path);
}

// dkcFirstFile: whether there is a row Review can open: the first changed file git has answered for (a provisional row has no diff
// to read yet).
function dkcFirstFile(view) {
  return view && view.kind === "rows" ? reviewRows(view.rows)[0] || null : null;
}

// dkcReview: Review opens the All changes tab (reviewall.js) at the first file whose reviewed mark does not stand (every one of them
// reviewed: the first).
function dkcReview() {
  const sid = dkcSid();
  const files = typeof drawerChangedFiles !== "undefined" && sid ? drawerChangedFiles.get(sid) || null : null;
  const first = dkcView && dkcView.kind === "rows" ? reviewFirstOpen(dkcView.rows, typeof reviewMarksOf === "function" ? reviewMarksOf(sid) : {}, files) : null;
  if (first && typeof reviewAllOpen === "function") reviewAllOpen(sid, first.path);
}

// dkcReviewState: what the section draws of the review loop, for the file rows of `view`:
//   {reviewing, done: {<path>: true}, notes: {<path>: pending notes}, doneN, total}
// `reviewing`: some tab of the session is in Changes (the header says "reviewed n of m"). Notes come from notesFor(sid) (the store of
// editornotes.js, asked behind typeof: where it is not loaded there are none).
function dkcReviewState(view, sid, files) {
  const rows = view.kind === "rows" ? reviewRows(view.rows) : [];
  const marks = typeof reviewMarksOf === "function" ? reviewMarksOf(sid) : {};
  const count = reviewedCount(rows, marks, files);
  const done = {};
  for (const r of rows) if (Object.prototype.hasOwnProperty.call(marks, r.path) && reviewedState(marks[r.path], r, files)) done[r.path] = true;
  let list = null;
  try { list = typeof notesFor === "function" && sid ? notesFor(sid) : null; } catch (_) { /* no notes */ }
  return {
    reviewing: typeof editorDiffAnyChanges === "function" && editorDiffAnyChanges(),
    done, notes: notesPendingByPath(list), doneN: count.done, total: count.total,
  };
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

// dkcPaintList brings the list to the view's rows IN PLACE, keyed by path: a row whose path is still there keeps its element
// (only its text and classes change), a new path gets a new element, a path that is gone loses its element, and an element
// is moved only when the order needs it. Status lands about once a second while an agent works, and a list that was
// replaced each time would drop the focus of a keyboard user on a row and swallow a click made between press and release.
function dkcPaintList(el, view, label, rv) {
  const rows = view.kind === "rows" ? view.rows : [];
  const nodes = [];
  const keep = new Set();
  for (const r of rows) {
    let b = dkcRows.get(r.path);
    if (!b) { b = dkcNewRow(); dkcRows.set(r.path, b); }
    dkcFillRow(b, r, label, rv);
    keep.add(r.path);
    nodes.push(b);
  }
  for (const [path, b] of dkcRows) {
    if (keep.has(path)) continue;
    b.remove();
    dkcRows.delete(path);
  }
  const more = view.kind === "rows" ? view.more : 0;
  if (more > 0) {
    if (!dkcMoreRow) { dkcMoreRow = document.createElement("div"); dkcMoreRow.className = "dk-more"; }
    dkcSetText(dkcMoreRow, "and " + more + " more");
    nodes.push(dkcMoreRow);
  } else if (dkcMoreRow && dkcMoreRow.parentNode) {
    dkcMoreRow.remove();
  }
  nodes.forEach((n, i) => { if (el.children[i] !== n) el.insertBefore(n, el.children[i] || null); });
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
  // "worktree" or "clone" after the name, when the root is one (dockrootchip.js).
  if (typeof dockRootChipPaint === "function") dockRootChipPaint(root);
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
// status, base and numstat answer, root change, Activity answer and session poll. Two checks keep that cheap: nothing is
// done unless an input is a different object than the last time (a poll), and nothing is written unless what would be
// drawn differs from what is (a status that says what the last one said: every status is a new object).
function dockChangesRender() {
  const sec = dkcEl("dock-changes");
  if (!sec) return;
  const root = dkcRoot();
  const sid = dkcSid();
  const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(root) : null;
  const files = typeof drawerChangedFiles !== "undefined" && sid ? drawerChangedFiles.get(sid) || null : null;
  const name = typeof drawerSessionName === "function" ? drawerSessionName(sid) : "";
  const dr = dkcEl("drawer");
  const box = dr ? dr.dataset.box || "" : "";
  const owns = dkcOwns();
  // The review marks (replaced, never edited, on every change), whether a tab is in Changes, and the pending notes as text: a value
  // that is the same one is "nothing new" even when the store makes a fresh list each time it is asked.
  const marks = typeof reviewMarksOf === "function" ? reviewMarksOf(sid) : null;
  const reviewing = typeof editorDiffAnyChanges === "function" && editorDiffAnyChanges();
  let notes = "";
  try { notes = typeof notesFor === "function" && sid ? JSON.stringify(notesPendingByPath(notesFor(sid))) : ""; } catch (_) { /* no notes */ }
  const inputs = [root, cur && cur.status, dkcNum, dkcBase, dkcPrefsMap(), sid, files, name, box, owns, marks, reviewing, notes];
  if (dkcDrawn && inputs.every((x, i) => x === dkcDrawn[i])) return;
  dkcDrawn = inputs;

  // statusAt: when the status held was asked for, so an agent edit made since is a provisional row until git answers.
  const statusAt = cur && typeof gitStatusAt === "function" ? gitStatusAt() : 0;
  let view = owns
    ? changesView({ root, status: cur, numstat: dkcNum, base: dkcBase, prefs: dkcPrefsMap(), sid, files, statusAt })
    : { kind: "none", count: 0 };
  // Before git's first answers for this root (no status yet, no base yet so the scope is still a guess, or Branch waiting for
  // its list), the list kept from the last time (gitlast.js) is drawn, Updating…, and read by nothing. While Branch waits, only
  // a Branch list: an Uncommitted one would be a list of other changes.
  // The scope shown is known once the base is, or with an Uncommitted pick (which needs no base to list).
  const pick = dkcPrefsMap().get(typeof sid === "string" ? sid : "");
  const scopeKnown = (!!dkcBase && dkcBase.root === root) || pick === "head";
  let stale = false;
  const listed = !cur || cur.status.state === "ok"; // a status that says not a repository, too many or unavailable says so itself
  if (owns && root && listed && typeof gitLastGet === "function" && (!cur || !scopeKnown || (view.kind === "note" && view.waiting === true))) {
    const kept = gitLastGet(root);
    if (kept && kept.view && (!cur || !scopeKnown || kept.view.scope === view.scope)) { view = kept.view; stale = true; }
  }
  const rv = dkcReviewState(view, sid, files);
  const painted = JSON.stringify([view, root, name, box, owns, rv, stale]);
  if (painted === dkcPainted) { // nothing new to draw here; the Changes view's header reads some of the same inputs (Activity's list)
    if (typeof editorReviewRefresh === "function") editorReviewRefresh();
    return;
  }
  dkcPainted = painted;
  dkcPaintRoot(root);
  dkcView = stale ? null : view;
  // Kept for the next reload or session switch: git's answer, once the scope is known (not the guess before the base lands).
  if (!stale && scopeKnown && view.kind === "rows" && root && typeof gitLastSet === "function") gitLastSet(root, "view", view);
  const rows = view.kind === "rows";
  sec.hidden = view.kind === "none";
  const count = stale ? "Updating…" : rows && rv.reviewing && rv.total > 0 ? "reviewed " + rv.doneN + " of " + rv.total : rows && view.count > 0 ? view.count + " changed" : "";
  dkcSetText(dkcEl("dock-chg-count"), count);
  dkcEl("dock-review").hidden = stale || !dkcFirstFile(view);
  const scope = dkcEl("dock-scope");
  scope.hidden = !view.showScope;
  for (const b of scope.querySelectorAll("[data-scope]")) b.setAttribute("aria-pressed", b.dataset.scope === view.scope ? "true" : "false");
  dkcPaintBranch(dkcEl("dock-branch"), rows ? view.branch : null);
  // One calm line where there is no list: the section's own words, or "No changes" for a clean workspace.
  const note = dkcEl("dock-chg-note");
  const say = view.kind === "note" ? view.text : rows && view.rows.length === 0 ? "No changes" : "";
  note.hidden = say === "";
  // The clean workspace is the declared "changes" empty state: its glyph goes before the words (G4 91). Any other note stays plain.
  const clean = view.kind !== "note" && !!rows && view.rows.length === 0;
  if (clean) {
    if (note.children.length === 0) {
      const e = emptyState("changes");
      note.textContent = say;
      note.insertBefore(uiIcon(e.icon, { size: 14, cls: "empty-glyph" }), note.firstChild);
    }
  } else if (note.children.length) note.textContent = say;
  else dkcSetText(note, say);
  note.title = view.kind === "note" ? view.title : "";
  const who = name ? "changed by " + name : "changed by this session";
  dkcPaintList(dkcEl("dock-chg-list"), view, who, rv);
  const legend = dkcEl("dock-chg-legend");
  const anyMine = rows && view.rows.some((r) => r.mine);
  legend.hidden = !anyMine;
  dkcSetText(legend.querySelector("span"), who);
  dkcPaintStrip(rows ? view.count : 0);
  if (typeof editorReviewRefresh === "function") editorReviewRefresh();
}

// dkcOwns: the dock is what shows this tree (the phone's tree has no Changes section).
function dkcOwns() {
  return typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks();
}

// --- actions ----------------------------------------------------------------------------------------------

// dkcPickScope: a press on Uncommitted or Branch. Remembered for the session, and Branch's list is asked for now. Either
// segment pins the choice, the one already on as well: a pick made on the default is still a pick, and it must hold when
// the default moves (a commit, a branch switch).
function dkcPickScope(scope) {
  dkcPrefs = changesScopePut(dkcPrefsMap(), dkcSid(), scope);
  try { localStorage.setItem(CHANGES_SCOPE_KEY, changesScopeSave(dkcPrefs)); } catch (_) { /* storage off: this page only */ }
  dockChangesRefresh();
}

// dkcRootEdit: show (or put away) today's root field, the directory picker, under the root header.
function dkcRootEdit(on, focus) {
  const tree = dkcEl("drawer-tree");
  if (!tree) return;
  const was = tree.dataset.rootedit === "1";
  // The root field is in the tree: a collapsed tree opens (P3).
  if (on && typeof dockSecOpen === "function") dockSecOpen("tree");
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

// dkcOpenMore: the ⋯ menu, the kit's, as the row menu is; a second press on the ⋯ closes it. A read-only folder offers what
// reads only.
function dkcOpenMore() {
  const more = dkcEl("dock-more");
  const menu = dkcEl("ft-menu");
  const root = dkcRoot();
  if (!more || !menu || !root || typeof ftShowMenu !== "function") return;
  if (more.getAttribute("aria-expanded") === "true") { closeRowMenu(true); return; } // ftShowMenu keeps it true while the menu is open
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
  // The "Reveal open file" checkbox (dockreveal.js): the menu closes before the choice is made.
  if (typeof dockRevealMenuRow === "function") nodes.push(kitMenuSep(), dockRevealMenuRow(() => closeRowMenu(true)));
  ftShowMenu(more, nodes, false, "Explorer actions");
  const first = menu.querySelector(".menu-item");
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
