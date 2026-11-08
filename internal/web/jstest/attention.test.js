"use strict";
// Replies are ON for the assertions below; the kill-switch group at the end turns them off.
globalThis.repliesEnabled = true;
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { needsYou, attentionClass, sessionDot, tabNeedWhy, needLabel, cardContent, attentionTitle, faviconHref, needsTransitions, NEEDS_REARM_MS, notifyAvailability, notifyRowCopy, notifyChecked, notifyBody, notifyPoll, sha256Hex, answerState, answerErrorText, staleExpanded, maskHint, ssoDoneKeep } = require("../static/attention.js");

test("needLabel names the two needs, and nothing that is not waiting", () => {
  assert.strictEqual(needLabel({ activity: "waiting", need: "ask" }), "asks");
  assert.strictEqual(needLabel({ activity: "waiting", need: "turn" }), "your turn");
  assert.strictEqual(needLabel({ activity: "waiting" }), "");
  assert.strictEqual(needLabel({ activity: "working", need: "ask" }), "");
  assert.strictEqual(needLabel(null), "");
});

test("ask card shows what the agent wants to run, and no reply path", () => {
  const c = cardContent({ activity: "waiting", need: "ask", ask: "Bash(rm -rf build)", claude: true, replyable: true, summary: "x" });
  assert.deepEqual(c, { text: "Bash(rm -rf build)", ask: true, reply: false, enableReplies: false });
});

test("ask card without ask text falls back to the summary", () => {
  assert.strictEqual(cardContent({ activity: "waiting", need: "ask", claude: true, summary: "may I?" }).text, "may I?");
});

test("turn card offers a reply field only when replies are enabled", () => {
  assert.deepEqual(cardContent({ activity: "waiting", need: "turn", claude: true, replyable: true, summary: "done" }),
    { text: "done", ask: false, reply: true, enableReplies: false });
  assert.deepEqual(cardContent({ activity: "waiting", need: "turn", claude: true, replyable: false, summary: "done" }),
    { text: "done", ask: false, reply: false, enableReplies: true });
});

test("claude waiting with no need (transcript unreadable) is Open only, like non-claude", () => {
  const c = cardContent({ activity: "waiting", claude: true, replyable: true });
  assert.deepEqual(c, { text: "Waiting for input.", ask: false, reply: false, enableReplies: false });
});

test("non-claude waiting card offers Open only", () => {
  assert.deepEqual(cardContent({ activity: "waiting", claude: false, replyable: true, summary: "" }),
    { text: "Waiting for input.", ask: false, reply: false, enableReplies: false });
});

test("attentionTitle prefixes the waiting count only when there is one", () => {
  assert.strictEqual(attentionTitle(0, "DEV"), "[DEV] CuliBox");
  assert.strictEqual(attentionTitle(2, "DEV"), "(2) [DEV] CuliBox");
  assert.strictEqual(attentionTitle(1, ""), "(1) CuliBox");
  assert.strictEqual(attentionTitle(0, ""), "CuliBox");
});

test("faviconHref flips to the amber-dot icon while anything needs you", () => {
  assert.strictEqual(faviconHref(0), "/favicon.svg");
  assert.strictEqual(faviconHref(3), "/favicon-needs.svg");
});

const W = (id) => ({ id, activity: "waiting" });
const B = (id) => ({ id, activity: "working" });
// seeded: the state after a first poll that saw nothing waiting.
const seeded = (now = 0) => needsTransitions(null, [], now).next;

test("the first poll after load only seeds — no notification for already-waiting sessions", () => {
  const r = needsTransitions(null, [W("a"), B("b")], 0);
  assert.deepEqual(r.fired, []);
  assert.deepEqual([...r.next.ids], ["a"]);
});

test("a session that becomes waiting fires exactly once", () => {
  let r = needsTransitions(seeded(), [W("a")], 3000);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
  r = needsTransitions(r.next, [W("a")], 6000);
  assert.deepEqual(r.fired, []);
});

test("waiting -> sustained working -> waiting fires again", () => {
  let r = needsTransitions(null, [W("a")], 0);
  r = needsTransitions(r.next, [B("a")], 3000);
  assert.deepEqual(r.fired, []);
  for (let t = 6000; t < 3000 + NEEDS_REARM_MS; t += 3000) r = needsTransitions(r.next, [B("a")], t);
  r = needsTransitions(r.next, [W("a")], 3000 + NEEDS_REARM_MS);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
});

// Activity decided by the PTY-silence fallback, or by the agent's own status.
const Wp = (id) => ({ id, activity: "waiting", activity_source: "pty" });
const Bp = (id) => ({ id, activity: "working", activity_source: "pty" });
const Wa = (id) => ({ id, activity: "waiting", activity_source: "agent" });
const Ba = (id) => ({ id, activity: "working", activity_source: "agent" });

test("pty fallback: one waiting period fires once even if a working blip lands in between (#77)", () => {
  let r = needsTransitions(seeded(), [Wp("a")], 3000);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
  // A false "working" (a repaint read as work) is visible for 7-13 s.
  for (const t of [6000, 9000, 12000, 15000]) r = needsTransitions(r.next, [Bp("a")], t);
  r = needsTransitions(r.next, [Wp("a")], 18000);
  assert.deepEqual(r.fired, []);
  // ...and a second short blip right after is absorbed the same way.
  r = needsTransitions(r.next, [Bp("a")], 21000);
  r = needsTransitions(r.next, [Wp("a")], 24000);
  assert.deepEqual(r.fired, []);
});

test("pty fallback: the hysteresis is per session: another session's first wait still fires", () => {
  let r = needsTransitions(seeded(), [Wp("a"), Bp("b")], 3000);
  r = needsTransitions(r.next, [Bp("a"), Bp("b")], 6000);
  r = needsTransitions(r.next, [Wp("a"), Wp("b")], 9000);
  assert.deepEqual(r.fired.map((s) => s.id), ["b"]);
});

test("agent status: a second prompt 5 s after approving the first fires again", () => {
  // The agent's status flips exactly at turn boundaries: no blip to debounce.
  let r = needsTransitions(seeded(), [Wa("a")], 3000);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
  r = needsTransitions(r.next, [Ba("a")], 6000);
  r = needsTransitions(r.next, [Wa("a")], 9000);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
});

test("agent status: an 8 s turn ending in waiting fires again", () => {
  let r = needsTransitions(seeded(), [Wa("a")], 3000);
  for (const t of [6000, 9000]) r = needsTransitions(r.next, [Ba("a")], t);
  r = needsTransitions(r.next, [Wa("a")], 12000);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
});

test("a session that leaves the list forgets its waiting period", () => {
  let r = needsTransitions(seeded(), [W("a")], 3000);
  r = needsTransitions(r.next, [], 6000);
  r = needsTransitions(r.next, [W("a")], 9000);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
});

test("idle, done and unknown states never fire", () => {
  const r = needsTransitions(seeded(), [{ id: "a", activity: "idle" }, { id: "b", activity: "done" }, { id: "c" }], 3000);
  assert.deepEqual(r.fired, []);
});

test("notifyAvailability names the state: insecure, unsupported, denied or ready", () => {
  // UI plan 4 Task 8 (#174, F39): the row says WHY it is off, so the state is named, not only the switch.
  const at = (secure, perm) => { const a = notifyAvailability(secure, perm); return [a.enabled, a.state]; };
  assert.deepEqual(at(false, "default"), [false, "insecure"]);
  assert.deepEqual(at(true, "unsupported"), [false, "unsupported"]);
  assert.deepEqual(at(true, "denied"), [false, "denied"]);
  assert.deepEqual(at(true, "default"), [true, "ready"]);
  assert.deepEqual(at(true, "granted"), [true, "ready"]);
  // An http page is an http page whatever the browser says about Notification: HTTPS is the way out either way.
  assert.deepEqual(at(false, "unsupported"), [false, "insecure"]);
  assert.deepEqual(at(false, "denied"), [false, "insecure"]);
  // `reason` stays for a caller that reads it: the first sentence of the row's paragraph, and none when ready.
  assert.strictEqual(notifyAvailability(false, "default").reason, "Not available at this address.");
  assert.strictEqual(notifyAvailability(true, "denied").reason, "This browser has blocked notifications for this address.");
  assert.strictEqual(notifyAvailability(true, "unsupported").reason, "This browser does not support notifications.");
  assert.strictEqual(notifyAvailability(true, "default").reason, "");
});

test("notifyRowCopy, http origin: says it is not available here, names the page, and gives the two ways out (M8.1 a)", () => {
  const c = notifyRowCopy("insecure", false, "http://100.76.118.121:8899");
  assert.strictEqual(c.para, "Not available at this address. Browsers allow notifications only on https:// or localhost pages, and this page is http://100.76.118.121:8899.");
  assert.deepEqual(c.paraMono, ["https://", "http://100.76.118.121:8899"], "the parts of the paragraph that are machine text, in order");
  assert.strictEqual(c.heading, "How to enable");
  assert.strictEqual(c.helper, "");
  assert.strictEqual(c.checkAgain, false);
  assert.deepEqual(c.steps, [
    { text: "On the machine CuliBox runs on, open http://127.0.0.1:8899. Localhost counts as secure.", mono: ["http://127.0.0.1:8899"] },
    { text: "From another device, put HTTPS in front of CuliBox yourself, for example tailscale serve or your own reverse proxy, and open that https:// address.", mono: ["tailscale serve", "https://"] },
  ]);
  // The monospaced parts really are in the text, in the order the painter will meet them.
  for (const t of [{ text: c.para, mono: c.paraMono }, ...c.steps]) {
    let at = 0;
    for (const m of t.mono) { const i = t.text.indexOf(m, at); assert.ok(i >= 0, m + " is in: " + t.text); at = i + m.length; }
  }
  // Whether the user is opted in changes nothing on a page that cannot ask.
  assert.deepEqual(notifyRowCopy("insecure", true, "http://100.76.118.121:8899"), c);
});

test("notifyRowCopy, http origin: the localhost address keeps the page's port, and has none when the page has none", () => {
  const step = (origin) => notifyRowCopy("insecure", false, origin).steps[0].text;
  assert.strictEqual(step("http://100.76.118.121:8899"), "On the machine CuliBox runs on, open http://127.0.0.1:8899. Localhost counts as secure.");
  // No port (the default 80): http://127.0.0.1, not http://127.0.0.1:
  assert.strictEqual(step("http://box.example"), "On the machine CuliBox runs on, open http://127.0.0.1. Localhost counts as secure.");
  assert.strictEqual(step("http://box.example:80"), "On the machine CuliBox runs on, open http://127.0.0.1. Localhost counts as secure.");
  // Any other port, a name that is not an address, an IPv6 host: the port is the page's own.
  assert.strictEqual(step("http://box.tailnet.ts.net:3000"), "On the machine CuliBox runs on, open http://127.0.0.1:3000. Localhost counts as secure.");
  assert.strictEqual(step("http://[fd7a:115c::1]:8899"), "On the machine CuliBox runs on, open http://127.0.0.1:8899. Localhost counts as secure.");
  // A page that does not say where it is (or text that is not an origin) makes no claim about the port or the address.
  for (const o of ["", undefined, null, "null", "not a url"]) {
    const c = notifyRowCopy("insecure", false, o);
    assert.strictEqual(c.steps[0].text, "On the machine CuliBox runs on, open http://127.0.0.1. Localhost counts as secure.", String(o));
    assert.strictEqual(c.para, "Not available at this address. Browsers allow notifications only on https:// or localhost pages.", String(o));
    assert.deepEqual(c.paraMono, ["https://"], String(o));
  }
});

test("notifyRowCopy, blocked in the browser: only the browser can undo it, per browser, and Check again (M8.1 d)", () => {
  const c = notifyRowCopy("denied", false, "http://127.0.0.1:8899");
  assert.strictEqual(c.para, "This browser has blocked notifications for this address. Only the browser can undo that:");
  assert.strictEqual(c.heading, "");
  assert.strictEqual(c.helper, "");
  assert.strictEqual(c.checkAgain, true);
  assert.deepEqual(c.steps, [
    { lead: "Chrome, Edge:", text: "click the icon left of the address, set Notifications to Allow, then reload." },
    { lead: "Firefox:", text: "click the icon left of the address and clear the Notifications block, then reload." },
    { lead: "Safari:", text: "Safari \u203a Settings \u203a Websites \u203a Notifications, set this address to Allow." },
  ]);
  assert.deepEqual(c.paraMono, []);
});

test("notifyRowCopy, available: one line that says what it does and that it is per browser (M8.1 b, c)", () => {
  const on = "When this tab is not focused: blocked on you or done. This browser only.";
  const c = notifyRowCopy("ready", true, "https://box.example");
  assert.strictEqual(c.helper, on);
  assert.deepEqual([c.para, c.heading, c.steps, c.checkAgain, c.paraMono], ["", "", [], false, []]);
  // Off: the same line, then what turning it on does. Not "ended": that class does not fire today (my call 5).
  assert.strictEqual(notifyRowCopy("ready", false, "https://box.example").helper, on + " Your browser asks for permission when you turn it on.");
  assert.doesNotMatch(on, /ended/i);
  // Permission already granted and the switch off: turning it on asks nothing, so the line does not say it will.
  assert.strictEqual(notifyRowCopy("ready", false, "https://box.example", "granted").helper, on);
  assert.strictEqual(notifyRowCopy("ready", false, "https://box.example", "default").helper, on + " Your browser asks for permission when you turn it on.");
});

test("notifyRowCopy, no Notification in this browser: says so and nothing else", () => {
  const c = notifyRowCopy("unsupported", false, "https://box.example");
  assert.strictEqual(c.helper, "This browser does not support notifications.");
  assert.deepEqual([c.para, c.heading, c.steps, c.checkAgain], ["", "", [], false]);
});

test("notifyRowCopy is a pure function: the same input gives the same copy, and no call shares an object", () => {
  const a = notifyRowCopy("denied", false, "http://x:1");
  const b = notifyRowCopy("denied", false, "http://x:1");
  assert.deepEqual(a, b);
  a.steps.push({ text: "x" });
  assert.strictEqual(notifyRowCopy("denied", false, "http://x:1").steps.length, 3);
  assert.deepEqual(notifyRowCopy("nonsense", false, "http://x:1"), notifyRowCopy("unsupported", false, "http://x:1"), "an unknown state is the quiet one, not a thrown error");
});

test("the notification copy is the approved wording: no 'when an agent needs you', no 'Requires HTTPS'", () => {
  // F39/F47: both strings were wrong (the first promised more than fires today, the second gave no way out).
  const fs = require("node:fs");
  const path = require("node:path");
  for (const f of ["app.js", "attention.js"]) {
    const src = fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");
    assert.ok(!/when an agent needs you/i.test(src), f + " still says 'when an agent needs you'");
    assert.ok(!/Requires HTTPS/i.test(src), f + " still says 'Requires HTTPS'");
  }
});

test("notifyChecked is false unless permission is granted", () => {
  assert.strictEqual(notifyChecked(true, "granted"), true);
  assert.strictEqual(notifyChecked(true, "denied"), false);   // revoked in the browser after opting in
  assert.strictEqual(notifyChecked(true, "default"), false);
  assert.strictEqual(notifyChecked(false, "granted"), false);
});

test("notifyBody: an ask shows the tool NAME only, never the command", () => {
  // The full command must never reach OS notification history.
  assert.strictEqual(notifyBody({ need: "ask", ask: "Bash(rm -rf build)", summary: "x" }), "asks: Bash");
  assert.strictEqual(notifyBody({ need: "ask", ask: "WebFetch" }), "asks: WebFetch"); // no "(" — whole ask text
  assert.strictEqual(notifyBody({ need: "ask", ask: "", summary: "fallback" }), "asks: fallback");
  assert.strictEqual(notifyBody({ need: "ask" }), "Waiting for you");
});

test("notifyBody: a turn (or unknown need) keeps the full summary", () => {
  assert.strictEqual(notifyBody({ need: "turn", ask: "Bash(ls)", summary: "done" }), "done");
  assert.strictEqual(notifyBody({ summary: "done" }), "done");
  assert.strictEqual(notifyBody({}), "Waiting for you");
});

test("notifyPoll: a call before the first successful list does not seed prevIds", () => {
  // Simulates initEnv's title refresh firing before /api/sessions has ever
  // resolved: loaded=false must hand prevIds back untouched (no seeding off
  // the empty lastSessions placeholder) and must never report a transition.
  let r = notifyPoll(false, null, [], 0);
  assert.deepEqual(r, { next: null, fired: [] });
  r = notifyPoll(false, null, [W("a")], 0); // even if a stray list were passed
  assert.deepEqual(r, { next: null, fired: [] });
  // Once the real list lands (loaded flips true), the real first poll still
  // seeds normally — nothing was consumed by the not-loaded calls above.
  r = notifyPoll(true, r.next, [W("a"), B("b")], 3000);
  assert.deepEqual(r.fired, []);
  assert.deepEqual([...r.next.ids], ["a"]);
});

test("notifyPoll: once loaded, behaves exactly like needsTransitions", () => {
  const r = notifyPoll(true, seeded(), [W("a")], 3000);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
});

// Known vectors (FIPS 180-2 + the padding boundaries 55/56/64 bytes) and the
// cross-language vector Go's TestAnswerHashMatchesTheWebClientVector pins.
test("sha256Hex matches known vectors", () => {
  const v = [
    ["", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"],
    ["abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"],
    ["abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq", "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"],
    ["a".repeat(55), "9f4390f8d30c2dd92ec9f095b65e2b9ae9b0a925a5258e241c9f1e910f734318"],
    ["a".repeat(56), "b35439a4ac6f0948b6d6f9e3c6af0f5f590ce20f1bde7090ef7970686ec6738a"],
    ["a".repeat(64), "ffe054fe7ae0cb6dc65c3af9b61d5209f439851db43d0ba5997337df154668eb"],
    ["x".repeat(4096), "a2e659dacb4691e887ac0139f8893d04764ee197d70fb73d3190d56113d18e3e"],
    ["Bash(rm -rf build) — é ü 漢 😀", "b8c0c145bfbc2ff37428ec2d15dc9ac3c6bf196fa6e8340756f91ec23d84166a"],
    ["Bash(echo 'it'\"s\" $(date) `id`)", "680035fbd7b3950cf3c89f1d80a39c5ecc5f8ed78cd8bfdd105ad8eece43046f"],
  ];
  for (const [input, want] of v) assert.strictEqual(sha256Hex(input), want, input.slice(0, 20));
});

test("answerState: Yes/No unlock only once the full command on screen was expanded", () => {
  const ask = { need: "ask", replyable: true, claude: true, activity: "waiting", ask_full: "Bash(touch a)" };
  // F51: the reason Yes/No are disabled is a visible line, and the tooltip keeps it too.
  assert.deepStrictEqual(answerState(ask, false, undefined),
    { show: true, enabled: false, title: "Expand to see the full command first", note: "", reason: "Expand to see the full command first" });
  assert.deepStrictEqual(answerState(ask, true, "Bash(touch a)"), { show: true, enabled: true, title: "", note: "", reason: "" });
  // Changed underneath (the next prompt arrived while expanded): read it again.
  const changed = answerState(ask, true, "Bash(touch b)");
  assert.strictEqual(changed.enabled, false);
  assert.match(changed.title, /changed/);
  assert.strictEqual(changed.reason, changed.title, "a disabled Yes/No always says why on screen");
  assert.strictEqual(answerState({ ...ask, replyable: false }, true, "Bash(touch a)").show, false);
  const tooLong = answerState({ ...ask, ask_full: undefined }, true, "");
  assert.strictEqual(tooLong.show, false);
  assert.match(tooLong.note, /terminal/);
  assert.strictEqual(tooLong.reason, "", "the note is that case's visible line");
  assert.deepStrictEqual(answerState({ ...ask, need: "turn" }, true, "Bash(touch a)"), { show: false, enabled: false, title: "", note: "", reason: "" });
  assert.strictEqual(answerState(null, true, "").show, false);
});

test("answerState: with remote replies off an ask says why it has no Yes/No (F50)", () => {
  const ask = { need: "ask", replyable: true, claude: true, activity: "waiting", ask_full: "Bash(touch a)" };
  const prev = globalThis.repliesEnabled;
  try {
    globalThis.repliesEnabled = false;
    const want = { show: false, enabled: false, title: "", note: "", reason: "Answer in the terminal · remote replies are off" };
    assert.deepStrictEqual(answerState(ask, false, undefined), want);
    assert.deepStrictEqual(answerState(ask, true, "Bash(touch a)"), want, "expanded or not");
    // The kill switch closes the path whatever the session's own opt-in says.
    assert.deepStrictEqual(answerState({ ...ask, replyable: false }, false, undefined), want);
    assert.deepStrictEqual(answerState({ ...ask, ask_full: undefined }, false, undefined), want);
    // Only an ask has a Yes/No to explain: a turn or no session says nothing.
    assert.strictEqual(answerState({ ...ask, need: "turn" }, false, undefined).reason, "");
    assert.strictEqual(answerState(null, false, undefined).reason, "");
    // The page has not heard from /api/meta yet (undefined): the paths are closed, but nobody
    // knows the switch is off, so the strip must not claim it.
    delete globalThis.repliesEnabled;
    assert.deepStrictEqual(answerState(ask, false, undefined), { show: false, enabled: false, title: "", note: "", reason: "" });
  } finally {
    globalThis.repliesEnabled = prev;
  }
});

test("ssoDoneKeep: the sign-in line stays until the session goes from not working to working (F63)", () => {
  // An agent that was working at sign-in keeps the line while it keeps working.
  assert.strictEqual(ssoDoneKeep("working", "working", false), true);
  // Stopped (any not-working state) keeps it; the agent has not started again.
  assert.strictEqual(ssoDoneKeep("working", "waiting", false), true);
  assert.strictEqual(ssoDoneKeep("working", "idle", false), true);
  assert.strictEqual(ssoDoneKeep("waiting", "waiting", false), true);
  assert.strictEqual(ssoDoneKeep("idle", "idle", false), true);
  // Not working -> working: the agent took it from here.
  assert.strictEqual(ssoDoneKeep("idle", "working", false), false);
  assert.strictEqual(ssoDoneKeep("waiting", "working", false), false);
  assert.strictEqual(ssoDoneKeep("", "working", false), false);
  assert.strictEqual(ssoDoneKeep(undefined, "working", false), false);
  // The session ended: nothing left to retry.
  assert.strictEqual(ssoDoneKeep("working", "working", true), false);
  assert.strictEqual(ssoDoneKeep("waiting", "waiting", true), false);
  assert.strictEqual(ssoDoneKeep("waiting", "done", false), false, "activity done is an exited session too");
  assert.strictEqual(ssoDoneKeep("waiting", "crashed", false), false);
});

test("answerErrorText words each refusal", () => {
  assert.strictEqual(answerErrorText(409, "the prompt changed"), "The prompt changed — review it again");
  assert.match(answerErrorText(409, "session is not waiting on a prompt"), /no longer waiting/);
  assert.match(answerErrorText(429, ""), /too soon/i);
  assert.match(answerErrorText(403, ""), /not enabled/);
  assert.match(answerErrorText(410, ""), /gone/);
  assert.match(answerErrorText(0, ""), /no connection/);
  assert.match(answerErrorText(500, ""), /500/);
});

test("an ask card offers Enable replies when the claude session has not opted in", () => {
  const c = cardContent({ activity: "waiting", need: "ask", ask: "Bash(x)", claude: true, replyable: false });
  assert.strictEqual(c.enableReplies, true);
  assert.strictEqual(c.reply, false);
  assert.strictEqual(cardContent({ activity: "waiting", need: "ask", ask: "Bash(x)", claude: true, replyable: true }).enableReplies, false);
});

test("staleExpanded: gone or no-longer-waiting is always stale", () => {
  assert.strictEqual(staleExpanded(undefined, "Bash(x)"), true);
  assert.strictEqual(staleExpanded(null, undefined), true);
  assert.strictEqual(staleExpanded({ activity: "working", need: "ask", ask_full: "Bash(x)" }, "Bash(x)"), true);
});

test("staleExpanded: ask_full changed since expand is stale; unchanged is not", () => {
  const s = { activity: "waiting", need: "ask", ask_full: "Bash(touch a)" };
  assert.strictEqual(staleExpanded(s, "Bash(touch a)"), false);
  assert.strictEqual(staleExpanded(s, "Bash(touch b)"), true);
});

test("staleExpanded: a turn card that gains an ask since expand is stale", () => {
  // Expanded as a turn card (no ask_full to track, so cardAskSeen recorded
  // ""); the very next poll shows the same session now needing an ask — the
  // new prompt has not been read, so it must collapse.
  const nowAsk = { activity: "waiting", need: "ask", ask_full: "Bash(rm -rf x)" };
  assert.strictEqual(staleExpanded(nowAsk, ""), true);
});

test("staleExpanded: no seenAskFull recorded never stales on ask_full alone", () => {
  const s = { activity: "waiting", need: "turn", summary: "done" };
  assert.strictEqual(staleExpanded(s, undefined), false);
});

test("maskHint names a display mask only when something was masked", () => {
  assert.strictEqual(maskHint("plain"), "");
  assert.strictEqual(maskHint(undefined), "");
  assert.strictEqual(maskHint(null), "");
  assert.strictEqual(maskHint("a [masked-ish] thing"), "");
  assert.strictEqual(maskHint("key [masked:aws-key-id] here"), "Display mask: the agent and its transcript still hold the original.");
});

test("a masked ask reaches the notification as the tool name only", () => {
  assert.strictEqual(notifyBody({ need: "ask", ask: "Bash(echo [masked:aws-key-id])" }), "asks: Bash");
  assert.strictEqual(cardContent({ need: "ask", ask: "Bash(echo [masked:aws-key-id])" }).text, "Bash(echo [masked:aws-key-id])");
});

test("the hint follows the shown text, not ask_full: the real command is never the masked one", () => {
  const s = { need: "ask", ask: "Bash(echo [masked:aws-key-id])", ask_full: "echo AKIAABCDEFGHIJKLMNOP" };
  assert.notStrictEqual(maskHint(cardContent(s).text), "");
  assert.strictEqual(maskHint(s.ask_full), "");
});

test("an SSO need: needs you, labelled, its own card, its own notification text", () => {
  const s = { id: "aws-1", activity: "working", need: "sso", aws: { sso_expired: "example-sso" } };
  assert.ok(needsYou(s));
  assert.ok(!needsYou({ activity: "working" }));
  assert.ok(needsYou({ activity: "waiting" }));
  assert.ok(!needsYou(null));
  assert.strictEqual(needLabel(s), "AWS sign-in");
  assert.deepStrictEqual(cardContent(s), { text: "AWS sign-in expired (example-sso). This session's AWS calls fail until you sign in.",
    sso: true, ask: false, reply: false, enableReplies: false });
  assert.strictEqual(notifyBody(s), "AWS sign-in expired");
  const first = needsTransitions(null, [s], 0);
  assert.deepStrictEqual([...first.next.ids], ["aws-1"]);
  const t = needsTransitions(seeded(), [s], 3000);
  assert.deepStrictEqual(t.fired.map((x) => x.id), ["aws-1"]);
  assert.strictEqual(staleExpanded(s, undefined), false);
  assert.strictEqual(staleExpanded({ ...s, need: undefined }, undefined), true); // sign-in done and agent running: stale
});

test("an SSO card keeps the reply box of a replyable session (spec 8.2)", () => {
  const s = { id: "aws-1", activity: "waiting", need: "sso", claude: true, replyable: true, summary: "Done with the plan", aws: { sso_expired: "example-sso" } };
  const c = cardContent(s);
  assert.strictEqual(c.sso, true);
  assert.strictEqual(c.reply, true);
  assert.strictEqual(c.enableReplies, false);
  assert.strictEqual(cardContent({ ...s, replyable: false }).reply, false);
  assert.ok(!cardContent(s).text.includes("Done with the plan")); // the SSO sentence, not the turn summary
});

test("tabNeedWhy is sso only while the agent itself is not waiting", () => {
  assert.strictEqual(tabNeedWhy({ activity: "working", need: "sso" }), "sso");
  assert.strictEqual(tabNeedWhy({ activity: "idle", need: "sso" }), "sso");
  assert.strictEqual(tabNeedWhy({ activity: "waiting", need: "sso" }), "");
  assert.strictEqual(tabNeedWhy({ activity: "waiting", need: "ask" }), "");
  assert.strictEqual(tabNeedWhy({ activity: "working" }), "");
  assert.ok(needsYou({ activity: "working", need: "sso" }), "still needs-you everywhere");
  assert.strictEqual(tabNeedWhy(null), "");
});

test("sessionDot: an SSO session reads as waiting, with its reason, on every dot", () => {
  assert.deepStrictEqual(sessionDot({ activity: "idle", need: "sso" }), { cls: "dot waiting", title: "AWS sign-in needed" });
  assert.deepStrictEqual(sessionDot({ activity: "working", need: "sso" }), { cls: "dot waiting", title: "AWS sign-in needed" });
  assert.deepStrictEqual(sessionDot({ activity: "waiting", need: "turn" }), { cls: "dot waiting", title: "waiting for input" });
  assert.deepStrictEqual(sessionDot({ activity: "idle" }), { cls: "dot idle", title: "idle" });
});

// Kill-switch: the reply gates close unless globalThis.repliesEnabled is the boolean true.
for (const off of [undefined, "true"]) {
  test("reply gates in attention.js are closed when repliesEnabled is " + String(off), () => {
    const turn = { activity: "waiting", need: "turn", claude: true, replyable: true, summary: "done" };
    const turnNo = { activity: "waiting", need: "turn", claude: true, replyable: false, summary: "done" };
    const ask = { activity: "waiting", need: "ask", ask: "Bash(x)", claude: true, replyable: false };
    const askYes = { activity: "waiting", need: "ask", ask: "Bash(x)", claude: true, replyable: true };
    const sso = { activity: "waiting", need: "sso", claude: true, replyable: true, aws: { sso_expired: "dev" } };
    const onText = [turn, turnNo, ask, askYes, sso].map((s) => cardContent(s).text);
    if (off === undefined) delete globalThis.repliesEnabled; else globalThis.repliesEnabled = off;
    try {
      [turn, turnNo, ask, askYes, sso].forEach((s, i) => {
        const c = cardContent(s);
        assert.strictEqual(c.reply, false, "reply " + i);
        assert.strictEqual(c.enableReplies, false, "enableReplies " + i);
        assert.strictEqual(c.text, onText[i], "text " + i);
      });
      assert.strictEqual(cardContent(sso).sso, true);
      assert.strictEqual(cardContent(ask).ask, true);
      const a = answerState({ need: "ask", replyable: true, ask_full: "x" }, true, "x");
      assert.strictEqual(a.show, false);
      assert.strictEqual(a.enabled, false);
      assert.strictEqual(a.note, "");
    } finally {
      globalThis.repliesEnabled = true;
    }
  });
}

// --- #155 named AWS connections ---------------------------------------------
test("an expired sign-in on any connection needs you, whatever the agent is doing", () => {
  const ok = { name: "a", sso_expired: "" };
  const bad = { name: "b", sso_expired: "corp" };
  assert.ok(needsYou({ activity: "working", aws_connections: [ok, bad] }));
  assert.ok(!needsYou({ activity: "working", aws_connections: [ok] }));
  assert.ok(!needsYou({ activity: "working", aws_connections: [] }));
  assert.strictEqual(tabNeedWhy({ activity: "working", aws_connections: [ok, bad] }), "sso");
  assert.strictEqual(tabNeedWhy({ activity: "waiting", aws_connections: [ok, bad] }), "");
  assert.strictEqual(tabNeedWhy({ activity: "working", aws_connections: [ok] }), "");
});

test("the SSO card text names the expired sources of a connections session", () => {
  const s = { need: "sso", activity: "working", aws_connections: [{ name: "b", sso_expired: "corp" }, { name: "a", sso_expired: "corp" }, { name: "c", sso_expired: "dev" }, { name: "d" }] };
  assert.strictEqual(cardContent(s).text, "AWS sign-in expired (corp, dev). This session's AWS calls fail until you sign in.");
  assert.strictEqual(cardContent({ need: "sso", activity: "working", aws_connections: [{ name: "a", sso_expired: "corp" }] }).sso, true);
  // the legacy sentence is unchanged
  assert.strictEqual(cardContent({ need: "sso", activity: "working", aws: { sso_expired: "example-sso" } }).text,
    "AWS sign-in expired (example-sso). This session's AWS calls fail until you sign in.");
});

test("store_locked needs you, even while the agent is working", () => {
  assert.strictEqual(needsYou({ activity: "working", need: "store_locked" }), true);
  assert.strictEqual(needsYou({ activity: "working" }), false);
});

test("store_locked card: the Store locked title and an unlock marker, no reply path", () => {
  const c = cardContent({ activity: "working", need: "store_locked", claude: true, replyable: true });
  assert.strictEqual(c.title, "Store locked");
  assert.strictEqual(c.unlock, true);
  assert.strictEqual(c.ask, false);
  assert.strictEqual(c.reply, false);
  assert.strictEqual(c.enableReplies, false);
  assert.match(c.text, /unlock/i);
});

test("sso outranks store_locked for which card shows; label and tab dot name the store", () => {
  assert.strictEqual(cardContent({ activity: "working", need: "sso" }).sso, true);
  assert.strictEqual(needLabel({ activity: "working", need: "store_locked" }), "store locked");
  assert.strictEqual(tabNeedWhy({ activity: "working", need: "store_locked" }), "store");
  assert.strictEqual(tabNeedWhy({ activity: "waiting", need: "store_locked" }), "");
  assert.strictEqual(sessionDot({ activity: "working", need: "store_locked" }).title, "Store locked");
});

// attentionClass: how a session asks for the operator on the desktop sidebar.
// The Go side (internal/session/attention.go) and this function read ONE fixture,
// so the two cannot drift: the server's field, and the client's own derivation
// for a payload without it.
const attentionCases = JSON.parse(fs.readFileSync(path.join(__dirname, "../../session/testdata/attention-cases.json"), "utf8"));

test("attentionClass: the shared fixture has rows to check", () => {
  assert.ok(attentionCases.length >= 10);
});

for (const c of attentionCases) {
  test("attentionClass derives '" + c.want + "' without the server field: " + c.name, () => {
    const info = { ...c.info };
    delete info.attention;
    assert.strictEqual(attentionClass(info), c.want);
  });
  test("attentionClass reads the server field when it is sent: " + c.name, () => {
    // The server omits the field when it is "", so a row that wants "" sends none.
    const info = { ...c.info };
    if (c.want) info.attention = c.want;
    assert.strictEqual(attentionClass(info), c.want);
  });
}

test("attentionClass: the server's word wins over the client's reading, only for block and done", () => {
  assert.strictEqual(attentionClass({ activity: "working", attention: "block" }), "block");
  assert.strictEqual(attentionClass({ activity: "waiting", need: "ask", attention: "done" }), "done");
  assert.strictEqual(attentionClass({ activity: "waiting", need: "ask", attention: "nonsense" }), "block", "an unknown word falls back to deriving");
});

test("attentionClass: an expired connection sign-in blocks even with no need on the record (older payload)", () => {
  assert.strictEqual(attentionClass({ activity: "working", aws_connections: [{ name: "a", sso_expired: "acme" }] }), "block");
  assert.strictEqual(attentionClass({ activity: "waiting", need: "turn", aws_connections: [{ name: "a", sso_expired: "acme" }] }), "block", "sign-in outranks a finished turn");
});

test("attentionClass: nothing, or a session that is not there, is ''", () => {
  assert.strictEqual(attentionClass(null), "");
  assert.strictEqual(attentionClass(undefined), "");
  assert.strictEqual(attentionClass({}), "");
});
