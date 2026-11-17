"use strict";
// The Full-mode agent strip's poll (dockagent.js), run for real in a vm against a fake page, fake timers and a fake fetch:
// when it polls (Full mode on, the dock on a session, the page visible), that it never overlaps itself, that it stops and
// restarts with the session, and that a reply for a poll that is over is dropped. dock-layout-css.test.js pins the markup and
// the stylesheet; the visual check proves the strip on screen.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function el(id) { return { id, textContent: "", className: "", title: "", dataset: {}, hidden: false }; }

// A page with the strip's elements and the dock's state. `run` evaluates in the page's scope.
function page(over) {
  const els = Object.fromEntries(["drawer", "dock-agent-dot", "dock-agent-name", "dock-agent-st", "dock-agent-last", "dock-agent-term", "dock-agent-kbd"].map((id) => [id, el(id)]));
  els.drawer.dataset.max = "1";
  const st = { fetches: [], intervals: [], listeners: {}, hidden: false };
  const document = {
    get hidden() { return st.hidden; },
    getElementById: (id) => els[id] || null,
    addEventListener: (ev, fn) => { st.listeners[ev] = fn; },
  };
  const ctx = vm.createContext(Object.assign({
    console, Object, Array, Math, JSON, Promise, AbortController, encodeURIComponent,
    document,
    setInterval: (fn, ms) => { st.intervals.push({ fn, ms, live: true }); return st.intervals.length; },
    clearInterval: (id) => { if (st.intervals[id - 1]) st.intervals[id - 1].live = false; },
    setTimeout: () => 0, // the request's own timeout: never fires here (a hung request is its own test)
    clearTimeout: () => {},
    fetch: (url, o) => new Promise((resolve, reject) => {
      const f = { url, signal: o && o.signal, aborted: false, resolve, reject };
      if (f.signal) f.signal.addEventListener("abort", () => { f.aborted = true; reject(Object.assign(new Error("aborted"), { name: "AbortError" })); });
      st.fetches.push(f);
    }),
    deskDot: (s) => ({ cls: "dot " + (s.activity || "unknown"), title: s.activity === "working" ? "working" : "" }),
    drawerSessionName: (id) => ({ a: "alpha", b: "beta" }[id] || ""),
    shortcutHint: (id) => (id === "dock.showTerminal" ? "Ctrl+`" : ""),
    IS_MAC: false,
    lastSessions: [{ id: "a", activity: "working" }, { id: "b", activity: "idle" }],
  }, over));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(src("accessmodel.js"), "accessmodel.js"); // accReadReply, the real one
  load('let drawerState = { open: "files", sessionId: "a", pinned: false };', "state");
  load(src("dockagent.js"), "dockagent.js");
  const p = {
    ctx, els, st, run: (code) => load(code, "test"),
    open: (sid) => p.run("drawerState = " + JSON.stringify({ open: "files", sessionId: sid, pinned: false })),
    // Answer the oldest unanswered request with a 200 and `body`, and let the poll's continuation run.
    reply: async (body, i) => {
      const f = st.fetches.filter((x) => !x.done && !x.aborted)[i || 0];
      f.done = true;
      f.resolve({ status: 200, json: async () => body });
      await new Promise((r) => setImmediate(r));
    },
    tick: () => { const iv = st.intervals.filter((x) => x.live).at(-1); iv.fn(); },
    line: () => els["dock-agent-last"].textContent,
    live: () => st.intervals.filter((x) => x.live).length,
  };
  return p;
}
const ev = (tool, detail, extra) => Object.assign({ ts: 1, kind: "tool", tool, detail, state: "ok" }, extra);

// --- the line ----------------------------------------------------------------------------------------------------------

test("dockAgentLine: a tool call reads Tool(detail); the newest event is the one shown", () => {
  const p = page();
  const f = (events) => p.ctx.dockAgentLine(events);
  assert.equal(f([ev("Update", "internal/web/fsread_cap_test.go"), ev("Read", "older")]), "Update(internal/web/fsread_cap_test.go)");
  assert.equal(f([ev("Bash", "go test ./...")]), "Bash(go test ./...)");
  assert.equal(f([ev("Bash", "line one\n   line  two\t\tthree")]), "Bash(line one line two three)", "one line: whitespace runs are one space");
  assert.equal(f([ev("Grep", "")]), "Grep", "no detail, no empty parentheses");
  assert.equal(f([ev("", "x")]), "Tool(x)");
  assert.equal(f([ev("  ", "x")]), "Tool(x)");
});

test("dockAgentLine: your message, a clear and an audit row read as their label and detail; an unknown kind is its detail", () => {
  const f = (e) => page().ctx.dockAgentLine([e]);
  assert.equal(f({ kind: "you", detail: "fix the cap" }), "You: fix the cap");
  assert.equal(f({ kind: "clear", detail: "" }), "Cleared");
  assert.equal(f({ kind: "credential", detail: "aws read granted" }), "Credential: aws read granted");
  assert.equal(f({ kind: "approval", detail: "write 15m" }), "Approval: write 15m");
  assert.equal(f({ kind: "future-kind", detail: "something" }), "something");
  assert.equal(f({ kind: "__proto__", detail: "x" }), "x", "an inherited key is not a label");
  assert.equal(f({ kind: "constructor", detail: "x" }), "x");
});

test("dockAgentLine: nothing true to say is the empty string, never \"undefined\" or \"null\"", () => {
  const f = (v) => page().ctx.dockAgentLine(v);
  for (const v of [undefined, null, [], {}, "x", 5, [null], [undefined], ["x"], [5], [{}], [{ kind: "tool" }], [{ kind: "tool", detail: 7, tool: 3 }]]) {
    const out = f(v);
    assert.ok(!/undefined|null|NaN|\[object/.test(out), JSON.stringify(v) + " gave " + JSON.stringify(out));
  }
  assert.equal(f([]), "");
  assert.equal(f(undefined), "");
  assert.equal(f([{ kind: "tool", detail: 7, tool: 3 }]), "Tool", "a field of the wrong type is no field");
});

test("dockAgentLine cuts a long detail to the server's own 300 (a stranger's reply cannot fill the page)", () => {
  const out = page().ctx.dockAgentLine([ev("Bash", "x".repeat(5000))]);
  assert.equal(out, "Bash(" + "x".repeat(300) + ")");
});

// --- the poll ----------------------------------------------------------------------------------------------------------

test("it polls the dock's session at once and then every 4000 ms, through the Activity endpoint, one event", async () => {
  const p = page();
  p.run("dockAgentSync()");
  assert.equal(p.st.fetches.length, 1, "the first ask is immediate");
  assert.equal(p.st.fetches[0].url, "/api/sessions/a/activity?limit=1");
  assert.equal(p.st.intervals.length, 1);
  assert.equal(p.st.intervals[0].ms, 4000);
  await p.reply({ events: [ev("Update", "a.go")] });
  assert.equal(p.line(), "Update(a.go)");
  p.tick();
  assert.equal(p.st.fetches.length, 2);
  await p.reply({ events: [ev("Read", "b.go")] });
  assert.equal(p.line(), "Read(b.go)");
});

test("a session id is encoded into the path", () => {
  const p = page();
  p.open("a/b c?");
  p.run("dockAgentSync()");
  assert.equal(p.st.fetches[0].url, "/api/sessions/a%2Fb%20c%3F/activity?limit=1");
});

test("no overlap: a tick that finds the last request still out is skipped, and the next one after the answer asks again", async () => {
  const p = page();
  p.run("dockAgentSync()");
  p.tick(); p.tick(); p.tick();
  assert.equal(p.st.fetches.length, 1, "three ticks while the first is out asked for nothing");
  await p.reply({ events: [] });
  p.tick();
  assert.equal(p.st.fetches.length, 2);
});

test("a failed or empty reply leaves the line as it was and frees the slot for the next tick", async () => {
  const p = page();
  p.run("dockAgentSync()");
  await p.reply({ events: [ev("Edit", "x.go")] });
  assert.equal(p.line(), "Edit(x.go)");
  p.tick();
  p.st.fetches[1].done = true;
  p.st.fetches[1].reject(new Error("offline"));
  await new Promise((r) => setImmediate(r));
  assert.equal(p.line(), "Edit(x.go)", "offline: the last line stays");
  p.tick();
  assert.equal(p.st.fetches.length, 3, "and the next tick asks again");
  p.st.fetches[2].done = true;
  p.st.fetches[2].resolve({ status: 500, json: async () => ({}) });
  await new Promise((r) => setImmediate(r));
  assert.equal(p.line(), "Edit(x.go)", "a 500 is no answer");
  p.tick();
  p.st.fetches[3].done = true;
  p.st.fetches[3].resolve({ status: 204, json: async () => null });
  await new Promise((r) => setImmediate(r));
  assert.equal(p.line(), "Edit(x.go)", "a 204 says nothing either");
  p.tick();
  await p.reply({ events: [] });
  assert.equal(p.line(), "", "a real answer of no events is an empty line");
});

test("a bash session with no transcript: the strip degrades to an empty line, with no text of 'undefined'", async () => {
  const p = page();
  p.run("dockAgentSync()");
  await p.reply({ claude: false, events: [], files: [] });
  assert.equal(p.line(), "");
  for (const id of ["dock-agent-dot", "dock-agent-name", "dock-agent-st", "dock-agent-last"]) {
    assert.ok(!/undefined|null/.test(p.els[id].textContent + p.els[id].className), id);
  }
});

test("Full mode off, or the dock closed, or no session: nothing is polled", () => {
  const off = page();
  delete off.els.drawer.dataset.max;
  off.run("dockAgentSync()");
  assert.equal(off.st.fetches.length, 0);
  assert.equal(off.st.intervals.length, 0);
  const closed = page();
  closed.run('drawerState = { open: null, sessionId: "a", pinned: false }; dockAgentSync()');
  assert.equal(closed.st.fetches.length, 0);
  const hiddenDock = page();
  hiddenDock.els.drawer.hidden = true;
  hiddenDock.run("dockAgentSync()");
  assert.equal(hiddenDock.st.fetches.length, 0);
  const none = page();
  none.open(null);
  none.run("dockAgentSync()");
  assert.equal(none.st.fetches.length, 0, "a dock with no session has nobody to ask about");
});

test("leaving Full mode stops the poll, aborts the request that is out, and drops its late answer", async () => {
  const p = page();
  p.run("dockAgentSync()");
  await p.reply({ events: [ev("Edit", "kept.go")] });
  p.tick();
  const out = p.st.fetches[1];
  delete p.els.drawer.dataset.max;
  p.run("dockAgentSync()");
  assert.equal(p.live(), 0, "the interval is cleared");
  assert.equal(out.aborted, true, "and the request in flight is aborted");
  await new Promise((r) => setImmediate(r));
  assert.equal(p.line(), "Edit(kept.go)", "an aborted request changes nothing");
  p.st.fetches.length = 0;
  p.run("dockAgentSync()"); // still not Full mode
  assert.equal(p.st.fetches.length, 0);
});

test("an answer that lands after a stop and a new start is not drawn: it was for a poll that is over", async () => {
  const p = page();
  p.run("dockAgentSync()");
  const old = p.st.fetches[0];
  delete p.els.drawer.dataset.max;
  p.run("dockAgentSync()");
  p.els.drawer.dataset.max = "1";
  p.run("dockAgentSync()"); // Full mode again, same session
  assert.equal(p.st.fetches.length, 2, "a new poll, a new request");
  old.resolve({ status: 200, json: async () => ({ events: [ev("Edit", "stale.go")] }) }); // too late: aborted, but suppose it slipped through
  await new Promise((r) => setImmediate(r));
  assert.equal(p.line(), "", "nothing of the old poll");
  await p.reply({ events: [ev("Edit", "fresh.go")] });
  assert.equal(p.line(), "Edit(fresh.go)");
});

test("the dock moving to another session restarts the poll for it, clears the old line at once, and one session's line is never read under another's name", async () => {
  const p = page();
  p.run("dockAgentSync()");
  await p.reply({ events: [ev("Edit", "alpha.go")] });
  assert.equal(p.line(), "Edit(alpha.go)");
  p.tick();
  const old = p.st.fetches[1]; // a request for a is out when the dock moves
  p.open("b");
  p.run("dockAgentSync()");
  assert.equal(old.aborted, true);
  assert.equal(p.line(), "", "alpha's last action is gone before beta's name is on screen");
  assert.equal(p.els["dock-agent-name"].textContent, "beta");
  assert.equal(p.st.fetches.at(-1).url, "/api/sessions/b/activity?limit=1");
  assert.equal(p.live(), 1, "one interval, not two");
  assert.equal(p.st.intervals.filter((x) => x.live)[0].ms, 4000);
  await p.reply({ events: [ev("Read", "beta.go")] });
  assert.equal(p.line(), "Read(beta.go)");
});

test("the same session asked again (every session poll repaints the dock) changes nothing: no second interval, no extra request", async () => {
  const p = page();
  p.run("dockAgentSync()");
  await p.reply({ events: [ev("Edit", "x.go")] });
  const n = p.st.fetches.length;
  for (let i = 0; i < 5; i++) p.run("dockAgentSync()");
  assert.equal(p.st.fetches.length, n);
  assert.equal(p.st.intervals.length, 1);
});

test("a hidden page polls nothing, and coming back starts again at once, only if Full mode is still on", async () => {
  const p = page();
  assert.equal(typeof p.st.listeners.visibilitychange, "function", "the module listens for the page being hidden and shown");
  p.run("dockAgentSync()");
  await p.reply({ events: [] });
  p.st.hidden = true;
  p.st.listeners.visibilitychange();
  assert.equal(p.live(), 0);
  const n = p.st.fetches.length;
  p.st.hidden = false;
  p.st.listeners.visibilitychange();
  assert.equal(p.st.fetches.length, n + 1, "visible again: an immediate request");
  assert.equal(p.live(), 1);
  await p.reply({ events: [] });
  // Hidden, Full mode ended meanwhile, visible again: nothing starts.
  p.st.hidden = true;
  p.st.listeners.visibilitychange();
  delete p.els.drawer.dataset.max;
  p.st.hidden = false;
  p.st.listeners.visibilitychange();
  assert.equal(p.live(), 0);
  assert.equal(p.st.fetches.length, n + 1);
});

test("a request that never answers cannot hold the one slot for good: its own timeout aborts it", () => {
  const timers = [];
  const p = page({ setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; } });
  p.run("dockAgentSync()");
  assert.equal(timers.length, 1);
  assert.equal(timers[0].ms, 8000);
  timers[0].fn();
  assert.equal(p.st.fetches[0].aborted, true);
});

// --- the static parts --------------------------------------------------------------------------------------------------

test("the strip draws the session's dot, name and status words from the same helpers as the sidebar row", () => {
  const p = page();
  p.run("dockAgentSync()");
  assert.equal(p.els["dock-agent-dot"].className, "dot working");
  assert.equal(p.els["dock-agent-name"].textContent, "alpha");
  assert.equal(p.els["dock-agent-st"].textContent, "working");
  p.open("b");
  p.run("dockAgentSync()");
  assert.equal(p.els["dock-agent-dot"].className, "dot idle");
  assert.equal(p.els["dock-agent-name"].textContent, "beta");
  assert.equal(p.els["dock-agent-st"].textContent, "", "a session whose activity has no words shows none (and the strip hides the gap)");
  // The list moves while the strip is open (a poll): the dot follows.
  p.ctx.lastSessions = [{ id: "a", activity: "working" }, { id: "b", activity: "working" }];
  p.run("dockAgentSync()");
  assert.equal(p.els["dock-agent-dot"].className, "dot working");
  assert.equal(p.els["dock-agent-st"].textContent, "working");
});

test("a session the list does not know yet has its name and a plain dot, never 'undefined'", () => {
  const p = page();
  p.ctx.lastSessions = [];
  p.run("dockAgentSync()");
  assert.equal(p.els["dock-agent-dot"].className, "dot");
  assert.equal(p.els["dock-agent-name"].textContent, "alpha");
  assert.equal(p.els["dock-agent-st"].textContent, "");
});

test("the event text is put on the page as text: a name or detail made of markup is just characters", async () => {
  const p = page();
  p.run("dockAgentSync()");
  await p.reply({ events: [ev("Edit", "<img src=x onerror=alert(1)>.md")] });
  assert.equal(p.line(), "Edit(<img src=x onerror=alert(1)>.md)");
  const code = src("dockagent.js").replace(/\/\/.*$/gm, "");
  assert.ok(!/innerHTML|outerHTML|insertAdjacentHTML|document\.write/.test(code), "only textContent");
});

test("Show terminal carries its chord from the shortcut registry", () => {
  const p = page();
  assert.equal(p.els["dock-agent-kbd"].textContent, "Ctrl+`");
  assert.equal(p.els["dock-agent-term"].title, "Show the terminal (Ctrl+`)");
  const none = page({ shortcutHint: () => "" });
  assert.equal(none.els["dock-agent-kbd"].textContent, "");
  assert.equal(none.els["dock-agent-term"].title, "Show the terminal");
});

test("dockAgentStart and dockAgentStop are the strip's two entry points; Start with no session is a stop", () => {
  const p = page();
  p.run('dockAgentStart("a")');
  assert.equal(p.live(), 1);
  p.run("dockAgentStart()");
  assert.equal(p.live(), 0);
  p.run('dockAgentStart("a"); dockAgentStop()');
  assert.equal(p.live(), 0);
  assert.equal(p.st.fetches.at(-1).aborted, true);
});

test("a reply that outlived its poll is dropped even when the fetch did not honour the abort (the body was already on its way)", async () => {
  const slow = [];
  const p = page({
    fetch: (url) => new Promise((resolve) => { slow.push({ url, resolve }); }), // ignores the signal: an abort cannot reject it
  });
  p.run("dockAgentSync()");
  p.open("b");
  p.run("dockAgentSync()"); // the dock moved: the poll for a is over, a new one for b is out
  assert.equal(slow.length, 2);
  slow[0].resolve({ status: 200, json: async () => ({ events: [ev("Edit", "alpha-late.go")] }) });
  await new Promise((r) => setImmediate(r));
  assert.equal(p.line(), "", "alpha's answer is not drawn under beta's name");
  slow[1].resolve({ status: 200, json: async () => ({ events: [ev("Read", "beta.go")] }) });
  await new Promise((r) => setImmediate(r));
  assert.equal(p.line(), "Read(beta.go)");
  // And the in-flight slot is free again for beta's next tick, not held by the dropped request.
  p.tick();
  assert.equal(slow.length, 3);
});
