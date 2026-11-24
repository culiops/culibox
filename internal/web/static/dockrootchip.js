"use strict";

// The explorer's root header chip (design P4.10; founder pick B, 2026-11-23): one short word after the root's name, "worktree"
// when the tree's root is in a linked worktree, "clone" when it is a clone-mode session's workspace, so the user always knows
// which checkout they are reading. Its title says of what (the main checkout or the clone's source) and the branch. The data
// rides on what the page already has: the status reply's worktreeOf (the server reads it from the files git keeps, no git
// run) and the session record's clone_of. Desktop only (the root header is the dock's).

// rootChipFor: the chip for {root, status, session, short?}, or null. `status` is the root's last status reply (live or kept),
// `session` the dock session's record ({cwd, clone_of}); `short` writes a path the way the page shows them (~ for home).
// A worktree wins over a clone: it is the checkout on screen.
function rootChipFor(o) {
  const root = o && typeof o.root === "string" ? o.root : "";
  if (!root) return null;
  const short = typeof o.short === "function" ? o.short : (p) => p;
  const st = o.status && typeof o.status === "object" ? o.status : null;
  const branch = st && typeof st.branch === "string" && st.branch ? "\nBranch: " + st.branch : "";
  if (st && typeof st.worktreeOf === "string" && st.worktreeOf) {
    return { text: "worktree", title: "Linked worktree of " + short(st.worktreeOf) + branch };
  }
  const s = o.session;
  if (s && typeof s.clone_of === "string" && s.clone_of && typeof s.cwd === "string" && s.cwd) {
    const cwd = s.cwd.length > 1 ? s.cwd.replace(/\/+$/, "") : s.cwd;
    if (root === cwd || root.indexOf(cwd + "/") === 0) return { text: "clone", title: "Clone of " + short(s.clone_of) + branch };
  }
  return null;
}

// dockRootChipPaint: draw the chip for the tree's root (dockchanges.js dkcPaintRoot calls it with every root header paint).
// The status is the live one for this root, else the one kept from the last time (gitlast.js), so a reload shows it at once.
function dockRootChipPaint(root) {
  const el = document.getElementById("dock-root-chip");
  if (!el) return;
  const cur = typeof gitStatusCurrent === "function" ? gitStatusCurrent(root) : null;
  const kept = !cur && typeof gitLastGet === "function" && root ? gitLastGet(root) : null;
  const sid = typeof drawerState !== "undefined" && drawerState ? drawerState.sessionId : null;
  const session = sid && typeof lastSessions !== "undefined" ? (lastSessions || []).find((x) => x.id === sid) || null : null;
  const chip = rootChipFor({
    root, status: cur ? cur.status : kept ? kept.status : null, session,
    short: typeof shortPath === "function" ? shortPath : undefined,
  });
  el.hidden = !chip;
  if (el.textContent !== (chip ? chip.text : "")) el.textContent = chip ? chip.text : "";
  el.title = chip ? chip.title : "";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { rootChipFor };
}
