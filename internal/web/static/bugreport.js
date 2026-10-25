// "Report a problem": Settings -> About shows the build, and a dialog previews
// the redacted bundle from GET /api/bugreport so the user can read exactly what
// would be shared before copying it or opening a prefilled GitHub issue.

// WHAT_MARKER is internal/bugreport's WhatHappenedMarker; it appears once under
// "## What happened" in the markdown the server returns.
const WHAT_MARKER = "<!-- what-happened -->";

// fillWhatHappened puts the user's words where the marker is. indexOf + slicing,
// deliberately not String.replace: replace would interpret "$&" / "$1" in what
// the user typed, and a global or regex form would also fill a second marker.
// Blank text, or markdown with no marker, comes back untouched.
function fillWhatHappened(markdown, text) {
  if (typeof text !== "string" || text.trim() === "") return markdown;
  const i = markdown.indexOf(WHAT_MARKER);
  if (i < 0) return markdown;
  return markdown.slice(0, i) + text + markdown.slice(i + WHAT_MARKER.length);
}

let bugBase = null;     // the markdown as the server built it (marker intact)
let bugIssueURL = "";
let bugSeq = 0;         // guards a slow earlier build from overwriting a later one

function bugFilled() {
  const what = document.getElementById("bug-what");
  return fillWhatHappened(bugBase || "", what ? what.value : "");
}

function bugRender() {
  const pre = document.getElementById("bug-preview");
  if (pre && bugBase !== null) pre.textContent = bugFilled();
}

function bugSetStatus(msg) {
  const el = document.getElementById("bug-status");
  if (el) el.textContent = msg || "";
}

function bugSetReady(ready) {
  for (const id of ["bug-copy", "bug-gh"]) {
    const b = document.getElementById(id);
    if (b) b.disabled = !ready;
  }
}

async function openBugReport() {
  const pre = document.getElementById("bug-preview");
  const what = document.getElementById("bug-what");
  if (!pre) return;
  const seq = ++bugSeq;
  bugBase = null;
  bugIssueURL = "";
  bugSetReady(false);
  bugSetStatus("");
  setDialogError("bug-modal", "");
  pre.textContent = "Building report… (runs agent-box doctor, up to 30 s)";
  openDialog("bug-modal");
  if (what) what.focus();
  try {
    const res = await api("/api/bugreport");
    if (seq !== bugSeq) return;
    const md = res && typeof res.markdown === "string" ? res.markdown : "";
    if (md.trim() === "") throw new Error("the server returned an empty report");
    bugBase = md;
    bugIssueURL = String((res && res.issue_url) || "");
    bugRender();
    bugSetReady(true);
  } catch (e) {
    if (seq !== bugSeq) return;
    if (e && e.status === 429) {
      pre.textContent = "";
      setDialogError("bug-modal", "A report is already being built. Try again in a moment.");
      showToast("A report is already being built", { error: true });
      return;
    }
    pre.textContent = "";
    setDialogError("bug-modal", "Could not build the report: " + ((e && e.message) || "unknown error"));
  }
}

function bugCopy() {
  if (bugBase === null) return;
  copyText(bugFilled(), (ok) => {
    bugSetStatus(ok ? "Copied." : "Could not copy to the clipboard.");
    showToast(ok ? "Report copied" : "Could not copy the report", { error: !ok });
  });
}

// bugStatusIssue writes the status line and, always, a real link to the issue
// next to it: a popup blocker can swallow window.open (and with "noopener" it
// returns null even on success, so a block cannot be detected), so the link is
// the one-click way in. The URL was validated as https://github.com/... first.
function bugStatusIssue(msg, url) {
  const el = document.getElementById("bug-status");
  if (!el) return;
  el.textContent = msg + " ";
  const a = document.createElement("a");
  a.href = url;
  a.target = "_blank";
  a.rel = "noopener";
  a.textContent = "Open the issue";
  el.appendChild(a);
}

// bugOpenIssue runs, synchronously inside the click: copy with the
// execCommand path (execCopy, app.js; it needs no await), then window.open.
// Neither waits on a promise, so the open keeps the click's user activation and
// Safari's popup blocker sees a plain click-initiated open. navigator.clipboard
// is only a fallback when the synchronous copy failed, and its result is
// reported afterwards. GitHub's URL carries only a short body (header +
// FAIL/WARN lines); the clipboard holds the reporter's words and the whole
// bundle, which is why the copy comes first.
function bugOpenIssue() {
  if (bugBase === null) return;
  if (typeof bugIssueURL !== "string" || !bugIssueURL.startsWith("https://github.com/")) {
    setDialogError("bug-modal", "No valid GitHub link was returned; use Copy report instead.");
    return;
  }
  setDialogError("bug-modal", "");
  const text = bugFilled();
  const url = bugIssueURL;
  window.__lastCopied = text; // same test hook copyText sets
  const copied = execCopy(text);
  window.open(url, "_blank", "noopener");
  if (copied) {
    bugStatusIssue("Report copied — paste it into the issue body.", url);
    showToast("Report copied", {});
    return;
  }
  // Synchronous copy failed: try the async clipboard (HTTPS only) and say how it went.
  bugStatusIssue("Copying…", url);
  const done = (ok) => {
    bugStatusIssue(ok ? "Report copied — paste it into the issue body."
                      : "Could not copy — use Copy report, then paste.", url);
    showToast(ok ? "Report copied" : "Could not copy the report", { error: !ok });
  };
  if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(() => done(true), () => done(false));
  } else {
    done(false);
  }
}

// loadAboutInfo shows the build in the About tab. /api/meta is the one place
// the commit is published.
async function loadAboutInfo() {
  const el = document.getElementById("about-version");
  if (!el) return;
  try {
    const meta = await api("/api/meta");
    el.textContent = "agent-box " + (meta && meta.commit ? meta.commit : "(unknown build)");
  } catch (_) {
    el.textContent = "agent-box (build unknown)";
  }
}

function initBugReport() {
  const open = document.getElementById("bug-open");
  if (open) open.onclick = openBugReport;
  const what = document.getElementById("bug-what");
  if (what) what.addEventListener("input", bugRender);
  const copy = document.getElementById("bug-copy");
  if (copy) copy.onclick = bugCopy;
  const gh = document.getElementById("bug-gh");
  if (gh) gh.onclick = bugOpenIssue;
}

if (typeof document !== "undefined") {
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initBugReport);
  } else {
    initBugReport();
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { WHAT_MARKER, fillWhatHappened };
}
