"use strict";
// Plan 7 final review I2: the 3 s sessions poll has a deadline and never stacks. app.js's api(), sys record,
// refreshSessions wrapper and refreshSessionsRun are lifted into a vm with a fake clock and a fetch that hangs.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const app = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
const deadline = fs.readFileSync(path.join(STATIC, "deadline.js"), "utf8").replace(/if \(typeof module[\s\S]*$/, "");
function cut(from, to) {
  const a = app.indexOf(from), b = app.indexOf(to, a);
  assert.ok(a >= 0 && b > a, from + " .. " + to);
  return app.slice(a, b);
}
const apiSrc = cut("function api(", "const $ = ");
const sysSrc = cut("const sys = {", "// A 401 on any call.").replace(/function sysPollOk[\s\S]*?\n}\n/, "function sysPollOk() { sys.lastOkAt = Date.now(); sys.failSince = null; sys.signedOut = false; }\n");
const nm = fs.readFileSync(path.join(STATIC, "notifymodel.js"), "utf8");
const nmSrc = nm.slice(nm.indexOf("const SYS_OFFLINE_MS"), nm.indexOf("\n}\n", nm.indexOf("function nmOffline")) + 3);
const wrapSrc = cut("const SESSIONS_DEADLINE_MS", "async function refreshSessionsRun()");

function clock() {
  let now = 1000; const timers = [];
  return {
    get now() { return now; },
    setTimeout(fn, ms) { timers.push({ at: now + ms, fn }); return timers.length; },
    async advance(ms) {
      const end = now + ms;
      for (;;) {
        await new Promise((r) => setImmediate(r));
        const due = timers.filter((t) => t.at <= end && !t.done).sort((a, b) => a.at - b.at)[0];
        if (!due) break;
        due.done = true; now = Math.max(now, due.at); due.fn();
      }
      now = end;
      await new Promise((r) => setImmediate(r));
    },
  };
}

test("a hanging /api/sessions fetch is a failed poll at the deadline, offline 12 s later, and never stacks", async () => {
  const clk = clock();
  const fetches = [];
  const ctx = {
    AbortController, AbortSignal: {}, Date: { now: () => clk.now }, setTimeout: clk.setTimeout.bind(clk), Array,
    window: { innerWidth: 1280 }, editing: false, replyFocused: false, storeUnlockFocused: () => false,
    sessionsLoaded: true, mobileActive: () => false, emptyLi: (t) => t, signalAttention() {}, sysNoteSignedOut() {},
    $: () => ({ replaceChildren() {} }),
    fetch(url, opts) {
      return new Promise((_, rej) => {
        fetches.push(url);
        opts.signal.addEventListener("abort", () => rej(new Error("aborted")));
      });
    },
  };
  vm.createContext(ctx);
  vm.runInContext(deadline + apiSrc + sysSrc + wrapSrc + cut("async function refreshSessionsRun()", "  const holdList") + "  const holdList = () => false;\n  let sessions;\n" +
    cut("  try { sessions = await api(", "  // A null/odd body") + "}\n" +
    "function sessionsFailedAt() { return sys.failSince; }", ctx);
  // wrapSrc ends before the run function; the run function above is only its head + the fetch block.
  const run = ctx.refreshSessions();
  assert.strictEqual(fetches.length, 1);
  ctx.sessionsTick(); ctx.sessionsTick();
  await clk.advance(3000); ctx.sessionsTick();
  assert.strictEqual(fetches.length, 1, "no second fetch while one is pending");
  assert.strictEqual(ctx.sessionsFailedAt(), null, "nothing recorded before the deadline");
  await clk.advance(7000);
  assert.strictEqual(await run, null);
  assert.strictEqual(ctx.sessionsFailedAt(), 11000, "the timeout is a failed poll recorded at the deadline");
  assert.strictEqual(vm.runInContext("sys.signedOut", ctx), false, "a timeout is never signed out");
  ctx.sessionsTick();
  assert.strictEqual(fetches.length, 2, "the next tick polls again once settled");
  await clk.advance(12000);
  const failSince = ctx.sessionsFailedAt();
  assert.strictEqual(failSince, 11000, "the first failure time is kept");
  const off = vm.runInContext(nmSrc + "(now) => nmOffline(sys, now)", ctx);
  assert.strictEqual(off(clk.now), true, "12 s after the first recorded failure: offline");
  assert.strictEqual(off(11000 + 11000), false, "not yet at 11 s");
});

test("refreshSessions: one fetch at a time, and an awaiting caller gets a list fetched after its call", async () => {
  const ctx = { log: [], gates: [] };
  vm.createContext(ctx);
  vm.runInContext(wrapSrc + `
let n = 0;
async function refreshSessionsRun() {
  const id = ++n; log.push("start" + id);
  await new Promise((r) => gates.push(r));
  log.push("end" + id);
  return ["list" + id];
}`, ctx);
  const flush = () => new Promise((r) => setImmediate(r));
  const tick = ctx.refreshSessions();           // fetch 1 pending
  ctx.sessionsTick();                           // skipped
  const killed = ctx.refreshSessions();         // a caller that changed something: needs fetch AFTER now
  const other = ctx.refreshSessions();          // coalesced into the same follow-up
  await flush();
  assert.strictEqual(ctx.log.join(), "start1");
  ctx.gates[0](); await flush(); await flush();
  assert.strictEqual(ctx.log.join(), "start1,end1,start2", "the follow-up starts when the first settles");
  assert.strictEqual(JSON.stringify(await tick), '["list1"]');
  ctx.gates[1](); await flush();
  assert.strictEqual(JSON.stringify(await killed), '["list2"]', "the caller sees the post-change list");
  assert.strictEqual(other, killed, "coalesced callers share the one follow-up");
  assert.strictEqual(ctx.log.filter((x) => x.startsWith("start")).length, 2, "two fetches for three calls");
  ctx.sessionsTick();
  assert.strictEqual(ctx.log[ctx.log.length - 1], "start3", "idle: a tick polls");
});
