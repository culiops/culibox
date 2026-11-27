"use strict";
// keptview.js: the profile page's "Kept from boxes" section as data (keptSectionModel), the
// spawn toast's part (keptToastPart), and the drawing, which must put every box-chosen string
// on the page as text. No jsdom in this repo: a small fake DOM stands in for the drawing test.
const test = require("node:test");
const assert = require("node:assert");
const K = require("../static/keptview.js");
const F = require("../static/fsops.js");

const at = new Date(2026, 10, 27, 12, 0, 0).toISOString(); // 27 Nov, local noon: the day is the same in every zone
const entry = (path, over) => Object.assign({ path, kept_at: at, bytes: 12 * 1024, files: 3, from_box: "b", status: "kept", held_reason: "", announced: true }, over);
const store = (over) => Object.assign({ key: "aaaaaaaaaaaa", profile: "p", workspace: "/w", account: "", kept: [], not_kept: [], clashes: [], total_bytes: 0 }, over);

test("no stores, or stores with nothing to say, hide the section", () => {
  assert.strictEqual(K.keptSectionModel(null), null);
  assert.strictEqual(K.keptSectionModel(undefined), null);
  assert.strictEqual(K.keptSectionModel([]), null);
  assert.strictEqual(K.keptSectionModel([store()]), null);
  assert.strictEqual(K.keptSectionModel("x"), null);
});

test("a kept folder is one line: path, size, files, day", () => {
  const m = K.keptSectionModel([store({ kept: [entry(".culiops")] })]);
  assert.strictEqual(m.title, "Kept from boxes");
  assert.strictEqual(m.note, "Folders your boxed agents wrote outside the repo. Host sessions use your real home and don't see these.");
  assert.strictEqual(m.rows.length, 1);
  const r = m.rows[0];
  assert.strictEqual(r.line, "~/.culiops · 12 KB · 3 files · kept 27 Nov");
  assert.deepStrictEqual([r.path, r.sizeText, r.filesText, r.keptText, r.status, r.reason], [".culiops", "12 KB", "3 files", "kept 27 Nov", "kept", ""]);
  assert.strictEqual(r.key, "aaaaaaaaaaaa");
});

test("sizes and counts read plainly", () => {
  const row = (bytes, files) => K.keptSectionModel([store({ kept: [entry(".a", { bytes, files })] })]).rows[0];
  assert.strictEqual(row(0, 0).sizeText, "0 B");
  assert.strictEqual(row(900, 1).sizeText, "900 B");
  assert.strictEqual(row(900, 1).filesText, "1 file");
  assert.strictEqual(row(1536, 2).sizeText, "1.5 KB");
  assert.strictEqual(row(5 * 1024 * 1024, 2).sizeText, "5 MB");
  assert.strictEqual(row(3 * 1024 ** 3, 2).sizeText, "3 GB");
  assert.strictEqual(row("x", "y").sizeText, "");
});

test("a held folder says held and why", () => {
  const m = K.keptSectionModel([store({ kept: [entry(".big", { status: "held", held_reason: "over the size limit" })] })]);
  const r = m.rows[0];
  assert.strictEqual(r.status, "held");
  assert.strictEqual(r.reason, "over the size limit");
});

test("rows from several stores keep their own key", () => {
  const m = K.keptSectionModel([store({ key: "aaaaaaaaaaaa", kept: [entry(".a")] }), store({ key: "bbbbbbbbbbbb", kept: [entry(".b")] })]);
  assert.deepStrictEqual(m.rows.map((r) => [r.key, r.path]), [["aaaaaaaaaaaa", ".a"], ["bbbbbbbbbbbb", ".b"]]);
});

test("not-kept rows: three show, the rest are '2 more'", () => {
  const nk = (i) => ({ path: ".x" + i, reason: "a link", box: "b", seen_at: at });
  const m = K.keptSectionModel([store({ not_kept: [0, 1, 2, 3, 4].map(nk) })]);
  assert.strictEqual(m.rows.length, 0);
  assert.deepStrictEqual(m.notKept.shown.map((r) => r.path), [".x0", ".x1", ".x2"]);
  assert.deepStrictEqual(m.notKept.hidden.map((r) => r.path), [".x3", ".x4"]);
  assert.strictEqual(m.notKept.moreText, "2 more");
  assert.strictEqual(m.notKept.shown[0].line, "~/.x0 · a link");
  const few = K.keptSectionModel([store({ not_kept: [0, 1, 2].map(nk) })]);
  assert.strictEqual(few.notKept.moreText, "");
  assert.strictEqual(few.notKept.hidden.length, 0);
});

test("older copies are counted per folder", () => {
  const m = K.keptSectionModel([store({ kept: [entry(".culiops")], clashes: ["clash/b1/.culiops", "clash/b2/.culiops", "clash/b1/.config/culiops"] })]);
  assert.deepStrictEqual(m.clashes.map((c) => c.text), ["2 older copies of ~/.culiops", "1 older copy of ~/.config/culiops"]);
  assert.deepStrictEqual(m.clashes[0].paths, ["clash/b1/.culiops", "clash/b2/.culiops"]);
  assert.strictEqual(m.clashes[0].key, "aaaaaaaaaaaa");
  assert.strictEqual(K.keptSectionModel([store({ kept: [entry(".culiops")], clashes: ["clash/b1/.culiops"] })]).clashes[0].text, "1 older copy of ~/.culiops");
});

test("keptToastPart", () => {
  assert.strictEqual(K.keptToastPart([]), "");
  assert.strictEqual(K.keptToastPart(undefined), "");
  assert.strictEqual(K.keptToastPart(null), "");
  assert.strictEqual(K.keptToastPart([".culiops"]), "now keeping ~/.culiops");
  assert.strictEqual(K.keptToastPart([".culiops", ".local/share/culiops", ".config/x"]), "now keeping ~/.culiops, ~/.local/share/culiops, ~/.config/x");
  assert.strictEqual(K.keptToastPart([".a", 7, "", null]), "now keeping ~/.a");
});

test("the kept viewer id round-trips to the profile's kept file route", () => {
  const id = F.keptBoxId("my prof", "aaaaaaaaaaaa");
  assert.ok(id.startsWith("kept:"));
  assert.strictEqual(F.boxFileURL(id, ".culiops/a b.txt"), "/api/profiles/my%20prof/kept/file?key=aaaaaaaaaaaa&path=.culiops%2Fa%20b.txt");
  assert.ok(F.boxFileURL(id, "x", true).endsWith("&download=1"));
  // A session id is still a session id.
  assert.strictEqual(F.boxFileURL("s1", "/p"), "/api/sessions/s1/boxfile?path=%2Fp");
});

// --- drawing: box-chosen text is text ------------------------------------------------------------

class El {
  constructor(tag) { this.tag = tag; this.children = []; this.className = ""; this.attrs = {}; this.handlers = {}; this._text = ""; }
  set textContent(v) { this.children = []; this._text = String(v); }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); }
  set innerHTML(v) { throw new Error("innerHTML must not be used: " + v); }
  append(...k) { for (const c of k) { const n = typeof c === "string" ? Object.assign(new El("#text"), { _text: c }) : c; n.parent = this; this.children.push(n); } }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this); }
  setAttribute(k, v) { this.attrs[k] = v; }
  addEventListener(t, f) { this.handlers[t] = f; }
  walk(f) { f(this); for (const c of this.children) c.walk(f); }
}

test("a path that looks like markup is drawn as text, never parsed", () => {
  const evil = '.<img src=x onerror=alert(1)>';
  const m = K.keptSectionModel([store({
    kept: [entry(evil, { status: "held", held_reason: "<b>why</b>" })],
    not_kept: [{ path: "<script>x</script>", reason: "<i>r</i>", box: "b", seen_at: at }],
    clashes: ["clash/<u>/" + evil],
  })]);
  global.document = { createElement: (t) => new El(t) };
  try {
    const el = K.keptDrawSection(m, { onView() {}, onForget() {} });
    const all = [];
    el.walk((n) => all.push(n));
    const text = el.textContent;
    assert.ok(text.includes(evil), "the path is on the page as characters");
    assert.ok(text.includes("<b>why</b>") && text.includes("<script>x</script>") && text.includes("<i>r</i>"));
    assert.ok(!all.some((n) => n.tag === "img" || n.tag === "script" || n.tag === "b" || n.tag === "i" || n.tag === "u"), "no element was made from box text");
    assert.ok(all.some((n) => n.tag === "button" && n.textContent === "View"));
    assert.ok(all.some((n) => n.tag === "button" && n.textContent === "Forget"));
  } finally {
    delete global.document;
  }
});

test("the forget confirm says what goes", () => {
  assert.strictEqual(K.keptForgetAsk(".culiops").body, "Delete the kept copy of ~/.culiops? Boxes won't keep it again.");
  assert.strictEqual(K.keptForgetAsk(".culiops").confirmLabel, "Delete");
  assert.strictEqual(K.keptForgetAsk(".culiops").danger, true);
});

// --- the page's async parts: reload, forget, folder listing --------------------------------------

// load runs keptview.js as the browser does: plain script, globals for what the page provides.
function load(over) {
  const g = Object.assign({ document: { createElement: (t) => new El(t), getElementById: () => null }, keptBoxId: F.keptBoxId, dockOpenFile() {} }, over);
  const vm = require("node:vm");
  const src = require("node:fs").readFileSync(require.resolve("../static/keptview.js"), "utf8");
  const ctx = vm.createContext(g);
  vm.runInContext(src + "\n;this.__k = { keptReload, keptForget, keptToggleTree, keptBtn, keptState };", ctx);
  return ctx.__k;
}
const defer = () => { let res, rej; const p = new Promise((a, b) => { res = a; rej = b; }); return { p, res, rej }; };
const tick = () => new Promise((r) => setImmediate(r));
const apiStores = (name) => ({ stores: [store({ profile: name, kept: [entry(".a")] })] });

test("keptReload: an older reply landing after a newer one does not win", async () => {
  const a = defer(), b = defer();
  const replies = { A: a, B: b };
  const k = load({ api: (url) => replies[url.split("/")[3]].p });
  const pa = k.keptReload("A"), pb = k.keptReload("B");
  b.res({ stores: [store({ key: "bbbbbbbbbbbb" })] });
  await pb;
  a.res({ stores: [store({ key: "aaaaaaaaaaaa" })] });
  await pa;
  assert.strictEqual(k.keptState.name, "B");
  assert.strictEqual(k.keptState.stores[0].key, "bbbbbbbbbbbb");
});

test("forget: confirm, POST {key, path}, then reload; the button shows Forgetting... meanwhile", async () => {
  const calls = [], post = defer();
  const k = load({
    askConfirm: async () => true,
    showToast() { throw new Error("no toast expected"); },
    api: (url, opt) => { calls.push([url, opt && opt.method, opt && opt.body]); return opt ? post.p : Promise.resolve(apiStores("p")); },
  });
  const btn = k.keptBtn("Forget", "Forget ~/.a", () => {});
  const done = k.keptForget("p", { key: "aaaaaaaaaaaa", path: ".a" }, btn);
  await tick();
  assert.strictEqual(btn.textContent, "Forgetting...");
  assert.strictEqual(btn.disabled, true);
  post.res({});
  await done;
  assert.deepStrictEqual(calls[0], ["/api/profiles/p/kept/forget", "POST", JSON.stringify({ key: "aaaaaaaaaaaa", path: ".a" })]);
  assert.strictEqual(calls[1][0], "/api/profiles/p/kept");
  assert.strictEqual(k.keptState.name, "p");
});

test("forget: a refusal shows the server's text as it is and brings the button back", async () => {
  const toasts = [];
  const k = load({
    askConfirm: async () => true,
    showToast: (m, o) => toasts.push([m, o]),
    api: async (url, opt) => { if (opt) throw new Error("a session is using it"); throw new Error("no reload expected"); },
  });
  const btn = k.keptBtn("Forget", "Forget ~/.a", () => {});
  await k.keptForget("p", { key: "aaaaaaaaaaaa", path: ".a" }, btn);
  assert.strictEqual(JSON.stringify(toasts), JSON.stringify([["a session is using it", { error: true }]]));
  assert.strictEqual(btn.textContent, "Forget");
  assert.strictEqual(btn.disabled, false);
});

test("forget: a No on the confirm sends nothing", async () => {
  const k = load({ askConfirm: async () => false, api() { throw new Error("no call expected"); } });
  const btn = k.keptBtn("Forget", "x", () => {});
  await k.keptForget("p", { key: "k", path: ".a" }, btn);
  assert.strictEqual(btn.textContent, "Forget");
});

test("folder listing: quick clicks before the reply neither throw nor list twice", async () => {
  const replies = [];
  const k = load({ api: () => { const d = defer(); replies.push(d); return d.p; } });
  const row = new El("div");
  row.querySelector = () => null;
  k.keptToggleTree("p", "aaaaaaaaaaaa", [".a"], row);
  const li = row.children[0].children[0];
  const btn = li.children[0];
  btn.handlers.click(); btn.handlers.click(); btn.handlers.click();
  const list = { entries: [{ name: "x.txt", is_dir: false }, { name: "y.txt", is_dir: false }] };
  for (const d of replies) d.res(list);
  await tick(); await tick();
  const uls = li.children.filter((c) => c.tag === "ul");
  const items = (u) => u.children.length;
  assert.ok(uls.length <= 1, "at most one open listing");
  assert.ok(uls.every((u) => items(u) === 2), "no duplicated entries");
});
