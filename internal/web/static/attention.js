"use strict";

// Pure helpers for the attention signal: what a needs-you card shows, the tab
// title/favicon, and which sessions just started needing you. DOM wiring lives
// in app.js; everything here is testable under node --test.

// needLabel: the human word for WHY a waiting session needs you. "" when it is
// not waiting, or waiting without a known need (the caller falls back to the
// activity's own title).
function needLabel(s) {
  if (!s || s.activity !== "waiting") return "";
  if (s.need === "ask") return "asks";
  if (s.need === "turn") return "your turn";
  return "";
}

// cardContent decides which blocks a desktop needs-you card renders.
//  - ask: the tool call it wants to run. NO reply field and NO digit buttons —
//    the server refuses any message to a pending/blocked session by design (a
//    permission dialog reads single keys), so the card sends you to the
//    terminal instead (Open, always rendered by the caller).
//  - turn / unknown need on claude: the summary + a one-line reply when the
//    session has opted in, else an "Enable replies" control — never a dead
//    field, and never digits (a digit would become the agent's next prompt).
//  - non-claude (codex): summary only; the reply path is claude-only.
function cardContent(s) {
  const v = s || {};
  if (v.need === "ask") {
    return { text: v.ask || v.summary || "Waiting on a prompt in the terminal.", ask: true, reply: false, enableReplies: false };
  }
  if (!v.claude) {
    return { text: v.summary || "Waiting for input.", ask: false, reply: false, enableReplies: false };
  }
  return { text: v.summary || "No summary", ask: false, reply: !!v.replyable, enableReplies: !v.replyable };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { needLabel, cardContent };
}
