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
  const both = (chord, mac, other) => {
    assert.strictEqual(SC.renderChord(chord, true), mac);
    assert.strictEqual(SC.renderChord(chord, false), other);
  };
  both(first("palette.open"), "⇧⌘K", "Ctrl+Shift+K");
  both(first("shell.below"), "⌃⇧`", "Ctrl+Shift+`");
  both(first("session.jump"), "⌘1…9", "Ctrl+1…9");
  both(first("files.toggle"), "⇧⌘E", "Ctrl+Shift+E");
  both(first("session.nextNeeds"), "⇧⌘J", "Ctrl+Shift+J");
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

// QWERTZ (German) layouts put the main-row "-" on the physical Slash key:
// e.code is "Slash" and e.key is "-". help.open matches by code, zoom.out by key,
// and shortcutFor returns the first match, so zoom.* must precede help.open in
// SHORTCUTS or Ctrl+- opens help instead of zooming out. Help stays reachable
// on QWERTZ through Ctrl+Shift+7 (key "/") and "?".
test("shortcutFor: on QWERTZ the Slash key's \"-\" zooms out, it does not open help", () => {
  assert.strictEqual(SC.shortcutFor(ev("Slash", "-", { ctrl: true }), false, NONE), "zoom.out");
  assert.strictEqual(SC.shortcutFor(ev("Slash", "-", { meta: true }), true, NONE), "zoom.out");
  assert.strictEqual(SC.shortcutFor(ev("Digit7", "/", { ctrl: true, shift: true }), false, NONE), "help.open");
});

test("SHORTCUTS: every zoom.* entry precedes help.open (QWERTZ code/key overlap)", () => {
  const at = (id) => SC.SHORTCUTS.findIndex((s) => s.id === id);
  const help = at("help.open");
  assert.ok(help >= 0);
  for (const id of ["zoom.in", "zoom.out", "zoom.reset"]) assert.ok(at(id) >= 0 && at(id) < help, id + " must come before help.open");
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
// readline's kill-line, so it is not a palette chord. Ctrl+Shift+K
// is a magnet for other apps' system-wide hotkeys (Notion took it on a Windows
// machine), so Ctrl+Shift+Space is a second way in on every platform.
test("shortcutFor: the palette opens on its chords on both platforms", () => {
  const sp = ev("Space", " ", { ctrl: true, shift: true });
  assert.strictEqual(SC.shortcutFor(sp, false, NONE), "palette.open.alt");
  assert.strictEqual(SC.shortcutFor(sp, true, NONE), "palette.open.alt");
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true }), false, NONE), "palette.open");
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { meta: true, shift: true }), true, NONE), "palette.open");
});

test("shortcutFor: Ctrl+Shift+K is not the palette on mac, and plain keys never are", () => {
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true }), true, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "k", { ctrl: true }), true, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("Space", " "), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("Space", " ", { shift: true }), false, NONE), null);
  // Ctrl+Shift+J was once the palette's chord; it is Next that needs you now (F69), never the palette.
  assert.strictEqual(SC.shortcutFor(ev("KeyJ", "J", { ctrl: true, shift: true }), false, NONE), "session.nextNeeds");
});

test("shortcutFor: Alt, AltGr or the other platform modifier disqualify a palette chord", () => {
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true, alt: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true, altGr: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "K", { ctrl: true, shift: true, meta: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("Space", " ", { ctrl: true, shift: true, alt: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "k", { meta: true, ctrl: true }), true, NONE), null);
});

test("shortcutFor: platform-specific chords", () => {
  assert.strictEqual(SC.shortcutFor(ev("KeyK", "k", { ctrl: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyB", "b", { ctrl: true }), false, NONE), null);
  assert.strictEqual(SC.shortcutFor(ev("KeyB", "B", { meta: true, shift: true }), true, NONE), "sidebar.toggle");
  assert.strictEqual(SC.shortcutFor(ev("KeyB", "B", { ctrl: true, shift: true }), false, NONE), "sidebar.toggle");
});

test("shortcutFor: Ctrl+Shift+- stays free for readline undo", () => {
  assert.strictEqual(SC.shortcutFor(ev("Minus", "_", { ctrl: true, shift: true }), false, NONE), null);
});

// The R1 table, written out independently of the registry.
// Order: terminal, editor, input, dialog, page.
const R1 = {
  "palette.open": [1, 1, 0, 1, 1], "palette.open.alt": [1, 1, 0, 1, 1], "help.open": [1, 1, 1, 0, 1],
  "sidebar.toggle": [1, 0, 0, 0, 1], "files.toggle": [1, 0, 0, 0, 1], "session.jump": [1, 0, 0, 0, 1],
  "session.nextNeeds": [1, 0, 0, 0, 1],
  // F6 (#177 call 12): terminal 1, editor 1, input 1, dialog 0, page 1. The way out of xterm must work from the terminal, and
  // from the editor and a field, which would otherwise trap it; a modal dialog keeps its own focus.
  "ui.regionNext": [1, 1, 1, 0, 1],
  "shell.below": [1, 0, 0, 0, 0], "files.goto": [0, 1, 1, 0, 0],
  // dock.showTerminal (Task 8b): Full mode's way back. Focus can be in the covered terminal, the dock's editor or one of its
  // fields; a dialog keeps its own focus and a page hides the dock.
  "dock.showTerminal": [1, 1, 1, 0, 0],
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
  "ui.regionNext", // F6 would be ESC[17~ in xterm: it is dispatched in the capture phase, so the terminal never sees it
  "help.open", // Ctrl+/ sends nothing in the vendored xterm (upstream maps 0x1F: upgrade hazard)
  "term.paste", // Ctrl+V is the browser paste: documented trade-off
  "dock.showTerminal", // Ctrl+` sends no byte in the vendored xterm (measured in Chromium: onData stayed empty), and the binding hands the key back outside the dock's Full mode
]);

test("terminal invariant", () => {
  for (const s of SC.SHORTCUTS) {
    if (s.status !== "bound") continue;
    const reach = s.scope === "terminal" || (s.allow && s.allow.terminal);
    if (!reach) continue;
    for (const mac of [true, false]) {
      const prim = s.chords.filter((c) => !c.on || (c.on === "mac") === mac);
      for (const c of prim) {
        if (c.allow && c.allow.terminal === false) continue;
        const label = `${s.id} ${SC.renderChord(c, mac)} mac=${mac}`;
        if (mac) {
          const altOnly = c.alt && !c.mod && !c.ctrl;
          assert.ok(!altOnly, `${label} is Alt-only on mac`);
        } else {
          assert.ok(c.shift || TERMINAL_OK.has(s.id), `${label} has no Shift and is not whitelisted`);
        }
      }
    }
  }
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

test("viewer.find and viewer.gotoLine are bound, listed in help, and never global", () => {
  for (const id of ["viewer.find", "viewer.gotoLine"]) {
    assert.strictEqual(entry(id).status, "bound", id);
    assert.strictEqual(entry(id).scope, "viewer", id);
  }
  assert.strictEqual(SC.renderChord(entry("viewer.find").chords[0], true), "⌘F");
  assert.strictEqual(SC.renderChord(entry("viewer.find").chords[0], false), "Ctrl+F");
  assert.strictEqual(SC.renderChord(entry("viewer.gotoLine").chords[0], true), "⌃G");
  assert.strictEqual(SC.renderChord(entry("viewer.gotoLine").chords[0], false), "Ctrl+G");
  for (const [mac, find, goto] of [[true, "⌘F", "⌃G"], [false, "Ctrl+F", "Ctrl+G"]]) {
    const rows = SC.helpRows(mac);
    const f = rows.find((r) => r.label === "Find in file");
    const g = rows.find((r) => r.label === "Go to line");
    assert.ok(f && g, "listed, mac=" + mac);
    assert.strictEqual(f.group, "Editor & files");
    assert.strictEqual(g.group, "Editor & files");
    assert.strictEqual(f.keys, find);
    assert.strictEqual(g.keys, goto);
  }
  // Never global: not in any context, so the terminal keeps readline's Ctrl+G
  // and the browser keeps its own find outside a viewer.
  const ctxs = [NONE, { inTerminal: true }, { inEditor: true }, { inInput: true }, { dialogOpen: true }, { pageOpen: true }];
  for (const ctx of ctxs) {
    assert.strictEqual(SC.shortcutFor(ev("KeyF", "f", { ctrl: true }), false, ctx), null);
    assert.strictEqual(SC.shortcutFor(ev("KeyG", "g", { ctrl: true }), true, ctx), null);
    assert.strictEqual(SC.shortcutFor(ev("KeyG", "g", { ctrl: true }), false, ctx), null);
    assert.strictEqual(SC.shortcutFor(ev("KeyF", "f", { meta: true }), true, ctx), null);
  }
  // The viewer's own listener matches them.
  assert.strictEqual(SC.shortcutMatches("viewer.find", ev("KeyF", "f", { meta: true }), true), true);
  assert.strictEqual(SC.shortcutMatches("viewer.find", ev("KeyF", "f", { ctrl: true }), false), true);
  assert.strictEqual(SC.shortcutMatches("viewer.find", ev("KeyF", "f", { ctrl: true }), true), false);
  assert.strictEqual(SC.shortcutMatches("viewer.gotoLine", ev("KeyG", "g", { ctrl: true }), true), true);
  assert.strictEqual(SC.shortcutMatches("viewer.gotoLine", ev("KeyG", "g", { ctrl: true }), false), true);
  assert.strictEqual(SC.shortcutMatches("viewer.gotoLine", ev("KeyG", "g", { meta: true }), true), false);
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
  for (const mac of [true, false]) {
    const rows = SC.helpRows(mac);
    const labels = rows.map((r) => r.label);
    for (const s of SC.SHORTCUTS) {
      if (s.status === "reserved") assert.ok(!labels.includes(s.label), `reserved ${s.id} listed`);
      else assert.ok(rows.some((r) => r.label === s.label), `${s.id} missing`);
    }
    for (const r of rows) {
      if (mac) assert.ok(!r.keys.includes("Ctrl+"), r.keys);
      else assert.ok(!r.keys.includes("⌘"), r.keys);
    }
  }
  const off = SC.helpRows(false);
  assert.strictEqual(off.find((r) => r.label === "Paste").keys, "Ctrl+V, Ctrl+Shift+V");
  assert.strictEqual(off.find((r) => r.label === "Select text when the agent holds the mouse").keys, "Shift+drag");
  assert.strictEqual(SC.helpRows(true).find((r) => r.label === "Move and pick in lists").keys, "↑ ↓ ↵");
});

test("helpText", () => {
  const rows = SC.helpRows(false);
  const text = SC.helpText(rows, "sysbox");
  const groups = ["General", "Sessions & panes", "Terminal", "Editor & files"];
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
  const order = ["General", "Sessions & panes", "Terminal", "Editor & files"];
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

// helpTextStacked is the phone's version of helpText (#59): the key column cannot
// sit beside its description in a 390 px dialog (the widest helpText line is 64
// chars, the dialog fits 44), so each row is its keys on one line and its
// description, indented two spaces, on the next. Same groups, same order, same
// trailing Runtime line.
test("helpTextStacked puts each row's keys on one line and its description indented on the next", () => {
  const order = ["General", "Sessions & panes", "Terminal", "Editor & files"];
  for (const mac of [true, false]) {
    const rows = SC.helpRows(mac);
    // Reversed so neither the group order nor the row order can come from the registry.
    const lines = SC.helpTextStacked(rows.slice().reverse(), "runc").split("\n");
    const at = order.map((g) => lines.indexOf(g));
    for (const i of at) assert.ok(i >= 0);
    for (let i = 1; i < at.length; i++) assert.ok(at[i - 1] < at[i], order[i] + " out of order");
    order.forEach((g, gi) => {
      // The block under a heading: up to the blank line that ends it.
      const from = at[gi] + 1;
      let to = from;
      while (to < lines.length && lines[to] !== "") to++;
      const want = rows.filter((r) => r.group === g).reverse().flatMap((r) => [r.keys, "  " + r.label]);
      assert.deepStrictEqual(lines.slice(from, to), want, g);
    });
  }
});

test("helpTextStacked: the Runtime line is last, and absent without a label", () => {
  const rows = SC.helpRows(false);
  const withRt = SC.helpTextStacked(rows, "sysbox");
  assert.ok(withRt.endsWith("\n\nRuntime: sysbox"));
  assert.strictEqual(withRt.split("\n").pop(), "Runtime: sysbox");
  assert.ok(!SC.helpTextStacked(rows).includes("Runtime:"));
  assert.ok(!SC.helpTextStacked(rows, "").includes("Runtime:"));
  assert.ok(!SC.helpTextStacked(rows).endsWith("\n"));
});

test("helpTextStacked: no line is wider than the 44 characters a 390 px dialog fits", () => {
  // Measured, not guessed: #ask-body is 322 px wide at 390 px and the 12 px mono
  // face is 7.22 px per character, so 44 characters fit. The longest is the
  // indented "Select text when the agent holds the mouse" (2 + 42 = 44).
  for (const mac of [true, false]) {
    // The rows the phone's dialog actually shows: the F6 row's frame label is wider, and the phone has no regions.
    const rows = SC.helpRows(mac).filter((r) => !DESKTOP_ONLY_LABELS.includes(r.label));
    for (const l of SC.helpTextStacked(rows, "sysbox").split("\n")) {
      assert.ok(l.length <= 44, JSON.stringify(l) + " is " + l.length + " chars");
    }
  }
});

test("helpText is unchanged by the stacked variant (desktop keeps its aligned columns)", () => {
  const rows = SC.helpRows(false);
  const w = Math.max(...rows.map((r) => r.keys.length));
  const text = SC.helpText(rows, "runc");
  assert.ok(text.includes("Ctrl+Shift+K".padEnd(w) + "  Command palette"));
  assert.notStrictEqual(text, SC.helpTextStacked(rows, "runc"));
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

test("#58: the v0.16 legacy chords no longer match anything", () => {
  const k = (o) => ({ ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, key: "", code: "", ...o });
  assert.equal(SC.shortcutFor(k({ altKey: true, code: "Digit1", key: "1" }), false, NONE), null);      // Win/Linux Alt+1
  assert.equal(SC.shortcutFor(k({ metaKey: true, code: "KeyK", key: "k" }), true, NONE), null);         // mac Cmd+K
  assert.equal(SC.shortcutFor(k({ metaKey: true, code: "KeyB", key: "b" }), true, NONE), null);         // mac Cmd+B
  assert.equal(SC.shortcutFor(k({ ctrlKey: true, altKey: true, code: "KeyS", key: "s" }), false, NONE), null);
  assert.equal(SC.shortcutFor(k({ metaKey: true, altKey: true, code: "KeyS", key: "s" }), true, NONE), null);
  assert.equal(SC.shortcutFor(k({ ctrlKey: true, altKey: true, code: "KeyF", key: "f" }), false, NONE), null);
});

test("#58: help has no Old keys group; the permanent fallback stays", () => {
  for (const mac of [true, false]) {
    assert.ok(!SC.helpRows(mac).some((r) => /Old keys/.test(r.group)));
    assert.ok(SC.helpRows(mac).some((r) => r.label === "Command palette (fallback)"));
  }
  assert.ok(!SC.SHORTCUTS.some((s) => "legacy" in s));
});

// F69 (#172): Next that needs you. The founder chose Mod+Shift+J; it fires in the
// terminal and on the page, never in the editor, a text input or a dialog.
test("session.nextNeeds: Mod+Shift+J, listed in the help dialog with its chord on each platform", () => {
  const e = entry("session.nextNeeds");
  assert.ok(e, "registered");
  assert.strictEqual(e.label, "Next that needs you");
  assert.strictEqual(e.group, "Sessions & panes");
  assert.strictEqual(e.status, "bound");
  assert.strictEqual(SC.shortcutHint("session.nextNeeds", false), "Ctrl+Shift+J");
  assert.strictEqual(SC.shortcutHint("session.nextNeeds", true), "⇧⌘J");
  assert.strictEqual(SC.helpRows(false).find((r) => r.label === "Next that needs you").keys, "Ctrl+Shift+J");
  assert.strictEqual(SC.helpRows(true).find((r) => r.label === "Next that needs you").keys, "⇧⌘J");
  assert.strictEqual(SC.helpRows(false).find((r) => r.label === "Next that needs you").group, "Sessions & panes");
  // Listed right after the numbered jump, in the same group.
  const ids = SC.SHORTCUTS.map((s) => s.id);
  assert.strictEqual(ids.indexOf("session.nextNeeds"), ids.indexOf("session.jump") + 1);
});

test("session.nextNeeds: the chord dispatches on a terminal and on the page, not in the editor, an input or a dialog", () => {
  for (const [mac, mods] of [[false, { ctrl: true, shift: true }], [true, { meta: true, shift: true }]]) {
    const e = () => ev("KeyJ", "J", mods);
    assert.strictEqual(SC.shortcutFor(e(), mac, NONE), "session.nextNeeds", "page, mac=" + mac);
    assert.strictEqual(SC.shortcutFor(e(), mac, ctxWith("inTerminal")), "session.nextNeeds", "terminal, mac=" + mac);
    for (const k of ["inEditor", "inInput", "dialogOpen"]) assert.strictEqual(SC.shortcutFor(e(), mac, ctxWith(k)), null, k + " mac=" + mac);
    // Without Shift it is not ours (Ctrl+J is the terminal's newline).
    assert.strictEqual(SC.shortcutFor(ev("KeyJ", "j", { ctrl: !mac, meta: mac }), mac, NONE), null);
  }
});

// Rows the phone's text leaves out: they work where the desktop's sidebar, panes and drawer are.
const DESKTOP_ONLY_LABELS = ["Next that needs you", "Next region: sidebar, pane, terminal, files", "Previous region", "Leave Full mode: show the terminal"];

// ---- F6 / Shift+F6 (#177, plan call 12, frame M8.8) ------------------------------------------------------------------------------------

test("ui.regionNext: F6 and Shift+F6 are one bound row; Shift reverses and is read by the binding", () => {
  const e = entry("ui.regionNext");
  assert.strictEqual(e.status, "bound");
  assert.strictEqual(e.scope, "global");
  assert.strictEqual(e.group, "Sessions & panes");
  assert.strictEqual(e.label, "Next region: sidebar, pane, terminal, files");
  assert.deepStrictEqual(e.chords, [{ code: "F6", shiftAny: true }]);
  for (const mac of [false, true]) {
    assert.strictEqual(SC.shortcutFor(ev("F6", "F6"), mac, NONE), "ui.regionNext", "F6 mac=" + mac);
    assert.strictEqual(SC.shortcutFor(ev("F6", "F6", { shift: true }), mac, NONE), "ui.regionNext", "Shift+F6 mac=" + mac);
    // Any other modifier is not ours (Ctrl+F6 is the browser's / the app's, Alt+F6 the window manager's).
    for (const m of [{ ctrl: true }, { meta: true }, { alt: true }]) {
      assert.strictEqual(SC.shortcutFor(ev("F6", "F6", m), mac, NONE), null, JSON.stringify(m));
    }
  }
});

test("ui.regionNext: the allow flags map to scAllow's order (terminal, editor, input, dialog, page)", () => {
  assert.deepStrictEqual(entry("ui.regionNext").allow, { terminal: true, editor: true, input: true, dialog: false, page: true });
  const f6 = () => ev("F6", "F6");
  for (const k of ["inTerminal", "inEditor", "inInput", "pageOpen"]) assert.strictEqual(SC.shortcutFor(f6(), false, ctxWith(k)), "ui.regionNext", k);
  assert.strictEqual(SC.shortcutFor(f6(), false, ctxWith("dialogOpen")), null, "a modal dialog keeps its focus");
});

test("ui.regionNext: the help rows say F6 and Shift+F6 in the frame's words, in the dialog's Sessions & panes group", () => {
  for (const mac of [false, true]) {
    const rows = SC.helpRows(mac);
    assert.strictEqual(rows.find((r) => r.label === "Next region: sidebar, pane, terminal, files").keys, "F6");
    assert.strictEqual(rows.find((r) => r.label === "Previous region").keys, mac ? "⇧+F6" : "Shift+F6");
    const grid = SC.helpGrid(mac);
    assert.deepStrictEqual(gridRow(grid, "Next region: sidebar, pane, terminal, files").alts, [kbd("F6")]);
    assert.deepStrictEqual(gridRow(grid, "Previous region").alts, [mac ? kbd("⇧", "F6") : kbd("Shift", "F6")]);
    const g = grid.flat().find((x) => x.group === "Sessions & panes");
    assert.ok(g.rows.some((r) => r.label === "Previous region"));
  }
  assert.strictEqual(SC.shortcutHint("ui.regionNext", false), "F6");
});

test("ui.regionNext: bound in app.js, and the phone's dialog filters both region rows", () => {
  const src = readStatic("app.js");
  assert.match(src, /bindShortcut\("ui\.regionNext"/);
  const body = fnSrc(src, "showShortcuts");
  assert.match(body, /ui\.regionNext/);
  assert.match(body, /ui\.regionPrev/);
});

// ---- F29 (#173 Task 5, frame M5.3): the Keyboard shortcuts dialog as two columns of key chips ----------------------------------

const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const STATIC = path.join(__dirname, "..", "static");
const readStatic = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
function fnSrc(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}
const kbd = (...keys) => keys.map((k) => ({ kbd: k }));
const gridRows = (grid) => grid.flatMap((col) => col.flatMap((g) => g.rows));
const gridRow = (grid, label) => gridRows(grid).find((r) => r.label === label);

test("chordKeys: the tokens renderChord joins, per platform", () => {
  assert.deepStrictEqual(SC.chordKeys({ mod: true, shift: true, code: "KeyK" }, false), ["Ctrl", "Shift", "K"]);
  assert.deepStrictEqual(SC.chordKeys({ mod: true, shift: true, code: "KeyK" }, true), ["⇧", "⌘", "K"]);
  assert.deepStrictEqual(SC.chordKeys({ ctrl: true, shift: true, code: "Backquote" }, true), ["⌃", "⇧", "`"]);
  assert.deepStrictEqual(SC.chordKeys({ mod: true, keys: ["Enter"] }, false), ["Ctrl", "↵"]);
  assert.deepStrictEqual(SC.chordKeys({ mod: true, keys: ["Enter"] }, true), ["⌘", "↵"]);
  assert.deepStrictEqual(SC.chordKeys({ mod: true, codes: "Digit1-9" }, false), ["Ctrl", "1…9"]);
  assert.deepStrictEqual(SC.chordKeys({ shiftAny: true, keys: ["?"] }, false), ["?"]);
  assert.deepStrictEqual(SC.chordKeys({ ctrl: true, alt: true, shift: true, code: "KeyX" }, false), ["Ctrl", "Alt", "Shift", "X"]);
  assert.deepStrictEqual(SC.chordKeys({ ctrl: true, alt: true, shift: true, mod: true, code: "KeyX" }, true), ["⌃", "⌥", "⇧", "⌘", "X"]);
});

test("renderChord is the chordKeys tokens joined: '+' off the Mac, nothing on it (every chord, both platforms)", () => {
  for (const s of SC.SHORTCUTS) {
    for (const c of s.chords) {
      assert.strictEqual(SC.renderChord(c, false), SC.chordKeys(c, false).join("+"), s.id);
      assert.strictEqual(SC.renderChord(c, true), SC.chordKeys(c, true).join(""), s.id);
    }
  }
  assert.strictEqual(SC.renderChord({ mod: true, shift: true, code: "KeyK" }, false), "Ctrl+Shift+K");
  assert.strictEqual(SC.renderChord({ mod: true, shift: true, code: "KeyK" }, true), "⇧⌘K");
});

test("helpGrid: General and Terminal on the left, Sessions & panes and Editor & files on the right", () => {
  const grid = SC.helpGrid(false);
  assert.strictEqual(grid.length, 2);
  assert.deepStrictEqual(grid[0].map((g) => g.group), ["General", "Terminal"]);
  assert.deepStrictEqual(grid[1].map((g) => g.group), ["Sessions & panes", "Editor & files"]);
});

test("helpGrid: every row of helpRows once, in SHORTCUTS order, 'New session: start' for the desktop", () => {
  for (const mac of [false, true]) {
    const grid = SC.helpGrid(mac);
    const want = SC.helpRows(mac).map((r) => (r.label === "New session: spawn" ? "New session: start" : r.label));
    const got = gridRows(grid).map((r) => r.label);
    assert.deepStrictEqual([...got].sort(), [...want].sort(), "mac=" + mac);
    assert.strictEqual(got.filter((l) => l === "New session: start").length, 1);
    assert.ok(!got.includes("New session: spawn"), "the phone's label is not the desktop's");
    // Inside a group the rows keep the registry's order.
    for (const g of grid.flat()) {
      const order = SC.SHORTCUTS.filter((s) => s.group === g.group).map((s) => s.deskLabel || s.label);
      assert.deepStrictEqual(g.rows.map((r) => r.label), order, g.group);
    }
  }
  // Only the one row has a desktop label, and helpRows (the phone's text) still says "spawn".
  assert.deepStrictEqual(SC.SHORTCUTS.filter((s) => s.deskLabel).map((s) => s.id), ["spawn.submit"]);
  assert.strictEqual(entry("spawn.submit").label, "New session: spawn");
  assert.ok(SC.helpRows(false).some((r) => r.label === "New session: spawn"));
});

test("helpGrid: a chord is its kbd tokens, several chords are several alternatives", () => {
  const grid = SC.helpGrid(false);
  assert.deepStrictEqual(gridRow(grid, "Command palette").alts, [kbd("Ctrl", "Shift", "K")]);
  assert.deepStrictEqual(gridRow(grid, "Keyboard shortcuts").alts, [kbd("Ctrl", "/"), kbd("?")]);
  assert.deepStrictEqual(gridRow(grid, "Paste").alts, [kbd("Ctrl", "V"), kbd("Ctrl", "Shift", "V")]);
  assert.deepStrictEqual(gridRow(grid, "Copy selection").alts, [kbd("Ctrl", "Shift", "C")]);
  assert.deepStrictEqual(gridRow(grid, "Jump to session 1–9").alts, [kbd("Ctrl", "1…9")]);
  assert.deepStrictEqual(gridRow(grid, "New session: start").alts, [kbd("Ctrl", "↵")]);
  assert.deepStrictEqual(gridRow(grid, "Next that needs you").alts, [kbd("Ctrl", "Shift", "J")], "the desktop keeps it");
  // Help-only text: the keys are chips, a word that is not a key stays text.
  assert.deepStrictEqual(gridRow(grid, "Select text when the agent holds the mouse").alts, [[{ kbd: "Shift" }, { text: "drag" }]]);
  assert.deepStrictEqual(gridRow(grid, "Move and pick in lists").alts, [kbd("↑", "↓", "Enter")]);
  assert.deepStrictEqual(gridRow(grid, "Close a dialog, menu or page").alts, [kbd("Esc")]);
});

test("helpGrid(true): the Mac's glyph tokens", () => {
  const grid = SC.helpGrid(true);
  assert.deepStrictEqual(gridRow(grid, "Command palette").alts, [kbd("⇧", "⌘", "K")]);
  assert.deepStrictEqual(gridRow(grid, "Keyboard shortcuts").alts, [kbd("⌘", "/"), kbd("?")]);
  assert.deepStrictEqual(gridRow(grid, "Paste").alts, [kbd("⌘", "V")]);
  assert.deepStrictEqual(gridRow(grid, "Copy selection").alts, [kbd("⌘", "C"), kbd("⇧", "⌘", "C")]);
  assert.deepStrictEqual(gridRow(grid, "New session: start").alts, [kbd("⌘", "↵")]);
  assert.deepStrictEqual(gridRow(grid, "Select text when the agent holds the mouse").alts, [[{ kbd: "⌥" }, { text: "drag" }]]);
  assert.deepStrictEqual(gridRow(grid, "Move and pick in lists").alts, [kbd("↑", "↓", "↵")]);
  for (const r of gridRows(grid)) for (const alt of r.alts) for (const t of alt) {
    if (t.kbd !== undefined) assert.ok(!/Ctrl|Shift|Alt/.test(t.kbd) || t.kbd === "Esc" || t.kbd === "Enter", t.kbd);
  }
});

test("helpGrid carries no Runtime line, and a group the columns do not name is not dropped", () => {
  for (const mac of [false, true]) assert.ok(!JSON.stringify(SC.helpGrid(mac)).includes("Runtime"));
  SC.SHORTCUTS.push({ id: "t.extra", label: "Extra", group: "Zeta", scope: "global", status: "help-only", chords: [], text: { mac: "X", other: "X" } });
  try {
    const grid = SC.helpGrid(false);
    assert.deepStrictEqual(grid[1].map((g) => g.group), ["Sessions & panes", "Editor & files", "Zeta"]);
    assert.deepStrictEqual(gridRow(grid, "Extra").alts, [kbd("X")]);
  } finally {
    SC.SHORTCUTS.pop();
  }
});

test("helpRows, helpText and helpTextStacked still say 'New session: spawn' (the phone's text is unchanged)", () => {
  const rows = SC.helpRows(false);
  assert.strictEqual(rows.find((r) => r.group === "Sessions & panes" && /^New session/.test(r.label)).label, "New session: spawn");
  assert.ok(SC.helpText(rows, "x").includes("New session: spawn"));
  assert.ok(SC.helpTextStacked(rows, "x").includes("New session: spawn"));
  assert.ok(!SC.helpText(rows, "x").includes("New session: start"));
});

// shortcutsGridEl and showShortcuts live in app.js, a classic script that cannot be required: both are cut out by name and
// run against a stand-in DOM, with the real registry (shortcuts.js) loaded into the same context.
function elStub(tag) {
  return {
    tag, className: "", textContent: "", children: [], attrs: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    append(...n) { this.children.push(...n); },
    appendChild(n) { this.children.push(n); return n; },
    replaceChildren(...n) { this.children = n; },
  };
}
function showShortcutsEnv(width) {
  const asked = [];
  const ctx = vm.createContext({
    window: { innerWidth: width },
    document: { createElement: elStub },
    mobileActive: (w) => w <= 700,
    tierRuntimeLabel: (r) => "tier:" + r,
    boxRuntime: "sysbox",
    askDialog: (o) => { asked.push(o); return Promise.resolve({ outcome: "cancel", value: "" }); },
  });
  new vm.Script(readStatic("shortcuts.js")).runInContext(ctx);
  const app = readStatic("app.js");
  new vm.Script(fnSrc(app, "shortcutsGridEl") + "\n" + fnSrc(app, "showShortcuts")).runInContext(ctx);
  return { ctx, asked };
}
const textOf = (n) => (n.textContent || "") + (n.children || []).map(textOf).join("|");

test("shortcutsGridEl: two columns of groups, a row per shortcut, its chips and an 'or' between alternatives", () => {
  const { ctx } = showShortcutsEnv(1280);
  const root = ctx.shortcutsGridEl(SC.helpGrid(false));
  assert.strictEqual(root.className, "sc-cols");
  assert.deepStrictEqual(root.children.map((c) => c.className), ["sc-col", "sc-col"]);
  const groups = root.children.map((col) => col.children.map((g) => g.children[0].textContent));
  assert.deepStrictEqual(groups, [["General", "Terminal"], ["Sessions & panes", "Editor & files"]]);
  for (const col of root.children) for (const grp of col.children) {
    assert.strictEqual(grp.className, "sc-grp");
    assert.strictEqual(grp.children[0].className, "grp-label", "the kit's group label");
    assert.strictEqual(grp.children[0].tag, "div");
    for (const row of grp.children.slice(1)) {
      assert.strictEqual(row.className, "sc-row");
      assert.deepStrictEqual(row.children.map((c) => c.className), ["sc-label", "sc-chord"]);
    }
  }
  const rowOf = (label) => root.children.flatMap((c) => c.children).flatMap((g) => g.children.slice(1)).find((r) => r.children[0].textContent === label);
  const chord = (label) => rowOf(label).children[1].children.map((c) => c.tag + "." + c.className + ":" + c.textContent);
  assert.deepStrictEqual(chord("Command palette"), ["kbd.kbd:Ctrl", "kbd.kbd:Shift", "kbd.kbd:K"]);
  assert.deepStrictEqual(chord("Paste"), ["kbd.kbd:Ctrl", "kbd.kbd:V", "span.sc-word:or", "kbd.kbd:Ctrl", "kbd.kbd:Shift", "kbd.kbd:V"]);
  assert.deepStrictEqual(chord("Keyboard shortcuts"), ["kbd.kbd:Ctrl", "kbd.kbd:/", "span.sc-word:or", "kbd.kbd:?"]);
  assert.deepStrictEqual(chord("Select text when the agent holds the mouse"), ["kbd.kbd:Shift", "span.sc-word:drag"]);
  assert.ok(rowOf("Next that needs you"), "the desktop lists it");
  assert.ok(rowOf("New session: start"));
  assert.ok(!textOf(root).includes("Runtime"));
});

test("showShortcuts on the desktop: the key grid, no footer, no Close button, no Runtime line", async () => {
  const { ctx, asked } = showShortcutsEnv(1280);
  await ctx.showShortcuts();
  assert.strictEqual(asked.length, 1);
  const o = asked[0];
  assert.strictEqual(o.title, "Keyboard shortcuts");
  assert.strictEqual(o.info, true);
  assert.strictEqual(o.footless, true);
  assert.strictEqual(o.cls, "keys");
  assert.strictEqual(o.wide, false, "only .keys sizes the dialog");
  assert.strictEqual(o.body || "", "");
  assert.strictEqual(o.confirmLabel, undefined, "no Close button: the X is the close");
  assert.strictEqual(o.extra.length, 1);
  assert.strictEqual(o.extra[0].className, "sc-cols");
  assert.ok(!textOf(o.extra[0]).includes("Runtime"));
  assert.ok(textOf(o.extra[0]).includes("Next that needs you"));
});

test("showShortcuts on the phone is what it was: stacked text, no Next that needs you, the Runtime line and Close", async () => {
  const { ctx, asked } = showShortcutsEnv(390);
  await ctx.showShortcuts();
  const rows = SC.helpRows(false).filter((r) => !DESKTOP_ONLY_LABELS.includes(r.label));
  assert.strictEqual(
    JSON.stringify(asked[0]),
    JSON.stringify({ title: "Keyboard shortcuts", body: SC.helpTextStacked(rows, "tier:sysbox"), confirmLabel: "Close", info: true }),
  );
  assert.ok(asked[0].body.endsWith("\n\nRuntime: tier:sysbox"));
  assert.ok(asked[0].body.includes("New session: spawn"));
  assert.ok(!asked[0].body.includes("Next that needs you"));
  assert.ok(!/region|F6/i.test(asked[0].body), "the phone has no regions to cycle");
});

test("showShortcuts source: the desktop branch passes footless and cls keys, the phone's keeps the stacked text and the Runtime label", () => {
  const body = fnSrc(readStatic("app.js"), "showShortcuts");
  assert.match(body, /footless:\s*true/);
  assert.match(body, /cls:\s*"keys"/);
  assert.match(body, /wide:\s*false/);
  assert.match(body, /helpGrid\(IS_MAC\)/);
  assert.match(body, /helpTextStacked\(/);
  assert.match(body, /tierRuntimeLabel\(boxRuntime\)/);
  assert.match(body, /session\.nextNeeds/);
  assert.ok(!/innerHTML/.test(fnSrc(readStatic("app.js"), "shortcutsGridEl")), "built with createElement and textContent");
});

test("desktop.css: Keyboard shortcuts is an 880 px card of two columns, as frame M5.3 draws it", () => {
  const css = readStatic("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => { const i = css.indexOf(sel + " {"); assert.ok(i >= 0, sel); return css.slice(i, css.indexOf("}", i)); };
  assert.match(rule("#ask-modal.keys"), /width:\s*880px/);
  assert.match(rule("#ask-modal.keys"), /max-width:\s*calc\(100vw - 40px\)/);
  assert.match(rule("#ask-modal.keys #ask-extra"), /overflow-y:\s*auto/, "a short window scrolls the list: the footer that held Close is gone");
  assert.match(rule("#ask-modal.keys #ask-extra"), /min-height:\s*0/);
  const cols = rule(".sc-cols");
  assert.match(cols, /display:\s*grid/);
  assert.match(cols, /grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(cols, /column-gap:\s*var\(--sp-10\)/);
  assert.match(rule(".sc-col"), /flex-direction:\s*column/);
  assert.match(rule(".sc-col"), /gap:\s*var\(--sp-5\)/);
  assert.match(rule(".sc-grp"), /flex-direction:\s*column/);
  assert.match(rule(".sc-grp"), /gap:\s*2px/);
  assert.match(rule(".sc-grp > .grp-label"), /padding-bottom:\s*var\(--sp-1\)/);
  const row = rule(".sc-row");
  assert.match(row, /display:\s*flex/);
  assert.match(row, /align-items:\s*center/);
  assert.match(row, /min-height:\s*30px/);
  assert.match(row, /gap:\s*var\(--sp-3\)/);
  const label = rule(".sc-label");
  assert.match(label, /flex:\s*1 1 auto/);
  assert.match(label, /min-width:\s*0/);
  assert.match(label, /font-size:\s*var\(--t-md\)/);
  const chord = rule(".sc-chord");
  assert.match(chord, /display:\s*flex/);
  assert.match(chord, /gap:\s*var\(--sp-1\)/);
  assert.match(chord, /flex:\s*none/);
  assert.match(rule(".sc-word"), /font-size:\s*var\(--t-sm\)/);
  assert.match(rule(".sc-word"), /color:\s*var\(--muted\)/);
  // It must beat the read-only dialog's 560 px (#ask-modal.info) by coming after it.
  assert.ok(css.indexOf("#ask-modal.keys {") > css.indexOf("#ask-modal.info {"));
});

// Task 8b: dock.showTerminal, Full mode's way back to the terminal.
test("dock.showTerminal: plain Ctrl+`, which is not + Shell below (Ctrl+Shift+`), on every platform", () => {
  const e = entry("dock.showTerminal");
  assert.strictEqual(e.status, "bound");
  assert.strictEqual(e.scope, "global");
  assert.strictEqual(SC.shortcutHint("dock.showTerminal", false), "Ctrl+`");
  assert.strictEqual(SC.shortcutHint("dock.showTerminal", true), "⌃`");
  for (const mac of [false, true]) {
    assert.strictEqual(SC.shortcutFor(ev("Backquote", "`", { ctrl: true }), mac, NONE), "dock.showTerminal", "mac=" + mac);
    assert.strictEqual(SC.shortcutFor(ev("Backquote", "~", { ctrl: true, shift: true }), mac, NONE), "shell.below", "the shifted chord is still + Shell below, mac=" + mac);
    assert.strictEqual(SC.shortcutFor(ev("Backquote", "`", {}), mac, NONE), null, "a bare backquote is a character");
    assert.strictEqual(SC.shortcutFor(ev("Backquote", "`", { alt: true }), mac, NONE), null);
  }
});

test("dock.showTerminal fires from the covered terminal, the dock's editor and its fields, not over a dialog or a page", () => {
  const e = () => ev("Backquote", "`", { ctrl: true });
  for (const k of ["inTerminal", "inEditor", "inInput"]) assert.strictEqual(SC.shortcutFor(e(), false, ctxWith(k)), "dock.showTerminal", k);
  for (const k of ["dialogOpen", "pageOpen"]) assert.strictEqual(SC.shortcutFor(e(), false, ctxWith(k)), null, k);
});

test("dock.showTerminal is listed in the help dialog (Editor & files) and left out of the phone's", () => {
  const row = SC.helpRows(false).find((r) => r.label === "Leave Full mode: show the terminal");
  assert.ok(row);
  assert.strictEqual(row.group, "Editor & files");
  assert.strictEqual(row.keys, "Ctrl+`");
  const app = readStatic("app.js");
  assert.match(fnSrc(app, "showShortcuts"), /"dock\.showTerminal"\]\.map\(\(id\) => scEntry\(id\)\.label\)/, "the phone's text drops it, as it drops the other desktop-only rows");
});

test("dock.showTerminal's binding hands the key back (false) unless the dock is in Full mode, so the terminal keeps it as ever", () => {
  const app = readStatic("app.js");
  assert.match(app, /bindShortcut\("dock\.showTerminal", \(\) => dockShowTerminal\(\)\);/);
  assert.match(fnSrc(app, "dockShowTerminal"), /if \(!dr \|\| dr\.hidden \|\| dr\.dataset\.max !== "1"\) return false;/);
});
