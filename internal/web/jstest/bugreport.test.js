"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { fillWhatHappened, WHAT_MARKER } = require("../static/bugreport.js");

const M = "<!-- what-happened -->";

test("empty or whitespace text leaves the markdown unchanged", () => {
  const md = "## What happened\n" + M + "\n## Env\n";
  assert.equal(fillWhatHappened(md, ""), md);
  assert.equal(fillWhatHappened(md, "   \n\t "), md);
  assert.equal(fillWhatHappened(md, undefined), md);
  assert.equal(fillWhatHappened(md, null), md);
});

test("text replaces the first marker only", () => {
  const md = "a " + M + " b " + M + " c";
  assert.equal(fillWhatHappened(md, "it broke"), "a it broke b " + M + " c");
});

test("regex replacement patterns in the text are inserted literally", () => {
  const md = "x " + M + " y";
  assert.equal(fillWhatHappened(md, "$& and $1"), "x $& and $1 y");
  assert.equal(fillWhatHappened(md, "$$ $` $'"), "x $$ $` $' y");
});

test("text that contains the marker is inserted literally, with no further substitution", () => {
  const md = "x " + M + " y";
  assert.equal(fillWhatHappened(md, "see " + M + " there"), "x see " + M + " there y");
});

test("markdown with no marker comes back unchanged", () => {
  assert.equal(fillWhatHappened("no marker here", "it broke"), "no marker here");
});

test("the marker constant matches the Go bundle", () => {
  assert.equal(WHAT_MARKER, M);
});

// bugOpenIssue is DOM-bound; load the file in a stub global scope and drive it.
function load(env) {
  const fs = require("node:fs"), path = require("node:path"), vm = require("node:vm");
  const src = fs.readFileSync(path.join(__dirname, "../static/bugreport.js"), "utf8");
  const els = {};
  const el = (id) => (els[id] = els[id] || { id, value: "", textContent: "", disabled: false, addEventListener() {}, focus() {} });
  const calls = [];
  const ctx = {
    document: { getElementById: el, readyState: "complete" },
    window: { open: (...a) => calls.push(["open", ...a]) },
    copyText: (t, done) => { calls.push(["copy", t]); done(true); },
    showToast: () => {}, setDialogError: (id, m) => calls.push(["err", m]), openDialog() {},
    api: env.api,
  };
  vm.createContext(ctx);
  vm.runInContext(src + "\n;this.__t = { openBugReport, bugOpenIssue };", ctx);
  return { t: ctx.__t, els, calls };
}

test("Open GitHub issue copies the filled report first, then opens the https://github.com URL", async () => {
  const md = "## What happened\n" + M + "\n";
  const { t, els, calls } = load({ api: async () => ({ markdown: md, issue_url: "https://github.com/o/r/issues/new?x=1" }) });
  await t.openBugReport();
  els["bug-what"].value = "it broke";
  t.bugOpenIssue();
  assert.deepEqual(calls.filter((c) => c[0] !== "err").map((c) => c[0]), ["copy", "open"]);
  assert.equal(calls.find((c) => c[0] === "copy")[1], "## What happened\nit broke\n");
  assert.equal(els["bug-status"].textContent, "Report copied — paste it into the issue body.");
});

test("a non-github issue_url is refused with an inline error and nothing opens", async () => {
  const { t, calls } = load({ api: async () => ({ markdown: "x " + M, issue_url: "javascript:alert(1)" }) });
  await t.openBugReport();
  t.bugOpenIssue();
  assert.ok(!calls.some((c) => c[0] === "open" || c[0] === "copy"));
  assert.ok(calls.some((c) => c[0] === "err" && /GitHub link/.test(c[1])));
});

test("an empty markdown in a 200 response is an error and leaves the buttons disabled", async () => {
  const { t, els, calls } = load({ api: async () => ({ markdown: "  ", issue_url: "https://github.com/o/r" }) });
  await t.openBugReport();
  assert.equal(els["bug-copy"].disabled, true);
  assert.equal(els["bug-gh"].disabled, true);
  assert.ok(calls.some((c) => c[0] === "err" && /empty report/.test(c[1])));
});
