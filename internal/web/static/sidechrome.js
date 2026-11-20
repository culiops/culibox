// sidechrome.js: the desktop sidebar's chrome (#340 S6): the layout button that shares a row with New
// session, and the cost on the foot's links line. Classic script, loaded after kitmenu.js and before tabs.js
// (applyCap repaints the button through xlayPaint).

// xlayLabel: the layout in words. Any cap other than 1 or 2 reads as 4, clampCap's default (layout.js).
function xlayLabel(n) {
  if (n === 1) return "1 pane";
  if (n === 2) return "2 panes";
  return "4 panes";
}

function xlayTitle(n) {
  return "Layout: " + xlayLabel(n) + " (click to change)";
}

function xlayAria(n) {
  return "Layout: " + xlayLabel(n);
}

// xlayShapes: the layout glyph as svgIcon shapes (24x24): a frame, then one divider per split. The paths are
// Lucide's square, columns-2 and layout-grid (ISC licence), as mockup t1 draws them.
function xlayShapes(n) {
  const frame = ["rect", { width: "18", height: "18", x: "3", y: "3", rx: "2" }];
  if (n === 1) return [frame];
  if (n === 2) return [frame, ["path", { d: "M12 3v18" }]];
  return [frame, ["path", { d: "M3 12h18" }], ["path", { d: "M12 3v18" }]];
}

// The open layout menu: {btn, wrap, menu}, or null. One button exists (the sidebar top's).
let xlayMenu = null;

// xlayPaint: the button shows the current layout, in its glyph and in its title and label. applyCap calls it,
// so a layout set from the menu, the palette or a restored layout all read the same; an open menu closes, so
// its tick is never stale.
function xlayPaint(btn, n) {
  btn.replaceChildren(svgIcon(xlayShapes(n), { viewBox: "0 0 24 24", strokeWidth: "2", size: 15 }));
  btn.setAttribute("title", xlayTitle(n));
  btn.setAttribute("aria-label", xlayAria(n));
  if (xlayMenu && xlayMenu.btn === btn) xlayMenuClose(false);
}

// xlayMenuBuild: the kit menu (mockup t2): a "Layout" label, then 1, 2 and 4 panes as radio rows, each with its
// glyph, and a tick on the current one. A row's click calls onPick(cap).
function xlayMenuBuild(n, onPick) {
  const mk = (tag, cls) => {
    const e = document.createElement(tag);
    e.className = cls;
    return e;
  };
  const menu = mk("div", "menu xmenu");
  menu.setAttribute("role", "menu");
  menu.setAttribute("aria-label", "Layout");
  const label = mk("div", "menu-label grp-label");
  label.textContent = "Layout";
  menu.append(label);
  for (const c of [1, 2, 4]) {
    const on = c === n;
    const b = mk("button", on ? "menu-item on" : "menu-item");
    b.type = "button";
    b.setAttribute("role", "menuitemradio");
    b.setAttribute("aria-checked", on ? "true" : "false");
    const icon = mk("span", "menu-icon");
    icon.setAttribute("aria-hidden", "true");
    icon.append(svgIcon(xlayShapes(c), { viewBox: "0 0 24 24", strokeWidth: "2", size: 14 }));
    const text = mk("span", "menu-text");
    text.textContent = xlayLabel(c);
    const tick = mk("span", "menu-kbd");
    tick.setAttribute("aria-hidden", "true");
    if (on) tick.append(menuIconSvg("check"));
    b.append(icon, text, tick);
    b.onclick = () => onPick(c);
    menu.append(b);
  }
  return menu;
}

// xlayMenuOpen: the menu in flow under the button (profilepage.js's ⋯ pattern), focus on the ticked row.
function xlayMenuOpen(btn) {
  xlayMenuClose(false);
  const wrap = btn.parentElement;
  const menu = xlayMenuBuild(cap, (c) => {
    xlayMenuClose(true);
    applyCap(c);
  });
  wrap.append(menu);
  xlayMenu = { btn, wrap, menu };
  btn.setAttribute("aria-expanded", "true");
  const on = menu.querySelector(".menu-item.on");
  if (on) on.focus();
}

function xlayMenuClose(refocus) {
  if (!xlayMenu) return;
  const m = xlayMenu;
  xlayMenu = null;
  m.menu.remove();
  m.btn.setAttribute("aria-expanded", "false");
  if (refocus) m.btn.focus();
}

// xlayDocClick: a click anywhere outside the button and its menu closes it, without picking.
function xlayDocClick(e) {
  if (xlayMenu && !xlayMenu.wrap.contains(e.target)) xlayMenuClose(false);
}

// xlayWrapKeydown: Esc closes the menu only. Its default is prevented, so the page or dialog behind it
// (pagehost.js) does not close too; focus goes back to the button. The arrows walk the rows (kitmenu.js).
function xlayWrapKeydown(e) {
  if (!xlayMenu) return;
  if (e.key === "Escape") {
    e.preventDefault();
    xlayMenuClose(true);
    return;
  }
  kitMenuKeys(xlayMenu.menu, e);
}

// initSideLayout: wires the sidebar top's layout button once, at boot (applyCap paints it).
function initSideLayout() {
  const btn = document.querySelector(".xlay");
  if (!btn) return;
  btn.onclick = () => {
    if (xlayMenu && xlayMenu.btn === btn) xlayMenuClose(false);
    else xlayMenuOpen(btn);
  };
  btn.parentElement.onkeydown = xlayWrapKeydown;
  document.addEventListener("click", xlayDocClick);
}

const SIDE_COST_TITLE = "Token usage today (UTC) priced at API list rates. A subscription does not bill this.";

// sideCostDep: a page global in the browser, the module under node (spawnpreview.js's spawnDep).
function sideCostDep(name, file) {
  return typeof globalThis[name] === "function" ? globalThis[name] : require(file)[name];
}

// sideCostLabel: the foot's cost. The amount is the Analytics page's usd(), so the same number reads the same
// way in both places; "≈" and the tooltip say it is list-price value, not a bill.
function sideCostLabel(cost) {
  return { text: "≈ " + sideCostDep("usd", "./analytics.js")(cost) + " today", title: SIDE_COST_TITLE };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    xlayLabel, xlayTitle, xlayAria, xlayShapes, xlayPaint, xlayMenuBuild, xlayMenuOpen, xlayMenuClose,
    xlayDocClick, xlayWrapKeydown, initSideLayout, SIDE_COST_TITLE, sideCostLabel,
  };
}
