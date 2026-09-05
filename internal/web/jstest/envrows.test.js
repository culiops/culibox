"use strict";
// The allowlist's row model. One table over two differently-shaped fields: env
// is literal key/value pairs stored in the profile, secrets are names whose
// value the daemon resolves elsewhere. A secret row has no value to edit, and
// the round trip must never invent one.
const test = require("node:test");
const assert = require("node:assert");
const { envRows, rowsToConfig, rowError } = require("../static/envrows.js");

test("literals come first, secrets after, each tagged", () => {
  assert.deepStrictEqual(
    envRows({ env: { TERM: "xterm-256color", LANG: "en_US.UTF-8" }, secrets: ["ANTHROPIC_API_KEY"] }),
    [
      { key: "TERM", value: "xterm-256color", kind: "literal" },
      { key: "LANG", value: "en_US.UTF-8", kind: "literal" },
      { key: "ANTHROPIC_API_KEY", value: "", kind: "secret" },
    ]);
});

test("an absent field is an empty list, never a throw", () => {
  assert.deepStrictEqual(envRows({}), []);
  assert.deepStrictEqual(envRows(null), []);
});

test("the round trip restores both fields and drops empty rows", () => {
  const rows = [
    { key: "TERM", value: "xterm", kind: "literal" },
    { key: "", value: "", kind: "literal" },
    { key: "TOK", value: "", kind: "secret" },
  ];
  assert.deepStrictEqual(rowsToConfig(rows), { env: { TERM: "xterm" }, secrets: ["TOK"] });
});

test("a secret carries no value even if one is somehow set", () => {
  assert.deepStrictEqual(
    rowsToConfig([{ key: "TOK", value: "leaked", kind: "secret" }]),
    { env: {}, secrets: ["TOK"] });
});

test("keys and values are trimmed", () => {
  assert.deepStrictEqual(
    rowsToConfig([{ key: "  A  ", value: "  b  ", kind: "literal" }]),
    { env: { A: "b" }, secrets: [] });
});

test("a duplicate name across the two kinds is refused, because the server refuses it", () => {
  assert.match(rowError([
    { key: "TOK", value: "x", kind: "literal" },
    { key: "TOK", value: "", kind: "secret" },
  ]), /TOK/);
});

test("an invalid name is refused with the name in the message", () => {
  assert.match(rowError([{ key: "not a name", value: "x", kind: "literal" }]), /not a name/);
  assert.match(rowError([{ key: "2FA", value: "x", kind: "literal" }]), /2FA/);
});

test("a value with no name is refused; a wholly empty row is not", () => {
  assert.match(rowError([{ key: "", value: "x", kind: "literal" }]), /name/i);
  assert.strictEqual(rowError([{ key: "", value: "", kind: "literal" }]), "");
});

test("a well-formed list has no error", () => {
  assert.strictEqual(rowError([
    { key: "TERM", value: "xterm", kind: "literal" },
    { key: "_A9", value: "", kind: "literal" },
    { key: "TOK", value: "", kind: "secret" },
  ]), "");
});
