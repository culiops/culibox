"use strict";
// Plan 2 (#172, F21-B): on desktop a finished turn leaves the amber group, but
// the notifications, the tab title and the favicon keep counting it exactly as
// before (Plan 7 changes that later). This file freezes the functions behind
// those three: the sha256 of each one's source text was computed on main
// 2458a2c (`git show 2458a2c:internal/web/static/<file>`), so an edit to any of
// them (attentionTitle, faviconHref and sessionDot included) fails here and has
// to be a decision, not a side effect of a sidebar task.
// Behaviour pins sit beside the hashes so a failure also says what moved.
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
    notifyPoll: "39cd5398c77ffb2aaef3090b49eebc203b556933a098ea220ddd64d16fb811e7",
    // The tab title, the favicon and the activity dot every surface reads (the
    // issue's out-of-scope note names the first two).
    sessionDot: "f67996244553319b86b27621bde878ace24d722c848bb7ea576562bc5a0e258c",
    attentionTitle: "78224420721664f775d8dd9b25cddf50c6a4d1cbf812d8a695d1cb8f7f5ea3c4",
    faviconHref: "c65957ddb27f2d08d75d6d771b8cc60c540b8870c695e29c18caca11bf729bcc",
  },
  "app.js": {
    waitingCount: "d1aed49a5d476614df48c8901624987dda735a9c598e62048c12cbb47da5af15",
    signalAttention: "cc58fa81498127f1176afb1daf4f1474230efc3a331b3602abd3d0f0aa883d99",
    notifyTransitions: "766deada00c7952911284f50b2fbff289fae18af9ae8f45c4c7ab27239c4158b",
  },
};

for (const [file, fns] of Object.entries(FROZEN)) {
  const src = read(file);
  for (const [name, want] of Object.entries(fns)) {
    test("frozen: " + file + " " + name + " is byte-identical to main 2458a2c", () => {
      assert.strictEqual(sha(fnSrc(src, name)), want,
        name + " changed: notifications, the tab title and the favicon must keep counting a finished turn (Plan 7 changes that)");
    });
  }
}

const turn = { id: "t", name: "t", activity: "waiting", need: "turn", claude: true, summary: "done, your move" };

test("a finished turn still needs you (title, favicon and notifications count it)", () => {
  assert.strictEqual(att.needsYou(turn), true);
  assert.strictEqual(att.needsYou({ ...turn, attention: "done" }), true, "the server's attention field changes nothing here");
});

test("a new finished turn still fires a notification transition", () => {
  const seed = att.needsTransitions(null, [], 0).next;
  const r = att.needsTransitions(seed, [{ ...turn, attention: "done" }], 1000);
  assert.deepStrictEqual(r.fired.map((s) => s.id), ["t"]);
  assert.strictEqual(r.next.ids.has("t"), true);
});

test("the favicon and the title helpers turn a finished turn's count into (N) and the amber icon", () => {
  const n = [turn, { ...turn, id: "u", attention: "done" }].filter(att.needsYou).length;
  assert.strictEqual(n, 2);
  assert.strictEqual(att.attentionTitle(n, ""), "(2) CuliBox");
  assert.strictEqual(att.attentionTitle(n, "dev"), "(2) [dev] CuliBox");
  assert.strictEqual(att.attentionTitle(0, ""), "CuliBox");
  assert.strictEqual(att.faviconHref(n), "/favicon-needs.svg");
  assert.strictEqual(att.faviconHref(0), "/favicon.svg");
});

test("sessionDot still draws a finished turn as the waiting dot (the phone and the card surfaces)", () => {
  assert.deepStrictEqual(att.sessionDot(turn), { cls: "dot waiting", title: "waiting for input" });
  assert.deepStrictEqual(att.sessionDot({ ...turn, attention: "done" }), { cls: "dot waiting", title: "waiting for input" });
  assert.deepStrictEqual(att.sessionDot({ id: "w", activity: "working" }), { cls: "dot working", title: "working" });
});

test("waitingCount (title and favicon) still counts a finished turn", () => {
  const ctx = vm.createContext({ needsYou: att.needsYou, lastSessions: [turn, { ...turn, id: "u", attention: "done" }, { id: "w", activity: "working" }] });
  new vm.Script(fnSrc(read("app.js"), "waitingCount") + "\nglobalThis.waitingCount = waitingCount;").runInContext(ctx);
  assert.strictEqual(ctx.waitingCount(), 2);
});
