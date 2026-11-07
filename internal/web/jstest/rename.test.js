"use strict";
// Inline rename in the sidebar swaps the name for an input. finish() used to leave
// the input in place and count on refreshSessions() to repaint the row, but the
// keyed reconcile (reconcile.js) keeps a row whose signature has not changed. So
// after Escape, an unchanged name or a failed save, the row kept a dead input and no
// name until a reload (#169 F02). app.js needs a DOM, so startRename is cut out of
// the source and run with stand-ins, as credentials-refetch.test.js does.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const APP = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");

function fn(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

// A parent node that swaps children the way the DOM does. Removing the focused
// input fires its blur, as Chrome does, so the Escape-then-blur path runs too.
function renameIn({ name = "old", label = name, nested = false, patch = async () => ({}) } = {}) {
  const calls = { patch: [], retitle: [], toast: [], refresh: 0 };
  const box = { focused: null };
  const parent = {
    child: null,
    replaceChild(next, prev) {
      assert.strictEqual(this.child, prev, "replaceChild: prev is not the current child");
      this.child = next; next.parentNode = this; prev.parentNode = null;
      if (box.focused === prev) { box.focused = null; if (prev.onblur) prev.onblur(); }
    },
  };
  const span = { textContent: label, parentNode: parent };
  parent.child = span;
  const ctx = vm.createContext({
    document: {
      createElement: () => ({ value: "", parentNode: null, focus() { box.focused = this; }, select() {} }),
    },
    api: async (p, opts) => { calls.patch.push([p, JSON.parse(opts.body)]); return patch(); },
    retitleTerminal: (id, v) => calls.retitle.push([id, v]),
    showToast: (msg) => calls.toast.push(msg),
    refreshSessions: () => { calls.refresh++; },
  });
  new vm.Script("let editing = false;\n" + fn(APP, "startRename") + "\nglobalThis.startRename = startRename;").runInContext(ctx);
  ctx.startRename({ isConnected: true, classList: { contains: (c) => nested && c === "nested" } }, span, { id: "s1", name });
  const input = parent.child;
  assert.notStrictEqual(input, span, "the input replaced the name");
  const key = (k) => input.onkeydown({ key: k, preventDefault() {} });
  const settle = () => new Promise((r) => setImmediate(r));
  const editing = () => new vm.Script("editing").runInContext(ctx);
  return { calls, parent, span, input, key, settle, editing };
}

test("rename then Escape puts the old name back and saves nothing", async () => {
  const t = renameIn();
  t.input.value = "typed-then-cancelled";
  t.key("Escape");
  await t.settle();
  assert.strictEqual(t.parent.child, t.span, "the name is back in the row");
  assert.strictEqual(t.span.textContent, "old");
  assert.deepStrictEqual(t.calls.patch, [], "the blur from removing the input did not commit");
  assert.strictEqual(t.editing(), false);
  assert.strictEqual(t.calls.refresh, 1);
});

test("rename then Enter shows the new name", async () => {
  const t = renameIn();
  t.input.value = "  audit-renamed ";
  t.key("Enter");
  await t.settle();
  assert.strictEqual(t.parent.child, t.span);
  assert.strictEqual(t.span.textContent, "audit-renamed");
  assert.deepStrictEqual(t.calls.patch, [["/api/sessions/s1", { name: "audit-renamed" }]]);
  assert.deepStrictEqual(t.calls.retitle, [["s1", "audit-renamed"]]);
  assert.strictEqual(t.calls.refresh, 1);
});

test("the new name shows while the save is in flight", async () => {
  let land;
  const t = renameIn({ patch: () => new Promise((r) => { land = r; }) });
  t.input.value = "audit-renamed";
  t.key("Enter");
  assert.strictEqual(t.parent.child, t.span);
  assert.strictEqual(t.span.textContent, "audit-renamed", "no flash of the old name during the round trip");
  land({ name: "audit-renamed" });
  await t.settle();
  assert.strictEqual(t.span.textContent, "audit-renamed");
});

test("the row shows the name the server stored (it caps names at 64 runes)", async () => {
  const t = renameIn({ patch: async () => ({ name: "capped" }) });
  t.input.value = "capped-plus-what-the-server-cut";
  t.key("Enter");
  await t.settle();
  assert.strictEqual(t.span.textContent, "capped");
});

test("a nested shell row keeps its shell N label", async () => {
  const t = renameIn({ name: "", label: "shell 1", nested: true });
  t.input.value = "build-watch";
  t.key("Enter");
  await t.settle();
  assert.strictEqual(t.span.textContent, "shell 1");
  assert.strictEqual(t.calls.patch.length, 1, "the name is still saved");
});

test("a failed save keeps the old name, and says so", async () => {
  const t = renameIn({ patch: async () => { throw new Error("409 name taken"); } });
  t.input.value = "taken";
  t.key("Enter");
  await t.settle();
  assert.strictEqual(t.parent.child, t.span);
  assert.strictEqual(t.span.textContent, "old");
  assert.deepStrictEqual(t.calls.toast, ["Rename failed: 409 name taken"]);
  assert.deepStrictEqual(t.calls.retitle, []);
});

test("Enter with the name unchanged, or blank, puts the name back without a save", async () => {
  for (const v of ["old", "   "]) {
    const t = renameIn();
    t.input.value = v;
    t.key("Enter");
    await t.settle();
    assert.strictEqual(t.parent.child, t.span, JSON.stringify(v));
    assert.strictEqual(t.span.textContent, "old");
    assert.deepStrictEqual(t.calls.patch, []);
  }
});

test("blur commits like Enter, once", async () => {
  const t = renameIn();
  t.input.value = "by-blur";
  t.input.onblur();
  t.input.onblur();
  await t.settle();
  assert.strictEqual(t.span.textContent, "by-blur");
  assert.strictEqual(t.calls.patch.length, 1);
});
