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
