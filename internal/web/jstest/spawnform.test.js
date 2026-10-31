"use strict";
// Replies are ON for the assertions below; the kill-switch group at the end turns them off.
globalThis.repliesEnabled = true;
const test = require("node:test");
const assert = require("node:assert");
const {
  BYPASS_OPTION_KEY, isClaudeBinName, promptFieldVisible, repliesFieldVisible, bypassWarningShown,
  runtimeSegChoices, spawnPayload, spawnPrefill, isSubmitHotkey, spawnProgressText,
} = require("../static/spawnform.js");
const { spawnRuntimeDefault } = require("../static/tier.js");

const claude = { name: "claude", accepts_prompt: true, claude: true, replyable: false };
const shell = { name: "bash" };

test("isClaudeBinName mirrors launcher.IsClaudeBin", () => {
  assert.strictEqual(isClaudeBinName("claude"), true);
  assert.strictEqual(isClaudeBinName("/usr/local/bin/claude"), true);
  assert.strictEqual(isClaudeBinName(" claude "), true);
  assert.strictEqual(isClaudeBinName("claude-wrapper"), false);
  assert.strictEqual(isClaudeBinName(""), false);
  assert.strictEqual(isClaudeBinName(undefined), false);
});

test("the first-message field and the replies toggle follow the profile", () => {
  assert.strictEqual(promptFieldVisible(claude), true);
  assert.strictEqual(promptFieldVisible({ claude: true }), false); // codex-like: no first message
  assert.strictEqual(promptFieldVisible(shell), false);
  assert.strictEqual(promptFieldVisible(undefined), false);
  assert.strictEqual(repliesFieldVisible(claude), true);
  assert.strictEqual(repliesFieldVisible(shell), false);
});

test("the bypass warning shows only when replies are on for a bypass session", () => {
  assert.strictEqual(bypassWarningShown(claude, { [BYPASS_OPTION_KEY]: true }, true), true);
  assert.strictEqual(bypassWarningShown(claude, { [BYPASS_OPTION_KEY]: true }, false), false);
  assert.strictEqual(bypassWarningShown(claude, { [BYPASS_OPTION_KEY]: false }, true), false);
  assert.strictEqual(bypassWarningShown({ ...claude, bypass_args: true }, {}, true), true);
  assert.strictEqual(bypassWarningShown(shell, { [BYPASS_OPTION_KEY]: true }, true), false);
});

// Criterion 7: the effective default is always one of the seg's own values, so
// a button is selected on open — never a blank unselected pair.
test("the containment default always matches a seg button", () => {
  const values = runtimeSegChoices().map((c) => c.value);
  assert.deepStrictEqual(values, ["box", "host"]);
  for (const p of [{ runtime: "box" }, { runtime: "host" }, {}, undefined, { runtime: "weird" }]) {
    assert.ok(values.includes(spawnRuntimeDefault(p, "runc")), JSON.stringify(p));
  }
  assert.strictEqual(spawnRuntimeDefault({}, ""), ""); // no box layer: the row is hidden, nothing sent
  assert.strictEqual(runtimeSegChoices()[1].danger, true);
});

test("spawnPayload carries prompt and replies only where they apply", () => {
  const base = { agent: "claude", name: "  api  ", cwd: "/w", options: { effort: "low" }, runtime: "box" };
  assert.deepStrictEqual(spawnPayload({ ...base, prompt: "  fix it  ", replyable: true, profile: claude }),
    { agent: "claude", name: "api", cwd: "/w", options: { effort: "low" }, runtime: "box", prompt: "fix it", replyable: true });
  assert.deepStrictEqual(spawnPayload({ ...base, prompt: "   ", replyable: false, profile: claude }),
    { agent: "claude", name: "api", cwd: "/w", options: { effort: "low" }, runtime: "box", replyable: false });
  // A shell profile: a stale first message or toggle never reaches the server.
  assert.deepStrictEqual(spawnPayload({ ...base, agent: "bash", runtime: "", prompt: "ls -la now", replyable: true, profile: shell }),
    { agent: "bash", name: "api", cwd: "/w", options: { effort: "low" } });
});

test("spawnPrefill copies profile, cwd, runtime and options — never the name or a first message", () => {
  const s = { id: "claude-3", agent: "claude", name: "api", cwd: "/w", runtime: "host", options: { effort: "low" }, summary: "x" };
  const pf = spawnPrefill(s);
  assert.deepStrictEqual(pf, { agent: "claude", cwd: "/w", runtime: "host", account: "", options: { effort: "low" } });
  pf.options.effort = "high";
  assert.strictEqual(s.options.effort, "low", "the prefill must not alias the session's options");
  assert.deepStrictEqual(spawnPrefill({}), { agent: "", cwd: "", runtime: "", account: "", options: {} });
});

test("Ctrl+Enter (⌘+Enter on mac) spawns; plain Enter, AltGr and IME composition do not", () => {
  const ev = (m) => Object.assign({ key: "Enter", ctrlKey: false, metaKey: false, altKey: false, isComposing: false }, m);
  assert.strictEqual(isSubmitHotkey(ev({ ctrlKey: true }), false), true);
  assert.strictEqual(isSubmitHotkey(ev({ metaKey: true }), true), true);
  assert.strictEqual(isSubmitHotkey(ev({}), false), false);
  assert.strictEqual(isSubmitHotkey(ev({ ctrlKey: true, altKey: true }), false), false);
  assert.strictEqual(isSubmitHotkey(ev({ ctrlKey: true, isComposing: true }), false), false);
  assert.strictEqual(isSubmitHotkey(ev({ metaKey: true }), false), false);
});

test("spawnPayload sends any chosen account (even default, to override a pinned profile); omits an unset one", () => {
  assert.strictEqual(spawnPayload({ agent: "c", account: "work" }).account, "work");
  assert.strictEqual(spawnPayload({ agent: "c", account: "default" }).account, "default");
  assert.strictEqual("account" in spawnPayload({ agent: "c", account: "" }), false);
  assert.strictEqual("account" in spawnPayload({ agent: "c" }), false);
  assert.strictEqual(spawnPrefill({ agent: "c", account: "work" }).account, "work");
});

test("spawnPayload sends login:true only when set (the Settings Log in session)", () => {
  assert.strictEqual(spawnPayload({ agent: "c", account: "work", login: true }).login, true);
  assert.strictEqual("login" in spawnPayload({ agent: "c", account: "work" }), false);
  assert.strictEqual("login" in spawnPayload({ agent: "c", login: false }), false);
});

// Kill-switch: the replies field closes unless globalThis.repliesEnabled is the boolean true.
for (const off of [undefined, "true"]) {
  test("replies field is hidden when repliesEnabled is " + String(off), () => {
    if (off === undefined) delete globalThis.repliesEnabled; else globalThis.repliesEnabled = off;
    try {
      assert.strictEqual(repliesFieldVisible({ claude: true }), false);
      assert.strictEqual(bypassWarningShown(Object.assign({}, claude, { bypass_args: true }), {}, true), false);
    } finally {
      globalThis.repliesEnabled = true;
    }
  });
}

test("spawnRefusedLocked: only a 423 carrying the store_locked code", () => {
  const { spawnRefusedLocked } = require("../static/spawnform.js");
  assert.strictEqual(spawnRefusedLocked(423, "store_locked"), true);
  assert.strictEqual(spawnRefusedLocked(423, ""), false);
  assert.strictEqual(spawnRefusedLocked(400, "store_locked"), false);
  assert.strictEqual(spawnRefusedLocked(undefined, undefined), false);
});

// #36: a spawn in flight says so, with how long it has waited; a boxed spawn
// that is not instant says why (the box VM/container may be starting).
test("spawnProgressText counts up and explains a slow boxed spawn", () => {
  assert.strictEqual(spawnProgressText(0, "box"), "Starting session…");
  assert.strictEqual(spawnProgressText(2, "box"), "Starting session… 2s");
  assert.match(spawnProgressText(3, "box"), /^Starting session… 3s · starting the box/);
  assert.strictEqual(spawnProgressText(12, "host"), "Starting session… 12s");
  assert.strictEqual(spawnProgressText(65, "box").split(" · ")[0], "Starting session… 1m 05s");
});

// Profile search (spec 5.5): case-insensitive substring on name or touch line.
test("profileMatches: name, touch line, case, blank query", () => {
  const { profileMatches, PLIST_SEARCH_MIN } = require("../static/spawnform.js");
  assert.strictEqual(PLIST_SEARCH_MIN, 8);
  assert.strictEqual(profileMatches("agent-box", "host · not contained", "BOX"), true);
  assert.strictEqual(profileMatches("culifeed", "box · aws prod", "aws"), true);
  assert.strictEqual(profileMatches("zsh", "host · zsh", "kube"), false);
  assert.strictEqual(profileMatches("x", "y", "  "), true);
  assert.strictEqual(profileMatches("x", "y", ""), true);
  assert.strictEqual(profileMatches("x", "y", undefined), true);
  assert.strictEqual(profileMatches("a b", "y", " a b "), true);
  assert.strictEqual(profileMatches(undefined, undefined, "q"), false);
});
