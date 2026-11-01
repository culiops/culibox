"use strict";

// Client-side viewer preferences, kept apart from fontprefs.js: that module
// rewrites its whole record on every save, and a font reset must not touch
// whether lines wrap. One JSON record in localStorage; a missing, corrupt or
// unreadable value is "off", which is how the viewer always behaved.
// index.html carries a pre-paint copy of the read below (body.viewer-wrap), so
// the page never flashes unwrapped: keep the two in step.
const viewerPrefs = (function () {
  const KEY = "agent-box:viewerPrefs";

  function read() {
    try {
      const o = JSON.parse(localStorage.getItem(KEY) || "{}");
      return o && typeof o === "object" && !Array.isArray(o) ? o : {};
    } catch (_) { return {}; }
  }
  function write(o) { try { localStorage.setItem(KEY, JSON.stringify(o)); } catch (_) {} }

  // Only a stored boolean true counts: "yes", 1 and the like read as off.
  function getWrap() { return read().wrap === true; }
  function setWrap(b) { write(Object.assign(read(), { wrap: !!b })); }

  return { KEY, getWrap, setWrap };
})();

if (typeof module !== "undefined" && module.exports) {
  module.exports = { viewerPrefs };
}
