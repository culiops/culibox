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
// spawnIsShellProfile: the profile runs a plain shell (its bin is one of SPAWN_SHELL_BINS), so a session of it has no agent.
// A capability test on what the profile runs, the one profileTouchLine uses for "host · bash", never "is it Claude".
function spawnIsShellProfile(p) {
  return !!p && SPAWN_SHELL_BINS.has(spawnBase(p.bin));
}

// spawnTierOf: the tier a profile runs under. boxRuntime, when given, is the
// answer of /api/meta: without a box layer every session is host tier whatever
// the profile says.
function spawnTierOf(profile, boxRuntime) {
  if (typeof boxRuntime === "string" && boxRuntime === "") return "host";
  return profile && profile.runtime === "host" ? "host" : "box";
}

// profileTouchLine: what a session of this profile can reach, in a line:
//   box · aws culipulse-prod · gh culiops     host · not contained     host · bash
// A private copy's source is written with the home prefix as ~ (frame M4.1, "private copy of ~/culiops-agent-box"), the way
// every other path on the page is: shortPath (fsops.js) reads the page's HOME_DIR and leaves a path outside home whole.
// Its callers are the New session list and the empty state, both desktop-only.
// boxRuntime is passed only once /api/meta has answered (`metaLoaded ? boxRuntime : undefined`): its "" before that reads as
// "no box layer" and would call every box profile "host · not contained"; undefined falls back to the profile's own runtime.
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
  if (prof.clone_of) parts.push("private copy of " + spawnDep("shortPath", "./fsops.js")(prof.clone_of));
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
//   o.profile       the ProfileView
//   o.tier          "box" | "host" as the form currently has it
//   o.cwd           the workspace as it is to be shown
//   o.meta          /api/meta: aws_connections [{name, prod, write_capable, mode,
//                   account_id, region}], static_tokens [{name, prod, description,
//                   env, hosts}]. A list that is absent means nothing is known of
//                   it, so no name in it is called missing.
//   o.creds         GET /api/credentials or null: store, aws [{name, prod, elevate}]
//   o.githubLogins  {owner: "app:<name>" | "token:<name>"}: how each GitHub owner signs in. ProfileView names no App or
//                   token, so a caller that holds the stored profile passes it; without it (New session) no owner is
//                   called missing. meta.github_apps (names) and meta.static_tokens are what the login is checked against.
//   o.bundleImports /api/meta bundle_imports: the CLAUDE.md imports item may appear
// Returns {files, original, cloud, reachable, tokenNote, network, account, store}:
//   files      the workspace line ("" never; the host says its whole account)
//   original   the read-only note about a private copy's original, "" when none
//   cloud      Item[]: AWS, Old-style AWS (the legacy inline binding), GitHub, Token
//   reachable  Item[]: Folder, Files (the original's shared files, original: true, and the imports item), Domain; none on host
//   tokenNote  once-per-list plain line when any token is bound, else ""
//   network    {line, sub, open} for a boxed profile with a posture, else null: an allowlist
//              (anything bound), open with nothing bound (any public website over HTTPS),
//              or egress_open on a bound profile (all internet hosts, no allowlist)
//   account    the agent account as shown ("" for a non-claude profile)
//   store      {v, tone:"warn"} when the credential store is locked and something is bound, else null
// Item = {kind, name, sub, scope, tag, missing}: tag "PROD" | "production" | "";
// missing: the name is bound but /api/meta does not list it (sub, scope and tag
// are then blank: nothing is guessed about it; a GitHub owner whose App or token is gone
// keeps its sub, the repositories the profile itself names). AWS and GitHub items also carry
// `facts` (AWS: write, max, direct, prod; GitHub: write, pat), what reachRows needs
// for its one-line wording. facts.prod is meta OR credentials, so a connection that
// only /api/credentials marks prod still draws the red PROD flag in reachRows.
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
  const metaApps = x.meta && Array.isArray(x.meta.github_apps) ? x.meta.github_apps : null;

  const cloud = [];
  for (const ent of spawnAWS(p)) {
    const f = spawnAWSFacts(ent, metaConns, credConns);
    const facts = { write: f.write, max: f.max, direct: f.direct, prod: f.prod };
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
  const logins = x.githubLogins && typeof x.githubLogins === "object" ? x.githubLogins : {};
  // The login the owner signs in with is gone from /api/meta (an App by name; a token by entry name, the server asks no more of it).
  const loginGone = (owner) => {
    const pick = Object.prototype.hasOwnProperty.call(logins, owner) && typeof logins[owner] === "string" ? logins[owner] : "";
    const has = (list, n) => list.some((e) => (typeof e === "string" ? e : e && e.name) === n);
    if (pick.startsWith("app:")) return metaApps !== null && !has(metaApps, pick.slice(4));
    if (pick.startsWith("token:")) return tokensKnown && !has(metaTokens, pick.slice(6));
    return false;
  };
  for (const owner of spawnNames(p.github_owners)) {
    const repos = repoOf(owner);
    const sub = repos.length ? "repo " + repos.join(", ") : "";
    cloud.push(loginGone(owner)
      ? spawnItem("GitHub", owner, { sub, missing: true, facts: ghFacts })
      : spawnItem("GitHub", owner, { sub, scope: spawnGitHubScope(p), facts: ghFacts }));
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
    // The original's shared files: a Files row like the imports item (the b-clone shot), told apart from it by `original`.
    for (const path of spawnNames(p.clone_share)) reachable.push(spawnItem("Files", path, { scope: "From the original, read only", original: true }));
    if (x.bundleImports === true && !p.imports_off) {
      reachable.push(spawnItem("Files", "CLAUDE.md imports", { sub: "The files your CLAUDE.md pulls in with @", scope: "Read-only copies" }));
    }
    for (const d of spawnNames(p.egress_allow)) reachable.push(spawnItem("Domain", d, { scope: d.startsWith("*.") ? "HTTPS, its subdomains" : "HTTPS" }));
  }

  // Three postures, told apart by what the profile holds. "open" is two different
  // things: a box with nothing bound (the proxy's open mode: any public host on
  // 443, the port, private-address, tailnet and host-address checks still apply,
  // internal/egress/proxy.go) and a bound box whose profile sets egress_open (no
  // allowlist although it holds credentials). The cloud list says which.
  let network = null;
  if (!host && (p.network === "allowlist" || p.network === "open")) {
    const open = p.network === "open";
    if (open && cloud.length === 0) {
      network = {
        line: "Any public website over HTTPS, through CuliBox's proxy",
        sub: "An allowlist starts once it holds cloud access or a token. Private, tailnet and host addresses stay blocked.",
        open,
      };
    } else {
      network = {
        line: "Internet only through CuliBox's proxy, HTTPS to hostnames",
        sub: open
          ? "All internet hosts (no allowlist)."
          : "Because this profile holds cloud access and tokens, it is limited to Anthropic, AWS, GitHub and package registries, plus the domains and token hosts above.",
        open,
      };
    }
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

const SPAWN_CLAUDE_HOME = "~/.claude";
// spawnExpand: `text` with its leading `short` path put back as `full`; text that does not start with `short` (or an
// absent `full`) comes back as it was, so a tooltip never says more than the row does.
function spawnExpand(text, short, full) {
  return short && full && text.startsWith(short) ? full + text.slice(short.length) : text;
}

// reachRows: the rows of the "This agent will be able to" box, projected from
// reachModel (one line per fact; the profile page draws the same model in blocks).
//   profile  the ProfileView (with the account the form currently shows, if the
//            caller overrides it)
//   tier     "box" | "host" as the form currently has it
//   cwd      the workspace as it is to be shown (home written ~)
//   cwdFull  the same workspace as the field holds it: the row's tooltip says this one
//   meta     /api/meta: aws_connections [{name, prod, write_capable}], static_tokens
//   creds    GET /api/credentials, or null when it could not be read:
//            store {initialized, locked, platform}, aws [{name, prod, elevate{max_minutes}}]
//   bundleImports  /api/meta bundle_imports: lists the CLAUDE.md imports row
// A row is {k, v, prod?, name?, tone?, full?, path?}: `full` is the tooltip text when it is longer than `v` (a path
// not abbreviated), `path` the part of `v` drawn as machine text. An AWS row with prod set is drawn by the
// caller as name + a red PROD tag + the rest of v (v starts with name). tone:
// "warn" (amber, needs you). Containment is never coloured: red is for PROD and
// write only (the PROD flag), a host session is told in words.
// Reads, Imports and Domains come after Tokens; on host there are none of them.
function reachRows(o) {
  const x = o || {};
  const m = reachModel(x);
  const shortOf = spawnDep("shortPath", "./fsops.js");
  const files = { k: "Files", v: m.files };
  // The workspace is drawn short (~/…), its tooltip long: `full` is the same text with the path as the field holds it,
  // `path` the part of `v` drawn as machine text. Neither is sent when there is no workspace, or on the host.
  if (x.tier !== "host" && x.cwd) {
    files.path = x.cwd;
    const full = spawnExpand(m.files, x.cwd, x.cwdFull);
    if (full !== m.files) files.full = full;
  }
  const rows = [files];
  if (m.original) {
    const clone = String(x.profile && x.profile.clone_of || "");
    const row = { k: "Original", v: m.original, path: shortOf(clone) + "/.git" };
    const full = spawnExpand(m.original, shortOf(clone), clone);
    if (full !== m.original) row.full = full;
    rows.push(row);
  }

  for (const it of m.cloud.filter((i) => i.kind === "AWS" || i.kind === "Old-style AWS")) {
    const f = it.facts;
    let v = it.name + " · read";
    if (f.direct) v += " only · SSO role directly";
    else if (f.write) v += " · write only after you elevate" + (f.max ? " (max " + f.max + " min)" : "");
    else v += " only";
    const row = { k: "AWS", v, name: it.name };
    if (f.prod) row.prod = true;
    rows.push(row);
  }

  const gh = m.cloud.filter((i) => i.kind === "GitHub");
  if (gh.length) rows.push({ k: "GitHub", v: ghReachValue(gh.map((i) => i.name), gh[0].facts.write, gh[0].facts.pat) });

  const tokens = m.cloud.filter((i) => i.kind === "Token");
  if (tokens.length) rows.push({ k: "Tokens", v: tokens.map((i) => i.name).join(", ") });

  const names = (kinds) => m.reachable.filter((i) => kinds.includes(i.kind)).map((i) => i.name);
  const reads = m.reachable.filter((i) => i.kind === "Folder" || i.original === true).map((i) => i.name);
  if (reads.length) rows.push({ k: "Reads", v: reads.join(", "), path: reads.join(", ") });
  if (m.reachable.some((i) => i.kind === "Files" && i.original !== true)) rows.push({ k: "Imports", v: "CLAUDE.md imports, read-only copies" });
  const domains = names(["Domain"]);
  if (domains.length) rows.push({ k: "Domains", v: domains.join(", ") });

  if (x.tier === "host") rows.push({ k: "Network", v: "not contained" });
  else if (m.network) rows.push({ k: "Network", v: m.network.open ? "open" : "allowlist only" });

  if (m.account) {
    const row = { k: "Account", v: m.account };
    if (m.account.includes(SPAWN_CLAUDE_HOME)) row.path = SPAWN_CLAUDE_HOME; // "default (~/.claude)": the folder is machine text
    rows.push(row);
  }
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

// spawnCwdChoice: the workspace the dialog opens on, and where it came from (the help line under the field says
// so: "From the profile", "Last used"). The profile's own default wins (it always did); then the last spawn's
// directory, but only when that spawn was of THIS profile, so a leftover directory of another profile is never
// prefilled; then the first workspace usable on the tier.
//   o.skipScratch (default false; the desktop passes true): a last directory under /tmp is not prefilled either (the
//   session of a throwaway run, F38: it reads as a leftover test path). Off, the answer is the one the dialog always
//   gave, which is what the phone keeps.
//   -> {cwd, source: "profile" | "last" | "default"}
function spawnCwdChoice(o) {
  const x = o || {};
  const p = x.profile || {};
  if (p.default_cwd) return { cwd: p.default_cwd, source: "profile" };
  const last = x.last;
  if (last && last.cwd && p.name && last.agent === p.name && !(x.skipScratch === true && spawnScratchCwd(last.cwd))) return { cwd: last.cwd, source: "last" };
  return { cwd: spawnDep("defaultWorkspace", "./tier.js")(x.workspaces || [], x.tier), source: "default" };
}

// spawnScratchCwd: /tmp itself or anything under it, whole components ("/tmpfoo" is not scratch).
function spawnScratchCwd(p) {
  const t = String(p).replace(/\/+$/, "");
  return t === "/tmp" || t.startsWith("/tmp/");
}

// pickSpawnCwd: the workspace alone.
function pickSpawnCwd(o) {
  return spawnCwdChoice(o).cwd;
}

// spawnWsNoteFor: the help line under the Workspace field while it still holds what the dialog opened on (frames
// M4.1 and M4.2): "Last used · 48 project roots · type to filter · ↵ to pick", "From the profile · type to
// filter · ↵ to pick" (no count: the profile named the folder, the roots are not what is being offered), and the
// plain caption for a default.
function spawnWsNoteFor(source, count) {
  const idle = spawnDep("spawnWsIdleNote", "./dirpicker.js");
  if (source === "last") return "Last used · " + idle(count);
  if (source === "profile") return "From the profile · type to filter · \u21b5 to pick";
  return idle(count);
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
  module.exports = { spawnIsShellProfile, profileTouchLine, reachRows, reachModel, pickSpawnProfile, pickSpawnCwd, spawnCwdChoice, spawnWsNoteFor, spawnNamePlaceholder, spawnHotkeyAction, spawnStoreLocked, spawnTierOf, ghReachValue };
}
