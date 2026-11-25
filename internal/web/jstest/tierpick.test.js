"use strict";
const test = require("node:test");
const { ICON_SRC } = require("./iconpage.js");
const assert = require("node:assert");
const {
  TP_KEYS, tierWhere, tierSamePath, tierWhy, tierHostState, tierCloneState, tierPreselect, tierModel,
  tierHostDrops, tierDropsLine, tierHostKeeps, tierHostGuardrail, tierGuardrailLine,
} = require("../static/tierpick.js");

const home = "/home/u";
const short = (p) => p.replace("/home/u", "~");
const clone = { name: "agent-box", clone_of: "/home/u/repo", default_cwd: "/home/u/clones/repo" };
const plain = { name: "claude" };
const hostp = { name: "sh", runtime: "host" };
const dropper = { name: "infra", runtime: "host", host_drops: ["context paths", "\"Sandbox\" option"] };
const guarded = { name: "infra", runtime: "host", host_guardrail: ["AWS binding", "GitHub binding"] };
const GUARD = "On Host: AWS and GitHub bindings are the default credentials, not a limit. The agent keeps your full access to this machine, including its own logins, and your shell's rc files can override the default.";
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

test("tierHostState: host_drops alone leaves Host on; only host_block turns it off (#359)", () => {
  assert.deepStrictEqual(tierHostState(dropper), { ok: true, why: "" });
  assert.deepStrictEqual(tierHostState({ ...dropper, host_block: "only in a box" }), { ok: false, why: "Only in a box." });
});

test("tierHostDrops: the profile's host_drops as a clean string list (#359)", () => {
  assert.deepStrictEqual(tierHostDrops(dropper), ["context paths", "\"Sandbox\" option"]);
  assert.deepStrictEqual(tierHostDrops(plain), []);
  assert.deepStrictEqual(tierHostDrops(null), []);
  assert.deepStrictEqual(tierHostDrops({ host_drops: "AWS binding" }), []);
  assert.deepStrictEqual(tierHostDrops({ host_drops: ["AWS binding", "", 3] }), ["AWS binding"]);
});

test("tierDropsLine joins like config.JoinKinds, with no logins tail (#360)", () => {
  assert.strictEqual(tierDropsLine(["context paths", "\"Sandbox\" option"]), "Not on Host: context paths and \"Sandbox\" option.");
  assert.strictEqual(tierDropsLine(["context paths"]), "Not on Host: context paths.");
  assert.strictEqual(tierDropsLine(["context paths", "A setting", "B setting"]), "Not on Host: context paths, A setting and B setting.");
  assert.strictEqual(tierDropsLine([]), "");
});

test("tierHostGuardrail: the profile's host_guardrail as a clean string list (#360)", () => {
  assert.deepStrictEqual(tierHostGuardrail(guarded), ["AWS binding", "GitHub binding"]);
  assert.deepStrictEqual(tierHostGuardrail(plain), []);
  assert.deepStrictEqual(tierHostGuardrail(null), []);
  assert.deepStrictEqual(tierHostGuardrail({ host_guardrail: "AWS binding" }), []);
  assert.deepStrictEqual(tierHostGuardrail({ host_guardrail: ["AWS binding", "", 3] }), ["AWS binding"]);
});

test("tierGuardrailLine says the bindings are the default credentials, not a limit (#360)", () => {
  assert.strictEqual(tierGuardrailLine(["AWS binding", "GitHub binding"]), GUARD);
  assert.strictEqual(tierGuardrailLine(["AWS binding", "GitHub binding", "token binding"]),
    "On Host: AWS, GitHub and token bindings are the default credentials, not a limit. The agent keeps your full access to this machine, including its own logins, and your shell's rc files can override the default.");
  assert.strictEqual(tierGuardrailLine(["GitHub binding"]),
    "On Host: the GitHub binding is the default credential, not a limit. The agent keeps your full access to this machine, including its own logins, and your shell's rc files can override the default.");
  assert.strictEqual(tierGuardrailLine([]), "");
  assert.ok(!/instead/.test(tierGuardrailLine(["AWS binding"])), "never the old 'uses this machine's own logins instead'");
});

// Host review 2026-11-23 (#346): Host still passes the profile's own secrets and env, so the warning names them.
test("tierHostKeeps: the profile's host_keeps as a clean string list", () => {
  assert.deepStrictEqual(tierHostKeeps({ host_keeps: ["GH_TOKEN secret", "", 3, "FOO env"] }), ["GH_TOKEN secret", "FOO env"]);
  assert.deepStrictEqual(tierHostKeeps({ host_keeps: "GH_TOKEN secret" }), []);
  assert.deepStrictEqual(tierHostKeeps(null), []);
});

test("tierDropsLine names what Host still passes after what it drops", () => {
  assert.strictEqual(tierDropsLine(["context paths"], ["GH_TOKEN secret", "FOO env"]),
    "Not on Host: context paths. Still on Host: GH_TOKEN secret and FOO env.");
  assert.strictEqual(tierDropsLine([], ["GH_TOKEN secret"]), "", "nothing dropped: no warning at all");
});

test("tierModel: the Host card has a Still on Host row when the profile drops or guards, and keeps", () => {
  const p = { ...dropper, host_keeps: ["GH_TOKEN secret"] };
  const host = tierModel({ profile: p, cwd: "/home/u/x", home, picked: "", short }).cards[2];
  assert.deepStrictEqual(host.rows.map((r) => r.k), ["Your folder", "Rest of machine", "Not on Host", "Still on Host"]);
  assert.strictEqual(flat(rowOf(host, "Still on Host")), "GH_TOKEN secret.");
  const plain = tierModel({ profile: { ...hostp, host_keeps: ["GH_TOKEN secret"] }, cwd: "/home/u/x", home, picked: "", short }).cards[2];
  assert.ok(!plain.rows.some((x) => x.k === "Still on Host"), "nothing dropped: the card says nothing new");
  const g = tierModel({ profile: { ...guarded, host_keeps: ["GH_TOKEN secret"] }, cwd: "/home/u/x", home, picked: "", short }).cards[2];
  assert.deepStrictEqual(g.rows.map((r) => r.k), ["Your folder", "Rest of machine", "On Host", "Still on Host"]);
});

test("tierPreselect: a host profile with host_drops opens on Box; a clone still wins on its workspace (#359)", () => {
  assert.strictEqual(tierPreselect({ profile: dropper, cwd: "/home/u/x", home }), "box");
  assert.strictEqual(tierPreselect({ profile: { ...dropper, ...clone }, cwd: "/home/u/repo", home }), "clone");
  assert.strictEqual(tierPreselect({ profile: hostp, cwd: "/home/u/x", home }), "host");
});

test("tierModel: Host stays pickable with host_drops, and its card has the Not on Host row only then (#359)", () => {
  const m = tierModel({ profile: dropper, cwd: "/home/u/x", home, picked: "", short });
  assert.strictEqual(m.pick, "box");
  assert.strictEqual(m.dflt, "box");
  const host = m.cards[2];
  assert.strictEqual(host.disabled, false);
  assert.deepStrictEqual(host.rows.map((r) => r.k), ["Your folder", "Rest of machine", "Not on Host"]);
  const r = rowOf(host, "Not on Host");
  assert.strictEqual(r.k + ": " + flat(r), tierDropsLine(dropper.host_drops));
  assert.strictEqual(tierModel({ profile: dropper, cwd: "/home/u/x", home, picked: "host", short }).pick, "host");
  const none = tierModel({ profile: hostp, cwd: "/home/u/x", home, picked: "", short }).cards[2];
  assert.deepStrictEqual(none.rows.map((x) => x.k), ["Your folder", "Rest of machine"]);
  const box = m.cards[0];
  assert.ok(!box.rows.some((x) => x.k === "Not on Host"));
});

test("tierModel: a guarded profile's Host card says its bindings are the default credentials, not a limit (#360)", () => {
  const m = tierModel({ profile: guarded, cwd: "/home/u/x", home, picked: "", short });
  assert.strictEqual(m.pick, "box", "Host is one click away, never the default");
  const host = m.cards[2];
  assert.strictEqual(host.disabled, false);
  assert.deepStrictEqual(host.rows.map((r) => r.k), ["Your folder", "Rest of machine", "On Host"]);
  const r = rowOf(host, "On Host");
  assert.strictEqual(r.k + ": " + flat(r), GUARD);
  assert.strictEqual(tierModel({ profile: guarded, cwd: "/home/u/x", home, picked: "host", short }).pick, "host");
  assert.ok(!m.cards[0].rows.some((x) => x.k === "On Host"));
  const both = tierModel({ profile: { ...guarded, host_drops: ["context paths"] }, cwd: "/home/u/x", home, picked: "", short }).cards[2];
  assert.deepStrictEqual(both.rows.map((r) => r.k), ["Your folder", "Rest of machine", "On Host", "Not on Host"]);
  assert.strictEqual(flat(rowOf(both, "Not on Host")), "context paths.");
});

test("tierPreselect: a host profile with a guardrail opens on Box too (#360)", () => {
  assert.strictEqual(tierPreselect({ profile: guarded, cwd: "/home/u/x", home }), "box");
});

test("tierModel: the Not on Host row has no logins tail (#359)", () => {
  const p = { name: "ctx", runtime: "host", host_drops: ["context paths"] };
  const host = tierModel({ profile: p, cwd: "/home/u/x", home, picked: "", short }).cards[2];
  const r = rowOf(host, "Not on Host");
  assert.strictEqual(r.k + ": " + flat(r), "Not on Host: context paths.");
  assert.strictEqual(r.k + ": " + flat(r), tierDropsLine(p.host_drops));
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

// --- the DOM part: cards, the one reader, and the picks -------------------------------------------------------

const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const readStatic = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// appFnSrc: one top-level function of app.js, cut by name (app.js is not loadable whole).
function appFnSrc(name) {
  const src = readStatic("app.js");
  const m = new RegExp("function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found in app.js");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

// One page: the real svgicon.js and tierpick.js in a shared scope, the dialog's globals as stand-ins.
function tpPage(over) {
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  doc.createElementNS = (ns, t) => doc.createElement(t);
  doc.createTextNode = (t) => new Text(t);
  const pick = doc.createElement("div");
  const seg = doc.createElement("div");
  seg.dataset.value = "box";
  const ws = doc.createElement("input");
  const calls = [];
  const box = {
    console, document: doc, window: { HOME_DIR: "/home/u" },
    boxRuntime: "runc", cloneUnavailable: "", desktop: true, profile: clone, cwd: "/home/u/clones/repo",
    seg, pick, calls,
    $: (id) => (id === "tierpick" ? pick : id === "runtime-seg" ? seg : id === "spawn-ws-input" ? ws : null),
    currentProfile: () => box.profile,
    getCwd: () => box.cwd,
    shortPath: (p) => short(p),
    drawerDesktop: () => box.desktop,
    segValue: (el) => (el && el.dataset && el.dataset.value) || "",
    setSegValue: (el, v) => { calls.push(["setSegValue", v]); el.dataset.value = v; },
    setSpawnCwd: (p, s) => { calls.push(["setSpawnCwd", p, s]); box.cwd = p; },
    onSpawnCwdChanged: () => calls.push(["onSpawnCwdChanged"]),
    updateRuntimeWarning: () => calls.push(["updateRuntimeWarning"]),
    syncSpawnCopyLine: () => calls.push(["syncSpawnCopyLine"]),
    syncSpawnPreview: () => calls.push(["syncSpawnPreview"]),
    paintSpawnAcctHint: () => calls.push(["paintSpawnAcctHint"]),
    // app.js's critical-rm row (#342): counted apart from `calls`, whose order the tests below pin.
    critSyncs: 0, syncCritRmRow: () => { box.critSyncs++; },
  };
  Object.assign(box, over || {});
  const ctx = vm.createContext(box);
  vm.runInContext(ICON_SRC + "\n" + readStatic("tier.js") + "\n" + readStatic("tierpick.js"), ctx);
  // An object a vm script returns has the vm's own prototypes, which deepStrictEqual refuses: hand back a plain copy.
  const run = (code) => {
    const r = vm.runInContext(code, ctx);
    return r && typeof r === "object" ? JSON.parse(JSON.stringify(r)) : r;
  };
  return { ctx, doc, pick, seg, ws, calls, run };
}

const cardsOf = (pg) => pg.pick.querySelectorAll("button.tp");
const hasClass = (n, c) => n.className.split(/\s+/).includes(c);

test("renderTierPick draws three cards, the pick pressed, a disabled card with its reason", () => {
  const pg = tpPage({ profile: bound, cwd: "/home/u/repo" });
  pg.run("renderTierPick()");
  const cards = cardsOf(pg);
  assert.strictEqual(cards.length, 3);
  assert.deepStrictEqual(cards.map((c) => c.attrs["aria-pressed"]), ["true", "false", "false"]);
  assert.strictEqual(pg.pick.dataset.value, "box");
  const host = cards[2];
  assert.ok(hasClass(host, "tp-host"));
  assert.strictEqual(host.disabled, true);
  assert.strictEqual(host.attrs["aria-disabled"], "true");
  assert.ok(hasClass(host, "dis"));
  assert.strictEqual(host.attrs.type, "button");
  const why = host.querySelector(".tp-why");
  assert.ok(why);
  assert.strictEqual(why.textContent, "This profile has credential bindings (AWS sandbox, GitHub), so it runs only in a box.");
  assert.strictEqual(pg.pick.querySelectorAll(".tp-vm").length, 0);
  assert.ok(hasClass(cards[0], "on"));
  // the clone card is disabled too: this profile has no clone setup
  assert.strictEqual(cards[1].disabled, true);
  assert.strictEqual(cards[1].querySelector(".tp-why").textContent, "This profile has no clone setup (Settings ⚙ › profile › Workspace).");
});

test("renderTierPick draws the card text: title, default tag, rows with b and code, the icon and the radio", () => {
  const pg = tpPage();
  pg.run("renderTierPick()");
  assert.strictEqual(pg.pick.dataset.value, "clone", "a clone profile at its clone dir pre-selects the clone card");
  const [box, cl, host] = cardsOf(pg);
  assert.strictEqual(box.querySelector(".tp-n").textContent, "Box · in place");
  assert.strictEqual(cl.querySelector(".tp-n").textContent, "Box · cloneprofile default");
  assert.strictEqual(cl.querySelector(".tp-dflt").textContent, "profile default");
  assert.strictEqual(box.querySelectorAll(".tp-dflt").length, 0);
  assert.strictEqual(host.querySelector(".tp-n").textContent, "Host · not contained");
  for (const c of [box, cl, host]) {
    assert.strictEqual(c.querySelectorAll(".tp-rb").length, 1);
    const ic = c.querySelector(".tp-i");
    assert.ok(ic);
    const svg = ic.children[0];
    assert.strictEqual(svg.tag, "svg");
    assert.strictEqual(svg.attrs.viewBox, "0 0 24 24");
    assert.strictEqual(svg.attrs["stroke-width"], "2");
  }
  const ks = (c) => c.querySelectorAll(".tp-k").map((n) => n.textContent);
  const vs = (c) => c.querySelectorAll(".tp-v").map((n) => n.textContent);
  assert.deepStrictEqual(ks(cl), ["Your folder", "Its work", "Rest of machine"]);
  assert.deepStrictEqual(vs(cl), [
    "untouched. The agent works on its own copy of the repo.",
    "comes home as box/<branch> branches in your repo, for you to review and merge.",
    "hidden",
  ]);
  assert.strictEqual(cl.querySelector(".tp-v b").textContent, "untouched.");
  assert.strictEqual(cl.querySelector(".tp-v code").textContent, "box/<branch>");
  assert.deepStrictEqual(vs(box), [
    "edited live. Each change lands in ~/repo as the agent makes it, uncommitted work included.",
    "hidden",
  ]);
  assert.deepStrictEqual(vs(host), ["edited live.", "your whole account: every file, key and network this machine has."]);
  assert.strictEqual(host.disabled, false);
  assert.strictEqual(host.attrs["aria-disabled"], undefined);
  assert.strictEqual(typeof box.onclick, "function", "a card's pick is its onclick");
});

test("renderTierPick: a stopped VM adds the VM line on the two Box cards, and a redraw starts from scratch", () => {
  const pg = tpPage();
  pg.run("tierVmStopped = true; renderTierPick()");
  const vms = pg.pick.querySelectorAll(".tp-vm");
  assert.strictEqual(vms.length, 2);
  assert.strictEqual(vms[0].textContent, "Box VM is stopped; starting it takes about a minute.");
  pg.run("renderTierPick()");
  assert.strictEqual(pg.pick.querySelectorAll(".tp-k").length, 7, "2 + 3 + 2 rows");
  assert.strictEqual(pg.pick.querySelectorAll("button.tp").length, 3);
});

test("renderTierPick draws nothing without a box layer, and spawnWhere is {}", () => {
  const pg = tpPage({ boxRuntime: "" });
  pg.run("renderTierPick()");
  assert.strictEqual(pg.pick.children.length, 0);
  assert.deepStrictEqual(pg.run("spawnWhere()"), {});
  const none = tpPage({ boxRuntime: "", desktop: false });
  none.seg.dataset.value = "host";
  assert.deepStrictEqual(none.run("spawnWhere()"), {}, "no box layer: nothing about the tier is sent, on the phone too");
  const bare = tpPage({ $: () => null });
  assert.doesNotThrow(() => bare.run("renderTierPick()"));
});

test("renderTierPick without a box layer clears the cards and the value it left", () => {
  const pg = tpPage();
  pg.run("renderTierPick()");
  assert.strictEqual(pg.pick.dataset.value, "clone");
  assert.strictEqual(pg.pick.children.length, 3);
  pg.ctx.boxRuntime = "";
  pg.run("renderTierPick()");
  assert.strictEqual(pg.pick.dataset.value, "");
  assert.strictEqual(pg.pick.children.length, 0);
  assert.deepStrictEqual(pg.run("spawnWhere()"), {});
  const bare = tpPage({ boxRuntime: "", $: () => null });
  assert.doesNotThrow(() => bare.run("renderTierPick()"));
});

test("spawnWhere reads the cards on the desktop and the seg on the phone", () => {
  const pg = tpPage();
  pg.pick.dataset.value = "clone";
  assert.deepStrictEqual(pg.run("spawnWhere()"), { runtime: "box", clone: true });
  pg.pick.dataset.value = "host";
  assert.deepStrictEqual(pg.run("spawnWhere()"), { runtime: "host", clone: false });
  pg.ctx.desktop = false;
  pg.seg.dataset.value = "host";
  const w = pg.run("spawnWhere()");
  assert.deepStrictEqual(w, { runtime: "host" });
  assert.strictEqual("clone" in w, false, "the phone never sends clone");
  pg.seg.dataset.value = "";
  assert.deepStrictEqual(pg.run("spawnWhere()"), {});
});

test("picking in place at the clone dir moves the workspace to the repo", () => {
  const pg = tpPage();
  pg.run('tierPick("clone")');
  assert.strictEqual(pg.calls.filter((c) => c[0] === "setSpawnCwd").length, 0);
  assert.strictEqual(pg.calls.filter((c) => c[0] === "onSpawnCwdChanged").length, 0);
  pg.calls.length = 0;
  pg.run('tierPick("box")');
  assert.deepStrictEqual(pg.calls.filter((c) => c[0] === "setSpawnCwd"), [["setSpawnCwd", "/home/u/repo", ""]]);
  assert.strictEqual(pg.calls.filter((c) => c[0] === "onSpawnCwdChanged").length, 1);
  assert.strictEqual(pg.pick.dataset.value, "box");
  // not at the clone dir: the workspace stays
  const other = tpPage({ cwd: "/home/u/else" });
  other.run('tierPick("box")');
  assert.strictEqual(other.calls.filter((c) => c[0] === "setSpawnCwd").length, 0);
  // a profile with no clone_of: the same
  const plainPg = tpPage({ profile: plain, cwd: "/home/u/clones/repo" });
  plainPg.run('tierPick("host")');
  assert.strictEqual(plainPg.calls.filter((c) => c[0] === "setSpawnCwd").length, 0);
});

test("a card pick mirrors the seg and a seg pick moves the cards", () => {
  const pg = tpPage();
  pg.run('tierPick("clone")');
  assert.deepStrictEqual(pg.calls.filter((c) => c[0] === "setSegValue").pop(), ["setSegValue", "box"]);
  const seq = pg.calls.map((c) => c[0]);
  for (const n of ["updateRuntimeWarning", "syncSpawnCopyLine", "syncSpawnPreview"]) assert.ok(seq.includes(n), n);
  const h = tpPage({ profile: plain, cwd: "/home/u/else" });
  h.run('tierPick("host")');
  assert.deepStrictEqual(h.calls.filter((c) => c[0] === "setSegValue").pop(), ["setSegValue", "host"]);
  assert.strictEqual(h.pick.dataset.value, "host");
  h.calls.length = 0;
  h.run('tierSegChanged("box")');
  assert.strictEqual(h.pick.dataset.value, "box");
  h.run('tierSegChanged("host")');
  assert.strictEqual(h.pick.dataset.value, "host");
  assert.ok(h.calls.some((c) => c[0] === "updateRuntimeWarning"));
});

test("tierSegChanged: Host to Host, Box keeps a clone card", () => {
  const pg = tpPage({ profile: Object.assign({}, clone, { runtime: "box" }) });
  pg.run('tierPick("clone")');
  pg.run('tierSegChanged("box")');
  assert.strictEqual(pg.pick.dataset.value, "clone");
  pg.run('tierSegChanged("host")');
  assert.strictEqual(pg.pick.dataset.value, "host");
  pg.run('tierSegChanged("box")');
  assert.strictEqual(pg.pick.dataset.value, "box");
});

test("a duplicate keeps its card", () => {
  const pg = tpPage();
  pg.run('tierApplyRuntime("box", false)');
  assert.strictEqual(pg.pick.dataset.value, "box", "even with the cwd at the clone dir");
  pg.run('tierApplyRuntime("box", true)');
  assert.strictEqual(pg.pick.dataset.value, "clone");
  pg.run('tierApplyRuntime("host", false)');
  assert.strictEqual(pg.pick.dataset.value, "host");
  pg.run('tierApplyRuntime("", false)');
  assert.strictEqual(pg.pick.dataset.value, "clone", "nothing asked: the pre-selection");
  const b = tpPage({ profile: bound, cwd: "/home/u/repo" });
  b.run('tierApplyRuntime("host", false)');
  assert.strictEqual(b.pick.dataset.value, "box", "an unavailable card still falls back");
});

test("a duplicate repaints the account hint after its card is applied", () => {
  const pg = tpPage();
  pg.run('tierApplyRuntime("host", false)');
  const names = pg.calls.map((c) => c[0]);
  assert.ok(names.includes("paintSpawnAcctHint"), "tierApplyRuntime calls paintSpawnAcctHint");
  assert.strictEqual(pg.pick.dataset.value, "host", "the hint reads the card, so the card is drawn first");
});

test("a profile change forgets the pick", () => {
  const pg = tpPage({ profile: plain, cwd: "/home/u/else" });
  pg.run('tierPick("host")');
  assert.strictEqual(pg.pick.dataset.value, "host");
  pg.run("tierReset(); renderTierPick()");
  assert.strictEqual(pg.pick.dataset.value, "box");
  // syncRuntimeField, cut from app.js: buildSeg with tierSegChanged, then tierReset and renderTierPick, before updateRuntimeWarning
  const order = [];
  const ctx = vm.createContext({
    boxRuntime: "runc",
    $: (id) => ({ style: {}, id }),
    currentProfile: () => ({}), spawnRuntimeDefault: () => "box", runtimeSegChoices: () => [],
    buildSeg: (el, ch, v, cb) => { order.push("buildSeg"); ctx.cb = cb; },
    tierSegChanged: function tierSegChanged() {}, tierReset: () => order.push("tierReset"),
    renderTierPick: () => order.push("renderTierPick"), updateRuntimeWarning: () => order.push("updateRuntimeWarning"),
  });
  vm.runInContext(appFnSrc("syncRuntimeField"), ctx);
  vm.runInContext("syncRuntimeField()", ctx);
  assert.deepStrictEqual(order, ["buildSeg", "tierReset", "renderTierPick", "updateRuntimeWarning"]);
  assert.strictEqual(ctx.cb, ctx.tierSegChanged);
});

test("applySpawnPrefill chooses the duplicate's card right after the seg, and nothing after it resets the card", () => {
  const src = appFnSrc("applySpawnPrefill");
  assert.match(src, /setSegValue\(seg, pf\.runtime\);\s*tierApplyRuntime\(pf\.runtime, pf\.clone\);/);
  assert.doesNotMatch(src, /syncRuntimeField|tierReset/);
});

test("only spawnWhere reads #runtime-seg", () => {
  const hits = [];
  for (const f of fs.readdirSync(STATIC).filter((n) => n.endsWith(".js"))) {
    const src = readStatic(f);
    const re = /segValue\(\$\("runtime-seg"\)\)/g;
    for (let m; (m = re.exec(src)); ) hits.push([f, m.index]);
  }
  assert.strictEqual(hits.length, 1, JSON.stringify(hits));
  assert.strictEqual(hits[0][0], "tierpick.js");
  const src = readStatic("tierpick.js");
  const fnAt = src.indexOf("function spawnWhere");
  assert.ok(fnAt > 0 && hits[0][1] > fnAt);
  assert.ok(!src.slice(fnAt, hits[0][1]).includes("\n}\n"), "inside spawnWhere");
});

test("the submit sends spawnWhere and the clone dir only for a clone", () => {
  const src = readStatic("app.js");
  const at = src.indexOf('$("spawn").addEventListener("submit"');
  assert.ok(at > 0);
  const body = src.slice(at, src.indexOf("spawnSession(req", at));
  assert.match(body, /const w = spawnWhere\(\);/);
  const call = body.slice(body.indexOf("spawnPayload({"));
  assert.match(call, /runtime: w\.runtime \|\| "", clone: w\.clone/);
  assert.match(call, /cwd: spawnCwdFor\(w\)/);
  const pg = tpPage({ cwd: "/home/u/else" });
  assert.strictEqual(pg.run("spawnCwdFor({ runtime: 'box', clone: true })"), "/home/u/clones/repo");
  assert.strictEqual(pg.run("spawnCwdFor({ runtime: 'box', clone: false })"), "/home/u/else");
  assert.strictEqual(pg.run("spawnCwdFor({})"), "/home/u/else");
  const np = tpPage({ profile: plain, cwd: "/home/u/else" });
  assert.strictEqual(np.run("spawnCwdFor({ runtime: 'box', clone: true })"), "/home/u/else", "no default_cwd: the workspace");
});

test("syncSpawnCopyLine is hidden for an in place pick, on the desktop only", () => {
  assert.match(readStatic("spawnpanel.js"), /drawerDesktop\(\) && spawnWhere\(\)\.clone === false/);
});

test("tierpick.js loads after spawnpanel.js and before app.js; the cards are hidden at base", () => {
  const html = readStatic("index.html");
  const at = (s) => html.indexOf('<script src="' + s + '"></script>');
  assert.ok(at("/spawnpanel.js") > 0 && at("/tierpick.js") > at("/spawnpanel.js") && at("/app.js") > at("/tierpick.js"));
  assert.match(html, /<div class="tierpick" id="tierpick" role="radiogroup" aria-label="Where it runs" data-value=""><\/div>/);
  assert.match(html, /<label><span class="tp-lbl-ph">Containment<\/span><span class="tp-lbl">Where it runs<\/span><\/label>/);
  // a rule outside any @media: blank the media blocks by brace depth
  const css = readStatic("app.css");
  let flat = "", depth = 0, mediaAt = 0;
  for (let i = 0; i < css.length; i++) {
    const ch = css[i];
    if (ch === "{") {
      depth++;
      if (!mediaAt && /@media[^{}]*$/.test(css.slice(Math.max(0, i - 200), i))) mediaAt = depth;
    }
    if (!mediaAt) flat += ch;
    if (ch === "}") {
      if (mediaAt && depth === mediaAt) mediaAt = 0;
      depth--;
    }
  }
  assert.match(flat, /#tierpick,[^{}]*\{\s*display:\s*none;?\s*\}/);
});

test("in place at the clone dir moves the workspace without a click: clone unavailable, desktop only", () => {
  const over = { cloneUnavailable: "clone mode needs git 2.40" };
  const pg = tpPage(over);
  pg.run("renderTierPick()");
  assert.deepStrictEqual(pg.calls.filter((c) => c[0] === "setSpawnCwd"), [["setSpawnCwd", "/home/u/repo", ""]]);
  assert.strictEqual(pg.calls.filter((c) => c[0] === "onSpawnCwdChanged").length, 1);
  assert.strictEqual(pg.pick.dataset.value, "box");
  // the phone moves nothing
  const ph = tpPage(Object.assign({ desktop: false }, over));
  ph.run("renderTierPick()");
  assert.strictEqual(ph.calls.filter((c) => c[0] === "setSpawnCwd").length, 0);
  assert.strictEqual(ph.calls.filter((c) => c[0] === "onSpawnCwdChanged").length, 0);
  // the clone card showing: nothing to move
  const ok = tpPage();
  ok.run("renderTierPick()");
  assert.strictEqual(ok.calls.filter((c) => c[0] === "setSpawnCwd").length, 0);
});

test("typing in the workspace field is not moved out of the clone dir; a click and an unfocused render still move", () => {
  const over = { cloneUnavailable: "clone mode needs git 2.40" };
  const pg = tpPage(over);
  pg.doc.activeElement = pg.ws;
  pg.run("renderTierPick()");
  assert.strictEqual(pg.calls.filter((c) => c[0] === "setSpawnCwd").length, 0, "focused field: no passive move");
  assert.strictEqual(pg.pick.dataset.value, "box");
  // an explicit click still moves, focused or not
  pg.run('tierPick("box")');
  assert.deepStrictEqual(pg.calls.filter((c) => c[0] === "setSpawnCwd"), [["setSpawnCwd", "/home/u/repo", ""]]);
  // another element focused, or no activeElement at all (fakedom): the passive move runs
  const other = tpPage(over);
  other.doc.activeElement = other.pick;
  other.run("renderTierPick()");
  assert.strictEqual(other.calls.filter((c) => c[0] === "setSpawnCwd").length, 1);
  const none = tpPage(over);
  delete none.doc.activeElement;
  none.run("renderTierPick()");
  assert.strictEqual(none.calls.filter((c) => c[0] === "setSpawnCwd").length, 1, "undefined activeElement is guarded");
});

test("the move to the repo ends, and the redraw it triggers shows the card of the new folder", () => {
  // the real onSpawnCwdChanged redraws the cards; so does a clone_of that equals the clone dir must not loop
  const pg = tpPage({ cloneUnavailable: "clone mode needs git 2.40" });
  pg.ctx.onSpawnCwdChanged = () => { pg.calls.push(["onSpawnCwdChanged"]); pg.run("renderTierPick()"); };
  pg.run("renderTierPick()");
  assert.strictEqual(pg.calls.filter((c) => c[0] === "setSpawnCwd").length, 1);
  assert.strictEqual(pg.pick.dataset.value, "box");
  assert.strictEqual(pg.ctx.cwd, "/home/u/repo");
  assert.strictEqual(pg.pick.querySelectorAll("button.tp").length, 3, "one set of cards");
  const same = tpPage({ profile: { name: "c", clone_of: "/home/u/repo", default_cwd: "/home/u/repo" }, cwd: "/home/u/repo", cloneUnavailable: "no" });
  same.ctx.onSpawnCwdChanged = () => { same.calls.push(["onSpawnCwdChanged"]); same.run("renderTierPick()"); };
  same.ctx.setSpawnCwd = (p) => { same.calls.push(["setSpawnCwd", p]); };
  same.run("renderTierPick()");
  assert.strictEqual(same.calls.filter((c) => c[0] === "setSpawnCwd").length, 0, "a clone dir that is the repo has nothing to move, and does not loop");
});

test("a seg tap on Box keeps the clone card the cards showed", () => {
  const pg = tpPage();
  pg.run("renderTierPick()");
  assert.strictEqual(pg.pick.dataset.value, "clone", "pre-selected, nothing picked");
  pg.run('tierSegChanged("box")');
  assert.strictEqual(pg.pick.dataset.value, "clone");
  pg.run('tierSegChanged("host")');
  assert.strictEqual(pg.pick.dataset.value, "host");
});

test("on the desktop the seg follows the cards, so a resize submits what was shown", () => {
  const blocked = { name: "x", runtime: "host", host_block: "this profile has credential bindings (AWS sandbox), so it runs only in a box" };
  const pg = tpPage({ profile: blocked, cwd: "/home/u/else" });
  pg.seg.dataset.value = "host";
  pg.run("renderTierPick()");
  assert.deepStrictEqual(pg.calls.filter((c) => c[0] === "setSegValue"), [["setSegValue", "box"]]);
  assert.strictEqual(pg.seg.dataset.value, "box");
  assert.strictEqual(pg.pick.dataset.value, "box");
  // a seg already in step is left alone
  pg.calls.length = 0;
  pg.run("renderTierPick()");
  assert.strictEqual(pg.calls.filter((c) => c[0] === "setSegValue").length, 0);
  // crossing the breakpoint now submits Box
  pg.ctx.desktop = false;
  assert.deepStrictEqual(pg.run("spawnWhere()"), { runtime: "box" });
  // the phone: renderTierPick leaves the seg alone
  const ph = tpPage({ profile: blocked, cwd: "/home/u/else", desktop: false });
  ph.seg.dataset.value = "host";
  ph.run("renderTierPick()");
  assert.strictEqual(ph.calls.filter((c) => c[0] === "setSegValue").length, 0);
  assert.strictEqual(ph.seg.dataset.value, "host");
});

test("tier cards draw ICON_LOCKED tier glyphs", () => {
  const pg = tpPage({ profile: bound, cwd: "/home/u/repo" });
  pg.run("renderTierPick()");
  const cards = cardsOf(pg);
  assert.deepStrictEqual(cards.map((c) => c.querySelector(".tp-i").children[0].attrs["data-icon"]), ["box", "copy", "triangle-alert"]);
  for (const c of cards) {
    const svg = c.querySelector(".tp-i").children[0];
    assert.strictEqual(svg.attrs.width, "14");
    assert.ok(svg.attrs.class.split(" ").includes("ui-icon"));
  }
  // a disabled card's reason starts with the lock, at 12
  const why = cards[2].querySelector(".tp-why");
  assert.strictEqual(why.children[0].attrs["data-icon"], "lock");
  assert.strictEqual(why.children[0].attrs.width, "12");
  const src = readStatic("tierpick.js");
  for (const gone of ["TP_ICONS", "TP_LOCK_ICON", "tierIcon"]) assert.ok(!src.includes(gone), gone + " is gone");
});

test("claudemark.js exports only claudeKindOf", () => {
  const mod = require("../static/claudemark.js");
  assert.deepStrictEqual(Object.keys(mod), ["claudeKindOf"]);
  assert.ok(!readStatic("claudemark.js").includes("CLAUDE_MARK_D"), "the path lives in icons.js only");
  assert.ok(readStatic("claudemark.js").includes("docs/ICONS.md"), "the trademark note points to docs/ICONS.md");
  const ctx = vm.createContext({});
  vm.runInContext(readStatic("claudemark.js"), ctx);
  assert.strictEqual(vm.runInContext("claudeKindOf({ claude: true })", ctx), true);
  assert.strictEqual(vm.runInContext("claudeKindOf({ claude: 1 })", ctx), false);
  assert.strictEqual(vm.runInContext("claudeKindOf(null)", ctx), false);
});

test("claudemark.js loads between spawnpanel.js and tierpick.js", () => {
  const html = readStatic("index.html");
  const at = (s) => html.indexOf('<script src="' + s + '"></script>');
  assert.ok(at("/spawnpanel.js") > 0 && at("/claudemark.js") > at("/spawnpanel.js") && at("/tierpick.js") > at("/claudemark.js"));
});

// --- the account hint under the account row (Task 5) ---------------------------------------------------------------

function acctPage(over) {
  const pg = tpPage();
  const doc = pg.doc;
  const hint = doc.createElement("div");
  const row = doc.createElement("div");
  row.hidden = false;
  const sel = doc.createElement("select");
  sel.value = "default";
  const calls = pg.calls;
  const { spawnAcctHint } = require("../static/spawnpreview.js");
  const { acctChoices } = require("../static/accounts.js");
  const ctx = pg.ctx;
  Object.assign(ctx, {
    $: (id) => ({ "spawn-acct-hint": hint, "spawn-account-row": row, "spawn-account": sel, tierpick: pg.pick, "runtime-seg": pg.seg })[id] || null,
    accountsCache: [{ name: "default", default: true, login_state: "logged_in" }, { name: "claude-box", login_state: "token" }, { name: "work", login_state: "token_locked" }],
    spawnAcctHint, acctChoices,
    spawnWhere: () => ({ runtime: "box", clone: false }),
    profile: { name: "claude", box_claude_credential: "no" },
    showSpawnUnlock: (msg, opts) => calls.push(["showSpawnUnlock", msg, opts]),
    closeDialogFromUI: (id) => calls.push(["closeDialogFromUI", id]),
    openSettings: (tab) => calls.push(["openSettings", tab]),
  });
  Object.assign(ctx, over || {});
  vm.runInContext(appFnSrc("paintSpawnAcctHint"), ctx);
  return Object.assign(pg, { hint, row, sel });
}

test("paintSpawnAcctHint draws Use buttons that switch the account", () => {
  const pg = acctPage();
  pg.run("paintSpawnAcctHint()");
  assert.strictEqual(pg.hint.hidden, false);
  assert.ok(pg.hint.textContent.startsWith("default has no login inside a box."), pg.hint.textContent);
  const btns = pg.hint.querySelectorAll("button");
  assert.deepStrictEqual(btns.map((b) => b.textContent), ["Use: claude-box", "Use: work"]);
  assert.ok(btns.every((b) => hasClass(b, "btn") && hasClass(b, "btn-sm")));
  assert.strictEqual(pg.hint.querySelectorAll("code")[0].textContent, "default");
  btns[0].click();
  assert.strictEqual(pg.sel.value, "claude-box");
  assert.ok(pg.calls.some((c) => c[0] === "syncSpawnPreview"));
  assert.strictEqual(pg.hint.hidden, true, "claude-box is not the profile's own account: nothing to warn about");
});

test("paintSpawnAcctHint: no token account offers Settings, which closes the dialog like its data-close button", () => {
  const pg = acctPage({ accountsCache: [{ name: "default", default: true }] });
  pg.row.hidden = true; // one account: the select is not offered, the chosen account is the profile's own
  pg.run("paintSpawnAcctHint()");
  assert.ok(pg.hint.textContent.startsWith("default has no login inside a box."));
  const btns = pg.hint.querySelectorAll("button");
  assert.deepStrictEqual(btns.map((b) => b.textContent), ["Settings → Agent accounts"]);
  assert.ok(hasClass(btns[0], "link"));
  btns[0].click();
  assert.deepStrictEqual(pg.calls.filter((c) => c[0] === "closeDialogFromUI" || c[0] === "openSettings"), [["closeDialogFromUI", "spawn-modal"], ["openSettings", "accounts"]]);
});

test("paintSpawnAcctHint: the locked store says so, with Unlock; nothing on Host, a non-claude profile, a named account on default, or the phone", () => {
  let pg = acctPage({ profile: { name: "claude", box_claude_credential: "locked" } });
  pg.run("paintSpawnAcctHint()");
  assert.strictEqual(pg.hint.hidden, false);
  assert.ok(pg.hint.textContent.startsWith("This account needs the store unlocked."));
  const un = pg.hint.querySelectorAll("button");
  assert.deepStrictEqual(un.map((b) => b.textContent), ["Unlock"]);
  assert.ok(hasClass(un[0], "link"));
  un[0].click();
  assert.deepStrictEqual(JSON.parse(JSON.stringify(pg.calls.find((c) => c[0] === "showSpawnUnlock"))), ["showSpawnUnlock", "This account needs the store unlocked.", { retry: false }]);

  pg = acctPage({ spawnWhere: () => ({ runtime: "host", clone: false }) });
  pg.run("paintSpawnAcctHint()");
  assert.strictEqual(pg.hint.hidden, true, "Host");
  pg = acctPage({ profile: { name: "sh", box_claude_credential: "" } });
  pg.run("paintSpawnAcctHint()");
  assert.strictEqual(pg.hint.hidden, true, "non-claude");
  pg = acctPage({ profile: { name: "claude", account: "claude-box", box_claude_credential: "no" } });
  pg.sel.value = "default";
  pg.run("paintSpawnAcctHint()");
  assert.strictEqual(pg.hint.hidden, true, "a named-account profile switched to default");
  pg = acctPage({ desktop: false });
  pg.run("paintSpawnAcctHint()");
  assert.strictEqual(pg.hint.hidden, true, "the phone");
  assert.strictEqual(pg.hint.children.length, 0);
});

test("showSpawnUnlock with retry:false offers no Retry; the one-argument path is unchanged", () => {
  const src = appFnSrc("showSpawnUnlock");
  assert.match(src, /function showSpawnUnlock\(message, opts\)/);
  assert.match(src, /opts && opts\.retry === false/);
  assert.match(src, /fetchAccounts\(\)/);
  assert.match(src, /refreshProfilesQuiet\(\)/);
  assert.match(src, /paintSpawnAccount\(/);
  assert.match(src, /"Retry"/, "the 423 path still offers Retry");
  const app = readStatic("app.js");
  assert.strictEqual((app.match(/showSpawnUnlock\(e\.message\)/g) || []).length, 1, "the 423 caller still passes one argument");
  assert.strictEqual((app.match(/showSpawnUnlock\(/g) || []).length, 3, "the definition, the one 423 caller, and the Unlock hint");
});

test("the account hint is repainted by the cards, the seg, the account select and the account row", () => {
  const tp = readStatic("tierpick.js");
  assert.match(tp.match(/function tierPick\(key\)[\s\S]*?\n}/)[0], /paintSpawnAcctHint\(\)/);
  assert.match(tp.match(/function tierSegChanged\(v\)[\s\S]*?\n}/)[0], /paintSpawnAcctHint\(\)/);
  assert.match(appFnSrc("paintSpawnAccount"), /paintSpawnAcctHint\(\)/);
  assert.match(readStatic("app.js"), /\$\("spawn-account"\)\.addEventListener\("change", \(\) => \{ syncSpawnPreview\(\); paintSpawnAcctHint\(\); \}\)/);
});

test("syncSpawnAccount repaints the account hint after the fetch even when only an account's login_state changed", async () => {
  const names = (state) => [{ name: "default", default: true, login_state: "logged_in" }, { name: "work", login_state: state }];
  const pg = acctPage({
    profile: { name: "claude", account: "work", box_claude_credential: "yes" },
    accountsCache: names("token"),
    paintSpawnAccount: () => {}, // the select is not under test: the row stays hidden, so the chosen account is the profile's own
    fetchAccounts: async () => { pg.ctx.accountsCache = names("token_locked"); }, // same names, one login_state different
  });
  pg.row.hidden = true;
  vm.runInContext(appFnSrc("syncSpawnAccount"), pg.ctx);
  pg.run("paintSpawnAcctHint()");
  assert.strictEqual(pg.hint.hidden, true, "before the fetch the cache says token: nothing to warn about");
  pg.run("syncSpawnAccount()");
  await new Promise((r) => setImmediate(r));
  assert.strictEqual(pg.hint.hidden, false, "the fetch says token_locked: the hint must follow");
  assert.ok(pg.hint.textContent.startsWith("This account needs the store unlocked."));
  // and the reverse: unlocked elsewhere, the stale locked hint clears
  pg.ctx.fetchAccounts = async () => { pg.ctx.accountsCache = names("token"); };
  pg.run("syncSpawnAccount()");
  await new Promise((r) => setImmediate(r));
  assert.strictEqual(pg.hint.hidden, true);
});

// --- Task 6: the "+" prefill (cwd only) and the VM state ------------------------------------------------------------

test("tierVmFetch: stopped only on a stopped answer, for the latest call", async () => {
  const tick = () => new Promise((r) => setImmediate(r));
  const asked = [];
  let answer = async () => ({ state: "stopped" });
  const pg = tpPage({
    deadlineSignal: (ms) => ({ ms }),
    api: (p, o) => { asked.push([p, o.signal.ms]); return answer(); },
  });
  const renders = () => pg.calls.filter((c) => c[0] === "render").length;
  pg.ctx.renderTierPick = () => pg.calls.push(["render"]);
  const stopped = () => pg.run("tierVmStopped");
  const fetchVm = () => vm.runInContext("tierVmFetch()", pg.ctx); // pg.run would copy the promise into {}

  await fetchVm();
  assert.deepStrictEqual(asked[0], ["/api/vm-state", 35000]);
  assert.strictEqual(stopped(), true);
  assert.strictEqual(renders(), 1);

  answer = async () => ({ state: "" });
  await fetchVm();
  assert.strictEqual(stopped(), false);

  pg.run("tierVmStopped = true");
  const before = renders();
  answer = async () => { throw new Error("boom"); };
  await fetchVm(); // never throws
  assert.strictEqual(stopped(), false);
  assert.strictEqual(renders(), before, "a failed answer draws nothing");

  // two calls, the first answering last: the second's answer wins
  let late;
  answer = () => new Promise((r) => { late = r; });
  const first = fetchVm();
  answer = async () => ({ state: "" });
  const second = fetchVm();
  await second;
  late({ state: "stopped" });
  await first;
  await tick();
  assert.strictEqual(stopped(), false, "the stale answer is dropped");
  assert.strictEqual(renders(), before + 1, "only the latest answer drew (the second call's)");

  // no box layer: nothing asked
  const n = asked.length;
  pg.ctx.boxRuntime = "";
  await fetchVm();
  assert.strictEqual(asked.length, n);
  // the phone: nothing asked either, and an old "stopped" is cleared rather than kept on a guess (spec 3.5)
  pg.ctx.boxRuntime = "runc";
  pg.ctx.desktop = false;
  pg.run("tierVmStopped = true");
  await fetchVm();
  assert.strictEqual(asked.length, n);
  assert.strictEqual(stopped(), false);
  pg.run("tierVmStopped = true");
  pg.ctx.desktop = true;
  pg.ctx.boxRuntime = "";
  await fetchVm();
  assert.strictEqual(stopped(), false, "no box layer clears it too");
  assert.strictEqual(asked.length, n);
});

test("tierVmFetch: an exception from renderTierPick is not swallowed as a failed request", async () => {
  const pg = tpPage({ deadlineSignal: () => ({}), api: async () => ({ state: "stopped" }) });
  pg.ctx.renderTierPick = () => { throw new Error("draw bug"); };
  await assert.rejects(vm.runInContext("tierVmFetch()", pg.ctx), /draw bug/);
});

test("openSpawnDialog takes a cwd-only prefill without the missing-profile toast", () => {
  const body = appFnSrc("openSpawnDialog");
  const cwdOnly = body.indexOf("!prefill.agent");
  const toast = body.indexOf("showToast('Profile \"'");
  assert.ok(cwdOnly > 0, "a branch on !prefill.agent");
  assert.ok(cwdOnly < toast, "before the missing-profile toast");
  assert.match(body.slice(cwdOnly, toast), /setSpawnCwd\(prefill\.cwd, prefill\.source \|\| ""\)/);
  assert.ok(body.indexOf("spawnCwdChoice(") < cwdOnly, "after the spawnCwdChoice line");
  assert.match(body.slice(cwdOnly, toast), /typeof prefill\.cwd === "string" && prefill\.cwd !== ""\)\s*setSpawnCwd\(/, "no path, nothing applied: the chosen workspace stays");
  const sync = body.indexOf("syncRuntimeField();");
  assert.match(body.slice(sync, sync + 80), /^syncRuntimeField\(\);\s*\n\s*tierVmFetch\(\);/, "tierVmFetch follows syncRuntimeField, not awaited");
});

// #342: the critical-rm row's line and visibility follow the card shown, so every redraw of the cards ends by syncing it (a card pick, a
// seg change, a workspace move and the preselection all go through renderTierPick), with a box layer or without.
test("renderTierPick syncs the critical-rm row after the cards are drawn, with or without a box layer", () => {
  const t = tpPage();
  const before = t.ctx.critSyncs;
  t.run("renderTierPick()");
  assert.strictEqual(t.ctx.critSyncs, before + 1);
  const bare = tpPage({ boxRuntime: "" });
  bare.run("renderTierPick()");
  assert.strictEqual(bare.ctx.critSyncs, 1);
  const picked = tpPage();
  const n = picked.ctx.critSyncs;
  picked.run('tierPick("host")');
  assert.ok(picked.ctx.critSyncs > n, "a card pick syncs");
});

// --- the confirm before a host spawn that drops bindings (#359) ---------------------------------------------------

test("hostDropsConfirm with drops only asks with the drops as a bullet list, Cancel focused (#359)", async () => {
  const asked = [];
  const pg = tpPage({ askConfirm: async (o) => { asked.push(o); return true; } });
  const ok = await vm.runInContext('hostDropsConfirm("infra", ["context paths", "A setting"])', pg.ctx);
  assert.strictEqual(ok, true);
  const o = asked[0];
  assert.strictEqual(o.title, "Start on Host without its box settings?");
  assert.strictEqual(o.body, "infra will run with your user's full access to this machine. These do not apply on Host:");
  assert.strictEqual(o.hint, undefined);
  assert.ok(!o.notes || o.notes.length === 0, "no logins note");
  assert.strictEqual(o.confirmLabel, "Start on Host");
  assert.strictEqual(o.danger, true);
  assert.strictEqual(o.focusCancel, true);
  const nodes = Array.isArray(o.extra) ? o.extra : [o.extra];
  const ul = nodes[0];
  assert.strictEqual(ul.tag, "ul");
  assert.deepStrictEqual(ul.querySelectorAll("li").map((li) => li.textContent), ["context paths", "A setting"]);
});

test("hostDropsConfirm with a guardrail only says the bindings are the default credentials, not a limit (#360)", async () => {
  const asked = [];
  const pg = tpPage({ askConfirm: async (o) => { asked.push(o); return true; } });
  await vm.runInContext('hostDropsConfirm("infra", [], [], ["AWS binding", "GitHub binding"])', pg.ctx);
  const o = asked[0];
  assert.strictEqual(o.title, "Start on Host?");
  assert.strictEqual(o.body, "infra will run with your user's full access to this machine, not in a box.");
  assert.deepStrictEqual(JSON.parse(JSON.stringify(o.notes)), [[GUARD]]);
  assert.ok(!o.extra || (Array.isArray(o.extra) && o.extra.length === 0), "no drops list");
  assert.strictEqual(o.confirmLabel, "Start on Host");
  assert.strictEqual(o.danger, true);
  assert.strictEqual(o.focusCancel, true);
});

test("hostDropsConfirm with a guardrail and drops lists the drops and the guardrail line, then what Host still passes (#360)", async () => {
  const asked = [];
  const pg = tpPage({ askConfirm: async (o) => { asked.push(o); return true; } });
  await vm.runInContext('hostDropsConfirm("infra", ["context paths"], ["GH_TOKEN secret", "FOO env"], ["AWS binding"])', pg.ctx);
  const o = asked[0];
  assert.strictEqual(o.title, "Start on Host without its box settings?");
  assert.strictEqual(o.body, "infra will run with your user's full access to this machine. These do not apply on Host:");
  const ul = (Array.isArray(o.extra) ? o.extra : [o.extra])[0];
  assert.deepStrictEqual(ul.querySelectorAll("li").map((li) => li.textContent), ["context paths"]);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(o.notes)), [
    ["On Host: the AWS binding is the default credential, not a limit. The agent keeps your full access to this machine, including its own logins, and your shell's rc files can override the default."],
    ["Still passed to the agent on Host: GH_TOKEN secret and FOO env."],
  ]);
});

test("hostDropsConfirm names the secrets and env Host still passes", async () => {
  const asked = [];
  const pg = tpPage({ askConfirm: async (o) => { asked.push(o); return true; } });
  await vm.runInContext('hostDropsConfirm("infra", ["context paths"], ["GH_TOKEN secret"])', pg.ctx);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(asked[0].notes)), [["Still passed to the agent on Host: GH_TOKEN secret."]]);
});

test("hostDropsConfirm resolves what askConfirm resolves (#359)", async () => {
  const pg = tpPage({ askConfirm: async () => false });
  assert.strictEqual(await vm.runInContext('hostDropsConfirm("infra", ["context paths"])', pg.ctx), false);
});

test("the submit asks hostDropsConfirm only for a host spawn that drops or keeps bindings, and leaves without spawning on a No (#359, #360)", () => {
  const src = readStatic("app.js");
  const at = src.indexOf('$("spawn").addEventListener("submit"');
  assert.ok(at > 0);
  const body = src.slice(at, src.indexOf("spawnSession(req", at));
  const ask = body.indexOf("hostDropsConfirm(");
  assert.ok(ask > 0, "the handler asks");
  assert.ok(ask < body.indexOf("Starting…"), "before the busy state and the Starting label");
  assert.match(body, /w\.runtime === "host"/);
  assert.match(body, /tierHostDrops\(currentProfile\(\)\)/);
  assert.match(body, /tierHostGuardrail\(currentProfile\(\)\)/);
  assert.match(body, /\(hostDrops\.length \|\| hostGuardrail\.length\) &&\s*!\(await hostDropsConfirm\(.*\)\) return;/);
  assert.match(body, /hostDropsBindings/);
  const call = body.slice(body.indexOf("spawnPayload({"));
  assert.match(call, /hostDropsBindings: hostDrops\.length > 0 \|\| hostGuardrail\.length > 0/);
});

test("the Host card row and the Host confirm both say a shell's rc files can override the bindings (#360)", async () => {
  const limit = "your shell's rc files can override the default";
  for (const g of [["AWS binding"], ["AWS binding", "GitHub binding"]]) {
    assert.ok(tierGuardrailLine(g).includes(limit), "card line: " + tierGuardrailLine(g));
  }
  const asked = [];
  const pg = tpPage({ askConfirm: async (o) => { asked.push(o); return true; } });
  await vm.runInContext('hostDropsConfirm("infra", [], [], ["GitHub binding"])', pg.ctx);
  const notes = JSON.parse(JSON.stringify(asked[0].notes)).flat();
  assert.ok(notes.some((n) => n.includes(limit)), "confirm notes: " + JSON.stringify(notes));
});

// A git folder boxes only as a private copy (#371): Box · in place is off, and the clone card converts the profile.
const gitRepo = { repo: "/home/u/repo", worktree: false, inside: false };

test("a git folder: Box · in place is disabled with its reason; the clone card is enabled and converts, and is the pick", () => {
  const m = tierModel({ profile: plain, cwd: "/home/u/repo", home, picked: "", short, git: gitRepo });
  const [box, cl, h] = m.cards;
  assert.strictEqual(box.disabled, true);
  assert.strictEqual(box.why, "A git repo boxes as a private copy");
  assert.strictEqual(cl.disabled, false);
  assert.strictEqual(cl.convert, true);
  assert.strictEqual(h.disabled, false);
  assert.strictEqual(m.pick, "clone");
  assert.strictEqual(m.dflt, "clone");
});

test("a git folder: a picked Box · in place does not stand; Host does", () => {
  assert.strictEqual(tierModel({ profile: plain, cwd: "/home/u/repo", home, picked: "box", short, git: gitRepo }).pick, "clone");
  assert.strictEqual(tierModel({ profile: plain, cwd: "/home/u/repo", home, picked: "host", short, git: gitRepo }).pick, "host");
});

test("a git folder with a host profile opens on Host, not on a conversion", () => {
  assert.strictEqual(tierModel({ profile: hostp, cwd: "/home/u/repo", home, picked: "", short, git: gitRepo }).pick, "host");
});

test("a git folder whose profile already has its copy: the clone card is the pick and converts nothing", () => {
  const m = tierModel({ profile: clone, cwd: "/home/u/repo", home, picked: "", short, git: gitRepo });
  assert.strictEqual(m.cards[0].disabled, true);
  assert.strictEqual(m.cards[1].convert, false);
  assert.strictEqual(m.pick, "clone");
});

// #371 C1 (Playwright D2): Host is never chosen for the user. With both Box cards off, nothing is picked and Start waits with the
// reason; Host stays one deliberate click away.
test("a git folder and a clone card the server does not offer: no card is picked, Host is not, and the reason is given", () => {
  const m = tierModel({ profile: plain, cwd: "/home/u/repo", home, picked: "", short, git: gitRepo, cloneUnavailable: "clone mode needs git 2.40" });
  assert.strictEqual(m.cards[1].disabled, true);
  assert.strictEqual(m.pick, "");
  assert.strictEqual(m.dflt, "");
  assert.deepStrictEqual(m.cards.map((c) => c.on), [false, false, false]);
  assert.deepStrictEqual(m.cards.map((c) => c.dflt), [false, false, false], "no card reads \"profile default\"");
  assert.strictEqual(m.none, "A git repo boxes as a private copy, which is not available here (Clone mode needs git 2.40). Pick Host to run it outside a box.");
  assert.strictEqual(tierModel({ profile: plain, cwd: "/home/u/repo", home, picked: "host", short, git: gitRepo, cloneUnavailable: "x" }).pick, "host",
    "the user's own Host click stands");
  const blocked = tierModel({ profile: bound, cwd: "/home/u/repo", home, picked: "", short, git: gitRepo, cloneUnavailable: "no git" });
  assert.strictEqual(blocked.pick, "");
  assert.ok(!/Host/.test(blocked.none), "no Host offer when Host is blocked: " + blocked.none);
});

test("a profile with a copy for another repo: the clone card converts, and the dialog says what blocks it", () => {
  const other = { repo: "/home/u/other", worktree: false, inside: false };
  const m = tierModel({ profile: clone, cwd: "/home/u/other", home, picked: "", short, git: other });
  assert.strictEqual(m.cards[1].disabled, false);
  assert.strictEqual(m.cards[1].convert, true);
  assert.strictEqual(m.pick, "clone");
  assert.strictEqual(m.none, "");
  const { tierConvert } = require("../static/tierpick.js");
  assert.strictEqual(tierConvert({ profile: clone, cwd: "/home/u/other", home, git: other, pick: "clone" }), true);
  // Not a git folder: as before, the card is off with its reason.
  const plainDir = tierModel({ profile: clone, cwd: "/home/u/other", home, picked: "", short, git: null });
  assert.strictEqual(plainDir.cards[1].disabled, true);
  assert.strictEqual(plainDir.cards[1].why, "This profile's clone setup is for ~/repo.");
  assert.strictEqual(plainDir.pick, "box");
});

test("Host is preselected only for a profile saved as Host, git folder or not", () => {
  for (const git of [null, gitRepo]) {
    for (const p of [plain, clone, dropper, guarded]) {
      const m = tierModel({ profile: p, cwd: "/home/u/repo", home, picked: "", short, git, cloneUnavailable: "off" });
      assert.notStrictEqual(m.pick, "host", JSON.stringify({ p, git }));
    }
    assert.strictEqual(tierModel({ profile: hostp, cwd: "/home/u/repo", home, picked: "", short, git }).pick, "host");
  }
});

test("a folder that is not git: the cards are as they were", () => {
  const m = tierModel({ profile: plain, cwd: "/home/u/x", home, picked: "", short, git: null });
  assert.strictEqual(m.cards[0].disabled, false);
  assert.strictEqual(m.cards[0].why, "");
  assert.strictEqual(m.cards[1].convert, false);
  assert.strictEqual(m.pick, "box");
  assert.deepStrictEqual(tierModel({ profile: plain, cwd: "/home/u/x", home, picked: "", short }), m);
});

test("tierConvert: the clone pick on a git folder whose profile has no copy yet needs the conversion before Start", () => {
  const { tierConvert } = require("../static/tierpick.js");
  assert.strictEqual(tierConvert({ profile: plain, cwd: "/home/u/repo", home, git: gitRepo, pick: "clone" }), true);
  assert.strictEqual(tierConvert({ profile: plain, cwd: "/home/u/repo", home, git: gitRepo, pick: "host" }), false);
  assert.strictEqual(tierConvert({ profile: plain, cwd: "/home/u/x", home, git: null, pick: "clone" }), false);
  assert.strictEqual(tierConvert({ profile: clone, cwd: "/home/u/repo", home, git: gitRepo, pick: "clone" }), false);
});

// #371 C1 on the page: no card can be picked for a git folder (its private copy is not available here), so none is pressed, the
// request names no tier, and Start waits with the reason; Host is never pressed for the user.
test("renderTierPick: a git folder with no card to pick presses none, and tierBlock gives the reason Start waits", () => {
  let gates = 0;
  const pg = tpPage({
    profile: plain, cwd: "/home/u/repo", cloneUnavailable: "clone mode needs git 2.40",
    spawnWsCheck: { git_repo: "/home/u/repo", git_worktree: false, git_inside: false },
    syncSpawnGate: () => { gates++; },
  });
  pg.run("renderTierPick()");
  const cards = cardsOf(pg);
  assert.deepStrictEqual(cards.map((c) => c.attrs["aria-pressed"]), ["false", "false", "false"]);
  assert.strictEqual(pg.pick.dataset.value, "");
  assert.ok(!pg.calls.some((c) => c[0] === "setSegValue"), "the seg is left alone: " + JSON.stringify(pg.calls));
  assert.strictEqual(pg.run("tierBlock()"), "A git repo boxes as a private copy, which is not available here (Clone mode needs git 2.40). Pick Host to run it outside a box.");
  assert.ok(gates > 0, "the Start gate is redrawn with the cards");
  // The user's own Host click stands, and lifts the block.
  pg.run('tierPick("host")');
  assert.strictEqual(pg.pick.dataset.value, "host");
  assert.strictEqual(pg.run("tierBlock()"), "");
  // The phone never sees it.
  pg.ctx.desktop = false;
  pg.run('tierReset(); renderTierPick()');
  assert.strictEqual(pg.run("tierBlock()"), "");
});

test("tierPick on the clone card of a git folder whose profile copies another repo opens the conversion (Start)", () => {
  let submitted = 0;
  const form = { requestSubmit: () => { submitted++; } };
  const pg = tpPage({
    profile: clone, cwd: "/home/u/other",
    spawnWsCheck: { git_repo: "/home/u/other", git_worktree: false, git_inside: false },
  });
  const base = pg.ctx.$;
  pg.ctx.$ = (id) => (id === "spawn" ? form : base(id));
  pg.run('tierPick("clone")');
  assert.strictEqual(submitted, 1);
  assert.strictEqual(pg.run("tierConvertNow()"), true);
});
