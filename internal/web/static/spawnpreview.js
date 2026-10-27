"use strict";

// The New session dialog's pure decisions (spec 5.6): the one-line "touches"
// summary of a profile, the "This agent will be able to" rows, which profile
// and workspace the dialog opens on. Everything here is built from names the
// server already sent (/api/profiles, /api/meta, /api/credentials): a field that
// is absent is left out, never guessed. DOM wiring lives in spawnpanel.js and
// app.js. Classic script: tier.js's defaultWorkspace is a browser global; under
// node it is required.

const SPAWN_SHELL_BINS = new Set(["bash", "sh", "zsh", "fish", "dash"]);

function spawnDep(name, file) {
  return typeof globalThis[name] === "function" ? globalThis[name] : require(file)[name];
}

const spawnNames = (l) => (Array.isArray(l) ? l.filter((x) => typeof x === "string" && x !== "") : []);
const spawnBase = (p) => String(p || "").split("/").filter(Boolean).pop() || "";

// spawnTierOf: the tier a profile runs under. boxRuntime, when given, is the
// answer of /api/meta: without a box layer every session is host tier whatever
// the profile says.
function spawnTierOf(profile, boxRuntime) {
  if (typeof boxRuntime === "string" && boxRuntime === "") return "host";
  return profile && profile.runtime === "host" ? "host" : "box";
}

// profileTouchLine: what a session of this profile can reach, in a line:
//   box · aws culipulse-prod · gh culiops     host · not contained     host · bash
function profileTouchLine(p, boxRuntime) {
  const prof = p || {};
  if (spawnTierOf(prof, boxRuntime) === "host") {
    return "host · " + (SPAWN_SHELL_BINS.has(spawnBase(prof.bin)) ? spawnBase(prof.bin) : "not contained");
  }
  const parts = ["box"];
  const aws = spawnNames(prof.aws_connections), gh = spawnNames(prof.github_owners), tok = spawnNames(prof.tokens);
  if (aws.length) parts.push("aws " + aws.join(", "));
  if (gh.length) parts.push("gh " + gh.join(", "));
  if (tok.length) parts.push("tok " + tok.join(", "));
  if (prof.clone_of) parts.push("clone of " + prof.clone_of);
  if (parts.length === 1) parts.push("no credentials");
  return parts.join(" · ");
}

// spawnStoreLocked: the store is set up, locked, and not the macOS keychain (the
// same test the Credentials tab uses for its Locked state).
function spawnStoreLocked(creds) {
  const st = creds && creds.store;
  return !!(st && st.initialized && st.locked && st.platform !== "darwin");
}

// reachRows: the rows of the "This agent will be able to" box.
//   profile  the ProfileView (with the account the form currently shows, if the
//            caller overrides it)
//   tier     "box" | "host" as the form currently has it
//   cwd      the workspace as it is to be shown
//   meta     /api/meta: aws_connections [{name, prod, write_capable}]
//   creds    GET /api/credentials, or null when it could not be read:
//            store {initialized, locked, platform}, aws [{name, prod, elevate{max_minutes}}]
// A row is {k, v, prod?, name?, tone?}. An AWS row with prod set is drawn by the
// caller as name + a red PROD tag + the rest of v (v starts with name). tone:
// "bad" (red, destructive reach) or "warn" (amber, needs you).
function reachRows(o) {
  const x = o || {};
  const p = x.profile || {};
  const host = x.tier === "host";
  const rows = [];
  rows.push(host
    ? { k: "Files", v: "your whole user account", tone: "bad" }
    : { k: "Files", v: (x.cwd || "the workspace") + " read/write · nothing else on this machine" });

  const metaConns = x.meta && Array.isArray(x.meta.aws_connections) ? x.meta.aws_connections : [];
  const credConns = x.creds && Array.isArray(x.creds.aws) ? x.creds.aws : [];
  for (const name of spawnNames(p.aws_connections)) {
    const m = metaConns.find((c) => c && c.name === name) || {};
    const c = credConns.find((e) => e && e.name === name) || {};
    const prod = m.prod === true || c.prod === true;
    const elevate = c.elevate && typeof c.elevate === "object" ? c.elevate : null;
    const writable = m.write_capable === true || !!elevate;
    let v = name + " · read";
    if (writable) {
      const max = elevate && Number(elevate.max_minutes) > 0 ? Number(elevate.max_minutes) : 0;
      v += " · write only after you elevate" + (max ? " (max " + max + " min)" : "");
    } else {
      v += " only";
    }
    const row = { k: "AWS", v, name };
    if (prod) row.prod = true;
    rows.push(row);
  }

  const owners = spawnNames(p.github_owners);
  if (owners.length) rows.push({ k: "GitHub", v: owners.map((n) => n + "/*").join(", ") + " · read, 1-hour tokens" });

  const tokens = spawnNames(p.tokens);
  if (tokens.length) rows.push({ k: "Tokens", v: tokens.join(", ") });

  if (host) rows.push({ k: "Network", v: "not contained", tone: "bad" });
  else if (p.network === "allowlist") rows.push({ k: "Network", v: "allowlist only" });
  else if (p.network === "open") rows.push({ k: "Network", v: "open" });

  if (p.claude) {
    const a = String(p.account || "");
    rows.push({ k: "Account", v: a && a !== "default" ? a : "default (~/.claude)" });
  }

  if (spawnStoreLocked(x.creds) && (spawnNames(p.aws_connections).length || owners.length || tokens.length)) {
    rows.push({ k: "Store", v: "credential store locked — unlock before spawn", tone: "warn" });
  }
  return rows;
}

// pickSpawnProfile: the profile the dialog opens on. The last used one when it
// still exists, otherwise the first that is not the quick-shell profile; the
// shell profile is never the default pick, even when it was the last used (it
// is one click away in the list).
function pickSpawnProfile(profiles, last, shellProfile) {
  const list = Array.isArray(profiles) ? profiles : [];
  const lastName = last && typeof last.agent === "string" ? last.agent : "";
  if (lastName && lastName !== shellProfile && list.some((p) => p.name === lastName)) return lastName;
  const first = list.find((p) => p.name !== shellProfile);
  if (first) return first.name;
  return list.length ? list[0].name : "";
}

// pickSpawnCwd: the workspace the dialog opens on. The profile's own default wins
// (it always did); then the last spawn's directory, but only when that spawn was
// of THIS profile, so a leftover directory of another profile is never prefilled;
// then the first workspace usable on the tier.
function pickSpawnCwd(o) {
  const x = o || {};
  const p = x.profile || {};
  if (p.default_cwd) return p.default_cwd;
  const last = x.last;
  if (last && last.cwd && p.name && last.agent === p.name) return last.cwd;
  return spawnDep("defaultWorkspace", "./tier.js")(x.workspaces || [], x.tier);
}

// spawnNamePlaceholder: what the Name field says it will use when left empty.
function spawnNamePlaceholder(cwd) {
  const b = spawnBase(cwd);
  return b ? "auto: " + b : "auto: working dir name";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { profileTouchLine, reachRows, pickSpawnProfile, pickSpawnCwd, spawnNamePlaceholder, spawnStoreLocked, spawnTierOf };
}
