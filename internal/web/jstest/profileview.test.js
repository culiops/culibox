"use strict";
// profileview.js: the profile page's read view as data (spec 5.2-5.4, 5.6). Pure:
// every wording of Cloud, Also reachable and Network comes from reachModel, and
// the rest is the approved mockup's (profile-mockups/shots/b-*.png).
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { pfvReadView, pfvAdvancedCount, pfvAdvancedItems, pfvPreselectWord } = require("../static/profileview.js");
const { reachModel } = require("../static/spawnpreview.js");
const { notHomeReasonText, notHomeFix, notHomeLabel, notHomeTitle } = require("../static/workhome.js");

// The BRIEF's aws-ops profile, as the list view (ProfileView) and the stored profile carry it.
const awsOpsView = {
  name: "aws-ops", editable: true, source: "runtime", runtime: "box", bin: "claude", claude: true,
  default_cwd: "/home/you/work/payments-api",
  aws_connections: ["prod-read", "staging-read"],
  github_owners: ["culiops"],
  github_repos: [{ owner: "culiops", repos: ["payments-api"] }],
  github_permissions: { contents: "read", pull_requests: "read", issues: "read", metadata: "read" },
  tokens: ["grafana-prod", "sentry-staging"],
  context_paths: ["/home/you/other-repo/docs"],
  egress_allow: ["api.example.com", "*.example.org"],
  network: "allowlist",
};
const awsOpsDoc = {
  name: "aws-ops", bin: "claude", runtime: "box", default_cwd: "/home/you/work/payments-api",
  options: { model: "claude-opus-5-5", effort: "high" },
  aws_connections: ["prod-read", "staging-read"], tokens: ["grafana-prod", "sentry-staging"],
  context_paths: ["/home/you/other-repo/docs"], egress_allow: ["api.example.com", "*.example.org"], rev: "r1",
};
const meta = {
  aws_connections: [
    { name: "prod-read", prod: true, write_capable: true, account_id: "222222222222", region: "eu-west-1" },
    { name: "staging-read", prod: false, write_capable: false, account_id: "111111111111", region: "eu-west-1" },
  ],
  static_tokens: [
    { name: "grafana-prod", prod: true, description: "Grafana service account", env: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"], hosts: [{ host: "grafana.acme.example.com", port: 443 }] },
    { name: "sentry-staging", description: "", env: ["SENTRY_ACCESS_TOKEN"], hosts: [{ host: "sentry.io", port: 443 }] },
  ],
};
const creds = { store: { initialized: true, locked: false, platform: "linux" }, aws: [{ name: "prod-read", prod: true, elevate: { max_minutes: 30 } }, { name: "staging-read" }] };
const defs = [
  { key: "model", label: "Model", type: "enum", choices: ["claude-opus-5-5"] },
  { key: "effort", label: "Effort", type: "enum", choices: ["low", "high"] },
  { key: "dangerously", label: "Skip permission checks", type: "bool" },
];
const awsIn = (over) => Object.assign({ view: awsOpsView, doc: awsOpsDoc, meta, creds, boxRuntime: "runc", boxUnavailableReason: "", bundleImports: true, defs }, over);
const byId = (v, id) => v.blocks.find((b) => b.id === id);
const ids = (v) => v.blocks.map((b) => b.id);
const pickRow = (r) => ({ kind: r.kind, name: r.name, sub: r.sub, scope: r.scope });

const hostView = { name: "runbook-host", editable: true, source: "runtime", runtime: "host", bin: "claude", claude: true, default_cwd: "/home/you/runbooks" };
const hostDoc = { name: "runbook-host", bin: "claude", runtime: "host", default_cwd: "/home/you/runbooks", rev: "r2" };
const hostIn = (over) => Object.assign({ view: hostView, doc: hostDoc, meta, creds, boxRuntime: "runc", bundleImports: true, defs }, over);

test("aws-ops read view has the eight blocks in order", () => {
  const v = pfvReadView(awsIn());
  assert.deepStrictEqual(ids(v), ["runs", "workspace", "agent", "cloud", "reachable", "network", "canreach", "advanced"]);
  assert.deepStrictEqual(v.blocks.map((b) => b.label), ["Runs in", "Workspace", "Agent", "Cloud and tokens", "Also reachable", "Network", "Can reach", "Advanced"]);
  assert.deepStrictEqual(v.header, { name: "aws-ops", tier: "box", agentLine: "Claude Code · claude-opus-5-5 · high effort", readOnly: false, source: "runtime" });
  assert.strictEqual(v.note, "");
  assert.ok(v.blocks.every((b) => b.edit === (b.id !== "canreach")), "every block of an editable profile has Edit but Can reach, whose action is Switch to box");

  assert.deepStrictEqual(byId(v, "runs").lines, ["In a box", "A container on this machine. It can write only to its workspace and reach only what this page lists.", "New session pre-selects: Box · in place"]);
  assert.ok(!byId(v, "runs").tone);

  const ws = byId(v, "workspace");
  assert.deepStrictEqual(ws.rows.map((r) => [r.kind, r.name, r.scope]), [["Folder", "/home/you/work/payments-api", "Read and write"]]);
  assert.strictEqual(ws.rows[0].sub, "Edits the folder directly, not a private copy", "the words b-aws-1440-full.png shows");

  assert.deepStrictEqual(byId(v, "agent").lines, ["Claude Code with claude-opus-5-5, high effort", "Signs in with the default Claude login (~/.claude)"]);
  assert.deepStrictEqual(byId(v, "advanced").lines.length, 1);
  assert.strictEqual(byId(v, "advanced").count, 0);
  assert.strictEqual(byId(v, "advanced").open, false);
  assert.strictEqual(byId(v, "advanced").status, "none set");
});

test("the Cloud, Also reachable and Network values are reachModel's, unchanged", () => {
  const v = pfvReadView(awsIn());
  const m = reachModel({ profile: awsOpsView, tier: "box", cwd: awsOpsView.default_cwd, meta, creds, bundleImports: true });
  const strip = (it) => ({ kind: it.kind, name: it.name, sub: it.sub, scope: it.scope, tag: it.tag, missing: it.missing, applies: "" });
  assert.deepStrictEqual(byId(v, "cloud").rows, m.cloud.map(strip));
  assert.deepStrictEqual(byId(v, "reachable").rows, m.reachable.map(strip));
  assert.deepStrictEqual(byId(v, "network").lines, [m.network.line, m.network.sub]);
  assert.strictEqual(byId(v, "cloud").note, "Long-lived; the agent can read it while it runs");
  assert.strictEqual(byId(v, "cloud").add, "+ Add access");
  assert.strictEqual(byId(v, "reachable").add, "+ Add folder or domain");
  assert.deepStrictEqual(byId(v, "cloud").rows.map((r) => [r.kind, r.name, r.tag]), [
    ["AWS", "prod-read", "PROD"], ["AWS", "staging-read", ""], ["GitHub", "culiops", ""], ["Token", "grafana-prod", "production"], ["Token", "sentry-staging", ""],
  ]);
  assert.strictEqual(byId(v, "cloud").count, 5);
  assert.strictEqual(byId(v, "reachable").count, 4);
  assert.ok(!/"facts"/.test(JSON.stringify(v)), "reachModel's internal facts do not leave the read model");
});

test("a profile with nothing bound still lists Cloud and Also reachable, empty, with their add action", () => {
  const view = { name: "payments-readonly", editable: true, source: "runtime", runtime: "box", bin: "claude", claude: true, default_cwd: "/home/you/work/payments-api", network: "open" };
  const v = pfvReadView({ view, doc: { name: "payments-readonly", bin: "claude", default_cwd: view.default_cwd }, meta, creds, boxRuntime: "runc", bundleImports: true, defs });
  assert.deepStrictEqual(ids(v), ["runs", "workspace", "agent", "cloud", "reachable", "network", "canreach", "advanced"]);
  assert.deepStrictEqual(byId(v, "cloud").rows, []);
  assert.strictEqual(byId(v, "cloud").add, "+ Add access");
  assert.deepStrictEqual(byId(v, "cloud").lines, ["Nothing yet", "It cannot use AWS, GitHub or any token."], "the value, then the helper (frame M2.1): two lines");
  assert.strictEqual(byId(v, "cloud").note, "");
  // Nothing bound: the words b-created-1440.png shows, not the allowlist sentence.
  assert.deepStrictEqual(byId(v, "network").lines, [
    "Any public website over HTTPS, through CuliBox's proxy",
    "An allowlist starts once it holds cloud access or a token. Private, tailnet and host addresses stay blocked.",
  ]);
  assert.deepStrictEqual(byId(v, "reachable").rows.map((r) => r.name), ["CLAUDE.md imports"]);
  assert.strictEqual(byId(v, "reachable").add, "+ Add folder or domain");
  assert.strictEqual(v.header.agentLine, "Claude Code · agent's default model");
  assert.deepStrictEqual(byId(v, "agent").lines, ["Claude Code with its default model", "Signs in with the default Claude login (~/.claude)"]);
  // Even with bundling off there is nothing to list, and the block still says so and offers the add.
  const bare = pfvReadView({ view, doc: {}, meta, creds, boxRuntime: "runc", bundleImports: false });
  assert.deepStrictEqual(byId(bare, "reachable").rows, []);
  assert.strictEqual(byId(bare, "reachable").add, "+ Add folder or domain");
  assert.deepStrictEqual(byId(bare, "reachable").lines, ["Nothing yet", "It cannot read other folders or reach other domains."], "the value, then the helper");
});

test("runbook-host read view has every block, Can reach last before Advanced", () => {
  const v = pfvReadView(hostIn());
  assert.deepStrictEqual(ids(v), ["runs", "workspace", "agent", "cloud", "reachable", "network", "canreach", "advanced"]);
  assert.strictEqual(v.header.tier, "host");
  assert.strictEqual(v.header.agentLine, "Claude Code · agent's default model");
  // The spec's sentence verbatim, on one line; the muted line is empty when boxes are available.
  assert.deepStrictEqual(byId(v, "runs").lines, ["On this machine: runs as you, with your files, your network and your credentials", "", "New session pre-selects: Host"]);
  assert.ok(!byId(v, "runs").tone);
  assert.deepStrictEqual(byId(v, "workspace").rows.map((r) => [r.kind, r.name, r.sub, r.scope]), [
    ["Folder", "/home/you/runbooks", "Sessions start here", "Read and write, like anything else you own"],
  ]);
  const cr = byId(v, "canreach");
  assert.strictEqual(cr.label, "Can reach");
  assert.deepStrictEqual(cr.lines, [
    "Everything you can",
    "Your whole home, your SSH keys, the Docker socket and any website.",
    "Scoped AWS, GitHub, tokens and network limits need a box; CuliBox cannot scope them on this machine.",
  ]);
  assert.strictEqual(cr.switchToBox, true);
  assert.strictEqual(cr.edit, false, "the block has no Edit: its action is Switch to box");
  assert.deepStrictEqual(cr.rows, []);
  assert.deepStrictEqual(byId(v, "agent").lines, ["Claude Code with its default model", "Signs in with the default Claude login (~/.claude)"]);
  assert.match(byId(v, "advanced").lines[0], /^Starting environment, arguments, environment variables, sidebar replies and CLAUDE\.md imports\./);
  assert.ok(byId(v, "advanced").lines[0].endsWith("All at their defaults."));
});

test("a host profile with no folder says so; a shell shows its binary, not a model", () => {
  const v = pfvReadView(hostIn({ view: { name: "claude", editable: false, source: "config", runtime: "host", bin: "claude", claude: true }, doc: { name: "claude", bin: "claude", options: { effort: "high" } } }));
  assert.deepStrictEqual(byId(v, "workspace").rows.map((r) => [r.name, r.sub, r.scope]), [["No default folder", "Picked in New session", "Read and write, like anything else you own"]]);
  assert.strictEqual(v.header.agentLine, "Claude Code · agent's default model · high effort");
  assert.deepStrictEqual(byId(v, "agent").lines, ["Claude Code with its default model, high effort", "Signs in with the default Claude login (~/.claude)"]);

  const sh = pfvReadView(hostIn({ view: { name: "scratch-bash", editable: true, source: "runtime", runtime: "host", bin: "/bin/bash", claude: false }, doc: { name: "scratch-bash", bin: "/bin/bash" } }));
  assert.strictEqual(sh.header.agentLine, "bash");
  assert.deepStrictEqual(byId(sh, "agent").lines, ["bash"]);
});

test("config.json profile is read-only with the note and no edit", () => {
  const view = { ...hostView, name: "claude", editable: false, source: "config", default_cwd: undefined };
  const v = pfvReadView(hostIn({ view, doc: { name: "claude", bin: "claude", options: { effort: "high" } } }));
  assert.strictEqual(v.note, "Defined in config.json. Duplicate it to change it.");
  assert.strictEqual(v.header.readOnly, true);
  assert.strictEqual(v.header.source, "config");
  assert.ok(v.blocks.every((b) => b.edit === false), "no Edit anywhere");
  assert.ok(v.blocks.every((b) => !b.add && !b.switchToBox), "no add action and no Switch to box either");
  // A box profile defined in config.json has no add actions either.
  const box = pfvReadView(awsIn({ view: { ...awsOpsView, editable: false, source: "config" } }));
  assert.ok(box.blocks.every((b) => b.edit === false && !b.add));
  assert.strictEqual(byId(box, "cloud").rows.length, 5, "the rows are still listed");
  // The seed that exists only in code gets the same line (the lock bar no longer says "Built-in").
  const seed = pfvReadView(hostIn({ view: { ...hostView, editable: false, source: "builtin" } }));
  assert.strictEqual(seed.note, "Defined in config.json. Duplicate it to change it.");
  assert.ok(seed.blocks.every((b) => b.edit === false));
});

test("boxes unavailable on a box profile is an error line with the reason", () => {
  const v = pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed" }));
  const runs = byId(v, "runs");
  assert.deepStrictEqual(runs.lines, [
    "In a box",
    "Boxes are unavailable on this machine: Docker is not installed.",
    "Sessions with this profile can't start until Docker is installed.",
    "New session pre-selects: Box · in place",
  ]);
  assert.strictEqual(runs.tone, "error");
  assert.strictEqual(runs.toneAt, 1, "only the sentence that names the problem is red");
  assert.strictEqual(v.header.tier, "box", "the profile still says box; it is what will refuse to start");
  assert.deepStrictEqual(ids(v), ["runs", "workspace", "agent", "cloud", "reachable", "network", "canreach", "advanced"]);
  // On a box with no layer the list view reports no network posture: the block still reads from the builder.
  const noLayer = pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed", view: { ...awsOpsView, network: "" } }));
  assert.deepStrictEqual(byId(noLayer, "network").lines, byId(v, "network").lines);

  const colima = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "colima is not installed" })), "runs");
  assert.strictEqual(colima.lines[1], "Boxes are unavailable on this machine: colima is not installed.");
  assert.strictEqual(colima.lines[2], "Sessions with this profile can't start until colima is installed.");

  const doctor = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "run `culibox doctor` for details" })), "runs");
  assert.strictEqual(doctor.lines[1], "Boxes are unavailable on this machine: run `culibox doctor` for details.");
  assert.strictEqual(doctor.lines[2], "Sessions with this profile can't start until boxes are available.");
  assert.strictEqual(doctor.tone, "error");

  // An older server sends no reason: the sentence still stands, without a clause after it.
  const none = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "" })), "runs");
  assert.strictEqual(none.lines[1], "Boxes are unavailable on this machine.");
});

// A profile that stores no tier only RESOLVES to box. With no box layer a session that names no tier runs on this machine, uncontained
// (internal/session runtimeFor), so "can't start" would be false for it; the page says what really happens.
test("boxes unavailable on a profile that names no tier says its sessions start on this machine", () => {
  // The stored profile names no tier; the list row's runtime is the one it resolves to (box). It holds no readable folder and no
  // private copy: a session that asks for no tier and needs neither runs on this machine (the next test is the ones that are refused).
  const unnamedDoc = { ...awsOpsDoc };
  delete unnamedDoc.runtime;
  delete unnamedDoc.context_paths;
  const unnamedView = { ...awsOpsView };
  delete unnamedView.context_paths;
  const noTier = (reason) => byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: reason, view: unnamedView, doc: unnamedDoc })), "runs");
  const docker = noTier("Docker is not installed");
  assert.deepStrictEqual(docker.lines, [
    "In a box",
    "Boxes are unavailable on this machine: Docker is not installed.",
    "Until it is, sessions start on this machine, uncontained.",
    "New session pre-selects: Box · in place",
  ]);
  assert.strictEqual(docker.tone, "error");
  assert.strictEqual(docker.toneAt, 1, "the red line is still the one that names the problem");
  assert.strictEqual(noTier("colima is not installed").lines[2], "Until it is, sessions start on this machine, uncontained.");
  assert.strictEqual(noTier("run `culibox doctor` for details").lines[2], "Until boxes are available, sessions start on this machine, uncontained.");
  assert.strictEqual(noTier("").lines[2], "Until boxes are available, sessions start on this machine, uncontained.");
  // The instance's own default_runtime: box is a tier a session of this profile asks for, and with no box layer that is refused.
  const asked = (runtimeAsked, reason) => byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: reason, view: { ...unnamedView, runtime_asked: runtimeAsked }, doc: unnamedDoc })), "runs").lines[2];
  assert.strictEqual(asked("box", "Docker is not installed"), "Sessions with this profile can't start until Docker is installed.");
  assert.strictEqual(asked("box", "colima is not installed"), "Sessions with this profile can't start until colima is installed.");
  assert.strictEqual(asked("box", "run `culibox doctor` for details"), "Sessions with this profile can't start until boxes are available.");
  assert.strictEqual(asked("", "Docker is not installed"), "Until it is, sessions start on this machine, uncontained.", "nothing asked: an older server, or no default_runtime");
  assert.strictEqual(asked(undefined, "Docker is not installed"), "Until it is, sessions start on this machine, uncontained.");
  // The stored profile comes first: a stored host or box tier is not the default's to change.
  const hostDoc = { ...unnamedDoc, runtime: "host" };
  const storedHost = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed", view: { ...awsOpsView, runtime: "host", runtime_asked: "host" }, doc: hostDoc })), "runs");
  assert.strictEqual(storedHost.lines.length, 3, "a host profile gets no box consequence at all: its two lines and the pre-select one");
  assert.strictEqual(byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed", view: { ...awsOpsView, runtime_asked: "" } })), "runs").lines[2], "Sessions with this profile can't start until Docker is installed.", "a stored box is named whatever the row says");
  // A stored box tier is a named one: today's sentence stays.
  assert.strictEqual(byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed" })), "runs").lines[2], "Sessions with this profile can't start until Docker is installed.");
});

// A readable folder and a private copy only work in a box: a session that asks for no tier is refused for either when there is no
// box layer (internal/session: readable folders on the host, a clone on the host), though the server can store such a profile with no
// tier (the default resolves to box). So "sessions start on this machine" would be false for it, and the line says can't start.
test("boxes unavailable on a profile that names no tier but holds a readable folder or a private copy: its sessions are refused", () => {
  const bare = { ...awsOpsDoc };
  delete bare.runtime;
  delete bare.context_paths;
  const bareView = { ...awsOpsView };
  delete bareView.context_paths;
  const line = (view, doc, reason) => byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: reason || "Docker is not installed", view, doc })), "runs").lines[2];
  const CANT = "Sessions with this profile can't start until Docker is installed.";
  const SPAWNS = "Until it is, sessions start on this machine, uncontained.";
  const folders = ["/home/you/other-repo/docs"];
  const repo = "/home/you/work/k8s-gitops";
  assert.strictEqual(line(bareView, bare), SPAWNS, "nothing that needs a box: unchanged");
  // A readable folder, from the list row or from the stored profile alone.
  assert.strictEqual(line({ ...bareView, context_paths: folders }, bare), CANT);
  assert.strictEqual(line(bareView, { ...bare, context_paths: folders }), CANT);
  assert.strictEqual(line({ ...bareView, context_paths: [] }, { ...bare, context_paths: [] }), SPAWNS, "an empty list holds nothing");
  // A private copy, the same two ways.
  assert.strictEqual(line({ ...bareView, clone_of: repo }, bare), CANT);
  assert.strictEqual(line(bareView, { ...bare, clone_of: repo }), CANT);
  assert.strictEqual(line({ ...bareView, clone_of: "  " }, { ...bare, clone_of: "" }), SPAWNS, "a blank copy source is no copy");
  // The reason's wording still shapes the sentence.
  assert.strictEqual(line({ ...bareView, context_paths: folders }, bare, "colima is not installed"), "Sessions with this profile can't start until colima is installed.");
  assert.strictEqual(line({ ...bareView, clone_of: repo }, bare, "run `culibox doctor` for details"), "Sessions with this profile can't start until boxes are available.");
  // A profile that stores host is not a box profile at all: it gets no consequence line.
  assert.strictEqual(byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed", view: { ...bareView, runtime: "host" }, doc: { ...bare, runtime: "host" } })), "runs").lines.length, 3, "the two host lines and the pre-select one, no consequence");
});

// With no box layer the list view reports no network posture for a boxed profile; the
// read view still shows the posture it will have once boxes exist (pfvPosture).
test("no box layer: the network block reads the posture the profile will have", () => {
  const noLayer = (over, doc) => byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed", view: { ...awsOpsView, network: "", ...over }, doc: { ...awsOpsDoc, ...doc } })), "network").lines;
  const unbound = { aws_connections: undefined, github_owners: undefined, github_repos: undefined, github_permissions: undefined, tokens: undefined };
  // Nothing bound: any public website over HTTPS (the b-created words), not an allowlist.
  assert.deepStrictEqual(noLayer(unbound), [
    "Any public website over HTTPS, through CuliBox's proxy",
    "An allowlist starts once it holds cloud access or a token. Private, tailnet and host addresses stay blocked.",
  ]);
  // Bound with egress_open: all internet hosts, plainly.
  assert.deepStrictEqual(noLayer({}, { egress_open: true }), ["Internet only through CuliBox's proxy, HTTPS to hostnames", "All internet hosts (no allowlist)."]);
  // Bound: the allowlist sentence (each of AWS, GitHub, a token, the old-style AWS setting binds).
  for (const only of [{ aws_connections: ["prod-read"] }, { github_owners: ["culiops"] }, { tokens: ["sentry-staging"] }, { aws_inline: { name: "legacy" } }]) {
    const lines = noLayer({ ...unbound, ...only });
    assert.strictEqual(lines[0], "Internet only through CuliBox's proxy, HTTPS to hostnames");
    assert.match(lines[1], /^Because this profile holds cloud access and tokens/, JSON.stringify(only));
  }
  // A host profile's Network block reads as a box would, layer or not (it is tagged Applies to Box).
  assert.strictEqual(byId(pfvReadView(hostIn({ boxRuntime: "" })), "network").lines[0], "Any public website over HTTPS, through CuliBox's proxy");
});

test("boxes available (or not yet known) is not an unavailable state", () => {
  for (const boxRuntime of ["runc", "sysbox-runc", undefined, null]) {
    const runs = byId(pfvReadView(awsIn({ boxRuntime })), "runs");
    assert.strictEqual(runs.lines.length, 3, String(boxRuntime));
    assert.ok(!runs.tone);
  }
});

test("boxes unavailable on a host profile is not an error", () => {
  const v = pfvReadView(hostIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed" }));
  const runs = byId(v, "runs");
  assert.deepStrictEqual(runs.lines, [
    "On this machine: runs as you, with your files, your network and your credentials",
    "Boxes are unavailable on this machine: Docker is not installed.",
    "New session pre-selects: Host",
  ]);
  assert.ok(!runs.tone && runs.toneAt === undefined, "muted, never red");
  // Switch to box would be a control the user cannot use: hidden, and the Runs in block says why.
  assert.ok(!byId(v, "canreach").switchToBox);
  assert.strictEqual(byId(v, "canreach").edit, false);
});

// #154: the box layer exists (box_runtime is set) but its docker daemon is stopped or not permitted
// (/api/meta box_daemon_reason). The page says so on the Runs in block; it does not call the layer
// unavailable, whose defaults flip a new profile to the host tier.
test("a box layer whose daemon cannot be used says why, on a box profile and quietly on a host one", () => {
  const reason = "Docker is not running";
  const box = byId(pfvReadView(awsIn({ boxRuntime: "runc", boxDaemonReason: reason })), "runs");
  assert.deepStrictEqual(box.lines, [
    "In a box",
    "Boxes can't start right now: Docker is not running.",
    "Sessions with this profile can't start until that is fixed.",
    "New session pre-selects: Box · in place",
  ]);
  assert.strictEqual(box.tone, "error");
  assert.strictEqual(box.toneAt, 1);
  const host = byId(pfvReadView(hostIn({ boxRuntime: "runc", boxDaemonReason: reason })), "runs");
  assert.deepStrictEqual(host.lines, [
    "On this machine: runs as you, with your files, your network and your credentials",
    "Boxes can't start right now: Docker is not running.",
    "New session pre-selects: Host",
  ]);
  assert.ok(!host.tone, "a host profile does not need a box: muted, never red");
  // An unclassified failure is not refused at spawn (only the two named ones are): the consequence is softer.
  const other = byId(pfvReadView(awsIn({ boxRuntime: "runc", boxDaemonReason: "run `culibox doctor` for details" })), "runs");
  assert.deepStrictEqual(other.lines, [
    "In a box",
    "Boxes can't start right now: run `culibox doctor` for details.",
    "Sessions with this profile may fail to start.",
    "New session pre-selects: Box · in place",
  ]);
  const denied = byId(pfvReadView(awsIn({ boxRuntime: "runc", boxDaemonReason: "no permission to use Docker" })), "runs");
  assert.strictEqual(denied.lines[2], "Sessions with this profile can't start until that is fixed.");
  // The daemon answering, an older server, and no layer at all keep today's lines.
  for (const over of [{ boxDaemonReason: "" }, {}, { boxDaemonReason: undefined }]) {
    const ok = byId(pfvReadView(awsIn({ boxRuntime: "runc", ...over })), "runs");
    assert.strictEqual(ok.lines.length, 3);
    assert.ok(!ok.tone);
  }
  const noLayer = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed", boxDaemonReason: reason })), "runs");
  assert.strictEqual(noLayer.lines[1], "Boxes are unavailable on this machine: Docker is not installed.", "no layer: that reason wins and the daemon one is not added");
  assert.strictEqual(noLayer.lines.length, 4);
});

test("private copy workspace lists not-home branches with today's reason text", () => {
  const notHome = {
    at: "2026-11-05T10:00:00Z", base: "master",
    branches: [{ branch: "fix-drift", reason: "not-ff" }, { branch: "bump-ingress-chart", reason: "checked-out", detail: "checked out at /home/you/work/k8s-gitops" }],
  };
  const view = {
    name: "gitops-clone", editable: true, source: "runtime", runtime: "box", bin: "claude", claude: true,
    default_cwd: "/home/you/box-clones/k8s-gitops", clone_of: "/home/you/work/k8s-gitops", clone_share: ["CLAUDE.md"], auto_return: true,
    github_owners: ["culiops"], github_repos: [{ owner: "culiops", repos: ["k8s-gitops"] }], github_permissions: { contents: "read", issues: "read" },
    network: "allowlist", not_home: notHome,
  };
  const v = pfvReadView({ view, doc: { name: "gitops-clone", bin: "claude", clone_of: view.clone_of, clone_share: ["CLAUDE.md"], default_cwd: view.default_cwd }, meta, creds, boxRuntime: "runc", bundleImports: true, defs });
  const ws = byId(v, "workspace");
  const kinds = ws.rows.map((r) => r.kind);
  assert.deepStrictEqual(kinds, ["Folder", "Copy", "Not home", "Not home"]);
  assert.deepStrictEqual(pickRow(ws.rows[0]), {
    kind: "Folder", name: "Private copy of /home/you/work/k8s-gitops", sub: "Its branches come home as box/<branch>, automatically", scope: "Untouched until work comes home",
  });
  assert.deepStrictEqual(pickRow(ws.rows[1]), {
    kind: "Copy", name: "/home/you/box-clones/k8s-gitops", sub: "Shares CLAUDE.md from the original, read only", scope: "Read and write",
  });
  const [a, b] = ws.rows.slice(2);
  assert.strictEqual(a.name, "fix-drift");
  assert.strictEqual(a.scope, notHomeReasonText(notHome.branches[0]));
  assert.strictEqual(a.scope, "box/fix-drift on the host has commits this branch lacks");
  assert.strictEqual(a.fix, notHomeFix("gitops-clone", "master", notHome.branches[0]));
  assert.strictEqual(a.fix, "`culibox clone fetch -base master gitops-clone fix-drift` overwrites box/fix-drift");
  assert.strictEqual(b.scope, "box/bump-ingress-chart is checked out in your repo, so it did not move");
  assert.strictEqual(b.fix, "switch your repo off box/bump-ingress-chart; it comes home at the next exit or spawn");
  assert.strictEqual(b.sub, "checked out at /home/you/work/k8s-gitops", "the server's detail rides along as today's form shows it");
  assert.deepStrictEqual(ws.notHome, { label: notHomeLabel(notHome), title: notHomeTitle(notHome) });
  assert.strictEqual(ws.notHome.label, "2 branches not home");
  assert.strictEqual(byId(v, "runs").lines[1], "A container on this machine. It can write only to its private copy and reach only what this page lists.");
  // b-clone: kind Files, the path, "From the original, read only"; the imports row is the other Files row.
  const reach = byId(v, "reachable").rows;
  assert.deepStrictEqual(reach.filter((r) => r.original).map((r) => [r.kind, r.name, r.scope]), [["Files", "CLAUDE.md", "From the original, read only"]]);
  assert.deepStrictEqual(reach.filter((r) => r.kind === "Files" && !r.original).map((r) => r.name), ["CLAUDE.md imports"]);

  // Auto-return off says so; a copy with nothing behind says nothing about branches.
  const off = pfvReadView({ view: { ...view, auto_return: undefined, not_home: undefined }, doc: {}, meta, creds, boxRuntime: "runc" });
  assert.ok(!byId(off, "workspace").rows[0].sub.endsWith("automatically"));
  assert.match(byId(off, "workspace").rows[0].sub, /auto-return is off/);
  assert.deepStrictEqual(byId(off, "workspace").rows.map((r) => r.kind), ["Folder", "Copy"]);
  assert.strictEqual(byId(off, "workspace").notHome, null);

  // A return that could not check the clone: no branches, one row that says why.
  const err = pfvReadView({ view: { ...view, not_home: { at: "x", error: "could not open the clone" } }, doc: {}, meta, creds, boxRuntime: "runc" });
  const nh = byId(err, "workspace").rows.filter((r) => r.kind === "Not home");
  assert.deepStrictEqual(nh.map((r) => r.scope), ["work not checked: could not open the clone"]);
});

test("a copy with no shared files says nothing about sharing", () => {
  const view = { name: "c", editable: true, source: "runtime", runtime: "box", bin: "claude", claude: true, default_cwd: "/h/c", clone_of: "/h/r", auto_return: true };
  const v = pfvReadView({ view, doc: {}, meta, creds, boxRuntime: "runc" });
  assert.strictEqual(byId(v, "workspace").rows[1].sub, "");
});

test("missing credential row carries missing:true", () => {
  const thin = { aws_connections: [meta.aws_connections[0]], static_tokens: [meta.static_tokens[0]] };
  const v = pfvReadView(awsIn({ meta: thin }));
  const staging = byId(v, "cloud").rows.find((r) => r.name === "staging-read");
  assert.strictEqual(staging.missing, true);
  assert.strictEqual(staging.scope, "Not found in Credentials");
  assert.strictEqual(staging.sub, "");
  assert.strictEqual(staging.tag, "");
  const sentry = byId(v, "cloud").rows.find((r) => r.name === "sentry-staging");
  assert.deepStrictEqual([sentry.missing, sentry.scope], [true, "Not found in Credentials"]);
  const prod = byId(v, "cloud").rows.find((r) => r.name === "prod-read");
  assert.strictEqual(prod.missing, false);
  assert.strictEqual(prod.scope, "Read only. Write only after you grant a window, max 30 min.");
  // Meta not loaded yet: nothing is known, so nothing is called missing.
  assert.ok(byId(pfvReadView(awsIn({ meta: null })), "cloud").rows.every((r) => r.missing === false));
});

test("the tier is the stored profile's when it names one; the list row only when it names none", () => {
  const tier = (view, doc, over) => pfvReadView(awsIn(Object.assign({ view, doc }, over))).header.tier;
  const boxDoc = { ...awsOpsDoc, runtime: "box" }, hostDoc2 = { ...awsOpsDoc, runtime: "host" };
  const noTier = { ...awsOpsDoc }; delete noTier.runtime;
  // A stale row never beats the stored tier, either way round.
  assert.strictEqual(tier({ ...awsOpsView, runtime: "host" }, boxDoc), "box");
  assert.strictEqual(tier({ ...awsOpsView, runtime: "box" }, hostDoc2), "host");
  assert.deepStrictEqual(byId(pfvReadView(awsIn({ view: { ...awsOpsView, runtime: "host" }, doc: boxDoc })), "runs").lines[0], "In a box");
  assert.deepStrictEqual(ids(pfvReadView(awsIn({ view: { ...awsOpsView, runtime: "host" }, doc: boxDoc }))), ["runs", "workspace", "agent", "cloud", "reachable", "network", "canreach", "advanced"]);
  // A profile that stores none takes the resolved tier.
  assert.strictEqual(tier({ ...awsOpsView, runtime: "host" }, noTier), "host");
  assert.strictEqual(tier({ ...awsOpsView, runtime: "box" }, noTier), "box");
  assert.strictEqual(tier({ ...awsOpsView, runtime: undefined }, noTier), "box", "nothing says host: box");
  // Anything but box or host stored is not a tier.
  assert.strictEqual(tier({ ...awsOpsView, runtime: "host" }, { ...awsOpsDoc, runtime: "" }), "host");
});

test("a GitHub owner whose App or token is gone from Credentials reads Not found in Credentials", () => {
  // The list view names owners and repositories only; the stored profile says which login each owner uses.
  const doc = (owners) => ({ ...awsOpsDoc, github: { owners, permissions: { contents: "read" } } });
  const appDoc = doc([{ owner: "culiops", app: "acme-bot", repos: ["payments-api"] }]);
  const withApps = (apps) => ({ ...meta, github_apps: apps });
  const gh = (o) => byId(pfvReadView(awsIn(o)), "cloud").rows.find((r) => r.kind === "GitHub");

  const gone = gh({ doc: appDoc, meta: withApps(["acme-app"]) });
  assert.deepStrictEqual([gone.name, gone.missing, gone.scope, gone.sub, gone.tag], ["culiops", true, "Not found in Credentials", "repo payments-api", ""]);
  assert.deepStrictEqual(byId(pfvReadView(awsIn({ doc: appDoc, meta: withApps([]) })), "cloud").rows.map((r) => r.name), ["prod-read", "staging-read", "culiops", "grafana-prod", "sentry-staging"], "the row keeps its place");
  assert.strictEqual(byId(pfvReadView(awsIn({ doc: appDoc, meta: withApps([]) })), "cloud").count, 5);

  // Still defined: the read view says what it says today.
  const ok = gh({ doc: appDoc, meta: withApps(["acme-bot"]) });
  assert.deepStrictEqual([ok.missing, ok.scope], [false, "Read only: contents, pull requests, issues, metadata. 1-hour tokens."]);

  // A token owner is checked against static_tokens.
  const tokDoc = doc([{ owner: "culiops", token: "gh-pat", repos: ["payments-api"] }]);
  assert.strictEqual(gh({ doc: tokDoc, meta: withApps([]) }).missing, true);
  assert.strictEqual(gh({ doc: doc([{ owner: "culiops", token: "grafana-prod", repos: ["payments-api"] }]), meta: withApps([]) }).missing, false);

  // Nothing known, nothing called missing: no github_apps list in meta, meta not loaded, a read-only profile still says so.
  assert.strictEqual(gh({ doc: appDoc }).missing, false);
  assert.strictEqual(gh({ doc: appDoc, meta: null }).missing, false);
  assert.strictEqual(gh({ doc: awsOpsDoc, meta: withApps([]) }).missing, false, "a doc that names no login");
  const ro = byId(pfvReadView(awsIn({ doc: appDoc, meta: withApps([]), view: { ...awsOpsView, editable: false, source: "config" } })), "cloud");
  assert.strictEqual(ro.rows.find((r) => r.kind === "GitHub").scope, "Not found in Credentials");
  assert.strictEqual(ro.add, "");
});

test("the old-style AWS setting offers the move to a saved connection, unless read-only", () => {
  const view = { ...awsOpsView, aws_connections: undefined, aws_inline: { name: "legacy-prod", prod: true, elevate_max_minutes: 30 } };
  const row = (o) => byId(pfvReadView(awsIn(o)), "cloud").rows.find((r) => r.kind === "Old-style AWS");
  assert.strictEqual(row({ view }).action, "Move to a saved connection");
  assert.strictEqual(row({ view }).tag, "PROD");
  assert.ok(!row({ view: { ...view, editable: false, source: "config" } }).action, "a read-only profile cannot be converted");
});

test("agent line omits unset parts", () => {
  const line = (options, view) => pfvReadView(awsIn({ doc: { ...awsOpsDoc, options }, view: view || awsOpsView })).header.agentLine;
  assert.strictEqual(line({ model: "claude-opus-5-5", effort: "high" }), "Claude Code · claude-opus-5-5 · high effort");
  assert.strictEqual(line({ model: "claude-opus-5-5" }), "Claude Code · claude-opus-5-5");
  assert.strictEqual(line({ effort: "high" }), "Claude Code · agent's default model · high effort");
  assert.strictEqual(line({}), "Claude Code · agent's default model");
  assert.strictEqual(line({ model: "", effort: "" }), "Claude Code · agent's default model");
  assert.strictEqual(line(undefined), "Claude Code · agent's default model");
  assert.strictEqual(line({ effortLevel: "low" }), "Claude Code · agent's default model · low effort", "the effortLevel spelling is the same setting");
  // A non-claude binary is named by its binary, with no "default model" it does not have.
  const codex = { ...awsOpsView, bin: "/usr/local/bin/codex", claude: false };
  assert.strictEqual(line({}, codex), "codex");
  assert.strictEqual(line({ model: "gpt-x" }, codex), "codex · gpt-x");
  // The list view's options carry the value when the stored profile has none.
  const viaView = pfvReadView({ view: { ...awsOpsView, options: [{ key: "model", type: "enum", value: "claude-sonnet-5" }, { key: "effort", type: "enum", value: "" }] }, doc: {}, meta, creds, boxRuntime: "runc" });
  assert.strictEqual(viaView.header.agentLine, "Claude Code · claude-sonnet-5");
});

test("a named agent account is said; the default account reads as the default login", () => {
  const v = pfvReadView(awsIn({ view: { ...awsOpsView, account: "work" } }));
  assert.strictEqual(byId(v, "agent").lines[1], "Signs in with the work Claude login");
  const d = pfvReadView(awsIn({ view: { ...awsOpsView, account: "default" } }));
  assert.strictEqual(byId(d, "agent").lines[1], "Signs in with the default Claude login (~/.claude)");
});

test("advanced count is 0 for replies off and imports on", () => {
  assert.strictEqual(pfvAdvancedCount({ replyable: false, imports_off: false, args: [], env: {}, secrets: [], options: {} }), 0);
  assert.strictEqual(pfvAdvancedCount({}), 0);
  assert.strictEqual(pfvAdvancedCount(null), 0);
  assert.strictEqual(pfvAdvancedCount(undefined), 0);
  // model and effort live in the Agent block, so they are not Advanced; an unset option is not set.
  assert.strictEqual(pfvAdvancedCount({ options: { model: "m", effort: "high", effortLevel: "low", dangerously: false, mode: "" } }), 0);
  // A stored base counts on any tier (the starting environment row is tagged host, not hidden); "inherit" is the default.
  assert.strictEqual(pfvAdvancedCount({ base: "clean", runtime: "box" }), 1);
  assert.strictEqual(pfvAdvancedCount({ base: "inherit", runtime: "host" }), 0);
  assert.strictEqual(pfvAdvancedCount({ runtime: "host" }), 0);
});

test("advanced count counts args and env", () => {
  assert.strictEqual(pfvAdvancedCount({ args: ["--verbose"] }), 1);
  assert.strictEqual(pfvAdvancedCount({ args: ["--verbose"], env: { A: "1", B: "2" } }), 2, "env is one setting however many variables");
  assert.strictEqual(pfvAdvancedCount({ args: ["--verbose"], env: { A: "1" }, secrets: ["S"] }), 3);
  assert.strictEqual(pfvAdvancedCount({ replyable: true }), 1);
  assert.strictEqual(pfvAdvancedCount({ imports_off: true }), 1);
  assert.strictEqual(pfvAdvancedCount({ base: "clean", runtime: "host" }), 1);
  assert.strictEqual(pfvAdvancedCount({ base: "login" }, "host"), 1, "the tier can be given when the stored runtime is blank");
  assert.strictEqual(pfvAdvancedCount({ options: { dangerously: true, mode: "plan", model: "m" } }), 2);
  assert.strictEqual(pfvAdvancedCount({ args: ["a"], env: { A: "1" }, secrets: ["S"], replyable: true, imports_off: true, base: "clean", runtime: "host", options: { x: true } }), 7);
});

test("Advanced opens by itself when something is set, and lists what is set by name", () => {
  const doc = { ...awsOpsDoc, args: ["--verbose", "--flag"], env: { LOG: "secret-looking-value", A: "1" }, secrets: ["TOKEN_X"], replyable: true, imports_off: true, options: { model: "claude-opus-5-5", effort: "high", dangerously: true } };
  const v = pfvReadView(awsIn({ doc }));
  const adv = byId(v, "advanced");
  assert.strictEqual(adv.count, 6);
  assert.strictEqual(adv.open, true);
  assert.strictEqual(adv.status, "6 set");
  assert.strictEqual(adv.rows.length, 6);
  assert.ok(!adv.lines[0].includes("All at their defaults"));
  const text = JSON.stringify(adv.rows);
  assert.ok(!/secret-looking-value/.test(text), "environment variables are listed by name, never by value");
  assert.ok(text.includes("A, LOG") && text.includes("TOKEN_X") && text.includes("--verbose --flag"));
  assert.ok(text.includes("Skip permission checks"), "an option is named by its definition's label");
  // On host the starting environment counts and is listed too.
  const host = pfvReadView(hostIn({ doc: { ...hostDoc, base: "clean" } }));
  assert.strictEqual(byId(host, "advanced").count, 1);
  assert.strictEqual(byId(host, "advanced").rows[0].kind, "Starting environment");
});

test("the model never carries a credential reference and does not change its input", () => {
  const input = awsIn({ meta: { ...meta, static_tokens: meta.static_tokens.map((t) => ({ ...t, value: "ghp_SECRETVALUE", ref: "keychain:SECRETREF" })) } });
  const before = JSON.stringify(input);
  const v = pfvReadView(input);
  assert.strictEqual(JSON.stringify(input), before, "pure: the arguments are left as they were");
  assert.ok(!/arn:|SECRETVALUE|SECRETREF/i.test(JSON.stringify(v)));
  assert.doesNotThrow(() => pfvReadView());
  assert.doesNotThrow(() => pfvReadView({}));
  assert.deepStrictEqual(pfvReadView({}).header.name, "");
});

test("every block carries the tier it applies to", () => {
  const want = [["runs", ""], ["workspace", ""], ["agent", ""], ["cloud", "box"], ["reachable", "box"], ["network", "box"], ["canreach", "host"], ["advanced", ""]];
  for (const v of [pfvReadView(hostIn()), pfvReadView(awsIn())]) {
    assert.deepStrictEqual(v.blocks.map((b) => [b.id, b.applies]), want);
  }
});

test("Runs in ends with what New session pre-selects", () => {
  const last = (v) => { const l = byId(v, "runs").lines; return l[l.length - 1]; };
  assert.strictEqual(last(pfvReadView(awsIn())), "New session pre-selects: Box · in place");
  assert.strictEqual(last(pfvReadView(awsIn({ view: { ...awsOpsView, clone_of: "/home/you/work/payments-api" } }))), "New session pre-selects: Box · clone");
  assert.strictEqual(last(pfvReadView(awsIn({ doc: { ...awsOpsDoc, clone_of: "/home/you/work/payments-api" } }))), "New session pre-selects: Box · clone");
  assert.strictEqual(last(pfvReadView(hostIn())), "New session pre-selects: Host");
  const daemon = byId(pfvReadView(awsIn({ boxDaemonReason: "Docker is not running" })), "runs");
  assert.strictEqual(daemon.tone, "error");
  assert.strictEqual(daemon.toneAt, 1);
  assert.strictEqual(daemon.lines[daemon.lines.length - 1], "New session pre-selects: Box · in place");
  const gone = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed" })), "runs");
  assert.strictEqual(gone.toneAt, 1);
  assert.strictEqual(gone.lines[gone.lines.length - 1], "New session pre-selects: Box · in place");
  assert.strictEqual(pfvPreselectWord("host", true), "Host");
  assert.strictEqual(pfvPreselectWord("box", true), "Box · clone");
  assert.strictEqual(pfvPreselectWord("box", false), "Box · in place");
});

test("a host profile's Cloud, Also reachable and Network read as a box would", () => {
  const v = pfvReadView(hostIn());
  assert.deepStrictEqual(byId(v, "cloud").lines, ["Nothing yet", "It cannot use AWS, GitHub or any token."]);
  assert.strictEqual(byId(v, "network").lines[0], "Any public website over HTTPS, through CuliBox's proxy");
  assert.strictEqual(byId(v, "cloud").add, "+ Add access");
});

test("Can reach offers Switch to box only on a host profile", () => {
  assert.strictEqual(byId(pfvReadView(awsIn()), "canreach").switchToBox, false);
  assert.strictEqual(byId(pfvReadView(hostIn()), "canreach").switchToBox, true);
});

test("Advanced lists the starting environment on any tier, tagged host, and the imports row tagged box", () => {
  assert.deepStrictEqual(pfvAdvancedItems({ base: "clean", imports_off: true }, "box", []).map((r) => [r.kind, r.applies]), [["Starting environment", "host"], ["Files", "box"]]);
  assert.strictEqual(byId(pfvReadView(awsIn()), "advanced").lines[0], "Starting environment, arguments, environment variables, sidebar replies and CLAUDE.md imports. All at their defaults.");
  assert.deepStrictEqual(pfvAdvancedItems({ args: ["a"] }, "box", []).map((r) => r.applies), [""], "a row defaults applies to empty");
});

// --- Permission mode and the critical-rm option in the read view (#342) ---------------------------------------------------------

const PM_READ_DEFS = defs.concat([
  { key: "permission_mode", label: "Permission mode", type: "enum", choices: ["manual", "acceptEdits", "auto", "plan", "bypassPermissions"], bins: ["claude"] },
  { key: "skip_critical_rm_prompt", label: "Skip the critical-rm prompt", type: "bool", tier: "box", bins: ["claude"] },
  { key: "skip_permissions_legacy", label: "Skip permissions", type: "bool", flag: "--dangerously-skip-permissions", bins: ["claude"] },
]);
const pmRead = (options, over) => pfvReadView(awsIn(Object.assign({ defs: PM_READ_DEFS, doc: { ...awsOpsDoc, options } }, over)));

test("the read view's Agent keys are the form's: PFV_AGENT_OPTIONS and PFE_AGENT_OPTIONS list the same keys", () => {
  const list = (file, name) => {
    const m = new RegExp("const " + name + " = (\\[[^\\]]*\\]);").exec(fs.readFileSync(path.join(__dirname, "..", "static", file), "utf8"));
    assert.ok(m, name + " is declared in " + file);
    return JSON.parse(m[1]);
  };
  assert.deepStrictEqual(list("profileview.js", "PFV_AGENT_OPTIONS"), list("profileedit.js", "PFE_AGENT_OPTIONS"));
});

test("Agent read rows: Permission mode reads by its label with its line; the critical-rm option when on, tagged Applies to Box; neither in Advanced", () => {
  const v = pmRead({ model: "claude-opus-5-5", permission_mode: "bypassPermissions", skip_critical_rm_prompt: true });
  const agent = byId(v, "agent");
  assert.deepStrictEqual(agent.rows.map((r) => [r.kind, r.name, r.sub, r.applies]), [
    ["Mode", "Bypass", "Runs every action without asking.", ""],
    ["Option", "Skip the critical-rm prompt", "", "box"],
  ]);
  assert.strictEqual(agent.count, 2);
  assert.deepStrictEqual(byId(v, "advanced").rows, []);
  assert.strictEqual(byId(v, "advanced").count, 0);
  // Off, or never set: no row, like an unset effort.
  const off = pmRead({ permission_mode: "plan", skip_critical_rm_prompt: false });
  assert.deepStrictEqual(byId(off, "agent").rows.map((r) => [r.kind, r.name]), [["Mode", "Plan"]]);
  assert.deepStrictEqual(byId(pmRead({ model: "claude-opus-5-5" }), "agent").rows, []);
  // A choice a config.json def adds shows as it is.
  const odd = pmRead({ permission_mode: "default" }, { defs: PM_READ_DEFS.map((d) => (d.key === "permission_mode" ? { ...d, choices: ["default", "plan"] } : d)) });
  assert.deepStrictEqual(byId(odd, "agent").rows.map((r) => [r.name, r.sub]), [["default", ""]]);
});

test("Agent read rows: the older bypass bool reads as Bypass only when true, with its (older form) line, and is never an Advanced row", () => {
  const on = pmRead({ skip_permissions_legacy: true });
  assert.deepStrictEqual(byId(on, "agent").rows.map((r) => [r.kind, r.name, r.sub]), [["Mode", "Bypass", "Runs every action without asking. Set by Skip permissions (older form)"]]);
  assert.deepStrictEqual(byId(on, "advanced").rows, []);
  // permission_mode wins over the bool, and the line is not said.
  const both = pmRead({ skip_permissions_legacy: true, permission_mode: "plan" });
  assert.deepStrictEqual(byId(both, "agent").rows.map((r) => [r.name, r.sub]), [["Plan", "Reads and plans only. Changes nothing until you approve the plan."]]);
  // False is not Bypass and not a row anywhere.
  const off = pmRead({ skip_permissions_legacy: false });
  assert.deepStrictEqual(byId(off, "agent").rows, []);
  assert.deepStrictEqual(byId(off, "advanced").rows, []);
  // For a command the Permission mode def does not apply to, the older bool is an ordinary Advanced row again.
  const sh = pmRead({ skip_permissions_legacy: true }, { view: { ...awsOpsView, bin: "bash", claude: false }, doc: { ...awsOpsDoc, bin: "bash", options: { skip_permissions_legacy: true } } });
  assert.deepStrictEqual(byId(sh, "agent").rows, []);
  assert.deepStrictEqual(byId(sh, "advanced").rows.map((r) => r.name), ["On"]);
  // No Permission mode def at all (an older daemon): nothing changes, the bool stays in Advanced.
  const none = pfvReadView(awsIn({ defs: PM_READ_DEFS.filter((d) => d.key !== "permission_mode"), doc: { ...awsOpsDoc, options: { skip_permissions_legacy: true } } }));
  assert.deepStrictEqual(byId(none, "advanced").rows.map((r) => [r.kind, r.name]), [["Skip permissions", "On"]]);
});
