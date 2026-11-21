"use strict";

// The link gate (#348). Every link an agent can put in front of the user (terminal output, OSC 8, rendered markdown, Monaco's
// Ctrl/Cmd+click) goes through openAgentLink and nothing else opens a tab. It parses the link, works out what is odd about
// it, and asks before opening unless the link is clean https to a host the user said to stop asking about.
//
// What is here, top to bottom: the pure parts (parsing, the warnings, punycode, edit distance, trust rules; no DOM, so a test can
// require() this file), the trust cache, the dialog, and openAgentLink itself. The browser-only parts are reached only from
// openAgentLink and the DOMContentLoaded hook, so require() in node loads nothing but functions.
//
// Loaded as a classic script, so every top-level name is a shared global: all of them are lg/LG_-prefixed except openAgentLink,
// the one entry point the other files call. Two classic scripts declaring the same top-level const is a SyntaxError that blanks
// the whole page (links.js says the same).

// Well-known public shorteners. A static list: nothing is fetched, and a host not on it is simply not flagged.
const LG_SHORTENERS = new Set([
  "bit.ly", "bitly.com", "tinyurl.com", "t.co", "goo.gl", "ow.ly", "is.gd", "v.gd", "buff.ly", "rebrand.ly", "cutt.ly",
  "shorturl.at", "tiny.cc", "rb.gy", "t.ly", "bl.ink", "lnkd.in", "s.id", "tr.im", "x.co", "shorte.st", "adf.ly",
]);

// The hosts links.js rewrites to the page's own host (LINK_LOCAL_HOSTS). Kept as its own copy so this file loads on its own in a
// test; linkgate.test.js pins the two sets equal.
const LG_LOOPBACK = new Set(["localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]"]);

// The classic confusable scripts. Checked per label with \p{Script=...}; digits and "-" belong to no script and are ignored.
const LG_SCRIPT_NAMES = ["Latin", "Cyrillic", "Greek", "Armenian", "Hebrew", "Arabic", "Han"];
const LG_SCRIPT_RES = LG_SCRIPT_NAMES.map((n) => [n, new RegExp("\\p{Script=" + n + "}", "u")]);

// Another address inside a query value or the fragment: an optional scheme, then // and a character that is not a space.
const LG_REDIRECT_RE = /^\s*(?:[a-z][a-z0-9+.-]*:)?\/\/\S/i;

const LG_API = "/api/link-trust";
const LG_NOTICE = "CuliBox does not check where a link leads; your browser's own check still runs.";

// --- parsing ------------------------------------------------------------------------------------------------------------------

// lgParse returns the URL, or null for anything that is not an absolute http or https link. That is also a relative markdown
// link, mailto:, file:, javascript: and data:, which the gate never opens.
function lgParse(raw) {
  if (typeof raw !== "string") return null;
  let u;
  try {
    u = new URL(raw);
  } catch (_) {
    return null;
  }
  return u.protocol === "http:" || u.protocol === "https:" ? u : null;
}

// lgHostKey is the host as the trust list spells it: lower-case ASCII, no trailing dot. URL.hostname is already lower-cased and
// punycoded by the WHATWG parser, but it keeps a trailing dot, which is the same host.
function lgHostKey(u) {
  return u.hostname.toLowerCase().replace(/\.$/, "");
}

function lgIsLoopbackHost(h) {
  return LG_LOOPBACK.has(String(h || "").toLowerCase());
}

// --- punycode (RFC 3492) --------------------------------------------------------------------------------------------------------

// Browsers have no API that turns an xn-- label back into letters, and URL.hostname is always ASCII. This is the RFC's decoder,
// small enough to read against the document. Any malformed input returns null.
function lgPunyDecodeRaw(input) {
  const BASE = 36, T_MIN = 1, T_MAX = 26, SKEW = 38, DAMP = 700, MAX_INT = 0x7fffffff;
  const adapt = (delta, points, first) => {
    let k = 0;
    delta = first ? Math.floor(delta / DAMP) : delta >> 1;
    delta += Math.floor(delta / points);
    for (; delta > ((BASE - T_MIN) * T_MAX) >> 1; k += BASE) delta = Math.floor(delta / (BASE - T_MIN));
    return k + Math.floor(((BASE - T_MIN + 1) * delta) / (delta + SKEW));
  };
  const digitOf = (c) => (c >= 48 && c <= 57 ? c - 22 : c >= 65 && c <= 90 ? c - 65 : c >= 97 && c <= 122 ? c - 97 : BASE);
  const out = [];
  let basic = input.lastIndexOf("-");
  if (basic < 0) basic = 0;
  for (let j = 0; j < basic; j++) {
    if (input.charCodeAt(j) >= 0x80) return null;
    out.push(input.charCodeAt(j));
  }
  let n = 128, i = 0, bias = 72;
  for (let idx = basic > 0 ? basic + 1 : 0; idx < input.length; ) {
    const oldi = i;
    for (let w = 1, k = BASE; ; k += BASE) {
      if (idx >= input.length) return null;
      const digit = digitOf(input.charCodeAt(idx++));
      if (digit >= BASE || digit > Math.floor((MAX_INT - i) / w)) return null;
      i += digit * w;
      const t = k <= bias ? T_MIN : k >= bias + T_MAX ? T_MAX : k - bias;
      if (digit < t) break;
      if (w > Math.floor(MAX_INT / (BASE - t))) return null;
      w *= BASE - t;
    }
    const len = out.length + 1;
    bias = adapt(i - oldi, len, oldi === 0);
    if (Math.floor(i / len) > MAX_INT - n) return null;
    n += Math.floor(i / len);
    i %= len;
    if (n > 0x10ffff) return null;
    out.splice(i++, 0, n);
  }
  return String.fromCodePoint(...out);
}

// lgPunyDecode turns one xn-- label into letters. Anything that is not an xn-- label, or does not decode, comes back as it came:
// the dialog then shows the ASCII form, and the idn warning still fires.
function lgPunyDecode(label) {
  if (!/^xn--/i.test(label) || label.length === 4) return label;
  let s = null;
  try {
    s = lgPunyDecodeRaw(label.slice(4));
  } catch (_) {}
  return s === null ? label : s;
}

// lgUnicodeHost is the host as a person would read it. An IPv6 literal is left alone.
function lgUnicodeHost(hostname) {
  if (hostname.startsWith("[")) return hostname;
  return hostname.split(".").map(lgPunyDecode).join(".");
}

// --- edit distance and scripts --------------------------------------------------------------------------------------------------

// lgOsa is the optimal string alignment distance: insert, delete, substitute, or swap two neighbours, each 1. By code point, so
// a letter outside the BMP is one letter. Not full Damerau-Levenshtein ("ca" to "abc" is 3 here, 2 there); the typosquatting
// studies the lookalike rule leans on use the restricted form too.
function lgOsa(a, b) {
  const s = Array.from(a), t = Array.from(b);
  const w = t.length + 1;
  let prev2 = null;
  let prev = new Array(w);
  for (let j = 0; j < w; j++) prev[j] = j;
  for (let i = 1; i <= s.length; i++) {
    const cur = new Array(w);
    cur[0] = i;
    for (let j = 1; j < w; j++) {
      const cost = s[i - 1] === t[j - 1] ? 0 : 1;
      let d = Math.min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + cost);
      if (prev2 && i > 1 && j > 1 && s[i - 1] === t[j - 2] && s[i - 2] === t[j - 1]) d = Math.min(d, prev2[j - 2] + 1);
      cur[j] = d;
    }
    prev2 = prev;
    prev = cur;
  }
  return prev[w - 1];
}

// lgScripts lists the scripts (of the classic confusable seven) a label's letters come from, in order of first appearance.
function lgScripts(label) {
  const seen = [];
  for (const ch of label) {
    for (const [name, re] of LG_SCRIPT_RES) {
      if (re.test(ch)) {
        if (!seen.includes(name)) seen.push(name);
        break;
      }
    }
  }
  return seen;
}

// --- addresses ------------------------------------------------------------------------------------------------------------------

function lgIsIPv4(h) {
  return /^\d{1,3}(?:\.\d{1,3}){3}$/.test(h);
}

function lgIsIPHost(h) {
  return lgIsIPv4(h) || h.startsWith("[");
}

// The eight 16-bit groups of an IPv6 literal as URL.hostname prints it ([::1], [fe80::1], [::ffff:7f00:1]), or null.
function lgIPv6Groups(h) {
  const s = h.replace(/^\[|\]$/g, "");
  const halves = s.split("::");
  if (halves.length > 2) return null;
  const part = (p) => (p === "" ? [] : p.split(":").map((x) => (/^[0-9a-f]{1,4}$/i.test(x) ? parseInt(x, 16) : NaN)));
  const left = part(halves[0]);
  const right = halves.length === 2 ? part(halves[1]) : [];
  let g;
  if (halves.length === 1) {
    if (left.length !== 8) return null;
    g = left;
  } else {
    const fill = 8 - left.length - right.length;
    if (fill < 1) return null;
    g = left.concat(new Array(fill).fill(0), right);
  }
  return g.some((n) => !(n >= 0 && n <= 0xffff)) ? null : g;
}

function lgIsPrivateIPv4(a, b) {
  return a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 100 && b >= 64 && b <= 127) || (a === 169 && b === 254);
}

// lgIsPrivateIP: the ranges named in the spec (10/8, 172.16/12, 192.168/16, 100.64/10, 169.254/16, 127/8, fc00::/7, fe80::/10,
// ::1), and an IPv4-mapped IPv6 address by its IPv4 half. A literal that does not parse is not called private.
function lgIsPrivateIP(h) {
  if (lgIsIPv4(h)) {
    const p = h.split(".").map(Number);
    return lgIsPrivateIPv4(p[0], p[1]);
  }
  const g = lgIPv6Groups(h);
  if (!g) return false;
  if (g.slice(0, 7).every((n) => n === 0) && g[7] === 1) return true;
  if ((g[0] & 0xfe00) === 0xfc00 || (g[0] & 0xffc0) === 0xfe80) return true;
  if (g.slice(0, 5).every((n) => n === 0) && g[5] === 0xffff) return lgIsPrivateIPv4(g[6] >> 8, g[6] & 0xff);
  return false;
}

// --- warnings -------------------------------------------------------------------------------------------------------------------

// A warning is {kind, text, parts}. `parts` is the text in pieces, a string or {mono: "..."}, so the dialog can set a host or an
// address in the mono face; `text` is the same sentence joined.
function lgWarn(kind, ...parts) {
  return { kind, parts, text: parts.map((p) => (typeof p === "string" ? p : p.mono)).join("") };
}

function lgNameList(names) {
  return names.length < 2 ? names.join("") : names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}

function lgIdnWarning(u) {
  const labels = u.hostname.split(".");
  if (!labels.some((l) => /^xn--/i.test(l))) return null;
  const decoded = labels.map(lgPunyDecode);
  for (const l of decoded) {
    const scripts = lgScripts(l);
    if (scripts.length > 1) return lgWarn("idn", "The host mixes " + lgNameList(scripts) + " letters.");
  }
  const chars = [];
  for (const ch of decoded.join(".")) if (ch.charCodeAt(0) > 127 && !chars.includes(ch)) chars.push(ch);
  if (!chars.length) return lgWarn("idn", "The host uses an encoded (xn--) name that cannot be read as letters.");
  return lgWarn("idn", "The host uses non-ASCII letters (", { mono: chars.slice(0, 5).join(", ") }, ") that can look like ASCII ones.");
}

function lgIsShortener(host) {
  const labels = host.split(".");
  for (let i = 0; i + 2 <= labels.length; i++) if (LG_SHORTENERS.has(labels.slice(i).join("."))) return true;
  return false;
}

function lgCarriesAddress(u) {
  for (const v of u.searchParams.values()) if (LG_REDIRECT_RE.test(v)) return true;
  let frag = u.hash.replace(/^#/, "");
  try {
    frag = decodeURIComponent(frag);
  } catch (_) {}
  return LG_REDIRECT_RE.test(frag);
}

// lgLookalike: for the first trusted host T that this host is not (and is not a subdomain of) but looks like: one edit away
// from T in its Unicode form, or starting with `T.` or `T-` (github.com.evil.example, github.com-login.example).
function lgLookalike(host, trusted) {
  for (const e of trusted || []) {
    const t = e && e.host;
    if (typeof t !== "string" || !t || host === t || host.endsWith("." + t)) continue;
    if (Math.abs(host.length - t.length) <= 1 && lgOsa(host, t) === 1) return lgWarn("lookalike", "One letter away from ", { mono: t }, ", a host you trust.");
    if (host.startsWith(t + ".") || host.startsWith(t + "-")) return lgWarn("lookalike", "Starts with ", { mono: t }, " but is a different host.");
  }
  return null;
}

// lgWarnings: one line per odd thing, in this order, and no line for a clean link (never a "safe" label). opts.loopback turns
// off the ip, http and port lines: a localhost link is rewritten to the page's own host (links.js), where those three would fire
// on every dev-server click and teach the user to ignore them. opts.trusted is the trust list, for the lookalike line.
function lgWarnings(u, opts) {
  const o = opts || {};
  const out = [];
  const uni = lgUnicodeHost(u.hostname);
  if (u.username || u.password) {
    out.push(lgWarn("userinfo", { mono: u.username + (u.password ? ":" + u.password : "") + "@" }, " is a user name, not the host. This link goes to ", { mono: uni }, "."));
  }
  const idn = lgIdnWarning(u);
  if (idn) out.push(idn);
  if (!o.loopback) {
    if (lgIsIPHost(u.hostname)) {
      out.push(lgWarn("ip", lgIsPrivateIP(u.hostname) ? "The host is a private network address." : "The host is an IP address, not a name."));
    }
    if (u.port !== "") out.push(lgWarn("port", "Port " + u.port + " is not the usual port for " + u.protocol.replace(":", "") + "."));
    if (u.protocol === "http:") out.push(lgWarn("http", "Plain http: the page is not encrypted and can be changed on the way."));
  }
  if (lgIsShortener(u.hostname)) out.push(lgWarn("shortener", { mono: u.hostname }, " is a link shortener: the real destination is hidden."));
  if (lgCarriesAddress(u)) out.push(lgWarn("redirect", "The link carries another address in it, so it may forward you elsewhere."));
  if (!o.loopback) {
    const look = lgLookalike(uni.replace(/\.$/, ""), o.trusted); // a trailing dot is the same host, not one letter more
    if (look) out.push(look);
  }
  return out;
}

// --- trust rules ----------------------------------------------------------------------------------------------------------------

// lgTrustMatch: an entry matches its own host, and its subdomains when it says so. "evilgithub.com" is neither: the test is the
// whole label boundary, "." + host.
function lgTrustMatch(entries, host) {
  const h = String(host || "").toLowerCase().replace(/\.$/, "");
  if (!h || !Array.isArray(entries)) return false;
  return entries.some((e) => e && typeof e.host === "string" && (h === e.host || (!!e.subdomains && h.endsWith("." + e.host))));
}

// lgUntrustableWhy is "" for a link the dialog may offer to trust, else the first reason it may not (in the dialog's words):
// clean https to a named, public, non-xn-- host with a dot. The server checks the same rules again on add.
function lgUntrustableWhy(u, warnings, loopback) {
  const host = lgHostKey(u);
  if (loopback) return "it is a localhost link";
  if (lgIsIPHost(u.hostname)) return "its host is an IP address";
  if (host.split(".").some((l) => /^xn--/i.test(l))) return "its host is an xn-- name";
  if (u.protocol !== "https:") return "it is not https";
  if (!host.includes(".")) return "its host has no dot";
  if (warnings.length) return "it has a warning above";
  return "";
}

function lgTrustable(u, warnings, loopback) {
  return lgUntrustableWhy(u, warnings, loopback) === "";
}

// lgUrlParts splits a link for the dialog's box. The host is the one that is really contacted (the ASCII form); the Unicode
// form goes in the meta line.
function lgUrlParts(u) {
  return {
    scheme: u.protocol + "//",
    userinfo: u.username || u.password ? u.username + (u.password ? ":" + u.password : "") + "@" : "",
    host: u.hostname,
    port: u.port ? ":" + u.port : "",
    rest: u.pathname + u.search + u.hash,
  };
}

// --- the decision ---------------------------------------------------------------------------------------------------------------

// lgBuildView is everything the gate decides about one link, with no DOM: ok is false for a link that is not http(s). The
// loopback test is on the link as written, before links.js rewrites it, so a rewritten dev-server link stays "localhost" here.
// What opens is u.href, the same URL the dialog shows, never the raw text.
function lgBuildView(raw, ctx, pageHost, trusted) {
  const first = lgParse(raw);
  if (!first) return { ok: false };
  const loopback = lgIsLoopbackHost(first.hostname);
  const url = typeof rewriteLocalhost === "function" ? rewriteLocalhost(raw, pageHost) : raw;
  const u = url === raw ? first : new URL(url);
  const list = loopback ? [] : trusted || [];
  const warnings = lgWarnings(u, { loopback, trusted: list });
  const host = lgHostKey(u);
  const why = lgUntrustableWhy(u, warnings, loopback);
  return {
    ok: true, ctx: ctx || {}, u, href: u.href, rewritten: url !== raw, loopback, pageHost: pageHost || "", warnings,
    host, unicodeHost: lgUnicodeHost(u.hostname), trustable: why === "", why,
    skip: u.protocol === "https:" && warnings.length === 0 && !loopback && lgTrustMatch(list, host),
  };
}

// --- the trust cache ------------------------------------------------------------------------------------------------------------

// The cache is read synchronously at the click: an awaited fetch before window.open can lose the click's transient activation
// (Safari is the strictest) and the popup would be blocked. So it is loaded ahead of time, and a failed load leaves it empty:
// every link then asks.
let lgTrustHosts = [];
let lgTrustBad = false;
let lgLoadSeq = 0;

function lgCleanHosts(list) {
  if (!Array.isArray(list)) return [];
  return list
    .filter((e) => e && typeof e.host === "string" && e.host)
    .map((e) => ({ host: e.host.toLowerCase(), subdomains: !!e.subdomains, added: typeof e.added === "string" ? e.added : "" }));
}

// lgTrustList is a copy of the cached entries, {host, subdomains, added}.
function lgTrustList() {
  return lgTrustHosts.slice();
}

// lgTrustCorrupt: the server found its list damaged, so it shows empty and refuses add and remove.
function lgTrustCorrupt() {
  return lgTrustBad;
}

// lgLoadTrust fetches the list and resolves to it. Only the newest load may write the cache, so a slow older answer cannot
// bring back an entry that a later load already saw removed.
async function lgLoadTrust() {
  const mine = ++lgLoadSeq;
  let hosts = [];
  let bad = false;
  try {
    const res = await fetch(LG_API, { credentials: "same-origin" });
    if (res.ok) {
      const body = await res.json();
      hosts = lgCleanHosts(body && body.hosts);
      bad = !!(body && body.corrupt);
    }
  } catch (_) {}
  if (mine === lgLoadSeq) {
    lgTrustHosts = hosts;
    lgTrustBad = bad;
  }
  return lgTrustList();
}

// lgSendTrust is a POST or DELETE with a JSON body. A refusal throws an Error carrying the server's sentence, its HTTP status and
// its machine code; on success the cache reloads.
async function lgSendTrust(method, body) {
  const res = await fetch(LG_API, { method, credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  if (!res.ok) {
    let msg = res.statusText || "request failed";
    let code = "";
    try {
      const j = await res.json();
      msg = j.error || msg;
      code = j.code || "";
    } catch (_) {}
    const err = new Error(msg);
    err.status = res.status;
    err.code = code;
    throw err;
  }
  await lgLoadTrust();
}

function lgAddTrust(host, subdomains) {
  return lgSendTrust("POST", { host, subdomains: !!subdomains });
}

// lgRemoveTrust is for the Settings tab: it throws on a refusal, so the row can say why.
function lgRemoveTrust(host) {
  return lgSendTrust("DELETE", { host });
}

// --- the dialog -----------------------------------------------------------------------------------------------------------------

function lgEl(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
}

// lgFill puts pieces (a string, or {mono}) into `el` as text nodes and spans. textContent only: link text is hostile input.
function lgFill(el, pieces) {
  for (const p of pieces) {
    if (typeof p === "string") el.appendChild(document.createTextNode(p));
    else el.appendChild(lgEl("span", "lk-mono", p.mono));
  }
  return el;
}

function lgSourcePieces(ctx, first) {
  const from = first ? "From " : "from ";
  const where = ctx && ctx.where ? String(ctx.where) : "";
  if (ctx && ctx.source === "terminal") return where ? [from + "the terminal of ", { mono: where }] : [from + "the terminal"];
  if (ctx && ctx.source === "markdown") return where ? [from, { mono: where }] : [from + "a markdown file"];
  if (ctx && ctx.source === "editor") return [from + "the Files editor"];
  return [];
}

// lgMetaPieces: the source, the Unicode and real forms of a host that differs, and the localhost note. A clean link's notice
// follows in the same muted paragraph (the mockups), so its last sentence ends with a period; a warned link's notice goes below.
function lgMetaPieces(v) {
  const segs = [];
  if (v.unicodeHost !== v.u.hostname) segs.push(["Host ", { mono: v.unicodeHost }], ["real form ", { mono: v.u.hostname }]);
  const src = lgSourcePieces(v.ctx, segs.length === 0);
  if (src.length) segs.push(src);
  const out = [];
  segs.forEach((s, i) => out.push(...(i ? [" · "] : []), ...s));
  const note = v.loopback ? ["A localhost link: it opens on ", { mono: v.pageHost }, "."] : [];
  if (out.length && (note.length || !v.warnings.length)) out.push(".");
  if (out.length && note.length) out.push(" ");
  return out.concat(note);
}

function lgUrlNode(v) {
  const p = lgUrlParts(v.u);
  const box = lgEl("div", "lk-url");
  box.setAttribute("role", "group");
  box.setAttribute("aria-label", "Link address");
  box.tabIndex = 0;
  box.appendChild(lgEl("span", "lk-dim", p.scheme));
  if (p.userinfo) box.appendChild(lgEl("span", "lk-dim", p.userinfo));
  box.appendChild(lgEl("span", "lk-host", p.host));
  if (p.port) box.appendChild(lgEl("span", "lk-port", p.port));
  box.appendChild(lgEl("span", "lk-dim", p.rest));
  return box;
}

function lgWarnNode(w) {
  const row = lgEl("div", "lk-warn");
  const ico = lgEl("span", "lk-ico", "⚠︎");
  ico.setAttribute("aria-hidden", "true");
  row.appendChild(ico);
  row.appendChild(lgFill(lgEl("span", "lk-wt"), w.parts));
  return row;
}

// lgTrustNode: the "always open" box, and under it a subdomains box and the way back, both shown only once the first is ticked.
function lgTrustNode(v) {
  const wrap = lgEl("div", "lk-trust");
  const main = lgEl("input");
  main.type = "checkbox";
  const mainLabel = lgEl("label", "lk-check");
  mainLabel.appendChild(main);
  mainLabel.appendChild(lgFill(lgEl("span"), ["Always open links to ", { mono: v.host }, " without asking"]));
  const subs = lgEl("input");
  subs.type = "checkbox";
  const subLabel = lgEl("label", "lk-check");
  subLabel.appendChild(subs);
  subLabel.appendChild(lgFill(lgEl("span"), ["and its subdomains (", { mono: "*." + v.host }, ")"]));
  const sub = lgEl("div", "lk-sub");
  sub.hidden = true;
  sub.appendChild(subLabel);
  const hint = lgEl("p", "lk-hint", "You can remove it later in Settings › Trusted hosts.");
  hint.hidden = true;
  main.addEventListener("change", () => {
    sub.hidden = hint.hidden = !main.checked;
    if (!main.checked) subs.checked = false;
  });
  wrap.append(mainLabel, sub, hint);
  return { node: wrap, main, subs };
}

// lgDialog asks and resolves to {confirmed, trust, subdomains}. askDialog answers "cancel" when another question is already open
// (askIsBusy), and any failure here is a no: a link never opens without an answer. The boxes are read after the dialog closes,
// from the nodes this call made, so a later ask replacing the card's contents cannot change them.
async function lgDialog(v) {
  const no = { confirmed: false, trust: false, subdomains: false };
  if (typeof askDialog !== "function") return no;
  const warned = v.warnings.length > 0;
  const nodes = [lgUrlNode(v)];
  const pieces = lgMetaPieces(v);
  const meta = lgFill(lgEl("p", "lk-meta"), pieces);
  if (!warned) {
    if (pieces.length) meta.appendChild(document.createTextNode(" "));
    meta.appendChild(lgEl("span", "lk-notice", LG_NOTICE));
  }
  if (pieces.length || !warned) nodes.push(meta);
  for (const w of v.warnings) nodes.push(lgWarnNode(w));
  let boxes = null;
  if (v.trustable) {
    boxes = lgTrustNode(v);
    nodes.push(boxes.node);
  }
  if (warned) nodes.push(lgEl("p", "lk-note", "This link cannot be trusted: " + v.why + ". " + LG_NOTICE));
  let ans;
  try {
    ans = await askDialog({
      title: "Open this link?", cls: warned ? "link-warn" : "link", confirmLabel: warned ? "Open anyway" : "Open",
      focusCancel: warned, extra: nodes, wide: false,
    });
  } catch (_) {
    return no;
  }
  if (!ans || ans.outcome !== "confirm") return no;
  const trust = !!(boxes && boxes.main.checked);
  return { confirmed: true, trust, subdomains: trust && !!boxes.subs.checked };
}

// --- the gate -------------------------------------------------------------------------------------------------------------------

function lgToast(msg, opts) {
  if (typeof showToast === "function") showToast(msg, opts);
}

// noopener denies the opened page a handle back into the console; noreferrer keeps the console's host out of a third party's
// logs. The toast is the only signal that the page opened is not the link on screen (a localhost one), so it is not optional.
function lgOpen(v) {
  window.open(v.href, "_blank", "noopener,noreferrer");
  if (v.rewritten) lgToast("opened " + v.u.host + " instead of localhost");
}

// openAgentLink is the one way an agent-controlled link opens. ctx = {source: "terminal"|"markdown"|"editor", where?}.
// Resolves true when the link was opened. The clean, trusted case opens before any await, inside the click's own activation.
async function openAgentLink(raw, ctx) {
  const v = lgBuildView(raw, ctx, typeof location !== "undefined" ? location.hostname : "", lgTrustHosts);
  if (!v.ok) {
    lgToast("CuliBox opens only http and https links");
    return false;
  }
  if (v.skip) {
    lgOpen(v);
    lgLoadTrust(); // background: the next click sees what another window changed
    return true;
  }
  lgLoadTrust();
  const ans = await lgDialog(v);
  if (!ans.confirmed) return false;
  lgOpen(v);
  if (ans.trust) {
    try {
      await lgAddTrust(v.host, ans.subdomains);
    } catch (e) {
      lgToast("Could not save " + v.host + " as trusted: " + (e && e.message ? e.message : "request failed"), { error: true });
    }
  }
  return true;
}

if (typeof document !== "undefined" && typeof document.addEventListener === "function") {
  document.addEventListener("DOMContentLoaded", () => { lgLoadTrust(); });
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    lgParse, lgHostKey, lgIsLoopbackHost, lgPunyDecode, lgUnicodeHost, lgOsa, lgScripts, lgIsIPHost, lgIsPrivateIP, lgWarnings,
    lgTrustMatch, lgTrustable, lgUntrustableWhy, lgUrlParts, lgBuildView, lgTrustList, lgTrustCorrupt, lgLoadTrust, lgAddTrust,
    lgRemoveTrust, openAgentLink,
  };
}
