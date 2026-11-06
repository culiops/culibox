"use strict";

// The profile page's edit model (spec 5.5, 5.6): what one block's Save writes
// into the loaded profile, which block a refused save belongs to, when a second
// edit or a leave needs a question, what the switch to host removes, and what a
// failed save means. Pure: no DOM, no fetch, no page state, so the page script
// (profilepage.js) only has to draw it and every rule here is tested on its own.
// Every top-level name is pfv/PFE_/pfe prefixed: the page scripts share one
// scope. Classic script: the form helpers (runtimeToSend, wmFieldsToSend, ...)
// are browser globals; under node they are required.
//
// pfvMergeBlock(doc, blockId, values) is the one place a block's controls become
// stored keys. `values` is what the block's controls hold. A key the values do
// not name keeps its stored value, which is also how a control the user cannot
// see or use is left alone (spec 9: a hidden value is preserved); a key they name
// with an empty value (undefined, null, "", false, [] or {}) is removed. Per block:
//
//   runs       { tier: "box"|"host", boxAvailable }
//                runtime, through runtimeToSend (always explicit)
//   workspace  { mode: "folder"|"copy", folder, copyDir, shareText, autoReturn }
//                default_cwd, clone_of, clone_share, clone_no_auto_return, through
//                wmFieldsToSend; the four are replaced as a unit, and no mode writes nothing
//   agent      { bin, account, options: { model, effort, effortLevel } }
//   cloud      { aws_connections: [name], tokens: [name], github: binding|null, aws: null }
//                github is ghBindingFromForm's answer; its egress_allow is carried when
//                not stated; the legacy aws is never edited, only removed (aws: null)
//   reachable  { context_paths: [path], egress_allow: [domain] }
//   network    { egress_open: bool }
//   advanced   { base, args: [string], envRows: [row], options: { <other keys> },
//                replies: { usable, checked }, imports: { usable, checked } }
//                replies and imports keep the stored value while the row is not usable
//
// An option is written only when the values name it, and only by the block that
// owns it: the Agent block owns model and effort, Advanced owns every other key,
// so an option the config no longer defines survives an Agent save untouched.

function pfeDep(name, file) {
  return typeof globalThis[name] === "function" ? globalThis[name] : require(file)[name];
}

// The options the Agent block owns, by key: renderPfOptions renders every option
// in one list, and the desktop page puts these three in the Agent block with CSS
// alone (desktop.css data-opt); the read view leaves them out of Advanced
// (PFV_AGENT_OPTIONS) and the server offers them unset (config.unsetOfferKeys).
const PFE_AGENT_OPTIONS = ["model", "effort", "effortLevel"];

// The keys the Workspace block replaces as a unit.
const PFE_WORKSPACE_KEYS = ["default_cwd", "clone_of", "clone_share", "clone_no_auto_return"];

// What a box gives that the host tier cannot hold: the server refuses each of
// these on host (config/aws.go, awsconn.go, clone.go, the egress checks).
const PFE_BOX_ONLY = ["aws_connections", "aws", "github", "tokens", "context_paths", "egress_allow", "egress_open"];

// The first key of each path the server's save refusals name (config.FieldError),
// and the block that edits it. `options.<key>` and `name` are handled apart.
const PFE_FIELD_BLOCK = new Map([
  ["runtime", "runs"],
  ["default_cwd", "workspace"], ["clone_of", "workspace"], ["clone_share", "workspace"], ["clone_no_auto_return", "workspace"],
  ["bin", "agent"], ["account", "agent"],
  ["aws", "cloud"], ["aws_connections", "cloud"], ["github", "cloud"], ["tokens", "cloud"],
  ["context_paths", "reachable"], ["egress_allow", "reachable"],
  ["egress_open", "network"],
  ["base", "advanced"], ["args", "advanced"], ["env", "advanced"], ["secrets", "advanced"], ["replyable", "advanced"], ["imports_off", "advanced"],
]);

const PFE_STALE_TEXT = "This profile changed elsewhere";
const PFE_SAVE_FAILED = "Could not save this profile.";
const PFE_HOST_REFUSAL = "A private copy needs a box. Set Workspace to 'Work in this folder' first.";

// --- small helpers -------------------------------------------------------------------

const pfeHas = (obj, key) => Object.prototype.hasOwnProperty.call(obj, key);
const pfeTrim = (s) => (typeof s === "string" ? s.trim() : "");
// A profile as the API sends it is plain JSON, so a JSON round trip is a deep copy.
const pfeClone = (doc) => JSON.parse(JSON.stringify(doc && typeof doc === "object" ? doc : {}));
const pfeNames = (v) => (Array.isArray(v) ? v.filter((x) => typeof x === "string" && x !== "") : []);
// One entry per item, trimmed, blanks dropped, order kept.
const pfeLines = (v) => (Array.isArray(v) ? v.map(pfeTrim).filter(Boolean) : []);

// pfeEmpty: a value that means "this key is not set". false counts, because every
// boolean profile key (replyable, imports_off, egress_open) is stored only when true.
function pfeEmpty(v) {
  if (v === undefined || v === null || v === "" || v === false) return true;
  if (Array.isArray(v)) return v.length === 0;
  return typeof v === "object" && Object.keys(v).length === 0;
}

// pfeSet: write v under key, or remove the key when v is empty.
function pfeSet(out, key, v) {
  if (pfeEmpty(v)) delete out[key];
  else out[key] = v;
}

// pfeOptions: write the options `owns` says belong to the calling block. An
// option that is named is set, or removed when it is empty; a bool keeps a
// false (the server offers a profile's stored bool at spawn, an absent one not,
// config.OfferedBy), so false is a value here, not "unset". A key the values do
// not name is left as it is, and so is every key the block does not own.
function pfeOptions(out, incoming, owns) {
  if (!incoming || typeof incoming !== "object") return;
  const next = out.options && typeof out.options === "object" && !Array.isArray(out.options) ? out.options : {};
  for (const key of Object.keys(incoming)) {
    if (!owns(key)) continue;
    const v = incoming[key];
    if (v === undefined || v === null || (typeof v === "string" && v.trim() === "")) delete next[key];
    else next[key] = typeof v === "string" ? v.trim() : v;
  }
  if (Object.keys(next).length) out.options = next;
  else delete out.options;
}

// --- the merge -----------------------------------------------------------------------

// What each block writes (see the table at the top). out is the fresh copy being
// built, v the block's values, doc the profile as loaded (the stored values the
// preserving rules read).
const PFE_MERGE = {
  runs(out, v, doc) {
    pfeSet(out, "runtime", pfeDep("runtimeToSend", "./profileform.js")(v.tier, doc.runtime, v.boxAvailable !== false));
  },

  workspace(out, v) {
    // Never guess between a folder and a copy: a wrong guess drops clone_of or invents it.
    if (v.mode !== "copy" && v.mode !== "folder") return;
    for (const k of PFE_WORKSPACE_KEYS) delete out[k];
    Object.assign(out, pfeDep("wmFieldsToSend", "./workmode.js")(v.mode, v));
  },

  agent(out, v) {
    if (pfeHas(v, "bin")) pfeSet(out, "bin", pfeTrim(v.bin));
    // The default login is stored as no account at all (accounts.js ACCT_DEFAULT).
    if (pfeHas(v, "account")) {
      const a = pfeTrim(v.account);
      pfeSet(out, "account", a === "default" ? "" : a);
    }
    pfeOptions(out, v.options, (k) => PFE_AGENT_OPTIONS.includes(k));
  },

  cloud(out, v, doc) {
    if (pfeHas(v, "aws_connections")) pfeSet(out, "aws_connections", pfeDep("awscToSend", "./profileform.js")(pfeNames(v.aws_connections)));
    if (pfeHas(v, "tokens")) pfeSet(out, "tokens", pfeDep("stTokensToSend", "./profileform.js")(pfeNames(v.tokens)));
    if (pfeHas(v, "github")) {
      if (!v.github || typeof v.github !== "object") {
        delete out.github;
      } else {
        const binding = pfeClone(v.github);
        // The editor has no field for the binding's extra hosts; they go back as loaded.
        const carried = doc.github && doc.github.egress_allow;
        if (binding.egress_allow === undefined && Array.isArray(carried) && carried.length) binding.egress_allow = carried.slice();
        out.github = binding;
      }
    }
    // The inline binding is not edited here, only taken away (or converted by the server).
    if (pfeHas(v, "aws") && !v.aws) delete out.aws;
  },

  reachable(out, v) {
    if (pfeHas(v, "context_paths")) pfeSet(out, "context_paths", pfeLines(v.context_paths));
    // The Network rules: split on newlines and commas, trim, drop blanks and repeats.
    if (pfeHas(v, "egress_allow")) pfeSet(out, "egress_allow", pfeDep("egressAllowFromText", "./profileform.js")(pfeLines(v.egress_allow).join("\n")));
  },

  network(out, v) {
    if (pfeHas(v, "egress_open")) pfeSet(out, "egress_open", v.egress_open === true);
  },

  advanced(out, v, doc) {
    if (pfeHas(v, "base")) pfeSet(out, "base", pfeTrim(v.base));
    if (pfeHas(v, "args")) pfeSet(out, "args", pfeLines(v.args));
    if (pfeHas(v, "envRows")) {
      const cfg = pfeDep("rowsToConfig", "./envrows.js")(v.envRows);
      pfeSet(out, "env", cfg.env);
      pfeSet(out, "secrets", cfg.secrets);
    }
    if (v.replies && typeof v.replies === "object") {
      pfeSet(out, "replyable", pfeDep("replyableToSend", "./profileform.js")(!!v.replies.usable, !!v.replies.checked, doc.replyable));
    }
    if (v.imports && typeof v.imports === "object") {
      pfeSet(out, "imports_off", pfeDep("importsOffToSend", "./importsmodel.js")(!!v.imports.usable, !!v.imports.checked, doc.imports_off));
    }
    pfeOptions(out, v.options, (k) => !PFE_AGENT_OPTIONS.includes(k));
  },
};

// pfvMergeBlock: the loaded profile with one block's values written into it. A
// fresh object: neither doc nor values is touched, and nothing is shared with
// them. Only the keys the block owns can change. An unknown block id is a bug in
// the caller, and saving nothing under a success banner would hide it.
function pfvMergeBlock(doc, blockId, values) {
  if (typeof blockId !== "string" || !pfeHas(PFE_MERGE, blockId)) throw new Error("unknown profile block: " + blockId);
  const out = pfeClone(doc);
  // The merge reads doc only (the stored values its preserving rules need) and copies what it takes.
  PFE_MERGE[blockId](out, values && typeof values === "object" ? values : {}, doc && typeof doc === "object" ? doc : {});
  return out;
}

// --- which block a refusal belongs to --------------------------------------------------

// pfvBlockForField: the block that edits the profile field a refused save names
// (the server's field paths: "context_paths[1]", "github.owners[0].app",
// "options.model"). The first key decides, with any index or sub-key ignored;
// `options.<key>` goes to the Agent block for model and effort and to Advanced for
// every other option. "" for a path no block owns (the name, a key the server
// invented later): the caller shows that error at the top of the open block.
function pfvBlockForField(path) {
  if (typeof path !== "string") return "";
  const head = /^[^.[\]]+/.exec(path);
  if (!head) return "";
  if (head[0] === "options") {
    const key = /^options\.([^.[\]]+)/.exec(path);
    if (!key) return "";
    return PFE_AGENT_OPTIONS.includes(key[1]) ? "agent" : "advanced";
  }
  return PFE_FIELD_BLOCK.get(head[0]) || "";
}

// --- the unsaved-changes guard -----------------------------------------------------------

// pfvGuard: what to do with a request while a block may hold changes.
//   action "edit"   open another block: refused while the open one is dirty
//                   (the same block again is a no-op, so it goes)
//   action "leave"  go away from the profile: switch profile, nav, Esc, close the
//                   page, New profile, Duplicate. Dirty asks Discard / Keep editing.
// Nothing dirty always goes. A word it does not know is treated as leaving: a
// change is never dropped silently.
function pfvGuard(o) {
  const x = o || {};
  if (!x.dirty) return "go";
  if (x.action === "edit") return x.target === x.openBlock ? "go" : "refuse";
  return "confirm";
}

// --- the host switch -----------------------------------------------------------------------

// pfvHostRemovals: what the server refuses on the host tier and the switch
// therefore removes, as the confirm lists it: kind words in the read view's own
// terms, in this order, only the kinds the profile has. names are credential and
// folder names and hosts, never values.
function pfvHostRemovals(doc) {
  const d = doc && typeof doc === "object" ? doc : {};
  const out = [];
  const conns = pfeNames(d.aws_connections);
  if (conns.length) out.push({ kind: "AWS connections", names: conns });
  if (d.aws && typeof d.aws === "object") out.push({ kind: "Old-style AWS", names: pfeNames([d.aws.label]) });
  if (d.github && typeof d.github === "object") {
    const owners = Array.isArray(d.github.owners) ? d.github.owners : [];
    out.push({ kind: "GitHub", names: pfeNames(owners.map((o) => o && o.owner)) });
  }
  const tokens = pfeNames(d.tokens);
  if (tokens.length) out.push({ kind: "Tokens", names: tokens });
  const folders = pfeNames(d.context_paths);
  if (folders.length) out.push({ kind: "Readable folders", names: folders });
  const domains = pfeNames(d.egress_allow);
  if (domains.length) out.push({ kind: "Domains", names: domains });
  if (d.egress_open === true) out.push({ kind: "Network settings", names: [] });
  return out;
}

// pfvToHost: the profile as a host profile: everything pfvHostRemovals lists is
// gone and the tier is named explicitly. A fresh object.
function pfvToHost(doc) {
  const out = pfeClone(doc);
  for (const k of PFE_BOX_ONLY) delete out[k];
  out.runtime = "host";
  return out;
}

// pfvHostRefusal: why the profile cannot be switched to host, or "". A private
// copy runs only in a box (config/clone.go refuses clone_of on host), and the
// switch never guesses which folder an uncontained agent should write to.
function pfvHostRefusal(doc) {
  return doc && typeof doc.clone_of === "string" && doc.clone_of !== "" ? PFE_HOST_REFUSAL : "";
}

// --- the GitHub permission preset ------------------------------------------------------------

// pfvGhPresetLevels: the "Read only" preset as permission levels: the server's
// default set (DefaultGitHubPermissions, mirrored as GH_DEFAULT_PERMS) all read,
// every other permission off. ghPermLevels reads "no stored permissions" as
// exactly that set, so the preset has one definition. A fresh object each call.
function pfvGhPresetLevels() {
  return pfeDep("ghPermLevels", "./profileform.js")(null);
}

// pfvGhPreset: "read-only" when the levels are the preset's, else "custom". A
// permission the levels leave out is off.
function pfvGhPreset(levels) {
  const have = levels && typeof levels === "object" ? levels : {};
  const preset = pfvGhPresetLevels();
  for (const k of new Set([...Object.keys(preset), ...Object.keys(have)])) {
    if ((have[k] || "off") !== (preset[k] || "off")) return "custom";
  }
  return "read-only";
}

// --- a failed save --------------------------------------------------------------------------------

// pfvSaveError: what a refused save means for the page. status and body are the
// response's (body the parsed JSON, {error, code?, field?}).
//   kind "stale"  409 profile_changed (a deleted profile has the same code): the
//                 block says "This profile changed elsewhere" with Reload
//   kind "field"  a refusal that names a field: `field` is its path and `block`
//                 the block that owns it ("" when none does, e.g. the name)
//   kind "block"  anything else: the server's sentence, shown at the top of the
//                 open block (block and field are "")
// text is the server's sentence unchanged, so an older client's matching and the
// words the server already chose keep working.
function pfvSaveError(status, body) {
  const b = body && typeof body === "object" ? body : {};
  const sentence = pfeTrim(b.error) || PFE_SAVE_FAILED;
  if (status === 409 && b.code === "profile_changed") return { kind: "stale", block: "", field: "", text: PFE_STALE_TEXT };
  if (status === 400 && typeof b.field === "string" && b.field !== "") {
    return { kind: "field", block: pfvBlockForField(b.field), field: b.field, text: sentence };
  }
  return { kind: "block", block: "", field: "", text: sentence };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { pfvMergeBlock, pfvBlockForField, pfvGuard, pfvHostRemovals, pfvToHost, pfvHostRefusal, pfvGhPreset, pfvGhPresetLevels, pfvSaveError };
}
