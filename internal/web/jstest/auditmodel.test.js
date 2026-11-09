"use strict";
// The Audit page's pure model: the query it sends, how one audit row reads, the
// KPI tiles and the footer texts. DOM wiring (auditpage.js) is pinned at the
// source level, the way pagehost.test.js does.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const { auditQuery, auditCsvHref, auditRowView, auditKpis, auditFootText, auditPolicyText, auditWhatRuns } =
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

test("auditRowView: an extended elevation is bad, a refused or dropped one is neutral", () => {
  assert.strictEqual(auditRowView(ev({ kind: "elevation", outcome: "extended" }), {}).tone, "bad");
  assert.strictEqual(auditRowView(ev({ kind: "elevation", outcome: "refused" }), {}).tone, "");
  const v = auditRowView(ev({ kind: "elevation", outcome: "dropped_early", actor: "you" }), {});
  assert.strictEqual(v.tone, "");
  assert.strictEqual(v.outcome, "dropped early · you");
});

// The red row (UI plan 5, #175; digest-design §B9): one write elevation is red, and a write elevation is a live
// grant, so granted and extended. A refused, dropped, expired or ended elevation opened nothing: no tint, no red
// Kind. Plan 6 (#176) made the Outcome tone follow: only a granted or extended elevation is red, a refused one is neutral.
test("auditRowView: elevated is a granted or extended elevation, nothing else", () => {
  for (const o of ["granted", "extended"]) {
    assert.strictEqual(auditRowView(ev({ kind: "elevation", outcome: o }), {}).elevated, true, o);
  }
  for (const o of ["dropped", "expired", "ended", "refused", "dropped_early", ""]) {
    assert.strictEqual(auditRowView(ev({ kind: "elevation", outcome: o }), {}).elevated, false, o);
  }
  // The word alone is not enough: only an elevation row is a write elevation.
  for (const kind of ["credential", "egress", "settings", "signin", "store", "weird"]) {
    assert.strictEqual(auditRowView(ev({ kind, outcome: "granted" }), {}).elevated, false, kind);
    assert.strictEqual(auditRowView(ev({ kind, outcome: "extended" }), {}).elevated, false, kind);
  }
  assert.strictEqual(auditRowView({}, {}).elevated, false);
  assert.strictEqual(auditRowView(null, {}).elevated, false);
});

test("auditRowView: a refused elevation is not tinted and its Outcome is neutral (containment working)", () => {
  const v = auditRowView(ev({ kind: "elevation", outcome: "refused", actor: "you" }), {});
  assert.strictEqual(v.elevated, false);
  assert.strictEqual(v.tone, "");
  assert.strictEqual(v.outcome, "refused · you");
});

test("auditRowView: egress denied x4 is neutral and carries its count; allowed is ok", () => {
  const d = auditRowView(ev({ kind: "egress", what: "pypi.org:443", outcome: "denied", count: 4 }), {});
  assert.strictEqual(d.kind, "Egress");
  assert.strictEqual(d.outcome, "denied ×4");
  assert.strictEqual(d.tone, "");
  const a = auditRowView(ev({ kind: "egress", outcome: "allowed", count: 1 }), {});
  assert.strictEqual(a.outcome, "allowed");
  assert.strictEqual(a.tone, "ok");
});

test("auditRowView: refusals are neutral, errors and live grants red", () => {
  // Egress: a denied tunnel is the allowlist working; a failed dial is an error.
  for (const o of ["denied", "private_addr", "bad_auth", "bad_method"]) {
    const v = auditRowView(ev({ kind: "egress", outcome: o }), {});
    assert.notStrictEqual(v.tone, "bad", o);
    assert.strictEqual(v.tone, "", o);
  }
  for (const o of ["dial_error", "failed"]) assert.strictEqual(auditRowView(ev({ kind: "egress", outcome: o }), {}).tone, "bad", o);
  // Credentials: every denial is neutral, every error is red.
  for (const o of ["denied_token", "denied_session", "denied_not_read_only", "denied_box", "denied_scope"]) {
    assert.strictEqual(auditRowView(ev({ kind: "credential", outcome: o }), {}).tone, "", o);
  }
  for (const o of ["sts_error", "scope_error", "github_error", "rate_limited", "sso_expired", "store_locked", "unresolved", "not_installed"]) {
    assert.strictEqual(auditRowView(ev({ kind: "credential", outcome: o }), {}).tone, "bad", o);
  }
  // Elevation: a refusal is neutral; only a grant that opened or was extended is red, and elevated.
  assert.strictEqual(auditRowView(ev({ kind: "elevation", outcome: "refused" }), {}).tone, "");
  for (const o of ["granted", "extended"]) {
    const v = auditRowView(ev({ kind: "elevation", outcome: o }), {});
    assert.strictEqual(v.tone, "bad", o);
    assert.strictEqual(v.elevated, true, o);
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

// auditWhatRuns (UI plan 5, digest-design §B9): the What cell's runs. The server's parts mark the identifiers; the page
// never guesses them, and never splits `what`. Anything the parts cannot describe is one plain run equal to `what`.
test("auditWhatRuns: the server's parts come back as runs, id true only where the server said so", () => {
  const parts = [{ text: "aws " }, { text: "culipulse-prod", id: true }, { text: " · role " }, { text: "agent-ops-write", id: true }, { text: " · elevated", id: false }];
  const runs = auditWhatRuns(ev({ what: "aws culipulse-prod · role agent-ops-write · elevated", parts }));
  assert.deepStrictEqual(runs.map((r) => [r.text, r.id]), [
    ["aws ", false], ["culipulse-prod", true], [" · role ", false], ["agent-ops-write", true], [" · elevated", false],
  ]);
  // id is a boolean, and only the literal true counts.
  assert.deepStrictEqual(auditWhatRuns({ what: "x", parts: [{ text: "x", id: "yes" }, { text: "y", id: 1 }] }).map((r) => r.id), [false, false]);
});

test("auditWhatRuns: no parts, or parts the page cannot trust, is one plain run equal to what", () => {
  const what = "encrypted store unlocked · ttl=168h0m0s";
  const one = [{ text: what, id: false }];
  const got = (o) => auditWhatRuns({ what, ...o }).map((r) => ({ text: r.text, id: r.id }));
  assert.deepStrictEqual(got({}), one, "an old server sends no parts");
  assert.deepStrictEqual(got({ parts: undefined }), one);
  assert.deepStrictEqual(got({ parts: null }), one);
  assert.deepStrictEqual(got({ parts: [] }), one, "an empty list");
  assert.deepStrictEqual(got({ parts: {} }), one, "an object is not a list");
  assert.deepStrictEqual(got({ parts: "aws" }), one);
  assert.deepStrictEqual(got({ parts: [{ id: true }] }), one, "a part without text");
  assert.deepStrictEqual(got({ parts: [{ text: 5 }] }), one, "a part whose text is not a string");
  assert.deepStrictEqual(got({ parts: [{ text: "ok" }, { text: 5, id: true }] }), one, "one bad part spoils the list: no half guess");
  assert.deepStrictEqual(got({ parts: [null] }), one);
  assert.deepStrictEqual(got({ parts: ["aws"] }), one);
});

test("auditWhatRuns: a missing what is an empty plain run, and a missing event does not throw", () => {
  assert.deepStrictEqual(auditWhatRuns({}).map((r) => [r.text, r.id]), [["", false]]);
  assert.deepStrictEqual(auditWhatRuns({ what: null }).map((r) => [r.text, r.id]), [["", false]]);
  assert.deepStrictEqual(auditWhatRuns(undefined).map((r) => [r.text, r.id]), [["", false]]);
  assert.deepStrictEqual(auditWhatRuns(null).map((r) => [r.text, r.id]), [["", false]]);
});

test("auditKpis: five tiles, labelled as the frame has them, numbers grouped", () => {
  const k = auditKpis({ issued: 2180, refused: 6, elevations: 3, egress_allowed: 1234567, egress_denied: 0 });
  assert.deepStrictEqual(k.map((x) => x.label),
    ["Credentials issued", "Refused", "Write elevations", "Egress allowed", "Egress denied"]);
  assert.deepStrictEqual(k.map((x) => x.n), ["2,180", "6", "3", "1,234,567", "0"]);
});

// Red only for write (digest-design §B5, §B9): the Write elevations value, and only above zero. Refused and Egress
// denied are containment working, so they are never bad, whatever their count.
test("auditKpis: only Write elevations above zero is bad", () => {
  const bad = (s) => auditKpis(s).map((x) => x.bad === true);
  assert.deepStrictEqual(bad({ issued: 37, refused: 2, elevations: 2, egress_allowed: 1284, egress_denied: 20 }), [false, false, true, false, false]);
  assert.deepStrictEqual(bad({ issued: 1, refused: 9, elevations: 0, egress_allowed: 1, egress_denied: 9 }), [false, false, false, false, false]);
  assert.deepStrictEqual(bad(null), [false, false, false, false, false]);
  assert.deepStrictEqual(bad({}), [false, false, false, false, false]);
  assert.strictEqual(auditKpis({ elevations: 1 })[2].bad, true);
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
  for (const id of ["au-csv", "au-esc", "au-filters", "au-session", "au-range", "au-scroll", "au-kpis", "au-table", "au-body", "au-foot", "au-count", "au-more", "au-policy"]) {
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
