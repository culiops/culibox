// The "Outside this tree · Reveal" hint (P4.5) in the breadcrumb row: static markup between the trail and "Edited by", and
// the rules that make it give way before "Edited by" (and P6's blame header after it) ever do.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const rule = (css, sel) => {
  const m = stripComments(css).match(new RegExp("(^|[}\\s])" + sel.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "\\s*\\{([^}]*)\\}"));
  return m ? m[2] : null;
};

test("the hint's markup: after the trail, before Edited by, hidden, its copy fixed and Reveal a real button", () => {
  const html = read("index.html");
  const row = html.slice(html.indexOf('id="dock-crumbs"'), html.indexOf("</div>", html.indexOf('id="dock-crumbs"')));
  const trail = row.indexOf('id="dock-trail"'), hint = row.indexOf('id="dock-outside"'), ed = row.indexOf('id="dock-edited"');
  assert.ok(trail > 0 && hint > trail && ed > hint, "trail · hint · Edited by");
  assert.match(row, /<span id="dock-outside" class="dk-outside" hidden><span class="dk-outside-t">Outside this tree<\/span><span aria-hidden="true"> · <\/span><button type="button" class="link dk-outside-go"( [^>]*)?>Reveal<\/button><\/span>/);
});

test("the hint gives way first: the trail shrinks, then the hint's words; Reveal and Edited by never do", () => {
  const css = read("desktop.css");
  assert.match(rule(css, ".dk-outside"), /flex:\s*0 1 auto/);
  assert.match(rule(css, ".dk-outside"), /min-width:\s*0/);
  assert.match(rule(css, ".dk-outside-t"), /text-overflow:\s*ellipsis/);
  assert.match(rule(css, ".dk-outside-t"), /min-width:\s*0/);
  assert.match(rule(css, ".dk-outside-go"), /flex:\s*none/);
  assert.match(rule(css, ".dk-edited"), /flex:\s*none/);
  assert.match(stripComments(css), /\.dk-outside\[hidden\]/, "hidden means not drawn (display: flex would beat the attribute)");
  assert.doesNotMatch(stripComments(read("app.css")), /dk-outside/, "desktop only");
});
