"use strict";

// The profile page's read view as data (spec 5.2-5.4, 5.6): the header, the note
// and one block per question "what will this agent be able to do?". Pure: no
// DOM, no fetch, and no page state read (only the helpers it depends on read their
// own: shortPath reads window.HOME_DIR), so the page script only has to draw it.
// Everything about Cloud and tokens, Also reachable and Network is reachModel's
// (spawnpreview.js), the one builder New session shares, so no wording of those
// blocks is invented here; the rest is the approved mockup's
// (profile-mockups/shots/b-*.png). Every top-level name is pfv/PFV_ prefixed:
// the page scripts share one scope. Classic script: reachModel, shortPath and the
// not-home words are browser globals; under node they are required.

function pfvDep(name, file) {
  return typeof globalThis[name] === "function" ? globalThis[name] : require(file)[name];
}

const PFV_CONFIG_NOTE = "Defined in config.json. Duplicate it to change it.";
const PFV_MISSING = "Not found in Credentials";
// The options the Agent block shows; any other option that is set is Advanced.
const PFV_AGENT_OPTIONS = ["model", "effort", "effortLevel"];
const PFV_HOST_SCOPE = "Read and write, like anything else you own";

const pfvText = (v) => (typeof v === "string" ? v.trim() : "");
const pfvList = (v) => (Array.isArray(v) ? v.filter((x) => typeof x === "string" && x !== "") : []);
const pfvBinName = (p) => String(p || "").split("/").filter(Boolean).pop() || "";
// An option counts as set when a bool is on or an enum has a value.
const pfvSet = (v) => v === true || (typeof v === "string" && v.trim() !== "");
const pfvRow = (kind, name, over) => Object.assign({ kind, name, sub: "", scope: "", tag: "", missing: false }, over);

// pfvTierOf: the tier the profile runs under, as the page shows it and as every save writes it. The stored
// profile's own runtime when it names "box" or "host" (the document the PUT is checked against by revision);
// only a profile that stores none takes the resolved tier, the list view's runtime (the instance default, else
// box). The list row can be stale (a failed list refetch after a save, a refetch that lands before the profile
// does), and a stale row must neither misdraw the page nor change where the profile runs on a save with no host
// confirm. Not the effective tier: without a box layer a boxed profile is still a boxed profile, it just cannot
// start (that is what the Runs in block says).
function pfvTierOf(view, doc) {
  const v = view && typeof view === "object" ? view : {};
  const d = doc && typeof doc === "object" ? doc : {};
  if (d.runtime === "box" || d.runtime === "host") return d.runtime;
  return v.runtime === "host" ? "host" : "box";
}

// pfvField: a value the list view and the stored profile both carry. The view
// wins (it is what New session reads); the stored profile is the fallback.
function pfvField(view, doc, key) {
  return view[key] !== undefined && view[key] !== null ? view[key] : doc[key];
}

// pfvOptions: the profile's option defaults as {key: value}: the stored profile's
// map, else what the list view's options carry.
function pfvOptions(view, doc) {
  if (doc.options && typeof doc.options === "object" && !Array.isArray(doc.options)) return doc.options;
  const out = {};
  for (const o of Array.isArray(view.options) ? view.options : []) if (o && typeof o.key === "string") out[o.key] = o.value;
  return out;
}

// pfvAdvancedItems: what the Advanced block holds that is not at its default,
// one row per setting. The starting environment counts on host only (a box does
// not use it); environment variables are listed by name, never by value.
function pfvAdvancedItems(doc, tier, defs) {
  const d = doc && typeof doc === "object" ? doc : {};
  const t = tier || (d.runtime === "host" ? "host" : "box");
  const rows = [];
  if (t === "host" && typeof d.base === "string" && d.base !== "" && d.base !== "inherit") {
    rows.push(pfvRow("Starting environment", d.base, { scope: d.base === "clean" ? "No secrets" : d.base === "login" ? "Source profile" : "" }));
  }
  const args = pfvList(d.args);
  if (args.length) rows.push(pfvRow("Arguments", args.join(" ")));
  const env = d.env && typeof d.env === "object" && !Array.isArray(d.env) ? Object.keys(d.env).sort() : [];
  if (env.length) rows.push(pfvRow("Environment", env.join(", ")));
  const secrets = pfvList(d.secrets);
  if (secrets.length) rows.push(pfvRow("Secrets", secrets.join(", ")));
  if (d.replyable === true) rows.push(pfvRow("Replies", "Allow replies by default"));
  if (d.imports_off === true) rows.push(pfvRow("Files", "CLAUDE.md imports", { scope: "Not given to the box" }));
  const opts = d.options && typeof d.options === "object" && !Array.isArray(d.options) ? d.options : {};
  for (const key of Object.keys(opts)) {
    if (PFV_AGENT_OPTIONS.includes(key) || !pfvSet(opts[key])) continue;
    const def = (Array.isArray(defs) ? defs : []).find((x) => x && x.key === key);
    rows.push(pfvRow((def && def.label) || key, opts[key] === true ? "On" : String(opts[key])));
  }
  return rows;
}

// pfvAdvancedCount: how many Advanced settings are not at their default. tier is
// optional: without it the stored profile's runtime decides.
function pfvAdvancedCount(doc, tier) {
  return pfvAdvancedItems(doc, tier, []).length;
}

// pfvPosture: the network posture the profile has, for reachModel. The list
// view reports none for a boxed profile on a machine with no box layer, but the
// posture it will have once boxes exist is fixed by the profile alone (with a box
// layer every boxed spawn has egress: bound means an allowlist unless egress_open,
// unbound means open), so the block still reads from the builder.
function pfvPosture(view, doc, tier) {
  if (tier !== "box") return "";
  if (view.network === "allowlist" || view.network === "open") return view.network;
  const bound = pfvList(view.aws_connections).length > 0 || !!view.aws_inline || pfvList(view.github_owners).length > 0 || pfvList(view.tokens).length > 0;
  return bound && doc.egress_open !== true ? "allowlist" : "open";
}

// pfvGithubLogins: how each GitHub owner of the stored profile signs in, {owner: "app:<name>" | "token:<name>"}, for
// reachModel's missing mark. ProfileView carries no App or token name, so it comes from the stored profile (names only).
function pfvGithubLogins(doc) {
  const gh = doc && typeof doc.github === "object" && doc.github ? doc.github : null;
  const out = {};
  for (const o of gh && Array.isArray(gh.owners) ? gh.owners : []) {
    if (!o || typeof o !== "object" || pfvText(o.owner) === "") continue;
    if (pfvText(o.token) !== "") out[o.owner] = "token:" + o.token;
    else if (pfvText(o.app) !== "") out[o.owner] = "app:" + o.app;
  }
  return out;
}

// pfvUnavailable: the two lines a box tier profile shows when there is no box
// layer. reason is /api/meta's box_unavailable_reason, already a clause
// ("Docker is not installed", "run `agent-box doctor` for details"). refused says
// a session of this profile is refused without a box (pfvReadView works it out): it
// asks for box, or it holds a readable folder or a private copy, which a session on
// this machine refuses. Then the line says so. A profile that asks for nothing and
// holds neither only RESOLVES to box: with no box layer such a session runs on this
// machine, with no container, so the line says that instead (internal/session runtimeFor).
function pfvUnavailable(reason, refused) {
  const r = pfvText(reason);
  const m = /^(Docker|colima) is not installed$/i.exec(r);
  let consequence;
  if (refused) consequence = "Sessions with this profile can't start until " + (m ? m[1] + " is installed" : "boxes are available") + ".";
  else consequence = (m ? "Until it is" : "Until boxes are available") + ", sessions start on this machine, uncontained.";
  return {
    line: "Boxes are unavailable on this machine" + (r ? ": " + r : "") + ".",
    consequence,
  };
}

function pfvAgentParts(view, doc) {
  const o = pfvOptions(view, doc);
  const claude = view.claude === true;
  const bin = pfvBinName(pfvField(view, doc, "bin"));
  return { claude, bin, model: pfvText(o.model), effort: pfvText(o.effort) || pfvText(o.effortLevel) };
}

// pfvAgentLine: "Claude Code · <model> · <effort> effort", only the parts that
// are set, "agent's default model" when a Claude Code profile sets none. Another
// binary is named by its binary.
function pfvAgentLine(a) {
  const parts = [a.claude ? "Claude Code" : a.bin || "agent"];
  if (a.model) parts.push(a.model);
  else if (a.claude) parts.push("agent's default model");
  if (a.effort) parts.push(a.effort + " effort");
  return parts.join(" · ");
}

function pfvAgentBlock(view, doc, a, edit) {
  const withParts = [a.model || (a.claude ? "its default model" : ""), a.effort ? a.effort + " effort" : ""].filter(Boolean);
  const main = (a.claude ? "Claude Code" : a.bin || "agent") + (withParts.length ? " with " + withParts.join(", ") : "");
  if (!a.claude) return { id: "agent", label: "Agent", lines: [main], rows: [], edit, add: "", count: 0, note: "" };
  const acct = pfvText(pfvField(view, doc, "account"));
  const sub = acct && acct !== "default" ? "Signs in with the " + acct + " Claude login" : "Signs in with the default Claude login (~/.claude)";
  return { id: "agent", label: "Agent", lines: [main, sub], rows: [], edit, add: "", count: 0, note: "" };
}

// pfvWorkspaceBlock: the folder the sessions work in. A private copy lists the
// folder it copies, where the copy lives and, as today's profile form does, the
// branches that did not come home with their reason and fix.
function pfvWorkspaceBlock(view, doc, host, edit) {
  const short = pfvDep("shortPath", "./fsops.js");
  const cwd = pfvText(pfvField(view, doc, "default_cwd"));
  const cloneOf = host ? "" : pfvText(pfvField(view, doc, "clone_of"));
  const rows = [];
  let notHome = null;
  if (cloneOf) {
    const auto = view.auto_return === true;
    rows.push(pfvRow("Folder", "Private copy of " + short(cloneOf), {
      sub: auto ? "Its branches come home as box/<branch>, automatically" : "Its work stays in the copy until you bring it home (auto-return is off)",
      scope: "Untouched until work comes home",
    }));
    const share = pfvList(pfvField(view, doc, "clone_share"));
    if (cwd) rows.push(pfvRow("Copy", short(cwd), { sub: share.length ? "Shares " + share.join(", ") + " from the original, read only" : "", scope: "Read and write" }));
    const nh = view.not_home;
    const branches = nh && Array.isArray(nh.branches) ? nh.branches : [];
    const reason = pfvDep("notHomeReasonText", "./workhome.js"), fix = pfvDep("notHomeFix", "./workhome.js");
    for (const b of branches) {
      rows.push(pfvRow("Not home", b.branch || "", { sub: b.detail || "", scope: reason(b), fix: fix(view.name || doc.name || "", nh.base, b) }));
    }
    const label = pfvDep("notHomeLabel", "./workhome.js")(nh);
    if (!branches.length && nh && nh.error) rows.push(pfvRow("Not home", "", { scope: label + ": " + nh.error, fix: "" }));
    if (label) notHome = { label, title: pfvDep("notHomeTitle", "./workhome.js")(nh) };
  } else if (host) {
    rows.push(pfvRow("Folder", cwd ? short(cwd) : "No default folder", { sub: cwd ? "Sessions start here" : "Picked in New session", scope: PFV_HOST_SCOPE }));
  } else {
    rows.push(pfvRow("Folder", cwd ? short(cwd) : "No default folder", {
      sub: cwd ? "Edits the folder directly, not a private copy" : "Picked in New session", scope: "Read and write",
    }));
  }
  return { id: "workspace", label: "Workspace", lines: [], rows, edit, add: "", count: rows.length, note: "", notHome };
}

// refused: a session of this profile is refused without a box layer (see pfvUnavailable).
// daemon: /api/meta's box_daemon_reason (#154), why a box layer that exists cannot be used right now
// ("Docker is not running"); "" when it can, and never given while there is no layer.
function pfvRunsBlock(host, copy, unavailable, reason, edit, refused, daemon) {
  const b = { id: "runs", label: "Runs in", lines: [], rows: [], edit, add: "", count: 0, note: "" };
  const u = pfvUnavailable(reason, refused);
  const dline = daemon ? "Boxes can't start right now: " + daemon + "." : "";
  if (daemon && host) {
    // Quiet, like the no-layer reason below: this profile does not need a box.
    b.lines.push("On this machine: runs as you, with your files, your network and your credentials", dline);
  } else if (daemon) {
    // The two named reasons refuse every session of a box profile (the layer's Prepare). Any other
    // (the doctor pointer) is not refused there: the spawn's own docker calls may still work.
    const refusing = /^(Docker is not running|no permission to use Docker)$/.test(daemon);
    b.lines.push("In a box", dline, refusing ? "Sessions with this profile can't start until that is fixed." : "Sessions with this profile may fail to start.");
    b.tone = "error";
    b.toneAt = 1;
  } else if (host) {
    // The spec's sentence, verbatim, on one line (spec 5.4). The muted line under it
    // carries only the boxes-unavailable reason when there is one, quietly: not an
    // error, this profile does not need a box.
    b.lines.push("On this machine: runs as you, with your files, your network and your credentials", unavailable ? u.line : "");
  } else if (unavailable) {
    b.lines.push("In a box", u.line, u.consequence);
    b.tone = "error";
    b.toneAt = 1;
  } else {
    b.lines.push("In a box", "A container on this machine. It can write only to its " + (copy ? "private copy" : "workspace") + " and reach only what this page lists.");
  }
  return b;
}

// pfvReadView: the whole read view of one profile.
//   o.view                 the profile's ProfileView (GET /api/profiles)
//   o.doc                  the stored profile (GET /api/profiles/{name})
//   o.meta, o.creds        /api/meta and /api/credentials (reachModel's inputs; meta's
//                          github_apps and static_tokens say whether a GitHub owner's login still exists)
//   o.githubLogins         optional: {owner: "app:<name>" | "token:<name>"} in place of the stored
//                          profile's (an open editor says which login the form holds now)
//   o.boxRuntime           /api/meta box_runtime: "" means no box layer; anything
//                          else, or not loaded yet, means boxes can run
//   o.boxUnavailableReason /api/meta box_unavailable_reason, a clause
//   o.boxDaemonReason      /api/meta box_daemon_reason, a clause: the layer exists but its
//                          docker daemon cannot be used right now (#154); "" or absent when it can
//   o.bundleImports        /api/meta bundle_imports
//   o.defs                 GET /api/options, to name an Advanced option
// Returns {header, note, blocks}:
//   header  {name, tier: "box"|"host", agentLine, readOnly, source}
//   note    the quiet line of a profile defined in config.json, else ""
//   blocks  [{id, label, lines, rows, edit, add, count, note}] in page order
//     runs, workspace, agent, cloud, reachable, network, advanced (box)
//     runs, workspace, agent, canreach, advanced (host)
//   lines   the statements, the first is the headline, the rest are muted; an
//           empty string is a line not drawn (host Runs in has a second, "" unless
//           boxes are unavailable). In a block with tone "error", the line at
//           toneAt is the red one.
//   rows    {kind, name, sub, scope, tag, missing} (reachModel's Item) plus, only
//           where it applies, fix (a Not home row), action (Old-style AWS) or
//           original (a file shared from a private copy's original: a Files row
//           that is not the CLAUDE.md imports one).
//           A row with missing:true carries scope "Not found in Credentials",
//           which the page draws red.
//   edit    the block has an Edit link; add is "" or the "+ Add ..." link text.
//   Workspace also carries notHome {label, title} (the chip) or null; Advanced
//   {count, open, status}; Can reach {switchToBox}.
// A read-only profile (config.json or built in) has edit false and no add action
// anywhere. Never an ARN, a token value or a reference: only names.
function pfvReadView(o) {
  const x = o || {};
  const view = x.view && typeof x.view === "object" ? x.view : {};
  const doc = x.doc && typeof x.doc === "object" ? x.doc : {};
  const readOnly = view.editable === false || view.source === "config" || view.source === "builtin";
  const edit = !readOnly;
  const tier = pfvTierOf(view, doc);
  const host = tier === "host";
  const unavailable = typeof x.boxRuntime === "string" && x.boxRuntime === "";
  const copy = !host && pfvText(pfvField(view, doc, "clone_of")) !== "";
  const a = pfvAgentParts(view, doc);
  // Whether a session of this profile is refused without a box layer. It asks for box: the stored profile's own tier comes first;
  // only a profile that stores none takes what the instance's default_runtime asks (the list row's runtime_asked: runtime is the
  // resolved tier, which ends in box when nobody said anything, so it cannot tell the two apart). Or it holds a readable folder
  // or a private copy: those are refused on this machine, and a profile that names no tier can hold them (the server saves it,
  // because its default resolves to box), so such a session is refused rather than started uncontained.
  const stored = doc.runtime === "box" || doc.runtime === "host";
  const asksBox = stored ? doc.runtime === "box" : view.runtime_asked === "box";
  const refused = asksBox || copy || pfvList(pfvField(view, doc, "context_paths")).length > 0;

  const blocks = [
    pfvRunsBlock(host, copy, unavailable, x.boxUnavailableReason, edit, refused, unavailable ? "" : pfvText(x.boxDaemonReason)),
    pfvWorkspaceBlock(view, doc, host, edit),
    pfvAgentBlock(view, doc, a, edit),
  ];

  if (host) {
    blocks.push({
      id: "canreach", label: "Can reach", edit: false, add: "", rows: [], count: 0, note: "",
      lines: [
        "Everything you can",
        "Your whole home, your SSH keys, the Docker socket and any website.",
        "Scoped AWS, GitHub, tokens and network limits need a box; CuliBox cannot scope them on this machine.",
      ],
      // Offered only when it can be used: with no box layer the reason is already in Runs in.
      switchToBox: edit && !unavailable,
    });
  } else {
    const short = pfvDep("shortPath", "./fsops.js");
    const profile = Object.assign({}, view, { network: pfvPosture(view, doc, tier) });
    const m = pfvDep("reachModel", "./spawnpreview.js")({
      profile, tier, cwd: short(pfvText(pfvField(view, doc, "default_cwd"))), meta: x.meta, creds: x.creds, bundleImports: x.bundleImports,
      githubLogins: x.githubLogins || pfvGithubLogins(doc),
    });
    const item = (it) => {
      const r = pfvRow(it.kind, it.name, { sub: it.sub, scope: it.scope, tag: it.tag, missing: it.missing === true });
      if (it.original === true) r.original = true;
      if (r.missing) r.scope = PFV_MISSING;
      if (it.kind === "Old-style AWS" && edit) r.action = "Move to a saved connection";
      return r;
    };
    const cloud = m.cloud.map(item), reachable = m.reachable.map(item);
    blocks.push({
      id: "cloud", label: "Cloud and tokens", edit, add: edit ? "+ Add access" : "", rows: cloud, count: cloud.length, note: m.tokenNote,
      // The value, then its helper line (frame M2.1): the page draws them as a value and a muted line, the block in edit as one sentence.
      lines: cloud.length ? [] : ["Nothing yet", "It cannot use AWS, GitHub or any token."],
    });
    blocks.push({
      id: "reachable", label: "Also reachable", edit, add: edit ? "+ Add folder or domain" : "", rows: reachable, count: reachable.length, note: "",
      lines: reachable.length ? [] : ["Nothing yet", "It cannot read other folders or reach other domains."],
    });
    blocks.push({ id: "network", label: "Network", edit, add: "", rows: [], count: 0, note: "", lines: m.network ? [m.network.line, m.network.sub] : [] });
  }

  const adv = pfvAdvancedItems(doc, tier, x.defs);
  const sentence = host
    ? "Starting environment, arguments, environment variables and sidebar replies."
    : "Arguments, environment variables, sidebar replies and CLAUDE.md imports.";
  blocks.push({
    id: "advanced", label: "Advanced", edit, add: "", rows: adv, count: adv.length, note: "",
    lines: [adv.length ? sentence : sentence + " All at their defaults."],
    open: adv.length > 0, status: adv.length ? adv.length + " set" : "none set",
  });

  return {
    header: { name: view.name || doc.name || "", tier, agentLine: pfvAgentLine(a), readOnly, source: view.source || "" },
    note: readOnly ? PFV_CONFIG_NOTE : "",
    blocks,
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { pfvReadView, pfvAdvancedCount };
}
