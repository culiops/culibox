"use strict";

// File tree: lazy directory browser rooted at a workspace. Clicking a file opens
// it in the focused cell (openFile). A paste-path input opens any absolute path.
// The root auto-follows the focused session's cwd (see syncTreeToSession), while
// a manual pick from the dropdown survives until a *different* session is focused.

let ftRootSel = null, ftTreeEl = null;
let lastSyncedSessionId = null; // which session the tree is currently following

async function ftRenderDir(path, container, depth) {
  let data;
  try { data = await api("/api/fs/list?path=" + encodeURIComponent(path)); }
  catch (e) { const d = document.createElement("div"); d.className = "ft-err"; d.textContent = e.message; container.append(d); return; }
  for (const ent of data.entries) {
    const full = path.replace(/\/$/, "") + "/" + ent.name;
    const row = document.createElement("div");
    row.className = "ft-node";
    row.style.paddingLeft = (depth * 12 + 6) + "px";
    row.textContent = (ent.is_dir ? "▸ " : "  ") + ent.name;
    if (ent.is_dir) {
      let open = false, childBox = null;
      row.classList.add("dir");
      row.onclick = async () => {
        open = !open;
        row.textContent = (open ? "▾ " : "▸ ") + ent.name;
        if (open) {
          childBox = document.createElement("div");
          row.after(childBox);
          await ftRenderDir(full, childBox, depth + 1);
        } else if (childBox) { childBox.remove(); childBox = null; }
      };
    } else {
      row.classList.add("file");
      row.onclick = () => openFile(full);
    }
    container.append(row);
  }
}

async function ftLoadRoot() {
  if (!ftRootSel || !ftTreeEl) return;
  ftTreeEl.replaceChildren();
  await ftRenderDir(ftRootSel.value, ftTreeEl, 0);
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
  pathInp.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); const v = pathInp.value.trim(); if (v) { openFile(v); pathInp.value = ""; } }
  };

  // initial root: the focused terminal's cwd if one is already open, else home.
  const term = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  if (term && term.cwd) await syncTreeToSession(term.cwd, term.id);
  else if (ftRootSel.options.length) await ftLoadRoot();
}
