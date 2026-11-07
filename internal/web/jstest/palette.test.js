"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { isInsertHotkey, palSection, palGroup, palPreferDir, palSessionMatchText, palModHint, palSessionDot } = require("../static/palette.js");
const { activityClass } = require("../static/activity.js");

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

test("shift or alt-graph or IME composition is not the insert hotkey", () => {
  assert.strictEqual(isInsertHotkey({ key: "Enter", ctrlKey: true, shiftKey: true }, false), false);
  assert.strictEqual(isInsertHotkey({ key: "Enter", metaKey: true, shiftKey: true }, true), false);
  const ag = { key: "Enter", ctrlKey: true, getModifierState: (k) => k === "AltGraph" };
  assert.strictEqual(isInsertHotkey(ag, false), false);
  assert.strictEqual(isInsertHotkey({ key: "Enter", ctrlKey: true, isComposing: true }, false), false);
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
  assert.strictEqual(palModHint("session", false), "Ctrl+↵ open in new pane");
  assert.strictEqual(palModHint("file", true), "⌘↵ insert path");
  assert.strictEqual(palModHint("file", false), "Ctrl+↵ insert path");
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
  assert.equal(cmds.find((c) => c.id === "files-drawer").hint, "Ctrl+Shift+E");
});

test("paletteShellCommands: hints are the registry's, per platform", () => {
  const { paletteShellCommands } = require("../static/palette.js");
  const hint = (mac, desktop, id) => paletteShellCommands(mac, desktop, {}).find((c) => c.id === id).hint;
  assert.equal(hint(true, true, "files-drawer"), "⇧⌘E");
  assert.equal(hint(false, false, "toggle-files"), "Ctrl+Shift+E");
  assert.equal(hint(true, true, "shortcuts"), "⌘/");
  assert.equal(hint(false, true, "shortcuts"), "Ctrl+/");
});

test("paletteShellCommands: mobile keeps the old entries and gets none of the new", () => {
  const { paletteShellCommands } = require("../static/palette.js");
  const ids = paletteShellCommands(false, false, {}).map((c) => c.id);
  assert.deepEqual(ids.sort(), ["steer-rail", "toggle-files"]);
});

test("paletteShellSpawn: desktop offers + Shell below (the shortcut), right and own pane", () => {
  const { paletteShellSpawn } = require("../static/palette.js");
  const asked = [];
  const cmds = paletteShellSpawn(false, true, (p) => asked.push(p));
  assert.deepEqual(cmds.map((c) => c.label), ["+ Shell below", "+ Shell right", "+ Shell in own pane"]);
  assert.deepEqual(cmds.map((c) => c.hint), ["Ctrl+Shift+`", "", ""]);
  assert.equal(paletteShellSpawn(true, true, () => {})[0].hint, "⌃⇧`");
  for (const c of cmds) { assert.equal(c.kind, "new"); c.run(); }
  assert.deepEqual(asked, ["below", "right", "own"]);
});

test("paletteShellSpawn: mobile keeps the one v0.2.0 entry", () => {
  const { paletteShellSpawn } = require("../static/palette.js");
  const asked = [];
  const cmds = paletteShellSpawn(false, false, (p) => asked.push(p));
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

test("paletteProfileSpawns: one entry per profile, no hint (bin is not shown), opens the dialog on it", () => {
  const { paletteProfileSpawns } = require("../static/palette.js");
  const opened = [];
  const cmds = paletteProfileSpawns([{ name: "host-zsh", bin: "/usr/bin/zsh" }, { name: "claude", bin: "claude" }], (n) => opened.push(n));
  assert.deepEqual(cmds.map((c) => [c.kind, c.label, c.hint]), [
    ["new", "New session · host-zsh", ""],
    ["new", "New session · claude", ""],
  ]);
  cmds.forEach((c) => c.run());
  assert.deepEqual(opened, ["host-zsh", "claude"]);
});

// S4: on desktop Settings, Analytics and Audit open as pages, and the palette
// reaches each Settings section directly. Mobile keeps exactly the two v0.4.0
// Settings entries and gets no page entries.
test("palettePageCommands: desktop lists Audit, Analytics and the six Settings sections", () => {
  const { palettePageCommands } = require("../static/palette.js");
  const ran = [];
  const h = { audit: () => ran.push("audit"), analytics: () => ran.push("analytics"), settings: (t) => ran.push("settings:" + t) };
  const cmds = palettePageCommands(true, { analytics: true, audit: true }, h);
  assert.deepEqual(cmds.map((c) => c.label), [
    "Audit", "Analytics",
    "Settings · Profiles", "Settings · Agent accounts", "Settings · Credentials",
    "Settings · API tokens", "Settings · Appearance", "Settings · About",
  ]);
  for (const c of cmds) assert.ok(c.kind === "settings" || c.kind === "view", c.label + " kind " + c.kind);
  cmds.forEach((c) => c.run());
  assert.deepEqual(ran, ["audit", "analytics",
    "settings:manage", "settings:accounts", "settings:creds", "settings:tokens", "settings:settings", "settings:about"]);
});

test("palettePageCommands: desktop without analytics drops only Analytics (7 entries)", () => {
  const { palettePageCommands } = require("../static/palette.js");
  const labels = palettePageCommands(true, { analytics: false, audit: true }, {}).map((c) => c.label);
  assert.equal(labels.length, 7);
  assert.ok(!labels.includes("Analytics"));
  assert.ok(labels.includes("Audit"));
});

test("palettePageCommands: desktop without the audit store offers no Audit (its link is hidden too)", () => {
  const { palettePageCommands } = require("../static/palette.js");
  const labels = palettePageCommands(true, { analytics: true, audit: false }, {}).map((c) => c.label);
  assert.ok(!labels.includes("Audit"));
  assert.equal(labels.length, 7);
});

test("palettePageCommands: mobile keeps exactly today's two Settings entries", () => {
  const { palettePageCommands } = require("../static/palette.js");
  const ran = [];
  const cmds = palettePageCommands(false, { analytics: true, audit: true }, { settings: (t) => ran.push(t) });
  assert.deepEqual(cmds.map((c) => [c.kind, c.label, c.hint]), [
    ["settings", "Settings · Profiles", ""],
    ["settings", "Settings · Font sizes", ""],
  ]);
  cmds.forEach((c) => c.run());
  assert.deepEqual(ran, ["manage", "settings"]);
});

// F21-B (#172): a finished turn is neutral in the command palette too. Desktop
// draws a session's dot with deskDot; the phone keeps activityClass(s.activity)
// byte-for-byte (its finished turn is still the amber "waiting" dot).
const palTurn = { id: "t", name: "turn", activity: "waiting", need: "turn", attention: "done", claude: true };
const palAsk = { id: "a", name: "ask", activity: "waiting", need: "ask", attention: "block", claude: true };

test("palSessionDot: on desktop a finished turn is the neutral idle dot titled your turn", () => {
  const d = palSessionDot(palTurn, false);
  assert.deepStrictEqual(d, { cls: activityClass("idle").cls, title: "your turn" });
  assert.strictEqual(d.cls.includes("waiting"), false);
  assert.deepStrictEqual(palSessionDot({ ...palTurn, attention: undefined }, false), d, "derived without the server field");
});

test("palSessionDot: on desktop a blocked session keeps the amber waiting dot", () => {
  assert.deepStrictEqual(palSessionDot(palAsk, false), activityClass("waiting"));
});

test("palSessionDot: the phone's dot is activityClass(s.activity), unchanged", () => {
  assert.deepStrictEqual(palSessionDot(palTurn, true), activityClass("waiting"));
  assert.deepStrictEqual(palSessionDot(palAsk, true), activityClass("waiting"));
  for (const a of ["working", "idle", "done", "crashed", "detached", "bogus", undefined]) {
    assert.deepStrictEqual(palSessionDot({ activity: a }, true), activityClass(a), String(a));
  }
});

test("paletteSessions draws its dot through palSessionDot", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "palette.js"), "utf8");
  assert.match(src, /const act = palSessionDot\(s, mobile\);/);
});
