"use strict";

// Client-side font-size preferences. One source of truth in localStorage
// (agent-box:fontPrefs), applied via CSS custom properties (code / markdown /
// file tree) and xterm's fontSize (terminal, wired in terminal-tab.js). This is a
// pure display preference: deliberately decoupled from the agent-profile /
// spawn-options pipeline (which maps to CLI flags / env vars).
const fontPrefs = (function () {
  const KEY = "agent-box:fontPrefs";
  const DEFAULTS = { terminal: 13, code: 12.5, markdown: 14, filetree: 12 };
  const BOUNDS = { min: 8, max: 32 };
  const STEP = 1;
  // surface → :root CSS custom property (terminal has none; it uses xterm's JS API)
  const CSS_VAR = { code: "--fs-code", markdown: "--fs-md", filetree: "--fs-tree" };

  function clamp(px) {
    px = Number(px);
    if (!isFinite(px)) return null;
    return Math.min(BOUNDS.max, Math.max(BOUNDS.min, px));
  }

  // load merges stored values over defaults, clamping each; a missing or malformed
  // value silently falls back to that surface's default.
  function load() {
    const out = Object.assign({}, DEFAULTS);
    try {
      const raw = JSON.parse(localStorage.getItem(KEY) || "{}");
      for (const k of Object.keys(DEFAULTS)) {
        const c = clamp(raw[k]);
        if (c !== null) out[k] = c;
      }
    } catch (_) {}
    return out;
  }

  let prefs = load();

  function save() { try { localStorage.setItem(KEY, JSON.stringify(prefs)); } catch (_) {} }
  function get(surface) { return prefs[surface]; }

  function applyCssVars() {
    const s = document.documentElement.style;
    s.setProperty(CSS_VAR.code, prefs.code + "px");
    s.setProperty(CSS_VAR.markdown, prefs.markdown + "px");
    s.setProperty(CSS_VAR.filetree, prefs.filetree + "px");
  }

  // applyTerminals is a no-op until terminal-tab.js/tabs.js define eachTerminal (Task 2).
  function applyTerminals() {
    if (typeof eachTerminal === "function") eachTerminal((t) => t.setFontSize(prefs.terminal));
  }

  // set a global default for one surface → persist + re-apply everywhere, and
  // re-baseline (clear transient live zoom) on panes of that surface for predictability.
  function set(surface, px) {
    const c = clamp(px);
    if (c === null || !(surface in prefs)) return;
    prefs[surface] = c;
    save();
    if (surface === "terminal") {
      applyTerminals(); // overwrites each terminal's size → clears their live zoom
    } else {
      applyCssVars();
      const v = CSS_VAR[surface];
      if (typeof eachViewer === "function") eachViewer((vt) => { if (vt.zoomVar === v) vt.resetZoom(); });
    }
  }

  function reset() {
    prefs = Object.assign({}, DEFAULTS);
    save();
    applyCssVars();
    applyTerminals();
    if (typeof eachViewer === "function") eachViewer((vt) => vt.resetZoom());
  }

  return { DEFAULTS, BOUNDS, STEP, CSS_VAR, clamp, get, set, reset, applyCssVars, applyTerminals };
})();
