"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { createToastStack, toastFailText } = require("../static/toaststack.js");

// F60/F61: the toast stack as data. No DOM and no timers inside the model: every
// call that cares about time takes `now` (a plain number here, a fake clock), so
// each rule below is a plain assertion. Task 7 renders list() and arms one
// setTimeout from nextDeadline().

const texts = (s) => s.list().map((t) => t.text);
const byText = (s, text) => s.list().find((t) => t.text === text);

test("newest first", () => {
  const s = createToastStack();
  s.push({ text: "a", kind: "info" }, 0);
  s.push({ text: "b", kind: "info" }, 0);
  s.push({ text: "c", kind: "info" }, 0);
  assert.deepStrictEqual(texts(s), ["c", "b", "a"]);
});

test("push returns a positive integer id, list entries carry the full shape", () => {
  const s = createToastStack();
  const id = s.push({ text: "a", kind: "info" }, 1000);
  assert.ok(Number.isInteger(id) && id > 0);
  assert.deepStrictEqual(s.list(), [
    { id, text: "a", kind: "info", undo: false, detail: "", progress: null, deadline: 7000 },
  ]);
});

test("a spec without a kind is an info toast", () => {
  const s = createToastStack();
  s.push({ text: "a" }, 0);
  s.push({ text: "b", kind: "bogus" }, 0);
  assert.strictEqual(byText(s, "a").kind, "info");
  assert.strictEqual(byText(s, "b").kind, "info");
});

test("info and warn expire at 6 s", () => {
  const s = createToastStack();
  const i = s.push({ text: "i", kind: "info" }, 1000);
  const w = s.push({ text: "w", kind: "warn" }, 1000);
  assert.strictEqual(byText(s, "i").deadline, 7000);
  assert.strictEqual(byText(s, "w").deadline, 7000);
  assert.strictEqual(s.nextDeadline(), 7000);
  assert.deepStrictEqual(s.expire(6999), []);
  assert.deepStrictEqual(s.expire(7000).sort(), [i, w].sort());
  assert.deepStrictEqual(s.list(), []);
  assert.strictEqual(s.nextDeadline(), null);
});

test("spec.ms and the stack ms set the timer length", () => {
  const s = createToastStack({ ms: 1000 });
  s.push({ text: "stack", kind: "info" }, 0);
  s.push({ text: "own", kind: "warn", ms: 2500 }, 0);
  assert.strictEqual(byText(s, "stack").deadline, 1000);
  assert.strictEqual(byText(s, "own").deadline, 2500);
});

// setTimeout holds a signed 32-bit delay: Infinity, NaN or anything past 2^31-1 ms
// fires at once, expire() removes nothing (the deadline is still ahead) and the page
// re-arms the same timer, a loop for as long as the entry is listed. Such a length,
// like 0 or a negative one, falls back to the default.
test("a timer length setTimeout cannot hold falls back to the default", () => {
  const s = createToastStack({ ms: 1000 });
  for (const ms of [Infinity, -Infinity, NaN, 2 ** 31, 0, -5, "9000"]) s.push({ text: "t" + String(ms), kind: "info", ms }, 0);
  for (const t of s.list()) assert.strictEqual(t.deadline, 1000, t.text);
  const max = createToastStack();
  max.push({ text: "max", kind: "warn", ms: 2 ** 31 - 1 }, 0);
  assert.strictEqual(byText(max, "max").deadline, 2 ** 31 - 1, "the largest delay setTimeout holds is kept");
  const id = max.push({ text: "u", kind: "info" }, 0);
  max.update(id, { text: "u", kind: "info", ms: Infinity }, 10);
  assert.strictEqual(byText(max, "u").deadline, 10 + 6000, "update clamps too");
  for (const bad of [Infinity, 2 ** 31, NaN]) {
    const d = createToastStack({ ms: bad });
    d.push({ text: "d", kind: "info" }, 0);
    assert.strictEqual(byText(d, "d").deadline, 6000, "the stack's own default: " + bad);
  }
});

test("error and pending never expire", () => {
  const s = createToastStack();
  s.push({ text: "e", kind: "error" }, 0);
  s.push({ text: "p", kind: "pending" }, 0);
  assert.strictEqual(byText(s, "e").deadline, null);
  assert.strictEqual(byText(s, "p").deadline, null);
  assert.strictEqual(s.nextDeadline(), null);
  assert.deepStrictEqual(s.expire(1e12), []);
  assert.deepStrictEqual(texts(s), ["p", "e"]);
});

test("cap 3 evicts the oldest plain toast first, then the oldest error, never an undo or pending while another kind exists", () => {
  const s = createToastStack();
  s.push({ text: "P", kind: "pending" }, 0);
  s.push({ text: "E1", kind: "error" }, 0);
  s.push({ text: "I1", kind: "info" }, 0);
  assert.deepStrictEqual(texts(s), ["I1", "E1", "P"]);

  // A plain toast is the first to go, even though the error is older.
  s.push({ text: "I2", kind: "info" }, 0);
  assert.deepStrictEqual(texts(s), ["I2", "E1", "P"]);
  s.push({ text: "W1", kind: "warn" }, 0);
  assert.deepStrictEqual(texts(s), ["W1", "E1", "P"]);

  // No plain toast is left to evict: now the oldest error goes.
  s.push({ text: "E2", kind: "error" }, 0);
  s.push({ text: "E3", kind: "error" }, 0);
  assert.deepStrictEqual(texts(s), ["E3", "E2", "P"]);
  s.push({ text: "E4", kind: "error" }, 0);
  assert.deepStrictEqual(texts(s), ["E4", "E3", "P"]);

  // An undo is protected like a pending entry: a plain toast, then an error, go first.
  s.push({ text: "U", kind: "info", undo: true }, 0);
  assert.deepStrictEqual(texts(s), ["U", "E4", "P"]);
  s.push({ text: "I3", kind: "info" }, 0);
  assert.deepStrictEqual(texts(s), ["I3", "U", "P"]);
  s.push({ text: "I4", kind: "info" }, 0);
  assert.deepStrictEqual(texts(s), ["I4", "U", "P"]);
});

test("an error that carries an undo is protected like any undo", () => {
  const s = createToastStack();
  s.push({ text: "EU", kind: "error", undo: true }, 0);
  s.push({ text: "E", kind: "error" }, 0);
  s.push({ text: "I", kind: "info" }, 0);
  assert.deepStrictEqual(texts(s), ["I", "E", "EU"]);
  s.push({ text: "E2", kind: "error" }, 0); // plain I goes
  assert.deepStrictEqual(texts(s), ["E2", "E", "EU"]);
  s.push({ text: "E3", kind: "error" }, 0); // the oldest unprotected error goes, not EU
  assert.deepStrictEqual(texts(s), ["E3", "E2", "EU"]);
});

test("when every old entry is undo or pending, the oldest of them goes", () => {
  // Documented limit: this needs `max` undo/pending entries already listed when
  // another toast arrives (3 by default), the 4th concurrent one counting the new push.
  const s = createToastStack();
  s.push({ text: "P", kind: "pending" }, 0);
  s.push({ text: "U1", kind: "info", undo: true }, 0);
  s.push({ text: "U2", kind: "info", undo: true }, 0);
  assert.deepStrictEqual(texts(s), ["U2", "U1", "P"]);
  s.push({ text: "I", kind: "info" }, 0);
  assert.deepStrictEqual(texts(s), ["I", "U2", "U1"]);
});

test("a new toast always shows", () => {
  const s = createToastStack();
  for (const kind of ["error", "pending", "info"]) {
    s.push({ text: "x", kind, undo: kind === "info" }, 0);
  }
  for (const spec of [
    { text: "n1", kind: "info" },
    { text: "n2", kind: "error" },
    { text: "n3", kind: "pending" },
    { text: "n4", kind: "warn", undo: true },
  ]) {
    s.push(spec, 0);
    assert.strictEqual(s.list()[0].text, spec.text);
    assert.ok(s.list().length <= 3);
  }
  // Even a cap of 1, with only a protected entry listed.
  const one = createToastStack({ max: 1 });
  one.push({ text: "U", kind: "info", undo: true }, 0);
  one.push({ text: "I", kind: "info" }, 0);
  assert.deepStrictEqual(texts(one), ["I"]);
});

test("undo is pushed down, not replaced", () => {
  const s = createToastStack();
  const u = s.push({ text: "Started claude", kind: "info", undo: true }, 0);
  s.push({ text: "i1", kind: "info" }, 100);
  s.push({ text: "i2", kind: "info" }, 200);
  s.push({ text: "i3", kind: "info" }, 300);
  assert.deepStrictEqual(texts(s), ["i3", "i2", "Started claude"]);
  assert.strictEqual(s.list()[2].id, u);
  assert.strictEqual(s.list()[2].undo, true);
  // The Undo keeps its own 6 s timer from the moment it was pushed.
  assert.strictEqual(s.list()[2].deadline, 6000);
  assert.ok(s.expire(6000).includes(u));
});

test("dedupe: same text and kind moves to the top and restarts its timer, same id", () => {
  const s = createToastStack();
  const a = s.push({ text: "Copied", kind: "info" }, 0);
  const b = s.push({ text: "Saved", kind: "info" }, 1000);
  assert.deepStrictEqual(texts(s), ["Saved", "Copied"]);
  const again = s.push({ text: "Copied", kind: "info" }, 3000);
  assert.strictEqual(again, a);
  assert.deepStrictEqual(texts(s), ["Copied", "Saved"]);
  assert.strictEqual(s.list()[0].id, a);
  assert.strictEqual(s.list()[0].deadline, 9000);
  assert.strictEqual(s.list()[1].id, b);
  assert.strictEqual(s.list()[1].deadline, 7000);
});

test("dedupe makes no new entry and evicts nothing, errors included", () => {
  const s = createToastStack();
  s.push({ text: "e", kind: "error" }, 0);
  s.push({ text: "b", kind: "info" }, 0);
  s.push({ text: "c", kind: "info" }, 0);
  const e = s.push({ text: "e", kind: "error" }, 5);
  assert.deepStrictEqual(texts(s), ["e", "c", "b"]);
  assert.strictEqual(s.list()[0].id, e);
  assert.strictEqual(s.list()[0].deadline, null);
});

test("dedupe takes the latest push's detail and progress", () => {
  const s = createToastStack();
  const id = s.push({ text: "Could not stop claude.", kind: "error", detail: "timeout" }, 0);
  const again = s.push({ text: "Could not stop claude.", kind: "error", detail: "refused (HTTP 500)" }, 1);
  assert.strictEqual(again, id);
  assert.strictEqual(s.list()[0].detail, "refused (HTTP 500)");
});

test("dedupe never merges a pending entry, an undo entry, a push that carries an undo, or another kind", () => {
  const s = createToastStack({ max: 10 });
  const p1 = s.push({ text: "Ending claude…", kind: "pending" }, 0);
  const p2 = s.push({ text: "Ending claude…", kind: "pending" }, 0);
  assert.notStrictEqual(p1, p2);

  const u1 = s.push({ text: "Started", kind: "info", undo: true }, 0);
  const u2 = s.push({ text: "Started", kind: "info" }, 0);
  assert.notStrictEqual(u1, u2);

  const plain = s.push({ text: "Done", kind: "info" }, 0);
  const withUndo = s.push({ text: "Done", kind: "info", undo: true }, 0);
  assert.notStrictEqual(plain, withUndo);

  const info = s.push({ text: "Same words", kind: "info" }, 0);
  const warn = s.push({ text: "Same words", kind: "warn" }, 0);
  assert.notStrictEqual(info, warn);
  assert.strictEqual(s.list().length, 8);
});

test("update turns pending into its result in place and starts the result's timer", () => {
  const s = createToastStack();
  s.push({ text: "a", kind: "info" }, 0);
  const p = s.push({ text: "Ending claude…", kind: "pending" }, 0);
  s.push({ text: "c", kind: "info" }, 0);
  assert.strictEqual(byText(s, "Ending claude…").deadline, null);

  assert.strictEqual(s.update(p, { text: "Ended claude", kind: "info" }, 5000), true);
  assert.deepStrictEqual(texts(s), ["c", "Ended claude", "a"]);
  const ended = s.list()[1];
  assert.strictEqual(ended.id, p);
  assert.strictEqual(ended.kind, "info");
  assert.strictEqual(ended.deadline, 11000);
  assert.ok(s.expire(11000).includes(p));
});

test("update restarts the timer by the new kind's rule", () => {
  const s = createToastStack();
  const id = s.push({ text: "Ending claude…", kind: "pending" }, 0);
  s.update(id, { text: "Could not end claude: busy", kind: "error", detail: "busy (HTTP 409)" }, 1000);
  assert.strictEqual(s.list()[0].deadline, null);
  assert.strictEqual(s.list()[0].detail, "busy (HTTP 409)");
  assert.deepStrictEqual(s.expire(1e12), []);
  // error -> info starts a fresh 6 s from `now`
  s.update(id, { text: "Retried", kind: "info" }, 20000);
  assert.strictEqual(s.list()[0].deadline, 26000);
  // info -> warn restarts it again
  s.update(id, { text: "Retried", kind: "warn", ms: 1000 }, 21000);
  assert.strictEqual(s.list()[0].deadline, 22000);
});

test("update replaces the content: an omitted undo, detail or progress is cleared", () => {
  const s = createToastStack();
  const id = s.push({ text: "a", kind: "info", undo: true, detail: "d", progress: 0.5 }, 0);
  s.update(id, { text: "b", kind: "info" }, 0);
  assert.deepStrictEqual(s.list()[0], { id, text: "b", kind: "info", undo: false, detail: "", progress: null, deadline: 6000 });
});

test("update with progress keeps the id", () => {
  const s = createToastStack();
  const id = s.push({ text: "Uploading a.zip", kind: "pending", progress: 0 }, 0);
  assert.strictEqual(s.list()[0].progress, 0);
  assert.strictEqual(s.update(id, { text: "Uploading a.zip", kind: "pending", progress: 0.4 }, 100), true);
  assert.strictEqual(s.update(id, { text: "Uploading a.zip", kind: "pending", progress: 0.9 }, 200), true);
  assert.strictEqual(s.list().length, 1);
  assert.strictEqual(s.list()[0].id, id);
  assert.strictEqual(s.list()[0].progress, 0.9);
});

test("progress is clamped to 0..1 and a non-number is none", () => {
  const s = createToastStack({ max: 10 });
  s.push({ text: "hi", kind: "info", progress: 3 }, 0);
  s.push({ text: "lo", kind: "info", progress: -1 }, 0);
  s.push({ text: "nan", kind: "info", progress: NaN }, 0);
  s.push({ text: "str", kind: "info", progress: "0.5" }, 0);
  assert.strictEqual(byText(s, "hi").progress, 1);
  assert.strictEqual(byText(s, "lo").progress, 0);
  assert.strictEqual(byText(s, "nan").progress, null);
  assert.strictEqual(byText(s, "str").progress, null);
});

test("update of an unknown or removed id is false and changes nothing", () => {
  const s = createToastStack();
  const id = s.push({ text: "a", kind: "info" }, 0);
  assert.strictEqual(s.update(999, { text: "x", kind: "info" }, 0), false);
  s.remove(id);
  assert.strictEqual(s.update(id, { text: "x", kind: "info" }, 0), false);
  assert.deepStrictEqual(s.list(), []);
});

test("remove returns whether the entry existed", () => {
  const s = createToastStack();
  const a = s.push({ text: "a", kind: "info" }, 0);
  const b = s.push({ text: "b", kind: "error" }, 0);
  assert.strictEqual(s.remove(a), true);
  assert.strictEqual(s.remove(a), false);
  assert.strictEqual(s.remove(12345), false);
  assert.deepStrictEqual(s.list().map((t) => t.id), [b]);
  assert.strictEqual(s.nextDeadline(), null);
});

test("ids are positive integers and never reused", () => {
  const s = createToastStack();
  const seen = new Set();
  for (let i = 0; i < 20; i++) {
    const id = s.push({ text: "t" + i, kind: "info" }, 0);
    assert.ok(Number.isInteger(id) && id > 0);
    assert.ok(!seen.has(id));
    seen.add(id);
    if (i % 2) s.remove(id);
  }
  s.expire(1e9);
  const id = s.push({ text: "late", kind: "info" }, 1e9);
  assert.ok(!seen.has(id));
});

test("nextDeadline is the earliest deadline of any timed entry", () => {
  const s = createToastStack();
  assert.strictEqual(s.nextDeadline(), null);
  s.push({ text: "late", kind: "info", ms: 9000 }, 0);
  s.push({ text: "soon", kind: "info", ms: 2000 }, 0);
  s.push({ text: "none", kind: "error" }, 0);
  assert.strictEqual(s.nextDeadline(), 2000);
});

test("pause freezes remaining time, resume restarts it", () => {
  const s = createToastStack();
  const id = s.push({ text: "a", kind: "info" }, 0); // would expire at 6000
  assert.strictEqual(s.paused, false);
  s.pause(2000); // 4000 ms left
  assert.strictEqual(s.paused, true);
  assert.strictEqual(s.nextDeadline(), null);
  assert.strictEqual(s.list()[0].deadline, null);
  assert.deepStrictEqual(s.expire(1e9), []);
  assert.strictEqual(s.list().length, 1);
  s.resume(60000);
  assert.strictEqual(s.paused, false);
  assert.strictEqual(s.list()[0].deadline, 64000);
  assert.strictEqual(s.nextDeadline(), 64000);
  assert.deepStrictEqual(s.expire(63999), []);
  assert.deepStrictEqual(s.expire(64000), [id]);
});

test("pause keeps each entry's own remaining time and leaves untimed ones alone", () => {
  const s = createToastStack();
  s.push({ text: "long", kind: "info", ms: 10000 }, 0);
  s.push({ text: "short", kind: "info", ms: 3000 }, 0);
  s.push({ text: "err", kind: "error" }, 0);
  s.pause(1000); // long 9000 left, short 2000 left
  s.resume(5000);
  assert.strictEqual(byText(s, "long").deadline, 14000);
  assert.strictEqual(byText(s, "short").deadline, 7000);
  assert.strictEqual(byText(s, "err").deadline, null);
});

test("pause and resume are idempotent", () => {
  const s = createToastStack();
  s.push({ text: "a", kind: "info" }, 0);
  s.pause(2000);
  s.pause(5000); // must not shrink the remaining 4000 further
  s.resume(10000);
  s.resume(20000); // must not push the deadline out again
  assert.strictEqual(s.list()[0].deadline, 14000);
});

test("pushes and updates while paused store the remaining time, not a deadline", () => {
  const s = createToastStack();
  s.pause(1000);
  const a = s.push({ text: "a", kind: "info" }, 2000);
  const p = s.push({ text: "p", kind: "pending" }, 2000);
  assert.strictEqual(byText(s, "a").deadline, null);
  assert.strictEqual(s.nextDeadline(), null);
  s.update(p, { text: "p done", kind: "info" }, 3000);
  assert.strictEqual(byText(s, "p done").deadline, null);
  s.resume(10000);
  assert.strictEqual(byText(s, "a").deadline, 16000);
  assert.strictEqual(byText(s, "p done").deadline, 16000);
  assert.deepStrictEqual(s.expire(16000).sort(), [a, p].sort());
});

test("a dedupe while paused restarts the timer in full, from resume", () => {
  const s = createToastStack();
  const a = s.push({ text: "a", kind: "info" }, 0);
  s.pause(5000); // 1000 left
  assert.strictEqual(s.push({ text: "a", kind: "info" }, 5500), a);
  s.resume(8000);
  assert.strictEqual(s.list()[0].deadline, 14000);
});

test("an entry that was overdue when paused expires as soon as the stack resumes", () => {
  const s = createToastStack();
  const id = s.push({ text: "a", kind: "info" }, 0);
  s.pause(9000); // overdue by 3000: nobody called expire yet
  s.resume(10000);
  assert.deepStrictEqual(s.expire(10000), [id]);
});

test("expire returns removed ids, top to bottom, and only those", () => {
  const s = createToastStack({ max: 10 });
  const a = s.push({ text: "a", kind: "info", ms: 1000 }, 0);
  const keep = s.push({ text: "keep", kind: "info", ms: 9000 }, 0);
  const err = s.push({ text: "err", kind: "error" }, 0);
  const b = s.push({ text: "b", kind: "warn", ms: 1000 }, 0);
  assert.deepStrictEqual(s.expire(1000), [b, a]);
  assert.deepStrictEqual(s.list().map((t) => t.id), [err, keep]);
  assert.deepStrictEqual(s.expire(1000), []);
});

test("list returns copies: editing one changes nothing in the stack", () => {
  const s = createToastStack();
  s.push({ text: "a", kind: "info" }, 0);
  const view = s.list();
  view[0].text = "tampered";
  view.push({ id: 99 });
  assert.deepStrictEqual(texts(s), ["a"]);
});

test("a spec object is not kept by reference", () => {
  const s = createToastStack();
  const spec = { text: "a", kind: "info" };
  s.push(spec, 0);
  spec.text = "changed";
  assert.deepStrictEqual(texts(s), ["a"]);
});

test("toastFailText: a 4xx with a message names the message", () => {
  const err = Object.assign(new Error("name in use"), { status: 409 });
  assert.deepStrictEqual(toastFailText("rename", err), {
    text: "Could not rename: name in use",
    detail: "name in use (HTTP 409)",
  });
});

test("toastFailText: a 5xx keeps the raw message out of the sentence", () => {
  const err = Object.assign(new Error("docker stop failed: timeout"), { status: 500 });
  assert.deepStrictEqual(toastFailText("end claude", err), {
    text: "Could not end claude.",
    detail: "docker stop failed: timeout (HTTP 500)",
  });
});

test("toastFailText: a network TypeError has no status", () => {
  assert.deepStrictEqual(toastFailText("save", new TypeError("Failed to fetch")), {
    text: "Could not save.",
    detail: "Failed to fetch",
  });
});

test("toastFailText: the 4xx range is 400 to 499 inclusive", () => {
  const at = (status) => toastFailText("x", Object.assign(new Error("m"), { status })).text;
  assert.strictEqual(at(399), "Could not x.");
  assert.strictEqual(at(400), "Could not x: m");
  assert.strictEqual(at(499), "Could not x: m");
  assert.strictEqual(at(500), "Could not x.");
});

test("toastFailText: no message gives the status alone, or nothing", () => {
  assert.deepStrictEqual(toastFailText("stop", Object.assign(new Error(""), { status: 404 })), {
    text: "Could not stop.",
    detail: "HTTP 404",
  });
  assert.deepStrictEqual(toastFailText("stop", new Error("")), { text: "Could not stop.", detail: "" });
  assert.deepStrictEqual(toastFailText("stop", null), { text: "Could not stop.", detail: "" });
  assert.deepStrictEqual(toastFailText("stop", undefined), { text: "Could not stop.", detail: "" });
  assert.deepStrictEqual(toastFailText("stop", {}), { text: "Could not stop.", detail: "" });
});

test("toastFailText: a status that is not a number is ignored", () => {
  const err = Object.assign(new Error("boom"), { status: "409" });
  assert.deepStrictEqual(toastFailText("stop", err), { text: "Could not stop.", detail: "boom" });
});
