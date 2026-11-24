// uiIcon (below) is the one builder every page icon goes through: a glyph of the vendored set (icons.js, Lucide
// 1.70.0 outlines and Simple Icons 16.37.0 marks, see docs/ICONS.md) on a 24 grid at stroke 2. svgIcon and svgShape
// are only the low-level <svg> wrapper uiIcon draws through: one preamble, each element made with createElementNS,
// never parsed from HTML. The glyph is decoration; its meaning is carried by the text or title next to it.
// Classic script: the browser shares one global scope.
const SVG_NS = "http://www.w3.org/2000/svg";

// svgShape: one child shape, `tag` with its attributes set in the order `attrs` lists them.
function svgShape(tag, attrs) {
  const n = document.createElementNS(SVG_NS, tag);
  for (const k in attrs) n.setAttribute(k, attrs[k]);
  return n;
}

// svgIcon(shapes, opts): the low-level wrapper uiIcon draws through; call sites use uiIcon, not this. `shapes` is
// [[tag, attrs], ...], drawn in that order. opts (uiIcon passes viewBox, strokeWidth and size; the rest fall back
// to the wrapper's own defaults):
//   viewBox      "0 0 14 14" when not given (uiIcon passes "0 0 24 24")
//   strokeWidth  "1.2" when not given (uiIcon passes "2")
//   round        true: stroke-linecap and stroke-linejoin round. false sets neither.
//   icon         the kind, as the data-icon attribute when given (uiIcon sets data-icon itself)
//   size         width and height in px, only when given (uiIcon defaults it to 14)
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
// A known, unlabelled glyph is built once per name, size and class and every call returns a deep clone of it (#168: a 2000-row
// tree folder asks for thousands of the same few glyphs); each call still gets its own node, so changing one changes no other.
// The kept copies belong to one document. An unknown name or a label (free text) is never kept, so the cache is bounded by
// the glyphs and sizes the code asks for.
let uiIconKept = null;
let uiIconKeptDoc = null;
function uiIcon(name, opts) {
  const o = opts || {};
  const set = iconSetOf();
  const known = Object.prototype.hasOwnProperty.call(set, name);
  if (!known || o.label) return uiIconBuild(name, o, known ? set[name] : null);
  if (uiIconKeptDoc !== document) { uiIconKept = new Map(); uiIconKeptDoc = document; }
  const key = name + "\n" + (o.size || 14) + "\n" + (o.cls || "");
  const kept = uiIconKept.get(key);
  if (kept) return kept.cloneNode(true);
  const svg = uiIconBuild(name, o, set[name]);
  if (typeof svg.cloneNode === "function") uiIconKept.set(key, svg.cloneNode(true));
  return svg;
}

// uiIconBuild draws uiIcon's glyph from its ICON_SET entry (null: an unknown name, an empty svg).
function uiIconBuild(name, o, entry) {
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
