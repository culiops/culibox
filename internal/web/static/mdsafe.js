"use strict";

// What the markdown preview lets through. Markdown can be semi-untrusted (an agent
// writes it, a cloned repo ships it), so it is rendered with DOMPurify and its
// images may only come from this origin: a remote <img src> is a request the page
// makes for whoever wrote the file (a tracking beacon, an exfiltration channel).
// Pure and DOM-free so `node --test` reaches it; the preview walks img, video,
// audio and source elements and swaps every src that fails isLocalSrc.

// Passed as the config of DOMPurify.sanitize. Frozen, so a caller that tries to
// loosen it throws instead of quietly weakening every later render.
const MD_PURIFY = Object.freeze({
  FORBID_TAGS: Object.freeze(["script", "style", "iframe", "object", "embed", "form", "link", "meta", "base"]),
  FORBID_ATTR: Object.freeze(["srcset", "formaction"]),
});

// The two same-origin routes a file's bytes come from: /api/fs/read, and the
// per-session box route boxFileURL builds (fsops.js).
const MS_BOX_ROUTE = /^\/api\/sessions\/[^/?#]+\/boxfile\?/;

// The image types a data: URL may carry. Not svg (a script can ride in one), and
// the payload is base64 only, so nothing else can ride in the URL either.
const MS_DATA_IMAGE = /^data:image\/(?:png|gif|jpeg|webp);base64,[a-z0-9+\/=%]*$/i;

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
  const s = url.replace(/[\t\n\r]/g, "").replace(/^[\u0000-\u0020]+|[\u0000-\u0020]+$/g, "");
  if (s === "" || /[\u0000-\u001f\u007f\\]/.test(s)) return false;
  if (/^[a-z][a-z0-9+.-]*:/i.test(s)) return MS_DATA_IMAGE.test(s); // a scheme: only the inline images pass
  if (s.startsWith("//")) return false;
  if (s.startsWith("/")) return s.startsWith("/api/fs/read?") || MS_BOX_ROUTE.test(s);
  return true;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { isLocalSrc, MD_PURIFY };
}
