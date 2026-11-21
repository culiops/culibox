"use strict";

// The workspace folder picker, one factory for every place a folder is chosen: the New
// session dialog's Workspace field and the profile page's Workspace block.
//
//   createWsPicker({ input, list, onPick, onNote, onCheck, checkOn, otherPlaces, keepEsc, openOnFocus, openOnClick, openOnType, reanchor, inline, lastUsed, idleText })
//     -> { value, set, open, close, destroy, isOpen, highlighted, settle, refresh, showIdleNote }
//
// It is a combobox on an <input> and its listbox <div>: the same backend as dirpicker.js's
// file-tree control (GET /api/fs/search?kind=dir, the workspaces in workspacesCache), the
// same pure functions (classifyPickerQuery, pickerRows, pickerCollapseAt,
// pickerShouldSelectAll, pickerReopenMode, pickerNextIndex, pickerEnterAction,
// dirPickerNote, spawnWsIdleNote), the same debounce + AbortController + sequence-guard
// shape, and the same "note row appended LAST so index math holds". But its OWN state, all
// of it in the closure, not a second caller of initDirPicker.
//
// That control's dpInput/dpList/dpSeq/... are MODULE-GLOBAL and bound to #ft-root-input,
// with a measured mousedown->focus->click ordering and a documented boot-restore race
// (dirpicker.js). Routing a second field through that same state would mean the file tree
// and the field fight over one dpActive/dpRows/dpAbort: whichever opened last would silently
// steal the other's search. A factory over module globals would be the same trap under
// another prefix, so this has none: the page can hold a New session picker and a profile
// Workspace picker at once, each with its own rows, highlight, timers and value, and
// destroy() takes every listener one put on the document and the window away.
//
// value() is the actual, resolved candidate folder, deliberately NOT read back from
// input.value, for the same reason ftRootPath isn't (filetree.js): the input doubles as the
// picker's live search box, and only a successful pick, an absolute-path resolve, or a
// literal typed "/..." path updates it. Escape, an outside click, or a half-typed fuzzy
// query left sitting in .value must never itself become the folder: this picks where real
// agent sessions start and what a profile saves, unlike the file tree's read-only re-root.
//
// Callbacks. onPick(path): the value changed because the user picked, resolved or typed a
// path, or emptied the field (set() is the caller's own write and tells nobody). onNote(text,
// idle): the line under the field. idle is true for the steady-state caption ("N project
// roots · type to filter · ↵ to pick") and false for a problem the search has to report
// (text "" when there is none). keepEsc: an Esc that closes an open list is consumed
// (preventDefault), so a surrounding page's own Esc does not also fire; a dialog that closes
// on the same Esc, as New session's does, leaves it unset. openOnFocus(): asked when the field
// gains focus; false keeps the list closed (a form that focuses the field by itself when it
// opens does not want a dropdown over it). A click opens the list unless openOnClick() says no; unset means yes.
// reanchor: also keep an open list under its field when something behind it scrolls. The list is
// position: fixed, so a page that scrolls under it (the profile page) needs this; New session never
// listened for scrolls and does not pass it.
// inline(): true when the list sits inside its field's own container, placed by the stylesheet (New session on the desktop:
// frame M4.3, a kit menu under the input, in the form's scroll box). It draws kit rows (.menu-item) and sets no top, left,
// width or max-height; false or unset keeps the fixed placement from the input's rect and the legacy .ft-opt rows (the
// phone, the profile page); an inline list also starts a search with its first row active (the row Enter picks, drawn as
// the frame draws it). lastUsed(): the folder the last session started in; only an inline list pins it (first row,
// trailing "Last used"), by reordering rows the server returned (pinLastUsed). idleText(count): the steady-state caption
// instead of spawnWsIdleNote's (New session names where the field's value came from).
// filter(path): optional; false drops a row from every list (typed paths are not filtered).
// onCheck(check): optional. When set (and checkOn(), if given, says yes), the folder in the field is asked of
// GET /api/workspace/check (typed paths 250 ms after the last key; picks and set() at once) and onCheck gets the
// answer, or null while there is none (the field changed, is empty or relative, or the server could not say). The caller
// draws it with wsCheckLine below. A pure existence check on one path, so nothing is listed.
// otherPlaces(): optional; true shows an "Other places" group under the project roots of the idle list (the project folders
// GET /api/workspace-roots found outside home and every root, spec 6.8). They are read each time the idle list opens from
// closed and drawn when they arrive. A row is picked like any path (the value is set and the check line runs); nothing is
// added until Start. Typing searches the roots only, so the group is not in a search.
//
// Classic script: the helpers above are dirpicker.js's, showToast is shell.js's and
// workspacesCache is app.js's, all read at call time.

const WSP_DEBOUNCE_MS = 120, WSP_FETCH_MS = 4000, WSP_CHECK_MS = 250;

// wsCheckLine: the ONE line under the Workspace field from a /api/workspace/check answer (spec 6.4), and `tier`
// ("box" or "host"), the tier the session would run on. {kind: "none"|"info"|"warn"|"error", text, share}: share asks the
// caller for the "Share this folder…" button (the box VM cannot see add_root_path). In order:
//   missing -> error; not a directory -> error; refused -> warn (an amber line: the folder cannot become a root);
//   would_add -> info, naming the folder (add_root_path, the repo top when the folder is inside a repo);
//   then, when the VM cannot see the folder, the share sentence is added;
//   a folder already under a root says nothing, unless the box tier is chosen and the box would refuse it: warn.
// A covered folder the VM cannot see says nothing on the host tier: the host session never needs the VM's view.
function wsCheckLine(check, tier) {
  const none = { kind: "none", text: "", share: false };
  const c = check && typeof check === "object" ? check : null;
  if (!c) return none;
  const path = c.path || "";
  if (!c.exists) return { kind: "error", text: "No such file or directory: " + path, share: false };
  if (!c.is_dir) return { kind: "error", text: "Not a directory: " + path, share: false };
  const where = c.add_root_path || path;
  if (c.refused) {
    return {
      kind: "warn", share: false,
      text: "CuliBox won't open " + where + " in Files (" + c.refused + "). A host session can still run here; a box can't.",
    };
  }
  const boxBad = tier === "box" && !!c.box && c.box.ok === false;
  const unseen = c.vm_shared === false && (c.would_add === true || boxBad);
  let text = "";
  if (c.would_add) {
    text = "Starting here also lets CuliBox's Files, git and search open " + where + (where !== path ? " (the repo this folder is in)" : "") + ".";
  }
  if (unseen) {
    text += (text ? " " : "") + "The box VM can't see " + where + ", so git status and boxes won't work there.";
  } else if (boxBad) {
    const why = String((c.box && c.box.reason) || "").trim();
    if (why) text += (text ? " " : "") + why + (/[.!?]$/.test(why) ? "" : ".");
  }
  if (text === "") return none;
  return { kind: boxBad ? "warn" : "info", text, share: unseen };
}

// wsCheckBlock: why Start must wait for this answer, "" when it need not: the box tier is chosen and the box
// would refuse the folder (a Mac's VM cannot see it: share it, or switch to host). The server refuses it too; this says so first.
function wsCheckBlock(check, tier) {
  const c = check && typeof check === "object" ? check : null;
  if (!c || !c.exists || !c.is_dir || tier !== "box" || !c.box || c.box.ok !== false) return "";
  return wsCheckLine(c, tier).text;
}
// wsShownPath: how a row shows a folder's path (#343 slice 4): "~/rest" under home, otherwise the absolute path. Home itself is
// spelled out, under its "~ (home)" name. A sibling sharing home's prefix (/home/meX) is not under it.
function wsShownPath(path, home) {
  const p = String(path || "");
  const h = String(home || "").replace(/\/+$/, "");
  if (!h || p === h || p.indexOf(h + "/") !== 0) return p;
  return "~" + p.slice(h.length);
}

// The New session list's Recent group: the folders the last sessions started in, newest first, kept in this browser
// (localStorage, written by spawnSession). A convenience list like the recent profiles: a damaged value reads as empty.
const WS_RECENTS_MAX = 5;
const wsBare = (p) => (p.length > 1 ? p.replace(/\/+$/, "") || "/" : p);
// wsRecentsPush: `cwd` first, once (a trailing "/" ignored), at most WS_RECENTS_MAX. A relative or empty cwd changes nothing.
function wsRecentsPush(paths, cwd) {
  const list = Array.isArray(paths) ? paths.filter((x) => typeof x === "string" && x.charAt(0) === "/") : [];
  if (typeof cwd !== "string" || cwd.charAt(0) !== "/") return list.slice(0, WS_RECENTS_MAX);
  const want = wsBare(cwd);
  return [cwd].concat(list.filter((x) => wsBare(x) !== want)).slice(0, WS_RECENTS_MAX);
}
// wsRecentsRead: the stored {"v":1,"paths":[...]} as a list (absolute paths, deduped, at most WS_RECENTS_MAX). Nothing stored, or
// nothing readable: the last spawn's folder alone, so a browser that spawned before this list existed still opens on it.
function wsRecentsRead(raw, lastCwd) {
  let paths = [];
  try {
    const v = JSON.parse(raw || "null");
    if (v && v.v === 1 && Array.isArray(v.paths)) paths = v.paths;
  } catch (_) { /* damaged: empty */ }
  const out = [];
  for (const x of paths) {
    if (out.length >= WS_RECENTS_MAX) break;
    if (typeof x === "string" && x.charAt(0) === "/" && !out.some((y) => wsBare(y) === wsBare(x))) out.push(x);
  }
  if (out.length) return out;
  return typeof lastCwd === "string" && lastCwd.charAt(0) === "/" ? [lastCwd] : [];
}

// wsNameMatches: the rows whose name contains the query, case-insensitive; names that start with it first, otherwise in the
// given order. The added roots and "Other places" are matched this way in a search: the server's index holds a root's
// contents, never the root folder itself, and the other places are not indexed at all.
function wsNameMatches(rows, q) {
  const lq = String(q || "").trim().toLowerCase();
  if (!lq) return [];
  const starts = [], within = [];
  for (const r of Array.isArray(rows) ? rows : []) {
    const at = String(r.label || "").toLowerCase().indexOf(lq);
    if (at === 0) starts.push(r);
    else if (at > 0) within.push(r);
  }
  return starts.concat(within);
}

// Each picker's option ids start with its own number: aria-activedescendant must name ONE element on the page.
let wspSerial = 0;

function createWsPicker(opts) {
  const o = opts || {};
  const input = o.input, list = o.list;
  const onPick = typeof o.onPick === "function" ? o.onPick : () => {};
  const onNote = typeof o.onNote === "function" ? o.onNote : () => {};
  const uid = "wsp" + ++wspSerial;
  let shown = [], optEls = [], active = -1, seq = 0, timer = null, abort = null, cwd = "";
  // "Other places": the rows of the last GET /api/workspace-roots, and the read in flight (its own sequence).
  let others = [], othersSeq = 0;
  let pointerGesture = false, gone = false;
  // The folder check (onCheck): its own timer and sequence, so a pick that closes the list never cancels it.
  let checkTimer = null, checkSeq = 0;

  // Every listener goes through here so destroy() can take them all away.
  const bound = [];
  const on = (target, type, fn, capture) => {
    target.addEventListener(type, fn, capture);
    bound.push([target, type, fn, capture]);
  };

  const inline = () => typeof o.inline === "function" && o.inline() === true;
  // The row's layout, as drawn: a resize across the phone breakpoint must not leave one kind of row in the other's list.
  let drawnInline = false;
  const workspaces = () => (typeof workspacesCache !== "undefined" && Array.isArray(workspacesCache) ? workspacesCache : []);
  // Asked each time rows are drawn: a session started since the last open is seen.
  // o.filter(path): false drops a row (a Mac's New session lists only folders the box VM can see). Typed paths are not
  // filtered; the server refuses them.
  const keep = (rows) => (typeof o.filter === "function" ? rows.filter((r) => o.filter(r.path) !== false) : rows);
  const pinned = (rows) => (inline() && typeof o.lastUsed === "function" ? pinLastUsed(keep(rows), o.lastUsed()) : keep(rows));
  const wantOthers = () => typeof o.otherPlaces === "function" && o.otherPlaces() === true;
  // The "Other places" rows: the same row shape as a project root, the parent folder as the hint, the first row carrying the
  // heading. Like the project rows they pass keep(), and a folder the project rows already name is not listed twice.
  const otherAll = () => (wantOthers() ? keep(others.map((p) => ({
    label: p.label, hint: p.path.slice(0, p.path.lastIndexOf("/")) || "/", path: p.path, kind: "other",
  }))) : []);
  const otherRows = (have) => {
    const taken = new Set(have.map((r) => r.path));
    const rows = otherAll().filter((r) => !taken.has(r.path));
    if (rows.length) rows[0] = Object.assign({}, rows[0], { heading: "Other places" });
    return rows;
  };
  // paths() (#343 slice 4): two-line rows, the name with its path dimmed under it, plus the added workspace roots. New session on
  // the desktop and the profile page ask for it; the phone (frozen, #340) does not, and keeps its rows.
  const paths = () => typeof o.paths === "function" && o.paths() === true;
  const home = () => (typeof window !== "undefined" && typeof window.HOME_DIR === "string" ? window.HOME_DIR : "");
  const baseName = (p) => p.slice(p.replace(/\/+$/, "").lastIndexOf("/") + 1).replace(/\/+$/, "") || p;
  // The added roots (window.WORKSPACE_ROOTS: the roots the server serves now, home first), as rows: never home, never twice.
  const rootAll = () => {
    const roots = typeof window !== "undefined" && Array.isArray(window.WORKSPACE_ROOTS) ? window.WORKSPACE_ROOTS : [];
    const h = home(), seen = new Set();
    const rows = [];
    roots.forEach((p, i) => {
      if (typeof p !== "string" || p.charAt(0) !== "/" || i === 0 || p === h || seen.has(p)) return;
      seen.add(p);
      rows.push({ label: baseName(p), hint: "", path: p, kind: "root", tag: "Workspace root" });
    });
    return keep(rows);
  };
  // recents(): the folders the last sessions started in, newest first (New session on the desktop). The first carries "Last
  // used" when it is the last spawn's folder.
  const recentRows = () => {
    const list = typeof o.recents === "function" ? o.recents() : [];
    const h = home();
    const rows = keep((Array.isArray(list) ? list : []).filter((p) => typeof p === "string" && p.charAt(0) === "/").map((p) => ({
      label: p === h ? "~ (home)" : baseName(p), hint: "", path: p, kind: "recent",
    })));
    const last = typeof o.lastUsed === "function" ? o.lastUsed() : "";
    if (rows.length && typeof last === "string" && last !== "" && wsBare(rows[0].path) === wsBare(last)) rows[0].tag = "Last used";
    return rows;
  };
  const idleRows = () => {
    if (!paths()) {
      const top = pinned(pickerRows({ mode: "empty", workspaces: workspaces() }));
      return top.concat(otherRows(top));
    }
    const recent = recentRows();
    // With a Recent group the last folder is already first in it; the folders keep their own order.
    let folders = recent.length ? keep(pickerRows({ mode: "empty", workspaces: workspaces() })) : pinned(pickerRows({ mode: "empty", workspaces: workspaces() }));
    const listed = new Set(folders.map((r) => r.path));
    folders = folders.concat(rootAll().filter((r) => !listed.has(r.path)));
    if (recent.length) {
      recent[0] = Object.assign({}, recent[0], { heading: "Recent" });
      if (folders.length) folders[0] = Object.assign({}, folders[0], { heading: "Folders" });
    }
    return recent.concat(folders, otherRows(folders));
  };
  // The caption counts the folders and roots only: the recents repeat them and the other places are not roots.
  const rootCount = (rows) => rows.filter((r) => r.kind !== "other" && r.kind !== "recent").length;
  // The inline list's last line while idle: how to get past it (founder 2026-11-21: no Browse…, a typed path is the way).
  const footText = () => (paths() && inline() ? "Type to search · or a full path (/…)" : "");
  const renderIdle = (rows) => render(rows, footText(), "wsp-foot");
  const note = (text, idle) => onNote(text || "", idle === true);
  const idleNote = (count) => note(typeof o.idleText === "function" ? o.idleText(count) : spawnWsIdleNote(count), true);

  function value() { return cwd; }
  // set is the ONE place that writes the canonical value and the field's displayed text together (a
  // profile's stored folder, the last-spawn fallback, a pick). Live typing (below) updates the value
  // alone, since the input already shows what was typed.
  function set(path) {
    cwd = path || "";
    input.value = cwd;
    checkPath(cwd, 0);
  }

  const checking = () => typeof o.onCheck === "function" && (typeof o.checkOn !== "function" || o.checkOn() === true);
  function checkCancel() {
    if (checkTimer) { clearTimeout(checkTimer); checkTimer = null; }
    checkSeq++;
  }
  // checkPath: ask the server what Start in `path` would do, `delay` ms from now (0: now). The old answer goes at once: it
  // was about another folder. A relative or empty path has no answer. The newest ask wins.
  function checkPath(path, delay) {
    checkCancel();
    if (!checking()) return;
    o.onCheck(null);
    if (!path || path.charAt(0) !== "/") return;
    const mine = checkSeq;
    const run = async () => {
      checkTimer = null;
      let data = null;
      try {
        const res = await fetch("/api/workspace/check?path=" + encodeURIComponent(path));
        if (res.ok) data = await res.json();
      } catch (_) { /* no answer: no line */ }
      if (gone || mine !== checkSeq || !data) return;
      o.onCheck(data);
    };
    if (delay > 0) checkTimer = setTimeout(run, delay);
    else run();
  }

  function setOpen(show) {
    input.setAttribute("aria-expanded", show ? "true" : "false");
    if (show && inline()) {
      // Placed by the stylesheet under the field (absolute in its row, 8 rows tall at most): nothing inline, and
      // nothing left from a fixed-position open before a resize.
      for (const k of ["top", "left", "width", "bottom", "maxHeight"]) list.style[k] = "";
    } else if (show) {
      // The list is position: fixed, computed from the input's rect, not from an ancestor. A dialog body
      // or the page clips overflow, and fixed positioning is how the dropdown escapes that (see
      // #ft-root-list's identical comment).
      const r = input.getBoundingClientRect();
      const below = window.innerHeight - r.bottom;
      list.style.left = r.left + "px";
      list.style.width = r.width + "px";
      if (below < 120 && r.top > below) {
        list.style.top = "";
        list.style.bottom = (window.innerHeight - r.top) + "px";
        list.style.maxHeight = Math.max(80, r.top - 8) + "px";
      } else {
        list.style.bottom = "";
        list.style.top = r.bottom + "px";
        list.style.maxHeight = Math.max(80, below - 8) + "px";
      }
    }
    list.hidden = !show;
  }

  // `noteText`, when non-empty, renders one extra row that is NOT in `shown` (so move() cannot arrow onto
  // it and choose() cannot pick it), appended LAST. A row's `heading` draws a group heading before it; optEls (the option
  // elements, in `shown` order) is what setActive indexes, so neither shifts the index math.
  function render(next, noteText, noteClass) {
    shown = next;
    active = -1;
    drawnInline = inline();
    const drawnPaths = paths();
    input.removeAttribute("aria-activedescendant");
    const els = [];
    const nodes = [];
    next.forEach((r, i) => {
      if (r.heading) {
        // A group heading, not a row: it is not in `shown`, and optEls keeps the row index math.
        const head = document.createElement("div");
        head.className = "ft-note wsp-head";
        head.setAttribute("role", "presentation");
        head.textContent = r.heading;
        nodes.push(head);
      }
      const el = document.createElement("div");
      el.className = drawnInline ? "menu-item" : "ft-opt";
      el.setAttribute("role", "option");
      el.setAttribute("aria-selected", "false");
      el.id = uid + "-opt-" + i;
      // "Last used" wins over the row's own tag: in a search the pinned last folder may be a root or an other place.
      const tagText = r.lastUsed === true ? "Last used" : (r.tag || "");
      if (drawnPaths) {
        // Two lines (#343 slice 4): the name, and under it the path, dimmed (~/… under home).
        const two = document.createElement("div");
        two.className = "wsp-two";
        const name = document.createElement("span");
        name.className = drawnInline ? "menu-text mono t-sm ellipsis wsp-name" : "wsp-name";
        name.textContent = r.label;
        const where = document.createElement("span");
        where.className = drawnInline ? "wsp-path mono t-xs muted ellipsis" : "hint wsp-path muted";
        where.textContent = wsShownPath(r.path, home());
        two.append(name, where);
        el.append(two);
      } else {
        const lab = document.createElement("span");
        lab.textContent = r.label;
        const hint = document.createElement("span");
        // Not .hint inside a kit row: app.css's global .hint adds a 7px top margin and 11px type. The frame's parent folder is
        // machine text, `mono t-xs muted` (M4.3).
        hint.className = drawnInline ? "wsp-hint mono t-xs muted ellipsis" : "hint";
        hint.textContent = r.hint;
        if (drawnInline) lab.className = "menu-text mono t-sm ellipsis";
        // A kit row spaces its cells by gap: an empty hint (a project root has no parent to show) would add a dead 10px.
        el.append(lab);
        if (!drawnInline || r.hint) el.append(hint);
      }
      if (tagText && (drawnInline || drawnPaths)) {
        const tag = document.createElement("span");
        tag.className = drawnInline ? "wsp-last t-sm muted" : "hint wsp-last";
        tag.textContent = tagText;
        el.append(tag);
      }
      el.title = r.path;
      el.onclick = () => choose(i);
      els.push(el);
      nodes.push(el);
    });
    optEls = els;
    list.replaceChildren(...nodes);
    if (noteText) {
      const el = document.createElement("div");
      el.className = noteClass ? "ft-note " + noteClass : "ft-note";
      el.setAttribute("role", "option");
      el.setAttribute("aria-disabled", "true");
      el.textContent = noteText;
      list.append(el);
    }
    setOpen(next.length > 0 || !!noteText);
  }

  function close() {
    setOpen(false);
    if (timer) { clearTimeout(timer); timer = null; }
    if (abort) { abort.abort(); abort = null; }
    seq++;
    shown = [];
    optEls = [];
    active = -1;
    input.removeAttribute("aria-activedescendant");
  }

  // open: what focusing or clicking the field does, derived from the text that is in there RIGHT NOW
  // (pickerReopenMode), so the list and the text never describe different searches.
  function open() {
    const mode = pickerReopenMode(input.value, !list.hidden);
    if (mode === "empty") {
      const was = list.hidden;
      const r = idleRows();
      renderIdle(r);
      idleNote(rootCount(r));
      if (was) loadOthers();
    } else if (mode === "search") query(input.value);
    // "none": the open list already shows this search: see pickerReopenMode.
  }

  function query(raw) {
    const c = classifyPickerQuery(raw);
    const mine = ++seq;
    if (timer) { clearTimeout(timer); timer = null; }
    if (abort) { abort.abort(); abort = null; }
    if (c.mode === "path") {
      // Only the server can say whether this is a real directory (Enter asks it, see resolvePath), but the
      // caption still reads as steady-state, not as an active search, matching pickerReopenMode's own
      // path/empty split.
      close();
      idleNote(rootCount(idleRows()));
      return;
    }
    if (c.mode === "empty") {
      const r = idleRows();
      renderIdle(r);
      idleNote(rootCount(r));
      return;
    }
    timer = setTimeout(() => search(c.value, mine), WSP_DEBOUNCE_MS);
  }

  async function search(q, mine) {
    const ac = new AbortController();
    abort = ac;
    const deadline = setTimeout(() => ac.abort(), WSP_FETCH_MS);
    let data = null, failed = false;
    try {
      const res = await fetch("/api/fs/search?kind=dir&limit=40&q=" + encodeURIComponent(q), { signal: ac.signal });
      if (res.ok) data = await res.json();
      else failed = true;
    } catch (_) {
      // Offline, or the WSP_FETCH_MS timeout fired. An abort from query() or close() also lands here, but
      // both bump seq first, so the guard below returns before this ever reaches the screen as a failure.
      failed = true;
    } finally { clearTimeout(deadline); }
    if (mine !== seq) return; // a newer query already won
    abort = null;
    const broke = failed || !data;
    let hits = broke ? [] : pickerRows({ mode: "fuzzy", hits: data.results || [] });
    if (paths()) {
      // The added roots and the other places, by name, before the index's hits; each path once.
      const named = wsNameMatches(rootAll().concat(otherAll().map((r) => Object.assign({}, r, { tag: "Other place" }))), q);
      const have = new Set(named.map((r) => r.path));
      hits = named.concat(hits.filter((r) => !have.has(r.path)));
    }
    const found = pinned(hits);
    const text = dirPickerNote(found.length, !!(data && data.truncated), broke);
    // The inline list says how many it found when nothing is wrong; the field's note keeps dirPickerNote's words.
    const shownText = text || (paths() && inline() && found.length ? found.length + (found.length === 1 ? " match" : " matches") + " · ↵ to pick" : "");
    render(found, shownText, text ? "" : "wsp-foot");
    // Inline (New session on the desktop, frame M4.3): the first hit is drawn highlighted, as the row Enter takes anyway (enter()
    // picks row 0 of a fuzzy list when none is active). The idle list, the phone's and the profile page's start with none.
    if (drawnInline && found.length) setActive(0, false);
    note(text, false);
  }

  // setActive: the one place a row becomes the active one (the arrows, and an inline search's first row): aria-selected on the
  // row, aria-activedescendant on the field. `reveal` scrolls it into view: the arrows ask, the first row does not (the list was
  // just drawn from the top, and typing must not scroll the form behind it).
  function setActive(i, reveal) {
    const prev = optEls[active];
    if (prev) prev.setAttribute("aria-selected", "false");
    active = i;
    const el = optEls[active];
    if (el) {
      el.setAttribute("aria-selected", "true");
      if (reveal) el.scrollIntoView({ block: "nearest" });
      input.setAttribute("aria-activedescendant", el.id);
    }
  }

  function move(delta) {
    if (!shown.length) return;
    setActive(pickerNextIndex(active, delta, shown.length), true);
  }

  function choose(i) {
    const r = shown[i];
    if (!r) return;
    close();
    set(r.path);
    idleNote(rootCount(idleRows()));
    onPick(cwd);
  }

  function onKeydown(e) {
    if (e.key === "ArrowDown") { e.preventDefault(); move(1); return; }
    if (e.key === "ArrowUp") { e.preventDefault(); move(-1); return; }
    if (e.key === "Escape") {
      const was = !list.hidden;
      close();
      if (o.keepEsc === true && was) e.preventDefault();
      return;
    }
    if (e.key !== "Enter") return;
    // Ctrl/Cmd+Enter is the form's spawn hotkey (initSpawnControl); its submit handler asks
    // spawnHotkeyAction and settles the field itself (settle): handling it here too would act twice.
    if (e.ctrlKey || e.metaKey) return;
    // Prevented unconditionally: the field may live inside a form, and Enter's default action there is
    // submitting it: the caption's own "↵ to pick" promise would be a lie if Enter here silently
    // submitted instead of resolving the field.
    e.preventDefault();
    enter();
  }
  // enter: what Enter means in the field: pick the highlighted row, else resolve a typed path or take the
  // first row.
  function enter() {
    if (active >= 0) { choose(active); return; }
    const c = classifyPickerQuery(input.value);
    const act = pickerEnterAction(c.mode, shown.length);
    if (act === "resolve") resolvePath(c.value);
    else if (act === "first") choose(0);
  }
  // settle: a submit found the field showing something other than the folder in use (spawnHotkeyAction).
  // Do what Enter does; when that settles nothing (a Spawn click closed the list, so a fuzzy query has no
  // rows to take), put the user back in the field with its matches open instead of going on.
  function settle() {
    enter();
    if (classifyPickerQuery(input.value).mode === "path") return; // resolving
    if (input.value.trim() === cwd.trim()) return;
    note("Pick a workspace first", false);
    if (document.activeElement !== input) input.focus(); // its focus handler reopens the list
    else open();
  }

  // A typed absolute path is resolved by /api/fs/list; see dpResolvePath's identical five-outcome breakdown
  // in dirpicker.js. The one difference: on a non-directory (400) there is nothing to fall through to
  // opening as a file (dpOnOpenFile), because a workspace is a directory or it is an error.
  async function resolvePath(path) {
    let res;
    try {
      res = await fetch("/api/fs/list?path=" + encodeURIComponent(path));
    } catch (_) {
      showToast("Could not reach the server", { error: true });
      return;
    }
    if (gone) return;
    if (res.ok) {
      let data = null;
      try { data = await res.json(); } catch (_) {}
      if (gone) return;
      close();
      set((data && data.path) || path);
      idleNote(rootCount(idleRows()));
      onPick(cwd);
      return;
    }
    if (res.status === 400) {
      showToast("Not a directory: " + path, { error: true });
      return;
    }
    // 403: outside every workspace root. Not an error any more: Start adds the folder as a root, and the check
    // line under the field says so (or why not). The typed path is already the value.
    if (res.status === 403) {
      close();
      checkPath(path, 0);
      return;
    }
    showToast(
      res.status === 404 ? "No such file or directory: " + path :
      "Could not read " + path,
      { error: true });
  }

  // refresh: workspacesCache was refetched. Redraw the project roots, but only when nothing is actively
  // being searched, so a fuzzy query in flight is never clobbered by a background refresh.
  function refresh() {
    if (classifyPickerQuery(input.value).mode === "fuzzy") return;
    const r = idleRows();
    if (!list.hidden) renderIdle(r);
    idleNote(rootCount(r));
  }

  // loadOthers: read the other places (the server caches its scan, so this is cheap) and redraw the idle list when they
  // arrive, keeping the row the arrows had reached. A failed read, or one for a picker that is gone or for an older open,
  // changes nothing.
  async function loadOthers() {
    if (!wantOthers()) return;
    const mine = ++othersSeq;
    let data = null;
    try {
      const res = await fetch("/api/workspace-roots");
      if (res.ok) data = await res.json();
    } catch (_) { /* no group */ }
    if (gone || mine !== othersSeq || !data || !Array.isArray(data.elsewhere)) return;
    others = data.elsewhere.filter((p) => p && typeof p.path === "string" && p.path.charAt(0) === "/" && typeof p.label === "string" && p.label !== "");
    if (list.hidden) return;
    const was = active;
    const at = was >= 0 && shown[was] ? shown[was].path : "";
    refresh();
    // The same row if it is still there (Recent and Folders can both hold a path), else the first row with that path.
    const back = !at ? -1 : (shown[was] && shown[was].path === at ? was : shown.findIndex((r) => r.path === at));
    if (back >= 0) setActive(back, false);
  }

  function showIdleNote() {
    idleNote(rootCount(idleRows()));
  }

  function destroy() {
    if (gone) return;
    close();
    checkCancel();
    othersSeq++;
    gone = true;
    for (const [target, type, fn, capture] of bound.splice(0)) target.removeEventListener(type, fn, capture);
  }

  on(input, "input", () => {
    // A typed absolute path is a valid folder candidate immediately: no pick or Enter required, exactly
    // like the old #cwd-custom field it replaces. A fuzzy query is NOT: a live search string must never
    // itself become the folder, so the value is left at whatever was last resolved or picked.
    const c = classifyPickerQuery(input.value);
    const before = cwd;
    if (c.mode === "path") cwd = c.value;
    else if (c.mode === "empty") cwd = "";
    if (typeof o.openOnType === "function" && !o.openOnType()) close(); // an on-demand list closes as the user types: it never covers the field message
    else query(input.value);
    // A typed path is asked about after a pause; a fuzzy query leaves the value, and so its line, alone.
    if (c.mode === "path") checkPath(c.value, WSP_CHECK_MS);
    else if (c.mode === "empty") checkPath("", 0);
    if (cwd !== before) onPick(cwd);
  });
  // Mirrors dirpicker.js's mousedown/focus/click choreography verbatim (same pure functions, same measured
  // ordering) so a click into an already-filled field selects the whole path instead of splicing into it:
  // the exact bug that control spent five rounds closing.
  on(input, "mousedown", (e) => {
    if (e.button !== 0) return;
    pointerGesture = true;
    const at = pickerCollapseAt(input.selectionStart, input.selectionEnd);
    if (at !== null) input.setSelectionRange(at, at);
  });
  on(document, "mouseup", () => { pointerGesture = false; });
  on(input, "focus", () => {
    const pointer = pointerGesture;
    pointerGesture = false;
    if (!pointer) input.select();
    if (typeof o.openOnFocus !== "function" || o.openOnFocus()) open();
  });
  on(input, "click", () => {
    if (pickerShouldSelectAll(input.value, input.selectionStart === input.selectionEnd)) input.select();
    if (typeof o.openOnClick === "function" && !o.openOnClick()) return; // an on-demand list (VM folders): only Browse… opens it
    open();
  });
  on(input, "keydown", onKeydown);
  // Tabbing on from the field (or from the scrollable list) closes the list, so it never sits over the next
  // field. A pointer press on a non-focusable row moves focus to an ANCESTOR of the list (Chrome: the
  // <dialog>), never to a field, and closing then would empty the rows before the click picks one; the click
  // handlers below own pointer dismissal. Tab never lands on a container, so skipping ancestors exempts only
  // pointer presses.
  const focusOut = (e) => {
    const to = e.relatedTarget;
    if (to && to !== input && !list.contains(to) && !to.contains(list)) close();
  };
  on(input, "focusout", focusOut);
  on(list, "focusout", focusOut);
  on(document, "click", (e) => {
    if (e.target !== input && !list.contains(e.target)) close();
  });
  // An open list stays under its field when the window resizes, and, when asked (reanchor), when something
  // behind it scrolls. The list's own scrolling moves nothing.
  on(window, "resize", () => {
    if (list.hidden) return;
    // Rows drawn for the other layout: close (reopening draws them for this one) rather than leave legacy rows in a kit menu.
    if (inline() !== drawnInline) close();
    else setOpen(true);
  });
  if (o.reanchor === true) on(document, "scroll", (e) => { if (!list.hidden && !list.contains(e.target)) setOpen(true); }, true);

  return { value, set, open, close, destroy, isOpen: () => !list.hidden, highlighted: () => active >= 0, settle, refresh, showIdleNote };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { createWsPicker, wsCheckLine, wsCheckBlock, wsShownPath, wsRecentsRead, wsRecentsPush, wsNameMatches };
}
