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

test("paletteShellCommands: desktop offers shortcuts, files dock and the Activity and Access views once", () => {
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
  assert.equal(cmds.find((c) => c.id === "files-drawer").label, "Toggle files dock", "the approved spec's name for the feature (box ruling R18)");
});

test("paletteShellCommands: hints are the registry's, per platform", () => {
  const { paletteShellCommands } = require("../static/palette.js");
  const hint = (mac, desktop, id) => paletteShellCommands(mac, desktop, {}).find((c) => c.id === id).hint;
  assert.equal(hint(true, true, "files-drawer"), "⇧⌘E");
  assert.equal(hint(false, false, "toggle-files"), "Ctrl+Shift+E");
  assert.equal(hint(true, true, "shortcuts"), "⌘/");
  assert.equal(hint(false, true, "shortcuts"), "Ctrl+/");
});

// F69 (#172): Next that needs you is a sidebar command, so the palette lists it on
// desktop only, with the chord as its hint; the phone's palette is unchanged.
test("paletteShellCommands: desktop lists Next that needs you with its chord, and runs the callback", () => {
  const { paletteShellCommands } = require("../static/palette.js");
  const ran = [];
  const cmds = paletteShellCommands(false, true, { nextNeeds: () => ran.push("next") });
  const c = cmds.find((x) => x.label === "Next that needs you");
  assert.ok(c, "listed");
  assert.equal(c.kind, "view");
  assert.equal(c.hint, "Ctrl+Shift+J");
  c.run();
  assert.deepEqual(ran, ["next"]);
  assert.equal(paletteShellCommands(true, true, {}).find((x) => x.label === "Next that needs you").hint, "\u21e7\u2318J");
  assert.equal(cmds.filter((x) => x.label === "Next that needs you").length, 1);
});

test("paletteShellCommands: mobile keeps the old entries and gets none of the new", () => {
  const { paletteShellCommands } = require("../static/palette.js");
  const ids = paletteShellCommands(false, false, {}).map((c) => c.id);
  assert.deepEqual(ids.sort(), ["steer-rail", "toggle-files"]);
  assert.ok(!paletteShellCommands(false, false, {}).some((c) => c.label === "Next that needs you"), "no sidebar groups on a phone");
});

test("paletteCommands offers every desktop shell command it builds (Next that needs you is wired in, not just defined)", () => {
  const src = require("fs").readFileSync(require("path").join(__dirname, "../static/palette.js"), "utf8");
  const i = src.indexOf("function paletteCommands(");
  const body = src.slice(i, src.indexOf("\n}\n", i));
  assert.match(body, /"next-needs"/);
  assert.match(body, /nextNeeds: /);
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
    "Settings · Device tokens", "Settings · Appearance", "Settings · About",
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

test("palettePageCommands: with vm_folders on, VM folders follows Credentials (desktop only)", () => {
  const { palettePageCommands } = require("../static/palette.js");
  const ran = [];
  const h = { settings: (t) => ran.push(t) };
  const cmds = palettePageCommands(true, { vm_folders: true }, h);
  const labels = cmds.map((c) => c.label);
  assert.equal(labels[labels.indexOf("Settings · Credentials") + 1], "Settings · VM folders");
  cmds[labels.indexOf("Settings · VM folders")].run();
  assert.deepEqual(ran, ["vmfolders"]);
  assert.ok(!palettePageCommands(true, {}, h).some((c) => /VM folders/.test(c.label)), "off by default");
  assert.ok(!palettePageCommands(false, { vm_folders: true }, h).some((c) => /VM folders/.test(c.label)), "no phone entry");
});

// --- Insert path at prompt: the palette keeps the FOCUSED terminal, and asks insertDecision about it -------------------------
// The palette's Ctrl/Cmd+Enter on a file row goes through filetree.js insertPathAtPrompt with the terminal that has the
// keyboard, whatever the Files dock shows (the P1 attempt to retarget the palette to the dock's session typed into terminals
// the user could not see; #337). Run for real in a vm: the real palette key handler, the real insertPathAtPrompt,
// insertTextAtPrompt and shellQuote. insertDecision is a stub that records what it was asked: its own states are
// insert-decision.test.js's, and insert-callers.test.js runs the callers against the real thing.
function insertPage(over) {
  const fs = require("node:fs");
  const path = require("node:path");
  const vm = require("node:vm");
  const STATIC = path.join(__dirname, "..", "static");
  const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
  const fnSource = (file, name) => {
    const text = src(file);
    const m = new RegExp("^function " + name + "\\(", "m").exec(text);
    assert.ok(m, name + " is a top-level function of " + file);
    return text.slice(m.index, text.indexOf("\n}\n", m.index) + 2);
  };
  const st = { toasts: [], refocus: 0, grabbed: [], asked: [], sent: { a: [], b: [] }, handlers: {}, verdict: { ok: true, why: "" } };
  const mk = (id) => ({ id, kind: "terminal", sendText(s) { st.sent[id].push(s); return true; }, grabFocus() { st.grabbed.push(id); } });
  const tabs = { a: mk("a"), b: mk("b") };
  const fakeEl = (id) => ({ id, open: true, value: "", close() { this.open = false; }, showModal() {}, focus() {}, querySelector: () => null,
    addEventListener(ev, fn) { (st.handlers[id + ":" + ev] = st.handlers[id + ":" + ev] || []).push(fn); } });
  const els = { palette: fakeEl("palette"), "pal-input": fakeEl("pal-input") };
  const ctx = vm.createContext(Object.assign({
    console, RegExp, Object, Array, Map, Math, Number, JSON, String, Error, Set,
    navigator: { platform: "Linux x86_64", userAgent: "" },
    document: { getElementById: (id) => els[id] || null },
    UNSAFE_TEXT: /[\u0000-\u001f\u007f]/,
    showToast: (m, opt) => st.toasts.push([m, opt && opt.error === true]),
    focusTerminal: () => { st.refocus++; },
    focusedTerminal: () => tabs.b, // B has the keyboard
    // The Files dock is open and pinned to session A: none of it changes where the palette inserts.
    drawerOwnsFileClicks: () => true,
    drawerState: { open: "files", sessionId: "a", pinned: true },
    terminalTabFor: (id) => tabs[id] || null,
    insertDecision: (t) => { st.asked.push(t.id); return st.verdict; },
    palRefresh: () => {}, palSelect: () => {}, runItem: () => {},
  }, over));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(src("shortcuts.js"), "shortcuts.js");
  load(fnSource("upload.js", "shellQuote"), "shellQuote");
  load(fnSource("upload.js", "insertTextAtPrompt"), "insertTextAtPrompt");
  load(fnSource("upload.js", "insertFailText"), "insertFailText"); // the toast words insertPathAtPrompt takes from it
  load(fnSource("insertdecision.js", "insertRefused"), "insertRefused"); // the real helper over the stubbed decision
  load(fnSource("filetree.js", "insertPathAtPrompt"), "insertPathAtPrompt");
  load(src("palette.js"), "palette.js");
  load("initPalette()", "init");
  const keydown = st.handlers["pal-input:keydown"][0];
  return {
    st, tabs, els,
    palette: (p) => {
      load("palItems = [{ kind: 'file', path: " + JSON.stringify(p) + " }]; palSel = 0;", "palItems");
      let prevented = false;
      keydown({ key: "Enter", ctrlKey: true, preventDefault() { prevented = true; } });
      return prevented;
    },
  };
}

test("the palette's Ctrl+Enter types at the focused terminal while the Files dock is pinned to another session", () => {
  const p = insertPage();
  assert.equal(p.palette("/w/a b.go"), true, "the key was taken");
  assert.deepEqual(p.st.sent.b, ["'/w/a b.go' "], "B has the keyboard");
  assert.deepEqual(p.st.sent.a, [], "the dock's session gets nothing");
  assert.deepEqual(p.st.asked, ["b"], "insertDecision was asked about the focused terminal, not the dock's");
  assert.equal(p.st.refocus, 1, "the caret returns to the focused terminal");
  assert.deepEqual(p.st.grabbed, []);
  assert.deepEqual(p.st.toasts, []);
  assert.equal(p.els.palette.open, false, "the palette closed first");
});

test("the palette refuses when insertDecision does: a toast with its sentence, nothing typed, no focus moved", () => {
  const p = insertPage();
  p.st.verdict = { ok: false, why: "beta is waiting on you; answer it first" };
  p.palette("/w/x.go");
  assert.deepEqual(p.st.toasts, [["beta is waiting on you; answer it first", true]]);
  assert.deepEqual(p.st.sent, { a: [], b: [] });
  assert.equal(p.st.refocus, 0);
  assert.deepEqual(p.st.grabbed, []);
});

test("insert path keeps its refusals: no terminal focused, control characters in the path, a session that is not connected", () => {
  const none = insertPage({ focusedTerminal: () => null });
  none.palette("/w/x.go");
  assert.match(none.st.toasts[0][0], /Focus a terminal first/);
  assert.deepEqual(none.st.asked, [], "no terminal: nothing to ask about");
  const ctl = insertPage();
  ctl.palette("/w/x\ny.go");
  assert.match(ctl.st.toasts[0][0], /control characters/);
  assert.deepEqual(ctl.st.sent.b, []);
  const down = insertPage();
  down.tabs.b.sendText = () => false;
  down.palette("/w/x.go");
  assert.match(down.st.toasts[0][0], /not connected/);
  assert.equal(down.st.refocus, 0);
});

// --- #168 S2 Task 2.3: a result-type glyph on every row, and the declared agentKind on a session item -----------------------
// palette.js run whole in a vm over a stand-in DOM, with the page's icon scripts (icons.js, iconmap.js, svgicon.js), so the
// rows are built by the real palBuildRow and paletteSessions.
function paletteVm(over) {
  const fs = require("node:fs");
  const path = require("node:path");
  const vm = require("node:vm");
  const { createRequire } = require("node:module");
  const STATIC = path.join(__dirname, "..", "static");
  const el = (tag, ns) => ({
    tag, namespaceURI: ns || "", className: "", title: "", onclick: null, onmouseenter: null, children: [], attrs: {}, _t: "",
    // As in a DOM: setting the text replaces the children.
    get textContent() { return this._t; },
    set textContent(v) { this._t = String(v); this.children = []; },
    classList: { add() {}, remove() {}, toggle() {} },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    removeAttribute(k) { delete this.attrs[k]; },
    append(...n) { this.children.push(...n); },
    prepend(...n) { this.children.unshift(...n); },
  });
  const ctx = vm.createContext(Object.assign({
    console, RegExp, Object, Array, Map, Math, Number, JSON, String, Error, Set,
    require: createRequire(path.join(STATIC, "palette.js")),
    document: { createElement: (t) => el(t), createElementNS: (ns, t) => el(t, ns) },
    window: { innerWidth: 1440 },
    mobileActive: (w) => Number(w) <= 700,
    deskWsCollapsedSet: () => new Set(),
    lastSessions: [], profiles: [],
    isNestedShell: () => false, revealShell() {}, openTerminal() {},
  }, over));
  // `const`/`let` at line start becomes `var`, so the names are properties of the context.
  for (const f of ["icons.js", "iconmap.js", "svgicon.js", "shortcuts.js", "deskshell.js", "palette.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8").replace(/^(const|let) /gm, "var "), { filename: f }).runInContext(ctx);
  }
  return ctx;
}
const shapeOf = (n) => (n.tag === "svg" ? "svg:" + n.attrs["data-icon"] : n.tag + "." + n.className + (n.textContent ? "'" + n.textContent + "'" : ""));

test("session items declare agentKind: claude, shell, or empty (from deskRowKind, never from a title)", () => {
  const ctx = paletteVm({
    lastSessions: [
      { id: "a", name: "audit-asks", claude: true, agent: "claude" },
      { id: "b", name: "audit-shell", claude: false, agent: "bash" },
      { id: "c", name: "mystery", claude: false, agent: "nope" },
      // A title that says "shell" or "claude" declares nothing: the kind is the profile's and the view's.
      { id: "d", name: "my claude shell", claude: false, agent: "nope" },
    ],
    profiles: [{ name: "claude", bin: "claude", claude: true }, { name: "bash", bin: "/bin/bash" }],
  });
  const items = ctx.paletteSessions(false);
  assert.deepStrictEqual(Array.from(items, (i) => i.agentKind).sort(), ["", "", "claude", "shell"]);
  const by = (label) => items.find((i) => i.label === label).agentKind;
  assert.strictEqual(by("audit-asks"), "claude");
  assert.strictEqual(by("audit-shell"), "shell");
  assert.strictEqual(by("mystery"), "");
  assert.strictEqual(by("my claude shell"), "");
});

test("palette row glyph by kind: one glyph per kind, never per command", () => {
  const ctx = paletteVm();
  const { ICON_PALETTE_KIND, ICON_LOCKED } = ctx;
  assert.deepStrictEqual({ ...ICON_PALETTE_KIND }, {
    new: "plus", "session-action": "arrow-right", view: "arrow-right", kill: "power", layout: "layout-grid", settings: "settings", file: "file",
  });
  assert.strictEqual(ICON_PALETTE_KIND.layout, ctx.ICON_LAYOUT[4], "one concept, one glyph: the layout picker's");
  // The type icon of a built row: the svg beside the label (null when the row draws none).
  const typeIcon = (it) => {
    const li = ctx.palBuildRow(it, 0, false);
    const svg = li.children.find((c) => c.tag === "svg");
    return { svg, li, name: svg ? svg.attrs["data-icon"] : null };
  };
  for (const [kind, glyph] of Object.entries(ICON_PALETTE_KIND)) {
    const r = typeIcon({ kind, label: "x " + kind, hint: "" });
    assert.strictEqual(r.name, glyph, kind);
    assert.strictEqual(r.svg.attrs["aria-hidden"], "true", kind + " is decoration");
    assert.match(r.svg.attrs.class, /\bpal-ti\b/, kind);
    assert.strictEqual(r.svg.attrs.width, "14", kind + " is the row size");
  }
  // New session · x and + Shell below are one kind, so one glyph; a view is arrow-right, never the nav glyph of its page.
  assert.strictEqual(typeIcon({ kind: "new", label: "New session · x", hint: "" }).name, "plus");
  assert.strictEqual(typeIcon({ kind: "new", label: "+ Shell below", hint: "" }).name, "plus");
  assert.strictEqual(typeIcon({ kind: "view", label: "Audit", hint: "" }).name, "arrow-right");
  assert.notStrictEqual(typeIcon({ kind: "view", label: "Audit", hint: "" }).name, "scroll-text");
  // A session row keeps its activity dot in the glyph column and adds its agent kind beside it.
  const claude = typeIcon({ kind: "session", label: "a", agentKind: "claude", dotCls: "dot working" });
  assert.strictEqual(claude.name, ICON_LOCKED["agent-claude"]);
  assert.strictEqual(claude.li.children[0].children[0].className, "dot working", "the dot stays");
  assert.strictEqual(typeIcon({ kind: "session", label: "s", agentKind: "shell", dotCls: "dot idle" }).name, ICON_LOCKED.shell);
  assert.strictEqual(typeIcon({ kind: "session", label: "u", agentKind: "", dotCls: "dot idle" }).svg, undefined, "unknown kind: dot only");
  assert.deepStrictEqual(typeIcon({ kind: "session", label: "u", agentKind: "", dotCls: "dot idle" }).li.children.map(shapeOf), ["span.pal-glyph", "span.pal-ti-gap", "span.pal-label'u'"], "a blank of the icon's width keeps the label aligned");
  // The icon sits between the glyph column and the label.
  assert.deepStrictEqual(typeIcon({ kind: "file", label: "a.go", hint: "" }).li.children.slice(0, 3).map(shapeOf), ["span.pal-glyph", "svg:file", "span.pal-label'a.go'"]);
});

test("palette row: the + prefix of a shell spawn was the plus glyph's text stand-in, so the row drops it; the item keeps its label", () => {
  const ctx = paletteVm();
  const it = { kind: "new", label: "+ Shell below", hint: "" };
  const li = ctx.palBuildRow(it, 0, false);
  assert.strictEqual(li.children.find((c) => c.className === "pal-label").textContent, "Shell below");
  assert.strictEqual(it.label, "+ Shell below", "filter text and the toast name keep the declared label");
});

test("palette copy-path button: the clipboard-copy glyph, its aria-label unchanged", () => {
  const ctx = paletteVm();
  const li = ctx.palBuildRow({ kind: "file", label: "a.go", path: "/x/a.go", hint: "" }, 0, true);
  const cp = li.children.find((c) => c.className === "pal-copy");
  assert.strictEqual(cp.attrs["aria-label"], "Copy path");
  assert.strictEqual(cp.title, "Copy path");
  assert.strictEqual(cp.textContent, "");
  assert.deepStrictEqual(cp.children.map(shapeOf), ["svg:clipboard-copy"]);
});

test("palette on the phone: the text glyphs and the ⧉ stay, no icon is drawn, the label is as declared (#340 frozen)", () => {
  const ctx = paletteVm({ window: { innerWidth: 390 } });
  const file = ctx.palBuildRow({ kind: "file", label: "a.go", path: "/x/a.go", hint: "" }, 0, true);
  assert.deepStrictEqual(file.children.map(shapeOf).slice(0, 2), ["span.pal-glyph'▸'", "span.pal-label'a.go'"]);
  assert.strictEqual(file.children.find((c) => c.className === "pal-copy").textContent, "⧉");
  const cmd = ctx.palBuildRow({ kind: "new", label: "+ Shell below", hint: "" }, 0, false);
  assert.deepStrictEqual(cmd.children.map(shapeOf), ["span.pal-glyph'>'", "span.pal-label'+ Shell below'"]);
  const ses = ctx.palBuildRow({ kind: "session", label: "a", agentKind: "claude", dotCls: "dot idle" }, 0, false);
  assert.ok(!ses.children.some((c) => c.tag === "svg"), "no kind glyph on a phone session row");
});

test("palette No matches: a 14 px muted search-x before the words on the desktop, none on the phone (G4 78, #340)", () => {
  const ctx = paletteVm();
  const empty = ctx.document.createElement("div");
  empty.textContent = "No matches";
  ctx.palPaintEmpty(empty, true, false);
  assert.strictEqual(empty.hidden, false);
  assert.deepStrictEqual(empty.children.map(shapeOf), ["svg:search-x"]);
  assert.strictEqual(empty.children[0].attrs.width, "14");
  assert.match(empty.children[0].attrs.class, /\bempty-glyph\b/);
  assert.strictEqual(empty.children[0].attrs["aria-hidden"], "true");
  ctx.palPaintEmpty(empty, true, false);
  assert.strictEqual(empty.children.length, 1, "a repaint does not stack a second glyph");
  ctx.palPaintEmpty(empty, false, false);
  assert.strictEqual(empty.hidden, true);
  const phone = ctx.document.createElement("div");
  phone.textContent = "No matches";
  ctx.palPaintEmpty(phone, true, true);
  assert.strictEqual(phone.children.length, 0);
  assert.strictEqual(phone.textContent, "No matches");
  ctx.palPaintEmpty(empty, true, true);
  assert.strictEqual(empty.children.length, 0, "a window that became a phone drops the glyph");
});
