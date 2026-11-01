// The shortcut registry (shortcuts.js) and the context object (shell.js) that
// decide whether a global shortcut may act. showModal() makes the page inert to
// POINTER input only: a keydown from a focused descendant of a dialog still
// reaches document in the capture phase, so the context must say so.
const test = require("node:test");
const assert = require("node:assert");
const { shortcutContext } = require("../static/shell.js");
const SC = require("../static/shortcuts.js");

// ---- shortcuts.js: the registry and its pure functions ----

const ev = (code, key, mods = {}) => ({
  code, key,
  ctrlKey: !!mods.ctrl, metaKey: !!mods.meta, altKey: !!mods.alt, shiftKey: !!mods.shift,
  isComposing: !!mods.composing,
  getModifierState: mods.altGr ? (m) => m === "AltGraph" : undefined,
  calls: [],
  preventDefault() { this.calls.push("pd"); },
  stopPropagation() { this.calls.push("sp"); },
});
const NONE = { dialogOpen: false, pageOpen: false, inTerminal: false, inEditor: false, inInput: false, mobile: false };
const ctxWith = (k) => Object.assign({}, NONE, { [k]: true });
const entry = (id) => SC.SHORTCUTS.find((x) => x.id === id);

test("isMacPlatform", () => {
  assert.strictEqual(SC.isMacPlatform({ platform: "MacIntel" }), true);
  assert.strictEqual(SC.isMacPlatform({ platform: "", userAgent: "Mozilla (iPhone; CPU)" }), true);
  assert.strictEqual(SC.isMacPlatform({ platform: "Win32" }), false);
  assert.strictEqual(SC.isMacPlatform({ platform: "Linux x86_64" }), false);
  assert.strictEqual(SC.isMacPlatform({}), false);
});

test("IS_MAC does not throw without navigator", () => {
  assert.strictEqual(typeof SC.IS_MAC, "boolean");
});

test("chordMatches: modifiers are exact and mod follows the platform", () => {
  const c = { mod: true, shift: true, code: "KeyK" };
  assert.strictEqual(SC.chordMatches(ev("KeyK", "K", { meta: true, shift: true }), c, true), true);
  assert.strictEqual(SC.chordMatches(ev("KeyK", "K", { ctrl: true, shift: true }), c, false), true);
  assert.strictEqual(SC.chordMatches(ev("KeyK", "K", { ctrl: true, shift: true }), c, true), false);
  assert.strictEqual(SC.chordMatches(ev("KeyK", "K", { meta: true, shift: true }), c, false), false);
  assert.strictEqual(SC.chordMatches(ev("KeyK", "K", { ctrl: true, shift: true, alt: true }), c, false), false);
  assert.strictEqual(SC.chordMatches(ev("KeyK", "k", { ctrl: true }), c, false), false);
});

test("chordMatches: AltGraph and IME composition are rejected", () => {
  const c = { mod: true, shift: true, code: "KeyK" };
  assert.strictEqual(SC.chordMatches(ev("KeyK", "K", { ctrl: true, shift: true, altGr: true }), c, false), false);
  assert.strictEqual(SC.chordMatches(ev("KeyK", "K", { ctrl: true, shift: true, composing: true }), c, false), false);
});

test("chordMatches: shiftAny, keys, codes and on", () => {
  const q = { shiftAny: true, keys: ["?"] };
  assert.strictEqual(SC.chordMatches(ev("Slash", "?", { shift: true }), q, false), true);
  assert.strictEqual(SC.chordMatches(ev("Slash", "?"), q, false), true);
  assert.strictEqual(SC.chordMatches(ev("Slash", "?", { ctrl: true }), q, false), false);
  const d = { mod: true, codes: "Digit1-9" };
  assert.strictEqual(SC.chordMatches(ev("Digit1", "1", { ctrl: true }), d, false), true);
  assert.strictEqual(SC.chordMatches(ev("Digit9", "9", { ctrl: true }), d, false), true);
  assert.strictEqual(SC.chordMatches(ev("Digit0", "0", { ctrl: true }), d, false), false);
  const m = { mod: true, code: "KeyK", on: "mac" };
  assert.strictEqual(SC.chordMatches(ev("KeyK", "k", { ctrl: true }), m, false), false);
  assert.strictEqual(SC.chordMatches(ev("KeyK", "k", { meta: true }), m, true), true);
});

test("renderChord", () => {
  const first = (id) => entry(id).chords[0];
  const legacy = (id, i = 0) => entry(id).legacy[i].chord;
  const both = (chord, mac, other) => {
    assert.strictEqual(SC.renderChord(chord, true), mac);
    assert.strictEqual(SC.renderChord(chord, false), other);
  };
  both(first("palette.open"), "⇧⌘K", "Ctrl+Shift+K");
  both(first("shell.below"), "⌃⇧`", "Ctrl+Shift+`");
  both(first("session.jump"), "⌘1…9", "Ctrl+1…9");
  both(legacy("files.toggle"), "⌥⌘F", "Ctrl+Alt+F");
  both(first("palette.insert"), "⌘↵", "Ctrl+↵");
  both(first("zoom.in"), "⌘=", "Ctrl+=");
  both(first("help.open"), "⌘/", "Ctrl+/");
  both(first("palette.open.alt"), "⌃⇧Space", "Ctrl+Shift+Space");
  both(entry("help.open").chords[1], "?", "?");
});

test("shortcutFor: ? is page-only", () => {
  const q = ev("Slash", "?", { shift: true });
  assert.strictEqual(SC.shortcutFor(q, false, NONE), "help.open");
  for (const k of ["inTerminal", "inEditor", "inInput", "dialogOpen"]) {
    assert.strictEqual(SC.shortcutFor(q, false, ctxWith(k)), null, k);
  }
});

test("shortcutFor: Ctrl+/ reaches terminal, editor and inputs but not a dialog", () => {
  const e = ev("Slash", "/", { ctrl: true });
  assert.strictEqual(SC.shortcutFor(e, false, NONE), "help.open");
  for (const k of ["inTerminal", "inEditor", "inInput"]) assert.strictEqual(SC.shortcutFor(e, false, ctxWith(k)), "help.open", k);
  assert.strictEqual(SC.shortcutFor(e, false, ctxWith("dialogOpen")), null);
});

test("shortcutFor: palette, files.goto, shell.below", () => {
  const k = ev("KeyK", "K", { ctrl: true, shift: true });
  assert.strictEqual(SC.shortcutFor(k, false, ctxWith("dialogOpen")), "palette.open");
  assert.strictEqual(SC.shortcutFor(k, false, ctxWith("inEditor")), "palette.open");
  assert.strictEqual(SC.shortcutFor(k, false, ctxWith("inInput")), null);
  const p = ev("KeyP", "p", { ctrl: true });
  assert.strictEqual(SC.shortcutFor(p, false, ctxWith("inTerminal")), null);
  assert.strictEqual(SC.shortcutFor(p, false, ctxWith("inEditor")), "files.goto");
  const b = ev("Backquote", "~", { ctrl: true, shift: true });
  assert.strictEqual(SC.shortcutFor(b, false, NONE), "shell.below");
  assert.strictEqual(SC.shortcutFor(b, false, ctxWith("pageOpen")), null);
});

// The palette opens on Ctrl+Shift+K off mac and Cmd+Shift+K on mac. Ctrl+K is
// readline's kill-line and Cmd+K stays only as a legacy mac alias. Ctrl+Shift+K
// is a magnet for other apps' system-wide hotkeys (Notion took it on a Windows
// machine), so Ctrl+Shift+Space is a second way in on every platform.
test("shortcutFor: the palette opens on its chords on both platforms", () => {
  const sp = ev("Space", " ", { ctrl: true, shift: true });
  assert.strictEqual(SC.shortcutFor(sp, false, NONE), "palette.open.alt");
  assert.strictEqual(SC.shortcutFor(sp, true, NONE), "palette.open.alt");
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true }), false, NONE), "palette.open");
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { meta: true, shift: true }), true, NONE), "palette.open");
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "k", { meta: true }), true, NONE), "palette.open");
});

test("shortcutFor: Ctrl+Shift+K is not the palette on mac, and plain keys never are", () => {
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true }), true, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "k", { ctrl: true }), true, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("Space", " "), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("Space", " ", { shift: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyJ", "J", { ctrl: true, shift: true }), false, NONE), null);
});

test("shortcutFor: Alt, AltGr or the other platform modifier disqualify a palette chord", () => {
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true, alt: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true, altGr: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true, meta: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("Space", " ", { ctrl: true, shift: true, alt: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "k", { meta: true, ctrl: true }), true, NONE), null);
});

test("shortcutFor: platform-specific and legacy chords", () => {
  const alt5 = ev("Digit5", "5", { alt: true });
  assert.strictEqual(SC.shortcutFor(alt5, true, NONE), null);
  assert.strictEqual(SC.shortcutFor(alt5, false, NONE), "session.jump");
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "k", { meta: true }), true, NONE), "palette.open");
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "k", { ctrl: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyB", "b", { meta: true }), true, NONE), "sidebar.toggle");
  assert.strictEqual(SC.shortcutFor(ev("KeyB", "b", { ctrl: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyS", "s", { ctrl: true, alt: true }), false, NONE), "sidebar.toggle");
  assert.strictEqual(SC.shortcutFor(ev("KeyS", "s", { ctrl: true, alt: true, altGr: true }), false, NONE), null);
});

test("shortcutFor: Ctrl+Shift+- stays free for readline undo", () => {
  assert.strictEqual(SC.shortcutFor(ev("Minus", "_", { ctrl: true, shift: true }), false, NONE), null);
});

// The R1 table, written out independently of the registry.
// Order: terminal, editor, input, dialog, page.
const R1 = {
  "palette.open": [1, 1, 0, 1, 1], "palette.open.alt": [1, 1, 0, 1, 1], "help.open": [1, 1, 1, 0, 1],
  "sidebar.toggle": [1, 0, 0, 0, 1], "files.toggle": [1, 0, 0, 0, 1], "session.jump": [1, 0, 0, 0, 1],
  "shell.below": [1, 0, 0, 0, 0], "files.goto": [0, 1, 1, 0, 0],
  "zoom.in": [1, 0, 0, 0, 0], "zoom.out": [1, 0, 0, 0, 0], "zoom.reset": [1, 0, 0, 0, 0],
};
const R1_FLAGS = ["inTerminal", "inEditor", "inInput", "dialogOpen", "pageOpen"];

function eventFromChord(c, mac) {
  const code = c.code ? c.code : c.codes ? "Digit1" : "KeyZ";
  const key = c.keys ? c.keys[0] : "x";
  return ev(code, key, { ctrl: !!c.ctrl || (!!c.mod && !mac), meta: !!c.mod && mac, alt: !!c.alt, shift: !!c.shift });
}

test("R1 table: every global bound shortcut admits exactly the contexts the table says", () => {
  const globals = SC.SHORTCUTS.filter((s) => s.scope === "global" && s.status === "bound");
  assert.deepStrictEqual(globals.map((s) => s.id).sort(), Object.keys(R1).sort());
  for (const mac of [true, false]) {
    for (const s of globals) {
      const e = eventFromChord(SC.activeChords(s, mac)[0], mac);
      assert.strictEqual(SC.shortcutFor(e, mac, NONE), s.id, `${s.id} none mac=${mac}`);
      R1_FLAGS.forEach((flag, i) => {
        assert.strictEqual(SC.shortcutFor(e, mac, ctxWith(flag)), R1[s.id][i] ? s.id : null, `${s.id} ${flag} mac=${mac}`);
      });
    }
  }
});

// Terminal invariant: a chord that can reach the terminal must not be a byte xterm sends.
const TERMINAL_OK = new Set([
  "session.jump", // Ctrl+3..8 send Esc ^\ ^] ^^ ^_ DEL: accepted; Ctrl+1/2/9/0 send nothing
  "zoom.in", "zoom.out", "zoom.reset", // Ctrl+= - 0 send nothing in xterm
  "help.open", // Ctrl+/ sends nothing in the vendored xterm (upstream maps 0x1F: upgrade hazard)
  "term.paste", // Ctrl+V is the browser paste: documented trade-off
]);
const LEGACY_TERMINAL_OK = new Set(["session.jump", "sidebar.toggle", "files.toggle"]);

test("terminal invariant", () => {
  for (const s of SC.SHORTCUTS) {
    if (s.status !== "bound") continue;
    const reach = s.scope === "terminal" || (s.allow && s.allow.terminal);
    if (!reach) continue;
    for (const mac of [true, false]) {
      const prim = s.chords.filter((c) => !c.on || (c.on === "mac") === mac);
      const leg = (s.legacy || []).map((l) => l.chord).filter((c) => !c.on || (c.on === "mac") === mac);
      for (const [list, okSet] of [[prim, TERMINAL_OK], [leg, LEGACY_TERMINAL_OK]]) {
        for (const c of list) {
          if (c.allow && c.allow.terminal === false) continue;
          const label = `${s.id} ${SC.renderChord(c, mac)} mac=${mac}`;
          if (mac) {
            const altOnly = c.alt && !c.mod && !c.ctrl;
            assert.ok(!altOnly, `${label} is Alt-only on mac`);
          } else {
            assert.ok(c.shift || okSet.has(s.id), `${label} has no Shift and is not whitelisted`);
          }
        }
      }
    }
  }
});

test("every legacy chord is removed in v0.17.0", () => {
  for (const s of SC.SHORTCUTS) for (const l of s.legacy || []) assert.strictEqual(l.removeIn, "v0.17.0", s.id);
});

test("no two ids render the same chord in overlapping scopes", () => {
  for (const mac of [true, false]) {
    const seen = [];
    for (const s of SC.SHORTCUTS) {
      if (s.status !== "bound") continue;
      for (const c of SC.activeChords(s, mac)) seen.push({ id: s.id, scope: s.scope, text: SC.renderChord(c, mac) });
    }
    for (let i = 0; i < seen.length; i++) {
      for (let j = i + 1; j < seen.length; j++) {
        const a = seen[i], b = seen[j];
        if (a.id === b.id || a.text !== b.text) continue;
        const overlap = a.scope === b.scope || a.scope === "global" || b.scope === "global";
        assert.ok(!overlap, `${a.id} and ${b.id} both render ${a.text} (mac=${mac})`);
      }
    }
  }
});

test("reserved entries render but never dispatch", () => {
  assert.strictEqual(SC.renderChord(entry("viewer.find").chords[0], true), "⌘F");
  assert.strictEqual(SC.renderChord(entry("viewer.find").chords[0], false), "Ctrl+F");
  assert.strictEqual(SC.renderChord(entry("viewer.gotoLine").chords[0], true), "⌃G");
  assert.strictEqual(SC.renderChord(entry("viewer.gotoLine").chords[0], false), "Ctrl+G");
  assert.strictEqual(SC.shortcutFor(ev("KeyF", "f", { ctrl: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyG", "g", { ctrl: true }), true, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyF", "f", { meta: true }), true, NONE), null);
});

test("shortcutHint, jumpHint, shortcutMatches", () => {
  assert.strictEqual(SC.shortcutHint("palette.open", true), "⇧⌘K");
  assert.strictEqual(SC.shortcutHint("term.paste", false), "Ctrl+V");
  assert.strictEqual(SC.shortcutHint("nope", false), "");
  assert.strictEqual(SC.jumpHint(3, true), "⌘3");
  assert.strictEqual(SC.jumpHint(3, false), "Ctrl+3");
  assert.strictEqual(SC.shortcutMatches("editor.save", ev("KeyS", "s", { meta: true }), true), true);
  assert.strictEqual(SC.shortcutMatches("editor.save", ev("KeyS", "s", { meta: true }), false), false);
  assert.strictEqual(SC.shortcutMatches("nope", ev("KeyS", "s", { meta: true }), true), false);
});

test("helpRows", () => {
  const OLD = "Old keys (removed in v0.17.0)";
  for (const mac of [true, false]) {
    const rows = SC.helpRows(mac);
    const labels = rows.map((r) => r.label);
    for (const s of SC.SHORTCUTS) {
      if (s.status === "reserved") assert.ok(!labels.includes(s.label), `reserved ${s.id} listed`);
      else assert.ok(rows.some((r) => r.label === s.label && r.group !== OLD), `${s.id} missing`);
      for (const l of s.legacy || []) {
        if (l.chord.on && (l.chord.on === "mac") !== mac) continue;
        assert.ok(rows.some((r) => r.group === OLD && r.keys === SC.renderChord(l.chord, mac)), `${s.id} legacy missing`);
      }
    }
    for (const r of rows) {
      if (mac) assert.ok(!r.keys.includes("Ctrl+"), r.keys);
      else assert.ok(!r.keys.includes("⌘"), r.keys);
    }
  }
  const off = SC.helpRows(false);
  assert.strictEqual(off.find((r) => r.label === "Paste").keys, "Ctrl+V / Ctrl+Shift+V");
  assert.strictEqual(off.find((r) => r.label === "Select text when the agent holds the mouse").keys, "Shift+drag");
  assert.strictEqual(SC.helpRows(true).find((r) => r.label === "Move and pick in lists").keys, "↑ ↓ ↵");
});

test("helpText", () => {
  const rows = SC.helpRows(false);
  const text = SC.helpText(rows, "sysbox");
  const groups = ["General", "Sessions & panes", "Terminal", "Editor & files", "Old keys (removed in v0.17.0)"];
  const lines = text.split("\n");
  for (const g of groups) assert.ok(lines.includes(g), g);
  assert.ok(text.endsWith("\n\nRuntime: sysbox"));
  const body = lines.slice(0, lines.lastIndexOf("Runtime: sysbox") - 1).filter((l) => l && !groups.includes(l));
  const w = Math.max(...rows.map((r) => r.keys.length));
  assert.strictEqual(body.length, rows.length);
  for (const r of rows) assert.ok(body.includes(r.keys.padEnd(w) + "  " + r.label), r.label);
  assert.ok(text.includes("\n\nSessions & panes\n"));
  assert.ok(!SC.helpText(rows).includes("Runtime:"));
});

test("helpText emits the groups in a fixed order, each row under its own heading", () => {
  const order = ["General", "Sessions & panes", "Terminal", "Editor & files", "Old keys (removed in v0.17.0)"];
  for (const mac of [true, false]) {
    const rows = SC.helpRows(mac);
    // Reversed so the order cannot come from the registry's row order.
    const lines = SC.helpText(rows.slice().reverse()).split("\n");
    const at = order.map((g) => lines.indexOf(g));
    for (const i of at) assert.ok(i >= 0);
    for (let i = 1; i < at.length; i++) assert.ok(at[i - 1] < at[i], order[i] + " out of order");
    const w = Math.max(...rows.map((r) => r.keys.length));
    for (const r of rows) {
      const gi = order.indexOf(r.group);
      const li = lines.indexOf(r.keys.padEnd(w) + "  " + r.label);
      assert.ok(li > at[gi] && (gi === order.length - 1 || li < at[gi + 1]), r.label + " not under " + r.group);
    }
  }
});

test("dispatchShortcut", () => {
  SC._resetShortcutBindings();
  const ran = [];
  SC.bindShortcut("shell.below", (e, ctx) => { ran.push(ctx); });
  const e = ev("Backquote", "~", { ctrl: true, shift: true });
  assert.strictEqual(SC.dispatchShortcut(e, NONE, false), true);
  assert.deepStrictEqual(e.calls, ["pd", "sp"]);
  assert.strictEqual(ran.length, 1);

  SC._resetShortcutBindings();
  SC.bindShortcut("shell.below", () => false);
  const e2 = ev("Backquote", "~", { ctrl: true, shift: true });
  assert.strictEqual(SC.dispatchShortcut(e2, NONE, false), false);
  assert.deepStrictEqual(e2.calls, []);

  SC._resetShortcutBindings();
  const e3 = ev("Backquote", "~", { ctrl: true, shift: true });
  assert.strictEqual(SC.dispatchShortcut(e3, NONE, false), false);
  assert.deepStrictEqual(e3.calls, []);

  SC.bindShortcut("shell.below", () => {});
  const e4 = ev("KeyQ", "q", { ctrl: true });
  assert.strictEqual(SC.dispatchShortcut(e4, NONE, false), false);
  assert.deepStrictEqual(e4.calls, []);
  SC._resetShortcutBindings();
});

test("cmKeys", () => {
  assert.deepStrictEqual(SC.cmKeys("editor.save", true), ["Cmd-S"]);
  assert.deepStrictEqual(SC.cmKeys("editor.save", false), ["Ctrl-S"]);
});

// shortcutContext: the one place that decides where focus is, for every shortcut.
const focusEl = (tagName, o = {}) => ({
  tagName,
  classList: { contains: (c) => c === o.cls },
  closest: (sel) => (sel === ".CodeMirror" && o.inCM ? {} : null),
  isContentEditable: !!o.editable,
});
const FOCUS_KEYS = ["inTerminal", "inEditor", "inInput"];
const onlyFocus = (ctx, key) => FOCUS_KEYS.every((k) => ctx[k] === (k === key));

test("ctx: xterm textarea is inTerminal only", () => {
  const ctx = shortcutContext(focusEl("TEXTAREA", { cls: "xterm-helper-textarea" }), 0, false, false);
  assert.ok(onlyFocus(ctx, "inTerminal"));
});

test("ctx: CodeMirror's textarea is inEditor only, not inInput", () => {
  const ctx = shortcutContext(focusEl("TEXTAREA", { inCM: true }), 0, false, false);
  assert.ok(onlyFocus(ctx, "inEditor"));
});

test("ctx: INPUT, SELECT, plain TEXTAREA, contenteditable DIV are inInput", () => {
  for (const e of [focusEl("INPUT"), focusEl("SELECT"), focusEl("TEXTAREA"), focusEl("DIV", { editable: true })]) {
    assert.ok(onlyFocus(shortcutContext(e, 0, false, false), "inInput"), e.tagName);
  }
});

test("ctx: a button or body is no focus kind; null is all false", () => {
  for (const e of [focusEl("BUTTON"), focusEl("BODY"), null, undefined]) {
    const ctx = shortcutContext(e, 0, false, false);
    assert.ok(FOCUS_KEYS.every((k) => ctx[k] === false));
  }
});

test("ctx: element without closest/classList does not throw", () => {
  const ctx = shortcutContext({ tagName: "INPUT" }, 0, false, false);
  assert.ok(onlyFocus(ctx, "inInput"));
});

test("ctx: dialogOpen follows the count; undefined is 0", () => {
  assert.strictEqual(shortcutContext(null, 2, false, false).dialogOpen, true);
  assert.strictEqual(shortcutContext(null, 1, false, false).dialogOpen, true);
  assert.strictEqual(shortcutContext(null, 0, false, false).dialogOpen, false);
  assert.strictEqual(shortcutContext(null, undefined, false, false).dialogOpen, false);
});

test("ctx: pageOpen and mobile pass through as strict booleans", () => {
  const a = shortcutContext(null, 0, true, true);
  assert.strictEqual(a.pageOpen, true);
  assert.strictEqual(a.mobile, true);
  const b = shortcutContext(null, 0, undefined, 0);
  assert.strictEqual(b.pageOpen, false);
  assert.strictEqual(b.mobile, false);
  const c = shortcutContext(null, 0, "x", 1);
  assert.strictEqual(c.pageOpen, true);
  assert.strictEqual(c.mobile, true);
});

test("ctx: every field is a strict boolean", () => {
  const ctx = shortcutContext(focusEl("TEXTAREA", { inCM: true }), 3, 1, "y");
  assert.deepStrictEqual(Object.keys(ctx).sort(), ["dialogOpen", "inEditor", "inInput", "inTerminal", "mobile", "pageOpen"]);
  for (const v of Object.values(ctx)) assert.strictEqual(typeof v, "boolean");
});
