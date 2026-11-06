"use strict";
// profileview.js: the profile page's read view as data (spec 5.2-5.4, 5.6). Pure:
// every wording of Cloud, Also reachable and Network comes from reachModel, and
// the rest is the approved mockup's (profile-mockups/shots/b-*.png).
const test = require("node:test");
const assert = require("node:assert");
const { pfvReadView, pfvAdvancedCount } = require("../static/profileview.js");
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

test("aws-ops read view has the seven blocks in order", () => {
  const v = pfvReadView(awsIn());
  assert.deepStrictEqual(ids(v), ["runs", "workspace", "agent", "cloud", "reachable", "network", "advanced"]);
  assert.deepStrictEqual(v.blocks.map((b) => b.label), ["Runs in", "Workspace", "Agent", "Cloud and tokens", "Also reachable", "Network", "Advanced"]);
  assert.deepStrictEqual(v.header, { name: "aws-ops", tier: "box", agentLine: "Claude Code · claude-opus-5-5 · high effort", readOnly: false, source: "runtime" });
  assert.strictEqual(v.note, "");
  assert.ok(v.blocks.every((b) => b.edit === true), "every block of an editable profile has Edit");

  assert.deepStrictEqual(byId(v, "runs").lines, ["In a box", "A container on this machine. It can write only to its workspace and reach only what this page lists."]);
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
  const strip = (it) => ({ kind: it.kind, name: it.name, sub: it.sub, scope: it.scope, tag: it.tag, missing: it.missing });
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
  assert.deepStrictEqual(ids(v), ["runs", "workspace", "agent", "cloud", "reachable", "network", "advanced"]);
  assert.deepStrictEqual(byId(v, "cloud").rows, []);
  assert.strictEqual(byId(v, "cloud").add, "+ Add access");
  assert.deepStrictEqual(byId(v, "cloud").lines, ["Nothing yet. It cannot use AWS, GitHub or any token."]);
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
  assert.strictEqual(byId(bare, "reachable").lines.length, 1);
});

test("runbook-host read view has runs, workspace, agent, canreach, advanced and no cloud", () => {
  const v = pfvReadView(hostIn());
  assert.deepStrictEqual(ids(v), ["runs", "workspace", "agent", "canreach", "advanced"]);
  assert.strictEqual(v.header.tier, "host");
  assert.strictEqual(v.header.agentLine, "Claude Code · agent's default model");
  // The spec's sentence verbatim, on one line; the muted line is empty when boxes are available.
  assert.deepStrictEqual(byId(v, "runs").lines, ["On this machine: runs as you, with your files, your network and your credentials", ""]);
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
  assert.match(byId(v, "advanced").lines[0], /^Starting environment, arguments, environment variables and sidebar replies\./);
  assert.ok(byId(v, "advanced").lines[0].endsWith("All at their defaults."));
  for (const id of ["cloud", "reachable", "network"]) assert.strictEqual(byId(v, id), undefined, id);
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
  ]);
  assert.strictEqual(runs.tone, "error");
  assert.strictEqual(runs.toneAt, 1, "only the sentence that names the problem is red");
  assert.strictEqual(v.header.tier, "box", "the profile still says box; it is what will refuse to start");
  assert.deepStrictEqual(ids(v), ["runs", "workspace", "agent", "cloud", "reachable", "network", "advanced"]);
  // On a box with no layer the list view reports no network posture: the block still reads from the builder.
  const noLayer = pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed", view: { ...awsOpsView, network: "" } }));
  assert.deepStrictEqual(byId(noLayer, "network").lines, byId(v, "network").lines);

  const colima = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "colima is not installed" })), "runs");
  assert.strictEqual(colima.lines[1], "Boxes are unavailable on this machine: colima is not installed.");
  assert.strictEqual(colima.lines[2], "Sessions with this profile can't start until colima is installed.");

  const doctor = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "run `agent-box doctor` for details" })), "runs");
  assert.strictEqual(doctor.lines[1], "Boxes are unavailable on this machine: run `agent-box doctor` for details.");
  assert.strictEqual(doctor.lines[2], "Sessions with this profile can't start until boxes are available.");
  assert.strictEqual(doctor.tone, "error");

  // An older server sends no reason: the sentence still stands, without a clause after it.
  const none = byId(pfvReadView(awsIn({ boxRuntime: "", boxUnavailableReason: "" })), "runs");
  assert.strictEqual(none.lines[1], "Boxes are unavailable on this machine.");
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
  // A host profile has no network block at all, layer or not.
  assert.strictEqual(byId(pfvReadView(hostIn({ boxRuntime: "" })), "network"), undefined);
});

test("boxes available (or not yet known) is not an unavailable state", () => {
  for (const boxRuntime of ["runc", "sysbox-runc", undefined, null]) {
    const runs = byId(pfvReadView(awsIn({ boxRuntime })), "runs");
    assert.strictEqual(runs.lines.length, 2, String(boxRuntime));
    assert.ok(!runs.tone);
  }
});

test("boxes unavailable on a host profile is not an error", () => {
  const v = pfvReadView(hostIn({ boxRuntime: "", boxUnavailableReason: "Docker is not installed" }));
  const runs = byId(v, "runs");
  assert.deepStrictEqual(runs.lines, [
    "On this machine: runs as you, with your files, your network and your credentials",
    "Boxes are unavailable on this machine: Docker is not installed.",
  ]);
  assert.ok(!runs.tone && runs.toneAt === undefined, "muted, never red");
  // Switch to box would be a control the user cannot use: hidden, and the Runs in block says why.
  assert.ok(!byId(v, "canreach").switchToBox);
  assert.strictEqual(byId(v, "canreach").edit, false);
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
  assert.strictEqual(a.fix, "`agent-box clone fetch -base master gitops-clone fix-drift` overwrites box/fix-drift");
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
  // The base environment only counts on host; "inherit" is the default there too.
  assert.strictEqual(pfvAdvancedCount({ base: "clean", runtime: "box" }), 0);
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
