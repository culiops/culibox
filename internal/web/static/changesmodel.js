"use strict";

// The explorer's Changes section, as pure functions: the rows (which files the
// workspace has changed, in what order, with what +a −d, changesRows) and the
// decisions around them (which scope shows, when the base is asked for again, the
// branch row's totals, and the whole section's view, changesView). No DOM, and the
// functions it takes from other scripts are pure too, so `node --test` reaches
// it; dockchanges.js asks for the replies and paints the view. Classic script:
// browser globals, required under node.
//
// A row's path is mapped from the reply's repo-relative `p` by gitstatus.js's
// gitPlace, the rule the tree's marks use, and "mine" is decided by drawerfiles.js's
// changedSegs, the rule behind the tree's session bars. Both are reached at call
// time (a global in the page, a require under node), so a Changes row and its
// tree row cannot disagree about a path.

const CHANGES_MAX = 200; // rows shown; the rest are counted in `more`
const CHANGES_SCOPE_KEY = "agent-box:changesScope"; // localStorage: {sid: "head"|"branch"}
const CHANGES_SCOPE_CAP = 100; // remembered sessions; the oldest choice is the first one out
const CHANGES_BASE_RETRY_MS = 30000; // a base that could not be read is asked for again after this
const CHANGES_TOOMANY = "Too many changes to list (>5000)";

// Letters a reply may carry. Numstat has no C (git diff calls an unmerged file
// a modification) and its U is the web layer's untracked file; status has all
// five. Anything else is dropped, so a stray letter never becomes a class name.
// Sets, not objects: "constructor" or "__proto__" must not pass for a letter.
const CHANGES_NUM_CODES = new Set(["M", "A", "U", "D"]);
const CHANGES_STATUS_CODES = new Set(["M", "A", "U", "D", "C"]);

// changesCount: a line count the reply gave, or null. Unknown is null and never
// 0: a count is shown only when git (or the web layer's own count) said it.
function changesCount(n) {
  return Number.isInteger(n) && n >= 0 ? n : null;
}

// changesNumstatFiles: the files of an ok numstat reply that sit under the tree root, as
// [{at, code, add, del}] in reply order: `at` is gitPlace's answer for the path, `add`/`del` the
// counts or null. Entries with a letter that is not a code, a path outside the tree, a folder
// (numstat never lists one) or a path already listed are left out. A binary file has no lines to
// count, and a pair with one bad half is not shown as a half-known "+? −3": unknown, not 0.
// One walk for the rows and for the totals, so the two cannot disagree about what a file is.
function changesNumstatFiles(numstat, base, placeOf) {
  const out = [];
  if (!numstat || numstat.state !== "ok" || !Array.isArray(numstat.entries)) return out;
  const seen = new Set();
  for (const e of numstat.entries) {
    if (!e || !CHANGES_NUM_CODES.has(e.x)) continue;
    const at = placeOf(e.p, numstat.rootRel, base);
    if (!at || at.isDir || seen.has(at.abs)) continue;
    seen.add(at.abs);
    const a = changesCount(e.a);
    const d = changesCount(e.d);
    const known = e.bin !== true && a !== null && d !== null;
    out.push({ at, code: e.x, add: known ? a : null, del: known ? d : null });
  }
  return out;
}

// changesRows: {rows, more} for the Changes section.
//   scope    "head" (uncommitted changes) or "branch" (HEAD against the merge
//            base, from /api/git/numstat's own scope); anything but "branch"
//            reads as "head".
//   status   the /api/git/status reply; only state "ok" yields rows.
//   numstat  the /api/git/numstat reply for the same scope, or null.
//   files    the Activity files of the session, most recently edited first.
//   root     the tree root, absolute.
//   statusAt when the status was asked for, in ms (gitStatusAt): an agent edit newer than it is one git has not been
//            asked about yet (see provisional rows below). Absent or not a positive number: there are none.
//
// A row is {path, name, dir, code, add, del, mine, folder, provisional}. Numstat gives the
// letter and the counts of each changed file, a deleted one included (the tree
// has no row for it). Status adds what numstat lacks: an untracked file or
// folder it could not count (no counts), and in "head" scope any other entry,
// so the list is not empty while numstat is slow or unavailable. In "branch"
// scope status's tracked entries are against HEAD, not the base, so only its
// untracked ones count. In "head" scope a conflict (C) wins over numstat's M,
// as the tree's badge says C.
//
// Counts come from numstat alone. Activity's add/del count edit strings, not the
// diff, so a file numstat does not list has null counts however much the agent
// typed. `mine` is true for a path in `files`. Mine come first in `files` order,
// then the rest by path (a plain code-unit compare: the same order whatever the
// browser's locale). Only CHANGES_MAX rows are returned, cut after the ordering
// so a row of this session is never the one dropped; `more` counts the rest.
//
// Provisional rows (both scopes): while the agent works its edits are known from Activity a poll after they are
// made, and git is asked about them a moment later. An Activity file under the root whose `ts` is newer than
// `statusAt`, and that no row above already covers (git's own row wins), is the agent's edit that git has not been
// asked about since: {code: "", provisional: true, mine: true}, no counts, drawn as a muted "…" until a status
// asked for after the edit lands. When one did and git does not list the file, git says it is clean (committed,
// reverted, ignored), so nothing is shown. A file with no `ts` cannot be told from an old edit: no row.
function changesRows(opts) {
  const none = { rows: [], more: 0 };
  const { scope, status, numstat, files, root, statusAt } = opts || {};
  if (!status || status.state !== "ok" || typeof root !== "string" || root === "") return none;
  const placeOf = typeof gitPlace === "function" ? gitPlace : require("./gitstatus.js").gitPlace;
  const segsOf = typeof changedSegs === "function" ? changedSegs : require("./drawerfiles.js").changedSegs;
  const head = scope !== "branch";
  const base = root.replace(/\/+$/, "");
  const byPath = new Map();
  const add = (at, code, a, d) => {
    byPath.set(at.abs, {
      path: at.abs,
      name: at.segs[at.segs.length - 1],
      dir: at.segs.slice(0, -1).join("/"),
      code,
      add: a,
      del: d,
      mine: false,
      folder: at.isDir,
      provisional: false,
    });
  };

  for (const f of changesNumstatFiles(numstat, base, placeOf)) add(f.at, f.code, f.add, f.del);
  if (Array.isArray(status.entries)) {
    for (const e of status.entries) {
      if (!e || !CHANGES_STATUS_CODES.has(e.x)) continue;
      const at = placeOf(e.p, status.rootRel, base);
      if (!at) continue;
      // Every directory entry is an untracked folder, whatever its letter, as in gitMarks.
      const code = at.isDir ? "U" : e.x;
      if (!head && code !== "U") continue;
      const have = byPath.get(at.abs);
      if (have) {
        if (head && code === "C") have.code = "C";
        continue;
      }
      add(at, code, null, null);
    }
  }

  // The agent's edits git has not been asked about yet: a row of their own, in the spot git's row will take.
  const rootSegs = Number.isFinite(statusAt) && statusAt > 0 ? segsOf(root) : null;
  if (rootSegs && Array.isArray(files)) {
    for (const f of files) {
      if (!f || !Number.isFinite(f.ts) || f.ts <= statusAt) continue;
      const segs = segsOf(f.path);
      if (!segs || segs.length <= rootSegs.length || !rootSegs.every((s, i) => segs[i] === s)) continue;
      const rel = segs.slice(rootSegs.length);
      const abs = base + "/" + rel.join("/");
      if (byPath.has(abs)) continue; // git has a row for it (or an earlier entry of this list made one)
      byPath.set(abs, {
        path: abs,
        name: rel[rel.length - 1],
        dir: rel.slice(0, -1).join("/"),
        code: "",
        add: null,
        del: null,
        mine: true,
        folder: false,
        provisional: true,
      });
    }
  }

  // Position in `files`, keyed by the normalized path; the first of a repeat wins.
  const keyOf = (p) => {
    const segs = segsOf(p);
    return segs ? "/" + segs.join("/") : null;
  };
  const order = new Map();
  if (Array.isArray(files)) {
    files.forEach((f, i) => {
      const key = f ? keyOf(f.path) : null;
      if (key !== null && !order.has(key)) order.set(key, i);
    });
  }
  const ranked = [];
  for (const row of byPath.values()) {
    const key = keyOf(row.path);
    const at = key === null ? undefined : order.get(key);
    if (at !== undefined) row.mine = true;
    ranked.push({ row, at: at === undefined ? -1 : at });
  }
  ranked.sort((x, y) => {
    if ((x.at >= 0) !== (y.at >= 0)) return x.at >= 0 ? -1 : 1;
    if (x.at >= 0) return x.at - y.at;
    return x.row.path < y.row.path ? -1 : x.row.path > y.row.path ? 1 : 0;
  });
  return { rows: ranked.slice(0, CHANGES_MAX).map((x) => x.row), more: Math.max(0, ranked.length - CHANGES_MAX) };
}

// --- the section around the rows: scope, base, totals, and the view --------------------------------

// changesScopeParse: the remembered scopes from their stored text, as a Map(session id -> "head" |
// "branch"). The store is user-editable text, so a value that is not a scope, a body that is not
// an object and a text that is not JSON are all dropped, and only the newest CHANGES_SCOPE_CAP
// sessions stay. A Map, not an object: a session id such as "__proto__" is just a key.
function changesScopeParse(raw) {
  const out = new Map();
  let obj = null;
  try { obj = JSON.parse(raw); } catch (_) { return out; }
  if (!obj || typeof obj !== "object" || Array.isArray(obj)) return out;
  for (const k of Object.keys(obj)) if (obj[k] === "head" || obj[k] === "branch") out.set(k, obj[k]);
  while (out.size > CHANGES_SCOPE_CAP) out.delete(out.keys().next().value);
  return out;
}

// changesScopePut: a NEW map with `sid`'s choice set and moved last (so the oldest is the first one
// out at the cap). Nothing is written to `map`, so a caller can tell a change by identity. A value
// that is not a scope changes nothing (the same map back). No session (a pane of files) is "".
function changesScopePut(map, sid, scope) {
  if (scope !== "head" && scope !== "branch") return map;
  const out = new Map(map || []);
  const key = typeof sid === "string" ? sid : "";
  out.delete(key);
  out.set(key, scope);
  while (out.size > CHANGES_SCOPE_CAP) out.delete(out.keys().next().value);
  return out;
}

// changesScopeSave: the text changesScopeParse reads back.
function changesScopeSave(map) {
  return JSON.stringify(Object.fromEntries(map || []));
}

// changesScopeFor: the scope the section shows for a session. `base` is the /api/git/base reply for
// its root. Without an ok base there is nothing to compare Branch with, so it is Uncommitted whatever
// was remembered (the toggle is hidden too). With one, the user's remembered pick wins; with none the
// default is Branch when HEAD is ahead of the merge base, else Uncommitted.
function changesScopeFor(map, sid, base) {
  if (!base || base.state !== "ok") return "head";
  const have = map && typeof map.get === "function" ? map.get(typeof sid === "string" ? sid : "") : undefined;
  if (have === "head" || have === "branch") return have;
  return base.ahead === true ? "branch" : "head";
}

// changesBranchOf: the checked-out branch a status names, "" for a detached HEAD or a reply that does not say.
function changesBranchOf(status) {
  return status && typeof status.branch === "string" ? status.branch : "";
}

// changesBaseStale: must the base be asked for again? `prev` is the last record, {root, head, branch, reply, at,
// failed}. Yes for a new root, a new HEAD (a commit can move "ahead") or a new branch (`git switch -c` leaves HEAD where it
// was, and the branch row names the branch), and for a failure (the record says failed, or the reply is null or
// "unavailable") once CHANGES_BASE_RETRY_MS has passed, so a flaky container does not hide Branch for good and does
// not cost a request per status either. A settled answer (ok, nobase, notrepo) is kept until the root, HEAD or branch moves.
function changesBaseStale(prev, root, head, branch, now) {
  if (!prev || prev.root !== root || prev.head !== head || prev.branch !== branch) return true;
  const r = prev.reply;
  if (!prev.failed && r && r.state !== "unavailable") return false;
  return !(now - prev.at < CHANGES_BASE_RETRY_MS);
}

// changesBaseRecord: the record to keep after asking for the base of (root, head, branch) at `now`; `reply` is the
// answer, null when the request failed. A failure (null, or the server's "unavailable") does not take away what
// the page already knew about this root: the last ok reply stays (marked failed, so it is asked for again after
// CHANGES_BASE_RETRY_MS), or the toggle would vanish and a Branch pick would read as Uncommitted for a moment's flake.
// Any other answer replaces it, and so does a failure with nothing good to keep.
function changesBaseRecord(prev, root, head, branch, reply, now) {
  const failed = reply === null || (!!reply && reply.state === "unavailable");
  const keep = failed && !!prev && prev.root === root && !!prev.reply && prev.reply.state === "ok";
  return { root, head, branch, reply: keep ? prev.reply : reply, at: now, failed };
}

// changesTotals: {add, del}, the sum of the counts numstat knows over the files under the tree root
// (what the branch row says: "+ΣA −ΣD"); null when the reply is not an ok one, so the row says no
// counts rather than +0 −0. A file with unknown counts adds nothing: the sum is of what git said.
function changesTotals(numstat, root) {
  if (!numstat || numstat.state !== "ok" || !Array.isArray(numstat.entries) || typeof root !== "string" || root === "") return null;
  const placeOf = typeof gitPlace === "function" ? gitPlace : require("./gitstatus.js").gitPlace;
  let add = 0;
  let del = 0;
  for (const f of changesNumstatFiles(numstat, root.replace(/\/+$/, ""), placeOf)) {
    if (f.add === null) continue;
    add += f.add;
    del += f.del;
  }
  return { add, del };
}

// changesView: what the section shows, from the records dockchanges.js keeps. It is the one place that
// decides, so the page only paints it. `root` is the tree root; `status` {root, status} the last
// status reply and the root it was for; `numstat` {root, scope, reply} the last numstat answer (reply
// null: the request failed); `base` {root, head, reply, at} the last base answer; `prefs` the remembered
// scopes (a Map); `sid` the session; `files` its Activity files.
//   {kind: "none", count: 0}                      nothing to draw (no root, no reply for this root, not a repository)
//   {kind: "note", text, title, count: 0}         one calm line: status unavailable, too many, Branch waiting or failed
//                                                 (a Branch note also carries scope and showScope: the toggle stays)
//   {kind: "rows", scope, showScope, rows, more, count, branch}   (count: the rows git has answered for, plus more)
// `statusAt` (gitStatusAt: when the status held was asked for) reaches changesRows, which makes the provisional rows.
// A reply is used only for the root (and scope) it was asked for: a late one for the root before is
// not this section's. Uncommitted lists status at once and fills the counts in when numstat lands;
// Branch lists numstat's files (status cannot say what the branch changed), so until that answer is
// here it is a note, and a failed one is a note too, never an empty list that looks like "no changes".
function changesView(o) {
  const none = { kind: "none", count: 0 };
  const note = (text, title) => ({ kind: "note", text, title: typeof title === "string" ? title : "", count: 0 });
  const root = o && typeof o.root === "string" ? o.root : "";
  const cur = o && o.status;
  if (root === "" || !cur || cur.root !== root || !cur.status) return none;
  const st = cur.status;
  if (st.state === "toomany") return note(CHANGES_TOOMANY, "");
  if (st.state === "unavailable") return note("Status unavailable", st.reason);
  if (st.state !== "ok") return none;
  const base = o.base && o.base.root === root ? o.base.reply : null;
  const scope = changesScopeFor(o.prefs, o.sid, base);
  const rec = o.numstat && o.numstat.root === root && o.numstat.scope === scope ? o.numstat : null;
  const reply = rec ? rec.reply : null;
  if (scope === "branch") {
    // A note in Branch scope keeps the toggle (scope, showScope): the way back to Uncommitted must stay on screen.
    const waits = (text, title) => Object.assign(note(text, title), { scope, showScope: true });
    if (!rec) return waits("Reading branch changes…", "");
    if (reply && reply.state === "toomany") return waits(CHANGES_TOOMANY, "");
    if (!reply || reply.state !== "ok") return waits("Branch changes unavailable", reply ? reply.reason : "");
  }
  const { rows, more } = changesRows({ scope, status: st, numstat: reply, files: o.files, root, statusAt: o.statusAt });
  let branch = null;
  if (scope === "branch") {
    const t = changesTotals(reply, root);
    branch = {
      from: typeof base.branch === "string" && base.branch !== "" ? base.branch : "(detached)",
      to: typeof base.baseName === "string" ? base.baseName : "",
      add: t ? t.add : null,
      del: t ? t.del : null,
    };
  }
  // The count is git's: a provisional row is listed (Waiting for git) but is not a change until git says so, so the header and the
  // strip never say "1 changed" for a file that turns out to be clean.
  const count = rows.reduce((n, r) => n + (r.provisional ? 0 : 1), 0) + more;
  return { kind: "rows", scope, showScope: !!base && base.state === "ok", rows, more, count, branch };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    CHANGES_MAX, CHANGES_SCOPE_KEY, changesRows, changesScopeParse, changesScopePut, changesScopeSave, changesScopeFor,
    changesBranchOf, changesBaseStale, changesBaseRecord, changesTotals, changesView,
  };
}
