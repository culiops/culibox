"use strict";

// The editor gutter's line diff: which lines of the buffer were added or changed
// against a base text, and where lines were removed. Pure and DOM-free so
// `node --test` reaches it; editortab.js turns the result into Monaco decorations.
//
// lineDiff(oldText, newText, maxLines = 20000) -> {changes, capped}
//   changes  [{kind: "add" | "mod" | "del", from, to}] in order. Line numbers are
//            1-based in the NEW text, the buffer the gutter is drawn on.
//     add    the new lines from..to have no counterpart in the old text.
//     mod    old lines were replaced by the new lines from..to. A block that
//            replaced N lines with M, N and M both above 0, is one mod over all M
//            new lines (what VS Code's gutter does); a removal that shrinks the
//            block is part of the mod, not a second marker.
//     del    lines were removed. from === to is the line the removal sits in front
//            of, so it is the first line after the removal. Removed at the very end
//            of the text, that is one past the last line (lastLine + 1); the gutter
//            clamps it to the last line when it draws the marker.
//   capped   true when the diff was not computed, for two reasons that look alike
//            to the caller: either text is too big (see below), or the two differ by
//            more than LD_MAX_EDITS lines, so finding the minimal difference would
//            take more work than a keystroke can afford. changes is [] then, and the
//            gutter shows nothing instead of a wrong or late answer.
//
// Lines split the way Monaco splits them (\r\n, \r or \n), so line numbers match
// the editor and a CRLF file against an LF base is not "all changed". Text that
// ends in a newline has an empty last line, as in Monaco; the empty string is no
// lines at all, so a new file is all added and an emptied file is one removal.
//
// The diff is Myers' O(ND) shortest edit script over lines hashed to small
// integers, after trimming the common head and tail (an edit in the middle of a
// big file costs only the middle).

// Over this many characters on either side the diff is skipped. The server's text
// read cap is 2 MiB (fsapi.go), so a file the editor opens is at most this; the
// check is on characters, not only lines, because one 2 MiB line is a single "line"
// that hashing and comparing must still read in full, on every keystroke.
const LD_MAX_CHARS = 2 << 20;

// The most inserted + deleted lines Myers is allowed to search for. The work and
// the memory of the trace grow with the square of this (2000 -> about 16 MB at the
// very worst, once, then garbage), so it is what keeps two unrelated 20000-line
// texts from freezing the page. A whole-file insert or delete never reaches it: those
// are answered without a search.
const LD_MAX_EDITS = 2000;

function ldLines(text) {
  return text === "" ? [] : text.split(/\r\n|\r|\n/);
}

// ldMyers marks, in delA and insB (0/1 per line), the lines of a that are not in
// the shortest edit script's common part and the lines of b that are new. Returns
// false when more than LD_MAX_EDITS edits are needed.
function ldMyers(a, b, delA, insB) {
  const n = a.length;
  const m = b.length;
  const max = Math.min(n + m, LD_MAX_EDITS);
  const off = max + 1; // v is indexed by diagonal k = x - y, shifted so k = -max-1..max+1 fits
  const v = new Int32Array(2 * max + 3); // v[k]: the furthest x reached on diagonal k
  const trace = []; // trace[d]: v as it stood before edit d, for the walk back
  for (let d = 0; d <= max; d++) {
    trace.push(v.slice(off - d, off + d + 1)); // trace[d][k + d] is v[k]
    for (let k = -d; k <= d; k += 2) {
      // Down (an insertion from b) from diagonal k+1 or right (a deletion from a) from k-1, whichever got further.
      let x = (k === -d || (k !== d && v[off + k - 1] < v[off + k + 1])) ? v[off + k + 1] : v[off + k - 1] + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) { x++; y++; }
      v[off + k] = x;
      if (x >= n && y >= m) { ldWalkBack(trace, d, n, m, delA, insB); return true; }
    }
  }
  return false;
}

// ldWalkBack retraces the path ldMyers found, from (n, m) to (0, 0), and flags the
// line each non-diagonal step consumed.
function ldWalkBack(trace, dEnd, n, m, delA, insB) {
  let x = n;
  let y = m;
  for (let d = dEnd; d > 0; d--) {
    const tr = trace[d]; // v after edit d-1
    const k = x - y;
    const down = k === -d || (k !== d && tr[k - 1 + d] < tr[k + 1 + d]);
    const px = down ? tr[k + 1 + d] : tr[k - 1 + d];
    const py = px - (down ? k + 1 : k - 1);
    if (down) insB[py] = 1; else delA[px] = 1;
    x = px;
    y = py;
  }
}

function lineDiff(oldText, newText, maxLines = 20000) {
  const capped = { changes: [], capped: true };
  const o = oldText == null ? "" : String(oldText);
  const t = newText == null ? "" : String(newText);
  if (o.length > LD_MAX_CHARS || t.length > LD_MAX_CHARS) return capped;
  const oldLines = ldLines(o);
  const newLines = ldLines(t);
  if (oldLines.length > maxLines || newLines.length > maxLines) return capped;

  const ids = new Map(); // a line's text -> a small integer, so Myers compares numbers and never re-reads a long line
  const idOf = (line) => {
    let id = ids.get(line);
    if (id === undefined) { id = ids.size; ids.set(line, id); }
    return id;
  };
  const a = Int32Array.from(oldLines, idOf);
  const b = Int32Array.from(newLines, idOf);

  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;
  let aEnd = a.length;
  let bEnd = b.length;
  while (aEnd > head && bEnd > head && a[aEnd - 1] === b[bEnd - 1]) { aEnd--; bEnd--; }

  const delA = new Uint8Array(a.length); // 1 where an old line is gone
  const insB = new Uint8Array(b.length); // 1 where a new line is not in the old text
  if (aEnd === head) insB.fill(1, head, bEnd);
  else if (bEnd === head) delA.fill(1, head, aEnd);
  else if (!ldMyers(a.subarray(head, aEnd), b.subarray(head, bEnd), delA.subarray(head, aEnd), insB.subarray(head, bEnd))) return capped;

  // The lines neither array flags are the common part, in order, so they pair up one to one; anything
  // between two pairs is one block, and its shape names it.
  const changes = [];
  let i = 0;
  let j = 0;
  while (i < a.length || j < b.length) {
    if (!(i < a.length && delA[i]) && !(j < b.length && insB[j])) { i++; j++; continue; }
    const start = j;
    let removed = 0;
    while ((i < a.length && delA[i]) || (j < b.length && insB[j])) {
      if (i < a.length && delA[i]) { i++; removed++; } else j++;
    }
    const added = j - start;
    if (added && removed) changes.push({ kind: "mod", from: start + 1, to: j });
    else if (added) changes.push({ kind: "add", from: start + 1, to: j });
    else changes.push({ kind: "del", from: start + 1, to: start + 1 });
  }
  return { changes, capped: false };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { lineDiff };
}
