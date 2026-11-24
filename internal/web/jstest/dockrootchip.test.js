// The explorer's root header chip (P4.10, founder pick B 2026-11-23: a short word, the rest in its title): "worktree" when the
// tree's root is in a linked worktree (the status reply's worktreeOf), "clone" when it is a clone-mode session's workspace.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { rootChipFor } = require("../static/dockrootchip.js");

const STATIC = path.join(__dirname, "..", "static");
const short = (p) => p.replace(/^\/home\/u(?=\/|$)/, "~");

test("a main checkout or a plain folder: no chip", () => {
  assert.strictEqual(rootChipFor({ root: "/home/u/p", status: { state: "ok", branch: "main" }, session: { cwd: "/home/u/p" } }), null);
  assert.strictEqual(rootChipFor({ root: "/home/u/p", status: null, session: null }), null);
  assert.strictEqual(rootChipFor({ root: "", status: { worktreeOf: "/home/u/p" } }), null);
});

test("a linked worktree: \"worktree\", its main checkout and branch in the title", () => {
  const c = rootChipFor({ root: "/home/u/p-wt", status: { state: "ok", branch: "feat/x", worktreeOf: "/home/u/p" }, short });
  assert.deepStrictEqual(c, { text: "worktree", title: "Linked worktree of ~/p\nBranch: feat/x" });
  const d = rootChipFor({ root: "/home/u/p-wt/sub", status: { state: "ok", worktreeOf: "/home/u/p" }, short });
  assert.deepStrictEqual(d, { text: "worktree", title: "Linked worktree of ~/p" }, "a detached HEAD or no branch known: no branch line");
});

test("a clone-mode session's workspace: \"clone\", its source and branch in the title; not after a re-root elsewhere", () => {
  const session = { cwd: "/home/u/.clones/p-1", clone_of: "/home/u/p" };
  assert.deepStrictEqual(rootChipFor({ root: "/home/u/.clones/p-1", status: { branch: "box/x" }, session, short }), { text: "clone", title: "Clone of ~/p\nBranch: box/x" });
  assert.deepStrictEqual(rootChipFor({ root: "/home/u/.clones/p-1/docs", status: null, session, short }), { text: "clone", title: "Clone of ~/p" });
  assert.strictEqual(rootChipFor({ root: "/home/u/.clones/p-10", status: null, session, short }), null, "by whole components");
  assert.strictEqual(rootChipFor({ root: "/home/u/other", status: null, session, short }), null);
});

test("both: the worktree wins (it is the checkout on screen)", () => {
  const session = { cwd: "/home/u/.clones/p-1", clone_of: "/home/u/p" };
  const c = rootChipFor({ root: "/home/u/.clones/p-1/.claude/worktrees/a", status: { worktreeOf: "/home/u/.clones/p-1" }, session, short });
  assert.strictEqual(c.text, "worktree");
});

test("strings only: a non-string worktreeOf, clone_of or branch is ignored", () => {
  assert.strictEqual(rootChipFor({ root: "/w", status: { worktreeOf: { x: 1 } } }), null);
  assert.strictEqual(rootChipFor({ root: "/w", status: null, session: { cwd: "/w", clone_of: 7 } }), null);
  assert.deepStrictEqual(rootChipFor({ root: "/w", status: { worktreeOf: "/m", branch: ["x"] } }), { text: "worktree", title: "Linked worktree of /m" });
});

test("the chip's markup sits in the root button between the name and the path; it is painted by text only", () => {
  const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
  assert.match(html, /<span id="dock-root-name" class="dk-root-name"><\/span><span id="dock-root-chip" class="chip dk-root-chip" hidden><\/span><span id="dock-root-path" class="dk-root-path"><\/span>/);
  const js = fs.readFileSync(path.join(STATIC, "dockrootchip.js"), "utf8");
  assert.doesNotMatch(js, /innerHTML|insertAdjacentHTML|outerHTML/);
  const dkc = fs.readFileSync(path.join(STATIC, "dockchanges.js"), "utf8");
  const body = dkc.slice(dkc.indexOf("function dkcPaintRoot("), dkc.indexOf("\n}\n", dkc.indexOf("function dkcPaintRoot(")));
  assert.match(body, /if \(typeof dockRootChipPaint === "function"\) dockRootChipPaint\(root\);/);
  const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /\.dk-root-chip\s*\{[^}]*flex:\s*none/, "the word never shrinks: the path gives way (founder pick B)");
  assert.match(css, /\.dk-root-chip\[hidden\]\s*\{\s*display:\s*none/);
});
