"use strict";

// What the markdown preview lets through. Markdown can be semi-untrusted (an agent
// writes it, a cloned repo ships it), so it is rendered with DOMPurify and the
// preview loads no remote resource: a remote request is one the page makes for
// whoever wrote the file (a tracking beacon, an exfiltration channel). The policy
// functions are pure; mdRenderFragment, the one place markdown becomes DOM, touches
// no global (the caller hands it marked, DOMPurify and highlight.js) and works on the
// nodes it is given, so `node --test` reaches all of it. Both previews call it: the
// dock's (editormd.js) and the phone's (viewer-tab.js renderMarkdown).
//
// The policy has two halves, and neither is enough alone.
//  1. DOMPurify, configured with MD_PURIFY, removes what can load or send something
//     whatever its value: tags that fetch or submit (script, style, iframe, object,
//     embed, form, link, meta, base, input, track) and attributes that carry a URL
//     the walk below does not look at (srcset, formaction, ping, poster, background)
//     or a CSS url() (style).
//  2. The walk (mdWalkFragment) runs on the sanitized fragment before it is attached to
//     the page: the fragment belongs to the inert document DOMPurify parsed in, where
//     an <img> loads nothing, and the first image that is adopted into the page does.
//     It visits EVERY element and applies mdUnsafeAttr to its src, href and xlink:href. DOMPurify keeps those
//     attributes on img, video, audio, source and the SVG image and feImage elements
//     (it drops SVG use and animate on its own), and a value in one of them that is not
//     local is a request. The walk takes every element rather than this list, so a DOMPurify
//     upgrade that allows another one is covered. Two kinds of element:
//       - img, video, audio and source: their src is a file of the preview's own. A relative one
//         is rewritten to the read route first; one that is then not local, or none on an <img>,
//         becomes a placeholder;
//       - every other element (SVG image and feImage, MathML, area, ...): no src; an href or
//         xlink:href may only point into the document (#id) or hold a small inline image (an SVG
//         <image>: the inline image only, it fetches even a fragment). A relative path would be a
//         GET of any route of the app, and an address a request to the host the file names, so
//         any other value loses the attribute.
//     It also removes any attribute that carries a CSS url() whose target is not one of those two
//     (and, on an attribute that is CSS, one of the other CSS functions that fetch): SVG
//     presentation attributes such as filter, mask, clip-path, fill and stroke survive DOMPurify
//     and take one. <a href> is a link the user must click, so it stays (the walk gives it
//     target=_blank rel="noopener noreferrer", and drops a javascript:, vbscript: or data: one).

// Passed as the config of DOMPurify.sanitize. Frozen, so a caller that tries to
// loosen it throws instead of quietly weakening every later render.
const MD_PURIFY = Object.freeze({
  FORBID_TAGS: Object.freeze(["script", "style", "iframe", "object", "embed", "form", "link", "meta", "base", "input", "track"]),
  FORBID_ATTR: Object.freeze(["srcset", "formaction", "style", "poster", "background", "ping"]),
});

// The two same-origin routes a file's bytes come from: /api/fs/read, and the
// per-session box route boxFileURL builds (fsops.js).
const MS_BOX_ROUTE = /^\/api\/sessions\/[^/?#]+\/boxfile\?/;

// The image types a data: URL may carry. Not svg (a script can ride in one), and
// the payload is base64 only, so nothing else can ride in the URL either.
const MS_DATA_IMAGE = /^data:image\/(?:png|gif|jpeg|webp);base64,[a-z0-9+\/=%]*$/i;

// msNormalize reads a URL the way a browser does before it parses one: tab, newline and carriage return
// are dropped wherever they are, and control characters and spaces are trimmed at both ends.
function msNormalize(url) {
  return url.replace(/[\t\n\r]/g, "").replace(/^[\u0000-\u0020]+|[\u0000-\u0020]+$/g, "");
}

// isLocalSrc says whether a src is safe to load: a relative path (the preview
// rewrites it to a read route), the two same-origin read routes, or a small image
// inline. Everything else is false: any other scheme (http, https, javascript,
// vbscript, blob, file, ...), a protocol-relative "//host", an absolute path that is
// not a read route, a data: URL that is not one of the images above, an empty src.
//
// The URL is read the way a browser reads it before anything is tested. The parser
// drops tab, newline and carriage return wherever they are and trims control
// characters and spaces at both ends, so "java\nscript:..." is a javascript: URL and
// "/\t/host" is "//host". A backslash counts as a slash for http(s) URLs, so "/\host"
// and "\\host" are "//host" as well; no honest src needs one, so any backslash is
// refused. A control character left inside is refused too.
function isLocalSrc(url) {
  if (typeof url !== "string") return false;
  const s = msNormalize(url);
  if (s === "" || /[\u0000-\u001f\u007f\\]/.test(s)) return false;
  if (/^[a-z][a-z0-9+.-]*:/i.test(s)) return MS_DATA_IMAGE.test(s); // a scheme: only the inline images pass
  if (s.startsWith("//")) return false;
  if (s.startsWith("/")) return s.startsWith("/api/fs/read?") || MS_BOX_ROUTE.test(s);
  return true;
}

// The attributes whose value is a URL the browser fetches or follows, which the walk checks on every element but <a>: src,
// href and xlink:href, whatever the case and with any prefix.
const MS_URL_ATTR = /^(?:[a-z0-9_-]+:)?(?:src|href)$/i;
const MS_SRC_ATTR = /^(?:[a-z0-9_-]+:)?src$/i;

// The elements whose src is a file of the preview's own, with what a placeholder calls them. A Map: a tag name is a file's
// text, so "constructor" must not find an inherited property.
const MS_MEDIA = new Map([["img", "image"], ["video", "video"], ["audio", "audio"], ["source", "media"]]);

// mdInertRef says whether a URL is one that cannot make a request: a reference into the document (#id) or a small inline image. A
// path, however relative, is a GET of a route of the app; an address is a GET of the host it names.
function mdInertRef(url) {
  if (typeof url !== "string") return false;
  const s = msNormalize(url);
  return s.startsWith("#") || MS_DATA_IMAGE.test(s);
}

// mdUnsafeAttr says whether one attribute of one element must go: it is a src, href
// or xlink:href, the element is not an <a> (a link waits for a click, a resource
// does not), and its value is not safe for the element:
//  - a media element's src is safe when it is local (isLocalSrc) and no longer relative: the walk rewrites a relative one to a
//    read route first;
//  - any other src (a frame, an embed, a track, MathML's mglyph) is not: markdown has no such element, so it goes;
//  - an SVG <image> fetches its href, a fragment too (it asks for the page itself), so only a small inline image is safe;
//  - any other href or xlink:href (feImage, area, MathML, a gradient's, textPath's ...) is safe when it is inert (mdInertRef): a
//    reference into the document, or a small inline image.
// An attribute that is not there (null or undefined) is nothing to remove. What the caller does with a true is its own business:
// placeholder for the media elements' src, remove the attribute for anything else.
function mdUnsafeAttr(tagName, attrName, value) {
  if (value == null) return false;
  const tag = String(tagName).toLowerCase();
  if (tag === "a") return false;
  const attr = String(attrName);
  if (!MS_URL_ATTR.test(attr)) return false;
  if (MS_SRC_ATTR.test(attr)) return !MS_MEDIA.has(tag) || !isLocalSrc(value) || mdIsRelative(value);
  if (tag === "image") return !MS_DATA_IMAGE.test(msNormalize(String(value)));
  return !mdInertRef(value);
}

// mdIsRelative says whether a src is a path relative to the file: local, with no scheme and no leading slash. The preview
// rewrites it to a read route (a bare "img.png" would be asked of the app's own origin, where it is not).
function mdIsRelative(url) {
  if (!isLocalSrc(url)) return false;
  const s = msNormalize(url);
  return !s.startsWith("/") && !/^[a-z][a-z0-9+.-]*:/i.test(s);
}

// CSS lets a name be written with escapes ("u\72l(" is "url("), and a browser reads the escapes before it looks at the name, so
// a test of the raw text would be a test that a hostile file passes: they are undone first. A backslash and one to six hex
// digits (and one space after them) is a code point; a backslash and any other character is that character.
const MS_CSS_ESCAPE = /\\(?:([0-9a-f]{1,6})[ \t\n\r\f]?|([^\n\r\f0-9a-f]))/gi;

function msCssUnescape(s) {
  return s.replace(MS_CSS_ESCAPE, (m, hex, ch) => {
    if (ch !== undefined) return ch;
    const cp = parseInt(hex, 16);
    return cp === 0 || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff) ? "\ufffd" : String.fromCodePoint(cp);
  });
}

// The CSS functions that take an image by address other than url(): image-set() (and its -webkit- spelling), image(),
// src(), cross-fade() and element(). No honest SVG puts one in a presentation attribute, so one is refused whatever it names.
const MS_CSS_FETCH_FN = /(?:image-set|image|src|cross-fade|element)\s*\(/i;

// The attributes whose value is parsed as CSS: style (DOMPurify drops it, MD_PURIFY says so too) and the SVG presentation
// attributes, which DOMPurify keeps. Free text is not: an alt of "Architecture image (v2)" or a title of "Page element (x)" is a
// sentence, and a function name in it fetches nothing.
const MS_CSS_ATTRS = new Set([
  "style", "alignment-baseline", "baseline-shift", "clip", "clip-path", "clip-rule", "color", "color-interpolation",
  "color-interpolation-filters", "color-profile", "color-rendering", "cursor", "direction", "display", "dominant-baseline", "fill",
  "fill-opacity", "fill-rule", "filter", "flood-color", "flood-opacity", "font", "font-family", "font-size", "font-size-adjust",
  "font-stretch", "font-style", "font-variant", "font-weight", "glyph-orientation-horizontal", "glyph-orientation-vertical",
  "image-rendering", "isolation", "kerning", "letter-spacing", "lighting-color", "marker", "marker-end", "marker-mid", "marker-start",
  "mask", "mask-type", "mix-blend-mode", "opacity", "overflow", "paint-order", "pointer-events", "shape-rendering", "stop-color",
  "stop-opacity", "stroke", "stroke-dasharray", "stroke-dashoffset", "stroke-linecap", "stroke-linejoin", "stroke-miterlimit",
  "stroke-opacity", "stroke-width", "text-anchor", "text-decoration", "text-overflow", "text-rendering", "transform",
  "transform-origin", "unicode-bidi", "vector-effect", "visibility", "white-space", "word-spacing", "writing-mode",
  "mathbackground", "mathcolor",
]);

// mdUnsafeCss says whether an attribute could make the browser fetch something that its value names as CSS. A url() on an
// attribute that is CSS (MS_CSS_ATTRS: an SVG presentation attribute, which DOMPurify keeps: filter, mask, clip-path, fill, stroke,
// marker-start, ...) must be inert (mdInertRef: a reference into the document, url(#blur), or a small inline image), the same test
// as an SVG image's href, so a relative path does not pass: it would be a GET of a route of the app. On an attribute that is CSS,
// one of the other CSS functions above is refused too. On any other attribute (free text, or one this list does not know that is
// parsed as CSS all the same) a url() is still looked at, and refused when its target is not local (isLocalSrc), but no function
// name is: a sentence that says "image (v2)" fetches nothing.
function mdUnsafeCss(attrName, value) {
  if (typeof value !== "string" || !value.includes("(")) return false; // a function needs a parenthesis (an escaped one is not a call)
  const s = msCssUnescape(value);
  const css = MS_CSS_ATTRS.has(String(attrName).toLowerCase());
  if (css && MS_CSS_FETCH_FN.test(s)) return true;
  const url = /url\s*\(/gi;
  for (let m; (m = url.exec(s)); ) {
    const rest = s.slice(m.index + m[0].length).replace(/^\s+/, "");
    const q = rest[0];
    let target;
    if (q === '"' || q === "'") {
      const end = rest.indexOf(q, 1);
      target = end < 0 ? rest.slice(1) : rest.slice(1, end);
    } else {
      const end = rest.indexOf(")");
      target = (end < 0 ? rest : rest.slice(0, end)).trim();
    }
    if (!(css ? mdInertRef(target) : isLocalSrc(target))) return true;
  }
  return false;
}

// mdUnsafeLink says whether an <a>'s href runs script when it is clicked. DOMPurify drops these itself; the walk asks again
// so that a link stays a link whatever a DOMPurify upgrade allows. Any other scheme is the user's to follow.
function mdUnsafeLink(url) {
  if (typeof url !== "string") return false;
  const m = /^([a-z][a-z0-9+.-]*):/i.exec(msNormalize(url));
  return !!m && /^(?:javascript|vbscript|data)$/i.test(m[1]);
}

// --- the walk and the render -------------------------------------------------------------------------------------

const MS_REMOTE_URL = /^(?:[a-z][a-z0-9+.-]*:|\/\/)/i; // a scheme or a protocol-relative address: the author meant another host
const MS_LINKABLE_URL = /^https?:\/\//i;

// mdLink makes an <a> that opens `url` in a new tab with no opener and no referrer.
function mdLink(doc, url, text) {
  const a = doc.createElement("a");
  a.setAttribute("href", url);
  a.setAttribute("target", "_blank");
  a.setAttribute("rel", "noopener noreferrer");
  a.textContent = text;
  return a;
}

// mdPlaceholder replaces `el` with <span class="md-blocked">remote image blocked <a href=url>url</a></span>. `url` is what
// the element asked for (null: nothing, an <img> that lost its srcset). The link is made only for an http(s) URL: the click
// is the user's, and a javascript: or data: address is not something to offer. A span inside a video or audio is fallback
// content that only a browser that cannot play media draws, so for a <source> there the notice goes before the video or
// audio, and the source is removed.
function mdPlaceholder(doc, el, tag, url) {
  const n = url === null ? "" : msNormalize(url);
  const span = doc.createElement("span");
  span.className = "md-blocked";
  span.append((MS_REMOTE_URL.test(n) ? "remote " : "") + MS_MEDIA.get(tag) + " blocked");
  if (n) span.title = n;
  if (MS_LINKABLE_URL.test(n)) span.append(" ", mdLink(doc, n, n.length > 60 ? n.slice(0, 59) + "\u2026" : n));
  const parent = el.parentNode;
  const host = tag === "source" && parent && /^(?:video|audio)$/i.test(String(parent.localName)) ? parent : null;
  if (host) { host.before(span); el.remove(); } else el.replaceWith(span);
}

// mdWalkMedia handles an img, video, audio or source: a relative src is rewritten to a read route first (so it is local),
// then a src that is still not local, or an <img> with none, becomes a placeholder. True when the element was replaced.
function mdWalkMedia(doc, el, tag, rewrite) {
  let src = el.getAttribute("src");
  if (src === null) {
    if (tag !== "img") return false; // a video or audio may take its sources from <source> children
    mdPlaceholder(doc, el, tag, null);
    return true;
  }
  if (mdIsRelative(src)) { src = rewrite(src); el.setAttribute("src", src); }
  if (!mdUnsafeAttr(tag, "src", src)) return false;
  mdPlaceholder(doc, el, tag, src);
  return true;
}

// mdWalkLink: a link opens in a new tab with no opener, so a click never navigates the app away or hands it a window, and
// one that would run script has no href left.
function mdWalkLink(el) {
  for (const name of ["href", "xlink:href"]) {
    if (mdUnsafeLink(el.getAttribute(name))) el.removeAttribute(name);
  }
  if (el.hasAttribute("href") || el.hasAttribute("xlink:href")) {
    el.setAttribute("target", "_blank");
    el.setAttribute("rel", "noopener noreferrer");
  }
}

// mdWalkFragment applies the second half of the policy (the top of this file) to a sanitized fragment, in place. `rewrite(rel)`
// turns a relative src into the read route that serves it. A <template> goes whole: its content is a second tree that no
// query below reaches, and markdown has no use for one.
function mdWalkFragment(root, rewrite) {
  const doc = root.ownerDocument;
  for (const el of Array.from(root.querySelectorAll("*"))) {
    if (!root.contains(el)) continue; // inside something that was replaced or removed above
    const tag = String(el.localName || el.tagName).toLowerCase();
    if (tag === "template") { el.remove(); continue; }
    if (MS_MEDIA.has(tag) && mdWalkMedia(doc, el, tag, rewrite)) continue;
    for (const attr of Array.from(el.attributes)) {
      if (mdUnsafeAttr(tag, attr.name, attr.value) || mdUnsafeCss(attr.name, attr.value)) el.removeAttribute(attr.name);
    }
    if (tag === "a") mdWalkLink(el);
  }
}

// mdRenderFragment is the only place markdown becomes DOM: marked, DOMPurify (MD_PURIFY, as a fragment), highlight.js on the
// fenced code, then the walk. `deps` = {marked, purify, hljs (optional), rewrite}. Throws when the markup cannot be made; the
// caller shows the file as plain text instead. The fragment is not yet in the page: appending it is what starts a load.
function mdRenderFragment(text, deps) {
  deps.marked.setOptions({ breaks: false, gfm: true }); // marked v12 removed the `highlight` option: the code is highlighted below
  const html = deps.marked.parse(text);
  if (typeof html !== "string") throw new Error("markdown did not render to a string");
  const frag = deps.purify.sanitize(html, Object.assign({}, MD_PURIFY, { RETURN_DOM_FRAGMENT: true }));
  if (!frag || typeof frag.querySelectorAll !== "function") throw new Error("the sanitizer returned no fragment");
  if (deps.hljs) frag.querySelectorAll("pre code").forEach((el) => { try { deps.hljs.highlightElement(el); } catch (_) {} });
  mdWalkFragment(frag, deps.rewrite);
  return frag;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    isLocalSrc, mdUnsafeAttr, MD_PURIFY, mdIsRelative, mdInertRef, mdUnsafeCss, mdUnsafeLink, mdWalkFragment, mdRenderFragment,
  };
}
