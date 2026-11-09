"use strict";

// The system banner (UI plan 7, #177; DD§A8 M8.6/6b/6c, §B1): at most ONE banner at the top of the sidebar, under
// #side-top, for the first of signed out, can't reach, paused, updated (R19). What it says and when is
// sysBannerModel (notifymodel.js, pure); this file only paints it. The store banner (Task 9) has its own slot
// below this one and signed out hides it; nothing of it is drawn here.
//
// Desktop only: nothing is built while mobileActive(); crossing to a phone width clears it. The banner is built
// once per kind (the model's key) and only its body text moves between polls (the age), so a screen reader
// hears a banner once, not every 3 s. Every string goes in as a text node.
//
// It also marks the list: while the model says `stale` (signed out or can't reach) #sessions wears
// `sess-stale` and the CSS draws hollow dots, muted names and no amber (R17). Groups and order are untouched.
//
// The collapsed rail has no room for a banner: it shows ONE 32 px marker above its list (sysRailMarkerModel /
// sysRailMarkerEl, called by renderRail48), named and titled with the banner's title, that opens the sidebar.

let sysBannerPaintedKey = null;
let sysBannerBody = null;
let sysBannerRailKey = null;

// The banner `sys` says now, or null. Reads app.js's `sys` and bugreport.js's versionLabel at call time.
function sysBannerCurrent() {
  if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) return null;
  return sysBannerModel(sys, Date.now(), typeof versionLabel === "function" ? versionLabel : null);
}

function sysBannerAct(id) {
  if (id === "retry") refreshSessions();
  else if (id === "signin") window.location.href = "/login?reason=expired";
  else if (id === "reload") window.location.reload(); // only ever from the button, never automatic
}

function sysBannerBuild(model) {
  const root = document.createElement("div");
  root.className = "banner";
  root.setAttribute("role", "status");
  root.setAttribute("aria-live", "polite");
  const title = document.createElement("div");
  title.className = "banner-title";
  if (model.dot) {
    const dot = document.createElement("span");
    dot.className = "dot dot-detached";
    dot.setAttribute("aria-hidden", "true");
    title.appendChild(dot);
  }
  title.appendChild(document.createTextNode(model.title));
  root.appendChild(title);
  sysBannerBody = null;
  if (model.body) {
    const p = document.createElement("p");
    p.className = "t-sm muted";
    p.setAttribute("aria-live", "off"); // the age ticks; only the title is announced
    sysBannerBody = document.createTextNode(model.body);
    p.appendChild(sysBannerBody);
    root.appendChild(p);
  }
  if (model.actions.length || model.dismiss) {
    const acts = document.createElement("div");
    acts.className = "banner-acts";
    for (const a of model.actions) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "btn btn-sm" + (a.primary ? " btn-primary" : "");
      b.appendChild(document.createTextNode(a.label));
      b.onclick = () => sysBannerAct(a.id);
      acts.appendChild(b);
    }
    if (model.dismiss) {
      const x = document.createElement("button");
      x.type = "button";
      x.className = "btn btn-ghost btn-icon btn-sm";
      x.setAttribute("aria-label", "Hide until the next update");
      x.title = "Hide until the next update";
      x.appendChild(document.createTextNode("✕"));
      x.onclick = () => { sys.dismissedVersion = sys.version || ""; sysBannerSync(); };
      acts.appendChild(x);
    }
    root.appendChild(acts);
  }
  return root;
}

// sysBannerSync: cheap (one model call), run on every session poll, the 30 s tick, a 401, a visibility change.
function sysBannerSync() {
  const slot = document.getElementById("sys-banner-slot");
  if (!slot) return;
  const model = sysBannerCurrent();
  const list = document.getElementById("sessions");
  const stale = !!(model && model.stale);
  if (list) list.classList.toggle("sess-stale", stale);
  // The collapsed rail's dots go stale with the list (R17): same class, same toggle.
  const rail = document.getElementById("rail48");
  if (rail) rail.classList.toggle("sess-stale", stale);
  const key = model ? model.key : null;
  if (key !== sysBannerPaintedKey) {
    sysBannerPaintedKey = key;
    if (model) slot.replaceChildren(sysBannerBuild(model));
    else { slot.replaceChildren(); sysBannerBody = null; }
    slot.hidden = !model;
  } else if (model && sysBannerBody && sysBannerBody.textContent !== model.body) {
    sysBannerBody.textContent = model.body;
  }
  // The rail's marker follows the same model (and, later, the store's); renderRail48 skips an unchanged rail.
  const rk = key;
  if (rk !== sysBannerRailKey) {
    sysBannerRailKey = rk;
    if (typeof renderRail48 === "function") renderRail48();
  }
}

// The rail marker's model: the first banner in display order (the system one; the store banner is Task 9's input).
function sysRailMarkerModel() {
  return railMarkerModel(sysBannerCurrent(), null);
}

function sysRailMarkerEl(marker) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "ri ri-banner";
  b.setAttribute("aria-label", marker.title);
  b.title = marker.title;
  const d = document.createElement("span");
  d.className = marker.tone === "amber" ? "dot dot-needs" : "dot dot-detached";
  d.setAttribute("aria-hidden", "true");
  b.appendChild(d);
  b.onclick = () => toggleSidebar();
  return b;
}
