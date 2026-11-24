"use strict";
// Plan 2 (#172) task 7, the desktop ask strip: F50 (replies off says why there is
// no Yes/No), F51 (the reason Yes/No are disabled is a line on screen, and an
// expanded ask keeps the command on line 1), F65 (a reply shows Sending…, then
// Sent for 2 s under the field instead of a toast) and F63 (the sign-in line a pane
// keeps). app.js and tabs.js are classic scripts that cannot be required, so the
// functions are cut out by name and run in vm against a small fake DOM (the repo
// has no jsdom); the pure decisions are in attention.test.js.
const { test, afterEach } = require("node:test");
const assert = require("node:assert");
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { iconStubs } = require("./iconpage.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const sha = (s) => crypto.createHash("sha256").update(s).digest("hex");

function fnSrc(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}
// A top-level `let|const name ... ;` line (one line), for module state the cut functions read.
function declSrc(src, name) {
  const m = new RegExp("^(let|const) " + name + "\\b.*;$", "m").exec(src);
  assert.ok(m, "declaration of " + name + " not found");
  return m[0];
}

// --- a fake DOM: what renderNeedsBody and the composer touch -------------------------
class Node {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.own = "";
    this.attrs = {}; this.dataset = {}; this.title = ""; this.disabled = false; this.isConnected = true; this.value = "";
    this.cls = new Set();
    const self = this;
    this.classList = {
      add: (c) => self.cls.add(c),
      remove: (c) => self.cls.delete(c),
      contains: (c) => self.cls.has(c),
      toggle: (c, on) => { if (on === undefined ? !self.cls.has(c) : on) self.cls.add(c); else self.cls.delete(c); },
    };
  }
  get className() { return [...this.cls].join(" "); }
  set className(v) { this.cls = new Set(String(v).split(/\s+/).filter(Boolean)); }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  appendChild(c) { if (c.parent) c.remove(); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => (typeof c === "string" ? this.appendChild(Object.assign(new Node("#text"), { own: c })) : this.appendChild(c))); }
  remove() { if (this.parent) { this.parent.children.splice(this.parent.children.indexOf(this), 1); this.parent = null; } }
  after(n) { const p = this.parent; if (n.parent) n.remove(); n.parent = p; p.children.splice(p.children.indexOf(this) + 1, 0, n); }
  get nextElementSibling() { return this.parent ? this.parent.children[this.parent.children.indexOf(this) + 1] || null : null; }
  all(pred, out = []) { for (const c of this.children) { if (pred(c)) out.push(c); c.all(pred, out); } return out; }
  byClass(c) { return this.all((n) => n.cls.has(c)); }
  buttons() { return this.all((n) => n.tag === "button"); }
  focus() {}
}

// A fake clock, so the 300 ms pending delay and the 2 s "Sent" line can be stepped.
function clockFor() {
  const clock = { now: 1000, timers: [], seq: 0 };
  clock.setTimeout = (fn, ms) => { const id = ++clock.seq; clock.timers.push({ id, at: clock.now + (ms || 0), fn }); return id; };
  clock.clearTimeout = (id) => { clock.timers = clock.timers.filter((t) => t.id !== id); };
  clock.tick = (ms) => {
    const end = clock.now + ms;
    for (;;) {
      const due = clock.timers.filter((t) => t.at <= end).sort((a, b) => a.at - b.at || a.id - b.id)[0];
      if (!due) break;
      clock.timers = clock.timers.filter((t) => t !== due);
      clock.now = due.at;
      due.fn();
    }
    clock.now = end;
  };
  return clock;
}

const att = require("../static/attention.js");
const peek = require("../static/peek.js");
const desk = require("../static/deskshell.js");

// page() sets the real global (attention.js and peek.js run in Node, not in the sandbox): put it back
// after every test, so no test depends on the one before it.
const HAD_REPLIES = Object.prototype.hasOwnProperty.call(globalThis, "repliesEnabled");
const REPLIES_WAS = globalThis.repliesEnabled;
afterEach(() => { if (HAD_REPLIES) globalThis.repliesEnabled = REPLIES_WAS; else delete globalThis.repliesEnabled; });

// One page: the functions under test plus the page state and stand-ins they read.
function page(over) {
  const clock = clockFor();
  const toasts = [];
  const appSrc = read("app.js");
  const posts = [];
  const box = {
    document: { createElement: (t) => new Node(t), createTextNode: (t) => Object.assign(new Node("#text"), { own: t }) },
    window: { innerWidth: 1440 }, console, Promise, JSON, Date: { now: () => clock.now }, Set, Map, String, Array, Number, Object,
    setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout,
    showToast: (m, o) => toasts.push({ m, o }),
    cardContent: att.cardContent, answerState: att.answerState, maskHint: att.maskHint, bashCommand: desk.bashCommand,
    sendState: peek.sendState, replyTextError: peek.replyTextError,
    lastSessions: [], pendingAnswers: new Set(), cardPeekCache: new Map(), cardAskSeen: new Map(), cardExpanded: new Set(),
    refreshSessions: () => Promise.resolve(),
    toggleCardPeek: () => {}, sendAnswer: () => {}, closeDrawerFor: () => {}, openTerminal: () => {},
    mobileActive: () => false, sessionRowFor: () => null, buildEnableRepliesButton: () => new Node("button"),
    postReplyable: async () => true, renderSSOBody: () => new Node("div"), renderStoreBody: () => new Node("div"),
    repliesEnabled: true,
    fetch: (url, init) => { posts.push({ url, init }); return box.fetchImpl(url, init); },
    fetchImpl: async () => ({ status: 204 }),
  };
  const ctx = vm.createContext(box);
  const run = (src) => new vm.Script(src).runInContext(ctx);
  run(read("pending.js").replace(/if \(typeof module[\s\S]*$/, ""));
  run([
    declSrc(appSrc, "replyFocused"), declSrc(appSrc, "pendingSends"), declSrc(appSrc, "replyDraft"),
    declSrc(appSrc, "REPLY_SENT_MS"), declSrc(appSrc, "REPLY_ERROR_MS"), declSrc(appSrc, "replyFlash"),
    fnSrc(appSrc, "syncMaskHint"), fnSrc(appSrc, "sendReplyFor"), fnSrc(appSrc, "buildReplyComposer"), fnSrc(appSrc, "buildCardComposer"),
    fnSrc(appSrc, "stripOpenButton"), fnSrc(appSrc, "renderNeedsBody"), fnSrc(appSrc, "pruneReplyFlash"),
    "globalThis.buildCardComposer = buildCardComposer; globalThis.renderNeedsBody = renderNeedsBody; globalThis.sendReplyFor = sendReplyFor;",
    "globalThis.pendingSends = pendingSends; globalThis.replyFlash = replyFlash; globalThis.pruneReplyFlash = pruneReplyFlash;",
  ].join("\n"));
  if (over) Object.assign(box, over);
  // attention.js and peek.js run here in Node, not in the sandbox: they read the real global.
  globalThis.repliesEnabled = box.repliesEnabled;
  return { box, clock, toasts, posts };
}

const ASK = { id: "s1", name: "s1", activity: "waiting", need: "ask", ask: "Bash(git push origin main)", ask_full: "Bash(git push origin main --force)", claude: true, replyable: true };

// --- F50 / F51: the lines of the ask strip's body ------------------------------------
function body(p, s, expanded, opts) {
  p.box.lastSessions = [s];
  if (expanded) { p.box.cardExpanded.add(s.id); p.box.cardAskSeen.set(s.id, s.ask_full); }
  return p.box.renderNeedsBody(s, { strip: true, ...opts });
}
const lines = (b) => b.children.map((c) => (c.cls.has("nacts") ? "[acts]" : c.cls.has("rep") ? "[reply]" : c.cls.has("askfull") ? "pre:" + c.textContent : c.textContent));
const yesNo = (b) => b.byClass("nacts")[0].buttons().filter((x) => x.dataset.choice);

test("F50: replies off, an ask strip has no Yes/No and says where to answer", () => {
  const p = page({ repliesEnabled: false });
  const b = body(p, ASK, false);
  assert.strictEqual(yesNo(b).length, 0, "no Yes/No");
  assert.deepStrictEqual(lines(b), ["git push origin main", "[acts]", "Answer in the terminal · remote replies are off"]);
  assert.ok(b.byClass("peek-why").length === 1, "the line is the strip's .peek-why");
  // Open terminal is still the way to answer.
  assert.deepStrictEqual(b.byClass("nacts")[0].buttons().map((x) => x.textContent), ["Open terminal"]);
  // A turn has no Yes/No to explain: nothing is said.
  const turn = { id: "t", activity: "waiting", need: "turn", claude: true, replyable: true, summary: "done" };
  assert.strictEqual(body(page({ repliesEnabled: false }), turn, false).byClass("peek-why").length, 0);
  // Not yet heard from /api/meta: no claim.
  const q = page({ repliesEnabled: undefined });
  assert.strictEqual(body(q, ASK, false).byClass("peek-why").length, 0);
});

test("F51: replies on and collapsed, the reason Yes/No are disabled is a line, not only a tooltip", () => {
  const p = page();
  const b = body(p, ASK, false);
  const [yes, no] = yesNo(b);
  assert.deepStrictEqual([yes.textContent, no.textContent], ["Yes", "No"]);
  assert.ok(yes.disabled && no.disabled, "disabled until expanded");
  assert.ok(yes.cls.has("off") && no.cls.has("off"));
  assert.strictEqual(yes.title, "Expand to see the full command first", "the tooltip stays");
  assert.deepStrictEqual(lines(b), ["git push origin main", "[acts]", "Expand to see the full command first"]);
  assert.strictEqual(b.byClass("peek-why")[0].textContent, "Expand to see the full command first");
});

test("F51: expanded, line 1 is still the command and the peek message is on its own line below it", () => {
  const p = page();
  p.box.cardPeekCache.set("s1", { text: "I will push the branch so CI can run.", muted: false });
  const b = body(p, ASK, true);
  assert.deepStrictEqual(lines(b), [
    "git push origin main",
    "I will push the branch so CI can run.",
    "pre:Bash(git push origin main --force)",
    "[acts]",
  ]);
  assert.ok(b.children[0].cls.has("peektxt") && b.children[0].cls.has("ask"), "line 1 is the toggle");
  assert.strictEqual(b.children[0].getAttribute("aria-expanded"), "true");
  assert.ok(b.children[1].cls.has("askmsg"), "the message has its own line");
  const [yes, no] = yesNo(b);
  assert.ok(!yes.disabled && !no.disabled, "Yes/No unlock once the command was read");
  assert.strictEqual(b.byClass("peek-why").length, 0, "nothing left to explain");
});

test("task 9b: the strip's command is what the frame draws, the command alone after the label 'Asks to run'", () => {
  const p = page();
  const first = (s, expanded) => body(p, s, expanded).children[0];
  // Bash(<cmd>): the command, in the mono toggle line; nested parentheses stay.
  assert.strictEqual(first({ ...ASK, ask: "Bash(echo $(date))" }, false).textContent, "echo $(date)");
  assert.ok(first(ASK, false).cls.has("ask"), "still the mono .peektxt.ask");
  // Any other tool keeps Tool(args) as the server wrote it.
  assert.strictEqual(first({ ...ASK, ask: "Read(/etc/hosts)" }, false).textContent, "Read(/etc/hosts)");
  assert.strictEqual(first({ ...ASK, ask: "WebFetch" }, false).textContent, "WebFetch");
  assert.strictEqual(first({ ...ASK, ask: "Bash()" }, false).textContent, "Bash()");
  // A call cut at the server's limit keeps its ellipsis and loses nothing else.
  assert.strictEqual(first({ ...ASK, ask: "Bash(find / -name x…" }, false).textContent, "find / -name x…");
  // No tool call text: the summary / waiting line, untouched.
  assert.strictEqual(first({ ...ASK, ask: "", summary: "**ok**" }, false).textContent, "ok");
  // Expanded: line 1 is still the command (F51), the toggle's title and role are unchanged.
  const e = first(ASK, true);
  assert.strictEqual(e.textContent, "git push origin main");
  assert.strictEqual(e.getAttribute("aria-expanded"), "true");
});

test("task 9b: every button of the strip is the kit's 24px .btn-sm, Yes also the amber .btn-yes", () => {
  const p = page();
  const b = body(p, ASK, true);
  const [yes, no] = yesNo(b);
  assert.strictEqual(yes.className, "btn btn-sm btn-yes");
  assert.strictEqual(no.className, "btn btn-sm", "No is the secondary .btn-sm");
  assert.ok(!yes.cls.has("off") && !no.cls.has("off"));
  const open = b.byClass("nacts")[0].buttons().find((x) => x.textContent === "Open terminal");
  assert.ok(open.cls.has("btn-sm"));
  // Disabled keeps the classes (the look is CSS): the collapsed ask.
  const c = yesNo(body(page(), ASK, false));
  assert.deepStrictEqual(c.map((x) => x.className), ["btn btn-sm btn-yes off", "btn btn-sm off"]);
  // Reply controls in the strip are small too: Disallow replies (replies on, a replyable turn), Enable replies.
  const turn = { id: "t", name: "t", activity: "waiting", need: "turn", claude: true, replyable: true, summary: "done" };
  const dis = body(page(), turn, false).byClass("nacts")[0].buttons().find((x) => x.textContent === "Disallow replies");
  assert.strictEqual(dis.className, "btn btn-sm");
  const en = body(page(), { ...turn, replyable: false }, false).byClass("nacts")[0].buttons()[0];
  assert.ok(en.cls.has("btn-sm"), "Enable replies");
});

test("task 9b: the sign-in strip's buttons are .btn-sm too, Sign in still the primary", () => {
  const app = read("app.js");
  const sso = (opts) => {
    const box = {
      document: { createElement: (t) => new Node(t), createTextNode: (t) => Object.assign(new Node("#text"), { own: t }) },
      ensureSSOStatus: () => {}, ssoModel: () => ({ text: "AWS sign-in expired", login: null, command: "aws sso login --profile x" }),
      ssoNote: (t) => Object.assign(new Node("p"), { own: t }), ssoStarting: new Set(), ssoSignIn: () => {}, copyText: () => {}, showToast: () => {},
      stripOpenButton: (s) => { const b = new Node("button"); b.className = "btn btn-sm"; b.textContent = "Open terminal"; return b; },
      openTerminal: () => {}, buildCardComposer: () => ({ form: new Node("form"), why: new Node("span"), refresh() {} }),
      mobileActive: () => false, window: { innerWidth: 1440 }, ssoExpiryText: () => "", ssoTimeLeft: () => "", ssoCodeEl: () => new Node("div"),
    };
    const ctx = vm.createContext(box);
    new vm.Script(fnSrc(app, "renderSSOBody") + "\nglobalThis.renderSSOBody = renderSSOBody;").runInContext(ctx);
    return box.renderSSOBody({ id: "s1", name: "s1" }, opts).byClass("nacts")[0].buttons();
  };
  const strip = sso({ strip: true });
  assert.deepStrictEqual(strip.map((b) => b.textContent), ["Sign in", "Copy host command", "Open terminal"]);
  assert.deepStrictEqual(strip.map((b) => b.className), ["btn pri btn-sm", "btn btn-sm", "btn btn-sm"]);
  // The card outside the strip (the Access view's card, the phone's) keeps the plain buttons.
  assert.deepStrictEqual(sso({ bare: true }).map((b) => b.className), ["btn pri", "btn"]);
  assert.deepStrictEqual(sso({ mobile: true }).map((b) => b.className), ["btn pri", "btn"]);
});

test("task 9b: outside the strip (the needs-you card) Yes/No keep the plain .btn", () => {
  const p = page();
  p.box.lastSessions = [ASK];
  p.box.cardExpanded.add(ASK.id); p.box.cardAskSeen.set(ASK.id, ASK.ask_full);
  const b = p.box.renderNeedsBody(ASK, {});
  const [yes, no] = yesNo(b);
  assert.deepStrictEqual([yes.textContent, yes.className, no.className], ["1 Yes", "btn", "btn"]);
});

test("task 9b: the strip's mask note follows the command it shows", () => {
  const p = page();
  const b = body(p, { ...ASK, ask: "Bash(echo [masked:aws-key-id])" }, false);
  assert.strictEqual(b.children[0].textContent, "echo [masked:aws-key-id]");
  assert.strictEqual(b.byClass("maskhint").length, 1);
});

test("F51: expanded with no message yet (a tool call has none) shows no message line; loading shows none either", () => {
  const p = page();
  p.box.cardPeekCache.set("s1", { text: "No message yet.", muted: true });
  assert.deepStrictEqual(lines(body(p, ASK, true)), ["git push origin main", "pre:Bash(git push origin main --force)", "[acts]"]);
  const q = page(); // nothing fetched yet
  assert.deepStrictEqual(lines(body(q, ASK, true)), ["git push origin main", "pre:Bash(git push origin main --force)", "[acts]"]);
});

test("F51: a message that carries a mask token gets the display-mask note under its own line", () => {
  const p = page();
  p.box.cardPeekCache.set("s1", { text: "token is [masked:aws-key] ok", muted: false });
  const b = body(p, ASK, true);
  assert.strictEqual(b.children[1].textContent, "token is [masked:aws-key] ok");
  assert.match(b.children[2].textContent, /Display mask/);
});

test("a turn strip still swaps its one line for the full message when expanded (unchanged)", () => {
  const turn = { id: "t", name: "t", activity: "waiting", need: "turn", claude: true, replyable: true, reply_ready: true, summary: "short **summary**" };
  const p = page();
  assert.strictEqual(body(p, turn, false).children[0].textContent, "short **summary**".replace(/\*\*/g, ""));
  const q = page();
  q.box.cardPeekCache.set("t", { text: "The whole message, long.", muted: false });
  assert.strictEqual(body(q, turn, true).children[0].textContent, "The whole message, long.");
});

// --- F65: Sending…, then Sent ----------------------------------------------------------
const TURN = { id: "s1", name: "s1", activity: "waiting", need: "turn", claude: true, replyable: true, reply_ready: true, summary: "done" };
function composer(p, s) {
  p.box.lastSessions = [s || TURN];
  const c = p.box.buildCardComposer(s || TURN);
  c.refresh();
  const [input, send] = c.form.children;
  return { c, input, send, why: c.why };
}
function held(p) { // a message POST the test settles by hand
  let settle;
  p.box.fetchImpl = () => new Promise((res) => { settle = res; });
  return (status) => settle({ status });
}
const flush = () => new Promise((r) => setImmediate(r));

test("F65: a reply shows Sending… on the button while it runs, then Sent for 2 s under the field, and no toast", async () => {
  const p = page();
  const { c, input, send, why } = composer(p);
  const settle = held(p);
  input.value = "go ahead";
  const done = c.send("go ahead");
  assert.strictEqual(send.disabled, true, "closed at once");
  assert.strictEqual(send.textContent, "send", "a quick send never flickers a label");
  p.clock.tick(299);
  assert.strictEqual(send.textContent, "send");
  p.clock.tick(1);
  assert.strictEqual(send.textContent, "Sending…");
  assert.ok(send.cls.has("busy"));
  assert.strictEqual(send.getAttribute("aria-busy"), "true");
  settle(204);
  await done;
  assert.strictEqual(send.textContent, "send", "the label is restored");
  assert.ok(!send.cls.has("busy"));
  assert.strictEqual(send.getAttribute("aria-busy"), null);
  assert.strictEqual(send.disabled, false, "the gate is open again");
  assert.strictEqual(input.value, "", "the field is cleared");
  assert.strictEqual(why.textContent, "Sent");
  assert.deepStrictEqual(p.toasts, [], "no Sent toast on desktop");
  assert.match(p.posts[0].url, /\/api\/sessions\/s1\/message$/);
  p.clock.tick(1999);
  assert.strictEqual(why.textContent, "Sent");
  p.clock.tick(1);
  assert.strictEqual(why.textContent, "", "gone after 2000 ms");
});

test("F65: Sent survives the strip being rebuilt inside its 2 s", async () => {
  const p = page();
  const first = composer(p);
  const settle = held(p);
  const done = first.c.send("go");
  settle(204);
  await done;
  p.clock.tick(500);
  // The poll replaced the strip: a fresh composer for the same session.
  const second = composer(p);
  assert.strictEqual(second.why.textContent, "Sent");
  p.clock.tick(1500);
  assert.strictEqual(second.why.textContent, "", "the new line clears on the same deadline");
});

test("F65: a refused send shows its reason on the line and puts the button back", async () => {
  const p = page();
  const { c, input, send, why } = composer(p);
  const settle = held(p);
  input.value = "go";
  const done = c.send("go");
  p.clock.tick(400);
  assert.strictEqual(send.textContent, "Sending…");
  settle(409);
  await done;
  assert.strictEqual(why.textContent, "Session is not ready for a message right now.");
  assert.strictEqual(send.textContent, "send");
  assert.strictEqual(send.disabled, false);
  assert.strictEqual(input.value, "go", "what was typed is kept");
  assert.deepStrictEqual(p.toasts.map((t) => t.m), ["Session is not ready for a message right now."], "the error toast is unchanged");
  p.clock.tick(29000);
  assert.strictEqual(why.textContent, "Session is not ready for a message right now.", "an error stays long enough to read after looking away");
  p.clock.tick(1000);
  assert.strictEqual(why.textContent, "", "but not for ever: it described a moment");
  // The next attempt starts clean.
  p.box.fetchImpl = async () => ({ status: 204 });
  await c.send("go");
  assert.strictEqual(why.textContent, "Sent");
});

test("F65: a network failure says so on the line too", async () => {
  const p = page();
  const { c, why, send } = composer(p);
  p.box.fetchImpl = () => Promise.reject(new Error("offline"));
  await c.send("go");
  assert.strictEqual(why.textContent, "Send failed: no connection.");
  assert.strictEqual(send.disabled, false);
});

test("F65: a session that left the list takes its leftover line with it", () => {
  const p = page();
  p.box.replyFlash.set("gone", { text: "Send failed: no connection.", until: Date.now() + 30000 });
  p.box.replyFlash.set("s1", { text: "Sent", until: Date.now() + 2000 });
  p.box.lastSessions = [TURN];
  p.box.pruneReplyFlash();
  assert.deepStrictEqual([...p.box.replyFlash.keys()], ["s1"]);
  assert.match(fnSrc(read("app.js"), "refreshSessionsRun"), /pruneReplyFlash\(\);/);
});

test("F65: a second tap while one is in flight sends nothing more", async () => {
  const p = page();
  const { c } = composer(p);
  const settle = held(p);
  const a = c.send("one");
  await c.send("two");
  assert.strictEqual(p.posts.length, 1);
  settle(204);
  await a;
});

test("F65: the phone keeps its Sent toast (sendReplyFor with no options, buildReplyStrip untouched)", async () => {
  const p = page();
  assert.strictEqual(await p.box.sendReplyFor(TURN, "1"), true);
  assert.deepStrictEqual(p.toasts.map((t) => t.m), ["Sent"]);
  const tabs = read("tabs.js");
  const strip = fnSrc(tabs, "buildReplyStrip");
  assert.match(strip, /fld\.value = "";\n\s+showToast\("Sent"\);/);
  // The whole function, byte for byte as on main (the phone's reply strip, postOnce included).
  assert.strictEqual(sha(strip), "83d748364cdb1e31f2e4bf86254172a686a6966e9c7cdfb26f048c43b183df23");
  assert.strictEqual(sha(fnSrc(read("app.js"), "renderNeedsCard")), "9f31bf795d9a4b8487bf77bd62d967f84a12965c23e0611974dacda46e7f3853");
});

// --- F63: the sign-in line ---------------------------------------------------------------
function ssoPage(sessions) {
  const clock = clockFor();
  const toasts = [];
  const synced = [];
  const appSrc = read("app.js");
  const box = {
    console, Promise, JSON, Set, Map, String, Array,
    window: { innerWidth: 1440 }, mobileActive: () => false,
    lastSessions: sessions, showToast: (m) => toasts.push(m), ssoDoneKeep: att.ssoDoneKeep, connSSOSources: att.connSSOSources,
    cells: [{ repaintReply: () => synced.push("repaint") }],
  };
  const ctx = vm.createContext(box);
  new vm.Script([
    declSrc(appSrc, "SSO_DONE_TOAST"), declSrc(appSrc, "ssoDone"), declSrc(appSrc, "ssoDoneHeard"),
    fnSrc(appSrc, "markSSODone"), fnSrc(appSrc, "pruneSSODone"), fnSrc(appSrc, "dismissSSODone"), fnSrc(appSrc, "ssoDoneShown"),
    fnSrc(appSrc, "ssoDoneFirstShow"),
    "globalThis.api = { markSSODone, pruneSSODone, dismissSSODone, ssoDoneShown, ssoDoneFirstShow, ssoDone, ssoDoneHeard, SSO_DONE_TOAST };",
  ].join("\n")).runInContext(ctx);
  return { box, api: box.api, synced, toasts, clock };
}

test("F63: a finished sign-in is kept per session, with the activity it saw, and repainted", () => {
  const p = ssoPage([{ id: "a", activity: "waiting", need: "turn" }, { id: "b", activity: "working" }]);
  p.api.markSSODone("a");
  assert.strictEqual(p.api.ssoDone.get("a"), "waiting");
  assert.deepStrictEqual(p.synced, ["repaint"]);
  assert.strictEqual(p.api.ssoDoneShown({ id: "a", need: "turn" }), true);
  assert.strictEqual(p.api.ssoDoneShown({ id: "b" }), false);
  // While the sign-in need is still on the record (the poll has not seen it cleared) the strip is the SSO form.
  assert.strictEqual(p.api.ssoDoneShown({ id: "a", need: "sso" }), false);
  assert.strictEqual(p.api.ssoDoneShown({ id: "a", aws_connections: [{ name: "x", sso_expired: "corp" }] }), false);
});

test("F63: each poll keeps the line until not-working becomes working, or the session ends or leaves", () => {
  const p = ssoPage([{ id: "a", activity: "waiting" }, { id: "w", activity: "working" }, { id: "x", activity: "waiting" }, { id: "g", activity: "waiting" }]);
  for (const id of ["a", "w", "x", "g"]) p.api.markSSODone(id);
  // poll: a still waiting, w still working, x exited, g left the list
  p.box.lastSessions = [{ id: "a", activity: "waiting" }, { id: "w", activity: "working" }, { id: "x", activity: "done", exited: true }];
  p.api.pruneSSODone();
  assert.deepStrictEqual([...p.api.ssoDone.keys()].sort(), ["a", "w"]);
  // poll: a starts working (drops); w stops (stays, remembers it stopped)
  p.box.lastSessions = [{ id: "a", activity: "working" }, { id: "w", activity: "waiting" }];
  p.api.pruneSSODone();
  assert.deepStrictEqual([...p.api.ssoDone.keys()], ["w"]);
  // poll: w starts working again: it took the retry up
  p.box.lastSessions = [{ id: "w", activity: "working" }];
  p.api.pruneSSODone();
  assert.strictEqual(p.api.ssoDone.size, 0);
});

test("F63: the ✕ drops the line and repaints", () => {
  const p = ssoPage([{ id: "a", activity: "waiting" }]);
  p.api.markSSODone("a");
  p.synced.length = 0;
  p.api.dismissSSODone("a");
  assert.strictEqual(p.api.ssoDone.has("a"), false);
  assert.deepStrictEqual(p.synced, ["repaint"]);
});

test("F63: the toast sites keep their exact call, each followed by the mark; the line's text is the toast's", () => {
  const app = read("app.js");
  const tick = fnSrc(app, "ssoPollTick");
  const signIn = fnSrc(app, "ssoSignIn");
  assert.strictEqual((tick.match(/showToast\(SSO_DONE_TOAST\);/g) || []).length, 2);
  assert.strictEqual((signIn.match(/showToast\(SSO_DONE_TOAST\);/g) || []).length, 1);
  assert.strictEqual((tick.match(/markSSODone\(id\);/g) || []).length, 2);
  assert.strictEqual((signIn.match(/markSSODone\(id\);/g) || []).length, 1);
  assert.match(app, /const SSO_DONE_TOAST = "Signed in\. The agent's next AWS call works\. Tell it to retry if it stopped\.";/);
  // refreshSessions sweeps the kept lines right after pruneSSO, before the panes repaint.
  const poll = fnSrc(app, "refreshSessionsRun");
  assert.ok(poll.indexOf("pruneSSO();") >= 0 && poll.indexOf("pruneSSODone();") > poll.indexOf("pruneSSO();"));
  assert.ok(poll.indexOf("pruneSSODone();") < poll.indexOf("c.repaintReply()"));
});

test("F63: buildNeedStrip draws the line with a ✕, alone as a neutral strip or as the last row of a need strip", () => {
  const tabs = read("tabs.js");
  const mk = (done) => {
    const dismissed = [];
    const heard = new Set();
    const box = {
      document: { createElement: (t) => new Node(t) }, cardContent: () => ({}), cardExpanded: new Set(), stripLabel: (l) => l,
      renderNeedsBody: () => new Node("div"), ResizeObserver: undefined, SSO_DONE_TOAST: "Signed in. The agent's next AWS call works. Tell it to retry if it stopped.",
      ssoDoneShown: () => done, dismissSSODone: (id) => dismissed.push(id),
      ssoDoneFirstShow: (id) => { if (heard.has(id)) return false; heard.add(id); return true; },
      ...iconStubs(() => new Node("svg")),
    };
      const ctx = vm.createContext(box);
    new vm.Script(fnSrc(tabs, "buildSSODoneLine") + "\n" + fnSrc(tabs, "buildNeedStrip") + "\nglobalThis.buildNeedStrip = buildNeedStrip;").runInContext(ctx);
    return { build: box.buildNeedStrip, dismissed };
  };
  const m = mk(true);
  // Alone: no need, the neutral .is-done strip.
  const alone = m.build({ id: "a" }, null, null);
  assert.strictEqual(alone.className, "needstrip is-done");
  const line = alone.byClass("ssodone")[0];
  assert.strictEqual(line.byClass("ssodone-txt")[0].textContent, "Signed in. The agent's next AWS call works. Tell it to retry if it stopped.");
  const x = line.buttons()[0];
  assert.strictEqual(x.className, "btn btn-ghost btn-icon btn-sm");
  assert.strictEqual(x.getAttribute("aria-label"), "Dismiss");
  assert.strictEqual(x.title, "Dismiss");
  // every icon-only control has an aria-label: the dismiss is the x glyph (14), not the text ✕.
  assert.strictEqual(x.textContent, "");
  assert.deepStrictEqual(x.children.map((n) => [n.getAttribute("data-icon"), n.getAttribute("width")]), [["x", "14"]]);
  assert.ok(x.getAttribute("aria-label").trim());
  x.onclick({ stopPropagation() {} });
  assert.deepStrictEqual(m.dismissed, ["a"]);
  // Under a need: the same line is the strip's last row, the strip keeps its own tone.
  const under = m.build({ id: "a" }, { label: "your turn", text: "t", tone: "done" }, null);
  assert.strictEqual(under.className, "needstrip is-done");
  assert.strictEqual(under.children[under.children.length - 1].className, "ssodone");
  const amber = m.build({ id: "a" }, { label: "asks", text: "t", tone: "block" }, null);
  assert.strictEqual(amber.className, "needstrip");
  assert.strictEqual(amber.children[amber.children.length - 1].className, "ssodone");
  // Not kept: nothing added.
  const none = mk(false).build({ id: "a" }, { label: "asks", text: "t", tone: "block" }, null);
  assert.strictEqual(none.byClass("ssodone").length, 0);
});

// The line is a live region the first time it appears for a sign-in, so a screen reader reads it once;
// a strip rebuild (a poll that moved something else) draws it again without the role and stays quiet.
test("F63: the kept line is role=status only the first time it appears; a rebuild is silent", () => {
  const tabs = read("tabs.js");
  const heard = new Set();
  const box = { document: { createElement: (t) => new Node(t) }, SSO_DONE_TOAST: "Signed in.", dismissSSODone() {}, ...iconStubs(() => new Node("svg")),
    ssoDoneFirstShow: (id) => { if (heard.has(id)) return false; heard.add(id); return true; } };
  const ctx = vm.createContext(box);
  new vm.Script(fnSrc(tabs, "buildSSODoneLine") + "\nglobalThis.build = buildSSODoneLine;").runInContext(ctx);
  const first = box.build({ id: "a" });
  assert.strictEqual(first.getAttribute("role"), "status", "first appearance: announced");
  const again = box.build({ id: "a" });
  assert.strictEqual(again.getAttribute("role"), null, "a rebuild of the same line: not announced again");
  assert.strictEqual(again.byClass("ssodone-txt")[0].textContent, "Signed in.", "it still draws the same line");
  assert.strictEqual(box.build({ id: "b" }).getAttribute("role"), "status", "another session's line is its own first");
});

test("F63: a line that goes away (✕, prune, a new sign-in) is announced again the next time it appears", () => {
  const p = ssoPage([{ id: "a", activity: "waiting" }, { id: "g", activity: "waiting" }]);
  const { api } = p;
  p.api.markSSODone("a");
  assert.strictEqual(api.ssoDoneFirstShow("a"), true);
  assert.strictEqual(api.ssoDoneFirstShow("a"), false, "second build of the same line");
  assert.strictEqual(api.ssoDoneFirstShow("a"), false);
  // another sign-in finishes for the same session: a new line, announced
  api.markSSODone("a");
  assert.strictEqual(api.ssoDoneFirstShow("a"), true);
  // the ✕
  api.dismissSSODone("a");
  assert.strictEqual(api.ssoDoneHeard.has("a"), false);
  api.markSSODone("a");
  assert.strictEqual(api.ssoDoneFirstShow("a"), true);
  // a prune that drops it (the session started working again, or left the list)
  api.markSSODone("g");
  assert.strictEqual(api.ssoDoneFirstShow("g"), true);
  p.box.lastSessions = [{ id: "a", activity: "working" }];
  api.pruneSSODone();
  assert.strictEqual(api.ssoDoneHeard.has("g"), false, "left the list");
  assert.strictEqual(api.ssoDoneHeard.has("a"), false, "took the retry up");
  // a kept line stays heard across polls
  api.markSSODone("a");
  assert.strictEqual(api.ssoDoneFirstShow("a"), true);
  p.box.lastSessions = [{ id: "a", activity: "waiting" }];
  api.pruneSSODone();
  assert.strictEqual(api.ssoDoneFirstShow("a"), false, "still the same line after a poll");
});

// --- Cell.syncStrip: when the pane's strip is rebuilt ------------------------------------
// The method is cut out of tabs.js and run on a stand-in pane.
function paneSync(over) {
  const tabs = read("tabs.js");
  const at = tabs.indexOf("  syncStrip() {");
  assert.ok(at >= 0, "syncStrip not found");
  let depth = 0, end = -1;
  for (let i = tabs.indexOf("{", at); i < tabs.length; i++) {
    if (tabs[i] === "{") depth++;
    else if (tabs[i] === "}" && --depth === 0) { end = i + 1; break; }
  }
  const method = tabs.slice(at, end);
  const built = [];
  const desk = require("../static/deskshell.js");
  const box = {
    document: { activeElement: null }, window: { innerWidth: 1440 },
    lastSessions: [], cellTerminal: () => ({ id: "s1" }), needStripModel: desk.needStripModel, stripKey: desk.stripKey, stripHoldKey: desk.stripHoldKey,
    cardExpanded: new Set(), cardPeekCache: new Map(), pendingAnswers: new Set(), pendingSends: new Set(),
    ssoLocalSig: () => "", ssoDoneShown: () => false, positionDrawer: () => {}, replyFocused: false,
    buildNeedStrip: (s, model) => { const n = new Node("div"); n.model = model; n.contains = () => false; built.push(n); return n; },
    repliesEnabled: true,
  };
  Object.assign(box, over || {});
  const ctx = vm.createContext(box);
  const pane = vm.runInContext("({" + method + "})", ctx);
  const cell = Object.assign(Object.create(pane), {
    el: new Node("div"), bar: new Node("div"), activeTab: null, _strip: null, _needsKey: undefined, _holdSig: undefined,
    dropStrip() { if (this._strip) { this._strip.remove(); this._strip = null; } },
  });
  const host = new Node("div");
  host.appendChild(cell.bar);
  return { box, cell, built, att: att };
}
const WAIT = { id: "s1", name: "s1", activity: "waiting", need: "turn", claude: true, replyable: true, reply_ready: true, summary: "one" };

test("syncStrip: a send in flight keeps its strip, and the first sync after it settles repaints what moved", () => {
  const p = paneSync();
  p.box.lastSessions = [WAIT];
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 1);
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 1, "nothing moved: no rebuild");
  // The send starts; the poll moves an incidental field (the summary) and the strip would be rebuilt under the button.
  p.box.pendingSends.add("s1");
  p.box.lastSessions = [{ ...WAIT, summary: "two" }];
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 1, "held while the send runs");
  // A different need still replaces it at once, as with a focused field.
  p.box.lastSessions = [{ ...WAIT, need: "ask", ask: "ls", ask_full: "ls -la" }];
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 2, "a new need is not held");
  // Settled: the summary change that was held lands on the next sync.
  p.box.lastSessions = [{ ...WAIT, summary: "three" }];
  p.box.pendingSends.delete("s1");
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 3);
});

test("syncStrip: with no need, the kept sign-in line is a strip of its own and goes when the line goes (F63)", () => {
  const p = paneSync();
  const working = { id: "s1", name: "s1", activity: "working", claude: true };
  let done = false;
  p.box.ssoDoneShown = () => done;
  p.box.lastSessions = [working];
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 0, "nothing to show");
  assert.strictEqual(p.cell.el.cls.has("needs"), false);
  done = true;
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 1);
  assert.strictEqual(p.built[0].model, null, "no need: the strip is only the line");
  assert.ok(p.cell._strip, "it is the pane's strip (the drawer sits below it)");
  assert.strictEqual(p.cell.el.cls.has("needs"), false, "the amber frame is the need's");
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 1, "stable between polls");
  done = false;
  p.cell.syncStrip();
  assert.strictEqual(p.cell._strip, null, "✕ or the next working turn removes it");
  // Under a need the same line is a row of that strip: appearing rebuilds it.
  p.box.lastSessions = [WAIT];
  p.cell.syncStrip();
  const n = p.built.length;
  done = true;
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, n + 1, "the line appearing moves the key");
  assert.ok(p.built[n].model, "and the need's strip carries it");
});

test("syncStrip: a strip built before /api/meta answers gains the replies-off line once it says off (F50)", () => {
  const ASKING = { id: "s1", name: "s1", activity: "waiting", need: "ask", ask: "Bash(ls)", ask_full: "ls -la", claude: true, replyable: true };
  const p = paneSync({ repliesEnabled: undefined });
  p.box.lastSessions = [ASKING];
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 1, "built while the page has not heard from /api/meta");
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 1, "undefined, polled again: no rebuild");
  // /api/meta answers "off": the strip must be drawn again so renderNeedsBody can add the line.
  p.box.repliesEnabled = false;
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 2, "undefined -> false rebuilds the strip");
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 2, "and then it is stable");
  // The other answer still rebuilds too (it did before).
  p.box.repliesEnabled = true;
  p.cell.syncStrip();
  assert.strictEqual(p.built.length, 3);
});

test("syncStrip: a send in flight does not keep a strip whose need has cleared or whose session is gone", () => {
  const p = paneSync();
  p.box.lastSessions = [WAIT];
  p.cell.syncStrip();
  assert.ok(p.cell._strip);
  // The send is out, and the agent starts working on it: no need, so nothing to hold.
  p.box.pendingSends.add("s1");
  p.box.lastSessions = [{ ...WAIT, activity: "working", need: "" }];
  p.cell.syncStrip();
  assert.strictEqual(p.cell._strip, null, "the need cleared: the strip goes even with the send unsettled");
  // And a session that left the list takes it too.
  p.box.pendingSends.delete("s1");
  p.box.lastSessions = [WAIT];
  p.cell.syncStrip();
  assert.ok(p.cell._strip);
  p.box.pendingSends.add("s1");
  p.box.lastSessions = [];
  p.cell.syncStrip();
  assert.strictEqual(p.cell._strip, null, "the session is gone: the strip goes");
});
