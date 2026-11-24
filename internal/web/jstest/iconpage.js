"use strict";
// iconpage.js: the page's own icon scripts (icons.js, iconmap.js, svgicon.js) as one source string, for a test that
// runs a page script in a vm context. `const` at line start becomes `var`, so the names are properties of the context
// (the browser's classic scripts share one lexical scope; a vm context's `const` is not on the context object).
const fs = require("node:fs");
const path = require("node:path");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");
const ICON_SRC = ["icons.js", "iconmap.js", "svgicon.js"].map((f) => read(f).replace(/^const /gm, "var ")).join("\n");

module.exports = { ICON_SRC };
