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
