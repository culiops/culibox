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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { editorState, saveLabel, duplicateName, containmentConfirm, segValue, setSegValue, awsFieldsState, segFromRuntime, runtimeToSend, cloneToSend, replyableToSend };
}
