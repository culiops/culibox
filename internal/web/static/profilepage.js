"use strict";

// The profile overview page (spec 5.1-5.4): Settings > Profiles on the desktop
// page is the read view of one profile, "This agent will be able to", with the
// profiles listed nested under Profiles in the Settings nav. profileview.js
// decides what the page says (pfvReadView); this file only draws it, so it has no
// wording of its own beyond the page's controls. Every string drawn is server or
// operator text and goes in through textContent only.
//
// Desktop only: the old form (.plist, .ped, #foot-manage) is hidden by desktop.css
// on the page and stays for the phone's modal. Edit, "+ Add", Remove, Duplicate,
// Switch to box and "+ New profile" are drawn but do nothing yet: the edit, add,
// duplicate and new-profile tasks wire them. Classic script: api, askConfirm,
// openSpawnDialog, loadProfiles and the profile globals are app.js's, read at call
// time; every top-level name here starts with pfv/PFV_ (the page scripts share one
// scope).

// What the page shows. openBlock, pending and errors are the edit tasks' (one
// block open at a time, unsaved changes, messages to draw at the top).
const pfvState = { name: "", doc: null, view: null, openBlock: "", pending: false, errors: [], advOpen: null };
// The newest pfvShow wins: a slower answer for a profile you have left is dropped.
let pfvSeq = 0;
// The open header menu: {wrap, btn, pop}, or null.
let pfvMenu = null;

const PFV_SVG = "http://www.w3.org/2000/svg";
const PFV_EDIT_LABEL = {
  runs: "Edit where it runs", workspace: "Edit workspace", agent: "Edit agent", cloud: "Edit cloud and tokens",
  reachable: "Edit also reachable", network: "Edit network", advanced: "Edit advanced settings",
};
// The Advanced rows whose value is machine text (the rest are words).
const PFV_MONO_KINDS = ["From the original", "Arguments", "Environment", "Secrets", "Starting environment"];
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
// muted tier word, a lock for one you cannot edit, then "+ New profile".
function pfvRenderNav() {
  const nav = document.getElementById("pf-navlist");
  if (!nav || !pfvOnPage()) return; // a phone's modal keeps its own list
  const kids = (typeof profiles === "object" && Array.isArray(profiles) ? profiles : []).map((p) => {
    const b = pfvMk("button", "pfv-ni");
    b.type = "button";
    if (p.name === pfvState.name) b.setAttribute("aria-current", "true");
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
  nav.replaceChildren(...kids, add);
  // The list was refetched: the page reads the fresh row.
  const v = pfvState.name && pfvState.doc ? profiles.find((x) => x.name === pfvState.name) : null;
  if (v && v !== pfvState.view && !pfvState.openBlock) {
    pfvState.view = v;
    pfvRender();
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
  acts.append(pfvBtn("Start session", "secondary", () => openSpawnDialog(h.name)));
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

function pfvDrawScope(r, known) {
  const p = pfvMk("p", "pfv-scope");
  if (r.missing) {
    // A bound name /api/meta no longer lists: the server refuses every save until
    // it is removed, so it is an error and says so in the error colour.
    p.append(pfvMk("span", "pfv-miss", r.scope), " · ", pfvDrawLink("Remove", "pfv-rm"));
    return p;
  }
  p.append(...pfvMonoNodes(r.scope, known));
  if (r.action) p.append(" ", pfvDrawLink(r.action, "pfv-act-link"));
  return p;
}

function pfvDrawRow(r, known) {
  const row = pfvMk("div", "pfv-row");
  row.append(pfvMk("span", "pfv-k", r.kind));
  const id = pfvMk("div", "pfv-id");
  const nm = pfvMk("p", "pfv-nm");
  if (PFV_MONO_KINDS.includes(r.kind)) nm.append(pfvMk("span", "pfv-mono", r.name));
  else nm.append(...pfvMonoNodes(r.name, known));
  if (r.tag) nm.append(pfvMk("span", "pfv-tag", r.tag));
  id.append(nm);
  if (r.sub) {
    const s = pfvMk("p", "pfv-rsub");
    s.append(...pfvMonoNodes(r.sub, known));
    id.append(s);
  }
  row.append(id, pfvDrawScope(r, known));
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

function pfvDrawBody(b, known) {
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
    for (const r of b.rows) if (r.kind !== "Not home") rows.append(pfvDrawRow(r, known));
    if (b.notHome) rows.append(pfvDrawBranches(b.notHome, nh));
    body.append(rows);
  }
  if (b.note) body.append(pfvMk("p", "pfv-tnote", b.note));
  if (b.add) body.append(pfvDrawLink(b.add, "pfv-add"));
  // Offered by the read model only where it can be used. Wired by the Runs in task.
  if (b.switchToBox) {
    const act = pfvMk("div", "pfv-stmt-act");
    act.append(pfvBtn("Switch to box", "secondary"));
    body.append(act);
  }
  return body;
}

function pfvDrawAct(b) {
  const act = pfvMk("div", "pfv-act");
  if (b.edit) act.append(pfvDrawLink("Edit", "", PFV_EDIT_LABEL[b.id] || "Edit " + b.label.toLowerCase()));
  return act;
}

// pfvDrawAdvanced: the folded block. "Advanced · N set" opens by itself when N > 0
// (pfvState.advOpen overrides once the user has toggled it); with nothing set there
// is nothing to unfold, so the label is not a button.
function pfvDrawAdvanced(b, known) {
  const open = b.count > 0 && (pfvState.advOpen === null ? b.open === true : pfvState.advOpen);
  const sec = pfvMk("section", "pfv-block pfv-adv" + (open ? " pfv-open" : ""));
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
    return;
  }
  const rv = pfvReadView(pfvInputs());
  const o = pfvState.doc.options && typeof pfvState.doc.options === "object" ? pfvState.doc.options : {};
  const known = typeof o.model === "string" && o.model !== "" ? [o.model] : [];
  const sheet = pfvMk("div", "pfv-sheet");
  const tail = pfvMk("div", "pfv-sheet");
  const heading = pfvMk("h3", "pfv-sheet-h", "This agent will be able to");
  for (const b of rv.blocks) (b.id === "advanced" ? tail : sheet).append(pfvDrawBlock(b, known));
  const kids = [...pfvDrawErrors(), pfvDrawHeader(rv, known)];
  if (rv.note) kids.push(pfvDrawNote(rv.note));
  kids.push(heading, sheet, tail);
  root.replaceChildren(...kids);
  pfvMenu = null;
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
// else the first), from the list's row and the stored profile. The previous
// profile stays drawn until the new one's answer lands.
async function pfvShow(name) {
  if (!pfvOnPage()) return;
  if (typeof profilesLoaded === "boolean" && !profilesLoaded) await loadProfiles(false);
  const has = (n) => profiles.some((p) => p.name === n);
  let pick = name;
  if (!pick || !has(pick)) pick = has(pfvState.name) ? pfvState.name : (profiles.find((p) => p.editable) || profiles[0] || {}).name || "";
  const seq = ++pfvSeq;
  if (pick !== pfvState.name) {
    pfvState.doc = null;
    pfvState.openBlock = "";
    pfvState.pending = false;
    pfvState.advOpen = null;
    pfvState.errors = [];
  }
  pfvState.name = pick;
  pfvState.view = pick ? profiles.find((p) => p.name === pick) : null;
  pfvMenu = null;
  pfvRenderNav();
  if (!pick) { pfvState.doc = null; pfvRender(); return; }
  if (!pfvState.doc) pfvRender();
  let doc;
  try { doc = await api("/api/profiles/" + encodeURIComponent(pick)); }
  catch (e) {
    if (seq === pfvSeq) { pfvState.errors = ["Load failed: " + e.message]; pfvRender(); }
    return;
  }
  if (seq !== pfvSeq) return;
  pfvState.doc = doc;
  pfvState.errors = [];
  pfvRender();
}

// pfvDelete: confirm in red, delete, then show the next profile in list order (the
// one before it when it was last). The deleted profile never stays on screen.
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
  await loadProfiles(false);
  if (pfvState.name !== name) return; // the user moved on while it was deleting
  const at = order.indexOf(name);
  const next = order.slice(at + 1).concat(order.slice(0, at).reverse()).find((n) => n !== name && profiles.some((p) => p.name === n));
  // No profile left: the page is blank until the New profile task draws its page.
  pfvState.name = "";
  pfvState.doc = null;
  await pfvShow(next || "");
}

// pfvMetaChanged: /api/meta's credential lists or /api/credentials answered (app.js
// applyCredLists and refreshPfCreds): a name the page called missing may exist now.
function pfvMetaChanged() {
  if (pfvState.doc && !pfvState.openBlock) pfvRender();
}

if (typeof document !== "undefined" && typeof document.addEventListener === "function") {
  // A click anywhere else closes the header menu.
  document.addEventListener("click", (ev) => {
    if (pfvMenu && !pfvMenu.wrap.contains(ev.target)) pfvMenuClose(false);
  });
}
