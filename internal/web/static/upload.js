"use strict";

// File transport: upload (drag-drop, file picker, clipboard paste) and download.
// Every drop zone in the app is wired from here. Kept out of app.js, which is
// already large; the only things needed from it are showToast and shortPath.

// uploadFiles POSTs to /api/fs/upload with XHR rather than fetch: fetch cannot
// report upload progress, and progress is the point of the feedback here.
function uploadFiles(files, destDir, opts) {
  opts = opts || {};
  const list = Array.from(files || []);
  if (!list.length) return Promise.resolve(null);
  if (!destDir) {
    showToast("No destination folder yet. Drop onto a folder in Files instead", { error: true });
    return Promise.resolve(null);
  }
  const fd = new FormData();
  for (const f of list) fd.append("file", f, f.name);

  const label = list.length === 1 ? list[0].name : list.length + " files";
  showToast("Uploading " + label + "…", { progress: 0, ms: 60000 });

  return new Promise((resolve) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/fs/upload?dir=" + encodeURIComponent(destDir));
    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) return;
      const frac = e.loaded / e.total;
      showToast("Uploading " + label + ": " + Math.round(frac * 100) + "%",
        { progress: frac, ms: 60000 });
    };
    xhr.onload = () => {
      let data = null;
      try { data = JSON.parse(xhr.responseText); } catch (_) {}
      if (xhr.status !== 200 || !data) {
        showToast("Upload failed: " + ((data && data.error) || ("HTTP " + xhr.status)), { error: true });
        resolve(null);
        return;
      }
      reportUpload(data, destDir);
      if (opts.onDone) opts.onDone(data);
      resolve(data);
    };
    xhr.onerror = () => { showToast("Upload failed: connection lost", { error: true }); resolve(null); };
    xhr.send(fd);
  });
}

// reportUpload turns the per-part result into one honest sentence. A rename is
// never silent: the file you dropped is not the file that landed.
function reportUpload(data, destDir) {
  const ok = data.uploaded || [], bad = data.errors || [];
  if (bad.length) {
    const first = bad[0];
    showToast(
      (ok.length ? ok.length + " uploaded · " : "") +
      "failed on " + (first.name || "file") + ": " + first.error +
      (bad.length > 1 ? " (+" + (bad.length - 1) + " more)" : ""),
      { error: true });
    return;
  }
  const renamed = ok.filter((u) => u.renamed_from);
  if (ok.length === 1) {
    const u = ok[0];
    showToast(u.renamed_from
      ? u.renamed_from + " already existed, saved as " + u.name + " in " + shortPath(destDir)
      : "Uploaded " + u.name + " to " + shortPath(destDir));
  } else {
    showToast("Uploaded " + ok.length + " files to " + shortPath(destDir) +
      (renamed.length ? " (" + renamed.length + " renamed)" : ""));
  }
}

// shouldRefreshTree decides whether an upload that landed in destDir changes what
// the file tree is currently showing. Only an upload into the tree's own root
// does: the tree lists one level, so a file dropped into a subfolder would not
// appear anyway, and re-listing would collapse every folder the user had opened.
// Trailing slashes differ by source (a <select> value vs a session's cwd), so
// they are normalised before comparing.
function shouldRefreshTree(destDir, treeRoot) {
  if (!destDir || !treeRoot) return false;
  const trim = (p) => (p.length > 1 ? p.replace(/\/+$/, "") : p);
  return trim(destDir) === trim(treeRoot);
}

// refreshTreeAfterUpload re-lists the tree when the upload changed what it shows.
// The terminal-pane drop and the image paste both upload into the session's cwd
// which is usually exactly the tree's root, since the tree follows the focused
// session, and they were the only upload routes that never told the tree, so an
// uploaded file simply never appeared until something else re-rooted it.
function refreshTreeAfterUpload(destDir) {
  if (typeof ftRoot === "undefined") return;
  if (!shouldRefreshTree(destDir, ftRoot())) return;
  if (typeof ftLoadRoot === "function") ftLoadRoot();
}

function humanBytes(n) {
  if (n >= (1 << 30)) return (n / (1 << 30)).toFixed(1) + " GB";
  if (n >= (1 << 20)) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n >= 1024) return (n / 1024).toFixed(1) + " KB";
  return n + " bytes";
}

// downloadPath triggers a browser download. A same-origin navigation carries the
// basic-auth credentials the page already holds, so no fetch/blob dance and no
// second password prompt.
//
// A folder is probed first. An <a download> navigation swallows the body of an
// error response, so without the probe an over-cap folder gives a failed
// download and no reason for it anywhere in the UI.
async function downloadPath(path, isDir) {
  if (isDir) {
    let probe;
    try {
      const r = await fetch("/api/fs/archive?probe=1&path=" + encodeURIComponent(path));
      probe = await r.json().catch(() => null);
      if (!r.ok) {
        showToast("Cannot download that folder: " + ((probe && probe.error) || ("HTTP " + r.status)),
          { error: true });
        return;
      }
    } catch (e) {
      showToast("Cannot download that folder: " + e.message, { error: true });
      return;
    }
    // This toast is now the ONLY place the symlink-skip is disclosed: the row
    // menu says just "Download .tar.gz", so it has to read properly.
    const n = probe.files || 0;
    showToast("Preparing " + n + (n === 1 ? " file (" : " files (") + humanBytes(probe.bytes || 0) +
      "); symlinks are skipped");
  }
  const a = document.createElement("a");
  a.style.display = "none";
  if (isDir) {
    a.href = "/api/fs/archive?path=" + encodeURIComponent(path);
    a.download = "";
  } else {
    a.href = "/api/fs/read?download=1&path=" + encodeURIComponent(path);
    // Deliberately no `download` attribute here. It makes the browser consume
    // the response whatever the status, so a 404 for a file deleted since the
    // tree was rendered would "download" a 30-byte JSON error named `read` with
    // nothing shown anywhere. Content-Disposition already forces the save, and
    // _blank means an error status renders in a throwaway tab instead of
    // navigating this one away and destroying the layout.
    a.target = "_blank";
    a.rel = "noopener";
  }
  document.body.append(a);
  a.click();
  a.remove();
}

// dragHasFiles distinguishes a real file drag from an in-page text or selection
// drag, so a drop target only ever lights up for something it can accept.
function dragHasFiles(e) {
  const dt = e.dataTransfer;
  if (!dt || !dt.types) return false;
  for (const t of dt.types) if (t === "Files") return true;
  return false;
}

// makeDropZone wires one element as a drop target. destFn resolves the
// destination at DROP time, not at wiring time: the tree root and the focused
// session both move while the page stays open.
function makeDropZone(el, destFn, opts) {
  opts = opts || {};
  // The element that lights up is not always the listener: a terminal pane
  // listens on its tab body but must highlight the whole cell, which is what
  // carries the border and is what reads as a target at 390px.
  const hot = () => (opts.highlight ? opts.highlight() : el);
  let lit = null;
  let depth = 0; // dragenter/leave also fire for descendants; count to avoid flicker
  el.addEventListener("dragenter", (e) => {
    if (!dragHasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    depth++;
    lit = hot();
    lit.classList.add("drop-hot");
  });
  el.addEventListener("dragover", (e) => {
    if (!dragHasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = "copy";
  });
  el.addEventListener("dragleave", (e) => {
    if (!dragHasFiles(e)) return;
    // Must stop propagation like the other three handlers: a nested zone's
    // dragleave bubbling to its parent decrements a counter the parent never
    // incremented (its dragenter was stopped), which strips the parent's
    // highlight while the pointer is still over a live drop target.
    e.stopPropagation();
    depth = Math.max(0, depth - 1);
    if (!depth && lit) { lit.classList.remove("drop-hot"); lit = null; }
  });
  el.addEventListener("drop", (e) => {
    if (!dragHasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    depth = 0;
    if (lit) { lit.classList.remove("drop-hot"); lit = null; }
    uploadFiles(e.dataTransfer.files, destFn(), { onDone: opts.onDone });
  });
}

// UNSAFE_TEXT matches anything that must never reach a PTY: C0 controls (\n
// would submit the line), DEL, and C1 controls (parsed as escape sequences once
// the shell echoes them back).
const UNSAFE_TEXT = /[\x00-\x1f\x7f-\x9f]/;

// shellQuote wraps a path for a prompt only when it needs it. Shells and agent
// CLIs both accept a single-quoted path; leaving a spaced path bare would
// silently split it into two arguments.
function shellQuote(p) {
  return /^[\w@%+=:,./-]+$/.test(p) ? p : "'" + p.replace(/'/g, "'\\''") + "'";
}

// insertTextAtPrompt types text into a session exactly as the user would, and
// never a newline: the user stays the one who presses Enter. It reports WHY it
// refused instead of raising its own message, because its two callers phrase
// failure differently ("Uploaded, but…" against a plain "no session focused").
//
// The unsafe check is here as well as in sendText because a caller may want to
// distinguish "this path can never be inserted" from "this socket is down".
function insertTextAtPrompt(term, text) {
  if (!term || typeof term.sendText !== "function") return { ok: false, reason: "noterm" };
  // The uploaded FILE name is sanitized server-side, but the directory half of
  // a path is not: a directory name may legally contain a newline on Linux, and
  // that newline would submit the line the moment it was typed.
  if (UNSAFE_TEXT.test(text)) return { ok: false, reason: "unsafe" };
  if (!term.sendText(text)) return { ok: false, reason: "disconnected" };
  return { ok: true, reason: "" };
}

// insertPathsIntoTerminal types the uploaded paths at the prompt, followed by a
// space and never a newline, so the user sees them before anything runs.
function insertPathsIntoTerminal(term, data) {
  const ok = (data && data.uploaded) || [];
  if (!ok.length) return;
  const res = insertTextAtPrompt(term, ok.map((u) => shellQuote(u.path)).join(" ") + " ");
  if (res.ok || res.reason === "noterm") return;
  showToast(res.reason === "unsafe"
    ? "Uploaded, but that folder's name contains control characters, so the path was not inserted"
    : "Uploaded, but this session is not connected, so the path was not inserted",
    { error: true });
}

// clipboardFiles extracts real files from a paste. A text paste has no files and
// must fall through untouched to xterm's own handling.
function clipboardFiles(e) {
  const dt = e.clipboardData;
  if (!dt || !dt.files) return [];
  return Array.from(dt.files);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { shouldRefreshTree, shellQuote, pasteFileName, humanBytes, insertTextAtPrompt };
}

// pasteFileName keeps the name the browser supplied and only invents one when
// there is nothing usable. clipboardData.files is also non-empty for a copy from
// the OS file manager, and those carry a real filename: renaming those to
// paste-<timestamp> would throw away the only identifying information the file
// had. "image.png" is the generic name browsers synthesise for a raw bitmap.
function pasteFileName(file) {
  const n = file.name || "";
  if (n && n !== "image.png") return n;
  return pasteName(file);
}

// pasteName gives a clipboard bitmap a stable, sortable name.
function pasteName(file) {
  const d = new Date();
  const p = (n) => String(n).padStart(2, "0");
  const stamp = d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + "-" +
                p(d.getHours()) + p(d.getMinutes()) + p(d.getSeconds());
  // Take the leading token of the MIME subtype: "svg+xml" is svg, "plain" is
  // not a useful extension so text/* falls back to txt.
  const sub = (file.type || "").split("/");
  let ext = (sub[1] || "").split(/[+;]/)[0].replace(/[^a-z0-9]/gi, "").slice(0, 8);
  if (sub[0] === "text") ext = "txt";
  return "paste-" + stamp + "." + (ext || "bin");
}

function initUpload() {
  // Global guard. Without it, a drop that misses every zone makes the browser
  // navigate to the dropped file and the entire working layout is gone.
  window.addEventListener("dragover", (e) => { if (dragHasFiles(e)) e.preventDefault(); });
  window.addEventListener("drop", (e) => {
    if (!dragHasFiles(e)) return;
    e.preventDefault();
    showToast("Drop onto a terminal pane, or onto a folder in Files", { error: true });
  });

  // Files pane ＋ button: the only upload route on mobile, where drag does not
  // exist at all.
  const picker = document.getElementById("ft-upload-input");
  const btn = document.getElementById("ft-upload");
  if (btn && picker) {
    btn.onclick = () => picker.click();
    picker.onchange = () => {
      uploadFiles(picker.files, ftRoot(), { onDone: () => ftLoadRoot() });
      picker.value = "";
    };
  }

  // The tree background is a drop zone rooted at the current root; individual
  // directory rows override it by stopping propagation (see filetree.js).
  const tree = document.getElementById("filetree");
  if (tree) makeDropZone(tree, () => ftRoot(), { onDone: () => ftLoadRoot() });
}
