"use strict";

// The line blame's pure parts: reading a POST /api/git/blame answer for a buffer line, the words for when, the
// links to a commit and a pull request, and the two layout decisions (inline or status bar, header form).
// DOM-free so `node --test` reaches it; editorblame.js draws with it.
//
// The answer (gitblame.go): {state, version, last, commits: [{sha, author, email, time, summary, ref, boundary}],
// lines: [[commit index, count], ...], remote: {kind, base}}. Every string is the server's cleaned text, still
// untrusted: it is only ever set as textContent.

// The hosts a link may point at, with the kind the server names them by.
const BL_HOSTS = { github: "github.com", gitlab: "gitlab.com", bitbucket: "bitbucket.org" };
const BL_SEG = "[A-Za-z0-9._-]{1,100}";
const BL_SHA = /^[0-9a-f]{40}(?:[0-9a-f]{24})?$/;
// The longest summary the line text carries; the card shows it whole.
const BL_LINE_SUMMARY = 60;

// blameIndex: the commit index of every blamed line, from the answer's runs; null when the runs or indices do
// not add up (the client then shows no line blame rather than a wrong one).
function blameIndex(res) {
  if (!res || !Array.isArray(res.commits) || !Array.isArray(res.lines)) return null;
  let n = 0;
  for (const run of res.lines) {
    if (!Array.isArray(run) || !Number.isInteger(run[0]) || !Number.isInteger(run[1])) return null;
    if (run[0] < 0 || run[0] >= res.commits.length || run[1] < 1) return null;
    n += run[1];
  }
  const idx = new Int32Array(n);
  let at = 0;
  for (const [c, count] of res.lines) { idx.fill(c, at, at + count); at += count; }
  return idx;
}

function blameZero(sha) { return typeof sha === "string" && /^0+$/.test(sha); }

// blameAt: the blame of buffer line `line` (1-based). map (lineMap of the blamed text against the buffer, or null
// when they are the same) turns it into the blamed line; a line with none, or the all-zero commit, is
// {commit: null, uncommitted: true}. null past the end.
function blameAt(res, idx, map, line) {
  if (!idx) return null;
  let old = line;
  if (map) {
    if (line < 1 || line > map.length) return null;
    old = map[line - 1];
    if (old === 0) return { commit: null, uncommitted: true };
  }
  if (old < 1 || old > idx.length) return null;
  const c = res.commits[idx[old - 1]];
  if (!c || blameZero(c.sha)) return { commit: null, uncommitted: true };
  return { commit: c, uncommitted: false };
}

// blameAgo: how long ago unix second `sec` was at unix ms `nowMs`, long ("3 days ago") and short ("3d").
function blameAgo(sec, nowMs) {
  const d = Math.max(0, Math.floor(nowMs / 1000 - sec));
  const steps = [[31536000, "year", "y"], [2592000, "month", "mo"], [86400, "day", "d"], [3600, "hour", "h"], [60, "minute", "m"]];
  for (const [size, word, s] of steps) {
    if (d >= size) {
      const n = Math.floor(d / size);
      return { long: n + " " + word + (n === 1 ? "" : "s") + " ago", short: n + s };
    }
  }
  return { long: "just now", short: "now" };
}

// blameLineText: what the current line shows, "Author, 3 days ago · summary" (summary cut), or "Not committed yet".
function blameLineText(c, nowMs) {
  if (!c) return "Not committed yet";
  let sum = String(c.summary || "");
  if (sum.length > BL_LINE_SUMMARY) sum = sum.slice(0, BL_LINE_SUMMARY - 1) + "…";
  return c.author + ", " + blameAgo(c.time, nowMs).long + (sum ? " · " + sum : "");
}

// blameBase: the remote's base when it is exactly an https page of its kind's host with plain path segments
// (GitHub and Bitbucket owner/repo, GitLab group/…/project), else "". The server built it the same way; it is
// checked again here because it is still data from a repository.
function blameBase(remote) {
  if (!remote || typeof remote.base !== "string" || !Object.prototype.hasOwnProperty.call(BL_HOSTS, remote.kind)) return "";
  const host = BL_HOSTS[remote.kind].replace(/\./g, "\\.");
  const path = remote.kind === "gitlab" ? BL_SEG + "(?:/" + BL_SEG + "){1,19}" : BL_SEG + "/" + BL_SEG;
  if (!new RegExp("^https://" + host + "/" + path + "$").test(remote.base)) return "";
  if (remote.base.split("/").slice(3).some((s) => s === "." || s === "..")) return "";
  return remote.base;
}

// blameCommitUrl: the commit's page on the remote's host, or "" (no remote, an unknown host, an id that is not one).
function blameCommitUrl(remote, sha) {
  const base = blameBase(remote);
  if (!base || typeof sha !== "string" || !BL_SHA.test(sha) || blameZero(sha)) return "";
  const path = { github: "/commit/", gitlab: "/-/commit/", bitbucket: "/commits/" }[remote.kind];
  return base + path + sha;
}

// blamePrUrl: the pull or merge request a summary names ("#12" GitHub and Bitbucket, "!12" GitLab, whose "#12" is an
// issue), or "".
function blamePrUrl(remote, ref) {
  const base = blameBase(remote);
  const m = typeof ref === "string" ? /^([#!])([1-9][0-9]{0,8})$/.exec(ref) : null;
  if (!base || !m) return "";
  const want = remote.kind === "gitlab" ? "!" : "#";
  if (m[1] !== want) return "";
  const path = { github: "/pull/", gitlab: "/-/merge_requests/", bitbucket: "/pull-requests/" }[remote.kind];
  return base + path + m[2];
}

// blameFits: whether inline text of textWidth px, gap px after the line's end, stays inside the editor's content
// width. endLeft is the end's x as Monaco reports it (relative to the scrolled view); the text extends the line, so
// what must fit is its absolute end, or the line grows a horizontal scrollbar (anti-goal A5).
function blameFits({ endLeft, scrollLeft, textWidth, contentWidth, gap }) {
  const vals = [endLeft, scrollLeft, textWidth, contentWidth, gap];
  if (!vals.every(Number.isFinite)) return false;
  return endLeft + scrollLeft + gap + textWidth <= contentWidth;
}

// blameHeaderForm: which form of the header the breadcrumb row has room for: "wide" (icon, author, when), "short"
// (icon, "3d") or "icon". otherWidth is what the row's other parts and gaps take beside the trail; the trail keeps
// trailNeed px (its own width up to a floor) before the header widens.
function blameHeaderForm({ rowWidth, otherWidth, wideWidth, shortWidth, trailNeed }) {
  const room = rowWidth - otherWidth;
  if (room - wideWidth >= trailNeed) return "wide";
  if (room - shortWidth >= trailNeed) return "short";
  return "icon";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { blameIndex, blameAt, blameAgo, blameLineText, blameCommitUrl, blamePrUrl, blameFits, blameHeaderForm, blameZero };
}
