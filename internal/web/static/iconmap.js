"use strict";
// The concept -> glyph maps of #168. A key is a concept word, a value a name in ICON_SET (icons.js, generated):
// a Lucide name, or "si:<slug>" for a Simple Icons mark. One concept, one glyph: the tier glyphs (box, copy,
// triangle-alert) belong to the tier chips only, so no action or status map may reuse them. The first five
// tables are the design kit's GLYPHS.md verbatim (internal/web/jstest/iconmap.test.js pins them); the rest are
// the per-surface migration maps, whose values come from the slice legends. Entries marked (port) were
// picked at port time where no legend covers the control; they match what the control draws today.
// Every map is frozen and has no prototype, so a key such as "constructor" never matches.
// Classic script: the browser shares one global scope; under node the names are exported below.

function iconMapOf(pairs) {
  return Object.freeze(Object.assign(Object.create(null), pairs));
}

// iconOf: the glyph name for `key` when it is an own key of `map`, else `fallback` ("" when none is given).
function iconOf(map, key, fallback) {
  return Object.prototype.hasOwnProperty.call(map, key) ? map[key] : fallback === undefined ? "" : fallback;
}

// GLYPHS.md "Locked by earlier shipped work".
const ICON_LOCKED = iconMapOf({
  "agent-claude": "si:claude", shell: "terminal", "tier-box": "box", "tier-clone": "copy", "tier-host": "triangle-alert",
  folder: "folder", "folder-open": "folder-open", expand: "chevron-right", collapse: "chevron-down", new: "plus",
});

// GLYPHS.md "Shared actions". Copy to clipboard is clipboard-copy, never copy (the clone tier).
const ICON_ACTION = iconMapOf({
  add: "plus", edit: "pencil", rename: "pencil-line", delete: "trash-2", "copy-text": "clipboard-copy",
  download: "download", upload: "upload", refresh: "refresh-cw", "open-new-tab": "external-link",
  filter: "list-filter", search: "search", close: "x", more: "ellipsis",
});

// GLYPHS.md "Navigation".
const ICON_NAV = iconMapOf({
  audit: "scroll-text", analytics: "chart-no-axes-column", settings: "settings", palette: "search",
  files: "folder-tree", activity: "activity", access: "shield",
});

// GLYPHS.md "Status". Warning is circle-alert, never triangle-alert (the host tier).
const ICON_STATUS = iconMapOf({
  info: "info", warning: "circle-alert", error: "circle-x", success: "circle-check", refused: "ban",
  pending: "loader-circle", locked: "lock", "signed-out": "log-in", unreachable: "wifi-off",
});

// GLYPHS.md "Resources and credentials" (declared kinds only; credential and resource are the fallbacks).
const ICON_RESOURCE = iconMapOf({
  aws: "cloud", aws_legacy: "cloud", github: "si:github", token: "key-round", domain: "globe", folder: "folder",
  files: "files", credential: "key-round", resource: "circle-dashed",
});

// The tabs of a pane (G4 18-21).
const ICON_PANE_TAB = iconMapOf({ terminal: "square-terminal", activity: "activity", access: "shield", files: "folder-tree" });

// The kit menu's items (G4 50-61). Port picks: interrupt, bringhome, insert (G4 left-out `r` and today's drawing).
const ICON_MENU = iconMapOf({
  rename: "pencil-line", edit: "pencil", files: "folder-tree", attribution: "chart-pie", copy: "clipboard-copy",
  split: "columns-2", duplicate: "copy-plus", end: "power", delete: "trash-2", interrupt: "pause",
  bringhome: "arrow-down-to-line", check: "check", download: "download", upload: "upload", refresh: "refresh-cw",
  collapse: "chevrons-down-up", insert: "arrow-right-to-line", "folder-plus": "folder-plus",
});

// Where the shell bar docks a terminal. No legend covers it: the picks (port) match today's drawings (tabs.js).
const ICON_DOCK_PLACE = iconMapOf({ below: "panel-bottom", right: "panel-right", own: "square-stack", hide: "x" });

// The Files dock's controls (G5 5-18; G5 governs the dock). Swipe and onion are the image comparer's two other
// modes, which no legend covers: the picks (port) keep what the controls draw today, a split frame and stacked layers.
const ICON_DOCK_VIEW = iconMapOf({
  preview: "file-text", code: "code", changes: "diff", image: "image", inline: "square-equal", side: "columns-2",
  follow: "eye", prev: "chevron-up", next: "chevron-down", caret: "chevron-down", blame: "git-commit-horizontal",
  more: "ellipsis", swipe: "square-split-horizontal", onion: "layers",
});

// The layout picker, by pane count (G4 1; 1 and 4 match the shapes drawn today, sidechrome.js).
const ICON_LAYOUT = iconMapOf({ 1: "square", 2: "columns-2", 4: "grid-2x2" });

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    iconOf, ICON_LOCKED, ICON_ACTION, ICON_NAV, ICON_STATUS, ICON_RESOURCE,
    ICON_PANE_TAB, ICON_MENU, ICON_DOCK_PLACE, ICON_DOCK_VIEW, ICON_LAYOUT,
  };
}
