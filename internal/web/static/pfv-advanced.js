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
// What each starting environment does, from the launcher (baseEnv, curatedBase, argv): every base leaves out the Claude session markers,
// CuliBox's own AB_* variables and its login password's variable (a non-default account also drops every account.IsAuthEnv name,
// launcher.go Build: Anthropic credentials, Bedrock keys, provider switches, base URLs), and clean and login pass on only the names below (TERM is
// defaulted by the launcher, not copied). A box always starts clean, which the "Applies to Host" chip says. A base not listed here
// (kept as stored) has no line.
const PFA_BASE_LINES = {
  inherit: "Everything in the CuliBox service's environment, keys it holds included, except CuliBox's own variables and Claude's session markers.",
  clean: "From the CuliBox service it passes on only PATH, HOME, USER, LOGNAME, SHELL, LANG and LC_*. Any other variable has to be added here.",
  login: "Starts as Minimal does, then runs the login shell's startup files, which can set anything.",
};
// The Claude login clause of the inherit line: only a claude command runs under a Claude login (launcher.go account check), so only
// a profile whose command (bin) is claude gets it.
const PFA_INHERIT_CLAUDE = " With a Claude login other than default, the service's own Anthropic credentials and provider settings are left out too.";
function pfaBaseLine(base, bin) {
  if (!Object.prototype.hasOwnProperty.call(PFA_BASE_LINES, base)) return [];
  return [PFA_BASE_LINES[base] + (base === "inherit" && isClaudeBinName(bin) ? PFA_INHERIT_CLAUDE : "")];
}
const PFA_ARGS_HINT = "One per line.";
const PFA_ENV_HINT = "Variables are saved as plain text in the profile. Put a token in Credentials instead.";
const PFA_ENV_NONE_BOX = "None. The agent gets the box's minimal environment.";
const PFA_ENV_NONE_HOST = "None added on top of the starting environment.";
const PFA_SECRET_NO_SOURCE = "No secret source is configured, so a session with this profile will refuse to start. Use a service token instead.";
// On a profile that does not run in a box a token binding is refused (config/statictoken.go validateTokens: runtime must be "box",
// set explicitly), so the pointer to a service token says what it needs. c.tier is the tier a save writes (profilepage.js).
const PFA_SECRET_NO_SOURCE_NOT_BOX = "No secret source is configured, so a session with this profile will refuse to start. A service token needs the Box tier: set Runs in to Box to use one.";
const PFA_SECRET_PICK_LABEL = "Add a secret";
const PFA_PICK_CLAUDE = "Pick the account this profile signs in with.";
const PFA_PICK_TOKEN = "An API key or token for a service. It only reaches that service's hosts.";
const PFA_PICK_TOKEN_NOT_BOX = "An API key or token for a service. It needs the Box tier.";

// pfaSources: the secret sources the daemon has configured (app.js secretSources, from /api/meta), [] when none are configured or
// they are not known yet (pfaSourcesKnown tells the two apart).
// pfaSourcesKnown: /api/meta has answered, so [] means none and not "not asked yet": until then the page claims nothing about them.
const pfaSources = () => (typeof secretSources !== "undefined" && Array.isArray(secretSources) ? secretSources : []);
const pfaSourcesKnown = () => typeof metaLoaded !== "undefined" && metaLoaded === true && typeof secretSources !== "undefined" && Array.isArray(secretSources);

// pfaSourcesWords: the configured sources in words, in config order ("" for none): the credential store, a file by its path,
// the CuliBox service's environment (the daemon's own, never "your shell").
function pfaSourcesWords(sources) {
  const words = [];
  for (const s of sources || []) {
    if (!s) continue;
    if (s.type === "store") words.push("the credential store");
    else if (s.type === "file") words.push("the file " + s.path);
    else if (s.type === "env") words.push("the CuliBox service's environment");
  }
  return pfvJoin(words);
}

// pfaSecretHint: what a stored secret row says about where its value comes from. True for every config: with sources it names
// them, without any the launcher refuses a name it cannot resolve and the way out depends on the tier (tier: c.tier; anything but "box"
// cannot hold a service token).
function pfaSecretHint(sources, tier) {
  const words = pfaSourcesWords(sources);
  if (words) return "Resolved at start from " + words + ".";
  return tier !== undefined && tier !== "box" ? PFA_SECRET_NO_SOURCE_NOT_BOX : PFA_SECRET_NO_SOURCE;
}

const pfaLines = (text) => String(text || "").split("\n").map((s) => s.trim()).filter(Boolean);
// pfaLive: the variable rows that count: not pending removal.
const pfaLive = (rows) => rows.filter((r) => r.removed !== true);

// pfaBin: the command the profile will have once what is open is saved: the Agent block's pending command when that
// block is open (a refused save can open Advanced beside it), else the stored one. What Advanced draws and offers
// follows it, so an option the new command does not take is not offered (the Agent save drops the stored one).
function pfaBin(c) {
  const agent = typeof pfvState !== "undefined" ? pfvState.edits.agent : null;
  const pending = agent && agent.values && typeof agent.values.bin === "string" ? agent.values.bin.trim() : "";
  return pending !== "" ? pending : typeof c.doc.bin === "string" ? c.doc.bin : "";
}

// pfaDefs: the options this block draws: every option the daemon defines except the Agent block's (model and
// effort), for the commands they apply to.
function pfaDefs(c) {
  const bin = pfaBin(c);
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
const pfaRepliesUsable = (c) => globalThis.repliesEnabled === true && isClaudeBinName(pfaBin(c));

// pfaImportsRow: importsmodel.js's rule for the imports row, from the page's own state. Before /api/meta has answered
// nothing is known and the row stays; once it has, "bundling off" (Ruling R8) and "no box layer" hide it like a host
// (nothing to give). The profile's own tier does not hide it: a host profile keeps the setting for the box.
// hidden: not drawn. disabled: the account has no personal config; the row's sentence is said instead.
function pfaImportsRow(c, checked) {
  const known = typeof metaLoaded !== "undefined" && metaLoaded === true;
  const bundleOff = known && !(typeof bundleImports !== "undefined" && bundleImports === true);
  const accounts = typeof accountsCache !== "undefined" && Array.isArray(accountsCache) ? accountsCache : [];
  const tier = !c.boxAvailable || bundleOff ? "host" : "box";
  const st = importsRowState({ tier, account: c.doc.account || "", accounts, set: null, failed: false, off: !checked });
  return { hidden: st.hidden, disabled: st.disabled, text: st.disabled ? st.list : "" };
}

// pfaMerge: the values pfvMergeBlock is given. The starting environment is drawn on both tiers, so the base always goes
// (a box profile keeps the one stored while it was a host profile); a bool option that was
// never stored and is off is not written (the server offers a stored bool at spawn, an absent one not).
function pfaMerge(rec, c) {
  const v = rec.values;
  const stored = pfvStoredOptions(c.doc);
  const options = {};
  for (const def of pfaDefs(c)) {
    const val = v.options[def.key];
    // An option the form was opened without (the Agent block's command changed after) is not this save's to write.
    if (val === undefined) continue;
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
  out.base = v.base === "inherit" && c.doc.base !== "inherit" ? "" : v.base;
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

// pfaCheckbox: a switch with its words, and under them what a refusal of its field says. tag: an Applies to chip after the
// label. disabled: the switch cannot be used (it stays drawn, dimmed as .pfv-off, with detail saying why).
function pfaCheckbox(rec, key, label, detail, checked, onChange, tag, disabled) {
  const lab = pfvMk("label", disabled ? "pfv-check pfv-off" : "pfv-check");
  const cb = pfvMk("input");
  cb.type = "checkbox";
  cb.checked = checked;
  if (disabled) cb.disabled = true;
  cb.onchange = () => onChange(cb.checked);
  pfvReg(rec, key, cb);
  const t = pfvMk("span", "pfv-check-t", label);
  if (detail) t.append(pfvMk("span", "pfv-check-d", detail));
  if (tag) t.append(" ", tag);
  lab.append(cb, t);
  const err = pfvFerr(rec, key);
  if (!err) return lab;
  const w = pfvMk("div", "pfv-field");
  const words = pfvMk("p", "pfv-ferr", err);
  pfvBad(rec, cb, words);
  w.append(lab, words);
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
// tpl-env and tpl-secret). A variable is a name and a value; a secret is a name only, with where its value comes from
// (hint: pfaSecretHint's, or "" while the sources are not known).
// A row pending removal is its words, struck through like the Cloud rows, with Undo. {node, name, btn}.
function pfaEnvRow(row, onInput, toggle, hint) {
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
    if (!secret) kv.append(pfaInput(row, "value", "Value", "value", onInput));
    else if (hint) kv.append(pfvMk("span", "pfv-hint", hint));
  }
  const what = (secret ? "secret" : "variable") + (row.key.trim() ? " " + row.key.trim() : "");
  const btn = pfvMk("button", "pfv-rmbtn", gone ? "Undo" : "Remove");
  btn.type = "button";
  btn.setAttribute("aria-label", (gone ? "Undo removing " : "Remove ") + (row.key.trim() ? what : "this " + what));
  btn.onclick = toggle;
  node.append(kv, btn);
  return { node, name, btn };
}

// pfaSecretPick: the "+ Add secret" menu, in the picker's spec ({label, filter, groups}). Three ways to give the agent a secret, each
// present when it can work: Claude login (the command is claude: the Agent block's account select), A service token (always: the
// Cloud and tokens block) and By name (older form), only once /api/meta has said a secret source is configured, because a name
// nothing resolves makes the session refuse to start. The two that leave the block go through pfvOpenBlock, which refuses (and
// nudges) while this block holds changes: the menu never discards or saves them, and a refusal leaves the block as it was.
function pfaSecretPick(rec, c) {
  const back = () => { const add = rec.fields.add; if (add && typeof add.focus === "function") add.focus(); };
  const items = [];
  if (isClaudeBinName(pfaBin(c))) {
    items.push({
      key: "claude", title: "Claude login", sub: PFA_PICK_CLAUDE,
      act: () => {
        if (!pfvOpenBlock("agent")) { back(); return; }
        const account = pfvState.edits.agent.fields.account;
        if (account && typeof account.focus === "function") account.focus();
      },
    });
  }
  items.push({
    key: "token", title: "A service token", sub: c.tier === "box" ? PFA_PICK_TOKEN : PFA_PICK_TOKEN_NOT_BOX,
    act: () => {
      const tokens = typeof stEntries !== "undefined" && Array.isArray(stEntries) ? stEntries : [];
      // With none defined there is nothing to choose from: straight to the drawer that makes one (Cloud and tokens takes it from there).
      const ok = tokens.length === 0 ? pfvOpenBlock("cloud") && pfvOpenDrawer("token", "cloud") : pfvOpenAdd("cloud");
      if (!ok && pfvState.edits.advanced === rec) back();
    },
  });
  const sources = pfaSources();
  if (pfaSourcesKnown() && pfaSourcesWords(sources) !== "") {
    // The row is added by the form drawn at this moment (the menu has closed and drawn it again), so the hook is read now.
    items.push({ key: "byname", title: "By name (older form)", sub: "Resolved from " + pfaSourcesWords(sources) + ".", act: () => rec.ui.pfaAddSecret() });
  }
  return { label: PFA_SECRET_PICK_LABEL, filter: "", groups: [{ title: "", items }] };
}

// pfaEnvField: the variables and secrets as rows, with the two "+ Add" links under them. The rows are rec.values.envRows
// itself, so a form drawn again keeps what was typed. Only this list is drawn again when a row is added, removed or
// brought back: the other controls (and the focus in them) stay. "+ Add secret" opens a menu (pfaSecretPick) that carries the
// picker's mechanics (rec.ui.picker, pfv-cloud.js); only its "By name" item adds a row here.
function pfaEnvField(rec, host, c) {
  const v = rec.values;
  const none = host ? PFA_ENV_NONE_HOST : PFA_ENV_NONE_BOX;
  const rows = pfvMk("div", "pfv-rows");
  const adds = pfvMk("div", "pfv-adds");
  const ctl = pfvMk("div", "pfv-env");
  ctl.append(rows, adds);
  const box = pfvFieldBox(rec, "env", "Environment variables", ctl, [PFA_ENV_HINT], "fieldset");
  // The server names a secret on its own field; both are this list's.
  const secretsErr = pfvFerr(rec, "secrets");
  const secretsNode = secretsErr ? pfvMk("p", "pfv-ferr", secretsErr) : null;
  if (secretsNode) box.append(secretsNode);
  // Before /api/meta has answered nothing is known about the sources, so a stored secret's row says nothing about them yet.
  const hint = pfaSourcesKnown() ? pfaSecretHint(pfaSources(), c.tier) : "";
  let names = [];
  let btns = [];
  // A row was typed in or taken, put or brought back: a refusal about the list is stale.
  const changed = () => {
    box.clear();
    names.forEach((n) => { if (n) pfvOk(n); });
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
      const r = pfaEnvRow(row, changed, () => toggle(row), hint);
      names.push(r.name);
      btns.push(r.btn);
      return r.node;
    }));
  };
  paint();
  // A refusal about the list is about one row of it, the first that makes the list wrong (a name that is not one, or the second
  // of a pair): that row's name is what is invalid, and where focus goes after a refused Save.
  const words = box.words() || secretsNode;
  if (words) {
    const live = pfaLive(v.envRows);
    const bad = live.findIndex((_, i) => rowError(live.slice(0, i + 1)) !== "");
    const input = names[bad >= 0 ? v.envRows.indexOf(live[bad]) : v.envRows.findIndex((r) => r.removed !== true)];
    if (input) pfvStandIn(rec, ctl, input, words);
  }
  const addRow = (kind) => {
    v.envRows.push({ key: "", value: "", kind, fresh: true });
    paint();
    changed();
    names[names.length - 1].focus();
  };
  rec.ui.pfaAddSecret = () => addRow("secret");
  const addVar = pfvDrawLink("+ Add variable", "");
  addVar.onclick = () => addRow("literal");
  adds.append(addVar);
  const wrap = pfvMk("div", "pfv-addwrap");
  const addSecret = pfvDrawLink("+ Add secret", "");
  addSecret.setAttribute("aria-expanded", rec.ui.picker === true ? "true" : "false");
  addSecret.setAttribute("aria-haspopup", "dialog");
  addSecret.onclick = () => {
    if (rec.ui.picker !== true) { pfvOpenPicker(rec.id); return; }
    pfvClosePicker(rec.id);
  };
  pfvReg(rec, "add", addSecret);
  wrap.append(addSecret);
  rec.ui.pickNodes = null;
  if (rec.ui.picker === true) {
    const pk = pfcPicker(rec, pfaSecretPick(rec, c));
    wrap.append(pk.node);
    rec.ui.pickNodes = { wrap, pop: pk.node, list: pk.list };
    if (pk.first) rec.first = pk.first;
  }
  adds.append(wrap);
  return box;
}

function pfaDraw(rec, c) {
  const v = rec.values;
  const host = c.tier === "host";
  const out = [];
  const stored = c.doc.base;
  const known = PFA_BASES.map((b) => b.value);
  const choices = typeof stored === "string" && stored !== "" && !known.includes(stored) ? PFA_BASES.concat([{ value: stored, label: stored }]) : PFA_BASES;
  const sel = pfvSelect(rec, "base", choices, v.base, false, (val) => { v.base = val; pfvChanged(rec, "base"); });
  const baseBox = pfvFieldBox(rec, "base", "Environment it starts with", sel, pfaBaseLine(v.base, pfaBin(c)));
  baseBox.children[0].append(" ", pfvAppliesTag("host"));
  out.push(baseBox);
  for (const def of pfaDefs(c)) out.push(pfaOptionField(rec, def));
  const args = pfaArea(rec, "args", v.argsText, (text) => { v.argsText = text; argsBox.clear(); pfvPaint(); });
  const argsBox = pfvFieldBox(rec, "args", "Extra command-line arguments", args, [PFA_ARGS_HINT]);
  out.push(argsBox, pfaEnvField(rec, host, c));
  if (pfaRepliesUsable(c)) {
    out.push(pfaCheckbox(rec, "replyable", "Answer prompts from the sidebar", "Sets the default in New session.", v.repliesChecked, (on) => { v.repliesChecked = on; pfvChanged(rec, "replyable"); }));
  }
  const imports = pfaImportsRow(c, v.importsChecked);
  if (imports.text) out.push(pfvMk("p", "pfv-hint", imports.text));
  else if (!imports.hidden) {
    out.push(pfaCheckbox(rec, "imports_off", "Include files my CLAUDE.md pulls in with @", "Read-only copies in the box.", v.importsChecked, (on) => { v.importsChecked = on; pfvChanged(rec, "imports_off"); }, pfvAppliesTag("box")));
  }
  return out;
}

// A row that was added and is still blank is no change: nothing is saved for it.
const pfaBlankNew = (r) => r.fresh === true && r.key.trim() === "" && r.value.trim() === "";

pfvRegisterEditor("advanced", {
  values: pfaValues, draw: pfaDraw, merge: pfaMerge, check: pfaCheck, fields: true,
  settled: (v) => Object.assign({}, v, { envRows: v.envRows.filter((r) => !pfaBlankNew(r)) }),
});
