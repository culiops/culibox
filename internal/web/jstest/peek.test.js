"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { peekState, replyTextError, sendState, notWaitingReason, offerEnableReplies } = require("../static/peek.js");

test("peekState distinguishes the empty cases", () => {
  // 200 with text: normal
  assert.deepEqual(peekState(true, 200, { text: "hi" }),
    { kind: "text", text: "hi" });
  // 200 with no text: the agent has not spoken yet
  assert.deepEqual(peekState(true, 200, { text: "" }),
    { kind: "empty", text: "No message yet." });
  // 204 on a claude session: transcript could not be read
  assert.deepEqual(peekState(true, 204, null),
    { kind: "notranscript", text: "Transcript not readable for this session." });
  // 204 on a non-claude session: expected, not an error
  assert.deepEqual(peekState(false, 204, null),
    { kind: "unsupported", text: "Only Claude sessions report a summary." });
});

test("peekState does not blame the transcript for a dead connection", () => {
  // status 0 is app.js's catch arm: the request never completed. It must not
  // fall through to the transcript wording, which sends the reader to look in
  // the wrong place — and it outranks the claude test, because a request that
  // never happened says nothing about which agent this is.
  const off = peekState(true, 0, null);
  assert.equal(off.kind, "offline");
  assert.match(off.text, /reach the server/);
  assert.deepEqual(peekState(false, 0, null), off);
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

test("sendState reports pending before activity, matching canReply", () => {
  // A session on a permission prompt redraws it, which keeps resetting the
  // idle timer — so the most common blocked state arrives here as
  // working+pending. Testing activity first told the operator to "wait until
  // it stops" about a session that had already stopped and was waiting on
  // them. The refusal is identical either way; only the words change. This
  // order must stay in step with canReply in internal/session/peek.go.
  const st = sendState({ replyable: true, activity: "working", pending: true });
  assert.equal(st.enabled, false);
  assert.equal(st.reason, "Session is inside a tool call. Open the tab to answer it.");
});

test("notWaitingReason does not tell a dead session to keep working", () => {
  assert.match(notWaitingReason("working"), /wait until it stops/);
  // "wait until it stops" used to be shown for all four: an agent that has
  // exited or crashed being told to wait until it stops is wrong in a way an
  // operator notices immediately.
  assert.match(notWaitingReason("done"), /ended/);
  assert.match(notWaitingReason("crashed"), /crashed/);
  assert.match(notWaitingReason("detached"), /detached/);
  for (const a of ["done", "crashed", "detached"]) {
    assert.doesNotMatch(notWaitingReason(a), /wait until it stops/);
  }
  // An activity value this client does not know about still gets a sentence,
  // never an empty reason next to a disabled button.
  assert.equal(notWaitingReason("something-new"), "Session is not ready for a message.");
});

test("notWaitingReason tells an idle session to be prompted in its terminal", () => {
  assert.match(notWaitingReason("idle"), /terminal/);
  assert.doesNotMatch(notWaitingReason("idle"), /wait until it stops/);
});

test("offerEnableReplies: a live claude that has not opted in", () => {
  assert.strictEqual(offerEnableReplies({ claude: true, replyable: false }), true);
  assert.strictEqual(offerEnableReplies({ claude: true, replyable: true }), false);
  assert.strictEqual(offerEnableReplies({ claude: false, replyable: false }), false);
  assert.strictEqual(offerEnableReplies({ claude: true, replyable: false, exited: true }), false);
  assert.strictEqual(offerEnableReplies(null), false);
});

// The button must appear exactly where the gate's reason names the missing
// opt-in — the two must not drift apart.
test("offerEnableReplies agrees with sendState's not-replyable reason", () => {
  const why = "Enable replies for this session first.";
  for (const replyable of [true, false]) {
    for (const activity of ["working", "waiting", "idle"]) {
      for (const pending of [true, false]) {
        const s = { claude: true, replyable, activity, pending };
        assert.strictEqual(offerEnableReplies(s), sendState(s).reason === why, JSON.stringify(s));
      }
    }
  }
});
