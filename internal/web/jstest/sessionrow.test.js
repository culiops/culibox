"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { attachedElsewhere, rowClasses, sessionMeta, menuItems, jumpTarget, canInterrupt, paletteActionItems, actionFailText, roleName, awsBadge, awsRailRows, readAWSFields } =
  require("../static/sessionrow.js");

const sess = (over) => Object.assign({ id: "claude-1", attached: false, exited: false }, over);

test("a session nobody is attached to is never elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess(), false), false);
  assert.strictEqual(attachedElsewhere(sess(), true), false);
});

test("attached and held by this page is not elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true }), true), false);
});

test("attached and not held by this page is elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true }), false), true);
});

// An exited session's viewer is not something anyone can act on, and the row
// already says "exited".
test("an exited session is never elsewhere, even while attached", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true, exited: true }), false), false);
});

// An older daemon behind a newer page sends no `attached` at all. The badge must
// never appear on a claim the server did not make.
test("a response with no attached field reads as not attached", () => {
  assert.strictEqual(attachedElsewhere({ id: "claude-1", exited: false }, false), false);
});

test("a missing session is not elsewhere rather than a crash", () => {
  assert.strictEqual(attachedElsewhere(null, false), false);
  assert.strictEqual(attachedElsewhere(undefined, false), false);
});

// Guards against a truthy-but-not-true value ever being treated as attached.
test("only a real boolean true counts as attached", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: "yes" }), false), false);
  assert.strictEqual(attachedElsewhere(sess({ attached: 1 }), false), false);
});

// The row has exactly two states above its base, and they are different axes:
// "needs you" is about the agent, "selected" is about where your keystrokes go.
// A row can carry both at once and must then carry both classes.
test("rowClasses marks a waiting session as needing you", () => {
  assert.deepEqual(rowClasses({ activity: "waiting" }, {}), ["srow", "needs"]);
  assert.deepEqual(rowClasses({ activity: "working" }, {}), ["srow"]);
  assert.deepEqual(rowClasses({ activity: "crashed" }, {}), ["srow"]);
});

test("rowClasses marks the focused pane's session as selected", () => {
  assert.deepEqual(rowClasses({ id: "a", activity: "working" }, { selectedId: "a" }),
    ["srow", "sel"]);
  assert.deepEqual(rowClasses({ id: "a", activity: "working" }, { selectedId: "b" }),
    ["srow"]);
  // Nothing is selected when the focused pane shows a viewer or holds no tabs.
  assert.deepEqual(rowClasses({ id: "a", activity: "working" }, { selectedId: null }),
    ["srow"]);
});

test("rowClasses carries both states at once", () => {
  assert.deepEqual(rowClasses({ id: "a", activity: "waiting" }, { selectedId: "a" }),
    ["srow", "sel", "needs"]);
});

// A teardown in flight is neither: the row is disabled, and painting it amber
// would advertise a session you can no longer answer.
test("rowClasses marks a killing row and drops the other states", () => {
  assert.deepEqual(rowClasses({ id: "a", activity: "waiting" }, { selectedId: "a", killing: true }),
    ["srow", "killing"]);
});

// The meta line is one line, three segments, and any missing segment is dropped
// rather than leaving a dangling separator.
test("sessionMeta joins agent, status and directory", () => {
  assert.equal(sessionMeta({ agent: "claude" }, "~/culifeed", "working"),
    "claude · working · ~/culifeed");
  assert.equal(sessionMeta({ agent: "sh" }, "~/x", "exited"), "sh · exited · ~/x");
});

test("sessionMeta drops empty segments instead of leaving separators", () => {
  assert.equal(sessionMeta({ agent: "claude" }, "", "working"), "claude · working");
  assert.equal(sessionMeta({}, "~/x", ""), "~/x");
  assert.equal(sessionMeta({}, "", ""), "");
  assert.equal(sessionMeta(null, null, null), "");
});

// The menu's contents are fixed, but one item is meaningless on a non-claude
// session and a pair of them is meaningless without a working directory.
// "End session…" is always last and always the only dangerous one.
test("menuItems offers the full list for a claude session with a cwd", () => {
  const ids = menuItems({ claude: true, cwd: "/home/x" }).map((i) => i.id);
  assert.deepEqual(ids, ["rename", "files", "attribution", "copycwd", "newpane", "replies-on", "duplicate", "end"]);
});

test("menuItems drops context-by-tool for a non-claude session", () => {
  const ids = menuItems({ claude: false, cwd: "/home/x" }).map((i) => i.id);
  assert.deepEqual(ids, ["rename", "files", "copycwd", "newpane", "duplicate", "end"]);
});

test("menuItems drops the directory actions when there is no cwd", () => {
  const ids = menuItems({ claude: true }).map((i) => i.id);
  assert.deepEqual(ids, ["rename", "attribution", "newpane", "replies-on", "duplicate", "end"]);
});

test("menuItems always ends with the one dangerous item", () => {
  for (const s of [{ claude: true, cwd: "/x" }, { claude: false }, {}]) {
    const items = menuItems(s);
    const last = items[items.length - 1];
    assert.equal(last.id, "end");
    assert.equal(last.label, "End session…");
    assert.equal(last.danger, true);
    assert.equal(items.filter((i) => i.danger).length, 1);
  }
});

// The number keys address the sidebar list the operator is looking at, 1-based.
test("jumpTarget maps 1..9 onto the session list", () => {
  const list = [{ id: "a" }, { id: "b" }, { id: "c" }];
  assert.equal(jumpTarget(1, list).id, "a");
  assert.equal(jumpTarget(3, list).id, "c");
});

test("jumpTarget returns null for anything out of range", () => {
  const list = [{ id: "a" }];
  assert.equal(jumpTarget(2, list), null);
  assert.equal(jumpTarget(0, list), null);
  assert.equal(jumpTarget(1, []), null);
  assert.equal(jumpTarget(1, null), null);
  assert.equal(jumpTarget(NaN, list), null);
});

test("canInterrupt: live only; a claude only while it has a turn to stop", () => {
  assert.strictEqual(canInterrupt({ status: "live", claude: false, activity: "idle" }), true); // a shell's quiet `sleep`
  assert.strictEqual(canInterrupt({ status: "live", claude: true, activity: "working" }), true);
  assert.strictEqual(canInterrupt({ status: "live", claude: true, activity: "waiting", need: "ask" }), true);
  // Esc on an idle claude prompt does nothing useful, and a second one opens rewind.
  assert.strictEqual(canInterrupt({ status: "live", claude: true, activity: "waiting", need: "turn" }), false);
  assert.strictEqual(canInterrupt({ status: "live", claude: true, activity: "idle" }), false);
  assert.strictEqual(canInterrupt({ status: "exited", claude: false }), false);
  assert.strictEqual(canInterrupt({ status: "detached", claude: false }), false);
  assert.strictEqual(canInterrupt(null), false);
});

test("menuItems adds the control actions per session state", () => {
  const ids = (s) => menuItems(s).map((i) => i.id);
  assert.deepStrictEqual(ids({ claude: true, status: "live", activity: "working", replyable: true, cwd: "/x" }),
    ["rename", "files", "attribution", "copycwd", "newpane", "interrupt", "replies-off", "duplicate", "end"]);
  assert.deepStrictEqual(ids({ claude: false, status: "live", activity: "idle" }),
    ["rename", "newpane", "interrupt", "duplicate", "end"]);
  const exited = menuItems({ claude: true, status: "exited", exited: true, activity: "done", resumable: true });
  assert.deepStrictEqual(exited.map((i) => i.id), ["rename", "attribution", "newpane", "restart", "duplicate", "end"]);
  assert.strictEqual(exited.find((i) => i.id === "restart").label, "Restart (resume conversation)");
  assert.strictEqual(menuItems({ status: "exited", exited: true }).find((i) => i.id === "restart").label, "Restart");
  assert.strictEqual(menuItems({ claude: true }).find((i) => i.id === "duplicate").label, "Duplicate…");
});

test("paletteActionItems: the focused session's commands, labels without the ellipsis", () => {
  const items = paletteActionItems({ claude: true, status: "live", activity: "working", replyable: false, cwd: "/x" });
  assert.deepStrictEqual(items.map((i) => i.id), ["interrupt", "rename", "replies-on", "duplicate", "newpane"]);
  assert.deepStrictEqual(items.map((i) => i.label), ["Interrupt", "Rename", "Allow replies", "Duplicate", "Open in new pane"]);
  const exited = paletteActionItems({ status: "exited", exited: true });
  assert.ok(!exited.some((i) => i.id === "interrupt"));
  assert.ok(exited.some((i) => i.id === "restart"));
  assert.ok(!paletteActionItems({}).some((i) => i.id === "end"), "End session has its own palette row");
});

test("actionFailText words each failure", () => {
  assert.match(actionFailText("interrupt", 409), /not running/);
  assert.match(actionFailText("restart", 409), /ended session/);
  assert.match(actionFailText("restart", 404), /gone/);
  assert.match(actionFailText("interrupt", 0), /no connection/i);
  assert.match(actionFailText("interrupt", 500), /500/);
});

test("actionFailText: 429 (ErrTooSoon) gets its own text for any action", () => {
  assert.strictEqual(actionFailText("interrupt", 429), "Too soon — try again in a second.");
  assert.strictEqual(actionFailText("restart", 429), "Too soon — try again in a second.");
});

const ARN = "arn:aws:iam::123456789012:role/agentbox-agent";

test("roleName is the last path segment of a role ARN", () => {
  assert.strictEqual(roleName(ARN), "agentbox-agent");
  assert.strictEqual(roleName("arn:aws:iam::123456789012:role/team/deep-role"), "deep-role");
  assert.strictEqual(roleName("bare"), "bare");
  assert.strictEqual(roleName(undefined), "");
});

test("awsBadge: label, account fallback, prod flag", () => {
  assert.strictEqual(awsBadge({}), null);
  assert.strictEqual(awsBadge(null), null);
  assert.deepStrictEqual(awsBadge({ aws: { label: "sandbox", account_id: "123456789012", role_arn: ARN, prod: false } }),
    { text: "sandbox", prod: false, title: "AWS 123456789012 · agentbox-agent · read-only" });
  assert.strictEqual(awsBadge({ aws: { label: "", account_id: "123456789012", role_arn: ARN, prod: true } }).text, "aws:123456789012");
  assert.strictEqual(awsBadge({ aws: { label: "prd", account_id: "1", role_arn: "x", prod: true } }).prod, true);
});

test("awsRailRows: short rows that wrap, and a production chip that is its own row", () => {
  assert.deepStrictEqual(awsRailRows({}), []);
  assert.deepStrictEqual(awsRailRows({ aws: { label: "sandbox", account_id: "123456789012", role_arn: ARN, prod: false } }), [
    { k: "aws", v: "sandbox · 123456789012", wrap: true },
    { k: "role", v: "agentbox-agent", wrap: true },
    { k: "scope", v: "read-only" },
  ]);
  // no label: the account alone, not "aws · 1234"
  assert.strictEqual(awsRailRows({ aws: { label: "", account_id: "123456789012", role_arn: ARN } })[0].v, "123456789012");
  // prod is a row of its own, drawn as the red chip: it cannot be clipped by a long label
  const prod = awsRailRows({ aws: { label: "x".repeat(40), account_id: "123456789012", role_arn: ARN, prod: true } });
  assert.deepStrictEqual(prod[prod.length - 1], { k: "prod", v: "production", chip: "bdg aws prod" });
  assert.strictEqual(prod.filter((r) => r.k === "prod").length, 1);
});

test("readAWSFields: omitted when role is empty; trims and splits the allowlist", () => {
  const vals = { "pf-aws-role": "", "pf-aws-profile": "x", "pf-aws-region": "ap-southeast-1", "pf-aws-s3": "", "pf-aws-label": "", "pf-aws-prod": false };
  assert.strictEqual(readAWSFields((id) => vals[id]), undefined);
  vals["pf-aws-role"] = " " + ARN + " ";
  vals["pf-aws-s3"] = "culishop-tfstate\n\n  culishop-logs/AWSLogs/ \n";
  vals["pf-aws-prod"] = true;
  assert.deepStrictEqual(readAWSFields((id) => vals[id]), {
    source_profile: "x", role_arn: ARN, region: "ap-southeast-1",
    s3_read: ["culishop-tfstate", "culishop-logs/AWSLogs/"], label: "", prod: true });
});
