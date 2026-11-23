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
//    getWorker starts every worker from /vendor/monaco/vs (the editor worker for a plain file, the json
//    worker for JSON), so the workers are the same origin without help. Its css, html and ts branches
//    name workers that scripts/vendor-monaco.sh trims; nothing asks for them, because that script also
//    stubs the chunks that would (JS, TS, CSS and HTML are highlighted by their Monarch tokenizers, with
//    no language service, so no diagnostics squiggles);
//  - it appends its own <link> for vs/editor/editor.main.css when it runs.

const MONACO_BASE = "/vendor/monaco/vs";
// The versioned base the server writes into index.html's asset-base meta: "/v/<build id>" (web.go). Under it the vendored files are
// cached for a year, and the id changes with every build, so an upgrade never serves a stale loader.js. Anything else in the tag
// (none, or not exactly that shape) means the unversioned path, which revalidates.
const MONACO_ASSET_BASE_RE = /^\/v\/[0-9a-f]{16}$/;
// The name editortab.js gives the editor (culiboxTheme(), monacotheme.js).
const MONACO_THEME = "culibox";

// The keys MONACO_UNBIND (monacokeys.js) takes away, as the KeyCode name and the command that holds it by default. Measured in
// 0.57.0: F1 opens Monaco's own command palette, and the box has one palette. A rule on the command with a "-" in front
// removes that default binding; the key then does nothing in the editor.
const MONACO_UNBOUND_KEYS = [{ command: "editor.action.quickCommand", code: "F1" }];

// How long the stylesheet may take to apply once the editor module is in: after this the load fails, instead of leaving every open
// at "loading…" until the page is reloaded. editor.main.css is about 390 KB uncompressed (a client that takes gzip gets far less, and
// a reload revalidates or uses its cache), so 60 s is a floor of about 6.5 KB/s at worst; 10 s failed every first load below about 39 KB/s.
const MONACO_CSS_TIMEOUT_MS = 60000;
// What a link click may scan to find the link's text again (monacoLinkText): a model of more than 5 MB is skipped, and at most 20000
// candidate links are tried in all, so a click on a huge generated file cannot stall the page.
const MONACO_SCAN_MAX_CHARS = 5 * 1024 * 1024;
const MONACO_SCAN_MAX_LINKS = 20000;

// The stylesheet links whose wait ended in a timeout once. Running out of time does not say the request is dead (a slow link may
// still be downloading it), so the first timeout leaves the link for the retry to wait on; a second one drops it.
const monacoSlowStyles = new WeakSet();

let monacoLoading = null;

// monacoAssetBase: the page's versioned base, or "" when it names none.
function monacoAssetBase() {
  const meta = typeof document !== "undefined" && typeof document.querySelector === "function" ? document.querySelector('meta[name="culibox-asset-base"]') : null;
  const v = meta && typeof meta.content === "string" ? meta.content : "";
  return MONACO_ASSET_BASE_RE.test(v) ? v : "";
}

// monacoBase: where vs/ is loaded from. Monaco finds its stylesheet and its workers from require's `vs` path (require.toUrl), so they
// follow it.
function monacoBase() {
  return monacoAssetBase() + MONACO_BASE;
}

// monacoInject appends `el` to <head> and settles when the browser has loaded or refused it.
function monacoInject(el) {
  return new Promise((resolve, reject) => {
    el.onload = () => resolve();
    el.onerror = () => reject(new Error("could not load " + (el.src || el.href)));
    document.head.append(el);
  });
}

// monacoStyled settles when the editor's stylesheet is applied: an editor measured before it would be laid out
// unstyled. The module injects the link itself; one is added here only if a build stops doing that. The wait ends in an error
// when the browser refuses the link or after MONACO_CSS_TIMEOUT_MS (an error that fired before this listens, or a request that
// never answers, says nothing at all). A link the browser refused is dead: it is taken out so that the retry, the next open, finds
// none and makes a fresh one. A link that only timed out is kept the first time, so that the retry waits on the download already
// under way instead of restarting it (which on a slow link would never finish); a second timeout takes it out too.
async function monacoStyled() {
  let css = document.head.querySelector('link[href$="/vs/editor/editor.main.css"]');
  if (!css) { css = document.createElement("link"); css.rel = "stylesheet"; css.href = monacoBase() + "/editor/editor.main.css"; document.head.append(css); }
  if (css.sheet) return;
  let timedOut = false;
  try {
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => { timedOut = true; reject(new Error("could not load " + css.href + " (no answer in " + MONACO_CSS_TIMEOUT_MS / 1000 + " s)")); }, MONACO_CSS_TIMEOUT_MS);
      css.addEventListener("load", () => { clearTimeout(timer); resolve(); }, { once: true });
      css.addEventListener("error", () => { clearTimeout(timer); reject(new Error("could not load " + css.href)); }, { once: true });
    });
  } catch (err) {
    if (timedOut && !monacoSlowStyles.has(css)) monacoSlowStyles.add(css);
    else css.remove();
    throw err;
  }
}

// monacoLinkText finds the link as it is written in an open model, for the URI a click on it produced, or returns "". The opener is
// only ever given a parsed URI, and URI.parse decodes every part, so rebuilding the text from it changes the link: %3F in a path turns
// into %253F, %23 into %2523, an encoded nested query is split into extra parameters, and %2F in a user name moves the host. So the
// text is read back out of the models: the first URL-looking run (links.js's LINK_URL_RE and trimUrl, so the same runs the terminal
// links) that parses to this very URI is what Monaco's click was on, and it goes on exactly as written. Only on a click; it stops at the
// first match, skips a model over MONACO_SCAN_MAX_CHARS and gives up after MONACO_SCAN_MAX_LINKS candidates. Any failure is "".
function monacoLinkText(monaco, uri) {
  try {
    if (typeof monaco.editor.getModels !== "function" || !monaco.Uri || typeof monaco.Uri.parse !== "function") return "";
    const want = uri.toString();
    // A private copy of the regex: the shared one keeps its lastIndex between calls.
    const re = new RegExp(typeof LINK_URL_RE !== "undefined" ? LINK_URL_RE.source : "https?:\\/\\/[^\\s<>\"'`]+", "gi");
    const trim = typeof trimUrl === "function" ? trimUrl : (u) => u.replace(/[.,;:!?)\]}>'"]+$/, "");
    let left = MONACO_SCAN_MAX_LINKS;
    for (const model of monaco.editor.getModels()) {
      if (model.getValueLength() > MONACO_SCAN_MAX_CHARS) continue;
      const text = model.getValue();
      re.lastIndex = 0;
      let m;
      while ((m = re.exec(text)) !== null) {
        if (--left < 0) return "";
        const c = trim(m[0]);
        if (c && monaco.Uri.parse(c).toString() === want) return c;
      }
    }
  } catch (_) {}
  return "";
}

// monacoBoot: loader.js, then require() of the editor, its stylesheet, then the theme, the unbound keys and the link opener.
async function monacoBoot() {
  const loader = document.createElement("script");
  const base = monacoBase();
  loader.src = base + "/loader.js";
  await monacoInject(loader);
  require.config({ paths: { vs: base } });
  await new Promise((resolve, reject) => require(["vs/editor/editor.main"], resolve, reject));
  await monacoStyled();
  const monaco = window.monaco;
  monaco.editor.defineTheme(MONACO_THEME, culiboxTheme());
  for (const k of MONACO_UNBOUND_KEYS) monaco.editor.addKeybindingRule({ keybinding: monaco.KeyCode[k.code], command: "-" + k.command });
  // Ctrl/Cmd+click on a link in the editor text (the dock editor and every diff editor) goes to the link gate (linkgate.js), like a
  // link in the terminal or in a rendered markdown file: a file in the box can say anything, and its URLs are as much the agent's
  // as its output. Monaco puts a registered opener ahead of its own, and true says "handled" for every scheme, so its opener never
  // runs; the gate itself refuses what is not http or https. open() is handed a parsed URI (registerLinkOpener's wrapper parses a
  // string first), never the text that was clicked, and URI.parse decodes it, so the link has to be put back together. Best: the text
  // as written, found again in the open models (monacoLinkText). Failing that, encodeURI(uri.toString(true)), what Monaco's own
  // external opener does: the readable form, which keeps a query's = & + : / unencoded (toString() would encode them and hide a
  // redirect from the gate) but is lossy for a %3F or %23 in the original.
  if (typeof monaco.editor.registerLinkOpener === "function") {
    monaco.editor.registerLinkOpener({
      open(uri) {
        if (typeof openAgentLink === "function") openAgentLink(monacoLinkText(monaco, uri) || encodeURI(uri.toString(true)), { source: "editor" });
        return true;
      },
    });
  }
  return monaco;
}

// loadMonaco() -> Promise<monaco>. Single-flight: every caller shares the one load.
// A failure (offline, blocked) is not remembered, so the next open tries again.
function loadMonaco() {
  if (!monacoLoading) monacoLoading = monacoBoot().catch((err) => { monacoLoading = null; throw err; });
  return monacoLoading;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { MONACO_BASE, monacoBase, MONACO_THEME, MONACO_UNBOUND_KEYS, MONACO_CSS_TIMEOUT_MS, loadMonaco };
}
