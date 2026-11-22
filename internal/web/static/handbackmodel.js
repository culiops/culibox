"use strict";

// The words and shapes of the "Flagged for your review" panel, its toasts and its
// badges (#346). Pure: no DOM, no globals read, so every surface says the same
// thing from one place. Every string that began in a box (paths, symlink targets,
// details, branch names, the truncated sentence) comes back as plain text:
// callers set textContent, never innerHTML.
//
// This is a pointer to files worth a look, not a verdict. No string here says a
// folder is clean, safe or free of threats: not finding something is not proof.
// Every top-level name carries a handback prefix (the page scripts share one scope).

const HANDBACK_TITLE = "Flagged for your review";
const HANDBACK_NOT_A_SCAN = "This is not a malware scan.";
const HANDBACK_IN_PLACE_LINE = "These files have been on the host since the agent wrote them.";
const HANDBACK_CUT_SHORT = "Not everything was checked — open the review";
// More than this many weaker flags in one directory collapse into one group row.
const HANDBACK_GROUP_OVER = 20;
// The classes that are never grouped: each is a reason to look at that one file.
const HANDBACK_STRONG = ["extension-mismatch", "setuid-setgid", "symlink-outside"];
const HANDBACK_GROUPED = ["executable", "exec-script", "archive"];

const HANDBACK_CLASS_TEXT = {
  "extension-mismatch": "Content does not match its extension",
  "executable": "Executable program",
  "exec-script": "Script with the exec bit set",
  "archive": "Archive",
  "setuid-setgid": "Set-user-ID or set-group-ID bit",
  "symlink-outside": "Symlink pointing outside the folder",
};

// handbackClassText: what one flag class means, in words. An unknown class (a
// newer daemon) comes back as it is.
function handbackClassText(c) {
  const k = String(c == null ? "" : c);
  return Object.prototype.hasOwnProperty.call(HANDBACK_CLASS_TEXT, k) ? HANDBACK_CLASS_TEXT[k] : k;
}

// handbackCountText: "" for none, else "N file(s) flagged for your review".
function handbackCountText(n) {
  if (typeof n !== "number" || !(n > 0)) return "";
  return n === 1 ? "1 file flagged for your review" : n + " files flagged for your review";
}

function handbackEmptyText() {
  return "Nothing flagged. " + HANDBACK_NOT_A_SCAN;
}

// handbackWindowText: what the listed files are. An in-place record reaches back
// to the previous scan of the folder, so it says "since last checked", not
// "since this session started".
function handbackWindowText(record) {
  const r = record || {};
  if (r.kind === "in-place") return "Files created or changed since this folder was last checked.";
  return "Files on " + (r.branch ? "branch " + r.branch : "a branch") + " that the box handed back.";
}

// handbackFooter: the lines under the list. The scan disclaimer always; the
// in-place line for a folder; the cut-short sentence when the scan did not finish.
function handbackFooter(record) {
  const r = record || {};
  const out = [HANDBACK_NOT_A_SCAN];
  if (r.kind === "in-place") out.push(HANDBACK_IN_PLACE_LINE);
  if (typeof r.truncated === "string" && r.truncated !== "") out.push(r.truncated);
  return out;
}

function handbackDirOf(path) {
  const p = String(path == null ? "" : path);
  const i = p.lastIndexOf("/");
  return i < 0 ? "." : i === 0 ? "/" : p.slice(0, i);
}

function handbackFileRow(f) {
  return {
    kind: "file",
    path: String(f.path == null ? "" : f.path),
    classes: Array.isArray(f.classes) ? f.classes.map(String) : [],
    detail: String(f.detail == null ? "" : f.detail),
    sha256: String(f.sha256 == null ? "" : f.sha256),
    hashNote: String(f.hash_note == null ? "" : f.hash_note),
    target: String(f.target == null ? "" : f.target),
  };
}

// handbackRows: a record's flags as panel rows. A flag with an extension
// mismatch, a set-id bit or an outside symlink is always its own row and comes
// first. The rest keep their order, except that a directory with more than 20 of
// them becomes one group row (at the place of its first file) with a count per
// class, so a node_modules does not bury the file that matters.
function handbackRows(record) {
  const flags = record && Array.isArray(record.flags) ? record.flags.filter((f) => f && typeof f === "object") : [];
  const strong = [];
  const weak = [];
  for (const f of flags) {
    const row = handbackFileRow(f);
    (row.classes.some((c) => HANDBACK_STRONG.includes(c)) ? strong : weak).push(row);
  }
  const perDir = new Map();
  for (const row of weak) {
    const d = handbackDirOf(row.path);
    perDir.set(d, (perDir.get(d) || 0) + 1);
  }
  const rows = strong.slice();
  const groups = new Map();
  for (const row of weak) {
    const d = handbackDirOf(row.path);
    if (perDir.get(d) <= HANDBACK_GROUP_OVER) { rows.push(row); continue; }
    let g = groups.get(d);
    if (!g) {
      g = { kind: "group", dir: d, counts: { executable: 0, "exec-script": 0, archive: 0 }, files: [] };
      groups.set(d, g);
      rows.push(g);
    }
    g.files.push(row);
    for (const c of HANDBACK_GROUPED) if (row.classes.includes(c)) g.counts[c]++;
  }
  return rows;
}

// A note is the small summary a session or a return carries: {id, count,
// truncated?}. It matters when it flagged something or when its scan was cut
// short (a count of 0 then is not "nothing flagged").
function handbackNoteMatters(n) {
  return !!n && typeof n === "object" && typeof n.id === "string" && ((n.count > 0) || n.truncated === true);
}

function handbackNoteIds(notes) {
  return (Array.isArray(notes) ? notes : []).filter(handbackNoteMatters).map((n) => n.id);
}

// handbackNoteText: the toast words for some notes: the files flagged, the fact
// that not everything was checked, or both. "" when there is nothing to say.
function handbackNoteText(notes) {
  const list = (Array.isArray(notes) ? notes : []).filter(handbackNoteMatters);
  const count = list.reduce((a, n) => a + (n.count > 0 ? n.count : 0), 0);
  const cut = list.some((n) => n.truncated === true);
  if (count > 0) return handbackCountText(count) + (cut ? " · not everything was checked" : "");
  return cut ? HANDBACK_CUT_SHORT : "";
}

// handbackPending: the records that still need a look: files flagged, or a scan
// that was cut short (the server's own rule; kept here so a stale list is safe).
function handbackPending(records) {
  return (Array.isArray(records) ? records : []).filter((r) => r && typeof r === "object" &&
    ((Array.isArray(r.flags) && r.flags.length > 0) || (typeof r.truncated === "string" && r.truncated !== "")));
}

// handbackSidebarText: the sidebar entry, shown only while something is pending.
// N is the files flagged; when only cut-short scans are pending (no files), it
// is the number of those records, so the entry never says 0.
function handbackSidebarText(records) {
  const pending = handbackPending(records);
  if (!pending.length) return "";
  const files = pending.reduce((a, r) => a + (Array.isArray(r.flags) ? r.flags.length : 0), 0);
  const n = files > 0 ? files : pending.length;
  return n + " flagged for your review";
}

// handbackSelect: which records a surface opens. undefined = every pending one;
// a record id; an array of ids; {cloneOf} or {workspace} for a profile (a profile
// that matches nothing, e.g. through a symlinked folder, falls back to everything
// pending rather than an empty panel).
function handbackSelect(records, sel) {
  const all = handbackPending(records);
  if (sel == null) return all;
  if (typeof sel === "string") return all.filter((r) => r.id === sel);
  if (Array.isArray(sel)) return all.filter((r) => sel.includes(r.id));
  let got = [];
  if (typeof sel.cloneOf === "string" && sel.cloneOf !== "") got = all.filter((r) => r.clone_of === sel.cloneOf);
  else if (typeof sel.workspace === "string" && sel.workspace !== "") got = all.filter((r) => r.workspace === sel.workspace);
  return got.length ? got : all;
}

// handbackProfileSel: the selection a profile's badge opens: its private copy's original, else its folder.
function handbackProfileSel(p) {
  const v = p || {};
  if (typeof v.clone_of === "string" && v.clone_of !== "") return { cloneOf: v.clone_of };
  if (typeof v.default_cwd === "string" && v.default_cwd !== "") return { workspace: v.default_cwd };
  return undefined;
}

// handbackSessionNotes: the notes a session carries: its in-place scan and the
// branches its last return flagged.
function handbackSessionNotes(s) {
  if (!s || typeof s !== "object") return [];
  const out = [];
  if (s.handback) out.push(s.handback);
  if (s.return && Array.isArray(s.return.flagged)) out.push(...s.return.flagged);
  return out.filter(handbackNoteMatters);
}

// handbackSessionText: the row's line. pendingIds (an array or Set of record ids
// still pending, once the list has loaded) hides notes already reviewed; leave
// it out before the list is known.
function handbackSessionText(s, pendingIds) {
  let notes = handbackSessionNotes(s);
  if (pendingIds) {
    const has = (id) => (pendingIds instanceof Set ? pendingIds.has(id) : Array.isArray(pendingIds) && pendingIds.includes(id));
    notes = notes.filter((n) => has(n.id));
  }
  return handbackNoteText(notes);
}

function handbackNoteSig(s, n) {
  return "hb:" + s.id + ":" + n.id + ":" + (n.count > 0 ? n.count : 0) + ":" + (n.truncated === true ? "cut" : "");
}

// handbackNews: a toast for each in-place scan that flagged something (or could
// not finish) and is not in seen yet. Like returnNews: seen is null before the
// first poll, which only seeds; seen comes back pruned to the notes still listed.
// A clone's flagged branches ride on its return and toast through returnNews.
function handbackNews(seen, sessions) {
  const list = (Array.isArray(sessions) ? sessions : []).filter((s) => s && typeof s === "object" && s.id);
  const now = [];
  const fresh = [];
  for (const s of list) {
    if (!handbackNoteMatters(s.handback)) continue;
    const sig = handbackNoteSig(s, s.handback);
    now.push(sig);
    fresh.push({ sig, s });
  }
  if (!Array.isArray(seen)) return { toasts: [], seen: now };
  const known = new Set(seen);
  const toasts = [];
  for (const f of fresh) {
    if (known.has(f.sig)) continue;
    const text = handbackNoteText([f.s.handback]);
    if (text) toasts.push({ text: (f.s.agent || f.s.name || f.s.id) + ": " + text, ids: [f.s.handback.id] });
  }
  return { toasts, seen: now };
}

// handbackDue: whether the pending list is due a refresh: never fetched, or the
// last fetch is older than minMs. Keeps the sidebar entry on the sessions poll's
// beat without a request every three seconds.
function handbackDue(lastAt, now, minMs) {
  return !(lastAt > 0) || now - lastAt >= minMs;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { HANDBACK_TITLE, HANDBACK_GROUP_OVER, handbackClassText, handbackCountText, handbackEmptyText, handbackWindowText, handbackFooter, handbackRows, handbackNoteIds, handbackNoteText, handbackPending, handbackSidebarText, handbackSelect, handbackProfileSel, handbackSessionNotes, handbackSessionText, handbackNews, handbackDue };
}
