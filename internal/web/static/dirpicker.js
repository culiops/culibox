"use strict";

// One control for "which directory is the file tree rooted at". It replaces a
// <select> that could only offer $HOME's direct children and a text input that,
// despite its placeholder, opened FILES and never re-rooted anything.
//
// The pure functions below are exported for node --test; everything that
// touches the DOM lives beneath them and is covered by the behavioural pass.

// classifyPickerQuery decides which of three worlds the typed text is in. It is
// the single place that decision is made, because every consumer downstream —
// what to fetch, what to render, what Enter does — branches on it.
function classifyPickerQuery(q) {
  const v = String(q == null ? "" : q).trim();
  if (!v) return { mode: "empty", value: "" };
  // Only a leading slash. "~/x" is deliberately NOT treated as a path: the
  // index is relative to home already, so it fuzzy-matches fine, and expanding
  // it here would need HOME_DIR and make this function impure.
  if (v[0] === "/") return { mode: "path", value: v };
  return { mode: "fuzzy", value: v };
}

// pickerRows turns state into the rows to render. Basename in the label column
// and parent in the hint column: showing the whole relative path in both
// truncated the part that identifies the directory, which is the lesson
// palette.js already records for files.
function pickerRows(state) {
  const s = state || {};
  if (s.mode === "empty") {
    return (s.workspaces || []).map((w) => ({
      label: w.label, hint: "", path: w.path, kind: "workspace",
    }));
  }
  if (s.mode === "fuzzy") {
    return (s.hits || []).map((h) => {
      const cut = h.rel.lastIndexOf("/");
      return {
        label: cut >= 0 ? h.rel.slice(cut + 1) : h.rel,
        hint: cut >= 0 ? h.rel.slice(0, cut) : "",
        path: h.path,
        kind: "hit",
      };
    });
  }
  // "path" mode renders nothing: only the server can say whether that string is
  // a directory to root at or a file to open, and it is asked on Enter.
  return [];
}

// pickerReopenMode answers the question both focus and click on the input ask:
// "given the text that is in there RIGHT NOW, what should the list show?"
//
// Deriving it from the value is the whole point. A handler that unconditionally
// renders the workspace list leaves the list disagreeing with the text beside
// it — type "culi", click away, click back, and the workspaces reappear under a
// query that still reads "culi"; Enter then has no highlight, classifies "culi"
// as fuzzy rather than a path, falls through to "pick row 0" and re-roots at
// whichever workspace happens to be first. The list and the value must never be
// allowed to describe different searches.
//
//   "empty"  → render the workspace list. Covers a blank value AND a settled
//              absolute path, which is the steady state: setFtRoot mirrors the
//              picked root into the input, so re-focusing it must still offer
//              the workspaces rather than re-deciding on a string that is
//              already the answer.
//   "search" → re-run the fuzzy search, because the list is not showing it.
//   "none"   → the open list already IS this search. Leave it: a click to fix a
//              typo mid-query must not refetch, re-create the rows, or drop the
//              arrow-key highlight the user just moved.
function pickerReopenMode(value, listOpen) {
  if (classifyPickerQuery(value).mode !== "fuzzy") return "empty";
  return listOpen ? "none" : "search";
}

// pickerShouldSelectAll answers the separate question a CLICK asks of the TEXT:
// "is this click starting a new pick, or editing what is already there?"
//
//   collapsed caret ⇒ a plain click ⇒ start over, so select all and let the
//     next keystroke replace the value. Without this, clicking back into a
//     field already holding "/home/claude/culipulse" and typing splices into
//     it — "/home/claude/culXXXXXipulse" — because a click into an
//     already-focused input fires no focus event at all.
//   a range ⇒ a drag ⇒ the user picked a substring on purpose; leave it, or no
//     part of a path is ever editable in place.
//   a fuzzy value ⇒ a live search the user is refining; a click is for moving
//     the caret, and select-all would throw the query away on the next key.
function pickerShouldSelectAll(value, collapsed) {
  return collapsed && classifyPickerQuery(value).mode !== "fuzzy";
}

// pickerCollapseAt answers the question a pointer gesture has to ask BEFORE the
// browser gets to interpret it: "is there a selection in the way?"
//
// Measured, not assumed (browser pass at df786d8): a mousedown that lands
// INSIDE an existing selection is not read as "start selecting here" at all —
// the browser treats it as grabbing the selected text, so no caret is placed,
// mousemove extends nothing, and the selection comes out of the gesture exactly
// as it went in. That was observed with no script touching the selection during
// the gesture, which is what rules out "our select() broke it" as the real
// cause: the field merely being fully selected at mousedown is enough on its
// own. And the field is fully selected in steady state, because that is what
// makes typing replace a path instead of splicing into it — so every first drag
// after a click was landing in exactly that trap.
//
// Collapsing here removes the precondition, and the browser's own caret
// placement (which runs later in the same default action) then puts the caret
// where the user actually clicked.
//
// Returns the caret index to collapse to, or null if there is nothing in the
// way. It collapses to the END, not the start: after select() an overflowing
// field is scrolled to show the tail, so the end is already on screen and
// moving the caret there scrolls nothing. Collapsing to 0 would scroll the
// field under the pointer between the physical mousedown and the browser's
// hit-test, which can land the caret on a different character than the one
// clicked.
function pickerCollapseAt(start, end) {
  return start === end ? null : end;
}

// Everything below touches the DOM and is covered by the behavioural pass, not
// node --test. It mirrors palette.js:167-200's debounce + AbortController +
// sequence guard rather than inventing a second version of the same pattern.

const DP_DEBOUNCE_MS = 120, DP_FETCH_MS = 4000;
let dpInput = null, dpList = null, dpWorkspaces = [], dpRows = [], dpActive = -1;
let dpSeq = 0, dpTimer = null, dpAbort = null, dpOnPick = null, dpOnOpenFile = null;
// True from a primary-button mousedown on the input until the mouseup that ends
// that gesture. It exists to tell the focus handler which kind of focus it is
// looking at, since the focus event itself cannot say.
let dpPointerGesture = false;

function initDirPicker(opts) {
  dpInput = document.getElementById("ft-root-input");
  dpList = document.getElementById("ft-root-list");
  if (!dpInput || !dpList) return;
  dpOnPick = (opts && opts.onPick) || function () {};
  dpOnOpenFile = (opts && opts.onOpenFile) || function () {};
  // filetree.js already awaits /api/workspaces for HOME_DIR, so the caller
  // can just hand the result over. The un-awaited fetch is only a fallback
  // for a caller that doesn't have the list yet — otherwise a focus before it
  // lands would render zero rows with no retry.
  if (opts && opts.workspaces) dpWorkspaces = opts.workspaces;
  else api("/api/workspaces").then((ws) => { dpWorkspaces = ws || []; }).catch(() => {});
  dpInput.addEventListener("input", () => dpQuery(dpInput.value));
  // Two jobs across three events. The ordering they rely on was MEASURED in a
  // browser, not reasoned about: mousedown fires first, then focus, then the
  // browser's own caret placement, then mouseup, then click.
  //
  // REOPENING the list is dpReopen's, and runs on focus AND click,
  // unconditionally. It is derived from the value, so the list can never
  // contradict the text. It is deliberately outside every guard below: Escape
  // closes the list WITHOUT blurring, so no focus event follows and a click is
  // the only way back in — gating this made that click do visibly nothing.
  //
  // SELECTING the text has to serve two demands that pull against each other:
  //   A. after a plain click, the whole value must be selected, so typing
  //      replaces the path instead of splicing into it.
  //   B. a drag must be able to carve out a substring, so a path can be edited
  //      in place.
  // They conflict because A leaves the field fully selected, and a mousedown
  // inside a full selection is read as grabbing the text rather than as the
  // start of a new one (see pickerCollapseAt) — so A's end state poisons B's
  // next gesture. Resolved by acting at both ends of the gesture:
  //   - mousedown clears the selection out of the way, so the browser is free
  //     to place a caret and extend from it. This is what makes a drag work,
  //     and it is needed whether or not the gesture also changes focus — the
  //     failure was reproduced on an already-focused field, where no focus
  //     event fires at all.
  //   - focus selects all ONLY when no pointer is involved. Keyboard (Tab) and
  //     programmatic focus have no click coming to finish the job, so this is
  //     their only chance; doing it for a pointer would immediately re-fill the
  //     selection that mousedown just cleared and put demand B back in the trap.
  //     This is an explicit mechanism rather than a bet on the browser
  //     select-alling on sequential focus navigation: if that bet were wrong,
  //     Tab-then-type would splice into the path like the click bug did.
  //   - click is the first moment a pointer's FINAL selection is knowable, so
  //     it decides between A and B by reading it: collapsed ⇒ a plain click ⇒
  //     select all; a range ⇒ a drag ⇒ leave the substring alone. It also fires
  //     on a click into an ALREADY-focused field, where focus does not.
  dpInput.addEventListener("mousedown", (e) => {
    // Primary button only: a right-click must keep the selection it was aimed
    // at (so "Copy" copies it) and never claims the gesture flag.
    if (e.button !== 0) return;
    dpPointerGesture = true;
    const at = pickerCollapseAt(dpInput.selectionStart, dpInput.selectionEnd);
    if (at !== null) dpInput.setSelectionRange(at, at);
  });
  // On document, not the input: a mousedown that starts on the input can be
  // released anywhere, and that release still has to end the gesture. Firing
  // for every mouseup also clears a flag left behind by a gesture that never
  // produced a click at all.
  document.addEventListener("mouseup", () => { dpPointerGesture = false; });
  dpInput.addEventListener("focus", () => {
    if (!dpPointerGesture) dpInput.select();
    dpReopen();
  });
  dpInput.addEventListener("click", () => {
    if (pickerShouldSelectAll(dpInput.value, dpInput.selectionStart === dpInput.selectionEnd)) {
      dpInput.select();
    }
    dpReopen();
  });
  dpInput.addEventListener("keydown", dpKeydown);
  document.addEventListener("click", (e) => {
    if (e.target !== dpInput && !dpList.contains(e.target)) dpClose();
  });
  // dpOpen(true) snapshots the input's rect once, on open. A mobile on-screen
  // keyboard (raised by the very focus that opens the list) shrinks
  // innerHeight after that snapshot, as does an ordinary window resize or
  // crossing the 700px breakpoint — none of those fire a click, so nothing
  // else re-runs the placement. A splitter drag needs no listener here: it
  // ends in a click outside, which already closes the list via dpClose().
  window.addEventListener("resize", () => { if (!dpList.hidden) dpOpen(true); });
}

// dpReopen is the only thing focus and click do to the LIST. Both events route
// through it so there is exactly one answer to "what should be on screen for
// this value", rather than one answer per event that can drift apart.
function dpReopen() {
  const mode = pickerReopenMode(dpInput.value, !dpList.hidden);
  if (mode === "empty") dpRender(pickerRows({ mode: "empty", workspaces: dpWorkspaces }));
  else if (mode === "search") dpQuery(dpInput.value);
  // "none": the open list already shows this search — see pickerReopenMode.
}

function dpQuery(raw) {
  const c = classifyPickerQuery(raw);
  const seq = ++dpSeq;
  if (dpTimer) { clearTimeout(dpTimer); dpTimer = null; }
  if (dpAbort) { dpAbort.abort(); dpAbort = null; }
  if (c.mode === "path") { dpClose(); return; }   // Enter asks the server
  if (c.mode === "empty") { dpRender(pickerRows({ mode: "empty", workspaces: dpWorkspaces })); return; }
  dpTimer = setTimeout(() => dpSearch(c.value, seq), DP_DEBOUNCE_MS);
}

async function dpSearch(q, seq) {
  const ac = new AbortController();
  dpAbort = ac;
  const timer = setTimeout(() => ac.abort(), DP_FETCH_MS);
  let data = null;
  try {
    const res = await fetch("/api/fs/search?kind=dir&limit=40&q=" + encodeURIComponent(q), { signal: ac.signal });
    if (res.ok) data = await res.json();
  } catch (_) { /* aborted or offline: fall through to the empty render */ }
  finally { clearTimeout(timer); }
  if (seq !== dpSeq) return;  // a newer query already won
  dpAbort = null;
  dpRender(pickerRows({ mode: "fuzzy", hits: (data && data.results) || [] }));
}

function dpRender(rows) {
  dpRows = rows; dpActive = -1;
  // A shorter or longer result set recycles ids: leaving a stale
  // aria-activedescendant would either point at nothing or, worse, at a row
  // the user never highlighted.
  dpInput.removeAttribute("aria-activedescendant");
  dpList.replaceChildren(...rows.map((r, i) => {
    const el = document.createElement("div");
    el.className = "ft-opt";
    el.setAttribute("role", "option");
    el.setAttribute("aria-selected", "false");
    el.id = "ft-opt-" + i;
    const lab = document.createElement("span");
    lab.textContent = r.label;
    const hint = document.createElement("span");
    hint.className = "hint";
    hint.textContent = r.hint;
    el.append(lab, hint);
    el.title = r.path;
    el.onclick = () => dpChoose(i);
    return el;
  }));
  dpOpen(rows.length > 0);
}

function dpOpen(show) {
  dpInput.setAttribute("aria-expanded", show ? "true" : "false");
  if (show) {
    // #ft-root-list is position: fixed (see index.html), so its box is
    // computed from the viewport, not from .ft-pick — it has to be told where
    // the input actually is. Recomputed on every call, not cached: the resize
    // listener above re-runs this exact code when the viewport changes under
    // an already-open list.
    const r = dpInput.getBoundingClientRect();
    const below = window.innerHeight - r.bottom;
    dpList.style.left = r.left + "px";
    dpList.style.width = r.width + "px";
    if (below < 120 && r.top > below) {
      // Flip above when there is more room there: without this, an input near
      // the bottom of the viewport clamps maxHeight to a sliver while `top`
      // stays at r.bottom, forcing the list off-screen below the fold instead
      // of merely short.
      dpList.style.top = "";
      dpList.style.bottom = (window.innerHeight - r.top) + "px";
      dpList.style.maxHeight = Math.max(80, r.top - 8) + "px";
    } else {
      dpList.style.bottom = "";
      dpList.style.top = r.bottom + "px";
      dpList.style.maxHeight = Math.max(80, below - 8) + "px";
    }
  }
  dpList.hidden = !show;
}

function dpClose() {
  dpOpen(false);
  // Escape and an outside click both come straight here, bypassing dpQuery's
  // own cancel-on-new-query logic — without this a debounced search or an
  // in-flight fetch from the abandoned query lands after the fact and
  // reopens a list the user just dismissed.
  if (dpTimer) { clearTimeout(dpTimer); dpTimer = null; }
  if (dpAbort) { dpAbort.abort(); dpAbort = null; }
  dpSeq++;
  // Also clears the highlight so a stale dpActive from a dismissed fuzzy
  // search cannot win a later Enter that was meant for a freshly typed path.
  dpRows = []; dpActive = -1;
  dpInput.removeAttribute("aria-activedescendant");
}

function dpMove(delta) {
  if (!dpRows.length) return;
  const prev = dpList.children[dpActive];
  if (prev) prev.setAttribute("aria-selected", "false");
  dpActive = (dpActive + delta + dpRows.length) % dpRows.length;
  const el = dpList.children[dpActive];
  if (el) {
    el.setAttribute("aria-selected", "true");
    el.scrollIntoView({ block: "nearest" });
    dpInput.setAttribute("aria-activedescendant", el.id);
  }
}

function dpChoose(i) {
  const r = dpRows[i];
  if (!r) return;
  dpClose();
  dpInput.value = "";
  dpOnPick(r.path);
}

function dpKeydown(e) {
  if (e.key === "ArrowDown") { e.preventDefault(); dpMove(1); return; }
  if (e.key === "ArrowUp") { e.preventDefault(); dpMove(-1); return; }
  if (e.key === "Escape") { dpClose(); return; }
  if (e.key !== "Enter") return;
  e.preventDefault();
  if (dpActive >= 0) { dpChoose(dpActive); return; }
  const c = classifyPickerQuery(dpInput.value);
  if (c.mode === "path") dpResolvePath(c.value);
  else if (dpRows.length) dpChoose(0);
}

// A typed absolute path is resolved by /api/fs/list, which already answers this
// exactly — no new endpoint. Five outcomes, and each gets its own answer,
// because a control that does nothing on a typo reads as broken:
//
//   200 → a directory. Re-root at data.path, the RESOLVED path, never the typed
//         string: the root is compared as a STRING by relativeTo() for "Copy
//         relative path" and by shouldRefreshTree() for upload, so a typed path
//         that crosses a symlink would leave the root disagreeing with every
//         path the tree lists.
//   404 → no such path.
//   403 → outside $HOME. The tree is rooted inside $HOME by construction, so
//         neither re-rooting nor listing is available.
//   500 → unreadable (a symlink loop, or a directory that stats fine but cannot
//         be read).
//   400 → not a directory. Fall through to opening it as a file, which is what
//         the input this replaces always did.
async function dpResolvePath(path) {
  let res;
  try {
    res = await fetch("/api/fs/list?path=" + encodeURIComponent(path));
  } catch (_) {
    showToast("Could not reach the server", { error: true });
    return;
  }
  if (res.ok) {
    let data = null;
    try { data = await res.json(); } catch (_) {}
    dpClose();
    dpInput.value = "";
    dpOnPick((data && data.path) || path);
    return;
  }
  if (res.status === 400) { dpClose(); dpInput.value = ""; dpOnOpenFile(path); return; }
  showToast(
    res.status === 404 ? "No such file or directory: " + path :
    res.status === 403 ? "That path is outside your home directory" :
    "Could not read " + path,
    { error: true });
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    classifyPickerQuery, pickerRows,
    pickerReopenMode, pickerShouldSelectAll, pickerCollapseAt,
  };
}
