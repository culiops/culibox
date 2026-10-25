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
    bugBase = String(res.markdown || "");
    bugIssueURL = String(res.issue_url || "");
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

function bugOpenIssue() {
  if (!bugIssueURL) return;
  window.open(bugIssueURL, "_blank", "noopener");
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
