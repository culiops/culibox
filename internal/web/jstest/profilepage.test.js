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
const APP = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
// A top-level function of app.js (not exported: app.js is the page itself), as source,
// so the page script runs against the real buildSeg / optApplies and not a copy of them.
function appFn(name) {
  const at = APP.indexOf("function " + name + "(");
  assert.ok(at >= 0, name + " is in app.js");
  return APP.slice(at, APP.indexOf("\n}\n", at) + 2);
}

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
  focus() { Node.focused = this; for (const f of (this.handlers.focus || []).slice()) f({ target: this }); }
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
const OPTION_DEFS = [
  { key: "model", label: "Model", type: "enum", choices: ["claude-opus-5-5", "claude-opus-5", "claude-sonnet-5"], bins: ["claude"] },
  { key: "effort", label: "Effort", type: "enum", choices: ["low", "medium", "high", "xhigh", "max"], bins: ["claude"] },
  { key: "skip_permissions", label: "Bypass permissions", type: "bool", bins: ["claude"] },
];

const ACCT = require("../static/accounts.js");
const PENDING = require("../static/pending.js");
// The page's pinned profile schema (app.js pfSchema), pinned like /api/meta does.
const SCHEMA = () => { const p = require("../static/profileform.js").schemaPin(); p.pin("sch-1"); return p; };
// An api() failure as app.js throws it: the sentence, the status and code, the parsed body.
const apiErr = (status, body) => Object.assign(new Error(body.error || "failed"), { status, code: body.code || "", body });

// The workspace picker (wspicker.js) has its own tests; here the Workspace editor is driven through a stand-in
// that records how it was built and lets a test do what the user does (pick, type, focus).
function stubPickers() {
  const made = [];
  const factory = (opts) => {
    const p = { opts, v: "", destroyed: false, opened: 0 };
    p.value = () => p.v;
    p.set = (x) => { p.v = x || ""; opts.input.value = p.v; };
    p.open = () => { p.opened++; p.shown = true; };
    p.close = () => { p.shown = false; };
    p.isOpen = () => p.shown === true;
    p.destroy = () => { p.destroyed = true; };
    p.pick = (x) => { p.v = x; opts.input.value = x; opts.onPick(x); };
    p.type = (x) => { opts.input.value = x; if (x.startsWith("/")) { p.v = x; opts.onPick(x); } else if (x === "") { p.v = ""; opts.onPick(""); } };
    // The real picker opens its list on focus unless asked not to (openOnFocus).
    opts.input.addEventListener("focus", () => { if (!opts.openOnFocus || opts.openOnFocus()) p.opened++; });
    made.push(p);
    return p;
  };
  return { made, factory };
}

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
  const pk = stubPickers();
  const box = Object.assign({
    document: doc, console, Promise, JSON, Date, Math, Array, Object, Set, String, Number, setTimeout, clearTimeout,
    window: { HOME_DIR: "/home/you" }, cloneUnavailable: "", createWsPicker: pk.factory, pickers: pk.made, pageLeaveHook: null,
    profiles: PROFILES.slice(), profilesLoaded: true, selectedProfile: "", metaLoaded: true,
    boxRuntime: "runc", boxUnavailableReason: "", bundleImports: true, optionDefs: OPTION_DEFS,
    accountsCache: [{ name: "work" }], acctChoices: ACCT.acctChoices, ACCT_DEFAULT: ACCT.ACCT_DEFAULT,
    withPending: PENDING.withPending, pfSchema: SCHEMA(), docs: {}, puts: [], revN: 1,
    awscEntries: [{ name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" }, { name: "staging-read", account_id: "111111111111", region: "eu-west-1" }],
    stEntries: [{ name: "grafana-prod", prod: true, env: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"], hosts: [{ host: "grafana.acme.example.com", port: 443 }] }],
    pfCredsView: { store: { initialized: true, locked: false }, aws: [] },
    ghApps: ["acme-app"], refreshCredLists: async () => { calls.push("refreshCredLists"); },
    awsErrText: (e) => "aws: " + e.message,
    CRED_D4: "The agent will be able to change this AWS account: create, modify and delete resources.",
    CRED_D4_REMEDY: "Use a role without write access as the base and put this one under Elevate.",
    api: async (p, opt) => {
      const method = (opt && opt.method) || "GET";
      calls.push(method + " " + p);
      if (box.apiFail) throw box.apiFail;
      const m = /^\/api\/profiles\/([^/]+)$/.exec(p);
      const name = m ? decodeURIComponent(m[1]) : "";
      if (m && method === "GET") {
        if (box.getFail) throw box.getFail;
        return box.docs[name] || DOCS[name];
      }
      if (m && method === "PUT") {
        const body = JSON.parse(opt.body);
        box.puts.push(body);
        if (box.putFail) throw box.putFail;
        // What the server keeps: the profile with a new revision, and no schema key.
        const kept = Object.assign({}, body, { rev: "r" + (++box.revN) + "0" });
        delete kept.schema;
        box.docs[name] = kept;
        return box.putReply || null;
      }
      return null;
    },
    openSettings: (tab) => { calls.push("openSettings:" + tab); },
    loadProfiles: async () => { calls.push("loadProfiles"); box.profiles = box.profiles.filter((p) => !box.gone || p.name !== box.gone); return true; },
    askConfirm: async (o) => { calls.push("confirm:" + o.title + ":" + (o.danger ? "danger" : "")); return box.confirmAnswer !== false; },
    openSpawnDialog: (n) => { calls.push("spawn:" + n); },
    setDialogError: (id, m) => { calls.push("error:" + m); },
  }, over);
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["fsops.js", "workhome.js", "workmode.js", "spawnpreview.js", "spawnform.js", "profileform.js", "importsmodel.js", "envrows.js", "profileview.js", "profileedit.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  for (const fn of ["buildSeg", "optApplies"]) new vm.Script(appFn(fn), { filename: "app.js:" + fn }).runInContext(ctx);
  for (const f of ["profilepage.js", "pfv-workspace.js", "pfv-cloud.js", "pfv-advanced.js"]) new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
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
  // Edit on every block of an editable profile, "+ Add" on the two row blocks.
  assert.strictEqual(byText(root, "button", "Edit").length, 7);
  assert.deepStrictEqual(byText(root, "button", "Edit").map((b) => b.getAttribute("aria-label")), [
    "Edit where it runs", "Edit workspace", "Edit agent", "Edit cloud and tokens", "Edit also reachable", "Edit network", "Edit advanced settings",
  ]);
  assert.strictEqual(byText(root, "button", "+ Add access").length, 1);
  assert.strictEqual(byText(root, "button", "+ Add folder or domain").length, 1);
  // Every block edits.
  assert.deepStrictEqual(byText(root, "button", "Edit").map((b) => !!b.onclick), [true, true, true, true, true, true, true]);
  assert.ok(byText(root, "button", "+ Add access")[0].onclick && byText(root, "button", "+ Add folder or domain")[0].onclick, "+ Add opens its block");
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

test("every pfv- class in the profile page scripts has a rule in desktop.css (classnames.test.js cannot see helper-built classes)", () => {
  const src = ["profilepage.js", "pfv-workspace.js", "pfv-cloud.js", "pfv-advanced.js"].map((f) => fs.readFileSync(path.join(STATIC, f), "utf8")).join("\n");
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


// --- editing one block at a time (spec 5.5, 5.6) ----------------------------------------

const field = (t, key) => t.els["pfv-in"].all((n) => n.dataset && n.dataset.pfvField === key)[0];
const blockOf = (t, id) => byCls(t.els["pfv-in"], "pfv-block").find((b) => b.dataset.block === id);
const clickBtn = (root, text) => byText(root, "button", text)[0].click();
const settle = () => new Promise((r) => setTimeout(r, 0));
const pickModel = (t, v) => { const m = field(t, "options.model"); m.value = v; m.onchange(); };
// aws-ops with a readable folder, so Also reachable has a Folder row to put an error under.
const withFolder = () => PROFILES.map((p) => (p.name === "aws-ops" ? Object.assign({}, p, { context_paths: ["/home/you/other-repo/docs"] }) : p));
// The block's form reads the stored profile, which holds the folder too.
const withFolderDoc = (t) => { t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { context_paths: ["/home/you/other-repo/docs"] }); return t; };
const gone400 = () => apiErr(400, { error: 'profile "aws-ops": context_paths[0] "/home/you/other-repo/docs": does not exist', field: "context_paths[0]" });

test("Edit opens the Agent block as a form: agent, model, effort, login; the rest stays read-only", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  const agent = blockOf(t, "agent");
  assert.ok(agent.className.includes("pfv-editing"));
  assert.strictEqual(t.run("pfvState").openBlock, "agent");
  assert.strictEqual(byText(agent, "button", "Edit").length, 0, "the Edit link goes while the block is open");
  assert.strictEqual(field(t, "bin").value, "claude");
  assert.strictEqual(field(t, "options.model").value, "claude-opus-5-5");
  assert.strictEqual(field(t, "options.effort").dataset.value, "high");
  assert.strictEqual(field(t, "account").value, "");
  assert.deepStrictEqual(byCls(agent, "pfv-flabel").map((n) => n.textContent), ["Agent", "Model", "Effort", "Claude login"]);
  assert.strictEqual(byCls(blockOf(t, "network"), "pfv-editing").length, 0);
  assert.strictEqual(byText(blockOf(t, "network"), "button", "Edit").length, 1);
  assert.strictEqual(Node.focused, field(t, "bin"), "focus moves into the form");
});

test("no unsaved changes until something changes; then the header says so with Show, and the footer Discard", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  const head = byCls(t.els["pfv-in"], "pfv-unsaved")[0];
  assert.ok(head.hidden, "a form that was opened and not touched holds no changes");
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.strictEqual(byText(blockOf(t, "agent"), "button", "Cancel").length, 1);
  pickModel(t, "claude-sonnet-5");
  assert.strictEqual(head.hidden, false);
  assert.strictEqual(head.textContent, "Unsaved changes" + "Show");
  assert.strictEqual(t.run("pfvState").pending, true);
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-dirty"), ["Unsaved changes"]);
  assert.strictEqual(byText(blockOf(t, "agent"), "button", "Discard").length, 1);
  assert.strictEqual(byText(blockOf(t, "agent"), "button", "Cancel").length, 0);
  pickModel(t, "claude-opus-5-5");
  assert.ok(head.hidden, "back to what was stored is no change");
});

test("save sends the merged profile with rev and schema, then shows it read-only with Saved", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  const ok = await clickBtn(blockOf(t, "agent"), "Save");
  assert.strictEqual(ok, true);
  assert.strictEqual(t.box.puts.length, 1);
  const body = t.box.puts[0];
  assert.strictEqual(body.rev, "r1");
  assert.strictEqual(body.schema, "sch-1");
  assert.strictEqual(body.options.model, "claude-sonnet-5");
  assert.strictEqual(body.options.effort, "high", "the other agent option is carried as stored");
  assert.strictEqual(body.default_cwd, "/home/you/work/payments-api", "keys of other blocks are untouched");
  assert.deepStrictEqual(t.calls.filter((c) => c === "GET /api/profiles/aws-ops").length, 2, "one GET to show it, one after the save");
  assert.ok(t.calls.includes("loadProfiles"));
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0);
  assert.strictEqual(t.run("pfvState").openBlock, "");
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-hsub"), ["Claude Code · claude-sonnet-5 · high effort"], "the read view shows the new model");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-saved"), ["Saved"]);
  t.run("pfvExpireSaved()");
  assert.ok(byCls(blockOf(t, "agent"), "pfv-saved").every((n) => n.hidden), "Saved goes after a moment");
});

test("second block save uses the rev from the refetch", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.strictEqual(t.box.puts[0].rev, "r1");
  clickBtn(blockOf(t, "network"), "Edit");
  const open = field(t, "egress_open");
  open.checked = true;
  open.onchange();
  await clickBtn(blockOf(t, "network"), "Save");
  assert.strictEqual(t.box.puts.length, 2);
  assert.strictEqual(t.box.puts[1].rev, "r20", "the revision the server gave after the first save, not the one the page opened at");
  assert.strictEqual(t.box.puts[1].egress_open, true);
  assert.strictEqual(t.box.puts[1].options.model, "claude-sonnet-5", "and it carries the first save");
});

test("a 409 shows changed-elsewhere inside the block and keeps nothing pending after Reload", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(409, { error: "the profile changed", code: "profile_changed" });
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), false);
  const agent = blockOf(t, "agent");
  assert.deepStrictEqual(texts(agent, "pfv-top-err"), ["This profile changed elsewhere Reload"]);
  assert.strictEqual(byText(agent, "button", "Reload").length, 1);
  assert.strictEqual(t.run("pfvState").pending, true, "the change is still there until Reload");
  assert.strictEqual(field(t, "options.model").value, "claude-sonnet-5");
  // The profile as it is now: somebody changed the model.
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { rev: "r7", options: { model: "claude-opus-5", effort: "high" } });
  await clickBtn(agent, "Reload");
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.strictEqual(t.run("pfvState").openBlock, "");
  assert.strictEqual(t.run("pfvState").doc.rev, "r7");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0);
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-hsub"), ["Claude Code · claude-opus-5 · high effort"]);
  assert.strictEqual(t.box.puts.length, 1, "Reload writes nothing");
});

test("a 409 for a profile deleted meanwhile says so, offers no Reload, and moves to the next profile", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(409, { error: "the profile changed", code: "profile_changed", gone: true });
  await clickBtn(blockOf(t, "agent"), "Save");
  const agent = blockOf(t, "agent");
  assert.match(texts(agent, "pfv-top-err")[0], /^This profile was deleted elsewhere/);
  assert.strictEqual(byText(agent, "button", "Reload").length, 0);
  t.box.gone = "aws-ops"; // the list no longer has it
  await clickBtn(agent, "Show another profile");
  assert.strictEqual(t.run("pfvState").name, "infra-terraform", "the profile that followed it, like Delete");
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.strictEqual(t.run("pfvState").openBlock, "");
  assert.ok(!byCls(t.els["pf-navlist"], "pfv-ni-name").some((n) => n.textContent === "aws-ops"));
});

test("a Reload that finds the profile gone says so too", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(409, { error: "the profile changed", code: "profile_changed" });
  await clickBtn(blockOf(t, "agent"), "Save");
  t.box.getFail = apiErr(404, { error: "no such profile" });
  await clickBtn(blockOf(t, "agent"), "Reload");
  assert.match(texts(blockOf(t, "agent"), "pfv-top-err")[0], /^This profile was deleted elsewhere/);
  assert.strictEqual(byText(blockOf(t, "agent"), "button", "Reload").length, 0);
});

test("a 400 with field context_paths[0] while editing agent opens reachable too and keeps the agent change", async () => {
  const t = withFolderDoc(page({ profiles: withFolder() }));
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = gone400();
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), false);
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), ["agent", "reachable"], "the one-open-block rule yields to repair");
  assert.strictEqual(t.run("pfvState").pending, true);
  assert.strictEqual(field(t, "options.model").value, "claude-sonnet-5", "the agent change is kept");
  const reach = blockOf(t, "reachable");
  assert.ok(reach.className.includes("pfv-editing"));
  // The error sits under the folder row, in words, not the server's sentence with its keys.
  const row = byCls(reach, "pfv-row")[0];
  assert.deepStrictEqual(texts(row, "pfv-ferr"), ["This folder no longer exists. Remove it or pick another."]);
  assert.ok(!/context_paths/.test(t.els["pfv-in"].textContent));
  // The Agent block says what is wrong and where, with a way there (approved state err2).
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-top-err"), ["Not saved: a folder in Also reachable needs a fix first. Show"]);
  // Both footers: the repair one is clean (Cancel) and says what Save also saves; the agent one is dirty (Discard).
  assert.deepStrictEqual(texts(reach, "pfv-st-invalid"), ["One field needs a fix before this can save."]);
  assert.ok(texts(reach, "pfv-fnote").includes("Saving also saves your Agent change."));
  assert.strictEqual(byText(reach, "button", "Cancel").length, 1);
  assert.strictEqual(byText(blockOf(t, "agent"), "button", "Discard").length, 1);
  // Saving from the repair block saves the same merged profile, agent change included.
  t.box.putFail = null;
  assert.strictEqual(await clickBtn(reach, "Save"), true);
  assert.strictEqual(t.box.puts.length, 2);
  assert.strictEqual(t.box.puts[1].options.model, "claude-sonnet-5");
  assert.strictEqual(t.run("pfvState").openBlock, "");
  assert.strictEqual(Object.keys(t.run("pfvState").edits).length, 0);
});

test("cancelling the repair block keeps the other block's change; its note about the repair goes", async () => {
  const t = withFolderDoc(page({ profiles: withFolder() }));
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = gone400();
  await clickBtn(blockOf(t, "agent"), "Save");
  clickBtn(blockOf(t, "reachable"), "Cancel");
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), ["agent"]);
  assert.strictEqual(t.run("pfvState").pending, true);
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-top-err"), [], "the block it pointed at is closed");
  assert.strictEqual(field(t, "options.model").value, "claude-sonnet-5");
});

test("a 400 naming a field in the open block puts the words under that input and keeps the value", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": options.model: "claude-sonnet-5" is not one of the allowed values', field: "options.model" });
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), ["agent"]);
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-ferr"), ['options.model: "claude-sonnet-5" is not one of the allowed values']);
  assert.ok(field(t, "options.model").className.includes("pfv-bad"));
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-invalid"), ["One field needs a fix before this can save."]);
  // Changing that field clears its error.
  pickModel(t, "claude-opus-5");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-ferr"), []);
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-invalid"), []);
});

test("an error no block owns shows at the top of the open block, and a network failure says it could not save", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": something the server invented' });
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-top-err"), ["something the server invented"]);
  t.box.putFail = new TypeError("Failed to fetch");
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-top-err"), ["Could not save this profile."]);
  assert.strictEqual(t.run("pfvState").pending, true, "nothing was lost");
});

test("save sends runtime explicitly", async () => {
  const t = page();
  t.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" }; // names no tier
  await t.box.pfvShow("infra-terraform");
  clickBtn(blockOf(t, "runs"), "Edit");
  await clickBtn(blockOf(t, "runs"), "Save");
  assert.strictEqual(t.box.puts[0].runtime, "box", "the tier the form showed, not a blank that hands it back to the instance default");
  // With no box layer a blank tier stays blank (runtimeToSend): the server refuses an explicit box there.
  const n = page({ boxRuntime: "" });
  n.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" };
  await n.box.pfvShow("infra-terraform");
  clickBtn(blockOf(n, "runs"), "Edit");
  await clickBtn(blockOf(n, "runs"), "Save");
  assert.ok(!("runtime" in n.box.puts[0]));
});

test("discard restores the read view with no PUT", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  clickBtn(blockOf(t, "agent"), "Discard");
  assert.strictEqual(t.box.puts.length, 0);
  assert.strictEqual(t.run("pfvState").openBlock, "");
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0);
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-hsub"), ["Claude Code · claude-opus-5-5 · high effort"]);
  clickBtn(blockOf(t, "agent"), "Edit");
  assert.strictEqual(field(t, "options.model").value, "claude-opus-5-5", "the form opens on what is stored, not the discarded pick");
});

test("Edit on another block while one has changes is refused in place; a block with no changes just yields", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  clickBtn(blockOf(t, "network"), "Edit"); // nothing changed in Agent yet
  assert.strictEqual(t.run("pfvState").openBlock, "network");
  assert.ok(!blockOf(t, "agent").className.includes("pfv-editing"), "the unchanged Agent form closed in favour of the new one");
  clickBtn(blockOf(t, "network"), "Cancel");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  clickBtn(blockOf(t, "network"), "Edit");
  assert.strictEqual(t.run("pfvState").openBlock, "agent", "refused");
  assert.ok(!blockOf(t, "network").className.includes("pfv-editing"));
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-nudge"), ["Save or discard these changes first"]);
  assert.ok(blockOf(t, "agent").className.includes("pfv-nudge"));
  assert.strictEqual(field(t, "options.model").value, "claude-sonnet-5", "and nothing is lost");
  // The next edit takes the nudge away.
  pickModel(t, "claude-opus-5");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-nudge"), []);
});

test("a failed GET that lands after a block was opened does not redraw over it", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  let fail;
  const real = t.box.api;
  t.box.api = async (p, o) => { if (p === "/api/profiles/aws-ops" && !(o && o.method)) { await new Promise((r) => { fail = r; }); throw new Error("offline"); } return real(p, o); };
  const slow = t.box.pfvShow("aws-ops"); // the tab was selected again
  await settle();
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  const form = field(t, "options.model");
  fail();
  await slow;
  assert.strictEqual(field(t, "options.model"), form, "the same control, not a redraw");
  assert.strictEqual(form.value, "claude-sonnet-5");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-err"), []);
});

test("an /api/credentials answer that differs only in a field the page does not read does not redraw", async () => {
  const t = page();
  t.box.pfCredsView = { store: { initialized: true, locked: false }, aws: [{ name: "prod-read", prod: true, checked_at: "2026-11-06T10:00:00Z" }] };
  await t.box.pfvShow("aws-ops");
  const kids = t.els["pfv-in"].children.slice();
  t.box.pfCredsView = { store: { initialized: true, locked: false }, aws: [{ name: "prod-read", prod: true, checked_at: "2026-11-06T10:00:20Z" }] };
  t.box.pfvMetaChanged();
  assert.ok(t.els["pfv-in"].children.every((c, i) => c === kids[i]), "checked_at moves every few seconds and says nothing the page draws");
  t.box.pfCredsView = { store: { initialized: true, locked: false }, aws: [{ name: "prod-read", prod: true, elevate: { max_minutes: 45 } }] };
  t.box.pfvMetaChanged();
  assert.match(t.els["pfv-in"].textContent, /max 45 min/);
});

test("Manage goes to Agent accounts through the leave guard", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  // Nothing changed: no question.
  await clickBtn(blockOf(t, "agent"), "Manage");
  assert.deepStrictEqual(t.calls.filter((c) => c.startsWith("openSettings") || c.startsWith("confirm")), ["openSettings:accounts"]);
  // Changed: the question, with its own words on both buttons. Keep editing stays.
  t.calls.length = 0;
  let asked = null;
  t.box.askConfirm = async (o) => { asked = o; return false; };
  pickModel(t, "claude-sonnet-5");
  await clickBtn(blockOf(t, "agent"), "Manage");
  assert.strictEqual(asked.confirmLabel, "Discard");
  assert.strictEqual(asked.cancelLabel, "Keep editing");
  assert.ok(!t.calls.includes("openSettings:accounts"));
  assert.strictEqual(t.run("pfvState").pending, true);
  // Discard: the changes go and the page is left.
  t.box.askConfirm = async () => true;
  await clickBtn(blockOf(t, "agent"), "Manage");
  assert.ok(t.calls.includes("openSettings:accounts"));
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.strictEqual(t.run("pfvState").openBlock, "");
});

test("pfvLeaveGuard asks only when something changed and Discard drops it", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(await t.box.pfvLeaveGuard("leave"), true, "nothing open");
  clickBtn(blockOf(t, "agent"), "Edit");
  assert.strictEqual(await t.box.pfvLeaveGuard("leave"), true, "open but unchanged");
  assert.ok(!t.calls.some((c) => c.startsWith("confirm")));
  pickModel(t, "claude-sonnet-5");
  t.box.confirmAnswer = false;
  assert.strictEqual(await t.box.pfvLeaveGuard("leave"), false);
  assert.strictEqual(t.run("pfvState").pending, true);
  t.box.confirmAnswer = true;
  assert.strictEqual(await t.box.pfvLeaveGuard("leave"), true);
  assert.strictEqual(t.run("pfvState").pending, false);
});

test("Runs in: Box and On this machine; Box is disabled with the reason when no box layer exists", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  assert.strictEqual(field(t, "tier-box").checked, true);
  assert.strictEqual(field(t, "tier-box").disabled, false);
  assert.strictEqual(field(t, "tier-host").checked, false);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-card-t").map((s) => s.replace(/\s*Recommended$/, "")), ["In a box", "On this machine"]);
  assert.match(blockOf(t, "runs").textContent, /Docker found/);
  assert.match(blockOf(t, "runs").textContent, /Runs as you, with your files, your network and your credentials\./);

  const n = page({ boxRuntime: "", boxUnavailableReason: "Docker is not installed" });
  await n.box.pfvShow("aws-ops");
  clickBtn(blockOf(n, "runs"), "Edit");
  assert.strictEqual(field(n, "tier-box").disabled, true);
  assert.match(blockOf(n, "runs").textContent, /Boxes are unavailable on this machine: Docker is not installed\./);
  assert.ok(!/Docker found/.test(blockOf(n, "runs").textContent));
});

test("Runs in: a private copy cannot choose host; the refusal shows in place and Save is hidden", async () => {
  const prof = view("gitops-clone", { default_cwd: "/home/you/box-clones/k8s-gitops", clone_of: "/home/you/work/k8s-gitops", auto_return: true });
  const t = page({ profiles: [prof] });
  t.box.docs["gitops-clone"] = { name: "gitops-clone", bin: "claude", runtime: "box", clone_of: "/home/you/work/k8s-gitops", rev: "r5" };
  await t.box.pfvShow("gitops-clone");
  clickBtn(blockOf(t, "runs"), "Edit");
  const runs = blockOf(t, "runs");
  assert.strictEqual(byCls(runs, "pfv-refuse").length, 0);
  assert.strictEqual(byText(runs, "button", "Save").length, 1);
  const host = field(t, "tier-host");
  host.checked = true;
  host.onchange();
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-refuse").map((s) => s.replace(/\s*Open Workspace$/, "")), ["A private copy needs a box. Set Workspace to 'Work in this folder' first."]);
  assert.strictEqual(byText(blockOf(t, "runs"), "button", "Open Workspace").length, 1);
  assert.ok(byText(blockOf(t, "runs"), "button", "Save").every((b) => b.hidden), "Save is hidden while the refusal stands");
  assert.strictEqual(byText(blockOf(t, "runs"), "button", "Discard").length, 1);
  assert.strictEqual(t.box.puts.length, 0);
  // Back to the box: the refusal and the Save return.
  const box = field(t, "tier-box");
  box.checked = true;
  box.onchange();
  assert.strictEqual(byCls(blockOf(t, "runs"), "pfv-refuse").length, 0);
  assert.ok(byText(blockOf(t, "runs"), "button", "Save").every((b) => !b.hidden));
});

test("Runs in to On this machine saves through the same pipeline; a refusal from the server shows in words in the block it names", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  const host = field(t, "tier-host");
  host.checked = true;
  host.onchange();
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": tokens[0]: requires runtime: box', field: "tokens[0]" });
  await clickBtn(blockOf(t, "runs"), "Save as host…");
  assert.strictEqual(t.box.puts[0].runtime, "host");
  const cloud = blockOf(t, "cloud");
  assert.ok(cloud.className.includes("pfv-editing"), "the block the server named opens");
  assert.ok(texts(cloud, "pfv-ferr").concat(texts(cloud, "pfv-top-err")).some((s) => /Set Runs in to Box/.test(s)));
  assert.ok(!/tokens\[0\]|requires runtime/.test(t.els["pfv-in"].textContent));
});

test("the box consequence line shows on Runs in (box) and Network, not on Agent, nor on a host profile's Runs in", async () => {
  const line = "Saving gives the next session a new box. Running sessions keep theirs.";
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-fnote"), [line]);
  const host = field(t, "tier-host");
  host.checked = true;
  host.onchange();
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-fnote"), [], "choosing host: the next session is not a box");
  clickBtn(blockOf(t, "runs"), "Discard");
  clickBtn(blockOf(t, "agent"), "Edit");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-fnote"), []);
  clickBtn(blockOf(t, "agent"), "Cancel");
  clickBtn(blockOf(t, "network"), "Edit");
  assert.deepStrictEqual(texts(blockOf(t, "network"), "pfv-fnote"), [line]);

  const h = page();
  await h.box.pfvShow("runbook-host");
  clickBtn(blockOf(h, "runs"), "Edit");
  assert.deepStrictEqual(texts(blockOf(h, "runs"), "pfv-fnote"), []);
});

test("Network edit is one switch with its consequence; it saves egress_open", async () => {
  const t = page();
  // The list refetch after a save reports the posture of what was saved, as the daemon does.
  t.box.loadProfiles = async () => {
    t.box.profiles = t.box.profiles.map((p) => (p.name === "aws-ops" && t.box.docs["aws-ops"] ? Object.assign({}, p, { network: t.box.docs["aws-ops"].egress_open ? "open" : "allowlist" }) : p));
    return true;
  };
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "network"), "Edit");
  const net = blockOf(t, "network");
  assert.deepStrictEqual(texts(net, "pfv-flabel"), []);
  assert.match(net.textContent, /Allow all internet hosts \(no allowlist\)/);
  assert.match(net.textContent, /Private, tailnet and host addresses stay blocked either way\./);
  assert.strictEqual(byCls(net, "pfv-consq").length, 0, "no consequence until it is on");
  const sw = field(t, "egress_open");
  assert.strictEqual(sw.checked, false);
  sw.checked = true;
  sw.onchange();
  assert.deepStrictEqual(texts(blockOf(t, "network"), "pfv-consq"), ["The agent could then send this profile's credentials to any website. Every host it contacts is logged in Activity."]);
  await clickBtn(blockOf(t, "network"), "Save");
  assert.strictEqual(t.box.puts[0].egress_open, true);
  assert.match(texts(blockOf(t, "network"), "pfv-sub").join(" "), /All internet hosts/);
  // Turning it off removes the key.
  clickBtn(blockOf(t, "network"), "Edit");
  const off = field(t, "egress_open");
  assert.strictEqual(off.checked, true);
  off.checked = false;
  off.onchange();
  await clickBtn(blockOf(t, "network"), "Save");
  assert.ok(!("egress_open" in t.box.puts[1]));
});

test("changing the agent to another command drops the options it does not take, and hides their fields", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  const bin = field(t, "bin");
  bin.value = "bash";
  bin.onchange();
  assert.strictEqual(field(t, "options.model"), undefined);
  assert.strictEqual(field(t, "options.effort"), undefined);
  assert.strictEqual(field(t, "account"), undefined, "a login is Claude's");
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.strictEqual(t.box.puts[0].bin, "bash");
  assert.ok(!("options" in t.box.puts[0]), "the server refuses a Claude-only option on bash");
});

test("an agent command that is neither claude nor bash opens as Other command, with its text kept", async () => {
  const t = page();
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { bin: "/opt/agents/claude" });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  assert.strictEqual(field(t, "bin").value, "__other__");
  assert.strictEqual(field(t, "bin-text").value, "/opt/agents/claude");
  assert.ok(field(t, "options.model"), "its basename is claude: the Claude options still apply");
  assert.strictEqual(t.run("pfvState").pending, false);
  const txt = field(t, "bin-text");
  txt.value = "";
  txt.oninput();
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.strictEqual(t.box.puts.length, 0, "an empty command is not sent");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-ferr"), ["A command is required."]);
});

test("the login choice: default and the saved accounts; the default is stored as no account", async () => {
  const t = page();
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { account: "work" });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  const acct = field(t, "account");
  assert.deepStrictEqual(acct.children.map((o) => o.value), ["", "work"]);
  assert.strictEqual(acct.value, "work");
  acct.value = "";
  acct.onchange();
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.ok(!("account" in t.box.puts[0]));
});

test("a save that succeeds but returns warnings shows them in words", async () => {
  const t = page();
  t.box.putReply = { warnings: ["egress_allow and egress_open apply only to a profile with a credential binding; without one the box already reaches any public host"] };
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "network"), "Edit");
  const sw = field(t, "egress_open");
  sw.checked = true;
  sw.onchange();
  await clickBtn(blockOf(t, "network"), "Save");
  const w = texts(t.els["pfv-in"], "pfv-warn");
  assert.strictEqual(w.length, 1);
  assert.ok(!/egress_/.test(w[0]));
  assert.match(w[0], /^Network settings apply only to a profile that holds cloud access or a token/);
});

test("a save whose reload fails still says it saved, and the next save is refused as stale instead of overwriting", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.getFail = apiErr(500, { error: "boom" });
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), true);
  assert.strictEqual(t.box.puts.length, 1);
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-err"), ["Saved, but reloading it failed: boom"]);
  assert.strictEqual(t.run("pfvState").openBlock, "");
  assert.strictEqual(t.run("pfvState").pending, false);
});

test("a save for a profile you have left does not draw over the one on screen", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  let release;
  const real = t.box.api;
  t.box.api = async (p, o) => { if (o && o.method === "PUT") await new Promise((r) => { release = r; }); return real(p, o); };
  const saving = clickBtn(blockOf(t, "agent"), "Save");
  await settle();
  await t.box.pfvShow("runbook-host");
  release();
  await saving;
  assert.strictEqual(t.run("pfvState").name, "runbook-host");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-title-name"), ["runbook-host"]);
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-saved").length, 0);
});

test("a read-only profile has no Edit to open, and pfvOpenBlock refuses it", async () => {
  const t = page();
  await t.box.pfvShow("claude");
  assert.strictEqual(t.box.pfvOpenBlock("agent"), false);
  assert.strictEqual(t.run("pfvState").openBlock, "");
});

test("Show scrolls to the open block that has changes", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  let scrolled = null;
  blockOf(t, "agent").scrollIntoView = (o) => { scrolled = o; };
  clickBtn(byCls(t.els["pfv-in"], "pfv-unsaved")[0], "Show");
  assert.strictEqual(JSON.stringify(scrolled), '{"block":"nearest"}');
});

test("picking an effort keeps focus on the lit button after the form redraws", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  const xhigh = byText(field(t, "options.effort"), "button", "xhigh")[0];
  xhigh.focus();
  xhigh.click();
  assert.strictEqual(field(t, "options.effort").dataset.value, "xhigh");
  assert.strictEqual(Node.focused.textContent, "xhigh");
  assert.ok(blockOf(t, "agent").contains(Node.focused), "a button of the new form, not of the one that was replaced");
  assert.strictEqual(t.run("pfvState").pending, true);
});

// --- Task 7 review minors: a stale answer after a save, and a stuck Save ---------------------------------------

test("an answer to a pfvShow that was already out before the edit cannot bring back the profile as it was before the save", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const old = JSON.parse(JSON.stringify(DOCS["aws-ops"]));
  let release;
  const real = t.box.api;
  t.box.api = async (p, o) => {
    if (p === "/api/profiles/aws-ops" && !(o && o.method) && !release) { await new Promise((r) => { release = r; }); return old; }
    return real(p, o);
  };
  const slow = t.box.pfvShow("aws-ops"); // the tab was selected again: a GET is out
  await settle();
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), true);
  const saved = t.run("pfvState").doc;
  assert.notStrictEqual(saved.rev, "r1", "the page holds the revision the save made");
  release();
  await slow;
  assert.strictEqual(t.run("pfvState").doc, saved, "the late answer is dropped");
  assert.strictEqual(t.run("pfvState").doc.options.model, "claude-sonnet-5");
});

test("a profile opened while a save is in flight still lands when the save returns first", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  let releasePut, releaseGet;
  const real = t.box.api;
  t.box.api = async (p, o) => {
    if (o && o.method === "PUT") await new Promise((r) => { releasePut = r; });
    if (p === "/api/profiles/runbook-host" && !(o && o.method)) await new Promise((r) => { releaseGet = r; });
    return real(p, o);
  };
  const saving = clickBtn(blockOf(t, "agent"), "Save");
  await settle();
  const showing = t.box.pfvShow("runbook-host"); // the user clicked another profile; its GET is out
  await settle();
  releasePut(); // the save finishes before that GET lands
  await saving;
  releaseGet();
  await showing;
  assert.strictEqual(t.run("pfvState").name, "runbook-host");
  assert.strictEqual(t.run("pfvState").doc && t.run("pfvState").doc.name, "runbook-host", "the pane is not left blank");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-title-name"), ["runbook-host"]);
});

test("an answer to an older pfvShow cannot replace what Reload fetched", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const old = JSON.parse(JSON.stringify(DOCS["aws-ops"]));
  let release;
  const real = t.box.api;
  t.box.api = async (p, o) => {
    if (p === "/api/profiles/aws-ops" && !(o && o.method) && !release) { await new Promise((r) => { release = r; }); return old; }
    return real(p, o);
  };
  const slow = t.box.pfvShow("aws-ops");
  await settle();
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(409, { error: "changed", code: "profile_changed" });
  await clickBtn(blockOf(t, "agent"), "Save");
  t.box.putFail = null;
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { rev: "r9" });
  await clickBtn(blockOf(t, "agent"), "Reload");
  assert.strictEqual(t.run("pfvState").doc.rev, "r9");
  release();
  await slow;
  assert.strictEqual(t.run("pfvState").doc.rev, "r9", "the late answer is dropped");
});

test("a throw while a save runs its course cannot leave Save and Edit refused for good", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.loadProfiles = async () => { throw new Error("boom"); };
  await assert.rejects(() => clickBtn(blockOf(t, "agent"), "Save"), /boom/);
  assert.strictEqual(t.run("pfvState").saving, false);
  t.box.loadProfiles = async () => true;
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), true, "Save works again");
  assert.strictEqual(t.box.puts.length, 2);
});

// --- the Workspace block (spec 5.5; pfv-workspace.js) -----------------------------------------------------------------

const CLONE_DOC = { name: "gitops-clone", bin: "claude", runtime: "box", default_cwd: "/home/you/box-clones/k8s-gitops", clone_of: "/home/you/work/k8s-gitops", clone_share: ["CLAUDE.md", "docs/a.md"], clone_no_auto_return: true, rev: "r5" };
const cloneView = () => view("gitops-clone", { default_cwd: "/home/you/box-clones/k8s-gitops", clone_of: "/home/you/work/k8s-gitops", auto_return: false });
const clonePage = (over) => {
  const t = page(Object.assign({ profiles: [cloneView(), PROFILES[1], PROFILES[3]] }, over));
  t.box.docs["gitops-clone"] = CLONE_DOC;
  return t;
};
const lastPicker = (t) => t.box.pickers[t.box.pickers.length - 1];
async function openWs(t, name) {
  await t.box.pfvShow(name || "aws-ops");
  clickBtn(blockOf(t, "workspace"), "Edit");
  return lastPicker(t);
}
// What a click on a radio does: focus moves to it, it becomes checked, and its change handler runs.
function pickMode(t, which) {
  const r = field(t, "mode-" + which);
  r.focus();
  r.checked = true;
  r.onchange();
}
const typeInto = (n, text) => { n.value = text; n.oninput(); };
const ferrs = (t) => texts(blockOf(t, "workspace"), "pfv-ferr");

test("Workspace edit: the folder picker and the two ways of working on it; the copy fields wait for the private copy", async () => {
  const t = page();
  const pk = await openWs(t);
  const ws = blockOf(t, "workspace");
  assert.ok(ws.className.includes("pfv-editing"));
  assert.strictEqual(t.run("pfvState").openBlock, "workspace");
  assert.deepStrictEqual(byCls(ws, "pfv-flabel").map((n) => n.textContent), ["Folder", "How it works on the folder"]);
  const input = field(t, "folder");
  assert.strictEqual(input.value, "/home/you/work/payments-api");
  assert.strictEqual(t.box.pickers.length, 1, "one picker for the block");
  assert.strictEqual(pk.opts.input, input);
  assert.strictEqual(pk.opts.keepEsc, true, "Esc closes the list, not the page");
  assert.strictEqual(pk.opts.reanchor, true, "the page scrolls under the fixed list");
  assert.strictEqual(pk.opts.list.className, "pfv-wslist");
  assert.strictEqual(pk.value(), "/home/you/work/payments-api");
  assert.deepStrictEqual(texts(ws, "pfv-hint"), ["A box cannot use your whole home or system folders."]);
  assert.deepStrictEqual(texts(ws, "pfv-card-t"), ["Work in this folder", "Work on a private copy"]);
  assert.strictEqual(field(t, "mode-folder").checked, true);
  assert.strictEqual(field(t, "mode-copy").checked, false);
  assert.strictEqual(field(t, "copy-dir"), undefined, "no copy fields yet");
  assert.strictEqual(field(t, "share"), undefined);
  assert.strictEqual(byText(ws, "button", "Edit").length, 0);
  assert.deepStrictEqual(texts(ws, "pfv-fnote"), [], "the 'new box' line is for the private copy, as the mockup draws it");
  assert.strictEqual(Node.focused, input, "focus goes to the folder");
  assert.strictEqual(pk.opened, 0, "but opening the block does not open the dropdown");
  input.focus();
  assert.strictEqual(pk.opened, 1, "tabbing in later does");
});

test("Workspace: picking a folder is a change; Save writes default_cwd and keeps the rest of the profile", async () => {
  const t = page();
  const pk = await openWs(t);
  const head = byCls(t.els["pfv-in"], "pfv-unsaved")[0];
  assert.ok(head.hidden);
  pk.pick("/home/you/work/billing");
  assert.strictEqual(head.hidden, false);
  assert.deepStrictEqual(texts(blockOf(t, "workspace"), "pfv-st-dirty"), ["Unsaved changes"]);
  assert.strictEqual(t.box.pickers.length, 1, "a pick does not draw the form again: the picker and its list stay");
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), true);
  assert.strictEqual(t.box.puts.length, 1);
  const body = t.box.puts[0];
  assert.strictEqual(body.default_cwd, "/home/you/work/billing");
  assert.strictEqual(body.clone_of, undefined);
  assert.strictEqual(body.runtime, "box");
  assert.deepStrictEqual(body.options, { model: "claude-opus-5-5", effort: "high" }, "the other blocks' values are kept");
  assert.strictEqual(body.rev, "r1");
  assert.strictEqual(t.run("pfvState").openBlock, "");
  assert.strictEqual(pk.destroyed, true, "the picker goes with the form");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-saved"), ["Saved"]);
});

test("Workspace: Work on a private copy shows the copy fields, the copy kept under ~/box-clones; Save sends clone_of and where the copy lives", async () => {
  const t = page();
  const first = await openWs(t);
  pickMode(t, "copy");
  const ws = blockOf(t, "workspace");
  assert.deepStrictEqual(byCls(ws, "pfv-flabel").map((n) => n.textContent), ["Folder to copy", "How it works on the folder", "Where the copy is kept", "Files to share from your repo, read-only"]);
  assert.strictEqual(field(t, "copy-dir").value, "/home/you/box-clones/payments-api");
  assert.strictEqual(field(t, "share").value, "");
  assert.strictEqual(field(t, "auto-return").checked, true);
  assert.deepStrictEqual(texts(ws, "pfv-check-t"), ["Bring work home automatically"]);
  assert.deepStrictEqual(texts(ws, "pfv-hint"), [
    "A box cannot use your whole home or system folders.", "CuliBox creates it on the first session.", "Relative to the folder, one per line, at most 8.",
  ]);
  assert.deepStrictEqual(texts(ws, "pfv-fnote"), ["Saving gives the next session a new box. Running sessions keep theirs."]);
  assert.strictEqual(first.destroyed, true, "the form was drawn again: the old picker is released");
  assert.strictEqual(t.box.pickers.length, 2);
  assert.strictEqual(lastPicker(t).value(), "/home/you/work/payments-api", "the folder carries over");
  assert.strictEqual(Node.focused, field(t, "mode-copy"), "focus stays on the radio");
  assert.strictEqual(lastPicker(t).opened, 0, "and the dropdown stays closed");
  typeInto(field(t, "share"), "CLAUDE.md\ndocs/guide.md");
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), true);
  const body = t.box.puts[0];
  assert.strictEqual(body.clone_of, "/home/you/work/payments-api");
  assert.strictEqual(body.default_cwd, "/home/you/box-clones/payments-api");
  assert.deepStrictEqual(body.clone_share, ["CLAUDE.md", "docs/guide.md"]);
  assert.strictEqual(body.clone_no_auto_return, undefined, "unset means on");
});

test("Workspace: the copy's folder follows the picked folder until the user writes their own, and is never one another profile keeps its copy in", async () => {
  const t = page({ profiles: PROFILES.concat([view("other", { default_cwd: "/home/you/box-clones/payments-api" })]) });
  await openWs(t);
  pickMode(t, "copy");
  assert.strictEqual(field(t, "copy-dir").value, "/home/you/box-clones/payments-api-2", "the other profile's copy is taken");
  lastPicker(t).pick("/home/you/work/billing");
  assert.strictEqual(field(t, "copy-dir").value, "/home/you/box-clones/billing");
  assert.strictEqual(t.box.pickers.length, 2, "no redraw for it");
  typeInto(field(t, "copy-dir"), "/home/you/clones/mine");
  lastPicker(t).pick("/home/you/work/other");
  assert.strictEqual(field(t, "copy-dir").value, "/home/you/clones/mine", "what the user wrote stays");
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), true);
  assert.strictEqual(t.box.puts[0].default_cwd, "/home/you/clones/mine");
  assert.strictEqual(t.box.puts[0].clone_of, "/home/you/work/other");
});

test("Workspace: a private copy and back is no change, and the copy fields return as they were left", async () => {
  const t = page();
  await openWs(t);
  const head = byCls(t.els["pfv-in"], "pfv-unsaved")[0];
  pickMode(t, "copy");
  assert.strictEqual(head.hidden, false);
  typeInto(field(t, "share"), "CLAUDE.md");
  pickMode(t, "folder");
  assert.ok(head.hidden, "back to what was stored is no change");
  assert.strictEqual(field(t, "copy-dir"), undefined);
  assert.strictEqual(byText(blockOf(t, "workspace"), "button", "Cancel").length, 1);
  pickMode(t, "copy");
  assert.strictEqual(field(t, "share").value, "CLAUDE.md");
  assert.strictEqual(field(t, "copy-dir").value, "/home/you/box-clones/payments-api");
  assert.strictEqual(head.hidden, false);
});

test("Workspace: a private copy with no folder or no place for the copy, or a relative one, is refused before any PUT, and the words go when the field changes", async () => {
  const t = page();
  await openWs(t);
  pickMode(t, "copy");
  lastPicker(t).type("");
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), false);
  assert.strictEqual(t.box.puts.length, 0);
  assert.deepStrictEqual(ferrs(t), ["Private copy: pick the folder to copy."]);
  assert.ok(field(t, "folder").className.includes("pfv-bad"));
  assert.deepStrictEqual(texts(blockOf(t, "workspace"), "pfv-st-invalid"), ["One field needs a fix before this can save."]);
  const redraws = t.box.pickers.length;
  lastPicker(t).pick("/home/you/work/payments-api");
  assert.deepStrictEqual(ferrs(t), [], "picking clears it in place");
  assert.strictEqual(t.box.pickers.length, redraws, "without drawing the form again");
  typeInto(field(t, "copy-dir"), "");
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), false);
  assert.deepStrictEqual(ferrs(t), ["Private copy: choose where the copy lives."]);
  typeInto(field(t, "copy-dir"), "~/box-clones/x");
  assert.deepStrictEqual(ferrs(t), []);
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), false);
  assert.deepStrictEqual(ferrs(t), ["Use a full path that starts with /"]);
  assert.strictEqual(t.box.puts.length, 0);
});

test("Workspace: text left in the folder field that was never picked or resolved is refused, not saved as the old folder", async () => {
  const t = page();
  const pk = await openWs(t);
  pk.type("culi"); // a search query, not a folder
  assert.strictEqual(pk.value(), "/home/you/work/payments-api");
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), false);
  assert.strictEqual(t.box.puts.length, 0);
  assert.deepStrictEqual(ferrs(t), ["Use a full path that starts with /"]);
  assert.strictEqual(field(t, "folder").value, "culi", "what the user typed is still there to fix");
  lastPicker(t).pick("/home/you/work/billing");
  assert.deepStrictEqual(ferrs(t), []);
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), true);
  assert.strictEqual(t.box.puts[0].default_cwd, "/home/you/work/billing");
});

test("Workspace: a refusal that names the folder, the copy's folder or a shared file goes under that control", async () => {
  const t = page();
  await openWs(t);
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": default working dir cannot be contained: it is your home', field: "default_cwd" });
  lastPicker(t).pick("/home/you");
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), false);
  assert.deepStrictEqual(ferrs(t), ["default working dir cannot be contained: it is your home"]);
  assert.ok(field(t, "folder").className.includes("pfv-bad"));
  assert.strictEqual(field(t, "folder").value, "/home/you", "the value stays");
  // Copy mode: three controls, three fields.
  pickMode(t, "copy");
  for (const [fieldPath, control] of [["clone_of", "folder"], ["default_cwd", "copy-dir"], ["clone_share[1]", "share"]]) {
    t.box.putFail = apiErr(400, { error: "profile \"aws-ops\": " + fieldPath + " is wrong", field: fieldPath });
    await clickBtn(blockOf(t, "workspace"), "Save");
    const bad = byCls(blockOf(t, "workspace"), "pfv-bad");
    assert.deepStrictEqual(bad.map((n) => n.dataset.pfvField), [control], fieldPath);
    assert.deepStrictEqual(ferrs(t), [fieldPath + " is wrong"], fieldPath);
  }
});

test("Workspace on a host profile: one folder field and one sentence saying why there is no private copy", async () => {
  const t = page();
  const pk = await openWs(t, "runbook-host");
  const ws = blockOf(t, "workspace");
  assert.deepStrictEqual(byCls(ws, "pfv-flabel").map((n) => n.textContent), ["Folder"]);
  assert.deepStrictEqual(texts(ws, "pfv-hint"), ["Where sessions start. A private copy needs a box."]);
  assert.strictEqual(byCls(ws, "pfv-card").length, 0);
  assert.strictEqual(field(t, "folder").value, "/home/you/runbooks");
  assert.deepStrictEqual(texts(ws, "pfv-fnote"), [], "a host has no box to give the next session");
  pk.pick("/home/you/notes");
  await clickBtn(blockOf(t, "workspace"), "Save");
  assert.strictEqual(t.box.puts[0].default_cwd, "/home/you/notes");
  assert.strictEqual(t.box.puts[0].runtime, "host");
});

test("Workspace where private copies are not available: the choice is hidden with the reason; a profile that already is a copy keeps both and the warning", async () => {
  const t = page({ cloneUnavailable: "Docker is not installed" });
  await openWs(t);
  let ws = blockOf(t, "workspace");
  assert.strictEqual(byCls(ws, "pfv-card").length, 0);
  assert.deepStrictEqual(texts(ws, "pfv-hint"), ["A box cannot use your whole home or system folders.", "Private copies are not available on this instance: Docker is not installed."]);
  const c = clonePage({ cloneUnavailable: "Docker is not installed" });
  await openWs(c, "gitops-clone");
  ws = blockOf(c, "workspace");
  assert.deepStrictEqual(texts(ws, "pfv-card-t"), ["Work in this folder", "Work on a private copy"]);
  assert.ok(texts(ws, "pfv-hint").includes("Private copies are not available on this instance (Docker is not installed). Sessions of this profile will not start."));
  assert.strictEqual(field(c, "mode-copy").checked, true);
});

test("Workspace on a private copy: it opens as one, and Work in this folder says the copy stays on disk and saves just the folder", async () => {
  const t = clonePage();
  await openWs(t, "gitops-clone");
  const ws = blockOf(t, "workspace");
  assert.strictEqual(field(t, "mode-copy").checked, true);
  assert.strictEqual(field(t, "folder").value, "/home/you/work/k8s-gitops");
  assert.strictEqual(field(t, "copy-dir").value, "/home/you/box-clones/k8s-gitops");
  assert.strictEqual(field(t, "share").value, "CLAUDE.md\ndocs/a.md");
  assert.strictEqual(field(t, "auto-return").checked, false, "clone_no_auto_return is the stored opt-out");
  assert.deepStrictEqual(byCls(ws, "pfv-flabel").map((n) => n.textContent)[0], "Folder to copy");
  assert.ok(texts(ws, "pfv-hint").includes("CuliBox created it on the first session."));
  assert.deepStrictEqual(texts(ws, "pfv-fnote"), ["Saving gives the next session a new box. Running sessions keep theirs."]);
  assert.ok(byCls(t.els["pfv-in"], "pfv-unsaved")[0].hidden, "opened and not touched is no change");
  pickMode(t, "folder");
  assert.deepStrictEqual(texts(blockOf(t, "workspace"), "pfv-hint").slice(-1), ["The private copy at ~/box-clones/k8s-gitops stays on disk with its work; this switch never deletes it."]);
  assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), true);
  const body = t.box.puts[0];
  assert.strictEqual(body.default_cwd, "/home/you/work/k8s-gitops");
  for (const k of ["clone_of", "clone_share", "clone_no_auto_return"]) assert.strictEqual(body[k], undefined, k);
});

test("Workspace on a private copy: share and the auto-return opt-out that were not touched are saved as stored", async () => {
  const t = clonePage();
  await openWs(t, "gitops-clone");
  lastPicker(t).pick("/home/you/work/other");
  await clickBtn(blockOf(t, "workspace"), "Save");
  const body = t.box.puts[0];
  assert.strictEqual(body.clone_of, "/home/you/work/other");
  assert.strictEqual(body.default_cwd, "/home/you/box-clones/k8s-gitops", "the stored copy folder is not replaced by a suggestion");
  assert.deepStrictEqual(body.clone_share, ["CLAUDE.md", "docs/a.md"]);
  assert.strictEqual(body.clone_no_auto_return, true);
});

test("Workspace: the picker is built for each draw of the form and released when the form goes", async () => {
  const t = page();
  const a = await openWs(t);
  pickMode(t, "copy");
  const b = lastPicker(t);
  assert.deepStrictEqual([a.destroyed, b.destroyed], [true, false]);
  clickBtn(blockOf(t, "workspace"), "Discard");
  assert.strictEqual(b.destroyed, true, "Discard");
  const c = await openWs(t);
  assert.strictEqual(c.destroyed, false);
  await t.box.pfvShow("runbook-host");
  assert.strictEqual(c.destroyed, true, "switching profile");
  await t.box.pfvShow("aws-ops");
  const d = await openWs(t);
  t.box.putFail = apiErr(400, { error: "nope", field: "default_cwd" });
  d.pick("/home/you/x");
  await clickBtn(blockOf(t, "workspace"), "Save");
  assert.strictEqual(d.destroyed, true, "a refused save draws the form again, with a new picker");
  assert.strictEqual(lastPicker(t).destroyed, false);
  assert.strictEqual(t.box.pickers.filter((p) => !p.destroyed).length, 1, "exactly one is alive");
});

test("Workspace: a refused save in another block opens Workspace as a repair form with the error under its folder", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": default working dir cannot be contained: it is your home', field: "default_cwd" });
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.ok(blockOf(t, "workspace").className.includes("pfv-editing"));
  assert.deepStrictEqual(ferrs(t), ["default working dir cannot be contained: it is your home"]);
  assert.strictEqual(field(t, "folder").value, "/home/you/work/payments-api");
  assert.ok(byCls(blockOf(t, "agent"), "pfv-top-err")[0].textContent.startsWith("Not saved: a folder in Workspace needs a fix first."));
});

test("Runs in: Open Workspace drops the host choice and opens the Workspace block, with no PUT", async () => {
  const t = clonePage();
  await t.box.pfvShow("gitops-clone");
  clickBtn(blockOf(t, "runs"), "Edit");
  const host = field(t, "tier-host");
  host.checked = true;
  host.onchange();
  assert.strictEqual(byText(blockOf(t, "runs"), "button", "Open Workspace").length, 1);
  byText(blockOf(t, "runs"), "button", "Open Workspace")[0].click();
  assert.strictEqual(byCls(blockOf(t, "runs"), "pfv-editing").length, 0, "the Runs in change is gone");
  assert.strictEqual(byCls(blockOf(t, "runs"), "pfv-refuse").length, 0);
  assert.ok(blockOf(t, "workspace").className.includes("pfv-editing"));
  assert.strictEqual(t.run("pfvState").openBlock, "workspace");
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), ["workspace"]);
  assert.ok(byCls(t.els["pfv-in"], "pfv-unsaved")[0].hidden, "nothing is pending");
  assert.strictEqual(t.box.puts.length, 0);
  assert.strictEqual(field(t, "mode-copy").checked, true, "the profile is still a copy until the user picks the folder");
});

test("Runs in: Open Workspace does not drop another block's unsaved change", async () => {
  const t = clonePage();
  await t.box.pfvShow("gitops-clone");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  // A repair block can sit beside the dirty one: Runs in, with host chosen.
  const rec = t.box.pfvOpenRepair("runs");
  rec.values.tier = "host";
  t.box.pfvRender();
  assert.strictEqual(t.box.pfvOpenWorkspace(), false);
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits).sort(), ["agent", "runs"], "nothing was dropped");
  assert.strictEqual(t.run("pfvState").edits.runs.values.tier, "host");
  assert.strictEqual(t.run("pfvState").edits.agent.nudge, true);
});

test("an editor in its own script registers through pfvRegisterEditor, once", () => {
  const t = page();
  assert.ok(t.run("PFV_EDITORS").workspace, "Workspace registered itself");
  assert.throws(() => t.box.pfvRegisterEditor("runs", {}), /registered twice: runs/);
  assert.throws(() => t.box.pfvRegisterEditor("workspace", {}), /registered twice: workspace/);
});

test("every top-level name in pfv-workspace.js is unique across the page scripts", () => {
  const names = (src) => new Set([...src.matchAll(/^(?:async\s+)?(?:function\s+|const\s+|let\s+|var\s+)([A-Za-z_$][\w$]*)/gm)].map((m) => m[1]));
  const mine = names(fs.readFileSync(path.join(STATIC, "pfv-workspace.js"), "utf8"));
  assert.ok(mine.size > 5);
  assert.ok([...mine].every((n) => /^(pfw|PFW_)/.test(n)), "every name is pfw/PFW_ prefixed: " + [...mine].join(", "));
  const clashes = [];
  for (const f of fs.readdirSync(STATIC).filter((x) => x.endsWith(".js") && x !== "pfv-workspace.js")) {
    for (const n of names(fs.readFileSync(path.join(STATIC, f), "utf8"))) if (mine.has(n)) clashes.push(n + " (" + f + ")");
  }
  assert.deepStrictEqual(clashes, []);
});

// --- Cloud and tokens, Also reachable (spec 5.3, 5.5; pfv-cloud.js) --------------------------------------------------------

const GH_READ = { actions: "read", checks: "read", contents: "read", pull_requests: "read", issues: "read", metadata: "read" };
const CLOUD_DOC = {
  name: "aws-ops", bin: "claude", runtime: "box", default_cwd: "/home/you/work/payments-api", options: { model: "claude-opus-5-5", effort: "high" },
  aws_connections: ["prod-read", "staging-read"], tokens: ["grafana-prod", "sentry-staging"],
  github: { owners: [{ owner: "culiops", app: "acme-app", repos: ["payments-api"] }], egress_allow: ["extra.example.com"] },
  context_paths: ["/home/you/other-repo/docs"], egress_allow: ["api.example.com", "*.example.org"], rev: "r1",
};
const cloudView = (over) => view("aws-ops", Object.assign({
  default_cwd: "/home/you/work/payments-api", aws_connections: ["prod-read", "staging-read"], tokens: ["grafana-prod", "sentry-staging"],
  github_owners: ["culiops"], github_repos: [{ owner: "culiops", repos: ["payments-api"] }], github_permissions: GH_READ,
  context_paths: ["/home/you/other-repo/docs"], egress_allow: ["api.example.com", "*.example.org"], network: "allowlist",
}, over));
const GH_PAT_ENTRY = { name: "gh-pat", provider: "github", hosts: [{ host: "api.github.com", port: 443 }] };
const cloudPage = (over, doc) => {
  const t = page(Object.assign({
    profiles: [PROFILES[0], cloudView(), PROFILES[2], PROFILES[3]],
    stEntries: [
      { name: "grafana-prod", prod: true, env: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"], hosts: [{ host: "grafana.acme.example.com", port: 443 }] },
      { name: "sentry-staging", env: ["SENTRY_ACCESS_TOKEN"], hosts: [{ host: "sentry.io", port: 443 }] },
      GH_PAT_ENTRY,
    ],
  }, over));
  t.box.docs["aws-ops"] = Object.assign({}, CLOUD_DOC, doc);
  return t;
};
const rowsOf = (t, id) => byCls(blockOf(t, id), "pfv-row");
const rowFor = (t, id, kind, name) => rowsOf(t, id).find((r) => texts(r, "pfv-k")[0] === kind && (texts(r, "pfv-nm")[0] || "").startsWith(name));
const unsavedNote = (t) => byCls(t.els["pfv-in"], "pfv-unsaved")[0];
const optBtn = (t, id, title) => byCls(blockOf(t, id), "pfv-opt").find((b) => texts(b, "pfv-opt-t")[0] === title);
const setLevel = (t, key, level) => field(t, key).children.find((b) => b.dataset.segvalue === level).click();
const tick = (t, key, on) => { const cb = field(t, key); cb.checked = on; cb.onchange(); };
const saveBlock = (t, id) => clickBtn(blockOf(t, id), "Save");

test("Cloud and tokens edit: every grant is a row with Remove, the read view's words, and one note for the tokens", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  const cloud = blockOf(t, "cloud");
  assert.ok(cloud.className.includes("pfv-editing"));
  assert.deepStrictEqual(rowsOf(t, "cloud").map((r) => texts(r, "pfv-k")[0]), ["AWS", "AWS", "GitHub", "Token", "Token"]);
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "AWS", "prod-read"), "pfv-scope"), ["Read only. Write only after you grant a window."]);
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "AWS", "prod-read"), "pfv-tag"), ["PROD"]);
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "Token", "grafana-prod"), "pfv-tag"), ["production"]);
  for (const r of rowsOf(t, "cloud")) assert.strictEqual(byText(r, "button", "Remove").length, 1, "one Remove per row");
  assert.deepStrictEqual(texts(cloud, "pfv-tnote"), ["Long-lived; the agent can read it while it runs"], "the note comes once");
  assert.strictEqual(byText(cloud, "button", "+ Add access").length, 1);
  assert.ok(unsavedNote(t).hidden, "opening a form is no change");
  assert.strictEqual(t.box.puts.length, 0);
});

test("removing a row is pending until save and undo restores it", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  const row = () => rowFor(t, "cloud", "AWS", "staging-read");
  byText(row(), "button", "Remove")[0].click();
  assert.ok(row().className.includes("pfv-removed"), "the row stays, struck through");
  assert.strictEqual(byText(row(), "button", "Undo").length, 1);
  assert.strictEqual(t.box.puts.length, 0, "nothing is sent until Save");
  assert.strictEqual(unsavedNote(t).hidden, false);
  byText(row(), "button", "Undo")[0].click();
  assert.ok(!row().className.includes("pfv-removed"));
  assert.ok(unsavedNote(t).hidden, "undone is no change");
  byText(row(), "button", "Remove")[0].click();
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  const body = t.box.puts[0];
  assert.deepStrictEqual(body.aws_connections, ["prod-read"]);
  assert.deepStrictEqual(body.tokens, ["grafana-prod", "sentry-staging"]);
  assert.deepStrictEqual(body.github, CLOUD_DOC.github, "the binding goes back as it was, its extra hosts too");
  assert.deepStrictEqual(body.context_paths, ["/home/you/other-repo/docs"], "the other block's keys are untouched");
  assert.deepStrictEqual([body.rev, body.schema], ["r1", "sch-1"]);
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0);
});

test("removing every token takes the token note with it; removing the GitHub row takes the binding", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  for (const name of ["grafana-prod", "sentry-staging"]) byText(rowFor(t, "cloud", "Token", name), "button", "Remove")[0].click();
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-tnote"), []);
  byText(rowFor(t, "cloud", "GitHub", "culiops"), "button", "Remove")[0].click();
  await saveBlock(t, "cloud");
  const body = t.box.puts[0];
  assert.ok(!("tokens" in body) && !("github" in body), "no tokens, no binding");
  assert.deepStrictEqual(body.aws_connections, ["prod-read", "staging-read"]);
});

test("a missing credential row can be removed and saves without it", async () => {
  const t = cloudPage({ awscEntries: [{ name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" }] });
  await t.box.pfvShow("aws-ops");
  const read = rowFor(t, "cloud", "AWS", "staging-read");
  assert.match(texts(read, "pfv-scope")[0], /^Not found in Credentials · Remove$/);
  byText(read, "button", "Remove")[0].click();
  // The link opens the block with the row removed, pending: never a silent save.
  assert.ok(blockOf(t, "cloud").className.includes("pfv-editing"));
  assert.strictEqual(t.box.puts.length, 0);
  const row = rowFor(t, "cloud", "AWS", "staging-read");
  assert.ok(row.className.includes("pfv-removed"));
  assert.deepStrictEqual(texts(row, "pfv-miss"), ["Not found in Credentials"], "still the error colour's text");
  assert.strictEqual(byText(row, "button", "Remove").length, 0, "the open block has its Remove column, not the inline link");
  assert.strictEqual(byText(row, "button", "Undo").length, 1);
  assert.strictEqual(unsavedNote(t).hidden, false);
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  assert.deepStrictEqual(t.box.puts[0].aws_connections, ["prod-read"]);
});

test("undoing the removal a read view link started leaves no change", async () => {
  const t = cloudPage({ awscEntries: [{ name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" }] });
  await t.box.pfvShow("aws-ops");
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Undo")[0].click();
  assert.ok(unsavedNote(t).hidden, "undo is what it says");
});

test("read view + Add access opens cloud in edit mode with the picker open", async () => {
  const t = cloudPage();
  const asked = [];
  // The grouped picker is a later task's: the page only asks for it, by block.
  t.box.pfvOpenPicker = (id) => { asked.push(id); };
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.ok(blockOf(t, "cloud").className.includes("pfv-editing"));
  assert.strictEqual(t.run("pfvState").openBlock, "cloud");
  assert.deepStrictEqual(asked, ["cloud"]);
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  assert.ok(blockOf(t, "reachable").className.includes("pfv-editing"));
  assert.ok(!blockOf(t, "cloud").className.includes("pfv-editing"), "a form without changes yields to the next");
  assert.deepStrictEqual(asked, ["cloud", "reachable"]);
});

test("+ Add is refused in place while another block holds changes, and asks for no picker", async () => {
  const t = cloudPage();
  const asked = [];
  t.box.pfvOpenPicker = (id) => { asked.push(id); };
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.deepStrictEqual(asked, []);
  assert.ok(!blockOf(t, "cloud").className.includes("pfv-editing"));
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-nudge"), ["Save or discard these changes first"]);
});

test("the stand-in picker offers what is added as an input row, and picking one puts a pending row in", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  assert.deepStrictEqual(byCls(blockOf(t, "reachable"), "pfv-opt").map((b) => texts(b, "pfv-opt-t")[0]), ["Folder", "Domain"]);
  assert.strictEqual(t.box.puts.length, 0);
  optBtn(t, "reachable", "Domain").click();
  assert.strictEqual(byCls(blockOf(t, "reachable"), "pfv-opt").length, 0, "picking closes it");
  assert.strictEqual(Node.focused, field(t, "domain:2"), "focus goes to the new row's input");
  assert.strictEqual(unsavedNote(t).hidden, true, "a row that is still empty is no change");
  field(t, "domain:2").value = "api.example.net"; field(t, "domain:2").oninput();
  assert.strictEqual(unsavedNote(t).hidden, false, "typing in it is");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(t.box.puts.length, 0);
});

test("the stand-in picker for Cloud and tokens adds a GitHub owner, or says why it cannot", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.deepStrictEqual(byCls(blockOf(t, "cloud"), "pfv-opt").map((b) => texts(b, "pfv-opt-t")[0]), ["Another org or user"]);
  const none = cloudPage({ ghApps: [], stEntries: [] });
  await none.box.pfvShow("aws-ops");
  clickBtn(blockOf(none, "cloud"), "+ Add access");
  assert.strictEqual(byCls(blockOf(none, "cloud"), "pfv-opt").length, 0, "an option nobody can use is not drawn");
  assert.deepStrictEqual(texts(blockOf(none, "cloud"), "pfv-pick-note"), ["No GitHub App or token is defined yet."]);
});

test("a relative folder shows the absolute-path error and blocks save", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  optBtn(t, "reachable", "Folder").click();
  const input = () => field(t, "folder:1");
  assert.deepStrictEqual(texts(input().parent, "pfv-hint"), ["Read only. List a folder rather than a single file."]);
  typeInto(input(), "home/you/runbooks");
  assert.strictEqual(await saveBlock(t, "reachable"), false);
  assert.strictEqual(t.box.puts.length, 0, "nothing was sent");
  assert.deepStrictEqual(texts(blockOf(t, "reachable"), "pfv-ferr"), ["Use a full path that starts with /"]);
  assert.ok(input().className.includes("pfv-bad"));
  assert.strictEqual(input().value, "home/you/runbooks", "what was typed stays");
  assert.deepStrictEqual(texts(blockOf(t, "reachable"), "pfv-st-invalid"), ["One field needs a fix before this can save."]);
  typeInto(input(), "/home/you/runbooks");
  assert.deepStrictEqual(texts(blockOf(t, "reachable"), "pfv-ferr"), [], "the words go as soon as the field changes");
  assert.strictEqual(await saveBlock(t, "reachable"), true);
  assert.deepStrictEqual(t.box.puts[0].context_paths, ["/home/you/other-repo/docs", "/home/you/runbooks"]);
});

test("a row that was added and not filled in is not sent, and removing it takes it outright", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  const folders = () => rowsOf(t, "reachable").filter((r) => texts(r, "pfv-k")[0] === "Folder");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  optBtn(t, "reachable", "Folder").click();
  assert.strictEqual(folders().length, 2);
  byText(folders()[1], "button", "Remove")[0].click();
  assert.strictEqual(folders().length, 1, "a row never saved has nothing to undo");
  assert.ok(unsavedNote(t).hidden, "and nothing is pending");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  optBtn(t, "reachable", "Folder").click();
  await saveBlock(t, "reachable");
  assert.deepStrictEqual(t.box.puts[0].context_paths, ["/home/you/other-repo/docs"], "an empty folder row is dropped");
});

test("a domain is added as a row with the HTTPS-only hint and saves with the others", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  optBtn(t, "reachable", "Domain").click();
  const input = field(t, "domain:2");
  assert.deepStrictEqual(texts(input.parent, "pfv-hint"), ["HTTPS only. IP addresses are not accepted."]);
  typeInto(input, "*.example.net");
  assert.strictEqual(await saveBlock(t, "reachable"), true);
  assert.deepStrictEqual(t.box.puts[0].egress_allow, ["api.example.com", "*.example.org", "*.example.net"]);
  assert.deepStrictEqual(t.box.puts[0].context_paths, ["/home/you/other-repo/docs"]);
  assert.deepStrictEqual(t.box.puts[0].github, CLOUD_DOC.github, "the cloud keys are untouched");
});

test("a refused domain says so in words under its row; a folder that is gone is under its own row", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  optBtn(t, "reachable", "Domain").click();
  typeInto(field(t, "domain:2"), "10.0.0.1");
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": egress_allow[2] "10.0.0.1": must be a normalised lowercase hostname (no trailing dot, no IP literal) or *.host', field: "egress_allow[2]" });
  await saveBlock(t, "reachable");
  const ferr = texts(blockOf(t, "reachable"), "pfv-ferr");
  assert.strictEqual(ferr.length, 1);
  assert.match(ferr[0], /^Not a valid domain/);
  assert.ok(!/egress_allow|normalised/.test(ferr[0]), "no config keys");
  assert.ok(field(t, "domain:2").className.includes("pfv-bad"));
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": context_paths[0] "/home/you/other-repo/docs": does not exist', field: "context_paths[0]" });
  typeInto(field(t, "domain:2"), "ok.example.com");
  await saveBlock(t, "reachable");
  assert.deepStrictEqual(texts(rowFor(t, "reachable", "Folder", "/home/you/other-repo/docs"), "pfv-ferr"), ["This folder no longer exists. Remove it or pick another."]);
  byText(rowFor(t, "reachable", "Folder", "/home/you/other-repo/docs"), "button", "Remove")[0].click();
  assert.deepStrictEqual(texts(blockOf(t, "reachable"), "pfv-ferr"), [], "removing the folder takes the refusal with it");
  t.box.putFail = null;
  assert.strictEqual(await saveBlock(t, "reachable"), true);
  const sent = t.box.puts[t.box.puts.length - 1];
  assert.ok(!("context_paths" in sent), "no readable folder is left");
  assert.deepStrictEqual(sent.egress_allow, ["api.example.com", "*.example.org", "ok.example.com"]);
});

test("Also reachable: the files the original and Advanced control are rows with where to change them, not Remove", async () => {
  const t = cloudPage({ profiles: [PROFILES[0], cloudView({ clone_share: ["CLAUDE.md"] }), PROFILES[2], PROFILES[3]] });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "Edit");
  const files = rowsOf(t, "reachable").filter((r) => texts(r, "pfv-k")[0] === "Files");
  assert.strictEqual(files.length, 2);
  assert.deepStrictEqual(files.map((r) => texts(r, "pfv-inote")[0]), ["Set in Workspace", "Set in Advanced"]);
  for (const r of files) assert.strictEqual(byText(r, "button", "Remove").length, 0);
  assert.deepStrictEqual(rowsOf(t, "reachable").map((r) => texts(r, "pfv-k")[0]), ["Folder", "Files", "Files", "Domain", "Domain"]);
});

test("GitHub: the owner row shows its fields and a Read only preset; untouched, no permissions are written", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(field(t, "gh:0:pick").value, "app:acme-app");
  assert.strictEqual(field(t, "gh:0:owner").value, "culiops");
  assert.strictEqual(field(t, "gh:0:repos").value, "payments-api");
  assert.deepStrictEqual(byCls(blockOf(t, "cloud"), "pfv-permsum").map((n) => n.textContent), ["Read only (6 of 7: actions, checks, contents, pull requests, issues, metadata)"]);
  assert.strictEqual(byText(blockOf(t, "cloud"), "button", "Customise").length, 1);
  assert.strictEqual(field(t, "gh:perm:contents"), undefined, "the seven are behind Customise");
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  await saveBlock(t, "cloud");
  assert.ok(!("permissions" in t.box.puts[0].github), "the preset is the server's default: nothing is written");
});

test("GitHub customise sends only writable keys as write", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  clickBtn(blockOf(t, "cloud"), "Customise");
  for (const k of ["actions", "checks", "contents", "pull_requests", "issues", "metadata", "statuses"]) assert.ok(field(t, "gh:perm:" + k), k + " has a checkbox");
  assert.deepStrictEqual(["contents", "pull_requests", "issues"].map((k) => !!field(t, "gh:level:" + k)), [true, true, true], "read/write toggles on the three writable keys");
  for (const k of ["actions", "checks", "metadata", "statuses"]) assert.strictEqual(field(t, "gh:level:" + k), undefined, k + " can only be read");
  assert.strictEqual(field(t, "gh:perm:statuses").checked, false);
  tick(t, "gh:perm:statuses", true);
  setLevel(t, "gh:level:contents", "write");
  setLevel(t, "gh:level:issues", "write");
  assert.match(texts(blockOf(t, "cloud"), "pfv-warnw")[0], /^Write: this profile works in your folder/);
  assert.match(byCls(blockOf(t, "cloud"), "pfv-permsum")[0].textContent, /^Can write: contents, issues\. Read only: /);
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  assert.deepStrictEqual(t.box.puts[0].github.permissions, {
    actions: "read", checks: "read", contents: "write", pull_requests: "read", issues: "write", metadata: "read", statuses: "read",
  });
});

test("GitHub customise: unticking a write key and ticking it again gives read, and a custom set can be saved as ticked", async () => {
  const t = cloudPage({}, { github: { owners: [{ owner: "culiops", app: "acme-app", repos: ["payments-api"] }], permissions: { contents: "write", metadata: "read" } } });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  clickBtn(blockOf(t, "cloud"), "Customise");
  assert.strictEqual(field(t, "gh:perm:contents").checked, true);
  assert.strictEqual(field(t, "gh:level:contents").dataset.value, "write");
  assert.strictEqual(field(t, "gh:perm:actions").checked, false);
  tick(t, "gh:perm:contents", false);
  assert.strictEqual(field(t, "gh:level:contents"), undefined, "no level for a key that is off");
  tick(t, "gh:perm:contents", true);
  assert.strictEqual(field(t, "gh:level:contents").dataset.value, "read");
  setLevel(t, "gh:level:contents", "write");
  assert.ok(unsavedNote(t).hidden, "back to what was stored is no change");
  tick(t, "gh:perm:issues", true);
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(t.box.puts[0].github.permissions, { contents: "write", issues: "read", metadata: "read" });
});

test("GitHub with no permission ticked is refused before the PUT, with the sentence under Permissions", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  clickBtn(blockOf(t, "cloud"), "Customise");
  for (const k of ["actions", "checks", "contents", "pull_requests", "issues", "metadata"]) tick(t, "gh:perm:" + k, false);
  assert.strictEqual(await saveBlock(t, "cloud"), false);
  assert.strictEqual(t.box.puts.length, 0);
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-ferr"), ["GitHub: tick at least one permission, or the agent gets none."]);
  tick(t, "gh:perm:contents", true);
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-ferr"), [], "the sentence goes when a box is ticked");
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  assert.deepStrictEqual(t.box.puts[0].github.permissions, { contents: "read" });
});

test("GitHub owner signing in with a token: not narrowed, no permissions to customise, stored permissions kept", async () => {
  const doc = { github: { owners: [{ owner: "culiops", token: "gh-pat", repos: ["payments-api"] }], permissions: { contents: "read" } } };
  const t = cloudPage({ profiles: [PROFILES[0], cloudView({ github_pat: true, github_permissions: { contents: "read" } }), PROFILES[2], PROFILES[3]] }, doc);
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(field(t, "gh:0:pick").value, "token:gh-pat");
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-hint").filter((x) => /personal access token/.test(x)),
    ["A personal access token is not narrowed by CuliBox: it reaches whatever the token allows."]);
  assert.strictEqual(byText(blockOf(t, "cloud"), "button", "Customise").length, 0, "the server never applies the set to a token");
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-permsum").length, 0);
  typeInto(field(t, "gh:0:repos"), "payments-api, runbooks");
  await saveBlock(t, "cloud");
  const g = t.box.puts[0].github;
  assert.deepStrictEqual(g.owners, [{ owner: "culiops", token: "gh-pat", repos: ["payments-api", "runbooks"] }], "a token owner carries no app key");
  assert.deepStrictEqual(g.permissions, { contents: "read" }, "what was stored is kept");
});

test("GitHub: switching the login between an App and a token shows or hides the permissions", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  const sel = field(t, "gh:0:pick");
  assert.deepStrictEqual(sel.children.map((o) => o.value), ["app:acme-app", "token:gh-pat"]);
  assert.deepStrictEqual(sel.children.map((o) => o.textContent), ["acme-app · GitHub App, 1-hour tokens", "gh-pat · personal access token"]);
  sel.value = "token:gh-pat";
  sel.onchange();
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-permsum").length, 0);
  assert.strictEqual(unsavedNote(t).hidden, false);
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(t.box.puts[0].github.owners, [{ owner: "culiops", token: "gh-pat", repos: ["payments-api"] }]);
});

test("GitHub: a login the lists no longer have stays selectable, and the server's refusal goes under it in words", async () => {
  const t = cloudPage({ ghApps: [] }, { github: { owners: [{ owner: "culiops", app: "gone-app", repos: ["payments-api"] }] } });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(field(t, "gh:0:pick").value, "app:gone-app");
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": github.owners[0].app "gone-app" is not defined in github_apps', field: "github.owners[0].app" });
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-ferr"), ["Not found in Credentials. Remove it or create it."]);
  assert.ok(field(t, "gh:0:pick").className.includes("pfv-bad"));
});

test("a new GitHub owner needs a name, a repository and a login before anything is sent", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  optBtn(t, "cloud", "Another org or user").click();
  assert.strictEqual(field(t, "gh:1:pick").value, "app:acme-app", "the first login is preselected");
  assert.strictEqual(Node.focused, field(t, "gh:1:owner"));
  typeInto(field(t, "gh:1:repos"), "svc-a");
  assert.strictEqual(await saveBlock(t, "cloud"), false);
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-ferr"), ["Name the GitHub org or user."]);
  typeInto(field(t, "gh:1:owner"), "acme-platform");
  typeInto(field(t, "gh:1:repos"), "");
  assert.strictEqual(await saveBlock(t, "cloud"), false);
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-ferr"), ["List at least one repository."]);
  assert.strictEqual(t.box.puts.length, 0);
  typeInto(field(t, "gh:1:repos"), "svc-a, svc-b");
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  assert.deepStrictEqual(t.box.puts[0].github.owners, [
    { owner: "culiops", app: "acme-app", repos: ["payments-api"] },
    { owner: "acme-platform", app: "acme-app", repos: ["svc-a", "svc-b"] },
  ]);
  assert.deepStrictEqual(t.box.puts[0].github.egress_allow, ["extra.example.com"]);
});

test("a GitHub owner row left empty is dropped, as the old form did", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  optBtn(t, "cloud", "Another org or user").click();
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  assert.strictEqual(t.box.puts[0].github.owners.length, 1);
});

test("a refusal that names a row (a token that is not defined any more) goes under that row in words", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": tokens[1] "sentry-staging" is not defined in static_tokens', field: "tokens[1]" });
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "Token", "sentry-staging"), "pfv-ferr"), ["Not found in Credentials. Remove it or create it."]);
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "Token", "grafana-prod"), "pfv-ferr"), []);
  assert.ok(!/static_tokens|tokens\[/.test(t.els["pfv-in"].textContent));
});

test("the picker's seam: pfvAddRow puts a named connection or token in as a pending row, once", async () => {
  const sandbox = { name: "sandbox", account_id: "333333333333", region: "eu-west-1" };
  const t = cloudPage({ awscEntries: [...page().box.awscEntries, sandbox] });
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "aws", name: "sandbox" }), false, "the block is not open yet");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "aws", name: "sandbox" }), true);
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "AWS", "sandbox"), "pfv-rsub"), ["account 333333333333, eu-west-1"], "the row has the read view's words");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "aws", name: "sandbox" }), false, "it is there already");
  assert.strictEqual(unsavedNote(t).hidden, false);
  // A row that was removed comes back instead of being added twice.
  byText(rowFor(t, "cloud", "Token", "sentry-staging"), "button", "Remove")[0].click();
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "token", name: "sentry-staging" }), true);
  assert.ok(!rowFor(t, "cloud", "Token", "sentry-staging").className.includes("pfv-removed"));
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(t.box.puts[0].aws_connections, ["prod-read", "sandbox", "staging-read"]);
  assert.deepStrictEqual(t.box.puts[0].tokens, ["grafana-prod", "sentry-staging"]);
});

// The old-style AWS setting: the server's convert, today's confirm and write acknowledgement.
const legacyPage = () => cloudPage(
  { profiles: [PROFILES[0], cloudView({ aws_connections: undefined, aws_inline: { name: "legacy-prod", prod: true, elevate_max_minutes: 30 } }), PROFILES[2], PROFILES[3]] },
  { aws_connections: undefined, aws: { source_profile: "old", role_arn: "arn:aws:iam::123456789012:role/ops", region: "eu-west-1", label: "legacy-prod" } },
);
const convertCalls = (t) => t.calls.filter((c) => /convert-aws/.test(c));

test("the old-style AWS row offers Move to a saved connection, which asks and converts on the server", async () => {
  const t = legacyPage();
  const posts = [];
  const real = t.box.api;
  t.box.api = async (p, o) => {
    if (/convert-aws$/.test(p)) { posts.push(JSON.parse(o.body)); t.calls.push("POST " + p); return { connection: "aws-ops" }; }
    return real(p, o);
  };
  await t.box.pfvShow("aws-ops");
  const row = rowFor(t, "cloud", "AWS", "Old-style AWS setting");
  assert.deepStrictEqual(texts(row, "pfv-tag"), ["PROD"]);
  assert.deepStrictEqual(texts(row, "pfv-rsub"), ["legacy-prod"], "the binding's own label is the sub-line");
  await byText(row, "button", "Move to a saved connection")[0].click();
  assert.ok(t.calls.includes("confirm:Move to a saved connection:"));
  assert.deepStrictEqual(posts, [{}]);
  assert.ok(t.calls.includes("refreshCredLists"), "the new connection is listed before the profile is drawn");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-warn"), ["Converted to the connection aws-ops. The profile now uses it; the next session gets a new box."]);
});

test("a convert that needs the write acknowledgement asks again, then sends it", async () => {
  const t = legacyPage();
  const posts = [];
  const real = t.box.api;
  t.box.api = async (p, o) => {
    if (!/convert-aws$/.test(p)) return real(p, o);
    const body = JSON.parse(o.body);
    posts.push(body);
    if (!body.ack_write) throw apiErr(409, { error: "the role can write", code: "needs_ack", verdict: { actions: ["s3:PutObject"] } });
    return { connection: "aws-ops" };
  };
  await t.box.pfvShow("aws-ops");
  await byText(rowFor(t, "cloud", "AWS", "Old-style AWS setting"), "button", "Move to a saved connection")[0].click();
  assert.deepStrictEqual(posts, [{}, { ack_write: true }]);
  assert.strictEqual(t.calls.filter((c) => c.startsWith("confirm:")).length, 2);
});

test("a declined convert, a refusal and unsaved changes each leave the profile as it was", async () => {
  const t = legacyPage();
  const real = t.box.api;
  t.box.api = async (p, o) => { if (/convert-aws$/.test(p)) { t.calls.push("POST " + p); throw apiErr(400, { error: "no role to convert" }); } return real(p, o); };
  await t.box.pfvShow("aws-ops");
  const link = () => byText(rowFor(t, "cloud", "AWS", "Old-style AWS setting"), "button", "Move to a saved connection")[0];
  t.box.confirmAnswer = false;
  await link().click();
  assert.deepStrictEqual(convertCalls(t), [], "declined: nothing is sent");
  t.box.confirmAnswer = true;
  await link().click();
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-err"), ["aws: no role to convert"], "a refusal reads as the Credentials screen words it");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  const before = convertCalls(t).length;
  await link().click();
  assert.strictEqual(convertCalls(t).length, before, "unsaved changes would be lost by the convert: it is refused");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-nudge"), ["Save or discard these changes first"]);
});

test("the old-style AWS row can be removed in the form, and the save drops the setting", async () => {
  const t = legacyPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  const row = () => rowFor(t, "cloud", "AWS", "Old-style AWS setting");
  assert.strictEqual(byText(row(), "button", "Move to a saved connection").length, 1, "the form offers the move too");
  byText(row(), "button", "Remove")[0].click();
  assert.ok(row().className.includes("pfv-removed"));
  assert.strictEqual(unsavedNote(t).hidden, false);
  await saveBlock(t, "cloud");
  assert.ok(!("aws" in t.box.puts[0]));
  const again = legacyPage();
  await again.box.pfvShow("aws-ops");
  clickBtn(blockOf(again, "cloud"), "Edit");
  await saveBlock(again, "cloud");
  assert.ok("aws" in again.box.puts[0], "an old-style setting that is not removed goes back as stored");
});

test("a missing credential stays red while its removal is pending: no rule that is about a removed row sets the colour of .pfv-miss", () => {
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ sel: m[1].trim(), body: m[2] }));
  const sets = (r, prop) => new RegExp("(?:^|[;\\s])" + prop + "\\s*:").test(r.body);
  const miss = rules.filter((r) => /\.pfv-miss(?![\w-])/.test(r.sel));
  assert.ok(miss.some((r) => !/\.pfv-removed/.test(r.sel) && /color:\s*var\(--red\)/.test(r.body)), "Not found in Credentials is red (the founder's decision)");
  // The strike shows the removal is pending; the colour stays. A rule may strike it, none may recolour it.
  const recolour = miss.filter((r) => /\.pfv-removed/.test(r.sel) && sets(r, "color"));
  assert.deepStrictEqual(recolour.map((r) => r.sel), [], "a pending removal must not mute the red text");
  assert.ok(miss.some((r) => /\.pfv-removed/.test(r.sel) && /line-through/.test(r.body)), "and it is struck through like the rest of the row");
});

// The server counts `aws_connections[i]` in the list the failing PUT carried: the stored one when another block saved, the one
// the Cloud merge builds (sorted) when Cloud was part of the save. A stored list nobody sorted tells the two apart.
const unsortedPage = (lists) => cloudPage(
  { profiles: [PROFILES[0], cloudView(lists), PROFILES[2], PROFILES[3]] }, lists,
);
const undefinedRef = (field, name, table) => apiErr(400, { error: 'profile "aws-ops": ' + field + " " + JSON.stringify(name) + " is not defined in " + table, field });

test("a refusal from another block's save lands under the row the server counted: the stored order, not the sorted one", async () => {
  for (const [key, kind, table, first, second] of [["aws_connections", "AWS", "aws_connections", "staging-read", "prod-read"], ["tokens", "Token", "static_tokens", "sentry-staging", "grafana-prod"]]) {
    const t = unsortedPage({ [key]: [first, second] });
    await t.box.pfvShow("aws-ops");
    clickBtn(blockOf(t, "agent"), "Edit");
    pickModel(t, "claude-sonnet-5");
    t.box.putFail = undefinedRef(key + "[0]", first, table);
    await saveBlock(t, "agent");
    assert.deepStrictEqual(t.box.puts[0][key], [first, second], "the agent's save carried the stored order");
    assert.deepStrictEqual(Object.keys(t.run("pfvState").edits).sort(), ["agent", "cloud"]);
    assert.deepStrictEqual(texts(rowFor(t, "cloud", kind, first), "pfv-ferr"), ["Not found in Credentials. Remove it or create it."], key + ": the blamed row");
    assert.deepStrictEqual(texts(rowFor(t, "cloud", kind, second), "pfv-ferr"), [], key + ": a good credential is not blamed");
  }
});

test("a refusal from another block's save names a folder or a domain by its place in the stored list", async () => {
  const folders = ["/home/you/a", "/home/you/b", "/home/you/c"];
  const t = unsortedPage({ context_paths: folders, egress_allow: ["x.example.com", "y.example.com"] });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": context_paths[1] "/home/you/b": does not exist', field: "context_paths[1]" });
  await saveBlock(t, "agent");
  const folderErrs = folders.map((p) => texts(rowFor(t, "reachable", "Folder", p), "pfv-ferr"));
  assert.deepStrictEqual(folderErrs, [[], ["This folder no longer exists. Remove it or pick another."], []]);
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": egress_allow[1] "y.example.com": must be a normalised lowercase hostname (no trailing dot, no IP literal) or *.host', field: "egress_allow[1]" });
  await saveBlock(t, "agent");
  assert.deepStrictEqual(["x.example.com", "y.example.com"].map((h) => texts(rowFor(t, "reachable", "Domain", h), "pfv-ferr").length), [0, 1]);
});

test("a refusal from Cloud's own save lands under the row of the sorted list it sent", async () => {
  const t = unsortedPage({ aws_connections: ["staging-read", "prod-read"] });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  t.box.putFail = undefinedRef("aws_connections[0]", "prod-read", "aws_connections");
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(t.box.puts[0].aws_connections, ["prod-read", "staging-read"], "Cloud sends its list sorted");
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "AWS", "prod-read"), "pfv-ferr"), ["Not found in Credentials. Remove it or create it."]);
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "AWS", "staging-read"), "pfv-ferr"), []);
});

test("an unrelated block's save refused for a credential opens Cloud with the error under that row; Remove and Save send one PUT with both changes", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = undefinedRef("aws_connections[1]", "staging-read", "aws_connections");
  assert.strictEqual(await saveBlock(t, "agent"), false);
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits).sort(), ["agent", "cloud"], "the one-open-block rule yields to repair");
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "AWS", "staging-read"), "pfv-ferr"), ["Not found in Credentials. Remove it or create it."]);
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-top-err"), ["Not saved: an AWS connection in Cloud and tokens needs a fix first. Show"]);
  assert.ok(!/aws_connections/.test(t.els["pfv-in"].textContent), "no config keys on the page");
  t.box.putFail = null;
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  assert.strictEqual(t.box.puts.length, 2, "the refused PUT and one that went through");
  assert.deepStrictEqual(t.box.puts[1].aws_connections, ["prod-read"]);
  assert.strictEqual(t.box.puts[1].options.model, "claude-sonnet-5", "the Agent change went in the same PUT");
  assert.deepStrictEqual(t.box.puts[1].tokens, ["grafana-prod", "sentry-staging"]);
  assert.strictEqual(Object.keys(t.run("pfvState").edits).length, 0);
});

test("Discard after several removes and adds sends nothing and leaves the profile as stored", async () => {
  const sandbox = { name: "sandbox", account_id: "333333333333", region: "eu-west-1" };
  const t = cloudPage({ awscEntries: [...page().box.awscEntries, sandbox] });
  await t.box.pfvShow("aws-ops");
  const before = rowsOf(t, "cloud").map((r) => r.textContent);
  const stored = JSON.stringify(t.run("pfvState").doc);
  clickBtn(blockOf(t, "cloud"), "Edit");
  for (const [kind, name] of [["AWS", "staging-read"], ["Token", "grafana-prod"], ["GitHub", "culiops"]]) byText(rowFor(t, "cloud", kind, name), "button", "Remove")[0].click();
  t.box.pfvAddRow("cloud", { kind: "aws", name: "sandbox" });
  t.box.pfvAddRow("cloud", { kind: "github" });
  typeInto(field(t, "gh:1:owner"), "acme-platform");
  assert.strictEqual(unsavedNote(t).hidden, false);
  assert.strictEqual(t.run("pfvState").pending, true);
  clickBtn(blockOf(t, "cloud"), "Discard");
  assert.strictEqual(t.box.puts.length, 0, "nothing was sent");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0);
  assert.deepStrictEqual(rowsOf(t, "cloud").map((r) => r.textContent), before, "the read view is as it was");
  assert.strictEqual(JSON.stringify(t.run("pfvState").doc), stored, "so is the stored profile");
  assert.ok(unsavedNote(t).hidden);
  assert.strictEqual(t.run("pfvState").pending, false);
  // Opened again, the form starts from what is stored: nothing struck, nothing added.
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-removed").length, 0);
  assert.deepStrictEqual(rowsOf(t, "cloud").map((r) => texts(r, "pfv-k")[0]), ["AWS", "AWS", "GitHub", "Token", "Token"]);
  assert.ok(unsavedNote(t).hidden);
});

test("every top-level name in pfv-cloud.js is unique across the page scripts", () => {
  const names = (src) => new Set([...src.matchAll(/^(?:async\s+)?(?:function\s+|const\s+|let\s+|var\s+)([A-Za-z_$][\w$]*)/gm)].map((m) => m[1]));
  const mine = names(fs.readFileSync(path.join(STATIC, "pfv-cloud.js"), "utf8"));
  assert.ok(mine.size > 10);
  // pfc/PFC_ for its own; the pfv names are the page's seams for the pages that call into it.
  const seams = new Set(["pfvOpenPicker", "pfvClosePicker", "pfvAddRow", "pfvConvertAws"]);
  assert.ok([...mine].every((n) => /^(pfc|PFC_)/.test(n) || seams.has(n)), "every name is pfc/PFC_ prefixed or a seam: " + [...mine].join(", "));
  const clashes = [];
  for (const f of fs.readdirSync(STATIC).filter((x) => x.endsWith(".js") && x !== "pfv-cloud.js")) {
    for (const n of names(fs.readFileSync(path.join(STATIC, f), "utf8"))) if (mine.has(n)) clashes.push(n + " (" + f + ")");
  }
  assert.deepStrictEqual(clashes, []);
});

test("Cloud and tokens and Also reachable register their editors through the seam", () => {
  const t = page();
  for (const id of ["cloud", "reachable"]) {
    assert.ok(t.run("PFV_EDITORS")[id], id + " registered itself");
    assert.throws(() => t.box.pfvRegisterEditor(id, {}), new RegExp("registered twice: " + id));
  }
});

// --- Advanced (spec 5.3, 5.5; pfv-advanced.js) -----------------------------------------------------------------------------

const ENVROWS = require("../static/envrows.js");
const advOf = (t) => blockOf(t, "advanced");
const advLabels = (t) => texts(advOf(t), "pfv-flabel");
// The refusals the user can see: a field's line is hidden, not removed, once its words are stale.
const advErrs = (t) => byCls(advOf(t), "pfv-ferr").filter((n) => !n.hidden).map((n) => n.textContent);
const openAdv = async (t, name, doc) => {
  if (doc) t.box.docs[name] = Object.assign({}, DOCS[name], doc);
  await t.box.pfvShow(name);
  clickBtn(advOf(t), "Edit");
  return advOf(t);
};
const typeArgs = (t, text) => { const a = field(t, "args"); a.value = text; a.oninput(); };
const advTick = (t, key, on) => { const c = field(t, key); c.checked = on; c.onchange(); };
// The variables and secrets: rows of the page's grammar (a kind column, the content, Remove or Undo).
const envRowsOf = (t) => byCls(advOf(t), "pfv-row");
const rowInputs = (row) => row.all((n) => n.tag === "input");
const rowName = (row) => rowInputs(row)[0];
const rowRm = (row) => byCls(row, "pfv-rmbtn")[0];
const typeRow = (row, name, value) => {
  const k = rowName(row);
  k.value = name; k.oninput();
  if (value !== undefined) { const v = rowInputs(row)[1]; v.value = value; v.oninput(); }
};
const unsaved = (t) => !byCls(t.els["pfv-in"], "pfv-unsaved")[0].hidden;

test("advanced opens by itself when something is set", async () => {
  // Nothing set: folded, with nothing to unfold; the Edit link opens the form all the same.
  const t = page();
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(texts(advOf(t), "pfv-label")[0], "Advanced · none set");
  assert.ok(!advOf(t).className.includes("pfv-open"));
  assert.strictEqual(byCls(advOf(t), "pfv-fold").length, 0);
  // Something set: open, listing it, and the form opened from the Edit link holds it.
  const set = page();
  set.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { args: ["--verbose"], env: { LOG_LEVEL: "debug" } });
  await set.box.pfvShow("aws-ops");
  assert.strictEqual(texts(advOf(set), "pfv-label")[0], "Advanced · 2 set");
  assert.ok(advOf(set).className.includes("pfv-open"), "open without a click");
  assert.strictEqual(byCls(advOf(set), "pfv-fold")[0].getAttribute("aria-expanded"), "true");
  assert.deepStrictEqual(texts(advOf(set), "pfv-dl"), ["Arguments--verboseEnvironmentLOG_LEVEL"]);
  clickBtn(advOf(set), "Edit");
  assert.ok(advOf(set).className.includes("pfv-editing"));
  assert.strictEqual(field(set, "args").value, "--verbose");
  assert.deepStrictEqual(envRowsOf(set).map((r) => [rowInputs(r)[0].value, rowInputs(r)[1].value]), [["LOG_LEVEL", "debug"]]);
  // The first setting saved from "none set" opens the fold by itself, with no click.
  await openAdv(t, "aws-ops");
  typeArgs(t, "--verbose");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.strictEqual(texts(advOf(t), "pfv-label")[0], "Advanced · 1 set");
  assert.ok(advOf(t).className.includes("pfv-open"));
  assert.deepStrictEqual(t.box.puts[0].args, ["--verbose"]);
});

test("base is not offered on a box profile and its stored value is kept", async () => {
  const t = page();
  await openAdv(t, "aws-ops", { base: "clean" });
  assert.strictEqual(field(t, "base"), undefined, "no select, no disabled wall");
  assert.strictEqual(byCls(advOf(t), "pfv-select").length, 0);
  assert.deepStrictEqual(texts(advOf(t), "pfv-hint").filter((x) => /starts with/.test(x)), ["Environment it starts with only applies on this machine; a box always starts minimal."]);
  assert.ok(!advLabels(t).includes("Environment it starts with"));
  typeArgs(t, "--verbose");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.strictEqual(t.box.puts[0].base, "clean", "a save from a box profile keeps the base the profile has");
  assert.deepStrictEqual(t.box.puts[0].args, ["--verbose"]);
  // A host profile does offer it, in the mockup's words; the default is stored as no base.
  const h = page();
  await openAdv(h, "runbook-host", { base: "clean" });
  assert.deepStrictEqual(Array.from(field(h, "base").children).map((o) => o.textContent), ["Everything you have (inherit)", "Minimal, no secrets (clean)", "Your login shell (login)"]);
  assert.strictEqual(field(h, "base").value, "clean");
  assert.strictEqual(byCls(advOf(h), "pfv-hint").filter((n) => /starts with only applies/.test(n.textContent)).length, 0, "the sentence is for a box");
  field(h, "base").value = "inherit"; field(h, "base").onchange();
  assert.strictEqual(await clickBtn(advOf(h), "Save"), true);
  assert.ok(!("base" in h.box.puts[0]), "inherit is the default: no base is stored");
  const login = page();
  await openAdv(login, "runbook-host");
  field(login, "base").value = "login"; field(login, "base").onchange();
  await clickBtn(advOf(login), "Save");
  assert.strictEqual(login.box.puts[0].base, "login");
});

test("env row errors block save", async () => {
  const t = page();
  await openAdv(t, "aws-ops");
  clickBtn(advOf(t), "+ Add variable");
  assert.strictEqual(envRowsOf(t).length, 1);
  assert.strictEqual(Node.focused, rowName(envRowsOf(t)[0]), "focus goes to the new row's name");
  typeRow(envRowsOf(t)[0], "1BAD", "x");
  const want = ENVROWS.rowError([{ key: "1BAD", value: "x", kind: "literal" }]);
  assert.ok(want.length > 0);
  assert.strictEqual(await clickBtn(advOf(t), "Save"), false);
  assert.strictEqual(t.box.puts.length, 0, "nothing is sent");
  assert.deepStrictEqual(advErrs(t), [want], "the sentence is under the variables, inside the block");
  assert.deepStrictEqual(texts(advOf(t), "pfv-st-invalid"), ["One field needs a fix before this can save."]);
  assert.strictEqual(envRowsOf(t).length, 1, "what was typed is still there");
  assert.strictEqual(rowName(envRowsOf(t)[0]).value, "1BAD");
  // Fixing the name takes the sentence away, and the same Save goes through.
  typeRow(envRowsOf(t)[0], "LEVEL");
  assert.deepStrictEqual(advErrs(t), [], "the refusal goes when the rows change");
  // A name listed twice is refused too, literal against secret as the server does.
  clickBtn(advOf(t), "+ Add secret");
  typeRow(envRowsOf(t)[1], "LEVEL");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), false);
  assert.deepStrictEqual(advErrs(t), ['Listed twice: "LEVEL".']);
  assert.strictEqual(t.box.puts.length, 0);
  typeRow(envRowsOf(t)[1], "LEVEL_FROM_STORE");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.deepStrictEqual(t.box.puts[0].env, { LEVEL: "x" });
  assert.deepStrictEqual(t.box.puts[0].secrets, ["LEVEL_FROM_STORE"]);
  // Only the rows that stay are checked: a stored row whose name was broken blocks Save while it is there, and not once it is pending removal.
  const g = page();
  await openAdv(g, "aws-ops", { env: { OLD: "1", KEEP: "2" } });
  typeRow(envRowsOf(g)[0], "1BAD");
  assert.strictEqual(await clickBtn(advOf(g), "Save"), false);
  assert.deepStrictEqual(advErrs(g), [ENVROWS.rowError([{ key: "1BAD", value: "1", kind: "literal" }])]);
  rowRm(envRowsOf(g)[0]).click();
  assert.deepStrictEqual(advErrs(g), [], "the refusal was about a row that is going");
  assert.strictEqual(await clickBtn(advOf(g), "Save"), true);
  assert.deepStrictEqual(g.box.puts[0].env, { KEEP: "2" });
  // A name listed twice counts only among the rows that stay: remove A, add A again.
  const d = page();
  await openAdv(d, "aws-ops", { env: { A: "1" } });
  rowRm(envRowsOf(d)[0]).click();
  clickBtn(advOf(d), "+ Add variable");
  typeRow(envRowsOf(d)[1], "A", "2");
  assert.strictEqual(await clickBtn(advOf(d), "Save"), true);
  assert.deepStrictEqual(d.box.puts[0].env, { A: "2" });
});

test("removing an env row is pending until save and undo restores it", async () => {
  const t = page();
  await openAdv(t, "aws-ops", { env: { KEEP: "1", DROP: "2" }, secrets: ["TOKEN_NAME"] });
  // The mockup's rows: the kind, then a name and a value (a secret: a name and where it is read from), then Remove.
  const rows = envRowsOf(t);
  assert.deepStrictEqual(rows.map((r) => texts(r, "pfv-k")[0]), ["Variable", "Variable", "Secret"]);
  assert.deepStrictEqual(rowInputs(rows[0]).map((i) => [i.getAttribute("aria-label"), i.getAttribute("placeholder"), i.value]), [["Variable name", "NAME", "KEEP"], ["Value", "value", "1"]]);
  assert.deepStrictEqual(rowInputs(rows[2]).map((i) => [i.getAttribute("aria-label"), i.getAttribute("placeholder"), i.value]), [["Secret name", "NAME", "TOKEN_NAME"]]);
  assert.deepStrictEqual(texts(rows[2], "pfv-hint"), ["Read from the credential store when it starts"]);
  assert.deepStrictEqual(rows.map((r) => rowRm(r).textContent), ["Remove", "Remove", "Remove"]);
  assert.ok(rows.every((r) => !r.className.includes("pfv-removed")) && !unsaved(t));
  // Edit the value, then Remove: the row stays, struck through, with Undo; the form holds a change; nothing is sent.
  rowInputs(rows[1])[1].value = "3"; rowInputs(rows[1])[1].oninput();
  rowRm(rows[1]).click();
  let now = envRowsOf(t);
  assert.strictEqual(now.length, 3, "the row is still listed");
  assert.ok(now[1].className.includes("pfv-removed"));
  assert.strictEqual(rowInputs(now[1]).length, 0, "its words, not inputs");
  assert.deepStrictEqual(texts(now[1], "pfv-mono"), ["DROP", "3"]);
  assert.strictEqual(rowRm(now[1]).textContent, "Undo");
  assert.strictEqual(rowRm(now[1]).getAttribute("aria-label"), "Undo removing variable DROP");
  assert.strictEqual(Node.focused, rowRm(now[1]), "focus stays on the button");
  assert.ok(unsaved(t));
  assert.strictEqual(t.box.puts.length, 0);
  // Undo: the row is back as it was left, typed value included.
  rowRm(now[1]).click();
  now = envRowsOf(t);
  assert.ok(!now[1].className.includes("pfv-removed"));
  assert.deepStrictEqual(rowInputs(now[1]).map((i) => i.value), ["DROP", "3"]);
  assert.strictEqual(rowRm(now[1]).textContent, "Remove");
  rowInputs(now[1])[1].value = "2"; rowInputs(now[1])[1].oninput();
  assert.ok(!unsaved(t), "removed and brought back, value put back: no change");
  // A row added in this edit and then removed just disappears.
  clickBtn(advOf(t), "+ Add variable");
  assert.strictEqual(envRowsOf(t).length, 4);
  rowRm(envRowsOf(t)[3]).click();
  assert.strictEqual(envRowsOf(t).length, 3);
  assert.ok(envRowsOf(t).every((r) => !r.className.includes("pfv-removed")));
  assert.ok(!unsaved(t));
  // Removed and saved: the row is not sent, the others are.
  rowRm(envRowsOf(t)[1]).click();
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.deepStrictEqual(t.box.puts[0].env, { KEEP: "1" });
  assert.deepStrictEqual(t.box.puts[0].secrets, ["TOKEN_NAME"]);
  // The last row removed: it is still listed, struck, until Save; then env is gone.
  const e = page();
  await openAdv(e, "aws-ops", { env: { ONE: "1" } });
  rowRm(envRowsOf(e)[0]).click();
  assert.strictEqual(envRowsOf(e).length, 1);
  assert.deepStrictEqual(texts(advOf(e), "pfv-hint").filter((x) => /^None/.test(x)), [], "the empty line waits for the save");
  await clickBtn(advOf(e), "Save");
  assert.ok(!("env" in e.box.puts[0]));
});

test("a removed secret is not sent", async () => {
  const t = page();
  await openAdv(t, "aws-ops", { secrets: ["A_SECRET", "B_SECRET"], env: { KEEP: "1" } });
  const secrets = () => envRowsOf(t).filter((r) => texts(r, "pfv-k")[0] === "Secret");
  rowRm(secrets()[0]).click();
  assert.ok(secrets()[0].className.includes("pfv-removed"));
  assert.deepStrictEqual(texts(secrets()[0], "pfv-mono"), ["A_SECRET"], "a secret has only its name to strike");
  assert.deepStrictEqual(texts(secrets()[0], "pfv-hint"), [], "and no line about where it is read from");
  assert.strictEqual(rowRm(secrets()[0]).getAttribute("aria-label"), "Undo removing secret A_SECRET");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.deepStrictEqual(t.box.puts[0].secrets, ["B_SECRET"]);
  assert.deepStrictEqual(t.box.puts[0].env, { KEEP: "1" });
  // Every secret removed: the key goes.
  const u = page();
  await openAdv(u, "aws-ops", { secrets: ["ONLY"] });
  rowRm(envRowsOf(u)[0]).click();
  await clickBtn(advOf(u), "Save");
  assert.ok(!("secrets" in u.box.puts[0]));
});

test("imports row hidden when bundling is off keeps imports_off", async () => {
  const t = page({ bundleImports: false });
  await openAdv(t, "aws-ops", { imports_off: true });
  assert.strictEqual(field(t, "imports_off"), undefined, "the row is not drawn");
  assert.ok(!/CLAUDE\.md/.test(advOf(t).textContent), "and not explained in words nobody asked for");
  typeArgs(t, "--verbose");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.strictEqual(t.box.puts[0].imports_off, true, "the stored value goes back as it was");
  // Before /api/meta has answered nothing is known: the row stays. Once it has, a bundling instance shows it too.
  const early = page({ metaLoaded: false, bundleImports: false });
  await openAdv(early, "aws-ops");
  assert.ok(field(early, "imports_off"), "the row is there until /api/meta says bundling is off");
  const on = page();
  await openAdv(on, "aws-ops", { imports_off: true });
  assert.strictEqual(field(on, "imports_off").checked, false, "off is unchecked");
  advTick(on, "imports_off", true);
  assert.strictEqual(await clickBtn(advOf(on), "Save"), true);
  assert.ok(!("imports_off" in on.box.puts[0]), "checked is the default: nothing stored");
  // A row that stops being usable while the form is open has no say any more: what was ticked in it is not sent.
  const late = page();
  await openAdv(late, "aws-ops");
  advTick(late, "imports_off", false);
  late.box.bundleImports = false;
  assert.strictEqual(await clickBtn(advOf(late), "Save"), true);
  assert.ok(!("imports_off" in late.box.puts[0]), "the stored value, which was none");
  // A host profile has no box to give the files to; an account with no personal config says so in the row's own words.
  const host = page();
  await openAdv(host, "runbook-host", { imports_off: true });
  assert.strictEqual(field(host, "imports_off"), undefined);
  const acct = page({ accountsCache: [{ name: "work" }] });
  await openAdv(acct, "aws-ops", { account: "work", imports_off: true });
  assert.strictEqual(field(acct, "imports_off"), undefined);
  assert.deepStrictEqual(texts(advOf(acct), "pfv-hint").filter((x) => /personal config/.test(x)), ["This account does not use your personal config, so its boxes get no CLAUDE.md imports."]);
  typeArgs(acct, "--x");
  await clickBtn(advOf(acct), "Save");
  assert.strictEqual(acct.box.puts[0].imports_off, true);
});

test("Advanced edit form: the mockup's fields in order, for a box profile and for a host profile", async () => {
  const t = page({ repliesEnabled: true });
  await openAdv(t, "aws-ops");
  assert.deepStrictEqual(advLabels(t), ["Extra command-line arguments", "Environment variables"]);
  assert.deepStrictEqual(texts(advOf(t), "pfv-check-t"), ["Bypass permissions", "Answer prompts from the sidebarSets the default in New session.", "Include files my CLAUDE.md pulls in with @Read-only copies in the box."]);
  assert.strictEqual(field(t, "args").getAttribute("placeholder"), "--some-flag");
  assert.deepStrictEqual(texts(advOf(t), "pfv-hint"), ["One per line.", "None. The agent gets the box's minimal environment.", "Environment it starts with only applies on this machine; a box always starts minimal."]);
  assert.strictEqual(byText(advOf(t), "button", "+ Add variable").length + byText(advOf(t), "button", "+ Add secret").length, 2);
  assert.strictEqual(Node.focused, field(t, "options.skip_permissions"), "focus goes to the first control");
  const h = page({ repliesEnabled: true });
  await openAdv(h, "runbook-host");
  assert.deepStrictEqual(advLabels(h), ["Environment it starts with", "Extra command-line arguments", "Environment variables"]);
  assert.deepStrictEqual(texts(advOf(h), "pfv-check-t"), ["Bypass permissions", "Answer prompts from the sidebarSets the default in New session."]);
  assert.deepStrictEqual(texts(advOf(h), "pfv-hint"), ["One per line.", "None added on top of the starting environment."]);
  assert.strictEqual(Node.focused, field(h, "base"));
  assert.ok(advOf(h).className.includes("pfv-editing"));
  // Opened and not touched holds no change, and the model and effort stay in the Agent block.
  assert.ok(byCls(t.els["pfv-in"], "pfv-unsaved")[0].hidden);
  assert.strictEqual(field(t, "options.model"), undefined);
  assert.strictEqual(field(t, "options.effort"), undefined);
});

test("Advanced: the other options are drawn the old form's way and only what the user set is written", async () => {
  const defs = OPTION_DEFS.concat([
    { key: "verbosity", label: "Verbosity", type: "enum", choices: ["quiet", "normal", "loud"], bins: ["claude"] },
    { key: "profile_pack", label: "Pack", type: "enum", choices: ["a", "b", "c", "d", "e"], bins: ["claude"] },
    { key: "shell_only", label: "Shell only", type: "bool", bins: ["bash"] },
  ]);
  const t = page({ optionDefs: defs });
  await openAdv(t, "aws-ops", { options: { model: "claude-opus-5-5", effort: "high", legacy_key: "keep me" } });
  assert.deepStrictEqual(advLabels(t).slice(0, 2), ["Verbosity", "Pack"]);
  assert.ok(hasCls("seg")(field(t, "options.verbosity")), "three choices: a segmented control");
  assert.strictEqual(field(t, "options.profile_pack").tag, "select", "five: a select");
  assert.deepStrictEqual(Array.from(field(t, "options.profile_pack").children).map((o) => o.textContent), ["(not set)", "a", "b", "c", "d", "e"]);
  assert.strictEqual(field(t, "options.shell_only"), undefined, "an option of another command is not drawn");
  assert.ok(byCls(t.els["pfv-in"], "pfv-unsaved")[0].hidden, "nothing changed yet");
  advTick(t, "options.skip_permissions", true);
  field(t, "options.profile_pack").value = "c"; field(t, "options.profile_pack").onchange();
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.deepStrictEqual(t.box.puts[0].options, { model: "claude-opus-5-5", effort: "high", legacy_key: "keep me", skip_permissions: true, profile_pack: "c" });
  // Untouched, an option that was never stored is not written as false; one stored as false stays false; unchecking a stored true says false.
  const u = page();
  await openAdv(u, "aws-ops", { options: { model: "claude-opus-5-5", skip_permissions: false } });
  typeArgs(u, "--x");
  await clickBtn(advOf(u), "Save");
  assert.deepStrictEqual(u.box.puts[0].options, { model: "claude-opus-5-5", skip_permissions: false });
  const n = page();
  await openAdv(n, "aws-ops");
  typeArgs(n, "--x");
  await clickBtn(advOf(n), "Save");
  assert.deepStrictEqual(n.box.puts[0].options, { model: "claude-opus-5-5", effort: "high" });
  const off = page();
  await openAdv(off, "aws-ops", { options: { model: "claude-opus-5-5", skip_permissions: true } });
  assert.strictEqual(field(off, "options.skip_permissions").checked, true);
  advTick(off, "options.skip_permissions", false);
  await clickBtn(advOf(off), "Save");
  assert.deepStrictEqual(off.box.puts[0].options, { model: "claude-opus-5-5", skip_permissions: false });
});

test("Advanced: arguments are one per line, blanks dropped; emptying them removes the key", async () => {
  const t = page();
  await openAdv(t, "aws-ops", { args: ["--a", "--b value"] });
  assert.strictEqual(field(t, "args").value, "--a\n--b value");
  typeArgs(t, "  --a \n\n--c\n  \n");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-unsaved")[0].hidden, false);
  await clickBtn(advOf(t), "Save");
  assert.deepStrictEqual(t.box.puts[0].args, ["--a", "--c"]);
  const e = page();
  await openAdv(e, "aws-ops", { args: ["--a"] });
  typeArgs(e, "");
  await clickBtn(advOf(e), "Save");
  assert.ok(!("args" in e.box.puts[0]));
});

test("Advanced: an emptied list says its line, in the mockup's words for a box and for this machine; a blank row is not a variable", async () => {
  const t = page();
  await openAdv(t, "aws-ops", { env: { KEEP: "1" } });
  clickBtn(advOf(t), "+ Add variable");
  typeRow(envRowsOf(t)[1], "ADDED", " v ");
  await clickBtn(advOf(t), "Save");
  assert.deepStrictEqual(t.box.puts[0].env, { KEEP: "1", ADDED: "v" }, "values are trimmed");
  const none = page();
  await openAdv(none, "aws-ops");
  assert.deepStrictEqual(envRowsOf(none), []);
  assert.deepStrictEqual(texts(advOf(none), "pfv-hint").filter((x) => /^None/.test(x)), ["None. The agent gets the box's minimal environment."]);
  const host = page();
  await openAdv(host, "runbook-host");
  assert.deepStrictEqual(texts(advOf(host), "pfv-hint").filter((x) => /^None/.test(x)), ["None added on top of the starting environment."]);
  // The line goes when a row is added and comes back when it is taken away again.
  clickBtn(advOf(host), "+ Add secret");
  assert.deepStrictEqual(texts(advOf(host), "pfv-hint").filter((x) => /^None/.test(x)), []);
  assert.deepStrictEqual(texts(envRowsOf(host)[0], "pfv-k"), ["Secret"]);
  rowRm(envRowsOf(host)[0]).click();
  assert.deepStrictEqual(texts(advOf(host), "pfv-hint").filter((x) => /^None/.test(x)), ["None added on top of the starting environment."]);
  // A row added and left blank is not a variable.
  const b = page();
  await openAdv(b, "aws-ops");
  clickBtn(advOf(b), "+ Add variable");
  clickBtn(advOf(b), "+ Add secret");
  assert.strictEqual(await clickBtn(advOf(b), "Save"), true);
  assert.ok(!("env" in b.box.puts[0]) && !("secrets" in b.box.puts[0]));
});

test("Advanced: replies are offered for a claude command on an instance that has them, and the stored value is kept when they are not", async () => {
  const on = page({ repliesEnabled: true });
  await openAdv(on, "aws-ops");
  assert.strictEqual(field(on, "replyable").checked, false);
  advTick(on, "replyable", true);
  await clickBtn(advOf(on), "Save");
  assert.strictEqual(on.box.puts[0].replyable, true);
  const clear = page({ repliesEnabled: true });
  await openAdv(clear, "aws-ops", { replyable: true });
  assert.strictEqual(field(clear, "replyable").checked, true);
  advTick(clear, "replyable", false);
  await clickBtn(advOf(clear), "Save");
  assert.ok(!("replyable" in clear.box.puts[0]));
  // The same when the instance's answer lands after the box was ticked.
  const late = page({ repliesEnabled: true });
  await openAdv(late, "aws-ops");
  advTick(late, "replyable", true);
  late.box.repliesEnabled = false;
  await clickBtn(advOf(late), "Save");
  assert.ok(!("replyable" in late.box.puts[0]));
  // The instance has replies off: no row, the profile's default stays.
  const off = page({ repliesEnabled: false });
  await openAdv(off, "aws-ops", { replyable: true });
  assert.strictEqual(field(off, "replyable"), undefined);
  typeArgs(off, "--x");
  await clickBtn(advOf(off), "Save");
  assert.strictEqual(off.box.puts[0].replyable, true);
  // Not a claude command: replies are claude-only.
  const shell = page({ repliesEnabled: true });
  await openAdv(shell, "aws-ops", { bin: "bash", replyable: true });
  assert.strictEqual(field(shell, "replyable"), undefined);
  typeArgs(shell, "--x");
  await clickBtn(advOf(shell), "Save");
  assert.strictEqual(shell.box.puts[0].replyable, true);
});

test("Advanced: a refusal that names args, an option, the variables or the base goes under that field", async () => {
  // under: the node the refusal's line must share a field with.
  const refuse = async (open, fieldPath, under) => {
    const t = page({ repliesEnabled: true });
    await open(t);
    typeArgs(t, "--x");
    t.box.putFail = apiErr(400, { error: 'profile "aws-ops": refused', field: fieldPath });
    assert.strictEqual(await clickBtn(advOf(t), "Save"), false);
    assert.deepStrictEqual(texts(advOf(t), "pfv-top-err"), [], "not at the top: " + fieldPath);
    const shown = byCls(advOf(t), "pfv-ferr").filter((n) => !n.hidden);
    assert.strictEqual(shown.length, 1, "one refusal for " + fieldPath);
    assert.ok(under(shown[0].parent), "and it is in the field of " + fieldPath);
    return t;
  };
  const hasField = (key) => (n) => n.all((x) => x.dataset && x.dataset.pfvField === key).length > 0;
  const hasRows = (n) => byCls(n, "pfv-env").length > 0;
  const t = await refuse((x) => openAdv(x, "aws-ops"), "args", hasField("args"));
  await refuse((x) => openAdv(x, "aws-ops"), "options.skip_permissions", hasField("options.skip_permissions"));
  await refuse((x) => openAdv(x, "aws-ops"), "env", hasRows);
  await refuse((x) => openAdv(x, "aws-ops"), "secrets[0]", hasRows);
  await refuse((x) => openAdv(x, "runbook-host"), "base", hasField("base"));
  // The refusal goes when its field changes.
  typeArgs(t, "--y");
  assert.deepStrictEqual(advErrs(t), []);
});

test("Advanced: a refused save in another block opens Advanced as a repair form with the refusal under its field", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": args refused', field: "args" });
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), false);
  assert.ok(advOf(t).className.includes("pfv-editing"), "Advanced opened to show it");
  assert.strictEqual(advErrs(t).length, 1);
  assert.ok(field(t, "args"));
});

test("every top-level name in pfv-advanced.js is unique across the page scripts", () => {
  const names = (src) => new Set([...src.matchAll(/^(?:async\s+)?(?:function\s+|const\s+|let\s+|var\s+)([A-Za-z_$][\w$]*)/gm)].map((m) => m[1]));
  const mine = names(fs.readFileSync(path.join(STATIC, "pfv-advanced.js"), "utf8"));
  assert.ok(mine.size > 8);
  assert.ok([...mine].every((n) => /^(pfa|PFA_)/.test(n)), "every name is pfa/PFA_ prefixed: " + [...mine].join(", "));
  const clashes = [];
  for (const f of fs.readdirSync(STATIC).filter((x) => x.endsWith(".js") && x !== "pfv-advanced.js")) {
    for (const n of names(fs.readFileSync(path.join(STATIC, f), "utf8"))) if (mine.has(n)) clashes.push(n + " (" + f + ")");
  }
  assert.deepStrictEqual(clashes, []);
});

test("Advanced registers its editor through the seam, and index.html loads it after profilepage.js", () => {
  const t = page();
  assert.ok(t.run("PFV_EDITORS").advanced, "advanced registered itself");
  assert.throws(() => t.box.pfvRegisterEditor("advanced", {}), /registered twice: advanced/);
  const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("profilepage.js") > 0 && at("pfv-advanced.js") > at("profilepage.js"));
  assert.ok(at("envrows.js") > 0 && at("envrows.js") < at("pfv-advanced.js") && at("importsmodel.js") < at("pfv-advanced.js"));
});

// --- the agent command and the options it takes (R33) ---------------------------------------------------------------------

// A claude profile with a Claude-only option (the example config has exactly this), one every command takes, and one no definition names.
const CLAUDE_OPTS = { model: "claude-opus-5-5", effort: "high", skip_permissions: true, all_bins: true, legacy_key: "keep me" };
const ALL_BINS_DEFS = OPTION_DEFS.concat([{ key: "all_bins", label: "Everywhere", type: "bool" }]);
const pickBin = (t, bin) => { const s = field(t, "bin"); s.value = bin; s.onchange(); };

test("changing the agent command drops options that do not apply to the new command and keeps undefined keys", async () => {
  const t = page({ optionDefs: ALL_BINS_DEFS, repliesEnabled: true });
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { options: Object.assign({}, CLAUDE_OPTS), replyable: true });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickBin(t, "bash");
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), true);
  assert.strictEqual(t.box.puts.length, 1);
  const body = t.box.puts[0];
  assert.strictEqual(body.bin, "bash");
  assert.ok(!("skip_permissions" in (body.options || {})), "the Claude-only option is not sent: the server would refuse the save");
  assert.deepStrictEqual(body.options, { all_bins: true, legacy_key: "keep me" }, "an option every command takes stays, and so does a key no definition names");
  assert.strictEqual(body.replyable, true, "replies are Advanced's and not this block's to drop");
  // What Advanced offers now is what bash takes: no Skip permissions, no replies row, and it still saves.
  clickBtn(advOf(t), "Edit");
  assert.strictEqual(field(t, "options.skip_permissions"), undefined);
  assert.ok(field(t, "options.all_bins"));
  assert.strictEqual(field(t, "replyable"), undefined);
});

test("the same command, or another name for the same program, keeps every option; a command that takes it keeps it", async () => {
  const same = page({ optionDefs: ALL_BINS_DEFS });
  same.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { options: Object.assign({}, CLAUDE_OPTS) });
  await same.box.pfvShow("aws-ops");
  clickBtn(blockOf(same, "agent"), "Edit");
  pickModel(same, "claude-sonnet-5");
  await clickBtn(blockOf(same, "agent"), "Save");
  assert.deepStrictEqual(same.box.puts[0].options, Object.assign({}, CLAUDE_OPTS, { model: "claude-sonnet-5" }), "no command change, nothing dropped");
  // A path to claude is still claude (optApplies matches the program's name).
  const path = page({ optionDefs: ALL_BINS_DEFS });
  path.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { options: Object.assign({}, CLAUDE_OPTS) });
  await path.box.pfvShow("aws-ops");
  clickBtn(blockOf(path, "agent"), "Edit");
  pickBin(path, "__other__");
  const txt = field(path, "bin-text");
  txt.value = "/usr/local/bin/claude"; txt.oninput(); txt.onchange();
  await clickBtn(blockOf(path, "agent"), "Save");
  assert.strictEqual(path.box.puts[0].bin, "/usr/local/bin/claude");
  assert.strictEqual(path.box.puts[0].options.skip_permissions, true);
});

test("Advanced follows the command the open Agent block will save: its options go from the form, and the one save drops them", async () => {
  const t = page({ optionDefs: ALL_BINS_DEFS, repliesEnabled: true });
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { options: Object.assign({}, CLAUDE_OPTS), replyable: true });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickBin(t, "bash");
  // The save is refused for something Advanced owns, so Advanced opens beside the Agent block with the command still pending.
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": args refused', field: "args" });
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), false);
  assert.ok(advOf(t).className.includes("pfv-editing"));
  assert.strictEqual(field(t, "options.skip_permissions"), undefined, "bash does not take it, so it is not offered");
  assert.ok(field(t, "options.all_bins"));
  assert.strictEqual(field(t, "replyable"), undefined, "nor replies");
  t.box.putFail = null;
  typeArgs(t, "--x");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  const body = t.box.puts[t.box.puts.length - 1];
  assert.strictEqual(body.bin, "bash");
  assert.deepStrictEqual(body.options, { all_bins: true, legacy_key: "keep me" });
  assert.deepStrictEqual(body.args, ["--x"]);
  assert.strictEqual(body.replyable, true, "the stored default stays");
});

// --- a row that was added and left blank is no change (R34) ----------------------------------------------------------------------

const leaveAsk = (t) => t.calls.filter((c) => c.startsWith("confirm:")).length;

test("a blank added env row does not make the block dirty", async () => {
  const t = page();
  await openAdv(t, "aws-ops", { env: { KEEP: "1" } });
  clickBtn(advOf(t), "+ Add variable");
  clickBtn(advOf(t), "+ Add secret");
  assert.strictEqual(envRowsOf(t).length, 3, "the rows are there to type in");
  assert.ok(!unsaved(t), "no Unsaved changes in the header");
  assert.deepStrictEqual(texts(advOf(t), "pfv-st-dirty"), [], "nor in the footer");
  assert.strictEqual(byText(advOf(t), "button", "Cancel").length, 1);
  assert.strictEqual(t.run("pfvState").pending, false);
  // Leaving asks nothing and sends nothing.
  assert.strictEqual(await t.box.pfvLeaveGuard("leave"), true);
  assert.strictEqual(leaveAsk(t), 0);
  assert.strictEqual(t.box.puts.length, 0);
  // Typing in one is a change; emptying it again is not; a value with no name is a change (it is a mistake the check will say).
  typeRow(envRowsOf(t)[1], "NEW");
  assert.ok(unsaved(t));
  typeRow(envRowsOf(t)[1], "");
  assert.ok(!unsaved(t));
  typeRow(envRowsOf(t)[1], "", "v");
  assert.ok(unsaved(t));
  typeRow(envRowsOf(t)[1], "", "");
  assert.ok(!unsaved(t));
  // A blank row that was removed again is nothing either.
  rowRm(envRowsOf(t)[2]).click();
  assert.ok(!unsaved(t));
  assert.strictEqual(envRowsOf(t).length, 2);
});

test("a blank added folder or domain row does not make the block dirty", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  optBtn(t, "reachable", "Folder").click();
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  optBtn(t, "reachable", "Domain").click();
  assert.ok(field(t, "folder:1") && field(t, "domain:2"), "both rows are there to type in (after the one folder and two domains it holds)");
  assert.ok(unsavedNote(t).hidden, "no Unsaved changes");
  assert.deepStrictEqual(texts(blockOf(t, "reachable"), "pfv-st-dirty"), []);
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.strictEqual(await t.box.pfvLeaveGuard("leave"), true);
  assert.strictEqual(leaveAsk(t), 0);
  assert.strictEqual(t.box.puts.length, 0);
  // Typing is a change, emptying it is not.
  const f = field(t, "folder:1");
  f.value = "/home/you/x"; f.oninput();
  assert.strictEqual(unsavedNote(t).hidden, false);
  f.value = ""; f.oninput();
  assert.ok(unsavedNote(t).hidden);
  const d = field(t, "domain:2");
  d.value = "api.example.net"; d.oninput();
  assert.strictEqual(unsavedNote(t).hidden, false);
  d.value = "  "; d.oninput();
  assert.ok(unsavedNote(t).hidden, "blank is blank");
  // A GitHub owner row with no owner and no repos is just as empty (a save drops it), even though it already signs in with something.
  const g = cloudPage();
  await g.box.pfvShow("aws-ops");
  clickBtn(blockOf(g, "cloud"), "+ Add access");
  optBtn(g, "cloud", "Another org or user").click();
  assert.ok(field(g, "gh:1:owner"), "the owner row is there");
  assert.ok(unsavedNote(g).hidden);
  const owner = field(g, "gh:1:owner");
  owner.value = "culiops"; owner.oninput();
  assert.strictEqual(unsavedNote(g).hidden, false);
});

// --- the host switch, the unsaved guard and the Esc layers (spec 5.5) -----------------------------------------------------

// A confirm that records what it was asked and answers `ans` (a value, or a function of what was asked).
function recordAsks(t, ans) {
  t.box.asked = [];
  t.box.askConfirm = async (o) => { t.box.asked.push(o); return typeof ans === "function" ? ans(o) : ans; };
  return t.box.asked;
}
const toHost = (t) => { const h = field(t, "tier-host"); h.checked = true; h.onchange(); };
const extraRows = (o) => {
  const dl = o.extra.find((n) => n.className === "ask-list");
  const kids = dl.children;
  return kids.filter((_, i) => i % 2 === 0).map((dt, i) => [dt.textContent, kids[i * 2 + 1].textContent]);
};
const nav = (t, name) => byCls(t.els["pf-navlist"], "pfv-ni").find((n) => n.dataset.pfvName === name);
const dirtyAgent = async (t, name) => {
  await t.box.pfvShow(name || "aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
};

test("switching to host lists what will be removed and saves without it", async () => {
  const t = cloudPage();
  const asked = recordAsks(t, true);
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  toHost(t);
  // Before Save: the cost is on the form and on the button, which is a way into losing the container.
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-consq"), [
    "A host profile cannot hold box-only access. Saving removes 2 AWS connections, GitHub, 2 tokens, 1 readable folder and 2 domains from aws-ops, and its sessions run with your whole home, your SSH keys and the Docker socket.",
  ]);
  assert.strictEqual(byText(blockOf(t, "runs"), "button", "Save as host…").length, 1);
  assert.ok(byText(blockOf(t, "runs"), "button", "Save as host…")[0].className.includes("pfv-btn-host"));
  assert.strictEqual(t.box.puts.length, 0, "nothing is sent by choosing it");

  await clickBtn(blockOf(t, "runs"), "Save as host…");
  assert.strictEqual(asked.length, 1);
  const o = asked[0];
  // Today's C3 words and button, in red, with Cancel focused; the closing question moved under the list.
  assert.strictEqual(o.title, "Save as host");
  assert.strictEqual(o.confirmLabel, "Save as host");
  assert.strictEqual(o.danger, true);
  assert.strictEqual(o.focusCancel, true);
  assert.strictEqual(o.body, 'Sessions from "aws-ops" will run with no container, as your user, with everything you can reach: your whole home directory, your SSH keys, your Docker socket.');
  assert.deepStrictEqual(Array.from(o.extra, (n) => n.className), ["ask-note", "ask-list", "ask-q"]);
  assert.strictEqual(o.extra[0].textContent, "A host profile cannot hold these, so saving also removes them from aws-ops:");
  assert.strictEqual(o.extra[2].textContent, "Save it that way?");
  assert.deepStrictEqual(extraRows(o), [
    ["AWS connections", "prod-read, staging-read"], ["GitHub", "culiops"], ["Tokens", "grafana-prod, sentry-staging"],
    ["Readable folder", "/home/you/other-repo/docs"], ["Domains", "api.example.com, *.example.org"],
  ]);
  // Folders and domains are machine text; the other names are words.
  const dds = o.extra[1].children.filter((n) => n.tag === "dd");
  assert.deepStrictEqual(dds.map((d) => byCls(d, "ask-mono").length), [0, 0, 0, 1, 2]);

  // Saved without them: host, explicit, and every box-only key gone; the rest of the profile is as it was.
  assert.strictEqual(t.box.puts.length, 1);
  const put = t.box.puts[0];
  assert.strictEqual(put.runtime, "host");
  for (const k of ["aws_connections", "aws", "github", "tokens", "context_paths", "egress_allow", "egress_open"]) assert.ok(!(k in put), k + " is not sent");
  assert.strictEqual(put.default_cwd, "/home/you/work/payments-api");
  assert.strictEqual(put.rev, "r1");
  assert.strictEqual(t.run("pfvState").openBlock, "", "back to the read view");
});

test("the host confirm names the network setting and the old-style AWS setting, and asks no list when there is nothing to remove", async () => {
  const t = cloudPage({}, { egress_open: true, aws: { source_profile: "s", region: "eu-west-1", label: "old-aws" } });
  const asked = recordAsks(t, false);
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  toHost(t);
  assert.match(texts(blockOf(t, "runs"), "pfv-consq")[0], /Saving removes 2 AWS connections, the old-style AWS setting, GitHub, 2 tokens, 1 readable folder, 2 domains and network settings from aws-ops, and its sessions run/);
  await clickBtn(blockOf(t, "runs"), "Save as host…");
  assert.deepStrictEqual(extraRows(asked[0]).slice(1, 2).concat(extraRows(asked[0]).slice(-1)), [["Old-style AWS", "old-aws"], ["Network settings", "CuliBox's proxy and its allowlist"]]);

  // Infra-terraform holds nothing box-only: the question has no list, and the body keeps its own closing question.
  const p = page();
  const a2 = recordAsks(p, false);
  await p.box.pfvShow("infra-terraform");
  clickBtn(blockOf(p, "runs"), "Edit");
  toHost(p);
  assert.deepStrictEqual(texts(blockOf(p, "runs"), "pfv-consq"), ["Its sessions run with your whole home, your SSH keys and the Docker socket."]);
  await clickBtn(blockOf(p, "runs"), "Save as host…");
  assert.strictEqual(a2[0].extra, undefined);
  assert.match(a2[0].body, /Save it that way\?$/);
});

test("cancelling the host confirm saves nothing", async () => {
  const t = cloudPage();
  const asked = recordAsks(t, false);
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  toHost(t);
  const saved = await clickBtn(blockOf(t, "runs"), "Save as host…");
  assert.strictEqual(saved, false);
  assert.strictEqual(asked.length, 1);
  assert.strictEqual(t.box.puts.length, 0, "no PUT");
  assert.strictEqual(t.run("pfvState").openBlock, "runs", "the form stays");
  assert.strictEqual(field(t, "tier-host").checked, true, "with host still chosen");
  assert.strictEqual(t.run("pfvState").saving, false);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-top-err"), []);
  // A save that stays a box asks nothing.
  clickBtn(blockOf(t, "runs"), "Discard");
  clickBtn(blockOf(t, "network"), "Edit");
  tick(t, "egress_open", true);
  asked.length = 0;
  await saveBlock(t, "network");
  assert.strictEqual(asked.length, 0);
  assert.strictEqual(t.box.puts.length, 1);
});

test("a host profile saved from another block asks nothing, and a box profile saved from another block while Runs in has host chosen does", async () => {
  const h = page();
  const hAsked = recordAsks(h, true);
  await h.box.pfvShow("runbook-host");
  clickBtn(blockOf(h, "agent"), "Edit");
  pickModel(h, "claude-sonnet-5");
  await saveBlock(h, "agent");
  assert.strictEqual(hAsked.length, 0, "it already is host");
  assert.strictEqual(h.box.puts.length, 1);

  const t = cloudPage();
  const asked = recordAsks(t, true);
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  toHost(t);
  // A repair-style second block holds a change too: both footers save the same merged profile.
  const rec = t.box.pfvOpenRepair("agent");
  rec.values.options.model = "claude-sonnet-5";
  t.box.pfvRender();
  await saveBlock(t, "agent");
  assert.strictEqual(asked.length, 1, "the host switch rides along, so it is asked");
  assert.strictEqual(t.box.puts[0].runtime, "host");
  assert.strictEqual(t.box.puts[0].options.model, "claude-sonnet-5");
});

test("a private copy cannot choose host: refused in place, Save hidden, nothing asked or sent", async () => {
  const t = clonePage();
  const asked = recordAsks(t, true);
  await t.box.pfvShow("gitops-clone");
  clickBtn(blockOf(t, "runs"), "Edit");
  toHost(t);
  const sentence = "A private copy needs a box. Set Workspace to 'Work in this folder' first.";
  assert.strictEqual(texts(blockOf(t, "runs"), "pfv-refuse")[0], sentence + " Open Workspace");
  assert.strictEqual(byCls(blockOf(t, "runs"), "pfv-consq").length, 0, "no host consequence line under a refusal");
  const save = byCls(blockOf(t, "runs"), "pfv-btn-primary")[0];
  assert.strictEqual(save.hidden, true, "Save is not offered");
  assert.strictEqual(await t.box.pfvSaveBlock("runs"), false);
  assert.strictEqual(asked.length, 0);
  assert.strictEqual(t.box.puts.length, 0);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-ferr"), [sentence]);
  // Choosing the box again brings Save back, with its own name.
  const box = field(t, "tier-box"); box.checked = true; box.onchange();
  assert.strictEqual(byCls(blockOf(t, "runs"), "pfv-btn-primary")[0].hidden, false);
  assert.strictEqual(byCls(blockOf(t, "runs"), "pfv-refuse").length, 0);
});

test("Switch to box opens Runs in with Box chosen and the line for it; nothing is restored or saved until Save, then + Add is offered", async () => {
  const t = page();
  const asked = recordAsks(t, true);
  t.box.loadProfiles = async () => { t.box.profiles = t.box.profiles.map((p) => (p.name === "runbook-host" ? Object.assign({}, p, { runtime: "box" }) : p)); return true; };
  await t.box.pfvShow("runbook-host");
  assert.strictEqual(byText(t.els["pfv-in"], "button", "+ Add access").length + byText(t.els["pfv-in"], "button", "+ Add folder or domain").length, 0, "a host profile has no + Add");
  byText(t.els["pfv-in"], "button", "Switch to box")[0].click();
  assert.ok(blockOf(t, "runs").className.includes("pfv-editing"));
  assert.strictEqual(field(t, "tier-box").checked, true);
  assert.strictEqual(Node.focused, field(t, "tier-box"), "focus is on the choice that was made");
  assert.strictEqual(t.box.puts.length, 0, "nothing is saved by opening it");
  assert.strictEqual(unsavedNote(t).hidden, false, "it is a pending change");
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-consq"), ["In a box, sessions can write only to ~/runbooks. After saving you can add cloud access, GitHub, tokens, folders and domains here."]);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-mono"), ["~/runbooks"]);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-fnote"), [], "the new-box line is for a box that stays a box");
  assert.strictEqual(byText(blockOf(t, "runs"), "button", "Save").length, 1, "plain Save: a box needs no question");
  // Choosing host again is no change.
  const h = field(t, "tier-host"); h.checked = true; h.onchange();
  assert.strictEqual(unsavedNote(t).hidden, true);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-consq"), []);
  const b = field(t, "tier-box"); b.checked = true; b.onchange();
  await saveBlock(t, "runs");
  assert.strictEqual(asked.length, 0);
  assert.strictEqual(t.box.puts.length, 1);
  assert.strictEqual(t.box.puts[0].runtime, "box");
  assert.ok(!("aws_connections" in t.box.puts[0]) && !("tokens" in t.box.puts[0]), "nothing was restored");
  // Now a box profile with nothing bound: the read view offers what to add.
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-chip"), ["box"]);
  assert.strictEqual(byText(blockOf(t, "cloud"), "button", "+ Add access").length, 1);
  assert.strictEqual(byText(blockOf(t, "reachable"), "button", "+ Add folder or domain").length, 1);
});

test("Switch to box is refused in place while another block holds changes", async () => {
  const t = page();
  await dirtyAgent(t, "runbook-host");
  byText(t.els["pfv-in"], "button", "Switch to box")[0].click();
  assert.ok(!blockOf(t, "runs").className.includes("pfv-editing"));
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-nudge"), ["Save or discard these changes first"]);
});

test("leaving with unsaved changes asks; Discard leaves, Keep editing stays: switching profile", async () => {
  const t = page();
  const asked = recordAsks(t, false);
  t.box.pfvRenderNav();
  await dirtyAgent(t);
  nav(t, "infra-terraform").click();
  await settle();
  assert.strictEqual(asked.length, 1);
  assert.deepStrictEqual([asked[0].title, asked[0].confirmLabel, asked[0].cancelLabel, asked[0].danger], ["Discard unsaved changes?", "Discard", "Keep editing", true]);
  assert.strictEqual(asked[0].focusCancel, true, "Keep editing is focused: Enter must not drop the changes");
  assert.strictEqual(t.run("pfvState").name, "aws-ops", "Keep editing stays");
  assert.strictEqual(t.run("pfvState").pending, true);
  assert.strictEqual(field(t, "options.model").value, "claude-sonnet-5", "what was typed is still there");
  recordAsks(t, true);
  nav(t, "infra-terraform").click();
  await settle();
  assert.strictEqual(t.run("pfvState").name, "infra-terraform");
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
  assert.deepStrictEqual(texts(t.els["pfv-in"], "pfv-title-name"), ["infra-terraform"]);
});

test("switching profile with nothing changed asks nothing, ends an open block and does not ask for the profile already shown", async () => {
  const t = page();
  const asked = recordAsks(t, false);
  t.box.pfvRenderNav();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  nav(t, "aws-ops").click();
  await settle();
  assert.strictEqual(t.run("pfvState").openBlock, "agent", "the profile on screen: nothing happens");
  nav(t, "runbook-host").click();
  await settle();
  assert.strictEqual(asked.length, 0);
  assert.strictEqual(t.run("pfvState").name, "runbook-host");
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
});

test("leaving with unsaved changes asks; Discard leaves, Keep editing stays: another Settings tab", async () => {
  const t = page();
  const asked = recordAsks(t, false);
  await dirtyAgent(t);
  assert.strictEqual(t.box.pfvTabLeave("manage"), true, "Profiles itself is not leaving");
  const keep = t.box.pfvTabLeave("creds");
  assert.ok(keep && typeof keep.then === "function", "a question is a promise");
  assert.strictEqual(await keep, false);
  assert.strictEqual(asked.length, 1);
  assert.strictEqual(t.run("pfvState").pending, true);
  recordAsks(t, true);
  assert.strictEqual(await t.box.pfvTabLeave("creds"), true);
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.strictEqual(t.box.pfvTabLeave("creds"), true, "nothing left to ask");
});

test("leaving with unsaved changes asks; Discard leaves, Keep editing stays: Esc and closing the page, through pagehost's hook", async () => {
  const t = page();
  const asked = recordAsks(t, false);
  assert.strictEqual(t.box.pageLeaveHook, t.box.pfvPageLeave, "the page registers its hook");
  await dirtyAgent(t);
  for (const why of ["esc", undefined]) {
    assert.strictEqual(await t.box.pageLeaveHook(why), false, "Keep editing: the page stays (" + why + ")");
  }
  assert.strictEqual(asked.length, 2);
  assert.strictEqual(t.run("pfvState").pending, true);
  recordAsks(t, true);
  assert.strictEqual(await t.box.pageLeaveHook("esc"), true);
  assert.strictEqual(t.run("pfvState").pending, false);
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
  // Clean: no question, true at once (not a promise), so a caller that does not wait still sees the page shut.
  const c = page();
  const cAsked = recordAsks(c, false);
  await c.box.pfvShow("aws-ops");
  clickBtn(blockOf(c, "agent"), "Edit");
  assert.strictEqual(c.box.pageLeaveHook(), true);
  assert.strictEqual(cAsked.length, 0);
  assert.deepStrictEqual(Object.keys(c.run("pfvState").edits), [], "an open block with no change ends with the page");
});

test("the profile page's hook and tab question stand down on a phone's modal and for any other page", async () => {
  const t = page();
  const asked = recordAsks(t, false);
  await dirtyAgent(t);
  t.els["settings-modal"].classList.remove("as-page");
  assert.strictEqual(t.box.pageLeaveHook("esc"), true);
  assert.strictEqual(t.box.pfvTabLeave("creds"), true);
  assert.strictEqual(t.box.pfvHold(() => { throw new Error("not held"); }), false);
  assert.strictEqual(asked.length, 0);
  assert.strictEqual(t.run("pfvState").pending, true, "and it drops nothing");
});

test("Discard ends the edit through pfvDropEdits: the folder picker is released", async () => {
  const t = page();
  recordAsks(t, true);
  const picker = await openWs(t);
  picker.pick("/home/you/other-folder");
  assert.strictEqual(t.run("pfvState").pending, true);
  assert.strictEqual(await t.box.pageLeaveHook("esc"), true);
  assert.strictEqual(picker.destroyed, true);
  // A clean leave releases it too.
  const c = page();
  const cp = await openWs(c);
  assert.strictEqual(c.box.pageLeaveHook(), true);
  assert.strictEqual(cp.destroyed, true);
});

test("New profile, Duplicate and the old editor's entry points ask first through pfvHold", async () => {
  const t = page();
  recordAsks(t, false);
  let again = 0;
  assert.strictEqual(t.box.pfvHold(() => again++), false, "nothing changed: not held");
  await dirtyAgent(t);
  assert.strictEqual(t.box.pfvHold(() => again++), true, "held");
  await settle();
  assert.strictEqual(again, 0, "Keep editing: it does not run");
  recordAsks(t, true);
  assert.strictEqual(t.box.pfvHold(() => again++), true);
  await settle();
  assert.strictEqual(again, 1, "Discard: it runs after the edit is gone");
  assert.strictEqual(t.run("pfvState").pending, false);
});

test("pfvEscTarget picks the innermost open layer", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  // The flags are the state: a test, or the code that opens a layer, sets them.
  t.run("pfvState").picker = "cloud";
  assert.strictEqual(t.box.pfvEscTarget(), "picker");
  t.run("pfvState").drawer = true;
  assert.strictEqual(t.box.pfvEscTarget(), "drawer", "the drawer hangs over the page and its pickers");
  t.els["ask-modal"] = { open: true };
  assert.strictEqual(t.box.pfvEscTarget(), "confirm", "a question over everything");
  t.els["ask-modal"].open = false;
  assert.strictEqual(t.box.pfvEscTarget(), "drawer");
  t.run("pfvState").drawer = false;
  t.run("pfvState").picker = "";
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  // An open Workspace folder dropdown is a picker too (it closes itself on Esc; the page must not also act).
  const p = page();
  const picker = await openWs(p);
  assert.strictEqual(p.box.pfvEscTarget(), "page");
  picker.open();
  assert.strictEqual(p.box.pfvEscTarget(), "picker");
  picker.close();
  assert.strictEqual(p.box.pfvEscTarget(), "page");
});

test("Esc with a picker open closes the picker only; the next Esc leaves the page", async () => {
  const t = cloudPage();
  const asked = recordAsks(t, false);
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  assert.strictEqual(t.run("pfvState").picker, "reachable", "the stand-in picker sets the flag");
  assert.strictEqual(byCls(blockOf(t, "reachable"), "pfv-pick").length, 1);
  assert.strictEqual(t.box.pageLeaveHook("esc"), false, "the page stays");
  assert.strictEqual(t.run("pfvState").picker, "");
  assert.strictEqual(byCls(blockOf(t, "reachable"), "pfv-pick").length, 0, "the picker is gone");
  assert.ok(blockOf(t, "reachable").className.includes("pfv-editing"), "the block it opened from is still open");
  assert.strictEqual(Node.focused, field(t, "add"), "focus is back on the link that opened it");
  assert.strictEqual(asked.length, 0);
  assert.strictEqual(t.box.pageLeaveHook("esc"), true, "nothing left above the page");
  // Picking closes it and clears the flag too, and so does Cancel.
  const u = cloudPage();
  await u.box.pfvShow("aws-ops");
  clickBtn(blockOf(u, "reachable"), "+ Add folder or domain");
  optBtn(u, "reachable", "Domain").click();
  assert.strictEqual(u.run("pfvState").picker, "");
  field(u, "domain:2").value = "api.example.net"; field(u, "domain:2").oninput();
  clickBtn(blockOf(u, "cloud"), "+ Add access");
  assert.strictEqual(u.run("pfvState").picker, "", "refused while the block holds a change");
  const w = cloudPage();
  await w.box.pfvShow("aws-ops");
  clickBtn(blockOf(w, "reachable"), "+ Add folder or domain");
  clickBtn(blockOf(w, "reachable"), "Cancel");
  assert.strictEqual(w.run("pfvState").picker, "");
});

test("Esc with the Workspace dropdown open lets the dropdown close and the page stays", async () => {
  const t = page();
  const picker = await openWs(t);
  picker.open();
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  assert.strictEqual(picker.isOpen(), false, "its list is closed");
  assert.strictEqual(t.run("pfvState").openBlock, "workspace");
});

test("Esc with a drawer or a question open never leaves the page", async () => {
  const t = page();
  const asked = recordAsks(t, true);
  await dirtyAgent(t);
  t.run("pfvState").drawer = true;
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  t.run("pfvState").drawer = false;
  t.els["ask-modal"] = { open: true };
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  assert.strictEqual(asked.length, 0);
  assert.strictEqual(t.run("pfvState").pending, true);
});
