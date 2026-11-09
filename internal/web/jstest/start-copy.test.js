"use strict";
// Task 13 (#177, founder call 1): "spawn" becomes "start" in desktop non-button text; the phone keeps its words.
// Also the single End-both toast (R25) and the neutral warn toast (#184).
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { reachRows, reachModel } = require("../static/spawnpreview.js");
const { notHomeFix } = require("../static/workhome.js");
const { baseScope, awsRailRows, awsConnRailRows } = require("../static/sessionrow.js");
const { accessModel } = require("../static/accessmodel.js");
const { toastFailText } = require("../static/toaststack.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

const sre = { name: "p", runtime: "box", bin: "claude", claude: true, aws_connections: ["c1"], network: "allowlist" };
const meta = { aws_connections: [{ name: "c1", prod: false, write_capable: false }] };
const locked = { store: { initialized: true, locked: true, platform: "linux" }, aws: [{ name: "c1", elevate: { role_arn: "arn:x", max_minutes: 60 } }] };

test("reach row: desktop says start, the phone (no flag) keeps spawn", () => {
  const row = (desktop) => reachRows({ profile: sre, tier: "box", cwd: "~/x", meta, creds: locked, desktop }).find((r) => r.k === "Store").v;
  assert.strictEqual(row(true), "credential store locked — unlock before starting");
  assert.strictEqual(row(false), "credential store locked — unlock before spawn");
  assert.strictEqual(row(undefined), "credential store locked — unlock before spawn");
  assert.strictEqual(reachModel({ profile: sre, tier: "box", meta, creds: locked, desktop: true }).store.v, "credential store locked — unlock before starting");
});

test("work-home fix: desktop says start, the phone keeps spawn", () => {
  const nh = { reason: "checked-out", branch: "feat" };
  assert.strictEqual(notHomeFix("p", "main", nh, true), "switch your repo off box/feat; it comes home at the next exit or start");
  assert.strictEqual(notHomeFix("p", "main", nh), "switch your repo off box/feat; it comes home at the next exit or spawn");
});

test("read-only gate tooltip: desktop says at start, the phone keeps at spawn", () => {
  const sso = { mode: "sso_direct", hours: 8, base_access: "none_found" };
  assert.match(baseScope(sso, true).title, /^the permission set passed the read-only gate at start · /);
  assert.match(baseScope(sso).title, /^the permission set passed the read-only gate at spawn · /);
  const rail = (desktop) => awsRailRows({ aws: sso }, null, Date.now(), desktop).find((r) => r.k === "scope").title;
  assert.match(rail(true), /gate at start/);
  assert.match(rail(false), /gate at spawn/);
  const conn = (desktop) => awsConnRailRows({ aws_connections: [Object.assign({ name: "x", label: "x", account_id: "1", mode: "sso_direct" }, { base_access: "none_found" })] }, null, Date.now(), false, desktop);
  assert.match(JSON.stringify(conn(true)), /gate at start/);
  assert.match(JSON.stringify(conn(false)), /gate at spawn/);
  const acc = (desktop) => accessModel({ session: { id: "s", aws_connections: [{ name: "x", label: "x", account_id: "1", mode: "sso_direct", base_access: "none_found" }] }, desktop });
  assert.match(JSON.stringify(acc(true)), /gate at start/);
  assert.match(JSON.stringify(acc(false)), /gate at spawn/);
});

test("cost tooltip: desktop pane header says since start", () => {
  assert.match(read("tabs.js"), /Session cost \(all conversations since start, as Analytics counts it\)/);
  assert.doesNotMatch(read("tabs.js"), /since spawn/);
});

test("pane placeholder: index.html keeps the phone's words; the desktop boot writes Start or open", () => {
  assert.match(read("index.html"), /Spawn or open a session\. Panes appear here\./);
  assert.match(read("app.js"), /Start or open a session\. Panes appear here\./);
});

test("started toast: desktop Started, phone Spawned", () => {
  const app = read("app.js");
  assert.match(app, /mobileActive\(window\.innerWidth\) \? "Spawned" : "Started"/);
});

test("#184: .toast.is-warn is neutral (no amber), .is-error stays red", () => {
  const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = /\.toast\.is-warn\s*\{([^}]*)\}/.exec(css);
  if (rule) assert.doesNotMatch(rule[1], /amber/);
  assert.doesNotMatch(css, /\.toast\.is-warn[^{]*\{[^}]*amber-tint/);
  assert.match(css, /\.toast\.is-error\s*\{[^}]*red-tint/);
});

// --- End both (R25) ---------------------------------------------------------------
function fnSrc(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

const SESS = [
  { id: "p", name: "agent-box", status: "live", created: "2026-10-27T09:00:00Z" },
  { id: "s1", name: "sh-a", parent_id: "p", status: "live", created: "2026-10-27T09:10:00Z" },
  { id: "s2", name: "sh-b", parent_id: "p", status: "live", created: "2026-10-27T09:20:00Z" },
];

function endEnv({ width = 1280, failIds = [] } = {}) {
  const log = [], toasts = [], closed = [], deleted = [];
  const endedByMe = new Map();
  const ctx = vm.createContext({
    window: { innerWidth: width },
    mobileActive: (w) => Number(w) <= 700,
    lastSessions: SESS,
    killing: new Set(),
    endedByMe, endedMark: (m, id, t) => m.set(id, t),
    refreshSessions: async () => {},
    api: async (url) => {
      const id = decodeURIComponent(url.split("/").pop());
      deleted.push(id);
      if (failIds.includes(id)) throw Object.assign(new Error("docker stop failed"), { status: 500 });
    },
    pendingToast: (text) => { log.push(["pending", text]); return (r) => log.push(["done", r]); },
    toastFailText,
    showToast: (msg, opts) => toasts.push([msg, opts]),
    closeTerminal: (id) => closed.push(id),
    encodeURIComponent, decodeURIComponent, Date, Set, Map, Promise, Array,
  });
  const src = read("app.js");
  new vm.Script(read("shellnest.js") + "\n" + fnSrc(src, "killSession") + "\n" + fnSrc(src, "killSessions")
    + "\nglobalThis.killSessions = killSessions;").runInContext(ctx);
  const plain = (x) => JSON.parse(JSON.stringify(x));
  return { run: (ids) => ctx.killSessions(ids), log: () => plain(log), toasts: () => plain(toasts), closed, deleted, endedByMe, ctx };
}

test("End both: one pending toast becomes one Ended toast naming the session and the shell", async () => {
  const t = endEnv();
  await t.run(["p", "s1"]);
  assert.deepStrictEqual(t.log(), [["pending", "Ending agent-box and shell 1…"], ["done", { msg: "Ended agent-box and shell 1", opts: { desktopOnly: true } }]]);
  assert.deepStrictEqual(t.toasts(), []);
  assert.deepStrictEqual(t.deleted, ["p", "s1"]);
  assert.deepStrictEqual(t.closed, ["p", "s1"]);
  assert.deepStrictEqual([...t.endedByMe.keys()].sort(), ["p", "s1"], "both ids marked ended-by-me");
});

test("End both with two shells joins as an English list", async () => {
  const t = endEnv();
  await t.run(["p", "s1", "s2"]);
  assert.strictEqual(t.log()[0][1], "Ending agent-box, shell 1 and shell 2…");
  assert.strictEqual(t.log()[1][1].msg, "Ended agent-box, shell 1 and shell 2");
  assert.deepStrictEqual([...t.endedByMe.keys()].sort(), ["p", "s1", "s2"]);
});

test("End both, one fails: today's error toast for that id, the success names only what ended, the failed id is unmarked", async () => {
  const t = endEnv({ failIds: ["s1"] });
  await t.run(["p", "s1"]);
  const log = t.log();
  assert.deepStrictEqual(log[0], ["pending", "Ending agent-box and shell 1…"]);
  assert.deepStrictEqual(log[1], ["done", { msg: "Ended agent-box", opts: { desktopOnly: true } }]);
  assert.deepStrictEqual(t.toasts(), [["End failed: docker stop failed",
    { error: true, human: "Could not end sh-a.", detail: "docker stop failed (HTTP 500)" }]]);
  assert.deepStrictEqual(t.closed, ["p"]);
  assert.deepStrictEqual([...t.endedByMe.keys()], ["p"], "the failed shell is still running: not ours");
});

test("End both, all fail: the pending toast becomes the first error, the others are their own toasts", async () => {
  const t = endEnv({ failIds: ["p", "s1"] });
  await t.run(["p", "s1"]);
  assert.strictEqual(t.log()[1][1].opts.error, true);
  assert.strictEqual(t.log()[1][1].opts.human, "Could not end agent-box.");
  assert.strictEqual(t.toasts().length, 1);
  assert.strictEqual(t.endedByMe.size, 0);
});

test("End both on the phone keeps today's per-session toasts", async () => {
  const t = endEnv({ width: 390 });
  await t.run(["p", "s1"]);
  assert.deepStrictEqual(t.log().filter((e) => e[0] === "pending").map((e) => e[1]), ["Ending agent-box…", "Ending sh-a…"]);
});

test("a single id uses killSession as before", async () => {
  const t = endEnv();
  await t.run(["p"]);
  assert.deepStrictEqual(t.log(), [["pending", "Ending agent-box…"], ["done", { msg: "Ended agent-box", opts: { desktopOnly: true } }]]);
});

test("confirmKill hands every id to killSessions", () => {
  assert.match(fnSrc(read("app.js"), "confirmKill"), /await killSessions\(ids\)/);
});
