"use strict";

// The profile editor's work-mode rules (spec: Work in this folder / Work on a
// private copy). Pure: no DOM, no network, every decision a function of its
// arguments, so the tests pin them and the DOM wiring stays thin. All globals
// are prefixed wm. Classic script: contextPathsFromText (profileform.js) and
// shortPath (fsops.js) are browser globals; under node they are required.

function wmDep(name, file) {
  return typeof globalThis[name] === "function" ? globalThis[name] : require(file)[name];
}

// wmModeOf: a profile is a private copy iff it names a folder to copy.
function wmModeOf(profile) {
  return profile && typeof profile.clone_of === "string" && profile.clone_of !== "" ? "copy" : "folder";
}

// wmCardState: whether the "private copy" card can be chosen, why not, and the
// hint under the cards. The hint speaks only about a profile that IS a copy:
// one that is stored as a copy on a host seg, or on an instance without clone
// mode, still opens as a copy and must say why its sessions will fail. It never
// blocks Save. unavailable is the server's reason string (/api/meta), "" or
// absent when clone mode works.
function wmCardState(o) {
  const x = o || {};
  const copy = x.mode === "copy";
  if (x.runtime === "host") {
    return {
      copyDisabled: true,
      copyReason: "Needs a box.",
      hint: copy ? "Private copy needs a box: switch containment to box, or choose Work in this folder." : "",
    };
  }
  if (typeof x.unavailable === "string" && x.unavailable !== "") {
    return {
      copyDisabled: true,
      copyReason: "Not available on this instance: " + x.unavailable + ".",
      hint: copy ? "Private copies are not available on this instance (" + x.unavailable + "). Sessions of this profile will not start." : "",
    };
  }
  return { copyDisabled: x.locked === true, copyReason: "", hint: "" };
}

// wmFieldsToSend: the work-mode fields a save writes. Folder mode sends only
// default_cwd. Copy mode sends clone_of (the folder), default_cwd (where the
// copy lives) and clone_share when there is at least one path, so an existing
// copy profile saved without edits keeps all three. An empty value is left out,
// never sent blank; wmFormError stops a copy with a missing field before this
// is used, so an empty clone_of cannot silently make a plain profile.
function wmFieldsToSend(mode, v) {
  const x = v || {};
  const folder = String(x.folder || "").trim(), copyDir = String(x.copyDir || "").trim();
  const out = {};
  if (mode === "copy") {
    if (folder) out.clone_of = folder;
    if (copyDir) out.default_cwd = copyDir;
    const share = wmDep("contextPathsFromText", "./profileform.js")(x.shareText);
    if (share.length) out.clone_share = share;
    // Unset means on. Only here: the server refuses clone_no_auto_return without
    // clone_of, so folder mode must never carry it, whatever the loaded profile had.
    Object.assign(out, wmDep("autoReturnToSend", "./profileform.js")(x.autoReturn !== false));
    // The merge switch (#370): off unless the values say on, and never beside an auto-return opt-out.
    Object.assign(out, wmDep("mergeHomeToSend", "./profileform.js")(x.autoReturn !== false, x.mergeHome === true, x.mergeAck === true));
    return out;
  }
  if (folder) out.default_cwd = folder;
  return out;
}

// wmMergeSwitch: the merge switch's face (#370) from the auto-return box and the switch as held: it can only be on with
// automatic return (the server refuses the pair), so without it the switch is off and disabled, with the reason as its title.
// Both forms (the profile page and the phone's legacy form) draw from this; each dims the box and its label while disabled.
function wmMergeSwitch(autoReturn, mergeHome) {
  const on = autoReturn === true;
  return { checked: on && mergeHome === true, disabled: !on, title: on ? "" : "Merging needs automatic return" };
}

// wmMergeAcked: whether a save carries ack_merge_home: the switch is on and the repo whose warning the user confirmed
// in the form is the folder being saved (the folder can change after the confirmation).
function wmMergeAcked(mergeHome, ackRepo, folder) {
  return mergeHome === true && typeof ackRepo === "string" && ackRepo !== "" && ackRepo === String(folder || "").trim();
}

// wmMergeTurnOn: the switch was just checked: ask (ask(repo) -> promise of a yes, cdAskMergeHome) about the folder to copy
// and answer what the form now holds: { mergeHome, ackRepo }. With no folder there is nothing to name, so no dialog and the
// switch stays off (the folder field says what is missing).
async function wmMergeTurnOn(folder, ask) {
  const repo = String(folder || "").trim();
  const yes = repo !== "" && (await ask(repo)) === true;
  return { mergeHome: yes, ackRepo: yes ? repo : "" };
}

// wmFormError: what stops a save before the PUT. Copy mode needs both the
// folder to copy and the copy's directory.
function wmFormError(mode, v) {
  if (mode !== "copy") return "";
  const x = v || {};
  if (!String(x.folder || "").trim()) return "Private copy: pick the folder to copy.";
  if (!String(x.copyDir || "").trim()) return "Private copy: choose where the copy lives.";
  return "";
}

// wmErrText: a server refusal about the clone fields, worded with the labels on
// screen. "" when it is not about the private-copy block (or the profile is not
// a copy), so the caller shows it where it showed errors before. A
// "default working dir cannot be contained" message matches but has no field
// name to rewrite: in copy mode it is about the copy dir, so it passes through.
function wmErrText(mode, msg) {
  const m = String(msg == null ? "" : msg);
  if (mode !== "copy") return "";
  if (!/\bclone_of\b|\bclone_share\b|clone mode|\bdefault_cwd\b|default working dir/.test(m)) return "";
  return m
    .replace(/\bclone_of\b/g, "Folder to copy")
    .replace(/\bdefault_cwd\b/g, "Copy lives in")
    .replace(/\bclone_share\b/g, "Read-only path");
}

// wmShareWarningsText: the save response's clone_share warnings, one per line,
// for the share field. The rest of the warnings stay with contextWarningsText.
function wmShareWarningsText(body) {
  if (!body || !Array.isArray(body.warnings)) return "";
  return body.warnings.filter((l) => typeof l === "string" && /^clone_share /.test(l)).join("\n");
}

// wmCopyDirSuggestion: where a new copy could live, ~/box-clones/<folder name>,
// with -2, -3 ... appended until the path is not in taken (the other profiles'
// copy dirs: two profiles writing one clone would share a .git). "" with no
// folder or no home.
function wmCopyDirSuggestion(folder, home, taken) {
  const base = String(folder || "").split("/").filter(Boolean).pop() || "";
  const h = String(home || "").replace(/\/+$/, "");
  if (!base || !h) return "";
  const used = new Set(Array.isArray(taken) ? taken : []);
  const first = h + "/box-clones/" + base;
  if (!used.has(first)) return first;
  for (let n = 2; ; n++) {
    if (!used.has(first + "-" + n)) return first + "-" + n;
  }
}

// wmSwitchNote: shown when a copy profile is switched to folder mode. The copy
// is not touched; the note says so. Every other switch has nothing to say.
function wmSwitchNote(savedMode, mode, savedCopyDir, home) {
  if (savedMode !== "copy" || mode !== "folder" || !savedCopyDir) return "";
  return "The private copy at " + wmDep("shortPath", "./fsops.js")(savedCopyDir, home) + " stays on disk with its work; this switch never deletes it.";
}

// wmNeedsGitHubHint: whether to show "To push and open PRs, add a GitHub
// token". A hint, never a block. Any GitHub binding owner row, or any ticked
// static token whose provider is github, counts as a route.
function wmNeedsGitHubHint(mode, github, tickedTokens, entries) {
  if (mode !== "copy") return false;
  if (github && Array.isArray(github.owners) && github.owners.length > 0) return false;
  const ticked = new Set(Array.isArray(tickedTokens) ? tickedTokens : []);
  const hasToken = (Array.isArray(entries) ? entries : []).some((e) => e && e.provider === "github" && ticked.has(e.name));
  return !hasToken;
}

// wmCopyLine: the session / spawn line for a private copy. "" for a profile
// that is not one.
function wmCopyLine(cloneOf, home) {
  if (typeof cloneOf !== "string" || cloneOf === "") return "";
  return "Private copy of " + wmDep("shortPath", "./fsops.js")(cloneOf, home);
}

// wmFolderCopy: the folder card's body and the "Agent can commit" cell of the
// difference table, plus homeCell for the "How work comes home" row. In a box
// the git history is read-only (the box enforces it); on the host tier the
// agent runs as the user, so nothing is and it can commit in the folder too.
function wmFolderCopy(isHost) {
  if (isHost) {
    return {
      body: "The agent edits your files directly, as you. On the host tier nothing is read-only.",
      commitCell: "Yes: on the host tier it runs as you",
      homeCell: "It is already there, and the agent can commit it too",
    };
  }
  return {
    body: "The agent edits your files directly. Your git history stays read-only: you review and commit yourself.",
    commitCell: "No: your git history is read-only",
    homeCell: "It is already there; you commit",
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { wmMergeSwitch, wmMergeAcked, wmMergeTurnOn, wmFolderCopy, wmModeOf, wmCardState, wmFieldsToSend, wmFormError, wmErrText, wmShareWarningsText, wmCopyDirSuggestion, wmSwitchNote, wmNeedsGitHubHint, wmCopyLine };
}
