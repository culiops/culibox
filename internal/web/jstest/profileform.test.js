"use strict";
// What the profile editor may do, and what a destructive confirm says. Kept out
// of the DOM so the rules can be read and tested without a browser: "built-in"
// is the server's own `editable` verdict, and the containment confirm's wording
// is the whole point of the control.
const test = require("node:test");
const assert = require("node:assert");
const { editorState, saveTarget, duplicateDraft, saveLabel, duplicateName, containmentConfirm, segValue, setSegValue, awsFieldsState, segFromRuntime, runtimeToSend, replyableToSend, contextPathsFromText, contextPathsToText, contextWarningsText, contextRowState, autoReturnToSend } =
  require("../static/profileform.js");

test("a new profile can be saved but not deleted or duplicated", () => {
  assert.deepStrictEqual(editorState(null),
    { locked: false, nameLocked: false, save: "on", delete: "off", duplicate: "off" });
});

test("a stored profile can do everything", () => {
  assert.deepStrictEqual(editorState({ name: "a", editable: true }),
    { locked: false, nameLocked: true, save: "on", delete: "on", duplicate: "on" });
});

test("a built-in is read-only, and duplicate is the way out", () => {
  assert.deepStrictEqual(editorState({ name: "claude", editable: false }),
    { locked: true, nameLocked: true, save: "off", delete: "off", duplicate: "on" });
});

// The Duplicate-then-rename bug: Save picked PUT or POST by whether the typed
// name existed, so renaming a saved profile created a second one and left the
// original behind. A saved profile's name is now fixed, and only a draft (New
// or Duplicate) can choose one.
test("a saved profile keeps its name; only a draft can choose one", () => {
  assert.strictEqual(editorState(null).nameLocked, false);
  assert.strictEqual(editorState({ name: "a", editable: true }).nameLocked, true);
  assert.strictEqual(editorState({ name: "claude", editable: false }).nameLocked, true);
});

test("Save on a saved profile updates it; on a draft it creates exactly one", () => {
  assert.deepStrictEqual(saveTarget("bash", "bash", ["bash"]), { method: "PUT", name: "bash" });
  assert.deepStrictEqual(saveTarget("", "bash copy", ["bash"]), { method: "POST" });
  assert.deepStrictEqual(saveTarget("", "renamed", ["bash", "bash copy"]), { method: "POST" });
});

test("a draft never overwrites a stored profile that has its name", () => {
  const t = saveTarget("", "bash", ["claude", "bash"]);
  assert.strictEqual(t.method, undefined);
  assert.match(t.error, /"bash" already exists/);
});

test("a duplicate draft is the form with a free name, and leaves the form alone", () => {
  const form = { name: "bash", bin: "/bin/bash", args: ["-l"] };
  const d = duplicateDraft(form, "bash", ["bash", "bash copy"]);
  assert.deepStrictEqual(d, { name: "bash copy 2", bin: "/bin/bash", args: ["-l"] });
  assert.strictEqual(form.name, "bash");
});

test("saving into host promises the question", () => {
  assert.strictEqual(saveLabel("host"), "Save…");
  assert.strictEqual(saveLabel("box"), "Save");
  assert.strictEqual(saveLabel(""), "Save");
});

test("a duplicate gets a free name, never a collision", () => {
  assert.strictEqual(duplicateName("claude", ["claude"]), "claude copy");
  assert.strictEqual(duplicateName("claude", ["claude", "claude copy"]), "claude copy 2");
  assert.strictEqual(duplicateName("claude", ["claude", "claude copy", "claude copy 2"]), "claude copy 3");
  assert.strictEqual(duplicateName("claude", []), "claude copy");
});

test("box needs no confirm; host names what it gives away", () => {
  assert.strictEqual(containmentConfirm("box", "a"), null);
  assert.strictEqual(containmentConfirm("", "a"), null);
  const c = containmentConfirm("host", "review");
  assert.strictEqual(c.confirmLabel, "Save as host");
  assert.strictEqual(c.danger, true);
  assert.match(c.body, /review/);
  assert.match(c.body, /no container/i);
  // No type-to-confirm anywhere in this dialog: the confirm states the
  // consequence instead of asking for a password of a name.
  assert.strictEqual(c.mustMatch, undefined);
});

test("a segmented control's value round-trips through its container", () => {
  const el = { dataset: {}, querySelectorAll: () => [] };
  setSegValue(el, "host");
  assert.strictEqual(segValue(el), "host");
  assert.strictEqual(segValue(null), "");
  assert.strictEqual(segValue({ dataset: {} }), "");
});

test("the AWS block is off on a locked profile and on the host tier, never re-enabled by either", () => {
  assert.deepStrictEqual(awsFieldsState(false, ""), { disabled: false, hint: false });
  assert.deepStrictEqual(awsFieldsState(false, "host"), { disabled: true, hint: true });
  // a locked box profile is disabled, but the hint is about the tier, not the lock
  assert.deepStrictEqual(awsFieldsState(true, ""), { disabled: true, hint: false });
  assert.deepStrictEqual(awsFieldsState(true, "host"), { disabled: true, hint: true });
});

test("the editor lights the tier the profile really runs on", () => {
  assert.strictEqual(segFromRuntime("box", "box"), "box");
  assert.strictEqual(segFromRuntime("host", "host"), "host");
  assert.strictEqual(segFromRuntime("", "host"), "host");     // blank on a host-default instance (PROD)
  assert.strictEqual(segFromRuntime(undefined, "box"), "box");
  assert.strictEqual(segFromRuntime(undefined, undefined), "box"); // new profile
});

test("the editor always sends the tier it shows", () => {
  assert.strictEqual(runtimeToSend("box", "", true), "box");
  assert.strictEqual(runtimeToSend("host", "box", true), "host");
  assert.strictEqual(runtimeToSend("", "host", true), "host");
  assert.strictEqual(runtimeToSend("", "", true), "");
});

test("with no box layer, an unset tier lit as box stays unset", () => {
  // The seg lights box for a blank profile whose default resolves to box; sending
  // it would pin a profile that cannot spawn on an instance without containment.
  assert.strictEqual(runtimeToSend("box", "", false), "");
  assert.strictEqual(runtimeToSend("box", "box", false), "box"); // explicit stays explicit
  assert.strictEqual(runtimeToSend("host", "", false), "host");
  assert.strictEqual(runtimeToSend("box", "", true), "box");
});

// The retired per-profile box.image: the server refuses a save that carries it,
// so the editor must never send `box`. app.js needs a DOM and cannot be
// require()d, so pin it at the source level (see profile-save-refill.test.js).
test("readForm never sends a box override", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const app = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");
  const m = /function readForm\s*\(/.exec(app);
  assert.ok(m, "readForm not found in app.js");
  const open = app.indexOf("{", app.indexOf(")", m.index));
  let depth = 0, end = -1;
  for (let i = open; i < app.length && end < 0; i++) {
    if (app[i] === "{") depth++;
    else if (app[i] === "}" && --depth === 0) end = i;
  }
  assert.ok(end > open, "unbalanced braces in readForm");
  const b = app.slice(open, end + 1);
  assert.doesNotMatch(b, /\bp\.box\b/);
  assert.doesNotMatch(b, /boxToSend/);
  assert.doesNotMatch(b, /pfLoadedBox/);
});

test("a hidden replies row keeps what the profile stored", () => {
  assert.strictEqual(replyableToSend(false, false, true), true);
  assert.strictEqual(replyableToSend(false, false, undefined), false);
  assert.strictEqual(replyableToSend(true, false, true), false); // visible: the checkbox decides
  assert.strictEqual(replyableToSend(true, true, false), true);
});

// N3: the Tokens block's pure rules.
const { stGroups, stEntryLine, stTokensToSend } = require("../static/profileform.js");

test("stGroups: providers sorted, entries without one under 'other' last, names sorted", () => {
  const got = stGroups([
    { name: "zeta" }, { name: "sentry-b", provider: "sentry" }, { name: "alpha", provider: "grafana" },
    { name: "sentry-a", provider: "sentry" }, { name: "beta", provider: "" },
  ]);
  assert.deepStrictEqual(got.map((g) => g.provider), ["grafana", "sentry", "other"]);
  assert.deepStrictEqual(got.map((g) => g.entries.map((e) => e.name)), [["alpha"], ["sentry-a", "sentry-b"], ["beta", "zeta"]]);
  assert.deepStrictEqual(stGroups([]), []);
  assert.deepStrictEqual(stGroups(undefined), []);
});

test("stEntryLine: env, config keys and hosts; the port only when not 443; (private) marked", () => {
  const e = {
    env: ["A", "B"], config_env_keys: ["K1"],
    hosts: [{ host: "h1", port: 443 }, { host: "h2", port: 8443, private: true }],
  };
  assert.strictEqual(stEntryLine(e), "env A, B · config K1 · hosts h1, h2:8443 (private)");
  assert.strictEqual(stEntryLine({ env: ["X"], config_env_keys: [], hosts: [] }), "env X");
  assert.strictEqual(stEntryLine({ env: ["X"], hosts: [{ host: "h" }] }), "env X · hosts h");
  assert.strictEqual(stEntryLine(undefined), "");
});

test("stTokensToSend: sorted, no repeats, undefined when none is ticked", () => {
  assert.deepStrictEqual(stTokensToSend(["b", "a", "b"]), ["a", "b"]);
  assert.strictEqual(stTokensToSend([]), undefined);
  assert.strictEqual(stTokensToSend(undefined), undefined);
});

// #155: the AWS connections picker's pure rules.
const { awscPickerRows, awscExclusiveState, awscToSend } = require("../static/profileform.js");

test("awscPickerRows: prod first, then by name; the account and chips come through", () => {
  const meta = [
    { name: "sbx-b", label: "Sandbox B", account_id: "222222222222", prod: false, write_capable: false },
    { name: "prd-z", label: "Prod Z", account_id: "999999999999", prod: true, write_capable: true },
    { name: "sbx-a", account_id: "111111111111", prod: false, write_capable: true },
    { name: "prd-a", prod: true },
  ];
  const got = awscPickerRows(meta, []);
  assert.deepStrictEqual(got.map((r) => r.name), ["prd-a", "prd-z", "sbx-a", "sbx-b"]);
  assert.deepStrictEqual(got[1], { name: "prd-z", label: "Prod Z", prod: true, write: true, account: "999999999999", missing: false });
  assert.deepStrictEqual(got[0], { name: "prd-a", label: "", prod: true, write: false, account: "", missing: false });
  assert.deepStrictEqual(awscPickerRows([], []), []);
  assert.deepStrictEqual(awscPickerRows(undefined, undefined), []);
});

test("awscPickerRows: a ticked name that config no longer defines is missing, and sorts last", () => {
  const meta = [{ name: "sbx-a", prod: false }, { name: "prd-a", prod: true }];
  const got = awscPickerRows(meta, ["zzz-gone", "sbx-a", "aaa-gone"]);
  assert.deepStrictEqual(got.map((r) => [r.name, r.missing]), [["prd-a", false], ["sbx-a", false], ["aaa-gone", true], ["zzz-gone", true]]);
  assert.deepStrictEqual(got[2], { name: "aaa-gone", label: "", prod: false, write: false, account: "", missing: true });
});

test("awscExclusiveState: connections ticked turn the inline binding off", () => {
  assert.deepStrictEqual(awscExclusiveState(2, ""),
    { pickerDisabled: false, inlineDisabled: true, note: "Using AWS connections — the inline binding is off" });
  assert.deepStrictEqual(awscExclusiveState(1, "arn:aws:iam::1:role/x").inlineDisabled, true);
});

test("awscExclusiveState: a filled inline role turns the picker off", () => {
  assert.deepStrictEqual(awscExclusiveState(0, " arn:aws:iam::123456789012:role/x "),
    { pickerDisabled: true, inlineDisabled: false, note: "Convert or remove the inline binding to use connections" });
});

test("awscExclusiveState: neither used leaves both open and says nothing", () => {
  assert.deepStrictEqual(awscExclusiveState(0, ""), { pickerDisabled: false, inlineDisabled: false, note: "" });
  assert.deepStrictEqual(awscExclusiveState(0, "   "), { pickerDisabled: false, inlineDisabled: false, note: "" });
  assert.deepStrictEqual(awscExclusiveState(undefined, undefined), { pickerDisabled: false, inlineDisabled: false, note: "" });
});

test("awscToSend: sorted, no repeats, undefined when none is ticked (the key is left out)", () => {
  assert.deepStrictEqual(awscToSend(["b", "a", "b"]), ["a", "b"]);
  assert.strictEqual(awscToSend([]), undefined);
  assert.strictEqual(awscToSend(undefined), undefined);
  // the form's property assignment follows this: nothing ticked leaves the profile JSON with no key at all
  const p = { name: "x" };
  const names = awscToSend([]);
  if (names) p.aws_connections = names;
  assert.strictEqual(JSON.stringify(p), '{"name":"x"}');
});

// --- Task 14: the profile form picks credentials only; an inline AWS binding is a summary ---
const fs = require("node:fs");
const path = require("node:path");
const { inlineAWSSummary, awsInlineToSend, convertAWSConfirm, convertConnName, convertAWSErrText } =
  require("../static/profileform.js");

test("inlineAWSSummary: source profile, role name and region on one line", () => {
  assert.strictEqual(
    inlineAWSSummary({ source_profile: "sandbox-admin", role_arn: "arn:aws:iam::226198813800:role/agentbox-agent", region: "ap-southeast-1" }),
    "sandbox-admin → agentbox-agent (ap-southeast-1)");
  // a role with a path shows only its name
  assert.strictEqual(
    inlineAWSSummary({ source_profile: "p", role_arn: "arn:aws:iam::226198813800:role/team/x", region: "eu-west-1" }),
    "p → x (eu-west-1)");
});

test("inlineAWSSummary: a missing part never prints 'undefined' or an empty ()", () => {
  assert.strictEqual(inlineAWSSummary({ source_profile: "p", role_arn: "arn:aws:iam::1:role/r" }), "p → r");
  assert.doesNotMatch(inlineAWSSummary({}), /undefined|null|\(\)/);
  assert.doesNotMatch(inlineAWSSummary(null), /undefined|null|\(\)/);
});

test("awsInlineToSend: an untouched inline binding round-trips unchanged, copied", () => {
  const aws = {
    source_profile: "sandbox-admin", role_arn: "arn:aws:iam::226198813800:role/agentbox-agent", region: "ap-southeast-1",
    s3_read: ["b/p/"], label: "sandbox", prod: true, egress_allow: ["*.example.com"],
    elevate: { role_arn: "arn:aws:iam::226198813800:role/agentbox-agent-write", max_minutes: 30, allow_iam: true },
  };
  const out = awsInlineToSend(aws, false);
  assert.deepStrictEqual(out, aws);
  assert.notStrictEqual(out, aws);
  assert.notStrictEqual(out.elevate, aws.elevate);
  // the saved JSON carries it unchanged
  const p = { name: "x" };
  if (out) p.aws = out;
  assert.deepStrictEqual(JSON.parse(JSON.stringify(p)).aws, aws);
});

test("awsInlineToSend: Remove drops it, and a profile with none sends none", () => {
  const aws = { source_profile: "p", role_arn: "arn:aws:iam::1:role/r" };
  assert.strictEqual(awsInlineToSend(aws, true), null);
  assert.strictEqual(awsInlineToSend(null, false), null);
  assert.strictEqual(awsInlineToSend(undefined, false), null);
});

test("convertAWSConfirm: the exact sentence, with the connection name twice", () => {
  assert.strictEqual(convertAWSConfirm("ops-legacy"),
    "This creates a connection named ops-legacy and points the profile at it. The next session gets a new box, and AWS_PROFILE becomes ops-legacy.");
});

test("convertConnName mirrors the server's slug: lowercase, [a-z0-9-] only, no leading dash, 63 max, 'aws' if empty", () => {
  assert.strictEqual(convertConnName("ops-legacy"), "ops-legacy");
  assert.strictEqual(convertConnName("Ops Legacy_2"), "ops-legacy-2");
  assert.strictEqual(convertConnName("--x"), "x");
  assert.strictEqual(convertConnName("___"), "aws");
  assert.strictEqual(convertConnName("a".repeat(80)).length, 63);
});

test("convertAWSErrText: the two refusals with their own wording", () => {
  assert.strictEqual(convertAWSErrText({ status: 409, code: "profile_changed" }),
    "This profile changed while converting; reload and try again");
  assert.strictEqual(convertAWSErrText({ status: 409, code: "no_broker" }),
    "AWS connections need the boxed runtime");
  assert.strictEqual(convertAWSErrText({ status: 409, code: "exists" }), "");
  assert.strictEqual(convertAWSErrText(null), "");
});

test("the 'none defined' hints no longer send the reader to config.json; each is a link to the Credentials tab", () => {
  const html = fs.readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");
  for (const [id, group] of [["pf-awsc-none", "aws"], ["pf-gh-noapps", "github"], ["pf-st-none", "tokens"]]) {
    const m = html.match(new RegExp('<p[^>]*id="' + id + '"[^>]*>([\\s\\S]*?)</p>'));
    assert.ok(m, id + " is in the page");
    assert.doesNotMatch(m[1], /config\.json|restart|aws_connections|github_apps|static_tokens/, id);
    assert.match(m[1], new RegExp('<a [^>]*data-creds-group="' + group + '"'), id);
  }
});

test("the inline AWS inputs are gone from the profile form", () => {
  const html = fs.readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");
  for (const id of ["profile", "region", "role", "s3", "egress", "label", "prod", "elev-role", "elev-max", "elev-iam", "elev-warn", "elev-help"]) {
    assert.ok(!html.includes('id="pf-aws-' + id + '"'), "pf-aws-" + id);
  }
  assert.ok(html.includes('id="pf-aws-convert"') && html.includes('id="pf-aws-remove"'));
});

// A live refresh of the pickers (the lists changed in Credentials) keeps the
// ticks the operator made, and drops only those for an entry that WAS defined
// and no longer is. A tick for a name that was never defined (a stored profile
// pointing at a removed entry) stays, so its "not defined" row survives.
const { ticksAfterRefresh } = require("../static/profileform.js");

test("a refresh keeps ticks for entries that still exist", () => {
  assert.deepStrictEqual(ticksAfterRefresh(["a", "b"], ["a", "b"], ["a", "b", "c"]), ["a", "b"]);
});

test("a refresh drops a tick whose entry was deleted meanwhile", () => {
  assert.deepStrictEqual(ticksAfterRefresh(["a", "b"], ["a", "b"], ["a"]), ["a"]);
});

test("a tick on a name that was never defined stays so the server names it", () => {
  assert.deepStrictEqual(ticksAfterRefresh(["gone"], ["a"], ["a", "b"]), ["gone"]);
});

test("a tick on a newly defined entry is kept and nothing is invented", () => {
  assert.deepStrictEqual(ticksAfterRefresh(["new"], ["a"], ["a", "new"]), ["new"]);
  assert.deepStrictEqual(ticksAfterRefresh([], ["a"], ["a", "b"]), []);
  assert.deepStrictEqual(ticksAfterRefresh(null, null, null), []);
});

test("context paths: one per line, trimmed, blanks dropped, order kept", () => {
  assert.deepStrictEqual(contextPathsFromText("  /a\n\n/b  \n"), ["/a", "/b"]);
  assert.deepStrictEqual(contextPathsFromText(""), []);
  assert.deepStrictEqual(contextPathsFromText(undefined), []);
});

test("context paths fill the textarea one per line, empty when there are none", () => {
  assert.strictEqual(contextPathsToText(["/a", "/b"]), "/a\n/b");
  assert.strictEqual(contextPathsToText(undefined), "");
  assert.strictEqual(contextPathsToText(null), "");
});

test("context warnings come from the save response body, safely absent", () => {
  assert.strictEqual(contextWarningsText({ warnings: ["x", "y"] }), "x\ny");
  // clone_share lines belong to the private-copy block (wmShareWarningsText)
  assert.strictEqual(contextWarningsText({ warnings: ['clone_share "a" …', "x", "y"] }), "x\ny");
  assert.strictEqual(contextWarningsText({ warnings: [] }), "");
  assert.strictEqual(contextWarningsText({}), "");
  assert.strictEqual(contextWarningsText(null), "");
  assert.strictEqual(contextWarningsText(undefined), "");
});

test("readable host paths: hidden on an empty host profile, flagged on a filled one", () => {
  // A boxed profile always shows the row, with no hint.
  assert.deepStrictEqual(contextRowState("box", ""), { hidden: false, hint: false });
  assert.deepStrictEqual(contextRowState("box", "/a"), { hidden: false, hint: false });
  assert.deepStrictEqual(contextRowState("", "/a"), { hidden: false, hint: false });
  // Host with nothing listed: nothing to say, the row hides.
  assert.deepStrictEqual(contextRowState("host", ""), { hidden: true, hint: false });
  assert.deepStrictEqual(contextRowState("host", "  \n \n"), { hidden: true, hint: false });
  // Host with paths: the server will refuse the save, so the row stays visible
  // with the "needs runtime: box" hint instead of hiding the conflict.
  assert.deepStrictEqual(contextRowState("host", "/a\n"), { hidden: false, hint: true });
});

// pfTouchProfile turns what the editor holds (readForm's request body) into the
// ProfileView shape reachRows reads, so the Settings page's "Can touch" line and
// the New session box are drawn by the same rules.
const { pfTouchProfile } = require("../static/profileform.js");

test("pfTouchProfile maps the form's bindings to the view fields reachRows reads", () => {
  const form = {
    name: "sre", bin: "claude", account: "work",
    aws_connections: ["culipulse-prod"],
    github: { owners: [{ owner: "culiops", app: "ci" }, { owner: "", app: "x" }], permissions: { contents: "read" } },
    tokens: ["npm"],
  };
  assert.deepStrictEqual(pfTouchProfile(form, { claude: true }), {
    bin: "claude", account: "work", claude: true,
    aws_connections: ["culipulse-prod"], github_owners: ["culiops"], tokens: ["npm"],
    network: "allowlist",
  });
});

test("pfTouchProfile names the inline binding from the saved view, else from the form", () => {
  const view = { aws_inline: { name: "legacy", prod: true, elevate_max_minutes: 30 } };
  assert.deepStrictEqual(pfTouchProfile({ bin: "claude", aws: { role_arn: "r" } }, { view }).aws_inline, view.aws_inline);
  assert.deepStrictEqual(pfTouchProfile({ bin: "claude", aws: { label: "ops", prod: true } }, {}).aws_inline, { name: "ops", prod: true });
  assert.deepStrictEqual(pfTouchProfile({ bin: "claude", aws: {} }, {}).aws_inline, { name: "aws", prod: false });
  // Remove pressed: readForm leaves `aws` out, and so does the touch line.
  assert.strictEqual(pfTouchProfile({ bin: "claude" }, { view }).aws_inline, undefined);
});

test("pfTouchProfile: unbound is open only when egress is mandatory, else no posture", () => {
  assert.strictEqual(pfTouchProfile({ bin: "claude" }, { egressAll: true }).network, "open");
  assert.strictEqual(pfTouchProfile({ bin: "claude" }, {}).network, "");
  assert.strictEqual(pfTouchProfile({ bin: "claude", tokens: ["t"] }, {}).network, "allowlist");
  assert.strictEqual(pfTouchProfile(null, null).claude, false);
});

test("pfTouchProfile carries clone_of only for a private copy", () => {
  const { pfTouchProfile } = require("../static/profileform.js");
  assert.strictEqual(pfTouchProfile({ clone_of: "/h/r", bin: "claude" }, {}).clone_of, "/h/r");
  assert.ok(!("clone_of" in pfTouchProfile({ bin: "claude" }, {})));
  assert.ok(!("clone_of" in pfTouchProfile({ clone_of: "", bin: "claude" }, {})));
});

test("autoReturnToSend: only an unticked switch is sent", () => {
  assert.deepStrictEqual(autoReturnToSend(true), {});
  assert.deepStrictEqual(autoReturnToSend(false), { clone_no_auto_return: true });
});
