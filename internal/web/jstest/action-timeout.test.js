"use strict";
// #257: a session action (End, Restart, Interrupt, Bring work home, + Shell) gives up after a bounded time. The row leaves
// its pending state, the user gets the standard "Could not <verb>." with Copy details, and a late server success changes
// nothing in the page: the next poll shows the truth. app.js cannot be require()d (it needs a DOM), so the real functions
// are lifted into a vm with a fake clock and a fetch that hangs, the way sessions-poll.test.js and toast-wiring.test.js do.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { toastFailText } = require("../static/toaststack.js");
const { withDeadline, actionTimeoutError, ACTION_TIMEOUT_MS, ACTION_SLOW_TIMEOUT_MS } = require("../static/deadline.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const APP = read("app.js");
const DEADLINE = read("deadline.js").replace(/if \(typeof module[\s\S]*$/, "");

function fnSrc(name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(APP);
  assert.ok(m, name + " not found in app.js");
  const open = APP.indexOf("{", APP.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < APP.length; i++) {
    if (APP[i] === "{") depth++;
    else if (APP[i] === "}" && --depth === 0) return APP.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}
const tick = async () => { for (let i = 0; i < 4; i++) await new Promise((r) => setImmediate(r)); };

// A fake clock: setTimeout/clearTimeout the code under test calls, and advance(ms) to run what came due.
function clock() {
  let now = 0, id = 0;
  const timers = new Map();
  return {
    setTimeout(fn, ms) { timers.set(++id, { at: now + ms, fn }); return id; },
    clearTimeout(i) { timers.delete(i); },
    get open() { return timers.size; },
    async advance(ms) {
      now += ms;
      for (const [i, t] of [...timers]) if (t.at <= now) { timers.delete(i); t.fn(); }
      await tick();
    },
  };
}

// A fetch that never answers on its own. Each call is kept: `answer` is the server's late reply, `aborted` says whether the
// page took the request back.
function hungFetch() {
  const calls = [];
  const fetch = (url, opts) => new Promise((resolve, reject) => {
    const c = { url, opts, aborted: false, answer: resolve };
    calls.push(c);
    opts.signal.addEventListener("abort", () => { c.aborted = true; reject(Object.assign(new Error("The operation was aborted."), { name: "AbortError" })); });
  });
  return { fetch, calls };
}

// --- deadline.js ---------------------------------------------------------------------------------------------------

test("withDeadline: a hung request is aborted and rejects with the timeout error at the deadline, not before", async () => {
  const clk = clock();
  const g = { AbortController, setTimeout: clk.setTimeout, clearTimeout: clk.clearTimeout };
  let signal = null;
  const p = withDeadline(60000, (s) => { signal = s; return new Promise(() => {}); }, g);
  let settled = null;
  p.then((v) => { settled = ["ok", v]; }, (e) => { settled = ["err", e]; });
  await clk.advance(59999);
  assert.strictEqual(settled, null, "still waiting one millisecond before the deadline");
  assert.strictEqual(signal.aborted, false);
  await clk.advance(1);
  assert.strictEqual(signal.aborted, true, "the request is taken back");
  assert.strictEqual(settled[0], "err");
  assert.strictEqual(settled[1].timedOut, true);
  assert.strictEqual(settled[1].message, "The server did not answer within 60 s.");
  assert.strictEqual(settled[1].status, undefined, "no status: toastFailText words it as the standard sentence");
});

test("withDeadline: a fetch that rejects with its AbortError the moment it is aborted still surfaces as the timeout", async () => {
  const clk = clock();
  const g = { AbortController, setTimeout: clk.setTimeout, clearTimeout: clk.clearTimeout };
  const p = withDeadline(1000, (signal) => new Promise((_, reject) => signal.addEventListener("abort", () => reject(Object.assign(new Error("aborted"), { name: "AbortError" })))), g);
  let err = null;
  p.catch((e) => { err = e; });
  await clk.advance(1000);
  assert.strictEqual(err && err.timedOut, true, "the caller sees the timeout, not the AbortError");
});

test("withDeadline: a request that ignores the signal still releases its caller", async () => {
  const clk = clock();
  const g = { AbortController, setTimeout: clk.setTimeout, clearTimeout: clk.clearTimeout };
  let err = null;
  withDeadline(1000, () => new Promise(() => {}), g).catch((e) => { err = e; });
  await clk.advance(1000);
  assert.strictEqual(err && err.timedOut, true);
});

test("withDeadline: an answer in time (or a refusal) passes through and the timer ends with it", async () => {
  const clk = clock();
  const g = { AbortController, setTimeout: clk.setTimeout, clearTimeout: clk.clearTimeout };
  assert.strictEqual(await withDeadline(1000, async () => "done", g), "done");
  assert.strictEqual(clk.open, 0, "no timer left behind");
  const refusal = Object.assign(new Error("no such session"), { status: 404 });
  await assert.rejects(withDeadline(1000, async () => { throw refusal; }, g), (e) => e === refusal && !e.timedOut);
  assert.strictEqual(clk.open, 0);
  await assert.rejects(withDeadline(1000, () => { throw refusal; }, g), (e) => e === refusal, "a run that throws at once is a rejection too");
});

test("withDeadline: the late answer after the deadline changes nothing (no unhandled rejection, no second result)", async () => {
  const clk = clock();
  const g = { AbortController, setTimeout: clk.setTimeout, clearTimeout: clk.clearTimeout };
  let answer;
  const results = [];
  withDeadline(1000, () => new Promise((r) => { answer = r; }), g).then((v) => results.push(["ok", v]), (e) => results.push(["err", e.timedOut]));
  await clk.advance(1000);
  answer("too late");
  await tick();
  assert.deepStrictEqual(results, [["err", true]]);
});

test("the bounds: End, interrupt and bring-home wait a minute; a restart or a new shell, which may start the box, two", () => {
  assert.strictEqual(ACTION_TIMEOUT_MS, 60000);
  assert.strictEqual(ACTION_SLOW_TIMEOUT_MS, 120000);
  assert.strictEqual(actionTimeoutError(120000).message, "The server did not answer within 120 s.");
});

test("the timeout error reads as the standard sentence with the raw text behind Copy details", () => {
  assert.deepStrictEqual(toastFailText("end docs", actionTimeoutError(60000)), { text: "Could not end docs.", detail: "The server did not answer within 60 s." });
});

// --- app.js, lifted ------------------------------------------------------------------------------------------------

function page(over) {
  const clk = clock();
  const hang = hungFetch();
  const out = { toasts: [], refreshes: 0, closed: [], pending: [] };
  const ctx = vm.createContext(Object.assign({
    AbortController, setTimeout: clk.setTimeout, clearTimeout: clk.clearTimeout, Promise: global.Promise, Date, Object, Error,
    encodeURIComponent, JSON, fetch: hang.fetch,
    sysNoteSignedOut() {},
    ACTION_TIMEOUT_MS, ACTION_SLOW_TIMEOUT_MS,
    lastSessions: [{ id: "s1", name: "docs" }],
    killing: new Set(), endedByMe: new Map(), restartedByMe: new Set(), endedMark: (m, id, t) => m.set(id, t),
    refreshSessions: async () => { out.refreshes++; },
    pendingToast: (text) => { out.pending.push(["pending", text]); return (r) => out.pending.push(["done", JSON.parse(JSON.stringify(r || null))]); },
    toastFailText, showToast: (msg, opts) => out.toasts.push([msg, JSON.parse(JSON.stringify(opts || {}))]),
    closeTerminal: (id) => out.closed.push(id),
    actionFailText: (a, s) => a + " failed (" + s + ").",
    spawnRefusedUnavailable: () => false, refreshStore() {},
  }, over));
  const src = [DEADLINE, fnSrc("api"), fnSrc("apiRun"), fnSrc("postSessionAction"), fnSrc("showActionTimeout"), fnSrc("actionSubject"),
    fnSrc("killSession"), fnSrc("restartSessionRun"), fnSrc("interruptSession"), fnSrc("bringHomeNow")].join("\n");
  new vm.Script(src + "\nObject.assign(globalThis, { api, postSessionAction, killSession, restartSessionRun, interruptSession, bringHomeNow });").runInContext(ctx);
  return { ctx, clk, hang, out };
}

test("End: a DELETE that never answers ends in the standard error; the row is no longer 'ending…' and the poll repaints", async () => {
  const { ctx, clk, hang, out } = page();
  const done = ctx.killSession("s1");
  await tick();
  assert.strictEqual(ctx.killing.has("s1"), true, "the row says 'ending…' while it waits");
  assert.strictEqual(hang.calls.length, 1);
  assert.deepStrictEqual([hang.calls[0].url, hang.calls[0].opts.method], ["/api/sessions/s1", "DELETE"]);
  await clk.advance(59999);
  assert.strictEqual(ctx.killing.has("s1"), true);
  await clk.advance(1);
  await done;
  assert.strictEqual(hang.calls[0].aborted, true);
  assert.strictEqual(ctx.killing.has("s1"), false, "killing is cleared: no reload needed");
  assert.strictEqual(ctx.endedByMe.has("s1"), false, "the session is not ours to announce as ended");
  assert.deepStrictEqual(out.pending[1], ["done", {
    msg: "End failed: The server did not answer within 60 s.",
    opts: { error: true, human: "Could not end docs.", detail: "The server did not answer within 60 s." },
  }], "the pending toast becomes the error (desktop: the sentence and Copy details; phone: the raw line)");
  assert.deepStrictEqual(out.toasts, [], "no second toast: the pending entry became it");
  assert.deepStrictEqual(out.closed, [], "the terminal stays: the session may still be running");
  assert.strictEqual(out.refreshes >= 2, true, "one refresh before the request, one after the timeout: the next poll shows the truth");
});

test("End: a late success after the timeout is silent; nothing in the page is changed by it", async () => {
  const { ctx, clk, hang, out } = page();
  const done = ctx.killSession("s1");
  await tick();
  await clk.advance(ACTION_TIMEOUT_MS);
  await done;
  const before = JSON.stringify([out.toasts, out.pending, out.closed, out.refreshes, [...ctx.killing]]);
  hang.calls[0].answer({ ok: true, status: 204, json: async () => null });
  await tick();
  assert.strictEqual(JSON.stringify([out.toasts, out.pending, out.closed, out.refreshes, [...ctx.killing]]), before);
});

test("End: a server that answers in time is unchanged: no timer left, the same result", async () => {
  const { ctx, clk, hang, out } = page();
  const done = ctx.killSession("s1");
  await tick();
  hang.calls[0].answer({ ok: true, status: 204, json: async () => null });
  await done;
  assert.strictEqual(clk.open, 0);
  assert.deepStrictEqual(out.pending[1], ["done", { msg: "Ended docs", opts: { desktopOnly: true } }]);
  assert.deepStrictEqual(out.closed, ["s1"]);
});

test("End: a refusal is still the refusal, not a timeout", async () => {
  const { ctx, hang, out } = page();
  const done = ctx.killSession("s1");
  await tick();
  hang.calls[0].answer({ ok: false, status: 500, statusText: "x", headers: { get: () => "" }, json: async () => ({ error: "kill failed" }) });
  await done;
  assert.deepStrictEqual(out.pending[1][1].opts, { error: true, human: "Could not end docs.", detail: "kill failed (HTTP 500)" });
});

test("Restart: a POST that never answers is 'Could not restart <name>.' with Copy details; the restart is not claimed or denied", async () => {
  const { ctx, clk, hang, out } = page();
  const done = ctx.restartSessionRun({ id: "s1", name: "docs" });
  await tick();
  assert.deepStrictEqual([hang.calls[0].url, hang.calls[0].opts.method], ["/api/sessions/s1/restart", "POST"]);
  await clk.advance(ACTION_TIMEOUT_MS);
  assert.strictEqual(hang.calls[0].aborted, false, "a restart may be starting a box: it gets the longer bound");
  await clk.advance(ACTION_SLOW_TIMEOUT_MS - ACTION_TIMEOUT_MS);
  await done;
  assert.strictEqual(hang.calls[0].aborted, true);
  assert.deepStrictEqual(out.toasts, [["Could not restart docs.", { error: true, detail: "The server did not answer within 120 s." }]]);
  assert.strictEqual(ctx.endedByMe.has("s1"), false);
  assert.strictEqual(ctx.restartedByMe.has("s1"), false, "the page does not claim a restart it never saw");
  assert.strictEqual(out.refreshes, 1, "the poll shows the new session if the server did restart it");
  assert.deepStrictEqual(out.closed, []);
});

test("Interrupt and Bring work home: the same bound and the same sentence", async () => {
  const a = page();
  const i = a.ctx.interruptSession("s1");
  await tick();
  assert.strictEqual(a.hang.calls[0].url, "/api/sessions/s1/interrupt");
  await a.clk.advance(ACTION_TIMEOUT_MS);
  await i;
  assert.deepStrictEqual(a.out.toasts, [["Could not interrupt docs.", { error: true, detail: "The server did not answer within 60 s." }]]);
  const b = page();
  const h = b.ctx.bringHomeNow({ id: "s1" });
  await tick();
  assert.strictEqual(b.hang.calls[0].url, "/api/sessions/s1/bring-home");
  await b.clk.advance(ACTION_TIMEOUT_MS);
  await h;
  assert.deepStrictEqual(b.out.toasts, [["Could not bring work home from docs.", { error: true, detail: "The server did not answer within 60 s." }]]);
});

test("postSessionAction: a failure that is not a timeout keeps its old shape (status 0, no timeout flag)", async () => {
  const { ctx } = page({ fetch: () => Promise.reject(new TypeError("Failed to fetch")) });
  const res = await ctx.postSessionAction("s1", "interrupt");
  assert.deepStrictEqual([res.ok, res.status, res.timedOut], [false, 0, undefined]);
});

test("api: without timeoutMs nothing changes (no signal, no timer); with it the request carries one", async () => {
  const a = page({ fetch: async (url, opts) => ({ ok: true, status: 200, json: async () => ({ url, signal: !!(opts && opts.signal) }) }) });
  assert.deepStrictEqual(JSON.parse(JSON.stringify(await a.ctx.api("/x"))), { url: "/x", signal: false });
  assert.strictEqual(a.clk.open, 0);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(await a.ctx.api("/x", { timeoutMs: 5 }))), { url: "/x", signal: true });
  assert.strictEqual(a.clk.open, 0, "the timer ends with the request");
});

test("the actions are wired to their bounds", () => {
  assert.match(fnSrc("killSession"), /method: "DELETE", timeoutMs: ACTION_TIMEOUT_MS/);
  assert.match(fnSrc("restartSessionRun"), /postSessionAction\(s\.id, "restart", ACTION_SLOW_TIMEOUT_MS\)/);
  assert.match(fnSrc("spawnShellRun"), /"\/shell", \{ method: "POST", timeoutMs: ACTION_SLOW_TIMEOUT_MS \}/);
  assert.match(fnSrc("postSessionAction"), /ms = ACTION_TIMEOUT_MS/);
  const html = read("index.html");
  assert.ok(html.indexOf('<script src="/deadline.js"></script>') > 0 && html.indexOf('<script src="/deadline.js"></script>') < html.indexOf('<script src="/app.js"></script>'));
});
