"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  TP_KEYS, tierWhere, tierSamePath, tierWhy, tierHostState, tierCloneState, tierPreselect, tierModel,
} = require("../static/tierpick.js");

const home = "/home/u";
const short = (p) => p.replace("/home/u", "~");
const clone = { name: "agent-box", clone_of: "/home/u/repo", default_cwd: "/home/u/clones/repo" };
const plain = { name: "claude" };
const hostp = { name: "sh", runtime: "host" };
const bound = { name: "infra", host_block: "this profile has credential bindings (AWS sandbox, GitHub), so it runs only in a box" };

// A row's text as the user reads it: the segs joined.
const flat = (row) => row.v.map((s) => s.t || s.b || s.code).join("");
const rowOf = (card, k) => card.rows.find((r) => r.k === k);

test("tierWhere maps the three cards to the request", () => {
  assert.deepStrictEqual(TP_KEYS, ["box", "clone", "host"]);
  assert.deepStrictEqual(tierWhere("box"), { runtime: "box", clone: false });
  assert.deepStrictEqual(tierWhere("clone"), { runtime: "box", clone: true });
  assert.deepStrictEqual(tierWhere("host"), { runtime: "host", clone: false });
  assert.deepStrictEqual(tierWhere("x"), {});
});

test("tierSamePath expands ~ and ignores a trailing slash", () => {
  assert.strictEqual(tierSamePath("~/repo/", "/home/u/repo", home), true);
  assert.strictEqual(tierSamePath("/home/u/repo", "/home/u/repo2", home), false);
  assert.strictEqual(tierSamePath("", "", home), false);
  assert.strictEqual(tierSamePath("~", "/home/u", home), true);
  assert.strictEqual(tierSamePath("/", "/", home), true);
});

test("tierWhy capitalises and ends the sentence", () => {
  assert.strictEqual(tierWhy("  clone mode needs git 2.40"), "Clone mode needs git 2.40.");
  assert.strictEqual(tierWhy("Already done."), "Already done.");
  assert.strictEqual(tierWhy("really?"), "Really?");
  assert.strictEqual(tierWhy("stop!"), "Stop!");
});

test("tierHostState reads host_block and nothing else", () => {
  assert.deepStrictEqual(tierHostState(plain), { ok: true, why: "" });
  assert.deepStrictEqual(tierHostState(null), { ok: true, why: "" });
  assert.deepStrictEqual(tierHostState({ host_block: "" }), { ok: true, why: "" });
  assert.deepStrictEqual(tierHostState({ host_block: "only in a box" }), { ok: false, why: "Only in a box." });
});

test("tierPreselect: clone at its repo or clone dir, else host if saved so, else in place", () => {
  assert.strictEqual(tierPreselect({ profile: clone, cwd: "/home/u/repo", home }), "clone");
  assert.strictEqual(tierPreselect({ profile: clone, cwd: "/home/u/clones/repo", home }), "clone");
  assert.strictEqual(tierPreselect({ profile: clone, cwd: "/home/u/other", home }), "box");
  assert.strictEqual(tierPreselect({ profile: hostp, cwd: "/home/u/x", home }), "host");
  assert.strictEqual(tierPreselect({ profile: plain, cwd: "/home/u/x", home }), "box");
  assert.strictEqual(tierPreselect({ profile: null, cwd: "", home }), "box");
});

test("tierCloneState: unavailable, no setup, another repo", () => {
  const o = { cwd: "/home/u/other", home, short };
  assert.strictEqual(tierCloneState({ ...o, profile: plain }).why, "This profile has no clone setup (Settings ⚙ › profile › Workspace).");
  assert.strictEqual(tierCloneState({ ...o, profile: clone }).why, "This profile's clone setup is for ~/repo.");
  assert.strictEqual(tierCloneState({ ...o, profile: clone, cloneUnavailable: "clone mode needs git 2.40" }).why, "Clone mode needs git 2.40.");
  assert.deepStrictEqual(tierCloneState({ ...o, profile: clone, cwd: "/home/u/repo" }), { ok: true, why: "" });
  assert.strictEqual(tierCloneState({ ...o, profile: clone }).ok, false);
});

test("tierModel: a host_block disables Host and a picked host falls back to in place", () => {
  const m = tierModel({ profile: bound, cwd: "/home/u/x", home, picked: "host", short });
  assert.strictEqual(m.pick, "box");
  const h = m.cards[2];
  assert.strictEqual(h.key, "host");
  assert.strictEqual(h.disabled, true);
  assert.strictEqual(h.why, "This profile has credential bindings (AWS sandbox, GitHub), so it runs only in a box.");
});

test("tierModel keeps an enabled pick, drops a disabled one, and follows the workspace when nothing was picked", () => {
  const at = (cwd, picked) => tierModel({ profile: clone, cwd, home, picked, short });
  let m = at("/home/u/repo", "");
  assert.strictEqual(m.pick, "clone");
  assert.strictEqual(m.dflt, "clone");
  m = at("/home/u/repo", "box");
  assert.strictEqual(m.pick, "box");
  assert.strictEqual(m.dflt, "clone");
  m = at("/home/u/other", "clone");
  assert.strictEqual(m.pick, "box");
  assert.strictEqual(m.cards[1].disabled, true);
  // A host-saved profile whose Host card is blocked: the default falls back to in place.
  m = tierModel({ profile: { runtime: "host", host_block: "no" }, cwd: "/x", home, picked: "", short });
  assert.strictEqual(m.dflt, "box");
  assert.strictEqual(m.pick, "box");
});

test("tierModel card text is the issue's wording", () => {
  const m = tierModel({ profile: plain, cwd: "/home/u/repo", home, picked: "", short });
  assert.deepStrictEqual(m.cards.map((c) => c.key), ["box", "clone", "host"]);
  assert.deepStrictEqual(m.cards.map((c) => c.title), ["Box · in place", "Box · clone", "Host · not contained"]);
  const [box, cl, host] = m.cards;

  assert.deepStrictEqual(box.rows.map((r) => r.k), ["Your folder", "Rest of machine"]);
  assert.strictEqual(flat(rowOf(box, "Your folder")), "edited live. Each change lands in ~/repo as the agent makes it, uncommitted work included.");
  assert.strictEqual(flat(rowOf(box, "Rest of machine")), "hidden");

  assert.deepStrictEqual(cl.rows.map((r) => r.k), ["Your folder", "Its work", "Rest of machine"]);
  assert.strictEqual(flat(rowOf(cl, "Your folder")), "untouched. The agent works on its own copy of the repo.");
  assert.strictEqual(flat(rowOf(cl, "Its work")), "comes home as box/<branch> branches in your repo, for you to review and merge.");
  assert.strictEqual(rowOf(cl, "Its work").v.find((s) => s.code).code, "box/<branch>");
  assert.strictEqual(flat(rowOf(cl, "Rest of machine")), "hidden");

  assert.deepStrictEqual(host.rows.map((r) => r.k), ["Your folder", "Rest of machine"]);
  assert.strictEqual(flat(rowOf(host, "Your folder")), "edited live.");
  assert.strictEqual(flat(rowOf(host, "Rest of machine")), "your whole account: every file, key and network this machine has.");
  assert.strictEqual(rowOf(host, "Rest of machine").v.find((s) => s.b).b, "your whole account:");

  // The bold lead of each consequence is a b seg, not plain text.
  assert.strictEqual(rowOf(box, "Your folder").v[0].b, "edited live.");
  assert.strictEqual(rowOf(cl, "Your folder").v[0].b, "untouched.");

  // No workspace: the in-place text names the workspace.
  const none = tierModel({ profile: plain, cwd: "", home, picked: "", short });
  assert.strictEqual(flat(rowOf(none.cards[0], "Your folder")), "edited live. Each change lands in the workspace as the agent makes it, uncommitted work included.");

  // In place from a clone dir edits the repo, not the clone dir.
  const atClone = tierModel({ profile: clone, cwd: "/home/u/clones/repo", home, picked: "", short });
  assert.match(flat(rowOf(atClone.cards[0], "Your folder")), /lands in ~\/repo as/);
  assert.doesNotMatch(flat(rowOf(atClone.cards[0], "Your folder")), /clones/);

  // Only the pick has `on`, only the default has `dflt`.
  const picked = tierModel({ profile: clone, cwd: "/home/u/repo", home, picked: "box", short });
  assert.deepStrictEqual(picked.cards.map((c) => c.on), [true, false, false]);
  assert.deepStrictEqual(picked.cards.map((c) => c.dflt), [false, true, false]);
});

test("tierModel: the VM line on the two Box cards only", () => {
  const line = "Box VM is stopped; starting it takes about a minute.";
  const on = tierModel({ profile: plain, cwd: "/x", home, picked: "", vmStopped: true, short });
  assert.deepStrictEqual(on.cards.map((c) => c.vm), [line, line, ""]);
  const off = tierModel({ profile: plain, cwd: "/x", home, picked: "", vmStopped: false, short });
  assert.deepStrictEqual(off.cards.map((c) => c.vm), ["", "", ""]);
});
