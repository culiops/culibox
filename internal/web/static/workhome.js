"use strict";

// Where a private copy's work goes, and what is not home (spec: work comes
// home). Pure: no DOM, no globals read, so the profile row, the spawn panel and
// the toasts all say the same thing from one place. Box-derived strings (branch
// names, errors) come back as plain text: callers set textContent, never
// innerHTML. Every top-level name is a function with a wh/workHome/notHome/
// returnToast/returnSig prefix, because the page scripts share one scope.

// workHomeSuffix: the line after a private copy's folder. The server omits
// auto_return when it is false (omitempty), so only a true value means on.
// pr: the copy's GitHub binding can push, so work leaves as a pull request;
// auto_return decides whether the box/* branches also come home.
function workHomeSuffix(p, pr) {
  if (!p || typeof p.clone_of !== "string" || p.clone_of === "") return "";
  if (pr) {
    return p.auto_return
      ? " · work comes home as a PR and as box/* branches"
      : " · work goes out as a PR; box/* return is off";
  }
  return p.auto_return
    ? " · work comes home as box/* branches"
    : " · work stays in the copy (auto-return off)";
}

// notHomeCount: how many branches a profile's last return left behind.
function notHomeCount(st) {
  return st && Array.isArray(st.branches) ? st.branches.length : 0;
}

// notHomeLabel: the short count shown on the profile row. An error with no
// branches means the clone could not be checked at all.
function notHomeLabel(st) {
  const n = notHomeCount(st);
  if (n === 0) return st && st.error ? "work not checked" : "";
  return n === 1 ? "1 branch not home" : n + " branches not home";
}

// notHomeReasonText: one line saying why nh did not come home. An unknown
// reason (a newer server) gets the generic line rather than nothing.
function notHomeReasonText(nh) {
  const b = "box/" + ((nh && nh.branch) || "");
  switch (nh && nh.reason) {
    case "off": return b + " was not brought home: auto-return is off for this profile";
    case "not-ff": return b + " on the host has commits this branch lacks";
    case "checked-out": return b + " is checked out in your repo, so it did not move";
    case "refused": return "the host refused the history of " + b;
    case "too-large": return b + " is too large to bring home automatically";
    case "name": return b + " is not a valid branch name for the host";
    case "deferred": return b + " was not tried in this run";
    case "failed": return b + " could not be brought home";
    default: return b + " was not brought home";
  }
}

// notHomeTitle: the reason lines behind the short count, one per branch, for a
// hover title. An error with no branches gets the error itself.
function notHomeTitle(st) {
  if (!st) return "";
  const lines = Array.isArray(st.branches) ? st.branches.map(notHomeReasonText) : [];
  if (!lines.length && st.error) lines.push(st.error);
  return lines.join("\n");
}

// notHomeFix: the action for nh's reason. Commands are backticked; -base is
// shown only when the base is not main (an empty base means main).
function notHomeFix(profile, base, nh, desktop) {
  const branch = (nh && nh.branch) || "";
  const B = base && base !== "main" ? "-base " + base + " " : "";
  const pull = "`culibox clone fetch " + B + profile + " " + branch + "`";
  switch (nh && nh.reason) {
    case "off": return pull;
    case "not-ff": return pull + " overwrites box/" + branch;
    case "checked-out": return "switch your repo off box/" + branch + "; it comes home at the next exit or " + (desktop ? "start" : "spawn");
    case "refused":
      return "rebase it in the box onto origin/" + (base || "main") + "; or, after checking it, `culibox clone fetch -allow-old-history " + B + profile + " " + branch + "`";
    case "name": return "rename the branch in the box";
    default: return 'use "Bring work home now", or ' + pull;
  }
}

// workHomeHb: a function of handbackmodel.js (the page loads it first; a node test
// of this file alone requires it).
function workHomeHb(name) {
  return typeof globalThis[name] === "function" ? globalThis[name] : require("./handbackmodel.js")[name];
}

// returnToastText: what one return says, or "" when there is nothing to say.
// A skipped run attempted nothing, so it says nothing. Files flagged for review
// (#346) end the line, and a cut-short scan says so instead of saying nothing.
function returnToastText(profile, sum) {
  if (!sum || sum.skipped) return "";
  const home = Array.isArray(sum.home) ? sum.home : [];
  const notHome = Array.isArray(sum.not_home) ? sum.not_home : [];
  const parts = [];
  if (home.length) parts.push(home.join(", ") + " came home");
  if (notHome.length) parts.push(notHome.length === 1 ? "1 branch not home" : notHome.length + " branches not home");
  if (sum.error) parts.push("work not checked");
  const flagged = workHomeHb("handbackNoteText")(sum.flagged);
  if (flagged) parts.push(flagged);
  if (!parts.length) {
    if (sum.trigger !== "now") return "";
    parts.push("all work is home");
  }
  return profile + ": " + parts.join(" · ");
}

// returnSig: the dedupe key for one profile's last return.
function returnSig(key, at) {
  return key + "@" + at;
}

// notHomeNews: a toast for each profile whose last return left branches behind
// and whose sig is not in seen. seen comes back pruned to the profiles that are
// still not home, so it cannot grow without bound; the input is not changed.
function notHomeNews(seen, profiles) {
  const known = new Set(Array.isArray(seen) ? seen : []);
  const toasts = [];
  const next = [];
  for (const p of Array.isArray(profiles) ? profiles : []) {
    if (!p || notHomeCount(p.not_home) === 0) continue;
    const sig = returnSig(p.name, p.not_home.at);
    next.push(sig);
    if (!known.has(sig)) toasts.push(p.name + ": " + notHomeLabel(p.not_home));
  }
  return { toasts, seen: next };
}

// returnNews: what one sessions poll means for returns. seen is the array of
// session return sigs already announced (null before the first poll, which only
// seeds); cloneIds the clone sessions the previous poll listed. Returns the
// toasts to show, the PROFILE sigs (returnSig(agent, at)) to mark seen for
// notHomeNews so the same return is not announced twice, the next seen/cloneIds
// to keep (links runs in step with toasts: the hand-back record ids each one opens, often none),
// and whether the profile list needs a refresh: a new return landed or
// a clone session left the list (a kill). seen is pruned to the sessions still
// listed, so it cannot grow. Inputs are not changed.
function returnNews(seen, cloneIds, sessions) {
  const list = Array.isArray(sessions) ? sessions : [];
  const nextClones = list.filter((s) => s && s.clone_of).map((s) => s.id);
  const nextSeen = list.filter((s) => s && s.return).map((s) => returnSig(s.id, s.return.at));
  if (!Array.isArray(seen)) return { toasts: [], links: [], sigs: [], seen: nextSeen, cloneIds: nextClones, refetch: false };
  const known = new Set(seen);
  const toasts = [];
  const links = [];
  const sigs = [];
  for (const s of list) {
    if (!s || !s.return || known.has(returnSig(s.id, s.return.at))) continue;
    const text = returnToastText(s.agent, s.return);
    if (text) {
      toasts.push(text);
      links.push(workHomeHb("handbackNoteIds")(s.return.flagged)); // the records the toast opens, if any
    }
    sigs.push(returnSig(s.agent, s.return.at));
  }
  const now = new Set(nextClones);
  const left = (Array.isArray(cloneIds) ? cloneIds : []).some((id) => !now.has(id));
  return { toasts, links, sigs, seen: nextSeen, cloneIds: nextClones, refetch: sigs.length > 0 || left };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { workHomeSuffix, notHomeCount, notHomeLabel, notHomeReasonText, notHomeTitle, notHomeFix, returnToastText, returnSig, notHomeNews, returnNews };
}
