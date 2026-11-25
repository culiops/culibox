"use strict";
// The private-copy conversion dialog's pure half (#371): what it says, when Start is allowed, how the profile is changed and how
// the inplace_history question is asked. The DOM half (copydialog.js, below the pure part) is exercised by hand.
const test = require("node:test");
const assert = require("node:assert");
const {
  copyDialogModel, cdEstimateURL, cdRefusalFromCheck, cdSavedDoc, cdIsInplaceHistory, cdSaveWithAck, cdSizeLine,
  CD_NOT_SEE, CD_LFS, CD_NO_DISK, CD_FOLDER_FIRST, CD_HOME, CD_HOME_UNKNOWN, CD_ACK_LABEL,
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

test("cdEstimateURL escapes both paths", () => {
  assert.strictEqual(cdEstimateURL("/h/a b", "/h/c&d"), "/api/clone/estimate?of=%2Fh%2Fa%20b&dir=%2Fh%2Fc%26d");
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
