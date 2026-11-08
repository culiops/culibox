"use strict";

// The workspace folder picker, one factory for every place a folder is chosen: the New
// session dialog's Workspace field and the profile page's Workspace block.
//
//   createWsPicker({ input, list, onPick, onNote, keepEsc, openOnFocus, reanchor, inline, lastUsed, idleText })
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
// opens does not want a dropdown over it). A click always opens the list; unset means yes.
// reanchor: also keep an open list under its field when something behind it scrolls. The list is
// position: fixed, so a page that scrolls under it (the profile page) needs this; New session never
// listened for scrolls and does not pass it.
// inline(): true when the list sits inside its field's own container, placed by the stylesheet (New session on the desktop:
// frame M4.3, a kit menu under the input, in the form's scroll box). It draws kit rows (.menu-item) and sets no top, left,
// width or max-height; false or unset keeps the fixed placement from the input's rect and the legacy .ft-opt rows (the
// phone, the profile page). lastUsed(): the folder the last session started in; only an inline list pins it (first row,
// trailing "Last used"), by reordering rows the server returned (pinLastUsed). idleText(count): the steady-state caption
// instead of spawnWsIdleNote's (New session names where the field's value came from).
//
// Classic script: the helpers above are dirpicker.js's, showToast is shell.js's and
// workspacesCache is app.js's, all read at call time.

const WSP_DEBOUNCE_MS = 120, WSP_FETCH_MS = 4000;
// Each picker's option ids start with its own number: aria-activedescendant must name ONE element on the page.
let wspSerial = 0;

function createWsPicker(opts) {
  const o = opts || {};
  const input = o.input, list = o.list;
  const onPick = typeof o.onPick === "function" ? o.onPick : () => {};
  const onNote = typeof o.onNote === "function" ? o.onNote : () => {};
  const uid = "wsp" + ++wspSerial;
  let shown = [], active = -1, seq = 0, timer = null, abort = null, cwd = "";
  let pointerGesture = false, gone = false;

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
  const pinned = (rows) => (inline() && typeof o.lastUsed === "function" ? pinLastUsed(rows, o.lastUsed()) : rows);
  const idleRows = () => pinned(pickerRows({ mode: "empty", workspaces: workspaces() }));
  const note = (text, idle) => onNote(text || "", idle === true);
  const idleNote = (count) => note(typeof o.idleText === "function" ? o.idleText(count) : spawnWsIdleNote(count), true);

  function value() { return cwd; }
  // set is the ONE place that writes the canonical value and the field's displayed text together (a
  // profile's stored folder, the last-spawn fallback, a pick). Live typing (below) updates the value
  // alone, since the input already shows what was typed.
  function set(path) {
    cwd = path || "";
    input.value = cwd;
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
  // it and choose() cannot pick it), appended LAST so move()'s list.children[active] lookup isn't shifted.
  function render(next, noteText) {
    shown = next;
    active = -1;
    drawnInline = inline();
    input.removeAttribute("aria-activedescendant");
    list.replaceChildren(...next.map((r, i) => {
      const el = document.createElement("div");
      el.className = drawnInline ? "menu-item" : "ft-opt";
      el.setAttribute("role", "option");
      el.setAttribute("aria-selected", "false");
      el.id = uid + "-opt-" + i;
      const lab = document.createElement("span");
      lab.textContent = r.label;
      const hint = document.createElement("span");
      // Not .hint inside a kit row: app.css's global .hint adds a 7px top margin and 11px type.
      hint.className = drawnInline ? "wsp-hint t-sm muted ellipsis" : "hint";
      hint.textContent = r.hint;
      if (drawnInline) lab.className = "menu-text mono t-sm ellipsis";
      // A kit row spaces its cells by gap: an empty hint (a project root has no parent to show) would add a dead 10px.
      el.append(lab);
      if (!drawnInline || r.hint) el.append(hint);
      if (drawnInline && r.lastUsed === true) {
        const tag = document.createElement("span");
        tag.className = "wsp-last t-sm muted";
        tag.textContent = "Last used";
        el.append(tag);
      }
      el.title = r.path;
      el.onclick = () => choose(i);
      return el;
    }));
    if (noteText) {
      const el = document.createElement("div");
      el.className = "ft-note";
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
    active = -1;
    input.removeAttribute("aria-activedescendant");
  }

  // open: what focusing or clicking the field does, derived from the text that is in there RIGHT NOW
  // (pickerReopenMode), so the list and the text never describe different searches.
  function open() {
    const mode = pickerReopenMode(input.value, !list.hidden);
    if (mode === "empty") {
      const r = idleRows();
      render(r);
      idleNote(r.length);
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
      idleNote(idleRows().length);
      return;
    }
    if (c.mode === "empty") {
      const r = idleRows();
      render(r);
      idleNote(r.length);
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
    const found = broke ? [] : pinned(pickerRows({ mode: "fuzzy", hits: data.results || [] }));
    const text = dirPickerNote(found.length, !!(data && data.truncated), broke);
    render(found, text);
    note(text, false);
  }

  function move(delta) {
    if (!shown.length) return;
    const prev = list.children[active];
    if (prev) prev.setAttribute("aria-selected", "false");
    active = pickerNextIndex(active, delta, shown.length);
    const el = list.children[active];
    if (el) {
      el.setAttribute("aria-selected", "true");
      el.scrollIntoView({ block: "nearest" });
      input.setAttribute("aria-activedescendant", el.id);
    }
  }

  function choose(i) {
    const r = shown[i];
    if (!r) return;
    close();
    set(r.path);
    idleNote(idleRows().length);
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
      idleNote(idleRows().length);
      onPick(cwd);
      return;
    }
    if (res.status === 400) {
      showToast("Not a directory: " + path, { error: true });
      return;
    }
    showToast(
      res.status === 404 ? "No such file or directory: " + path :
      res.status === 403 ? "That path is outside your home directory" :
      "Could not read " + path,
      { error: true });
  }

  // refresh: workspacesCache was refetched. Redraw the project roots, but only when nothing is actively
  // being searched, so a fuzzy query in flight is never clobbered by a background refresh.
  function refresh() {
    if (classifyPickerQuery(input.value).mode === "fuzzy") return;
    const r = idleRows();
    if (!list.hidden) render(r);
    idleNote(r.length);
  }

  function showIdleNote() {
    idleNote(idleRows().length);
  }

  function destroy() {
    if (gone) return;
    close();
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
    query(input.value);
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
