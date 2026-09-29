"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { needLabel, cardContent } = require("../static/attention.js");

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
