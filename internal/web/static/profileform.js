"use strict";

// What the profile editor may do with the profile it is showing, and what its
// destructive confirms say. Pure — no DOM — so the rules are readable and
// testable on their own.
//
// "Built-in" is not a name or a flag this file invents: the server computes it,
// and a profile is read-only precisely because it exists only as a code-level
// seed and has never been written to the runtime store. Duplicate is what makes
// a read-only profile useful — a copy you own, seeded with what it does.

function editorState(view) {
  if (!view) return { locked: false, save: "on", delete: "off", duplicate: "off" };
  if (view.editable) return { locked: false, save: "on", delete: "on", duplicate: "on" };
  return { locked: true, save: "off", delete: "off", duplicate: "on" };
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

// cloneToSend: the stored profile's clone fields, sent back on save. The editor
// has no clone field, and a save that omits clone_of turns the profile into a
// plain boxed one. Only a non-empty clone_of is a clone; clone_share is a copy
// and omitted when the profile has none.
function cloneToSend(loaded) {
  if (!loaded || typeof loaded.clone_of !== "string" || !loaded.clone_of) return undefined;
  const out = { clone_of: loaded.clone_of };
  if (Array.isArray(loaded.clone_share)) out.clone_share = loaded.clone_share.slice();
  return out;
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
    return { pickerDisabled: true, inlineDisabled: false, note: "Clear the inline binding to use connections" };
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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { awscPickerRows, awscExclusiveState, awscToSend, stGroups, stEntryLine, stTokensToSend, editorState, saveLabel, duplicateName, containmentConfirm, segValue, setSegValue, awsFieldsState, segFromRuntime, runtimeToSend, cloneToSend, replyableToSend, GH_PERMS, GH_DEFAULT_PERMS, ghPermsChecked, ghBindingFromForm, ghFormError };
}
