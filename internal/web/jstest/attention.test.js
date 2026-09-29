"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { needLabel, cardContent, attentionTitle, faviconHref, needsTransitions, notifyAvailability, notifyChecked, notifyBody, notifyPoll } = require("../static/attention.js");

test("needLabel names the two needs, and nothing that is not waiting", () => {
  assert.strictEqual(needLabel({ activity: "waiting", need: "ask" }), "asks");
  assert.strictEqual(needLabel({ activity: "waiting", need: "turn" }), "your turn");
  assert.strictEqual(needLabel({ activity: "waiting" }), "");
  assert.strictEqual(needLabel({ activity: "working", need: "ask" }), "");
  assert.strictEqual(needLabel(null), "");
});

test("ask card shows what the agent wants to run, and no reply path", () => {
  const c = cardContent({ activity: "waiting", need: "ask", ask: "Bash(rm -rf build)", claude: true, replyable: true, summary: "x" });
  assert.deepEqual(c, { text: "Bash(rm -rf build)", ask: true, reply: false, enableReplies: false });
});

test("ask card without ask text falls back to the summary", () => {
  assert.strictEqual(cardContent({ activity: "waiting", need: "ask", claude: true, summary: "may I?" }).text, "may I?");
});

test("turn card offers a reply field only when replies are enabled", () => {
  assert.deepEqual(cardContent({ activity: "waiting", need: "turn", claude: true, replyable: true, summary: "done" }),
    { text: "done", ask: false, reply: true, enableReplies: false });
  assert.deepEqual(cardContent({ activity: "waiting", need: "turn", claude: true, replyable: false, summary: "done" }),
    { text: "done", ask: false, reply: false, enableReplies: true });
});

test("claude waiting with no need (transcript unreadable) is Open only, like non-claude", () => {
  const c = cardContent({ activity: "waiting", claude: true, replyable: true });
  assert.deepEqual(c, { text: "Waiting for input.", ask: false, reply: false, enableReplies: false });
});

test("non-claude waiting card offers Open only", () => {
  assert.deepEqual(cardContent({ activity: "waiting", claude: false, replyable: true, summary: "" }),
    { text: "Waiting for input.", ask: false, reply: false, enableReplies: false });
});

test("attentionTitle prefixes the waiting count only when there is one", () => {
  assert.strictEqual(attentionTitle(0, "DEV"), "[DEV] Agent Box");
  assert.strictEqual(attentionTitle(2, "DEV"), "(2) [DEV] Agent Box");
  assert.strictEqual(attentionTitle(1, ""), "(1) Agent Box");
  assert.strictEqual(attentionTitle(0, ""), "Agent Box");
});

test("faviconHref flips to the amber-dot icon while anything needs you", () => {
  assert.strictEqual(faviconHref(0), "/favicon.svg");
  assert.strictEqual(faviconHref(3), "/favicon-needs.svg");
});

const W = (id) => ({ id, activity: "waiting" });
const B = (id) => ({ id, activity: "working" });

test("the first poll after load only seeds — no notification for already-waiting sessions", () => {
  const r = needsTransitions(null, [W("a"), B("b")]);
  assert.deepEqual(r.fired, []);
  assert.deepEqual([...r.next], ["a"]);
});

test("a session that becomes waiting fires exactly once", () => {
  let r = needsTransitions(new Set(), [W("a")]);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
  r = needsTransitions(r.next, [W("a")]);
  assert.deepEqual(r.fired, []);
});

test("waiting -> working -> waiting fires again", () => {
  let r = needsTransitions(new Set(["a"]), [B("a")]);
  assert.deepEqual(r.fired, []);
  r = needsTransitions(r.next, [W("a")]);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
});

test("idle, done and unknown states never fire", () => {
  const r = needsTransitions(new Set(), [{ id: "a", activity: "idle" }, { id: "b", activity: "done" }, { id: "c" }]);
  assert.deepEqual(r.fired, []);
});

test("notifyAvailability: insecure origin disables with the HTTPS hint", () => {
  const a = notifyAvailability(false, "default");
  assert.strictEqual(a.enabled, false);
  assert.match(a.reason, /Requires HTTPS — open via agent-box\.culiops\.dev/);
  assert.strictEqual(notifyAvailability(true, "unsupported").enabled, false);
  assert.deepEqual(notifyAvailability(true, "default"), { enabled: true, reason: "" });
});

test("notifyChecked is false unless permission is granted", () => {
  assert.strictEqual(notifyChecked(true, "granted"), true);
  assert.strictEqual(notifyChecked(true, "denied"), false);   // revoked in the browser after opting in
  assert.strictEqual(notifyChecked(true, "default"), false);
  assert.strictEqual(notifyChecked(false, "granted"), false);
});

test("notifyBody prefers the ask, then the summary", () => {
  assert.strictEqual(notifyBody({ ask: "Bash(ls)", summary: "x" }), "Bash(ls)");
  assert.strictEqual(notifyBody({ summary: "done" }), "done");
  assert.strictEqual(notifyBody({}), "Waiting for you");
});

test("notifyPoll: a call before the first successful list does not seed prevIds", () => {
  // Simulates initEnv's title refresh firing before /api/sessions has ever
  // resolved: loaded=false must hand prevIds back untouched (no seeding off
  // the empty lastSessions placeholder) and must never report a transition.
  let r = notifyPoll(false, null, []);
  assert.deepEqual(r, { next: null, fired: [] });
  r = notifyPoll(false, null, [W("a")]); // even if a stray list were passed
  assert.deepEqual(r, { next: null, fired: [] });
  // Once the real list lands (loaded flips true), the real first poll still
  // seeds normally — nothing was consumed by the not-loaded calls above.
  r = notifyPoll(true, r.next, [W("a"), B("b")]);
  assert.deepEqual(r.fired, []);
  assert.deepEqual([...r.next], ["a"]);
});

test("notifyPoll: once loaded, behaves exactly like needsTransitions", () => {
  const r = notifyPoll(true, new Set(), [W("a")]);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
});
