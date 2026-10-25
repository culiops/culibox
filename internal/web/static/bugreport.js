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

// bugOpenIssue copies the full report FIRST, then opens the issue. GitHub's URL
// can only carry a short body (header + FAIL/WARN lines), so the clipboard is
// what holds the reporter's words and the whole bundle. The window opens from
// the copy callback, whether or not the copy worked (the user can still paste
// by hand via Copy report); the click's transient user activation outlasts the
// clipboard write, so the popup is not blocked.
function bugOpenIssue() {
  if (bugBase === null) return;
  if (typeof bugIssueURL !== "string" || !bugIssueURL.startsWith("https://github.com/")) {
    setDialogError("bug-modal", "No valid GitHub link was returned; use Copy report instead.");
    return;
  }
  setDialogError("bug-modal", "");
  copyText(bugFilled(), (ok) => {
    bugSetStatus(ok ? "Report copied — paste it into the issue body."
                    : "Could not copy the report — use Copy report, then paste it into the issue.");
    showToast(ok ? "Report copied" : "Could not copy the report", { error: !ok });
    window.open(bugIssueURL, "_blank", "noopener");
  });
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
