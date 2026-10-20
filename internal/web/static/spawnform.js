"use strict";

// Pure decisions behind the spawn dialog. DOM wiring lives in app.js; this
// file only DEFINES things (it loads before app.js) and is node-tested.

// BYPASS_OPTION_KEY is the launch option that maps to
// --dangerously-skip-permissions in this deployment's option definitions.
const BYPASS_OPTION_KEY = "skip_permissions";

// isClaudeBinName mirrors launcher.IsClaudeBin: the binary's basename is
// "claude". Used where only a typed bin exists (the profile editor).
function isClaudeBinName(bin) {
  const b = String(bin == null ? "" : bin).trim();
  return b !== "" && b.split("/").pop() === "claude";
}

// promptFieldVisible: the server says which agents take a first message
// (ProfileView.accepts_prompt) — codex does not, so its profiles hide it.
function promptFieldVisible(profile) {
  return !!(profile && profile.accepts_prompt);
}

// repliesFieldVisible: replies are a claude-only feature on the server.
function repliesFieldVisible(profile) {
  // Kill-switch: a field the server would refuse is not shown.
  if (globalThis.repliesEnabled !== true) return false;
  return !!(profile && profile.claude);
}

// bypassWarningShown: replies to a session that skips permission checks type
// straight into an agent that runs anything — say so while the toggle is on.
// Bypass comes from this run's option or from the profile's own args.
function bypassWarningShown(profile, options, replyable) {
  if (!replyable || !repliesFieldVisible(profile)) return false;
  return !!(profile.bypass_args || (options && options[BYPASS_OPTION_KEY] === true));
}

// runtimeSegChoices: the containment seg's buttons. Their values are the tier
// names spawnRuntimeDefault returns ("box"/"host"). The seg used "" for box,
// which matched no button, so the dialog opened with neither selected.
function runtimeSegChoices() {
  return [
    { value: "box", label: "box" },
    { value: "host", label: "host", danger: true },
  ];
}

// spawnPayload builds the POST /api/sessions body from what the form holds.
// A first message and the replies flag are sent only for a profile that shows
// them, so a value left in a hidden field never reaches the server.
function spawnPayload(f) {
  const v = f || {};
  const req = { agent: v.agent, name: String(v.name || "").trim(), cwd: v.cwd || "", options: v.options || {} };
  if (v.runtime) req.runtime = v.runtime;
  // Any chosen account is sent, "default" included: an explicit default pick
  // must override a profile pinned to another account. Unset = omit.
  if (v.account) req.account = v.account;
  // login: the Settings "Log in" session; the server drops the profile's own
  // auth and accepts it only for a named claude account on the host tier.
  if (v.login) req.login = true;
  const prompt = String(v.prompt || "").trim();
  if (prompt && promptFieldVisible(v.profile)) req.prompt = prompt;
  if (repliesFieldVisible(v.profile)) req.replyable = !!v.replyable;
  return req;
}

// spawnPrefill: what Duplicate carries into the dialog. Not the name (two
// rows with one name cannot be told apart) and never a first message (that
// was the old run's task).
function spawnPrefill(s) {
  const v = s || {};
  return { agent: v.agent || "", cwd: v.cwd || "", runtime: v.runtime || "", account: v.account || "", options: Object.assign({}, v.options || {}) };
}

// isSubmitHotkey: Ctrl+Enter (⌘+Enter on mac) in the first-message field
// spawns; plain Enter stays a newline. AltGr (ctrl+alt off-mac) and an IME
// committing a candidate are excluded.
function isSubmitHotkey(e, mac) {
  if (!e || e.key !== "Enter" || e.altKey || e.isComposing) return false;
  return mac ? !!e.metaKey && !e.ctrlKey : !!e.ctrlKey && !e.metaKey;
}

// spawnRefusedLocked: the server refused a spawn or restart because the secret
// store is locked (HTTP 423 with code "store_locked"). The dialog answers by
// showing the unlock form inline, not by closing with a toast.
function spawnRefusedLocked(status, code) {
  return status === 423 && code === "store_locked";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    spawnRefusedLocked, BYPASS_OPTION_KEY, isClaudeBinName, promptFieldVisible, repliesFieldVisible, bypassWarningShown,
    runtimeSegChoices, spawnPayload, spawnPrefill, isSubmitHotkey,
  };
}
