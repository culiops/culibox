// internal/web/jstest/shellnest.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const n = require("../static/shellnest.js");

const parent = { id: "p", name: "agent", created: "2026-10-27T09:00:00Z" };
const sh1 = { id: "s1", name: "sh-a", parent_id: "p", created: "2026-10-27T09:10:00Z" };
const sh2 = { id: "s2", name: "sh-b", parent_id: "p", created: "2026-10-27T09:20:00Z" };

test("shellChildren groups nested shells by parent, oldest first, whatever the list order", () => {
  const m = n.shellChildren([sh2, parent, sh1]);
  assert.deepEqual(m.get("p").map((s) => s.id), ["s1", "s2"]);
  assert.equal(m.size, 1);
});

test("shellChildren ignores an orphan whose parent is absent", () => {
  const orphan = { id: "o", name: "o", parent_id: "gone", created: "2026-10-27T09:30:00Z" };
  assert.equal(n.shellChildren([parent, orphan]).size, 0);
  assert.equal(n.shellChildren(null).size, 0);
});

test("shellChildren breaks a created tie, or a missing created, by list order", () => {
  const a = { id: "a", parent_id: "p" };
  const b = { id: "b", parent_id: "p" };
  assert.deepEqual(n.shellChildren([parent, b, a]).get("p").map((s) => s.id), ["b", "a"]);
  const c = { id: "c", parent_id: "p", created: "2026-10-27T09:05:00Z" };
  assert.deepEqual(n.shellChildren([parent, a, c]).get("p").map((s) => s.id), ["c", "a"], "dated before undated");
});

test("isNestedShell needs a parent_id AND a parent in the list", () => {
  assert.equal(n.isNestedShell(sh1, [parent, sh1]), true);
  assert.equal(n.isNestedShell(sh1, [sh1]), false, "parent gone: an ordinary row");
  assert.equal(n.isNestedShell(parent, [parent, sh1]), false);
  assert.equal(n.isNestedShell({ id: "x" }, [parent]), false);
  assert.equal(n.isNestedShell(null, [parent]), false);
  assert.equal(n.isNestedShell(sh1, null), false);
});

test("a parent created after its shell is a later session that reused the id, not the parent", () => {
  // After a daemon restart ids restart from live sessions: a new "p" may take
  // the freed id of the ended parent a surviving shell still points at.
  const stranger = { id: "p", name: "new agent", created: "2026-10-27T10:00:00Z" };
  const old = { id: "o", name: "sh:old", parent_id: "p", created: "2026-10-27T09:30:00Z", status: "working" };
  assert.equal(n.isNestedShell(old, [stranger, old]), false);
  assert.equal(n.shellChildren([stranger, old]).size, 0);
  assert.equal(n.shellLabel(old, [stranger, old]), "sh:old");
  assert.deepEqual(n.liveShellsOf("p", [stranger, old]), [], "End all must not reach it");
});

test("a parent created at or before its shell still nests it", () => {
  const same = { id: "s3", parent_id: "p", created: parent.created };
  assert.equal(n.isNestedShell(sh1, [parent, sh1]), true);
  assert.equal(n.isNestedShell(same, [parent, same]), true, "same instant");
  assert.equal(n.isNestedShell({ id: "u", parent_id: "p" }, [parent]), true, "no created on the shell");
});

test("a shell's parent being itself a shell, or itself, does not nest (no session may vanish)", () => {
  const self = { id: "z", parent_id: "z" };
  assert.equal(n.isNestedShell(self, [self]), false);
  const a = { id: "a", parent_id: "b" };
  const b = { id: "b", parent_id: "a" };
  assert.equal(n.isNestedShell(a, [a, b]), false);
  assert.equal(n.isNestedShell(b, [a, b]), false);
  const grand = { id: "g", parent_id: "s1" };
  assert.equal(n.isNestedShell(grand, [parent, sh1, grand]), false, "a shell of a shell stays top-level");
  assert.equal(n.isNestedShell(sh1, [parent, sh1, grand]), true);
});

test("shellLabel numbers by created order, stable when the list shuffles", () => {
  for (const list of [[parent, sh1, sh2], [sh2, sh1, parent], [sh2, parent, sh1]]) {
    assert.equal(n.shellLabel(sh1, list), "shell 1");
    assert.equal(n.shellLabel(sh2, list), "shell 2");
  }
});

test("shellLabel falls back to the name when the shell is not nested", () => {
  assert.equal(n.shellLabel(sh1, [sh1]), "sh-a");
  assert.equal(n.shellLabel(parent, [parent, sh1]), "agent");
});

test("shellLabel finds a session by id, so a copy of the row gets the same label", () => {
  const list = [parent, sh1, sh2];
  assert.equal(n.shellLabel(Object.assign({}, sh2), list), "shell 2");
  assert.equal(n.shellLabel(Object.assign({}, sh1), list), "shell 1");
});

// --- ending a parent -----------------------------------------------------------
const live = { ...sh1, status: "live" };
const gone = { ...sh2, status: "exited" };

test("liveShellsOf counts a shell unless it has exited", () => {
  const det = { ...sh1, id: "s3", status: "detached" };
  assert.deepEqual(n.liveShellsOf("p", [parent, live, gone, det]).map((s) => s.id), ["s1", "s3"]);
  assert.deepEqual(n.liveShellsOf("p", [parent, gone]), []);
  assert.deepEqual(n.liveShellsOf("p", [parent]), []);
  assert.deepEqual(n.liveShellsOf("nope", [parent, live]), []);
  assert.deepEqual(n.liveShellsOf("p", undefined), []);
});

test("killDialogOpts with no live shells is the plain End dialog", () => {
  assert.deepEqual(n.killDialogOpts("agent", "p", 0), {
    title: "End session",
    body: 'End session "agent"? This cannot be undone.',
    confirmLabel: "End",
    danger: true,
  });
  assert.strictEqual(n.killDialogOpts("", "p", 0).body, 'End session "p"? This cannot be undone.');
});

test("killDialogOpts with shells offers End all, End session only", () => {
  const one = n.killDialogOpts("agent", "p", 1);
  assert.strictEqual(one.body, 'End "agent" and its 1 shell? This cannot be undone.');
  assert.strictEqual(one.confirmLabel, "End all");
  assert.strictEqual(one.altLabel, "End session only");
  assert.strictEqual(one.danger, true);
  assert.strictEqual(one.title, "End session");
  assert.strictEqual(n.killDialogOpts("agent", "p", 3).body, 'End "agent" and its 3 shells? This cannot be undone.');
});

test("killIdsFor: confirm ends the parent then its shells, alt only the parent, cancel nothing", () => {
  const kids = [live, { ...sh2, status: "live" }];
  assert.deepEqual(n.killIdsFor("confirm", "p", kids), ["p", "s1", "s2"]);
  assert.deepEqual(n.killIdsFor("alt", "p", kids), ["p"]);
  assert.deepEqual(n.killIdsFor("cancel", "p", kids), []);
  assert.deepEqual(n.killIdsFor("confirm", "p", []), ["p"]);
});

// --- killDialogDeskOpts: the desktop End session dialog (frame M5.2) -------------

test("killDialogDeskOpts: one shell, names what it ends, what it keeps, and the files it leaves", () => {
  assert.deepStrictEqual(n.killDialogDeskOpts("agent-box", "x", ["shell 1"], "~/box-clones/culiops-agent-box"), {
    title: "End session agent-box?",
    body: "The agent stops now and agent-box leaves the sidebar. An ended session can't be restarted.",
    notes: [
      ["Files in its workspace, ", { mono: "~/box-clones/culiops-agent-box" }, ", are not touched."],
      ["It has 1 shell open, shell 1. “End agent-box only” keeps that shell running as its own row."],
    ],
    confirmLabel: "End both",
    altLabel: "End agent-box only",
    danger: true,
    cls: "end",
  });
});

test("killDialogDeskOpts: no shells is one red End session, with no shell note and no second way out", () => {
  const o = n.killDialogDeskOpts("agent-box", "x", [], "~/w");
  assert.strictEqual(o.confirmLabel, "End session");
  assert.ok(!("altLabel" in o), "no altLabel");
  assert.deepStrictEqual(o.notes, [["Files in its workspace, ", { mono: "~/w" }, ", are not touched."]]);
  assert.strictEqual(o.danger, true);
  assert.strictEqual(o.cls, "end");
});

test("killDialogDeskOpts: shells are listed by their labels and the note reads right for 2 and 3", () => {
  const two = n.killDialogDeskOpts("agent-box", "x", ["shell 1", "shell 2"], "~/w");
  assert.deepStrictEqual(two.notes[1], ["It has 2 shells open, shell 1 and shell 2. “End agent-box only” keeps those shells running as their own rows."]);
  const three = n.killDialogDeskOpts("agent-box", "x", ["shell 1", "shell 2", "shell 3"], "~/w");
  assert.deepStrictEqual(three.notes[1], ["It has 3 shells open, shell 1, shell 2 and shell 3. “End agent-box only” keeps those shells running as their own rows."]);
  // With two or more shells "End both" would be untrue (decision 2): today's "End all" stays.
  for (const o of [two, three]) {
    assert.strictEqual(o.confirmLabel, "End all");
    assert.strictEqual(o.altLabel, "End agent-box only");
  }
});

test("killDialogDeskOpts: no cwd drops the Files note (no empty paragraph), no name falls back to the id everywhere", () => {
  for (const cwd of ["", undefined, null]) {
    const o = n.killDialogDeskOpts("agent-box", "x", ["shell 1"], cwd);
    assert.strictEqual(o.notes.length, 1, String(cwd));
    assert.match(o.notes[0][0], /^It has 1 shell open/);
  }
  const bare = n.killDialogDeskOpts("agent-box", "x", [], "");
  assert.deepStrictEqual(bare.notes, []);
  const o = n.killDialogDeskOpts("", "s-42", ["shell 1"], "~/w");
  assert.strictEqual(o.title, "End session s-42?");
  assert.match(o.body, /^The agent stops now and s-42 leaves the sidebar\./);
  assert.strictEqual(o.altLabel, "End s-42 only");
  assert.match(o.notes[1][0], /“End s-42 only” keeps that shell/);
});

test("killDialogDeskOpts: a long path stays one mono part (the CSS wraps it), and the phone's killDialogOpts is untouched", () => {
  const long = "~/" + "a".repeat(118);
  const o = n.killDialogDeskOpts("agent-box", "x", [], long);
  assert.deepStrictEqual(o.notes[0][1], { mono: long });
  assert.deepStrictEqual(n.killDialogOpts("agent", "p", 0), {
    title: "End session",
    body: 'End session "agent"? This cannot be undone.',
    confirmLabel: "End",
    danger: true,
  });
});

// --- confirmKill (app.js): the desktop gets the M5.2 copy, the phone keeps today's -------------
// app.js is the page itself and exports nothing, so the real function is cut out of it and run
// against the real shellnest.js / fsops.js helpers with the page's globals faked.
function confirmKillEnv(width, sessions, answer) {
  const fs = require("node:fs");
  const path = require("node:path");
  const vm = require("node:vm");
  const src = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");
  const at = src.indexOf("async function confirmKill(");
  assert.ok(at >= 0, "confirmKill is in app.js");
  const code = src.slice(at, src.indexOf("\n}\n", at) + 2);
  const asked = [];
  const killed = [];
  const box = {
    window: { innerWidth: width, HOME_DIR: "/home/u" },
    lastSessions: sessions,
    mobileActive: (w) => Number(w) <= 700,
    askChoice: async (o) => { asked.push(o); return answer; },
    killSession: async (id) => { killed.push(id); },
    Promise, Array,
  };
  const ctx = vm.createContext(box);
  for (const f of ["fsops.js", "shellnest.js"]) new vm.Script(fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8"), { filename: f }).runInContext(ctx);
  new vm.Script(code, { filename: "app.js:confirmKill" }).runInContext(ctx);
  return { run: (id, name) => ctx.confirmKill(id, name), asked, killed };
}
// Objects built inside the vm carry its realm's prototypes; the JSON round trip makes them comparable.
const plain = (x) => JSON.parse(JSON.stringify(x));
const KILL_SESSIONS = [
  { id: "p", name: "agent-box", cwd: "/home/u/box-clones/culiops-agent-box", created: "2026-10-27T09:00:00Z", status: "live" },
  { id: "s1", name: "sh-a", parent_id: "p", created: "2026-10-27T09:10:00Z", status: "live" },
  { id: "s2", name: "sh-b", parent_id: "p", created: "2026-10-27T09:20:00Z", status: "exited" },
];

test("confirmKill on the desktop asks the M5.2 question: labelled shells, ~ for home, End both ends both", async () => {
  const t = confirmKillEnv(1280, KILL_SESSIONS, "confirm");
  await t.run("p", "agent-box");
  assert.deepStrictEqual(plain(t.asked), [n.killDialogDeskOpts("agent-box", "p", ["shell 1"], "~/box-clones/culiops-agent-box")]);
  assert.deepStrictEqual(t.killed, ["p", "s1"], "confirm = the parent and its live shells");
});

test("confirmKill on the desktop: End agent-box only ends the parent alone, Cancel ends nothing", async () => {
  const alt = confirmKillEnv(1280, KILL_SESSIONS, "alt");
  await alt.run("p", "agent-box");
  assert.deepStrictEqual(alt.killed, ["p"]);
  const no = confirmKillEnv(1280, KILL_SESSIONS, "cancel");
  await no.run("p", "agent-box");
  assert.deepStrictEqual(no.killed, []);
});

test("confirmKill on the desktop with no recorded cwd leaves the Files note out", async () => {
  const bare = [{ id: "p", name: "agent-box", created: "2026-10-27T09:00:00Z", status: "live" }];
  const t = confirmKillEnv(1280, bare, "confirm");
  await t.run("p", "agent-box");
  assert.deepStrictEqual(plain(t.asked[0].notes), []);
  assert.strictEqual(t.asked[0].confirmLabel, "End session");
  assert.deepStrictEqual(t.killed, ["p"]);
  // A tab for a session the list no longer holds: still the dialog, still no invented path.
  const gone = confirmKillEnv(1280, [], "confirm");
  await gone.run("zz", "");
  assert.strictEqual(gone.asked[0].title, "End session zz?");
  assert.deepStrictEqual(plain(gone.asked[0].notes), []);
});

// The ⏻ on a shell's tab, the palette and the row menu all reach confirmKill for a nested companion shell. "The
// agent stops now" is false for a shell, so on desktop it keeps today's plain End question; the dialog chrome
// (Close (Esc), the hint, the kit buttons) comes from askDialog and the stylesheet either way.
test("confirmKill on the desktop for a nested shell asks today's plain End question, not the agent copy", async () => {
  const t = confirmKillEnv(1280, KILL_SESSIONS, "confirm");
  await t.run("s1", "shell 1");
  assert.deepStrictEqual(plain(t.asked), [n.killDialogOpts("shell 1", "s1", 0)]);
  assert.ok(!("notes" in t.asked[0]) && !("cls" in t.asked[0]), "no M5.2 notes or card class");
  assert.doesNotMatch(JSON.stringify(plain(t.asked)), /agent stops/);
  assert.deepStrictEqual(t.killed, ["s1"], "confirm ends the shell alone");
  // A nameless tab falls back to the id, as the phone's question does.
  const bare = confirmKillEnv(1280, KILL_SESSIONS, "cancel");
  await bare.run("s1", "");
  assert.deepStrictEqual(plain(bare.asked), [n.killDialogOpts("", "s1", 0)]);
  assert.deepStrictEqual(bare.killed, []);
  // The agent session beside it still gets the M5.2 copy on the same desktop.
  const agent = confirmKillEnv(1280, KILL_SESSIONS, "alt");
  await agent.run("p", "agent-box");
  assert.deepStrictEqual(plain(agent.asked), [n.killDialogDeskOpts("agent-box", "p", ["shell 1"], "~/box-clones/culiops-agent-box")]);
});

test("confirmKill on the phone keeps today's dialog byte for byte", async () => {
  const t = confirmKillEnv(390, KILL_SESSIONS, "alt");
  await t.run("p", "agent-box");
  assert.deepStrictEqual(plain(t.asked), [n.killDialogOpts("agent-box", "p", 1)]);
  assert.deepStrictEqual(t.killed, ["p"]);
});
