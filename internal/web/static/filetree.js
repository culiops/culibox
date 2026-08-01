"use strict";

// File tree: lazy directory browser rooted at a workspace. Clicking a file opens
// it in the focused cell (openFile). A paste-path input opens any absolute path.
// The root auto-follows the focused TAB — a terminal's cwd or the directory of
// the file a viewer is showing (see treeFollowTarget / syncTreeToRoot) — while a
// manual pick from the dropdown survives until a *different* tab is focused.

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

// treeBranch builds the box-drawing prefix for one row and the prefix its own
// children inherit. Depth used to be left padding; drawing it is what makes the
// tree read as a TUI.
//
// The whole subtlety is the second return value: a row that is the last of its
// siblings stops its vertical (children get spaces), while a row that is not
// keeps it running (children get "│"). Get that wrong and deep trees grow
// verticals that belong to branches which already ended.
//
// Two characters per level, not three: this matches WebTUI's own marker-="tree"
// convention (`content: "├ "` — no dash), not an invention of ours. Note this
// repo vendors only webtui's box.css/badge.css, not typography.css, so the tree
// itself is hand-rolled to the same convention rather than consuming that CSS.
// At the sidebar's 280px default (200px minimum), every character given to the
// connector is a character taken from the filename.
//
// U+2500 Box Drawing, NOT the Private Use Area — these are in ordinary monospace
// fonts and do not depend on the vendored subset.
function treeBranch(prefix, isLast) {
  return {
    rowPrefix: prefix + (isLast ? "└ " : "├ "),
    childPrefix: prefix + (isLast ? "  " : "│ "),
  };
}

let ftRootSel = null, ftTreeEl = null;
// ftRoot / setFtRoot are the ONLY way any other script reads or writes the
// tree's root. They exist so the control underneath can change shape without
// three other files reaching into its .value — upload.js did that in three
// places, and filetree.js in three more.
function ftRoot() { return (ftRootSel && ftRootSel.value) || ""; }
function setFtRoot(path) { if (ftRootSel) ftRootSel.value = path; }
let lastSyncedKey = null;       // which tab the tree is currently following (see treeFollowTarget)
let ftGen = 0;                  // bumped per root load; a superseded load must not paint

async function ftRenderDir(path, container, prefix) {
  prefix = prefix || "";
  let data;
  try { data = await api("/api/fs/list?path=" + encodeURIComponent(path)); }
  catch (e) { const d = document.createElement("div"); d.className = "ft-err"; d.textContent = e.message; container.append(d); return; }
  for (let i = 0; i < data.entries.length; i++) {
    const ent = data.entries[i];
    // Indexed, not for..of: a row cannot draw itself without knowing whether it
    // is the last of its siblings — that is what decides └ against ├, and
    // whether its own children keep the vertical running.
    const b = treeBranch(prefix, i === data.entries.length - 1);
    const full = path.replace(/\/$/, "") + "/" + ent.name;
    const row = document.createElement("div");
    row.className = "ft-node";
    // Depth is drawn now, not padded. The branch is its own element for the same
    // reason the icon is: the label's textContent is rewritten on every toggle.
    const branch = document.createElement("span");
    branch.className = "ft-branch";
    branch.textContent = b.rowPrefix;
    branch.setAttribute("aria-hidden", "true"); // decorative: indentation is not content
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
    label.textContent = ent.name;
    // The title goes on the LABEL, not the row: the ⋯ button carries its own
    // title and must keep it. The full path rather than the bare name, because
    // it also answers "which of the three index.html files is this one".
    label.title = shortPath(full);
    row.append(branch, icon, label);
    // One affordance per row. Download used to sit here as a bare ⤓ and Rename /
    // Delete behind the menu; two controls squeezed the name column (badly at
    // 390px) and put a destructive action a finger-width from a harmless one.
    // Download costs a second click now, and in exchange the tarball's
    // symlink-skip finally reads as a visible label instead of a tooltip no
    // touch device can reach.
    const more = document.createElement("button");
    more.className = "ft-more nf";
    more.textContent = "";
    more.title = "Actions";
    // A button's accessible name comes from its TEXT CONTENT before its title,
    // and the title is only consulted when that text is empty. This button's text
    // is a Private Use Area glyph — non-empty, but with no Unicode name — so
    // without this the control announces as nothing at all.
    more.setAttribute("aria-label", "Actions");
    more.onclick = (e) => { e.stopPropagation(); openRowMenu(more, full, ent.name, ent.is_dir); };
    row.append(more);
    if (ent.is_dir) {
      let open = false, childBox = null;
      row.classList.add("dir");
      row.onclick = async () => {
        open = !open;
        icon.textContent = iconFor(ent.name, true, open);
        if (open) {
          childBox = document.createElement("div");
          row.after(childBox);
          await ftRenderDir(full, childBox, b.childPrefix);
        } else if (childBox) { childBox.remove(); childBox = null; }
      };
      // Each directory row is its own drop target and stops propagation, so it
      // wins over the whole-tree zone underneath it.
      if (typeof makeDropZone === "function") {
        makeDropZone(row, () => full, {
          onDone: () => {
            if (!open || !childBox) return;
            childBox.replaceChildren();
            ftRenderDir(full, childBox, b.childPrefix);
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
  await ftRenderDir(ftRoot(), box);
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
    ["Copy relative path", () => copyPath(relativeTo(ftRoot(), path))],
  ];
  // Contents last in the clipboard group, and only where there is text to take.
  // A directory has none, and an image is served as raw bytes rather than JSON,
  // so asking for one would fail inside JSON.parse with nothing useful to say.
  // A symlink TO a directory still gets the item — os.ReadDir reports lstat, so
  // the tree cannot tell — and /api/fs/read answers "is a directory", which the
  // toast repeats.
  if (!isDir && !IMAGE_EXTS.has(extOf(name))) {
    items.push(["Copy contents", () => copyContents(path, name)]);
  }
  items.push(null); // separator
  items.push([isDir ? "Download .tar.gz" : "Download", () => downloadPath(path, isDir)]);
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
// until a DIFFERENT tab is focused.
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

// copyContents reads the file and copies its text. Unlike the viewer's button
// there is nothing on screen to be faithful to, so it reads fresh.
//
// The clipboard write can fail here for an ordinary reason, not an exotic one:
// this page is not a secure context, so copyText always lands in
// execCommand("copy"), which needs a user activation that expires a few seconds
// after the click that opened the menu. A slow read really can come back
// ok=false, which is why the failure message says to try again.
async function copyContents(path, name) {
  const plan = copyContentPlan(await fsRead(path));
  if (plan.action === "refuse") {
    const m = copyResultMessage(plan, name, false);
    // closeRowMenu already hid the button that had focus, so a refusal that
    // returns here would leave the next keystroke going nowhere.
    focusTerminal();
    showToast(m.text, { error: m.error });
    return;
  }
  copyText(plan.text, (ok) => {
    focusTerminal(); // the textarea fallback stole focus, as in copyPath above
    const m = copyResultMessage(plan, name, ok);
    showToast(m.text, { error: m.error });
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

// treeFollowTarget answers, for the focused tab, which directory the tree should
// be rooted at and under what key the follow-guard records that it is already
// there. null means "hold the current root".
//
// The key is what lets a manual root pick survive until a DIFFERENT tab is
// focused. A terminal keys on its session; a viewer has no session, so it keys
// on the directory it resolves to — which also means paging through several
// files in one folder is a single target, not one re-root per file.
//
// The absolute-path test is a guard, not a formality. `path` reaches a ViewerTab
// through three producers that can each supply a relative string: the paste
// input (whose "paste absolute path" is a placeholder, not a check),
// TerminalTab.resolvePath while its cwd is still empty, and a restored snapshot
// that only tested for a truthy string. A relative root reaches /api/fs/list,
// earns a 400, and paints ft-err over a tree that had been fine.
function treeFollowTarget(tab, home) {
  // Same optional-home convention as shortPath/relativeTo in fsops.js: an
  // explicit argument for tests, window.HOME_DIR in the browser.
  if (home === undefined) home = (typeof window !== "undefined" && window.HOME_DIR) || "";
  if (!tab) return null;
  // insideHome APPROXIMATES the server's rule, textually. /api/fs/list resolves
  // symlinks on both sides (containedUnder, fsapi.go:40-47); this cannot, so the
  // two can disagree. It errs safe in one direction — textually-outside but
  // resolving inside just holds the root — but a depth-1 symlink in $HOME that
  // points OUT of it reads as inside here and would still earn the 403 below.
  // listWorkspaces offers such symlinks as workspaces (workspaces.go:49), so the
  // shape is legal; there are none on this host today.
  // /api/fs/list is confined to $HOME and
  // answers 403 for anything outside it, so rooting there does not fail
  // gracefully: ftLoadRoot replaces the whole tree with one "forbidden" line,
  // the follow-key has already been recorded so re-focusing will not retry, and
  // a junk transient option is left in the picker. Holding the previous root is
  // the only sane answer.
  //
  // This is not hypothetical. LINK_PATH_RE (links.js) matches
  // /etc/nginx/nginx.conf and /opt/culiops/staging/docker-compose.yml, and
  // clicking such a link in a terminal opens it as a viewer
  // (terminal-tab.js:156). A session whose cwd is outside home reaches it too —
  // that path predates the viewer support and is covered by the same guard.
  //
  // With no home known yet (pre-/api/workspaces) the check cannot run, so it
  // allows: initFileTree re-asserts the root once the list arrives.
  const insideHome = (d) => !home || d === home || d.indexOf(home + "/") === 0;
  if (tab.kind === "terminal") {
    // cwd is "" until TerminalTab's fetch resolves it; rooting there would send
    // the server a request it must reject.
    if (!tab.cwd || !insideHome(tab.cwd)) return null;
    return { root: tab.cwd, key: "sess:" + tab.id };
  }
  if (tab.kind === "viewer") {
    // Trailing slashes, "//" and "./" cannot arrive here: ViewerTab's
    // constructor normalises through normalizePath (viewer-tab.js:15), as does
    // openFile (tabs.js). The tests feed un-normalised input deliberately, so
    // this function must not quietly depend on that beyond what it checks.
    const p = tab.path;
    if (!p || p[0] !== "/") return null;
    const cut = p.lastIndexOf("/");
    // A file directly under "/" has its slash at index 0: slice(0, 0) is "",
    // which is not a root anything can list.
    const dir = cut > 0 ? p.slice(0, cut) : "/";
    if (!insideHome(dir)) return null;
    return { root: dir, key: "view:" + dir };
  }
  return null;
}

// syncTreeToRoot re-roots the tree, but ONLY when the follow KEY changes — so a
// manual root pick survives until a different tab is focused. A root not already
// in the dropdown is added as a transient option so any depth/custom path works.
//
// The `!ftRootSel || !root` early return is load-bearing and must stay AHEAD of
// the key write: during boot restore a fresh TerminalTab has no cwd yet, and
// recording the key before there is a root to go with it would tell the guard we
// are already following a tab we never rooted at.
async function syncTreeToRoot(root, key) {
  if (!ftRootSel || !root) return;
  if (key && key === lastSyncedKey) return; // already following this tab (respect manual pick)
  lastSyncedKey = key || null;
  let has = false;
  for (const o of ftRootSel.options) if (o.value === root) { has = true; break; }
  if (!has) {
    const o = document.createElement("option");
    o.value = root; o.textContent = "↳ " + (root.replace(/\/$/, "").split("/").pop() || root);
    o.title = root;
    ftRootSel.appendChild(o);
  }
  setFtRoot(root);
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

  // A manual dropdown pick re-roots the tree but does NOT change which TAB
  // we're following, so it survives until a different tab is focused.
  ftRootSel.onchange = ftLoadRoot;

  // New folder in the root currently shown: no row can reach the root itself.
  const newdir = document.getElementById("ft-newdir");
  if (newdir) newdir.onclick = () => promptMkdir(ftRoot());

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

  // Initial root: whatever the focused TAB resolves to — a terminal's cwd or a
  // viewer's directory — else home. focusedTerminal() was wrong here: a layout
  // restored onto a viewer booted at home with the follow-guard unset.
  // The option list was only just built, and a restore that won the race
  // against /api/workspaces may already have rooted the tree against an EMPTY
  // list — replaceChildren above then destroyed that option and reset .value to
  // home, leaving the painted tree and the picker disagreeing. Clearing the
  // guard makes this seeding authoritative so it re-asserts the root over the
  // fresh list. The cost is one redundant listing in the common case.
  // Gated on a populated picker: if /api/workspaces threw above, HOME_DIR is
  // unset, so the containment check is disabled and seeding could root outside
  // home and paint ft-err. That state is already broken (empty picker); it
  // should stay quiet rather than add an error to it.
  if (ftRootSel.options.length) {
    lastSyncedKey = null;
    const t = (typeof focusedTab === "function") ? focusedTab() : null;
    const target = treeFollowTarget(t);
    if (target) await syncTreeToRoot(target.root, target.key);
    else await ftLoadRoot();
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { iconFor, treeBranch, treeFollowTarget };
}
