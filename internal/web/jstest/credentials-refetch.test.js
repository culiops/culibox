"use strict";
// loadCredentials reads GET /api/credentials, paints it, then asks the profile
// page's reach view to refresh, which used to read /api/credentials again
// straight after. The refresh now takes the view just read. app.js needs a DOM, so
// refreshPfCreds is cut out of the source and run with stand-ins, and the hand-over
// is pinned at the source level, as settings-profiles-refetch.test.js does.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const APP = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
const CREDS = fs.readFileSync(path.join(STATIC, "credentials.js"), "utf8");

function fn(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

function refreshPfCredsIn({ desktop = true, answers = [] } = {}) {
  const calls = [];
  const box = {
    desktop, pfCredsView: "unset", pfCredsSeq: 0, painted: 0,
    drawerDesktop: () => box.desktop,
    deadlineSignal: () => undefined,
    pfvMetaChanged: () => { box.painted++; }, // the desktop profile page redraws from the new view
    api: async (p) => { calls.push(p); const a = answers.shift(); if (a instanceof Error) throw a; return a; },
  };
  const ctx = vm.createContext(box);
  new vm.Script(fn(APP, "refreshPfCreds") + "\nglobalThis.refreshPfCreds = refreshPfCreds;").runInContext(ctx);
  return { box, calls, run: (...a) => box.refreshPfCreds(...a) };
}

test("refreshPfCreds with the view loadCredentials just read does not fetch it again", async () => {
  const t = refreshPfCredsIn();
  const view = { store: { locked: false }, aws: [] };
  await t.run(view);
  assert.deepStrictEqual(t.calls, [], "no second GET /api/credentials");
  assert.strictEqual(t.box.pfCredsView, view);
  assert.strictEqual(t.box.painted, 1, "the profile page is told");
});

test("refreshPfCreds with nothing (the Profiles tab was shown) still fetches", async () => {
  const view = { store: { locked: true } };
  const t = refreshPfCredsIn({ answers: [view] });
  await t.run();
  assert.deepStrictEqual(t.calls, ["/api/credentials"]);
  assert.strictEqual(t.box.pfCredsView, view);
});

test("a failed fetch leaves the reach words without a Store part, as before", async () => {
  const t = refreshPfCredsIn({ answers: [new Error("down")] });
  await t.run();
  assert.strictEqual(t.box.pfCredsView, null);
});

test("a phone skips the refresh either way", async () => {
  const t = refreshPfCredsIn({ desktop: false });
  await t.run({ store: {} });
  assert.deepStrictEqual(t.calls, []);
  assert.strictEqual(t.box.pfCredsView, "unset");
});

test("a view handed over wins over a fetch still in flight", async () => {
  const older = { store: { locked: true } }, fresh = { store: { locked: false } };
  const t = refreshPfCredsIn({ answers: [older] });
  const slow = t.run();
  await t.run(fresh);
  await slow;
  assert.strictEqual(t.box.pfCredsView, fresh);
});

test("loadCredentials hands its view on, and refreshCredLists passes it to refreshPfCreds", () => {
  assert.match(fn(CREDS, "loadCredentials"), /refreshCredLists\(view\)/);
  const r = fn(APP, "refreshCredLists");
  assert.match(r, /^async function refreshCredLists\(view\)/);
  assert.match(r, /refreshPfCreds\(view\)/);
});
