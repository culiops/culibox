"use strict";

// The toast stack as data (F60, F61). Today's single slot lets any later toast
// replace the one before it: an error fades at 6 s like a confirmation, and the
// spawn Undo (which kills the new session) is lost the moment the next toast
// lands. This is the model that fixes that; the page renders list() and arms one
// timer from nextDeadline() (Task 7). Classic script: pure, no DOM, and no timers
// of its own. Every call that cares about time takes `now` (milliseconds, any
// epoch the caller keeps), so each rule is testable with a fake clock.
//
// Kinds
//   info, warn   expire after `spec.ms`, or the stack's ms (6000) when spec.ms
//                is not a number in (0, 2^31-1] (toastStackMs)
//   error        stays until removed (the close button); no deadline
//   pending      stays until its owner update()s it into the result; no deadline
//   undo: true   a flag, not a kind. The entry keeps its kind's timer (an info
//                undo still ends at 6 s) but is protected from eviction.
//
// Order is newest first: index 0 is the top of the stack.
//
// Cap. A push that would make the list longer than `max` (3) evicts from the OLD
// entries, so the new toast always shows. Oldest (lowest on the list) first, in
// three tiers: plain info/warn without undo; then error without undo; then undo
// and pending entries, and only when every old entry is one of those. That last
// case needs `max` undo/pending entries already listed when another toast
// arrives (3 by default; the 4th concurrent one is the push itself when it is
// undo/pending too). An evicted pending entry's owner finds update() returning
// false and must push its result as a new toast.
//
// Dedupe. A push whose text and kind equal a listed entry that is not pending
// and has no undo does not add an entry: that entry moves to the top, takes the
// push's detail, progress and ms, restarts its timer, and its id is returned.
// A push that itself carries an undo never dedupes either: each Undo stays its
// own entry, with its own id (the page keys the callback by id), and a listed
// Undo is never the entry a later push replaces.
//
// Pause (hover). pause(now) freezes every timed entry's remaining time; while
// paused nextDeadline() is null and expire() removes nothing. resume(now) sets
// each deadline to now + remaining. Pushes and updates made while paused keep
// their full length as remaining time and get a deadline on resume.

const TOAST_STACK_KINDS = ["info", "warn", "error", "pending"];

// TOAST_STACK_MAX_MS: the longest delay setTimeout holds (a signed 32-bit int).
// Past it, or for Infinity/NaN, the timer fires at once while the deadline is
// still ahead, so expire() removes nothing and the page re-arms the same timer
// in a loop. toastStackMs keeps a length only when it is a number in (0, max].
const TOAST_STACK_MAX_MS = 2147483647;
function toastStackMs(ms) {
  return typeof ms === "number" && ms > 0 && ms <= TOAST_STACK_MAX_MS;
}

// toastStackSpec: one spec as the plain fields an entry keeps. `ms` is the timer
// length for the kinds that have one, null for the kinds that do not.
function toastStackSpec(spec, defaultMs) {
  spec = spec || {};
  const kind = TOAST_STACK_KINDS.indexOf(spec.kind) >= 0 ? spec.kind : "info";
  const timed = kind === "info" || kind === "warn";
  const p = spec.progress;
  return {
    text: spec.text == null ? "" : String(spec.text),
    kind: kind,
    undo: !!spec.undo,
    detail: spec.detail == null ? "" : String(spec.detail),
    progress: typeof p === "number" && Number.isFinite(p) ? Math.max(0, Math.min(1, p)) : null,
    ms: timed ? (toastStackMs(spec.ms) ? spec.ms : defaultMs) : null,
  };
}

// toastStackTier: how late an entry is evicted. Lower goes first.
function toastStackTier(e) {
  if (e.undo || e.kind === "pending") return 2;
  return e.kind === "error" ? 1 : 0;
}

function createToastStack(opts) {
  opts = opts || {};
  const max = typeof opts.max === "number" && opts.max >= 1 ? Math.floor(opts.max) : 3;
  const defaultMs = toastStackMs(opts.ms) ? opts.ms : 6000;
  let entries = []; // newest first
  let nextId = 1; // never reused, not even after a remove
  let paused = false;

  // Each entry: the toastStackSpec fields plus `id`, `deadline` (a time, or null
  // when untimed or paused) and `left` (remaining ms while paused, else null).
  function restart(e, now) {
    e.deadline = null;
    e.left = null;
    if (e.ms === null) return;
    if (paused) e.left = e.ms;
    else e.deadline = now + e.ms;
  }

  function assign(e, f, now) {
    e.text = f.text;
    e.kind = f.kind;
    e.undo = f.undo;
    e.detail = f.detail;
    e.progress = f.progress;
    e.ms = f.ms;
    restart(e, now);
  }

  function evict() {
    while (entries.length > max) {
      // Old entries only: index 0 is the push that must show, and max >= 1
      // means the list is at least 2 long here, so there is always a victim.
      // Scanning from the bottom with a strict < keeps the oldest of its tier.
      let victim = entries.length - 1;
      for (let i = victim - 1; i >= 1; i--) {
        if (toastStackTier(entries[i]) < toastStackTier(entries[victim])) victim = i;
      }
      entries.splice(victim, 1);
    }
  }

  function push(spec, now) {
    const f = toastStackSpec(spec, defaultMs);
    if (!f.undo && f.kind !== "pending") {
      const i = entries.findIndex((e) => e.text === f.text && e.kind === f.kind && !e.undo);
      if (i >= 0) {
        const e = entries.splice(i, 1)[0];
        assign(e, f, now);
        entries.unshift(e);
        return e.id;
      }
    }
    const e = { id: nextId++, deadline: null, left: null };
    assign(e, f, now);
    entries.unshift(e);
    evict();
    return e.id;
  }

  function update(id, spec, now) {
    const e = entries.find((x) => x.id === id);
    if (!e) return false;
    assign(e, toastStackSpec(spec, defaultMs), now);
    return true;
  }

  function remove(id) {
    const i = entries.findIndex((e) => e.id === id);
    if (i < 0) return false;
    entries.splice(i, 1);
    return true;
  }

  function pause(now) {
    if (paused) return;
    paused = true;
    for (const e of entries) {
      if (e.deadline === null) continue;
      e.left = Math.max(0, e.deadline - now);
      e.deadline = null;
    }
  }

  function resume(now) {
    if (!paused) return;
    paused = false;
    for (const e of entries) {
      if (e.left === null) continue;
      e.deadline = now + e.left;
      e.left = null;
    }
  }

  // expire removes every entry whose deadline has come and returns their ids,
  // top of the stack first. Nothing expires while paused.
  function expire(now) {
    if (paused) return [];
    const gone = [];
    entries = entries.filter((e) => {
      if (e.deadline === null || e.deadline > now) return true;
      gone.push(e.id);
      return false;
    });
    return gone;
  }

  function nextDeadline() {
    if (paused) return null;
    let next = null;
    for (const e of entries) {
      if (e.deadline !== null && (next === null || e.deadline < next)) next = e.deadline;
    }
    return next;
  }

  function list() {
    return entries.map((e) => ({
      id: e.id,
      text: e.text,
      kind: e.kind,
      undo: e.undo,
      detail: e.detail,
      progress: e.progress,
      deadline: e.deadline,
    }));
  }

  return {
    push, update, remove, pause, resume, expire, nextDeadline, list,
    get paused() { return paused; },
  };
}

// toastFailText: the error toast for a failed action, as { text, detail }.
// `what` is the verb phrase ("end claude"). A 4xx answer with a message is the
// server explaining a refusal the user can act on, so the message is the
// sentence ("Could not rename: name in use"). Anything else (a 5xx, a network
// TypeError, a refusal with no message) gets the plain sentence, and the raw
// message stays in `detail` for the "Copy details" button, with " (HTTP n)"
// when err.status is a number. An error with a status but no message has the
// status alone as its detail ("HTTP 404"); no message and no status is "".
function toastFailText(what, err) {
  const msg = err && typeof err.message === "string" ? err.message.trim() : "";
  const status = err && typeof err.status === "number" && Number.isFinite(err.status) ? err.status : null;
  const named = msg !== "" && status !== null && status >= 400 && status <= 499;
  let detail = msg;
  if (status !== null) detail = msg === "" ? "HTTP " + status : msg + " (HTTP " + status + ")";
  return { text: "Could not " + what + (named ? ": " + msg : "."), detail: detail };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { createToastStack, toastFailText };
}
