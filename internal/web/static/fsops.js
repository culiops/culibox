"use strict";

// File mutations: create a folder, rename, delete. The pure text builders live
// here too, so they are testable under `node --test`.
//
// These call fetch directly rather than the shared api() helper: api() throws
// away the response body on a non-OK status, and the 409 body is exactly where
// the counts and the typed-confirmation name live.

async function fsPost(path, body) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  let data = null;
  if (res.status !== 204) { try { data = await res.json(); } catch (_) {} }
  return { ok: res.ok, status: res.status, data };
}

function fsMkdir(dir, name) { return fsPost("/api/fs/mkdir", { dir, name }); }
function fsRename(path, name) { return fsPost("/api/fs/rename", { path, name }); }
function fsDelete(path, opts) {
  opts = opts || {};
  return fsPost("/api/fs/delete", {
    path,
    recursive: !!opts.recursive,
    files: opts.files || 0,
    confirm_name: opts.confirmName || "",
  });
}

// fsErrText prefers the server's own sentence — it knows why it refused.
function fsErrText(res) {
  return (res && res.data && res.data.error) || ("HTTP " + (res && res.status));
}

// deleteConfirmText says exactly what is about to be lost. A capped measurement
// never states a total: after a short-circuit the number is "just over the cap",
// not the truth — the same lesson the archive probe already learned.
function deleteConfirmText(name, isDir, files, bytes, capped) {
  const what = isDir ? "folder" : "file";
  let size = "";
  if (isDir && files > 0) {
    size = capped
      ? " — over " + files + " files"
      : " — " + files + (files === 1 ? " file" : " files") + ", " +
        (typeof humanBytes === "function" ? humanBytes(bytes) : bytes + " bytes");
  }
  return "Delete the " + what + ' "' + name + '"' + size + "?\n\nThis cannot be undone.";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { deleteConfirmText, fsErrText };
}
