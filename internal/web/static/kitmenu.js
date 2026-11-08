// The kit menu's builders (frame M5.1): the desktop menus (the session row's ⋯, the pane header's tab
// switcher, the profile page's ⋯ and "+ Add" pickers, the file tree's row menu) are all `.menu-item` rows
// built here, so their look, icons and arrow keys are one component. Classic script, loaded before tabs.js
// (menuIconSvg reuses tabs.js's tabIcon and dockIcon, which only run once the page has loaded them all).

// menuNavIndex: where the arrow keys move focus inside the open menu. `count` rows, focus on row `at`
// (-1 when it is on none of them, e.g. a tab switcher's close button). Down and Up wrap; from outside the
// rows Down lands on the first and Up on the last; Home and End jump. null means the key is not the menu's,
// so the caller leaves it alone.
function menuNavIndex(count, at, key) {
  if (!(count > 0)) return null;
  if (key === "Home") return 0;
  if (key === "End") return count - 1;
  if (key === "ArrowDown") return at < 0 ? 0 : (at + 1) % count;
  if (key === "ArrowUp") return at < 0 ? count - 1 : (at - 1 + count) % count;
  return null;
}

// MENU_SHAPES: the glyphs menuIconSvg draws itself, as svgIcon shapes (14x14). The rest reuse what the product
// already draws (see menuIconSvg).
const MENU_SHAPES = {
  pencil: [
    ["path", { d: "M2 12l.7-3L9.7 2a1.2 1.2 0 0 1 1.7 0l.6.6a1.2 1.2 0 0 1 0 1.7L5 11.3z" }],
    ["path", { d: "M8.4 3.3l2.3 2.3" }],
  ],
  pie: [
    ["circle", { cx: "7", cy: "7", r: "5.5" }],
    ["path", { d: "M7 7V1.5A5.5 5.5 0 0 1 12.5 7z", fill: "currentColor", stroke: "none" }],
  ],
  split: [
    ["rect", { x: "1.5", y: "2.5", width: "11", height: "9", rx: "1.5" }],
    ["path", { d: "M7 2.5v9" }],
  ],
  pause: [
    ["rect", { x: "3.5", y: "2.5", width: "2.5", height: "9", rx: ".8", fill: "currentColor", stroke: "none" }],
    ["rect", { x: "8", y: "2.5", width: "2.5", height: "9", rx: ".8", fill: "currentColor", stroke: "none" }],
  ],
  tray: [
    ["path", { d: "M7 2v6.5M4.2 6l2.8 2.8L9.8 6" }],
    ["path", { d: "M2 9.5v2c0 .55.45 1 1 1h8c.55 0 1-.45 1-1v-2" }],
  ],
  check: [
    ["path", { d: "M2.5 7.4l3.2 3.2 5.8-6.4" }],
  ],
  // An arrow down onto a line: the file leaves for the user's machine. Bring work home is the tray.
  download: [
    ["path", { d: "M7 1.5v7.5M3.8 6.2L7 9.4l3.2-3.2" }],
    ["path", { d: "M2.5 12.5h9" }],
  ],
  // An arrow into a bar: the path goes into the terminal's line.
  insert: [
    ["path", { d: "M1.5 7h7.2M5.9 4.2L8.7 7l-2.8 2.8" }],
    ["path", { d: "M12 2.5v9" }],
  ],
};

// menuIconSvg: the row icon for a kind, as inline SVG in the product's icon style (svgicon.js: 14x14, no
// fill, currentColor, 1.2 stroke, round caps and joins), so the menu does not depend on any font having a
// glyph. Shapes the product already draws are reused: Files is the pane header's folder (Folder-plus adds a
// plus), Copy and Duplicate are the dock's two windows (the latter with a plus), End session and Delete are
// the dock's cross. The rest are MENU_SHAPES. null for "".
function menuIconSvg(kind) {
  if (!kind) return null;
  let svg;
  if (kind === "files" || kind === "folder-plus") {
    svg = tabIcon("files");
    if (kind === "folder-plus") svg.append(svgShape("path", { d: "M7 6v3.6M5.2 7.8h3.6" }));
  } else if (kind === "copy" || kind === "duplicate") {
    svg = dockIcon("own");
    if (kind === "duplicate") svg.append(svgShape("path", { d: "M5.5 7v3M4 8.5h3" }));
  } else if (kind === "end" || kind === "delete") {
    svg = dockIcon("hide");
  } else {
    // Own properties only: a kind such as "constructor" must not find Object.prototype's member.
    const own = Object.prototype.hasOwnProperty.call(MENU_SHAPES, kind);
    return svgIcon(own ? MENU_SHAPES[kind] : [], { icon: kind });
  }
  // dockIcon draws no round caps; the menu's icons all have them.
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("data-icon", kind);
  return svg;
}

// One row of a desktop (kit) menu: .menu-item > .menu-icon + .menu-text, as frame M5.1 draws it. `icon` is a
// menuIconSvg kind; the slot is decoration (aria-hidden) and, with no icon, stays an empty 16px box so the
// text of every row lines up.
function kitMenuButton(label, icon, danger, run) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = danger ? "menu-item is-danger" : "menu-item";
  b.setAttribute("role", "menuitem");
  const ic = document.createElement("span");
  ic.className = "menu-icon";
  ic.setAttribute("aria-hidden", "true");
  const svg = menuIconSvg(icon);
  if (svg) ic.append(svg);
  const tx = document.createElement("span");
  tx.className = "menu-text";
  tx.textContent = label;
  b.append(ic, tx);
  b.onclick = run;
  return b;
}

// kitMenuSep: the hairline between two groups of rows. Not an item: the arrow keys step over it.
function kitMenuSep() {
  const sep = document.createElement("div");
  sep.className = "menu-sep";
  sep.setAttribute("role", "separator");
  return sep;
}

// kitMenuKeys: the open menu's arrow keys. Up and Down (wrapping), Home and End move focus over `menu`'s
// rows and return true (the key is handled: default prevented); any other key returns false and is left
// alone. A row that is aria-disabled does nothing, so the keys step over it. `.mi` is the phone's legacy
// flat row, stepped the same.
function kitMenuKeys(menu, e) {
  const rows = Array.from(menu.querySelectorAll('.menu-item:not([aria-disabled="true"]), .mi'));
  const next = menuNavIndex(rows.length, rows.indexOf(document.activeElement), e.key);
  if (next === null) return false;
  e.preventDefault();
  rows[next].focus();
  return true;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { menuNavIndex, menuIconSvg, kitMenuButton, kitMenuSep, kitMenuKeys };
}
