"use strict";

// "Kept from boxes" on the profile page (desktop only: the page itself is): the folders a boxed
// agent wrote outside the repo that CuliBox keeps for this profile. The model and the toast part
// are pure; the drawing and the fetches follow. Every string that came from a box (a path, a
// reason, a name) goes on the page as a text node, never as markup: this file has no innerHTML.
// Top-level names are kept-prefixed: the page scripts share one scope. Classic script:
// keptBoxId (fsops.js), api, showToast, askConfirm, dockOpenFile are page globals.

const KEPT_TITLE = "Kept from boxes";
const KEPT_NOTE = "Folders your boxed agents wrote outside the repo. Host sessions use your real home and don't see these.";
const KEPT_SHOWN = 3; // not-kept rows shown before "N more"
const KEPT_MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const KEPT_SEP = " · ";

const keptHome = (p) => "~/" + p;

// keptSize: 12288 -> "12 KB", 1536 -> "1.5 KB", 900 -> "900 B". "" for a number that is not one.
function keptSize(n) {
  if (typeof n !== "number" || !isFinite(n) || n < 0) return "";
  if (n < 1024) return Math.round(n) + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  const s = v < 10 ? String(Math.round(v * 10) / 10) : String(Math.round(v));
  return s + " " + units[i];
}

function keptFiles(n) {
  if (typeof n !== "number" || !isFinite(n) || n < 0) return "";
  return n + (n === 1 ? " file" : " files");
}

// keptDay: "27 Nov" in the viewer's own zone, "" for a time that is not one.
function keptDay(iso) {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  return d.getDate() + " " + KEPT_MONTHS[d.getMonth()];
}

// keptClashRel: "clash/<box>/<rel>" -> rel, "" for any other shape.
function keptClashRel(p) {
  const m = /^clash\/[^/]+\/(.+)$/.exec(typeof p === "string" ? p : "");
  return m ? m[1] : "";
}

// keptSectionModel: what the section shows, from GET /api/profiles/{name}/kept's `stores`, or null
// when there is nothing to say (the section is then not drawn).
// rows: one per kept folder {key, path, sizeText, filesText, keptText, status, reason, line}.
// notKept: {shown, hidden, moreText}: folders CuliBox left in the box's own home, with why.
// clashes: one per folder that has older copies {key, rel, paths, text}.
function keptSectionModel(stores) {
  if (!Array.isArray(stores)) return null;
  const rows = [];
  const nk = [];
  const clashes = [];
  for (const s of stores) {
    if (!s || typeof s !== "object" || typeof s.key !== "string") continue;
    for (const e of Array.isArray(s.kept) ? s.kept : []) {
      if (!e || typeof e.path !== "string" || e.path === "") continue;
      const sizeText = keptSize(e.bytes);
      const filesText = keptFiles(e.files);
      const day = keptDay(e.kept_at);
      const keptText = day ? "kept " + day : "";
      rows.push({
        key: s.key, path: e.path, sizeText, filesText, keptText,
        status: e.status === "held" ? "held" : "kept",
        reason: e.status === "held" && typeof e.held_reason === "string" ? e.held_reason : "",
        line: [keptHome(e.path), sizeText, filesText, keptText].filter(Boolean).join(KEPT_SEP),
      });
    }
    for (const r of Array.isArray(s.not_kept) ? s.not_kept : []) {
      if (!r || typeof r.path !== "string" || r.path === "") continue;
      const reason = typeof r.reason === "string" ? r.reason : "";
      nk.push({ key: s.key, path: r.path, reason, line: [keptHome(r.path), reason].filter(Boolean).join(KEPT_SEP) });
    }
    const byRel = new Map();
    for (const c of Array.isArray(s.clashes) ? s.clashes : []) {
      const rel = keptClashRel(c);
      if (!rel) continue;
      if (!byRel.has(rel)) byRel.set(rel, []);
      byRel.get(rel).push(c);
    }
    for (const [rel, paths] of byRel) {
      clashes.push({ key: s.key, rel, paths, text: paths.length + (paths.length === 1 ? " older copy of " : " older copies of ") + keptHome(rel) });
    }
  }
  if (!rows.length && !nk.length && !clashes.length) return null;
  const hidden = nk.slice(KEPT_SHOWN);
  return {
    title: KEPT_TITLE,
    note: KEPT_NOTE,
    rows,
    notKept: { shown: nk.slice(0, KEPT_SHOWN), hidden, moreText: hidden.length ? hidden.length + " more" : "" },
    clashes,
  };
}

// keptToastPart: the spawn toast's part for folders kept since the last spawn, "" for none.
function keptToastPart(paths) {
  const ps = (Array.isArray(paths) ? paths : []).filter((p) => typeof p === "string" && p !== "");
  return ps.length ? "now keeping " + ps.map(keptHome).join(", ") : "";
}

// keptForgetAsk: the confirm for Forget.
function keptForgetAsk(path) {
  return {
    title: "Forget kept folder",
    body: "Delete the kept copy of " + keptHome(path) + "? Boxes won't keep it again.",
    confirmLabel: "Delete",
    danger: true,
  };
}

// keptForgetClashAsk: the confirm for Forget on an older-copies row. Only the copies go.
function keptForgetClashAsk(rel) {
  return {
    title: "Forget older copies",
    body: "Delete the older copies of " + keptHome(rel) + "? The kept folder stays.",
    confirmLabel: "Delete",
    danger: true,
  };
}

// --- drawing -------------------------------------------------------------------------------------

function keptMk(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function keptBtn(label, aria, onclick) {
  const b = keptMk("button", "pfv-link", label);
  b.type = "button";
  b.setAttribute("aria-label", aria);
  b.addEventListener("click", onclick);
  return b;
}

// keptDrawSection: the section for a model. hooks: onView(row, rowEl) and onForget(row, btn) for a
// kept row, onViewClash(clash, rowEl) and onForgetClash(clash, btn) for a clash row. Pure drawing:
// no fetch here.
function keptDrawSection(model, hooks) {
  const h = hooks || {};
  const sec = keptMk("div", "pfv-kept-sec");
  sec.append(keptMk("h2", "pfv-sheet-h", model.title), keptMk("p", "pfv-kept-note", model.note));
  const card = keptMk("div", "pfv-sheet");
  for (const r of model.rows) {
    const row = keptMk("div", "pfv-kept-row");
    const line = keptMk("div", "pfv-kept-main");
    line.append(keptMk("span", "pfv-kept-line", r.line));
    if (r.status === "held") {
      line.append(keptMk("span", "pfv-kept-tag", "held"));
      if (r.reason) line.append(keptMk("span", "pfv-kept-why", r.reason));
    }
    const acts = keptMk("span", "pfv-kept-acts");
    const forgetBtn = keptBtn("Forget", "Forget " + keptHome(r.path), () => h.onForget && h.onForget(r, forgetBtn));
    acts.append(
      keptBtn("View", "View " + keptHome(r.path), () => h.onView && h.onView(r, row)),
      forgetBtn,
    );
    line.append(acts);
    row.append(line);
    card.append(row);
  }
  for (const c of model.clashes) {
    const row = keptMk("div", "pfv-kept-row");
    const line = keptMk("div", "pfv-kept-main");
    line.append(keptMk("span", "pfv-kept-line", c.text));
    const acts = keptMk("span", "pfv-kept-acts");
    const forgetBtn = keptBtn("Forget", "Forget " + c.text, () => h.onForgetClash && h.onForgetClash(c, forgetBtn));
    acts.append(keptBtn("View", "View " + c.text, () => h.onViewClash && h.onViewClash(c, row)), forgetBtn);
    line.append(acts);
    row.append(line);
    card.append(row);
  }
  const nk = model.notKept;
  if (nk.shown.length) {
    const box = keptMk("div", "pfv-kept-nk");
    box.append(keptMk("div", "pfv-kept-nkh", "Not kept"));
    const addRow = (r) => { const d = keptMk("div", "pfv-kept-nkrow", r.line); box.append(d); return d; };
    for (const r of nk.shown) addRow(r);
    if (nk.moreText) {
      const more = keptBtn(nk.moreText, nk.moreText + " not kept", () => {
        for (const r of nk.hidden) addRow(r);
        more.remove();
      });
      box.append(more);
    }
    card.append(box);
  }
  sec.append(card);
  return sec;
}

// --- the page: fetch, view, forget ---------------------------------------------------------------

const keptState = { name: "", stores: null };

// keptHost: the section's container for the page being drawn. Filled from what was last fetched
// for that profile, empty (hidden) until then.
function keptHost(name) {
  const host = keptMk("div", "pfv-kept");
  host.id = "pfv-kept";
  host.dataset.profile = name;
  keptPaint(host);
  return host;
}

function keptPaint(host) {
  host.replaceChildren();
  const name = host.dataset.profile;
  if (keptState.name !== name) return;
  const model = keptSectionModel(keptState.stores);
  if (!model) return;
  host.append(keptDrawSection(model, {
    onView: (r, rowEl) => keptToggleTree(name, r.key, [r.path], rowEl),
    onViewClash: (c, rowEl) => keptToggleTree(name, c.key, c.paths, rowEl),
    onForget: (r, btn) => keptForget(name, r, btn),
    onForgetClash: (c, btn) => keptForget(name, { key: c.key, path: c.rel }, btn, true),
  }));
}

function keptRepaint() {
  const host = document.getElementById("pfv-kept");
  if (host) keptPaint(host);
}

// keptReload: fetch the profile's kept stores and redraw the section. A failure leaves what was
// there (the section is secondary; the page must not say "load failed" for it).
let keptSeq = 0;
async function keptReload(name) {
  if (!name) return;
  const seq = ++keptSeq;
  let d;
  try { d = await api("/api/profiles/" + encodeURIComponent(name) + "/kept"); } catch (_) { return; }
  if (seq !== keptSeq) return; // a newer reload is the answer
  const stores = d && Array.isArray(d.stores) ? d.stores : [];
  keptState.name = name;
  keptState.stores = stores;
  keptRepaint();
}

// keptForget: Forget for a kept row, or with clash for an older-copies row (r.path is then the kept
// folder whose older copies go; the folder itself stays).
async function keptForget(name, r, btn, clash) {
  if (!(await askConfirm(clash ? keptForgetClashAsk(r.path) : keptForgetAsk(r.path)))) return;
  const label = btn ? btn.textContent : "";
  if (btn) { btn.disabled = true; btn.textContent = "Forgetting..."; }
  try {
    await api("/api/profiles/" + encodeURIComponent(name) + "/kept/forget", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(clash ? { key: r.key, path: r.path, clash: true } : { key: r.key, path: r.path }),
    });
  } catch (e) {
    if (btn) { btn.disabled = false; btn.textContent = label; }
    showToast(e && e.message ? e.message : "Could not forget it", { error: true });
    return;
  }
  await keptReload(name);
}

// keptToggleTree: a read-only listing under a row. The folders list on demand (GET .../kept/list);
// a file opens in the same read-only viewer the Claude scratch folder uses, through keptBoxId.
function keptToggleTree(name, key, roots, rowEl) {
  const old = rowEl.querySelector ? rowEl.querySelector(".pfv-kept-tree") : null;
  if (old) { old.remove(); return; }
  const tree = keptMk("ul", "pfv-kept-tree");
  rowEl.append(tree);
  for (const p of roots) tree.append(keptTreeItem(name, key, p, p, true));
}

function keptTreeItem(name, key, path, label, dir) {
  const li = keptMk("li", "pfv-kept-item");
  if (!dir) {
    li.append(keptBtn(label, "Open " + label, () => dockOpenFile(null, path, { box: keptBoxId(name, key) })));
    return li;
  }
  let sub = null;
  const b = keptBtn(label + "/", "Show " + label, async () => {
    if (sub) { sub.remove(); sub = null; return; }
    const mine = sub = keptMk("ul", "pfv-kept-tree");
    li.append(mine);
    let d;
    try {
      d = await api("/api/profiles/" + encodeURIComponent(name) + "/kept/list?key=" + encodeURIComponent(key) + "&path=" + encodeURIComponent(path));
    } catch (e) {
      if (sub === mine) mine.append(keptMk("li", "pfv-kept-item", e && e.message ? e.message : "Could not list it"));
      return;
    }
    if (sub !== mine) return; // closed, or closed and opened again, while the reply was on its way
    for (const e of Array.isArray(d.entries) ? d.entries : []) {
      if (!e || typeof e.name !== "string") continue;
      const child = path + "/" + e.name;
      if (e.link) mine.append(keptMk("li", "pfv-kept-item", e.name + " (link)"));
      else mine.append(keptTreeItem(name, key, child, e.name, e.is_dir === true));
    }
    if (d.truncated) mine.append(keptMk("li", "pfv-kept-item", "More not shown"));
  });
  li.append(b);
  return li;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { keptSectionModel, keptToastPart, keptForgetAsk, keptForgetClashAsk, keptDrawSection };
}
