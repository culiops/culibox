"use strict";
// activitymodel.js: the Activity view's pure model (spec 5.8) and its load helpers.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const {
  activityModel, ACTIVITY_FILTERS, filterKind, outcomeText, actTime, actReadReply, actLoadDone,
  actSlide, actOldest, actContext, actDelta, actRowKey, actToolRows,
} = require("../static/activitymodel.js");

const NOW = Date.parse("2026-10-27T14:35:10");
const at = (hms) => Date.parse("2026-10-27T" + hms);
const ev = (over) => Object.assign({ ts: at("14:32:51"), kind: "tool", tool: "Bash", detail: "ls", state: "ok" }, over);
const sess = (over) => Object.assign({ id: "s1", claude: true, need: "", ask: "", ask_full: "", summary: "Done.", replyable: false }, over);
const resp = (over) => Object.assign({
  claude: true, events: [], more: false, partial: false, files: [],
  summary: { tool_calls: 38, files_changed: 3, credentials: 12, running_s: 41 * 60 }, waiting_since: 0,
}, over);
const model = (o) => activityModel(Object.assign({ nowMs: NOW, filter: "all" }, o));

test("filters are All / Commands / Files / Cloud / You (no Asks)", () => {
  assert.deepStrictEqual(ACTIVITY_FILTERS, ["all", "commands", "files", "cloud", "you"]);
});

test("filterKind: Bash is a command, file tools are files, credential and approval are cloud, you is you", () => {
  assert.strictEqual(filterKind(ev({ tool: "Bash" })), "commands");
  for (const t of ["Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Grep", "Glob"]) {
    assert.strictEqual(filterKind(ev({ tool: t })), "files", t);
  }
  assert.strictEqual(filterKind(ev({ kind: "credential", tool: undefined })), "cloud");
  assert.strictEqual(filterKind(ev({ kind: "approval", tool: undefined })), "cloud");
  assert.strictEqual(filterKind(ev({ kind: "you", tool: undefined })), "you");
  assert.strictEqual(filterKind(ev({ kind: "clear", tool: undefined })), "you");
  // Neither a command nor a file: only under All.
  assert.strictEqual(filterKind(ev({ tool: "WebFetch" })), "other");
  assert.strictEqual(filterKind(ev({ tool: "mcp__x__y" })), "other");
  assert.strictEqual(filterKind(null), "other");
});

test("outcomeText: the exact strings", () => {
  assert.strictEqual(outcomeText(ev({ state: "ok", ms: 1400 }), NOW), "ok · 1.4s");
  assert.strictEqual(outcomeText(ev({ state: "ok", ms: 200 }), NOW), "ok · 0.2s");
  assert.strictEqual(outcomeText(ev({ state: "ok", ms: 3200 }), NOW), "ok · 3.2s");
  assert.strictEqual(outcomeText(ev({ state: "ok", ms: 80 }), NOW), "ok · 80ms");
  assert.strictEqual(outcomeText(ev({ state: "ok", ms: 125000 }), NOW), "ok · 2m 5s");
  assert.strictEqual(outcomeText(ev({ state: "ok", tool: "WebFetch" }), NOW), "ok");
  assert.strictEqual(outcomeText(ev({ state: "error", exit: 254, ms: 900 }), NOW), "error · exit 254");
  assert.strictEqual(outcomeText(ev({ state: "error" }), NOW), "error");
  assert.strictEqual(outcomeText(ev({ tool: "Edit", state: "ok", add: 18, del: 0 }), NOW), "+18 −0");
  assert.strictEqual(outcomeText(ev({ tool: "Edit", state: "ok", add: 9, del: 2 }), NOW), "+9 −2");
  assert.strictEqual(outcomeText(ev({ tool: "Read", state: "ok", lines: 212 }), NOW), "212 lines");
  assert.strictEqual(outcomeText(ev({ tool: "Read", state: "ok", lines: 1 }), NOW), "1 line");
  assert.strictEqual(outcomeText(ev({ state: "pending" }), NOW), "running");
  assert.strictEqual(outcomeText(ev({ kind: "credential", state: "minted" }), NOW), "minted");
  assert.strictEqual(outcomeText(ev({ kind: "credential", state: "refused" }), NOW), "refused");
  assert.strictEqual(outcomeText(ev({ kind: "approval", state: "granted" }), NOW), "granted");
  assert.strictEqual(outcomeText(ev({ kind: "approval", state: "dropped" }), NOW), "dropped");
  assert.strictEqual(outcomeText(ev({ kind: "you", state: "" }), NOW), "message");
  assert.strictEqual(outcomeText(ev({ kind: "clear" }), NOW), "");
  // The current ask: age from its own timestamp.
  assert.strictEqual(outcomeText({ kind: "ask", ts: NOW - 2 * 60000 - 5000 }, NOW), "waiting 2m");
  assert.strictEqual(outcomeText({ kind: "ask", ts: NOW - 40000 }, NOW), "waiting 40s");
  assert.strictEqual(outcomeText({ kind: "ask", ts: NOW - 3720000 }, NOW), "waiting 1h 2m");
  assert.strictEqual(outcomeText({ kind: "ask", ts: 0 }, NOW), "waiting");
  assert.strictEqual(outcomeText({ kind: "ask", ts: NOW + 60000 }, NOW), "waiting"); // clock skew never goes negative
});

test("outcomeText never throws on junk", () => {
  for (const x of [null, undefined, {}, { kind: "tool" }, { kind: 7, state: {} }]) {
    assert.strictEqual(typeof outcomeText(x, NOW), "string");
  }
});

test("rows are newest-first as given, with local time, kind, detail and a tone", () => {
  const m = model({
    session: sess(),
    resp: resp({
      events: [
        ev({ ts: at("14:32:51"), tool: "Bash", detail: "aws ecs describe-services", state: "ok", ms: 1400 }),
        ev({ ts: at("14:31:02"), kind: "credential", tool: undefined, detail: "aws culipulse-prod", state: "minted" }),
        ev({ ts: at("14:29:58"), tool: "Bash", detail: "aws logs tail", state: "error", exit: 254 }),
        ev({ ts: at("14:28:30"), kind: "you", tool: undefined, detail: "Focus on the deploy", state: "" }),
        ev({ ts: at("14:28:00"), kind: "clear", tool: undefined, detail: "" }),
      ],
    }),
  });
  assert.deepStrictEqual(m.rows.map((r) => r.time), ["14:32:51", "14:31:02", "14:29:58", "14:28:30", "14:28:00"]);
  assert.deepStrictEqual(m.rows.map((r) => r.kind), ["Bash", "Credential", "Bash", "You", "Cleared"]);
  assert.deepStrictEqual(m.rows.map((r) => r.outcome.tone), ["ok", "ok", "err", "muted", "muted"]);
  assert.deepStrictEqual(m.rows.map((r) => r.cls), ["", "", "", "you", "you"]);
  assert.strictEqual(m.rows[0].outcome.text, "ok · 1.4s");
  assert.strictEqual(m.rows[2].outcome.text, "error · exit 254");
  assert.strictEqual(m.rows[4].detail, "conversation cleared");
  assert.ok(m.rows.every((r) => typeof r.key === "string" && r.key));
});

test("actTone: a refusal is muted, a failure and a write grant are red", () => {
  const tone = (e) => model({ session: sess(), resp: resp({ events: [e] }) }).rows[0].outcome.tone;
  const cred = (state) => ev({ kind: "credential", tool: undefined, state });
  const appr = (state) => ev({ kind: "approval", tool: undefined, state });
  assert.strictEqual(tone(cred("minted")), "ok");
  assert.strictEqual(tone(cred("refused")), "muted");
  assert.strictEqual(tone(cred("failed")), "err");
  assert.strictEqual(tone(cred("")), "muted");
  assert.strictEqual(tone(cred("whatever")), "muted");
  assert.strictEqual(tone(appr("refused")), "muted");
  for (const st of ["granted", "extended"]) assert.strictEqual(tone(appr(st)), "err", st);
  for (const st of ["expired", "dropped", "ended"]) assert.strictEqual(tone(appr(st)), "muted", st);
  assert.strictEqual(tone(ev({ state: "error", exit: 1 })), "err");
  assert.strictEqual(tone(ev({ tool: "Edit", add: 18, del: 0 })), "add");
  assert.strictEqual(tone(ev({ state: "pending" })), "muted");
});

test("tones: diffs are add, a refusal and an end are muted, a failure and a write grant are err", () => {
  const m = model({
    session: sess(),
    resp: resp({ events: [
      ev({ tool: "Edit", add: 18, del: 0 }),
      ev({ kind: "credential", tool: undefined, state: "refused" }),
      ev({ kind: "credential", tool: undefined, state: "failed" }),
      ev({ kind: "approval", tool: undefined, state: "granted" }),
      ev({ kind: "approval", tool: undefined, state: "expired" }),
      ev({ state: "pending" }),
    ] }),
  });
  assert.deepStrictEqual(m.rows.map((r) => r.outcome.tone), ["add", "muted", "err", "err", "muted", "muted"]);
  assert.strictEqual(m.rows[2].outcome.text, "failed");
});

test("the current ask is the top amber row, only when need is ask", () => {
  const events = [ev({ ts: at("14:32:51"), detail: "aws ecs describe-services" })];
  const asking = model({
    session: sess({ need: "ask", ask: "aws ecs update-service ...", ask_full: "aws ecs update-service --cluster c --service worker" }),
    resp: resp({ events, waiting_since: NOW - 2 * 60000 }),
  });
  assert.strictEqual(asking.rows.length, 2);
  const top = asking.rows[0];
  assert.strictEqual(top.kind, "Asks to run");
  assert.strictEqual(top.cls, "ask");
  assert.strictEqual(top.detail, "aws ecs update-service --cluster c --service worker"); // ask_full wins
  assert.strictEqual(top.outcome.text, "waiting 2m");
  assert.strictEqual(top.outcome.tone, "amber");
  assert.strictEqual(asking.ask, true);

  const short = model({ session: sess({ need: "ask", ask: "just the short one" }), resp: resp({ events: [] }) });
  assert.strictEqual(short.rows[0].detail, "just the short one");
  assert.strictEqual(short.rows[0].outcome.text, "waiting"); // no waiting_since: no age

  for (const need of ["", "sso", "store_locked", undefined]) {
    const m = model({ session: sess({ need, ask: "x", ask_full: "x" }), resp: resp({ events, waiting_since: NOW - 60000 }) });
    assert.strictEqual(m.rows.length, 1, "need=" + need);
    assert.strictEqual(m.ask, false);
    assert.ok(m.rows.every((r) => r.cls !== "ask"));
  }
});

test("while asking, the pending tool row the ask stands for is not drawn twice", () => {
  const m = model({
    session: sess({ need: "ask", ask_full: "aws ecs update-service" }),
    resp: resp({ events: [
      ev({ ts: at("14:33:10"), detail: "aws ecs update-service", state: "pending" }),
      ev({ ts: at("14:32:51"), detail: "aws ecs describe-services", state: "ok" }),
      ev({ ts: at("14:20:00"), detail: "sleep 900", state: "pending" }), // an older pending tool stays
    ] }),
  });
  assert.deepStrictEqual(m.rows.map((r) => r.detail), ["aws ecs update-service", "aws ecs describe-services", "sleep 900"]);
  assert.strictEqual(m.rows[0].cls, "ask");
  // Not asking: the pending row is an ordinary running row.
  const idle = model({ session: sess(), resp: resp({ events: [ev({ state: "pending", detail: "make" })] }) });
  assert.strictEqual(idle.rows[0].outcome.text, "running");
});

test("the ask row shows under every filter; other rows follow the filter", () => {
  const events = [
    ev({ tool: "Bash", detail: "ls" }),
    ev({ tool: "Read", detail: "a.go", lines: 3 }),
    ev({ kind: "credential", tool: undefined, detail: "aws p", state: "minted" }),
    ev({ kind: "you", tool: undefined, detail: "hi", state: "" }),
    ev({ tool: "WebFetch", detail: "https://x" }),
  ];
  const s = sess({ need: "ask", ask: "rm -rf x" });
  const kinds = (f) => model({ session: s, resp: resp({ events }), filter: f }).rows.map((r) => r.kind);
  assert.deepStrictEqual(kinds("all"), ["Asks to run", "Bash", "Read", "Credential", "You", "WebFetch"]);
  assert.deepStrictEqual(kinds("commands"), ["Asks to run", "Bash"]);
  assert.deepStrictEqual(kinds("files"), ["Asks to run", "Read"]);
  assert.deepStrictEqual(kinds("cloud"), ["Asks to run", "Credential"]);
  assert.deepStrictEqual(kinds("you"), ["Asks to run", "You"]);
  assert.deepStrictEqual(kinds("bogus"), kinds("all")); // an unknown filter is All
});

const KIT_TILES = ["Tool calls", "Files changed", "Credentials", "Waiting for you", "Session age"];

test("tiles are the kit's five, sentence case, Session age last", () => {
  const m = model({ session: sess({ need: "ask", ask: "x" }), resp: resp() });
  assert.deepStrictEqual([...m.tiles], [
    { n: 38, label: "Tool calls" },
    { n: 3, label: "Files changed" },
    { n: 12, label: "Credentials" },
    { n: 1, label: "Waiting for you" },
    { n: "41m", label: "Session age" },
  ]);
  assert.deepStrictEqual(m.tiles.map((t) => t.label), KIT_TILES);
  assert.strictEqual(m.empty, null); // a Claude session never gets the empty block
  const quiet = model({ session: sess(), resp: resp({ summary: { tool_calls: 0, files_changed: 0, credentials: 0, running_s: 35 } }) });
  assert.deepStrictEqual(quiet.tiles.map((t) => t.n), [0, 0, 0, 0, "35s"]);
  const long = model({ session: sess(), resp: resp({ summary: { tool_calls: 1, files_changed: 0, credentials: 0, running_s: 4320 } }) });
  assert.strictEqual(long.tiles[4].n, "1h 12m");
  const days = model({ session: sess(), resp: resp({ summary: { tool_calls: 1, files_changed: 0, credentials: 0, running_s: 11 * 86400 + 13 * 3600 } }) });
  assert.strictEqual(days.tiles[4].n, "11d 13h");
  // An sso need also waits for you.
  assert.strictEqual(model({ session: sess({ need: "sso" }), resp: resp() }).tiles[3].n, 1);
});

test("tiles read — until the first reply", () => {
  const m = model({ session: sess(), resp: null });
  assert.deepStrictEqual(m.tiles.map((t) => t.label), KIT_TILES);
  assert.deepStrictEqual(m.tiles.map((t) => t.n), ["—", "—", "—", "—", "—"]);
  assert.strictEqual(m.tiles[0].n, "\u2014"); // U+2014, never a fake 0
  assert.strictEqual(m.loaded, false);
  // A session that needs you still reads — until the reply: no number is drawn before data.
  assert.deepStrictEqual(model({ session: sess({ need: "ask" }), resp: undefined }).tiles.map((t) => t.n), ["—", "—", "—", "—", "—"]);
  // The first reply replaces every dash, and the model says it is loaded.
  const got = model({ session: sess(), resp: resp() });
  assert.strictEqual(got.loaded, true);
  assert.ok(got.tiles.every((t) => t.n !== "—"));
});

test("files changed carry a +a −d text", () => {
  const m = model({ session: sess(), resp: resp({ files: [
    { path: "docs/a.md", add: 18, del: 0 }, { path: "scripts/x.sh", add: 9, del: 2 }, { path: ".env.example", add: 1, del: 0 }, { path: "gone", add: 0, del: 4 },
  ] }) });
  assert.deepStrictEqual(m.files.map((f) => [f.path, f.text]), [
    ["docs/a.md", "+18 −0"], ["scripts/x.sh", "+9 −2"], [".env.example", "+1 −0"], ["gone", "+0 −4"],
  ]);
  assert.strictEqual(actDelta(5, 0), "+5 −0");
});

test("a session that is not Claude: credential rows only, no tool rows, nothing throws", () => {
  const m = model({
    session: sess({ claude: false }),
    resp: resp({ claude: false, events: [
      ev({ kind: "credential", tool: undefined, detail: "aws p", state: "minted" }),
      ev({ kind: "approval", tool: undefined, detail: "granted 15 min", state: "granted" }),
      ev({ tool: "Bash", detail: "stray" }),
    ] }),
  });
  assert.strictEqual(m.notClaude, true);
  assert.deepStrictEqual(m.rows.map((r) => r.kind), ["Credential", "Approval"]);
  assert.strictEqual(m.ask, false);
  assert.strictEqual(model({ session: sess({ claude: false }), resp: null }).notClaude, true);
});

test("a non-Claude session has no tiles and the M6.2 empty block", () => {
  const m = model({
    session: sess({ id: "s9", name: "audit-shell", agent: "bash", claude: false }),
    resp: resp({ claude: false, events: [], summary: { tool_calls: 0, files_changed: 0, credentials: 0, running_s: 11 * 86400 + 13 * 3600 } }),
  });
  assert.deepStrictEqual([...m.tiles], []);
  assert.deepStrictEqual({ ...m.empty }, {
    title: "Activity is for Claude sessions",
    body: "audit-shell runs bash, so there is no transcript to show. If this session uses a credential, the event still appears here.",
    meta: "Session age 11d 13h \u00b7 no credential events yet",
  });
  assert.strictEqual(m.empty.meta, "Session age 11d 13h · no credential events yet");
  assert.strictEqual(m.notClaude, true);
  assert.strictEqual(m.loaded, true);
  assert.deepStrictEqual(m.rows, []);

  // The name falls back to the id; an empty agent drops the "runs X" clause.
  const noName = model({ session: sess({ id: "s9", name: "", agent: "bash", claude: false }), resp: resp({ claude: false }) });
  assert.strictEqual(noName.empty.body, "s9 runs bash, so there is no transcript to show. If this session uses a credential, the event still appears here.");
  const noAgent = model({ session: sess({ id: "s9", name: "audit-shell", agent: "", claude: false }), resp: resp({ claude: false }) });
  assert.strictEqual(noAgent.empty.body, "audit-shell has no transcript to show. If this session uses a credential, the event still appears here.");
  const noAgentField = model({ session: sess({ id: "s9", name: "audit-shell", claude: false }), resp: resp({ claude: false }) });
  assert.strictEqual(noAgentField.empty.body, noAgent.empty.body);
});

// ncResp: a non-Claude reply whose server-side credential total is 0 unless a test says otherwise.
const ncResp = (events, over) => resp({
  claude: false, events,
  summary: Object.assign({ tool_calls: 0, files_changed: 0, credentials: 0, running_s: 41 * 60 }, over),
});
const credRow = (state) => ev({ kind: "credential", tool: undefined, detail: "aws culipulse-prod", state });
const apprRow = (state) => ev({ kind: "approval", tool: undefined, detail: "granted 15 min", state });
const NC = sess({ id: "s9", name: "audit-shell", agent: "bash", claude: false });

test("a non-Claude session: before the first reply the age reads —, and a failed read says so", () => {
  const s = sess({ id: "s9", name: "audit-shell", agent: "bash", claude: false });
  const before = model({ session: s, resp: null });
  assert.strictEqual(before.empty.meta, "Session age —");
  assert.strictEqual(before.loaded, false);
  assert.deepStrictEqual([...before.tiles], []);
  const failed = model({ session: s, resp: null, failed: true });
  assert.strictEqual(failed.empty.meta, "Session age — \u00b7 could not read credential events");
  assert.strictEqual(failed.loaded, false);
  // A reply that arrived wins over an older failure flag: the count is real data.
  const stale = model({ session: s, resp: ncResp([]), failed: true });
  assert.strictEqual(stale.empty.meta, "Session age 41m · no credential events yet");
  // No session at all: nothing to say, so no block.
  assert.strictEqual(activityModel({}).empty, null);
});

test("a non-Claude session with two credential events counts them and keeps the rows", () => {
  const events = [
    ev({ ts: at("14:32:51"), kind: "credential", tool: undefined, detail: "aws culipulse-prod", state: "minted" }),
    ev({ ts: at("14:20:00"), kind: "approval", tool: undefined, detail: "granted 15 min", state: "granted" }),
    ev({ ts: at("14:10:00"), kind: "credential", tool: undefined, detail: "aws culipulse-prod", state: "refused" }),
    ev({ ts: at("14:05:00"), tool: "Bash", detail: "stray" }),
  ];
  const s = sess({ id: "s9", name: "audit-shell", agent: "bash", claude: false });
  const m = model({ session: s, resp: resp({ claude: false, events, summary: { tool_calls: 0, files_changed: 0, credentials: 1, running_s: 90 } }) });
  assert.strictEqual(m.empty.meta, "Session age 1m \u00b7 2 credential events");
  assert.deepStrictEqual(m.rows.map((r) => r.kind), ["Credential", "Approval", "Credential"]);
  assert.deepStrictEqual([...m.tiles], []);
  // Exactly one is singular (controller ruling P2); the count ignores the filter and approvals.
  const one = model({ session: s, filter: "you", resp: ncResp([events[0], events[1]]) });
  assert.strictEqual(one.empty.meta, "Session age 41m \u00b7 1 credential event");
  const none = model({ session: s, resp: ncResp([events[1]]) });
  assert.strictEqual(none.empty.meta, "Session age 41m \u00b7 no credential events yet");
});

test("a partial reply with no credential events says the read failed, never \"no credential events yet\"", () => {
  // partial is the server's "an audit read failed" (activity_api.go): HTTP 200, so `failed` never fires.
  const partial = model({ session: NC, resp: resp({ claude: false, events: [], partial: true, summary: { credentials: 0, running_s: 41 * 60 } }) });
  assert.strictEqual(partial.empty.meta, "Session age 41m \u00b7 could not read credential events");
  // The same through the transport flag alone, and both together.
  assert.strictEqual(model({ session: NC, resp: null, failed: true }).empty.meta, "Session age \u2014 \u00b7 could not read credential events");
  assert.strictEqual(model({ session: NC, resp: ncResp([]), failed: true }).empty.meta, "Session age 41m \u00b7 no credential events yet");
  // Approval rows are not credential events: still nothing known about credentials.
  const apprOnly = model({ session: NC, resp: Object.assign(ncResp([apprRow("granted")]), { partial: true }) });
  assert.strictEqual(apprOnly.empty.meta, "Session age 41m \u00b7 could not read credential events");
  // A partial reply that did find credential events counts them: that count is real, if possibly low.
  const found = model({ session: NC, resp: Object.assign(ncResp([credRow("minted")]), { partial: true }) });
  assert.strictEqual(found.empty.meta, "Session age 41m \u00b7 1 credential event");
  const fromTotal = model({ session: NC, resp: Object.assign(ncResp([], { credentials: 3 }), { partial: true }) });
  assert.strictEqual(fromTotal.empty.meta, "Session age 41m \u00b7 3 credential events");
  // A whole reply with none is the real "none yet".
  assert.strictEqual(model({ session: NC, resp: ncResp([]) }).empty.meta, "Session age 41m \u00b7 no credential events yet");
});

test("the credential count never falls below the server's unpaged total, nor below the rows held", () => {
  // The page holds mostly approvals; the server counted 5 minted over the whole session.
  const paged = model({ session: NC, resp: Object.assign(ncResp([apprRow("granted"), apprRow("expired"), credRow("minted")], { credentials: 5 }), { more: true }) });
  assert.strictEqual(paged.empty.meta, "Session age 41m \u00b7 5 credential events");
  assert.deepStrictEqual(paged.rows.map((r) => r.kind), ["Approval", "Approval", "Credential"]);
  // The server total is 1 (minted only); refused and failed rows are credential events too.
  const rows = [credRow("minted"), credRow("refused"), credRow("failed")];
  assert.strictEqual(model({ session: NC, resp: ncResp(rows, { credentials: 1 }) }).empty.meta, "Session age 41m \u00b7 3 credential events");
  // Equal: no double counting of the same rows.
  assert.strictEqual(model({ session: NC, resp: ncResp([credRow("minted")], { credentials: 1 }) }).empty.meta, "Session age 41m \u00b7 1 credential event");
  // A total of 1 with no row held is still one event, singular.
  assert.strictEqual(model({ session: NC, resp: ncResp([], { credentials: 1 }) }).empty.meta, "Session age 41m \u00b7 1 credential event");
  // Junk in the total is no total.
  assert.strictEqual(model({ session: NC, resp: ncResp([], { credentials: "9" }) }).empty.meta, "Session age 41m \u00b7 no credential events yet");
});

test("a session age of 0 is the server's unknown and reads \u2014, never 0s", () => {
  // The server sends running_s 0 for an exited session with no events (and no creation time).
  const zero = model({ session: NC, resp: ncResp([], { running_s: 0 }) });
  assert.strictEqual(zero.empty.meta, "Session age \u2014 \u00b7 no credential events yet");
  assert.strictEqual(model({ session: NC, resp: ncResp([credRow("minted")], { running_s: 0 }) }).empty.meta, "Session age \u2014 \u00b7 1 credential event");
  assert.strictEqual(model({ session: NC, resp: Object.assign(ncResp([], { running_s: 0 }), { partial: true }) }).empty.meta, "Session age \u2014 \u00b7 could not read credential events");
  // Missing or wrong-typed is unknown too.
  for (const running_s of [undefined, null, "41", NaN, -3]) {
    assert.strictEqual(model({ session: NC, resp: ncResp([], { running_s }) }).empty.meta, "Session age \u2014 \u00b7 no credential events yet", String(running_s));
  }
  // The tile agrees with the line.
  const tile = (running_s) => model({ session: sess(), resp: resp({ summary: { tool_calls: 4, files_changed: 1, credentials: 2, running_s } }) }).tiles[4];
  assert.deepStrictEqual({ ...tile(0) }, { n: "\u2014", label: "Session age" });
  assert.strictEqual(tile(undefined).n, "\u2014");
  assert.strictEqual(tile(1).n, "1s");
  assert.strictEqual(tile(35).n, "35s");
  // A reply with no summary at all has no age either; the counts keep their existing 0.
  const bare = model({ session: sess(), resp: { claude: true, events: [], summary: null } });
  assert.strictEqual(bare.tiles[4].n, "\u2014");
});

test("the empty block names the session by name, then id, and never invents a name", () => {
  assert.ok(model({ session: sess({ id: "s9", name: "", agent: "bash", claude: false }), resp: null }).empty.body.startsWith("s9 runs bash,"));
  assert.ok(model({ session: sess({ id: "s9", name: "audit-shell", agent: "bash", claude: false }), resp: null }).empty.body.startsWith("audit-shell runs bash,"));
  assert.doesNotMatch(model({ session: sess({ id: "s9", name: "", agent: "", claude: false }), resp: null }).empty.body, /This session/);
});

test("filters map to real kinds", () => {
  const kind = (over) => filterKind(ev(over));
  assert.strictEqual(kind({ tool: "Bash" }), "commands");
  for (const t of ["Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Grep", "Glob"]) assert.strictEqual(kind({ tool: t }), "files", t);
  assert.strictEqual(kind({ tool: "WebFetch" }), "other");
  assert.strictEqual(kind({ kind: "you", tool: undefined }), "you");
  assert.strictEqual(kind({ kind: "clear", tool: undefined }), "you");
  assert.strictEqual(kind({ kind: "credential", tool: undefined }), "cloud");
  assert.strictEqual(kind({ kind: "approval", tool: undefined }), "cloud");
  // The Kind column names each real kind: the tool, Credential, Approval, You, Cleared, Asks to run.
  const events = [
    ev({ tool: "Bash" }), ev({ tool: "Read" }), ev({ tool: "Edit" }), ev({ tool: "Write" }), ev({ tool: "MultiEdit" }),
    ev({ tool: "NotebookEdit" }), ev({ tool: "Grep" }), ev({ tool: "Glob" }), ev({ tool: "WebFetch" }),
    ev({ kind: "credential", tool: undefined }), ev({ kind: "approval", tool: undefined }),
    ev({ kind: "you", tool: undefined }), ev({ kind: "clear", tool: undefined }),
  ];
  const m = model({ session: sess({ need: "ask", ask: "x" }), resp: resp({ events }) });
  assert.deepStrictEqual(m.rows.map((r) => r.kind), [
    "Asks to run", "Bash", "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Grep", "Glob", "WebFetch",
    "Credential", "Approval", "You", "Cleared",
  ]);
});

test("credential failed reads \"failed\" in red", () => {
  const m = model({ session: sess(), resp: resp({ events: [ev({ kind: "credential", tool: undefined, detail: "aws culipulse-prod", state: "failed" })] }) });
  assert.deepStrictEqual({ ...m.rows[0].outcome }, { text: "failed", tone: "err" });
});

test("partial and more are carried through; the model tolerates nothing loaded", () => {
  assert.strictEqual(model({ session: sess(), resp: resp({ partial: true }) }).partial, true);
  assert.strictEqual(model({ session: sess(), resp: resp() }).partial, false);
  assert.strictEqual(model({ session: sess(), resp: resp({ more: true }) }).more, true);
  const empty = activityModel({});
  assert.deepStrictEqual(empty.rows, []);
  assert.strictEqual(empty.notClaude, true); // no session: nothing to claim
  const noResp = model({ session: sess(), resp: null });
  assert.deepStrictEqual(noResp.rows, []);
  assert.deepStrictEqual(noResp.tiles.map((t) => t.n), ["—", "—", "—", "—", "—"]);
});

test("a server field of the wrong type never throws", () => {
  const m = model({ session: sess(), resp: { claude: true, events: [null, 7, { kind: "tool" }, { ts: "x", kind: "tool", tool: "Bash", detail: 5 }], files: [null, { path: 3 }], summary: null } });
  assert.ok(Array.isArray(m.rows));
  assert.ok(Array.isArray(m.files));
});

test("actTime: the clock today, the date and clock for an earlier day", () => {
  assert.strictEqual(actTime(at("14:33:10"), NOW), "14:33:10");
  assert.strictEqual(actTime(at("09:05:07"), NOW), "09:05:07");
  assert.strictEqual(actTime(Date.parse("2026-10-26T23:59:01"), NOW), "26 Oct 23:59");
  assert.strictEqual(actTime(NaN, NOW), "");
});

test("actContext: the context bar line", () => {
  assert.deepStrictEqual(actContext({ pct: 23, contextTokens: 230000, contextMax: 1000000 }), { pct: 23, width: "23%", text: "context 23% of 1M", label: "23% of 1M" });
  assert.strictEqual(actContext({ pct: 61, contextTokens: 122000, contextMax: 200000 }).label, "61% of 200k");
  assert.strictEqual(actContext({ pct: 12 }).label, "12%");
  assert.strictEqual(actContext({ pct: 40, contextTokens: 80000, contextMax: 200000 }).text, "context 40% of 200k");
  assert.strictEqual(actContext({ contextTokens: 250000, contextMax: 1000000 }).pct, 25);
  assert.strictEqual(actContext({ pct: 12 }).text, "context 12%");
  assert.strictEqual(actContext({ pct: 250, contextMax: 1000000 }).width, "100%");
  assert.strictEqual(actContext(null), null);
  assert.strictEqual(actContext({}), null);
});

test("actToolRows: bars are each tool's share of the result bytes", () => {
  const r = actToolRows({ tools: [
    { name: "Bash", kind: "tool", calls: 17, ctxBytes: 1640000 },
    { name: "Read", kind: "tool", calls: 9, ctxBytes: 960000 },
    { name: "mcp__x__y", kind: "mcp", calls: 2, ctxBytes: 400000 },
  ], outputTokens: 5 });
  assert.deepStrictEqual(r.rows.map((x) => [x.name, x.kind, x.calls, x.ctx, x.pct]), [
    ["Bash", "tool", 17, "≈410k", 55], ["Read", "tool", 9, "≈240k", 32], ["mcp__x__y", "mcp", 2, "≈100k", 13],
  ]);
  assert.strictEqual(r.more, 0);
  const many = actToolRows({ tools: Array.from({ length: 11 }, (_, i) => ({ name: "t" + i, calls: 1, ctxBytes: 4 })) });
  assert.strictEqual(many.rows.length, 8);
  assert.strictEqual(many.more, 3);
  assert.deepStrictEqual(actToolRows(null), { rows: [], more: 0 });
  assert.deepStrictEqual(actToolRows({ tools: [null, { name: 3, ctxBytes: "x" }] }).rows.map((x) => [x.name, x.pct]), [["?", 0]]);
});

// --- paging ---------------------------------------------------------------------

test("actOldest: the oldest ts held, across the live page and the older pages", () => {
  assert.strictEqual(actOldest([{ ts: 50 }, { ts: 40 }], [{ ts: 30 }, { ts: 20 }]), 20);
  assert.strictEqual(actOldest([{ ts: 50 }, { ts: 40 }], []), 40);
  assert.strictEqual(actOldest([], []), 0);
});

test("actSlide: events that slid off the live page are kept once older pages were loaded", () => {
  const prev = [{ ts: 50, detail: "a" }, { ts: 40, detail: "b" }, { ts: 30, detail: "c" }];
  const next = [{ ts: 60, detail: "n" }, { ts: 50, detail: "a" }, { ts: 40, detail: "b" }];
  const older = [{ ts: 20, detail: "z" }];
  assert.deepStrictEqual(actSlide(prev, next, older, true).map((e) => e.detail), ["c", "z"]);
  assert.deepStrictEqual(actSlide(prev, next, older, false), older); // not paging: nothing accumulates
  assert.deepStrictEqual(actSlide([], next, older, true), older);
  assert.deepStrictEqual(actSlide(prev, [], older, true), older);
});

test("actRowKey: stable per event, ignores the state that changes while it runs", () => {
  const a = ev({ state: "pending" });
  const b = Object.assign({}, a, { state: "ok", ms: 5 });
  assert.strictEqual(actRowKey(a), actRowKey(b));
  assert.notStrictEqual(actRowKey(a), actRowKey(ev({ detail: "other" })));
});

// --- the load path is total -------------------------------------------------------

test("actReadReply: a 200 with events is its JSON; anything else is undefined; never throws", async () => {
  const good = { claude: true, events: [] };
  assert.deepStrictEqual(await actReadReply({ status: 200, json: async () => good }), good);
  assert.strictEqual(await actReadReply({ status: 403, json: async () => good }), undefined);
  assert.strictEqual(await actReadReply({ status: 500, json: async () => good }), undefined);
  assert.strictEqual(await actReadReply({ status: 204, json: async () => null }), undefined);
  assert.strictEqual(await actReadReply({ status: 200, json: async () => { throw new SyntaxError("x"); } }), undefined);
  assert.strictEqual(await actReadReply({ status: 200, json: async () => null }), undefined);
  assert.strictEqual(await actReadReply({ status: 200, json: async () => ({ events: "no" }) }), undefined);
  assert.strictEqual(await actReadReply(null), undefined);
});

test("actLoadDone: a failed read keeps what was there and always ends the read", () => {
  const prev = { resp: { events: [1] }, loaded: true, failed: false, inflight: true };
  const bad = actLoadDone(prev, undefined, 1000);
  assert.strictEqual(bad.inflight, false);
  assert.strictEqual(bad.failed, true);
  assert.deepStrictEqual(bad.resp, { events: [1] });
  assert.strictEqual(bad.loaded, true);
  const good = actLoadDone(prev, { events: [2] }, 2000);
  assert.deepStrictEqual(good, { resp: { events: [2] }, at: 2000, inflight: false, loaded: true, failed: false });
  const first = actLoadDone(null, undefined, 3000);
  assert.deepStrictEqual(first, { resp: null, at: 3000, inflight: false, loaded: false, failed: true });
});

// --- hygiene ----------------------------------------------------------------------

test("activitytab.js writes server text with textContent only", () => {
  const src = fs.readFileSync(path.join(__dirname, "../static/activitytab.js"), "utf8");
  assert.doesNotMatch(src.replace(/\/\/.*$/gm, ""), /innerHTML|outerHTML|insertAdjacentHTML|document\.write/);
});

test("activitymodel.js has no DOM or network access", () => {
  const src = fs.readFileSync(path.join(__dirname, "../static/activitymodel.js"), "utf8").replace(/\/\/.*$/gm, "");
  assert.doesNotMatch(src, /document\.|window\.|fetch\(|innerHTML/);
});
