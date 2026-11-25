"use strict";
// The conversion's wiring (#371): copyDialogRun's loop on the page's globals (stubbed in a vm), and where the page calls it.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function world(over) {
  const calls = [];
  const box = Object.assign({
    calls,
    api: async (p, o) => { calls.push([p, o && o.method, o && o.body ? JSON.parse(o.body) : null]); return { name: "claude", bin: "claude", rev: "r1", schema: "s1" }; },
    withRev: (b, l) => Object.assign({}, b, { rev: l.rev }),
    withSchema: (b, s) => Object.assign({}, b, { schema: s }),
    pfSchema: { get: () => "s1" },
    refreshProfilesQuiet: async () => { calls.push("refresh"); },
  }, over);
  box.globalThis = box;
  const ctx = vm.createContext(box);
  new vm.Script(read("copydialog.js"), { filename: "copydialog.js" }).runInContext(ctx);
  return { box, ctx, calls, run: (code) => new vm.Script(code).runInContext(ctx) };
}
const refusal = { repo: "/home/u/repo", suggested: "/home/u/box-clones/claude", worktree: false, inside: false, lfs: false, git_home: false };
const o = { profileName: "claude", profile: { name: "claude" }, home: "/home/u", hostOk: true };

test("copyDialogRun: a confirmed dialog saves the profile as a copy (clone_of, default_cwd, rev, schema) and refreshes the list", async () => {
  const t = world({});
  t.run("copyDialogAsk = async () => ({ outcome: 'confirm', dir: '/home/u/box-clones/claude', ack: false });");
  const r = await t.run("copyDialogRun(" + JSON.stringify(refusal) + ", " + JSON.stringify(o) + ")");
  assert.strictEqual(r.dir, "/home/u/box-clones/claude");
  const put = t.calls.find((c) => c[1] === "PUT");
  assert.deepStrictEqual(put[2], { name: "claude", bin: "claude", clone_of: "/home/u/repo", default_cwd: "/home/u/box-clones/claude", rev: "r1", schema: "s1" });
  assert.strictEqual(t.calls[t.calls.length - 1], "refresh");
});

test("copyDialogRun: backing out or picking Host saves nothing", async () => {
  for (const [out, want] of [["cancel", null], ["host", { host: true }]]) {
    const t = world({});
    t.run("copyDialogAsk = async () => ({ outcome: '" + out + "', dir: '', ack: false });");
    const r = await t.run("copyDialogRun(" + JSON.stringify(refusal) + ", " + JSON.stringify(o) + ")");
    assert.strictEqual(JSON.stringify(r), JSON.stringify(want)); // a vm's objects have their own prototypes
    assert.strictEqual(t.calls.length, 0);
  }
});

test("copyDialogRun: inplace_history comes back into the dialog with the server's words and the box; the ticked box resends with ack_inplace_history", async () => {
  let puts = 0;
  const t = world({
    api: async (p, op) => {
      if (op && op.method === "PUT") {
        puts++;
        const body = JSON.parse(op.body);
        if (!body.ack_inplace_history) throw Object.assign(new Error("the .git may be box-written"), { status: 400, code: "inplace_history" });
        return { rev: "r2", body };
      }
      return { name: "claude", rev: "r1" };
    },
  });
  const asks = [];
  t.box.record = (a) => { asks.push(a); return asks.length === 1 ? { outcome: "confirm", dir: "/c/d", ack: false } : { outcome: "confirm", dir: "/c/d", ack: true }; };
  t.run("copyDialogAsk = async (r, a) => record(a);");
  const r = await t.run("copyDialogRun(" + JSON.stringify(refusal) + ", " + JSON.stringify(o) + ")");
  assert.strictEqual(r.dir, "/c/d");
  assert.strictEqual(puts, 2);
  assert.strictEqual(asks[0].ackAsked, false);
  assert.strictEqual(asks[1].ackAsked, true);
  assert.strictEqual(asks[1].note, "the .git may be box-written");
  assert.strictEqual(asks[1].dir, "/c/d", "the location the user typed stays");
});

test("copyDialogRun: another save failure comes back into the dialog with its words, and a back-out then ends it", async () => {
  const t = world({
    api: async (p, op) => { if (op && op.method === "PUT") throw Object.assign(new Error("a copy must be under your home"), { status: 400 }); return { name: "claude", rev: "r1" }; },
  });
  const asks = [];
  t.box.record = (a) => { asks.push(a); return asks.length === 1 ? { outcome: "confirm", dir: "/etc/x", ack: false } : { outcome: "cancel", dir: "", ack: false }; };
  t.run("copyDialogAsk = async (r, a) => record(a);");
  const r = await t.run("copyDialogRun(" + JSON.stringify(refusal) + ", " + JSON.stringify(o) + ")");
  assert.strictEqual(r, null);
  assert.strictEqual(asks[1].note, "Could not make this profile a private copy: a copy must be under your home");
  assert.strictEqual(asks[1].ackAsked, false);
});

test("the editors save through cdSendProfile; the dialog's own ack is the same body", () => {
  assert.match(read("profilepage.js"), /cdSendProfile\("\/api\/profiles\/" \+ encodeURIComponent\(name\), "PUT", body\)/);
  assert.match(read("pfv-new.js"), /cdSendProfile\("\/api\/profiles", "POST", body\)/);
  assert.match(read("app.js"), /cdSendProfile\(path, editing \? "PUT" : "POST"/);
  const t = world({});
  const sent = [];
  t.box.api = async (p, op) => { sent.push(JSON.parse(op.body)); if (sent.length === 1) throw Object.assign(new Error("ask"), { status: 400, code: "inplace_history" }); return { rev: "r" }; };
  t.box.cdAskAck = async () => true;
  return t.run("cdSendProfile('/api/profiles/p', 'PUT', { name: 'p' })").then(() => {
    assert.deepStrictEqual(sent, [{ name: "p" }, { name: "p", ack_inplace_history: true }]);
  });
});

test("the page opens the dialog from a needs_copy refusal of a start and of a restart, and from the card path before a start", () => {
  const app = read("app.js");
  assert.match(app, /spawnRefusedNeedsCopy\(e\.status, e\.code\)/);
  assert.match(app, /await restartNeedsCopy\(s, body\)/);
  assert.match(app, /if \(tierConvertNow\(\)\) \{\s*const refusal = await spawnCopyRefusal\(\);\s*if \(!refusal\) return;\s*copied = await spawnNeedsCopy\(refusal\);/);
  // The card path asks the server for this profile (its suggested folder and home verdict), and the retry asks outside the busy state.
  assert.match(app, /"\/api\/workspace\/check\?path=" \+ encodeURIComponent\(getCwd\(\)\) \+ "&profile=" \+ encodeURIComponent\(\$\("agent"\)\.value\)/);
  assert.match(app, /asked = true;\s*spawnIdle\(\);\s*const made = await spawnNeedsCopy\(e\.body\);\s*if \(!made\) break;\s*spawnBusyAgain\(\);/);
  // The cards follow the check: a git answer turns Box · in place off.
  assert.match(app, /function setSpawnWsCheck\(check\) \{\s*spawnWsCheck = check;\s*renderTierPick\(\);/);
  const html = read("index.html");
  assert.ok(html.indexOf('<script src="/upload.js"></script>') < html.indexOf('<script src="/copydialog.js"></script>'), "humanBytes loads first");
  assert.ok(html.indexOf('<script src="/tierpick.js"></script>') < html.indexOf('<script src="/copydialog.js"></script>'));
});

test("every server string reaches the dialog as text: copydialog.js has no innerHTML", () => {
  assert.doesNotMatch(read("copydialog.js"), /innerHTML|insertAdjacentHTML|outerHTML/);
});

test("cdSendProfile answers merge_home_ack: the dialog opens with the profile's repo, Turn on resends with ack_merge_home, Cancel does not", async () => {
  const body = { name: "p", clone_of: "/home/u/repo", clone_merge_home: true };
  const mk = (answer) => {
    const t = world({});
    const sent = [];
    const asked = [];
    t.box.api = async (p, op) => {
      const b = JSON.parse(op.body);
      sent.push(b);
      if (!b.ack_merge_home) throw Object.assign(new Error("clone_merge_home lets this box change your branch"), { status: 400, code: "merge_home_ack", body: { code: "merge_home_ack", field: "clone_merge_home" } });
      return { rev: "r" };
    };
    t.box.cdAskMergeHome = async (repo) => { asked.push(repo); return answer; };
    return { t, sent, asked };
  };
  const yes = mk(true);
  await yes.t.run("cdSendProfile('/api/profiles/p', 'PUT', " + JSON.stringify(body) + ")");
  assert.deepStrictEqual(yes.sent, [body, Object.assign({}, body, { ack_merge_home: true })]);
  assert.deepStrictEqual(yes.asked, ["/home/u/repo"]);
  const no = mk(false);
  await assert.rejects(no.t.run("cdSendProfile('/api/profiles/p', 'PUT', " + JSON.stringify(body) + ")"), /clone_merge_home lets this box/);
  assert.strictEqual(no.sent.length, 1);
});

test("the one warning dialog is built once: the form's checkbox and the 400 both call cdAskMergeHome", () => {
  assert.match(read("pfv-workspace.js"), /cdAskMergeHome\(/);
  assert.match(read("copydialog.js"), /cdMergeHomeDialog\(repo\)/);
  assert.match(read("copydialog.js"), /cdAskMergeHome\(/);
});
