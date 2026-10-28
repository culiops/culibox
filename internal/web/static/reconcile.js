"use strict";

// Keyed reconcile for the desktop session list. The list used to be rebuilt
// with replaceChildren every 3s poll, which detached every row — and removing a
// node drops keyboard focus inside it (a focused ⋯ lost focus every 3s).
// Instead: keep a row whose signature is unchanged, replace only a row whose
// signature moved, fix order with insertBefore only where it differs.
//
// Rows are keyed by data-sid. An open answer block (`li.peek`) is the row's next
// sibling, not a child, so it must move with its row. Anything else in the list
// — the "No sessions yet" placeholder, mobile group headers, a mobile card that
// has no signature — is purged or replaced.

function isPeekNode(n) {
  return !!(n && n.classList && n.classList.contains("peek"));
}

function peekAfterRow(n) {
  const x = n && n.nextElementSibling;
  return isPeekNode(x) ? x : null;
}

// reconcileRows makes `ul` show exactly `sessions`, in order. render(s) builds a
// fresh row; sigOf(s) is the string of everything that row renders. Returns a
// Map of session id -> its live row node.
function reconcileRows(ul, sessions, render, sigOf) {
  const list = Array.isArray(sessions) ? sessions : [];
  const want = new Set(list.map((s) => s.id));
  const have = new Map();

  // 1. Purge every node that is not a wanted, first-seen row. A peek survives
  //    only directly behind a kept row; one whose row was just removed (or that
  //    has no row before it) goes too.
  let prevKept = null;
  for (const n of Array.from(ul.children)) {
    if (isPeekNode(n)) {
      if (!prevKept) n.remove();
      prevKept = null;
      continue;
    }
    const sid = n.dataset && n.dataset.sid;
    if (sid && want.has(sid) && !have.has(sid)) {
      have.set(sid, n);
      prevKept = n;
      continue;
    }
    n.remove();
    prevKept = null;
  }

  // 2. Replace a row whose signature moved; create missing rows at the end
  //    (step 3 puts them in place). A replaced row's peek is dropped: its
  //    closures belong to the old node; app.js reopens it on the fresh one.
  for (const s of list) {
    const sig = String(sigOf(s));
    const old = have.get(s.id);
    if (old && old.dataset.sig === sig) continue;
    const fresh = render(s);
    fresh.dataset.sig = sig;
    if (old) {
      const pk = peekAfterRow(old);
      if (pk) pk.remove();
      ul.replaceChild(fresh, old);
    } else {
      ul.appendChild(fresh);
    }
    have.set(s.id, fresh);
  }

  // 3. Order. `cursor` is the first node not yet known to be in place; a row
  //    already at the cursor is never touched (never detached).
  let cursor = ul.firstElementChild;
  for (const s of list) {
    const row = have.get(s.id);
    const pk = peekAfterRow(row);
    if (row !== cursor) {
      ul.insertBefore(row, cursor);
      if (pk) ul.insertBefore(pk, cursor);
    }
    cursor = (pk || row).nextElementSibling;
  }
  return have;
}

// rowSig is the signature of everything a desktop row or needs-you card
// renders: the session's own fields plus page-local state (`local`) the row
// reads — a teardown in flight, whether this page holds the terminal, the
// context %, the tier label, a reply in flight, and a nested shell's "shell N"
// label (which moves when a sibling appears or goes). A field the row does not
// render must NOT be here, or the row re-renders (and drops focus) for nothing.
function rowSig(s, local) {
  const v = s || {};
  const l = local || {};
  return JSON.stringify([
    v.id, v.parent_id || "", l.shellLabel || "", v.name || "", v.agent || "", v.cwd || "", v.runtime || "",
    v.activity || "", v.need || "", v.ask || "", v.ask_full || "", v.summary || "",
    !!v.replyable, !!v.attached, !!v.claude, !!v.exited,
    !!l.killing, !!l.heldHere, typeof l.pct === "number" ? l.pct : null,
    l.boxRuntime || "", !!l.replies, !!l.sending, !!l.expanded, !!l.answering, !!l.elev,
    // The AWS chip (label, prod, the amber write state) and the SSO card's text
    // read the binding; elevated_until appears and goes with a grant and a drop.
    l.sso || "", v.aws ? JSON.stringify(v.aws) : "",
    // Named connections: the chip (count, prod, write) and the SSO text read them.
    JSON.stringify(v.aws_connections),
    // The row's "$": absent (history not fully known) is not the same as 0.
    typeof v.cost_usd === "number" ? v.cost_usd : null,
  ]);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { reconcileRows, rowSig };
}
