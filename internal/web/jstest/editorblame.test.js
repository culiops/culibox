"use strict";
// The dock's line blame (editorblame.js) on the real EditorTab, with editorpage.js's fake Monaco: when it asks
// POST /api/git/blame, what it ignores, inline vs the status bar, the line map after edits, the toggle, the card.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { page } = require("./editorpage.js");
// blHeaderText and blCols read blamemodel.js's names: run them in a page, where both scripts are loaded.
const inPage = page();
const blHeaderText = inPage.get("blHeaderText");
const blCols = inPage.get("blCols");

const A = "a".repeat(40), B = "b".repeat(40), Z = "0".repeat(40);
const TEXT = "resource x {\n  size = 200\n}\n";
const commits = [
  { sha: A, author: "Minh", email: "m@x", time: 1000, summary: "add rds (#12)", ref: "#12" },
  { sha: B, author: "Lan Tran", email: "l@x", time: 2000, summary: "raise storage" },
];
const answer = (text, over) => Object.assign({
  state: "ok", version: "h:" + text, last: commits[1], commits, lines: [[0, 1], [1, 1], [0, 1]],
  remote: { kind: "github", base: "https://github.com/example/demo" },
}, over);

// A page with the dock on screen, git known, one file served, the clock at `now` (unix ms).
async function setup(opts) {
  const o = Object.assign({ text: TEXT, path: "/p/rds.tf", now: 1_000_000_000 }, opts);
  const p = page();
  p.st.git.on = true;
  p.disk = p.serve(o.text);
  p.setShown(true);
  p.st.git.blame = (body) => answer(p.disk.text);
  p.st.git.answer = { state: "ok", content: o.text }; // the change gutter's base: HEAD has the file as served
  p.clock = o.now;
  p.ctx.blNow = () => p.clock;
  const tab = await p.open(o.path, o.tab);
  const ed = p.st.editors[0];
  return { p, tab, ed, sync: async () => { p.get("editorBlameSync")(tab); await p.settle(); } };
}

const inline = (ed) => ed.decor.list().filter((d) => d.options && d.options.after);
const GAP = "\u00a0\u00a0\u00a0"; // the inline text's leading gap

test("a text file in Code asks once for its version and HEAD, and draws the line's blame inline", async () => {
  const { p, tab, ed, sync } = await setup();
  await sync();
  assert.equal(p.st.git.blames.length, 1);
  assert.deepEqual(p.st.git.blames[0].body, { path: "/p/rds.tf" });
  await sync();
  assert.equal(p.st.git.blames.length, 1, "the same version and HEAD: no second request");
  const d = inline(ed);
  assert.equal(d.length, 1);
  assert.equal(d[0].options.showIfCollapsed, true);
  assert.equal(d[0].options.after.inlineClassName, "dk-blame-inline");
  assert.match(d[0].options.after.content, /^\u00a0{3}Minh, .* ago · add rds \(#12\)$/);
  assert.deepEqual([d[0].range.startLineNumber, d[0].range.startColumn, d[0].range.endColumn], [1, 13, 13], "collapsed at the line's end");
  assert.equal(tab.sbBlame.hidden, true, "never both");
});

test("no room after the line's end: the status bar says it instead, and no decoration is left", async () => {
  const { p, tab, ed, sync } = await setup();
  ed.width = 200;
  await sync();
  assert.equal(inline(ed).length, 0);
  assert.equal(tab.sbBlame.hidden, false);
  assert.match(tab.sbBlame.textContent, /^Minh, /);
  // Room again (the dock widened): back inline, the status item empty.
  ed.width = 900;
  p.get("blPaint")(tab);
  assert.equal(inline(ed).length, 1);
  assert.equal(tab.sbBlame.hidden, true);
  // Scrolled right: the line's end is further than it looks, and the text would widen the line.
  ed.width = 330;
  ed.left = 100;
  p.get("blPaint")(tab);
  assert.equal(inline(ed).length, 0);
  assert.equal(tab.sbBlame.hidden, false);
});

test("the cursor's line is the one blamed; edits since the blame move with their lines and new lines read Not committed yet", async () => {
  const { p, tab, ed, sync } = await setup();
  await sync();
  ed.setPosition({ lineNumber: 2, column: 1 });
  p.get("blPaint")(tab);
  assert.match(inline(ed)[0].options.after.content, /^\u00a0{3}Lan Tran, /);
  tab.model.type("# added\n" + TEXT);
  ed.setPosition({ lineNumber: 3, column: 1 });
  p.get("blPaint")(tab);
  assert.match(inline(ed)[0].options.after.content, /^\u00a0{3}Lan Tran, /, "blamed line 2 is buffer line 3 now");
  ed.setPosition({ lineNumber: 1, column: 1 });
  p.get("blPaint")(tab);
  assert.equal(inline(ed)[0].options.after.content, GAP + "Not committed yet");
  assert.equal(p.st.git.blames.length, 1, "a dirty buffer never asks");
});

test("an agent writing the file: one request per 10 s at most, and an answer for an older version is not used", async () => {
  const { p, tab, sync } = await setup();
  await sync();
  for (const n of [1, 2, 3]) {
    p.clock += 1000;
    p.disk.text = TEXT + "#" + n + "\n";
    tab.replaceFromDisk(p.disk.text, p.hash(p.disk.text), true);
    await sync();
  }
  assert.equal(p.st.git.blames.length, 1, "three new versions within 4 s: still one request");
  const gap = p.st.timers.filter((t) => !t.dead && t.ms > 2000 && t.ms <= 10000); // the gutter and the status have short timers of their own
  assert.equal(gap.length, 1, "one wait for the gap");
  assert.equal(gap[0].ms, 10000 - 1000);
  p.clock += 9000;
  gap[0].fn();
  await p.settle();
  assert.equal(p.st.git.blames.length, 2, "asked again once the gap is over");
  // The server answers for a version the tab no longer has: kept out, asked again later.
  const { p: q, tab: t2, ed: e2, sync: s2 } = await setup();
  q.st.git.blame = () => answer("something else");
  await s2();
  assert.equal(t2.bl.res, null);
  assert.equal(inline(e2).length, 0);
});

test("a request still running for a tab that is hidden is let go, and its late answer draws nothing", async () => {
  const { p, tab, ed } = await setup();
  let release;
  p.st.git.blameGate = new Promise((r) => { release = r; });
  p.get("editorBlameSync")(tab);
  await p.settle();
  assert.equal(p.st.git.blames.length, 1);
  const signal = p.st.git.blames[0].signal;
  tab.onHide();
  assert.equal(signal.aborted, true, "the server's wait or run stops with the client");
  release();
  await p.settle();
  assert.equal(tab.bl.res, null);
  assert.equal(inline(ed).length, 0);
});

test("nothing is asked for a dirty buffer, a box file, Preview, or before git has said anything", async () => {
  {
    const { p, tab, sync } = await setup();
    tab.model.type(TEXT + "x\n");
    await sync();
    assert.equal(p.st.git.blames.length, 0, "dirty");
  }
  {
    const { p, sync } = await setup({ tab: { box: "s1" } });
    await sync();
    assert.equal(p.st.git.blames.length, 0, "box file");
  }
  {
    const { p, tab, sync } = await setup();
    p.st.git.on = false;
    await sync();
    assert.equal(p.st.git.blames.length, 0, "no git status yet");
    p.st.git.on = true;
    await sync();
    assert.equal(p.st.git.blames.length, 1);
    assert.ok(tab.bl.res);
  }
  {
    const { p, tab, sync } = await setup({ path: "/p/README.md", text: "# Title\n\ntext\n" });
    assert.equal(tab.previewing, true, "a markdown file opens in Preview");
    await sync();
    assert.equal(p.st.git.blames.length, 0, "Preview does not ask");
    p.get("editorMdPick")(tab, "source");
    await sync();
    assert.equal(p.st.git.blames.length, 1, "Source does");
  }
});

test("a blame that took too long is not asked again on its own", async () => {
  const { p, tab, sync } = await setup();
  p.st.git.blame = () => ({ state: "slow", reason: "Blame took too long for this file" });
  await sync();
  p.clock += 60000;
  p.disk.text = TEXT + "#\n";
  tab.replaceFromDisk(p.disk.text, p.hash(p.disk.text), true);
  await sync();
  assert.equal(p.st.git.blames.length, 1);
  assert.deepEqual(blHeaderText(tab.bl.res, 0), { who: "Blame unavailable", when: "", short: "", title: "Blame took too long for this file", muted: true });
});

test("Show line blame off: no inline text and no status item; on again: back; the choice is stored", async () => {
  const { p, tab, ed, sync } = await setup();
  await sync();
  p.get("editorBlameToggle")();
  assert.equal(p.ctx.viewerPrefs.getBlame(), false);
  assert.equal(inline(ed).length, 0);
  assert.equal(tab.sbBlame.hidden, true);
  p.get("editorBlameToggle")();
  assert.equal(inline(ed).length, 1);
});

test("the ⋯ menu has Show line blame (checked) and Blame of this line in Code", async () => {
  const { p, tab, sync } = await setup();
  await sync();
  const rows = p.get("editorBlameMenuRows")(tab, (label, icon, run, can) => ({ label, can, run }), (label, on, run) => ({ label, on, run }));
  assert.deepEqual(rows.map((r) => r.label), ["Show line blame", "Blame of this line"]);
  assert.equal(rows[0].on, true);
  assert.notEqual(rows[1].can, false);
  tab.showChanges(true);
  await p.settle();
  assert.ok(tab.diff, "the Changes view is up");
  assert.deepEqual(p.get("editorBlameMenuRows")(tab, () => ({}), () => ({})), [], "not in Changes");
});

test("header text: the last commit, a new file, unavailable; nothing for no answer or not a repo", () => {
  const now = 2000 * 1000 + 3 * 86400 * 1000;
  assert.deepEqual(blHeaderText(answer(TEXT), now), { who: "Lan Tran", when: "3 days ago", short: "3d", title: "Last commit: Lan Tran, 3 days ago · raise storage" });
  assert.equal(blHeaderText({ state: "untracked" }, now).who, "Not committed yet");
  assert.equal(blHeaderText({ state: "unavailable", reason: "Git took too long" }, now).title, "Git took too long");
  assert.equal(blHeaderText(null, now), null);
  assert.equal(blHeaderText({ state: "notrepo" }, now), null);
  assert.equal(blHeaderText({ state: "toobig" }, now), null);
});

test("blCols counts a wide character as two cells", () => {
  assert.equal(blCols("Minh"), 4);
  assert.equal(blCols("Nguyễn"), 6);
  assert.equal(blCols("山田"), 4);
});

// --- the card -----------------------------------------------------------------------------------------------------

function cardOf(p) { return p.get("blCard"); }
const texts = (el) => el.querySelectorAll("button").map((b) => b.textContent);

test("the card: SHA, author, email, times, summary as text; Copy SHA, Open commit and the PR through the link gate", async () => {
  const opened = [], copied = [];
  const { p, tab, sync } = await setup();
  p.ctx.openAgentLink = (url, ctx) => { opened.push([url, ctx]); return Promise.resolve(true); };
  p.ctx.copyText = (t, done) => { copied.push(t); done(true); };
  await sync();
  const anchor = tab.sbBlame;
  assert.equal(p.get("editorBlameCardFor")(tab, "line", anchor, true), true);
  const c = cardOf(p);
  assert.equal(c.hidden, false);
  assert.equal(c.getAttribute("role"), "dialog");
  assert.equal(c.querySelector(".dk-bcard-sha").textContent, "aaaaaaa");
  assert.equal(c.querySelector(".dk-bcard-who").textContent, "Minh");
  assert.equal(c.querySelector(".dk-bcard-mail").textContent, "<m@x>");
  assert.equal(c.querySelector(".dk-bcard-sum").textContent, "add rds (#12)");
  assert.deepEqual(texts(c), ["Copy SHA", "Open commit ↗", "PR #12 ↗"]);
  assert.equal(p.doc.activeElement, c.querySelector("button"), "opened by a click: the card has the keyboard");
  c.querySelectorAll("button")[0].listeners.click();
  assert.deepEqual(copied, [A], "the full SHA");
  c.querySelectorAll("button")[1].listeners.click();
  assert.deepEqual(opened[0], ["https://github.com/example/demo/commit/" + A, { source: "editor" }]);
  assert.equal(c.hidden, true, "an open closes the card");
  p.get("editorBlameCardFor")(tab, "line", anchor, true);
  cardOf(p).querySelectorAll("button")[2].listeners.click();
  assert.deepEqual(opened[1], ["https://github.com/example/demo/pull/12", { source: "editor" }]);
});

test("the card: Escape closes it and gives the keyboard back to what opened it", async () => {
  const { p, tab, sync } = await setup();
  await sync();
  const anchor = tab.sbBlame;
  p.get("editorBlameCardFor")(tab, "file", anchor, true);
  const c = cardOf(p);
  assert.equal(c.querySelector(".dk-bcard-who").textContent, "Lan Tran", "the file's last commit");
  let prevented = false;
  c.listeners.keydown({ key: "Escape", preventDefault() { prevented = true; }, stopPropagation() {} });
  assert.equal(prevented, true);
  assert.equal(c.hidden, true);
  assert.equal(p.doc.activeElement, anchor);
});

test("the card: hostile text stays text, and no link without a known host or a valid id", async () => {
  const { p, tab, sync } = await setup();
  const evil = { sha: A, author: "<img src=x onerror=alert(1)>", email: "\"><script>", time: 1, summary: "<b>bold</b> (#7)", ref: "#7" };
  p.st.git.blame = () => answer(p.disk.text, { commits: [evil, evil], remote: { kind: "github", base: "javascript:alert(1)//github.com/o/r" } });
  await sync();
  p.get("editorBlameCardFor")(tab, "line", tab.sbBlame, false);
  const c = cardOf(p);
  assert.equal(c.querySelector(".dk-bcard-who").textContent, "<img src=x onerror=alert(1)>");
  assert.equal(c.querySelector(".dk-bcard-who").children.length, 0, "no element was made from it");
  assert.deepEqual(texts(c), ["Copy SHA"], "no link from an unchecked base");
  const src = fs.readFileSync(path.join(__dirname, "..", "static", "editorblame.js"), "utf8");
  assert.doesNotMatch(src, /innerHTML|outerHTML|insertAdjacentHTML|document\.write/, "blame text never goes through an HTML sink");
});

test("the card for an uncommitted line: Not committed yet, the agent's mark, Open changes and Add note", async () => {
  const { p, tab, ed, sync } = await setup();
  p.ctx.drawerChangedFiles = new Map([["s1", [{ path: "/p/rds.tf", ts: 1 }]]]);
  p.ctx.changedSegs = require("../static/drawerfiles.js").changedSegs; // what editedLabel reads (drawerfiles.js, not in the page)
  const noted = [];
  p.ctx.editorNotesFromCode = (t, r) => { noted.push(r); };
  await sync();
  tab.model.type("# new\n" + TEXT);
  ed.setPosition({ lineNumber: 1, column: 1 });
  p.get("editorBlameCardFor")(tab, "line", tab.sbBlame, false);
  const c = cardOf(p);
  assert.equal(c.querySelector(".dk-bcard-who").textContent, "Not committed yet");
  assert.equal(c.querySelector(".dk-bcard-chip").textContent, "changed by this session's agent");
  assert.deepEqual(texts(c), ["Open changes", "Add note"]);
  c.querySelectorAll("button")[1].listeners.click();
  assert.deepEqual(noted, [{ a: 1, b: 1 }]);
});

test("hovering the inline text opens the card after a pause; leaving the editor closes it", async () => {
  const { p, tab, ed, sync } = await setup();
  await sync();
  const span = new (require("./editorpage.js").El)("span", p.doc);
  span.className = "mtk1 dk-blame-inline";
  ed.fire("mousemove", { target: { element: span } });
  const hover = p.st.timers.filter((t) => !t.dead && t.ms === 400);
  assert.equal(hover.length, 1, "a pause before it opens");
  hover[0].fn();
  assert.equal(cardOf(p).hidden, false);
  assert.notEqual(p.doc.activeElement, cardOf(p).querySelector("button"), "a hover leaves the keyboard where it is");
  ed.fire("mouseleave", {});
  const leave = p.st.timers.filter((t) => !t.dead && t.ms === 300);
  assert.equal(leave.length, 1);
  leave[0].fn();
  assert.equal(cardOf(p).hidden, true);
  assert.ok(tab);
});
