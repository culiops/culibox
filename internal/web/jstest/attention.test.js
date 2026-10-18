"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { needsYou, sessionDot, tabNeedWhy, needLabel, cardContent, attentionTitle, faviconHref, needsTransitions, notifyAvailability, notifyChecked, notifyBody, notifyPoll, sha256Hex, answerState, answerErrorText, staleExpanded, maskHint } = require("../static/attention.js");

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
  assert.strictEqual(attentionTitle(0, "DEV"), "[DEV] Agent Box");
  assert.strictEqual(attentionTitle(2, "DEV"), "(2) [DEV] Agent Box");
  assert.strictEqual(attentionTitle(1, ""), "(1) Agent Box");
  assert.strictEqual(attentionTitle(0, ""), "Agent Box");
});

test("faviconHref flips to the amber-dot icon while anything needs you", () => {
  assert.strictEqual(faviconHref(0), "/favicon.svg");
  assert.strictEqual(faviconHref(3), "/favicon-needs.svg");
});

const W = (id) => ({ id, activity: "waiting" });
const B = (id) => ({ id, activity: "working" });

test("the first poll after load only seeds — no notification for already-waiting sessions", () => {
  const r = needsTransitions(null, [W("a"), B("b")]);
  assert.deepEqual(r.fired, []);
  assert.deepEqual([...r.next], ["a"]);
});

test("a session that becomes waiting fires exactly once", () => {
  let r = needsTransitions(new Set(), [W("a")]);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
  r = needsTransitions(r.next, [W("a")]);
  assert.deepEqual(r.fired, []);
});

test("waiting -> working -> waiting fires again", () => {
  let r = needsTransitions(new Set(["a"]), [B("a")]);
  assert.deepEqual(r.fired, []);
  r = needsTransitions(r.next, [W("a")]);
  assert.deepEqual(r.fired.map((s) => s.id), ["a"]);
});

test("idle, done and unknown states never fire", () => {
  const r = needsTransitions(new Set(), [{ id: "a", activity: "idle" }, { id: "b", activity: "done" }, { id: "c" }]);
  assert.deepEqual(r.fired, []);
});

test("notifyAvailability: insecure origin disables with the HTTPS hint", () => {
  const a = notifyAvailability(false, "default");
  assert.strictEqual(a.enabled, false);
  assert.match(a.reason, /Requires HTTPS — open via agent-box\.culiops\.dev/);
  assert.strictEqual(notifyAvailability(true, "unsupported").enabled, false);
  assert.deepEqual(notifyAvailability(true, "default"), { enabled: true, reason: "" });
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
  let r = notifyPoll(false, null, []);
  assert.deepEqual(r, { next: null, fired: [] });
  r = notifyPoll(false, null, [W("a")]); // even if a stray list were passed
  assert.deepEqual(r, { next: null, fired: [] });
  // Once the real list lands (loaded flips true), the real first poll still
  // seeds normally — nothing was consumed by the not-loaded calls above.
  r = notifyPoll(true, r.next, [W("a"), B("b")]);
  assert.deepEqual(r.fired, []);
  assert.deepEqual([...r.next], ["a"]);
});

test("notifyPoll: once loaded, behaves exactly like needsTransitions", () => {
  const r = notifyPoll(true, new Set(), [W("a")]);
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
  assert.deepStrictEqual(answerState(ask, false, undefined),
    { show: true, enabled: false, title: "Expand to see the full command first", note: "" });
  assert.deepStrictEqual(answerState(ask, true, "Bash(touch a)"), { show: true, enabled: true, title: "", note: "" });
  // Changed underneath (the next prompt arrived while expanded): read it again.
  const changed = answerState(ask, true, "Bash(touch b)");
  assert.strictEqual(changed.enabled, false);
  assert.match(changed.title, /changed/);
  assert.strictEqual(answerState({ ...ask, replyable: false }, true, "Bash(touch a)").show, false);
  const tooLong = answerState({ ...ask, ask_full: undefined }, true, "");
  assert.strictEqual(tooLong.show, false);
  assert.match(tooLong.note, /terminal/);
  assert.deepStrictEqual(answerState({ ...ask, need: "turn" }, true, "Bash(touch a)"), { show: false, enabled: false, title: "", note: "" });
  assert.strictEqual(answerState(null, true, "").show, false);
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
  const first = needsTransitions(null, [s]);
  assert.deepStrictEqual([...first.next], ["aws-1"]);
  const t = needsTransitions(new Set(), [s]);
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
