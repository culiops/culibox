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
    "ask-label": mk(), "ask-x": mk(), "ask-extra": extra,
  };
  const ctx = vm.createContext({ document: { getElementById: (id) => els[id] || null }, Promise, Array });
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
