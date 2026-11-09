"use strict";
// activitytab.js: the view's lifecycle against a minimal fake DOM (the repo has no
// jsdom). Covers what the pure model cannot: a view re-targeted to another session
// must not keep the previous session's reply box.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");

const { Node, Text } = require("./fakedom.js");

// One page: scripts run as classic scripts in a shared global scope, like the browser.
function page() {
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  const box = {
    document: doc, window: { getSelection: () => null }, console, AbortController, Promise, JSON, Date,
    lastSessions: [], paneViewRenderers: {}, replyFocused: false, replyDraft: null, repliesEnabled: true,
    composers: [],
    fetch: async () => ({ status: 200, json: async () => ({ claude: true, events: [], more: false, files: [], summary: {} }) }),
    api: async () => ({ tools: [] }),
    terminalTabFor: () => null,
    maskHint: () => "",
    // Stand-ins for app.js: the composer is bound to the session object it was built for.
    buildCardComposer(s) {
      const form = doc.createElement("div");
      form.appendChild(doc.createElement("input"));
      const c = { form, why: doc.createElement("span"), refresh() {}, sid: s.id };
      box.composers.push(c);
      return c;
    },
    buildEnableRepliesButton() { return doc.createElement("button"); },
  };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["attribution.js", "duration.js", "activitymodel.js", "activitytab.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  return { box, doc, host: doc.createElement("div") };
}
const tick = () => new Promise((r) => setImmediate(r));
const claude = (id, over) => Object.assign({ id, claude: true, summary: "said " + id, replyable: true, activity: "idle" }, over);

test("a mounted Claude view shows the latest message and a reply box", async () => {
  const { box, host } = page();
  box.lastSessions = [claude("s1")];
  box.paneViewRenderers.activity(host, "s1");
  await tick();
  assert.match(host.textContent, /Latest message/);
  assert.match(host.textContent, /said s1/);
  assert.strictEqual(host.querySelectorAll("input").length, 1);
  assert.strictEqual(box.composers.length, 1);
});

test("re-targeted to a non-Claude session: no stale message, reply box or filter bar", async () => {
  const { box, host } = page();
  box.lastSessions = [claude("s1"), { id: "s2", claude: false }];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  assert.strictEqual(host.querySelectorAll("input").length, 1);
  h.update("s2");
  await tick();
  assert.strictEqual(host.querySelectorAll("input").length, 0, "the old session's reply box is gone");
  assert.doesNotMatch(host.textContent, /Latest message|said s1/);
  assert.strictEqual(host.querySelectorAll(".act-pill").length, 0);
  assert.match(host.textContent, /Activity is available for Claude sessions/);
  // And back: a fresh box, built for the session now shown.
  h.update("s1");
  await tick();
  assert.strictEqual(host.querySelectorAll("input").length, 1);
  assert.strictEqual(box.composers[box.composers.length - 1].sid, "s1");
});

test("re-targeted to another Claude session: one reply box, bound to the new session", async () => {
  const { box, host } = page();
  box.lastSessions = [claude("s1"), claude("s2")];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  h.update("s2");
  await tick();
  assert.strictEqual(host.querySelectorAll("input").length, 1);
  assert.strictEqual(box.composers[box.composers.length - 1].sid, "s2");
  assert.match(host.textContent, /said s2/);
  assert.doesNotMatch(host.textContent, /said s1/);
});

test("a session that leaves the list takes its reply box with it", async () => {
  const { box, host } = page();
  box.lastSessions = [claude("s1")];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  box.lastSessions = [claude("zz")];
  h.update("s1");
  assert.strictEqual(host.querySelectorAll("input").length, 0);
  assert.match(host.textContent, /no longer listed/);
});

test("removing a focused reply box releases the poll's reply-focus hold", async () => {
  const { box, doc, host } = page();
  box.lastSessions = [claude("s1"), { id: "s2", claude: false }];
  const h = box.paneViewRenderers.activity(host, "s1");
  await tick();
  host.querySelector("input").focus();
  box.replyFocused = true;
  h.update("s2");
  assert.strictEqual(box.replyFocused, false);
  assert.ok(doc.activeElement);
});

test("a diff's +a −d is neutral text: green and red are status and error roles only", () => {
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  for (const cls of ["act-add", "act-del"]) {
    const rules = css.split("}").filter((r) => new RegExp("\\." + cls + "\\b").test(r.split("{")[0]));
    assert.ok(rules.length, cls + " is styled");
    for (const r of rules) assert.doesNotMatch(r, /var\(--(ok|danger|amber)\)/, cls);
  }
});

// .act-out sets the muted default; a tone class that does not out-rank it (equal
// specificity, declared earlier) silently paints "error · exit 2" grey. Every tone
// the outcome cell can carry must be compounded with .act-out.
test("outcome tone colours out-rank the muted .act-out default", () => {
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rules = css.split("}").map((r) => r.split("{")).filter((r) => r.length === 2);
  const colour = (sel) => {
    const hit = rules.filter(([s]) => s.split(",").map((x) => x.trim()).includes(sel));
    assert.ok(hit.length, sel + " is styled");
    return hit.map(([, b]) => (b.match(/color:\s*var\(--(\w+)\)/) || [])[1]).pop();
  };
  assert.strictEqual(colour(".act-out"), "muted");
  assert.strictEqual(colour(".act-out.act-err"), "danger");
  assert.strictEqual(colour(".act-out.act-ok"), "ok");
  assert.strictEqual(colour(".act-out.act-amber"), "amber");
  assert.strictEqual(colour(".act-out.act-muted"), "muted");
});

test("the New session dialog fits the viewport and keeps its action row pinned", () => {
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#spawn-modal\s*\{[^}]*max-height:\s*calc\(100vh - 32px\)/);
  // Pinned by being outside the form's scroll box (frame M4.1, #173): out of flow on the card's bottom edge, with the
  // body (a fixed min(640px, ...) tall) keeping its band free. It was sticky inside the form before.
  assert.match(css, /#spawn \.spawn-actions\s*\{[^}]*position:\s*absolute;[^}]*bottom:\s*0/);
  assert.match(css, /#spawn-modal \.dlg-body\s*\{[^}]*height:\s*min\(640px,[^}]*margin-bottom:\s*var\(--ns-foot\)/);
});
