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

// uncommittedLabel: how many files the copy's work tree holds that no commit
// has. n is null/undefined when the server could not count (never shown as
// zero); cut means n is a floor.
function uncommittedLabel(n, cut) {
  if (typeof n !== "number" || !(n > 0)) return "";
  if (cut) return n + "+ files not committed";
  return n === 1 ? "1 file not committed" : n + " files not committed";
}

// notHomeLine: the profile row's / sidebar's whole line: the branch label and
// the uncommitted files, joined with a middle dot, each only when it has
// something to say. st is a profile's not_home view, or the same fields built
// from a session's return.
function notHomeLine(st) {
  if (!st) return "";
  return [notHomeLabel(st), uncommittedLabel(st.uncommitted, st.uncommitted_cut)].filter(Boolean).join(" \u00b7 ");
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
// hover title. An error with no branches gets the error itself, and with
// neither, the uncommitted files label.
function notHomeTitle(st) {
  if (!st) return "";
  const lines = Array.isArray(st.branches) ? st.branches.map(notHomeReasonText) : [];
  if (!lines.length && st.error) lines.push(st.error);
  // A chip with only uncommitted files still says what it counts.
  if (!lines.length) {
    const unc = uncommittedLabel(st.uncommitted, st.uncommitted_cut);
    if (unc) lines.push(unc);
  }
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
// A skipped run attempted nothing, so it says nothing, except before a Host
// session, where silence would read as "all home": "still coming home" when
// the time budget cut it, "work not checked" otherwise. Files flagged for review
// (#346) end the line, and a cut-short scan says so instead of saying nothing.
// An idle return (after each agent turn) only says what came home and what is
// flagged: not-home state lives on the profile and the sidebar, so a turn that
// changes nothing never repeats it. A host return (before a Host spawn) adds
// the files not committed.
function returnToastText(profile, sum) {
  if (!sum) return "";
  if (sum.skipped) {
    if (sum.trigger !== "host") return "";
    return profile + ": " + (sum.skipped === "deferred" ? "still coming home" : "work not checked");
  }
  const home = Array.isArray(sum.home) ? sum.home : [];
  const notHome = Array.isArray(sum.not_home) ? sum.not_home : [];
  const parts = [];
  if (home.length) parts.push(home.join(", ") + " came home");
  const idle = sum.trigger === "idle";
  if (notHome.length && !idle) parts.push(notHome.length === 1 ? "1 branch not home" : notHome.length + " branches not home");
  if (sum.trigger === "host") {
    const unc = uncommittedLabel(sum.uncommitted, sum.uncommitted_cut);
    if (unc) parts.push(unc);
  }
  if (sum.error && !idle) parts.push("work not checked");
  const flagged = workHomeHb("handbackNoteText")(sum.flagged);
  if (flagged) parts.push(flagged);
  if (!parts.length) {
    if (sum.trigger !== "now") return "";
    parts.push("all work is home");
  }
  return profile + ": " + parts.join(" · ");
}

// hostReturnToast: what bringing home the clones of a Host session's repo said,
// one part per clone that has something to say, "" when none does. Each summary
// carries its own profile name.
function hostReturnToast(list) {
  if (!Array.isArray(list)) return "";
  return list.map((sum) => returnToastText(sum && sum.profile, sum)).filter(Boolean).join(" \u00b7 ");
}

// returnSig: the dedupe key for one session's last return.
function returnSig(key, at) {
  return key + "@" + at;
}

// notHomeSig: the dedupe key for one profile's not-home state: its name and the
// sorted branch:reason pairs. Neither the run's time nor the uncommitted count
// is part of it, so a repeated return with the same state is not news.
function notHomeSig(name, st) {
  const list = st && Array.isArray(st.branches) ? st.branches : [];
  return name + "@" + list.map((b) => ((b && b.branch) || "") + ":" + ((b && b.reason) || "")).sort().join(",");
}

// notHomePrefix: the not-home line as the first part of a row's meta or sub-line
// (a clipped line keeps its start, so the count is what stays readable). Either
// part may be empty: the other stands alone.
function notHomePrefix(line, rest) {
  return line && rest ? line + " \u00b7 " + rest : line || rest || "";
}

// notHomeHostSigs: the notHomeSig of every clone whose not-home set the Host spawn
// toast announced (hostReturnToast names it for a summary that was not skipped
// and has branches not home), so the next profile load does not announce the same
// set again. Each profile once.
function notHomeHostSigs(list) {
  const out = [];
  for (const sum of Array.isArray(list) ? list : []) {
    if (!sum || sum.skipped || !sum.profile || !Array.isArray(sum.not_home) || !sum.not_home.length) continue;
    const sig = notHomeSig(sum.profile, { branches: sum.not_home });
    if (!out.includes(sig)) out.push(sig);
  }
  return out;
}

// sessionNotHome: the not-home line for a clone session's sidebar row. The
// session's own latest return speaks when it ran (not skipped); otherwise the
// profile's last state does. "" for a session that is not a private copy.
function sessionNotHome(s, profile) {
  if (!s || !s.clone_of) return "";
  const r = s.return;
  if (r && !r.skipped) {
    return notHomeLine({ branches: r.not_home, error: r.error, uncommitted: r.uncommitted, uncommitted_cut: r.uncommitted_cut });
  }
  return notHomeLine(profile && profile.not_home);
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
    const sig = notHomeSig(p.name, p.not_home);
    next.push(sig);
    if (!known.has(sig)) toasts.push(p.name + ": " + notHomeLabel(p.not_home));
  }
  return { toasts, seen: next };
}

// returnNews: what one sessions poll means for returns. seen is the array of
// session return sigs already announced (null before the first poll, which only
// seeds); cloneIds the clone sessions the previous poll listed. Returns the
// toasts to show, the PROFILE sigs (notHomeSig of the return's not-home set; none
// for an idle return, whose toast omits not-home) to mark seen for
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
  let fresh = false;
  for (const s of list) {
    if (!s || !s.return || known.has(returnSig(s.id, s.return.at))) continue;
    fresh = true;
    const text = returnToastText(s.agent, s.return);
    if (text) {
      toasts.push(text);
      links.push(workHomeHb("handbackNoteIds")(s.return.flagged)); // the records the toast opens, if any
    }
    // An idle toast omits not-home, so it must not mark the set seen: the next
    // profile poll announces a set first found by an idle return, once.
    if (s.return.trigger !== "idle") sigs.push(notHomeSig(s.agent, { branches: s.return.not_home }));
  }
  const now = new Set(nextClones);
  const left = (Array.isArray(cloneIds) ? cloneIds : []).some((id) => !now.has(id));
  return { toasts, links, sigs, seen: nextSeen, cloneIds: nextClones, refetch: fresh || left };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { workHomeSuffix, notHomeCount, notHomeLabel, notHomeReasonText, notHomeTitle, notHomeFix, returnToastText, returnSig, notHomeNews, returnNews, uncommittedLabel, notHomeLine, hostReturnToast, notHomeSig, sessionNotHome, notHomePrefix, notHomeHostSigs };
}
