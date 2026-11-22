// The "Flagged for your review" panel, the sidebar entry and the pending list behind them (#346).
// handbackmodel.js owns the words and the grouping; this file only fetches and paints. Everything a box
// wrote (paths, symlink targets, details, branch names, the cut-short sentence) goes in through
// textContent. Desktop only: the phone UI is frozen (#340), so nothing here runs a request on a phone.

// How often the sidebar entry may ask the server: at most once per this many ms, on the sessions poll's
// beat (refreshSessionsRun calls handbackOnSessions), plus once per event that can change it.
const HANDBACK_POLL_MS = 30000;

let handbackPendingIds = null; // Set of pending record ids once the list has loaded, else null
let handbackPendingText = ""; // the sidebar entry's text, "" when nothing is pending
let handbackFetchedAt = 0;
let handbackListSeq = 0; // a slow earlier fetch must not overwrite a later one
let handbackSeen = null; // in-place note sigs already announced; null until the first sessions poll seeds it
let handbackPanelSeq = 0;

function hbEl(tag, cls, text) {
  const el = document.createElement(tag);
  if (cls) el.className = cls;
  if (text != null) el.textContent = text;
  return el;
}

function hbDesktop() {
  return !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
}

// handbackFetch: the pending records, or null when they could not be read.
async function handbackFetch() {
  const got = await api("/api/handback", { signal: typeof deadlineSignal === "function" ? deadlineSignal(8000) : undefined });
  return got && Array.isArray(got.records) ? got.records : [];
}

// handbackPaintSide: the sidebar entry (R18): shown only while something is pending, and only on the
// desktop, which is where the toast that would otherwise be the only way in is shown.
function handbackPaintSide(records) {
  const btn = document.getElementById("side-flagged");
  const text = handbackSidebarText(records);
  handbackPendingText = text;
  if (!btn) return;
  btn.textContent = text;
  btn.hidden = text === "" || !hbDesktop();
}

// handbackSetPending: a fresh pending list. Repaints the sidebar entry and, when the set of pending
// records changed, the session rows (their line hides a note that was reviewed).
function handbackSetPending(records) {
  const ids = new Set(handbackPending(records).map((r) => r.id));
  const before = handbackPendingIds;
  const changed = !before || before.size !== ids.size || [...ids].some((id) => !before.has(id));
  handbackPendingIds = ids;
  handbackPaintSide(records);
  if (changed && typeof refreshSessions === "function") refreshSessions();
}

// handbackRefresh: reads the pending list and paints what depends on it. A failed read keeps what was shown.
async function handbackRefresh() {
  if (!hbDesktop()) return;
  const seq = ++handbackListSeq;
  handbackFetchedAt = Date.now();
  let records;
  try { records = await handbackFetch(); } catch (_) { return; }
  if (seq !== handbackListSeq) return;
  handbackSetPending(records);
}

// handbackOnSessions: what one sessions poll means for hand-back. An in-place scan that flagged
// something (or could not finish) toasts once, with a link into the panel; the pending list is read
// again then, and otherwise at most once per HANDBACK_POLL_MS.
function handbackOnSessions(sessions) {
  const n = handbackNews(handbackSeen, sessions);
  handbackSeen = n.seen;
  for (const t of n.toasts) handbackToast(t.text, t.ids);
  if (n.toasts.length || handbackDue(handbackFetchedAt, Date.now(), HANDBACK_POLL_MS)) handbackRefresh();
}

// handbackToast: a warning toast that opens the review.
function handbackToast(text, ids) {
  showToast(text, { warn: true, link: { label: "Review", run: () => openHandback(ids) } });
}

// handbackLine: the session row's line for what this session handed back, or null. A button: it opens
// the review of exactly this session's records.
function handbackLine(s) {
  const text = handbackSessionText(s, handbackPendingIds || undefined);
  if (!text) return null;
  const b = hbEl("button", "sub hb-line", text);
  b.type = "button";
  b.title = HANDBACK_TITLE;
  b.onclick = (e) => { e.stopPropagation(); openHandback(handbackNoteIds(handbackSessionNotes(s))); };
  return b;
}

function hbFileRow(f) {
  const li = hbEl("li", "hb-file");
  li.appendChild(hbEl("div", "hb-path", f.path));
  const why = f.classes.map(handbackClassText).join(" · ");
  if (why) li.appendChild(hbEl("div", "hb-why", why));
  if (f.detail) li.appendChild(hbEl("div", "hb-detail", f.detail));
  if (f.target) li.appendChild(hbEl("div", "hb-detail", "Points to: " + f.target));
  if (f.sha256) {
    const row = hbEl("div", "hb-sha");
    row.appendChild(hbEl("span", "hb-shak", "SHA-256"));
    row.appendChild(hbEl("code", "hb-shav", f.sha256));
    const cp = hbEl("button", "btn gh hb-copy", "Copy");
    cp.type = "button";
    cp.setAttribute("aria-label", "Copy SHA-256 of " + f.path);
    cp.onclick = () => copyText(f.sha256, (ok) => {
      cp.textContent = ok ? "Copied" : "Copy failed";
      clearTimeout(cp._hbReset);
      cp._hbReset = setTimeout(() => { cp.textContent = "Copy"; }, 1500);
    });
    row.appendChild(cp);
    li.appendChild(row);
  } else if (f.hashNote) {
    li.appendChild(hbEl("div", "hb-detail", f.hashNote));
  }
  return li;
}

function hbGroupRow(g) {
  const li = hbEl("li", "hb-group");
  const d = hbEl("details", "hb-gd");
  const parts = [];
  for (const c of ["executable", "exec-script", "archive"]) if (g.counts[c] > 0) parts.push(g.counts[c] + " " + handbackClassText(c).toLowerCase());
  d.appendChild(hbEl("summary", "hb-gsum", g.dir + " — " + g.files.length + " files (" + parts.join(", ") + ")"));
  const ul = hbEl("ul", "hb-list");
  for (const f of g.files) ul.appendChild(hbFileRow(f));
  d.appendChild(ul);
  li.appendChild(d);
  return li;
}

// hbRecord: one record as a section: what it is, its files, the footer, and Mark reviewed.
function hbRecord(r, onReviewed) {
  const sec = hbEl("section", "hb-rec");
  sec.dataset.id = r.id;
  const inPlace = r.kind === "in-place";
  sec.appendChild(hbEl("h3", "hb-head", inPlace ? (r.workspace || "A folder") : (r.branch || "A branch") + (r.clone_of ? " · copy of " + r.clone_of : "")));
  sec.appendChild(hbEl("p", "hb-window", handbackWindowText(r)));
  const rows = handbackRows(r);
  if (rows.length) {
    const ul = hbEl("ul", "hb-list");
    for (const row of rows) ul.appendChild(row.kind === "group" ? hbGroupRow(row) : hbFileRow(row));
    sec.appendChild(ul);
  } else if (r.truncated) {
    // Nothing was listed, but the scan did not finish: that is not "nothing flagged".
    sec.appendChild(hbEl("p", "hb-empty", "Not everything was checked."));
  } else {
    sec.appendChild(hbEl("p", "hb-empty", handbackEmptyText()));
  }
  for (const line of handbackFooter(r)) sec.appendChild(hbEl("p", "hint hb-foot", line));
  const act = hbEl("div", "hb-act");
  const btn = hbEl("button", "btn hb-reviewed", "Mark reviewed");
  btn.type = "button";
  btn.onclick = () => onReviewed(r, btn);
  act.appendChild(btn);
  sec.appendChild(act);
  return sec;
}

// handbackRender: the panel's body for these records (an empty list says so).
function handbackRender(records) {
  const body = document.getElementById("hb-body");
  if (!body) return;
  const seen = [];
  const onReviewed = (r, btn) => handbackMarkReviewed(r, btn, seen);
  const secs = records.map((r) => { const s = hbRecord(r, onReviewed); seen.push(s); return s; });
  if (!secs.length) body.replaceChildren(hbEl("p", "hb-empty", handbackEmptyText()));
  else body.replaceChildren(...secs);
}

// handbackMarkReviewed: POST, then drop the section, repaint the entry and the profile badges. A 404 means the
// record is already gone (another tab reviewed it): the same outcome.
async function handbackMarkReviewed(r, btn, secs) {
  btn.disabled = true;
  setDialogError("handback-modal", "");
  try {
    await api("/api/handback/" + encodeURIComponent(r.id) + "/reviewed", { method: "POST" });
  } catch (e) {
    if (!e || e.status !== 404) {
      btn.disabled = false;
      setDialogError("handback-modal", "Could not mark this reviewed: " + ((e && e.message) || "unknown error"));
      return;
    }
  }
  const sec = secs.find((x) => x.dataset.id === r.id);
  if (sec) sec.remove();
  const body = document.getElementById("hb-body");
  if (body && !body.querySelector(".hb-rec")) body.replaceChildren(hbEl("p", "hb-empty", handbackEmptyText()));
  handbackRefresh();
  if (typeof refreshProfilesQuiet === "function") refreshProfilesQuiet();
}

// openHandback(sel): the review panel. sel is a record id, an array of ids, a profile's {cloneOf} or
// {workspace}, or nothing for every pending record (handbackSelect).
async function openHandback(sel) {
  const body = document.getElementById("hb-body");
  if (!body) return;
  const seq = ++handbackPanelSeq;
  setDialogError("handback-modal", "");
  body.replaceChildren(hbEl("p", "hb-empty", "Loading…"));
  openDialog("handback-modal");
  let records;
  try { records = await handbackFetch(); } catch (e) {
    if (seq !== handbackPanelSeq) return;
    body.replaceChildren();
    setDialogError("handback-modal", "Could not load the list: " + ((e && e.message) || "unknown error"));
    return;
  }
  if (seq !== handbackPanelSeq) return;
  handbackRender(handbackSelect(records, sel));
  handbackSetPending(records);
}

function handbackInit() {
  const btn = document.getElementById("side-flagged");
  if (btn) btn.onclick = () => openHandback();
}
handbackInit();
