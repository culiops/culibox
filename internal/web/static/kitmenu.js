// The kit menu's builders (frame M5.1): the desktop menus (the session row's ⋯, the pane header's tab
// switcher, the profile page's ⋯ and "+ Add" pickers, the file tree's row menu) are all `.menu-item` rows
// built here, so their look, icons and arrow keys are one component. Classic script, loaded before tabs.js
// (menuIconSvg draws through uiIcon and ICON_MENU, svgicon.js and iconmap.js, which load before it).

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

// menuIconSvg: the row icon for a kind, a vendored glyph (uiIcon, 14px) named by ICON_MENU (iconmap.js), so the
// menu does not depend on any font having a glyph. data-icon is the glyph's name. A kind that is not an
// ICON_MENU key draws an empty svg; null for "".
function menuIconSvg(kind) {
  return kind ? uiIcon(iconOf(ICON_MENU, kind), { size: 14 }) : null;
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
