"use strict";
// A text file that is not valid UTF-8 (Latin-1, CP1252: no NUL byte, so the server calls it text) is shown with U+FFFD for
// every invalid byte, and saving that buffer would write the replacements over the original bytes. The server says so on
// both reads (`utf8: false`, fsapi_test.go); this file proves what the editor does with it: the tab opens read-only with the
// reason, the status bar names the encoding it really has, nothing is ever written, and a file that turns non-UTF-8 under an
// open tab (the agent wrote it) locks the tab the same way. The phone's ViewerTab (CM5) is covered at the end.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { page, keydown } = require("./editorpage.js");

const NOT_UTF8 = "This file is not UTF-8; saving it here would change its bytes.";
const latin1 = (extra) => ({ ok: true, status: 200, data: Object.assign({ kind: "text", content: "caf� = 1\n", version: "v1", utf8: false }, extra) });

test("a text file that is not UTF-8 opens read-only with the reason, and the status bar says it is not UTF-8", async () => {
  const p = page();
  p.setRead(latin1());
  const tab = await p.open("/p/latin.txt");
  assert.equal(tab.mode, "read");
  assert.equal(tab.readOnly, true);
  assert.equal(tab.model.value, "caf� = 1\n", "it is still shown");
  assert.equal(tab.roEl.hidden, false);
  assert.equal(tab.roEl.textContent, NOT_UTF8, "the note under the crumbs gives the reason");
  assert.equal(tab.sbParts.enc.textContent, "Not UTF-8", "the status bar does not claim UTF-8");
  assert.equal(p.st.editors[0].options.at(-1).readOnly, true, "Monaco is read-only over it");
  assert.equal(p.st.editors[0].options.at(-1).readOnlyMessage.value, NOT_UTF8, "and tells the user why when they type");
});

test("a UTF-8 file, with the flag true or absent, is as editable as ever and says UTF-8", async () => {
  for (const data of [{ utf8: true }, {}]) {
    const p = page();
    p.setRead({ ok: true, status: 200, data: Object.assign({ kind: "text", content: "café\n", version: "v1" }, data) });
    const tab = await p.open("/p/utf8.txt");
    assert.equal(tab.mode, "edit", JSON.stringify(data));
    assert.equal(tab.sbParts.enc.textContent, "UTF-8");
    assert.equal(tab.roEl.hidden, true);
  }
});

test("a non-UTF-8 file is never written: Mod+S and save say why, typing is not a dirty buffer", async () => {
  const p = page();
  p.setRead(latin1());
  const tab = await p.open("/p/latin.txt");
  tab.model.type("caf� = 2\n");
  assert.equal(tab.dirty(), false, "an edit that cannot be saved draws no dot and pins nothing");
  await tab.save();
  tab.el.listeners.keydown(keydown());
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.writes.length, 0, "not one byte goes back");
  assert.equal(p.st.toasts.length, 2);
  assert.equal(p.st.toasts[0][0], NOT_UTF8);
  assert.deepEqual(p.st.host, []);
});

test("the server's own read-only reason and a box file keep their sentences over the encoding's", async () => {
  const p = page();
  p.setRead(latin1({ readonly: true }));
  const tab = await p.open("/tmp/claude-1/latin.txt");
  assert.match(tab.roEl.textContent, /server never saves/);
  assert.equal(tab.sbParts.enc.textContent, "Not UTF-8", "the encoding is still told truly");
});

test("a clean tab whose file turns non-UTF-8 on disk reloads and locks; when it is UTF-8 again it is editable again", async () => {
  const p = page();
  const disk = p.serve("one\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  assert.equal(tab.mode, "edit");
  disk.text = "caf�\n";
  disk.utf8 = false;
  await p.tick();
  assert.equal(tab.model.value, "caf�\n");
  assert.equal(tab.mode, "read", "the agent's file is not something this buffer can write back");
  assert.equal(tab.readOnly, true);
  assert.equal(tab.roEl.textContent, NOT_UTF8);
  assert.equal(tab.sbParts.enc.textContent, "Not UTF-8");
  assert.equal(p.st.editors[0].options.at(-1).readOnly, true, "the shared editor is told, not only the tab");
  tab.model.type("typed\n");
  await tab.save();
  assert.equal(p.st.writes.length, 0);
  disk.text = "plain again\n";
  disk.utf8 = true;
  await p.tick();
  assert.equal(tab.model.value, "plain again\n");
  assert.equal(tab.mode, "edit");
  assert.equal(tab.readOnly, false);
  assert.equal(tab.roEl.hidden, true);
  assert.equal(tab.sbParts.enc.textContent, "UTF-8");
  assert.equal(p.st.editors[0].options.at(-1).readOnly, false);
});

test("a dirty tab whose file turns non-UTF-8 gets the bar, and Load the agent's version locks it (Keep mine stays the user's choice)", async () => {
  const p = page();
  const disk = p.serve("one\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "caf�\n";
  disk.utf8 = false;
  await p.tick();
  assert.equal(tab.barEl.hidden, false, "a dirty buffer is never replaced without a choice");
  assert.equal(tab.mode, "edit", "and it is still the user's buffer, which is valid text");
  const [, , acts] = tab.barEl.children;
  acts.children[2].click(); // Load the agent's version
  await p.settle();
  assert.equal(tab.model.value, "caf�\n");
  assert.equal(tab.mode, "read");
  assert.equal(tab.dirty(), false);
  assert.equal(tab.roEl.textContent, NOT_UTF8);
});

test("the phone viewer: readOnlyFrom marks a non-UTF-8 text read read-only, so the Edit button is not offered", () => {
  const { readOnlyFrom } = require("../static/copyfile.js");
  assert.equal(readOnlyFrom({ ok: true, data: { kind: "text", content: "x", utf8: false } }), true);
  assert.equal(readOnlyFrom({ ok: true, data: { kind: "text", content: "x", utf8: true } }), false);
  assert.equal(readOnlyFrom({ ok: true, data: { kind: "text", content: "x" } }), false);
  assert.equal(readOnlyFrom({ ok: false, status: 500, data: { utf8: false } }), false, "only a successful read says so");
  // The viewer takes its Edit from that answer and the toolbar hides Edit for a read-only viewer.
  const { viewerToolbarModel } = require("../static/viewertoolbar.js");
  assert.equal(viewerToolbarModel({ path: "/a.txt", readOnly: true }).edit.shown, false);
  const src = fs.readFileSync(path.join(__dirname, "..", "static", "viewer-tab.js"), "utf8");
  assert.match(src, /readOnlyFrom\(res\)/);
});
