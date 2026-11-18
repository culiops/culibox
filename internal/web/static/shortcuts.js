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
//  - Chrome on Linux binds Alt+1...9 to tab selection, which is why session
//    jump is Ctrl/Cmd+digit and never Alt+digit.
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

// scAllow(terminal, editor, input, dialog, page): 1/0 flags, in the R1 table's column order.
function scAllow(terminal, editor, input, dialog, page) {
  return { terminal: !!terminal, editor: !!editor, input: !!input, dialog: !!dialog, page: !!page };
}

const SHORTCUTS = [
  { id: "palette.open", label: "Command palette", group: "General", scope: "global", status: "bound",
    chords: [{ mod: true, shift: true, code: "KeyK" }],
    allow: scAllow(1, 1, 0, 1, 1) },
  { id: "palette.open.alt", label: "Command palette (fallback)", group: "General", scope: "global", status: "bound",
    chords: [{ ctrl: true, shift: true, code: "Space" }],
    allow: scAllow(1, 1, 0, 1, 1) },
  // zoom.* MUST stay above help.open: shortcutFor returns the first match. On
  // QWERTZ layouts the main-row "-" is e.code "Slash" with e.key "-", which
  // help.open's code match would claim before zoom.out's key match. Help stays
  // reachable there through Ctrl+Shift+7 (key "/") and "?".
  { id: "zoom.in", label: "Zoom in", group: "General", scope: "global", status: "bound",
    chords: [{ mod: true, shiftAny: true, keys: ["=", "+"] }],
    allow: scAllow(1, 0, 0, 0, 0) },
  { id: "zoom.out", label: "Zoom out", group: "General", scope: "global", status: "bound",
    chords: [{ mod: true, shiftAny: true, keys: ["-"] }],
    allow: scAllow(1, 0, 0, 0, 0) },
  { id: "zoom.reset", label: "Reset zoom", group: "General", scope: "global", status: "bound",
    chords: [{ mod: true, shiftAny: true, keys: ["0"] }],
    allow: scAllow(1, 0, 0, 0, 0) },
  { id: "help.open", label: "Keyboard shortcuts", group: "General", scope: "global", status: "bound",
    chords: [
      { mod: true, shiftAny: true, code: "Slash", keys: ["/"] },
      { shiftAny: true, keys: ["?"], allow: scAllow(0, 0, 0, 0, 1) },
    ],
    allow: scAllow(1, 1, 1, 0, 1) },
  { id: "sidebar.toggle", label: "Toggle sidebar", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ mod: true, shift: true, code: "KeyB" }],
    allow: scAllow(1, 0, 0, 0, 1) },
  { id: "files.toggle", label: "Files dock", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ mod: true, shift: true, code: "KeyE" }],
    allow: scAllow(1, 0, 0, 0, 1) },
  { id: "session.jump", label: "Jump to session 1–9", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ mod: true, codes: "Digit1-9" }],
    allow: scAllow(1, 0, 0, 0, 1) },
  { id: "session.nextNeeds", label: "Next that needs you", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ mod: true, shift: true, code: "KeyJ" }],
    allow: scAllow(1, 0, 0, 0, 1) },
  // F6 / Shift+F6 (#177 call 12, frame M8.8): cycle sidebar -> pane header + strip -> terminal -> drawer. One bound row:
  // shiftAny, and the binding reads e.shiftKey for the direction. Dispatched in the capture phase, so xterm (which
  // would send ESC[17~) never sees it. Allowed in the terminal, the editor and a field (else F6 would trap there) and
  // on a page; not over a modal dialog. Whether a browser lets a page cancel F6 is measured, and a Mac row (Fn key).
  { id: "ui.regionNext", label: "Next region: sidebar, pane, terminal, files", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ code: "F6", shiftAny: true }],
    allow: scAllow(1, 1, 1, 0, 1) },
  // The reverse direction is the same chord with Shift; this row only tells the dialog so (the frame draws two rows).
  { id: "ui.regionPrev", label: "Previous region", group: "Sessions & panes", scope: "global", status: "help-only",
    chords: [], text: { mac: "⇧+F6", other: "Shift+F6" } },
  { id: "shell.below", label: "+ Shell below", group: "Sessions & panes", scope: "global", status: "bound",
    chords: [{ ctrl: true, shift: true, code: "Backquote" }],
    allow: scAllow(1, 0, 0, 0, 0) },
  { id: "files.goto", label: "Go to file", group: "Editor & files", scope: "global", status: "bound",
    chords: [{ mod: true, code: "KeyP" }],
    allow: scAllow(0, 1, 1, 0, 0) },
  // Out of the Files dock's Full mode, back to the session's terminal (the agent strip's button says the chord). Plain
  // Ctrl+`: Ctrl+Shift+` is + Shell below. Only fires in Full mode (the binding returns false anywhere else, which leaves
  // the key to the terminal as ever), so it is allowed wherever focus can be in Full mode: the covered terminal, the dock's
  // editor and its fields; not over a dialog, and not on a page, which hides the dock.
  { id: "dock.showTerminal", label: "Leave Full mode: show the terminal", group: "Editor & files", scope: "global", status: "bound",
    chords: [{ ctrl: true, code: "Backquote" }],
    allow: scAllow(1, 1, 1, 0, 0) },
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
  // Bound inside the Files dock's editor (Monaco's addCommand, editortab.js), where the toggle also lives in the ⋯ menu.
  { id: "editor.wrap", label: "Wrap lines", group: "Editor & files", scope: "editor", status: "bound",
    chords: [{ alt: true, code: "KeyZ" }] },
  // Bound inside the Files dock's Changes view (editortab.js editorBind, editorreview.js): the next change of the file, and past the
  // last one the next changed file of the session, opened in Changes. Shift goes back, onto the last change of the previous file.
  { id: "editor.nextChange", label: "Next change (then the next changed file)", group: "Editor & files", scope: "editor", status: "bound",
    chords: [{ code: "F7" }] },
  { id: "editor.prevChange", label: "Previous change", group: "Editor & files", scope: "editor", status: "bound",
    chords: [{ shift: true, code: "F7" }] },
  // Add a note on a line of the Changes view (editornotes.js): `c` while a Changes view has the keyboard (it is a read-only view, so the key
  // is free; never in Code, where it is typing), and Mod+Shift+A from a Changes view or from Code, where it asks for Changes at the
  // line. Bound as Monaco commands (editortab.js editorBindNoteKeys), so scope "changes" keeps the global dispatcher out of it.
  { id: "editor.addNote", label: "Add a note on the line (Changes)", group: "Editor & files", scope: "changes", status: "bound",
    chords: [{ code: "KeyC" }, { mod: true, shift: true, code: "KeyA" }] },
  // The note's own text box (editornotes.js): Mod+Enter saves. Esc cancels, as everywhere (ui.escape).
  { id: "editor.noteSave", label: "Note: save", group: "Editor & files", scope: "note", status: "bound",
    chords: [{ mod: true, keys: ["Enter"] }] },
  { id: "palette.insert", label: "Palette: open in new pane / insert path", group: "Editor & files", scope: "palette",
    status: "bound", chords: [{ mod: true, keys: ["Enter"] }] },
  // deskLabel: what the desktop's Keyboard shortcuts grid says (the dialog's button reads "Start session" there);
  // the phone's help text keeps `label`.
  { id: "spawn.submit", label: "New session: spawn", deskLabel: "New session: start", group: "Sessions & panes", scope: "spawn", status: "bound",
    chords: [{ mod: true, keys: ["Enter"] }] },
  { id: "ui.escape", label: "Close a dialog, menu or page", group: "General", scope: "global", status: "help-only",
    chords: [], text: { mac: "Esc", other: "Esc" } },
  { id: "ui.lists", label: "Move and pick in lists", group: "General", scope: "global", status: "help-only",
    chords: [], text: { mac: "↑ ↓ ↵", other: "↑ ↓ Enter" } },
  { id: "viewer.find", label: "Find in file", group: "Editor & files", scope: "viewer", status: "bound",
    chords: [{ mod: true, code: "KeyF" }] },
  { id: "viewer.gotoLine", label: "Go to line", group: "Editor & files", scope: "viewer", status: "bound",
    chords: [{ ctrl: true, code: "KeyG" }] },
];

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

// chordKeys: the tokens of a chord in the order renderChord writes them, one per key cap. Off the Mac
// ["Ctrl", "Shift", "K"]; on it the glyphs ["⇧", "⌘", "K"]. renderChord joins them; the desktop's
// Keyboard shortcuts dialog draws one chip per token.
function chordKeys(chord, mac) {
  const key = scKeyName(chord);
  const ctrl = !!chord.ctrl || (!!chord.mod && !mac);
  const cmd = !!chord.mod && !!mac;
  if (mac) return [ctrl ? "⌃" : "", chord.alt ? "⌥" : "", chord.shift ? "⇧" : "", cmd ? "⌘" : "", key].filter(Boolean);
  return [ctrl ? "Ctrl" : "", chord.alt ? "Alt" : "", chord.shift ? "Shift" : "", key].filter(Boolean);
}

function renderChord(chord, mac) {
  return chordKeys(chord, mac).join(mac ? "" : "+");
}

function scPrimary(entry, mac) {
  return entry.chords.filter((c) => scOnHere(c, mac));
}

function activeChords(entry, mac) {
  return scPrimary(entry, mac);
}

function shortcutHint(id, mac) {
  const entry = scEntry(id);
  const first = entry && scPrimary(entry, mac)[0];
  return first ? renderChord(first, mac) : "";
}

// hintTitle: a button's tooltip and accessible name, "Command palette (Ctrl+Shift+K)":
// its own label and, in brackets, the chord the registry binds to `id` on this
// platform. A label alone when the id has no chord here.
function hintTitle(label, id, mac) {
  const chord = shortcutHint(id, mac);
  return chord ? label + " (" + chord + ")" : label;
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

// skip(id), optional: true leaves the key to whoever owns it where focus is (Monaco's own find and go to line,
// monacokeys.js registrySkipsInEditor), without stopping it or marking it used.
function dispatchShortcut(e, ctx, mac, skip) {
  const id = shortcutFor(e, mac, ctx);
  if (id && typeof skip === "function" && skip(id)) return false;
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
      : scPrimary(s, mac).map((c) => renderChord(c, mac)).join(", ");
    rows.push({ group: s.group, keys, label: s.label });
  }
  return rows;
}

// The Keyboard shortcuts dialog's two columns on the desktop (frame M5.3): the groups of each, top to bottom.
// A group not named here follows in the last column, so a new group is never dropped.
const SC_GRID_COLUMNS = [["General", "Terminal"], ["Sessions & panes", "Editor & files"]];

// scHelpTokens: a help-only text ("Shift+drag", "↑ ↓ Enter") as chips and words. Keys are written capitalised or
// as a glyph; a lower-case word ("drag") is plain text beside them.
function scHelpTokens(text) {
  return String(text).split(/[\s+]+/).filter(Boolean).map((t) => (/^[a-z]+$/.test(t) ? { text: t } : { kbd: t }));
}

// helpGrid(mac) -> [[{ group, rows: [{ label, alts }] }, ...], [...]]: the same shortcuts as helpRows, as key chips
// for the desktop dialog. `alts` is one entry per way to do it (the dialog puts "or" between them), each a list of
// { kbd } chips and { text } words. The label is deskLabel when the row has one.
function helpGrid(mac) {
  const byGroup = new Map();
  for (const s of SHORTCUTS) {
    if (s.status === "reserved") continue;
    const alts = s.status === "help-only"
      ? [scHelpTokens(s.text[mac ? "mac" : "other"])]
      : scPrimary(s, mac).map((c) => chordKeys(c, mac).map((k) => ({ kbd: k })));
    if (!byGroup.has(s.group)) byGroup.set(s.group, []);
    byGroup.get(s.group).push({ label: s.deskLabel || s.label, alts });
  }
  const cols = SC_GRID_COLUMNS.map((names) => names.filter((g) => byGroup.has(g)).map((g) => ({ group: g, rows: byGroup.get(g) })));
  for (const [g, rows] of byGroup) {
    if (!SC_GRID_COLUMNS.some((names) => names.includes(g))) cols[cols.length - 1].push({ group: g, rows });
  }
  return cols;
}

// The order the help screen lists its groups in. A group not named here (none
// today) follows these, in the order its first row appears.
const SC_GROUP_ORDER = ["General", "Sessions & panes", "Terminal", "Editor & files"];

function helpText(rows, runtimeLabel) {
  const w = Math.max(0, ...rows.map((r) => r.keys.length));
  const groups = SC_GROUP_ORDER.filter((g) => rows.some((r) => r.group === g));
  for (const r of rows) if (!groups.includes(r.group)) groups.push(r.group);
  const blocks = groups.map((g) => [g].concat(
    rows.filter((r) => r.group === g).map((r) => r.keys.padEnd(w) + "  " + r.label),
  ).join("\n"));
  return blocks.join("\n\n") + (runtimeLabel ? "\n\nRuntime: " + runtimeLabel : "");
}

// helpTextStacked: the phone's version of helpText (#59). The aligned key column
// is up to 64 characters wide, more than a 390 px dialog fits (322 px of 12 px
// mono, 7.22 px per character = 44), so each row is its keys on one line and its
// description on the next, indented two spaces. Same groups, same order, same
// trailing Runtime line; showShortcuts picks it on the phone layout (app.js).
function helpTextStacked(rows, runtimeLabel) {
  const groups = SC_GROUP_ORDER.filter((g) => rows.some((r) => r.group === g));
  for (const r of rows) if (!groups.includes(r.group)) groups.push(r.group);
  const blocks = groups.map((g) => [g].concat(
    ...rows.filter((r) => r.group === g).map((r) => [r.keys, "  " + r.label]),
  ).join("\n"));
  return blocks.join("\n\n") + (runtimeLabel ? "\n\nRuntime: " + runtimeLabel : "");
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    isMacPlatform, IS_MAC, SHORTCUTS, chordMatches, chordKeys, renderChord, activeChords, shortcutHint, hintTitle, jumpHint,
    shortcutMatches, shortcutFor, bindShortcut, dispatchShortcut, _resetShortcutBindings, cmKeys, helpRows, helpText,
    helpTextStacked, helpGrid,
  };
}
