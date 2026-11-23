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

  // The dock's line blame (editorblame.js), on unless a stored false says off: the one choice here that defaults to on.
  let blame = null;
  function getBlame() {
    if (blame === null) blame = read().blame !== false;
    return blame;
  }
  function setBlame(b) {
    blame = !!b;
    write(Object.assign(read(), { blame }));
  }

  // The Changes view's own two choices (editordiff.js), kept beside wrap and apart from it: "side" (Side by side, the default) or
  // "inline", and whether the diff wraps long lines (separate from the editor's wrap). Anything else stored reads as the default.
  let diffMode = null;
  function getDiffMode() {
    if (diffMode === null) diffMode = read().diffMode === "inline" ? "inline" : "side";
    return diffMode;
  }
  function setDiffMode(m) {
    diffMode = m === "inline" ? "inline" : "side";
    write(Object.assign(read(), { diffMode }));
  }
  let diffWrap = null;
  function getDiffWrap() {
    if (diffWrap === null) diffWrap = read().diffWrap === true;
    return diffWrap;
  }
  function setDiffWrap(b) {
    diffWrap = !!b;
    write(Object.assign(read(), { diffWrap }));
  }

  // The image comparer's mode (imagediff.js): "side" (Side by side, the default), "swipe" or "onion" (Onion skin). Anything else stored
  // reads as the default.
  const IMG_MODES = ["side", "swipe", "onion"];
  let imgDiffMode = null;
  function getImgDiffMode() {
    if (imgDiffMode === null) { const m = read().imgDiffMode; imgDiffMode = IMG_MODES.includes(m) ? m : "side"; }
    return imgDiffMode;
  }
  function setImgDiffMode(m) {
    imgDiffMode = IMG_MODES.includes(m) ? m : "side";
    write(Object.assign(read(), { imgDiffMode }));
  }

  return { KEY, getWrap, setWrap, getBlame, setBlame, getDiffMode, setDiffMode, getDiffWrap, setDiffWrap, getImgDiffMode, setImgDiffMode };
})();

if (typeof module !== "undefined" && module.exports) {
  module.exports = { viewerPrefs };
}
