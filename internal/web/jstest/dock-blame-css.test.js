// The line blame's look (Files dock P6): the header after "Edited by" that gives way before it, the status bar item that is
// cut first, the inline text that only follows the line, the card. Desktop only.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
const rule = (sel) => {
  const m = css.match(new RegExp("(^|[}\\s,])" + sel.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "\\s*\\{([^}]*)\\}"));
  return m ? m[2] : "";
};

test("the header's markup: after Edited by, a real button, hidden until the script has an answer", () => {
  const html = read("index.html");
  const row = html.slice(html.indexOf('id="dock-crumbs"'), html.indexOf("</div>", html.indexOf('id="dock-crumbs"')));
  assert.ok(row.indexOf('id="dock-blame"') > row.indexOf('id="dock-edited"'), "Edited by · blame");
  assert.match(row, /<button id="dock-blame" class="dk-blame" type="button" hidden>/);
  for (const c of ["dk-blame-i", "dk-blame-who", "dk-blame-sep", "dk-blame-when", "dk-blame-short"]) assert.match(row, new RegExp('class="' + c + '"'));
});

test("the header gives way before Edited by, which never does; its forms are the script's data-form", () => {
  assert.match(rule(".dk-edited"), /flex:\s*none/);
  assert.match(rule(".dk-blame"), /flex:\s*0 1 auto/);
  assert.match(rule(".dk-blame"), /min-width:\s*0/);
  assert.match(css, /\.dk-blame\[hidden\]\s*\{\s*display:\s*none/);
  assert.match(rule(".dk-blame-who"), /text-overflow:\s*ellipsis/);
  assert.match(rule(".dk-blame-short"), /display:\s*none/, "the short form is off in the wide one");
  assert.match(css, /\.dk-blame\[data-form="short"\] \.dk-blame-short/);
  assert.match(css, /\.dk-blame\[data-form="icon"\] :is\(\.dk-blame-who, \.dk-blame-sep, \.dk-blame-when\)/);
});

test("the status bar item is cut first and is a quiet button", () => {
  assert.match(rule(".dk-sb-blame"), /flex:\s*1 1 0/, "only the room the branch, Saved and the right side leave");
  assert.match(rule(".dk-sb-blame"), /min-width:\s*0/);
  assert.match(rule(".dk-sb-blame"), /text-overflow:\s*ellipsis/);
  assert.match(css, /\.dk-sb-blame\[hidden\]\s*\{\s*display:\s*none/);
  assert.match(rule(".dk-sb-r"), /flex:\s*none/, "the position and language keep their place");
});

test("the inline text is muted over Monaco's token colour, with no font or spacing of its own (Monaco cuts it into spans)", () => {
  const r = rule("#drawer .monaco-editor .dk-blame-inline");
  assert.match(r, /color:\s*var\(--muted\)/);
  assert.doesNotMatch(r, /font(-family|-size)?:|margin|padding|letter-spacing/, "the width the fit measured must be the width drawn");
  assert.match(read("editorblame.js"), /content: BL_GAP \+ short/, "the gap is in the text");
});

test("the card: a kit panel, the SHA in the code font, names isolated, no amber/green/red", () => {
  assert.match(rule(".dk-bcard"), /position:\s*fixed/);
  assert.match(rule(".dk-bcard"), /background:\s*var\(--panel\)/);
  assert.match(css, /\.dk-bcard\[hidden\]\s*\{\s*display:\s*none/);
  assert.match(rule(".dk-bcard-sha"), /font-family:\s*var\(--font-code\)/);
  for (const sel of [".dk-blame-who", ".dk-sb-blame", ".dk-bcard-who", ".dk-bcard-mail", ".dk-bcard-sum"]) {
    assert.match(rule(sel), /unicode-bidi:\s*isolate/, sel + " is isolated");
  }
  const blameRules = css.match(/\.dk-(b|sb-b)[a-z-]*[^{]*\{[^}]*\}/g).join("\n");
  assert.doesNotMatch(blameRules, /--amber|--ok|--red/);
  assert.doesNotMatch(read("app.css"), /dk-blame|dk-bcard/, "desktop only");
});
