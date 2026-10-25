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
