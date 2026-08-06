"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { saveBody, classifySave } = require("../static/fsedit.js");

test("saveBody carries path, content, base version, and overwrite", () => {
  const b = saveBody("/home/u/a.conf", "new\n", "abc123", false);
  assert.deepStrictEqual(b, {
    path: "/home/u/a.conf", content: "new\n", base_version: "abc123", overwrite: false,
  });
});

test("a 200 is a save and surfaces the new version", () => {
  const r = classifySave({ ok: true, status: 200, data: { version: "v2" } });
  assert.strictEqual(r.outcome, "saved");
  assert.strictEqual(r.version, "v2");
});

test("a 409 is a conflict and carries the current version", () => {
  const r = classifySave({ ok: false, status: 409, data: { current_version: "v9" } });
  assert.strictEqual(r.outcome, "conflict");
  assert.strictEqual(r.currentVersion, "v9");
});

test("any other failure is an error with the server's message when present", () => {
  const r = classifySave({ ok: false, status: 413, data: { error: "file too large to save" } });
  assert.strictEqual(r.outcome, "error");
  assert.match(r.message, /too large/);
});

test("a transport failure (status 0) is an error, not a conflict", () => {
  const r = classifySave({ ok: false, status: 0, data: null });
  assert.strictEqual(r.outcome, "error");
});
