// P3 Task 5: the review bar types into a PTY, so the wiring is pinned at the source. What the code DOES is run in reviewbar.test.js
// (the real bar on the real tabs.js layout, the real decision and the real notes model); these pins say what no run can show: which
// calls exist and which never do. The delivery is ONE xterm paste (bracketed when the program asked for it), on a click, after the
// agent-state gate, the visibility decision and the composed (control-stripped, capped) message; Enter is never sent.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "").replace(/\s\/\/ .*$/gm, "");
const js = () => stripComments(read("reviewbar.js"));

// The text of one top-level function, from its line to the closing brace at column 0.
function fn(text, name) {
  const m = new RegExp("^function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function");
  return text.slice(m.index, text.indexOf("\n}\n", m.index) + 2);
}

test("reviewbar.js gates, composes and pastes: pasteBarState (pasteGate), composeMessage, term.paste", () => {
  const src = js();
  assert.match(src, /\bpasteBarState\(/);
  assert.match(src, /\bcomposeMessage\(/);
  assert.match(src, /\.term\.paste\(/);
  assert.match(read("reviewmodel.js"), /function pasteBarState\([\s\S]*?\bpasteGate\(/, "the bar's state is the model's gate plus the unsaved-file rule");
});

test("reviewbar.js has no sendKey, no sendText, no insertTextAtPrompt, no carriage-return literal, and no timer", () => {
  const src = js();
  assert.doesNotMatch(src, /sendKey/);
  assert.doesNotMatch(src, /sendText/);
  assert.doesNotMatch(src, /insertTextAtPrompt/);
  assert.doesNotMatch(src, /\\r|\\x0d|\\u000d|\\n\\r|String\.fromCharCode|charCodeAt/i, "no Enter anywhere: no \\r literal, no built-up control byte");
  assert.doesNotMatch(src, /["'`]\\n["'`]\s*\)\s*;?\s*$/m, "no newline appended to anything that is typed");
  assert.doesNotMatch(src, /setTimeout|setInterval|requestAnimationFrame|queueMicrotask|\.then\(|await /, "no queued or deferred paste: the click is the only trigger");
  assert.doesNotMatch(src, /\.ws\b[^;]*\.send|WebSocket\(/, "nothing reaches the socket but through xterm's paste");
});

test("term.paste is called in exactly one place in the whole page, and only after the state gate, the visibility decision and composeMessage, in one function", () => {
  const sites = [];
  for (const f of fs.readdirSync(STATIC).filter((n) => /\.js$/.test(n))) {
    const text = stripComments(read(f));
    const n = (text.match(/\.paste\(/g) || []).length;
    if (n) sites.push(f + ":" + n);
  }
  assert.deepStrictEqual(sites, ["reviewbar.js:1"], "xterm's paste is the review bar's alone (the terminal's own paste goes through its DOM paste event)");
  const now = fn(js(), "reviewBarPasteNow");
  const at = (re) => now.search(re);
  const gate = at(/reviewBarLive\(/), see = at(/insertRefused\(/), msg = at(/composeMessage\(/), paste = at(/\.term\.paste\(/);
  assert.ok(gate >= 0 && see > gate && msg > gate && paste > see && paste > msg, "state gate, then compose and the visibility decision, then paste: " + [gate, see, msg, paste]);
  assert.match(now, /if \(!st\.ok\)[\s\S]*?return false;/, "a refused state returns before anything");
  assert.match(now, /if \(insertRefused\(term\)\) return false;/);
  assert.match(now, /if \(!msg\.ok\)[\s\S]*?return false;/);
  assert.ok(now.indexOf("markSent(", paste) > paste, "notes are marked sent only after the paste");
});

test("the click path leaves Full mode through the one shared flow (editorRunLeavingFull) and never calls reviewBarPasteNow from anywhere else", () => {
  const src = js();
  assert.match(fn(src, "reviewBarPaste"), /editorRunLeavingFull\(/);
  const calls = src.match(/reviewBarPasteNow\(/g) || [];
  assert.equal(calls.length, 2, "its definition and the one call inside reviewBarPaste's callback");
  assert.match(fn(src, "reviewBarPaste"), /reviewBarPasteNow\(sid\)/);
  const sel = stripComments(read("editorselbar.js"));
  assert.match(fn(sel, "editorInsertLeavingFull"), /editorRunLeavingFull\(/, "the selection bar's Insert and the bar's Paste share it");
});

test("the notes are read through notesFor and changed through notesUpdate, never the raw store", () => {
  const src = js();
  assert.match(src, /\bnotesFor\(/);
  assert.match(src, /\bnotesUpdate\(/);
  assert.match(src, /\bnotesShowSent\(/);
  assert.doesNotMatch(src, /ntStore|ntRaw|loadNotes|localStorage|agent-box:notes/, "the store is the notes module's");
});

test("Preview is the kit's dialog with the composed text set as textContent, and no native dialog or HTML string is used", () => {
  const src = js();
  assert.match(fn(src, "reviewBarPreview"), /askDialog\(/);
  assert.match(fn(src, "reviewBarPreview"), /\.textContent = msg\.text/);
  assert.doesNotMatch(src, /\b(alert|confirm|prompt)\(/);
  assert.doesNotMatch(src, /innerHTML|insertAdjacentHTML|outerHTML/);
});

test("the bar is mounted by every EditorTab, repainted from the session-list poll and from the notes, and its script loads after editornotes.js", () => {
  const tab = stripComments(read("editortab.js"));
  assert.match(tab, /reviewBarMount\(this\)/);
  assert.match(tab, /reviewBarUnmount\(this\)/);
  const app = stripComments(read("app.js"));
  const i = app.indexOf("lastSessions = sessions;");
  assert.ok(i > 0);
  const j = app.indexOf("reviewBarRender(", i);
  assert.ok(j > i && j - i < 1500, "in the session-list update, right after lastSessions is set");
  assert.match(stripComments(read("editornotes.js")), /reviewBarRender\(/, "a change of the notes repaints the bar");
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("reviewbar.js") > at("editornotes.js") && at("editornotes.js") > 0);
});
