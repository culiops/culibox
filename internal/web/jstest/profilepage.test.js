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
    // Only what a test reads back: the custom properties a script sets on an element's style.
    this.style = { props: {}, setProperty: (k, v) => { self.style.props[k] = v; } };
    const set = () => new Set(self.className.split(/\s+/).filter(Boolean));
    this.classList = {
      contains: (c) => set().has(c),
      add: (c) => { const s = set(); s.add(c); self.className = [...s].join(" "); },
      remove: (c) => { const s = set(); s.delete(c); self.className = [...s].join(" "); },
      toggle: (c, on) => { const s = set(); if (on === undefined ? !s.has(c) : on) s.add(c); else s.delete(c); self.className = [...s].join(" "); },
    };
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  // A layout model for one thing: the "+ Add" picker's list is 50px tall per option that is shown (neither it nor its group hidden).
  get offsetHeight() {
    if (!this.className.split(/\s+/).includes("pfv-pick-list")) return 0;
    return 50 * this.all((n) => n.tag === "button" && !n.hidden && !(n.parent && n.parent.hidden)).length;
  }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "class") this.className = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  appendChild(c) { if (typeof c === "string") c = new Text(c); if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = ""; cs.forEach((c) => this.appendChild(c)); }
  addEventListener(ev, fn) { (this.handlers[ev] = this.handlers[ev] || []).push(fn); }
  contains(n) { for (let x = n; x; x = x.parent) if (x === this) return true; return false; }
  // As a browser: only a control takes focus (a div, a fieldset or a paragraph does not), so a test cannot pass on a focus that never landed.
  focus() {
    if (!["input", "select", "textarea", "button"].includes(this.tag) && !("tabindex" in this.attrs)) return;
    if (this.disabled) return;
    Node.focused = this;
    for (const f of (this.handlers.focus || []).slice()) f({ target: this });
  }
  click() { if (this.onclick) return this.onclick({ target: this }); }
  all(pred, out = []) { for (const c of this.children) { if (!(c instanceof Node)) continue; if (pred(c)) out.push(c); c.all(pred, out); } return out; }
}
const hasCls = (cls) => (n) => n.className.split(/\s+/).includes(cls);
const byCls = (root, cls) => root.all(hasCls(cls));
const texts = (root, cls) => byCls(root, cls).map((n) => n.textContent);
const byText = (root, tag, text) => root.all((n) => n.tag === tag && n.textContent.trim() === text);
// The page head's title and its one line: every profile-page head (the overview, New profile, Duplicate) is the kit's h1.page-title and p.page-desc.
const titles = (root) => texts(root, "page-title");
const subs = (root) => texts(root, "page-desc");
const classesOf = (n) => n.className.split(/\s+/).filter(Boolean);
// A block's label as words: Advanced's label also holds its count under the name (span.pfv-advst), which is not part of the name.
const labelName = (l) => l.own;
const labelNames = (root) => byCls(root, "pfv-label").map(labelName);

const view = (name, over) => Object.assign({ name, editable: true, source: "runtime", runtime: "box", bin: "claude", claude: true }, over);
const PROFILES = [
  view("claude", { editable: false, source: "config", runtime: "host" }),
  view("aws-ops", { default_cwd: "/home/you/work/payments-api", aws_connections: ["prod-read", "staging-read"], tokens: ["grafana-prod"], network: "allowlist", host_block: "this profile has an AWS binding, so it runs only in a box" }),
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
    getElementById: (id) => els[id] || null,
    // What the page listens for on the document (a click anywhere else closes the header menu and the "+ Add" picker).
    listeners: {}, addEventListener(ev, fn) { (doc.listeners[ev] = doc.listeners[ev] || []).push(fn); },
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
    withPending: PENDING.withPending, pfSchema: SCHEMA(), docs: {}, puts: [], posts: [], asked: [], revN: 1,
    awscEntries: [{ name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" }, { name: "staging-read", account_id: "111111111111", region: "eu-west-1" }],
    stEntries: [{ name: "grafana-prod", prod: true, env: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"], hosts: [{ host: "grafana.acme.example.com", port: 443 }] }],
    pfCredsView: { store: { initialized: true, locked: false }, aws: [] },
    ghApps: ["acme-app"], refreshCredLists: async () => { calls.push("refreshCredLists"); },
    // tabs.js draws the dock's glyphs (the ⋯ menu's Duplicate… and Delete… icons); a bare svg stands in here.
    tabIcon: () => new Node("svg"), dockIcon: () => new Node("svg"),
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
      if (!m && p === "/api/profiles" && method === "POST") {
        const body = JSON.parse(opt.body);
        box.posts.push(body);
        if (box.postFail) throw box.postFail;
        // What the server keeps and lists: the profile with a revision, and a row of the list for it (a runtime profile is editable).
        const kept = Object.assign({}, body, { rev: "r" + (++box.revN) + "0" });
        delete kept.schema;
        box.docs[body.name] = kept;
        const row = box.rowFor ? box.rowFor(kept) : view(body.name, { runtime: kept.runtime || "box", default_cwd: kept.default_cwd, clone_of: kept.clone_of });
        if (!box.rowFor) {
          for (const k of ["aws_connections", "tokens", "context_paths", "egress_allow"]) if (kept[k]) row[k] = kept[k];
          if (kept.aws_connections || kept.tokens) row.network = "allowlist";
        }
        box.profiles = box.profiles.concat([row]);
        return Object.assign({ name: body.name, rev: kept.rev }, box.postReply);
      }
      if (m && method === "PUT") {
        const body = JSON.parse(opt.body);
        box.puts.push(body);
        if (box.putFail) throw box.putFail;
        // What the server keeps: the profile with a new revision, and no schema key.
        const kept = Object.assign({}, body, { rev: "r" + (++box.revN) + "0" });
        delete kept.schema;
        box.docs[name] = kept;
        return Object.assign({ rev: kept.rev }, box.putReply);
      }
      return null;
    },
    openSettings: (tab) => { calls.push("openSettings:" + tab); },
    loadProfiles: async () => { calls.push("loadProfiles"); box.profiles = box.profiles.filter((p) => !box.gone || p.name !== box.gone); return true; },
    askConfirm: async (o) => { calls.push("confirm:" + o.title + ":" + (o.danger ? "danger" : "")); box.asked.push(o); return box.confirmAnswer !== false; },
    openSpawnDialog: (n) => { calls.push("spawn:" + n); },
    setDialogError: (id, m) => { calls.push("error:" + m); },
    // settingshead.js settingsEscButton closes the page through it (shell.js).
    closeDialogFromUI: (id) => { calls.push("close:" + id); },
  }, over);
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["svgicon.js", "kitmenu.js", "fsops.js", "workhome.js", "workmode.js", "spawnpreview.js", "spawnform.js", "profileform.js", "importsmodel.js", "envrows.js", "profileview.js", "profileedit.js", "settingshead.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  for (const fn of ["buildSeg", "optApplies"]) new vm.Script(appFn(fn), { filename: "app.js:" + fn }).runInContext(ctx);
  for (const f of ["profilepage.js", "pfv-workspace.js", "pfv-cloud.js", "pfv-advanced.js", "pfv-new.js"]) new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  return { box, els, calls, doc, run: (code) => new vm.Script(code).runInContext(ctx) };
}

test("pfvRenderNav lists every profile with a read-only marker for config.json and box only where Host is blocked", () => {
  const t = page();
  t.box.pfvRenderNav();
  const items = byCls(t.els["pf-navlist"], "pfv-ni").filter((n) => !hasCls("pfv-ni-add")(n));
  assert.deepStrictEqual(items.map((n) => byCls(n, "pfv-ni-name")[0].textContent), ["claude", "aws-ops", "infra-terraform", "runbook-host"]);
  assert.deepStrictEqual(items.map((n) => texts(n, "pfv-ni-boxonly")), [[], ["box only"], [], []]);
  assert.strictEqual(byCls(t.els["pf-navlist"], "pfv-ni-tier").length, 0, "the tier chip is gone");
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

// The M2 nav row: the name, then the lock, then `box only` when the server says Host is blocked for the profile. No tier chip:
// the tier is not a fixed property of a profile. The lock and its screen-reader words are the row's own children, before the chip.
test("a nav row is name, lock, then box only when Host is blocked", () => {
  const t = page();
  t.box.pfvRenderNav();
  const items = byCls(t.els["pf-navlist"], "pfv-ni").filter((n) => !hasCls("pfv-ni-add")(n));
  const order = (n) => n.children.filter((c) => c instanceof Node).map((c) => c.className.split(/\s+/)[0] + (c.tag === "svg" ? "(svg)" : ""));
  assert.deepStrictEqual(order(items[0]), ["pfv-ni-name", "pfv-lock(svg)", "pfv-sr"], "claude: read-only");
  assert.deepStrictEqual(order(items[1]), ["pfv-ni-name", "pfv-ni-boxonly"], "aws-ops: editable, Host blocked");
  assert.deepStrictEqual(order(items[3]), ["pfv-ni-name"], "runbook-host: a host profile with no block");
  assert.strictEqual(byCls(items[1], "pfv-ni-boxonly")[0].children.length, 0, "nothing but the words inside a chip");
  assert.strictEqual(byCls(t.els["pf-navlist"], "pfv-ni-host").length, 0);
});

// A refetch hands back new objects. The nav is rebuilt only when what it shows changed: a new host_block sentence does, a runtime flip alone does not.
test("the nav rebuilds when host_block changes, not when only runtime does", () => {
  const t = page();
  t.box.pfvRenderNav();
  const kept = t.els["pf-navlist"].children[2];
  t.box.profiles = t.box.profiles.map((p) => (p.name === "infra-terraform" ? Object.assign({}, p, { runtime: "host" }) : p));
  t.box.pfvRenderNav();
  assert.strictEqual(t.els["pf-navlist"].children[2], kept, "only runtime changed: the same node");
  t.box.profiles = t.box.profiles.map((p) => (p.name === "infra-terraform" ? Object.assign({}, p, { host_block: "this profile is set to box only" }) : p));
  t.box.pfvRenderNav();
  const now = t.els["pf-navlist"].children[2];
  assert.notStrictEqual(now, kept, "host_block changed: a new node");
  assert.deepStrictEqual(texts(now, "pfv-ni-boxonly"), ["box only"]);
});

// N2 (diff M2.1#9): the lock is the frame's glyph, 10x12 (it was 10x11). Only the desktop page draws it: pfvRenderNav and pfvDrawNote run behind
// pfvOnPage(), and the phone's own list and its note have no lock, so the phone cannot change with it.
test("the read-only lock is the frame's 10x12 glyph: viewBox 0 0 10 12, rect y 5.5 h 5.5 rx 1, stroke 1, the shackle M3 5.5V3.75a2 2 0 0 1 4 0V5.5", () => {
  const t = page();
  t.box.pfvRenderNav();
  const lock = byCls(t.els["pf-navlist"], "pfv-lock")[0];
  assert.strictEqual(lock.tag, "svg");
  assert.deepStrictEqual(["width", "height", "viewBox", "aria-hidden"].map((k) => lock.getAttribute(k)), ["10", "12", "0 0 10 12", "true"]);
  const [rect, shackle] = lock.children;
  assert.deepStrictEqual([rect.tag, rect.getAttribute("x"), rect.getAttribute("y"), rect.getAttribute("width"), rect.getAttribute("height"), rect.getAttribute("rx")], ["rect", "1.5", "5.5", "7", "5.5", "1"]);
  assert.deepStrictEqual([shackle.tag, shackle.getAttribute("d")], ["path", "M3 5.5V3.75a2 2 0 0 1 4 0V5.5"]);
  for (const n of [rect, shackle]) assert.deepStrictEqual(["fill", "stroke", "stroke-width"].map((k) => n.getAttribute(k)), ["none", "currentColor", "1"]);
  assert.strictEqual(lock.children.length, 2);
  assert.ok(!/pfv-lock/.test(fs.readFileSync(path.join(STATIC, "app.css"), "utf8")), "the phone's stylesheet never styles it");
  for (const f of ["app.js", "mobileterm.js", "shell.js"]) assert.ok(!/pfvLockIcon/.test(fs.readFileSync(path.join(STATIC, f), "utf8")), f + " (phone code) does not draw it");
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
  assert.deepStrictEqual(titles(t.els["pfv-in"]), ["runbook-host"]);
});

// P4 / G3-D6 (frame M2.1, digest-design §A2): an empty Cloud and tokens block reads "Nothing yet" as its value (13/20 500 --fg, the .pfv-main every value
// is) with the explanation as the muted 12/18 line under it (.pfv-sub); it was one muted sentence. The block in edit (no frame draws it) keeps today's
// one sentence. The words are the read model's (profileview.js), which only the desktop page and its copy list read: the phone never loads them.
test("an empty Cloud and tokens block: 'Nothing yet' is its value and the explanation its helper line, two lines and no sentence of its own", async () => {
  const t = page();
  await t.box.pfvShow("infra-terraform");
  const cloud = blockOf(t, "cloud");
  assert.strictEqual(byCls(cloud, "pfv-row").length, 0, "the profile binds nothing");
  assert.deepStrictEqual(texts(cloud, "pfv-main"), ["Nothing yet"]);
  assert.deepStrictEqual(texts(cloud, "pfv-sub"), ["It cannot use AWS, GitHub or any token."]);
  assert.strictEqual(byCls(cloud, "pfv-empty").length, 0, "the old one-sentence line is not drawn");
  const body = byCls(cloud, "pfv-body")[0];
  assert.deepStrictEqual(body.children.filter((n) => n instanceof Node).map((n) => n.className.split(/\s+/).slice(0, 2).join(" ")), ["pfv-main", "pfv-sub", "pfv-link pfv-add"], "value, helper, then + Add access");
  // Block in edit: today's sentence, whole.
  clickBtn(cloud, "Edit");
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-empty"), ["Nothing yet. It cannot use AWS, GitHub or any token."]);
});

test("an empty Also reachable block says 'Nothing yet' as its value, with its own explanation under it", async () => {
  const t = page({ bundleImports: false });
  await t.box.pfvShow("infra-terraform");
  const reach = blockOf(t, "reachable");
  assert.strictEqual(byCls(reach, "pfv-row").length, 0);
  assert.deepStrictEqual(texts(reach, "pfv-main"), ["Nothing yet"]);
  assert.deepStrictEqual(texts(reach, "pfv-sub"), ["It cannot read other folders or reach other domains."]);
  assert.strictEqual(byCls(reach, "pfv-empty").length, 0);
});

test("pfvShow fetches the stored profile and draws the eight blocks from the read model", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  assert.ok(t.calls.includes("GET /api/profiles/aws-ops"));
  assert.strictEqual(t.run("pfvState").name, "aws-ops");
  assert.strictEqual(t.run("pfvState").doc.rev, "r1");
  const root = t.els["pfv-in"];
  assert.deepStrictEqual(titles(root), ["aws-ops"]);
  assert.deepStrictEqual(texts(root, "chip"), ["box"]);
  assert.deepStrictEqual(subs(root), ["Claude Code · claude-opus-5-5 · high effort"]);
  assert.deepStrictEqual(texts(root, "pfv-sheet-h"), ["This agent will be able to"]);
  assert.deepStrictEqual(labelNames(root), ["Runs in", "Workspace", "Agent", "Cloud and tokens", "Also reachable", "Network", "Can reach", "Advanced"]);
  // Edit on every block of an editable profile but Can reach (its action is Switch to box, offered on a host profile only), "+ Add" on the two row blocks.
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

// pfvMonoNodes marks machine text. Its pattern once used lookbehind, which Safari before 16.4 cannot parse (no-lookbehind.test.js); it
// now says the same without it. REF is that former pattern, word for word (node has lookbehind, so it can still be the oracle), and the
// two are compared on the page's own wording and on a deterministic spread of strings built from the pieces that matter to it.
const PFV_MACHINE_REF = [
  "(?<![\\w.])~?(?:\\/[\\w.@+-]+)+\\/?",
  "(?<![\\w.@/-])(?:[a-z0-9*][a-z0-9-]*\\.)+[a-z]{2,}(?![\\w-])",
  "(?<!\\d)\\d{12}(?!\\d)",
  "\\b[a-z]{2}(?:-[a-z]+)+-\\d\\b",
  "\\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\\b",
  "\\bbox\\/<branch>",
  "(?<=^Shares ).+?(?= from the original)",
  "(?<=^repo ).+$",
].join("|");
function monoRef(text, known) {
  const s = String(text == null ? "" : text);
  const esc = (k) => k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const own = (Array.isArray(known) ? known : []).filter((k) => typeof k === "string" && k !== "").map(esc);
  const re = new RegExp((own.length ? "(?:" + own.join("|") + ")|" : "") + PFV_MACHINE_REF, "g");
  const out = [];
  let at = 0;
  for (const m of s.matchAll(re)) {
    if (m[0] === "") continue;
    if (m.index > at) out.push(["text", s.slice(at, m.index)]);
    out.push(["mono", m[0]]);
    at = m.index + m[0].length;
  }
  if (at < s.length) out.push(["text", s.slice(at)]);
  return out;
}

test("pfvMonoNodes marks the same machine text as the lookbehind pattern it replaced", () => {
  const t = page();
  const got = (text, known) => Array.from(t.box.pfvMonoNodes(text, known)).map((n) => (typeof n === "string" ? ["text", n] : ["mono", n.textContent]));
  const wording = [
    "Claude Code · claude-opus-5-5 · high effort", "~/work/payments-api read/write · nothing else on this machine", "/home/you/other-repo/docs",
    "account 111111111111, eu-west-1", "api.example.com", "*.example.org", "GRAFANA_SERVICE_ACCOUNT_TOKEN", "box/<branch>", "Shares README.md, docs/a.md from the original",
    "repo payments-api, runbooks", "repo web", "Not found in Credentials", "Read only: contents, pull requests. 1-hour tokens.", "see /a/b.c, then ~/d and e.f.g.com",
    "/a//b", "x/a-/b", "foo.bar/baz.qux.com", "https://a.example.com/c", "a@b.example.com", "1234567890123", "x123456789012y", "Shares  from the original", "repo ",
    "", "plain words only",
  ];
  for (const s of wording) for (const known of [undefined, [], ["claude-opus-5-5"], ["x.y", "a(b)"]]) assert.deepStrictEqual(got(s, known), monoRef(s, known), JSON.stringify(s));
  const pieces = ["/", "~", ".", "-", "@", "+", "a", "b", "z", "1", "0", " ", "_", "A", "Z", "*", "repo ", "Shares ", " from the original", "box/<branch>", "eu-west-1",
    "123456789012", "example.com", "x.y", "/home", "claude-opus-5-5", "\n"];
  let seed = 12345;
  const rnd = (n) => { seed = (seed * 1103515245 + 12345) % 2147483648; return Math.floor((seed / 2147483648) * n); };
  for (let i = 0; i < 20000; i++) {
    let s = "";
    for (let k = rnd(9); k >= 0; k--) s += pieces[rnd(pieces.length)];
    const known = rnd(4) === 0 ? ["claude-opus-5-5", "x.y"] : undefined;
    assert.deepStrictEqual(got(s, known), monoRef(s, known), JSON.stringify([s, known]));
  }
});

test("a GitHub App deleted from Credentials reads Not found in Credentials on the profile that binds it, with Remove in edit mode", async () => {
  const ghView = view("gh-ops", { default_cwd: "/home/you/work/web", github_owners: ["acme"], github_repos: [{ owner: "acme", repos: ["web"] }], github_permissions: { contents: "read" }, network: "allowlist" });
  const ghDoc = { name: "gh-ops", bin: "claude", runtime: "box", default_cwd: "/home/you/work/web", github: { owners: [{ owner: "acme", app: "acme-bot", repos: ["web"] }], permissions: { contents: "read" } }, rev: "r9" };
  const mk = (over) => page(Object.assign({ profiles: PROFILES.concat([ghView]), docs: { "gh-ops": ghDoc } }, over));

  // ghApps (the /api/meta list the page holds) no longer has acme-bot.
  const t = mk({ ghApps: ["acme-app"] });
  await t.box.pfvShow("gh-ops");
  const miss = byCls(t.els["pfv-in"], "pfv-miss");
  assert.deepStrictEqual(miss.map((n) => n.textContent), ["Not found in Credentials"]);
  const row = miss[0].parent.parent;
  assert.match(row.textContent, /GitHub\s*acme/);
  assert.match(miss[0].parent.textContent, /^Not found in Credentials · Remove$/);

  // Still defined: no mark. Before /api/meta has answered nothing is known, so no mark either.
  const ok = mk({ ghApps: ["acme-bot"] });
  await ok.box.pfvShow("gh-ops");
  assert.strictEqual(byCls(ok.els["pfv-in"], "pfv-miss").length, 0);
  const early = mk({ ghApps: [], metaLoaded: false });
  await early.box.pfvShow("gh-ops");
  assert.strictEqual(byCls(early.els["pfv-in"], "pfv-miss").length, 0);

  // A token owner: a deleted GitHub token reads the same.
  const tokDoc = { ...ghDoc, github: { owners: [{ owner: "acme", token: "gh-pat", repos: ["web"] }] } };
  const tk = mk({ docs: { "gh-ops": tokDoc }, ghApps: ["acme-bot"] });
  await tk.box.pfvShow("gh-ops");
  assert.deepStrictEqual(byCls(tk.els["pfv-in"], "pfv-miss").map((n) => n.textContent), ["Not found in Credentials"]);

  // Edit mode: the row keeps its place, can be removed, and says so until the owner is signed in with a login that exists.
  const ed = mk({ ghApps: ["acme-app"] });
  await ed.box.pfvShow("gh-ops");
  byText(ed.els["pfv-in"], "button", "Edit").find((b) => b.getAttribute("aria-label") === "Edit cloud and tokens").click();
  const inEdit = byCls(ed.els["pfv-in"], "pfv-miss");
  assert.deepStrictEqual(inEdit.map((n) => n.textContent), ["Not found in Credentials"]);
  assert.strictEqual(byText(inEdit[0].parent.parent, "button", "Remove").length, 1);
  const sel = ed.els["pfv-in"].all((n) => n.tag === "select" && n.children.some((o) => o.value === "app:acme-bot"))[0];
  assert.ok(sel, "the login select is drawn, and keeps the stored login selectable");
  assert.strictEqual(sel.value, "app:acme-bot");
  sel.value = "app:acme-app";
  sel.onchange({ target: sel });
  assert.strictEqual(byCls(ed.els["pfv-in"], "pfv-miss").length, 0, "re-pointed to an App that exists, the row stops saying it is gone");
});

test("a host profile has every block, Switch to box, and the box blocks tagged Applies to Box", async () => {
  const t = page();
  await t.box.pfvShow("runbook-host");
  const root = t.els["pfv-in"];
  assert.deepStrictEqual(texts(root, "chip"), ["host"]);
  assert.ok(byCls(root, "chip")[0].className.includes("chip-host"));
  assert.deepStrictEqual(labelNames(root), ["Runs in", "Workspace", "Agent", "Cloud and tokens", "Also reachable", "Network", "Can reach", "Advanced"]);
  assert.deepStrictEqual(byCls(root, "pfv-label").map((l) => texts(l, "pfv-applies")), [[], [], [], ["Applies to Box"], ["Applies to Box"], ["Applies to Box"], ["Applies to Host"], []]);
  // Spec 5.4 sentence, one line.
  assert.deepStrictEqual(texts(byCls(root, "pfv-block")[0], "pfv-main"), ["On this machine: runs as you, with your files, your network and your credentials"]);
  assert.strictEqual(byText(root, "button", "Switch to box").length, 1);
  assert.strictEqual(byCls(root, "pfv-add").length, 2, "Cloud and Also reachable offer their + Add on any tier");
});

test("a read-only profile has no Edit, the config.json note and no menu", async () => {
  const t = page();
  await t.box.pfvShow("claude");
  const root = t.els["pfv-in"];
  assert.strictEqual(byText(root, "button", "Edit").length, 0);
  assert.deepStrictEqual(texts(root, "pfv-note"), ["Defined in config.json. Duplicate it to change it."]);
  assert.strictEqual(byText(root, "button", "Start session").length, 1);
  assert.strictEqual(byCls(root, "menu").length, 0);
  assert.strictEqual(byCls(root, "btn-icon").length, 0);
  assert.strictEqual(byText(root, "button", "Switch to box").length, 0, "a profile you cannot edit has nothing to switch");
});

// The page head (UI plan 4 Task 3, design digest §B1.4 and §B2): the kit's page head, the one the other five Settings pages draw
// (settingshead.js): an H1, the one line, the actions, a 1x16 divider, then Esc ✕ (settingsEscButton). Start session is the one
// primary; the ⋯ is the kit's 28px icon button; Duplicate… on a profile you cannot change is a secondary.
const headOf = (root) => byCls(root, "pfv-head")[0];
const shownActions = (root) => byCls(headOf(root), "page-actions")[0].children.filter((c) => c instanceof Node && !c.hidden);

test("an editable profile: Start session is the primary, then ⋯, a divider and Esc ✕", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const root = t.els["pfv-in"];
  const [start, more, sep, esc, ...rest] = shownActions(root);
  assert.deepStrictEqual(rest, [], "four things in the head's actions: the note about unsaved changes is hidden while nothing changed");
  assert.strictEqual(start.tag, "button");
  assert.strictEqual(start.textContent, "Start session");
  assert.deepStrictEqual(classesOf(start), ["btn", "btn-primary"]);
  // ⋯: the kit's icon button (28px comes from .btn-icon, see settings-shell.test.js), inside the wrapper the menu hangs from.
  assert.ok(classesOf(more).includes("pfv-more"));
  const dots = byCls(more, "btn-icon")[0];
  assert.deepStrictEqual(classesOf(dots), ["btn", "btn-icon"]);
  assert.strictEqual(dots.getAttribute("aria-label"), "More actions for aws-ops");
  assert.strictEqual(dots.getAttribute("title"), "More actions");
  assert.strictEqual(dots.getAttribute("aria-haspopup"), "menu");
  assert.deepStrictEqual(byCls(more, "menu").length, 1, "the kit menu is untouched");
  assert.deepStrictEqual(classesOf(sep), ["set-sep"]);
  assert.strictEqual(sep.getAttribute("aria-hidden"), "true");
  assert.deepStrictEqual(classesOf(esc), ["btn", "btn-ghost", "btn-sm", "set-esc"], "settingsEscButton");
  assert.strictEqual(esc.getAttribute("aria-label"), "Close (Esc)");
  assert.strictEqual(esc.textContent, "Esc✕");
  esc.click();
  assert.deepStrictEqual(t.calls.filter((c) => c.startsWith("close:")), ["close:settings-modal"]);
  // One primary in the whole view.
  assert.strictEqual(root.all((n) => classesOf(n).includes("btn-primary")).length, 1);
});

test("a read-only profile: Start session is the primary and Duplicate… is a secondary", async () => {
  const t = page();
  await t.box.pfvShow("claude");
  const root = t.els["pfv-in"];
  const [start, dup, sep, esc, ...rest] = shownActions(root);
  assert.deepStrictEqual(rest, []);
  assert.deepStrictEqual([start.textContent, classesOf(start)], ["Start session", ["btn", "btn-primary"]]);
  assert.strictEqual(dup.textContent, "Duplicate…", "the label is exactly Duplicate…, like the menu's");
  assert.deepStrictEqual(classesOf(dup), ["btn"]);
  assert.deepStrictEqual(classesOf(sep), ["set-sep"]);
  assert.ok(classesOf(esc).includes("set-esc"));
  assert.deepStrictEqual(root.all((n) => /Duplicate/.test(n.textContent) && classesOf(n).some((c) => /primary/.test(c))), [], "no element with Duplicate in it is a primary");
  assert.strictEqual(root.all((n) => classesOf(n).some((c) => /primary/.test(c))).length, 1, "Start session is the only primary");
  dup.click();
  assert.strictEqual(t.run("pfvState").page, "dup", "it opens the Duplicate page");
});

test("the profile page's H1s carry their full text as a title: a long name ellipsizes in a narrow window (final review M6)", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const [h1] = byCls(headOf(t.els["pfv-in"]), "page-title");
  assert.strictEqual(h1.getAttribute("title"), "aws-ops", "the overview's head: the profile's name, whole");
  const n = await newPage();
  assert.strictEqual(byCls(rootOf(n), "page-title")[0].getAttribute("title"), "New profile", "New profile");
  const d = page();
  await d.box.pfvShow("aws-ops");
  await dupFromMenu(d);
  assert.strictEqual(byCls(rootOf(d), "page-title")[0].getAttribute("title"), "Copy of aws-ops", "Duplicate");
});

test("the head: the title is an h1, the model in the description is span.mono, and the section title reads This agent will be able to", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const root = t.els["pfv-in"];
  const [h1] = byCls(headOf(root), "page-title");
  assert.strictEqual(h1.tag, "h1");
  assert.strictEqual(h1.textContent, "aws-ops");
  const chip = byCls(headOf(root), "chip")[0];
  assert.deepStrictEqual(classesOf(chip), ["chip", "chip-box"]);
  assert.strictEqual(chip.parent, h1.parent, "the chip sits beside the title");
  const [desc] = byCls(headOf(root), "page-desc");
  assert.strictEqual(desc.tag, "p");
  assert.strictEqual(desc.textContent, "Claude Code · claude-opus-5-5 · high effort");
  const mono = byCls(desc, "mono");
  assert.deepStrictEqual(mono.map((n) => [n.tag, n.textContent, classesOf(n)]), [["span", "claude-opus-5-5", ["mono", "t-sm"]]], "only the model is machine text");
  assert.deepStrictEqual(texts(root, "pfv-sheet-h"), ["This agent will be able to"]);
  assert.strictEqual(byCls(root, "pfv-sheet-h")[0].tag, "h2", "under the h1");
  // A host profile's chip is the kit's host one.
  const h = page();
  await h.box.pfvShow("runbook-host");
  assert.deepStrictEqual(classesOf(byCls(h.els["pfv-in"], "chip")[0]), ["chip", "chip-host"]);
});

test("the head is correct whenever the page draws: on the first open (after the dialog is the page) and on a redraw", async () => {
  const t = page();
  // Before the dialog is the page (selectSettingsTab's first call) nothing is drawn: the head would be a phone's.
  t.els["settings-modal"].classList.remove("as-page");
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-head").length, 0);
  // openSettings calls pfvShow again once openDialog made it the page.
  t.els["settings-modal"].classList.add("as-page");
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(byCls(t.els["pfv-in"], "set-esc").length, 1);
  t.run("pfvRender()");
  assert.strictEqual(byCls(t.els["pfv-in"], "set-esc").length, 1, "a redraw replaces the head, it does not stack a second Esc ✕");
});

test("pfvHeaderPrimary: Start session is the one primary until a block is in edit, then the head has none (Save is)", async () => {
  const t = page();
  const pri = t.box.pfvHeaderPrimary;
  assert.strictEqual(pri({ readOnly: false, editing: false }), "start");
  assert.strictEqual(pri({ readOnly: true, editing: false }), "start", "a profile you cannot change still starts");
  assert.strictEqual(pri({ readOnly: false, editing: true }), "none");
  // The head follows it: with a block open, Start session is a secondary.
  await t.box.pfvShow("aws-ops");
  byText(t.els["pfv-in"], "button", "Edit")[0].click();
  const [start] = shownActions(t.els["pfv-in"]);
  assert.strictEqual(start.textContent, "Start session");
  assert.deepStrictEqual(classesOf(start), ["btn"], "demoted while a block is open");
  assert.strictEqual(byCls(headOf(t.els["pfv-in"]), "btn-primary").length, 0);
});

test("Edit is a ghost small button in column 3 with the block's aria-label", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const labels = t.run("PFV_EDIT_LABEL");
  const blocks = byCls(t.els["pfv-in"], "pfv-block");
  assert.strictEqual(blocks.length, 8);
  for (const b of blocks.filter((x) => x.dataset.block !== "canreach")) {
    const id = b.dataset.block;
    // Label, body, act: the grid's three columns in this order (label 132px, value, Edit).
    assert.deepStrictEqual(b.children.map((c) => classesOf(c)[0]), ["pfv-label", "pfv-body", "pfv-act"], id);
    const edit = byCls(b.children[2], "btn").filter((n) => n.textContent === "Edit");
    assert.strictEqual(edit.length, 1, id);
    assert.strictEqual(edit[0].tag, "button");
    assert.deepStrictEqual(classesOf(edit[0]), ["btn", "btn-ghost", "btn-sm"], id);
    assert.strictEqual(edit[0].getAttribute("aria-label"), labels[id], id);
    assert.strictEqual(edit[0].type, "button");
  }
});

// Frame M2.1 (design digest §B2): Advanced is the last block of the one card, a normal block with an Edit button and no collapse:
// its label with "none set" / "N set" under it, the helper as its value, Edit in column 3.
const ADV_HELPER = "Starting environment, arguments, environment variables, sidebar replies and CLAUDE.md imports. All at their defaults.";

test("Advanced is the last block of the one card: its count under the label, the helper as its value, no fold", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const sheets = byCls(t.els["pfv-in"], "pfv-sheet");
  assert.strictEqual(sheets.length, 1, "one card: Advanced has none of its own");
  const adv = blockOf(t, "advanced");
  assert.strictEqual(adv.parent, sheets[0]);
  assert.deepStrictEqual(sheets[0].children.map((b) => b.dataset.block), ["runs", "workspace", "agent", "cloud", "reachable", "network", "canreach", "advanced"]);
  assert.strictEqual(sheets[0].children[7], adv, "the eighth block");
  const label = byCls(adv, "pfv-label")[0];
  assert.strictEqual(label.tag, "h3");
  assert.strictEqual(labelName(label), "Advanced");
  assert.deepStrictEqual(label.children.map((c) => [c.tag, classesOf(c)[0], c.textContent]), [["span", "pfv-advst", "none set"]], "the count under the name, not after it on the line");
  assert.deepStrictEqual(adv.children.map((c) => classesOf(c)[0]), ["pfv-label", "pfv-body", "pfv-act"], "label, value, Edit: the three columns");
  assert.deepStrictEqual(texts(adv, "pfv-adv-sum"), [ADV_HELPER], "§A2: the helper only, with nothing set");
  assert.strictEqual(byText(adv, "button", "Edit").length, 1);
  // No collapse: no button in the label, no chevron, no .fold, nothing expanded or not.
  assert.deepStrictEqual([byCls(adv, "pfv-fold").length, byCls(adv, "fold").length, byCls(adv, "pfv-chev").length], [0, 0, 0]);
  assert.strictEqual(adv.all((n) => "aria-expanded" in n.attrs).length, 0);
  assert.strictEqual(adv.all((n) => n.tag === "svg").length, 0, "no chevron icon");
  assert.strictEqual(adv.className, "pfv-block", "no .pfv-open state either");
  assert.strictEqual("advOpen" in t.run("pfvState"), false, "the fold's state is gone");
});

test("Advanced with settings shows what is set as its value, always: nothing to unfold", async () => {
  const t = page();
  DOCS["aws-ops"].args = ["--verbose"];
  DOCS["aws-ops"].env = { LOG_LEVEL: "debug" };
  try {
    await t.box.pfvShow("aws-ops");
    const adv = blockOf(t, "advanced");
    assert.strictEqual(adv.parent, byCls(t.els["pfv-in"], "pfv-sheet")[0], "still in the one card");
    assert.deepStrictEqual([labelName(byCls(adv, "pfv-label")[0]), texts(adv, "pfv-advst")[0]], ["Advanced", "2 set"]);
    assert.deepStrictEqual(texts(adv, "pfv-dl"), ["Arguments--verboseEnvironmentLOG_LEVEL"], "the values are the block's value");
    assert.strictEqual(byCls(adv, "pfv-adv-sum").length, 0, "the helper stands in for them only when there are none");
    assert.deepStrictEqual([byCls(adv, "pfv-fold").length, byCls(adv, "pfv-chev").length, adv.all((n) => "aria-expanded" in n.attrs).length], [0, 0, 0]);
    assert.deepStrictEqual(adv.children.map((c) => classesOf(c)[0]), ["pfv-label", "pfv-body", "pfv-act"]);
  } finally {
    delete DOCS["aws-ops"].args;
    delete DOCS["aws-ops"].env;
  }
});

test("an editable profile has Start session and a menu with Duplicate… and a red Delete… (the kit menu: profile-menu.test.js)", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const root = t.els["pfv-in"];
  const [start] = byText(root, "button", "Start session");
  assert.ok(classesOf(start).includes("btn-primary"));
  const menu = byCls(root, "menu")[0];
  assert.ok(menu.hidden, "closed until the ⋯ button opens it");
  assert.strictEqual(menu.getAttribute("aria-label"), "aws-ops profile actions");
  const items = byCls(menu, "menu-item");
  assert.deepStrictEqual(items.map((i) => i.textContent), ["Duplicate…", "Delete…"]);
  assert.ok(items[1].className.includes("is-danger"));
  const more = byCls(root, "btn-icon")[0];
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
  assert.strictEqual(sub.length, 3, "the reason, the consequence, and what New session pre-selects");
  assert.ok(!sub[1].className.includes("pfv-red"));
  assert.strictEqual(sub[2].textContent, "New session pre-selects: Box · in place");
  assert.strictEqual(sub[1].textContent, "Sessions with this profile can't start until Docker is installed.");
});

test("boxes unavailable on a profile that names no tier: it says its sessions start on this machine, in the same two lines", async () => {
  const t = page({ boxRuntime: "", boxUnavailableReason: "Docker is not installed" });
  t.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" }; // names no tier
  await t.box.pfvShow("infra-terraform");
  const runs = byCls(t.els["pfv-in"], "pfv-block")[0];
  assert.deepStrictEqual(texts(runs, "pfv-main"), ["In a box"]);
  assert.deepStrictEqual(texts(runs, "pfv-red"), ["Boxes are unavailable on this machine: Docker is not installed."]);
  const sub = byCls(runs, "pfv-sub");
  assert.strictEqual(sub.length, 3, "the reason, the consequence, and what New session pre-selects");
  assert.ok(!sub[1].className.includes("pfv-red"));
  assert.strictEqual(sub[2].textContent, "New session pre-selects: Box · in place");
  assert.strictEqual(sub[1].textContent, "Until it is, sessions start on this machine, uncontained.");
});

test("boxes unavailable, a profile that names no tier under default_runtime box: the list row's runtime_asked keeps the can't-start line", async () => {
  const t = page({
    boxRuntime: "", boxUnavailableReason: "Docker is not installed",
    profiles: PROFILES.map((p) => (p.name === "infra-terraform" ? Object.assign({}, p, { runtime_asked: "box" }) : p)),
  });
  t.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" };
  await t.box.pfvShow("infra-terraform");
  const runs = byCls(t.els["pfv-in"], "pfv-block")[0];
  assert.strictEqual(byCls(runs, "pfv-sub")[1].textContent, "Sessions with this profile can't start until Docker is installed.");
});

test("Advanced has its count under the label and lists what is set, with no fold to open", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  let adv = byCls(t.els["pfv-in"], "pfv-block").find((b) => b.dataset.block === "advanced");
  assert.deepStrictEqual([labelName(byCls(adv, "pfv-label")[0]), texts(adv, "pfv-advst")[0]], ["Advanced", "none set"]);
  assert.strictEqual(byCls(adv, "pfv-adv-sum").length, 1);
  assert.strictEqual(byCls(adv, "pfv-dl").length, 0);

  DOCS["aws-ops"].args = ["--verbose"];
  try {
    await t.box.pfvShow("aws-ops");
    adv = byCls(t.els["pfv-in"], "pfv-block").find((b) => b.dataset.block === "advanced");
    assert.deepStrictEqual([labelName(byCls(adv, "pfv-label")[0]), texts(adv, "pfv-advst")[0]], ["Advanced", "1 set"]);
    assert.deepStrictEqual(texts(adv, "pfv-dl"), ["Arguments--verbose"]);
    assert.strictEqual(byCls(adv, "pfv-fold").length, 0, "no button to close it");
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
  assert.ok(t.calls.includes("DELETE /api/profiles/aws-ops?rev=r1"), "the delete names the revision the page opened");
  assert.strictEqual(t.run("pfvState").name, "infra-terraform", "the profile that followed it in the list");
  assert.deepStrictEqual(titles(t.els["pfv-in"]), ["infra-terraform"]);
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
  assert.ok(t.calls.includes("DELETE /api/profiles/aws-ops?rev=r1"));
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
  assert.deepStrictEqual(titles(t.els["pfv-in"]), ["infra-terraform"]);
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
  // A profile that stores no runtime takes the resolved tier, the row's: when the list says the instance default changed, the page follows.
  // (One that stores a runtime keeps it, whatever the row says: pfvTierOf.)
  const t = page();
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"]);
  delete t.box.docs["aws-ops"].runtime;
  await t.box.pfvShow("aws-ops");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "chip"), ["box"]);
  // The list is fetched again (app.js loadProfiles -> renderProfileList -> pfvRenderNav) and the row changed.
  t.box.profiles = PROFILES.map((p) => (p.name === "aws-ops" ? Object.assign({}, p, { runtime: "host" }) : p));
  t.box.pfvRenderNav();
  assert.deepStrictEqual(texts(t.els["pfv-in"], "chip"), ["host"]);
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
  assert.ok(t.calls.includes("DELETE /api/profiles/aws-ops?rev=r1"));
  assert.strictEqual(t.run("pfvState").name, "", "nothing selected while the reload is out");
  assert.strictEqual(titles(t.els["pfv-in"]).length, 0, "the deleted profile's header, Start session and menu are gone");
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
  // Nothing left to show: the New profile page (R19), not a blank pane and not the profile that was deleted.
  assert.deepStrictEqual(titles(lone.els["pfv-in"]), ["New profile"]);
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
  byCls(t.els["pfv-in"], "btn-icon")[0].click();
  const menu = byCls(t.els["pfv-in"], "menu")[0];
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
  const src = ["profilepage.js", "pfv-workspace.js", "pfv-cloud.js", "pfv-advanced.js", "pfv-new.js"].map((f) => fs.readFileSync(path.join(STATIC, f), "utf8")).join("\n");
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
  assert.deepStrictEqual(subs(t.els["pfv-in"]), ["Claude Code · claude-sonnet-5 · high effort"], "the read view shows the new model");
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
  assert.deepStrictEqual(subs(t.els["pfv-in"]), ["Claude Code · claude-opus-5 · high effort"]);
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
  assert.deepStrictEqual(subs(t.els["pfv-in"]), ["Claude Code · claude-opus-5-5 · high effort"]);
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

// --- Runs in: the Box only checkbox (S1's host_block and box_only on the profile view) ---------------------------------

const BOX_ONLY_LABEL = "Box only: never offer Host for this profile";
const AWS_SENTENCE = "This profile has an AWS binding, so it runs only in a box.";
async function openRuns(t, name) {
  await t.box.pfvShow(name);
  clickBtn(blockOf(t, "runs"), "Edit");
}
const boxOnlyLabel = (t) => byCls(blockOf(t, "runs"), "pfv-check")[0];

test("the Box only checkbox: forced by bindings is ticked and disabled with the reason, a set lock stays clearable", async () => {
  // A plain box profile: the choice is free.
  const plain = page();
  await openRuns(plain, "infra-terraform");
  const free = field(plain, "box_only");
  assert.strictEqual(free.checked, false);
  assert.notStrictEqual(free.disabled, true);
  assert.deepStrictEqual(byCls(blockOf(plain, "runs"), "pfv-check-t").map(labelName), [BOX_ONLY_LABEL]);
  assert.deepStrictEqual(texts(blockOf(plain, "runs"), "pfv-check-d"), []);
  assert.ok(!classesOf(boxOnlyLabel(plain)).includes("pfv-off"));
  // Bound, with no lock set: the bindings force it, so it is ticked, off for the user, and says why.
  const bound = page();
  await openRuns(bound, "aws-ops");
  const forced = field(bound, "box_only");
  assert.strictEqual(forced.checked, true);
  assert.strictEqual(forced.disabled, true);
  assert.deepStrictEqual(byCls(blockOf(bound, "runs"), "pfv-check-t").map(labelName), [BOX_ONLY_LABEL]);
  assert.deepStrictEqual(texts(blockOf(bound, "runs"), "pfv-check-d"), [AWS_SENTENCE]);
  assert.ok(classesOf(boxOnlyLabel(bound)).includes("pfv-off"));
  // Bound and locked: the lock is redundant, but it is the user's to clear.
  const locked = page({ profiles: PROFILES.map((p) => (p.name === "aws-ops" ? Object.assign({}, p, { box_only: true }) : p)) });
  locked.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { box_only: true });
  await openRuns(locked, "aws-ops");
  const clearable = field(locked, "box_only");
  assert.strictEqual(clearable.checked, true);
  assert.notStrictEqual(clearable.disabled, true);
  assert.deepStrictEqual(texts(blockOf(locked, "runs"), "pfv-check-d"), []);
  assert.ok(!classesOf(boxOnlyLabel(locked)).includes("pfv-off"));
  // Clearing it saves the profile with the key gone.
  clearable.checked = false;
  clearable.onchange();
  assert.strictEqual(await clickBtn(blockOf(locked, "runs"), "Save"), true);
  assert.ok(!("box_only" in locked.box.puts[0]));
  assert.strictEqual(locked.box.puts[0].runtime, "box");
});

test("Host with Box only on is refused before the PUT", async () => {
  const t = page({ profiles: PROFILES.map((p) => (p.name === "infra-terraform" ? Object.assign({}, p, { box_only: true, host_block: "this profile is set to box only" }) : p)) });
  t.box.docs["infra-terraform"] = Object.assign({}, DOCS["infra-terraform"], { box_only: true });
  await openRuns(t, "infra-terraform");
  const host = field(t, "tier-host");
  host.checked = true;
  host.onchange();
  const before = t.calls.length;
  assert.strictEqual(await clickBtn(blockOf(t, "runs"), "Save as host…"), false);
  assert.strictEqual(t.box.puts.length, 0);
  assert.ok(!t.calls.some((x) => x.startsWith("PUT /api/profiles/")), "no PUT was made");
  assert.strictEqual(t.box.asked.length, 0, "no host-switch confirm was opened");
  assert.ok(!t.calls.slice(before).some((x) => x.startsWith("confirm:")));
  const PFV_BOX_ONLY_HOST = t.run("PFV_BOX_ONLY_HOST");
  assert.strictEqual(PFV_BOX_ONLY_HOST, "Box only is on, so this profile cannot be saved as host. Turn Box only off first.");
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-ferr"), [PFV_BOX_ONLY_HOST]);
  assert.strictEqual(field(t, "box_only").getAttribute("aria-invalid"), "true");
});

test("a server refusal of box_only with host lands in Runs in", async () => {
  const t = page();
  await openRuns(t, "infra-terraform");
  field(t, "tier-host").checked = true;
  field(t, "tier-host").onchange();
  t.box.confirmAnswer = true;
  t.box.putFail = apiErr(400, { error: 'profile "infra-terraform": box_only: cannot be combined with runtime: host', field: "box_only" });
  await clickBtn(blockOf(t, "runs"), "Save as host…");
  const runs = blockOf(t, "runs");
  assert.ok(runs.className.includes("pfv-editing"));
  assert.strictEqual(texts(runs, "pfv-ferr").length, 1, "the field's words sit in the block that owns it");
  assert.strictEqual(field(t, "box_only").getAttribute("aria-invalid"), "true");
});

test("ticking Box only and saving sends box_only", async () => {
  const t = page();
  await openRuns(t, "infra-terraform");
  const cb = field(t, "box_only");
  cb.checked = true;
  cb.onchange();
  assert.strictEqual(await clickBtn(blockOf(t, "runs"), "Save"), true);
  assert.strictEqual(t.box.puts[0].box_only, true);
  assert.strictEqual(t.box.puts[0].runtime, "box");
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

test("a save whose reload fails still says it saved, and the next save carries the revision the save answered with", async () => {
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
  // The page holds the new revision, not the one it opened: a second save is not refused for the first.
  assert.strictEqual(t.run("pfvState").doc.rev, t.box.docs["aws-ops"].rev);
  t.box.getFail = null;
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-opus-5");
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), true);
  assert.strictEqual(t.box.puts.length, 2);
  assert.notStrictEqual(t.box.puts[1].rev, "r1", "the second save names the first save's revision");
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
  assert.deepStrictEqual(titles(t.els["pfv-in"]), ["runbook-host"]);
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
  assert.deepStrictEqual(titles(t.els["pfv-in"]), ["runbook-host"]);
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

test("Workspace: a stored copy where copies cannot be chosen can pick the folder and then the copy again, with no change left", async () => {
  const stuck = async (t, name) => {
    await openWs(t, name);
    const head = byCls(t.els["pfv-in"], "pfv-unsaved")[0];
    assert.strictEqual(field(t, "mode-copy").checked, true);
    assert.notStrictEqual(field(t, "mode-copy").disabled, true);
    pickMode(t, "folder");
    assert.strictEqual(head.hidden, false);
    assert.strictEqual(field(t, "mode-folder").checked, true);
    assert.notStrictEqual(field(t, "mode-copy").disabled, true, "the way back stays open after the redraw");
    // The copy card's own description is its one .pfv-card-d line; a reason, whatever its words, would be a second.
    const copyCard = byCls(blockOf(t, "workspace"), "pfv-card").filter((n) => n.textContent.includes("Work on a private copy"))[0];
    assert.strictEqual(byCls(copyCard, "pfv-card-d").length, 1, "no reason is said for a card that can be picked");
    pickMode(t, "copy");
    assert.strictEqual(field(t, "mode-copy").checked, true);
    assert.ok(head.hidden, "a copy and back is no change");
    // Saving after the way back sends the stored copy as it was: the same folder, copy, share list and auto-return opt-out.
    const stored = t.box.docs[name];
    assert.strictEqual(await clickBtn(blockOf(t, "workspace"), "Save"), true);
    assert.strictEqual(t.box.puts.length, 1);
    const body = t.box.puts[0];
    for (const k of ["default_cwd", "clone_of", "clone_share", "clone_no_auto_return"]) assert.deepStrictEqual(body[k], stored[k], k + " is as stored");
    assert.ok(stored.clone_of && stored.clone_share.length > 0, "the stored copy has something to lose");
  };
  await stuck(clonePage({ cloneUnavailable: "Docker is not installed" }), "gitops-clone");
  const h = page({ profiles: [view("gitops-host", { runtime: "host", default_cwd: "/home/you/box-clones/k8s-gitops", clone_of: "/home/you/work/k8s-gitops" }), PROFILES[1], PROFILES[3]] });
  h.box.docs["gitops-host"] = Object.assign({}, CLONE_DOC, { name: "gitops-host", runtime: "host" });
  await stuck(h, "gitops-host");
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

test("Workspace on a host profile: one folder field, and the private-copy card off with the sentence saying why", async () => {
  const t = page();
  const pk = await openWs(t, "runbook-host");
  const ws = blockOf(t, "workspace");
  assert.deepStrictEqual(byCls(ws, "pfv-flabel").map((n) => n.textContent), ["Folder", "How it works on the folder"]);
  assert.deepStrictEqual(texts(ws, "pfv-hint"), ["Where sessions start. A private copy needs a box."]);
  assert.strictEqual(byCls(ws, "pfv-card").length, 2, "both cards; the copy one is off");
  assert.strictEqual(field(t, "mode-copy").disabled, true);
  assert.strictEqual(field(t, "folder").value, "/home/you/runbooks");
  assert.deepStrictEqual(texts(ws, "pfv-fnote"), [], "a host has no box to give the next session");
  pk.pick("/home/you/notes");
  await clickBtn(blockOf(t, "workspace"), "Save");
  assert.strictEqual(t.box.puts[0].default_cwd, "/home/you/notes");
  assert.strictEqual(t.box.puts[0].runtime, "host");
});

test("Workspace on a host profile draws the private-copy card disabled, tagged Applies to Box", async () => {
  const t = page();
  await openWs(t, "runbook-host");
  const ws = blockOf(t, "workspace");
  const copy = field(t, "mode-copy");
  assert.strictEqual(copy.disabled, true);
  const card = copy.parent;
  assert.ok(card.textContent.includes("Needs a box."));
  assert.ok(card.textContent.includes("Applies to Box"));
  assert.strictEqual(field(t, "mode-folder").checked, true);
  assert.notStrictEqual(field(t, "mode-folder").disabled, true);
  assert.deepStrictEqual(texts(ws, "pfv-card-t"), ["Work in this folder", "Work on a private copy"]);
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
const optBtn = (t, id, title) => byCls(blockOf(t, id), "pfv-opt").find((b) => texts(b, "pfv-opt-t")[0].startsWith(title));
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

test("read view + Add access opens cloud in edit mode and asks for the picker", async () => {
  const t = cloudPage();
  const asked = [];
  // Whatever draws the picker, the page only asks for it, by block.
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

test("read view + Add access opens the block with the picker open and focus in its filter", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-pick").length, 1);
  assert.strictEqual(Node.focused, byCls(blockOf(t, "cloud"), "pfv-pick-find")[0]);
  assert.strictEqual(t.run("pfvState").picker, "cloud");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  assert.strictEqual(t.run("pfvState").picker, "reachable", "the one in the open block; the other block's form is gone with its picker");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-pick").length, 1);
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

// --- the "+ Add" picker (spec 5.7; the mockup's `picker` state) ----------------------------------------------------------

// What the instance defines: a production connection that can write, a plain one, two more and one that uses the SSO role directly;
// tokens with and without a description, one with two hosts (one on a port that is not 443), and a GitHub one.
const PICK_AWS = [
  { name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" },
  { name: "staging-read", account_id: "111111111111", region: "eu-west-1" },
  { name: "sandbox", account_id: "333333333333", region: "us-east-1" },
  { name: "sso-lab", mode: "sso_direct", account_id: "444444444444", region: "eu-west-1" },
];
const PICK_TOKENS = [
  { name: "grafana-prod", prod: true, description: "Grafana service account", env: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"], hosts: [{ host: "grafana.acme.example.com", port: 443 }] },
  { name: "sentry-staging", env: ["SENTRY_ACCESS_TOKEN"], hosts: [{ host: "sentry.io", port: 443 }] },
  { name: "pagerduty", description: "PagerDuty read key", env: ["PAGERDUTY_TOKEN"], hosts: [{ host: "api.pagerduty.com", port: 443 }, { host: "events.pagerduty.com", port: 8443 }] },
  GH_PAT_ENTRY,
];
const pickPage = (over, doc) => cloudPage(Object.assign({ awscEntries: PICK_AWS, stEntries: PICK_TOKENS }, over), doc);
const openPicker = async (t) => { await t.box.pfvShow("aws-ops"); clickBtn(blockOf(t, "cloud"), "+ Add access"); };
const popOf = (t, id) => byCls(blockOf(t, id || "cloud"), "pfv-pick")[0];
const optsOf = (t, id) => byCls(blockOf(t, id || "cloud"), "pfv-opt");
const optName = (b) => texts(b, "pfv-opt-t")[0];
const groupsOf = (t) => byCls(popOf(t), "pfv-pick-grp");
// What the user can see of the options: neither the option nor its group is hidden.
const shownOpts = (t) => groupsOf(t).filter((g) => !g.hidden).flatMap((g) => byCls(g, "pfv-opt")).filter((o) => !o.hidden).map(optName);
// A click on the document, as the page's listeners get it.
const docClick = (t, target) => (t.doc.listeners.click || []).forEach((f) => f({ target }));

test("the + Add picker is a dialog under its link: a filter, then AWS connections, GitHub and Tokens, each ending in what creates the missing thing", async () => {
  const t = pickPage();
  await openPicker(t);
  const pop = popOf(t);
  assert.strictEqual(pop.getAttribute("role"), "dialog");
  assert.strictEqual(pop.getAttribute("aria-label"), "Add cloud access or a token");
  const link = field(t, "add");
  assert.strictEqual(link.getAttribute("aria-expanded"), "true");
  assert.strictEqual(link.getAttribute("aria-haspopup"), "dialog");
  assert.strictEqual(link.parent, pop.parent, "the popover hangs on the link's own wrapper, so it sits under it");
  const filter = byCls(pop, "pfv-pick-find")[0];
  assert.strictEqual(filter.getAttribute("placeholder"), "Filter connections and tokens");
  assert.strictEqual(filter.getAttribute("aria-label"), "Filter connections and tokens");
  assert.strictEqual(Node.focused, filter, "focus starts in the filter");
  assert.deepStrictEqual(groupsOf(t).map((g) => texts(g, "menu-label")[0]), ["AWS connections", "GitHub", "Tokens"]);
  assert.deepStrictEqual(groupsOf(t).map((g) => byCls(g, "pfv-opt").map(optName)), [
    ["prod-read PROD", "staging-read", "sandbox", "sso-lab", "New AWS connection…"],
    ["Another org or user", "New GitHub App…"],
    ["grafana-prod production", "sentry-staging", "pagerduty", "gh-pat", "New token…"],
  ]);
  assert.deepStrictEqual(texts(popOf(t), "pfv-pick-note"), ["Tokens are long-lived, and the agent can read them while it runs."]);
  assert.strictEqual(t.run("pfvState").picker, "cloud");
  assert.strictEqual(t.box.puts.length, 0);
});

test("picker sub-line shows account and region, what the connection can do, a token's description and hosts", async () => {
  const t = pickPage();
  await openPicker(t);
  const sub = (name) => texts(optsOf(t).find((b) => optName(b).startsWith(name)), "pfv-opt-s");
  assert.deepStrictEqual(sub("prod-read"), ["account 222222222222, eu-west-1. Read only; write after a granted window"]);
  assert.deepStrictEqual(sub("staging-read"), ["account 111111111111, eu-west-1. Read only"]);
  assert.deepStrictEqual(sub("sandbox"), ["account 333333333333, us-east-1. Read only"]);
  assert.deepStrictEqual(sub("sso-lab"), ["account 444444444444, eu-west-1. Read only. Uses the SSO role directly."], "the SSO-direct words of the read view's row");
  assert.deepStrictEqual(sub("grafana-prod"), ["Grafana service account, grafana.acme.example.com"]);
  assert.deepStrictEqual(sub("sentry-staging"), ["sentry.io"], "no description: just the hosts");
  assert.deepStrictEqual(sub("pagerduty"), ["PagerDuty read key, api.pagerduty.com, events.pagerduty.com:8443"], "a port that is not 443 is shown");
  assert.deepStrictEqual(sub("Another org or user"), ["Through the GitHub App: 1-hour tokens, read only unless you change it"]);
  assert.deepStrictEqual(sub("New AWS connection…"), ["Set it up in Credentials without leaving this page"]);
  assert.deepStrictEqual(sub("New GitHub App…"), ["Install the App on an org, or paste a personal access token"]);
  assert.deepStrictEqual(sub("New token…"), ["Kept in the encrypted credential store"]);
  assert.deepStrictEqual(texts(optsOf(t).find((b) => optName(b).startsWith("prod-read")), "pfv-tag"), ["PROD"]);
  assert.deepStrictEqual(texts(optsOf(t).find((b) => optName(b).startsWith("grafana-prod")), "pfv-tag"), ["production"]);
  // A connection the instance has no account or region for says only what it can do.
  const bare = pickPage({ awscEntries: [{ name: "bare" }] });
  await openPicker(bare);
  assert.deepStrictEqual(texts(optsOf(bare).find((b) => optName(b) === "bare"), "pfv-opt-s"), ["Read only"]);
});

test("picker marks names already bound as Added and they cannot be picked", async () => {
  const t = pickPage();
  await openPicker(t);
  const added = optsOf(t).filter((b) => texts(b, "pfv-opt-r").length > 0);
  assert.deepStrictEqual(added.map(optName), ["prod-read PROD", "staging-read", "grafana-prod production", "sentry-staging"], "what the profile holds already, drawn rather than left out");
  for (const b of added) {
    assert.deepStrictEqual(texts(b, "pfv-opt-r"), ["Added"]);
    assert.strictEqual(b.disabled, true);
    assert.strictEqual(b.onclick, undefined, "a disabled option does nothing");
  }
  const free = optsOf(t).filter((b) => !added.includes(b));
  assert.ok(free.every((b) => b.disabled !== true), "everything else can be picked");
  // A name that was only just picked counts as added too.
  optBtn(t, "cloud", "sandbox").click();
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(optBtn(t, "cloud", "sandbox").disabled, true);
  assert.deepStrictEqual(texts(optBtn(t, "cloud", "sandbox"), "pfv-opt-r"), ["Added"]);
});

test("picking adds a pending row: the connection or token, with the read view's words, saved only with Save", async () => {
  const t = pickPage();
  await openPicker(t);
  optBtn(t, "cloud", "sandbox").click();
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-pick").length, 0, "picking closes the picker");
  assert.strictEqual(t.run("pfvState").picker, "");
  assert.deepStrictEqual(texts(rowFor(t, "cloud", "AWS", "sandbox"), "pfv-rsub"), ["account 333333333333, us-east-1"]);
  assert.strictEqual(Node.focused, field(t, "rm:aws:sandbox"), "focus goes to the new row");
  assert.strictEqual(unsavedNote(t).hidden, false);
  assert.strictEqual(t.box.puts.length, 0);
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  optBtn(t, "cloud", "pagerduty").click();
  assert.ok(rowFor(t, "cloud", "Token", "pagerduty"));
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(t.box.puts[0].aws_connections, ["prod-read", "sandbox", "staging-read"]);
  assert.deepStrictEqual(t.box.puts[0].tokens, ["grafana-prod", "pagerduty", "sentry-staging"]);
});

test("a row pending removal stays pickable: picking it takes the removal back", async () => {
  const t = pickPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  assert.strictEqual(unsavedNote(t).hidden, false);
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  const opt = optBtn(t, "cloud", "staging-read");
  assert.strictEqual(opt.disabled, false, "it is not bound while its removal is pending");
  assert.deepStrictEqual(texts(opt, "pfv-opt-r"), []);
  opt.click();
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-removed").length, 0, "the row is back, not struck");
  assert.strictEqual(rowsOf(t, "cloud").filter((r) => (texts(r, "pfv-nm")[0] || "").startsWith("staging-read")).length, 1, "and not listed twice");
  assert.ok(unsavedNote(t).hidden, "nothing is left to save");
});

test("the GitHub option adds an owner row; with only a token to sign in with it says what a token does", async () => {
  const t = pickPage();
  await openPicker(t);
  optBtn(t, "cloud", "Another org or user").click();
  assert.strictEqual(field(t, "gh:1:pick").value, "app:acme-app", "the first login is preselected");
  assert.strictEqual(Node.focused, field(t, "gh:1:owner"));
  const tok = pickPage({ ghApps: [] });
  await openPicker(tok);
  const gh = optBtn(tok, "cloud", "Another org or user");
  assert.deepStrictEqual(texts(gh, "pfv-opt-s"), ["A personal access token is not narrowed by CuliBox: it reaches whatever the token allows."], "the App's words would not be true here");
  gh.click();
  assert.strictEqual(field(tok, "gh:1:pick").value, "token:gh-pat");
});

test("with nothing to sign in to GitHub with, the picker says so and still offers to create it", async () => {
  const t = pickPage({ ghApps: [], stEntries: PICK_TOKENS.slice(0, 3) });
  await openPicker(t);
  const gh = groupsOf(t)[1];
  assert.deepStrictEqual(byCls(gh, "pfv-opt").map(optName), ["New GitHub App…"], "an option nobody can use is not drawn");
  assert.deepStrictEqual(texts(gh, "pfv-pick-note"), ["No GitHub App or token is defined yet."]);
});

test("while the profile has the old-style AWS setting the picker says why AWS cannot be added, and offers it once that is removed", async () => {
  const t = legacyPage();
  t.box.awscEntries = PICK_AWS;
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  const aws = () => groupsOf(t)[0];
  assert.deepStrictEqual(byCls(aws(), "pfv-opt").map(optName), [], "the server takes the old-style setting or saved connections, never both");
  assert.deepStrictEqual(texts(aws(), "pfv-pick-note"), ["Move the old-style AWS setting to a saved connection, or remove it, before adding connections."]);
  assert.strictEqual(optBtn(t, "cloud", "New AWS connection…"), undefined, "a connection made now could not be saved with it");
  assert.deepStrictEqual(groupsOf(t).slice(1).map((g) => texts(g, "menu-label")[0]), ["GitHub", "Tokens"], "the rest is unaffected");
  // Taking the setting away (pending, like any removal) frees AWS in the same edit.
  byText(rowFor(t, "cloud", "AWS", "Old-style AWS setting"), "button", "Remove")[0].click();
  assert.strictEqual(t.run("pfvState").picker, "cloud", "the picker stays open under the row");
  assert.deepStrictEqual(byCls(aws(), "pfv-opt").map(optName), ["prod-read PROD", "staging-read", "sandbox", "sso-lab", "New AWS connection…"]);
  assert.deepStrictEqual(texts(aws(), "pfv-pick-note"), []);
});

test("the filter narrows the options by name and by sub-line, hides a group with nothing left, and says when nothing matches", async () => {
  const t = pickPage();
  await openPicker(t);
  const filter = byCls(popOf(t), "pfv-pick-find")[0];
  const none = () => byCls(popOf(t), "pfv-pick-none")[0];
  const all = shownOpts(t);
  assert.strictEqual(all.length, 12, "every option and every create item");
  assert.strictEqual(none().hidden, true);
  typeInto(filter, "prod");
  assert.deepStrictEqual(shownOpts(t), ["prod-read PROD", "grafana-prod production"], "by name");
  assert.strictEqual(groupsOf(t)[1].hidden, true, "GitHub has nothing left");
  typeInto(filter, "SENTRY.io");
  assert.deepStrictEqual(shownOpts(t), ["sentry-staging"], "by sub-line, whatever the case");
  typeInto(filter, "us-east");
  assert.deepStrictEqual(shownOpts(t), ["sandbox"]);
  typeInto(filter, "  zzz ");
  assert.deepStrictEqual(shownOpts(t), []);
  assert.strictEqual(none().hidden, false);
  assert.deepStrictEqual(texts(popOf(t), "pfv-pick-none"), ["Nothing matches."]);
  typeInto(filter, "");
  assert.deepStrictEqual(shownOpts(t), all);
  assert.strictEqual(none().hidden, true);
  assert.ok(groupsOf(t).every((g) => g.hidden !== true));
  assert.ok(unsavedNote(t).hidden, "typing in the filter is no change");
  assert.strictEqual(t.box.puts.length, 0);
});

test("what was typed in the filter survives the form being drawn again, and is gone when the picker is opened anew", async () => {
  const t = pickPage();
  await openPicker(t);
  typeInto(byCls(popOf(t), "pfv-pick-find")[0], "sand");
  t.run("pfvRedrawForm(pfvState.edits.cloud)");
  const filter = byCls(popOf(t), "pfv-pick-find")[0];
  assert.strictEqual(filter.value, "sand");
  assert.deepStrictEqual(shownOpts(t), ["sandbox"]);
  assert.strictEqual(Node.focused, filter, "and focus is still in it");
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(byCls(popOf(t), "pfv-pick-find")[0].value, "");
  assert.strictEqual(shownOpts(t).length, 12);
});

// The popover is what holds the page's scroll height when the block is the last on the page. Options that the filter hides would
// shorten it, the page would scroll back under the user's hands, and the filter and the link would move down while it is typed in.
const PICK_MIN = "--pfv-pick-min";
const pickMin = (t) => byCls(popOf(t), "pfv-pick-list")[0].style.props[PICK_MIN];

test("the filter does not move while it is typed in: the list keeps the height it had when the picker opened", async () => {
  const t = pickPage();
  await openPicker(t);
  assert.strictEqual(pickMin(t), "600px", "12 options at 50px each, measured once the picker is drawn");
  const filter = byCls(popOf(t), "pfv-pick-find")[0];
  typeInto(filter, "prod");
  assert.strictEqual(byCls(popOf(t), "pfv-pick-list")[0].offsetHeight, 100, "the options that are shown take less room");
  assert.strictEqual(pickMin(t), "600px", "but the list may not get shorter");
  typeInto(filter, "zzz");
  assert.strictEqual(pickMin(t), "600px");
  typeInto(filter, "");
  assert.strictEqual(pickMin(t), "600px");
  // The form drawn again while the picker is open keeps it.
  typeInto(byCls(popOf(t), "pfv-pick-find")[0], "sand");
  t.run("pfvRedrawForm(pfvState.edits.cloud)");
  assert.strictEqual(pickMin(t), "600px");
});

test("the height is measured when the picker is opened, not remembered from the last time", async () => {
  const t = legacyPage();
  t.box.awscEntries = PICK_AWS;
  t.box.stEntries = PICK_TOKENS;
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(pickMin(t), "350px", "7 options while the old-style setting hides AWS");
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  byText(rowFor(t, "cloud", "AWS", "Old-style AWS setting"), "button", "Remove")[0].click();
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(pickMin(t), "600px", "12 now");
});

// A Tab that moves focus out of the popover closes it (it would stay open over the block's footer and Save); focus is where the user tabbed.
const pressTab = (t, shift) => {
  const pop = popOf(t);
  const ev = { key: "Tab", shiftKey: shift === true, target: Node.focused, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
  for (const f of pop.handlers.keydown || []) f(ev);
  return ev;
};

test("Tab out of the popover closes it and focus stays where the user tabbed; Tab inside it does not", async () => {
  const t = pickPage();
  await openPicker(t);
  // Tab from the filter to the first option, then on: inside the popover.
  pressTab(t);
  optsOf(t).find((b) => !b.disabled).focus();
  await settle();
  assert.strictEqual(t.run("pfvState").picker, "cloud", "focus is still in the popover");
  // From the last option to Cancel: out.
  const last = optsOf(t).filter((b) => !b.disabled).pop();
  last.focus();
  const ev = pressTab(t);
  assert.strictEqual(ev.defaultPrevented, false, "the browser does the move, the popover only follows it");
  const cancel = byText(blockOf(t, "cloud"), "button", "Cancel")[0];
  cancel.focus();
  await settle();
  assert.strictEqual(t.run("pfvState").picker, "");
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-pick").length, 0, "it no longer covers the footer");
  assert.strictEqual(Node.focused, cancel, "focus is not pulled back to the + Add link");
  assert.strictEqual(field(t, "add").getAttribute("aria-expanded"), "false");
  assert.ok(blockOf(t, "cloud").className.includes("pfv-editing"), "the block stays open");
  assert.ok(unsavedNote(t).hidden, "closing is no change");
});

test("Shift+Tab from the filter to the + Add link closes the popover with focus on the link", async () => {
  const t = pickPage();
  await openPicker(t);
  pressTab(t, true);
  field(t, "add").focus();
  await settle();
  assert.strictEqual(t.run("pfvState").picker, "");
  assert.strictEqual(Node.focused, field(t, "add"), "where the user tabbed");
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-pick").length, 0);
});

test("Tab out of a field the form draws again keeps focus on that field", async () => {
  const t = pickPage();
  await openPicker(t);
  pressTab(t);
  // Focus lands on a control the redraw replaces (a row's Remove): the form is drawn again with focus kept on it by its key.
  const rm = field(t, "rm:token:grafana-prod");
  rm.focus();
  await settle();
  assert.strictEqual(t.run("pfvState").picker, "");
  assert.strictEqual(Node.focused, field(t, "rm:token:grafana-prod"), "the new node for the same control");
});

test("a Tab that leaves nothing to do after the popover is gone changes nothing", async () => {
  const t = pickPage();
  await openPicker(t);
  pressTab(t);
  assert.strictEqual(t.box.pageLeaveHook("esc"), false, "Esc closed it before the timer ran");
  const at = Node.focused;
  await settle();
  assert.strictEqual(Node.focused, at);
  assert.strictEqual(t.run("pfvState").picker, "");
});

test("the create items call the Credentials drawer for their kind, once there is one, and close the picker", async () => {
  const t = pickPage();
  const asked = [];
  t.box.pfvOpenDrawer = (kind) => { asked.push(kind); };
  await openPicker(t);
  optBtn(t, "cloud", "New AWS connection…").click();
  assert.deepStrictEqual(asked, ["aws"]);
  assert.strictEqual(t.run("pfvState").picker, "", "the drawer takes over");
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  optBtn(t, "cloud", "New GitHub App…").click();
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  optBtn(t, "cloud", "New token…").click();
  assert.deepStrictEqual(asked, ["aws", "github", "token"]);
  assert.ok(unsavedNote(t).hidden, "creating is not picking: no row is added here");
});

test("a create item the page has no drawer for does nothing: the choice stays as it was", async () => {
  const t = pickPage();
  await openPicker(t);
  const rows = rowsOf(t, "cloud").length;
  for (const title of ["New AWS connection…", "New GitHub App…", "New token…"]) optBtn(t, "cloud", title).click();
  assert.strictEqual(t.run("pfvState").picker, "cloud", "the picker is as it was");
  assert.strictEqual(rowsOf(t, "cloud").length, rows);
  assert.ok(unsavedNote(t).hidden);
  assert.deepStrictEqual(t.calls.filter((c) => /^(POST|PUT)/.test(c)), []);
});

test("a click outside the open picker closes it and leaves focus where the click put it; a click inside, or on its link, does not", async () => {
  const t = pickPage();
  await openPicker(t);
  docClick(t, byCls(popOf(t), "pfv-pick-find")[0]);
  assert.strictEqual(t.run("pfvState").picker, "cloud", "inside");
  docClick(t, optBtn(t, "cloud", "New token…"));
  assert.strictEqual(t.run("pfvState").picker, "cloud", "on an option");
  docClick(t, field(t, "add"));
  assert.strictEqual(t.run("pfvState").picker, "cloud", "on its own link: the link toggles it");
  const elsewhere = field(t, "rm:aws:prod-read");
  elsewhere.focus();
  docClick(t, elsewhere);
  assert.strictEqual(t.run("pfvState").picker, "");
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-pick").length, 0);
  assert.strictEqual(Node.focused, field(t, "rm:aws:prod-read"), "not pulled back to the + Add link");
  assert.ok(unsavedNote(t).hidden, "closing is no change");
  assert.ok(blockOf(t, "cloud").className.includes("pfv-editing"), "the block stays open");
});

test("Esc in the filter closes the picker only and gives focus back to the link; typed text goes with it", async () => {
  const t = pickPage();
  await openPicker(t);
  typeInto(byCls(popOf(t), "pfv-pick-find")[0], "pro");
  assert.strictEqual(t.run("pfvEscTarget()"), "picker");
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  assert.strictEqual(byCls(blockOf(t, "cloud"), "pfv-pick").length, 0);
  assert.strictEqual(Node.focused, field(t, "add"));
  assert.strictEqual(field(t, "add").getAttribute("aria-expanded"), "false");
  assert.ok(blockOf(t, "cloud").className.includes("pfv-editing"));
  assert.strictEqual(t.box.pageLeaveHook("esc"), true, "the next Esc is the page's");
});

test("Also reachable's picker is the same popover with its two kinds, no filter and no headings; picking one adds an input row", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  const pop = popOf(t, "reachable");
  assert.strictEqual(pop.getAttribute("role"), "dialog");
  assert.strictEqual(pop.getAttribute("aria-label"), "Add a folder or a domain");
  assert.strictEqual(byCls(pop, "pfv-pick-find").length, 0, "two options need no filter");
  assert.strictEqual(byCls(pop, "menu-label").length, 0);
  assert.deepStrictEqual(optsOf(t, "reachable").map(optName), ["Folder it can read", "Domain it can reach"]);
  assert.deepStrictEqual(optsOf(t, "reachable").map((b) => texts(b, "pfv-opt-s")[0]), [
    "Read only, at the same path inside the box. Never list secrets.", "A hostname or *.example.com, over HTTPS",
  ]);
  assert.strictEqual(Node.focused, optsOf(t, "reachable")[0], "focus starts on the first option");
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

test("the picker stays inside the page column: it hangs on its link's wrapper at no more than the width the form has", () => {
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ sel: m[1].trim(), body: m[2] }));
  const rule = (cls) => rules.filter((r) => new RegExp("\\." + cls + "(?![\\w-])").test(r.sel.split(/\s+/).pop()) && !r.sel.includes("@container"));
  const has = (cls, re) => rule(cls).some((r) => re.test(r.body));
  assert.ok(has("pfv-addwrap", /position:\s*relative/), "the wrapper is what the popover is placed against");
  assert.ok(has("pfv-pick", /position:\s*absolute/));
  assert.ok(has("pfv-pick", /max-width:\s*100%/), "420px at most, never wider than the form");
  assert.ok(has("pfv-pick", /width:\s*420px/));
  assert.ok(has("pfv-pick", /z-index:\s*30/), "over the blocks after it");
  assert.ok(has("pfv-pick-list", /max-height:/) && has("pfv-pick-list", /overflow-y:\s*auto/), "a long list scrolls under the filter instead of running down the page");
  assert.ok(has("pfv-pick-list", /min-height:\s*min\(var\(--pfv-pick-min/), "the list may not get shorter than it opened at, and never taller than the room");
  assert.ok(has("pfv-opt", /text-decoration:\s*none/), "primitives.css strikes every disabled button through; an Added option is not struck");
  assert.ok(has("pfv-opt-t", /overflow-wrap:\s*anywhere/) && has("pfv-opt-s", /overflow-wrap:\s*anywhere/), "a long name breaks rather than push the popover out");
  assert.ok(rules.some((r) => /\.pfv-opt\[hidden\]/.test(r.sel) && /display:\s*none/.test(r.body)), "a grid option is hidden by the filter");
  assert.ok(rules.some((r) => /\.pfv-pick-grp\[hidden\]/.test(r.sel) && /display:\s*none/.test(r.body)));
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

// A profile that stores no runtime runs in whatever the instance defaults to (the list view's runtime is that resolved tier), and the
// page shows it. Credentials, egress and readable folders need an EXPLICIT box on the server (internal/config/awsconn.go: "ValidateProfile
// sees only an explicit runtime"), so every save, whichever block it is, writes the tier the page shows (spec 9 rule 1; runtimeToSend).
const noTier = (t, name) => { delete t.box.docs[name].runtime; return t; };

test("a Cloud and tokens save on a profile that stores no runtime sends the tier the page shows, box", async () => {
  const t = noTier(cloudPage(), "aws-ops");
  await t.box.pfvShow("aws-ops");
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-main"), ["In a box"], "the page reads the profile as boxed");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "token", name: "gh-pat" }), true);
  await saveBlock(t, "cloud");
  assert.strictEqual(t.box.puts.length, 1);
  assert.strictEqual(t.box.puts[0].runtime, "box", "without it the server refuses the binding: credentials need an explicit box");
  assert.deepStrictEqual(t.box.puts[0].tokens, ["gh-pat", "grafana-prod", "sentry-staging"]);
  assert.strictEqual(t.box.asked.length, 0, "no host confirm: the tier did not change");
  assert.strictEqual(t.box.docs["aws-ops"].runtime, "box", "what the server keeps");
});

test("every block's save writes the tier the page shows, and keeps the keys no block owns", async () => {
  const save = {
    agent: (t) => { clickBtn(blockOf(t, "agent"), "Edit"); pickModel(t, "claude-sonnet-5"); },
    cloud: (t) => { clickBtn(blockOf(t, "cloud"), "Edit"); t.box.pfvAddRow("cloud", { kind: "token", name: "gh-pat" }); },
    reachable: (t) => { clickBtn(blockOf(t, "reachable"), "Edit"); t.box.pfvAddRow("reachable", { kind: "domain" }); typeInto(field(t, "domain:2"), "extra.example.net"); },
  };
  for (const id of Object.keys(save)) {
    const t = noTier(cloudPage(), "aws-ops");
    t.box.docs["aws-ops"].replyable = true;
    await t.box.pfvShow("aws-ops");
    save[id](t);
    await saveBlock(t, id);
    assert.strictEqual(t.box.puts[0].runtime, "box", id + " save");
    assert.strictEqual(t.box.puts[0].replyable, true, id + " save keeps a key no block of it owns");
    assert.deepStrictEqual(t.box.puts[0].github, CLOUD_DOC.github, id + " save keeps the GitHub binding");
  }
});

test("a profile that stores no runtime and resolves to host saves host, without the host confirm", async () => {
  // An instance whose default_runtime is host: the list view's runtime is host and so is the page.
  const t = page({ profiles: PROFILES.map((p) => (p.name === "infra-terraform" ? Object.assign({}, p, { runtime: "host" }) : p)) });
  t.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" };
  await t.box.pfvShow("infra-terraform");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "chip"), ["host"]);
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  await saveBlock(t, "agent");
  assert.strictEqual(t.box.puts[0].runtime, "host", "pinned to what the page showed, not handed back to the instance default");
  assert.strictEqual(t.box.asked.length, 0, "the tier did not change, so nothing to confirm");
});

test("with no box layer a runtime-less profile that holds nothing box-only stays blank (runtimeToSend); one that reads as host saves host", async () => {
  const box = page({ boxRuntime: "" });
  box.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" }; // names no tier, holds nothing a box alone accepts
  await box.box.pfvShow("infra-terraform");
  clickBtn(blockOf(box, "agent"), "Edit");
  pickModel(box, "claude-sonnet-5");
  await saveBlock(box, "agent");
  assert.ok(!("runtime" in box.box.puts[0]), "an Agent-only edit does not pin a tier the profile never named");
  const host = page({ boxRuntime: "", profiles: PROFILES.map((p) => (p.name === "infra-terraform" ? Object.assign({}, p, { runtime: "host" }) : p)) });
  host.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" };
  await host.box.pfvShow("infra-terraform");
  clickBtn(blockOf(host, "agent"), "Edit");
  pickModel(host, "claude-sonnet-5");
  await saveBlock(host, "agent");
  assert.strictEqual(host.box.puts[0].runtime, "host");
});

// #164: with no box layer, a profile that stores no tier reads "In a box" only because a blank resolves to box. The server refuses a
// token, connection, GitHub binding or egress without an explicit box, and a readable folder or a private copy only works in a box,
// so the save names it when the profile being sent holds one of them, whichever block is saved.
test("with no box layer a runtime-less profile that holds credentials saves an explicit box, whichever block is saved", async () => {
  const save = {
    cloud: (t) => { clickBtn(blockOf(t, "cloud"), "Edit"); t.box.pfvAddRow("cloud", { kind: "token", name: "gh-pat" }); },
    agent: (t) => { clickBtn(blockOf(t, "agent"), "Edit"); pickModel(t, "claude-sonnet-5"); },
  };
  for (const id of Object.keys(save)) {
    const t = noTier(cloudPage({ boxRuntime: "" }), "aws-ops");
    await t.box.pfvShow("aws-ops");
    save[id](t);
    await saveBlock(t, id);
    assert.strictEqual(t.box.puts[0].runtime, "box", id + " save: a blank would be refused");
    assert.strictEqual(t.box.asked.length, 0, "no host confirm: the tier the page shows did not change");
  }
});

test("with no box layer, adding a token to a runtime-less profile that held nothing names box in the same save", async () => {
  const t = page({ boxRuntime: "" });
  t.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" };
  await t.box.pfvShow("infra-terraform");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "token", name: "grafana-prod" }), true);
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(t.box.puts[0].tokens, ["grafana-prod"]);
  assert.strictEqual(t.box.puts[0].runtime, "box");
});

// The Runs in merge reads the whole profile, so it has to run after every other block's merge. The blocks are merged in the order they
// were opened, and a refused save opens a second block on top of the first (a repair block), so Runs in can be opened before Cloud.
test("Runs in opened before Cloud, or after it, still sees the token Cloud adds in the same save", async () => {
  for (const order of [["runs", "cloud"], ["cloud", "runs"]]) {
    const t = page({ boxRuntime: "" });
    t.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" };
    await t.box.pfvShow("infra-terraform");
    clickBtn(blockOf(t, order[0]), "Edit");
    t.box.pfvOpenRepair(order[1]);
    t.box.pfvRender();
    assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), order);
    assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "token", name: "grafana-prod" }), true);
    await saveBlock(t, "runs");
    const what = "opened " + order.join(" then ");
    assert.strictEqual(t.box.puts.length, 1, what);
    assert.deepStrictEqual(t.box.puts[0].tokens, ["grafana-prod"], what);
    assert.strictEqual(t.box.puts[0].runtime, "box", what);
  }
});

test("Runs in saved on its own, with Cloud open but unchanged, still names box for a profile that holds credentials", async () => {
  const t = noTier(cloudPage({ boxRuntime: "" }), "aws-ops");
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  t.box.pfvOpenRepair("cloud");
  t.box.pfvRender();
  await saveBlock(t, "runs");
  assert.strictEqual(t.box.puts[0].runtime, "box");
  assert.strictEqual(t.box.asked.length, 0);
});

test("a stored tier is sent back as it is by every block's save", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  await saveBlock(t, "agent");
  assert.strictEqual(t.box.puts[0].runtime, "box");
  const h = page();
  await h.box.pfvShow("runbook-host");
  clickBtn(blockOf(h, "agent"), "Edit");
  pickModel(h, "claude-sonnet-5");
  await saveBlock(h, "agent");
  assert.strictEqual(h.box.puts[0].runtime, "host");
  assert.strictEqual(h.box.asked.length, 0);
});

// The tier the page shows, and so the tier every save writes, is the STORED profile's when it names one; only a profile that
// stores none takes the resolved tier (the list row: the instance's default). The list row can be stale (a failed list refetch
// after a save, a refetch that lands before the profile does), and a stale row must never write a tier: that would change
// where the profile runs with no host confirm (spec 9, C3; R40).
const rowTier = (name, runtime) => PROFILES.map((p) => (p.name === name ? Object.assign({}, p, { runtime }) : p));

test("a list row that says host over a stored box: the page reads box, and a Cloud or Agent save writes box with no confirm", async () => {
  const t = cloudPage({ profiles: [PROFILES[0], cloudView({ runtime: "host" }), PROFILES[2], PROFILES[3]] });
  await t.box.pfvShow("aws-ops");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "chip"), ["box"]);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-main"), ["In a box"]);
  assert.ok(blockOf(t, "cloud"), "a box profile has its Cloud block");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "token", name: "gh-pat" }), true);
  await saveBlock(t, "cloud");
  assert.strictEqual(t.box.puts[0].runtime, "box");
  assert.strictEqual(t.box.asked.length, 0);
  assert.strictEqual(t.box.docs["aws-ops"].runtime, "box", "the profile stays where it ran");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  await saveBlock(t, "agent");
  assert.strictEqual(t.box.puts[1].runtime, "box");
  assert.strictEqual(t.box.asked.length, 0);
  assert.strictEqual(t.box.docs["aws-ops"].runtime, "box");
});

test("a list row that says box over a stored host: the page reads On this machine, and a save writes host", async () => {
  const t = page({ profiles: rowTier("runbook-host", "box") });
  await t.box.pfvShow("runbook-host");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "chip"), ["host"]);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-main"), ["On this machine: runs as you, with your files, your network and your credentials"]);
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-applies"), ["Applies to Box"], "a host profile shows its Cloud block, tagged");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  await saveBlock(t, "agent");
  assert.strictEqual(t.box.puts[0].runtime, "host", "not turned into a box profile by a stale row");
  assert.strictEqual(t.box.asked.length, 0);
  assert.strictEqual(t.box.docs["runbook-host"].runtime, "host");
});

test("a profile that stores no runtime takes the resolved tier, from the list row", async () => {
  for (const [row, want] of [["host", "host"], ["box", "box"]]) {
    const t = page({ profiles: rowTier("infra-terraform", row) });
    t.box.docs["infra-terraform"] = { name: "infra-terraform", bin: "claude", rev: "r2" };
    await t.box.pfvShow("infra-terraform");
    assert.deepStrictEqual(texts(t.els["pfv-in"], "chip"), [want]);
    clickBtn(blockOf(t, "agent"), "Edit");
    pickModel(t, "claude-sonnet-5");
    await saveBlock(t, "agent");
    assert.strictEqual(t.box.puts[0].runtime, want, "row " + row);
    assert.strictEqual(t.box.asked.length, 0);
  }
});

test("after a host switch whose list refetch gave back the old row, the page reads host and the next save keeps host", async () => {
  // The harness's loadProfiles leaves the list as it was: the row still says box after the switch (review B M11).
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  const host = field(t, "tier-host");
  host.checked = true;
  host.onchange();
  await clickBtn(blockOf(t, "runs"), "Save as host…");
  await settle();
  assert.strictEqual(t.box.puts[0].runtime, "host");
  assert.strictEqual(t.box.asked.length, 1, "the switch asked once");
  assert.strictEqual(t.box.profiles.find((p) => p.name === "aws-ops").runtime, "box", "the list row is stale");
  assert.deepStrictEqual(texts(t.els["pfv-in"], "chip"), ["host"], "the page reads the stored tier");
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-main"), ["On this machine: runs as you, with your files, your network and your credentials"]);
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-applies"), ["Applies to Box"], "the Cloud block of a host profile is tagged, not hidden");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  await saveBlock(t, "agent");
  assert.strictEqual(t.box.puts[1].runtime, "host");
  assert.strictEqual(t.box.asked.length, 1, "no second question, and no quiet change back to box");
  assert.strictEqual(t.box.docs["aws-ops"].runtime, "host");
});

test("Duplicate offers the stored tier of the source, not a stale row's", async () => {
  const t = cloudPage({ profiles: [PROFILES[0], cloudView({ runtime: "host" }), PROFILES[2], PROFILES[3]] });
  await t.box.pfvShow("aws-ops");
  await dupFromMenu(t);
  assert.strictEqual(field(t, "tier-box").checked, true);
  assert.strictEqual(field(t, "tier-host").checked, false);
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

test("advanced lists what is set, and the form opened from Edit holds it", async () => {
  // Nothing set: the helper line, and the Edit button opens the form all the same.
  const t = page();
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(texts(advOf(t), "pfv-advst")[0], "none set");
  assert.strictEqual(byCls(advOf(t), "pfv-fold").length, 0);
  // Something set: listed as the block's value, and the form opened from the Edit button holds it.
  const set = page();
  set.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { args: ["--verbose"], env: { LOG_LEVEL: "debug" } });
  await set.box.pfvShow("aws-ops");
  assert.strictEqual(texts(advOf(set), "pfv-advst")[0], "2 set");
  assert.strictEqual(byCls(advOf(set), "pfv-fold").length, 0, "nothing to open or close");
  assert.deepStrictEqual(texts(advOf(set), "pfv-dl"), ["Arguments--verboseEnvironmentLOG_LEVEL"]);
  clickBtn(advOf(set), "Edit");
  assert.ok(advOf(set).className.includes("pfv-editing"));
  assert.strictEqual(field(set, "args").value, "--verbose");
  assert.deepStrictEqual(envRowsOf(set).map((r) => [rowInputs(r)[0].value, rowInputs(r)[1].value]), [["LOG_LEVEL", "debug"]]);
  // The first setting saved from "none set" is listed at once, with no click.
  await openAdv(t, "aws-ops");
  typeArgs(t, "--verbose");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.strictEqual(texts(advOf(t), "pfv-advst")[0], "1 set");
  assert.deepStrictEqual(texts(advOf(t), "pfv-dl"), ["Arguments--verbose"]);
  assert.deepStrictEqual(t.box.puts[0].args, ["--verbose"]);
});

test("base: a box profile shows the one it has and a save keeps it; a host profile offers it in the mockup's words, the default stored as none", async () => {
  const t = page();
  await openAdv(t, "aws-ops", { base: "clean" });
  assert.strictEqual(field(t, "base").value, "clean");
  typeArgs(t, "--verbose");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.strictEqual(t.box.puts[0].base, "clean", "a save from a box profile keeps the base the profile has");
  assert.deepStrictEqual(t.box.puts[0].args, ["--verbose"]);
  // A host profile does offer it, in the mockup's words; the default is stored as no base.
  const h = page();
  await openAdv(h, "runbook-host", { base: "clean" });
  assert.deepStrictEqual(Array.from(field(h, "base").children).map((o) => o.textContent), ["Everything you have (inherit)", "Minimal, no secrets (clean)", "Your login shell (login)"]);
  assert.strictEqual(field(h, "base").value, "clean");
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
  // A host profile keeps the row for the box: shown while there is a box layer, and with bundling off it goes like any other.
  const host = page();
  await openAdv(host, "runbook-host", { imports_off: true });
  assert.ok(field(host, "imports_off"));
  const hostOff = page({ bundleImports: false });
  await openAdv(hostOff, "runbook-host", { imports_off: true });
  assert.strictEqual(field(hostOff, "imports_off"), undefined);
  const hostNoBox = page({ boxRuntime: "" });
  await openAdv(hostNoBox, "runbook-host", { imports_off: true });
  assert.strictEqual(field(hostNoBox, "imports_off"), undefined, "no box layer, nothing to give");
  // An account with no personal config says so in the row's own words.
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
  assert.deepStrictEqual(advLabels(t), ["Environment it starts with Applies to Host", "Extra command-line arguments", "Environment variables"]);
  assert.deepStrictEqual(texts(advOf(t), "pfv-check-t"), ["Bypass permissions", "Answer prompts from the sidebarSets the default in New session.", "Include files my CLAUDE.md pulls in with @Read-only copies in the box. Applies to Box"]);
  assert.strictEqual(field(t, "args").getAttribute("placeholder"), "--some-flag");
  assert.deepStrictEqual(texts(advOf(t), "pfv-hint"), ["One per line.", "None. The agent gets the box's minimal environment."]);
  assert.strictEqual(byText(advOf(t), "button", "+ Add variable").length + byText(advOf(t), "button", "+ Add secret").length, 2);
  assert.strictEqual(Node.focused, field(t, "base"), "focus goes to the first control");
  const h = page({ repliesEnabled: true });
  await openAdv(h, "runbook-host");
  assert.deepStrictEqual(advLabels(h), ["Environment it starts with Applies to Host", "Extra command-line arguments", "Environment variables"]);
  assert.deepStrictEqual(texts(advOf(h), "pfv-check-t"), ["Bypass permissions", "Answer prompts from the sidebarSets the default in New session.", "Include files my CLAUDE.md pulls in with @Read-only copies in the box. Applies to Box"]);
  assert.deepStrictEqual(texts(advOf(h), "pfv-hint"), ["One per line.", "None added on top of the starting environment."]);
  assert.strictEqual(Node.focused, field(h, "base"));
  assert.ok(advOf(h).className.includes("pfv-editing"));
  // Opened and not touched holds no change, and the model and effort stay in the Agent block.
  assert.ok(byCls(t.els["pfv-in"], "pfv-unsaved")[0].hidden);
  assert.strictEqual(field(t, "options.model"), undefined);
  assert.strictEqual(field(t, "options.effort"), undefined);
});

test("Advanced on a box profile draws Environment it starts with, tagged Applies to Host", async () => {
  const t = page();
  await openAdv(t, "aws-ops");
  const base = field(t, "base");
  assert.ok(base, "the base select is drawn on a box profile");
  const label = byCls(base.parent, "pfv-flabel")[0];
  assert.deepStrictEqual(texts(label, "pfv-applies"), ["Applies to Host"]);
  assert.ok(!texts(advOf(t), "pfv-hint").includes("Environment it starts with only applies on this machine; a box always starts minimal."));
});

test("an Advanced save keeps base on a box profile and writes the one picked", async () => {
  const t = page();
  await openAdv(t, "aws-ops", { base: "login" });
  assert.strictEqual(field(t, "base").value, "login");
  typeArgs(t, "--x");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.strictEqual(t.box.puts[0].base, "login", "the base stored while it was a host profile stays");
  assert.strictEqual(t.box.puts[0].runtime, "box");
  const u = page();
  await openAdv(u, "aws-ops", { base: "login" });
  const sel = field(u, "base");
  sel.value = "clean";
  sel.onchange();
  assert.strictEqual(await clickBtn(advOf(u), "Save"), true);
  assert.strictEqual(u.box.puts[0].base, "clean");
  assert.strictEqual(u.box.puts[0].runtime, "box");
});

test("Advanced on a host profile offers the CLAUDE.md imports row, tagged Applies to Box", async () => {
  const t = page({ metaLoaded: true, bundleImports: true });
  await openAdv(t, "runbook-host", { imports_off: true });
  const cb = field(t, "imports_off");
  assert.ok(cb, "the imports checkbox is drawn on a host profile");
  assert.strictEqual(cb.checked, false);
  assert.deepStrictEqual(texts(cb.parent, "pfv-applies"), ["Applies to Box"]);
  // Saved with something else changed: the stored imports_off stays.
  typeArgs(t, "--x");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), true);
  assert.strictEqual(t.box.puts[0].imports_off, true);
  assert.strictEqual(t.box.puts[0].runtime, "host");
});

test("Advanced: the other options are drawn the old form's way and only what the user set is written", async () => {
  const defs = OPTION_DEFS.concat([
    { key: "verbosity", label: "Verbosity", type: "enum", choices: ["quiet", "normal", "loud"], bins: ["claude"] },
    { key: "profile_pack", label: "Pack", type: "enum", choices: ["a", "b", "c", "d", "e"], bins: ["claude"] },
    { key: "shell_only", label: "Shell only", type: "bool", bins: ["bash"] },
  ]);
  const t = page({ optionDefs: defs });
  await openAdv(t, "aws-ops", { options: { model: "claude-opus-5-5", effort: "high", legacy_key: "keep me" } });
  assert.deepStrictEqual(advLabels(t).slice(1, 3), ["Verbosity", "Pack"]);
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
// What a forced close kept (pfvKept), as plain data so it compares across the sandbox's realm; null when nothing is.
const keptOf = (t) => { const k = t.run("pfvKept"); return k === null ? null : JSON.parse(JSON.stringify(k)); };

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
  assert.ok(byText(blockOf(t, "runs"), "button", "Save as host…")[0].className.includes("btn-primary"), "still the one primary of the view, restyled red on clear");
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
  const save = byCls(blockOf(t, "runs"), "btn-primary")[0];
  assert.strictEqual(save.hidden, true, "Save is not offered");
  assert.strictEqual(await t.box.pfvSaveBlock("runs"), false);
  assert.strictEqual(asked.length, 0);
  assert.strictEqual(t.box.puts.length, 0);
  assert.deepStrictEqual(texts(blockOf(t, "runs"), "pfv-ferr"), [sentence]);
  // Choosing the box again brings Save back, with its own name.
  const box = field(t, "tier-box"); box.checked = true; box.onchange();
  assert.strictEqual(byCls(blockOf(t, "runs"), "btn-primary")[0].hidden, false);
  assert.strictEqual(byCls(blockOf(t, "runs"), "pfv-refuse").length, 0);
});

test("Switch to box opens Runs in with Box chosen and the line for it; nothing is restored or saved until Save, then + Add is offered", async () => {
  const t = page();
  const asked = recordAsks(t, true);
  t.box.loadProfiles = async () => { t.box.profiles = t.box.profiles.map((p) => (p.name === "runbook-host" ? Object.assign({}, p, { runtime: "box" }) : p)); return true; };
  await t.box.pfvShow("runbook-host");
  assert.strictEqual(byText(t.els["pfv-in"], "button", "+ Add access").length + byText(t.els["pfv-in"], "button", "+ Add folder or domain").length, 2, "a host profile offers + Add too: its Cloud and Also reachable are tagged, not hidden");
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
  assert.deepStrictEqual(texts(t.els["pfv-in"], "chip"), ["box"]);
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
  assert.deepStrictEqual(titles(t.els["pfv-in"]), ["infra-terraform"]);
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

test("a way out that is not the page's own (the old New profile button) asks first through pfvHold", async () => {
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
  // The state is the layers: the "+ Add" choice is open while its block's rec.ui.picker is; the drawer is a flag.
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(t.run("pfvState").picker, "cloud");
  assert.strictEqual(t.box.pfvEscTarget(), "picker");
  t.run("pfvState").drawer = true;
  assert.strictEqual(t.box.pfvEscTarget(), "drawer", "the drawer hangs over the page and its pickers");
  t.els["ask-modal"] = { open: true };
  assert.strictEqual(t.box.pfvEscTarget(), "confirm", "a question over everything");
  t.els["ask-modal"].open = false;
  assert.strictEqual(t.box.pfvEscTarget(), "drawer");
  t.run("pfvState").drawer = false;
  assert.strictEqual(t.box.pfvEscTarget(), "picker", "the picker is still open under the drawer");
  t.run("pfvState").edits.cloud.ui.picker = false;
  assert.strictEqual(t.run("pfvState").picker, "");
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
  assert.strictEqual(t.run("pfvState").picker, "reachable", "the picker sets the flag");
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

test("the Esc layer for a picker is read from the open blocks, so it cannot disagree with them", async () => {
  const t = cloudPage();
  const asked = recordAsks(t, false);
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  // A second block is open (a refused save opens one on top of the first) and its picker is open too.
  t.box.pfvOpenRepair("cloud");
  t.box.pfvRender();
  assert.strictEqual(t.box.pfvOpenPicker("cloud"), true);
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-pick").length, 2, "two choices are open");
  // Closing one by its own link leaves the other open, and the layer says so (a single flag would have said none).
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-pick").length, 1);
  assert.strictEqual(t.run("pfvState").picker, "reachable");
  assert.strictEqual(t.box.pfvEscTarget(), "picker");
  // Esc does not go to the page while any is open, and closes whatever is left.
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-pick").length, 0);
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  // Both open again: one Esc closes both, and the page still stays for it.
  t.box.pfvOpenPicker("reachable");
  t.box.pfvOpenPicker("cloud");
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  // A block that goes takes its picker with it: nothing is left claiming the layer.
  t.box.pfvOpenPicker("reachable");
  assert.strictEqual(t.box.pfvEscTarget(), "picker");
  clickBtn(blockOf(t, "reachable"), "Cancel");
  assert.strictEqual(t.run("pfvState").picker, "");
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  assert.strictEqual(asked.length, 0);
  // Dropping every edit does the same, and the page's leave answers for the page again.
  t.box.pfvOpenPicker("cloud");
  t.box.pfvDropEdits();
  assert.strictEqual(t.box.pfvEscTarget(), "page");
});

test("a forced close (the window crossed to a phone) ends the edit through pfvDropEdits without asking, and keeps what was typed", async () => {
  const t = page();
  const asked = recordAsks(t, false);
  const picker = await openWs(t);
  picker.pick("/home/you/other-folder");
  assert.strictEqual(t.run("pfvState").pending, true);
  assert.strictEqual(t.box.pageLeaveHook("force"), true);
  assert.strictEqual(asked.length, 0, "never asked");
  assert.strictEqual(picker.destroyed, true, "the folder picker's listeners are released");
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), [], "the live state has no edit left");
  assert.deepStrictEqual([t.run("pfvState").pending, t.run("pfvState").openBlock], [false, ""]);
  // What was typed is kept, beside the page: the profile, the profile as the edit was made against, the block that was open, and per
  // block only what redraws the typed form. Nothing of the page it came from (nodes, listeners, pickers, refusals).
  const k = keptOf(t);
  assert.deepStrictEqual([k.name, k.doc.rev, k.openBlock], ["aws-ops", "r1", "workspace"]);
  assert.deepStrictEqual(k.edits.map((e) => e.id), ["workspace"]);
  assert.deepStrictEqual(Object.keys(k.edits[0]).sort(), ["id", "init", "repair", "ui", "values"]);
  assert.strictEqual(k.edits[0].values.folder, "/home/you/other-folder");
  assert.strictEqual(k.edits[0].repair, false);
  // So reopening Settings on another tab and pressing Esc asks nothing about a profile that is not on screen.
  assert.strictEqual(t.box.pageLeaveHook("esc"), true);
  assert.strictEqual(t.box.pfvTabLeave("creds"), true);
  assert.strictEqual(asked.length, 0);
  // The read view is what comes back.
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0);
  // On a phone's modal the hook is inert for "force" too: it drops nothing and keeps nothing.
  const m = page();
  await dirtyAgent(m);
  m.els["settings-modal"].classList.remove("as-page");
  assert.strictEqual(m.box.pageLeaveHook("force"), true);
  assert.strictEqual(m.run("pfvState").pending, true, "it drops nothing there");
  assert.deepStrictEqual(Object.keys(m.run("pfvState").edits), ["agent"]);
  assert.strictEqual(keptOf(m), null, "and keeps nothing");
});

// --- R43 (b): a forced close keeps what was typed, for when that profile opens on the desktop page again -------------------

test("a kept edit comes back when that profile opens on the desktop page again: typed values, dirty, in edit mode, asking before it is left", async () => {
  const t = page();
  const asked = recordAsks(t, false);
  await dirtyAgent(t);
  t.box.pageLeaveHook("force");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0, "the page closed as the read view");
  // Reopening Settings > Profiles shows the profile it was on: pfvShow() with no name.
  await t.box.pfvShow();
  const st = t.run("pfvState");
  assert.strictEqual(st.name, "aws-ops");
  assert.deepStrictEqual(Object.keys(st.edits), ["agent"]);
  assert.deepStrictEqual([st.openBlock, st.pending], ["agent", true]);
  assert.ok(blockOf(t, "agent").className.includes("pfv-editing"), "the block is a form again");
  assert.strictEqual(field(t, "options.model").value, "claude-sonnet-5", "with exactly what was typed");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-st-dirty"), ["Unsaved changes"], "the block says so");
  assert.strictEqual(unsavedNote(t).hidden, false, "and so does the header");
  assert.strictEqual(byText(blockOf(t, "agent"), "button", "Discard").length, 1);
  assert.strictEqual(t.run("pfvKept"), null, "it came back once: nothing is kept any more");
  assert.strictEqual(t.box.puts.length, 0, "coming back writes nothing");
  // It is an unsaved edit like any other: leaving asks.
  assert.strictEqual(await t.box.pfvLeaveGuard("leave"), false);
  assert.strictEqual(asked.length, 1);
  assert.strictEqual(asked[0].title, "Discard unsaved changes?");
  // A second forced close keeps it again, and a Discard on the restored edit ends it for good.
  t.box.pageLeaveHook("force");
  assert.strictEqual(keptOf(t).edits[0].values.options.model, "claude-sonnet-5");
  await t.box.pfvShow();
  recordAsks(t, true);
  assert.strictEqual(await t.box.pfvLeaveGuard("leave"), true);
  assert.strictEqual(t.run("pfvKept"), null);
  await t.box.pfvShow();
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), [], "discarded: the next open is the read view");
});

test("a kept edit is saved against the profile it was typed on, so a profile changed elsewhere meanwhile refuses it as stale", async () => {
  const t = page();
  await dirtyAgent(t);
  t.box.pageLeaveHook("force");
  // Somebody saved the profile while Settings was closed.
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], { rev: "r9", options: { model: "claude-opus-5", effort: "high" } });
  await t.box.pfvShow();
  assert.strictEqual(t.run("pfvState").doc.rev, "r1", "the page holds the profile the edit was made against, not a copy newer than it");
  t.box.putFail = apiErr(409, { error: "the profile changed", code: "profile_changed" });
  assert.strictEqual(await clickBtn(blockOf(t, "agent"), "Save"), false);
  assert.strictEqual(t.box.puts.length, 1);
  assert.strictEqual(t.box.puts[0].rev, "r1", "the save carries the revision the edit began at");
  assert.strictEqual(t.box.puts[0].options.model, "claude-sonnet-5");
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-top-err"), ["This profile changed elsewhere Reload"], "the existing stale path, never a silent overwrite");
  await clickBtn(blockOf(t, "agent"), "Reload");
  assert.strictEqual(t.run("pfvState").doc.rev, "r9");
  assert.strictEqual(t.run("pfvState").pending, false);
  // Unchanged elsewhere, the same restore saves.
  const u = page();
  await dirtyAgent(u);
  u.box.pageLeaveHook("force");
  await u.box.pfvShow();
  assert.strictEqual(await clickBtn(blockOf(u, "agent"), "Save"), true);
  assert.strictEqual(u.box.puts[0].rev, "r1");
  assert.strictEqual(u.box.puts[0].options.model, "claude-sonnet-5");
  assert.strictEqual(u.run("pfvState").pending, false);
});

test("a clean open block is not kept; neither is anything when the page was never edited", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  t.box.pageLeaveHook("force");
  assert.strictEqual(keptOf(t), null, "no block open");
  const u = page();
  await openWs(u);
  assert.strictEqual(u.run("pfvState").pending, false);
  u.box.pageLeaveHook("force");
  assert.strictEqual(keptOf(u), null, "an open editor that holds no change is dropped as before");
  await u.box.pfvShow();
  assert.deepStrictEqual(Object.keys(u.run("pfvState").edits), []);
  // A change typed and typed back is no change either.
  const w = page();
  await openWs(w);
  lastPicker(w).pick("/home/you/other-folder");
  lastPicker(w).pick("/home/you/work/payments-api");
  assert.strictEqual(w.run("pfvState").pending, false);
  w.box.pageLeaveHook("force");
  assert.strictEqual(keptOf(w), null);
});

test("a nav click on the profile the edit was kept for brings it back too; another profile does not, and the edit waits for its own", async () => {
  const t = page();
  recordAsks(t, true);
  await dirtyAgent(t);
  t.box.pageLeaveHook("force");
  t.box.pfvRenderNav();
  // Another profile first: shown as it is, the kept edit untouched.
  nav(t, "runbook-host").click();
  await settle();
  await settle();
  assert.strictEqual(t.run("pfvState").name, "runbook-host");
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0);
  assert.deepStrictEqual(titles(t.els["pfv-in"]), ["runbook-host"]);
  assert.strictEqual(keptOf(t).name, "aws-ops", "still kept");
  // Reopening Settings shows runbook-host (the one on screen): that does not restore aws-ops's edit onto it.
  await t.box.pfvShow();
  assert.strictEqual(t.run("pfvState").name, "runbook-host");
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
  assert.strictEqual(keptOf(t).name, "aws-ops");
  // Its own nav row: the edit is back.
  nav(t, "aws-ops").click();
  await settle();
  await settle();
  const st = t.run("pfvState");
  assert.strictEqual(st.name, "aws-ops");
  assert.deepStrictEqual([st.openBlock, st.pending], ["agent", true]);
  assert.strictEqual(field(t, "options.model").value, "claude-sonnet-5");
  assert.strictEqual(st.doc.rev, "r1");
  assert.strictEqual(t.run("pfvKept"), null);
});

test("a kept edit of a profile that is gone is dropped; a read that fails for another reason keeps it for the next open", async () => {
  const t = page();
  await dirtyAgent(t);
  t.box.pageLeaveHook("force");
  t.box.getFail = apiErr(404, { error: "no such profile" });
  await t.box.pfvShow();
  assert.strictEqual(t.run("pfvKept"), null, "the profile is gone: what was typed has nowhere to go");
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
  t.box.getFail = null;
  await t.box.pfvShow();
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), [], "and it does not come back later");
  // The list has stopped naming it (deleted elsewhere, and the page's list knows): dropped too, and the next profile shows.
  const v = page();
  await dirtyAgent(v);
  v.box.pageLeaveHook("force");
  v.box.profiles = v.box.profiles.filter((p) => p.name !== "aws-ops");
  await v.box.pfvShow();
  assert.strictEqual(v.run("pfvKept"), null);
  assert.notStrictEqual(v.run("pfvState").name, "aws-ops");
  assert.deepStrictEqual(Object.keys(v.run("pfvState").edits), []);
  const u = page();
  await dirtyAgent(u);
  u.box.pageLeaveHook("force");
  u.box.getFail = new Error("offline");
  await u.box.pfvShow();
  assert.strictEqual(keptOf(u).name, "aws-ops", "an offline read says nothing about the profile");
  assert.deepStrictEqual(Object.keys(u.run("pfvState").edits), []);
  u.box.getFail = null;
  await u.box.pfvShow();
  assert.deepStrictEqual(Object.keys(u.run("pfvState").edits), ["agent"]);
  assert.strictEqual(field(u, "options.model").value, "claude-sonnet-5");
});

test("every open block is kept in the order it was opened, the open one still open, with pending rows and removals", async () => {
  const t = pickPage({ awscEntries: PICK_AWS.filter((e) => e.name !== "staging-read") });
  await t.box.pfvShow("aws-ops");
  // The Remove link of a row the instance no longer defines: the block opens with that row removed (pending); then a token is added.
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(t.run("pfvState").picker, "cloud");
  optBtn(t, "cloud", "pagerduty").click();
  // A refused save opens another block on top: it holds no change itself.
  t.box.pfvOpenRepair("reachable");
  t.box.pfvRender();
  t.run("pfvState").openBlock = "cloud";
  t.box.pageLeaveHook("force");
  const k = keptOf(t);
  assert.deepStrictEqual(k.edits.map((e) => e.id), ["cloud", "reachable"]);
  assert.deepStrictEqual(k.edits.map((e) => e.repair), [false, true]);
  assert.strictEqual(k.openBlock, "cloud");
  assert.strictEqual(k.edits[0].values.aws.find((e) => e.name === "staging-read").removed, true);
  assert.strictEqual(k.edits[0].values.tokens.find((e) => e.name === "pagerduty").fresh, true);
  assert.ok(k.edits[0].ui.picker === undefined && k.edits[0].ui.pickNodes === undefined, "no picker, no node");
  await t.box.pfvShow();
  const st = t.run("pfvState");
  assert.deepStrictEqual(Object.keys(st.edits), ["cloud", "reachable"]);
  assert.strictEqual(st.openBlock, "cloud");
  assert.strictEqual(st.pending, true);
  assert.ok(rowFor(t, "cloud", "AWS", "staging-read").className.includes("pfv-removed"), "the removal is still pending");
  assert.ok(rowFor(t, "cloud", "Token", "pagerduty"), "and so is the added token");
  assert.ok(blockOf(t, "reachable").className.includes("pfv-editing"));
  assert.strictEqual(st.picker, "", "an open picker is not brought back");
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  assert.strictEqual(t.box.puts.length, 0);
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  assert.deepStrictEqual(t.box.puts[0].aws_connections, ["prod-read"], "and it saves as typed");
  assert.ok(t.box.puts[0].tokens.includes("pagerduty"));
});

test("the typed text of each editor outlives a forced close: the other command, the copy's fields, the permission rows", async () => {
  // Agent: Other command... typed "mycmd", then claude again: the text waits behind the select for the way back.
  const a = page();
  await dirtyAgent(a);
  pickBin(a, "__other__");
  typeInto(field(a, "bin-text"), "mycmd");
  pickBin(a, "claude");
  assert.strictEqual(a.run("pfvState").edits.agent.values.bin, "claude");
  a.box.pageLeaveHook("force");
  assert.deepStrictEqual(keptOf(a).edits[0].ui, { binOther: false, otherText: "mycmd" });
  await a.box.pfvShow();
  assert.strictEqual(field(a, "bin").value, "claude");
  pickBin(a, "__other__");
  assert.strictEqual(field(a, "bin-text").value, "mycmd", "what was typed in Other is still there");
  // Workspace: the copy's fields stashed while the block works in the folder, what the copy fields were opened with (so a copy and
  // back is no change), where the suggested copy folder is followed from, and text a refused Save left in the folder field.
  const w = page();
  const pk = await openWs(w);
  pickMode(w, "copy");
  typeInto(field(w, "copy-dir"), "/home/you/box-clones/mine");
  typeInto(field(w, "share"), "README.md");
  pickMode(w, "folder");
  pk.pick("/home/you/other-folder");
  const half = lastPicker(w).opts.input;
  half.value = "half/typed";
  assert.strictEqual(await saveBlock(w, "workspace"), false, "the folder field shows text that is not the folder");
  assert.strictEqual(w.run("pfvState").edits.workspace.ui.typed, "half/typed");
  w.box.pageLeaveHook("force");
  const ws = keptOf(w).edits[0].ui;
  assert.deepStrictEqual(Object.keys(ws).sort(), ["autoDir", "copyInit", "copyStash", "typed"], "and no node, picker or input of the form");
  assert.strictEqual(ws.copyStash.copyDir, "/home/you/box-clones/mine");
  assert.strictEqual(ws.copyStash.shareText, "README.md");
  await w.box.pfvShow();
  assert.strictEqual(field(w, "folder").value, "half/typed", "the refused text is still in the field");
  assert.strictEqual(w.run("pfvState").edits.workspace.values.folder, "/home/you/other-folder");
  pickMode(w, "copy");
  assert.strictEqual(field(w, "copy-dir").value, "/home/you/box-clones/mine", "the copy fields come back from the stash");
  assert.strictEqual(field(w, "share").value, "README.md");
  pickMode(w, "folder");
  assert.strictEqual(w.run("pfvState").edits.workspace.values.copyDir, "", "and the way back is the profile's own, as opened");
  // Cloud: the permission rows that were folded open stay open.
  const c = cloudPage();
  await c.box.pfvShow("aws-ops");
  clickBtn(blockOf(c, "cloud"), "Edit");
  byText(blockOf(c, "cloud"), "button", "Customise")[0].click();
  tick(c, "gh:perm:contents", false);
  assert.strictEqual(c.run("pfvState").pending, true);
  c.box.pageLeaveHook("force");
  assert.deepStrictEqual(keptOf(c).edits[0].ui, { customise: true });
  await c.box.pfvShow();
  assert.strictEqual(field(c, "gh:perm:contents").checked, false, "the row is drawn, as it was left");
  assert.strictEqual(c.run("pfvState").edits.cloud.values.perms.contents, "off");
});

test("a restore does not land on a page that has closed again: the edit stays kept for the next desktop open", async () => {
  const t = page();
  const picker = await openWs(t);
  picker.pick("/home/you/other-folder");
  t.box.pageLeaveHook("force");
  assert.strictEqual(t.box.pickers.length, 1);
  // Settings opens again and its read is out; the window crosses to a phone before it lands.
  let release;
  const real = t.box.api;
  t.box.api = async (p, o) => { if (p === "/api/profiles/aws-ops" && !(o && o.method)) await new Promise((r) => { release = r; }); return real(p, o); };
  const showing = t.box.pfvShow();
  await settle();
  t.box.pageLeaveHook("force");
  t.els["settings-modal"].classList.remove("as-page");
  release();
  await showing;
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), [], "no form was drawn on the closed page");
  assert.strictEqual(t.box.pickers.length, 1, "so no folder picker took listeners on it");
  assert.strictEqual(keptOf(t).name, "aws-ops", "and the edit was not used up");
  // The next time the desktop page is open it comes back.
  t.box.api = real;
  t.els["settings-modal"].classList.add("as-page");
  await t.box.pfvShow();
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), ["workspace"]);
  assert.strictEqual(field(t, "folder").value, "/home/you/other-folder");
  assert.strictEqual(t.run("pfvKept"), null);
});

test("what is restored is a copy: the dropped form's own objects cannot change it afterwards", async () => {
  const t = page();
  const pk = await openWs(t);
  pickMode(t, "copy");
  typeInto(field(t, "copy-dir"), "/home/you/box-clones/mine");
  pickMode(t, "folder");
  pk.pick("/home/you/other-folder");
  // The objects the form held, as a late callback of the dropped form could still reach them.
  const rec = t.run("pfvState").edits.workspace;
  const [values, stash, init] = [rec.values, rec.ui.copyStash, rec.ui.copyInit];
  assert.ok(stash && init, "the editor has both to keep");
  t.box.pageLeaveHook("force");
  values.folder = "/tampered";
  values.shareText = "tampered";
  stash.copyDir = "/tampered";
  init.copyDir = "/tampered";
  await t.box.pfvShow();
  const back = t.run("pfvState").edits.workspace;
  assert.strictEqual(back.values.folder, "/home/you/other-folder");
  assert.strictEqual(back.values.shareText, "");
  assert.strictEqual(back.ui.copyStash.copyDir, "/home/you/box-clones/mine");
  assert.strictEqual(back.ui.copyInit.copyDir, "");
  assert.notStrictEqual(back.values, values);
});

test("the New profile page is not kept by a forced close: its record is the page", async () => {
  const t = await newPage();
  typeName(t, "scratch");
  assert.strictEqual(t.run("pfvState").page, "new");
  t.box.pageLeaveHook("force");
  assert.strictEqual(t.run("pfvKept"), null);
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
  assert.strictEqual(t.run("pfvState").page, "");
  await t.box.pfvShow();
  assert.strictEqual(t.run("pfvState").page, "");
  assert.strictEqual(byCls(t.els["pfv-in"], "pfv-editing").length, 0);
});

test("an edit whose save goes through while the window crosses to a phone does not come back", async () => {
  const t = page();
  await dirtyAgent(t);
  let release;
  const real = t.box.api;
  t.box.api = async (p, o) => { if (o && o.method === "PUT") await new Promise((r) => { release = r; }); return real(p, o); };
  const saving = clickBtn(blockOf(t, "agent"), "Save");
  await settle();
  t.box.pageLeaveHook("force");
  assert.strictEqual(keptOf(t).name, "aws-ops", "kept while its fate is unknown");
  release();
  assert.strictEqual(await saving, true);
  assert.strictEqual(t.box.puts.length, 1);
  assert.strictEqual(t.run("pfvKept"), null, "it was written: it must not return as an unsaved edit");
  await t.box.pfvShow();
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
});

// --- Task 11 slice gate: the edit blocks as the mockup draws them -------------------------------------------------------

test("GitHub permissions are listed writable first, in the mockup's order; Repos is a one-line input", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.strictEqual(field(t, "gh:0:repos").tag, "input", "one line, not a textarea");
  assert.strictEqual(field(t, "gh:0:repos").getAttribute("placeholder"), "payments-api, runbooks");
  clickBtn(blockOf(t, "cloud"), "Customise");
  const rows = byCls(blockOf(t, "cloud"), "pfv-perm");
  assert.deepStrictEqual(rows.map((r) => texts(r, "pfv-check-t")[0]), ["contents", "pull requests", "issues", "metadata", "actions", "checks", "statuses"]);
  assert.deepStrictEqual(rows.map((r) => byCls(r, "pfv-seg").length), [1, 1, 1, 0, 0, 0, 0], "the three that can be written come first, each with its Read/Write control");
  // A comma list is still what is sent.
  typeInto(field(t, "gh:0:repos"), "payments-api, runbooks");
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  assert.strictEqual(await saveBlock(t, "cloud"), true);
  assert.deepStrictEqual(t.box.puts[0].github.owners[0].repos, ["payments-api", "runbooks"]);
});

test("Effort not set says who decides, until one is lit", async () => {
  const t = page();
  await t.box.pfvShow("infra-terraform");
  clickBtn(blockOf(t, "agent"), "Edit");
  const hints = () => texts(blockOf(t, "agent"), "pfv-hint");
  assert.ok(hints().includes("Not set: Claude Code decides."), "no effort stored: the line is under the control");
  const lit = field(t, "options.effort");
  assert.strictEqual(lit.parent.children[lit.parent.children.indexOf(lit) + 1].textContent, "Not set: Claude Code decides.", "right under it");
  lit.children.find((b) => b.dataset.segvalue === "high").click();
  assert.ok(!hints().includes("Not set: Claude Code decides."), "gone once an effort is picked");
  // A profile that has one never shows it.
  const set = page();
  await set.box.pfvShow("aws-ops");
  clickBtn(blockOf(set, "agent"), "Edit");
  assert.ok(!texts(blockOf(set, "agent"), "pfv-hint").includes("Not set: Claude Code decides."));
  // The Manage link stays beside the login hint.
  assert.deepStrictEqual(byText(blockOf(set, "agent"), "button", "Manage").length, 1);
});

test("Advanced keeps its label while editing: the name and what is set under it, no chevron, the form beside it", async () => {
  for (const [doc, word] of [[{}, "none set"], [{ args: ["--verbose"], env: { LOG_LEVEL: "debug" } }, "2 set"]]) {
    const t = page();
    await openAdv(t, "aws-ops", doc);
    const adv = advOf(t);
    assert.ok(adv.className.includes("pfv-editing"));
    const label = byCls(adv, "pfv-label")[0];
    assert.strictEqual(labelName(label), "Advanced");
    assert.strictEqual(texts(label, "pfv-advst")[0], word, "the count is under the name, as in the read view");
    assert.strictEqual(label.className, "pfv-label", "the same label as every block");
    assert.strictEqual(adv.all((n) => n.tag === "svg").length, 0, "no chevron: nothing folds");
    assert.ok(!adv.className.includes("pfv-open"));
    assert.strictEqual(byCls(adv, "pfv-fold").length, 0);
    assert.ok(byCls(adv, "pfv-edit-body")[0].className.includes("pfv-adv-body"));
  }
  // The other blocks keep their plain label.
  const o = page();
  await o.box.pfvShow("aws-ops");
  clickBtn(blockOf(o, "agent"), "Edit");
  assert.strictEqual(byCls(blockOf(o, "agent"), "pfv-label")[0].className, "pfv-label");
});

test("a failed save focuses the first invalid field and marks it aria-invalid, pointing at its words; fixing it clears both", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "reachable"), "+ Add folder or domain");
  optBtn(t, "reachable", "Folder").click();
  const input = () => field(t, "folder:1");
  typeInto(input(), "home/you/runbooks");
  assert.strictEqual(input().getAttribute("aria-invalid"), null, "nothing is wrong until Save says so");
  assert.strictEqual(await saveBlock(t, "reachable"), false);
  assert.strictEqual(Node.focused, input(), "focus is on the field, not on Save");
  assert.strictEqual(input().getAttribute("aria-invalid"), "true");
  const words = byCls(blockOf(t, "reachable"), "pfv-ferr")[0];
  assert.ok(words.id, "the words have an id");
  assert.strictEqual(input().getAttribute("aria-describedby"), words.id);
  typeInto(input(), "/home/you/runbooks");
  assert.strictEqual(input().getAttribute("aria-invalid"), null);
  assert.strictEqual(input().getAttribute("aria-describedby"), null);
  assert.strictEqual(await saveBlock(t, "reachable"), true);
});

test("a refusal from the server focuses its field too: an input, a segmented control, a stored row's Remove", async () => {
  // Workspace: an input under the picker; the focus must not open the folder dropdown.
  const w = page();
  await openWs(w);
  w.box.putFail = apiErr(400, { error: 'profile "aws-ops": default working dir cannot be contained: it is your home', field: "default_cwd" });
  lastPicker(w).pick("/home/you");
  assert.strictEqual(await clickBtn(blockOf(w, "workspace"), "Save"), false);
  assert.strictEqual(Node.focused, field(w, "folder"));
  assert.strictEqual(field(w, "folder").getAttribute("aria-invalid"), "true");
  assert.strictEqual(field(w, "folder").getAttribute("aria-describedby"), byCls(blockOf(w, "workspace"), "pfv-ferr")[0].id);
  assert.strictEqual(lastPicker(w).opened, 0, "a focus the page moved is not a request for the dropdown");
  // Agent: a segmented control is not focusable, its lit button is.
  const a = page();
  await a.box.pfvShow("aws-ops");
  clickBtn(blockOf(a, "agent"), "Edit");
  pickModel(a, "claude-sonnet-5");
  a.box.putFail = apiErr(400, { error: 'profile "aws-ops": options.effort: "high" is not one of the allowed values', field: "options.effort" });
  assert.strictEqual(await clickBtn(blockOf(a, "agent"), "Save"), false);
  const seg = field(a, "options.effort");
  assert.strictEqual(Node.focused.textContent, "high");
  assert.ok(seg.contains(Node.focused), "the lit button of the control the refusal is under");
  assert.strictEqual(seg.getAttribute("aria-invalid"), "true");
  // A refusal that names a stored row (a folder that is gone) has no input: the row's Remove is read with it and focused.
  const r = cloudPage();
  await r.box.pfvShow("aws-ops");
  clickBtn(blockOf(r, "reachable"), "Edit");
  clickBtn(blockOf(r, "reachable"), "+ Add folder or domain");
  optBtn(r, "reachable", "Domain").click();
  typeInto(field(r, "domain:2"), "ok.example.com");
  r.box.putFail = gone400();
  assert.strictEqual(await saveBlock(r, "reachable"), false);
  const row = rowFor(r, "reachable", "Folder", "/home/you/other-repo/docs");
  const rm = byText(row, "button", "Remove")[0];
  assert.strictEqual(Node.focused, rm);
  assert.strictEqual(rm.getAttribute("aria-describedby"), byCls(row, "pfv-ferr")[0].id);
  assert.strictEqual(rm.getAttribute("aria-invalid"), null, "a button is not an invalid field");
});

test("a variable's refusal focuses the row that is wrong, not the first row, and typing clears it", async () => {
  const t = page();
  await openAdv(t, "aws-ops");
  clickBtn(advOf(t), "+ Add variable");
  typeRow(envRowsOf(t)[0], "OK_ONE", "1");
  clickBtn(advOf(t), "+ Add variable");
  typeRow(envRowsOf(t)[1], "1BAD", "x");
  assert.strictEqual(await clickBtn(advOf(t), "Save"), false);
  const bad = () => rowName(envRowsOf(t)[1]);
  assert.strictEqual(Node.focused, bad(), "the second row is the one that is wrong");
  assert.strictEqual(bad().getAttribute("aria-invalid"), "true");
  assert.strictEqual(bad().getAttribute("aria-describedby"), byCls(advOf(t), "pfv-ferr")[0].id);
  assert.strictEqual(rowName(envRowsOf(t)[0]).getAttribute("aria-invalid"), null);
  typeRow(envRowsOf(t)[1], "GOOD", "x");
  assert.strictEqual(bad().getAttribute("aria-invalid"), null);
  assert.strictEqual(bad().getAttribute("aria-describedby"), null);
});

// --- Task 11 gate fix, round 1: a refusal about the permission set puts focus on a control, never on a div ---------------

const untickAll = (t) => { for (const k of ["actions", "checks", "contents", "pull_requests", "issues", "metadata", "statuses"]) if (field(t, "gh:perm:" + k)) tick(t, "gh:perm:" + k, false); };
const SET_WORDS = "GitHub: tick at least one permission, or the agent gets none.";

test("an empty GitHub permission set focuses the first permission checkbox", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  clickBtn(blockOf(t, "cloud"), "Customise");
  untickAll(t);
  assert.strictEqual(await saveBlock(t, "cloud"), false);
  assert.strictEqual(t.box.puts.length, 0);
  const words = byCls(blockOf(t, "cloud"), "pfv-ferr")[0];
  assert.strictEqual(words.textContent, SET_WORDS);
  const first = field(t, "gh:perm:contents");
  assert.strictEqual(Node.focused, first, "focus is on the first permission, in the order the list shows");
  assert.strictEqual(first.getAttribute("aria-invalid"), "true");
  assert.strictEqual(first.getAttribute("aria-describedby"), words.id);
  const group = byCls(blockOf(t, "cloud"), "pfv-perms")[0];
  assert.strictEqual(group.getAttribute("aria-invalid"), null, "the div is not the field: the checkbox that holds the focus is");
  tick(t, "gh:perm:contents", true);
  assert.strictEqual(field(t, "gh:perm:contents").getAttribute("aria-invalid"), null);
  assert.strictEqual(field(t, "gh:perm:contents").getAttribute("aria-describedby"), null);
  // Folded away, the Customise link stands in for the checkboxes: described, and it takes the focus.
  untickAll(t);
  clickBtn(blockOf(t, "cloud"), "Customise");
  assert.strictEqual(field(t, "gh:perm:contents"), undefined, "folded");
  assert.strictEqual(await saveBlock(t, "cloud"), false);
  const cust = byText(blockOf(t, "cloud"), "button", "Customise")[0];
  assert.strictEqual(Node.focused, cust);
  assert.strictEqual(cust.getAttribute("aria-describedby"), byCls(blockOf(t, "cloud"), "pfv-ferr")[0].id);
  assert.strictEqual(cust.getAttribute("aria-invalid"), null, "a link is not an invalid field");
});

test("a server refusal about the permission set focuses the first permission checkbox too, not the body", async () => {
  const t = cloudPage();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  clickBtn(blockOf(t, "cloud"), "Customise");
  byText(rowFor(t, "cloud", "AWS", "staging-read"), "button", "Remove")[0].click();
  t.box.putFail = apiErr(400, { error: 'profile "aws-ops": github.permissions: nothing is ticked', field: "github.permissions" });
  assert.strictEqual(await saveBlock(t, "cloud"), false);
  assert.strictEqual(t.box.puts.length, 1, "this one reached the server");
  const words = byCls(blockOf(t, "cloud"), "pfv-ferr")[0];
  assert.ok(words, "the refusal is under Permissions");
  const first = field(t, "gh:perm:contents");
  assert.strictEqual(Node.focused, first, "the page was drawn again: focus is on the new checkbox, not lost with the old Save");
  assert.strictEqual(first.getAttribute("aria-invalid"), "true");
  assert.strictEqual(first.getAttribute("aria-describedby"), words.id);
});

test("pfvFocusInvalid is a success only when the focus landed, and tries the next marked control otherwise", () => {
  const t = page();
  const mk = (tag, over) => Object.assign(t.box.document.createElement(tag), over);
  const gone = mk("button", { disabled: true });
  const plain = mk("div");
  const input = mk("input");
  const inner = mk("div");
  const innerBtn = mk("button");
  inner.append(innerBtn);
  Node.focused = null;
  assert.strictEqual(t.box.pfvFocusInvalid({ ui: {}, invalid: [gone, plain, input] }), true);
  assert.strictEqual(Node.focused, input, "a disabled control and a div took nothing: the input did");
  assert.strictEqual(t.box.pfvFocusInvalid({ ui: {}, invalid: [inner] }), true);
  assert.strictEqual(Node.focused, innerBtn, "a group gives its first control");
  Node.focused = null;
  assert.strictEqual(t.box.pfvFocusInvalid({ ui: {}, invalid: [gone, plain] }), false);
  assert.strictEqual(Node.focused, null);
  assert.strictEqual(t.box.pfvFocusInvalid({ ui: {}, invalid: [] }), false);
});


// --- New profile, the created state and Duplicate (spec 5.2, 5.8; pfv-new.js) ---------------------------------------------------

const rootOf = (t) => t.els["pfv-in"];
const newPage = async (over) => { const t = page(over); await t.box.pfvShow("aws-ops"); await t.box.pfvNew(); return t; };
// What a click on a tier card does: focus moves to it, it becomes checked, its change handler runs.
const pickTier = (t, which) => { const r = field(t, "tier-" + which); r.focus(); r.checked = true; r.onchange(); };
const typeName = (t, v) => typeInto(field(t, "name"), v);
const nameErr = (t) => texts(field(t, "name").parent, "pfv-ferr");
const wsErr = (t) => texts(field(t, "folder").parent, "pfv-ferr");
const btn = (t, label) => byText(rootOf(t), "button", label)[0];
const POSTS = (t) => t.calls.filter((c) => c.startsWith("POST"));
// The ⋯ menu's Duplicate… of an editable profile, or the primary Duplicate of a config.json one (it has no menu).
const dupMenu = (t, label) => byText(rootOf(t), "button", label || "Duplicate…")[0];
const navAdd = (t) => byCls(t.els["pf-navlist"], "pfv-ni-add")[0];
const NO_DOCKER = { boxRuntime: "", boxUnavailableReason: "Docker is not installed" };
const nextBtn = (t, label) => byCls(rootOf(t), "pfv-next-btn").find((b) => texts(b, "pfv-next-t")[0] === label);
// aws-ops as the server has it: its stored profile holds what the list's row says it binds (the shared fixture's stored profile is bare).
const BOUND = { aws_connections: ["prod-read", "staging-read"], tokens: ["grafana-prod"] };
const boundPage = () => {
  const t = page();
  t.box.docs["aws-ops"] = Object.assign({}, DOCS["aws-ops"], BOUND);
  t.box.rowFor = (d) => Object.assign({}, PROFILES[1], { name: d.name });
  return t;
};

// Task 1 removed the Settings top bar, so a page head is where the page's "Esc ✕" is. The New profile and Duplicate pages draw the same head
// as the overview but with no action of their own (their Create is in the card's foot), and a head draws its divider only when there is an
// action before it (settingshead.js does the same): so these two end with Esc ✕ alone, and the overview keeps divider then Esc ✕.
test("the New profile and Duplicate heads have no action, so they end with Esc ✕ alone and no divider", async () => {
  const closes = (t) => t.calls.filter((c) => c.startsWith("close:"));
  const tail = (t) => {
    const acts = byCls(byCls(rootOf(t), "pfv-head")[0], "page-actions");
    assert.strictEqual(acts.length, 1, "the head's actions");
    return acts[0].children.filter((c) => c instanceof Node);
  };
  const n = await newPage();
  assert.strictEqual(n.run("pfvState").page, "new");
  const [esc, ...rest] = tail(n);
  assert.deepStrictEqual(rest, [], "Esc ✕ alone: a divider with nothing before it divides nothing");
  assert.strictEqual(byCls(rootOf(n), "set-sep").length, 0);
  assert.deepStrictEqual(classesOf(esc), ["btn", "btn-ghost", "btn-sm", "set-esc"], "settingsEscButton");
  assert.strictEqual(esc.getAttribute("aria-label"), "Close (Esc)");
  esc.click();
  assert.deepStrictEqual(closes(n), ["close:settings-modal"]);
  assert.strictEqual(byCls(rootOf(n), "btn-primary").length, 1, "Create profile is still the one primary");
  // The first-run page (no profile at all) is the New profile page too.
  const lone = page({ profiles: [] });
  await lone.box.pfvShow();
  assert.strictEqual(lone.run("pfvState").page, "new");
  assert.strictEqual(byCls(rootOf(lone), "set-esc").length, 1, "the first-run New profile page can be closed from the page");
  assert.strictEqual(byCls(rootOf(lone), "set-sep").length, 0);
  // Duplicate: the same tail.
  const d = page();
  await d.box.pfvShow("aws-ops");
  await dupFromMenu(d);
  assert.strictEqual(d.run("pfvState").page, "dup");
  const [desc, ...drest] = tail(d);
  assert.deepStrictEqual([classesOf(desc).includes("set-esc"), drest.length], [true, 0]);
  // The overview has actions before it, so it keeps the divider: Start session, ⋯, divider, Esc ✕.
  const o = page();
  await o.box.pfvShow("aws-ops");
  assert.strictEqual(byCls(rootOf(o), "set-esc").length, 1);
  assert.strictEqual(byCls(rootOf(o), "set-sep").length, 1);
  assert.deepStrictEqual([...o.box.pfvHeadTail(true)].map((n) => classesOf(n)[0]), ["set-sep", "btn"]);
  assert.deepStrictEqual([...o.box.pfvHeadTail(false)].map((n) => classesOf(n)[0]), ["btn"]);
});

test("New profile's and Duplicate's head is the kit page head, as every other head: h1.page-title over p.page-desc, no h2 and no head classes of its own (G3-D1 / P7)", async () => {
  const kinds = (root) => {
    const head = byCls(root, "pfv-head")[0];
    const h = head.all((n) => n.tag === "h1" || n.tag === "h2" || n.tag === "h3");
    return {
      heads: h.map((n) => n.tag + "." + classesOf(n).join(".")),
      desc: byCls(head, "page-desc").map((n) => n.tag),
      main: classesOf(byCls(head, "pfv-head-main")[0]),
      own: ["pfv-title-name", "pfv-hsub"].filter((c) => byCls(root, c).length),
    };
  };
  const t = await newPage();
  assert.deepStrictEqual(kinds(rootOf(t)), { heads: ["h1.page-title"], desc: ["p"], main: ["page-head-text", "pfv-head-main"], own: [] }, "New profile");
  const d = page();
  await d.box.pfvShow("aws-ops");
  await dupFromMenu(d);
  assert.strictEqual(d.run("pfvState").page, "dup");
  assert.deepStrictEqual(kinds(rootOf(d)), { heads: ["h1.page-title"], desc: ["p"], main: ["page-head-text", "pfv-head-main"], own: [] }, "Duplicate");
  // Esc ✕ stays alone at the end (no divider, no head action).
  for (const x of [t, d]) {
    assert.deepStrictEqual([byCls(rootOf(x), "set-esc").length, byCls(rootOf(x), "set-sep").length], [1, 0]);
  }
});

test("New profile asks for three things and the page says nothing else", async () => {
  const t = await newPage();
  const root = rootOf(t);
  assert.strictEqual(t.run("pfvState").page, "new");
  assert.strictEqual(t.run("pfvState").name, "", "no profile is on screen");
  assert.deepStrictEqual(titles(root), ["New profile"]);
  assert.deepStrictEqual(subs(root), ["Three choices to start. Everything else is added on the profile's page."]);
  assert.deepStrictEqual(texts(root, "pfv-flabel"), ["Name", "Where it runs", "Workspace"]);
  assert.strictEqual(field(t, "name").getAttribute("placeholder"), "e.g. payments-readonly");
  assert.strictEqual(field(t, "name").value, "");
  assert.deepStrictEqual(texts(root, "pfv-hint"), ["You can't rename a profile later.", "The folder it works in. In a box, the only place it can write."]);
  // Box is lit and recommended, with the Docker line; On this machine is the other choice.
  assert.strictEqual(field(t, "tier-box").checked, true);
  assert.strictEqual(field(t, "tier-host").checked, false);
  assert.deepStrictEqual(texts(root, "pfv-chip-word"), ["Recommended"]);
  assert.deepStrictEqual(texts(root, "pfv-card-t"), ["In a boxRecommended", "On this machine"]);
  assert.deepStrictEqual(texts(root, "pfv-card-d"), ["A container. It writes only to its workspace, and CuliBox scopes AWS, GitHub and tokens.", "Docker found", "Runs as you, with your files, your network and your credentials."]);
  assert.strictEqual(field(t, "folder").getAttribute("placeholder"), "~/work/payments-api");
  assert.deepStrictEqual(classesOf(btn(t, "Create profile")), ["btn", "btn-primary"], "the kit's primary");
  assert.deepStrictEqual(classesOf(btn(t, "Cancel")), ["btn"], "Cancel is a secondary: never ghost, never primary");
  assert.deepStrictEqual(texts(root, "pfv-newafter"), ["It starts as Claude Code with its default model. You can change that, and add cloud access, on the next page."]);
  // The nav: "+ New profile" is the current row, no profile is.
  const nav = t.els["pf-navlist"].children;
  assert.deepStrictEqual(nav.filter((n) => n.getAttribute("aria-current") === "true").map((n) => n.textContent), ["+ New profile"]);
  assert.strictEqual(Node.focused, field(t, "name"), "focus starts in Name");
  assert.strictEqual(byCls(root, "pfv-block").length, 0, "no read view");
});

test("New profile: the nav's + New profile opens it, and again changes nothing", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  await navAdd(t).click();
  assert.strictEqual(t.run("pfvState").page, "new");
  typeName(t, "half");
  const before = field(t, "name");
  await navAdd(t).click();
  assert.strictEqual(field(t, "name"), before, "the same page, not drawn again");
  assert.strictEqual(before.value, "half");
  assert.deepStrictEqual(t.calls.filter((c) => c.startsWith("confirm")), []);
});

test("New profile on a machine with no Docker preselects On this machine and says why Box is off", async () => {
  const t = await newPage(NO_DOCKER);
  const root = rootOf(t);
  assert.strictEqual(field(t, "tier-host").checked, true);
  assert.strictEqual(field(t, "tier-box").checked, false);
  assert.strictEqual(field(t, "tier-box").disabled, true);
  assert.ok(texts(root, "pfv-card-d").includes("Boxes are unavailable on this machine: Docker is not installed."));
  assert.ok(!texts(root, "pfv-card-d").includes("Docker found"));
  assert.ok(texts(root, "pfv-hint").includes("On this machine a profile cannot hold AWS, GitHub or tokens."), "host is chosen: what it cannot hold");
  // Only the usable choice can be picked: nothing about Box is a button.
  assert.strictEqual(byText(root, "button", "In a box").length, 0);
});

test("choosing On this machine says what it cannot hold, choosing Box takes the sentence away", async () => {
  const t = await newPage();
  const HOLD = "On this machine a profile cannot hold AWS, GitHub or tokens.";
  assert.ok(!texts(rootOf(t), "pfv-hint").includes(HOLD));
  pickTier(t, "host");
  assert.ok(texts(rootOf(t), "pfv-hint").includes(HOLD));
  assert.strictEqual(field(t, "tier-host").checked, true);
  pickTier(t, "box");
  assert.ok(!texts(rootOf(t), "pfv-hint").includes(HOLD));
});

test("the box availability arriving after the page opened re-reads it, unless the user already chose", async () => {
  const t = page({ metaLoaded: false });
  await t.box.pfvShow("aws-ops");
  await t.box.pfvNew();
  assert.strictEqual(field(t, "tier-box").checked, true, "until /api/meta answers, boxes are assumed");
  Object.assign(t.box, { metaLoaded: true }, NO_DOCKER);
  t.box.pfvMetaChanged();
  assert.strictEqual(field(t, "tier-host").checked, true);
  assert.strictEqual(field(t, "tier-box").disabled, true);
  // A choice the user made stays.
  const u = page({ metaLoaded: false });
  await u.box.pfvShow("aws-ops");
  await u.box.pfvNew();
  pickTier(u, "box");
  Object.assign(u.box, { metaLoaded: true }, { boxRuntime: "runc" });
  u.box.pfvMetaChanged();
  assert.strictEqual(field(u, "tier-box").checked, true);
});

test("an empty name is refused under Name, and typing takes the refusal away", async () => {
  const t = await newPage();
  await btn(t, "Create profile").click();
  assert.deepStrictEqual(nameErr(t), ["Give the profile a name."]);
  assert.strictEqual(field(t, "name").getAttribute("aria-invalid"), "true");
  assert.strictEqual(Node.focused, field(t, "name"));
  assert.deepStrictEqual(POSTS(t), []);
  typeName(t, "x");
  assert.deepStrictEqual(nameErr(t), []);
  assert.strictEqual(field(t, "name").getAttribute("aria-invalid"), null);
});

test("create with a taken name shows the refusal under Name", async () => {
  const t = await newPage();
  typeName(t, "aws-ops");
  await btn(t, "Create profile").click();
  assert.deepStrictEqual(POSTS(t), [], "refused before anything is sent");
  assert.deepStrictEqual(nameErr(t), ['A profile named "aws-ops" already exists. Pick another name.']);
  assert.deepStrictEqual(texts(rootOf(t), "pfv-err"), [], "under the field, not at the top of the page");
  assert.strictEqual(Node.focused, field(t, "name"));
  // The server's own refusal, for a list that was behind: the same words, the list refetched.
  const s = await newPage();
  s.box.postFail = apiErr(409, { error: "a profile with this name already exists; nothing was saved", code: "profile_exists" });
  typeName(s, "fresh");
  s.calls.length = 0;
  await btn(s, "Create profile").click();
  assert.deepStrictEqual(nameErr(s), ['A profile named "fresh" already exists. Pick another name.']);
  assert.ok(s.calls.includes("loadProfiles"));
  assert.strictEqual(field(s, "name").value, "fresh", "what was typed stays");
  assert.deepStrictEqual(texts(rootOf(s), "pfv-err"), []);
});

test("Create sends the three inputs through POST and lands on the new profile's overview with its next steps", async () => {
  const t = await newPage();
  typeName(t, "  payments-readonly ");
  lastPicker(t).pick("/home/you/work/payments-api");
  await btn(t, "Create profile").click();
  assert.deepStrictEqual(t.box.posts, [{ name: "payments-readonly", bin: "claude", base: "inherit", runtime: "box", default_cwd: "/home/you/work/payments-api", schema: "sch-1" }]);
  const st = t.run("pfvState");
  assert.strictEqual(st.page, "");
  assert.strictEqual(st.name, "payments-readonly");
  const root = rootOf(t);
  assert.deepStrictEqual(titles(root), ["payments-readonly"]);
  assert.deepStrictEqual(texts(root, "chip"), ["box"]);
  assert.deepStrictEqual(subs(root), ["Claude Code · agent's default model"]);
  // The created state: the read view with what to do next.
  assert.deepStrictEqual(texts(root, "pfv-next-p"), ["Profile created. It can start sessions now."]);
  assert.deepStrictEqual(texts(root, "pfv-next-h"), ["Next, if this agent needs them"]);
  assert.deepStrictEqual(texts(root, "pfv-next-t"), ["Add cloud access", "Pick a model", "Add a folder it can read"]);
  assert.deepStrictEqual(texts(root, "pfv-next-d"), ["AWS, GitHub or a token", "Claude Code's default until you do", "Read only, outside the workspace"]);
  assert.deepStrictEqual(labelNames(root), ["Runs in", "Workspace", "Agent", "Cloud and tokens", "Also reachable", "Network", "Can reach", "Advanced"]);
  // It is in the nav, and the picker of the New page is gone.
  assert.ok(byCls(t.els["pf-navlist"], "pfv-ni-name").map((n) => n.textContent).includes("payments-readonly"));
  assert.strictEqual(t.box.pickers[0].destroyed, true);
  assert.strictEqual(t.run("pfvState").pending, false);
});

test("the next steps open what they say: cloud access with the choice open, the agent, a folder", async () => {
  const t = await newPage();
  typeName(t, "p1");
  await btn(t, "Create profile").click();
  nextBtn(t, "Add cloud access").click();
  assert.strictEqual(t.run("pfvState").openBlock, "cloud");
  assert.strictEqual(t.run("pfvState").picker, "cloud");
  assert.strictEqual(byCls(rootOf(t), "pfv-next").length, 1, "the steps stay while the user works through them");
  clickBtn(blockOf(t, "cloud"), "Cancel");
  nextBtn(t, "Pick a model").click();
  assert.strictEqual(t.run("pfvState").openBlock, "agent");
  clickBtn(blockOf(t, "agent"), "Cancel");
  nextBtn(t, "Add a folder it can read").click();
  assert.strictEqual(t.run("pfvState").openBlock, "reachable");
  assert.strictEqual(t.run("pfvState").picker, "reachable");
});

test("the next steps go with the first save and with the profile", async () => {
  const t = await newPage();
  typeName(t, "p1");
  await btn(t, "Create profile").click();
  nextBtn(t, "Pick a model").click();
  t.box.OPTION_DEFS = OPTION_DEFS;
  pickModel(t, "claude-sonnet-5");
  await clickBtn(blockOf(t, "agent"), "Save");
  assert.strictEqual(byCls(rootOf(t), "pfv-next").length, 0, "a saved change ends the first-steps list");
  const u = await newPage();
  typeName(u, "p2");
  await btn(u, "Create profile").click();
  assert.strictEqual(byCls(rootOf(u), "pfv-next").length, 1);
  await u.box.pfvShow("aws-ops");
  await u.box.pfvShow("p2");
  assert.strictEqual(byCls(rootOf(u), "pfv-next").length, 0, "leaving the profile ends it");
});

test("Create as host asks the C3 question first, and Cancel writes nothing", async () => {
  const t = await newPage({ confirmAnswer: false });
  typeName(t, "ops-box");
  pickTier(t, "host");
  await btn(t, "Create profile").click();
  assert.ok(t.calls.includes("confirm:Save as host:danger"));
  assert.strictEqual(t.box.asked[0].confirmLabel, "Save as host");
  assert.match(t.box.asked[0].body, /^Sessions from "ops-box" will run with no container/);
  assert.strictEqual(t.box.asked[0].focusCancel, true, "Enter is not the way into containment loss");
  assert.deepStrictEqual(POSTS(t), []);
  assert.strictEqual(t.run("pfvState").page, "new", "still on the page, with what was typed");
  assert.strictEqual(field(t, "name").value, "ops-box");
  assert.strictEqual(field(t, "tier-host").checked, true);
  t.box.confirmAnswer = true;
  await btn(t, "Create profile").click();
  assert.strictEqual(t.box.posts.length, 1);
  assert.strictEqual(t.box.posts[0].runtime, "host");
  // A host profile has no cloud to add: only the model is a next step.
  assert.deepStrictEqual(texts(rootOf(t), "pfv-next-t"), ["Pick a model"]);
  // A box profile asks nothing.
  const b = await newPage();
  typeName(b, "boxy");
  await btn(b, "Create profile").click();
  assert.deepStrictEqual(b.calls.filter((c) => c.startsWith("confirm")), []);
});

test("a host profile on a machine with no Docker is created as host, after the question", async () => {
  const t = await newPage(NO_DOCKER);
  typeName(t, "local");
  await btn(t, "Create profile").click();
  assert.ok(t.calls.includes("confirm:Save as host:danger"), "preselected is not confirmed: the question is the gate (spec 5.8)");
  assert.strictEqual(t.box.posts[0].runtime, "host");
});

test("Enter in Name creates", async () => {
  const t = await newPage();
  typeName(t, "enter-me");
  let prevented = false;
  await field(t, "name").onkeydown({ key: "Enter", preventDefault() { prevented = true; } });
  assert.strictEqual(prevented, true);
  assert.strictEqual(t.box.posts.length, 1);
});

test("a half-typed workspace is not a folder: Create refuses it under Workspace", async () => {
  const t = await newPage();
  typeName(t, "x1");
  lastPicker(t).type("proj");
  await btn(t, "Create profile").click();
  assert.deepStrictEqual(wsErr(t), ["Use a full path that starts with /"]);
  assert.deepStrictEqual(POSTS(t), []);
  lastPicker(t).type("");
  await btn(t, "Create profile").click();
  assert.strictEqual(t.box.posts.length, 1, "no folder is allowed: the New session dialog asks for one");
  assert.ok(!("default_cwd" in t.box.posts[0]));
});

test("a refusal that is not about the name is said at the top and keeps what was typed", async () => {
  const t = await newPage();
  typeName(t, "needs-cloud");
  lastPicker(t).pick("/home/you/work/x");
  t.box.postFail = apiErr(400, { error: 'profile "needs-cloud": default_cwd must be an absolute path', field: "default_cwd" });
  await btn(t, "Create profile").click();
  assert.deepStrictEqual(wsErr(t), ["Use a full path that starts with /"], "a field of the form lands under it");
  t.box.postFail = apiErr(400, { error: "something the page has no field for" });
  await btn(t, "Create profile").click();
  assert.deepStrictEqual(texts(rootOf(t), "pfv-err"), ["something the page has no field for"]);
  assert.strictEqual(field(t, "name").value, "needs-cloud");
  assert.strictEqual(t.run("pfvState").page, "new");
  // A name the server refuses lands under Name.
  t.box.postFail = apiErr(400, { error: "profile requires a name and bin", field: "name" });
  await btn(t, "Create profile").click();
  assert.deepStrictEqual(nameErr(t), ["profile requires a name and bin"]);
});

test("Cancel on New profile goes back to the profile it came from", async () => {
  const t = await newPage();
  typeName(t, "typed-and-dropped");
  await btn(t, "Cancel").click();
  assert.deepStrictEqual(t.calls.filter((c) => c.startsWith("confirm")), [], "Cancel is the user's own answer");
  assert.strictEqual(t.run("pfvState").page, "");
  assert.strictEqual(t.run("pfvState").name, "aws-ops");
  assert.deepStrictEqual(titles(rootOf(t)), ["aws-ops"]);
  assert.strictEqual(t.box.pickers[0].destroyed, true);
  // With no profile to go back to there is no Cancel to press.
  const e = page({ profiles: [] });
  await e.box.pfvNew();
  assert.strictEqual(byText(rootOf(e), "button", "Cancel").length, 0);
  assert.strictEqual(byText(rootOf(e), "button", "Create profile").length, 1);
});

test("leaving a New profile page that has something typed asks first; an untouched one just goes", async () => {
  const t = await newPage({ confirmAnswer: false });
  const before = field(t, "name");
  typeName(t, "half");
  byCls(t.els["pf-navlist"], "pfv-ni")[2].click();
  await settle();
  assert.ok(t.calls.includes("confirm:Discard unsaved changes?:danger"));
  assert.ok(!/""/.test(t.box.asked[0].body), "the question does not name a profile that has no name: " + t.box.asked[0].body);
  assert.strictEqual(t.run("pfvState").page, "new");
  assert.strictEqual(field(t, "name"), before, "Keep editing leaves the page as it was");
  assert.strictEqual(t.run("pfvState").pending, true);
  t.box.confirmAnswer = true;
  byCls(t.els["pf-navlist"], "pfv-ni")[2].click();
  await settle();
  assert.strictEqual(t.run("pfvState").page, "");
  assert.strictEqual(t.run("pfvState").name, "infra-terraform");
  assert.strictEqual(t.box.pickers[0].destroyed, true, "the folder picker goes with the page");
  // Nothing typed: no question.
  const u = await newPage();
  byCls(u.els["pf-navlist"], "pfv-ni")[2].click();
  await settle();
  assert.deepStrictEqual(u.calls.filter((c) => c.startsWith("confirm")), []);
  assert.strictEqual(u.run("pfvState").name, "infra-terraform");
});

test("closing the page and Esc go through the same question as any way out of a New profile page", async () => {
  const t = await newPage({ confirmAnswer: false });
  typeName(t, "half");
  const go = t.box.pfvPageLeave("esc");
  assert.ok(go && typeof go.then === "function", "a question, not an answer");
  assert.strictEqual(await go, false);
  assert.ok(t.calls.includes("confirm:Discard unsaved changes?:danger"));
  t.box.confirmAnswer = true;
  assert.strictEqual(await t.box.pfvPageLeave("click"), true);
  assert.strictEqual(t.run("pfvState").page, "", "the page ended with the answer");
});

test("Esc closes an open folder dropdown before it leaves the page", async () => {
  const t = await newPage();
  const picker = lastPicker(t);
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  picker.open();
  assert.strictEqual(t.box.pfvEscTarget(), "picker");
  assert.strictEqual(t.box.pfvPageLeave("esc"), false, "the dropdown took the Esc");
  assert.strictEqual(picker.isOpen(), false);
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  assert.strictEqual(t.box.pfvPageLeave("esc"), true, "nothing typed: the next Esc leaves");
});

test("the New profile page waits for nothing on a phone's modal", async () => {
  const t = page();
  t.els["settings-modal"].classList.remove("as-page");
  assert.strictEqual(await t.box.pfvNew(), false);
  assert.strictEqual(await t.box.pfvDuplicate("aws-ops"), false);
  assert.strictEqual(t.els["pfv-in"].children.length, 0);
  assert.deepStrictEqual(t.calls, []);
});

test("deleting the last profile in the list leaves the New profile page, not a blank pane", async () => {
  const t = page({ profiles: [view("solo", { runtime: "host" })] });
  t.box.docs.solo = { name: "solo", bin: "claude", runtime: "host", rev: "r1" };
  await t.box.pfvShow("solo");
  t.box.gone = "solo";
  await t.box.pfvDelete();
  assert.strictEqual(t.run("pfvState").page, "new");
  assert.deepStrictEqual(titles(rootOf(t)), ["New profile"]);
  assert.strictEqual(byText(rootOf(t), "button", "Cancel").length, 0, "nothing to go back to");
});

test("a New profile that came up while an older pfvShow was still loading is not replaced by it", async () => {
  const t = page({ profilesLoaded: false });
  const first = t.box.pfvShow();
  await t.box.pfvNew();
  await first;
  assert.strictEqual(t.run("pfvState").page, "new", "the older show gave way");
  assert.deepStrictEqual(titles(rootOf(t)), ["New profile"]);
});

// --- Duplicate -----------------------------------------------------------------------------------------------------------------

const dupFromMenu = async (t, label) => {
  const more = byCls(rootOf(t), "btn-icon")[0];
  if (more) more.click();
  await dupMenu(t, label).click();
};
const cloudNames = (t) => texts(blockOf(t, "cloud"), "pfv-nm");

test("Duplicate opens the New profile page filled in, with what the copy will also get, and writes nothing", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const original = cloudNames(t);
  assert.strictEqual(original.length, 3);
  await dupFromMenu(t);
  const root = rootOf(t);
  assert.strictEqual(t.run("pfvState").page, "dup");
  assert.deepStrictEqual(titles(root), ["Copy of aws-ops"]);
  assert.deepStrictEqual(subs(root), ["A new profile with the same settings as aws-ops. Give it a name and check the rest."]);
  assert.strictEqual(field(t, "name").value, "", "names cannot change later, so the user picks it");
  assert.strictEqual(field(t, "name").getAttribute("placeholder"), "aws-ops-copy");
  assert.strictEqual(field(t, "tier-box").checked, true, "where it runs is copied");
  assert.strictEqual(lastPicker(t).v, "/home/you/work/payments-api", "and the workspace");
  assert.ok(texts(root, "pfv-hint").includes("Copied from aws-ops. In a box, the only place it can write."));
  // The list: the read view's words, then the buttons.
  assert.deepStrictEqual(texts(root, "pfv-sheet-h"), ["This copy will also get"]);
  assert.deepStrictEqual(texts(root, "pfv-label"), ["Cloud and tokens", "Also reachable", "Agent", "Network", "Advanced"]);
  assert.deepStrictEqual(cloudNames(t), original, "the same rows the read view lists");
  assert.deepStrictEqual(texts(blockOf(t, "cloud"), "pfv-tnote"), ["Long-lived; the agent can read it while it runs"]);
  assert.deepStrictEqual(texts(blockOf(t, "agent"), "pfv-main"), ["Claude Code with claude-opus-5-5, high effort"]);
  assert.deepStrictEqual(texts(blockOf(t, "advanced"), "pfv-main"), ["None set"]);
  assert.strictEqual(byText(root, "button", "Remove").length, 0, "a list, not a form: nothing to remove here");
  assert.deepStrictEqual(classesOf(btn(t, "Create copy")), ["btn", "btn-primary"], "the kit's primary");
  assert.strictEqual(byText(root, "button", "Create profile").length, 0);
  assert.deepStrictEqual(texts(root, "pfv-dup-note"), ["Nothing is saved until you click Create copy."]);
  assert.deepStrictEqual(t.calls.filter((c) => /^(POST|PUT)/.test(c)), []);
  // aws-ops is no longer the current nav row: "+ New profile" is.
  assert.deepStrictEqual(t.els["pf-navlist"].children.filter((n) => n.getAttribute("aria-current") === "true").map((n) => n.textContent), ["+ New profile"]);
});

test("Create copy writes every binding under the typed name and the copy's overview lists the same rows", async () => {
  const t = boundPage();
  await t.box.pfvShow("aws-ops");
  const original = cloudNames(t);
  await dupFromMenu(t);
  await btn(t, "Create copy").click();
  assert.deepStrictEqual(nameErr(t), ["Give the profile a name."]);
  assert.deepStrictEqual(POSTS(t), []);
  typeName(t, "aws-ops-copy");
  await btn(t, "Create copy").click();
  const want = Object.assign({}, DOCS["aws-ops"], BOUND, { name: "aws-ops-copy", schema: "sch-1" });
  delete want.rev;
  assert.deepStrictEqual(t.box.posts, [want], "every binding, the agent and the folder; no revision");
  assert.strictEqual(t.run("pfvState").name, "aws-ops-copy");
  assert.deepStrictEqual(titles(rootOf(t)), ["aws-ops-copy"]);
  assert.deepStrictEqual(cloudNames(t), original, "the copy lists the same cloud rows");
  // The status line, and no next steps: a copy already has what it needs (R59; the steps are New profile's, b-created).
  assert.deepStrictEqual(texts(rootOf(t), "pfv-next-p"), ["Profile created. It can start sessions now."]);
  assert.strictEqual(byCls(rootOf(t), "pfv-next-btn").length, 0);
  assert.deepStrictEqual(texts(rootOf(t), "pfv-next-h"), []);
});

test("a taken name is refused under Name on a copy as well", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  await dupFromMenu(t);
  typeName(t, "infra-terraform");
  await btn(t, "Create copy").click();
  assert.deepStrictEqual(nameErr(t), ['A profile named "infra-terraform" already exists. Pick another name.']);
  assert.deepStrictEqual(POSTS(t), []);
});

test("a copy that runs on this machine lists what a host profile gets, and the question names what it drops", async () => {
  const t = boundPage();
  await t.box.pfvShow("aws-ops");
  await dupFromMenu(t);
  pickTier(t, "host");
  assert.deepStrictEqual(texts(rootOf(t), "pfv-label"), ["Agent", "Advanced"], "the list is what the copy will have");
  assert.ok(texts(rootOf(t), "pfv-hint").includes("On this machine a profile cannot hold AWS, GitHub or tokens."));
  typeName(t, "aws-host");
  await btn(t, "Create copy").click();
  const ask = t.box.asked[0];
  assert.strictEqual(ask.title, "Save as host");
  assert.match(ask.extra.map((n) => n.textContent).join(" "), /prod-read, staging-read/);
  assert.strictEqual(t.box.posts.length, 1);
  assert.strictEqual(t.box.posts[0].runtime, "host");
  assert.ok(!("aws_connections" in t.box.posts[0]) && !("tokens" in t.box.posts[0]), "box-only bindings are gone");
  assert.strictEqual(t.box.posts[0].default_cwd, "/home/you/work/payments-api");
  assert.strictEqual(t.box.posts[0].options.model, "claude-opus-5-5", "the agent comes along");
});

test("duplicate of a config.json profile creates an editable profile", async () => {
  const t = page();
  await t.box.pfvShow("claude");
  assert.deepStrictEqual(texts(rootOf(t), "pfv-note"), ["Defined in config.json. Duplicate it to change it."]);
  await dupMenu(t).click();
  assert.strictEqual(t.run("pfvState").page, "dup");
  assert.deepStrictEqual(titles(rootOf(t)), ["Copy of claude"]);
  assert.strictEqual(field(t, "name").getAttribute("placeholder"), "claude-copy");
  assert.strictEqual(field(t, "tier-host").checked, true, "claude runs on this machine");
  typeName(t, "claude-copy");
  await btn(t, "Create copy").click();
  assert.ok(t.calls.includes("confirm:Save as host:danger"), "a host copy asks the same question");
  assert.deepStrictEqual(t.box.posts, [{ name: "claude-copy", bin: "claude", runtime: "host", schema: "sch-1" }], "nothing of config.json's source rides along");
  assert.strictEqual(t.run("pfvState").name, "claude-copy");
  const root = rootOf(t);
  assert.deepStrictEqual(texts(root, "pfv-note"), [], "no config.json line");
  assert.ok(byText(root, "button", "Edit").length > 0, "a profile you can edit");
  assert.strictEqual(byText(root, "button", "Duplicate…").length, 1, "the menu's Duplicate…");
});

test("duplicating a private copy gives the copy its own folder, and it stays in a box", async () => {
  const t = clonePage();
  await t.box.pfvShow("gitops-clone");
  await dupFromMenu(t);
  assert.strictEqual(lastPicker(t).v, "/home/you/work/k8s-gitops", "the workspace of a private copy is the folder it copies");
  assert.strictEqual(byText(rootOf(t), "button", "On this machine").length, 0);
  assert.strictEqual(field(t, "tier-host"), undefined, "a private copy needs a box: the host card is not drawn");
  assert.ok(texts(rootOf(t), "pfv-hint").includes("A private copy needs a box."));
  typeName(t, "gitops-2");
  await btn(t, "Create copy").click();
  assert.strictEqual(t.box.posts.length, 1);
  const post = t.box.posts[0];
  assert.strictEqual(post.clone_of, "/home/you/work/k8s-gitops");
  assert.strictEqual(post.default_cwd, "/home/you/box-clones/k8s-gitops-2", "never the source's own copy folder");
  assert.deepStrictEqual(post.clone_share, ["CLAUDE.md", "docs/a.md"]);
  assert.strictEqual(post.clone_no_auto_return, true);
  assert.ok(!("rev" in post));
  assert.deepStrictEqual(t.calls.filter((c) => c.startsWith("confirm")), []);
});

test("Cancel on a copy goes back to the profile it was made from", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  await dupFromMenu(t);
  await btn(t, "Cancel").click();
  assert.strictEqual(t.run("pfvState").page, "");
  assert.strictEqual(t.run("pfvState").name, "aws-ops");
  assert.deepStrictEqual(t.calls.filter((c) => c.startsWith("confirm") || /^(POST|PUT)/.test(c)), []);
});

test("Duplicate asks first when a block has unsaved changes, and Keep editing stays on the profile", async () => {
  const t = page({ confirmAnswer: false });
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  pickModel(t, "claude-sonnet-5");
  await dupFromMenu(t);
  assert.ok(t.calls.includes("confirm:Discard unsaved changes?:danger"));
  assert.strictEqual(t.run("pfvState").page, "");
  assert.strictEqual(t.run("pfvState").openBlock, "agent");
  t.box.confirmAnswer = true;
  await dupFromMenu(t);
  assert.strictEqual(t.run("pfvState").page, "dup");
});

test("a copy made while the profile page holds a New profile page starts from the profile on screen only", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  assert.strictEqual(await t.box.pfvDuplicate(), true);
  assert.strictEqual(await t.box.pfvDuplicate(), false, "there is no profile on screen to copy from the copy page");
  assert.strictEqual(t.run("pfvState").page, "dup");
});

test("every top-level name in pfv-new.js is pfn/PFN_ prefixed or a seam, and unique across the page scripts", () => {
  const names = (src) => new Set([...src.matchAll(/^(?:async\s+)?(?:function\s+|const\s+|let\s+|var\s+)([A-Za-z_$][\w$]*)/gm)].map((m) => m[1]));
  const mine = names(fs.readFileSync(path.join(STATIC, "pfv-new.js"), "utf8"));
  assert.ok(mine.size > 10);
  const seams = new Set(["pfvNew", "pfvDuplicate", "pfvNextSteps"]);
  assert.ok([...mine].every((n) => /^(pfn|PFN_)/.test(n) || seams.has(n)), "every name is pfn/PFN_ prefixed or a seam: " + [...mine].join(", "));
  for (const n of seams) assert.ok(mine.has(n), n + " is declared here");
  const clashes = [];
  for (const f of fs.readdirSync(STATIC).filter((x) => x.endsWith(".js") && x !== "pfv-new.js")) {
    for (const n of names(fs.readFileSync(path.join(STATIC, f), "utf8"))) if (mine.has(n)) clashes.push(n + " (" + f + ")");
  }
  assert.deepStrictEqual(clashes, []);
});

test("the New profile editor registers through the seam, and index.html loads it after the page script and before app.js", () => {
  const t = page();
  assert.ok(t.run("PFV_EDITORS").draft, "the draft registered itself");
  assert.throws(() => t.box.pfvRegisterEditor("draft", {}), /registered twice: draft/);
  const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
  const tags = [...html.matchAll(/<script src="\/([a-z-]+\.js)"><\/script>/g)].map((m) => m[1]);
  assert.strictEqual(tags.filter((x) => x === "pfv-new.js").length, 1);
  assert.ok(tags.indexOf("pfv-new.js") > tags.indexOf("pfv-workspace.js"), "after the scripts whose helpers it calls");
  assert.ok(tags.indexOf("pfv-new.js") > tags.indexOf("wspicker.js") && tags.indexOf("pfv-new.js") < tags.indexOf("app.js"));
});

test("app.js gives the old New profile button the page's own New profile page on the desktop page, and keeps the phone's form after it", () => {
  const at = APP.indexOf('$("pf-new").addEventListener("click"');
  assert.ok(at >= 0);
  const click = APP.slice(at, APP.indexOf("\n});", at));
  assert.ok(click.indexOf("profilePageHolds(") >= 0 && click.indexOf("profilePageHolds(") < click.indexOf("pfvNew()"), "the hold (unsaved changes) first");
  assert.match(click, /pfvOnPage\(\)[^\n]*pfvNew\(\); return;/, "the desktop page opens its New profile page and stops");
  assert.ok(click.indexOf("pfvNew()") < click.indexOf("newProfile()"), "the phone's blank form is only reached when it is not the page");
  // The old Duplicate button is in the footer the page does not draw, and the page's own Duplicate (pfvDuplicate) does not go through it.
  assert.doesNotMatch(appFn("duplicateProfile"), /pfv/);
});


// --- fix round 1 ---------------------------------------------------------------------------------------------------------

// Every refusal Create can get lands where the user can see it: under the control it names, else at the top of the page.
test("every refusal of Create or Create copy is visible: under its field, else at the top", async () => {
  const draw = (t) => ({ top: texts(rootOf(t), "pfv-err"), name: nameErr(t), ws: field(t, "folder") ? wsErr(t) : [] });
  const cases = [
    // [how the server answers, where it must land, the words]
    [apiErr(400, { error: 'profile "x": default_cwd must be an absolute path', field: "default_cwd" }), "ws", "Use a full path that starts with /"],
    [apiErr(400, { error: "profile requires a name and bin", field: "name" }), "name", "profile requires a name and bin"],
    [apiErr(400, { error: 'profile "x": aws_connections[0] "gone" is not defined in aws_connections', field: "aws_connections[0]" }), "top", "Not found in Credentials. Remove it or create it."],
    [apiErr(400, { error: "something with no field" }), "top", "something with no field"],
    [apiErr(409, { error: "CuliBox was updated since this page was loaded; reload the page, then save again; nothing was saved", code: "page_outdated" }), "top", "CuliBox was updated since this page was loaded; reload the page, then save again; nothing was saved"],
    [new Error("boom"), "top", "Could not save this profile."],
  ];
  for (const [fail, where, words] of cases) {
    const t = await newPage();
    typeName(t, "refused");
    t.box.postFail = fail;
    await btn(t, "Create profile").click();
    const at = draw(t);
    const seen = where === "top" ? at.top : where === "name" ? at.name : at.ws;
    assert.deepStrictEqual(seen, [words], words);
    assert.strictEqual(t.run("pfvState").page, "new", "the page and what was typed stay");
    assert.strictEqual(field(t, "name").value, "refused");
  }
});

test("a refusal of the copy's own folder on Create copy of a private copy is said at the top, not swallowed", async () => {
  // The Workspace control of a private copy is the folder to copy (clone_of); the copy's folder (default_cwd) has no control here.
  for (const [fail, where, words] of [
    [apiErr(400, { error: 'profile "gitops-2": clone_of requires default_cwd', field: "default_cwd" }), "top", 'clone_of requires default_cwd'],
    [apiErr(400, { error: 'profile "gitops-2": default_cwd must be an absolute path', field: "default_cwd" }), "top", "Use a full path that starts with /"],
    [apiErr(400, { error: 'profile "gitops-2": clone_of must be an absolute path', field: "clone_of" }), "ws", "Use a full path that starts with /"],
  ]) {
    const t = clonePage();
    await t.box.pfvShow("gitops-clone");
    await dupFromMenu(t);
    typeName(t, "gitops-2");
    t.box.postFail = fail;
    await btn(t, "Create copy").click();
    assert.strictEqual(t.box.posts.length, 1, "it was sent: the refusal is the server's");
    assert.deepStrictEqual(where === "top" ? texts(rootOf(t), "pfv-err") : wsErr(t), [words]);
    assert.strictEqual(t.run("pfvState").page, "dup");
  }
});

test("Create copy of a private copy with its folder emptied is refused under Workspace, before anything is sent", async () => {
  const t = clonePage();
  await t.box.pfvShow("gitops-clone");
  await dupFromMenu(t);
  typeName(t, "gitops-2");
  lastPicker(t).type("");
  await btn(t, "Create copy").click();
  assert.deepStrictEqual(wsErr(t), ["Private copy: pick the folder to copy."]);
  assert.deepStrictEqual(POSTS(t), []);
});

test("the folder picker and the tier cards are built in one place each, for Workspace, Runs in, New profile and Duplicate", () => {
  const files = fs.readdirSync(STATIC).filter((f) => /^(profilepage|pfv-[a-z]+)\.js$/.test(f));
  // The code, not the comments that name it.
  const code = (f) => fs.readFileSync(path.join(STATIC, f), "utf8").split("\n").filter((l) => !/^\s*\/\//.test(l)).join("\n");
  const count = (re) => files.map((f) => [f, (code(f).match(re) || []).length]).filter(([, n]) => n > 0);
  assert.deepStrictEqual(count(/createWsPicker\(/g), [["pfv-workspace.js", 1]], "one picker construction (its Esc and release wiring included)");
  assert.deepStrictEqual(count(/A container\. It writes only to its workspace/g), [["profilepage.js", 1]]);
  assert.deepStrictEqual(count(/Runs as you, with your files, your network and your credentials/g), [["profilepage.js", 1]]);
  assert.deepStrictEqual(count(/Docker found/g), [["profilepage.js", 1]]);
});

test("opening the page with no profile to show is the New profile page, not a blank pane", async () => {
  const t = page({ profiles: [] });
  await t.box.pfvShow();
  assert.strictEqual(t.run("pfvState").page, "new");
  assert.deepStrictEqual(titles(rootOf(t)), ["New profile"]);
  assert.strictEqual(byText(rootOf(t), "button", "Cancel").length, 0, "nothing to go back to");
  // The Profiles tab selected again leaves it as it is.
  typeName(t, "half");
  await t.box.pfvShow();
  assert.strictEqual(field(t, "name").value, "half");
  // Not while a profile can be shown.
  const u = page();
  await u.box.pfvShow();
  assert.strictEqual(u.run("pfvState").page, "");
  assert.strictEqual(u.run("pfvState").name, "aws-ops", "the first one you own");
});

test("no Docker: a copy of a box profile keeps Box selectable with the reason under it, and host stays a choice", async () => {
  const t = page(NO_DOCKER);
  await t.box.pfvShow("aws-ops");
  await dupFromMenu(t);
  const box = field(t, "tier-box");
  assert.strictEqual(box.checked, true, "the copy's tier is the source's");
  assert.strictEqual(box.disabled, false, "a copy of a box profile may stay one");
  assert.ok(texts(rootOf(t), "pfv-card-d").includes("Boxes are unavailable on this machine: Docker is not installed."));
  assert.ok(!texts(rootOf(t), "pfv-card-d").includes("Docker found"));
  assert.strictEqual(field(t, "tier-host").disabled, false);
  // It can be created as the box profile it copies, and the page does not promise sessions.
  typeName(t, "aws-box-copy");
  await btn(t, "Create copy").click();
  assert.strictEqual(t.box.posts[0].runtime, "box");
  assert.deepStrictEqual(texts(rootOf(t), "pfv-next-p"), ["Profile created."]);
  // And the user can choose On this machine, then Box again.
  const u = page(NO_DOCKER);
  await u.box.pfvShow("aws-ops");
  await dupFromMenu(u);
  pickTier(u, "host");
  assert.strictEqual(field(u, "tier-host").checked, true);
  assert.strictEqual(field(u, "tier-box").disabled, false);
  pickTier(u, "box");
  assert.strictEqual(field(u, "tier-box").checked, true);
  // A host profile's copy on this machine has no Box to choose: as for New profile.
  const h = page(NO_DOCKER);
  await h.box.pfvShow("runbook-host");
  await dupFromMenu(h);
  assert.strictEqual(field(h, "tier-host").checked, true);
  assert.strictEqual(field(h, "tier-box").disabled, true);
});

test("after Duplicate -> + New profile, Cancel goes back to the profile the copy was made from", async () => {
  const t = page();
  await t.box.pfvShow("infra-terraform");
  await dupFromMenu(t);
  await navAdd(t).click();
  assert.strictEqual(t.run("pfvState").page, "new");
  await btn(t, "Cancel").click();
  assert.strictEqual(t.run("pfvState").name, "infra-terraform", "not the first profile you own");
});

// --- Task 15: the Credentials drawer over the profile page (spec 5.7) ----------------------------------------------------

// The page with the drawer's own parts (index.html's #cred-scrim and #cred-drawer, with its body, foot and status line) and the
// page's other parts beside them, and stand-ins for the Credentials flows that record how they were started. The stand-ins end
// the way credentials.js's do: credFlowCancel reports what the visit saved (st.saved, of kind st.kind) through onCreated, then onClose.
const FOCUSABLE_TAGS = ["button", "input", "select", "textarea"];
function drawerPage(over, doc) {
  const t = pickPage(over, doc);
  const scrim = new Node("div");
  const drawer = new Node("aside");
  const head = new Node("div"), body = new Node("div"), foot = new Node("div"), status = new Node("p");
  scrim.hidden = drawer.hidden = status.hidden = true;
  drawer.setAttribute("tabindex", "-1");
  drawer.append(head, body, status, foot);
  const behind = [new Node("div"), new Node("nav"), new Node("div")];
  t.els["settings-modal"].append(...behind, scrim, drawer);
  Object.assign(t.els, { "cred-scrim": scrim, "cred-drawer": drawer, "cred-drawer-head": head, "cred-drawer-body": body, "cred-drawer-foot": foot, "cred-drawer-status": status });
  // What the browser's querySelectorAll finds for the focus selector: the enabled controls and the links made tabbable.
  drawer.selectors = [];
  drawer.querySelectorAll = (sel) => {
    drawer.selectors.push(sel);
    return drawer.all((n) => (FOCUSABLE_TAGS.includes(n.tag) || (n.attrs.tabindex !== undefined && n.attrs.tabindex !== "-1")) && !n.disabled);
  };
  const st = { started: [], busy: false, drops: 0, cancels: 0, saved: [], kind: "aws", cur: null };
  const control = (tag, text) => { const n = new Node(tag); if (text) n.own = text; return n; };
  const draw = (kind, opts) => {
    st.started.push({ kind, opts });
    st.cur = opts;
    const crumb = control("a", "Credentials");
    crumb.className = "crumb";
    crumb.setAttribute("tabindex", "0");
    opts.head.replaceChildren(crumb);
    opts.host.replaceChildren(control("input"), control("button", "Tile"));
    opts.foot.replaceChildren(control("button", "Cancel"), control("button", opts.saveLabel || "Save"));
  };
  t.box.awsOpenAdd = (opts) => draw("aws", opts);
  t.box.credGitHubChoose = (opts) => draw("github", opts);
  t.box.credTokenAdd = (group, start, opts) => { st.group = group; st.start = start; draw("token", opts); };
  t.box.credFlowBusy = () => st.busy;
  t.box.credFlowDrop = () => { st.drops++; st.cur = null; };
  t.box.credFlowCancel = (opts) => {
    if (st.busy || !st.cur || st.cur !== opts) return false;
    st.cur = null;
    st.cancels++;
    if (st.saved.length) opts.onCreated(st.saved.slice(), st.kind);
    opts.onClose();
    return true;
  };
  return { t, st, drawer, scrim, head, body, foot, status, behind };
}
// The picker's "New ..." item of Cloud and tokens, from the read view.
const openDrawerFrom = async (d, title) => {
  await openPicker(d.t);
  optBtn(d.t, "cloud", title).click();
  return d.st.started[d.st.started.length - 1];
};
const SANDBOX = { name: "sandbox", account_id: "333333333333", region: "us-east-1" };
// The server defines these once the flow has saved them: what refreshCredLists reads.
const defineAfterRefresh = (t, over) => {
  t.box.refreshCredLists = async () => {
    t.calls.push("refreshCredLists");
    if (over.aws) t.box.awscEntries = t.box.awscEntries.concat(over.aws);
    if (over.tokens) t.box.stEntries = t.box.stEntries.concat(over.tokens);
    if (over.apps) t.box.ghApps = t.box.ghApps.concat(over.apps);
  };
};
const ownerRows = (t) => byCls(blockOf(t, "cloud"), "pfv-row").filter((r) => texts(r, "pfv-k")[0] === "GitHub");

test("a created AWS connection becomes a pending row", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  defineAfterRefresh(t, { aws: [{ name: "lab", account_id: "555555555555", region: "eu-west-1" }] });
  const flow = await openDrawerFrom(d, "New AWS connection…");
  assert.strictEqual(flow.kind, "aws");
  st.saved = ["lab"];
  assert.strictEqual(rowFor(t, "cloud", "AWS", "lab"), undefined, "nothing yet");
  assert.strictEqual(t.box.pfvCloseDrawer(), true);
  await settle();
  const row = rowFor(t, "cloud", "AWS", "lab");
  assert.ok(row, "the connection is a row of the open block");
  assert.deepStrictEqual(texts(row, "pfv-rsub"), ["account 555555555555, eu-west-1"], "defined by now: the read view's words, not Not found in Credentials");
  assert.strictEqual(byCls(row, "pfv-miss").length, 0);
  assert.strictEqual(unsavedNote(t).hidden, false, "it is pending");
  assert.deepStrictEqual(t.calls.filter((c) => /^(PUT|POST)/.test(c)), [], "nothing is sent until the block's Save");
  assert.ok(t.calls.indexOf("refreshCredLists") >= 0, "the lists were read before the row was drawn");
  await saveBlock(t, "cloud");
  assert.ok(t.box.puts[0].aws_connections.includes("lab"));
});

test("several AWS connections from one visit become several rows, each removable before Save", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  defineAfterRefresh(t, { aws: [{ name: "lab-a", account_id: "555555555555", region: "eu-west-1" }, { name: "lab-b", account_id: "666666666666", region: "eu-west-1" }] });
  await openDrawerFrom(d, "New AWS connection…");
  st.saved = ["lab-a", "lab-b"];
  t.box.pfvCloseDrawer();
  await settle();
  assert.ok(rowFor(t, "cloud", "AWS", "lab-a"));
  assert.ok(rowFor(t, "cloud", "AWS", "lab-b"));
  assert.strictEqual(Node.focused, field(t, "rm:aws:lab-b"), "focus is on the last row added");
  byText(rowFor(t, "cloud", "AWS", "lab-a"), "button", "Remove")[0].click();
  assert.strictEqual(rowFor(t, "cloud", "AWS", "lab-a"), undefined, "a row that was only just added is deleted outright");
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(t.box.puts[0].aws_connections, ["lab-b", "prod-read", "staging-read"]);
});

test("closing the drawer without creating changes nothing", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  await openDrawerFrom(d, "New AWS connection…");
  const rows = rowsOf(t, "cloud").length;
  t.calls.length = 0;
  assert.strictEqual(t.box.pfvCloseDrawer(), true);
  await settle();
  assert.strictEqual(st.cancels, 1, "the flow was ended through its own Cancel path");
  assert.strictEqual(rowsOf(t, "cloud").length, rows);
  assert.strictEqual(unsavedNote(t).hidden, true);
  assert.ok(blockOf(t, "cloud").className.includes("pfv-editing"), "the block is still open");
  assert.deepStrictEqual(t.calls, [], "no refresh, no request");
  assert.strictEqual(t.run("pfvState").pending, false);
});

test("the drawer opens from a create item of the picker with a host, a foot, a label and a way to report, and takes over the page", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st, drawer, scrim, body, foot, behind } = d;
  const flow = await openDrawerFrom(d, "New AWS connection…");
  assert.strictEqual(flow.opts.host, body);
  assert.strictEqual(flow.opts.foot, foot);
  assert.strictEqual(flow.opts.head, d.head, "the header band, which stays put while the body scrolls");
  assert.strictEqual(typeof flow.opts.onIdle, "function");
  assert.strictEqual(flow.opts.saveLabel, "Save all and add", "the mockup's label for AWS");
  assert.strictEqual(typeof flow.opts.onCreated, "function");
  assert.strictEqual(typeof flow.opts.onClose, "function");
  assert.strictEqual(drawer.hidden, false);
  assert.strictEqual(scrim.hidden, false);
  assert.strictEqual(t.run("pfvState").drawer, true);
  assert.strictEqual(t.box.pfvEscTarget(), "drawer");
  assert.strictEqual(t.run("pfvState").picker, "", "the picker went when the drawer came");
  assert.deepStrictEqual(behind.map((n) => n.attrs.inert), ["", "", ""], "the page behind it is inert");
  assert.strictEqual(drawer.attrs.inert, undefined, "the drawer and its scrim are not");
  assert.strictEqual(scrim.attrs.inert, undefined);
  assert.strictEqual(st.started.length, 1);
  // Only one at a time.
  assert.strictEqual(t.box.pfvOpenDrawer("token"), false);
  assert.strictEqual(st.started.length, 1);
});

test("each kind starts its own Credentials flow in the drawer; the primary button reads 'and add'", async () => {
  const labels = {};
  for (const [title, kind] of [["New AWS connection…", "aws"], ["New GitHub App…", "github"], ["New token…", "token"]]) {
    const d = drawerPage({ awscEntries: PICK_AWS });
    const flow = await openDrawerFrom(d, title);
    assert.strictEqual(flow.kind, kind, title);
    labels[kind] = flow.opts.saveLabel;
    if (kind === "token") assert.deepStrictEqual([d.st.group, d.st.start], [null, undefined], "today's default start, not a provider chosen for the user");
    assert.deepStrictEqual(d.foot.all((n) => n.tag === "button").map((n) => n.textContent), ["Cancel", labels[kind]]);
  }
  assert.deepStrictEqual(labels, { aws: "Save all and add", github: "Save and add", token: "Save and add" });
});

test("the drawer opens only while a block is open in edit mode", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  await d.t.box.pfvShow("aws-ops");
  assert.strictEqual(d.t.box.pfvOpenDrawer("aws"), false, "the read view has no picker to ask from");
  assert.strictEqual(d.t.box.pfvOpenDrawer("nonsense"), false);
  assert.strictEqual(d.st.started.length, 0);
  assert.strictEqual(d.drawer.hidden, true);
  assert.strictEqual(d.t.run("pfvState").drawer, false);
});

test("a token made from New token… is a pending token row, one per name the server made", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t } = d;
  defineAfterRefresh(t, { tokens: [{ name: "pd-eu", env: ["PAGERDUTY_TOKEN"], hosts: [{ host: "api.eu.pagerduty.com", port: 443 }] }, { name: "pd-us", env: ["PAGERDUTY_TOKEN"], hosts: [{ host: "api.pagerduty.com", port: 443 }] }] });
  const flow = await openDrawerFrom(d, "New token…");
  flow.opts.onCreated(["pd-eu", "pd-us"], "token");
  flow.opts.onClose();
  await settle();
  for (const n of ["pd-eu", "pd-us"]) {
    const row = rowFor(t, "cloud", "Token", n);
    assert.ok(row, n);
    assert.strictEqual(byCls(row, "pfv-miss").length, 0, n + " is listed by the time it is drawn");
  }
  assert.strictEqual(ownerRows(t).length, 1, "no GitHub owner was added");
  await saveBlock(t, "cloud");
  assert.deepStrictEqual(t.box.puts[0].tokens, ["grafana-prod", "pd-eu", "pd-us", "sentry-staging"]);
});

test("a GitHub App made from New GitHub App… is a new owner row with that App selected and the owner and repos left to the user", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t } = d;
  defineAfterRefresh(t, { apps: ["new-app"] });
  const flow = await openDrawerFrom(d, "New GitHub App…");
  const owners = ownerRows(t).length;
  flow.opts.onCreated(["new-app"], "app");
  flow.opts.onClose();
  await settle();
  assert.strictEqual(ownerRows(t).length, owners + 1);
  const g = t.run("pfvState").edits.cloud.values.gh;
  assert.deepStrictEqual(JSON.parse(JSON.stringify(g[g.length - 1])), { fresh: true, owner: "", pick: "app:new-app", repos: "" });
  assert.strictEqual(field(t, "gh:" + (g.length - 1) + ":pick").value, "app:new-app", "selected in the row's Sign in with");
  assert.strictEqual(Node.focused, field(t, "gh:" + (g.length - 1) + ":owner"), "focus is on the owner, the first thing the user fills in");
  // Left empty, the row is no change: Save sends no owner for it.
  await saveBlock(t, "cloud");
  assert.strictEqual(t.box.puts[0].github.owners.length, 1);
});

test("a GitHub personal access token made from New GitHub App… is a new owner row signing in with that token", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t } = d;
  defineAfterRefresh(t, { tokens: [{ name: "gh-pat-2", provider: "github", hosts: [{ host: "api.github.com", port: 443 }] }] });
  const flow = await openDrawerFrom(d, "New GitHub App…");
  flow.opts.onCreated(["gh-pat-2"], "token");
  flow.opts.onClose();
  await settle();
  const g = t.run("pfvState").edits.cloud.values.gh;
  assert.deepStrictEqual(JSON.parse(JSON.stringify(g[g.length - 1])), { fresh: true, owner: "", pick: "token:gh-pat-2", repos: "" });
  assert.strictEqual(rowFor(t, "cloud", "Token", "gh-pat-2"), undefined, "it is a GitHub owner, not a token row");
});

test("pfvAddRow takes the login a new GitHub owner signs in with, else the first one there is", async () => {
  const t = pickPage();
  await openPicker(t);
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "github", login: "token:gh-pat" }), true);
  assert.strictEqual(t.run("pfvState").edits.cloud.values.gh.pop().pick, "token:gh-pat");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "github", login: "app:later" }), true, "a login the lists do not hold yet stays selectable");
  assert.strictEqual(t.run("pfvState").edits.cloud.values.gh.pop().pick, "app:later");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "github", login: "nonsense" }), true);
  assert.strictEqual(t.run("pfvState").edits.cloud.values.gh.pop().pick, "app:acme-app", "not a login: the first there is, as before");
  assert.strictEqual(t.box.pfvAddRow("cloud", { kind: "github" }), true);
  assert.strictEqual(t.run("pfvState").edits.cloud.values.gh.pop().pick, "app:acme-app");
});

test("the names are listed before their rows are drawn, and a block that went meanwhile gets nothing", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  let release;
  t.box.refreshCredLists = () => new Promise((r) => { release = () => { t.box.awscEntries = t.box.awscEntries.concat([{ name: "lab", account_id: "555555555555", region: "eu-west-1" }]); r(); }; });
  await openDrawerFrom(d, "New AWS connection…");
  st.saved = ["lab"];
  t.box.pfvCloseDrawer();
  await settle();
  assert.strictEqual(rowFor(t, "cloud", "AWS", "lab"), undefined, "the lists are still being read");
  release();
  await settle();
  assert.strictEqual(byCls(rowFor(t, "cloud", "AWS", "lab"), "pfv-miss").length, 0);
  // The same, but the block is closed while the lists are read: its rows have nowhere to go.
  const e = drawerPage({ awscEntries: PICK_AWS });
  let release2;
  e.t.box.refreshCredLists = () => new Promise((r) => { release2 = r; });
  await openDrawerFrom(e, "New AWS connection…");
  e.st.saved = ["lab"];
  e.t.box.pfvCloseDrawer();
  clickBtn(blockOf(e.t, "cloud"), "Cancel");
  assert.strictEqual(byCls(e.t.els["pfv-in"], "pfv-editing").length, 0);
  release2();
  await settle();
  assert.strictEqual(byCls(e.t.els["pfv-in"], "pfv-editing").length, 0, "nothing reopened it");
  assert.strictEqual(e.t.run("pfvState").pending, false);
  // Opened again before the lists came back, the block is a new edit: the names are not put into it.
  const g = drawerPage({ awscEntries: PICK_AWS });
  let release3;
  g.t.box.refreshCredLists = () => new Promise((r) => { release3 = r; });
  await openDrawerFrom(g, "New AWS connection…");
  g.st.saved = ["lab"];
  g.t.box.pfvCloseDrawer();
  clickBtn(blockOf(g.t, "cloud"), "Cancel");
  clickBtn(blockOf(g.t, "cloud"), "Edit");
  release3();
  await settle();
  assert.strictEqual(rowFor(g.t, "cloud", "AWS", "lab"), undefined, "not the block the drawer was opened from");
  assert.strictEqual(g.t.run("pfvState").pending, false);
  // And a failed read of the lists still adds the row (it reads as missing, which is true to what the page knows).
  const f = drawerPage({ awscEntries: PICK_AWS });
  f.t.box.refreshCredLists = async () => { throw new Error("offline"); };
  await openDrawerFrom(f, "New AWS connection…");
  f.st.saved = ["lab"];
  f.t.box.pfvCloseDrawer();
  await settle();
  assert.ok(rowFor(f.t, "cloud", "AWS", "lab"));
});

test("every close empties the drawer's host and foot, hides it and its scrim, frees the page and gives focus back to + Add access", async () => {
  const closers = {
    "pfvCloseDrawer": (d) => d.t.box.pfvCloseDrawer(),
    "the scrim": (d) => d.scrim.onclick({ target: d.scrim }),
    "Esc": (d) => assert.strictEqual(d.t.box.pageLeaveHook("esc"), false),
    "the flow's own Cancel": (d) => d.st.started[0].opts.onClose(),
  };
  for (const [how, close] of Object.entries(closers)) {
    const d = drawerPage({ awscEntries: PICK_AWS });
    await openDrawerFrom(d, "New token…");
    d.body.append(new Node("input"));
    assert.ok(d.head.children.length > 0 && d.body.children.length > 0 && d.foot.children.length > 0, how + ": drawn");
    close(d);
    await settle();
    assert.deepStrictEqual([d.head.children.length, d.body.children.length, d.foot.children.length], [0, 0, 0], how + ": what was typed does not linger in a hidden DOM");
    assert.strictEqual(d.drawer.hidden, true, how);
    assert.strictEqual(d.scrim.hidden, true, how);
    assert.strictEqual(d.t.run("pfvState").drawer, false, how);
    assert.strictEqual(d.t.box.pfvEscTarget(), "page", how);
    assert.deepStrictEqual(d.behind.map((n) => n.attrs.inert), [undefined, undefined, undefined], how + ": inert is lifted");
    assert.strictEqual(Node.focused, field(d.t, "add"), how + ": focus is back on the link that led here");
    assert.strictEqual(blockOf(d.t, "cloud").className.includes("pfv-editing"), true, how + ": the block is open");
  }
});

test("Esc with the drawer open closes the drawer only, the next Esc is the page's", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  const asked = recordAsks(t, false);
  await openDrawerFrom(d, "New AWS connection…");
  assert.strictEqual(t.box.pageLeaveHook("esc"), false, "the page stays");
  assert.strictEqual(st.cancels, 1);
  assert.strictEqual(d.drawer.hidden, true);
  assert.strictEqual(t.run("pfvState").openBlock, "cloud", "the block is open still");
  assert.strictEqual(asked.length, 0);
  // A picker open under it is the next layer, then the page.
  clickBtn(blockOf(t, "cloud"), "+ Add access");
  assert.strictEqual(t.box.pfvEscTarget(), "picker");
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  assert.strictEqual(t.box.pfvEscTarget(), "page");
  assert.strictEqual(t.box.pageLeaveHook("esc"), true, "nothing changed: the page may go");
});

test("closing by Esc, the scrim or the flow's own Cancel after saving 1 of 2 still adds the saved name, once", async () => {
  for (const how of ["Esc", "scrim", "crumb"]) {
    const d = drawerPage({ awscEntries: PICK_AWS });
    const { t, st } = d;
    defineAfterRefresh(t, { aws: [{ name: "lab-a", account_id: "555555555555", region: "eu-west-1" }] });
    await openDrawerFrom(d, "New AWS connection…");
    st.saved = ["lab-a"];
    if (how === "Esc") t.box.pageLeaveHook("esc");
    else if (how === "scrim") d.scrim.onclick({ target: d.scrim });
    else { st.cur = null; d.st.started[0].opts.onCreated(["lab-a"], "aws"); d.st.started[0].opts.onClose(); }
    await settle();
    assert.strictEqual(rowsOf(t, "cloud").filter((r) => (texts(r, "pfv-nm")[0] || "").startsWith("lab-a")).length, 1, how);
    assert.strictEqual(d.drawer.hidden, true, how);
  }
});

test("while a flow saves, a close is refused with a line that says why; once it has answered, the close goes through", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st, status } = d;
  const asked = recordAsks(t, true);
  await openDrawerFrom(d, "New AWS connection…");
  st.busy = true;
  assert.strictEqual(t.box.pfvCloseDrawer(), false);
  assert.strictEqual(d.drawer.hidden, false, "still open");
  assert.strictEqual(st.cancels, 0);
  assert.strictEqual(status.hidden, false);
  assert.ok(status.textContent.length > 0, "a short line says why");
  assert.strictEqual(t.box.pageLeaveHook("esc"), false);
  d.scrim.onclick({ target: d.scrim });
  assert.strictEqual(d.drawer.hidden, false, "neither Esc nor the scrim closed it");
  assert.strictEqual(st.drops, 0, "nothing was dropped under a save: its name would be lost");
  assert.strictEqual(t.run("pfvState").drawer, true);
  st.busy = false;
  assert.strictEqual(t.box.pfvCloseDrawer(), true);
  assert.strictEqual(d.drawer.hidden, true);
  assert.strictEqual(status.hidden, true, "the line goes with it");
  assert.strictEqual(status.textContent, "");
  assert.strictEqual(asked.length, 0);
});

test("a close with no flow to end (it was still reading the server) drops the flow, so it draws nothing into the emptied host", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  await openDrawerFrom(d, "New token…");
  st.cur = null;
  assert.strictEqual(t.box.pfvCloseDrawer(), true);
  assert.strictEqual(st.drops, 1);
  assert.strictEqual(d.drawer.hidden, true);
  assert.deepStrictEqual([d.body.children.length, d.foot.children.length], [0, 0]);
});

test("another Settings tab, a session or a profile switch closes the drawer first, through the same path, then asks what it always asks", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  defineAfterRefresh(t, { aws: [{ name: "lab", account_id: "555555555555", region: "eu-west-1" }] });
  recordAsks(t, true);
  await openDrawerFrom(d, "New AWS connection…");
  st.saved = ["lab"];
  assert.strictEqual(t.box.pfvTabLeave("creds"), true, "nothing else holds changes: the tab may go once the drawer is closed");
  assert.strictEqual(d.drawer.hidden, true, "the drawer is gone");
  assert.strictEqual(st.cancels, 1, "through the flow's own Cancel");
  // Busy: the page does not go, and the answer is a promise of false (shell.js waits on one), not a bare false.
  const e = drawerPage({ awscEntries: PICK_AWS });
  recordAsks(e.t, true);
  await openDrawerFrom(e, "New AWS connection…");
  e.st.busy = true;
  const stay = e.t.box.pfvTabLeave("creds");
  assert.ok(stay && typeof stay.then === "function", "a promise");
  assert.strictEqual(await stay, false);
  assert.strictEqual(e.drawer.hidden, false);
  const close = e.t.box.pageLeaveHook("close");
  assert.strictEqual(await close, false, "closing the page waits for the save too");
  assert.strictEqual(e.drawer.hidden, false);
  e.st.busy = false;
  assert.strictEqual(await e.t.box.pageLeaveHook("close"), true, "answered: the drawer closes and the page may go (nothing was changed)");
  assert.strictEqual(e.drawer.hidden, true);
});

test("a forced close (the window crossed to a phone) takes the drawer with the page: nothing is asked, nothing waits", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  await openDrawerFrom(d, "New AWS connection…");
  st.busy = true;
  assert.strictEqual(t.box.pageLeaveHook("force"), true);
  assert.strictEqual(d.drawer.hidden, true);
  assert.strictEqual(d.scrim.hidden, true);
  assert.strictEqual(t.run("pfvState").drawer, false);
  assert.strictEqual(st.drops, 1, "dropped, not reported: the edit it would have added to is gone");
  assert.deepStrictEqual([d.body.children.length, d.foot.children.length], [0, 0]);
  assert.deepStrictEqual(Object.keys(t.run("pfvState").edits), []);
  assert.strictEqual(t.run("pfvKept"), null, "the block held no change: nothing is kept");
});

test("a forced close keeps what the drawer's flow had already created, as pending rows; the half-filled drawer form is lost", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st } = d;
  defineAfterRefresh(t, { aws: [{ name: "lab-a", account_id: "555555555555", region: "eu-west-1" }] });
  const first = await openDrawerFrom(d, "New AWS connection…");
  first.opts.onCreated(["lab-a"], "aws");
  await settle();
  assert.ok(rowFor(t, "cloud", "AWS", "lab-a"), "the created connection is a pending row of the block");
  st.busy = true;
  assert.strictEqual(t.box.pageLeaveHook("force"), true);
  assert.strictEqual(d.drawer.hidden, true);
  assert.deepStrictEqual([d.body.children.length, d.foot.children.length], [0, 0], "nothing typed in the drawer lingers");
  const k = keptOf(t);
  assert.strictEqual(k.edits[0].values.aws.find((e) => e.name === "lab-a").fresh, true);
  assert.deepStrictEqual(Object.keys(k.edits[0]).sort(), ["id", "init", "repair", "ui", "values"]);
  await t.box.pfvShow();
  assert.ok(rowFor(t, "cloud", "AWS", "lab-a"), "the row is back");
  assert.strictEqual(t.run("pfvState").pending, true);
  assert.strictEqual(t.run("pfvState").drawer, false, "the drawer is not");
  assert.strictEqual(d.drawer.hidden, true);
});

test("Tab cycles inside the open drawer, and a Tab from outside it goes in", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, drawer } = d;
  await openDrawerFrom(d, "New token…");
  const keys = t.doc.listeners.keydown || [];
  assert.ok(keys.length > 0, "the page listens for Tab on the document, so it also holds when focus fell to the body");
  const press = (shift) => { let stopped = false; keys.forEach((f) => f({ key: "Tab", shiftKey: shift, preventDefault: () => { stopped = true; } })); return stopped; };
  const list = drawer.querySelectorAll("");
  const first = list[0], last = list[list.length - 1];
  assert.ok(list.length >= 3 && first !== last);
  last.focus();
  assert.strictEqual(press(false), true);
  assert.strictEqual(Node.focused, first, "past the last control is the first");
  assert.strictEqual(press(true), true);
  assert.strictEqual(Node.focused, last, "before the first is the last");
  const mid = list[1];
  mid.focus();
  assert.strictEqual(press(false), false, "a Tab inside is the browser's own");
  Node.focused = null;
  assert.strictEqual(press(false), true);
  assert.strictEqual(Node.focused, first, "from the page behind, into the drawer");
  Node.focused = field(t, "add");
  assert.strictEqual(press(true), true);
  assert.strictEqual(Node.focused, last);
  // Closed, it leaves Tab alone.
  t.box.pfvCloseDrawer();
  Node.focused = null;
  assert.strictEqual(press(false), false);
  // A question on screen is not the drawer's to steal Tab from.
  const e = drawerPage({ awscEntries: PICK_AWS });
  await openDrawerFrom(e, "New token…");
  e.t.els["ask-modal"] = { open: true };
  Node.focused = null;
  let stopped = false;
  (e.t.doc.listeners.keydown || []).forEach((f) => f({ key: "Tab", shiftKey: false, preventDefault: () => { stopped = true; } }));
  assert.strictEqual(stopped, false);
});

test("focus goes into the drawer when it opens: the first control after the crumb, else the drawer itself", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  await openDrawerFrom(d, "New token…");
  const inputs = d.body.all((n) => n.tag === "input");
  assert.strictEqual(Node.focused, inputs[0], "not the crumb, which would close the drawer on Enter");
  // A flow that has drawn nothing yet (it is reading the server): the drawer takes focus, so Esc and Tab are already its own.
  const e = drawerPage({ awscEntries: PICK_AWS });
  e.t.box.credTokenAdd = (g, s, opts) => { e.st.started.push({ kind: "token", opts }); e.st.cur = opts; };
  await openDrawerFrom(e, "New token…");
  assert.strictEqual(Node.focused, e.drawer);
  assert.strictEqual(e.t.box.pfvEscTarget(), "drawer");
});

test("the drawer's markup: an aside after #manage inside the settings modal, not a .dbd or .dft, hidden until opened", () => {
  const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
  const modal = html.slice(html.indexOf('<dialog id="settings-modal"'), html.indexOf("</dialog>", html.indexOf('<dialog id="settings-modal"')));
  const at = modal.indexOf('<aside id="cred-drawer"');
  assert.ok(at > modal.indexOf('id="manage"'), "after #manage");
  assert.ok(modal.indexOf('id="cred-scrim"') > 0, "the scrim is in the modal too");
  const tag = modal.slice(at, modal.indexOf(">", at) + 1);
  assert.match(tag, /\bhidden\b/);
  assert.match(tag, /role="dialog"/);
  assert.match(tag, /aria-modal="true"/);
  assert.match(tag, /tabindex="-1"/);
  assert.match(tag, /aria-label="[^"]+"/);
  assert.doesNotMatch(tag, /class="[^"]*\b(dbd|dft)\b/, "selectSettingsTab hides every .dbd and .dft that is not its tab");
  const aside = modal.slice(at, modal.indexOf("</aside>", at));
  assert.match(aside, /id="cred-drawer-body"/);
  assert.match(aside, /id="cred-drawer-foot"/);
  assert.match(aside, /id="cred-drawer-status"[^>]*role="status"[^>]*hidden/);
});

// --- Task 15 gate fix (R67, R68) -----------------------------------------------------------------------------------------

test("the busy line goes as soon as the save has answered, whether or not the flow went on", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  const { t, st, status } = d;
  const flow = await openDrawerFrom(d, "New AWS connection…");
  st.busy = true;
  assert.strictEqual(t.box.pfvCloseDrawer(), false);
  assert.strictEqual(status.hidden, false);
  assert.ok(status.textContent.length > 0);
  // The save is over and the flow stays open (a row was refused): the host is told, and the line goes.
  st.busy = false;
  flow.opts.onIdle();
  assert.strictEqual(status.hidden, true);
  assert.strictEqual(status.textContent, "");
  assert.strictEqual(d.drawer.hidden, false, "the drawer is open still");
  // Idle with nothing said is harmless.
  flow.opts.onIdle();
  assert.strictEqual(status.hidden, true);
  // A late answer for a drawer that has gone does not touch a new one's line.
  t.box.pfvCloseDrawer();
  const again = await openDrawerFrom(d, "New token…");
  again.opts.onIdle();
  flow.opts.onIdle();
  assert.strictEqual(status.hidden, true);
});

test("the focus trap reaches a summary (the AWS Advanced fold, the App's help)", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  await openDrawerFrom(d, "New AWS connection…");
  assert.ok(d.drawer.selectors.length > 0);
  assert.ok(d.drawer.selectors.every((sel) => /(^|,)\s*summary\b/.test(sel)), "the selector lists summary: " + d.drawer.selectors[0]);
});

test("the header band is part of the drawer's markup, above the body", () => {
  const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
  const aside = html.slice(html.indexOf('<aside id="cred-drawer"'), html.indexOf("</aside>", html.indexOf('<aside id="cred-drawer"')));
  const at = (id) => aside.indexOf('id="' + id + '"');
  assert.ok(at("cred-drawer-head") > 0, "the header");
  assert.ok(at("cred-drawer-head") < at("cred-drawer-body") && at("cred-drawer-body") < at("cred-drawer-status") && at("cred-drawer-status") < at("cred-drawer-foot"), "head, body, status, foot");
});

test("the first control the drawer focuses is in the body, never the header's crumb link", async () => {
  const d = drawerPage({ awscEntries: PICK_AWS });
  await openDrawerFrom(d, "New token…");
  assert.ok(d.body.contains(Node.focused), "in the body");
  assert.ok(!d.head.contains(Node.focused));
});

// --- A block in edit is its own card (UI plan 4 Task 4; frame M2.2, digest-design §A3, §B3) ----------------------------------------
// settings-shell.test.js pins the values; these pin the markup: which element holds which, in which order, with which kit classes.

test("while a block is open, Save is the only primary and Start session is secondary", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const root = t.els["pfv-in"];
  const primaries = () => root.all((n) => classesOf(n).includes("btn-primary")).map((n) => n.textContent);
  assert.deepStrictEqual(primaries(), ["Start session"], "the overview: Start session");
  clickBtn(blockOf(t, "workspace"), "Edit");
  assert.deepStrictEqual(primaries(), ["Save"], "a block in edit: Save, and nothing else");
  assert.deepStrictEqual(classesOf(byText(root, "button", "Start session")[0]), ["btn"], "Start session steps down to a secondary");
  assert.strictEqual(root.all((n) => classesOf(n).includes("pfv-btn-primary")).length, 0, "no second button system");
  clickBtn(blockOf(t, "workspace"), "Cancel");
  assert.deepStrictEqual(primaries(), ["Start session"], "closed: Start session is the primary again");
  assert.strictEqual(t.box.pfvHeaderPrimary({ readOnly: false, editing: true }), "none");
  assert.strictEqual(t.box.pfvHeaderPrimary({ readOnly: false, editing: false }), "start");
});

test("the block foot: a top-bordered row with the status line, then Cancel (secondary) and Save (primary)", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "runs"), "Edit");
  const runs = blockOf(t, "runs");
  const foot = byCls(runs, "pfv-foot");
  assert.strictEqual(foot.length, 1);
  assert.strictEqual(foot[0].parent, runs, "the foot is the block's own last row, so the card's whole width is its");
  assert.strictEqual(runs.children[runs.children.length - 1], foot[0]);
  const kids = foot[0].children.filter((c) => c instanceof Node);
  assert.deepStrictEqual(kids.map((c) => [c.tag, c.textContent]), [["div", "Saving gives the next session a new box. Running sessions keep theirs."], ["button", "Cancel"], ["button", "Save"]], "status, Cancel, Save: Save is last");
  assert.deepStrictEqual(classesOf(kids[0]), ["pfv-status"]);
  assert.strictEqual(kids[0].getAttribute("role"), "status");
  assert.deepStrictEqual(texts(foot[0], "pfv-fnote"), ["Saving gives the next session a new box. Running sessions keep theirs."], "§A3: a box profile says what saving costs");
  assert.deepStrictEqual(classesOf(kids[1]), ["btn"], "Cancel is a secondary: never ghost, never primary");
  assert.deepStrictEqual(classesOf(kids[2]), ["btn", "btn-primary"], "Save is the primary");
  assert.strictEqual(byCls(foot[0], "pfv-fbtns").length, 0, "no wrapper: the foot is the kit's flat row");
  // Cancel is Discard once something changed, and still a plain .btn.
  const host = field(t, "tier-host");
  host.checked = true;
  host.onchange();
  const dis = byText(runs, "button", "Discard")[0];
  assert.deepStrictEqual(classesOf(dis), ["btn"]);
  assert.strictEqual(byText(runs, "button", "Cancel").length, 0);
  assert.deepStrictEqual(texts(foot[0], "pfv-fnote"), [], "choosing host: the next session is not a box");
  // Save as host…: the same button, red on clear (.pfv-btn-host), still the one .btn-primary of the view.
  const asHost = byText(runs, "button", "Save as host…")[0];
  assert.deepStrictEqual(classesOf(asHost).sort(), ["btn", "btn-primary", "pfv-btn-host"]);
  assert.strictEqual(byCls(t.els["pfv-in"], "btn-primary").length, 1);
});

test("the edited block renders as its own card: the blocks before, the editing one, the blocks after", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  const root = t.els["pfv-in"];
  const ids = (sheet) => sheet.children.map((b) => b.dataset.block);
  assert.strictEqual(byCls(root, "pfv-sheet").length, 1, "read view: one card");
  clickBtn(blockOf(t, "agent"), "Edit");
  const sheets = byCls(root, "pfv-sheet");
  assert.deepStrictEqual(sheets.map(ids), [["runs", "workspace"], ["agent"], ["cloud", "reachable", "network", "canreach", "advanced"]]);
  assert.strictEqual(blockOf(t, "agent").parent, sheets[1], "the editing section sits alone in its card");
  assert.strictEqual(byCls(sheets[1], "pfv-foot").length, 1, "its foot is inside that card");
  assert.deepStrictEqual(sheets.map((s) => s.parent), [root, root, root], "the three cards are siblings, in order, under the heading");
  const order = root.children.filter((c) => c instanceof Node && (hasCls("pfv-sheet")(c) || hasCls("pfv-sheet-h")(c)));
  assert.deepStrictEqual(order.map((c) => classesOf(c)[0]), ["pfv-sheet-h", "pfv-sheet", "pfv-sheet", "pfv-sheet"]);
  // The first block and the last block: two cards, the editing one at the end it is at.
  clickBtn(blockOf(t, "agent"), "Cancel");
  clickBtn(blockOf(t, "runs"), "Edit");
  assert.deepStrictEqual(byCls(root, "pfv-sheet").map(ids), [["runs"], ["workspace", "agent", "cloud", "reachable", "network", "canreach", "advanced"]]);
  clickBtn(blockOf(t, "runs"), "Cancel");
  clickBtn(blockOf(t, "advanced"), "Edit");
  assert.deepStrictEqual(byCls(root, "pfv-sheet").map(ids), [["runs", "workspace", "agent", "cloud", "reachable", "network", "canreach"], ["advanced"]]);
  assert.strictEqual(blockOf(t, "advanced").parent, byCls(root, "pfv-sheet")[1]);
  clickBtn(blockOf(t, "advanced"), "Cancel");
  assert.deepStrictEqual(byCls(root, "pfv-sheet").map(ids), [["runs", "workspace", "agent", "cloud", "reachable", "network", "canreach", "advanced"]], "closed: one card again");
});

test("two blocks open at once are two cards: a refusal opens a second form under the first", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  t.run("pfvOpenRepair('cloud')");
  t.run("pfvRender()");
  const sheets = byCls(t.els["pfv-in"], "pfv-sheet");
  assert.deepStrictEqual(sheets.map((s) => s.children.map((b) => b.dataset.block)), [["runs", "workspace"], ["agent"], ["cloud"], ["reachable", "network", "canreach", "advanced"]]);
});

test("the foot of a block in a narrow card is still the last child of its section, whichever block it is", async () => {
  for (const id of ["runs", "workspace", "agent", "cloud", "reachable", "network", "advanced"]) {
    const t = page();
    await t.box.pfvShow("aws-ops");
    clickBtn(blockOf(t, id), "Edit");
    const sec = blockOf(t, id);
    const parts = sec.children.filter((c) => c instanceof Node).map((c) => classesOf(c)[0]);
    assert.deepStrictEqual(parts.slice(-2), ["pfv-body", "pfv-foot"], id + ": the body, then the foot");
    assert.strictEqual(parts[0], "pfv-label", id + ": the label first");
  }
});

test("Switch to box is a kit secondary too: the page has no button system of its own left", async () => {
  const t = page();
  await t.box.pfvShow("runbook-host");
  const sw = byText(t.els["pfv-in"], "button", "Switch to box")[0];
  assert.ok(sw, "the host profile offers it");
  assert.deepStrictEqual(classesOf(sw), ["btn"]);
  assert.strictEqual(t.els["pfv-in"].all((n) => classesOf(n).some((c) => /^pfv-btn(-|$)/.test(c) && c !== "pfv-btn-host")).length, 0);
});

test("profile field labels and controls are the kit's look under the page's own classes: the segmented control is the kit .seg", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "agent"), "Edit");
  const effort = field(t, "options.effort");
  assert.deepStrictEqual(classesOf(effort), ["seg", "pfv-seg"], "Effort is a .seg of buttons, one per level");
  assert.deepStrictEqual(effort.children.filter((c) => c instanceof Node).map((b) => b.textContent), ["low", "medium", "high", "xhigh", "max"]);
  assert.strictEqual(effort.parent.tag, "fieldset");
  assert.deepStrictEqual(classesOf(effort.parent), ["pfv-field"]);
});

test("workspace mode choices are two option cards in one fieldset: the kit's .opt-card, the chosen one is-on", async () => {
  const t = page();
  const pk = await openWs(t);
  assert.ok(pk);
  const ws = blockOf(t, "workspace");
  const cards = byCls(ws, "pfv-card");
  assert.strictEqual(cards.length, 2);
  assert.strictEqual(cards[0].parent, cards[1].parent, "one fieldset holds both, so one grid puts them side by side");
  assert.deepStrictEqual(classesOf(cards[0].parent), ["pfv-field", "pfv-choice"]);
  assert.deepStrictEqual(cards.map((c) => classesOf(c)), [["opt-card", "pfv-card", "is-on"], ["opt-card", "pfv-card"]], "folder is chosen on a profile that works in its folder");
  pickMode(t, "copy");
  assert.deepStrictEqual(byCls(blockOf(t, "workspace"), "pfv-card").map((c) => classesOf(c).includes("is-on")), [false, true]);
});

// Fix round 1 (Task 4 review): .pfv-choice is a two-column grid of option cards, so a hint put in the fieldset beside the cards would be a grid cell
// of its own. settings-shell.test.js pins the rule that spans anything that is not a card across the row; this pins that the hints ARE direct
// children of the fieldset, the thing that rule matches, and that nothing but cards and hints is in there.
test("a hint in the New profile's 'Where it runs' fieldset is a direct child of the choice grid, beside no card", async () => {
  const isCard = (n) => classesOf(n).includes("pfv-card");
  const fieldsetOf = (t) => byCls(rootOf(t), "pfv-choice")[0];
  const cells = (fs) => fs.children.filter((c) => c instanceof Node && c.tag !== "legend");
  // Duplicate of a private copy: one Box card (no host card) and the sentence that says why, which must not sit in the second column.
  const t = clonePage();
  await t.box.pfvShow("gitops-clone");
  await dupFromMenu(t);
  const fs = fieldsetOf(t);
  assert.strictEqual(fs.tag, "fieldset");
  assert.deepStrictEqual(cells(fs).map((c) => [isCard(c), c.tag, classesOf(c)[0]]), [[true, "label", "opt-card"], [false, "p", "pfv-hint"]], "one card, then the hint, as direct children");
  assert.strictEqual(cells(fs)[1].textContent, "A private copy needs a box.");
  assert.strictEqual(cells(fs)[1].parent, fs, "a direct child: `.pfv-choice > :not(.pfv-card)` matches it");
  // New profile with host chosen: the two cards, then the "host holds" sentence.
  const n = await newPage();
  pickTier(n, "host");
  const nfs = fieldsetOf(n);
  assert.deepStrictEqual(cells(nfs).map((c) => [isCard(c), c.tag]), [[true, "label"], [true, "label"], [false, "p"]]);
  assert.strictEqual(cells(nfs)[2].textContent, "On this machine a profile cannot hold AWS, GitHub or tokens.");
  // Every fieldset the page draws as a choice holds only cards, hints and (not a grid item) its legend: nothing else can need the row.
  for (const root of [rootOf(t), rootOf(n)]) for (const c of byCls(root, "pfv-choice")) {
    assert.deepStrictEqual(c.children.filter((x) => x instanceof Node && !isCard(x) && x.tag !== "legend").map((x) => classesOf(x)[0]), c.children.filter((x) => x instanceof Node && classesOf(x)[0] === "pfv-hint").map(() => "pfv-hint"));
  }
});

// Task 2 (#340 S4): a section, a row and an Advanced entry say which tier they apply to, in words, as a neutral tag after the label or name.
test("a tagged block, row and Advanced entry draw Applies to ...", () => {
  const t = page();
  const tags = (n) => byCls(n, "pfv-applies");
  const block = (applies) => ({ id: "cloud", label: "Cloud and tokens", applies, edit: false, add: "", rows: [], count: 0, note: "", lines: ["Nothing yet"] });
  const boxed = t.box.pfvDrawBlock(block("box"), []);
  const label = byCls(boxed, "pfv-label")[0];
  assert.strictEqual(label.tag, "h3");
  assert.strictEqual(labelName(label), "Cloud and tokens");
  assert.deepStrictEqual(tags(label).map((n) => [n.tag, n.textContent]), [["span", "Applies to Box"]]);
  assert.strictEqual(tags(t.box.pfvDrawBlock(block(""), [])).length, 0, "an untagged block has none");
  assert.strictEqual(tags(t.box.pfvDrawBlock(block(undefined), [])).length, 0);
  const row = { kind: "Starting environment", name: "clean", sub: "", scope: "No secrets", tag: "", missing: false, applies: "host" };
  const drawn = t.box.pfvDrawRow(row, []);
  assert.deepStrictEqual(tags(drawn).map((n) => n.textContent), ["Applies to Host"]);
  assert.strictEqual(tags(t.box.pfvDrawRow(Object.assign({}, row, { applies: "" }), [])).length, 0);
  const adv = t.box.pfvDrawAdvanced({ id: "advanced", label: "Advanced", applies: "", edit: true, add: "", rows: [row, { kind: "Arguments", name: "--x", sub: "", scope: "", tag: "", missing: false, applies: "" }], count: 2, note: "", lines: ["x"], open: true, status: "2 set" }, []);
  const dds = adv.all((n) => n.tag === "dd");
  assert.deepStrictEqual(dds.map((d) => tags(d).map((n) => n.textContent)), [["Applies to Host"], []]);
});

test("a block in edit keeps its Applies to tag in the label, and an untagged one has none", async () => {
  const t = page();
  await t.box.pfvShow("aws-ops");
  clickBtn(blockOf(t, "cloud"), "Edit");
  assert.ok(blockOf(t, "cloud").className.includes("pfv-editing"));
  const cl = byCls(blockOf(t, "cloud"), "pfv-label")[0];
  assert.strictEqual(cl.tag, "h3");
  assert.strictEqual(labelName(cl), "Cloud and tokens");
  assert.deepStrictEqual(texts(cl, "pfv-applies"), ["Applies to Box"]);
  clickBtn(blockOf(t, "cloud"), "Cancel");
  clickBtn(blockOf(t, "agent"), "Edit");
  assert.ok(blockOf(t, "agent").className.includes("pfv-editing"));
  assert.deepStrictEqual(texts(byCls(blockOf(t, "agent"), "pfv-label")[0], "pfv-applies"), []);
});
