"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// The phone's profile editor draws a narrow enum as a .seg. A seg has no empty
// state of its own, so without a "not set" segment a value once picked could
// never be cleared there (the built-in Model, 4 choices, hit this); the wider
// <select> branch has "(not set)". Five segments wrap on a 390px phone, so the
// seg takes at most 3 choices plus "not set". Pinned at the source level:
// app.js needs a DOM to run.
const APP = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");

function body(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("a seg-drawn enum option in the profile editor offers a not-set segment", () => {
  const fn = body(APP, "renderPfOptions");
  const seg = fn.slice(fn.indexOf("buildSeg("), fn.indexOf("} else {", fn.indexOf("buildSeg(")));
  assert.match(seg, /value:\s*""\s*,\s*label:\s*"not set"/, "the seg branch needs a { value: \"\", label: \"not set\" } choice");
});

test("the seg is used for at most 3 choices, so with not set it stays one row", () => {
  assert.match(body(APP, "renderPfOptions"), /choices\.length > 0 && choices\.length <= 3\)/);
});
