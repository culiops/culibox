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
  const calls = [];
  const box = {
    console, document: doc, window: { HOME_DIR: "/home/u" },
    boxRuntime: "runc", cloneUnavailable: "", desktop: true, profile: clone, cwd: "/home/u/clones/repo",
    seg, pick, calls,
    $: (id) => (id === "tierpick" ? pick : id === "runtime-seg" ? seg : null),
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
  };
  Object.assign(box, over || {});
  const ctx = vm.createContext(box);
  vm.runInContext(readStatic("svgicon.js") + "\n" + readStatic("tierpick.js"), ctx);
  // An object a vm script returns has the vm's own prototypes, which deepStrictEqual refuses: hand back a plain copy.
  const run = (code) => {
    const r = vm.runInContext(code, ctx);
    return r && typeof r === "object" ? JSON.parse(JSON.stringify(r)) : r;
  };
  return { ctx, doc, pick, seg, calls, run };
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
    assert.strictEqual(svg.attrs.width, "14");
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
