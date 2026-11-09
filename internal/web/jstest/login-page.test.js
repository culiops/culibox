"use strict";
// login.html's states (UI plan 1, #171, audit F73 and F74): a busy submit, the wrong-credential
// line, the expired line, and the #token= link that must not flash the empty form. The page's
// two inline scripts (one in <head>, one at the end of <body>) run in a vm against a minimal
// fake document: the repo has no jsdom. The fake only knows what the page uses, and it logs
// every write to the DOM so "nothing from the URL is ever shown" is checked, not assumed.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const html = fs.readFileSync(path.join(__dirname, "..", "static", "login.html"), "utf8");
const [headHtml, bodyHtml] = html.split("</head>");
const scriptsOf = (part) => [...part.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
const headScripts = scriptsOf(headHtml);
const bodyScripts = scriptsOf(bodyHtml);

// The markup is the source of the page's fixed strings: the fake elements start from it, so a
// reworded line in login.html is what these tests read, not a copy of it kept here.
const tagOf = (id) => {
  const m = new RegExp("<(\\w+)([^>]*\\bid=\"" + id + "\"[^>]*)>((?:[^<]|<span[^>]*></span>)*)</\\1>").exec(bodyHtml);
  assert.ok(m, "login.html has no #" + id);
  return { attrs: m[2], text: m[3].replace(/<span[^>]*><\/span>/g, "") };
};

class El {
  constructor(name, log, init) {
    this.name = name;
    this._log = log;
    this._attrs = {};
    this.value = "";
    this.disabled = false;
    this.readOnly = false;
    this._hidden = !!(init && /\bhidden\b/.test(init.attrs));
    this._text = init ? init.text : "";
    this._listeners = {};
    this.submitted = 0;
    this._cls = new Set();
    this.classList = {
      add: (c) => { this._log.push([this.name, "+class", c]); this._cls.add(c); },
      remove: (c) => { this._log.push([this.name, "-class", c]); this._cls.delete(c); },
      contains: (c) => this._cls.has(c),
    };
  }
  get hidden() { return this._hidden; }
  set hidden(v) { this._log.push([this.name, "hidden", v]); this._hidden = !!v; }
  get textContent() { return this._text; }
  set textContent(v) { this._log.push([this.name, "textContent", v]); this._text = String(v); }
  setAttribute(k, v) { this._log.push([this.name, "@" + k, v]); this._attrs[k] = String(v); }
  getAttribute(k) { return k in this._attrs ? this._attrs[k] : null; }
  removeAttribute(k) { this._log.push([this.name, "-@" + k, null]); delete this._attrs[k]; }
  addEventListener(type, fn) { (this._listeners[type] = this._listeners[type] || []).push(fn); }
  dispatch(type) {
    const ev = { type, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
    (this._listeners[type] || []).forEach((fn) => fn.call(this, ev));
    return ev;
  }
  submit() { this.submitted++; this.valueAtSubmit = this._pw ? this._pw.value : undefined; }
  set innerHTML(v) { throw new Error("innerHTML written on " + this.name); }
  set outerHTML(v) { throw new Error("outerHTML written on " + this.name); }
  insertAdjacentHTML() { throw new Error("insertAdjacentHTML on " + this.name); }
}

// One page load. The head script runs before the body exists (getElementById/querySelector see
// nothing), then the body is "parsed" and its script runs, as in the browser.
function load({ search = "", hash = "", headOnly = false } = {}) {
  const log = [];
  let parsed = false;
  const classes = new Set();
  const el = {
    err: new El("err", log, tagOf("err")),
    expired: new El("expired", log, tagOf("expired")),
    "err-field": new El("err-field", log, tagOf("err-field")),
    signing: new El("signing", log, tagOf("signing")),
    password: new El("password", log),
  };
  const button = new El("button", log, { attrs: "", text: /<button type="submit">([^<]*)<\/button>/.exec(bodyHtml)[1] });
  const form = new El("form", log);
  form._pw = el.password;
  form.querySelector = (sel) => (sel === "button" || sel === "button[type=submit]" || sel === "[type=submit]" ? button : null);
  const location = { search, hash, pathname: "/login" };
  const replaced = [];
  const history = {
    replaceState(state, title, url) {
      replaced.push(url);
      if (!String(url).includes("#")) location.hash = "";
    },
  };
  const win = { _listeners: {}, addEventListener(t, fn) { (this._listeners[t] = this._listeners[t] || []).push(fn); } };
  const doc = {
    documentElement: {
      classList: {
        add: (c) => classes.add(c),
        remove: (c) => classes.delete(c),
        contains: (c) => classes.has(c),
        toggle: (c, on) => (on === undefined ? !classes.has(c) : on) ? classes.add(c) : classes.delete(c),
      },
    },
    getElementById: (id) => (parsed ? el[id] || null : null),
    querySelector: (sel) => (parsed && sel === "form" ? form : null),
    querySelectorAll: () => [],
    addEventListener() {},
    write() { throw new Error("document.write"); },
  };
  const ctx = vm.createContext({ document: doc, location, history, window: win, URLSearchParams, console });
  ctx.globalThis = ctx;
  const run = (src, name) => new vm.Script(src, { filename: name }).runInContext(ctx);
  headScripts.forEach((s, i) => run(s, "login.html:head" + i));
  if (!headOnly) {
    parsed = true;
    bodyScripts.forEach((s, i) => run(s, "login.html:body" + i));
  }
  const pageshow = (persisted) => (win._listeners.pageshow || []).forEach((fn) => fn({ type: "pageshow", persisted }));
  // What a reader of the card sees from the two notice lines (the CSS hides the rest in token mode).
  const lines = () => [el.err, el.expired].filter((e) => !e.hidden).map((e) => e.textContent);
  return { el, button, form, classes, location, replaced, log, pageshow, lines };
}

test("the page has one head script and one body script", () => {
  assert.strictEqual(headScripts.length, 1, "the token-mode class is set by a <head> script, before first paint");
  assert.strictEqual(bodyScripts.length, 1);
  assert.ok(html.indexOf("<script>") < html.indexOf("</head>"), "the head script is in <head>");
});

test("both notice lines start hidden in the markup, so a plain visit never flashes one", () => {
  assert.match(tagOf("err").attrs, /\bhidden\b/);
  assert.match(tagOf("expired").attrs, /\bhidden\b/);
  assert.strictEqual(load().lines().length, 0);
});

test("the card's fixed copy", () => {
  assert.strictEqual(tagOf("err").text, "Wrong password or token.");
  assert.strictEqual(tagOf("expired").text, "Your login expired, sign in again.");
  assert.strictEqual(tagOf("signing").text, "Signing in…");
});

// Rewritten on purpose (UI plan 7, task 12, M8.8b): the wrong-credential line used to sit in a
// boxed block ABOVE the form only. On desktop it now sits UNDER the field; the phone keeps the
// block above the form, so both exist and the script shows both for ?e=1.
test("the expired line sits above the field label; the error is under the field (desktop) and above the form (phone)", () => {
  const at = (s) => bodyHtml.indexOf(s);
  assert.ok(at('id="expired"') > 0 && at('id="expired"') < at("<label"), "expired line is above the label");
  assert.ok(at('id="err"') < at("<form"), "the phone's error block stays above the form");
  assert.ok(at('id="err-field"') > at('id="password"') && at('id="err-field"') < at("<button"), "the desktop error is under the field, above the button");
  assert.match(tagOf("err-field").attrs, /\bhidden\b/, "starts hidden");
  assert.match(tagOf("err-field").attrs, /class="field-error"/);
  assert.strictEqual(tagOf("err-field").text, "Wrong password or token.");
});

test("?e=1 shows the error under the field too, marks the field invalid, and nothing else does", () => {
  const p = load({ search: "?e=1" });
  assert.strictEqual(p.el["err-field"].hidden, false);
  assert.strictEqual(p.el.password.classList.contains("is-invalid"), true);
  assert.strictEqual(p.el.password.getAttribute("aria-invalid"), "true");
  assert.strictEqual(p.el.password.getAttribute("aria-describedby"), "err-field");
  for (const search of ["", "?reason=expired", "?e=0"]) {
    const q = load({ search });
    assert.strictEqual(q.el["err-field"].hidden, true, search);
    assert.strictEqual(q.el.password.classList.contains("is-invalid"), false, search);
    assert.strictEqual(q.el.password.getAttribute("aria-invalid"), null, search);
  }
});

test("typing after a wrong guess clears the red border (the error is about the old value)", () => {
  const p = load({ search: "?e=1" });
  p.el.password.dispatch("input");
  assert.strictEqual(p.el.password.classList.contains("is-invalid"), false);
  assert.strictEqual(p.el.password.getAttribute("aria-invalid"), null);
});

test("checking: the field dims (is-disabled) and Back restores it", () => {
  const p = load();
  p.form.dispatch("submit");
  assert.strictEqual(p.el.password.classList.contains("is-disabled"), true);
  p.pageshow(true);
  assert.strictEqual(p.el.password.classList.contains("is-disabled"), false);
});

test("token mode shows a kit .spin beside 'Signing in…'", () => {
  assert.match(bodyHtml, /<p class="signing" id="signing"><span class="spin" aria-hidden="true"><\/span>Signing in…<\/p>/);
});

test("the M8.8b look is desktop-only: every visual rule sits in one min-width: 701px block", () => {
  const style = [...headHtml.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1]).join("\n").replace(/\/\*[\s\S]*?\*\//g, "");
  const i = style.indexOf("@media (min-width: 701px)");
  assert.ok(i > 0, "a desktop media block");
  const block = style.slice(i);
  assert.match(block, /label\s*\{[^}]*font-weight:\s*var\(--w-medium\)[^}]*\}/, "label weight");
  assert.match(block, /label\s*\{[^}]*font-size:\s*var\(--t-sm\)/, "label size");
  assert.match(block, /label\s*\{[^}]*color:\s*var\(--fg\)/, "label colour");
  assert.match(block, /input\[type=password\]\s*\{[^}]*border-color:\s*var\(--faint\)/, "--faint border");
  assert.match(block, /\.is-invalid\s*\{[^}]*border-color:\s*var\(--red\)/, "red border");
  assert.match(block, /\.is-disabled\s*\{[^}]*opacity/, "dimmed while checking");
  assert.match(block, /#err\s*\{[^}]*display:\s*none/, "the phone's block is hidden on desktop");
  assert.match(block, /\.field-error\s*\{[^}]*color:\s*var\(--red\)/);
  assert.doesNotMatch(block, /font-weight:\s*(7|8|9)\d\d|font-weight:\s*bold/, "weight <= 600");
  const outside = style.slice(0, i);
  assert.match(outside, /\.field-error,\s*\.signing \.spin\s*\{[^}]*display:\s*none/, "the under-field error and the spinner are off by default (phone unchanged)");
  assert.doesNotMatch(outside, /--faint|--red\b/, "phone rules do not use the desktop colours");
});

test("?e=1 shows the wrong-credential line", () => {
  assert.deepStrictEqual(load({ search: "?e=1" }).lines(), ["Wrong password or token."]);
});

test("?reason=expired shows the expired line", () => {
  assert.deepStrictEqual(load({ search: "?reason=expired" }).lines(), ["Your login expired, sign in again."]);
});

test("any other reason, or a lookalike, shows nothing", () => {
  for (const search of ["?reason=other", "?reason=", "?reason=Expired", "?reason=expired%20", "?reason=expiredx", "?e=0", "?e=true", "?e=", "?e=11"]) {
    assert.deepStrictEqual(load({ search }).lines(), [], search);
  }
});

test("a wrong guess on the expired page comes back as ?e=1: only the error shows", () => {
  assert.deepStrictEqual(load({ search: "?e=1&reason=expired" }).lines(), ["Wrong password or token."]);
});

test("nothing from the URL reaches the DOM, and no HTML is ever written", () => {
  const evil = "<img src=x onerror=alert(1)>";
  const p = load({ search: "?reason=" + encodeURIComponent(evil) + "&e=" + encodeURIComponent(evil), hash: "#x=" + evil });
  assert.deepStrictEqual(p.lines(), []);
  assert.deepStrictEqual(p.log, [], "no DOM write at all for an unrecognised value");
  // And with a recognised one: only the page's own fixed strings are written (here: a hidden flag).
  const q = load({ search: "?reason=expired" });
  assert.deepStrictEqual(q.log, [["expired", "hidden", false]]);
  for (const s of [...headScripts, ...bodyScripts]) {
    assert.doesNotMatch(s, /innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\(|new Function/);
    assert.doesNotMatch(s, /textContent\s*=\s*[^"'\n;]*location|\.value\s*=\s*[^;\n]*location\.search/);
  }
});

test("submit: 'Checking…', disabled, aria-busy; the password field is readOnly, never disabled", () => {
  const p = load();
  assert.strictEqual(p.button.textContent, "Log in");
  p.el.password.value = "hunter2";
  const ev = p.form.dispatch("submit");
  assert.strictEqual(ev.defaultPrevented, false, "the submit itself goes through");
  assert.strictEqual(p.button.textContent, "Checking…");
  assert.strictEqual(p.button.disabled, true);
  assert.strictEqual(p.button.getAttribute("aria-busy"), "true");
  assert.strictEqual(p.el.password.readOnly, true);
  assert.strictEqual(p.el.password.disabled, false, "a disabled field is not submitted");
  assert.strictEqual(p.el.password.value, "hunter2", "the value is untouched");
});

test("a second submit while busy is swallowed (no double POST while the server sleeps)", () => {
  const p = load();
  assert.strictEqual(p.form.dispatch("submit").defaultPrevented, false);
  assert.strictEqual(p.form.dispatch("submit").defaultPrevented, true);
});

test("pageshow with persisted (Back after submit) restores the idle form", () => {
  const p = load();
  p.form.dispatch("submit");
  p.pageshow(true);
  assert.strictEqual(p.button.textContent, "Log in");
  assert.strictEqual(p.button.disabled, false);
  assert.strictEqual(p.button.getAttribute("aria-busy"), null);
  assert.strictEqual(p.el.password.readOnly, false);
  assert.strictEqual(p.form.dispatch("submit").defaultPrevented, false, "and it can be submitted again");
});

test("a plain pageshow (the first one, on load) changes nothing", () => {
  const p = load();
  p.form.dispatch("submit");
  p.pageshow(false);
  assert.strictEqual(p.button.textContent, "Checking…");
  assert.strictEqual(p.button.disabled, true);
});

test("#token=: the head script marks token mode before any element exists", () => {
  const p = load({ hash: "#token=abc", headOnly: true });
  assert.strictEqual(p.classes.has("token-mode"), true);
});

test("#token=: the fragment is stripped, the token is the password, the form is submitted once", () => {
  const p = load({ search: "?reason=expired", hash: "#token=abc_-9" });
  assert.strictEqual(p.classes.has("token-mode"), true);
  assert.deepStrictEqual(p.replaced, ["/login?reason=expired"], "fragment gone from the URL bar and history, query kept");
  assert.strictEqual(p.location.hash, "");
  assert.strictEqual(p.form.submitted, 1);
  assert.strictEqual(p.form.valueAtSubmit, "abc_-9");
});

test("#token= and Back: the persisted page shows the form again, not 'Signing in…'", () => {
  const p = load({ hash: "#token=abc" });
  p.pageshow(false);
  assert.strictEqual(p.classes.has("token-mode"), true, "the load-time pageshow must not un-hide the form");
  assert.strictEqual(p.el.password.value, "abc", "the load-time pageshow must not clear the token before it is submitted");
  p.pageshow(true);
  assert.strictEqual(p.classes.has("token-mode"), false);
});

test("Back to a persisted page clears the password field: a token must not come back in the masked box", () => {
  const p = load({ hash: "#token=abc" });
  assert.strictEqual(p.form.valueAtSubmit, "abc");
  p.pageshow(true);
  assert.strictEqual(p.el.password.value, "");
  // The same for a typed password submitted and then navigated back to.
  const q = load();
  q.el.password.value = "hunter2";
  q.form.dispatch("submit");
  q.pageshow(true);
  assert.strictEqual(q.el.password.value, "");
  assert.strictEqual(q.el.password.readOnly, false);
});

test("no token fragment: no token-mode class, no submit", () => {
  for (const hash of ["", "#", "#token=", "#xtoken=abc", "#token", "#other=1"]) {
    const p = load({ hash });
    assert.strictEqual(p.classes.has("token-mode"), false, JSON.stringify(hash));
    assert.strictEqual(p.form.submitted, 0, JSON.stringify(hash));
  }
});

test("the head and body scripts agree on what a token fragment is", () => {
  for (const hash of ["#token=abc", "#a=1&token=abc", "#token=abc&a=1", "#token=a%20b", "#token=", "#xtoken=abc", "#token=!!!", "#TOKEN=abc"]) {
    const p = load({ hash });
    assert.strictEqual(p.classes.has("token-mode"), p.form.submitted === 1, "head says " + p.classes.has("token-mode") + ", body submitted " + p.form.submitted + " for " + hash);
  }
});

test("a wrong token comes back as /login?e=1 with no fragment: the form and the error show, no 'Signing in…'", () => {
  const p = load({ search: "?e=1" });
  assert.strictEqual(p.classes.has("token-mode"), false);
  assert.deepStrictEqual(p.lines(), ["Wrong password or token."]);
  assert.strictEqual(p.form.submitted, 0);
});

// The CSS half of the no-flash promise: the class only helps if the style hides the card's
// contents and shows the one line. The fake DOM cannot run CSS, so read the rules.
test("token mode hides everything in the card but the 'Signing in…' line", () => {
  const style = [...headHtml.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1]).join("\n").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(style, /(^|\})\s*\.signing\s*\{[^}]*display:\s*none/, ".signing is hidden unless token mode");
  assert.match(style, /html\.token-mode\s+\.signing\s*\{[^}]*display:\s*block/, "and shown in token mode");
  assert.match(style, /html\.token-mode\s+\.card\s*>\s*:not\(\.signing\)\s*\{[^}]*display:\s*none/, "everything else in the card is hidden in token mode");
  assert.match(tagOf("signing").attrs, /class="signing"/);
});

test("a disabled button keeps its label legible on a phone (primitives.css would strike it through)", () => {
  const style = [...headHtml.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1]).join("\n").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(style, /button:disabled\s*\{[^}]*text-decoration:\s*none/);
});
