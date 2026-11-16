"use strict";
// First run without Docker (UI plan 7, #177 Task 11; DD§A11/B10, DC§9): step 2 keeps the D18 copy and adds a
// muted reason (a text node from /api/meta box_unavailable_reason), a "How to install Docker" link, and a
// disabled "Create box profile". With Docker the screen is as before; the phone never builds this node.
// app.js needs a browser, so emptyFirstEl is cut out of its source and run over a tiny fake DOM.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const APP = read("app.js");

function fnSource(src, name) {
  const m = new RegExp("(?:async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

function page({ boxRuntime = "", reason = "", daemon = "", metaLoaded = true, desktop = true } = {}) {
  const document = { createElement: (t) => new Node(t, document) };
  const opened = [];
  const ctx = vm.createContext({
    document, console, boxRuntime, boxUnavailableReason: reason, boxDaemonReason: daemon, metaLoaded,
    drawerDesktop: () => desktop,
    openSpawnDialog: () => opened.push("spawn"), openSettings: (t) => opened.push("settings:" + t), $: () => ({ click() { opened.push("new-profile"); } }),
  });
  new vm.Script(read("credentials.js").match(/function setDisabledReason[\s\S]*?\n}\n/)[0], { filename: "setDisabledReason" }).runInContext(ctx);
  vm.runInContext(["emptyEl", "emptyFirstEl", "emptyStateKey"].map((n) => fnSource(APP, n)).join("\n"), ctx);
  return { ctx, opened };
}
const all = (n, out = []) => { out.push(n); for (const c of n.children) all(c, out); return out; };
const byClass = (root, cls) => all(root).filter((n) => n.className.split(/\s+/).includes(cls));
const step2 = (root) => byClass(root, "empst-step")[1];

test("with Docker the screen is the D18 one: found, an enabled button, no reason, no link", () => {
  const { ctx, opened } = page({ boxRuntime: "runc" });
  const s = step2(ctx.emptyFirstEl());
  assert.ok(s.textContent.includes("Needs Docker (found)."));
  const btn = all(s).find((n) => n.tag === "button");
  assert.strictEqual(btn.textContent, "Create box profile");
  assert.ok(!btn.disabled && btn.attrs["aria-disabled"] === undefined);
  assert.strictEqual(all(s).filter((n) => n.tag === "a").length, 0);
  assert.strictEqual(byClass(s, "empst-why").length, 0);
  btn.onclick();
  assert.deepStrictEqual(opened, ["settings:manage", "new-profile"]);
});

test("without Docker: D18 copy kept, muted reason text, install link, disabled button carrying the reason", () => {
  const reason = "Docker is not installed";
  const { ctx, opened } = page({ reason });
  const s = step2(ctx.emptyFirstEl());
  assert.ok(s.textContent.includes("Needs Docker (not found)."));
  const why = byClass(s, "empst-why")[0];
  assert.ok(why.textContent.includes(reason));
  assert.strictEqual(byClass(why, "empst-reason")[0].textContent, reason);
  const a = all(s).find((n) => n.tag === "a");
  assert.strictEqual(a.textContent, "How to install Docker");
  assert.strictEqual(a.href, "https://docs.docker.com/engine/install/");
  assert.strictEqual(a.target, "_blank");
  assert.strictEqual(a.rel, "noopener noreferrer");
  assert.ok(a.className.split(/\s+/).includes("link"));
  const btn = all(s).find((n) => n.tag === "button");
  assert.strictEqual(btn.textContent, "Create box profile");
  assert.strictEqual(btn.attrs["aria-disabled"], "true");
  assert.strictEqual(btn.attrs.title, reason);
  btn.onclick && btn.onclick();
  assert.deepStrictEqual(opened, [], "a disabled button opens nothing");
});

test("a reason is text, never markup, and an empty reason draws no reason line", () => {
  const evil = "<img src=x onerror=1>";
  const { ctx } = page({ reason: evil });
  const r = byClass(ctx.emptyFirstEl(), "empst-reason")[0];
  assert.strictEqual(r.textContent, evil);
  assert.strictEqual(all(ctx.emptyFirstEl()).filter((n) => n.tag === "img").length, 0);
  const none = step2(page({ reason: "" }).ctx.emptyFirstEl());
  assert.strictEqual(byClass(none, "empst-reason").length, 0);
  assert.ok(all(none).find((n) => n.tag === "a"), "the link stays");
  assert.strictEqual(all(none).find((n) => n.tag === "button").attrs.title, undefined, "no invented title");
});

test("before /api/meta answers nothing about Docker is claimed", () => {
  const s = step2(page({ metaLoaded: false, reason: "x" }).ctx.emptyFirstEl());
  assert.ok(s.textContent.includes("Needs Docker."));
  assert.strictEqual(byClass(s, "empst-why").length, 0);
});

test("the phone path builds none of the new elements", () => {
  const s = step2(page({ reason: "Docker is not installed", desktop: false }).ctx.emptyFirstEl());
  assert.strictEqual(byClass(s, "empst-why").length, 0);
  assert.strictEqual(all(s).filter((n) => n.tag === "a").length, 0);
  assert.ok(!all(s).find((n) => n.tag === "button").attrs["aria-disabled"]);
});

test("the repaint key changes when the reason does", () => {
  const k = (reason) => page({ reason }).ctx.emptyStateKey({ kind: "first", profiles: [] });
  assert.notStrictEqual(k("a"), k("b"));
});

// #154: the layer exists but its docker daemon cannot be used (/api/meta box_daemon_reason). Step 2 keeps
// "found" (Docker is installed) and its enabled button (a profile can still be made), and says why a box
// cannot start right now, as a text node. No install link: Docker is installed.
test("a box layer whose daemon cannot be used: found, an enabled button, the reason as text", () => {
  const { ctx, opened } = page({ boxRuntime: "runc", daemon: "Docker is not running" });
  const s = step2(ctx.emptyFirstEl());
  assert.ok(s.textContent.includes("Needs Docker (found)."));
  const why = byClass(s, "empst-why")[0];
  assert.strictEqual(byClass(why, "empst-reason")[0].textContent, "Boxes can't start right now: Docker is not running.");
  assert.strictEqual(all(s).filter((n) => n.tag === "a").length, 0, "no install link for an installed docker");
  const btn = all(s).find((n) => n.tag === "button");
  assert.ok(!btn.disabled && btn.attrs["aria-disabled"] === undefined);
  btn.onclick();
  assert.deepStrictEqual(opened, ["settings:manage", "new-profile"]);
  const evil = byClass(page({ boxRuntime: "runc", daemon: "<img src=x onerror=1>" }).ctx.emptyFirstEl(), "empst-reason")[0];
  assert.strictEqual(evil.textContent, "Boxes can't start right now: <img src=x onerror=1>.");
  for (const none of [page({ boxRuntime: "runc" }), page({ boxRuntime: "runc", daemon: "x", metaLoaded: false }), page({ boxRuntime: "runc", daemon: "x", desktop: false })]) {
    assert.strictEqual(byClass(none.ctx.emptyFirstEl(), "empst-why").length, 0);
  }
  // No layer: the no-layer reason is the one shown, the daemon one is not added.
  const noLayer = byClass(page({ boxRuntime: "", reason: "Docker is not installed", daemon: "Docker is not running" }).ctx.emptyFirstEl(), "empst-reason");
  assert.deepStrictEqual(noLayer.map((n) => n.textContent), ["Docker is not installed"]);
});

test("the repaint key changes when the daemon reason does, with a layer only", () => {
  const k = (boxRuntime, daemon) => page({ boxRuntime, daemon }).ctx.emptyStateKey({ kind: "first", profiles: [] });
  assert.notStrictEqual(k("runc", "Docker is not running"), k("runc", ""));
  assert.strictEqual(k("", "Docker is not running"), k("", ""));
});
