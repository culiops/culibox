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
    p.open = () => { p.opened++; };
    p.close = () => {};
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
    window: { HOME_DIR: "/home/you" }, cloneUnavailable: "", createWsPicker: pk.factory, pickers: pk.made,
    profiles: PROFILES.slice(), profilesLoaded: true, selectedProfile: "", metaLoaded: true,
    boxRuntime: "runc", boxUnavailableReason: "", bundleImports: true, optionDefs: OPTION_DEFS,
    accountsCache: [{ name: "work" }], acctChoices: ACCT.acctChoices, ACCT_DEFAULT: ACCT.ACCT_DEFAULT,
    withPending: PENDING.withPending, pfSchema: SCHEMA(), docs: {}, puts: [], revN: 1,
    awscEntries: [{ name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" }, { name: "staging-read", account_id: "111111111111", region: "eu-west-1" }],
    stEntries: [{ name: "grafana-prod", prod: true, env: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"], hosts: [{ host: "grafana.acme.example.com", port: 443 }] }],
    pfCredsView: { store: { initialized: true, locked: false }, aws: [] },
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
  for (const f of ["fsops.js", "workhome.js", "workmode.js", "spawnpreview.js", "spawnform.js", "profileform.js", "profileview.js", "profileedit.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  for (const fn of ["buildSeg", "optApplies"]) new vm.Script(appFn(fn), { filename: "app.js:" + fn }).runInContext(ctx);
  for (const f of ["profilepage.js", "pfv-workspace.js"]) new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
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
  // Runs in, Workspace, Agent and Network edit now; the blocks whose editor is a later task are drawn but inert.
  assert.deepStrictEqual(byText(root, "button", "Edit").map((b) => !!b.onclick), [true, true, true, false, false, true, false]);
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
  const src = ["profilepage.js", "pfv-workspace.js"].map((f) => fs.readFileSync(path.join(STATIC, f), "utf8")).join("\n");
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
  const t = page({ profiles: withFolder() });
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
  const t = page({ profiles: withFolder() });
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
  await clickBtn(blockOf(t, "runs"), "Save");
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

test("a GitHub binding with no permission ticked is refused before the PUT, with the sentence", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  // The Cloud editor is a later task; the pipeline already checks what it will hand over.
  t.run('pfvState.edits.cloud = { id: "cloud", values: { github: { owners: [{ owner: "culiops", app: "x" }], permissions: {} } }, init: "{}", ferrs: [], nodes: null }; pfvState.openBlock = "cloud"');
  assert.strictEqual(await t.box.pfvSaveBlock("cloud"), false);
  assert.strictEqual(t.box.puts.length, 0);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(t.run("pfvState.edits.cloud.ferrs").map((e) => e.text))), ["GitHub: tick at least one permission, or the agent gets none."]);
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
