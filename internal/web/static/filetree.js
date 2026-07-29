"use strict";

// File tree: lazy directory browser rooted at a workspace. Clicking a file opens
// it in the focused cell (openFile). A paste-path input opens any absolute path.
// The root auto-follows the focused session's cwd (see syncTreeToSession), while
// a manual pick from the dropdown survives until a *different* session is focused.

// Nerd Font glyphs for the tree. Every codepoint here was rendered and looked at
// before being added; an absent codepoint subsets to nothing and ships a tofu
// box that no test can see. Adding one means re-running the probe in
// scripts/build-font-subset.sh's header, then regenerating the subset.
//
// EXACT wins over EXT because ".gitignore" has no extension in the ordinary
// sense — splitting on the last dot would call it "gitignore".
// Object.create(null): a plain {} inherits from Object.prototype, so a file or
// directory literally named "constructor" or "toString" would look up a real
// method instead of undefined, and iconFor would hand the DOM a function or an
// object to stringify into the icon column. Do not "simplify" this back to {}.
const ICON_EXACT = Object.assign(Object.create(null), {
  ".gitignore": "", ".gitattributes": "", ".gitmodules": "",
});
const ICON_EXT = Object.assign(Object.create(null), {
  go: "",
  js: "", mjs: "", cjs: "",
  md: "",
  json: "",
  html: "", htm: "",
  css: "",
  png: "", jpg: "", jpeg: "", gif: "", webp: "", svg: "",
  sh: "", bash: "", zsh: "",
  yml: "", yaml: "", toml: "", ini: "", conf: "",
  lock: "", sum: "",
});
const ICON_DIR = "", ICON_DIR_OPEN = "", ICON_FILE = "";

// iconFor picks one glyph for a tree row. Directories report their open state so
// the icon agrees with the ▸/▾ next to it.
function iconFor(name, isDir, open) {
  if (isDir) return open ? ICON_DIR_OPEN : ICON_DIR;
  const lower = String(name || "").toLowerCase();
  if (ICON_EXACT[lower]) return ICON_EXACT[lower];
  const cut = lower.lastIndexOf(".");
  // cut <= 0 covers both "Makefile" (no dot) and ".env" (leading dot only).
  if (cut <= 0) return ICON_FILE;
  return ICON_EXT[lower.slice(cut + 1)] || ICON_FILE;
}

let ftRootSel = null, ftTreeEl = null;
let lastSyncedSessionId = null; // which session the tree is currently following
let ftGen = 0;                  // bumped per root load; a superseded load must not paint

async function ftRenderDir(path, container, depth) {
  let data;
  try { data = await api("/api/fs/list?path=" + encodeURIComponent(path)); }
  catch (e) { const d = document.createElement("div"); d.className = "ft-err"; d.textContent = e.message; container.append(d); return; }
  for (const ent of data.entries) {
    const full = path.replace(/\/$/, "") + "/" + ent.name;
    const row = document.createElement("div");
    row.className = "ft-node";
    row.style.paddingLeft = (depth * 12 + 6) + "px";
    // The label lives in its own span so the row can also carry a download
    // button: writing row.textContent would wipe any child element.
    // The icon is its own element, not part of the label's text: the label's
    // textContent is rewritten on every directory toggle, and a glyph baked into
    // that string would have to be re-derived there too.
    const icon = document.createElement("span");
    icon.className = "ft-icon";
    icon.textContent = iconFor(ent.name, ent.is_dir, false);
    icon.setAttribute("aria-hidden", "true"); // decorative: the name is the label
    const label = document.createElement("span");
    label.className = "ft-name";
    label.textContent = (ent.is_dir ? "▸ " : "  ") + ent.name;
    // The title goes on the LABEL, not the row: the ⋯ button carries its own
    // title and must keep it. The full path rather than the bare name, because
    // it also answers "which of the three index.html files is this one".
    label.title = shortPath(full);
    row.append(icon, label);
    // One affordance per row. Download used to sit here as a bare ⤓ and Rename /
    // Delete behind the menu; two controls squeezed the name column (badly at
    // 390px) and put a destructive action a finger-width from a harmless one.
    // Download costs a second click now, and in exchange the tarball's
    // symlink-skip finally reads as a visible label instead of a tooltip no
    // touch device can reach.
    const more = document.createElement("button");
    more.className = "ft-more";
    more.textContent = "⋯";
    more.title = "Actions";
    more.onclick = (e) => { e.stopPropagation(); openRowMenu(more, full, ent.name, ent.is_dir); };
    row.append(more);
    if (ent.is_dir) {
      let open = false, childBox = null;
      row.classList.add("dir");
      row.onclick = async () => {
        open = !open;
        label.textContent = (open ? "▾ " : "▸ ") + ent.name;
        icon.textContent = iconFor(ent.name, true, open);
        if (open) {
          childBox = document.createElement("div");
          row.after(childBox);
          await ftRenderDir(full, childBox, depth + 1);
        } else if (childBox) { childBox.remove(); childBox = null; }
      };
      // Each directory row is its own drop target and stops propagation, so it
      // wins over the whole-tree zone underneath it.
      if (typeof makeDropZone === "function") {
        makeDropZone(row, () => full, {
          onDone: () => {
            if (!open || !childBox) return;
            childBox.replaceChildren();
            ftRenderDir(full, childBox, depth + 1);
          },
        });
      }
    } else {
      row.classList.add("file");
      row.onclick = () => openFile(full);
    }
    container.append(row);
  }
}

// ftLoadRoot renders off-DOM and swaps in only if it is still the newest load.
// Two roots can be in flight at once: boot loads home while a session's cwd
// arrives and re-roots, and ftRenderDir appends after its own await, so painting
// straight into the tree let a superseded load leave its rows behind (the tree
// showed home while the picker said the session's directory).
async function ftLoadRoot() {
  if (!ftRootSel || !ftTreeEl) return;
  const gen = ++ftGen;
  const box = document.createElement("div");
  await ftRenderDir(ftRootSel.value, box, 0);
  if (gen !== ftGen) return; // a newer root started meanwhile: drop this render
  ftTreeEl.replaceChildren(...box.childNodes);
}

// --- row actions -------------------------------------------------------------

// openRowMenu shows what can be done to one entry, positioned under the ⋯ that
// opened it (same placement maths as the spawn menu in app.js).
//
// The first line is the entry's full path, not a control. It identifies which
// row the menu belongs to, and it is the only place a touch device can read a
// name the tree truncated (title tooltips are unreachable there). It does NOT
// preview what the items below will put on the clipboard — "Copy path" copies
// the absolute path and "Copy relative path" copies a third, different string,
// so no single header could match all of them.
function openRowMenu(anchor, path, name, isDir) {
  const menu = document.getElementById("ft-menu");
  if (!menu) return;
  const nodes = [];

  const head = document.createElement("div");
  head.className = "ft-menu-path";
  head.textContent = shortPath(path);
  nodes.push(head);

  // Path actions first: the terminal next door is where these paths are going,
  // and inserting at the prompt is the shortest route there. Copy is second
  // because composing a command by hand means typing `cat ` BEFORE the path.
  const items = [
    ["Insert path at prompt", () => insertPathAtPrompt(path)],
    ["Copy path", () => copyPath(path)],
    ["Copy relative path", () => copyPath(relativeTo(ftRootSel ? ftRootSel.value : "", path))],
    null, // separator
    [isDir ? "Download .tar.gz" : "Download", () => downloadPath(path, isDir)],
  ];
  // The symlink-skip disclosure is NOT repeated here: downloadPath's probe toast
  // already carries it on every folder download, and a toast is readable on a
  // phone where the old ⤓ tooltip never was.
  if (isDir) items.push(["New folder here…", () => promptMkdir(path)]);
  items.push(null); // destructive actions sit past a second rule
  items.push(["Rename…", () => promptRename(path, name)]);
  items.push(["Delete…", () => confirmDelete(path, name, isDir)]);

  for (const it of items) {
    if (!it) { nodes.push(document.createElement("hr")); continue; }
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = it[0];
    b.onclick = () => { closeRowMenu(); it[1](); };
    nodes.push(b);
  }
  menu.replaceChildren(...nodes);

  menu.classList.add("open"); // must be visible before it can be measured
  const r = anchor.getBoundingClientRect();
  const w = menu.offsetWidth;
  menu.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, r.right - w)) + "px";
  // Rows sit anywhere in the viewport, unlike the spawn menu's header anchor, so
  // a menu opened near the bottom must flip above its row instead of clipping.
  // The path header makes this menu taller and its height variable, so the
  // measurement must stay AFTER replaceChildren, not before it.
  const h = menu.offsetHeight;
  const below = r.bottom + 4;
  menu.style.top = (below + h <= window.innerHeight - 8 ? below : Math.max(8, r.top - h - 4)) + "px";
}

// insertPathAtPrompt types an entry's ABSOLUTE path at the focused prompt.
// Absolute, not relative: it is correct no matter where the agent has cd'd,
// whereas a relative path is silently wrong the moment the tree root and the
// session's cwd diverge — which they do, because a manual root pick survives
// until a DIFFERENT session is focused.
function insertPathAtPrompt(path) {
  const term = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  const res = insertTextAtPrompt(term, shellQuote(path) + " ");
  if (res.ok) { focusTerminal(); return; } // closeRowMenu hid the button that had focus
  showToast(
    res.reason === "noterm" ? "Focus a terminal first, then insert the path" :
    res.reason === "unsafe" ? "That path contains control characters, so it was not inserted" :
    "That session is not connected, so the path was not inserted",
    { error: true });
}

// copyPath copies and says so: a clipboard write is otherwise invisible, and on
// this HTTP-only page copyText falls back to an off-screen textarea whose only
// visible symptom of failure would be nothing happening. Refused up front for
// the same reason insertTextAtPrompt refuses one menu item up: a path may
// legally contain a raw control character (e.g. a newline in a Linux filename),
// and pasting that into a terminal would submit whatever line it landed on.
function copyPath(text) {
  if (UNSAFE_TEXT.test(text)) {
    showToast("That path was NOT copied: it contains control characters", { error: true });
    return;
  }
  copyText(text, (ok) => {
    focusTerminal(); // the textarea fallback stole focus; shared with insertPathAtPrompt above
    showToast(ok ? "Copied " + text : "Could not copy " + text + " to the clipboard", { error: !ok });
  });
}

function closeRowMenu() {
  const menu = document.getElementById("ft-menu");
  if (menu) menu.classList.remove("open");
}

async function promptMkdir(dir) {
  const name = await askPrompt({
    title: "New folder",
    label: "Folder name",
    placeholder: "new-folder",
    confirmLabel: "Create",
  });
  if (name === null || !name) return;
  const res = await fsMkdir(dir, name);
  if (!res.ok) { showToast("Could not create: " + fsErrText(res), { error: true }); return; }
  showToast("Created " + name);
  ftLoadRoot();
}

async function promptRename(path, name) {
  const next = await askPrompt({
    title: "Rename",
    label: "New name",
    value: name,
    confirmLabel: "Rename",
  });
  if (next === null || !next || next === name) return;
  let res = await fsRename(path, next);
  // A protected folder must not be renamable around its delete gate. mustMatch
  // keeps the confirm button disabled until the typed name is exactly right, so
  // a confirmed dialog has already matched.
  if (res.status === 409 && res.data && res.data.confirm_name) {
    const typed = await askPrompt({
      title: "Protected folder",
      body: 'This folder is protected. Type "' + res.data.confirm_name + '" to rename it.',
      label: "Folder name",
      confirmLabel: "Rename",
      mustMatch: res.data.confirm_name,
    });
    if (typed === null) return;
    res = await fsRename(path, next, typed);
  }
  if (!res.ok) { showToast("Could not rename: " + fsErrText(res), { error: true }); return; }
  // The old path is gone either way, so any viewer tab on it is stale.
  if (typeof closeViewersFor === "function") closeViewersFor(path);
  showToast("Renamed to " + next);
  ftLoadRoot();
}

// confirmDelete drives the server's two-step gate: the first request reports
// what is inside, the second carries back the count the user actually saw.
async function confirmDelete(path, name, isDir) {
  let res = await fsDelete(path);
  if (res.status === 409 && res.data && res.data.confirm_name) {
    // Protected: name what is inside as well as demanding the typed name. The
    // folders that earn this gate must not be the ones told the least.
    const d = res.data;
    const inside = deleteSummary(d);
    const typed = await askPrompt({
      title: "Delete protected folder",
      body: 'Deleting "' + d.confirm_name + '"' + (inside ? ", " + inside : "") +
        ". This cannot be undone.",
      label: "Type the folder name to confirm",
      confirmLabel: "Delete",
      danger: true,
      mustMatch: d.confirm_name,
    });
    if (typed === null) return;
    res = await fsDelete(path, { recursive: true, confirmName: typed, entries: d.entries || 0 });
  } else if (res.status === 409 && res.data) {
    const d = res.data;
    const okDelete = await askConfirm({
      title: "Delete",
      body: deleteConfirmText(name, isDir, d),
      confirmLabel: "Delete",
      danger: true,
    });
    if (!okDelete) return;
    res = await fsDelete(path, { recursive: true, entries: d.entries || 0 });
  }
  if (!res.ok) { showToast("Could not delete: " + fsErrText(res), { error: true }); return; }
  if (typeof closeViewersFor === "function") closeViewersFor(path);
  showToast("Deleted " + name);
  ftLoadRoot();
}

// syncTreeToSession re-roots the tree at a session's cwd, but ONLY when a
// *different* session becomes focused (sessionId changes), so a manual root
// pick survives until you focus another session. A cwd not already in the
// dropdown is added as a transient option so any depth/custom cwd works.
async function syncTreeToSession(cwd, sessionId) {
  if (!ftRootSel || !cwd) return;
  if (sessionId && sessionId === lastSyncedSessionId) return; // already following this session (respect manual pick)
  lastSyncedSessionId = sessionId || null;
  let has = false;
  for (const o of ftRootSel.options) if (o.value === cwd) { has = true; break; }
  if (!has) {
    const o = document.createElement("option");
    o.value = cwd; o.textContent = "↳ " + (cwd.replace(/\/$/, "").split("/").pop() || cwd);
    o.title = cwd;
    ftRootSel.appendChild(o);
  }
  ftRootSel.value = cwd;
  await ftLoadRoot();
}

async function initFileTree() {
  ftRootSel = document.getElementById("ft-root");
  const pathInp = document.getElementById("ft-path");
  ftTreeEl = document.getElementById("filetree");
  if (!ftRootSel || !pathInp || !ftTreeEl) return;

  try {
    const ws = (await api("/api/workspaces")) || [];
    window.HOME_DIR = (ws[0] && ws[0].path) || ""; // first entry is "~ (home)": used to expand ~/ in clicked links
    ftRootSel.replaceChildren(...ws.map((w) => {
      const o = document.createElement("option");
      o.value = w.path; o.textContent = w.label;
      return o;
    }));
  } catch (_) {}

  // A manual dropdown pick re-roots the tree but does NOT change which session
  // we're following, so it survives until a different session is focused.
  ftRootSel.onchange = ftLoadRoot;

  // New folder in the root currently shown: no row can reach the root itself.
  const newdir = document.getElementById("ft-newdir");
  if (newdir) newdir.onclick = () => promptMkdir(ftRootSel.value);

  // One document-level closer for the row menu, like the spawn menu's.
  document.addEventListener("click", (e) => {
    const menu = document.getElementById("ft-menu");
    if (menu && menu.classList.contains("open") && !menu.contains(e.target)) closeRowMenu();
  });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeRowMenu(); });

  // Manual re-read. The tree loads lazily and never polls, so anything the AGENT
  // writes, which is most of what changes in this app, is invisible until the
  // root is re-listed. Without this the only ways were re-rooting or reloading
  // the whole page. Disabled while in flight so an impatient double click cannot
  // stack requests (ftLoadRoot's generation guard would drop one anyway).
  const refresh = document.getElementById("ft-refresh");
  if (refresh) {
    refresh.onclick = async () => {
      refresh.disabled = true;
      try { await ftLoadRoot(); } finally { refresh.disabled = false; }
    };
  }
  pathInp.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); const v = pathInp.value.trim(); if (v) { openFile(v); pathInp.value = ""; } }
  };

  // initial root: the focused terminal's cwd if one is already open, else home.
  const term = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  if (term && term.cwd) await syncTreeToSession(term.cwd, term.id);
  else if (ftRootSel.options.length) await ftLoadRoot();
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { iconFor };
}
