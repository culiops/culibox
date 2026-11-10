"use strict";

// Applying the VM folder list (#70 Task 14; frames M9.6-M9.8): the confirm copy, the progress card's model, the sidebar banner's
// model and the ONE global poller that follows the restart job. The poller is not owned by the Settings page: leaving the page
// (or closing it) does not stop it, so the sidebar banner keeps up and the page, when it is open again, shows the live state.
//
// Pure: vmaFmt, vmApplyConfirmCopy, vmApplyBannerModel, vmApplyProgressModel (jstest/vmapply.test.js pins every word).
// Classic script: api, showToast, copyText, askConfirm, openSettings, sysBannerSync, loadVMFolders and vmfApplyChanged are other
// files' globals, read at call time (each guarded, so a test or a page without one still runs).

const VMA_POLL_MS = 1000;
const VMA_POLL_MAX_MS = 5 * 60 * 1000;

const VMA_TITLES = { stop: "Stop the box VM", start: "Start it with the new folders", check: "Check containment" };
const VMA_FOOT_RUNNING = "New box sessions are refused until this finishes. Host sessions are not affected. You can leave this page; the restart goes on.";
const VMA_FOOT_FAILED = "Your folder list is saved. The box VM is stopped, so box sessions can’t start until it runs."; // a failed start
const VMA_FOOT_NOT_APPLIED = "Your folder list is saved. Try again, or run it from a terminal."; // a failed stop or check

// vmaFmt(ms): "12 s", "2 m 0 s". Whole seconds, rounded down; never negative.
function vmaFmt(ms) {
  const s = Math.max(0, Math.floor((Number(ms) || 0) / 1000));
  if (s < 60) return s + " s";
  return Math.floor(s / 60) + " m " + (s % 60) + " s";
}

// vmApplyConfirmCopy(names): the M9.6 body and notes for the sessions running in boxes (names of the sessions the stop ends).
// A single session is new copy (the plural sentence says "1 sessions"); none running has its own first line.
function vmApplyConfirmCopy(names) {
  const list = (names || []).slice();
  let body;
  if (list.length === 0) body = "No box sessions are running.";
  else {
    const who = list.length === 1 ? list[0] : list.slice(0, -1).join(", ") + " and " + list[list.length - 1];
    body = list.length === 1 ? "1 session is running in a box; it will end: " + who + "." : list.length + " sessions are running in boxes; they will end: " + who + ".";
  }
  return {
    title: "Restart the box VM?",
    body,
    notes: [
      ["The VM comes back with the new folder list in about 20 seconds. Your images are kept. Files in the workspaces are not touched."],
      ["Host sessions keep running. New box sessions are refused until the VM is back."],
    ],
    confirmLabel: "Restart box VM",
    hint: "Esc to cancel",
  };
}

// vmaFailedStep: the name of the step that failed ("" when none says so).
function vmaFailedStep(job) {
  const s = ((job && job.steps) || []).find((x) => x.state === "failed");
  return s ? s.name : "";
}

// vmApplyBannerModel(job): the sidebar banner (M9.7 running, M9.8 failed), or null for done or no job. No amber, no red.
// Only a failed start says the VM is stopped; a failed stop or check says the folder list was not applied.
function vmApplyBannerModel(job) {
  if (!job) return null;
  if (job.state === "running") {
    return { kind: "vmapply", title: "Box VM restarting", body: "New box sessions are refused until it is back, about 20 s.", actions: [] };
  }
  if (job.state === "failed") {
    const actions = [{ id: "open-vmfolders", label: "Open VM folders" }];
    if (vmaFailedStep(job) === "start") return { kind: "vmapply", title: "Box VM stopped", body: "Box sessions can’t start.", actions };
    return { kind: "vmapply", title: "Box VM folders not applied", body: "Open VM folders to see why.", actions };
  }
  return null;
}

// The failed card's head: the frame's for a failed start, "The folder list was not applied" for a failed stop or check.
function vmaFailedHead(step) { return step === "start" ? "The box VM did not start" : "The folder list was not applied"; }

// vmApplyProgressModel(job, nowMs): the card for a running or failed job, null otherwise. A step's glyph is "✓", "spin", "✕" or
// its number; `right` is its time (a waiting step says Waiting, one after the failure says Not run). A running step's time is
// counted from where the finished steps end (the server only stamps a step's own time once it is done or failed). Time is the
// server's elapsed_ms at the last answer plus the local time since that answer (seen_ms): never this browser's clock against
// the server's started_ms.
function vmApplyProgressModel(job, nowMs) {
  if (!job || (job.state !== "running" && job.state !== "failed")) return null;
  const failed = job.state === "failed";
  const total = (Number(job.elapsed_ms) || 0) + Math.max(0, nowMs - (Number(job.seen_ms) || nowMs));
  let at = 0; // where the next step starts, on the job's own clock
  let failedName = "";
  const steps = (job.steps || []).map((s, i) => {
    const m = { n: i + 1, glyph: String(i + 1), title: VMA_TITLES[s.name] || s.name, sub: s.sub || "", right: "", red: false };
    switch (s.state) {
      case "done": m.glyph = "✓"; m.right = vmaFmt(s.ms); at += Number(s.ms) || 0; break;
      case "running": m.glyph = "spin"; m.right = vmaFmt(total - at); break;
      case "failed":
        m.glyph = "✕"; m.red = true; m.right = vmaFmt(s.ms);
        if (job.error) m.sub = job.error;
        failedName = s.name;
        break;
      case "not_run": m.right = "Not run"; break;
      default: m.right = "Waiting";
    }
    return m;
  });
  return {
    head: failed ? vmaFailedHead(failedName) : "Restarting the box VM",
    elapsed: failed ? "" : vmaFmt(total),
    steps,
    foot: !failed ? VMA_FOOT_RUNNING : failedName === "start" ? VMA_FOOT_FAILED : VMA_FOOT_NOT_APPLIED,
  };
}

// ---- the global job state and poller -----------------------------------------------------------------------------------------

let vmaJob = null;       // the last job seen (from the POST, a poll or /api/meta)
let vmaTimer = null;
let vmaPollStart = 0;
let vmaPollBusy = false;

function vmApplyCurrent() { return vmaJob; }

function vmaChanged(prev) {
  if (typeof sysBannerSync === "function") sysBannerSync();
  if (typeof vmfApplyChanged === "function") vmfApplyChanged(prev, vmaJob);
}

// vmaSeen(job): take a job from any source. A repaint only when something the screen shows moved.
function vmaSeen(job) {
  if (!job) return;
  const prev = vmaJob;
  vmaJob = Object.assign({}, job, { seen_ms: Date.now() });
  vmaChanged(prev);
}

function stopVMApplyPoll() {
  if (vmaTimer) clearInterval(vmaTimer);
  vmaTimer = null;
}

async function vmaTick() {
  if (vmaPollBusy) return;
  if (Date.now() - vmaPollStart > VMA_POLL_MAX_MS) { stopVMApplyPoll(); return; }
  vmaPollBusy = true;
  try {
    const j = await api("/api/vm-folders/apply");
    vmaSeen(j);
    if (!j || j.state !== "running") stopVMApplyPoll();
  } catch (e) {
    // A blip (a dropped request) is not an end: the next tick asks again. 404 = the server has no such job any more (it restarted
    // mid-apply): the banner and the card go.
    if (e && e.status === 404) {
      stopVMApplyPoll();
      const prev = vmaJob;
      vmaJob = null;
      vmaChanged(prev);
    }
  } finally { vmaPollBusy = false; }
}

// startVMApplyPoll(job): follow the job once a second while it runs; stops on done or failed, or after 5 minutes (the 30 s tick
// starts it again if the server still says running). A second call while polling only takes the new job.
function startVMApplyPoll(job) {
  if (job) vmaSeen(job);
  if (vmaTimer) return;
  vmaPollStart = Date.now();
  vmaTimer = setInterval(vmaTick, VMA_POLL_MS);
}

// vmaFromMeta(job): /api/meta.vm_apply (the job while it runs or failed, else null). Called at boot and on the 30 s tick.
function vmaFromMeta(job) {
  if (job && (job.state === "running" || job.state === "failed")) {
    // Never step back from a newer local answer (a later job, or a finished one) to an older meta one.
    if (!vmaJob || job.id > vmaJob.id || (job.id === vmaJob.id && vmaJob.state === "running")) vmaSeen(job);
    if (job.state === "running") startVMApplyPoll();
  } else if (vmaJob && vmaJob.state === "failed") {
    // The server no longer reports the failure (a terminal apply worked, or it restarted): drop the banner and the card.
    const prev = vmaJob;
    vmaJob = null;
    vmaChanged(prev);
  } else if (vmaJob && vmaJob.state === "running") startVMApplyPoll(); // meta says it ended: one poll fetches how
}

// ---- the sidebar banner ------------------------------------------------------------------------------------------------------

let vmaBannerKey = null;

function vmaIsPhone() {
  return typeof mobileActive === "function" && mobileActive(window.innerWidth);
}

// vmApplySideModel: what the sidebar shows now (null on a phone: the page shows progress there, the banner is desktop's).
function vmApplySideModel() {
  if (vmaIsPhone()) return null;
  return vmApplyBannerModel(vmaJob);
}

// paintVMApplyBanner: #vmapply-banner-slot below the store's. Rebuilt only when its title changes (running -> failed).
function paintVMApplyBanner() {
  const slot = document.getElementById("vmapply-banner-slot");
  if (!slot) return;
  const m = vmApplySideModel();
  const key = m ? m.title : null;
  if (key === vmaBannerKey) return;
  vmaBannerKey = key;
  if (!m) { slot.replaceChildren(); slot.hidden = true; return; }
  const root = document.createElement("div");
  root.className = "banner";
  root.setAttribute("role", m.actions.length ? "alert" : "status");
  const title = document.createElement("p");
  title.className = "banner-title";
  title.textContent = m.title;
  const body = document.createElement("p");
  body.className = "t-sm muted";
  body.textContent = m.body;
  root.append(title, body);
  if (m.actions.length) {
    const acts = document.createElement("div");
    acts.className = "banner-acts";
    for (const a of m.actions) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "btn btn-ghost btn-sm vmapply-link";
      b.textContent = a.label;
      b.onclick = () => { if (typeof openSettings === "function") openSettings("vmfolders"); };
      acts.appendChild(b);
    }
    root.appendChild(acts);
  }
  slot.replaceChildren(root);
  slot.hidden = false;
}

// vmApplyRailBanner: the banner's input to the collapsed rail's one marker (lowest priority), or null.
function vmApplyRailBanner() {
  const m = vmApplySideModel();
  return m ? { title: m.title } : null;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    vmaFmt, vmApplyConfirmCopy, vmApplyBannerModel, vmApplyProgressModel, startVMApplyPoll, stopVMApplyPoll, vmaFromMeta, vmApplyCurrent,
    vmaSeen, vmApplySideModel,
    _reset() { stopVMApplyPoll(); vmaJob = null; vmaPollBusy = false; vmaBannerKey = null; },
  };
}
