"use strict";

// Pure helpers for the viewer's "Changes vs HEAD" view (no DOM, node-testable).
//
// The server sends one file's unified diff (POST /api/git/diff). parseUnifiedDiff
// turns it into rows; viewer-tab.js renders each row through the same `.vl`
// machinery as the read view, so line numbers, chunking, find and wrap all work.

const VD_HUNK = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

// parseUnifiedDiff(text) -> [{t, text, n}]
//   t     "meta" | "hunk" | "add" | "del" | "ctx"
//   text  the line without its +/-/space column for add/del/ctx; the whole line
//         for hunk and meta
//   n     the NEW-file line number for add and ctx, null for everything else (a
//         removed line has no number in the file as it is now)
//
// Inside a hunk the counts from `@@ -a,b +c,d @@` say how many old and new lines
// are still to come, so a removed line that reads "-- x" (shown "--- x") is a
// removal, not a file header, and a second file's header after the counts run out
// is meta. Everything before the first hunk is meta (diff --git, index, ---/+++,
// new file mode, ...), as is the "\ No newline at end of file" marker.
function parseUnifiedDiff(text) {
  if (!text) return [];
  const lines = String(text).split("\n");
  if (lines[lines.length - 1] === "") lines.pop(); // the final newline ends a line, it does not start one
  const rows = [];
  let inHunk = false;
  let oldLeft = 0;
  let newLeft = 0;
  let nextNew = 0;
  for (const line of lines) {
    if (line.startsWith("@@")) {
      const m = VD_HUNK.exec(line);
      if (m) {
        oldLeft = m[2] === undefined ? 1 : Number(m[2]);
        newLeft = m[4] === undefined ? 1 : Number(m[4]);
        nextNew = Number(m[3]);
      } else { oldLeft = Infinity; newLeft = Infinity; } // unreadable header: take the body as it comes
      inHunk = true;
      rows.push({ t: "hunk", text: line, n: null });
      continue;
    }
    if (inHunk && line.charCodeAt(0) === 92) { // "\"
      rows.push({ t: "meta", text: line, n: null });
      continue;
    }
    if (inHunk && (oldLeft > 0 || newLeft > 0)) {
      const c = line.charAt(0);
      if (c === "+") { newLeft--; rows.push({ t: "add", text: line.slice(1), n: nextNew++ }); continue; }
      if (c === "-") { oldLeft--; rows.push({ t: "del", text: line.slice(1), n: null }); continue; }
      if (c === " " || line === "") { // some tools strip the space off an empty context line
        oldLeft--; newLeft--;
        rows.push({ t: "ctx", text: line.slice(1), n: nextNew++ });
        continue;
      }
    }
    inHunk = false;
    rows.push({ t: "meta", text: line, n: null });
  }
  return rows;
}

// diffSign: the sign column's text. A true minus (U+2212), the same glyph the
// toolbar's "A−" uses and so known to be in the fonts.
function diffSign(t) {
  return t === "add" ? "+" : t === "del" ? "−" : "";
}

// diffMaxLine: the highest new-file line number on show (0 when there is none).
function diffMaxLine(rows) {
  let max = 0;
  for (const r of rows) if (r.n !== null && r.n > max) max = r.n;
  return max;
}

// diffRowHtml(rows, hl, esc) -> one html string per row.
//
// Code rows (add/del/ctx) are highlighted as two source streams, because that is
// what they are: the new side (ctx + add) and the old side (ctx + del). One mixed
// stream would let a comment opened on a removed line colour the added lines
// after it. A context row takes its html from the new-side stream. `hl(text)` is
// the highlighter (html for the whole stream, a "\n" between lines); it may throw
// or lose lines, in which case that stream falls back to plain escaped text.
// Hunk and meta rows are escaped plain.
function diffRowHtml(rows, hl, esc) {
  const split = typeof splitHighlightedLines === "function" ? splitHighlightedLines : require("./viewerlines.js").splitHighlightedLines;
  const out = new Array(rows.length);
  const stream = (isMember) => {
    const idx = [];
    const texts = [];
    rows.forEach((r, i) => { if (isMember(r.t)) { idx.push(i); texts.push(r.text); } });
    if (!idx.length) return { idx, html: [] };
    let html = null;
    if (typeof hl === "function") {
      try {
        const got = hl(texts.join("\n"));
        if (typeof got === "string") {
          const parts = split(got);
          // splitHighlightedLines drops the empty segment after a final newline, so a
          // stream that ends in blank lines comes back short by exactly those.
          while (parts.length < texts.length && texts[parts.length] === "") parts.push("");
          if (parts.length === texts.length) html = parts;
        }
      } catch (_) { /* plain below */ }
    }
    return { idx, html: html || texts.map(esc) };
  };
  const put = (s) => s.idx.forEach((ri, k) => { out[ri] = s.html[k]; });
  const newSide = stream((t) => t === "ctx" || t === "add");
  const delSide = stream((t) => t === "ctx" || t === "del");
  put(delSide);
  put(newSide); // overwrites ctx rows: the new side owns them
  rows.forEach((r, i) => { if (r.t === "hunk" || r.t === "meta") out[i] = esc(r.text); });
  return out;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { parseUnifiedDiff, diffSign, diffMaxLine, diffRowHtml };
}
