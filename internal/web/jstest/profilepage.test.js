"use strict";
// profilepage.js: the desktop profile overview page (spec 5.1-5.4). It draws
// profileview.js's read model and owns the nav list, the header actions and
// Delete. The repo has no jsdom, so the page runs against a minimal fake DOM
// (as activitytab.test.js and credentials-dom.test.js do), with the real model
// scripts loaded beside it and the app.js globals it calls stubbed.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");

class Text { constructor(t) { this.text = String(t); this.parent = null; } get textContent() { return this.text; } }
class Node {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.dataset = {}; this.hidden = false; this.disabled = false; this.handlers = {};
    const self = this;
    const set = () => new Set(self.className.split(/\s+/).filter(Boolean));
    this.classList = {
      contains: (c) => set().has(c),
      add: (c) => { const s = set(); s.add(c); self.className = [...s].join(" "); },
      remove: (c) => { const s = set(); s.delete(c); self.className = [...s].join(" "); },
      toggle: (c, on) => { const s = set(); if (on === undefined ? !s.has(c) : on) s.add(c); else s.delete(c); self.className = [...s].join(" "); },
    };
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "class") this.className = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  appendChild(c) { if (typeof c === "string") c = new Text(c); if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = ""; cs.forEach((c) => this.appendChild(c)); }
  addEventListener(ev, fn) { (this.handlers[ev] = this.handlers[ev] || []).push(fn); }
  contains(n) { for (let x = n; x; x = x.parent) if (x === this) return true; return false; }
  focus() { Node.focused = this; }
  click() { if (this.onclick) return this.onclick({ target: this }); }
  all(pred, out = []) { for (const c of this.children) { if (!(c instanceof Node)) continue; if (pred(c)) out.push(c); c.all(pred, out); } return out; }
}
const hasCls = (cls) => (n) => n.className.split(/\s+/).includes(cls);
const byCls = (root, cls) => root.all(hasCls(cls));
const texts = (root, cls) => byCls(root, cls).map((n) => n.textContent);
const byText = (root, tag, text) => root.all((n) => n.tag === tag && n.textContent.trim() === text);

const view = (name, over) => Object.assign({ name, editable: true, source: "runtime", runtime: "box", bin: "claude", claude: true }, over);
const PROFILES = [
  view("claude", { editable: false, source: "config", runtime: "host" }),
  view("aws-ops", { default_cwd: "/home/you/work/payments-api", aws_connections: ["prod-read", "staging-read"], tokens: ["grafana-prod"], network: "allowlist" }),
  view("infra-terraform"),
  view("runbook-host", { runtime: "host", default_cwd: "/home/you/runbooks" }),
];
const DOCS = {
  claude: { name: "claude", bin: "claude", runtime: "host" },
  "aws-ops": { name: "aws-ops", bin: "claude", runtime: "box", default_cwd: "/home/you/work/payments-api", options: { model: "claude-opus-5-5", effort: "high" }, rev: "r1" },
  "infra-terraform": { name: "infra-terraform", bin: "claude", runtime: "box", rev: "r2" },
  "runbook-host": { name: "runbook-host", bin: "claude", runtime: "host", default_cwd: "/home/you/runbooks", rev: "r3" },
};

// One page: the real model scripts, profilepage.js, and stand-ins for what app.js owns.
function page(over) {
  const calls = [];
  const els = {
    "pf-navlist": new Node("div"), "pfv-in": new Node("div"), "settings-modal": new Node("dialog"),
  };
  els["settings-modal"].classList.add("as-page");
  const doc = {
    createElement: (t) => new Node(t), createElementNS: (_ns, t) => new Node(t), createTextNode: (t) => new Text(t),
    getElementById: (id) => els[id] || null, addEventListener() {},
    get activeElement() { return Node.focused || null; },
  };
  Node.focused = null;
  const box = Object.assign({
    document: doc, console, Promise, JSON, Date, Math, Array, Object, Set, String, Number, setTimeout, clearTimeout,
    window: { HOME_DIR: "/home/you" },
    profiles: PROFILES.slice(), profilesLoaded: true, selectedProfile: "", metaLoaded: true,
    boxRuntime: "runc", boxUnavailableReason: "", bundleImports: true, optionDefs: [],
    awscEntries: [{ name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" }, { name: "staging-read", account_id: "111111111111", region: "eu-west-1" }],
    stEntries: [{ name: "grafana-prod", prod: true, env: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"], hosts: [{ host: "grafana.acme.example.com", port: 443 }] }],
    pfCredsView: { store: { initialized: true, locked: false }, aws: [] },
    api: async (p, opt) => {
      const method = (opt && opt.method) || "GET";
      calls.push(method + " " + p);
      if (box.apiFail) throw box.apiFail;
      const m = /^\/api\/profiles\/([^/]+)$/.exec(p);
      if (m && method === "GET") return DOCS[decodeURIComponent(m[1])];
      return null;
    },
    loadProfiles: async () => { calls.push("loadProfiles"); box.profiles = box.profiles.filter((p) => !box.gone || p.name !== box.gone); return true; },
    askConfirm: async (o) => { calls.push("confirm:" + o.title + ":" + (o.danger ? "danger" : "")); return box.confirmAnswer !== false; },
    openSpawnDialog: (n) => { calls.push("spawn:" + n); },
    setDialogError: (id, m) => { calls.push("error:" + m); },
  }, over);
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["fsops.js", "workhome.js", "spawnpreview.js", "profileview.js", "profilepage.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  return { box, els, calls, run: (code) => new vm.Script(code).runInContext(ctx) };
}

test("pfvRenderNav lists every profile with its tier word and a read-only marker for config.json", () => {
  const t = page();
  t.box.pfvRenderNav();
  const items = byCls(t.els["pf-navlist"], "pfv-ni").filter((n) => !hasCls("pfv-ni-add")(n));
  assert.deepStrictEqual(items.map((n) => byCls(n, "pfv-ni-name")[0].textContent), ["claude", "aws-ops", "infra-terraform", "runbook-host"]);
  assert.deepStrictEqual(items.map((n) => byCls(n, "pfv-ni-tier")[0].textContent.replace(/^read-only, /, "")), ["host", "box", "box", "host"]);
  // The marker: a lock glyph plus words for a screen reader, only on the config.json profile.
  assert.deepStrictEqual(items.map((n) => byCls(n, "pfv-lock").length), [1, 0, 0, 0]);
  assert.deepStrictEqual(items.map((n) => texts(n, "pfv-sr")), [["read-only, "], [], [], []]);
  assert.strictEqual(items[0].getAttribute("title"), "Defined in config.json (read-only)");
  // "+ New profile" closes the list, and is not a profile.
  const add = byCls(t.els["pf-navlist"], "pfv-ni-add");
  assert.strictEqual(add.length, 1);
  assert.strictEqual(add[0].textContent, "+ New profile");
  assert.strictEqual(t.els["pf-navlist"].children[t.els["pf-navlist"].children.length - 1], add[0]);
});

test("pfvRenderNav marks the shown profile", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const cur = byCls(t.els["pf-navlist"], "pfv-ni").filter((n) => n.getAttribute("aria-current") === "true");
  assert.deepStrictEqual(cur.map((n) => byCls(n, "pfv-ni-name")[0].textContent), ["aws-ops"]);
});

test("a nav item shows its profile", async () => {
  const t = page();
  t.box.pfvRenderNav();
  byCls(t.els["pf-navlist"], "pfv-ni")[3].click();
  await new Promise((r) => setTimeout(r, 0));
  assert.strictEqual(t.run("pfvState").name, "runbook-host");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-title-name"), ["runbook-host"]);
});

test("pfvShow fetches the stored profile and draws the seven blocks from the read model", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  assert.ok(t.calls.includes("GET /api/profiles/aws-ops"));
  assert.strictEqual(t.run("pfvState").name, "aws-ops");
  assert.strictEqual(t.run("pfvState").doc.rev, "r1");
  const root = t.els["pfv-in"];
  assert.deepStrictEqual(texts(root, "pfv-title-name"), ["aws-ops"]);
  assert.deepStrictEqual(texts(root, "pfv-chip"), ["box"]);
  assert.deepStrictEqual(texts(root, "pfv-hsub"), ["Claude Code · claude-opus-5-5 · high effort"]);
  assert.deepStrictEqual(texts(root, "pfv-sheet-h"), ["This agent will be able to"]);
  assert.deepStrictEqual(texts(root, "pfv-label").map((s) => s.replace(/\s*· .*$/, "")), ["Runs in", "Workspace", "Agent", "Cloud and tokens", "Also reachable", "Network", "Advanced"]);
  // Edit on every block of an editable profile, "+ Add" on the two row blocks; none of them acts yet.
  assert.strictEqual(byText(root, "button", "Edit").length, 7);
  assert.deepStrictEqual(byText(root, "button", "Edit").map((b) => b.getAttribute("aria-label")), [
    "Edit where it runs", "Edit workspace", "Edit agent", "Edit cloud and tokens", "Edit also reachable", "Edit network", "Edit advanced settings",
  ]);
  assert.strictEqual(byText(root, "button", "+ Add access").length, 1);
  assert.strictEqual(byText(root, "button", "+ Add folder or domain").length, 1);
  assert.ok(byText(root, "button", "Edit").every((b) => !b.onclick), "Edit does nothing until the edit task");
  // Rows come from reachModel: a PROD tag on the prod connection, the production tag on the token.
  const rows = byCls(root, "pfv-row");
  assert.ok(rows.some((r) => texts(r, "pfv-tag").join() === "PROD" && /prod-read/.test(r.textContent)));
  assert.ok(rows.some((r) => texts(r, "pfv-tag").join() === "production" && /grafana-prod/.test(r.textContent)));
  assert.deepStrictEqual(texts(root, "pfv-tnote"), ["Long-lived; the agent can read it while it runs"]);
});

test("the page reads the meta lists only once /api/meta has answered", async () => {
  // Before it has, no connection or token is called missing: nothing is known of either list.
  const t = page({ metaLoaded: false, awscEntries: [], stEntries: [] });
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-miss").length, 0);
  const t2 = page({ awscEntries: [], stEntries: [] });
  await t2.box.pfvShow("aws-ops");
  assert.ok(byCls(t2.els["pfv-in"], "pfv-miss").length >= 1, "once meta answered, a bound name it lacks is missing");
});

test("a missing credential row reads Not found in Credentials, in the error colour, with Remove", async () => {
  const t = page({ awscEntries: [{ name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" }] });
  await t.box.pfvShow("aws-ops");
  const miss = byCls(t.els["pfv-in"], "pfv-miss");
  assert.strictEqual(miss.length, 1);
  assert.strictEqual(miss[0].textContent, "Not found in Credentials");
  const scope = miss[0].parent;
  assert.match(scope.textContent, /^Not found in Credentials · Remove$/);
  assert.strictEqual(byText(scope, "button", "Remove").length, 1);
});

test("a host profile has Can reach, Switch to box and no cloud blocks", async () => {
  const t = page();
  await t.box.pfvShow("runbook-host");
  const root = t.els["pfv-in"];
  assert.deepStrictEqual(texts(root, "pfv-chip"), ["host"]);
  assert.ok(byCls(root, "pfv-chip")[0].className.includes("pfv-chip-host"));
  assert.deepStrictEqual(texts(root, "pfv-label").map((s) => s.replace(/\s*· .*$/, "")), ["Runs in", "Workspace", "Agent", "Can reach", "Advanced"]);
  // Spec 5.4 sentence, one line.
  assert.deepStrictEqual(texts(byCls(root, "pfv-block")[0], "pfv-main"), ["On this machine: runs as you, with your files, your network and your credentials"]);
  assert.strictEqual(byText(root, "button", "Switch to box").length, 1);
  assert.strictEqual(byCls(root, "pfv-add").length, 0);
});

test("a read-only profile has no Edit, the config.json note, Duplicate as the primary and no menu", async () => {
  const t = page();
  await t.box.pfvShow("claude");
  const root = t.els["pfv-in"];
  assert.strictEqual(byText(root, "button", "Edit").length, 0);
  assert.deepStrictEqual(texts(root, "pfv-note"), ["Defined in config.json. Duplicate it to change it."]);
  const dup = byText(root, "button", "Duplicate");
  assert.strictEqual(dup.length, 1);
  assert.ok(dup[0].className.includes("pfv-btn-primary"));
  assert.strictEqual(byText(root, "button", "Start session").length, 1);
  assert.strictEqual(byCls(root, "pfv-menu").length, 0);
  assert.strictEqual(byCls(root, "pfv-btn-icon").length, 0);
  assert.strictEqual(byText(root, "button", "Switch to box").length, 0, "a profile you cannot edit has nothing to switch");
});

test("an editable profile has Start session and a menu with Duplicate and a red Delete…", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const root = t.els["pfv-in"];
  const [start] = byText(root, "button", "Start session");
  assert.ok(start.className.includes("pfv-btn-secondary"));
  const menu = byCls(root, "pfv-menu")[0];
  assert.ok(menu.hidden, "closed until the ⋯ button opens it");
  const items = byCls(menu, "pfv-menu-item");
  assert.deepStrictEqual(items.map((i) => i.textContent), ["Duplicate", "Delete…"]);
  assert.ok(items[1].className.includes("pfv-menu-red"));
  const more = byCls(root, "pfv-btn-icon")[0];
  assert.strictEqual(more.getAttribute("aria-label"), "More actions for aws-ops");
  assert.strictEqual(more.getAttribute("aria-expanded"), "false");
  more.click();
  assert.strictEqual(menu.hidden, false);
  assert.strictEqual(more.getAttribute("aria-expanded"), "true");
  more.click();
  assert.strictEqual(menu.hidden, true);
});

test("Start session preselects the profile", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  byText(t.els["pfv-in"], "button", "Start session")[0].click();
  assert.deepStrictEqual(t.calls.filter((c) => c.startsWith("spawn:")), ["spawn:aws-ops"]);
});

test("a private copy shows its branches that did not come home in the Workspace block", async () => {
  const nh = { base: "master", branches: [{ branch: "fix-drift", reason: "not-ff" }, { branch: "bump", reason: "checked-out" }] };
  const prof = view("gitops-clone", { default_cwd: "/home/you/box-clones/k8s-gitops", clone_of: "/home/you/work/k8s-gitops", auto_return: true, not_home: nh });
  const t = page({ profiles: [prof] });
  const real = t.box.api;
  t.box.api = async (p, o) => (p === "/api/profiles/gitops-clone" ? { name: "gitops-clone", bin: "claude", runtime: "box" } : real(p, o));
  await t.box.pfvShow("gitops-clone");
  const ws = byCls(t.els["pfv-in"], "pfv-block").find((b) => b.dataset.block === "workspace");
  assert.deepStrictEqual(texts(ws, "pfv-nh-chip"), ["2 branches not home"]);
  assert.strictEqual(byCls(ws, "pfv-nh-chip")[0].getAttribute("title").split("\n").length, 2, "the chip's title is notHomeTitle");
  assert.strictEqual(byCls(ws, "pfv-nh-row").length, 2);
  assert.deepStrictEqual(texts(ws, "pfv-nh-why")[0], "box/fix-drift on the host has commits this branch lacks");
  assert.match(texts(ws, "pfv-nh-fix")[0], /overwrites box\/fix-drift$/);
});

test("boxes unavailable on a box profile: the reason line is red, the consequence is not", async () => {
  const t = page({ boxRuntime: "", boxUnavailableReason: "Docker is not installed" });
  await t.box.pfvShow("aws-ops");
  const runs = byCls(t.els["pfv-in"], "pfv-block")[0];
  assert.deepStrictEqual(texts(runs, "pfv-main"), ["In a box"]);
  assert.deepStrictEqual(texts(runs, "pfv-red"), ["Boxes are unavailable on this machine: Docker is not installed."]);
  const sub = byCls(runs, "pfv-sub");
  assert.strictEqual(sub.length, 2);
  assert.ok(!sub[1].className.includes("pfv-red"));
  assert.strictEqual(sub[1].textContent, "Sessions with this profile can't start until Docker is installed.");
});

test("Advanced is folded with its count, and opens by itself when something is set", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  let adv = byCls(t.els["pfv-in"], "pfv-block").find((b) => b.dataset.block === "advanced");
  assert.strictEqual(texts(adv, "pfv-label")[0], "Advanced · none set");
  assert.strictEqual(byCls(adv, "pfv-fold").length, 0, "nothing to unfold: no dead toggle");
  assert.strictEqual(byCls(adv, "pfv-adv-sum").length, 1);

  DOCS["aws-ops"].args = ["--verbose"];
  try {
    await t.box.pfvShow("aws-ops");
    adv = byCls(t.els["pfv-in"], "pfv-block").find((b) => b.dataset.block === "advanced");
    assert.strictEqual(texts(adv, "pfv-label")[0], "Advanced · 1 set");
    const fold = byCls(adv, "pfv-fold")[0];
    assert.strictEqual(fold.getAttribute("aria-expanded"), "true");
    assert.deepStrictEqual(texts(adv, "pfv-dl"), ["Arguments--verbose"]);
    fold.click();
    adv = byCls(t.els["pfv-in"], "pfv-block").find((b) => b.dataset.block === "advanced");
    assert.strictEqual(byCls(adv, "pfv-fold")[0].getAttribute("aria-expanded"), "false");
    assert.strictEqual(byCls(adv, "pfv-dl").length, 0);
  } finally {
    delete DOCS["aws-ops"].args;
  }
});

test("pfvDelete confirms in red, deletes, and shows the next profile in list order", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  t.box.gone = "aws-ops";
  await t.box.pfvDelete();
  assert.ok(t.calls.includes("confirm:Delete profile:danger"));
  assert.ok(t.calls.includes("DELETE /api/profiles/aws-ops"));
  assert.strictEqual(t.run("pfvState").name, "infra-terraform", "the profile that followed it in the list");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-title-name"), ["infra-terraform"]);
});

test("deleting the last profile in the list shows the one before it", async () => {
  const t = page();
  await t.box.pfvShow("runbook-host");
  t.box.gone = "runbook-host";
  await t.box.pfvDelete();
  assert.strictEqual(t.run("pfvState").name, "infra-terraform");
});

test("a declined confirm deletes nothing, and a failed delete keeps the profile on screen with the error", async () => {
  const t = page({ confirmAnswer: false });
  await t.box.pfvShow("aws-ops");
  await t.box.pfvDelete();
  assert.ok(!t.calls.some((c) => c.startsWith("DELETE")));
  assert.strictEqual(t.run("pfvState").name, "aws-ops");

  const f = page();
  await f.box.pfvShow("aws-ops");
  f.box.apiFail = new Error("boom");
  await f.box.pfvDelete();
  assert.strictEqual(f.run("pfvState").name, "aws-ops");
  assert.deepStrictEqual(texts(f.els["pfv-in"], "pfv-err"), ["Delete failed: boom"]);
});

test("the menu's Delete… runs pfvDelete", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  t.box.gone = "aws-ops";
  const del = byText(t.els["pfv-in"], "button", "Delete…")[0];
  await del.click();
  assert.ok(t.calls.includes("DELETE /api/profiles/aws-ops"));
  assert.notStrictEqual(t.run("pfvState").name, "aws-ops");
});

test("the page is a no-op on a phone's modal (no .as-page)", async () => {
  const t = page();
  t.els["settings-modal"].classList.remove("as-page");
  await t.box.pfvShow("aws-ops");
  assert.deepStrictEqual(t.calls, []);
  assert.strictEqual(t.els["pfv-in"].children.length, 0);
});

test("a slower answer for the profile you left does not replace the one on screen", async () => {
  const t = page();
  let release;
  const slow = new Promise((r) => { release = r; });
  const real = t.box.api;
  t.box.api = async (p, o) => { if (p === "/api/profiles/aws-ops") { await slow; return DOCS["aws-ops"]; } return real(p, o); };
  const first = t.box.pfvShow("aws-ops");
  await t.box.pfvShow("infra-terraform");
  release();
  await first;
  assert.strictEqual(t.run("pfvState").name, "infra-terraform");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-title-name"), ["infra-terraform"]);
});

test("machine text (paths, hosts, ids, env names) is drawn in the mono class, words are not", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const mono = texts(t.els["pfv-in"], "pfv-mono");
  for (const want of ["claude-opus-5-5", "~/work/payments-api", "222222222222", "eu-west-1", "GRAFANA_SERVICE_ACCOUNT_TOKEN", "grafana.acme.example.com"]) {
    assert.ok(mono.includes(want), want + " is machine text");
  }
  assert.ok(!mono.some((m) => /Read only|Edits the folder/.test(m)));
});

test("the nav is not drawn in a phone's modal", () => {
  const t = page();
  t.els["settings-modal"].classList.remove("as-page");
  t.box.pfvRenderNav();
  assert.strictEqual(t.els["pf-navlist"].children.length, 0);
});

test("a credential that /api/meta lists later stops being missing (pfvMetaChanged redraws)", async () => {
  const t = page({ awscEntries: [{ name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" }] });
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-miss").length, 1);
  t.box.awscEntries = t.box.awscEntries.concat([{ name: "staging-read", account_id: "111111111111", region: "eu-west-1" }]);
  t.box.pfvMetaChanged();
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-miss").length, 0);
});

test("a refetched profile list redraws the page from the fresh row", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-chip"), ["box"]);
  // The list is fetched again (app.js loadProfiles -> renderProfileList -> pfvRenderNav) and the row changed.
  t.box.profiles = PROFILES.map((p) => (p.name === "aws-ops" ? Object.assign({}, p, { runtime: "host" }) : p));
  t.box.pfvRenderNav();
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-chip"), ["host"]);
});

// --- delete keeps the page steady -------------------------------------------------

test("a deleted profile leaves the screen as soon as the DELETE succeeds, before the list reloads", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  let release;
  t.box.loadProfiles = () => new Promise((r) => { release = () => r(true); });
  const done = t.box.pfvDelete();
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
  assert.ok(t.calls.includes("DELETE /api/profiles/aws-ops"));
  assert.strictEqual(t.run("pfvState").name, "", "nothing selected while the reload is out");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-title-name").length, 0, "the deleted profile's header, Start session and menu are gone");
  assert.ok(!byCls(t.els["pf-navlist"], "pfv-ni-name").some((n) => n.textContent === "aws-ops"), "and its nav row");
  release();
  await done;
  assert.strictEqual(t.run("pfvState").name, "infra-terraform");
});

test("a failed list reload cannot bring the deleted profile back, even when it was the only one", async () => {
  const lone = page({ profiles: [PROFILES[1]] });
  lone.box.loadProfiles = async () => false; // the reload failed and left the list as it was
  await lone.box.pfvShow("aws-ops");
  await lone.box.pfvDelete();
  assert.strictEqual(lone.run("pfvState").name, "");
  assert.strictEqual(byCls(lone.els["pfv-in"], "pfv-title-name").length, 0);
  assert.ok(!lone.calls.some((c) => c === "GET /api/profiles/aws-ops" && lone.calls.indexOf(c) !== lone.calls.lastIndexOf(c)), "never fetched again");

  const many = page();
  many.box.loadProfiles = async () => false;
  await many.box.pfvShow("aws-ops");
  await many.box.pfvDelete();
  assert.strictEqual(many.run("pfvState").name, "infra-terraform");
  assert.ok(!byCls(many.els["pf-navlist"], "pfv-ni-name").some((n) => n.textContent === "aws-ops"));
});

test("if you moved to another profile while the DELETE was out, that profile stays", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  let release;
  const real = t.box.api;
  t.box.api = async (p, o) => { if (o && o.method === "DELETE") { await new Promise((r) => { release = r; }); } return real(p, o); };
  const done = t.box.pfvDelete();
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
  await t.box.pfvShow("runbook-host");
  release();
  await done;
  assert.strictEqual(t.run("pfvState").name, "runbook-host");
});

// --- an open block is never redrawn from under the user -----------------------------

test("showing the same profile again (tab select) while a block is open fetches nothing and redraws nothing", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  t.run('pfvState.openBlock = "agent"');
  const before = t.els["pfv-in"].children.slice();
  const calls = t.calls.length;
  await t.box.pfvShow();
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(t.calls.length, calls, "no GET");
  assert.ok(t.els["pfv-in"].children.every((c, i) => c === before[i]), "the same nodes");
  // The edit tasks decide leaving: an explicit move to another profile still goes.
  await t.box.pfvShow("infra-terraform");
  assert.strictEqual(t.run("pfvState").name, "infra-terraform");
  assert.strictEqual(t.run("pfvState").openBlock, "");
});

test("an answer that lands after a block was opened does not replace the stored profile", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  let release;
  const real = t.box.api;
  t.box.api = async (p, o) => { if (p === "/api/profiles/aws-ops") { await new Promise((r) => { release = r; }); return Object.assign({}, DOCS["aws-ops"], { rev: "r9" }); } return real(p, o); };
  const slow = t.box.pfvShow("aws-ops"); // the tab was selected again
  await new Promise((r) => setTimeout(r, 0));
  t.run('pfvState.openBlock = "agent"'); // and the user started editing meanwhile
  release();
  await slow;
  assert.strictEqual(t.run("pfvState").doc.rev, "r1");
});

// --- a refetch that changes nothing changes nothing on screen ----------------------

test("a list refetch with the same data keeps the nav, the page, the focus and the open menu", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const navKids = t.els["pf-navlist"].children.slice();
  const pageKids = t.els["pfv-in"].children.slice();
  byCls(t.els["pfv-in"], "pfv-btn-icon")[0].click();
  const menu = byCls(t.els["pfv-in"], "pfv-menu")[0];
  assert.strictEqual(menu.hidden, false);
  const focused = navKids[2];
  focused.focus();
  t.box.profiles = PROFILES.map((p) => Object.assign({}, p)); // loadProfiles returns new objects every time
  t.box.pfvRenderNav();
  assert.ok(t.els["pf-navlist"].children.every((c, i) => c === navKids[i]), "the nav buttons are the same nodes");
  assert.ok(t.els["pfv-in"].children.every((c, i) => c === pageKids[i]), "the page is not rebuilt");
  assert.strictEqual(menu.hidden, false, "the menu stays open");
  assert.strictEqual(Node.focused, focused, "focus stays");
});

test("an /api/credentials answer that changes nothing does not redraw; one that changes a row does", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const kids = t.els["pfv-in"].children.slice();
  t.box.pfCredsView = JSON.parse(JSON.stringify(t.box.pfCredsView));
  t.box.pfvMetaChanged();
  assert.ok(t.els["pfv-in"].children.every((c, i) => c === kids[i]), "same nodes");
  t.box.pfCredsView = { store: { initialized: true, locked: false }, aws: [{ name: "prod-read", prod: true, elevate: { max_minutes: 45 } }] };
  t.box.pfvMetaChanged();
  assert.ok(t.els["pfv-in"].children.some((c, i) => c !== kids[i]), "a changed elevate window redraws");
  assert.match(t.els["pfv-in"].textContent, /max 45 min/);
});

test("when the list itself changes the nav is rebuilt and the focused row keeps focus by name", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const old = t.els["pf-navlist"].children.slice();
  old[3].focus(); // gitops... the fourth row
  const name = byCls(old[3], "pfv-ni-name")[0].textContent;
  t.box.profiles = PROFILES.concat([view("zzz-new")]);
  t.box.pfvRenderNav();
  const now = t.els["pf-navlist"].children;
  assert.strictEqual(now.length, old.length + 1);
  assert.notStrictEqual(now[3], old[3]);
  assert.ok(t.els["pf-navlist"].contains(Node.focused), "focus is on a button that is still in the nav, not on the one that was replaced");
  assert.strictEqual(byCls(Node.focused, "pfv-ni-name")[0].textContent, name);
});

test("clicking a nav row keeps its button and moves aria-current", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const items = t.els["pf-navlist"].children.slice();
  items[3].focus();
  await items[3].click();
  await new Promise((r) => setTimeout(r, 0));
  assert.ok(t.els["pf-navlist"].children.every((c, i) => c === items[i]), "no rebuild");
  assert.strictEqual(Node.focused, items[3]);
  assert.deepStrictEqual(t.els["pf-navlist"].children.filter((n) => n.getAttribute("aria-current") === "true").map((n) => byCls(n, "pfv-ni-name")[0].textContent), ["runbook-host"]);
});

// --- every class the page draws has a rule ------------------------------------------

test("every pfv- class in profilepage.js has a rule in desktop.css (classnames.test.js cannot see helper-built classes)", () => {
  const src = fs.readFileSync(path.join(STATIC, "profilepage.js"), "utf8");
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const used = new Set();
  for (const m of src.matchAll(/"([^"\n]*)"/g)) {
    for (const tok of m[1].split(/\s+/)) if (/^pfv-[a-z][a-z-]*[a-z]$/.test(tok)) used.add(tok);
  }
  assert.ok(used.size > 40, "the scan found the page's classes");
  const missing = [...used].filter((c) => !new RegExp("\\." + c + "(?![\\w-])").test(css));
  assert.deepStrictEqual(missing, []);
});

test("the file shared from a private copy's original is drawn mono, the CLAUDE.md imports row is not", async () => {
  const prof = view("gitops-clone", { default_cwd: "/home/you/box-clones/k8s-gitops", clone_of: "/home/you/work/k8s-gitops", clone_share: ["CLAUDE.md"], auto_return: true });
  const t = page({ profiles: [prof] });
  const real = t.box.api;
  t.box.api = async (p, o) => (p === "/api/profiles/gitops-clone" ? { name: "gitops-clone", bin: "claude", runtime: "box" } : real(p, o));
  await t.box.pfvShow("gitops-clone");
  const reach = byCls(t.els["pfv-in"], "pfv-block").find((b) => b.dataset.block === "reachable");
  const rows = byCls(reach, "pfv-row");
  assert.deepStrictEqual(rows.map((r) => texts(r, "pfv-k")[0]), ["Files", "Files"]);
  assert.deepStrictEqual(texts(rows[0], "pfv-nm"), ["CLAUDE.md"]);
  assert.deepStrictEqual(texts(rows[0], "pfv-mono"), ["CLAUDE.md"]);
  assert.deepStrictEqual(texts(rows[0], "pfv-scope"), ["From the original, read only"]);
  assert.deepStrictEqual(texts(rows[1], "pfv-mono"), [], "CLAUDE.md imports is words");
});
