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

// The runtime seg has two buttons: "" (box, the instance default) and "host".
// A stored profile can say runtime "box" explicitly — an AWS binding requires
// it — and the seg would show neither button lit for that value.
function segFromRuntime(runtime) { return runtime === "host" ? "host" : ""; }

// What to send as runtime. Blank stays blank (the instance default decides),
// except where "box" must be explicit: a profile stored as box, which a blank
// would silently turn into the instance default (host on PROD), and any
// profile with an AWS binding, which the server refuses without it.
function runtimeToSend(seg, storedExplicitBox, hasAWS) {
  if (seg) return seg;
  return storedExplicitBox || hasAWS ? "box" : "";
}

// boxToSend: the stored profile's box override, sent back on save. The editor
// has no box field, and a save that omits it erases the override (and so
// changes the box): follow-up A4. Only a non-empty image is an override.
function boxToSend(loaded) {
  const img = loaded && typeof loaded.image === "string" ? loaded.image.trim() : "";
  return img ? { image: img } : undefined;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { editorState, saveLabel, duplicateName, containmentConfirm, segValue, setSegValue, awsFieldsState, segFromRuntime, runtimeToSend, boxToSend };
}
