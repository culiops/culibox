"use strict";
// workhome.js: the wording for where a private copy's work goes and what is not home.
const test = require("node:test");
const assert = require("node:assert");
const { workHomeSuffix, notHomeCount, notHomeLabel, notHomeReasonText, notHomeTitle, notHomeFix, returnToastText, returnSig, notHomeNews, returnNews, uncommittedLabel, notHomeLine, hostReturnToast, notHomeSig, sessionNotHome, notHomePrefix, notHomeHostSigs } = require("../static/workhome.js");

const ON = " · work comes home as box/* branches";
const OFF = " · work stays in the copy (auto-return off)";

test("suffix: pr says work goes out as a PR; auto_return decides whether box/* comes home too", () => {
  const PR_ON = " · work comes home as a PR and as box/* branches";
  const PR_OFF = " · work goes out as a PR; box/* return is off";
  assert.strictEqual(workHomeSuffix({ clone_of: "/r", auto_return: true }, true), PR_ON);
  assert.strictEqual(workHomeSuffix({ clone_of: "/r", auto_return: false }, true), PR_OFF);
  assert.strictEqual(workHomeSuffix({ clone_of: "/r" }, true), PR_OFF);
  assert.strictEqual(workHomeSuffix({ clone_of: "/r", auto_return: true }, false), ON);
  assert.strictEqual(workHomeSuffix({ clone_of: "/r" }, false), OFF);
  assert.strictEqual(workHomeSuffix({ clone_of: "" }, true), "");
  assert.strictEqual(workHomeSuffix(null, true), "");
});

test("suffix: on and off for a private copy, empty for anything else", () => {
  assert.strictEqual(workHomeSuffix({ clone_of: "/r", auto_return: true }), ON);
  assert.strictEqual(workHomeSuffix({ clone_of: "/r", auto_return: false }), OFF);
  // the server omits auto_return when it is false (omitempty), so a missing value is off
  assert.strictEqual(workHomeSuffix({ clone_of: "/r" }), OFF);
  assert.strictEqual(workHomeSuffix({ clone_of: "", auto_return: true }), "");
  assert.strictEqual(workHomeSuffix({}), "");
  assert.strictEqual(workHomeSuffix(null), "");
  assert.strictEqual(workHomeSuffix(undefined), "");
});

test("count: branches of a state, 0 for no state", () => {
  assert.strictEqual(notHomeCount(null), 0);
  assert.strictEqual(notHomeCount({ branches: [] }), 0);
  assert.strictEqual(notHomeCount({ branches: [{ branch: "a" }, { branch: "b" }] }), 2);
});

test("label: empty, singular, plural, and the error-only label", () => {
  assert.strictEqual(notHomeLabel(null), "");
  assert.strictEqual(notHomeLabel({ branches: [] }), "");
  assert.strictEqual(notHomeLabel({ branches: [{ branch: "feat", reason: "off" }] }), "1 branch not home");
  assert.strictEqual(notHomeLabel({ branches: [{ branch: "a" }, { branch: "b" }] }), "2 branches not home");
  assert.strictEqual(notHomeLabel({ branches: [], error: "could not check" }), "work not checked");
  // branches win over the error when both are present
  assert.strictEqual(notHomeLabel({ branches: [{ branch: "a" }], error: "x" }), "1 branch not home");
});

test("reason text: one line per known reason, naming box/<branch>", () => {
  const r = (reason) => notHomeReasonText({ branch: "feat", reason });
  assert.strictEqual(r("not-ff"), "box/feat on the host has commits this branch lacks");
  assert.strictEqual(r("off"), "box/feat was not brought home: auto-return is off for this profile");
  assert.strictEqual(r("checked-out"), "box/feat is checked out in your repo, so it did not move");
  assert.strictEqual(r("refused"), "the host refused the history of box/feat");
  assert.strictEqual(r("too-large"), "box/feat is too large to bring home automatically");
  assert.strictEqual(r("name"), "box/feat is not a valid branch name for the host");
  assert.strictEqual(r("deferred"), "box/feat was not tried in this run");
  assert.strictEqual(r("failed"), "box/feat could not be brought home");
});

test("reason text: an unknown reason from a newer server gets the generic line", () => {
  assert.strictEqual(notHomeReasonText({ branch: "feat", reason: "bogus" }), "box/feat was not brought home");
  assert.strictEqual(notHomeReasonText({ branch: "feat", reason: "" }), "box/feat was not brought home");
  // a prototype key is not a reason: it must not reach an inherited property
  assert.strictEqual(notHomeReasonText({ branch: "feat", reason: "constructor" }), "box/feat was not brought home");
  assert.strictEqual(notHomeReasonText({ branch: "feat", reason: "__proto__" }), "box/feat was not brought home");
});

test("reason text: a box-derived branch name is passed through verbatim", () => {
  assert.strictEqual(notHomeReasonText({ branch: "<img src=x>", reason: "not-ff" }),
    "box/<img src=x> on the host has commits this branch lacks");
});

test("fix: each reason, with and without -base", () => {
  const f = (base, reason) => notHomeFix("work", base, { branch: "feat", reason });
  assert.strictEqual(f("main", "off"), "`culibox clone fetch work feat`");
  assert.strictEqual(f("master", "off"), "`culibox clone fetch -base master work feat`");
  assert.strictEqual(f("main", "not-ff"), "`culibox clone fetch work feat` overwrites box/feat");
  assert.strictEqual(f("master", "not-ff"), "`culibox clone fetch -base master work feat` overwrites box/feat");
  assert.strictEqual(f("main", "checked-out"), "switch your repo off box/feat; it comes home at the next exit or spawn");
  assert.strictEqual(f("main", "refused"),
    "rebase it in the box onto origin/main; or, after checking it, `culibox clone fetch -allow-old-history work feat`");
  assert.strictEqual(f("master", "refused"),
    "rebase it in the box onto origin/master; or, after checking it, `culibox clone fetch -allow-old-history -base master work feat`");
  assert.strictEqual(f("main", "name"), "rename the branch in the box");
  for (const reason of ["too-large", "deferred", "failed"]) {
    assert.strictEqual(f("main", reason), 'use "Bring work home now", or `culibox clone fetch work feat`');
    assert.strictEqual(f("master", reason), 'use "Bring work home now", or `culibox clone fetch -base master work feat`');
  }
});

test("fix: an unknown reason gets the generic fix", () => {
  assert.strictEqual(notHomeFix("work", "main", { branch: "feat", reason: "bogus" }),
    'use "Bring work home now", or `culibox clone fetch work feat`');
  assert.strictEqual(notHomeFix("work", "main", { branch: "feat", reason: "constructor" }),
    'use "Bring work home now", or `culibox clone fetch work feat`');
});

test("fix: an empty base is treated as main", () => {
  assert.strictEqual(notHomeFix("work", "", { branch: "feat", reason: "off" }), "`culibox clone fetch work feat`");
  assert.strictEqual(notHomeFix("work", undefined, { branch: "feat", reason: "refused" }),
    "rebase it in the box onto origin/main; or, after checking it, `culibox clone fetch -allow-old-history work feat`");
});

test("toast: nothing to say gives empty", () => {
  assert.strictEqual(returnToastText("p", null), "");
  assert.strictEqual(returnToastText("p", undefined), "");
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: [], not_home: [] }), "");
});

test("toast: a skipped run gives empty, whatever else it carries", () => {
  assert.strictEqual(returnToastText("p", { trigger: "exit", skipped: "in-use", home: ["box/feat"], not_home: [] }), "");
  assert.strictEqual(returnToastText("p", { trigger: "now", skipped: "unavailable", not_home: [{ branch: "feat", reason: "off" }] }), "");
});

test("toast: home only, not home only, and both", () => {
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: ["box/feat"], not_home: [] }), "p: box/feat came home");
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: [], not_home: [{ branch: "feat", reason: "not-ff" }] }), "p: 1 branch not home");
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: [], not_home: [{ branch: "a", reason: "off" }, { branch: "b", reason: "off" }] }),
    "p: 2 branches not home");
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: ["box/feat"], not_home: [{ branch: "x", reason: "not-ff" }] }),
    "p: box/feat came home · 1 branch not home");
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: ["box/a", "box/b"], not_home: [] }), "p: box/a, box/b came home");
});

test("toast: a manual run with nothing to do says all work is home", () => {
  assert.strictEqual(returnToastText("p", { trigger: "now", home: [], not_home: [] }), "p: all work is home");
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: [], not_home: [] }), "");
});

test("toast: an error with nothing home says work not checked", () => {
  assert.strictEqual(returnToastText("p", { trigger: "now", error: "could not check" }), "p: work not checked");
});

test("toast: box-derived strings are passed through as text", () => {
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: ["box/<img src=x>"], not_home: [] }),
    "p: box/<img src=x> came home");
});

test("sig: name at at", () => {
  assert.strictEqual(returnSig("p", "2026-10-31T10:00:00Z"), "p@2026-10-31T10:00:00Z");
});

test("news: one toast for a new sig, none for the same sig again", () => {
  const profiles = [{ name: "p", not_home: { at: "A", branches: [{ branch: "feat", reason: "off" }] } }];
  const first = notHomeNews([], profiles);
  assert.deepStrictEqual(first, { toasts: ["p: 1 branch not home"], seen: ["p@feat:off"] });
  const again = notHomeNews(first.seen, profiles);
  assert.deepStrictEqual(again, { toasts: [], seen: ["p@feat:off"] });
});

test("news: a different not-home set toasts again", () => {
  const seen = ["p@feat:off"];
  const profiles = [{ name: "p", not_home: { at: "B", branches: [{ branch: "a", reason: "off" }, { branch: "b", reason: "not-ff" }] } }];
  assert.deepStrictEqual(notHomeNews(seen, profiles), { toasts: ["p: 2 branches not home"], seen: ["p@a:off,b:not-ff"] });
});

test("notHomeSig: a missing branch or reason is empty, never the word undefined", () => {
  assert.strictEqual(notHomeSig("p", { branches: [{ branch: "a" }, { reason: "off" }, null] }), "p@:,:off,a:");
  assert.ok(!notHomeSig("p", { branches: [{ branch: "a" }] }).includes("undefined"));
});

test("news: no toast for a profile with no branches, even with an error", () => {
  const profiles = [
    { name: "a", not_home: null },
    { name: "b", not_home: { at: "A", branches: [], error: "could not check" } },
    { name: "c" },
  ];
  assert.deepStrictEqual(notHomeNews([], profiles), { toasts: [], seen: [] });
});

test("news: seen is pruned to the sigs still not home, and the input is not mutated", () => {
  const seen = ["gone@x:off", "p@feat:off"];
  const profiles = [
    { name: "p", not_home: { at: "A", branches: [{ branch: "feat", reason: "off" }] } },
    { name: "q", not_home: { at: "C", branches: [{ branch: "x", reason: "off" }] } },
  ];
  assert.deepStrictEqual(notHomeNews(seen, profiles), { toasts: ["q: 1 branch not home"], seen: ["p@feat:off", "q@x:off"] });
  assert.deepStrictEqual(seen, ["gone@x:off", "p@feat:off"]);
});

test("news: a box-derived branch name never reaches a toast as markup", () => {
  const profiles = [{ name: "p", not_home: { at: "A", branches: [{ branch: "<img src=x>", reason: "not-ff" }] } }];
  const { toasts } = notHomeNews([], profiles);
  assert.deepStrictEqual(toasts, ["p: 1 branch not home"]);
  assert.ok(!toasts.join("").includes("<img"));
});

test("notHomeTitle: one reason line per branch, or the error alone", () => {
  assert.strictEqual(notHomeTitle(null), "");
  assert.strictEqual(
    notHomeTitle({ branches: [{ branch: "a", reason: "not-ff" }, { branch: "b", reason: "checked-out" }] }),
    "box/a on the host has commits this branch lacks\nbox/b is checked out in your repo, so it did not move");
  assert.strictEqual(notHomeTitle({ branches: [], error: "clone unreadable" }), "clone unreadable");
  // A chip that only has uncommitted files still gets a title.
  assert.strictEqual(notHomeTitle({ branches: [], uncommitted: 3 }), "3 files not committed");
  assert.strictEqual(notHomeTitle({ uncommitted: 4, uncommitted_cut: true }), "4+ files not committed");
});

const EXIT = (id, agent, at, home) => ({ id, agent, clone_of: "/r", return: { trigger: "exit", at, home: home || ["feat"] } });

test("returnNews: the first poll seeds silently and asks for nothing", () => {
  const n = returnNews(null, [], [EXIT("s1", "p", "t1")]);
  assert.deepStrictEqual(n.toasts, []);
  assert.deepStrictEqual(n.sigs, []);
  assert.strictEqual(n.refetch, false);
  assert.deepStrictEqual(n.seen, [returnSig("s1", "t1")]);
  assert.deepStrictEqual(n.cloneIds, ["s1"]);
});

test("returnNews: a new sig toasts once, marks the profile sig seen, and asks for a refetch", () => {
  const first = returnNews(null, [], [EXIT("s1", "p", "t1")]);
  const list = [EXIT("s1", "p", "t1"), EXIT("s2", "p", "t2")];
  const n = returnNews(first.seen, first.cloneIds, list);
  assert.deepStrictEqual(n.toasts, ["p: feat came home"]);
  assert.deepStrictEqual(n.sigs, [notHomeSig("p", { branches: [] })]);
  assert.strictEqual(n.refetch, true);
  const again = returnNews(n.seen, n.cloneIds, list);
  assert.deepStrictEqual(again.toasts, []);
  assert.deepStrictEqual(again.sigs, []);
  assert.strictEqual(again.refetch, false);
});

test("returnNews: a skipped return is news for the profile list but says nothing", () => {
  const first = returnNews(null, [], [{ id: "s1", agent: "p", clone_of: "/r" }]);
  const sk = { id: "s1", agent: "p", clone_of: "/r", return: { trigger: "exit", at: "t9", skipped: "off" } };
  const n = returnNews(first.seen, first.cloneIds, [sk]);
  assert.deepStrictEqual(n.toasts, []);
  assert.strictEqual(n.refetch, true);
});

test("returnNews: a clone session leaving the list asks for a refetch; a plain one does not", () => {
  const first = returnNews(null, [], [EXIT("s1", "p", "t1"), { id: "sh", agent: "x" }]);
  const gone = returnNews(first.seen, first.cloneIds, [{ id: "sh", agent: "x" }]);
  assert.strictEqual(gone.refetch, true);
  assert.deepStrictEqual(gone.toasts, []);
  assert.deepStrictEqual(gone.seen, [], "seen is pruned to the sessions still listed");
  const plainGone = returnNews(gone.seen, gone.cloneIds, []);
  assert.strictEqual(plainGone.refetch, false);
});

test("returnNews: tolerates a missing list", () => {
  assert.deepStrictEqual(returnNews(["x"], ["s1"], null).toasts, []);
});

// --- flagged files handed back (#346) ----------------------------------------
test("returnToastText: flagged files end the line with their count", () => {
  const sum = { trigger: "exit", home: ["box/feat"], not_home: [], flagged: [{ id: "h1", count: 2, branch: "feat" }] };
  assert.strictEqual(returnToastText("p", sum), "p: box/feat came home · 2 files flagged for your review");
  assert.ok(returnToastText("p", sum).endsWith("2 files flagged for your review"));
  assert.strictEqual(returnToastText("p", { trigger: "exit", flagged: [{ id: "h1", count: 1 }] }), "p: 1 file flagged for your review");
});

test("returnToastText: a cut-short scan with nothing flagged does not read as nothing flagged", () => {
  const t = returnToastText("p", { trigger: "exit", home: [], not_home: [], flagged: [{ id: "h1", count: 0, truncated: true }] });
  assert.strictEqual(t, "p: Not everything was checked — open the review");
  assert.doesNotMatch(t, /\b(clean|safe|no threats)\b/i);
});

test("returnToastText: a skipped return says nothing even with flagged notes", () => {
  assert.strictEqual(returnToastText("p", { trigger: "exit", skipped: "in-use", flagged: [{ id: "h1", count: 2 }] }), "");
});

test("returnNews: a flagged return toasts once and carries the record ids for the link", () => {
  const flagged = { id: "s1", agent: "p", clone_of: "/r", return: { trigger: "exit", at: "t1", home: [], flagged: [{ id: "h1", count: 2 }] } };
  const first = returnNews(null, [], [{ id: "s1", agent: "p", clone_of: "/r" }]);
  const n = returnNews(first.seen, first.cloneIds, [flagged]);
  assert.deepStrictEqual(n.toasts, ["p: 2 files flagged for your review"]);
  assert.deepStrictEqual(n.links, [["h1"]]);
  const plain = returnNews(first.seen, first.cloneIds, [EXIT("s9", "p", "t9")]);
  assert.deepStrictEqual(plain.links, [[]], "links runs in step with toasts");
});

// --- uncommitted files and the idle/host triggers (#369) ----------------------
test("uncommittedLabel: empty for unknown or none, singular, plural, floor when cut", () => {
  assert.strictEqual(uncommittedLabel(null, false), "");
  assert.strictEqual(uncommittedLabel(undefined, false), "");
  assert.strictEqual(uncommittedLabel(0, false), "");
  assert.strictEqual(uncommittedLabel(0, true), "");
  assert.strictEqual(uncommittedLabel(1, false), "1 file not committed");
  assert.strictEqual(uncommittedLabel(3, false), "3 files not committed");
  assert.strictEqual(uncommittedLabel(3, true), "3+ files not committed");
  assert.strictEqual(uncommittedLabel(1, true), "1+ files not committed");
});

test("notHomeLine: branches and uncommitted joined, each alone, or empty", () => {
  const b = [{ branch: "a", reason: "off" }];
  assert.strictEqual(notHomeLine({ branches: b, uncommitted: 3 }), "1 branch not home · 3 files not committed");
  assert.strictEqual(notHomeLine({ branches: b }), "1 branch not home");
  assert.strictEqual(notHomeLine({ branches: [], uncommitted: 2, uncommitted_cut: true }), "2+ files not committed");
  assert.strictEqual(notHomeLine({ branches: [], error: "x", uncommitted: 1 }), "work not checked · 1 file not committed");
  assert.strictEqual(notHomeLine({ branches: [], uncommitted: 0 }), "");
  assert.strictEqual(notHomeLine(null), "");
  assert.strictEqual(notHomeLabel({ branches: b, uncommitted: 3 }), "1 branch not home", "notHomeLabel is branches only");
});

test("toast: an idle return says only what came home and what is flagged", () => {
  const nh = [{ branch: "a", reason: "off" }];
  assert.strictEqual(returnToastText("p", { trigger: "idle", home: [], not_home: nh, uncommitted: 3 }), "");
  assert.strictEqual(returnToastText("p", { trigger: "idle", home: [], error: "boom" }), "");
  assert.strictEqual(returnToastText("p", { trigger: "idle", home: ["box/feat"], not_home: nh, error: "boom", uncommitted: 2 }), "p: box/feat came home");
  const f = returnToastText("p", { trigger: "idle", home: [], flagged: [{ id: "h1", count: 2, branch: "feat" }] });
  assert.match(f, /^p: /);
});

test("toast: a host return adds the uncommitted files", () => {
  const nh = [{ branch: "a", reason: "off" }, { branch: "b", reason: "not-ff" }];
  const t = returnToastText("p", { trigger: "host", home: [], not_home: nh, uncommitted: 3 });
  assert.strictEqual(t, "p: 2 branches not home · 3 files not committed");
  assert.strictEqual(returnToastText("p", { trigger: "host", home: ["box/x"], uncommitted: 1 }), "p: box/x came home · 1 file not committed");
  assert.strictEqual(returnToastText("p", { trigger: "host", home: [], uncommitted: 4, uncommitted_cut: true }), "p: 4+ files not committed");
  assert.strictEqual(returnToastText("p", { trigger: "host", home: [] }), "");
});

test("toast: a skipped host return says whether the work is still coming or was not checked", () => {
  assert.strictEqual(returnToastText("p", { trigger: "host", skipped: "deferred" }), "p: still coming home");
  assert.strictEqual(returnToastText("p", { trigger: "host", skipped: "other-instance", error: "docker down", uncommitted: 3 }), "p: work not checked");
  assert.strictEqual(returnToastText("p", { trigger: "host", skipped: "in-use" }), "p: work not checked");
  assert.strictEqual(returnToastText("p", { trigger: "host", skipped: "unavailable" }), "p: work not checked");
  // Other triggers stay silent when skipped.
  assert.strictEqual(returnToastText("p", { trigger: "idle", skipped: "unchanged" }), "");
  assert.strictEqual(returnToastText("p", { trigger: "exit", skipped: "other-instance" }), "");
  assert.strictEqual(returnToastText("p", { trigger: "now", skipped: "deferred" }), "");
});

test("toast: an exit return is unchanged by uncommitted", () => {
  assert.strictEqual(returnToastText("p", { trigger: "exit", home: [], not_home: [{ branch: "a", reason: "off" }], uncommitted: 3 }), "p: 1 branch not home");
});

test("hostReturnToast: one part per clone that has something to say, joined", () => {
  assert.strictEqual(hostReturnToast(null), "");
  assert.strictEqual(hostReturnToast([]), "");
  assert.strictEqual(hostReturnToast([{ trigger: "host", profile: "p", home: [] }]), "");
  const one = { trigger: "host", profile: "p", home: ["box/x"] };
  const two = { trigger: "host", profile: "q", home: [], uncommitted: 2 };
  const skip = { trigger: "host", profile: "r", skipped: "other-instance", uncommitted: 9 };
  assert.strictEqual(hostReturnToast([one]), "p: box/x came home");
  assert.strictEqual(hostReturnToast([one, skip, two]), "p: box/x came home · r: work not checked · q: 2 files not committed");
});

test("hostReturnToast: a profile name is passed through as text", () => {
  assert.strictEqual(hostReturnToast([{ trigger: "host", profile: "<b>", home: ["box/x"] }]), "<b>: box/x came home");
});

test("notHomeSig: name and the sorted branch:reason list; uncommitted is not part of it", () => {
  const a = { branches: [{ branch: "b", reason: "off" }, { branch: "a", reason: "not-ff" }], uncommitted: 3 };
  assert.strictEqual(notHomeSig("p", a), "p@a:not-ff,b:off");
  assert.strictEqual(notHomeSig("p", { branches: [{ branch: "a", reason: "not-ff" }, { branch: "b", reason: "off" }], uncommitted: 9 }), "p@a:not-ff,b:off");
  assert.strictEqual(notHomeSig("p", null), "p@");
});

test("news: same not-home set with a different at is one toast; a changed reason is a second; uncommitted alone is none", () => {
  const prof = (at, reason, uncommitted) => [{ name: "p", not_home: { at, uncommitted, branches: [{ branch: "feat", reason }] } }];
  const first = notHomeNews([], prof("A", "off"));
  assert.strictEqual(first.toasts.length, 1);
  const again = notHomeNews(first.seen, prof("B", "off"));
  assert.deepStrictEqual(again.toasts, []);
  const unc = notHomeNews(again.seen, prof("C", "off", 7));
  assert.deepStrictEqual(unc.toasts, []);
  const changed = notHomeNews(unc.seen, prof("D", "not-ff", 7));
  assert.strictEqual(changed.toasts.length, 1);
  assert.deepStrictEqual(changed.seen, [notHomeSig("p", { branches: [{ branch: "feat", reason: "not-ff" }] })]);
});

test("returnNews: an exit return marks the profile's notHomeSig, not a per-run sig", () => {
  const nh = [{ branch: "feat", reason: "off" }];
  const exit = (id, at) => ({ id, agent: "p", clone_of: "/r", return: { trigger: "exit", at, home: [], not_home: nh } });
  const first = returnNews(null, [], [EXIT("s0", "p", "t0")]);
  const n = returnNews(first.seen, first.cloneIds, [EXIT("s0", "p", "t0"), exit("s1", "t1")]);
  assert.deepStrictEqual(n.toasts, ["p: 1 branch not home"]);
  assert.deepStrictEqual(n.sigs, [notHomeSig("p", { branches: nh })]);
});

test("returnNews: an idle return announces nothing and marks nothing, so the profile poll announces a new not-home set once", () => {
  const nh = [{ branch: "feat", reason: "off" }];
  const idle = (id, at) => ({ id, agent: "p", clone_of: "/r", return: { trigger: "idle", at, home: [], not_home: nh, uncommitted: 3 } });
  const first = returnNews(null, [], [idle("s1", "t0")]);
  const n = returnNews(first.seen, first.cloneIds, [idle("s1", "t0"), idle("s2", "t2")]);
  assert.deepStrictEqual(n.toasts, [], "an idle return says nothing about not-home");
  assert.deepStrictEqual(n.sigs, [], "an idle return does not mark the profile sig seen");
  assert.strictEqual(n.refetch, true, "a new return still refreshes the profile list");
  // The next profile poll sees the set for the first time: one toast, then none.
  const prof = [{ name: "p", not_home: { at: "A", branches: nh } }];
  const poll1 = notHomeNews([], prof);
  assert.deepStrictEqual(poll1.toasts, ["p: 1 branch not home"]);
  assert.deepStrictEqual(notHomeNews(poll1.seen, prof).toasts, []);
});

test("sessionNotHome: the session's own latest return wins, else the profile; nothing for a non-clone", () => {
  const prof = { name: "p", not_home: { at: "A", branches: [{ branch: "x", reason: "off" }, { branch: "y", reason: "off" }], uncommitted: 5 } };
  const s = { id: "s1", agent: "p", clone_of: "/r", return: { trigger: "idle", at: "t", home: [], not_home: [{ branch: "a", reason: "off" }], uncommitted: 3 } };
  assert.strictEqual(sessionNotHome(s, prof), "1 branch not home · 3 files not committed");
  const cut = { ...s, return: { ...s.return, not_home: [], uncommitted: 3, uncommitted_cut: true } };
  assert.strictEqual(sessionNotHome(cut, prof), "3+ files not committed");
  const skipped = { ...s, return: { trigger: "idle", at: "t", skipped: "unchanged" } };
  assert.strictEqual(sessionNotHome(skipped, prof), "2 branches not home · 5 files not committed");
  assert.strictEqual(sessionNotHome({ id: "s1", agent: "p", clone_of: "/r" }, prof), "2 branches not home · 5 files not committed");
  assert.strictEqual(sessionNotHome({ id: "s1", agent: "p", clone_of: "/r" }, undefined), "");
  assert.strictEqual(sessionNotHome({ id: "s2", agent: "p", return: s.return }, prof), "");
  assert.strictEqual(sessionNotHome(null, prof), "");
});

test("notHomePrefix: the line first, joined with a middle dot; either part alone when the other is empty", () => {
  assert.strictEqual(notHomePrefix("1 branch not home", "demo-clone · Private copy of ~/x"), "1 branch not home · demo-clone · Private copy of ~/x");
  assert.strictEqual(notHomePrefix("1 branch not home", ""), "1 branch not home");
  assert.strictEqual(notHomePrefix("", "summary"), "summary");
  assert.strictEqual(notHomePrefix("", ""), "");
  assert.strictEqual(notHomePrefix(undefined, undefined), "");
});

test("notHomeHostSigs: one sig per announced not-home set, so the profile load does not announce it again", () => {
  const nh = [{ branch: "feat", reason: "off" }];
  const list = [
    { trigger: "host", profile: "p", home: [], not_home: nh },
    { trigger: "host", profile: "p", home: [], not_home: nh },
    { trigger: "host", profile: "q", home: ["box/x"], not_home: [] },
    { trigger: "host", profile: "r", skipped: "other-instance", not_home: nh },
    { trigger: "host", not_home: nh },
    null,
  ];
  assert.deepStrictEqual(notHomeHostSigs(list), [notHomeSig("p", { branches: nh })]);
  assert.deepStrictEqual(notHomeHostSigs(null), []);
  // What the toast announced, the profile poll then finds already seen.
  const prof = [{ name: "p", not_home: { at: "A", branches: nh } }];
  assert.deepStrictEqual(notHomeNews(notHomeHostSigs(list), prof).toasts, []);
});
