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
  assert.match(b, /\(metaLoaded && !boxRuntime\) \|\| segValue\(\$\("pf-runtime"\)\) === "host"/, "same tier test as renderPfTouch");
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

test("the profile form keeps scrolled-to lines above the sticky footer", () => {
  // setDialogError scrolls the error block with block:"nearest". On desktop the
  // scroller .ped runs under the 56px #foot-manage, so scroll-padding-bottom
  // (footer 56px + 16px) is what keeps the line clear of the footer.
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ selector: m[1], body: m[2] }));
  const hit = rules.find((r) => r.selector.includes("#settings-modal.as-page #manage .ped") && /scroll-padding-bottom:\s*72px\s*;/.test(r.body));
  assert.ok(hit, "a desktop.css rule for #settings-modal.as-page #manage .ped must set scroll-padding-bottom: 72px");
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
  assert.ok(!html.includes("when you run agent-box clone fetch"));
  assert.ok(html.includes("As box/&lt;branch&gt; on its own, or as a PR the agent pushes"));
  assert.ok(html.includes('id="pf-mode-diff-home"'));
  assert.match(body(app, "syncPfMode"), /\$\("pf-mode-diff-home"\)\.textContent\s*=\s*folderCopy\.homeCell/);
});

test("the sessions poll hands the list to noteReturns, and return news goes through workhome.js", () => {
  const poll = body(app, "refreshSessions");
  assert.match(poll, /lastSessions = sessions \|\| \[\];\s*sessionsLoaded = true;\s*noteReturns\(lastSessions\)/);
  const b = body(app, "noteReturns");
  assert.match(b, /returnsSeen === null/, "the first poll seeds silently");
  assert.match(b, /returnToastText\(s\.agent, s\.return\)/);
  assert.match(b, /returnSig\(/);
  assert.match(b, /loadProfiles\(/);
  assert.match(body(app, "loadProfiles"), /notHomeToasts\(\)/);
  const t = body(app, "notHomeToasts");
  assert.match(t, /notHomeNews\(/);
  assert.match(app, /"ab\.notHomeSeen"/);
  // Storage reads and writes are guarded.
  assert.match(body(app, "notHomeSeenRead"), /try \{[\s\S]*localStorage[\s\S]*\} catch/);
  assert.match(body(app, "notHomeSeenWrite"), /try \{[\s\S]*localStorage[\s\S]*\} catch/);
});

test("work-home text from box and server stays text: no innerHTML in the new renderers", () => {
  for (const fn of ["renderPfNotHome", "noteReturns", "bringHomeNow", "sessionWorkHome", "toastReturnSuffix"]) {
    assert.doesNotMatch(body(app, fn), /innerHTML|insertAdjacentHTML/, fn);
  }
  assert.match(body(app, "renderPfNotHome"), /d\.textContent = b\.detail/, "nh.detail shows beside the reason");
});

test("a session with no profile gets no work-home suffix; the spawn line and the rows call workHomeSuffix", () => {
  const b = body(app, "sessionWorkHome");
  assert.match(b, /p \? workHomeSuffix\(p\) : ""/);
  assert.match(app, /wmCopyLine\(s\.clone_of, window\.HOME_DIR\) \+ sessionWorkHome\(s\)/);
  const sp = fs.readFileSync(path.join(STATIC, "spawnpanel.js"), "utf8");
  assert.match(body(sp, "syncSpawnCopyLine"), /workHomeSuffix\(p\)/);
  assert.match(body(sp, "renderSpawnPlist"), /sp-nothome/);
});

test("the profile row badge sits outside .psub and the menu dispatches bringhome", () => {
  const b = body(app, "renderProfileList");
  assert.ok(b.indexOf('"pnothome"') > 0 && b.indexOf('"pnothome"') < b.indexOf('"psub"'));
  assert.match(body(app, "runSessionAction"), /bringhome: \(\) => bringHomeNow\(s\)/);
  assert.match(body(app, "bringHomeNow"), /postSessionAction\(s\.id, "bring-home"\)/);
  const css = fs.readFileSync(path.join(STATIC, "app.css"), "utf8");
  assert.doesNotMatch(css, /\.pnothome[^{]*\{[^}]*display:\s*none/, "never hidden");
});
