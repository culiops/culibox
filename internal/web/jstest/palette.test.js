"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { isPaletteHotkey, isInsertHotkey, palSection, palGroup, palPreferDir, palSessionMatchText, palModHint } = require("../static/palette.js");

// A KeyboardEvent stand-in: only the fields the matcher reads.
const ev = (code, mods) => Object.assign(
  { code, ctrlKey: false, shiftKey: false, altKey: false, metaKey: false }, mods || {});

test("mac opens on Cmd+K", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { metaKey: true }), true), true);
});

test("mac ignores Ctrl+Shift+K — Cmd is the platform convention", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true, shiftKey: true }), true), false);
});

test("non-mac opens on Ctrl+Shift+K", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true, shiftKey: true }), false), true);
});

// Ctrl+Shift+K is a magnet for global hotkeys registered by other desktop apps
// (Notion took it on the reporter's Windows machine, so Chrome never saw the
// keypress at all). The page cannot win that fight, so it offers a second combo.
test("non-mac also opens on Ctrl+Shift+Space", () => {
  assert.strictEqual(isPaletteHotkey(ev("Space", { ctrlKey: true, shiftKey: true }), false), true);
});

test("mac also accepts Ctrl+Shift+Space, so one combo works everywhere", () => {
  assert.strictEqual(isPaletteHotkey(ev("Space", { ctrlKey: true, shiftKey: true }), true), true);
});

test("plain Ctrl+K is never the palette — it is readline's kill-to-end-of-line", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true }), false), false);
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true }), true), false);
});

test("plain Space and Shift+Space are left alone", () => {
  assert.strictEqual(isPaletteHotkey(ev("Space"), false), false);
  assert.strictEqual(isPaletteHotkey(ev("Space", { shiftKey: true }), false), false);
});

test("adding Alt or the other platform modifier disqualifies the combo", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true, shiftKey: true, altKey: true }), false), false);
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { ctrlKey: true, shiftKey: true, metaKey: true }), false), false);
  assert.strictEqual(isPaletteHotkey(ev("Space", { ctrlKey: true, shiftKey: true, altKey: true }), false), false);
  assert.strictEqual(isPaletteHotkey(ev("KeyK", { metaKey: true, ctrlKey: true }), true), false);
});

test("another key with the right modifiers is not the palette", () => {
  assert.strictEqual(isPaletteHotkey(ev("KeyJ", { ctrlKey: true, shiftKey: true }), false), false);
});

// Ctrl+Enter (⌘+Enter on mac) inserts the selected file's path at the prompt.
// Plain Enter must keep opening the viewer, so the modifier is what separates
// them and a bare Enter must never match.
test("ctrl+enter is the insert hotkey off mac", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter", ctrlKey: true }, false), true);
  assert.strictEqual(isInsertHotkey({ key: "Enter", metaKey: true }, false), false);
});

test("cmd+enter is the insert hotkey on mac", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter", metaKey: true }, true), true);
  assert.strictEqual(isInsertHotkey({ key: "Enter", ctrlKey: true }, true), false);
});

test("a bare enter is never the insert hotkey", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter" }, false), false);
  assert.strictEqual(isInsertHotkey({ key: "Enter" }, true), false);
});

test("a modified non-enter key is never the insert hotkey", () => {
  assert.strictEqual(isInsertHotkey({ key: "k", ctrlKey: true }, false), false);
});

// AltGr (common on non-US keyboards off mac) reports as BOTH ctrlKey and
// altKey, so AltGr+Enter would otherwise satisfy the off-mac Ctrl+Enter check
// and insert a path when the user just meant to open the file.
test("altgr+enter off mac is not the insert hotkey", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter", ctrlKey: true, altKey: true }, false), false);
});

test("cmd+alt+enter on mac is not the insert hotkey", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter", metaKey: true, altKey: true }, true), false);
});

// --- palSection / palGroup (kind -> D4 section heading) ---------------------

test("palSection maps session and file to their own headings, everything else to COMMANDS", () => {
  assert.strictEqual(palSection("session"), "SESSIONS");
  assert.strictEqual(palSection("file"), "FILES");
  for (const kind of ["new", "kill", "layout", "settings", "view"]) {
    assert.strictEqual(palSection(kind), "COMMANDS");
  }
});

// The regroup must not silently drop or duplicate rows regardless of the
// caller's original order (sessions/commands interleaved, files appended
// later by the async search) — palRender trusts palGroup's output 1:1 as the
// new palItems/selection order.
test("palGroup renders SESSIONS, then FILES, then COMMANDS, regardless of input order", () => {
  const items = [
    { kind: "new", label: "New session…" },
    { kind: "session", label: "agent-box" },
    { kind: "file", label: "peek.go" },
    { kind: "view", label: "Toggle sidebar" },
    { kind: "session", label: "culipulse" },
  ];
  const grouped = palGroup(items);
  assert.deepStrictEqual(grouped.map((g) => g.section),
    ["SESSIONS", "SESSIONS", "FILES", "COMMANDS", "COMMANDS"]);
  // Order WITHIN a section is preserved from the input.
  assert.deepStrictEqual(grouped.map((g) => g.it.label),
    ["agent-box", "culipulse", "peek.go", "New session…", "Toggle sidebar"]);
});

test("palGroup omits a section entirely when it has no rows (commands-only mode)", () => {
  const items = [{ kind: "new", label: "New session…" }, { kind: "view", label: "Toggle sidebar" }];
  const grouped = palGroup(items);
  assert.deepStrictEqual(grouped.map((g) => g.section), ["COMMANDS", "COMMANDS"]);
});

test("palPreferDir: the focused session's cwd, else the tree root, else nothing", () => {
  assert.strictEqual(palPreferDir({ cwd: "/home/u/repo" }, "/home/u/other"), "/home/u/repo");
  assert.strictEqual(palPreferDir({ cwd: "" }, "/home/u/other"), "/home/u/other");
  assert.strictEqual(palPreferDir(null, "/home/u/other"), "/home/u/other");
  assert.strictEqual(palPreferDir(null, ""), "");
});

test("sessions match by name and by summary", () => {
  assert.strictEqual(palSessionMatchText({ id: "claude-3", name: "api", summary: "Fixed the flaky login test" }),
    "api Fixed the flaky login test");
  assert.strictEqual(palSessionMatchText({ id: "claude-3" }), "claude-3");
});

test("palModHint follows the selected row's kind (⌘↵ keeps inserting a file's path)", () => {
  assert.strictEqual(palModHint("session", true), "⌘↵ open in new pane");
  assert.strictEqual(palModHint("session", false), "^↵ open in new pane");
  assert.strictEqual(palModHint("file", true), "⌘↵ insert path");
  assert.strictEqual(palModHint("view", false), "");
});

test("paletteShellCommands: desktop offers shortcuts, files drawer and the Activity and Access views once", () => {
  const { paletteShellCommands } = require("../static/palette.js");
  const ran = [];
  const cmds = paletteShellCommands(false, true, { activityView: () => ran.push("activity"), accessView: () => ran.push("access") });
  const ids = cmds.map((c) => c.id);
  for (const id of ["shortcuts", "files-drawer", "activity-view", "access-view"]) assert.ok(ids.includes(id), id);
  assert.ok(!ids.includes("access-drawer"), "Access is a pane view, not a drawer");
  assert.ok(!ids.includes("steer-rail"), "no second Access entry");
  assert.ok(!ids.includes("toggle-files"), "the old files collapse is dead on desktop (R17)");
  assert.equal(cmds.find((c) => c.id === "access-view").label, "Access");
  assert.equal(cmds.find((c) => c.id === "activity-view").label, "Activity");
  cmds.find((c) => c.id === "activity-view").run();
  cmds.find((c) => c.id === "access-view").run();
  assert.deepEqual(ran, ["activity", "access"]);
  assert.equal(cmds.find((c) => c.id === "files-drawer").hint, "Ctrl+Alt+F");
});

test("paletteShellCommands: mobile keeps the old entries and gets none of the new", () => {
  const { paletteShellCommands } = require("../static/palette.js");
  const ids = paletteShellCommands(false, false, {}).map((c) => c.id);
  assert.deepEqual(ids.sort(), ["steer-rail", "toggle-files"]);
});

test("paletteShellSpawn: desktop offers + Shell below (the shortcut), right and own pane", () => {
  const { paletteShellSpawn } = require("../static/palette.js");
  const asked = [];
  const cmds = paletteShellSpawn(true, (p) => asked.push(p));
  assert.deepEqual(cmds.map((c) => c.label), ["+ Shell below", "+ Shell right", "+ Shell in own pane"]);
  assert.deepEqual(cmds.map((c) => c.hint), ["Ctrl+Shift+`", "", ""]);
  for (const c of cmds) { assert.equal(c.kind, "new"); c.run(); }
  assert.deepEqual(asked, ["below", "right", "own"]);
});

test("paletteShellSpawn: mobile keeps the one v0.2.0 entry", () => {
  const { paletteShellSpawn } = require("../static/palette.js");
  const asked = [];
  const cmds = paletteShellSpawn(false, (p) => asked.push(p));
  assert.deepEqual(cmds.map((c) => [c.label, c.hint]), [["New shell here", "Ctrl+Shift+`"]]);
  cmds[0].run();
  assert.deepEqual(asked, ["below"]);
});

test("palSessionRun: a nested shell is revealed beside its parent, any other session opened", () => {
  const { palSessionRun } = require("../static/palette.js");
  const calls = [];
  const act = { reveal: (id) => calls.push(["reveal", id]), open: (id, name) => calls.push(["open", id, name]) };
  palSessionRun({ id: "sh-1", name: "sh:w" }, true, act)();
  palSessionRun({ id: "a-1", name: "agent" }, false, act)();
  palSessionRun({ id: "a-2" }, false, act)();
  assert.deepEqual(calls, [["reveal", "sh-1"], ["open", "a-1", "agent"], ["open", "a-2", "a-2"]]);
});
