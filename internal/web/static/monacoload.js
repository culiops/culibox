"use strict";

// Loads Monaco for the Files dock's editor, once, the first time a file is opened in
// the dock. Nothing here touches the network at page boot: this file only defines
// loadMonaco, and the loader, the stylesheet and the editor are requested by the
// first call. Every URL is the vendored copy under /vendor/monaco (a pinned version,
// checksummed by MANIFEST.sha256; never a CDN).
//
// The AMD build is used: a classic script (vs/loader.js) and require(), which fits a
// page of classic scripts with no bundler. It defines two globals, `require` and
// `define`, and `monaco`.
//
// Two things the vendored 0.57.0 editor.main.js does by itself, measured, so this file does not:
//  - it sets self.MonacoEnvironment = {getWorker}, replacing whatever the page set before it, and its
//    getWorker starts every worker from /vendor/monaco/vs (the editor worker for a plain file, the json,
//    css, html and ts workers for their languages), so the workers are the same origin without help;
//  - it appends its own <link> for vs/editor/editor.main.css when it runs.

const MONACO_BASE = "/vendor/monaco/vs";
// The name editortab.js gives the editor (culiboxTheme(), monacotheme.js).
const MONACO_THEME = "culibox";

// The keys MONACO_UNBIND (monacokeys.js) takes away, as the KeyCode name and the command that holds it by default. Measured in
// 0.57.0: F1 opens Monaco's own command palette, and the box has one palette. A rule on the command with a "-" in front
// removes that default binding; the key then does nothing in the editor.
const MONACO_UNBOUND_KEYS = [{ command: "editor.action.quickCommand", code: "F1" }];

let monacoLoading = null;

// monacoInject appends `el` to <head> and settles when the browser has loaded or refused it.
function monacoInject(el) {
  return new Promise((resolve, reject) => {
    el.onload = () => resolve();
    el.onerror = () => reject(new Error("could not load " + (el.src || el.href)));
    document.head.append(el);
  });
}

// monacoStyled settles when the editor's stylesheet is applied: an editor measured before it would be laid out
// unstyled. The module injects the link itself; one is added here only if a build stops doing that.
async function monacoStyled() {
  let css = document.head.querySelector('link[href$="/vs/editor/editor.main.css"]');
  if (!css) { css = document.createElement("link"); css.rel = "stylesheet"; css.href = MONACO_BASE + "/editor/editor.main.css"; document.head.append(css); }
  if (css.sheet) return;
  await new Promise((resolve, reject) => {
    css.addEventListener("load", () => resolve(), { once: true });
    css.addEventListener("error", () => reject(new Error("could not load " + css.href)), { once: true });
  });
}

// monacoBoot: loader.js, then require() of the editor, its stylesheet, then the theme and the unbound keys.
async function monacoBoot() {
  const loader = document.createElement("script");
  loader.src = MONACO_BASE + "/loader.js";
  await monacoInject(loader);
  require.config({ paths: { vs: MONACO_BASE } });
  await new Promise((resolve, reject) => require(["vs/editor/editor.main"], resolve, reject));
  await monacoStyled();
  const monaco = window.monaco;
  monaco.editor.defineTheme(MONACO_THEME, culiboxTheme());
  for (const k of MONACO_UNBOUND_KEYS) monaco.editor.addKeybindingRule({ keybinding: monaco.KeyCode[k.code], command: "-" + k.command });
  return monaco;
}

// loadMonaco() -> Promise<monaco>. Single-flight: every caller shares the one load.
// A failure (offline, blocked) is not remembered, so the next open tries again.
function loadMonaco() {
  if (!monacoLoading) monacoLoading = monacoBoot().catch((err) => { monacoLoading = null; throw err; });
  return monacoLoading;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { MONACO_BASE, MONACO_THEME, MONACO_UNBOUND_KEYS, loadMonaco };
}
