"use strict";

// File transport: upload (drag-drop, file picker, clipboard paste) and download.
// Every drop zone in the app is wired from here. Kept out of app.js, which is
// already large; the only things needed from it are showToast and shortPath.

// uploadFiles POSTs to /api/fs/upload with XHR rather than fetch — fetch cannot
// report upload progress, and progress is the point of the feedback here.
function uploadFiles(files, destDir, opts) {
  opts = opts || {};
  const list = Array.from(files || []);
  if (!list.length) return Promise.resolve(null);
  if (!destDir) {
    showToast("No destination folder yet — drop onto a folder in Files instead", { error: true });
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
      showToast("Uploading " + label + " — " + Math.round(frac * 100) + "%",
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
    xhr.onerror = () => { showToast("Upload failed — connection lost", { error: true }); resolve(null); };
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
      "failed: " + (first.name || "file") + " — " + first.error +
      (bad.length > 1 ? " (+" + (bad.length - 1) + " more)" : ""),
      { error: true });
    return;
  }
  const renamed = ok.filter((u) => u.renamed_from);
  if (ok.length === 1) {
    const u = ok[0];
    showToast(u.renamed_from
      ? u.renamed_from + " already existed — saved as " + u.name + " in " + shortPath(destDir)
      : "Uploaded " + u.name + " to " + shortPath(destDir));
  } else {
    showToast("Uploaded " + ok.length + " files to " + shortPath(destDir) +
      (renamed.length ? " (" + renamed.length + " renamed)" : ""));
  }
}

// downloadPath triggers a browser download. A same-origin navigation carries the
// basic-auth credentials the page already holds, so no fetch/blob dance and no
// second password prompt.
function downloadPath(path, isDir) {
  const url = isDir
    ? "/api/fs/archive?path=" + encodeURIComponent(path)
    : "/api/fs/read?download=1&path=" + encodeURIComponent(path);
  const a = document.createElement("a");
  a.href = url;
  a.download = "";
  a.style.display = "none";
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
  let depth = 0; // dragenter/leave also fire for descendants; count to avoid flicker
  el.addEventListener("dragenter", (e) => {
    if (!dragHasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    depth++;
    el.classList.add("drop-hot");
  });
  el.addEventListener("dragover", (e) => {
    if (!dragHasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = "copy";
  });
  el.addEventListener("dragleave", (e) => {
    if (!dragHasFiles(e)) return;
    depth = Math.max(0, depth - 1);
    if (!depth) el.classList.remove("drop-hot");
  });
  el.addEventListener("drop", (e) => {
    if (!dragHasFiles(e)) return;
    e.preventDefault();
    e.stopPropagation();
    depth = 0;
    el.classList.remove("drop-hot");
    uploadFiles(e.dataTransfer.files, destFn(), { onDone: opts.onDone });
  });
}

// shellQuote wraps a path for a prompt only when it needs it. Shells and agent
// CLIs both accept a single-quoted path; leaving a spaced path bare would
// silently split it into two arguments.
function shellQuote(p) {
  return /^[\w@%+=:,./-]+$/.test(p) ? p : "'" + p.replace(/'/g, "'\\''") + "'";
}

// insertPathsIntoTerminal types the uploaded paths at the prompt, followed by a
// space and never a newline, so the user sees them before anything runs.
function insertPathsIntoTerminal(term, data) {
  const ok = (data && data.uploaded) || [];
  if (!ok.length || !term || typeof term.sendText !== "function") return;
  const text = ok.map((u) => shellQuote(u.path)).join(" ") + " ";
  if (!term.sendText(text)) {
    showToast("Uploaded, but this session is not connected — path not inserted", { error: true });
  }
}

// clipboardFiles extracts real files from a paste. A text paste has no files and
// must fall through untouched to xterm's own handling.
function clipboardFiles(e) {
  const dt = e.clipboardData;
  if (!dt || !dt.files) return [];
  return Array.from(dt.files);
}

// pasteName gives a clipboard image a stable, sortable name. Browsers report it
// as "image.png" or as nothing at all, so every paste would otherwise collide.
function pasteName(file) {
  const d = new Date();
  const p = (n) => String(n).padStart(2, "0");
  const stamp = d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + "-" +
                p(d.getHours()) + p(d.getMinutes()) + p(d.getSeconds());
  const sub = (file.type || "").split("/")[1] || "";
  const ext = sub.replace(/[^a-z0-9]/gi, "").slice(0, 8) || "bin";
  return "paste-" + stamp + "." + ext;
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

  // Files pane ＋ button — the only upload route on mobile, where drag does not
  // exist at all.
  const picker = document.getElementById("ft-upload-input");
  const btn = document.getElementById("ft-upload");
  if (btn && picker) {
    btn.onclick = () => picker.click();
    picker.onchange = () => {
      uploadFiles(picker.files, ftRootSel && ftRootSel.value, { onDone: () => ftLoadRoot() });
      picker.value = "";
    };
  }

  // The tree background is a drop zone rooted at the current root; individual
  // directory rows override it by stopping propagation (see filetree.js).
  const tree = document.getElementById("filetree");
  if (tree) makeDropZone(tree, () => ftRootSel && ftRootSel.value, { onDone: () => ftLoadRoot() });
}
