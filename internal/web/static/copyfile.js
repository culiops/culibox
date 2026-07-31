"use strict";

// Deciding WHAT to copy out of a file and WHAT to say about it, with no DOM and
// no fetch, so both call sites — the viewer toolbar and the file-tree row menu —
// agree, and the whole decision is testable under `node --test`.
//
// Loaded as a classic script: these top-level names are shared globals. They
// live here rather than in viewer-tab.js because that file has no CommonJS
// guard, and re-declaring a const in a second script is a SyntaxError that
// blanks the page.

// Extensions with no copyable text. SVG is text, but /api/fs/read streams it as
// image/svg+xml with no JSON route to its source, so it stays on the Download
// path (see "Known limitation" in the design).
const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg"]);

// extOf returns the lowercase extension. Lowercase because the server lowercases
// before matching, so photo.PNG is an image to it and must be one to us as well;
// otherwise the menu offers Copy and the request fails inside JSON.parse.
function extOf(path) {
  const b = String(path || "").split("/").pop() || "";
  const i = b.lastIndexOf(".");
  return i < 0 ? "" : b.slice(i + 1).toLowerCase();
}

// countLines counts lines the way a person does: a trailing newline terminates
// the last line, it does not start an empty one.
function countLines(text) {
  if (!text) return 0;
  const n = text.split("\n").length;
  return text.endsWith("\n") ? n - 1 : n;
}

// copyContentPlan maps a read result to what should happen. `res` is the
// envelope {ok, status, data} rather than a decoded body, because a 413 carries
// no body worth reading and "too large" has to be distinguishable from "not
// found" — the shared api() helper keeps only a message string.
function copyContentPlan(res) {
  if (!res) return { action: "refuse", reason: "loading" };
  if (!res.ok) {
    if (res.status === 413) return { action: "refuse", reason: "toolarge" };
    // Only ONE of the three 400s means a directory. The others — "bad path"
    // (any relative path, which the paste-path box accepts) and "not a
    // directory" — must keep the server's own sentence instead of being
    // relabelled into a confident lie.
    if (res.status === 400 && res.data && res.data.error === "is a directory") {
      return { action: "refuse", reason: "isdir" };
    }
    if (!res.status) return { action: "refuse", reason: "loading" }; // network/abort
    return {
      action: "refuse",
      reason: "unreadable",
      detail: (res.data && res.data.error) || ("HTTP " + res.status),
    };
  }
  const data = res.data || {};
  if (data.kind === "binary") return { action: "refuse", reason: "binary" };
  // json:"content,omitempty" means a 0-byte file arrives with no content key at
  // all, so this must catch undefined as well as "".
  if (data.content == null || data.content === "") return { action: "refuse", reason: "empty" };
  return { action: "copy", text: data.content, lines: countLines(data.content) };
}

// copyResultMessage returns {text, error} rather than a bare string: showToast
// styles failures from opts.error, and a caller handed only prose would have to
// re-derive the tone this function just worked out.
function copyResultMessage(plan, name, ok) {
  if (!plan || plan.action === "copy") {
    if (!ok) return { text: 'Could not copy "' + name + '" to the clipboard — try again', error: true };
    const n = plan.lines;
    return { text: 'Copied "' + name + '" (' + n + " line" + (n === 1 ? "" : "s") + ")", error: false };
  }
  const say = {
    empty: '"' + name + '" is empty — nothing copied',
    binary: '"' + name + '" is a binary file — use Download instead',
    toolarge: '"' + name + '" is too large to copy — use Download instead',
    isdir: '"' + name + '" is a directory — nothing to copy',
    image: '"' + name + '" is an image — nothing to copy',
    loading: '"' + name + '" is still loading — try again',
    unreadable: 'Could not read "' + name + '": ' + (plan.detail || "unknown error"),
  }[plan.reason] || ('Could not copy "' + name + '"');
  return { text: say, error: true };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { IMAGE_EXTS, extOf, countLines, copyContentPlan, copyResultMessage };
}
