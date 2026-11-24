"use strict";
// iconpage.js: the page's own icon scripts (icons.js, iconmap.js, svgicon.js) as one source string, for a test that
// runs a page script in a vm context. `const` at line start becomes `var`, so the names are properties of the context
// (the browser's classic scripts share one lexical scope; a vm context's `const` is not on the context object).
const fs = require("node:fs");
const path = require("node:path");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");
const ICON_SRC = ["icons.js", "iconmap.js", "svgicon.js"].map((f) => read(f).replace(/^const /gm, "var ")).join("\n");

// iconStubs(make): the page globals a test that fakes the DOM gives a script that draws icons: iconmap.js whole, and a
// uiIcon reduced to what a test reads (the glyph's name as data-icon, the width, the classes). `make()` returns the
// test's own fake <svg> node.
function iconStubs(make) {
  return {
    ...require("../static/iconmap.js"),
    uiIcon: (name, o) => {
      const e = make();
      e.setAttribute("data-icon", name);
      e.setAttribute("width", String((o && o.size) || 14));
      e.setAttribute("class", o && o.cls ? "ui-icon " + o.cls : "ui-icon");
      if (o && o.label) e.setAttribute("aria-label", o.label);
      return e;
    },
  };
}

module.exports = { ICON_SRC, iconStubs };
