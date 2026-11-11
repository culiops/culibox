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

// dpSkipWorkspaceNames mirrors skipDirRecord (fssearch.go) — the directories
// the dir index refuses to record at all, regardless of what marker file sits
// inside them or whether they qualify under Rule 1 (see dirPickerNote) as a
// direct child of $HOME. listWorkspaces (workspaces.go) was NOT given the same
// filter: it also feeds the New-session working-dir form, which is out of
// scope here (that form's own regression trap is around spawning outside
// $HOME). So without filtering here, the picker contradicts itself — open it
// with a blank query and "code-server" or "sdk" is on screen; type either name
// and dirPickerNote says the search found nothing, because the index that
// powers the fuzzy search never recorded them. For "sdk" that would be a
// regression against the picker's behaviour before Task 1.
//
// This is skipDirRecord's FULL key set, including entries Task 1 did not add
// ("vendor" predates it) — not just the ones introduced alongside the project-
// root rule. A literal $HOME/vendor would otherwise reproduce the identical
// self-contradiction "sdk" had: suppressed from the search index, still
// offered here. Keep this a superset of skipDirRecord, not a hand-picked
// subset someone has to re-derive from the commit that touched it.
//
// Hidden entries (leading ".") never reach here at all — listWorkspaces itself
// skips them — but they are kept in this set anyway so it mirrors
// skipDirRecord's keys directly rather than a manually-trimmed copy.
const dpSkipWorkspaceNames = new Set([
  "vendor", "code-server", "sdk", ".zinit", ".tmux", ".open-next", ".worktrees", "worktrees",
]);

// pickerRows turns state into the rows to render. Basename in the label column
// and parent in the hint column: showing the whole relative path in both
// truncated the part that identifies the directory, which is the lesson
// palette.js already records for files.
function pickerRows(state) {
  const s = state || {};
  if (s.mode === "empty") {
    return (s.workspaces || [])
      .filter((w) => !dpSkipWorkspaceNames.has(w.label))
      .map((w) => ({
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

// pinLastUsed puts the folder the last session started in first, marked `lastUsed`, in a list the server returned
// (frame M4.3: "Last used" trails the first row of the New session list). It only REORDERS: a folder the list does
// not hold (deleted, a scratch path under /tmp, not a project root, past the search's limit) adds no row, and the rows
// come back untouched. Paths match as whole paths, a trailing "/" on either side ignored. The matched row is a
// copy; the input is never changed. No match, or no last folder: the same array.
function pinLastUsed(rows, lastCwd) {
  const list = Array.isArray(rows) ? rows : [];
  const bare = (p) => (typeof p === "string" && p.length > 1 ? p.replace(/\/+$/, "") || "/" : p);
  const want = bare(lastCwd);
  if (typeof want !== "string" || want === "") return list;
  const at = list.findIndex((r) => bare(r.path) === want);
  if (at < 0) return list;
  return [Object.assign({}, list[at], { lastUsed: true })].concat(list.slice(0, at), list.slice(at + 1));
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
//     field already holding "/home/user/culipulse" and typing splices into
//     it — "/home/user/culXXXXXipulse" — because a click into an
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
// Measured, not assumed. The decisive measurement is Probe 1 of the browser
// pass at 4a3bd92 — a capture-phase trace of one drag over an already-fully-
// selected field, which recorded mousedown at offsets 0,50 (the full selection,
// untouched), then mouseup at 16,16 and click at 16,16. So the drag extended
// NOTHING: the browser collapsed the selection to a caret at the release point
// and reported a plain click. (The earlier pass at df786d8 saw only the
// symptom — "the drag selects nothing" — not which event the selection
// survived to.) That trace was taken with no script touching the selection
// during the gesture, which is what rules out "our select() broke it": the
// field merely being fully selected at mousedown is enough on its own. It is
// also documented Chrome/Firefox behaviour — a mousedown inside a selection
// initiates a text-DRAG of that selection rather than a new selection, which is
// precisely why the gesture cannot extend anything.
//
// And the field is fully selected in steady state, because that is what makes
// typing replace a path instead of splicing into it — so every first drag after
// a click was landing in exactly that trap.
//
// Collapsing here removes the precondition, and the browser's own caret
// placement (which runs later in the same default action) then puts the caret
// where the user actually clicked.
//
// Returns the caret index to collapse to, or null if there is nothing in the
// way. It collapses to the END, not the start, because the end is the one
// choice that CANNOT scroll the box between the physical mousedown and the
// browser's hit-test: setSelectionRange(end, end) leaves the caret where
// select() already left it, whereas collapsing to 0 asks the field to jump to
// its head, and a field that scrolls under a pointer mid-gesture lands the
// caret on a different character than the one clicked.
//
// Note what was NOT observed: the browser pass measured scrollLeft = 0
// throughout, on a 50-character value in a ~242px box — so in the one
// environment measured the field did not auto-scroll to its tail after select()
// and the hazard above was never reproduced. The choice stands because it is
// free and cannot make things worse, not because the scroll was seen.
function pickerCollapseAt(start, end) {
  return start === end ? null : end;
}

// pickerNextIndex wraps the arrow-key highlight. `from` is -1 whenever nothing
// is highlighted, which is the state EVERY fresh render leaves — so -1 is the
// common case here, not an edge one.
//
// It has to be special-cased. The naive (from + delta + n) % n reads -1 as a
// real position and sends the first ArrowUp to (n - 2), the second-to-last row,
// skipping the last one entirely. ArrowDown got away with it by arithmetic
// accident: (-1 + 1 + n) % n is 0, the answer we wanted anyway.
function pickerNextIndex(from, delta, n) {
  if (n <= 0) return -1;
  if (from < 0) return delta > 0 ? 0 : n - 1;
  return (from + delta + n) % n;
}

// pickerEnterAction decides what Enter does when NO row is highlighted. (With a
// highlight the answer is never in doubt: take that row.)
//
//   "resolve" → a typed absolute path; only the server can say what it is.
//   "first"   → the top fuzzy hit. This is a defensible guess ONLY in fuzzy
//               mode, where row 0 is the best match for what was typed.
//   "none"    → do nothing. In "empty" mode the rows are the workspace list,
//               which the user never searched for: taking row 0 there re-roots
//               at home the instant Enter is pressed on a blank box, with
//               nothing visibly highlighted to explain why the tree moved.
function pickerEnterAction(mode, rowCount) {
  if (mode === "path") return "resolve";
  if (mode === "fuzzy" && rowCount > 0) return "first";
  return "none";
}

// dirPickerNote is the picker's answer to "why is this list not a list of
// folders?". Without it, no-matches / HTTP-error / offline all rendered
// identically — the dropdown simply vanished — and the user could not tell "no
// such folder" from "search is broken".
//
// It is not a hypothetical: the dir index admits only two kinds of directory
// (fssearch.go markerFiles / Rule 1) — a PROJECT ROOT (one holding
// .git/go.mod/package.json/Cargo.toml/pyproject.toml, anywhere under $HOME),
// or a direct, non-hidden CHILD OF $HOME regardless of what it contains
// (mirroring listWorkspaces) — which took the offered set from 10,049
// directories to 91 on a real home. So typing the name of a real, VISIBLE
// directory that is neither — two levels deep and not a project, e.g.
// ~/culipulse/docs/notes — is now the NORMAL way to land on zero rows, and
// "no matching folders" reads as a broken search rather than as that rule.
// The zero-row copy has to name BOTH admitted kinds, not just projects — an
// earlier draft said "only project folders are listed", which is false for
// ~/bin or ~/Downloads and claims a narrower index than Rule 1 actually
// builds — plus the escape hatch (a full path reaches anything else).
// `truncated` is the same signal palette.js already surfaces for files, and
// the server has been computing it for directories all along — though on a
// real home it measures false (the file-cap bug that used to abort the
// directory walk early is fixed), so it is trustworthy: zero rows means
// "not admitted by either rule", not "the index gave up".
function dirPickerNote(count, truncated, failed) {
  if (failed) return "folder search unavailable";
  if (!count) return "projects and top-level folders — type a full path";
  return truncated ? "index truncated: some folders are not listed" : "";
}

// spawnWsIdleNote is the spawn dialog's own steady-state caption (D5): "N
// project roots · type to filter · ↵ to pick", shown under its workspace field
// whenever there is nothing wrong to report (see dirPickerNote, which only
// speaks up for the zero-hit/truncated/failed cases a live search can hit).
// The file-tree picker has no room for an always-on caption and needs none —
// the tree itself shows what is rooted — but the spawn dialog's wider layout
// has space to say what the field does before a single keystroke.
//
// `count` is meant to be pickerRows({mode:"empty", workspaces}).length — the
// same dpSkipWorkspaceNames-filtered set the picker itself renders when
// opened blank — so the number on screen always matches what the dropdown
// shows if it's opened right now, rather than a raw, unfiltered tally.
function spawnWsIdleNote(count) {
  const n = count || 0;
  return n + (n === 1 ? " project root" : " project roots") + " · type to filter · ↵ to pick";
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
  // produced a click at all. Belt-and-braces only — the focus handler below is
  // what actually guarantees the flag cannot outlive one gesture.
  document.addEventListener("mouseup", () => { dpPointerGesture = false; });
  dpInput.addEventListener("focus", () => {
    // Read AND clear, because the flag has exactly one consumer: this line.
    // Leaving the clearing to the document mouseup alone stranded it — a press
    // released outside the window delivers no mouseup, so the flag stayed true
    // and the NEXT focus (a Tab, which has no click coming to finish the job)
    // skipped select(); typing then spliced into the path, which is the exact
    // defect this control spent five rounds closing. Nothing else reads the
    // flag, so consuming it here is complete.
    const pointer = dpPointerGesture;
    dpPointerGesture = false;
    if (!pointer) dpInput.select();
    dpReopen();
  });
  dpInput.addEventListener("click", () => {
    if (pickerShouldSelectAll(dpInput.value, dpInput.selectionStart === dpInput.selectionEnd)) {
      dpInput.select();
    }
    dpReopen();
  });
  dpInput.addEventListener("keydown", dpKeydown);
  // Desktop: Tab (or Shift+Tab) out of the field closes the list, so it never stays over the tree whose row just took focus
  // (GATE3 finding 1: with real Tab presses it stayed open, covering the rows). The rule is wspicker.js's: only focus moving
  // to a focusable element that is neither the field, nor the list, nor an ancestor of the list closes it. A pointer press on
  // a row (a plain div) lands on nothing (relatedTarget null) or on a container around the list, and the click picks the row.
  // The phone keeps its behaviour: Escape, a pick or an outside click.
  const dpFocusOut = (e) => {
    if (!(typeof drawerDesktop === "function" && drawerDesktop())) return;
    const to = e.relatedTarget;
    if (to && to !== dpInput && !dpList.contains(to) && !to.contains(dpList)) dpClose();
  };
  dpInput.addEventListener("focusout", dpFocusOut);
  dpList.addEventListener("focusout", dpFocusOut);
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
  let data = null, failed = false;
  try {
    const res = await fetch("/api/fs/search?kind=dir&limit=40&q=" + encodeURIComponent(q), { signal: ac.signal });
    if (res.ok) data = await res.json();
    else failed = true;
  } catch (_) {
    // Offline, or the DP_FETCH_MS timeout fired. An abort from dpQuery or
    // dpClose also lands here, but both bump dpSeq first, so the guard below
    // returns before this ever reaches the screen as a failure.
    failed = true;
  } finally { clearTimeout(timer); }
  if (seq !== dpSeq) return;  // a newer query already won
  dpAbort = null;
  const broke = failed || !data;
  const rows = broke ? [] : pickerRows({ mode: "fuzzy", hits: data.results || [] });
  dpRender(rows, dirPickerNote(rows.length, !!(data && data.truncated), broke));
}

// `note`, when non-empty, renders one extra row that is NOT in dpRows — so
// dpMove cannot arrow onto it and dpChoose cannot pick it (see dirPickerNote).
// It is appended LAST for a reason: dpMove reaches its element by
// dpList.children[dpActive], so anything ahead of the rows would shift every
// index by one.
function dpRender(rows, note) {
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
  if (note) {
    const el = document.createElement("div");
    el.className = "ft-note";
    // role="option" + aria-disabled keeps the listbox's children all options
    // (a bare <div> in a listbox is invalid ARIA) while still telling assistive
    // tech this one cannot be chosen. Deliberately no id: aria-activedescendant
    // is only ever set from dpMove, so it can never point here.
    el.setAttribute("role", "option");
    el.setAttribute("aria-disabled", "true");
    el.textContent = note;
    dpList.append(el);
  }
  dpOpen(rows.length > 0 || !!note);
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
  // Callable before initDirPicker has run. syncTreeToRoot calls this, and
  // tabs.js's syncFocusedTree can reach syncTreeToRoot during initFileTree's
  // `await api("/api/workspaces")` — ftRootSel is already assigned by then but
  // initDirPicker is not, which is the boot-restore race filetree.js documents
  // where it re-asserts the root. There is nothing open to close in that
  // window, so returning is the whole correct behaviour; without it dpOpen
  // throws on a null dpInput and takes the restore down with it.
  if (!dpInput || !dpList) return;
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
  dpActive = pickerNextIndex(dpActive, delta, dpRows.length);
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
  const act = pickerEnterAction(c.mode, dpRows.length);
  if (act === "resolve") dpResolvePath(c.value);
  else if (act === "first") dpChoose(0);
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
    pickerNextIndex, pickerEnterAction, dirPickerNote, spawnWsIdleNote, pinLastUsed,
  };
}
