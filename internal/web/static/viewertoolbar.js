"use strict";

// One toolbar for every file viewer: the pane's and the Files drawer's (D11).
//
// viewerToolbarModel is pure (node-tested): the viewer's state in, which control
// is shown / enabled / labelled / titled out. buildViewerToolbar builds the DOM
// from that model and repaints it. Nothing here knows which host it sits in; the
// host supplies only what differs ({extras, pathText}).
//
// Primary row: Edit (Save Cancel while editing) · Copy · Path ▾ · Find · [host
// extras] · ⋯ · path label · status. The Path ▾ and ⋯ menus hold the rest.
//
// Words, not glyphs, on every button except ⋯ and the ▾ (an unprobed codepoint
// ships as a tofu box no test catches); the glyph buttons carry an aria-label.

// The shape of one entry: {shown, enabled, label, title, pressed?}. `title` is
// the tooltip and, on a disabled control, the reason.
//
// s = {path, image, box, readOnly, editing, dirty, markdown, auto, wrap,
//      lines,            // are there numbered lines to jump to (read view or editor)
//      insert, reveal, rel, // {ok, title} from viewerpath.js; a boolean is accepted
//      extras: [{key, label, title}],   // host buttons, between Find and ⋯
//      keys: {find, gotoLine, save, zoomIn, zoomOut}}  // shortcutHint() strings, or absent
function viewerToolbarModel(s) {
  s = s || {};
  const keys = s.keys || {};
  const hint = (id) => (keys[id] ? " (" + keys[id] + ")" : "");
  const image = !!s.image;
  const editing = !!s.editing;
  const dirtyEdit = editing && !!s.dirty;
  const verdict = (v, why) => (v && typeof v === "object" ? { ok: !!v.ok, title: v.title || why } : { ok: v !== false, title: why });
  const insert = verdict(s.insert, "There is no prompt to insert at");
  const reveal = verdict(s.reveal, "This file cannot be shown in the tree");
  const rel = verdict(s.rel, "This file has no path relative to the tree");
  const ent = (shown, enabled, label, title, pressed) => {
    const e = { shown, enabled, label, title };
    if (pressed !== undefined) e.pressed = pressed;
    return e;
  };
  const m = {};

  m.edit = ent(!editing && !image && !s.box && !s.readOnly, true, "Edit", "Edit this file");
  m.save = ent(editing, true, "Save", "Save" + hint("save"));
  m.cancel = ent(editing, true, "Cancel", "Discard changes and return to the read view");
  m.copy = ent(true, !image, "Copy", image ? "An image has no text to copy" : "Copy this file's contents");
  m.path = ent(true, true, "Path ▾", "Copy, insert or reveal this file's path");
  m.find = ent(true, !image, "Find", image ? "An image has no text to search" : "Find in this file" + hint("find"));
  for (const x of Array.isArray(s.extras) ? s.extras : []) {
    if (!x || !x.key) continue;
    m[x.key] = ent(true, !editing, x.label || x.key, editing ? "Save or cancel your edit first" : (x.title || x.label || x.key));
  }
  m.more = ent(true, true, "⋯", "More: reload, download, follow, wrap, go to line, text size");

  m.pathAbs = ent(true, true, "Copy absolute path", "Copy this file's full path");
  m.pathRel = ent(true, rel.ok, "Copy relative path", rel.ok ? "Copy the path relative to the tree's root" : rel.title);
  m.pathInsert = ent(true, insert.ok, "Insert path at prompt", insert.ok ? "Type this file's path at the prompt, followed by a space" : insert.title);
  m.reveal = ent(true, reveal.ok, "Reveal in tree", reveal.ok ? "Show this file in the file tree" : reveal.title);

  const SAVE_FIRST = "Save or cancel your edits first";
  m.reload = ent(true, !dirtyEdit, "Reload", dirtyEdit ? SAVE_FIRST : "Re-read this file from disk");
  m.download = ent(true, true, "Download", "Download this file");
  m.follow = ent(true, !image && !dirtyEdit, "Follow file",
    image ? "An image is not re-read automatically" : dirtyEdit ? SAVE_FIRST : "Re-read the file every 2 seconds while it is open", !!s.auto);
  m.wrap = ent(true, !image, "Wrap lines", image ? "An image has no lines to wrap" : "Wrap long lines (remembered in this browser)", !!s.wrap);
  const noLines = s.lines === false;
  m.gotoLine = ent(true, !image && !s.markdown && !noLines, "Go to line…",
    image ? "An image has no lines" :
    s.markdown ? "Markdown is shown rendered — no line numbers" :
    noLines ? "There are no numbered lines to go to" : "Go to a line number" + hint("gotoLine"));
  m.smaller = ent(true, !image, "A−", image ? "An image has no text to resize" : "Smaller text" + hint("zoomOut"));
  m.bigger = ent(true, !image, "A+", image ? "An image has no text to resize" : "Bigger text" + hint("zoomIn"));
  return m;
}

// --- DOM ----------------------------------------------------------------------

// Accessible names. A visible word is part of its name (WCAG label-in-name);
// the glyph-only buttons get a spoken one.
const VT_ARIA = {
  path: "Path actions", more: "More file actions", smaller: "Smaller text", bigger: "Bigger text",
};
const VT_PATH_ITEMS = ["pathAbs", "pathRel", "pathInsert", "reveal"];
const VT_MORE_ITEMS = ["reload", "download", "follow", "wrap", "gotoLine"];
const VT_CHECKS = new Set(["follow", "wrap"]);

// One menu open at a time across every toolbar (pane and drawer): the open
// menu's closer, or null.
let vtOpen = null;

// vtMenuOpen: is a toolbar menu open? app.js's Escape handler asks, so a press
// that closes a menu does not also restore a maximized drawer.
function vtMenuOpen() { return !!vtOpen; }
function vtCloseMenus() { if (vtOpen) vtOpen.close(); }

if (typeof document !== "undefined") {
  // An outside press closes the menu. Capture phase on pointerdown, so a handler
  // that stops the click (the line-number menu does) cannot keep it open.
  document.addEventListener("pointerdown", (e) => {
    if (vtOpen && !vtOpen.contains(e.target)) vtOpen.close();
  }, true);
  // Esc closes it, and preventDefault marks the press as used: the maximized
  // drawer's own Escape (app.js) leaves a used press alone.
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && vtOpen) { e.preventDefault(); vtOpen.close(true); }
  });
  window.addEventListener("resize", vtCloseMenus);
}

// buildViewerToolbar(viewer, host) -> {el, paint(), closeMenus()}.
// host = {extras: [{key, label, title, run}], pathText(viewer) -> string} (both optional).
function buildViewerToolbar(viewer, host) {
  host = host || {};
  const extras = Array.isArray(host.extras) ? host.extras : [];
  const el = document.createElement("div");
  el.className = "viewer-tools";
  el.setAttribute("role", "toolbar");
  el.setAttribute("aria-label", "File actions");

  const keys = {
    find: shortcutHint("viewer.find", IS_MAC), gotoLine: shortcutHint("viewer.gotoLine", IS_MAC),
    save: shortcutHint("editor.save", IS_MAC), zoomIn: shortcutHint("zoom.in", IS_MAC), zoomOut: shortcutHint("zoom.out", IS_MAC),
  };

  const mkBtn = (key, cls) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "vt-btn" + (cls ? " " + cls : "");
    b.dataset.k = key;
    b.setAttribute("aria-label", VT_ARIA[key] || "");
    return b;
  };
  const btns = {};
  const act = {}; // key -> handler; a disabled control does nothing
  const rowKeys = ["edit", "save", "cancel", "copy", "path", "find", ...extras.map((x) => x.key), "more"];
  for (const k of rowKeys) {
    btns[k] = mkBtn(k, k === "save" ? "pri" : "");
    el.append(btns[k]);
  }
  btns.path.setAttribute("aria-haspopup", "menu");
  btns.path.setAttribute("aria-expanded", "false");
  btns.more.setAttribute("aria-haspopup", "menu");
  btns.more.setAttribute("aria-expanded", "false");
  btns.more.classList.add("vt-more");

  const pathLbl = document.createElement("span");
  pathLbl.className = "viewer-path vt-path";
  const stat = document.createElement("span");
  stat.className = "vt-status";
  stat.setAttribute("role", "status");
  el.append(pathLbl, stat);

  act.edit = () => viewer.enterEdit();
  act.save = () => viewer.save(false);
  act.cancel = () => viewer.cancelEdit();
  act.copy = () => viewer.copy(btns.copy);
  act.find = () => viewer.openFind("find");
  for (const x of extras) act[x.key] = () => x.run();

  // --- menus -----------------------------------------------------------------
  const items = {};
  const mkItem = (key, role) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "vt-mi";
    b.dataset.k = key;
    b.setAttribute("role", role || "menuitem");
    if (role === "menuitemcheckbox") b.setAttribute("aria-checked", "false");
    items[key] = b;
    return b;
  };
  const menus = {};
  const mkMenu = (name, label, children) => {
    const m = document.createElement("div");
    m.className = "vt-menu";
    m.setAttribute("role", "menu");
    m.setAttribute("aria-label", label);
    m.append(...children);
    menus[name] = m;
    return m;
  };
  mkMenu("path", "Path actions", VT_PATH_ITEMS.map((k) => mkItem(k)));
  // Text size: one row, so A− and A+ can be pressed in turn with the menu staying open.
  const sizeRow = document.createElement("div");
  sizeRow.className = "vt-size";
  sizeRow.setAttribute("role", "group");
  sizeRow.setAttribute("aria-label", "Text size");
  const sizeLbl = document.createElement("span");
  sizeLbl.className = "vt-size-lbl";
  sizeLbl.textContent = "Text size";
  sizeRow.append(sizeLbl, mkItem("smaller"), mkItem("bigger"));
  items.smaller.setAttribute("aria-label", VT_ARIA.smaller);
  items.bigger.setAttribute("aria-label", VT_ARIA.bigger);
  mkMenu("more", "More file actions", [...VT_MORE_ITEMS.map((k) => mkItem(k, VT_CHECKS.has(k) ? "menuitemcheckbox" : "menuitem")), sizeRow]);

  act.pathAbs = () => viewer.copyPath("abs");
  act.pathRel = () => viewer.copyPath("rel");
  act.pathInsert = () => viewer.insertPath();
  act.reveal = () => viewer.revealInTree();
  act.reload = () => viewer.reload();
  act.download = () => viewer.download();
  act.follow = () => viewer.toggleAuto();
  act.wrap = () => toggleViewerWrap();
  act.gotoLine = () => viewer.openFind("line");
  act.smaller = () => viewer.fontStep(-1);
  act.bigger = () => viewer.fontStep(+1);
  const STAYS = new Set(["smaller", "bigger"]);

  // closeMenus closes whichever of this toolbar's menus is open.
  const closeMenus = () => { if (vtOpen && (vtOpen.owner === el)) vtOpen.close(); };

  const openMenu = (name, trigger) => {
    if (vtOpen && vtOpen.owner === el && vtOpen.name === name) { vtOpen.close(true); return; }
    vtCloseMenus();
    if (typeof closeRowMenu === "function") closeRowMenu(); // the file tree's row menu is another popup
    paint(); // items reflect right now: dirty, lines, wrap, follow, the insert target
    const menu = menus[name];
    document.body.append(menu);
    menu.style.visibility = "hidden";
    const r = trigger.getBoundingClientRect();
    const w = menu.offsetWidth;
    const h = menu.offsetHeight;
    menu.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, r.left)) + "px";
    const below = r.bottom + 4;
    menu.style.top = (below + h <= window.innerHeight - 8 ? below : Math.max(8, r.top - h - 4)) + "px";
    menu.style.visibility = "";
    trigger.setAttribute("aria-expanded", "true");
    const closer = {
      owner: el, name,
      contains: (n) => menu.contains(n) || trigger.contains(n),
      close: (refocus) => {
        if (vtOpen === closer) vtOpen = null;
        menu.remove();
        trigger.setAttribute("aria-expanded", "false");
        if (refocus === true && trigger.isConnected) trigger.focus();
      },
    };
    vtOpen = closer;
    const first = menu.querySelector('[role^="menuitem"]:not([aria-disabled="true"])');
    if (first) first.focus();
  };
  btns.path.onclick = () => openMenu("path", btns.path);
  btns.more.onclick = () => openMenu("more", btns.more);

  for (const name of ["path", "more"]) {
    menus[name].addEventListener("keydown", (e) => {
      // Disabled items stay reachable (WAI-ARIA menu practice) so a keyboard user can
      // land on one and hear its title, the reason; activating it does nothing.
      const live = [...menus[name].querySelectorAll('[role^="menuitem"]')];
      if (!live.length) return;
      const i = live.indexOf(document.activeElement);
      let to = null;
      if (e.key === "ArrowDown") to = live[(i + 1) % live.length];
      else if (e.key === "ArrowUp") to = live[(i - 1 + live.length) % live.length];
      else if (e.key === "Home") to = live[0];
      else if (e.key === "End") to = live[live.length - 1];
      else if (e.key === "Tab") { vtCloseMenus(); return; }
      if (to) { e.preventDefault(); to.focus(); }
    });
  }

  // run: a disabled control does nothing (aria-disabled, not `disabled`, so its
  // title still shows the reason and it stays reachable by keyboard).
  const run = (key, fromMenu, trigger) => {
    const b = btns[key] || items[key];
    if (b.getAttribute("aria-disabled") === "true") return;
    if (fromMenu && !STAYS.has(key)) vtCloseMenus();
    act[key]();
    if (fromMenu && !STAYS.has(key) && trigger.isConnected &&
        (!document.activeElement || document.activeElement === document.body)) trigger.focus();
  };
  for (const k of rowKeys) if (k !== "path" && k !== "more") btns[k].onclick = () => run(k, false);
  for (const k of VT_PATH_ITEMS) items[k].onclick = (e) => { e.stopPropagation(); run(k, true, btns.path); };
  for (const k of [...VT_MORE_ITEMS, "smaller", "bigger"]) items[k].onclick = (e) => { e.stopPropagation(); run(k, true, btns.more); };

  // --- paint -----------------------------------------------------------------
  const apply = (b, e, aria) => {
    if (b.textContent !== e.label) b.textContent = e.label;
    b.hidden = !e.shown;
    b.title = e.title;
    b.setAttribute("aria-label", aria);
    b.setAttribute("aria-disabled", e.enabled ? "false" : "true");
    b.classList.toggle("vt-off", !e.enabled);
    if (e.pressed !== undefined) {
      b.setAttribute("aria-checked", e.pressed ? "true" : "false");
      b.classList.toggle("on", e.pressed);
    }
  };

  function state() {
    const home = typeof window !== "undefined" ? window.HOME_DIR || "" : "";
    const root = typeof ftRoot === "function" ? ftRoot() : "";
    return {
      path: viewer.path,
      image: !!viewer.isImage,
      box: !!viewer.box,
      readOnly: !!viewer.readOnly,
      editing: !!viewer.editing,
      dirty: !!(viewer.editing && viewer.dirty()),
      markdown: MD_EXTS.has(extOf(viewer.path)),
      auto: !!viewer.auto,
      wrap: isViewerWrap(),
      lines: !!viewer.hasLines || !!(viewer.editing && viewer.cm),
      insert: insertTargetFor(viewer, viewer.insertCandidate()),
      reveal: revealable(viewer, home),
      rel: relativeAvail(viewer, root),
      extras: extras.map((x) => ({ key: x.key, label: x.label, title: x.title })),
      keys,
    };
  }

  function paint() {
    const m = viewerToolbarModel(state());
    for (const k of rowKeys) apply(btns[k], m[k], VT_ARIA[k] || m[k].label);
    for (const k of [...VT_PATH_ITEMS, ...VT_MORE_ITEMS, "smaller", "bigger"]) {
      apply(items[k], m[k], k === "smaller" || k === "bigger" ? VT_ARIA[k] : m[k].label);
    }
    const label = typeof host.pathText === "function" ? host.pathText(viewer) : viewer.path;
    if (pathLbl.textContent !== label) pathLbl.textContent = label;
    pathLbl.title = viewer.path;
    const st = viewer.statusText ? "· " + viewer.statusText : "";
    if (stat.textContent !== st) stat.textContent = st;
  }

  paint();
  return { el, paint, closeMenus };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { viewerToolbarModel };
}
