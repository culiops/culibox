"use strict";
// workmode.js: the profile editor's pure work-mode rules.
const test = require("node:test");
const assert = require("node:assert");
const { wmModeOf, wmCardState, wmFieldsToSend, wmFormError, wmErrText, wmShareWarningsText, wmCopyDirSuggestion, wmSwitchNote, wmNeedsGitHubHint, wmCopyLine } = require("../static/workmode.js");

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
