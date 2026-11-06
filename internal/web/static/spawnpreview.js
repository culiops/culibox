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
// spawnAWS: the AWS entries a profile reaches, named connections first, then the
// legacy inline binding (profile.aws_inline) named like one. An unnamed inline
// entry is left out: a name is never guessed.
function spawnAWS(p) {
  const out = spawnNames(p && p.aws_connections).map((name) => ({ name }));
  const inl = p && p.aws_inline;
  if (inl && typeof inl.name === "string" && inl.name !== "") {
    out.push({ name: inl.name, inline: true, mode: inl.mode, prod: inl.prod === true, max: Number(inl.elevate_max_minutes) > 0 ? Number(inl.elevate_max_minutes) : 0 });
  }
  return out;
}
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
  const aws = spawnAWS(prof).map((e) => e.name), gh = spawnNames(prof.github_owners), tok = spawnNames(prof.tokens);
  if (aws.length) parts.push("aws " + aws.join(", "));
  if (gh.length) parts.push("gh " + gh.join(", "));
  if (tok.length) parts.push("tok " + tok.join(", "));
  if (prof.clone_of) parts.push("private copy of " + prof.clone_of);
  if (parts.length === 1) parts.push("no credentials");
  return parts.join(" · ");
}

// spawnStoreLocked: the store is set up, locked, and not the macOS keychain (the
// same test the Credentials tab uses for its Locked state).
function spawnStoreLocked(creds) {
  const st = creds && creds.store;
  return !!(st && st.initialized && st.locked && st.platform !== "darwin");
}

// SPAWN_GH_PERM_ORDER: the order GitHub permissions are listed in words (the same
// order the profile editor lists them, profileform.js GH_PERMS); a key it does not
// know is listed after these, alphabetically.
const SPAWN_GH_PERM_ORDER = ["actions", "checks", "contents", "pull_requests", "issues", "metadata", "statuses"];
const SPAWN_TOKEN_NOTE = "Long-lived; the agent can read it while it runs";
const spawnJoin = (l) => l.filter((x) => typeof x === "string" && x !== "").join(", ");
const spawnItem = (kind, name, over) => Object.assign({ kind, name, sub: "", scope: "", tag: "", missing: false }, over);

// spawnAWSFacts: what one AWS binding reaches. ent is spawnAWS' entry, metaConns
// /api/meta's aws_connections, credConns /api/credentials' aws. The inline binding
// is not in meta/credentials (those list named connections), so its own flags
// from the profile view are what is known. found: meta lists it (an inline one is
// always found).
function spawnAWSFacts(ent, metaConns, credConns) {
  const name = ent.name;
  const found = ent.inline ? true : metaConns.some((c) => c && c.name === name);
  const m = ent.inline ? { prod: ent.prod, write_capable: ent.max > 0 } : metaConns.find((c) => c && c.name === name) || {};
  const c = ent.inline ? {} : credConns.find((e) => e && e.name === name) || {};
  const elevate = ent.inline ? (ent.max > 0 ? { max_minutes: ent.max } : null) : c.elevate && typeof c.elevate === "object" ? c.elevate : null;
  return {
    m, found,
    prod: m.prod === true || c.prod === true,
    write: m.write_capable === true || !!elevate,
    max: elevate && Number(elevate.max_minutes) > 0 ? Number(elevate.max_minutes) : 0,
    // The gate admits read-only permission sets only and there is no elevate.
    direct: ent.mode === "sso_direct" || m.mode === "sso_direct" || c.mode === "sso_direct",
  };
}

function spawnAWSScope(f) {
  if (f.direct) return "Read only. Uses the SSO role directly.";
  if (!f.write) return "Read only";
  return "Read only. Write only after you grant a window" + (f.max ? ", max " + f.max + " min" : "") + ".";
}

// spawnGitHubScope: what a GitHub binding may do, from the EFFECTIVE permission set
// the server sends. A personal access token is not narrowed by CuliBox. With no
// set sent nothing is said about permissions, never a guess.
function spawnGitHubScope(p) {
  if (p.github_pat === true) return "Personal access token, not narrowed by CuliBox: it reaches whatever the token allows.";
  const perms = p.github_permissions && typeof p.github_permissions === "object" ? p.github_permissions : {};
  const at = (k) => (SPAWN_GH_PERM_ORDER.includes(k) ? SPAWN_GH_PERM_ORDER.indexOf(k) : SPAWN_GH_PERM_ORDER.length);
  const keys = Object.keys(perms).sort((a, b) => at(a) - at(b) || (a < b ? -1 : a > b ? 1 : 0));
  const words = (l) => l.map((k) => k.replace(/_/g, " ")).join(", ");
  const write = keys.filter((k) => perms[k] === "write");
  const read = keys.filter((k) => perms[k] !== "write");
  const out = [];
  if (write.length) out.push("Can write: " + words(write) + ".");
  if (read.length) out.push("Read only: " + words(read) + ".");
  out.push("1-hour tokens.");
  return out.join(" ");
}

// spawnTokenHosts: the hosts a token may go to, host:port when the port is not the
// TLS default.
function spawnTokenHosts(hosts) {
  return (Array.isArray(hosts) ? hosts : [])
    .filter((h) => h && typeof h.host === "string" && h.host !== "")
    .map((h) => (Number(h.port) > 0 && Number(h.port) !== 443 ? h.host + ":" + h.port : h.host));
}

// reachModel: everything the "This agent will be able to" views say, as data, in
// the words of the profile page (spec 5.3). New session's box (reachRows, below)
// and the profile read view both draw from it, so neither invents wording.
//   o.profile       the ProfileView (or what pfTouchProfile builds in its shape)
//   o.tier          "box" | "host" as the form currently has it
//   o.cwd           the workspace as it is to be shown
//   o.meta          /api/meta: aws_connections [{name, prod, write_capable, mode,
//                   account_id, region}], static_tokens [{name, prod, description,
//                   env, hosts}]. A list that is absent means nothing is known of
//                   it, so no name in it is called missing.
//   o.creds         GET /api/credentials or null: store, aws [{name, prod, elevate}]
//   o.bundleImports /api/meta bundle_imports: the CLAUDE.md imports item may appear
// Returns {files, original, cloud, reachable, tokenNote, network, account, store}:
//   files      the workspace line ("" never; the host says its whole account)
//   original   the read-only note about a private copy's original, "" when none
//   cloud      Item[]: AWS, Old-style AWS (the legacy inline binding), GitHub, Token
//   reachable  Item[]: Folder, From the original, Files (the imports), Domain; none on host
//   tokenNote  once-per-list plain line when any token is bound, else ""
//   network    {line, sub, open} for a boxed profile with a posture, else null
//   account    the agent account as shown ("" for a non-claude profile)
//   store      {v, tone:"warn"} when the credential store is locked and something is bound, else null
// Item = {kind, name, sub, scope, tag, missing}: tag "PROD" | "production" | "";
// missing: the name is bound but /api/meta does not list it (sub, scope and tag
// are then blank: nothing is guessed about it). AWS and GitHub items also carry
// `facts` (write, max, direct, pat), what reachRows needs for its one-line wording.
// Never an ARN, a token value or a reference: only what /api/meta and the profile
// view name.
function reachModel(o) {
  const x = o || {};
  const p = x.profile || {};
  const host = x.tier === "host";
  const metaConns = x.meta && Array.isArray(x.meta.aws_connections) ? x.meta.aws_connections : [];
  const credConns = x.creds && Array.isArray(x.creds.aws) ? x.creds.aws : [];
  const metaTokens = x.meta && Array.isArray(x.meta.static_tokens) ? x.meta.static_tokens : [];
  const awsKnown = !!x.meta && Array.isArray(x.meta.aws_connections);
  const tokensKnown = !!x.meta && Array.isArray(x.meta.static_tokens);

  const cloud = [];
  for (const ent of spawnAWS(p)) {
    const f = spawnAWSFacts(ent, metaConns, credConns);
    const facts = { write: f.write, max: f.max, direct: f.direct };
    if (ent.inline) {
      cloud.push(spawnItem("Old-style AWS", ent.name, { scope: spawnAWSScope(f), tag: f.prod ? "PROD" : "", facts }));
    } else if (awsKnown && !f.found) {
      cloud.push(spawnItem("AWS", ent.name, { missing: true, facts }));
    } else {
      cloud.push(spawnItem("AWS", ent.name, {
        sub: spawnJoin([f.m.account_id ? "account " + f.m.account_id : "", f.m.region]),
        scope: spawnAWSScope(f), tag: f.prod ? "PROD" : "", facts,
      }));
    }
  }

  const repoOf = (owner) => {
    const e = (Array.isArray(p.github_repos) ? p.github_repos : []).find((r) => r && r.owner === owner);
    return e ? spawnNames(e.repos) : [];
  };
  const ghFacts = { write: p.github_write === true, pat: p.github_pat === true };
  for (const owner of spawnNames(p.github_owners)) {
    const repos = repoOf(owner);
    cloud.push(spawnItem("GitHub", owner, { sub: repos.length ? "repo " + repos.join(", ") : "", scope: spawnGitHubScope(p), facts: ghFacts }));
  }

  const tokens = spawnNames(p.tokens);
  for (const name of tokens) {
    const t = metaTokens.find((e) => e && e.name === name);
    if (!t) {
      cloud.push(spawnItem("Token", name, { missing: tokensKnown }));
      continue;
    }
    const hosts = spawnTokenHosts(t.hosts);
    cloud.push(spawnItem("Token", name, {
      sub: spawnJoin([t.description].concat(Array.isArray(t.env) ? t.env : [])),
      scope: hosts.length ? "Reaches " + hosts.join(", ") : "",
      tag: t.prod === true ? "production" : "",
    }));
  }

  const reachable = [];
  if (!host) {
    for (const path of spawnNames(p.context_paths)) reachable.push(spawnItem("Folder", path, { scope: "Read only" }));
    for (const path of spawnNames(p.clone_share)) reachable.push(spawnItem("From the original", path, { scope: "Read only" }));
    if (x.bundleImports === true && !p.imports_off) {
      reachable.push(spawnItem("Files", "CLAUDE.md imports", { sub: "The files your CLAUDE.md pulls in with @", scope: "Read-only copies" }));
    }
    for (const d of spawnNames(p.egress_allow)) reachable.push(spawnItem("Domain", d, { scope: d.startsWith("*.") ? "HTTPS, its subdomains" : "HTTPS" }));
  }

  let network = null;
  if (!host && (p.network === "allowlist" || p.network === "open")) {
    const open = p.network === "open";
    network = {
      line: "Internet only through CuliBox's proxy, HTTPS to hostnames",
      sub: open
        ? "All internet hosts (no allowlist)."
        : "Because this profile holds cloud access and tokens, it is limited to Anthropic, AWS, GitHub and package registries, plus the domains above.",
      open,
    };
  }

  const acct = String(p.account || "");
  return {
    files: host ? "your whole user account" : (x.cwd || "the workspace") + " read/write · nothing else on this machine",
    // A private copy: the box edits its own copy; the original's history is mounted read-only.
    original: !host && typeof p.clone_of === "string" && p.clone_of !== ""
      ? spawnDep("shortPath", "./fsops.js")(p.clone_of) + "/.git read-only · the box cannot change your history" : "",
    cloud,
    reachable,
    tokenNote: tokens.length ? SPAWN_TOKEN_NOTE : "",
    network,
    account: p.claude ? (acct && acct !== "default" ? acct : "default (~/.claude)") : "",
    store: spawnStoreLocked(x.creds) && cloud.length ? { v: "credential store locked — unlock before spawn", tone: "warn" } : null,
  };
}

// reachRows: the rows of the "This agent will be able to" box, projected from
// reachModel (one line per fact; the profile page draws the same model in blocks).
//   profile  the ProfileView (with the account the form currently shows, if the
//            caller overrides it)
//   tier     "box" | "host" as the form currently has it
//   cwd      the workspace as it is to be shown
//   meta     /api/meta: aws_connections [{name, prod, write_capable}], static_tokens
//   creds    GET /api/credentials, or null when it could not be read:
//            store {initialized, locked, platform}, aws [{name, prod, elevate{max_minutes}}]
//   bundleImports  /api/meta bundle_imports: lists the CLAUDE.md imports row
// A row is {k, v, prod?, name?, tone?}. An AWS row with prod set is drawn by the
// caller as name + a red PROD tag + the rest of v (v starts with name). tone:
// "warn" (amber, needs you). Containment is never coloured: red is for PROD and
// write only (the PROD flag), a host session is told in words.
// Reads, Imports and Domains come after Tokens; on host there are none of them.
function reachRows(o) {
  const x = o || {};
  const m = reachModel(x);
  const rows = [{ k: "Files", v: m.files }];
  if (m.original) rows.push({ k: "Original", v: m.original });

  for (const it of m.cloud.filter((i) => i.kind === "AWS" || i.kind === "Old-style AWS")) {
    const f = it.facts;
    let v = it.name + " · read";
    if (f.direct) v += " only · SSO role directly";
    else if (f.write) v += " · write only after you elevate" + (f.max ? " (max " + f.max + " min)" : "");
    else v += " only";
    const row = { k: "AWS", v, name: it.name };
    if (it.tag === "PROD") row.prod = true;
    rows.push(row);
  }

  const gh = m.cloud.filter((i) => i.kind === "GitHub");
  if (gh.length) rows.push({ k: "GitHub", v: ghReachValue(gh.map((i) => i.name), gh[0].facts.write, gh[0].facts.pat) });

  const tokens = m.cloud.filter((i) => i.kind === "Token");
  if (tokens.length) rows.push({ k: "Tokens", v: tokens.map((i) => i.name).join(", ") });

  const names = (kinds) => m.reachable.filter((i) => kinds.includes(i.kind)).map((i) => i.name);
  const reads = names(["Folder", "From the original"]);
  if (reads.length) rows.push({ k: "Reads", v: reads.join(", ") });
  if (m.reachable.some((i) => i.kind === "Files")) rows.push({ k: "Imports", v: "CLAUDE.md imports, read-only copies" });
  const domains = names(["Domain"]);
  if (domains.length) rows.push({ k: "Domains", v: domains.join(", ") });

  if (x.tier === "host") rows.push({ k: "Network", v: "not contained" });
  else if (m.network) rows.push({ k: "Network", v: m.network.open ? "open" : "allowlist only" });

  if (m.account) rows.push({ k: "Account", v: m.account });
  if (m.store) rows.push({ k: "Store", v: m.store.v, tone: m.store.tone });
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

// spawnHotkeyAction: what a submit of the New session form does (the Spawn button,
// Ctrl/Cmd+Enter from any field, Retry after unlock). The workspace field
// doubles as a live search box, so its text can differ from the directory that
// will really be used (a fuzzy query, a path not resolved yet) and its dropdown
// can be open. Spawning then would start the session in the PREVIOUS directory
// while the field shows another, so the submit does what plain Enter does (pick
// or resolve) and the next one spawns. A field that shows exactly the directory
// in use spawns, even with its list open (the caller closes it), unless a row is
// highlighted: then the keypress takes that row, as Enter would.
function spawnHotkeyAction(o) {
  const x = o || {};
  if (x.listOpen && x.highlighted) return "pick";
  return String(x.text == null ? "" : x.text).trim() === String(x.cwd == null ? "" : x.cwd).trim() ? "submit" : "pick";
}

// ghReachValue: the GitHub row of "This agent will be able to". A PAT's reach is
// whatever the token was scoped to on GitHub, which CuliBox cannot read.
function ghReachValue(owners, write, pat) {
  const names = owners.map((n) => n + "/*").join(", ");
  if (pat) return names + " · personal access token: what the token allows";
  return names + (write ? " · read and write, 1-hour tokens" : " · read, 1-hour tokens");
}

// spawnNamePlaceholder: what the Name field says it will use when left empty.
function spawnNamePlaceholder(cwd) {
  const b = spawnBase(cwd);
  return b ? "auto: " + b : "auto: working dir name";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { profileTouchLine, reachRows, reachModel, pickSpawnProfile, pickSpawnCwd, spawnNamePlaceholder, spawnHotkeyAction, spawnStoreLocked, spawnTierOf, ghReachValue };
}
