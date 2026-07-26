"use strict";

// File tree: lazy directory browser rooted at a workspace. Clicking a file opens
// it in the focused cell (openFile). A paste-path input opens any absolute path.
// The root auto-follows the focused session's cwd (see syncTreeToSession), while
// a manual pick from the dropdown survives until a *different* session is focused.

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
    // button — writing row.textContent would wipe any child element.
    const label = document.createElement("span");
    label.className = "ft-name";
    label.textContent = (ent.is_dir ? "▸ " : "  ") + ent.name;
    row.append(label);
    const dl = document.createElement("button");
    dl.className = "ft-dl";
    dl.textContent = "⤓";
    // The directory tooltip discloses the symlink-skip up front: a tarball
    // quietly missing symlinked content is the kind of surprise you only find
    // out about after you needed it.
    dl.title = ent.is_dir
      ? "Download " + ent.name + ".tar.gz (symlinks are skipped)"
      : "Download " + ent.name;
    dl.onclick = (e) => { e.stopPropagation(); downloadPath(full, ent.is_dir); };
    row.append(dl);
    // Rename and delete live behind a menu, not as bare buttons: a destructive
    // control one finger-width from ⤓ is a mis-tap, and this delete cannot be
    // undone.
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
// Two roots can be in flight at once — boot loads home while a session's cwd
// arrives and re-roots — and ftRenderDir appends after its own await, so painting
// straight into the tree let a superseded load leave its rows behind (the tree
// showed home while the picker said the session's directory).
async function ftLoadRoot() {
  if (!ftRootSel || !ftTreeEl) return;
  const gen = ++ftGen;
  const box = document.createElement("div");
  await ftRenderDir(ftRootSel.value, box, 0);
  if (gen !== ftGen) return; // a newer root started meanwhile — drop this render
  ftTreeEl.replaceChildren(...box.childNodes);
}

// --- row actions -------------------------------------------------------------

// openRowMenu shows what can be done to one entry, positioned under the ⋯ that
// opened it (same placement maths as the spawn menu in app.js).
function openRowMenu(anchor, path, name, isDir) {
  const menu = document.getElementById("ft-menu");
  if (!menu) return;
  const items = [];
  if (isDir) items.push(["New folder here…", () => promptMkdir(path)]);
  items.push(["Rename…", () => promptRename(path, name)]);
  items.push(["Delete…", () => confirmDelete(path, name, isDir)]);
  menu.replaceChildren(...items.map(([label, run]) => {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = label;
    b.onclick = () => { closeRowMenu(); run(); };
    return b;
  }));
  menu.classList.add("open"); // must be visible before it can be measured
  const r = anchor.getBoundingClientRect();
  const w = menu.offsetWidth;
  menu.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, r.right - w)) + "px";
  // Rows sit anywhere in the viewport, unlike the spawn menu's header anchor, so
  // a menu opened near the bottom must flip above its row instead of clipping.
  const h = menu.offsetHeight;
  const below = r.bottom + 4;
  menu.style.top = (below + h <= window.innerHeight - 8 ? below : Math.max(8, r.top - h - 4)) + "px";
}

function closeRowMenu() {
  const menu = document.getElementById("ft-menu");
  if (menu) menu.classList.remove("open");
}

async function promptMkdir(dir) {
  const raw = prompt("New folder name:");
  if (raw === null) return;
  const name = raw.trim();
  if (!name) return;
  const res = await fsMkdir(dir, name);
  if (!res.ok) { showToast("Could not create: " + fsErrText(res), { error: true }); return; }
  showToast("Created " + name);
  ftLoadRoot();
}

async function promptRename(path, name) {
  const raw = prompt("Rename to:", name);
  if (raw === null) return;
  const next = raw.trim();
  if (!next || next === name) return;
  let res = await fsRename(path, next);
  // A protected folder must not be renamable around its delete gate.
  if (res.status === 409 && res.data && res.data.confirm_name) {
    const typed = prompt('This folder is protected. Type "' + res.data.confirm_name + '" to rename it:');
    if (typed !== res.data.confirm_name) return;
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
    const typed = prompt('Deleting "' + d.confirm_name + '"' + (inside ? " — " + inside : "") +
      ". This cannot be undone.\n\nType the folder name to confirm:");
    if (typed !== d.confirm_name) return;
    res = await fsDelete(path, { recursive: true, confirmName: typed, entries: d.entries || 0 });
  } else if (res.status === 409 && res.data) {
    const d = res.data;
    if (!confirm(deleteConfirmText(name, isDir, d))) return;
    res = await fsDelete(path, { recursive: true, entries: d.entries || 0 });
  }
  if (!res.ok) { showToast("Could not delete: " + fsErrText(res), { error: true }); return; }
  if (typeof closeViewersFor === "function") closeViewersFor(path);
  showToast("Deleted " + name);
  ftLoadRoot();
}

// syncTreeToSession re-roots the tree at a session's cwd, but ONLY when a
// *different* session becomes focused (sessionId changes) — so a manual root
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
    window.HOME_DIR = (ws[0] && ws[0].path) || ""; // first entry is "~ (home)" — used to expand ~/ in clicked links
    ftRootSel.replaceChildren(...ws.map((w) => {
      const o = document.createElement("option");
      o.value = w.path; o.textContent = w.label;
      return o;
    }));
  } catch (_) {}

  // A manual dropdown pick re-roots the tree but does NOT change which session
  // we're following, so it survives until a different session is focused.
  ftRootSel.onchange = ftLoadRoot;

  // New folder in the root currently shown — no row can reach the root itself.
  const newdir = document.getElementById("ft-newdir");
  if (newdir) newdir.onclick = () => promptMkdir(ftRootSel.value);

  // One document-level closer for the row menu, like the spawn menu's.
  document.addEventListener("click", (e) => {
    const menu = document.getElementById("ft-menu");
    if (menu && menu.classList.contains("open") && !menu.contains(e.target)) closeRowMenu();
  });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeRowMenu(); });

  // Manual re-read. The tree loads lazily and never polls, so anything the AGENT
  // writes — which is most of what changes in this app — is invisible until the
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
