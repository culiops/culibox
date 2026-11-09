"use strict";
// The notification rule table (#177, DC§1, DD§A4): which OS notification a poll produces, as pure data.
// Fixtures use made-up names only.
const test = require("node:test");
const assert = require("node:assert");
const m = require("../static/notifymodel.js");
const { NEEDS_REARM_MS } = require("../static/attention.js");

const ON = { master: true, block: true, done: true, ended: true, message: false };
const T0 = 1000000;
function sess(id, over) { return Object.assign({ id, name: "n-" + id, activity: "working" }, over || {}); }
function ask(id, a) { return sess(id, { activity: "waiting", need: "ask", ask: a === undefined ? "Bash(rm -rf build)" : a, attention: "block" }); }
function doneS(id, summary) { return sess(id, { activity: "waiting", need: "turn", summary: summary || "all good", attention: "done" }); }
function ctx(over) { return Object.assign({ now: T0, focused: false, prefs: ON, endedByMe: new Set() }, over || {}); }
// seed on `before`, then step on `after`
function run(before, after, c, c0) {
  const seed = m.notifyStep(null, before, ctx(c0));
  assert.deepEqual(seed.notes, []);
  return m.notifyStep(seed.next, after, ctx(c));
}

test("ask: tag, renotify, requireInteraction, tool-only body, title = name", () => {
  const { notes } = run([sess("a")], [ask("a")]);
  assert.equal(notes.length, 1);
  const n = notes[0];
  assert.equal(n.title, "n-a");
  assert.equal(n.body, "Wants to run Bash");
  assert.equal(n.tag, "ab:a:block");
  assert.equal(n.sid, "a");
  assert.equal(n.options.tag, "ab:a:block");
  assert.equal(n.options.renotify, true);
  assert.equal(n.options.requireInteraction, true);
  assert.equal(n.options.silent, false);
});

test("first step only seeds, even for sessions already blocked", () => {
  const r = m.notifyStep(null, [ask("a"), doneS("b")], ctx());
  assert.deepEqual(r.notes, []);
  assert.ok(r.next.block.size === 1 && r.next.done.has("b"));
  // and the next identical step is quiet
  assert.deepEqual(m.notifyStep(r.next, [ask("a"), doneS("b")], ctx()).notes, []);
});

test("SSO with one session: title = name, tag per source", () => {
  const s = sess("a", { activity: "waiting", need: "sso", attention: "block", aws_connections: [{ sso_expired: "prof-x" }] });
  const { notes } = run([sess("a")], [s]);
  assert.equal(notes.length, 1);
  assert.equal(notes[0].title, "n-a");
  assert.equal(notes[0].body, "AWS sign-in expired");
  assert.equal(notes[0].tag, "ab:sso:prof-x");
  assert.equal(notes[0].options.renotify, true);
  assert.equal(notes[0].options.requireInteraction, true);
});

test("SSO with two sessions on one source: one CuliBox note with the count", () => {
  const mk = (id) => sess(id, { activity: "waiting", need: "sso", attention: "block", aws_connections: [{ sso_expired: "prof-x" }] });
  const { notes } = run([sess("a"), sess("b")], [mk("a"), mk("b")]);
  assert.equal(notes.length, 1);
  assert.equal(notes[0].title, "CuliBox");
  assert.equal(notes[0].body, "AWS sign-in expired · 2 sessions");
  assert.equal(notes[0].tag, "ab:sso:prof-x");
});

test("SSO with two sources: two notes", () => {
  const mk = (id, src) => sess(id, { activity: "waiting", need: "sso", attention: "block", aws_connections: [{ sso_expired: src }] });
  const { notes } = run([sess("a"), sess("b")], [mk("a", "p1"), mk("b", "p2")]);
  assert.deepEqual(notes.map((n) => n.tag).sort(), ["ab:sso:p1", "ab:sso:p2"]);
  assert.ok(notes.every((n) => n.title !== "CuliBox"));
});

test("legacy aws.sso_expired key is a source too", () => {
  const s = sess("a", { activity: "waiting", need: "sso", attention: "block", aws: { sso_expired: "legacy" } });
  const { notes } = run([sess("a")], [s]);
  assert.equal(notes[0].tag, "ab:sso:legacy");
});

test("store locked with 3 sessions: one note", () => {
  const mk = (id) => sess(id, { activity: "waiting", need: "store_locked", attention: "block" });
  const { notes } = run([sess("a"), sess("b"), sess("c")], [mk("a"), mk("b"), mk("c")]);
  assert.equal(notes.length, 1);
  assert.equal(notes[0].tag, "ab:store");
  assert.equal(notes[0].title, "CuliBox");
  assert.equal(notes[0].body, "Secret store locked: 3 sessions can't fetch credentials");
  assert.equal(notes[0].options.requireInteraction, true);
});

test("store locked with 1 session reads in the singular", () => {
  const s = sess("a", { activity: "waiting", need: "store_locked", attention: "block" });
  assert.equal(run([sess("a")], [s]).notes[0].body, "Secret store locked: 1 session can't fetch credentials");
});

test("store note is not repeated when more sessions join", () => {
  const mk = (id) => sess(id, { activity: "waiting", need: "store_locked", attention: "block" });
  const first = run([sess("a"), sess("b")], [mk("a")]);
  const again = m.notifyStep(first.next, [mk("a"), mk("b")], ctx());
  assert.deepEqual(again.notes, []);
});

test("Done x1: silent, no renotify, no requireInteraction, generic body", () => {
  const { notes } = run([sess("a")], [doneS("a", "secret summary")]);
  assert.equal(notes.length, 1);
  const n = notes[0];
  assert.equal(n.tag, "ab:a:done");
  assert.equal(n.title, "n-a");
  assert.equal(n.body, "Finished its turn");
  assert.equal(n.options.silent, true);
  assert.ok(!("renotify" in n.options) && !("requireInteraction" in n.options));
});

test("Done x3 in one step: one batch note", () => {
  const { notes } = run([sess("a"), sess("b"), sess("c")], [doneS("a"), doneS("b"), doneS("c")]);
  assert.equal(notes.length, 1);
  assert.equal(notes[0].tag, "ab:done-batch");
  assert.equal(notes[0].title, "CuliBox");
  assert.equal(notes[0].body, "3 sessions finished");
  assert.equal(notes[0].options.silent, true);
  assert.ok(!("renotify" in notes[0].options) && !("requireInteraction" in notes[0].options));
});

test("Done x2 in one step: two separate notes, no batch (R1)", () => {
  const { notes } = run([sess("a"), sess("b")], [doneS("a"), doneS("b")]);
  assert.deepEqual(notes.map((n) => n.tag), ["ab:a:done", "ab:b:done"]);
});

test("Ended crashed", () => {
  const { notes } = run([sess("a", { status: "live" })], [sess("a", { exited: true, status: "exited", activity: "crashed" })]);
  assert.equal(notes.length, 1);
  assert.equal(notes[0].body, "Ended (crashed)");
  assert.equal(notes[0].tag, "ab:a:end");
  assert.equal(notes[0].title, "n-a");
});

test("Ended: tag and options are a plain notification (R1)", () => {
  const { notes } = run([sess("a")], [sess("a", { exited: true, status: "exited", activity: "done" })]);
  assert.equal(notes[0].body, "Ended");
  assert.deepEqual(notes[0].options, { tag: "ab:a:end", silent: false });
});

test("Ended: exited without an End from this client", () => {
  const { notes } = run([sess("a")], [sess("a", { exited: true, status: "exited", activity: "done" })]);
  assert.equal(notes[0].body, "Ended");
});

test("Ended: gone from the list without an End from this client", () => {
  const { notes } = run([sess("a")], []);
  assert.equal(notes.length, 1);
  assert.equal(notes[0].body, "Ended");
  assert.equal(notes[0].title, "n-a");
});

test("Ended: exited WITH an End from this client gives none; gone too", () => {
  const e = new Set(["a"]);
  assert.deepEqual(run([sess("a")], [sess("a", { exited: true, status: "exited" })], { endedByMe: e }).notes, []);
  assert.deepEqual(run([sess("a")], [], { endedByMe: e }).notes, []);
});

test("Ended is announced once: an exited session that stays listed, then goes", () => {
  const ex = sess("a", { exited: true, status: "exited" });
  const r1 = run([sess("a")], [ex]);
  assert.equal(r1.notes.length, 1);
  assert.deepEqual(m.notifyStep(r1.next, [ex], ctx()).notes, []);
  assert.deepEqual(m.notifyStep(r1.next, [], ctx()).notes, []);
});

test("no note for write expiry, offline, or quota >= 100", () => {
  const s = sess("a", { aws_connections: [{ elevated_until: "2099-01-01T00:00:00Z" }], rate_limits: { five_hour: { used_percentage: 100 } } });
  const s2 = sess("a", { aws_connections: [{ elevated_until: "2000-01-01T00:00:00Z" }], rate_limits: { five_hour: { used_percentage: 100 } }, activity: "detached", status: "detached" });
  assert.deepEqual(run([sess("a")], [s]).notes, []);
  assert.deepEqual(run([s], [s2]).notes, []);
});

test("focused: no notes, but the state still advances", () => {
  const seed = m.notifyStep(null, [sess("a")], ctx());
  const r = m.notifyStep(seed.next, [ask("a")], ctx({ focused: true }));
  assert.deepEqual(r.notes, []);
  assert.ok(r.next.block.has("ab:a:block"));
  // when focus is lost the same ask is not announced late
  assert.deepEqual(m.notifyStep(r.next, [ask("a")], ctx()).notes, []);
});

test("pty re-arm: block -> idle -> block within 15 s gives no second note, a later one does", () => {
  const pty = (over) => sess("a", Object.assign({ activity_source: "pty" }, over));
  const blocked = pty({ activity: "waiting", need: "ask", ask: "Bash(x)", attention: "block" });
  const idle = pty({});
  let r = run([idle], [blocked], { now: T0 });
  assert.equal(r.notes.length, 1);
  r = m.notifyStep(r.next, [idle], ctx({ now: T0 + 2000 }));
  r = m.notifyStep(r.next, [blocked], ctx({ now: T0 + 5000 }));
  assert.deepEqual(r.notes, []);
  r = m.notifyStep(r.next, [idle], ctx({ now: T0 + 6000 }));
  r = m.notifyStep(r.next, [blocked], ctx({ now: T0 + 6000 + NEEDS_REARM_MS + 1 }));
  assert.equal(r.notes.length, 1);
});

test("a non-pty source re-notifies immediately (the agent's own status has no blips)", () => {
  const blocked = ask("a");
  let r = run([sess("a")], [blocked], { now: T0 });
  r = m.notifyStep(r.next, [sess("a")], ctx({ now: T0 + 1000 }));
  r = m.notifyStep(r.next, [blocked], ctx({ now: T0 + 2000 }));
  assert.equal(r.notes.length, 1);
});

test("prefs: Done off, Blocked off, Ended off, master off", () => {
  const off = (k) => Object.assign({}, ON, { [k]: false });
  const mix = () => [ask("a"), doneS("b"), sess("c", { exited: true, status: "exited" })];
  const before = [sess("a"), sess("b"), sess("c")];
  const tags = (p) => run(before, mix(), { prefs: p }).notes.map((n) => n.tag).sort();
  assert.deepEqual(tags(ON), ["ab:a:block", "ab:b:done", "ab:c:end"]);
  assert.deepEqual(tags(off("done")), ["ab:a:block", "ab:c:end"]);
  assert.deepEqual(tags(off("block")), ["ab:b:done", "ab:c:end"]);
  assert.deepEqual(tags(off("ended")), ["ab:a:block", "ab:b:done"]);
  assert.deepEqual(tags(off("master")), []);
});

test("a switched-off class still advances state (no late burst when turned on)", () => {
  const p = Object.assign({}, ON, { block: false });
  const r = run([sess("a")], [ask("a")], { prefs: p });
  assert.deepEqual(r.notes, []);
  assert.deepEqual(m.notifyStep(r.next, [ask("a")], ctx()).notes, []);
});

test("message text: off keeps Done generic, on uses plainSummary; ask unchanged", () => {
  const sum = "Added **hints** for lab 3";
  const d = doneS("a", sum);
  const a = ask("b");
  const on = Object.assign({}, ON, { message: true });
  const off = run([sess("a"), sess("b")], [d, a]).notes;
  const withMsg = run([sess("a"), sess("b")], [d, a], { prefs: on }).notes;
  assert.equal(off.find((n) => n.tag === "ab:a:done").body, "Finished its turn");
  assert.equal(withMsg.find((n) => n.tag === "ab:a:done").body, "Added hints for lab 3");
  assert.equal(withMsg.find((n) => n.tag === "ab:b:block").body, "Wants to run Bash");
});

test("message on with an empty summary falls back to the generic Done body", () => {
  const on = Object.assign({}, ON, { message: true });
  const d = doneS("a", "");
  d.summary = "";
  assert.equal(run([sess("a")], [d], { prefs: on }).notes[0].body, "Finished its turn");
});

test("a blocked session with no ask and no cause field gets a generic body, never the summary", () => {
  const s = sess("a", { activity: "waiting", attention: "block", summary: "do not leak" });
  const { notes } = run([sess("a")], [s]);
  assert.equal(notes[0].body, "Waiting for you");
  assert.equal(notes[0].tag, "ab:a:block");
});

test("askTool", () => {
  assert.equal(m.askTool("Bash(rm -rf /)"), "Bash");
  assert.equal(m.askTool("  mcp__x__y (arg)"), "mcp__x__y");
  assert.equal(m.askTool("rm -rf /etc/shadow (x)"), "a tool");
  assert.equal(m.askTool("/usr/bin/curl(x)"), "a tool");
  assert.equal(m.askTool("Bash (rm)"), "Bash");
  assert.equal(m.askTool("WebFetch"), "WebFetch");
  assert.equal(m.askTool("Read some/path"), "Read");
  assert.equal(m.askTool("/etc/passwd please"), "a tool");
  assert.equal(m.askTool(""), "a tool");
  assert.equal(m.askTool(undefined), "a tool");
  assert.equal(m.askTool("(only args)"), "a tool");
  assert.equal(m.askTool("x".repeat(100) + "(y)").length, 40);
});

test("property: 200 generated asks never leak a command, a path, arguments or the summary", () => {
  // mulberry32: exact 32-bit arithmetic.
  let st = 0x9e3779b9;
  const rnd = (n) => {
    st = (st + 0x6d2b79f5) | 0;
    let x = Math.imul(st ^ (st >>> 15), 1 | st);
    x = (x + Math.imul(x ^ (x >>> 7), 61 | x)) ^ x;
    return (((x ^ (x >>> 14)) >>> 0) % n);
  };
  const SUMMARY = "SUMMARYTEXT-do-not-show";
  const on = Object.assign({}, ON, { message: true });
  const heads = ["Bash", "Read", "mcp__a__b", "rm -rf", "/etc/CMDPATH", "curl CMDWORD", "x.y-z", "", "/CMDPREFIX do"];
  const seen = { spaced: 0, unspaced: 0, paren: 0, noParen: 0, empty: 0 };
  for (let i = 0; i < 200; i++) {
    const head = heads[rnd(heads.length)];
    const sep = rnd(2) ? " " : "";
    const withParen = rnd(2) === 1;
    const a = withParen ? head + sep + "(ARGMARK " + "/ARGPATH)" : head + (head && rnd(2) ? " ARGMARK /ARGPATH" : "");
    seen[a.indexOf(" ") >= 0 ? "spaced" : "unspaced"]++;
    seen[withParen ? "paren" : "noParen"]++;
    if (a === "") seen.empty++;
    const s = ask("a", a);
    s.summary = SUMMARY;
    for (const p of [ON, on]) {
      const body = run([sess("a")], [s], { prefs: p }).notes[0].body;
      assert.match(body, /^Wants to run (a tool|[A-Za-z][\w.-]*)$/, JSON.stringify([a, body]));
      for (const bad of ["ARGMARK", "ARGPATH", "CMDPATH", "CMDWORD", "CMDPREFIX", SUMMARY, "(", "/", "-rf"]) {
        assert.ok(!body.includes(bad), JSON.stringify([a, body, bad]));
      }
    }
  }
  assert.ok(seen.spaced > 20 && seen.unspaced > 20 && seen.paren > 20 && seen.noParen > 20, JSON.stringify(seen));
  assert.equal(m.notifyStep(null, [ask("a", "")], ctx()).notes.length, 0);
  assert.equal(run([sess("a")], [ask("a", "")]).notes[0].body, "Wants to run a tool");
});

test("a failed fetch (no list) changes nothing and ends nothing (R8a)", () => {
  const seed = m.notifyStep(null, [sess("a"), ask("b")], ctx());
  for (const bad of [undefined, null, {}]) {
    const r = m.notifyStep(seed.next, bad, ctx());
    assert.equal(r.next, seed.next);
    assert.deepEqual(r.notes, []);
  }
  assert.equal(m.notifyStep(null, undefined, ctx()).next, null);
});

test("re-arm entries older than the window are pruned each step (R8b)", () => {
  const blocked = ask("a");
  let r = run([blocked], [sess("a")], { now: T0 }, { now: T0 });
  assert.ok(r.next.away.has("ab:a:block"));
  r = m.notifyStep(r.next, [sess("a")], ctx({ now: T0 + NEEDS_REARM_MS - 1 }));
  assert.ok(r.next.away.has("ab:a:block"));
  r = m.notifyStep(r.next, [sess("a")], ctx({ now: T0 + NEEDS_REARM_MS }));
  assert.equal(r.next.away.size, 0);
});

test("the summary appears only for Done with the message setting on", () => {
  const on = Object.assign({}, ON, { message: true });
  const s1 = ask("a"); s1.summary = "SUMTEXT";
  const s2 = sess("b", { activity: "waiting", need: "store_locked", attention: "block", summary: "SUMTEXT" });
  const s3 = sess("c", { exited: true, status: "exited", summary: "SUMTEXT" });
  const d = doneS("d", "SUMTEXT");
  const { notes } = run([sess("a"), sess("b"), sess("c"), sess("d")], [s1, s2, s3, d], { prefs: on });
  assert.equal(notes.length, 4);
  for (const n of notes) assert.equal(n.body.includes("SUMTEXT"), n.tag === "ab:d:done");
});

test("causeKeys: ask, sso by source, store, done, generic", () => {
  assert.deepEqual(m.causeKeys(ask("a")), ["ab:a:block"]);
  assert.deepEqual(m.causeKeys(doneS("a")), ["ab:a:done"]);
  assert.deepEqual(m.causeKeys(sess("a", { activity: "waiting", need: "store_locked", attention: "block" })), ["ab:store"]);
  assert.deepEqual(m.causeKeys(sess("a", { attention: "block", aws_connections: [{ sso_expired: "p2" }, { sso_expired: "p1" }] })), ["ab:sso:p1", "ab:sso:p2"]);
  assert.deepEqual(m.causeKeys(sess("a")), []);
  assert.deepEqual(m.causeKeys(sess("a", { exited: true, need: "ask" })), []);
});

test("notifyClassOf", () => {
  assert.equal(m.notifyClassOf(ask("a")), "block");
  assert.equal(m.notifyClassOf(doneS("a")), "done");
  assert.equal(m.notifyClassOf(sess("a")), "");
  assert.equal(m.notifyClassOf(null), "");
});

test("missing prefs default to the shipped defaults (master, block, done, ended on; message off)", () => {
  const seed = m.notifyStep(null, [sess("a")], { now: T0, focused: false });
  const r = m.notifyStep(seed.next, [doneS("a", "S")], { now: T0, focused: false });
  assert.equal(r.notes[0].body, "Finished its turn");
});

// ---- Task 2: titleState / faviconSvg (DD§F, DC§2) ----
const SYS_OK = { lastOkAt: T0, failSince: null, signedOut: false };
const iso = (ms) => new Date(ms).toISOString();
function conn(name, over) { return Object.assign({ name }, over || {}); }

test("titleState: nothing blocked or done gives the plain title and the normal icon", () => {
  assert.deepEqual(m.titleState([sess("a"), sess("b")], SYS_OK, "", T0), { title: "CuliBox", icon: "normal" });
  assert.deepEqual(m.titleState([], SYS_OK, "PROD", T0), { title: "[PROD] CuliBox", icon: "normal" });
  assert.deepEqual(m.titleState(null, null, "DEV", T0), { title: "[DEV] CuliBox", icon: "normal" });
});

test("titleState: (N) counts blocked causes, the store once however many sessions wait on it", () => {
  const st = (id) => sess(id, { activity: "waiting", need: "store_locked", attention: "block" });
  const r = m.titleState([ask("a"), st("b"), st("c"), st("d")], SYS_OK, "DEV", T0);
  assert.deepEqual(r, { title: "(2) [DEV] CuliBox", icon: "amber" });
});

test("titleState: two sessions on one SSO source are one cause; two sources are two", () => {
  const sso = (id, src) => sess(id, { activity: "waiting", attention: "block", aws_connections: [conn("c", { sso_expired: src })] });
  assert.equal(m.titleState([sso("a", "culipulse-prod"), sso("b", "culipulse-prod")], SYS_OK, "", T0).title, "(1) CuliBox");
  assert.equal(m.titleState([sso("a", "culipulse-prod"), sso("b", "gap-sh2")], SYS_OK, "", T0).title, "(2) CuliBox");
});

test("titleState: a finished turn gives a check mark and the neutral icon, not (N)", () => {
  const r = m.titleState([doneS("a"), doneS("b"), doneS("c"), sess("d")], SYS_OK, "", T0);
  assert.deepEqual(r, { title: "✓3 CuliBox", icon: "neutral" });
});

test("titleState: a blocked cause outranks done and done sessions are not added to (N)", () => {
  const r = m.titleState([ask("a"), doneS("b"), doneS("c")], SYS_OK, "", T0);
  assert.deepEqual(r, { title: "(1) CuliBox", icon: "amber" });
});

test("titleState: exited sessions with a leftover need are not counted", () => {
  const gone = [
    sess("a", { status: "exited", need: "sso", activity: "waiting" }),
    sess("b", { exited: true, need: "store_locked", activity: "waiting" }),
    sess("c", { status: "exited", need: "ask", ask: "Bash(x)", activity: "waiting", attention: "block" }),
    sess("d", { status: "exited", need: "turn", activity: "waiting", attention: "done" }),
  ];
  assert.deepEqual(m.titleState(gone, SYS_OK, "", T0), { title: "CuliBox", icon: "normal" });
});

test("titleState: signed out is Offline at once and the stale count is dropped", () => {
  const r = m.titleState([ask("a")], { lastOkAt: T0 - 1, failSince: T0, signedOut: true }, "PROD", T0);
  assert.deepEqual(r, { title: "Offline · [PROD] CuliBox", icon: "offline" });
});

test("titleState: a network failure is Offline only after 12 s without an OK poll", () => {
  const sys = (failAt) => ({ lastOkAt: T0, failSince: failAt, signedOut: false });
  assert.equal(m.SYS_OFFLINE_MS, 12000);
  assert.equal(m.titleState([ask("a")], sys(T0 + 3000), "", T0 + 9000).title, "(1) CuliBox", "one blip never flickers");
  assert.equal(m.titleState([ask("a")], sys(T0 + 3000), "", T0 + 11999).title, "(1) CuliBox");
  assert.deepEqual(m.titleState([ask("a")], sys(T0 + 3000), "", T0 + 12000), { title: "Offline · CuliBox", icon: "offline" });
  // never had an OK poll: counted from the first failure
  const never = { lastOkAt: null, failSince: T0, signedOut: false };
  assert.equal(m.titleState([], never, "", T0 + 11000).title, "CuliBox");
  assert.equal(m.titleState([], never, "", T0 + 12000).title, "Offline · CuliBox");
});

test("titleState: the write ring follows any open write window, on every dot", () => {
  const live = [conn("c", { elevated_until: iso(T0 + 600000) })];
  const lapsed = [conn("c", { elevated_until: iso(T0 - 1) })];
  const w = (over) => sess("w", Object.assign({ aws_connections: live }, over));
  assert.equal(m.titleState([w(), ask("a")], SYS_OK, "", T0).icon, "amber-ring");
  assert.equal(m.titleState([w(), doneS("b")], SYS_OK, "", T0).icon, "neutral-ring");
  assert.equal(m.titleState([w()], SYS_OK, "PROD", T0).icon, "normal-ring");
  assert.equal(m.titleState([sess("w", { aws_connections: lapsed }), ask("a")], SYS_OK, "", T0).icon, "amber");
  assert.equal(m.titleState([w()], SYS_OK, "", T0).title, "CuliBox", "the ring adds nothing to the title");
  const out = { lastOkAt: T0 - 5000, failSince: T0 - 4000, signedOut: true };
  assert.deepEqual(m.titleState([w(), ask("a")], out, "PROD", T0), { title: "Offline · [PROD] CuliBox", icon: "offline-ring" }, "R9: ring round the hollow dot while the last-known window is open");
  assert.equal(m.titleState([w(), ask("a")], out, "", T0 + 600000).icon, "offline", "no ring after the deadline");
  assert.equal(m.titleState([ask("a")], out, "", T0).icon, "offline");
});

test("faviconSvg: a data: URI per state, kit hex, DD§F2 geometry", () => {
  const dec = (k) => {
    const u = m.faviconSvg(k);
    assert.ok(u.startsWith("data:image/svg+xml,"), k);
    return decodeURIComponent(u.slice("data:image/svg+xml,".length));
  };
  for (const k of ["normal", "amber", "neutral", "offline", "offline-ring", "normal-ring", "amber-ring", "neutral-ring"]) {
    const svg = dec(k);
    assert.match(svg, /^<svg xmlns="http:\/\/www\.w3\.org\/2000\/svg" width="16" height="16" viewBox="0 0 16 16">/, k);
    assert.ok(svg.includes('<rect width="16" height="16" rx="3.5" style="fill:#0d0f12;stroke:#23282f"/>'), k);
    assert.doesNotMatch(svg, /var\(--/, "a standalone SVG cannot read page variables");
    assert.doesNotMatch(svg, /<script|on\w+=/i);
  }
  const n = dec("normal");
  assert.ok(n.includes('d="M11 5V3a2 2 0 0 0-2-2H3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2V7"'));
  assert.ok(n.includes('<rect x="4" y="4" width="4" height="4" rx="2" style="fill:#4ccf8a"/>'));
  assert.ok(n.includes("stroke:#6ea8fe"));
  const badge = 'd="M11 3.5V3a2 2 0 0 0-2-2H3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2V8.5"';
  assert.ok(dec("amber").includes(badge));
  assert.ok(dec("amber").includes('<rect x="9" y="3.5" width="5" height="5" rx="2.5" style="fill:#f5b94a"/>'));
  assert.ok(dec("neutral").includes('<rect x="9" y="3.5" width="5" height="5" rx="2.5" style="fill:#8b939e"/>'));
  const off = dec("offline");
  assert.ok(off.includes('<circle cx="11.5" cy="6" r="2" style="fill:none;stroke:#8b939e;stroke-width:1.4"/>'));
  assert.ok(off.includes("stroke:#8b939e") && !off.includes("#6ea8fe"), "the frame goes muted offline");
  const ring = '<circle cx="11.5" cy="6" r="3.9" style="fill:none;stroke:#f06a6a;stroke-width:1.3"/>';
  assert.ok(dec("amber-ring").includes(ring));
  const offRing = dec("offline-ring");
  assert.ok(offRing.includes('<circle cx="11.5" cy="6" r="2" style="fill:none;stroke:#8b939e;stroke-width:1.4"/>' + ring), "ring last, concentric with the hollow dot");
  assert.ok(dec("neutral-ring").includes(ring));
  assert.ok(dec("amber-ring").indexOf(ring) > dec("amber-ring").indexOf("#f5b94a"), "the ring is drawn last");
  // the ring around the normal dot is centred on that dot (8,8 absolute = 6,6 in the group)
  assert.ok(dec("normal-ring").includes('<circle cx="6" cy="6" r="3.9" style="fill:none;stroke:#f06a6a;stroke-width:1.3"/>'));
  assert.ok(!dec("amber").includes("#f06a6a"));
  assert.equal(m.faviconSvg("nope"), m.faviconSvg("normal"), "an unknown key falls back to normal");
});

// ---- Task 5: blockedCauses / chipModel / chimeStep (DD§A6, §B3; DC§5) ----
function ssoS(id, src, name) { return sess(id, { name: name || "n-" + id, activity: "waiting", need: "sso", attention: "block", aws_connections: [{ sso_expired: src }] }); }
function storeS(id) { return sess(id, { activity: "waiting", need: "store_locked", attention: "block" }); }
const none = new Set();

test("blockedCauses: one per cause in the order given, no done, no exited, shared causes once", () => {
  const list = [doneS("d"), ask("a"), sess("x", { exited: true, activity: "waiting", need: "ask", ask: "Bash(x)", attention: "block" }), ssoS("s1", "p"), ssoS("s2", "p"), storeS("t1"), storeS("t2")];
  const cs = m.blockedCauses(list);
  assert.deepEqual(cs.map((c) => c.key), ["ab:a:block", "ab:sso:p", "ab:store"]);
  assert.deepEqual(cs.map((c) => c.kind), ["ask", "sso", "store"]);
  assert.deepEqual(cs[1].sessions.map((s) => s.id), ["s1", "s2"]);
  assert.deepEqual(m.blockedCauses(null), []);
});

test("blockedCauses and titleState count the same causes", () => {
  const list = [ask("a"), ssoS("s1", "p"), ssoS("s2", "p"), storeS("t1"), storeS("t2"), doneS("d")];
  const n = m.blockedCauses(list).length;
  assert.equal(m.titleState(list, null, "", T0).title, "(" + n + ") CuliBox");
});

test("chipModel: a blocked ask that is on no visible pane gives the chip text", () => {
  const c = m.chipModel([ask("a", "Bash(rm -rf /tmp/x)")], none, "");
  assert.equal(c.name, "n-a");
  assert.equal(c.rest, " wants to run Bash");
  assert.equal(c.text, "n-a wants to run Bash");
  assert.equal(c.more, 0);
  assert.equal(c.sid, "a");
  assert.ok(!/rm|\/tmp/.test(c.text), "never the command");
});

test("chipModel: the session on a visible pane gives none; a Set or an array both work", () => {
  assert.equal(m.chipModel([ask("a")], new Set(["a"]), ""), null);
  assert.equal(m.chipModel([ask("a")], ["a"], ""), null);
});

test("chipModel: Done never gets a chip", () => {
  assert.equal(m.chipModel([doneS("d")], none, ""), null);
  assert.equal(m.chipModel([], none, ""), null);
  assert.equal(m.chipModel(null, none, ""), null);
});

test("chipModel: SSO and store copy, singular and plural", () => {
  assert.equal(m.chipModel([ssoS("s1", "p", "gap-sh2")], none, "").text, "gap-sh2 needs AWS sign-in");
  assert.equal(m.chipModel([ssoS("s1", "p"), ssoS("s2", "p")], none, "").text, "AWS sign-in expired · 2 sessions");
  assert.equal(m.chipModel([storeS("t1"), storeS("t2")], none, "").text, "Secret store locked · 2 sessions");
  assert.equal(m.chipModel([storeS("t1")], none, "").text, "Secret store locked · 1 session");
});

test("chipModel: a shared cause is off-screen only when none of its sessions is visible", () => {
  assert.equal(m.chipModel([storeS("t1"), storeS("t2")], new Set(["t2"]), ""), null);
  assert.equal(m.chipModel([ssoS("s1", "p"), ssoS("s2", "p")], new Set(["s1"]), ""), null);
  assert.ok(m.chipModel([storeS("t1"), storeS("t2")], new Set(["zz"]), ""));
});

test("chipModel: names the first off-screen cause in the given order and counts the rest as +N", () => {
  const list = [ask("v"), storeS("t1"), ask("a"), ssoS("s1", "p")];
  const c = m.chipModel(list, new Set(["v"]), "");
  assert.equal(c.name, "", "the store is first off-screen: a shared cause has no session name");
  assert.equal(c.text, "Secret store locked · 1 session · +2");
  assert.equal(c.more, 2);
  const c2 = m.chipModel([ask("b"), ask("a")], none, "");
  assert.equal(c2.text, "n-b wants to run Bash · +1");
  assert.equal(c2.rest, " wants to run Bash · +1");
});

test("chipModel: a visible blocked session is not counted in +N", () => {
  const c = m.chipModel([ask("a"), ask("v"), ask("b")], new Set(["v"]), "");
  assert.equal(c.more, 1);
});

test("chipModel: dismissed key = the sorted blocked cause keys; hides until that set changes", () => {
  const list = [ask("b"), ask("a")];
  const c = m.chipModel(list, none, "");
  assert.equal(c.dismissKey, "ab:a:block|ab:b:block");
  assert.equal(m.chipModel(list, none, c.dismissKey), null);
  assert.equal(m.chipModel(list.slice().reverse(), none, c.dismissKey), null, "order does not matter");
  assert.ok(m.chipModel([ask("a"), ask("b"), ask("c")], none, c.dismissKey), "a new cause shows it again");
  assert.ok(m.chipModel([ask("a")], none, c.dismissKey), "so does a changed set");
});

test("chipModel: key changes with the text, so an unchanged chip is not repainted", () => {
  const a = m.chipModel([ask("a")], none, ""), a2 = m.chipModel([ask("a")], none, "");
  assert.equal(a.key, a2.key);
  assert.notEqual(a.key, m.chipModel([ask("a"), ask("b")], none, "").key);
});

test("chipModel: a generic wait (no ask text) still has a chip, and an odd ask never leaks", () => {
  const w = sess("w", { name: "gap-sh2", activity: "waiting", need: "input", attention: "block" });
  assert.equal(m.chipModel([w], none, "").text, "gap-sh2 is waiting for you");
  assert.equal(m.chipModel([ask("a", "rm -rf /etc/shadow (x)")], none, "").text, "n-a wants to run a tool");
});

// chimeStep: the pure decision behind the blocked blip
const chOn = (over) => Object.assign({ now: T0, focused: true, on: true }, over || {});
test("chimeStep: first call only seeds", () => {
  const r = m.chimeStep(null, m.blockedCauses([ask("a")]), none, chOn());
  assert.equal(r.play, false);
  assert.deepEqual([...r.next.keys], ["ab:a:block"]);
});

test("chimeStep: plays once per new cause, never again while it stays", () => {
  const seed = m.chimeStep(null, [], none, chOn()).next;
  const r1 = m.chimeStep(seed, m.blockedCauses([ask("a")]), none, chOn());
  assert.equal(r1.play, true);
  const r2 = m.chimeStep(r1.next, m.blockedCauses([ask("a")]), none, chOn());
  assert.equal(r2.play, false);
  const r3 = m.chimeStep(r2.next, m.blockedCauses([ask("a"), ask("b")]), none, chOn());
  assert.equal(r3.play, true, "a second new cause");
});

test("chimeStep: silent when the pref is off, and turning it on later does not replay", () => {
  const seed = m.chimeStep(null, [], none, chOn()).next;
  const off = m.chimeStep(seed, m.blockedCauses([ask("a")]), none, chOn({ on: false }));
  assert.equal(off.play, false);
  const on = m.chimeStep(off.next, m.blockedCauses([ask("a")]), none, chOn());
  assert.equal(on.play, false);
});

test("chimeStep: a cause on a visible pane stays silent in a focused tab, and plays in an unfocused one", () => {
  const seed = m.chimeStep(null, [], none, chOn()).next;
  const cs = m.blockedCauses([ask("a")]);
  assert.equal(m.chimeStep(seed, cs, new Set(["a"]), chOn({ focused: true })).play, false);
  assert.equal(m.chimeStep(seed, cs, new Set(["a"]), chOn({ focused: false })).play, true);
  assert.equal(m.chimeStep(seed, cs, none, chOn({ focused: true })).play, true, "off-screen plays while focused");
});

test("chimeStep: a store cause shared by a visible and a hidden session is on screen", () => {
  const seed = m.chimeStep(null, [], none, chOn()).next;
  const cs = m.blockedCauses([storeS("t1"), storeS("t2")]);
  assert.equal(m.chimeStep(seed, cs, new Set(["t1"]), chOn()).play, false);
});

test("chimeStep: a PTY blip that returns within the re-arm window stays silent", () => {
  const pty = (id) => Object.assign(ask(id), { activity_source: "pty" });
  const seed = m.chimeStep(null, [], none, chOn()).next;
  const r1 = m.chimeStep(seed, m.blockedCauses([pty("a")]), none, chOn({ now: T0 }));
  assert.equal(r1.play, true);
  const gone = m.chimeStep(r1.next, [], none, chOn({ now: T0 + 3000 }));
  const back = m.chimeStep(gone.next, m.blockedCauses([pty("a")]), none, chOn({ now: T0 + 6000 }));
  assert.equal(back.play, false);
  const late = m.chimeStep(gone.next, m.blockedCauses([pty("a")]), none, chOn({ now: T0 + 3000 + NEEDS_REARM_MS }));
  assert.equal(late.play, true);
});
