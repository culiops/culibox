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

// Everything below touches the DOM and is covered by the behavioural pass, not
// node --test. It mirrors palette.js:167-200's debounce + AbortController +
// sequence guard rather than inventing a second version of the same pattern.

const DP_DEBOUNCE_MS = 120, DP_FETCH_MS = 4000;
let dpInput = null, dpList = null, dpWorkspaces = [], dpRows = [], dpActive = -1;
let dpSeq = 0, dpTimer = null, dpAbort = null, dpOnPick = null, dpOnOpenFile = null;

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
  // setFtRoot writes the picked path into the input, so in steady state
  // .value always starts with "/" — dpQuery on that classifies as "path" and
  // closes immediately, making the workspace list unreachable on a re-focus.
  // Rendering it here covers Tab/keyboard focus outright: the browser already
  // select-alls a text input's value on keyboard focus, so nothing else is
  // needed for that path.
  dpInput.addEventListener("focus", () => {
    dpRender(pickerRows({ mode: "empty", workspaces: dpWorkspaces }));
  });
  // Mouse selection is handled on click, not focus: on a click, caret
  // placement (or, for a drag, a range selection) is part of mousedown's
  // default action and runs AFTER focus is dispatched — a select() in the
  // focus handler gets overwritten by it every time, which is what forced the
  // previous fix into a setTimeout(0) workaround. click fires after that
  // default action finishes, so by the time this runs the final selection is
  // already known:
  //   - a plain click leaves a COLLAPSED selection at the caret — treated as
  //     "start a new pick", so select() replaces it with select-all.
  //   - a drag leaves a RANGE selection — left alone, so a substring of an
  //     already-picked path can be edited in place (a collapsed-only select()
  //     would otherwise clobber every drag into select-all, DOM-verified).
  // click also fires on a second click into an ALREADY-focused field, where
  // no focus event fires at all — this is what makes clicking back into the
  // input right after an Enter-driven pick (list already closed, nothing else
  // pending) reopen it; typing from that state would otherwise insert at the
  // caret into the old path instead of replacing it.
  //
  // Guarded on the CURRENT value's mode, not unconditional: while the user is
  // mid-typing a fuzzy query — the list already open on real search hits — a
  // click is for repositioning the caret to fix a typo, not a restart, and
  // must not select-all-and-reset the list out from under them.
  // classifyPickerQuery is the same pure function every other consumer uses
  // to answer exactly that question, so the gate is one call, not new state.
  dpInput.addEventListener("click", () => {
    if (classifyPickerQuery(dpInput.value).mode === "fuzzy") return;
    if (dpInput.selectionStart === dpInput.selectionEnd) dpInput.select();
    dpRender(pickerRows({ mode: "empty", workspaces: dpWorkspaces }));
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
  module.exports = { classifyPickerQuery, pickerRows };
}
