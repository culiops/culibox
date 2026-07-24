"use strict";

// File tree: lazy directory browser rooted at a workspace. Clicking a file opens
// it in the focused cell (openFile). A paste-path input opens any absolute path.
async function initFileTree() {
  const rootSel = document.getElementById("ft-root");
  const pathInp = document.getElementById("ft-path");
  const treeEl = document.getElementById("filetree");
  if (!rootSel || !pathInp || !treeEl) return;

  try {
    const ws = (await api("/api/workspaces")) || [];
    rootSel.replaceChildren(...ws.map((w) => {
      const o = document.createElement("option");
      o.value = w.path; o.textContent = w.label;
      return o;
    }));
  } catch (_) {}

  const renderDir = async (path, container, depth) => {
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
            await renderDir(full, childBox, depth + 1);
          } else if (childBox) { childBox.remove(); childBox = null; }
        };
      } else {
        row.classList.add("file");
        row.onclick = () => openFile(full);
      }
      container.append(row);
    }
  };

  const loadRoot = async () => {
    treeEl.replaceChildren();
    await renderDir(rootSel.value, treeEl, 0);
  };
  rootSel.onchange = loadRoot;
  pathInp.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); const v = pathInp.value.trim(); if (v) { openFile(v); pathInp.value = ""; } }
  };

  // default root = the focused terminal's cwd if it matches a workspace option; else home.
  try {
    const cwd = focusedCell && focusedCell.activeTab && focusedCell.activeTab.kind === "terminal" && focusedCell.activeTab.cwd;
    if (cwd) for (const o of rootSel.options) if (o.value === cwd) { rootSel.value = cwd; break; }
  } catch (_) {}
  if (rootSel.options.length) await loadRoot();
}
