"use strict";

// The private-copy conversion (#371): a git repo boxes only as a private copy (clone mode), so a boxed in-place start of a git folder
// is refused with `needs_copy`, and the New session dialog offers the conversion instead of a dead end. This file holds
//   - the pure half: what the dialog says (copyDialogModel), the profile change and the inplace_history question (cdSaveWithAck),
//     node-tested;
//   - the DOM half after it: the dialog (askDialog with the lk-* cards' classes, so desktop and phone both have a style), the
//     estimate fetches and the profile save. Every server string reaches the page as text, never as markup.
//
// Loaded as a classic script, so top-level names are shared globals: they are cd-/copyDialog-prefixed on purpose.

const CD_NOT_SEE = "The box will not see uncommitted, untracked or gitignored files.";
const CD_LFS = "Git LFS files arrive as pointer files.";
const CD_NO_DISK = "Not enough free disk";
const CD_FOLDER_FIRST = "Set this profile's folder to the repo first.";
const CD_HOME = "This folder is inside the git repo at your home directory. Pick Host, or move the folder out of that repo.";
const CD_HOME_UNKNOWN = "CuliBox could not tell whether this repo is your home directory. Pick Host, or update CuliBox.";
const CD_HOST_PROFILE = "This profile runs on Host. A private copy runs in a box: switch the profile to Box first.";
const CD_BUILT_IN = "This profile is built in and cannot be changed. Duplicate it in Settings first.";
const CD_CONFIG = "This profile is defined in the config file and cannot be changed here. Duplicate it in Settings first.";
const CD_SHELL = "This is the profile + Shell starts. As a private copy of one repo, every later + Shell would open in that copy: use another profile.";
const CD_BODY = "The agent works on its own copy of the repo. Its work comes home as box/<branch> branches in your repo, for you to review and merge. Your folder is untouched.";
const CD_WORKTREE = "A linked git worktree cannot be boxed or copied.";
const CD_ACK_LABEL = "The .git folder here existed before any box ran in this folder.";

// cdFmt: a byte count as people read it (upload.js's humanBytes, a global in the page and its export under node).
function cdFmt(n) {
  return (typeof humanBytes === "function" ? humanBytes : require("./upload.js").humanBytes)(n);
}

function cdNorm(p, home) {
  let s = String(p == null ? "" : p).trim();
  if (s === "") return "";
  if (home && (s === "~" || s.startsWith("~/"))) s = String(home) + s.slice(1);
  s = s.replace(/\/+$/, "");
  return s === "" ? "/" : s;
}

// cdUnder: p is root or a folder inside it (a sibling that shares a name prefix is not).
function cdUnder(p, root, home) {
  const a = cdNorm(p, home);
  const b = cdNorm(root, home);
  return a !== "" && b !== "" && (a === b || a.startsWith(b === "/" ? "/" : b + "/"));
}

// cdSizeLine: "Copy ≈ <git+tree> · free <free>". A partial walk (a very large repo) only knows a lower bound: "≥".
function cdSizeLine(est) {
  const total = (Number(est.git_bytes) || 0) + (Number(est.tree_bytes) || 0);
  const free = typeof est.free_bytes === "number" ? " · free " + cdFmt(est.free_bytes) : "";
  return "Copy " + (est.partial ? "≥" : "≈") + " " + cdFmt(total) + free;
}

// cdChangesLine: what Start does to the profile itself, said before it is done (the save is permanent until the user edits it).
function cdChangesLine(name, repo) {
  return "This changes the profile " + String(name || "") + ": it will work on private copies of " + String(repo || "") + " from now on.";
}

// copyDialogModel: what the conversion dialog shows and whether Start may go on.
//   refusal  the needs_copy body ({error, repo, suggested, worktree, inside, lfs}) or cdRefusalFromCheck's likeness
//   estimate POST /api/clone/estimate's answer, null while it is out or when it was refused (ctx.estimateError says why)
//   profile  the profile that would be converted (a ProfileView)
//   ctx      {home, ackAsked, estimateError, profileName, shellProfile}; home only expands a ~ in the profile's own folder;
//            profileName is the profile the save changes (the profile's own name when absent); shellProfile is the one + Shell
//            starts (window.shellProfile), which is never converted
// The refusal's git_home is the SERVER's verdict that the repo is the home directory (resolved paths): the page never compares
// paths for it, and a refusal without the flag (an older server) is not converted.
// -> {repo, suggested, body, lines, canConvert, needsAck, worktreeOnly, hostOnly, loading}. `hostOnly`: nothing here can be
// converted, so the only way on is Host (a linked worktree, or a folder inside the repo at the home directory). `body` is the
// dialog's text: what a copy is, and which profile it changes.
function copyDialogModel(refusal, estimate, profile, ctx) {
  const r = refusal || {};
  const p = profile || {};
  const c = ctx || {};
  const name = c.profileName || p.name || "";
  const m = {
    repo: r.repo || "", suggested: r.suggested || "", body: CD_BODY + " " + cdChangesLine(name, r.repo), lines: [], canConvert: false,
    needsAck: !!c.ackAsked, worktreeOnly: !!r.worktree, hostOnly: false, loading: false,
  };
  if (r.worktree) {
    m.hostOnly = true;
    m.body = "";
    m.lines = [r.error || CD_WORKTREE];
    return m;
  }
  // A repo at ~ (a dotfiles repo) would copy the whole home directory. Only the server knows (git_home); no flag, no conversion.
  if (r.git_home !== false) {
    m.hostOnly = true;
    m.body = "";
    m.lines = [r.git_home === true ? CD_HOME : CD_HOME_UNKNOWN];
    return m;
  }
  const est = estimate || null;
  if (est) {
    m.lines.push(cdSizeLine(est));
    if (est.enough === false) m.lines.push(CD_NO_DISK);
  } else if (c.estimateError) {
    m.lines.push(String(c.estimateError));
  } else {
    m.loading = true;
  }
  m.lines.push(CD_NOT_SEE);
  if (r.lfs || (est && est.lfs)) m.lines.push(CD_LFS);
  const blocks = [];
  if (p.editable === false) blocks.push(p.source === "config" ? CD_CONFIG : CD_BUILT_IN);
  // + Shell starts this profile in whatever folder it is asked for: made a copy of one repo, every later shell would open there.
  if (c.shellProfile && name === c.shellProfile) blocks.push(CD_SHELL);
  if (p.runtime === "host") blocks.push(CD_HOST_PROFILE);
  // A profile that already makes copies of another repo would have that replaced by the save: a separate profile is the way.
  if (typeof p.clone_of === "string" && p.clone_of.trim() !== "" && cdNorm(p.clone_of, c.home) !== cdNorm(r.repo, c.home)) {
    blocks.push("This profile already makes private copies of " + p.clone_of + ". Use a separate profile for this repo.");
  }
  // The profile's folder is what becomes the copy's place, so one that is elsewhere is the user's to settle first.
  if (typeof p.default_cwd === "string" && p.default_cwd.trim() !== "" && !cdUnder(p.default_cwd, r.repo, c.home)) blocks.push(CD_FOLDER_FIRST);
  m.lines.push(...blocks);
  m.canConvert = !!est && est.enough !== false && blocks.length === 0;
  return m;
}

// cdEstimateBody: the POST /api/clone/estimate body. `known` is an earlier full estimate of the same repo ({git_bytes, tree_bytes}):
// then only the free space at dir is asked (free_only), judged by the server against those sizes; the repo is not walked again.
function cdEstimateBody(repo, dir, known) {
  if (!known) return { of: repo, dir };
  return { of: repo, dir, free_only: true, git_bytes: Number(known.git_bytes) || 0, tree_bytes: Number(known.tree_bytes) || 0 };
}

// cdMergeFree: a free-space-only answer over the cached sizes: the sizes, partial and lfs are the full estimate's, the free space
// and the verdict the new answer's.
function cdMergeFree(known, got) {
  return Object.assign({}, known, { free_bytes: got.free_bytes, enough: got.enough });
}

// cdRefusalFromCheck: the refusal a spawn would get, built from /api/workspace/check's answer (asked with the profile's name), for the
// New session card path (nothing was refused yet). The suggested folder and the home verdict are the server's. null for a folder
// that is not git.
function cdRefusalFromCheck(check) {
  if (!check || typeof check.git_repo !== "string" || check.git_repo === "") return null;
  return {
    error: "", code: "needs_copy", repo: check.git_repo,
    suggested: typeof check.suggested === "string" ? check.suggested : "",
    worktree: check.git_worktree === true, inside: check.git_inside === true, lfs: false,
    git_home: typeof check.git_home === "boolean" ? check.git_home : undefined,
  };
}

// cdSavedDoc: the stored profile as a save that makes it a private copy: clone_of the repo, default_cwd the copy. rev and schema
// are the page's to add (withRev, withSchema); ack is the user's say-so the server asked for (ack_inplace_history).
function cdSavedDoc(doc, repo, dir, ack) {
  const out = Object.assign({}, doc, { clone_of: repo, default_cwd: dir });
  delete out.rev;
  delete out.schema;
  if (ack) out.ack_inplace_history = true;
  return out;
}

// cdIsInplaceHistory: the save was refused because the .git of clone_of may have been written by a box (400 inplace_history).
function cdIsInplaceHistory(err) {
  return !!err && err.status === 400 && (err.code === "inplace_history" || (!!err.body && err.body.code === "inplace_history"));
}

// cdSaveWithAck: run a profile save (send(ack) -> promise) and, when the server asks about the .git history, put the server's words
// to the user (ask(message) -> promise of a yes) and send the same save again with the ack. A no throws the refusal as it came, so
// the caller's own error line shows it; a second refusal after the ack is not asked again.
async function cdSaveWithAck(send, ask) {
  try {
    return await send(false);
  } catch (e) {
    if (!cdIsInplaceHistory(e)) throw e;
    if (!(await ask(e.message))) throw e;
    return await send(true);
  }
}

// --- the DOM part -------------------------------------------------------------------------------------------------
// Reads the page's globals (askDialog and askConfirm from shell.js, api and $ from app.js, withRev/withSchema/pfSchema from
// profileform.js, refreshProfilesQuiet) and draws into #ask-modal.

function cdEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// cdAckBox: the "the .git existed before any box ran here" checkbox row. {node, input}.
function cdAckBox() {
  const input = cdEl("input");
  input.type = "checkbox";
  const label = cdEl("label", "lk-check");
  label.append(input, cdEl("span", "", CD_ACK_LABEL));
  const wrap = cdEl("div", "lk-trust");
  wrap.append(label);
  return { node: wrap, input };
}

// cdAskAck: the server refused a save with inplace_history. Its words, a box to tick (OK stays off until it is) and the same
// save again on yes. A question already open reads as no, which askDialog does too.
function cdAskAck(message) {
  const d = document.getElementById("ask-modal");
  if (!d || d.open) return Promise.resolve(false);
  const ack = cdAckBox();
  const p = askConfirm({
    title: "Use this folder's existing .git?", body: String(message || ""), extra: [ack.node], confirmLabel: "Confirm and save",
    focusCancel: true, cls: "lk-ask", wide: false,
  });
  const ok = document.getElementById("ask-ok");
  ok.disabled = true;
  ack.input.addEventListener("change", () => { ok.disabled = !ack.input.checked; });
  return p;
}

// cdSendProfile: a profile editor's save (POST or PUT of `body` to `path`) that answers the server's inplace_history question: the
// server's words and a box to tick, then the same save again with ack_inplace_history. Every editor sends its save through this, so
// a profile whose clone_of is a folder boxed in place before the record kept a flag can still be saved from any of them.
function cdSendProfile(path, method, body) {
  return cdSaveWithAck((ack) => api(path, {
    method, headers: { "Content-Type": "application/json" },
    body: JSON.stringify(ack ? Object.assign({}, body, { ack_inplace_history: true }) : body),
  }), cdAskAck);
}

// cdConvertProfile: make the profile a private copy: read it, set clone_of and default_cwd, save it. cdSaveWithAck's `send`.
async function cdConvertProfile(name, repo, dir, ack) {
  const path = "/api/profiles/" + encodeURIComponent(name);
  const doc = await api(path);
  const body = withSchema(withRev(cdSavedDoc(doc, repo, dir, ack), doc), pfSchema.get());
  return api(path, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
}

// cdEstimateTimeoutMs: how long one estimate may take before the dialog says so (the server stops the walk at 5 s and the listing
// of a very large repo gets the rest of its 30 s; a location edit asks only for the free space).
const CD_ESTIMATE_TIMEOUT_MS = 15000;

// copyDialogAsk: the conversion dialog once. o = {profile, profileName, home, dir, ackAsked, note, hostOk, sizes}. Resolves
// {outcome: "confirm"|"host"|"cancel", dir, ack}. "host" only for a folder nothing here can convert, when Host is on offer.
// The estimate is read for the location in the field, again 400 ms after it is edited; Start stays off until it says the copy fits.
// o.sizes (a Map, repo -> full estimate) keeps the repo's measured sizes across the dialog's openings: once the repo is measured, an
// edit of the location asks only for the free space there. A newer request aborts the one still out, and closing the dialog
// aborts the last.
async function copyDialogAsk(refusal, o) {
  // Another question is open: askDialog would answer cancel, and the controls below would be that question's.
  const card = document.getElementById("ask-modal");
  if (card && card.open) return { outcome: "cancel", dir: "", ack: false };
  const profile = o.profile || {};
  const sizes = o.sizes instanceof Map ? o.sizes : new Map();
  const shellProfile = typeof window !== "undefined" && window && typeof window.shellProfile === "string" ? window.shellProfile : "";
  const ctx = { home: o.home || "", ackAsked: !!o.ackAsked, estimateError: "", profileName: o.profileName || "", shellProfile };
  let est = null;
  const first = copyDialogModel(refusal, null, profile, ctx);
  const nodes = [];
  const repoBox = cdEl("div", "lk-url", refusal.repo);
  nodes.push(cdEl("p", "lk-meta", first.hostOnly ? "This folder is in a git repo:" : "A git repo boxes as a private copy of it:"), repoBox);
  const lines = cdEl("div", "cd-lines");
  nodes.push(lines);
  // The note (a refused save, the server's question) only when there is one: an empty warning strip reads as a broken warning.
  if (o.note) {
    const note = cdEl("div", "lk-warn");
    note.append(cdEl("span", "lk-wt", String(o.note)));
    nodes.push(note);
  }
  const ack = first.needsAck ? cdAckBox() : null;
  if (ack) nodes.push(ack.node);

  const input = () => document.getElementById("ask-input");
  const ok = () => document.getElementById("ask-ok");
  const draw = () => {
    const m = copyDialogModel(refusal, est, profile, ctx);
    const kids = m.lines.map((t) => cdEl("p", "lk-meta", t));
    if (m.loading) kids.push(cdEl("p", "lk-meta", "Measuring the copy…"));
    lines.replaceChildren(...kids);
    if (!first.hostOnly && ok()) ok().disabled = !(m.canConvert && (!ack || ack.input.checked));
  };
  let seq = 0;
  let timer = null;
  let closed = false;
  let inflight = null; // the AbortController of the request still out
  const measure = async () => {
    if (closed) return;
    const mine = ++seq;
    if (inflight) inflight.abort();
    const ac = new AbortController();
    inflight = ac;
    let late = false;
    const deadline = setTimeout(() => { late = true; ac.abort(); }, CD_ESTIMATE_TIMEOUT_MS);
    const dir = input() ? input().value.trim() : "";
    const known = sizes.get(refusal.repo) || null;
    est = null;
    ctx.estimateError = "";
    let got = null;
    let err = "";
    try {
      got = await api("/api/clone/estimate", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(cdEstimateBody(refusal.repo, dir, known)), signal: ac.signal,
      });
    } catch (e) {
      err = late ? "Measuring the copy took too long. Edit the location to try again." : e && e.message ? e.message : "Could not measure the copy.";
    } finally {
      clearTimeout(deadline);
      if (inflight === ac) inflight = null;
    }
    if (closed || mine !== seq) return; // the dialog is gone, or the location changed again
    if (got && !known) sizes.set(refusal.repo, got);
    est = got && known ? cdMergeFree(known, got) : got;
    ctx.estimateError = err;
    draw();
  };
  const onInput = () => {
    est = null;
    ctx.estimateError = "";
    draw(); // no estimate for this location yet: Start is off
    clearTimeout(timer);
    timer = setTimeout(measure, 400);
  };

  const hostOffer = first.hostOnly && !!o.hostOk;
  const opts = {
    cls: "lk-ask", wide: false, extra: nodes, hint: "",
    title: first.hostOnly ? "This folder cannot be copied" : "Work on a private copy?",
    body: first.body,
    confirmLabel: hostOffer ? "Use Host" : first.hostOnly ? "Close" : "Make the copy and start",
    cancelLabel: first.hostOnly ? "Close" : "Cancel", info: first.hostOnly && !hostOffer,
    input: !first.hostOnly, label: "Copy location", value: o.dir || refusal.suggested || "",
  };
  const asked = askDialog(opts);
  // askDialog has run up to showing the card: the controls exist now. Start waits for the estimate. The listener is this
  // opening's only: #ask-input outlives the dialog, and a listener left on it would measure again for a dialog that is gone.
  const field = input();
  if (field && !first.hostOnly) field.addEventListener("input", onInput);
  if (ack) ack.input.addEventListener("change", draw);
  draw();
  if (!first.hostOnly) measure();
  const r = await asked;
  closed = true;
  clearTimeout(timer);
  if (inflight) inflight.abort();
  if (field) field.removeEventListener("input", onInput);
  if (hostOffer && r.outcome === "confirm") return { outcome: "host", dir: "", ack: false };
  if (first.hostOnly || r.outcome !== "confirm") return { outcome: "cancel", dir: "", ack: false };
  return { outcome: "confirm", dir: String(r.value || "").trim(), ack: !!(ack && ack.input.checked) };
}

// copyDialogRun: the whole conversion. Asks, makes the profile a private copy (ack_inplace_history when the server asked about the
// .git and the user ticked the box), and says what happened: {dir} when the profile now holds the copy, {host: true} when the
// user chose Host, null when they backed out. A save that fails comes back into the dialog with the server's words, and the repo
// is not measured again (o.sizes).
// o = {profileName, profile, home, hostOk}; o.profile is the ProfileView (editable, source, runtime, default_cwd).
async function copyDialogRun(refusal, o) {
  let ackAsked = false;
  let note = "";
  let dir = "";
  const sizes = new Map();
  for (;;) {
    const r = await copyDialogAsk(refusal, { profile: o.profile, profileName: o.profileName, home: o.home, hostOk: o.hostOk, dir, ackAsked, note, sizes });
    if (r.outcome === "host") return { host: true };
    if (r.outcome !== "confirm") return null;
    dir = r.dir;
    try {
      await cdConvertProfile(o.profileName, refusal.repo, dir, r.ack);
      if (typeof refreshProfilesQuiet === "function") await refreshProfilesQuiet();
      return { dir };
    } catch (e) {
      if (cdIsInplaceHistory(e) && !ackAsked) {
        ackAsked = true;
        note = e.message;
      } else {
        note = "Could not make this profile a private copy: " + (e && e.message ? e.message : "no answer");
      }
    }
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    copyDialogModel, cdEstimateBody, cdMergeFree, cdChangesLine, cdRefusalFromCheck, cdSavedDoc, cdIsInplaceHistory, cdSaveWithAck, cdSizeLine,
    CD_NOT_SEE, CD_LFS, CD_NO_DISK, CD_FOLDER_FIRST, CD_HOME, CD_HOME_UNKNOWN, CD_ACK_LABEL, CD_BUILT_IN, CD_CONFIG, CD_SHELL, CD_BODY,
  };
}
