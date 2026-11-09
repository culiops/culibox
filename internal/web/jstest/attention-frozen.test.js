"use strict";
// Plan 2 (#172, F21-B) froze the functions behind the notifications, the tab title and the
// favicon, so a sidebar task could not move them by side effect. Plan 7 (#177) is the decision
// that moves the notification, title and favicon behaviour, so the pins for what it rewrote are
// gone (attentionTitle, faviconHref, signalAttention, notifyTransitions, notifyPoll) and the
// behaviour pins now state the new rule: a finished turn is not in "(N)" but gives the check
// mark. What stays hashed (sha256 of the source text, computed on main 2458a2c with
// `git show 2458a2c:internal/web/static/<file>`) is what Plan 7 must NOT change: the per-session
// predicates other surfaces read, and waitingCount, which also drives the phone pip and nav badge.
const test = require("node:test");
const assert = require("node:assert");
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const att = require("../static/attention.js");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");

// fnSrc cuts one top-level function out of a classic script by name.
function fnSrc(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}
const sha = (s) => crypto.createHash("sha256").update(s).digest("hex");

const FROZEN = {
  "attention.js": {
    needsYou: "9c01907cc8cb2241c18a06432f44b844ed177ada528bae765e2f0e1cbca2eec2",
    needsTransitions: "5cbf722b99affdf2f2a7d63f7f76aeb7a26256011c269ac14826e6ce5d33d98a",
    tabNeedWhy: "cecd6821867f348c8013c08d4d3b0645cccb3b749cfb641694f9a805feefb6a8",
    needLabel: "39698945a56878555e268996944a096560b44c6a408e42d4a18a41e765d37e7e",
    // The activity dot every surface reads.
    sessionDot: "f67996244553319b86b27621bde878ace24d722c848bb7ea576562bc5a0e258c",
  },
  "app.js": {
    waitingCount: "d1aed49a5d476614df48c8901624987dda735a9c598e62048c12cbb47da5af15",
  },
};

for (const [file, fns] of Object.entries(FROZEN)) {
  const src = read(file);
  for (const [name, want] of Object.entries(fns)) {
    test("frozen: " + file + " " + name + " is byte-identical to main 2458a2c", () => {
      assert.strictEqual(sha(fnSrc(src, name)), want,
        name + " changed: Plan 7 must not change it (other surfaces, the phone pip and nav badge, read it)");
    });
  }
}

const turn = { id: "t", name: "t", activity: "waiting", need: "turn", claude: true, summary: "done, your move" };

test("a finished turn still needs you for the per-session predicates (rows, tab dot, phone)", () => {
  assert.strictEqual(att.needsYou(turn), true);
  assert.strictEqual(att.needsYou({ ...turn, attention: "done" }), true, "the server's attention field changes nothing here");
});

test("a finished turn no longer counts in the title's (N): it gives the check mark and the neutral icon", () => {
  const nm = require("../static/notifymodel.js");
  const sys = { lastOkAt: 1, failSince: null, signedOut: false };
  const sessions = [turn, { ...turn, id: "u", attention: "done" }];
  assert.deepStrictEqual(nm.titleState(sessions, sys, "", 2), { title: "\u27132 CuliBox", icon: "neutral" });
  assert.deepStrictEqual(nm.titleState([], sys, "dev", 2), { title: "[dev] CuliBox", icon: "normal" });
});

test("a finished turn gives a Done notification, not a Blocked one", () => {
  const nm = require("../static/notifymodel.js");
  const c = (now) => ({ now, focused: false, prefs: { master: true, block: true, done: true, ended: true, message: false } });
  const seed = nm.notifyStep(null, [{ id: "t", name: "t", activity: "working" }], c(0));
  const r = nm.notifyStep(seed.next, [{ ...turn, attention: "done" }], c(1000));
  assert.deepStrictEqual(r.notes.map((n) => n.tag), ["ab:t:done"]);
});

test("sessionDot still draws a finished turn as the waiting dot (the phone and the card surfaces)", () => {
  assert.deepStrictEqual(att.sessionDot(turn), { cls: "dot waiting", title: "waiting for input" });
  assert.deepStrictEqual(att.sessionDot({ ...turn, attention: "done" }), { cls: "dot waiting", title: "waiting for input" });
  assert.deepStrictEqual(att.sessionDot({ id: "w", activity: "working" }), { cls: "dot working", title: "working" });
});

test("waitingCount (phone pip and nav badge) still counts a finished turn", () => {
  const ctx = vm.createContext({ needsYou: att.needsYou, lastSessions: [turn, { ...turn, id: "u", attention: "done" }, { id: "w", activity: "working" }] });
  new vm.Script(fnSrc(read("app.js"), "waitingCount") + "\nglobalThis.waitingCount = waitingCount;").runInContext(ctx);
  assert.strictEqual(ctx.waitingCount(), 2);
});
