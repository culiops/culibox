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
//     and take one. An <a> is the link gate's (#348): one that would run script (javascript:, vbscript:, data:) loses its href, and
//     any other whose href is not a #fragment loses it too, its address kept in data-href with a click and Enter handler that hands
//     it to openAgentLink (linkgate.js), which asks before anything opens. With no href there is no middle-click, no "open in new
//     tab" in the context menu and no drag to the tab bar either, which a click handler alone could not stop.

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

// mdGate turns an <a> into a link the gate opens: `url` is kept in data-href (the caller has already taken the href off), the
// element is still a link to the keyboard, a screen reader and the pointer (role, tabindex, md-link for the styling), its title
// says where it goes, and a click or Enter hands the address to openAgentLink. Without openAgentLink in the page the handlers do
// nothing: a link that cannot ask does not open. `where` names the file for the dialog; none is fine.
function mdGate(a, url, where) {
  a.setAttribute("data-href", url);
  a.setAttribute("role", "link");
  a.setAttribute("tabindex", "0");
  const cls = (a.getAttribute("class") || "").split(/\s+/).filter(Boolean);
  if (!cls.includes("md-link")) cls.push("md-link");
  a.setAttribute("class", cls.join(" "));
  a.setAttribute("title", url);
  const ctx = where ? { source: "markdown", where } : { source: "markdown" };
  const go = (e) => {
    e.preventDefault();
    if (typeof openAgentLink === "function") openAgentLink(url, ctx);
  };
  a.addEventListener("click", go);
  // Enter is the link's key: claimed, so nothing else in the page acts on it too, and a held key's repeats do not ask again.
  a.addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    if (e.stopPropagation) e.stopPropagation();
    if (e.repeat) e.preventDefault();
    else go(e);
  });
}

// mdLink makes a gated <a> for `url`: the one the blocked-image notice offers.
function mdLink(doc, url, text, where) {
  const a = doc.createElement("a");
  a.textContent = text;
  mdGate(a, url, where);
  return a;
}

// mdPlaceholder replaces `el` with <span class="md-blocked">remote image blocked <a href=url>url</a></span>. `url` is what
// the element asked for (null: nothing, an <img> that lost its srcset). The link is made only for an http(s) URL: the click
// is the user's, and a javascript: or data: address is not something to offer. A span inside a video or audio is fallback
// content that only a browser that cannot play media draws, so for a <source> there the notice goes before the video or
// audio, and the source is removed.
function mdPlaceholder(doc, el, tag, url, where) {
  const n = url === null ? "" : msNormalize(url);
  const span = doc.createElement("span");
  span.className = "md-blocked";
  span.append((MS_REMOTE_URL.test(n) ? "remote " : "") + MS_MEDIA.get(tag) + " blocked");
  if (n) span.title = n;
  if (MS_LINKABLE_URL.test(n)) span.append(" ", mdLink(doc, n, n.length > 60 ? n.slice(0, 59) + "\u2026" : n, where));
  const parent = el.parentNode;
  const host = tag === "source" && parent && /^(?:video|audio)$/i.test(String(parent.localName)) ? parent : null;
  if (host) { host.before(span); el.remove(); } else el.replaceWith(span);
}

// mdWalkMedia handles an img, video, audio or source: a relative src is rewritten to a read route first (so it is local),
// then a src that is still not local, or an <img> with none, becomes a placeholder. True when the element was replaced.
function mdWalkMedia(doc, el, tag, rewrite, where) {
  let src = el.getAttribute("src");
  if (src === null) {
    if (tag !== "img") return false; // a video or audio may take its sources from <source> children
    mdPlaceholder(doc, el, tag, null, where);
    return true;
  }
  if (mdIsRelative(src)) { src = rewrite(src); el.setAttribute("src", src); }
  if (!mdUnsafeAttr(tag, "src", src)) return false;
  mdPlaceholder(doc, el, tag, src, where);
  return true;
}

// mdWalkLink: a link that would run script has no href left. Any other whose href (or xlink:href) is not a reference into the
// document is gated (mdGate): its href is taken off and kept in data-href, so a click opens nothing by itself. A #fragment stays an
// ordinary link, in a new tab as before. When both attributes are there each is judged on its own and the first address found is
// the one kept; the other, if it is not a fragment, is dropped.
function mdWalkLink(el, where) {
  let url = null;
  for (const name of ["href", "xlink:href"]) {
    const v = el.getAttribute(name);
    if (v === null) continue;
    if (mdUnsafeLink(v)) { el.removeAttribute(name); continue; }
    if (msNormalize(v).startsWith("#")) continue;
    el.removeAttribute(name);
    if (url === null) url = v;
  }
  if (url !== null) { mdGate(el, url, where); return; }
  if (el.hasAttribute("href") || el.hasAttribute("xlink:href")) {
    el.setAttribute("target", "_blank");
    el.setAttribute("rel", "noopener noreferrer");
  }
}

// mdWalkFragment applies the second half of the policy (the top of this file) to a sanitized fragment, in place. `rewrite(rel)`
// turns a relative src into the read route that serves it; `where` (optional) names the file for the link gate's dialog. A <template> goes whole: its content is a second tree that no
// query below reaches, and markdown has no use for one.
function mdWalkFragment(root, rewrite, where) {
  const doc = root.ownerDocument;
  for (const el of Array.from(root.querySelectorAll("*"))) {
    if (!root.contains(el)) continue; // inside something that was replaced or removed above
    const tag = String(el.localName || el.tagName).toLowerCase();
    if (tag === "template") { el.remove(); continue; }
    if (MS_MEDIA.has(tag) && mdWalkMedia(doc, el, tag, rewrite, where)) continue;
    for (const attr of Array.from(el.attributes)) {
      if (mdUnsafeAttr(tag, attr.name, attr.value) || mdUnsafeCss(attr.name, attr.value)) el.removeAttribute(attr.name);
    }
    if (tag === "a") mdWalkLink(el, where);
  }
}

// The longest code block the dock highlights (deps.hlMaxChars): highlight.js on a block of 100 KB already takes a noticeable while,
// and such a block is generated text nobody reads for its colours.
const MD_HL_MAX_CHARS = 102400;

// mdHlLanguage: the language a fence named, as marked writes it on the <code> ("language-go"), when highlight.js knows it; else "".
function mdHlLanguage(el, hljs) {
  const cls = (typeof el.getAttribute === "function" && el.getAttribute("class")) || "";
  const m = /(?:^|\s)(?:language|lang)-(\S+)/.exec(cls);
  return m && typeof hljs.getLanguage === "function" && hljs.getLanguage(m[1]) ? m[1] : "";
}

// mdFinish is the one way a piece of marked's HTML becomes DOM: DOMPurify (MD_PURIFY, as a fragment), highlight.js on the code blocks
// (every block; or, with deps.hlLabelledOnly, only a block whose fence named a language highlight.js knows, so nothing is
// auto-detected; and with deps.hlMaxChars, none longer than that), then the walk. Every render goes through it, whole or in chunks.
function mdFinish(html, deps) {
  if (typeof html !== "string") throw new Error("markdown did not render to a string");
  const frag = deps.purify.sanitize(html, Object.assign({}, MD_PURIFY, { RETURN_DOM_FRAGMENT: true }));
  if (!frag || typeof frag.querySelectorAll !== "function") throw new Error("the sanitizer returned no fragment");
  if (deps.hljs) {
    frag.querySelectorAll("pre code").forEach((el) => {
      if (deps.hlLabelledOnly && !mdHlLanguage(el, deps.hljs)) return;
      if (deps.hlMaxChars && el.textContent.length > deps.hlMaxChars) return;
      try { deps.hljs.highlightElement(el); } catch (_) {}
    });
  }
  mdWalkFragment(frag, deps.rewrite, deps.where);
  return frag;
}

// --- chunks (the dock, for a big file) ----------------------------------------------------------------------------------------
// One marked.lexer over a 2 MiB file was one 715 ms task, and the whole document's sanitize, highlight, walk and layout another 15 s
// (measured, Files dock P1). So the dock can ask for the document in chunks: the text is lexed in pieces, one step each, then handed
// back a few top-level blocks at a time, each through mdFinish. Joined, the chunks are exactly what one marked.parse makes (tested on
// the real marked), because a piece only ever starts where every block of the one before has ended, and a group of blocks only ever
// ends where no HTML tag is left open.

// MD_HEADING_CUT: an ATX heading line at the left margin. After a blank line it ends every block that could be open (a paragraph, a
// list, a quote, indented code, a table, an HTML block of types 6 and 7), so the text can be lexed in two parts there.
const MD_HEADING_CUT = /^#{1,6}(?:[ \t]|$)/;
// The HTML blocks that run across blank lines (CommonMark types 1 to 5, as marked 12 reads them): how each starts and what ends it.
const MD_HTML_SPANS = [
  { open: /^ {0,3}<(pre|script|style|textarea)(?=[\s>]|$)/i, close: (m) => new RegExp("</" + m[1] + ">", "i") },
  { open: /^ {0,3}<!--/, close: () => /-->/ },
  { open: /^ {0,3}<\?/, close: () => /\?>/ },
  { open: /^ {0,3}<![A-Za-z]/, close: () => />/ },
  { open: /^ {0,3}<!\[CDATA\[/, close: () => /\]\]>/ },
];

// mdLexPieces cuts `src` (newlines already \n) into pieces of at least `chunkChars` where it can: only before a heading line
// (MD_HEADING_CUT) that follows a blank line and is outside a fenced code block and outside an HTML block of MD_HTML_SPANS. A text
// with no such place is one piece. Joined, the pieces are `src`.
function mdLexPieces(src, chunkChars) {
  const pieces = [];
  let start = 0;
  let pos = 0;
  let fence = "";   // the open fence's backticks or tildes
  let html = null;  // the regex that ends the open HTML block
  let blank = false;
  while (pos < src.length) {
    const nl = src.indexOf("\n", pos);
    const end = nl < 0 ? src.length : nl;
    const line = src.slice(pos, end);
    if (fence) {
      const m = /^ {0,3}([`~]+) *$/.exec(line);
      if (m && m[1].startsWith(fence)) fence = "";
    } else if (html) {
      if (html.test(line)) html = null;
    } else {
      if (blank && pos > start && pos - start >= chunkChars && MD_HEADING_CUT.test(line)) { pieces.push(src.slice(start, pos)); start = pos; }
      const f = /^ {0,3}(`{3,}|~{3,})/.exec(line);
      if (f && !(f[1][0] === "`" && line.slice(f[0].length).includes("`"))) fence = f[1];
      else {
        for (const k of MD_HTML_SPANS) {
          const m = k.open.exec(line);
          if (!m) continue;
          const close = k.close(m);
          if (!close.test(line.slice(m[0].length))) html = close;
          break;
        }
      }
    }
    blank = /^[ \t]*$/.test(line);
    pos = end + 1;
  }
  pieces.push(src.slice(start));
  return pieces;
}

const MD_VOID_TAGS = new Set(["area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr"]);

// mdTagBalance: the tags an HTML snippet opens minus the ones it closes (void elements, self-closing tags, comments and declarations
// open nothing).
function mdTagBalance(raw) {
  const s = raw.replace(/<!--[\s\S]*?(?:-->|$)/g, "");
  let d = 0;
  for (const m of s.matchAll(/<([A-Za-z][A-Za-z0-9-]*)(?:\s[^<>]*)?>/g)) if (!MD_VOID_TAGS.has(m[1].toLowerCase()) && !m[0].endsWith("/>")) d++;
  for (const _ of s.matchAll(/<\/[A-Za-z][A-Za-z0-9-]*\s*>/g)) d--;
  return d;
}

// mdHtmlDepth: the balance of every HTML token in a token and the tokens inside it (a paragraph's inline tags, a list's items, a
// table's cells), leaving code out.
function mdHtmlDepth(token) {
  let d = 0;
  const visit = (x) => {
    if (!x || typeof x !== "object") return;
    if (Array.isArray(x)) { x.forEach(visit); return; }
    if (x.type === "code" || x.type === "codespan") return;
    if (x.type === "html" && typeof x.raw === "string") d += mdTagBalance(x.raw);
    for (const k of ["tokens", "items", "header", "rows"]) if (x[k]) visit(x[k]);
  };
  visit(token);
  return d;
}

// mdChunkGroups splits the top-level tokens into runs of about `chunkChars` of source: a run ends once it holds that much and no
// HTML tag it opened is still open (a <details> around a few paragraphs stays in one run, so DOMPurify nests it as in one piece).
// Every run carries the document's reference links, as marked's parser expects of a token list.
function mdChunkGroups(tokens, chunkChars) {
  const groups = [];
  let cur = [];
  let size = 0;
  let depth = 0;
  const close = () => { cur.links = tokens.links; groups.push(cur); cur = []; size = 0; };
  for (const t of tokens) {
    cur.push(t);
    size += typeof t.raw === "string" ? t.raw.length : 0;
    depth = Math.max(0, depth + mdHtmlDepth(t));
    if (size >= chunkChars && depth === 0) close();
  }
  if (cur.length) close();
  return groups;
}

// mdOpenAtEnd: the last block of a piece lexed on its own is one that the rest of the text could have continued: a fenced code block
// with no closing fence, or an HTML block of MD_HTML_SPANS with no end. Those are the only blocks that run on past a blank line and a
// heading at the margin (every other one ends there, in marked 12 as in CommonMark), so a piece whose last block is neither lexes
// exactly as the same text does inside the whole document.
function mdOpenAtEnd(tokens) {
  let last = null;
  for (let i = tokens.length - 1; i >= 0 && !last; i--) if (tokens[i].type !== "space") last = tokens[i];
  if (!last || typeof last.raw !== "string") return false;
  if (last.type === "code") {
    const open = /^ {0,3}(`{3,}|~{3,})/.exec(last.raw);
    if (!open) return false; // indented code: a heading at the margin ends it
    const lines = last.raw.replace(/\n+$/, "").split("\n");
    const close = lines.length > 1 ? /^ {0,3}([`~]+) *$/.exec(lines[lines.length - 1]) : null;
    return !(close && close[1].startsWith(open[1]));
  }
  if (last.type === "html") {
    for (const k of MD_HTML_SPANS) {
      const m = k.open.exec(last.raw);
      if (m) return !k.close(m).test(last.raw.slice(m[0].length));
    }
  }
  return false;
}

// mdLexSteps lexes `text` as marked.lexer does, a step at a time (each yields null), and returns the tokens. The block pass runs a
// piece (mdLexPieces) per step, each with a Lexer of its own; a piece whose last block the text after it could have continued
// (mdOpenAtEnd: the cut fell inside a block mdLexPieces did not see, such as a fence inside an HTML block) is dropped, and everything
// from it on is lexed in one step, correct if slower. The reference links of the pieces are merged in document order, the first
// definition of a label winning as in marked, and the inline pass then runs over every piece's queue, in slices of about `chunkChars`,
// on one Lexer that holds them, so a link resolves wherever its definition is.
function* mdLexSteps(text, marked, chunkChars) {
  const pieces = mdLexPieces(text.replace(/\r\n|\r/g, "\n"), chunkChars);
  const main = new marked.Lexer(marked.defaults);
  const queue = [];
  for (let i = 0; i < pieces.length; i++) {
    let lx = new marked.Lexer(marked.defaults);
    lx.blockTokens(pieces[i], lx.tokens);
    if (i < pieces.length - 1 && mdOpenAtEnd(lx.tokens)) {
      lx = new marked.Lexer(marked.defaults);
      lx.blockTokens(pieces.slice(i).join(""), lx.tokens);
      i = pieces.length;
    }
    for (const t of lx.tokens) main.tokens.push(t);
    for (const k of Object.keys(lx.tokens.links)) if (!(k in main.tokens.links)) main.tokens.links[k] = lx.tokens.links[k];
    for (const q of lx.inlineQueue) queue.push(q);
    yield null;
  }
  let done = 0;
  for (const q of queue) {
    main.inlineTokens(q.src, q.tokens);
    done += q.src.length;
    if (done >= chunkChars) { done = 0; yield null; }
  }
  return main.tokens;
}

// mdChunks: the chunked render. A step that only lexed yields null (nothing to show yet); every other step yields one fragment.
function* mdChunks(text, deps) {
  const tokens = yield* mdLexSteps(text, deps.marked, deps.chunkChars);
  for (const group of mdChunkGroups(tokens, deps.chunkChars)) yield mdFinish(deps.marked.parser(group), deps);
}

// mdRenderFragment is the only place markdown becomes DOM: marked, then mdFinish (DOMPurify, highlight.js, the walk). `deps` =
// {marked, purify, hljs (optional), rewrite, where (optional), and, set by the dock only: hlLabelledOnly, hlMaxChars, chunkChars}.
// Throws when the markup cannot be made; the caller shows the file as plain text instead. The fragment is not yet in the page:
// appending it is what starts a load. With deps.chunkChars (a number of characters) it returns an iterator instead (mdChunks): its
// steps yield null or the next fragment, in order, and may throw like the one-shot render.
function mdRenderFragment(text, deps) {
  deps.marked.setOptions({ breaks: false, gfm: true }); // marked v12 removed the `highlight` option: the code is highlighted below
  if (deps.chunkChars > 0) return mdChunks(text, deps);
  return mdFinish(deps.marked.parse(text), deps);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    isLocalSrc, mdUnsafeAttr, MD_PURIFY, mdIsRelative, mdInertRef, mdUnsafeCss, mdUnsafeLink, mdWalkFragment, mdRenderFragment, MD_HL_MAX_CHARS,
    mdLexPieces, mdChunkGroups, mdLexSteps,
  };
}
