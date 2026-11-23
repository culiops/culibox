"use strict";

// The last git status and Changes view of each tree root, kept for the tab's life (sessionStorage), so a reload or a session
// switch paints the tree's marks and the Changes list at once, marked Updating…, until git answers (gitstatus.js,
// dockchanges.js). Nothing acts on what is kept here: it is only drawn.
//
// The store is text anyone with this page's origin can write (a user in DevTools, another tab), so everything read back is
// checked field by field and rebuilt: a path is a string, a letter one of M A U D C, a count a whole number or null, a root
// an absolute path. A record that fails is dropped whole; a row or entry that fails is left out. Pure functions under node
// (gitLastParse, gitLastPut, gitLastSave); the page glue (gitLastGet, gitLastSet) reads the store once and writes it through.

const GITLAST_KEY = "agent-box:gitLast";
const GITLAST_ROOTS = 8; // roots kept; the least recently written is the first one out
const GITLAST_ENTRIES = 500; // a status with more entries keeps no marks for its root
const GITLAST_ROWS = 200; // a view's rows (CHANGES_MAX)
const GITLAST_BYTES = 200 * 1024; // the saved text, at most
const GITLAST_CODES = new Set(["M", "A", "U", "D", "C"]);

const gitLastStr = (x) => typeof x === "string";
const gitLastCount = (x) => x === null || (Number.isInteger(x) && x >= 0);
const gitLastWhole = (x) => Number.isInteger(x) && x >= 0;

// gitLastStatus: a clean copy of a status reply worth keeping (ok or toomany, at most GITLAST_ENTRIES entries), else null.
function gitLastStatus(st) {
  if (!st || typeof st !== "object" || (st.state !== "ok" && st.state !== "toomany")) return null;
  if (!gitLastStr(st.rootRel) || !Array.isArray(st.entries) || st.entries.length > GITLAST_ENTRIES) return null;
  const entries = [];
  for (const e of st.entries) if (e && gitLastStr(e.p) && GITLAST_CODES.has(e.x)) entries.push({ p: e.p, x: e.x });
  const out = { state: st.state, rootRel: st.rootRel, entries };
  if (gitLastStr(st.head)) out.head = st.head;
  if (gitLastStr(st.branch)) out.branch = st.branch;
  if (gitLastStr(st.worktreeOf)) out.worktreeOf = st.worktreeOf; // the root header's chip (dockrootchip.js)
  return out;
}

// gitLastRow: a clean copy of one Changes row, else null.
function gitLastRow(r) {
  if (!r || typeof r !== "object" || !gitLastStr(r.path) || !gitLastStr(r.name) || !gitLastStr(r.dir)) return null;
  const prov = r.provisional === true;
  if (!(GITLAST_CODES.has(r.code) || (prov && r.code === ""))) return null;
  if (!gitLastCount(r.add) || !gitLastCount(r.del)) return null;
  for (const k of ["mine", "folder", "provisional"]) if (typeof r[k] !== "boolean") return null;
  return { path: r.path, name: r.name, dir: r.dir, code: r.code, add: r.add, del: r.del, mine: r.mine, folder: r.folder, provisional: prov };
}

// gitLastView: a clean copy of a changesView of kind "rows", else null.
function gitLastView(v) {
  if (!v || typeof v !== "object" || v.kind !== "rows" || (v.scope !== "head" && v.scope !== "branch")) return null;
  if (typeof v.showScope !== "boolean" || !gitLastWhole(v.more) || !gitLastWhole(v.count)) return null;
  if (!Array.isArray(v.rows) || v.rows.length > GITLAST_ROWS) return null;
  let branch = null;
  if (v.branch !== null && v.branch !== undefined) {
    const b = v.branch;
    if (!b || typeof b !== "object" || !gitLastStr(b.from) || !gitLastStr(b.to) || !gitLastCount(b.add) || !gitLastCount(b.del)) return null;
    branch = { from: b.from, to: b.to, add: b.add, del: b.del };
  }
  const rows = [];
  for (const r of v.rows) { const c = gitLastRow(r); if (c) rows.push(c); }
  return { kind: "rows", scope: v.scope, showScope: v.showScope, rows, more: v.more, count: v.count, branch };
}

// gitLastRoot: a root worth a record: an absolute path of plain length with no NUL.
function gitLastRoot(root) {
  return gitLastStr(root) && root.startsWith("/") && root.length <= 4096 && !root.includes("\u0000");
}

// gitLastParse: the kept records from their stored text, as a Map(root -> {status, view}) in write order, oldest first.
// Text longer than twice what gitLastSave ever writes is not parsed at all.
function gitLastParse(raw) {
  const out = new Map();
  if (!gitLastStr(raw) || raw.length > 2 * GITLAST_BYTES) return out;
  let arr = null;
  try { arr = JSON.parse(raw); } catch (_) { return out; }
  if (!Array.isArray(arr)) return out;
  for (const pair of arr) {
    if (!Array.isArray(pair) || pair.length !== 2 || !gitLastRoot(pair[0])) continue;
    const rec = pair[1];
    if (!rec || typeof rec !== "object" || Array.isArray(rec)) continue;
    out.delete(pair[0]);
    out.set(pair[0], { status: gitLastStatus(rec.status), view: gitLastView(rec.view) });
  }
  while (out.size > GITLAST_ROOTS) out.delete(out.keys().next().value);
  return out;
}

// gitLastPut: a NEW map with `part` ("status" or "view") of `root` set to a clean copy of `value` (null when it is not one
// worth keeping) and the root moved last. A root that is not one, or a part that is neither, changes nothing.
function gitLastPut(map, root, part, value) {
  if (!gitLastRoot(root) || (part !== "status" && part !== "view")) return map;
  const out = new Map(map || []);
  const rec = Object.assign({ status: null, view: null }, out.get(root) || {});
  rec[part] = part === "status" ? gitLastStatus(value) : gitLastView(value);
  out.delete(root);
  out.set(root, rec);
  while (out.size > GITLAST_ROOTS) out.delete(out.keys().next().value);
  return out;
}

// gitLastSave: the text gitLastParse reads back, the oldest roots left out until it is at most GITLAST_BYTES long.
function gitLastSave(map) {
  const arr = [...(map || [])];
  let text = JSON.stringify(arr);
  while (text.length > GITLAST_BYTES && arr.length > 0) {
    arr.shift();
    text = JSON.stringify(arr);
  }
  return text;
}

// --- the page's store ---------------------------------------------------------------------------------

let gitLastMem = null;

function gitLastMap() {
  if (!gitLastMem) {
    let raw = null;
    try { raw = sessionStorage.getItem(GITLAST_KEY); } catch (_) { /* storage off: nothing kept */ }
    gitLastMem = gitLastParse(raw);
  }
  return gitLastMem;
}

// gitLastGet: the record kept for `root`, {status, view} (either may be null), or null.
function gitLastGet(root) {
  return gitLastMap().get(root) || null;
}

// gitLastSet: keep `value` as `root`'s `part`. A storage that is off or full keeps it for this page only.
function gitLastSet(root, part, value) {
  gitLastMem = gitLastPut(gitLastMap(), root, part, value);
  try { sessionStorage.setItem(GITLAST_KEY, gitLastSave(gitLastMem)); } catch (_) { /* this page only */ }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    GITLAST_KEY, GITLAST_ROOTS, GITLAST_ENTRIES, GITLAST_ROWS, GITLAST_BYTES, gitLastParse, gitLastPut, gitLastSave,
  };
}
