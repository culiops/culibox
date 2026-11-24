"use strict";
// Replies are ON for the assertions below; the kill-switch group at the end turns them off.
globalThis.repliesEnabled = true;
const test = require("node:test");
const assert = require("node:assert");
const { attachedElsewhere, rowClasses, sessionMeta, menuItems, menuGroups, menuIcon, jumpTarget, canInterrupt, paletteActionItems, actionFailText, restartNotice, roleName, awsBadge, badgeParts, awsSig, awsRailRows, elevActions, awsConnRailRows, awsBadgeParts, connWindowLine, liveWindowConn, baseScope, baseReasonText, BASE_REASON, ROLE_WRITE_SUFFIX } =
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

// A companion shell listed under its parent keeps only what means something for
// a shell: no restart, duplicate, reply opt-in or context-by-tool.
test("menuItems for a nested shell keeps rename, files, copy cwd, new pane and end", () => {
  const sh = { claude: false, cwd: "/home/x", exited: true, status: "live", parent_id: "p" };
  assert.deepEqual(menuItems(sh, { nested: true }).map((i) => i.id), ["rename", "files", "copycwd", "newpane", "end"]);
  assert.ok(!menuItems(sh, { nested: true }).some((i) => i.id === "interrupt"), "interrupt stays hidden");
  const noCwd = { claude: false, parent_id: "p" };
  assert.deepEqual(menuItems(noCwd, { nested: true }).map((i) => i.id), ["rename", "newpane", "end"]);
  const last = menuItems(sh, { nested: true }).pop();
  assert.equal(last.danger, true);
  assert.ok(menuItems(sh).map((i) => i.id).includes("duplicate"), "no opts: unchanged");
});

// menuGroups: the kit menu draws the items in four groups (M5.1), a hairline between groups and
// none under every item. The grouping is a fact about the items, so it lives here and not in the
// DOM builder.
const gids = (groups) => groups.map((g) => g.map((i) => i.id));

test("menuGroups: a full session is rename | the where-actions | the control actions | end", () => {
  const live = { claude: true, cwd: "/x", status: "live", activity: "working", clone_of: "/r", runtime: "box" };
  assert.deepStrictEqual(gids(menuGroups(menuItems(live))), [
    ["rename"],
    ["files", "attribution", "copycwd", "newpane"],
    ["interrupt", "replies-on", "bringhome", "duplicate"],
    ["end"],
  ]);
  // An ended session: Restart joins the control group, after the reply opt-in's slot and before Duplicate.
  const ended = { claude: true, cwd: "/x", status: "exited", exited: true, resumable: true };
  assert.deepStrictEqual(gids(menuGroups(menuItems(ended))), [
    ["rename"], ["files", "attribution", "copycwd", "newpane"], ["restart", "duplicate"], ["end"],
  ]);
  // Stop replies sits where Allow replies does.
  assert.deepStrictEqual(gids(menuGroups(menuItems({ claude: true, status: "live", activity: "working", replyable: true })))[2], ["interrupt", "replies-off", "duplicate"]);
});

test("menuGroups: End session… is last and alone", () => {
  for (const s of [{ claude: true, cwd: "/x", status: "live", activity: "working" }, { claude: false }, {}, { status: "exited", exited: true }]) {
    const groups = menuGroups(menuItems(s));
    const last = groups[groups.length - 1];
    assert.deepStrictEqual(gids([last]), [["end"]]);
    assert.strictEqual(last[0].danger, true);
    assert.strictEqual(groups.flat().filter((i) => i.danger).length, 1, "nothing else is red");
  }
});

test("menuGroups: a nested shell is rename | files, copy cwd, new pane | end", () => {
  const sh = { claude: false, cwd: "/home/x", status: "live", parent_id: "p" };
  assert.deepStrictEqual(gids(menuGroups(menuItems(sh, { nested: true }))), [["rename"], ["files", "copycwd", "newpane"], ["end"]]);
  assert.deepStrictEqual(gids(menuGroups(menuItems({ parent_id: "p" }, { nested: true }))), [["rename"], ["newpane"], ["end"]]);
});

test("menuGroups: an empty group is dropped, so there are never two separators in a row", () => {
  for (const s of [{}, { claude: true }, { claude: false, cwd: "/x" }, { status: "exited", exited: true }, { parent_id: "p" }]) {
    for (const opts of [undefined, { nested: true }]) {
      const groups = menuGroups(menuItems(s, opts));
      assert.ok(groups.length > 0 && groups.every((g) => g.length > 0), JSON.stringify([s, opts]));
    }
  }
  // No cwd, not a claude: the where-actions group is one item, not an empty hole.
  assert.deepStrictEqual(gids(menuGroups(menuItems({ claude: false }))), [["rename"], ["newpane"], ["duplicate"], ["end"]]);
  assert.deepStrictEqual(menuGroups([]), []);
  assert.deepStrictEqual(menuGroups(null), []);
});

test("menuGroups: every item comes through once, in menuItems' own order", () => {
  const full = { claude: true, cwd: "/x", status: "live", activity: "working", clone_of: "/r", runtime: "box" };
  for (const s of [full, { claude: true, cwd: "/x", status: "exited", exited: true }, {}]) {
    const items = menuItems(s);
    assert.deepStrictEqual(menuGroups(items).flat(), items);
  }
  // An id the groups do not know is kept (a menu item must not vanish), with the control actions.
  const odd = [{ id: "rename", label: "Rename…" }, { id: "future", label: "Future" }, { id: "end", label: "End session…", danger: true }];
  assert.deepStrictEqual(gids(menuGroups(odd)), [["rename"], ["future"], ["end"]]);
});

test("menuGroups: an id that is an Object.prototype name joins the control group and never throws", () => {
  const protoIds = ["constructor", "toString", "hasOwnProperty", "__proto__", "valueOf"];
  const items = [{ id: "rename", label: "Rename…" }, ...protoIds.map((id) => ({ id, label: id })), { id: "end", label: "End session…", danger: true }];
  let groups;
  assert.doesNotThrow(() => { groups = menuGroups(items); });
  assert.deepStrictEqual(gids(groups), [["rename"], protoIds, ["end"]]);
});

// The icon is an ICON_MENU kind the DOM code draws as a vendored glyph (a font glyph is tofu where no installed font has it).
test("menuIcon: the kind of drawing each row has, and none for the two the frame draws no icon for", () => {
  assert.deepStrictEqual(
    ["rename", "files", "attribution", "copycwd", "newpane", "interrupt", "bringhome", "duplicate", "end"].map(menuIcon),
    ["rename", "files", "attribution", "copy", "split", "interrupt", "bringhome", "duplicate", "end"]);
  assert.strictEqual(menuIcon("copycwd"), "copy");
  assert.strictEqual(menuIcon("restart"), "", "S2 gives Restart its glyph");
  for (const id of ["replies-on", "replies-off", "restart", "nope", undefined]) assert.strictEqual(menuIcon(id), "", String(id));
  // Every id menuItems can produce has an answer (a string), so the builder never prints "undefined".
  const every = menuItems({ claude: true, cwd: "/x", status: "live", activity: "working", clone_of: "/r", runtime: "box" })
    .concat(menuItems({ status: "exited", exited: true }));
  for (const i of every) assert.strictEqual(typeof menuIcon(i.id), "string", i.id);
  // No two rows share a drawing: Copy working directory and Duplicate in particular stay apart.
  const kinds = new Map(every.map((i) => [i.id, menuIcon(i.id)]));
  const drawn = [...kinds.values()].filter((k) => k !== "");
  assert.strictEqual(new Set(drawn).size, drawn.length, "one kind per id");
  const { ICON_MENU } = require("../static/iconmap.js");
  for (const k of drawn) assert.ok(Object.prototype.hasOwnProperty.call(ICON_MENU, k), k + " is an ICON_MENU key");
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

// A boxed restart that could not continue the old conversation says so once;
// every other restart (resumed, host, an older daemon) says nothing.
test("restartNotice names a fresh conversation only when the server skipped the resume", () => {
  assert.strictEqual(restartNotice({ id: "claude-2", resume_skipped: "the previous conversation is not in this box's home" }),
    "Started a new conversation: the previous one is not in this box anymore");
  assert.strictEqual(restartNotice({ id: "claude-2" }), "");
  assert.strictEqual(restartNotice({ id: "claude-2", resume_skipped: "" }), "");
  assert.strictEqual(restartNotice(null), "");
  assert.strictEqual(restartNotice(undefined), "");
});

test("actionFailText: 429 (ErrTooSoon) gets its own text for any action", () => {
  assert.strictEqual(actionFailText("interrupt", 429), "Too soon — try again in a second.");
  assert.strictEqual(actionFailText("restart", 429), "Too soon — try again in a second.");
});

const ARN = "arn:aws:iam::123456789012:role/agentbox-agent";
const NOW = Date.parse("2027-01-15T08:00:00Z");
const iso = (min) => new Date(NOW + min * 60000).toISOString();
const ELEV = { label: "sandbox", account_id: "210987654321", role_arn: ARN, prod: false,
  elevate: { role_arn: "arn:aws:iam::210987654321:role/agentbox-agent-write", max_minutes: 60, allow_iam: false } };

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
    { text: "sandbox", prod: false, write: false, roleWrite: false, title: "AWS 123456789012 · agentbox-agent · base role unverified (not checked)" });
  assert.strictEqual(awsBadge({ aws: { label: "", account_id: "123456789012", role_arn: ARN, prod: true } }).text, "aws:123456789012");
  assert.strictEqual(awsBadge({ aws: { label: "prd", account_id: "1", role_arn: "x", prod: true } }).prod, true);
});

test("awsRailRows: short rows that wrap, and a production chip that is its own row", () => {
  assert.deepStrictEqual(awsRailRows({}, null, NOW), []);
  assert.deepStrictEqual(awsRailRows({ aws: { label: "sandbox", account_id: "123456789012", role_arn: ARN, prod: false } }, null, NOW), [
    { k: "aws", v: "sandbox · 123456789012", wrap: true },
    { k: "role", v: "agentbox-agent", wrap: true },
    { k: "scope", v: "unverified (not checked)", wrap: true, title: "the base role's write access could not be checked" },
  ]);
  // no label: the account alone, not "aws · 1234"
  assert.strictEqual(awsRailRows({ aws: { label: "", account_id: "123456789012", role_arn: ARN } })[0].v, "123456789012");
  // prod is a row of its own, drawn as the red chip: it cannot be clipped by a long label
  const prod = awsRailRows({ aws: { label: "x".repeat(40), account_id: "123456789012", role_arn: ARN, prod: true } });
  assert.deepStrictEqual(prod[prod.length - 1], { k: "prod", v: "production", chip: "bdg aws prod" });
  assert.strictEqual(prod.filter((r) => r.k === "prod").length, 1);
});

test("awsBadge: write state, and prod keeps its red with the write outline", () => {
  assert.deepStrictEqual(awsBadge({ aws: { label: "sandbox", account_id: "123456789012", role_arn: ARN, prod: false } }, NOW),
    { text: "sandbox", prod: false, write: false, roleWrite: false, title: "AWS 123456789012 · agentbox-agent · base role unverified (not checked)" });
  const w = awsBadge({ aws: { ...ELEV, elevated_until: iso(17) } }, NOW);
  assert.strictEqual(w.write, true);
  assert.strictEqual(w.text, "sandbox · write");
  assert.match(w.title, /agentbox-agent-write · write until \d\d:\d\d$/);
  assert.strictEqual(awsBadge({ aws: { ...ELEV, elevated_until: iso(-1) } }, NOW).write, false); // a stale list entry is not write
  const pw = awsBadge({ aws: { ...ELEV, prod: true, elevated_until: iso(17) } }, NOW);
  assert.ok(pw.prod && pw.write);
  assert.strictEqual(awsBadge({ aws: { ...ELEV, label: "", elevated_until: iso(17) } }, NOW).text, "aws:210987654321 · write");
});

test("awsRailRows: scope row by state", () => {
  const scope = (aws, st) => awsRailRows({ aws }, st, NOW).find((r) => r.k === "scope");
  assert.deepStrictEqual(scope(ELEV, null), { k: "scope", v: "unverified (not checked)", wrap: true, title: "the base role's write access could not be checked" });
  const el = scope({ ...ELEV, elevated_until: iso(17) }, null);
  assert.match(el.v, /^write · until \d\d:\d\d \(17 min\)$/);
  assert.strictEqual(el.chip, "bdg aws write");
  assert.match(scope({ ...ELEV, elevated_until: iso(12) }, null).v, /\(12 min\) · new credentials from the base role$/);
  assert.doesNotMatch(scope({ ...ELEV, elevated_until: iso(16) }, null).v, /new credentials/); // 16 min still mints
  assert.match(scope({ ...ELEV, elevated_until: iso(15) }, null).v, /new credentials from the base role$/);
  assert.match(scope(ELEV, { elevation: { active: false, last_valid: iso(9) } }).v, /^unverified \(not checked\) · issued write creds valid until \d\d:\d\d$/);
  assert.deepStrictEqual(scope(ELEV, { elevation: { active: false, last_valid: iso(-1) } }), { k: "scope", v: "unverified (not checked)", wrap: true, title: "the base role's write access could not be checked" });
  const role = awsRailRows({ aws: { ...ELEV, elevated_until: iso(17) } }, null, NOW).find((r) => r.k === "role");
  assert.strictEqual(role.v, "agentbox-agent-write"); // while elevated, the rail names the role in use
  assert.strictEqual(awsRailRows({ aws: ELEV }, null, NOW).find((r) => r.k === "role").v, "agentbox-agent");
});

test("elevActions", () => {
  assert.deepStrictEqual(elevActions(ELEV, NOW), { elevate: true, extend: false, drop: false });
  assert.deepStrictEqual(elevActions({ ...ELEV, elevated_until: iso(25) }, NOW), { elevate: false, extend: false, drop: true });
  assert.deepStrictEqual(elevActions({ ...ELEV, elevated_until: iso(19) }, NOW), { elevate: false, extend: true, drop: true });
  assert.deepStrictEqual(elevActions({ ...ELEV, elevated_until: iso(20) }, NOW), { elevate: false, extend: false, drop: true });
  assert.deepStrictEqual(elevActions({ label: "x" }, NOW), { elevate: false, extend: false, drop: false });
  assert.deepStrictEqual(elevActions(undefined, NOW), { elevate: false, extend: false, drop: false });
});

test("rowClasses: an SSO need is a needs-you row", () => {
  assert.ok(rowClasses({ activity: "working", need: "sso" }, {}).includes("needs"));
  assert.ok(!rowClasses({ activity: "working" }, {}).includes("needs"));
});

test("badgeParts splits the write state from the label so only the label can be cut", () => {
  assert.strictEqual(badgeParts(null), null);
  assert.deepStrictEqual(badgeParts(awsBadge({ aws: { ...ELEV, elevated_until: iso(17) } }, NOW)), { label: "sandbox", write: true, roleWrite: false });
  assert.deepStrictEqual(badgeParts(awsBadge({ aws: { ...ELEV, label: "sandbox-prod", prod: true, elevated_until: iso(17) } }, NOW)), { label: "sandbox-prod", write: true, roleWrite: false });
  assert.deepStrictEqual(badgeParts(awsBadge({ aws: ELEV }, NOW)), { label: "sandbox", write: false, roleWrite: false });
  // A label that itself ends in " · write" is not a write state when the window is closed.
  assert.deepStrictEqual(badgeParts({ text: "x · write", write: false }), { label: "x · write", write: false, roleWrite: false });
});

test("awsSig moves on grant, drop and lapse, and on nothing else", () => {
  const base = { aws: ELEV };
  const s0 = awsSig(base, NOW);
  assert.strictEqual(awsSig({}, NOW), "");
  const granted = awsSig({ aws: { ...ELEV, elevated_until: iso(17) } }, NOW);
  assert.notStrictEqual(granted, s0, "a grant repaints the tab chip");
  assert.strictEqual(awsSig({ aws: { ...ELEV, elevated_until: iso(-1) } }, NOW), s0, "a lapsed window reads as dropped");
  assert.notStrictEqual(awsSig({ aws: { ...ELEV, prod: true } }, NOW), s0);
  assert.strictEqual(awsSig({ aws: { ...ELEV, role_arn: "other" } }, NOW), s0, "fields the chip does not draw");
});

// --- #155 named AWS connections ---------------------------------------------
const CARN = (n) => "arn:aws:iam::210987654321:role/agentbox-" + n;
const conn = (name, over) => Object.assign({ name: name, label: name.toUpperCase(), account_id: "210987654321", role_arn: CARN(name),
  region: "ap-southeast-1", prod: false, write_capable: false }, over);

test("awsConnRailRows: prod first, then by name; three lines per block", () => {
  assert.deepStrictEqual(awsConnRailRows({}, null, NOW, false), []);
  assert.deepStrictEqual(awsConnRailRows({ aws_connections: [] }, null, NOW, false), []);
  const s = { aws_connections: [conn("sbx-b"), conn("zz-prod", { prod: true }), conn("sbx-a")] };
  const rows = awsConnRailRows(s, null, NOW, false);
  assert.deepStrictEqual(rows.map((r) => r.name), ["zz-prod", "sbx-a", "sbx-b"]);
  assert.ok(rows.every((r) => r.kind === "conn"));
  assert.strictEqual(rows[1].detail, "SBX-A · 210987654321 · agentbox-sbx-a · ap-southeast-1");
  assert.deepStrictEqual(rows[0].chips, [{ text: "prod", chip: "bdg aws prod" }]);
  assert.deepStrictEqual(rows[1].chips, []);
  assert.strictEqual(rows[1].stat, "", "no counters loaded yet: no third line, not a made-up zero");
});

test("awsConnRailRows: write and SSO chips, and the write role while elevated", () => {
  const w = conn("sbx-a", { write_capable: true, elevated_until: iso(17), sso_expired: "corp",
    elevate: { role_arn: CARN("sbx-a-write"), max_minutes: 60 } });
  const r = awsConnRailRows({ aws_connections: [w] }, null, NOW, false)[0];
  assert.strictEqual(r.chips.length, 2);
  assert.match(r.chips[0].text, /^write until \d\d:\d\d$/);
  assert.strictEqual(r.chips[0].chip, "bdg aws write");
  assert.deepStrictEqual(r.chips[1], { text: "SSO expired", chip: "bdg dgr" });
  assert.match(r.detail, /agentbox-sbx-a-write/);
  // a lapsed window is not write
  const lapsed = awsConnRailRows({ aws_connections: [{ ...w, elevated_until: iso(-1), sso_expired: undefined }] }, null, NOW, false)[0];
  assert.deepStrictEqual(lapsed.chips, []);
  assert.match(lapsed.detail, /agentbox-sbx-a · /);
});

test("awsConnRailRows: line 3 is the counters, or the last error verbatim", () => {
  const s = { aws_connections: [conn("sbx-a"), conn("sbx-b")] };
  const st = { connections: [
    { name: "sbx-a", stats: { mints: 3, cache_hits: 1, last_mint: iso(-5) } },
    { name: "sbx-b", stats: { mints: 0, last_mint: "0001-01-01T00:00:00Z", last_error: "sso session expired — run aws sso login" } },
  ] };
  const [a, b] = awsConnRailRows(s, st, NOW, false);
  assert.match(a.stat, /^3 mints · last mint \d\d:\d\d · 5m ago$/, "the legacy row's clock-and-age text");
  assert.strictEqual(a.err, false);
  assert.strictEqual(b.stat, "sso session expired — run aws sso login");
  assert.strictEqual(b.err, true);
  const one = awsConnRailRows({ aws_connections: [conn("x")] }, { connections: [{ name: "x", stats: { mints: 1, last_mint: "0001-01-01T00:00:00Z" } }] }, NOW, false)[0];
  assert.strictEqual(one.stat, "1 mint · last mint never");
});

test("awsConnRailRows: six fit; seven collapse to six plus a show-all row; expanded shows all", () => {
  const mk = (n) => ({ aws_connections: Array.from({ length: n }, (_, i) => conn("c" + String(i).padStart(2, "0"))) });
  assert.strictEqual(awsConnRailRows(mk(6), null, NOW, false).length, 6);
  assert.ok(awsConnRailRows(mk(6), null, NOW, false).every((r) => r.kind === "conn"));
  const seven = awsConnRailRows(mk(7), null, NOW, false);
  assert.strictEqual(seven.length, 7);
  assert.deepStrictEqual(seven.slice(0, 6).map((r) => r.name), ["c00", "c01", "c02", "c03", "c04", "c05"]);
  assert.strictEqual(seven[6].kind, "more");
  assert.strictEqual(seven[6].n, 7, "show all N: N is the total");
  const open = awsConnRailRows(mk(7), null, NOW, true);
  assert.strictEqual(open.filter((r) => r.kind === "conn").length, 7);
  assert.strictEqual(open[open.length - 1].kind, "less");
});

test("awsConnRailRows rows carry a signature that moves with what they draw", () => {
  const s = { aws_connections: [conn("sbx-a", { write_capable: true })] };
  const v0 = awsConnRailRows(s, null, NOW, false)[0].v;
  const v1 = awsConnRailRows({ aws_connections: [{ ...s.aws_connections[0], elevated_until: iso(17) }] }, null, NOW, false)[0].v;
  assert.notStrictEqual(v0, v1);
  assert.strictEqual(awsConnRailRows(s, null, NOW, false)[0].k, "aws");
});

test("awsBadgeParts: one connection shows its label, several show AWS xN", () => {
  assert.strictEqual(awsBadgeParts({}, NOW), null);
  assert.strictEqual(awsBadgeParts({ aws_connections: [] }, NOW), null);
  const one = awsBadgeParts({ aws_connections: [conn("sbx-a", { label: "Sandbox" })] }, NOW);
  assert.strictEqual(one.label, "Sandbox");
  assert.deepStrictEqual([one.prod, one.write], [false, false]);
  assert.strictEqual(awsBadgeParts({ aws_connections: [conn("sbx-a", { label: "" })] }, NOW).label, "sbx-a", "no label: the name");
  const two = awsBadgeParts({ aws_connections: [conn("a"), conn("b")] }, NOW);
  assert.strictEqual(two.label, "AWS \u00d72");
  assert.match(two.title, /a, b/);
});

test("awsBadgeParts: red if any is prod, amber if any window is open", () => {
  const p = awsBadgeParts({ aws_connections: [conn("a"), conn("b", { prod: true })] }, NOW);
  assert.strictEqual(p.prod, true);
  const w = awsBadgeParts({ aws_connections: [conn("a", { elevated_until: iso(17) }), conn("b")] }, NOW);
  assert.strictEqual(w.write, true);
  assert.match(w.title, /write on a until \d\d:\d\d/);
  assert.strictEqual(awsBadgeParts({ aws_connections: [conn("a", { elevated_until: iso(-1) }), conn("b")] }, NOW).write, false);
});

test("awsBadge reads the connections too, in the shape the chip renderer already takes", () => {
  const ab = awsBadge({ aws_connections: [conn("a"), conn("b", { elevated_until: iso(17) })] }, NOW);
  assert.strictEqual(ab.text, "AWS \u00d72 \u00b7 write");
  assert.strictEqual(ab.write, true);
  assert.deepStrictEqual(badgeParts(ab), { label: "AWS \u00d72", write: true, roleWrite: false });
  assert.strictEqual(awsBadge({ aws_connections: [conn("a", { label: "Sandbox" })] }, NOW).text, "Sandbox");
  // the legacy binding still wins nothing: a session never carries both
  assert.strictEqual(awsBadge({ aws: ELEV }, NOW).text, "sandbox");
});

test("awsSig covers connections: moves on a grant, a drop and prod, not on unrendered fields", () => {
  const base = { aws_connections: [conn("a"), conn("b")] };
  const s0 = awsSig(base, NOW);
  assert.notStrictEqual(s0, "");
  assert.notStrictEqual(awsSig({ aws_connections: [conn("a", { elevated_until: iso(17) }), conn("b")] }, NOW), s0);
  assert.strictEqual(awsSig({ aws_connections: [conn("a", { elevated_until: iso(-1) }), conn("b")] }, NOW), s0);
  assert.notStrictEqual(awsSig({ aws_connections: [conn("a", { prod: true }), conn("b")] }, NOW), s0);
  assert.strictEqual(awsSig({ aws_connections: [conn("a", { region: "eu-west-1" }), conn("b")] }, NOW), s0);
});

// --- #155 fix round 1: a connection's window counts down like the legacy scope row ---
test("connWindowLine: the open window reads as the legacy scope row does", () => {
  const conns = [conn("a", { elevated_until: iso(17) }), conn("b")];
  const w = connWindowLine(conns, null, NOW);
  assert.strictEqual(w.name, "a");
  assert.match(w.text, /^write \u00b7 until \d\d:\d\d \(17 min\)$/);
  assert.match(connWindowLine([conn("a", { elevated_until: iso(12) })], null, NOW).text, /\(12 min\) \u00b7 new credentials from the base role$/);
  assert.doesNotMatch(connWindowLine([conn("a", { elevated_until: iso(16) })], null, NOW).text, /new credentials/);
  assert.strictEqual(connWindowLine([conn("a", { elevated_until: iso(-1) }), conn("b")], null, NOW), null, "a lapsed window says nothing");
  assert.strictEqual(connWindowLine([conn("a")], null, NOW), null);
});

test("connWindowLine: a status-only window, and the issued credentials after a drop", () => {
  const conns = [conn("a"), conn("b")];
  const live = connWindowLine(conns, { elevation: { active: true, name: "b", until: iso(30) } }, NOW);
  assert.strictEqual(live.name, "b");
  assert.match(live.text, /^write \u00b7 until \d\d:\d\d \(30 min\)$/);
  assert.strictEqual(connWindowLine(conns, { elevation: { active: true, name: "b", until: iso(-2) } }, NOW), null);
  const left = connWindowLine(conns, { elevation: { active: false, name: "a", last_valid: iso(9) } }, NOW);
  assert.strictEqual(left.name, "a");
  assert.match(left.text, /^unverified \(not checked\) \u00b7 issued write creds valid until \d\d:\d\d$/);
  assert.strictEqual(connWindowLine(conns, { elevation: { active: false, name: "a", last_valid: iso(-1) } }, NOW), null);
  assert.deepStrictEqual(liveWindowConn(conns, { elevation: { active: true, name: "b", until: iso(30) } }, NOW), { name: "b", until: iso(30) });
  assert.strictEqual(liveWindowConn(conns, null, NOW), null);
});

// Kill-switch: the reply menu items close unless globalThis.repliesEnabled is the boolean true.
for (const off of [undefined, "true"]) {
  test("menu and palette offer no reply actions when repliesEnabled is " + String(off), () => {
    if (off === undefined) delete globalThis.repliesEnabled; else globalThis.repliesEnabled = off;
    try {
      for (const s of [{ claude: true }, { claude: true, replyable: true }]) {
        assert.ok(!menuItems(s).some((i) => i.id.startsWith("replies-")));
        assert.ok(!paletteActionItems(s).some((i) => i.id.startsWith("replies-")));
      }
      // the rest of the menu is untouched
      assert.ok(menuItems({ claude: true }).some((i) => i.id === "duplicate"));
      assert.ok(paletteActionItems({ claude: true }).some((i) => i.id === "rename"));
    } finally {
      globalThis.repliesEnabled = true;
    }
  });
}

// --- base-role write probe: the UI shows the measured base-role access ---
const CHK = "2027-01-15T09:30:00Z";
const hm = (iso) => { const d = new Date(iso); return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0"); };
const WRITES = { base_access: "writes", base_write_actions: ["s3:PutObject", "iam:CreateUser"], base_checked_at: CHK };
const NONE = { base_access: "none_found", base_checked_at: CHK };
const UNV = (r) => ({ base_access: "unverified", base_access_reason: r });

test("baseScope: three states and fallbacks", () => {
  assert.deepStrictEqual(baseScope(WRITES), { state: "writes", text: "role can write: s3:PutObject, iam:CreateUser",
    title: "checked " + hm(CHK) + " \u00b7 IAM policy simulation" });
  assert.deepStrictEqual(baseScope(NONE), { state: "none_found", text: "no broad writes found",
    title: "checked " + hm(CHK) + " \u00b7 scoped write grants are not detected" });
  const u = baseScope(UNV("sso_expired"));
  assert.deepStrictEqual([u.state, u.text, u.title], ["unverified", "unverified (SSO expired)", "the base role's write access could not be checked"]);
  assert.strictEqual(baseScope({}).text, "unverified (not checked)");
  assert.strictEqual(baseScope({}).state, "unverified");
  assert.strictEqual(baseScope(undefined).text, "unverified (not checked)");
  assert.strictEqual(baseScope(UNV("bogus")).text, "unverified (check failed)");
  assert.strictEqual(baseScope({ base_access: "bogus", base_access_reason: "timeout" }).text, "unverified (not checked)", "an unknown state is never none_found");
  assert.strictEqual(baseScope({ base_access: "writes" }).text, "role can write");
  assert.strictEqual(baseScope({ base_access: "writes" }).title, "IAM policy simulation", "no check time: no 'checked' prefix");
  assert.strictEqual(baseScope({ base_access: "none_found" }).title, "scoped write grants are not detected");
  assert.strictEqual(baseScope({ base_access: "writes", base_write_actions: [] }).text, "role can write");
  assert.strictEqual(baseReasonText("timeout"), "timed out");
  assert.strictEqual(baseReasonText("toString"), "check failed");
  assert.strictEqual(Object.keys(BASE_REASON).length, 7);
  assert.strictEqual(ROLE_WRITE_SUFFIX, " \u00b7 can write");
});

test("scopeRow: base states", () => {
  const scope = (aws, st) => awsRailRows({ aws: { ...ELEV, ...aws } }, st, NOW).find((r) => r.k === "scope");
  assert.deepStrictEqual(scope({ ...WRITES, base_write_actions: ["s3:PutObject"] }, null),
    { k: "scope", v: "role can write: s3:PutObject", chip: "bdg aws rolew", wrap: true, title: "checked " + hm(CHK) + " \u00b7 IAM policy simulation" });
  assert.deepStrictEqual(scope(NONE, null),
    { k: "scope", v: "no broad writes found", title: "checked " + hm(CHK) + " \u00b7 scoped write grants are not detected" });
  assert.deepStrictEqual(scope(UNV("timeout"), null),
    { k: "scope", v: "unverified (timed out)", wrap: true, title: "the base role's write access could not be checked" });
});

test("scopeRow: elevated copy wins, whatever the base state", () => {
  const scope = (aws, st) => awsRailRows({ aws: { ...ELEV, ...aws } }, st, NOW).find((r) => r.k === "scope");
  for (const b of [WRITES, NONE, UNV("timeout"), {}]) {
    assert.match(scope({ ...b, elevated_until: iso(17) }, null).v, /^write \u00b7 until \d\d:\d\d \(17 min\)$/);
    const low = scope({ ...b, elevated_until: iso(12) }, null);
    assert.match(low.v, /^write \u00b7 until /);
    assert.match(low.v, /\u00b7 new credentials from the base role$/);
    assert.strictEqual(low.chip, "bdg aws write");
  }
  const after = scope(NONE, { elevation: { active: false, last_valid: iso(9) } });
  assert.match(after.v, /^no broad writes found \u00b7 issued write creds valid until \d\d:\d\d$/);
  assert.strictEqual(scope(WRITES, { elevation: { active: false, last_valid: iso(9) } }).v.startsWith("role can write: s3:PutObject, iam:CreateUser \u00b7 issued write creds"), true);
});

test("awsBadge: base role can write", () => {
  const w = awsBadge({ aws: { ...ELEV, ...WRITES, base_write_actions: ["s3:PutObject"] } }, NOW);
  assert.strictEqual(w.text, "sandbox \u00b7 can write");
  assert.strictEqual(w.roleWrite, true);
  assert.strictEqual(w.write, false);
  assert.match(w.title, / \u00b7 role can write: s3:PutObject$/);
  const open = awsBadge({ aws: { ...ELEV, ...WRITES, elevated_until: iso(17) } }, NOW);
  assert.strictEqual(open.write, true);
  assert.strictEqual(open.roleWrite, false);
  assert.match(open.text, / \u00b7 write$/);
  assert.match(awsBadge({ aws: { ...ELEV, ...NONE } }, NOW).title, / \u00b7 no broad writes found \(scoped write grants are not detected\)$/);
  assert.match(awsBadge({ aws: { ...ELEV, ...UNV("sso_expired") } }, NOW).title, / \u00b7 base role unverified \(SSO expired\)$/);
  assert.strictEqual(awsBadge({ aws: { ...ELEV, ...NONE } }, NOW).roleWrite, false);
});

test("awsBadgeParts: one and many connections", () => {
  const one = awsBadgeParts({ aws_connections: [conn("a", WRITES)] }, NOW);
  assert.strictEqual(one.roleWrite, true);
  assert.match(one.title, / \u00b7 role can write: s3:PutObject, iam:CreateUser$/);
  assert.strictEqual(awsBadgeParts({ aws_connections: [conn("a", NONE)] }, NOW).roleWrite, false);
  const many = awsBadgeParts({ aws_connections: [conn("a", WRITES), conn("c", UNV("timeout")), conn("b", NONE)] }, NOW);
  assert.strictEqual(many.roleWrite, true);
  assert.ok(many.title.includes(" \u00b7 role can write on a"), many.title);
  assert.ok(many.title.includes(" \u00b7 unverified on c"), many.title);
  assert.ok(!many.title.includes("on b"), many.title);
  const live = awsBadgeParts({ aws_connections: [conn("a", { ...WRITES, elevated_until: iso(17) }), conn("b", WRITES)] }, NOW);
  assert.strictEqual(live.roleWrite, false, "an open window wins");
  assert.strictEqual(live.write, true);
  assert.strictEqual(awsBadgeParts({ aws_connections: [conn("a", { ...WRITES, elevated_until: iso(17) })] }, NOW).roleWrite, false);
});

test("badgeParts: role-write suffix never ellipsizes", () => {
  const ab = awsBadge({ aws: { ...ELEV, ...WRITES } }, NOW);
  assert.deepStrictEqual(badgeParts(ab), { label: "sandbox", write: false, roleWrite: true });
  const many = awsBadge({ aws_connections: [conn("a", WRITES), conn("b")] }, NOW);
  assert.deepStrictEqual(badgeParts(many), { label: "AWS \u00d72", write: false, roleWrite: true });
});

test("awsConnRailRows: base line", () => {
  const rows = (c, st) => awsConnRailRows({ aws_connections: [c] }, st || null, NOW, false)[0];
  const w = rows(conn("a", { ...WRITES, base_write_actions: ["s3:PutObject"] }));
  assert.strictEqual(w.base, "role can write: s3:PutObject");
  assert.strictEqual(w.baseWarn, true);
  assert.ok(w.v.includes(w.base));
  const n = rows(conn("a", NONE));
  assert.strictEqual(n.baseWarn, false);
  assert.strictEqual(n.baseTitle, "checked " + hm(CHK) + " \u00b7 scoped write grants are not detected");
  assert.strictEqual(n.base, "no broad writes found");
  const u = rows(conn("a", UNV("assume_denied")));
  assert.deepStrictEqual([u.base, u.baseWarn], ["unverified (assume-role denied)", false]);
  const live = rows(conn("a", { ...WRITES, elevated_until: iso(17) }));
  assert.strictEqual(live.base, "");
  assert.strictEqual(live.baseWarn, false);
  assert.notStrictEqual(w.v, n.v, "the repaint key moves with the base state");
});

test("connWindowLine after a drop uses the connection's base", () => {
  const conns = [conn("a", NONE), conn("b", WRITES)];
  const st = { elevation: { active: false, name: "a", last_valid: iso(9) } };
  assert.match(connWindowLine(conns, st, NOW).text, /^no broad writes found \u00b7 issued write creds valid until \d\d:\d\d$/);
  const stb = { elevation: { active: false, name: "b", last_valid: iso(9) } };
  assert.match(connWindowLine(conns, stb, NOW).text, /^role can write: s3:PutObject, iam:CreateUser \u00b7 issued write creds/);
});

test("awsSig moves with the base-role result", () => {
  for (const mk of [(o) => ({ aws: { ...ELEV, ...o } }), (o) => ({ aws_connections: [conn("a", o)] })]) {
    const none = awsSig(mk(NONE), NOW);
    const wr = awsSig(mk(WRITES), NOW);
    const r1 = awsSig(mk(UNV("timeout")), NOW);
    const r2 = awsSig(mk(UNV("sso_expired")), NOW);
    assert.strictEqual(new Set([none, wr, r1, r2]).size, 4);
    assert.notStrictEqual(awsSig(mk({ ...WRITES, base_write_actions: ["s3:PutObject"] }), NOW), wr, "the actions move it");
    assert.strictEqual(awsSig(mk({ ...NONE, base_checked_at: "2027-01-15T10:00:00Z" }), NOW), none, "a re-check with the same result does not rebuild the tab");
  }
});

test("no unqualified read-only", () => {
  const states = [WRITES, NONE, UNV("timeout"), UNV("bogus"), {}];
  const texts = [];
  for (const b of states) {
    for (const extra of [{}, { elevated_until: iso(17) }, { elevated_until: iso(12) }]) {
      const s = { aws: { ...ELEV, ...b, ...extra } };
      texts.push(awsBadge(s, NOW).title);
      for (const r of awsRailRows(s, null, NOW)) texts.push(String(r.v), String(r.title || ""));
      texts.push(awsRailRows(s, { elevation: { active: false, last_valid: iso(9) } }, NOW).map((r) => r.v).join(" "));
      const cs = { aws_connections: [conn("a", { ...b, ...extra }), conn("b", b)] };
      texts.push(awsBadge(cs, NOW).title);
      for (const r of awsConnRailRows(cs, null, NOW, false)) texts.push(r.v, r.base || "", r.baseTitle || "");
      texts.push(connWindowLine(cs.aws_connections, { elevation: { active: false, name: "b", last_valid: iso(9) } }, NOW).text);
    }
  }
  for (const t of texts) assert.doesNotMatch(t, /read-only/i, t);
});

// --- sso_direct: rail, chip and signature key on mode, never on base_access ----

const SSOA = { label: "sbx", account_id: "123456789012", role_arn: "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_RO_abc", mode: "sso_direct", hours: 8, base_access: "none_found" };

test("sso_direct: the scope row ends with the mode label and never says no broad writes found", () => {
  const scope = awsRailRows({ aws: SSOA }, null, NOW).find((r) => r.k === "scope");
  assert.strictEqual(scope.v, "read-only gate passed · up to 8 h · SSO role directly");
  assert.ok(scope.v.endsWith("· SSO role directly"));
  assert.doesNotMatch(JSON.stringify(awsRailRows({ aws: SSOA }, null, NOW)), /no broad writes found/);
  assert.strictEqual(awsRailRows({ aws: { ...SSOA, hours: 0 } }, null, NOW).find((r) => r.k === "scope").v, "read-only gate passed · SSO role directly");
});

test("sso_direct: the connection block and the chip title carry the mode, not the AssumeRole wording", () => {
  const b = awsConnRailRows({ aws_connections: [{ name: "sbx", ...SSOA }] }, null, NOW)[0];
  assert.ok(b.base.endsWith("· SSO role directly"));
  assert.strictEqual(b.baseWarn, false);
  const chip = awsBadge({ aws: SSOA }, NOW);
  assert.match(chip.title, /SSO role directly/);
  assert.doesNotMatch(chip.title, /no broad writes found|scoped write grants/);
});

test("sso_direct: awsSig changes with the mode and the hours", () => {
  const a = { label: "sbx", account_id: "1", role_arn: "arn:aws:iam::1:role/x", base_access: "none_found" };
  assert.notStrictEqual(awsSig({ aws: a }, NOW), awsSig({ aws: { ...a, mode: "sso_direct" } }, NOW));
  assert.notStrictEqual(awsSig({ aws: { ...a, mode: "sso_direct", hours: 8 } }, NOW), awsSig({ aws: { ...a, mode: "sso_direct", hours: 4 } }, NOW));
  const cs = (c) => ({ aws_connections: [{ name: "n", ...a, ...c }] });
  assert.notStrictEqual(awsSig(cs({}), NOW), awsSig(cs({ mode: "sso_direct" }), NOW));
});

test("baseScope: sso_direct is its own state", () => {
  assert.strictEqual(baseScope(SSOA).state, "sso_direct");
});

test("sso_direct with no role name: the rail, chip title and connection detail show the mode, not an empty role", () => {
  const A = { label: "sbx", account_id: "123456789012", role_arn: "", mode: "sso_direct", hours: 8 };
  const role = awsRailRows({ aws: A }, null, NOW).find((r) => r.k === "role");
  assert.strictEqual(role.v, "SSO role directly");
  const chip = awsBadge({ aws: A }, NOW);
  assert.strictEqual(chip.title, "AWS 123456789012 · read-only gate passed · up to 8 h · SSO role directly");
  assert.doesNotMatch(chip.title, /·\s+·/);
  const one = awsBadge({ aws_connections: [{ name: "sbx", ...A }] }, NOW);
  assert.doesNotMatch(one.title, /·\s+·/);
  const b = awsConnRailRows({ aws_connections: [{ name: "sbx", ...A, region: "eu-west-1" }] }, null, NOW)[0];
  assert.strictEqual(b.detail, "sbx · 123456789012 · SSO role directly · eu-west-1");
  // an assume_role binding keeps its role name
  const plain = awsRailRows({ aws: { label: "x", account_id: "1", role_arn: "arn:aws:iam::1:role/ops" } }, null, NOW).find((r) => r.k === "role");
  assert.strictEqual(plain.v, "ops");
});

test("menuItems offers Bring work home now for a boxed clone session, before duplicate", () => {
  const ids = (s, o) => menuItems(s, o).map((i) => i.id);
  const clone = menuItems({ clone_of: "/r", runtime: "box" });
  const it = clone.find((i) => i.id === "bringhome");
  assert.deepStrictEqual(it, { id: "bringhome", label: "Bring work home now", danger: false });
  assert.ok(ids({ clone_of: "/r", runtime: "box" }).indexOf("bringhome") < ids({ clone_of: "/r", runtime: "box" }).indexOf("duplicate"));
  assert.ok(!ids({ clone_of: "/r", runtime: "host" }).includes("bringhome"), "host");
  assert.ok(!ids({ runtime: "box" }).includes("bringhome"), "not a clone");
  assert.ok(!ids({ clone_of: "/r", runtime: "box" }, { nested: true }).includes("bringhome"), "nested shell");
  assert.ok(!paletteActionItems({ clone_of: "/r", runtime: "box" }).some((i) => i.id === "bringhome"), "not a palette action");
});

test("actionFailText names Bring work home for its own failures", () => {
  assert.strictEqual(actionFailText("bringhome", 500), "Bring work home failed (500).");
  assert.strictEqual(actionFailText("bringhome", 0), "No connection — try again.");
});
