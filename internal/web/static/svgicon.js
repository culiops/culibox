// svgIcon: the product's inline icons are built here and nowhere else. One <svg> preamble (a 14x14 box,
// no fill, currentColor, 1.2 stroke, round caps and joins, hidden from assistive tech) used to be written
// three times (tabs.js tabIcon and dockIcon, the kit menu's menuIconSvg); the glyph is decoration, its
// meaning is carried by the text or title next to it. Classic script: the browser shares one global scope.
const SVG_NS = "http://www.w3.org/2000/svg";

// svgShape: one child shape, `tag` with its attributes set in the order `attrs` lists them.
function svgShape(tag, attrs) {
  const n = document.createElementNS(SVG_NS, tag);
  for (const k in attrs) n.setAttribute(k, attrs[k]);
  return n;
}

// svgIcon(shapes, opts): `shapes` is [[tag, attrs], ...], drawn in that order. opts:
//   viewBox      "0 0 14 14"
//   strokeWidth  "1.2"
//   round        true: stroke-linecap and stroke-linejoin round (tabIcon's, the menu's). false is dockIcon's
//                preamble, which sets neither.
//   icon         the kind, as the data-icon attribute (the kit menu tags each of its glyphs)
//   size         width and height in px, only when given: the stylesheet sizes every icon so far (14px in
//                a menu's 16px slot), so an unsized icon carries no width or height at all.
function svgIcon(shapes, opts) {
  const o = opts || {};
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("viewBox", o.viewBox || "0 0 14 14");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", o.strokeWidth || "1.2");
  if (o.round !== false) {
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
  }
  if (o.icon) svg.setAttribute("data-icon", o.icon);
  if (o.size) {
    svg.setAttribute("width", o.size);
    svg.setAttribute("height", o.size);
  }
  for (const [tag, attrs] of shapes || []) svg.append(svgShape(tag, attrs));
  return svg;
}

// iconSetOf: ICON_SET (icons.js, generated) is a page global; under node it is required, as filekinds.js does.
const iconSetOf = () => (typeof ICON_SET !== "undefined" ? ICON_SET : require("./icons.js").ICON_SET);

// uiIcon(name, opts): the one builder of the product's glyphs. `name` is a key of ICON_SET: a Lucide name drawn
// as an outline (24 grid, stroke 2, round caps and joins) or "si:<slug>", a Simple Icons mark, filled. opts:
//   size   width and height in px, default 14
//   cls    appended to class="ui-icon"
//   label  makes the icon an image (role="img", aria-label) for the one place it carries meaning alone;
//          without it the icon is decoration beside its text and hidden from assistive tech
// A name that is not an own key of ICON_SET draws an empty <svg> (never throws, never text).
function uiIcon(name, opts) {
  const o = opts || {};
  const set = iconSetOf();
  const entry = Object.prototype.hasOwnProperty.call(set, name) ? set[name] : null;
  const svg = svgIcon(entry ? entry[1] : [], { viewBox: "0 0 24 24", strokeWidth: "2", size: o.size || 14 });
  svg.setAttribute("data-icon", name);
  if (entry && entry[0] === "f") {
    svg.setAttribute("fill", "currentColor");
    svg.setAttribute("stroke", "none");
  }
  svg.setAttribute("focusable", "false");
  svg.setAttribute("class", o.cls ? "ui-icon " + o.cls : "ui-icon");
  if (o.label) {
    svg.removeAttribute("aria-hidden");
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", o.label);
  }
  return svg;
}

// paintStaticIcons(root): the page's own markup holds an empty <svg data-uicon="name" width="14" class="x"> where
// a glyph goes; each is replaced by uiIcon(name) of that width and class, keeping its id. Returns the count.
function paintStaticIcons(root) {
  const holes = Array.from(root.querySelectorAll("svg[data-uicon]"));
  for (const h of holes) {
    const n = uiIcon(h.dataset.uicon, { size: h.getAttribute("width") || undefined, cls: h.getAttribute("class") || "" });
    if (h.getAttribute("id")) n.setAttribute("id", h.getAttribute("id"));
    h.replaceWith(n);
  }
  return holes.length;
}

// SVG_TERMINAL_24: a terminal prompt (">_"), the "shell" icon of a profile row. The shapes are Lucide's "terminal" (ISC licence,
// https://lucide.dev). Drawn with svgIcon(SVG_TERMINAL_24, { viewBox: "0 0 24 24", strokeWidth: "2", size }).
const SVG_TERMINAL_24 = [["polyline", { points: "4 17 10 11 4 5" }], ["line", { x1: "12", x2: "20", y1: "19", y2: "19" }]];

// The sidebar's glyphs below (SVG_FOLDER_24 to SVG_HOST_24) are Lucide's folder, chevron-down, chevron-right, plus, box, copy and triangle-alert (ISC licence, https://lucide.dev).
const SVG_FOLDER_24 = [["path", { d: "M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" }]];
const SVG_CHEVRON_DOWN_24 = [["path", { d: "m6 9 6 6 6-6" }]];
const SVG_CHEVRON_RIGHT_24 = [["path", { d: "m9 18 6-6-6-6" }]];
const SVG_PLUS_24 = [["path", { d: "M5 12h14" }], ["path", { d: "M12 5v14" }]];
const SVG_BOX_24 = [["path", { d: "M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z" }], ["path", { d: "m3.3 7 8.7 5 8.7-5" }], ["path", { d: "M12 22V12" }]];
const SVG_CLONE_24 = [["rect", { width: "14", height: "14", x: "8", y: "8", rx: "2" }], ["path", { d: "M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" }]];
const SVG_HOST_24 = [["path", { d: "m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" }], ["path", { d: "M12 9v4" }], ["path", { d: "M12 17h.01" }]];

// svgIcon24: a Lucide glyph (a 24-unit shape list above, or SVG_TERMINAL_24) at `size` px, stroke 2.
function svgIcon24(shapes, size) {
  return svgIcon(shapes, { viewBox: "0 0 24 24", strokeWidth: "2", size });
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { svgIcon, svgShape, uiIcon, paintStaticIcons, svgIcon24, SVG_TERMINAL_24, SVG_FOLDER_24, SVG_CHEVRON_DOWN_24, SVG_CHEVRON_RIGHT_24, SVG_PLUS_24, SVG_BOX_24, SVG_CLONE_24, SVG_HOST_24 };
}
