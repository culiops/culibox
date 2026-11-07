"use strict";

// The profile overview page (spec 5.1-5.4): Settings > Profiles on the desktop
// page is the read view of one profile, "This agent will be able to", with the
// profiles listed nested under Profiles in the Settings nav. profileview.js
// decides what the page says (pfvReadView); this file only draws it, so it has no
// wording of its own beyond the page's controls. Every string drawn is server or
// operator text and goes in through textContent only.
//
// Desktop only: the old form (.plist, .ped, #foot-manage) is hidden by desktop.css
// on the page and stays for the phone's modal. Edit opens one block as a small form
// (Runs in, Agent and Network here, Workspace in pfv-workspace.js, Cloud and tokens and
// Also reachable in pfv-cloud.js, Advanced in pfv-advanced.js). "+ New profile" and Duplicate open the New
// profile page of pfv-new.js, which replaces the profile on screen (pfvState.page). profileedit.js
// decides what a save writes and what a refusal means; this file draws the forms, keeps
// what the user has typed and runs the save. A block editor in a file of its own registers
// through pfvRegisterEditor (see PFV_EDITORS) and is loaded after this one.
// Classic script: api, askConfirm, openSpawnDialog, openSettings, loadProfiles,
// withPending, buildSeg and the profile globals are app.js's and the helpers',
// read at call time; every top-level name here starts with pfv/PFV_ (the page
// scripts share one scope).

// What the page shows. openBlock is the block the user opened ("" when none is open:
// every redraw hook below refuses to run while it is set, so nothing is drawn over a
// form). edits holds one record per open block (see pfvNewEdit): that is where the
// pending values live, so the page can rebuild a form from them. pending: some open
// block differs from what is stored. saved: the blocks whose "Saved" shows for a
// moment. warnings: the lines a successful save answered with. drawer: the credentials drawer is open (pfvOpenDrawer
// sets it, whichever way it closes clears it). picker: the block whose "+ Add" choice is open, "" when none; it is read from the open blocks
// (rec.ui.picker, which is also what draws the choice), never kept beside them, so it can neither claim a picker
// that a closed or redrawn block took with it nor miss one that is open. pfv-cloud.js's picker sets and clears
// rec.ui.picker. pfvEscTarget reads both, so Esc goes to the innermost open layer.
// page: "new" or "dup" while the New profile page is on screen instead of a profile (pfv-new.js; then there is no name, doc
// or view, and the page is the record edits.draft); src what a copy is made from, back the profile Cancel returns to,
// created the profile whose "Profile created" line the overview shows (createdBy "new": with its next steps, "dup": the line only).
const pfvState = {
  name: "", doc: null, view: null, openBlock: "", pending: false, errors: [], advOpen: null, edits: {}, saved: [], warnings: [], saving: false, drawer: false,
  page: "", src: null, back: "", created: "", createdBy: "",
  get picker() {
    return Object.keys(this.edits).find((i) => this.edits[i].ui && this.edits[i].ui.picker === true) || "";
  },
};
// The newest pfvShow wins: a slower answer for a profile you have left is dropped.
let pfvSeq = 0;
// What a forced close kept of an edit (pfvKeepEdits), until that profile opens on the desktop page again (pfvShow restores it) or it is
// gone: null, or {name, doc, openBlock, edits}: the profile, the stored profile the edit was made against, the block that was open and one
// {id, repair, values, init, ui} per open block. Plain data of this page load only, none of the page it came from.
let pfvKept = null;
// Counts the times the New profile page was opened: a pfvShow that was waiting for the list when it was must not draw over it.
let pfvPageGen = 0;
// What the page is drawn from, as a string (pfvKey) and what the nav lists (pfvNavKey):
// a refetch that would draw the same thing is skipped, so it cannot rebuild the page,
// drop focus or close the open menu.
let pfvDrawn = "";
let pfvNavKey = "";
// The open header menu: {wrap, btn, pop}, or null.
let pfvMenu = null;
// The nodes the last draw made that later code reaches for: the header's "Unsaved
// changes" note, each block's Edit link (to give focus back) and the "Saved" notes.
let pfvRefs = { unsaved: null, edit: {}, saved: [] };
// The timer that takes "Saved" away.
let pfvSavedTimer = null;
const PFV_SAVED_MS = 2000;

const PFV_SVG = "http://www.w3.org/2000/svg";
const PFV_EDIT_LABEL = {
  runs: "Edit where it runs", workspace: "Edit workspace", agent: "Edit agent", cloud: "Edit cloud and tokens",
  reachable: "Edit also reachable", network: "Edit network", advanced: "Edit advanced settings",
};
// The Advanced rows whose value is machine text (the rest are words).
const PFV_MONO_KINDS = ["Arguments", "Environment", "Secrets", "Starting environment"];
// Machine text inside a sentence: a path, a hostname, an account id, a region, an
// environment variable name, the box/<branch> pattern, the files a copy shares and
// the repos of a GitHub row. Mono is for machine text only (BRIEF); words stay Inter.
// Each rule is [pattern, allowed]: the pattern is tried at one position (sticky), and
// allowed(text, position), when there is one, says whether the text before that position
// lets it start there. That is a lookbehind written out by hand: a regex lookbehind is a
// SyntaxError on Safari before 16.4 (macOS 13 ships such a Safari), and one in this file
// blanked the whole Profiles page (no-lookbehind.test.js keeps it out). pfvMonoNodes tries
// the rules in order at every position, which is what one alternation of them would do.
const pfvBefore = (re) => (s, at) => !re.test(s.charAt(at - 1));
const PFV_MACHINE = [
  [/~?(?:\/[\w.@+-]+)+\/?/y, pfvBefore(/[\w.]/)],
  [/(?:[a-z0-9*][a-z0-9-]*\.)+[a-z]{2,}(?![\w-])/y, pfvBefore(/[\w.@/-]/)],
  [/\d{12}(?!\d)/y, pfvBefore(/\d/)],
  [/\b[a-z]{2}(?:-[a-z]+)+-\d\b/y, null],
  [/\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b/y, null],
  [/\bbox\/<branch>/y, null],
  [/.+?(?= from the original)/y, (s, at) => s.slice(0, at) === "Shares "],
  [/.+$/y, (s, at) => s.slice(0, at) === "repo "],
];

function pfvMk(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// pfvIcon: a small inline SVG from [shape, attributes] pairs. The class goes on
// through the attribute: an SVG element's className is read-only.
function pfvIcon(cls, w, h, box, shapes) {
  const s = document.createElementNS(PFV_SVG, "svg");
  s.setAttribute("class", cls);
  s.setAttribute("width", String(w));
  s.setAttribute("height", String(h));
  s.setAttribute("viewBox", box);
  s.setAttribute("aria-hidden", "true");
  for (const [shape, attrs] of shapes) {
    const n = document.createElementNS(PFV_SVG, shape);
    for (const k of Object.keys(attrs)) n.setAttribute(k, attrs[k]);
    s.appendChild(n);
  }
  return s;
}
const PFV_STROKE = { fill: "none", stroke: "currentColor", "stroke-width": "1.2" };
function pfvLockIcon() {
  return pfvIcon("pfv-lock", 10, 11, "0 0 10 11", [
    ["rect", Object.assign({ x: "1.5", y: "4.6", width: "7", height: "5.4", rx: "1.2" }, PFV_STROKE)],
    ["path", Object.assign({ d: "M3.2 4.6V3.3a1.8 1.8 0 0 1 3.6 0v1.3" }, PFV_STROKE)],
  ]);
}
function pfvChevronIcon() {
  return pfvIcon("pfv-chev", 10, 10, "0 0 10 10", [
    ["path", { d: "M3.5 2l3 3-3 3", fill: "none", stroke: "currentColor", "stroke-width": "1.5", "stroke-linecap": "round", "stroke-linejoin": "round" }],
  ]);
}
function pfvDotsIcon() {
  return pfvIcon("pfv-dots", 14, 14, "0 0 14 14", [
    ["circle", { cx: "2.5", cy: "7", r: "1.3", fill: "currentColor" }],
    ["circle", { cx: "7", cy: "7", r: "1.3", fill: "currentColor" }],
    ["circle", { cx: "11.5", cy: "7", r: "1.3", fill: "currentColor" }],
  ]);
}

// pfvMonoNodes: `text` as nodes, machine text in .pfv-mono spans. `known` lists
// exact strings that are machine text too (the model id).
function pfvMonoNodes(text, known) {
  const s = String(text == null ? "" : text);
  const esc = (k) => k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const own = (Array.isArray(known) ? known : []).filter((k) => typeof k === "string" && k !== "").map(esc);
  const rules = (own.length ? [[new RegExp("(?:" + own.join("|") + ")", "y"), null]] : []).concat(PFV_MACHINE);
  const out = [];
  let at = 0; // where the text not yet put in a node starts
  let i = 0;
  while (i < s.length) {
    let hit = "";
    for (const [re, allowed] of rules) {
      re.lastIndex = i;
      const m = re.exec(s);
      if (m && (!allowed || allowed(s, i))) { hit = m[0]; break; }
    }
    if (hit === "") { i++; continue; }
    if (i > at) out.push(s.slice(at, i));
    out.push(pfvMk("span", "pfv-mono", hit));
    i += hit.length;
    at = i;
  }
  if (at < s.length) out.push(s.slice(at));
  return out;
}

// pfvInputs: what pfvReadView reads, from the page's own globals (app.js). Until
// /api/meta has answered nothing is known of its lists or of the box layer, so
// neither is passed: no name is called missing and no box is called unavailable.
function pfvInputs() {
  const meta = typeof metaLoaded === "boolean" && metaLoaded;
  return {
    view: pfvState.view,
    doc: pfvState.doc,
    meta: meta ? { aws_connections: awscEntries, static_tokens: stEntries, github_apps: ghApps } : null,
    creds: typeof pfCredsView === "object" ? pfCredsView : null,
    boxRuntime: meta ? boxRuntime : undefined,
    boxUnavailableReason: typeof boxUnavailableReason === "string" ? boxUnavailableReason : "",
    bundleImports: typeof bundleImports === "boolean" ? bundleImports : false,
    defs: typeof optionDefs !== "undefined" ? optionDefs : [],
  };
}

// pfvOnPage: the desktop Settings page is showing (a phone, or a modal opened
// before the window widened, keeps today's form).
function pfvOnPage() {
  const dlg = document.getElementById("settings-modal");
  return !!dlg && dlg.classList.contains("as-page");
}

// --- the nav list -------------------------------------------------------------

// pfvRenderNav: the profiles nested under Profiles in the Settings nav: name, the
// muted tier word, a lock for one you cannot edit, then "+ New profile". The list is
// rebuilt only when what it shows changed (every loadProfiles hands back new objects);
// otherwise only the current row's aria-current moves, so a refetch or a click never
// replaces the button that has focus.
function pfvRenderNav() {
  const nav = document.getElementById("pf-navlist");
  if (!nav || !pfvOnPage()) return; // a phone's modal keeps its own list
  const list = typeof profiles === "object" && Array.isArray(profiles) ? profiles : [];
  const key = JSON.stringify(list.map((p) => [p.name, p.runtime === "host" ? "host" : "box", !p.editable]));
  if (key !== pfvNavKey || nav.children.length === 0) {
    const at = document.activeElement;
    const had = at && nav.contains(at) ? at.dataset.pfvName : undefined; // a profile's name, "" for the add row
    const kids = list.map((p) => {
      const b = pfvMk("button", "pfv-ni");
      b.type = "button";
      b.dataset.pfvName = p.name;
      const tier = pfvMk("span", "pfv-ni-tier");
      if (!p.editable) {
        b.setAttribute("title", "Defined in config.json (read-only)");
        tier.append(pfvLockIcon(), pfvMk("span", "pfv-sr", "read-only, "));
      }
      tier.append(p.runtime === "host" ? "host" : "box");
      b.append(pfvMk("span", "pfv-ni-name", p.name), tier);
      b.onclick = () => pfvSwitch(p.name);
      return b;
    });
    const add = pfvMk("button", "pfv-ni pfv-ni-add", "+ New profile");
    add.type = "button";
    add.dataset.pfvName = "";
    add.onclick = () => pfvNew();
    nav.replaceChildren(...kids, add);
    pfvNavKey = key;
    if (had !== undefined) {
      const again = Array.from(nav.children).find((b) => b.dataset.pfvName === had);
      if (again) again.focus();
    }
  }
  for (const b of Array.from(nav.children)) {
    // The New profile page (and a copy's, which is one) is "+ New profile"; no profile is current while it is on screen.
    const on = b.dataset.pfvName === "" ? pfvState.page !== "" : b.dataset.pfvName === pfvState.name;
    if (on) b.setAttribute("aria-current", "true");
    else b.removeAttribute("aria-current");
  }
  // The list was refetched: the page reads the fresh row, and redraws only if it differs.
  const v = pfvState.name && pfvState.doc ? list.find((x) => x.name === pfvState.name) : null;
  if (v) {
    pfvState.view = v;
    pfvRefresh();
  }
}

// --- the header -----------------------------------------------------------------

function pfvBtn(label, kind, onclick) {
  const b = pfvMk("button", "pfv-btn pfv-btn-" + kind, label);
  b.type = "button";
  if (onclick) b.onclick = onclick;
  return b;
}

function pfvMenuClose(refocus) {
  if (!pfvMenu) return;
  const m = pfvMenu;
  pfvMenu = null;
  m.pop.hidden = true;
  m.btn.setAttribute("aria-expanded", "false");
  if (refocus) m.btn.focus();
}

// pfvMoreMenu: the ⋯ button and its menu, Duplicate and Delete… (red). It closes
// on a second press, a click elsewhere and Esc (which must not also close the page).
function pfvMoreMenu(name) {
  const wrap = pfvMk("div", "pfv-more");
  const btn = pfvBtn("", "secondary pfv-btn-icon");
  btn.setAttribute("aria-label", "More actions for " + name);
  btn.setAttribute("aria-haspopup", "menu");
  btn.setAttribute("aria-expanded", "false");
  btn.append(pfvDotsIcon());
  const pop = pfvMk("div", "pfv-menu");
  pop.setAttribute("role", "menu");
  pop.hidden = true;
  const item = (label, cls, fn) => {
    const i = pfvMk("button", "pfv-menu-item" + cls, label);
    i.type = "button";
    i.setAttribute("role", "menuitem");
    i.onclick = fn;
    return i;
  };
  pop.append(
    item("Duplicate", "", () => { pfvMenuClose(false); return pfvDuplicate(); }),
    item("Delete…", " pfv-menu-red", () => { pfvMenuClose(false); return pfvDelete(); }),
  );
  btn.onclick = () => {
    if (pfvMenu && pfvMenu.wrap === wrap) { pfvMenuClose(false); return; }
    pfvMenuClose(false);
    pfvMenu = { wrap, btn, pop };
    pop.hidden = false;
    btn.setAttribute("aria-expanded", "true");
  };
  wrap.addEventListener("keydown", (ev) => {
    if (ev.key !== "Escape" || !pfvMenu) return;
    ev.preventDefault();
    pfvMenuClose(true);
  });
  wrap.append(btn, pop);
  return wrap;
}

function pfvDrawHeader(rv, known) {
  const h = rv.header;
  const head = pfvMk("div", "pfv-head");
  const left = pfvMk("div", "pfv-head-main");
  const title = pfvMk("div", "pfv-title");
  title.append(pfvMk("h2", "pfv-title-name", h.name), pfvMk("span", "pfv-chip" + (h.tier === "host" ? " pfv-chip-host" : ""), h.tier));
  const sub = pfvMk("p", "pfv-hsub");
  sub.append(...pfvMonoNodes(h.agentLine, known));
  left.append(title, sub);
  const acts = pfvMk("div", "pfv-acts");
  // Shown while any open block differs from what is stored; "Show" takes you to it.
  const unsaved = pfvMk("span", "pfv-unsaved");
  unsaved.hidden = !pfvAnyDirty();
  const show = pfvDrawLink("Show", "", "Show the block with unsaved changes");
  show.onclick = () => pfvShowBlock(pfvFirstDirty());
  unsaved.append(pfvMk("span", "pfv-udot"), "Unsaved changes", show);
  pfvRefs.unsaved = unsaved;
  acts.append(unsaved, pfvBtn("Start session", "secondary", () => openSpawnDialog(h.name)));
  // Read-only: Duplicate is the way to a profile you can change, so it leads and
  // there is no menu (nothing to delete).
  if (h.readOnly) acts.append(pfvBtn("Duplicate", "primary", () => pfvDuplicate()));
  else acts.append(pfvMoreMenu(h.name));
  head.append(left, acts);
  return head;
}

// --- the blocks -----------------------------------------------------------------

function pfvDrawLink(label, cls, aria) {
  const b = pfvMk("button", "pfv-link" + (cls ? " " + cls : ""), label);
  b.type = "button";
  if (aria) b.setAttribute("aria-label", aria);
  return b;
}

// o: {block, edit, bad}. block: the block the row belongs to (the read view's Remove link opens it with the row
// removed); edit: the row is drawn in the open block, whose Remove column is the way out, so the link is not drawn;
// bad: called with the words of a refusal that names the row, so the open block can point its control at them.
function pfvDrawScope(r, known, o) {
  const p = pfvMk("p", "pfv-scope");
  const at = o || {};
  if (r.missing) {
    // A bound name /api/meta no longer lists: the server refuses every save until
    // it is removed, so it is an error and says so in the error colour.
    p.append(pfvMk("span", "pfv-miss", r.scope));
    if (!at.edit) {
      const rm = pfvDrawLink("Remove", "pfv-rm");
      rm.onclick = () => pfvOpenRemove(at.block, r);
      p.append(" · ", rm);
    }
    return p;
  }
  p.append(...pfvMonoNodes(r.scope, known));
  if (r.action) {
    const act = pfvDrawLink(r.action, "pfv-act-link");
    act.onclick = () => pfvConvertAws();
    p.append(" ", act);
  }
  return p;
}

// err: a refusal that names this row (a folder that is gone, say), drawn under it in words. o: pfvDrawScope's.
function pfvDrawRow(r, known, err, o) {
  const row = pfvMk("div", "pfv-row");
  // The old-style AWS setting is an AWS row named for what it is (the kind column is 52px: "Old-style AWS" would wrap
  // in three lines); the binding's own label is its sub-line.
  const old = r.kind === "Old-style AWS";
  row.append(pfvMk("span", "pfv-k", old ? "AWS" : r.kind));
  const id = pfvMk("div", "pfv-id");
  const nm = pfvMk("p", "pfv-nm");
  // The file shared from a private copy's original is a path; the CLAUDE.md imports row, also Files, is words.
  if (old) nm.append("Old-style AWS setting");
  else if (r.original === true || PFV_MONO_KINDS.includes(r.kind)) nm.append(pfvMk("span", "pfv-mono", r.name));
  else nm.append(...pfvMonoNodes(r.name, known));
  if (r.tag) nm.append(pfvMk("span", "pfv-tag", r.tag));
  id.append(nm);
  const sub = old ? r.name : r.sub;
  if (sub) {
    const s = pfvMk("p", "pfv-rsub");
    s.append(...pfvMonoNodes(sub, known));
    id.append(s);
  }
  if (err) {
    const words = pfvMk("p", "pfv-ferr", err);
    if (o && typeof o.bad === "function") o.bad(words);
    id.append(words);
  }
  row.append(id, pfvDrawScope(r, known, o));
  return row;
}

// pfvDrawBranches: the branches that did not come home, today's chip and per-branch
// rows (amber, as the old list and form paint them) inside the Workspace block.
function pfvDrawBranches(notHome, rows) {
  const row = pfvMk("div", "pfv-row");
  row.append(pfvMk("span", "pfv-k", "Branches"));
  const box = pfvMk("div", "pfv-nhome");
  const chip = pfvMk("span", "pfv-nh-chip", notHome.label);
  if (notHome.title) chip.setAttribute("title", notHome.title);
  box.append(chip);
  for (const r of rows) {
    const nr = pfvMk("div", "pfv-nh-row");
    nr.append(pfvMk("span", "pfv-nh-why", r.scope));
    if (r.fix) nr.append(pfvMk("span", "pfv-nh-fix", r.fix));
    if (r.sub) nr.append(pfvMk("span", "pfv-nh-detail", r.sub));
    box.append(nr);
  }
  row.append(box);
  return row;
}

// rec: the block's edit record when it is open without a form (a refused save sent the
// user here): the refusals that name one of its rows are drawn under that row.
function pfvDrawBody(b, known, rec) {
  const body = pfvMk("div", "pfv-body");
  const empty = b.rows.length === 0 && (b.id === "cloud" || b.id === "reachable");
  b.lines.forEach((line, i) => {
    if (line === "") return;
    const cls = empty ? "pfv-empty" : i === 0 ? "pfv-main" : "pfv-sub";
    const p = pfvMk("p", cls + (b.tone === "error" && i === b.toneAt ? " pfv-red" : ""));
    p.append(...pfvMonoNodes(line, known));
    body.append(p);
  });
  if (b.rows.length || b.notHome) {
    const rows = pfvMk("div", "pfv-rows");
    const nh = b.rows.filter((r) => r.kind === "Not home");
    for (const r of b.rows) {
      if (r.kind === "Not home") continue;
      const e = rec ? rec.ferrs.find((x) => pfvRowForField(b.rows, x.field) === r) : null;
      if (e) rec.consumed.add(e.field);
      rows.append(pfvDrawRow(r, known, e ? e.text : "", { block: b.id }));
    }
    if (b.notHome) rows.append(pfvDrawBranches(b.notHome, nh));
    body.append(rows);
  }
  if (b.note) body.append(pfvMk("p", "pfv-tnote", b.note));
  if (b.add) {
    const add = pfvDrawLink(b.add, "pfv-add");
    add.onclick = () => pfvOpenAdd(b.id);
    body.append(add);
  }
  // Offered by the read model only where it can be used.
  if (b.switchToBox) {
    const act = pfvMk("div", "pfv-stmt-act");
    act.append(pfvBtn("Switch to box", "secondary", pfvSwitchToBox));
    body.append(act);
  }
  return body;
}

// pfvDrawAct: the Edit link, and the "Saved" note for a moment after a save. Only the blocks
// that have an editor open; the others are drawn but inert until their task.
function pfvDrawAct(b) {
  const act = pfvMk("div", "pfv-act");
  if (b.edit) {
    const link = pfvDrawLink("Edit", "", PFV_EDIT_LABEL[b.id] || "Edit " + b.label.toLowerCase());
    if (PFV_EDITORS[b.id]) link.onclick = () => pfvOpenBlock(b.id);
    pfvRefs.edit[b.id] = link;
    act.append(link);
  }
  if (pfvState.saved.includes(b.id)) {
    const note = pfvMk("span", "pfv-saved");
    note.setAttribute("role", "status");
    note.append(pfvIcon("pfv-saved-ic", 12, 12, "0 0 12 12", [
      ["path", { d: "M2.5 6.4l2.3 2.3 4.7-5", fill: "none", stroke: "currentColor", "stroke-width": "1.6", "stroke-linecap": "round", "stroke-linejoin": "round" }],
    ]), "Saved");
    pfvRefs.saved.push(note);
    act.append(note);
  }
  return act;
}

// pfvAdvLabel: what the Advanced label says: the chevron, the name and how many are set.
const pfvAdvLabel = (b) => [pfvChevronIcon(), b.label, " ", pfvMk("span", "pfv-muted", "· " + b.status)];

// pfvDrawAdvanced: the folded block. "Advanced · N set" opens by itself when N > 0
// (pfvState.advOpen overrides once the user has toggled it); with nothing set there
// is nothing to unfold, so the label is not a button.
function pfvDrawAdvanced(b, known) {
  const open = b.count > 0 && (pfvState.advOpen === null ? b.open === true : pfvState.advOpen);
  const sec = pfvMk("section", "pfv-block" + (open ? " pfv-open" : ""));
  sec.dataset.block = b.id;
  const label = pfvMk("h4", "pfv-label pfv-adv-label");
  if (b.count > 0) {
    const fold = pfvMk("button", "pfv-fold");
    fold.type = "button";
    fold.setAttribute("aria-expanded", open ? "true" : "false");
    fold.append(...pfvAdvLabel(b));
    fold.onclick = () => { pfvState.advOpen = !open; pfvRender(); };
    label.append(fold);
  } else {
    label.append(...pfvAdvLabel(b));
  }
  const body = pfvMk("div", "pfv-body pfv-adv-body");
  if (open) {
    const dl = pfvMk("dl", "pfv-dl");
    for (const r of b.rows) {
      const dd = pfvMk("dd");
      if (PFV_MONO_KINDS.includes(r.kind)) dd.append(pfvMk("span", "pfv-mono", r.name));
      else dd.append(...pfvMonoNodes(r.name, known));
      if (r.scope) dd.append(" ", pfvMk("span", "pfv-muted", r.scope));
      dl.append(pfvMk("dt", "", r.kind), dd);
    }
    body.append(dl);
  } else {
    body.append(pfvMk("p", "pfv-adv-sum", b.lines[0] || ""));
  }
  sec.append(label, pfvDrawAct(b), body);
  return sec;
}

function pfvDrawBlock(b, known) {
  const rec = pfvState.edits[b.id];
  if (rec) return pfvDrawEditBlock(b, rec, known);
  if (b.id === "advanced") return pfvDrawAdvanced(b, known);
  const sec = pfvMk("section", "pfv-block");
  sec.dataset.block = b.id;
  sec.append(pfvMk("h4", "pfv-label", b.label), pfvDrawBody(b, known), pfvDrawAct(b));
  return sec;
}

// pfvDrawNote: the quiet line of a profile defined in config.json. Its file name is
// machine text, like everywhere else on the page.
function pfvDrawNote(text) {
  const p = pfvMk("p", "pfv-note");
  const words = pfvMk("span", "pfv-note-text");
  words.append(...pfvMonoNodes(text, []));
  p.append(pfvLockIcon(), words);
  return p;
}

// pfvRender: the page for pfvState, from the read model.
function pfvRender() {
  const root = document.getElementById("pfv-in");
  if (!root) return;
  // The New profile page (pfv-new.js) is drawn from its record, not from a profile.
  const draft = pfvState.page ? pfvState.edits.draft : null;
  if (draft && PFV_EDITORS.draft) {
    pfvRefs = { unsaved: null, edit: {}, saved: [] };
    root.replaceChildren(...PFV_EDITORS.draft.draw(draft, pfvEditCtx()));
    pfvDrawn = "";
    return;
  }
  if (!pfvState.name || !pfvState.view || !pfvState.doc) {
    root.replaceChildren(...pfvDrawErrors());
    pfvDrawn = "";
    return;
  }
  const rv = pfvReadView(pfvInputs());
  pfvRefs = { unsaved: null, edit: {}, saved: [] };
  const o = pfvState.doc.options && typeof pfvState.doc.options === "object" ? pfvState.doc.options : {};
  const known = typeof o.model === "string" && o.model !== "" ? [o.model] : [];
  const sheet = pfvMk("div", "pfv-sheet");
  const tail = pfvMk("div", "pfv-sheet");
  const heading = pfvMk("h3", "pfv-sheet-h", "This agent will be able to");
  for (const b of rv.blocks) (b.id === "advanced" ? tail : sheet).append(pfvDrawBlock(b, known));
  const kids = [...pfvDrawErrors(), pfvDrawHeader(rv, known)];
  if (rv.note) kids.push(pfvDrawNote(rv.note));
  for (const w of pfvState.warnings) kids.push(pfvMk("p", "pfv-warn", w));
  // Right after Create: what the new profile may need next (pfv-new.js).
  if (pfvState.created !== "" && pfvState.created === pfvState.name) kids.push(pfvNextSteps(rv));
  kids.push(heading, sheet, tail);
  root.replaceChildren(...kids);
  pfvMenu = null;
  pfvDrawn = pfvKey();
}

// pfvCredsKey: what the read model takes from /api/credentials: whether the store is
// locked, and each connection's name, production mark, mode and elevate window
// (spawnpreview.js reachModel). The rest of the payload (the checks' timestamps, which
// move every few seconds) says nothing the page draws and must not redraw it.
function pfvCredsKey(creds) {
  if (!creds || typeof creds !== "object") return null;
  const st = creds.store && typeof creds.store === "object" ? creds.store : {};
  const aws = Array.isArray(creds.aws) ? creds.aws : [];
  return [
    [st.initialized, st.locked, st.platform],
    aws.map((e) => (e && typeof e === "object" ? [e.name, e.prod, e.mode, e.elevate && typeof e.elevate === "object" ? e.elevate.max_minutes : null] : null)),
  ];
}

// pfvKey: everything the page is drawn from (the model's inputs and the messages).
function pfvKey() {
  const inp = pfvInputs();
  return JSON.stringify([Object.assign({}, inp, { creds: pfvCredsKey(inp.creds) }), pfvState.errors]);
}

// pfvRefresh: redraw only when what the page is drawn from changed, and never while a
// block is being edited. The list, /api/meta and /api/credentials all refetch often
// and mostly answer the same thing.
function pfvRefresh() {
  if (!pfvState.doc || pfvState.openBlock) return;
  if (pfvKey() !== pfvDrawn) pfvRender();
}

function pfvDrawErrors() {
  return pfvState.errors.map((m) => {
    const p = pfvMk("p", "pfv-err", m);
    p.setAttribute("role", "alert");
    return p;
  });
}

// --- showing and deleting ---------------------------------------------------------

// pfvShow: the page for one profile (the one on screen, else the first you own,
// else the first), from the list's row and the stored profile. Switching profile
// blanks the pane until the new one's answer lands (it never shows the wrong
// profile under a highlighted nav row); showing the one on screen again keeps it
// drawn and redraws only if the answer differs. While a block of that profile is
// open it does nothing at all: the tab being selected again must not redraw or
// refetch from under an edit. A profile that a forced close kept an edit of (pfvKept) comes back with it: once the
// profile has been read, the block opens again with what was typed (pfvRestoreKept).
async function pfvShow(name) {
  if (!pfvOnPage()) return;
  // The Profiles tab selected again (no profile named) leaves the New profile page as it is, with what was typed.
  if (!name && pfvState.page) return;
  const gen = pfvPageGen;
  if (typeof profilesLoaded === "boolean" && !profilesLoaded) await loadProfiles(false);
  // A New profile page came up while the list loaded: it wins.
  if (gen !== pfvPageGen) return;
  const has = (n) => profiles.some((p) => p.name === n);
  let pick = name;
  if (!pick || !has(pick)) pick = has(pfvState.name) ? pfvState.name : (profiles.find((p) => p.editable) || profiles[0] || {}).name || "";
  if (pick !== "" && pick === pfvState.name && pfvState.openBlock) return;
  // An edit kept for a profile the list no longer has (deleted elsewhere) has nowhere to go.
  if (pfvKept && !has(pfvKept.name)) pfvKept = null;
  const seq = ++pfvSeq;
  const same = pick !== "" && pick === pfvState.name && pfvState.doc !== null;
  if (pick !== pfvState.name) {
    pfvState.doc = null;
    pfvDropEdits();
    pfvState.advOpen = null;
    pfvState.errors = [];
    pfvState.warnings = [];
    pfvClearSaved();
    // Leaving the New profile page, and the first steps of a profile that is not the one on screen.
    pfvState.page = "";
    pfvState.src = null;
    if (pick !== pfvState.created) pfvState.created = "";
  }
  pfvState.name = pick;
  pfvState.view = pick ? profiles.find((p) => p.name === pick) : null;
  pfvRenderNav();
  if (!pick) {
    // Nothing to show (no profile at all): the way on is a new profile, not a blank pane. Whatever page was up goes with its record.
    pfvState.doc = null;
    pfvDropEdits();
    pfvState.page = "";
    pfvState.src = null;
    await pfvNew();
    return;
  }
  if (!pfvState.doc) pfvRender();
  let doc;
  try { doc = await api("/api/profiles/" + encodeURIComponent(pick)); }
  catch (e) {
    // The profile an edit was kept for is gone: what was typed has nowhere to go. Any other failure says nothing about it.
    if (pfvKept && pfvKept.name === pick && e && e.status === 404) pfvKept = null;
    // A block opened while this was out is being edited: the failed refresh says nothing it needs.
    if (seq === pfvSeq && !pfvState.openBlock) { pfvState.errors = ["Load failed: " + e.message]; pfvRender(); }
    return;
  }
  if (seq !== pfvSeq) return;
  if (same && pfvState.openBlock) return; // a block was opened while this was out
  // The edit a forced close kept of this profile comes back, drawn from what was typed (the read above showed the profile is there).
  if (pfvKept && pfvKept.name === pick) {
    pfvRestoreKept();
    return;
  }
  pfvState.doc = doc;
  pfvState.errors = [];
  if (same) pfvRefresh();
  else pfvRender();
}

// pfvNextAfter: the profile to show once `name` is gone: the one after it in list order,
// the one before it when it was last. order is the list as it was, names that are still listed qualify.
function pfvNextAfter(order, name) {
  const at = order.indexOf(name);
  return order.slice(at + 1).concat(order.slice(0, at).reverse()).find((n) => n !== name && profiles.some((p) => p.name === n));
}

// pfvMoveOn: the profile `name` is gone, deleted here or elsewhere. It leaves the screen and the
// nav at once, not when the list reloads, and is dropped from the page's list first, so a reload that
// fails or is slow cannot bring it back. Then the next profile shows. Its open blocks and their
// pending values go with it. A user who moved on meanwhile stays where they are.
async function pfvMoveOn(name, order) {
  const onIt = pfvState.name === name; // false: the user moved on while it was being removed
  profiles = profiles.filter((p) => p.name !== name);
  if (onIt) {
    // No profile selected until the next one is shown, or the New profile page when there is none.
    pfvState.name = "";
    pfvState.doc = null;
    pfvState.view = null;
    pfvDropEdits();
    pfvState.warnings = [];
    pfvClearSaved();
    pfvRender();
  }
  pfvRenderNav();
  await loadProfiles(false);
  if (!onIt || pfvState.name !== "") return;
  // pfvShow opens the New profile page (pfv-new.js) when there is nothing left to show.
  await pfvShow(pfvNextAfter(order, name) || "");
}

// pfvDelete: confirm in red, delete, then show the next profile in list order.
async function pfvDelete() {
  const name = pfvState.name;
  if (!name) return;
  const order = profiles.map((p) => p.name);
  const ok = await askConfirm({
    title: "Delete profile",
    body: 'Delete the profile "' + name + '"? Running sessions are unaffected. The profile\'s saved workspace default and environment allowlist will be lost. This cannot be undone.',
    confirmLabel: "Delete",
    danger: true,
  });
  if (!ok) return;
  pfvState.errors = [];
  try { await api("/api/profiles/" + encodeURIComponent(name), { method: "DELETE" }); }
  catch (e) { pfvState.errors = ["Delete failed: " + e.message]; pfvRender(); return; }
  await pfvMoveOn(name, order);
}

// --- editing: the records ----------------------------------------------------------------

// What each editable block is. values(c): the controls' starting values from the stored
// profile (c is pfvEditCtx). draw(rec, c): the form's nodes; what it builds that outlives
// the nodes (a picker's listeners) goes on rec.disposers, run before the next draw and when
// the record goes. merge(rec, c): the values pfvMergeBlock is given (default: rec.values).
// check(rec, c): what the client refuses before any PUT, {field, text} or null. note(rec, c):
// the footer carries the box consequence. hideSave(rec, c): Save is not offered. saveLabel(rec, c): what Save reads
// when it is not "Save" (and it is then drawn as a way into containment loss: red). settled(values): the values as they
// count when a change is looked for (a row added and left blank is no change). fields: the
// form is a stack of labelled fields (a 16px rhythm, 440px wide); without it the form flows
// as it likes. A block of rows has two more: add(rec, what) puts a pending row in (what is
// pfvAddRow's) and answers the key of the control to focus, "" when it added nothing;
// remove(rec, row) marks the read view's row removed. keep: the names of the rec.ui fields that hold what the user typed or
// chose (a stash, a text the form is not showing), as opposed to what the form built (nodes, pickers, a fetch's flag): they go
// with the edit when a forced close keeps it (pfvKeepEdits), and nothing else of rec.ui does. A block with no entry here is drawn
// but its Edit link does nothing; one that a refused save opens
// is drawn read-only with the refusal under the row it names, so the user can see what is wrong.
const PFV_EDITORS = {
  runs: { values: pfvRunsValues, draw: pfvRunsForm, merge: pfvRunsMerge, note: pfvRunsNote, hideSave: pfvRunsHideSave, saveLabel: pfvRunsSaveLabel },
  // The command typed under "Other command..." waits behind the select when another command is picked, for the way back.
  agent: { values: pfvAgentValues, draw: pfvAgentForm, merge: pfvAgentMerge, fields: true, keep: ["binOther", "otherText"] },
  network: { values: pfvNetworkValues, draw: pfvNetworkForm, note: pfvNetworkNote },
};

// pfvRegisterEditor: the seam for an editor that lives in its own script (pfv-workspace.js, and the
// Cloud and tokens, Also reachable and Advanced scripts after it). It runs while the page loads, before
// the first draw, which is when the Edit links are wired.
function pfvRegisterEditor(id, spec) {
  if (PFV_EDITORS[id]) throw new Error("profile block editor registered twice: " + id);
  PFV_EDITORS[id] = spec;
}

const PFV_NEW_BOX = "Saving gives the next session a new box. Running sessions keep theirs.";
const PFV_NUDGE = "Save or discard these changes first";
const PFV_NO_COMMAND = "A command is required.";
// What a refused field is, for the note on the block that was being saved ("Not saved: a folder in Also reachable ...").
const PFV_THING = new Map([
  ["context_paths", "a folder"], ["egress_allow", "a domain"], ["aws_connections", "an AWS connection"], ["aws", "the old-style AWS setting"],
  ["tokens", "a token"], ["github", "a GitHub setting"], ["clone_of", "a folder"], ["default_cwd", "a folder"], ["clone_share", "a shared file"],
  ["bin", "the command"], ["account", "the login"], ["options", "an option"], ["base", "the starting environment"], ["args", "an argument"],
  ["env", "a variable"], ["secrets", "a secret"], ["egress_open", "a network setting"], ["runtime", "where it runs"],
]);
// The row a refused field names, by kind and position (the read model lists folders, domains, connections,
// tokens and GitHub owners in the profile's own order).
const PFV_FIELD_ROW = [
  [/^context_paths\[(\d+)\]/, "Folder"], [/^egress_allow\[(\d+)\]/, "Domain"], [/^aws_connections\[(\d+)\]/, "AWS"],
  [/^tokens\[(\d+)\]/, "Token"], [/^github\.owners\[(\d+)\]/, "GitHub"],
];
const PFV_EGRESS_WARNING = /^egress_allow and egress_open apply only to a profile with a credential binding/;
const PFV_EGRESS_WORDS = "Network settings apply only to a profile that holds cloud access or a token; without one the box already reaches any public host.";

let pfvUid = 0;

// pfvBlank: a value that counts as "not set" when two sets of values are compared.
const pfvBlank = (x) => x === undefined || x === null || x === "" || x === false;
// pfvStable: the values as one string that does not depend on key order or on a key set to
// "not set" versus absent, so "changed and changed back" reads as no change.
function pfvStable(v) {
  if (Array.isArray(v)) return "[" + v.map(pfvStable).join(",") + "]";
  if (v && typeof v === "object") {
    return "{" + Object.keys(v).sort().filter((k) => !pfvBlank(v[k])).map((k) => JSON.stringify(k) + ":" + pfvStable(v[k])).join(",") + "}";
  }
  return JSON.stringify(v === undefined ? null : v);
}

// pfvEditCtx: what the forms read the page's state for.
function pfvEditCtx() {
  const inp = pfvInputs();
  const doc = pfvState.doc || {};
  const view = pfvState.view || {};
  return {
    doc,
    view,
    tier: pfvTierOf(view, doc),
    // /api/meta says "" when there is no box layer; before it has answered boxes are assumed (as the read view does).
    boxAvailable: inp.boxRuntime !== "",
    reason: inp.boxUnavailableReason,
    defs: Array.isArray(inp.defs) ? inp.defs : [],
  };
}

// pfvNewEdit: the record of one open block. values: what its controls hold (null for a block
// opened only to show a refusal); init: the same as opened, to tell a change from none.
// repair: opened by a refused save. nudge: an Edit elsewhere was refused. top: the block's
// message ({kind, text, target}). ferrs: refusals to draw under their input ({field, text}).
// consumed: the fields the form drew an error for (the rest go to the top). fields: the controls
// by field key (focus goes back to one after a redraw). ui: state of the form that is not a value.
// sent: the block was part of the last save (pfvSaveBlock), so a refusal's list index counts in the list its merge
// built; a block that was not (a repair block, or one open and clean) had its stored lists sent as they are.
// seed: a record a forced close kept ({values, init, ui}, see pfvKept): the form starts from what was typed, not from the stored
// profile, and init stays what it was when the block was first opened, so a change is still told from none.
function pfvNewEdit(id, repair, seed) {
  const ed = PFV_EDITORS[id];
  const values = seed ? seed.values : ed ? ed.values(pfvEditCtx()) : null;
  const init = seed ? seed.init : pfvStable(pfvSettled(id, values));
  return { id, values, init, repair: repair === true, sent: false, nudge: false, top: null, ferrs: [], consumed: new Set(), nodes: null, sigs: {}, fields: {}, first: null, block: null, known: [], ui: seed ? seed.ui : {}, disposers: [], invalid: [] };
}

// pfvDisposeRec: the record's form is going away (drawn again, closed or dropped): what it built that
// outlives its nodes, a picker's listeners and timers, is released.
function pfvDisposeRec(rec) {
  const fns = rec.disposers;
  rec.disposers = [];
  for (const f of fns) f();
}

// pfvSettled: the values as they count when two sets are compared. An editor may say what is no change even though the values
// differ (its settled(values)): a row that was added and is still blank.
const pfvSettled = (id, values) => (values !== null && PFV_EDITORS[id] && PFV_EDITORS[id].settled ? PFV_EDITORS[id].settled(values) : values);
const pfvEditDirty = (rec) => !!rec && rec.values !== null && pfvStable(pfvSettled(rec.id, rec.values)) !== rec.init;
const pfvAnyDirty = () => Object.values(pfvState.edits).some(pfvEditDirty);
// The block "Show" goes to: the first one with changes, else the first one open.
function pfvFirstDirty() {
  const ids = Object.keys(pfvState.edits);
  return ids.find((i) => pfvEditDirty(pfvState.edits[i])) || ids[0] || "";
}

// pfvSyncOpen: openBlock names an open block ("" when none) and pending says if one has changes.
function pfvSyncOpen() {
  const ids = Object.keys(pfvState.edits);
  if (!ids.includes(pfvState.openBlock)) pfvState.openBlock = ids[0] || "";
  pfvState.pending = pfvAnyDirty();
}

// pfvDropEdits: every open block goes, and what was typed in it.
function pfvDropEdits() {
  for (const rec of Object.values(pfvState.edits)) pfvDisposeRec(rec);
  pfvState.edits = {};
  pfvState.openBlock = "";
  pfvState.pending = false;
}

function pfvClearSaved() {
  pfvState.saved = [];
  if (pfvSavedTimer) clearTimeout(pfvSavedTimer);
  pfvSavedTimer = null;
}

// pfvExpireSaved: "Saved" has been shown long enough. The note is hidden in place: the page is not redrawn
// for it, so a block that is open by now is left alone.
function pfvExpireSaved() {
  pfvState.saved = [];
  pfvSavedTimer = null;
  for (const n of pfvRefs.saved) n.hidden = true;
  pfvRefs.saved = [];
}

function pfvMarkSaved(ids) {
  pfvClearSaved();
  pfvState.saved = ids.slice();
  pfvSavedTimer = setTimeout(pfvExpireSaved, PFV_SAVED_MS);
  // unref is a Node timer's, not a browser's: a test run need not wait out the two seconds.
  if (pfvSavedTimer && typeof pfvSavedTimer.unref === "function") pfvSavedTimer.unref();
}

// pfvBlockModel: the read model's block, or null when this profile has none of that id.
function pfvBlockModel(id) {
  return pfvReadView(pfvInputs()).blocks.find((b) => b.id === id) || null;
}

function pfvRowForField(rows, field) {
  for (const [re, kind] of PFV_FIELD_ROW) {
    const m = re.exec(field);
    if (m) return rows.filter((r) => r.kind === kind)[Number(m[1])] || null;
  }
  return null;
}

const pfvJoin = (l) => (l.length < 2 ? l.join("") : l.slice(0, -1).join(", ") + " and " + l[l.length - 1]);

// --- editing: open, close, leave ---------------------------------------------------------------

function pfvFocusBlock(id) {
  const rec = pfvState.edits[id];
  if (rec && rec.first && typeof rec.first.focus === "function") rec.first.focus();
}

// pfvShowBlock: scroll to an open block and put focus in it ("Show" in the header and on a refusal).
function pfvShowBlock(id) {
  const rec = pfvState.edits[id];
  if (!rec || !rec.nodes) return;
  if (typeof rec.nodes.sec.scrollIntoView === "function") rec.nodes.sec.scrollIntoView({ block: "nearest" });
  pfvFocusBlock(id);
}

// pfvOpenBlock: turn one block into its form. Refused (in place, on the block that holds the
// changes) while another open block has changes; a block that was opened and not changed just
// closes in favour of the new one. A block whose editor is a later task, a profile you cannot
// edit, and a save in flight all refuse.
function pfvOpenBlock(id) {
  const ed = PFV_EDITORS[id];
  if (!ed || !pfvOnPage() || !pfvState.doc || !pfvState.view || pfvState.view.editable === false || pfvState.saving) return false;
  if (pfvState.edits[id]) { pfvFocusBlock(id); return true; }
  if (pfvGuard({ openBlock: pfvState.openBlock, dirty: pfvAnyDirty(), action: "edit", target: id }) !== "go") {
    pfvNudge();
    return false;
  }
  pfvDropEdits();
  pfvState.edits[id] = pfvNewEdit(id, false);
  pfvState.openBlock = id;
  pfvClearSaved();
  pfvSyncOpen();
  pfvRender();
  pfvFocusBlock(id);
  return true;
}

// pfvOpenAdd: a "+ Add ..." link of the read view: the block opens as a form with the picker open (the picker
// is pfvOpenPicker's, pfv-cloud.js). Nothing opens when the block refuses to (another block holds changes).
function pfvOpenAdd(id) {
  if (!pfvOpenBlock(id)) return false;
  pfvOpenPicker(id);
  return true;
}

// pfvOpenRemove: the Remove link of a read view row: the block opens as a form with that row removed. The
// removal is pending like any other, so nothing is saved until Save.
function pfvOpenRemove(id, row) {
  if (!pfvOpenBlock(id)) return false;
  const ed = PFV_EDITORS[id];
  const rec = pfvState.edits[id];
  if (ed && typeof ed.remove === "function" && rec) ed.remove(rec, row);
  return true;
}

// pfvOpenRepair: open a block a refused save named, on top of the one being edited (the
// one-open-block rule yields to error repair, spec 5.6). No question: it holds no changes.
function pfvOpenRepair(id) {
  if (pfvState.edits[id]) return pfvState.edits[id];
  const rec = pfvNewEdit(id, true);
  pfvState.edits[id] = rec;
  pfvSyncOpen();
  return rec;
}

// pfvNudge: an Edit was refused: say so on the block that holds the changes, and bring it into view.
function pfvNudge() {
  for (const rec of Object.values(pfvState.edits)) if (pfvEditDirty(rec)) rec.nudge = true;
  pfvPaint();
  pfvShowBlockQuietly(pfvFirstDirty());
}
function pfvShowBlockQuietly(id) {
  const rec = pfvState.edits[id];
  if (rec && rec.nodes && typeof rec.nodes.sec.scrollIntoView === "function") rec.nodes.sec.scrollIntoView({ block: "nearest" });
}

// pfvCloseBlock: close the open blocks, or just `id`, back to the read view. discard: the changes
// in them may go; without it a block with changes stays open (returns false). The changes are
// dropped with the record. A note on another block that points at one closed here goes with it.
function pfvCloseBlock(discard, id) {
  const ids = id ? [id] : Object.keys(pfvState.edits);
  if (!discard && ids.some((i) => pfvEditDirty(pfvState.edits[i]))) return false;
  for (const i of ids) {
    if (pfvState.edits[i]) pfvDisposeRec(pfvState.edits[i]);
    delete pfvState.edits[i];
  }
  for (const rec of Object.values(pfvState.edits)) if (rec.top && rec.top.kind === "repair" && ids.includes(rec.top.target)) rec.top = null;
  pfvSyncOpen();
  pfvRender();
  const back = pfvRefs.edit[ids[0]];
  if (back && typeof back.focus === "function") back.focus();
  return true;
}

// pfvLeaveGuard: may the page go away from what it is showing (another tab, here the Agent
// block's Manage link)? Nothing changed: yes. Changes: the question, Discard drops them and
// goes, Keep editing stays. action is pfvGuard's ("leave" for anything that is not opening
// another block). It is the one question every way out asks: switching profile, a Settings tab,
// Esc, the page's close buttons, New profile and Duplicate reach it through pfvLeaving below.
// Discard ends the edit through pfvEndEdits (pfvDropEdits), which releases the folder pickers.
async function pfvLeaveGuard(action) {
  const verdict = pfvGuard({ openBlock: pfvState.openBlock, dirty: pfvAnyDirty(), action: action || "leave", target: "" });
  if (verdict === "go") return true;
  if (verdict === "refuse") { pfvNudge(); return false; }
  const ok = await askConfirm({
    title: "Discard unsaved changes?",
    body: pfvState.page !== "" ? "The new profile has not been created. Leaving drops what you typed." : 'The changes to "' + pfvState.name + '" have not been saved. Leaving drops them.',
    confirmLabel: "Discard",
    cancelLabel: "Keep editing",
    danger: true,
    // Esc raises this question, so a second keypress on reflex must not be the one that drops the changes.
    focusCancel: true,
  });
  if (!ok) return false;
  pfvEndEdits();
  return true;
}

// pfvEndEdits: the page is being left: every open block goes with what was typed in it (pfvDropEdits releases a
// folder picker's listeners), and the page is drawn as the read view again so coming back shows it. A no-op
// with nothing open.
function pfvEndEdits() {
  if (Object.keys(pfvState.edits).length === 0) return;
  pfvDropEdits();
  // The New profile page ends with its record: coming back shows a profile (pfvShow), not what was typed.
  pfvState.page = "";
  pfvState.src = null;
  pfvRender();
}

// pfvKeepEdits: the page is going whatever the user would say (the window crossed to a phone), and what was typed goes into pfvKept,
// in memory and for this page load only, to come back when that profile opens on the desktop page again (pfvRestoreKept). Only an edit
// that holds a change is kept (an open block with none is dropped, as it always was), and not the New profile page, whose record is
// the page. Per open block it keeps what the controls hold, what the block was opened with (so a change is still told from none) and
// the rec.ui fields its editor names (PFV_EDITORS keep); copied, so nothing that lingers on the form being dropped can write into it.
// It keeps the stored profile the edit was made against, too: the edit's save carries that revision, so a profile that changed
// elsewhere meanwhile answers the save as stale (Reload) instead of being overwritten with what was typed on an older copy.
function pfvKeepEdits() {
  if (pfvState.page !== "" || !pfvAnyDirty()) return;
  const edits = Object.values(pfvState.edits).filter((rec) => rec.values !== null).map((rec) => {
    const ed = PFV_EDITORS[rec.id];
    const ui = {};
    for (const k of ed && ed.keep ? ed.keep : []) if (rec.ui[k] !== undefined) ui[k] = rec.ui[k];
    return pfeClone({ id: rec.id, repair: rec.repair, values: rec.values, init: rec.init, ui });
  });
  pfvKept = { name: pfvState.name, doc: pfvState.doc, openBlock: pfvState.openBlock, edits };
}

// pfvRestoreKept: the profile an edit was kept for is on screen again and was read (pfvShow). The page is the one the edit was made on:
// its blocks open as forms drawn from what was typed, the one that was open is open, and the header and the blocks say Unsaved changes
// as they would have. The profile it holds is the kept one, not the read that just landed: the save then carries the kept revision.
// It comes back once; from here the records are the page's own.
function pfvRestoreKept() {
  const k = pfvKept;
  pfvKept = null;
  for (const e of k.edits) pfvState.edits[e.id] = pfvNewEdit(e.id, e.repair, e);
  pfvState.openBlock = k.openBlock;
  pfvState.doc = k.doc;
  pfvState.errors = [];
  pfvClearSaved();
  pfvSyncOpen();
  pfvRender();
}

// pfvLeaving: the question before the desktop profile page goes away from the profile it shows. true, itself and
// not a promise, when nothing is asked: not the desktop page (a phone's modal and its old form never see a
// question) or nothing changed (open blocks without changes simply end). Otherwise pfvLeaveGuard's promise.
function pfvLeaving() {
  if (!pfvOnPage()) return true;
  // An open drawer goes first, through the same close as its own Esc (R63): what its flow saved is added to the block, and a flow
  // that is saving holds the page, as a promise of false (callers wait on a promise, never on a bare false).
  if (pfvDrawer && !pfvCloseDrawer()) return Promise.resolve(false);
  if (pfvAnyDirty()) return pfvLeaveGuard("leave");
  pfvEndEdits();
  return true;
}

// pfvHold: a way out that is not the page's own (the old "+ new profile" button's entry point in app.js, which the empty state's
// "Create box profile" presses):
// true when the page is asking whether to drop unsaved changes, and `again` runs if the answer is Discard. false
// means nothing is asked and the caller goes on.
function pfvHold(again) {
  const go = pfvLeaving();
  if (go === true) return false;
  go.then((ok) => { if (ok) again(); });
  return true;
}

// pfvSwitch: a profile in the nav. The same profile again changes nothing and asks nothing.
async function pfvSwitch(name) {
  if (name !== pfvState.name) {
    const go = pfvLeaving();
    if (go !== true && !(await go)) return false;
  }
  await pfvShow(name);
  return true;
}

// pfvTabLeave: another Settings tab (shell.js settingsLeave). Profiles itself is not leaving.
function pfvTabLeave(tab) {
  return tab === "manage" ? true : pfvLeaving();
}

// --- the Credentials drawer (spec 5.7) ---------------------------------------------------------------------------

// The "New AWS connection...", "New GitHub App..." and "New token..." items of the "+ Add" picker open credentials.js's add flows in
// a drawer over the page (index.html: #cred-scrim and #cred-drawer, whose head is the header band that stays put (the flow's crumb and
// intro), whose body is where a flow draws itself, whose foot is where its buttons and a refused Save go, and whose status line says
// why a close has to wait). The flows are handed `opts` = {head, host, foot, saveLabel, onCreated, onIdle, onClose} and know nothing of
// the page; what they made comes back through onCreated, and each name becomes a pending row of the block
// the picker was in, saved only with that block's Save. While the drawer is open it is the page's top layer: the page behind it is
// inert, Tab stays inside it, and Esc, the scrim and any way out of the page end the flow through its own Cancel (credFlowCancel), so a
// visit that saved 1 of 2 connections still reports that one.
// pfvDrawer: {kind, id, rec, parts, opts, inert, watch} while it is open, else null.
let pfvDrawer = null;

// The mockup's primary labels: the drawer also adds what it creates to the open block.
const PFV_DRAWER_SAVE = { aws: "Save all and add", github: "Save and add", token: "Save and add" };
const PFV_DRAWER_BUSY = "Wait for the save to finish, then close.";
const PFV_DRAWER_CONTROLS = 'a[href], summary, button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function pfvDrawerParts() {
  const parts = {};
  for (const [k, id] of [["drawer", "cred-drawer"], ["scrim", "cred-scrim"], ["head", "cred-drawer-head"], ["body", "cred-drawer-body"], ["foot", "cred-drawer-foot"], ["status", "cred-drawer-status"]]) {
    parts[k] = document.getElementById(id);
    if (!parts[k]) return null;
  }
  return parts;
}

// pfvDrawerStarter: the flow that makes a kind of credential, as a function of opts; null for a kind there is none of.
// A token starts where the Credentials tab's own Add token does (no group, no provider chosen for the user).
function pfvDrawerStarter(kind) {
  if (kind === "aws" && typeof awsOpenAdd === "function") return (opts) => awsOpenAdd(opts);
  if (kind === "github" && typeof credGitHubChoose === "function") return (opts) => credGitHubChoose(opts);
  if (kind === "token" && typeof credTokenAdd === "function") return (opts) => credTokenAdd(null, undefined, opts);
  return null;
}

// pfvOpenDrawer: the picker's "New ..." item. kind "aws" | "github" | "token"; blockId defaults to the open block, which has to be in
// edit mode (that is where the picker lives). false when it opens nothing: no such block, a drawer open already, a kind it has no flow
// for, or no drawer on the page.
function pfvOpenDrawer(kind, blockId) {
  const id = blockId || pfvState.openBlock;
  const rec = pfvState.edits[id];
  const parts = pfvDrawerParts();
  const start = pfvDrawerStarter(kind);
  if (pfvDrawer || !rec || !rec.fields || !parts || !start || !pfvOnPage()) return false;
  const d = { kind, id, rec, parts, opts: null, inert: [], watch: null };
  d.opts = {
    head: parts.head, host: parts.body, foot: parts.foot, saveLabel: PFV_DRAWER_SAVE[kind],
    onCreated: (names, made) => pfvDrawerCreated(d, names, made),
    // A save has answered and the flow stays open (a row was refused): the line that asked to wait is out of date.
    onIdle: () => { if (pfvDrawer === d) pfvDrawerSay(""); },
    onClose: () => pfvDrawerEnd(d, true),
  };
  pfvDrawer = d;
  pfvState.drawer = true;
  // Everything else on the page is inert: its controls cannot be reached by Tab or by a click while the drawer is over it.
  const modal = document.getElementById("settings-modal");
  d.inert = modal ? Array.from(modal.children).filter((n) => n !== parts.drawer && n !== parts.scrim) : [];
  for (const n of d.inert) n.setAttribute("inert", "");
  parts.scrim.onclick = () => { pfvCloseDrawer(); };
  parts.scrim.hidden = false;
  parts.drawer.hidden = false;
  try {
    start(d.opts);
  } catch (e) {
    pfvDrawerEnd(d, true);
    throw e;
  }
  pfvDrawerFocus(d);
  return true;
}

// pfvDrawerCreated: the flow made `names` (kind "aws" | "token" | "app", the flow's words). The page's credential lists are read
// first, so no new row reads "Not found in Credentials" (pfcAfterConvert does the same); then each name is a pending row of the block
// the drawer was opened from, if that block is still open as it was: a block that was closed or left meanwhile has nowhere to put
// them (they are in Credentials all the same). From the GitHub drawer an App or a token is a new owner signing in with it, with the
// owner and the repos left for the user to fill in.
async function pfvDrawerCreated(d, names, kind) {
  try {
    await refreshCredLists();
  } catch (_) {
    // The rows go in all the same; they read as missing until the lists are read, which is what the page knows.
  }
  const rec = pfvState.edits[d.id];
  if (rec !== d.rec || !rec.values) return;
  for (const name of names) {
    pfvAddRow(d.id, d.kind === "github" ? { kind: "github", login: (kind === "app" ? "app:" : "token:") + name } : { kind: kind === "aws" ? "aws" : "token", name });
  }
}

// pfvDrawerEnd: the drawer is gone, however it went (a flow's onClose, pfvCloseDrawer, pfvDropDrawer): its head, host and foot are emptied so
// nothing typed in it (a token's value, a passphrase) lingers in a hidden DOM, the page is live again, and focus goes back to the
// block's "+ Add" link (`back`). Once only.
function pfvDrawerEnd(d, back) {
  if (pfvDrawer !== d) return;
  pfvDrawer = null;
  pfvState.drawer = false;
  if (d.watch) d.watch.disconnect();
  const p = d.parts;
  p.head.replaceChildren();
  p.body.replaceChildren();
  p.foot.replaceChildren();
  pfvDrawerSay("");
  p.scrim.hidden = true;
  p.drawer.hidden = true;
  for (const n of d.inert) n.removeAttribute("inert");
  const add = pfvState.edits[d.id] === d.rec && d.rec.fields ? d.rec.fields.add : null;
  if (back && add && typeof add.focus === "function") add.focus();
}

// pfvDrawerSay: the line between the body and the foot ("" takes it away).
function pfvDrawerSay(text) {
  const el = document.getElementById("cred-drawer-status");
  if (!el) return;
  el.textContent = text;
  el.hidden = text === "";
}

// pfvCloseDrawer: Esc, the scrim and the way out of the page. The flow is ended through its own Cancel, which reports what it saved
// (onCreated) and then closes the drawer (onClose); a flow that is not there to be asked (it was still reading the server) is dropped, so
// it draws nothing into the emptied host. While a flow saves nothing closes: it would lose the name being saved, so the drawer says why.
// true when it is closed (or was not open), false when it stays.
function pfvCloseDrawer() {
  const d = pfvDrawer;
  if (!d) {
    pfvState.drawer = false;
    return true;
  }
  if (credFlowBusy()) {
    pfvDrawerSay(PFV_DRAWER_BUSY);
    return false;
  }
  credFlowCancel(d.opts);
  if (pfvDrawer === d) {
    credFlowDrop();
    pfvDrawerEnd(d, true);
  }
  return true;
}

// pfvDropDrawer: the page is going whatever the user would say (the window crossed to a phone), so the drawer goes with it: nothing is
// asked, nothing waits, and nothing is reported. What its flow had already created is a row of the block by now and is kept with the
// edit (pfvKeepEdits); what was half filled in the drawer itself is lost.
function pfvDropDrawer() {
  const d = pfvDrawer;
  if (!d) {
    pfvState.drawer = false;
    return;
  }
  credFlowDrop();
  pfvDrawerEnd(d, false);
}

// pfvDrawerControls: what Tab reaches in the drawer, in order.
function pfvDrawerControls(root) {
  return Array.from(root.querySelectorAll(PFV_DRAWER_CONTROLS)).filter((n) => (typeof n.getClientRects === "function" ? n.getClientRects().length > 0 : n.hidden !== true));
}

// pfvDrawerFirst: the first control of what the flow drew in the body, not the crumb link in the header (it leaves).
function pfvDrawerFirst(parts) {
  return pfvDrawerControls(parts.drawer).find((n) => parts.body.contains(n)) || null;
}

// pfvDrawerFocus: focus goes into the drawer as it opens: the first control of the flow. A flow that has drawn nothing yet (it is
// reading the server, or the host's profiles) leaves focus on the drawer itself, so Esc and Tab are already its own, and it moves to
// the first control when one is drawn, if the user has not gone anywhere else meanwhile.
function pfvDrawerFocus(d) {
  const first = pfvDrawerFirst(d.parts);
  if (first) {
    first.focus();
    return;
  }
  d.parts.drawer.focus();
  if (typeof MutationObserver !== "function") return;
  d.watch = new MutationObserver(() => {
    if (pfvDrawer !== d || document.activeElement !== d.parts.drawer) {
      d.watch.disconnect();
      return;
    }
    const f = pfvDrawerFirst(d.parts);
    if (!f) return;
    d.watch.disconnect();
    f.focus();
  });
  d.watch.observe(d.parts.body, { childList: true, subtree: true });
}

// pfvDrawerTab: Tab (and Shift+Tab) stays inside the open drawer: past the last control is the first, before the first is the last, and
// a Tab with focus outside it (it fell to the body when the flow drew its next view) goes in. A question on screen keeps its own Tab.
function pfvDrawerTab(ev) {
  const d = pfvDrawer;
  if (ev.key !== "Tab" || !d) return;
  const ask = document.getElementById("ask-modal");
  if (ask && ask.open) return;
  const root = d.parts.drawer;
  const list = pfvDrawerControls(root);
  if (list.length === 0) {
    ev.preventDefault();
    root.focus();
    return;
  }
  const first = list[0], last = list[list.length - 1], at = document.activeElement;
  if (!at || !root.contains(at)) {
    ev.preventDefault();
    (ev.shiftKey ? last : first).focus();
  } else if (ev.shiftKey ? at === first || at === root : at === last) {
    ev.preventDefault();
    (ev.shiftKey ? last : first).focus();
  }
}

// --- Esc ----------------------------------------------------------------------------------------------

// pfvFolderListOpen: a Workspace folder dropdown is open (the picker is on the record while its form stands).
function pfvFolderListOpen() {
  return Object.values(pfvState.edits).some((r) => r.ui && r.ui.folderPicker && typeof r.ui.folderPicker.isOpen === "function" && r.ui.folderPicker.isOpen());
}

// pfvEscTarget: the layer the next Esc belongs to, innermost first: a question on screen (askConfirm and askDialog
// are one modal), the credentials drawer, an open picker (the "+ Add" choice or a Workspace folder dropdown),
// else the page itself. Read from state, so it is the same answer whichever control has focus.
function pfvEscTarget() {
  const ask = document.getElementById("ask-modal");
  if (ask && ask.open) return "confirm";
  if (pfvState.drawer) return "drawer";
  if (pfvState.picker || pfvFolderListOpen()) return "picker";
  return "page";
}

// pfvPageLeave: pagehost.js's pageLeaveHook while the desktop Settings page is the open page; for any other page,
// a phone's modal included, it answers true and does nothing. why "force" is a close that cannot be refused (the
// window crossed to a phone): the edit ends through pfvDropEdits, so no folder picker keeps a listener on a page that
// is gone, and what was typed is kept as data (pfvKeepEdits) for the next time that profile opens on the desktop
// page. why "esc" is the Esc key: a layer inside the page
// takes it (an open picker closes, the drawer closes itself, a question answers itself) and the page stays;
// only an Esc that no layer wants is a way out. Every other close asks the same question as any way out.
function pfvPageLeave(why) {
  if (!pfvOnPage()) return true;
  // The window crossed to a phone: the page goes whatever the user would say, so nothing is asked. The edit goes, and what was typed
  // in it is kept for when this profile is on the desktop page again.
  if (why === "force") {
    pfvDropDrawer();
    pfvKeepEdits();
    pfvEndEdits();
    return true;
  }
  if (why === "esc") {
    const at = pfvEscTarget();
    if (at !== "page") {
      if (at === "drawer") pfvCloseDrawer();
      if (at === "picker") {
        pfvClosePicker();
        for (const r of Object.values(pfvState.edits)) if (r.ui && r.ui.folderPicker && typeof r.ui.folderPicker.close === "function") r.ui.folderPicker.close();
      }
      return false;
    }
  }
  return pfvLeaving();
}

// --- editing: the block and its footer --------------------------------------------------------------

// pfvDrawEditBlock: the open block: its label, the form (or, for a block opened only to show a
// refusal, the read rows), and the footer. The form is drawn from rec.values, so redrawing
// it loses nothing. Created once per draw; the parts that change as the user types are
// filled by pfvFillForm and pfvPaintEdit.
function pfvDrawEditBlock(b, rec, known) {
  // Advanced keeps its fold label while it is edited (the mockup's: the chevron turned down and what is set), the form under it.
  const adv = b.id === "advanced";
  const sec = pfvMk("section", "pfv-block pfv-editing" + (adv ? " pfv-open" : ""));
  sec.dataset.block = b.id;
  const body = pfvMk("div", "pfv-body pfv-edit-body" + (adv ? " pfv-adv-body" : ""));
  const top = pfvMk("div", "pfv-top");
  const form = pfvMk("div", "pfv-form" + (PFV_EDITORS[b.id] && PFV_EDITORS[b.id].fields ? " pfv-fields" : ""));
  const foot = pfvMk("div", "pfv-foot");
  const status = pfvMk("div", "pfv-status");
  status.setAttribute("role", "status");
  const btns = pfvMk("div", "pfv-fbtns");
  // One button for both: with no change it reads Cancel, with changes Discard; either way what it drops is nothing the user has not seen.
  const discard = pfvBtn("Cancel", "ghost", () => pfvCloseBlock(true, b.id));
  const save = pfvBtn("Save", "primary", () => pfvSaveBlock(b.id));
  btns.append(discard, save);
  foot.append(status, btns);
  body.append(top, form, foot);
  let label = pfvMk("h4", "pfv-label", b.label);
  if (adv) {
    label = pfvMk("h4", "pfv-label pfv-adv-label");
    label.append(...pfvAdvLabel(b));
  }
  sec.append(label, body);
  rec.block = b;
  rec.known = known;
  rec.nodes = { sec, top, form, status, discard, save };
  rec.sigs = {};
  pfvFillForm(rec);
  pfvPaintEdit(rec);
  return sec;
}

// pfvFillForm: draw the form from the record's values.
function pfvFillForm(rec) {
  if (!rec.nodes) return;
  const ed = PFV_EDITORS[rec.id];
  pfvDisposeRec(rec);
  rec.fields = {};
  rec.first = null;
  rec.invalid = [];
  rec.consumed = new Set();
  const kids = ed ? ed.draw(rec, pfvEditCtx()) : [pfvDrawBody(rec.block, rec.known, rec)];
  rec.nodes.form.replaceChildren(...kids);
}

// pfvRedrawForm: draw it again after a change that alters what the form shows, keeping focus on the same control.
function pfvRedrawForm(rec) {
  if (!rec.nodes) return;
  const at = typeof document !== "undefined" ? document.activeElement : null;
  // The control itself, or a button of a segmented control (its container is the field).
  const key = Object.keys(rec.fields).find((k) => rec.fields[k] === at || (rec.fields[k].dataset.value !== undefined && rec.fields[k].contains(at)));
  pfvFillForm(rec);
  const el = key ? rec.fields[key] : null;
  if (!el) return;
  const target = pfvFocusTarget(el);
  if (target && typeof target.focus === "function") target.focus();
}

// pfvChanged: a control changed a value. Its refusal is stale now, a nudge was about this very
// change, and the form (and every footer, and the header note) re-reads the values.
function pfvChanged(rec, key) {
  if (key) rec.ferrs = rec.ferrs.filter((e) => !pfvFieldIs(e.field, key));
  rec.nudge = false;
  pfvRedrawForm(rec);
  pfvPaint();
}

// pfvPaint: bring the header note and every footer in line with the records, without touching a form.
function pfvPaint() {
  pfvState.pending = pfvAnyDirty();
  if (pfvRefs.unsaved) pfvRefs.unsaved.hidden = !pfvState.pending;
  for (const rec of Object.values(pfvState.edits)) pfvPaintEdit(rec);
}

function pfvBoxNote(rec, c) {
  const ed = PFV_EDITORS[rec.id];
  if (ed) return !!(ed.note && ed.note(rec, c));
  // A block that is only showing a refusal: its bindings and paths belong to a box.
  return c.tier === "box" && (rec.id === "cloud" || rec.id === "reachable");
}

function pfvPaintEdit(rec) {
  const n = rec.nodes;
  if (!n) return;
  const ed = PFV_EDITORS[rec.id];
  const c = pfvEditCtx();
  const dirty = pfvEditDirty(rec);
  n.sec.classList.toggle("pfv-nudge", rec.nudge);
  // Both are live regions: they are rebuilt only when what they say changed, or every keystroke would be read out again.
  pfvSetKids(rec, "top", pfvTopKids(rec));
  pfvSetKids(rec, "status", pfvStatusKids(rec, dirty, c));
  n.discard.textContent = dirty ? "Discard" : "Cancel";
  n.save.hidden = !!(ed && ed.hideSave && ed.hideSave(rec, c));
  // Not while a save is running: withPending owns the label then.
  if (n.save.getAttribute("aria-busy") !== "true") {
    const label = (ed && ed.saveLabel && ed.saveLabel(rec, c)) || "Save";
    n.save.textContent = label;
    n.save.classList.toggle("pfv-btn-host", label !== "Save");
  }
}

function pfvSetKids(rec, part, kids) {
  const sig = kids.map((k) => k.textContent).join("\n");
  if (rec.sigs[part] === sig) return;
  rec.sigs[part] = sig;
  rec.nodes[part].replaceChildren(...kids);
}

function pfvTopKids(rec) {
  const out = [];
  const topErr = (text) => {
    const p = pfvMk("p", "pfv-top-err");
    p.setAttribute("role", "alert");
    p.append(text);
    return p;
  };
  const t = rec.top;
  if (t) {
    const p = topErr(t.text);
    const link = t.kind === "stale" ? ["Reload", pfvReload] : t.kind === "gone" ? ["Show another profile", pfvLeaveGone] : t.kind === "repair" ? ["Show", () => pfvShowBlock(t.target)] : null;
    if (link) {
      const b = pfvDrawLink(link[0], "");
      b.onclick = link[1];
      p.append(" ", b);
    }
    out.push(p);
  }
  // A refusal no input of the form drew is shown here, at the top of the block.
  for (const e of rec.ferrs) if (!rec.consumed.has(e.field)) out.push(topErr(e.text));
  return out;
}

function pfvStatusKids(rec, dirty, c) {
  const out = [];
  if (dirty) {
    const p = pfvMk("p", "pfv-st-dirty");
    p.append(pfvMk("span", "pfv-udot"), "Unsaved changes");
    out.push(p);
  }
  const n = rec.ferrs.length;
  if (n) out.push(pfvMk("p", "pfv-st-invalid", n === 1 ? "One field needs a fix before this can save." : n + " fields need a fix before this can save."));
  if (rec.nudge) out.push(pfvMk("p", "pfv-st-nudge", PFV_NUDGE));
  // Both footers save the same merged profile (spec 5.6): say what else this Save takes along.
  const others = Object.values(pfvState.edits).filter((r) => r !== rec && pfvEditDirty(r)).map((r) => (r.block ? r.block.label : r.id));
  if (others.length) out.push(pfvMk("p", "pfv-fnote", "Saving also saves your " + pfvJoin(others) + (others.length > 1 ? " changes." : " change.")));
  if (pfvBoxNote(rec, c)) out.push(pfvMk("p", "pfv-fnote", PFV_NEW_BOX));
  return out;
}

// --- editing: the form controls ----------------------------------------------------------------------

// pfvReg: remember a control under its field key and mark it (data-pfv-field) for the keyboard and the tests.
function pfvReg(rec, key, el) {
  el.dataset.pfvField = key;
  rec.fields[key] = el;
  if (!rec.first) rec.first = el;
  return el;
}

// pfvBad: `control` is in error and `words` (the node that says it) is what a screen reader reads for it: aria-invalid and
// aria-describedby, and the controls of a form that are marked, in the order they were drawn, are where focus goes after a
// refused Save (pfvFocusInvalid). plain: describe only (a Remove button is no input, so it is not "invalid").
function pfvBad(rec, control, words, plain) {
  if (!words.id) words.id = "pfv-e" + ++pfvUid;
  control.setAttribute("aria-describedby", words.id);
  if (!plain) control.setAttribute("aria-invalid", "true");
  if (!rec.invalid.includes(control)) rec.invalid.push(control);
}

// pfvStandIn: the field is a group of controls (the permissions, the variables), and `control`, one of them, stands for it: it is
// marked and focused in the group's place (a div takes no focus, and a screen reader is told where the focus is, not about the div).
function pfvStandIn(rec, group, control, words, plain) {
  pfvOk(group);
  pfvBad(rec, control, words, plain);
  const at = rec.invalid.indexOf(group);
  if (at < 0) return;
  rec.invalid.splice(rec.invalid.indexOf(control), 1);
  rec.invalid[at] = control;
}

// pfvOk: the error of `control` is gone: it is not invalid any more.
function pfvOk(control) {
  control.removeAttribute("aria-invalid");
  control.removeAttribute("aria-describedby");
}

// pfvFirstFocusable: `el` if it takes focus, else the first control inside it that does (null when none).
const PFV_FOCUSABLE = ["input", "select", "textarea", "button"];
function pfvFirstFocusable(el) {
  if (PFV_FOCUSABLE.includes(String(el.tagName || el.tag || "").toLowerCase()) && !el.disabled) return el;
  for (const c of Array.from(el.children || [])) {
    const f = pfvFirstFocusable(c);
    if (f) return f;
  }
  return null;
}

// pfvFocusTarget: the node of a control that takes focus. A segmented control is not focusable itself: its lit button is.
function pfvFocusTarget(el) {
  if (el.dataset && el.dataset.value !== undefined) return Array.from(el.children).find((b) => b.dataset.segvalue === el.dataset.value) || el.children[0];
  return pfvFirstFocusable(el);
}

// pfvFocusInvalid: after a refused Save, focus goes to the first marked control of the form that takes it. True only when focus is
// there afterwards: a control that did not take it (hidden, gone) is not a success, and the next one is tried. The folder's dropdown
// (the Workspace editor's quiet) is not asked for by a focus that is the page's, not the user's.
function pfvFocusInvalid(rec) {
  const ui = rec && rec.ui ? rec.ui : {};
  for (const c of rec && rec.invalid ? rec.invalid : []) {
    const el = pfvFocusTarget(c);
    if (!el || typeof el.focus !== "function") continue;
    const was = ui.quiet;
    ui.quiet = true;
    try { el.focus(); } finally { ui.quiet = was; }
    if (document.activeElement === el) return true;
  }
  return false;
}

// pfvFieldIs: a refused field path is the control `key`'s: the same path, or an entry of it ("clone_share[1]" is
// "clone_share"'s).
const pfvFieldIs = (field, key) => field === key || field.startsWith(key + "[");

// pfvFerr: the refusal for a field, in words, and note that the form drew it.
function pfvFerr(rec, key) {
  const e = rec.ferrs.find((x) => pfvFieldIs(x.field, key));
  if (!e) return "";
  rec.consumed.add(e.field);
  return e.text;
}

// pfvFieldBox: a label, the control, and under it the hint, or in its place the refusal for errKey
// (the control then wears the error border). tag "fieldset" for a group of controls. w.clear() takes the
// refusal away in place (the hint comes back) for a control that changes without the form being drawn
// again, as a folder picker's must not be: its input and its dropdown are the nodes that stay.
function pfvFieldBox(rec, errKey, label, control, hint, tag) {
  const w = pfvMk(tag || "div", "pfv-field");
  const lab = pfvMk(tag === "fieldset" ? "legend" : "label", "pfv-flabel", label);
  if (tag !== "fieldset" && control.id) lab.setAttribute("for", control.id);
  w.append(lab, control);
  const hints = hint && hint.length ? hint : [];
  let below = null;
  const paint = (err) => {
    control.classList.toggle("pfv-bad", err !== "");
    if (err === "" && hints.length === 0) {
      pfvOk(control);
      if (below) below.hidden = true;
      return;
    }
    if (!below) {
      below = pfvMk("p", "");
      w.append(below);
    }
    below.hidden = false;
    below.className = err ? "pfv-ferr" : "pfv-hint";
    below.replaceChildren(...(err ? [err] : hints));
    if (err) pfvBad(rec, control, below);
    else pfvOk(control);
  };
  paint(pfvFerr(rec, errKey));
  w.clear = () => {
    rec.ferrs = rec.ferrs.filter((e) => !pfvFieldIs(e.field, errKey));
    rec.nudge = false;
    paint("");
  };
  // The words, while there are any: what a refusal about a list of controls points them at (the variables).
  w.words = () => (below && !below.hidden && below.className === "pfv-ferr" ? below : null);
  return w;
}

function pfvSelect(rec, key, choices, value, mono, onPick) {
  const s = pfvMk("select", "pfv-ctl pfv-select" + (mono ? " pfv-ctl-mono" : ""));
  s.id = "pfv-f" + ++pfvUid;
  for (const c of choices) {
    const o = pfvMk("option", "", c.label);
    o.value = c.value;
    s.append(o);
  }
  s.value = value;
  s.onchange = () => onPick(s.value);
  return pfvReg(rec, key, s);
}

function pfvTextCtl(rec, key, value, mono, onInput, onCommit) {
  const i = pfvMk("input", "pfv-ctl" + (mono ? " pfv-ctl-mono" : ""));
  i.type = "text";
  i.id = "pfv-f" + ++pfvUid;
  i.value = value;
  i.setAttribute("autocomplete", "off");
  i.setAttribute("spellcheck", "false");
  i.oninput = () => onInput(i.value);
  i.onchange = () => onCommit(i.value);
  return pfvReg(rec, key, i);
}

// pfvSegCtl: the app's own segmented control (buildSeg), its value on the container (segValue).
function pfvSegCtl(rec, key, choices, value, onPick) {
  const seg = pfvMk("div", "seg pfv-seg");
  seg.setAttribute("role", "group");
  buildSeg(seg, choices.map((c) => ({ value: c, label: c })), value, onPick);
  return pfvReg(rec, key, seg);
}

// The choices of an enum, with the stored value kept selectable when the list no longer has it,
// so a save can never quietly drop it.
const pfvWithValue = (choices, value) => (value !== "" && !choices.includes(value) ? choices.concat([value]) : choices);

// --- Runs in -----------------------------------------------------------------------------------------

function pfvRunsValues(c) {
  return { tier: c.tier };
}

function pfvRunsMerge(rec, c) {
  return { tier: rec.values.tier, boxAvailable: c.boxAvailable };
}

// The consequence line is for a box that stays a box; choosing host asks the host question instead.
function pfvRunsNote(rec, c) {
  return c.tier === "box" && rec.values.tier === "box";
}

// A private copy runs only in a box, and the switch never guesses which folder an uncontained agent writes to.
function pfvRunsHideSave(rec, c) {
  return rec.values.tier === "host" && pfvHostRefusal(c.doc) !== "";
}

// Choosing host on a profile that is not host: Save is the way into it, and it asks (pfvConfirmHost).
const pfvToHostPending = (rec, c) => rec.values.tier === "host" && c.tier !== "host";
function pfvRunsSaveLabel(rec, c) {
  return pfvToHostPending(rec, c) ? "Save as host…" : "";
}

// pfvRemovalCount: one kind of what host removes, as the Runs in consequence line counts it: "2 AWS connections".
const PFV_REMOVAL_COUNT = new Map([
  ["AWS connections", ["AWS connection", "AWS connections"]], ["Tokens", ["token", "tokens"]],
  ["Readable folders", ["readable folder", "readable folders"]], ["Domains", ["domain", "domains"]],
]);
function pfvRemovalCount(r) {
  const n = r.names.length;
  const word = PFV_REMOVAL_COUNT.get(r.kind);
  if (word) return n + " " + word[n === 1 ? 0 : 1];
  if (r.kind === "Old-style AWS") return "the old-style AWS setting";
  return r.kind === "Network settings" ? "network settings" : r.kind;
}

// The consequence under the cards (the mockup's when-host and when-box lines).
const PFV_HOST_RISK = "its sessions run with your whole home, your SSH keys and the Docker socket";
function pfvHostConsequence(doc) {
  const rm = pfvHostRemovals(doc);
  const name = typeof doc.name === "string" && doc.name !== "" ? doc.name : pfvState.name;
  if (rm.length === 0) return "Its sessions run with your whole home, your SSH keys and the Docker socket.";
  return "A host profile cannot hold box-only access. Saving removes " + pfvJoin(rm.map(pfvRemovalCount)) + " from " + name + ", and " + PFV_HOST_RISK + ".";
}
function pfvBoxConsequence(doc) {
  const p = pfvMk("p", "pfv-consq");
  const cwd = typeof doc.default_cwd === "string" && doc.default_cwd !== "" ? doc.default_cwd : "";
  p.append("In a box, sessions can write only to ");
  if (cwd) p.append(pfvMk("span", "pfv-mono", shortPath(cwd)));
  else p.append("their workspace");
  p.append(". After saving you can add cloud access, GitHub, tokens, folders and domains here.");
  return p;
}

// --- the host confirm ---

// What the confirm calls each kind it lists (profileedit.js pfvHostRemovals gives the plural kind words): the singular
// when the profile has one, as the mockup's dialog does ("Readable folder"). Folders and domains are machine text.
const PFV_REMOVAL_ONE = new Map([["AWS connections", "AWS connection"], ["Tokens", "Token"], ["Readable folders", "Readable folder"], ["Domains", "Domain"]]);
const PFV_REMOVAL_MONO = ["Readable folders", "Domains"];
// A setting that has no name to list.
const PFV_REMOVAL_NONAME = new Map([["Network settings", "CuliBox's proxy and its allowlist"]]);

// pfvHostConfirmNodes: what sits under the body of the "Save as host" question: why the list, the list (one row per
// kind: a name and where it is, never a value), and the question itself last. Drawn in #ask-modal, so the classes
// are the dialog's (ask-note, ask-list, ask-q), not the page's.
function pfvHostConfirmNodes(name, removals, question) {
  const out = [pfvMk("p", "ask-note", "A host profile cannot hold these, so saving also removes them from " + name + ":")];
  const dl = pfvMk("dl", "ask-list");
  for (const r of removals) {
    const dd = pfvMk("dd");
    if (r.names.length === 0) dd.append(PFV_REMOVAL_NONAME.get(r.kind) || "Set");
    r.names.forEach((n, i) => {
      if (i > 0) dd.append(", ");
      if (PFV_REMOVAL_MONO.includes(r.kind)) dd.append(pfvMk("span", "ask-mono", n));
      else dd.append(n);
    });
    dl.append(pfvMk("dt", "", r.names.length === 1 && PFV_REMOVAL_ONE.has(r.kind) ? PFV_REMOVAL_ONE.get(r.kind) : r.kind), dd);
  }
  out.push(dl);
  if (question) out.push(pfvMk("p", "ask-q", question));
  return out;
}

// pfvConfirmHost: the C3 question before a profile is saved as host: today's title, body and "Save as host" button
// (containmentConfirm), and under the body what the switch removes (pfvHostRemovals of `doc`, the profile as it
// would be saved). The body's closing question is moved below the list, where the mockup has it. Resolves true only
// on the explicit "Save as host". Cancel is focused: Enter must not be the way into containment loss.
async function pfvConfirmHost(doc) {
  const name = doc && typeof doc.name === "string" && doc.name !== "" ? doc.name : pfvState.name;
  const ask = containmentConfirm("host", name);
  const removals = pfvHostRemovals(doc);
  if (removals.length === 0) return askConfirm(Object.assign({}, ask, { focusCancel: true }));
  const q = /\s*(Save it that way\?)$/.exec(ask.body);
  return askConfirm(Object.assign({}, ask, {
    body: q ? ask.body.slice(0, q.index) : ask.body,
    extra: pfvHostConfirmNodes(name, removals, q ? q[1] : ""),
    focusCancel: true,
  }));
}

// pfvOpenWorkspace: the link under the private-copy refusal. The host choice cannot be saved, so it goes (the
// Runs in block closes with its change) and Workspace opens, where the user picks the folder explicitly.
// Another block's changes are not this link's to drop: they refuse the open, in place, as an Edit would.
function pfvOpenWorkspace() {
  if (Object.values(pfvState.edits).some((r) => r.id !== "runs" && pfvEditDirty(r))) {
    pfvNudge();
    return false;
  }
  pfvCloseBlock(true, "runs");
  return pfvOpenBlock("workspace");
}

// pfvSwitchToBox: the host read view's "Switch to box": Runs in opens with Box chosen. Nothing is restored and
// nothing is saved until Save; refused in place while another block holds changes, as an Edit would be.
function pfvSwitchToBox() {
  if (!pfvOpenBlock("runs")) return false;
  const rec = pfvState.edits.runs;
  if (!rec || !rec.values) return false;
  rec.values.tier = "box";
  pfvChanged(rec, "runtime");
  const r = rec.fields["tier-box"];
  if (r && !r.disabled && typeof r.focus === "function") r.focus();
  return true;
}

function pfvCard(rec, key, o) {
  const lab = pfvMk("label", "pfv-card" + (o.checked ? " pfv-on" : "") + (o.disabled ? " pfv-off" : ""));
  const r = pfvMk("input");
  r.type = "radio";
  r.name = o.group || "pfv_tier"; // the radio group (an underscore: the class scan reads pfv- words as classes)
  r.checked = o.checked;
  r.disabled = o.disabled;
  r.onchange = () => { if (r.checked) o.pick(); };
  pfvReg(rec, key, r);
  const t = pfvMk("span", "pfv-card-t", o.title);
  if (o.chip) t.append(pfvMk("span", "pfv-chip-word", o.chip));
  lab.append(r, t, ...o.lines);
  return lab;
}

// pfvTierCards: the two cards of "Where it runs", the one place their words are: In a box (Recommended, with "Docker found" or why
// there are no boxes) and On this machine. Runs in (above) and the New profile and Duplicate page (pfv-new.js) draw them. o:
// {tier, pick(tier), group, host, boxOff}: the lit tier, what choosing a card does, the radio group (a name apart from other
// forms'), false for no host card, and whether the Box card is off (default: when there are no boxes). The cards are registered
// on rec as tier-box and tier-host.
function pfvTierCards(rec, c, o) {
  const boxLine = pfvMk("span", "pfv-card-d");
  if (c.boxAvailable) boxLine.append(pfvMk("span", "pfv-okdot"), "Docker found");
  else boxLine.append(pfvUnavailable(c.reason).line);
  const cards = [pfvCard(rec, "tier-box", {
    group: o.group, checked: o.tier === "box", disabled: o.boxOff === undefined ? !c.boxAvailable : o.boxOff, title: "In a box", chip: "Recommended", pick: () => o.pick("box"),
    lines: [pfvMk("span", "pfv-card-d", "A container. It writes only to its workspace, and CuliBox scopes AWS, GitHub and tokens."), boxLine],
  })];
  if (o.host !== false) {
    cards.push(pfvCard(rec, "tier-host", {
      group: o.group, checked: o.tier === "host", disabled: false, title: "On this machine", chip: "", pick: () => o.pick("host"),
      lines: [pfvMk("span", "pfv-card-d", "Runs as you, with your files, your network and your credentials.")],
    }));
  }
  return cards;
}

function pfvRunsForm(rec, c) {
  const v = rec.values;
  const cards = pfvTierCards(rec, c, { tier: v.tier, pick: (tier) => { v.tier = tier; pfvChanged(rec, "runtime"); } });
  // The control to focus on open: the one that is lit and can be used.
  rec.first = [rec.fields["tier-box"], rec.fields["tier-host"]].find((r) => r.checked && !r.disabled) || rec.fields["tier-host"];
  const fs = pfvMk("fieldset", "pfv-field pfv-choice");
  fs.append(pfvMk("legend", "pfv-flabel", "Where it runs"), ...cards);
  const out = [fs];
  const err = pfvFerr(rec, "runtime");
  if (err) {
    const words = pfvMk("p", "pfv-ferr", err);
    pfvBad(rec, rec.first, words);
    out.push(words);
  }
  const refusal = v.tier === "host" ? pfvHostRefusal(c.doc) : "";
  // A box profile choosing host: what that costs. A private copy gets the refusal below instead.
  if (pfvToHostPending(rec, c) && !refusal) out.push(pfvMk("p", "pfv-consq", pfvHostConsequence(c.doc)));
  // A host profile choosing box: where it will write, and that the cloud blocks come back empty.
  if (v.tier === "box" && c.tier === "host") out.push(pfvBoxConsequence(c.doc));
  if (refusal) {
    const p = pfvMk("p", "pfv-refuse");
    p.setAttribute("role", "alert");
    const link = pfvDrawLink("Open Workspace", "");
    link.onclick = pfvOpenWorkspace;
    p.append(refusal, " ", link);
    out.push(p);
  }
  return out;
}

// --- Agent -------------------------------------------------------------------------------------------------

const PFV_BINS = [["claude", "Claude Code (claude)"], ["bash", "Bash shell (bash)"]];
const PFV_EFFORT_UNSET = "Not set: Claude Code decides.";
const PFV_OTHER = "__other__";

function pfvStoredOptions(doc) {
  return doc.options && typeof doc.options === "object" && !Array.isArray(doc.options) ? doc.options : {};
}

// pfvAgentDefs: the options the Agent block offers for this command: model and effort, as the
// daemon defines them (GET /api/options), for the commands they apply to.
function pfvAgentDefs(c, bin) {
  const ok = (d) => d.type === "enum" && Array.isArray(d.choices) && d.choices.length > 0 && optApplies(d, bin);
  // Model, then effort: the order of the form (the daemon lists its options in its own).
  return PFE_AGENT_OPTIONS.map((k) => c.defs.find((d) => d.key === k)).filter((d) => d && ok(d));
}

// The values keep every agent option the daemon defines, applicable or not, so changing the
// command and back loses nothing; what the save sends is decided by pfvAgentMerge.
function pfvAgentValues(c) {
  const stored = pfvStoredOptions(c.doc);
  const options = {};
  for (const d of c.defs) if (PFE_AGENT_OPTIONS.includes(d.key) && d.type === "enum") options[d.key] = typeof stored[d.key] === "string" ? stored[d.key] : "";
  return { bin: typeof c.doc.bin === "string" ? c.doc.bin : "", account: typeof c.doc.account === "string" ? c.doc.account : "", options };
}

// An option the command does not take is removed: the server refuses a Claude-only option on another command. Model and
// effort are decided here by pfvAgentDefs; when the command changes, every other stored option whose definition does not
// apply to the new one goes too (the Advanced block's, which this block does not show), by the rule the old form draws its
// options by (optApplies). An option no definition names is not ours to judge and is sent back as it was.
function pfvAgentMerge(rec, c) {
  const v = rec.values;
  const takes = new Set(pfvAgentDefs(c, v.bin).map((d) => d.key));
  const options = {};
  for (const k of Object.keys(v.options)) options[k] = takes.has(k) ? v.options[k] : "";
  const bin = v.bin.trim();
  const changed = bin !== "" && bin !== (typeof c.doc.bin === "string" ? c.doc.bin.trim() : "");
  const dropOptions = changed ? c.defs.filter((d) => d && typeof d.key === "string" && !optApplies(d, bin)).map((d) => d.key) : [];
  return { bin: v.bin, account: v.account, options, dropOptions };
}

function pfvAccountChoices(account) {
  const list = typeof accountsCache !== "undefined" && Array.isArray(accountsCache) ? accountsCache : [];
  return acctChoices(list, account).map((n) => ({ value: n === ACCT_DEFAULT ? "" : n, label: n === ACCT_DEFAULT ? "default (~/.claude)" : n }));
}

function pfvAgentForm(rec, c) {
  const v = rec.values;
  const ui = rec.ui;
  if (ui.binOther === undefined) ui.binOther = !PFV_BINS.some(([b]) => b === v.bin);
  if (ui.otherText === undefined) ui.otherText = ui.binOther ? v.bin : "";
  if (!ui.fetched && typeof fetchAccounts === "function") {
    // The saved logins may not have been fetched yet: draw from the cache now, and again if they land different.
    ui.fetched = true;
    const had = pfvAccountChoices(v.account).map((x) => x.value).join("\n");
    Promise.resolve(fetchAccounts()).then(() => {
      if (pfvState.edits[rec.id] === rec && pfvAccountChoices(rec.values.account).map((x) => x.value).join("\n") !== had) pfvRedrawForm(rec);
    }, () => {});
  }
  const out = [];
  const choices = PFV_BINS.map(([value, label]) => ({ value, label })).concat([{ value: PFV_OTHER, label: "Other command…" }]);
  const sel = pfvSelect(rec, "bin", choices, ui.binOther ? PFV_OTHER : v.bin, false, (val) => {
    if (val === PFV_OTHER) { ui.binOther = true; v.bin = ui.otherText; } else { ui.binOther = false; v.bin = val; }
    pfvChanged(rec, "bin");
  });
  out.push(pfvFieldBox(rec, ui.binOther ? "" : "bin", "Agent", sel, []));
  if (ui.binOther) {
    const txt = pfvTextCtl(rec, "bin-text", v.bin, true, (val) => { v.bin = val; ui.otherText = val; pfvPaint(); }, () => pfvChanged(rec, "bin"));
    out.push(pfvFieldBox(rec, "bin", "Command", txt, ["The program to run, as a name on your PATH or a full path."]));
  }
  const claude = isClaudeBinName(v.bin);
  for (const d of pfvAgentDefs(c, v.bin)) {
    const key = "options." + d.key;
    const choiceList = pfvWithValue(d.choices, v.options[d.key] || "");
    const pickOpt = (val) => { v.options[d.key] = val; pfvChanged(rec, key); };
    if (d.key === "model" || choiceList.length > 6) {
      const none = { value: "", label: "not set: " + (claude ? "Claude Code's default" : "the agent's default") };
      const ctl = pfvSelect(rec, key, [none].concat(choiceList.map((x) => ({ value: x, label: x }))), v.options[d.key] || "", d.key === "model", pickOpt);
      out.push(pfvFieldBox(rec, key, d.label || d.key, ctl, []));
    } else {
      // Effort not set: say who decides (the mockup's line), so an unlit control is not read as an empty choice.
      const hint = claude && d.key === "effort" && !v.options[d.key] ? [PFV_EFFORT_UNSET] : [];
      out.push(pfvFieldBox(rec, key, d.label || d.key, pfvSegCtl(rec, key, choiceList, v.options[d.key] || "", pickOpt), hint, "fieldset"));
    }
  }
  if (claude) {
    const acct = pfvSelect(rec, "account", pfvAccountChoices(v.account), v.account, false, (val) => { v.account = val; pfvChanged(rec, "account"); });
    const manage = pfvDrawLink("Manage", "pfv-manage");
    // Leaving for the accounts page asks first when something here has changed.
    manage.onclick = async () => { if (await pfvLeaveGuard("leave")) openSettings("accounts"); };
    out.push(pfvFieldBox(rec, "account", "Claude login", acct, ["Add more logins in Agent accounts. ", manage]));
  }
  return out;
}

// --- Network ----------------------------------------------------------------------------------------------------

function pfvNetworkValues(c) {
  return { egress_open: c.doc.egress_open === true };
}

// Only a box has a Network block, so a save here always gives the next session a new box.
function pfvNetworkNote(rec, c) {
  return c.tier === "box";
}

function pfvNetworkForm(rec) {
  const v = rec.values;
  const lab = pfvMk("label", "pfv-check");
  const cb = pfvMk("input");
  cb.type = "checkbox";
  cb.checked = v.egress_open === true;
  cb.onchange = () => { v.egress_open = cb.checked; pfvChanged(rec, "egress_open"); };
  pfvReg(rec, "egress_open", cb);
  const t = pfvMk("span", "pfv-check-t", "Allow all internet hosts (no allowlist)");
  t.append(pfvMk("span", "pfv-check-d", "Private, tailnet and host addresses stay blocked either way."));
  lab.append(cb, t);
  const out = [lab];
  const err = pfvFerr(rec, "egress_open");
  if (err) {
    const words = pfvMk("p", "pfv-ferr", err);
    pfvBad(rec, cb, words);
    out.push(words);
  }
  if (v.egress_open) out.push(pfvMk("p", "pfv-consq", "The agent could then send this profile's credentials to any website. Every host it contacts is logged in Activity."));
  return out;
}

// --- editing: save --------------------------------------------------------------------------------------------------

// pfvPreCheck: what the client can refuse before anything is sent: {field, text} or null.
function pfvPreCheck(rec, c) {
  const v = rec.values;
  if (!v) return null;
  if (rec.id === "agent" && typeof v.bin === "string" && v.bin.trim() === "") return { field: "bin", text: PFV_NO_COMMAND };
  if (rec.id === "runs" && v.tier === "host" && pfvHostRefusal(c.doc)) return { field: "runtime", text: pfvHostRefusal(c.doc) };
  const ed = PFV_EDITORS[rec.id];
  const own = ed && ed.check ? ed.check(rec, c) : null;
  if (own) return own;
  // The permission set is checked here, on what the merge is given, not repaired in it: "none ticked" must not become
  // the six read defaults.
  const sent = ed && ed.merge ? ed.merge(rec, c) : v;
  if (sent && sent.github) {
    const text = pfvGithubError(sent.github);
    if (text) return { field: "github.permissions", text };
  }
  return null;
}

// pfvWarnLines: the lines a successful save answered with, in words (the server words one of them in config keys).
function pfvWarnLines(saved) {
  const l = saved && Array.isArray(saved.warnings) ? saved.warnings : [];
  return l.filter((x) => typeof x === "string" && x.trim() !== "").map((x) => (PFV_EGRESS_WARNING.test(x) ? PFV_EGRESS_WORDS : x));
}

// pfvSaveBlock: save block `id`: the loaded profile with this block's values merged in, and
// with those of every other open block that changed (both footers save the same merged
// profile, spec 5.6), as one PUT carrying the revision and schema the page loaded. Then show
// what the server holds now (a new revision), refetch the list, and return to the read view.
// A refusal stays on the blocks, with everything typed. Resolves true when it was saved.
async function pfvSaveBlock(id) {
  const rec = pfvState.edits[id];
  if (!rec || pfvState.saving || !pfvState.doc) return false;
  const name = pfvState.name;
  const c = pfvEditCtx();
  const ids = Object.keys(pfvState.edits).filter((i) => i === id || pfvEditDirty(pfvState.edits[i]));
  for (const r of Object.values(pfvState.edits)) { r.ferrs = []; r.top = null; r.nudge = false; r.sent = ids.includes(r.id); }
  for (const i of ids) {
    const r = pfvState.edits[i];
    const bad = pfvPreCheck(r, c);
    if (bad) {
      r.ferrs = [bad];
      pfvFillForm(r);
      pfvPaint();
      pfvFocusInvalid(r);
      return false;
    }
  }
  let doc = pfvState.doc;
  let tierMerged = false;
  // Runs in goes last: with no box layer the tier it writes depends on what the saved profile holds (runtimeToSend), and every
  // other block's merge decides that. The blocks are listed in the order they were opened, which can put Runs in first.
  const order = ids.filter((i) => i !== "runs").concat(ids.filter((i) => i === "runs"));
  for (const i of order) {
    const r = pfvState.edits[i];
    if (r.values) {
      doc = pfvMergeBlock(doc, i, PFV_EDITORS[i].merge ? PFV_EDITORS[i].merge(r, c) : r.values);
      if (i === "runs") tierMerged = true;
    }
  }
  // Whichever block it is, a save writes the tier the page shows (spec 9 rule 1): a profile that stores no runtime reads as the
  // instance's default, and the server wants an explicit box for credentials and egress. The tier did not change,
  // so there is nothing to confirm. With no box layer runtimeToSend keeps a blank unless the profile holds something that needs a box.
  if (!tierMerged) doc = pfvMergeBlock(doc, "runs", { tier: c.tier, boxAvailable: c.boxAvailable });
  // A box profile becoming host: say what that costs and what it removes, then save it without. Cancel saves nothing and
  // leaves the form as it is.
  if (ids.includes("runs") && pfvToHostPending(pfvState.edits.runs, c)) {
    if (!(await pfvConfirmHost(doc))) return false;
    if (pfvState.name !== name) return false; // the page moved on while it was asked
    doc = pfvToHost(doc);
  }
  // The revision and schema in the body are the page's (withRev, withSchema), not copies the loaded profile carries.
  const send = Object.assign({}, doc);
  delete send.rev;
  delete send.schema;
  const body = withSchema(withRev(send, pfvState.doc), pfSchema.get());
  const put = () => api("/api/profiles/" + encodeURIComponent(name), { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  pfvState.saving = true;
  pfvState.warnings = [];
  // saving stays set until the save has run its course, whatever happens in it: a throw must not leave Save and Edit refused for good.
  try {
    let saved;
    try {
      saved = rec.nodes ? await withPending(rec.nodes.save, "Saving…", put) : await put();
    } catch (e) {
      if (pfvState.name === name) pfvShowError(e, id);
      return false;
    }
    // It is written: an edit a forced close kept of it while the save was out is saved too, and must not come back as unsaved.
    if (pfvKept && pfvKept.name === name) pfvKept = null;
    // A pfvShow GET that was already out when the block was opened would answer with the profile as it was before this
    // save, so every older answer is dropped from here on. Not when the user has moved to another profile since: the GET that move
    // sent is that profile's own, and dropping it would leave the pane blank.
    if (pfvState.name === name) pfvSeq++;
    // Show what the server holds now: a failed refetch must not read as a failed save.
    let fresh = null;
    let reloadErr = null;
    try { fresh = await api("/api/profiles/" + encodeURIComponent(name)); } catch (e) { reloadErr = e; }
    await loadProfiles(false);
    if (pfvState.name !== name) return true; // the user moved on: nothing here to draw
    pfvTakeRow(name);
    pfvDropEdits();
    if (fresh) {
      pfvState.doc = fresh;
      pfvState.errors = [];
    } else {
      // What was sent, still at the old revision: the next save is refused as stale (and offers Reload) rather than overwriting.
      pfvState.doc = doc;
      pfvState.errors = ["Saved, but reloading it failed: " + (reloadErr && reloadErr.message ? reloadErr.message : "no answer")];
    }
    pfvState.warnings = pfvWarnLines(saved);
    // The first steps of a new profile end with its first change.
    pfvState.created = "";
    pfvMarkSaved(ids);
    pfvRender();
    const back = pfvRefs.edit[id];
    if (back && typeof back.focus === "function") back.focus();
    return true;
  } finally {
    pfvState.saving = false;
  }
}

// pfvShowError: a refused save, on the page. err is api()'s error; fromId the block whose Save it was.
// A stale or deleted profile is said in that block; a refusal that names a field of ANOTHER block
// opens that block too, with the words under the field, and the block being saved keeps its
// changes and says where the problem is; a refusal that names a field of this block goes under
// that field; any other is shown at the top of the block. Returns pfvSaveError's reading of it.
function pfvShowError(err, fromId) {
  const status = err && typeof err.status === "number" ? err.status : 0;
  const r = pfvSaveError(status, err && err.body && typeof err.body === "object" ? err.body : null);
  const from = pfvState.edits[fromId] || pfvState.edits[pfvState.openBlock];
  if (!from) {
    pfvState.errors = [r.text];
    pfvRender();
    return r;
  }
  for (const x of Object.values(pfvState.edits)) { x.ferrs = []; x.top = null; }
  const model = r.kind === "field" && r.block !== "" ? pfvBlockModel(r.block) : null;
  if (r.kind === "stale" || r.kind === "gone") {
    from.top = { kind: r.kind, text: r.text };
  } else if (model && r.block !== from.id) {
    const rec = pfvOpenRepair(r.block);
    rec.ferrs = [{ field: r.field, text: r.text }];
    const head = /^[^.[\]]+/.exec(r.field);
    from.top = { kind: "repair", target: r.block, text: "Not saved: " + (PFV_THING.get(head ? head[0] : "") || "a setting") + " in " + model.label + " needs a fix first." };
  } else if (model) {
    from.ferrs = [{ field: r.field, text: r.text }];
  } else {
    from.top = { kind: "error", text: r.text };
  }
  pfvRender();
  // Focus goes to the first field the refusal is under (this block's, else the block it sent the user to); a refusal no field
  // holds (a stale profile, say) leaves it on Save.
  const focused = [from].concat(Object.values(pfvState.edits).filter((x) => x !== from)).some((x) => pfvFocusInvalid(x));
  if (!focused && from.nodes && from.nodes.save && typeof from.nodes.save.focus === "function") from.nodes.save.focus();
  return r;
}

// pfvTakeRow: the page reads the profile's row of the list as well as the stored profile, and the list
// was just fetched again: the page's copy of the row is the fresh one, whether or not the nav hook ran.
function pfvTakeRow(name) {
  const row = profiles.find((p) => p.name === name);
  if (row) pfvState.view = row;
}

// pfvReload: the profile changed elsewhere: take what is stored now and drop the pending changes.
// A profile that is gone by now says so, like the save did.
async function pfvReload() {
  const name = pfvState.name;
  if (!name || pfvState.saving) return;
  pfvSeq++; // an older pfvShow answer must not land over what this fetches
  let doc;
  try { doc = await api("/api/profiles/" + encodeURIComponent(name)); }
  catch (e) {
    const at = pfvState.edits[pfvState.openBlock];
    if (!at) return;
    at.top = e && e.status === 404
      ? pfvSaveError(409, { code: "profile_changed", gone: true })
      : { kind: "error", text: "Could not reload this profile." };
    pfvPaint();
    return;
  }
  if (pfvState.name !== name) return;
  pfvState.doc = doc;
  pfvDropEdits();
  await loadProfiles(false); // the list's row changed too
  if (pfvState.name !== name) return;
  pfvTakeRow(name);
  pfvRender();
}

// pfvLeaveGone: the profile was deleted elsewhere: drop what was pending and show the next one, as Delete does.
async function pfvLeaveGone() {
  const name = pfvState.name;
  if (!name) return;
  await pfvMoveOn(name, profiles.map((p) => p.name));
}

// pfvMetaChanged: /api/meta's credential lists or /api/credentials answered (app.js
// applyCredLists and refreshPfCreds): a name the page called missing may exist now.
function pfvMetaChanged() {
  // The New profile page reads whether there are boxes, and a copy's list the credential lists (pfv-new.js).
  if (pfvState.page && pfvState.edits.draft && PFV_EDITORS.draft) { PFV_EDITORS.draft.refresh(pfvState.edits.draft, pfvEditCtx()); return; }
  pfvRefresh();
}

// pagehost.js asks this before the Settings page goes (the close buttons, Esc, another page, a session opening). It
// answers true for everything but the desktop Settings page, so it is installed once and is inert on a phone.
if (typeof pageLeaveHook !== "undefined") pageLeaveHook = pfvPageLeave;

if (typeof document !== "undefined" && typeof document.addEventListener === "function") {
  // A click anywhere else closes the header menu.
  document.addEventListener("click", (ev) => {
    if (pfvMenu && !pfvMenu.wrap.contains(ev.target)) pfvMenuClose(false);
  });
  // Tab stays inside an open drawer, whichever control (or none) has focus.
  document.addEventListener("keydown", pfvDrawerTab);
}
