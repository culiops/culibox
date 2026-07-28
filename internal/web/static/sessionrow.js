"use strict";

// One decision about a session row, kept out of app.js so `node --test` can
// reach it. Shared logic in app.js is unreachable by every test in this repo,
// which is how a deleted helper silently broke three files earlier this week.

// attachedElsewhere reports that a viewer holds this session and it is not this
// page. The server says only THAT someone is subscribed — it has no identity to
// report — so whether that someone is us is a question only the page can answer,
// which is why the caller passes it in.
//
// An exited session never qualifies: who was watching something already over is
// not something anyone can act on. A response with no `attached` field (an older
// daemon behind a newer page) reads as not attached, so the badge can never
// appear on a claim the server never made.
function attachedElsewhere(session, heldHere) {
  if (!session || session.exited) return false;
  return session.attached === true && !heldHere;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { attachedElsewhere };
}
