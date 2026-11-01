"use strict";

// Client-side viewer preferences, kept apart from fontprefs.js: that module
// rewrites its whole record on every save, and a font reset must not touch
// whether lines wrap. One JSON record in localStorage is the PERSISTENCE; the
// value the page acts on is held in memory (read once, then updated by setWrap),
// so with storage blocked a toggle still works for the life of the page. A
// missing, corrupt or unreadable stored value is "off", which is how the viewer
// always behaved.
// index.html carries a pre-paint copy of the read below (viewer-wrap on <html>),
// so the page never flashes unwrapped: keep the two in step.
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
  // null = not read yet.
  let wrap = null;
  function getWrap() {
    if (wrap === null) wrap = read().wrap === true;
    return wrap;
  }
  function setWrap(b) {
    wrap = !!b;
    write(Object.assign(read(), { wrap }));
  }

  return { KEY, getWrap, setWrap };
})();

if (typeof module !== "undefined" && module.exports) {
  module.exports = { viewerPrefs };
}
