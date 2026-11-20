"use strict";

// Where a new session runs, on the desktop New session dialog: three cards (Box · in place, Box · clone, Host).
// This part is pure. It decides which card is picked, which are disabled and why, and what each card says.
// Nothing here reads the page.
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

// tierHostState: Host is off when the server says the profile may not run there (ProfileView.host_block).
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
// (or clone dir) is the workspace, else a profile saved as host, else in place. Never a "last used" tier.
// o = {profile, cwd, home}.
function tierPreselect(o) {
  const p = o.profile || {};
  if (p.clone_of && (tierSamePath(o.cwd, p.clone_of, o.home) || tierSamePath(o.cwd, p.default_cwd, o.home))) return "clone";
  if (p.runtime === "host") return "host";
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

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    TP_KEYS, tierWhere, tierSamePath, tierWhy, tierHostState, tierCloneState, tierPreselect, tierModel,
  };
}
