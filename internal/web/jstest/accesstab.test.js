"use strict";
// accesstab.js: the Access view on the kit (UI plan 6, frames M6.3 and M6.4; #176), against the shared fake DOM (the
// repo has no jsdom). The page's real accessmodel.js, sessionrow.js, steerrail.js, elevation.js and elevbanner.js run
// beside it, so the rows are the model's own; app.js and the pane (tabs.js) are stand-ins. Fixtures use made-up ids
// (402100009917, culipulse-prod, agent-ops-read), never a real account.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const MIN = 60000;
const iso = (min) => new Date(Date.now() + min * MIN + 30000).toISOString(); // +30 s: floor() reads exactly `min`
const ago = (min) => new Date(Date.now() - min * MIN).toISOString();
const tick = () => new Promise((r) => setImmediate(r));

const BASE = "arn:aws:iam::402100009917:role/agent-ops-read";
const WRITE = "arn:aws:iam::402100009917:role/agent-ops-write";
const conn = (over) => Object.assign({
  name: "culipulse-prod", label: "culipulse-prod", account_id: "402100009917", role_arn: BASE, region: "us-east-1",
  prod: true, write_capable: true, base_access: "none_found", base_checked_at: ago(120),
  elevate: { role_arn: WRITE, max_minutes: 60, allow_iam: false },
}, over);
const claudeBox = (over) => Object.assign({
  id: "s1", agent: "claude", runtime: "box", cwd: "/home/u/culipulse", account: "",
  created: new Date(Date.now() - (2 * 86400 + 5 * 3600) * 1000).toISOString(),
  aws_connections: [conn()], github: { owners: ["culiops"] },
}, over);
const open = (min, over) => claudeBox(Object.assign({ aws_connections: [conn({ elevated_until: iso(min) })] }, over));
const status = (over) => Object.assign({
  stats: { mints: 2, last_mint: ago(30) },
  connections: [{ name: "culipulse-prod", stats: { mints: 2, last_mint: ago(30) } }],
  recent: [], approvals: [], elevate: { host_sessions: [] },
  egress: { mode: "allowlist", allowed: 214, denied: 3 },
  sso: { sources: [{ expired: false, names: ["culipulse-prod"] }] },
}, over);
const granted = (over) => Object.assign({ ts: Date.now() - 2 * MIN, action: "granted", minutes: 30, name: "culipulse-prod", reason: "rotate KMS key" }, over);

// One page: the scripts the view leans on, in a shared global scope like the browser.
function page() {
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  doc.body = doc.createElement("body");
  const box = {
    document: doc, window: { innerWidth: 1280 }, console, Date, JSON, Map, Set, Math, Promise,
    lastSessions: [], paneViewRenderers: {}, boxRuntime: "runc",
    terminalTabFor: () => null,
    shortPath: (p) => (String(p).startsWith("/home/u/") ? "~" + String(p).slice(7) : p),
    mobileActive: () => false, positionDrawer() {},
    audits: [], grants: [], toasts: [],
    openAudit: (o) => box.audits.push({ ...o }),
    showToast: (m, o) => box.toasts.push([m, !!(o && o.error)]),
    refreshSessions() {},
    ssoStarting: new Set(), ssoModel: () => ({}),
    renderSSOBody: () => { const d = doc.createElement("div"); d.className = "ssocard"; return d; },
    fetch: async () => ({ status: 204 }),
  };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["duration.js", "tier.js", "sessionrow.js", "steerrail.js", "elevation.js", "accessmodel.js", "accesstab.js", "elevbanner.js"]) {
    new vm.Script(read(f), { filename: f }).runInContext(ctx);
  }
  box.elevGrant = async (sid, o) => { box.grants.push({ sid, ...o }); return null; };
  return { box, doc, host: doc.createElement("div"), lex: (name) => vm.runInContext(name, ctx) };
}

// A mounted, loaded view for session `s` whose /aws status is `aws` (null: the 204 of an unbound session).
async function mount(s, aws, extra) {
  const p = page();
  Object.assign(p.box, extra || {});
  p.box.lastSessions = [s];
  p.box.fetch = async (url) => (/\/aws$/.test(url) && aws ? { status: 200, json: async () => aws } : { status: 204 });
  p.handle = p.box.paneViewRenderers.access(p.host, s.id);
  await tick();
  return p;
}

const classes = (n) => n.className.split(/\s+/).filter(Boolean);
const has = (n, ...cs) => cs.every((c) => classes(n).includes(c));
const buttons = (n) => n.querySelectorAll("button");
const btn = (n, text) => buttons(n).find((b) => b.textContent === text);
// What Chromium does a couple of frames after the focused control becomes :disabled (focus fixup): focus falls to
// <body>. An aria-disabled control keeps it. Call after anything that could have disabled a button.
const settle = (doc) => { const a = doc.activeElement; if (a && a.disabled) doc.activeElement = doc.body; };

// ---- M6.3: containment ------------------------------------------------------------------------------

test("containment is a card with four cells (Tier, Workspace, Network, Agent account) and Started / Model rows", async () => {
  const { host } = await mount(claudeBox(), status(), { terminalTabFor: () => ({ usage: () => ({ model: "opus-5.5", effort: "high" }) }) });
  const card = host.querySelector("section.ac-cont.card");
  assert.ok(card, "section.card.ac-cont");
  const [head, cells, kvs] = card.children;
  assert.strictEqual(head.className, "card-head");
  assert.deepStrictEqual(head.children.map((c) => [c.tag, c.className, c.textContent]), [
    ["h2", "t-md w-600", "Containment"], ["span", "ph-spacer", ""], ["span", "mono t-sm muted", "box · runc"],
  ]);

  assert.strictEqual(cells.className, "ac-cells");
  assert.strictEqual(cells.children.length, 4);
  assert.ok(cells.children.every((c) => has(c, "ac-cell") && c.children.length === 2));
  assert.ok(cells.children.every((c) => c.children[0].tag === "span" && c.children[0].className === "t-sm muted"), "the label is span.t-sm.muted");
  assert.ok(cells.children.every((c) => c.children[1].tag === "span" && c.children[1].className === "t-md"), "the value is span.t-md");
  assert.deepStrictEqual(cells.children.map((c) => [c.children[0].textContent, c.children[1].textContent]), [
    ["Tier", "box"], ["Workspace", "~/culipulse"], ["Network", "allowlist · 214 allowed · 3 denied"], ["Agent account", "default"],
  ]);
  const chip = cells.children[0].children[1].children[0];
  assert.ok(has(chip, "chip", "chip-box", "chip-fg"), "Tier is a box chip whose word is the value");
  const ws = cells.children[1].children[1].children[0];
  assert.ok(has(ws, "mono", "t-sm"), "the workspace path is mono t-sm");
  assert.strictEqual(cells.children[1].children[1].title, "/home/u/culipulse", "the full path rides in the title");
  assert.strictEqual(cells.children[2].children[1].children.length, 0, "Network is plain text");
  assert.strictEqual(cells.children[3].children[1].children.length, 0, "Agent account is plain text");

  assert.strictEqual(kvs.className, "ac-kvs");
  assert.deepStrictEqual(kvs.children.map((c) => [c.tag, c.className]), [
    ["span", "t-sm muted"], ["span", "t-md tnum"], ["span", "t-sm muted"], ["span", "t-md"],
  ], "label and value are siblings in one grid");
  assert.strictEqual(kvs.children[0].textContent, "Started");
  assert.match(kvs.children[1].textContent, /^\d\d:\d\d · 2d 5h ago$/);
  assert.strictEqual(kvs.children[2].textContent, "Model");
  assert.strictEqual(kvs.children[3].textContent, "opus-5.5 · high");
  assert.ok(has(kvs.children[3].querySelector("span"), "mono", "t-sm"), "the model name is mono, its effort is sans");
  assert.strictEqual(kvs.children[3].querySelector("span").textContent, "opus-5.5");
});

test("a session with no usage reading draws only the Started row, and one with no start time draws no kvs at all", async () => {
  const a = await mount(claudeBox(), status());
  assert.deepStrictEqual(a.host.querySelector(".ac-kvs").children.map((c) => c.textContent).filter((t) => t === "Started" || t === "Model"), ["Started"]);
  const b = await mount(claudeBox({ created: undefined }), status());
  assert.strictEqual(b.host.querySelector(".ac-kvs"), null);
});

test("the product's own containment facts stay: readable paths, clone, left out of the box, in the same label / value grid", async () => {
  const s = claudeBox({ context_paths: ["/home/u/notes", "/home/u/ref"], clone_of: "/home/u/src", bundle_drops: [{ kind: "settings", name: "*", reason: "not copied" }] });
  const { host } = await mount(s, status());
  const kvs = host.querySelector(".ac-kvs");
  const labels = kvs.children.filter((c) => c.className === "t-sm muted").map((c) => c.textContent);
  assert.deepStrictEqual(labels, ["Started", "Readable host paths", "Cloned from", "Left out of the box"]);
  assert.strictEqual(kvs.children.length, labels.length * 2, "every label has one value");
  const paths = kvs.children[3];
  assert.deepStrictEqual(paths.querySelectorAll("div").map((d) => d.textContent), ["/home/u/notes", "/home/u/ref"]);
  assert.ok(paths.querySelectorAll("div").every((d) => has(d, "mono", "t-sm")));
});

test("a host session says so: a host chip and the line that it is not contained", async () => {
  const { host } = await mount({ id: "s1", agent: "bash", runtime: "host", cwd: "/home/u/w" }, null);
  const tier = host.querySelector(".ac-cell");
  assert.strictEqual(tier.children[0].textContent, "Tier");
  assert.ok(has(tier.querySelector(".chip"), "chip-host", "chip-fg"));
  assert.strictEqual(tier.querySelector(".chip").textContent, "host");
  assert.match(tier.textContent, /not contained/);
  assert.ok(!has(tier.querySelector(".chip"), "chip-box"));
});

test("a host session with no credentials draws only the containment card", async () => {
  const { host } = await mount({ id: "s1", agent: "bash", runtime: "host", cwd: "/home/u/w" }, null);
  const cards = host.querySelectorAll("section");
  assert.strictEqual(cards.length, 1);
  assert.ok(has(cards[0], "card", "ac-cont"));
  assert.strictEqual(host.querySelector(".ac-creds"), null);
  assert.strictEqual(host.querySelector(".ac-events"), null);
  assert.strictEqual(host.querySelector("table"), null);
});

test("a session that left the list says so and draws no card", async () => {
  const { host, box, handle } = await mount(claudeBox(), status());
  box.lastSessions = [];
  handle.update();
  assert.strictEqual(host.querySelectorAll("section").length, 0);
  assert.match(host.textContent, /This session is no longer listed\./);
});

// ---- M6.3 / M6.4: cloud credentials -----------------------------------------------------------------

test("cloud credentials: a card of connection rows (name + PROD, sub-line, Scope, SSO, Last credential, action) and the base-role note", async () => {
  const { host } = await mount(claudeBox(), status());
  const card = host.querySelector("section.ac-creds.card");
  assert.ok(card, "section.card.ac-creds");
  const head = card.children[0];
  assert.deepStrictEqual(head.children.map((c) => [c.tag, c.className, c.textContent]), [
    ["h2", "t-md w-600", "Cloud credentials"], ["span", "ph-spacer", ""], ["span", "t-sm muted tnum", "2 mints · last " + new Date(ago(30)).toTimeString().slice(0, 5)],
  ]);
  const blocks = card.querySelectorAll(".ac-blk");
  assert.strictEqual(blocks.length, 2, "the AWS connection (PROD first), then GitHub");
  const [aws, gh] = blocks;

  const row = aws.children[0];
  assert.strictEqual(row.className, "ac-conn");
  assert.strictEqual(row.children.length, 5, "four cells and the actions");
  assert.deepStrictEqual(row.children.slice(0, 4).map((c) => c.className), ["ac-cell", "ac-cell", "ac-cell", "ac-cell"]);
  assert.strictEqual(row.children[4].className, "ac-acts");
  const id = row.children[0];
  const name = id.querySelector(".ac-name");
  assert.deepStrictEqual(name.children.map((c) => [c.tag, c.textContent]), [["span", "culipulse-prod"], ["span", "PROD"]]);
  assert.ok(has(name.children[0], "mono", "t-md", "w-500", "ellipsis"));
  assert.strictEqual(name.children[0].title, "culipulse-prod");
  assert.ok(has(name.children[1], "chip", "chip-prod"));
  const sub = id.children[1];
  assert.ok(has(sub, "t-sm", "muted", "ellipsis"));
  assert.strictEqual(sub.textContent, "account 402100009917 · role agent-ops-read · us-east-1");
  assert.strictEqual(sub.title, sub.textContent);

  const lv = (c) => [c.children[0].textContent, c.children[1].textContent];
  assert.deepStrictEqual(row.children.slice(1, 4).map(lv), [["Scope", "read"], ["SSO", "valid"], ["Last credential", new Date(ago(30)).toTimeString().slice(0, 5)]]);
  assert.strictEqual(row.children[1].children[1].children.length, 0, "a read scope is plain fg text, no tone span");
  assert.ok(has(row.children[2].children[1].children[0], "c-ok"), "valid is green");
  assert.ok(has(row.children[3].children[1].children[0], "tnum"));
  assert.deepStrictEqual(buttons(row.children[4]).map((b) => b.textContent), ["Elevate…"]);
  assert.ok(has(buttons(row)[0], "btn", "btn-sm") && !has(buttons(row)[0], "btn-danger") && !has(buttons(row)[0], "btn-primary"), "Elevate… is the plain secondary button");

  const note = aws.children[1];
  assert.strictEqual(note.className, "ac-connnote");
  assert.strictEqual(note.children[0].textContent, "Base role: no broad writes found");
  assert.ok(has(note.children[0], "t-sm", "muted"));
  assert.match(note.children[0].title, /^checked \d\d:\d\d · scoped write grants are not detected$/);

  // GitHub: Scope —, Status ok (green), Last token, an empty actions cell, and no base-role note.
  const g = gh.children[0];
  assert.strictEqual(g.children.length, 5);
  assert.strictEqual(g.querySelector(".ac-name").children.length, 1, "no PROD chip");
  assert.strictEqual(g.children[0].children[1].textContent, "owners: culiops");
  assert.deepStrictEqual(g.children.slice(1, 4).map((c) => c.children[0].textContent), ["Scope", "Status", "Last token"]);
  assert.strictEqual(g.children[1].children[1].textContent, "—");
  assert.strictEqual(g.children[4].children.length, 0, "an empty actions cell keeps the grid");
  assert.strictEqual(gh.children.length, 1);
});

test("an elevated connection row shows \"write (elevated)\" in red and no Drop; the pane has exactly one Drop control (the banner's)", async () => {
  const s = open(12);
  const p = await mount(s, status({ approvals: [granted()] }));
  const row = p.host.querySelector(".ac-conn");
  const scope = row.children[1];
  assert.strictEqual(scope.children[0].textContent, "Scope");
  const val = scope.children[1].children[0];
  assert.strictEqual(val.textContent, "write (elevated)");
  assert.ok(has(val, "c-red", "w-500"), "red 500");
  assert.match(row.children[0].children[1].textContent, /role agent-ops-write/);
  assert.strictEqual(p.host.querySelector(".ac-connnote"), null, "the base-role note is absent while the window is open");
  assert.deepStrictEqual(buttons(row).map((b) => b.textContent), ["Extend"], "under EXTEND_UNDER the row offers Extend, never Drop");

  const drops = (n) => buttons(n).filter((b) => /^drop/i.test(b.textContent));
  assert.deepStrictEqual(drops(p.host).map((b) => b.textContent), [], "the Access view draws no Drop control of its own");

  // The pane: header, the banner, the view. elevbanner.js is the page's own.
  const pane = p.doc.createElement("div");
  const bar = p.doc.createElement("div");
  bar.className = "cell-bar";
  pane.append(bar, p.host);
  const cell = { bar, el: pane };
  p.box.paneElevSync(cell, s);
  assert.deepStrictEqual(drops(pane).map((b) => b.textContent), ["Drop now"], "one Drop in the pane");
  assert.ok(cell._elev.contains(drops(pane)[0]), "and it is the banner's");
  p.handle.update();
  assert.strictEqual(drops(pane).length, 1, "still one after the view repaints");
});

// G3-D1 / I1 end to end, with the page's real accLoad and elevbanner.js: a window that ends elsewhere leaves the list
// first, the cached status still says open, and the banner (drawn from the list alone) goes at that sync; the forced
// read that lands later re-syncs the panes and asks for nothing more.
test("accLoad answers once the read is stored, so an early-ended window's banner is already gone and the read lands quietly (G3-D1, I1)", async () => {
  const p = page();
  const { box, doc } = p;
  const s = open(23);
  const live = () => status({ approvals: [granted()], elevation: { active: true, name: "culipulse-prod", until: s.aws_connections[0].elevated_until } });
  const over = () => status({ approvals: [granted(), { ts: Date.now(), action: "dropped", name: "culipulse-prod", reason: "" }], elevation: { active: false, name: "culipulse-prod" } });
  let reply = live();
  const urls = [];
  box.fetch = async (url) => { urls.push(url); return /\/aws$/.test(url) ? { status: 200, json: async () => reply } : { status: 204 }; };
  const bar = doc.createElement("div");
  bar.className = "cell-bar";
  const pane = doc.createElement("div");
  pane.append(bar);
  const cell = { bar, el: pane };
  cell.syncElev = () => box.paneElevSync(cell, box.lastSessions.find((x) => x.id === "s1") || null);
  box.cells = [cell];

  box.lastSessions = [s];
  cell.syncElev();
  await tick();
  assert.ok(cell._elev, "the window is open: a banner");
  assert.strictEqual(urls.filter((u) => /\/aws$/.test(u)).length, 1, "one forced read for the new window");

  // Dropped elsewhere: the next list has no window; the cached status is still the old one.
  reply = over();
  box.lastSessions = [claudeBox()];
  cell.syncElev();
  assert.strictEqual(cell._elev, null, "the banner goes at the sync that drops the window: the cached status keeps nothing up");
  await tick();
  assert.strictEqual(cell._elev, null, "and the forced read landing changes nothing");
  assert.strictEqual(urls.filter((u) => /\/aws$/.test(u)).length, 2, "one read for the change, none for the re-sync");
});

// I1, the failure path with the page's real accLoad: the follow-up read answers 500, accLoadDone keeps the old status
// (which says open), and no later poll asks again. The banner must already be gone.
test("a window that ends elsewhere while the real accLoad's follow-up read fails: no stale banner (I1)", async () => {
  const p = page();
  const { box, doc } = p;
  const s = open(23);
  let fail = false;
  box.fetch = async (url) => /\/aws$/.test(url)
    ? (fail ? { status: 500 } : { status: 200, json: async () => status({ approvals: [granted()], elevation: { active: true, name: "culipulse-prod", until: s.aws_connections[0].elevated_until } }) })
    : { status: 204 };
  const bar = doc.createElement("div");
  bar.className = "cell-bar";
  const pane = doc.createElement("div");
  pane.append(bar);
  const cell = { bar, el: pane };
  cell.syncElev = () => box.paneElevSync(cell, box.lastSessions.find((x) => x.id === "s1") || null);
  box.cells = [cell];
  box.lastSessions = [s];
  cell.syncElev();
  await tick();
  assert.match(cell._elev.children[1].textContent, /“rotate KMS key”$/, "the first read landed: the reason");
  fail = true;
  box.lastSessions = [claudeBox()]; // dropped elsewhere
  cell.syncElev();
  assert.strictEqual(cell._elev, null, "gone at that sync");
  await tick();
  assert.strictEqual(p.lex("accData").get("s1").failed, true, "the follow-up read failed and kept the stale status");
  assert.strictEqual(p.lex("accData").get("s1").aws.elevation.active, true);
  for (let i = 0; i < 4; i++) { cell.syncElev(); await tick(); }
  assert.strictEqual(cell._elev, null, "no later poll brings it back");
});

// F5 with the page's real accPrune: elevbanner's poll forgets what an Access view's update() would have.
test("a banner poll forgets the cached status of a session that left the list, with no Access view mounted (F5)", () => {
  const { box, doc, lex } = page();
  const data = lex("accData");
  const entry = () => ({ aws: null, egress: null, at: Date.now(), inflight: false, loaded: true, failed: false });
  data.set("gone", entry());
  data.set("s1", entry());
  lex("accDrafts").set("gone", { minutes: 30 });
  const bar = doc.createElement("div");
  bar.className = "cell-bar";
  const cell = { bar, el: doc.createElement("div") };
  cell.el.append(bar);
  box.lastSessions = [];
  box.paneElevSync(cell, null);
  assert.strictEqual(data.has("gone"), true, "the page before its first poll forgets nothing");
  box.lastSessions = [claudeBox()];
  box.paneElevSync(cell, claudeBox());
  assert.strictEqual(data.has("gone"), false, "a session that left has no cached status");
  assert.strictEqual(lex("accDrafts").has("gone"), false, "nor draft");
  assert.strictEqual(data.has("s1"), true, "a listed session keeps it");
});

test("accLoad returns a promise that settles after the status is stored and the Access view repainted", async () => {
  const { box, lex } = page();
  box.lastSessions = [open(12)];
  let painted = 0;
  lex("accViews").set("s1", () => { painted++; });
  box.fetch = async (url) => (/\/aws$/.test(url) ? { status: 200, json: async () => status() } : { status: 204 });
  const r = box.accLoad("s1", true);
  assert.strictEqual(typeof r.then, "function");
  assert.strictEqual(lex("accData").get("s1").inflight, true);
  await r;
  const d = lex("accData").get("s1");
  assert.strictEqual(d.inflight, false);
  assert.strictEqual(d.loaded, true);
  assert.strictEqual(painted, 1, "the view repaints before the promise settles");
  const again = box.accLoad("s1", true);
  assert.strictEqual(typeof again.then, "function", "a forced read after one has settled starts another");
  assert.strictEqual(box.accLoad("s1", true), undefined, "a read while one is in flight is dropped");
  await again;
});

test("a window with plenty of time left offers no row action at all (Extend appears under 20 minutes)", async () => {
  const { host } = await mount(open(23), status({ approvals: [granted()] }));
  assert.deepStrictEqual(buttons(host.querySelector(".ac-conn")).map((b) => b.textContent), []);
  const soon = await mount(open(19), status({ approvals: [granted()] }));
  assert.deepStrictEqual(buttons(soon.host.querySelector(".ac-conn")).map((b) => b.textContent), ["Extend"]);
});

test("the lapse and leftover lines render under the connection's sub-line, the leftover in red", async () => {
  const aws = status({
    approvals: [{ ts: Date.now() - 3 * MIN, action: "expired", name: "culipulse-prod", reason: "" }],
    elevation: { active: false, name: "culipulse-prod", last_valid: iso(10) },
  });
  const { host } = await mount(claudeBox(), aws);
  const id = host.querySelector(".ac-conn").children[0];
  const notes = id.querySelectorAll(".ac-note");
  assert.strictEqual(notes.length, 2);
  const subAt = id.children.findIndex((c) => has(c, "ellipsis") && has(c, "muted"));
  assert.ok(subAt >= 0, "the sub-line");
  const after = id.children.slice(subAt + 1);
  assert.ok(after.length === notes.length && after.every((n, i) => n === notes[i]), "the two lines follow the sub-line, in the same cell");
  const [ended, leftover] = notes;
  assert.match(ended.textContent, /^Write access ended (\d{1,2} \w{3} )?\d\d:\d\d$/);
  assert.ok(has(ended, "t-sm", "muted") && !has(ended, "c-red"), "the lapse is muted: it is over");
  assert.match(leftover.textContent, /^Credentials already issued stay valid until \d\d:\d\d$/);
  assert.ok(has(leftover, "t-sm", "c-red"), "the leftover is red: live write credentials are write");
  assert.strictEqual(host.querySelector(".ac-conn").children[1].textContent, "Scoperead");
});

test("the lapse alone is one muted line; an open window or a plain session has none", async () => {
  const ended = await mount(claudeBox(), status({ approvals: [{ ts: Date.now() - 3 * MIN, action: "dropped", name: "culipulse-prod", reason: "" }] }));
  assert.deepStrictEqual(ended.host.querySelectorAll(".ac-note").map((n) => classes(n).includes("muted")), [true]);
  const live = await mount(open(25), status({
    approvals: [{ ts: Date.now() - 30 * MIN, action: "expired", name: "culipulse-prod", reason: "" }, granted()],
    elevation: { active: true, name: "culipulse-prod", until: iso(25), last_valid: iso(25) },
  }));
  assert.strictEqual(live.host.querySelector(".ac-note"), null, "an open window is the whole story");
  const plain = await mount(claudeBox(), status());
  assert.strictEqual(plain.host.querySelector(".ac-note"), null);
});

test("SSO expired: the cell says so in red (an error, never amber) and the sign-in card hangs under its connection", async () => {
  const s = claudeBox({ need: "sso", aws_connections: [conn({ sso_expired: true })] });
  const { host } = await mount(s, status({ sso: { sources: [{ expired: true, names: ["culipulse-prod"] }] } }));
  const row = host.querySelector(".ac-conn");
  const val = row.children[2].children[1].children[0];
  assert.strictEqual(val.textContent, "expired");
  assert.ok(has(val, "c-red"), "sso_expired is in the plan's error class");
  assert.ok(!has(val, "c-amber"), "amber is only for an agent blocked on the user (F21)");
  const blk = host.querySelector(".ac-blk");
  const sso = blk.querySelector(".ac-sso");
  assert.ok(sso && sso.querySelector(".ssocard"), "the card is renderSSOBody's");
  assert.ok(blk.children.indexOf(sso) > blk.children.indexOf(row));
});

test("a sign-in status that is neither valid nor expired stays neutral text, and nothing in the Access view is amber", async () => {
  // The model names only valid / expired / unknown ("" for static keys or a status not read yet); an unknown one is a dash.
  const { host } = await mount(claudeBox({ aws_connections: [conn()] }), status({ sso: { sources: [] } }));
  const sso = host.querySelector(".ac-conn").children[2];
  assert.strictEqual(sso.children[0].textContent, "SSO");
  assert.strictEqual(sso.children[1].textContent, "—");
  assert.strictEqual(sso.children[1].children.length, 0, "plain fg text, no tone span");
  const src = read("accesstab.js").replace(/\/\/.*$/gm, "");
  assert.doesNotMatch(src, /amber/, "the view applies no amber class");
  assert.doesNotMatch(accessCss(), /amber/, "and its rules name none");
});

test("a broker error on a connection is one red line under its row", async () => {
  const aws = status({ connections: [{ name: "culipulse-prod", stats: { mints: 2, last_mint: ago(30), last_error: "sts: access denied" } }] });
  const { host } = await mount(claudeBox(), aws);
  const notes = host.querySelector(".ac-blk").querySelectorAll(".ac-connnote");
  assert.deepStrictEqual(notes.map((n) => n.textContent), ["Base role: no broad writes found", "sts: access denied"]);
  assert.ok(has(notes[1].children[0], "t-sm", "c-red"));
});

// ---- M6.3 / M6.4: recent credential events -----------------------------------------------------------

// Six rows, newest first: an elevated mint, the grant that opened the window, a GitHub mint, an older mint, a denied
// scope from days ago, a broker error.
function eventsStatus() {
  return status({
    recent: [
      { time: ago(5), provider: "aws", entry: "culipulse-prod", outcome: "minted", elevated: true },
      { time: ago(40), provider: "github", subject: "culiops", outcome: "minted" },
      { time: ago(60), provider: "aws", entry: "culipulse-prod", outcome: "minted", elevated: false },
      { time: new Date(Date.now() - 6 * 86400000).toISOString(), provider: "aws", entry: "culipulse-prod", outcome: "denied_scope" },
      { time: new Date(Date.now() - 7 * 86400000).toISOString(), provider: "aws", entry: "culipulse-prod", outcome: "sts_error" },
    ],
    approvals: [granted({ ts: Date.now() - 20 * MIN })],
  });
}
const evRows = (host) => host.querySelectorAll("section.ac-events.card table.tbl tbody tr");
const evCells = (tr) => tr.querySelectorAll("td");

test("recent events: the Time / Provider / Subject / Outcome / Elevated heads; tinted rows are exactly the tint rows; \"yes\" is red; \"denied scope\" is muted", async () => {
  const { host } = await mount(open(12), eventsStatus());
  const table = host.querySelector(".ac-events.card table.tbl");
  assert.ok(table, "the events card holds a kit table");
  assert.ok(has(table, "ac-tbl"));
  const heads = table.querySelectorAll("thead th");
  assert.deepStrictEqual(heads.map((h) => h.textContent), ["Time", "Provider", "Subject", "Outcome", "Elevated"]);
  assert.ok(heads.every((h) => classes(h).length === 0), "every head is left-aligned and unclassed (no .num here)");
  assert.ok(heads.every((h) => h.getAttribute("scope") === "col"));

  const rows = evRows(host);
  assert.strictEqual(rows.length, 6);
  const tinted = rows.filter((r) => has(r, "is-elevated"));
  assert.deepStrictEqual(tinted.map((r) => evCells(r)[1].textContent), ["aws", "approval"], "the elevated mint and the grant, nothing else");
  assert.ok(rows.filter((r) => !has(r, "is-elevated")).every((r) => r.className === ""), "an untinted row carries no class");

  // Time and Provider: sub cells, the time tabular.
  for (const r of rows) {
    const c = evCells(r);
    assert.strictEqual(c.length, 5);
    assert.ok(has(c[0], "sub", "tnum") && has(c[1], "sub"));
    assert.strictEqual(c[3].className, "", "Outcome carries no class: its tone is on a span");
    assert.strictEqual(c[4].className, "", "Elevated carries no class");
  }
  assert.match(evCells(rows[0])[0].textContent, /^\d\d:\d\d$/);
  assert.match(evCells(rows[4])[0].textContent, /^\d{1,2} \w{3} \d\d:\d\d$/, "an earlier day carries its date");

  // Outcome: minted green, a refusal muted (never red), an error red, a person's decision plain.
  const out = (i) => evCells(rows[i])[3];
  assert.strictEqual(out(0).children[0].textContent, "minted");
  assert.ok(has(out(0).children[0], "c-ok"));
  assert.strictEqual(out(1).textContent, "you (browser)");
  assert.strictEqual(out(1).children.length, 0, "plain fg text, no span");
  assert.strictEqual(out(4).children[0].textContent, "denied scope");
  assert.ok(has(out(4).children[0], "muted") && !has(out(4).children[0], "c-red"), "denied scope is muted, never red");
  assert.strictEqual(out(5).children[0].textContent, "sts error");
  assert.ok(has(out(5).children[0], "c-red"), "a broker error is red");

  // Elevated: "yes" red 500, "no" plain, the approval's blank.
  const el = (i) => evCells(rows[i])[4];
  assert.strictEqual(el(0).children[0].textContent, "yes");
  assert.ok(has(el(0).children[0], "c-red", "w-500"));
  assert.strictEqual(el(1).textContent, "");
  assert.strictEqual(el(2).textContent, "no");
  assert.strictEqual(el(2).children.length, 0, "no is plain fg text");
});

test("event subjects: a credential is its mono name; an approval is a sentence with the name mono and the reason muted in quotes", async () => {
  const { host } = await mount(open(12), eventsStatus());
  const rows = evRows(host);
  const subj = (i) => evCells(rows[i])[2];
  assert.ok(has(subj(0), "ac-subj"));
  assert.strictEqual(subj(0).children.length, 1);
  assert.ok(has(subj(0).children[0], "mono", "t-sm"));
  assert.strictEqual(subj(0).textContent, "culipulse-prod");
  const ap = subj(1);
  assert.strictEqual(ap.textContent, "granted 30 min on culipulse-prod · “rotate KMS key”");
  assert.ok(ap.children[0] instanceof Text && ap.children[0].textContent === "granted 30 min on ", "the sentence is a text node");
  const parts = ap.children.filter((c) => c instanceof Node);
  assert.deepStrictEqual(parts.map((c) => [c.tag, c.className, c.textContent]), [["span", "mono t-sm", "culipulse-prod"], ["span", "muted", " · “rotate KMS key”"]]);
  assert.strictEqual(subj(2).textContent, "culiops");
});

test("an approval without a reason or a connection stays a plain sentence", async () => {
  const aws = status({ approvals: [
    { ts: Date.now() - 2 * MIN, action: "sso_login_ok", name: "", reason: "" },
    { ts: Date.now() - 4 * MIN, action: "dropped", name: "culipulse-prod", reason: "" },
  ] });
  const { host } = await mount(claudeBox(), aws);
  const rows = evRows(host);
  assert.strictEqual(evCells(rows[0])[2].textContent, "sign-in ok");
  assert.strictEqual(evCells(rows[0])[2].children.length, 0);
  assert.strictEqual(evCells(rows[1])[2].textContent, "dropped on culipulse-prod");
  assert.deepStrictEqual(evCells(rows[1])[2].children.filter((c) => c instanceof Node).map((c) => c.textContent), ["culipulse-prod"], "only the name is a span; there is no reason span");
  assert.ok(rows.every((r) => !has(r, "is-elevated")), "a drop is not write access");
});

test("the card head says \"Last 20 ·\" and \"Full history in Audit\" calls openAudit({session: sid})", async () => {
  const { host, box } = await mount(open(12), eventsStatus());
  const head = host.querySelector(".ac-events").children[0];
  assert.strictEqual(head.className, "card-head");
  assert.deepStrictEqual(head.children.map((c) => [c.tag, c.className, c.textContent]), [
    ["h2", "t-md w-600", "Recent credential events"], ["span", "ph-spacer", ""], ["span", "t-sm muted", "Last 20 ·"], ["button", "link t-sm", "Full history in Audit"],
  ]);
  const full = head.children[3];
  assert.strictEqual(full.type, "button");
  full.click();
  assert.deepStrictEqual(box.audits, [{ session: "s1" }]);
});

test("an events card with nothing to show says why, without a table", async () => {
  const none = await mount(claudeBox(), status());
  assert.strictEqual(none.host.querySelector("table"), null);
  assert.match(none.host.querySelector(".ac-events").textContent, /No credential events yet\./);
  assert.strictEqual(none.host.querySelector(".ac-events button").textContent, "Full history in Audit", "the link stays: the history may hold what the list does not");
  // A read that failed before anything loaded.
  const p = page();
  p.box.lastSessions = [claudeBox()];
  p.box.fetch = async () => ({ status: 500 });
  p.box.paneViewRenderers.access(p.host, "s1");
  await tick();
  assert.match(p.host.querySelector(".ac-events").textContent, /Could not read the credential log\./);
});

test("write-window decisions that could not be read say so under the table", async () => {
  const aws = eventsStatus();
  delete aws.approvals;
  const { host } = await mount(open(12), aws);
  assert.match(host.querySelector(".ac-events").textContent, /Write-window decisions are unavailable right now\./);
  assert.strictEqual(evRows(host).length, 5);
});

// ---- the keyboard: a button the keyboard is on is never [disabled], and a repaint hands it back ------------

test("Elevate… stays a focusable button while a grant or drop is in flight: aria-disabled and a click guard, never [disabled]", async () => {
  const { doc, host, handle, lex } = await mount(claudeBox(), status());
  const first = btn(host, "Elevate…");
  first.focus();
  lex("accBusy").add("s1");
  handle.update();
  settle(doc);
  const busy = btn(host, "Elevate…");
  assert.strictEqual(busy.getAttribute("aria-disabled"), "true", "closed-looking");
  assert.strictEqual(busy.disabled, false, "but not [disabled]: that would drop the keyboard to <body>");
  assert.ok(doc.activeElement === busy, "the keyboard is on the button the repaint built");
  assert.ok(host.contains(doc.activeElement));
  busy.click();
  assert.strictEqual(host.querySelector('[data-ac="reason"]'), null, "a press while busy opens no panel");
  lex("accBusy").delete("s1");
  handle.update();
  const live = btn(host, "Elevate…");
  assert.strictEqual(live.getAttribute("aria-disabled"), null);
  assert.ok(doc.activeElement === live, "and back again, still on it");
  live.click();
  const reason = host.querySelector('[data-ac="reason"]');
  assert.ok(reason, "a press when idle opens the panel");
  assert.ok(doc.activeElement === reason, "and the panel takes the keyboard to its reason field");
});

test("Extend keeps focus the same way", async () => {
  const { doc, host, handle, lex } = await mount(open(12), status({ approvals: [granted()] }));
  btn(host, "Extend").focus();
  lex("accBusy").add("s1");
  handle.update();
  settle(doc);
  const busy = btn(host, "Extend");
  assert.strictEqual(busy.getAttribute("aria-disabled"), "true");
  assert.strictEqual(busy.disabled, false);
  assert.ok(doc.activeElement === busy);
  busy.click();
  assert.strictEqual(host.querySelector('[data-ac="reason"]'), null);
  lex("accBusy").delete("s1");
  handle.update();
  assert.ok(doc.activeElement === btn(host, "Extend"));
});

test("an Elevate… the row cannot offer (another connection holds the window) says why in its title and is aria-disabled", async () => {
  const s = claudeBox({ aws_connections: [conn({ name: "culipulse-prod", elevated_until: iso(25) }), conn({ name: "culipulse-dev", label: "culipulse-dev", prod: false })] });
  const aws = status({ approvals: [granted()], connections: [{ name: "culipulse-prod", stats: { mints: 1, last_mint: ago(5) } }, { name: "culipulse-dev", stats: { mints: 0 } }] });
  const { host } = await mount(s, aws);
  const dev = host.querySelectorAll(".ac-conn").find((r) => r.children[0].querySelector(".ac-name").children[0].textContent === "culipulse-dev");
  const b = btn(dev, "Elevate…");
  assert.ok(b);
  assert.strictEqual(b.getAttribute("aria-disabled"), "true");
  assert.strictEqual(b.disabled, false);
  assert.strictEqual(b.title, "Drop culipulse-prod first");
});

test("the reason a blocked Elevate… gives is the model's own sentence, verbatim, and the view writes none of its own", async () => {
  // Credentials issued for culipulse-prod before a Drop are still valid: the server's conn_blocked sentence names why the
  // other connection cannot be elevated yet. Both sentences already ship (elevation.js grantPanel, shown in the grant panel).
  const sentence = "Credentials already issued for culipulse-prod are still valid; wait until they expire.";
  const s = claudeBox({ aws_connections: [conn(), conn({ name: "culipulse-dev", label: "culipulse-dev", prod: false })] });
  const aws = status({
    connections: [{ name: "culipulse-prod", stats: { mints: 1, last_mint: ago(5) } }, { name: "culipulse-dev", stats: { mints: 0 } }],
    elevation: { active: false, name: "culipulse-prod", last_valid: iso(10) },
    elevate: { host_sessions: [], conn_blocked: { "culipulse-dev": sentence } },
  });
  const { host } = await mount(s, aws);
  const rowOf = (name) => host.querySelectorAll(".ac-conn").find((r) => r.children[0].querySelector(".ac-name").children[0].textContent === name);
  const dev = btn(rowOf("culipulse-dev"), "Elevate…");
  assert.strictEqual(dev.getAttribute("aria-disabled"), "true");
  assert.strictEqual(dev.title, sentence);
  const prod = btn(rowOf("culipulse-prod"), "Elevate…");
  assert.strictEqual(prod.getAttribute("aria-disabled"), null, "the connection the credentials came from is not blocked by them");
  assert.ok(!prod.title, "and gives no reason");
  assert.doesNotMatch(read("accesstab.js").replace(/\/\/.*$/gm, ""), /Drop [^"\n]*first|"[^"\n]* first"/, "no wording of its own: it comes from grantPanel");
});

test("Full history in Audit keeps focus when the events card is rebuilt under it", async () => {
  const { doc, host, handle, lex } = await mount(open(12), eventsStatus());
  const before = btn(host, "Full history in Audit");
  before.focus();
  lex("accData").get("s1").aws.recent.unshift({ time: ago(1), provider: "aws", entry: "culipulse-prod", outcome: "minted", elevated: true });
  handle.update();
  settle(doc);
  assert.strictEqual(evRows(host).length, 7, "the card was rebuilt with the new row");
  const after = btn(host, "Full history in Audit");
  assert.ok(after !== before, "a new node");
  assert.ok(doc.activeElement === after, "the keyboard went with it, not to <body>");
});

// G3-D3: fill() used to give the keyboard back by the button's POSITION among the section's buttons, so with three
// connections Cancel on the first landed on the third's Elevate…, and Cancel on the others fell to the view host. It is
// the control's identity now (its connection and its action), and a closed panel's Cancel goes to its own row's Elevate….
const THREE = () => claudeBox({ aws_connections: [conn(), conn({ name: "culipulse-dev", label: "culipulse-dev", prod: false }), conn({ name: "culipulse-stage", label: "culipulse-stage", prod: false })] });
const THREE_STATUS = () => status({ connections: ["culipulse-prod", "culipulse-dev", "culipulse-stage"].map((name) => ({ name, stats: { mints: 1, last_mint: ago(5) } })) });
const ownerOf = (n) => {
  let b = n;
  while (b && !classes(b).includes("ac-blk")) b = b.parent;
  return b ? b.querySelector(".ac-name").children[0].textContent : null;
};

test("Cancel on any of three connections gives the keyboard to THAT connection's Elevate…, not another's and not the view (G3-D3)", async () => {
  for (const name of ["culipulse-prod", "culipulse-dev", "culipulse-stage"]) {
    const { doc, host } = await mount(THREE(), THREE_STATUS());
    const elevates = () => buttons(host).filter((b) => b.textContent === "Elevate…");
    assert.strictEqual(elevates().length, 3);
    elevates().find((b) => ownerOf(b) === name).click();
    const panel = host.querySelector(".ac-panel");
    assert.ok(panel && ownerOf(panel) === name, "the panel opened under " + name);
    const cancel = btn(panel, "Cancel");
    cancel.focus();
    cancel.click();
    assert.strictEqual(host.querySelector(".ac-panel"), null, "the panel closed");
    const a = doc.activeElement;
    assert.ok(a && a !== host, name + ": focus is not on the view host");
    assert.strictEqual(a.textContent, "Elevate…", name + ": it is an Elevate…");
    assert.strictEqual(ownerOf(a), name, "and it is " + name + "'s");
    assert.ok(elevates().includes(a), "a button of the rebuilt section, not a stale node");
  }
});

test("Cancel on the one connection goes to its Elevate…, and an open window's Extend is where Cancel returns on an extending row", async () => {
  const one = await mount(claudeBox(), status());
  btn(one.host, "Elevate…").click();
  btn(one.host, "Cancel").focus();
  btn(one.host, "Cancel").click();
  assert.ok(one.doc.activeElement === btn(one.host, "Elevate…"), "the seeded single connection too (it used to fall to the view host)");
  const ext = await mount(open(12), status({ approvals: [granted()] }));
  btn(ext.host, "Extend").click();
  btn(ext.host, "Cancel").focus();
  btn(ext.host, "Cancel").click();
  assert.ok(ext.doc.activeElement === btn(ext.host, "Extend"), "an Extend row gets its Extend back");
});

test("a repaint keeps the keyboard on the same control of the same connection, among three of them (G3-D3)", async () => {
  const { doc, host, handle, lex } = await mount(THREE(), THREE_STATUS());
  const on = (name) => buttons(host).filter((b) => b.textContent === "Elevate…").find((b) => ownerOf(b) === name);
  for (const name of ["culipulse-stage", "culipulse-dev", "culipulse-prod"]) {
    on(name).focus();
    lex("accBusy").add("s1");
    handle.update();
    lex("accBusy").delete("s1");
    handle.update();
    assert.ok(doc.activeElement === on(name), name + ": still on its own Elevate…");
  }
  // A panel's own buttons are identified the same way: the window choices and Grant keep the keyboard on themselves.
  on("culipulse-dev").click();
  const panel = () => host.querySelector(".ac-panel");
  const seg = () => buttons(panel()).filter((b) => /min$/.test(b.textContent));
  assert.ok(seg().length >= 2, "the panel offers windows");
  seg()[1].focus();
  const label = seg()[1].textContent;
  lex("accBusy").add("s1");
  handle.update();
  assert.strictEqual(doc.activeElement.textContent, label, "the window choice keeps the keyboard when the section is rebuilt");
  assert.strictEqual(ownerOf(doc.activeElement), "culipulse-dev");
  lex("accBusy").delete("s1");
});

test("when the control the keyboard was on is gone and its row is too, it goes to the view, not <body>", async () => {
  const { box, doc, host, handle } = await mount(claudeBox(), status());
  btn(host, "Elevate…").click();
  btn(host, "Cancel").focus();
  box.lastSessions = []; // the session leaves the list: no panel, no row, no Elevate…
  handle.update();
  assert.strictEqual(host.querySelector(".ac-panel"), null);
  assert.ok(doc.activeElement === host, "focus is on the view host (tabindex -1)");
});

test("a poll that changes nothing rebuilds nothing", async () => {
  const { host, handle } = await mount(open(12), eventsStatus());
  const sels = ["section.ac-cont", "section.ac-creds", "section.ac-events"];
  const nodes = sels.map((q) => host.querySelector(q));
  assert.ok(nodes.every(Boolean));
  handle.update();
  handle.update();
  assert.deepStrictEqual(sels.map((q, i) => host.querySelector(q) === nodes[i]), [true, true, true]);
});

// ---- the grant panel: unchanged in behaviour, on kit parts -------------------------------------------

test("the grant panel keeps its data-ac fields, the typing guard and Enter; Grant is aria-disabled until the form is valid", async () => {
  const { doc, host, box, handle, lex } = await mount(claudeBox(), status());
  btn(host, "Elevate…").click();
  const panel = host.querySelector(".ac-panel");
  assert.ok(panel);
  assert.strictEqual(panel.getAttribute("role"), "group");
  assert.strictEqual(panel.getAttribute("aria-label"), "Grant write access · culipulse-prod");
  assert.match(panel.children[0].textContent, /^Grant write on culipulse-prod to this session\. Credentials already issued stay valid until they expire\.$/);

  const seg = panel.querySelector(".seg");
  assert.strictEqual(seg.getAttribute("aria-label"), "Window");
  const items = seg.querySelectorAll(".seg-item");
  assert.deepStrictEqual(items.map((b) => b.textContent), ["30 min", "60 min"]);
  assert.deepStrictEqual(items.map((b) => b.getAttribute("aria-pressed")), ["true", "false"]);
  items[1].click();
  assert.deepStrictEqual(items.map((b) => b.getAttribute("aria-pressed")), ["false", "true"]);
  items[0].click();

  const reason = panel.querySelector('[data-ac="reason"]');
  const typed = panel.querySelector('[data-ac="typed"]');
  assert.ok(has(reason, "input") && reason.tag === "input" && reason.maxLength === 200);
  assert.ok(has(typed, "input", "mono") && typed.tag === "input", "a prod binding asks for its label");
  assert.ok(doc.activeElement === reason);
  assert.ok(panel.querySelector("label").htmlFor === reason.id);

  const cancel = btn(panel, "Cancel");
  const grant = buttons(panel).find((b) => /^Grant write/.test(b.textContent));
  assert.ok(has(cancel, "btn") && has(grant, "btn", "btn-primary"));
  assert.strictEqual(grant.textContent, "Grant write · 30 min");
  assert.strictEqual(grant.getAttribute("aria-disabled"), "true");
  assert.strictEqual(grant.disabled, false, "aria-disabled, never [disabled]");
  grant.click();
  assert.deepStrictEqual(box.grants, [], "an incomplete form sends nothing");

  reason.value = "rotate KMS key";
  reason.oninput();
  assert.strictEqual(grant.getAttribute("aria-disabled"), "true", "a prod label is still to type");
  typed.value = "culipulse-prod";
  typed.oninput();
  assert.strictEqual(grant.getAttribute("aria-disabled"), null);

  // The typing guard: a field with focus keeps the credentials section where it is.
  const creds = host.querySelector(".ac-creds");
  lex("accData").get("s1").aws.stats.mints = 3;
  handle.update();
  assert.ok(host.querySelector(".ac-creds") === creds, "the section is not rebuilt under the operator's typing");
  assert.strictEqual(reason.value, "rotate KMS key");

  // Enter in a field sends it.
  reason.focus();
  reason.onkeydown({ key: "Enter", preventDefault() {} });
  await tick();
  assert.deepStrictEqual(box.grants, [{ sid: "s1", name: "culipulse-prod", minutes: 30, reason: "rotate KMS key", confirm_label: "culipulse-prod" }]);
});

test("a grant in flight greys Grant and Cancel without disabling them, and the keyboard stays on the field it was in", async () => {
  const { doc, host, box, handle, lex } = await mount(claudeBox(), status());
  btn(host, "Elevate…").click();
  const reason = host.querySelector('[data-ac="reason"]');
  const typed = host.querySelector('[data-ac="typed"]');
  reason.value = "rotate KMS key"; reason.oninput();
  typed.value = "culipulse-prod"; typed.oninput();
  let release;
  box.elevGrant = (sid, o) => { box.grants.push({ sid, ...o }); return new Promise((r) => { release = r; }); };
  typed.focus();
  typed.onkeydown({ key: "Enter", preventDefault() {} });
  assert.strictEqual(box.grants.length, 1);
  assert.ok(lex("accBusy").has("s1"));
  settle(doc);
  const now = host.querySelector('[data-ac="typed"]');
  assert.ok(doc.activeElement === now, "the repaint kept the field the keyboard was in");
  const panel = host.querySelector(".ac-panel");
  const cancel = btn(panel, "Cancel");
  const grant = buttons(panel).find((b) => /^Grant write/.test(b.textContent));
  for (const b of [cancel, grant]) {
    assert.strictEqual(b.getAttribute("aria-disabled"), "true");
    assert.strictEqual(b.disabled, false);
  }
  cancel.click();
  assert.ok(host.querySelector(".ac-panel"), "Cancel does nothing while the request is out");
  grant.click();
  assert.strictEqual(box.grants.length, 1, "and a second press sends no second request");
  assert.match(panel.textContent, /Sending…/);
  release(null);
  await tick();
  assert.ok(!lex("accBusy").has("s1"));
  assert.strictEqual(host.querySelector(".ac-panel"), null, "a granted panel closes");
  void handle;
});

test("a refused grant keeps the panel, its draft and its error", async () => {
  const { host, box } = await mount(claudeBox(), status());
  btn(host, "Elevate…").click();
  const reason = host.querySelector('[data-ac="reason"]');
  reason.value = "rotate KMS key"; reason.oninput();
  const typed = host.querySelector('[data-ac="typed"]');
  typed.value = "culipulse-prod"; typed.oninput();
  box.elevGrant = async () => { throw new Error("A window is already open on another connection."); };
  typed.onkeydown({ key: "Enter", preventDefault() {} });
  await tick();
  const err = host.querySelector(".ac-err");
  assert.strictEqual(err.textContent, "A window is already open on another connection.");
  assert.strictEqual(err.getAttribute("role"), "alert");
  assert.strictEqual(err.hidden, false);
  assert.strictEqual(host.querySelector('[data-ac="reason"]').value, "rotate KMS key", "the draft survives the repaint");
});

test("Extend opens the same panel, worded for an extension", async () => {
  const { host } = await mount(open(12), status({ approvals: [granted()] }));
  btn(host, "Extend").click();
  const panel = host.querySelector(".ac-panel");
  assert.match(panel.getAttribute("aria-label"), /^Extend write access/);
  assert.match(panel.children[0].textContent, /^Extend write on culipulse-prod/);
  assert.ok(buttons(panel).some((b) => /^Extend · \d+ min$/.test(b.textContent)));
});

// ---- source and styles ---------------------------------------------------------------------------------

test("the source: no [disabled] on a view button, no row Drop, no markup parsing, and the banner's globals kept as they were", () => {
  const src = read("accesstab.js").replace(/\/\/.*$/gm, "");
  assert.doesNotMatch(src, /\.disabled\s*=/, "a button the keyboard may hold is aria-disabled");
  assert.doesNotMatch(src, /["']Drop["']|["']dgr["']|includes\("drop"\)/, "the row's Drop is gone: the banner owns the only one");
  assert.doesNotMatch(src, /innerHTML|outerHTML|insertAdjacentHTML|document\.write/);
  assert.match(src, /aria-disabled/);
  for (const keep of ["const accData", "const accBusy", "const accViews", "function accLoad\\(sid, force\\)", "async function accDrop\\(sid, repaint\\)"]) {
    assert.match(src, new RegExp(keep), keep + " (elevbanner.js reads it)");
  }
  assert.match(src, /ACCESS_REFRESH_MS = 15000/, "the 15 s status refresh");
  assert.match(src, /openAudit\(\{ session: sid \}\)/);
  assert.match(read("accesstab.js"), /return ok;/, "accDrop still answers true / false");
  for (const old of ["ac-card", "ac-head", "ac-title", "ac-meta", "ac-lab", "ac-val", "ac-rowblk", "ac-evrow", "ac-evhead", "ac-link", "ac-ok", "ac-bad", "ac-write", "ac-need"]) {
    assert.ok(!new RegExp("[\"' ]" + old + "[\"' ]").test(src), old + " is gone");
  }
});

test("Full history in Audit is a bare kit .link: no UA button chrome, the 2px ring kept, and the head leaves room for the ring", async () => {
  const strip = (c) => c.replace(/\/\*[\s\S]*?\*\//g, "");
  const { host } = await mount(open(12), eventsStatus());
  const full = btn(host, "Full history in Audit");
  assert.ok(has(full, "link", "t-sm") && full.tag === "button" && full.type === "button");
  // The kit's .link resets the button's own chrome, and nothing the view adds puts any of it back.
  const kit = /\.link \{([^}]*)\}/.exec(strip(read("desktop.css")));
  assert.ok(kit, "the kit .link rule");
  for (const d of [/background:\s*none/, /border:\s*0/, /padding:\s*0/, /color:\s*var\(--accent\)/]) assert.match(kit[1], d);
  assert.match(strip(read("desktop.css")), /:where\(\.btn, \.seg-item, \.nav-item, \.menu-item, \.link, \.input\) \{ font: inherit; \}/, "and the font is the line's, not the button default");
  assert.doesNotMatch(accessCss(), /\.link/, "no Access rule restyles it");
  // The ring: primitives.css gives every :focus-visible 2px at offset 2 (desktop.css sets the width and offset), and no
  // rule anywhere takes it off a .link.
  assert.match(strip(read("primitives.css")), /:focus-visible \{ outline: var\(--focus\); outline-offset: var\(--focus-off\); \}/);
  assert.match(strip(read("desktop.css")), /--focus-w: 2px; --focus-off: 2px;/);
  for (const f of ["primitives.css", "app.css", "desktop.css"]) {
    for (const m of strip(read(f)).matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      if (/\.link(?![\w-])/.test(m[1])) assert.doesNotMatch(m[2], /outline|box-shadow:\s*none/, f + ": " + m[1].trim() + " leaves the ring alone");
    }
  }
  // The ring sits 2px out at offset 2 (4px beyond the button); the card head's 12px / 16px padding keeps it inside the card.
  assert.match(strip(read("desktop.css")), /\.card-head \{[^}]*padding:\s*var\(--sp-3\) var\(--sp-4\)/);
});

// Every class the view puts on screen is defined by a stylesheet (classnames.test.js cannot see a class that goes
// through accEl()), so a typo or a rename never renders unstyled.
test("every class on the drawn view is defined in a stylesheet", async () => {
  const strip = (c) => c.replace(/\/\*[\s\S]*?\*\//g, "");
  const defined = new Set();
  for (const f of ["tokens.css", "primitives.css", "app.css", "desktop.css"]) {
    for (const m of strip(read(f)).matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)) defined.add(m[1]);
  }
  const used = new Set();
  const walk = (n) => { for (const c of n.children) { if (c instanceof Node) { classes(c).forEach((k) => used.add(k)); walk(c); } } };
  const views = [];
  // Elevated, with the grant panel open on an Extend, a broker error and every event shape.
  const a = await mount(open(12), Object.assign(eventsStatus(), {
    connections: [{ name: "culipulse-prod", stats: { mints: 2, last_mint: ago(30), last_error: "sts: access denied" } }],
  }));
  btn(a.host, "Extend").click();
  views.push(a);
  // Closed, with the lapse and leftover lines, the SSO card and the grant panel with a host warning.
  const b = await mount(claudeBox({ need: "sso", aws_connections: [conn({ sso_expired: true })] }), status({
    approvals: [{ ts: Date.now() - 3 * MIN, action: "expired", name: "culipulse-prod", reason: "" }],
    elevation: { active: false, name: "culipulse-prod", last_valid: iso(10) },
    sso: { sources: [{ expired: true, names: ["culipulse-prod"] }] },
    elevate: { host_sessions: [{ name: "audit-shell" }], blocked: "Another session holds the write window." },
  }));
  btn(b.host, "Elevate…").click();
  views.push(b);
  // A host session, an unloaded one and one that left the list.
  views.push(await mount({ id: "s1", agent: "bash", runtime: "host", cwd: "/home/u/w", clone_of: "/home/u/src" }, null));
  const gone = await mount(claudeBox(), status());
  gone.box.lastSessions = [];
  gone.handle.update();
  views.push(gone);
  for (const v of views) walk(v.host);
  // Hooks: found again by the tests and the gates, never styled (their look is the kit's .card). classnames.test.js's HOOKS
  // lists them too.
  const HOOKS = new Set(["ac-cont", "ac-creds"]);
  const missing = [...used].filter((k) => !defined.has(k) && !HOOKS.has(k));
  assert.deepStrictEqual(missing, [], "classes with no CSS rule");
  for (const k of ["ac-wrap", "ac-cont", "ac-creds", "ac-events", "ac-conn", "ac-note", "ac-tbl", "ac-panel", "ac-sso"]) assert.ok(used.has(k), "the walk reached " + k);
});

const desktopCss = () => read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
const accessCss = () => {
  const css = desktopCss();
  const a = css.indexOf('.pane-view[data-view="access"]');
  const b = css.indexOf('.pane-view[data-view="activity"]', a);
  assert.ok(a > 0 && b > a, "the Access block is findable, ahead of the Activity block");
  return css.slice(a, b);
};

test("the Access rules replace the legacy ones: nothing of the old card, grid-row or event-grid layout is left", () => {
  const css = desktopCss();
  for (const old of [".ac-card", ".ac-head", ".ac-title", ".ac-meta", ".ac-lab", ".ac-val", ".ac-sub", ".ac-cols", ".ac-tier", ".ac-kv", ".ac-kvv", ".ac-cloneline", ".ac-ok", ".ac-bad", ".ac-write", ".ac-need", ".ac-rowblk", ".ac-row", ".ac-id", ".ac-nameline", ".ac-prod", ".ac-plead", ".ac-prole", ".ac-ev", ".ac-evrow", ".ac-evhead", ".ac-evt", ".ac-evs", ".ac-link"]) {
    assert.ok(!new RegExp("\\" + old.replace(/ /g, "\\s+\\") + "(?![\\w-])").test(css), old + " is gone");
  }
  assert.doesNotMatch(css, /\.ac-pcol \.fld|\.ac-pcol\.grow/, "the panel's fields are the kit's .input");
});

test("the view's CSS uses kit values only: no hex, no color-mix, no heavy weight, no --faint text, no legacy --danger", () => {
  const block = accessCss();
  assert.doesNotMatch(block, /#[0-9a-fA-F]{3,8}\b/);
  assert.doesNotMatch(block, /color-mix|@layer|\(\?<[=!]/);
  assert.doesNotMatch(block, /font-weight:\s*(bold|[789]00)/);
  assert.doesNotMatch(block, /(^|[^-])color:\s*var\(--faint\)/);
  assert.doesNotMatch(block, /var\(--danger\)/);
});

test("the port traps are handled: the view root sets the 20px line, h2 and p lose their margin, empty slots own no box", () => {
  const block = accessCss();
  assert.match(block, /\.ac-wrap\s*\{[^}]*line-height:\s*var\(--lh-md\)/);
  assert.match(block, /\.ac-wrap :where\(h2, p\)\s*\{\s*margin:\s*0/);
  assert.match(block, /\.ac-wrap > div\s*\{\s*display:\s*contents/);
  // §D1: the view is a column of cards 16px apart inside 20px 24px of padding.
  assert.match(block, /\.ac-wrap\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*gap:\s*var\(--sp-4\)[^}]*padding:\s*var\(--sp-5\) var\(--sp-6\)/);
  assert.match(block, /\.pane-view\[data-view="access"\]\s*\{\s*container-type:\s*inline-size/);
});

test("containment is the frame's: four equal cells with a hairline between, a 140px label column under them", () => {
  const block = accessCss();
  assert.match(block, /\.ac-cells\s*\{[^}]*display:\s*grid[^}]*grid-template-columns:\s*repeat\(4, minmax\(0, 1fr\)\)/);
  assert.match(block, /\.ac-cell\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*gap:\s*2px[^}]*padding:\s*var\(--sp-3\) var\(--sp-4\)[^}]*min-width:\s*0/);
  assert.match(block, /\.ac-cell \+ \.ac-cell\s*\{[^}]*border-left:\s*1px solid var\(--line\)/);
  assert.match(block, /\.ac-kvs\s*\{[^}]*display:\s*grid[^}]*grid-template-columns:\s*140px minmax\(0, 1fr\)[^}]*row-gap:\s*var\(--sp-1\)[^}]*padding:\s*var\(--sp-3\) var\(--sp-4\)[^}]*border-top:\s*1px solid var\(--line\)/);
});

test("the connection row is the frame's grid: 2fr, three 1fr and a 160px action cell, cells without padding, a rule between blocks, the note pulled up 8px", () => {
  const block = accessCss();
  assert.match(block, /\.ac-conn\s*\{[^}]*display:\s*grid[^}]*grid-template-columns:\s*minmax\(0, 2fr\) repeat\(3, minmax\(0, 1fr\)\) 160px[^}]*align-items:\s*center[^}]*column-gap:\s*var\(--sp-4\)[^}]*padding:\s*var\(--sp-3\) var\(--sp-4\)/);
  assert.match(block, /\.ac-conn > \.ac-cell\s*\{[^}]*padding:\s*0/);
  assert.match(block, /\.ac-blk \+ \.ac-blk\s*\{[^}]*border-top:\s*1px solid var\(--line\)/);
  assert.match(block, /\.ac-name\s*\{[^}]*display:\s*flex[^}]*align-items:\s*center[^}]*gap:\s*var\(--sp-2\)[^}]*min-width:\s*0/);
  assert.match(block, /\.ac-acts\s*\{[^}]*display:\s*flex[^}]*justify-content:\s*flex-end[^}]*gap:\s*var\(--sp-2\)/);
  assert.match(block, /\.ac-connnote\s*\{[^}]*padding:\s*0 var\(--sp-4\) var\(--sp-3\)[^}]*margin-top:\s*calc\(-1 \* var\(--sp-2\)\)/);
  assert.match(block, /\.ac-note\s*\{[^}]*white-space:\s*normal/, "the window lines wrap, they never ellipsize");
});

test("the events table is the frame's: fixed layout, 120 / 112 / 160 / 96 columns, a wrapping 6px-padded subject, the tint kept on hover", () => {
  const block = accessCss();
  assert.match(block, /\.ac-events\s*\{[^}]*overflow:\s*hidden/);
  assert.match(block, /\.ac-tbl\s*\{[^}]*table-layout:\s*fixed/);
  for (const [n, w] of [[1, 120], [2, 112], [4, 160], [5, 96]]) {
    assert.match(block, new RegExp("\\.ac-tbl th:nth-child\\(" + n + "\\)\\s*\\{[^}]*width:\\s*" + w + "px"));
  }
  assert.match(block, /\.ac-tbl td\.ac-subj\s*\{[^}]*white-space:\s*normal[^}]*overflow-wrap:\s*anywhere[^}]*height:\s*auto[^}]*padding-top:\s*6px[^}]*padding-bottom:\s*6px[^}]*line-height:\s*var\(--lh-md\)/);
  // The kit's `.tbl tbody tr:hover` (0,2,2) out-weighs `.tbl tr.is-elevated` (0,2,1): a tinted row keeps its tint on hover.
  assert.match(block, /\.ac-tbl tbody tr\.is-elevated:hover\s*\{[^}]*background:\s*var\(--red-tint\)/);
  // The Outcome head is left-aligned here (Activity's is .num), so nothing here right-aligns a cell.
  assert.doesNotMatch(block, /\.ac-tbl[^{]*\{[^}]*text-align:\s*right/);
});

test("a narrow pane (under 800px) stacks the connection row, halves the containment grid and lets the table scroll in its card", () => {
  const block = accessCss();
  const m = /@container \(max-width: 799px\)\s*\{([\s\S]*?\n  \})/.exec(block);
  assert.ok(m, "a narrow-pane container query");
  assert.ok(block.indexOf("@container (max-width: 799px)") > block.indexOf(".ac-tbl td.ac-subj"), "after the base rules it overrides");
  assert.match(m[1], /\.ac-cells\s*\{[^}]*repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(m[1], /\.ac-conn\s*\{[^}]*grid-template-columns:\s*repeat\(3, minmax\(0, 1fr\)\) auto/);
  assert.match(m[1], /\.ac-conn > \.ac-cell:first-child\s*\{[^}]*grid-column:\s*1 \/ -1/);
  assert.match(m[1], /\.ac-events\s*\{[^}]*overflow-x:\s*auto/);
  assert.match(m[1], /\.ac-tbl\s*\{[^}]*table-layout:\s*auto/);
});

test("the panel is built on kit parts: a divided inset box, kit buttons, the kit .seg and .input, red errors from --red", () => {
  const block = accessCss();
  assert.match(block, /\.ac-panel\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*border:\s*1px solid var\(--line\)[^}]*border-radius:\s*var\(--r-ctl\)/);
  assert.match(block, /\.ac-err\s*\{[^}]*color:\s*var\(--red\)/);
  assert.match(block, /\.ac-pbtns\s*\{[^}]*justify-content:\s*flex-end/);
  assert.doesNotMatch(block, /\.ac-[\w-]+ \.btn[^{]*\{[^}]*padding/, "no page rule resizes a kit button");
});
