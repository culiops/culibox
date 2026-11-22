"use strict";
// The link gate (#348): the pure parts (warnings, punycode, edit distance, trust rules) straight from require(), and the gate itself
// (openAgentLink, its dialog and its trust cache) in a vm page with stubs for askDialog, window.open, showToast, fetch and location.
// The repo has no jsdom: the dialog's nodes are the shared fake DOM, and the stub askDialog reads what the gate handed it.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

Node.prototype.addEventListener = function (t, f) { ((this.listeners = this.listeners || {})[t] = this.listeners[t] || []).push(f); };
Node.prototype.fire = function (t, ev) { for (const f of (this.listeners || {})[t] || []) f(ev || {}); };

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const lg = require("../static/linkgate.js");
const tick = () => new Promise((r) => setImmediate(r));
// A vm array is not this realm's Array, so deepStrictEqual would fail on the prototype: compare as JSON.
const plain = (x) => JSON.parse(JSON.stringify(x));

const warnsOf = (raw, opts) => lg.lgWarnings(new URL(raw), opts || {});
const kindsOf = (raw, opts) => warnsOf(raw, opts).map((w) => w.kind);

// ---- pure: parse, punycode, distance --------------------------------------------------------------------------------------------

test("lgParse takes http and https only", () => {
  assert.ok(lg.lgParse("https://example.com/a"));
  assert.ok(lg.lgParse("http://example.com"));
  for (const bad of ["mailto:a@b.c", "file:///etc/passwd", "javascript:alert(1)", "data:text/html,x", "./docs/a.md", "/abs/path", "", "ftp://x.org", "ssh://x.org"]) {
    assert.strictEqual(lg.lgParse(bad), null, bad);
  }
  assert.strictEqual(lg.lgParse(undefined), null);
  assert.strictEqual(lg.lgParse(42), null);
});

test("lgPunyDecode: RFC 3492 labels, and invalid input comes back as it came", () => {
  assert.strictEqual(lg.lgPunyDecode("xn--gthub-zsa"), "gíthub");
  assert.strictEqual(lg.lgPunyDecode("xn--mller-kva"), "müller");
  assert.strictEqual(lg.lgPunyDecode("xn--pypal-4ve"), "pаypal"); // Cyrillic а
  assert.strictEqual(lg.lgPunyDecode("xn--bcher-kva"), "bücher");
  assert.strictEqual(lg.lgPunyDecode("xn--80ak6aa92e"), "аррӏе");
  assert.strictEqual(lg.lgPunyDecode("xn--!!"), "xn--!!");
  assert.strictEqual(lg.lgPunyDecode("xn--"), "xn--");
  assert.strictEqual(lg.lgPunyDecode("xn--99999999999999"), "xn--99999999999999");
  assert.strictEqual(lg.lgPunyDecode("plain"), "plain"); // not an xn-- label: nothing to decode
});

test("lgUnicodeHost decodes each xn-- label and leaves the rest", () => {
  assert.strictEqual(lg.lgUnicodeHost("xn--gthub-zsa.com"), "gíthub.com");
  assert.strictEqual(lg.lgUnicodeHost("www.xn--mller-kva.de"), "www.müller.de");
  assert.strictEqual(lg.lgUnicodeHost("github.com"), "github.com");
  assert.strictEqual(lg.lgUnicodeHost("[::1]"), "[::1]");
  assert.strictEqual(lg.lgUnicodeHost("xn--!!.com"), "xn--!!.com");
});

test("lgOsa is the optimal string alignment distance", () => {
  assert.strictEqual(lg.lgOsa("", ""), 0);
  assert.strictEqual(lg.lgOsa("abc", "abc"), 0);
  assert.strictEqual(lg.lgOsa("", "abc"), 3);
  assert.strictEqual(lg.lgOsa("abc", ""), 3);
  assert.strictEqual(lg.lgOsa("github.com", "gíthub.com"), 1); // substitution
  assert.strictEqual(lg.lgOsa("github.com", "githb.com"), 1); // deletion
  assert.strictEqual(lg.lgOsa("github.com", "githubb.com"), 1); // insertion
  assert.strictEqual(lg.lgOsa("github.com", "gihtub.com"), 1); // adjacent transposition counts as one
  assert.strictEqual(lg.lgOsa("github.com", "gitlab.com"), 2); // two real hosts: the reason distance 2 is not a lookalike
  assert.strictEqual(lg.lgOsa("kitten", "sitting"), 3);
  assert.strictEqual(lg.lgOsa("ca", "abc"), 3); // OSA, not full Damerau-Levenshtein (that is 2)
  assert.strictEqual(lg.lgOsa("a😀b", "a😀c"), 1); // by code point, not UTF-16 unit
});

test("lgScripts and mixed scripts per label", () => {
  assert.deepStrictEqual(plain(lg.lgScripts("github")), ["Latin"]);
  assert.deepStrictEqual(plain(lg.lgScripts("pаypal")), ["Latin", "Cyrillic"]); // Cyrillic а in a Latin word
  assert.deepStrictEqual(plain(lg.lgScripts("аррӏе")), ["Cyrillic"]);
  assert.deepStrictEqual(plain(lg.lgScripts("abc-123")), ["Latin"]); // digits and - are ignored
  assert.deepStrictEqual(plain(lg.lgScripts("123-45")), []);
  assert.deepStrictEqual(plain(lg.lgScripts("αβc")), ["Greek", "Latin"]);
  assert.deepStrictEqual(plain(lg.lgScripts("例えa")), ["Han", "Latin"]);
});

// ---- pure: warnings --------------------------------------------------------------------------------------------------------------

test("no warning: a plain https link has no line at all", () => {
  assert.deepStrictEqual(plain(warnsOf("https://grafana.example.com/d/abc")), []);
  assert.deepStrictEqual(plain(warnsOf("https://github.com/culiops/culibox/pull/412?diff=split#files")), []);
  assert.deepStrictEqual(plain(warnsOf("https://example.com/?next=/home&q=a:b")), []); // a path and a colon are not another address
});

test("userinfo: the text names the real host first", () => {
  const w = warnsOf("https://github.com@evil.example/login");
  assert.deepStrictEqual(w.map((x) => x.kind), ["userinfo"]);
  assert.strictEqual(w[0].text, "This link goes to evil.example. github.com@ before it is a user name, not the host.");
  assert.strictEqual(warnsOf("https://user:pw@evil.example/")[0].text, "This link goes to evil.example. user:pw@ before it is a user name, not the host.");
  // the host named is the Unicode one
  assert.match(warnsOf("https://github.com@xn--gthub-zsa.com/x").find((x) => x.kind === "userinfo").text, /^This link goes to gíthub\.com\. /);
  // the host is a mono part, so the dialog sets it in the host's face
  assert.deepStrictEqual(plain(w[0].parts), ["This link goes to ", { mono: "evil.example" }, ". ", { mono: "github.com@" }, " before it is a user name, not the host."]);
});

test("userinfo: a long one is cut to 40 characters and an ellipsis in the warning and in the box, never in the link that opens", () => {
  const long = "a".repeat(200);
  const u = new URL("https://" + long + "@evil.example/login");
  const w = lg.lgWarnings(u, {}).find((x) => x.kind === "userinfo");
  assert.strictEqual(w.text, "This link goes to evil.example. " + "a".repeat(40) + "…@ before it is a user name, not the host.");
  assert.strictEqual(lg.lgUrlParts(u).userinfo, "a".repeat(40) + "…@");
  assert.strictEqual(lg.lgBuildView(u.href, {}, "box.test", []).href, u.href, "window.open still gets the full link");
  // exactly 40 is whole; a password counts in the same 40
  assert.strictEqual(lg.lgUrlParts(new URL("https://" + "a".repeat(40) + "@x.example/")).userinfo, "a".repeat(40) + "@");
  assert.strictEqual(lg.lgUrlParts(new URL("https://u:" + "p".repeat(60) + "@x.example/")).userinfo, "u:" + "p".repeat(38) + "…@");
});

test("idn: mixed scripts name the scripts, otherwise the non-ASCII letters", () => {
  const mixed = warnsOf("https://xn--pypal-4ve.com/").find((x) => x.kind === "idn");
  assert.strictEqual(mixed.text, "The host mixes Latin and Cyrillic letters.");
  const plainIdn = warnsOf("https://xn--gthub-zsa.com/").find((x) => x.kind === "idn");
  assert.strictEqual(plainIdn.text, "The host uses non-ASCII letters (í) that can look like ASCII ones.");
  // a wholly Cyrillic label is not mixed: it lists the letters
  assert.match(warnsOf("https://xn--80ak6aa92e.com/").find((x) => x.kind === "idn").text, /^The host uses non-ASCII letters \(.+\) that can look like ASCII ones\.$/);
  // at most 5 distinct letters
  const many = warnsOf("https://xn--80ak6aa92e.com/").find((x) => x.kind === "idn").text;
  assert.ok(/\(([^)]*)\)/.exec(many)[1].split(", ").length <= 5);
  // a label that does not decode still warns. Whether the URL parser accepts such a label at all differs between Node patch
  // versions (24.14 throws, 24.22 parses); when it refuses, the gate must refuse the link too.
  let u = null;
  try { u = new URL("https://xn--a.com/"); } catch (_) {}
  if (u) assert.ok(lg.lgWarnings(u, {}).some((x) => x.kind === "idn"));
  else assert.strictEqual(lg.lgParse("https://xn--a.com/"), null);
  // the same rule, independent of the parser: a hand-made URL with that hostname
  const bad = lg.lgWarnings({ hostname: "xn--a.com", protocol: "https:", port: "", username: "", password: "", href: "https://xn--a.com/", pathname: "/", search: "", hash: "", searchParams: new URLSearchParams() }, {});
  assert.ok(bad.some((x) => x.kind === "idn"));
});

test("ip: private ranges say so, a public address says it is not a name", () => {
  const PRIVATE = "The host is a private network address.";
  const PUBLIC = "The host is an IP address, not a name.";
  for (const h of ["10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1", "100.127.255.255", "169.254.1.1", "127.0.0.2", "[fc00::1]", "[fd12:3456::1]", "[fe80::1]", "[febf::1]", "[::1]", "[::ffff:a00:1]"]) {
    const w = warnsOf("https://" + h + "/").find((x) => x.kind === "ip");
    assert.ok(w, h);
    assert.strictEqual(w.text, PRIVATE, h);
  }
  for (const h of ["8.8.8.8", "172.32.0.1", "172.15.0.1", "100.128.0.1", "100.63.0.1", "192.169.0.1", "1.1.1.1", "[2606:4700::1111]", "[fec0::1]", "[::ffff:808:808]"]) {
    const w = warnsOf("https://" + h + "/").find((x) => x.kind === "ip");
    assert.ok(w, h);
    assert.strictEqual(w.text, PUBLIC, h);
  }
  // an integer or hex host the URL parser turns into an address is still an address
  assert.ok(kindsOf("https://2130706433/").includes("ip"));
  assert.ok(!kindsOf("https://example.com/").includes("ip"));
  assert.ok(!kindsOf("https://10.example.com/").includes("ip"));
});

test("port: any explicit port that is not the scheme's default", () => {
  assert.strictEqual(warnsOf("https://example.com:8443/").find((x) => x.kind === "port").text, "Port 8443 is not the usual port for https.");
  assert.strictEqual(warnsOf("http://example.com:8080/").find((x) => x.kind === "port").text, "Port 8080 is not the usual port for http.");
  assert.ok(!kindsOf("https://example.com:443/").includes("port")); // the URL parser drops a default port
  assert.ok(!kindsOf("http://example.com:80/").includes("port"));
  assert.ok(kindsOf("https://example.com:80/").includes("port")); // 80 is not https's default
});

test("http: plain http warns, https does not", () => {
  assert.strictEqual(warnsOf("http://example.com/").find((x) => x.kind === "http").text, "Plain http: the page is not encrypted and can be changed on the way.");
  assert.ok(!kindsOf("https://example.com/").includes("http"));
});

test("shortener: the host or a parent of it is on the list", () => {
  assert.strictEqual(warnsOf("https://bit.ly/3abc").find((x) => x.kind === "shortener").text, "bit.ly is a link shortener: the real destination is hidden.");
  assert.ok(kindsOf("https://www.bit.ly/3abc").includes("shortener"));
  assert.ok(kindsOf("https://t.co/xyz").includes("shortener"));
  for (const s of "bit.ly bitly.com tinyurl.com t.co goo.gl ow.ly is.gd v.gd buff.ly rebrand.ly cutt.ly shorturl.at tiny.cc rb.gy t.ly bl.ink lnkd.in s.id tr.im x.co shorte.st adf.ly".split(" ")) {
    assert.ok(kindsOf("https://" + s + "/x").includes("shortener"), s);
  }
  assert.ok(!kindsOf("https://notbit.ly/x").includes("shortener")); // a different host that merely ends in the letters
  assert.ok(!kindsOf("https://bit.ly.example.com/x").includes("shortener"));
});

test("redirect: another address in a query value or the fragment", () => {
  const TEXT = "The link carries another address in it, so it may forward you elsewhere.";
  assert.strictEqual(warnsOf("https://example.com/out?url=https%3A%2F%2Fevil.example%2F").find((x) => x.kind === "redirect").text, TEXT);
  assert.ok(kindsOf("https://example.com/out?next=//evil.example").includes("redirect"));
  assert.ok(kindsOf("https://example.com/out?to=HTTP://evil.example").includes("redirect"));
  assert.ok(kindsOf("https://example.com/out?to=%20ftp://evil.example").includes("redirect"));
  assert.ok(kindsOf("https://example.com/#//evil.example/x").includes("redirect"));
  assert.ok(kindsOf("https://example.com/#https%3A%2F%2Fevil.example").includes("redirect"));
  assert.ok(!kindsOf("https://example.com/out?next=/home").includes("redirect"));
  assert.ok(!kindsOf("https://example.com/out?n=a:b").includes("redirect"));
  assert.ok(!kindsOf("https://example.com/out?https://").includes("redirect")); // a scheme with nothing after // is not an address
  assert.ok(!kindsOf("https://example.com/#section-2").includes("redirect"));
  assert.ok(!kindsOf("https://example.com/a/https://b.example/c").includes("redirect")); // the path is not a query value
});

test("lookalike: one letter from a trusted host, or starting with it", () => {
  const trust = { trusted: [{ host: "github.com", subdomains: false }] };
  const one = warnsOf("https://githhub.com/x", trust).find((x) => x.kind === "lookalike");
  assert.strictEqual(one.text, "One letter away from github.com, a host you trust.");
  assert.ok(kindsOf("https://gihtub.com/x", trust).includes("lookalike")); // a swap
  assert.ok(kindsOf("https://xn--gthub-zsa.com/x", trust).includes("lookalike")); // the Unicode form is the one compared
  const starts = warnsOf("https://github.com.evil.example/x", trust).find((x) => x.kind === "lookalike");
  assert.strictEqual(starts.text, "Starts with github.com but is a different host.");
  assert.ok(kindsOf("https://github.com-login.example/x", trust).includes("lookalike"));
  // not a lookalike
  assert.ok(!kindsOf("https://github.com/x", trust).includes("lookalike")); // the host itself
  assert.ok(!kindsOf("https://gist.github.com/x", trust).includes("lookalike")); // a real subdomain
  assert.ok(!kindsOf("https://gitlab.com/x", trust).includes("lookalike")); // distance 2: two real hosts
  assert.ok(!kindsOf("https://evilgithub.com/x", trust).includes("lookalike"));
  assert.ok(!kindsOf("https://example.org/x", trust).includes("lookalike"));
  // nothing trusted: nothing to look like
  assert.ok(!kindsOf("https://githhub.com/x", {}).includes("lookalike"));
  // a loopback link is never a lookalike
  assert.ok(!kindsOf("https://githhub.com/x", { trusted: trust.trusted, loopback: true }).includes("lookalike"));
});

test("a loopback link gets no ip, http or port warning", () => {
  const w = kindsOf("http://192.168.1.5:3030/x", { loopback: true });
  assert.deepStrictEqual(plain(w), []);
  assert.deepStrictEqual(plain(kindsOf("http://192.168.1.5:3030/x", {})), ["ip", "port", "http"]);
  // the others still fire
  assert.ok(kindsOf("http://example.com:3030/?u=https://evil.example", { loopback: true }).includes("redirect"));
});

test("warnings come in the spec's order", () => {
  const raw = "http://github.com@bit.ly:8080/?u=https://evil.example";
  assert.deepStrictEqual(plain(kindsOf(raw, { trusted: [{ host: "bit.lx", subdomains: false }] })), ["userinfo", "port", "http", "shortener", "redirect", "lookalike"]);
  assert.deepStrictEqual(plain(kindsOf("http://u@xn--gthub-zsa.com:81/", {})), ["userinfo", "idn", "port", "http"]);
  assert.deepStrictEqual(plain(kindsOf("http://u@10.0.0.1:81/", {})), ["userinfo", "ip", "port", "http"]);
});

test("a warning's text is its parts joined, and mono parts are marked", () => {
  const w = warnsOf("https://github.com@evil.example/")[0];
  assert.strictEqual(w.parts.map((p) => (typeof p === "string" ? p : p.mono)).join(""), w.text);
  assert.ok(w.parts.some((p) => typeof p === "object" && p.mono === "github.com@"));
});

// ---- pure: trust rules ----------------------------------------------------------------------------------------------------------

test("lgTrustMatch: exact, subdomains on, subdomains off, and a host that merely ends in the letters", () => {
  const exact = [{ host: "github.com", subdomains: false }];
  const subs = [{ host: "github.com", subdomains: true }];
  assert.ok(lg.lgTrustMatch(exact, "github.com"));
  assert.ok(!lg.lgTrustMatch(exact, "gist.github.com"));
  assert.ok(lg.lgTrustMatch(subs, "github.com"));
  assert.ok(lg.lgTrustMatch(subs, "gist.github.com"));
  assert.ok(lg.lgTrustMatch(subs, "a.b.github.com"));
  assert.ok(!lg.lgTrustMatch(subs, "evilgithub.com"));
  assert.ok(!lg.lgTrustMatch(exact, "evilgithub.com"));
  assert.ok(!lg.lgTrustMatch(subs, "github.com.evil.example"));
  assert.ok(!lg.lgTrustMatch([], "github.com"));
  assert.ok(!lg.lgTrustMatch(null, "github.com"));
  assert.ok(lg.lgTrustMatch(subs, "GitHub.com")); // a hostname is lower-case already, but the rule does not depend on it
  assert.ok(lg.lgTrustMatch(subs, "github.com."), "a trailing dot is the same host"); // the URL parser keeps one
});

test("lgTrustable: only a clean https link to a named host with a dot", () => {
  const t = (raw, opts) => { const u = new URL(raw); const w = lg.lgWarnings(u, opts || {}); return lg.lgTrustable(u, w, !!(opts && opts.loopback)); };
  assert.strictEqual(t("https://github.com/x"), true);
  assert.strictEqual(t("https://gist.github.com/x"), true);
  assert.strictEqual(t("http://github.com/x"), false); // http
  assert.strictEqual(t("https://10.0.0.1/x"), false); // an IP
  assert.strictEqual(t("https://8.8.8.8/x"), false);
  assert.strictEqual(t("https://[2606:4700::1111]/x"), false);
  assert.strictEqual(t("https://xn--gthub-zsa.com/x"), false); // xn--
  assert.strictEqual(t("https://intranet/x"), false); // one label
  assert.strictEqual(t("https://localhost/x", { loopback: true }), false); // loopback
  assert.strictEqual(t("https://github.com:8443/x"), false); // any warning
  assert.strictEqual(t("https://bit.ly/x"), false);
  // the guard holds even if the caller's warning list were empty
  const u = new URL("https://10.0.0.1/x");
  assert.strictEqual(lg.lgTrustable(u, [], false), false);
  assert.strictEqual(lg.lgTrustable(new URL("https://xn--gthub-zsa.com/"), [], false), false);
  assert.strictEqual(lg.lgTrustable(new URL("https://intranet/"), [], false), false);
});

test("lgUntrustableWhy names the first reason", () => {
  const why = (raw, opts) => { const u = new URL(raw); const w = lg.lgWarnings(u, opts || {}); return lg.lgUntrustableWhy(u, w, !!(opts && opts.loopback)); };
  assert.strictEqual(why("https://github.com/x"), "");
  assert.match(why("https://xn--gthub-zsa.com/x"), /xn--/);
  assert.match(why("https://10.0.0.1/x"), /IP address/);
  assert.match(why("http://example.com/x"), /not https/);
  assert.match(why("https://localhost/x", { loopback: true }), /localhost/);
  assert.match(why("https://intranet/x"), /no dot/);
  assert.match(why("https://example.com:8443/x"), /warning/);
});

test("lgUntrustableWhy refuses the page's own host, so an agent-printed CuliBox link can never skip the dialog", () => {
  const why = (raw, pageHost) => { const u = new URL(raw); return lg.lgUntrustableWhy(u, lg.lgWarnings(u, {}), false, pageHost); };
  assert.match(why("https://box.example.com/s/1", "box.example.com"), /this CuliBox/);
  assert.match(why("https://BOX.example.com./s/1", "box.example.com"), /this CuliBox/, "case and a trailing dot are the same host");
  assert.match(why("https://box.example.com/", "Box.Example.Com."), /this CuliBox/);
  assert.strictEqual(why("https://other.example.com/", "box.example.com"), "");
  assert.strictEqual(why("https://example.com/", "box.example.com"), "", "the parent domain is another host");
  assert.strictEqual(why("https://box.example.com/", ""), "", "no page host, no rule");
  const v = lg.lgBuildView("https://box.example.com/x", {}, "box.example.com", []);
  assert.strictEqual(v.trustable, false);
});

test("lgUntrustableWhy mirrors the server's label rules, so the dialog never offers a trust the server refuses", () => {
  const why = (host, proto) => { const u = { hostname: host, protocol: proto || "https:" }; return lg.lgUntrustableWhy(u, [], false); };
  for (const host of ["foo_bar.example.com", "-a.example.com", "a-.example.com", "a.-b.example.com", "a..example.com", "a.123", "a.0x1f", "a." + "b".repeat(64) + ".com"]) {
    assert.notStrictEqual(why(host), "", host + " must not be offered");
    assert.strictEqual(lg.lgTrustable({ hostname: host, protocol: "https:" }, [], false), false, host);
  }
  // what the real parser lets through: an underscore and the hyphen edges survive new URL()
  for (const raw of ["https://foo_bar.example.com/x", "https://-a.example.com/x", "https://a-.example.com/x"]) {
    const u = new URL(raw);
    assert.notStrictEqual(lg.lgUntrustableWhy(u, lg.lgWarnings(u, {}), false), "", raw);
  }
  assert.match(why("foo_bar.example.com"), /letters, digits and hyphens/);
  assert.match(why("-a.example.com"), /hyphen/);
  assert.match(why("a-.example.com"), /hyphen/);
  assert.match(why("a.123"), /number/);
  // and the ones that are fine stay fine
  for (const host of ["a-b.example.com", "1a.example.com", "docs.example.co", "a.b2", "x.0xz", "x-1.example.com"]) assert.strictEqual(why(host), "", host);
});

test("lgUrlParts splits the link for the dialog's box", () => {
  const p = lg.lgUrlParts(new URL("https://user:pw@grafana.example.com:8443/d/abc?x=1#top"));
  assert.deepStrictEqual(plain(p), { scheme: "https://", userinfo: "user:pw@", host: "grafana.example.com", port: ":8443", rest: "/d/abc?x=1#top" });
  assert.deepStrictEqual(plain(lg.lgUrlParts(new URL("https://example.com"))), { scheme: "https://", userinfo: "", host: "example.com", port: "", rest: "/" });
  assert.strictEqual(lg.lgUrlParts(new URL("http://[::1]:3000/")).host, "[::1]");
  assert.strictEqual(lg.lgUrlParts(new URL("https://github.com@xn--gthub-zsa.com/")).host, "xn--gthub-zsa.com"); // the host that is really contacted
});

test("the loopback set is links.js's own", () => {
  const p = page();
  assert.deepStrictEqual([...p.run("LG_LOOPBACK")].sort(), [...p.run("LINK_LOCAL_HOSTS")].sort());
});

test("a trailing dot is the same host, not one letter more", () => {
  assert.ok(!kindsOf("https://github.com./x", { trusted: [{ host: "github.com", subdomains: false }] }).includes("lookalike"));
});

test("lgIsLoopbackHost", () => {
  for (const h of ["localhost", "LOCALHOST", "127.0.0.1", "0.0.0.0", "::1", "[::1]"]) assert.ok(lg.lgIsLoopbackHost(h), h);
  for (const h of ["127.0.0.2", "localhost.example.com", "example.com", ""]) assert.ok(!lg.lgIsLoopbackHost(h), h);
});

// ---- the gate --------------------------------------------------------------------------------------------------------------------

const DEFAULT_TRUST = [{ host: "github.com", subdomains: false, added: "2026-11-01T00:00:00Z" }, { host: "grafana.example.com", subdomains: true, added: "2026-11-02T00:00:00Z" }];

// One page: links.js and linkgate.js in one shared global scope, with the stubs the gate calls.
function page(over) {
  const o = Object.assign({ hostname: "box.tail.ts.net", hosts: DEFAULT_TRUST }, over);
  const doc = { listeners: {} };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  doc.addEventListener = (t, f) => { (doc.listeners[t] = doc.listeners[t] || []).push(f); };
  const server = { hosts: o.hosts, corrupt: false, failGet: false, failPost: null, calls: [], getDelay: null };
  const events = [];
  const box = {
    document: doc, console, JSON, Map, Set, Math, Promise, Array, Object, String, Number, Date, URL, URLSearchParams, Error, RegExp, Symbol,
    encodeURIComponent, decodeURIComponent,
    location: { hostname: o.hostname },
    module: { exports: {} },
    opened: [], toasts: [], asks: [], events,
    window: { open: (...a) => { box.opened.push(a); events.push("open"); } },
    showToast: (m, opts) => box.toasts.push([m, !!(opts && opts.error)]),
    askAnswer: () => ({ outcome: "cancel" }),
    askDialog: async (opts) => { box.asks.push(opts); return box.askAnswer(opts, box.asks.length - 1) || { outcome: "cancel" }; },
    fetch: async (url, init) => {
      const method = (init && init.method) || "GET";
      server.calls.push({ url, method, init: init || {}, body: init && init.body ? JSON.parse(init.body) : null });
      if (method === "GET") {
        events.push("get");
        // The answer is what the server held when the request ARRIVED, not when json() is read: a slow reply is an old one.
        const answer = { hosts: server.hosts, corrupt: server.corrupt, newer: !!server.newer };
        if (server.getDelay) await server.getDelay();
        if (server.failGet) throw new Error("offline");
        if (server.getStatus) return { ok: false, status: server.getStatus, statusText: "Bad", json: async () => ({ error: "no" }) };
        return { ok: true, status: 200, statusText: "OK", json: async () => answer };
      }
      events.push(method.toLowerCase());
      if (server.failPost) return { ok: false, status: server.failPost.status, statusText: "Bad", json: async () => ({ error: server.failPost.error }) };
      const b = JSON.parse(init.body);
      if (method === "POST") server.hosts = server.hosts.filter((h) => h.host !== b.host).concat([{ host: b.host, subdomains: !!b.subdomains, added: "2026-11-21T00:00:00Z" }]);
      else server.hosts = server.hosts.filter((h) => h.host !== b.host);
      return { ok: true, status: 200, statusText: "OK", json: async () => ({ hosts: server.hosts, corrupt: false }) };
    },
  };
  box.server = server;
  const ctx = vm.createContext(box);
  vm.runInContext(read("links.js"), ctx, { filename: "links.js" });
  vm.runInContext(read("linkgate.js"), ctx, { filename: "linkgate.js" });
  box.exports = box.module.exports;
  box.ready = async () => { for (const f of doc.listeners.DOMContentLoaded || []) await f(); await tick(); };
  box.open = (raw, ctxArg) => vm.runInContext("openAgentLink", ctx)(raw, ctxArg);
  box.run = (src) => vm.runInContext(src, ctx);
  return box;
}

// The answer a user gives: confirm or cancel, with the trust boxes ticked or not. Reads the nodes the gate handed to askDialog.
function user(box, a) {
  box.askAnswer = (opts) => {
    const boxes = [];
    for (const n of [].concat(opts.extra || [])) if (n.querySelectorAll) boxes.push(...n.querySelectorAll("input"));
    if (a.trust) { boxes[0].checked = true; boxes[0].fire("change"); }
    if (a.subs) { boxes[1].checked = true; boxes[1].fire("change"); }
    return { outcome: a.outcome || "confirm", value: "" };
  };
}
const textOf = (opts) => [].concat(opts.extra || []).map((n) => n.textContent).join("\n");
// The node itself counts too: .lk-url, .lk-meta and .lk-warn are the extra nodes, not their children.
const findAll = (opts, sel) => [].concat(opts.extra || []).flatMap((n) => (n.querySelectorAll ? (n.is(sel) ? [n] : []).concat(n.querySelectorAll(sel)) : []));

test("the script is a classic script: every top-level name but openAgentLink is lg-prefixed", () => {
  const src = read("linkgate.js");
  const names = [...src.matchAll(/^(?:async\s+)?(?:function\s*\*?|const|let|var|class)\s+([A-Za-z_$][\w$]*)/gm)].map((m) => m[1]);
  assert.ok(names.length > 10);
  for (const n of names) assert.ok(n === "openAgentLink" || /^(?:lg[A-Z]|LG_)/.test(n), n);
  assert.ok(!/innerHTML|insertAdjacentHTML|outerHTML|document\.write/.test(src), "no HTML strings: the dialog is built from nodes");
  assert.match(read("index.html"), /<script src="\/links\.js"><\/script>\s*<script src="\/linkgate\.js"><\/script>/);
});

test("module.exports carries the pure functions and openAgentLink; require() works with no browser", () => {
  for (const n of ["lgParse", "lgWarnings", "lgPunyDecode", "lgUnicodeHost", "lgOsa", "lgScripts", "lgTrustMatch", "lgTrustable", "lgUrlParts", "lgIsLoopbackHost", "openAgentLink", "lgTrustList", "lgLoadTrust", "lgRemoveTrust", "lgTrustLoadFailed"]) {
    assert.strictEqual(typeof lg[n], "function", n);
  }
});

test("a non-http link opens nothing and says so", async () => {
  const p = page();
  for (const bad of ["mailto:a@b.c", "file:///etc/passwd", "./README.md", "javascript:alert(1)", "", "ftp://x.org/a"]) {
    assert.strictEqual(await p.open(bad, { source: "markdown" }), false, bad);
  }
  assert.strictEqual(p.opened.length, 0);
  assert.strictEqual(p.asks.length, 0);
  assert.strictEqual(p.toasts.length, 6);
  assert.deepStrictEqual(p.toasts[0], ["CuliBox opens only http and https links", false]);
});

test("a trusted, clean https link opens without asking, with noopener,noreferrer", async () => {
  const p = page();
  await p.ready();
  assert.strictEqual(await p.open("https://github.com/culiops/culibox/pull/412", { source: "terminal", where: "s1" }), true);
  assert.strictEqual(p.asks.length, 0);
  assert.deepStrictEqual(plain(p.opened), [["https://github.com/culiops/culibox/pull/412", "_blank", "noopener,noreferrer"]]);
});

test("the skip decision reads the cache without awaiting anything before window.open", async () => {
  const p = page();
  await p.ready();
  const pr = p.open("https://github.com/x", { source: "terminal" });
  assert.strictEqual(p.opened.length, 1, "window.open ran synchronously inside the click");
  await pr;
});

test("a trusted subdomain opens only when subdomains are on", async () => {
  const p = page();
  await p.ready();
  await p.open("https://metrics.grafana.example.com/d/1", { source: "terminal" });
  assert.strictEqual(p.asks.length, 0);
  assert.strictEqual(p.opened.length, 1);
  await p.open("https://gist.github.com/a", { source: "terminal" }); // github.com has subdomains off
  assert.strictEqual(p.asks.length, 1);
  assert.strictEqual(p.opened.length, 1, "cancelled, so it did not open");
});

test("a trusted but warned link still asks", async () => {
  const p = page();
  await p.ready();
  user(p, { outcome: "cancel" });
  await p.open("https://github.com:8443/x", { source: "terminal" });
  await p.open("https://github.com/x?next=https://evil.example", { source: "terminal" });
  await p.open("https://githhub.com/x", { source: "terminal" }); // a lookalike of a trusted host
  assert.strictEqual(p.asks.length, 3);
  assert.strictEqual(p.opened.length, 0);
  assert.match(textOf(p.asks[2]), /One letter away from github\.com, a host you trust\./);
});

test("a trusted http link asks (the skip is https only)", async () => {
  const p = page({ hosts: [{ host: "intranet.example.com", subdomains: false }] });
  await p.ready();
  user(p, { outcome: "cancel" });
  await p.open("http://intranet.example.com/x", { source: "terminal" });
  assert.strictEqual(p.asks.length, 1);
});

test("an untrusted clean link asks: Open, primary, cls lk-ask, cancel focus is askDialog's default", async () => {
  const p = page();
  await p.ready();
  user(p, { outcome: "confirm" });
  assert.strictEqual(await p.open("https://docs.example.org/a/b?q=1", { source: "terminal", where: "s1" }), true);
  const a = p.asks[0];
  assert.strictEqual(a.title, "Open this link?");
  assert.strictEqual(a.cls, "lk-ask");
  assert.strictEqual(a.confirmLabel, "Open");
  assert.ok(!a.focusCancel);
  assert.ok(!/\s/.test(a.cls), "askDialog's classList.toggle throws on a space");
  assert.strictEqual(a.wide, false);
  assert.strictEqual(findAll(a, ".lk-warn").length, 0);
  assert.strictEqual(p.opened.length, 1);
});

test("the dialog's URL box: parts from nodes, the host in bold", async () => {
  const p = page();
  await p.ready();
  await p.open("https://user:pw@docs.example.org:8443/a/b?q=1#x", { source: "terminal", where: "s1" });
  const box = findAll(p.asks[0], ".lk-url")[0];
  assert.strictEqual(box.textContent, "https://user:pw@docs.example.org:8443/a/b?q=1#x");
  const host = box.querySelectorAll(".lk-host");
  assert.strictEqual(host.length, 1);
  assert.strictEqual(host[0].textContent, "docs.example.org");
  assert.deepStrictEqual(box.querySelectorAll(".lk-dim").map((n) => n.textContent), ["https://", "user:pw@", "/a/b?q=1#x"]);
  assert.strictEqual(box.querySelectorAll(".lk-port")[0].textContent, ":8443");
});

test("a long user name is cut in the box and the warning, and the whole link is what opens", async () => {
  const p = page();
  await p.ready();
  const raw = "https://" + "u".repeat(300) + "@evil.example/login";
  user(p, { outcome: "confirm" });
  await p.open(raw, { source: "terminal" });
  const box = findAll(p.asks[0], ".lk-url")[0];
  assert.ok(box.textContent.length < 100, "the box is short: " + box.textContent.length);
  assert.strictEqual(box.querySelectorAll(".lk-dim")[1].textContent, "u".repeat(40) + "…@");
  assert.strictEqual(box.querySelectorAll(".lk-host")[0].textContent, "evil.example");
  const warn = findAll(p.asks[0], ".lk-wt")[0].textContent;
  assert.ok(warn.startsWith("This link goes to evil.example. "), warn);
  assert.ok(warn.length < 120, warn.length);
  assert.strictEqual(p.opened[0][0], new URL(raw).href, "window.open gets the full link");
});

test("a cut never lands inside a %XX triplet: it backs off to before the %", () => {
  const shown = (name) => lg.lgUrlParts(new URL("https://" + name + "@x.example/")).userinfo;
  const tail = "z".repeat(30);
  // the 40th character is the % or the first hex digit of a triplet: drop the part of it
  assert.strictEqual(shown("a".repeat(39) + "%41" + tail), "a".repeat(39) + "…@");
  assert.strictEqual(shown("a".repeat(38) + "%41" + tail), "a".repeat(38) + "…@");
  // a triplet that ends exactly at 40 is whole, and one that starts after is not touched
  assert.strictEqual(shown("a".repeat(37) + "%41" + tail), "a".repeat(37) + "%41…@");
  assert.strictEqual(shown("a".repeat(40) + "%41" + tail), "a".repeat(40) + "…@");
  // spaces in a user name are %20 once parsed, so the same holds for them
  assert.strictEqual(shown("a".repeat(39) + " " + tail), "a".repeat(39) + "…@");
  const w = lg.lgWarnings(new URL("https://" + "a".repeat(39) + "%41" + tail + "@evil.example/"), {}).find((x) => x.kind === "userinfo");
  assert.ok(w.text.includes("a".repeat(39) + "…@"), w.text);
});

test("the meta line names the source", async () => {
  const p = page();
  await p.ready();
  const meta = async (raw, c) => { p.asks.length = 0; await p.open(raw, c); return findAll(p.asks[0], ".lk-meta")[0].textContent; };
  const NOTICE = "CuliBox does not check where a link leads; your browser's own check still runs.";
  assert.strictEqual(await meta("https://a.example.org/", { source: "terminal", where: "link-demo" }), "From the terminal of link-demo. " + NOTICE);
  assert.strictEqual(await meta("https://a.example.org/", { source: "markdown", where: "LINKS.md" }), "From LINKS.md. " + NOTICE);
  assert.strictEqual(await meta("https://a.example.org/", { source: "markdown" }), "From a markdown file. " + NOTICE);
  assert.strictEqual(await meta("https://a.example.org/", { source: "editor" }), "From the Files editor. " + NOTICE);
  // where is shown as a mono node, and never parsed as markup
  await p.open("https://a.example.org/", { source: "markdown", where: "<b>x</b>.md" });
  assert.strictEqual(findAll(p.asks[p.asks.length - 1], ".lk-meta")[0].querySelectorAll(".lk-mono")[0].textContent, "<b>x</b>.md");
});

test("every dialog carries the muted notice", async () => {
  const p = page();
  await p.ready();
  await p.open("https://a.example.org/", { source: "terminal" });
  await p.open("http://a.example.org:81/", { source: "terminal" });
  await p.open("http://localhost:3000/", { source: "terminal" });
  assert.strictEqual(p.asks.length, 3);
  for (const a of p.asks) assert.ok(textOf(a).includes("CuliBox does not check where a link leads; your browser's own check still runs."));
});

test("a warned link: lk-ask-warn, Open anyway, focusCancel, one row per warning in order", async () => {
  const p = page({ hosts: [] });
  await p.ready();
  user(p, { outcome: "confirm" });
  await p.open("https://github.com@xn--gthub-zsa.com/login/oauth", { source: "markdown", where: "LINKS.md" });
  const a = p.asks[0];
  assert.strictEqual(a.cls, "lk-ask-warn");
  assert.strictEqual(a.confirmLabel, "Open anyway");
  assert.strictEqual(a.focusCancel, true);
  const rows = findAll(a, ".lk-warn");
  assert.strictEqual(rows.length, 2, "userinfo and idn fire; nothing is trusted here so there is no lookalike");
  assert.match(rows[0].textContent, /This link goes to gíthub\.com\. github\.com@ before it is a user name, not the host\./);
  assert.match(rows[1].textContent, /The host uses non-ASCII letters \(í\) that can look like ASCII ones\./);
  assert.match(findAll(a, ".lk-meta")[0].textContent, /^Host gíthub\.com · real form xn--gthub-zsa\.com · from LINKS\.md/);
  assert.strictEqual(p.opened.length, 1, "Open anyway opened it");
});

test("the lookalike row appears when a trusted host is one letter away", async () => {
  const p = page();
  await p.ready();
  await p.open("https://github.com@xn--gthub-zsa.com/login/oauth", { source: "terminal", where: "s1" });
  const rows = findAll(p.asks[0], ".lk-warn");
  assert.strictEqual(rows.length, 3);
  assert.match(rows[2].textContent, /One letter away from github\.com, a host you trust\./);
});

test("an unwarned link shows no warning row and never a safe label", async () => {
  const p = page();
  await p.ready();
  await p.open("https://docs.example.org/a", { source: "terminal" });
  assert.strictEqual(findAll(p.asks[0], ".lk-warn").length, 0);
  assert.ok(!/safe|verified|trusted link/i.test(textOf(p.asks[0]).replace(/Always open links[^\n]*/, "")));
});

test("the trust checkbox: offered for a trustable link, revealing the subdomain row", async () => {
  const p = page();
  await p.ready();
  await p.open("https://docs.example.org/a", { source: "terminal" });
  const a = p.asks[0];
  const boxes = findAll(a, "input");
  assert.strictEqual(boxes.length, 2);
  assert.strictEqual(boxes[0].type, "checkbox");
  assert.ok(!boxes[0].checked);
  assert.ok(!boxes[1].checked, "subdomains are off by default");
  const labels = findAll(a, "label").map((l) => l.textContent);
  assert.ok(labels.includes("Always open links to docs.example.org without asking"));
  assert.ok(labels.includes("and its subdomains (*.docs.example.org)"));
  const sub = findAll(a, ".lk-sub")[0];
  assert.strictEqual(sub.hidden, true, "hidden until the first box is ticked");
  boxes[0].checked = true; boxes[0].fire("change");
  assert.strictEqual(sub.hidden, false);
  boxes[0].checked = false; boxes[0].fire("change");
  assert.strictEqual(sub.hidden, true);
  assert.match(textOf(a), /You can remove it later in Settings › Trusted hosts\./);
});

test("no checkbox for a link that cannot be trusted, and a warned one says why", async () => {
  const p = page();
  await p.ready();
  await p.open("http://docs.example.org/a", { source: "terminal" }); // http
  await p.open("https://10.0.0.5/a", { source: "terminal" }); // IP
  await p.open("https://xn--gthub-zsa.com/a", { source: "terminal" }); // xn--
  await p.open("https://intranet/a", { source: "terminal" }); // one label, no warning
  await p.open("http://localhost:3000/a", { source: "terminal" }); // loopback
  for (const a of p.asks) assert.strictEqual(findAll(a, "input").length, 0, textOf(a));
  assert.match(textOf(p.asks[0]), /This link cannot be trusted: .*not https/);
  assert.match(textOf(p.asks[1]), /This link cannot be trusted: .*IP address/);
  assert.match(textOf(p.asks[2]), /This link cannot be trusted: .*xn--/);
  assert.ok(!/cannot be trusted/.test(textOf(p.asks[3])), "no warning, so no 'why' line");
  assert.ok(!/cannot be trusted/.test(textOf(p.asks[4])));
});

test("a localhost link is rewritten to the page's host, with the toast, and the dialog says so", async () => {
  const p = page({ hostname: "box.tail.ts.net" });
  await p.ready();
  user(p, { outcome: "confirm" });
  await p.open("http://localhost:3030/app", { source: "terminal", where: "s1" });
  const a = p.asks[0];
  assert.strictEqual(a.cls, "lk-ask", "no ip, http or port warning for a loopback link");
  assert.strictEqual(findAll(a, ".lk-warn").length, 0);
  assert.strictEqual(findAll(a, ".lk-url")[0].textContent, "http://box.tail.ts.net:3030/app");
  assert.match(findAll(a, ".lk-meta")[0].textContent, /A localhost link: it opens on box\.tail\.ts\.net\./);
  assert.deepStrictEqual(plain(p.opened), [["http://box.tail.ts.net:3030/app", "_blank", "noopener,noreferrer"]]);
  assert.deepStrictEqual(p.toasts, [["opened box.tail.ts.net:3030 instead of localhost", false]]);
  assert.strictEqual(findAll(a, "input").length, 0, "a localhost link is never trustable");
});

test("a localhost link to an IP page host still gets no ip warning", async () => {
  const p = page({ hostname: "100.64.1.2" });
  await p.ready();
  await p.open("http://127.0.0.1:8080/", { source: "terminal" });
  assert.strictEqual(findAll(p.asks[0], ".lk-warn").length, 0);
  assert.strictEqual(findAll(p.asks[0], ".lk-url")[0].textContent, "http://100.64.1.2:8080/");
});

test("a loopback link with another warning still shows that warning", async () => {
  const p = page();
  await p.ready();
  await p.open("http://localhost:3000/?next=https://evil.example", { source: "terminal" });
  const rows = findAll(p.asks[0], ".lk-warn");
  assert.strictEqual(rows.length, 1);
  assert.match(rows[0].textContent, /forward you elsewhere/);
});

test("a loopback link with userinfo is not rewritten and warns", async () => {
  const p = page();
  await p.ready();
  await p.open("http://evil.example@localhost:3000/", { source: "terminal" });
  assert.match(findAll(p.asks[0], ".lk-warn")[0].textContent, /evil\.example@ before it is a user name, not the host\./);
  assert.strictEqual(p.toasts.length, 0);
  // Not rewritten, so it opens the viewer's own localhost: the meta line must not say it opens on the page's host.
  const meta = findAll(p.asks[0], ".lk-meta")[0].textContent;
  assert.ok(!/opens on|localhost link/i.test(meta), meta);
});

test("the localhost note appears only when the link was rewritten", async () => {
  const p = page({ hostname: "localhost" }); // the page is on localhost itself: nothing is rewritten
  await p.ready();
  await p.open("http://localhost:3000/", { source: "terminal", where: "s1" });
  assert.ok(!/opens on/.test(findAll(p.asks[0], ".lk-meta")[0].textContent));
  assert.strictEqual(p.toasts.length, 0);
});

test("a loopback link never skips the dialog, even when the host it is rewritten to is trusted", async () => {
  const p = page({ hostname: "box.tail.ts.net", hosts: [{ host: "box.tail.ts.net", subdomains: false }] });
  await p.ready();
  // https://localhost:3000/ becomes https://box.tail.ts.net:3000/, has no warning (the port line is off for loopback) and matches.
  await p.open("https://localhost:3000/", { source: "terminal", where: "s1" });
  assert.strictEqual(p.asks.length, 1, "it asked");
  assert.strictEqual(p.opened.length, 0, "and, cancelled, did not open");
  assert.strictEqual(p.asks[0].cls, "lk-ask");
  assert.strictEqual(findAll(p.asks[0], "input").length, 0, "a localhost link is never trustable");
  // the same for the subdomain form
  const q = page({ hostname: "box.tail.ts.net", hosts: [{ host: "tail.ts.net", subdomains: true }] });
  await q.ready();
  await q.open("https://127.0.0.1:3000/", { source: "terminal" });
  assert.strictEqual(q.asks.length, 1);
  assert.strictEqual(q.opened.length, 0);
});

test("a link to the page's own host always asks and offers no trust, even when the list already matches it", async () => {
  // an entry from before this rule, and a parent domain with subdomains on
  for (const hosts of [[{ host: "box.example.com", subdomains: false }], [{ host: "example.com", subdomains: true }]]) {
    const p = page({ hostname: "box.example.com", hosts });
    await p.ready();
    await p.open("https://box.example.com/#/s/1", { source: "terminal" });
    assert.strictEqual(p.asks.length, 1, "it asked: " + JSON.stringify(hosts));
    assert.strictEqual(p.opened.length, 0, "and, cancelled, did not open");
    assert.strictEqual(findAll(p.asks[0], "input").length, 0, "no trust box for the page's own host");
  }
  // a sibling under the same trusted parent still skips
  const q = page({ hostname: "box.example.com", hosts: [{ host: "example.com", subdomains: true }] });
  await q.ready();
  await q.open("https://docs.example.com/a", { source: "terminal" });
  assert.strictEqual(q.asks.length, 0);
  assert.strictEqual(q.opened.length, 1);
});

test("the box on Open: the link opens first, then POST {host, subdomains} as JSON", async () => {
  const p = page();
  await p.ready();
  p.events.length = 0;
  user(p, { trust: true });
  await p.open("https://docs.example.org/a", { source: "terminal" });
  await tick();
  const posts = p.server.calls.filter((c) => c.method === "POST");
  assert.strictEqual(posts.length, 1);
  assert.strictEqual(posts[0].url, "/api/link-trust");
  assert.deepStrictEqual(plain(posts[0].body), { host: "docs.example.org", subdomains: false });
  assert.strictEqual(posts[0].init.headers["Content-Type"], "application/json");
  assert.strictEqual(posts[0].init.credentials, "same-origin");
  assert.ok(p.events.indexOf("open") >= 0 && p.events.indexOf("open") < p.events.indexOf("post"), "window.open before the POST: " + p.events);
  // and the cache reloaded after the add, so the next click skips the dialog
  p.asks.length = 0;
  await p.open("https://docs.example.org/b", { source: "terminal" });
  assert.strictEqual(p.asks.length, 0);
  assert.strictEqual(p.opened.length, 2);
});

test("the box with subdomains ticked sends subdomains true", async () => {
  const p = page();
  await p.ready();
  user(p, { trust: true, subs: true });
  await p.open("https://docs.example.org/a", { source: "terminal" });
  const post = p.server.calls.find((c) => c.method === "POST");
  assert.deepStrictEqual(plain(post.body), { host: "docs.example.org", subdomains: true });
});

test("a ticked subdomain row is ignored while the main box is unticked", async () => {
  const p = page();
  await p.ready();
  user(p, { subs: true });
  await p.open("https://docs.example.org/a", { source: "terminal" });
  assert.strictEqual(p.server.calls.filter((c) => c.method === "POST").length, 0);
});

test("on Cancel the box is ignored: nothing opens, nothing is saved", async () => {
  const p = page();
  await p.ready();
  user(p, { trust: true, outcome: "cancel" });
  assert.strictEqual(await p.open("https://docs.example.org/a", { source: "terminal" }), false);
  await tick();
  assert.strictEqual(p.opened.length, 0);
  assert.strictEqual(p.server.calls.filter((c) => c.method === "POST").length, 0);
});

test("the checkbox is not offered, so nothing is saved, on a warned Open anyway", async () => {
  const p = page();
  await p.ready();
  user(p, { outcome: "confirm" });
  await p.open("https://docs.example.org:8443/a", { source: "terminal" });
  assert.strictEqual(p.opened.length, 1);
  assert.strictEqual(p.server.calls.filter((c) => c.method === "POST").length, 0);
});

test("a failed save toasts after the link opened", async () => {
  const p = page();
  await p.ready();
  p.server.failPost = { status: 409, error: "too many trusted hosts" };
  user(p, { trust: true });
  assert.strictEqual(await p.open("https://docs.example.org/a", { source: "terminal" }), true);
  assert.strictEqual(p.opened.length, 1);
  assert.deepStrictEqual(p.toasts, [["Could not save docs.example.org as trusted: too many trusted hosts", true]]);
  // a network failure too
  const q = page();
  await q.ready();
  q.fetch = undefined;
  q.run("fetch = async () => { throw new Error('offline'); }");
  user(q, { trust: true });
  await q.open("https://docs.example.org/a", { source: "terminal" });
  assert.match(q.toasts[0][0], /^Could not save docs\.example\.org as trusted: offline$/);
});

test("a busy ask (askDialog answers cancel) never opens the link", async () => {
  const p = page();
  await p.ready();
  p.askDialog = async () => ({ outcome: "cancel", value: "" });
  assert.strictEqual(await p.open("https://docs.example.org/a", { source: "terminal" }), false);
  assert.strictEqual(p.opened.length, 0);
  // no askDialog at all fails closed too
  const q = page();
  await q.ready();
  q.run("askDialog = undefined");
  assert.strictEqual(await q.open("https://docs.example.org/a", { source: "terminal" }), false);
  assert.strictEqual(q.opened.length, 0);
});

test("an askDialog that rejects never opens the link", async () => {
  const p = page();
  await p.ready();
  p.askDialog = async () => { throw new Error("boom"); };
  assert.strictEqual(await p.open("https://docs.example.org/a", { source: "terminal" }), false);
  assert.strictEqual(p.opened.length, 0);
});

test("the cache loads on DOMContentLoaded with same-origin credentials", async () => {
  const p = page();
  assert.strictEqual(p.server.calls.length, 0, "not before the page is ready");
  await p.ready();
  const gets = p.server.calls.filter((c) => c.method === "GET");
  assert.strictEqual(gets.length, 1);
  assert.strictEqual(gets[0].url, "/api/link-trust");
  assert.strictEqual(gets[0].init.credentials, "same-origin");
  assert.deepStrictEqual(plain(p.run("lgTrustList()")).map((h) => h.host), ["github.com", "grafana.example.com"]);
});

test("each openAgentLink call refreshes the cache in the background", async () => {
  const p = page();
  await p.ready();
  const before = p.server.calls.filter((c) => c.method === "GET").length;
  await p.open("https://github.com/x", { source: "terminal" });
  await tick();
  assert.strictEqual(p.server.calls.filter((c) => c.method === "GET").length, before + 1);
  // a host trusted elsewhere (another window) is known by the next click after that refresh
  p.server.hosts = p.server.hosts.concat([{ host: "docs.example.org", subdomains: false }]);
  await p.open("https://github.com/y", { source: "terminal" });
  await tick();
  p.asks.length = 0;
  await p.open("https://docs.example.org/z", { source: "terminal" });
  assert.strictEqual(p.asks.length, 0);
});

test("a failed load leaves the cache empty, so every link asks (fails closed)", async () => {
  const p = page();
  p.server.failGet = true;
  await p.ready();
  assert.deepStrictEqual(plain(p.run("lgTrustList()")), []);
  await p.open("https://github.com/x", { source: "terminal" });
  assert.strictEqual(p.asks.length, 1);
  // and a later failed load empties a cache that had entries
  const q = page();
  await q.ready();
  assert.strictEqual(plain(q.run("lgTrustList()")).length, 2);
  q.server.failGet = true;
  await q.run("lgLoadTrust()");
  assert.deepStrictEqual(plain(q.run("lgTrustList()")), []);
});

test("a malformed answer leaves the cache empty", async () => {
  const p = page();
  p.server.hosts = "nope";
  await p.ready();
  assert.deepStrictEqual(plain(p.run("lgTrustList()")), []);
  const q = page({ hosts: [{ host: 7 }, null, { host: "ok.example.com", subdomains: 1 }] });
  await q.ready();
  assert.deepStrictEqual(plain(q.run("lgTrustList()")).map((h) => [h.host, h.subdomains]), [["ok.example.com", true]]);
});

test("an older load that answers late does not overwrite a newer one", async () => {
  const p = page();
  await p.ready();
  let release;
  p.server.getDelay = () => new Promise((r) => { release = r; });
  const slow = p.run("lgLoadTrust()"); // asked while github.com and grafana.example.com were trusted; answers last
  await tick();
  p.server.getDelay = null;
  p.server.hosts = [{ host: "new.example.com", subdomains: false }]; // github.com was removed meanwhile
  await p.run("lgLoadTrust()");
  assert.deepStrictEqual(plain(p.run("lgTrustList()")).map((h) => h.host), ["new.example.com"]);
  release();
  const late = await slow;
  assert.deepStrictEqual(plain(late).map((h) => h.host), ["new.example.com"], "the late call resolves to the cache, not its own stale answer");
  assert.deepStrictEqual(plain(p.run("lgTrustList()")).map((h) => h.host), ["new.example.com"], "a removed host does not come back");
  // so a link to the removed host asks again
  await p.open("https://github.com/x", { source: "terminal" });
  assert.strictEqual(p.asks.length, 1);
});

test("lgTrustLoadFailed tells a failed load from an empty list", async () => {
  const p = page({ hosts: [] });
  await p.ready();
  assert.strictEqual(p.run("lgTrustLoadFailed()"), false, "an empty list is not a failure");
  p.server.failGet = true; // network error
  await p.run("lgLoadTrust()");
  assert.strictEqual(p.run("lgTrustLoadFailed()"), true);
  assert.deepStrictEqual(plain(p.run("lgTrustList()")), []);
  p.server.failGet = false;
  p.server.getStatus = 500; // an HTTP refusal
  await p.run("lgLoadTrust()");
  assert.strictEqual(p.run("lgTrustLoadFailed()"), true);
  p.server.getStatus = 0;
  await p.run("lgLoadTrust()");
  assert.strictEqual(p.run("lgTrustLoadFailed()"), false, "a good load clears it");
  // an older failed load that answers late does not set it over a newer good one
  let release;
  p.server.getDelay = () => new Promise((r) => { release = r; });
  p.server.failGet = true;
  const slow = p.run("lgLoadTrust()");
  await tick();
  p.server.getDelay = null;
  p.server.failGet = false;
  await p.run("lgLoadTrust()");
  release();
  await slow;
  assert.strictEqual(p.run("lgTrustLoadFailed()"), false);
});

test("lgRemoveTrust sends DELETE {host} and refreshes; a failure throws for the Settings tab", async () => {
  const p = page();
  await p.ready();
  await p.run("lgRemoveTrust('github.com')");
  const del = p.server.calls.find((c) => c.method === "DELETE");
  assert.strictEqual(del.url, "/api/link-trust");
  assert.deepStrictEqual(plain(del.body), { host: "github.com" });
  assert.strictEqual(del.init.headers["Content-Type"], "application/json");
  assert.deepStrictEqual(plain(p.run("lgTrustList()")).map((h) => h.host), ["grafana.example.com"]);
  p.asks.length = 0;
  await p.open("https://github.com/x", { source: "terminal" });
  assert.strictEqual(p.asks.length, 1, "removed, so it asks again");
  p.server.failPost = { status: 404, error: "not trusted" };
  await assert.rejects(() => p.run("lgRemoveTrust('nope.example.com')"), (e) => e.status === 404 && e.message === "not trusted");
});

test("lgTrustCorrupt reports the server's damaged-file flag", async () => {
  const p = page();
  p.server.corrupt = true;
  await p.ready();
  assert.strictEqual(p.run("lgTrustCorrupt()"), true);
  p.server.corrupt = false;
  await p.run("lgLoadTrust()");
  assert.strictEqual(p.run("lgTrustCorrupt()"), false);
});

test("lgTrustNewer tells a newer file from a damaged one; both are corrupt", async () => {
  const p = page();
  p.server.corrupt = true;
  p.server.newer = true;
  await p.ready();
  assert.strictEqual(p.run("lgTrustNewer()"), true);
  assert.strictEqual(p.run("lgTrustCorrupt()"), true, "newer stays corrupt too, so the old logic holds");
  p.server.newer = false;
  await p.run("lgLoadTrust()");
  assert.strictEqual(p.run("lgTrustNewer()"), false, "damaged is not newer");
  assert.strictEqual(p.run("lgTrustCorrupt()"), true);
  p.server.corrupt = false;
  await p.run("lgLoadTrust()");
  assert.strictEqual(p.run("lgTrustNewer()"), false);
  // a failed load says neither
  p.server.corrupt = true; p.server.newer = true;
  await p.run("lgLoadTrust()");
  p.server.failGet = true;
  await p.run("lgLoadTrust()");
  assert.strictEqual(p.run("lgTrustNewer()"), false);
});

// F4: the box is hidden while the list is damaged or newer, because the POST is certain to fail.
test("no trust checkbox while the list is damaged or newer: it would only fail on save", async () => {
  for (const newer of [false, true]) {
    const p = page();
    p.server.corrupt = true;
    p.server.newer = newer;
    await p.ready();
    await p.open("https://docs.example.org/a", { source: "terminal" });
    const a = p.asks[0];
    assert.strictEqual(findAll(a, "input").length, 0, "newer=" + newer);
    assert.ok(!/Always open links/.test(textOf(a)));
    assert.strictEqual(findAll(a, ".lk-trust").length, 0);
  }
  // control: a healthy list still offers it
  const ok = page();
  await ok.ready();
  await ok.open("https://docs.example.org/a", { source: "terminal" });
  assert.strictEqual(findAll(ok.asks[0], "input").length, 2);
});

// F7: the agent names the file or session, and a bidi control in it must not reorder the line around it.
test("the source name is isolated from the meta line's direction", async () => {
  const p = page();
  await p.ready();
  const where = "a\u202Eb.md";
  await p.open("https://a.example.org/", { source: "markdown", where });
  const meta = findAll(p.asks[0], ".lk-meta")[0];
  const iso = meta.querySelectorAll("bdi");
  assert.strictEqual(iso.length, 1, "one isolated element: the name");
  assert.strictEqual(iso[0].textContent, where);
  await p.open("https://a.example.org/", { source: "terminal", where });
  const t = findAll(p.asks[1], ".lk-meta")[0].querySelectorAll("bdi");
  assert.strictEqual(t.length, 1);
  assert.strictEqual(t[0].textContent, where);
});

test("the dialog is built with textContent only: hostile text stays text", async () => {
  const p = page();
  await p.ready();
  await p.open("https://docs.example.org/<img src=x onerror=alert(1)>?q=<script>#<b>", { source: "markdown", where: "<script>x</script>" });
  const a = p.asks[0];
  const walk = (n, f) => { f(n); for (const c of n.children || []) walk(c, f); };
  const tags = new Set();
  for (const n of [].concat(a.extra)) walk(n, (x) => { if (x.tag) tags.add(x.tag); });
  assert.ok(![...tags].some((t) => /^(img|script|b|iframe|a)$/.test(t)), [...tags].join());
  assert.ok(textOf(a).includes("<script>x</script>"));
});
