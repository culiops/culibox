"use strict";
// Settings > VM folders (#70 Task 13): the pure model and add-card state, pinned word by word to the M9 copy index.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { vmfModel, vmfAddState, vmfCheckFrom, vmfRemoveCopy, vmfAddHelp, VMF_HELPER, vmfPhoneModel, vmfFootModel } = require("../static/vmfolders.js");

const row = (p, o) => Object.assign({ path: "/Users/me" + p, display: "~" + p, mode: "rw", origin: "Added by you", used_by: [], running: 0, pending: "" }, o);
const APPLIED = {
  state: "applied", added: 0, removed: 0, switched: 0, data_root: "~/.local/share/agent-box", running_boxed: ["culibox"], apply: null, list_error: "",
  rows: [
    row("/code/culibox", { origin: "Workspace of profile culibox; culibox-clone copies it", used_by: ["culibox", "culibox-clone"] }),
    row("/code/infra", { origin: "A boxed session ran here; no profile uses it", running: 1 }),
    row("/box-clones", { origin: "Where culibox-clone keeps its copies", used_by: ["culibox-clone"] }),
    row("/notes/runbooks", { mode: "ro", origin: "Context path of profile ops", used_by: ["ops"] }),
  ],
};

test("M9.1 applied: strip with a dot, 4 rows then the data row, count 4, the helper", () => {
  const m = vmfModel(APPLIED);
  assert.deepStrictEqual(m.strip, { dot: true, status: "Applied", detail: "The box VM sees only these 4 folders and CuliBox data." });
  assert.strictEqual(m.banner, null);
  assert.strictEqual(m.title, "Shared folders");
  assert.strictEqual(m.count, 4);
  assert.strictEqual(m.rows.length, 5);
  assert.strictEqual(m.rows[4].data, true);
  assert.strictEqual(m.rows[4].display, "CuliBox data");
  assert.strictEqual(m.rows[4].origin, "Box homes and the harness bundle, from ~/.local/share/agent-box. Never your config or credentials.");
  assert.strictEqual(m.rows[4].modeText, "Set by CuliBox");
  assert.strictEqual(m.rows[4].usedText, "Every box");
  assert.strictEqual(m.under, VMF_HELPER);
  assert.strictEqual(m.under, "Changes take effect when the box VM restarts. A folder that does not exist yet can be listed; boxes see it once you create it.");
  assert.strictEqual(m.headAddDisabled, false);
});

test("used by: profile names, else the running sessions, else No profile (empty, painted muted)", () => {
  const m = vmfModel(APPLIED);
  assert.deepStrictEqual(m.rows.slice(0, 4).map((r) => r.usedText), ["culibox, culibox-clone", "1 running session", "culibox-clone", "ops"]);
  assert.strictEqual(vmfModel({ state: "applied", rows: [row("/x", { running: 3 }), row("/y")] }).rows.map((r) => r.usedText).slice(0, 2).join("|"), "3 running sessions|");
  assert.deepStrictEqual(m.rows.map((r) => r.seg).slice(0, 4), ["rw", "rw", "rw", "ro"]);
});

test("M9.2 whole home: no strip, the banner, Proposed folders; the body counts the folders", () => {
  const m = vmfModel(Object.assign({}, APPLIED, { state: "whole_home" }));
  assert.strictEqual(m.strip, null);
  assert.strictEqual(m.banner.title, "The box VM can see your whole home folder");
  assert.ok(m.banner.body.includes("the 4 folders your boxes use."), m.banner.body);
  assert.strictEqual(m.banner.body, "It shares `~` read & write, so a box that broke out of its container could change any file in it, like `~/.zshrc`. Apply the list below and the VM sees only the 4 folders your boxes use.");
  assert.strictEqual(m.banner.muted, "Nothing changes until you apply: sessions still start as before.");
  assert.strictEqual(m.banner.action, "Apply (restart box VM)");
  assert.strictEqual(m.banner.terminal, "or, in a terminal, `agent-box box share apply`");
  assert.strictEqual(m.title, "Proposed folders");
  assert.strictEqual(m.subtitle, "Made from your profiles and the folders boxed sessions ran in");
  assert.strictEqual(m.under, VMF_HELPER);
});

const PENDING = {
  state: "pending", added: 1, removed: 1, switched: 0, data_root: "~/.local/share/agent-box", running_boxed: ["culibox", "infra-audit"], apply: null, list_error: "",
  rows: [
    row("/code/culibox", { used_by: ["culibox"] }),
    row("/code/newproj", { pending: "added" }),
    row("/notes/runbooks", { mode: "ro", origin: "Context path of profile ops", used_by: ["ops"], pending: "removed" }),
  ],
};

test("M9.3 pending: strip without a dot, only the non-zero parts, the Apply button, the sessions line", () => {
  const m = vmfModel(PENDING);
  assert.deepStrictEqual(m.strip, { dot: false, status: "Changes not applied yet", detail: "1 added, 1 removed. The box VM still sees the old list.", action: "Apply (restart box VM)" });
  assert.strictEqual(m.note, "Applying restarts the box VM, about 20 s. The 2 sessions running in boxes end; host sessions keep running.");
  const [, added, removed] = m.rows;
  assert.strictEqual(added.chip, "Added · not applied");
  assert.strictEqual(removed.chip, "Removed · not applied");
  assert.strictEqual(removed.seg, null);
  assert.strictEqual(removed.modeText, "Read only");
  assert.strictEqual(removed.action, "Undo");
  assert.strictEqual(removed.origin, "Context path of profile ops · ops sessions are refused once applied");
  assert.strictEqual(m.count, 2, "a removed-pending row is not counted (the frame: 5 rows, chip 4)");
});

test("pending detail lists added, removed, switched in that order and skips zeros; switched is the new copy", () => {
  const d = (a, r, s) => vmfModel({ state: "pending", added: a, removed: r, switched: s, rows: [] }).strip.detail;
  assert.strictEqual(d(0, 2, 3), "2 removed, 3 switched. The box VM still sees the old list.");
  assert.strictEqual(d(1, 1, 1), "1 added, 1 removed, 1 switched. The box VM still sees the old list.");
  assert.strictEqual(d(0, 0, 1), "1 switched. The box VM still sees the old list.");
});

test("the sessions line with none running says so; with one it is singular", () => {
  const n = (names) => vmfModel(Object.assign({}, PENDING, { running_boxed: names })).note;
  assert.strictEqual(n([]), "Applying restarts the box VM, about 20 s. No box sessions are running.");
  assert.strictEqual(n(["a"]), "Applying restarts the box VM, about 20 s. The 1 session running in a box ends; host sessions keep running.");
});

test("while the VM restarts the header Add folder waits and the helper is gone (M9.7)", () => {
  const m = vmfModel(Object.assign({}, APPLIED, { apply: { state: "running" } }));
  assert.strictEqual(m.headAddDisabled, true);
  assert.strictEqual(m.under, "");
});

test("M9.5: the remove question exists only for a folder a profile uses, and says what the copy index says", () => {
  const m = vmfModel(APPLIED);
  const c = vmfRemoveCopy(m.rows[0]);
  assert.strictEqual(c.title, "Remove `~/code/culibox`?");
  assert.strictEqual(c.body, "culibox and culibox-clone use it. Once you apply, their sessions are refused until you add it back.");
  assert.strictEqual(c.note, "Files in the folder are not touched.");
  assert.strictEqual(vmfRemoveCopy(m.rows[1]), null, "only a running session: no profile, so no question");
  assert.strictEqual(vmfRemoveCopy(vmfModel({ state: "applied", rows: [row("/z")] }).rows[0]), null);
  assert.strictEqual(vmfRemoveCopy(vmfModel({ state: "applied", rows: [row("/z", { used_by: ["ops"] })] }).rows[0]).body,
    "ops uses it. Once you apply, its sessions are refused until you add it back.");
});

test("vmfAddState: nothing typed, nothing to add; a plain path is valid", () => {
  assert.deepStrictEqual(vmfAddState("", "rw", null), { invalid: false, error: "", note: "", addDisabled: true });
  assert.deepStrictEqual(vmfAddState("~/code/x", "rw", null), { invalid: false, error: "", note: "", addDisabled: false });
  assert.deepStrictEqual(vmfAddState("~/code/x", "rw", { kind: "ok" }), { invalid: false, error: "", note: "", addDisabled: false });
});

test("vmfAddState: a refusal is invalid with the server's text; Add stays enabled as drawn (M9.4a, M9.4c)", () => {
  const refused = vmfCheckFrom({ ok: false, code: "refused", error: "`~/.ssh` holds keys and sockets, so it can never be shared with the box VM." }, []);
  const s = vmfAddState("~/.ssh", "rw", refused);
  assert.strictEqual(s.invalid, true);
  assert.strictEqual(s.error, "`~/.ssh` holds keys and sockets, so it can never be shared with the box VM.");
  assert.strictEqual(s.addDisabled, false);
  const ro = vmfAddState("~/notes/runbooks/drafts", "rw", vmfCheckFrom({ ok: false, code: "rw_in_ro", error: "needs write access" }, []));
  assert.strictEqual(ro.invalid, true);
  assert.strictEqual(ro.addDisabled, false);
});

test("vmfAddState: already covered is a muted note, not an error, and disables Add (M9.4b)", () => {
  const check = vmfCheckFrom({ ok: true, result: { covered_by: "/Users/me/code/culibox", absorbed: [] } }, APPLIED.rows);
  assert.deepStrictEqual(check, { kind: "covered", display: "~/code/culibox", mode: "rw" });
  assert.deepStrictEqual(vmfAddState("~/code/culibox/docs", "rw", check),
    { invalid: false, error: "", note: "Already covered by `~/code/culibox`, which boxes can read and write. Nothing to add.", addDisabled: true });
  const ro = vmfCheckFrom({ ok: true, result: { covered_by: "/users/me/notes/RUNBOOKS" } }, APPLIED.rows);
  assert.strictEqual(vmfAddState("~/notes/runbooks/a", "ro", ro).note, "Already covered by `~/notes/runbooks`, which boxes can read. Nothing to add.", "case-folded match, read only wording");
  assert.strictEqual(vmfCheckFrom({ ok: true, result: { covered_by: "", absorbed: [] } }, []).kind, "ok");
  assert.strictEqual(vmfCheckFrom(null, []), null);
});

test("the help line: the default, and M9.9b for an add that comes from a refused New session", () => {
  assert.strictEqual(vmfAddHelp(""), "A folder on this Mac. Not your whole home, and not a folder that holds keys, like `~/.ssh`, `~/.aws` or `~/Library`.");
  assert.strictEqual(vmfAddHelp("spawn"), "From New session: a box couldn’t use this folder because the box VM can’t see it. After you add it, Apply restarts the VM; then start the session again.");
});

test("vmfolders.js: no native dialogs, no innerHTML (a path is user input), and it is a classic script with a module guard", () => {
  const src = fs.readFileSync(path.join(__dirname, "../static/vmfolders.js"), "utf8");
  assert.doesNotMatch(src, /\b(alert|confirm|prompt)\(/);
  assert.doesNotMatch(src, /innerHTML/);
  assert.match(src, /typeof module !== "undefined" && module\.exports/);
});

test("one folder reads in the singular (new copy): the Applied strip and the whole-home banner", () => {
  const one = { state: "applied", rows: [row("/code/culibox")], running_boxed: [], apply: null };
  assert.strictEqual(vmfModel(one).strip.detail, "The box VM sees only this folder and CuliBox data.");
  const whole = vmfModel(Object.assign({}, one, { state: "whole_home" }));
  assert.ok(whole.banner.body.endsWith("Apply the list below and the VM sees only the folder your boxes use."), whole.banner.body);
});

const job = (state) => ({ id: 1, state, started_ms: 1, steps: [], error: "", detail: "" });

test("M9.7 a running job: card, no strip, no banner, no helper, table and header Add folder locked", () => {
  const m = vmfModel(Object.assign({}, PENDING, { apply: job("running") }));
  assert.strictEqual(m.card, true);
  assert.strictEqual(m.locked, true);
  assert.strictEqual(m.strip, null);
  assert.strictEqual(m.banner, null);
  assert.strictEqual(m.note, "");
  assert.strictEqual(m.under, "");
  assert.strictEqual(m.headAddDisabled, true);
  const whole = vmfModel(Object.assign({}, APPLIED, { state: "whole_home", apply: job("running") }));
  assert.strictEqual(whole.banner, null, "the whole-home banner gives way to the card");
});

test("M9.8 a failed job: the card replaces the strip, the table is enabled", () => {
  const m = vmfModel(Object.assign({}, PENDING, { apply: job("failed") }));
  assert.strictEqual(m.card, true);
  assert.strictEqual(m.locked, false);
  assert.strictEqual(m.strip, null);
  assert.strictEqual(m.under, "");
  assert.strictEqual(m.headAddDisabled, false);
});

test("a finished job is not a card: the strip is back", () => {
  const m = vmfModel(Object.assign({}, APPLIED, { apply: job("done") }));
  assert.strictEqual(m.card, false);
  assert.strictEqual(m.strip.status, "Applied");
  assert.strictEqual(m.under, VMF_HELPER);
});

// #70 Task 16: the phone's list, built from the same model (no copy of its own).
test("phone M9.1: a card per folder with path, origin, Used by, a two-way switch and a Remove; no table, no Apply", () => {
  const m = vmfPhoneModel(vmfModel(APPLIED));
  assert.strictEqual(m.rows.length, 5);
  const [a, b, , d, data] = m.rows;
  assert.strictEqual(a.display, "~/code/culibox");
  assert.strictEqual(a.origin, "Workspace of profile culibox; culibox-clone copies it");
  assert.strictEqual(a.usedLabel, "Used by");
  assert.strictEqual(a.usedText, "culibox, culibox-clone");
  assert.deepStrictEqual(a.seg, { value: "rw", items: [["rw", "Read & write"], ["ro", "Read only"]] });
  assert.strictEqual(a.action, "Remove");
  assert.strictEqual(b.usedText, "1 running session");
  assert.strictEqual(d.seg.value, "ro");
  assert.strictEqual(data.data, true);
  assert.strictEqual(data.seg, null);
  assert.strictEqual(data.action, "");
  assert.strictEqual(data.modeText, "Set by CuliBox");
  assert.strictEqual(m.rows.filter((r) => r.usedText === "").length, 0, "an empty Used by is the muted No profile");
});

test("phone M9.3: the removed-pending row shows its chip, plain text and Undo; the armed question is the M9.5 text", () => {
  const m = vmfPhoneModel(vmfModel(PENDING));
  const removed = m.rows[2];
  assert.strictEqual(removed.chip, "Removed · not applied");
  assert.strictEqual(removed.seg, null);
  assert.strictEqual(removed.modeText, "Read only");
  assert.strictEqual(removed.action, "Undo");
  assert.strictEqual(m.rows[1].chip, "Added · not applied");
  assert.strictEqual(vmfRemoveCopy(m.rows[0]).body, "culibox uses it. Once you apply, its sessions are refused until you add it back.");
});

test("phone: the status line, the note and the banner come from the page model", () => {
  const m = vmfPhoneModel(vmfModel(PENDING));
  assert.strictEqual(m.strip.status, "Changes not applied yet");
  assert.strictEqual(m.strip.action, undefined, "the footer carries Apply on the phone");
  assert.match(m.note, /^Applying restarts the box VM/);
  const w = vmfPhoneModel(vmfModel(Object.assign({}, APPLIED, { state: "whole_home" })));
  assert.strictEqual(w.banner.title, "The box VM can see your whole home folder");
  assert.strictEqual(w.banner.action, undefined);
});

test("phone footer: Apply only when pending or whole-home, disabled while a job runs, Add folder waits with it", () => {
  assert.deepStrictEqual(vmfFootModel(APPLIED, null, false), { apply: false, applyDisabled: false, addDisabled: false });
  assert.deepStrictEqual(vmfFootModel(PENDING, null, false), { apply: true, applyDisabled: false, addDisabled: false });
  assert.deepStrictEqual(vmfFootModel(Object.assign({}, APPLIED, { state: "whole_home" }), null, false), { apply: true, applyDisabled: false, addDisabled: false });
  assert.deepStrictEqual(vmfFootModel(PENDING, { state: "running" }, false), { apply: true, applyDisabled: true, addDisabled: true });
  assert.strictEqual(vmfFootModel(APPLIED, null, true).addDisabled, true, "an open add form");
});

test("phone footer and layout are wired in index.html and app.css", () => {
  const HTML = fs.readFileSync(path.join(__dirname, "../static/index.html"), "utf8");
  const CSS = fs.readFileSync(path.join(__dirname, "../static/app.css"), "utf8");
  assert.match(HTML, /<div class="dft" id="foot-vmfolders" hidden>[\s\S]*?data-close="settings-modal"[\s\S]*?id="vmf-foot-add"[\s\S]*?id="vmf-foot-apply"[^>]*>Apply \(restart box VM\)</);
  assert.doesNotMatch(CSS, /#settings-tabs \.t\[data-tab="vmfolders"\] \{ display: none; \}/, "the phone no longer hides the tab");
  assert.match(CSS, /#settings-modal:not\(\.as-page\) #vmfolders/);
});

test("the desktop page hides the phone's footer (#foot-vmfolders is in desktop.css's page-footer hide rule)", () => {
  const DESK = fs.readFileSync(path.join(__dirname, "../static/desktop.css"), "utf8");
  assert.match(DESK, /#page-host > #settings-modal\.as-page > #foot-vmfolders[^{]*\{ display: none; \}/);
});
