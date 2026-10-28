"use strict";
// The Audit page's pure model: the query it sends, how one audit row reads, the
// KPI tiles and the footer texts. DOM wiring (auditpage.js) is pinned at the
// source level, the way pagehost.test.js does.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const { auditQuery, auditCsvHref, auditRowView, auditKpis, auditFootText, auditPolicyText } =
  require("../static/auditmodel.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

const NOW = 1_800_000_000;
const st = (o) => Object.assign({ range: "7d", filter: "", session: "", before: "" }, o);

test("auditQuery: each range is from = now minus its seconds, to = now", () => {
  assert.strictEqual(auditQuery(st({ range: "24h" }), NOW), `from=${NOW - 86400}&to=${NOW}`);
  assert.strictEqual(auditQuery(st({ range: "7d" }), NOW), `from=${NOW - 604800}&to=${NOW}`);
  assert.strictEqual(auditQuery(st({ range: "30d" }), NOW), `from=${NOW - 2592000}&to=${NOW}`);
});

test("auditQuery: an unknown range falls back to 7d", () => {
  assert.strictEqual(auditQuery(st({ range: "bogus" }), NOW), `from=${NOW - 604800}&to=${NOW}`);
});

test("auditQuery: the filter goes as kind=, the session and the cursor are escaped", () => {
  assert.strictEqual(auditQuery(st({ filter: "egress", session: "a b&c", before: "17.2.9" }), NOW),
    `kind=egress&session=a%20b%26c&from=${NOW - 604800}&to=${NOW}&before=17.2.9`);
});

test("auditCsvHref: the same filters, never the cursor", () => {
  assert.strictEqual(auditCsvHref(st({ filter: "credentials", session: "s1", before: "17.2.9" }), NOW),
    `/api/audit.csv?kind=credentials&session=s1&from=${NOW - 604800}&to=${NOW}`);
  assert.strictEqual(auditCsvHref(st({}), NOW), `/api/audit.csv?from=${NOW - 604800}&to=${NOW}`);
});

const ev = (o) => Object.assign({ ts: 1_760_000_000, cursor: "c", kind: "credential", what: "aws sandbox", outcome: "minted" }, o);
const pad = (n) => String(n).padStart(2, "0");

test("auditRowView: time is MM-DD HH:MM local", () => {
  const d = new Date(1_760_000_000 * 1000);
  assert.strictEqual(auditRowView(ev(), {}).time, `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`);
});

test("auditRowView: a minted credential is ok, kind Credential", () => {
  const v = auditRowView(ev({ session: "s1" }), { s1: "cost-investigate" });
  assert.strictEqual(v.kind, "Credential");
  assert.strictEqual(v.outcome, "minted");
  assert.strictEqual(v.tone, "ok");
  assert.strictEqual(v.session, "cost-investigate");
  assert.strictEqual(v.elevated, false);
});

test("auditRowView: an elevation granted by you is bad (the write rule) and elevated", () => {
  const v = auditRowView(ev({ kind: "elevation", outcome: "granted", actor: "you", reason: "restart worker" }), {});
  assert.strictEqual(v.kind, "Elevation");
  assert.strictEqual(v.outcome, "granted · you");
  assert.strictEqual(v.tone, "bad");
  assert.strictEqual(v.elevated, true);
  assert.strictEqual(v.reason, "restart worker");
});

test("auditRowView: extended and refused elevations are bad, a dropped one is neutral", () => {
  for (const o of ["extended", "refused"]) assert.strictEqual(auditRowView(ev({ kind: "elevation", outcome: o }), {}).tone, "bad");
  const v = auditRowView(ev({ kind: "elevation", outcome: "dropped_early", actor: "you" }), {});
  assert.strictEqual(v.tone, "");
  assert.strictEqual(v.outcome, "dropped early · you");
});

test("auditRowView: egress denied x4 is bad and carries its count; allowed is ok", () => {
  const d = auditRowView(ev({ kind: "egress", what: "pypi.org:443", outcome: "denied", count: 4 }), {});
  assert.strictEqual(d.kind, "Egress");
  assert.strictEqual(d.outcome, "denied ×4");
  assert.strictEqual(d.tone, "bad");
  const a = auditRowView(ev({ kind: "egress", outcome: "allowed", count: 1 }), {});
  assert.strictEqual(a.outcome, "allowed");
  assert.strictEqual(a.tone, "ok");
});

test("auditRowView: every egress failure word is bad", () => {
  for (const o of ["denied", "private_addr", "bad_auth", "bad_method", "dial_error", "failed"]) {
    assert.strictEqual(auditRowView(ev({ kind: "egress", outcome: o }), {}).tone, "bad", o);
  }
  assert.strictEqual(auditRowView(ev({ kind: "settings", outcome: "ok" }), {}).tone, "ok");
});

test("auditRowView: sso_expired is bad (the Access view's rule, not amber)", () => {
  assert.strictEqual(auditRowView(ev({ outcome: "sso_expired" }), {}).tone, "bad");
  assert.strictEqual(auditRowView(ev({ outcome: "sso_expired" }), {}).outcome, "sso expired");
});

test("auditRowView: a settings row 'created' by you is neutral; sign-in and store label", () => {
  const v = auditRowView(ev({ kind: "settings", what: "token cf created", outcome: "created", actor: "you" }), {});
  assert.strictEqual(v.kind, "Settings");
  assert.strictEqual(v.outcome, "created · you");
  assert.strictEqual(v.tone, "");
  assert.strictEqual(auditRowView(ev({ kind: "signin" }), {}).kind, "Sign-in");
  assert.strictEqual(auditRowView(ev({ kind: "store" }), {}).kind, "Store");
});

test("auditRowView: an unknown session shows its id, none shows an em dash", () => {
  assert.strictEqual(auditRowView(ev({ session: "gone-1" }), { s1: "x" }).session, "gone-1");
  assert.strictEqual(auditRowView(ev({ session: "" }), {}).session, "—");
  assert.strictEqual(auditRowView(ev({ session: undefined }), {}).session, "—");
  assert.strictEqual(auditRowView(ev({ session: "constructor" }), {}).session, "constructor");
});

test("auditRowView: a missing reason is an empty string, an unknown kind keeps its word", () => {
  assert.strictEqual(auditRowView(ev(), {}).reason, "");
  assert.strictEqual(auditRowView(ev({ kind: "weird" }), {}).kind, "weird");
});

test("auditKpis: five tiles, labelled, numbers grouped", () => {
  const k = auditKpis({ issued: 2180, refused: 6, elevations: 3, egress_allowed: 1234567, egress_denied: 0 });
  assert.deepStrictEqual(k.map((x) => x.label),
    ["credentials issued", "refused", "write elevations", "egress allowed", "egress denied"]);
  assert.deepStrictEqual(k.map((x) => x.n), ["2,180", "6", "3", "1,234,567", "0"]);
});

test("auditKpis: a missing summary is dashes, not zeros", () => {
  assert.deepStrictEqual(auditKpis(null).map((x) => x.n), ["—", "—", "—", "—", "—"]);
});

test("auditFootText and auditPolicyText", () => {
  assert.strictEqual(auditFootText(10, 2628), "Showing 10 of 2,628 · newest first");
  assert.strictEqual(auditPolicyText(0), "Everything kept forever");
  assert.strictEqual(auditPolicyText(90), "Egress kept 90 days · everything else kept forever");
});

test("auditpage.js reaches the DOM through textContent only", () => {
  const src = read("auditpage.js");
  assert.ok(!/innerHTML|insertAdjacentHTML|outerHTML/.test(src.replace(/\/\/.*$/gm, "")), "no HTML sinks");
});

test("auditpage.js: mobile returns at once, one request per kind aborts the older, no polling", () => {
  const src = read("auditpage.js");
  assert.match(src, /function openAudit\(opts\)[\s\S]*?mobileActive\(window\.innerWidth\)[\s\S]*?return;/);
  assert.match(src, /new AbortController\(\)/);
  assert.match(src, /\.abort\(\)/);
  assert.ok(!/setInterval/.test(src), "no polling");
  assert.match(src, /accReadReply\(/);
});

test("the entry points all call openAudit; the Access card links to it", () => {
  assert.match(read("app.js"), /\$\("side-audit"\)\.addEventListener\("click", \(\) => openAudit\(\)\)/);
  assert.match(read("palette.js"), /audit: \(\) => openAudit\(\)/);
  assert.match(read("accesstab.js"), /openAudit\(\{ session: sid \}\)/);
  assert.ok(!/openAuditPage/.test(read("pagehost.js")), "the forward-reference shim is gone");
});

test("index.html carries the page markup and loads both scripts after accesstab.js", () => {
  const html = read("index.html");
  for (const id of ["au-range", "au-csv", "au-kpis", "au-filters", "au-session", "au-table", "au-foot", "au-count", "au-more", "au-policy"]) {
    assert.ok(html.includes('id="' + id + '"'), id);
  }
  assert.match(html, /<dialog id="audit-page" aria-label="Audit">/);
  const at = html.indexOf('<script src="/accesstab.js">');
  assert.ok(html.indexOf('<script src="/auditmodel.js">') > at);
  assert.ok(html.indexOf('<script src="/auditpage.js">') > html.indexOf('<script src="/auditmodel.js">'));
});

// The server refuses a window wider than 31 days (auditlog.MaxWindow): every
// range the page can send, in the list and the export, must fit.
test("every range's window is within the server's 31-day cap", () => {
  const now = 1_800_000_000;
  for (const range of ["24h", "7d", "30d", undefined, "bogus"]) {
    for (const href of [auditQuery({ range }, now), auditCsvHref({ range }, now)]) {
      const from = Number(/from=(\d+)/.exec(href)[1]);
      const to = Number(/to=(\d+)/.exec(href)[1]);
      assert.ok(to - from <= 31 * 86400, range + ": " + (to - from));
    }
  }
});
