"use strict";
// The desktop page host (S4): Settings, Analytics and Audit re-parent their
// existing <dialog> into #page-host instead of opening modally. The DOM half
// needs a browser, so it is pinned at the source level the way
// settings-profiles-refetch.test.js does; the decision itself is pure.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// pageModeFor asks mobileshell's breakpoint, as the page does at runtime.
global.mobileActive = require("../static/mobileshell.js").mobileActive;
const { PAGE_IDS, pageModeFor } = require("../static/pagehost.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function body(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("PAGE_IDS is the three page-capable dialogs", () => {
  assert.deepEqual(PAGE_IDS, ["settings-modal", "analytics-modal", "audit-page"]);
});

test("pageModeFor: every page id is a page on desktop, a modal at 700px and below", () => {
  for (const id of PAGE_IDS) {
    assert.strictEqual(pageModeFor(id, 1280), true, id + " @1280");
    assert.strictEqual(pageModeFor(id, 701), true, id + " @701");
    assert.strictEqual(pageModeFor(id, 700), false, id + " @700");
    assert.strictEqual(pageModeFor(id, 390), false, id + " @390");
  }
});

test("pageModeFor: the other dialogs stay modal on desktop", () => {
  for (const id of ["spawn-modal", "bug-modal", "ask-modal", "palette", "sheet-modal", "", undefined]) {
    assert.strictEqual(pageModeFor(id, 1280), false, String(id));
  }
});

test("openDialog offers the dialog to the page host before showModal", () => {
  const b = body(read("shell.js"), "openDialog");
  const p = b.indexOf("openPage("), s = b.indexOf("showModal(");
  assert.ok(p >= 0, "openDialog must call openPage(");
  assert.ok(s > p, "openPage( must come before showModal(");
});

// Review Focus 1: anything that means "show me a session / the panes" closes
// the page first, so the user lands on what they asked for.
const pageCloseHooks = [
  ["tabs.js", "openTerminal"], ["tabs.js", "openPaneView"], ["tabs.js", "applyCap"], ["tabs.js", "toggleMax"],
  ["app.js", "spawnShellIn"], ["app.js", "spawnSession"], ["app.js", "toggleDrawer"], ["app.js", "openDrawer"],
  ["tabs.js", "dockShellBesideParent"],
];
for (const [file, name] of pageCloseHooks) {
  test(name + " (" + file + ") closes an open page", () => {
    // Each one closes the page through closePageHolds (or reads closePage's answer itself, as spawnSession does).
    assert.match(body(read(file), name), /closePage(?:Holds)?\(/);
  });
}

test("the palette's Activity and Access entries close an open page", () => {
  const src = read("palette.js");
  for (const [k, kind] of [["activityView", "activity"], ["accessView", "access"]]) {
    assert.match(src, new RegExp(k + ':\\s*\\(\\)\\s*=>\\s*paletteOpenView\\("' + kind + '"\\)'), k);
  }
  assert.match(body(src, "paletteOpenView"), /closePageHolds\(/);
});

test("dialogsOpen counts modal dialogs only, never a page", () => {
  assert.match(read("app.js"), /const dialogsOpen = \(\) => document\.querySelectorAll\("dialog\[open\]:not\(\.as-page\)"\)\.length;/);
});

test("every dialog's close runs pageClosed before focusTerminal (the grid must be back to take focus)", () => {
  const b = body(read("shell.js"), "initDialogs");
  const pc = b.indexOf("pageClosed(d)"), ft = b.indexOf('addEventListener("close", focusTerminal)');
  assert.ok(pc >= 0 && ft > pc, "pageClosed(d) must be wired before focusTerminal");
});

test("a click on a page's empty area is not a backdrop click", () => {
  assert.match(body(read("shell.js"), "initDialogs"), /pressedBackdrop && e\.target === d && !d\.classList\.contains\("as-page"\)/);
});

test("crossing to mobile closes the page", () => {
  const src = read("app.js");
  const at = src.indexOf('window.matchMedia(mobileMediaQuery()).addEventListener("change"');
  assert.ok(at >= 0);
  // "force": the layout is leaving under the page, so the profile page's leave hook is not asked.
  assert.match(src.slice(at, at + 400), /closePage\("force"\)/);
});

test("the Audit link shows only when the server has an audit store", () => {
  assert.match(read("app.js"), /sideAudit\.hidden = !\(meta && meta\.audit\)/);
  assert.match(read("index.html"), /<button id="side-audit" type="button" hidden>Audit<\/button>\s*<button id="side-analytics"/);
});

test("#page-host sits after #drawer in #layout; pagehost.js loads after shell.js", () => {
  const html = read("index.html");
  const drawerEnd = html.indexOf("</aside>", html.indexOf('<aside id="drawer"'));
  const host = html.indexOf('<div id="page-host" hidden></div>');
  assert.ok(host > drawerEnd && host < html.indexOf('<aside id="steer-rail"'), "#page-host right after #drawer");
  assert.ok(html.indexOf('<script src="/pagehost.js">') > html.indexOf('<script src="/shell.js">'));
  assert.match(html, /<dialog id="audit-page" aria-label="Audit">/);
});

test("desktop.css hides the panes behind a page and makes the dialog fill the host", () => {
  const css = read("desktop.css");
  assert.match(css, /body\.page-open #grid, body\.page-open #drawer \{ display: none; \}/);
  assert.match(css, /#page-host \{ flex: 1; min-width: 0; display: flex; background: var\(--bg\); \}/);
  assert.match(css, /dialog\.as-page \{[^}]*position: static;[^}]*width: 100%; height: 100%;[^}]*border: 0;[^}]*display: flex; flex-direction: column;/);
  assert.match(css, /\.side-links \.on \{ color: var\(--fg\); font-weight: 600; \}/);
});

test("base CSS keeps #page-host off on mobile", () => {
  assert.match(read("app.css"), /#side-top, #side-foot, #drawer, #page-host \{ display: none; \}/);
});

test("with a page open, zoom and Ctrl+P stand down to the browser", () => {
  // The registry's allow table is what says so now (the per-listener
  // currentPage() guards are gone); the dispatcher passes ctx.pageOpen.
  const { shortcutFor } = require("../static/shortcuts.js");
  const ev = (code, key, mods) => Object.assign({ code, key, ctrlKey: true, metaKey: false, altKey: false, shiftKey: false }, mods);
  const ctx = { dialogOpen: false, pageOpen: true, inTerminal: false, inEditor: false, inInput: false, mobile: false };
  for (const e of [ev("Equal", "="), ev("Minus", "-"), ev("Digit0", "0"), ev("KeyP", "p")]) {
    assert.strictEqual(shortcutFor(e, false, ctx), null, e.key);
  }
  assert.match(read("app.js"), /shortcutContext\(.*!!currentPage\(\)/);
});

test("classnames HOOKS lists the page-<id> body classes for every PAGE_ID", () => {
  const src = fs.readFileSync(path.join(__dirname, "classnames.test.js"), "utf8");
  for (const id of PAGE_IDS) assert.ok(src.includes('"page-' + id + '"'), "page-" + id);
});

// --- the leave hook: a page with something to ask (the profile page's unsaved changes) is asked before it goes ---

// pagehost.js on a fake DOM: two page dialogs and the host, enough for openPage / closePage / the Esc handler.
function pageEnv() {
  const vm = require("node:vm");
  const mk = (id) => {
    const cls = new Set();
    return {
      id, open: false, hidden: false, cls,
      classList: { add: (c) => cls.add(c), remove: (c) => cls.delete(c), contains: (c) => cls.has(c) },
      before() {}, show() { this.open = true; }, close() { this.open = false; }, querySelector: () => null,
    };
  };
  const els = { "settings-modal": mk("settings-modal"), "analytics-modal": mk("analytics-modal"), "page-host": Object.assign(mk("page-host"), { appendChild() {} }) };
  const listeners = [];
  const body = { classList: { add() {}, remove() {} } };
  const ctx = vm.createContext({
    document: {
      getElementById: (id) => els[id] || null, createComment: () => ({ parentNode: {}, replaceWith() {} }),
      addEventListener: (ev, fn) => listeners.push([ev, fn]), body,
    },
    window: { innerWidth: 1280 }, mobileActive: global.mobileActive, requestAnimationFrame() {},
  });
  new vm.Script(read("pagehost.js"), { filename: "pagehost.js" }).runInContext(ctx);
  const esc = (taken) => {
    const ev = { key: "Escape", defaultPrevented: taken === true, prevented: false, preventDefault() { this.prevented = true; } };
    for (const [name, fn] of listeners) if (name === "keydown") fn(ev);
    return ev;
  };
  return { ctx, els, esc, run: (code) => new vm.Script(code).runInContext(ctx) };
}
const tick = () => new Promise((r) => setTimeout(r, 0));

test("pagehost closePage waits for the leave hook", async () => {
  const t = pageEnv();
  // No hook: shut at once, and the answer says so.
  t.ctx.openPage("settings-modal");
  assert.strictEqual(t.ctx.currentPage(), "settings-modal");
  assert.strictEqual(t.ctx.closePage(), true);
  assert.strictEqual(t.ctx.currentPage(), "");
  assert.strictEqual(t.ctx.closePage(), true, "nothing open is closed");

  // A hook that says stay.
  t.ctx.openPage("settings-modal");
  const seen = [];
  t.run("pageLeaveHook = (why) => { globalThis.seen.push(why); return globalThis.verdict; }");
  t.ctx.seen = seen;
  t.ctx.verdict = false;
  assert.strictEqual(t.ctx.closePage(), false);
  assert.strictEqual(t.ctx.currentPage(), "settings-modal", "it stays");
  assert.deepStrictEqual(seen, [undefined]);
  // A hook that answers true at once: shut by the time closePage returns (callers that do not wait rely on it).
  t.ctx.verdict = true;
  assert.strictEqual(t.ctx.closePage(), true);
  assert.strictEqual(t.ctx.currentPage(), "");

  // A hook that asks: closePage hands back the promise and the page stays until it is answered.
  t.ctx.openPage("settings-modal");
  let answer;
  t.ctx.verdict = new Promise((r) => { answer = r; });
  const pending = t.ctx.closePage();
  assert.ok(pending && typeof pending.then === "function");
  assert.strictEqual(t.ctx.currentPage(), "settings-modal", "open while the question is on screen");
  answer(false);
  assert.strictEqual(await pending, false);
  assert.strictEqual(t.ctx.currentPage(), "settings-modal", "Keep editing: it stays");
  t.ctx.verdict = Promise.resolve(true);
  assert.strictEqual(await t.ctx.closePage(), true);
  assert.strictEqual(t.ctx.currentPage(), "", "Discard: it closes");

  // "force" (the window crossed to a phone) closes whatever the hook answers: it is told, so it can end its edit, and
  // never asked. A hook that throws does not keep the page up either.
  t.ctx.openPage("settings-modal");
  t.ctx.verdict = false;
  seen.length = 0;
  assert.strictEqual(t.ctx.closePage("force"), true);
  assert.strictEqual(t.ctx.currentPage(), "");
  assert.deepStrictEqual(seen, ["force"]);
  t.ctx.openPage("settings-modal");
  t.run("pageLeaveHook = () => { throw new Error('boom'); }");
  assert.strictEqual(t.ctx.closePage("force"), true);
  assert.strictEqual(t.ctx.currentPage(), "");
});

test("closePageHolds: false (go on now, synchronously) when nothing is asked; true while asked, and the action runs only on Discard", async () => {
  const t = pageEnv();
  let ran = 0;
  const again = () => { ran++; };
  // No page open (a phone, or nothing shown): not held, the caller continues in the same tick.
  assert.strictEqual(t.ctx.closePageHolds(again), false);
  assert.strictEqual(ran, 0, "the caller runs its own code, not `again`");
  // A page with no hook, or a hook that says go at once: shut, not held.
  t.ctx.openPage("settings-modal");
  assert.strictEqual(t.ctx.closePageHolds(again), false);
  assert.strictEqual(t.ctx.currentPage(), "");
  t.ctx.openPage("settings-modal");
  t.run("pageLeaveHook = () => globalThis.verdict");
  t.ctx.verdict = true;
  assert.strictEqual(t.ctx.closePageHolds(again), false);
  assert.strictEqual(t.ctx.currentPage(), "");
  // Stay, answered at once: held, nothing runs.
  t.ctx.openPage("settings-modal");
  t.ctx.verdict = false;
  assert.strictEqual(t.ctx.closePageHolds(again), true);
  await tick();
  assert.strictEqual(ran, 0);
  assert.strictEqual(t.ctx.currentPage(), "settings-modal");
  // A question: held; Keep editing runs nothing, Discard closes the page and then runs the action.
  let answer;
  t.ctx.verdict = new Promise((r) => { answer = r; });
  assert.strictEqual(t.ctx.closePageHolds(again), true);
  answer(false);
  await tick();
  assert.strictEqual(ran, 0, "Keep editing");
  assert.strictEqual(t.ctx.currentPage(), "settings-modal");
  t.ctx.verdict = Promise.resolve(true);
  assert.strictEqual(t.ctx.closePageHolds(again), true);
  await tick();
  assert.strictEqual(ran, 1, "Discard");
  assert.strictEqual(t.ctx.currentPage(), "", "the page is shut before the action runs");
});

test("the Esc key asks the leave hook with why esc, and a hook that says stay keeps the page", async () => {
  const t = pageEnv();
  t.ctx.openPage("settings-modal");
  const seen = [];
  t.ctx.seen = seen;
  t.ctx.verdict = false;
  t.run("pageLeaveHook = (why) => { globalThis.seen.push(why); return globalThis.verdict; }");
  const ev = t.esc();
  assert.strictEqual(ev.prevented, true);
  assert.deepStrictEqual(seen, ["esc"]);
  assert.strictEqual(t.ctx.currentPage(), "settings-modal");
  t.ctx.verdict = true;
  t.esc();
  assert.strictEqual(t.ctx.currentPage(), "", "nothing wants it: the page closes");
  // A key something above already took (a field, an open list) is not the page's: the hook is not even asked.
  t.ctx.openPage("settings-modal");
  seen.length = 0;
  const taken = t.esc(true);
  assert.strictEqual(taken.prevented, false);
  assert.deepStrictEqual(seen, []);
  assert.strictEqual(t.ctx.currentPage(), "settings-modal");
});

test("another page opening asks the open page's hook, and opens after Discard", async () => {
  const t = pageEnv();
  t.ctx.openPage("settings-modal");
  t.ctx.verdict = false;
  t.run("pageLeaveHook = () => globalThis.verdict");
  assert.strictEqual(t.ctx.openPage("analytics-modal"), true, "handled: the caller must not open it as a modal");
  assert.strictEqual(t.ctx.currentPage(), "settings-modal", "Keep editing: Settings stays");
  let answer;
  t.ctx.verdict = new Promise((r) => { answer = r; });
  assert.strictEqual(t.ctx.openPage("analytics-modal"), true);
  assert.strictEqual(t.ctx.currentPage(), "settings-modal", "still Settings while it is asked");
  answer(true);
  await tick();
  assert.strictEqual(t.ctx.currentPage(), "analytics-modal", "Discard: the other page opens");
});

test("the profile page and the shell wire the guard through one function each", () => {
  const shell = read("shell.js");
  // The close buttons go through closePage for the open page; every other dialog closes as before.
  assert.match(body(shell, "initDialogs"), /btn\.onclick = \(\) => closeDialogFromUI\(btn\.dataset\.close\)/);
  assert.match(body(shell, "closeDialogFromUI"), /currentPage\(\) === id[^\n]*closePage\(\);\s*else closeDialog\(id\)/);
  // The Settings nav click and openSettings ask settingsLeave first, which is a no-op answer (true) off the desktop page.
  assert.match(body(shell, "initDialogs"), /const leave = settingsLeave\(b\.dataset\.tab\);\s*if \(leave === true\) selectSettingsTab\(b\.dataset\.tab\);/);
  assert.match(body(shell, "openSettings"), /const leave = settingsLeave\(tab\);\s*if \(leave !== true\) \{\s*leave\.then\(\(ok\) => \{ if \(ok\) openSettings\(tab, group\); \}\);\s*return;/);
  assert.match(body(shell, "settingsLeave"), /typeof pfvTabLeave === "function" \? pfvTabLeave\(tab \|\| "manage"\) : true/);
  // The profile page installs its hook; the old editor's entry points ask through it on the desktop page only.
  assert.match(read("profilepage.js"), /if \(typeof pageLeaveHook !== "undefined"\) pageLeaveHook = pfvPageLeave;/);
  const app = read("app.js");
  assert.match(body(app, "profilePageHolds"), /typeof pfvHold === "function" && pfvHold\(again\)/);
  assert.match(body(app, "editProfile"), /^\{\s*if \(profilePageHolds\(\(\) => editProfile\(name\)\)\) return;/);
  assert.match(body(app, "duplicateProfile"), /^\{\s*\/\/[^\n]*\n\s*if \(profilePageHolds\(duplicateProfile\)\) return;/);
  assert.match(app, /\$\("pf-new"\)\.addEventListener\("click", \(\) => \{\s*if \(profilePageHolds\(\(\) => \$\("pf-new"\)\.click\(\)\)\) return;/);
});
