"use strict";

// The Advanced block's editor (spec 5.3, 5.5): the environment the agent starts with (on this machine only),
// the options that are not model or effort, extra command-line arguments, the environment variables and secrets,
// whether prompts can be answered from the sidebar and whether the CLAUDE.md imports go to the box. What a save
// writes is profileedit.js's (pfvMergeBlock "advanced"); this file draws the form, keeps what the user has typed
// and says what the client refuses before the PUT (a variable row that is not valid). It registers itself with
// profilepage.js (pfvRegisterEditor), so it loads after it.
//
// A control the user cannot use is not drawn, and the value behind it is kept: the starting environment on a
// box profile (said in one sentence), replies when the command is not claude or the instance has them off, the
// imports when the profile has no box, bundling is off or the account does not use the personal config. Whether a
// row is usable is decided when it is drawn and again when the values are merged, never stored in the values, so
// a /api/meta answer that lands while the form is open cannot make it look changed.
//
// Values (rec.values): { base, argsText, envRows: [row], options: { <key>: value }, repliesChecked, importsChecked }.
// options holds the options this block draws (those that apply to the command), a bool as true/false. A row is
// envrows.js's {key, value, kind: "literal" | "secret"} plus two marks of this form: removed (an existing row
// whose removal is pending until Save; Undo takes it back) and fresh (added in this edit, so Remove deletes it
// outright). Only the rows that are not removed are checked (rowError) and sent (rowsToConfig).
//
// Every top-level name here starts with pfa/PFA_ (the page scripts share one scope). Classic script:
// profilepage.js's helpers (pfvMk, pfvReg, pfvFieldBox, pfvFerr, pfvChanged, pfvPaint, ...), envrows.js, importsmodel.js,
// spawnform.js's isClaudeBinName and the globals of app.js (optionDefs through the context, optApplies,
// accountsCache, metaLoaded, bundleImports, repliesEnabled) are read at call time.

const PFA_BASES = [
  { value: "inherit", label: "Everything you have (inherit)" },
  { value: "clean", label: "Minimal, no secrets (clean)" },
  { value: "login", label: "Your login shell (login)" },
];
const PFA_BOX_BASE = "Environment it starts with only applies on this machine; a box always starts minimal.";
const PFA_ARGS_HINT = "One per line.";
const PFA_ENV_NONE_BOX = "None. The agent gets the box's minimal environment.";
const PFA_ENV_NONE_HOST = "None added on top of the starting environment.";
const PFA_SECRET_HINT = "Read from the credential store when it starts";

const pfaLines = (text) => String(text || "").split("\n").map((s) => s.trim()).filter(Boolean);
// pfaLive: the variable rows that count: not pending removal.
const pfaLive = (rows) => rows.filter((r) => r.removed !== true);

// pfaDefs: the options this block draws: every option the daemon defines except the Agent block's (model and
// effort), for the commands they apply to.
function pfaDefs(c) {
  const bin = typeof c.doc.bin === "string" ? c.doc.bin : "";
  return c.defs.filter((d) => d && typeof d.key === "string" && !PFE_AGENT_OPTIONS.includes(d.key) && optApplies(d, bin));
}

function pfaValues(c) {
  const d = c.doc;
  const stored = pfvStoredOptions(d);
  const options = {};
  for (const def of pfaDefs(c)) options[def.key] = def.type === "bool" ? stored[def.key] === true : typeof stored[def.key] === "string" ? stored[def.key] : "";
  return {
    base: typeof d.base === "string" && d.base !== "" ? d.base : "inherit",
    argsText: (Array.isArray(d.args) ? d.args.filter((x) => typeof x === "string") : []).join("\n"),
    envRows: envRows(d),
    options,
    repliesChecked: d.replyable === true,
    importsChecked: d.imports_off !== true,
  };
}

// pfaRepliesUsable: replies are claude-only, and the instance can switch them off (/api/meta); the old form's gate.
const pfaRepliesUsable = (c) => globalThis.repliesEnabled === true && isClaudeBinName(c.doc.bin);

// pfaImportsRow: importsmodel.js's rule for the imports row, from the page's own state. Before /api/meta has answered
// nothing is known and the row stays; once it has, "bundling off" (Ruling R8) and "no box layer" hide it like a host
// profile (nothing to give). hidden: not drawn. disabled: the account has no personal config; the row's sentence is said instead.
function pfaImportsRow(c, checked) {
  const known = typeof metaLoaded !== "undefined" && metaLoaded === true;
  const bundleOff = known && !(typeof bundleImports !== "undefined" && bundleImports === true);
  const accounts = typeof accountsCache !== "undefined" && Array.isArray(accountsCache) ? accountsCache : [];
  const tier = c.tier === "host" || !c.boxAvailable || bundleOff ? "host" : "box";
  const st = importsRowState({ tier, account: c.doc.account || "", accounts, set: null, failed: false, off: !checked });
  return { hidden: st.hidden, disabled: st.disabled, text: st.disabled ? st.list : "" };
}

// pfaMerge: the values pfvMergeBlock is given. The starting environment is the host's only; a bool option that was
// never stored and is off is not written (the server offers a stored bool at spawn, an absent one not).
function pfaMerge(rec, c) {
  const v = rec.values;
  const stored = pfvStoredOptions(c.doc);
  const options = {};
  for (const def of pfaDefs(c)) {
    const val = v.options[def.key];
    if (def.type === "bool") options[def.key] = val === true ? true : stored[def.key] === undefined ? "" : false;
    else options[def.key] = typeof val === "string" ? val : "";
  }
  const imports = pfaImportsRow(c, v.importsChecked);
  const out = {
    args: pfaLines(v.argsText),
    envRows: pfaLive(v.envRows),
    options,
    replies: { usable: pfaRepliesUsable(c), checked: v.repliesChecked },
    imports: { usable: !imports.hidden && !imports.disabled, checked: v.importsChecked },
  };
  // "inherit" is the default and is stored as no base at all, unless the profile already says it.
  if (c.tier === "host") out.base = v.base === "inherit" && c.doc.base !== "inherit" ? "" : v.base;
  return out;
}

// pfaCheck: what the client refuses before the PUT: a variable row the server would refuse (envrows.js rowError).
function pfaCheck(rec) {
  const text = rowError(pfaLive(rec.values.envRows));
  return text ? { field: "env", text } : null;
}

// pfaArea: the arguments' textarea.
function pfaArea(rec, key, value, onInput) {
  const a = pfvMk("textarea", "pfv-ctl pfv-ctl-mono pfv-area");
  a.id = "pfv-f" + ++pfvUid;
  a.value = value;
  a.setAttribute("placeholder", "--some-flag");
  a.setAttribute("autocomplete", "off");
  a.setAttribute("spellcheck", "false");
  a.oninput = () => onInput(a.value);
  return pfvReg(rec, key, a);
}

// pfaCheckbox: a switch with its words, and under them what a refusal of its field says.
function pfaCheckbox(rec, key, label, detail, checked, onChange) {
  const lab = pfvMk("label", "pfv-check");
  const cb = pfvMk("input");
  cb.type = "checkbox";
  cb.checked = checked;
  cb.onchange = () => onChange(cb.checked);
  pfvReg(rec, key, cb);
  const t = pfvMk("span", "pfv-check-t", label);
  if (detail) t.append(pfvMk("span", "pfv-check-d", detail));
  lab.append(cb, t);
  const err = pfvFerr(rec, key);
  if (!err) return lab;
  const w = pfvMk("div", "pfv-field");
  w.append(lab, pfvMk("p", "pfv-ferr", err));
  return w;
}

// pfaOptionField: one option as the old form draws it: a bool is a checkbox, a choice of up to four a segmented control,
// anything wider a select with "(not set)".
function pfaOptionField(rec, def) {
  const v = rec.values;
  const key = "options." + def.key;
  const label = def.label || def.key;
  if (def.type === "bool") return pfaCheckbox(rec, key, label, "", v.options[def.key] === true, (on) => { v.options[def.key] = on; pfvChanged(rec, key); });
  const now = typeof v.options[def.key] === "string" ? v.options[def.key] : "";
  const choices = pfvWithValue(Array.isArray(def.choices) ? def.choices : [], now);
  const pick = (val) => { v.options[def.key] = val; pfvChanged(rec, key); };
  if (choices.length > 0 && choices.length <= 4) return pfvFieldBox(rec, key, label, pfvSegCtl(rec, key, choices, now, pick), [], "fieldset");
  const none = { value: "", label: "(not set)" };
  return pfvFieldBox(rec, key, label, pfvSelect(rec, key, [none].concat(choices.map((x) => ({ value: x, label: x }))), now, false, pick), []);
}

// pfaInput: one of a variable row's mono inputs.
function pfaInput(row, prop, label, placeholder, onInput) {
  const i = pfvMk("input", "pfv-ctl pfv-ctl-mono");
  i.type = "text";
  i.value = row[prop];
  i.setAttribute("aria-label", label);
  i.setAttribute("placeholder", placeholder);
  i.setAttribute("autocomplete", "off");
  i.setAttribute("spellcheck", "false");
  i.oninput = () => { row[prop] = i.value; onInput(); };
  return i;
}

// pfaEnvRow: a row in the page's row grammar (the kind column, the content, Remove or Undo at the right; the mockup's
// tpl-env and tpl-secret). A variable is a name and a value; a secret is a name only, with where its value comes from.
// A row pending removal is its words, struck through like the Cloud rows, with Undo. {node, name, btn}.
function pfaEnvRow(row, onInput, toggle) {
  const secret = row.kind === "secret";
  const gone = row.removed === true;
  const node = pfvMk("div", "pfv-row" + (gone ? " pfv-removed" : ""));
  node.append(pfvMk("span", "pfv-k", secret ? "Secret" : "Variable"));
  const kv = pfvMk("div", "pfv-kv");
  let name = null;
  if (gone) {
    kv.append(pfvMk("span", "pfv-mono", row.key.trim()));
    if (!secret && row.value.trim()) kv.append(pfvMk("span", "pfv-mono", row.value.trim()));
  } else {
    name = pfaInput(row, "key", secret ? "Secret name" : "Variable name", "NAME", onInput);
    kv.append(name);
    kv.append(secret ? pfvMk("span", "pfv-hint", PFA_SECRET_HINT) : pfaInput(row, "value", "Value", "value", onInput));
  }
  const what = (secret ? "secret" : "variable") + (row.key.trim() ? " " + row.key.trim() : "");
  const btn = pfvMk("button", "pfv-rmbtn", gone ? "Undo" : "Remove");
  btn.type = "button";
  btn.setAttribute("aria-label", (gone ? "Undo removing " : "Remove ") + (row.key.trim() ? what : "this " + what));
  btn.onclick = toggle;
  node.append(kv, btn);
  return { node, name, btn };
}

// pfaEnvField: the variables and secrets as rows, with the two "+ Add" links under them. The rows are rec.values.envRows
// itself, so a form drawn again keeps what was typed. Only this list is drawn again when a row is added, removed or
// brought back: the other controls (and the focus in them) stay.
function pfaEnvField(rec, host) {
  const v = rec.values;
  const none = host ? PFA_ENV_NONE_HOST : PFA_ENV_NONE_BOX;
  const rows = pfvMk("div", "pfv-rows");
  const adds = pfvMk("div", "pfv-adds");
  const ctl = pfvMk("div", "pfv-env");
  ctl.append(rows, adds);
  const box = pfvFieldBox(rec, "env", "Environment variables", ctl, [], "fieldset");
  // The server names a secret on its own field; both are this list's.
  const secretsErr = pfvFerr(rec, "secrets");
  const secretsNode = secretsErr ? pfvMk("p", "pfv-ferr", secretsErr) : null;
  if (secretsNode) box.append(secretsNode);
  let names = [];
  let btns = [];
  // A row was typed in or taken, put or brought back: a refusal about the list is stale.
  const changed = () => {
    box.clear();
    if (secretsNode) {
      secretsNode.hidden = true;
      rec.ferrs = rec.ferrs.filter((e) => !pfvFieldIs(e.field, "secrets"));
    }
    pfvPaint();
  };
  // Remove on a row that was only just added deletes it; on a stored row it is pending until Save, and Undo brings it back.
  const toggle = (row) => {
    const i = v.envRows.indexOf(row);
    if (row.fresh === true) v.envRows.splice(i, 1);
    else row.removed = row.removed !== true;
    paint();
    changed();
    const to = btns[Math.min(i, btns.length - 1)] || adds.children[0];
    if (to) to.focus();
  };
  const paint = () => {
    names = [];
    btns = [];
    if (!v.envRows.length) {
      rows.replaceChildren(pfvMk("p", "pfv-hint", none));
      return;
    }
    rows.replaceChildren(...v.envRows.map((row) => {
      const r = pfaEnvRow(row, changed, () => toggle(row));
      names.push(r.name);
      btns.push(r.btn);
      return r.node;
    }));
  };
  paint();
  for (const [label, kind] of [["+ Add variable", "literal"], ["+ Add secret", "secret"]]) {
    const b = pfvDrawLink(label, "");
    b.onclick = () => {
      v.envRows.push({ key: "", value: "", kind, fresh: true });
      paint();
      changed();
      names[names.length - 1].focus();
    };
    adds.append(b);
  }
  return box;
}

function pfaDraw(rec, c) {
  const v = rec.values;
  const host = c.tier === "host";
  const out = [];
  if (host) {
    const stored = c.doc.base;
    const known = PFA_BASES.map((b) => b.value);
    const choices = typeof stored === "string" && stored !== "" && !known.includes(stored) ? PFA_BASES.concat([{ value: stored, label: stored }]) : PFA_BASES;
    const sel = pfvSelect(rec, "base", choices, v.base, false, (val) => { v.base = val; pfvChanged(rec, "base"); });
    out.push(pfvFieldBox(rec, "base", "Environment it starts with", sel, []));
  }
  for (const def of pfaDefs(c)) out.push(pfaOptionField(rec, def));
  const args = pfaArea(rec, "args", v.argsText, (text) => { v.argsText = text; argsBox.clear(); pfvPaint(); });
  const argsBox = pfvFieldBox(rec, "args", "Extra command-line arguments", args, [PFA_ARGS_HINT]);
  out.push(argsBox, pfaEnvField(rec, host));
  if (pfaRepliesUsable(c)) {
    out.push(pfaCheckbox(rec, "replyable", "Answer prompts from the sidebar", "Sets the default in New session.", v.repliesChecked, (on) => { v.repliesChecked = on; pfvChanged(rec, "replyable"); }));
  }
  const imports = pfaImportsRow(c, v.importsChecked);
  if (imports.text) out.push(pfvMk("p", "pfv-hint", imports.text));
  else if (!imports.hidden) {
    out.push(pfaCheckbox(rec, "imports_off", "Include files my CLAUDE.md pulls in with @", "Read-only copies in the box.", v.importsChecked, (on) => { v.importsChecked = on; pfvChanged(rec, "imports_off"); }));
  }
  if (!host) out.push(pfvMk("p", "pfv-hint", PFA_BOX_BASE));
  return out;
}

pfvRegisterEditor("advanced", { values: pfaValues, draw: pfaDraw, merge: pfaMerge, check: pfaCheck, fields: true });
