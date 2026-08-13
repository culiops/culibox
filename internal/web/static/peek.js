"use strict";

// Pure helpers for the sidebar peek. DOM wiring lives in app.js; everything
// here is testable under node --test.

// peekState turns an /api/sessions/{id}/peek result into what to render. A 204
// means two different things depending on the agent, and a blank panel would
// hide both — the silent-empty failure mode that hid the projectSlug bug for so
// long. Each case gets its own words.
function peekState(agent, status, body) {
  if (status === 200 && body && String(body.text || "").trim() !== "") {
    return { kind: "text", text: body.text };
  }
  if (status === 200) {
    return { kind: "empty", text: "No message yet." };
  }
  if (agent !== "claude") {
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
function sendState(s) {
  if (!s || !s.replyable) {
    return { enabled: false, reason: "Enable replies for this session first." };
  }
  if (s.activity !== "waiting") {
    return { enabled: false, reason: "Session is working — wait until it stops." };
  }
  if (s.pending) {
    return { enabled: false, reason: "Session is inside a tool call. Open the tab to answer it." };
  }
  return { enabled: true, reason: "" };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { peekState, replyTextError, sendState };
}
