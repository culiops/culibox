"use strict";

// File tree: lazy directory browser rooted at a workspace. Clicking a file opens
// it in the focused cell (openFile). One searchable control picks the root —
// fuzzy-search a directory or paste an absolute path into the same box (see
// dirpicker.js); a pasted path that turns out to be a FILE is opened.
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

// actionsLabel names the row's ⋯ menu button by what it acts on, matching the
// session row's kebab (app.js: "Session actions" on both aria-label and title).
// A glyph button has no accessible name from its own text, so screen readers
// need this — and "Actions" alone didn't say whether the row was a file or a
// folder.
function actionsLabel(isDir) {
  return isDir ? "Folder actions" : "File actions";
}

// listingReadOnly: did /api/fs/list say every write here is refused (the Claude
// scratch folder, or anything reached through it)? Only the exact boolean true
// counts: the key is absent for an ordinary listing, so an older server, an error
// body or a string all read as "writable" and keep today's menus. The server
// stays the authority either way; this only spares the user a button that would
// answer 403.
function listingReadOnly(data) {
  return !!data && data.readonly === true;
}

// The row menu's items by id. The ids and their order are what the tests pin;
// the labels are the strings the menu has always shown.
const ROW_MENU_LABELS = {
  insert: "Insert path at prompt",
  copy: "Copy path",
  copyrel: "Copy relative path",
  copycontents: "Copy contents",
  download: "Download",
  downloaddir: "Download .tar.gz",
  newdir: "New folder here\u2026",
  rename: "Rename\u2026",
  delete: "Delete\u2026",
};
function rowMenuLabel(id, isDir) {
  return ROW_MENU_LABELS[id === "download" && isDir ? "downloaddir" : id];
}

// rowMenuIds lists a row's menu, `null` being a rule. Path actions first: the
// terminal next door is where these paths are going, and inserting at the prompt
// is the shortest route there. Copy is second because composing a command by
// hand means typing `cat ` BEFORE the path. Then Download and, on a folder, New
// folder here, then past a second rule the destructive pair. A read-only listing
// keeps only what reads: no New folder here, Rename or Delete, and not the rule
// that fenced them off.
function rowMenuIds(o) {
  const ids = ["insert", "copy", "copyrel"];
  // Contents last in the clipboard group, and only where there is text to take.
  // A directory has none, and an image is served as raw bytes rather than JSON,
  // so asking for one would fail inside JSON.parse with nothing useful to say.
  // A symlink TO a directory still gets the item (os.ReadDir reports lstat, so
  // the tree cannot tell) and /api/fs/read answers "is a directory", which the
  // toast repeats.
  if (!o.isDir && !o.isImage) ids.push("copycontents");
  ids.push(null, "download");
  if (o.readOnly) return ids;
  if (o.isDir) ids.push("newdir");
  ids.push(null, "rename", "delete"); // destructive actions sit past a second rule
  return ids;
}

// The row menu on desktop is the kit menu (kitmenu.js), whose rows come in groups with a hairline between
// two groups: the path actions (where the text is going next door), then Download and New folder here, then
// Rename and Delete each alone: the destructive one is always last, after a rule. ftMenuGroups regroups
// rowMenuIds' list (the phone keeps that list and its rules) into those, dropping what the listing leaves out,
// and ids it does not know.
const ROW_MENU_GROUPS = [
  ["insert", "copy", "copyrel", "copycontents"],
  ["download", "newdir"],
  ["rename"],
  ["delete"],
];
function ftMenuGroups(ids) {
  return ROW_MENU_GROUPS.map((g) => g.filter((id) => ids.includes(id))).filter((g) => g.length > 0);
}
// The icon of each row (kitmenu.js menuIconSvg kinds): inline SVG, never a glyph a font may lack.
const ROW_MENU_ICONS = {
  insert: "insert", copy: "copy", copyrel: "copy", copycontents: "copy",
  download: "download", newdir: "folder-plus", rename: "pencil", delete: "delete",
};

// ftKeyAction: what a key does on the tree row `at` (F68). `rows` is every visible row in order,
// [{dir, open, level}] (level 1 at the root). Returns {index, action}: `index` is the row the key is about
// (the row to focus for "focus", else `at`), `action` is one of
//   "focus"     move focus to rows[index]                 ArrowDown / ArrowUp (clamped), Home, End,
//                                                          ArrowRight into an open folder (its first child),
//                                                          ArrowLeft to the parent of a file or closed folder
//   "expand"    open the folder                           ArrowRight on a closed folder
//   "collapse"  close the folder                          ArrowLeft on an open folder
//   "open"      what a click does                         Enter
//   "menu"      open the row's ⋯ menu                     the ContextMenu key, Shift+F10
//   null        the key is not the tree's: leave it alone (a plain F10 is the browser's menu bar)
// A clamped ArrowDown at the last row still answers "focus", so the page does not scroll under the key.
function ftKeyAction(rows, at, key, shift) {
  const none = { index: at, action: null };
  const n = rows.length;
  if (!(n > 0) || !(at >= 0 && at < n)) return none;
  const row = rows[at];
  const focus = (i) => ({ index: Math.max(0, Math.min(n - 1, i)), action: "focus" });
  switch (key) {
    case "ArrowDown": return focus(at + 1);
    case "ArrowUp": return focus(at - 1);
    case "Home": return focus(0);
    case "End": return focus(n - 1);
    case "ArrowRight":
      if (!row.dir) return none;
      if (!row.open) return { index: at, action: "expand" };
      // An open folder's first child is the next row one level down; a folder that is empty (or still
      // loading) has none, and the next row is its sibling.
      return at + 1 < n && rows[at + 1].level > row.level ? { index: at + 1, action: "focus" } : none;
    case "ArrowLeft":
      if (row.dir && row.open) return { index: at, action: "collapse" };
      for (let i = at - 1; i >= 0; i--) if (rows[i].level === row.level - 1) return { index: i, action: "focus" };
      return none;
    case "Enter": return { index: at, action: "open" };
    case "ContextMenu": return { index: at, action: "menu" };
    case "F10": return shift ? { index: at, action: "menu" } : none;
    default: return none;
  }
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
// repo vendors only webtui's box.css/separator.css, not typography.css, so the tree
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

let ftRootSel = null, ftTreeEl = null, ftRootPath = "";
// ftRoot / setFtRoot are the ONLY way any other script reads or writes the
// tree's root. They exist so the control underneath can change shape without
// three other files reaching into its .value — upload.js did that in three
// places, and filetree.js in three more.
//
// The root is stored in ftRootPath, NOT read back from the input's .value: the
// input doubles as the picker's search box, and only a successful pick clears
// it. Escape, an outside click, and a 404/403/500 on a typed path all leave
// whatever the user was typing sitting in .value — reading the root from there
// would silently re-root the tree at abandoned search text.
function ftRoot() { return ftRootPath; }
function setFtRoot(path) {
  ftRootPath = path || "";
  if (ftRootSel) { ftRootSel.value = ftRootPath; ftRootSel.title = ftRootPath; }
  // Keep a collapsed Files bar's root readout live even though the tree
  // itself may be hidden — this is the one place the root actually changes.
  if (typeof renderCollapsedSummaries === "function") renderCollapsedSummaries();
}
let lastSyncedKey = null;       // which tab the tree is currently following (see treeFollowTarget)
let ftGen = 0;                  // bumped per root load; a superseded load must not paint
let ftExpanded = new Set();     // absolute dir paths the user has opened; see treestate.js
// The root listing's own read-only answer, kept so the whole-tree drop zone
// (upload.js) can ask at drop time. Set by ftLoadRoot, only for the newest load.
let ftRootReadOnly = false;
function ftRootIsReadOnly() { return ftRootReadOnly; }
// ftApplyReadOnly hides the toolbar buttons that write into the root (new folder,
// upload) while its listing is read-only. The `hidden` attribute needs the
// !important rule in app.css to win against the desktop drawer's and the phone's
// own display rules; refresh and the palette stay.
function ftApplyReadOnly(ro) {
  for (const id of ["ft-newdir", "ft-upload"]) {
    const el = document.getElementById(id);
    if (el) el.hidden = !!ro;
  }
}

// The Files drawer marks what the agent changed (filesdrawer.js decides which
// paths): a dot after the name. A row's path is kept here, not in the DOM, so
// the dots can be repainted when the answer arrives after the tree is drawn.
// `set` null = no marks (mobile, or the drawer is not showing Files): the span
// is only ever added while the drawer owns file clicks.
const ftRowPaths = new WeakMap();
// A directory row's expand control and state, for ftReveal (see setOpen).
const ftRowOpeners = new WeakMap();
function ftMarkRow(row, path, set) {
  const have = row.querySelector(":scope > .ft-chg");
  if (!set || !set.has(path)) { if (have) have.remove(); return; }
  if (have) return;
  const dot = document.createElement("span");
  dot.className = "ft-chg";
  dot.textContent = "\u25CF";
  dot.title = "edited by this session's agent";
  // After the git badge (gitstatus.js) when there is one: the badge is the
  // fact, this dot is the lesser hint, and it stays alone where git has no mark.
  const anchor = row.querySelector(":scope > .ft-git, :scope > .ft-git-dot") || row.querySelector(":scope > .ft-name");
  if (anchor) anchor.after(dot); else row.append(dot);
}
function ftPaintChanged(set) {
  if (!ftTreeEl) return;
  for (const row of ftTreeEl.querySelectorAll(".ft-node")) {
    const p = ftRowPaths.get(row);
    if (p) ftMarkRow(row, p, set);
  }
}

// The tree is one Tab stop, not one per row (F68, the WAI-ARIA tree pattern): the row with tabindex="0" is
// where Tab lands and the arrows walk from; every other row is tabindex="-1" (focusable by the arrows and a
// click, skipped by Tab), and so is every row's ⋯, which the menu key reaches instead. ftRovePath is the
// stop's path, kept here and not in the DOM so a re-render (refresh, expand, a delete) gives the stop back to
// the same path, or to the first row when that path is gone.
let ftRovePath = "";
// ftRoveTo hands the Tab stop to `row` (a focus on it or inside it does this).
function ftRoveTo(row) {
  const p = ftRowPaths.get(row);
  if (!p) return;
  ftRovePath = p;
  const tree = row.closest ? row.closest('[role="tree"]') : null;
  if (tree) for (const r of tree.querySelectorAll('.ft-node[tabindex="0"]')) if (r !== row) r.setAttribute("tabindex", "-1");
  row.setAttribute("tabindex", "0");
}
// ftRoveEnsure: the tree has a Tab stop. A folder that closes takes its rows with it, and the stop with them
// when the focused row was inside; the first row takes it, or the tree could no longer be reached with Tab.
function ftRoveEnsure(tree) {
  if (!tree || tree.querySelector('.ft-node[tabindex="0"]')) return;
  const first = tree.querySelector(".ft-node");
  if (first) ftRoveTo(first);
}
// ftRowModel: a row as ftKeyAction sees it, read back from its ARIA (a folder is the row that has
// aria-expanded), so the keys and the screen reader agree on what the tree is.
function ftRowModel(row) {
  const ex = row.getAttribute("aria-expanded");
  return { dir: ex !== null, open: ex === "true", level: Number(row.getAttribute("aria-level")) || 1 };
}

// `st` is the root render's own bookkeeping, handed down to the folders it auto-opens: every row drawn under
// it, in visible order, so the one Tab stop is chosen once the whole subtree is there.
async function ftRenderDir(path, container, prefix, st) {
  prefix = prefix || "";
  const rootCall = !st && prefix === "";
  if (rootCall) st = { rows: [] };
  // The pending line sits where the children will go, indented to match them
  // (app.css reads --ft-indent). One line per directory in flight: a restored
  // subtree loads level by level, each into its own box. It is gone before the
  // entries or the error row are appended.
  container.style.setProperty("--ft-indent", prefix.length + "ch");
  const done = pendingRegion(container, "Loading…");
  let data, failure = null;
  try { data = await api("/api/fs/list?path=" + encodeURIComponent(path)); }
  catch (e) { failure = e; }
  finally { done(); }
  // A failed listing is not "read-only": the buttons stay and the server refuses.
  if (failure) { const d = document.createElement("div"); d.className = "ft-err"; d.textContent = failure.message; container.append(d); return false; }
  // Per listing, not per tree: a link in $HOME can lead into the scratch folder
  // and back, so each directory answers for itself. Every row below acts on
  // THIS listing's folder (rename, delete, drop onto it).
  const ro = listingReadOnly(data);
  const chg = typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks() && typeof drawerChangedFor === "function"
    ? drawerChangedFor(ftRoot()) : null;
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
    // A tree item, one level per two characters of prefix, named by its entry (not by its content, which
    // would add the ⋯'s "Folder actions" and the git badge to every name). Out of the Tab order until the
    // roving stop is chosen (below, or ftRoveTo).
    row.setAttribute("role", "treeitem");
    row.setAttribute("aria-level", String(prefix.length / 2 + 1));
    row.setAttribute("aria-label", ent.name);
    row.setAttribute("tabindex", "-1");
    // One affordance per row. Download used to sit here as a bare ⤓ and Rename /
    // Delete behind the menu; two controls squeezed the name column (badly at
    // 390px) and put a destructive action a finger-width from a harmless one.
    // Download costs a second click now, and in exchange the tarball's
    // symlink-skip finally reads as a visible label instead of a tooltip no
    // touch device can reach.
    const more = document.createElement("button");
    more.className = "ft-more nf";
    more.textContent = "";
    // Folder vs File so a screen reader announces which kind of row this menu
    // acts on, matching the session row's kebab (app.js sets both aria-label
    // and title to the same string).
    const moreLabel = actionsLabel(ent.is_dir);
    more.title = moreLabel;
    // A button's accessible name comes from its TEXT CONTENT before its title,
    // and the title is only consulted when that text is empty. This button's text
    // is a Private Use Area glyph — non-empty, but with no Unicode name — so
    // without this the control announces as nothing at all.
    more.setAttribute("aria-label", moreLabel);
    more.setAttribute("tabindex", "-1"); // the row's menu key opens this menu; one Tab stop per tree, not two per row
    more.onclick = (e) => { e.stopPropagation(); openRowMenu(more, full, ent.name, ent.is_dir, ro); };
    row.append(more);
    ftRowPaths.set(row, full);
    if (st && st.rows) st.rows.push(row);
    ftMarkRow(row, full, chg);
    // Marks from the status already held: a folder opened later, or a re-list
    // while a refresh is in flight, paints at once instead of waiting.
    if (typeof gitMarkRow === "function") gitMarkRow(row, full);
    // Appended BEFORE the directory branch, not after it: auto-open below calls
    // row.after(childBox), and ChildNode.after() is a silent no-op while the node
    // has no parent — so an orphaned row would render its restored subtree into a
    // box that is never inserted. Nothing between here and the branch touches
    // container, and a click's row.after() still lands correctly.
    container.append(row);
    if (ent.is_dir) {
      let open = false, childBox = null;
      row.classList.add("dir");
      row.setAttribute("aria-expanded", "false");
      // openChildren renders this directory's contents into a fresh box right
      // after its row. Shared by the click toggle and the auto-open on render so
      // the two cannot drift in how the subtree is built.
      const openChildren = async () => {
        childBox = document.createElement("div");
        row.after(childBox);
        await ftRenderDir(full, childBox, b.childPrefix, st);
      };
      // setOpen is the one expand/collapse path: the click toggles it, and
      // ftReveal drives it through ftRowOpeners to open the ancestors of a file,
      // so a revealed folder is persisted (treestate) exactly like a clicked one.
      const setOpen = async (want) => {
        if (want === open) return;
        open = want;
        icon.textContent = iconFor(ent.name, true, open);
        row.setAttribute("aria-expanded", open ? "true" : "false");
        if (open) {
          // Persist BEFORE the await: a second click that closes the row toggles
          // `open` synchronously, so the stored state must already reflect open.
          addExpanded(ftExpanded, full);
          saveExpanded(ftExpanded);
          await openChildren();
        } else {
          removeExpanded(ftExpanded, full);
          saveExpanded(ftExpanded);
          if (childBox) { childBox.remove(); childBox = null; }
          ftRoveEnsure(row.closest ? row.closest('[role="tree"]') : null);
        }
      };
      row.onclick = () => setOpen(!open);
      ftRowOpeners.set(row, { setOpen, isOpen: () => open });
      // Each directory row is its own drop target and stops propagation, so it
      // wins over the whole-tree zone underneath it.
      if (typeof makeDropZone === "function") {
        makeDropZone(row, () => full, {
          readOnly: () => ro,
          onDone: () => {
            if (!open || !childBox) return;
            childBox.replaceChildren();
            ftRenderDir(full, childBox, b.childPrefix);
            if (typeof gitStatusRefreshSoon === "function") gitStatusRefreshSoon();
          },
        });
      }
      // Auto-open on render if this directory was open before. This is what
      // restores the tree after a refresh or a re-root: rendering the root opens
      // each remembered child, whose own render opens ITS remembered children, so
      // the whole previously-open subtree comes back. It does NOT re-persist: it
      // only reflects state already stored.
      //
      // The await is deliberate, and it is what makes the ftGen guard sound: the
      // ENTIRE remembered subtree renders into ftLoadRoot's off-DOM box before the
      // atomic replaceChildren, so a superseded load is dropped whole with nothing
      // half-painted into the live tree. The cost is first paint — the root shows
      // only after the remembered folders' /api/fs/list calls resolve (one per
      // open folder, in series). Bounded by what the user actually expanded; a
      // handful of folders is imperceptible on a local server.
      if (shouldAutoOpen(ftExpanded, full)) {
        open = true;
        icon.textContent = iconFor(ent.name, true, true);
        row.setAttribute("aria-expanded", "true");
        await openChildren();
      }
    } else {
      // The desktop Files drawer shows the file beside the tree instead of in a
      // pane (filesdrawer.js; typeof-guarded, it loads later). Mobile: openFile.
      row.onclick = () => {
        if (typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks()) drawerShowFile(full);
        else openFile(full);
      };
    }
  }
  if (rootCall) {
    // The whole tree is drawn (a restored subtree included): the Tab stop is the row the user last had
    // focus on if it is still here, else the first row. Folders opened later never take it (st.rows is gone).
    const stop = st.rows.find((r) => ftRowPaths.get(r) === ftRovePath) || st.rows[0];
    if (stop) stop.setAttribute("tabindex", "0");
    st.rows = null;
  }
  return ro;
}

// ftLoadRoot renders off-DOM and swaps in only if it is still the newest load.
// Two roots can be in flight at once: boot loads home while a session's cwd
// arrives and re-roots, and ftRenderDir appends after its own await, so painting
// straight into the tree let a superseded load leave its rows behind (the tree
// showed home while the picker said the session's directory).
let ftLoading = Promise.resolve(); // settles when the newest ftLoadRoot has finished its render, so ftReveal can wait for it
// opts.fresh: the caller just changed something on disk (or the user pressed
// refresh), so git status must be re-read rather than reused.
async function ftLoadRoot(opts) {
  if (!ftRootSel || !ftTreeEl) return;
  const gen = ++ftGen;
  let release;
  ftLoading = new Promise((r) => { release = r; });
  const box = document.createElement("div");
  // The whole render happens in an off-DOM box, so the pending lines
  // ftRenderDir paints there are never seen. This one sits in the live tree,
  // above whatever it shows now, and covers every level of a restored subtree.
  const done = pendingRegion(ftTreeEl, "Loading…");
  let ro = false;
  try {
    ro = await ftRenderDir(ftRoot(), box);
  } finally {
    done();
    // Waiters resume in a later microtask, after the swap below has run.
    release();
  }
  if (gen !== ftGen) return; // a newer root started meanwhile: drop this render
  ftRootReadOnly = ro === true;
  ftApplyReadOnly(ftRootReadOnly);
  ftTreeEl.replaceChildren(...box.childNodes);
  // The listing is on screen; now ask what git says about it (badges follow).
  if (typeof gitStatusLoad === "function") gitStatusLoad(ftRoot(), typeof gitStatusTreeSid === "function" ? gitStatusTreeSid() : null, { fresh: !!(opts && opts.fresh) });
  // A refresh asks again what the agent has changed since (Files drawer only).
  if (typeof drawerLoadChanged === "function" && typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks()) drawerLoadChanged(drawerState.sessionId);
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
//
// Desktop draws it as the kit menu (kitmenu.js, the same rows as the session row's ⋯): the path as a
// `.menu-label`, icon rows in groups with a rule between two groups (ftMenuGroups), Delete… red and alone
// last; it takes focus on its first row, and the arrow keys, Escape and a chosen row hand focus back to the
// tree row it hung from. The phone keeps the plain buttons and rules it has always had.
function openRowMenu(anchor, path, name, isDir, readOnly) {
  const menu = document.getElementById("ft-menu");
  if (!menu) return;
  const desktop = !mobileActive(window.innerWidth);
  const nodes = [];

  const head = document.createElement("div");
  if (desktop) {
    // The path WRAPS (desktop.css), it is not cut with an ellipsis: this line is where a name the tree had to
    // truncate can be read in full, and the file's own name is the tail of the path.
    head.className = "menu-label";
    const p = document.createElement("span");
    p.className = "mono t-xs muted";
    p.textContent = shortPath(path);
    head.append(p);
  } else {
    head.className = "ft-menu-path";
    head.textContent = shortPath(path);
  }
  nodes.push(head);

  // Which items, in which order, is rowMenuIds' (a read-only listing leaves out
  // what the server would refuse). The handlers live here.
  // The symlink-skip disclosure is NOT repeated on Download: downloadPath's probe
  // toast already carries it on every folder download, and a toast is readable on
  // a phone where the old ⤓ tooltip never was.
  const act = {
    insert: () => insertPathAtPrompt(path),
    copy: () => copyPath(path),
    copyrel: () => copyPath(relativeTo(ftRoot(), path)),
    copycontents: () => copyContents(path, name),
    download: () => downloadPath(path, isDir),
    newdir: () => promptMkdir(path),
    rename: () => promptRename(path, name),
    delete: () => confirmDelete(path, name, isDir),
  };
  const ids = rowMenuIds({ isDir: !!isDir, isImage: IMAGE_EXTS.has(extOf(name)), readOnly: readOnly === true });
  if (desktop) {
    // Close first and give focus to the row, then act: a dialog (New folder, Rename, Delete) hands focus back
    // to what had it when it opened, which is now the row, and an action that moves focus on (Insert path
    // goes to the terminal) still does.
    ftMenuGroups(ids).forEach((group, i) => {
      if (i > 0) nodes.push(kitMenuSep());
      for (const id of group) nodes.push(kitMenuButton(rowMenuLabel(id, isDir), ROW_MENU_ICONS[id], id === "delete", () => { closeRowMenu(true); act[id](); }));
    });
    ftShowMenu(anchor, nodes, false, actionsLabel(isDir));
    const first = menu.querySelector(".menu-item");
    if (first) first.focus();
    return;
  }
  for (const id of ids) {
    if (id === null) { nodes.push(document.createElement("hr")); continue; }
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = rowMenuLabel(id, isDir);
    b.onclick = () => { closeRowMenu(); act[id](); };
    nodes.push(b);
  }
  ftShowMenu(anchor, nodes);
}

// ftShowMenu fills #ft-menu with `nodes` and places it under `anchor` (above it
// when there is no room). Shared by the row menu and the viewer's line-number
// menu so both look and behave alike; closeRowMenu closes either.
// `alignLeft` lines the menu's left edge up with the anchor's (a gutter number
// sits at the left of its pane); the default right-aligns to a trailing ⋯.
// `kitLabel` (a string) makes it the kit's menu: the `.menu` container (desktop.css places it) with
// role="menu" named by `kitLabel`. Without it the element is the plain popover it was, so the viewer's
// line-number menu, which shares this element, never inherits a tree menu's class and role.
// ftMenuFrom is where the open menu hangs from, the tree row holding `anchor` (null for a menu that hangs
// from none, the viewer's), for closeRowMenu(true) to give focus back to.
let ftMenuFrom = null;
function ftShowMenu(anchor, nodes, alignLeft, kitLabel) {
  const menu = document.getElementById("ft-menu");
  if (!menu) return;
  menu.replaceChildren(...nodes);
  const kit = typeof kitLabel === "string";
  menu.classList[kit ? "add" : "remove"]("menu");
  if (kit) {
    menu.setAttribute("role", "menu");
    menu.setAttribute("aria-label", kitLabel);
  } else {
    menu.removeAttribute("role");
    menu.removeAttribute("aria-label");
  }
  const row = anchor.closest ? anchor.closest(".ft-node") : null;
  ftMenuFrom = row ? { row, path: ftRowPaths.get(row) || "" } : null;

  menu.classList.add("open"); // must be visible before it can be measured
  const r = anchor.getBoundingClientRect();
  const w = menu.offsetWidth;
  menu.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, alignLeft ? r.left : r.right - w)) + "px";
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
// `refocus` (optional) says where focus goes afterwards; the default is the
// terminal, which is right for the tree. The viewer passes its own reader.
function copyPath(text, refocus) {
  if (UNSAFE_TEXT.test(text)) {
    showToast("That path was NOT copied: it contains control characters", { error: true });
    if (refocus) refocus();
    return;
  }
  copyText(text, (ok) => {
    (refocus || focusTerminal)(); // the textarea fallback stole focus; shared with insertPathAtPrompt above
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

// closeRowMenu hides the menu. `restoreFocus` (Escape, a chosen row) gives focus back to the tree row it hung
// from: the live row for the same path when a refresh replaced it meanwhile, none when that path is gone, and
// never for the viewer's line-number menu, which hangs from no row. A click elsewhere leaves focus where the
// click put it.
function closeRowMenu(restoreFocus) {
  const menu = document.getElementById("ft-menu");
  const was = !!menu && menu.classList.contains("open");
  if (menu) menu.classList.remove("open");
  const from = ftMenuFrom;
  ftMenuFrom = null;
  if (!restoreFocus || !was || !from) return;
  const row = from.row.isConnected ? from.row : from.path ? ftRowFor(from.path) : null;
  if (row) row.focus();
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
  ftLoadRoot({ fresh: true });
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
  // The old path is gone either way, so any viewer tab on it is stale, and its
  // expand entries would be re-applied by a later render or linger in the cap.
  if (typeof closeViewersFor === "function") closeViewersFor(path);
  forgetSubtree(ftExpanded, path);
  saveExpanded(ftExpanded);
  showToast("Renamed to " + next);
  ftLoadRoot({ fresh: true });
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
  forgetSubtree(ftExpanded, path); // the path and its children are gone; drop their expand state
  saveExpanded(ftExpanded);
  showToast("Deleted " + name);
  ftLoadRoot({ fresh: true });
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
  // A box viewer's path exists only inside that session's box, never on the
  // host: /api/fs/list would 403 or list an unrelated host directory of the
  // same name. Hold the current root (see the 403 note below).
  if (tab.box) return null;
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
    // agentCwd: the worktree root the agent moved into (Claude Code's
    // EnterWorktree does not chdir), from the usage poll; "" outside one. The
    // tree shows that checkout, as terminal links already resolve into it, or a
    // click in the tree opens the main checkout's copy. The key carries it so
    // entering or leaving re-roots, while a manual pick still holds in between.
    if (tab.agentCwd && insideHome(tab.agentCwd)) return { root: tab.agentCwd, key: "sess:" + tab.id + "@" + tab.agentCwd };
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

// rootableCwd: may the tree be rooted at this session cwd? The same verdict
// treeFollowTarget gives a terminal tab (inside $HOME, not empty), for callers
// that root the tree from a session record rather than a tab: the desktop
// drawer. A cwd that /api/fs/list would refuse must never become the root.
function rootableCwd(cwd, home) {
  return treeFollowTarget({ kind: "terminal", id: "", cwd: cwd || "" }, home) !== null;
}

// syncTreeToRoot re-roots the tree, but ONLY when the follow KEY changes — so a
// manual root pick survives until a different tab is focused. The root is a
// plain input now, so any path just overwrites it; there is no option list to
// maintain.
//
// The `!ftRootSel || !root` early return is load-bearing and must stay AHEAD of
// the key write: during boot restore a fresh TerminalTab has no cwd yet, and
// recording the key before there is a root to go with it would tell the guard we
// are already following a tab we never rooted at.
async function syncTreeToRoot(root, key) {
  if (!ftRootSel || !root) return;
  if (key && key === lastSyncedKey) return; // already following this tab (respect manual pick)
  lastSyncedKey = key || null;
  setFtRoot(root);
  // setFtRoot writes the input's value programmatically, which fires no "input"
  // event — so nothing bumps dpSeq or clears dpTimer, and a fuzzy search
  // scheduled just before this tab-focus change lands AFTERWARDS and renders
  // hits underneath a path value. Enter then finds rows, no highlight and a
  // non-fuzzy value, and re-roots at whatever that stale search returned.
  //
  // Not inside setFtRoot: initFileTree calls that before initDirPicker, when
  // dpInput is still null. Here it can ALSO run pre-init — tabs.js's
  // syncFocusedTree reaches this during initFileTree's await, the same restore
  // race the seeding block at the foot of initFileTree exists to repair — so
  // dpClose carries its own uninitialised guard rather than this call site
  // pretending the ordering is guaranteed.
  dpClose();
  await ftLoadRoot();
}

// ftRowFor finds the live row for an absolute path, or null while it is not
// rendered (its parent is collapsed, or still loading).
function ftRowFor(path) {
  if (!ftTreeEl) return null;
  for (const row of ftTreeEl.querySelectorAll(".ft-node")) if (ftRowPaths.get(row) === path) return row;
  return null;
}

// ftSettled waits until no root load is in flight. A load can start while we
// wait (the drawer re-roots as it opens), so it loops until the newest one has
// finished.
async function ftSettled() {
  let p;
  do { p = ftLoading; try { await p; } catch (_) {} } while (p !== ftLoading);
}

// ftReveal shows `path` in the tree: re-root at the file's directory when it is
// outside the current root (the root treeFollowTarget picks for a viewer; never
// one it refuses), open each ancestor in turn through the tree's own expand path
// (each level loads asynchronously, and the next row does not exist until it has
// finished), then select the file's row and bring it into view. Resolves true
// when the row was found. A newer root load part-way (ftGen moved) abandons it.
async function ftReveal(path) {
  if (!ftRootSel || !ftTreeEl || !path) return false;
  await ftSettled();
  if (ancestorsBetween(ftRoot(), path) === null) {
    const target = treeFollowTarget({ kind: "viewer", path, box: "" });
    if (!target) return false;
    // No follow key: this is an explicit pick, so the tree resumes following
    // focus at the next change (syncTreeToRoot clears the guard).
    await syncTreeToRoot(target.root);
  }
  await ftSettled();
  const gen = ftGen;
  const chain = ancestorsBetween(ftRoot(), path);
  if (chain === null) return false;
  for (const dir of chain) {
    const row = ftRowFor(dir);
    const op = row && ftRowOpeners.get(row);
    if (!op) return false;
    await op.setOpen(true);
    if (gen !== ftGen) return false;
  }
  const file = ftRowFor(path);
  if (!file) return false;
  for (const old of ftTreeEl.querySelectorAll(".ft-sel")) old.classList.remove("ft-sel");
  file.classList.add("ft-sel");
  ftRoveTo(file); // the revealed row is the tree's Tab stop (it was tabindex -1 and out of reach)
  file.scrollIntoView({ block: "nearest" });
  file.focus({ preventScroll: true });
  return true;
}

// ftTreeSetup makes `el` the tree: role="tree", a name, the arrow keys on its rows, and the Tab stop following
// focus. Delegated on the one element, so a re-render never has to re-attach anything.
function ftTreeSetup(el) {
  el.setAttribute("role", "tree");
  el.setAttribute("aria-label", "File tree");
  el.addEventListener("keydown", ftTreeKeydown);
  el.addEventListener("focusin", (e) => {
    const row = e.target && e.target.closest ? e.target.closest(".ft-node") : null;
    if (row) ftRoveTo(row);
  });
}

// ftTreeKeydown: the keys of a focused tree row (ftKeyAction decides what each one means). Only a key typed on
// the row itself counts: Enter on its ⋯ presses the ⋯, and a chord (Ctrl, Alt, Meta) belongs to the app.
function ftTreeKeydown(e) {
  const row = e.target;
  if (!row || !row.classList || !row.classList.contains("ft-node")) return;
  if (e.ctrlKey || e.altKey || e.metaKey) return;
  const rows = Array.from(e.currentTarget.querySelectorAll(".ft-node"));
  const r = ftKeyAction(rows.map(ftRowModel), rows.indexOf(row), e.key, e.shiftKey);
  if (!r.action) return;
  e.preventDefault(); // also keeps the browser's own context menu from stacking on the row's
  if (r.action === "focus") {
    rows[r.index].focus();
  } else if (r.action === "expand" || r.action === "collapse") {
    const op = ftRowOpeners.get(row);
    if (op) op.setOpen(r.action === "expand");
  } else if (r.action === "open") {
    row.click();
  } else if (r.action === "menu") {
    const more = row.querySelector(":scope > .ft-more");
    if (more) more.click();
  }
}

// ftMenuKeydown: the open row menu's keys, on the document. Escape closes it (a press that closes a menu is
// marked used, preventDefault, so the maximized drawer's Escape in app.js leaves it alone) and gives focus
// back to the row. The arrow keys, Home and End walk the kit's rows, but only while focus is inside the menu:
// a user who tabbed away keeps their arrows.
function ftMenuKeydown(e) {
  const menu = document.getElementById("ft-menu");
  const open = !!menu && menu.classList.contains("open");
  if (e.key === "Escape") {
    if (open) e.preventDefault();
    closeRowMenu(true);
    return;
  }
  if (open && menu.contains(document.activeElement)) kitMenuKeys(menu, e);
}

async function initFileTree() {
  ftRootSel = document.getElementById("ft-root-input");
  ftTreeEl = document.getElementById("filetree");
  if (!ftRootSel || !ftTreeEl) return;
  ftTreeSetup(ftTreeEl); // before the first await: the rows a racing restore draws are already in a tree

  ftExpanded = loadExpanded(); // which folders to auto-open as the tree renders

  let ws = [];
  try {
    ws = (await api("/api/workspaces")) || [];
    window.HOME_DIR = (ws[0] && ws[0].path) || ""; // first entry is "~ (home)": used to expand ~/ in clicked links
    // Before Task 5 the <select>'s first <option> (built from this same
    // response) was implicitly its value, so the tree defaulted to home. That
    // building was deleted along with the <select>, and nothing replaced the
    // default it produced as a side effect — leaving ftRootPath "" until a
    // followed tab or a manual pick set it, which paints .ft-err on any first
    // load that has neither (a fresh page, or a boot that loses the cwd race).
    if (window.HOME_DIR) setFtRoot(window.HOME_DIR);
  } catch (_) {}

  // A picked directory re-roots the tree but does NOT change which TAB we're
  // following, so it survives until a different tab is focused. workspaces is
  // passed in because this function already awaited /api/workspaces above —
  // letting the picker fetch it again unawaited would leave a focus before
  // that second fetch lands rendering zero rows with no retry.
  initDirPicker({
    workspaces: ws,
    onPick: async (path) => { setFtRoot(path); await ftLoadRoot(); },
    onOpenFile: (path) => openFile(path),
  });

  // New folder in the root currently shown: no row can reach the root itself.
  const newdir = document.getElementById("ft-newdir");
  if (newdir) newdir.onclick = () => promptMkdir(ftRoot());

  // One document-level closer for the row menu, like the spawn menu's.
  document.addEventListener("click", (e) => {
    const menu = document.getElementById("ft-menu");
    if (menu && menu.classList.contains("open") && !menu.contains(e.target)) closeRowMenu();
  });
  document.addEventListener("keydown", ftMenuKeydown);

  // Manual re-read. The tree loads lazily and never polls, so anything the AGENT
  // writes, which is most of what changes in this app, is invisible until the
  // root is re-listed. Without this the only ways were re-rooting or reloading
  // the whole page. Disabled while in flight so an impatient double click cannot
  // stack requests (ftLoadRoot's generation guard would drop one anyway).
  const refresh = document.getElementById("ft-refresh");
  if (refresh) {
    refresh.onclick = async () => {
      refresh.disabled = true;
      try { await ftLoadRoot({ fresh: true }); } finally { refresh.disabled = false; }
    };
  }
  // Initial root: whatever the focused TAB resolves to — a terminal's cwd or a
  // viewer's directory — else home. focusedTerminal() was wrong here: a layout
  // restored onto a viewer booted at home with the follow-guard unset.
  // A restore that won the race against /api/workspaces above may already have
  // rooted the tree against an empty HOME_DIR. Clearing the guard makes this
  // seeding authoritative so it re-asserts the root once workspaces are known.
  // The cost is one redundant listing in the common case.
  // Gated on a non-empty workspace list: if /api/workspaces threw above,
  // HOME_DIR is unset, so the containment check is disabled and seeding could
  // root outside home and paint ft-err. That state is already broken; it
  // should stay quiet rather than add an error to it.
  if (ws.length) {
    lastSyncedKey = null;
    const t = (typeof focusedTab === "function") ? focusedTab() : null;
    const target = treeFollowTarget(t);
    if (target) await syncTreeToRoot(target.root, target.key);
    else await ftLoadRoot();
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    iconFor, treeBranch, treeFollowTarget, rootableCwd, actionsLabel,
    listingReadOnly, rowMenuIds, rowMenuLabel, openRowMenu, ftApplyReadOnly, ftRenderDir,
    ftKeyAction, ftMenuGroups,
  };
}
