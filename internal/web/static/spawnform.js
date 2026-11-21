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
// (ProfileView.accepts_prompt) — a shell or other non-claude bin does not, so its
// profiles hide it.
function promptFieldVisible(profile) {
  return !!(profile && profile.accepts_prompt);
}

// repliesFieldVisible: replies are a claude-only feature on the server.
function repliesFieldVisible(profile) {
  // Kill-switch: a field the server would refuse is not shown.
  if (globalThis.repliesEnabled !== true) return false;
  return !!(profile && profile.claude);
}

// Permission mode (#342): the Claude rules the dialog and the profile page share. The words are founder-approved;
// the server's own strings (the box-only reason, "needs Permission mode: Bypass") live in Go.
const PERM_MODE_KEY = "permission_mode";
const BYPASS_MODE = "bypassPermissions";
const CRIT_RM_KEY = "skip_critical_rm_prompt";
const PERM_MODE_LABELS = { manual: "Manual", acceptEdits: "Accept edits", auto: "Auto", plan: "Plan", bypassPermissions: "Bypass" };
const PERM_MODE_LINES = {
  "": "Claude Code's default. On current versions that is Auto.",
  manual: "Asks you before edits and commands.",
  acceptEdits: "Edits files without asking. Still asks before other commands.",
  auto: "A classifier reviews each action and blocks risky ones. Asks you less.",
  plan: "Reads and plans only. Changes nothing until you approve the plan.",
  bypassPermissions: "Runs every action without asking.",
};
const CRIT_RM_COPY = {
  detail: "In Bypass, Claude Code still stops before an rm of the working folder, ~ or /, waits 2 minutes, then refuses. This turns that stop off.",
  needsBypass: "Needs Permission mode: Bypass",
  clone: "The agent works on its own copy of the repo.",
  inPlace: "Your own folder is mounted live. With this on, the agent can delete it, and everything in it, without asking.",
  legacy: (label) => "Set by " + label + " (older form)",
  args: "This profile's own arguments also turn Bypass on.",
};

// permModeLabel: the seg's button text. A choice a config.json def adds (not one of the five) shows as it is.
function permModeLabel(choice) {
  return Object.prototype.hasOwnProperty.call(PERM_MODE_LABELS, choice) ? PERM_MODE_LABELS[choice] : choice;
}

// permModeLine: the consequence line under the control; "" for a choice with none.
function permModeLine(choice) {
  return Object.prototype.hasOwnProperty.call(PERM_MODE_LINES, choice) ? PERM_MODE_LINES[choice] : "";
}

const LEGACY_BYPASS_FLAG = "--dangerously-skip-permissions";

// optionDefApplies: config.OptionDef.AppliesTo for a def (no bins: every agent; else the bin or its basename). Pure twin of app.js
// optApplies, for the scripts that run under node.
function optionDefApplies(d, bin) {
  if (!d || !Array.isArray(d.bins) || d.bins.length === 0) return true;
  const base = String(bin || "").split("/").pop();
  return d.bins.includes(bin) || d.bins.includes(base);
}

const isLegacyBypassDef = (d) => !!d && d.type === "bool" && d.flag === LEGACY_BYPASS_FLAG;

// enumOwnsLegacyBypass: the Permission mode control owns the older bypass bool for this command only when the permission_mode def
// that applies to it can say Bypass (has bypassPermissions among its choices). Mirrors config.EnumOwnsLegacyBypass. A def whose
// choices omit it cannot stand in for the bool, so the bool stays a checkbox of its own, as before #342.
function enumOwnsLegacyBypass(defs, bin) {
  const list = Array.isArray(defs) ? defs : Object.values(defs || {});
  return list.some((d) => d && d.key === PERM_MODE_KEY && optionDefApplies(d, bin) && Array.isArray(d.choices) && d.choices.includes(BYPASS_MODE));
}

// legacyBypassKeyFromDefs: the key of the older bypass bool, found by its flag and never by its name (mirrors
// config.LegacyBypassKey: a key the profile has, whatever its value, while the enum owns it). The first sorted such key. For the
// profile page, which has the defs; New session reads profile.legacy_bypass_key.
function legacyBypassKeyFromDefs(defs, options, bin) {
  if (!enumOwnsLegacyBypass(defs, bin)) return "";
  const opts = options || {};
  const list = Array.isArray(defs) ? defs : Object.values(defs || {});
  const keys = list.filter((d) => isLegacyBypassDef(d) && optionDefApplies(d, bin)).map((d) => d.key).sort();
  for (const k of keys) if (Object.prototype.hasOwnProperty.call(opts, k)) return k;
  return "";
}

// legacyKeysOwnedByEnum: the keys of the older bypass bools (any declared bool on the legacy flag) that the Permission mode control
// owns for this command: they are the legacy form of Bypass, so Advanced does not draw them, whether or not the profile stores one.
// Only while the enum owns the bool (enumOwnsLegacyBypass); otherwise the checkbox is the only way to set it and stays.
function legacyKeysOwnedByEnum(defs, bin) {
  if (!enumOwnsLegacyBypass(defs, bin)) return [];
  const list = Array.isArray(defs) ? defs : Object.values(defs || {});
  return list.filter((d) => isLegacyBypassDef(d) && optionDefApplies(d, bin)).map((d) => d.key);
}

// effectiveMode: permission_mode when it carries a value; else Bypass when the legacy bool is on; else "".
function effectiveMode(options, legacyKey) {
  const o = options || {};
  if (o.permission_mode) return o.permission_mode;
  return legacyKey && o[legacyKey] === true ? "bypassPermissions" : "";
}

// effectiveBypass: the run skips permission checks. From the profile's own args, from the mode (the enum, or the legacy bool
// by its flag), from a ticked bool the server marks as the older bypass form (profile.options[].bypass: the enum does not own it,
// so it is a checkbox; a mode that is set supersedes it, as on the server), or from the flagless fixture key BYPASS_OPTION_KEY.
function effectiveBypass(profile, options) {
  const p = profile || {};
  const o = options || {};
  const markedOn = !o[PERM_MODE_KEY] && (Array.isArray(p.options) ? p.options : []).some((x) => x && x.bypass === true && o[x.key] === true);
  return !!(p.bypass_args
    || effectiveMode(options, p.legacy_bypass_key) === "bypassPermissions"
    || markedOn
    || o[BYPASS_OPTION_KEY] === true);
}

// critRmView: the critical-rm row for the card picked. Hidden off a box; dimmed unless the run is in bypass; otherwise the
// line for a clone (plain) or in place (strong). `where` is spawnWhere().
function critRmView(where, bypass) {
  const w = where || {};
  const show = w.runtime === "box";
  if (!bypass) return { show, disabled: true, line: CRIT_RM_COPY.needsBypass, strong: false };
  if (w.clone) return { show, disabled: false, line: CRIT_RM_COPY.clone, strong: false };
  return { show, disabled: false, line: CRIT_RM_COPY.inPlace, strong: true };
}

// bypassWarningShown: replies to a session that skips permission checks type
// straight into an agent that runs anything — say so while the toggle is on.
// Bypass comes from this run's mode or option, or from the profile's own args.
function bypassWarningShown(profile, options, replyable) {
  if (!replyable || !repliesFieldVisible(profile)) return false;
  return effectiveBypass(profile, options);
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
  // clone: the desktop card's pick (Box · clone / in place). Unset keeps the profile deciding, as the phone does.
  if (typeof v.clone === "boolean") req.clone = v.clone;
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
// was the old run's task). clone: only a clone session carries clone_of, so a duplicate of an in-place session on a
// clone profile stays in place.
function spawnPrefill(s) {
  const v = s || {};
  return { agent: v.agent || "", cwd: v.cwd || "", runtime: v.runtime || "", account: v.account || "", clone: !!v.clone_of, options: Object.assign({}, v.options || {}) };
}

// isSubmitHotkey: spawn.submit in the shortcut registry (Ctrl+Enter, ⌘+Enter on
// mac) in any field of the spawn form spawns; plain Enter stays a newline.
// AltGr and an IME committing a candidate are excluded by the registry match.
function isSubmitHotkey(e, mac) {
  const matches = typeof shortcutMatches === "function" ? shortcutMatches : require("./shortcuts.js").shortcutMatches;
  return !!e && matches("spawn.submit", e, mac);
}

// spawnSubmitLabel: the New session submit's caption. One verb on the desktop (F16, frame M4.1); the phone sheet
// keeps "Spawn ⏎". `desktop` is the layout (drawerDesktop()), not the platform.
function spawnSubmitLabel(desktop) {
  return desktop ? "Start session" : "Spawn ⏎";
}

// spawnFootHint: the desktop footer's hint, the spawn.submit chord as one key per chip and then the words
// ({ keys: ["Ctrl", "↵"], text: "to start" }; on the Mac ["⌘", "↵"]). The keys come from the shortcut registry,
// so the hint cannot drift from the binding isSubmitHotkey tests.
function spawnFootHint(mac) {
  const sc = typeof chordKeys === "function" ? { chordKeys, activeChords, SHORTCUTS } : require("./shortcuts.js");
  const entry = sc.SHORTCUTS.find((s) => s.id === "spawn.submit");
  const chord = entry && sc.activeChords(entry, mac)[0];
  return { keys: chord ? sc.chordKeys(chord, mac) : [], text: "to start" };
}

// PLIST_SEARCH_MIN: the New session profile list gets a search box from this many
// rendered rows up (desktop only; the list does not exist on mobile).
const PLIST_SEARCH_MIN = 8;

// profileMatches: the list filter. Case-insensitive substring on the profile name
// or its "touches" line; no fuzzy matching. A blank query matches everything.
function profileMatches(name, touch, q) {
  const needle = String(q == null ? "" : q).trim().toLowerCase();
  if (needle === "") return true;
  return String(name == null ? "" : name).toLowerCase().includes(needle)
    || String(touch == null ? "" : touch).toLowerCase().includes(needle);
}

// spawnRefusedLocked: the server refused a spawn or restart because the secret
// store is locked (HTTP 423 with code "store_locked"). The dialog answers by
// showing the unlock form inline, not by closing with a toast.
function spawnRefusedLocked(status, code) {
  return status === 423 && code === "store_locked";
}

// spawnRefusedUnavailable: the server refused a spawn or restart because the
// macOS Keychain did not give the store key (HTTP 503 with code
// "store_unavailable"). Unlike a locked store there is nothing to type, so the
// dialog shows the server's sentence (what failed, how to fix it) in its error
// line and offers no unlock form.
function spawnRefusedUnavailable(status, code) {
  return status === 503 && code === "store_unavailable";
}

// spawnRefusedNotShared: a boxed spawn was refused because its folder is not shared with the macOS box VM (HTTP 400, code
// "not_shared"). The dialog stays open: the error sits under the Workspace field with a "Share this folder…" button (M9.9).
function spawnRefusedNotShared(status, code) {
  return status === 400 && code === "not_shared";
}

// spawnRefusedRestarting: refused because the box VM is restarting (HTTP 503, code "vm_restarting"). The dialog shows the
// server's sentence in its error line and starts following the apply.
function spawnRefusedRestarting(status, code) {
  return status === 503 && code === "vm_restarting";
}

// spawnRefusedBoxUnavailable: the containment layer refused a boxed spawn (HTTP 400, code "box_unavailable": no box layer, the daemon
// is down, egress control is not running, ...). On the desktop the dialog stays open and shows the sentence on its error line (#142).
function spawnRefusedBoxUnavailable(status, code) {
  return status === 400 && code === "box_unavailable";
}

// The M9.9 sentence, after the folder's path (the path itself is drawn as code).
const SPAWN_NOT_SHARED_TAIL = " is not shared with the box VM, so a box can’t use it.";
const SPAWN_VM_SEES_HELP = "The list offers only folders the box VM can see.";

// spawnNotSharedLine(body): the field error for a not_shared refusal body ({error, path, pending, read_only}). The plain case is
// the M9.9 sentence built from the path; a folder that is only pending, or shared read-only, gets the server's own sentence.
function spawnNotSharedLine(body) {
  const b = body || {};
  if ((b.pending === true || b.read_only === true) && b.error) return String(b.error);
  return String(b.path || "") + SPAWN_NOT_SHARED_TAIL;
}

// vmShareFilter(view): the picker's folder filter on a Mac, from GET /api/vm-folders. Returns null (no filtering; the server still
// enforces) unless the VM's list is applied: a whole-home or not-set-up VM can see everything or nothing we can name. Otherwise a
// function(path) true when the path is inside an applied read-write share: rows with mode "rw" whose pending is not "added" (a
// row pending removal is still applied). Case-insensitive, whole path components.
function vmShareFilter(view) {
  if (!view || (view.state !== "applied" && view.state !== "pending") || !Array.isArray(view.rows)) return null;
  const norm = (p) => String(p || "").replace(/\/+$/, "").toLowerCase();
  const roots = view.rows.filter((r) => r && r.mode === "rw" && r.pending !== "added").map((r) => norm(r.path)).filter((r) => r !== "");
  return (path) => {
    const p = norm(path);
    return roots.some((r) => p === r || p.startsWith(r + "/"));
  };
}

// spawnShareMode(body): the mode "Share this folder…" prefills (spec I3). The refusal body says need_write; only an explicit false
// (a clone_of or context path the box only reads) prefills Read only. Anything else, including an older server's body, is Read & write.
function spawnShareMode(body) {
  return body && body.need_write === false ? "ro" : "rw";
}

// spawnVMFilterFor(runtime, filter): the folder filter the picker uses (spec I4). It applies only to a box session, since a host
// session needs no VM folder; on a host runtime every folder is offered.
function spawnVMFilterFor(runtime, filter) {
  return runtime === "box" && typeof filter === "function" ? filter : null;
}

// spawnLatest(): a request serial. next() starts a request and returns its number; current(n) is true only for the newest one, so a
// slow older answer never overwrites a newer one (T15).
function spawnLatest() {
  let n = 0;
  return { next: () => ++n, current: (k) => k === n };
}

// spawnProgressText is the dialog's status line while a spawn POST is in flight
// (#36). The server answers only once the session exists, and a boxed spawn may
// first start the box (on a Mac, its VM too) and a container, which takes tens of seconds after an
// idle spell. So: an elapsed count, and for a box past 3s, the reason.
function spawnProgressText(seconds, runtime) {
  const s = Math.max(0, Math.floor(seconds || 0));
  let line = "Starting session…";
  if (s > 0) line += " " + (s < 60 ? s + "s" : Math.floor(s / 60) + "m " + String(s % 60).padStart(2, "0") + "s");
  if (runtime === "box" && s >= 3) line += " · starting the box (this can take a minute after an idle spell)";
  return line;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    spawnRefusedLocked, spawnRefusedUnavailable, spawnRefusedBoxUnavailable, spawnRefusedNotShared, spawnRefusedRestarting, spawnNotSharedLine, vmShareFilter, spawnShareMode, spawnVMFilterFor, spawnLatest,
    SPAWN_NOT_SHARED_TAIL, SPAWN_VM_SEES_HELP, BYPASS_OPTION_KEY, isClaudeBinName, promptFieldVisible, repliesFieldVisible, bypassWarningShown,
    runtimeSegChoices, spawnPayload, spawnPrefill, isSubmitHotkey, spawnProgressText, spawnSubmitLabel, spawnFootHint,
    PLIST_SEARCH_MIN, profileMatches,
    PERM_MODE_KEY, BYPASS_MODE, CRIT_RM_KEY, PERM_MODE_LABELS, PERM_MODE_LINES, CRIT_RM_COPY, permModeLabel, permModeLine, enumOwnsLegacyBypass, legacyBypassKeyFromDefs, legacyKeysOwnedByEnum, optionDefApplies, effectiveMode, effectiveBypass, critRmView,
  };
}
