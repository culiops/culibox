"use strict";

// boximage.js: the read-only "Box image" rows in Settings -> About (desktop only; app.css hides the container on a phone).
// GET /api/box-image answers from the daemon and the cached registry check, never the network (#156), so opening the tab
// is cheap. boxImageRows is pure (a report in, rows out); loadBoxImage paints them with textContent only. The report is
// untrusted text: nothing from it is parsed as markup, and the issue link is built here from an integer, never taken from it.

const BI_ISSUES = "https://github.com/culiops/culibox/issues/";
const BI_SETUP = "culibox box setup";

// shortDigest shows every sha256:<64 hex> in s as sha256: + the first 4 and last 4 of the hex. The full value stays in the
// row's title and in Copy, so nothing is lost by the short form.
function shortDigest(s) {
  return String(s).replace(/sha256:([0-9a-f]{64})/g, (_, h) => "sha256:" + h.slice(0, 4) + "…" + h.slice(-4));
}

// biRepo is the repository part of an image ref: no @digest, no :tag.
function biRepo(ref) {
  const r = String(ref).split("@")[0];
  const slash = r.lastIndexOf("/");
  const colon = r.indexOf(":", slash + 1);
  return colon < 0 ? r : r.slice(0, colon);
}

// boxImageRepoDigest is the digest the info row shows: the repo digest whose repository is the configured ref's, else the
// first repo digest, else the image id. "" when the daemon holds no such image.
function boxImageRepoDigest(r) {
  const info = r && r.info;
  if (!info || !info.present) return "";
  const ds = Array.isArray(info.repo_digests) ? info.repo_digests.filter((d) => typeof d === "string" && d) : [];
  const repo = biRepo(r.configured || "");
  return ds.find((d) => biRepo(d) === repo) || ds[0] || (typeof info.id === "string" ? info.id : "");
}

// Dates are the user's local time, ISO-like. "" for anything that is not a date.
function biDate(iso, withTime) {
  const d = new Date(iso);
  if (typeof iso !== "string" || isNaN(d.getTime())) return "";
  const p = (n) => String(n).padStart(2, "0");
  const day = d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate());
  return withTime ? day + " " + p(d.getHours()) + ":" + p(d.getMinutes()) : day;
}

// biDigestRow is a row for an image ref or digest: shortened to read, full value in title (and in Copy when copy is true).
function biDigestRow(label, full, copy) {
  const row = { label, text: shortDigest(full), code: true };
  if (row.text !== full) row.title = full;
  if (copy) row.copy = full;
  return row;
}

function biStatus(r) {
  switch (r.verdict) {
    case "current": return "Current: this is the image for this version";
    case "current-local": return "Current: built here from this version's Dockerfile";
    case "update": return "Built from an older Dockerfile than this CuliBox";
    case "stale-unknown": {
      let why = r.check_err || "not checked yet";
      if (r.check_on === false) why = "check off: image_check is false";
      return "Built from an older Dockerfile than this CuliBox. Whether CI published this version's image is unknown (" + why + ")";
    }
    case "missing": return "Not on the daemon: run " + BI_SETUP;
    case "unknown": return "Box image details unknown (docker did not answer)";
  }
  return "";
}

// biWantRows is the image for this version: its digest with the Claude Code and built facts, when the report names one.
function biWantRows(r) {
  if (typeof r.want !== "string" || !r.want) return [];
  const row = biDigestRow("For this version", r.want, false);
  const facts = [];
  if (typeof r.want_claude_code === "string" && r.want_claude_code) facts.push("Claude Code " + r.want_claude_code);
  const built = biDate(r.want_created, false);
  if (built) facts.push("built " + built);
  if (facts.length) row.note = "(" + facts.join(", ") + ")";
  return [row];
}

// biUpdateRows is what the newer image is, how much it costs to fetch and the steps to take. Read-only: nothing here runs.
function biUpdateRows(r) {
  const rows = biWantRows(r);
  if (typeof r.download_mb === "string" && r.download_mb) rows.push({ label: "", text: "about " + r.download_mb + " MB to download" });
  const line = typeof r.config_line === "string" ? r.config_line : "";
  if (line) rows.push(biDigestRow("", line, true));
  rows.push({ label: "", text: (line ? "then run: " : "run: ") + (r.setup_cmd || BI_SETUP) });
  const at = biDate(r.checked_at, true);
  if (at) rows.push({ label: "", text: "Checked " + at + " · nothing changes until you do", fine: true });
  return rows;
}

function biScanRow(s) {
  if (!s || typeof s !== "object" || !Number.isInteger(s.count) || s.count < 0) return null;
  if (s.count === 0) return { label: "Weekly scan", text: "no fixable HIGH/CRITICAL in the published images" };
  const row = { label: "Weekly scan", text: s.count + " fixable HIGH/CRITICAL in the published images" };
  if (Number.isSafeInteger(s.number) && s.number > 0) {
    row.text += " (#" + s.number + ")";
    row.href = BI_ISSUES + s.number;
  }
  return row;
}

// boxImageRows turns a /api/box-image report into display rows: {label, text, code?, title?, note?, copy?, href?}. An empty
// label is a continuation of the row above; muted / fine rows are secondary text (fine is also the smaller size). [] when r is not a report, so the container stays hidden.
function boxImageRows(r) {
  if (!r || typeof r !== "object" || Array.isArray(r)) return [];
  const rows = [];
  const digest = boxImageRepoDigest(r);
  if (digest) {
    rows.push(biDigestRow("Box image", digest, true));
    const info = r.info;
    const facts = [];
    if (typeof info.arch === "string" && info.arch) facts.push("linux/" + info.arch);
    const built = biDate(info.created, false);
    if (built) facts.push("built " + built);
    if (typeof info.claude_code === "string" && info.claude_code) facts.push("Claude Code " + info.claude_code);
    if (facts.length) rows.push({ label: "", text: facts.join(" · "), muted: true });
  } else if (typeof r.configured === "string" && r.configured) {
    rows.push(biDigestRow("Box image", r.configured, false));
  }
  const status = biStatus(r);
  if (status) rows.push({ label: "", text: status });
  if (r.verdict === "update") rows.push(...biUpdateRows(r));
  // Docker not answering hides the comparison, not what this version's image is.
  else if (r.verdict === "unknown") rows.push(...biWantRows(r));
  const scan = biScanRow(r.scan);
  if (scan) rows.push(scan);
  return rows;
}

function biEl(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text) n.textContent = text;
  return n;
}

function biValue(row) {
  if (row.href) {
    const a = biEl("a", "bi-link", row.text);
    a.setAttribute("href", row.href);
    a.setAttribute("rel", "noopener");
    a.onclick = (e) => {
      e.preventDefault();
      openAgentLink(row.href, { source: "settings" });
    };
    return a;
  }
  const v = biEl("span", row.code ? "bi-code" : "bi-text" + (row.fine ? " bi-fine" : row.muted ? " bi-muted" : ""), row.text);
  if (row.title) v.setAttribute("title", row.title);
  return v;
}

function biRow(row) {
  const div = biEl("div", "bi-row");
  div.appendChild(biEl("span", "bi-l", row.label));
  const val = biEl("span", "bi-v");
  val.appendChild(biValue(row));
  if (row.note) val.appendChild(biEl("span", "bi-note", row.note));
  if (row.copy) {
    const b = biEl("button", "btn btn-sm bi-copy", "Copy");
    b.setAttribute("type", "button");
    b.setAttribute("aria-label", "Copy the full value");
    b.onclick = () => copyText(row.copy, (ok) => {
      if (typeof showToast === "function") showToast(ok ? "Copied" : "Could not copy");
    });
    val.appendChild(b);
  }
  div.appendChild(val);
  return div;
}

// A row with a label starts a group (Box image, For this version, Weekly scan); the unlabelled rows after it belong to it.
function groupRows(rows) {
  const groups = [];
  for (const row of rows) {
    if (row.label || !groups.length) groups.push([row]);
    else groups[groups.length - 1].push(row);
  }
  return groups;
}

function biGroup(rows) {
  const g = biEl("div", "bi-group");
  rows.forEach((row) => g.appendChild(biRow(row)));
  return g;
}

// loadBoxImage fills #about-image. The container is shown only after a report arrived: a CuliBox with no hook (404), a
// network failure or an answer that is not a report leaves it hidden and empty, so About looks as it did before.
// A phone never shows the rows (app.css hides them), so it does not ask; an environment with no matchMedia asks.
// A slower, older answer never overwrites a newer one (the tab can be opened twice quickly).
let biSeq = 0;
async function loadBoxImage() {
  const el = document.getElementById("about-image");
  if (!el) return;
  if (typeof window !== "undefined" && typeof window.matchMedia === "function" && !window.matchMedia("(min-width: 701px)").matches) return;
  const seq = ++biSeq;
  let rows = [];
  try {
    rows = boxImageRows(await api("/api/box-image"));
  } catch (_) {
    rows = [];
  }
  if (seq !== biSeq) return;
  el.replaceChildren(...groupRows(rows).map(biGroup));
  el.hidden = rows.length === 0;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { boxImageRows, boxImageRepoDigest, shortDigest };
}
