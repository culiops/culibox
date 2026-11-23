"use strict";
// handbackview.js: the review panel, the sidebar entry and the session-row line (#346), run against the fake DOM.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const flag = (p, classes, more) => Object.assign({ path: p, classes, detail: "d", sha256: "", hash_note: "", target: "" }, more);
const REC = (more) => Object.assign({ id: "a".repeat(32), kind: "in-place", workspace: "/w", clone_of: "", branch: "", last_scan: "t", revision: 1, flags: [], inspected: 3, truncated: "" }, more);

function page(over) {
  const doc = { activeElement: null };
  const byId = {};
  for (const id of ["hb-body", "side-flagged"]) byId[id] = new Node(id === "hb-body" ? "div" : "button", doc);
  byId["side-flagged"].hidden = true;
  doc.getElementById = (id) => byId[id] || null;
  doc.createElement = (t) => new Node(t, doc);
  const log = { api: [], bodies: [], dialogs: [], errors: [], toasts: [], copies: [], refreshed: 0, profiles: 0 };
  const state = { records: [], width: 1280, fail: null };
  const ctx = vm.createContext({
    document: doc, window: { get innerWidth() { return state.width; } }, Date, Set, Promise, setTimeout, clearTimeout, encodeURIComponent,
    mobileActive: (w) => w <= 700,
    deadlineSignal: () => undefined,
    api: async (p, o) => {
      log.api.push([p, o && o.method || "GET"]);
      log.bodies.push(o && o.body);
      if (state.fail && state.fail.when(p, o)) throw state.fail.err;
      return p === "/api/handback" ? { records: state.records } : null;
    },
    openDialog: (id) => log.dialogs.push(id),
    setDialogError: (id, m) => log.errors.push([id, m]),
    showToast: (m, o) => log.toasts.push([m, o]),
    copyText: (t, done) => log.copies.push({ t, done }),
    refreshSessions: () => { log.refreshed++; },
    refreshProfilesQuiet: () => { log.profiles++; },
  });
  Object.assign(ctx, over || {});
  for (const f of ["handbackmodel.js", "handbackview.js"]) new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  return { ctx, doc, byId, log, state };
}
const tick = () => new Promise((r) => setImmediate(r));

test("openHandback renders every pending record with textContent only, and opens the dialog", async () => {
  const pg = page();
  pg.state.records = [REC({ workspace: "/w/<img src=x>", flags: [flag("a/<b>.pdf", ["extension-mismatch"], { detail: "ELF in a .pdf", sha256: "ab12", target: "" })] })];
  await pg.ctx.openHandback();
  assert.deepStrictEqual(pg.log.dialogs, ["handback-modal"]);
  const body = pg.byId["hb-body"];
  assert.strictEqual(body.querySelectorAll(".hb-rec").length, 1);
  assert.strictEqual(body.querySelector(".hb-head").textContent, "/w/<img src=x>");
  assert.strictEqual(body.querySelector(".hb-path").textContent, "a/<b>.pdf");
  assert.strictEqual(body.querySelector(".hb-path").children.length, 0, "text, not markup");
  assert.match(body.querySelector(".hb-window").textContent, /^Files created or changed while the session ran\.$/);
  assert.strictEqual(body.querySelector(".hb-shav").textContent, "ab12");
  const foot = body.querySelectorAll(".hb-foot").map((n) => n.textContent);
  assert.deepStrictEqual(foot, ["This is not a malware scan.", "These files have been on the host since the agent wrote them."]);
});

test("a copy button per SHA-256 copies it and says how that went", async () => {
  const pg = page();
  pg.state.records = [REC({ flags: [flag("x", ["executable"], { sha256: "deadbeef" })] })];
  await pg.ctx.openHandback();
  const cp = pg.byId["hb-body"].querySelector(".hb-copy");
  cp.onclick();
  assert.strictEqual(pg.log.copies[0].t, "deadbeef");
  pg.log.copies[0].done(true);
  assert.strictEqual(cp.textContent, "Copied");
});

test("a record with nothing listed says it was cut short instead of 'Nothing flagged'", async () => {
  const pg = page();
  pg.state.records = [REC({ truncated: "stopped after 500000 entries" })];
  await pg.ctx.openHandback();
  const text = pg.byId["hb-body"].textContent;
  assert.match(text, /Not everything was checked/);
  assert.match(text, /stopped after 500000 entries/);
  assert.doesNotMatch(text, /Nothing flagged/);
});

test("no pending record: the empty text, which is not a clean bill", async () => {
  const pg = page();
  await pg.ctx.openHandback();
  assert.strictEqual(pg.byId["hb-body"].textContent, "Nothing flagged. This is not a malware scan.");
});

test("a selection narrows the panel; a profile that matches nothing shows all pending", async () => {
  const pg = page();
  pg.state.records = [REC({ id: "1".repeat(32), flags: [flag("a", ["archive"])] }), REC({ id: "2".repeat(32), workspace: "/z", flags: [flag("b", ["archive"])] })];
  await pg.ctx.openHandback("2".repeat(32));
  assert.deepStrictEqual(pg.byId["hb-body"].querySelectorAll(".hb-rec").map((n) => n.dataset.id), ["2".repeat(32)]);
  await pg.ctx.openHandback({ workspace: "/nowhere" });
  assert.strictEqual(pg.byId["hb-body"].querySelectorAll(".hb-rec").length, 2);
});

test("a large directory is one collapsed group whose files are listed inside", async () => {
  const pg = page();
  const flags = Array.from({ length: 25 }, (_, i) => flag("node_modules/x/b" + i, ["executable"]));
  pg.state.records = [REC({ flags })];
  await pg.ctx.openHandback();
  const g = pg.byId["hb-body"].querySelector(".hb-gsum");
  assert.strictEqual(g.textContent, "node_modules/x — 25 files (25 executable program)");
  assert.strictEqual(pg.byId["hb-body"].querySelectorAll(".hb-file").length, 25);
});

test("Mark reviewed POSTs, drops the section, refreshes the list and the profiles", async () => {
  const pg = page();
  pg.state.records = [REC({ flags: [flag("x", ["archive"])] })];
  await pg.ctx.openHandback();
  pg.state.records = [];
  pg.byId["hb-body"].querySelector(".hb-reviewed").onclick();
  await tick(); await tick();
  assert.ok(pg.log.api.some(([p, m]) => p === "/api/handback/" + "a".repeat(32) + "/reviewed" && m === "POST"));
  assert.strictEqual(pg.byId["hb-body"].querySelectorAll(".hb-rec").length, 0);
  assert.strictEqual(pg.byId["hb-body"].textContent, "Nothing flagged. This is not a malware scan.");
  assert.strictEqual(pg.log.profiles, 1);
  assert.strictEqual(pg.byId["side-flagged"].hidden, true);
});

test("Mark reviewed sends the revision of the record the user saw", async () => {
  const pg = page();
  pg.state.records = [REC({ last_scan: "2026-11-22T10:00:00.123456789Z", revision: 7, flags: [flag("x", ["archive"])] })];
  await pg.ctx.openHandback();
  pg.log.bodies.length = 0; pg.log.api.length = 0;
  pg.byId["hb-body"].querySelector(".hb-reviewed").onclick();
  await tick();
  const i = pg.log.api.findIndex(([p, m]) => p.endsWith("/reviewed") && m === "POST");
  assert.ok(i >= 0);
  assert.deepStrictEqual(JSON.parse(pg.log.bodies[i]), { revision: 7 });
});

test("Mark reviewed refused with 409 refreshes the list and says what happened", async () => {
  const pg = page();
  pg.state.records = [REC({ last_scan: "t", revision: 3, flags: [flag("x", ["archive"])] })];
  await pg.ctx.openHandback();
  // The daemon scanned again: the record now holds a second file the user has not seen.
  pg.state.records = [REC({ last_scan: "t", revision: 4, flags: [flag("x", ["archive"]), flag("new.pdf", ["extension-mismatch"])] })];
  pg.state.fail = { when: (p, o) => o && o.method === "POST", err: Object.assign(new Error("record changed since it was read"), { status: 409 }) };
  const btn = pg.byId["hb-body"].querySelector(".hb-reviewed");
  btn.onclick();
  await tick(); await tick(); await tick();
  const paths = pg.byId["hb-body"].querySelectorAll(".hb-path").map((n) => n.textContent);
  assert.deepStrictEqual(paths, ["new.pdf", "x"], "the list shows the new file");
  assert.deepStrictEqual(pg.log.errors.at(-1), ["handback-modal", "New files were flagged since you opened this. Review them, then mark reviewed again."]);
  assert.strictEqual(pg.byId["hb-body"].querySelector(".hb-reviewed").disabled, false);
  // The next click sends the new revision (last_scan did not move).
  pg.state.fail = null;
  pg.log.bodies.length = 0; pg.log.api.length = 0;
  pg.byId["hb-body"].querySelector(".hb-reviewed").onclick();
  await tick();
  const i = pg.log.api.findIndex(([p, m]) => p.endsWith("/reviewed") && m === "POST");
  assert.deepStrictEqual(JSON.parse(pg.log.bodies[i]), { revision: 4 });
});

test("a stale link to records already reviewed says so, not 'Nothing flagged'", async () => {
  const pg = page();
  pg.state.records = [REC({ id: "1".repeat(32), flags: [flag("a", ["archive"])] })];
  await pg.ctx.openHandback("9".repeat(32));
  assert.strictEqual(pg.byId["hb-body"].textContent, "Already marked reviewed.");
  await pg.ctx.openHandback(["8".repeat(32), "9".repeat(32)]);
  assert.strictEqual(pg.byId["hb-body"].textContent, "Already marked reviewed.");
  // The all-pending view with nothing pending keeps the usual words.
  pg.state.records = [];
  await pg.ctx.openHandback();
  assert.strictEqual(pg.byId["hb-body"].textContent, "Nothing flagged. This is not a malware scan.");
  // A profile selection that matches nothing and nothing pending is the same.
  await pg.ctx.openHandback({ workspace: "/nowhere" });
  assert.strictEqual(pg.byId["hb-body"].textContent, "Nothing flagged. This is not a malware scan.");
  // Some of the ids still pending: those are shown.
  pg.state.records = [REC({ id: "1".repeat(32), flags: [flag("a", ["archive"])] })];
  await pg.ctx.openHandback(["1".repeat(32), "9".repeat(32)]);
  assert.strictEqual(pg.byId["hb-body"].querySelectorAll(".hb-rec").length, 1);
});

test("Mark reviewed that fails keeps the record and says so in the dialog", async () => {
  const pg = page();
  pg.state.records = [REC({ flags: [flag("x", ["archive"])] })];
  await pg.ctx.openHandback();
  pg.state.fail = { when: (p, o) => o && o.method === "POST", err: Object.assign(new Error("boom"), { status: 500 }) };
  const btn = pg.byId["hb-body"].querySelector(".hb-reviewed");
  btn.onclick();
  await tick();
  assert.strictEqual(pg.byId["hb-body"].querySelectorAll(".hb-rec").length, 1);
  assert.strictEqual(btn.disabled, false);
  assert.deepStrictEqual(pg.log.errors.at(-1), ["handback-modal", "Could not mark this reviewed: boom"]);
});

test("the sidebar entry shows only while something is pending, in the model's words", async () => {
  const pg = page();
  pg.state.records = [REC({ flags: [flag("x", ["archive"]), flag("y", ["archive"])] })];
  await pg.ctx.handbackRefresh();
  assert.strictEqual(pg.byId["side-flagged"].hidden, false);
  assert.strictEqual(pg.byId["side-flagged"].textContent, "2 flagged for your review");
  pg.state.records = [];
  await pg.ctx.handbackRefresh();
  assert.strictEqual(pg.byId["side-flagged"].hidden, true);
});

test("the phone asks for nothing and never shows the entry", async () => {
  const pg = page();
  pg.state.width = 390;
  pg.state.records = [REC({ flags: [flag("x", ["archive"])] })];
  await pg.ctx.handbackRefresh();
  assert.deepStrictEqual(pg.log.api, []);
  assert.strictEqual(pg.byId["side-flagged"].hidden, true);
});

test("the sessions poll: an in-place note toasts once with a Review link; the list is read once per interval, not per poll", async () => {
  const pg = page();
  const ended = { id: "s1", agent: "p", handback: { id: "h1", count: 2, in_place: true } };
  pg.ctx.handbackOnSessions([{ id: "s1", agent: "p" }]); // seeds
  await tick();
  assert.strictEqual(pg.log.api.length, 1, "the first poll reads the list");
  pg.ctx.handbackOnSessions([ended]);
  await tick();
  assert.strictEqual(pg.log.toasts.length, 1);
  assert.strictEqual(pg.log.toasts[0][0], "p: 2 files flagged for your review");
  assert.strictEqual(pg.log.toasts[0][1].warn, true);
  assert.strictEqual(pg.log.toasts[0][1].link.label, "Review");
  assert.strictEqual(pg.log.api.length, 2, "a new note reads the list again");
  pg.ctx.handbackOnSessions([ended]);
  pg.ctx.handbackOnSessions([ended]);
  await tick();
  assert.strictEqual(pg.log.toasts.length, 1, "shown once");
  assert.strictEqual(pg.log.api.length, 2, "no read per poll");
});

test("the Review link opens the panel for the note's record", async () => {
  const pg = page();
  pg.state.records = [REC({ id: "9".repeat(32), flags: [flag("x", ["archive"])] }), REC({ id: "8".repeat(32), flags: [flag("y", ["archive"])] })];
  pg.ctx.handbackToast("t", ["9".repeat(32)]);
  pg.log.toasts[0][1].link.run();
  await tick();
  assert.deepStrictEqual(pg.byId["hb-body"].querySelectorAll(".hb-rec").map((n) => n.dataset.id), ["9".repeat(32)]);
});

test("the row line shows a session's count until its records are reviewed", async () => {
  const pg = page();
  const s = { id: "s", agent: "p", handback: { id: "h1", count: 1, in_place: true } };
  assert.strictEqual(pg.ctx.handbackLine(s).textContent, "1 file flagged for your review", "before the list is known");
  pg.state.records = [REC({ id: "other", flags: [flag("x", ["archive"])] })];
  await pg.ctx.handbackRefresh();
  assert.strictEqual(pg.ctx.handbackLine(s), null, "h1 is no longer pending");
  assert.strictEqual(pg.ctx.handbackLine({ id: "t" }), null);
});

test("index.html: the panel's dialog, the sidebar entry, and the scripts in their order", () => {
  const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
  assert.match(html, /<dialog id="handback-modal">/);
  assert.match(html, /<span class="grow">Flagged for your review<\/span>/);
  assert.match(html, /<button id="side-flagged" type="button" hidden><\/button>/);
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("handbackmodel.js") > 0 && at("handbackmodel.js") < at("workhome.js"), "the model loads before workhome.js");
  assert.ok(at("handbackview.js") > 0 && at("handbackview.js") < at("app.js"), "the view loads before app.js");
});

// noteReturns cut out of app.js: a flagged return's toast links to its records, a plain one does not.
test("noteReturns: a flagged return toasts with a link; the others are plain toasts", () => {
  const src = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
  const m = /function noteReturns\(sessions\) \{[\s\S]*?\n\}\n/.exec(src);
  assert.ok(m, "noteReturns found");
  const wh = require("../static/workhome.js");
  const calls = [];
  let pollNew = false;
  const ctx = vm.createContext(Object.assign({}, wh, {
    returnsSeen: [], returnsCloneIds: [],
    showToast: (t, o) => calls.push(["toast", t, o]),
    handbackToast: (t, ids) => calls.push(["link", t, ids]),
    handbackOnSessions: () => { calls.push(["poll"]); return pollNew; },
    handbackRefresh: () => calls.push(["refresh"]),
    notHomeSeenRead: () => null, notHomeSeenWrite: () => {}, refreshProfilesQuiet: () => calls.push(["profiles"]),
  }));
  new vm.Script(m[0] + "\nglobalThis.noteReturns = noteReturns;").runInContext(ctx);
  ctx.noteReturns([
    { id: "s1", agent: "p", clone_of: "/r", return: { trigger: "exit", at: "t1", home: ["feat"], flagged: [{ id: "h1", count: 2 }] } },
    { id: "s2", agent: "q", clone_of: "/r", return: { trigger: "exit", at: "t2", home: ["x"] } },
  ]);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(calls)), [
    ["link", "p: feat came home · 2 files flagged for your review", ["h1"]],
    ["toast", "q: x came home", null], // (undefined, as JSON)
    ["refresh"],
    ["poll"],
    ["profiles"], // a clone return refreshes the badges (as before)
  ]);
});

// Item 12: the profile badge counts flags from an in-place scan too, so a new in-place note refreshes the profiles.
test("noteReturns refreshes the profiles quietly when handbackOnSessions reports a new in-place note", () => {
  const src = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
  const m = /function noteReturns\(sessions\) \{[\s\S]*?\n\}\n/.exec(src);
  const wh = require("../static/workhome.js");
  for (const [fresh, want] of [[true, 1], [false, 0]]) {
    let profiles = 0;
    const ctx = vm.createContext(Object.assign({}, wh, {
      returnsSeen: [], returnsCloneIds: [],
      showToast: () => {}, handbackToast: () => {}, handbackRefresh: () => {},
      handbackOnSessions: () => fresh,
      notHomeSeenRead: () => null, notHomeSeenWrite: () => {}, refreshProfilesQuiet: () => { profiles++; },
    }));
    new vm.Script(m[0] + "\nglobalThis.noteReturns = noteReturns;").runInContext(ctx);
    ctx.noteReturns([{ id: "s1", agent: "p", handback: { id: "h1", count: 1, in_place: true } }]);
    assert.strictEqual(profiles, want, "fresh=" + fresh);
  }
});
