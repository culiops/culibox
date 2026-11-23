"use strict";

// Where a new session runs, on the desktop New session dialog: three cards (Box · in place, Box · clone, Host).
// The first part is pure: it decides which card is picked, which are disabled and why, and what each card says.
// The part after it (renderTierPick and the picks) is the only code that reads the page.
//
// Loaded as a classic script, so top-level names are shared globals: they are tier-/tp-prefixed on purpose.

const TP_KEYS = ["box", "clone", "host"];

const TP_NO_CLONE = "This profile has no clone setup (Settings ⚙ › profile › Workspace).";
const TP_VM_LINE = "Box VM is stopped; starting it takes about a minute.";

// tierWhere: a card's request fields. Box · clone sends the same runtime as Box · in place; `clone` is what differs.
function tierWhere(key) {
  if (key === "box") return { runtime: "box", clone: false };
  if (key === "clone") return { runtime: "box", clone: true };
  if (key === "host") return { runtime: "host", clone: false };
  return {};
}

function tierNormPath(p, home) {
  let s = String(p == null ? "" : p).trim();
  if (s === "") return "";
  if (home && (s === "~" || s.startsWith("~/"))) s = String(home) + s.slice(1);
  s = s.replace(/\/+$/, "");
  return s === "" ? "/" : s;
}

// tierSamePath: two paths name the same folder, with a leading ~ expanded and a trailing slash ignored. An empty side never matches.
function tierSamePath(a, b, home) {
  const x = tierNormPath(a, home);
  const y = tierNormPath(b, home);
  return x !== "" && y !== "" && x === y;
}

// tierWhy: a server reason as a sentence: first letter upper-cased, a full stop unless it already ends one.
function tierWhy(s) {
  const t = String(s == null ? "" : s).trim();
  if (t === "") return "";
  const cap = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(cap) ? cap : cap + ".";
}

// tierJoin: tier.js's prose join, a global in the page and its export under node.
function tierJoin(words) {
  return (typeof tierJoinWords === "function" ? tierJoinWords : require("./tier.js").tierJoinWords)(words);
}

// tierHostDrops: what a Host run of the profile leaves out (ProfileView.host_drops, "AWS binding"...). Empty when it leaves nothing.
function tierHostDrops(profile) {
  const d = profile && profile.host_drops;
  return Array.isArray(d) ? d.filter((x) => typeof x === "string" && x !== "") : [];
}

// tierHostKeeps: the profile's own secrets and env a Host run still passes (ProfileView.host_keeps, "GH_TOKEN secret"...).
function tierHostKeeps(profile) {
  const k = profile && profile.host_keeps;
  return Array.isArray(k) ? k.filter((x) => typeof x === "string" && x !== "") : [];
}

// What Host does instead of the dropped bindings; the card row, the confirm's hint and the Access tab say it the same way.
const TP_DROPS_TAIL = "The agent uses this machine's own logins (~/.aws, gh) instead.";

// The drop items that are credentials (ProfileView.host_drops names): Host swaps these for the machine's own logins.
const TP_CREDENTIAL_DROPS = ["AWS binding", "GitHub binding", "token binding"];

// tierHasCredentialDrop: true when at least one dropped item is a credential binding. Context paths and box-only settings are not,
// so "uses this machine's own logins instead" would claim a swap that did not happen.
function tierHasCredentialDrop(drops) {
  return Array.isArray(drops) && drops.some((d) => TP_CREDENTIAL_DROPS.includes(d));
}

// tierDropsLine: the Host card's warning as one sentence ("" when nothing is dropped). The logins tail only when a credential drops;
// then what Host still passes (keeps), so the tail never reads as "only this machine's logins".
function tierDropsLine(drops, keeps) {
  if (!drops || !drops.length) return "";
  const kept = Array.isArray(keeps) && keeps.length ? " Still on Host: " + tierJoin(keeps) + "." : "";
  return "Not on Host: " + tierJoin(drops) + "." + (tierHasCredentialDrop(drops) ? " " + TP_DROPS_TAIL : "") + kept;
}

// tierHostState: Host is off when the server says the profile may not run there (ProfileView.host_block). host_drops does not
// turn it off: Host runs without the bindings, after a confirm.
function tierHostState(profile) {
  const b = profile && profile.host_block;
  if (typeof b === "string" && b !== "") return { ok: false, why: tierWhy(b) };
  return { ok: true, why: "" };
}

// tierCloneState: Box · clone needs the server to offer clone mode and a clone setup that is for this workspace.
// o = {profile, cwd, home, cloneUnavailable, short}.
function tierCloneState(o) {
  const p = o.profile || {};
  if (o.cloneUnavailable) return { ok: false, why: tierWhy(o.cloneUnavailable) };
  if (!p.clone_of) return { ok: false, why: TP_NO_CLONE };
  if (tierSamePath(o.cwd, p.clone_of, o.home) || tierSamePath(o.cwd, p.default_cwd, o.home)) return { ok: true, why: "" };
  const short = typeof o.short === "function" ? o.short : (x) => x;
  return { ok: false, why: "This profile's clone setup is for " + short(p.clone_of) + "." };
}

// tierPreselect: the card the profile and workspace point at, before any fallback (spec §5): a clone profile whose repo
// (or clone dir) is the workspace, else a profile saved as host, else in place. A host profile that drops bindings there opens on Box
// (#359): Host is one click away, and never what a stray Enter starts. Never a "last used" tier.
// o = {profile, cwd, home}.
function tierPreselect(o) {
  const p = o.profile || {};
  if (p.clone_of && (tierSamePath(o.cwd, p.clone_of, o.home) || tierSamePath(o.cwd, p.default_cwd, o.home))) return "clone";
  if (p.runtime === "host" && tierHostDrops(p).length === 0) return "host";
  return "box";
}

// tierModel: everything the cards need. o = {profile, cwd, home, picked, cloneUnavailable, vmStopped, short}.
// `picked` is the user's own choice ("" when they have not made one); it stands unless its card is now disabled.
function tierModel(o) {
  const p = o.profile || {};
  const short = typeof o.short === "function" ? o.short : (x) => x;
  const host = tierHostState(p);
  const cl = tierCloneState({ profile: p, cwd: o.cwd, home: o.home, cloneUnavailable: o.cloneUnavailable, short });
  const state = { box: { ok: true, why: "" }, clone: cl, host };

  const pre = tierPreselect({ profile: p, cwd: o.cwd, home: o.home });
  const dflt = state[pre].ok ? pre : "box";
  const pick = TP_KEYS.includes(o.picked) && state[o.picked].ok ? o.picked : dflt;

  // <ws>: the folder an in-place spawn edits. On a profile's clone dir that is the repo it was cloned from, because picking
  // in place moves the workspace there.
  const cwd = String(o.cwd == null ? "" : o.cwd);
  let ws = "the workspace";
  if (p.clone_of && tierSamePath(cwd, p.default_cwd, o.home)) ws = short(p.clone_of);
  else if (cwd !== "") ws = short(cwd);

  const rest = { k: "Rest of machine", v: [{ t: "hidden" }] };
  const rows = {
    box: [
      { k: "Your folder", v: [{ b: "edited live." }, { t: " Each change lands in " + ws + " as the agent makes it, uncommitted work included." }] },
      rest,
    ],
    clone: [
      { k: "Your folder", v: [{ b: "untouched." }, { t: " The agent works on its own copy of the repo." }] },
      { k: "Its work", v: [{ t: "comes home as " }, { code: "box/<branch>" }, { t: " branches in your repo, for you to review and merge." }] },
      rest,
    ],
    host: [
      { k: "Your folder", v: [{ b: "edited live." }] },
      { k: "Rest of machine", v: [{ b: "your whole account:" }, { t: " every file, key and network this machine has." }] },
    ],
  };
  const drops = tierHostDrops(p);
  if (drops.length) {
    const v = [{ b: tierJoin(drops) + "." }];
    if (tierHasCredentialDrop(drops)) v.push({ t: " " + TP_DROPS_TAIL });
    rows.host.push({ k: "Not on Host", v });
    const keeps = tierHostKeeps(p);
    if (keeps.length) rows.host.push({ k: "Still on Host", v: [{ b: tierJoin(keeps) + "." }] });
  }
  const titles = { box: "Box · in place", clone: "Box · clone", host: "Host · not contained" };

  const cards = TP_KEYS.map((key) => ({
    key,
    title: titles[key],
    dflt: key === dflt,
    on: key === pick,
    disabled: !state[key].ok,
    why: state[key].why,
    rows: rows[key],
    vm: o.vmStopped && key !== "host" ? TP_VM_LINE : "",
  }));
  return { pick, dflt, cards };
}

// --- the DOM part -------------------------------------------------------------------------------------------------
// Everything below reads the page (the dialog's globals from app.js, spawnpanel.js, profileform.js and svgicon.js) and draws
// #tierpick. Strings go in through textContent only.

// The card the user picked, "" until they do. It stands unless its card becomes disabled (tierModel), and a profile change forgets it.
let tierPicked = "";
// The Box VM is stopped (the S1 /api/vm-state); a Box card then says starting it takes about a minute.
let tierVmStopped = false;

// The mockup's Lucide icons (24x24, stroke 2): package, copy, triangle-alert; and the lock before a disabled reason.
const TP_ICONS = {
  box: [
    ["path", { d: "M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z" }],
    ["path", { d: "m3.3 7 8.7 5 8.7-5" }],
    ["path", { d: "M12 22V12" }],
  ],
  clone: [
    ["rect", { width: "14", height: "14", x: "8", y: "8", rx: "2" }],
    ["path", { d: "M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" }],
  ],
  host: [
    ["path", { d: "m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3" }],
    ["path", { d: "M12 9v4" }],
    ["path", { d: "M12 17h.01" }],
  ],
};
const TP_LOCK_ICON = [
  ["rect", { width: "18", height: "11", x: "3", y: "11", rx: "2" }],
  ["path", { d: "M7 11V7a5 5 0 0 1 10 0v4" }],
];

function tierEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function tierIcon(shapes, size) {
  return svgIcon(shapes, { viewBox: "0 0 24 24", strokeWidth: "2", size });
}

// tierCard: one button for one model card. It reads the card and never changes it (the box and clone cards share a row object).
function tierCard(c) {
  // tp-host is the one card with a class of its own: the host warning keys on it (desktop.css). Box and clone need none.
  const b = tierEl("button", "tp" + (c.key === "host" ? " tp-host" : "") + (c.on ? " on" : "") + (c.disabled ? " dis" : ""));
  b.type = "button";
  b.setAttribute("type", "button");
  b.setAttribute("aria-pressed", c.on ? "true" : "false");
  if (c.disabled) {
    b.disabled = true;
    b.setAttribute("aria-disabled", "true");
  }
  b.onclick = () => tierPick(c.key);
  const ic = tierEl("span", "tp-i");
  ic.append(tierIcon(TP_ICONS[c.key], "14"));
  const name = tierEl("span", "tp-n", c.title);
  if (c.dflt) name.append(tierEl("span", "tp-dflt", "profile default"));
  const rows = tierEl("span", "tp-d");
  for (const r of c.rows) {
    const v = tierEl("span", "tp-v");
    for (const s of r.v) {
      if (s.b !== undefined) v.append(tierEl("b", "", s.b));
      else if (s.code !== undefined) v.append(tierEl("code", "", s.code));
      else v.append(document.createTextNode(s.t));
    }
    rows.append(tierEl("span", "tp-k", r.k), v);
  }
  b.append(tierEl("span", "tp-rb"), ic, name, rows);
  if (c.disabled && c.why) {
    const why = tierEl("div", "tp-why");
    why.append(tierIcon(TP_LOCK_ICON, "12"), tierEl("span", "", c.why));
    b.append(why);
  }
  if (c.vm) b.append(tierEl("div", "tp-vm", c.vm));
  return b;
}

// renderTierPick: redraw the three cards from the live dialog. Not drawn without a box layer.
function renderTierPick() {
  const el = $("tierpick");
  if (!el) return;
  if (!boxRuntime) {
    // No box layer: nothing is drawn, and nothing of an earlier draw stays.
    el.replaceChildren();
    el.dataset.value = "";
    syncCritRmRow(); // no box: the box-only rows are hidden
    return;
  }
  const m = tierModel({
    profile: currentProfile(), cwd: getCwd(), home: window.HOME_DIR || "", picked: tierPicked,
    cloneUnavailable, vmStopped: tierVmStopped, short: shortPath,
  });
  el.replaceChildren(...m.cards.map(tierCard));
  el.dataset.value = m.pick;
  // The seg is the phone's control and spawnWhere reads it there, so on the desktop it follows the cards: a window narrowed below
  // the breakpoint with the dialog open then submits what the cards showed (a host profile Host is blocked for shows Box, not the
  // seg's own default). The phone never has its seg touched from here.
  if (drawerDesktop()) {
    const seg = $("runtime-seg");
    const rt = tierWhere(m.pick).runtime;
    if (seg && segValue(seg) !== rt) setSegValue(seg, rt);
    // In place says "each change lands in the repo", so the folder sent must be the repo: whichever way the pick ended on in place
    // (a click, the clone card unavailable, a seg tap) with the workspace still the clone dir, the workspace moves. The move
    // redraws through onSpawnCwdChanged, which is the last word on the DOM; this call has nothing more to draw.
    // Not while the user types in the workspace field: the picker reports every keystroke, so a path typed under the clone dir
    // would be swapped for the repo halfway through. A click (tierPick) and an open or fallback render still move it.
    if (m.pick !== "clone" && !tierTypingCwd()) tierLeaveCloneDir(currentProfile());
  }
  // The critical-rm row's line and visibility follow the card shown (clone, in place, host), however the pick came about.
  syncCritRmRow();
}

// tierTypingCwd: the workspace field has the focus. activeElement is guarded: the fake document of the tests has none.
function tierTypingCwd() {
  const input = $("spawn-ws-input");
  return !!input && typeof document !== "undefined" && !!document && document.activeElement === input;
}

// tierVmFetch: ask whether the Box VM is stopped (macOS colima; "" elsewhere) and let the Box cards say so when it answers. Not awaited
// by the dialog: the cards draw first. The server caps its colima call at 30 s, so the page waits a little longer. Only the latest
// call's answer counts, and a failed request leaves "not stopped"; it never throws for a network or API error.
let tierVmSeq = 0;
async function tierVmFetch() {
  // Cleared first, on every call: "stopped" is never shown on a guess, so a call that asks nothing (no box layer, the phone) leaves no old answer.
  tierVmStopped = false;
  const seq = ++tierVmSeq;
  if (!boxRuntime || !drawerDesktop()) return;
  let r;
  try {
    r = await api("/api/vm-state", { signal: deadlineSignal(35000) });
  } catch (_) {
    return; // no answer, or one that is not usable: no line
  }
  if (seq !== tierVmSeq) return;
  tierVmStopped = !!r && r.state === "stopped";
  renderTierPick();
}

// tierLeaveCloneDir: a clone profile whose workspace is its clone dir, and the session will not be a clone: the workspace becomes the
// repo the clone was made from. True when it moved. Not re-entrant: the redraw the move causes cannot move again, and a clone dir
// that is the repo itself has nowhere to move to.
let tierMoving = false;
function tierLeaveCloneDir(p) {
  if (tierMoving || !p || !p.clone_of || tierSamePath(p.clone_of, p.default_cwd, window.HOME_DIR || "")) return false;
  if (!tierSamePath(getCwd(), p.default_cwd, window.HOME_DIR || "")) return false;
  tierMoving = true;
  try {
    setSpawnCwd(p.clone_of, "");
    onSpawnCwdChanged();
  } finally {
    tierMoving = false;
  }
  return true;
}

// tierPick: a click on a card. Box · in place on a clone profile whose workspace is the clone dir moves the workspace to the repo
// the clone was made from (that is what "in place" edits), in the field and in the request.
function tierPick(key) {
  tierPicked = key;
  if (key !== "clone") tierLeaveCloneDir(currentProfile());
  setSegValue($("runtime-seg"), tierWhere(key).runtime);
  renderTierPick();
  updateRuntimeWarning();
  syncSpawnCopyLine();
  syncSpawnPreview();
  paintSpawnAcctHint();
}

// tierSegChanged: the phone's seg changed. Host is host; Box keeps a clone card.
function tierSegChanged(v) {
  // Box keeps a clone card: the one picked, or the one the cards show (a tap on the seg's selected Box fires this too).
  const el = $("tierpick");
  const shown = tierPicked === "clone" || (el && el.dataset.value === "clone");
  tierPicked = v === "host" ? "host" : (shown ? "clone" : "box");
  renderTierPick();
  updateRuntimeWarning();
  paintSpawnAcctHint();
}

// tierReset: a profile change forgets the pick; the cards pre-select again.
function tierReset() {
  tierPicked = "";
}

// tierApplyRuntime: a Duplicate keeps its card. `clone` is whether the session it copies was a clone.
function tierApplyRuntime(rt, clone) {
  if (rt === "host") tierPicked = "host";
  else if (rt === "box" && clone === true) tierPicked = "clone";
  else if (rt === "box" && clone === false) tierPicked = "box";
  else tierPicked = "";
  renderTierPick();
  paintSpawnAcctHint(); // the hint reads the card just applied
}

// spawnWhere: the ONE reader of where the dialog's session runs. No box layer: {}, nothing about the tier is sent. The desktop reads
// the cards ({runtime, clone}); the phone reads its seg, runtime only, so the profile still decides clone.
function spawnWhere() {
  if (!boxRuntime) return {};
  if (drawerDesktop()) return tierWhere($("tierpick").dataset.value);
  const v = segValue($("runtime-seg"));
  return v ? { runtime: v } : {};
}

// hostDropsConfirm: the question before a Host spawn of a profile whose bindings Host drops. Resolves true only on "Start on Host";
// Cancel has the focus. The drops are text nodes in a list, never markup. The logins sentence is a note in the body (the footer
// hint is hidden when too wide) and only when a credential drops: otherwise the title names box settings and there is no sentence.
// keeps (ProfileView.host_keeps) are the profile's own secrets and env Host still passes: a second note names them.
async function hostDropsConfirm(profileName, drops, keeps) {
  const creds = tierHasCredentialDrop(drops);
  const ul = tierEl("ul", "ask-bullets");
  for (const d of drops) ul.append(tierEl("li", "", d));
  const notes = creds ? [[TP_DROPS_TAIL + " Box keeps them."]] : [];
  if (Array.isArray(keeps) && keeps.length) notes.push(["Still passed to the agent on Host: " + tierJoin(keeps) + "."]);
  return askConfirm({
    title: creds ? "Start on Host without its bindings?" : "Start on Host without its box settings?",
    body: profileName + " will run with your user's full access to this machine. These do not apply on Host:",
    extra: [ul],
    notes,
    confirmLabel: "Start on Host",
    danger: true,
    focusCancel: true,
  });
}

// spawnCwdFor: the folder the request names. A clone session starts in the profile's clone dir; every other card in the workspace.
function spawnCwdFor(w) {
  if (w && w.clone) return (currentProfile() || {}).default_cwd || getCwd();
  return getCwd();
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    TP_KEYS, tierWhere, tierSamePath, tierWhy, tierHostState, tierCloneState, tierPreselect, tierModel, tierVmFetch,
    tierHostDrops, tierDropsLine, tierHasCredentialDrop, tierHostKeeps,
  };
}
