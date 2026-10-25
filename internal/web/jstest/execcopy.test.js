"use strict";
// execCopy lives in app.js (a classic script with no exports): slice the one
// function out and run it against a small DOM stub.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs"), path = require("node:path"), vm = require("node:vm");

const src = fs.readFileSync(path.join(__dirname, "../static/app.js"), "utf8");
const start = src.indexOf("function execCopy(");
assert.ok(start > 0, "execCopy not found in app.js");
const fn = src.slice(start, src.indexOf("\n}\n", start) + 3);

function run(openDialogs, copyOK = true) {
  const mkHost = (name) => ({
    name, kids: [],
    appendChild(c) { this.kids.push(c); c.parent = this; },
    removeChild(c) { this.kids = this.kids.filter((k) => k !== c); },
  });
  const body = mkHost("body");
  const dialogs = openDialogs.map(mkHost);
  const log = [];
  const ctx = {
    document: {
      body,
      createElement: () => ({ style: {}, focus() { log.push(["focus", this.parent && this.parent.name]); }, select() {} }),
      querySelectorAll: (sel) => { log.push(["query", sel]); return dialogs; },
      execCommand: (c) => { log.push(["exec", c, [body, ...dialogs].filter((h) => h.kids.length).map((h) => h.name)]); return copyOK; },
    },
  };
  vm.createContext(ctx);
  vm.runInContext(fn + "\n;this.__execCopy = execCopy;", ctx);
  const ok = ctx.__execCopy("hello");
  return { ok, log, body, dialogs };
}

test("with no dialog open the textarea goes in body and is removed afterwards", () => {
  const r = run([]);
  assert.equal(r.ok, true);
  assert.deepEqual(r.log.find((l) => l[0] === "exec")[2], ["body"]);
  assert.equal(r.body.kids.length, 0);
});

test("inside a modal dialog the textarea goes in the dialog, not the inert body", () => {
  const r = run(["bug-modal"]);
  assert.equal(r.ok, true);
  assert.deepEqual(r.log.find((l) => l[0] === "query"), ["query", "dialog[open]"]);
  assert.deepEqual(r.log.find((l) => l[0] === "focus"), ["focus", "bug-modal"]);
  assert.deepEqual(r.log.find((l) => l[0] === "exec")[2], ["bug-modal"]);
  assert.equal(r.dialogs[0].kids.length, 0, "temp textarea must be removed");
  assert.equal(r.body.kids.length, 0);
});

test("with stacked dialogs the last open one hosts the textarea", () => {
  const r = run(["first", "second"]);
  assert.deepEqual(r.log.find((l) => l[0] === "exec")[2], ["second"]);
  assert.equal(r.dialogs[1].kids.length, 0);
});

test("a failed execCommand reports false and still cleans up", () => {
  const r = run(["d"], false);
  assert.equal(r.ok, false);
  assert.equal(r.dialogs[0].kids.length, 0);
});
