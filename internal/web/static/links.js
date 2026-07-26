"use strict";

// Pure link tokenizing for terminal output: no DOM, no globals mutated, and the
// only xterm contact is a duck-typed `term` with .cols and .buffer.active.getLine.
// That is what makes the rule that matters — a URL outranks a file path over the
// same characters — something a test can pin down instead of something to trust.
//
// Loaded as a classic script, so every top-level name here is a shared global.
// Constants are LINK_-prefixed so a later file cannot collide: two classic scripts
// declaring the same top-level const is a SyntaxError that blanks the whole page.

// The scheme is mandatory. A bare `www.example.com` is deliberately NOT a link: it
// is indistinguishable from a relative file path and would fight LINK_PATH_RE for
// the same characters, which is the very bug this file exists to fix.
const LINK_URL_RE = /https?:\/\/[^\s<>"'`]+/gi;

// Copied verbatim from the original inline provider. It works; the conflict with
// URLs is resolved by precedence below, not by making this regex cleverer.
const LINK_PATH_RE = /(?:\/|~\/|\.{1,2}\/|[\w.\-]+\/)[\w./\-]+\.[A-Za-z0-9]{1,8}/g;

const LINK_TRAILING_PUNCT = /[.,;:!?]+$/;
const LINK_CLOSERS = { ")": "(", "]": "[", "}": "{" };

function linkCountChar(s, c) {
  let n = 0;
  for (let i = 0; i < s.length; i++) if (s[i] === c) n++;
  return n;
}

// trimUrl strips characters that belong to the surrounding prose rather than to
// the URL. A closing bracket is only dropped when it is unbalanced, so a URL that
// genuinely ends in one — `.../Foo_(bar)` — survives.
function trimUrl(url) {
  let out = url;
  for (;;) {
    const before = out;
    out = out.replace(LINK_TRAILING_PUNCT, "");
    const last = out[out.length - 1];
    const opener = LINK_CLOSERS[last];
    if (opener && linkCountChar(out, last) > linkCountChar(out, opener)) out = out.slice(0, -1);
    if (out === before) return out;
  }
}

// scanLinks returns every clickable token in `text`, sorted by position and never
// overlapping. Two passes: URLs claim their span first, then any path match that
// intersects a claimed span is dropped. That single rule is the whole bug fix —
// `culiops.dev/learn/x.html` sits inside a URL and never becomes a path again.
function scanLinks(text) {
  const out = [];
  const claimed = [];
  let m;

  LINK_URL_RE.lastIndex = 0;
  while ((m = LINK_URL_RE.exec(text)) !== null) {
    const trimmed = trimUrl(m[0]);
    if (!trimmed) continue;
    const start = m.index;
    const end = start + trimmed.length;
    claimed.push([start, end]);
    out.push({ start, end, kind: "url", text: trimmed });
  }

  LINK_PATH_RE.lastIndex = 0;
  while ((m = LINK_PATH_RE.exec(text)) !== null) {
    const start = m.index;
    const end = start + m[0].length;
    if (claimed.some(([s, e]) => start < e && s < end)) continue;
    out.push({ start, end, kind: "path", text: m[0] });
  }

  out.sort((a, b) => a.start - b.start);
  return out;
}

// The browser is never on the same machine as the terminal: this console is
// reached over Tailscale, so a `localhost:3030` printed by a dev server points at
// the viewer's own machine and reliably fails. Rewriting the host to whatever
// hostname the console is being viewed on is what the user actually meant.
// URL.hostname keeps the brackets on an IPv6 literal, so both spellings are listed.
const LINK_LOCAL_HOSTS = new Set(["localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]"]);

function rewriteLocalhost(raw, pageHost) {
  if (!pageHost || LINK_LOCAL_HOSTS.has(pageHost.toLowerCase())) return raw;
  let u;
  try {
    u = new URL(raw);
  } catch (_) {
    return raw;
  }
  if (!u.hostname || !LINK_LOCAL_HOSTS.has(u.hostname.toLowerCase())) return raw;
  u.hostname = pageHost;
  return u.toString();
}

// A token that wraps across rows used to linkify as a fragment, so a long path in a
// narrow pane opened a truncated path. Joining the wrapped rows fixes that for
// paths and URLs at once. 50 rows bounds the cost of a pathological line.
const LINK_MAX_WRAP_ROWS = 50;

// readLogicalLine joins the wrapped rows around 1-based row `y` into one string,
// and reports where each row's text begins (rowStarts). It does NOT assume a row
// translates to exactly `cols` characters — it does not for double-width or
// combining characters — which is why callers use rowStarts instead of dividing.
function readLogicalLine(term, y, maxRows) {
  const max = maxRows || LINK_MAX_WRAP_ROWS;
  const buf = term.buffer.active;
  const cols = term.cols;

  // Walk up while the current row is a continuation of the one above. The `> 1`
  // bound is load-bearing: the buffer is a circular list, so getLine(-1) returns a
  // real but unrelated evicted row rather than undefined, and row 1 is routinely
  // marked isWrapped once the ring has wrapped.
  let startY = y;
  for (let i = 1; i < max && startY > 1; i++) {
    const cur = buf.getLine(startY - 1);
    if (!cur || !cur.isWrapped) break;
    startY--;
  }

  let text = "";
  const rowStarts = [];
  let row = startY;
  for (let i = 0; i < max; i++) {
    const line = buf.getLine(row - 1);
    if (!line) break;
    rowStarts.push(text.length);
    // Clamp to the terminal's CURRENT width. A BufferLine keeps its allocated
    // width after the terminal shrinks (a 51-column terminal was observed holding
    // 138-wide lines), and an unclamped translateToString returns that stale width
    // space-padded — which splices a gap into the middle of the joined line and
    // truncates any token spanning the wrap at that gap.
    text += line.translateToString(false, 0, cols);
    const next = buf.getLine(row);
    if (!next || !next.isWrapped) break;
    row++;
  }
  return { text, startY, cols, rowStarts };
}

// cellFromIndex maps an index in the joined text to an absolute buffer cell. The
// row is always exact. The column can drift inside a row containing double-width or
// combining characters, because the public buffer API exposes no cell index for a
// translated offset; it is clamped to the terminal width so a link can never point
// off-row.
function cellFromIndex(index, rowStarts, startY, cols) {
  let r = 0;
  while (r + 1 < rowStarts.length && rowStarts[r + 1] <= index) r++;
  const x = Math.min(cols, index - rowStarts[r] + 1);
  return { x: Math.max(1, x), y: startY + r };
}

// linksForRow is what the xterm provider asks for. The final filter is required by
// xterm's contract: Linkifier._removeIntersectingLinks(y, ...) assumes every link
// returned for row y intersects row y, and treats one lying above it as covering
// columns 0..end.x OF row y — which then evicts a real link at those columns.
function linksForRow(term, y, maxRows) {
  const { text, startY, cols, rowStarts } = readLogicalLine(term, y, maxRows);
  return scanLinks(text)
    .map((tok) => ({
      range: {
        start: cellFromIndex(tok.start, rowStarts, startY, cols),
        end: cellFromIndex(tok.end - 1, rowStarts, startY, cols),
      },
      text: tok.text,
      kind: tok.kind,
    }))
    .filter((l) => l.range.start.y <= y && l.range.end.y >= y);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    scanLinks, trimUrl, rewriteLocalhost, readLogicalLine, cellFromIndex, linksForRow,
  };
}
