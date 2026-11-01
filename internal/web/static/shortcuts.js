"use strict";

// One registry for every keyboard shortcut: its chords, its label, where it may
// fire, and how it is rendered. app.js, palette.js and the other scripts read
// this instead of keeping their own key tests and hint strings (issue #52).
// Pure: no DOM, so `node --test` reaches all of it.
//
// Sourced facts behind the table (defect 7 of the spec):
//  - A page CAN cancel Ctrl/Cmd+digit tab-select in Chrome and Firefox: the
//    Chromium IsReservedCommandOrKey list does not include it. Only
//    Ctrl/Cmd+T/N/W (and Shift variants) are out of reach.
//  - Chrome on Linux binds Alt+1...9 to tab selection, which is why Alt+digit
//    is a legacy chord only, scheduled for removal.
//  - xterm.js sends bytes for Ctrl+3...8 only (Esc ^\ ^] ^^ ^_ DEL). It sends
//    nothing for Ctrl+1, 2, 9, 0, nothing for Ctrl+= - 0, and nothing for
//    Ctrl+/ in the vendored build. Upstream master maps Ctrl+/ to 0x1F: an
//    xterm-upgrade hazard for help.open.
//
// Everything below is a top-level name of a classic script, so under the
// browser it is a global shared with every sibling script. Names are the
// public API; helpers are prefixed sc to stay clear of collisions.

function isMacPlatform(nav) {
  return /Mac|iPhone|iPad/.test((nav && (nav.platform || nav.userAgent)) || "");
}

const IS_MAC = typeof navigator !== "undefined" && isMacPlatform(navigator);

const SC_REMOVE_IN = "v0.17.0";

// scAllow(terminal, editor, input, dialog, page): 1/0 flags, in the R1 table's column order.
function scAllow(terminal, editor, input, dialog, page) {
  return { terminal: !!terminal, editor: !!editor, input: !!input, dialog: !!dialog, page: !!page };
}

const SC_LEGACY = (chord) => ({ chord, removeIn: SC_REMOVE_IN });

const SHORTCUTS = [
  { id: "palette.open", label: "Command palette", group: "General", scope: "global", status: "bound",
    chords: [{ mod: true, shift: true, code: "KeyK" }],
    legacy: [SC_LEGACY({ mod: true, code: "KeyK", on: "mac" })],
    allow: scAllow(1, 1, 0, 1, 1) },
  { id: "palette.open.alt", label: "Command palette (fallback)", group: "General", scope: "global", status: "bound",
    chords: [{ ctrl: true, shift: true, code: "Space" }],
    allow: scAllow(1, 1, 0, 1, 1) },
  { id: "help.open", label: "Keyboard shortcuts", group: "General", scope: "global", status: "bound",
    chords: [
      { mod: true, shiftAny: true, code: "Slash", keys: ["/"] },
      { shiftAny: true, keys: ["?"], allow: scAllow(0, 0, 0, 0, 1) },
    ],
    allow: scAllow(1, 1, 1, 0, 1) },
  { id: "sidebar.toggle", label: "Toggle sidebar", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ mod: true, shift: true, code: "KeyB" }],
    legacy: [SC_LEGACY({ mod: true, code: "KeyB", on: "mac" }), SC_LEGACY({ mod: true, alt: true, code: "KeyS" })],
    allow: scAllow(1, 0, 0, 0, 1) },
  { id: "files.toggle", label: "Files drawer", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ mod: true, shift: true, code: "KeyE" }],
    legacy: [SC_LEGACY({ mod: true, alt: true, code: "KeyF" })],
    allow: scAllow(1, 0, 0, 0, 1) },
  { id: "session.jump", label: "Jump to session 1–9", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ mod: true, codes: "Digit1-9" }],
    legacy: [SC_LEGACY({ alt: true, codes: "Digit1-9", on: "other" })],
    allow: scAllow(1, 0, 0, 0, 1) },
  { id: "shell.below", label: "+ Shell below", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ ctrl: true, shift: true, code: "Backquote" }],
    allow: scAllow(1, 0, 0, 0, 0) },
  { id: "files.goto", label: "Go to file", group: "Editor & files", scope: "global", status: "bound",
    chords: [{ mod: true, code: "KeyP" }],
    allow: scAllow(0, 1, 1, 0, 0) },
  { id: "zoom.in", label: "Zoom in", group: "General", scope: "global", status: "bound",
    chords: [{ mod: true, shiftAny: true, keys: ["=", "+"] }],
    allow: scAllow(1, 0, 0, 0, 0) },
  { id: "zoom.out", label: "Zoom out", group: "General", scope: "global", status: "bound",
    chords: [{ mod: true, shiftAny: true, keys: ["-"] }],
    allow: scAllow(1, 0, 0, 0, 0) },
  { id: "zoom.reset", label: "Reset zoom", group: "General", scope: "global", status: "bound",
    chords: [{ mod: true, shiftAny: true, keys: ["0"] }],
    allow: scAllow(1, 0, 0, 0, 0) },
  { id: "term.copy", label: "Copy selection", group: "Terminal", scope: "terminal", status: "bound",
    chords: [{ mod: true, code: "KeyC", on: "mac" }, { mod: true, shift: true, code: "KeyC" }] },
  { id: "term.paste", label: "Paste", group: "Terminal", scope: "terminal", status: "bound",
    chords: [
      { mod: true, code: "KeyV", on: "mac" },
      { ctrl: true, code: "KeyV", on: "other" },
      { ctrl: true, shift: true, code: "KeyV", on: "other" },
    ] },
  { id: "term.select", label: "Select text when the agent holds the mouse", group: "Terminal", scope: "terminal",
    status: "help-only", chords: [], text: { mac: "⌥+drag", other: "Shift+drag" } },
  { id: "editor.save", label: "Save", group: "Editor & files", scope: "editor", status: "bound",
    chords: [{ mod: true, code: "KeyS" }] },
  { id: "palette.insert", label: "Palette: open in new pane / insert path", group: "Editor & files", scope: "palette",
    status: "bound", chords: [{ mod: true, keys: ["Enter"] }] },
  { id: "spawn.submit", label: "New session: spawn", group: "Sessions & panes", scope: "spawn", status: "bound",
    chords: [{ mod: true, keys: ["Enter"] }] },
  { id: "ui.escape", label: "Close a dialog, menu or page", group: "General", scope: "global", status: "help-only",
    chords: [], text: { mac: "Esc", other: "Esc" } },
  { id: "ui.lists", label: "Move and pick in lists", group: "General", scope: "global", status: "help-only",
    chords: [], text: { mac: "↑ ↓ ↵", other: "↑ ↓ Enter" } },
  { id: "viewer.find", label: "Find in file", group: "Editor & files", scope: "viewer", status: "reserved",
    chords: [{ mod: true, code: "KeyF" }] },
  { id: "viewer.gotoLine", label: "Go to line", group: "Editor & files", scope: "viewer", status: "reserved",
    chords: [{ ctrl: true, code: "KeyG" }] },
];

const SC_OLD_KEYS = "Old keys (removed in " + SC_REMOVE_IN + ")";

function scEntry(id) {
  return SHORTCUTS.find((s) => s.id === id) || null;
}

function scOnHere(chord, mac) {
  return !chord.on || (chord.on === "mac") === !!mac;
}

function chordMatches(e, chord, mac) {
  if (!chord || !scOnHere(chord, mac)) return false;
  if (e.isComposing) return false;
  if (e.getModifierState && e.getModifierState("AltGraph")) return false;
  const wantCtrl = !!chord.ctrl || (!!chord.mod && !mac);
  const wantMeta = !!chord.mod && !!mac;
  if (!!e.ctrlKey !== wantCtrl || !!e.metaKey !== wantMeta || !!e.altKey !== !!chord.alt) return false;
  if (!chord.shiftAny && !!e.shiftKey !== !!chord.shift) return false;
  if (chord.code && e.code === chord.code) return true;
  if (chord.codes === "Digit1-9" && /^Digit[1-9]$/.test(e.code || "")) return true;
  if (chord.keys && chord.keys.includes(e.key)) return true;
  return false;
}

function scKeyName(chord) {
  if (chord.code) {
    const c = chord.code;
    if (/^Key[A-Z]$/.test(c)) return c.slice(3);
    if (/^Digit[0-9]$/.test(c)) return c.slice(5);
    return { Backquote: "`", Space: "Space", Slash: "/" }[c] || c;
  }
  if (chord.codes === "Digit1-9") return "1…9";
  if (chord.keys && chord.keys.length) return chord.keys[0] === "Enter" ? "↵" : chord.keys[0];
  return "";
}

function renderChord(chord, mac) {
  const key = scKeyName(chord);
  const ctrl = !!chord.ctrl || (!!chord.mod && !mac);
  const cmd = !!chord.mod && !!mac;
  if (mac) return (ctrl ? "⌃" : "") + (chord.alt ? "⌥" : "") + (chord.shift ? "⇧" : "") + (cmd ? "⌘" : "") + key;
  return [ctrl ? "Ctrl" : "", chord.alt ? "Alt" : "", chord.shift ? "Shift" : "", key].filter(Boolean).join("+");
}

function scPrimary(entry, mac) {
  return entry.chords.filter((c) => scOnHere(c, mac));
}

function scLegacy(entry, mac) {
  return (entry.legacy || []).map((l) => l.chord).filter((c) => scOnHere(c, mac));
}

function activeChords(entry, mac) {
  return scPrimary(entry, mac).concat(scLegacy(entry, mac));
}

function shortcutHint(id, mac) {
  const entry = scEntry(id);
  const first = entry && scPrimary(entry, mac)[0];
  return first ? renderChord(first, mac) : "";
}

function jumpHint(n, mac) {
  return renderChord({ mod: true, code: "Digit" + n }, mac);
}

function shortcutMatches(id, e, mac) {
  const entry = scEntry(id);
  return !!entry && activeChords(entry, mac).some((c) => chordMatches(e, c, mac));
}

// A context flag that is true and not allowed rejects the chord. ctx.mobile is
// not part of Allow: callers that care about it gate the binding itself.
function scAdmits(allow, ctx) {
  if (!allow) return false;
  const c = ctx || {};
  return !((c.inTerminal && !allow.terminal) || (c.inEditor && !allow.editor) || (c.inInput && !allow.input) ||
    (c.dialogOpen && !allow.dialog) || (c.pageOpen && !allow.page));
}

function shortcutFor(e, mac, ctx) {
  for (const s of SHORTCUTS) {
    if (s.scope !== "global" || s.status !== "bound") continue;
    for (const c of activeChords(s, mac)) {
      if (chordMatches(e, c, mac) && scAdmits(c.allow || s.allow, ctx)) return s.id;
    }
  }
  return null;
}

let scBindings = {};

function bindShortcut(id, fn) {
  scBindings[id] = fn;
}

function _resetShortcutBindings() {
  scBindings = {};
}

function dispatchShortcut(e, ctx, mac) {
  const id = shortcutFor(e, mac, ctx);
  const fn = id && scBindings[id];
  if (!fn) return false;
  if (fn(e, ctx) === false) return false;
  e.preventDefault();
  e.stopPropagation();
  return true;
}

// CodeMirror key names for a binding, e.g. editor.save -> ["Cmd-S"] / ["Ctrl-S"].
function cmKeys(id, mac) {
  const entry = scEntry(id);
  if (!entry) return [];
  return scPrimary(entry, mac).map((c) => {
    const parts = [];
    if (c.mod) parts.push(mac ? "Cmd" : "Ctrl");
    if (c.ctrl) parts.push("Ctrl");
    if (c.alt) parts.push("Alt");
    if (c.shift) parts.push("Shift");
    parts.push(scKeyName(c));
    return parts.join("-");
  });
}

function helpRows(mac) {
  const rows = [];
  for (const s of SHORTCUTS) {
    if (s.status === "reserved") continue;
    const keys = s.status === "help-only"
      ? s.text[mac ? "mac" : "other"]
      : scPrimary(s, mac).map((c) => renderChord(c, mac)).join(" / ");
    rows.push({ group: s.group, keys, label: s.label });
  }
  for (const s of SHORTCUTS) {
    for (const c of scLegacy(s, mac)) rows.push({ group: SC_OLD_KEYS, keys: renderChord(c, mac), label: s.label });
  }
  return rows;
}

function helpText(rows, runtimeLabel) {
  const w = Math.max(0, ...rows.map((r) => r.keys.length));
  const groups = [];
  for (const r of rows) if (!groups.includes(r.group)) groups.push(r.group);
  const blocks = groups.map((g) => [g].concat(
    rows.filter((r) => r.group === g).map((r) => r.keys.padEnd(w) + "  " + r.label),
  ).join("\n"));
  return blocks.join("\n\n") + (runtimeLabel ? "\n\nRuntime: " + runtimeLabel : "");
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    isMacPlatform, IS_MAC, SHORTCUTS, chordMatches, renderChord, activeChords, shortcutHint, jumpHint,
    shortcutMatches, shortcutFor, bindShortcut, dispatchShortcut, _resetShortcutBindings, cmKeys, helpRows, helpText,
  };
}
