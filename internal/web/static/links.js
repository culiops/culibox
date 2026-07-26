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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { scanLinks, trimUrl, rewriteLocalhost };
}
