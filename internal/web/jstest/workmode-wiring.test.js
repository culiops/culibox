// Source checks for the work-mode cards in the profile editor: app.js and
// index.html are read as text (no DOM here), the way checkbox-theme.test.js
// reads the CSS. The pure rules are pinned in workmode.test.js; these pin that
// the editor actually goes through them.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const app = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
const profileform = fs.readFileSync(path.join(STATIC, "profileform.js"), "utf8");

// body: the braces-balanced body of `function name(...) {`.
function body(src, name) {
  const m = new RegExp("(?:async )?function " + name + "\\(").exec(src);
  assert.ok(m, "function " + name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  assert.fail("unbalanced braces in " + name);
}

test("the old clone carry-over is gone", () => {
  assert.doesNotMatch(app, /cloneToSend/);
  assert.doesNotMatch(app, /pfLoadedClone/);
  assert.doesNotMatch(profileform, /cloneToSend/);
});

test("readForm sends the work-mode fields through wmFieldsToSend, and only there", () => {
  const b = body(app, "readForm");
  assert.match(b, /wmFieldsToSend\(/);
  assert.match(b, /pfMode\(\)/);
  assert.doesNotMatch(b, /\bp\.default_cwd\b/, "default_cwd comes from wmFieldsToSend");
});

test("saveProfile refuses an incomplete copy before the PUT starts", () => {
  const b = body(app, "saveProfile");
  const at = b.indexOf("wmFormError(");
  assert.ok(at > 0, "saveProfile must call wmFormError");
  assert.ok(at < b.indexOf("withPending("), "wmFormError must run before withPending");
  assert.match(b, /wmErrText\(/);
  assert.match(b, /wmShareWarningsText\(/);
  assert.ok(b.indexOf("wmShareWarningsText(") > b.lastIndexOf("fillForm("), "the share warning is shown after the refetch, which clears it");
  assert.match(b, /\$\("pf-copy-warn"\)\.hidden = !/, "un-hides pf-copy-warn");
});

test("saveProfile scrolls the share warning into view after un-hiding it", () => {
  const b = body(app, "saveProfile");
  const shown = b.indexOf('$("pf-copy-warn").hidden = !shareWarn');
  assert.ok(shown > 0, "un-hides pf-copy-warn");
  const m = /\$\("pf-copy-warn"\)\.scrollIntoView\(\{ block: "nearest" \}\)/.exec(b);
  assert.ok(m, "scrolls pf-copy-warn with block nearest");
  assert.ok(m.index > shown, "the scroll follows the un-hide");
});

test("duplicateProfile gives a copy profile a fresh copy directory", () => {
  const b = body(app, "duplicateProfile");
  assert.match(b, /wmCopyDirSuggestion\(/);
  assert.ok(b.indexOf("wmCopyDirSuggestion(") < b.indexOf("fillForm("), "before fillForm");
});

test("duplicateProfile decides on the form's mode, and drops the source's copy dir when no fresh one exists", () => {
  const b = body(app, "duplicateProfile");
  assert.match(b, /pfMode\(\)\s*===\s*"copy"/);
  assert.doesNotMatch(b, /if \(draft\.clone_of\)/, "clone_of is omitted when the folder field is cleared");
  assert.match(b, /delete draft\.default_cwd/);
  assert.ok(b.indexOf('pfMode() === "copy"') < b.indexOf("delete draft.default_cwd"));
});

test("fillForm shows the folder to copy in the workspace field", () => {
  const b = body(app, "fillForm");
  assert.match(b, /renderPfWorkspaceOptions\(\s*copy \? p\.clone_of/);
  assert.match(b, /pfSavedMode\s*=/);
  assert.match(b, /pfSavedCopyDir\s*=/);
});

test("syncPfMode is display only", () => {
  const b = body(app, "syncPfMode");
  assert.doesNotMatch(b, /\bfetch\(|\bapi\(|getBoundingClientRect|offsetHeight|offsetWidth|clientHeight|getComputedStyle|innerHTML/);
  assert.match(b, /wmCardState\(/);
  assert.match(b, /wmSwitchNote\(/);
  assert.match(b, /wmNeedsGitHubHint\(/);
});

test("syncPfMode: the folder card tells the truth on the host tier", () => {
  const b = body(app, "syncPfMode");
  assert.ok(b.includes("wmFolderCopy("), "syncPfMode calls wmFolderCopy(");
  assert.match(b, /\(metaLoaded && !boxRuntime\) \|\| segValue\(\$\("pf-runtime"\)\) === "host"/, "no box layer means every session is host");
  assert.match(b, /\$\("pf-mode-folder-body"\)\.textContent/);
  assert.match(b, /\$\("pf-mode-diff-commit"\)\.textContent/);
  assert.ok(html.includes('id="pf-mode-folder-body"') && html.includes('id="pf-mode-diff-commit"'));
});

test("duplicateProfile clears the saved-mode memory, so no stale switch note names a directory that was never made", () => {
  const b = body(app, "duplicateProfile");
  const fill = b.indexOf("fillForm(draft)");
  assert.ok(fill > 0);
  const after = b.slice(fill);
  assert.match(after, /pfSavedMode\s*=\s*"folder"/);
  assert.match(after, /pfSavedCopyDir\s*=\s*""/);
  assert.ok(after.indexOf("syncPfMode()") > after.indexOf("pfSavedCopyDir"), "re-synced after the reset");
});

test("the editor lock closes the work-mode radios too, and syncPfMode runs last", () => {
  assert.match(app, /\.ped input\[type=radio\]/);
  const b = body(app, "syncEditorActions");
  assert.ok(b.trimEnd().replace(/\}$/, "").trimEnd().endsWith("syncPfMode();"), "syncPfMode() is the last statement");
});

test("the meta reads set cloneUnavailable beside boxRuntime", () => {
  assert.match(app, /let cloneUnavailable = ""/);
  const n = (app.match(/cloneUnavailable = \(meta && meta\.clone_unavailable\) \|\| ""/g) || []).length;
  assert.strictEqual(n, 2);
});

test("index.html carries the two cards with their exact copy", () => {
  assert.ok(html.includes("<b>Work in this folder</b>"));
  assert.ok(html.includes("The agent edits your files directly. Your git history stays read-only: you review and commit yourself."));
  assert.ok(html.includes("<b>Work on a private copy</b>"));
  assert.ok(html.includes("The agent gets its own copy: it can commit, branch and push. Your folder is untouched until its work comes home."));
  assert.ok(html.includes("What's the difference?"));
  assert.ok(html.includes('id="pf-cwd-lab"'));
  for (const id of ["pf-mode-block", "pf-copy-block", "pf-copy-dir", "pf-copy-share", "pf-copy-gh", "pf-copy-warn", "pf-copy-err", "pf-mode-hint", "pf-mode-switch", "pf-mode-copy-why"]) {
    assert.ok(html.includes('id="' + id + '"'), id);
  }
});

test("readForm reads the auto-return switch only through wmFieldsToSend", () => {
  const b = body(app, "readForm");
  assert.match(b, /autoReturn:\s*\$\("pf-clone-autoreturn"\)\.checked/);
  assert.doesNotMatch(b, /clone_no_auto_return/, "wmFieldsToSend alone decides whether the field goes out");
  assert.ok(html.includes('id="pf-clone-autoreturn"') && html.includes("Bring work home automatically"));
  assert.ok(html.includes('id="pf-nothome"'));
});

test("fillForm sets the switch from the loaded profile", () => {
  const b = body(app, "fillForm");
  assert.match(b, /pf-clone-autoreturn"\)\.checked\s*=\s*!p\.clone_no_auto_return/);
});

test("how work comes home: the table row is host-aware and no longer names the CLI", () => {
  assert.ok(!html.includes("when you run culibox clone fetch"));
  assert.ok(html.includes("As box/&lt;branch&gt; on its own, or as a PR the agent pushes"));
  assert.ok(html.includes('id="pf-mode-diff-home"'));
  assert.match(body(app, "syncPfMode"), /\$\("pf-mode-diff-home"\)\.textContent\s*=\s*folderCopy\.homeCell/);
});

test("the sessions poll hands the list to noteReturns, which only applies returnNews", () => {
  const poll = body(app, "refreshSessionsRun");
  assert.match(poll, /lastSessions = sessions;\s*sessionsLoaded = true;\s*noteReturns\(lastSessions\)/);
  const b = body(app, "noteReturns");
  assert.match(b, /returnNews\(/);
  assert.match(b, /refreshProfilesQuiet\(\)/);
  assert.doesNotMatch(b, /loadProfiles\(/, "a background return must not rebuild the dialog's controls");
  assert.doesNotMatch(b, /returnSig\(|returnToastText\(/, "the decisions live in workhome.js");
  assert.match(body(app, "loadProfiles"), /notHomeToasts\(\)/);
  assert.match(body(app, "notHomeToasts"), /notHomeNews\(/);
  assert.match(app, /"ab\.notHomeSeen"/);
  // Storage reads and writes are guarded.
  assert.match(body(app, "notHomeSeenRead"), /try \{[\s\S]*localStorage[\s\S]*\} catch/);
  assert.match(body(app, "notHomeSeenWrite"), /try \{[\s\S]*localStorage[\s\S]*\} catch/);
});

test("the quiet profile refresh repaints not-home state only", () => {
  const b = body(app, "refreshProfilesQuiet");
  assert.match(b, /renderProfileList\(\)/);
  assert.match(b, /renderSpawnPlist\(\)/);
  assert.match(b, /renderPfNotHome\(/);
  assert.doesNotMatch(b, /renderOptions\(|renderProfileSeg\(|loadProfiles\(|\$\("agent"\)\.replaceChildren|fillForm\(/,
    "option controls, the profile segment and the editor form are never rebuilt");
});

test("bringHomeNow shows its toast once", () => {
  const b = body(app, "bringHomeNow");
  assert.strictEqual((b.match(/Bringing work home…/g) || []).length, 1);
});

test("work-home text from box and server stays text: no innerHTML in the new renderers", () => {
  for (const fn of ["renderPfNotHome", "noteReturns", "bringHomeNow", "sessionWorkHome", "toastReturnSuffix", "toastHostReturnSuffix", "sessionNotHomeLine", "markHostNotHomeSeen"]) {
    assert.doesNotMatch(body(app, fn), /innerHTML|insertAdjacentHTML/, fn);
  }
  assert.match(body(app, "renderPfNotHome"), /d\.textContent = b\.detail/, "nh.detail shows beside the reason");
});

test("a session with no profile gets no work-home suffix; the spawn line and the rows call workHomeSuffix", () => {
  const b = body(app, "sessionWorkHome");
  assert.match(b, /p \? workHomeSuffix\(p, !!\(s\.clone && s\.clone\.push_repo\)\) : ""/);
  assert.match(app, /smeta\.textContent = notHomePrefix\(s\.clone_of \? sessionNotHomeLine\(s\) : "",\s*sessionMeta\(s, s\.clone_of \? wmCopyLine\(s\.clone_of, window\.HOME_DIR\) \+ sessionWorkHome\(s\)/, "the phone row's meta starts with the not-home line");
  assert.match(body(app, "sessionNotHomeLine"), /sessionNotHome\(s, profiles\.find/);
  assert.match(app, /toastHostReturnSuffix\(s\),\s*\{ undo[^\n]*\n\s*markHostNotHomeSeen\(s\.host_return\)/, "the Host toast marks what it announced as seen");
  assert.match(app, /toastReturnSuffix\(s\) \+ toastHostReturnSuffix\(s\)/, "the spawn toast appends the Host return");
  assert.match(body(app, "toastHostReturnSuffix"), /hostReturnToast\(s\.host_return\)/);
  const sp = fs.readFileSync(path.join(STATIC, "spawnpanel.js"), "utf8");
  assert.match(body(sp, "syncSpawnCopyLine"), /workHomeSuffix\(p, !!\(p\.clone_of && p\.github_write\)\)/);
  assert.match(body(sp, "renderSpawnPlist"), /sp-nothome/);
});

test("the profile row shows work that has not come home, and the menu dispatches bringhome", () => {
  const b = body(app, "renderProfileList");
  assert.ok(b.indexOf('"pnothome"') > 0);
  assert.match(body(app, "runSessionAction"), /bringhome: \(\) => bringHomeNow\(s\)/);
  assert.match(body(app, "bringHomeNow"), /postSessionAction\(s\.id, "bring-home"\)/);
  const css = fs.readFileSync(path.join(STATIC, "app.css"), "utf8");
  assert.doesNotMatch(css, /\.pnothome[^{]*\{[^}]*display:\s*none/, "never hidden");
});

test("the not-home chips wrap and shrink; the profile name keeps a floor width", () => {
  const css = fs.readFileSync(path.join(STATIC, "app.css"), "utf8");
  const rule = (sel) => new RegExp("(?:^|\\n)\\s*" + sel.replace(/[.]/g, "\\.") + "\\s*\\{([^}]*)\\}").exec(css);
  const shared = /\.pitem \.pnothome, \.sp-nothome \{([^}]*)\}/.exec(css)[1];
  assert.match(shared, /white-space:\s*normal/);
  assert.match(shared, /min-width:\s*0/);
  assert.doesNotMatch(shared, /nowrap/);
  const pn = rule(".pitem .pnothome")[1];
  assert.match(pn, /flex:\s*0 1 auto/);
  assert.match(pn, /max-width:\s*60%/);
  assert.match(rule(".sp-nothome")[1], /max-width:\s*100%/);
  const nm = rule(".pitem .pnm")[1];
  assert.match(nm, /min-width:\s*8ch/);
  assert.match(nm, /text-overflow:\s*ellipsis/);
  assert.match(body(app, "renderProfileList"), /nm\.className = "pnm"/);
});

test("legacy form: the merge checkbox sits under auto-return and goes through the shared helpers (#370)", () => {
  const i = html.indexOf('id="pf-clone-autoreturn"'), j = html.indexOf('id="pf-clone-merge"');
  assert.ok(j > i && j < html.indexOf('id="pf-nothome"'), "right under Bring work home automatically");
  assert.ok(html.includes("Merge into the checked-out branch"));
  const f = body(app, "fillForm");
  assert.match(f, /paintPfMerge\(/);
  assert.match(body(app, "paintPfMerge"), /wmMergeSwitch\(/);
  assert.match(f, /pfMergeAckRepo\s*=\s*""/, "a load forgets an earlier confirmation");
  const r = body(app, "readForm");
  assert.match(r, /mergeHome:\s*\$\("pf-clone-merge"\)\.checked/);
  assert.match(r, /mergeAck:\s*wmMergeAcked\(/);
  assert.doesNotMatch(r, /pfLoaded\.clone_merge_home/, "the switch is read from the form, a stored one no longer rides along");
  assert.match(app, /\$\("pf-clone-autoreturn"\)\.addEventListener\("change"/);
  assert.match(app, /\$\("pf-clone-merge"\)\.addEventListener\("change"[\s\S]*wmMergeTurnOn\([\s\S]*cdAskMergeHome/);
});

test("legacy form: the phone's not-home block also shows the merge line and the last merge or refusal (#370)", () => {
  const b = body(app, "renderPfNotHome");
  assert.match(b, /mergeHomeLine\(view && view\.merge_home\)/);
  assert.match(b, /mergeLastLine\(/);
  assert.doesNotMatch(b, /innerHTML/);
});
