"use strict";
// Applying the VM folder list (#70 Task 14): the confirm copy, the progress and banner models (M9.6-M9.8, word by word) and the
// global poller, driven with fake timers.
const test = require("node:test");
const assert = require("node:assert");
const A = require("../static/vmapply.js");

const T0 = 1_700_000_000_000;
// started_ms is deliberately far from the browser clock: the card must count from elapsed_ms (server) and seen_ms (this browser).
const RUNNING = {
  id: 1, state: "running", started_ms: 5, elapsed_ms: 10000, seen_ms: T0 + 10000, error: "", detail: "",
  steps: [
    { name: "stop", state: "done", sub: "culibox and infra-audit ended", ms: 2000 },
    { name: "start", state: "running", sub: "Usually about 16 s", ms: 0 },
    { name: "check", state: "waiting", sub: "The firewall and folder checks that run after every start", ms: 0 },
  ],
};
const FAILED = {
  id: 1, state: "failed", started_ms: T0, error: "The VM did not start within 2 minutes.", detail: "colima start: timed out",
  steps: [
    { name: "stop", state: "done", sub: "culibox and infra-audit ended", ms: 2000 },
    { name: "start", state: "failed", sub: "Usually about 16 s", ms: 120000 },
    { name: "check", state: "not_run", sub: "The firewall and folder checks that run after every start", ms: 0 },
  ],
};

test("vmaFmt: seconds, then minutes and seconds", () => {
  assert.strictEqual(A.vmaFmt(0), "0 s");
  assert.strictEqual(A.vmaFmt(2000), "2 s");
  assert.strictEqual(A.vmaFmt(59999), "59 s");
  assert.strictEqual(A.vmaFmt(65000), "1 m 5 s");
  assert.strictEqual(A.vmaFmt(120000), "2 m 0 s");
  assert.strictEqual(A.vmaFmt(-5), "0 s");
});

test("M9.6 confirm copy: two sessions, one, none", () => {
  const c = A.vmApplyConfirmCopy(["culibox", "infra-audit"]);
  assert.strictEqual(c.title, "Restart the box VM?");
  assert.strictEqual(c.body, "2 sessions are running in boxes; they will end: culibox and infra-audit.");
  assert.deepStrictEqual(c.notes, [
    ["The VM comes back with the new folder list in about 20 seconds. Your images are kept. Files in the workspaces are not touched."],
    ["Host sessions keep running. New box sessions are refused until the VM is back."],
  ]);
  assert.strictEqual(c.confirmLabel, "Restart box VM");
  assert.strictEqual(c.hint, "Esc to cancel");
  assert.strictEqual(A.vmApplyConfirmCopy(["a", "b", "c"]).body, "3 sessions are running in boxes; they will end: a, b and c.");
  assert.strictEqual(A.vmApplyConfirmCopy(["culibox"]).body, "1 session is running in a box; it will end: culibox.");
  assert.strictEqual(A.vmApplyConfirmCopy([]).body, "No box sessions are running.");
  assert.strictEqual(A.vmApplyConfirmCopy(undefined).body, "No box sessions are running.");
});

test("banner model: running, failed, done, none", () => {
  assert.deepStrictEqual(A.vmApplyBannerModel(RUNNING), { kind: "vmapply", title: "Box VM restarting", body: "New box sessions are refused until it is back, about 20 s.", actions: [] });
  assert.deepStrictEqual(A.vmApplyBannerModel(FAILED), { kind: "vmapply", title: "Box VM stopped", body: "Box sessions can’t start.", actions: [{ id: "open-vmfolders", label: "Open VM folders" }] });
  assert.strictEqual(A.vmApplyBannerModel({ id: 1, state: "done", steps: [] }), null);
  assert.strictEqual(A.vmApplyBannerModel(null), null);
});

test("M9.7 progress model: step 1 done 2 s, step 2 spinner 10 s, step 3 Waiting, elapsed 12 s", () => {
  const m = A.vmApplyProgressModel(RUNNING, T0 + 12000); // answered at 10 s, 2 s ago here: 12 s
  assert.strictEqual(m.head, "Restarting the box VM");
  assert.strictEqual(m.elapsed, "12 s");
  assert.deepStrictEqual(m.steps.map((s) => [s.n, s.glyph, s.title, s.right, s.red]), [
    [1, "✓", "Stop the box VM", "2 s", false],
    [2, "spin", "Start it with the new folders", "10 s", false],
    [3, "3", "Check containment", "Waiting", false],
  ]);
  assert.strictEqual(m.steps[0].sub, "culibox and infra-audit ended");
  assert.strictEqual(m.steps[1].sub, "Usually about 16 s");
  assert.strictEqual(m.steps[2].sub, "The firewall and folder checks that run after every start");
  assert.strictEqual(m.foot, "New box sessions are refused until this finishes. Host sessions are not affected. You can leave this page; the restart goes on.");
});

test("M9.8 progress model: failed step red with the short error, 2 m 0 s, step 3 Not run", () => {
  const m = A.vmApplyProgressModel(FAILED, T0 + 999999);
  assert.strictEqual(m.head, "The box VM did not start");
  assert.strictEqual(m.elapsed, "");
  assert.deepStrictEqual(m.steps.map((s) => [s.glyph, s.right, s.red]), [["✓", "2 s", false], ["✕", "2 m 0 s", true], ["3", "Not run", false]]);
  assert.strictEqual(m.steps[1].sub, "The VM did not start within 2 minutes.");
  assert.strictEqual(m.foot, "Your folder list is saved. The box VM is stopped, so box sessions can’t start until it runs.");
  assert.strictEqual(A.vmApplyProgressModel({ id: 1, state: "done", steps: [] }, T0), null);
});

const failedAt = (name) => {
  const names = ["stop", "start", "check"];
  const at = names.indexOf(name);
  return {
    id: 1, state: "failed", started_ms: T0, error: "It broke.", detail: "raw",
    steps: names.map((n, i) => ({ name: n, state: i < at ? "done" : i === at ? "failed" : "not_run", sub: "", ms: i <= at ? 1000 : 0 })),
  };
};

test("failure copy by step: only a failed start says the VM is stopped (head, foot, banner)", () => {
  const start = failedAt("start");
  assert.strictEqual(A.vmApplyProgressModel(start, T0).head, "The box VM did not start");
  assert.strictEqual(A.vmApplyProgressModel(start, T0).foot, "Your folder list is saved. The box VM is stopped, so box sessions can’t start until it runs.");
  assert.strictEqual(A.vmApplyBannerModel(start).title, "Box VM stopped");
  assert.strictEqual(A.vmApplyBannerModel(start).body, "Box sessions can’t start.");
  for (const name of ["stop", "check"]) {
    const j = failedAt(name);
    const m = A.vmApplyProgressModel(j, T0);
    assert.strictEqual(m.head, "The folder list was not applied", name);
    assert.strictEqual(m.foot, "Your folder list is saved. Try again, or run it from a terminal.", name);
    assert.strictEqual(m.steps.filter((s) => s.red).length, 1);
    const b = A.vmApplyBannerModel(j);
    assert.strictEqual(b.title, "Box VM folders not applied", name);
    assert.strictEqual(b.body, "Open VM folders to see why.", name);
    assert.deepStrictEqual(b.actions, [{ id: "open-vmfolders", label: "Open VM folders" }]);
  }
});

// ---- the poller -------------------------------------------------------------------------------------------------------------

const flush = () => new Promise((r) => setImmediate(r));

test("poller: polls every second while running and stops on done", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: T0 });
  A._reset();
  const answers = [RUNNING, RUNNING, { id: 1, state: "done", started_ms: T0, steps: [] }];
  let calls = 0;
  globalThis.api = async (path) => { assert.strictEqual(path, "/api/vm-folders/apply"); return answers[Math.min(calls++, 2)]; };
  t.after(() => { A._reset(); delete globalThis.api; });
  A.startVMApplyPoll(RUNNING);
  assert.strictEqual(A.vmApplyCurrent().state, "running");
  t.mock.timers.tick(1000); await flush();
  t.mock.timers.tick(1000); await flush();
  assert.strictEqual(calls, 2);
  assert.strictEqual(A.vmApplyCurrent().state, "running");
  t.mock.timers.tick(1000); await flush();
  assert.strictEqual(calls, 3);
  assert.strictEqual(A.vmApplyCurrent().state, "done");
  t.mock.timers.tick(5000); await flush();
  assert.strictEqual(calls, 3, "no poll after the terminal state");
});

test("poller: stops on failed; a failed request does not stop it", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: T0 });
  A._reset();
  let calls = 0;
  globalThis.api = async () => { calls++; if (calls === 1) throw Object.assign(new Error("x"), { status: 502 }); return FAILED; };
  t.after(() => { A._reset(); delete globalThis.api; });
  A.startVMApplyPoll(RUNNING);
  t.mock.timers.tick(1000); await flush();
  assert.strictEqual(A.vmApplyCurrent().state, "running");
  t.mock.timers.tick(1000); await flush();
  assert.strictEqual(A.vmApplyCurrent().state, "failed");
  t.mock.timers.tick(5000); await flush();
  assert.strictEqual(calls, 2);
});

test("poller: gives up after 5 minutes; a second start while polling does not double the timer", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: T0 });
  A._reset();
  let calls = 0;
  globalThis.api = async () => { calls++; return RUNNING; };
  t.after(() => { A._reset(); delete globalThis.api; });
  A.startVMApplyPoll(RUNNING);
  A.startVMApplyPoll();
  t.mock.timers.tick(1000); await flush();
  assert.strictEqual(calls, 1, "one timer, one request per second");
  for (let i = 0; i < 305; i++) { t.mock.timers.tick(1000); await flush(); }
  const at = calls;
  assert.ok(at <= 301, "stopped near 5 min, got " + at);
  t.mock.timers.tick(10000); await flush();
  assert.strictEqual(calls, at);
});

test("meta: a running job starts the poller; a failed one is shown without polling; an older one never replaces a newer", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: T0 });
  A._reset();
  let calls = 0;
  globalThis.api = async () => { calls++; return Object.assign({}, RUNNING, { id: 2 }); };
  t.after(() => { A._reset(); delete globalThis.api; });
  A.vmaFromMeta(FAILED);
  assert.strictEqual(A.vmApplyCurrent().state, "failed");
  t.mock.timers.tick(3000); await flush();
  assert.strictEqual(calls, 0);
  A.vmaFromMeta(Object.assign({}, RUNNING, { id: 2 }));
  assert.strictEqual(A.vmApplyCurrent().id, 2);
  t.mock.timers.tick(1000); await flush();
  assert.strictEqual(calls, 1);
  A.vmaFromMeta(FAILED); // job 1 again: older
  assert.strictEqual(A.vmApplyCurrent().id, 2);
  assert.strictEqual(A.vmApplyCurrent().state, "running");
});

test("the side banner is the model for the current job", () => {
  A._reset();
  assert.strictEqual(A.vmApplySideModel(), null);
  A.vmaSeen(FAILED);
  assert.strictEqual(A.vmApplySideModel().title, "Box VM stopped");
  A._reset();
});

test("server restart mid-apply: the poll gets 404, the job and the banner go", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: T0 });
  A._reset();
  globalThis.api = async () => { throw Object.assign(new Error("no apply has run"), { status: 404 }); };
  t.after(() => { A._reset(); delete globalThis.api; });
  A.startVMApplyPoll(RUNNING);
  assert.strictEqual(A.vmApplySideModel().title, "Box VM restarting");
  t.mock.timers.tick(1000); await flush();
  assert.strictEqual(A.vmApplyCurrent(), null);
  assert.strictEqual(A.vmApplySideModel(), null);
  t.mock.timers.tick(5000); await flush(); // and the poller stopped
});

test("null meta after a failed job drops it; a running job is not dropped by null meta (one poll tells how)", async (t) => {
  t.mock.timers.enable({ apis: ["setInterval", "Date"], now: T0 });
  A._reset();
  let calls = 0;
  globalThis.api = async () => { calls++; return { id: 1, state: "done", steps: [] }; };
  t.after(() => { A._reset(); delete globalThis.api; });
  A.vmaFromMeta(FAILED);
  assert.strictEqual(A.vmApplySideModel().title, "Box VM stopped");
  A.vmaFromMeta(null);
  assert.strictEqual(A.vmApplyCurrent(), null);
  assert.strictEqual(A.vmApplySideModel(), null);
  A.vmaFromMeta(RUNNING);
  A.vmaFromMeta(null);
  assert.strictEqual(A.vmApplyCurrent().state, "running");
  t.mock.timers.tick(1000); await flush();
  assert.ok(calls >= 1);
  assert.strictEqual(A.vmApplyCurrent().state, "done");
});

test("elapsed counts server time plus the local time since the answer, whatever started_ms says", () => {
  const j = Object.assign({}, RUNNING, { started_ms: 1, elapsed_ms: 3000, seen_ms: T0 });
  assert.strictEqual(A.vmApplyProgressModel(j, T0).elapsed, "3 s");
  assert.strictEqual(A.vmApplyProgressModel(j, T0 + 4000).elapsed, "7 s");
});
