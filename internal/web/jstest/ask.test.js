"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { askOutcome, askAltVisible, matchesRequired, promptValue, shouldRefocusTerminal, askIsBusy, setDialogError } =
  require("../static/shell.js");

// A <dialog> closed by Esc, by a backdrop click, or by dialog.close() with no
// argument reports returnValue "". Only the confirm button sets it, so anything
// that is not exactly "confirm" must cancel: a dialog that failed open would
// delete a folder nobody agreed to delete.
test("only an explicit confirm counts as confirmation", () => {
  assert.strictEqual(askOutcome("confirm"), "confirm");
  for (const v of ["", "cancel", "Confirm", " confirm", undefined, null]) {
    assert.strictEqual(askOutcome(v), "cancel", String(v));
  }
});

test("the required name must match exactly once trimmed", () => {
  assert.strictEqual(matchesRequired(".git", ".git"), true);
  assert.strictEqual(matchesRequired("  .git  ", ".git"), true);
  assert.strictEqual(matchesRequired(".giT", ".git"), false);
  assert.strictEqual(matchesRequired(".git ext", ".git"), false);
  assert.strictEqual(matchesRequired("", ".git"), false);
});

// An absent requirement must never act as a skeleton key.
test("an absent requirement never auto-matches", () => {
  assert.strictEqual(matchesRequired("", ""), false);
  assert.strictEqual(matchesRequired("anything", ""), false);
  assert.strictEqual(matchesRequired("x", null), false);
  assert.strictEqual(matchesRequired(null, "x"), false);
});

// null means "cancelled" and is distinct from "": promptMkdir has to tell a
// dismissed dialog from a submitted-but-blank one.
test("a cancelled prompt yields null, a confirmed one yields trimmed text", () => {
  assert.strictEqual(promptValue("", "ignored"), null);
  assert.strictEqual(promptValue("cancel", "ignored"), null);
  assert.strictEqual(promptValue("confirm", "  notes.txt  "), "notes.txt");
  assert.strictEqual(promptValue("confirm", ""), "");
  assert.strictEqual(promptValue("confirm", undefined), "");
});

// A second askDialog() while one is open used to reconfigure the dialog in
// place: it overwrote the title, cleared a mustMatch gate, and left BOTH close
// listeners attached, so one click settled two promises. showModal() does not
// throw on an already-open dialog, so nothing surfaced the collision. Reachable
// with the keyboard: the confirm button is focused, so the palette hotkey still
// opens, and running "Kill session" from there re-labelled the live dialog.
test("a second ask is refused while one is already open", () => {
  assert.strictEqual(askIsBusy(true), true);
  assert.strictEqual(askIsBusy(false), false);
});

// focusTerminal runs on every dialog close. With a nested dialog still open it
// would pull the caret into the terminal underneath, and keystrokes would stop
// reaching the pane with nothing on screen to explain it.
test("focus returns to the terminal only when no dialog is left open", () => {
  assert.strictEqual(shouldRefocusTerminal(0), true);
  assert.strictEqual(shouldRefocusTerminal(1), false);
  assert.strictEqual(shouldRefocusTerminal(3), false);
});

// The profile editor's error line sits at the bottom of a scrolling form, so a
// failed Save wrote its message below the fold and nothing visibly happened.
// Setting a message must bring the line into view; clearing it must not move
// the form under the reader.
test("setDialogError scrolls a new message into view, and only a message", () => {
  const calls = [];
  const el = { textContent: "", scrollIntoView: (o) => calls.push(o) };
  global.document = { querySelector: (sel) => (sel === "#settings-modal .dlg-err" ? el : null) };
  try {
    setDialogError("settings-modal", "aws.region: not a region");
    assert.strictEqual(el.textContent, "aws.region: not a region");
    assert.deepStrictEqual(calls, [{ block: "nearest" }]);
    setDialogError("settings-modal", "");
    assert.strictEqual(el.textContent, "");
    assert.strictEqual(calls.length, 1);
    setDialogError("nope", "x"); // no such dialog: no throw
  } finally {
    delete global.document;
  }
});

// The third button of askChoice. "alt" is only ever set by #ask-alt, so it maps
// to itself; every other close route still fails safe to a cancel.
test("alt maps to alt and nothing else changes", () => {
  assert.strictEqual(askOutcome("alt"), "alt");
  assert.strictEqual(askOutcome("confirm"), "confirm");
  for (const v of ["", "cancel", "ALT", " alt", undefined, null]) {
    assert.strictEqual(askOutcome(v), "cancel", String(v));
  }
});

// Every existing dialog passes no altLabel and must keep its two buttons.
test("the alt button shows only when a label is given", () => {
  assert.strictEqual(askAltVisible("End session only"), true);
  for (const v of [undefined, null, ""]) assert.strictEqual(askAltVisible(v), false, String(v));
});

// The alt button ships hidden, so a dialog that never names it cannot show it.
test("index.html ships #ask-alt hidden, after Cancel", () => {
  const html = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "index.html"), "utf8");
  assert.match(html, /<button id="ask-cancel"[^>]*>Cancel<\/button>\s*<button id="ask-alt"[^>]*\bhidden\b/);
});

// --- askDialog opts.extra and opts.focusCancel (the profile page's "Save as host" lists what it removes) ----------------------

// shell.js on a fake DOM with the ask dialog's parts, enough to run askDialog and answer it.
function askEnv() {
  const fs = require("node:fs");
  const path = require("node:path");
  const vm = require("node:vm");
  const mk = (extra) => Object.assign({ textContent: "", hidden: false, disabled: false, value: "", placeholder: "", focus() { askEnvFocus.at = this; } }, extra);
  let askEnvFocus = { at: null };
  const closeFns = [];
  const cls = new Set();
  const d = mk({
    open: false, returnValue: "",
    classList: { toggle: (c, on) => { if (on) cls.add(c); else cls.delete(c); }, contains: (c) => cls.has(c) },
    addEventListener: (ev, fn) => { if (ev === "close") closeFns.push(fn); },
    removeEventListener: (ev, fn) => { const i = closeFns.indexOf(fn); if (ev === "close" && i >= 0) closeFns.splice(i, 1); },
    showModal() { this.open = true; },
    close(v) { this.returnValue = v || ""; this.open = false; for (const f of closeFns.slice()) f(); },
  });
  const extra = mk({ children: [], replaceChildren(...k) { this.children = k; } });
  const els = {
    "ask-modal": d, "ask-input": mk(), "ask-ok": mk(), "ask-title": mk(), "ask-body": mk(), "ask-cancel": mk(), "ask-alt": mk(),
    "ask-label": mk(), "ask-x": mk(), "ask-extra": extra, "ask-hint": mk(),
  };
  // Just enough DOM to build a note: an element has a tag, a class, text and children; a text node has text.
  const doc = {
    getElementById: (id) => els[id] || null,
    createElement: (tag) => ({ tag, className: "", textContent: "", children: [], appendChild(c) { this.children.push(c); return c; } }),
    createTextNode: (text) => ({ text: String(text) }),
  };
  const ctx = vm.createContext({ document: doc, Promise, Array, Object });
  new vm.Script(fs.readFileSync(path.join(__dirname, "..", "static", "shell.js"), "utf8"), { filename: "shell.js" }).runInContext(ctx);
  return { ctx, els, cls, focus: () => askEnvFocus.at };
}

test("askDialog puts opts.extra under the body, widens the card, and the next ask without it clears both", async () => {
  const t = askEnv();
  const note = { textContent: "A host profile cannot hold these" };
  const list = { textContent: "AWS connections prod-read" };
  const first = t.ctx.askConfirm({ title: "Save as host", body: "body", extra: [note, list], danger: true, confirmLabel: "Save as host" });
  assert.deepStrictEqual(t.els["ask-extra"].children, [note, list]);
  assert.strictEqual(t.els["ask-extra"].hidden, false);
  assert.strictEqual(t.cls.has("wide"), true);
  assert.strictEqual(t.els["ask-body"].textContent, "body");
  t.els["ask-modal"].close("confirm");
  assert.strictEqual(await first, true);
  // Another question, no extra: nothing of the last one is left (the same reset as the cancel label).
  const second = t.ctx.askConfirm({ title: "Delete profile", body: "sure?", confirmLabel: "Delete", danger: true });
  assert.deepStrictEqual(t.els["ask-extra"].children, []);
  assert.strictEqual(t.els["ask-extra"].hidden, true);
  assert.strictEqual(t.cls.has("wide"), false);
  t.els["ask-modal"].close("cancel");
  assert.strictEqual(await second, false);
  // A single node is accepted as well as a list.
  const third = t.ctx.askConfirm({ title: "t", body: "b", extra: note });
  assert.deepStrictEqual(t.els["ask-extra"].children, [note]);
  t.els["ask-modal"].close("cancel");
  await third;
});

test("askDialog focuses OK by default and Cancel when asked (a yes that must not be one keypress away)", async () => {
  const t = askEnv();
  const a = t.ctx.askConfirm({ title: "t", body: "b" });
  assert.strictEqual(t.focus(), t.els["ask-ok"]);
  t.els["ask-modal"].close("cancel");
  await a;
  const b = t.ctx.askConfirm({ title: "t", body: "b", focusCancel: true });
  assert.strictEqual(t.focus(), t.els["ask-cancel"]);
  t.els["ask-modal"].close("cancel");
  await b;
});

test("the ask dialog has a hidden extra slot under the body, and the extra classes have rules", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const html = fs.readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");
  assert.match(html, /<p id="ask-body"><\/p>\s*<div id="ask-extra" hidden><\/div>/);
  const css = fs.readFileSync(path.join(__dirname, "..", "static", "desktop.css"), "utf8");
  for (const sel of ["#ask-modal.wide", "#ask-modal .ask-note", "#ask-modal .ask-list", "#ask-modal .ask-mono", "#ask-modal .ask-q"]) assert.ok(css.includes(sel), sel);
});

// --- the dialog family (#173 Task 4): hint, cls, footless, notes, wide -----------------------------------------------------------

// A note's text as the page shows it: text nodes as they are, a mono span in its own tag.
const noteText = (p) => p.children.map((c) => (c.tag === "span" ? "[" + c.className + ":" + c.textContent + "]" : c.text)).join("");

test("askDialog's footer hint is 'Esc to cancel' by default, empty for an info dialog, and an explicit hint wins", async () => {
  const t = askEnv();
  const a = t.ctx.askConfirm({ title: "t", body: "b" });
  assert.strictEqual(t.els["ask-hint"].textContent, "Esc to cancel");
  t.els["ask-modal"].close("cancel");
  await a;
  const b = t.ctx.askChoice({ title: "Keyboard shortcuts", body: "b", info: true, confirmLabel: "Close" });
  assert.strictEqual(t.els["ask-hint"].textContent, "", "no Cancel on an info dialog, so no hint");
  t.els["ask-modal"].close("cancel");
  await b;
  // The hint is reset on every ask: nothing of the last one is left.
  const c = t.ctx.askConfirm({ title: "t", body: "b", hint: "Enter to save" });
  assert.strictEqual(t.els["ask-hint"].textContent, "Enter to save");
  t.els["ask-modal"].close("cancel");
  await c;
  const d = t.ctx.askPrompt({ title: "New folder", label: "Name" });
  assert.strictEqual(t.els["ask-hint"].textContent, "Esc to cancel", "a prompt shows Cancel too");
  t.els["ask-modal"].close("cancel");
  await d;
});

test("askDialog's cls is one class on the dialog and is gone on the next ask", async () => {
  const t = askEnv();
  const a = t.ctx.askChoice({ title: "End session x?", body: "b", cls: "end" });
  assert.strictEqual(t.cls.has("end"), true);
  t.els["ask-modal"].close("cancel");
  assert.strictEqual(await a, "cancel");
  const b = t.ctx.askConfirm({ title: "t", body: "b" });
  assert.strictEqual(t.cls.has("end"), false, "the next ask without cls does not inherit it");
  t.els["ask-modal"].close("cancel");
  await b;
  // Two asks in a row that both give one: the second class replaces the first.
  const c = t.ctx.askConfirm({ title: "t", body: "b", cls: "end" });
  t.els["ask-modal"].close("cancel");
  await c;
  const d = t.ctx.askConfirm({ title: "t", body: "b", cls: "keys" });
  assert.strictEqual(t.cls.has("keys"), true);
  assert.strictEqual(t.cls.has("end"), false);
  t.els["ask-modal"].close("cancel");
  await d;
});

test("askDialog's footless hides the footer for that ask only, and Esc and the X still settle it", async () => {
  const t = askEnv();
  const a = t.ctx.askChoice({ title: "t", body: "b", footless: true });
  assert.strictEqual(t.cls.has("footless"), true);
  assert.strictEqual(t.focus(), t.els["ask-x"], "nothing in the footer to focus: the X is where focus lands");
  t.els["ask-modal"].close(""); // Esc: the dialog closes with no return value
  assert.strictEqual(await a, "cancel");
  const b = t.ctx.askChoice({ title: "t", body: "b", footless: true });
  t.els["ask-x"].onclick();
  assert.strictEqual(await b, "cancel");
  const c = t.ctx.askConfirm({ title: "t", body: "b" });
  assert.strictEqual(t.cls.has("footless"), false, "the next ask has its footer back");
  t.els["ask-modal"].close("confirm");
  assert.strictEqual(await c, true);
});

test("askDialog's notes are one paragraph each under the body, with a mono span for a path", async () => {
  const t = askEnv();
  const a = t.ctx.askChoice({
    title: "End session x?", body: "The agent stops now.",
    notes: [["Files in its workspace, ", { mono: "~/box-clones/x" }, ", are not touched."], ["It has 1 shell open, shell 1."]],
  });
  const kids = t.els["ask-extra"].children;
  assert.deepStrictEqual(kids.map((p) => p.tag), ["p", "p"]);
  assert.deepStrictEqual(kids.map(noteText), ["Files in its workspace, [mono:~/box-clones/x], are not touched.", "It has 1 shell open, shell 1."]);
  assert.strictEqual(t.els["ask-extra"].hidden, false);
  assert.strictEqual(t.cls.has("wide"), false, "notes are not a list: the card keeps its width");
  t.els["ask-modal"].close("cancel");
  await a;
  // The next ask without notes: the slot is empty and hidden again.
  const b = t.ctx.askConfirm({ title: "t", body: "b" });
  assert.deepStrictEqual(t.els["ask-extra"].children, []);
  assert.strictEqual(t.els["ask-extra"].hidden, true);
  t.els["ask-modal"].close("cancel");
  await b;
  // An empty list of notes leaves no empty slot.
  const c = t.ctx.askConfirm({ title: "t", body: "b", notes: [] });
  assert.strictEqual(t.els["ask-extra"].hidden, true);
  t.els["ask-modal"].close("cancel");
  await c;
});

test("askDialog's wide: undefined is today's rule (wide with extra), false keeps it off, true forces it", async () => {
  const t = askEnv();
  const node = { textContent: "list" };
  const a = t.ctx.askConfirm({ title: "t", body: "b", extra: [node] });
  assert.strictEqual(t.cls.has("wide"), true, "extra, no wide option: wide as before");
  t.els["ask-modal"].close("cancel");
  await a;
  const b = t.ctx.askConfirm({ title: "t", body: "b", extra: [node], wide: false });
  assert.deepStrictEqual(t.els["ask-extra"].children, [node]);
  assert.strictEqual(t.cls.has("wide"), false);
  t.els["ask-modal"].close("cancel");
  await b;
  const c = t.ctx.askConfirm({ title: "t", body: "b", wide: true });
  assert.strictEqual(t.cls.has("wide"), true);
  t.els["ask-modal"].close("cancel");
  await c;
  const d = t.ctx.askConfirm({ title: "t", body: "b" });
  assert.strictEqual(t.cls.has("wide"), false, "reset on the next ask");
  t.els["ask-modal"].close("cancel");
  await d;
});

// --- the markup and the styles of the family ---------------------------------------------------------------------------------------

const STATIC_DIR = require("node:path").join(__dirname, "..", "static");
const readStatic = (f) => require("node:fs").readFileSync(require("node:path").join(STATIC_DIR, f), "utf8");

test("index.html: #ask-hint is the first thing in the footer row, and the three dialog Xs read 'Close (Esc)'", () => {
  const html = readStatic("index.html");
  assert.match(html, /<div class="ask-actions">\s*<span id="ask-hint"><\/span>\s*<button id="ask-cancel"/);
  const tag = (re) => { const m = re.exec(html); assert.ok(m, String(re)); return m[0]; };
  for (const x of [
    tag(/<button[^>]*id="ask-x"[^>]*>/),
    tag(/<button[^>]*class="dlg-x"[^>]*data-close="spawn-modal"[^>]*>/),
    tag(/<button[^>]*data-close="bug-modal"[^>]*>/),
  ]) {
    assert.match(x, /aria-label="Close \(Esc\)"/, x);
    assert.match(x, /title="Close \(Esc\)"/, x);
  }
  // The phone sheet's own close is not one of the three: untouched.
  assert.match(html, /<button class="spawn-x" type="button" data-close="spawn-modal" title="Close">/);
});

test("the footer hint is hidden by a base rule in app.css (the phone never shows it) and shown by desktop.css only", () => {
  const app = readStatic("app.css").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(app, /(^|\})\s*#ask-hint\s*\{\s*display:\s*none;?\s*\}/, "base rule");
  const desk = readStatic("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const m = /#ask-modal #ask-hint\s*\{([^{}]*)\}/.exec(desk);
  assert.ok(m, "desktop #ask-hint rule");
  assert.match(m[1], /display:\s*block/);
  assert.match(m[1], /flex:\s*1 1 auto/);
  assert.match(m[1], /font-size:\s*var\(--t-sm\)/);
  assert.match(m[1], /color:\s*var\(--muted\)/);
});

test("desktop.css draws the End session card: 480 px, a footer rule, the kit buttons, muted notes", () => {
  const desk = readStatic("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => { const i = desk.indexOf(sel + " {"); assert.ok(i >= 0, sel); return desk.slice(i, desk.indexOf("}", i)); };
  assert.match(rule("#ask-modal.end"), /width:\s*480px/);
  assert.match(rule("#ask-modal.footless .ask-actions"), /display:\s*none/);
  const foot = rule("#ask-modal .ask-actions");
  assert.match(foot, /border-top:\s*1px solid var\(--line\)/);
  assert.match(foot, /padding:\s*var\(--sp-3\) var\(--sp-5\)/);
  // Cancel and the alt are the kit secondary; the red one is the kit .btn-danger (clear fill, red text, a 45% red border).
  const danger = rule("#ask-modal.danger #ask-ok");
  assert.match(danger, /background:\s*transparent/);
  assert.match(danger, /color:\s*var\(--red\)/);
  assert.match(danger, /border-color:\s*rgba\(240, ?106, ?106, ?\.45\)/);
  assert.match(rule("#ask-modal.danger #ask-ok:hover"), /background:\s*var\(--red-tint\)/);
  assert.match(rule("#ask-modal .ask-para"), /margin:\s*var\(--sp-4\) 0 0/);
  assert.match(rule("#ask-modal .ask-para"), /color:\s*var\(--muted\)/);
  assert.match(rule("#ask-modal .ask-para .mono"), /font-size:\s*var\(--t-sm\)/);
  assert.match(rule("#ask-modal #ask-body"), /color:\s*var\(--fg\)/, "the first paragraph is the foreground, the notes are muted");
  // The three dialogs' one close (desktop-chrome.test.js pins its look); here only that the New session one lost its "Esc" word.
  assert.ok(!desk.includes("#spawn-modal .dlg-x::before"), "the New session close drops its Esc word");
});

test("shell.js builds notes with createElement (no innerHTML): a path in a note is text", () => {
  const src = readStatic("shell.js");
  const at = src.indexOf("function askNoteNodes(");
  assert.ok(at >= 0, "askNoteNodes");
  assert.ok(!/innerHTML/.test(src.slice(at, src.indexOf("\n}\n", at))), "no innerHTML in the notes builder");
});
