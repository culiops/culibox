// Pure line helpers for the viewer's read view (no DOM, node-testable).
//
// highlight.js returns ONE html string for the whole file, with <span> elements
// that cross line breaks (block comments, raw strings). The read view wants one
// element per line (a gutter number beside each), so the string is cut at every
// newline, and every span still open at the cut is closed on that line and
// reopened on the next. hljs emits only <span ...> and </span> as tags and
// entity-escapes every other "<" in the source, so those two are the only tags
// recognised here.

const VL_TOKEN = /<span\b[^>]*>|<\/span>|\r?\n/g;

// splitHighlightedLines cuts highlighted html into one html string per source
// line, in a single linear scan. One "\r" before a "\n" is dropped (CRLF files),
// and the empty segment after a final newline is not a line.
function splitHighlightedLines(html) {
  const lines = [];
  const open = []; // the opening tags of the spans still open, outermost first
  let cur = "";
  let hasText = false; // anything but tags on this line yet?
  let last = 0;
  VL_TOKEN.lastIndex = 0;
  let m;
  while ((m = VL_TOKEN.exec(html)) !== null) {
    if (m.index > last) { cur += html.slice(last, m.index); hasText = true; }
    last = VL_TOKEN.lastIndex;
    const tok = m[0];
    if (tok === "</span>") { open.pop(); cur += tok; }
    else if (tok.charCodeAt(0) === 60) { open.push(tok); cur += tok; } // "<span ...>"
    else { // a line break
      lines.push(cur + "</span>".repeat(open.length));
      cur = open.join("");
      hasText = false;
    }
  }
  if (last < html.length) { cur += html.slice(last); hasText = true; }
  if (hasText) lines.push(cur);
  return lines;
}

// countSourceLines counts lines the way a person does: a final newline ends the
// last line, it does not start another. CRLF is one break.
function countSourceLines(text) {
  if (!text) return 0;
  let n = 0;
  let i = -1;
  while ((i = text.indexOf("\n", i + 1)) !== -1) n++;
  return text.charCodeAt(text.length - 1) === 10 ? n : n + 1;
}

// clampLine pins a requested 1-based line into 1..total and says whether it had
// to. total 0 (nothing to show) clamps to line 1.
function clampLine(n, total) {
  const max = Math.max(1, total | 0);
  if (!Number.isFinite(n) || n < 1) return { line: 1, clamped: true };
  const line = Math.floor(n);
  if (line > max) return { line: max, clamped: true };
  return { line, clamped: line !== n };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { splitHighlightedLines, countSourceLines, clampLine };
}
