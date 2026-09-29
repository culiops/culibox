"use strict";

// Pure helpers for the sidebar peek. DOM wiring lives in app.js; everything
// here is testable under node --test.

// peekState turns an /api/sessions/{id}/peek result into what to render. A 204
// means two different things depending on the agent, and a blank panel would
// hide both — the silent-empty failure mode that hid the projectSlug bug for so
// long. Each case gets its own words.
//
// isClaude is the server's own isClaudeBin verdict (Info.claude), not a guess
// from the profile name: a profile may be called anything, and the send path is
// scoped by the binary.
function peekState(isClaude, status, body) {
  if (status === 200 && body && String(body.text || "").trim() !== "") {
    return { kind: "text", text: body.text };
  }
  if (status === 200) {
    return { kind: "empty", text: "No message yet." };
  }
  // status 0 is app.js's catch arm: the request never completed. Falling
  // through to the transcript wording would blame the agent's files for what is
  // a connectivity problem, and send the reader to look in the wrong place.
  if (status === 0) {
    return { kind: "offline", text: "Could not reach the server — the summary is unavailable, not missing." };
  }
  if (!isClaude) {
    return { kind: "unsupported", text: "Only Claude sessions report a summary." };
  }
  return { kind: "notranscript", text: "Transcript not readable for this session." };
}

// UNSAFE_REPLY matches anything a TUI would interpret rather than read: every
// C0 control (which includes tab and newline) plus DEL. One escape sequence
// reaching the terminal is a loss of control over the session.
var UNSAFE_REPLY = /[\x00-\x1f\x7f]/;

// replyTextError returns "" when the text may be sent, or a short machine
// reason. Reasons are codes, not sentences: the caller phrases them.
function replyTextError(text) {
  var t = String(text == null ? "" : text);
  if (t.trim() === "") return "empty";
  if (t.indexOf("\n") !== -1 || t.indexOf("\r") !== -1) return "newline";
  if (UNSAFE_REPLY.test(t)) return "control";
  return "";
}

// sendState mirrors the server's canReply guard so the button can explain
// itself before a request is made. The server re-checks all three — this is
// UX, never the control.
//
// The order MUST match canReply's (peek.go): replyable, then pending, then
// activity. A session on a permission prompt redraws it, so it reads as
// `working` while it is in fact pending — with activity tested first, the most
// common blocked state was explained as "wait until it stops", about a session
// that had already stopped and was waiting on the operator.
function sendState(s) {
  if (!s || !s.replyable) {
    return { enabled: false, reason: "Enable replies for this session first." };
  }
  if (s.pending) {
    return { enabled: false, reason: "Session is inside a tool call. Open the tab to answer it." };
  }
  if (s.activity !== "waiting") {
    return { enabled: false, reason: notWaitingReason(s.activity) };
  }
  return { enabled: true, reason: "" };
}

// notWaitingReason phrases a non-waiting activity. Branching matters: "wait
// until it stops" was shown for done, crashed and detached too, and a dead
// agent telling you to wait until it stops is wrong in a way an operator
// notices immediately.
function notWaitingReason(activity) {
  switch (activity) {
    case "working":
      return "Session is working — wait until it stops.";
    case "done":
      return "Session has ended — there is nothing listening.";
    case "crashed":
      return "Session crashed — there is nothing listening.";
    case "detached":
      return "Session is detached — open it to reattach first.";
    case "idle":
      return "Nothing to answer yet — give this session its first prompt in its terminal.";
    default:
      return "Session is not ready for a message.";
  }
}

// offerEnableReplies: show an "Enable replies" button beside a closed reply
// control. True exactly when sendState's reason is the missing opt-in, for a
// claude session that still runs (replies are claude-only on the server).
function offerEnableReplies(s) {
  return !!s && s.claude === true && !s.replyable && !s.exited;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { peekState, replyTextError, sendState, notWaitingReason, offerEnableReplies };
}
