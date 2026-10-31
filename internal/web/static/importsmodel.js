"use strict";

// What the profile editor's "CLAUDE.md imports" row shows. Pure, no DOM: the
// row's wording and its enabled/hidden rules are readable and testable here.
// The data is GET /api/bundle/imports (an ImportSet); the page script
// (app.js) fetches it and paints what importsRowState says.

// importsBoxLabel: a box path with the box home written as ~.
function importsBoxLabel(boxPath) {
  const p = String(boxPath || "");
  return p === "/home/agent" ? "~" : p.startsWith("/home/agent/") ? "~" + p.slice("/home/agent".length) : p;
}

// importsNames: each file's name, or its ~ box path when another file in the
// set has the same name, so two notes.md stay tellable apart.
function importsNames(files) {
  const count = {};
  for (const x of files) count[x.name] = (count[x.name] || 0) + 1;
  return files.map((x) => (count[x.name] > 1 ? importsBoxLabel(x.box) : x.name));
}

// importsAccountInherits: whether boxes of this account get the user's
// personal config. The default account does; a named account does only when
// its inherit_personal is set. An account the list does not carry is not
// assumed to refuse: the row stays usable rather than lock on a stale list.
function importsAccountInherits(account, accounts) {
  if (!account || account === "default") return true;
  const a = (accounts || []).find((x) => x.name === account);
  return !a || !!a.inherit_personal || !!a.default;
}

// importsRowState: o = {tier: "box"|"host", account, accounts, set, failed, off}.
// Returns {hidden, disabled, checked, list, warn, refused}. checked is the
// switch as the user sees it (on unless off); the host tier hides the row (no
// box, nothing to give). set is null before the first answer or after a
// failure; failed tells the two apart.
function importsRowState(o) {
  const out = { hidden: o.tier !== "box", disabled: false, checked: !o.off, list: "", warn: "", refused: "" };
  if (out.hidden) return out;
  if (!importsAccountInherits(o.account, o.accounts)) {
    out.disabled = true;
    out.list = "This account does not use your personal config, so its boxes get no CLAUDE.md imports.";
    return out;
  }
  if (!o.set) {
    if (o.failed) out.list = "Could not read your CLAUDE.md imports.";
    return out;
  }
  const files = Array.isArray(o.set.files) ? o.set.files : [];
  const refused = Array.isArray(o.set.refused) ? o.set.refused : [];
  const names = importsNames(files).join(", ");
  if (o.off) {
    out.list = files.length ? "Not given to the box: " + names : "Your CLAUDE.md imports no files.";
    return out;
  }
  out.list = files.length ? "Your CLAUDE.md imports: " + names + " (read-only in the box)" : "Your CLAUDE.md imports no files.";
  out.warn = files.flatMap((x) => (Array.isArray(x.warnings) ? x.warnings : [])).join("\n");
  out.refused = refused.map((r) => "Left out: " + r.import + " (" + r.reason + ")").join("\n");
  return out;
}

// importsOffToSend: whether a save writes imports_off:true. A usable row is
// the checkbox's word (unchecked = off). A hidden or disabled row has no say,
// so it keeps the stored value, the same rule as replyableToSend.
function importsOffToSend(rowUsable, checked, stored) {
  return rowUsable ? !checked : !!stored;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { importsRowState, importsOffToSend };
}
