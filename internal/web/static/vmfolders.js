"use strict";

// Settings > VM folders (#70, UI plan: frames M9.1-M9.5, M9.9b of the locked M9 design). The folders on this Mac the box VM can
// see: the list, the read & write / read only switch, the add card (a live dry run under the field) and Remove (one step, or
// the in-row warning when a profile uses the folder). Add, remove and the switch save the list at once; only Apply touches the
// VM (a later task). macOS only: the nav button is hidden unless /api/meta says vm_folders.
//
// vmfModel and vmfAddState are pure (jstest/vmfolders.test.js pins every word); the rest paints. Classic script: api, showToast,
// createWsPicker and openSettings are other files' globals, read at call time.

const VMF_HELPER = "Changes take effect when the box VM restarts. A folder that does not exist yet can be listed; boxes see it once you create it.";
const VMF_HELP_DEFAULT = "A folder on this Mac. Not your whole home, and not a folder that holds keys, like `~/.ssh`, `~/.aws` or `~/Library`.";
const VMF_HELP_SPAWN = "From New session: a box couldn’t use this folder because the box VM can’t see it. After you add it, Apply restarts the VM; then start the session again.";
const VMF_DEBOUNCE_MS = 250;

// vmfNames: "a", "a and b", "a, b and c".
function vmfNames(list) {
  const l = (list || []).slice();
  if (l.length <= 1) return l.join("");
  return l.slice(0, -1).join(", ") + " and " + l[l.length - 1];
}

function vmfPlural(n, one, many) {
  return n + " " + (n === 1 ? one : many);
}

// vmfStrip: the status line over the table. Applied: a dot and the sentence. Pending: no dot, the parts that are not zero
// (added, removed, switched; "switched" is new copy) and the primary Apply. Whole-home and not-set-up draw no strip.
function vmfStrip(view, n) {
  if (view.state === "applied") {
    // One folder reads "this folder" (new copy: the frame's sentence is the plural).
    return { dot: true, status: "Applied", detail: "The box VM sees only " + (n === 1 ? "this folder" : "these " + n + " folders") + " and CuliBox data." };
  }
  if (view.state === "pending") {
    const parts = [];
    if (view.added > 0) parts.push(view.added + " added");
    if (view.removed > 0) parts.push(view.removed + " removed");
    if (view.switched > 0) parts.push(view.switched + " switched");
    return { dot: false, status: "Changes not applied yet", detail: parts.join(", ") + ". The box VM still sees the old list.", action: "Apply (restart box VM)" };
  }
  return null;
}

// vmfPendingNote: the line under a pending strip. With none running the sentence is M9.6's own "No box sessions are running."
function vmfPendingNote(view) {
  const n = (view.running_boxed || []).length;
  const tail = n === 0 ? "No box sessions are running." :
    "The " + (n === 1 ? "1 session running in a box ends" : n + " sessions running in boxes end") + "; host sessions keep running.";
  return "Applying restarts the box VM, about 20 s. " + tail;
}

function vmfUsedBy(r) {
  if (r.used_by && r.used_by.length) return r.used_by.join(", ");
  if (r.running > 0) return vmfPlural(r.running, "running session", "running sessions");
  return "";
}

// vmfModel(view): everything the page draws, as data. rows ends with the fixed CuliBox data row (data: true); count is the
// chip beside the card title and leaves that row out.
function vmfModel(view, cardOpen) {
  const v = view || {};
  const src = v.rows || [];
  const live = src.filter((r) => r.pending !== "removed").length;
  const whole = v.state === "whole_home";
  const rows = src.map((r) => {
    const removed = r.pending === "removed";
    const used = r.used_by || [];
    let origin = r.origin || "";
    if (removed && used.length) origin += " · " + vmfNames(used) + " sessions are refused once applied";
    return {
      path: r.path, display: r.display || r.path, mode: r.mode, removed,
      chip: r.pending === "added" ? "Added · not applied" : removed ? "Removed · not applied" : "",
      origin,
      seg: removed ? null : r.mode,
      modeText: r.mode === "ro" ? "Read only" : "Read & write",
      usedBy: used, running: r.running || 0,
      usedText: vmfUsedBy(r),
      action: removed ? "Undo" : "Remove",
    };
  });
  rows.push({
    data: true, display: "CuliBox data",
    origin: "Box homes and the harness bundle, from " + (v.data_root || "") + ". Never your config or credentials.",
    modeText: "Set by CuliBox", usedText: "Every box",
  });
  const applying = !!(v.apply && v.apply.state === "running");
  // A running or failed job is a card (M9.7, M9.8) in place of the strip and the banner; only a running one locks the table.
  const card = applying || !!(v.apply && v.apply.state === "failed");
  return {
    card,
    locked: applying,
    strip: card ? null : vmfStrip(v, live),
    note: v.state === "pending" && !card ? vmfPendingNote(v) : "",
    banner: whole && !card ? {
      title: "The box VM can see your whole home folder",
      body: "It shares `~` read & write, so a box that broke out of its container could change any file in it, like `~/.zshrc`. Apply the list below and the VM sees only " + (live === 1 ? "the folder" : "the " + live + " folders") + " your boxes use.",
      muted: "Nothing changes until you apply: sessions still start as before.",
      action: "Apply (restart box VM)",
      terminal: "or, in a terminal, `agent-box box share apply`",
    } : null,
    title: whole ? "Proposed folders" : "Shared folders",
    subtitle: whole ? "Made from your profiles and the folders boxed sessions ran in" : "",
    count: live,
    rows,
    under: card || cardOpen ? "" : VMF_HELPER, // M9.1/2/3/5/6 only: not while the add card is open, applying or failed
    headAddDisabled: applying,
  };
}

// vmfPhoneModel(m): the phone's list from the page model (same words; nothing new). Each row also carries the two-way switch as
// data. The strip and banner lose their Apply button: on the phone the footer holds it (vmfFootModel).
function vmfPhoneModel(m) {
  const rows = m.rows.map((r) => Object.assign({}, r, {
    usedLabel: "Used by",
    usedText: r.usedText || (r.data ? "" : "No profile"),
    seg: r.seg ? { value: r.seg, items: [["rw", "Read & write"], ["ro", "Read only"]] } : null,
    action: r.data ? "" : r.action,
  }));
  const strip = m.strip ? Object.assign({}, m.strip) : null;
  if (strip) delete strip.action;
  const banner = m.banner ? Object.assign({}, m.banner) : null;
  if (banner) delete banner.action;
  return Object.assign({}, m, { rows, strip, banner });
}

// vmfFootModel(view, job, cardOpen): the phone footer. Apply shows for a pending list or the whole-home proposal and waits while a
// restart runs; Add folder waits with it and while the add form is open.
function vmfFootModel(view, job, cardOpen) {
  const v = view || {};
  const running = !!(job && job.state === "running");
  return { apply: v.state === "pending" || v.state === "whole_home", applyDisabled: running, addDisabled: running || !!cardOpen };
}

// vmfRemoveCopy(row): the in-row question for a folder a profile uses (M9.5), or null when nothing uses it and Remove is one step.
function vmfRemoveCopy(row) {
  const used = row.usedBy || [];
  if (!used.length) return null;
  const who = vmfNames(used);
  return {
    title: "Remove `" + (row.display || row.path) + "`?",
    body: who + (used.length === 1 ? " uses it. Once you apply, its sessions are refused until you add it back." : " use it. Once you apply, their sessions are refused until you add it back."),
    note: "Files in the folder are not touched.",
  };
}

// vmfCheckFrom(outcome, rows): the add card's dry-run result, as vmfAddState reads it. outcome is {ok:true, result} from a 200 or
// {ok:false, code, error} from a refusal; rows are the view's rows (the covering folder's display and mode are found there).
function vmfCheckFrom(outcome, rows) {
  if (!outcome) return null;
  if (!outcome.ok) return { kind: "error", code: outcome.code || "", error: outcome.error || "" };
  const by = outcome.result && outcome.result.covered_by;
  if (!by) return { kind: "ok" };
  const hit = (rows || []).find((r) => String(r.path).toLowerCase() === String(by).toLowerCase());
  return { kind: "covered", display: hit ? hit.display || hit.path : by, mode: hit ? hit.mode : "rw" };
}

// vmfErrorText(check): the sentence under the field. The server's rw_in_ro text ("A needs write access but B is shared read-only;
// make B read-write or remove it") becomes the M9.4c sentence with both paths as code; every error ends with a period.
function vmfErrorText(check) {
  let t = String(check.error || "").trim();
  if (check.code === "rw_in_ro") {
    const m = /^(.+?) needs write access but (.+?) is shared read-only; make (.+?) read-write or remove it\.?$/.exec(t);
    if (m && m[2] === m[3]) t = "`" + m[1] + "` needs write access but `" + m[2] + "` is shared read-only; make `" + m[2] + "` read-write or remove it";
  }
  return t && !/[.!?]$/.test(t) ? t + "." : t;
}

// vmfAddState(input, mode, check): the add card's field message and button. An empty field has nothing to add. A refusal is
// red under the field; a folder a listed one already covers is a muted note and nothing to add; only that one disables Add.
function vmfAddState(input, mode, check) {
  const empty = String(input || "").trim() === "";
  const s = { invalid: false, error: "", note: "", addDisabled: empty };
  if (empty || !check) return s;
  if (check.kind === "error") { s.invalid = true; s.error = vmfErrorText(check); return s; }
  if (check.kind === "covered") {
    s.note = "Already covered by `" + check.display + "`, which boxes can " + (check.mode === "ro" ? "read" : "read and write") + ". Nothing to add.";
    s.addDisabled = true;
  }
  return s;
}

function vmfAddHelp(from) {
  return from === "spawn" ? VMF_HELP_SPAWN : VMF_HELP_DEFAULT;
}

// ---- painting --------------------------------------------------------------------------------------------------------------

let vmfView = null;
let vmfArmed = "";      // the path of the row showing the in-row remove question
let vmfBusy = false;    // a save is in flight: the switch and the buttons wait
let vmfCard = null;     // the open add card: { path, mode, from, check, seq, timer, picker, els }
let vmfLoadSeq = 0;

function vmfEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

// vmfRich: text with `code` parts set in mono (textContent only: a path is user input).
function vmfRich(parent, text) {
  String(text).split("`").forEach((part, i) => {
    if (!part) return;
    if (i % 2) parent.appendChild(vmfEl("span", "mono", part));
    else parent.appendChild(document.createTextNode(part));
  });
  return parent;
}

function vmfBtn(label, cls, onclick) {
  const b = vmfEl("button", cls, label);
  b.type = "button";
  b.onclick = onclick;
  return b;
}

function vmfRole(el, role) {
  el.setAttribute("role", role);
  return el;
}

// The desktop page (pagehost.js sets as-page) or the phone's modal. Asked at paint time, like tokens.js: a modal opened on a phone
// stays a modal when the window widens, and a page stays a page when it narrows, so the next paint matches the dialog.
function vmfPhoneMode() {
  if (typeof document === "undefined") return false;
  const m = document.getElementById("settings-modal");
  return !m || !m.classList.contains("as-page");
}

function vmfLock() {
  const NS = "http://www.w3.org/2000/svg";
  const s = document.createElementNS(NS, "svg");
  s.setAttribute("class", "vmf-lock");
  s.setAttribute("viewBox", "0 0 16 16");
  s.setAttribute("width", "12");
  s.setAttribute("height", "12");
  s.setAttribute("fill", "none");
  s.setAttribute("stroke", "currentColor");
  s.setAttribute("stroke-width", "1.5");
  s.setAttribute("role", "img");
  s.setAttribute("aria-label", "Always shared, set by CuliBox");
  const body = document.createElementNS(NS, "rect");
  for (const [k, v] of [["x", "3.5"], ["y", "7"], ["width", "9"], ["height", "6.5"], ["rx", "1.5"]]) body.setAttribute(k, v);
  const arc = document.createElementNS(NS, "path");
  arc.setAttribute("d", "M5.5 7V5a2.5 2.5 0 0 1 5 0v2");
  s.append(body, arc);
  return s;
}

function vmfSeg(row, locked) {
  const cur = row.seg && row.seg.value ? row.seg.value : row.seg; // the phone's model carries the switch as {value, items}
  const seg = vmfEl("div", "seg vmf-seg");
  seg.setAttribute("role", "group");
  seg.setAttribute("aria-label", "What boxes can do in " + row.display);
  for (const [mode, label] of [["rw", "Read & write"], ["ro", "Read only"]]) {
    const b = vmfBtn(label, "seg-item", () => { if (cur !== mode) vmfSave(() => vmfPatch(row.path, mode)); });
    b.setAttribute("aria-pressed", cur === mode ? "true" : "false");
    if (vmfPhoneMode()) b.classList.toggle("on", cur === mode); // the phone's .seg marks the choice with .on, the page's kit with aria-pressed
    b.disabled = !!locked;
    seg.appendChild(b);
  }
  return seg;
}

function vmfRowEl(row, locked) {
  const tr = vmfRole(vmfEl("tr", row.removed ? "vmf-removed" : ""), "row");
  const td = (cls) => vmfRole(vmfEl("td", cls), "cell");
  const f = td("vmf-folder");
  const line = vmfEl("div", "vmf-path");
  line.appendChild(vmfEl("span", row.data ? "t-md w-500" : "mono", row.display)); // the data row's label is a name, not a path
  if (row.data) line.appendChild(vmfLock());
  if (row.chip) line.appendChild(vmfEl("span", "chip", row.chip));
  f.append(line, vmfRich(vmfEl("div", "vmf-origin t-sm muted"), row.origin));
  const mode = td(row.data || row.removed ? "muted" : "");
  if (row.seg) mode.appendChild(vmfSeg(row, locked));
  else mode.textContent = row.modeText;
  const used = td(row.usedText ? "" : "muted");
  used.textContent = row.usedText || "No profile";
  const act = td("num");
  if (!row.data) {
    const b = vmfBtn(row.action, "btn btn-ghost btn-sm", () => (row.removed ? vmfSave(() => vmfAdd(row.path, row.mode), row.path) : vmfRemoveClick(row)));
    b.setAttribute("aria-label", row.action + " " + row.display);
    b.dataset.vmfAct = row.path;
    b.disabled = !!locked;
    act.appendChild(b);
  }
  tr.append(f, mode, used, act);
  return tr;
}

// The in-row question (M9.5): a neutral banner across the row's four columns, focus on Keep it.
function vmfAskRow(row, copy) {
  const tr = vmfRole(vmfEl("tr", "vmf-ask"), "row");
  const td = vmfRole(vmfEl("td"), "cell");
  td.colSpan = 4;
  const box = vmfEl("div", "vmf-askbox");
  box.setAttribute("role", "alert");
  box.append(vmfRich(vmfEl("p", "banner-title vmf-asktitle"), copy.title), vmfEl("p", "t-md", copy.body), vmfEl("p", "t-sm muted", copy.note));
  const acts = vmfEl("div", "banner-acts");
  const keep = vmfBtn("Keep it", "btn btn-sm", () => { vmfArmed = ""; vmfPaint(); vmfFocusRow(row.path); });
  const go = vmfBtn("Remove", "btn btn-danger btn-sm", () => { vmfArmed = ""; vmfSave(() => vmfDelete(row.path), row.path); });
  acts.append(keep, go);
  box.appendChild(acts);
  td.appendChild(box);
  tr.appendChild(td);
  vmfFocusKeep = keep;
  return tr;
}
let vmfFocusKeep = null;

function vmfTable(m) {
  const table = vmfRole(vmfEl("table", "tbl vmf-tbl"), "table");
  table.setAttribute("aria-label", m.title);
  table.tabIndex = -1; // where focus lands when the row it was on is gone
  const cols = vmfEl("colgroup");
  for (let i = 0; i < 4; i++) cols.appendChild(vmfEl("col"));
  table.appendChild(cols);
  const head = vmfRole(vmfEl("tr"), "row");
  for (const h of ["Folder", "Boxes can", "Used by", ""]) {
    const th = vmfRole(vmfEl("th", h ? "" : "num", h), "columnheader");
    if (!h) th.appendChild(vmfEl("span", "sr-only", "Actions"));
    head.appendChild(th);
  }
  table.appendChild(vmfRole(vmfEl("thead"), "rowgroup")).appendChild(head);
  const body = vmfRole(vmfEl("tbody"), "rowgroup");
  vmfFocusKeep = null;
  for (const r of m.rows) {
    body.appendChild(vmfRowEl(r, m.locked));
    const copy = !r.data && !r.removed && !m.locked && r.path === vmfArmed ? vmfRemoveCopy(r) : null;
    if (copy) body.appendChild(vmfAskRow(r, copy));
  }
  table.appendChild(body);
  return table;
}

function vmfStripEl(s, note) {
  const wrap = vmfEl("div", "vmf-stripwrap");
  const el = vmfEl("div", "vmf-strip");
  if (s.dot) el.appendChild(vmfEl("span", "dot dot-run"));
  el.append(vmfEl("span", "vmf-status", s.status), vmfEl("span", "vmf-detail muted", s.detail));
  if (s.action) {
    const b = vmfBtn(s.action, "btn btn-primary btn-sm vmf-apply", () => { if (typeof globalThis.vmfApplyOpen === "function") globalThis.vmfApplyOpen(); });
    b.id = "vmf-apply";
    el.appendChild(b);
  }
  wrap.appendChild(el);
  if (note) wrap.appendChild(vmfEl("p", "t-sm muted vmf-note", note));
  return wrap;
}

function vmfBannerEl(b) {
  const el = vmfEl("div", "banner vmf-banner");
  el.setAttribute("role", "status");
  el.append(vmfEl("p", "banner-title t-md", b.title), vmfRich(vmfEl("p", "t-sm", ""), b.body), vmfEl("p", "t-sm muted", b.muted));
  const acts = vmfEl("div", "banner-acts");
  const apply = vmfBtn(b.action, "btn btn-primary btn-sm", () => { if (typeof globalThis.vmfApplyOpen === "function") globalThis.vmfApplyOpen(); });
  apply.id = "vmf-apply";
  acts.append(apply, vmfRich(vmfEl("span", "t-sm muted"), b.terminal));
  el.appendChild(acts);
  return el;
}

// vmfJob: the apply job the page shows (the global one, vmapply.js, followed by its own poller).
function vmfJob() {
  // Only the global job (every source goes through vmaFromMeta / vmaSeen, which stamp when this browser got it); a view's own
  // copy would freeze the elapsed time, and could bring back a card the global state dropped.
  if (typeof vmApplyCurrent === "function") return vmApplyCurrent();
  return (vmfView && vmfView.apply) || null;
}

function vmfJobRunning() {
  const j = vmfJob();
  return !!(j && j.state === "running");
}

function vmfPaint() {
  const body = document.getElementById("vmf-body");
  if (!body || !vmfView) return;
  const m = vmfModel(Object.assign({}, vmfView, { apply: vmfJob() }), !!vmfCard);
  if (m.locked) vmfArmed = "";
  vmfProg = null;
  vmfEnsureSlot();
  const main = document.getElementById("vmf-main");
  const kids = [];
  if (vmfView.list_error) {
    const e = vmfEl("p", "field-error", vmfView.list_error);
    e.setAttribute("role", "alert");
    kids.push(e);
  }
  const pm = m.card && typeof vmApplyProgressModel === "function" ? vmApplyProgressModel(vmfJob(), Date.now()) : null;
  if (vmfPhoneMode()) { vmfPaintPhone(main, kids, vmfPhoneModel(m), pm); return; }
  if (pm) kids.push(vmfProgressEl(pm, vmfJob()));
  else if (m.banner) kids.push(vmfBannerEl(m.banner));
  else if (m.strip) kids.push(vmfStripEl(m.strip, m.note));
  const card = vmfEl("div", "vmf-fill");
  const head = vmfEl("div", "card-head");
  head.appendChild(vmfEl("span", "vmf-title", m.title));
  head.appendChild(vmfEl("span", "chip", String(m.count)));
  if (m.subtitle) head.appendChild(vmfEl("span", "t-sm muted", m.subtitle));
  card.append(head, vmfTable(m));
  kids.push(card);
  if (m.under) kids.push(vmfEl("p", "t-sm muted vmf-under", m.under));
  main.replaceChildren(...kids);
  if (vmfFocusKeep && typeof vmfFocusKeep.focus === "function") vmfFocusKeep.focus();
  vmfSyncHead();
}

// ---- the phone's layout (#70 Task 16): the current mobile style, a list of cards, the footer holds the actions ---------------

function vmfPaintPhone(main, kids, pm, prog) {
  const j = vmfJob();
  if (prog) kids.push(vmfProgressEl(prog, j));
  else if (pm.banner) {
    const b = vmfEl("div", "vmf-banner");
    b.setAttribute("role", "status");
    b.append(vmfEl("p", "vmf-banner-t", pm.banner.title), vmfRich(vmfEl("p", "hint", ""), pm.banner.body), vmfEl("p", "hint", pm.banner.muted), vmfRich(vmfEl("p", "hint", ""), pm.banner.terminal));
    kids.push(b);
  } else if (pm.strip) {
    const s = vmfEl("div", "vmf-pstrip");
    const line = vmfEl("p", "vmf-pstatus");
    if (pm.strip.dot) line.appendChild(vmfEl("span", "dot dot-run"));
    line.appendChild(vmfEl("b", "", pm.strip.status));
    s.append(line, vmfEl("p", "hint", pm.strip.detail));
    if (pm.note) s.appendChild(vmfEl("p", "hint", pm.note));
    kids.push(s);
  }
  const head = vmfEl("p", "vmf-ptitle", pm.title);
  head.appendChild(vmfEl("span", "hint", String(pm.count)));
  kids.push(head);
  if (pm.subtitle) kids.push(vmfEl("p", "hint vmf-psub", pm.subtitle));
  const ul = vmfEl("ul", "vmf-plist");
  vmfFocusKeep = null;
  for (const r of pm.rows) {
    ul.appendChild(vmfPhoneRow(r, pm.locked));
    const copy = !r.data && !r.removed && !pm.locked && r.path === vmfArmed ? vmfRemoveCopy(r) : null;
    if (copy) ul.lastChild.append(vmfPhoneAsk(r, copy));
  }
  kids.push(ul);
  if (pm.under) kids.push(vmfEl("p", "hint vmf-under", pm.under));
  main.replaceChildren(...kids);
  if (vmfFocusKeep && typeof vmfFocusKeep.focus === "function") vmfFocusKeep.focus();
  vmfSyncHead();
}

function vmfPhoneRow(r, locked) {
  const li = vmfEl("li", "vmf-prow" + (r.removed ? " vmf-removed" : "") + (r.data ? " vmf-pdata" : ""));
  const line = vmfEl("div", "vmf-path");
  line.appendChild(vmfEl("span", "mono", r.display));
  if (r.data) line.appendChild(vmfLock());
  if (r.chip) line.appendChild(vmfEl("span", "hint vmf-pchip", r.chip));
  li.append(line, vmfRich(vmfEl("div", "hint vmf-origin"), r.origin));
  const used = vmfEl("div", "vmf-pused");
  used.append(vmfEl("span", "hint", r.usedLabel), vmfEl("span", r.usedText === "No profile" ? "hint" : "", r.usedText));
  if (r.seg) li.append(used, vmfSeg(r, locked));
  else li.append(used, vmfEl("div", "hint", r.modeText));
  if (r.action) {
    const b = vmfBtn(r.action === "Remove" ? "Remove…" : r.action, "btn" + (r.action === "Remove" ? " dgr" : ""), () => (r.removed ? vmfSave(() => vmfAdd(r.path, r.mode), r.path) : vmfRemoveClick(r)));
    b.setAttribute("aria-label", r.action + " " + r.display);
    b.dataset.vmfAct = r.path;
    b.disabled = !!locked;
    li.appendChild(b);
  }
  return li;
}

// The in-row question (M9.5) on the phone: the row arms like a token's revoke, Keep it / Remove side by side.
function vmfPhoneAsk(r, copy) {
  const box = vmfEl("div", "vmf-pask");
  box.setAttribute("role", "alert");
  box.append(vmfRich(vmfEl("p", "vmf-banner-t vmf-asktitle"), copy.title), vmfEl("p", "", copy.body), vmfEl("p", "hint", copy.note));
  const acts = vmfEl("div", "vmf-pacts");
  const keep = vmfBtn("Keep it", "btn", () => { vmfArmed = ""; vmfPaint(); vmfFocusRow(r.path); });
  const go = vmfBtn("Remove", "btn dgr", () => { vmfArmed = ""; vmfSave(() => vmfDelete(r.path), r.path); });
  acts.append(keep, go);
  box.appendChild(acts);
  vmfFocusKeep = keep;
  return box;
}

// vmfSyncFoot: the phone footer's Add folder and Apply follow the view, the job and the open add form.
function vmfSyncFoot() {
  if (!vmfPhoneMode()) return; // the desktop page has no footer (desktop.css hides it)
  const add = document.getElementById("vmf-foot-add");
  const apply = document.getElementById("vmf-foot-apply");
  if (!add && !apply) return;
  const f = vmfFootModel(vmfView, vmfJob(), !!vmfCard);
  if (add) { add.disabled = f.addDisabled; add.onclick = () => openVMFoldersAdd("", "rw", ""); }
  if (apply) { apply.hidden = !f.apply; apply.disabled = f.applyDisabled; apply.onclick = () => vmfApplyOpen(); }
}

// vmfSyncHead: the header's Add folder waits while the card is open and while the VM restarts.
function vmfSyncHead() {
  vmfSyncFoot();
  const under = document.querySelector("#vmfolders .vmf-under");
  if (under) under.hidden = !!vmfCard;
  else if (!vmfCard && vmfView && vmfModel(Object.assign({}, vmfView, { apply: vmfJob() })).under) vmfPaint(); // the card closed: bring the helper back
  const b = document.getElementById("set-act-vmfolders");
  if (!b) return;
  const j = vmfJob();
  b.disabled = !!vmfCard || !!(j && j.state === "running");
}

function vmfSetView(v) {
  vmfView = v;
  if (vmfArmed && !(v.rows || []).some((r) => r.path === vmfArmed && r.pending !== "removed")) vmfArmed = "";
  vmfPaint();
}

async function loadVMFolders() {
  const seq = ++vmfLoadSeq;
  let v;
  try { v = await api("/api/vm-folders"); } catch (e) {
    if (seq !== vmfLoadSeq) return;
    vmfEnsureSlot();
    const main = document.getElementById("vmf-main");
    if (main && !vmfView) main.replaceChildren(vmfEl("p", "field-error", "Could not load the VM folders: " + e.message));
    return;
  }
  if (seq !== vmfLoadSeq) return;
  // An apply the server says is running or failed (or no longer reports): the global state and the sidebar banner follow (vmapply.js).
  if (v && typeof vmaFromMeta === "function") vmaFromMeta(v.apply);
  vmfSetView(v);
}

// ---- saves -----------------------------------------------------------------------------------------------------------------

function vmfJSON(method, body) {
  return { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

async function vmfPatch(path, mode) { return api("/api/vm-folders", vmfJSON("PATCH", { path, mode })); }
async function vmfDelete(path) { return api("/api/vm-folders?path=" + encodeURIComponent(path), { method: "DELETE" }); }
async function vmfAdd(path, mode) { const r = await api("/api/vm-folders", vmfJSON("POST", { path, mode, dry_run: false })); return r.view; }

// vmfSave: one change at a time; the page repaints from the answer, a refusal is a toast (the add card has its own place).
// One sequence for loads and saves (vmfLoadSeq): a GET that started before a save, or while it ran, never paints over its answer.
async function vmfSave(run, focusPath) {
  if (vmfBusy) return;
  vmfBusy = true;
  vmfLoadSeq++;
  try {
    const v = await run();
    vmfLoadSeq++;
    vmfSetView(v);
    if (focusPath) vmfFocusRow(focusPath);
  } catch (e) {
    if (typeof showToast === "function") showToast(e.message);
    loadVMFolders();
  } finally { vmfBusy = false; }
}

function vmfRemoveClick(row) {
  if (vmfRemoveCopy(row)) { vmfArmed = row.path; vmfPaint(); return; }
  vmfSave(() => vmfDelete(row.path), row.path);
}

// vmfFocusRow: focus goes back to the row's own Remove (or Undo) button; when the row is gone, to the table.
function vmfFocusRow(path) {
  const main = document.getElementById("vmf-main");
  if (!main) return;
  const btn = Array.from(main.querySelectorAll("button")).find((b) => b.dataset && b.dataset.vmfAct === path && !b.disabled);
  const target = btn || main.querySelector("table");
  if (target && typeof target.focus === "function") target.focus();
}

// ---- the add card ----------------------------------------------------------------------------------------------------------

function vmfModeCard(name, mode, title, text, on, pick) {
  const label = vmfEl("label", "vmf-opt" + (on ? " is-on" : ""));
  const radio = vmfEl("input");
  radio.type = "radio";
  radio.name = "vmf-mode";
  radio.value = mode;
  radio.checked = on;
  radio.onchange = () => pick(mode);
  const t = vmfEl("span", "vmf-opt-t", title);
  const d = vmfEl("span", "vmf-opt-d t-sm muted", text);
  label.append(radio, t, d);
  return label;
}

function vmfCardSync() {
  const c = vmfCard;
  if (!c) return;
  const st = vmfAddState(c.els.input.value, c.mode, c.check);
  c.els.input.classList.toggle("is-invalid", st.invalid);
  c.els.input.setAttribute("aria-invalid", st.invalid ? "true" : "false");
  const msg = c.els.msg;
  const phone = c.els.input.classList.contains("fld");
  msg.className = st.invalid ? (phone ? "dlg-err" : "field-error") : (phone ? "hint" : "field-help");
  msg.replaceChildren();
  vmfRich(msg, st.invalid ? st.error : st.note || vmfAddHelp(c.from));
  if (st.invalid) msg.setAttribute("role", "alert"); else msg.removeAttribute("role");
  c.els.add.disabled = st.addDisabled || c.busy === true || vmfJobRunning();
  for (const l of c.els.opts) l.classList.toggle("is-on", l.firstChild.checked);
}

// The dry run: what the field holds now, asked of the server (nothing is saved). A newer keystroke supersedes an older answer.
function vmfCardCheck() {
  const c = vmfCard;
  if (!c) return;
  const typed = c.els.input.value.trim();
  const seq = ++c.seq;
  if (c.timer) clearTimeout(c.timer);
  if (!typed) { c.check = null; vmfCardSync(); return; }
  c.timer = setTimeout(async () => {
    let out;
    try {
      const r = await api("/api/vm-folders", vmfJSON("POST", { path: typed, mode: c.mode, dry_run: true }));
      out = { ok: true, result: r.result, view: r.view };
    } catch (e) {
      if (e.status !== 400 && e.status !== 409 && e.status !== 423) return;
      out = { ok: false, code: e.code, error: e.message };
    }
    if (vmfCard !== c || seq !== c.seq) return;
    c.check = vmfCheckFrom(out, (out.view || vmfView || {}).rows);
    vmfCardSync();
  }, VMF_DEBOUNCE_MS);
}

async function vmfCardSubmit() {
  const c = vmfCard;
  if (!c || c.busy || vmfJobRunning()) return;
  const typed = c.els.input.value.trim();
  if (!typed) return;
  c.busy = true;
  vmfCardSync();
  try {
    vmfLoadSeq++;
    const r = await api("/api/vm-folders", vmfJSON("POST", { path: typed, mode: c.mode, dry_run: false }));
    vmfLoadSeq++;
    c.busy = false;
    if (r.result && r.result.covered_by) {
      c.check = vmfCheckFrom({ ok: true, result: r.result }, r.view.rows);
      vmfCardSync();
      return;
    }
    vmfCloseCard();
    vmfSetView(r.view);
  } catch (e) {
    c.busy = false;
    c.check = { kind: "error", code: e.code || "", error: e.message };
    vmfCardSync();
  }
}

function vmfCloseCard() {
  const c = vmfCard;
  if (!c) return;
  if (c.timer) clearTimeout(c.timer);
  c.seq++;
  if (c.picker) c.picker.destroy();
  vmfCard = null;
  const slot = document.getElementById("vmf-card-slot");
  if (slot) slot.replaceChildren();
  vmfSyncHead();
}

function vmfBuildCard(path, mode, from) {
  const slot = document.getElementById("vmf-card-slot");
  if (!slot) return;
  const els = { opts: [] };
  const phone = vmfPhoneMode();
  const card = vmfEl("div", "vmf-fill vmf-add" + (phone ? " vmf-padd" : ""));
  card.setAttribute("role", "group");
  card.setAttribute("aria-label", "Add a folder");
  const body = vmfEl("div", "vmf-add-body");
  body.appendChild(vmfEl("div", "vmf-add-lbl", "Add a folder"));
  const form = vmfEl("div", "vmf-add-form");

  const field = vmfEl("div", phone ? "fcol vmf-fieldbox" : "field vmf-fieldbox");
  const lab = vmfEl("label", phone ? "flab" : "field-label", "Folder");
  const input = vmfEl("input", phone ? "fld mono" : "input mono");
  input.id = "vmf-path-in";
  input.type = "text";
  input.setAttribute("autocomplete", "off");
  input.setAttribute("spellcheck", "false");
  input.setAttribute("role", "combobox");
  input.setAttribute("aria-expanded", "false");
  input.setAttribute("aria-autocomplete", "list");
  lab.setAttribute("for", input.id);
  const list = vmfEl("div", "vmf-wslist");
  list.id = "vmf-ws-list";
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Directories");
  list.hidden = true;
  input.setAttribute("aria-controls", list.id);
  const msg = vmfEl("span", phone ? "hint" : "field-help");
  msg.id = "vmf-msg";
  input.setAttribute("aria-describedby", msg.id);
  // The picker closes on any click outside the field and its list, this one included, so it opens once the click has finished.
  const browse = vmfBtn("Browse…", "btn", () => { input.focus(); setTimeout(() => { if (vmfCard && vmfCard.picker) vmfCard.picker.open(); }, 0); });
  const row = vmfEl("div", "vmf-inrow");
  // The phone has no Browse… and no directory list: the list is a fixed overlay that would sit on the message under the field, and a
  // phone types a path or pastes one. The dry run under the field still answers (stated in the Task 16 report).
  if (phone) {
    for (const a of ["role", "aria-expanded", "aria-autocomplete", "aria-controls"]) input.removeAttribute(a); // no list to control
    row.append(input);
    field.append(lab, row, msg);
  } else { row.append(input, browse); field.append(lab, row, list, msg); }

  const fs = vmfEl("fieldset", "vmf-modes");
  fs.appendChild(vmfEl("legend", phone ? "flab" : "field-label", "Boxes can"));
  const pick = (m) => { vmfCard.mode = m; vmfCardSync(); vmfCardCheck(); };
  const rw = vmfModeCard("rw", "rw", "Read & write", "For a workspace, or where clone copies live.", mode !== "ro", pick);
  const ro = vmfModeCard("ro", "ro", "Read only", "For notes, docs or runbooks a box only reads.", mode === "ro", pick);
  els.opts.push(rw, ro);
  const opts = vmfEl("div", "vmf-optrow");
  opts.append(rw, ro);
  fs.appendChild(opts);
  form.append(field, fs);
  body.appendChild(form);

  const foot = vmfEl("div", "vmf-add-foot");
  const add = vmfBtn("Add folder", "btn btn-primary", vmfCardSubmit);
  foot.append(vmfEl("span", "t-sm muted vmf-hint", "Added to the list now. The box VM sees it after you apply."), vmfBtn("Cancel", "btn", vmfCloseCard), add);
  card.append(body, foot);
  slot.replaceChildren(card);

  Object.assign(els, { input, msg, add });
  vmfCard = { path, mode: mode === "ro" ? "ro" : "rw", from: from || "", check: null, seq: 0, timer: null, picker: null, busy: false, els };
  if (typeof createWsPicker === "function" && !phone) {
    vmfCard.picker = createWsPicker({
      input, list, keepEsc: true, reanchor: true,
      openOnFocus: () => false, openOnClick: () => false, // on demand only: Browse… opens the list, so no idle caption sits over the field message
      onPick: () => { vmfCardSync(); vmfCardCheck(); },
      onNote: () => {},
    });
    vmfCard.picker.set(path || "");
  } else input.value = path || "";
  input.addEventListener("input", () => { vmfCard.check = null; vmfCardSync(); vmfCardCheck(); });
  card.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && e.target === input && !(vmfCard.picker && vmfCard.picker.isOpen())) { e.preventDefault(); vmfCardSubmit(); }
    else if (e.key === "Escape" && !(vmfCard.picker && vmfCard.picker.isOpen())) { e.stopPropagation(); vmfCloseCard(); }
  });
  vmfCardSync();
  vmfSyncHead();
  if (path) vmfCardCheck();
  input.focus();
}

// openVMFoldersAdd(path, mode, from): open the add card, with `path` in the field and `mode` ("rw" or "ro") chosen. from "spawn"
// is a refused New session (M9.9b): its help says why the folder is here. Opens the page first when it is not showing.
function openVMFoldersAdd(path, mode, from) {
  const panel = document.getElementById("vmfolders");
  if ((!panel || panel.hidden) && typeof openSettings === "function") openSettings("vmfolders");
  vmfEnsureSlot();
  vmfCloseCard();
  vmfBuildCard(path || "", mode, from);
}

function vmfEnsureSlot() {
  const body = document.getElementById("vmf-body");
  if (body && !document.getElementById("vmf-card-slot")) {
    const slot = vmfEl("div", "vmf-cardslot");
    slot.id = "vmf-card-slot";
    const main = vmfEl("div", "vmf-main");
    main.id = "vmf-main";
    body.append(slot, main);
  }
}


// ---- applying the list (M9.6 confirm, M9.7 progress, M9.8 failure) -----------------------------------------------------------

let vmfProg = null; // the painted card: { sig, elapsed, rights } so a poll tick only moves the numbers

function vmfProgSig(pm) {
  return [pm.head].concat(pm.steps.map((s) => [s.glyph, s.title, s.sub, s.red].join("|"))).join("\n");
}

function vmfStepEl(s) {
  const el = vmfEl("div", "empst-step vmf-step");
  const n = vmfEl("span", "empst-n");
  if (s.glyph === "spin") { const sp = vmfEl("span", "spin"); sp.setAttribute("aria-hidden", "true"); n.appendChild(sp); }
  else { n.textContent = s.glyph; if (s.glyph === "✓") n.classList.add("c-ok"); else if (s.glyph === "✕") n.classList.add("c-red"); }
  const right = vmfEl("span", "vmf-step-right t-md muted", s.right);
  el.append(n, vmfEl("b", "empst-t" + (s.red ? " c-red" : ""), s.title), right, vmfEl("small", "empst-d", s.sub));
  return { el, right };
}

// vmfProgressEl: the card for a running job (spinner, elapsed) or a failed one (red step, the buttons, Details and the terminal line).
function vmfProgressEl(pm, job) {
  const failed = job.state === "failed";
  const card = vmfEl("div", "vmf-fill vmf-prog");
  card.setAttribute("role", failed ? "alert" : "status");
  const head = vmfEl("div", "vmf-prog-head");
  if (!failed) { const sp = vmfEl("span", "spin"); sp.setAttribute("aria-hidden", "true"); head.appendChild(sp); }
  head.appendChild(vmfEl("span", "vmf-prog-title", pm.head));
  const prog = { sig: vmfProgSig(pm), elapsed: null, rights: [] };
  if (failed) {
    head.appendChild(vmfEl("span", "vmf-prog-spacer"));
    head.appendChild(vmfBtn("Copy details", "btn", () => vmfCopy(job.detail || job.error || "", "Details copied")));
    head.appendChild(vmfBtn("Try again", "btn btn-primary", () => vmfApplyOpen()));
  } else {
    prog.elapsed = vmfEl("span", "vmf-prog-elapsed t-md muted", pm.elapsed);
    head.appendChild(prog.elapsed);
  }
  const steps = vmfEl("div", "empst-steps vmf-steps");
  for (const s of pm.steps) {
    const e = vmfStepEl(s);
    prog.rights.push(e.right);
    steps.appendChild(e.el);
  }
  card.append(head, steps, vmfEl("p", "vmf-prog-foot t-md" + (failed ? "" : " muted"), pm.foot));
  if (failed) card.appendChild(vmfFailExtras(job));
  vmfProg = prog;
  return card;
}

function vmfFailExtras(job) {
  const wrap = vmfEl("div", "vmf-fail");
  const toggle = vmfBtn("", "vmf-details-toggle", () => {
    const open = toggle.getAttribute("aria-expanded") !== "true";
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
    chev.textContent = open ? "⌄" : "›";
    raw.hidden = !open;
  });
  toggle.setAttribute("aria-expanded", "false");
  const chev = vmfEl("span", "vmf-chev", "›");
  toggle.append(chev, vmfEl("span", "", "Details"), vmfEl("span", "muted", "The error as the VM reported it"));
  const raw = vmfEl("pre", "vmf-raw mono t-sm", job.detail || job.error || "");
  raw.hidden = true;
  wrap.append(toggle, raw, vmfEl("p", "t-md muted vmf-or", "Or try it from a terminal on this Mac:"));
  const cmd = vmfEl("div", "vmf-cmd");
  cmd.appendChild(vmfEl("code", "mono", VMA_CLI_TEXT));
  const copy = vmfBtn("Copy", "btn btn-ghost btn-sm", () => vmfCopy(VMA_CLI_TEXT, "Command copied"));
  copy.setAttribute("aria-label", "Copy the command");
  cmd.appendChild(copy);
  wrap.appendChild(cmd);
  return wrap;
}
const VMA_CLI_TEXT = "agent-box box share apply";

function vmfCopy(text, okMsg) {
  if (typeof copyText !== "function") return;
  copyText(text, (ok) => {
    if (typeof showToast === "function") showToast(ok ? okMsg : "Could not copy. Select the text and copy it.", { error: !ok });
  });
}

// vmfApplyChanged(prev, job): the global job moved (vmapply.js). Done after running reloads the view (the strip says Applied);
// otherwise the card is repainted, or only its numbers move when nothing but time changed.
function vmfApplyChanged(prev, job) {
  if (!vmfView || !document.getElementById("vmf-main")) return;
  vmfCardSync(); // an open add card waits while the VM restarts
  if (!job) vmfView.apply = null; // the job is gone: the view's old copy must not bring the card back
  if (job && job.state === "done") {
    if (prev && prev.state === "running") loadVMFolders();
    else vmfPaint();
    return;
  }
  const pm = job && typeof vmApplyProgressModel === "function" ? vmApplyProgressModel(job, Date.now()) : null;
  if (pm && vmfProg && vmfProg.sig === vmfProgSig(pm) && job.state === "running") {
    if (vmfProg.elapsed) vmfProg.elapsed.textContent = pm.elapsed;
    pm.steps.forEach((s, i) => { if (vmfProg.rights[i]) vmfProg.rights[i].textContent = s.right; });
    return;
  }
  vmfPaint();
}

// vmfApplyOpen: Apply and Try again. The confirm always opens, with the sessions running in boxes right now (the page's list
// may be old, so the view is fetched first); the restart starts on Restart box VM.
let vmfApplyAsking = false;
async function vmfApplyOpen() {
  if (vmfApplyAsking) return;
  vmfApplyAsking = true;
  try {
    try {
      vmfLoadSeq++;
      const v = await api("/api/vm-folders");
      vmfLoadSeq++;
      if (typeof vmaFromMeta === "function") vmaFromMeta(v.apply);
      vmfSetView(v);
    } catch (_) { /* the page's last view will do: the confirm opens with it */ }
    const j = vmfJob();
    if (j && j.state === "running") return;
    const c = vmApplyConfirmCopy((vmfView && vmfView.running_boxed) || []);
    const ok = await askConfirm({ title: c.title, body: c.body, notes: c.notes, confirmLabel: c.confirmLabel, danger: true, focusCancel: true, hint: c.hint, cls: "vmapply" });
    if (!ok) return;
    try {
      startVMApplyPoll(await api("/api/vm-folders/apply", { method: "POST" }));
    } catch (e) {
      if (e.code === "apply_running") { startVMApplyPoll(); return; } // another tab or the CLI started it: follow it
      if (typeof showToast === "function") showToast(e.message, { error: true });
    }
  } finally { vmfApplyAsking = false; }
}
globalThis.vmfApplyOpen = vmfApplyOpen;
globalThis.vmfApplyChanged = vmfApplyChanged;

if (typeof module !== "undefined" && module.exports) {
  module.exports = { vmfPhoneModel, vmfFootModel, vmfModel, vmfAddState, vmfCheckFrom, vmfRemoveCopy, vmfAddHelp, vmfNames, VMF_HELPER, VMF_HELP_DEFAULT, VMF_HELP_SPAWN };
}
