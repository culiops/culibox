"use strict";
// Source-level pins for where the shared pending helpers (pending.js) are
// applied: Credentials load, file-tree directory load, Restart and + Shell.
// The DOM half needs a browser, so these pin the wiring the way
// pagehost.test.js does; pending.test.js covers the helpers' behaviour.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function body(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

// The text of the first `finally { ... }` block in src at or after `from`.
function finallyBlock(src, from) {
  const i = src.indexOf("finally", from);
  assert.ok(i >= 0, "no finally block");
  const open = src.indexOf("{", i);
  let depth = 0;
  for (let j = open; j < src.length; j++) {
    if (src[j] === "{") depth++;
    else if (src[j] === "}" && --depth === 0) return src.slice(open, j + 1);
  }
  throw new Error("unbalanced finally");
}

test("loadCredentials shows a pending region before refreshStore and clears it in finally", () => {
  const b = body(read("credentials.js"), "loadCredentials");
  const start = b.indexOf('pendingRegion(document.getElementById("creds-body"), "Loading credentials…")');
  assert.ok(start >= 0, "pendingRegion on #creds-body");
  assert.ok(start < b.indexOf("refreshStore()"), "started before refreshStore");
  assert.match(finallyBlock(b, start), /done\(\)/);
  assert.ok(b.indexOf("renderCredentials(view)") > start, "still renders after the load");
});

test("ftRenderDir shows a pending region around the list call and clears it before appending", () => {
  const b = body(read("filetree.js"), "ftRenderDir");
  const start = b.indexOf('pendingRegion(container, "Loading…")');
  assert.ok(start >= 0, "pendingRegion on the container");
  assert.ok(start < b.indexOf("/api/fs/list"), "started before the list call");
  const fin = finallyBlock(b, start);
  assert.match(fin, /done\(\)/);
  // The error row is appended after the line is gone, not before.
  assert.ok(b.indexOf(fin) < b.indexOf('"ft-err"'), "done() runs before the error row");
});

test("ftLoadRoot shows its pending line in the live tree, not the off-DOM box", () => {
  const b = body(read("filetree.js"), "ftLoadRoot");
  const start = b.indexOf('pendingRegion(ftTreeEl, "Loading…")');
  assert.ok(start >= 0, "pendingRegion on ftTreeEl");
  assert.match(finallyBlock(b, start), /done\(\)/);
});

test("restartSession: a trigger gets withPending, no trigger gets a toast cleared in finally", () => {
  const b = body(read("app.js"), "restartSession");
  assert.match(read("app.js"), /async function restartSession\(s, trigger\)/);
  assert.match(b, /withPending\(trigger, "Restarting…"/);
  const start = b.indexOf('pendingToast("Restarting " + (s.name || s.id) + "…")');
  assert.ok(start >= 0, "toast for the trigger-less path");
  assert.match(finallyBlock(b, start), /done\(\)/);
});

test("spawnShellIn: a trigger gets withPending, no trigger gets a toast over the shell call only", () => {
  const src = read("app.js");
  assert.match(src, /async function spawnShellIn\(cell, placement = "below", trigger, label = "Starting shell…"\)/);
  assert.match(body(src, "spawnShellIn"), /withPending\(trigger, label,/);
  const run = body(src, "spawnShellRun");
  const start = run.indexOf('pendingToast("Starting shell…")');
  assert.ok(start >= 0, "toast helper");
  assert.ok(start > run.indexOf("openSpawnDialog"), "after the form fallback branch");
  assert.ok(start < run.indexOf('"/shell"'), "started before the shell call");
  const fin = finallyBlock(run, start);
  assert.match(fin, /done\(\)/);
  // The toast covers the one call: it is cleared before refreshSessions.
  assert.ok(run.indexOf(fin) < run.indexOf("await refreshSessions()"));
});

test("killSession: a toast covers the DELETE, cleared in finally before the error toast", () => {
  const src = read("app.js");
  const b = body(src, "killSession");
  const start = b.indexOf('pendingToast("Ending " + ');
  assert.ok(start >= 0, "toast helper with an Ending… label");
  assert.ok(start < b.indexOf('method: "DELETE"'), "started before the DELETE");
  // The 300ms clock starts at the click, not after the row re-render.
  assert.ok(start < b.indexOf("await refreshSessions()"), "started before the first refreshSessions");
  assert.ok(b.indexOf("lastSessions") < start, "name lookup precedes the toast");
  assert.ok(b.indexOf("killing.add(id)") < start, "after the early return and killing.add");
  // The first refresh sits inside the try, so a throw still reaches finally.
  const tryAt = b.indexOf("try {", start);
  assert.ok(tryAt >= 0 && tryAt < b.indexOf("await refreshSessions()"), "refresh inside try");
  const fin = finallyBlock(b, start);
  assert.match(fin, /done\(\)/);
  // The error toast must come after done(), so done() cannot hide it.
  assert.ok(b.indexOf(fin) < b.indexOf('"End failed: "'), "done() runs before the error toast");
  assert.ok(b.indexOf('"End failed: "') > 0, "error toast still present");
});

test("trigger buttons hand themselves to the action instead of a disabled/finally guard", () => {
  const term = read("terminal-tab.js");
  assert.match(term, /restartSession\(\{ id: this\.id \}, rb\)/);
  assert.ok(!/rb\.disabled = (true|false)/.test(term), "no ad-hoc guard on the restart button");
  const tabs = read("tabs.js");
  // The icon-sized buttons (dock "+" in an overflow:hidden strip, flex:none
  // "+sh") pass a short label so they do not grow into a sentence; "+ Shell"
  // keeps the default "Starting shell…".
  assert.match(tabs, /spawnShellIn\(this, this\.dock \? this\.dock\.placement : "below", add, "…"\)/);
  assert.match(tabs, /spawnShellIn\(this, undefined, sh, "…"\)/);
  assert.match(tabs, /spawnShellIn\(this, this\.dock \? this\.dock\.placement : "below", sh\)/);
  assert.ok(!/(add|sh)\.disabled = (true|false)/.test(tabs), "no ad-hoc guards on the + Shell buttons");
});

test("the tree's pending line takes the tree font and indents like a child row", () => {
  const css = read("app.css");
  assert.match(css, /#filetree \.pending-line\s*\{[^}]*font-size:\s*inherit/);
  assert.match(css, /#filetree \.pending-line\s*\{[^}]*--ft-indent/);
  assert.match(body(read("filetree.js"), "ftRenderDir"), /--ft-indent/);
});
