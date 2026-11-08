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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { svgIcon, svgShape };
}
