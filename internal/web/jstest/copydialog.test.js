"use strict";
// The private-copy conversion dialog's pure half (#371): what it says, when Start is allowed, how the profile is changed and how
// the inplace_history question is asked. The DOM half (copydialog.js, below the pure part) is exercised by hand.
const test = require("node:test");
const assert = require("node:assert");
const {
  copyDialogModel, cdEstimateBody, cdMergeFree, cdChangesLine, cdRefusalFromCheck, cdSavedDoc, cdIsInplaceHistory, cdSaveWithAck, cdSizeLine,
  CD_NOT_SEE, CD_LFS, CD_NO_DISK, CD_FOLDER_FIRST, CD_HOME, CD_HOME_UNKNOWN, CD_ACK_LABEL, CD_BUILT_IN, CD_CONFIG, CD_SHELL, CD_BODY,
} = require("../static/copydialog.js");

const MB = 1 << 20;
const GB = 1 << 30;
const refusal = { error: "a git repo boxes as a private copy", code: "needs_copy", repo: "/home/u/repo", suggested: "/home/u/box-clones/claude", worktree: false, inside: false, lfs: false, git_home: false };
const est = { git_bytes: 100 * MB, tree_bytes: 50 * MB, free_bytes: 10 * GB, enough: true, partial: false, lfs: false };
const plain = { name: "claude" };
const home = "/home/u";

test("a plain repo: the copy can be made, with its size, free space and what the box will not see", () => {
  const m = copyDialogModel(refusal, est, plain, { home });
  assert.strictEqual(m.repo, "/home/u/repo");
  assert.strictEqual(m.suggested, "/home/u/box-clones/claude");
  assert.strictEqual(m.canConvert, true);
  assert.strictEqual(m.needsAck, false);
  assert.strictEqual(m.worktreeOnly, false);
  assert.deepStrictEqual(m.lines, ["Copy ≈ 150.0 MB · free 10.0 GB", "The box will not see uncommitted, untracked or gitignored files."]);
  assert.strictEqual(CD_NOT_SEE, m.lines[1]);
});

test("cdSizeLine: git + tree, human readable; a partial walk says at least; no free figure leaves it out", () => {
  assert.strictEqual(cdSizeLine(est), "Copy ≈ 150.0 MB · free 10.0 GB");
  assert.strictEqual(cdSizeLine({ ...est, partial: true }), "Copy ≥ 150.0 MB · free 10.0 GB");
  assert.strictEqual(cdSizeLine({ git_bytes: 1, tree_bytes: 2 }), "Copy ≈ 3 bytes");
});

test("a linked worktree: the refusal text, no conversion, Host only", () => {
  const m = copyDialogModel({ ...refusal, worktree: true, error: "this folder is a linked git worktree" }, est, plain, { home });
  assert.strictEqual(m.worktreeOnly, true);
  assert.strictEqual(m.canConvert, false);
  assert.deepStrictEqual(m.lines, ["this folder is a linked git worktree"]);
});

test("LFS from the refusal or from the estimate adds the pointer-file line", () => {
  assert.ok(copyDialogModel({ ...refusal, lfs: true }, est, plain, { home }).lines.includes(CD_LFS));
  assert.ok(copyDialogModel(refusal, { ...est, lfs: true }, plain, { home }).lines.includes(CD_LFS));
  assert.strictEqual(CD_LFS, "Git LFS files arrive as pointer files.");
  assert.ok(!copyDialogModel(refusal, est, plain, { home }).lines.includes(CD_LFS));
});

test("not enough free disk: said, and no conversion", () => {
  const m = copyDialogModel(refusal, { ...est, enough: false, free_bytes: 100 * MB }, plain, { home });
  assert.strictEqual(m.canConvert, false);
  assert.ok(m.lines.includes(CD_NO_DISK));
  assert.strictEqual(CD_NO_DISK, "Not enough free disk");
});

test("a profile folder outside the repo must be set to the repo first; the repo itself or inside it is fine", () => {
  const out = copyDialogModel(refusal, est, { ...plain, default_cwd: "/home/u/elsewhere" }, { home });
  assert.strictEqual(out.canConvert, false);
  assert.ok(out.lines.includes(CD_FOLDER_FIRST));
  assert.strictEqual(CD_FOLDER_FIRST, "Set this profile's folder to the repo first.");
  assert.strictEqual(copyDialogModel(refusal, est, { ...plain, default_cwd: "/home/u/repo" }, { home }).canConvert, true);
  assert.strictEqual(copyDialogModel(refusal, est, { ...plain, default_cwd: "~/repo/pkg/" }, { home }).canConvert, true);
  assert.strictEqual(copyDialogModel(refusal, est, { ...plain, default_cwd: "" }, { home }).canConvert, true);
  // A sibling that only shares a name prefix is outside.
  assert.strictEqual(copyDialogModel(refusal, est, { ...plain, default_cwd: "/home/u/repo2" }, { home }).canConvert, false);
});

test("the server's git_home verdict decides: a home repo is never converted, whatever the page's own home says", () => {
  const m = copyDialogModel({ ...refusal, git_home: true, inside: true }, est, plain, { home });
  assert.strictEqual(m.canConvert, false);
  assert.strictEqual(m.hostOnly, true);
  assert.deepStrictEqual(m.lines, [CD_HOME]);
  assert.strictEqual(CD_HOME, "This folder is inside the git repo at your home directory. Pick Host, or move the folder out of that repo.");
  // The same with a home the page has not loaded (or a symlinked one): the page's own comparison is not consulted.
  assert.strictEqual(copyDialogModel({ ...refusal, git_home: true }, est, plain, { home: "" }).canConvert, false);
  assert.strictEqual(copyDialogModel({ ...refusal, git_home: true, repo: "/elsewhere/real-home" }, est, plain, { home }).canConvert, false);
  // A repo whose path equals the page's home string but that the server says is not home converts: the server decides.
  assert.strictEqual(copyDialogModel({ ...refusal, repo: "/home/u", git_home: false }, est, plain, { home }).canConvert, true);
});

test("a refusal without git_home (an older server) is not converted", () => {
  const old = { ...refusal };
  delete old.git_home;
  const m = copyDialogModel(old, est, plain, { home });
  assert.strictEqual(m.canConvert, false);
  assert.strictEqual(m.hostOnly, true);
  assert.deepStrictEqual(m.lines, [CD_HOME_UNKNOWN]);
});

test("a profile that already makes copies of another repo is not converted; the same repo or none is", () => {
  const other = copyDialogModel(refusal, est, { ...plain, clone_of: "/home/u/other", default_cwd: "" }, { home });
  assert.strictEqual(other.canConvert, false);
  assert.ok(other.lines.includes("This profile already makes private copies of /home/u/other. Use a separate profile for this repo."));
  assert.strictEqual(copyDialogModel(refusal, est, { ...plain, clone_of: "~/repo/" }, { home }).canConvert, true);
  assert.strictEqual(copyDialogModel(refusal, est, { ...plain, clone_of: "" }, { home }).canConvert, true);
});

test("no estimate yet or a refused one: nothing to convert with, and the error is said", () => {
  const wait = copyDialogModel(refusal, null, plain, { home });
  assert.strictEqual(wait.canConvert, false);
  assert.strictEqual(wait.loading, true);
  assert.deepStrictEqual(wait.lines, ["The box will not see uncommitted, untracked or gitignored files."]);
  const bad = copyDialogModel(refusal, null, plain, { home, estimateError: "that folder is refused" });
  assert.strictEqual(bad.canConvert, false);
  assert.strictEqual(bad.loading, false);
  assert.ok(bad.lines.includes("that folder is refused"));
});

test("needsAck follows the server asking for the history confirmation", () => {
  assert.strictEqual(copyDialogModel(refusal, est, plain, { home, ackAsked: true }).needsAck, true);
  assert.strictEqual(copyDialogModel(refusal, est, plain, { home }).needsAck, false);
  assert.strictEqual(CD_ACK_LABEL, "The .git folder here existed before any box ran in this folder.");
});

test("a profile saved as Host is not converted behind the user's back", () => {
  const m = copyDialogModel(refusal, est, { ...plain, runtime: "host" }, { home });
  assert.strictEqual(m.canConvert, false);
  assert.ok(m.lines.includes("This profile runs on Host. A private copy runs in a box: switch the profile to Box first."));
});

test("cdEstimateBody: a full estimate first; once the repo is measured, the free space alone against its sizes", () => {
  assert.deepStrictEqual(cdEstimateBody("/h/a b", "/h/c&d", null), { of: "/h/a b", dir: "/h/c&d" });
  assert.deepStrictEqual(cdEstimateBody("/h/r", "/h/c", est),
    { of: "/h/r", dir: "/h/c", free_only: true, git_bytes: 100 * MB, tree_bytes: 50 * MB });
});

test("cdMergeFree: the cached sizes, partial and lfs stay; the free space and the verdict are the new answer's", () => {
  const known = { ...est, partial: true, lfs: true };
  const got = { git_bytes: 100 * MB, tree_bytes: 50 * MB, free_bytes: 1 * MB, enough: false, partial: false, lfs: false };
  assert.deepStrictEqual(cdMergeFree(known, got), { ...known, free_bytes: 1 * MB, enough: false });
});

// The save rewrites the profile for good (#371 C3): the dialog says which profile and what it will do from now on.
test("the dialog body names the profile it changes and what it does from now on", () => {
  const m = copyDialogModel(refusal, est, plain, { home, profileName: "claude" });
  assert.strictEqual(cdChangesLine("claude", "/home/u/repo"), "This changes the profile claude: it will work on private copies of /home/u/repo from now on.");
  assert.strictEqual(m.body, CD_BODY + " " + cdChangesLine("claude", "/home/u/repo"));
  assert.strictEqual(copyDialogModel(refusal, est, plain, { home }).body, m.body, "the profile's own name when none is given");
  assert.strictEqual(copyDialogModel({ ...refusal, worktree: true }, est, plain, { home }).body, "", "nothing is changed for a worktree");
});

// #371 N-3: a profile that cannot be changed is not told it will be.
test("a blocked profile's dialog does not say it changes the profile", () => {
  for (const [p, ctx] of [
    [{ ...plain, editable: false, source: "builtin" }, {}],
    [{ ...plain, runtime: "host" }, {}],
    [{ ...plain, clone_of: "/home/u/other" }, {}],
    [{ ...plain, default_cwd: "/elsewhere" }, {}],
    [{ name: "zsh" }, { shellProfile: "zsh" }],
  ]) {
    const m = copyDialogModel(refusal, est, p, { home, ...ctx });
    assert.strictEqual(m.canConvert, false, JSON.stringify(p));
    assert.strictEqual(m.body, CD_BODY, JSON.stringify(p));
  }
  // Not enough disk is no block on the profile: the change is still what Start would do once there is room.
  assert.strictEqual(copyDialogModel(refusal, { ...est, enough: false }, plain, { home }).body, CD_BODY + " " + cdChangesLine("claude", "/home/u/repo"));
});

test("the quick-shell profile is never converted: every later + Shell would open in the copy", () => {
  const m = copyDialogModel(refusal, est, { name: "zsh" }, { home, profileName: "zsh", shellProfile: "zsh" });
  assert.strictEqual(m.canConvert, false);
  assert.ok(m.lines.includes(CD_SHELL), JSON.stringify(m.lines));
  assert.strictEqual(copyDialogModel(refusal, est, plain, { home, profileName: "claude", shellProfile: "zsh" }).canConvert, true);
  assert.strictEqual(copyDialogModel(refusal, est, plain, { home, profileName: "claude", shellProfile: "" }).canConvert, true);
});

test("a profile that cannot be edited: built in, or defined in the config file, each said as it is", () => {
  const b = copyDialogModel(refusal, est, { ...plain, editable: false, source: "builtin" }, { home });
  assert.strictEqual(b.canConvert, false);
  assert.ok(b.lines.includes(CD_BUILT_IN) && !b.lines.includes(CD_CONFIG));
  const c = copyDialogModel(refusal, est, { ...plain, editable: false, source: "config" }, { home });
  assert.strictEqual(c.canConvert, false);
  assert.ok(c.lines.includes(CD_CONFIG) && !c.lines.includes(CD_BUILT_IN));
  assert.match(CD_CONFIG, /defined in the config file/);
});

test("cdRefusalFromCheck: a workspace check of a git folder as the refusal the dialog reads, with the server's suggestion and home verdict", () => {
  const chk = { git_repo: "/home/u/repo", git_worktree: true, git_inside: true, git_home: false, suggested: "/home/u/box-clones/claude" };
  assert.deepStrictEqual(cdRefusalFromCheck(chk), { error: "", code: "needs_copy", repo: "/home/u/repo", suggested: "/home/u/box-clones/claude", worktree: true, inside: true, lfs: false, git_home: false });
  assert.strictEqual(cdRefusalFromCheck({ ...chk, git_home: true }).git_home, true);
  // An older server's check has neither: no suggestion, and no home verdict, so the model will not convert.
  const old = cdRefusalFromCheck({ git_repo: "/home/u/repo" });
  assert.strictEqual(old.suggested, "");
  assert.strictEqual(old.git_home, undefined);
  assert.strictEqual(copyDialogModel(old, est, plain, { home }).canConvert, false);
  assert.strictEqual(cdRefusalFromCheck({ git_repo: "" }), null);
  assert.strictEqual(cdRefusalFromCheck(null), null);
});

test("cdSavedDoc: the stored profile with clone_of and default_cwd set, and nothing else touched; rev and schema stay out", () => {
  const doc = { name: "claude", bin: "claude", rev: "r1", schema: "s1", options: { a: "b" }, default_cwd: "/home/u/repo" };
  const out = cdSavedDoc(doc, "/home/u/repo", "/home/u/box-clones/claude", false);
  assert.deepStrictEqual(out, { name: "claude", bin: "claude", options: { a: "b" }, clone_of: "/home/u/repo", default_cwd: "/home/u/box-clones/claude" });
  assert.strictEqual(doc.clone_of, undefined, "the stored doc is not mutated");
  assert.strictEqual(cdSavedDoc(doc, "/r", "/c", true).ack_inplace_history, true);
  assert.ok(!("ack_inplace_history" in cdSavedDoc(doc, "/r", "/c", false)));
});

test("cdIsInplaceHistory reads the code off the error or its body", () => {
  assert.strictEqual(cdIsInplaceHistory({ status: 400, code: "inplace_history" }), true);
  assert.strictEqual(cdIsInplaceHistory({ status: 400, body: { code: "inplace_history" } }), true);
  assert.strictEqual(cdIsInplaceHistory({ status: 400, code: "other" }), false);
  assert.strictEqual(cdIsInplaceHistory({ status: 500, code: "inplace_history" }), false);
  assert.strictEqual(cdIsInplaceHistory(null), false);
});

test("cdSaveWithAck: a save the server accepts is sent once, with no ack", async () => {
  const sent = [];
  const out = await cdSaveWithAck(async (ack) => { sent.push(ack); return "saved"; }, async () => { throw new Error("not asked"); });
  assert.strictEqual(out, "saved");
  assert.deepStrictEqual(sent, [false]);
});

test("cdSaveWithAck: inplace_history asks with the server's words and resends once with the ack when confirmed", async () => {
  const sent = [];
  const asked = [];
  const refuse = Object.assign(new Error("the .git here may be box-written"), { status: 400, code: "inplace_history" });
  const out = await cdSaveWithAck(async (ack) => { sent.push(ack); if (!ack) throw refuse; return "saved"; }, async (msg) => { asked.push(msg); return true; });
  assert.strictEqual(out, "saved");
  assert.deepStrictEqual(sent, [false, true]);
  assert.deepStrictEqual(asked, ["the .git here may be box-written"]);
});

test("cdSaveWithAck: declining throws the server's refusal and sends nothing more; another error passes through untouched", async () => {
  const refuse = Object.assign(new Error("the .git here may be box-written"), { status: 400, code: "inplace_history" });
  const sent = [];
  await assert.rejects(cdSaveWithAck(async (ack) => { sent.push(ack); throw refuse; }, async () => false), (e) => e === refuse);
  assert.deepStrictEqual(sent, [false]);
  const other = Object.assign(new Error("boom"), { status: 500 });
  await assert.rejects(cdSaveWithAck(async () => { throw other; }, async () => { throw new Error("not asked"); }), (e) => e === other);
});

test("cdSaveWithAck: a second inplace_history after the ack is not asked again", async () => {
  const refuse = Object.assign(new Error("again"), { status: 400, code: "inplace_history" });
  let asks = 0;
  await assert.rejects(cdSaveWithAck(async () => { throw refuse; }, async () => { asks++; return true; }), (e) => e === refuse);
  assert.strictEqual(asks, 1);
});

// --- the DOM half, over a fake page ---------------------------------------------------------------------------
// copyDialogAsk with askDialog, api and the timers as stand-ins: what it asks the server, and when (#371 C4, D1, C3).

const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { EvNode, Text } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");

function cdPage() {
  const doc = { activeElement: null };
  doc.createElement = (t) => new EvNode(t, doc);
  doc.createTextNode = (t) => new Text(t);
  const els = { "ask-modal": new EvNode("dialog", doc), "ask-input": new EvNode("input", doc), "ask-ok": new EvNode("button", doc) };
  els["ask-modal"].open = false;
  els["ask-input"].value = "";
  doc.getElementById = (id) => els[id] || null;
  const calls = []; // every api request: {path, body, signal, resolve, reject}
  const asks = []; // every askDialog: {opts, resolve}
  const timers = []; // every setTimeout: {fn, ms, cleared}
  const box = {
    console, document: doc, window: { shellProfile: "" }, AbortController,
    humanBytes: (n) => n + " B",
    setTimeout: (fn, ms) => { timers.push({ fn, ms, cleared: false }); return timers.length; },
    clearTimeout: (id) => { if (timers[id - 1]) timers[id - 1].cleared = true; },
    api: (p, o) => new Promise((resolve, reject) => {
      const c = { path: p, method: o && o.method, body: o && o.body ? JSON.parse(o.body) : null, signal: o && o.signal, resolve, reject };
      if (c.signal) c.signal.addEventListener("abort", () => reject(new Error("aborted")));
      calls.push(c);
    }),
    askDialog: (opts) => {
      els["ask-input"].value = opts.value || "";
      return new Promise((resolve) => asks.push({ opts, resolve }));
    },
  };
  const ctx = vm.createContext(box);
  vm.runInContext(fs.readFileSync(path.join(STATIC, "copydialog.js"), "utf8"), ctx);
  // fire: the pending, uncleared timers of `ms` (the 400 ms edit debounce), as if that time had passed.
  const fire = (ms) => timers.filter((t) => !t.cleared && t.ms === ms).forEach((t) => { t.cleared = true; t.fn(); });
  const settle = () => new Promise((r) => setImmediate(r));
  const edit = (v) => { els["ask-input"].value = v; els["ask-input"].dispatchEvent({ type: "input" }); };
  return { box, ctx, els, calls, asks, fire, settle, edit };
}

test("the dialog measures the repo once, then asks only for the free space on a location edit, aborting the request still out", async () => {
  const pg = cdPage();
  const done = pg.box.copyDialogAsk(refusal, { profile: plain, profileName: "claude", home });
  assert.strictEqual(pg.calls.length, 1);
  assert.deepStrictEqual([pg.calls[0].path, pg.calls[0].method], ["/api/clone/estimate", "POST"]);
  assert.deepStrictEqual(pg.calls[0].body, { of: "/home/u/repo", dir: "/home/u/box-clones/claude" });
  pg.calls[0].resolve(est);
  await pg.settle();
  assert.strictEqual(pg.els["ask-ok"].disabled, false, "the copy fits: Start is on");

  pg.edit("/home/u/elsewhere");
  assert.strictEqual(pg.els["ask-ok"].disabled, true, "no answer for the new location yet");
  assert.strictEqual(pg.calls.length, 1, "nothing is asked before the debounce");
  pg.fire(400);
  assert.deepStrictEqual(pg.calls[1].body,
    { of: "/home/u/repo", dir: "/home/u/elsewhere", free_only: true, git_bytes: est.git_bytes, tree_bytes: est.tree_bytes });
  pg.edit("/home/u/third");
  pg.fire(400);
  assert.strictEqual(pg.calls[1].signal.aborted, true, "the older request is aborted");
  assert.strictEqual(pg.calls.length, 3);
  pg.calls[2].resolve({ git_bytes: est.git_bytes, tree_bytes: est.tree_bytes, free_bytes: 1, enough: false, partial: false, lfs: false });
  await pg.settle();
  assert.strictEqual(pg.els["ask-ok"].disabled, true, "the new location does not fit");

  // Closing ends it: the listener leaves #ask-input (which outlives the dialog), and the last request is aborted.
  pg.edit("/home/u/fourth");
  pg.asks[0].resolve({ outcome: "cancel", value: "" });
  const r = await done;
  assert.strictEqual(r.outcome, "cancel");
  assert.strictEqual((pg.els["ask-input"].ev.input || []).length, 0);
  pg.fire(400);
  pg.edit("/home/u/fifth");
  pg.fire(400);
  assert.strictEqual(pg.calls.length, 3, "a closed dialog asks nothing more");
});

test("a dialog opened again for the same repo (a refused save) asks only for the free space", async () => {
  const pg = cdPage();
  const sizes = vm.runInContext("new Map()", pg.ctx);
  const first = pg.box.copyDialogAsk(refusal, { profile: plain, home, sizes });
  pg.calls[0].resolve(est);
  await pg.settle();
  pg.asks[0].resolve({ outcome: "cancel", value: "" });
  await first;
  const again = pg.box.copyDialogAsk(refusal, { profile: plain, home, sizes, note: "Could not make this profile a private copy: x" });
  assert.strictEqual(pg.calls.length, 2);
  assert.strictEqual(pg.calls[1].body.free_only, true);
  pg.asks[1].resolve({ outcome: "cancel", value: "" });
  await again;
});

test("the dialog names the profile it changes, and has a warning strip only for a note (D1)", async () => {
  const pg = cdPage();
  const done = pg.box.copyDialogAsk(refusal, { profile: plain, profileName: "claude", home });
  const o = pg.asks[0].opts;
  assert.ok(o.body.endsWith("This changes the profile claude: it will work on private copies of /home/u/repo from now on."), o.body);
  assert.strictEqual(o.extra.filter((n) => n.className === "lk-warn").length, 0);
  pg.asks[0].resolve({ outcome: "cancel", value: "" });
  await done;
  const noted = pg.box.copyDialogAsk(refusal, { profile: plain, home, note: "the server said no" });
  const warn = pg.asks[1].opts.extra.filter((n) => n.className === "lk-warn");
  assert.strictEqual(warn.length, 1);
  assert.strictEqual(warn[0].textContent, "the server said no");
  pg.asks[1].resolve({ outcome: "cancel", value: "" });
  await noted;
});

test("the quick-shell profile is blocked in the dialog itself (window.shellProfile)", async () => {
  const pg = cdPage();
  pg.box.window.shellProfile = "zsh";
  const done = pg.box.copyDialogAsk(refusal, { profile: { name: "zsh" }, profileName: "zsh", home });
  pg.calls[0].resolve(est);
  await pg.settle();
  assert.strictEqual(pg.els["ask-ok"].disabled, true);
  pg.asks[0].resolve({ outcome: "cancel", value: "" });
  await done;
});

test("a hidden lk-warn is not drawn: display flex must not beat the hidden attribute (D1)", () => {
  for (const f of ["app.css", "desktop.css"]) {
    assert.match(fs.readFileSync(path.join(STATIC, f), "utf8"), /#ask-modal \.lk-warn\[hidden\][^{]*\{ display: none; \}/, f);
  }
});
