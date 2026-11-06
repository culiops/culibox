"use strict";

// The Cloud and tokens and Also reachable editors (spec 5.3, 5.5, 5.6). Each grant is a row with Remove at its
// right: the removal is pending until Save (the row stays, struck through, with Undo, so a mistake is one click). A
// folder, a domain or a GitHub owner that is being added is an input row. What a save writes is profileedit.js's
// (pfvMergeBlock "cloud" and "reachable"); this file draws the rows from the same read model the read view uses
// (profileview.js, so no wording of a row is invented here), keeps what the user has typed and says what the client
// refuses before the PUT. Both editors register with profilepage.js (pfvRegisterEditor), so it loads after it.
//
// Values (rec.values), per block:
//   cloud      { aws: [{name}], tokens: [{name}], gh: [{was, owner, pick, repos}], perms: levels, legacy: {} | null }
//              A row that is pending removal has removed: true; one that was only just added has fresh: true (its
//              Remove deletes it, there is nothing to undo). gh.pick is "app:<name>" or "token:<entry>"; perms are
//              ghPermLevels' (off / read / write per permission); legacy is the old-style AWS setting, taken away
//              or converted, never edited.
//   reachable  { folders: [{path}], domains: [{host}] }, the same removed / fresh flags
//
// The seams (spec 5.7): pfvOpenPicker(blockId) opens the "+ Add" picker of an open block and pfvClosePicker(blockId?) closes
// it, giving focus back to the link; pfvAddRow(blockId, what) puts a pending row in, what being {kind: "folder" | "domain" |
// "github"} for an input row or {kind: "aws" | "token", name} for a name the instance defines. The picker's "New ..." items
// call pfvOpenDrawer(kind), kind "aws" | "github" | "token", when the page has that (the Credentials drawer). pfvConvertAws is
// the old-style AWS row's "Move to a saved connection" (today's convert).
//
// Every top-level name here starts with pfc/PFC_, except the seams above (the page scripts share one scope).
// Classic script: profilepage.js's helpers (pfvMk, pfvReg, pfvFieldBox, pfvChanged, pfvDrawRow, ...), profileform.js's
// GitHub rules and the credential lists of app.js are globals read at call time.

const PFC_FOLDER_HINT = "Read only. List a folder rather than a single file.";
const PFC_DOMAIN_HINT = "HTTPS only. IP addresses are not accepted.";
// config/github.go: the permission set shapes the minted App token (broker/github.go passes it to Mint); a token owner's
// credential is used as it is, so the set does nothing for it (the old form's note says the same).
const PFC_PAT_NOTE = "A personal access token is not narrowed by CuliBox: it reaches whatever the token allows.";
const PFC_NO_OWNER = "Name the GitHub org or user.";
const PFC_NO_REPOS = "List at least one repository.";
const PFC_NO_LOGIN = "Pick a GitHub App or token to sign in with.";
const PFC_NO_LOGINS = "No GitHub App or token is defined yet.";
const PFC_MOVE = "Move to a saved connection";

const pfcNames = (l) => (Array.isArray(l) ? l.filter((x) => typeof x === "string" && x !== "") : []);
const pfcLive = (l) => l.filter((e) => e.removed !== true);

// pfcRowOf: the read model's row for a grant, so an edited row has the read view's own words.
function pfcRowOf(rows, kind, name) {
  return rows.find((r) => r.kind === kind && r.name === name) || { kind, name, sub: "", scope: "", tag: "", missing: false };
}

// pfcRows: the rows of block `id` as the read view draws them, over the profile as the form holds it now: `over` names
// what the form has in place of the stored lists (a row that is pending removal is still listed).
function pfcRows(id, over) {
  const rv = pfvReadView(Object.assign({}, pfvInputs(), { view: Object.assign({}, pfvState.view, over) }));
  const b = rv.blocks.find((x) => x.id === id);
  return b ? b.rows : [];
}

// pfcCounted: the list the server counted a refusal's [i] in (the field is its name: "aws_connections", "tokens", "context_paths",
// "egress_allow"). A block that was part of the failing save sent the list its merge builds (`built`); one that was not (a repair
// block, or one open and clean) had the stored list sent as it is, whatever order the store keeps it in. rec.sent says which.
function pfcCounted(rec, c, field, built) {
  return rec.sent === true ? built : Array.isArray(c.doc[field]) ? c.doc[field] : [];
}

function pfcRmButton(rec, what, key, removed, toggle) {
  const b = pfvMk("button", "pfv-rmbtn", removed ? "Undo" : "Remove");
  b.type = "button";
  b.setAttribute("aria-label", (removed ? "Undo removing " : "Remove ") + what);
  b.onclick = toggle;
  return pfvReg(rec, key, b);
}

// pfcToggle: Remove, or Undo, on a row. A row that was only just added goes outright. Its refusals were about
// positions in the list that has just changed, so they go too.
function pfcToggle(rec, list, e) {
  if (e.fresh === true) list.splice(list.indexOf(e), 1);
  else e.removed = e.removed !== true;
  rec.ferrs = [];
  pfvChanged(rec, "");
}

// pfcRowNode: a read view row with its Remove (Undo) at the right. o: {removed, key, toggle, err}.
function pfcRowNode(rec, r, o) {
  const rm = pfcRmButton(rec, r.kind + " " + r.name, o.key, o.removed, o.toggle);
  // A refusal that names a stored row has no input to sit under: the Remove beside it is what answers it (a folder that is gone is
  // removed or replaced), so that is what reads it and what takes focus.
  const row = pfvDrawRow(r, rec.known, o.err || "", { edit: true, block: rec.id, bad: (words) => pfvBad(rec, rm, words, true) });
  if (o.removed) row.classList.add("pfv-removed");
  row.append(rm);
  return row;
}

// pfcInputRow: a row that is one input: a folder or a domain being added. The kind column is its visible label.
function pfcInputRow(rec, o) {
  const row = pfvMk("div", "pfv-row");
  row.append(pfvMk("span", "pfv-k", o.kind));
  const input = pfvTextCtl(rec, o.key, o.entry[o.prop], true, (val) => { o.entry[o.prop] = val; box.clear(); pfvPaint(); }, () => {});
  input.setAttribute("placeholder", o.placeholder);
  const box = pfvFieldBox(rec, o.errKey, o.label, input, [o.hint]);
  box.children[0].classList.add("pfv-sr");
  row.append(box, pfcRmButton(rec, o.label, o.rmKey, false, o.toggle));
  return row;
}

// --- the "+ Add" picker (spec 5.7; the mockup's `picker` state) -------------------------------------------------------------

// A popover under the "+ Add" link. What it offers is data: groups [{title, note, items}], an item being {title, sub, tag,
// added, add, create}: `add` is what pfvAddRow is given, `create` the kind of thing a "New ..." item makes (pfvOpenDrawer's), `added`
// that the profile holds it already (drawn, disabled, "Added"). A group with no title is drawn without a heading. The popover is
// open while rec.ui.picker is true: that is what draws it and what pfvState.picker and the Esc layering read.
const PFC_PICK_READ = "Read only";
const PFC_PICK_AWS_WRITE = "Read only; write after a granted window";
const PFC_PICK_GH_APP = "Through the GitHub App: 1-hour tokens, read only unless you change it";
const PFC_PICK_TOKENS = "Tokens are long-lived, and the agent can read them while it runs.";
const PFC_PICK_LEGACY = "Move the old-style AWS setting to a saved connection, or remove it, before adding connections.";
const PFC_PICK_NONE = "Nothing matches.";

// pfcPickOption: one option: its name (with the PROD / production tag), the line under it and, on one that is added already,
// the word at its right. Added ones are not buttons that do anything: a disabled button, no handler.
function pfcPickOption(rec, it) {
  const b = pfvMk("button", "pfv-opt" + (it.create ? " pfv-create" : ""));
  b.type = "button";
  const t = pfvMk("span", "pfv-opt-t");
  t.append(it.title);
  if (it.tag) t.append(" ", pfvMk("span", "pfv-tag", it.tag));
  b.append(t);
  if (it.sub) b.append(pfvMk("span", "pfv-opt-s", it.sub));
  if (it.added === true) {
    b.disabled = true;
    b.append(pfvMk("span", "pfv-opt-r", "Added"));
    return b;
  }
  b.onclick = it.create ? () => pfcPickCreate(rec, it.create) : () => pfvAddRow(rec.id, it.add);
  return pfvReg(rec, "pick:" + (it.create ? "new:" + it.create : it.add.kind + (it.add.name ? ":" + it.add.name : "")), b);
}

// pfcPickCreate: a "New ..." item opens the Credentials drawer for that kind (pfvOpenDrawer), which takes over from the picker.
// While the page has no drawer the item does nothing. It is read off globalThis, the way the page reads what another script may
// not have defined (a top-level function there is a property of it), so scripts/lint.sh does not take it for a name nobody defines.
function pfcPickCreate(rec, kind) {
  if (typeof globalThis.pfvOpenDrawer !== "function") return;
  pfcPickClose(rec);
  globalThis.pfvOpenDrawer(kind);
}

// pfcPickEnd: the picker is not asked for any more: what was typed in its filter and the height it was pinned at go with it.
function pfcPickEnd(rec) {
  rec.ui.picker = false;
  rec.ui.pickQuery = "";
  rec.ui.pickMin = 0;
}

// pfcPickClose: the picker goes without focus being given to anything: the one that closes it by Esc or a pick says where focus goes.
function pfcPickClose(rec) {
  pfcPickEnd(rec);
  pfvRedrawForm(rec);
}

// pfcPickPin: the list may not get shorter than `px`. The popover hangs off the link and, when its block is the last on the page,
// it is what holds the page's scroll height: options that the filter hides would shorten it, the page would scroll back under the
// user's hands and the filter and the link would move down while it is typed in. The height is a custom property (desktop.css
// takes the smaller of it and the room there is), so it also survives the form being drawn again.
function pfcPickPin(rec, list) {
  if (rec.ui.pickMin > 0) list.style.setProperty("--pfv-pick-min", rec.ui.pickMin + "px");
}

// pfcPickTabbed: a Tab (or Shift+Tab) in the popover has been given to the browser; once focus has moved, a popover that focus is
// not in any more closes, so it does not stay open over the block's footer and Save. Focus is where the user tabbed: the form is
// drawn again with focus kept on that control (pfvRedrawForm), and nothing is given back to the link.
function pfcPickTabbed(rec) {
  setTimeout(() => {
    const pk = rec.ui.pickNodes;
    if (rec.ui.picker !== true || !pk || pk.pop.contains(document.activeElement)) return;
    pfcPickClose(rec);
  }, 0);
}

// pfcPicker: the popover. spec: {label, filter, groups}; filter is the filter's name (and placeholder), "" for none. Typing in
// the filter narrows the options in place (by name and by the line under it), hides a group with nothing left and says when
// nothing matches; what was typed is kept on rec.ui, so drawing the form again does not lose it.
function pfcPicker(rec, spec) {
  const pop = pfvMk("div", "pfv-pick");
  pop.setAttribute("role", "dialog");
  pop.setAttribute("aria-label", spec.label);
  let first = null;
  let find = null;
  if (spec.filter) {
    find = pfvMk("input", "pfv-ctl pfv-pick-find");
    find.type = "search";
    find.setAttribute("placeholder", spec.filter);
    find.setAttribute("aria-label", spec.filter);
    find.setAttribute("autocomplete", "off");
    find.setAttribute("spellcheck", "false");
    pfvReg(rec, "pick:filter", find);
    pop.append(find);
    first = find;
  }
  // The options scroll inside the popover (the filter stays put above them): an instance can define far more than a screen holds.
  const list = pfvMk("div", "pfv-pick-list");
  pfcPickPin(rec, list);
  pop.append(list);
  const groups = [];
  for (const g of spec.groups) {
    const node = pfvMk("div", "pfv-pick-grp");
    if (g.title) node.append(pfvMk("p", "pfv-pick-g", g.title));
    if (g.note) node.append(pfvMk("p", "pfv-pick-note", g.note));
    const mine = [];
    for (const it of g.items) {
      const b = pfcPickOption(rec, it);
      node.append(b);
      mine.push({ node: b, hay: (it.title + " " + (it.sub || "")).toLowerCase() });
      first = first || (b.disabled === true ? null : b);
    }
    groups.push({ node, opts: mine });
    list.append(node);
  }
  const none = pfvMk("p", "pfv-pick-none", PFC_PICK_NONE);
  none.hidden = true;
  list.append(none);
  const narrow = (q) => {
    const w = q.trim().toLowerCase();
    let any = false;
    for (const g of groups) {
      let n = 0;
      for (const o of g.opts) {
        o.node.hidden = w !== "" && !o.hay.includes(w);
        if (!o.node.hidden) n++;
      }
      g.node.hidden = w !== "" && n === 0;
      any = any || n > 0;
    }
    none.hidden = w === "" || any;
  };
  if (find) {
    find.value = typeof rec.ui.pickQuery === "string" ? rec.ui.pickQuery : "";
    find.oninput = () => { rec.ui.pickQuery = find.value; narrow(find.value); };
    narrow(find.value);
  }
  pop.addEventListener("keydown", (ev) => { if (ev.key === "Tab") pfcPickTabbed(rec); });
  return { node: pop, first, list };
}

// pfvOpenPicker: the "+ Add" picker of an open block, with focus in it (the filter, else the first option) and the popover in
// view. false if the block is not open.
function pfvOpenPicker(blockId) {
  const rec = pfvState.edits[blockId];
  if (!rec) return false;
  pfcPickEnd(rec);
  rec.ui.picker = true;
  pfvRedrawForm(rec);
  const pk = rec.ui.pickNodes;
  if (pk && pk.list.offsetHeight > 0) {
    rec.ui.pickMin = pk.list.offsetHeight;
    pfcPickPin(rec, pk.list);
  }
  if (pk && typeof pk.pop.scrollIntoView === "function") pk.pop.scrollIntoView({ block: "nearest" });
  pfvFocusBlock(blockId);
  return true;
}

// pfvClosePicker: close the open picker (of block `blockId`, else of every open block) and give focus back to the link that
// opened it. false when none was open.
function pfvClosePicker(blockId) {
  const open = Object.values(pfvState.edits).filter((r) => r.ui && r.ui.picker === true && (blockId === undefined || r.id === blockId));
  for (const rec of open) pfcPickClose(rec);
  const add = open.length ? open[0].fields.add : null;
  if (add && typeof add.focus === "function") add.focus();
  return open.length > 0;
}

// A click anywhere outside an open picker closes it, and focus stays wherever the click put it (a field, another link). In the
// capture phase, so it runs before the clicked control's own handler: that handler may draw the form again, and a node the form
// has dropped is inside nothing. The link has its own toggle, so a click on it is not "outside".
if (typeof document !== "undefined" && typeof document.addEventListener === "function") {
  document.addEventListener("click", (ev) => {
    for (const rec of Object.values(pfvState.edits)) {
      const wrap = rec.ui && rec.ui.pickNodes ? rec.ui.pickNodes.wrap : null;
      if (rec.ui && rec.ui.picker === true && !(wrap && wrap.contains(ev.target))) pfcPickClose(rec);
    }
  }, true);
}

// pfvAddRow: a pending row in an open block. true when it added one (or brought a removed row back).
function pfvAddRow(blockId, what) {
  const rec = pfvState.edits[blockId];
  const ed = PFV_EDITORS[blockId];
  if (!rec || !rec.values || !ed || typeof ed.add !== "function") return false;
  const key = ed.add(rec, what || {});
  if (!key) return false;
  pfcPickEnd(rec);
  rec.ferrs = [];
  pfvChanged(rec, "");
  const el = rec.fields[key];
  if (el && typeof el.focus === "function") el.focus();
  return true;
}

// pfcFoot: what ends the form of a block of rows: the empty line and the "+ Add" link, which carries the picker while it is asked
// for. pick() is the picker's spec, asked for only while it is open.
function pfcFoot(rec, list, pick) {
  const out = [list];
  if (list.children.length === 0) out.push(pfvMk("p", "pfv-empty", rec.block.lines[0] || ""));
  const wrap = pfvMk("div", "pfv-addwrap");
  const add = pfvDrawLink(rec.block.add || "+ Add", "pfv-add");
  add.setAttribute("aria-expanded", rec.ui.picker === true ? "true" : "false");
  add.setAttribute("aria-haspopup", "dialog");
  add.onclick = () => {
    if (rec.ui.picker !== true) { pfvOpenPicker(rec.id); return; }
    pfvClosePicker(rec.id);
  };
  pfvReg(rec, "add", add);
  wrap.append(add);
  rec.ui.pickNodes = null;
  if (rec.ui.picker === true) {
    const pk = pfcPicker(rec, pick());
    wrap.append(pk.node);
    rec.ui.pickNodes = { wrap, pop: pk.node, list: pk.list };
    if (pk.first) rec.first = pk.first;
  }
  out.push(wrap);
  return out;
}

// --- Cloud and tokens ---------------------------------------------------------------------------------------------------

const pfcRepos = (g) => String(g.repos || "").split(/[\s,]+/).filter(Boolean);
// The owner rows a save sends (ghBindingFromForm drops one with no owner and no repository), in the order the server names them.
const pfcLiveGh = (v) => v.gh.filter((g) => g.removed !== true && (String(g.owner).trim() !== "" || pfcRepos(g).length > 0));

function pfcCloudValues(c) {
  const d = c.doc;
  const gh = d.github && typeof d.github === "object" ? d.github : null;
  const owners = gh && Array.isArray(gh.owners) ? gh.owners.filter((o) => o && typeof o === "object") : [];
  return {
    aws: pfcNames(d.aws_connections).map((name) => ({ name })),
    tokens: pfcNames(d.tokens).map((name) => ({ name })),
    gh: owners.map((o) => ({
      was: String(o.owner || ""), owner: String(o.owner || ""), pick: o.token ? "token:" + o.token : o.app ? "app:" + o.app : "", repos: pfcNames(o.repos).join(", "),
    })),
    perms: ghPermLevels(gh),
    legacy: d.aws && typeof d.aws === "object" ? {} : null,
  };
}

// pfcBinding: the binding the rows make (ghBindingFromForm's), or null when none is left. A token owner carries no app.
function pfcBinding(v) {
  const owners = pfcLiveGh(v).map((g) => ({
    owner: g.owner, app: g.pick.startsWith("app:") ? g.pick.slice(4) : "", token: g.pick.startsWith("token:") ? g.pick.slice(6) : "", repos: g.repos,
  }));
  return ghBindingFromForm({ owners, permissions: v.perms }) || null;
}

function pfcCloudMerge(rec) {
  const v = rec.values;
  const out = { aws_connections: pfcLive(v.aws).map((e) => e.name), tokens: pfcLive(v.tokens).map((e) => e.name), github: pfcBinding(v) };
  // The old-style setting is not edited: it goes back as stored, or is taken away.
  if (v.legacy && v.legacy.removed === true) out.aws = null;
  return out;
}

// pfcCheck: what the client refuses before the PUT: an owner row that cannot be a binding. {field, text} or null.
function pfcCloudCheck(rec) {
  const live = pfcLiveGh(rec.values);
  for (let i = 0; i < live.length; i++) {
    const g = live[i];
    const at = "github.owners[" + i + "]";
    if (String(g.owner).trim() === "") return { field: at + ".owner", text: PFC_NO_OWNER };
    if (pfcRepos(g).length === 0) return { field: at + ".repos", text: PFC_NO_REPOS };
    if (g.pick === "") return { field: at + ".app", text: PFC_NO_LOGIN };
  }
  return null;
}

// pfcLogins: what an owner can sign in with: the instance's GitHub Apps and its GitHub token entries. A stored login the
// lists no longer have stays selectable, so opening and saving never re-points it quietly; the server then names it.
function pfcLogins(pick) {
  const apps = (typeof ghApps !== "undefined" && Array.isArray(ghApps) ? ghApps : []).filter((n) => typeof n === "string");
  const toks = ghUsableTokens(typeof stEntries !== "undefined" && Array.isArray(stEntries) ? stEntries : []).map((e) => e.name);
  if (pick.startsWith("app:") && !apps.includes(pick.slice(4))) apps.push(pick.slice(4));
  if (pick.startsWith("token:") && !toks.includes(pick.slice(6))) toks.push(pick.slice(6));
  return apps.map((n) => ({ value: "app:" + n, label: n + " · GitHub App, 1-hour tokens" }))
    .concat(toks.map((n) => ({ value: "token:" + n, label: n + " · personal access token" })));
}

// pfcPermWords: the permission set in words: the preset's name and what it holds, else what can be written and what only read.
function pfcPermWords(levels) {
  const on = GH_PERMS.filter((k) => levels[k] !== "off");
  const words = (l) => l.map((k) => k.replace(/_/g, " ")).join(", ");
  if (on.length === 0) return ["No permission ticked."];
  if (pfvGhPreset(levels) === "read-only") return ["Read only ", pfvMk("span", "pfv-muted", "(" + on.length + " of " + GH_PERMS.length + ": " + words(on) + ")")];
  const write = on.filter((k) => levels[k] === "write");
  const read = on.filter((k) => levels[k] !== "write");
  return [[write.length ? "Can write: " + words(write) + "." : "", read.length ? "Read only: " + words(read) + "." : ""].filter(Boolean).join(" ")];
}

// The order Customise lists them in (the mockup's): the three the server lets be written first, then those that are only read.
// A permission the server adds later is listed last rather than lost.
const PFC_PERM_ORDER = ["contents", "pull_requests", "issues", "metadata", "actions", "checks", "statuses"];
const pfcPermOrder = () => PFC_PERM_ORDER.filter((k) => GH_PERMS.includes(k)).concat(GH_PERMS.filter((k) => !PFC_PERM_ORDER.includes(k)));

// pfcPermRow: one permission: ticked means read, and the three the server lets be written also say read or write.
function pfcPermRow(rec, k) {
  const v = rec.values;
  const lev = v.perms[k];
  const row = pfvMk("div", "pfv-perm");
  const lab = pfvMk("label", "pfv-check");
  const cb = pfvMk("input");
  cb.type = "checkbox";
  cb.checked = lev !== "off";
  cb.onchange = () => { v.perms[k] = cb.checked ? "read" : "off"; pfvChanged(rec, "github.permissions"); };
  pfvReg(rec, "gh:perm:" + k, cb);
  lab.append(cb, pfvMk("span", "pfv-check-t", k.replace(/_/g, " ")));
  row.append(lab);
  if (!GH_WRITABLE.includes(k)) row.append(pfvMk("span", "pfv-hint", "read"));
  else if (lev !== "off") row.append(pfvSegCtl(rec, "gh:level:" + k, ["read", "write"], lev === "write" ? "write" : "read", (val) => { v.perms[k] = val; pfvChanged(rec, "github.permissions"); }));
  return row;
}

// pfcPermsField: the "Read only" preset (the server's default set), with Customise for the seven and the three toggles.
function pfcPermsField(rec, c) {
  const v = rec.values;
  const body = pfvMk("div", "pfv-perms");
  const sum = pfvMk("p", "pfv-permsum");
  sum.append(...pfcPermWords(v.perms));
  const cust = pfvDrawLink("Customise", "pfv-cust");
  cust.setAttribute("aria-expanded", rec.ui.customise === true ? "true" : "false");
  cust.onclick = () => { rec.ui.customise = rec.ui.customise !== true; pfvRedrawForm(rec); };
  pfvReg(rec, "gh:customise", cust);
  body.append(sum, cust);
  if (rec.ui.customise === true) for (const k of pfcPermOrder()) body.append(pfcPermRow(rec, k));
  const warn = ghWriteWarning(v.perms, wmModeOf(c.doc) !== "copy");
  if (warn) body.append(pfvMk("p", "pfv-warnw", warn));
  const box = pfvFieldBox(rec, "github.permissions", "Permissions", body, [], "fieldset");
  // A refusal about the set is about its checkboxes (none ticked, say): the first one is what is marked and takes focus, or the
  // Customise link while they are folded away.
  const words = box.words();
  if (words) {
    const first = rec.fields["gh:perm:" + pfcPermOrder()[0]];
    pfvStandIn(rec, body, first || cust, words, !first);
  }
  return box;
}

// pfcGithubFields: one owner's fields: how it signs in, the login, its repositories and (on the row that holds them,
// for the owners that use an App) the permissions. at: the index the server names the row by, -1 when it is not sent.
function pfcGithubFields(rec, c, i, at, withPerms) {
  const g = rec.values.gh[i];
  const out = pfvMk("div", "pfv-fields");
  const key = (leaf) => (at >= 0 ? "github.owners[" + at + "]." + leaf : "");
  const logins = pfcLogins(g.pick);
  if (logins.length) {
    const errKey = key(g.pick.startsWith("token:") ? "token" : "app");
    const sel = pfvSelect(rec, "gh:" + i + ":pick", logins, g.pick, false, (val) => { g.pick = val; pfvChanged(rec, errKey); });
    out.append(pfvFieldBox(rec, errKey, "Sign in with", sel, g.pick.startsWith("token:") ? [PFC_PAT_NOTE] : []));
  } else {
    out.append(pfvMk("p", "pfv-hint", PFC_NO_LOGINS));
  }
  const own = pfvTextCtl(rec, "gh:" + i + ":owner", g.owner, true, (val) => { g.owner = val; ownBox.clear(); pfvPaint(); }, () => {});
  own.setAttribute("placeholder", "culiops");
  const ownBox = pfvFieldBox(rec, key("owner"), "GitHub org or user", own, []);
  // One line, comma separated (a paste with line breaks is read the same: pfcRepos splits on both).
  const repos = pfvTextCtl(rec, "gh:" + i + ":repos", g.repos, true, (val) => { g.repos = val; reposBox.clear(); pfvPaint(); }, () => {});
  repos.setAttribute("placeholder", "payments-api, runbooks");
  const reposBox = pfvFieldBox(rec, key("repos"), "Repos", repos, ["Comma separated."]);
  out.append(ownBox, reposBox);
  if (withPerms) out.append(pfcPermsField(rec, c));
  return out;
}

function pfcGithubRow(rec, c, rows, i, live, permsAt) {
  const v = rec.values;
  const g = v.gh[i];
  const toggle = () => pfcToggle(rec, v.gh, g);
  const gone = g.removed === true;
  const fields = gone ? null : pfcGithubFields(rec, c, i, live.indexOf(g), i === permsAt);
  if (g.fresh !== true) {
    const row = pfcRowNode(rec, pfcRowOf(rows, "GitHub", g.was), { removed: gone, key: "rm:gh:" + i, toggle });
    if (fields) {
      fields.classList.add("pfv-rowedit");
      row.append(fields);
    }
    return row;
  }
  // A row that is only being added: its fields are all there is to it.
  const row = pfvMk("div", "pfv-row");
  row.append(pfvMk("span", "pfv-k", "GitHub"));
  fields.classList.add("pfv-rowfields");
  row.append(fields, pfcRmButton(rec, "GitHub owner", "rm:gh:" + i, false, toggle));
  return row;
}

function pfcCloudDraw(rec, c) {
  const v = rec.values;
  const rows = pfcRows("cloud", { aws_connections: v.aws.map((e) => e.name), tokens: v.tokens.map((e) => e.name) });
  const list = pfvMk("div", "pfv-rows");
  // A named row: the server names it by its place in the list the failing save carried (pfcCounted): sorted when this block
  // sent it, as stored when another block's save did.
  const named = (kind, key, field, arr, built) => {
    const counted = pfcCounted(rec, c, field, built);
    for (const e of arr) {
      const gone = e.removed === true;
      const at = counted.indexOf(e.name);
      list.append(pfcRowNode(rec, pfcRowOf(rows, kind, e.name), {
        removed: gone, key: "rm:" + key + ":" + e.name, toggle: () => pfcToggle(rec, arr, e),
        err: gone || at < 0 ? "" : pfvFerr(rec, field + "[" + at + "]"),
      }));
    }
  };
  named("AWS", "aws", "aws_connections", v.aws, awscToSend(pfcLive(v.aws).map((e) => e.name)) || []);
  if (v.legacy) {
    const row = rows.find((r) => r.kind === "Old-style AWS") || { kind: "Old-style AWS", name: "Old-style AWS setting", sub: "", scope: "", tag: "", missing: false, action: PFC_MOVE };
    const gone = v.legacy.removed === true;
    list.append(pfcRowNode(rec, row, {
      removed: gone, key: "rm:legacy", err: gone ? "" : pfvFerr(rec, "aws"),
      toggle: () => { v.legacy.removed = !gone; rec.ferrs = []; pfvChanged(rec, ""); },
    }));
  }
  const live = pfcLiveGh(v);
  let permsAt = -1;
  v.gh.forEach((g, i) => { if (g.removed !== true && g.pick.startsWith("app:")) permsAt = i; });
  v.gh.forEach((g, i) => list.append(pfcGithubRow(rec, c, rows, i, live, permsAt)));
  named("Token", "token", "tokens", v.tokens, stTokensToSend(pfcLive(v.tokens).map((e) => e.name)) || []);
  const out = pfcFoot(rec, list, () => pfcCloudPick(rec));
  // The note comes once, after the rows, while any token is still bound.
  if (pfcLive(v.tokens).length) out.splice(1, 0, pfvMk("p", "pfv-tnote", SPAWN_TOKEN_NOTE));
  return out;
}

// pfcAwsSub: the line under a connection in the picker: where it is, then what it can do.
function pfcAwsSub(c) {
  const place = spawnJoin([c.account_id ? "account " + c.account_id : "", c.region]);
  const words = c.mode === "sso_direct" ? spawnAWSScope({ direct: true }) : c.write_capable === true ? PFC_PICK_AWS_WRITE : PFC_PICK_READ;
  return place ? place + ". " + words : words;
}

// pfcCloudPick: what the Cloud and tokens picker offers, in the instance's own order: what the profile holds is drawn as Added
// (one that is only pending removal is not held: picking it takes the removal back), and what cannot be added now is said in one
// line instead. The last item of a group makes the thing that is missing.
function pfcCloudPick(rec) {
  const v = rec.values;
  const held = (list) => new Set(pfcLive(list).map((e) => e.name));
  const heldAws = held(v.aws);
  const heldTok = held(v.tokens);
  const conns = typeof awscEntries !== "undefined" && Array.isArray(awscEntries) ? awscEntries : [];
  const tokens = typeof stEntries !== "undefined" && Array.isArray(stEntries) ? stEntries : [];
  // The server takes the old-style AWS setting or saved connections, never both (config/awsconn.go): while it stays, neither a
  // connection nor a new one can be added.
  const legacy = !!v.legacy && v.legacy.removed !== true;
  const aws = { title: "AWS connections", note: legacy ? PFC_PICK_LEGACY : "", items: [] };
  if (!legacy) {
    for (const c of conns) aws.items.push({ title: c.name, tag: c.prod ? "PROD" : "", sub: pfcAwsSub(c), added: heldAws.has(c.name), add: { kind: "aws", name: c.name } });
    aws.items.push({ title: "New AWS connection…", sub: "Set it up in Credentials without leaving this page", create: "aws" });
  }
  const logins = pfcLogins("");
  const gh = { title: "GitHub", note: logins.length ? "" : PFC_NO_LOGINS, items: [] };
  // An owner signs in with a GitHub App, else a personal access token: the App's words are true only when there is an App.
  if (logins.length) gh.items.push({ title: "Another org or user", sub: logins.some((l) => l.value.startsWith("app:")) ? PFC_PICK_GH_APP : PFC_PAT_NOTE, add: { kind: "github" } });
  gh.items.push({ title: "New GitHub App…", sub: "Install the App on an org, or paste a personal access token", create: "github" });
  const tok = { title: "Tokens", note: PFC_PICK_TOKENS, items: [] };
  for (const t of tokens) {
    tok.items.push({ title: t.name, tag: t.prod === true ? "production" : "", sub: spawnJoin([t.description].concat(spawnTokenHosts(t.hosts))), added: heldTok.has(t.name), add: { kind: "token", name: t.name } });
  }
  tok.items.push({ title: "New token…", sub: "Kept in the encrypted credential store", create: "token" });
  return { label: "Add cloud access or a token", filter: "Filter connections and tokens", groups: [aws, gh, tok] };
}

// pfcCloudAdd: pfvAddRow's hook. A name the instance defines goes in as a row (a removed one comes back); a GitHub owner
// goes in as an input row signing in with the first login there is.
function pfcCloudAdd(rec, what) {
  const v = rec.values;
  if (what.kind === "aws" || what.kind === "token") {
    const list = what.kind === "aws" ? v.aws : v.tokens;
    const name = typeof what.name === "string" ? what.name : "";
    if (!name) return "";
    const have = list.find((e) => e.name === name);
    if (have && have.removed !== true) return "";
    if (have) have.removed = false;
    else list.push({ name, fresh: true });
    return "rm:" + what.kind + ":" + name;
  }
  if (what.kind === "github") {
    const logins = pfcLogins("");
    v.gh.push({ fresh: true, owner: "", pick: logins.length ? logins[0].value : "", repos: "" });
    return "gh:" + (v.gh.length - 1) + ":owner";
  }
  return "";
}

// pfcCloudRemove: the read view's Remove link on a row whose name is not defined any more.
function pfcCloudRemove(rec, row) {
  const list = row.kind === "AWS" ? rec.values.aws : row.kind === "Token" ? rec.values.tokens : null;
  const e = list && list.find((x) => x.name === row.name);
  if (!e || e.removed === true) return;
  e.removed = true;
  rec.ferrs = [];
  pfvChanged(rec, "");
}

// --- Also reachable -----------------------------------------------------------------------------------------------------

function pfcReachValues(c) {
  return { folders: pfcNames(c.doc.context_paths).map((path) => ({ path })), domains: pfcNames(c.doc.egress_allow).map((host) => ({ host })) };
}

function pfcReachMerge(rec) {
  const v = rec.values;
  return { context_paths: pfcLive(v.folders).map((e) => e.path), egress_allow: pfcLive(v.domains).map((e) => e.host) };
}

// pfcFolderSent: each folder entry the save sends, with the index the server names it by (blank ones are dropped).
function pfcFolderSent(v) {
  const at = new Map();
  let n = 0;
  for (const e of pfcLive(v.folders)) if (e.path.trim() !== "") at.set(e, n++);
  return at;
}

// pfcReachCheck: the client refuses a folder that is not a full path before the PUT; the server stays the authority on the rest.
function pfcReachCheck(rec) {
  for (const [e, i] of pfcFolderSent(rec.values)) {
    if (!e.path.trim().startsWith("/")) return { field: "context_paths[" + i + "]", text: PFE_RELATIVE_PATH };
  }
  return null;
}

function pfcReachDraw(rec, c) {
  const v = rec.values;
  const stored = (l) => l.filter((e) => e.fresh !== true);
  const rows = pfcRows("reachable", { context_paths: stored(v.folders).map((e) => e.path), egress_allow: stored(v.domains).map((e) => e.host) });
  const list = pfvMk("div", "pfv-rows");
  // The place the server counts an entry at (pfcCounted): in what this block's merge builds (blanks dropped, domains split and
  // deduped) when it sent the save, else in the stored list.
  const sentFolder = pfcFolderSent(v);
  const storedFolders = pfcCounted(rec, c, "context_paths", []);
  const folderAt = (e) => (rec.sent === true ? (sentFolder.has(e) ? sentFolder.get(e) : -1) : storedFolders.indexOf(e.path));
  const sentDomains = pfcCounted(rec, c, "egress_allow", egressAllowFromText(pfcLive(v.domains).map((e) => e.host).join("\n")));
  const domainAt = (e) => sentDomains.indexOf(rec.sent === true ? egressAllowFromText(e.host)[0] : e.host);
  v.folders.forEach((e, i) => {
    if (e.fresh === true) return;
    const gone = e.removed === true;
    list.append(pfcRowNode(rec, pfcRowOf(rows, "Folder", e.path), {
      removed: gone, key: "rm:folder:" + i, toggle: () => pfcToggle(rec, v.folders, e),
      err: gone || folderAt(e) < 0 ? "" : pfvFerr(rec, "context_paths[" + folderAt(e) + "]"),
    }));
  });
  // What the original shares and the CLAUDE.md imports are set elsewhere: the row says where.
  for (const r of rows.filter((x) => x.kind === "Files")) {
    const row = pfvDrawRow(r, rec.known, "", { edit: true, block: rec.id });
    row.append(pfvMk("span", "pfv-inote", r.original === true ? "Set in Workspace" : "Set in Advanced"));
    list.append(row);
  }
  v.domains.forEach((e, i) => {
    if (e.fresh === true) return;
    const gone = e.removed === true;
    list.append(pfcRowNode(rec, pfcRowOf(rows, "Domain", e.host), {
      removed: gone, key: "rm:domain:" + i, toggle: () => pfcToggle(rec, v.domains, e),
      err: gone || domainAt(e) < 0 ? "" : pfvFerr(rec, "egress_allow[" + domainAt(e) + "]"),
    }));
  });
  v.folders.forEach((e, i) => {
    if (e.fresh !== true) return;
    list.append(pfcInputRow(rec, {
      kind: "Folder", label: "Folder the agent can read", entry: e, prop: "path", key: "folder:" + i, rmKey: "rm:folder:" + i,
      errKey: folderAt(e) >= 0 ? "context_paths[" + folderAt(e) + "]" : "", placeholder: "/home/you/other-repo/docs", hint: PFC_FOLDER_HINT,
      toggle: () => pfcToggle(rec, v.folders, e),
    }));
  });
  v.domains.forEach((e, i) => {
    if (e.fresh !== true) return;
    list.append(pfcInputRow(rec, {
      kind: "Domain", label: "Domain the agent can reach", entry: e, prop: "host", key: "domain:" + i, rmKey: "rm:domain:" + i,
      errKey: domainAt(e) >= 0 ? "egress_allow[" + domainAt(e) + "]" : "", placeholder: "api.example.com or *.example.com", hint: PFC_DOMAIN_HINT,
      toggle: () => pfcToggle(rec, v.domains, e),
    }));
  });
  return pfcFoot(rec, list, pfcReachPick);
}

// pfcReachPick: the Also reachable picker: the two kinds, each an input row (no drawer, nothing to filter).
const pfcReachPick = () => ({
  label: "Add a folder or a domain", filter: "",
  groups: [{ title: "", note: "", items: [
    { title: "Folder it can read", sub: "Read only, at the same path inside the box. Never list secrets.", add: { kind: "folder" } },
    { title: "Domain it can reach", sub: "A hostname or *.example.com, over HTTPS", add: { kind: "domain" } },
  ] }],
});

function pfcReachAdd(rec, what) {
  const v = rec.values;
  if (what.kind === "folder") {
    v.folders.push({ path: "", fresh: true });
    return "folder:" + (v.folders.length - 1);
  }
  if (what.kind === "domain") {
    v.domains.push({ host: "", fresh: true });
    return "domain:" + (v.domains.length - 1);
  }
  return "";
}

// --- the old-style AWS setting: Move to a saved connection ----------------------------------------------------------------

let pfcConverting = false;

// pfvConvertAws: today's convert (app.js convertPfAws) from the profile page: ask, POST, and when the role can write ask
// again and send the acknowledgement; then show what the server holds now. It changes the stored profile, so it is refused
// while any block holds changes (the old form discarded them; here the user decides).
async function pfvConvertAws() {
  const name = pfvState.name;
  if (!name || !pfvState.doc || pfvState.saving || pfcConverting) return false;
  if (pfvAnyDirty()) {
    pfvNudge();
    return false;
  }
  if (!(await askConfirm({ title: "Move to a saved connection", body: convertAWSConfirm(name), confirmLabel: "Move" }))) return false;
  pfcConverting = true;
  try {
    let res = null;
    let ack = false;
    for (;;) {
      try {
        res = await api("/api/profiles/" + encodeURIComponent(name) + "/convert-aws", {
          method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(ack ? { ack_write: true } : {}),
        });
        break;
      } catch (e) {
        if (e && e.status === 409 && e.code === "needs_ack" && !ack) {
          // The role the profile uses can write to the account: today's words, then the same request with ack_write.
          const v = (e.body && e.body.verdict) || {};
          const acts = Array.isArray(v.actions) && v.actions.length ? " Allowed here: " + v.actions.join(", ") + "." : "";
          const yes = await askConfirm({ title: "This role can change the account", body: CRED_D4 + " " + CRED_D4_REMEDY + acts, confirmLabel: "I understand, move it", danger: true });
          if (!yes) return false;
          ack = true;
          continue;
        }
        if (pfvState.name !== name) return false;
        // The stored profile moved under the convert: show what it holds now, with the sentence.
        if (e && e.code === "profile_changed") return pfcAfterConvert(name, convertAWSErrText(e));
        pfvState.errors = [convertAWSErrText(e) || awsErrText(e)];
        pfvRender();
        return false;
      }
    }
    return pfcAfterConvert(name, "Converted to the connection " + String(res && res.connection) + ". The profile now uses it; the next session gets a new box.");
  } finally {
    pfcConverting = false;
  }
}

// pfcAfterConvert: show what the server holds now, with the result line. The new connection is listed before the profile
// is drawn, or its row would read "Not found in Credentials".
async function pfcAfterConvert(name, line) {
  try {
    await loadProfiles(false);
    await refreshCredLists();
    const doc = await api("/api/profiles/" + encodeURIComponent(name));
    if (pfvState.name !== name) return true;
    pfvSeq++;
    pfvState.doc = doc;
    pfvDropEdits();
    pfvTakeRow(name);
    pfvState.errors = [];
    pfvState.warnings = [line];
    pfvRender();
  } catch (e) {
    if (pfvState.name === name) {
      pfvState.errors = ["Converted, but reloading the profile failed: " + e.message];
      pfvRender();
    }
  }
  return true;
}

// pfcBlankNew: a row that was added in this edit and is still empty is no change (a save drops it: the old form did). The props
// are the entry's fields that must all be empty.
const pfcBlankNew = (e, ...props) => e.fresh === true && props.every((p) => String(e[p] || "").trim() === "");

pfvRegisterEditor("cloud", {
  values: pfcCloudValues, draw: pfcCloudDraw, merge: pfcCloudMerge, check: pfcCloudCheck, add: pfcCloudAdd, remove: pfcCloudRemove,
  settled: (v) => Object.assign({}, v, { gh: v.gh.filter((g) => !pfcBlankNew(g, "owner", "repos")) }),
  // Only a box has this block, so a save always gives the next session a new box.
  note: (rec, c) => c.tier === "box",
});

pfvRegisterEditor("reachable", {
  values: pfcReachValues, draw: pfcReachDraw, merge: pfcReachMerge, check: pfcReachCheck, add: pfcReachAdd,
  settled: (v) => ({ folders: v.folders.filter((e) => !pfcBlankNew(e, "path")), domains: v.domains.filter((e) => !pfcBlankNew(e, "host")) }),
  note: (rec, c) => c.tier === "box",
});
