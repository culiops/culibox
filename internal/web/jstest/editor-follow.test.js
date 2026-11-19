"use strict";
// Follow agent (editorfollow.js, with editorref.js followPlan): the real modules and the real EditorTab, editorsync.js and
// editordiff.js in a vm, against the fake page and the fake Monaco of editorpage.js. Stood in: the dock's watch (dockWatchSubscribe
// hands the test the subscriber, which the test calls with what Activity would bring: `files` and `events`) and the dock's tab
// handling (drawerShowFile and dockAttached: a new tab is attached at once and reads its file; an old one is shown again by
// being attached, as dockShow does). The real ones: dwUnderRoot (cut out of dockwatch.js), changedSegs, the registry, the poll of
// the file on disk and the base text from a stubbed POST /api/git/show. What this proves: which events open which file, that the
// file is opened as the preview tab and nothing takes the keyboard, where the editor is taken to and when, and what is stored.
// How the real dock lays out its tabs is dock-tabs-dom.test.js's, and the real Monaco's behaviour is the browser check's.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { page, El } = require("./editorpage.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function fnSource(file, name) {
  const text = src(file);
  const m = new RegExp("^(?:async )?function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function of " + file);
  return text.slice(m.index, text.indexOf("\n}\n", m.index) + 2);
}

const HEAD = "a\nb\nX\nd\n";   // git's text of the file
const DISK = "a\nb\nc\nd\n";   // the file as the agent left it: line 3 differs

// ev / file: what Activity brings for an edit that finished (the call and the file totals).
const ev = (over) => Object.assign({ ts: 100, kind: "tool", tool: "Edit", detail: "/p/a.go", state: "ok" }, over);
const file = (p, ts) => ({ path: p, add: 1, del: 0, ts });

// A page whose dock shows session s1, with Follow's one subscription recorded and the dock's tabs faked as said above.
async function setup(opts) {
  const o = Object.assign({ store: {}, git: true, text: DISK, serve: true }, opts);
  const subs = [];
  const shown = [];
  const dock = { tabs: new Map(), active: null };
  const p = page({
    drawerState: { open: "files", sessionId: "s1" },
    lastSessions: [{ id: "s1", name: "alpha" }, { id: "s2", name: "beta" }],
    window: { localStorage: { getItem: (k) => (k in o.store ? o.store[k] : null), setItem: (k, v) => { o.store[k] = String(v); } } },
    dockWatchSubscribe: (sid, fn) => { const s = { sid, fn, live: true }; subs.push(s); return () => { s.live = false; }; },
    drawerShowFile: (pth, op) => {
      shown.push([pth, op]);
      let t = dock.tabs.get(pth);
      if (!t) { t = new p.EditorTab(pth, { host: p.host }); dock.tabs.set(pth, t); }
      if (dock.active !== t) {
        if (dock.active) dock.active.onHide();
        dock.active = t;
        t.onAttach();
      }
      return true;
    },
    dockAttached: () => dock.active,
    dockViewerList: () => [...dock.tabs.values()],
  });
  p.load("drawerfiles.js");
  new (require("node:vm").Script)(fnSource("dockwatch.js", "dwUnderRoot"), { filename: "dwUnderRoot" }).runInContext(p.ctx);
  p.st.shown = true; // the dock is on screen: the poll of the file on disk runs
  if (o.serve) p.serve(o.text);
  if (o.git) { p.st.git.on = true; p.st.git.answer = { state: "ok", content: HEAD }; }
  const focusReads = { reader: 0 };
  const focusReader = p.EditorTab.prototype.focusReader;
  p.EditorTab.prototype.focusReader = function () { focusReads.reader++; return focusReader.call(this); };
  const f = {
    p, subs, shown, dock, store: o.store, focusReads,
    live: () => subs.filter((s) => s.live),
    // The watch's answer for the session the dock shows: the one live subscriber hears (files, newEvents).
    hear(files, events) { const l = f.live(); assert.equal(l.length, 1, "exactly one subscriber to tell"); l[0].fn(files, events); },
    // The timers of the reveal's waiting (100 ms steps) and everything the editor's requests were waiting on, until quiet.
    async drain(rounds) {
      for (let i = 0; i < (rounds || 80); i++) {
        await p.settle();
        const due = p.st.timers.filter((t) => !t.dead && t.ms === 100);
        if (!due.length) return;
        for (const t of due) { t.dead = true; t.fn(); }
      }
    },
    waiting: () => p.st.timers.filter((t) => !t.dead && t.ms === 100).length,
    ed: () => p.st.editors[0],
    on: () => p.get("editorFollowSet")("s1", true),
  };
  return f;
}

const revealed = (f) => (f.ed() ? f.ed().centered : []);

test("Follow is off until it is turned on, and turning it on listens to the session the dock shows: one subscription, no poll of its own", async () => {
  const f = await setup();
  f.p.get("editorFollowSync")();
  assert.equal(f.subs.length, 0, "off by default: nobody listens");
  assert.equal(f.p.get("editorFollowOn")("s1"), false);
  f.on();
  assert.deepEqual(f.subs.map((s) => [s.sid, s.live]), [["s1", true]]);
  f.p.get("editorFollowSync")();
  f.p.get("editorFollowSync")();
  assert.equal(f.subs.length, 1, "the dock's repaints do not subscribe again");
  assert.equal(f.p.st.intervals.filter((i) => !i.dead && i.ms !== 2000).length, 0, "and Follow starts no timer of its own that polls");
});

test("a finished edit opens the file as the dock's PREVIEW tab and never takes the keyboard", async () => {
  const f = await setup();
  f.on();
  const terminal = { id: "the terminal" };
  f.p.doc.activeElement = terminal; // the user is typing in the terminal
  f.hear([file("/p/a.go", 100)], [ev()]);
  assert.deepEqual(JSON.parse(JSON.stringify(f.shown)), [["/p/a.go", { preview: true }]], "drawerShowFile(path, {preview: true}): the tab the next one replaces");
  await f.drain();
  assert.equal(f.dock.active.model.value, DISK, "the file was read");
  assert.equal(f.p.doc.activeElement, terminal, "the keyboard is where it was");
  assert.equal(f.focusReads.reader, 0, "nothing asked the reader to take the keyboard");
  assert.equal(f.ed().focused, 0, "and the editor never took it either");
  // The next edit is another file: another preview, never a pile of tabs of Follow's own making.
  f.hear([file("/p/b.go", 200), file("/p/a.go", 100)], [ev({ ts: 200, detail: "/p/b.go" })]);
  assert.deepEqual(f.shown.map((s) => s[1].preview), [true, true]);
  await f.drain();
  assert.equal(f.ed().focused, 0);
});

test("only a finished call of a tool that writes a file counts: Edit, Write, MultiEdit, NotebookEdit; anything else is nothing", async () => {
  const f = await setup();
  f.on();
  const files = [file("/p/a.go", 100)];
  f.hear(files, [ev({ tool: "Read" }), ev({ tool: "Bash", detail: "go test ./..." }), ev({ tool: "Grep" }), ev({ state: "pending" }), ev({ state: "error" }), ev({ kind: "you" }), ev({ kind: "credential" })]);
  assert.deepEqual(f.shown, [], "reads, searches, commands, calls pending or failed, other rows");
  for (const tool of ["Write", "MultiEdit", "NotebookEdit", "Edit"]) {
    f.hear([file("/p/a.go", 100)], [ev({ tool, ts: 100 })]);
  }
  assert.equal(f.shown.length, 4, "each of the four tools opened it");
});

test("a path outside the dock's root, a masked or cut detail and a detail Activity's files do not have are ignored; the path opened is the entry's", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/elsewhere/x.go", 100)], [ev({ detail: "/elsewhere/x.go" })]);
  f.hear([file("/p/[masked:github-token].go", 100)], [ev({ detail: "/p/[masked:github-token].go" })]);
  f.hear([file("/p/a.go", 100)], [ev({ detail: "/p/zzz.go" })]);
  f.hear([], [ev()]);
  assert.deepEqual(f.shown, [], "none of them opens anything");
  f.hear([file("/p/sub/a.go", 100)], [ev({ detail: "sub/a.go" })]);
  assert.deepEqual(f.shown.map((s) => s[0]), ["/p/sub/a.go"], "a relative detail resolves to the entry that ends with it: its path is what is opened");
  const root = f.p.get("dwUnderRoot");
  assert.equal(root("/p/x.go"), true);
  assert.equal(root("/p2/x.go"), false, "a sibling folder that shares the prefix is not under the root");
  assert.equal(root("/p"), false, "nor is the root itself");
});

test("only new events: the first answer is a baseline, a file seen is not shown again, and an edit first seen as a pending call is shown once it finished", async () => {
  const f = await setup();
  f.on();
  // The first answer after Follow came on carries the files that were already edited: that is what was there, not news.
  f.hear([file("/p/a.go", 100)], []);
  assert.deepEqual(f.shown, []);
  f.hear([file("/p/a.go", 100)], []);
  assert.deepEqual(f.shown, [], "the same answer again is not news");
  // The watch saw the call pending (an agent that waited on a permission prompt): not an event Follow can show...
  f.hear([file("/p/a.go", 100)], [ev({ ts: 150, detail: "/p/b.go", state: "pending" })]);
  assert.deepEqual(f.shown, []);
  // ...and when it finished the row is not new to the watch (same ts), but its file is now in `files` with a later time.
  f.hear([file("/p/b.go", 160), file("/p/a.go", 100)], []);
  assert.deepEqual(f.shown.map((s) => s[0]), ["/p/b.go"], "shown once, from files");
  f.hear([file("/p/b.go", 160), file("/p/a.go", 100)], []);
  assert.equal(f.shown.length, 1, "and not again");
  // An event that is new and finished is shown at once, whatever the mark.
  f.hear([file("/p/c.go", 300), file("/p/b.go", 160), file("/p/a.go", 100)], [ev({ ts: 300, detail: "/p/c.go" })]);
  assert.deepEqual(f.shown.map((s) => s[0]), ["/p/b.go", "/p/c.go"]);
});

test("the newest finished edit of an answer wins when several arrive together", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/p/c.go", 130), file("/p/b.go", 120)], [ev({ ts: 140, tool: "Bash", detail: "make" }), ev({ ts: 130, detail: "/p/c.go" }), ev({ ts: 120, detail: "/p/b.go" })]);
  assert.deepEqual(f.shown.map((s) => s[0]), ["/p/c.go"], "one open, the newest, not a flash through each");
});

test("it reveals the first line that differs from git, in the editor, without moving the keyboard", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  await f.drain();
  assert.deepEqual(revealed(f), [3], "line 3 is the first change of HEAD against the file");
  assert.deepEqual(f.ed().positions.at(-1), { lineNumber: 3, column: 1 });
  assert.equal(f.waiting(), 0, "nothing is left waiting");
  const tab = f.dock.active;
  assert.ok(tab.changes && tab.changes.lineChanges.length, "from the gutter's own diff of the buffer");
  assert.equal(f.ed().focused, 0);
});

test("no change against git is line 1, so is git that cannot say, and a removal at the very end is shown on the last line without a toast", async () => {
  const same = await setup({ text: HEAD });
  same.on();
  same.hear([file("/p/a.go", 100)], [ev()]);
  await same.drain();
  assert.deepEqual(revealed(same), [1], "the file is as git has it: line 1");

  const nogit = await setup({ git: false });
  nogit.on();
  nogit.hear([file("/p/a.go", 100)], [ev()]);
  await nogit.drain();
  assert.deepEqual(revealed(nogit), [1], "git's status is not known: after a short wait, line 1");
  assert.equal(nogit.p.st.git.shows.length, 0);

  const gone = await setup({ text: "a\nb\nc\n" }); // HEAD had a fourth line, the agent removed it
  gone.p.st.git.answer = { state: "ok", content: "a\nb\nc\nd\n" };
  gone.on();
  gone.hear([file("/p/a.go", 100)], [ev()]);
  await gone.drain();
  assert.deepEqual(revealed(gone), [3], "the removal sits past the last line: the last line is shown");
  assert.deepEqual(gone.p.st.toasts, [], "revealLine's out-of-range toast is for links, not for Follow");

  const unavailable = await setup();
  unavailable.p.st.git.answer = { state: "unavailable", reason: "git is not available here" };
  unavailable.on();
  unavailable.hear([file("/p/a.go", 100)], [ev()]);
  await unavailable.drain();
  assert.deepEqual(revealed(unavailable), [1], "git answered that it cannot: line 1");
});

test("an open clean tab is compared with the disk first, so the line it reveals is in the agent's text and not the stale one", async () => {
  const f = await setup({ text: HEAD }); // the file as git has it: nothing differs yet
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  await f.drain();
  assert.deepEqual(revealed(f), [1]);
  const tab = f.dock.active;
  assert.equal(tab.model.value, HEAD);
  // The agent edits the file the user already has open: the buffer is stale until the disk is looked at.
  f.p.serve(DISK);
  f.p.st.git.answer = { state: "ok", content: HEAD };
  f.hear([file("/p/a.go", 200)], [ev({ ts: 200 })]);
  assert.equal(tab.model.value, HEAD, "nothing has been read yet");
  await f.drain();
  assert.equal(tab.model.value, DISK, "reloaded from the disk by Follow's own check, not left to the poll 2 s later");
  assert.equal(tab.dirty(), false);
  assert.deepEqual(revealed(f).at(-1), 3, "and the line is the first change of the new text");
  assert.equal(f.dock.active, tab, "the same tab, shown again");
  assert.equal(f.shown.length, 2);
});

test("a buffer holding an unsaved edit is shown but never revealed, and neither is an editor that has the keyboard", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  await f.drain();
  const tab = f.dock.active;
  const before = revealed(f).length;
  tab.model.type("a\nmine\nc\nd\n");
  assert.equal(tab.dirty(), true);
  f.hear([file("/p/a.go", 200)], [ev({ ts: 200 })]);
  await f.drain();
  assert.equal(f.shown.length, 2, "the file is still shown");
  assert.equal(revealed(f).length, before, "but the caret of a buffer being edited is not moved");
  assert.equal(tab.model.value, "a\nmine\nc\nd\n", "and the buffer is the user's");
  // The editor has the keyboard: the user is in it.
  tab.discardEdits();
  f.ed().focus();
  const focused = f.ed().focused;
  f.hear([file("/p/a.go", 300)], [ev({ ts: 300 })]);
  await f.drain();
  assert.equal(revealed(f).length, before, "not under the user's typing");
  assert.equal(f.ed().focused, focused, "and Follow did not call focus either");
});

test("a tab the user left before it was ready is not revealed, and an image or a binary file has no lines to show", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  f.dock.active.onHide(); // the user went to another tab while the file was still being read
  await f.drain();
  assert.deepEqual(revealed(f), []);
  assert.equal(f.waiting(), 0, "and nothing keeps looking");

  const bin = await setup({ serve: false });
  bin.p.setRead({ ok: true, status: 200, data: { kind: "binary", size: 9 } });
  bin.on();
  bin.hear([file("/p/x.bin", 100)], [ev({ detail: "/p/x.bin" })]);
  await bin.drain();
  assert.equal(bin.shown.length, 1);
  assert.ok(bin.dock.active.inner, "opened as the binary viewer");
  assert.equal(bin.waiting(), 0);
});

test("a file that never loads is given up on: the wait is bounded and leaves no timer behind", async () => {
  const f = await setup();
  f.p.serve().fail = 500; // every read of the file fails, for good
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  await f.drain(400);
  assert.equal(f.dock.active.model, null);
  assert.equal(f.waiting(), 0, "gave up (FOLLOW_SETTLE_MAX_MS) and left no timer");
  assert.deepEqual(revealed(f), []);
});

test("a newer edit takes over the wait: one reveal, for the file shown last", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  f.hear([file("/p/b.go", 200), file("/p/a.go", 100)], [ev({ ts: 200, detail: "/p/b.go" })]);
  assert.ok(f.waiting() <= 1, "one wait, not two");
  await f.drain();
  assert.equal(f.dock.active.path, "/p/b.go");
  assert.equal(revealed(f).length, 1, "revealed once");
});

test("an answer that comes after the dock moved to another session is dropped", async () => {
  const f = await setup();
  f.on();
  const stale = f.live()[0];
  f.p.ctx.drawerState.sessionId = "s2"; // the dock moved; the subscription has not been told yet
  stale.fn([file("/p/a.go", 100)], [ev()]);
  assert.deepEqual(f.shown, [], "drawerShowFile would open it in s2's dock");
  f.p.get("editorFollowSync")();
  assert.equal(stale.live, false, "and the next sync lets go of s1's subscription (s2 has Follow off)");
  assert.equal(f.live().length, 0);
});

test("the subscription follows the dock: another session, a close, and Follow turned off", async () => {
  const f = await setup();
  const sync = () => f.p.get("editorFollowSync")();
  f.on();
  f.p.get("editorFollowSet")("s2", true);
  assert.deepEqual(f.live().map((s) => s.sid), ["s1"], "Follow for a session the dock is not showing listens to nothing");
  f.p.ctx.drawerState.sessionId = "s2";
  sync();
  assert.deepEqual(f.live().map((s) => s.sid), ["s2"], "the dock moved: s1's subscription is gone, s2's is on");
  f.p.get("editorFollowSet")("s2", false);
  assert.deepEqual(f.live().map((s) => s.sid), [], "turned off for the session the dock shows");
  f.p.get("editorFollowSet")("s2", true);
  f.p.st.shown = false; // the dock closed (drawerOwnsFileClicks is false)
  sync();
  assert.deepEqual(f.live().map((s) => s.sid), [], "a closed dock listens to nothing");
  f.p.st.shown = true;
  sync();
  assert.deepEqual(f.live().map((s) => s.sid), ["s2"]);
  assert.equal(f.subs.filter((s) => s.sid === "s2").length, 3, "each opening is a fresh subscription: the first answer after it is a baseline");
});

test("closing the dock or moving it stops a reveal that was waiting", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  assert.ok(f.waiting() >= 1);
  f.p.st.shown = false;
  f.p.get("editorFollowSync")();
  assert.equal(f.waiting(), 0);
  await f.drain();
  assert.deepEqual(revealed(f), []);
});

test("Follow is remembered per session in localStorage under agent-box:dockFollow as {sid: true}", async () => {
  const store = {};
  const a = await setup({ store });
  a.p.get("editorFollowSet")("s1", true);
  assert.equal(store["agent-box:dockFollow"], '{"s1":true}');
  a.p.get("editorFollowSet")("s2", true);
  assert.deepEqual(JSON.parse(store["agent-box:dockFollow"]), { s1: true, s2: true });
  a.p.get("editorFollowSet")("s1", false);
  assert.deepEqual(JSON.parse(store["agent-box:dockFollow"]), { s2: true }, "off removes the session's entry");
  // A reload: a new page reads it.
  const b = await setup({ store });
  assert.equal(b.p.get("editorFollowOn")("s2"), true);
  assert.equal(b.p.get("editorFollowOn")("s1"), false, "per session");
  assert.equal(b.p.get("editorFollowOn")("nope"), false);
  assert.equal(b.p.get("editorFollowOn")(null), false);
  // A session that is gone from the list is dropped on the next write.
  b.p.ctx.lastSessions = [{ id: "s1", name: "alpha" }];
  b.p.get("editorFollowSet")("s1", true);
  assert.deepEqual(JSON.parse(store["agent-box:dockFollow"]), { s1: true });
});

test("what is stored is read with care: only {sid: true} counts, and storage that is missing or throws is Follow off", async () => {
  for (const raw of ["not json", "[true]", "null", '"s1"', '{"s1":"yes","s2":1,"s3":false}', ""]) {
    const f = await setup({ store: { "agent-box:dockFollow": raw } });
    assert.equal(f.p.get("editorFollowOn")("s1"), false, raw);
    assert.equal(f.p.get("editorFollowOn")("s2"), false, raw);
  }
  const noStore = await setup();
  delete noStore.p.ctx.window;
  noStore.p.get("editorFollowSet")("s1", true); // nowhere to write: it still works for the life of the page
  assert.equal(noStore.p.get("editorFollowOn")("s1"), true);
  const throws = await setup({ store: {} });
  throws.p.ctx.window = { get localStorage() { throw new Error("blocked"); } };
  assert.doesNotThrow(() => throws.p.get("editorFollowSet")("s1", true));
  assert.equal(throws.p.get("editorFollowOn")("s1"), true);
});

test("the toggle is a button in the tab's actions, an eye and the word, pressed when Follow is on for the session, and every tab says the same", async () => {
  const f = await setup();
  const a = await f.p.open("/p/a.go");
  const b = await f.p.open("/p/b.go");
  f.dock.tabs.set("/p/a.go", a); // both are the dock's tabs: the setting repaints every one
  f.dock.tabs.set("/p/b.go", b);
  for (const t of [a, b]) {
    assert.ok(t.followBtn, "built with the tab's actions");
    assert.equal(t.followBtn.className, "btn btn-sm dk-follow");
    assert.equal(t.followBtn.tag, "button");
    assert.equal(t.followBtn.textContent, "Follow");
    assert.equal(t.followBtn.getAttribute("aria-pressed"), "false");
    assert.ok(t.actsEl.children.includes(t.followBtn), "in the actions, with the Code | Changes toggle and the ⋯");
  }
  assert.deepEqual(a.actsEl.children.map((c) => c.className.split(" ")[0]), ["seg", "seg", "seg", "btn", "btn"], "Code | Changes, the diff layout (Changes only), Image | Changes (an image only), Follow, then ⋯ (Follow before the ⋯)");
  assert.equal(a.actsEl.children[3], a.followBtn);
  a.followBtn.click();
  assert.equal(f.p.get("editorFollowOn")("s1"), true, "a press turns it on for the dock's session");
  assert.equal(a.followBtn.getAttribute("aria-pressed"), "true");
  assert.equal(b.followBtn.getAttribute("aria-pressed"), "true", "the other tab's toggle follows (it is the session's setting)");
  assert.match(a.followBtn.title, /^Following the agent/);
  assert.deepEqual(f.live().map((s) => s.sid), ["s1"], "and the dock is open on s1, so it listens now");
  b.followBtn.click();
  assert.equal(f.p.get("editorFollowOn")("s1"), false, "a second press turns it off");
  assert.equal(a.followBtn.getAttribute("aria-pressed"), "false");
});

test("the session's Follow, not the tab's: a toggle in a dock of another session reads that session's setting", async () => {
  const f = await setup();
  f.p.get("editorFollowSet")("s2", true);
  const tab = await f.p.open("/p/a.go");
  assert.equal(tab.followBtn.getAttribute("aria-pressed"), "false", "the dock shows s1, where it is off");
  f.p.host.sessionId = () => "s2";
  tab.paint();
  assert.equal(tab.followBtn.getAttribute("aria-pressed"), "true");
});

test("a subscription that was let go hears nothing more, even from a callback the watch had already collected", async () => {
  const f = await setup();
  f.on();
  const old = f.live()[0];
  f.p.get("editorFollowSet")("s1", false);
  assert.equal(old.live, false);
  old.fn([file("/p/a.go", 100)], [ev()]); // dockwatch.js dwNotify walks a copy of its list: a late call is possible
  assert.deepEqual(f.shown, []);
});

test("only the tab that was asked for is revealed: a dock that did not show it (parked, another session) reveals nothing", async () => {
  const f = await setup();
  f.on();
  f.p.ctx.drawerShowFile = (pth, op) => { f.shown.push([pth, op]); return true; }; // the dock accepted the call but shows no such tab
  f.hear([file("/p/a.go", 100)], [ev()]);
  assert.equal(f.shown.length, 1);
  assert.equal(f.waiting(), 0, "nothing to wait for");
  assert.equal(f.p.st.editors.length, 0, "and no editor was touched");
  // Another file is in front: the tab that is attached is not the one that was asked for.
  const other = new f.p.EditorTab("/p/other.go", { host: f.p.host });
  f.dock.active = other;
  other.onAttach();
  f.p.ctx.dockAttached = () => other;
  f.hear([file("/p/a.go", 200)], [ev({ ts: 200 })]);
  await f.drain();
  assert.deepEqual(revealed(f), [], "the file on screen is not the one Follow was told about");
});

test("the reveal waits for git's base text, so it is the first change and not line 1 reached early", async () => {
  const f = await setup();
  let release;
  f.p.st.git.gate = new Promise((r) => { release = r; }); // POST /api/git/show is out and has not answered
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  for (let i = 0; i < 10; i++) {
    await f.p.settle();
    for (const t of f.p.st.timers.filter((x) => !x.dead && x.ms === 100)) { t.dead = true; t.fn(); }
  }
  assert.deepEqual(revealed(f), [], "a second of waiting, and nothing is revealed yet");
  assert.equal(f.waiting(), 1, "still looking");
  release();
  await f.drain();
  assert.deepEqual(revealed(f), [3]);
});

test("drawerPaint tells Follow, so the subscription moves with the dock (the one place that runs on every session change, poll and close)", () => {
  const body = fnSource("filesdrawer.js", "drawerPaint");
  assert.match(body, /if \(typeof editorFollowSync === "function"\) editorFollowSync\(\);/);
});

// --- the user is typing in the editor ------------------------------------------------------------------------------------------

test("while the editor has the keyboard Follow does nothing, not even swapping the tab, and the next answer after the keyboard left still shows the edit", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  await f.drain();
  assert.equal(f.dock.active.path, "/p/a.go");
  const ed = f.ed();
  ed.focus(); // the user clicked into the editor and is typing
  const before = revealed(f).length;
  f.hear([file("/p/b.go", 200), file("/p/a.go", 100)], [ev({ ts: 200, detail: "/p/b.go" })]);
  assert.equal(f.shown.length, 1, "drawerShowFile was not called: the active tab is not swapped (that moves the editor's element and drops the caret)");
  assert.equal(f.dock.active.path, "/p/a.go");
  assert.equal(f.waiting(), 0);
  // The user went back to the terminal. Nothing new happened, but the answer that comes next carries the edit that was held back.
  ed.blur();
  f.hear([file("/p/b.go", 200), file("/p/a.go", 100)], []);
  assert.deepEqual(f.shown.map((x) => x[0]), ["/p/a.go", "/p/b.go"], "the mark was kept, so b.go is shown now");
  await f.drain();
  assert.equal(f.dock.active.path, "/p/b.go");
  assert.ok(revealed(f).length > before);
  f.hear([file("/p/b.go", 200), file("/p/a.go", 100)], []);
  assert.equal(f.shown.length, 2, "and only once");
});

test("a new event after the keyboard left follows too, and a first answer that was held back leaves the mark at 'everything so far is news'", async () => {
  const f = await setup();
  f.on();
  await f.p.open("/p/a.go"); // the editor exists and the user is in it
  f.ed().focus();
  f.hear([file("/p/c.go", 300)], [ev({ ts: 300, detail: "/p/c.go" })]); // the very first answer carries a finished edit
  assert.deepEqual(f.shown, []);
  f.ed().blur();
  f.hear([file("/p/c.go", 300)], []);
  assert.deepEqual(f.shown.map((x) => x[0]), ["/p/c.go"], "not lost as a baseline");
  f.hear([file("/p/d.go", 400), file("/p/c.go", 300)], [ev({ ts: 400, detail: "/p/d.go" })]);
  assert.deepEqual(f.shown.map((x) => x[0]), ["/p/c.go", "/p/d.go"]);
});

test("an answer with nothing to show moves the mark even while the keyboard is in the editor", async () => {
  const f = await setup();
  f.on();
  await f.p.open("/p/a.go");
  f.ed().focus();
  f.hear([file("/p/x.go", 50)], []); // the baseline
  f.hear([file("/p/x.go", 50)], [ev({ tool: "Read", ts: 60 })]);
  f.ed().blur();
  f.hear([file("/p/x.go", 50)], []);
  assert.deepEqual(f.shown, [], "nothing was held back, so nothing is shown late");
});

test("the Changes view's new side having the keyboard counts as typing too", async () => {
  const f = await setup();
  f.on();
  f.hear([file("/p/a.go", 100)], [ev()]);
  await f.drain();
  const side = { hasTextFocus: () => true };
  f.dock.active.diff = { diff: { getModifiedEditor: () => side } };
  f.hear([file("/p/b.go", 200)], [ev({ ts: 200, detail: "/p/b.go" })]);
  assert.equal(f.shown.length, 1);
  f.dock.active.diff = null;
  f.hear([file("/p/b.go", 200)], []);
  assert.equal(f.shown.length, 2);
});

// --- the toggle is reachable without a text tab ---------------------------------------------------------------------------------

// A page whose document has the dock's two hosts: the bar's actions area and the empty dock's hint.
function withHosts(f) {
  const acts = new El("div", f.p.doc);
  const empty = new El("div", f.p.doc);
  empty.append("Open a file from the tree, or click a path in the terminal");
  f.p.doc.getElementById = (id) => ({ "dock-acts": acts, "drawer-vempty": empty }[id] || null);
  return { acts, empty };
}

test("an empty dock has a Follow toggle in its hint, for the session the dock shows, with the same state and storage as every other", async () => {
  const f = await setup();
  const { empty } = withHosts(f);
  f.p.get("editorFollowSync")(); // drawerPaint runs it: the toggle is made
  const b = empty.children.find((c) => c.id === "dock-follow-empty");
  assert.ok(b, "in #drawer-vempty, after the hint");
  assert.equal(empty.children[0].textContent, "Open a file from the tree, or click a path in the terminal", "the hint's own text is untouched");
  assert.equal(b.className, "btn btn-sm dk-follow");
  assert.equal(b.tag, "button");
  assert.equal(b.type, "button");
  assert.equal(b.textContent, "Follow");
  assert.equal(b.hidden, false);
  assert.equal(b.getAttribute("aria-pressed"), "false");
  b.click();
  assert.equal(f.p.get("editorFollowOn")("s1"), true, "the dock's session");
  assert.deepEqual(JSON.parse(f.store["agent-box:dockFollow"]), { s1: true }, "the same storage");
  assert.equal(b.getAttribute("aria-pressed"), "true");
  assert.match(b.title, /^Following the agent/);
  assert.deepEqual(f.live().map((s) => s.sid), ["s1"], "and it listens: turned on from an empty dock, it waits for the agent");
  // The setting is the session's: a toggle on a tab and this one say the same.
  const tab = await f.p.open("/p/a.go");
  f.dock.tabs.set("/p/a.go", tab);
  assert.equal(tab.followBtn.getAttribute("aria-pressed"), "true");
  b.click();
  assert.equal(tab.followBtn.getAttribute("aria-pressed"), "false");
  assert.equal(b.getAttribute("aria-pressed"), "false");
  assert.deepEqual(f.live().map((s) => s.sid), []);
});

test("the empty dock's toggle follows the dock's session, and is hidden while the dock shows none; one toggle, not one per repaint", async () => {
  const f = await setup();
  const { empty } = withHosts(f);
  f.p.get("editorFollowSync")();
  f.p.get("editorFollowSync")();
  f.p.get("editorFollowSync")();
  assert.equal(empty.children.filter((c) => c.id === "dock-follow-empty").length, 1);
  const b = empty.children.find((c) => c.id === "dock-follow-empty");
  f.p.get("editorFollowSet")("s2", true);
  assert.equal(b.getAttribute("aria-pressed"), "false", "the dock shows s1");
  f.p.ctx.drawerState.sessionId = "s2";
  f.p.get("editorFollowSync")();
  assert.equal(b.getAttribute("aria-pressed"), "true", "now s2, where it is on");
  f.p.ctx.drawerState.sessionId = null;
  f.p.get("editorFollowSync")();
  assert.equal(b.hidden, true, "no session, nothing to follow");
  b.click();
  assert.equal(f.store["agent-box:dockFollow"], '{"s2":true}', "and a press with no session changes nothing");
});

test("the tab's actions keep Follow for a file that is not text (an image, a binary) and while it is still being read; the ⋯ and Code | Changes are for text", async () => {
  const f = await setup({ serve: false });
  const { acts } = withHosts(f);
  f.p.setRead({ ok: true, status: 200, data: { kind: "binary", size: 9 } });
  const bin = new f.p.EditorTab("/p/x.bin", { host: f.p.host });
  assert.equal(bin.seg.el.hidden, true, "Code | Changes is not there before the file is read as text");
  assert.equal(bin.moreBtn.hidden, true);
  assert.equal(bin.followBtn.hidden, false, "Follow is, even while it loads");
  bin.onAttach();
  assert.ok(acts.children.includes(bin.actsEl), "the actions are in the dock's bar");
  await f.p.settle();
  assert.ok(bin.inner, "read as a binary file");
  assert.equal(bin.actsEl.hidden, false);
  assert.equal(bin.seg.el.hidden, true);
  assert.equal(bin.moreBtn.hidden, true);
  assert.equal(bin.followBtn.hidden, false);
  assert.ok(acts.children.includes(bin.actsEl), "and they stayed there when the file turned out to be a binary one");
  bin.followBtn.click();
  assert.equal(f.p.get("editorFollowOn")("s1"), true, "the same state and storage");
  assert.deepEqual(JSON.parse(f.store["agent-box:dockFollow"]), { s1: true });
  bin.onHide();
  assert.ok(!acts.children.includes(bin.actsEl), "hidden with its tab");
  // A binary file's tab that is brought back (already read: it has its viewer) gets its Follow in the bar as well.
  bin.onAttach();
  assert.ok(acts.children.includes(bin.actsEl), "shown again with its tab");
  const late = new f.p.EditorTab("/p/y.bin", { host: f.p.host });
  await f.p.settle();
  assert.ok(late.inner);
  late.onAttach();
  assert.ok(acts.children.includes(late.actsEl), "a tab read before it was shown puts its actions in the bar when it is");
  assert.equal(late.followBtn.hidden, false);
  // A text file: all three.
  const f2 = await setup();
  const h2 = withHosts(f2);
  const txt = await f2.p.open("/p/a.go");
  assert.ok(h2.acts.children.includes(txt.actsEl));
  assert.equal(txt.seg.el.hidden, false);
  assert.equal(txt.moreBtn.hidden, false);
  assert.equal(txt.followBtn.hidden, false);
});
