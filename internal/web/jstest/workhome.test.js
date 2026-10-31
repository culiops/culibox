"use strict";
// workhome.js: the wording for where a private copy's work goes and what is not home.
const test = require("node:test");
const assert = require("node:assert");
const { workHomeSuffix, notHomeCount, notHomeLabel, notHomeReasonText, notHomeTitle, notHomeFix, returnToastText, returnSig, notHomeNews, returnNews } = require("../static/workhome.js");

const ON = " · work comes home as box/* branches";
const OFF = " · work stays in the copy (auto-return off)";

test("suffix: pr says the work comes home as a PR, whatever auto_return is", () => {
  const PR = " · work comes home as a PR";
  assert.strictEqual(workHomeSuffix({ clone_of: "/r", auto_return: true }, true), PR);
  assert.strictEqual(workHomeSuffix({ clone_of: "/r", auto_return: false }, true), PR);
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
  assert.strictEqual(f("main", "off"), "`agent-box clone fetch work feat`");
  assert.strictEqual(f("master", "off"), "`agent-box clone fetch -base master work feat`");
  assert.strictEqual(f("main", "not-ff"), "`agent-box clone fetch work feat` overwrites box/feat");
  assert.strictEqual(f("master", "not-ff"), "`agent-box clone fetch -base master work feat` overwrites box/feat");
  assert.strictEqual(f("main", "checked-out"), "switch your repo off box/feat; it comes home at the next exit or spawn");
  assert.strictEqual(f("main", "refused"),
    "rebase it in the box onto origin/main; or, after checking it, `agent-box clone fetch -allow-old-history work feat`");
  assert.strictEqual(f("master", "refused"),
    "rebase it in the box onto origin/master; or, after checking it, `agent-box clone fetch -allow-old-history -base master work feat`");
  assert.strictEqual(f("main", "name"), "rename the branch in the box");
  for (const reason of ["too-large", "deferred", "failed"]) {
    assert.strictEqual(f("main", reason), 'use "Bring work home now", or `agent-box clone fetch work feat`');
    assert.strictEqual(f("master", reason), 'use "Bring work home now", or `agent-box clone fetch -base master work feat`');
  }
});

test("fix: an unknown reason gets the generic fix", () => {
  assert.strictEqual(notHomeFix("work", "main", { branch: "feat", reason: "bogus" }),
    'use "Bring work home now", or `agent-box clone fetch work feat`');
  assert.strictEqual(notHomeFix("work", "main", { branch: "feat", reason: "constructor" }),
    'use "Bring work home now", or `agent-box clone fetch work feat`');
});

test("fix: an empty base is treated as main", () => {
  assert.strictEqual(notHomeFix("work", "", { branch: "feat", reason: "off" }), "`agent-box clone fetch work feat`");
  assert.strictEqual(notHomeFix("work", undefined, { branch: "feat", reason: "refused" }),
    "rebase it in the box onto origin/main; or, after checking it, `agent-box clone fetch -allow-old-history work feat`");
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
  assert.deepStrictEqual(first, { toasts: ["p: 1 branch not home"], seen: ["p@A"] });
  const again = notHomeNews(first.seen, profiles);
  assert.deepStrictEqual(again, { toasts: [], seen: ["p@A"] });
});

test("news: a new run (new at) toasts again", () => {
  const seen = ["p@A"];
  const profiles = [{ name: "p", not_home: { at: "B", branches: [{ branch: "a" }, { branch: "b" }] } }];
  assert.deepStrictEqual(notHomeNews(seen, profiles), { toasts: ["p: 2 branches not home"], seen: ["p@B"] });
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
  const seen = ["gone@X", "p@A"];
  const profiles = [
    { name: "p", not_home: { at: "A", branches: [{ branch: "feat" }] } },
    { name: "q", not_home: { at: "C", branches: [{ branch: "x" }] } },
  ];
  assert.deepStrictEqual(notHomeNews(seen, profiles), { toasts: ["q: 1 branch not home"], seen: ["p@A", "q@C"] });
  assert.deepStrictEqual(seen, ["gone@X", "p@A"]);
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
  assert.deepStrictEqual(n.sigs, [returnSig("p", "t2")]);
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
