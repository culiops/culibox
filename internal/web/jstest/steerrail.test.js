// The two rails' pure layer. This half is the icon rail's item list: what a
// 52px column of 40×40 buttons contains, in order, with an accessible name on
// every one of them. The design gives only the sidebar toggle a title; a button
// a screen reader cannot name is not a button anyone can use.
const test = require("node:test");
const assert = require("node:assert");
const { railItems, railMeta, startedText, ctxFill, ctxEmptyNote, ctxUsedText, awsStatsRows, egressRow, policyRow, dropLabel, toastDropsSuffix, dropsRow, lockedRow, cloneParts, cloneLevel, cloneRow, toastCloneSuffix } = require("../static/steerrail.js");
// steerrail.js is a browser script: withCommas is usage.js's global there.
globalThis.withCommas = require("../static/usage.js").withCommas;
// The real tierBadge, not a hand-written stand-in: the point of the change these
// tests cover is that the rail consumes tier.js's verdict rather than deriving a
// second one, so a fixture that agreed with the rail but not with tier.js would
// pass while the two surfaces disagreed on screen.
const { tierBadge } = require("../static/tier.js");

// startedText prepends a clock read in the machine's own local time (same as
// the rest of the app), so the expected clock half is computed the same way
// here rather than hard-coded — that keeps the test passing under any TZ.
function hhmm(iso) {
  const d = new Date(Date.parse(iso));
  return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
}

test("the toggle leads and the add trails, even with no sessions", () => {
  const items = railItems([], {});
  assert.deepStrictEqual(items.map((i) => i.kind), ["toggle", "new"]);
  assert.strictEqual(items[0].label, "Show sidebar (Ctrl+Shift+B)");
  assert.strictEqual(items[1].label, "New session");
});

// F57: the way back to the sidebar is the frame's ghost "»", named with the real chord.
test("the toggle is the » glyph and names the chord of this platform", () => {
  assert.strictEqual(railItems([], {})[0].glyph, "\u00bb");
  assert.strictEqual(railItems([], { mac: false })[0].label, "Show sidebar (Ctrl+Shift+B)");
  assert.strictEqual(railItems([], { mac: true })[0].label, "Show sidebar (\u21e7\u2318B)");
});

test("one item per session, in the list's own order", () => {
  const items = railItems(
    [{ id: "a", name: "review", activity: "working" }, { id: "b", name: "drain", activity: "done" }], {});
  assert.deepStrictEqual(items.map((i) => i.kind), ["toggle", "session", "session", "new"]);
  assert.deepStrictEqual(items.slice(1, 3).map((i) => i.id), ["a", "b"]);
});

test("a waiting session is alert; the focused one is on; both at once is legal", () => {
  const [, w] = railItems([{ id: "a", name: "n", activity: "waiting" }], { focusedId: "a" });
  assert.strictEqual(w.alert, true);
  assert.strictEqual(w.on, true);
  const [, q] = railItems([{ id: "a", name: "n", activity: "working" }], { focusedId: "b" });
  assert.strictEqual(q.alert, false);
  assert.strictEqual(q.on, false);
});

test("an expired AWS sign-in is alert even while the agent is working", () => {
  const [, s] = railItems([{ id: "a", name: "n", activity: "working", need: "sso" }], {});
  assert.strictEqual(s.alert, true);
  assert.match(s.label, /waiting for you/);
  assert.strictEqual(s.name, "n", "the tab title never carries the suffix");
});

test("alert follows the activity, never the focus", () => {
  const [, s] = railItems([{ id: "a", name: "n", activity: "working" }], { focusedId: "a" });
  assert.strictEqual(s.on, true);
  assert.strictEqual(s.alert, false);
});

test("every item carries a non-empty label", () => {
  const items = railItems([{ id: "a", name: "", activity: "waiting" }, { id: "b" }], { focusedId: "" });
  for (const i of items) assert.ok(i.label.length > 0, i.kind + " has no label");
});

test("an unnamed session falls back to its id, and a waiting one says so", () => {
  const [, a, b] = railItems([{ id: "abc123", activity: "waiting" }, { id: "d", name: "drain" }], {});
  assert.strictEqual(a.label, "abc123 — waiting for you");
  assert.strictEqual(b.label, "drain");
});

test("a waiting session's name stays undecorated even though its label carries the suffix", () => {
  const [, a] = railItems([{ id: "abc123", name: "review", activity: "waiting" }], {});
  assert.strictEqual(a.name, "review");
  assert.strictEqual(a.label, "review — waiting for you");
});

test("a rail entry carries the session name as its hover title", () => {
  const [, a, b] = railItems([{ id: "abc123", name: "review", activity: "waiting" }, { id: "d" }], {});
  assert.strictEqual(a.title, "review");
  assert.strictEqual(b.title, "d", "an unnamed session titles by id");
});

// F21-B (#172): a finished turn is neutral on the rail too: no amber dot, and the
// hover says whose turn it is. The session's name never carries the suffix.
test("a finished turn is not an alert: its dot is neutral and it says it is your turn", () => {
  const [, t] = railItems([{ id: "a", name: "review", activity: "waiting", need: "turn", attention: "done" }], {});
  assert.strictEqual(t.attention, "done");
  assert.strictEqual(t.alert, false);
  assert.strictEqual(t.title, "review \u00b7 your turn");
  assert.strictEqual(t.label, "review \u2014 your turn");
  assert.strictEqual(t.name, "review", "the tab title never carries the suffix");
  const [, u] = railItems([{ id: "a", name: "review", activity: "waiting", need: "turn" }], {});
  assert.strictEqual(u.attention, "done", "derived when the server sends no field");
});

test("a blocked session is an alert with attention block; a working one has neither", () => {
  const [, a, w] = railItems([{ id: "a", name: "n", activity: "waiting", need: "ask", attention: "block" }, { id: "b", name: "m", activity: "working" }], {});
  assert.strictEqual(a.attention, "block");
  assert.strictEqual(a.alert, true);
  assert.strictEqual(a.title, "n");
  assert.strictEqual(w.attention, "");
  assert.strictEqual(w.alert, false);
});

test("an exited session's stale sign-in need is no alert on the rail", () => {
  const [, s] = railItems([{ id: "a", name: "n", exited: true, activity: "done", need: "sso" }], {});
  assert.strictEqual(s.attention, "");
  assert.strictEqual(s.alert, false);
});

test("an entry is elevated only while a write window is open, on the record or a connection", () => {
  const now = Date.parse("2026-10-27T10:00:00Z");
  const live = "2026-10-27T10:30:00Z", gone = "2026-10-27T09:00:00Z";
  const items = railItems([
    { id: "a", name: "a", activity: "working", aws: { elevated_until: live } },
    { id: "b", name: "b", activity: "working", aws_connections: [{ name: "x", elevated_until: live }] },
    { id: "c", name: "c", activity: "working", aws: { elevated_until: gone } },
    { id: "d", name: "d", activity: "working" },
  ], { nowMs: now });
  assert.deepStrictEqual(items.slice(1, 5).map((i) => i.elevated), [true, true, false, false]);
  assert.strictEqual(items[0].elevated, false, "the toggle is never elevated");
});

test("an absent list is empty, never a throw", () => {
  assert.deepStrictEqual(railItems(null, null).map((i) => i.kind), ["toggle", "new"]);
});

// The steer rail's own half: the panel beside ONE session. Every assertion here
// is about a field the daemon really returns — the rail invents nothing, and a
// row it cannot fill is a row it does not draw.

test("the rail shows a row only for a field that has a value", () => {
  const s = { id: "a", name: "review", activity: "working", runtime: "box", created: "2026-09-05T10:00:00Z" };
  const m = railMeta(s, { model: "opus-5", effort: "high" }, "~/culiops-agent-box",
    Date.parse("2026-09-05T13:12:00Z"), tierBadge(s, "runc"));
  assert.deepStrictEqual(m.rows, [
    { k: "model", v: "opus-5" },
    { k: "effort", v: "high" },
    { k: "cwd", v: "~/culiops-agent-box" },
    { k: "started", v: hhmm("2026-09-05T10:00:00Z") + " · 3h 12m ago" },
  ]);
  // A contained session takes .bdg.box, matching tierBadge's neutral tone —
  // the host colour is reserved for the tier that has none.
  assert.strictEqual(m.tier.label, "box");
  assert.strictEqual(m.tier.cls, "bdg box");
  assert.match(m.tier.title, /Contained \(runc\)/);
  assert.strictEqual(m.name, "review");
});

test("no usage, no cwd, no created — the rail renders no rows, and invents none", () => {
  const m = railMeta({ id: "a", activity: "done" }, null, "", 0, null);
  assert.deepStrictEqual(m.rows, []);
  assert.strictEqual(m.name, "a");
  assert.strictEqual(m.tier, null); // no badge from the caller, no badge drawn
});

test("an uncontained session is drawn in tier.js's words, at tier.js's severity", () => {
  const s = { id: "a", runtime: "host" };
  const t = railMeta(s, null, "", 0, tierBadge(s, "runc")).tier;
  assert.strictEqual(t.label, "host");
  assert.strictEqual(t.cls, "bdg host"); // danger tone reads as the canvas's warn-coloured host chip
  assert.match(t.title, /NOT contained/);
});

test("an unknown or absent runtime reads as host, never as no tier at all", () => {
  // The fail-loud fallback. A rail that draws nothing here would tell the
  // operator there is nothing to worry about, on the surface they steer from,
  // while the sidebar shouts danger about the same session.
  for (const s of [{ id: "a" }, { id: "b", runtime: "" }, { id: "c", runtime: "mystery" }]) {
    const t = railMeta(s, null, "", 0, tierBadge(s, "runc")).tier;
    assert.strictEqual(t.label, "host", JSON.stringify(s));
    assert.strictEqual(t.cls, "bdg host", JSON.stringify(s));
  }
});

test("an exited session shows no tier row: a dead session's containment is not actionable", () => {
  const s = { id: "a", runtime: "host", exited: true };
  assert.strictEqual(tierBadge(s, "runc"), null); // tier.js's decision...
  assert.strictEqual(railMeta(s, null, "", 0, tierBadge(s, "runc")).tier, null); // ...carried through
});

test("an absent session is null, never a throw", () => {
  assert.strictEqual(railMeta(null, null, "", 0, null), null);
});

test("started is relative, and degrades rather than lying", () => {
  const created = "2026-09-05T10:00:00Z";
  const t0 = Date.parse(created);
  const clock = hhmm(created);
  assert.strictEqual(startedText(created, t0 + 30_000), clock + " · just now");
  assert.strictEqual(startedText(created, t0 + 9 * 60_000), clock + " · 9m ago");
  assert.strictEqual(startedText(created, t0 + 3 * 3600_000 + 12 * 60_000), clock + " · 3h 12m ago");
  assert.strictEqual(startedText(created, t0 + 2 * 86400_000 + 4 * 3600_000), clock + " · 2d 4h ago");
  assert.strictEqual(startedText(created, t0 - 5000), clock + " · just now"); // clock skew is not "-1m ago"
  assert.strictEqual(startedText("", t0), "");
  assert.strictEqual(startedText("not a date", t0), "");
});

test("started leads with a zero-padded HH:MM clock, joined by ·", () => {
  // A separate, format-focused assertion (independent of the relative-time
  // fixture above): the clock half is always two digits each side of the
  // colon, even for a single-digit hour/minute.
  const created = "2026-09-05T00:05:00Z";
  const text = startedText(created, Date.parse(created) + 60_000);
  const [clockPart, relPart] = text.split(" · ");
  assert.match(clockPart, /^\d{2}:\d{2}$/);
  assert.strictEqual(clockPart, hhmm(created));
  assert.strictEqual(relPart, "1m ago");
});

test("the context bar prefers the server's percentage to its own arithmetic", () => {
  // usage.go omits pct when the max is unknown or already exceeded, so a
  // present pct is the server's verdict and must win over a local division.
  assert.deepStrictEqual(ctxFill({ contextTokens: 134982, contextMax: 200000, pct: 67 }),
    { pct: 67, width: "67%", used: 134982, max: 200000 });
});

test("with no percentage it computes one, and clamps it", () => {
  assert.strictEqual(ctxFill({ contextTokens: 100000, contextMax: 200000 }).pct, 50);
  assert.strictEqual(ctxFill({ contextTokens: 300000, contextMax: 200000 }).pct, 100);
  assert.strictEqual(ctxFill({ contextTokens: 5000, contextMax: 0 }).pct, null);
  assert.strictEqual(ctxFill({}).pct, null);
  assert.strictEqual(ctxFill(null).pct, null);
});

test("a null percentage draws no fill rather than a zero-width one", () => {
  assert.strictEqual(ctxFill(null).width, "0%");
});

test("the empty context note: compacted with no size reported is not 'no usage yet'", () => {
  assert.strictEqual(ctxEmptyNote({ claude: true }, null), "No transcript usage reported yet.");
  assert.strictEqual(ctxEmptyNote({ claude: true }, { postCompact: true, contextTokens: 0 }), "Compacted; size not reported.");
  // tokens but no pct: the model is not in the window table, so no bar is drawn.
  assert.strictEqual(ctxEmptyNote({ claude: true }, { contextTokens: 25340, postCompact: true }), "Window size unknown for this model.");
  assert.strictEqual(ctxEmptyNote({ claude: true }, { contextTokens: 5000, model: "claude-opus-4-6" }), "Window size unknown for this model.");
  assert.strictEqual(ctxEmptyNote({ claude: false }, null), "Not a Claude session \u2014 no context to report.");
});

test("the 'used' row says so when the count is the size right after a compaction", () => {
  assert.strictEqual(ctxUsedText(ctxFill({ contextTokens: 25340, contextMax: 1000000, pct: 3, postCompact: true }), { postCompact: true }),
    "25,340 / 1,000,000 (after compact)");
  assert.strictEqual(ctxUsedText(ctxFill({ contextTokens: 25340, contextMax: 1000000, pct: 3 }), {}), "25,340 / 1,000,000");
  assert.strictEqual(ctxUsedText(ctxFill({ contextTokens: 25340 }), null), "25,340");
});

test("railMeta appends the caller's extra rows after its own", () => {
  const s = { id: "a", activity: "done", created: "2026-09-05T10:00:00Z" };
  const m = railMeta(s, { model: "opus-5" }, "", Date.parse("2026-09-05T10:00:30Z"), null,
    [{ k: "aws", v: "sandbox · 123456789012" }, null]);
  assert.deepStrictEqual(m.rows.map((r) => r.k), ["model", "started", "aws"]);
  assert.deepStrictEqual(railMeta(s, null, "", 0, null, null).rows.map((r) => r.k), ["started"]);
});

test("awsStatsRows: never before the first mint, the zero time is not year 1", () => {
  assert.deepStrictEqual(awsStatsRows(null, 0), []);
  assert.deepStrictEqual(awsStatsRows({ stats: { mints: 0, last_mint: "0001-01-01T00:00:00Z" } }, Date.now()),
    [{ k: "minted", v: "0 mints · last mint never" }]);
  const now = Date.parse("2026-10-16T12:10:00Z");
  const rows = awsStatsRows({ stats: { mints: 1, last_mint: "2026-10-16T12:05:00Z" } }, now);
  assert.strictEqual(rows.length, 1);
  assert.strictEqual(rows[0].v, "1 mint · last mint " + startedText("2026-10-16T12:05:00Z", now));
});

test("awsStatsRows: last error is its own wrapping row, verbatim", () => {
  const err = "aws sso login --profile sandbox-admin (session expired)";
  const rows = awsStatsRows({ stats: { mints: 3, last_mint: "0001-01-01T00:00:00Z", last_error: err } }, 0);
  assert.deepStrictEqual(rows[1], { k: "error", v: err, wrap: true });
  assert.strictEqual(awsStatsRows({ stats: { mints: 3, last_mint: "0001-01-01T00:00:00Z", last_error: "" } }, 0).length, 1);
});

test("egressRow: absent without the egress object; allowlist and the denied count otherwise", () => {
  assert.strictEqual(egressRow(null), null);
  assert.strictEqual(egressRow({ stats: {} }), null);
  assert.deepStrictEqual(egressRow({ egress: { mode: "allowlist", denied: 0 } }), { k: "egress", v: "allowlist \u00b7 0 denied since start", wrap: true });
  assert.deepStrictEqual(egressRow({ egress: { mode: "allowlist", denied: 3 } }), { k: "egress", v: "allowlist \u00b7 3 denied since start", wrap: true });
});

test("egressRow: a missing or non-numeric denied count reads as zero, never NaN", () => {
  assert.strictEqual(egressRow({ egress: { mode: "allowlist" } }).v, "allowlist \u00b7 0 denied since start");
  assert.strictEqual(egressRow({ egress: { mode: "allowlist", denied: "x" } }).v, "allowlist \u00b7 0 denied since start");
});

test("egressRow: open mode says open and counts denials; anything else stays allowlist", () => {
  assert.strictEqual(egressRow({ egress: { mode: "open", denied: 2 } }).v, "open \u00b7 2 denied since start");
  assert.strictEqual(egressRow({ egress: { mode: "open", denied: "x" } }).v, "open \u00b7 0 denied since start");
  assert.strictEqual(egressRow({ egress: { mode: "allowlist", denied: 2 } }).v, "allowlist \u00b7 2 denied since start");
  assert.strictEqual(egressRow({ egress: { denied: 1 } }).v, "allowlist \u00b7 1 denied since start");
});

test("egressRow: the polled session's mode wins over the cached one, which is only as fresh as the last focus (#95)", () => {
  assert.strictEqual(egressRow({ egress: { mode: "allowlist", denied: 1 } }, "open").v, "open \u00b7 1 denied since start");
  assert.strictEqual(egressRow({ egress: { mode: "open", denied: 1 } }, "allowlist").v, "allowlist \u00b7 1 denied since start");
  assert.strictEqual(egressRow({ egress: { mode: "open", denied: 1 } }, "").v, "open \u00b7 1 denied since start");
  assert.strictEqual(egressRow(null, "open"), null);
});

// egress_change (#95): a profile edit reached the running session.
test("policyRow: absent without egress_change", () => {
  assert.strictEqual(policyRow(null), null);
  assert.strictEqual(policyRow({}), null);
});

test("policyRow: a widening says what was added, that it applies now, and is a warning", () => {
  const at = "2026-11-24T09:05:00Z";
  const r = policyRow({ egress_change: { at, added: ["pypi.org", "files.pythonhosted.org"], open: false } });
  assert.strictEqual(r.k, "policy");
  assert.strictEqual(r.v, "changed " + hhmm(at) + " \u00b7 added pypi.org, files.pythonhosted.org \u00b7 applies now");
  assert.strictEqual(r.chip, "v warn");
  assert.strictEqual(r.wrap, true);
  assert.ok(r.title.includes("next connection"));
  const o = policyRow({ egress_change: { at, open: true, open_changed: true } });
  assert.strictEqual(o.v, "changed " + hhmm(at) + " \u00b7 now open to any public host on 443 \u00b7 applies now");
  assert.strictEqual(o.chip, "v warn");
});

test("policyRow: a narrowing names what was removed and the tunnels it closed, without a warning", () => {
  const at = "2026-11-24T09:05:00Z";
  const r = policyRow({ egress_change: { at, removed: ["pypi.org"], open: false, open_changed: true, closed: 2 } });
  assert.strictEqual(r.v, "changed " + hhmm(at) + " \u00b7 removed pypi.org \u00b7 no longer open \u00b7 2 tunnels closed \u00b7 applies now");
  assert.strictEqual(r.chip, undefined);
  assert.ok(policyRow({ egress_change: { at, removed: ["a.example"], closed: 1 } }).v.includes("1 tunnel closed"));
});

test("policyRow: a hold says what the box holds back and why", () => {
  const r = policyRow({ egress_change: { open: false, held: ["pypi.org"], held_open: true, reason: "shared_box" } });
  assert.strictEqual(r.v, "held back: pypi.org, open \u2014 another profile shares this box; applies when its sessions end, or on a new session");
  assert.strictEqual(r.chip, undefined);
  const g = policyRow({ egress_change: { at: "2026-11-24T09:05:00Z", removed: ["pypi.org"], open: false, reason: "profile_gone" } });
  assert.ok(g.v.endsWith("profile removed or no longer boxed: only the binding's own hosts apply"), g.v);
});

// bundle_drops: what the config bundle left out of a box. The toast is a glance,
// so it names at most three; the rail has room, so it names all of them but
// groups by reason rather than repeating one long reason per key.
const drop = (kind, name, reason) => ({ kind, name, reason });

test("dropLabel: a whole-kind drop (name *) is named by its kind, anything else by its name", () => {
  assert.strictEqual(dropLabel(drop("settings", "*", "r")), "settings");
  assert.strictEqual(dropLabel(drop("mcp", "playwright", "r")), "playwright");
});

test("toastDropsSuffix: empty for no drops; names at most three, then an ellipsis", () => {
  assert.strictEqual(toastDropsSuffix([]), "");
  assert.strictEqual(toastDropsSuffix(undefined), "");
  const d = (n) => drop("skill", n, "r");
  assert.strictEqual(toastDropsSuffix([d("a"), d("b"), d("c")]), " \u00b7 box left out 3: a, b, c");
  assert.strictEqual(toastDropsSuffix([d("a"), d("b"), d("c"), d("d"), d("e")]), " \u00b7 box left out 5: a, b, c, \u2026");
  assert.strictEqual(toastDropsSuffix([drop("settings", "*", "r")]), " \u00b7 box left out 1: settings");
});

test("dropsRow: null without drops; otherwise one wrapping row grouped by kind and reason in first-seen order", () => {
  assert.strictEqual(dropsRow({}), null);
  assert.strictEqual(dropsRow({ bundle_drops: [] }), null);
  const row = dropsRow({ bundle_drops: [
    drop("settings", "hooks", "r1"), drop("mcp", "playwright", "replaced by the image's playwright"),
    drop("settings", "env", "r1"), drop("settings", "permissions", "r1"),
  ] });
  assert.deepStrictEqual(row, { k: "left out", v: "hooks, env, permissions \u2014 r1; playwright \u2014 replaced by the image's playwright", wrap: true });
  // The same reason under two kinds is two groups: the kind is part of the key.
  const two = dropsRow({ bundle_drops: [drop("skill", "a", "r"), drop("mcp", "b", "r")] });
  assert.strictEqual(two.v, "a \u2014 r; b \u2014 r");
});

// N3: the steer rail's token rows. Only names, outcomes and times: the payload
// has no value, variable name or path to print.
const { stRowText, stRailRows } = require("../static/steerrail.js");

test("stRowText: entry, outcome and the local clock", () => {
  const time = "2026-10-19T14:05:00Z";
  assert.strictEqual(stRowText({ provider: "static", subject: "sentry-corp", outcome: "fetched", time }), "Token \u00b7 sentry-corp \u00b7 fetched \u00b7 " + hhmm(time));
  assert.strictEqual(stRowText({ subject: "sentry-corp", outcome: "denied_scope", time }), "Token \u00b7 sentry-corp \u00b7 denied scope \u00b7 " + hhmm(time));
  assert.strictEqual(stRowText({ subject: "sentry-corp", outcome: "rate_limited", time }), "Token \u00b7 sentry-corp \u00b7 rate limited \u00b7 " + hhmm(time));
  // a row with no usable time has no clock, never "Invalid Date"
  assert.strictEqual(stRowText({ subject: "x", outcome: "fetched" }), "Token \u00b7 x \u00b7 fetched");
  assert.doesNotMatch(stRowText({ subject: "x", outcome: "fetched", time, error: "tok-SECRET-1234" }), /SECRET/);
});

test("stRailRows: at most five fetch rows, then one row naming the bound entries", () => {
  const static_recent = Array.from({ length: 8 }, (_, i) => ({ provider: "static", subject: "t" + i, outcome: "fetched", time: "2026-10-19T14:05:00Z" }));
  const rows = stRailRows({ static_recent, tokens: [{ name: "sentry-corp" }, { name: "grafana-prd" }] });
  assert.strictEqual(rows.length, 6);
  assert.deepStrictEqual(rows[0], { k: "token", v: "Token \u00b7 t0 \u00b7 fetched \u00b7 " + hhmm("2026-10-19T14:05:00Z"), wrap: true });
  assert.deepStrictEqual(rows[5], { k: "tokens", v: "sentry-corp, grafana-prd", wrap: true });
  assert.deepStrictEqual(stRailRows(null), []);
  assert.deepStrictEqual(stRailRows({}), []);
});

test("stRailRows: a production entry adds a row drawn as the AWS production chip", () => {
  const rows = stRailRows({ static_recent: [], tokens: [{ name: "a" }, { name: "b", prod: true }] });
  assert.deepStrictEqual(rows.map((r) => r.k), ["tokens", "prod tokens"]);
  assert.deepStrictEqual(rows[1], { k: "prod tokens", v: "b", chip: "bdg aws prod", wrap: true });
});

// Clone refresh state. The row and the toast tail are pure strings; the
// rail draws them with textContent (kvRow), the toast with textContent too.
test("cloneRow: null without s.clone", () => {
  assert.strictEqual(cloneRow({}), null);
  assert.strictEqual(toastCloneSuffix({}), "");
  assert.strictEqual(cloneLevel(undefined), null);
});

test("cloneRow: up to date is an info row with the fetch-origin tooltip", () => {
  const r = cloneRow({ agent: "ab", clone: { refreshed: true, base: "main", head_in_host: true } });
  assert.strictEqual(r.k, "clone");
  assert.strictEqual(r.v, "up to date with host main");
  assert.strictEqual(r.wrap, true);
  assert.strictEqual(r.chip, undefined);
  assert.ok(r.title.includes("git fetch origin"));
  assert.strictEqual(cloneLevel({ refreshed: true }), "info");
});

test("cloneRow: unsent work names the fetch command; detached has none", () => {
  const r = cloneRow({ agent: "ab", clone: { refreshed: true, head_in_host: false, branch: "feat", ahead: 2 } });
  assert.ok(r.v.includes("branch feat: 2 commits not sent to host (`culibox clone fetch ab feat`)"));
  const one = cloneRow({ agent: "ab", clone: { head_in_host: false, branch: "feat", ahead: 1 } });
  assert.ok(one.v.includes("1 commit not sent"));
  const zero = cloneRow({ agent: "ab", clone: { head_in_host: false, branch: "feat", ahead: 0 } });
  assert.ok(zero.v.includes("branch feat: commits not sent to host (`culibox clone fetch ab feat`)"));
  const d = cloneRow({ agent: "ab", clone: { head_in_host: false, branch: "" } });
  assert.ok(d.v.includes("detached HEAD: commits not sent to host"));
  assert.ok(!d.v.includes("clone fetch"));
  // Unknown (absent) is not "unsent".
  assert.ok(!cloneRow({ agent: "ab", clone: { refreshed: true, branch: "feat" } }).v.includes("not sent"));
});

test("cloneRow: rewritten is a warning naming stale branches, or 'may carry'", () => {
  const r = cloneRow({ clone: { rewritten: true, refreshed: true, base: "main", stale_branches: ["a", "b"] } });
  assert.strictEqual(r.chip, "v warn");
  assert.ok(r.v.startsWith("Host main was rewritten since this clone last saw it. Branches a, b still carry the old history: rebase them onto origin/main or recreate the clone before `clone fetch`."));
  const m = cloneRow({ clone: { rewritten: true, refreshed: false, base: "main", stale_branches: [] } });
  assert.strictEqual(m.chip, "v warn");
  assert.ok(m.v.includes("Branches may carry the old history: rebase them onto origin/main or recreate the clone before `clone fetch`."));
  assert.strictEqual(cloneLevel({ rewritten: true, refreshed: false }), "warn");
});

test("cloneRow: rewritten but fully fixed is only info", () => {
  const c = { rewritten: true, refreshed: true, base: "main", stale_branches: [], head_in_host: false, branch: "x", ahead: 3 };
  const r = cloneRow({ agent: "ab", clone: c });
  assert.strictEqual(r.chip, undefined);
  assert.ok(r.v.includes("host main was rewritten earlier; no branch here carries the old history"));
  assert.ok(!r.v.includes("Host main was rewritten since"));
  assert.ok(!r.v.includes("not sent")); // unsent is suppressed while rewritten
  assert.strictEqual(cloneLevel(c), "info");
});

test("cloneRow: master base is used in every clause", () => {
  const r = cloneRow({ clone: { refreshed: true, base: "master", ff_from: "aaaaaaaaaa", ff_to: "bbbbbbbbbb", ff_count: 3, branch: "feat", behind: 2 } });
  assert.ok(r.v.includes("master updated aaaaaaa..bbbbbbb (+3)"));
  assert.ok(r.v.includes("feat is 2 behind master"));
  assert.ok(!r.v.includes("main"));
  assert.ok(r.title.includes("origin/master"));
  // The fetch hint carries -base (flags before positionals) off the default branch only.
  const u = cloneRow({ agent: "ab", clone: { base: "master", head_in_host: false, branch: "feat", ahead: 1 } });
  assert.ok(u.v.includes("`culibox clone fetch -base master ab feat`"));
  assert.ok(!u.v.includes("main"));
  const um = cloneRow({ agent: "ab", clone: { base: "main", head_in_host: false, branch: "feat", ahead: 1 } });
  assert.ok(um.v.includes("`culibox clone fetch ab feat`"));
  assert.ok(!um.v.includes("-base"));
  assert.ok(!cloneRow({ agent: "ab", clone: { head_in_host: false, branch: "feat", ahead: 1 } }).v.includes("-base"));
  // A missing or garbled ff_count drops the "(+N)" instead of printing undefined.
  assert.strictEqual(cloneRow({ clone: { refreshed: true, base: "main", ff_from: "aaaaaaaaaa", ff_to: "bbbbbbbbbb" } }).v, "main updated aaaaaaa..bbbbbbb");
  assert.ok(!cloneRow({ clone: { refreshed: true, ff_from: "aaaaaaaaaa", ff_to: "bbbbbbbbbb", ff_count: "x" } }).v.includes("+"));
  assert.ok(cloneRow({ clone: { refreshed: true, base: "master", ff_refused: true } }).v.includes("local master has commits host master lacks; not updated"));
  assert.ok(cloneRow({ clone: { skipped: "created", base: "master" } }).v.includes("new clone of host master"));
});

test("cloneRow: in-use, other-instance and no-base texts", () => {
  assert.strictEqual(cloneRow({ clone: { skipped: "in-use" } }).v, "not refreshed: another session is using this clone");
  assert.strictEqual(cloneRow({ clone: { skipped: "other-instance" } }).v, "not refreshed: a box from another CuliBox instance holds this clone");
  assert.strictEqual(cloneRow({ clone: { skipped: "no-base" } }).v, "not refreshed: host repo has neither main nor master");
});

test("cloneRow: error and unverified are warnings", () => {
  const r = cloneRow({ clone: { error: "x", unverified: true } });
  assert.strictEqual(r.chip, "v warn");
  assert.ok(r.v.includes("Clone not refreshed: x; the box is on older code."));
  assert.ok(r.v.includes("This clone's history is unverified: created before refresh tracking. Recreate the clone to clear this."));
});

test("cloneRow: an error beside a completed refresh does not say the box is on older code", () => {
  const r = cloneRow({ clone: { refreshed: true, error: "could not check host history: x" } });
  assert.strictEqual(r.chip, "v warn");
  assert.ok(r.v.includes("Clone refreshed, but could not check host history: x."));
  assert.ok(!r.v.includes("older code"));
  assert.ok(!r.v.includes("not refreshed"));
  assert.strictEqual(toastCloneSuffix({ clone: { refreshed: true, error: "y" } }), " \u00b7 Clone refreshed, but y.");
});

test("toastCloneSuffix: one warn clause, else one short info clause", () => {
  assert.strictEqual(toastCloneSuffix({ clone: { error: "x" } }), " \u00b7 Clone not refreshed: x; the box is on older code.");
  assert.strictEqual(cloneLevel({ error: "x" }), "warn");
  const s = { agent: "ab", clone: { refreshed: true, base: "main", head_in_host: false, branch: "f", ahead: 1 } };
  assert.strictEqual(toastCloneSuffix(s), " \u00b7 clone: branch f: 1 commit not sent to host (`culibox clone fetch ab f`)");
  assert.strictEqual(toastCloneSuffix({ clone: { skipped: "in-use" } }), " \u00b7 clone: not refreshed: another session is using this clone");
  assert.strictEqual(toastCloneSuffix({ clone: { refreshed: true, base: "main" } }), " \u00b7 clone: up to date with host main");
  assert.strictEqual(toastCloneSuffix({ clone: {} }), "");
});

test("clone values stay literal text: markup in a branch is never interpreted", () => {
  const evil = "<img src=x onerror=alert(1)>";
  const s = { agent: "ab", clone: { head_in_host: false, branch: evil, ahead: 1, stale_branches: [evil], rewritten: true, error: evil } };
  const r = cloneRow(s);
  assert.ok(r.v.includes(evil)); // present verbatim, as a string
  assert.ok(toastCloneSuffix(s).includes(evil));
  assert.ok(cloneParts(s.clone, "ab").warn.join("").includes(evil));
  // The render path: kvRow (the only consumer of the row) uses textContent, and
  // the toast sets #toast-msg via textContent. Neither may use innerHTML.
  const fs = require("node:fs");
  const path = require("node:path");
  const rail = fs.readFileSync(path.join(__dirname, "../static/steerrail.js"), "utf8");
  const shell = fs.readFileSync(path.join(__dirname, "../static/shell.js"), "utf8");
  assert.ok(/function kvRow[\s\S]*?b\.textContent = v;/.test(rail));
  assert.ok(!/innerHTML/.test(rail));
  assert.ok(/getElementById\("toast-msg"\)\.textContent = msg/.test(shell));
});

test("lockedRow: null without locked files; otherwise one wrapping row naming them and why", () => {
  assert.strictEqual(lockedRow({}), null);
  assert.strictEqual(lockedRow({ locked_files: [] }), null);
  const row = lockedRow({ locked_files: [".mcp.json", "CLAUDE.md"] });
  assert.strictEqual(row.k, "locked");
  assert.strictEqual(row.wrap, true);
  assert.strictEqual(row.v, "Locked on your Mac while this session runs: .mcp.json, CLAUDE.md " +
    "(macOS cannot otherwise stop the box changing them through another letter case)");
});
