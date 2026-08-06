"use strict";

// Pure edit-flow logic for the file viewer's Edit mode. No DOM here so it is
// reachable from `node --test`; viewer-tab.js (which is not) calls into it.

function saveBody(path, content, baseVersion, overwrite) {
  return { path, content, base_version: baseVersion, overwrite: !!overwrite };
}

// classifySave turns an fsWrite result ({ok, status, data}) into a decision the
// viewer acts on. 409 is the only conflict; everything else that is not ok is an
// error (including status 0, a fetch that never completed — never a conflict).
function classifySave(res) {
  if (res && res.ok && res.status === 200) {
    return { outcome: "saved", version: res.data && res.data.version, message: "saved" };
  }
  if (res && res.status === 409) {
    return {
      outcome: "conflict",
      currentVersion: res.data && res.data.current_version,
      message: "file changed on disk",
    };
  }
  const msg = (res && res.data && res.data.error) ||
    (res && res.status ? "HTTP " + res.status : "could not reach the server");
  return { outcome: "error", message: msg };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { saveBody, classifySave };
}
