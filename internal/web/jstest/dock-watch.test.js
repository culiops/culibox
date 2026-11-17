// Task 9c: live change tracking while the agent works (dockwatch.js), run for real in a vm. Part one is the poll on its own
// against a fake page, fake timers and a fake fetch: when it polls (the dock shows a session's files, the page is visible, no
// page view is over it, the session is working), that it never overlaps itself, that it stops and restarts with the session
// and leaves no timer behind, what it does with new or changed files (the page's list at once, one fresh git status) and what
// its subscribers hear. Part two runs it together with the real Changes section, gitstatus.js and changesmodel.js: an edit
// shows as a provisional "…" row at once, a fresh status and a fresh numstat follow, and git's answer takes the row over.
// changesmodel.test.js has the pure rules; the visual check proves the look.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const settle = async () => { for (let i = 0; i < 8; i++) await new Promise((r) => setImmediate(r)); };
// Arrays and objects made inside the vm have its prototypes: compare them as plain data.
const plain = (x) => JSON.parse(JSON.stringify(x === undefined ? null : x));

const file = (p, ts, add, del) => ({ path: p, add: add === undefined ? 1 : add, del: del === undefined ? 0 : del, ts });
const ev = (ts, tool, detail) => ({ ts, kind: "tool", tool: tool || "Edit", detail: detail || "x", state: "ok" });
const body = (files, events) => ({ claude: true, events: events || [], more: false, files, summary: {} });

// --- part one: the poll on its own ----------------------------------------------------------------------------------------

function watchPage(over) {
  const st = {
    fetches: [], intervals: [], timeouts: [], listeners: {}, hidden: false, page: "", owns: true, root: "/w",
    refreshes: [], sets: [],
  };
  const document = {
    get hidden() { return st.hidden; },
    addEventListener: (ev2, fn) => { st.listeners[ev2] = fn; },
  };
  const ctx = vm.createContext(Object.assign({
    console, Object, Array, Map, Set, Math, Number, JSON, Promise, AbortController, encodeURIComponent, Date,
    document,
    setInterval: (fn, ms) => { st.intervals.push({ fn, ms, live: true }); return st.intervals.length; },
    clearInterval: (id) => { if (st.intervals[id - 1]) st.intervals[id - 1].live = false; },
    setTimeout: (fn, ms) => { st.timeouts.push({ fn, ms, live: true }); return st.timeouts.length; },
    clearTimeout: (id) => { if (st.timeouts[id - 1]) st.timeouts[id - 1].live = false; },
    fetch: (url, o) => new Promise((resolve, reject) => {
      const f = { url, signal: o && o.signal, aborted: false, resolve, reject };
      if (f.signal) f.signal.addEventListener("abort", () => { f.aborted = true; reject(Object.assign(new Error("aborted"), { name: "AbortError" })); });
      st.fetches.push(f);
    }),
    currentPage: () => st.page,
    drawerOwnsFileClicks: () => st.owns,
    ftRoot: () => st.root,
    gitStatusRefreshSoon: (o) => { st.refreshes.push(o); },
    __sets: st.sets,
  }, over));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(src("accessmodel.js"), "accessmodel.js"); // accReadReply, the real one
  load(src("drawerfiles.js"), "drawerfiles.js"); // changedSegs, the real one
  load('let drawerState = { open: "files", sessionId: "s1", pinned: false }; let drawerChangedFiles = new Map(); let lastSessions = [{ id: "s1", activity: "working" }, { id: "s2", activity: "idle" }];', "state");
  // filesdrawer.js's own: the page's list for the session, and every reader repainted (a fake page has nothing to repaint).
  load("function drawerSetChanged(sid, files) { drawerChangedFiles.set(sid, files); __sets.push([sid, files]); }", "drawerSetChanged");
  load(src("dockwatch.js"), "dockwatch.js");
  const p = {
    ctx, st, run: (code) => load(code, "test"),
    sync: () => load("dockWatchSync()", "sync"),
    activity: (sid, act) => load("lastSessions = lastSessions.map((s) => s.id === " + JSON.stringify(sid) + " ? Object.assign({}, s, { activity: " + JSON.stringify(act) + " }) : s)", "act"),
    open: (sid) => load("drawerState = " + JSON.stringify({ open: "files", sessionId: sid, pinned: false }), "open"),
    close: () => load('drawerState = { open: null, sessionId: null, pinned: false }', "close"),
    seed: (sid, files) => { ctx.__v = files; load("drawerChangedFiles.set(" + JSON.stringify(sid) + ", __v)", "seed"); },
    held: (sid) => plain(load("drawerChangedFiles.get(" + JSON.stringify(sid) + ")", "held")),
    // Answer the oldest unanswered request with a 200 and `data`, and let the poll's continuation run.
    reply: async (data, status) => {
      const f = st.fetches.find((x) => !x.done && !x.aborted);
      assert.ok(f, "a request is out");
      f.done = true;
      f.resolve({ status: status || 200, json: async () => data });
      await settle();
    },
    tick: () => { const iv = st.intervals.filter((x) => x.live).at(-1); iv.fn(); },
    liveIntervals: () => st.intervals.filter((x) => x.live).length,
    liveTimeouts: () => st.timeouts.filter((x) => x.live).length,
    pending: () => st.fetches.filter((x) => !x.done && !x.aborted).length,
  };
  return p;
}

test("it polls the dock's session at once and then every 2000 ms, through the Activity endpoint, 20 events", async () => {
  const p = watchPage();
  p.sync();
  assert.strictEqual(p.st.fetches.length, 1, "the first ask is immediate");
  assert.strictEqual(p.st.fetches[0].url, "/api/sessions/s1/activity?limit=20");
  assert.strictEqual(p.st.intervals.length, 1);
  assert.strictEqual(p.st.intervals[0].ms, 2000);
  await p.reply(body([]));
  p.tick();
  assert.strictEqual(p.st.fetches.length, 2);
  p.sync();
  p.sync();
  assert.strictEqual(p.st.intervals.length, 1, "the poll's own sync calls (every session poll) start no second timer");
  assert.strictEqual(p.st.fetches.length, 2, "and ask nothing extra");
});

test("a session id is encoded into the path", () => {
  const p = watchPage();
  p.run('lastSessions = [{ id: "a/b c?", activity: "working" }]');
  p.open("a/b c?");
  p.sync();
  assert.strictEqual(p.st.fetches[0].url, "/api/sessions/a%2Fb%20c%3F/activity?limit=20");
});

test("nothing is asked while the session is not working, whatever else is true", () => {
  for (const act of ["idle", "waiting", "done", "crashed", "detached", "", undefined, null]) {
    const p = watchPage();
    p.activity("s1", act);
    p.sync();
    assert.strictEqual(p.st.fetches.length, 0, String(act));
    assert.strictEqual(p.st.intervals.length, 0, "no timer for " + String(act));
  }
  const p = watchPage();
  p.run("lastSessions = []"); // a session the list does not have
  p.sync();
  assert.strictEqual(p.st.fetches.length + p.st.intervals.length, 0);
});

test("it polls only while the dock shows the session's files, the page is visible and no page view is over it", () => {
  const cases = [
    ["the document is hidden", (p) => { p.st.hidden = true; }],
    ["a page view (Settings) is open over the dock", (p) => { p.st.page = "settings-modal"; }],
    ["the dock is not what shows the tree (closed, another drawer, the phone)", (p) => { p.st.owns = false; }],
    ["the dock has no session", (p) => { p.close(); p.st.owns = true; }],
  ];
  for (const [why, off] of cases) {
    const p = watchPage();
    off(p);
    p.sync();
    assert.strictEqual(p.st.fetches.length + p.st.intervals.length, 0, "does not start: " + why);
  }
});

test("it stops when any of those ends, aborts what is out and leaves no timer behind", async () => {
  const cases = [
    ["the session leaves working", (p) => p.activity("s1", "idle")],
    ["the document is hidden", (p) => { p.st.hidden = true; }],
    ["a page view opens", (p) => { p.st.page = "audit-modal"; }],
    ["the dock closes", (p) => { p.st.owns = false; p.close(); }],
  ];
  for (const [why, end] of cases) {
    const p = watchPage();
    p.sync();
    assert.strictEqual(p.liveIntervals(), 1);
    assert.strictEqual(p.pending(), 1, "a request is out");
    end(p);
    p.sync();
    assert.strictEqual(p.liveIntervals(), 0, "no interval after: " + why);
    assert.strictEqual(p.liveTimeouts(), 0, "no request timeout after: " + why);
    assert.strictEqual(p.st.fetches[0].aborted, true, "the request that was out is aborted: " + why);
    // A late reply for the poll that was stopped is nobody's: nothing is written, nothing asked.
    await settle();
    assert.strictEqual(p.st.sets.length + p.st.refreshes.length, 0, why);
    assert.strictEqual(p.st.fetches.length, 1, "and nothing more is asked: " + why);
  }
});

test("it starts again when what ended comes back, and the visibilitychange and page events are its triggers", () => {
  const p = watchPage();
  assert.strictEqual(typeof p.st.listeners.visibilitychange, "function", "a hidden page polls nothing; coming back starts it");
  p.st.hidden = true;
  p.st.listeners.visibilitychange();
  assert.strictEqual(p.st.fetches.length, 0);
  p.st.hidden = false;
  p.st.listeners.visibilitychange();
  assert.strictEqual(p.st.fetches.length, 1);
  assert.strictEqual(p.liveIntervals(), 1);
  p.activity("s1", "idle");
  p.sync();
  assert.strictEqual(p.liveIntervals(), 0);
  p.activity("s1", "working");
  p.sync();
  assert.strictEqual(p.liveIntervals(), 1, "working again: polling again");
  assert.strictEqual(p.st.fetches.length, 2);
});

test("a tick re-checks before it asks: if the world changed and nobody said so, it stops instead of asking", () => {
  const p = watchPage();
  p.sync();
  p.st.page = "settings-modal"; // opened with no sync call in between
  p.tick();
  assert.strictEqual(p.st.fetches.length, 1, "the tick asked nothing");
  assert.strictEqual(p.liveIntervals(), 0, "and the poll ended itself");
  assert.strictEqual(p.st.fetches[0].aborted, true, "dropping the request that was out");
});

test("no overlap: a tick that finds the last request still out is skipped, and the next one after the answer asks again", async () => {
  const p = watchPage();
  p.sync();
  p.tick(); p.tick(); p.tick();
  assert.strictEqual(p.st.fetches.length, 1, "three ticks while the first is out asked for nothing");
  await p.reply(body([]));
  p.tick();
  assert.strictEqual(p.st.fetches.length, 2);
});

test("a request that never answers is abandoned after 8000 ms, so the poll does not hang on it", async () => {
  const p = watchPage();
  p.sync();
  const t = p.st.timeouts.find((x) => x.live && x.ms === 8000);
  assert.ok(t, "an abort timer of 8000 ms");
  t.fn();
  await settle();
  assert.strictEqual(p.st.fetches[0].aborted, true);
  p.tick();
  assert.strictEqual(p.st.fetches.length, 2, "the slot is free again");
});

test("another session restarts the poll for it, and a reply for the old one is dropped", async () => {
  const p = watchPage();
  p.run('lastSessions = [{ id: "s1", activity: "working" }, { id: "s2", activity: "working" }]');
  p.sync();
  p.open("s2");
  p.sync();
  assert.strictEqual(p.liveIntervals(), 1, "one timer, for the new session");
  assert.strictEqual(p.st.fetches[1].url, "/api/sessions/s2/activity?limit=20");
  assert.strictEqual(p.st.fetches[0].aborted, true);
  p.st.fetches[0].done = true; // its reply, if one came late
  assert.strictEqual(p.st.sets.length, 0);
  await p.reply(body([file("/w/x.go", 5)]));
  assert.deepStrictEqual(p.st.sets.map((s) => s[0]), ["s2"], "only the shown session's list is written");
});

test("a reply that is not a good one changes nothing and leaves the slot free", async () => {
  const p = watchPage();
  p.sync();
  await p.reply({}, 500);
  p.tick();
  await p.reply({ events: [] }); // no files list
  p.tick();
  await p.reply({ files: "x" });
  p.tick();
  await p.reply(null);
  p.tick();
  assert.strictEqual(p.st.fetches.length, 5, "every tick found the slot free");
  p.st.fetches[4].reject(new Error("offline"));
  await settle();
  p.tick();
  assert.strictEqual(p.st.fetches.length, 6);
  assert.strictEqual(p.st.sets.length + p.st.refreshes.length, 0, "no list was written and no git was asked");
});

// --- what a new or changed file does ---------------------------------------------------------------------------------------

test("a new file: the page's list is set at once and git is asked once, fresh", async () => {
  const p = watchPage();
  p.sync();
  await p.reply(body([file("/w/a.go", 100)]));
  assert.deepStrictEqual(p.st.sets.map((s) => [s[0], plain(s[1])]), [["s1", [file("/w/a.go", 100)]]]);
  assert.deepStrictEqual(plain(p.st.refreshes), [{ fresh: true }], "gitStatusRefreshSoon({fresh:true}): the debounce and the server's single flight bound the runs");
  // The same answer again is no news.
  p.tick();
  await p.reply(body([file("/w/a.go", 100)]));
  assert.strictEqual(p.st.sets.length, 1);
  assert.strictEqual(p.st.refreshes.length, 1);
});

test("a changed entry (a later edit of the same file, or other counts) is news; so is another new file", async () => {
  const p = watchPage();
  p.sync();
  await p.reply(body([file("/w/a.go", 100, 1, 0)]));
  p.tick();
  await p.reply(body([file("/w/a.go", 250, 1, 0)]));
  assert.strictEqual(p.st.refreshes.length, 2, "a newer ts");
  p.tick();
  await p.reply(body([file("/w/a.go", 250, 4, 2)]));
  assert.strictEqual(p.st.refreshes.length, 3, "other counts");
  p.tick();
  await p.reply(body([file("/w/b.go", 300), file("/w/a.go", 250, 4, 2)]));
  assert.strictEqual(p.st.refreshes.length, 4, "another file, most recent first");
  assert.strictEqual(p.st.sets.length, 4);
});

test("an entry that is only reordered, or only gone, repaints the list but does not ask git", async () => {
  const p = watchPage();
  p.seed("s1", [file("/w/a.go", 1), file("/w/b.go", 2)]);
  p.sync();
  await p.reply(body([file("/w/b.go", 2), file("/w/a.go", 1)]));
  assert.strictEqual(p.st.sets.length, 1, "the order is the bars' and the Changes list's order");
  assert.strictEqual(p.st.refreshes.length, 0);
  p.tick();
  await p.reply(body([file("/w/b.go", 2)]));
  assert.strictEqual(p.st.sets.length, 2);
  assert.strictEqual(p.st.refreshes.length, 0);
});

test("the first answer is compared with what the page already holds for the session", async () => {
  const p = watchPage();
  p.seed("s1", [file("/w/a.go", 100)]);
  p.sync();
  await p.reply(body([file("/w/a.go", 100)]));
  assert.strictEqual(p.st.sets.length, 0, "the tree's own load already knew it");
  assert.strictEqual(p.st.refreshes.length, 0);
  p.tick();
  await p.reply(body([file("/w/z.go", 900), file("/w/a.go", 100)]));
  assert.strictEqual(p.st.refreshes.length, 1);
});

test("git is not asked for a file outside the tree (a box session's paths, an edit elsewhere); the list is still set", async () => {
  const p = watchPage();
  p.sync();
  await p.reply(body([file("/elsewhere/q.go", 5), file("/w2/x.go", 5), file("/work/x.go", 5), file("relative.go", 5)]));
  assert.strictEqual(p.st.sets.length, 1);
  assert.strictEqual(p.st.refreshes.length, 0, "no status of the shown tree could say anything about them");
  p.tick();
  await p.reply(body([file("/w/sub/n.go", 9), file("/elsewhere/q.go", 5), file("/w2/x.go", 5), file("/work/x.go", 5), file("relative.go", 5)]));
  assert.strictEqual(p.st.refreshes.length, 1, "one under the root is enough");
  p.st.root = "";
  p.tick();
  await p.reply(body([file("/w/sub/n.go", 11)]));
  assert.strictEqual(p.st.refreshes.length, 1, "no tree root: nothing to ask about");
});

test("a late reply of the tree's own load cannot leave the list older than the poll's: the next poll puts it right, without asking git", async () => {
  const p = watchPage();
  p.sync();
  await p.reply(body([file("/w/a.go", 100), file("/w/b.go", 50)]));
  p.seed("s1", [file("/w/b.go", 50)]); // drawerLoadChanged's older answer landed after
  p.tick();
  await p.reply(body([file("/w/a.go", 100), file("/w/b.go", 50)]));
  assert.deepStrictEqual(p.held("s1"), [file("/w/a.go", 100), file("/w/b.go", 50)]);
  assert.strictEqual(p.st.refreshes.length, 1, "the file was news once");
});

test("a bash session (no transcript) has no files: nothing is written and nothing is asked of git", async () => {
  const p = watchPage();
  p.sync();
  await p.reply({ claude: false, events: [], more: false, files: [], summary: {} });
  p.tick();
  await p.reply({ claude: false, events: [], more: false, files: [], summary: {} });
  assert.strictEqual(p.st.sets.length + p.st.refreshes.length, 0);
});

// --- subscribers -----------------------------------------------------------------------------------------------------------

test("dockWatchSubscribe: (files, newEvents) after each answer with news; the first answer is the baseline, so old events are not new", async () => {
  const p = watchPage();
  const heard = [];
  const off = p.ctx.dockWatchSubscribe("s1", (files, evs) => heard.push([plain(files), plain(evs)]));
  assert.strictEqual(typeof off, "function");
  p.sync();
  await p.reply(body([file("/w/a.go", 100)], [ev(90, "Edit", "a.go"), ev(80, "Read", "a.go")]));
  assert.deepStrictEqual(heard, [[[file("/w/a.go", 100)], []]], "the files changed against what the page held; the events were there before the watch");
  p.tick();
  await p.reply(body([file("/w/a.go", 100)], [ev(90, "Edit", "a.go"), ev(80, "Read", "a.go")]));
  assert.strictEqual(heard.length, 1, "no news, no call");
  p.tick();
  await p.reply(body([file("/w/b.go", 300), file("/w/a.go", 100)], [ev(300, "Write", "b.go"), ev(250, "Bash", "go test"), ev(90, "Edit", "a.go")]));
  assert.strictEqual(heard.length, 2);
  assert.deepStrictEqual(heard[1][0].map((f) => f.path), ["/w/b.go", "/w/a.go"]);
  assert.deepStrictEqual(heard[1][1].map((e) => e.ts), [300, 250], "the events newer than the newest seen, newest first, as Activity gives them");
  p.tick();
  await p.reply(body([file("/w/b.go", 300), file("/w/a.go", 100)], [ev(410, "Read", "b.go"), ev(300, "Write", "b.go")]));
  assert.strictEqual(heard.length, 3, "events alone are news too");
  assert.deepStrictEqual(heard[2][1].map((e) => e.ts), [410]);
  off();
  p.tick();
  await p.reply(body([file("/w/c.go", 500), file("/w/b.go", 300)], [ev(500, "Edit", "c.go")]));
  assert.strictEqual(heard.length, 3, "unsubscribed");
});

test("subscribers are per session, several may listen, and one that throws does not stop the watch or the others", async () => {
  const p = watchPage();
  const a = [];
  const b = [];
  const other = [];
  p.ctx.dockWatchSubscribe("s1", () => { throw new Error("a bad subscriber"); });
  p.ctx.dockWatchSubscribe("s1", (f) => a.push(f.length));
  const offB = p.ctx.dockWatchSubscribe("s1", (f) => b.push(f.length));
  p.ctx.dockWatchSubscribe("s2", (f) => other.push(f.length));
  p.sync();
  await p.reply(body([file("/w/a.go", 100)]));
  assert.deepStrictEqual([a, b, other], [[1], [1], []]);
  assert.strictEqual(p.st.refreshes.length, 1, "git was still asked");
  offB();
  p.tick();
  await p.reply(body([file("/w/z.go", 200), file("/w/a.go", 100)]));
  assert.deepStrictEqual([a, b], [[1, 2], [1]]);
  // A subscriber may unsubscribe itself while it is called.
  let n = 0;
  const off = p.ctx.dockWatchSubscribe("s1", () => { n++; off(); });
  p.tick();
  await p.reply(body([file("/w/y.go", 300), file("/w/z.go", 200), file("/w/a.go", 100)]));
  p.tick();
  await p.reply(body([file("/w/x.go", 400), file("/w/y.go", 300), file("/w/z.go", 200), file("/w/a.go", 100)]));
  assert.strictEqual(n, 1);
  // Not a function: a no-op handle, never a throw.
  assert.strictEqual(typeof p.ctx.dockWatchSubscribe("s1", null), "function");
});

test("subscribing does not start a poll: the watch runs on its own conditions", () => {
  const p = watchPage();
  p.activity("s1", "idle");
  p.ctx.dockWatchSubscribe("s1", () => {});
  p.sync();
  assert.strictEqual(p.st.fetches.length + p.st.intervals.length, 0);
});

test("the events already seen survive a stop for the same session (the next turn delivers what came since), and not another session", async () => {
  const p = watchPage();
  const heard = [];
  p.ctx.dockWatchSubscribe("s1", (f, e) => heard.push(plain(e).map((x) => x.ts)));
  p.sync();
  await p.reply(body([], [ev(100)]));
  p.activity("s1", "idle");
  p.sync();
  p.activity("s1", "working");
  p.sync();
  await p.reply(body([], [ev(400, "Edit"), ev(300, "you"), ev(100)]));
  assert.deepStrictEqual(heard, [[400, 300]], "the turn's first answer: everything since the last one seen");
  // Another session starts from its own baseline.
  const q = [];
  p.run('lastSessions = [{ id: "s1", activity: "working" }, { id: "s2", activity: "working" }]');
  p.ctx.dockWatchSubscribe("s2", (f, e) => q.push(plain(e).map((x) => x.ts)));
  p.open("s2");
  p.sync();
  await p.reply(body([file("/w/n.go", 1)], [ev(9000)]));
  assert.deepStrictEqual(q, [[]]);
});

// --- wiring ----------------------------------------------------------------------------------------------------------------

test("the watch is synced where the dock's state changes: every session poll while it is open, the page view and the visibility", () => {
  const read = (f) => src(f).replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  const fn = (file2, name) => {
    const s = read(file2);
    const at = s.indexOf("function " + name + "(");
    assert.ok(at >= 0, name);
    return s.slice(at, s.indexOf("\n}\n", at) + 3);
  };
  assert.match(fn("filesdrawer.js", "drawerPaint"), /dockWatchSync\(\)/);
  assert.match(fn("pagehost.js", "openPage"), /dockWatchSync\(\)/);
  assert.match(fn("pagehost.js", "pageClosed"), /dockWatchSync\(\)/);
  assert.match(fn("filesdrawer.js", "drawerLoadChanged"), /drawerSetChanged\(sid, files\)/, "the tree's own load writes the list the same way");
  const html = src("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("dockwatch.js") > at("filesdrawer.js") && at("dockwatch.js") < at("app.js"), "a DOM module: after filesdrawer.js, before app.js");
  assert.ok(at("dockwatch.js") > at("gitstatus.js") && at("dockwatch.js") > at("accessmodel.js"), "it reads what these define");
});

// --- part two: with the Changes section ------------------------------------------------------------------------------------

const KEY = "agent-box:changesScope";

function fullPage() {
  const doc = { activeElement: null, hidden: false, listeners: {} };
  doc.createElement = (t) => new Node(t, doc);
  doc.addEventListener = (ev2, fn) => { (doc.listeners[ev2] = doc.listeners[ev2] || []).push(fn); };
  const nodes = {};
  const node = (id, tag) => (nodes[id] = Object.assign(new Node(tag || "div", doc), { id }));
  for (const id of [
    "drawer", "drawer-tree", "dock-changes", "dock-chg-count", "dock-review", "dock-branch", "dock-chg-note", "dock-chg-list",
    "drawer-strip-count", "drawer-strip-changes", "dock-root", "dock-root-name", "dock-root-path", "dock-newfile", "dock-more",
    "drawer-goto-kbd", "ft-root-input", "ft-upload", "ft-menu",
  ]) node(id);
  nodes["dock-changes"].hidden = true;
  nodes["ft-root-input"].addEventListener = () => {};
  nodes["drawer"].hidden = false;
  const scope = node("dock-scope");
  for (const s of ["head", "branch"]) {
    const b = new Node("button", doc);
    b.dataset.scope = s;
    b.setAttribute("aria-pressed", s === "head" ? "true" : "false");
    scope.append(b);
  }
  node("dock-chg-legend").append(new Node("span", doc));
  doc.getElementById = (id) => nodes[id] || null;

  const st = { fetches: [], timeouts: [], intervals: [], now: 1000000, root: "/w", sid: "s1" };
  const fetch = (url, init) => new Promise((resolve, reject) => {
    const f = { url, body: init && init.body ? JSON.parse(init.body) : null, signal: init && init.signal, resolve, reject };
    if (f.signal) f.signal.addEventListener("abort", () => reject(Object.assign(new Error("aborted"), { name: "AbortError" })));
    st.fetches.push(f);
  });
  const ctx = vm.createContext({
    console, Array, Object, Set, Map, Math, Number, JSON, Promise, String, Error, AbortController, encodeURIComponent,
    Date: { now: () => st.now },
    document: doc,
    fetch,
    localStorage: { getItem: () => null, setItem: () => {} },
    setTimeout: (fn, ms) => { st.timeouts.push({ fn, ms, live: true }); return st.timeouts.length; },
    clearTimeout: (id) => { if (st.timeouts[id - 1]) st.timeouts[id - 1].live = false; },
    setInterval: (fn, ms) => { st.intervals.push({ fn, ms, live: true }); return st.intervals.length; },
    clearInterval: (id) => { if (st.intervals[id - 1]) st.intervals[id - 1].live = false; },
    shortPath: (p) => p,
    iconFor: (name, isDir) => (isDir ? "D" : "F"),
    ftRoot: () => st.root,
    drawerOwnsFileClicks: () => true,
    drawerSessionName: () => "fix-fs-cap",
    currentPage: () => "",
    shortcutHint: () => "Ctrl+P",
    IS_MAC: false,
  });
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load('let drawerState = { open: "files", sessionId: "s1", pinned: false }; let drawerChangedFiles = new Map(); let lastSessions = [{ id: "s1", activity: "working" }]; let ftRootSel = null;', "state");
  for (const f of ["accessmodel.js", "drawerfiles.js", "changesmodel.js", "gitstatus.js", "dockchanges.js"]) load(src(f), f);
  // filesdrawer.js's own: the list is set and the Changes section hears of it.
  load("function drawerSetChanged(sid, files) { drawerChangedFiles.set(sid, files); dockChangesRender(); }", "drawerSetChanged");
  load(src("dockwatch.js"), "dockwatch.js");
  const p = {
    ctx, st, nodes, run: (code) => load(code, "test"),
    calls: (url) => st.fetches.filter((f) => f.url === url),
    pending: (url) => st.fetches.filter((f) => f.url === url && !f.done),
    rows: () => nodes["dock-chg-list"].children,
    // A status the tree's own load asks for, answered now.
    status: async (data, o) => {
      const before = st.fetches.length;
      const done = ctx.gitStatusLoad(st.root, st.sid, o);
      st.fetches[before].done = true;
      st.fetches[before].resolve({ ok: true, json: async () => data });
      await done;
      await settle();
    },
    answer: async (url, data) => {
      const f = st.fetches.find((x) => x.url === url && !x.done);
      assert.ok(f, "a pending " + url);
      f.done = true;
      f.resolve({ ok: true, status: 200, json: async () => data });
      await settle();
      return f;
    },
    // The activity request the watch has out, answered.
    watchReply: async (data) => {
      const f = st.fetches.find((x) => /\/activity\?/.test(x.url) && !x.done);
      assert.ok(f, "an activity request is out");
      f.done = true;
      f.resolve({ ok: true, status: 200, json: async () => data });
      await settle();
    },
    // The debounce window of gitStatusRefreshSoon: the live 1000 ms timer fires.
    debounce: () => {
      const t = st.timeouts.filter((x) => x.live && x.ms === 1000).at(-1);
      assert.ok(t, "a 1000 ms debounce is pending");
      t.live = false;
      t.fn();
    },
  };
  return p;
}

const okStatus = (entries, extra) => Object.assign({ state: "ok", top: "/w", branch: "feat/x", head: "h1", rootRel: ".", entries }, extra || {});
const num = (entries) => ({ state: "ok", rootRel: ".", entries });
const cls = (n) => n.className.split(/\s+/);
const text = (n) => n.textContent;

test("an edit shows at once as a provisional row, git is asked fresh, and git's answer takes the row over in place", async () => {
  const p = fullPage();
  await p.status(okStatus([]), { fresh: true }); // sent at 1000000
  await p.answer("/api/git/numstat", num([]));
  assert.strictEqual(p.ctx.gitStatusAt(), 1000000);
  p.run("dockWatchSync()");
  p.st.now += 2500;
  await p.watchReply({ claude: true, events: [], more: false, summary: {}, files: [{ path: "/w/src/new.go", add: 5, del: 0, ts: p.st.now - 300 }] });

  // At once, before git has been asked: the file is a row with a muted "…", the session bar, and counts nowhere.
  assert.strictEqual(p.rows().length, 1);
  const row = p.rows()[0];
  assert.deepStrictEqual(cls(row), ["dk-row", "dk-mine"]);
  assert.deepStrictEqual(Array.from(row.children, (c) => cls(c)[0]), ["dk-ic", "dk-nm", "dk-dir", "dk-st"]);
  assert.strictEqual(text(row.querySelector(".dk-nm")), "new.go");
  assert.strictEqual(text(row.querySelector(".dk-dir")), "src");
  assert.strictEqual(text(row.querySelector(".dk-st")), "…");
  assert.deepStrictEqual(cls(row.querySelector(".dk-st")), ["dk-st", "dk-prov"]);
  assert.strictEqual(text(p.nodes["dock-chg-count"]), "1 changed");
  assert.match(row.getAttribute("aria-label"), /waiting for git/i, "the muted mark is not the only way to tell");
  assert.strictEqual(p.nodes["dock-review"].hidden, true, "Review opens a diff: there is none to read until git knows the file");

  // The debounce window passes: a fresh status is asked for, and it is the one that carries the file.
  assert.strictEqual(p.calls("/api/git/status").length, 1, "git has not been asked yet");
  p.debounce();
  assert.deepStrictEqual(p.calls("/api/git/status").map((f) => f.body), [{ path: "/w", fresh: true }, { path: "/w", fresh: true }]);
  assert.deepStrictEqual(cls(p.rows()[0].querySelector(".dk-st")), ["dk-st", "dk-prov"], "still provisional while git thinks");
  const askedAt = p.st.now;
  p.st.now += 400;
  await p.answer("/api/git/status", okStatus([{ p: "src/new.go", x: "U" }]));
  // The status is the answer to the ask made after the edit, and it lists the file: the letter replaces the "…".
  assert.strictEqual(p.ctx.gitStatusAt(), askedAt);
  assert.strictEqual(p.rows().length, 1);
  assert.strictEqual(p.rows()[0], row, "the same element: the row was updated in place");
  assert.strictEqual(text(row.querySelector(".dk-st")), "U");
  assert.deepStrictEqual(cls(row.querySelector(".dk-st")), ["dk-st", "dk-st-u"]);
  // The numstat that follows the status is fresh, as the status was: it was read after the edit too.
  const ns = p.calls("/api/git/numstat");
  assert.deepStrictEqual(ns.at(-1).body, { path: "/w", scope: "head", fresh: true });
  await p.answer("/api/git/numstat", num([{ p: "src/new.go", x: "U", a: 5, d: 0 }]));
  assert.strictEqual(p.rows()[0], row);
  assert.strictEqual(text(row.querySelector(".dk-ct")), "+5−0");
  assert.strictEqual(text(p.nodes["dock-chg-count"]), "1 changed");
  assert.strictEqual(p.nodes["dock-review"].hidden, false);
});

test("an edit git says is clean is a provisional row until a status asked after it lands, and then it is gone", async () => {
  const p = fullPage();
  await p.status(okStatus([]), { fresh: true });
  p.run("dockWatchSync()");
  p.st.now += 2000;
  await p.watchReply({ claude: true, events: [], files: [{ path: "/w/ignored.log", add: 1, del: 0, ts: p.st.now - 100 }] });
  assert.strictEqual(p.rows().length, 1);
  assert.strictEqual(text(p.nodes["dock-chg-count"]), "1 changed");
  p.debounce();
  p.st.now += 300;
  await p.answer("/api/git/status", okStatus([])); // gitignored: git lists nothing
  assert.strictEqual(p.rows().length, 0, "git says it is clean: nothing is left provisional");
  assert.strictEqual(text(p.nodes["dock-chg-note"]), "No changes");
  assert.strictEqual(text(p.nodes["dock-chg-count"]), "");
});

test("a plain reload that lands before the fresh one does not hide the edit, and one that lands after does not keep it", async () => {
  const p = fullPage();
  await p.status(okStatus([]), { fresh: true }); // at 1000000
  p.run("dockWatchSync()");
  p.st.now += 2000;
  await p.watchReply({ claude: true, events: [], files: [{ path: "/w/a.go", add: 1, del: 0, ts: p.st.now - 100 }] });
  assert.strictEqual(p.rows().length, 1);
  // A tree paint asks for a plain status meanwhile, and the server answers it from what it read before the edit.
  p.st.now += 300;
  await p.status(okStatus([]));
  assert.strictEqual(p.rows().length, 1, "the plain answer proves nothing about the edit: still provisional");
  p.debounce();
  p.st.now += 200;
  await p.answer("/api/git/status", okStatus([{ p: "a.go", x: "M" }]));
  assert.strictEqual(text(p.rows()[0].querySelector(".dk-st")), "M");
});

test("each file the agent edits in turn shows within one poll, newest first, and each is replaced by its letter when git answers", async () => {
  const p = fullPage();
  await p.status(okStatus([]), { fresh: true });
  p.run("dockWatchSync()");
  const files = [];
  for (const name of ["one.go", "two.go", "three.go"]) {
    p.st.now += 2000;
    files.unshift({ path: "/w/" + name, add: 1, del: 0, ts: p.st.now - 50 });
    p.run("dockWatchTick()");
    await p.watchReply({ claude: true, events: [], files: files.slice() });
  }
  assert.deepStrictEqual(Array.from(p.rows(), (r) => text(r.querySelector(".dk-nm"))), ["three.go", "two.go", "one.go"]);
  assert.deepStrictEqual(Array.from(p.rows(), (r) => text(r.querySelector(".dk-st"))), ["…", "…", "…"]);
  assert.strictEqual(p.calls("/api/git/status").length, 1, "the debounce has not fired: one window for the three edits");
  p.debounce();
  p.st.now += 300;
  await p.answer("/api/git/status", okStatus([{ p: "one.go", x: "M" }, { p: "two.go", x: "M" }, { p: "three.go", x: "A" }]));
  assert.deepStrictEqual(Array.from(p.rows(), (r) => text(r.querySelector(".dk-st"))), ["A", "M", "M"]);
  assert.strictEqual(p.rows().length, 3);
});

test("the poll leaves no timer behind when the session finishes its turn", async () => {
  const p = fullPage();
  await p.status(okStatus([]), { fresh: true });
  p.run("dockWatchSync()");
  assert.strictEqual(p.st.intervals.filter((x) => x.live).length, 1);
  p.run('lastSessions = [{ id: "s1", activity: "done" }]; dockWatchSync()');
  assert.strictEqual(p.st.intervals.filter((x) => x.live).length, 0);
  assert.deepStrictEqual(p.st.timeouts.filter((x) => x.live && x.ms === 8000), [], "and no request timer");
});
