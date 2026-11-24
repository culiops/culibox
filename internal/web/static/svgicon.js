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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { svgIcon, svgShape, uiIcon, paintStaticIcons };
}
