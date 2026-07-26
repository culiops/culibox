"use strict";

// The session tier badge: which containment a session is actually running under.
//
// This is a security signal, so it is deliberately fail-loud. An unknown or
// missing tier reads as trusted-host, never as boxed — a badge that claims
// containment a session does not have is worse than no badge at all.
//
// Loaded as a classic script, so top-level names are shared globals; they are
// TIER_-prefixed to avoid colliding with another file (two classic scripts
// declaring the same top-level const is a SyntaxError that blanks the page).

const TIER_BOX = "box";
const TIER_HOST = "host";

// Short label for the isolation runtime the daemon detected.
function tierRuntimeLabel(boxRuntime) {
  if (boxRuntime === "sysbox-runc") return "sysbox";
  if (boxRuntime) return boxRuntime;
  return "runc";
}

// tierBadge maps a session + the daemon's detected runtime to what the row shows.
// Returns null when there is nothing worth showing (an exited session already
// carries its own badge, and the tier of a dead session is not actionable).
function tierBadge(session, boxRuntime) {
  if (!session || session.exited) return null;
  if (session.runtime === TIER_BOX) {
    return {
      text: "boxed · " + tierRuntimeLabel(boxRuntime),
      tone: "neutral",
      title:
        "Contained: only this project directory is mounted. No host home, no ~/.ssh, no docker socket.",
    };
  }
  return {
    text: "trusted-host",
    tone: "danger",
    title:
      "NOT contained: this session runs on the host with your user's access. Use only for work that needs the host.",
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { tierBadge, tierRuntimeLabel, TIER_BOX, TIER_HOST };
}
