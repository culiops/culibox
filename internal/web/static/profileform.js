"use strict";

// What the profile editor may do with the profile it is showing, and what its
// destructive confirms say. Pure — no DOM — so the rules are readable and
// testable on their own.
//
// "Built-in" is not a name or a flag this file invents: the server computes it,
// and a profile is read-only precisely because it exists only as a code-level
// seed and has never been written to the runtime store. Duplicate is what makes
// a read-only profile useful — a copy you own, seeded with what it does.

// nameLocked: a saved profile's name is fixed, because the API has no rename and
// Save on a changed name would create a second profile. Only a draft (New or
// Duplicate) names itself; to change a saved name, Duplicate it and delete the old.
function editorState(view) {
  if (!view) return { locked: false, nameLocked: false, save: "on", delete: "off", duplicate: "off" };
  if (view.editable) return { locked: false, nameLocked: true, save: "on", delete: "on", duplicate: "on" };
  return { locked: true, nameLocked: true, save: "off", delete: "off", duplicate: "on" };
}

// What Save does with the form. selected is the saved profile the editor is on,
// "" for a draft. A saved profile is always updated under its own name; a draft
// is always created, and refused when a stored profile already has its name
// (the server's Save is an upsert, so a POST would silently overwrite it).
function saveTarget(selected, formName, existing) {
  if (selected) return { method: "PUT", name: selected };
  if ((existing || []).includes(formName)) return { error: 'A profile named "' + formName + '" already exists. Pick another name.' };
  return { method: "POST" };
}

// The draft Duplicate opens: the editor's form (read as Save would, so unsaved
// edits come along) under a free "<source> copy" name. Nothing is written until Save.
function duplicateDraft(form, sourceName, existing) {
  return Object.assign({}, form, { name: duplicateName(sourceName, existing) });
}

// A trigger that is going to ask a question says so with a trailing ellipsis;
// the confirm button itself never carries one.
function saveLabel(tier) { return tier === "host" ? "Save…" : "Save"; }

function duplicateName(name, existing) {
  const taken = new Set(existing || []);
  const base = name + " copy";
  if (!taken.has(base)) return base;
  for (let n = 2; ; n++) {
    const c = base + " " + n;
    if (!taken.has(c)) return c;
  }
}

// The confirm answers the question the reader actually has — what do I give up
// — rather than asking them to retype the name. There is no type-to-confirm
// anywhere in this dialog: it reads as ceremony and buys nothing that stating
// the consequence does not.
function containmentConfirm(tier, name) {
  if (tier !== "host") return null;
  return {
    title: "Save as host",
    body: 'Sessions from "' + name + '" will run with no container, as your user, ' +
          "with everything you can reach: your whole home directory, your SSH keys, " +
          "your Docker socket. Save it that way?",
    confirmLabel: "Save as host",
    danger: true,
  };
}

// A segmented control is buttons, not a form control, so its value lives on the
// container. Reading it through one helper is what keeps the four places that
// used to read a <select> in step with each other.
function segValue(el) { return (el && el.dataset && el.dataset.value) || ""; }

function setSegValue(el, value) {
  if (!el || !el.dataset) return;
  el.dataset.value = value || "";
  for (const b of el.querySelectorAll ? el.querySelectorAll("button") : []) {
    b.classList.toggle("on", b.dataset.segvalue === (value || ""));
    b.setAttribute("aria-pressed", b.dataset.segvalue === (value || "") ? "true" : "false");
  }
}

// The AWS block's state. The credential broker only serves a boxed session, so
// the fields are off on the host tier as well as on a locked profile. One
// function decides both, because syncEditorActions re-enables every field of an
// unlocked profile and would otherwise undo the host rule. hint = show "AWS
// needs runtime: box" (the tier is the reason, not the lock).
function awsFieldsState(locked, runtime) {
  const host = runtime === "host";
  return { disabled: !!locked || host, hint: host };
}

// The runtime seg has two buttons, "box" and "host", and one of them is always
// lit. A stored "box"/"host" lights its own button. A profile naming no tier
// runs on the instance default, which GET /api/profiles reports as `resolved`;
// show that, or a host-default instance would light "box" for a profile that
// really runs on the host. A new profile has no resolved tier: box.
function segFromRuntime(stored, resolved) {
  if (stored === "box" || stored === "host") return stored;
  return resolved === "host" ? "host" : "box";
}

// What to send as runtime: the tier the seg shows, always. Sending it
// explicitly is what keeps a profile on the tier the editor lit when the
// instance default is host — a blank would hand the tier back to that default.
// The stored value is a defensive fallback only; the seg always has a value.
// One exception: with no box layer, a profile naming no tier is lit "box" only
// because a blank resolves to box there. Pinning it would make the profile
// unspawnable (the server refuses an explicit box), so it stays blank and goes
// back to box once containment exists. The seg still offers host.
function runtimeToSend(seg, stored, boxAvailable) {
  if (!boxAvailable && !stored && seg === "box") return "";
  if (seg === "box" || seg === "host") return seg;
  return stored === "box" || stored === "host" ? stored : "";
}

// The readable-host-paths field (context_paths): one absolute path per line.
// contextPathsFromText is what readForm sends: trimmed, blanks dropped, order
// kept. The server does the real validation, so nothing is judged here.
function contextPathsFromText(text) {
  return String(text || "").split("\n").map((s) => s.trim()).filter(Boolean);
}

// contextPathsToText fills the textarea; a profile with no paths gives "".
function contextPathsToText(list) {
  return Array.isArray(list) ? list.join("\n") : "";
}

// contextRowState: the readable-host-paths row. The paths only mean something
// in a box. On the host tier an empty row hides; a filled one stays visible with
// hint = "needs runtime: box", because the server refuses the save and paths
// hidden behind a tier flip would leave the user with an error and nothing to
// clear. hidden/hint as for awsFieldsState.
function contextRowState(runtime, text) {
  const host = runtime === "host";
  const filled = contextPathsFromText(text).length > 0;
  return { hidden: host && !filled, hint: host && filled };
}

// contextWarningsText: the save response's warnings as the lines shown under the
// field, minus the clone_share ones. A clean PUT is a 204, which api() returns as
// null, so a missing body or missing list gives "" rather than throwing.
function contextWarningsText(body) {
  if (!body || !Array.isArray(body.warnings)) return "";
  // clone_share lines belong to the private-copy block (workmode.js).
  return body.warnings.filter((line) => !/^clone_share /.test(line)).join("\n");
}

// replyableToSend: whether a save writes replyable:true. A visible row is the
// checkbox's word. A hidden row (replies off, or a non-claude bin) has no
// checkbox to read, so it keeps the stored value: otherwise a save made while
// the row is hidden silently drops a default the user set. Harmless for a
// non-claude bin, since the server ignores replyable there.
function replyableToSend(rowVisible, checked, stored) {
  return rowVisible ? !!checked : !!stored;
}

// The GH block (N1). The permission list and the defaults mirror
// config.githubPermissionKeys / DefaultGitHubPermissions: every one is read-only,
// and the server refuses anything else, so the form only ever sends "read".
const GH_PERMS = ["actions", "checks", "contents", "pull_requests", "issues", "metadata", "statuses"];
const GH_DEFAULT_PERMS = ["actions", "checks", "contents", "pull_requests", "issues", "metadata"];

// ghPermsChecked: which permission boxes a stored binding lights. No stored
// permissions means the defaults, exactly as the server reads it.
function ghPermsChecked(binding) {
  const stored = binding && binding.permissions && Object.keys(binding.permissions);
  const on = stored && stored.length ? stored : GH_DEFAULT_PERMS;
  return Object.fromEntries(GH_PERMS.map((k) => [k, on.includes(k)]));
}

// ghBindingFromForm: the editor's GH block as the object the server stores, or
// undefined when no owner row has anything in it (an empty block means "no
// binding", not an invalid one). fields = { owners: [{owner, app | token, repos}],
// permissions: {key: checked} } with repos the textarea's raw text. A row that is
// only half filled is kept, so the server's validation names what is missing. A
// permission set equal to the defaults is left out, so a default binding keeps
// the same stored shape (and the same box key) however the boxes were clicked.
function ghBindingFromForm(fields) {
  const rows = ((fields && fields.owners) || []).map((r) => {
    const owner = String(r.owner || "").trim();
    const token = String(r.token || "").trim();
    const repos = String(r.repos || "").split(/[\s,]+/).filter(Boolean);
    // An owner uses an App or a personal access token entry, never both: a
    // token row carries no app key at all.
    return token ? { owner, token, repos } : { owner, app: String(r.app || "").trim(), repos };
  }).filter((r) => r.owner || r.repos.length);
  if (!rows.length) return undefined;
  const out = { owners: rows };
  const perms = (fields && fields.permissions) || {};
  const on = GH_PERMS.filter((k) => perms[k]);
  const isDefault = on.length === GH_DEFAULT_PERMS.length && GH_DEFAULT_PERMS.every((k) => on.includes(k));
  if (!isDefault) out.permissions = Object.fromEntries(on.map((k) => [k, "read"]));
  // The editor has no field for the binding's extra egress hosts; carry them.
  if (fields && Array.isArray(fields.egress_allow) && fields.egress_allow.length) out.egress_allow = fields.egress_allow.slice();
  return out;
}

// ghFormError: the sentence that refuses a save, or "". An empty permission set
// is not "the defaults" to the server (it refuses it too), and sending nothing
// would read as the defaults, so a binding with no box ticked is stopped here.
function ghFormError(fields) {
  const b = ghBindingFromForm(fields);
  if (b && b.permissions && Object.keys(b.permissions).length === 0) {
    return "GitHub: tick at least one read permission, or the agent gets none.";
  }
  return "";
}

// The Tokens block (N3). entries are /api/meta's static_tokens (names and
// shape only; the value and where it is read from never reach the browser).

// stGroups: the picker's groups, by provider. Providers sorted, an entry with
// none under "other" last, names sorted inside each.
function stGroups(entries) {
  const by = new Map();
  for (const e of entries || []) {
    const key = (e && e.provider) || "other";
    if (!by.has(key)) by.set(key, []);
    by.get(key).push(e);
  }
  const keys = [...by.keys()].filter((k) => k !== "other").sort();
  if (by.has("other")) keys.push("other");
  return keys.map((provider) => ({
    provider,
    entries: by.get(provider).slice().sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)),
  }));
}

// stEntryLine: what an entry fills and where it may go, on one line:
// "env A, B · config K1 · hosts h1, h2:8443 (private)". The port is shown only
// when it is not 443. A part with nothing in it is left out.
function stEntryLine(e) {
  const x = e || {};
  const parts = [];
  if ((x.env || []).length) parts.push("env " + x.env.join(", "));
  if ((x.config_env_keys || []).length) parts.push("config " + x.config_env_keys.join(", "));
  if ((x.hosts || []).length) {
    parts.push("hosts " + x.hosts.map((h) => h.host + (h.port && h.port !== 443 ? ":" + h.port : "") + (h.private ? " (private)" : "")).join(", "));
  }
  return parts.join(" · ");
}

// stTokensToSend: the ticked entry names as the profile's tokens field, sorted
// and without repeats, or undefined when none is ticked (no tokens = no field).
function stTokensToSend(checked) {
  const names = [...new Set((checked || []).map((n) => String(n)).filter(Boolean))].sort();
  return names.length ? names : undefined;
}

// The AWS connections block (#155). meta is /api/meta's aws_connections (names,
// account and shape only; the source profile and the roles never reach the
// browser).

// awscPickerRows: one row per defined connection, production first and then by
// name, followed by one `missing` row for each ticked name the instance no
// longer defines (so a save never drops a stored name silently; the server
// refuses it by name instead).
function awscPickerRows(meta, ticked) {
  const byName = (a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
  const defined = (meta || []).map((e) => ({
    name: e.name, label: e.label || "", prod: !!e.prod, write: !!e.write_capable, account: e.account_id || "", missing: false,
  })).sort((a, b) => (a.prod === b.prod ? byName(a, b) : a.prod ? -1 : 1));
  const have = new Set(defined.map((r) => r.name));
  const gone = [...new Set((ticked || []).filter((n) => !have.has(n)))].sort().map((name) => (
    { name, label: "", prod: false, write: false, account: "", missing: true }));
  return defined.concat(gone);
}

// awscExclusiveState: a profile binds AWS inline or by connections, never both.
// Ticked connections close the inline block; a filled inline role closes the
// picker; the note names whichever one is closed. If both are somehow set (a
// stored profile the server would refuse), the connections win the display and
// the picker stays open, so unticking is the way out.
function awscExclusiveState(tickedCount, inlineRole) {
  if ((tickedCount || 0) > 0) {
    return { pickerDisabled: false, inlineDisabled: true, note: "Using AWS connections — the inline binding is off" };
  }
  if (String(inlineRole || "").trim()) {
    return { pickerDisabled: true, inlineDisabled: false, note: "Convert or remove the inline binding to use connections" };
  }
  return { pickerDisabled: false, inlineDisabled: false, note: "" };
}

// awscToSend: the ticked connection names as the profile's aws_connections
// field, sorted and without repeats, or undefined when none is ticked (the key
// is then left out, so a legacy profile's JSON is unchanged).
function awscToSend(checked) {
  const names = [...new Set((checked || []).map((n) => String(n)).filter(Boolean))].sort();
  return names.length ? names : undefined;
}

// --- The inline AWS binding (legacy) -------------------------------------
// The form no longer edits it: a profile that still carries one shows a summary
// with "Convert to a connection" and "Remove", and runs unchanged until then.

// inlineAWSSummary: "<source profile> → <role name> (<region>)", for the
// read-only line. A part the binding lacks is left out rather than printed.
function inlineAWSSummary(aws) {
  const a = aws || {};
  const arn = String(a.role_arn || "");
  const role = arn.slice(arn.lastIndexOf("/") + 1);
  const src = String(a.source_profile || "");
  const region = String(a.region || "");
  const head = src && role ? src + " → " + role : src || role;
  return region ? (head ? head + " (" + region + ")" : region) : head;
}

// awsInlineToSend: what a save sends as the profile's `aws`. The binding as it
// was loaded (a copy), unchanged; null once Remove was pressed, or when there
// never was one (the caller then leaves the key out).
function awsInlineToSend(loaded, removed) {
  if (removed || !loaded || typeof loaded !== "object") return null;
  return JSON.parse(JSON.stringify(loaded));
}

// convertConnName: the name the server will give the new connection, which is
// the profile's name as a slug (internal/web/profiles_api.go convertSlug). The
// server falls back to "<slug>-aws" when that is taken; the confirm names the
// first choice and the result line names the real one.
function convertConnName(profile) {
  let out = String(profile || "").toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/^-+/, "");
  if (out.length > 63) out = out.slice(0, 63);
  return out || "aws";
}

// convertAWSConfirm: the sentence the confirm shows before converting.
function convertAWSConfirm(profile) {
  const n = convertConnName(profile);
  return "This creates a connection named " + n + " and points the profile at it. The next session gets a new box, and AWS_PROFILE becomes " + n + ".";
}

// convertAWSErrText: the wording for the two refusals that are specific to a
// convert. "" means the generic AWS-screen wording applies (awsErrText).
function convertAWSErrText(e) {
  if (!e) return "";
  if (e.code === "profile_changed") return "This profile changed while converting; reload and try again";
  if (e.code === "no_broker") return "AWS connections need the boxed runtime";
  return "";
}

// ticksAfterRefresh: the checkbox ticks that survive a live refresh of a picker
// (the lists changed in Credentials while the form is open). A tick goes only
// when its entry WAS defined in oldNames and is not in newNames. A tick on a
// name that was never defined (a stored profile pointing at a removed entry)
// stays, so its "(not defined)" row survives and the server names it on save.
function ticksAfterRefresh(ticked, oldNames, newNames) {
  const was = new Set(oldNames || []);
  const now = new Set(newNames || []);
  return (ticked || []).filter((n) => !(was.has(n) && !now.has(n)));
}

// pfTouchProfile: what the editor holds now (readForm's request body) in the
// ProfileView shape reachRows reads (spawnpreview.js), so the Settings page's
// "Can touch" line and the New session box follow one set of rules.
//   o.view       the saved ProfileView of this profile, or null for a draft
//   o.claude     the form's binary is claude (spawnform.js isClaudeBinName)
//   o.egressAll  every boxed spawn goes through the egress proxy
// Network mirrors session.EgressMode: bound = allowlist, unbound = open only
// when egress is mandatory. The inline AWS binding is not edited here, so its
// name and flags come from the saved view while the form still carries it.
function pfTouchProfile(form, o) {
  const f = form || {};
  const x = o || {};
  const list = (l) => (Array.isArray(l) ? l.filter((n) => typeof n === "string" && n !== "") : []);
  const out = { bin: String(f.bin || ""), account: String(f.account || ""), claude: x.claude === true };
  const conns = list(f.aws_connections);
  if (conns.length) out.aws_connections = conns;
  if (f.aws && typeof f.aws === "object") {
    const saved = x.view && x.view.aws_inline;
    out.aws_inline = saved ? saved : { name: String(f.aws.label || "") || "aws", prod: f.aws.prod === true };
  }
  const owners = list(((f.github && f.github.owners) || []).map((w) => w && w.owner));
  if (owners.length) out.github_owners = owners;
  const tokens = list(f.tokens);
  if (tokens.length) out.tokens = tokens;
  if (typeof f.clone_of === "string" && f.clone_of !== "") out.clone_of = f.clone_of;
  const bound = !!(out.aws_connections || out.aws_inline || f.github || out.tokens);
  out.network = bound ? "allowlist" : x.egressAll === true ? "open" : "";
  return out;
}

// autoReturnToSend: the auto-return switch's part of a save. The field is the
// off switch (omitted means on), so a ticked box sends nothing. Only a private
// copy may carry it (the server refuses it without clone_of): wmFieldsToSend
// calls this for copy mode alone.
function autoReturnToSend(checked) {
  return checked ? {} : { clone_no_auto_return: true };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { autoReturnToSend, saveTarget, duplicateDraft, ticksAfterRefresh, awscPickerRows, awscExclusiveState, awscToSend, stGroups, stEntryLine, stTokensToSend, editorState, saveLabel, duplicateName, containmentConfirm, segValue, setSegValue, awsFieldsState, segFromRuntime, runtimeToSend, contextPathsFromText, contextPathsToText, contextWarningsText, contextRowState, replyableToSend, GH_PERMS, GH_DEFAULT_PERMS, ghPermsChecked, ghBindingFromForm, ghFormError, inlineAWSSummary, awsInlineToSend, convertConnName, convertAWSConfirm, convertAWSErrText, pfTouchProfile };
}
