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

// fsRead is GET /api/fs/read as the same envelope, for the same reason as
// fsPost above: copying a file has to tell "too large" (413) from "is a
// directory" (400) from "not found" (404), and api() keeps only a message
// string. status 0 means the request never completed at all.
async function fsRead(path) {
  let res;
  try {
    res = await fetch("/api/fs/read?path=" + encodeURIComponent(path));
  } catch (_) {
    return { ok: false, status: 0, data: null };
  }
  let data = null;
  try { data = await res.json(); } catch (_) {}
  return { ok: res.ok, status: res.status, data };
}

// fsWrite is POST /api/fs/write as the same envelope. Wrapped in its own
// try/catch (unlike fsPost, which does not guard the transport error) for the
// same reason fsRead is: a save has to tell a genuine 409 conflict from a
// fetch that never completed (status 0), and the caller (viewer-tab.js's
// save()) routes those two very differently via classifySave.
async function fsWrite(path, body) {
  try {
    return await fsPost("/api/fs/write", body);
  } catch (_) {
    return { ok: false, status: 0, data: null };
  }
}

function fsMkdir(dir, name) { return fsPost("/api/fs/mkdir", { dir, name }); }
function fsRename(path, name, confirmName) {
  return fsPost("/api/fs/rename", { path, name, confirm_name: confirmName || "" });
}
function fsDelete(path, opts) {
  opts = opts || {};
  return fsPost("/api/fs/delete", {
    path,
    recursive: !!opts.recursive,
    // Echo back the entry count that was actually shown: the server re-measures
    // and refuses if the tree has grown well past what the user agreed to.
    entries: opts.entries || 0,
    confirm_name: opts.confirmName || "",
  });
}

// fsErrText prefers the server's own sentence: it knows why it refused.
function fsErrText(res) {
  return (res && res.data && res.data.error) || ("HTTP " + (res && res.status));
}

// deleteSummary describes a measurement in one clause, leading with the ENTRY
// count: everything the delete destroys, not just the regular files. Counting
// files alone let a tree of folders and symlinks describe itself as empty.
//
// A capped measurement never states the capped total: past a short-circuit the
// number is "just over the bound", not the truth. Which bound was hit decides
// which half of the sentence hedges.
function deleteSummary(m) {
  m = m || {};
  const entries = m.entries || 0;
  if (entries <= 0) return "";
  const size = (n) => (typeof humanBytes === "function" ? humanBytes(n) : n + " bytes");
  let out = m.capped === "entries"
    ? "over " + entries + " items"
    : entries + (entries === 1 ? " item" : " items");
  if (m.files > 0) {
    out += ", " + m.files + (m.files === 1 ? " file" : " files") +
      (m.capped === "bytes" ? ", over " + size(m.bytes) : ", " + size(m.bytes));
  }
  return out;
}

// deleteConfirmText says exactly what is about to be lost.
function deleteConfirmText(name, isDir, m) {
  const what = isDir ? "folder" : "file";
  const summary = isDir ? deleteSummary(m) : "";
  return "Delete the " + what + ' "' + name + '"' +
    (summary ? " (" + summary + ")" : "") + "?\n\nThis cannot be undone.";
}

// normalizePath collapses "." segments and duplicate slashes so a path typed
// into the paste box matches the one the tree renders. Viewer tabs are keyed by
// this string, and a tab opened as "/home/u/./a.txt" would otherwise survive the
// deletion of "/home/u/a.txt" showing content that no longer exists.
function normalizePath(p) {
  if (!p) return p;
  const abs = p.charAt(0) === "/";
  const out = [];
  for (const part of p.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === ".." && out.length && out[out.length - 1] !== "..") { out.pop(); continue; }
    out.push(part);
  }
  return (abs ? "/" : "") + out.join("/");
}

// shortPath renders an absolute path as ~/… when it sits under home. Callers in
// three other scripts (the spawn toast, the command palette, the upload toasts)
// rely on it through the shared global scope; it lives in this required module
// so deleting it breaks the test suite instead of only the browser.
function shortPath(p, home) {
  if (home === undefined) home = (typeof window !== "undefined" && window.HOME_DIR) || "";
  if (!home || !p) return p;
  if (p === home) return "~";
  // Compare whole components: "/home/user2" is not inside "/home/user".
  return p.indexOf(home + "/") === 0 ? "~" + p.slice(home.length) : p;
}

// relativeTo renders `path` relative to `root`. It is the ONE definition of
// "relative path" in this app: the tree is rooted at whatever directory the
// root picker (#ft-root-input) last resolved to, while /api/fs/search returns
// paths relative to HOME, so a second definition anywhere would make two
// identically-labelled buttons disagree.
//
// A path outside the root falls back to shortPath's ~/… form rather than to the
// bare absolute path: returning the same string as the "Copy path" button next
// to it, with no explanation, is how a user stops trusting both.
function relativeTo(root, path, home) {
  if (!root || !path) return shortPath(path, home);
  const r = root === "/" ? "/" : root.replace(/\/+$/, "");
  if (path === r) return ".";
  const prefix = r === "/" ? "/" : r + "/";
  // Whole-component compare, like shortPath: "/home/user2" is not inside
  // "/home/user".
  return path.indexOf(prefix) === 0 ? path.slice(prefix.length) : shortPath(path, home);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { deleteConfirmText, deleteSummary, normalizePath, fsErrText, shortPath, relativeTo };
}
