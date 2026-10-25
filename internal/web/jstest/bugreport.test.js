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
  const mk = (id) => ({
    id, value: "", textContent: "", disabled: false, children: [],
    addEventListener() {}, focus() {},
    appendChild(c) { this.children.push(c); },
    get link() { return this.children.find((c) => c.tag === "a"); },
  });
  const el = (id) => (els[id] = els[id] || mk(id));
  const calls = [];
  const clip = env.clipboard === undefined ? undefined : {
    writeText: (t) => { calls.push(["writeText", t]); return env.clipboard ? Promise.resolve() : Promise.reject(new Error("no")); },
  };
  const ctx = {
    document: {
      getElementById: el, readyState: "complete",
      createElement: (tag) => ({ tag, textContent: "", set href(v) { this._href = v; }, get href() { return this._href; } }),
    },
    window: { open: (...a) => calls.push(["open", ...a]), isSecureContext: !!clip },
    navigator: { clipboard: clip },
    execCopy: (t) => { calls.push(["copy", t]); return env.sync !== false; },
    copyText: (t, done) => { calls.push(["copyText", t]); done(true); },
    showToast: () => {}, setDialogError: (id, m) => calls.push(["err", m]), openDialog() {},
    api: env.api,
  };
  vm.createContext(ctx);
  vm.runInContext(src + "\n;this.__t = { openBugReport, bugOpenIssue };", ctx);
  return { t: ctx.__t, els, calls };
}
const URL_OK = "https://github.com/o/r/issues/new?x=1";
const names = (calls) => calls.filter((c) => c[0] !== "err").map((c) => c[0]);

test("Open GitHub issue copies the filled report then opens the URL, both synchronously in the one call", async () => {
  const md = "## What happened\n" + M + "\n";
  const { t, els, calls } = load({ api: async () => ({ markdown: md, issue_url: URL_OK }), clipboard: true });
  await t.openBugReport();
  els["bug-what"].value = "it broke";
  t.bugOpenIssue(); // no await: everything below must already have happened
  assert.deepEqual(names(calls), ["copy", "open"]);
  const real = calls.filter((c) => c[0] !== "err");
  assert.equal(real[0][1], "## What happened\nit broke\n");
  assert.deepEqual(real[1].slice(1), [URL_OK, "_blank", "noopener"]);
  assert.equal(els["bug-status"].textContent, "Report copied — paste it into the issue body. ");
});

test("the status always carries a real fallback link with the exact href", async () => {
  const { t, els } = load({ api: async () => ({ markdown: "x " + M, issue_url: URL_OK }), clipboard: true });
  await t.openBugReport();
  t.bugOpenIssue();
  const a = els["bug-status"].link;
  assert.ok(a, "expected an <a> in #bug-status");
  assert.equal(a.href, URL_OK);
  assert.equal(a.target, "_blank");
  assert.equal(a.rel, "noopener");
  assert.equal(a.textContent, "Open the issue");
});

test("navigator.clipboard.writeText is used only when the synchronous copy fails", async () => {
  const ok = load({ api: async () => ({ markdown: "x " + M, issue_url: URL_OK }), clipboard: true, sync: true });
  await ok.t.openBugReport();
  ok.t.bugOpenIssue();
  assert.ok(!ok.calls.some((c) => c[0] === "writeText"));

  const bad = load({ api: async () => ({ markdown: "x " + M, issue_url: URL_OK }), clipboard: true, sync: false });
  await bad.t.openBugReport();
  bad.t.bugOpenIssue();
  assert.deepEqual(names(bad.calls), ["copy", "open", "writeText"]); // open is not delayed by the fallback
  await new Promise((r) => setImmediate(r));
  assert.equal(bad.els["bug-status"].textContent, "Report copied — paste it into the issue body. ");
  assert.ok(bad.els["bug-status"].link);
});

test("when every copy path fails the status says so and the link is still there", async () => {
  const { t, els, calls } = load({ api: async () => ({ markdown: "x " + M, issue_url: URL_OK }), sync: false });
  await t.openBugReport();
  t.bugOpenIssue();
  assert.deepEqual(names(calls), ["copy", "open"]);
  assert.equal(els["bug-status"].textContent, "Could not copy — use Copy report, then paste. ");
  assert.equal(els["bug-status"].link.href, URL_OK);
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

// The build is side-effecting (doctor probes the host), so the fetch must be a
// POST: csrfGuard same-origin-checks cookie POSTs, and a cross-site GET
// navigation would slip past it.
test("the report is requested with POST, never a bare GET", async () => {
  const seen = [];
  const { t } = load({ api: async (path, opts) => { seen.push([path, opts]); return { markdown: "x " + M, issue_url: URL_OK }; } });
  await t.openBugReport();
  assert.equal(seen.length, 1);
  assert.equal(seen[0][0], "/api/bugreport");
  assert.equal(seen[0][1] && seen[0][1].method, "POST");
});
