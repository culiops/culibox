"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { peekState, replyTextError, sendState } = require("../static/peek.js");

test("peekState distinguishes the three empty cases", () => {
  // 200 with text: normal
  assert.deepEqual(peekState("claude", 200, { text: "hi" }),
    { kind: "text", text: "hi" });
  // 200 with no text: the agent has not spoken yet
  assert.deepEqual(peekState("claude", 200, { text: "" }),
    { kind: "empty", text: "No message yet." });
  // 204 on a claude session: transcript could not be read
  assert.deepEqual(peekState("claude", 204, null),
    { kind: "notranscript", text: "Transcript not readable for this session." });
  // 204 on a non-claude session: expected, not an error
  assert.deepEqual(peekState("codex", 204, null),
    { kind: "unsupported", text: "Only Claude sessions report a summary." });
});

test("replyTextError rejects empty, newline and control characters", () => {
  assert.equal(replyTextError("looks fine"), "");
  assert.equal(replyTextError(""), "empty");
  assert.equal(replyTextError("   "), "empty");
  assert.equal(replyTextError("two\nlines"), "newline");
  assert.equal(replyTextError("bell\x07"), "control");
  assert.equal(replyTextError("esc\x1b[31m"), "control");
  assert.equal(replyTextError("del\x7f"), "control");
  // A tab is a control character too: it triggers completion in a TUI.
  assert.equal(replyTextError("a\tb"), "control");
});

test("sendState enables only when all three guards pass", () => {
  const ok = { replyable: true, activity: "waiting", pending: false };
  assert.deepEqual(sendState(ok), { enabled: true, reason: "" });
  assert.deepEqual(sendState({ ...ok, replyable: false }),
    { enabled: false, reason: "Enable replies for this session first." });
  assert.deepEqual(sendState({ ...ok, activity: "working" }),
    { enabled: false, reason: "Session is working — wait until it stops." });
  assert.deepEqual(sendState({ ...ok, pending: true }),
    { enabled: false, reason: "Session is inside a tool call. Open the tab to answer it." });
  // replyable is checked first: the most actionable message wins.
  assert.equal(sendState({ replyable: false, activity: "working", pending: true }).reason,
    "Enable replies for this session first.");
});
