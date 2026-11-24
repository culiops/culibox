"use strict";
// The concept -> glyph maps of #168, pinned as literal expected values copied from the design kit's
// GLYPHS.md (docs/superpowers is absent in CI, so the table lives here). One concept, one glyph.
const test = require("node:test");
const assert = require("node:assert/strict");
const maps = require("../static/iconmap.js");
const { ICON_LOCKED, ICON_ACTION, ICON_NAV, ICON_STATUS, ICON_RESOURCE, ICON_MENU, iconOf } = maps;

test('ICON_LOCKED equals GLYPHS.md "Locked by earlier shipped work"', () => {
  assert.deepStrictEqual({ ...ICON_LOCKED }, {
    "agent-claude": "si:claude",
    shell: "terminal",
    "tier-box": "box",
    "tier-clone": "copy",
    "tier-host": "triangle-alert",
    folder: "folder",
    "folder-open": "folder-open",
    expand: "chevron-right",
    collapse: "chevron-down",
    new: "plus",
  });
});

test('ICON_ACTION equals GLYPHS.md "Shared actions"', () => {
  assert.deepStrictEqual({ ...ICON_ACTION }, {
    add: "plus",
    edit: "pencil",
    rename: "pencil-line",
    delete: "trash-2",
    "copy-text": "clipboard-copy",
    download: "download",
    upload: "upload",
    refresh: "refresh-cw",
    "open-new-tab": "external-link",
    filter: "list-filter",
    search: "search",
    close: "x",
    more: "ellipsis",
  });
  assert.notEqual(ICON_ACTION["copy-text"], "copy");
});

test('ICON_NAV equals GLYPHS.md "Navigation"', () => {
  assert.deepStrictEqual({ ...ICON_NAV }, {
    audit: "scroll-text",
    analytics: "chart-no-axes-column",
    settings: "settings",
    palette: "search",
    files: "folder-tree",
    activity: "activity",
    access: "shield",
  });
});

test('ICON_STATUS equals GLYPHS.md "Status"', () => {
  assert.deepStrictEqual({ ...ICON_STATUS }, {
    info: "info",
    warning: "circle-alert",
    error: "circle-x",
    success: "circle-check",
    refused: "ban",
    pending: "loader-circle",
    locked: "lock",
    "signed-out": "log-in",
    unreachable: "wifi-off",
  });
  assert.notEqual(ICON_STATUS.warning, "triangle-alert");
});

test('ICON_RESOURCE equals GLYPHS.md "Resources and credentials"', () => {
  assert.deepStrictEqual({ ...ICON_RESOURCE }, {
    aws: "cloud",
    aws_legacy: "cloud",
    github: "si:github",
    token: "key-round",
    domain: "globe",
    folder: "folder",
    files: "files",
    credential: "key-round",
    resource: "circle-dashed",
  });
});

test("every map is frozen and prototype-free", () => {
  const names = Object.keys(maps).filter((k) => k.startsWith("ICON_"));
  assert.ok(names.length >= 10, "the S1 maps: " + names.join(","));
  for (const n of names) {
    assert.ok(Object.isFrozen(maps[n]), n + " is frozen");
    assert.equal(Object.getPrototypeOf(maps[n]), null, n + " has no prototype");
  }
});

test("iconOf ignores prototype keys", () => {
  assert.equal(iconOf(ICON_ACTION, "constructor"), "");
  assert.equal(iconOf(ICON_ACTION, "__proto__", "x"), "x");
  assert.equal(iconOf(ICON_ACTION, "close"), "x");
  assert.equal(iconOf(ICON_ACTION, "nope", "fb"), "fb");
});

test("one concept one glyph: tier glyphs are not reused", () => {
  for (const m of [ICON_ACTION, ICON_MENU, ICON_STATUS]) {
    for (const v of Object.values(m)) assert.ok(!["copy", "triangle-alert", "box"].includes(v), v);
  }
});
