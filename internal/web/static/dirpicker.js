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
  api("/api/workspaces").then((ws) => { dpWorkspaces = ws || []; }).catch(() => {});
  dpInput.addEventListener("input", () => dpQuery(dpInput.value));
  dpInput.addEventListener("focus", () => dpQuery(dpInput.value));
  dpInput.addEventListener("keydown", dpKeydown);
  document.addEventListener("click", (e) => {
    if (e.target !== dpInput && !dpList.contains(e.target)) dpClose();
  });
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
  dpList.hidden = !show;
  dpInput.setAttribute("aria-expanded", show ? "true" : "false");
}
function dpClose() { dpOpen(false); }

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
