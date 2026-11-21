"use strict";

// Settings > Workspace roots (#343, spec 6.7): the folders CuliBox's Files, git and search can open. Home first and always, then each root
// a session start or this page added, with its dates and a Remove. The add card takes a typed or picked folder, asks the same check
// endpoint the New session form asks, shows the same line (wsCheckLine) and posts what that line promised: the repo top when the folder is
// inside a repo. Remove and Add save at once, and the answer of each is the whole list.
//
// wsrModel, wsrNoteParts, wsrRowNote and wsrAddState are pure (jstest/wsroots.test.js pins every word); the rest paints. Classic script: api, showToast,
// createWsPicker, wsCheckLine (wspicker.js), openSettings, openVMFoldersAdd, loadWorkspaceRoots and loadWorkspaces are other files'
// globals, read at call time.

const WSR_CORRUPT = "workspace-roots.json is damaged; CuliBox is using your home folder only until it is fixed or removed.";

// wsrDate: "Nov 21, 2026", in the browser's language (the Device tokens page's way of writing a day), "" for anything that is not a date.
function wsrDate(iso) {
  const d = new Date(iso);
  if (!iso || isNaN(d.getTime())) return "";
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

// wsrModel: GET /api/workspace-roots as rows (home first) and the damaged-list flag. A row without a path is dropped.
function wsrModel(resp) {
  const r = resp && typeof resp === "object" ? resp : {};
  const rows = [];
  for (const x of Array.isArray(r.roots) ? r.roots : []) {
    if (!x || typeof x.path !== "string" || !x.path) continue;
    const always = x.always === true;
    rows.push({
      path: x.path, label: x.path, always, available: x.available !== false,
      added: typeof x.added === "string" ? x.added : "", lastUsed: typeof x.last_used === "string" ? x.last_used : "",
      removable: !always,
    });
  }
  rows.sort((a, b) => (a.always === b.always ? 0 : a.always ? -1 : 1)); // stable: the server's order within each group
  return { rows, corrupt: r.corrupt === true };
}

// wsrNoteParts: the note beside a folder as the pieces that may wrap apart. Home is "always" (even when its folder is gone: it cannot be
// removed). A root that no longer resolves is "not available" and stays removable. Else the two dates, each with its label; a hand-edited
// row without dates still reads.
function wsrNoteParts(row) {
  if (row.always) return ["always"];
  if (!row.available) return ["not available"];
  const a = wsrDate(row.added), u = wsrDate(row.lastUsed);
  return ["added" + (a ? " " + a : ""), "last used " + (u || "never")];
}

// wsrRowNote: the same note as one string (the pieces joined by " · ").
function wsrRowNote(row) {
  return wsrNoteParts(row).join(" · ");
}

// wsrAddState: what the add card shows and does for one check answer (null: none yet). The line is the form's own (host tier: there is no
// session here to box); a folder a root already covers gets Settings' own line, since "nothing" would leave a dead button unexplained.
// Add waits for would_add and posts add_root_path, the root the line named.
function wsrAddState(check) {
  const none = { kind: "none", text: "", share: false };
  if (!check || typeof check !== "object") return { line: none, addDisabled: true, addPath: "" };
  const covered = check.exists && check.is_dir && check.root;
  const line = covered ? { kind: "info", text: "Already covered by " + check.root + ". Nothing to add.", share: false } : wsCheckLine(check, "host");
  const ok = check.would_add === true;
  return { line, addDisabled: !ok, addPath: ok ? check.add_root_path || check.path || "" : "" };
}

// ---- painting --------------------------------------------------------------------------------------------------------------

let wsrView = null;     // the last answer of GET /api/workspace-roots
let wsrCard = null;     // the open add card: { check, err, busy, picker, els }
let wsrBusy = false;    // a Remove is in flight
let wsrLoadSeq = 0;     // a GET that started before a save, or while it ran, never paints over its answer
let wsrPaintSeq = 0;    // the per-row share checks of an older paint are dropped

function wsrEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

function wsrBtn(label, cls, onclick) {
  const b = wsrEl("button", cls, label);
  b.type = "button";
  b.onclick = onclick;
  return b;
}

function wsrRole(el, role) {
  el.setAttribute("role", role);
  return el;
}

// The desktop page (pagehost.js sets as-page) or the phone's modal, asked at paint time as vmfolders.js does.
function wsrPhone() {
  const m = typeof document === "undefined" ? null : document.getElementById("settings-modal");
  return !m || !m.classList.contains("as-page");
}

function wsrEnsureSlot() {
  const body = document.getElementById("wsr-body");
  if (body && !document.getElementById("wsr-card-slot")) {
    const slot = wsrEl("div", "wsr-cardslot");
    slot.id = "wsr-card-slot";
    const main = wsrEl("div", "wsr-main");
    main.id = "wsr-main";
    body.append(slot, main);
  }
}

// wsrRefreshPage: the rest of the page learns the change: window.WORKSPACE_ROOTS (Files, the viewer's reveal) and the workspaces list.
function wsrRefreshPage() {
  if (typeof loadWorkspaceRoots === "function") loadWorkspaceRoots();
  if (typeof loadWorkspaces === "function") loadWorkspaces();
}

function wsrRowEl(row, cells) {
  const tr = wsrRole(wsrEl("tr", row.available ? "" : "wsr-gone"), "row");
  const td = (cls) => wsrRole(wsrEl("td", cls), "cell");
  const f = td("wsr-folder");
  const p = wsrEl("span", "mono", row.label);
  p.title = row.path;
  f.appendChild(p);
  const note = td("muted wsr-note");
  // One nowrap span per piece, so the note stays on one line where it fits and wraps at the separator where it does not.
  wsrNoteParts(row).forEach((t, i) => {
    if (i) note.appendChild(document.createTextNode(" · "));
    note.appendChild(wsrEl("span", "wsr-nb", t));
  });
  const act = td("num");
  if (row.removable) {
    const b = wsrBtn("Remove", "btn btn-ghost btn-sm", () => wsrRemove(row.path));
    b.setAttribute("aria-label", "Remove " + row.path);
    b.dataset.wsrAct = row.path;
    act.appendChild(b);
    cells.set(row.path, act);
  }
  tr.append(f, note, act);
  return tr;
}

function wsrTable(m, cells) {
  const table = wsrRole(wsrEl("table", "tbl wsr-tbl"), "table");
  table.setAttribute("aria-label", "Workspace roots");
  table.tabIndex = -1; // where focus lands when the row it was on is gone
  const cols = wsrEl("colgroup");
  for (let i = 0; i < 3; i++) cols.appendChild(wsrEl("col"));
  table.appendChild(cols);
  const head = wsrRole(wsrEl("tr"), "row");
  for (const h of ["Folder", "", ""]) {
    const th = wsrRole(wsrEl("th", h ? "" : "num", h), "columnheader");
    if (!h) th.appendChild(wsrEl("span", "sr-only", "Actions"));
    head.appendChild(th);
  }
  table.appendChild(wsrRole(wsrEl("thead"), "rowgroup")).appendChild(head);
  const body = wsrRole(wsrEl("tbody"), "rowgroup");
  for (const r of m.rows) body.appendChild(wsrRowEl(r, cells));
  table.appendChild(body);
  return table;
}

function wsrPaint() {
  const body = document.getElementById("wsr-body");
  if (!body || !wsrView) return;
  wsrEnsureSlot();
  const main = document.getElementById("wsr-main");
  const m = wsrModel(wsrView);
  const kids = [];
  if (m.corrupt) {
    const b = wsrEl("div", "banner wsr-banner");
    b.setAttribute("role", "status");
    b.appendChild(wsrEl("p", "t-md", WSR_CORRUPT));
    kids.push(b);
  }
  const cells = new Map();
  const card = wsrEl("div", "wsr-fill");
  const head = wsrEl("div", "card-head");
  head.append(wsrEl("span", "wsr-title", "Folders CuliBox can open"), wsrEl("span", "chip", String(m.rows.length)));
  card.append(head, wsrTable(m, cells));
  kids.push(card);
  main.replaceChildren(...kids);
  wsrSync();
  wsrShareOffers(m.rows, cells);
}

// The share offer on a Mac (spec 7): a root the box VM cannot see has no git status and no boxes, on either tier. Only a Mac has a VM
// to share with (the same flag as the VM folders page); each added folder that still exists is asked of the check endpoint, and one it
// says is unshared gets the button that opens the prefilled VM folders card (the share is the user's call, not a side effect of a click).
async function wsrShareOffers(rows, cells) {
  if (!globalThis.vmFoldersOn) return;
  const mine = ++wsrPaintSeq;
  await Promise.all(rows.filter((r) => r.removable && r.available).map(async (r) => {
    let c = null;
    try {
      const res = await fetch("/api/workspace/check?path=" + encodeURIComponent(r.path));
      if (res.ok) c = await res.json();
    } catch (_) { /* no answer: no button */ }
    const cell = cells.get(r.path);
    if (mine !== wsrPaintSeq || !c || !cell || !c.exists || !c.is_dir || c.vm_shared !== false) return;
    const b = wsrBtn("Share this folder…", "btn btn-sm", () => {
      if (typeof openVMFoldersAdd === "function") openVMFoldersAdd(r.path, "rw", "wsroots");
    });
    b.setAttribute("aria-label", "Share " + r.path + " with the box VM");
    b.dataset.wsrShare = r.path;
    cell.insertBefore(b, cell.firstChild);
  }));
}

// wsrSync: the header's Add folder (and the phone footer's) wait while the card is open and while the list is damaged.
function wsrSync() {
  const off = !!wsrCard || !!(wsrView && wsrView.corrupt === true);
  for (const id of ["set-act-wsroots", "wsr-foot-add"]) {
    const b = document.getElementById(id);
    if (!b) continue;
    b.disabled = off;
    if (id === "wsr-foot-add") b.onclick = () => openWsRootsAdd("");
  }
}

function wsrSetView(v) {
  wsrView = v;
  wsrPaint();
}

async function loadWsRoots() {
  const seq = ++wsrLoadSeq;
  let v;
  try { v = await api("/api/workspace-roots"); } catch (e) {
    if (seq !== wsrLoadSeq) return;
    wsrEnsureSlot();
    const main = document.getElementById("wsr-main");
    if (main && !wsrView) main.replaceChildren(wsrEl("p", "field-error", "Could not load the workspace roots: " + e.message));
    return;
  }
  if (seq !== wsrLoadSeq) return;
  wsrSetView(v);
}

// ---- Remove ----------------------------------------------------------------------------------------------------------------

async function wsrRemove(path) {
  if (wsrBusy) return;
  wsrBusy = true;
  wsrLoadSeq++;
  try {
    const v = await api("/api/workspace-roots", { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });
    wsrLoadSeq++;
    wsrSetView(v);
    wsrRefreshPage();
    const main = document.getElementById("wsr-main");
    const table = main ? main.querySelector("table") : null;
    if (table && typeof table.focus === "function") table.focus(); // the row it was on is gone
  } catch (e) {
    if (typeof showToast === "function") showToast(e.message, { error: true });
    loadWsRoots();
  } finally { wsrBusy = false; }
}

// ---- the add card ----------------------------------------------------------------------------------------------------------

function wsrCardSync() {
  const c = wsrCard;
  if (!c) return;
  const st = wsrAddState(c.check);
  const line = c.err ? { kind: "error", text: c.err, share: false } : st.line;
  const msg = c.els.msg;
  msg.className = line.kind === "error" ? "field-error" : "field-help ck-" + line.kind;
  msg.replaceChildren();
  if (line.text) msg.appendChild(wsrEl("span", "", line.text));
  if (line.share) {
    const path = (c.check && (c.check.add_root_path || c.check.path)) || "";
    msg.appendChild(wsrBtn("Share this folder…", "btn btn-sm wsr-share", () => {
      if (typeof openVMFoldersAdd === "function") openVMFoldersAdd(path, "rw", "wsroots");
    }));
  }
  if (line.kind === "error") msg.setAttribute("role", "alert"); else msg.removeAttribute("role");
  c.els.input.classList.toggle("is-invalid", line.kind === "error");
  c.els.input.setAttribute("aria-invalid", line.kind === "error" ? "true" : "false");
  c.els.add.disabled = st.addDisabled || c.busy === true || !!(wsrView && wsrView.corrupt === true);
}

async function wsrCardSubmit() {
  const c = wsrCard;
  if (!c || c.busy) return;
  const st = wsrAddState(c.check);
  if (st.addDisabled) return;
  c.busy = true;
  c.err = "";
  wsrCardSync();
  try {
    wsrLoadSeq++;
    const v = await api("/api/workspace-roots", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: st.addPath }) });
    wsrLoadSeq++;
    c.busy = false;
    wsrCloseCard();
    wsrSetView(v);
    wsrRefreshPage();
  } catch (e) {
    c.busy = false;
    c.err = e.message || "Could not add the folder.";
    wsrCardSync();
  }
}

function wsrCloseCard() {
  const c = wsrCard;
  if (!c) return;
  if (c.picker) c.picker.destroy();
  wsrCard = null;
  const slot = document.getElementById("wsr-card-slot");
  if (slot) slot.replaceChildren();
  wsrSync();
}

function wsrBuildCard(path) {
  const slot = document.getElementById("wsr-card-slot");
  if (!slot) return;
  const phone = wsrPhone();
  const card = wsrEl("div", "wsr-fill wsr-add");
  card.setAttribute("role", "group");
  card.setAttribute("aria-label", "Add a folder");
  const body = wsrEl("div", "wsr-add-body");
  body.appendChild(wsrEl("div", "wsr-add-lbl", "Add a folder"));
  const field = wsrEl("div", phone ? "fcol wsr-fieldbox" : "field wsr-fieldbox");
  const lab = wsrEl("label", phone ? "flab" : "field-label", "Folder");
  const input = wsrEl("input", phone ? "fld mono" : "input mono");
  input.id = "wsr-path-in";
  input.type = "text";
  input.setAttribute("autocomplete", "off");
  input.setAttribute("spellcheck", "false");
  input.setAttribute("role", "combobox");
  input.setAttribute("aria-expanded", "false");
  input.setAttribute("aria-autocomplete", "list");
  lab.setAttribute("for", input.id);
  const list = wsrEl("div", "wsr-wslist");
  list.id = "wsr-ws-list";
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Directories");
  list.hidden = true;
  input.setAttribute("aria-controls", list.id);
  const msg = wsrEl("span", "field-help");
  msg.id = "wsr-msg";
  input.setAttribute("aria-describedby", msg.id);
  const row = wsrEl("div", "wsr-inrow");
  row.appendChild(input);
  // The picker closes on any click outside the field and its list, this one included, so it opens once the click has finished. The
  // phone has no Browse…: its list is a fixed overlay that would sit on the message under the field, and a phone types a path.
  if (!phone) row.appendChild(wsrBtn("Browse…", "btn", () => { input.focus(); setTimeout(() => { if (wsrCard && wsrCard.picker) wsrCard.picker.open(); }, 0); }));
  field.append(lab, row, list, msg);
  body.appendChild(field);

  const foot = wsrEl("div", "wsr-add-foot");
  const add = wsrBtn("Add folder", "btn btn-primary", wsrCardSubmit);
  foot.append(wsrBtn("Cancel", "btn", wsrCloseCard), add);
  card.append(body, foot);
  slot.replaceChildren(card);

  wsrCard = { check: null, err: "", busy: false, picker: null, els: { input, msg, add } };
  if (typeof createWsPicker === "function") {
    wsrCard.picker = createWsPicker({
      input, list, keepEsc: true, reanchor: true,
      openOnFocus: () => false, openOnClick: () => false, openOnType: () => false, // on demand only: Browse… opens the list
      onPick: () => {},
      onNote: () => {},
      // The check of the folder in the field, or null while there is none (the field changed): the line and Add follow it.
      onCheck: (data) => { if (!wsrCard) return; wsrCard.check = data; wsrCard.err = ""; wsrCardSync(); },
    });
    wsrCard.picker.set(path || "");
  } else input.value = path || "";
  card.addEventListener("keydown", (e) => {
    const open = wsrCard && wsrCard.picker && wsrCard.picker.isOpen();
    if (e.key === "Enter" && e.target === input && !open) { e.preventDefault(); wsrCardSubmit(); }
    // The picker's own Esc handler (the field's, so it ran first) has already closed its list and prevented the default: that Esc
    // was the list's, and the card stays.
    else if (e.key === "Escape" && !open && !e.defaultPrevented) { e.stopPropagation(); wsrCloseCard(); }
  });
  wsrCardSync();
  wsrSync();
  input.focus();
}

// openWsRootsAdd(path): open the add card with `path` in the field (empty: blank). Opens the page first when it is not showing.
function openWsRootsAdd(path) {
  const panel = document.getElementById("wsroots");
  if ((!panel || panel.hidden) && typeof openSettings === "function") openSettings("wsroots");
  wsrEnsureSlot();
  wsrCloseCard();
  wsrBuildCard(path || "");
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { wsrModel, wsrNoteParts, wsrRowNote, wsrAddState, wsrDate, WSR_CORRUPT };
}
