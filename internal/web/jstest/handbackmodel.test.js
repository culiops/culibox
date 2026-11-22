"use strict";
// handbackmodel.js: the words and shapes of the "Flagged for your review" panel (#346). Pure.
const test = require("node:test");
const assert = require("node:assert");
const hb = require("../static/handbackmodel.js");

const flag = (path, classes, more) => Object.assign({ path, classes, detail: "d", sha256: "", hash_note: "", target: "" }, more);
const rec = (flags, more) => Object.assign({ id: "a".repeat(32), kind: "clone", workspace: "", clone_of: "/r", branch: "feat", flags, inspected: 10, truncated: "" }, more);

test("handbackCountText: empty for 0, singular for 1, plural otherwise", () => {
  assert.strictEqual(hb.handbackCountText(0), "");
  assert.strictEqual(hb.handbackCountText(1), "1 file flagged for your review");
  assert.strictEqual(hb.handbackCountText(3), "3 files flagged for your review");
  assert.strictEqual(hb.handbackCountText(undefined), "");
  assert.strictEqual(hb.handbackCountText(-2), "");
});

test("handbackRows: 25 executables in one directory collapse to one group", () => {
  const flags = [];
  for (let i = 0; i < 25; i++) flags.push(flag("node_modules/x/bin" + i, ["executable"]));
  flags.push(flag("node_modules/x/notes.pdf", ["extension-mismatch", "executable"]));
  const rows = hb.handbackRows(rec(flags));
  assert.strictEqual(rows.length, 2);
  assert.strictEqual(rows[0].kind, "file", "the mismatch is a file row and comes first");
  assert.strictEqual(rows[0].path, "node_modules/x/notes.pdf");
  assert.strictEqual(rows[1].kind, "group");
  assert.strictEqual(rows[1].dir, "node_modules/x");
  assert.deepStrictEqual(rows[1].counts, { executable: 25, "exec-script": 0, archive: 0 });
  assert.strictEqual(rows[1].files.length, 25);
});

test("handbackRows: 20 in one directory do not collapse; 21 do", () => {
  const mk = (n) => Array.from({ length: n }, (_, i) => flag("d/f" + i, ["archive"]));
  const twenty = hb.handbackRows(rec(mk(20)));
  assert.strictEqual(twenty.length, 20);
  assert.ok(twenty.every((r) => r.kind === "file"));
  const more = hb.handbackRows(rec(mk(21)));
  assert.strictEqual(more.length, 1);
  assert.deepStrictEqual(more[0].counts, { executable: 0, "exec-script": 0, archive: 21 });
});

test("handbackRows: set-id and symlink flags are never grouped and come before the rest", () => {
  const flags = [];
  for (let i = 0; i < 30; i++) flags.push(flag("bin/t" + i, ["exec-script"]));
  flags.push(flag("bin/su", ["setuid-setgid", "executable"]));
  flags.push(flag("bin/link", ["symlink-outside"], { target: "/etc/passwd" }));
  const rows = hb.handbackRows(rec(flags));
  assert.deepStrictEqual(rows.map((r) => r.kind), ["file", "file", "group"]);
  assert.deepStrictEqual(rows.slice(0, 2).map((r) => r.path), ["bin/su", "bin/link"]);
  assert.strictEqual(rows[1].target, "/etc/passwd");
  assert.strictEqual(rows[2].counts["exec-script"], 30);
});

test("handbackRows: a file row carries what the panel shows", () => {
  const rows = hb.handbackRows(rec([flag("a/b.pdf", ["extension-mismatch"], { detail: "ELF in a .pdf", sha256: "ab12", hash_note: "not hashed: larger than 64 MiB" })]));
  assert.deepStrictEqual(rows, [{ kind: "file", path: "a/b.pdf", classes: ["extension-mismatch"], detail: "ELF in a .pdf", sha256: "ab12", hashNote: "not hashed: larger than 64 MiB", target: "" }]);
});

test("handbackRows: tolerates a missing record or flags", () => {
  assert.deepStrictEqual(hb.handbackRows(null), []);
  assert.deepStrictEqual(hb.handbackRows({ id: "x" }), []);
});

test("handbackFooter: clone, in-place and cut short", () => {
  assert.deepStrictEqual(hb.handbackFooter(rec([])), ["This is not a malware scan."]);
  assert.deepStrictEqual(hb.handbackFooter(rec([], { kind: "in-place" })),
    ["This is not a malware scan.", "These files have been on the host since the agent wrote them."]);
  assert.deepStrictEqual(hb.handbackFooter(rec([], { truncated: "stopped after 500000 entries" })),
    ["This is not a malware scan.", "stopped after 500000 entries"]);
  assert.deepStrictEqual(hb.handbackFooter(rec([], { kind: "in-place", truncated: "t" })),
    ["This is not a malware scan.", "These files have been on the host since the agent wrote them.", "t"]);
});

test("handbackEmptyText and the window sentence", () => {
  assert.strictEqual(hb.handbackEmptyText(), "Nothing flagged. This is not a malware scan.");
  assert.match(hb.handbackWindowText(rec([], { kind: "in-place" })), /created or changed since this folder was last checked/);
  assert.doesNotMatch(hb.handbackWindowText(rec([])), /last checked/);
});

test("handbackNoteText: a count, a cut-short scan that never reads as nothing flagged, or both", () => {
  assert.strictEqual(hb.handbackNoteText([{ id: "a", count: 2 }]), "2 files flagged for your review");
  assert.strictEqual(hb.handbackNoteText([{ id: "a", count: 1 }, { id: "b", count: 2 }]), "3 files flagged for your review");
  assert.strictEqual(hb.handbackNoteText([{ id: "a", count: 0, truncated: true }]), "Not everything was checked — open the review");
  assert.strictEqual(hb.handbackNoteText([{ id: "a", count: 2, truncated: true }]), "2 files flagged for your review · not everything was checked");
  assert.strictEqual(hb.handbackNoteText([{ id: "a", count: 0 }]), "");
  assert.strictEqual(hb.handbackNoteText(undefined), "");
  assert.deepStrictEqual(hb.handbackNoteIds([{ id: "a", count: 1 }, { id: "b", count: 0 }, { id: "c", count: 0, truncated: true }, null]), ["a", "c"]);
});

test("handbackPending, handbackSidebarText: only records with flags or a cut-short note count", () => {
  const rs = [rec([flag("a", ["archive"]), flag("b", ["archive"])]), rec([], { id: "b".repeat(32), truncated: "cut" }), rec([], { id: "c".repeat(32) })];
  assert.strictEqual(hb.handbackPending(rs).length, 2);
  assert.strictEqual(hb.handbackSidebarText(rs), "2 flagged for your review");
  assert.strictEqual(hb.handbackSidebarText([rs[0]].slice(0, 1).map((r) => Object.assign({}, r, { flags: [flag("a", ["archive"])] }))), "1 flagged for your review");
  assert.strictEqual(hb.handbackSidebarText([rs[1]]), "1 flagged for your review", "a cut-short record alone still counts as one");
  assert.strictEqual(hb.handbackSidebarText([rs[2]]), "");
  assert.strictEqual(hb.handbackSidebarText(null), "");
});

test("handbackSelect: by id, ids, profile match, or everything pending", () => {
  const a = rec([flag("a", ["archive"])], { id: "1".repeat(32), clone_of: "/r1" });
  const b = rec([flag("b", ["archive"])], { id: "2".repeat(32), kind: "in-place", clone_of: "", workspace: "/w", branch: "" });
  const c = rec([flag("c", ["archive"])], { id: "3".repeat(32), clone_of: "/r2" });
  const all = [a, b, c];
  assert.deepStrictEqual(hb.handbackSelect(all, undefined), all);
  assert.deepStrictEqual(hb.handbackSelect(all, "2".repeat(32)), [b]);
  assert.deepStrictEqual(hb.handbackSelect(all, ["1".repeat(32), "3".repeat(32)]), [a, c]);
  assert.deepStrictEqual(hb.handbackSelect(all, { cloneOf: "/r2" }), [c]);
  assert.deepStrictEqual(hb.handbackSelect(all, { workspace: "/w" }), [b]);
  assert.deepStrictEqual(hb.handbackSelect(all, { cloneOf: "/none" }), all, "a profile that matches nothing shows all pending, not an empty panel");
});

test("handbackProfileSel: a copy by its original, a folder profile by its folder", () => {
  assert.deepStrictEqual(hb.handbackProfileSel({ clone_of: "/r", default_cwd: "/c" }), { cloneOf: "/r" });
  assert.deepStrictEqual(hb.handbackProfileSel({ default_cwd: "/w" }), { workspace: "/w" });
  assert.strictEqual(hb.handbackProfileSel({}), undefined);
  assert.strictEqual(hb.handbackProfileSel(null), undefined);
});

test("handbackSessionNotes and handbackSessionText: a session's own notes, hidden once reviewed", () => {
  const s = { id: "s", agent: "p", handback: { id: "h1", count: 2, in_place: true }, return: { flagged: [{ id: "h2", count: 1, branch: "x" }] } };
  assert.deepStrictEqual(hb.handbackSessionNotes(s).map((n) => n.id), ["h1", "h2"]);
  assert.strictEqual(hb.handbackSessionText(s), "3 files flagged for your review");
  assert.strictEqual(hb.handbackSessionText(s, new Set(["h2"])), "1 file flagged for your review");
  assert.strictEqual(hb.handbackSessionText(s, new Set()), "");
  assert.strictEqual(hb.handbackSessionText({ id: "s" }), "");
  assert.strictEqual(hb.handbackSessionText(null), "");
});

test("handbackNews: the first poll seeds; a new in-place note toasts once", () => {
  const ended = (note) => ({ id: "s1", agent: "p", handback: note });
  const first = hb.handbackNews(null, [ended({ id: "h1", count: 2, in_place: true })]);
  assert.deepStrictEqual(first.toasts, []);
  assert.strictEqual(first.seen.length, 1);
  const quiet = hb.handbackNews(first.seen, [ended({ id: "h1", count: 2, in_place: true })]);
  assert.deepStrictEqual(quiet.toasts, []);
  const fresh = hb.handbackNews(first.seen, [ended({ id: "h1", count: 2, in_place: true }), { id: "s2", agent: "q", handback: { id: "h2", count: 1, in_place: true } }]);
  assert.deepStrictEqual(fresh.toasts, [{ text: "q: 1 file flagged for your review", ids: ["h2"] }]);
  const cut = hb.handbackNews([], [{ id: "s3", agent: "q", handback: { id: "h3", count: 0, in_place: true, truncated: true } }]);
  assert.deepStrictEqual(cut.toasts, [{ text: "q: Not everything was checked — open the review", ids: ["h3"] }]);
  const none = hb.handbackNews([], [{ id: "s4", agent: "q", handback: { id: "h4", count: 0, in_place: true } }]);
  assert.deepStrictEqual(none.toasts, [], "a scan that flagged nothing and finished says nothing");
  assert.deepStrictEqual(hb.handbackNews(["x"], null).toasts, []);
});

test("handbackDue: a refresh is due when never done or the interval has passed", () => {
  assert.strictEqual(hb.handbackDue(0, 1000, 30000), true);
  assert.strictEqual(hb.handbackDue(1000, 20000, 30000), false);
  assert.strictEqual(hb.handbackDue(1000, 31000, 30000), true);
});

test("no wording from the model says clean, safe or no threats", () => {
  const out = [];
  out.push(hb.handbackCountText(0), hb.handbackCountText(1), hb.handbackCountText(9), hb.handbackEmptyText());
  out.push(...hb.handbackFooter(rec([], { kind: "in-place", truncated: "cut" })));
  out.push(hb.handbackWindowText(rec([], { kind: "in-place" })), hb.handbackWindowText(rec([])));
  out.push(hb.handbackNoteText([{ id: "a", count: 0, truncated: true }]), hb.handbackNoteText([{ id: "a", count: 3, truncated: true }]));
  out.push(hb.handbackSidebarText([rec([flag("a", ["archive"])])]), hb.handbackSidebarText([rec([], { truncated: "x" })]));
  out.push(hb.HANDBACK_TITLE, hb.handbackClassText("extension-mismatch"), hb.handbackClassText("executable"), hb.handbackClassText("exec-script"),
    hb.handbackClassText("archive"), hb.handbackClassText("setuid-setgid"), hb.handbackClassText("symlink-outside"));
  for (const s of out) assert.doesNotMatch(s, /\b(clean|safe|no threats)\b/i, s);
  assert.strictEqual(hb.HANDBACK_TITLE, "Flagged for your review");
});
