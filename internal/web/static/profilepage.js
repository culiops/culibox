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
// Also reachable in pfv-cloud.js; the other blocks' Edit, Duplicate, Switch to box and
// "+ New profile" are drawn but do nothing until their tasks wire them). profileedit.js
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
// moment. warnings: the lines a successful save answered with.
const pfvState = { name: "", doc: null, view: null, openBlock: "", pending: false, errors: [], advOpen: null, edits: {}, saved: [], warnings: [], saving: false };
// The newest pfvShow wins: a slower answer for a profile you have left is dropped.
let pfvSeq = 0;
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
const PFV_MACHINE = [
  "(?<![\\w.])~?(?:\\/[\\w.@+-]+)+\\/?",
  "(?<![\\w.@/-])(?:[a-z0-9*][a-z0-9-]*\\.)+[a-z]{2,}(?![\\w-])",
  "(?<!\\d)\\d{12}(?!\\d)",
  "\\b[a-z]{2}(?:-[a-z]+)+-\\d\\b",
  "\\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\\b",
  "\\bbox\\/<branch>",
  "(?<=^Shares ).+?(?= from the original)",
  "(?<=^repo ).+$",
].join("|");

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
  const re = new RegExp((own.length ? "(?:" + own.join("|") + ")|" : "") + PFV_MACHINE, "g");
  const out = [];
  let at = 0;
  for (const m of s.matchAll(re)) {
    if (m[0] === "") continue;
    if (m.index > at) out.push(s.slice(at, m.index));
    out.push(pfvMk("span", "pfv-mono", m[0]));
    at = m.index + m[0].length;
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
    meta: meta ? { aws_connections: awscEntries, static_tokens: stEntries } : null,
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
      b.onclick = () => pfvShow(p.name);
      return b;
    });
    const add = pfvMk("button", "pfv-ni pfv-ni-add", "+ New profile");
    add.type = "button";
    add.dataset.pfvName = "";
    nav.replaceChildren(...kids, add);
    pfvNavKey = key;
    if (had !== undefined) {
      const again = Array.from(nav.children).find((b) => b.dataset.pfvName === had);
      if (again) again.focus();
    }
  }
  for (const b of Array.from(nav.children)) {
    if (b.dataset.pfvName !== "" && b.dataset.pfvName === pfvState.name) b.setAttribute("aria-current", "true");
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
  // Duplicate is the duplicate task's: it only closes the menu until then.
  pop.append(item("Duplicate", "", () => pfvMenuClose(false)), item("Delete…", " pfv-menu-red", () => { pfvMenuClose(false); return pfvDelete(); }));
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
  // there is no menu (nothing to delete). Both are the duplicate task's to wire.
  if (h.readOnly) acts.append(pfvBtn("Duplicate", "primary"));
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

// o: {block, edit}. block: the block the row belongs to (the read view's Remove link opens it with the row
// removed); edit: the row is drawn in the open block, whose Remove column is the way out, so the link is not drawn.
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
  if (err) id.append(pfvMk("p", "pfv-ferr", err));
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
  // Offered by the read model only where it can be used. Wired by the Runs in task.
  if (b.switchToBox) {
    const act = pfvMk("div", "pfv-stmt-act");
    act.append(pfvBtn("Switch to box", "secondary"));
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

// pfvDrawAdvanced: the folded block. "Advanced · N set" opens by itself when N > 0
// (pfvState.advOpen overrides once the user has toggled it); with nothing set there
// is nothing to unfold, so the label is not a button.
function pfvDrawAdvanced(b, known) {
  const open = b.count > 0 && (pfvState.advOpen === null ? b.open === true : pfvState.advOpen);
  const sec = pfvMk("section", "pfv-block" + (open ? " pfv-open" : ""));
  sec.dataset.block = b.id;
  const label = pfvMk("h4", "pfv-label pfv-adv-label");
  const status = pfvMk("span", "pfv-muted", "· " + b.status);
  if (b.count > 0) {
    const fold = pfvMk("button", "pfv-fold");
    fold.type = "button";
    fold.setAttribute("aria-expanded", open ? "true" : "false");
    fold.append(pfvChevronIcon(), b.label, " ", status);
    fold.onclick = () => { pfvState.advOpen = !open; pfvRender(); };
    label.append(fold);
  } else {
    label.append(pfvChevronIcon(), b.label, " ", status);
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
// refetch from under an edit.
async function pfvShow(name) {
  if (!pfvOnPage()) return;
  if (typeof profilesLoaded === "boolean" && !profilesLoaded) await loadProfiles(false);
  const has = (n) => profiles.some((p) => p.name === n);
  let pick = name;
  if (!pick || !has(pick)) pick = has(pfvState.name) ? pfvState.name : (profiles.find((p) => p.editable) || profiles[0] || {}).name || "";
  if (pick !== "" && pick === pfvState.name && pfvState.openBlock) return;
  const seq = ++pfvSeq;
  const same = pick !== "" && pick === pfvState.name && pfvState.doc !== null;
  if (pick !== pfvState.name) {
    pfvState.doc = null;
    pfvDropEdits();
    pfvState.advOpen = null;
    pfvState.errors = [];
    pfvState.warnings = [];
    pfvClearSaved();
  }
  pfvState.name = pick;
  pfvState.view = pick ? profiles.find((p) => p.name === pick) : null;
  pfvRenderNav();
  if (!pick) { pfvState.doc = null; pfvRender(); return; }
  if (!pfvState.doc) pfvRender();
  let doc;
  try { doc = await api("/api/profiles/" + encodeURIComponent(pick)); }
  catch (e) {
    // A block opened while this was out is being edited: the failed refresh says nothing it needs.
    if (seq === pfvSeq && !pfvState.openBlock) { pfvState.errors = ["Load failed: " + e.message]; pfvRender(); }
    return;
  }
  if (seq !== pfvSeq) return;
  if (same && pfvState.openBlock) return; // a block was opened while this was out
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
    // No profile selected until the next one is shown; the New profile task draws the empty case.
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
// the footer carries the box consequence. hideSave(rec, c): Save is not offered. fields: the
// form is a stack of labelled fields (a 16px rhythm, 440px wide); without it the form flows
// as it likes. A block of rows has two more: add(rec, what) puts a pending row in (what is
// pfvAddRow's) and answers the key of the control to focus, "" when it added nothing;
// remove(rec, row) marks the read view's row removed. A block with no entry here is drawn
// but its Edit link does nothing until its task (Advanced); one that a refused save opens
// is drawn read-only with the refusal under the row it names, so the user can see what is wrong.
const PFV_EDITORS = {
  runs: { values: pfvRunsValues, draw: pfvRunsForm, merge: pfvRunsMerge, note: pfvRunsNote, hideSave: pfvRunsHideSave },
  agent: { values: pfvAgentValues, draw: pfvAgentForm, merge: pfvAgentMerge, fields: true },
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
    tier: (view.runtime || doc.runtime) === "host" ? "host" : "box",
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
function pfvNewEdit(id, repair) {
  const ed = PFV_EDITORS[id];
  const values = ed ? ed.values(pfvEditCtx()) : null;
  return { id, values, init: pfvStable(values), repair: repair === true, sent: false, nudge: false, top: null, ferrs: [], consumed: new Set(), nodes: null, sigs: {}, fields: {}, first: null, block: null, known: [], ui: {}, disposers: [] };
}

// pfvDisposeRec: the record's form is going away (drawn again, closed or dropped): what it built that
// outlives its nodes, a picker's listeners and timers, is released.
function pfvDisposeRec(rec) {
  const fns = rec.disposers;
  rec.disposers = [];
  for (const f of fns) f();
}

const pfvEditDirty = (rec) => !!rec && rec.values !== null && pfvStable(rec.values) !== rec.init;
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
// another block); Esc, the nav, switching profile and closing the page go through the same
// function as their tasks wire them.
async function pfvLeaveGuard(action) {
  const verdict = pfvGuard({ openBlock: pfvState.openBlock, dirty: pfvAnyDirty(), action: action || "leave", target: "" });
  if (verdict === "go") return true;
  if (verdict === "refuse") { pfvNudge(); return false; }
  const ok = await askConfirm({
    title: "Discard unsaved changes?",
    body: 'The changes to "' + pfvState.name + '" have not been saved. Leaving drops them.',
    confirmLabel: "Discard",
    cancelLabel: "Keep editing",
    danger: true,
  });
  if (!ok) return false;
  pfvCloseBlock(true);
  return true;
}

// --- editing: the block and its footer --------------------------------------------------------------

// pfvDrawEditBlock: the open block: its label, the form (or, for a block opened only to show a
// refusal, the read rows), and the footer. The form is drawn from rec.values, so redrawing
// it loses nothing. Created once per draw; the parts that change as the user types are
// filled by pfvFillForm and pfvPaintEdit.
function pfvDrawEditBlock(b, rec, known) {
  const sec = pfvMk("section", "pfv-block pfv-editing");
  sec.dataset.block = b.id;
  const body = pfvMk("div", "pfv-body pfv-edit-body");
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
  sec.append(pfvMk("h4", "pfv-label", b.label), body);
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
  // A segmented control is not focusable itself: its lit button is.
  const target = el.dataset.value !== undefined ? Array.from(el.children).find((b) => b.dataset.segvalue === el.dataset.value) || el.children[0] : el;
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
  };
  paint(pfvFerr(rec, errKey));
  w.clear = () => {
    rec.ferrs = rec.ferrs.filter((e) => !pfvFieldIs(e.field, errKey));
    rec.nudge = false;
    paint("");
  };
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

function pfvRunsForm(rec, c) {
  const v = rec.values;
  const pick = (tier) => () => { v.tier = tier; pfvChanged(rec, "runtime"); };
  const boxLine = pfvMk("span", "pfv-card-d");
  if (c.boxAvailable) boxLine.append(pfvMk("span", "pfv-okdot"), "Docker found");
  else boxLine.append(pfvUnavailable(c.reason).line);
  const box = pfvCard(rec, "tier-box", {
    checked: v.tier === "box", disabled: !c.boxAvailable, title: "In a box", chip: "Recommended", pick: pick("box"),
    lines: [pfvMk("span", "pfv-card-d", "A container. It writes only to its workspace, and CuliBox scopes AWS, GitHub and tokens."), boxLine],
  });
  const host = pfvCard(rec, "tier-host", {
    checked: v.tier === "host", disabled: false, title: "On this machine", chip: "", pick: pick("host"),
    lines: [pfvMk("span", "pfv-card-d", "Runs as you, with your files, your network and your credentials.")],
  });
  // The control to focus on open: the one that is lit and can be used.
  rec.first = [rec.fields["tier-box"], rec.fields["tier-host"]].find((r) => r.checked && !r.disabled) || rec.fields["tier-host"];
  const fs = pfvMk("fieldset", "pfv-field pfv-choice");
  fs.append(pfvMk("legend", "pfv-flabel", "Where it runs"), box, host);
  const out = [fs];
  const err = pfvFerr(rec, "runtime");
  if (err) out.push(pfvMk("p", "pfv-ferr", err));
  const refusal = v.tier === "host" ? pfvHostRefusal(c.doc) : "";
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

// An option the command does not take is removed: the server refuses a Claude-only option on another command.
function pfvAgentMerge(rec, c) {
  const v = rec.values;
  const takes = new Set(pfvAgentDefs(c, v.bin).map((d) => d.key));
  const options = {};
  for (const k of Object.keys(v.options)) options[k] = takes.has(k) ? v.options[k] : "";
  return { bin: v.bin, account: v.account, options };
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
      out.push(pfvFieldBox(rec, key, d.label || d.key, pfvSegCtl(rec, key, choiceList, v.options[d.key] || "", pickOpt), [], "fieldset"));
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
  if (err) out.push(pfvMk("p", "pfv-ferr", err));
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
      return false;
    }
  }
  let doc = pfvState.doc;
  for (const i of ids) {
    const r = pfvState.edits[i];
    if (r.values) doc = pfvMergeBlock(doc, i, PFV_EDITORS[i].merge ? PFV_EDITORS[i].merge(r, c) : r.values);
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
    // It is written: a pfvShow GET that was already out when the block was opened would answer with the profile as it was before this
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
  if (from.nodes && from.nodes.save && typeof from.nodes.save.focus === "function") from.nodes.save.focus();
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
  pfvRefresh();
}

if (typeof document !== "undefined" && typeof document.addEventListener === "function") {
  // A click anywhere else closes the header menu.
  document.addEventListener("click", (ev) => {
    if (pfvMenu && !pfvMenu.wrap.contains(ev.target)) pfvMenuClose(false);
  });
}
