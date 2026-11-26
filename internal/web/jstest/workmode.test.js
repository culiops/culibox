"use strict";
// workmode.js: the profile editor's pure work-mode rules.
const test = require("node:test");
const assert = require("node:assert");
const { wmModeOf, wmCardState, wmFieldsToSend, wmFormError, wmErrText, wmShareWarningsText, wmCopyDirSuggestion, wmSwitchNote, wmNeedsGitHubHint, wmCopyLine, wmFolderCopy, wmMergeSwitch, wmMergeAcked, wmMergeTurnOn } = require("../static/workmode.js");

test("mode: copy only for a non-empty string clone_of", () => {
  assert.strictEqual(wmModeOf({ clone_of: "/r" }), "copy");
  assert.strictEqual(wmModeOf(null), "folder");
  assert.strictEqual(wmModeOf({}), "folder");
  assert.strictEqual(wmModeOf({ clone_of: "" }), "folder");
  assert.strictEqual(wmModeOf({ clone_of: 42 }), "folder");
});

test("card state: host tier disables the copy card, and hints only for a copy profile", () => {
  assert.deepStrictEqual(wmCardState({ runtime: "host", mode: "folder" }), { copyDisabled: true, copyReason: "Needs a box.", hint: "" });
  assert.strictEqual(wmCardState({ runtime: "host", mode: "copy" }).hint,
    "Private copy needs a box: switch containment to box, or choose Work in this folder.");
});

test("card state: an instance without clone mode says why, for a copy profile", () => {
  assert.deepStrictEqual(wmCardState({ runtime: "box", mode: "copy", unavailable: "no containment layer" }), {
    copyDisabled: true,
    copyReason: "Not available on this instance: no containment layer.",
    hint: "Private copies are not available on this instance (no containment layer). Sessions of this profile will not start.",
  });
  // folder mode: the hint speaks only about an existing copy profile
  assert.deepStrictEqual(wmCardState({ runtime: "box", mode: "folder", unavailable: "no containment layer" }), {
    copyDisabled: true, copyReason: "Not available on this instance: no containment layer.", hint: "",
  });
});

test("card state: locked disables without a reason; a free box copy has no hint", () => {
  const l = wmCardState({ runtime: "box", mode: "folder", locked: true });
  assert.strictEqual(l.copyDisabled, true);
  assert.strictEqual(l.copyReason, "");
  assert.deepStrictEqual(wmCardState({ runtime: "box", mode: "copy" }), { copyDisabled: false, copyReason: "", hint: "" });
});

test("fields to send: folder mode sends only default_cwd", () => {
  assert.deepStrictEqual(wmFieldsToSend("folder", { folder: "/h/r", copyDir: "/h/box-clones/r", shareText: "a" }), { default_cwd: "/h/r" });
  assert.deepStrictEqual(wmFieldsToSend("folder", { folder: "", copyDir: "/h/box-clones/r", shareText: "a" }), {});
});

test("fields to send: copy mode round-trips clone_of, default_cwd and clone_share", () => {
  assert.deepStrictEqual(
    wmFieldsToSend("copy", { folder: "/h/r", copyDir: "/h/box-clones/r", shareText: "CLAUDE.md\n docs/notes \n\n" }),
    { clone_of: "/h/r", default_cwd: "/h/box-clones/r", clone_share: ["CLAUDE.md", "docs/notes"] });
  const none = wmFieldsToSend("copy", { folder: "/h/r", copyDir: "/h/box-clones/r", shareText: "" });
  assert.deepStrictEqual(none, { clone_of: "/h/r", default_cwd: "/h/box-clones/r" });
  assert.ok(!("clone_share" in none));
});

test("form error: copy mode needs both the folder and the copy dir", () => {
  assert.strictEqual(wmFormError("copy", { folder: "", copyDir: "/x" }), "Private copy: pick the folder to copy.");
  assert.strictEqual(wmFormError("copy", { folder: "/r", copyDir: " " }), "Private copy: choose where the copy lives.");
  assert.strictEqual(wmFormError("copy", { folder: "/r", copyDir: "/x" }), "");
  assert.strictEqual(wmFormError("folder", { folder: "", copyDir: "" }), "");
});

test("error text: clone field names are rewritten to the labels on screen", () => {
  assert.strictEqual(
    wmErrText("copy", 'clone_of "/h/r" and default_cwd "/h/r/x" overlap; the clone must live outside the repo it copies'),
    'Folder to copy "/h/r" and Copy lives in "/h/r/x" overlap; the clone must live outside the repo it copies');
  assert.ok(wmErrText("copy", 'clone_share "a:rw": shares are read-only (D12); remove ":rw"').startsWith('Read-only path "a:rw"'));
  assert.strictEqual(wmErrText("copy", "tokens: x"), "");
  assert.strictEqual(wmErrText("folder", 'clone_of "/r" overlaps'), "");
  const dwd = "default working dir cannot be contained: /x is outside";
  assert.strictEqual(wmErrText("copy", dwd), dwd);
});

test("share warnings: only the clone_share lines, safely absent", () => {
  assert.strictEqual(wmShareWarningsText({ warnings: ['clone_share ".env" looks like a secret or config file; the box will be able to read it', "context_paths x"] }),
    'clone_share ".env" looks like a secret or config file; the box will be able to read it');
  assert.strictEqual(wmShareWarningsText(null), "");
  assert.strictEqual(wmShareWarningsText({}), "");
});

test("copy dir suggestion: ~/box-clones/<name>, skipping taken dirs", () => {
  assert.strictEqual(wmCopyDirSuggestion("/h/culiops-skills", "/h", []), "/h/box-clones/culiops-skills");
  assert.strictEqual(wmCopyDirSuggestion("/h/culiops-skills", "/h", ["/h/box-clones/culiops-skills"]), "/h/box-clones/culiops-skills-2");
  assert.strictEqual(wmCopyDirSuggestion("/h/culiops-skills", "/h", ["/h/box-clones/culiops-skills", "/h/box-clones/culiops-skills-2"]), "/h/box-clones/culiops-skills-3");
  assert.strictEqual(wmCopyDirSuggestion("", "/h", []), "");
  assert.strictEqual(wmCopyDirSuggestion("/h/r", "", []), "");
});

test("switch note: only copy to folder, and it never promises deletion", () => {
  assert.strictEqual(wmSwitchNote("copy", "folder", "/h/box-clones/r", "/h"),
    "The private copy at ~/box-clones/r stays on disk with its work; this switch never deletes it.");
  assert.strictEqual(wmSwitchNote("folder", "copy", "/h/box-clones/r", "/h"), "");
  assert.strictEqual(wmSwitchNote("copy", "copy", "/h/box-clones/r", "/h"), "");
  assert.strictEqual(wmSwitchNote("folder", "folder", "/h/box-clones/r", "/h"), "");
});

test("github hint: copy mode with no github route", () => {
  assert.strictEqual(wmNeedsGitHubHint("copy", undefined, [], []), true);
  assert.strictEqual(wmNeedsGitHubHint("copy", { owners: [{ owner: "o" }] }, [], []), false);
  assert.strictEqual(wmNeedsGitHubHint("copy", undefined, ["gh"], [{ name: "gh", provider: "github" }]), false);
  assert.strictEqual(wmNeedsGitHubHint("copy", undefined, ["s"], [{ name: "s", provider: "sentry" }]), true);
  assert.strictEqual(wmNeedsGitHubHint("folder", undefined, [], []), false);
  assert.strictEqual(wmNeedsGitHubHint("folder", { owners: [{ owner: "o" }] }, [], []), false);
});

test("copy line: Private copy of <~path>", () => {
  assert.strictEqual(wmCopyLine("/h/culiops-skills", "/h"), "Private copy of ~/culiops-skills");
  assert.strictEqual(wmCopyLine("", "/h"), "");
});

test("folder copy: the box tier keeps the verbatim copy, the host tier never claims read-only", () => {
  assert.deepStrictEqual(wmFolderCopy(false), {
    body: "The agent edits your files directly. Your git history stays read-only: you review and commit yourself.",
    commitCell: "No: your git history is read-only",
    homeCell: "It is already there; you commit",
  });
  assert.deepStrictEqual(wmFolderCopy(true), {
    body: "The agent edits your files directly, as you. On the host tier nothing is read-only.",
    commitCell: "Yes: on the host tier it runs as you",
    homeCell: "It is already there, and the agent can commit it too",
  });
});

test("fields to send: the auto-return switch rides only with a private copy", () => {
  const v = { folder: "/h/r", copyDir: "/h/box-clones/r", shareText: "" };
  assert.deepStrictEqual(wmFieldsToSend("copy", { ...v, autoReturn: false }),
    { clone_of: "/h/r", default_cwd: "/h/box-clones/r", clone_no_auto_return: true });
  assert.ok(!("clone_no_auto_return" in wmFieldsToSend("copy", { ...v, autoReturn: true })), "ticked");
  assert.ok(!("clone_no_auto_return" in wmFieldsToSend("copy", v)), "unset means on");
  // The server answers 400 for the field without clone_of: folder mode never sends it,
  // not even when the loaded profile had it or the caller passes it through.
  assert.deepStrictEqual(wmFieldsToSend("folder", { folder: "/h/r", autoReturn: false, clone_no_auto_return: true }), { default_cwd: "/h/r" });
  assert.deepStrictEqual(wmFieldsToSend("folder", { folder: "", autoReturn: false }), {});
});

test("wmFieldsToSend: the merge switch rides a private copy's save only while auto-return is on", () => {
  const base = { folder: "/h/r", copyDir: "/h/box-clones/r" };
  assert.deepStrictEqual(wmFieldsToSend("copy", { ...base, mergeHome: true, mergeAck: true }), { clone_of: "/h/r", default_cwd: "/h/box-clones/r", clone_merge_home: true, ack_merge_home: true });
  assert.deepStrictEqual(wmFieldsToSend("copy", { ...base, mergeHome: false, mergeAck: true }), { clone_of: "/h/r", default_cwd: "/h/box-clones/r" });
  assert.deepStrictEqual(wmFieldsToSend("copy", { ...base, autoReturn: false, mergeHome: true, mergeAck: true }), { clone_of: "/h/r", default_cwd: "/h/box-clones/r", clone_no_auto_return: true });
  assert.deepStrictEqual(wmFieldsToSend("folder", { folder: "/h/r", mergeHome: true, mergeAck: true }), { default_cwd: "/h/r" });
});

test("merge switch state: only with automatic return; dimmed (box and label) while disabled, with the reason", () => {
  assert.deepStrictEqual(wmMergeSwitch(true, true), { checked: true, disabled: false, title: "" });
  assert.deepStrictEqual(wmMergeSwitch(true, false), { checked: false, disabled: false, title: "" });
  assert.deepStrictEqual(wmMergeSwitch(false, true), { checked: false, disabled: true, title: "Merging needs automatic return" });
  assert.deepStrictEqual(wmMergeSwitch(undefined, true), { checked: false, disabled: true, title: "Merging needs automatic return" });
});

test("merge ack: true only while the switch is on and the confirmed repo is the folder being saved", () => {
  assert.strictEqual(wmMergeAcked(true, "/r", "/r"), true);
  assert.strictEqual(wmMergeAcked(true, "/r", "  /r "), true);
  assert.strictEqual(wmMergeAcked(true, "/r", "/other"), false);
  assert.strictEqual(wmMergeAcked(true, "", ""), false);
  assert.strictEqual(wmMergeAcked(false, "/r", "/r"), false);
});

test("merge turn-on: asks with the folder; Turn on records the repo, Cancel and an empty folder do not", async () => {
  const asked = [];
  const yes = async (r) => { asked.push(r); return true; };
  const no = async (r) => { asked.push(r); return false; };
  assert.deepStrictEqual(await wmMergeTurnOn(" /r ", yes), { mergeHome: true, ackRepo: "/r" });
  assert.deepStrictEqual(await wmMergeTurnOn("/r", no), { mergeHome: false, ackRepo: "" });
  assert.deepStrictEqual(asked, ["/r", "/r"]);
  assert.deepStrictEqual(await wmMergeTurnOn("  ", yes), { mergeHome: false, ackRepo: "" });
  assert.strictEqual(asked.length, 2, "no folder, no dialog");
});

test("merge switch state: a locked profile keeps it disabled whatever auto-return says", () => {
  assert.deepStrictEqual(wmMergeSwitch(true, true, true), { checked: true, disabled: true, title: "" });
  assert.deepStrictEqual(wmMergeSwitch(false, true, true), { checked: false, disabled: true, title: "Merging needs automatic return" });
});
