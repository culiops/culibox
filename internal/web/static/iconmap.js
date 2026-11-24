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

// The New session reach box's label column (G1 22-28), keyed by the row's declared `id` (reachRows). Files, Original, Work home,
// Network and Account stay plain (G1 left-outs w, x, y, z), so they have no entry.
const ICON_REACH = iconMapOf({
  aws: "cloud", github: "si:github", tokens: "key-round", reads: "folder", imports: "files", domains: "globe", store: "lock",
});

// The tabs of a pane (G4 18-21).
const ICON_PANE_TAB = iconMapOf({ terminal: "square-terminal", activity: "activity", access: "shield", files: "folder-tree" });

// The pane's maximize toggle (G4 25): the dock's Full button draws maximize too, one concept for pane and dock.
const ICON_PANE_CTL = iconMapOf({ maximize: "maximize", restore: "minimize" });

// The kit menu's items (G4 50-61). Port picks: interrupt, bringhome, insert (G4 left-out `r` and today's drawing).
const ICON_MENU = iconMapOf({
  rename: "pencil-line", edit: "pencil", files: "folder-tree", attribution: "chart-pie", copy: "clipboard-copy",
  split: "columns-2", duplicate: "copy-plus", end: "power", delete: "trash-2", interrupt: "pause",
  bringhome: "arrow-down-to-line", check: "check", download: "download", upload: "upload", refresh: "refresh-cw",
  collapse: "chevrons-down-up", insert: "arrow-right-to-line", "folder-plus": "folder-plus",
  restart: "rotate-cw", "replies-on": "message-square-reply", "replies-off": "message-square-off",
  // The Credentials page's Add credential menu (G1 18-20), by the row's declared kind (credAddItems): one concept, one glyph.
  aws: ICON_RESOURCE.aws, github: ICON_RESOURCE.github, token: ICON_RESOURCE.token,
});

// A Credentials token section's provider mark (G1 15, 17), keyed by the catalog template id a token entry declares
// (internal/credtemplates/catalog.go). The provider field is free text the user types and never picks a mark; `other`
// has no entry, so a section of it (or of a token an older server sent with no template) draws ICON_RESOURCE.token.
const ICON_TOKEN_TEMPLATE = iconMapOf({
  "github-pat": "si:github", sentry: "si:sentry", grafana: "si:grafana", datadog: "si:datadog", pagerduty: "si:pagerduty",
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

// The layout picker, by pane count (G4 1; 1 matches the shape drawn today, sidechrome.js; 4 follows the approved G4 legend n1).
const ICON_LAYOUT = iconMapOf({ 1: "square", 2: "columns-2", 4: "layout-grid" });

// The system banner's glyphs by banner kind (notifymodel.js sysBannerModel): both sit after the dot in the title
// (the action buttons stay words). Paused and updated have no drawn glyph (G4's left-out `p`
// was a proposal, not drawn), so they are not keys.
const ICON_SYS_BANNER = iconMapOf({ unreachable: ICON_STATUS.unreachable, signedout: ICON_STATUS["signed-out"] });

// The desktop toast stack's status glyph by toast kind (G4 80, 81): warn and error carry their status glyph after the kind dot.
// Info (a confirmation, the founder's call) stays plain and pending keeps the CSS spinner, so neither is a key.
const ICON_TOAST = iconMapOf({ warn: ICON_STATUS.warning, error: ICON_STATUS.error });

// The desktop sidebar row's own marks (G4 12): a companion shell's arrow under its parent, drawn at chip size 12.
const ICON_ROW = iconMapOf({ nested: "corner-down-right" });

// The command palette's result-type glyph by item.kind (G4 70-76): one glyph per KIND, never per command (G4 left-out
// `n`). Session rows take their glyph from the declared agentKind instead (ICON_LOCKED "agent-claude" and "shell"), and
// a session of no known kind draws none, so its activity dot stands alone. layout is the layout picker's four-pane glyph
// (ICON_LAYOUT[4]): one concept, one glyph.
const ICON_PALETTE_KIND = iconMapOf({
  new: ICON_LOCKED.new, "session-action": "arrow-right", view: "arrow-right", kill: ICON_MENU.end, layout: ICON_LAYOUT[4],
  settings: ICON_NAV.settings, file: "file",
});

// The four empty states (G4 90, 91, 78), by the kind the CALLER is rendering, never by the message text: none = no session
// open, first = the first-run welcome (both full-surface: a 32 px hero), changes = the dock Changes section of a clean
// workspace, matches = the palette with no match (both list-level: 14 px inline). `first` is a port pick: no frame draws
// the first-run screen, so it takes the tier-box glyph's own `box`, the product's mark of a container.
const ICON_EMPTY = iconMapOf({ none: "app-window", first: "box", changes: "git-compare", matches: "search-x" });
const EMPTY_HERO = iconMapOf({ none: true, first: true, changes: false, matches: false });

// emptyState(kind): { icon, hero } for a declared kind, null for any other (a prototype key included). `hero` true = the
// 32 px muted block glyph above a title; false = the 14 px muted glyph before the line of text.
function emptyState(kind) {
  if (!Object.prototype.hasOwnProperty.call(ICON_EMPTY, kind)) return null;
  return { icon: ICON_EMPTY[kind], hero: EMPTY_HERO[kind] };
}

// The Settings nav's glyph by the tab's declared data-tab (G2 1-9), drawn at 15 before the row's words; Sign out takes
// log-out beside it. index.html carries the placeholders (data-uicon): this map is the pinned record of them, and a test
// checks the two agree. VM folders is the macOS-only tab, hidden elsewhere.
const ICON_SETTINGS_TAB = iconMapOf({
  manage: "id-card", accounts: "circle-user-round", creds: "key-round", vmfolders: "hard-drive", wsroots: "folders",
  tokens: "monitor-smartphone", settings: "sliders-horizontal", linktrust: "link", about: "info",
});

// The agent account row's provider mark by its declared `provider` (G2 13; Go account.ProviderClaude). A provider with no
// entry draws no mark and the word stays: a second provider gets its own here, never a generic one.
const ICON_AGENT_PROVIDER = iconMapOf({ claude: "si:claude" });

// The Audit row's Kind glyph by the declared `kind` (G2 14-19, drawn at 14 before the word; the Credentials, Elevations, Egress
// and Settings filters take the same glyph). A credential row takes its provider's glyph instead (G2 14, ICON_CRED_PROVIDER;
// Activity's credential rows read it too); a kind with no entry draws none.
const ICON_AUDIT_KIND = iconMapOf({
  credential: "key-round", elevation: "shield-alert", signin: "log-in", egress: "globe", settings: "settings", store: "vault",
});

// A credential row's glyph by the broker's declared `provider` (aws | github | static). A row with none (an older server) or an
// unknown one falls back to key-round at the call site.
const ICON_CRED_PROVIDER = iconMapOf({ aws: "cloud", github: "si:github", static: "key-round" });

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    ICON_AUDIT_KIND, ICON_CRED_PROVIDER,
    emptyState, ICON_EMPTY, ICON_SETTINGS_TAB, ICON_AGENT_PROVIDER,
    iconOf, ICON_LOCKED, ICON_ACTION, ICON_NAV, ICON_STATUS, ICON_RESOURCE, ICON_REACH,
    ICON_PANE_TAB, ICON_PANE_CTL, ICON_MENU, ICON_TOKEN_TEMPLATE, ICON_DOCK_PLACE, ICON_DOCK_VIEW, ICON_LAYOUT, ICON_SYS_BANNER, ICON_TOAST, ICON_ROW, ICON_PALETTE_KIND,
  };
}
