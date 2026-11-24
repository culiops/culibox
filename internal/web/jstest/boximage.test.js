"use strict";
// boximage.js: the read-only "Box image" rows in Settings -> About (#156 Task 7), fed by GET /api/box-image. The rows are a
// pure function of the report; loadBoxImage paints them with textContent only, against the shared fake DOM.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");
const { boxImageRows, shortDigest, boxImageRepoDigest } = require("../static/boximage.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

const H1 = "3f2a" + "0".repeat(56) + "9c1d"; // 64 hex
const H2 = "81be" + "1".repeat(56) + "07aa";
const REPO = "ghcr.io/culiops/culibox";
const OLD = REPO + "@sha256:" + H1;
const NEW = REPO + "@sha256:" + H2;
// Local-time dates: the rows print the user's local day, so the fixtures are built from local parts.
const local = (y, mo, d, h, mi) => new Date(y, mo - 1, d, h, mi).toISOString();

const info = (over) => Object.assign({
  present: true, id: "sha256:" + "a".repeat(64), repo_digests: [OLD], created: local(2026, 11, 6, 12, 0),
  arch: "amd64", hash: "h", claude_code: "2.1.292",
}, over);
const report = (over) => Object.assign({
  configured: OLD, explicit: true, info: info(), verdict: "current", check_on: true,
}, over);
const texts = (rows) => rows.map((r) => (r.label ? r.label + ": " : "") + r.text + (r.note ? " " + r.note : ""));
const byText = (rows, re) => rows.find((r) => re.test(r.text));

test("shortDigest keeps sha256: plus the first and last 4 of the hex, anything else untouched", () => {
  assert.equal(shortDigest("sha256:" + H1), "sha256:3f2a…9c1d");
  assert.equal(shortDigest(OLD), REPO + "@sha256:3f2a…9c1d");
  assert.equal(shortDigest("no digest here"), "no digest here");
  assert.equal(shortDigest('"box_image": "' + NEW + '"'), '"box_image": "' + REPO + "@sha256:81be…07aa" + '"');
});

test("the digest shown is the repo digest of the configured repo, else the first, else the id", () => {
  const other = "docker.io/library/other@sha256:" + "b".repeat(64);
  const i = info({ repo_digests: [other, OLD] });
  assert.equal(boxImageRepoDigest(report({ info: i, configured: OLD })), OLD);
  assert.equal(boxImageRepoDigest(report({ info: i, configured: REPO + ":2026-11-01" })), OLD);
  assert.equal(boxImageRepoDigest(report({ info: i, configured: "culiops/elsewhere:x" })), other);
  assert.equal(boxImageRepoDigest(report({ info: info({ repo_digests: [] }) })), "sha256:" + "a".repeat(64));
  assert.equal(boxImageRepoDigest(report({ info: undefined })), "");
});

test("current: the digest row with Copy, the facts line, and the one status line", () => {
  const rows = boxImageRows(report());
  assert.deepEqual(rows[0], {
    label: "Box image", text: REPO + "@sha256:3f2a…9c1d", code: true, title: OLD, copy: OLD,
  });
  assert.equal(rows[1].text, "linux/amd64 · built 2026-11-06 · Claude Code 2.1.292");
  assert.equal(rows[1].muted, true, "the facts line is secondary text");
  assert.ok(!rows[2].muted && !rows[2].fine, "the status line is not");
  assert.equal(rows[2].text, "Current: this is the image for this version");
  assert.equal(rows.length, 3);
});

test("current-local says it was built here", () => {
  const rows = boxImageRows(report({ verdict: "current-local" }));
  assert.ok(byText(rows, /^Current: built here from this version's Dockerfile$/));
});

test("facts the image does not carry are left out of the facts line", () => {
  const rows = boxImageRows(report({ info: info({ created: undefined, claude_code: undefined, arch: "arm64" }) }));
  assert.equal(rows[1].text, "linux/arm64");
  const none = boxImageRows(report({ info: info({ created: "garbage", claude_code: undefined, arch: undefined }) }));
  assert.ok(!none.some((r) => /linux|built|Claude Code/.test(r.text)));
});

test("update: the block with the new image, download size, config line with Copy, then run, checked", () => {
  const rows = boxImageRows(report({
    verdict: "update", want: NEW, want_source: "registry", want_created: local(2026, 11, 20, 12, 0),
    want_claude_code: "2.1.311", download_mb: "212",
    config_line: '"box_image": "' + NEW + '"', setup_cmd: "culibox box setup", checked_at: local(2026, 11, 24, 9, 12),
  }));
  assert.ok(byText(rows, /^Built from an older Dockerfile than this CuliBox$/));
  const want = rows.find((r) => r.label === "For this version");
  assert.equal(want.text, REPO + "@sha256:81be…07aa");
  assert.equal(want.title, NEW);
  assert.equal(want.note, "(Claude Code 2.1.311, built 2026-11-20)");
  assert.ok(byText(rows, /^about 212 MB to download$/));
  const cfg = rows.find((r) => r.code && r.copy && /box_image/.test(r.text));
  assert.equal(cfg.text, '"box_image": "' + REPO + "@sha256:81be…07aa" + '"');
  assert.equal(cfg.copy, '"box_image": "' + NEW + '"');
  assert.equal(cfg.title, cfg.copy);
  const run = byText(rows, /^then run: culibox box setup$/);
  assert.equal(run.note, undefined);
  const checked = rows[rows.indexOf(run) + 1];
  assert.deepEqual(checked, { label: "", text: "Checked 2026-11-24 09:12 · nothing changes until you do", fine: true });
});

test("update without a config line (the release default) says run, with no config row", () => {
  const rows = boxImageRows(report({
    verdict: "update", want: NEW, want_source: "baked", explicit: false, configured: REPO + ":latest", setup_cmd: "culibox box setup",
  }));
  assert.ok(byText(rows, /^run: culibox box setup$/));
  assert.ok(!rows.some((r) => r.copy && /box_image/.test(r.text)));
  assert.ok(!rows.some((r) => /MB to download/.test(r.text)));
  assert.equal(rows.find((r) => r.label === "For this version").note, undefined);
  assert.ok(!rows.some((r) => /Checked/.test(r.text)), "no checked_at, no Checked row");
});

test("stale-unknown names why: the registry's error, not checked yet, or the check being off", () => {
  const base = { verdict: "stale-unknown", check_on: true };
  const msg = (over) => boxImageRows(report(Object.assign({}, base, over))).find((r) => /^Built from an older/.test(r.text)).text;
  assert.equal(msg({ check_err: "the registry could not be reached" }),
    "Built from an older Dockerfile than this CuliBox. Whether CI published this version's image is unknown (the registry could not be reached)");
  assert.match(msg({}), /unknown \(not checked yet\)$/);
  assert.match(msg({ check_on: false, check_err: "stale error" }), /unknown \(check off: image_check is false\)$/);
  assert.ok(!boxImageRows(report(Object.assign({}, base, { check_err: "x" }))).some((r) => r.label === "For this version"));
});

test("missing and unknown: the configured ref and one line, no digest to show", () => {
  const miss = boxImageRows(report({ verdict: "missing", info: { present: false } }));
  assert.deepEqual(miss[0], { label: "Box image", text: OLD.replace(H1, "3f2a…9c1d"), code: true, title: OLD });
  assert.equal(miss[1].text, "Not on the daemon: run culibox box setup");
  const unk = boxImageRows(report({ verdict: "unknown", info: undefined }));
  assert.equal(unk[0].label, "Box image");
  assert.ok(!unk[0].copy);
  assert.equal(unk[unk.length - 1].text, "Box image details unknown (docker did not answer)");
});

test("the weekly scan row: a link to the issue built from the number, no row without a scan", () => {
  const rows = boxImageRows(report({ scan: { number: 357, count: 680 } }));
  const scan = rows[rows.length - 1];
  assert.equal(scan.label, "Weekly scan");
  assert.equal(scan.text, "680 fixable HIGH/CRITICAL in the published images (#357)");
  assert.equal(scan.href, "https://github.com/culiops/culibox/issues/357");
  const clean = boxImageRows(report({ scan: { number: 358, count: 0 } }));
  assert.equal(clean[clean.length - 1].text, "no fixable HIGH/CRITICAL in the published images");
  assert.equal(clean[clean.length - 1].href, undefined);
  assert.ok(!boxImageRows(report()).some((r) => r.label === "Weekly scan"));
});

test("a scan number that is not a positive integer gives no link and no URL", () => {
  for (const number of ["357/../../x", 3.5, -1, 0, null, "<b>", 1e21]) {
    const rows = boxImageRows(report({ scan: { number, count: 4 } }));
    const scan = rows[rows.length - 1];
    assert.equal(scan.href, undefined, String(number));
    assert.equal(scan.text, "4 fixable HIGH/CRITICAL in the published images");
  }
  for (const count of ["4", -2, 1.5, undefined]) {
    assert.ok(!boxImageRows(report({ scan: { number: 5, count } })).some((r) => r.label === "Weekly scan"), String(count));
  }
});

test("a report that is not an object gives no rows", () => {
  for (const r of [null, undefined, 3, "x", []]) assert.deepEqual(boxImageRows(r), []);
});

// --- loadBoxImage against the fake DOM ------------------------------------------------------------------------------------------
function page(apiImpl, win) {
  const doc = { created: [] };
  const mk = (tag) => { doc.created.push(tag); const n = new Node(tag, doc); n.ev = {}; return n; };
  doc.createElement = mk;
  const el = mk("div");
  el.hidden = true;
  doc.getElementById = (id) => (id === "about-image" ? el : null);
  doc.created.length = 0;
  const copied = [], opened = [];
  const ctx = {
    document: doc,
    api: apiImpl,
    copyText: (t, cb) => { copied.push(t); cb(true); },
    openAgentLink: (u, c) => { opened.push([u, c]); return Promise.resolve(true); },
    showToast: () => {},
  };
  if (win) ctx.window = win;
  vm.createContext(ctx);
  new vm.Script(read("boximage.js"), { filename: "boximage.js" }).runInContext(ctx);
  return { ctx, el, doc, copied, opened };
}
const all = (n, out = []) => { out.push(n); n.children.forEach((c) => all(c, out)); return out; };

test("loadBoxImage fetches /api/box-image and shows the rows", async () => {
  const urls = [];
  const p = page(async (u) => { urls.push(u); return report({ scan: { number: 357, count: 680 } }); });
  await p.ctx.loadBoxImage();
  assert.deepEqual(urls, ["/api/box-image"]);
  assert.equal(p.el.hidden, false);
  assert.match(p.el.textContent, /Box image/);
  assert.match(p.el.textContent, /Current: this is the image for this version/);
  const code = all(p.el).find((n) => /font-code|bi-code/.test(n.className) && /sha256:3f2a/.test(n.textContent));
  assert.ok(code, "the digest is in a code span");
  assert.equal(code.getAttribute("title"), OLD);
});

test("Copy copies the full value; the scan link opens through openAgentLink, not by navigation", async () => {
  const p = page(async () => report({ scan: { number: 357, count: 680 } }));
  await p.ctx.loadBoxImage();
  const btn = all(p.el).find((n) => n.tag === "button");
  btn.click();
  assert.deepEqual(p.copied, [OLD]);
  const a = all(p.el).find((n) => n.tag === "a");
  assert.equal(a.getAttribute("href"), "https://github.com/culiops/culibox/issues/357");
  let prevented = false;
  a.onclick({ preventDefault() { prevented = true; } });
  assert.ok(prevented);
  assert.deepEqual(p.opened, [["https://github.com/culiops/culibox/issues/357", { source: "settings" }]]);
});

test("report text is text: markup in configured or check_err creates no element", async () => {
  const evil = '<img src=x onerror="alert(1)">';
  const p = page(async () => report({ configured: evil, info: { present: false }, verdict: "stale-unknown", check_err: evil }));
  await p.ctx.loadBoxImage();
  assert.ok(p.el.textContent.includes(evil), "shown as text");
  assert.ok(!p.doc.created.includes("img"));
  assert.ok(!all(p.el).some((n) => n.tag === "img"));
});

test("a failed fetch leaves the container hidden and empty; a success then a failure clears it", async () => {
  const p = page(async () => { const e = new Error("404"); e.status = 404; throw e; });
  await p.ctx.loadBoxImage();
  assert.equal(p.el.hidden, true);
  assert.equal(p.el.children.length, 0);
  assert.equal(p.el.textContent, "");
  let fail = false;
  const q = page(async () => { if (fail) throw new Error("net"); return report(); });
  await q.ctx.loadBoxImage();
  assert.equal(q.el.hidden, false);
  fail = true;
  await q.ctx.loadBoxImage();
  assert.equal(q.el.hidden, true);
  assert.equal(q.el.children.length, 0);
});

test("an answer that is not a report keeps it hidden", async () => {
  const p = page(async () => "<html>");
  await p.ctx.loadBoxImage();
  assert.equal(p.el.hidden, true);
});

test("the rows come in three groups, one per labelled row, each with its continuation lines", async () => {
  const p = page(async () => report({
    verdict: "update", want: NEW, want_source: "registry", download_mb: "5", config_line: '"box_image": "' + NEW + '"',
    checked_at: local(2026, 11, 24, 9, 12), scan: { number: 357, count: 680 },
  }));
  await p.ctx.loadBoxImage();
  const groups = p.el.children.filter((c) => c.className === "bi-group");
  assert.equal(groups.length, p.el.children.length, "only groups sit directly in the container");
  assert.deepEqual(groups.map((g) => g.querySelector(".bi-l").textContent), ["Box image", "For this version", "Weekly scan"]);
  assert.ok(/Built from an older/.test(groups[0].textContent) && !/Built from an older/.test(groups[1].textContent));
  assert.ok(/Checked 2026-11-24 09:12/.test(groups[1].textContent));
  const rowOf = (re) => all(p.el).find((n) => n.className === "bi-row" && re.test(n.textContent));
  assert.ok(rowOf(/Checked/) !== rowOf(/then run/), "Checked is its own line");
  assert.ok(all(p.el).some((n) => /bi-fine/.test(n.className) && /Checked/.test(n.textContent)));
  assert.ok(all(p.el).some((n) => /bi-muted/.test(n.className) && /linux\/amd64/.test(n.textContent)));
});

test("an older answer never overwrites a newer one", async () => {
  const waits = [];
  const p = page(() => new Promise((res) => waits.push(res)));
  const first = p.ctx.loadBoxImage();
  const second = p.ctx.loadBoxImage();
  waits[1](report({ verdict: "current-local" }));
  await second;
  waits[0](report({ verdict: "current" }));
  await first;
  assert.match(p.el.textContent, /built here from this version/);
  assert.doesNotMatch(p.el.textContent, /this is the image for this version/);
  // An older failure does not hide a newer success either.
  const q = page(() => new Promise((res, rej) => waits.push({ res, rej })));
  waits.length = 0;
  const a = q.ctx.loadBoxImage();
  const b = q.ctx.loadBoxImage();
  waits[1].res(report());
  await b;
  waits[0].rej(new Error("late"));
  await a;
  assert.equal(q.el.hidden, false);
});

test("a phone does not fetch; a desktop width does; no matchMedia fetches", async () => {
  const run = async (win) => {
    let calls = 0;
    const p = page(async () => { calls++; return report(); }, win);
    await p.ctx.loadBoxImage();
    return { calls, hidden: p.el.hidden };
  };
  const asked = [];
  const mm = (matches) => ({ matchMedia: (q) => { asked.push(q); return { matches }; } });
  assert.deepEqual(await run(mm(false)), { calls: 0, hidden: true });
  assert.deepEqual(await run(mm(true)), { calls: 1, hidden: false });
  assert.deepEqual(asked, ["(min-width: 701px)", "(min-width: 701px)"]);
  assert.deepEqual(await run({}), { calls: 1, hidden: false });
  assert.deepEqual(await run(undefined), { calls: 1, hidden: false });
});

// --- wiring ---------------------------------------------------------------------------------------------------------------------
test("index.html: the hidden container follows the runtime line and the script sits beside bugreport.js", () => {
  const html = read("index.html");
  assert.match(html, /<p id="about-runtime"><\/p>\s*<div id="about-image" hidden><\/div>\s*<p><button type="button" class="btn" id="kbd-open-about">/);
  assert.ok(html.includes('<script src="/boximage.js"></script>'));
  assert.ok(html.indexOf('<script src="/boximage.js"></script>') > html.indexOf('<script src="/bugreport.js"></script>'));
});

test("loadAboutInfo calls loadBoxImage when it is defined", () => {
  assert.match(read("bugreport.js"), /typeof loadBoxImage === "function"\) loadBoxImage\(\)/);
});

test("the phone hides the rows by its own rule, and the pinned list is untouched", () => {
  const app = read("app.css");
  assert.match(app, /#about-image \{ display: none; \}/);
  assert.ok(!/#about-runtime, #set-signout, \.set-head, #acct-desk-note, #about-image/.test(app));
});

test("desktop.css draws the rows in the About card with kit tokens only", () => {
  const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter((m) => /#about-image|\.bi-/.test(m[1]));
  assert.ok(rules.length >= 3, "rules for the container, rows and digests");
  const body = rules.map((m) => m[2]).join(" ");
  assert.match(body, /var\(--panel\)/);
  assert.match(body, /var\(--line\)/);
  assert.match(body, /var\(--font-code\)/);
  assert.ok(!/#[0-9a-f]{3,8}\b|rgb\(/i.test(body), "no literal colours");
  assert.ok(!/font-weight:\s*(700|800|900|bold)/.test(body));
  // Each group is a card row divided like the others; the lines in it sit 4px apart; secondary text is --muted.
  const g = /#about-image \.bi-group \{([^}]*)\}/.exec(css);
  assert.ok(g, "a rule for the groups");
  assert.match(g[1], /padding: 12px 16px/);
  assert.match(g[1], /border-top: 1px solid var\(--line\)/);
  assert.match(g[1], /flex-direction: column/);
  assert.match(g[1], /gap: 4px/);
  assert.match(/#about-image \.bi-fine \{([^}]*)\}/.exec(css)[1], /color: var\(--muted\); font-size: var\(--t-sm\)/);
  assert.match(/#about-image \.bi-muted \{([^}]*)\}/.exec(css)[1], /color: var\(--muted\)/);
  // Nothing outside the desktop media block can show the container: every #about-image rule is in it, and [hidden] still wins.
  assert.match(css, /#about-image\[hidden\] \{ display: none; \}/);
  assert.ok(!/(^|\})\s*#about-image/m.test(css.replace(/@media \(min-width: 701px\) \{[\s\S]*$/, "")));
  // The runtime row is no longer the card's only top row once the image rows follow it: it keeps the top radius, the last row the bottom one.
  assert.match(css, /#about > #about-runtime \{[^}]*border-radius: var\(--r-card\) var\(--r-card\) 0 0/);
});
