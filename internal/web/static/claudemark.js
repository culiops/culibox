"use strict";

// Whether a profile is a Claude one, for the Claude mark beside its name in New session and on a sidebar row.
// The mark itself is "si:claude" in icons.js (generated from Simple Icons, CC0 data); it is Anthropic's trademark and is
// used only to identify Claude sessions, never to suggest endorsement. The notice is in docs/ICONS.md.
// Classic script: the browser shares one global scope; under node the name is exported.

// claudeKindOf: the profile is a Claude one, by the view field the server already sends. Identity for an icon, not a gate on behaviour.
function claudeKindOf(p) {
  return !!(p && p.claude === true);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { claudeKindOf };
}
