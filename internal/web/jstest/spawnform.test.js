"use strict";
// Replies are ON for the assertions below; the kill-switch group at the end turns them off.
globalThis.repliesEnabled = true;
const test = require("node:test");
const assert = require("node:assert");
const {
  BYPASS_OPTION_KEY, isClaudeBinName, promptFieldVisible, repliesFieldVisible, bypassWarningShown,
  runtimeSegChoices, spawnPayload, spawnPrefill, isSubmitHotkey, spawnProgressText,
  PERM_MODE_LABELS, PERM_MODE_LINES, CRIT_RM_COPY, permModeLabel, permModeLine, enumOwnsLegacyBypass, legacyBypassKeyFromDefs, legacyKeysOwnedByEnum, effectiveMode, effectiveBypass, critRmView,
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

// Permission mode (#342): the founder-approved words, exact.
test("permModeLabel and permModeLine give the approved words, and a raw label for an unknown choice", () => {
  assert.deepStrictEqual(["manual", "acceptEdits", "auto", "plan", "bypassPermissions"].map(permModeLabel), ["Manual", "Accept edits", "Auto", "Plan", "Bypass"]);
  assert.deepStrictEqual(PERM_MODE_LABELS, { manual: "Manual", acceptEdits: "Accept edits", auto: "Auto", plan: "Plan", bypassPermissions: "Bypass" });
  assert.strictEqual(permModeLine(""), "Claude Code's default. On current versions that is Auto.");
  assert.strictEqual(permModeLine("manual"), "Asks you before edits and commands.");
  assert.strictEqual(permModeLine("acceptEdits"), "Edits files without asking. Still asks before other commands.");
  assert.strictEqual(permModeLine("auto"), "A classifier reviews each action and blocks risky ones. Asks you less.");
  assert.strictEqual(permModeLine("plan"), "Reads and plans only. Changes nothing until you approve the plan.");
  assert.strictEqual(permModeLine("bypassPermissions"), "Runs every action without asking.");
  assert.strictEqual(Object.keys(PERM_MODE_LINES).length, 6);
  // A config.json def can offer other choices: the label is the choice as written, and it has no line.
  assert.strictEqual(permModeLabel("default"), "default");
  assert.strictEqual(permModeLine("default"), "");
  // Names on Object.prototype are not choices.
  assert.strictEqual(permModeLabel("toString"), "toString");
  assert.strictEqual(permModeLine("toString"), "");
});

test("CRIT_RM_COPY holds the approved sentences", () => {
  assert.strictEqual(CRIT_RM_COPY.detail, "In Bypass, Claude Code still stops before an rm of the working folder, ~ or /, waits 2 minutes, then refuses. This turns that stop off.");
  assert.strictEqual(CRIT_RM_COPY.needsBypass, "Needs Permission mode: Bypass");
  assert.strictEqual(CRIT_RM_COPY.clone, "The agent works on its own copy of the repo.");
  assert.strictEqual(CRIT_RM_COPY.inPlace, "Your own folder is mounted live. With this on, the agent can delete it, and everything in it, without asking.");
  assert.strictEqual(CRIT_RM_COPY.legacy("Bypass permissions"), "Set by Bypass permissions (older form)");
  assert.strictEqual(CRIT_RM_COPY.args, "This profile's own arguments also turn Bypass on.");
});

test("effectiveMode: the enum wins, else the legacy bool reads as Bypass", () => {
  assert.strictEqual(effectiveMode({ skip: true }, "skip"), "bypassPermissions");
  assert.strictEqual(effectiveMode({ skip: true, permission_mode: "plan" }, "skip"), "plan");
  assert.strictEqual(effectiveMode({ skip: true, permission_mode: "" }, "skip"), "bypassPermissions");
  assert.strictEqual(effectiveMode({ skip: false }, "skip"), "");
  assert.strictEqual(effectiveMode({ skip: true }, ""), "");
  assert.strictEqual(effectiveMode({ skip: true }, undefined), "");
  assert.strictEqual(effectiveMode(undefined, "skip"), "");
});

test("effectiveBypass: args, the enum, the legacy bool by key, or the flagless fixture key", () => {
  assert.strictEqual(effectiveBypass({ bypass_args: true }, {}), true);
  assert.strictEqual(effectiveBypass(claude, { permission_mode: "bypassPermissions" }), true);
  assert.strictEqual(effectiveBypass(claude, { permission_mode: "plan" }), false);
  assert.strictEqual(effectiveBypass({ ...claude, legacy_bypass_key: "yolo" }, { yolo: true }), true);
  assert.strictEqual(effectiveBypass({ ...claude, legacy_bypass_key: "yolo" }, { yolo: true, permission_mode: "auto" }), false);
  assert.strictEqual(effectiveBypass(claude, { yolo: true }), false, "a key with no flag is not bypass");
  assert.strictEqual(effectiveBypass(claude, { [BYPASS_OPTION_KEY]: true }), true);
  assert.strictEqual(effectiveBypass(undefined, undefined), false);
});

test("legacyBypassKeyFromDefs finds the bool by its flag, the first sorted key the options set", () => {
  const defs = [
    { key: "zzz_skip", type: "bool", flag: "--dangerously-skip-permissions" },
    { key: "aaa_skip", type: "bool", flag: "--dangerously-skip-permissions" },
    { key: "skip_permissions", type: "enum", flag: "--dangerously-skip-permissions" },
    { key: "verbose", type: "bool", flag: "--verbose" },
    MODE_DEF,
  ];
  assert.strictEqual(legacyBypassKeyFromDefs(defs, { zzz_skip: true, aaa_skip: false }, "claude"), "aaa_skip");
  assert.strictEqual(legacyBypassKeyFromDefs(defs, { zzz_skip: true }, "claude"), "zzz_skip");
  assert.strictEqual(legacyBypassKeyFromDefs(defs, { verbose: true, skip_permissions: true }, "claude"), "", "by flag and type, not by name");
  assert.strictEqual(legacyBypassKeyFromDefs(defs, {}, "claude"), "");
  assert.strictEqual(legacyBypassKeyFromDefs(undefined, { zzz_skip: true }, "claude"), "");
});

// The enum owns the older bool only when the permission_mode def for the command can say Bypass (mirrors config.EnumOwnsLegacyBypass).
const MODE_DEF = { key: "permission_mode", type: "enum", choices: ["manual", "plan", "bypassPermissions"], flag: "--permission-mode", bins: ["claude"] };
const SKIP_DEF = { key: "skip", type: "bool", flag: "--dangerously-skip-permissions", bins: ["claude"] };

test("the enum owns the legacy bool only when its def applies and has bypassPermissions", () => {
  assert.strictEqual(enumOwnsLegacyBypass([MODE_DEF, SKIP_DEF], "claude"), true);
  assert.strictEqual(enumOwnsLegacyBypass([MODE_DEF, SKIP_DEF], "/usr/bin/claude"), true);
  assert.deepStrictEqual(legacyKeysOwnedByEnum([MODE_DEF, SKIP_DEF], "claude"), ["skip"]);
  assert.strictEqual(legacyBypassKeyFromDefs([MODE_DEF, SKIP_DEF], { skip: false }, "claude"), "skip");
  const noBypass = { ...MODE_DEF, choices: ["default", "acceptEdits", "plan"] };
  assert.strictEqual(enumOwnsLegacyBypass([noBypass, SKIP_DEF], "claude"), false, "no bypassPermissions choice");
  assert.deepStrictEqual(legacyKeysOwnedByEnum([noBypass, SKIP_DEF], "claude"), []);
  assert.strictEqual(legacyBypassKeyFromDefs([noBypass, SKIP_DEF], { skip: true }, "claude"), "");
  assert.strictEqual(enumOwnsLegacyBypass([{ ...MODE_DEF, bins: ["codex"] }, SKIP_DEF], "claude"), false, "def for another command");
  assert.strictEqual(enumOwnsLegacyBypass([SKIP_DEF], "claude"), false, "no permission_mode def");
  assert.strictEqual(enumOwnsLegacyBypass(undefined, "claude"), false);
  assert.strictEqual(legacyBypassKeyFromDefs([MODE_DEF, SKIP_DEF], { skip: true }, "bash"), "", "legacy def does not apply to bash");
});

test("effectiveBypass counts a ticked bool the server marks as the older form when the enum does not own it", () => {
  const p = { ...claude, options: [{ key: "skip", type: "bool", value: true, bypass: true }, { key: "other", type: "bool", value: true }] };
  assert.strictEqual(effectiveBypass(p, { skip: true }), true);
  assert.strictEqual(effectiveBypass(p, { skip: false }), false);
  assert.strictEqual(effectiveBypass(p, { other: true }), false, "not marked");
  assert.strictEqual(effectiveBypass(p, { skip: true, permission_mode: "plan" }), false, "a set mode supersedes the bool, as on the server");
  assert.strictEqual(bypassWarningShown(p, { skip: true }, true), true);
});

test("critRmView: hidden off a box, dimmed outside bypass, plain on a clone, strong in place", () => {
  assert.strictEqual(critRmView({ runtime: "host", clone: false }, true).show, false);
  assert.strictEqual(critRmView({}, true).show, false);
  assert.deepStrictEqual(critRmView({ runtime: "box", clone: true }, true), { show: true, disabled: false, line: CRIT_RM_COPY.clone, strong: false });
  assert.deepStrictEqual(critRmView({ runtime: "box", clone: false }, true), { show: true, disabled: false, line: CRIT_RM_COPY.inPlace, strong: true });
  assert.deepStrictEqual(critRmView({ runtime: "box", clone: false }, false), { show: true, disabled: true, line: "Needs Permission mode: Bypass", strong: false });
  assert.deepStrictEqual(critRmView({ runtime: "box", clone: true }, false), { show: true, disabled: true, line: "Needs Permission mode: Bypass", strong: false });
});

test("the bypass warning follows the effective mode (the enum, or a legacy bool by its flag)", () => {
  assert.strictEqual(bypassWarningShown({ ...claude, legacy_bypass_key: "yolo" }, { yolo: true }, true), true);
  assert.strictEqual(bypassWarningShown(claude, { permission_mode: "bypassPermissions" }, true), true);
  assert.strictEqual(bypassWarningShown(claude, { permission_mode: "bypassPermissions" }, false), false);
  assert.strictEqual(bypassWarningShown(claude, { permission_mode: "plan" }, true), false);
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
  // The phone seg never offers clone, so a clone profile is still plain "box" here; the desktop default is tierPreselect.
  assert.strictEqual(spawnRuntimeDefault({ runtime: "box", clone_of: "/r", default_cwd: "/c" }, "runc"), "box");
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

test("spawnPayload sends clone only when it is a boolean", () => {
  const base = { agent: "claude", name: "api", cwd: "/w", options: {}, runtime: "box" };
  assert.strictEqual(spawnPayload({ ...base, clone: true, profile: claude }).clone, true);
  assert.strictEqual(spawnPayload({ ...base, clone: false, profile: claude }).clone, false);
  assert.ok(!("clone" in spawnPayload({ ...base, clone: undefined, profile: claude })));
  assert.ok(!("clone" in spawnPayload({ ...base, clone: "yes", profile: claude })));
});

test("spawnPrefill copies profile, cwd, runtime and options — never the name or a first message", () => {
  const s = { id: "claude-3", agent: "claude", name: "api", cwd: "/w", runtime: "host", options: { effort: "low" }, summary: "x" };
  const pf = spawnPrefill(s);
  assert.deepStrictEqual(pf, { agent: "claude", cwd: "/w", runtime: "host", account: "", clone: false, options: { effort: "low" } });
  pf.options.effort = "high";
  assert.strictEqual(s.options.effort, "low", "the prefill must not alias the session's options");
  assert.deepStrictEqual(spawnPrefill({}), { agent: "", cwd: "", runtime: "", account: "", clone: false, options: {} });
  // Only a clone session carries clone_of, so only it is duplicated as a clone.
  assert.strictEqual(spawnPrefill({ ...s, clone_of: "/r" }).clone, true);
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

test("Shift, AltGraph, a missing event and a non-Enter key do not spawn", () => {
  const ev = (m) => Object.assign({ key: "Enter", ctrlKey: false, metaKey: false, altKey: false, isComposing: false }, m);
  assert.strictEqual(isSubmitHotkey(ev({ ctrlKey: true, shiftKey: true }), false), false);
  assert.strictEqual(isSubmitHotkey(ev({ metaKey: true, shiftKey: true }), true), false);
  assert.strictEqual(isSubmitHotkey(ev({ ctrlKey: true, getModifierState: (k) => k === "AltGraph" }), false), false);
  assert.strictEqual(isSubmitHotkey(null, false), false);
  assert.strictEqual(isSubmitHotkey(ev({ ctrlKey: true, key: "k" }), false), false);
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
  // A Keychain failure is never an unlock prompt: there is nothing to type.
  assert.strictEqual(spawnRefusedLocked(503, "store_unavailable"), false);
});

test("spawnRefusedUnavailable: only a 503 carrying the store_unavailable code", () => {
  const { spawnRefusedUnavailable } = require("../static/spawnform.js");
  assert.strictEqual(spawnRefusedUnavailable(503, "store_unavailable"), true);
  assert.strictEqual(spawnRefusedUnavailable(503, ""), false);
  assert.strictEqual(spawnRefusedUnavailable(423, "store_locked"), false);
  assert.strictEqual(spawnRefusedUnavailable(400, "store_unavailable"), false);
  assert.strictEqual(spawnRefusedUnavailable(undefined, undefined), false);
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

test("spawnRefusedNotShared / spawnRefusedRestarting: status and code must both match", () => {
  const f = require("../static/spawnform.js");
  assert.strictEqual(f.spawnRefusedNotShared(400, "not_shared"), true);
  assert.strictEqual(f.spawnRefusedNotShared(400, "other"), false);
  assert.strictEqual(f.spawnRefusedNotShared(423, "not_shared"), false);
  assert.strictEqual(f.spawnRefusedRestarting(503, "vm_restarting"), true);
  assert.strictEqual(f.spawnRefusedRestarting(503, "store_unavailable"), false);
  assert.strictEqual(f.spawnRefusedRestarting(400, "vm_restarting"), false);
});

test("spawnNotSharedLine: the M9.9 sentence for a plain refusal, the server's for pending / read-only", () => {
  const { spawnNotSharedLine } = require("../static/spawnform.js");
  assert.strictEqual(spawnNotSharedLine({ error: "x", path: "~/work/api", code: "not_shared" }),
    "~/work/api is not shared with the box VM, so a box can’t use it.");
  assert.strictEqual(spawnNotSharedLine({ error: "Added, not applied yet.", path: "~/a", pending: true }), "Added, not applied yet.");
  assert.strictEqual(spawnNotSharedLine({ error: "Shared read-only.", path: "~/a", read_only: true }), "Shared read-only.");
});

test("vmShareFilter: applied rw shares only, case-folded, whole components; null when the VM list is not in force", () => {
  const { vmShareFilter } = require("../static/spawnform.js");
  const view = { state: "applied", rows: [
    { path: "/Users/Me/Work", mode: "rw" },
    { path: "/Users/Me/Docs", mode: "ro" },
    { path: "/Users/Me/New", mode: "rw", pending: "added" },
    { path: "/Users/Me/Going", mode: "rw", pending: "removed" },
  ] };
  const f = vmShareFilter(view);
  assert.strictEqual(f("/Users/Me/Work"), true);
  assert.strictEqual(f("/users/me/work/api/"), true, "case-insensitive, inside the share");
  assert.strictEqual(f("/Users/Me/Workshop"), false, "whole components, not a string prefix");
  assert.strictEqual(f("/Users/Me/Docs"), false, "read-only is not enough for a workspace");
  assert.strictEqual(f("/Users/Me/New"), false, "added but not applied");
  assert.strictEqual(f("/Users/Me/Going/x"), true, "pending removal is still applied");
  assert.strictEqual(vmShareFilter({ state: "whole_home", rows: view.rows }), null);
  assert.strictEqual(vmShareFilter({ state: "not_set_up", rows: [] }), null);
  assert.strictEqual(vmShareFilter(null), null);
});

test("I3: Share this folder prefills Read only only when the body says need_write false", () => {
  const { spawnShareMode } = require("../static/spawnform.js");
  assert.strictEqual(spawnShareMode({ need_write: false }), "ro");
  assert.strictEqual(spawnShareMode({ need_write: true }), "rw");
  assert.strictEqual(spawnShareMode({}), "rw");
  assert.strictEqual(spawnShareMode(null), "rw");
});

test("I4: the VM filter applies only to a box runtime; switching re-evaluates", () => {
  const { spawnVMFilterFor } = require("../static/spawnform.js");
  const f = () => true;
  assert.strictEqual(spawnVMFilterFor("box", f), f);
  assert.strictEqual(spawnVMFilterFor("host", f), null);
  assert.strictEqual(spawnVMFilterFor("", f), null);
  assert.strictEqual(spawnVMFilterFor("box", null), null);
});

test("T15: only the newest request is current", () => {
  const { spawnLatest } = require("../static/spawnform.js");
  const l = spawnLatest();
  const a = l.next(), b = l.next();
  assert.strictEqual(l.current(a), false);
  assert.strictEqual(l.current(b), true);
});
