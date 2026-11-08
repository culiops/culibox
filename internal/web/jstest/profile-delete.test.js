"use strict";
// One delete-profile confirm (#173 F78). The profile page (pfvDelete) and the Settings form (deleteProfile)
// used to carry the same question twice, word for word; both now ask deleteProfileAsk, which lives in
// profileform.js next to containmentConfirm, the other confirm the two share.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

const BODY = 'Delete the profile "x"? Running sessions are unaffected. The profile\'s saved workspace default and environment allowlist will be lost. This cannot be undone.';

// profileform.js as the page loads it: a classic script, askConfirm a global of shell.js.
function loadProfileForm(askConfirm) {
  const ctx = vm.createContext({ askConfirm });
  new vm.Script(read("profileform.js"), { filename: "profileform.js" }).runInContext(ctx);
  return ctx;
}

test("deleteProfileAsk asks today's exact question, in red, and answers what the dialog answered", async () => {
  const seen = [];
  const ctx = loadProfileForm(async (o) => { seen.push(o); return seen.length === 1; });
  assert.strictEqual(await ctx.deleteProfileAsk("x"), true);
  assert.strictEqual(await ctx.deleteProfileAsk("x"), false);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(seen[0])), { title: "Delete profile", body: BODY, confirmLabel: "Delete", danger: true });
});

test("deleteProfileAsk puts the profile's own name in the question, quotes and all", async () => {
  let seen = null;
  const ctx = loadProfileForm(async (o) => { seen = o; return false; });
  await ctx.deleteProfileAsk('a "b" c');
  assert.match(seen.body, /^Delete the profile "a "b" c"\? Running sessions/);
});

test("it is exported for node, and is one of profileform.js's own functions", () => {
  const m = require("../static/profileform.js");
  assert.strictEqual(typeof m.deleteProfileAsk, "function");
  assert.match(read("profileform.js"), /\nfunction deleteProfileAsk\(name\) \{/);
});

test("the question's text is written once across the page scripts, and both callers ask deleteProfileAsk", () => {
  const files = fs.readdirSync(STATIC).filter((f) => /\.js$/.test(f));
  const holders = files.filter((f) => read(f).includes("Running sessions are unaffected. The profile\\'s saved workspace default"));
  assert.deepStrictEqual(holders, ["profileform.js"]);
  const count = (read("profileform.js").match(/Running sessions are unaffected\./g) || []).length;
  assert.strictEqual(count, 1, "once in profileform.js");
  for (const f of ["app.js", "profilepage.js"]) {
    const src = read(f);
    assert.match(src, /await deleteProfileAsk\(name\)/, f + " asks deleteProfileAsk");
    assert.ok(!/title: "Delete profile"/.test(src), f + " carries no copy of the dialog");
  }
});
